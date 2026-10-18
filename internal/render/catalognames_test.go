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
)

// withNamesApp is the fixture with notes, an app of a catalog blueprint in
// the format that gives templates the display and community names, its
// .env printing both.
func withNamesApp(t *testing.T, displayName, community string) (*config.Config, *config.Secrets) {
	t.Helper()
	coretest.Cache(t)
	repo := filepath.Join(t.TempDir(), "paisans-example")
	for name, content := range map[string]string{
		"catalog.yaml": "apiVersion: paisans.catalog/v1\nname: example\n",
		"blueprints/journal/blueprint.yaml": `apiVersion: paisans.blueprint/v3
name: journal
services:
  - {name: app, image: "ghcr.io/example/journal:2.4.1"}
database: {engine: none}
port: 8300
config: {format: env, file: .env}
health: {path: /status, status: [200]}
`,
		"blueprints/journal/compose.yaml.tmpl":  "services: {}\n",
		"blueprints/journal/caddy.snippet.tmpl": "\n",
		"blueprints/journal/.env.secret.tmpl":   "NAMES={{ .DisplayName }}|{{ .CommunityName }}\n",
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
	cfg := fixture(t)
	cfg.Community.Name = community
	cfg.Catalogs = map[string]config.Catalog{"ex": {URL: repo, Commit: git("rev-parse", "HEAD")}}
	cfg.Apps["notes"] = config.App{Blueprint: "ex/journal", Hostname: "notes.example.org", DisplayName: displayName,
		Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-a"}}
	if problems := config.ResolveBlueprints(cfg); len(problems) > 0 {
		t.Fatal(problems)
	}
	return cfg, fixtureSecrets(t)
}

// A template reads the app's display name, the name its client has at the
// identity provider, and the community's name.
func TestTemplatesReadTheDisplayAndCommunityNames(t *testing.T) {
	for _, c := range []struct{ display, community, want string }{
		{"Notes", "Paisans", "NAMES=Notes|Paisans\n"},
		{"", "Paisans", "NAMES=notes|Paisans\n"},
		{"Notes", "", "NAMES=Notes|\n"},
	} {
		cfg, secrets := withNamesApp(t, c.display, c.community)
		plan, err := render.Build(cfg, secrets)
		if err != nil {
			t.Fatal(err)
		}
		if got := planFiles(plan)["home-a/srv/paisans/f2a9/notes/.env"]; got != c.want {
			t.Errorf("display %q, community %q: %q, want %q", c.display, c.community, got, c.want)
		}
	}
}
