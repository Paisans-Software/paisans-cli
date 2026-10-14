package appremove

import (
	"errors"
	"fmt"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/apply/applytest"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
)

const seed = "/srv/paisans/f2a9/health/monitors.json"

// watched is world with the monitor site applied while blog was still
// declared, so its seed carries blog' checks.
func watched(t *testing.T) (*config.Config, *config.Secrets, map[string]*host) {
	t.Helper()
	cfg, hosts := world(t)
	secrets, err := config.LoadSecrets(filepath.Join("..", "render", "testdata", "secrets.fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := render.Build(fixture(t), secrets)
	if err != nil {
		t.Fatal(err)
	}
	watch := hosts["watch"]
	var entries []render.ManifestFile
	for _, f := range before.Files {
		rel, ok := strings.CutPrefix(f.Path, "watch/")
		if !ok || rel == render.ManifestName || !strings.HasPrefix(rel, "srv/paisans/f2a9/health/") {
			continue
		}
		watch.files["/"+rel] = applytest.Pinned(f.Content)
		entries = append(entries, watch.entry("/"+rel, false))
	}
	watch.manifest(t, entries...)
	if !strings.Contains(watch.files[seed], "blog") {
		t.Fatal("the seed rendered with blog does not check it")
	}
	return cfg, secrets, hosts
}

func monitors(t *testing.T, cfg *config.Config, secrets *config.Secrets, hosts map[string]*host, p *Plan) {
	t.Helper()
	rendered, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	transports := map[string]apply.Transport{}
	for name, h := range hosts {
		transports[name] = h
	}
	if err := p.PlanMonitors(cfg, rendered, "", transports); err != nil {
		t.Fatal(err)
	}
}

// The monitor is reseeded last: once blog is off every site, watch's seed no
// longer checks it, and the monitor restarts on the new file. A second run
// finds the seed current and plans nothing for the monitor.
func TestTheRemovalReseedsTheMonitorLast(t *testing.T) {
	cfg, secrets, hosts := watched(t)
	p, err := Build(cfg, "blog", probe(t, cfg, "blog", hosts, false))
	if err != nil {
		t.Fatal(err)
	}
	monitors(t, cfg, secrets, hosts, p)
	rec := &ui.Recorder{Verbose_: true}
	p.Show(rec)
	if !rec.Has("section", "watch (the monitor)") || !rec.Has("detail", seed) {
		t.Fatalf("the plan does not show the reseed:\n%s", rec.Lines())
	}
	if err := executor(hosts).Execute(p); err != nil {
		t.Fatal(err)
	}
	watch := hosts["watch"]
	if strings.Contains(watch.files[seed], "blog") {
		t.Errorf("the monitor still checks blog:\n%s", watch.files[seed])
	}
	restarted := 0
	for _, c := range watch.sent {
		if strings.HasSuffix(c, "/health/compose.yaml restart") {
			restarted++
		}
	}
	if restarted != 1 {
		t.Errorf("the monitor was restarted %d time(s)", restarted)
	}
	for _, c := range hosts["home-a"].sent {
		if strings.Contains(c, "restart") {
			t.Errorf("a site other than the monitor's was restarted: %s", c)
		}
	}

	again, err := Build(cfg, "blog", probe(t, cfg, "blog", hosts, false))
	if err != nil {
		t.Fatal(err)
	}
	monitors(t, cfg, secrets, hosts, again)
	if len(again.Monitors) != 1 || again.Monitors[0].Pending() {
		t.Error("a reseeded monitor still plans work")
	}
	if err := executor(hosts).Execute(again); err != nil {
		t.Errorf("a second run fails the monitor's gate: %v", err)
	}
}

// A failed reseed comes after everything of the app's is gone, and names
// the apply that finishes it.
func TestAFailedReseedNamesTheApply(t *testing.T) {
	cfg, secrets, hosts := watched(t)
	hosts["watch"].fail = "/health/compose.yaml restart"
	p, err := Build(cfg, "blog", probe(t, cfg, "blog", hosts, false))
	if err != nil {
		t.Fatal(err)
	}
	monitors(t, cfg, secrets, hosts, p)
	rec := &ui.Recorder{}
	ex := executor(hosts)
	ex.Report = rec
	err = ex.Execute(p)
	// Only the monitor's own line fails: the removal step before it ended
	// done and is not marked for the reseed's error.
	for _, e := range rec.Events {
		if e.Kind == "fail" && !strings.HasPrefix(e.Text, "apply ") {
			t.Errorf("the failure marked %q, not the reseed:\n%s", e.Text, rec.Lines())
		}
	}
	var me *MonitorError
	if !errors.As(err, &me) || !strings.Contains(err.Error(), "paisans apply --site watch --only health --execute") {
		t.Fatalf("want the monitor's failure naming the apply, got %v", err)
	}
	if _, ok := hosts["home-a"].files[blogEnv]; ok {
		t.Error("the app's files were not removed before the monitor")
	}
}

// A deployment with no monitor site plans no reseed.
func TestNoMonitorSitePlansNoReseed(t *testing.T) {
	cfg, secrets, hosts := watched(t)
	delete(cfg.Sites, "watch")
	delete(cfg.Apps, "health")
	delete(hosts, "watch")
	p, err := Build(cfg, "blog", probe(t, cfg, "blog", hosts, false))
	if err != nil {
		t.Fatal(err)
	}
	monitors(t, cfg, secrets, hosts, p)
	if len(p.Monitors) != 0 {
		t.Errorf("monitors planned: %d", len(p.Monitors))
	}
}

// A monitor that does not come back healthy fails after everything of the
// app's is gone, with the cause read from its logs, the logs themselves only
// with -v, and the apply that finishes it.
func TestAnUnhealthyMonitorNamesItsCauseAndTheApply(t *testing.T) {
	cfg, secrets, hosts := watched(t)
	hosts["watch"].unhealthy = "health"
	p, err := Build(cfg, "blog", probe(t, cfg, "blog", hosts, false))
	if err != nil {
		t.Fatal(err)
	}
	monitors(t, cfg, secrets, hosts, p)
	err = executor(hosts).Execute(p)
	if err == nil {
		t.Fatal("an unhealthy monitor passed")
	}
	// as the command wraps it
	shortAndVerbose(t, fmt.Errorf("app remove: %w", err), "stack health is not healthy", "The app is removed", "paisans apply --site watch --only health --execute")
}
