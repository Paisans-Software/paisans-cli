package main

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/acme"
	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/topology"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// classifyHost is a site's host as classification reads it: a login that
// lets the run in or refuses it, and files. etcd answers that none runs, and
// every other command fails, which Gather reads as nothing on a host that
// holds no record of having run it.
type classifyHost struct {
	login error
	files map[string]string
}

func (h classifyHost) Describe() string { return "fake" }

func (h classifyHost) Run(command string) (string, error) {
	switch {
	case h.login != nil:
		return "", h.login
	case command == "true":
		return "", nil
	case strings.Contains(command, "ps --status running"):
		return "__PAISANS_NO_ETCD__\n", nil
	}
	return "", fmt.Errorf("fake: no answer for %q", command)
}

func (h classifyHost) RunInput(command, _ string) (string, error) { return h.Run(command) }

func (h classifyHost) ReadFile(path string) (string, bool, error) {
	if h.login != nil {
		return "", false, h.login
	}
	content, ok := h.files[path]
	return content, ok, nil
}

func (h classifyHost) WriteFile(string, string, uint32) error {
	return errors.New("fake: classification writes nothing")
}

// admittedAs is the record admit writes for site in cfg, with every grant
// its roles need.
func admittedAs(cfg *config.Config, site string) string {
	req, err := admission.Build(cfg, site)
	if err != nil {
		panic(err)
	}
	rec := admission.NewRecord(req, req.Grants, "", time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC))
	if cfg.Sites[site].WebServerManagedBy() == config.ManagedByExternal {
		rec.WebServer = &admission.WebServer{Server: "caddy", Process: "caddy"}
	}
	b, err := admission.EncodeRecord(rec)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// classifyHosts is every declared site's host, each admitted as its site,
// but those refused, whose login is refused, and the gateways holding a
// deployment record listing joined, when any is given.
func classifyHosts(refused []string, joined ...string) func(*config.Config) map[string]apply.Transport {
	return func(cfg *config.Config) map[string]apply.Transport {
		out := map[string]apply.Transport{}
		for _, name := range cfg.SiteNames() {
			h := classifyHost{files: map[string]string{}}
			if slices.Contains(refused, name) {
				h.login = fmt.Errorf("%s: %w", name, apply.ErrLoginRefused)
			} else {
				h.files[admittedPath(cfg)] = admittedAs(cfg, name)
			}
			if len(joined) > 0 && cfg.Sites[name].Has(config.RoleGateway) {
				h.files[deployrecord.Path(cfg.Deployment())] = deployrecord.Encode(deployrecord.Record{Version: deployrecord.Version, Sites: joined})
			}
			out[name] = h
		}
		return out
	}
}

// classifyWith makes the sites classified from hosts.
func classifyWith(t *testing.T, hosts func(*config.Config) map[string]apply.Transport) {
	t.Helper()
	saved := classifyTransports
	classifyTransports = hosts
	t.Cleanup(func() { classifyTransports = saved })
}

// classified sets the run's classification to c, as a run that has
// classified does.
func classified(t *testing.T, c *topology.Classification) {
	t.Helper()
	saved := runSites
	runSites = c
	t.Cleanup(func() { runSites = saved })
}

func degraded(t *testing.T) *topology.Classification {
	t.Helper()
	cfg := fixture(t)
	c := &topology.Classification{Sites: map[string]topology.Site{}}
	for _, name := range cfg.SiteNames() {
		c.Sites[name] = topology.Site{Name: name, Class: topology.Ready}
	}
	c.Sites["home-b"] = topology.Site{Name: "home-b", Class: topology.Pending, Why: "home-b: its host has not admitted this deployment", Request: true}
	return c
}

// The plan opens with a pending section: a line per pending site with its
// reason and its request line, and what the deployment is without.
func TestThePendingSection(t *testing.T) {
	cfg := fixture(t)
	r := &ui.Recorder{Verbose_: true}
	showPending(r, cfg, degraded(t))
	out := r.Lines()
	for _, want := range []string{"pending", "home-b: its host has not admitted this deployment", "paisans site request --site home-b > home-b.admit", "without"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
}

// A site pending only because its route into the mesh is through a pending
// one is not sent to ask its host again: its host may have admitted it.
func TestThePendingSectionAsksOnlyWhereTheHostMust(t *testing.T) {
	c := degraded(t)
	c.Sites["watch"] = topology.Site{Name: "watch", Class: topology.Pending, Why: "watch: its only route into the mesh is through home-b, which is pending"}
	r := &ui.Recorder{Verbose_: true}
	showPending(r, fixture(t), c)
	out := r.Lines()
	if strings.Contains(out, "--site watch > watch.admit") || !strings.Contains(out, "only route into the mesh") {
		t.Errorf("got:\n%s", out)
	}
}

// The degraded ending, and --require-all making it exit 1.
func TestTheDegradedEnding(t *testing.T) {
	c := degraded(t)
	if got := degradedResult(c); got != "Applied, degraded: 1 site pending or down (home-b)" {
		t.Errorf("got %q", got)
	}
	if err := requireAll(c, false); err != nil {
		t.Errorf("without --require-all: %v", err)
	}
	err := requireAll(c, true)
	var w *ui.Warning
	if err == nil || errors.As(err, &w) {
		t.Errorf("with --require-all a degraded run must fail, not warn: %v", err)
	}
}

// deployment apply --site on a pending site is refused before anything is read.
func TestApplySiteRefusesAPendingSite(t *testing.T) {
	classified(t, degraded(t))
	var err error
	captureStdout(t, func() {
		err = runApply([]string{"--config", fixtureConfig(), "--secrets", fixtureSecretsPath(), "--site", "home-b"}, nil, nil)
	})
	if err == nil || !strings.Contains(problemText(err), "--site home-b") {
		t.Errorf("got %v: %s", err, problemText(err))
	}
}

// deployment apply --site alone classifies every site itself, and refuses a site that
// is pending.
func TestApplySiteAloneClassifies(t *testing.T) {
	classifyWith(t, classifyHosts([]string{"home-b"}))
	var err error
	captureStdout(t, func() {
		err = runApply([]string{"--config", fixtureConfig(), "--secrets", fixtureSecretsPath(), "--site", "home-b"}, nil, nil)
	})
	if err == nil || !strings.Contains(problemText(err), "--site home-b") {
		t.Errorf("got %v: %s", err, problemText(err))
	}
	if runSites != nil {
		t.Error("a command run alone left its classification for the next")
	}
}

// A run classifies once: a step reuses the run's classification instead of
// reading every site again.
func TestARunClassifiesOnce(t *testing.T) {
	cfg := fixture(t)
	reads := 0
	classifyWith(t, func(*config.Config) map[string]apply.Transport { reads++; return nil })
	classified(t, degraded(t))
	if _, _, err := effectiveConfig(&ui.Recorder{}, cfg, ""); err != nil {
		t.Fatal(err)
	}
	if reads != 0 {
		t.Errorf("a step read the sites again (%d)", reads)
	}
}

// A refusal from topology stops a step: the gateway down is never a site to
// do without.
func TestARefusalIsNeverPermissionToGoOn(t *testing.T) {
	cfg := fixture(t)
	c := degraded(t)
	c.Sites["vm"] = topology.Site{Name: "vm", Class: topology.Down, Why: topology.DownLine("vm")}
	classified(t, c)
	if got, _, err := effectiveConfig(&ui.Recorder{}, cfg, ""); err == nil || got != nil {
		t.Errorf("the gateway down was not refused: %v", err)
	}
}

// The apply of one site records in its manifest the sites pending in the
// run.
func TestTheManifestRecordsTheRunsPending(t *testing.T) {
	cfg := fixture(t).WithoutJoining("home-b")
	secrets := &config.Secrets{Version: 1}
	if _, err := secretsgen.Fill(cfg, secrets); err != nil {
		t.Fatal(err)
	}
	// The fixture's Beszel hub lists every site by its token.
	for name, site := range secrets.Sites {
		site.BeszelToken = "b0000000000000000000000000000aaab0000000000000000000000000000aaa"
		secrets.Sites[name] = site
	}
	rendered, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := apply.Build("home-a", rendered, acme.Module(cfg.ACME.Provider), emptyHost{}, pendingOptions(degraded(t))...)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Pending, []string{"home-b"}) {
		t.Errorf("pending %v", plan.Pending)
	}
}

// The deployment record lists the sites of the effective topology: a site
// never admitted is not recorded as joined, or the next run would classify
// it down instead of pending.
func TestTheRecordListsTheEffectiveSites(t *testing.T) {
	cfg := fixture(t)
	gw := &recordFake{files: map[string]string{}}
	if err := recordApplied(&ui.Recorder{}, cfg, degraded(t), "vm", func(string) registry.Runner { return gw }); err != nil {
		t.Fatal(err)
	}
	rec, found, _ := readRecord(gw, cfg.Deployment())
	if !found || rec.Lists("sites", "home-b") || !rec.Lists("sites", "home-a") {
		t.Errorf("%+v %v", rec, found)
	}
}

// With storage held back, no storage step runs; and a step about a site
// that is not ready is not run.
func TestTheRunSkipsWhatItCannotDo(t *testing.T) {
	declared := fixture(t)
	c := degraded(t)
	c.Held = []string{"talk"}
	c.Warnings = []string{"storage held back"}
	cfg := c.Effective(declared)
	steps := runOn(cfg, c, convergePlan(cfg, convergeState{}))
	for _, s := range steps {
		if s.Stage == stageStorage || strings.Contains(s.Title, "home-b") {
			t.Errorf("planned %s", s.Title)
		}
	}
	c.Held, c.Warnings = nil, nil
	c.Sites["home-b"] = topology.Site{Name: "home-b", Class: topology.Down, Why: topology.DownLine("home-b")}
	cfg = c.Effective(declared)
	steps = runOn(cfg, c, convergePlan(cfg, convergeState{}))
	for _, s := range steps {
		if s.Stage == stageStorage || stepSite(s) == "home-b" {
			t.Errorf("planned %s with home-b down", s.Title)
		}
	}
}

// storage init and storage add refuse while storage waits: Garage held back
// for want of nodes, or a Garage node down.
func TestStorageWaitsForItsSites(t *testing.T) {
	cfg := fixture(t)
	c := degraded(t)
	c.Held, c.Warnings = []string{"talk"}, []string{"storage held back"}
	classified(t, c)
	if _, err := storageConfig(&ui.Recorder{}, cfg, ""); err == nil {
		t.Error("storage add went on with storage held back")
	}
	down := degraded(t)
	down.Sites["home-b"] = topology.Site{Name: "home-b", Class: topology.Down, Why: topology.DownLine("home-b")}
	classified(t, down)
	if _, err := storageConfig(&ui.Recorder{}, cfg, ""); err == nil || !strings.Contains(problemText(err), "home-b is down") {
		t.Errorf("storage add went on with a Garage node down: %v", err)
	}
	ready := degraded(t)
	ready.Sites["home-b"] = topology.Site{Name: "home-b", Class: topology.Ready}
	classified(t, ready)
	if _, err := storageConfig(&ui.Recorder{}, cfg, "home-a"); err != nil {
		t.Errorf("every site ready: %v", err)
	}
}

// deployment apply --site alone ends degraded in one line, and --require-all fails it;
// a warning it ends on is folded into that failure, never left to exit 0.
func TestApplySiteEndsDegraded(t *testing.T) {
	cfg := fixture(t)
	rec := &ui.Recorder{}
	if err := siteApplied(rec, cfg, fixtureConfig(), "home-a", "", 3, degraded(t), false, nil); err != nil {
		t.Fatal(err)
	}
	if !rec.Has("result", "Applied, degraded: 1 site pending or down (home-b). 3 files applied to home-a.") {
		t.Errorf("result:\n%s", rec.Lines())
	}
	err := siteApplied(&ui.Recorder{}, cfg, fixtureConfig(), "home-a", "", 3, degraded(t), true, nil)
	var w *ui.Warning
	if err == nil || errors.As(err, &w) {
		t.Errorf("--require-all: %v", err)
	}
	warning := &ui.Warning{Problem: ui.Problem{Hint: "1 app failed its web server check"}}
	rec = &ui.Recorder{}
	err = degradedEnd(rec, degraded(t), true, warning)
	if err == nil || errors.As(err, &w) || !strings.Contains(problemText(err), "web server check") || !rec.Has("result", "Applied, degraded") {
		t.Errorf("a warning under --require-all: %v: %s\n%s", err, problemText(err), rec.Lines())
	}
	if err := degradedEnd(&ui.Recorder{}, degraded(t), false, warning); !errors.As(err, &w) {
		t.Errorf("without --require-all the warning stays a warning: %v", err)
	}
}

// deployment apply --site naming a down site is refused, as a pending one is.
func TestApplySiteRefusesADownSite(t *testing.T) {
	c := degraded(t)
	c.Sites["home-b"] = topology.Site{Name: "home-b", Class: topology.Down, Why: topology.DownLine("home-b")}
	classified(t, c)
	var err error
	captureStdout(t, func() {
		err = runApply([]string{"--config", fixtureConfig(), "--secrets", fixtureSecretsPath(), "--site", "home-b"}, nil, nil)
	})
	if err == nil || !strings.Contains(problemText(err), "--site home-b") || !strings.Contains(problemText(err), "evicted") {
		t.Errorf("got %v: %s", err, problemText(err))
	}
}

// site add names the site as it was typed, as an argument, when it is not
// ready; and refuses plainly while a joined site is down, since a join
// changes every site's mesh file and a down one cannot take it.
func TestSiteAddRefusesWithoutEverySite(t *testing.T) {
	classified(t, degraded(t))
	err := runSiteAdd([]string{"home-b", "--config", fixtureConfig(), "--secrets", fixtureSecretsPath()}, nil, nil)
	if err == nil || !strings.Contains(problemText(err), "site add home-b:") || strings.Contains(problemText(err), "--site") {
		t.Errorf("pending: %v: %s", err, problemText(err))
	}
	c := degraded(t)
	c.Sites["home-b"] = topology.Site{Name: "home-b", Class: topology.Ready}
	c.Sites["watch"] = topology.Site{Name: "watch", Class: topology.Down, Why: topology.DownLine("watch")}
	classified(t, c)
	err = runSiteAdd([]string{"home-b", "--config", fixtureConfig(), "--secrets", fixtureSecretsPath()}, nil, nil)
	if err == nil || !strings.Contains(problemText(err), "every joined site answers") || !strings.Contains(problemText(err), "watch") {
		t.Errorf("down: %v: %s", err, problemText(err))
	}
}

// A dry run of a degraded deployment with --require-all exits 1 as the run
// would, so automation that gates on a dry run does not pass on one: apply
// without --site, and deployment apply --site alone.
func TestADryRunHonoursRequireAll(t *testing.T) {
	fakeConverge(t, nil)
	classifyWith(t, classifyHosts([]string{"home-b"}))
	recordConverge(t)
	if err := converge(t); err != nil {
		t.Fatalf("without --require-all: %v: %s", err, problemText(err))
	}
	if err := converge(t, "--require-all"); err == nil || !strings.Contains(problemText(err), "--require-all") {
		t.Errorf("apply: %v", err)
	}

	quietSSH(t)
	classified(t, degraded(t))
	path := externalFixture(t)
	rec := &ui.Recorder{}
	saved := reporterOverride
	reporterOverride = rec
	t.Cleanup(func() { reporterOverride = saved })
	var err error
	captureStdout(t, func() {
		err = runApply([]string{"--config", path, "--secrets", fixtureSecretsPath(), "--site", "watch", "--require-all"}, strings.NewReader(""), io.Discard)
	})
	if err == nil || !strings.Contains(problemText(err), "--require-all") || !rec.Has("result", "Nothing changed") {
		t.Errorf("deployment apply --site: %v: %s\n%s", err, problemText(err), rec.Lines())
	}
}

// A site whose host is switched off is classified down on one connection
// attempt, not three each waiting out ssh's connect timeout, and no later
// command of the run to it tries ssh again.
func TestASwitchedOffSiteIsTriedOnceARun(t *testing.T) {
	cfg := fixture(t)
	resetLogins(t)
	answerQuietly(t, "", "")
	t.Cleanup(apply.SetSleep(func(time.Duration) {}))
	tries := 0
	t.Cleanup(apply.SetRunSSH(func(cmd *exec.Cmd) error {
		if strings.Contains(strings.Join(cmd.Args, " "), "@home-b.local") {
			tries++
			io.WriteString(cmd.Stdout, "ssh: connect to host home-b.local port 22: Operation timed out\n")
			return sshExit(255)
		}
		out, exit, ok := quietAnswer(cmd.Args, "", "")
		if !ok {
			return cmd.Run()
		}
		io.WriteString(cmd.Stdout, out)
		if exit != 0 {
			return sshExit(exit)
		}
		return nil
	}))
	classifyWith(t, func(cfg *config.Config) map[string]apply.Transport {
		out := map[string]apply.Transport{}
		for _, name := range cfg.SiteNames() {
			out[name] = siteTransport(name, cfg.Sites[name], "")
		}
		return out
	})
	classified(t, nil)
	c, err := classifySites(ui.NewPlain(io.Discard, false), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Sites["home-b"]; got.Class == topology.Ready {
		t.Errorf("home-b is %s, want it not ready", got.Class)
	}
	if tries != 1 {
		t.Errorf("classification tried home-b %d times, want once", tries)
	}
	if _, err := siteTransport("home-b", cfg.Sites["home-b"], "").Run("true"); !errors.Is(err, apply.ErrUnreachable) {
		t.Errorf("a later command: %v, want ErrUnreachable", err)
	}
	if tries != 1 {
		t.Errorf("home-b was tried %d times in the run, want once", tries)
	}
}
