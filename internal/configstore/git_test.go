package configstore

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		if !v.Signed || v.SignerFP == "" || !v.TreeOK {
			t.Fatalf("version %+v", v)
		}
	}
	if byRev[2].SHA != child || byRev[2].Parents[0] != root {
		t.Fatalf("graph %+v", byRev)
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

func TestTreeWithFourthFileIsNotOK(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	key, pub := testSSHKey(t)
	r, err := InitRepo(filepath.Join(t.TempDir(), "a.git"))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := r.git(strings.NewReader("x\n"), nil, "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	b := strings.TrimSpace(string(blob))
	var tree strings.Builder
	for _, n := range []string{".sops.yaml", "extra.yaml", "paisans.yaml", "secrets.enc.yaml"} {
		tree.WriteString("100644 blob " + b + "\t" + n + "\n")
	}
	treeSHA, err := r.git(strings.NewReader(tree.String()), nil, "mktree")
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.git(nil, nil, "-c", "gpg.format=ssh", "-c", "user.signingKey="+key,
		"-c", "user.name=paisans", "-c", "user.email=paisans@invalid",
		"commit-tree", strings.TrimSpace(string(treeSHA)), "-S", "-m", "bad\n\nPaisans-Revision: 1\n")
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(string(out))
	if _, err := r.git(nil, nil, "update-ref", "refs/versions/"+sha, sha); err != nil {
		t.Fatal(err)
	}
	vs, err := r.Versions(allowedSignersFile(t, pub))
	if err != nil || len(vs) != 1 {
		t.Fatalf("Versions = %v, %v", vs, err)
	}
	if vs[0].TreeOK {
		t.Fatalf("TreeOK with four files: %+v", vs[0])
	}
}
