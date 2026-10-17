package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/siteremove"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// noSiteHosts fails the test if `site remove` reaches for any host.
func noSiteHosts(t *testing.T) {
	t.Helper()
	saved := removeSiteHost
	removeSiteHost = func(string, config.Site) apply.Transport {
		t.Fatal("a host was reached")
		return nil
	}
	t.Cleanup(func() { removeSiteHost = saved })
	savedDest := reachDestination
	reachDestination = func(config.Destination) apply.Transport {
		t.Fatal("a host was reached")
		return nil
	}
	t.Cleanup(func() { reachDestination = savedDest })
}

// Deleting member data needs a person at a terminal, or --yes. Refused
// before any host is read.
func TestSiteRemoveDeleteDataNeedsATerminal(t *testing.T) {
	noSiteHosts(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString("home-b\n")
	w.Close()
	defer r.Close()
	err = runSiteRemove([]string{"home-b", "--config", fixtureConfig(), "--delete-data", "--execute"}, r, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "stdin is not a terminal") {
		t.Errorf("err = %v", err)
	}
}

// A refusal from the configuration alone reaches no host either.
func TestSiteRemoveRefusesBeforeReachingAHost(t *testing.T) {
	noSiteHosts(t)
	for args, want := range map[string]string{
		"home-a":                                 "pinned to it",
		"home-z":                                 "declares no site",
		"home-b --host-gone --delete-data":       "cannot be used together",
		"home-b --execute --host-gone extra-arg": "one site",
	} {
		err := runSiteRemove(append(strings.Fields(args), "--config", fixtureConfig()), strings.NewReader(""), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", args, err, want)
		}
	}
}

func TestConfirmSiteWantsTheSitesName(t *testing.T) {
	saved := isTerminal
	isTerminal = func(*os.File) bool { return true }
	t.Cleanup(func() { isTerminal = saved })
	for answer, ok := range map[string]bool{"home-b\n": true, "yes\n": false, "": false} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		w.WriteString(answer)
		w.Close()
		err = confirmSite(answerer{}, r, &bytes.Buffer{}, "home-b")
		r.Close()
		if (err == nil) != ok {
			t.Errorf("answer %q: %v", answer, err)
		}
	}
}

func TestChooseHost(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	declared := cfg.Sites["home-b"].Destination().String()
	for _, tc := range []struct{ site, ssh, want, err string }{
		{site: "home-b", want: declared},
		{site: "home-b", ssh: "admin@198.51.100.4", want: "admin@198.51.100.4:22"},
		{site: "never", ssh: "admin@192.0.2.1:2222", want: "admin@192.0.2.1:2222"},
		{site: "never", err: "Name its host with --ssh"},
		{site: "home-b", ssh: "myalias", err: "not user@host"},
	} {
		got, err := chooseHost(cfg, tc.site, tc.ssh)
		switch {
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%s %s: err = %v, want %q", tc.site, tc.ssh, err, tc.err)
		case tc.err == "" && (err != nil || got.String() != tc.want):
			t.Errorf("%s %s: got %v, %v; want %s", tc.site, tc.ssh, got, err, tc.want)
		}
	}
}

// Refusals that need no host reach none.
func TestSiteRemoveForceRefusesBeforeReachingAHost(t *testing.T) {
	noSiteHosts(t)
	for args, want := range map[string]string{
		"home-b --ssh admin@192.0.2.1":                                "only --force takes",
		"home-b --force --host-gone":                                  "--host-gone cleans no host",
		"never --force":                                               "Name its host with --ssh",
		"home-b --force --ssh myalias":                                "not user@host",
		"home-b --force --execute":                                    "terminal",
		"never --force --ssh admin@192.0.2.1 --execute --delete-data": "terminal",
	} {
		err := runSiteRemove(append(strings.Fields(args), "--config", fixtureConfig()), strings.NewReader("home-b\n"), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", args, err, want)
		}
	}
}

// A host holding nothing of this deployment ends the run without a re-run
// hint, dry run or not.
func TestForcedOnAnEmptyHostSaysNothingToDo(t *testing.T) {
	plan := &siteremove.Plan{Site: "home-b", Stages: []*siteremove.Stage{{Number: 3, Name: "clean the host"}}}
	rec := &ui.Recorder{}
	if !forcedNothingToDo(rec, plan, config.Destination{User: "admin", Host: "192.0.2.1", Port: 22}, false) {
		t.Fatal("an empty plan was not ended")
	}
	if out := rec.Lines(); strings.Contains(out, "--execute") || !strings.Contains(out, "holds nothing of this deployment") {
		t.Errorf("printed:\n%s", out)
	}
	rec = &ui.Recorder{}
	forcedNothingToDo(rec, plan, config.Destination{User: "admin", Host: "192.0.2.1", Port: 22}, true)
	if out := rec.Lines(); !strings.Contains(out, "--execute") || !strings.Contains(out, "deployment record") {
		t.Errorf("a dry run with a record left to clean printed:\n%s", out)
	}
	// A host left for its owner's evict says so on every ending.
	plan.Evict = "sudo paisans site evict --deployment x --user deploy --execute"
	for _, recordLeft := range []bool{false, true} {
		rec = &ui.Recorder{}
		forcedNothingToDo(rec, plan, config.Destination{User: "admin", Host: "192.0.2.1", Port: 22}, recordLeft)
		if !strings.Contains(rec.Lines(), plan.Evict) {
			t.Errorf("record left %v: no evict line in:\n%s", recordLeft, rec.Lines())
		}
	}
	plan.Stages[0].Steps = []siteremove.Step{{Site: "home-b", Verb: "remove"}}
	if forcedNothingToDo(&ui.Recorder{}, plan, config.Destination{}, false) {
		t.Error("a plan with steps was ended")
	}
}

// A forced run on a site no longer declared reports a gateway whose record
// it could not reach, and still finishes: the host is clean.
func TestForcedRunReportsAGatewayItCouldNotReach(t *testing.T) {
	savedSite, savedReg := removeSiteHost, registryHost
	t.Cleanup(func() { removeSiteHost, registryHost = savedSite, savedReg })
	quiet := admittedRunning{runningHost: runningHost{failingHost{match: "\x00"}}, site: "monitor-a", roles: []string{"monitor"}}
	removeSiteHost = func(string, config.Site) apply.Transport { return quiet }
	registryHost = func(string, config.Site, string) registry.Runner { return &recordFake{down: true} }
	var err error
	out := captureStdout(t, func() {
		err = runSiteRemove([]string{"monitor-a", "--force", "--ssh", "admin@192.0.2.1", "--config", fixtureConfig(), "--secrets", fixtureSecretsPath(), "--execute"}, strings.NewReader(""), &bytes.Buffer{})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "vm missed this change to the deployment record") {
		t.Errorf("no warning:\n%s", out)
	}
}

// A host whose only part of the deployment is its kept Caddy says so, and
// names the sites it is kept for, rather than that it holds nothing.
func TestForcedOnAHostWithOnlyAKeptCaddySaysSo(t *testing.T) {
	plan := &siteremove.Plan{Site: "vm", Stages: []*siteremove.Stage{{Number: 3, Name: "clean the host"}}, CaddyKept: []string{"/srv/paisans/f2a9/caddy.d/blog.caddy"}}
	rec := &ui.Recorder{}
	if !forcedNothingToDo(rec, plan, config.Destination{User: "admin", Host: "192.0.2.1", Port: 22}, false) {
		t.Fatal("a plan with nothing to do was not ended")
	}
	out := rec.Lines()
	if !strings.Contains(out, "Caddy") || !strings.Contains(out, "/srv/paisans/f2a9/caddy.d/blog.caddy") || strings.Contains(out, "holds nothing of this deployment.") {
		t.Errorf("printed:\n%s", out)
	}
}

// idHost is an empty running host whose registry holds the fixture
// deployment's monitor and a neighbour's entry, and whose login, ubuntu, the
// fixture deployment's admission record names as its watch site's user.
type idHost struct {
	runningHost
	registry string
}

func (h idHost) ReadFile(path string) (string, bool, error) {
	if path == registry.Path {
		return h.registry, true, nil
	}
	return admittedRunning{runningHost: h.runningHost, site: "watch", roles: []string{"monitor"}}.ReadFile(path)
}

func (h idHost) Run(command string) (string, error) {
	if strings.Contains(command, `echo "user $(id -un)"`) {
		p, _ := admission.Path("f2a9")
		return "user ubuntu\n" + p + "\n", nil
	}
	return h.runningHost.Run(command)
}

// admittedRunning is a running host that admitted the fixture deployment
// as its login user, ubuntu, whom site remove cleans as: as site with
// roles, or as home-a when site is empty.
type admittedRunning struct {
	runningHost
	site  string
	roles []string
}

func (h admittedRunning) ReadFile(path string) (string, bool, error) {
	if p, _ := admission.Path("f2a9"); path == p {
		return ubuntuRecord(h.site, h.roles), true, nil
	}
	return h.runningHost.ReadFile(path)
}

// ubuntuRecord is the fixture deployment's admission record naming ubuntu,
// the user every fake host's passwd entry answers for the login, for home-a,
// or for site with roles when site is not empty.
func ubuntuRecord(site string, roles []string) string {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		panic(err)
	}
	req, err := admission.Build(cfg, "home-a")
	if err != nil {
		panic(err)
	}
	rec := admission.NewRecord(req, req.Grants, "", time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC))
	rec.User = "ubuntu"
	if site != "" {
		rec.Site, rec.Roles = site, roles
	}
	b, err := admission.EncodeRecord(rec)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// cleaningHost is an idHost holding one unit of the deployment's in its
// user's systemd directory, which a removal deletes.
type cleaningHost struct {
	idHost
	removed bool
}

func (h *cleaningHost) Run(command string) (string, error) {
	if strings.Contains(command, "daemon-reload") {
		h.removed = true
		return "", nil
	}
	if strings.Contains(command, `echo "unit $f"`) {
		if h.removed {
			return "", nil
		}
		return "unit /home/ubuntu/.config/systemd/user/paisans-f2a9-config-daemon.service\n", nil
	}
	return h.idHost.Run(command)
}

// Every --id run says DNS was not modified, in one line and the comment to
// search for, since with no configuration there is no provider to ask.
func TestSiteRemoveByIDWarnsThatDNSWasNotModified(t *testing.T) {
	withHost(t, idHost{runningHost{failingHost{match: "\x00"}}, twoDeployments(t)})
	out := plainOutput(t)
	if err := runSiteRemove([]string{"--force", "--ssh", "admin@192.0.2.30", "--id", "f2a9"}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	t.Log("\n" + got)
	if strings.Count(got, "DNS") != 1 {
		t.Errorf("want the warning once:\n%s", got)
	}
	got = strings.Join(strings.Fields(got), " ")
	for _, want := range []string{
		"WARN DNS records for this deployment, if any exist, were not modified They carry",
		`They carry the comment "paisans-f2a9: created by paisans dns init", and may point at 192.0.2.30`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
}

// A forced removal of a site paisans-stack.yaml no longer declares has no address
// for it, so its DNS stage skips, and no provider is made.
func TestForcedRemovalOfAnUndeclaredSiteMakesNoProvider(t *testing.T) {
	savedSite, savedReg, savedDNS := removeSiteHost, registryHost, dnsProviderFor
	t.Cleanup(func() { removeSiteHost, registryHost, dnsProviderFor = savedSite, savedReg, savedDNS })
	removeSiteHost = func(string, config.Site) apply.Transport {
		return admittedRunning{runningHost: runningHost{failingHost{match: "\x00"}}, site: "monitor-a", roles: []string{"monitor"}}
	}
	registryHost = func(string, config.Site, string) registry.Runner { return &recordFake{} }
	dnsProviderFor = func(string, string) (dns.Provider, error) {
		t.Error("a DNS provider was made")
		return nil, nil
	}
	plainOutput(t)
	err := runSiteRemove([]string{"monitor-a", "--force", "--ssh", "admin@192.0.2.1", "--config", fixtureConfig(), "--secrets", fixtureSecretsPath()}, strings.NewReader(""), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
}

// An --id run that cleans the host ends on one line naming the host and the
// deployment it was cleaned of, and the site evict line for its owner.
func TestSiteRemoveByIDEndsNamingTheDeployment(t *testing.T) {
	withHost(t, &cleaningHost{idHost: idHost{runningHost{failingHost{match: "\x00"}}, twoDeployments(t)}})
	saved := isTerminal
	isTerminal = func(*os.File) bool { return true }
	t.Cleanup(func() { isTerminal = saved })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString("watch\n")
	w.Close()
	defer r.Close()
	rec := &ui.Recorder{}
	reporterOverride = rec
	t.Cleanup(func() { reporterOverride = nil })
	if err := runSiteRemove([]string{"--force", "--ssh", "admin@192.0.2.30", "--id", "f2a9", "--execute"}, r, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	want := "admin@192.0.2.30:22 is cleaned of deployment " + monitorID + "."
	if !rec.Has("result", want) {
		t.Errorf("no result %q in:\n%s", want, rec.Lines())
	}
	// Then the line the host's owner runs to remove what is root's.
	if !rec.Has("note", "the host's owner finishes on watch") || !strings.Contains(rec.Lines(), "    sudo paisans site evict --deployment "+monitorID+" --user ubuntu --execute") {
		t.Errorf("no evict line in:\n%s", rec.Lines())
	}
}

// --host-gone refuses what would reach the host, and an unattended run,
// before any host is read.
func TestSiteRemoveHostGoneRefusesBeforeReachingAHost(t *testing.T) {
	noSiteHosts(t)
	for args, want := range map[string]string{
		"home-b --host-gone --force":               "--host-gone cleans no host",
		"home-b --host-gone --ssh admin@192.0.2.1": "--host-gone cleans no host",
		"home-b --host-gone --delete-data":         "--host-gone and --delete-data cannot be used together",
		"home-b --host-gone --execute":             "stdin is not a terminal",
	} {
		err := runSiteRemove(append(strings.Fields(args), "--config", fixtureConfig()), strings.NewReader("home-b\n"), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", args, err, want)
		}
	}
}

// Every word an eviction asks for is typed in turn, from one reader, and a
// wrong one stops it.
func TestConfirmEvictionAsksForEachWord(t *testing.T) {
	saved := isTerminal
	isTerminal = func(*os.File) bool { return true }
	t.Cleanup(func() { isTerminal = saved })
	questions := []question{{word: "revoked", what: "the token"}, {word: "vm", what: "the site"}, {word: "lose writes", what: "the lag", danger: true}, {word: "lose objects", what: "the objects", danger: true}}
	for answers, want := range map[string]string{
		"revoked\nvm\nlose writes\nlose objects\n": "",
		"revoked\nvm\nyes\nlose objects\n":         `"yes" is not lose writes`,
		"vm\n":                                     `"vm" is not revoked`,
		"revoked\nvm\nlose writes\n":               "no answer read",
	} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		w.WriteString(answers)
		w.Close()
		var out bytes.Buffer
		err = confirmEviction(answerer{}, r, &out, questions)
		r.Close()
		switch {
		case want == "" && err != nil:
			t.Errorf("%q: %v", answers, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%q: err = %v, want %q", answers, err, want)
		}
		if want == "" && strings.Count(out.String(), "Type ") != 4 {
			t.Errorf("asked:\n%s", out.String())
		}
	}
	isTerminal = func(*os.File) bool { return false }
	r, w, _ := os.Pipe()
	w.WriteString("revoked\n")
	w.Close()
	defer r.Close()
	if err := confirmEviction(answerer{}, r, &bytes.Buffer{}, questions); err == nil || !strings.Contains(err.Error(), "not a terminal") {
		t.Errorf("without a terminal: %v", err)
	}
}

// The questions follow the plan: revoked only for a gateway, the site's name
// always, and the losses only when the plan has them.
func TestEvictionQuestionsFollowThePlan(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	var words []string
	for _, q := range evictionQuestions(cfg, "home-b", &siteremove.Plan{}) {
		words = append(words, q.word)
	}
	if strings.Join(words, ",") != "home-b" {
		t.Errorf("asked %v", words)
	}
}

// `revoked` is asked before the secrets file is read: the operator sets the
// new DNS token with `secrets set` while it waits, and the token checked and
// every secret written back must be that file's.
func TestAnEvictedGatewayIsAskedRevokedBeforeTheSecretsAreRead(t *testing.T) {
	noSiteHosts(t)
	saved := isTerminal
	isTerminal = func(*os.File) bool { return true }
	t.Cleanup(func() { isTerminal = saved })
	data, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(data), "  vm:\n", `  home-d:
    roles: [gateway, apps, witness]
    address: 10.44.0.6
    endpoint: home-d.example.org:51820
    ssh:
      host: home-d.example.org
      user: ubuntu
      keys: [alice, bob]
  vm:
`, 1)
	text = strings.ReplaceAll(text, "placement: { pinned: vm }", "placement: { pinned: home-d }")
	text = strings.Replace(text, "members: [home-a, home-b, vm]", "members: [home-a, home-b, home-d, vm]", 1)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "paisans-stack.yaml")
	if err := os.WriteFile(configPath, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString("revoked\n")
	w.Close()
	defer r.Close()
	var out bytes.Buffer
	err = runSiteRemove([]string{"vm", "--host-gone", "--execute", "--config", configPath, "--secrets", filepath.Join(dir, "missing.enc.yaml")}, r, &out)
	if err == nil {
		t.Fatal("an eviction with no secrets file went on")
	}
	if !strings.Contains(out.String(), "Type 'revoked'") {
		t.Errorf("the secrets were read before revoked was asked: %v\n%s", err, out.String())
	}
}

// The generated secrets are written back only to the file as it was read: a
// file changed since is refused, and keeps the change.
func TestWriteFilledRefusesAFileChangedSinceItWasRead(t *testing.T) {
	data, err := os.ReadFile(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "secrets.enc.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(data), "external:", "external:\n  acme_dns_token: \"the-new-one\"", 1)
	if changed == string(data) {
		t.Fatal("the fixture has no external section")
	}
	if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	err = writeFilled(ui.Discard, path, data, secrets, secretsgen.Result{Generated: []string{"sites.home-d.wireguard_private_key"}})
	if err == nil || !strings.Contains(err.Error(), "changed after it was read") {
		t.Fatalf("err = %v", err)
	}
	if now, _ := os.ReadFile(path); string(now) != changed {
		t.Error("the change made since the read was overwritten")
	}
}
