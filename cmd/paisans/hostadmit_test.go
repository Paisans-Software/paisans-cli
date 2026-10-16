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
	if strings.Contains(command, "paisans host admit: read only") {
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
		{"userns-restrict", "1"}, {"rootlesskit-profile", "loaded"},
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
		"paisans host admit: as the user":          "daemon inactive\nunit absent\ndaemon-json absent\ncontainers 0\nimages 0\nvolumes 0\nend\n",
		"paisans host admit: who holds 80 and 443": "end\n",
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
		err := runHostAdmit(args, strings.NewReader(""), new(strings.Builder))
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
		{[]string{}, "--request <file> on the host, or --site <name> --ssh"},
		{[]string{"--request", path, "--site", "vm"}, "not both"},
		{[]string{"--site", "vm", "--config", fixtureConfig()}, "--ssh"},
		{[]string{"--request", path, "--ssh", "owner@h"}, "--ssh"},
		{[]string{"--request", path, "extra"}, "takes no argument"},
	} {
		noHost(t)
		if err := runHostAdmit(c.args, strings.NewReader(""), new(strings.Builder)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.args, err, c.want)
		}
	}
	noHost(t)
	runningAsRootOnLinux = func() bool { return false }
	if err := runHostAdmit([]string{"--request", path}, strings.NewReader(""), new(strings.Builder)); err == nil || !strings.Contains(err.Error(), "as root on the host") {
		t.Errorf("--request not as root: %v", err)
	}
}

// A request larger than any request is refused before it is parsed.
func TestAdmitRefusesAnOversizedRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.admit")
	os.WriteFile(path, make([]byte, admission.MaxRequestSize+1), 0o644)
	noHost(t)
	if err := runHostAdmit([]string{"--request", path}, strings.NewReader(""), new(strings.Builder)); err == nil || !strings.Contains(err.Error(), "larger than any request") {
		t.Errorf("err = %v", err)
	}
}

// --execute with --request asks for the digest at a terminal, so it cannot
// be scripted past; a mistyped one changes nothing; the digest read aloud,
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
		err := runHostAdmit([]string{"--request", path, "--public-ports", "--execute"}, typed(t, c.typed), new(strings.Builder))
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
	if err := runHostAdmit([]string{"--request", path, "--public-ports"}, strings.NewReader(""), new(strings.Builder)); err != nil {
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
	if err := runHostAdmit([]string{"--request", path, "--public-ports", "--execute"}, typed(t, digest+"\n"), new(strings.Builder)); err != nil {
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
	err := runHostAdmit([]string{"--site", "vm", "--ssh", "owner@vm.example.org", "--config", fixtureConfig(), "--public-ports", "--execute"}, strings.NewReader(""), new(strings.Builder))
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
		err := runHostEvict([]string{"--deployment", fixtureID(t), "--user", "paisans-f2a9", "--execute"}, typed(t, c.typed), new(strings.Builder))
		if executed := h.ranAny("rm -f "); executed != c.executes {
			t.Errorf("typed %q at a terminal %v: executed %v (%v)", c.typed, c.terminal, executed, err)
		}
	}
}

// Without --execute evict shows the plan and changes nothing.
func TestEvictWithoutExecuteChangesNothing(t *testing.T) {
	h := evictedHost(t)
	if err := runHostEvict([]string{"--deployment", fixtureID(t), "--user", "paisans-f2a9"}, strings.NewReader(""), new(strings.Builder)); err != nil {
		t.Fatal(err)
	}
	if h.ranAny("rm -f ") {
		t.Errorf("ran a change: %q", h.ran)
	}
}

// evict takes --data-dir to BuildEvict, which checks it as admit does.
func TestEvictPassesDataDir(t *testing.T) {
	evictedHost(t)
	err := runHostEvict([]string{"--deployment", fixtureID(t), "--user", "paisans-f2a9", "--data-dir", "relative"}, strings.NewReader(""), new(strings.Builder))
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
		{[]string{"--site", "vm"}, "--site needs --ssh"},
		{[]string{"--request", path, "--ssh", "owner@h"}, "--ssh goes with --site"},
		{[]string{"--request", path, "extra"}, "takes no argument"},
	} {
		noHost(t)
		if err := runHostEvict(c.args, strings.NewReader(""), new(strings.Builder)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.args, err, c.want)
		}
	}
	noHost(t)
	runningAsRootOnLinux = func() bool { return false }
	if err := runHostEvict([]string{"--deployment", fixtureID(t), "--user", "paisans-f2a9"}, strings.NewReader(""), new(strings.Builder)); err == nil || !strings.Contains(err.Error(), "as root on the host") {
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
	if err := runHostAdmit([]string{"--request", path, "--public-ports", "--execute"}, typed(t, digest+"\n"), prompt); err != nil {
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
	if err := runHostAdmit([]string{"--site", "vm", "--ssh", "owner@vm.example.org", "--config", fixtureConfig(), "--public-ports", "--execute"}, strings.NewReader(""), new(strings.Builder)); err != nil {
		t.Fatal(err)
	}
	if executed == nil {
		t.Fatal("--site: nothing executed")
	}
}
