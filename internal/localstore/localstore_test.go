package localstore

import (
	"os"
	"path/filepath"
	"runtime"
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
