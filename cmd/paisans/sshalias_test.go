package main

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// stubAlias is one Host block of a stub ssh config: its HostName, and the
// User and Port it sets, empty for none.
type stubAlias struct{ hostname, user, port string }

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
		return fmt.Sprintf("user %s\nhostname %s\nport %s\n", a.user, a.hostname, a.port), nil
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
	}
	operatorSSHConfig = func() config.SSHConfig { return stubSSHConfig(aliases) }
}

// An alias is given to ssh as the alias, with the user and port ssh -G
// reported, so the connection takes the alias's ProxyJump, IdentityFile and
// the rest of its ssh config; an address is given as before.
func TestADestinationGivesSSHTheAlias(t *testing.T) {
	withSSHAliases(t)
	for in, want := range map[string]string{
		"myalias":                   "ssh://admin@myalias:2222",
		"root@myalias":              "ssh://root@myalias:2222",
		"myalias:2200":              "ssh://admin@myalias:2200",
		"root@other:22":             "ssh://root@other:22",
		"other":                     "ssh://" + stubLocalUser + "@other:22",
		"ubuntu@192.0.2.10":         "ubuntu@192.0.2.10",
		"ubuntu@192.0.2.10:2222":    "ssh://ubuntu@192.0.2.10:2222",
		"ubuntu@[2001:db8::1]:2222": "ssh://ubuntu@[2001:db8::1]:2222",
	} {
		dest, err := parseDestination(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if tr, ok := reachDestination(dest).(apply.SSHTransport); !ok || tr.Destination != want {
			t.Errorf("%s: reached as %+v, want %s", in, tr, want)
		}
		if owner := ownerTransport(dest, true); owner.Destination != want {
			t.Errorf("%s: owner reached as %s, want %s", in, owner.Destination, want)
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
	plain, _ := parseDestination("admin@192.0.2.10")
	if tr := ownerTransport(plain, true); tr.KnownHost != "" {
		t.Errorf("a plain host's KnownHost = %q, want none", tr.KnownHost)
	}
}

// site admit and evict with --site take --ssh as a login for the declared
// host, or as an alias: a word ssh config maps to another HostName, or one
// with a port. A login alone keeps the Port a declared alias's ssh config
// gives it unless the declared port is not 22.
func TestOwnerDestinationTellsAnAliasFromALogin(t *testing.T) {
	withSSHAliases(t)
	byIP := config.Site{SSH: config.SSH{Host: "203.0.113.20"}}
	byAlias := config.Site{SSH: config.SSH{Host: "myalias"}}
	byAliasPort := config.Site{SSH: config.SSH{Host: "myalias", Port: 2200}}
	byV6 := config.Site{SSH: config.SSH{Host: "2001:db8::1"}}
	for _, c := range []struct {
		ssh      string
		declared config.Site
		want     string
	}{
		{"alice", byIP, "alice@203.0.113.20:22"},
		{"myalias", byIP, "myalias -> admin@203.0.113.9:2222"},
		{"myalias:2200", byIP, "myalias -> admin@203.0.113.9:2200"},
		{"other:22", byIP, "other -> " + stubLocalUser + "@203.0.113.10:22"},
		{"alice@other", byIP, "other -> alice@203.0.113.10:22"},
		{"alice", byAlias, "myalias -> alice@203.0.113.9:2222"},
		{"alice", byAliasPort, "myalias -> alice@203.0.113.9:2200"},
		{"alice", byV6, "alice@[2001:db8::1]:22"},
	} {
		dest, err := ownerDestination("site admit", c.ssh, "home-a", c.declared, strings.NewReader(""), &bytes.Buffer{})
		if err != nil || dest.String() != c.want {
			t.Errorf("--ssh %s on %s: got %s, %v; want %s", c.ssh, c.declared.SSH.Host, dest, err, c.want)
		}
	}
	if _, err := ownerDestination("site evict", "-oProxyCommand=x", "home-a", byIP, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Error("an option was taken as a login")
	}
}

// A host named twice, by its alias and by the address it resolves to, is
// read once.
func TestRecoverReadsAHostNamedTwiceOnce(t *testing.T) {
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
	if err != nil || len(hosts) != 1 || len(reached) != 2 {
		t.Errorf("hosts %v, reached %v, %v: want one host, read once and reached again as the deployment's user", slices.Sorted(maps.Keys(hosts)), reached, err)
	}
}
