package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/draft"
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
	got := staleDraftWarning(sys, g, d)
	want := "This draft was last saved before revision 2 reached this workstation; it may not include revision 2's changes."
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestFreshDraftDoesNotWarn(t *testing.T) {
	arrived := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	sys, g, d := staleFixture(t, arrived, arrived.Add(time.Hour))
	if got := staleDraftWarning(sys, g, d); got != "" {
		t.Fatalf("warned %q", got)
	}
	// One older file is enough to warn; a version with no record never does.
	old := arrived.Add(-time.Minute)
	if err := os.Chtimes(filepath.Join(d.Dir, ".sops.yaml"), old, old); err != nil {
		t.Fatal(err)
	}
	if got := staleDraftWarning(sys, g, d); !strings.Contains(got, "revision 2") {
		t.Fatalf("one stale file: %q", got)
	}
	if got := staleDraftWarning(configstore.Local{Dir: t.TempDir()}, g, d); got != "" {
		t.Fatalf("no record warned %q", got)
	}
}
