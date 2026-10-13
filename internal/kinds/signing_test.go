package kinds

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
)

type signedRepo struct {
	t   *testing.T
	dir string
}

func sshKey(t *testing.T, name string) (path, pub, fp string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), name)
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", path).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %s", out)
	}
	raw, err := os.ReadFile(path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	pub = strings.TrimSpace(string(raw))
	key, _, err := config.ParseKeyLine(pub)
	if err != nil {
		t.Fatal(err)
	}
	return path, pub, key.Fingerprint
}

func (r signedRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir, "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false", "-c", "gpg.format=ssh"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

// release writes the published keys, commits, and tags, signed with key or
// unsigned when key is empty.
func (r signedRepo) release(tag, key string, published ...string) string {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Join(r.dir, "keys"), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.dir, "keys", "allowed_signers"), []byte(strings.Join(published, "\n")+"\n"), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", tag)
	if key == "" {
		r.git("-c", "tag.gpgsign=false", "tag", "-a", tag, "-m", tag)
	} else {
		r.git("-c", "user.signingkey="+key, "tag", "-s", tag, "-m", tag)
	}
	return r.git("rev-parse", "HEAD")
}

func newSignedRepo(t *testing.T) signedRepo {
	t.Helper()
	dir, _ := testCatalog(t, nil)
	return signedRepo{t, dir}
}

// A signed pin fetches only when its tag is signed by one of its signers.
func TestAPinIsCheckedAgainstItsSigners(t *testing.T) {
	t.Setenv("PAISANS_CATALOG_CACHE", t.TempDir())
	k1, pub1, fp1 := sshKey(t, "one")
	_, pub2, _ := sshKey(t, "two")
	r := newSignedRepo(t)
	c1 := r.release("v1.1.0", k1, pub1)
	c2 := r.release("v1.2.0", "", pub1)

	if _, err := Fetch(config.Catalog{URL: r.dir, Tag: "v1.1.0", Commit: c1, Signers: []string{pub1}}); err != nil {
		t.Errorf("signed by its signer: %v", err)
	}
	if _, err := Fetch(config.Catalog{URL: r.dir, Tag: "v1.1.0", Commit: c1, Signers: []string{pub2}}); err == nil || !strings.Contains(err.Error(), fp1) {
		t.Errorf("signed by another key: %v", err)
	}
	if _, err := Fetch(config.Catalog{URL: r.dir, Tag: "v1.2.0", Commit: c2, Signers: []string{pub1}}); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Errorf("an unsigned tag: %v", err)
	}
	if _, err := Fetch(config.Catalog{URL: r.dir, Tag: "v1.2.0", Commit: c2, Unsigned: true}); err != nil {
		t.Errorf("an unsigned pin: %v", err)
	}
}

// A catalog moves to a new key only through a release the old key signed
// that lists the new one; any other new key stops the walk at the release
// it signed, until the operator accepts its fingerprint.
func TestAKeyRotatesOnlyThroughTheOldKey(t *testing.T) {
	t.Setenv("PAISANS_CATALOG_CACHE", t.TempDir())
	k1, pub1, _ := sshKey(t, "one")
	k2, pub2, _ := sshKey(t, "two")
	k3, pub3, fp3 := sshKey(t, "three")
	r := newSignedRepo(t)
	r.release("v1.1.0", k1, pub1)
	r.release("v1.2.0", k1, pub1, pub2) // the old key introduces the new one
	r.release("v1.3.0", k2, pub2)
	c4 := r.release("v1.4.0", k3, pub3) // a key nothing trusted lists

	pin := config.Catalog{URL: r.dir, Tag: "v1.1.0", Signers: []string{pub1}}
	dir, err := FetchUnchecked(config.Catalog{URL: r.dir, Tag: "v1.4.0", Commit: c4})
	if err != nil {
		t.Fatal(err)
	}
	rot, err := Rotate(dir, pin, "v1.3.0", "")
	if err != nil || rot.Stopped != "" {
		t.Fatalf("v1.3.0: %+v, %v", rot, err)
	}
	if strings.Join(rot.Signers, "\n") != strings.Join(sortedLines(pub1, pub2), "\n") {
		t.Errorf("signers %v", rot.Signers)
	}
	rot, err = Rotate(dir, pin, "v1.4.0", "")
	if err != nil || rot.Stopped != "v1.4.0" || rot.Fingerprint != fp3 {
		t.Fatalf("v1.4.0 unaccepted: %+v, %v", rot, err)
	}
	rot, err = Rotate(dir, pin, "v1.4.0", fp3)
	if err != nil || rot.Stopped != "" {
		t.Fatalf("v1.4.0 accepted: %+v, %v", rot, err)
	}
	// Accepting a key the release does not publish leaves nothing to record.
	k4, _, fp4 := sshKey(t, "four")
	c5 := r.release("v1.5.0", k4, pub3)
	dir, err = FetchUnchecked(config.Catalog{URL: r.dir, Tag: "v1.5.0", Commit: c5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Rotate(dir, config.Catalog{URL: r.dir, Tag: "v1.4.0", Signers: []string{pub3}}, "v1.5.0", fp4); err == nil {
		t.Error("recorded a key the catalog does not publish")
	}
}

func sortedLines(lines ...string) []string {
	out := append([]string(nil), lines...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// The core catalog pinned to a commit of its own is checked against the
// core release keys in the binary, and with none there it is refused.
func TestACorePinNeedsTheCoreKeys(t *testing.T) {
	t.Setenv("PAISANS_CATALOG_CACHE", t.TempDir())
	url, commit := testCatalog(t, nil)
	cfg := &config.Config{
		Catalogs: map[string]config.Catalog{"core": {URL: url, Tag: "v1.0.0", Commit: commit}},
		Apps:     map[string]config.App{"talk": {Kind: "mbin", Hostname: "talk.example.org"}},
	}
	problems := strings.Join(resolve(cfg), "\n")
	if len(CoreSigners()) == 0 && !strings.Contains(problems, "carries no core release key") {
		t.Errorf("got %q", problems)
	}
}
