package kinds

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// testCatalog makes a git repository holding a catalog with one blueprint,
// journal, and returns its path and the commit.
func testCatalog(t *testing.T, extra map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"catalog.yaml":                         "apiVersion: paisans.catalog/v1\nname: example\n",
		"blueprints/journal/blueprint.yaml":    strings.Replace(minimal, "name: example", "name: journal", 1),
		"blueprints/journal/compose.yaml.tmpl": "services: {}\n",
	}
	for k, v := range extra {
		files[k] = v
	}
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("add", "-A")
	run("commit", "-q", "-m", "catalog")
	run("tag", "v1.0.0")
	return dir, run("rev-parse", "HEAD")
}

func catalogConfig(url, commit, tag, blueprint string) *config.Config {
	return &config.Config{
		Catalogs: map[string]config.Catalog{"example": {URL: url, Commit: commit, Tag: tag, Unsigned: true}},
		Apps:     map[string]config.App{"notes": {Kind: config.Kind(blueprint), Hostname: "notes.example.org"}},
	}
}

// A blueprint an app names from a declared catalog is fetched, read and
// recorded under its qualified name, and its templates are rendered from
// where it was fetched.
func TestACatalogBlueprintResolves(t *testing.T) {
	t.Setenv("PAISANS_CATALOG_CACHE", t.TempDir())
	url, commit := testCatalog(t, nil)
	if problems := resolve(catalogConfig(url, commit, "v1.0.0", "example/journal")); len(problems) > 0 {
		t.Fatal(problems)
	}
	d, ok := For("example/journal")
	if !ok || d.Port != 8000 || d.Reserved {
		t.Fatalf("record %+v, %v", d, ok)
	}
	var seen []string
	err := fs.WalkDir(Templates, "example/journal", func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			seen = append(seen, p)
		}
		return nil
	})
	if err != nil || len(seen) != 1 || seen[0] != "example/journal/compose.yaml.tmpl" {
		t.Errorf("templates %v, %v", seen, err)
	}
	if _, err := fs.ReadFile(Templates, "example/journal/blueprint.yaml"); err == nil {
		t.Error("the blueprint's description is readable as a template")
	}
	// The built in ones are still there beside it.
	if _, err := fs.Stat(Templates, "mbin/compose.yaml.tmpl"); err != nil {
		t.Error(err)
	}
}

// What a catalog cannot do is refused by name.
func TestACatalogMistakeIsRefused(t *testing.T) {
	t.Setenv("PAISANS_CATALOG_CACHE", t.TempDir())
	url, commit := testCatalog(t, map[string]string{
		"blueprints/pocket-id/blueprint.yaml": strings.Replace(minimal, "name: example", "name: pocket-id", 1),
	})
	cases := []struct {
		cfg  *config.Config
		want string
	}{
		{catalogConfig(url, commit, "", "example/pocket-id"), "reserved blueprint"},
		{catalogConfig(url, commit, "", "example/missing"), "no blueprint missing"},
		{catalogConfig(url, commit, "v9.9.9", "example/journal"), "no tag v9.9.9"},
		{catalogConfig(url, strings.Repeat("0", 40), "", "example/journal"), "has no commit"},
		{catalogConfig(filepath.Join(t.TempDir(), "absent"), strings.Repeat("1", 40), "", "example/journal"), "fetching"},
	}
	for _, c := range cases {
		problems := strings.Join(resolve(c.cfg), "\n")
		if !strings.Contains(problems, c.want) {
			t.Errorf("want %q, got %q", c.want, problems)
		}
	}
}

// A tag that has moved since the pin was written is refused: the pin names
// a commit, and the tag no longer does.
func TestAMovedTagIsRefused(t *testing.T) {
	t.Setenv("PAISANS_CATALOG_CACHE", t.TempDir())
	url, first := testCatalog(t, nil)
	if err := os.WriteFile(filepath.Join(url, "catalog.yaml"), []byte("apiVersion: paisans.catalog/v1\nname: example\n# moved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"commit", "-q", "-am", "moved"}, {"tag", "-f", "v1.0.0"}} {
		cmd := exec.Command("git", append([]string{"-C", url, "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s", out)
		}
	}
	problems := strings.Join(resolve(catalogConfig(url, first, "v1.0.0", "example/journal")), "\n")
	if !strings.Contains(problems, "not the pinned") {
		t.Errorf("got %q", problems)
	}
}

// A snapshot taken from a resolved configuration, installed on a machine
// that has never fetched the catalog, resolves the same blueprints with the
// catalog gone.
func TestASnapshotResolvesWithoutTheCatalog(t *testing.T) {
	t.Setenv("PAISANS_CATALOG_CACHE", t.TempDir())
	url, commit := testCatalog(t, nil)
	cfg := catalogConfig(url, commit, "", "example/journal")
	if p := resolve(cfg); len(p) > 0 {
		t.Fatal(p)
	}
	snap, err := Snapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap[commit+"/journal/blueprint.yaml"]; !ok || len(snap) != 2 {
		t.Fatalf("snapshot %v", snap)
	}
	// Another machine: a fresh cache, and the catalog deleted.
	t.Setenv("PAISANS_CATALOG_CACHE", t.TempDir())
	if err := os.RemoveAll(url); err != nil {
		t.Fatal(err)
	}
	if err := InstallSnapshot(snap); err != nil {
		t.Fatal(err)
	}
	if p := resolve(cfg); len(p) > 0 {
		t.Fatalf("with the snapshot installed: %v", p)
	}
	if dir, ok := Source("example/journal"); !ok || !strings.Contains(dir, "snapshots") {
		t.Errorf("resolved from %s", dir)
	}
	// A snapshot path that leaves its directory is refused.
	if err := InstallSnapshot(map[string][]byte{strings.Repeat("b", 40) + "/../../x": nil}); err == nil {
		t.Error("a path leaving the snapshot was installed")
	}
}
