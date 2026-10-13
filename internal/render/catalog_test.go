package render_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

// A blueprint from a third party catalog renders like a built in one: the
// app's stack holds its templates, rendered with the same values, and the
// gateway routes to it.
func TestACatalogBlueprintRenders(t *testing.T) {
	t.Setenv("PAISANS_CATALOG_CACHE", t.TempDir())
	repo := t.TempDir()
	files := map[string]string{
		"catalog.yaml": "apiVersion: paisans.catalog/v1\nname: example\n",
		"blueprints/journal/blueprint.yaml": `apiVersion: paisans.blueprint/v1
name: journal
services:
  - {name: app, image: "ghcr.io/example/journal:2.4.1", purpose: the application}
database: {engine: none}
port: 8300
config: {format: env, file: .env}
health: {path: /status, status: [200]}
gate: {gateable: true}
`,
		"blueprints/journal/compose.yaml.tmpl": `name: {{ .Project }}
services:
  app:
    image: {{ .Image "app" }}
    ports:
      - "{{ .MeshAddress }}:{{ .App.Port }}:{{ .App.Port }}"
`,
		"blueprints/journal/.env.secret.tmpl":   "JOURNAL_URL=https://{{ .Hostname }}\n",
		"blueprints/journal/caddy.snippet.tmpl": "reverse_proxy {{ range .Upstreams }}{{ . }} {{ end }}\n",
	}
	for name, content := range files {
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
	commit := git("rev-parse", "HEAD")

	base, err := os.ReadFile(filepath.Join("testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Replace(string(base), "\napps:\n", "\ncatalogs:\n  example:\n    url: "+repo+"\n    commit: "+commit+"\n    unsigned: true\n\napps:\n  notes:\n    blueprint: example/journal\n    hostname: notes.example.org\n    placement: { pinned: vm }\n", 1)
	path := filepath.Join(t.TempDir(), config.FileName)
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if refusals := validate.Check(cfg).Refusals(); len(refusals) > 0 {
		t.Fatalf("refused: %v", refusals)
	}
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	got := planFiles(plan)
	var compose, env, caddy string
	for p, content := range got {
		switch {
		case strings.HasSuffix(p, "/notes/compose.yaml"):
			compose = content
		case strings.HasSuffix(p, "/notes/.env"):
			env = content
		case strings.HasSuffix(p, "Caddyfile") || strings.Contains(p, "notes.caddy"):
			caddy += content
		}
	}
	if !strings.Contains(compose, "ghcr.io/example/journal:2.4.1") || !strings.Contains(compose, ":8300:8300") {
		t.Errorf("compose:\n%s", compose)
	}
	if env != "JOURNAL_URL=https://notes.example.org\n" {
		t.Errorf(".env: %q", env)
	}
	if !strings.Contains(caddy, "notes.example.org") {
		t.Errorf("the gateway does not route notes.example.org")
	}
}
