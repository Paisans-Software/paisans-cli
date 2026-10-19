package main

import (
	"bytes"
	"encoding/base64"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/pocketid-go"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/grant"
	"github.com/paisans-software/paisans-cli/internal/ui"
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
	answer  string
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
	base := snapshotted(configstore.Files{Paisans: []byte(fixtureAs(t, pub)), Secrets: secrets, SOPS: []byte("creation_rules: []\n")})
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
	f.answer = answer
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
	f.cfgPath = filepath.Join(f.l.Dir, "paisans-stack.yaml")
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

// applySite runs deployment apply --site vm on the fixture, every host fake.
func (f *applyStoreFixture) applySite(t *testing.T, stdin io.Reader, extra ...string) error {
	t.Helper()
	var err error
	captureStdout(t, func() {
		err = runApply(append([]string{"--config", f.cfgPath, "--site", "vm"}, extra...), stdin, io.Discard)
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
// before the review and before any host changes, naming deployment pull.
func TestApplyRefusesAStaleBase(t *testing.T) {
	f := newApplyStore(t, 2)
	f.based(t)
	log := loggingSSH(t, "")
	hosts := fakeGrants(t, grantedWith(t, f, nil))
	rec := recordConverge(t)
	err := f.applySite(t, noTerminalStdin(), "--execute")
	if err == nil || !strings.Contains(problemText(err), "not based on the sites' newest version") || !strings.Contains(problemText(err), "paisans deployment pull") {
		t.Fatalf("got %v\n%s", err, rec.Lines())
	}
	if checked(logged(t, log)) || f.pushed() || hosts["vm"] != nil && hosts["vm"].writes != 0 {
		t.Errorf("changed a host: checked %v, pushed %v", checked(logged(t, log)), f.pushed())
	}
	if rec.Has("pending", "review what paisans-stack.yaml grants") || rec.Index("step", "read what was granted") >= 0 {
		t.Errorf("reviewed before refusing:\n%s", rec.Lines())
	}
	if h, _ := f.l.Head(); h != f.shas[0] {
		t.Errorf("head moved to %s", h)
	}
	if rev, _, _ := f.l.Seen(); rev != 0 {
		t.Errorf("a refused apply raised seen to %d", rev)
	}
}

// publishedBeforeTheCheck reports whether the version was published, and
// before the host's admission was checked, ahead of its first change. The
// fake host lets the apply go no further than its mesh, which is past both.
func publishedBeforeTheCheck(rec *ui.Recorder, what string) bool {
	publish, check := rec.Index("done", what), rec.Index("done", "check site's admission")
	return publish >= 0 && check > publish
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
	if head == "" || root != head || !f.pushed() || !checked(logged(t, log)) {
		t.Fatalf("head %q, root %q, pushed %v, checked %v\n%s", head, root, f.pushed(), checked(logged(t, log)), rec.Lines())
	}
	if !publishedBeforeTheCheck(rec, "publish revision 1") {
		t.Errorf("not published before the admission check:\n%s", rec.Lines())
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
	if head == f.shas[0] || !f.pushed() || !publishedBeforeTheCheck(rec, "publish revision 2") {
		t.Errorf("head %s, pushed %v\n%s", head, f.pushed(), rec.Lines())
	}
	if b, _ := os.ReadFile(filepath.Join(f.l.Dir, ".paisans", "config-seen")); string(b) != "2 "+head+"\n" {
		t.Errorf("seen %q", b)
	}
}

// A finished secrets rotate leaves the secrets file changed on the
// workstation and the base where it was: its scoped applies neither gather
// nor publish. The next apply takes that file as an edit on the sites' head
// and publishes it as the next revision, as it does any other edit.
func TestApplyPublishesWhatSecretsRotateLeft(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	rotated := f.files[0]
	rotated.Secrets = []byte(strings.Replace(string(rotated.Secrets), "fixture-not-a-secret-standby", "fixture-not-a-secret-standby-rotated", 1))
	if bytes.Equal(rotated.Secrets, f.files[0].Secrets) {
		t.Fatal("the fixture's standby password moved; the test no longer edits the secrets")
	}
	if err := f.l.WriteFiles(rotated); err != nil {
		t.Fatal(err)
	}
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, &grant.ConfigPart{Hash: f.shas[0], Revision: 1}))
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	head, _ := f.l.Head()
	if head == f.shas[0] || !f.pushed() || !publishedBeforeTheCheck(rec, "publish revision 2") {
		t.Errorf("head %s, pushed %v\n%s", head, f.pushed(), rec.Lines())
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
	if h, _ := f.l.Head(); h != f.shas[0] || !publishedBeforeTheCheck(rec, "publish revision 1, "+f.shas[0][:12]) {
		t.Errorf("head %s\n%s", h, rec.Lines())
	}
}

// What a host's grant record holds once its apply has succeeded: what was
// granted and the version applied.
func TestTheRecordHoldsTheVersionApplied(t *testing.T) {
	cfg := fixture(t)
	hosts := fakeGrants(t, func(string) *grant.Record { return nil })
	g, _ := grant.For(cfg, nil, "home-a")
	s, err := reviewSite(cfg.Deployment(), "home-a", g, grant.ApplyScope(), nil)
	if err != nil {
		t.Fatal(err)
	}
	v := &appliedVersion{sha: strings.Repeat("d", 40), revision: 4}
	s.version = v
	if err := recordGrant(ui.Discard, s, cfg.Deployment(), ""); err != nil {
		t.Fatal(err)
	}
	got := hosts["home-a"].record(t)
	if got.Config == nil || got.Config.Hash != v.sha || got.Config.Revision != 4 || !got.Config.AppliedAt.Equal(time.Date(2026, 10, 11, 23, 10, 4, 0, time.UTC)) || got.Images == nil {
		t.Errorf("record %+v", got.Config)
	}
}

// site prepare, as a step of apply without --site, records its keys and
// no configuration version: it applies none.
func TestAPrepareStepRecordsNoVersion(t *testing.T) {
	cfg := fixture(t)
	loggingSSH(t, "")
	hosts := fakeGrants(t, func(string) *grant.Record { return nil })
	g, _ := grant.For(cfg, nil, "home-a")
	saved := reviewedByRun
	t.Cleanup(func() { reviewedByRun = saved })
	reviewedByRun = &runReview{
		approved: map[string]grant.Review{"home-a": grant.Compare("home-a", g, grant.Record{}, grant.PrepareScope)},
		version:  &appliedVersion{sha: strings.Repeat("d", 40), revision: 4},
	}
	if rec, err := prepare(t, noTerminalStdin(), io.Discard, "--execute"); err != nil {
		t.Fatalf("%v\n%s", err, rec.Lines())
	}
	got := hosts["home-a"].record(t)
	if got.Keys == nil || got.Config != nil {
		t.Errorf("keys %+v, config %+v", got.Keys, got.Config)
	}
}

// A host whose record holds another version at the revision being applied
// is refused like one holding a newer revision, unless the operator types
// rollback; a version not yet published differs from any a record holds.
func TestApplyAnotherVersionAtTheSameRevisionAsks(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	other := strings.Repeat("e", 40)
	part := &grant.ConfigPart{Hash: other, Revision: 1}
	log := loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, part))
	err := f.applySite(t, noTerminalStdin(), "--execute")
	if text := problemText(err); err == nil || !strings.Contains(text, "vm holds revision 1 ("+other+"), another version at the same revision") || !strings.Contains(text, "rollback") {
		t.Fatalf("got %v", text)
	}
	if checked(logged(t, log)) || f.pushed() {
		t.Errorf("changed a host: checked %v, pushed %v", checked(logged(t, log)), f.pushed())
	}

	log = loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, part))
	var out bytes.Buffer
	captureStdout(t, func() {
		_ = runApply([]string{"--config", f.cfgPath, "--site", "vm", "--execute"}, answering(t, "rollback\n"), &out)
	})
	if !strings.Contains(out.String(), "Type 'rollback' (no quotes) to proceed: ") || !checked(logged(t, log)) {
		t.Errorf("not asked, or not applied: %q", out.String())
	}

	// The same version is not another one.
	log = loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, &grant.ConfigPart{Hash: f.shas[0], Revision: 1}))
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	if !checked(logged(t, log)) {
		t.Error("the version a host holds was refused")
	}

	// A new version at revision 2 is another version than any revision 2
	// a host holds.
	edited := f.files[0]
	edited.Paisans = append(append([]byte{}, edited.Paisans...), "# next\n"...)
	if err := f.l.WriteFiles(edited); err != nil {
		t.Fatal(err)
	}
	log = loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, &grant.ConfigPart{Hash: other, Revision: 2}))
	err = f.applySite(t, noTerminalStdin(), "--execute")
	if text := problemText(err); err == nil || !strings.Contains(text, "vm holds revision 2 ("+other+"), another version at the same revision") || checked(logged(t, log)) {
		t.Errorf("a new version over another revision 2: %v", text)
	}
}

// A version no site takes stops the apply before the first change; one
// some sites took goes on, naming those that did not.
func TestApplyStopsWhenNoSiteTakesTheVersion(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	refuse := func(all bool) {
		configSiteTransport = func(name string, _ config.Site) apply.Transport {
			return &storeHost{name: name, answer: f.answer, runs: f.runs, noPush: all || name == "watch"}
		}
	}
	refuse(true)
	log := loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	err := f.applySite(t, noTerminalStdin(), "--execute")
	if err == nil || !strings.Contains(problemText(err), "no site took the version") || checked(logged(t, log)) {
		t.Fatalf("got %v, checked %v", problemText(err), checked(logged(t, log)))
	}
	if strings.Contains(problemText(err), "pushes it again") {
		t.Errorf("promises a push: %s", problemText(err))
	}

	refuse(false)
	log = loggingSSH(t, "")
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	if !checked(logged(t, log)) || !rec.Has("warn", "watch did not take the version") {
		t.Errorf("checked %v\n%s", checked(logged(t, log)), rec.Lines())
	}
}

// deployment apply --site on a deployment whose sites have no store yet refuses before
// the first change, naming site prepare for each site.
func TestApplySiteWithNoStoreNamesHostPrepare(t *testing.T) {
	f := newApplyStore(t, 0)
	none := func() bool { return true }
	configSiteTransport = func(name string, _ config.Site) apply.Transport {
		return &storeHost{name: name, answer: f.answer, runs: f.runs, noStore: none}
	}
	log := loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	err := f.applySite(t, noTerminalStdin(), "--execute")
	text := problemText(err)
	for _, site := range []string{"home-a", "home-b", "vm", "watch"} {
		if err == nil || !strings.Contains(text, "paisans site prepare --site "+site+" --execute") {
			t.Errorf("does not name site prepare for %s: %v", site, text)
		}
	}
	if checked(logged(t, log)) || f.pushed() {
		t.Errorf("changed a host: checked %v, pushed %v", checked(logged(t, log)), f.pushed())
	}
}

// apply without --site on a deployment whose sites have no store yet
// publishes once its site prepare steps have made them, before any other
// step, and goes on.
func TestConvergePublishesAfterHostPrepare(t *testing.T) {
	f := newApplyStore(t, 0)
	prepared := false
	configSiteTransport = func(name string, _ config.Site) apply.Transport {
		return &storeHost{name: name, answer: f.answer, runs: f.runs, noStore: func() bool { return !prepared }}
	}
	ran := fakeConverge(t, nil)
	saved := convergePrepare
	t.Cleanup(func() { convergePrepare = saved })
	convergePrepare = func(env prepareEnv, args []string) error {
		prepared = true
		return saved(env, args)
	}
	fakeRunGrants(t)
	rec := recordConverge(t)
	var err error
	captureStdout(t, func() {
		err = runApply([]string{"--config", f.cfgPath, "--execute"}, noTerminalStdin(), io.Discard)
	})
	if err != nil {
		t.Fatalf("%v\n%s", problemText(err), rec.Lines())
	}
	head, _ := f.l.Head()
	if head == "" || !f.pushed() {
		t.Fatalf("head %q, pushed %v", head, f.pushed())
	}
	prepare, publish := rec.Index("section", "prepare"), rec.Index("done", "publish revision 1")
	next := rec.Index("section", "install")
	if !(prepare >= 0 && prepare < publish && publish < next) {
		t.Errorf("order: prepare %d, publish %d, install %d\n%s", prepare, publish, next, rec.Lines())
	}
	if len(*ran) == 0 || !strings.HasPrefix((*ran)[0], "site prepare") {
		t.Errorf("ran %q", *ran)
	}
}

// The bytes apply loaded are the ones published: an edit made on disk
// while the operator was asked is not.
func TestApplyPublishesTheFilesItLoaded(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	read := f.files[0]
	read.Paisans = append(append([]byte{}, read.Paisans...), "# read\n"...)
	if err := f.l.WriteFiles(read); err != nil {
		t.Fatal(err)
	}
	cfg, paisans, err := config.LoadWithBytes(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded := configstore.Files{Paisans: paisans, Secrets: read.Secrets}
	later := read
	later.Paisans = append(append([]byte{}, read.Paisans...), "# edited while asked\n"...)
	if err := f.l.WriteFiles(later); err != nil {
		t.Fatal(err)
	}
	v, err := gatherForApply(ui.Discard, f.cfgPath, "", &loaded, cfg, true, answerer{}, noTerminalStdin(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := v.publish(ui.Discard, true)
	if err != nil {
		t.Fatal(err)
	}
	cache, _ := f.l.Cache()
	got, err := cache.Files(applied.sha)
	if err != nil || !got.Equal(read) || !applied.files.Equal(read) {
		t.Errorf("published %q, %v", got.Paisans[len(got.Paisans)-40:], err)
	}
}

// A step of apply without --site whose files are not what the run
// published refuses before it changes anything.
func TestAStepRefusesFilesTheRunDidNotPublish(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	log := loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	saved := reviewedByRun
	t.Cleanup(func() { reviewedByRun = saved })
	published := f.files[0]
	published.Paisans = append(append([]byte{}, published.Paisans...), "# published\n"...)
	reviewedByRun = &runReview{approved: map[string]grant.Review{}, version: &appliedVersion{sha: strings.Repeat("d", 40), revision: 2, files: published}}
	err := f.applySite(t, noTerminalStdin(), "--execute")
	if err == nil || !strings.Contains(problemText(err), "changed after the run published it") || checked(logged(t, log)) {
		t.Errorf("got %v, checked %v", problemText(err), checked(logged(t, log)))
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
	for _, want := range []string{"vm holds revision 5 (" + newer + "), applied 2026-10-01 00:00 UTC, newer than the one you are applying", "revision 1, " + f.shas[0][:12], "rollback"} {
		if err == nil || !strings.Contains(text, want) {
			t.Errorf("refusal lacks %q: %v", want, text)
		}
	}
	if checked(logged(t, log)) || f.pushed() {
		t.Errorf("changed a host: checked %v, pushed %v", checked(logged(t, log)), f.pushed())
	}

	log = loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, part))
	var out bytes.Buffer
	captureStdout(t, func() {
		_ = runApply([]string{"--config", f.cfgPath, "--site", "vm", "--execute"}, answering(t, "rollback\n"), &out)
	})
	if !strings.Contains(out.String(), "Type 'rollback' (no quotes) to proceed: ") || !checked(logged(t, log)) || !f.pushed() {
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
	if !rec.Has("pending", "publish revision 2") || !strings.Contains(rec.Lines(), "--execute will ask for rollback") {
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
			err = runApply([]string{"--config", f.cfgPath, "--execute"}, stdin, io.Discard)
		})
		return err
	}
	err := converge(noTerminalStdin())
	if err == nil || !strings.Contains(problemText(err), "not based on the sites' newest version") || !strings.Contains(problemText(err), "paisans deployment pull") || len(*ran) != 0 || rec.Index("section", "review") >= 0 {
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
	convergeRecord = func(cfg *config.Config, site string, g grant.Grant) (grant.Record, error) {
		rec, err := saved(cfg, site, g)
		if site == "vm" {
			rec.Config = &grant.ConfigPart{Hash: strings.Repeat("c", 40), Revision: 9}
		}
		return rec, err
	}
	rec = recordConverge(t)
	err = converge(noTerminalStdin())
	if err == nil || !strings.Contains(problemText(err), "vm holds revision 9") || len(*ran) != 0 || f.pushed() {
		t.Fatalf("witness: %v, ran %q, pushed %v\n%s", err, *ran, f.pushed(), rec.Lines())
	}

	convergeRecord = func(cfg *config.Config, site string, g grant.Grant) (grant.Record, error) {
		return saved(cfg, site, g)
	}
	rec = recordConverge(t)
	if err := converge(noTerminalStdin()); err != nil {
		t.Fatalf("%v\n%s", problemText(err), rec.Lines())
	}
	if n := strings.Count(rec.Lines(), "done: publish revision 2"); n != 1 || !f.pushed() || len(*ran) == 0 {
		t.Errorf("published %d times, pushed %v, ran %q\n%s", n, f.pushed(), *ran, rec.Lines())
	}
	review, prepare, publish, install := rec.Index("section", "review"), rec.Index("section", "prepare"), rec.Index("done", "publish revision 2"), rec.Index("section", "install")
	if !(review >= 0 && review < prepare && prepare < publish && publish < install) {
		t.Errorf("order: review %d, prepare %d, publish %d, install %d\n%s", review, prepare, publish, install, rec.Lines())
	}
}

// recordedAtPocketID is a workstation whose sites hold revision 1, published
// as apply publishes it, the store it was gathered into, the version, and the
// bytes an apply's client step wrote to secrets.enc.yaml after it, recording
// a client's credentials.
func recordedAtPocketID(t *testing.T) (*applyStoreFixture, storeVersion, *appliedVersion, []byte) {
	t.Helper()
	f := newApplyStore(t, 1)
	f.based(t)
	secretsPath := filepath.Join(f.l.Dir, "secrets.enc.yaml")
	cfg, paisans, err := config.LoadWithBytes(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	secretsBytes, err := os.ReadFile(secretsPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded := configstore.Files{Paisans: paisans, Secrets: secretsBytes}
	store, err := gatherForApply(ui.Discard, f.cfgPath, "", &loaded, cfg, true, answerer{}, noTerminalStdin(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := store.publish(ui.Discard, true)
	if err != nil || v1 == nil {
		t.Fatalf("%v %v", v1, err)
	}
	secrets, err := config.LoadSecrets(secretsPath)
	if err != nil {
		t.Fatal(err)
	}
	rec := &secretsRecorder{app: "talk", path: secretsPath, secrets: secrets}
	if err := rec.Record("c-1", "not-a-real-secret-0001"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rec.written, []byte("c-1")) {
		t.Fatalf("the recorder kept %q", rec.written)
	}
	return f, store, v1, rec.written
}

// headSecrets is the secrets.enc.yaml of the workstation's head.
func headSecrets(t *testing.T, f *applyStoreFixture) (string, []byte) {
	t.Helper()
	head, _ := f.l.Head()
	cache, err := f.l.Cache()
	if err != nil {
		t.Fatal(err)
	}
	files, err := cache.Files(head)
	if err != nil {
		t.Fatal(err)
	}
	return head, files.Secrets
}

// What an apply publishes after recording at Pocket ID is the bytes it
// wrote, not the file read again: another writer's edit made since is not
// published as the apply's, and the run's next step refuses it.
func TestAnApplyPublishesTheBytesItWroteNotTheFile(t *testing.T) {
	f, store, v1, written := recordedAtPocketID(t)
	secretsPath := filepath.Join(f.l.Dir, "secrets.enc.yaml")
	foreign := append(append([]byte{}, written...), "# another writer\n"...)
	if err := os.WriteFile(secretsPath, foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &siteReview{version: v1}
	if err := s.publishWritten(ui.Discard, written, store, &runIdentity{}); err != nil {
		t.Fatal(err)
	}
	head, held := headSecrets(t, f)
	if head == v1.sha || !bytes.Equal(held, written) || s.version.sha != head || s.version.revision != 2 {
		t.Fatalf("head %s holds the apply's bytes %v, version %s at %d", head, bytes.Equal(held, written), s.version.sha, s.version.revision)
	}

	log := loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	saved := reviewedByRun
	t.Cleanup(func() { reviewedByRun = saved })
	reviewedByRun = &runReview{approved: map[string]grant.Review{}, version: s.version}
	err := f.applySite(t, noTerminalStdin(), "--execute")
	if err == nil || !strings.Contains(problemText(err), "changed after the run published it") || checked(logged(t, log)) {
		t.Errorf("got %v, checked %v", problemText(err), checked(logged(t, log)))
	}
}

// A step of apply without --site that records at Pocket ID, the run's last
// one among them, publishes the next revision before its host records its
// version: the host records that revision, the run's version is it for any
// step after, and the run ends naming it.
func TestAStepThatRecordsAtPocketIDRecordsTheRevisionItPublished(t *testing.T) {
	f, store, v1, written := recordedAtPocketID(t)
	cfg := mustLoad(t, f.cfgPath)
	hosts := fakeGrants(t, func(string) *grant.Record { return nil })
	saved := reviewedByRun
	t.Cleanup(func() { reviewedByRun = saved })
	run := &runIdentity{}
	run.applied(v1.sha, v1.revision)
	reviewedByRun = &runReview{approved: map[string]grant.Review{}, version: v1, store: store, identity: run}
	g, _ := grant.For(cfg, nil, "watch")
	s, err := reviewSite(cfg.Deployment(), "watch", g, grant.ApplyScope(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.version = reviewedByRun.version
	if err := s.publishWritten(ui.Discard, written, reviewedByRun.store, reviewedByRun.identity); err != nil {
		t.Fatal(err)
	}
	if err := recordGrant(ui.Discard, s, cfg.Deployment(), ""); err != nil {
		t.Fatal(err)
	}
	head, held := headSecrets(t, f)
	got := hosts["watch"].record(t)
	if got.Config == nil || got.Config.Hash != head || got.Config.Revision != 2 || !bytes.Equal(held, written) {
		t.Errorf("the host records %+v, the head is %s", got.Config, head)
	}
	if v := reviewedByRun.version; v.sha != head || v.revision != 2 || run.sha != head || run.revision != 2 {
		t.Errorf("the run's version %s at %d, its identity %s %d", v.sha, v.revision, run.sha, run.revision)
	}
}

// deployment apply --site alone that records at Pocket ID leaves the store current:
// the head holds what it wrote, and the host records it. An apply that
// wrote nothing publishes nothing.
func TestApplySiteThatRecordsAtPocketIDLeavesTheStoreCurrent(t *testing.T) {
	f, store, v1, written := recordedAtPocketID(t)
	s := &siteReview{version: v1}
	if err := s.publishWritten(ui.Discard, nil, store, &runIdentity{}); err != nil || s.version != v1 {
		t.Fatalf("nothing written published %s, %v", s.version.sha, err)
	}
	if head, _ := f.l.Head(); head != v1.sha {
		t.Fatalf("nothing written moved the head to %s", head)
	}
	identity := &runIdentity{}
	if err := s.publishWritten(ui.Discard, written, store, identity); err != nil {
		t.Fatal(err)
	}
	head, held := headSecrets(t, f)
	if head == v1.sha || !bytes.Equal(held, written) || s.version.sha != head || identity.sha != head || !f.pushed() {
		t.Errorf("head %s, version %s, identity %s", head, s.version.sha, identity.sha)
	}
}

// pocketIDHost is every host as ssh reaches it in process: files written are
// read back, and answer gives the reply to a command it knows, before the
// quiet host's. It returns every command run, decoded.
func pocketIDHost(t *testing.T, files map[string]string, answer func(host, cmd string) (string, bool)) *[]string {
	t.Helper()
	answerQuietly(t, "", "")
	var mu sync.Mutex
	var cmds []string
	writeTarget := regexp.MustCompile(`mv -f "\$tmp" '([^']+)'`)
	readTarget := regexp.MustCompile(`^if \[ -f '([^']+)' \]; then cat`)
	t.Cleanup(apply.SetRunSSH(func(c *exec.Cmd) error {
		args := c.Args
		b := args[len(args)-1]
		if _, after, found := strings.Cut(b, "printf %s "); found {
			b = after
		}
		b, _, _ = strings.Cut(b, " |")
		decoded, err := base64.StdEncoding.DecodeString(b)
		cmd := strings.TrimRight(string(decoded), "\n")
		if strings.HasPrefix(cmd, "export DOCKER_HOST=") {
			if _, after, found := strings.Cut(cmd, "\n"); found {
				cmd = after
			}
		}
		mu.Lock()
		cmds = append(cmds, cmd)
		mu.Unlock()
		reply := func(out string) error {
			io.WriteString(c.Stdout, out)
			return nil
		}
		if err == nil {
			if m := writeTarget.FindStringSubmatch(cmd); m != nil && c.Stdin != nil {
				in, _ := io.ReadAll(c.Stdin)
				mu.Lock()
				files[m[1]] = string(in)
				mu.Unlock()
				return nil
			}
			if m := readTarget.FindStringSubmatch(cmd); m != nil {
				mu.Lock()
				content, ok := files[m[1]]
				mu.Unlock()
				if ok {
					return reply(content)
				}
			}
			host := ""
			for _, a := range args {
				if strings.Contains(a, "@") && !strings.Contains(a, " ") {
					host = a
				}
			}
			if out, ok := answer(host, cmd); ok {
				return reply(out)
			}
		}
		out, exit, ok := quietAnswer(args, "", "")
		if !ok {
			return c.Run()
		}
		io.WriteString(c.Stdout, out)
		if exit != 0 {
			return sshExit(exit)
		}
		return nil
	}))
	return &cmds
}

// runningSite answers what an apply asks a site whose stacks come up:
// names resolve, the mesh comes up when started, containers run healthy,
// etcd and Patroni answer, and Pocket ID is active on home-a.
func runningSite() func(host, cmd string) (string, bool) {
	var mu sync.Mutex
	started := false
	return func(host, cmd string) (string, bool) {
		mu.Lock()
		defer mu.Unlock()
		return runningAnswer(host, cmd, &started)
	}
}

// runningAnswer is runningSite's answer to cmd on host. The mesh reads as up
// only once, right after it is started, so each pass of an apply starts it.
func runningAnswer(host, cmd string, started *bool) (string, bool) {
	switch {
	case strings.HasSuffix(cmd, "wg-psns-f2a9' up"):
		*started = true
		return "", true
	case strings.Contains(cmd, "member list -w json"):
		return `{"members":[{"ID":1,"name":"home-a","peerURLs":["http://10.44.0.1:2380"],"clientURLs":["http://10.44.0.1:2379"]},{"ID":2,"name":"home-b","peerURLs":["http://10.44.0.2:2380"],"clientURLs":["http://10.44.0.2:2379"]},{"ID":3,"name":"vm","peerURLs":["http://10.44.0.3:2380"],"clientURLs":["http://10.44.0.3:2379"]}]}`, true
	case strings.Contains(cmd, "/healthz") && strings.Contains(cmd, "echo absent"):
		if strings.Contains(host, "home-a") {
			return "active\n", true
		}
		return "absent\n", true
	case strings.Contains(cmd, ":8008/cluster"):
		return `{"members":[{"name":"home-a","role":"leader","state":"running"}],"scope":"fixture"}`, true
	case strings.Contains(cmd, " ps --all --format json"):
		return `{"Service":"app","Name":"app-1","State":"running","Health":""}` + "\n", true
	case strings.Contains(cmd, "ps --status running"):
		return "c0ffee\n", true
	case strings.Contains(cmd, "getent ahosts"):
		return "", true
	case strings.HasPrefix(cmd, "ip -br addr show dev psns-f2a9"):
		if !*started {
			return "", true
		}
		*started = false
		return "psns-f2a9 UNKNOWN 10.44.0.1/24\n7: psns-f2a9: <POINTOPOINT,NOARP,UP,LOWER_UP> mtu 1420 qdisc noqueue state UNKNOWN\n", true
	}
	return "", false
}

// deployment apply --site on the site that runs Pocket ID, with no client yet for an
// app there, creates the client, publishes the bytes it wrote as the next
// revision, and only then records the grant: the host's record names that
// revision, which holds the client's ID.
func TestApplySiteRecordsTheRevisionItPublishedAtPocketID(t *testing.T) {
	f := newApplyStore(t, 0)
	secretsPath := filepath.Join(f.l.Dir, "secrets.enc.yaml")
	secrets, err := config.LoadSecrets(secretsPath)
	if err != nil {
		t.Fatal(err)
	}
	delete(secrets.OIDCClients, "talk")
	if err := config.WriteSecrets(secretsPath, secrets, nil); err != nil {
		t.Fatal(err)
	}
	fake := newIDPFake()
	saved := oidcTransport
	oidcTransport = func(tr apply.SSHTransport) pocketid.Transport {
		fake.destination = tr.Describe()
		return fake
	}
	t.Cleanup(func() { oidcTransport = saved })
	t.Setenv("PAISANS_ALLOW_YES", "1")
	// home-a's etcd member was founded: its record is on the host, as
	// rendered.
	initial, err := os.ReadFile(filepath.Join("..", "..", "internal", "render", "testdata", "golden", "home-a", "srv", "paisans", "f2a9", "infra", "etcd-initial"))
	if err != nil {
		t.Fatal(err)
	}
	founded := map[string]string{"/srv/paisans/f2a9/infra/etcd-initial": string(initial)}
	cmds := pocketIDHost(t, founded, runningSite())
	hosts := fakeGrants(t, func(string) *grant.Record { return nil })
	rec := recordConverge(t)
	captureStdout(t, func() {
		err = runApply([]string{"--config", f.cfgPath, "--site", "home-a", "--execute", "--yes"}, noTerminalStdin(), io.Discard)
	})
	if err != nil {
		t.Fatalf("%v: %v\n%s\ncommands:\n%s", problemText(err), err, rec.Lines(), strings.Join(*cmds, "\n----\n"))
	}
	head, held := headSecrets(t, f)
	if !bytes.Contains(held, []byte("c-1")) {
		t.Errorf("the head's secrets lack the client ID c-1")
	}
	if hosts["home-a"] == nil {
		t.Fatalf("home-a has no record\n%s", rec.Lines())
	}
	got := hosts["home-a"].record(t)
	if got.Config == nil || got.Config.Revision != 2 || got.Config.Hash != head {
		t.Errorf("home-a records %+v, the head is %s", got.Config, head)
	}
}
