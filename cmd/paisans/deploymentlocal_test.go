package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/draft"
	"github.com/paisans-software/paisans-cli/internal/localstore"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// ownStore points the saved deployments at a directory of the test's own.
func ownStore(t *testing.T) localstore.Store {
	t.Helper()
	t.Setenv(localstore.DirVariable, t.TempDir())
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// registryHolding is a registry naming each id, as root's file on a host.
func registryHolding(t *testing.T, ids ...string) string {
	t.Helper()
	r := registry.Registry{Version: registry.Version, Deployments: map[string]registry.Entry{}}
	for _, id := range ids {
		r.Deployments[id] = registry.Entry{Token: id[:4], Root: "/srv/paisans/" + id[:4], Domain: "example.org", Site: "home-a", ClaimedAt: "2026-10-17T00:00:00Z", Build: version.Current()}
	}
	b, err := registry.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fetchFrom makes --ssh reach, as its owner, a host whose registry is reg
// and whose store answers answer.
func fetchFrom(t *testing.T, reg, answer string) {
	t.Helper()
	saved := reachOwner
	t.Cleanup(func() { reachOwner = saved })
	reachOwner = func(dest config.Destination, sudo bool) apply.Transport {
		if !sudo {
			t.Error("fetch reached the host without sudo")
		}
		files := map[string]string{}
		if reg != "" {
			files[registry.Path] = reg
		}
		return &storeHost{name: dest.String(), answer: answer, files: files}
	}
	var b bytes.Buffer
	savedR := reporterOverride
	reporterOverride = ui.NewPlain(&b, false)
	t.Cleanup(func() { reporterOverride = savedR })
}

// deployment apply --execute of a new deployment makes its system copy:
// the version it published, with the key that signed it as the anchor, so
// a later fetch can judge the sites from it wherever the draft went, and
// the record deployment list shows.
func TestApplySavesThePublishedVersion(t *testing.T) {
	f := newApplyStore(t, 0)
	st := must(openStore())
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	head, _ := f.sys.Head()
	if head == "" {
		t.Fatal("nothing was published")
	}
	rec, ok, err := st.Load(fixtureID(t))
	if err != nil || !ok {
		t.Fatalf("no record: %v", err)
	}
	if rec.SHA != head || rec.Revision != 1 || rec.Status != localstore.Verified || rec.Domain != "example.org" || !strings.HasPrefix(rec.Source, "applied from ") {
		t.Errorf("record %+v", rec)
	}
	l, _ := st.Local(fixtureID(t))
	want, _ := draftVersion(draft.Draft{Dir: f.dir})
	got, err := l.Files()
	if err != nil || !bytes.Equal(got.Paisans, want.Paisans) || !bytes.Equal(got.Secrets, want.Secrets) {
		t.Fatalf("saved files differ: %v", err)
	}
	if key, _ := l.SigningKey(); key == "" {
		t.Error("no signing key recorded as the anchor")
	}
	if h, _ := l.Head(); h != head {
		t.Errorf("saved head %q", h)
	}
}

// A dry run saves nothing.
func TestApplyDryRunSavesNothing(t *testing.T) {
	f := newApplyStore(t, 0)
	st := must(openStore())
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	recordConverge(t)
	_ = f.applySite(t, noTerminalStdin())
	if listed, _ := st.List(); len(listed) != 0 {
		t.Fatalf("saved %+v", listed)
	}
}

// A recovery makes the system copy from the sites, its root pinned and the
// record deployment list shows, and starts a draft of it in the folder,
// which holds nothing else.
func TestRecoverCreatesSystemCopyAndDraft(t *testing.T) {
	st := ownStore(t)
	sha, answer, f := storedVersion(t)
	recoverHosts(t, answer)
	dir := t.TempDir()
	var out bytes.Buffer
	if err := runDeployment([]string{"recover", "--from", "ubuntu@203.0.113.9", "--root", sha, "--config", filepath.Join(dir, "paisans-stack.yaml"), "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err))
	}
	sys := heldCopy(t)
	if got, err := sys.Files(); err != nil || !got.Equal(f) {
		t.Fatalf("the system copy's files: %v", err)
	}
	if h, _ := sys.Head(); h != sha {
		t.Errorf("head %q", h)
	}
	if r, _ := sys.Root(); r != sha {
		t.Errorf("the recovery's pinned root was not recorded: %q", r)
	}
	rec, ok, _ := st.Load(fixtureID(t))
	if !ok || rec.SHA != sha || rec.Revision != 1 || rec.Status != localstore.Verified || rec.Source != "recovered into "+dir {
		t.Fatalf("record %+v", rec)
	}
	wantDraftOf(t, dir, sys)
	entries, _ := os.ReadDir(st.Base)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "recover-") {
			t.Errorf("the directory the recovery worked in is left: %s", e.Name())
		}
	}
}

// A recovery only creates: a folder holding any of the three files is
// refused, and so is a deployment whose system copy this workstation holds,
// naming pull and checkout; neither is changed.
func TestRecoverRefusesExistingDraft(t *testing.T) {
	ownStore(t)
	sha, answer, _ := storedVersion(t)
	recoverHosts(t, answer)
	recover := func(dir string) error {
		return runDeployment([]string{"recover", "--from", "ubuntu@203.0.113.9", "--root", sha, "--config", filepath.Join(dir, "paisans-stack.yaml"), "--execute"}, noTerminalStdin(), &bytes.Buffer{})
	}
	part := t.TempDir()
	if err := os.WriteFile(filepath.Join(part, "secrets.enc.yaml"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recover(part); err == nil || !strings.Contains(problemText(err), filepath.Join(part, "secrets.enc.yaml")) {
		t.Fatalf("part of a draft: %v", problemText(err))
	}
	noSystemCopy(t)
	if got, _ := os.ReadFile(filepath.Join(part, "secrets.enc.yaml")); string(got) != "x\n" {
		t.Error("the folder's file was changed")
	}

	first := t.TempDir()
	if err := recover(first); err != nil {
		t.Fatal(problemText(err))
	}
	sys := heldCopy(t)
	before := tree(t, sys.Dir)
	second := t.TempDir()
	err := recover(second)
	text := problemText(err)
	if err == nil || !strings.Contains(text, "deployment checkout --id "+fixtureDeploymentID) || !strings.Contains(text, "deployment pull") {
		t.Fatalf("a system copy held: %v", text)
	}
	if (configstore.Local{Dir: second}).HasFiles() {
		t.Error("a refused recovery started a draft")
	}
	after := tree(t, sys.Dir)
	for p, v := range before {
		if after[p] != v {
			t.Errorf("%s changed", p)
		}
	}
	if len(after) != len(before) {
		t.Errorf("the system copy changed: %d entries, was %d", len(after), len(before))
	}
}

// fileContents is every regular file under dir, by its path from dir.
func fileContents(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for p, v := range tree(t, dir) {
		if b, err := os.ReadFile(p); err == nil && !strings.HasPrefix(v, "d") {
			rel, _ := filepath.Rel(dir, p)
			out[rel] = string(b)
		}
	}
	return out
}

// existingCopy makes the fixture deployment's system copy what an earlier
// run left: the sites' versions in its store and, with head set, that
// version checked out, root pinned when given, and a record of status.
func existingCopy(t *testing.T, st localstore.Store, answer, head, root string, status localstore.Status) configstore.Local {
	t.Helper()
	l, err := st.Local(fixtureID(t))
	if err != nil {
		t.Fatal(err)
	}
	cache, err := l.Cache()
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.FetchBundle(mustBundle(t, answer)); err != nil {
		t.Fatal(err)
	}
	if head != "" {
		if err := l.Checkout(head); err != nil {
			t.Fatal(err)
		}
	}
	if root != "" {
		if err := l.SetRoot(root); err != nil {
			t.Fatal(err)
		}
	}
	if status != "" {
		if err := st.Record(localstore.Saved{ID: fixtureID(t), SHA: head, Revision: 1, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

// Whether a recovery may take the place of a system copy is decided from
// the copy itself, never its record: one with a head is refused, even one
// a fetch recorded as a fork, and so is --root naming the root it pinned;
// --root naming another root replaces it, as does a copy with no head.
// What is replaced is moved aside whole and named, never removed.
func TestRecoverReplacesOnlyWhatItMay(t *testing.T) {
	shas, answer, files, _, pub := storedHistoryKey(t, 2, 0)
	other := strings.Repeat("ab", 20)
	recover := func(t *testing.T, more ...string) (string, error) {
		t.Helper()
		recoverHosts(t, answer)
		allSitesUp(answer, nil)
		agentHolding(t, pub)
		var rep bytes.Buffer
		reporterOverride = ui.NewPlain(&rep, true)
		args := append([]string{"recover", "--from", "ubuntu@203.0.113.9", "--config", filepath.Join(t.TempDir(), "paisans-stack.yaml"), "--execute"}, more...)
		err := runDeployment(args, noTerminalStdin(), &bytes.Buffer{})
		return rep.String(), err
	}
	refused := func(t *testing.T, l configstore.Local, more ...string) {
		t.Helper()
		before := tree(t, l.Dir)
		_, err := recover(t, more...)
		if text := problemText(err); err == nil || !strings.Contains(text, "holds the system copy of "+fixtureID(t)+" already") || !strings.Contains(text, "deployment checkout --id") {
			t.Fatalf("%v: %v", more, text)
		}
		after := tree(t, l.Dir)
		if len(after) != len(before) {
			t.Fatalf("%v: the system copy changed", more)
		}
		for p, v := range before {
			if after[p] != v {
				t.Fatalf("%v: %s changed", more, p)
			}
		}
	}

	t.Run("a head a fetch recorded as a fork", func(t *testing.T) {
		st := ownStore(t)
		l := existingCopy(t, st, answer, shas[0], shas[0], localstore.Fork)
		refused(t, l, "--key", pubFile(t, pub))
	})
	t.Run("--root naming the pinned root", func(t *testing.T) {
		st := ownStore(t)
		l := existingCopy(t, st, answer, shas[0], shas[0], localstore.Verified)
		refused(t, l, "--root", shas[0])
	})
	t.Run("--root naming another root", func(t *testing.T) {
		st := ownStore(t)
		l := existingCopy(t, st, answer, shas[0], other, localstore.Verified)
		old := fileContents(t, l.Dir)
		out, err := recover(t, "--root", shas[0])
		if err != nil {
			t.Fatal(problemText(err))
		}
		sys := heldCopy(t)
		if r, _ := sys.Root(); r != shas[0] {
			t.Errorf("root %q", r)
		}
		if got, _ := sys.Files(); !got.Equal(files[1]) {
			t.Error("the system copy is not the recovered head's")
		}
		asides, _ := filepath.Glob(filepath.Join(st.Base, "replaced", fixtureID(t)+"-*"))
		if len(asides) != 1 {
			t.Fatalf("moved aside: %v", asides)
		}
		if kept := fileContents(t, asides[0]); len(kept) != len(old) {
			t.Errorf("kept aside %d files of %d", len(kept), len(old))
		} else {
			for rel, v := range old {
				if kept[rel] != v {
					t.Errorf("%s was not kept aside as it was", rel)
				}
			}
		}
		for _, want := range []string{"moved aside to " + asides[0], "paisans-stack.yaml", ".paisans", "saved.json", "its pinned root " + other + " is not the root --root names"} {
			if !strings.Contains(out, want) {
				t.Errorf("the warning lacks %q:\n%s", want, out)
			}
		}
	})
	t.Run("no head", func(t *testing.T) {
		st := ownStore(t)
		existingCopy(t, st, answer, "", "", localstore.Unverified)
		out, err := recover(t, "--root", shas[0])
		if err != nil {
			t.Fatal(problemText(err))
		}
		if h, _ := heldCopy(t).Head(); h != shas[1] {
			t.Errorf("head %q", h)
		}
		if !strings.Contains(out, "it held no version") {
			t.Errorf("the warning does not say why:\n%s", out)
		}
	})
}

// A system copy with a head moves only by a deploy or a pull: a fetch
// records in its store the versions the host holds, judged from its anchor,
// and leaves its head, its files and its record as they were, naming
// deployment pull for the newer version.
func TestFetchLeavesAHeadedCopyWhereItIs(t *testing.T) {
	st := ownStore(t)
	shas, answer, files := storedHistory(t, 2, 0)
	// What an earlier recovery left: the first version, its root pinned.
	l, _ := st.Local(fixtureID(t))
	cache, _ := l.Cache()
	g := mustBundle(t, answer)
	if err := cache.FetchBundle(g); err != nil {
		t.Fatal(err)
	}
	if err := l.Checkout(shas[0]); err != nil {
		t.Fatal(err)
	}
	if err := l.SetRoot(shas[0]); err != nil {
		t.Fatal(err)
	}
	want := localstore.Saved{ID: fixtureID(t), SHA: shas[0], Revision: 1, Status: localstore.Verified, Source: "recovered"}
	if err := st.Record(want); err != nil {
		t.Fatal(err)
	}
	before := fileContents(t, l.Dir)
	fetchFrom(t, registryHolding(t, fixtureID(t)), answer)
	rep := plainReporter(t)
	if err := runDeployment([]string{"fetch", "--ssh", "admin@203.0.113.9"}, noTerminalStdin(), &bytes.Buffer{}); err != nil {
		t.Fatal(problemText(err))
	}
	if h, _ := l.Head(); h != shas[0] {
		t.Errorf("the fetch moved the head to %s", h)
	}
	if got, _ := l.Files(); !got.Equal(files[0]) {
		t.Error("the fetch changed the system copy's files")
	}
	if rec, _, _ := st.Load(fixtureID(t)); rec != want {
		t.Errorf("record %+v, want %+v", rec, want)
	}
	for _, name := range []string{"paisans-stack.yaml", "secrets.enc.yaml", ".sops.yaml", "saved.json", ".paisans/config-head", ".paisans/config-root"} {
		if fileContents(t, l.Dir)[name] != before[name] {
			t.Errorf("the fetch changed %s", name)
		}
	}
	if rev, sha, _ := l.Seen(); rev != 2 || sha != shas[1] {
		t.Errorf("seen %d %s, want the host's revision 2", rev, sha)
	}
	out := rep.String()
	for _, w := range []string{"keep the system copy at " + shortSHA(shas[0]), "deployment pull moves the system copy to it", "1 recorded into a system copy here"} {
		if !strings.Contains(out, w) {
			t.Errorf("the output lacks %q:\n%s", w, out)
		}
	}
}

// A system copy with an anchor and no head, as a run that recorded a root
// and stopped leaves, is checked out by a fetch: the host's newest trusted
// version becomes its files and head, recorded as verified.
func TestFetchChecksOutACopyWithNoHead(t *testing.T) {
	st := ownStore(t)
	shas, answer, files := storedHistory(t, 2, 0)
	l, _ := st.Local(fixtureID(t))
	if err := l.SetRoot(shas[0]); err != nil {
		t.Fatal(err)
	}
	fetchFrom(t, registryHolding(t, fixtureID(t)), answer)
	if err := runDeployment([]string{"fetch", "--ssh", "admin@203.0.113.9"}, noTerminalStdin(), &bytes.Buffer{}); err != nil {
		t.Fatal(problemText(err))
	}
	rec, _, _ := st.Load(fixtureID(t))
	if rec.SHA != shas[1] || rec.Revision != 2 || rec.Status != localstore.Verified || rec.Source != "fetched from admin@203.0.113.9:22" || rec.Domain != "example.org" {
		t.Errorf("record %+v", rec)
	}
	if h, _ := l.Head(); h != shas[1] {
		t.Errorf("head %s", h)
	}
	if got, _ := l.Files(); !got.Equal(files[1]) {
		t.Error("the saved files are not the newest version")
	}
}

// A deployment this workstation never saved has nothing to trust what the
// host serves from: its newest version is set aside under incoming, never
// written as the copy's own files, and listed as unverified.
func TestFetchSetsAsideAnUnknownDeployment(t *testing.T) {
	st := ownStore(t)
	shas, answer, _ := storedHistory(t, 2, 0)
	fetchFrom(t, registryHolding(t, fixtureID(t)), answer)
	if err := runDeployment([]string{"fetch", "--ssh", "admin@203.0.113.9"}, noTerminalStdin(), &bytes.Buffer{}); err != nil {
		t.Fatal(problemText(err))
	}
	l, _ := st.Local(fixtureID(t))
	if l.HasFiles() {
		t.Fatal("unverified files were written as the saved copy's own")
	}
	if _, err := os.Stat(filepath.Join(l.IncomingFor(shas[1]), "paisans-stack.yaml")); err != nil {
		t.Fatalf("the newest version is not set aside: %v", err)
	}
	rec, _, _ := st.Load(fixtureID(t))
	if rec.Status != localstore.Unverified || rec.SHA != shas[1] {
		t.Errorf("record %+v", rec)
	}
	var out bytes.Buffer
	if err := runDeploymentList(nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), fixtureID(t)) || !strings.Contains(out.String(), "unverified") {
		t.Errorf("list:\n%s", out.String())
	}
}

// A fetch writes the system copy only: nothing in the folder it runs in,
// and what it sets aside stays in the system copy's own .paisans. It holds
// the system copy's lock while it writes, so one another command holds
// fetches nothing.
func TestFetchWritesOnlySystemCopy(t *testing.T) {
	st := ownStore(t)
	shas, answer, _ := storedHistory(t, 2, 0)
	id := fixtureID(t)
	fetchFrom(t, registryHolding(t, id), answer)
	folder := t.TempDir()
	t.Chdir(folder)

	release, err := st.Lock(id)
	if err != nil {
		t.Fatal(err)
	}
	err = runDeployment([]string{"fetch", "--ssh", "admin@203.0.113.9"}, noTerminalStdin(), &bytes.Buffer{})
	release()
	if err == nil || !strings.Contains(problemText(err), "not fetched") {
		t.Fatalf("a fetch beside a held lock: %v", problemText(err))
	}
	if _, ok, _ := st.Held(id); ok {
		t.Fatal("a fetch wrote the system copy another command holds")
	}

	if err := runDeployment([]string{"fetch", "--ssh", "admin@203.0.113.9"}, noTerminalStdin(), &bytes.Buffer{}); err != nil {
		t.Fatal(problemText(err))
	}
	if entries, _ := os.ReadDir(folder); len(entries) != 0 {
		t.Fatalf("the fetch wrote %d entries in the folder it ran in", len(entries))
	}
	l, _ := st.Local(id)
	set := filepath.Join(l.IncomingFor(shas[1]), "paisans-stack.yaml")
	if !strings.HasPrefix(set, filepath.Join(l.Dir, ".paisans")+string(filepath.Separator)) {
		t.Fatalf("set aside outside the system copy's .paisans: %s", set)
	}
	if _, err := os.Stat(set); err != nil {
		t.Fatalf("the newest version is not set aside: %v", err)
	}
}

// A registry that cannot be read fetches nothing; a fetch needs --ssh; an
// id a host's registry names that is not one is never made a path.
func TestFetchRefusals(t *testing.T) {
	st := ownStore(t)
	if err := runDeploymentFetch(nil); err == nil || !strings.Contains(problemText(err), "--ssh") {
		t.Fatalf("no --ssh: %v", err)
	}
	fetchFrom(t, "not json", "")
	if err := runDeploymentFetch([]string{"--ssh", "admin@203.0.113.9"}); err == nil || !strings.Contains(problemText(err), "registry") {
		t.Fatalf("bad registry: %v", err)
	}
	if _, err := st.Local("../../etc"); err == nil {
		t.Fatal("a path was taken for an id")
	}
}

// With nothing saved, list says how deployments come to be saved.
func TestListWithNothingSaved(t *testing.T) {
	ownStore(t)
	var out bytes.Buffer
	if err := runDeploymentList(nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No deployment is saved") {
		t.Errorf("list:\n%s", out.String())
	}
}

// mustBundle is a store answer's bundle.
func mustBundle(t *testing.T, answer string) []byte {
	t.Helper()
	b, _, err := configstore.Pull(&storeHost{name: "x", answer: answer}, deployment.Deployment{ID: fixtureID(t)}, false)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A fetch that could not record when its versions arrived says so, as
// every gather does.
func TestFetchWarnsWhenArrivalsAreNotRecorded(t *testing.T) {
	ownStore(t)
	_, answer, _ := storedHistory(t, 2, 0)
	fetchFrom(t, registryHolding(t, fixtureID(t)), answer)
	saved := fetchGather
	t.Cleanup(func() { fetchGather = saved })
	fetchGather = func(l configstore.Local, host string, tr apply.Transport, id string) (configstore.Gathered, error) {
		g, err := saved(l, host, tr, id)
		g.ArrivalErr = errors.New("arrivals: read-only file system")
		return g, err
	}
	rep := plainReporter(t)
	_ = runDeployment([]string{"fetch", "--ssh", "admin@203.0.113.9"}, noTerminalStdin(), &bytes.Buffer{})
	if !strings.Contains(rep.String(), "the arrival of these versions could not be recorded") {
		t.Errorf("no warning:\n%s", rep.String())
	}
}
