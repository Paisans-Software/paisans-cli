package main

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/topology"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// siteApply is how deployment apply --site --execute ends once site's stacks are up,
// on the configuration at path: through rec, or, with inside, as a step of
// apply without --site does.
func siteApply(t *testing.T, path, site string, inside bool) (*ui.Recorder, error) {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := &ui.Recorder{}
	var r ui.Reporter = rec
	if inside {
		r = checkReporter{rec, rec, &[]checkReport{}}
	}
	return rec, siteApplied(r, cfg, path, site, "", 4, nil, false, nil)
}

// Until the owner's copy serves the app, deployment apply --site ends on the full
// apply's warning: every step ran, the exit status is 0, and the warning
// names what the owner installs.
func TestApplySiteWarnsUntilTheOwnersCopyServes(t *testing.T) {
	fakeWebServerCheck(t, certificateFails)
	path := externalFixture(t)
	h := fakeWebServerFiles(t, path)
	rec, err := siteApply(t, path, "watch", false)
	var w *ui.Warning
	if !errors.As(err, &w) || w.Hint != "Deployment complete, but 1 app failed its web server check: health" || !strings.Contains(w.Explain, "paisans web-server show --app health") {
		t.Fatalf("err %v\n%s", err, rec.Lines())
	}
	if reportError(io.Discard, err, false) != 0 {
		t.Errorf("a warning exits non-zero")
	}
	if _, ok := h.files[healthReference]; !ok {
		t.Errorf("wrote %v", h.written)
	}
	if rec.Has("result", "Applied") {
		t.Errorf("ended as applied:\n%s", rec.Lines())
	}
}

// Degraded, deployment apply --site ends on the web server warning under the degraded
// result line, which still says what was applied, and the warning says
// "Applied, but": the deployment is not complete. With --require-all the
// run fails, the warning folded into the failure.
func TestApplySiteEndsDegradedOnAWebServerWarning(t *testing.T) {
	fakeWebServerCheck(t, certificateFails)
	path := externalFixture(t)
	fakeWebServerFiles(t, path)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c := &topology.Classification{Sites: map[string]topology.Site{}}
	for _, name := range cfg.SiteNames() {
		c.Sites[name] = topology.Site{Name: name, Class: topology.Ready}
	}
	c.Sites["home-b"] = topology.Site{Name: "home-b", Class: topology.Pending, Why: "home-b: its host has not admitted this deployment"}
	rec := &ui.Recorder{}
	err = siteApplied(rec, cfg, path, "watch", "", 4, c, false, nil)
	var w *ui.Warning
	if !errors.As(err, &w) || w.Hint != "Applied, but 1 app failed its web server check: health" {
		t.Fatalf("err %v\n%s", err, rec.Lines())
	}
	if !rec.Has("result", "Applied, degraded: 1 site pending or down (home-b). 4 files applied to watch.") {
		t.Errorf("result:\n%s", rec.Lines())
	}
	err = siteApplied(&ui.Recorder{}, cfg, path, "watch", "", 4, c, true, nil)
	if err == nil || errors.As(err, &w) || !strings.Contains(problemText(err), "--require-all") || !strings.Contains(problemText(err), "web server check") {
		t.Errorf("--require-all: %v: %s", err, problemText(err))
	}
}

// What apply needs on the monitor and cannot read there is a failure, not
// the operator's web server: ✗, and the exit status is 1.
func TestApplySiteFailsWhereTheMonitorCannotBeRead(t *testing.T) {
	fakeWebServerCheck(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, path)
	f.unreachable = true
	rec, err := siteApply(t, path, "watch", false)
	var w *ui.Warning
	if err == nil || errors.As(err, &w) {
		t.Fatalf("err %v", err)
	}
	if hint, _ := ui.Describe(err); hint != "Deployment not complete: 1 web server step failed: reference files on watch" {
		t.Errorf("hint %q", hint)
	}
	if reportError(io.Discard, err, false) != 1 {
		t.Errorf("a failure exits 0")
	}
	if !rec.Has("fail", "reference files on watch") || rec.Has("result", "Applied") {
		t.Errorf("output:\n%s", rec.Lines())
	}
}

// deployment apply --site on a site without managed_by: external reaches no web
// server, and checks nothing.
func TestApplySiteWithoutAnExternalWebServerRunsNoWebServerStep(t *testing.T) {
	checked := fakeWebServerCheck(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, path)
	rec, err := siteApply(t, path, "home-a", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.ran) != 0 || f.writes != 0 || len(*checked) != 0 || rec.Has("section", "web server") {
		t.Errorf("ran %v, checked %v:\n%s", f.ran, *checked, rec.Lines())
	}
	if !rec.Has("result", "Applied 4 files to home-a.") {
		t.Errorf("output:\n%s", rec.Lines())
	}
}

// As a step of apply without --site, deployment apply --site leaves the web server
// to the run, which does it once at its end.
func TestApplySiteInsideTheFullApplyLeavesTheWebServerToIt(t *testing.T) {
	checked := fakeWebServerCheck(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, path)
	rec, err := siteApply(t, path, "watch", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.ran) != 0 || f.writes != 0 || len(*checked) != 0 || rec.Has("section", "web server") {
		t.Errorf("ran %v, checked %v:\n%s", f.ran, *checked, rec.Lines())
	}
}

// A full apply writes the reference file and checks the app once, at its end,
// although the deployment apply --site it runs for the monitor ends as that command
// does.
func TestApplyRunsTheWebServerStepOnce(t *testing.T) {
	fakeConverge(t, nil)
	checked := fakeWebServerCheck(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, path)
	fake := convergeRun
	convergeRun = func(args []string) error {
		if isApply(args) && slices.Contains(args, "watch") && slices.Contains(args, "--execute") {
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			return siteApplied(reporterOverride, cfg, path, "watch", "", 4, nil, false, nil)
		}
		return fake(args)
	}
	rec := recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatal(err)
	}
	steps := 0
	for _, e := range rec.Events {
		if e.Kind == "step" && e.Text == "reference file for health" {
			steps++
		}
	}
	if steps != 1 || !slices.Equal(f.written, []string{healthReference, manifestFile}) || !slices.Equal(*checked, []string{"health"}) {
		t.Errorf("reference file steps %d, wrote %v, checked %v:\n%s", steps, f.written, *checked, rec.Lines())
	}
}

// deployment apply --site's dry run lists the apps its web server step checks with
// --execute, as the full apply's does; a site without managed_by:
// external lists none, and nor does the dry run of a step of the full
// apply.
func TestApplySiteDryRunListsTheWebServerStep(t *testing.T) {
	quietSSH(t)
	path := externalFixture(t)
	for _, tc := range []struct {
		site   string
		inside bool
		want   bool
	}{
		{"watch", false, true},
		{"home-a", false, false},
		{"watch", true, false},
	} {
		rec := &ui.Recorder{}
		saved := reporterOverride
		reporterOverride = rec
		if tc.inside {
			reporterOverride = checkReporter{rec, rec, &[]checkReport{}}
		}
		captureStdout(t, func() {
			runApply([]string{"--config", path, "--secrets", fixtureSecretsPath(), "--site", tc.site}, strings.NewReader(""), io.Discard)
		})
		reporterOverride = saved
		got := lines(inSection(rec, "web server"))
		if tc.want && !slices.Equal(got, []string{"step health", "waiting health: --execute will check it", "result Nothing changed. Re-run with --execute to apply."}) {
			t.Errorf("%s: web server section:\n%s\n\n%s", tc.site, strings.Join(got, "\n"), rec.Lines())
		}
		if !tc.want && rec.Has("section", "web server") {
			t.Errorf("%s (inside %v): a web server section:\n%s", tc.site, tc.inside, rec.Lines())
		}
	}
}

// deployment apply --site --ssh's web server step reaches the monitor to read and write
// its reference files through the destination it was given, as the ssh
// user.
func TestApplySiteWebServerStepReachesTheSiteAsTheApplyDid(t *testing.T) {
	cfg := fixture(t)
	scope := webServerScope{site: "watch", ssh: "root@203.0.113.9"}
	applied := siteTransport("watch", cfg.Sites["watch"], scope.ssh)
	record, ok := scope.recordHost(cfg, "watch").(apply.SSHTransport)
	if !ok || record.Describe() != applied.Describe() || record.Sudo {
		t.Errorf("record through %#v", record)
	}
}
