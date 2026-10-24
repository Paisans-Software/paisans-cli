package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/draft"
	"github.com/paisans-software/paisans-cli/internal/localstore"
)

// withDataDir points the workstation's data directory at a directory of the
// test's own.
func withDataDir(t *testing.T) {
	t.Helper()
	t.Setenv(localstore.DirVariable, t.TempDir())
}

// withSystemCopy points the workstation's data directory at a directory of
// the test's own and returns deployment id's system copy there, with an
// empty store and no head.
func withSystemCopy(t *testing.T, id string) configstore.Local {
	t.Helper()
	withDataDir(t)
	return addSystemCopy(t, id)
}

// addSystemCopy is deployment id's system copy in the data directory as it
// is, with an empty store and no head.
func addSystemCopy(t *testing.T, id string) configstore.Local {
	t.Helper()
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	l, err := st.Local(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Cache(); err != nil {
		t.Fatal(err)
	}
	return l
}

// fixtureDraft writes the fixture's paisans-stack.yaml into a folder of the
// test's own, with id as its deployment id, and returns its path.
func fixtureDraft(t *testing.T, id string) string {
	t.Helper()
	body, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), config.FileName)
	if err := os.WriteFile(path, []byte(strings.Replace(string(body), "id: "+fixtureDeploymentID, "id: "+id, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A draft names its deployment by its id, whatever else the workstation
// holds; --id naming another is refused.
func TestSystemForByDraftID(t *testing.T) {
	want := withSystemCopy(t, fixtureDeploymentID)
	addSystemCopy(t, otherID)
	path := fixtureDraft(t, fixtureDeploymentID)
	sys, d, id, err := systemFor(path, "")
	if err != nil {
		t.Fatal(problemText(err), err)
	}
	if id != fixtureDeploymentID || sys.Dir != want.Dir || d.Dir != filepath.Dir(path) {
		t.Fatalf("got %s, %s, %s", id, sys.Dir, d.Dir)
	}
	if _, _, _, err := systemFor(path, fixtureDeploymentID); err != nil {
		t.Fatalf("--id naming the draft's own: %v", err)
	}
	_, _, _, err = systemFor(path, otherID)
	if text := problemText(err); err == nil || !strings.Contains(text, otherID) || !strings.Contains(text, fixtureDeploymentID) {
		t.Fatalf("--id naming another: %v\n%s", err, text)
	}
}

// A draft that does not load is reported with the load error.
func TestSystemForReportsADraftThatDoesNotLoad(t *testing.T) {
	withSystemCopy(t, fixtureDeploymentID)
	path := filepath.Join(t.TempDir(), config.FileName)
	os.WriteFile(path, []byte("sites: [\n"), 0o644)
	_, _, _, err := systemFor(path, "")
	if text := problemText(err); err == nil || !strings.Contains(text, path) {
		t.Fatalf("got %v\n%s", err, text)
	}
}

// In a folder with no draft, the only system copy is the one.
func TestSystemForOnlyCopy(t *testing.T) {
	want := withSystemCopy(t, fixtureDeploymentID)
	path := filepath.Join(t.TempDir(), config.FileName)
	sys, d, id, err := systemFor(path, "")
	if err != nil {
		t.Fatal(problemText(err), err)
	}
	if id != fixtureDeploymentID || sys.Dir != want.Dir || d.Has() {
		t.Fatalf("got %s, %s", id, sys.Dir)
	}
}

// With several system copies and no draft, --id chooses; without it the
// refusal lists every id.
func TestSystemForNeedsIDWhenSeveral(t *testing.T) {
	withSystemCopy(t, fixtureDeploymentID)
	other := addSystemCopy(t, otherID)
	path := filepath.Join(t.TempDir(), config.FileName)
	_, _, _, err := systemFor(path, "")
	if text := problemText(err); err == nil || !strings.Contains(text, fixtureDeploymentID) || !strings.Contains(text, otherID) || !strings.Contains(text, "--id") {
		t.Fatalf("got %v\n%s", err, text)
	}
	sys, _, id, err := systemFor(path, otherID)
	if err != nil || id != otherID || sys.Dir != other.Dir {
		t.Fatalf("--id: %s, %s, %v", id, sys.Dir, err)
	}
}

// A draft of a deployment with no system copy here is refused, naming the
// recovery, and nothing is written: not the system copy, not the folder.
// With no draft and nothing on the workstation, or --id naming nothing held,
// the same.
func TestSystemForRefusesMissingCopy(t *testing.T) {
	withDataDir(t)
	base := dataDir(t)
	path := fixtureDraft(t, fixtureDeploymentID)
	empty := filepath.Join(t.TempDir(), config.FileName)
	for name, tc := range map[string]struct{ path, id string }{
		"draft":    {path, ""},
		"none":     {empty, ""},
		"--id":     {empty, fixtureDeploymentID},
		"bad --id": {empty, "../x"},
	} {
		before := tree(t, filepath.Dir(tc.path))
		_, _, _, err := systemFor(tc.path, tc.id)
		text := problemText(err)
		if err == nil || (name != "bad --id" && !strings.Contains(text, "deployment recover")) {
			t.Errorf("%s: %v\n%s", name, err, text)
		}
		if entries, _ := os.ReadDir(base); len(entries) != 0 {
			t.Errorf("%s: wrote %v into the data directory", name, entries)
		}
		if after := tree(t, filepath.Dir(tc.path)); len(after) != len(before) {
			t.Errorf("%s: the folder changed", name)
		}
	}
}

// A deployment directory that holds no store and no head is no system
// copy, and neither is what taking the lock leaves: a draft of it is
// refused as one with none, and deployment list shows nothing.
func TestSystemForIgnoresWhatIsNoCopy(t *testing.T) {
	withDataDir(t)
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	path := fixtureDraft(t, fixtureDeploymentID)
	unlock, err := st.Lock(fixtureDeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	check := func(what string) {
		t.Helper()
		_, _, _, err := systemFor(path, "")
		if text := problemText(err); err == nil || !strings.Contains(text, "deployment recover") {
			t.Errorf("%s: %v\n%s", what, err, text)
		}
		if listed, err := st.List(); len(listed) != 0 || err != nil {
			t.Errorf("%s: List = %v, %v", what, listed, err)
		}
	}
	check("after a lock")
	if _, err := st.Local(fixtureDeploymentID); err != nil {
		t.Fatal(err)
	}
	check("an empty directory")
}

// A draft started from the system copy is its head's files, with the line
// naming that head's revision and hash, and nothing else in the folder.
func TestStartDraftWritesMarker(t *testing.T) {
	c := newSavedCopy(t)
	d := draft.Draft{Dir: filepath.Join(t.TempDir(), "work")}
	if err := startDraft(c.l, d); err != nil {
		t.Fatal(problemText(err), err)
	}
	got, err := d.Files()
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := draft.ReadMarker(got.Paisans); !ok || m != (draft.Marker{Revision: 1, SHA: c.shas[0]}) {
		t.Errorf("marker %v, %v", m, ok)
	}
	got.Paisans, got.Blueprints = draft.StripMarker(got.Paisans), c.files[0].Blueprints
	if !got.Equal(c.files[0]) {
		t.Error("the draft is not the head's files")
	}
	entries, _ := os.ReadDir(d.Dir)
	if len(entries) != 3 {
		t.Errorf("the folder holds %v", entries)
	}

	empty := withSystemCopy(t, otherID)
	err = startDraft(empty, draft.Draft{Dir: t.TempDir()})
	if text := problemText(err); err == nil || !strings.Contains(text, "deployment recover") {
		t.Errorf("a system copy with no head: %v\n%s", err, text)
	}
}

// ensureDraft starts a draft in a folder with none and leaves one that is
// there as it is.
func TestEnsureDraftLeavesExistingDraft(t *testing.T) {
	c := newSavedCopy(t)
	path := fixtureDraft(t, fixtureDeploymentID)
	before, _ := os.ReadFile(path)
	d, err := ensureDraft(path, "")
	if err != nil {
		t.Fatal(problemText(err), err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, before) || d.Dir != filepath.Dir(path) {
		t.Fatal("the draft was rewritten")
	}
	if _, err := os.Stat(filepath.Join(d.Dir, "secrets.enc.yaml")); err == nil {
		t.Error("files were added to the draft")
	}

	empty := filepath.Join(t.TempDir(), config.FileName)
	d, err = ensureDraft(empty, "")
	if err != nil {
		t.Fatal(problemText(err), err)
	}
	got, err := d.Files()
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := draft.ReadMarker(got.Paisans); !ok || m.SHA != c.shas[0] {
		t.Errorf("marker %v, %v", m, ok)
	}

	// Part of a draft, without its paisans-stack.yaml, is refused rather
	// than replaced.
	partial := t.TempDir()
	os.WriteFile(filepath.Join(partial, "secrets.enc.yaml"), []byte("mine\n"), 0o600)
	_, err = ensureDraft(filepath.Join(partial, config.FileName), "")
	if text := problemText(err); err == nil || !strings.Contains(text, "secrets.enc.yaml") {
		t.Errorf("partial draft: %v\n%s", err, text)
	}
	if b, _ := os.ReadFile(filepath.Join(partial, "secrets.enc.yaml")); string(b) != "mine\n" {
		t.Error("the partial draft was replaced")
	}
}
