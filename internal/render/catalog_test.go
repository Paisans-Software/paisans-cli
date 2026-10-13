package render_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints/coretest"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

// A blueprint from a third party catalog renders like a built in one: the
// app's stack holds its templates, rendered with the same values, and the
// gateway routes to it.
func TestACatalogBlueprintRenders(t *testing.T) {
	coretest.Cache(t)
	repo := filepath.Join(t.TempDir(), "paisans-example")
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
	doc := strings.Replace(string(base), "\napps:\n", "\ncatalogs:\n  example:\n    url: "+repo+"\n    commit: "+commit+"\n\napps:\n  notes:\n    blueprint: example/journal\n    hostname: notes.example.org\n    placement: { pinned: vm }\n", 1)
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

// An app whose blueprint has no health route is watched by the fallback: a
// TCP connect on each site it runs on, and its front page answering below
// 500, in every monitor's seed.
func TestACatalogBlueprintWithoutHealthIsWatched(t *testing.T) {
	coretest.Cache(t)
	repo := filepath.Join(t.TempDir(), "paisans-example")
	for name, content := range map[string]string{
		"catalog.yaml":                          "apiVersion: paisans.catalog/v1\nname: example\n",
		"blueprints/journal/blueprint.yaml":     "apiVersion: paisans.blueprint/v1\nname: journal\nservices:\n  - {name: app, image: \"ghcr.io/example/journal:2.4.1\"}\ndatabase: {engine: none}\nport: 8300\nconfig: {format: env, file: .env}\n",
		"blueprints/journal/compose.yaml.tmpl":  "services: {}\n",
		"blueprints/journal/caddy.snippet.tmpl": "\n",
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
	base, err := os.ReadFile(filepath.Join("testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Replace(string(base), "\napps:\n", "\ncatalogs:\n  example:\n    url: "+repo+"\n    commit: "+git("rev-parse", "HEAD")+"\n\napps:\n  notes:\n    blueprint: example/journal\n    hostname: notes.example.org\n    placement: { pinned: vm }\n", 1)
	path := filepath.Join(t.TempDir(), config.FileName)
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	seeds := 0
	for p, content := range planFiles(plan) {
		if !strings.HasSuffix(p, "monitors.json") {
			continue
		}
		seeds++
		for _, want := range []string{"notes \u2014 reachable (vm)", "\"tcp_port\": 8300", "notes \u2014 public (no health route)", "200,301,302,303,307,308,401,403,404"} {
			if !strings.Contains(content, want) {
				t.Errorf("%s lacks %q", p, want)
			}
		}
	}
	if seeds == 0 {
		t.Fatal("the fixture renders no monitor seed")
	}
}

// A catalog blueprint's templates see the default it declares for a setting
// an app leaves out, and the app's own value for one it sets.
func TestACatalogBlueprintsTemplatesSeeItsDefaults(t *testing.T) {
	coretest.Cache(t)
	repo := filepath.Join(t.TempDir(), "paisans-example")
	for name, content := range map[string]string{
		"catalog.yaml": "apiVersion: paisans.catalog/v1\nname: example\n",
		"blueprints/journal/blueprint.yaml": `apiVersion: paisans.blueprint/v1
name: journal
services:
  - {name: app, image: "ghcr.io/example/journal:2.4.1"}
database: {engine: none}
port: 8300
config: {format: env, file: .env}
settings:
  title: {type: string, default: Notes}
  open: {type: bool, default: true}
`,
		"blueprints/journal/compose.yaml.tmpl":  "services: {}\n",
		"blueprints/journal/caddy.snippet.tmpl": "\n",
		"blueprints/journal/.env.tmpl":          "TITLE={{ .Setting \"title\" \"unset\" }}\nOPEN={{ .SettingBool \"open\" false }}\n",
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
	base, err := os.ReadFile(filepath.Join("testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	apps := "\napps:\n" +
		"  notes:\n    blueprint: example/journal\n    hostname: notes.example.org\n    placement: { pinned: vm }\n" +
		"  diary:\n    blueprint: example/journal\n    hostname: diary.example.org\n    placement: { pinned: home-a }\n    settings: { title: Mine, open: false }\n"
	doc := strings.Replace(string(base), "\napps:\n", "\ncatalogs:\n  example:\n    url: "+repo+"\n    commit: "+git("rev-parse", "HEAD")+"\n"+apps, 1)
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
	want := map[string]string{"/notes/.env": "TITLE=Notes\nOPEN=true\n", "/diary/.env": "TITLE=Mine\nOPEN=false\n"}
	got := planFiles(plan)
	for suffix, content := range want {
		found := false
		for p, c := range got {
			if strings.HasSuffix(p, suffix) {
				found = true
				if c != content {
					t.Errorf("%s: %q, want %q", p, c, content)
				}
			}
		}
		if !found {
			t.Errorf("no %s rendered", suffix)
		}
	}
	if s := cfg.Apps["notes"].Settings; s != nil {
		t.Errorf("the defaults were written into the configuration: %v", s)
	}
}
