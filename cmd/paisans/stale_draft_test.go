package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/draft"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// staleFixture is a two-revision history whose newest version arrived at
// arrived, and a draft folder whose files were saved at saved.
func staleFixture(t *testing.T, arrived, saved time.Time) (configstore.Local, configstore.Gathered, draft.Draft) {
	t.Helper()
	sys, g, head := historyOf(t, revision{1, "a: 1\n", noSecrets}, revision{2, "a: 2\n", noSecrets})
	g.Verdict = configstore.Verdict{Trusted: map[string]bool{head: true}, Heads: []string{head}}
	if err := sys.RecordArrival(g, arrived); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range []string{config.FileName, configstore.FileSecrets, configstore.FileSOPS} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, saved, saved); err != nil {
			t.Fatal(err)
		}
	}
	return sys, g, draft.Draft{Dir: dir}
}

func TestStaleDraftWarns(t *testing.T) {
	arrived := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	sys, g, d := staleFixture(t, arrived, arrived.Add(-time.Hour))
	got := staleDraftWarning(sys, g, d, []byte("x\n"))
	want := "This draft was last saved before revision 2 reached this workstation; it may not include revision 2's changes."
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestFreshDraftDoesNotWarn(t *testing.T) {
	arrived := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	sys, g, d := staleFixture(t, arrived, arrived.Add(time.Hour))
	if got := staleDraftWarning(sys, g, d, []byte("x\n")); got != "" {
		t.Fatalf("warned %q", got)
	}
	// One older file is enough to warn; a version with no record never does.
	old := arrived.Add(-time.Minute)
	if err := os.Chtimes(filepath.Join(d.Dir, ".sops.yaml"), old, old); err != nil {
		t.Fatal(err)
	}
	if got := staleDraftWarning(sys, g, d, []byte("x\n")); !strings.Contains(got, "revision 2") {
		t.Fatalf("one stale file: %q", got)
	}
	if got := staleDraftWarning(configstore.Local{Dir: t.TempDir()}, g, d, []byte("x\n")); got != "" {
		t.Fatalf("no record warned %q", got)
	}
}

// A draft whose "drafted from" line names a trusted version is never warned
// about, whatever its other files' times.
func TestMarkedDraftNamingATrustedVersionDoesNotWarn(t *testing.T) {
	arrived := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	sys, g, d := staleFixture(t, arrived, arrived.Add(-time.Hour))
	head := g.Verdict.Heads[0]
	marked := []byte("# paisans: drafted from revision 2 (" + head + ")\na: 2\n")
	if got := staleDraftWarning(sys, g, d, marked); got != "" {
		t.Fatalf("warned %q", got)
	}
	unknown := []byte("# paisans: drafted from revision 1 (" + strings.Repeat("d", 40) + ")\na: 1\n")
	if got := staleDraftWarning(sys, g, d, unknown); got == "" {
		t.Fatal("a line naming no trusted version did not warn")
	}
}

// The first recorded arrival stands, whichever order lines are read in.
func TestArrivalKeepsTheFirstLine(t *testing.T) {
	sys, g, _ := staleFixture(t, time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC), time.Now())
	head := g.Verdict.Heads[0]
	later := time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)
	f, err := os.OpenFile(filepath.Join(sys.Dir, ".paisans", "arrivals"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(head + " " + later.Format(time.RFC3339) + "\n")
	f.Close()
	if at, _ := sys.ArrivedAt(head); at.Day() != 2 {
		t.Fatalf("got %v", at)
	}
}

// Apply prints the stale-draft warning once, for a draft with no "drafted
// from" line saved before the head arrived, and not for a marked one.
func TestApplyWarnsOnceAboutAStaleMarkerlessDraft(t *testing.T) {
	for _, marked := range []bool{false, true} {
		f := newApplyStore(t, 1)
		f.based(t)
		if !marked {
			f.marker = nil
			if err := f.write(f.files[0]); err != nil {
				t.Fatal(err)
			}
		}
		g := configstore.Gathered{Verdict: configstore.Verdict{Trusted: map[string]bool{f.shas[0]: true}}}
		if err := f.sys.RecordArrival(g, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		cfg, paisans, err := config.LoadWithBytes(f.cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		secrets, _ := os.ReadFile(filepath.Join(f.dir, "secrets.enc.yaml"))
		rec := &ui.Recorder{}
		loaded := configstore.Files{Paisans: paisans, Secrets: secrets}
		if _, err := gatherForApply(rec, f.cfgPath, "", &loaded, cfg, false, answerer{}, noTerminalStdin(), io.Discard); err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, e := range rec.Events {
			if e.Kind == "warn" && strings.HasPrefix(e.Text, "This draft was last saved before revision 1") {
				n++
			}
		}
		if want := map[bool]int{false: 1, true: 0}[marked]; n != want {
			t.Errorf("marked=%v: %d warnings, want %d\n%s", marked, n, want, rec.Lines())
		}
	}
}
