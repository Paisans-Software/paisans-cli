package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/draft"
	"github.com/paisans-software/paisans-cli/internal/localstore"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// savedCopy is the fixture deployment's system copy as an earlier apply
// left it: every version of a two-version history in its store, the first
// checked out, the key that signed both recorded as its anchor. The sites
// answer with the whole history, so the copy is one version behind them.
type savedCopy struct {
	st     localstore.Store
	l      configstore.Local
	shas   []string
	files  []configstore.Files
	answer string
	key    string
	pub    string
	report *bytes.Buffer
}

func newSavedCopy(t *testing.T) savedCopy {
	t.Helper()
	l := withSystemCopy(t, fixtureID(t))
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	shas, answer, files, key, pub := storedHistoryKey(t, 2, 0)
	cache, err := l.Cache()
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.FetchBundle(mustBundle(t, answer)); err != nil {
		t.Fatal(err)
	}
	if err := l.Checkout(shas[0]); err != nil {
		t.Fatal(err)
	}
	if err := l.SetSigningKey(pub); err != nil {
		t.Fatal(err)
	}
	if err := l.Remember(1, shas[0]); err != nil {
		t.Fatal(err)
	}
	if err := st.Record(localstore.Saved{ID: fixtureID(t), Domain: "example.org", SHA: shas[0], Revision: 1, Status: localstore.Verified, Source: "test"}); err != nil {
		t.Fatal(err)
	}
	savedSite, savedR := configSiteTransport, reporterOverride
	t.Cleanup(func() { configSiteTransport, reporterOverride = savedSite, savedR })
	configSiteTransport = func(name string, _ config.Site) apply.Transport {
		return &storeHost{name: name, answer: answer}
	}
	var b bytes.Buffer
	reporterOverride = ui.NewPlain(&b, false)
	return savedCopy{st: st, l: l, shas: shas, files: files, answer: answer, key: key, pub: pub, report: &b}
}

// checkout runs deployment checkout into dir.
func checkout(dir string, more ...string) error {
	return runDeployment(append([]string{"checkout", "--config", filepath.Join(dir, config.FileName)}, more...), noTerminalStdin(), &bytes.Buffer{})
}

// A checkout starts a draft from the system copy: its head's files, with the
// line naming that head, and the system copy is left as it was.
func TestCheckoutStartsDraft(t *testing.T) {
	c := newSavedCopy(t)
	dir := filepath.Join(t.TempDir(), "work")
	if err := checkout(dir, "--execute"); err != nil {
		t.Fatal(problemText(err))
	}
	got, err := draft.Draft{Dir: dir}.Files()
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := draft.ReadMarker(got.Paisans); !ok || m != (draft.Marker{Revision: 1, SHA: c.shas[0]}) {
		t.Errorf("marker %v, %v", m, ok)
	}
	got.Paisans, got.Blueprints = draft.StripMarker(got.Paisans), c.files[0].Blueprints
	if !got.Equal(c.files[0]) {
		t.Error("the draft is not the system copy's files")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "secrets.enc.yaml")); fi.Mode().Perm() != 0o600 {
		t.Errorf("secrets.enc.yaml is %v", fi.Mode().Perm())
	}
	if h, _ := c.l.Head(); h != c.shas[0] {
		t.Errorf("the system copy moved to %s", h)
	}
	if !strings.Contains(c.report.String(), "deployment pull") {
		t.Errorf("no pull suggested:\n%s", c.report.String())
	}
	// --id names the deployment, as it does with several system copies.
	dir = t.TempDir()
	if err := checkout(dir, "--id", fixtureID(t), "--execute"); err != nil {
		t.Fatal(problemText(err))
	}
	if !(draft.Draft{Dir: dir}).Has() {
		t.Error("--id: nothing checked out")
	}
}

// --head writes a trusted head of the system copy's store, which need not be
// the system copy's own files, with the line naming it; a version that is
// not a trusted head is refused.
func TestCheckoutHeadWritesThatHead(t *testing.T) {
	c := newSavedCopy(t)
	dir := t.TempDir()
	if err := checkout(dir, "--head", c.shas[1], "--execute"); err != nil {
		t.Fatal(problemText(err))
	}
	got, err := draft.Draft{Dir: dir}.Files()
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := draft.ReadMarker(got.Paisans); !ok || m != (draft.Marker{Revision: 2, SHA: c.shas[1]}) {
		t.Errorf("marker %v, %v", m, ok)
	}
	got.Paisans, got.Blueprints = draft.StripMarker(got.Paisans), c.files[1].Blueprints
	if !got.Equal(c.files[1]) {
		t.Error("the draft is not the head's files")
	}
	if h, _ := c.l.Head(); h != c.shas[0] {
		t.Errorf("the system copy moved to %s", h)
	}
	for _, sha := range []string{c.shas[0], strings.Repeat("0", 40), "nonsense"} {
		dir := t.TempDir()
		err := checkout(dir, "--head", sha, "--execute")
		if text := problemText(err); err == nil || !strings.Contains(text, c.shas[1]) {
			t.Errorf("%s: %v\n%s", sha, err, text)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s: wrote %v", sha, entries)
		}
	}
}

// A folder that holds any of the three files is refused, and nothing in it
// is written.
func TestCheckoutRefusesExistingDraft(t *testing.T) {
	newSavedCopy(t)
	for _, existing := range []string{config.FileName, "secrets.enc.yaml", ".sops.yaml"} {
		dir := t.TempDir()
		path := filepath.Join(dir, existing)
		os.WriteFile(path, []byte("mine\n"), 0o644)
		err := checkout(dir, "--execute")
		if text := problemText(err); err == nil || !strings.Contains(text, "already") {
			t.Errorf("%s present: %v\n%s", existing, err, text)
		}
		if b, _ := os.ReadFile(path); string(b) != "mine\n" {
			t.Errorf("%s was overwritten", existing)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 1 {
			t.Errorf("%s present: wrote %v", existing, entries)
		}
	}
}

// No checkout writes .paisans/ into the folder: neither a dry run, nor
// --execute, nor --head.
func TestNoFolderStateFromCheckout(t *testing.T) {
	c := newSavedCopy(t)
	for _, more := range [][]string{nil, {"--execute"}, {"--head", c.shas[1], "--execute"}} {
		dir := t.TempDir()
		if err := checkout(dir, more...); err != nil {
			t.Fatal(problemText(err))
		}
		if _, err := os.Lstat(filepath.Join(dir, ".paisans")); err == nil {
			t.Errorf("%v: .paisans written", more)
		}
	}
}

// Without --execute nothing is written, and the run names every path it
// would write in full, and the version.
func TestCheckoutDryRunWritesNothing(t *testing.T) {
	c := newSavedCopy(t)
	dir := t.TempDir()
	t.Chdir(dir)
	if err := runDeployment([]string{"checkout"}, noTerminalStdin(), &bytes.Buffer{}); err != nil {
		t.Fatal(problemText(err))
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a dry run wrote %v", entries)
	}
	abs, _ := os.Getwd()
	out := c.report.String()
	for _, want := range []string{"revision 1", c.shas[0][:12], filepath.Join(abs, config.FileName), filepath.Join(abs, "secrets.enc.yaml"), "--execute"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
}

// tree is every path under dir with its mode, modification time and
// contents, to tell whether anything there was written.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
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

// Checking out reads the system copy and writes nothing to it, not even the
// .gitignore or the store's settings opening a workstation's store for
// writing would put back: neither a dry run nor --execute.
func TestCheckoutLeavesTheSavedCopyAsItWas(t *testing.T) {
	c := newSavedCopy(t)
	if err := os.Remove(filepath.Join(c.l.Dir, ".paisans", ".gitignore")); err != nil {
		t.Fatal(err)
	}
	before := tree(t, c.st.Base)
	for _, more := range [][]string{nil, {"--execute"}} {
		if err := checkout(t.TempDir(), more...); err != nil {
			t.Fatal(problemText(err))
		}
		after := tree(t, c.st.Base)
		for p, v := range after {
			if before[p] != v {
				t.Errorf("%v: %s was written", more, p)
			}
		}
		for p := range before {
			if _, ok := after[p]; !ok {
				t.Errorf("%v: %s was removed", more, p)
			}
		}
	}
}

// A system copy whose head the trust rules do not reach from its own anchor
// is not checked out: the checkout would carry a trust it does not have.
func TestCheckoutRefusesACopyItsAnchorDoesNotTrust(t *testing.T) {
	c := newSavedCopy(t)
	_, other := testKey(t)
	if err := c.l.SetSigningKey(other); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	err := checkout(dir, "--execute")
	if text := problemText(err); err == nil || !strings.Contains(text, "--root") {
		t.Fatalf("got %v\n%s", err, text)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("wrote %v", entries)
	}
}

// A system copy whose files are not the version it records is refused, so
// a checkout's files are always a version its store holds.
func TestCheckoutRefusesEditedSavedFiles(t *testing.T) {
	c := newSavedCopy(t)
	edited := c.files[0]
	edited.Paisans = append(append([]byte{}, edited.Paisans...), "# local\n"...)
	if err := c.l.WriteFiles(edited); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	err := checkout(dir, "--execute")
	if text := problemText(err); err == nil || !strings.Contains(text, "deployment fetch") {
		t.Fatalf("got %v\n%s", err, text)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("wrote %v", entries)
	}
}

// A system copy recorded as verified whose store holds a fork is refused,
// whatever its record says.
func TestCheckoutRefusesAStoreHoldingAFork(t *testing.T) {
	c := newSavedCopy(t)
	cache, err := c.l.Cache()
	if err != nil {
		t.Fatal(err)
	}
	other := c.files[1]
	other.Paisans = append(append([]byte{}, other.Paisans...), "# other\n"...)
	if _, err := cache.Commit(other, []string{c.shas[0]}, 2, c.key, time.Unix(1_800_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	err = checkout(dir, "--execute")
	if text := problemText(err); err == nil || !strings.Contains(text, "fork") || !strings.Contains(text, "--root") {
		t.Fatalf("got %v\n%s", err, text)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("wrote %v", entries)
	}
}

// A system copy recorded as unverified or forked names the recovery that pins
// a root, and writes nothing.
func TestCheckoutRefusesUnverifiedAndFork(t *testing.T) {
	c := newSavedCopy(t)
	for _, status := range []localstore.Status{localstore.Unverified, localstore.Fork} {
		if err := c.st.Record(localstore.Saved{ID: fixtureID(t), Domain: "example.org", SHA: c.shas[1], Revision: 2, Status: status}); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		err := checkout(dir, "--execute")
		if text := problemText(err); err == nil || !strings.Contains(text, string(status)) || !strings.Contains(text, "deployment recover") || !strings.Contains(text, "--root") {
			t.Errorf("%s: %v\n%s", status, err, text)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s: wrote %v", status, entries)
		}
	}
}

// --head naming a version the store holds but does not trust, signed by a
// key no operator lists, is refused, and nothing is written.
func TestCheckoutHeadRefusesAnUntrustedVersion(t *testing.T) {
	c := newSavedCopy(t)
	cache, err := c.l.Cache()
	if err != nil {
		t.Fatal(err)
	}
	stranger, _ := testKey(t)
	next := c.files[1]
	next.Paisans = append(append([]byte{}, next.Paisans...), "# stranger\n"...)
	sha, err := cache.Commit(next, []string{c.shas[1]}, 3, stranger, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	err = checkout(dir, "--head", sha, "--execute")
	if text := problemText(err); err == nil || !strings.Contains(text, "not a trusted head") || !strings.Contains(text, c.shas[1]) {
		t.Fatalf("got %v\n%s", err, text)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("wrote %v", entries)
	}
}
