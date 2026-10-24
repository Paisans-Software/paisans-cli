package localstore

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The data directory follows the platform: Application Support on macOS,
// the XDG data home elsewhere, and PAISANS_DATA_DIR over either.
func TestBase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(DirVariable, "")
	t.Setenv("XDG_DATA_HOME", "")
	got, err := Base()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "share", "paisans")
	if runtime.GOOS == "darwin" {
		want = filepath.Join(home, "Library", "Application Support", "Paisans")
	}
	if got != want {
		t.Errorf("Base() = %q, want %q", got, want)
	}

	if runtime.GOOS != "darwin" {
		xdg := t.TempDir()
		t.Setenv("XDG_DATA_HOME", xdg)
		if got, _ := Base(); got != filepath.Join(xdg, "paisans") {
			t.Errorf("with XDG_DATA_HOME: %q", got)
		}
		t.Setenv("XDG_DATA_HOME", "relative")
		if got, _ := Base(); got != want {
			t.Errorf("a relative XDG_DATA_HOME was used: %q", got)
		}
	}

	own := t.TempDir()
	t.Setenv(DirVariable, own)
	if got, _ := Base(); got != own {
		t.Errorf("with %s: %q", DirVariable, got)
	}
}

// A deployment's directory is named by a checked id, and private to the
// operator.
func TestLocal(t *testing.T) {
	s := Store{Base: t.TempDir()}
	for _, bad := range []string{"", "..", "../x", "a/b", "f2a9"} {
		if _, err := s.Local(bad); err == nil {
			t.Errorf("%q was taken for an id", bad)
		}
	}
	id := "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"
	l, err := s.Local(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Dir(l.Dir), l.Dir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("%s is %v", dir, info.Mode().Perm())
		}
	}
}

// A record reads back, and list shows every directory, one without a
// readable record too.
func TestRecordAndList(t *testing.T) {
	s := Store{Base: t.TempDir()}
	a := Saved{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01", Domain: "example.org", SHA: "0123456789abcdef0123456789abcdef01234567", Revision: 3, Status: Verified, Source: "test", SavedAt: time.Unix(1_800_000_000, 0).UTC()}
	if err := s.Record(a); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Load(a.ID)
	if err != nil || !ok || got != a {
		t.Fatalf("Load = %+v, %v, %v", got, ok, err)
	}
	b := "0b7dc4e1-f2a9-4c3a-9e2f-5a6b7c8d9e01"
	if _, err := s.Local(b); err != nil {
		t.Fatal(err)
	}
	listed, err := s.List()
	if err != nil || len(listed) != 2 {
		t.Fatalf("List = %+v, %v", listed, err)
	}
	if listed[0].ID != b || listed[0].Err == nil || listed[1].Saved != a {
		t.Errorf("List = %+v", listed)
	}
}

// One writer at a time: a second Lock of the same deployment is refused,
// naming the holder's pid and command, and goes ahead once the first is
// released. Another deployment's lock is its own.
func TestLockRefusesSecondWriter(t *testing.T) {
	s := Store{Base: t.TempDir()}
	const id, other = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01", "11111111-2222-4333-8444-555555555555"
	unlock, err := s.Lock(id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Lock(id)
	var held *LockedError
	if !errors.As(err, &held) {
		t.Fatalf("second Lock: %v", err)
	}
	for _, want := range []string{strconv.Itoa(os.Getpid()), os.Args[0], id} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal lacks %q: %v", want, err)
		}
	}
	unlockOther, err := s.Lock(other)
	if err != nil {
		t.Fatalf("another deployment: %v", err)
	}
	unlockOther()
	unlock()
	again, err := s.Lock(id)
	if err != nil {
		t.Fatalf("after unlock: %v", err)
	}
	again()
	if _, err := os.Stat(filepath.Join(s.Base, "deployments", id, ".lock")); err != nil {
		t.Errorf("lock file: %v", err)
	}
}

// A lock file a dead process left holds no lock: flock is released when
// its holder exits, whatever the file says.
func TestLockIgnoresAStaleFile(t *testing.T) {
	s := Store{Base: t.TempDir()}
	const id = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"
	dir := filepath.Join(s.Base, "deployments", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".lock"), []byte("99999999 paisans deployment apply --execute\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unlock, err := s.Lock(id)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

// Held opens a deployment's system copy only when it exists, and creates
// nothing.
func TestHeldCreatesNothing(t *testing.T) {
	s := Store{Base: t.TempDir()}
	const id = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"
	if _, ok, err := s.Held(id); err != nil || ok {
		t.Fatalf("Held = %v, %v", ok, err)
	}
	if entries, _ := os.ReadDir(s.Base); len(entries) != 0 {
		t.Fatalf("wrote %v", entries)
	}
	if _, err := s.Local(id); err != nil {
		t.Fatal(err)
	}
	l, ok, err := s.Held(id)
	if err != nil || !ok || l.Dir != filepath.Join(s.Base, "deployments", id) {
		t.Fatalf("Held = %v, %v, %v", l, ok, err)
	}
	if _, _, err := s.Held("../x"); err == nil {
		t.Error("a path accepted as an id")
	}
}
