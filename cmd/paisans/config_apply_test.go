package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/configstore"
	"github.com/paisans-software/paisans-stack/internal/grant"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// applyStoreFixture is a workstation holding the fixture deployment, its
// admin alice's key replaced by one the test holds and the agent holds, and
// a store of versions of those files the sites hold.
type applyStoreFixture struct {
	l       configstore.Local
	cfgPath string
	shas    []string
	files   []configstore.Files
	runs    *[]string
}

// newApplyStore makes n versions of the fixture's files, each on the one
// before and each differing by a comment, and every fixture site answering
// with them (with nothing for n 0). The local files are the first
// version's, or the fixture's for n 0, and no base is recorded.
func newApplyStore(t *testing.T, n int) *applyStoreFixture {
	t.Helper()
	key, pub := testKey(t)
	s := newStore(t)
	secrets, err := os.ReadFile(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	base := configstore.Files{Paisans: []byte(fixtureAs(t, pub)), Secrets: secrets, SOPS: []byte("creation_rules: []\n")}
	f := &applyStoreFixture{runs: &[]string{}}
	for i := range n {
		v := base
		v.Paisans = []byte(string(base.Paisans) + strings.Repeat("# edited\n", i))
		var parents []string
		if i > 0 {
			parents = f.shas[i-1:]
		}
		sha, err := s.repo.Commit(v, parents, i+1, key, time.Unix(1_800_000_000, 0))
		if err != nil {
			t.Fatal(err)
		}
		f.shas, f.files = append(f.shas, sha), append(f.files, v)
	}
	answer := "__PAISANS_EMPTY__\n"
	if n > 0 {
		answer = s.answer(t)
	}
	saved, savedStore, savedKeys, savedSign := configSiteTransport, applyStore, agentKeys, configSignWith
	t.Cleanup(func() {
		configSiteTransport, applyStore, agentKeys, configSignWith = saved, savedStore, savedKeys, savedSign
	})
	allSitesUp(answer, f.runs)
	applyStore = gatherForApply
	agentKeys = func() []string { return []string{pub} }
	configSignWith = func(string) string { return key }
	f.l = configstore.Local{Dir: t.TempDir()}
	local := base
	if n > 0 {
		local = f.files[0]
	}
	if err := f.l.WriteFiles(local); err != nil {
		t.Fatal(err)
	}
	f.cfgPath = filepath.Join(f.l.Dir, "paisans.yaml")
	return f
}

// based records the first version as the base the local files started
// from, and the root.
func (f *applyStoreFixture) based(t *testing.T) {
	t.Helper()
	if err := f.l.SetRoot(f.shas[0]); err != nil {
		t.Fatal(err)
	}
	if err := f.l.SetHead(f.shas[0]); err != nil {
		t.Fatal(err)
	}
}

func (f *applyStoreFixture) pushed() bool {
	for _, r := range *f.runs {
		if strings.Contains(r, "fetch --no-prune") {
			return true
		}
	}
	return false
}

// applySite runs apply --site vm on the fixture, every host fake.
func (f *applyStoreFixture) applySite(t *testing.T, stdin io.Reader, extra ...string) error {
	t.Helper()
	var err error
	captureStdout(t, func() {
		err = runApply(append([]string{"--config", f.cfgPath, "--sudo=false", "--site", "vm"}, extra...), stdin, io.Discard)
	})
	return err
}

// grantedWith is grantedAlready with the configuration part c on every
// site's record.
func grantedWith(t *testing.T, f *applyStoreFixture, c *grant.ConfigPart) func(string) *grant.Record {
	t.Helper()
	held := grantedAlready(t, f.cfgPath, filepath.Join(f.l.Dir, "secrets.enc.yaml"))
	return func(site string) *grant.Record {
		rec := held(site)
		rec.Config = c
		return rec
	}
}

// Local files on an older base than the sites' newest version are refused
// before the review and before any host changes, naming config pull.
func TestApplyRefusesAStaleBase(t *testing.T) {
	f := newApplyStore(t, 2)
	f.based(t)
	log := loggingSSH(t, "")
	hosts := fakeGrants(t, grantedWith(t, f, nil))
	rec := recordConverge(t)
	err := f.applySite(t, noTerminalStdin(), "--execute")
	if err == nil || !strings.Contains(problemText(err), "not based on the sites' newest version") || !strings.Contains(problemText(err), "paisans config pull") {
		t.Fatalf("got %v\n%s", err, rec.Lines())
	}
	if claimed(logged(t, log)) || f.pushed() || hosts["vm"] != nil && hosts["vm"].writes != 0 {
		t.Errorf("changed a host: claimed %v, pushed %v", claimed(logged(t, log)), f.pushed())
	}
	if rec.Has("pending", "review what paisans.yaml grants") || rec.Index("step", "read what was granted") >= 0 {
		t.Errorf("reviewed before refusing:\n%s", rec.Lines())
	}
	if h, _ := f.l.Head(); h != f.shas[0] {
		t.Errorf("head moved to %s", h)
	}
}

// publishedBeforeClaim reports whether the version was published, and
// before the host was claimed, the first change. The fake host lets the
// apply go no further than its mesh, which is past both.
func publishedBeforeClaim(rec *ui.Recorder, what string) bool {
	publish, claim := rec.Index("done", what), rec.Index("done", "claim site")
	return publish >= 0 && claim > publish
}

// A workstation with no base and sites that hold nothing, every deployment
// from before the store, publishes the first version as the root before the
// first change, and goes on to apply it.
func TestApplyWithNoBasePublishesTheRoot(t *testing.T) {
	f := newApplyStore(t, 0)
	log := loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	head, _ := f.l.Head()
	root, _ := f.l.Root()
	if head == "" || root != head || !f.pushed() || !claimed(logged(t, log)) {
		t.Fatalf("head %q, root %q, pushed %v, claimed %v\n%s", head, root, f.pushed(), claimed(logged(t, log)), rec.Lines())
	}
	if !publishedBeforeClaim(rec, "publish revision 1, the first version") {
		t.Errorf("not published before the claim:\n%s", rec.Lines())
	}
}

// Edited local files on the sites' head are published as the next
// revision before the first change.
func TestApplyPublishesTheNextRevisionFirst(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	edited := f.files[0]
	edited.Paisans = append(append([]byte{}, edited.Paisans...), "# next\n"...)
	if err := f.l.WriteFiles(edited); err != nil {
		t.Fatal(err)
	}
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, &grant.ConfigPart{Hash: f.shas[0], Revision: 1}))
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	head, _ := f.l.Head()
	if head == f.shas[0] || !f.pushed() || !publishedBeforeClaim(rec, "publish revision 2, a new version on "+f.shas[0][:12]) {
		t.Errorf("head %s, pushed %v\n%s", head, f.pushed(), rec.Lines())
	}
	if b, _ := os.ReadFile(filepath.Join(f.l.Dir, ".paisans", "config-seen")); string(b) != "2 "+head+"\n" {
		t.Errorf("seen %q", b)
	}
}

// Local files equal to the sites' head publish nothing new: the head is
// what is applied.
func TestApplyOnTheHeadPublishesNothingNew(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	if h, _ := f.l.Head(); h != f.shas[0] || !publishedBeforeClaim(rec, "publish revision 1, "+f.shas[0][:12]) {
		t.Errorf("head %s\n%s", h, rec.Lines())
	}
}

// What a host's grant record holds once its apply has succeeded: what was
// granted and the version applied, the version as a step of apply without
// --site has it from the run.
func TestTheRecordHoldsTheVersionApplied(t *testing.T) {
	cfg := fixture(t)
	hosts := fakeGrants(t, func(string) *grant.Record { return nil })
	g, _ := grant.For(cfg, nil, "home-a")
	s, err := reviewSite(cfg.Deployment(), "home-a", g, grant.PrepareScope, nil)
	if err != nil {
		t.Fatal(err)
	}
	v := &appliedVersion{sha: strings.Repeat("d", 40), revision: 4}
	s.version = v
	if err := recordGrant(ui.Discard, s, cfg.Deployment(), ""); err != nil {
		t.Fatal(err)
	}
	got := hosts["home-a"].record(t)
	if got.Config == nil || got.Config.Hash != v.sha || got.Config.Revision != 4 || !got.Config.AppliedAt.Equal(time.Date(2026, 10, 11, 23, 10, 4, 0, time.UTC)) || got.Keys == nil {
		t.Errorf("record %+v", got.Config)
	}

	saved := reviewedByRun
	t.Cleanup(func() { reviewedByRun = saved })
	reviewedByRun = &runReview{approved: map[string]grant.Review{"home-a": grant.Compare("home-a", g, got, grant.PrepareScope)}, version: v}
	step, _, err := stepReview(ui.Discard, nil, nil, cfg, "home-a", g, grant.PrepareScope, nil, true, "x")
	if err != nil || step.version != v {
		t.Errorf("the step does not record the run's version: %v %+v", err, step)
	}
}

// A host whose record holds a newer revision than the one being applied is
// refused, naming the host and both revisions and hashes, unless the
// operator types rollback; then it goes on.
func TestApplyRollbackOnAHostAsks(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	newer := strings.Repeat("c", 40)
	part := &grant.ConfigPart{Hash: newer, Revision: 5, AppliedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}

	log := loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, part))
	err := f.applySite(t, noTerminalStdin(), "--execute")
	text := problemText(err)
	for _, want := range []string{"vm last applied revision 5 (" + newer + ")", "revision 1, " + f.shas[0][:12], "rollback"} {
		if err == nil || !strings.Contains(text, want) {
			t.Errorf("refusal lacks %q: %v", want, text)
		}
	}
	if claimed(logged(t, log)) || f.pushed() {
		t.Errorf("changed a host: claimed %v, pushed %v", claimed(logged(t, log)), f.pushed())
	}

	log = loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, part))
	var out bytes.Buffer
	captureStdout(t, func() {
		_ = runApply([]string{"--config", f.cfgPath, "--sudo=false", "--site", "vm", "--execute"}, answering(t, "rollback\n"), &out)
	})
	if !strings.Contains(out.String(), "Type rollback to go on: ") || !claimed(logged(t, log)) || !f.pushed() {
		t.Errorf("not asked, or not applied: %q", out.String())
	}
}

// A dry run gathers without asking a site to sync, publishes nothing,
// remembers nothing, and reports the rollback --execute would ask for.
func TestApplyDryRunChangesNoStore(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	edited := f.files[0]
	edited.Paisans = append(append([]byte{}, edited.Paisans...), "# next\n"...)
	if err := f.l.WriteFiles(edited); err != nil {
		t.Fatal(err)
	}
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, &grant.ConfigPart{Hash: strings.Repeat("c", 40), Revision: 5}))
	rec := recordConverge(t)
	if err := f.applySite(t, noTerminalStdin()); err != nil {
		t.Fatalf("%v\n%s", problemText(err), rec.Lines())
	}
	if synced(*f.runs) || f.pushed() {
		t.Errorf("a dry run changed a site: %q", *f.runs)
	}
	if h, _ := f.l.Head(); h != f.shas[0] {
		t.Errorf("head moved to %s", h)
	}
	if _, err := os.Stat(filepath.Join(f.l.Dir, ".paisans", "config-seen")); !os.IsNotExist(err) {
		t.Errorf("a dry run remembered a revision: %v", err)
	}
	if !rec.Has("pending", "publish revision 2") || !strings.Contains(rec.Lines(), "--execute asks for rollback") {
		t.Errorf("the dry run does not say what --execute does:\n%s", rec.Lines())
	}
}

// apply without --site gathers once before its review and refuses a stale
// base there; once approved it checks every host's record before its first
// step, and publishes once.
func TestConvergeChecksTheStoreOnceBeforeItsSteps(t *testing.T) {
	f := newApplyStore(t, 2)
	f.based(t)
	ran := fakeConverge(t, nil)
	fakeRunGrants(t)
	rec := recordConverge(t)
	converge := func(stdin io.Reader) error {
		var err error
		captureStdout(t, func() {
			err = runApply([]string{"--config", f.cfgPath, "--sudo=false", "--execute"}, stdin, io.Discard)
		})
		return err
	}
	err := converge(noTerminalStdin())
	if err == nil || !strings.Contains(problemText(err), "not based on the sites' newest version") || !strings.Contains(problemText(err), "paisans config pull") || len(*ran) != 0 || rec.Index("section", "review") >= 0 {
		t.Fatalf("stale: %v, ran %q\n%s", err, *ran, rec.Lines())
	}

	// On the sites' head, a host with a newer record stops the run before
	// its first step.
	if err := f.l.WriteFiles(f.files[1]); err != nil {
		t.Fatal(err)
	}
	if err := f.l.SetHead(f.shas[1]); err != nil {
		t.Fatal(err)
	}
	saved := convergeRecord
	t.Cleanup(func() { convergeRecord = saved })
	convergeRecord = func(cfg *config.Config, site string, g grant.Grant, sudo bool) (grant.Record, error) {
		rec, err := saved(cfg, site, g, sudo)
		if site == "vm" {
			rec.Config = &grant.ConfigPart{Hash: strings.Repeat("c", 40), Revision: 9}
		}
		return rec, err
	}
	rec = recordConverge(t)
	err = converge(noTerminalStdin())
	if err == nil || !strings.Contains(problemText(err), "vm last applied revision 9") || len(*ran) != 0 || f.pushed() {
		t.Fatalf("witness: %v, ran %q, pushed %v\n%s", err, *ran, f.pushed(), rec.Lines())
	}

	convergeRecord = func(cfg *config.Config, site string, g grant.Grant, sudo bool) (grant.Record, error) {
		return saved(cfg, site, g, sudo)
	}
	rec = recordConverge(t)
	if err := converge(noTerminalStdin()); err != nil {
		t.Fatalf("%v\n%s", problemText(err), rec.Lines())
	}
	if n := strings.Count(rec.Lines(), "done: publish revision 2"); n != 1 || !f.pushed() || len(*ran) == 0 {
		t.Errorf("published %d times, pushed %v, ran %q\n%s", n, f.pushed(), *ran, rec.Lines())
	}
	review, publish, prepare := rec.Index("section", "review"), rec.Index("done", "publish revision 2"), rec.Index("section", "prepare")
	if !(review >= 0 && review < publish && publish < prepare) {
		t.Errorf("order: review %d, publish %d, prepare %d\n%s", review, publish, prepare, rec.Lines())
	}
}
