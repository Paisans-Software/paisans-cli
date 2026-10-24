package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/pocketid-go"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/draft"
	"github.com/paisans-software/paisans-cli/internal/grant"
	"github.com/paisans-software/paisans-cli/internal/localstore"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// applyStoreFixture is the fixture deployment, its admin alice's key
// replaced by one the test holds and the agent holds: a draft of it in a
// folder of its own, its system copy, and a store of versions of those
// files the sites hold.
type applyStoreFixture struct {
	// sys is the system copy. For a fixture of no version it is where the
	// system copy goes, and nothing is there.
	sys configstore.Local
	// dir is the draft's folder, and cfgPath its paisans-stack.yaml.
	dir     string
	cfgPath string
	shas    []string
	files   []configstore.Files
	runs    *[]string
	answer  string
	// marker is the draft's "drafted from" line once based, which write
	// keeps.
	marker *draft.Marker
	// store is what the sites hold, and key the private key that signs
	// its versions.
	store store
	key   string
}

// newApplyStore makes n versions of the fixture's files, each on the one
// before and each differing by a comment, and every fixture site answering
// with them (with nothing for n 0). The draft is the first version's files,
// or the fixture's for n 0, with no "drafted from" line. The system copy
// is there with an empty store and nothing recorded, or, for n 0, not
// there at all: a new deployment.
func newApplyStore(t *testing.T, n int) *applyStoreFixture {
	t.Helper()
	key, pub := testKey(t)
	s := newStore(t)
	secrets, err := os.ReadFile(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	base := snapshotted(configstore.Files{Paisans: []byte(fixtureAs(t, pub)), Secrets: secrets, SOPS: []byte("creation_rules: []\n")})
	f := &applyStoreFixture{runs: &[]string{}, store: s, key: key}
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
	if n > 0 {
		f.sys = withSystemCopy(t, fixtureDeploymentID)
	} else {
		withDataDir(t)
		st, err := openStore()
		if err != nil {
			t.Fatal(err)
		}
		f.sys = configstore.Local{Dir: filepath.Join(st.Base, "deployments", fixtureDeploymentID)}
	}
	f.dir = t.TempDir()
	f.cfgPath = filepath.Join(f.dir, config.FileName)
	local := base
	if n > 0 {
		local = f.files[0]
	}
	if err := f.write(local); err != nil {
		t.Fatal(err)
	}
	return f
}

// based makes the first version the system copy's: its files, its head
// and its root, as a deploy of it leaves them. The draft is that version's,
// drafted from it.
func (f *applyStoreFixture) based(t *testing.T) {
	t.Helper()
	if err := f.sys.SetRoot(f.shas[0]); err != nil {
		t.Fatal(err)
	}
	if err := f.sys.SetHead(f.shas[0]); err != nil {
		t.Fatal(err)
	}
	if err := f.sys.WriteFiles(f.files[0]); err != nil {
		t.Fatal(err)
	}
	f.marker = &draft.Marker{Revision: 1, SHA: f.shas[0]}
	if err := f.write(f.files[0]); err != nil {
		t.Fatal(err)
	}
}

// write writes files as the draft, with its "drafted from" line when it
// has one.
func (f *applyStoreFixture) write(files configstore.Files) error {
	if f.marker != nil {
		return draft.Draft{Dir: f.dir}.Write(files, *f.marker)
	}
	return configstore.WriteFilesTo(f.dir, files)
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
	held := grantedAlready(t, f.cfgPath, filepath.Join(f.dir, "secrets.enc.yaml"))
	return func(site string) *grant.Record {
		rec := held(site)
		rec.Config = c
		return rec
	}
}

// publishedBeforeTheCheck reports whether the version was published, and
// before the host's admission was checked, ahead of its first change. The
// fake host lets the apply go no further than its mesh, which is past both.
func publishedBeforeTheCheck(rec *ui.Recorder, what string) bool {
	publish, check := rec.Index("done", what), rec.Index("done", "check site's admission")
	return publish >= 0 && check > publish
}

// A new deployment, with no system copy here and sites that hold nothing,
// publishes the first version as the root before the first change, and
// goes on to apply it.
func TestApplyWithNoBasePublishesTheRoot(t *testing.T) {
	f := newApplyStore(t, 0)
	log := loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	head, _ := f.sys.Head()
	root, _ := f.sys.Root()
	if head == "" || root != head || !f.pushed() || !checked(logged(t, log)) {
		t.Fatalf("head %q, root %q, pushed %v, checked %v\n%s", head, root, f.pushed(), checked(logged(t, log)), rec.Lines())
	}
	if !publishedBeforeTheCheck(rec, "publish revision 1") {
		t.Errorf("not published before the admission check:\n%s", rec.Lines())
	}
	// The first deploy of a new deployment makes its system copy, and the
	// draft is drafted from the version deployed.
	if _, ok, err := must(openStore()).Held(fixtureDeploymentID); !ok || err != nil {
		t.Errorf("no system copy: %v", err)
	}
	if m, ok := draft.ReadMarker(f.draftBody(t)); !ok || m.SHA != head || m.Revision != 1 {
		t.Errorf("marker %+v %v", m, ok)
	}
}

// Edited local files on the sites' head are published as the next
// revision before the first change.
func TestApplyPublishesTheNextRevisionFirst(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	edited := f.files[0]
	edited.Paisans = append(append([]byte{}, edited.Paisans...), "# next\n"...)
	if err := f.write(edited); err != nil {
		t.Fatal(err)
	}
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, &grant.ConfigPart{Hash: f.shas[0], Revision: 1}))
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	head, _ := f.sys.Head()
	if head == f.shas[0] || !f.pushed() || !publishedBeforeTheCheck(rec, "publish revision 2") {
		t.Errorf("head %s, pushed %v\n%s", head, f.pushed(), rec.Lines())
	}
	if b, _ := os.ReadFile(filepath.Join(f.sys.Dir, ".paisans", "config-seen")); string(b) != "2 "+head+"\n" {
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
	if err := f.write(rotated); err != nil {
		t.Fatal(err)
	}
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, &grant.ConfigPart{Hash: f.shas[0], Revision: 1}))
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	head, _ := f.sys.Head()
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
	if h, _ := f.sys.Head(); h != f.shas[0] || !publishedBeforeTheCheck(rec, "publish revision 1, "+f.shas[0][:12]) {
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
	if err := f.write(edited); err != nil {
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
	f.editDraft(t, "# next\n")
	sysBefore, draftBefore := rawFiles(t, f.sys.Dir), rawFiles(t, f.dir)
	log := loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	err := f.applySite(t, noTerminalStdin(), "--execute")
	if err == nil || !strings.Contains(problemText(err), "no site took the version") || checked(logged(t, log)) {
		t.Fatalf("got %v, checked %v", problemText(err), checked(logged(t, log)))
	}
	// The system copy's files are what they were, and the draft's line too.
	if !maps.Equal(rawFiles(t, f.sys.Dir), sysBefore) || !maps.Equal(rawFiles(t, f.dir), draftBefore) {
		t.Error("a version no site took changed the system copy's files or the draft")
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
	// A version some sites took is the system copy's, whatever the others
	// did: head, seen and the draft's line name it.
	head, _ := f.sys.Head()
	rev, seen, _ := f.sys.Seen()
	m, ok := draft.ReadMarker(f.draftBody(t))
	if head == f.shas[0] || rev != 2 || seen != head || !ok || m.SHA != head || m.Revision != 2 {
		t.Errorf("head %s, seen %d %s, marker %+v %v", head, rev, seen, m, ok)
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
	head, _ := f.sys.Head()
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
	if err := f.write(read); err != nil {
		t.Fatal(err)
	}
	cfg, paisans, err := config.LoadWithBytes(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded := configstore.Files{Paisans: paisans, Secrets: read.Secrets}
	later := read
	later.Paisans = append(append([]byte{}, read.Paisans...), "# edited while asked\n"...)
	if err := f.write(later); err != nil {
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
	cache, _ := f.sys.Cache()
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
	if err := f.sys.SetHead(f.shas[1]); err != nil {
		t.Fatal(err)
	}
	f.marker = &draft.Marker{Revision: 2, SHA: f.shas[1]}
	if err := f.write(f.files[1]); err != nil {
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
	secretsPath := filepath.Join(f.dir, "secrets.enc.yaml")
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
	head, _ := f.sys.Head()
	cache, err := f.sys.Cache()
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
	secretsPath := filepath.Join(f.dir, "secrets.enc.yaml")
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
	if head, _ := f.sys.Head(); head != v1.sha {
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
	secretsPath := filepath.Join(f.dir, "secrets.enc.yaml")
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

// differenceShown is the step apply's review shows the configuration's
// difference on, and the lines listed under it.
func differenceShown(t *testing.T, rec *ui.Recorder) (ui.Event, []string, int) {
	t.Helper()
	i := rec.Index("done", "what changes in the configuration")
	if i < 0 {
		t.Fatalf("no difference shown:\n%s", rec.Lines())
	}
	var lines []string
	for _, e := range rec.Events[i+1:] {
		if e.Kind != "verbatim" {
			break
		}
		lines = append(lines, e.Text)
	}
	return rec.Events[i], lines, i
}

// edit writes the version's files with old replaced by new in one of them.
func (f *applyStoreFixture) edit(t *testing.T, secrets bool, old, new string) {
	t.Helper()
	edited := f.files[0]
	target := &edited.Paisans
	if secrets {
		target = &edited.Secrets
	}
	if !bytes.Contains(*target, []byte(old)) {
		t.Fatalf("the fixture lacks %q", old)
	}
	*target = bytes.Replace(*target, []byte(old), []byte(new), 1)
	if err := f.write(edited); err != nil {
		t.Fatal(err)
	}
}

// A dry run shows how paisans-stack.yaml differs from the sites' newest
// version, as a diff from that revision to the local files, before the
// site is reached.
func TestApplyDryRunShowsTheConfigurationDifference(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	f.edit(t, false, "hostname: wiki.example.org", "hostname: wiki2.example.org")
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin())
	step, lines, i := differenceShown(t, rec)
	if !strings.Contains(step.Extra, "revision 1, "+f.shas[0][:12]+", signed by alice") {
		t.Errorf("does not name the sites' revision: %q", step.Extra)
	}
	text := strings.Join(lines, "\n")
	for _, want := range []string{"-    hostname: wiki.example.org", "+    hostname: wiki2.example.org", "revision-1/paisans-stack.yaml", "draft/paisans-stack.yaml"} {
		if !strings.Contains(text, want) {
			t.Errorf("difference lacks %q:\n%s", want, text)
		}
	}
	if site := rec.Index("section", "vm ("); site >= 0 && site < i {
		t.Errorf("shown after the site was reached:\n%s", rec.Lines())
	}
}

// A changed secret is named by its key, and neither its old value nor its
// new one is printed.
func TestApplyShowsAChangedSecretByKeyOnly(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	const old, changed = "fixture-not-a-secret-standby", "rotated-standby-value-7f3e"
	f.edit(t, true, `"`+old+`"`, `"`+changed+`"`)
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	rec := recordConverge(t)
	var out bytes.Buffer
	printed := captureStdout(t, func() {
		_ = runApply([]string{"--config", f.cfgPath, "--site", "vm"}, noTerminalStdin(), &out)
	})
	_, lines, _ := differenceShown(t, rec)
	if !slices.Contains(lines, "secrets: cluster.standby_password differs") {
		t.Errorf("does not name the key:\n%s", strings.Join(lines, "\n"))
	}
	for _, value := range []string{old, changed} {
		if all := rec.Lines() + printed + out.String(); strings.Contains(all, value) {
			t.Errorf("printed the value %q:\n%s", value, all)
		}
	}
}

// Local files equal to the sites' newest version say so in one line.
func TestApplyShowsUnchangedFilesInOneLine(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin())
	step, lines, _ := differenceShown(t, rec)
	if !strings.Contains(step.Extra, "unchanged from revision 1, "+f.shas[0][:12]) || len(lines) != 0 {
		t.Errorf("result %q, lines %q", step.Extra, lines)
	}
}

// A deployment's first version has nothing to differ from, and says so.
func TestApplyShowsTheFirstVersionAsSuch(t *testing.T) {
	f := newApplyStore(t, 0)
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin())
	step, lines, _ := differenceShown(t, rec)
	if !strings.Contains(step.Extra, "the first version") || len(lines) != 0 {
		t.Errorf("result %q, lines %q", step.Extra, lines)
	}
}

// --execute off a terminal, answered with --yes, shows the difference
// before the review it approves.
func TestApplyWithYesShowsTheDifferenceBeforeTheReview(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	f.edit(t, false, "hostname: wiki.example.org", "hostname: wiki2.example.org")
	allowYes(t)
	loggingSSH(t, "")
	fakeGrants(t, func(string) *grant.Record { return nil })
	rec := recordConverge(t)
	var out bytes.Buffer
	captureStdout(t, func() {
		_ = runApply([]string{"--config", f.cfgPath, "--site", "vm", "--execute", "--yes"}, noTerminalStdin(), &out)
	})
	_, lines, i := differenceShown(t, rec)
	if !slices.Contains(lines, "+    hostname: wiki2.example.org") {
		t.Errorf("difference %q", lines)
	}
	review := rec.Index("pending", "review what paisans-stack.yaml grants")
	if review < 0 || i > review || !strings.Contains(out.String(), "approved") {
		t.Errorf("difference at %d, review at %d, printed %q\n%s", i, review, out.String(), rec.Lines())
	}
}

// The difference is shown as git wrote it: a blank context line is kept,
// so each hunk still holds the lines its header counts.
func TestApplyShowsTheDifferenceAsWritten(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	long := "# " + strings.Repeat("a long comment ", 12)
	f.edit(t, false, "    replication: 2\n", "    replication: 2 "+long+"\n")
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin())
	_, lines, _ := differenceShown(t, rec)
	if !slices.Contains(lines, " ") || !slices.Contains(lines, "+    replication: 2 "+long) {
		t.Errorf("not as written:\n%q", lines)
	}
}

// draftBody is the draft's paisans-stack.yaml as it is on disk.
func (f *applyStoreFixture) draftBody(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// editDraft writes the first version's files with line added to
// paisans-stack.yaml as the draft, and returns them as a version holds
// them.
func (f *applyStoreFixture) editDraft(t *testing.T, line string) configstore.Files {
	t.Helper()
	edited := f.files[0]
	edited.Paisans = append(append([]byte{}, edited.Paisans...), line...)
	if err := f.write(edited); err != nil {
		t.Fatal(err)
	}
	return edited
}

// rawFiles is each of the three files in dir byte for byte, absent ones
// left out.
func rawFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, name := range []string{config.FileName, configstore.FileSecrets, configstore.FileSOPS} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(b)
	}
	return out
}

// A deploy makes the draft's files the system copy's: its files are the
// version published, its head is that version, and its record names it
// and the folder it was deployed from.
func TestApplyDeploysDraftIntoSystemCopy(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	edited := f.editDraft(t, "# next\n")
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, &grant.ConfigPart{Hash: f.shas[0], Revision: 1}))
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	head, _ := f.sys.Head()
	if head == f.shas[0] || !f.pushed() {
		t.Fatalf("head %s, pushed %v\n%s", head, f.pushed(), rec.Lines())
	}
	got, err := f.sys.Files()
	if err != nil || !bytes.Equal(got.Paisans, edited.Paisans) || !bytes.Equal(got.Secrets, edited.Secrets) || !bytes.Equal(got.SOPS, edited.SOPS) {
		t.Errorf("the system copy's files are not the version deployed: %v", err)
	}
	saved, ok, err := must(openStore()).Load(fixtureDeploymentID)
	if err != nil || !ok || saved.SHA != head || saved.Revision != 2 || saved.Status != localstore.Verified || saved.Source != "applied from "+f.dir {
		t.Errorf("record %+v, %v", saved, err)
	}
}

// The version a deploy signs holds the draft's paisans-stack.yaml without
// its "drafted from" line, and so do the system copy's files.
func TestPublishedVersionHasNoMarker(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	edited := f.editDraft(t, "# next\n")
	if _, ok := draft.ReadMarker(f.draftBody(t)); !ok {
		t.Fatal("the draft has no drafted from line")
	}
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, &grant.ConfigPart{Hash: f.shas[0], Revision: 1}))
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	head, _ := f.sys.Head()
	cache, err := f.sys.Cache()
	if err != nil {
		t.Fatal(err)
	}
	v, err := cache.Files(head)
	if err != nil || head == f.shas[0] {
		t.Fatalf("head %s: %v\n%s", head, err, rec.Lines())
	}
	if !bytes.Equal(v.Paisans, edited.Paisans) || bytes.Contains(v.Paisans, []byte("drafted from")) {
		t.Errorf("the version holds:\n%s", v.Paisans)
	}
	if b, _ := os.ReadFile(filepath.Join(f.sys.Dir, config.FileName)); bytes.Contains(b, []byte("drafted from")) {
		t.Errorf("the system copy's file holds the line:\n%s", b)
	}
}

// A deployed draft's "drafted from" line names the version just deployed,
// and the rest of the draft is left as it was.
func TestDeployRewritesMarker(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	edited := f.editDraft(t, "# next\n")
	secrets := rawFiles(t, f.dir)[configstore.FileSecrets]
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, &grant.ConfigPart{Hash: f.shas[0], Revision: 1}))
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	head, _ := f.sys.Head()
	body := f.draftBody(t)
	m, ok := draft.ReadMarker(body)
	if !ok || m.Revision != 2 || m.SHA != head || head == f.shas[0] {
		t.Fatalf("marker %+v %v, head %s\n%s", m, ok, head, rec.Lines())
	}
	if !bytes.Equal(draft.StripMarker(body), edited.Paisans) || rawFiles(t, f.dir)[configstore.FileSecrets] != secrets {
		t.Errorf("the rest of the draft changed:\n%s", body)
	}
}

// A publish that fails leaves the system copy's files as they were, byte
// for byte, and the draft's line unchanged; a system copy with no files is
// left with none.
func TestDeployRestoresSystemCopyOnPublishFailure(t *testing.T) {
	sys := withSystemCopy(t, fixtureDeploymentID)
	held := configstore.Files{Paisans: []byte("# held\nid: " + fixtureDeploymentID + "\n"), Secrets: []byte("held: secrets\n"), SOPS: []byte("creation_rules: []\n")}
	if err := sys.WriteFiles(held); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(sys.Dir, config.FileName), 0o600); err != nil {
		t.Fatal(err)
	}
	before := rawFiles(t, sys.Dir)
	d := draft.Draft{Dir: t.TempDir()}
	version := configstore.Files{Paisans: []byte("id: " + fixtureDeploymentID + "\n# drafted\n"), Secrets: []byte("draft: secrets\n"), SOPS: []byte("creation_rules: []\n")}
	if err := d.Write(version, draft.Marker{Revision: 4, SHA: strings.Repeat("a", 40)}); err != nil {
		t.Fatal(err)
	}
	drafted := rawFiles(t, d.Dir)
	boom := errors.New("no site took it")
	var during map[string]string
	_, _, err := deployDraft(ui.Discard, sys, d, version, func() (string, int, error) {
		during = rawFiles(t, sys.Dir)
		return "", 0, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if during[config.FileName] != string(version.Paisans) {
		t.Errorf("the publish ran on the system copy's files %q", during[config.FileName])
	}
	if after := rawFiles(t, sys.Dir); !maps.Equal(after, before) {
		t.Errorf("the system copy's files are\n%q\nnot\n%q", after, before)
	}
	if info, _ := os.Stat(filepath.Join(sys.Dir, config.FileName)); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	if after := rawFiles(t, d.Dir); !maps.Equal(after, drafted) {
		t.Errorf("the draft changed: %q", after)
	}

	empty := addSystemCopy(t, "0b7d4c3a-f2a9-4c3a-9e2f-5a6b7c8d9e01")
	if _, _, err := deployDraft(ui.Discard, empty, d, version, func() (string, int, error) { return "", 0, boom }); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if after := rawFiles(t, empty.Dir); len(after) != 0 {
		t.Errorf("an empty system copy was left %q", after)
	}
}

// A draft drafted from a version older than the sites' newest is refused
// until it is pulled forward, before the review and before any host
// changes, naming deployment pull; the system copy and the draft are as
// they were.
func TestApplyRefusesNewerUntilPulled(t *testing.T) {
	f := newApplyStore(t, 2)
	f.based(t)
	sysBefore, draftBefore := rawFiles(t, f.sys.Dir), rawFiles(t, f.dir)
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
	if h, _ := f.sys.Head(); h != f.shas[0] {
		t.Errorf("head moved to %s", h)
	}
	if rev, _, _ := f.sys.Seen(); rev != 0 {
		t.Errorf("a refused apply raised seen to %d", rev)
	}
	if !maps.Equal(rawFiles(t, f.sys.Dir), sysBefore) || !maps.Equal(rawFiles(t, f.dir), draftBefore) {
		t.Error("a refused apply changed the system copy's files or the draft")
	}
}

// The base is the version the draft's "drafted from" line names when the
// system copy trusts it, whatever the system copy's own files are: a draft
// of the sites' newest deploys on it. A line naming a version the history
// does not hold is ignored, the system copy's head taken instead, and the
// review says so.
func TestApplyBaseIsTheDraftedFromVersion(t *testing.T) {
	f := newApplyStore(t, 2)
	f.based(t)
	f.marker = &draft.Marker{Revision: 2, SHA: f.shas[1]}
	next := f.files[1]
	next.Paisans = append(append([]byte{}, next.Paisans...), "# next\n"...)
	if err := f.write(next); err != nil {
		t.Fatal(err)
	}
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, &grant.ConfigPart{Hash: f.shas[1], Revision: 2}))
	rec := recordConverge(t)
	if err := f.applySite(t, noTerminalStdin(), "--execute"); err != nil && !f.pushed() {
		t.Fatalf("%v\n%s", problemText(err), rec.Lines())
	}
	head, _ := f.sys.Head()
	cache, _ := f.sys.Cache()
	if head == f.shas[1] || cache.Revision(head) != 3 || !publishedBeforeTheCheck(rec, "publish revision 3") {
		t.Errorf("head %s at %d\n%s", head, cache.Revision(head), rec.Lines())
	}

	g := newApplyStore(t, 2)
	g.based(t)
	g.marker = &draft.Marker{Revision: 2, SHA: strings.Repeat("b", 40)}
	if err := g.write(g.files[0]); err != nil {
		t.Fatal(err)
	}
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, g, nil))
	rec = recordConverge(t)
	err := g.applySite(t, noTerminalStdin(), "--execute")
	if err == nil || !strings.Contains(problemText(err), "paisans deployment pull") || g.pushed() {
		t.Fatalf("got %v\n%s", problemText(err), rec.Lines())
	}
	if !strings.Contains(rec.Lines(), "it is taken as drafted from revision 1, the system copy's") {
		t.Errorf("the review does not say which base it took:\n%s", rec.Lines())
	}
}

// Sites holding a fork are refused before the review, naming publish
// --merge and the checkout that writes each head's files; nothing changes.
func TestApplyRefusesFork(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	for _, suffix := range []string{"# one\n", "# two\n"} {
		v := f.files[0]
		v.Paisans = append(append([]byte{}, v.Paisans...), suffix...)
		if _, err := f.store.repo.Commit(v, f.shas[:1], 2, f.key, time.Unix(1_800_000_000, 0)); err != nil {
			t.Fatal(err)
		}
	}
	f.answer = f.store.answer(t)
	allSitesUp(f.answer, f.runs)
	f.editDraft(t, "# next\n")
	sysBefore, draftBefore := rawFiles(t, f.sys.Dir), rawFiles(t, f.dir)
	log := loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	recordConverge(t)
	err := f.applySite(t, noTerminalStdin(), "--execute")
	text := problemText(err)
	for _, want := range []string{"the sites hold a fork", "paisans deployment publish --merge", "paisans deployment checkout --head"} {
		if err == nil || !strings.Contains(text, want) {
			t.Errorf("refusal lacks %q: %v", want, text)
		}
	}
	if checked(logged(t, log)) || f.pushed() {
		t.Errorf("changed a host: checked %v, pushed %v", checked(logged(t, log)), f.pushed())
	}
	if h, _ := f.sys.Head(); h != f.shas[0] || !maps.Equal(rawFiles(t, f.sys.Dir), sysBefore) || !maps.Equal(rawFiles(t, f.dir), draftBefore) {
		t.Errorf("a refused fork changed the system copy (head %s) or the draft", h)
	}
}

// A dry run gathers without asking a site to sync and records the versions
// the sites hold in the system copy's store, and nothing else: no publish,
// no seen record, no head, no file of the system copy or the draft moved.
// It reports the rollback --execute would ask for.
func TestApplyDryRunWritesNothing(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	f.editDraft(t, "# next\n")
	sysBefore, draftBefore := rawFiles(t, f.sys.Dir), rawFiles(t, f.dir)
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, &grant.ConfigPart{Hash: strings.Repeat("c", 40), Revision: 5}))
	rec := recordConverge(t)
	if err := f.applySite(t, noTerminalStdin()); err != nil {
		t.Fatalf("%v\n%s", problemText(err), rec.Lines())
	}
	if synced(*f.runs) || f.pushed() {
		t.Errorf("a dry run changed a site: %q", *f.runs)
	}
	if h, _ := f.sys.Head(); h != f.shas[0] {
		t.Errorf("head moved to %s", h)
	}
	if _, err := os.Stat(filepath.Join(f.sys.Dir, ".paisans", "config-seen")); !os.IsNotExist(err) {
		t.Errorf("a dry run remembered a revision: %v", err)
	}
	if !maps.Equal(rawFiles(t, f.sys.Dir), sysBefore) || !maps.Equal(rawFiles(t, f.dir), draftBefore) {
		t.Error("a dry run changed the system copy's files or the draft")
	}
	if cache, _ := f.sys.Cache(); cache.Revision(f.shas[0]) != 1 {
		t.Error("a dry run did not record the versions the sites hold")
	}
	if _, ok, _ := must(openStore()).Load(fixtureDeploymentID); ok {
		t.Error("a dry run wrote the record")
	}
	if !rec.Has("pending", "publish revision 2") || !strings.Contains(rec.Lines(), "--execute will ask for rollback") {
		t.Errorf("the dry run does not say what --execute does:\n%s", rec.Lines())
	}
	noFolderState(t, f.dir)
}

// apply writes nothing into the draft's folder but the draft: no .paisans,
// whether it deploys a new deployment or the next version of one.
func TestApplyNoFolderState(t *testing.T) {
	for _, n := range []int{0, 1} {
		f := newApplyStore(t, n)
		if n > 0 {
			f.based(t)
			f.editDraft(t, "# next\n")
		}
		loggingSSH(t, "")
		fakeGrants(t, grantedWith(t, f, nil))
		rec := recordConverge(t)
		_ = f.applySite(t, noTerminalStdin())
		_ = f.applySite(t, noTerminalStdin(), "--execute")
		if head, _ := f.sys.Head(); head == "" || !f.pushed() {
			t.Errorf("%d: nothing deployed\n%s", n, rec.Lines())
		}
		noFolderState(t, f.dir)
	}
}

// A deploy that another overtook between its gather and its publish is
// refused at publish, naming deployment apply again, and changes nothing.
func TestApplyRefusesAnOvertakenDeploy(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	f.editDraft(t, "# next\n")
	cfg := mustLoad(t, f.cfgPath)
	v, err := gatherForApply(ui.Discard, f.cfgPath, "", nil, cfg, true, answerer{}, noTerminalStdin(), io.Discard)
	if err != nil {
		t.Fatal(problemText(err))
	}
	other := strings.Repeat("d", 40)
	if err := f.sys.SetHead(other); err != nil {
		t.Fatal(err)
	}
	sysBefore := rawFiles(t, f.sys.Dir)
	_, err = v.publish(ui.Discard, true)
	if err == nil || !strings.Contains(problemText(err), "paisans deployment apply") || f.pushed() {
		t.Fatalf("got %v, pushed %v", problemText(err), f.pushed())
	}
	if h, _ := f.sys.Head(); h != other || !maps.Equal(rawFiles(t, f.sys.Dir), sysBefore) {
		t.Errorf("head %s; the system copy changed", h)
	}
}

// A deploy while another command writes the system copy is refused,
// naming the writer, and publishes nothing.
func TestApplyRefusesWhileTheSystemCopyIsLocked(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	f.editDraft(t, "# next\n")
	cfg := mustLoad(t, f.cfgPath)
	v, err := gatherForApply(ui.Discard, f.cfgPath, "", nil, cfg, true, answerer{}, noTerminalStdin(), io.Discard)
	if err != nil {
		t.Fatal(problemText(err))
	}
	unlock, err := must(openStore()).Lock(fixtureDeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	_, err = v.publish(ui.Discard, true)
	if err == nil || !strings.Contains(problemText(err), "is being written by pid") || f.pushed() {
		t.Fatalf("got %v, pushed %v", problemText(err), f.pushed())
	}
	if h, _ := f.sys.Head(); h != f.shas[0] {
		t.Errorf("head moved to %s", h)
	}
}

// scratchLeft is the scratch directories a gather of a deployment with no
// system copy left in the temporary directory.
func scratchLeft(t *testing.T) []string {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(os.TempDir(), "paisans-gather-*"))
	if err != nil {
		t.Fatal(err)
	}
	return left
}

// The scratch directory a deployment with no system copy gathers into is
// gone once apply or publish ends: after a dry run, a first deploy, and a
// refusal of a deployment the sites hold versions of.
func TestApplyLeavesNoScratchDirectory(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	f := newApplyStore(t, 0)
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	recordConverge(t)
	_ = f.applySite(t, noTerminalStdin())
	if left := scratchLeft(t); len(left) != 0 {
		t.Errorf("a dry run left %q", left)
	}
	var out bytes.Buffer
	captureStdout(t, func() { _ = runDeployment([]string{"publish", "--config", f.cfgPath}, noTerminalStdin(), &out) })
	if left := scratchLeft(t); len(left) != 0 {
		t.Errorf("a publish dry run left %q", left)
	}
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	if head, _ := f.sys.Head(); head == "" {
		t.Fatal("nothing deployed")
	}
	if left := scratchLeft(t); len(left) != 0 {
		t.Errorf("a first deploy left %q", left)
	}

	g := newApplyStore(t, 1)
	if err := os.RemoveAll(g.sys.Dir); err != nil {
		t.Fatal(err)
	}
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, g, nil))
	recordConverge(t)
	if err := g.applySite(t, noTerminalStdin(), "--execute"); err == nil || !strings.Contains(problemText(err), "paisans deployment recover") {
		t.Fatalf("got %v", problemText(err))
	}
	if left := scratchLeft(t); len(left) != 0 {
		t.Errorf("a refusal left %q", left)
	}
}

// What fails once a site took the version is a warning and takes nothing
// back: a record that cannot be written leaves the system copy holding the
// version, its head, seen and files, and the draft's line naming it.
func TestApplyAfterThePushWarnsAndKeepsTheVersion(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	edited := f.editDraft(t, "# next\n")
	// saved.json cannot be replaced by a directory of that name.
	if err := os.MkdirAll(filepath.Join(f.sys.Dir, "saved.json", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, &grant.ConfigPart{Hash: f.shas[0], Revision: 1}))
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	if !rec.Has("warn", "the system copy's record was not written") {
		t.Errorf("no warning:\n%s", rec.Lines())
	}
	head, _ := f.sys.Head()
	rev, seen, _ := f.sys.Seen()
	cache, _ := f.sys.Cache()
	got, _ := f.sys.Files()
	m, ok := draft.ReadMarker(f.draftBody(t))
	if head == f.shas[0] || rev != 2 || seen != head || cache.Revision(head) != 2 || !bytes.Equal(got.Paisans, edited.Paisans) || !ok || m.SHA != head {
		t.Errorf("head %s, seen %d %s, marker %+v %v\n%s", head, rev, seen, m, ok, rec.Lines())
	}
}

// A "drafted from" line that cannot be rewritten after the publish is a
// warning: the system copy keeps the version's files.
func TestDeployWarnsWhenTheMarkerCannotBeRewritten(t *testing.T) {
	sys := withSystemCopy(t, fixtureDeploymentID)
	d := draft.Draft{Dir: t.TempDir()}
	version := configstore.Files{Paisans: []byte("id: " + fixtureDeploymentID + "\n"), Secrets: []byte("draft: secrets\n"), SOPS: []byte("creation_rules: []\n")}
	if err := d.Write(version, draft.Marker{Revision: 1, SHA: strings.Repeat("a", 40)}); err != nil {
		t.Fatal(err)
	}
	rec := &ui.Recorder{}
	sha := strings.Repeat("b", 40)
	got, rev, err := deployDraft(rec, sys, d, version, func() (string, int, error) {
		return sha, 2, os.Remove(filepath.Join(d.Dir, config.FileName))
	})
	if err != nil || got != sha || rev != 2 {
		t.Fatalf("got %s %d %v", got, rev, err)
	}
	if !rec.Has("warn", `the draft's "drafted from" line was not rewritten`) {
		t.Errorf("no warning:\n%s", rec.Lines())
	}
	if b, _ := os.ReadFile(filepath.Join(sys.Dir, config.FileName)); string(b) != string(version.Paisans) {
		t.Errorf("the system copy holds %q", b)
	}
}

// A first deploy that no site took leaves no system copy: no root, no
// signing key, no record, nothing under the data directory's deployments.
func TestApplyFailedFirstDeployLeavesNoSystemCopy(t *testing.T) {
	f := newApplyStore(t, 0)
	configSiteTransport = func(name string, _ config.Site) apply.Transport {
		return &storeHost{name: name, answer: f.answer, runs: f.runs, noPush: true}
	}
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	recordConverge(t)
	before := f.draftBody(t)
	if err := f.applySite(t, noTerminalStdin(), "--execute"); err == nil || !strings.Contains(problemText(err), "no site took the version") {
		t.Fatalf("got %v", problemText(err))
	}
	if _, err := os.Stat(f.sys.Dir); !os.IsNotExist(err) {
		t.Errorf("the system copy is there: %v", err)
	}
	if _, ok, _ := must(openStore()).Held(fixtureDeploymentID); ok {
		t.Error("a system copy is held")
	}
	if !bytes.Equal(f.draftBody(t), before) {
		t.Error("the draft changed")
	}
}
