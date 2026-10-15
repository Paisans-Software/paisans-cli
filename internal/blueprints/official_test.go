package blueprints

import (
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/blueprints/coretest"
	"github.com/paisans-software/paisans-cli/internal/config"
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

// pinCore resolves a configuration that pins the core catalog to commit
// and tag, with no url, and returns the foundation's etcd it then reads.
func pinCore(t *testing.T, commit, tag string) string {
	t.Helper()
	t.Cleanup(forgetCore)
	forgetCore()
	cfg := &config.Config{Catalogs: map[string]config.Catalog{config.CoreCatalog: {Commit: commit, Tag: tag}}}
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
	if etcd := pinCore(t, commit, ""); etcd != "evil.example/etcd:v3.5.*" {
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
	pinCore(t, commit, "")
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
	if etcd := pinCore(t, release.Commit, release.Tag); etcd != "gcr.io/etcd-development/etcd:v3.5.*" {
		t.Errorf("read etcd %s, from the snapshot rather than the cache", etcd)
	}
	if !CoreIsOfficial() {
		t.Error("the official core, pinned and cached, is not official")
	}
}

// officialRepo is a core catalog repository standing in for the official
// one, with the release v1.0.0 and a commit on a branch no release names,
// as a pull request's head is. It returns its URL, the release's commit and
// the branch's.
func officialRepo(t *testing.T) (url, release, head string) {
	t.Helper()
	url = coreRepo(t, []string{"v1.0.0"}, nil)
	dir := strings.TrimPrefix(url, "file://")
	run := func(args ...string) string {
		out, err := git(dir, append([]string{"-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return out
	}
	release = run("rev-parse", "HEAD")
	run("checkout", "-q", "-b", "pull")
	p := filepath.Join(dir, FoundationFile)
	raw, _ := os.ReadFile(p)
	if err := os.WriteFile(p, []byte(strings.Replace(string(raw), "gcr.io/etcd-development/etcd:v3.5.*", "evil.example/etcd:v3.5.*", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	run("commit", "-q", "-am", "a pull request")
	head = run("rev-parse", "HEAD")
	run("checkout", "-q", "main")
	t.Setenv("PAISANS_CORE_CATALOG", url)
	t.Setenv("PAISANS_TEST_OFFICIAL_CORE", url)
	return url, release, head
}

// The official repository serves every commit it holds, a pull request's
// head included, which nobody on the team has merged or released. Only a
// commit a release tag of the official repository names is official.
func TestAPullRequestHeadIsNotOfficial(t *testing.T) {
	coretest.Cache(t)
	_, _, head := officialRepo(t)
	if etcd := pinCore(t, head, ""); etcd != "evil.example/etcd:v3.5.*" {
		t.Fatalf("read etcd %s, not the pull request's", etcd)
	}
	if CoreIsOfficial() {
		t.Error("a pull request's head, fetched from the official repository, is official")
	}
}

// A release of the official repository is official, and stays so offline:
// the check against the official repository is recorded in the checkout.
func TestAnOfficialReleaseIsOfficialOffline(t *testing.T) {
	coretest.Cache(t)
	url, release, _ := officialRepo(t)
	pinCore(t, release, "v1.0.0")
	if !CoreIsOfficial() {
		t.Fatal("the official release is not official")
	}
	cache, _ := CacheDir()
	if _, err := os.Stat(filepath.Join(cache, release, ".git", stateDir, "official-tags", hex.EncodeToString([]byte("v1.0.0")))); err != nil {
		t.Errorf("the check against the official repository is not recorded: %v", err)
	}
	if err := os.RemoveAll(strings.TrimPrefix(url, "file://")); err != nil {
		t.Fatal(err)
	}
	pinCore(t, release, "v1.0.0")
	if !CoreIsOfficial() {
		t.Error("the official release is not official offline")
	}
}

// A commit a third party catalog entry fetched from the official URL, a
// pull request's head, sits in the cache with the official origin. A core
// read from a mirror that tags that commit as a release is still not
// official: the official repository names no release at it.
func TestAMirrorTagOnAnOfficialOriginCommitIsNotOfficial(t *testing.T) {
	coretest.Cache(t)
	url, _, head := officialRepo(t)
	if _, err := Fetch(config.Catalog{URL: url, Commit: head}); err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(t.TempDir(), "paisans-blueprints")
	if out, err := git("", "clone", "-q", url, mirror); err != nil {
		t.Fatal(out)
	}
	if out, err := git(mirror, "-c", "tag.gpgsign=false", "tag", "v1.1.0", head); err != nil {
		t.Fatal(out)
	}
	t.Setenv("PAISANS_CORE_CATALOG", "file://"+mirror)
	t.Cleanup(forgetCore)
	forgetCore()
	if err := LoadCore(""); err != nil {
		t.Fatal(err)
	}
	if release, _ := CoreRelease(); release.Commit != head {
		t.Fatalf("read %+v, not the mirror's tag", release)
	}
	if CoreIsOfficial() {
		t.Error("a mirror's tag on a commit the official repository does not release is official")
	}
}

// A snapshot names its commit as a directory, so a key that is not a full
// commit id is refused.
func TestASnapshotKeyIsACommit(t *testing.T) {
	coretest.Cache(t)
	for _, key := range []string{"main", strings.Repeat("A", 40), strings.Repeat("a", 39), "..", "."} {
		if err := InstallSnapshot(map[string][]byte{key + "/" + FoundationFile: []byte("x")}); err == nil {
			t.Errorf("a snapshot keyed %q was installed", key)
		}
	}
}

// The environment the toolkit runs in cannot point its git at another
// repository or another configuration, which would let it read an origin
// or a commit that is not the checkout's.
func TestCatalogGitIgnoresTheEnvironmentsRepository(t *testing.T) {
	url, release, _ := officialRepo(t)
	dir := strings.TrimPrefix(url, "file://")
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "elsewhere"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "remote.origin.url")
	t.Setenv("GIT_CONFIG_VALUE_0", CoreURL)
	if head, err := git(dir, "rev-parse", "HEAD"); err != nil || head != release {
		t.Errorf("HEAD %q, %v", head, err)
	}
	if origin, _ := git(dir, "config", "--get", "remote.origin.url"); origin == CoreURL {
		t.Error("the environment set the origin")
	}
}

// TagCommit takes only the tag itself: ls-remote matches a pattern against
// the end of a ref, so refs/tags/x/refs/tags/v1.0.0 would otherwise stand in
// for v1.0.0.
func TestTagCommitTakesOnlyTheTagItself(t *testing.T) {
	url, _, head := officialRepo(t)
	dir := strings.TrimPrefix(url, "file://")
	for _, forged := range []string{"refs/tags/v9.9.9", "x/refs/tags/v1.0.0"} {
		if out, err := git(dir, "-c", "tag.gpgsign=false", "tag", forged, head); err != nil {
			t.Fatal(out)
		}
	}
	if got, err := TagCommit(url, "v9.9.9"); err == nil {
		t.Errorf("v9.9.9, which no tag is, names %s", got)
	}
	if got, err := TagCommit(url, "v1.0.0"); err != nil || got == head {
		t.Errorf("v1.0.0 names %s, %v", got, err)
	}
}

// The official core's releases are tagged vMAJOR.MINOR.PATCH. A tag without
// the v, even one the official repository puts on a commit, is not a release
// and is not official.
func TestAnUnprefixedTagIsNotAnOfficialRelease(t *testing.T) {
	coretest.Cache(t)
	url, _, head := officialRepo(t)
	if out, err := git(strings.TrimPrefix(url, "file://"), "-c", "tag.gpgsign=false", "tag", "9.9.9", head); err != nil {
		t.Fatal(out)
	}
	pinCore(t, head, "9.9.9")
	if CoreIsOfficial() {
		t.Error("a pull request's head tagged 9.9.9 is official")
	}
}
