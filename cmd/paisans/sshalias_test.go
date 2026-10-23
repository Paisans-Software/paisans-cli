package main

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// stubAlias is one Host block of a stub ssh config: its HostName, and the
// User, Port and HostKeyAlias it sets, empty for none.
type stubAlias struct{ hostname, user, port, hostKeyAlias string }

// stubLocalUser is the operator's local login to a stub ssh -G, which it
// reports as the user of a host no User applies to, as ssh does.
const stubLocalUser = "test-local"

// stubSSHConfig is an ssh config read through a stub ssh -G that knows the
// aliases given: any other host resolves to itself on port 22, as ssh
// resolves a host no Host block names. -l and -p override, as for ssh.
func stubSSHConfig(aliases map[string]stubAlias) config.SSHConfig {
	return config.SSHConfig{G: func(args []string) (string, error) {
		if len(args) < 2 || args[len(args)-2] != "--" {
			return "", errors.New("stub ssh -G: the host is not after --")
		}
		host := args[len(args)-1]
		a, ok := aliases[host]
		if !ok {
			a = stubAlias{hostname: strings.ToLower(host)}
		}
		for i := 0; i < len(args)-2; i++ {
			switch args[i] {
			case "-l":
				a.user = args[i+1]
			case "-p":
				a.port = args[i+1]
			}
		}
		if a.user == "" {
			a.user = stubLocalUser
		}
		if a.port == "" {
			a.port = "22"
		}
		out := fmt.Sprintf("user %s\nhostname %s\nport %s\n", a.user, a.hostname, a.port)
		if a.hostKeyAlias != "" {
			out += "hostkeyalias " + a.hostKeyAlias + "\n"
		}
		return out, nil
	}}
}

// withSSHAliases makes the operator's ssh config, for this test, one that
// defines myalias (admin, port 2222) and other (no User, so ssh -G reports
// the local login).
func withSSHAliases(t *testing.T) {
	t.Helper()
	saved := operatorSSHConfig
	t.Cleanup(func() { operatorSSHConfig = saved })
	aliases := map[string]stubAlias{
		"myalias": {hostname: "203.0.113.9", user: "admin", port: "2222"},
		"other":   {hostname: "203.0.113.10"},
		// A plain host whose config sets its Port.
		"gw.example.org": {hostname: "gw.example.org", port: "2222"},
		// A Host block with no HostName: the word is itself.
		"plainblock": {hostname: "plainblock", user: "admin"},
		// The declared host's own alias, and one with a HostKeyAlias.
		"home-a": {hostname: "203.0.113.20", user: "admin"},
		"keyed":  {hostname: "keyed", user: "admin", hostKeyAlias: "keyed-key"},
	}
	operatorSSHConfig = func() config.SSHConfig { return stubSSHConfig(aliases) }
}

// An alias is given to ssh as the alias, with the user and port ssh -G
// reported, so the connection takes the alias's ProxyJump, IdentityFile and
// the rest of its ssh config; an address is given as before.
func TestADestinationGivesSSHTheAlias(t *testing.T) {
	withSSHAliases(t)
	for in, want := range map[string]string{
		"myalias":                   "-p 2222 -- admin@myalias",
		"root@myalias":              "-p 2222 -- root@myalias",
		"myalias:2200":              "-p 2200 -- admin@myalias",
		"root@other:22":             "-p 22 -- root@other",
		"other":                     "-p 22 -- " + stubLocalUser + "@other",
		"ubuntu@192.0.2.10":         "-p 22 -- ubuntu@192.0.2.10",
		"ubuntu@192.0.2.10:2222":    "-p 2222 -- ubuntu@192.0.2.10",
		"ubuntu@[2001:db8::1]:2222": "-p 2222 -- ubuntu@2001:db8::1",
	} {
		dest, err := parseDestination(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if tr, ok := reachDestination(dest).(apply.SSHTransport); !ok || sshTail(tr) != want {
			t.Errorf("%s: reached as %+v, want %s", in, tr, want)
		}
		if owner := ownerTransport(dest, true); sshTail(owner) != want {
			t.Errorf("%s: owner reached as %s, want %s", in, sshTail(owner), want)
		}
	}
}

// deployment fetch --ssh takes an alias, says what it resolved to, and saves
// that as where each copy came from.
func TestFetchTakesAnSSHAlias(t *testing.T) {
	withSSHAliases(t)
	ownStore(t)
	var reached config.Destination
	saved := reachOwner
	t.Cleanup(func() { reachOwner = saved })
	reachOwner = func(dest config.Destination, sudo bool) apply.Transport {
		reached = dest
		return &storeHost{name: dest.String(), files: map[string]string{registry.Path: `{"deployments":{}}`}}
	}
	var b bytes.Buffer
	savedR := reporterOverride
	reporterOverride = ui.NewPlain(&b, false)
	t.Cleanup(func() { reporterOverride = savedR })
	if err := runDeploymentFetch([]string{"--ssh", "myalias"}); err != nil {
		t.Fatalf("%v\n%s", err, b.String())
	}
	if want := (config.Destination{User: "admin", Host: "203.0.113.9", Port: 2222, Alias: "myalias"}); reached != want {
		t.Errorf("reached %+v, want %+v", reached, want)
	}
	if !strings.Contains(b.String(), "myalias -> admin@203.0.113.9:2222") {
		t.Errorf("the output does not show what the alias resolved to:\n%s", b.String())
	}
}

// deployment recover --from takes aliases, alone and with a user, beside
// the forms it always took.
func TestRecoverTakesSSHAliases(t *testing.T) {
	withSSHAliases(t)
	saved := reachDestination
	t.Cleanup(func() { reachDestination = saved })
	files := ownRecord(t)
	reachDestination = func(dest config.Destination) apply.Transport {
		return &storeHost{name: dest.String(), login: dest.User, files: files}
	}
	var dests []config.Destination
	for _, from := range []string{"myalias", "paisans-f2a9@other"} {
		d, err := parseDestination(from)
		if err != nil {
			t.Fatalf("%s: %v", from, err)
		}
		dests = append(dests, d)
	}
	var b bytes.Buffer
	id, hosts, err := recoverID(ui.NewPlain(&b, false), dests, "")
	want := []string{"other -> paisans-f2a9@203.0.113.10:22", "myalias -> paisans-f2a9@203.0.113.9:2222"}
	slices.Sort(want)
	if got := slices.Sorted(maps.Keys(hosts)); err != nil || id != exampleID || !slices.Equal(got, want) {
		t.Errorf("recoverID = %q, %v, %v; want %v", id, got, err, want)
	}
	if tr, ok := hosts["myalias -> paisans-f2a9@203.0.113.9:2222"].(*storeHost); !ok || tr.login != "paisans-f2a9" {
		t.Errorf("the alias is not reached again as the deployment's user: %+v", hosts)
	}
}

// A destination that could be an ssh option, or is no destination, is
// refused before ssh -G sees it, and the refusal mentions aliases.
func TestDestinationFlagsRefuseOptions(t *testing.T) {
	withSSHAliases(t)
	ownStore(t)
	fetchFrom(t, "", "")
	for in, want := range map[string]string{
		"-oProxyCommand=x": "alias of your ssh config",
		"my alias":         "alias of your ssh config",
		"host*":            "alias of your ssh config",
	} {
		err := runDeploymentFetch([]string{"--ssh", in})
		if text := problemText(err); err == nil || !strings.Contains(text, "--ssh is not a destination") || !strings.Contains(text, want) {
			t.Errorf("%q: %v", in, text)
		}
	}
}

// A site declared by its alias is the site --ssh names by the address the
// alias resolves to, so site remove --force still asks before cleaning a
// site's own host.
func TestADeclaredAliasIsTheSameHost(t *testing.T) {
	withSSHAliases(t)
	dest, err := parseDestination("admin@myalias")
	if err != nil {
		t.Fatal(err)
	}
	declared := config.Site{SSH: config.SSH{User: "admin", Host: "203.0.113.9", Port: 2222}}
	if !declared.Destination().Same(dest) {
		t.Errorf("%s is not the declared %s", dest, declared.Destination())
	}
}

// A transport to an alias names, in a changed host key's advice, the host
// known_hosts records: the HostName it resolved to.
func TestAnAliasTransportKnowsItsKnownHostsName(t *testing.T) {
	withSSHAliases(t)
	dest, err := parseDestination("myalias")
	if err != nil {
		t.Fatal(err)
	}
	if tr := reachDestination(dest).(apply.SSHTransport); tr.KnownHost != "[203.0.113.9]:2222" {
		t.Errorf("reachDestination KnownHost = %q", tr.KnownHost)
	}
	if tr := ownerTransport(dest, true); tr.KnownHost != "[203.0.113.9]:2222" {
		t.Errorf("ownerTransport KnownHost = %q", tr.KnownHost)
	}
	for in, want := range map[string]string{"admin@192.0.2.10": "192.0.2.10", "admin@192.0.2.10:2222": "[192.0.2.10]:2222", "admin@keyed": "keyed-key"} {
		plain, err := parseDestination(in)
		if err != nil {
			t.Fatal(err)
		}
		if tr := ownerTransport(plain, true); tr.KnownHost != want {
			t.Errorf("%s: KnownHost = %q, want %q", in, tr.KnownHost, want)
		}
	}
}

// site admit and evict with --site take --ssh as a login for the declared
// host, on the declared port, or as an alias: user@alias, alias:port, or a
// word alone that ssh config maps to the declared host and port. A word
// mapped to another host is refused rather than guessed, as is a word that
// is no login.
func TestOwnerDestinationTellsAnAliasFromALogin(t *testing.T) {
	withSSHAliases(t)
	byIP := config.Site{SSH: config.SSH{Host: "203.0.113.20"}}
	byAlias := config.Site{SSH: config.SSH{Host: "myalias"}}
	byAliasPort := config.Site{SSH: config.SSH{Host: "myalias", Port: 2222}}
	byV6 := config.Site{SSH: config.SSH{Host: "2001:db8::1"}}
	for _, c := range []struct {
		ssh      string
		declared config.Site
		want     string
	}{
		{"alice", byIP, "alice@203.0.113.20:22"},
		{"home-a", byIP, "home-a -> admin@203.0.113.20:22"},
		{"plainblock", byIP, "plainblock@203.0.113.20:22"},
		{"myalias", byAliasPort, "myalias -> admin@203.0.113.9:2222"},
		{"myalias:2200", byIP, "myalias -> admin@203.0.113.9:2200"},
		{"alice@other", byIP, "other -> alice@203.0.113.10:22"},
		{"alice", byAlias, "myalias -> alice@203.0.113.9:22"},
		{"alice", byAliasPort, "myalias -> alice@203.0.113.9:2222"},
		{"alice", byV6, "alice@[2001:db8::1]:22"},
	} {
		dest, err := ownerDestination("site admit", c.ssh, "home-a", c.declared, strings.NewReader(""), &bytes.Buffer{})
		if err != nil || dest.String() != c.want {
			t.Errorf("--ssh %s on %s: got %s, %v; want %s", c.ssh, c.declared.SSH.Host, dest, err, c.want)
		}
	}
	for _, c := range []struct {
		ssh      string
		declared config.Site
		want     string
	}{
		// myalias is another host, or the declared one on another port.
		{"myalias", byIP, "Write --ssh user@myalias for that alias, or --ssh myalias@203.0.113.20:22"},
		{"myalias", byAlias, "which is not myalias:22"},
		{"gw.example.org", byIP, "not a login"},
		{"-oProxyCommand=x", byIP, "alias of your ssh config"},
	} {
		_, err := ownerDestination("site evict", c.ssh, "home-a", c.declared, strings.NewReader(""), &bytes.Buffer{})
		if text := problemText(err); err == nil || !strings.Contains(text, c.want) {
			t.Errorf("--ssh %s on %s: %q, want %q", c.ssh, c.declared.SSH.Host, text, c.want)
		}
	}
}

// A config that expands short names (Host * with HostName %h.corp.example)
// maps every word to a host. A login meant for the declared host is then
// refused, naming both spellings, never taken as that other host.
func TestOwnerDestinationRefusesAShortNameExpansion(t *testing.T) {
	saved := operatorSSHConfig
	t.Cleanup(func() { operatorSSHConfig = saved })
	operatorSSHConfig = func() config.SSHConfig {
		return config.SSHConfig{G: func(args []string) (string, error) {
			host, user := args[len(args)-1], stubLocalUser
			for i := 0; i < len(args)-2; i++ {
				if args[i] == "-l" {
					user = args[i+1]
				}
			}
			if !strings.Contains(host, ".") && !strings.Contains(host, ":") {
				host += ".corp.example"
			}
			return fmt.Sprintf("user %s\nhostname %s\nport 22\n", user, host), nil
		}}
	}
	declared := config.Site{SSH: config.SSH{Host: "203.0.113.20"}}
	_, err := ownerDestination("site admit", "admin", "home-a", declared, strings.NewReader(""), &bytes.Buffer{})
	if text := problemText(err); err == nil || !strings.Contains(text, "admin.corp.example") || !strings.Contains(text, "--ssh admin@203.0.113.20:22") {
		t.Errorf("a short name expanded to another host: %q", text)
	}
}

// A login named twice is read once; an alias and the address it resolves to
// stay apart, since the alias may take another route there.
func TestRecoverReadsALoginNamedTwiceOnce(t *testing.T) {
	withSSHAliases(t)
	saved := reachDestination
	t.Cleanup(func() { reachDestination = saved })
	files := ownRecord(t)
	var reached []string
	reachDestination = func(dest config.Destination) apply.Transport {
		reached = append(reached, dest.String())
		return &storeHost{name: dest.String(), login: dest.User, files: files}
	}
	var dests []config.Destination
	for _, from := range []string{"myalias", "admin@203.0.113.9:2222", "myalias"} {
		d, err := parseDestination(from)
		if err != nil {
			t.Fatal(err)
		}
		dests = append(dests, d)
	}
	var b bytes.Buffer
	_, hosts, err := recoverID(ui.NewPlain(&b, false), dests, "")
	want := []string{"admin@203.0.113.9:2222", "myalias -> admin@203.0.113.9:2222"}
	if err != nil || len(hosts) != 2 || !slices.Equal(reached[:2], want) || len(reached) != 4 {
		t.Errorf("hosts %v, reached %v, %v: want the alias and the address, each read once and reached again as the deployment's user", slices.Sorted(maps.Keys(hosts)), reached, err)
	}
}

// A port written goes to ssh ahead of the config's Port, and one not written
// is the config's.
func TestADestinationsPortGoesToSSH(t *testing.T) {
	withSSHAliases(t)
	for in, want := range map[string]string{
		"bob@gw.example.org:22": "-p 22 -- bob@gw.example.org",
		"bob@gw.example.org":    "-p 2222 -- bob@gw.example.org",
	} {
		dest, err := parseDestination(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := sshTail(ownerTransport(dest, false)); got != want {
			t.Errorf("%s: ssh is given %s, want %s", in, got, want)
		}
	}
}

// sshTail is what tr gives ssh after -T and before the command: the port and
// the destination.
func sshTail(tr apply.SSHTransport) string {
	args := tr.SSHArgs(nil, "true")
	i := slices.Index(args, "-T")
	return strings.Join(args[i+1:len(args)-1], " ")
}

// What a destination gives ssh is what ssh itself reads as that login: the
// real ssh -G, with a scratch config standing in for the operator's, reads
// the arguments for an IPv4 and an IPv6 address and an alias, and reports
// the user, host and port the destination names.
func TestRealSSHReadsADestinationsArgs(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh on PATH")
	}
	cfg := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(cfg, []byte("Host myalias\n  HostName 203.0.113.9\n  Port 2200\n  ProxyJump jump.example.org\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		dest                 config.Destination
		user, hostname, port string
	}{
		{config.Destination{User: "admin", Host: "203.0.113.9", Port: 22}, "admin", "203.0.113.9", "22"},
		{config.Destination{User: "admin", Host: "2001:db8::1", Port: 2222}, "admin", "2001:db8::1", "2222"},
		{config.Destination{User: "admin", Host: "2001:db8::1", Port: 22}, "admin", "2001:db8::1", "22"},
		{config.Destination{User: "root", Host: "203.0.113.9", Port: 2222, Alias: "myalias"}, "root", "203.0.113.9", "2222"},
	} {
		args := destinationTransport(c.dest).SSHArgs(nil, "true")
		i := slices.Index(args, "-T")
		g := append([]string{"-F", cfg, "-G"}, args[i+1:len(args)-1]...)
		out, err := exec.Command("ssh", g...).Output()
		if err != nil {
			t.Fatalf("ssh %q: %v", g, err)
		}
		got := map[string]string{}
		for _, line := range strings.Split(string(out), "\n") {
			k, v, _ := strings.Cut(line, " ")
			if _, seen := got[k]; !seen {
				got[k] = v
			}
		}
		if got["user"] != c.user || got["hostname"] != c.hostname || got["port"] != c.port {
			t.Errorf("%s: ssh reads %s@%s port %s, want %s@%s port %s", c.dest, got["user"], got["hostname"], got["port"], c.user, c.hostname, c.port)
		}
		if c.dest.Alias != "" && got["proxyjump"] != "jump.example.org" {
			t.Errorf("%s: the alias's ProxyJump is %q", c.dest, got["proxyjump"])
		}
	}
}
