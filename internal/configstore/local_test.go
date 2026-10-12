package configstore

import (
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/deployment"
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

// testConfig is a paisans.yaml that loads, listing each admin's key. alice
// is always one, since the site's ssh section names her. note changes the
// community's name, so two edits differ.
func testConfig(note string, admins map[string]string) []byte {
	var a strings.Builder
	for _, id := range slices.Sorted(maps.Keys(admins)) {
		a.WriteString("  " + id + ":\n    name: " + strings.ToUpper(id[:1]) + id[1:] + " Example\n    email: " + id + "@example.org\n    keys: |\n      " + admins[id] + "\n")
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
admins:
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

func edit(f Files, note string, admins map[string]string) Files {
	f.Paisans = testConfig(note, admins)
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
	if err != nil || !strings.Contains(string(ignore), "store.git/") || !strings.Contains(string(ignore), "incoming/") {
		t.Fatalf(".gitignore %q, %v", ignore, err)
	}
	g, err := gather(l, reach(sites, "home-a"), 3, testID, mustAnchor(t, l), GatherOptions{Sync: true, Remember: true}, fakePull)
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
	admins := map[string]string{"alice": alice.pub, "bob": bob.pub}
	sites := storeSites(t, "home-a", "home-b")
	all := reach(sites, "home-a", "home-b")
	la := workstation(t, edit(testFiles(), "1", admins))
	v1, _, err := alice.publish(t, la, all, false)
	if err != nil {
		t.Fatal(err)
	}
	// Bob recovers v1 with the root pinned, then alice publishes v2.
	lb := Local{Dir: t.TempDir()}
	if err := lb.SetRoot(v1); err != nil {
		t.Fatal(err)
	}
	g, err := gather(lb, all, 2, testID, mustAnchor(t, lb), GatherOptions{Sync: true, Remember: true}, fakePull)
	if err != nil || g.Verdict.Heads[0] != v1 {
		t.Fatalf("bob's gather %+v %v", g.Verdict, err)
	}
	if err := lb.Checkout(v1); err != nil {
		t.Fatal(err)
	}
	if err := la.WriteFiles(edit(testFiles(), "2", admins)); err != nil {
		t.Fatal(err)
	}
	v2, _, err := alice.publish(t, la, all, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := lb.WriteFiles(edit(testFiles(), "bob", admins)); err != nil {
		t.Fatal(err)
	}
	_, _, err = bob.publish(t, lb, all, false)
	if err == nil || !strings.Contains(err.Error(), "stale") || !strings.Contains(err.Error(), v2[:12]) || !strings.Contains(err.Error(), "paisans config pull") {
		t.Fatalf("stale publish: %v", err)
	}
	if head, _ := lb.Head(); head != v1 {
		t.Fatalf("a refused publish moved the head to %s", head)
	}
}

func TestTwoAdminsFromOneHeadForkAndMergeResolvesIt(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice, bob := newOperator(t), newOperator(t)
	admins := map[string]string{"alice": alice.pub, "bob": bob.pub}
	sites := storeSites(t, "home-a", "home-b")
	all := reach(sites, "home-a", "home-b")
	la := workstation(t, edit(testFiles(), "1", admins))
	v1, _, err := alice.publish(t, la, all, false)
	if err != nil {
		t.Fatal(err)
	}
	lb := Local{Dir: t.TempDir()}
	if err := lb.SetRoot(v1); err != nil {
		t.Fatal(err)
	}
	if _, err := gather(lb, all, 2, testID, mustAnchor(t, lb), GatherOptions{Sync: true, Remember: true}, fakePull); err != nil {
		t.Fatal(err)
	}
	if err := lb.Checkout(v1); err != nil {
		t.Fatal(err)
	}
	// Each reaches only one site, so neither sees the other's version.
	la.WriteFiles(edit(testFiles(), "alice", admins))
	lb.WriteFiles(edit(testFiles(), "bob", admins))
	a2, _, err := alice.publish(t, la, reach(sites, "home-a"), false)
	if err != nil {
		t.Fatal(err)
	}
	b2, _, err := bob.publish(t, lb, reach(sites, "home-b"), false)
	if err != nil {
		t.Fatal(err)
	}
	g, err := gather(la, all, 2, testID, mustAnchor(t, la), GatherOptions{Sync: true, Remember: true}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	if !g.Verdict.Fork || len(g.Verdict.Heads) != 2 {
		t.Fatalf("verdict %+v", g.Verdict)
	}
	la.WriteFiles(edit(testFiles(), "merged", admins))
	if _, _, err := alice.publish(t, la, all, false); err == nil || !strings.Contains(err.Error(), "--merge") {
		t.Fatalf("publish on a fork: %v", err)
	}
	m, _, err := alice.publish(t, la, all, true)
	if err != nil {
		t.Fatal(err)
	}
	g, err = gather(la, all, 2, testID, mustAnchor(t, la), GatherOptions{Sync: true, Remember: true}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	v := g.Versions[m]
	if g.Verdict.Fork || g.Verdict.Heads[0] != m || v.Revision != 3 || !slices.Contains(v.Parents, a2) || !slices.Contains(v.Parents, b2) {
		t.Fatalf("after merge %+v, version %+v", g.Verdict, v)
	}
}

// Only a site reached directly counts toward the majority, and exactly half
// is not one.
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
	g, err := gather(l, reach(sites, "home-a", "home-b", "vm", "watch"), 4, testID, mustAnchor(t, l), GatherOptions{Sync: true, Remember: true}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	if g.Majority {
		t.Fatal("two of four reached counted as a majority")
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
	g, err = gather(l, reach(sites, "home-a", "home-b", "vm", "watch"), 3, testID, mustAnchor(t, l), GatherOptions{Sync: true, Remember: true}, fakePull)
	if err != nil || !g.Majority {
		t.Fatalf("two of three: %v %v", g.Majority, err)
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
	if _, err := gather(empty, reach(sites, "home-a"), 1, testID, mustAnchor(t, empty), GatherOptions{Sync: true, Remember: true}, fakePull); !errors.Is(err, ErrNoAnchor) {
		t.Fatalf("got %v", err)
	}
}

// A recovery onto an empty directory, with the root pinned, writes the head's
// three files and the state that names it.
func TestRecoverWritesTheFilesAndState(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	admins := map[string]string{"alice": alice.pub}
	sites := storeSites(t, "home-a", "home-b")
	all := reach(sites, "home-a", "home-b")
	la := workstation(t, edit(testFiles(), "1", admins))
	v1, _, err := alice.publish(t, la, all, false)
	if err != nil {
		t.Fatal(err)
	}
	want := edit(testFiles(), "2", admins)
	la.WriteFiles(want)
	v2, _, err := alice.publish(t, la, all, false)
	if err != nil {
		t.Fatal(err)
	}
	l := Local{Dir: t.TempDir()}
	g, err := gather(l, all, 2, testID, Anchor{Root: v1}, GatherOptions{Sync: true, Remember: true}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	if g.Verdict.Heads[0] != v2 || !g.Majority || !slices.Equal(g.Roots(v2), []string{v1}) {
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

// A publish signed by a key no admin of the parent lists is refused, and
// so is one that reaches no site.
func TestPublishRefusesAKeyNotAnAdminsAndNoSite(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice, mallory := newOperator(t), newOperator(t)
	sites := storeSites(t, "home-a")
	l := workstation(t, edit(testFiles(), "1", map[string]string{"alice": alice.pub}))
	if _, _, err := mallory.publish(t, l, reach(sites, "home-a"), false); err == nil || !strings.Contains(err.Error(), "admin") {
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

// Publish signs as Publish does, with key::<public key line>, so the private
// key is the ssh-agent's alone.
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
	g, err := gather(l, reach(sites, "home-a"), 1, testID, mustAnchor(t, l), GatherOptions{Sync: true, Remember: true}, fakePull)
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
	admins := map[string]string{"alice": alice.pub}
	sites := storeSites(t, "home-a", "home-b")
	l := workstation(t, edit(testFiles(), "1", admins))
	v1, _, err := alice.publish(t, l, reach(sites, "home-a", "home-b"), false)
	if err != nil {
		t.Fatal(err)
	}
	l.WriteFiles(edit(testFiles(), "2", admins))
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
	g, err := gather(l, reach(sites, "home-b"), 2, testID, mustAnchor(t, l), GatherOptions{Sync: true, Remember: true}, fakePull)
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
		g, err := gather(l, reach(sites, "home-a"), 1, testID, Anchor{}, GatherOptions{Sync: sync}, pull)
		if err != nil || g.Synced != sync {
			t.Fatalf("sync %v: %+v %v", sync, g, err)
		}
	}
	if !slices.Equal(asked, []bool{false, true}) {
		t.Fatalf("asked %v", asked)
	}
}

// forge adds a chain of n versions on parent to a site's store, each signed
// by mallory and listing mallory as an admin in its own paisans.yaml, which
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

// A version mallory signs on v1, listing herself as an admin, is not trusted
// and not a head, and nor is a longer chain of them that would outrank the
// real head by revision.
func TestForgedVersionsAreNeverHeads(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice, mallory := newOperator(t), newOperator(t)
	admins := map[string]string{"alice": alice.pub}
	sites := storeSites(t, "home-a")
	l := workstation(t, edit(testFiles(), "1", admins))
	v1, _, err := alice.publish(t, l, reach(sites, "home-a"), false)
	if err != nil {
		t.Fatal(err)
	}
	l.WriteFiles(edit(testFiles(), "2", admins))
	v2, _, err := alice.publish(t, l, reach(sites, "home-a"), false)
	if err != nil {
		t.Fatal(err)
	}
	one := forge(t, sites["home-a"], mallory, v1, 2, 1)
	chain := forge(t, sites["home-a"], mallory, v1, 2, 4)
	g, err := gather(l, reach(sites, "home-a"), 1, testID, mustAnchor(t, l), GatherOptions{Sync: true, Remember: true}, fakePull)
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

// A gather that may not remember leaves the record alone, and an accepted
// rollback is the one write that lowers it.
func TestSeenIsRaisedOnlyWhenRememberingAndLoweredOnlyByAcceptance(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	sites := storeSites(t, "home-a")
	l := workstation(t, edit(testFiles(), "1", map[string]string{"alice": alice.pub}))
	v1, _, err := alice.publish(t, l, reach(sites, "home-a"), false)
	if err != nil {
		t.Fatal(err)
	}
	fresh := Local{Dir: t.TempDir()}
	if _, err := gather(fresh, reach(sites, "home-a"), 1, testID, Anchor{Root: v1}, GatherOptions{}, fakePull); err != nil {
		t.Fatal(err)
	}
	if rev, _, _ := fresh.Seen(); rev != 0 {
		t.Fatalf("a gather from an unrecorded root raised seen to %d", rev)
	}
	if err := l.AcceptRollback(1, v1); err != nil {
		t.Fatal(err)
	}
	if err := fresh.AcceptRollback(7, strings.Repeat("c", 40)); err != nil {
		t.Fatal(err)
	}
	if err := fresh.AcceptRollback(1, v1); err != nil {
		t.Fatal(err)
	}
	if rev, sha, _ := fresh.Seen(); rev != 1 || sha != v1 {
		t.Fatalf("accepted rollback left %d %s", rev, sha)
	}
}
