package lease

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/deployment"
)

var fixture = deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}

// shellSite runs each command in sh with the deployment's root moved into a
// directory of the test's, so the lease's own shell is what is tested. flock
// is a stand-in that takes nothing, since one test drives one site at a time
// and macOS has none.
type shellSite struct {
	root string
	bin  string
	down bool
	mu   sync.Mutex
	runs int
}

func newSite(t *testing.T) *shellSite {
	t.Helper()
	s := &shellSite{root: t.TempDir(), bin: t.TempDir()}
	if err := os.WriteFile(filepath.Join(s.bin, "flock"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *shellSite) Run(command string) (string, error) {
	s.mu.Lock()
	s.runs++
	s.mu.Unlock()
	if s.down {
		return "", errors.New("ssh: connect to host port 22: Connection refused")
	}
	command = strings.ReplaceAll(command, fixture.Root(), s.root)
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+s.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (s *shellSite) lease(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.root, ".paisans", "lease"))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func holder(t *testing.T, who string) Holder {
	t.Helper()
	h, err := NewHolder(who, "deployment apply")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestALeaseIsTakenOnEverySiteAndReleased(t *testing.T) {
	a, b := newSite(t), newSite(t)
	h := holder(t, "alice@laptop")
	l, unreached, err := Take(map[string]Runner{"home-a": a, "home-b": b}, fixture, h, false)
	if err != nil || len(unreached) != 0 {
		t.Fatalf("take: %v, %v", err, unreached)
	}
	for name, s := range map[string]*shellSite{"home-a": a, "home-b": b} {
		got := s.lease(t)
		if !strings.Contains(got, "id="+h.ID+"\n") || !strings.Contains(got, "who=alice@laptop\n") || !strings.Contains(got, "command=deployment apply\n") {
			t.Errorf("%s holds %q", name, got)
		}
	}
	if errs := l.Release(); len(errs) != 0 {
		t.Fatal(errs)
	}
	if a.lease(t) != "" || b.lease(t) != "" {
		t.Fatal("release left a lease")
	}
}

// A second command finds the first's live lease and is refused, naming who
// holds it, and leaves the first's lease where it was on every site,
// including a site it took before it found the other.
func TestASecondCommandIsRefusedWhileTheFirstHoldsIt(t *testing.T) {
	a, b := newSite(t), newSite(t)
	first := holder(t, "alice@laptop")
	l, _, err := Take(map[string]Runner{"home-a": a}, fixture, first, false)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	second := holder(t, "bob@desk")
	_, _, err = Take(map[string]Runner{"home-a": a, "home-b": b}, fixture, second, false)
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("a second take gives %v", err)
	}
	if held.Other.Site != "home-a" || held.Other.Who != "alice@laptop" || held.Other.Command != "deployment apply" {
		t.Errorf("the refusal names %+v", held.Other)
	}
	if !strings.Contains(a.lease(t), "id="+first.ID) {
		t.Error("the refusal changed the holder's lease")
	}
	if b.lease(t) != "" {
		t.Errorf("the refused take left its lease on home-b: %q", b.lease(t))
	}
}

// A lease nobody renewed for TTL is expired, and the next command takes
// it without asking.
func TestAnExpiredLeaseIsTaken(t *testing.T) {
	a := newSite(t)
	if err := os.MkdirAll(filepath.Join(a.root, ".paisans"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := "id=dead\nwho=alice@laptop\ncommand=deployment apply\nstarted=1\nrenewed=1\n"
	if err := os.WriteFile(filepath.Join(a.root, ".paisans", "lease"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	h := holder(t, "bob@desk")
	l, _, err := Take(map[string]Runner{"home-a": a}, fixture, h, false)
	if err != nil {
		t.Fatalf("an expired lease refuses: %v", err)
	}
	defer l.Release()
	if !strings.Contains(a.lease(t), "id="+h.ID) {
		t.Fatalf("the expired lease was not replaced: %q", a.lease(t))
	}
}

// takeover takes a live lease, and the first holder's renewal then finds
// it lost; its release leaves the new holder's lease alone.
func TestATakeoverIsSeenByTheFirstHolder(t *testing.T) {
	saved := Heartbeat
	Heartbeat = 20 * time.Millisecond
	t.Cleanup(func() { Heartbeat = saved })
	a := newSite(t)
	first := holder(t, "alice@laptop")
	l, _, err := Take(map[string]Runner{"home-a": a}, fixture, first, false)
	if err != nil {
		t.Fatal(err)
	}
	second := holder(t, "bob@desk")
	l2, _, err := Take(map[string]Runner{"home-a": a}, fixture, second, true)
	if err != nil {
		t.Fatalf("a takeover refuses: %v", err)
	}
	defer l2.Release()
	deadline := time.Now().Add(5 * time.Second)
	for l.Lost() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !errors.Is(l.Lost(), ErrLost) || !strings.Contains(l.Lost().Error(), "bob@desk") {
		t.Fatalf("the first holder's lease reads %v", l.Lost())
	}
	l.Release()
	if !strings.Contains(a.lease(t), "id="+second.ID) {
		t.Fatalf("the first holder's release removed the second's lease: %q", a.lease(t))
	}
}

// Renewal keeps when the lease was first taken and moves when it was last
// renewed.
func TestARenewalKeepsWhenItWasTaken(t *testing.T) {
	a := newSite(t)
	h := holder(t, "alice@laptop")
	if err := take(a, "home-a", fixture, h, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.root, ".paisans", "lease")
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), "started=", "started=1", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := take(a, "home-a", fixture, h, false); err != nil {
		t.Fatal(err)
	}
	if got := a.lease(t); !strings.Contains(got, "\nstarted=1") {
		t.Fatalf("a renewal reset when it was taken: %q", got)
	}
}

// A site that cannot be reached is left out and named, so a down site never
// blocks a run; no site at all is an error.
func TestAnUnreachedSiteIsLeftOut(t *testing.T) {
	a, b := newSite(t), newSite(t)
	b.down = true
	l, unreached, err := Take(map[string]Runner{"home-a": a, "home-b": b}, fixture, holder(t, "alice@laptop"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if _, ok := unreached["home-b"]; !ok || len(unreached) != 1 || !l.Covers("home-a") || l.Covers("home-b") {
		t.Fatalf("unreached %v", unreached)
	}
	a.down = true
	if _, _, err := Take(map[string]Runner{"home-a": a, "home-b": b}, fixture, holder(t, "bob@desk"), false); err == nil {
		t.Fatal("a take no site answered succeeds")
	}
}

// A name with a quote or a newline in it reaches the file as one line.
func TestAHoldersNameIsOneLine(t *testing.T) {
	a := newSite(t)
	h := holder(t, "o'brien\n@laptop")
	if err := take(a, "home-a", fixture, h, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.lease(t), "who=o'brien @laptop\n") {
		t.Fatalf("lease reads %q", a.lease(t))
	}
}
