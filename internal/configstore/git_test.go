package configstore

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/version"
)

func needTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
}

// testSSHKey makes an unencrypted ed25519 key and returns the private key path
// and the public key line.
func testSSHKey(t *testing.T) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", path).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	pub, err := os.ReadFile(path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	return path, strings.TrimSpace(string(pub))
}

// allowedSignersFile writes an allowed-signers file naming each public key by
// its SHA256 fingerprint.
func allowedSignersFile(t *testing.T, pubs ...string) string {
	t.Helper()
	var b strings.Builder
	for _, pub := range pubs {
		pf := filepath.Join(t.TempDir(), "p.pub")
		if err := os.WriteFile(pf, []byte(pub+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("ssh-keygen", "-lf", pf).Output()
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Fields(string(out))
		if len(fields) < 2 {
			t.Fatalf("ssh-keygen -lf: %q", out)
		}
		parts := strings.Fields(pub)
		b.WriteString(fields[1] + " " + parts[0] + " " + parts[1] + "\n")
	}
	path := filepath.Join(t.TempDir(), "allowed_signers")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testFiles() Files {
	return Files{Paisans: []byte("p\n"), Secrets: []byte("s\n"), SOPS: []byte("o\n")}
}

func TestCommitListBundleFetch(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	key, pub := testSSHKey(t)
	a, err := InitRepo(filepath.Join(t.TempDir(), "a.git"))
	if err != nil {
		t.Fatal(err)
	}
	f := testFiles()
	root, err := a.Commit(f, nil, 1, key, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	child, err := a.Commit(f, []string{root}, 2, key, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	signers := allowedSignersFile(t, pub)
	vs, err := a.Versions(signers)
	if err != nil || len(vs) != 2 {
		t.Fatalf("Versions = %v, %v", vs, err)
	}
	byRev := map[int]Version{}
	for _, v := range vs {
		byRev[v.Revision] = v
		if !v.Signed || v.SignerFP == "" || !v.TreeOK || !v.BuildOK || v.Build != version.Current() {
			t.Fatalf("version %+v", v)
		}
	}
	if byRev[2].SHA != child || byRev[2].Parents[0] != root {
		t.Fatalf("graph %+v", byRev)
	}
	if !byRev[1].Committed.Equal(time.Unix(1_800_000_000, 0)) || !byRev[2].Committed.Equal(time.Unix(1_700_000_000, 0)) {
		t.Fatalf("committed %v, %v", byRev[1].Committed, byRev[2].Committed)
	}
	b, err := a.Bundle()
	if err != nil {
		t.Fatal(err)
	}
	c, err := InitRepo(filepath.Join(t.TempDir(), "c.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.FetchBundle(b); err != nil {
		t.Fatal(err)
	}
	got, err := c.Files(child)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Paisans) != "p\n" || string(got.Secrets) != "s\n" || string(got.SOPS) != "o\n" {
		t.Fatalf("files %+v", got)
	}
	// A second fetch of the same bundle changes nothing and is not an error.
	if err := c.FetchBundle(b); err != nil {
		t.Fatalf("refetch: %v", err)
	}
}

func TestInitRepoIdempotentAndEmptyBundle(t *testing.T) {
	needTools(t, "git")
	dir := filepath.Join(t.TempDir(), "r.git")
	if _, err := InitRepo(dir); err != nil {
		t.Fatal(err)
	}
	r, err := InitRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Bundle()
	if err != nil || b != nil {
		t.Fatalf("empty Bundle = %d bytes, %v", len(b), err)
	}
	if err := r.FetchBundle([]byte("not a bundle")); err == nil {
		t.Fatal("garbage bundle accepted")
	}
}

func TestSignerNotAllowedIsNotSigned(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	key, _ := testSSHKey(t)
	_, otherPub := testSSHKey(t)
	r, err := InitRepo(filepath.Join(t.TempDir(), "a.git"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(testFiles(), nil, 1, key, time.Unix(1_800_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	vs, err := r.Versions(allowedSignersFile(t, otherPub))
	if err != nil || len(vs) != 1 {
		t.Fatalf("Versions = %v, %v", vs, err)
	}
	if vs[0].Signed {
		t.Fatalf("commit by a key outside the allowed signers is Signed: %+v", vs[0])
	}
}

// entry is one line of git mktree input.
type entry struct{ mode, typ, name string }

// rawCommit stores a tree of the given entries (all pointing at one blob) and a
// commit over it with the given message, and names it refs/versions/<sha>. The
// commit is written literally so that the message can hold any bytes.
func rawCommit(t *testing.T, r Repo, entries []entry, msg string) string {
	t.Helper()
	blob, err := r.git(strings.NewReader("x\n"), nil, "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	b := strings.TrimSpace(string(blob))
	var tree strings.Builder
	for _, e := range entries {
		tree.WriteString(e.mode + " " + e.typ + " " + b + "\t" + e.name + "\n")
	}
	treeSHA, err := r.git(strings.NewReader(tree.String()), nil, "mktree")
	if err != nil {
		t.Fatal(err)
	}
	body := "tree " + strings.TrimSpace(string(treeSHA)) + "\nauthor a <a@invalid> 1700000000 +0000\ncommitter a <a@invalid> 1700000000 +0000\n\n" + msg
	out, err := r.git(strings.NewReader(body), nil, "hash-object", "-t", "commit", "-w", "--literally", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(string(out))
	if _, err := r.git(nil, nil, "update-ref", "refs/versions/"+sha, sha); err != nil {
		t.Fatal(err)
	}
	return sha
}

func threeFiles() []entry {
	return []entry{{"100644", "blob", ".sops.yaml"}, {"100644", "blob", "paisans-stack.yaml"}, {"100644", "blob", "secrets.enc.yaml"}}
}

func newRepo(t *testing.T) Repo {
	t.Helper()
	r, err := InitRepo(filepath.Join(t.TempDir(), "a.git"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func versionOf(t *testing.T, r Repo, sha string) Version {
	t.Helper()
	vs, err := r.Versions(allowedSignersFile(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		if v.SHA == sha {
			return v
		}
	}
	t.Fatalf("%s not listed in %+v", sha, vs)
	return Version{}
}

func TestTreeShapes(t *testing.T) {
	needTools(t, "git")
	r := newRepo(t)
	msg := "m\n\nPaisans-Revision: 1\n"
	cases := map[string][]entry{
		"fourth file":   append(threeFiles(), entry{"100644", "blob", "extra.yaml"}),
		"space in name": {{"100644", "blob", ".sops.yaml"}, {"100644", "blob", "paisans-stack.yaml secrets.enc.yaml"}},
		"symlink":       {{"100644", "blob", ".sops.yaml"}, {"120000", "blob", "paisans-stack.yaml"}, {"100644", "blob", "secrets.enc.yaml"}},
		"executable":    {{"100644", "blob", ".sops.yaml"}, {"100755", "blob", "paisans-stack.yaml"}, {"100644", "blob", "secrets.enc.yaml"}},
		"missing file":  threeFiles()[:2],
	}
	for name, es := range cases {
		if versionOf(t, r, rawCommit(t, r, es, msg+"# "+name+"\n")).TreeOK {
			t.Errorf("%s: TreeOK", name)
		}
	}
	if !versionOf(t, r, rawCommit(t, r, threeFiles(), msg)).TreeOK {
		t.Error("three plain files: not TreeOK")
	}
}

func TestHostileMessageSpoilsOnlyItself(t *testing.T) {
	needTools(t, "git")
	r := newRepo(t)
	good := rawCommit(t, r, threeFiles(), "ok\n\nPaisans-Revision: 3\n")
	bad := rawCommit(t, r, threeFiles(), "evil\x00\x1e\x00\x1e\n\nPaisans-Revision: 9\n")
	vs, err := r.Versions(allowedSignersFile(t))
	if err != nil || len(vs) != 2 {
		t.Fatalf("Versions = %+v, %v", vs, err)
	}
	if versionOf(t, r, good).Revision != 3 {
		t.Error("good version lost its revision")
	}
	_ = bad
}

func TestRevisionTrailer(t *testing.T) {
	needTools(t, "git")
	r := newRepo(t)
	for msg, want := range map[string]int{
		"m\n\nPaisans-Revision: 4\n":    4,
		"m\n":                           0,
		"m\n\nPaisans-Revision: four\n": 0,
		"m\n\nPaisans-Revision: -2\n":   0,
		"m\n\nPaisans-Revision: 1\nPaisans-Revision: 2\n": 0,
	} {
		if got := versionOf(t, r, rawCommit(t, r, threeFiles(), msg)).Revision; got != want {
			t.Errorf("%q: Revision %d, want %d", msg, got, want)
		}
	}
}

// A version names the build of paisans that published it in three
// trailers, each there once, and one without them all, or with one
// malformed or repeated, names none.
func TestBuildTrailers(t *testing.T) {
	needTools(t, "git")
	r := newRepo(t)
	const commit = "519299d5cb009da4904cdf53c2025f1ad609f102"
	dirty := version.Build{Version: "0.3.0-dev", Commit: commit, Modified: true}
	for msg, want := range map[string]*version.Build{
		"m\n\nPaisans-Revision: 1\n" + buildTrailers(dirty):                                                                           &dirty,
		"m\n\nPaisans-Revision: 1\n" + buildTrailers(version.Build{Version: "0.2.0", Commit: "v0.2.0"}):                               {Version: "0.2.0", Commit: "v0.2.0"},
		"m\n\nPaisans-Revision: 1\n":                                                                                                  nil,
		"m\n\nPaisans-Revision: 1\nPaisans-Toolkit-Version: 0.3.0\nPaisans-Toolkit-Commit: v0.3.0\n":                                  nil,
		"m\n\nPaisans-Revision: 1\n" + buildTrailers(version.Build{Version: "next", Commit: "v0.3.0"}):                                nil,
		"m\n\nPaisans-Revision: 1\n" + buildTrailers(version.Build{Version: "0.3.0", Commit: "519299d5cb0"}):                          nil,
		"m\n\nPaisans-Revision: 1\nPaisans-Toolkit-Version: 0.3.0\nPaisans-Toolkit-Commit: v0.3.0\nPaisans-Toolkit-Modified: maybe\n": nil,
		"m\n\nPaisans-Revision: 1\n" + buildTrailers(dirty) + "Paisans-Toolkit-Version: 9.9.9\n":                                      nil,
	} {
		v := versionOf(t, r, rawCommit(t, r, threeFiles(), msg))
		switch {
		case want == nil && v.BuildOK:
			t.Errorf("%q: read %+v", msg, v.Build)
		case want != nil && (!v.BuildOK || v.Build != *want):
			t.Errorf("%q: read %+v, %v, want %+v", msg, v.Build, v.BuildOK, *want)
		}
	}
}

// The signature covers the build trailers: a version whose trailers were
// changed after it was signed has no good signature.
func TestTheSignatureCoversTheBuild(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	key, pub := testSSHKey(t)
	r := newRepo(t)
	sha, err := r.Commit(testFiles(), nil, 1, key, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := r.git(nil, nil, "cat-file", "commit", sha)
	if err != nil {
		t.Fatal(err)
	}
	trailer := "Paisans-Toolkit-Version: " + version.Current().Version + "\n"
	if !strings.Contains(string(raw), trailer) {
		t.Fatalf("no %q in:\n%s", trailer, raw)
	}
	forged := strings.Replace(string(raw), trailer, "Paisans-Toolkit-Version: 99.0.0\n", 1)
	out, err := r.git(strings.NewReader(forged), nil, "hash-object", "-t", "commit", "-w", "--literally", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	forgedSHA := strings.TrimSpace(string(out))
	if _, err := r.git(nil, nil, "update-ref", "refs/versions/"+forgedSHA, forgedSHA); err != nil {
		t.Fatal(err)
	}
	vs, err := r.Versions(allowedSignersFile(t, pub))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		switch v.SHA {
		case sha:
			if !v.Signed {
				t.Error("the signed version is not Signed")
			}
		case forgedSHA:
			if v.Signed || v.Build.Version != "99.0.0" {
				t.Errorf("the forged version: %+v", v)
			}
		}
	}
}

func TestUnsignedCommitIsNotSigned(t *testing.T) {
	needTools(t, "git")
	r := newRepo(t)
	if v := versionOf(t, r, rawCommit(t, r, threeFiles(), "m\n\nPaisans-Revision: 1\n")); v.Signed {
		t.Fatalf("unsigned commit is Signed: %+v", v)
	}
}

func TestMisnamedRefIsNotListed(t *testing.T) {
	needTools(t, "git")
	r := newRepo(t)
	a := rawCommit(t, r, threeFiles(), "a\n\nPaisans-Revision: 1\n")
	b := rawCommit(t, r, threeFiles(), "b\n\nPaisans-Revision: 2\n")
	// Move a's ref to b, and add a ref with an arbitrary name for a.
	if _, err := r.git(nil, nil, "update-ref", "refs/versions/"+a, b); err != nil {
		t.Fatal(err)
	}
	if _, err := r.git(nil, nil, "update-ref", "refs/versions/latest", b); err != nil {
		t.Fatal(err)
	}
	vs, err := r.Versions(allowedSignersFile(t))
	if err != nil || len(vs) != 1 || vs[0].SHA != b {
		t.Fatalf("Versions = %+v, %v", vs, err)
	}
}

func TestShaValidation(t *testing.T) {
	needTools(t, "git")
	r := newRepo(t)
	for _, bad := range []string{"", "HEAD", "abc", "--help", strings.Repeat("A", 40), strings.Repeat("a", 41)} {
		if _, err := r.Files(bad); err == nil {
			t.Errorf("Files(%q) accepted", bad)
		}
		if _, err := r.Commit(testFiles(), []string{bad}, 2, "unused", time.Now()); err == nil {
			t.Errorf("Commit parent %q accepted", bad)
		}
	}
}

func TestCallerGitEnvironmentIsIgnored(t *testing.T) {
	needTools(t, "git")
	other := newRepo(t)
	t.Setenv("GIT_DIR", other.Dir)
	t.Setenv("GIT_WORK_TREE", other.Dir)
	r, err := InitRepo(filepath.Join(t.TempDir(), "mine.git"))
	if err != nil {
		t.Fatal(err)
	}
	sha := rawCommit(t, r, threeFiles(), "m\n\nPaisans-Revision: 1\n")
	if out, err := other.git(nil, nil, "for-each-ref", "refs/versions/"); err != nil || len(out) != 0 {
		t.Fatalf("other repo touched: %q, %v", out, err)
	}
	if versionOf(t, r, sha).Revision != 1 {
		t.Fatal("version not in r.Dir")
	}
}
