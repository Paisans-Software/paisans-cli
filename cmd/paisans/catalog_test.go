package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/blueprints/coretest"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// catalogRepo makes a git repository holding a catalog with one blueprint,
// tagged v1.0.0, and returns its path and a function that commits a change to
// the blueprint and tags it.
func catalogRepo(t *testing.T) (string, func(image, tag string)) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "paisans-example")
	write := func(name, content string) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	spec := func(image string) string {
		return "apiVersion: paisans.blueprint/v1\nname: journal\nservices:\n  - {name: app, image: \"" + image + "\"}\ndatabase: {engine: none}\nport: 8300\nconfig: {format: env, file: .env}\nhealth: {path: /status, status: [200]}\n"
	}
	git := func(args ...string) {
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	write("catalog.yaml", "apiVersion: paisans.catalog/v1\nname: example\n")
	write("blueprints/journal/blueprint.yaml", spec("ghcr.io/example/journal:1"))
	write("blueprints/journal/compose.yaml.tmpl", "services: {}\n")
	write("blueprints/journal/caddy.snippet.tmpl", "\n")
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "one")
	git("tag", "v1.0.0")
	return dir, func(image, tag string) {
		write("blueprints/journal/blueprint.yaml", spec(image))
		git("commit", "-q", "-am", tag)
		git("tag", tag)
	}
}

// catalog add pins the newest tag, an app can then name its blueprint,
// catalog remove refuses while it does, and catalog update moves the pin
// and says what changed.
func TestTheCatalogCommands(t *testing.T) {
	coretest.Cache(t)
	var out bytes.Buffer
	reporterOverride = ui.NewPlain(&out, false)
	defer func() { reporterOverride = nil }()
	repo, release := catalogRepo(t)
	base, err := os.ReadFile(filepath.Join("..", "..", "internal", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), config.FileName)
	if err := os.WriteFile(path, base, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runCatalogAdd([]string{"ex", repo, "--config", path}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c := cfg.Catalogs["ex"]; c.Tag != "v1.0.0" || len(c.Commit) != 40 {
		t.Fatalf("pin %+v", c)
	}

	withApp := strings.Replace(string(mustRead(t, path)), "\napps:\n", "\napps:\n  notes:\n    blueprint: ex/journal\n    hostname: notes.example.org\n    placement: { pinned: vm }\n", 1)
	if err := os.WriteFile(path, []byte(withApp), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runCatalogRemove([]string{"ex", "--config", path}); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("a remove while in use: %v", err)
	}

	release("ghcr.io/example/journal:2", "v1.1.0")
	out.Reset()
	if err := runCatalogUpdate([]string{"--config", path}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "app runs ghcr.io/example/journal:2, was ghcr.io/example/journal:1") {
		t.Errorf("the update did not say what changed:\n%s", out.String())
	}
	cfg, err = config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Catalogs["ex"].Tag != "v1.1.0" {
		t.Errorf("pin %+v", cfg.Catalogs["ex"])
	}

	var list bytes.Buffer
	if err := runBlueprintList([]string{"--config", path}, &list); err != nil || !strings.Contains(list.String(), "ex/journal\tv1.1.0") || !strings.Contains(list.String(), "pocket-id\treserved") {
		t.Errorf("blueprint list: %v\n%s", err, list.String())
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// catalog add names a catalog <owner>/<name> from its repository unless told
// otherwise, reads <owner>/<name> alone as a GitHub repository, and refuses
// a repository not named paisans-<name>.
func TestCatalogAddNamesTheCatalog(t *testing.T) {
	for _, c := range []struct {
		args       []string
		name, url  string
		wantRefuse string
	}{
		{args: []string{"example/blueprints"}, name: "example/blueprints", url: "https://github.com/example/paisans-blueprints"},
		{args: []string{"Example/Media"}, name: "example/media", url: "https://github.com/Example/paisans-Media"},
		{args: []string{"https://github.com/example/paisans-blueprints.git"}, name: "example/blueprints", url: "https://github.com/example/paisans-blueprints.git"},
		{args: []string{"notes", "https://github.com/example/paisans-blueprints"}, name: "notes", url: "https://github.com/example/paisans-blueprints"},
		{args: []string{"https://github.com/example/blueprints"}, wantRefuse: "is not a catalog's repository"},
		{args: []string{"notes", "https://github.com/example/blueprints"}, wantRefuse: "is not a catalog's repository"},
		{args: []string{"Not A Name", "https://github.com/example/paisans-x"}, wantRefuse: "is not a catalog name"},
		{args: nil, wantRefuse: "catalog add takes a URL"},
	} {
		name, url, err := catalogAddArgs(c.args)
		switch {
		case c.wantRefuse != "":
			if err == nil || !strings.Contains(err.Error(), c.wantRefuse) {
				t.Errorf("%v: got %v, want %q", c.args, err, c.wantRefuse)
			}
		case err != nil || name != c.name || url != c.url:
			t.Errorf("%v: got %q %q %v, want %q %q", c.args, name, url, err, c.name, c.url)
		}
	}

	// Through the command: a URL alone pins the catalog under its default
	// name, and an app names a blueprint in it with three parts.
	coretest.Cache(t)
	reporterOverride = ui.NewPlain(&bytes.Buffer{}, false)
	defer func() { reporterOverride = nil }()
	repo, _ := catalogRepo(t)
	base, err := os.ReadFile(filepath.Join("..", "..", "internal", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), config.FileName)
	if err := os.WriteFile(path, base, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runCatalogAdd([]string{repo, "--config", path}); err != nil {
		t.Fatal(err)
	}
	want, _ := config.DefaultCatalogName(repo)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Catalogs[want]; !ok || !strings.HasSuffix(want, "/example") {
		t.Fatalf("catalogs %v, want %s", cfg.Catalogs, want)
	}
	withApp := strings.Replace(string(mustRead(t, path)), "\napps:\n", "\napps:\n  notes:\n    blueprint: "+want+"/journal\n    hostname: notes.example.org\n    placement: { pinned: vm }\n", 1)
	if err := os.WriteFile(path, []byte(withApp), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err != nil {
		t.Errorf("an app naming %s/journal: %v", want, err)
	}
}
