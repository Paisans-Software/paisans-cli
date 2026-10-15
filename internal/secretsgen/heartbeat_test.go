package secretsgen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/blueprints/coretest"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
)

// withHeartbeatApp is the fixture with notes, an app of a catalog blueprint
// that declares a heartbeat.
func withHeartbeatApp(t *testing.T) *config.Config {
	t.Helper()
	coretest.Cache(t)
	repo := filepath.Join(t.TempDir(), "paisans-example")
	for name, content := range map[string]string{
		"catalog.yaml":                         "apiVersion: paisans.catalog/v1\nname: example\n",
		"blueprints/journal/blueprint.yaml":    "apiVersion: paisans.blueprint/v2\nname: journal\nservices:\n  - {name: app, image: \"ghcr.io/example/journal:2.4.1\"}\ndatabase: {engine: none}\nport: 8300\nconfig: {format: env, file: .env}\nheartbeat: {name: settings}\n",
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
	cfg := load(t)
	cfg.Catalogs = map[string]config.Catalog{"ex": {URL: repo, Commit: git("rev-parse", "HEAD")}}
	cfg.Apps["notes"] = config.App{Blueprint: "ex/journal", Hostname: "notes.example.org",
		Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-a"}}
	if problems := config.ResolveBlueprints(cfg); len(problems) > 0 {
		t.Fatal(problems)
	}
	return cfg
}

// An app whose blueprint declares a heartbeat gets its token at init, in the
// site heartbeat token's shape, kept once set, listed as generated, and
// replaced in the same shape.
func TestFillGeneratesABlueprintsHeartbeatToken(t *testing.T) {
	cfg := withHeartbeatApp(t)
	secrets := &config.Secrets{}
	filled, err := secretsgen.Fill(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(filled.Generated, "apps.notes.heartbeat_token") {
		t.Fatalf("generated %v", filled.Generated)
	}
	shape := regexp.MustCompile(`^[0-9a-f]{32}$`)
	token, _ := secrets.Apps["notes"]["heartbeat_token"].(string)
	if !shape.MatchString(token) {
		t.Fatalf("apps.notes.heartbeat_token is %q, not 32 lowercase hex characters", token)
	}
	for name, app := range cfg.Apps {
		_, declares := blueprints.HeartbeatOf(app.Blueprint)
		if _, ok := secrets.Apps[name]["heartbeat_token"]; ok && !declares {
			t.Errorf("apps.%s, a %s, was given a heartbeat token", name, app.Blueprint)
		} else if !ok && declares {
			t.Errorf("apps.%s, a %s, which declares a heartbeat, was given no heartbeat token", name, app.Blueprint)
		}
	}
	if again, err := secretsgen.Fill(cfg, secrets); err != nil || again.Changed() {
		t.Fatalf("a second pass: %v %v", again.Generated, err)
	}
	if !slices.Contains(secretsgen.Declare(cfg, secrets), secretsgen.Declared{Path: "apps.notes.heartbeat_token", Source: secretsgen.Generated}) {
		t.Error("the token is not declared as generated")
	}
	next, err := secretsgen.ReplacementIn(cfg, "apps.notes.heartbeat_token", secrets)
	if err != nil || !shape.MatchString(next) || next == token {
		t.Errorf("replacement %q, %v", next, err)
	}
}
