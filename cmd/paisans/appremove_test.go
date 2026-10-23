package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/appremove"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// noHosts fails the test if `app remove` reaches for any host.
func noHosts(t *testing.T) {
	t.Helper()
	saved := removeHost
	removeHost = func(string, config.Site) appremove.Host {
		t.Fatal("a host was reached")
		return nil
	}
	t.Cleanup(func() { removeHost = saved })
}

func TestAppRemoveRefusesADeclaredApp(t *testing.T) {
	noHosts(t)
	err := runAppRemove([]string{"talk", "--config", fixtureConfig()}, strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "still in paisans-stack.yaml") {
		t.Errorf("err = %v", err)
	}
	err = runAppRemove([]string{"--config", fixtureConfig(), "infra"}, strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "infrastructure stack") {
		t.Errorf("err = %v", err)
	}
}

// Deleting member data needs a person at a terminal, or --yes. Refused
// before any host is read.
func TestDeleteDataNeedsATerminal(t *testing.T) {
	noHosts(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString("old\n")
	w.Close()
	defer r.Close()
	err = runAppRemove([]string{"old", "--config", fixtureConfig(), "--delete-data", "--execute"}, r, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "stdin is not one") {
		t.Errorf("err = %v", err)
	}
}

func TestConfirmNameWantsTheAppsName(t *testing.T) {
	saved := isTerminal
	isTerminal = func(*os.File) bool { return true }
	t.Cleanup(func() { isTerminal = saved })
	for answer, ok := range map[string]bool{"docs\n": true, "yes\n": false, "\n": false, "": false} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		w.WriteString(answer)
		w.Close()
		err = confirmName(answerer{}, r, &bytes.Buffer{}, "docs")
		r.Close()
		if (err == nil) != ok {
			t.Errorf("answer %q: %v", answer, err)
		}
	}
	isTerminal = func(*os.File) bool { return false }
	if err := confirmName(answerer{}, os.Stdin, &bytes.Buffer{}, "docs"); err == nil {
		t.Error("a name was accepted from something that is not a terminal")
	}
}

// A removal's leftovers name their object after the hint (which client,
// which path), and after site remove --execute the configuration no longer
// declares the site, so the run cannot be repeated with --verbose to learn
// it. The whole line shows by default.
func TestRemainsShowTheirObjectWithoutVerbose(t *testing.T) {
	var b strings.Builder
	reportRemains(ui.NewPlain(&b, false), []string{"a client kept; delete it in Pocket ID if it served only talk. It is talk (id 0123) at pocket-id on home-a: unrecorded"})
	want := "  WARN a client kept; delete it in Pocket ID if it served only talk\n       It is talk (id 0123) at pocket-id on home-a: unrecorded\n"
	if b.String() != want {
		t.Errorf("got\n%q\nwant\n%q", b.String(), want)
	}
}

// The client of an app gone from paisans-stack.yaml is looked for by the
// name apply last gave it, which is its display_name when the version it was
// last applied from sets one.
func TestRemovedClientNameIsTheLastAppliedOne(t *testing.T) {
	key, pub := testKey(t)
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	// With no store to read, the name is the one the configuration gives.
	if name, err := removedClientName(fixtureConfig(), cfg, "talk"); name != "talk" || err != nil {
		t.Errorf("with no store: %q, %v", name, err)
	}
	body := strings.Replace(fixtureAs(t, pub), "    blueprint: mbin\n", "    blueprint: mbin\n    display_name: Test Talk\n", 1)
	files := snapshotted(configstore.Files{Paisans: []byte(body), Secrets: []byte("s\n"), SOPS: []byte("creation_rules: []\n")})
	l := configstore.Local{Dir: t.TempDir()}
	if err := l.WriteFiles(files); err != nil {
		t.Fatal(err)
	}
	cache, err := l.Cache()
	if err != nil {
		t.Fatal(err)
	}
	sha, err := cache.Commit(files, nil, 1, key, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.SetHead(sha); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(l.Dir, config.FileName)
	if name, err := removedClientName(path, cfg, "talk"); name != "Test Talk" || err != nil {
		t.Errorf("talk's client: %q, %v", name, err)
	}
	if name, err := removedClientName(path, cfg, "gone"); name != "gone" || err != nil {
		t.Errorf("an app no version declares: %q, %v", name, err)
	}
}
