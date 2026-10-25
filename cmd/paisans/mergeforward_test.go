package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/draft"
	"github.com/paisans-software/paisans-cli/internal/grant"
)

// The two paths the tests below edit: the community's name and bob's.
const (
	communityName = `name: "Fixture Community"`
	bobName       = "name: Bob Example"
)

// replaced is files with old replaced by new in paisans-stack.yaml.
func replaced(t *testing.T, files configstore.Files, old, new string) configstore.Files {
	t.Helper()
	if !bytes.Contains(files.Paisans, []byte(old)) {
		t.Fatalf("the files lack %q", old)
	}
	files.Paisans = bytes.Replace(files.Paisans, []byte(old), []byte(new), 1)
	return files
}

// sitesHold adds files to the sites' store as revision rev on parent, and
// every site answers with it. It returns the version.
func (f *applyStoreFixture) sitesHold(t *testing.T, files configstore.Files, parent string, rev int) string {
	t.Helper()
	sha, err := f.store.repo.Commit(files, []string{parent}, rev, f.key, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	f.answer = f.store.answer(t)
	allSitesUp(f.answer, f.runs)
	return sha
}

// pull runs deployment pull on the draft, and returns what it reported.
func (f *applyStoreFixture) pull(t *testing.T, extra ...string) (string, error) {
	t.Helper()
	rep := plainReporter(t)
	var out bytes.Buffer
	err := runDeployment(append([]string{"pull", "--config", f.cfgPath}, extra...), noTerminalStdin(), &out)
	return rep.String() + out.String(), err
}

// wantMerged fails the test unless the draft holds both want strings and
// is drafted from revision rev, sha.
func wantMerged(t *testing.T, path string, rev int, sha string, want ...string) {
	t.Helper()
	body := must(os.ReadFile(path))
	if m, ok := draft.ReadMarker(body); !ok || m.Revision != rev || m.SHA != sha {
		t.Errorf("marker %+v %v, want revision %d %s", m, ok, rev, sha)
	}
	for _, w := range want {
		if !bytes.Contains(body, []byte(w)) {
			t.Errorf("the draft lacks %q:\n%s", w, body)
		}
	}
}

// A draft behind the sites is merged forward from its base: the draft
// takes what the sites' newest version changed and keeps its own changes,
// is drafted from that version, and the system copy moves to it. A dry run
// says so and writes nothing.
func TestPullMergesDraftForward(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	sha := f.sitesHold(t, replaced(t, f.files[0], communityName, `name: "Their Community"`), f.shas[0], 2)
	if err := f.write(replaced(t, f.files[0], bobName, "name: Bob Draft")); err != nil {
		t.Fatal(err)
	}
	sysBefore, draftBefore := rawFiles(t, f.sys.Dir), rawFiles(t, f.dir)
	out, err := f.pull(t)
	if err != nil {
		t.Fatal(problemText(err))
	}
	if !strings.Contains(out, "merge revision 2 into this draft") || !strings.Contains(out, "--execute will write it") {
		t.Errorf("the dry run does not say what --execute merges:\n%s", out)
	}
	if !maps.Equal(rawFiles(t, f.sys.Dir), sysBefore) || !maps.Equal(rawFiles(t, f.dir), draftBefore) {
		t.Error("a dry run changed the system copy's files or the draft")
	}
	if h, _ := f.sys.Head(); h != f.shas[0] {
		t.Errorf("a dry run moved the head to %s", h)
	}

	if _, err := f.pull(t, "--execute"); err != nil {
		t.Fatal(problemText(err))
	}
	wantMerged(t, f.cfgPath, 2, sha, `name: "Their Community"`, "name: Bob Draft")
	if h, _ := f.sys.Head(); h != sha {
		t.Errorf("head %s, want %s", h, sha)
	}
	if got := rawFiles(t, f.sys.Dir)[config.FileName]; !strings.Contains(got, "Their Community") || strings.Contains(got, "Bob Draft") {
		t.Errorf("the system copy's files are not revision 2's:\n%s", got)
	}
	noFolderState(t, f.dir)
}

// A path both sides changed differently is listed by its dotted path; the
// draft keeps its own value there, takes the rest, and is drafted from the
// sites' newest version.
func TestPullConflictListsPaths(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	theirs := replaced(t, replaced(t, f.files[0], communityName, `name: "Their Community"`), bobName, "name: Bob Theirs")
	sha := f.sitesHold(t, theirs, f.shas[0], 2)
	if err := f.write(replaced(t, f.files[0], communityName, `name: "Draft Community"`)); err != nil {
		t.Fatal(err)
	}
	_, err := f.pull(t, "--execute")
	text := problemText(err)
	if err == nil || !strings.Contains(text, "paisans-stack.yaml: community.name") || strings.Contains(text, "operators.bob") {
		t.Fatalf("got %v\n%s", err, text)
	}
	wantMerged(t, f.cfgPath, 2, sha, `name: "Draft Community"`, "name: Bob Theirs")
	if h, _ := f.sys.Head(); h != sha {
		t.Errorf("head %s, want %s", h, sha)
	}
}

// In a folder with no draft, pull brings the system copy to the sites'
// newest version and starts a draft of it; a dry run writes neither.
func TestPullStartsDraftWhenNone(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	sha := f.sitesHold(t, replaced(t, f.files[0], communityName, `name: "Their Community"`), f.shas[0], 2)
	empty := filepath.Join(t.TempDir(), config.FileName)
	var out bytes.Buffer
	if err := runDeployment([]string{"pull", "--config", empty}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err))
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Fatalf("a dry run started a draft: %v", err)
	}
	if h, _ := f.sys.Head(); h != f.shas[0] {
		t.Errorf("a dry run moved the head to %s", h)
	}
	if err := runDeployment([]string{"pull", "--config", empty, "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err))
	}
	wantMerged(t, empty, 2, sha, `name: "Their Community"`)
	if h, _ := f.sys.Head(); h != sha {
		t.Errorf("head %s, want %s", h, sha)
	}
	noFolderState(t, filepath.Dir(empty))
}

// Two folders drafted from revision 1: once the first is deployed, applying
// the second merges the first's changes into it from revision 1, the
// version its "drafted from" line names, rather than deploying it over
// them; a third that changes a path the first changed is a conflict, and
// nothing is deployed.
func TestMergeBaseFromMarker(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	second, third := t.TempDir(), t.TempDir()
	from := draft.Marker{Revision: 1, SHA: f.shas[0]}
	if err := (draft.Draft{Dir: second}).Write(replaced(t, f.files[0], bobName, "name: Bob Second"), from); err != nil {
		t.Fatal(err)
	}
	if err := (draft.Draft{Dir: third}).Write(replaced(t, f.files[0], communityName, `name: "Third Community"`), from); err != nil {
		t.Fatal(err)
	}
	if err := f.write(replaced(t, f.files[0], communityName, `name: "First Community"`)); err != nil {
		t.Fatal(err)
	}
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, &grant.ConfigPart{Hash: f.shas[0], Revision: 1}))
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	first, _ := f.sys.Head()
	if first == f.shas[0] || !f.pushed() {
		t.Fatalf("the first draft was not deployed\n%s", rec.Lines())
	}
	// The sites hold what the first deploy pushed.
	cache, _ := f.sys.Cache()
	allSitesUp(base64.StdEncoding.EncodeToString(must(cache.Bundle()))+"\n", f.runs)
	*f.runs = nil

	f.dir, f.cfgPath = second, filepath.Join(second, config.FileName)
	err := f.applySite(t, noTerminalStdin(), "--execute")
	if err == nil || !strings.Contains(problemText(err), "merged revision 2 into this draft; review it and run apply again") || f.pushed() {
		t.Fatalf("got %v, pushed %v\n%s", problemText(err), f.pushed(), rec.Lines())
	}
	wantMerged(t, f.cfgPath, 2, first, `name: "First Community"`, "name: Bob Second")

	f.dir, f.cfgPath = third, filepath.Join(third, config.FileName)
	err = f.applySite(t, noTerminalStdin(), "--execute")
	if text := problemText(err); err == nil || !strings.Contains(text, "paisans-stack.yaml: community.name") || f.pushed() {
		t.Fatalf("got %v, pushed %v", text, f.pushed())
	}
	wantMerged(t, f.cfgPath, 2, first, `name: "Third Community"`)
	if h, _ := f.sys.Head(); h != first {
		t.Errorf("head moved to %s", h)
	}
}

// A "drafted from" line naming a version the system copy's history does not
// hold is ignored: the review says so, and the system copy's files are the
// merge's base.
func TestMergeBaseIgnoresUnknownMarker(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	sha := f.sitesHold(t, replaced(t, f.files[0], communityName, `name: "Their Community"`), f.shas[0], 2)
	f.marker = &draft.Marker{Revision: 7, SHA: strings.Repeat("b", 40)}
	if err := f.write(replaced(t, f.files[0], bobName, "name: Bob Draft")); err != nil {
		t.Fatal(err)
	}
	out, err := f.pull(t, "--execute")
	if err != nil {
		t.Fatal(problemText(err))
	}
	if !strings.Contains(out, "it is taken as drafted from revision 1, the system copy's") {
		t.Errorf("the notice is missing:\n%s", out)
	}
	wantMerged(t, f.cfgPath, 2, sha, `name: "Their Community"`, "name: Bob Draft")
}

// apply of a draft behind the sites merges the sites' newest version into
// it and stops, deploying nothing, and deploys the merged draft on the next
// run; a dry run says what --execute merges and writes nothing.
func TestApplyMergesNewerThenStops(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	sha := f.sitesHold(t, replaced(t, f.files[0], communityName, `name: "Their Community"`), f.shas[0], 2)
	if err := f.write(replaced(t, f.files[0], bobName, "name: Bob Draft")); err != nil {
		t.Fatal(err)
	}
	sysBefore, draftBefore := rawFiles(t, f.sys.Dir), rawFiles(t, f.dir)
	log := loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, &grant.ConfigPart{Hash: sha, Revision: 2}))
	rec := recordConverge(t)
	err := f.applySite(t, noTerminalStdin())
	if text := problemText(err); err == nil || !strings.Contains(text, "--execute merges revision 2 into this draft") {
		t.Fatalf("dry run: %v\n%s", text, rec.Lines())
	}
	if !maps.Equal(rawFiles(t, f.sys.Dir), sysBefore) || !maps.Equal(rawFiles(t, f.dir), draftBefore) {
		t.Error("a dry run changed the system copy's files or the draft")
	}
	if rev, _, _ := f.sys.Seen(); rev != 0 {
		t.Errorf("a dry run raised seen to %d", rev)
	}

	err = f.applySite(t, noTerminalStdin(), "--execute")
	if text := problemText(err); err == nil || !strings.Contains(text, "merged revision 2 into this draft; review it and run apply again") {
		t.Fatalf("got %v\n%s", text, rec.Lines())
	}
	if checked(logged(t, log)) || f.pushed() {
		t.Errorf("changed a host: checked %v, pushed %v", checked(logged(t, log)), f.pushed())
	}
	if rec.Has("pending", "review what paisans-stack.yaml grants") || rec.Index("step", "read what was granted") >= 0 {
		t.Errorf("reviewed before stopping:\n%s", rec.Lines())
	}
	wantMerged(t, f.cfgPath, 2, sha, `name: "Their Community"`, "name: Bob Draft")
	if h, _ := f.sys.Head(); h != sha {
		t.Errorf("head %s, want %s", h, sha)
	}

	_ = f.applySite(t, noTerminalStdin(), "--execute")
	head, _ := f.sys.Head()
	if cache, _ := f.sys.Cache(); head == sha || cache.Revision(head) != 3 || !f.pushed() {
		t.Errorf("the merged draft was not deployed: head %s\n%s", head, rec.Lines())
	}
}

// A merged draft whose write fails part way, its secrets not written, is
// still drafted from its old base, and the system copy has not moved: the
// next pull or apply merges again rather than deploy files the merge never
// finished.
func TestMergedDraftWriteFailureKeepsTheOldBase(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	f.sitesHold(t, replaced(t, f.files[0], communityName, `name: "Their Community"`), f.shas[0], 2)
	if err := f.write(replaced(t, f.files[0], bobName, "name: Bob Draft")); err != nil {
		t.Fatal(err)
	}
	saved := writeMerged
	t.Cleanup(func() { writeMerged = saved })
	boom := errors.New("disk full")
	writeMerged = func(dir string, files configstore.Files) error {
		if err := os.WriteFile(filepath.Join(dir, config.FileName), files.Paisans, 0o644); err != nil {
			t.Fatal(err)
		}
		return boom
	}
	if _, err := f.pull(t, "--execute"); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	wantMerged(t, f.cfgPath, 1, f.shas[0], `name: "Their Community"`, "name: Bob Draft")
	if h, _ := f.sys.Head(); h != f.shas[0] {
		t.Errorf("the system copy moved to %s", h)
	}
}
