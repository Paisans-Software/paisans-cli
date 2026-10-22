package config_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// stubSSH is an ssh config read through a stub `ssh -G`, never the real one:
// hosts maps a Host alias to the HostName, User and Port it sets (User or
// Port empty for none). Any other host resolves to itself, port 22, as ssh
// resolves a host no Host block names. Its local user is "localme". calls
// records every argv the stub was given.
type stubSSH struct {
	hosts map[string][3]string
	calls [][]string
}

func (s *stubSSH) config() config.SSHConfig {
	return config.SSHConfig{LocalUser: "localme", G: func(args []string) (string, error) {
		s.calls = append(s.calls, args)
		var user, port string
		for i := 0; i < len(args)-1; i++ {
			switch args[i] {
			case "-l":
				user = args[i+1]
			case "-p":
				port = args[i+1]
			}
		}
		if len(args) < 2 || args[len(args)-2] != "--" {
			return "", errors.New("stub: the host is not after --")
		}
		host := args[len(args)-1]
		name, cfgUser, cfgPort := strings.ToLower(host), "", ""
		if h, ok := s.hosts[host]; ok {
			name, cfgUser, cfgPort = h[0], h[1], h[2]
		}
		if user == "" {
			user = cfgUser
		}
		if user == "" {
			user = "localme"
		}
		if port == "" {
			port = cfgPort
		}
		if port == "" {
			port = "22"
		}
		return fmt.Sprintf("user %s\nhostname %s\nport %s\nproxyjump bastion\nidentityfile ~/.ssh/id_ed25519\n", user, name, port), nil
	}}
}

func aliases() *stubSSH {
	return &stubSSH{hosts: map[string][3]string{
		"myalias": {"203.0.113.9", "admin", "2222"},
		"nouser":  {"203.0.113.10", "", ""},
		"local":   {"203.0.113.11", "localme", ""},
		"v6":      {"2001:db8::1", "admin", ""},
		"badname": {"bad host", "admin", ""},
	}}
}

// Every form ssh takes resolves through the ssh config: an alias alone,
// with a user, with a port, or both, a user or port written overriding the
// config's, as it does for ssh.
func TestResolveDestinationReadsAliases(t *testing.T) {
	for in, want := range map[string]config.Destination{
		"myalias":           {User: "admin", Host: "203.0.113.9", Port: 2222, Alias: "myalias"},
		"root@myalias":      {User: "root", Host: "203.0.113.9", Port: 2222, Alias: "myalias"},
		"myalias:2200":      {User: "admin", Host: "203.0.113.9", Port: 2200, Alias: "myalias"},
		"root@myalias:2200": {User: "root", Host: "203.0.113.9", Port: 2200, Alias: "myalias"},
		"admin@nouser":      {User: "admin", Host: "203.0.113.10", Port: 22, Alias: "nouser"},
		"v6":                {User: "admin", Host: "2001:db8::1", Port: 22, Alias: "v6"},
	} {
		got, err := config.ResolveDestination(in, aliases().config())
		if err != nil || got != want {
			t.Errorf("%s: got %+v, %v; want %+v", in, got, err, want)
		}
	}
	d, _ := config.ResolveDestination("myalias", aliases().config())
	if d.String() != "myalias -> admin@203.0.113.9:2222" || d.Arg() != "admin@myalias:2222" {
		t.Errorf("shown as %q, given again as %q", d.String(), d.Arg())
	}
}

// What ssh -G is asked is what plain ssh would be given: the user and port
// written as -l and -p, and the host after --.
func TestResolveDestinationAsksSSHWithTheWrittenUserAndPort(t *testing.T) {
	s := aliases()
	if _, err := config.ResolveDestination("root@myalias:2200", s.config()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"-l", "root", "-p", "2200", "--", "myalias"}; len(s.calls) != 1 || !slices.Equal(s.calls[0], want) {
		t.Errorf("ssh -G got %q, want %q", s.calls, want)
	}
}

// user@host[:port] and user@[v6]:port read exactly as ParseDestination reads
// them: a host no ssh config renames keeps the user and port written.
func TestResolveDestinationKeepsTheExistingForms(t *testing.T) {
	for _, in := range []string{"admin@203.0.113.9", "admin@203.0.113.9:2222", "admin@[2001:db8::1]:22",
		"admin@host.example.org", "admin@[2001:db8::1]", "admin@HOST.example.org:2200"} {
		want, err := config.ParseDestination(in)
		if err != nil {
			t.Fatal(err)
		}
		got, err := config.ResolveDestination(in, aliases().config())
		if err != nil || got != want || got.Alias != "" {
			t.Errorf("%s: got %+v, %v; want %+v", in, got, err, want)
		}
	}
}

// A destination with no user, whose ssh config gives none, is refused
// rather than reached as the operator's local name, which is what ssh -G
// reports when no User applies.
func TestResolveDestinationRefusesNoUser(t *testing.T) {
	for _, in := range []string{"nouser", "nouser:2200", "local", "203.0.113.9", "host.example.org"} {
		_, err := config.ResolveDestination(in, aliases().config())
		if err == nil || !strings.Contains(err.Error(), "names no user") || !strings.Contains(err.Error(), "Eg: admin@"+in) {
			t.Errorf("%s: %v, want the refusal of no user", in, err)
		}
	}
	_, err := config.ResolveDestination("nouser", aliases().config())
	if !strings.Contains(err.Error(), "nouser -> 203.0.113.10") {
		t.Errorf("the refusal does not show what the alias resolved to: %v", err)
	}
}

// A destination that could be read as an option of ssh -G, or that is not a
// destination in any form, is refused before ssh is run.
func TestResolveDestinationRefusesBeforeRunningSSH(t *testing.T) {
	for _, bad := range []string{"-oProxyCommand=x", "-G", "admin@-oProxyCommand=x", "-l@h", "", "@host", "admin@",
		"admin@h:0", "admin@h:x", "Admin@h", "a b@h", "admin@a@b", "my alias", "host*", "a;b", "%h",
		"admin@2001:db8::5:2222", "admin@[nothost]:22", "admin@[203.0.113.9]"} {
		s := aliases()
		if got, err := config.ResolveDestination(bad, s.config()); err == nil {
			t.Errorf("%q: accepted as %+v", bad, got)
		}
		if len(s.calls) > 0 {
			t.Errorf("%q: ssh -G was run: %q", bad, s.calls)
		}
	}
	_, err := config.ResolveDestination("my alias", aliases().config())
	if err == nil || !strings.Contains(err.Error(), "alias of your ssh config") {
		t.Errorf("the refusal does not mention aliases: %v", err)
	}
}

// What ssh config resolves to is held to the same rules as what is typed,
// and a failing ssh -G is said, not taken for a host.
func TestResolveDestinationRefusesWhatSSHResolvesBadly(t *testing.T) {
	if _, err := config.ResolveDestination("badname", aliases().config()); err == nil || !strings.Contains(err.Error(), "HostName") {
		t.Errorf("a HostName with a space: %v", err)
	}
	broken := config.SSHConfig{LocalUser: "localme", G: func([]string) (string, error) { return "", errors.New("ssh -G: line 3: Bad configuration option") }}
	if _, err := config.ResolveDestination("myalias", broken); err == nil || !strings.Contains(err.Error(), "Bad configuration option") {
		t.Errorf("ssh -G failing: %v", err)
	}
}

// An alias and the host it resolves to are one login.
func TestDestinationSame(t *testing.T) {
	alias := config.Destination{User: "admin", Host: "203.0.113.9", Port: 22, Alias: "home-a"}
	for _, o := range []config.Destination{
		{User: "admin", Host: "203.0.113.9", Port: 22},
		{User: "admin", Host: "home-a", Port: 22},
		{User: "admin", Host: "HOME-A", Port: 22},
	} {
		if !alias.Same(o) || !o.Same(alias) {
			t.Errorf("%+v is not the same as %+v", o, alias)
		}
	}
	for _, o := range []config.Destination{
		{User: "root", Host: "203.0.113.9", Port: 22},
		{User: "admin", Host: "203.0.113.9", Port: 2222},
		{User: "admin", Host: "home-b", Port: 22},
	} {
		if alias.Same(o) {
			t.Errorf("%+v is the same as %+v", o, alias)
		}
	}
}
