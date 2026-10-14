package garage

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints/coretest"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// filesBlueprint stores objects in S3 and says nothing about public reads,
// which is a private bucket: public_reads is false unless a blueprint says
// otherwise. No core blueprint stores objects privately, so a catalog of the
// test's own carries one.
const filesBlueprint = `apiVersion: paisans.blueprint/v1
name: files
services:
  - {name: app, image: "ghcr.io/example/files:1"}
database: {engine: none}
port: 8500
config: {format: env, file: .env}
storage:
  s3: true
`

// loadWithPrivateFiles is the render fixture with an app named files on
// filesBlueprint, pinned to home-a, loaded through a catalog of the test's
// own. Its secrets are the caller's to give.
func loadWithPrivateFiles(t *testing.T) *config.Config {
	t.Helper()
	coretest.Cache(t)
	repo := filepath.Join(t.TempDir(), "paisans-example")
	for name, content := range map[string]string{
		"catalog.yaml":                        "apiVersion: paisans.catalog/v1\nname: example\n",
		"blueprints/files/blueprint.yaml":     filesBlueprint,
		"blueprints/files/compose.yaml.tmpl":  "services: {}\n",
		"blueprints/files/caddy.snippet.tmpl": "\n",
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
	base, err := os.ReadFile(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	app := "\napps:\n  files:\n    blueprint: ex/files\n    hostname: files.example.org\n    placement: { pinned: home-a }\n"
	doc := strings.Replace(string(base), "\napps:\n", "\ncatalogs:\n  ex:\n    url: "+repo+"\n    commit: "+git("rev-parse", "HEAD")+"\n"+app, 1)
	path := filepath.Join(t.TempDir(), config.FileName)
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// LoadWithPrivateFiles is loadWithPrivateFiles for the external tests.
var LoadWithPrivateFiles = loadWithPrivateFiles
