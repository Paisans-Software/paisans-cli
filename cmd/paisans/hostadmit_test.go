package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// noHost fails the test if anything builds a transport: the early stops
// come before admit reaches any host.
func noHost(t *testing.T) {
	t.Helper()
	quietAdmit(t)
	savedLocal, savedOwner, savedRoot := localTransport, reachOwnerAdmit, runningAsRootOnLinux
	localTransport = func() apply.Transport { t.Fatal("the local transport was built"); return nil }
	reachOwnerAdmit = func(config.Destination) apply.Transport { t.Fatal("the owner's transport was built"); return nil }
	runningAsRootOnLinux = func() bool { return true }
	t.Cleanup(func() { localTransport, reachOwnerAdmit, runningAsRootOnLinux = savedLocal, savedOwner, savedRoot })
}

// admitReport is what the command under test has reported so far.
var admitReport *strings.Builder

// quietAdmit sends the commands' report to a buffer, and keeps their
// unsetting of DOCKER_HOST and DOCKER_CONTEXT to the test.
func quietAdmit(t *testing.T) {
	t.Helper()
	b := new(strings.Builder)
	admitReport = b
	reporterOverride = ui.NewPlain(b, false)
	t.Cleanup(func() { reporterOverride = nil })
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	resetLogins(t)
}

func fixtureCfg(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// siteWith is the fixture's first site, by name, whose roles make
// admission.Needs give want.
func siteWith(t *testing.T, want admission.Grants) string {
	t.Helper()
	cfg := fixtureCfg(t)
	names := make([]string, 0, len(cfg.Sites))
	for name := range cfg.Sites {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if admission.Needs(cfg.Sites[name]) == want {
			return name
		}
	}
	t.Fatalf("the fixture has no site needing %+v", want)
	return ""
}

// dataSiteOf is a fixture site that needs the watchdog and nothing else.
func dataSiteOf(t *testing.T) string { return siteWith(t, admission.Grants{Watchdog: true}) }

// gatewayOf is a fixture site that needs the low ports and nothing else.
func gatewayOf(t *testing.T) string { return siteWith(t, admission.Grants{PublicPorts: true}) }

func fixtureID(t *testing.T) string { return fixtureCfg(t).ID }

// requestFile writes site's request from the fixture to a file, as the
// operator sends it.
func requestFile(t *testing.T, site string) (path, digest string) {
	t.Helper()
	req, err := admission.Build(fixtureCfg(t), site)
	if err != nil {
		t.Fatal(err)
	}
	data := admission.Encode(req)
	path = filepath.Join(t.TempDir(), site+".admit")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, admission.Digest(data)
}

// admitHost is a host as admit's and evict's probes see it: the probe's
// facts as key and value lines, and an answer for each other probe, found
// by a substring of its command. It records every command and runs none.
type admitHost struct {
	facts   [][2]string
	answers map[string]string
	files   map[string]string
	ran     []string
}

func (h *admitHost) Describe() string { return "fake host" }

func (h *admitHost) Run(command string) (string, error) { return h.RunInput(command, "") }

func (h *admitHost) RunInput(command, _ string) (string, error) {
	h.ran = append(h.ran, command)
	if strings.Contains(command, "paisans site admit: read only") {
		var b strings.Builder
		for _, kv := range h.facts {
			fmt.Fprintf(&b, "%s %s\n", kv[0], kv[1])
		}
		return b.String() + "end\n", nil
	}
	keys := make([]string, 0, len(h.answers))
	for k := range h.answers {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		if strings.Contains(command, k) {
			return h.answers[k], nil
		}
	}
	return "", nil
}

func (h *admitHost) ReadFile(path string) (string, bool, error) {
	c, ok := h.files[path]
	return c, ok, nil
}

func (h *admitHost) WriteFile(path, content string, _ uint32) error {
	h.files[path] = content
	return nil
}

// ranAny reports whether any command run starts with prefix, or holds it
// after a shell's && or ;.
func (h *admitHost) ranAny(prefix string) bool {
	for _, c := range h.ran {
		if strings.HasPrefix(c, prefix) || strings.Contains(c, "&& "+prefix) || strings.Contains(c, "; "+prefix) {
			return true
		}
	}
	return false
}

// blankHost is a VPS fresh from Ubuntu 24.04, as hostadmit's own tests
// draw it: no Docker, no user for the deployment, ufw installed and
// active, nothing on 80 or 443. user is the user the request names.
func blankHost(user string) *admitHost {
	h := &admitHost{answers: map[string]string{}, files: map[string]string{
		"/etc/os-release":  "PRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\nNAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nVERSION_CODENAME=noble\nID=ubuntu\nID_LIKE=debian\nUBUNTU_CODENAME=noble\n",
		"/etc/resolv.conf": "nameserver 127.0.0.53\noptions edns0 trust-ad\n",
	}}
	h.facts = [][2]string{
		{"engine", "absent"}, {"pkg", "docker-ce absent"}, {"pkg", "docker.io absent"},
		{"pkg", "docker-ce-rootless-extras absent"}, {"pkg", "uidmap absent"}, {"pkg", "nftables installed"},
		{"setuptool", "absent"}, {"snap", "absent"}, {"system-socket", "absent"},
		{"userns-restrict", "1"}, {"rootlesskit-profile", "loaded"}, {"rootlesskit-shipped", "yes"},
		{"passwd", "absent"}, {"home", "absent 0 /home/" + user}, {"sudo", "absent"},
		{"subuid", "owner:100000:65536"}, {"subgid", "owner:100000:65536"},
		{"br_netfilter", "absent"}, {"iptables-module", "nf_tables loaded"}, {"forward-policy", "ACCEPT"},
		{"port-start", "1024"}, {"ip-forward", "0"},
		{"default-dev", "eth0"}, {"lan", "203.0.113.10/24"}, {"lan6", "fe80::/64 eth0"},
		{"ufw", "installed"}, {"ufw-active", "yes"},
		{"egress-enabled", "no"}, {"egress-table", "absent"},
		{"data-path", "/home"}, {"data-fs", "ext2/ext3"}, {"data-avail", "42949672960"},
		{"dir", "/etc/wireguard absent 0 0"}, {"dir", "/etc/paisans absent 0 0"},
		{"dir", "/srv/paisans absent 0 0"}, {"dir", "/srv/paisans/f2a9 absent 0 0"},
	}
	h.answers = map[string]string{
		"docker compose version":                   "compose absent\npkg nftables install ok installed\npkg ufw install ok installed\nkeyring absent\narch amd64\n",
		"RuntimeWatchdogUSec":                      "device absent\nsystemd 0\ndaemon absent\n",
		"ufw show added":                           "ufw present\nstatus active\n",
		"paisans site admit: as the user":          "daemon inactive\nunit absent\ndaemon-json absent\ncontainers 0\nimages 0\nvolumes 0\nend\n",
		"paisans site admit: who holds 80 and 443": "end\n",
		"is-enabled":                               "enabled \nactive \n",
		"ip -j route":                              "[]",
		"ip -j addr":                               "[]",
		"docker network":                           "[]",
	}
	return h
}

// withFakeLocalHost runs --request against h, a blank host for user, as
// root on Linux.
func withFakeLocalHost(t *testing.T, user string) *admitHost {
	t.Helper()
	quietAdmit(t)
	h := blankHost(user)
	savedLocal, savedRoot := localTransport, runningAsRootOnLinux
	localTransport = func() apply.Transport { return h }
	runningAsRootOnLinux = func() bool { return true }
	t.Cleanup(func() { localTransport, runningAsRootOnLinux = savedLocal, savedRoot })
	return h
}

// withTerminal makes every *os.File stdin read as a terminal, or as none.
func withTerminal(t *testing.T, terminal bool) {
	t.Helper()
	saved := isTerminal
	isTerminal = func(*os.File) bool { return terminal }
	t.Cleanup(func() { isTerminal = saved })
}

// typed is a stdin holding text, as a file, so withTerminal can make it a
// terminal.
func typed(t *testing.T, text string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString(text)
	w.Close()
	t.Cleanup(func() { r.Close() })
	return r
}

// A data site without --watchdog, and a gateway without --public-ports,
// stop admit before any transport exists, from a request and from the
// configuration alike: exit 1, nothing read or changed. A re-run that
// drops --watchdog from an admitted data site stops the same way, since
// the stop never reads the host.
func TestAdmitStopsBeforeReachingTheHost(t *testing.T) {
	data, _ := requestFile(t, dataSiteOf(t))
	gw, _ := requestFile(t, gatewayOf(t))
	for _, args := range [][]string{
		{"--request", data},
		{"--request", data, "--execute"},
		{"--request", gw, "--watchdog"},
		{"--site", dataSiteOf(t), "--ssh", "owner@host.example.org", "--config", fixtureConfig()},
	} {
		noHost(t)
		err := runSiteAdmit(args, strings.NewReader(""), new(strings.Builder))
		if err == nil || !strings.Contains(err.Error(), "Nothing was read or changed") {
			t.Errorf("%q: %v", args, err)
		}
	}
}

func TestAdmitFlagCombinations(t *testing.T) {
	path, _ := requestFile(t, "vm")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{}, "--request <file> on the host, or --site <name> from a workstation"},
		{[]string{"--request", path, "--site", "vm"}, "not both"},
		{[]string{"--request", path, "--ssh", "owner@h"}, "--ssh"},
		{[]string{"--request", path, "extra"}, "takes no argument"},
	} {
		noHost(t)
		if err := runSiteAdmit(c.args, strings.NewReader(""), new(strings.Builder)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.args, err, c.want)
		}
	}
	noHost(t)
	runningAsRootOnLinux = func() bool { return false }
	if err := runSiteAdmit([]string{"--request", path}, strings.NewReader(""), new(strings.Builder)); err == nil || !strings.Contains(err.Error(), "as root on the host") {
		t.Errorf("--request not as root: %v", err)
	}
}

// A request larger than any request is refused before it is parsed.
func TestAdmitRefusesAnOversizedRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.admit")
	os.WriteFile(path, make([]byte, admission.MaxRequestSize+1), 0o644)
	noHost(t)
	if err := runSiteAdmit([]string{"--request", path}, strings.NewReader(""), new(strings.Builder)); err == nil || !strings.Contains(err.Error(), "larger than any request") {
		t.Errorf("err = %v", err)
	}
}

// --execute with --request asks for the digest at a terminal, unless
// --digest gives it; a mistyped one changes nothing; the digest read aloud,
// in another case, spaced, with any 1 and 0 read as l and o, is accepted.
func TestAdmitAsksForTheDigest(t *testing.T) {
	path, digest := requestFile(t, "vm")
	aloud := strings.NewReplacer("-", " ", "1", "l", "0", "o").Replace(strings.ToUpper(digest))
	for _, c := range []struct {
		typed    string
		terminal bool
		executes bool
	}{
		{digest + "\n", false, false},
		{"0000-0000-0000-0000\n", true, false},
		{"\n", true, false},
		{aloud + "\n", true, true},
	} {
		h := withFakeLocalHost(t, "ubuntu")
		withTerminal(t, c.terminal)
		err := runSiteAdmit([]string{"--request", path, "--public-ports", "--execute"}, typed(t, c.typed), new(strings.Builder))
		if executed := h.ranAny("useradd "); executed != c.executes {
			t.Errorf("typed %q at a terminal %v: executed %v (%v)", c.typed, c.terminal, executed, err)
		}
	}
}

// Without --execute admit shows the plan and changes nothing, and asks for
// nothing.
func TestAdmitWithoutExecuteChangesNothing(t *testing.T) {
	path, _ := requestFile(t, "vm")
	h := withFakeLocalHost(t, "ubuntu")
	if err := runSiteAdmit([]string{"--request", path, "--public-ports"}, strings.NewReader(""), new(strings.Builder)); err != nil {
		t.Fatal(err)
	}
	if h.ranAny("useradd ") || h.ranAny("apt-get ") {
		t.Errorf("ran a change: %q", h.ran)
	}
}

// Once Execute is done, admit checks the user's own Docker on both paths;
// with --site it then logs in as the user, as the operators will, which
// records ssh.user where the declaration leaves it out.
func TestAdmitChecksTheUserOnBothPaths(t *testing.T) {
	savedExecute, savedLogin := executeAdmit, reachSiteUser
	t.Cleanup(func() { executeAdmit, reachSiteUser = savedExecute, savedLogin })
	var executed bool
	executeAdmit = func(apply.Transport, hostadmit.Input, *hostadmit.Plan, ui.Reporter) error {
		executed = true
		return nil
	}

	path, digest := requestFile(t, "vm")
	h := withFakeLocalHost(t, "ubuntu")
	withTerminal(t, true)
	reachSiteUser = func(string, config.Site) apply.Transport { t.Fatal("--request logged in as the user"); return nil }
	if err := runSiteAdmit([]string{"--request", path, "--public-ports", "--execute"}, typed(t, digest+"\n"), new(strings.Builder)); err != nil {
		t.Fatal(err)
	}
	if !executed || !strings.Contains(h.ran[len(h.ran)-1], "docker version") {
		t.Errorf("--request: executed %v, last command %q", executed, h.ran[len(h.ran)-1])
	}

	executed = false
	quietAdmit(t)
	owner := blankHost("ubuntu")
	savedOwner := reachOwnerAdmit
	t.Cleanup(func() { reachOwnerAdmit = savedOwner })
	var reached config.Destination
	reachOwnerAdmit = func(d config.Destination) apply.Transport { reached = d; return owner }
	login := blankHost("ubuntu")
	var loggedIn string
	reachSiteUser = func(name string, _ config.Site) apply.Transport { loggedIn = name; return login }
	err := runSiteAdmit([]string{"--site", "vm", "--ssh", "owner@vm.example.org", "--config", fixtureConfig(), "--public-ports", "--execute"}, strings.NewReader(""), new(strings.Builder))
	if err != nil {
		t.Fatal(err)
	}
	if reached.User != "owner" || reached.Host != "vm.example.org" {
		t.Errorf("reached %+v", reached)
	}
	if !executed || !strings.Contains(owner.ran[len(owner.ran)-1], "docker version") {
		t.Errorf("--site: executed %v, last command %q", executed, owner.ran[len(owner.ran)-1])
	}
	if loggedIn != "vm" || len(login.ran) != 1 || !strings.Contains(login.ran[0], "docker version") {
		t.Errorf("--site: logged in to %q, ran %q", loggedIn, login.ran)
	}
}

// evictedHost is a blank host that still holds the deployment's mesh conf,
// so evict plans its removal.
func evictedHost(t *testing.T) *admitHost {
	t.Helper()
	h := withFakeLocalHost(t, "paisans-f2a9")
	h.facts = append(h.facts, [2]string{"file", "/etc/wireguard/psns-f2a9.conf " + strings.Repeat("0", 64) + " 600 0 0"})
	return h
}

// Evict asks for the user's name at a terminal, and refuses anything else.
func TestEvictAsksForTheUserName(t *testing.T) {
	for _, c := range []struct {
		typed    string
		terminal bool
		executes bool
	}{{"paisans-f2a9\n", true, true}, {"paisans-f2a9\n", false, false}, {"yes\n", true, false}, {"\n", true, false}} {
		h := evictedHost(t)
		withTerminal(t, c.terminal)
		err := runSiteEvict([]string{"--deployment", fixtureID(t), "--user", "paisans-f2a9", "--execute"}, typed(t, c.typed), new(strings.Builder))
		if executed := h.ranAny("rm -f "); executed != c.executes {
			t.Errorf("typed %q at a terminal %v: executed %v (%v)", c.typed, c.terminal, executed, err)
		}
	}
}

// Without --execute evict shows the plan and changes nothing.
func TestEvictWithoutExecuteChangesNothing(t *testing.T) {
	h := evictedHost(t)
	if err := runSiteEvict([]string{"--deployment", fixtureID(t), "--user", "paisans-f2a9"}, strings.NewReader(""), new(strings.Builder)); err != nil {
		t.Fatal(err)
	}
	if h.ranAny("rm -f ") {
		t.Errorf("ran a change: %q", h.ran)
	}
}

// evict takes --data-dir to BuildEvict, which checks it as admit does.
func TestEvictPassesDataDir(t *testing.T) {
	evictedHost(t)
	err := runSiteEvict([]string{"--deployment", fixtureID(t), "--user", "paisans-f2a9", "--data-dir", "relative"}, strings.NewReader(""), new(strings.Builder))
	if err == nil || !strings.Contains(err.Error(), "--data-dir") {
		t.Errorf("err = %v", err)
	}
}

func TestEvictFlagCombinations(t *testing.T) {
	path, _ := requestFile(t, "vm")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{}, "give one of --request <file>"},
		{[]string{"--request", path, "--site", "vm", "--ssh", "owner@h"}, "give one of"},
		{[]string{"--deployment", "x"}, "go together"},
		{[]string{"--user", "paisans-f2a9"}, "go together"},
		{[]string{"--site", "vm"}, "--site needs --ssh off a terminal"},
		{[]string{"--request", path, "--ssh", "owner@h"}, "--ssh goes with --site"},
		{[]string{"--request", path, "extra"}, "takes no argument"},
	} {
		noHost(t)
		if err := runSiteEvict(c.args, strings.NewReader(""), new(strings.Builder)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.args, err, c.want)
		}
	}
	noHost(t)
	runningAsRootOnLinux = func() bool { return false }
	if err := runSiteEvict([]string{"--deployment", fixtureID(t), "--user", "paisans-f2a9"}, strings.NewReader(""), new(strings.Builder)); err == nil || !strings.Contains(err.Error(), "as root on the host") {
		t.Errorf("--deployment not as root: %v", err)
	}
}

// writerFunc is an io.Writer that hands each write to a function.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// --execute shows the whole plan, every step's title, on both paths: before
// the digest is asked for with --request, and before anything runs.
func TestAdmitExecuteShowsThePlanFirst(t *testing.T) {
	savedExecute, savedLogin := executeAdmit, reachSiteUser
	t.Cleanup(func() { executeAdmit, reachSiteUser = savedExecute, savedLogin })
	shown := func(when string, plan *hostadmit.Plan) {
		t.Helper()
		if len(plan.Steps) == 0 {
			t.Fatalf("%s: the plan has no steps", when)
		}
		for _, s := range plan.Steps {
			if !strings.Contains(admitReport.String(), s.Title) {
				t.Errorf("%s: step %q not shown:\n%s", when, s.Title, admitReport.String())
			}
		}
	}
	var executed *hostadmit.Plan
	executeAdmit = func(_ apply.Transport, _ hostadmit.Input, plan *hostadmit.Plan, _ ui.Reporter) error {
		shown("before it runs", plan)
		executed = plan
		return nil
	}
	reachSiteUser = func(string, config.Site) apply.Transport { return blankHost("ubuntu") }

	path, digest := requestFile(t, "vm")
	withFakeLocalHost(t, "ubuntu")
	withTerminal(t, true)
	asked := false
	prompt := writerFunc(func(p []byte) (int, error) {
		if !asked {
			asked = true
			if !strings.Contains(admitReport.String(), "start ubuntu's manager after the mesh") {
				t.Errorf("the digest was asked for before the plan was shown:\n%s", admitReport.String())
			}
		}
		return len(p), nil
	})
	if err := runSiteAdmit([]string{"--request", path, "--public-ports", "--execute"}, typed(t, digest+"\n"), prompt); err != nil {
		t.Fatal(err)
	}
	if !asked || executed == nil {
		t.Fatalf("--request: asked %v, executed %v", asked, executed != nil)
	}

	executed = nil
	quietAdmit(t)
	savedOwner := reachOwnerAdmit
	t.Cleanup(func() { reachOwnerAdmit = savedOwner })
	reachOwnerAdmit = func(config.Destination) apply.Transport { return blankHost("ubuntu") }
	if err := runSiteAdmit([]string{"--site", "vm", "--ssh", "owner@vm.example.org", "--config", fixtureConfig(), "--public-ports", "--execute"}, strings.NewReader(""), new(strings.Builder)); err != nil {
		t.Fatal(err)
	}
	if executed == nil {
		t.Fatal("--site: nothing executed")
	}
}

// --execute with --request needs a terminal for the digest, so off one it
// refuses before anything reads the host.
func TestAdmitRefusesExecuteOffATerminalBeforeReadingTheHost(t *testing.T) {
	path, _ := requestFile(t, "vm")
	noHost(t)
	withTerminal(t, false)
	err := runSiteAdmit([]string{"--request", path, "--public-ports", "--execute"}, strings.NewReader(""), new(strings.Builder))
	if err == nil || !strings.Contains(err.Error(), "at a terminal") || !strings.Contains(err.Error(), "Nothing was read or changed") {
		t.Errorf("err = %v", err)
	}
}

// evict --site holds the declaration to what validate holds it to, as
// admit does, before it reaches any host.
func TestEvictSiteValidatesTheDeclaration(t *testing.T) {
	body, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	path, _ := archFixture(t, strings.Replace(string(body), "address: 10.44.0.3", "address: 10.44.0.2", 1))
	noHost(t)
	err = runSiteEvict([]string{"--site", "vm", "--ssh", "owner@vm.example.org", "--config", path}, strings.NewReader(""), new(strings.Builder))
	if err == nil || !strings.Contains(err.Error(), "Nothing was read or changed") {
		t.Errorf("err = %v", err)
	}
}

// With --site, admit's last step logs in as the deployment's user through
// the declaration's own site transport, which records ssh.user where the
// declaration leaves it out; and a declared host that is not --ssh's host
// is named when that login fails, since it may be another machine.
func TestAdmitSiteLogsInThroughTheDeclaration(t *testing.T) {
	savedExecute, savedLogin, savedOwner := executeAdmit, reachSiteUser, reachOwnerAdmit
	t.Cleanup(func() { executeAdmit, reachSiteUser, reachOwnerAdmit = savedExecute, savedLogin, savedOwner })
	executeAdmit = func(apply.Transport, hostadmit.Input, *hostadmit.Plan, ui.Reporter) error { return nil }
	body, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	path, _ := archFixture(t, strings.Replace(string(body), "      host: vm.example.org\n      user: ubuntu\n", "      host: vm.example.org\n", 1))
	quietAdmit(t)
	executing(t)
	reachOwnerAdmit = func(config.Destination) apply.Transport { return blankHost("paisans-f2a9") }
	reachSiteUser = func(name string, site config.Site) apply.Transport {
		tr := siteTransport(name, site, "")
		if tr.Login == nil || tr.Login.Connected == nil {
			t.Fatal("the login records nothing")
		}
		tr.Login.Connected()
		return blankHost("paisans-f2a9")
	}
	if err := runSiteAdmit([]string{"--site", "vm", "--ssh", "owner@vm.example.org", "--config", path, "--public-ports", "--execute"}, strings.NewReader(""), new(strings.Builder)); err != nil {
		t.Fatal(err)
	}
	reread, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reread.Sites["vm"].SSH.User; got != "paisans-f2a9" {
		t.Errorf("vm's ssh.user is %q, want paisans-f2a9", got)
	}

	quietAdmit(t)
	reachSiteUser = func(string, config.Site) apply.Transport { return refusingHost{} }
	err = runSiteAdmit([]string{"--site", "vm", "--ssh", "owner@10.0.0.9", "--config", fixtureConfig(), "--public-ports", "--execute"}, strings.NewReader(""), new(strings.Builder))
	if err == nil || !strings.Contains(err.Error(), "vm.example.org") || !strings.Contains(err.Error(), "10.0.0.9") {
		t.Errorf("a failed login to another host: %v", err)
	}
}

// refusingHost answers every command with a failure.
type refusingHost struct{}

func (refusingHost) Describe() string { return "refusing host" }
func (refusingHost) Run(string) (string, error) {
	return "", fmt.Errorf("ssh: connect to host: Connection refused")
}
func (refusingHost) RunInput(string, string) (string, error) {
	return "", fmt.Errorf("ssh: connect to host: Connection refused")
}
func (refusingHost) ReadFile(string) (string, bool, error)  { return "", false, fmt.Errorf("refused") }
func (refusingHost) WriteFile(string, string, uint32) error { return fmt.Errorf("refused") }

// --site knows the site's host from the declaration, so --ssh may name
// only the owner's login, and without --ssh admit asks for it at a
// terminal.
func TestAdmitSiteTakesTheHostFromTheDeclaration(t *testing.T) {
	for _, c := range []struct {
		name     string
		ssh      []string
		typed    string
		terminal bool
		want     string
	}{
		{"login alone", []string{"--ssh", "owner"}, "", false, "owner@vm.example.org:22"},
		{"asked at a terminal", nil, "owner\n", true, "owner@vm.example.org:22"},
		{"full destination", []string{"--ssh", "admin@10.0.0.9:2222"}, "", false, "admin@10.0.0.9:2222"},
	} {
		quietAdmit(t)
		withTerminal(t, c.terminal)
		savedOwner := reachOwnerAdmit
		var reached config.Destination
		reachOwnerAdmit = func(d config.Destination) apply.Transport { reached = d; return blankHost("ubuntu") }
		args := append([]string{"--site", "vm", "--config", fixtureConfig(), "--public-ports"}, c.ssh...)
		if err := runSiteAdmit(args, typed(t, c.typed), new(strings.Builder)); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if got := fmt.Sprintf("%s@%s:%d", reached.User, reached.Host, reached.Port); got != c.want {
			t.Errorf("%s: reached %s, want %s", c.name, got, c.want)
		}
		reachOwnerAdmit = savedOwner
	}
	// Off a terminal, with no --ssh, there is no one to ask.
	noHost(t)
	withTerminal(t, false)
	err := runSiteAdmit([]string{"--site", "vm", "--config", fixtureConfig(), "--public-ports"}, typed(t, ""), new(strings.Builder))
	if err == nil || !strings.Contains(err.Error(), "--ssh") {
		t.Errorf("no --ssh off a terminal: %v", err)
	}
}

// A grant the site's roles need and the flags leave out is asked for at a
// terminal, with what it gives; a yes grants it, anything else stops admit
// before the host is reached. Off a terminal it stops, as a flag would.
func TestAdmitAsksForAMissingGrantAtATerminal(t *testing.T) {
	data, _ := requestFile(t, dataSiteOf(t))
	for _, c := range []struct {
		typed    string
		terminal bool
		admits   bool
	}{
		{"y\n", true, true},
		{"yes\n", true, true},
		{"n\n", true, false},
		{"\n", true, false},
		{"", true, false},
		{"y\n", false, false},
	} {
		h := withFakeLocalHost(t, "ubuntu")
		withTerminal(t, c.terminal)
		out := new(strings.Builder)
		err := runSiteAdmit([]string{"--request", data}, typed(t, c.typed), out)
		if planned := err == nil; planned != c.admits {
			t.Errorf("typed %q at a terminal %v: planned %v (%v)", c.typed, c.terminal, planned, err)
		}
		if !c.admits && err != nil && !strings.Contains(err.Error(), "Nothing was read or changed") {
			t.Errorf("typed %q: %v", c.typed, err)
		}
		if c.terminal && !strings.Contains(out.String(), "--watchdog") {
			t.Errorf("the question does not name the grant:\n%s", out.String())
		}
		_ = h
	}
	// --site asks the same.
	quietAdmit(t)
	withTerminal(t, true)
	savedOwner := reachOwnerAdmit
	t.Cleanup(func() { reachOwnerAdmit = savedOwner })
	reachOwnerAdmit = func(config.Destination) apply.Transport { return blankHost("ubuntu") }
	if err := runSiteAdmit([]string{"--site", "vm", "--ssh", "owner", "--config", fixtureConfig()}, typed(t, "y\n"), new(strings.Builder)); err != nil {
		t.Errorf("--site asked for --public-ports: %v", err)
	}
}

// inactiveUFW makes h a host whose ufw is installed and not active, with
// sshd listening on 22, systemd-run there and no fallback loaded.
func inactiveUFW(h *admitHost) *admitHost {
	for i, kv := range h.facts {
		if kv[0] == "ufw-active" {
			h.facts[i][1] = "no"
		}
	}
	h.answers["paisans site admit: before enabling ufw"] = `listen LISTEN 0 4096 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=812,fd=3))` + "\nipv6 yes\nsystemd-run present\nfallback not-found not-found\nend\n"
	return h
}

// ranIndex is the index of the first command run that contains s, -1 for
// none.
func (h *admitHost) ranIndex(s string) int {
	for i, c := range h.ran {
		if strings.Contains(c, s) {
			return i
		}
	}
	return -1
}

// Off a terminal, an inactive ufw refuses, naming --enable-ufw and the
// commands that enable it safely; at a terminal admit asks, and only a yes
// plans the enable.
func TestAdmitAsksToEnableUFWAtATerminal(t *testing.T) {
	path, _ := requestFile(t, "vm")
	for _, c := range []struct {
		typed    string
		terminal bool
		plans    bool
	}{
		{"y\n", true, true},
		{"yes\n", true, true},
		{"n\n", true, false},
		{"", true, false},
		{"y\n", false, false},
	} {
		h := withFakeLocalHost(t, "ubuntu")
		inactiveUFW(h)
		withTerminal(t, c.terminal)
		out := new(strings.Builder)
		err := runSiteAdmit([]string{"--request", path, "--public-ports"}, typed(t, c.typed), out)
		if planned := err == nil && strings.Contains(admitReport.String(), "arm the ufw fallback"); planned != c.plans {
			t.Errorf("typed %q at a terminal %v: planned the enable %v (%v)", c.typed, c.terminal, planned, err)
		}
		if !c.plans && (err == nil || !strings.Contains(err.Error(), "--enable-ufw") || !strings.Contains(err.Error(), "1. sudo ufw allow 22/tcp")) {
			t.Errorf("typed %q at a terminal %v: %v", c.typed, c.terminal, err)
		}
		if c.terminal && !strings.Contains(out.String(), "allow 22/tcp, every port sshd listens on") {
			t.Errorf("the question does not say what admit does:\n%s", out.String())
		}
	}
	// --enable-ufw asks nothing.
	h := withFakeLocalHost(t, "ubuntu")
	inactiveUFW(h)
	withTerminal(t, false)
	if err := runSiteAdmit([]string{"--request", path, "--public-ports", "--enable-ufw"}, typed(t, ""), new(strings.Builder)); err != nil || !strings.Contains(admitReport.String(), "arm the ufw fallback") {
		t.Errorf("--enable-ufw: %v\n%s", err, admitReport.String())
	}
}

// fakeExecute replaces Execute with one that runs nothing, for the test.
func fakeExecute(t *testing.T) {
	saved := executeAdmit
	executeAdmit = func(apply.Transport, hostadmit.Input, *hostadmit.Plan, ui.Reporter) error { return nil }
	t.Cleanup(func() { executeAdmit = saved })
}

// With --site, the login as the deployment's user, a new SSH connection
// made after ufw is on, is what stops the fallback, through the owner's
// login; a failed login leaves it armed and says when ufw turns off.
func TestAdmitSiteStopsTheUFWFallbackAfterTheLogin(t *testing.T) {
	fakeExecute(t)
	savedLogin, savedOwner := reachSiteUser, reachOwnerAdmit
	t.Cleanup(func() { reachSiteUser, reachOwnerAdmit = savedLogin, savedOwner })
	args := []string{"--site", "vm", "--ssh", "owner", "--config", fixtureConfig(), "--public-ports", "--enable-ufw", "--execute"}

	quietAdmit(t)
	owner := inactiveUFW(blankHost("ubuntu"))
	reachOwnerAdmit = func(config.Destination) apply.Transport { return owner }
	login := blankHost("ubuntu")
	loggedIn := -1
	reachSiteUser = func(string, config.Site) apply.Transport { loggedIn = len(owner.ran); return login }
	if err := runSiteAdmit(args, strings.NewReader(""), new(strings.Builder)); err != nil {
		t.Fatal(err)
	}
	if i := owner.ranIndex(hostadmit.DisarmUFW); i < 0 || loggedIn < 0 || i < loggedIn {
		t.Errorf("the fallback was stopped at %d, the login made at %d: %q", i, loggedIn, owner.ran)
	}

	quietAdmit(t)
	owner = inactiveUFW(blankHost("ubuntu"))
	reachSiteUser = func(string, config.Site) apply.Transport { return refusingHost{} }
	err := runSiteAdmit(args, strings.NewReader(""), new(strings.Builder))
	if err == nil || !strings.Contains(err.Error(), "ufw turns itself off again by") || owner.ranIndex(hostadmit.DisarmUFW) >= 0 {
		t.Errorf("a failed login: %v, stopped the fallback at %d", err, owner.ranIndex(hostadmit.DisarmUFW))
	}

	// A plan that enables nothing stops nothing.
	quietAdmit(t)
	owner = blankHost("ubuntu")
	reachSiteUser = func(string, config.Site) apply.Transport { return login }
	if err := runSiteAdmit(args, strings.NewReader(""), new(strings.Builder)); err != nil || owner.ranIndex(hostadmit.DisarmUFW) >= 0 {
		t.Errorf("active ufw: %v, stopped the fallback at %d", err, owner.ranIndex(hostadmit.DisarmUFW))
	}
}

// With --request, the owner on the host confirms a new SSH session: a yes
// stops the fallback, anything else leaves it to turn ufw off, and says so.
// The answer to the ufw question, the digest and the confirmation are read
// from one terminal in turn.
func TestAdmitRequestConfirmsANewLoginBeforeStoppingTheFallback(t *testing.T) {
	fakeExecute(t)
	path, digest := requestFile(t, "vm")
	for _, c := range []struct {
		typed string
		stops bool
	}{
		{"y\n" + digest + "\nyes\n", true},
		{"y\n" + digest + "\nno\n", false},
		{"y\n" + digest + "\n", false},
	} {
		h := withFakeLocalHost(t, "ubuntu")
		inactiveUFW(h)
		withTerminal(t, true)
		out := new(strings.Builder)
		if err := runSiteAdmit([]string{"--request", path, "--public-ports", "--execute"}, typed(t, c.typed), out); err != nil {
			t.Fatalf("typed %q: %v", c.typed, err)
		}
		if stopped := h.ranIndex(hostadmit.DisarmUFW) >= 0; stopped != c.stops {
			t.Errorf("typed %q: stopped the fallback %v", c.typed, stopped)
		}
		if !strings.Contains(out.String(), "Open a new SSH session to this host now. Type yes once it logs in") {
			t.Errorf("typed %q: not asked for a new session:\n%s", c.typed, out.String())
		}
		if !c.stops && !strings.Contains(admitReport.String(), "ufw turns itself off again by") {
			t.Errorf("typed %q: does not say ufw turns off:\n%s", c.typed, admitReport.String())
		}
	}
}

// The public-ports question ends with how a monitor needs neither, as a
// sentence of its own.
func TestThePublicPortsQuestionReadsAsSentences(t *testing.T) {
	gw, _ := requestFile(t, gatewayOf(t))
	withFakeLocalHost(t, "ubuntu")
	withTerminal(t, true)
	out := new(strings.Builder)
	if err := runSiteAdmit([]string{"--request", gw}, typed(t, "y\n"), out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "in the firewall. A monitor behind your own web server (web_server.managed_by: external) needs neither.\nGrant --public-ports? [y/N] ") {
		t.Errorf("the question:\n%s", out.String())
	}
}

// A grant answered at a terminal, then the digest, are read in turn from
// one stdin: the answer is not read ahead past its line.
func TestAGrantThenTheDigestAtOneTerminal(t *testing.T) {
	data, digest := requestFile(t, dataSiteOf(t))
	saved := executeAdmit
	t.Cleanup(func() { executeAdmit = saved })
	var granted *bool
	executeAdmit = func(_ apply.Transport, in hostadmit.Input, _ *hostadmit.Plan, _ ui.Reporter) error {
		granted = &in.Flags.Watchdog
		return nil
	}
	withFakeLocalHost(t, "ubuntu")
	withTerminal(t, true)
	if err := runSiteAdmit([]string{"--request", data, "--execute"}, typed(t, "y\n"+digest+"\n"), new(strings.Builder)); err != nil {
		t.Fatal(err)
	}
	if granted == nil || !*granted {
		t.Errorf("executed %v with the grant", granted != nil)
	}
}

// A login alone reaches the site's declared host and port, an IPv6
// address and a port other than 22 among them, with admit and evict alike.
func TestALoginAloneReachesAnIPv6HostOnItsDeclaredPort(t *testing.T) {
	body, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	path, _ := archFixture(t, strings.Replace(string(body), "      host: vm.example.org\n", "      host: 2001:db8::9\n      port: 2222\n", 1))
	savedOwner := reachOwnerAdmit
	t.Cleanup(func() { reachOwnerAdmit = savedOwner })
	for _, run := range []func([]string) error{
		func(a []string) error {
			return runSiteAdmit(append(a, "--public-ports"), strings.NewReader(""), new(strings.Builder))
		},
		// Evict then refuses ubuntu, the site's declared ssh.user, on a host
		// with no record: what is under test is where it went.
		func(a []string) error { _ = runSiteEvict(a, strings.NewReader(""), new(strings.Builder)); return nil },
	} {
		quietAdmit(t)
		var reached config.Destination
		reachOwnerAdmit = func(d config.Destination) apply.Transport { reached = d; return blankHost("ubuntu") }
		if err := run([]string{"--site", "vm", "--ssh", "owner", "--config", path}); err != nil {
			t.Fatal(err)
		}
		if reached.User != "owner" || reached.Host != "2001:db8::9" || reached.Port != 2222 {
			t.Errorf("reached %+v", reached)
		}
	}
}

// Evict, like admit, asks for the owner's login at a terminal when --ssh is
// left out.
func TestEvictAsksForTheLoginAtATerminal(t *testing.T) {
	quietAdmit(t)
	withTerminal(t, true)
	savedOwner := reachOwnerAdmit
	t.Cleanup(func() { reachOwnerAdmit = savedOwner })
	var reached config.Destination
	reachOwnerAdmit = func(d config.Destination) apply.Transport { reached = d; return blankHost("ubuntu") }
	out := new(strings.Builder)
	// Evict then refuses ubuntu, the site's declared ssh.user, on a host
	// with no record: what is under test is where it went.
	_ = runSiteEvict([]string{"--site", "vm", "--config", fixtureConfig()}, typed(t, "owner\n"), out)
	if reached.User != "owner" || reached.Host != "vm.example.org" || !strings.Contains(out.String(), "Existing username on vm.example.org (site vm) to connect under, with sudo:") {
		t.Errorf("reached %+v, asked:\n%s", reached, out.String())
	}
}

// Where --enable-ufw would be refused, a deny rule of the owner's among
// them, admit asks nothing at a terminal and gives the refusal.
func TestAdmitDoesNotAskToEnableUFWWhereTheFlagWouldBeRefused(t *testing.T) {
	path, _ := requestFile(t, "vm")
	h := withFakeLocalHost(t, "ubuntu")
	inactiveUFW(h)
	h.answers["paisans site admit: before enabling ufw"] = "rule deny OpenSSH\n" + h.answers["paisans site admit: before enabling ufw"]
	withTerminal(t, true)
	out := new(strings.Builder)
	err := runSiteAdmit([]string{"--request", path, "--public-ports"}, typed(t, "y\n"), out)
	if err == nil || !strings.Contains(err.Error(), "`ufw deny OpenSSH`") || strings.Contains(out.String(), "Enable ufw") {
		t.Errorf("%v\n%s", err, out.String())
	}
}

// The question and a failure once the fallback is armed both say the
// timer does not survive a reboot.
func TestTheUFWQuestionSaysTheFallbackDoesNotSurviveAReboot(t *testing.T) {
	path, _ := requestFile(t, "vm")
	inactiveUFW(withFakeLocalHost(t, "ubuntu"))
	withTerminal(t, true)
	out := new(strings.Builder)
	_ = runSiteAdmit([]string{"--request", path, "--public-ports"}, typed(t, "n\n"), out)
	if !strings.Contains(out.String(), "does not survive a reboot") || !strings.Contains(out.String(), "/etc/ufw/before.rules") || !strings.Contains(out.String(), "sslh") {
		t.Errorf("the question:\n%s", out.String())
	}
	err := ufwFallbackNote(fmt.Errorf("failed"), &hostadmit.Plan{EnablesUFW: true}, true)
	if !strings.Contains(err.Error(), "does not survive a reboot") {
		t.Errorf("the note: %v", err)
	}
}

// holding is a reporter that counts the holds in place, so a test sees
// whether a prompt was written while the reporter was held.
type holding struct {
	ui.Reporter
	held int
}

func (h *holding) Hold() func() {
	h.held++
	return func() { h.held-- }
}

// The digest admit asks for and the word evict asks for are prompts on the
// terminal the plan was drawn on: each is asked with the reporter held, so
// the run draws the plan's rows again below it rather than reach above it.
func TestThePromptsBeforeARunHoldTheReporter(t *testing.T) {
	saved := executeAdmit
	t.Cleanup(func() { executeAdmit = saved })
	executeAdmit = func(apply.Transport, hostadmit.Input, *hostadmit.Plan, ui.Reporter) error { return nil }
	path, digest := requestFile(t, "vm")
	withFakeLocalHost(t, "ubuntu")
	withTerminal(t, true)
	r := &holding{Reporter: reporterOverride}
	reporterOverride = r
	heldAtPrompt := false
	prompt := writerFunc(func(p []byte) (int, error) {
		heldAtPrompt = heldAtPrompt || (r.held > 0 && strings.Contains(string(p), "digest"))
		return len(p), nil
	})
	if err := runSiteAdmit([]string{"--request", path, "--public-ports", "--execute"}, typed(t, digest+"\n"), prompt); err != nil {
		t.Fatal(err)
	}
	if !heldAtPrompt || r.held != 0 {
		t.Errorf("admit: held at the digest %v, holds left %d", heldAtPrompt, r.held)
	}

	evictedHost(t)
	r = &holding{Reporter: reporterOverride}
	reporterOverride = r
	heldAtPrompt = false
	prompt = writerFunc(func(p []byte) (int, error) {
		heldAtPrompt = heldAtPrompt || (r.held > 0 && strings.Contains(string(p), "to proceed"))
		return len(p), nil
	})
	if err := runSiteEvict([]string{"--deployment", fixtureID(t), "--user", "paisans-f2a9", "--execute"}, typed(t, "paisans-f2a9\n"), prompt); err != nil {
		t.Fatal(err)
	}
	if !heldAtPrompt || r.held != 0 {
		t.Errorf("evict: held at the word %v, holds left %d", heldAtPrompt, r.held)
	}
}

// --yes grants each grant the site's roles need and the flags leave out,
// as typing y would, with no terminal, and says so.
func TestYesGrantsAMissingGrant(t *testing.T) {
	allowYes(t)
	data, _ := requestFile(t, dataSiteOf(t))
	withFakeLocalHost(t, "ubuntu")
	withTerminal(t, false)
	out := new(strings.Builder)
	if err := runSiteAdmit([]string{"--request", data, "--yes"}, strings.NewReader(""), out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "approved by --yes: grant --watchdog\n") {
		t.Errorf("the grant is not said to be approved:\n%s", out.String())
	}
	if strings.Contains(out.String(), "[y/N]") {
		t.Errorf("asked anyway:\n%s", out.String())
	}
}

// --digest takes the place of the typed digest, with or without --yes and
// with no terminal: the right one admits, any other refuses before the
// host is reached.
func TestTheDigestFlag(t *testing.T) {
	path, digest := requestFile(t, "vm")
	allowYes(t)
	for _, extra := range [][]string{nil, {"--yes"}} {
		h := withFakeLocalHost(t, "ubuntu")
		withTerminal(t, false)
		args := append([]string{"--request", path, "--public-ports", "--execute", "--digest", digest}, extra...)
		// The fake host does not become what the plan makes it, so the run
		// stops part way: that it began is what shows the digest passed.
		if err := runSiteAdmit(args, strings.NewReader(""), new(strings.Builder)); !h.ranAny("useradd ") {
			t.Errorf("%q: not executed: %v", args, err)
		}

		noHost(t)
		args = append([]string{"--request", path, "--public-ports", "--execute", "--digest", "0000-0000-0000-0000"}, extra...)
		if err := runSiteAdmit(args, strings.NewReader(""), new(strings.Builder)); err == nil || !strings.Contains(err.Error(), "not this request's digest") || !strings.Contains(err.Error(), "Nothing was changed") {
			t.Errorf("%q: %v", args, err)
		}
	}
	// The digest read aloud is accepted as when it is typed.
	aloud := strings.NewReplacer("-", " ", "1", "l", "0", "o").Replace(strings.ToUpper(digest))
	h := withFakeLocalHost(t, "ubuntu")
	if err := runSiteAdmit([]string{"--request", path, "--public-ports", "--execute", "--digest", aloud}, strings.NewReader(""), new(strings.Builder)); !h.ranAny("useradd ") {
		t.Errorf("aloud: not executed: %v", err)
	}
	// --digest goes with --request alone.
	noHost(t)
	if err := runSiteAdmit([]string{"--site", "vm", "--ssh", "owner", "--config", fixtureConfig(), "--digest", digest}, strings.NewReader(""), new(strings.Builder)); err == nil || !strings.Contains(err.Error(), "--digest goes with --request") {
		t.Errorf("--site --digest: %v", err)
	}
}

// --yes never answers the digest: --yes --request --execute without
// --digest refuses before anything is read, naming --digest.
func TestYesDoesNotAnswerTheDigest(t *testing.T) {
	allowYes(t)
	path, _ := requestFile(t, "vm")
	noHost(t)
	withTerminal(t, true)
	err := runSiteAdmit([]string{"--request", path, "--public-ports", "--execute", "--yes"}, typed(t, "y\n"), new(strings.Builder))
	if err == nil || !strings.Contains(err.Error(), "--digest") || !strings.Contains(err.Error(), "Nothing was read or changed") {
		t.Errorf("err = %v", err)
	}
}

// --yes with --request refuses where admit would enable ufw: given
// --enable-ufw, before the host is reached; asked, once the host is read
// and before anything changes. Both point to site admit --site.
func TestYesWithRequestRefusesToEnableUFW(t *testing.T) {
	allowYes(t)
	path, digest := requestFile(t, "vm")
	noHost(t)
	err := runSiteAdmit([]string{"--request", path, "--public-ports", "--enable-ufw", "--yes"}, strings.NewReader(""), new(strings.Builder))
	if err == nil || !strings.Contains(err.Error(), "paisans site admit --site") || !strings.Contains(err.Error(), "new SSH session") {
		t.Errorf("--enable-ufw: %v", err)
	}

	executed := false
	saved := executeAdmit
	executeAdmit = func(apply.Transport, hostadmit.Input, *hostadmit.Plan, ui.Reporter) error {
		executed = true
		return nil
	}
	t.Cleanup(func() { executeAdmit = saved })
	h := withFakeLocalHost(t, "ubuntu")
	inactiveUFW(h)
	withTerminal(t, false)
	out := new(strings.Builder)
	err = runSiteAdmit([]string{"--request", path, "--public-ports", "--execute", "--digest", digest, "--yes"}, strings.NewReader(""), out)
	if err == nil || !strings.Contains(err.Error(), "paisans site admit --site") || !strings.Contains(err.Error(), "Nothing was changed") {
		t.Errorf("asked: %v", err)
	}
	if executed || strings.Contains(out.String(), "approved by --yes: enable ufw") {
		t.Errorf("executed %v, printed:\n%s", executed, out.String())
	}
}

// With --site, --yes answers the ufw question, and admit's own login as
// the user stops the fallback, as it does after a typed yes.
func TestYesWithSiteEnablesUFW(t *testing.T) {
	allowYes(t)
	fakeExecute(t)
	savedLogin, savedOwner := reachSiteUser, reachOwnerAdmit
	t.Cleanup(func() { reachSiteUser, reachOwnerAdmit = savedLogin, savedOwner })
	quietAdmit(t)
	withTerminal(t, false)
	owner := inactiveUFW(blankHost("ubuntu"))
	reachOwnerAdmit = func(config.Destination) apply.Transport { return owner }
	reachSiteUser = func(string, config.Site) apply.Transport { return blankHost("ubuntu") }
	out := new(strings.Builder)
	if err := runSiteAdmit([]string{"--site", "vm", "--ssh", "owner", "--config", fixtureConfig(), "--public-ports", "--execute", "--yes"}, strings.NewReader(""), out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "approved by --yes: enable ufw (--enable-ufw)\n") {
		t.Errorf("the answer is not printed:\n%s", out.String())
	}
	if owner.ranIndex(hostadmit.DisarmUFW) < 0 {
		t.Errorf("the fallback was not stopped: %q", owner.ran)
	}
}

// --yes does not choose a login: --site without --ssh refuses, naming
// --ssh, for admit and evict, before any host is reached, even at a
// terminal.
func TestYesDoesNotChooseALogin(t *testing.T) {
	allowYes(t)
	withTerminal(t, true)
	noHost(t)
	if err := runSiteAdmit([]string{"--site", "vm", "--config", fixtureConfig(), "--public-ports", "--yes"}, typed(t, "owner\n"), new(strings.Builder)); err == nil || !strings.Contains(err.Error(), "--ssh") || !strings.Contains(err.Error(), "Nothing was read or changed") {
		t.Errorf("admit: %v", err)
	}
	noHost(t)
	if err := runSiteEvict([]string{"--site", "vm", "--config", fixtureConfig(), "--yes"}, typed(t, "owner\n"), new(strings.Builder)); err == nil || !strings.Contains(err.Error(), "--ssh") || !strings.Contains(err.Error(), "Nothing was read or changed") {
		t.Errorf("evict: %v", err)
	}
}

// Evict's typed user name, answered by --yes with no terminal.
func TestYesAnswersEvict(t *testing.T) {
	allowYes(t)
	h := evictedHost(t)
	withTerminal(t, false)
	out := new(strings.Builder)
	if err := runSiteEvict([]string{"--deployment", fixtureID(t), "--user", "paisans-f2a9", "--execute", "--yes"}, strings.NewReader(""), out); err != nil || !h.ranAny("rm -f ") {
		t.Errorf("%v, executed %v", err, h.ranAny("rm -f "))
	}
	if !strings.Contains(out.String(), "approved by --yes: paisans-f2a9\n") {
		t.Errorf("printed:\n%s", out.String())
	}
}

// Off a terminal nothing confirms the new SSH login: --request
// --enable-ufw --execute refuses before anything changes, with --digest
// or not, and a "yes" piped in never stops the fallback.
func TestAPipeNeverConfirmsTheNewLogin(t *testing.T) {
	path, digest := requestFile(t, "vm")
	for _, extra := range [][]string{{"--digest", digest}, nil} {
		noHost(t)
		withTerminal(t, false)
		args := append([]string{"--request", path, "--public-ports", "--enable-ufw", "--execute"}, extra...)
		err := runSiteAdmit(args, typed(t, digest+"\nyes\n"), new(strings.Builder))
		if err == nil || !strings.Contains(err.Error(), "paisans site admit --site") || !strings.Contains(err.Error(), "Nothing was changed") {
			t.Errorf("%q: %v", args, err)
		}
	}
	// confirmUFW itself reads nothing off a terminal.
	h := inactiveUFW(blankHost("ubuntu"))
	quietAdmit(t)
	withTerminal(t, false)
	if err := confirmUFW(typed(t, "yes\n"), new(strings.Builder), reporterOverride, h, "ubuntu", "29.5"); err != nil {
		t.Fatal(err)
	}
	if h.ranIndex(hostadmit.DisarmUFW) >= 0 {
		t.Errorf("a pipe stopped the fallback: %q", h.ran)
	}
}

// A --yes --request dry run that meets an inactive ufw refuses, before
// anything changes, pointing to site admit --site.
func TestYesRequestDryRunRefusesAnInactiveUFW(t *testing.T) {
	allowYes(t)
	path, _ := requestFile(t, "vm")
	h := withFakeLocalHost(t, "ubuntu")
	inactiveUFW(h)
	withTerminal(t, false)
	out := new(strings.Builder)
	err := runSiteAdmit([]string{"--request", path, "--public-ports", "--yes"}, strings.NewReader(""), out)
	if err == nil || !strings.Contains(err.Error(), "paisans site admit --site") || !strings.Contains(err.Error(), "Nothing was changed") {
		t.Errorf("err = %v", err)
	}
	if strings.Contains(out.String(), "approved by --yes: enable ufw") || h.ranAny("useradd ") {
		t.Errorf("went on:\n%s", out.String())
	}
}

// fakeAdmitFromInit makes site admit --site, as init runs it, run over
// blank hosts for user: the owner's login and the deployment user's are
// blankHost(user), and the plan's run calls admitted in place of changing
// anything. It returns where the owner's login went, once admit reached it.
func fakeAdmitFromInit(t *testing.T, user string, admitted func()) *config.Destination {
	t.Helper()
	savedExecute, savedLogin, savedOwner := executeAdmit, reachSiteUser, reachOwnerAdmit
	t.Cleanup(func() { executeAdmit, reachSiteUser, reachOwnerAdmit = savedExecute, savedLogin, savedOwner })
	reached := new(config.Destination)
	reachOwnerAdmit = func(d config.Destination) apply.Transport { *reached = d; return blankHost(user) }
	reachSiteUser = func(string, config.Site) apply.Transport { return blankHost(user) }
	executeAdmit = func(apply.Transport, hostadmit.Input, *hostadmit.Plan, ui.Reporter) error {
		admitted()
		return nil
	}
	return reached
}

// noAdmitFromInit fails the test if site admit reaches a host.
func noAdmitFromInit(t *testing.T) {
	t.Helper()
	savedOwner, savedExecute := reachOwnerAdmit, executeAdmit
	t.Cleanup(func() { reachOwnerAdmit, executeAdmit = savedOwner, savedExecute })
	reachOwnerAdmit = func(config.Destination) apply.Transport { t.Fatal("site admit reached the owner's host"); return nil }
	executeAdmit = func(apply.Transport, hostadmit.Input, *hostadmit.Plan, ui.Reporter) error {
		t.Fatal("site admit ran a plan")
		return nil
	}
}

// ownerApp makes h's inactive ufw one in front of an app of the owner's
// answering on 8080, which no ufw rule allows.
func ownerApp(h *admitHost) *admitHost {
	const marker = "paisans site admit: before enabling ufw"
	h.answers[marker] = `listen LISTEN 0 4096 0.0.0.0:8080 0.0.0.0:* users:(("app",pid=950,fd=3))` + "\n" + h.answers[marker]
	return h
}

// At a terminal, the ufw question lists what enabling ufw blocks before it
// asks, and a yes needs a second yes that those may be blocked; a no to
// either refuses, the second with the allow lines. With --enable-ufw the
// second question alone is asked; off a terminal only
// --enable-ufw-block-listed enables ufw over them.
func TestAdmitAsksWhetherUFWMayBlockWhatAnswers(t *testing.T) {
	path, _ := requestFile(t, "vm")
	for _, c := range []struct {
		args     []string
		typed    string
		terminal bool
		plans    bool
		want     string
	}{
		{nil, "y\ny\n", true, true, ""},
		{nil, "y\nn\n", true, false, "  sudo ufw allow 8080/tcp\n"},
		{nil, "y\n", true, false, "  sudo ufw allow 8080/tcp\n"},
		{nil, "n\n", true, false, "--enable-ufw"},
		{[]string{"--enable-ufw"}, "y\n", true, true, ""},
		{[]string{"--enable-ufw"}, "n\n", true, false, "  sudo ufw allow 8080/tcp\n"},
		{[]string{"--enable-ufw"}, "", false, false, "  sudo ufw allow 8080/tcp\n"},
		{[]string{"--enable-ufw", "--enable-ufw-block-listed"}, "", false, true, ""},
	} {
		ownerApp(inactiveUFW(withFakeLocalHost(t, "ubuntu")))
		withTerminal(t, c.terminal)
		out := new(strings.Builder)
		err := runSiteAdmit(append([]string{"--request", path, "--public-ports"}, c.args...), typed(t, c.typed), out)
		name := fmt.Sprintf("%q typed %q", c.args, c.typed)
		if planned := err == nil && strings.Contains(admitReport.String(), "arm the ufw fallback"); planned != c.plans {
			t.Errorf("%s: planned the enable %v (%v)", name, planned, err)
		}
		if !c.plans && (err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "8080/tcp: app (pid 950, uid 0) listens on 0.0.0.0:8080")) {
			t.Errorf("%s: %v", name, err)
		}
		if !c.terminal {
			continue
		}
		text := out.String()
		if c.args == nil && (strings.Index(text, "8080/tcp: app") < 0 || strings.Index(text, "8080/tcp: app") > strings.Index(text, "Enable ufw (--enable-ufw)?")) {
			t.Errorf("%s: the ufw question does not list 8080 before it asks:\n%s", name, text)
		}
		if asked := strings.Contains(text, "Block every one listed (--enable-ufw-block-listed)?"); asked != (c.typed != "n\n" || c.args != nil) {
			t.Errorf("%s: asked whether to block %v:\n%s", name, asked, text)
		}
	}
}

// --enable-ufw-block-listed enables nothing alone.
func TestTheBlockListedFlagGoesWithEnableUFW(t *testing.T) {
	path, _ := requestFile(t, "vm")
	noHost(t)
	err := runSiteAdmit([]string{"--request", path, "--enable-ufw-block-listed"}, strings.NewReader(""), new(strings.Builder))
	if err == nil || !strings.Contains(err.Error(), "goes with --enable-ufw") {
		t.Errorf("%v", err)
	}
}

// --yes never answers whether ufw may block what answers from off the
// host: with --site it refuses, naming the flag, and approves nothing.
func TestYesNeverBlocksWhatAnswers(t *testing.T) {
	allowYes(t)
	fakeExecute(t)
	savedOwner := reachOwnerAdmit
	t.Cleanup(func() { reachOwnerAdmit = savedOwner })
	quietAdmit(t)
	withTerminal(t, false)
	owner := ownerApp(inactiveUFW(blankHost("ubuntu")))
	reachOwnerAdmit = func(config.Destination) apply.Transport { return owner }
	out := new(strings.Builder)
	err := runSiteAdmit([]string{"--site", "vm", "--ssh", "owner", "--config", fixtureConfig(), "--public-ports", "--execute", "--yes"}, strings.NewReader(""), out)
	if err == nil || !strings.Contains(err.Error(), "--enable-ufw-block-listed") || strings.Contains(out.String(), "approved by --yes") || owner.ranIndex("ufw --force enable") >= 0 {
		t.Errorf("%v\n%s", err, out.String())
	}
}
