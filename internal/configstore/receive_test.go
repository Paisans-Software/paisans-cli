package configstore

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/hostprep"
)

// childOf writes a commit on parent with the three files and names it
// correctly, as an honest store would.
func childOf(t *testing.T, r Repo, parent, msg string) string {
	t.Helper()
	tree, err := r.git(nil, nil, "rev-parse", parent+"^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.git(nil, []string{"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@invalid", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@invalid"},
		"commit-tree", strings.TrimSpace(string(tree)), "-p", parent, "-m", msg)
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(string(out))
	if _, err := r.git(nil, nil, "update-ref", "refs/versions/"+sha, sha); err != nil {
		t.Fatal(err)
	}
	return sha
}

// refs is every ref under prefix as name -> commit.
func refs(t *testing.T, r Repo, prefix string) map[string]string {
	t.Helper()
	out, err := r.git(nil, nil, "for-each-ref", "--format=%(refname) %(objectname)", prefix)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			m[f[0]] = f[1]
		}
	}
	return m
}

// hostileStore is a store whose owner moved refs/versions/x to x's child y
// and planted a ref whose name holds a quote. raw is a bundle of all its
// refs, as git would pack them without this package's filter.
func hostileStore(t *testing.T) (r Repo, x, y string, raw []byte) {
	t.Helper()
	r = newRepo(t)
	x = rawCommit(t, r, threeFiles(), "x\n\nPaisans-Revision: 1\n")
	y = childOf(t, r, x, "y\n\nPaisans-Revision: 2\n")
	for _, ref := range []string{"refs/versions/" + x, "refs/versions/a'b", `refs/versions/c"d`} {
		if _, err := r.git(nil, nil, "update-ref", ref, y); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "raw.bundle")
	if _, err := r.git(nil, nil, "bundle", "create", "--quiet", path, "--glob=refs/versions"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return r, x, y, raw
}

// honestStore holds x and y, each under its own name.
func honestStore(t *testing.T, from Repo, x, y string) Repo {
	t.Helper()
	h := newRepo(t)
	// y brings x, its parent, and both trees.
	if _, err := h.git(nil, nil, "fetch", "--quiet", "--no-tags", from.Dir, y+":refs/tmp/y"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.git(nil, nil, "update-ref", "-d", "refs/tmp/y"); err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{x, y} {
		if _, err := h.git(nil, nil, "update-ref", "refs/versions/"+sha, sha); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func wantVersions(t *testing.T, r Repo, shas ...string) {
	t.Helper()
	want := map[string]string{}
	for _, s := range shas {
		want["refs/versions/"+s] = s
	}
	got := refs(t, r, "refs/")
	if len(got) != len(want) {
		t.Fatalf("refs %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("refs %v, want exactly %v", got, want)
		}
	}
}

// A bundle that moves refs/versions/x to x's child leaves x where it is, adds
// the child under its own name only, ignores names with quotes, and leaves
// nothing in the quarantine. An honest bundle afterwards still lands.
func TestFetchBundleNeverMovesAVersion(t *testing.T) {
	needTools(t, "git")
	hostile, x, y, raw := hostileStore(t)
	c := newRepo(t)
	if _, err := c.git(nil, nil, "fetch", "--quiet", "--no-tags", hostile.Dir, x+":refs/versions/"+x); err != nil {
		t.Fatal(err)
	}
	if err := c.FetchBundle(raw); err != nil {
		t.Fatalf("hostile bundle: %v", err)
	}
	wantVersions(t, c, x, y)

	honest, err := honestStore(t, hostile, x, y).Bundle()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.FetchBundle(honest); err != nil {
		t.Fatalf("honest bundle after a hostile one: %v", err)
	}
	wantVersions(t, c, x, y)
}

// Bundle packs only refs named for the commit they point to.
func TestBundleCarriesOnlyCorrectlyNamedVersions(t *testing.T) {
	needTools(t, "git")
	hostile, _, y, _ := hostileStore(t)
	b, err := hostile.Bundle()
	if err != nil {
		t.Fatal(err)
	}
	heads, err := hostile.bundleHeads(b)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(heads, []string{y + " refs/versions/" + y}) {
		t.Fatalf("bundle heads %q", heads)
	}
}

// bundleHeads is git's list of a bundle's refs, one "sha name" per line.
func (r Repo) bundleHeads(bundle []byte) ([]string, error) {
	path := filepath.Join(os.TempDir(), "paisans-test-"+strings.ReplaceAll(filepath.Base(r.Dir), ".", "-")+".bundle")
	if err := os.WriteFile(path, bundle, 0o600); err != nil {
		return nil, err
	}
	defer os.Remove(path)
	out, err := r.git(nil, nil, "bundle", "list-heads", path)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n"), nil
}

// localShell runs a site's commands with a local sh, with the host's
// configuration directory moved into a temporary one: the shell Push and
// Pull send executes as written, against real repositories.
type localShell struct {
	t   *testing.T
	dir string
}

const hostConfigDir = "/var/lib/paisans/config"

func (s localShell) local(p string) string { return strings.ReplaceAll(p, hostConfigDir, s.dir) }

func (s localShell) Run(c string) (string, error) {
	cmd := exec.Command("sh", "-c", s.local(c))
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	return string(out), err
}
func (s localShell) RunInput(c, _ string) (string, error) { return s.Run(c) }
func (s localShell) ReadFile(p string) (string, bool, error) {
	data, err := os.ReadFile(s.local(p))
	if os.IsNotExist(err) {
		return "", false, nil
	}
	return string(data), err == nil, err
}
func (s localShell) WriteFile(p, content string, mode uint32) error {
	return os.WriteFile(s.local(p), []byte(content), os.FileMode(mode))
}
func (s localShell) Describe() string { return "local" }

// siteStore is a site's store under a localShell, at the path the host uses.
func siteStore(t *testing.T) (localShell, Repo) {
	t.Helper()
	s := localShell{t: t, dir: t.TempDir()}
	r, err := InitRepo(s.local(hostprep.ConfigRepo(dep)))
	if err != nil {
		t.Fatal(err)
	}
	return s, r
}

// Push and Pull run their shell for real: a hostile push cannot move a
// version or plant a quoted name, an honest push afterwards lands, and a
// store holding planted refs still answers a pull with only its versions.
func TestPushAndPullShellKeepEveryVersionWhereItIs(t *testing.T) {
	needTools(t, "git", "sh", "base64", "mktemp")
	hostile, x, y, raw := hostileStore(t)
	s, site := siteStore(t)
	honest, err := honestStore(t, hostile, x, y).Bundle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := site.git(nil, nil, "fetch", "--quiet", "--no-tags", hostile.Dir, x+":refs/versions/"+x); err != nil {
		t.Fatal(err)
	}
	if err := Push(s, dep, raw); err != nil {
		t.Fatalf("hostile push: %v", err)
	}
	wantVersions(t, site, x, y)
	if _, err := os.Stat(s.local(incomingPath(dep))); !os.IsNotExist(err) {
		t.Fatalf("the pushed bundle was left behind: %v", err)
	}
	if err := Push(s, dep, honest); err != nil {
		t.Fatalf("honest push after a hostile one: %v", err)
	}
	wantVersions(t, site, x, y)

	// Plant what an owner of the site could, then pull.
	for _, ref := range []string{"refs/versions/a'b", `refs/versions/c"d`, "refs/versions/" + strings.Repeat("0", 40)} {
		if _, err := site.git(nil, nil, "update-ref", ref, y); err != nil {
			t.Fatal(err)
		}
	}
	b, _, err := Pull(s, dep, false)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	heads, err := site.bundleHeads(b)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(heads)
	want := []string{x + " refs/versions/" + x, y + " refs/versions/" + y}
	slices.Sort(want)
	if !slices.Equal(heads, want) {
		t.Fatalf("pulled heads %q, want %q", heads, want)
	}
}

// A store with only planted refs answers as empty.
func TestPullOfOnlyPlantedRefsIsEmpty(t *testing.T) {
	needTools(t, "git", "sh", "base64", "mktemp")
	s, site := siteStore(t)
	x := rawCommit(t, site, threeFiles(), "x\n\nPaisans-Revision: 1\n")
	if _, err := site.git(nil, nil, "update-ref", "-d", "refs/versions/"+x); err != nil {
		t.Fatal(err)
	}
	if _, err := site.git(nil, nil, "update-ref", "refs/versions/a'b", x); err != nil {
		t.Fatal(err)
	}
	b, _, err := Pull(s, dep, false)
	if err != nil || b != nil {
		t.Fatalf("got %d bytes, %v", len(b), err)
	}
}
