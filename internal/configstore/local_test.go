package configstore

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// storeSite is a site whose store is a local bare repository, reached by
// fakePull and fakePush in place of ssh.
type storeSite struct {
	name string
	repo Repo
	down bool
}

func (s *storeSite) Run(string) (string, error)              { return "", nil }
func (s *storeSite) RunInput(string, string) (string, error) { return "", nil }
func (s *storeSite) ReadFile(string) (string, bool, error)   { return "", false, nil }
func (s *storeSite) WriteFile(string, string, uint32) error  { return nil }
func (s *storeSite) Describe() string                        { return s.name }

func fakePull(t apply.Transport, _ deployment.Deployment, _ bool) ([]byte, *SyncReport, error) {
	s := t.(*storeSite)
	if s.down {
		return nil, nil, errors.New("ssh: connect to host " + s.name + ": connection refused")
	}
	b, err := s.repo.Bundle()
	return b, nil, err
}

func fakePush(t apply.Transport, _ deployment.Deployment, b []byte) error {
	s := t.(*storeSite)
	if s.down {
		return errors.New("ssh: connect to host " + s.name + ": connection refused")
	}
	return s.repo.FetchBundle(b)
}

// storeSites makes one empty site store per name.
func storeSites(t *testing.T, names ...string) map[string]*storeSite {
	t.Helper()
	out := map[string]*storeSite{}
	for _, n := range names {
		r, err := InitRepo(filepath.Join(t.TempDir(), n+".git"))
		if err != nil {
			t.Fatal(err)
		}
		out[n] = &storeSite{name: n, repo: r}
	}
	return out
}

// reach is the transports of the named sites, as a command passes them.
func reach(sites map[string]*storeSite, names ...string) map[string]apply.Transport {
	out := map[string]apply.Transport{}
	for _, n := range names {
		out[n] = sites[n]
	}
	return out
}

const testID = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"

// testConfig is a paisans-stack.yaml that loads, listing each operator's key. alice
// is always one, since the site's ssh section names her. note changes the
// community's name, so two edits differ.
func testConfig(note string, operators map[string]string) []byte {
	var a strings.Builder
	for _, id := range slices.Sorted(maps.Keys(operators)) {
		a.WriteString("  " + id + ":\n    name: " + strings.ToUpper(id[:1]) + id[1:] + " Example\n    email: " + id + "@example.org\n    keys: |\n      " + operators[id] + "\n")
	}
	return []byte(`version: 1
id: ` + testID + `
community:
  name: Example ` + note + `
  domain: example.org
mesh:
  subnet: 10.44.0.0/24
acme:
  provider: desec
operators:
` + a.String() + `sites:
  home-a:
    roles: [data, apps]
    address: 10.44.0.1
    ssh:
      host: home-a.local
      user: ubuntu
      keys: [alice]
cluster:
  sites: [home-a]
  port: 5000
  postgres_version: "18"
etcd:
  members: [home-a]
storage:
  garage:
    sites: [home-a]
    replication: 1
`)
}

type operator struct {
	key, pub string
}

func newOperator(t *testing.T) operator {
	k, p := testSSHKey(t)
	return operator{k, p}
}

// workstation is a directory holding the three files.
func workstation(t *testing.T, f Files) Local {
	t.Helper()
	l := Local{Dir: t.TempDir()}
	if err := l.WriteFiles(f); err != nil {
		t.Fatal(err)
	}
	return l
}

func edit(f Files, note string, operators map[string]string) Files {
	f.Paisans = testConfig(note, operators)
	return f
}

var testNow = time.Unix(1_800_000_000, 0)

func (o operator) publish(t *testing.T, l Local, sites map[string]apply.Transport, merge bool) (string, []string, error) {
	t.Helper()
	return publish(l, sites, publishOpts{keyLine: o.pub, signWith: o.key, merge: merge, now: testNow}, fakePull, fakePush)
}

func held(t *testing.T, s *storeSite, sha string) bool {
	t.Helper()
	shas, err := s.repo.versionRefs()
	if err != nil {
		t.Fatal(err)
	}
	return slices.Contains(shas, sha)
}

func TestPublishFirstCreatesTheRootAndState(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	sites := storeSites(t, "home-a", "home-b", "vm")
	l := workstation(t, edit(testFiles(), "1", map[string]string{"alice": alice.pub}))
	sha, pushed, err := alice.publish(t, l, reach(sites, "home-a", "home-b", "vm"), false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pushed, []string{"home-a", "home-b", "vm"}) {
		t.Fatalf("pushed %v", pushed)
	}
	for _, s := range sites {
		if !held(t, s, sha) {
			t.Errorf("%s lacks %s", s.name, sha)
		}
	}
	root, _ := l.Root()
	head, _ := l.Head()
	key, _ := l.SigningKey()
	if root != sha || head != sha || key != alice.pub {
		t.Fatalf("root %q head %q key %q, want %s and alice's key", root, head, key, sha)
	}
	ignore, err := os.ReadFile(filepath.Join(l.Dir, ".paisans", ".gitignore"))
	if err != nil || string(ignore) != "store.git/\nincoming/\nsigning-key\nconfig-head\nconfig-seen\narrivals\n" {
		t.Fatalf(".gitignore %q, %v", ignore, err)
	}
	g, err := gather(l, reach(sites, "home-a"), testID, mustAnchor(t, l), GatherOptions{Sync: true}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Verdict.Heads) != 1 || g.Verdict.Heads[0] != sha || g.Versions[sha].Revision != 1 {
		t.Fatalf("verdict %+v", g.Verdict)
	}
}

func mustAnchor(t *testing.T, l Local) Anchor {
	t.Helper()
	a, err := l.Anchor()
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// A publish with nothing changed since the head pushes the head again rather
// than making another version: it is how a publish some sites missed is
// resumed.
func TestPublishResumesWithoutANewVersion(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	sites := storeSites(t, "home-a", "home-b")
	l := workstation(t, edit(testFiles(), "1", map[string]string{"alice": alice.pub}))
	sites["home-b"].down = true
	sha, pushed, err := alice.publish(t, l, reach(sites, "home-a", "home-b"), false)
	var pe *PushError
	if !errors.As(err, &pe) || !slices.Equal(pushed, []string{"home-a"}) || pe.Failed["home-b"] == nil {
		t.Fatalf("first: %v %v", pushed, err)
	}
	sites["home-b"].down = false
	again, pushed, err := alice.publish(t, l, reach(sites, "home-a", "home-b"), false)
	if err != nil || again != sha || len(pushed) != 2 || !held(t, sites["home-b"], sha) {
		t.Fatalf("resume: %s %v %v", again, pushed, err)
	}
}

func TestPublishFromAStaleHeadIsRefused(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice, bob := newOperator(t), newOperator(t)
	operators := map[string]string{"alice": alice.pub, "bob": bob.pub}
	sites := storeSites(t, "home-a", "home-b")
	all := reach(sites, "home-a", "home-b")
	la := workstation(t, edit(testFiles(), "1", operators))
	v1, _, err := alice.publish(t, la, all, false)
	if err != nil {
		t.Fatal(err)
	}
	// Bob recovers v1 with the root pinned, then alice publishes v2.
	lb := Local{Dir: t.TempDir()}
	if err := lb.SetRoot(v1); err != nil {
		t.Fatal(err)
	}
	g, err := gather(lb, all, testID, mustAnchor(t, lb), GatherOptions{Sync: true}, fakePull)
	if err != nil || g.Verdict.Heads[0] != v1 {
		t.Fatalf("bob's gather %+v %v", g.Verdict, err)
	}
	if err := lb.Checkout(v1); err != nil {
		t.Fatal(err)
	}
	if err := la.WriteFiles(edit(testFiles(), "2", operators)); err != nil {
		t.Fatal(err)
	}
	v2, _, err := alice.publish(t, la, all, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := lb.WriteFiles(edit(testFiles(), "bob", operators)); err != nil {
		t.Fatal(err)
	}
	_, _, err = bob.publish(t, lb, all, false)
	if err == nil || !strings.Contains(err.Error(), "stale") || !strings.Contains(err.Error(), v2[:12]) || !strings.Contains(err.Error(), "paisans deployment pull") {
		t.Fatalf("stale publish: %v", err)
	}
	if head, _ := lb.Head(); head != v1 {
		t.Fatalf("a refused publish moved the head to %s", head)
	}
}

func TestTwoOperatorsFromOneHeadForkAndMergeResolvesIt(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice, bob := newOperator(t), newOperator(t)
	operators := map[string]string{"alice": alice.pub, "bob": bob.pub}
	sites := storeSites(t, "home-a", "home-b")
	all := reach(sites, "home-a", "home-b")
	la := workstation(t, edit(testFiles(), "1", operators))
	v1, _, err := alice.publish(t, la, all, false)
	if err != nil {
		t.Fatal(err)
	}
	lb := Local{Dir: t.TempDir()}
	if err := lb.SetRoot(v1); err != nil {
		t.Fatal(err)
	}
	if _, err := gather(lb, all, testID, mustAnchor(t, lb), GatherOptions{Sync: true}, fakePull); err != nil {
		t.Fatal(err)
	}
	if err := lb.Checkout(v1); err != nil {
		t.Fatal(err)
	}
	// Each reaches only one site, so neither sees the other's version.
	la.WriteFiles(edit(testFiles(), "alice", operators))
	lb.WriteFiles(edit(testFiles(), "bob", operators))
	a2, _, err := alice.publish(t, la, reach(sites, "home-a"), false)
	if err != nil {
		t.Fatal(err)
	}
	b2, _, err := bob.publish(t, lb, reach(sites, "home-b"), false)
	if err != nil {
		t.Fatal(err)
	}
	g, err := gather(la, all, testID, mustAnchor(t, la), GatherOptions{Sync: true}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	if !g.Verdict.Fork || len(g.Verdict.Heads) != 2 {
		t.Fatalf("verdict %+v", g.Verdict)
	}
	la.WriteFiles(edit(testFiles(), "merged", operators))
	if _, _, err := alice.publish(t, la, all, false); err == nil || !strings.Contains(err.Error(), "--merge") {
		t.Fatalf("publish on a fork: %v", err)
	}
	m, _, err := alice.publish(t, la, all, true)
	if err != nil {
		t.Fatal(err)
	}
	g, err = gather(la, all, testID, mustAnchor(t, la), GatherOptions{Sync: true}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	v := g.Versions[m]
	if g.Verdict.Fork || g.Verdict.Heads[0] != m || v.Revision != 3 || !slices.Contains(v.Parents, a2) || !slices.Contains(v.Parents, b2) {
		t.Fatalf("after merge %+v, version %+v", g.Verdict, v)
	}
}

// Only a site reached directly is Direct; a version arriving through another
// site's sync makes that site a holder and nothing more.
func TestGatherCountsOnlyDirectReaches(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	sites := storeSites(t, "home-a", "home-b", "vm", "watch")
	l := workstation(t, edit(testFiles(), "1", map[string]string{"alice": alice.pub}))
	// The version reaches home-b only, as if by home-a's sync it would
	// arrive anywhere; home-a holds nothing.
	sha, _, err := alice.publish(t, l, reach(sites, "home-b"), false)
	if err != nil {
		t.Fatal(err)
	}
	sites["vm"].down, sites["watch"].down = true, true
	g, err := gather(l, reach(sites, "home-a", "home-b", "vm", "watch"), testID, mustAnchor(t, l), GatherOptions{Sync: true}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	direct := 0
	for _, r := range g.Reached {
		if r.Direct {
			direct++
		} else if r.Err == nil {
			t.Errorf("%s neither reached nor failed", r.Site)
		}
	}
	if direct != 2 || !slices.Equal(g.Holders[sha], []string{"home-b"}) {
		t.Fatalf("direct %d, holders %v", direct, g.Holders)
	}
}

// With versions held and nothing to anchor them, gathering refuses.
func TestGatherWithoutAnAnchorIsRefused(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	sites := storeSites(t, "home-a")
	l := workstation(t, edit(testFiles(), "1", map[string]string{"alice": alice.pub}))
	if _, _, err := alice.publish(t, l, reach(sites, "home-a"), false); err != nil {
		t.Fatal(err)
	}
	empty := Local{Dir: t.TempDir()}
	if _, err := gather(empty, reach(sites, "home-a"), testID, mustAnchor(t, empty), GatherOptions{Sync: true}, fakePull); !errors.Is(err, ErrNoAnchor) {
		t.Fatalf("got %v", err)
	}
}

// A recovery onto an empty directory, with the root pinned, writes the head's
// three files and the state that names it.
func TestRecoverWritesTheFilesAndState(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	operators := map[string]string{"alice": alice.pub}
	sites := storeSites(t, "home-a", "home-b")
	all := reach(sites, "home-a", "home-b")
	la := workstation(t, edit(testFiles(), "1", operators))
	v1, _, err := alice.publish(t, la, all, false)
	if err != nil {
		t.Fatal(err)
	}
	want := edit(testFiles(), "2", operators)
	la.WriteFiles(want)
	v2, _, err := alice.publish(t, la, all, false)
	if err != nil {
		t.Fatal(err)
	}
	l := Local{Dir: t.TempDir()}
	g, err := gather(l, all, testID, Anchor{Root: v1}, GatherOptions{Sync: true}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	if g.Verdict.Heads[0] != v2 || !slices.Equal(g.Roots(v2), []string{v1}) {
		t.Fatalf("verdict %+v roots %v", g.Verdict, g.Roots(v2))
	}
	if err := l.Checkout(v2); err != nil {
		t.Fatal(err)
	}
	if err := l.SetRoot(v1); err != nil {
		t.Fatal(err)
	}
	got, err := l.Files()
	if err != nil || string(got.Paisans) != string(want.Paisans) || string(got.Secrets) != string(want.Secrets) || string(got.SOPS) != string(want.SOPS) {
		t.Fatalf("files %+v %v", got, err)
	}
	if head, _ := l.Head(); head != v2 {
		t.Fatalf("head %q", head)
	}
	if root, _ := l.Root(); root != v1 {
		t.Fatalf("root %q", root)
	}
}

// A publish signed by a key no operator of the parent lists is refused, and
// so is one that reaches no site.
func TestPublishRefusesAKeyNotAnAdminsAndNoSite(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice, mallory := newOperator(t), newOperator(t)
	sites := storeSites(t, "home-a")
	l := workstation(t, edit(testFiles(), "1", map[string]string{"alice": alice.pub}))
	if _, _, err := mallory.publish(t, l, reach(sites, "home-a"), false); err == nil || !strings.Contains(err.Error(), "operator") {
		t.Fatalf("mallory: %v", err)
	}
	sites["home-a"].down = true
	if _, _, err := alice.publish(t, l, reach(sites, "home-a"), false); err == nil || !strings.Contains(err.Error(), "no site") {
		t.Fatalf("no site: %v", err)
	}
	if head, _ := l.Head(); head != "" {
		t.Fatalf("a refused publish recorded head %s", head)
	}
}

// State files hold one value each, and a malformed one is an error rather
// than an empty value.
func TestStateFiles(t *testing.T) {
	l := Local{Dir: t.TempDir()}
	if r, err := l.Root(); r != "" || err != nil {
		t.Fatalf("absent root %q %v", r, err)
	}
	sha := strings.Repeat("a", 40)
	if err := l.SetHead(sha); err != nil {
		t.Fatal(err)
	}
	if h, err := l.Head(); h != sha || err != nil {
		t.Fatalf("head %q %v", h, err)
	}
	if err := l.SetRoot("nonsense"); err == nil {
		t.Fatal("a malformed root was written")
	}
	os.WriteFile(filepath.Join(l.Dir, ".paisans", "config-root"), []byte("nonsense\n"), 0o644)
	if _, err := l.Root(); err == nil {
		t.Fatal("a malformed root was read")
	}
}

// HeadIs names the head and its revision only for the head's files, and
// creates no state.
func TestHeadIsTheHeadOnlyForItsFiles(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	sites := storeSites(t, "home-a")
	f := edit(testFiles(), "1", map[string]string{"alice": alice.pub})
	l := workstation(t, f)
	if sha, rev, err := l.HeadIs(f); sha != "" || rev != 0 || err != nil {
		t.Fatalf("before a publish: %q %d %v", sha, rev, err)
	}
	if _, err := os.Stat(filepath.Join(l.Dir, ".paisans")); !os.IsNotExist(err) {
		t.Fatalf("HeadIs made the state directory: %v", err)
	}
	published, _, err := alice.publish(t, l, reach(sites, "home-a"), false)
	if err != nil {
		t.Fatal(err)
	}
	if sha, rev, err := l.HeadIs(f); sha != published || rev != 1 || err != nil {
		t.Fatalf("published: %q %d %v, want %s at revision 1", sha, rev, err, published)
	}
	if sha, rev, err := l.HeadIs(edit(f, "2", map[string]string{"alice": alice.pub})); sha != "" || rev != 0 || err != nil {
		t.Fatalf("edited: %q %d %v", sha, rev, err)
	}
}

// A publish signs as the commands do, with key::<public key line>, so the
// private key is the ssh-agent's alone.
func TestPublishSignsThroughTheAgent(t *testing.T) {
	needTools(t, "git", "ssh-keygen", "ssh-agent", "ssh-add")
	alice := newOperator(t)
	// A socket's path is limited to about a hundred bytes, which a test's
	// temporary directory can exceed.
	short, err := os.MkdirTemp("/tmp", "pa")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	sock := filepath.Join(short, "s")
	agent := exec.Command("ssh-agent", "-D", "-a", sock)
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.Process.Kill(); agent.Wait() })
	t.Setenv("SSH_AUTH_SOCK", sock)
	for i := 0; i < 50; i++ {
		if err = exec.Command("ssh-add", alice.key).Run(); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("ssh-add: %v", err)
	}
	sites := storeSites(t, "home-a")
	l := workstation(t, edit(testFiles(), "1", map[string]string{"alice": alice.pub}))
	sha, _, err := publish(l, reach(sites, "home-a"), publishOpts{keyLine: alice.pub, signWith: "key::" + alice.pub, now: testNow}, fakePull, fakePush)
	if err != nil {
		t.Fatal(err)
	}
	g, err := gather(l, reach(sites, "home-a"), testID, mustAnchor(t, l), GatherOptions{Sync: true}, fakePull)
	if err != nil || !g.Verdict.Trusted[sha] || !g.Versions[sha].Signed {
		t.Fatalf("verdict %+v %v", g.Verdict, err)
	}
}

// The workstation remembers the newest revision it has seen. With the cache
// gone and a site serving only an older version, gathering refuses, and
// still returns what it found for an operator who lets it go on.
func TestGatherRefusesARollbackTheWorkstationRemembers(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	operators := map[string]string{"alice": alice.pub}
	sites := storeSites(t, "home-a", "home-b")
	l := workstation(t, edit(testFiles(), "1", operators))
	v1, _, err := alice.publish(t, l, reach(sites, "home-a", "home-b"), false)
	if err != nil {
		t.Fatal(err)
	}
	l.WriteFiles(edit(testFiles(), "2", operators))
	v2, _, err := alice.publish(t, l, reach(sites, "home-a"), false)
	if err != nil {
		t.Fatal(err)
	}
	if rev, sha, _ := l.Seen(); rev != 2 || sha != v2 {
		t.Fatalf("seen %d %s after publishing", rev, sha)
	}
	if err := os.RemoveAll(filepath.Join(l.Dir, ".paisans", "store.git")); err != nil {
		t.Fatal(err)
	}
	g, err := gather(l, reach(sites, "home-b"), testID, mustAnchor(t, l), GatherOptions{Sync: true}, fakePull)
	var rb *RollbackError
	if !errors.As(err, &rb) || rb.Revision != 1 || rb.SHA != v1 || rb.SeenRevision != 2 || rb.SeenSHA != v2 {
		t.Fatalf("got %v", err)
	}
	if len(g.Verdict.Heads) != 1 || g.Verdict.Heads[0] != v1 {
		t.Fatalf("verdict %+v", g.Verdict)
	}
	if rev, _, _ := l.Seen(); rev != 2 {
		t.Fatalf("seen lowered to %d", rev)
	}
	if _, _, err := alice.publish(t, l, reach(sites, "home-b"), false); !errors.As(err, &rb) {
		t.Fatalf("publish on a rollback: %v", err)
	}
}

// A dry run's gather does not ask a site to sync.
func TestGatherSyncsOnlyWhenAsked(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	sites := storeSites(t, "home-a")
	var asked []bool
	pull := func(tr apply.Transport, d deployment.Deployment, sync bool) ([]byte, *SyncReport, error) {
		asked = append(asked, sync)
		return fakePull(tr, d, sync)
	}
	l := Local{Dir: t.TempDir()}
	for _, sync := range []bool{false, true} {
		g, err := gather(l, reach(sites, "home-a"), testID, Anchor{}, GatherOptions{Sync: sync}, pull)
		if err != nil || g.Synced != sync {
			t.Fatalf("sync %v: %+v %v", sync, g, err)
		}
	}
	if !slices.Equal(asked, []bool{false, true}) {
		t.Fatalf("asked %v", asked)
	}
}

// forge adds a chain of n versions on parent to a site's store, each signed
// by mallory and listing mallory as an operator in its own paisans-stack.yaml, which
// is the history someone with root on a site can write.
func forge(t *testing.T, s *storeSite, mallory operator, parent string, rev, n int) []string {
	t.Helper()
	var out []string
	for i := range n {
		f := edit(testFiles(), "forged", map[string]string{"alice": mallory.pub})
		sha, err := s.repo.Commit(f, []string{parent}, rev+i, mallory.key, testNow)
		if err != nil {
			t.Fatal(err)
		}
		out, parent = append(out, sha), sha
	}
	return out
}

// A version mallory signs on v1, listing herself as an operator, is not trusted
// and not a head, and nor is a longer chain of them that would outrank the
// real head by revision.
func TestForgedVersionsAreNeverHeads(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice, mallory := newOperator(t), newOperator(t)
	operators := map[string]string{"alice": alice.pub}
	sites := storeSites(t, "home-a")
	l := workstation(t, edit(testFiles(), "1", operators))
	v1, _, err := alice.publish(t, l, reach(sites, "home-a"), false)
	if err != nil {
		t.Fatal(err)
	}
	l.WriteFiles(edit(testFiles(), "2", operators))
	v2, _, err := alice.publish(t, l, reach(sites, "home-a"), false)
	if err != nil {
		t.Fatal(err)
	}
	one := forge(t, sites["home-a"], mallory, v1, 2, 1)
	chain := forge(t, sites["home-a"], mallory, v1, 2, 4)
	g, err := gather(l, reach(sites, "home-a"), testID, mustAnchor(t, l), GatherOptions{Sync: true}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range append(one, chain...) {
		if _, held := g.Versions[m]; !held || g.Verdict.Trusted[m] || slices.Contains(g.Verdict.Heads, m) {
			t.Errorf("forged %s: held %v trusted %v heads %v", m[:12], held, g.Verdict.Trusted[m], g.Verdict.Heads)
		}
	}
	if !slices.Equal(g.Verdict.Heads, []string{v2}) || g.Versions[chain[3]].Revision != 5 {
		t.Fatalf("heads %v", g.Verdict.Heads)
	}
}

// A gather never writes the seen record: it carries the change, which
// RecordSeen writes, and an accepted rollback is the one change that lowers
// it.
func TestAGatherCarriesTheSeenChangeAndRecordSeenWritesIt(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	operators := map[string]string{"alice": alice.pub}
	sites := storeSites(t, "home-a")
	l := workstation(t, edit(testFiles(), "1", operators))
	v1, _, err := alice.publish(t, l, reach(sites, "home-a"), false)
	if err != nil {
		t.Fatal(err)
	}
	l.WriteFiles(edit(testFiles(), "2", operators))
	v2, _, err := alice.publish(t, l, reach(sites, "home-a"), false)
	if err != nil {
		t.Fatal(err)
	}
	fresh := Local{Dir: t.TempDir()}
	g, err := gather(fresh, reach(sites, "home-a"), testID, Anchor{Root: v1}, GatherOptions{}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	if rev, _, _ := fresh.Seen(); rev != 0 {
		t.Fatalf("a gather wrote seen %d", rev)
	}
	if g.Seen == nil || g.Seen.Revision != 2 || g.Seen.SHA != v2 || g.Seen.Lower {
		t.Fatalf("carried %+v", g.Seen)
	}
	if err := fresh.RecordSeen(g); err != nil {
		t.Fatal(err)
	}
	if rev, sha, _ := fresh.Seen(); rev != 2 || sha != v2 {
		t.Fatalf("recorded %d %s", rev, sha)
	}
	// An older head is a rollback, which carries nothing until accepted.
	if err := fresh.raiseSeen(7, strings.Repeat("c", 40)); err != nil {
		t.Fatal(err)
	}
	g, err = gather(fresh, reach(sites, "home-a"), testID, Anchor{Root: v1}, GatherOptions{}, fakePull)
	var rb *RollbackError
	if !errors.As(err, &rb) || g.Seen != nil {
		t.Fatalf("got %v, carried %+v", err, g.Seen)
	}
	g.AcceptRollback(rb)
	if rev, _, _ := fresh.Seen(); rev != 7 {
		t.Fatalf("accepting wrote seen %d before RecordSeen", rev)
	}
	if err := fresh.RecordSeen(g); err != nil {
		t.Fatal(err)
	}
	if rev, sha, _ := fresh.Seen(); rev != 2 || sha != v2 {
		t.Fatalf("accepted rollback left %d %s", rev, sha)
	}
}

func (o operator) attest(t *testing.T, l Local, sites map[string]apply.Transport) (string, error) {
	t.Helper()
	return attest(l, sites, o.pub, o.key, testNow, fakePull, fakePush)
}

// twoVersions is alice's v1 and v2 on every site, operators alice and bob, and
// bob's workstation holding v2's files with nothing recorded.
func twoVersions(t *testing.T) (alice, bob operator, sites map[string]*storeSite, v1, v2 string, lb Local) {
	t.Helper()
	needTools(t, "git", "ssh-keygen")
	alice, bob = newOperator(t), newOperator(t)
	operators := map[string]string{"alice": alice.pub, "bob": bob.pub}
	sites = storeSites(t, "home-a", "home-b")
	all := reach(sites, "home-a", "home-b")
	la := workstation(t, edit(testFiles(), "1", operators))
	v1, _, err := alice.publish(t, la, all, false)
	if err != nil {
		t.Fatal(err)
	}
	la.WriteFiles(edit(testFiles(), "2", operators))
	if v2, _, err = alice.publish(t, la, all, false); err != nil {
		t.Fatal(err)
	}
	lb = workstation(t, edit(testFiles(), "2", operators))
	return alice, bob, sites, v1, v2, lb
}

// An attestation is the head's tree on the head, one revision on, signed by
// the operator; it is pushed to every site, recorded as the head and the
// key's anchor, and a later judgement from the key alone starts from it.
func TestAttestSignsTheHeadAndBecomesTheAnchor(t *testing.T) {
	_, bob, sites, v1, v2, lb := twoVersions(t)
	all := reach(sites, "home-a", "home-b")
	if _, err := bob.attest(t, lb, all); !errors.Is(err, ErrNoAnchor) {
		t.Fatalf("no pin and nothing signed by bob: %v", err)
	}
	if k, _ := lb.SigningKey(); k != "" {
		t.Fatalf("a refused attestation recorded key %q", k)
	}
	if err := lb.SetRoot(v1); err != nil {
		t.Fatal(err)
	}
	sha, err := bob.attest(t, lb, all)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sites {
		if !held(t, s, sha) {
			t.Errorf("%s lacks the attestation", s.name)
		}
	}
	cache, _ := lb.Cache()
	f, err := cache.Files(sha)
	hf, _ := cache.Files(v2)
	if err != nil || !f.Equal(hf) {
		t.Fatalf("the attestation's tree is not the head's: %v", err)
	}
	key, _ := lb.SigningKey()
	head, _ := lb.Head()
	root, _ := lb.Root()
	rev, seen, _ := lb.Seen()
	if key != bob.pub || head != sha || root != v1 || rev != 3 || seen != sha {
		t.Fatalf("key %q head %q root %q seen %d %q", key, head, root, rev, seen)
	}
	// A workstation that forgot the pin trusts from bob's key alone.
	os.Remove(filepath.Join(lb.Dir, ".paisans", "config-root"))
	g, err := gather(lb, all, testID, mustAnchor(t, lb), GatherOptions{}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	v := g.Versions[sha]
	if g.Verdict.AnchorAt != sha || !slices.Equal(g.Verdict.Heads, []string{sha}) || !slices.Equal(v.Parents, []string{v2}) || v.Revision != 3 {
		t.Fatalf("verdict %+v, version %+v", g.Verdict, v)
	}
	// Registering again signs nothing new: the head is already bob's.
	again, err := bob.attest(t, lb, all)
	if err != nil || again != sha {
		t.Fatalf("again: %s %v", again, err)
	}
}

// Local files that differ from the head are refused with both sides, and
// nothing is recorded or pushed.
func TestAttestRefusesFilesThatDifferFromTheHead(t *testing.T) {
	alice, bob, sites, v1, v2, lb := twoVersions(t)
	lb.SetRoot(v1)
	mine := edit(testFiles(), "mine", map[string]string{"alice": alice.pub, "bob": bob.pub})
	lb.WriteFiles(mine)
	_, err := bob.attest(t, lb, reach(sites, "home-a", "home-b"))
	var de *DiffersError
	if !errors.As(err, &de) || de.Head != v2 || !de.Local.Equal(mine) || de.Theirs.Equal(mine) {
		t.Fatalf("got %v", err)
	}
	if k, _ := lb.SigningKey(); k != "" {
		t.Fatalf("key recorded %q", k)
	}
	if h, _ := lb.Head(); h != "" {
		t.Fatalf("head recorded %q", h)
	}
	shas, _ := sites["home-a"].repo.versionRefs()
	if len(shas) != 2 {
		t.Fatalf("home-a holds %d versions", len(shas))
	}
}

// With no local files and a pinned root, the head the pin leads to is
// attested and its files are written; with no pin it is refused.
func TestAttestWritesTheHeadsFilesOnlyFromAPin(t *testing.T) {
	_, bob, sites, v1, v2, _ := twoVersions(t)
	all := reach(sites, "home-a", "home-b")
	l := Local{Dir: t.TempDir()}
	g, err := gather(l, all, testID, Anchor{Root: v1}, GatherOptions{}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := attestFrom(l, g, all, "", bob.pub, bob.key, testNow, fakePush); err == nil || !strings.Contains(err.Error(), "--root") {
		t.Fatalf("no files and no pin: %v", err)
	}
	if l.HasFiles() {
		t.Fatal("a refused attestation wrote the files")
	}
	head, write, err := AttestTarget(l, g, v1)
	if err != nil || head != v2 || !write {
		t.Fatalf("target %s %v %v", head, write, err)
	}
	sha, _, err := attestFrom(l, g, all, v1, bob.pub, bob.key, testNow, fakePush)
	if err != nil {
		t.Fatal(err)
	}
	cache, _ := l.Cache()
	want, _ := cache.Files(v2)
	got, err := l.Files()
	if err != nil || !got.Equal(want) {
		t.Fatalf("files %v", err)
	}
	if h, _ := l.Head(); h != sha {
		t.Fatalf("head %s", h)
	}
	if r, _ := l.Root(); r != v1 {
		t.Fatalf("root %s", r)
	}
}

// publish is a gather and publishFrom in one, through pull and push: a
// command gathers itself, for its own prompts, and calls PublishFrom.
func publish(l Local, sites map[string]apply.Transport, o publishOpts, pull PullFunc, push PushFunc) (string, []string, error) {
	f, err := l.Files()
	if err != nil {
		return "", nil, fmt.Errorf("read the files to publish: %w", err)
	}
	local, err := config.LoadBytes(f.Paisans)
	if err != nil {
		return "", nil, err
	}
	a, err := l.Anchor()
	if err != nil {
		return "", nil, err
	}
	g, err := gather(l, sites, local.ID, a, GatherOptions{Sync: true}, pull)
	if err != nil {
		return "", nil, err
	}
	return publishFrom(l, g, sites, o, push)
}

// attest gathers from the sites the local paisans-stack.yaml declares, anchored
// on the pinned root and on keyLine, and calls attestFrom through push.
func attest(l Local, sites map[string]apply.Transport, keyLine, signWith string, now time.Time, pull PullFunc, push PushFunc) (string, error) {
	f, err := l.Files()
	if err != nil {
		return "", fmt.Errorf("read the local files: %w", err)
	}
	local, err := config.LoadBytes(f.Paisans)
	if err != nil {
		return "", err
	}
	key, opts, err := config.ParseKeyLine(keyLine)
	if err != nil || len(opts) > 0 {
		return "", fmt.Errorf("the signing key is not a bare public key line: %q", keyLine)
	}
	root, err := l.Root()
	if err != nil {
		return "", err
	}
	g, err := gather(l, sites, local.ID, Anchor{Root: root, Signers: []string{key.Fingerprint}}, GatherOptions{Sync: true}, pull)
	if err != nil {
		return "", err
	}
	sha, _, err := attestFrom(l, g, sites, root, keyLine, signWith, now, push)
	return sha, err
}

// A run that publishes a version and then writes the secrets itself
// publishes the next version on its own, from the gather it made before the
// first: the sites hold the run's version as their head, so the next is
// revision 2 on it, trusted and the head once gathered again.
func TestPublishNextBuildsOnTheRunsVersion(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	sites := storeSites(t, "home-a", "home-b")
	all := reach(sites, "home-a", "home-b")
	first := edit(testFiles(), "1", map[string]string{"alice": alice.pub})
	l := workstation(t, first)
	g, err := gather(l, all, testID, mustAnchor(t, l), GatherOptions{Sync: true}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	o := publishOpts{keyLine: alice.pub, signWith: alice.key, now: testNow}
	v1, _, err := publishFiles(l, g, first, all, o, fakePush)
	if err != nil {
		t.Fatal(err)
	}
	next := first
	next.Secrets = []byte("s, with what the run recorded\n")
	v2, pushed, err := publishNext(l, g, v1, 1, next, all, o, fakePush)
	if err != nil || v2 == v1 || len(pushed) != 2 {
		t.Fatalf("next: %s %v %v", v2, pushed, err)
	}
	if head, _ := l.Head(); head != v2 || !held(t, sites["home-a"], v2) || !held(t, sites["home-b"], v2) {
		t.Fatalf("head %s, held %v %v", head, held(t, sites["home-a"], v2), held(t, sites["home-b"], v2))
	}
	again, err := gather(l, all, testID, mustAnchor(t, l), GatherOptions{Sync: true}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Verdict.Heads) != 1 || again.Verdict.Heads[0] != v2 || !again.Verdict.Trusted[v2] || again.Versions[v2].Revision != 2 || !slices.Equal(again.Versions[v2].Parents, []string{v1}) {
		t.Fatalf("verdict %+v, version %+v", again.Verdict, again.Versions[v2])
	}

	// Once the head has moved past the version named, nothing is published.
	other := next
	other.Secrets = []byte("s, again\n")
	if _, _, err := publishNext(l, g, v1, 1, other, all, o, fakePush); err == nil {
		t.Fatal("published on a version that is not the head")
	}
}

// Outside the explicit test mode a version holding plaintext secrets is
// never published, by publishFiles or publishNext: nothing is signed or
// pushed, and the local head does not move.
func TestPublishRefusesPlaintextSecrets(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	sites := storeSites(t, "home-a", "home-b")
	all := reach(sites, "home-a", "home-b")
	first := edit(testFiles(), "1", map[string]string{"alice": alice.pub})
	l := workstation(t, first)
	g, err := gather(l, all, testID, mustAnchor(t, l), GatherOptions{Sync: true}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	o := publishOpts{keyLine: alice.pub, signWith: alice.key, now: testNow}
	v1, _, err := publishFiles(l, g, first, all, o, fakePush)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.PlaintextSecretsVariable, "")
	next := first
	next.Secrets = []byte("version: 2\n")
	if _, _, err := publishFiles(l, g, next, all, o, fakePush); err == nil || !strings.Contains(err.Error(), "is not encrypted") {
		t.Errorf("publishFiles = %v, want a refusal of plaintext", err)
	}
	if _, _, err := publishNext(l, g, v1, 1, next, all, o, fakePush); err == nil || !strings.Contains(err.Error(), "is not encrypted") {
		t.Errorf("publishNext = %v, want a refusal of plaintext", err)
	}
	if head, _ := l.Head(); head != v1 {
		t.Errorf("head moved to %s", head)
	}
}

// A version published with plaintext secrets is never written to disk
// outside the explicit test mode: not by a checkout (pull, recover), not to
// incoming/, and not by an attestation, which refuses before it signs.
func TestAPlaintextVersionIsNeverWrittenToDisk(t *testing.T) {
	_, bob, sites, v1, v2, _ := twoVersions(t)
	all := reach(sites, "home-a", "home-b")
	t.Setenv(config.PlaintextSecretsVariable, "")
	l := Local{Dir: t.TempDir()}
	g, err := gather(l, all, testID, Anchor{Root: v1}, GatherOptions{Sync: true}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Checkout(v2); err == nil || !strings.Contains(err.Error(), "is not encrypted") {
		t.Errorf("Checkout = %v, want a refusal of plaintext", err)
	}
	if l.HasFiles() {
		t.Error("a refused checkout wrote files")
	}
	if head, _ := l.Head(); head != "" {
		t.Errorf("head %s", head)
	}
	if err := l.WriteIncomingFor(v2); err == nil {
		t.Error("WriteIncomingFor wrote a plaintext version")
	}
	if entries, _ := os.ReadDir(l.Incoming()); len(entries) != 0 {
		t.Errorf("incoming holds %v", entries)
	}
	if _, _, err := attestFrom(l, g, all, v1, bob.pub, bob.key, testNow, fakePush); err == nil || !strings.Contains(err.Error(), "is not encrypted") {
		t.Errorf("attest = %v, want a refusal of plaintext", err)
	}
	if l.HasFiles() {
		t.Error("a refused attestation wrote files")
	}
	if head, _ := l.Head(); head != "" {
		t.Errorf("a refused attestation moved the head to %s", head)
	}
}

// stateTree is every path under l's .paisans/ with its mode, modification
// time and contents.
func stateTree(t *testing.T, l Local) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(filepath.Join(l.Dir, stateDir), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		v := fmt.Sprintf("%v %d", info.Mode(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			v += " " + string(b)
		}
		out[p] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// HeldStore opens only a store that is there, and creates nothing when none
// is.
func TestHeldStoreCreatesNothing(t *testing.T) {
	needTools(t, "git")
	l := Local{Dir: t.TempDir()}
	if _, ok, err := l.HeldStore(); ok || err != nil {
		t.Fatalf("HeldStore with no store: %v, %v", ok, err)
	}
	if _, err := os.Lstat(filepath.Join(l.Dir, stateDir)); !os.IsNotExist(err) {
		t.Fatalf(".paisans/ was created: %v", err)
	}
	if _, err := l.Cache(); err != nil {
		t.Fatal(err)
	}
	r, ok, err := l.HeldStore()
	if !ok || err != nil || r.Dir != filepath.Join(l.Dir, stateDir, cacheDir) {
		t.Fatalf("HeldStore of a store: %+v, %v, %v", r, ok, err)
	}
}

// JudgeHeld judges what the store holds as a gather reaching no site would,
// and writes nothing in .paisans/: no .gitignore put back, no store setting,
// no seen record.
func TestJudgeHeldWritesNothing(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	operators := map[string]string{"alice": alice.pub}
	sites := storeSites(t, "home-a")
	l := workstation(t, edit(testFiles(), "1", operators))
	v1, _, err := alice.publish(t, l, reach(sites, "home-a"), false)
	if err != nil {
		t.Fatal(err)
	}
	l.WriteFiles(edit(testFiles(), "2", operators))
	v2, _, err := alice.publish(t, l, reach(sites, "home-a"), false)
	if err != nil {
		t.Fatal(err)
	}
	src, _ := l.Cache()
	bundle, err := src.Bundle()
	if err != nil {
		t.Fatal(err)
	}
	other := Local{Dir: t.TempDir()}
	to, _ := other.Cache()
	if err := to.FetchBundle(bundle); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(other.Dir, stateDir, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	before := stateTree(t, other)
	r, ok, err := other.HeldStore()
	if !ok || err != nil {
		t.Fatalf("HeldStore: %v, %v", ok, err)
	}
	g, err := JudgeHeld(other, r, testID, Anchor{Root: v1})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Verdict.Heads) != 1 || g.Verdict.Heads[0] != v2 || !g.Verdict.Trusted[v1] || g.Versions[v2].Revision != 2 {
		t.Fatalf("verdict %+v", g.Verdict)
	}
	if g.Seen == nil || g.Seen.Revision != 2 || len(g.Reached) != 0 {
		t.Fatalf("carried %+v, reached %+v", g.Seen, g.Reached)
	}
	after := stateTree(t, other)
	if !maps.Equal(before, after) {
		for p, v := range after {
			if before[p] != v {
				t.Errorf("%s was written", p)
			}
		}
	}
	if rev, _, _ := other.Seen(); rev != 0 {
		t.Errorf("seen %d was recorded", rev)
	}
}

// Declaring finds the newest version in the head's history that declares
// an app, after the app has left paisans-stack.yaml: a re-added app's newest
// declaration wins, and a version that does not load is passed over.
func TestDeclaringIsTheNewestVersionThatDeclaresTheApp(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	ops := map[string]string{"alice": alice.pub}
	news := func(note, name string) Files {
		f := edit(testFiles(), note, ops)
		if name != "" {
			f.Paisans = append(f.Paisans, "apps:\n  news:\n    blueprint: writefreely\n    hostname: news.example.org\n    display_name: "+name+"\n"...)
		}
		return f
	}
	l := workstation(t, news("0", ""))
	if d, err := l.Declaring("news"); d.History || d.Config != nil || err != nil {
		t.Fatalf("with no head: %+v, %v", d, err)
	}
	if err := l.SetHead(strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	if d, err := l.Declaring("news"); d.History || err != nil {
		t.Fatalf("with a head and no store: %+v, %v", d, err)
	}
	if _, err := os.Stat(l.state(cacheDir)); !os.IsNotExist(err) {
		t.Fatalf("Declaring made a store: %v", err)
	}
	cache, err := l.Cache()
	if err != nil {
		t.Fatal(err)
	}
	unloadable := news("x", "")
	unloadable.Paisans = []byte("version: 1\nnot_a_field: true\n")
	var shas []string
	for i, f := range []Files{news("1", "Old News"), news("2", "Test News"), news("3", ""), news("4", "Back News"), news("5", ""), unloadable} {
		sha, err := cache.Commit(f, shas[max(len(shas)-1, 0):], i+1, alice.key, testNow)
		if err != nil {
			t.Fatal(err)
		}
		shas = append(shas, sha)
	}
	for _, tc := range []struct {
		head       int
		want       string
		unreadable int
	}{{2, "Test News", 0}, {5, "Back News", 1}} {
		if err := l.SetHead(shas[tc.head]); err != nil {
			t.Fatal(err)
		}
		d, err := l.Declaring("news")
		if err != nil || d.Config == nil || !d.History {
			t.Fatalf("head %d: %+v, %v", tc.head, d, err)
		}
		if got := d.Config.ClientName("news"); got != tc.want || len(d.Unreadable) != tc.unreadable {
			t.Errorf("head %d: news's client is %q, passed over %v", tc.head, got, d.Unreadable)
		}
	}
	if d, err := l.Declaring("talk"); d.Config != nil || d.SHA != "" || !d.History || len(d.Unreadable) != 1 || err != nil {
		t.Errorf("an app no version declares: %+v, %v", d, err)
	}
}

// paisansFiles leaves out a version without a paisans-stack.yaml and reads
// the versions after it in the same answer.
func TestPaisansFilesLeavesOutAVersionWithoutOne(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	key, _ := testSSHKey(t)
	cache, err := InitRepo(filepath.Join(t.TempDir(), "c.git"))
	if err != nil {
		t.Fatal(err)
	}
	held, err := cache.Commit(testFiles(), nil, 1, key, testNow)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := cache.git(strings.NewReader(""), nil, "mktree")
	if err != nil {
		t.Fatal(err)
	}
	out, err := cache.git(nil, []string{"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.org", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.org"}, "commit-tree", "-m", "empty", strings.TrimSpace(string(tree)))
	if err != nil {
		t.Fatal(err)
	}
	empty := strings.TrimSpace(string(out))
	files, err := cache.paisansFiles([]string{empty, held, empty})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files[empty]; ok || string(files[held]) != "p\n" || len(files) != 1 {
		t.Errorf("files %q", files)
	}
}

// A publish on a base the caller names builds on it, whatever the head is,
// and is refused, as one on the head is, when the sites' newest is another.
func TestPublishOnTheBaseNamed(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	sites := storeSites(t, "home-a")
	all := reach(sites, "home-a")
	first := edit(testFiles(), "1", map[string]string{"alice": alice.pub})
	l := workstation(t, first)
	g, err := gather(l, all, testID, mustAnchor(t, l), GatherOptions{Sync: true}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	o := publishOpts{keyLine: alice.pub, signWith: alice.key, now: testNow}
	v1, _, err := publishFiles(l, g, first, all, o, fakePush)
	if err != nil {
		t.Fatal(err)
	}
	if g, err = gather(l, all, testID, mustAnchor(t, l), GatherOptions{Sync: true}, fakePull); err != nil {
		t.Fatal(err)
	}
	if err := l.SetHead(strings.Repeat("d", 40)); err != nil {
		t.Fatal(err)
	}
	base := v1
	second := edit(testFiles(), "2", map[string]string{"alice": alice.pub})
	o.base = &base
	v2, _, err := publishFiles(l, g, second, all, o, fakePush)
	if err != nil || v2 == v1 {
		t.Fatalf("on %s: %s, %v", v1, v2, err)
	}
	if head, _ := l.Head(); head != v2 {
		t.Errorf("head %s", head)
	}
	stale := strings.Repeat("e", 40)
	o.base = &stale
	if _, _, err := publishFiles(l, g, edit(testFiles(), "3", map[string]string{"alice": alice.pub}), all, o, fakePush); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("on a base the sites' newest is not: %v", err)
	}
}

// Mark's undo puts back the records and drops every version a publish
// added to the store, so a gather finds the head as it was.
func TestMarkTakesAPublishBack(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	sites := storeSites(t, "home-a")
	all := reach(sites, "home-a")
	first := edit(testFiles(), "1", map[string]string{"alice": alice.pub})
	l := workstation(t, first)
	g, err := gather(l, all, testID, mustAnchor(t, l), GatherOptions{Sync: true}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	o := publishOpts{keyLine: alice.pub, signWith: alice.key, now: testNow}
	v1, _, err := publishFiles(l, g, first, all, o, fakePush)
	if err != nil {
		t.Fatal(err)
	}
	if g, err = gather(l, all, testID, mustAnchor(t, l), GatherOptions{Sync: true}, fakePull); err != nil {
		t.Fatal(err)
	}
	seen, _ := os.ReadFile(filepath.Join(l.Dir, ".paisans", "config-seen"))
	undo, err := l.Mark()
	if err != nil {
		t.Fatal(err)
	}
	noPush := func(apply.Transport, deployment.Deployment, []byte) error { return errors.New("refused") }
	v2, _, err := publishFiles(l, g, edit(testFiles(), "2", map[string]string{"alice": alice.pub}), all, o, noPush)
	if err == nil || v2 == "" {
		t.Fatalf("got %s, %v", v2, err)
	}
	if err := undo(v2); err != nil {
		t.Fatal(err)
	}
	if head, _ := l.Head(); head != v1 {
		t.Errorf("head %s", head)
	}
	if b, _ := os.ReadFile(filepath.Join(l.Dir, ".paisans", "config-seen")); string(b) != string(seen) {
		t.Errorf("seen %q, was %q", b, seen)
	}
	cache, _ := l.Cache()
	if shas, _ := cache.versionRefs(); !slices.Equal(shas, []string{v1}) {
		t.Errorf("the store names %v", shas)
	}
}

// Mark's undo drops only the version the publish committed: one another
// gather brought into the store meanwhile stays. A first version taken back
// leaves no root and no signing key recorded.
func TestMarkDropsOnlyTheCommittedVersion(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	sites := storeSites(t, "home-a")
	all := reach(sites, "home-a")
	first := edit(testFiles(), "1", map[string]string{"alice": alice.pub})
	l := workstation(t, first)
	g, err := gather(l, all, testID, mustAnchor(t, l), GatherOptions{Sync: true}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	undo, err := l.Mark()
	if err != nil {
		t.Fatal(err)
	}
	o := publishOpts{keyLine: alice.pub, signWith: alice.key, now: testNow}
	noPush := func(apply.Transport, deployment.Deployment, []byte) error { return errors.New("refused") }
	v1, _, err := publishFiles(l, g, first, all, o, noPush)
	if err == nil || v1 == "" {
		t.Fatalf("got %s, %v", v1, err)
	}
	if root, _ := l.Root(); root != v1 {
		t.Fatalf("root %q", root)
	}
	// What a concurrent gather brought in: another version, named.
	cache, _ := l.Cache()
	other, err := cache.Commit(edit(testFiles(), "other", map[string]string{"alice": alice.pub}), nil, 1, alice.key, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := undo(v1); err != nil {
		t.Fatal(err)
	}
	root, _ := l.Root()
	key, _ := l.SigningKey()
	head, _ := l.Head()
	if root != "" || key != "" || head != "" {
		t.Errorf("root %q, key %q, head %q left", root, key, head)
	}
	if shas, _ := cache.versionRefs(); !slices.Equal(shas, []string{other}) {
		t.Errorf("the store names %v, want only %s", shas, other)
	}
}

// A publish whose record fails after it committed returns the version with
// its error, so undo drops exactly it: the store names no orphan, head and
// seen are as they were, and the next publish is the one version on the
// sites, no fork. Given "", undo drops nothing and names what the store
// gained.
func TestMarkDropsAVersionCommittedBeforeAFailure(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	sites := storeSites(t, "home-a")
	all := reach(sites, "home-a")
	first := edit(testFiles(), "1", map[string]string{"alice": alice.pub})
	l := workstation(t, first)
	g, err := gather(l, all, testID, mustAnchor(t, l), GatherOptions{Sync: true}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	undo, err := l.Mark()
	if err != nil {
		t.Fatal(err)
	}
	// The root cannot be written over a directory of its name: the first
	// version's SetRoot fails after its commit.
	if err := os.MkdirAll(filepath.Join(l.Dir, ".paisans", "config-root"), 0o755); err != nil {
		t.Fatal(err)
	}
	o := publishOpts{keyLine: alice.pub, signWith: alice.key, now: testNow}
	sha, _, err := publishFiles(l, g, first, all, o, fakePush)
	if err == nil || sha == "" {
		t.Fatalf("got %q, %v", sha, err)
	}
	if err := undo(sha); err != nil {
		t.Fatal(err)
	}
	cache, _ := l.Cache()
	if shas, _ := cache.versionRefs(); len(shas) != 0 {
		t.Errorf("the store names %v", shas)
	}
	if head, _ := l.Head(); head != "" {
		t.Errorf("head %s", head)
	}
	if rev, _, _ := l.Seen(); rev != 0 {
		t.Errorf("seen %d", rev)
	}
	again := edit(testFiles(), "2", map[string]string{"alice": alice.pub})
	v, _, err := publishFiles(l, g, again, all, o, fakePush)
	if err != nil {
		t.Fatal(err)
	}
	if shas, _ := cache.versionRefs(); !slices.Equal(shas, []string{v}) {
		t.Errorf("the store names %v, want only %s", shas, v)
	}
	if held, _ := sites["home-a"].repo.versionRefs(); !slices.Equal(held, []string{v}) {
		t.Errorf("home-a holds %v, want only %s", held, v)
	}

	// undo("") leaves what the store gained, and says so.
	undo, err = l.Mark()
	if err != nil {
		t.Fatal(err)
	}
	other, err := cache.Commit(edit(testFiles(), "other", map[string]string{"alice": alice.pub}), []string{v}, 2, alice.key, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := undo(""); err == nil || !strings.Contains(err.Error(), other) {
		t.Errorf("undo(\"\") = %v", err)
	}
	if shas, _ := cache.versionRefs(); !slices.Contains(shas, other) {
		t.Errorf("dropped %s", other)
	}
}
