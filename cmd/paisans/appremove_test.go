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
// name in the newest version of the workstation's configuration history that
// declares the app, which is its display_name when that version sets one.
// Without one, it is the name the current configuration gives, and a
// recorded client makes that a warning.
func TestRemovedClientNameIsTheLastDeclaredOne(t *testing.T) {
	key, pub := testKey(t)
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	rec := &ui.Recorder{}
	name, from, err := removedClientName(rec, fixtureConfig(), cfg, "talk", "c-1")
	if name != "talk" || !strings.Contains(from, "holds no configuration history") || err != nil {
		t.Errorf("with no store: %q, %q, %v", name, from, err)
	}
	if rec.Index("warn", "the name the current configuration gives it") < 0 {
		t.Errorf("no warning with no store: %+v", rec.Events)
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
	rec = &ui.Recorder{}
	name, from, err = removedClientName(rec, path, cfg, "talk", "c-1")
	if name != "Test Talk" || !strings.Contains(from, sha[:12]) || err != nil {
		t.Errorf("talk's client: %q, %q, %v", name, from, err)
	}
	if len(rec.Events) != 0 {
		t.Errorf("warned with a declaring version: %+v", rec.Events)
	}
	name, _, err = removedClientName(rec, path, cfg, "gone", "")
	if name != "gone" || err != nil {
		t.Errorf("an app no version declares: %q, %v", name, err)
	}
	if len(rec.Events) != 0 {
		t.Errorf("warned with no client recorded: %+v", rec.Events)
	}
}
