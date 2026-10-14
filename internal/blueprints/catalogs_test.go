package blueprints

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints/coretest"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// testCatalog makes a git repository holding a catalog with one blueprint,
// journal, and returns its path and the commit.
func testCatalog(t *testing.T, extra map[string]string) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "paisans-example")
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
		Catalogs: map[string]config.Catalog{"example": {URL: url, Commit: commit, Tag: tag}},
		Apps:     map[string]config.App{"notes": {Kind: config.Kind(blueprint), Hostname: "notes.example.org"}},
	}
}

// A blueprint an app names from a declared catalog is fetched, read and
// recorded under its qualified name, and its templates are rendered from
// where it was fetched.
func TestACatalogBlueprintResolves(t *testing.T) {
	coretest.Cache(t)
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
	coretest.Cache(t)
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
	coretest.Cache(t)
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
	coretest.Cache(t)
	url, commit := testCatalog(t, nil)
	cfg := catalogConfig(url, commit, "", "example/journal")
	if p := resolve(cfg); len(p) > 0 {
		t.Fatal(p)
	}
	snap, err := Snapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The journal's two files, and the core catalog's foundation.
	if _, ok := snap[commit+"/journal/blueprint.yaml"]; !ok || len(snap) != 3 {
		t.Fatalf("snapshot %v", snap)
	}
	// Another machine: a fresh cache, and the catalog deleted.
	coretest.Cache(t)
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

// The core catalog pinned to a release of its own replaces the newest
// release's blueprints with the ones that release holds.
func TestACorePinReplacesTheBuiltinCopy(t *testing.T) {
	coretest.Cache(t)
	url, commit := testCatalog(t, map[string]string{"foundation.yaml": testFoundation(t)})
	t.Cleanup(forgetCore)
	cfg := &config.Config{
		Catalogs: map[string]config.Catalog{"core": {URL: url, Tag: "v1.0.0", Commit: commit}},
		Apps:     map[string]config.App{"notes": {Kind: "journal", Hostname: "notes.example.org"}},
	}
	if problems := resolve(cfg); len(problems) > 0 {
		t.Fatalf("problems %v", problems)
	}
	cache, _ := filepath.EvalSymlinks(os.Getenv("PAISANS_CATALOG_CACHE"))
	if dir, ok := Source("journal"); !ok || !strings.HasPrefix(dir, cache) {
		t.Errorf("journal was not read from the core pin: %q %v", dir, ok)
	}
	if _, ok := For("mbin"); ok {
		t.Error("mbin, which the configuration's core pin does not hold, is still known")
	}
}

// forgetCore drops the core catalog read so far, so the next blueprint
// asked for reads its newest release again.
func forgetCore() {
	dropCore()
	setFoundation(nil)
	core.Lock()
	defer core.Unlock()
	core.done, core.err, core.release, core.skipped = false, nil, config.Catalog{}, nil
}

// testFoundation is the test copy's foundation.yaml.
func testFoundation(t testing.TB) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "blueprints", "testdata", "core", FoundationFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A fetch takes the commit's trees and checks out only the blueprints in
// use: a blueprint no app names is listed but its files are never fetched,
// and one a later configuration names is added to the same checkout.
func TestAFetchTakesOnlyTheBlueprintsInUse(t *testing.T) {
	coretest.Cache(t)
	src, commit := testCatalog(t, map[string]string{
		"blueprints/diary/blueprint.yaml":    strings.Replace(minimal, "name: example", "name: diary", 1),
		"blueprints/diary/compose.yaml.tmpl": "services: {}\n# diary\n",
	})
	// A local path is copied whole; file:// with filters allowed is fetched
	// as a forge would serve it.
	if out, err := exec.Command("git", "-C", src, "config", "uploadpack.allowFilter", "true").CombinedOutput(); err != nil {
		t.Fatalf("%s", out)
	}
	url := "file://" + src
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		delete(defs, "example/diary")
		delete(sources, "example/diary")
	})
	if p := resolve(catalogConfig(url, commit, "v1.0.0", "example/journal")); len(p) > 0 {
		t.Fatal(p)
	}
	dir, err := Fetch(config.Catalog{URL: url, Commit: commit})
	if err != nil {
		t.Fatal(err)
	}
	names, err := Blueprints(dir)
	if err != nil || strings.Join(names, ",") != "diary,journal" {
		t.Fatalf("blueprints %v, %v", names, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "blueprints", "diary")); err == nil {
		t.Error("diary, which no app names, was checked out")
	}
	blob, err := exec.Command("git", "-C", src, "rev-parse", commit+":blueprints/diary/compose.yaml.tmpl").Output()
	if err != nil {
		t.Fatal(err)
	}
	present := exec.Command("git", "-C", dir, "cat-file", "-e", strings.TrimSpace(string(blob)))
	present.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1")
	if err := present.Run(); err == nil {
		t.Error("diary's files were fetched, though no app names it")
	}
	if p := resolve(catalogConfig(url, commit, "v1.0.0", "example/diary")); len(p) > 0 {
		t.Fatal(p)
	}
	if got, ok := Source("example/diary"); !ok || got != filepath.Join(dir, "blueprints", "diary") {
		t.Errorf("diary resolved from %q, want the same checkout", got)
	}
	for _, name := range []string{"journal", "diary"} {
		if !checkedOut(dir, name) {
			t.Errorf("%s has no mark", name)
		}
	}
}

// A blueprint whose checkout was interrupted, so has no mark, is checked
// out again rather than used half done.
func TestAnUnmarkedBlueprintIsCheckedOutAgain(t *testing.T) {
	coretest.Cache(t)
	url, commit := testCatalog(t, nil)
	dir, err := Fetch(config.Catalog{URL: url, Commit: commit})
	if err != nil {
		t.Fatal(err)
	}
	if err := Checkout(dir, "journal"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "blueprints", "journal", "compose.yaml.tmpl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, ".git", stateDir, "complete", "journal")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFrom(dir, "journal"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "blueprints", "journal", "compose.yaml.tmpl")); err != nil {
		t.Errorf("the interrupted checkout was not redone: %v", err)
	}
	if err := Checkout(dir, "../journal"); err == nil {
		t.Error("a name leaving blueprints/ was checked out")
	}
}
