package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/localstore"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// savedCopy is a saved copy of the fixture deployment as an earlier apply
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
	st := ownStore(t)
	shas, answer, files, key, pub := storedHistoryKey(t, 2, 0)
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

// checkout runs deployment checkout of what into dir.
func checkout(dir, what string, more ...string) error {
	return runDeployment(append([]string{"checkout", what, "--config", filepath.Join(dir, config.FileName)}, more...), noTerminalStdin(), &bytes.Buffer{})
}

// A checkout is a workstation in its own right: the saved copy's files and
// trust state, nothing else, and log, pull and publish work on it from the
// key it carries, with no root pinned.
func TestCheckoutMakesAWorkstation(t *testing.T) {
	c := newSavedCopy(t)
	dir := filepath.Join(t.TempDir(), "work")
	if err := checkout(dir, fixtureID(t), "--execute"); err != nil {
		t.Fatal(problemText(err))
	}
	l := configstore.Local{Dir: dir}
	if got, err := l.Files(); err != nil || !got.Equal(c.files[0]) {
		t.Fatalf("files: %v", err)
	}
	for _, name := range []string{config.FileName, "secrets.enc.yaml", ".sops.yaml", ".paisans/signing-key", ".paisans/config-head", ".paisans/config-seen"} {
		want, _ := os.ReadFile(filepath.Join(c.l.Dir, name))
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s differs from the saved copy's: %v", name, err)
		}
		wi, _ := os.Stat(filepath.Join(c.l.Dir, name))
		gi, err := os.Stat(filepath.Join(dir, name))
		if err != nil || gi.Mode().Perm() != wi.Mode().Perm() {
			t.Errorf("%s: mode %v, the saved copy's %v", name, gi.Mode().Perm(), wi.Mode().Perm())
		}
	}
	if fi, _ := os.Stat(filepath.Join(dir, "secrets.enc.yaml")); fi.Mode().Perm() != 0o600 {
		t.Errorf("secrets.enc.yaml is %v", fi.Mode().Perm())
	}
	for _, name := range []string{"saved.json", ".paisans/incoming", ".paisans/config-root"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s was copied", name)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, ".paisans", ".gitignore")); err != nil || !strings.Contains(string(b), "signing-key") {
		t.Errorf(".paisans/.gitignore: %q, %v", b, err)
	}
	if !strings.Contains(c.report.String(), "deployment pull") {
		t.Errorf("no pull suggested:\n%s", c.report.String())
	}

	cfg := filepath.Join(dir, config.FileName)
	var out bytes.Buffer
	if err := runDeployment([]string{"log", "--config", cfg}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err))
	}
	if !strings.Contains(out.String(), c.shas[1][:12]) || strings.Contains(out.String(), "not trusted") {
		t.Fatalf("log:\n%s", out.String())
	}
	if err := runDeployment([]string{"pull", "--config", cfg, "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err))
	}
	if h, _ := l.Head(); h != c.shas[1] {
		t.Fatalf("pull left the head at %s", h)
	}
	savedKeys, savedSign := agentKeys, configSignWith
	agentKeys = func() []string { return []string{c.pub} }
	configSignWith = func(string) string { return c.key }
	t.Cleanup(func() { agentKeys, configSignWith = savedKeys, savedSign })
	edited := c.files[1]
	edited.Paisans = append(append([]byte{}, edited.Paisans...), "# next\n"...)
	if err := l.WriteFiles(edited); err != nil {
		t.Fatal(err)
	}
	if err := runDeployment([]string{"publish", "--config", cfg, "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err))
	}
	if h, _ := l.Head(); h == c.shas[1] || h == "" {
		t.Fatalf("publish left the head at %q", h)
	}
	if r, _ := l.Root(); r != "" {
		t.Errorf("a root was pinned: %s", r)
	}
	if h, _ := c.l.Head(); h != c.shas[0] {
		t.Errorf("the saved copy moved to %s", h)
	}
}

// A pinned root is carried with the rest of the trust state.
func TestCheckoutCarriesThePinnedRoot(t *testing.T) {
	c := newSavedCopy(t)
	if err := c.l.SetRoot(c.shas[0]); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := checkout(dir, fixtureID(t), "--execute"); err != nil {
		t.Fatal(problemText(err))
	}
	if r, _ := (configstore.Local{Dir: dir}).Root(); r != c.shas[0] {
		t.Errorf("root %q", r)
	}
}

// Without --execute nothing is written, and the run names every path it
// would write in full, and the version.
func TestCheckoutDryRunWritesNothing(t *testing.T) {
	c := newSavedCopy(t)
	dir, id := t.TempDir(), fixtureID(t)
	t.Chdir(dir)
	if err := runDeployment([]string{"checkout", id}, noTerminalStdin(), &bytes.Buffer{}); err != nil {
		t.Fatal(problemText(err))
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a dry run wrote %v", entries)
	}
	abs, _ := os.Getwd()
	out := c.report.String()
	for _, want := range []string{"revision 1", c.shas[0][:12], filepath.Join(abs, config.FileName), filepath.Join(abs, ".paisans", "store.git"), filepath.Join(abs, ".paisans", "signing-key"), "--execute"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
}

// The deployment is found by its id, a unique prefix of it, or its domain.
func TestCheckoutFindsByPrefixAndDomain(t *testing.T) {
	newSavedCopy(t)
	for _, what := range []string{fixtureID(t)[:4], "example.org"} {
		dir := t.TempDir()
		if err := checkout(dir, what, "--execute"); err != nil {
			t.Fatalf("%s: %s", what, problemText(err))
		}
		if !(configstore.Local{Dir: dir}).HasFiles() {
			t.Errorf("%s: nothing checked out", what)
		}
	}
}

// An unknown or ambiguous id names deployment list; a non-empty target is
// refused as recover refuses it; an unverified or forked copy names the
// recovery that pins a root. None writes anything.
func TestCheckoutRefusals(t *testing.T) {
	c := newSavedCopy(t)
	const other = "f2a90000-0b7d-4c3a-9e2f-5a6b7c8d9e01"
	if err := c.st.Record(localstore.Saved{ID: other, Domain: "example.org", SHA: c.shas[0], Revision: 1, Status: localstore.Verified}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ what, want string }{
		{"0000", "deployment list"},
		{"f2a9", "more than one"},
		{"example.org", "more than one"},
	} {
		dir := t.TempDir()
		err := checkout(dir, tc.what, "--execute")
		if text := problemText(err); err == nil || !strings.Contains(text, tc.want) {
			t.Errorf("%s: %v\n%s", tc.what, err, text)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s: wrote %v", tc.what, entries)
		}
	}

	for _, existing := range []string{config.FileName, ".paisans/signing-key", ".paisans/store.git"} {
		dir := t.TempDir()
		path := filepath.Join(dir, existing)
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte("mine\n"), 0o644)
		err := checkout(dir, fixtureID(t), "--execute")
		if text := problemText(err); err == nil || !strings.Contains(text, "already") {
			t.Errorf("%s present: %v\n%s", existing, err, text)
		}
		if b, _ := os.ReadFile(path); string(b) != "mine\n" {
			t.Errorf("%s was overwritten", existing)
		}
		if (configstore.Local{Dir: dir}).HasFiles() != (existing == config.FileName) {
			t.Errorf("%s present: files were written", existing)
		}
	}

	for _, status := range []localstore.Status{localstore.Unverified, localstore.Fork} {
		if err := c.st.Record(localstore.Saved{ID: fixtureID(t), Domain: "example.org", SHA: c.shas[1], Revision: 2, Status: status}); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		err := checkout(dir, fixtureID(t), "--execute")
		if text := problemText(err); err == nil || !strings.Contains(text, string(status)) || !strings.Contains(text, "deployment recover") || !strings.Contains(text, "--root") {
			t.Errorf("%s: %v\n%s", status, err, text)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s: wrote %v", status, entries)
		}
	}
}

// A saved copy whose head the trust rules do not reach from its own anchor
// is not checked out: the checkout would carry a trust it does not have.
func TestCheckoutRefusesACopyItsAnchorDoesNotTrust(t *testing.T) {
	c := newSavedCopy(t)
	_, other := testKey(t)
	if err := c.l.SetSigningKey(other); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	err := checkout(dir, fixtureID(t), "--execute")
	if text := problemText(err); err == nil || !strings.Contains(text, "--root") {
		t.Fatalf("got %v\n%s", err, text)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("wrote %v", entries)
	}
}

// A saved copy whose files are not the version it records is refused, so
// a checkout's files are always a version its store holds.
func TestCheckoutRefusesEditedSavedFiles(t *testing.T) {
	c := newSavedCopy(t)
	edited := c.files[0]
	edited.Paisans = append(append([]byte{}, edited.Paisans...), "# local\n"...)
	if err := c.l.WriteFiles(edited); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	err := checkout(dir, fixtureID(t), "--execute")
	if text := problemText(err); err == nil || !strings.Contains(text, "deployment fetch") {
		t.Fatalf("got %v\n%s", err, text)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("wrote %v", entries)
	}
}
