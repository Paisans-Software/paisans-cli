package render_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/blueprints/coretest"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// notesToken is the heartbeat token the notes app pushes with in these tests.
const notesToken = "5555555555555555555555555555eeee"

// withHeartbeatApp is withMonitor with notes, an app of a catalog blueprint
// that declares a heartbeat, pinned to home-a, its token in the secrets, and
// its .env carrying .HeartbeatURLs as the wikijs bootstrap's does.
func withHeartbeatApp(t *testing.T) (*config.Config, *config.Secrets) {
	t.Helper()
	coretest.Cache(t)
	repo := filepath.Join(t.TempDir(), "paisans-example")
	for name, content := range map[string]string{
		"catalog.yaml": "apiVersion: paisans.catalog/v1\nname: example\n",
		"blueprints/journal/blueprint.yaml": `apiVersion: paisans.blueprint/v2
name: journal
services:
  - {name: app, image: "ghcr.io/example/journal:2.4.1"}
database: {engine: none}
port: 8300
config: {format: env, file: .env}
health: {path: /status, status: [200]}
heartbeat: {name: settings}
`,
		"blueprints/journal/compose.yaml.tmpl":  "services: {}\n",
		"blueprints/journal/caddy.snippet.tmpl": "\n",
		"blueprints/journal/.env.secret.tmpl":   "HEARTBEAT_URLS={{ .HeartbeatURLs }}\n",
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
	cfg, secrets := withMonitor(t, fixture(t).SMTP, nil)
	cfg.Catalogs = map[string]config.Catalog{"ex": {URL: repo, Commit: git("rev-parse", "HEAD")}}
	cfg.Apps["notes"] = config.App{Blueprint: "ex/journal", Hostname: "notes.example.org",
		Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-a"}}
	if problems := config.ResolveBlueprints(cfg); len(problems) > 0 {
		t.Fatal(problems)
	}
	secrets.Apps["notes"] = map[string]any{"heartbeat_token": notesToken}
	return cfg, secrets
}

// notesEnv is the notes app's rendered .env, which must be 0600: it carries
// the token in every URL.
func notesEnv(t *testing.T, cfg *config.Config, secrets *config.Secrets) string {
	t.Helper()
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range plan.Files {
		if f.Path == "home-a/srv/paisans/f2a9/notes/.env" {
			if f.Mode != 0o600 {
				t.Errorf("notes .env rendered %o", f.Mode)
			}
			return f.Content
		}
	}
	t.Fatal("no notes .env was rendered")
	return ""
}

// The heartbeat a catalog blueprint declares is one push monitor per app in
// the seed, named after the app, with the app's own token and the site
// heartbeat's schedule, grace and threshold.
func TestTheSeedExpectsACatalogBlueprintsHeartbeat(t *testing.T) {
	cfg, secrets := withHeartbeatApp(t)
	if refusals := validate.Check(cfg).Refusals(); len(refusals) > 0 {
		t.Fatalf("refused: %v", refusals)
	}
	m := byName(renderSeed(t, cfg, secrets))["notes — settings"]
	if m == nil {
		t.Fatal("no heartbeat for notes")
	}
	if m["monitor_type"] != "heartbeat" || m["heartbeat_token"] != notesToken {
		t.Errorf("notes heartbeat: %v", m)
	}
	if m["heartbeat_schedule_kind"] != "interval" || m["interval_seconds"] != float64(60) || m["heartbeat_grace_seconds"] != float64(120) || m["failure_threshold"] != float64(2) {
		t.Errorf("notes heartbeat schedule: %v", m)
	}
	if _, ok := m["url"]; ok {
		t.Errorf("notes heartbeat carries a url: %v", m)
	}
}

// The app's templates read every monitor's ping URL, space separated, in app
// order, the monitor's own site included, as the operators heartbeat's are.
func TestACatalogBlueprintsTemplatesReadItsHeartbeatURLs(t *testing.T) {
	cfg, secrets := withHeartbeatApp(t)
	if got := notesEnv(t, cfg, secrets); got != "HEARTBEAT_URLS=https://health.example.org/ping/"+notesToken+"\n" {
		t.Errorf("one monitor: %q", got)
	}

	watchB := cfg.Sites["watch"]
	watchB.Address = "10.44.0.5"
	watchB.Endpoint = "watch-b.example.org:51820"
	watchB.PublicAddress = "203.0.113.21"
	watchB.SSH.Host = "watch-b.example.org"
	cfg.Sites["watch-b"] = watchB
	cfg.Apps["health-b"] = config.App{Blueprint: config.BlueprintUptime, Hostname: "health-b.example.org",
		Placement: config.Placement{Mode: config.PlacementPinned, Site: "watch-b"},
		Settings:  map[string]any{"admin_group": "admins"}}
	secrets.Sites["watch-b"] = config.SiteSecrets{WireGuardPrivateKey: "REREREREREREREREREREREREREREREREREREREREREQ=", HeartbeatToken: "4444444444444444444444444444dddd", BeszelToken: "b4444444444444444444444444444ddd"}
	secrets.Apps["health-b"] = map[string]any{"admin_password": "fixture-admin-b", "session_secret": "fixture-session-b"}
	want := "HEARTBEAT_URLS=https://health.example.org/ping/" + notesToken + " https://health-b.example.org/ping/" + notesToken + "\n"
	if got := notesEnv(t, cfg, secrets); got != want {
		t.Errorf("two monitors: %q, want %q", got, want)
	}
	if byName(renderSeedAt(t, cfg, secrets, "watch-b/srv/paisans/f2a9/health-b/monitors.json"))["notes — settings"] == nil {
		t.Error("the second monitor does not expect notes' heartbeat")
	}
}

// With no monitor the URLs are empty, and nothing is pushed.
func TestACatalogBlueprintsHeartbeatURLsAreEmptyWithNoMonitor(t *testing.T) {
	cfg, secrets := withHeartbeatApp(t)
	delete(cfg.Apps, "health")
	if got := notesEnv(t, cfg, secrets); got != "HEARTBEAT_URLS=\n" {
		t.Errorf("no monitor: %q", got)
	}
}

// A token that is missing, or not what the monitor's seed accepts, is refused
// by name, with or without a monitor.
func TestACatalogBlueprintsHeartbeatTokenIsRequired(t *testing.T) {
	for _, monitor := range []bool{true, false} {
		for _, token := range []any{nil, "ABCDEF", strings.Repeat("A", 32)} {
			cfg, secrets := withHeartbeatApp(t)
			if !monitor {
				delete(cfg.Apps, "health")
			}
			if token == nil {
				delete(secrets.Apps["notes"], "heartbeat_token")
			} else {
				secrets.Apps["notes"]["heartbeat_token"] = token
			}
			_, err := render.Build(cfg, secrets)
			if err == nil || !strings.Contains(err.Error(), "secrets apps.notes.heartbeat_token") {
				t.Errorf("monitor %v, token %v: %v", monitor, token, err)
			}
		}
	}
}
