package secretsrotate

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/blueprints/coretest"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// withNotes adds notes to the world: an app on home-a and home-b of a
// catalog blueprint that declares a heartbeat, its token in the file, and
// every monitor and both its sites rendered with it.
func (w *uptimeWorld) withNotes() *uptimeWorld {
	t := w.t
	t.Helper()
	coretest.Cache(t)
	repo := filepath.Join(t.TempDir(), "paisans-example")
	for name, content := range map[string]string{
		"catalog.yaml":                         "apiVersion: paisans.catalog/v1\nname: example\n",
		"blueprints/journal/blueprint.yaml":    "apiVersion: paisans.blueprint/v2\nname: journal\nservices:\n  - {name: app, image: \"example/journal:1\"}\ndatabase: {engine: none}\nport: 8300\nconfig: {format: env, file: .env}\nheartbeat: {name: settings}\n",
		"blueprints/journal/compose.yaml.tmpl": "services: {}\n",
	} {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "catalog")
	w.cfg.Catalogs = map[string]config.Catalog{"ex": {URL: repo, Commit: git("rev-parse", "HEAD")}}
	w.cfg.Apps["notes"] = config.App{Blueprint: "ex/journal", Hostname: "notes.example.org", Placement: config.Placement{Mode: config.PlacementCluster}}
	if problems := config.ResolveBlueprints(w.cfg); len(problems) > 0 {
		t.Fatal(problems)
	}
	w.store.s.Apps["notes"] = map[string]any{"heartbeat_token": strings.Repeat("cd", 16)}
	w.render(w.store.s, func(string) {})
	w.renderStack("home-a", "notes", w.store.s, func(string) {})
	w.renderStack("home-b", "notes", w.store.s, func(string) {})
	w.log = nil
	return w
}

// Every monitor learns the next token of an app's own heartbeat first, the
// one on a site the app runs on included, each healthy before the next, and
// the app's stack on each of its sites is recreated with it only once every
// monitor holds it.
func TestAppHeartbeatMonitorsLearnTheTokenBeforeTheApp(t *testing.T) {
	w := newUptimeWorld(t).withNotes()
	r := AppHeartbeat("notes")
	if r.Path() != "apps.notes.heartbeat_token" {
		t.Fatalf("path %q", r.Path())
	}
	if err := r.(Prechecker).Precheck(w.env()); err != nil {
		t.Fatal(err)
	}
	if got := usesOf(w.env(), r); !slices.Equal(got, []string{"home-c:health", "home-a:watch", "home-a:notes", "home-b:notes"}) {
		t.Fatalf("users %v, want every monitor, then notes on each of its sites", got)
	}
	old, _ := w.store.s.Get("apps.notes.heartbeat_token")
	if err := w.rotate(r); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"apply home-c health seed notes", "healthy home-c",
		"apply home-a watch seed notes", "healthy home-a",
		"apply home-a notes", "healthy home-a",
		"apply home-b notes", "healthy home-b",
	}
	if !slices.Equal(w.log, want) {
		t.Fatalf("log\n%v\nwant\n%v", w.log, want)
	}
	next, _ := w.store.s.Get("apps.notes.heartbeat_token")
	if next == old || !heartbeatShape.MatchString(next) {
		t.Fatalf("the file holds a token of %d characters, not a new one of 32 lowercase hex", len(next))
	}
	for _, site := range []string{"home-a", "home-b"} {
		at := w.notesSeededAtPush[site]
		if at["health/notes"] != next || at["watch/notes"] != next {
			t.Fatalf("notes on %s pushed the next token before every monitor held it", site)
		}
		if w.notesPushed[site] != next {
			t.Fatalf("notes on %s does not push the promoted token", site)
		}
	}
	if len(w.store.s.Rotations) != 0 {
		t.Fatalf("rotations left: %v", w.store.s.Rotations)
	}
}

// With no monitor nothing renders the token: it is replaced in the file and
// handed to nobody.
func TestAppHeartbeatWithNoMonitor(t *testing.T) {
	w := newUptimeWorld(t).withNotes()
	delete(w.cfg.Apps, "health")
	delete(w.cfg.Apps, "watch")
	r := AppHeartbeat("notes")
	if got := usesOf(w.env(), r); len(got) != 0 {
		t.Fatalf("users %v, want none", got)
	}
	old, _ := w.store.s.Get("apps.notes.heartbeat_token")
	if err := w.rotate(r); err != nil {
		t.Fatal(err)
	}
	if len(w.log) != 0 {
		t.Fatalf("log %v, want nothing applied", w.log)
	}
	if next, _ := w.store.s.Get("apps.notes.heartbeat_token"); next == old || !heartbeatShape.MatchString(next) {
		t.Fatal("the token was not replaced in the file")
	}
}

// An app whose blueprint declares no heartbeat has no token to rotate.
func TestAppHeartbeatPrecheckRefusesAnotherBlueprint(t *testing.T) {
	w := newUptimeWorld(t).withNotes()
	err := AppHeartbeat("forum").(Prechecker).Precheck(w.env())
	if err == nil || !strings.Contains(err.Error(), "declares no heartbeat") {
		t.Fatalf("%v", err)
	}
}

func TestAppHeartbeatCostStatesTheRefusedPush(t *testing.T) {
	c := AppHeartbeat("notes").Cost()
	if c.SignsOut || c.Reconnects {
		t.Fatalf("cost %+v: a heartbeat token signs nobody out", c)
	}
	if !strings.Contains(c.Downtime, "notes heartbeat") || !strings.Contains(c.Downtime, "refuse") || !strings.Contains(c.Downtime, "grace") {
		t.Fatalf("downtime %q", c.Downtime)
	}
}
