package blueprints

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints/coretest"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// The official core catalog is one repository, written the ways git accepts
// for it; anything else, a fork or a local copy included, is not it.
func TestOfficialCoreURL(t *testing.T) {
	for url, want := range map[string]bool{
		"https://github.com/Paisans-Software/paisans-blueprints":             true,
		"https://github.com/Paisans-Software/paisans-blueprints/":            true,
		"https://github.com/Paisans-Software/paisans-blueprints.git":         true,
		"https://github.com/Paisans-Software/paisans-blueprints.git/":        true,
		"https://GitHub.com/Paisans-Software/paisans-blueprints":             true,
		"https://github.com/Paisans-Software/paisans-blueprints-fork":        false,
		"https://github.com/Paisans-Software/paisans-blueprints/x":           false,
		"https://github.com/paisans-software/paisans-blueprints":             false,
		"https://github.com/example/paisans-blueprints":                      false,
		"https://github.com.example.org/Paisans-Software/paisans-blueprints": false,
		"https://github.com:443/Paisans-Software/paisans-blueprints":         false,
		"https://user@github.com/Paisans-Software/paisans-blueprints":        false,
		"https://github.com/Paisans-Software/paisans-blueprints?x=1":         false,
		"https://github.com/Paisans-Software/%70aisans-blueprints":           false,
		"http://github.com/Paisans-Software/paisans-blueprints":              false,
		"git@github.com:Paisans-Software/paisans-blueprints.git":             false,
		"file:///srv/mirror/paisans-blueprints":                              false,
		" https://github.com/Paisans-Software/paisans-blueprints":            false,
		"": false,
	} {
		if got := officialCoreURL(url); got != want {
			t.Errorf("%q: official %v, want %v", url, got, want)
		}
	}
}

// The toolkit's own blueprints are the reserved ones and the core catalog's,
// both written unqualified; a catalog's is written <catalog>/<name>.
func TestToolkitBlueprints(t *testing.T) {
	if err := LoadCore(""); err != nil {
		t.Fatal(err)
	}
	for blueprint, want := range map[config.Blueprint]bool{
		config.BlueprintPocketID:  true,
		"mbin":                    true,
		"example/journal":         false,
		"example/blueprints/mbin": false,
		"nonesuch":                false,
	} {
		if got := ToolkitBlueprint(blueprint); got != want {
			t.Errorf("%s: toolkit's own %v, want %v", blueprint, got, want)
		}
	}
}

// pinCore resolves a configuration that pins the core catalog to commit,
// with no url, and returns the foundation's etcd it then reads.
func pinCore(t *testing.T, commit string) string {
	t.Helper()
	t.Cleanup(forgetCore)
	forgetCore()
	cfg := &config.Config{Catalogs: map[string]config.Catalog{config.CoreCatalog: {Commit: commit}}}
	if problems := resolve(cfg); len(problems) > 0 {
		t.Fatalf("problems %v", problems)
	}
	etcd, err := FoundationImage(FoundationEtcd)
	if err != nil {
		t.Fatal(err)
	}
	return etcd
}

// forgedSnapshot installs a snapshot of commit whose foundation names
// another etcd, as another admin's published configuration could.
func forgedSnapshot(t *testing.T, commit string) {
	t.Helper()
	forged := strings.Replace(testFoundation(t), "gcr.io/etcd-development/etcd:v3.5.*", "evil.example/etcd:v3.5.*", 1)
	snap := map[string][]byte{commit + "/" + FoundationFile: []byte(forged)}
	src := filepath.Join("testdata", "core", "blueprints", "writefreely")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		data, err := os.ReadFile(p)
		snap[commit+"/writefreely/"+filepath.ToSlash(rel)] = data
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallSnapshot(snap); err != nil {
		t.Fatal(err)
	}
}

// An installed snapshot is bytes another admin published, checked only
// against its own list of sums, so a core read from one is never the
// official core, whatever commit it claims.
func TestASnapshotCoreIsNotOfficial(t *testing.T) {
	coretest.Cache(t)
	commit := strings.Repeat("ab", 20)
	forgedSnapshot(t, commit)
	if etcd := pinCore(t, commit); etcd != "evil.example/etcd:v3.5.*" {
		t.Fatalf("read etcd %s, not the snapshot's", etcd)
	}
	if CoreIsOfficial() {
		t.Error("a core read from an installed snapshot is official")
	}
}

// The cache is keyed by commit alone, so a commit fetched from a mirror and
// then pinned with no url is still the mirror's.
func TestAMirrorFetchedCommitIsNotOfficial(t *testing.T) {
	coretest.Cache(t)
	mirror := coreRepo(t, []string{"v1.0.0"}, nil)
	commit, err := git(strings.TrimPrefix(mirror, "file://"), "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(commit)
	}
	if _, err := Fetch(config.Catalog{URL: mirror, Commit: commit}); err != nil {
		t.Fatal(err)
	}
	pinCore(t, commit)
	if CoreIsOfficial() {
		t.Error("a commit fetched from a mirror is official")
	}
}

// The official core pinned to its own commit, held in the cache, is read
// from the cache rather than from a snapshot of the same commit, and keeps
// the exemption.
func TestAnOfficialPinnedCoreIsReadFromTheCache(t *testing.T) {
	coretest.Cache(t)
	if err := LoadCore(""); err != nil {
		t.Fatal(err)
	}
	release, _ := CoreRelease()
	forgedSnapshot(t, release.Commit)
	if etcd := pinCore(t, release.Commit); etcd != "gcr.io/etcd-development/etcd:v3.5.*" {
		t.Errorf("read etcd %s, from the snapshot rather than the cache", etcd)
	}
	if !CoreIsOfficial() {
		t.Error("the official core, pinned and cached, is not official")
	}
}
