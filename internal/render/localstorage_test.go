package render_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

// localStorageApps are the fixture's three apps of kinds that can use S3,
// each pinned and given `storage: local` by localStorageFixture.
var localStorageApps = []string{"talk", "docs", "blog"}

// localStorageFixture is the golden tree's deployment with every app that can
// use S3 pinned and keeping its files on its own disk instead, while the
// deployment still runs Garage. talk keeps its member gate, so the golden
// tree shows the media path it opens.
func localStorageFixture(t *testing.T) *config.Config {
	t.Helper()
	cfg := fixture(t)
	pins := map[string]string{"talk": "home-a", "docs": "home-b", "blog": "home-a"}
	for _, name := range localStorageApps {
		app := cfg.Apps[name]
		app.Placement = config.Placement{Mode: config.PlacementPinned, Site: pins[name], Literal: "pinned"}
		app.Storage = config.StorageLocal
		cfg.Apps[name] = app
	}
	if refusals := validate.Check(cfg).Refusals(); len(refusals) > 0 {
		t.Fatalf("the local storage fixture is refused: %v", refusals)
	}
	return cfg
}

// localStorageFiles is the part of the render a local storage app changes:
// its own stack directory on every site, and the gateway's Caddy.
func localStorageFiles(t *testing.T, cfg *config.Config) map[string]string {
	t.Helper()
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatalf("building the plan: %v", err)
	}
	out := map[string]string{}
	for _, f := range plan.Files {
		keep := strings.Contains(f.Path, "/infra/caddy/")
		for _, name := range localStorageApps {
			if strings.Contains(f.Path, "/srv/paisans/f2a9/"+name+"/") {
				keep = true
			}
		}
		if keep {
			out[f.Path] = f.Content
		}
	}
	return out
}

// The golden tree for apps keeping their files locally: Mbin, Outline and the
// writefreely-wisp fork each render their own local storage settings and no
// S3 ones, and the gateway routes no media hostname for any of them.
// Regenerated with -update, like the main golden tree.
func TestGoldenLocalStorage(t *testing.T) {
	files := localStorageFiles(t, localStorageFixture(t))
	golden := filepath.Join("testdata", "golden-local-storage")
	if *update {
		if err := os.RemoveAll(golden); err != nil {
			t.Fatal(err)
		}
		for path, content := range files {
			full := filepath.Join(golden, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		t.Log("local storage golden tree rewritten")
		return
	}
	dir := t.TempDir()
	for path, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	compareTrees(t, golden, dir)
}

// What the golden tree shows, asserted so that a regeneration that loses it
// fails rather than being read past.
func TestLocalStorageRendersNoObjectStorage(t *testing.T) {
	files := localStorageFiles(t, localStorageFixture(t))
	file := func(path string) string {
		t.Helper()
		content, ok := files[path]
		if !ok {
			t.Fatalf("%s was not rendered", path)
		}
		return content
	}

	talk := file("home-a/srv/paisans/f2a9/talk/.env")
	if !strings.Contains(talk, "KBIN_STORAGE_URL=https://talk.example.org/media\n") {
		t.Error("talk's media URL is not its own hostname under /media")
	}
	docs := file("home-b/srv/paisans/f2a9/docs/.env")
	if !strings.Contains(docs, "FILE_STORAGE=local\n") || !strings.Contains(docs, "FILE_STORAGE_LOCAL_ROOT_DIR=/var/lib/outline/data\n") {
		t.Error("docs does not render Outline's local file storage")
	}
	blog := file("home-a/srv/paisans/f2a9/blog/config.ini")
	if !strings.Contains(blog, "type = local\n") || !strings.Contains(blog, "dir = /data/uploads\n") {
		t.Error("blog does not render the fork's local image store")
	}
	for name, content := range map[string]string{"talk": talk, "docs": docs, "blog": blog} {
		for _, s3 := range []string{"S3_KEY=", "AWS_ACCESS_KEY_ID=", "s3_access_key_id =", "s3_endpoint =", "AWS_S3_UPLOAD_BUCKET_URL=", "-media.example.org"} {
			if strings.Contains(content, s3) {
				t.Errorf("%s keeps its files locally and still renders %s", name, s3)
			}
		}
	}

	for path := range files {
		for _, name := range localStorageApps {
			if strings.HasSuffix(path, "/snippets/"+name+"-"+kinds.MediaRole+".caddy") {
				t.Errorf("%s was rendered for an app with no bucket", path)
			}
		}
	}
	caddyfile := file("vm/srv/paisans/f2a9/infra/caddy/Caddyfile")
	for _, name := range localStorageApps {
		if strings.Contains(caddyfile, name+"-media.example.org") {
			t.Errorf("the gateway still routes %s-media.example.org", name)
		}
	}
	// talk is gated, and its images must still reach a federating server,
	// which has no gate session, as they would on a media hostname.
	block := caddyfile[strings.Index(caddyfile, "\ntalk.example.org {"):]
	block = block[:strings.Index(block, "\n}\n")]
	if !strings.Contains(block, "/media/*") {
		t.Errorf("talk is gated and keeps its media locally, and its host block does not open /media/*:\n%s", block)
	}
}
