package appremove

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/render"
)

// answered is a Host whose Run answers out; it is used for nothing else.
type answered struct {
	Host
	out string
}

func (a answered) Run(string) (string, error) { return a.out, nil }

// DirCommand, run by a shell on a real directory, answers what probeDir
// reads; one it cannot read all of fails, so an error never reads as an
// entry or a size. rootlesskit stands in as a script that runs its
// arguments. GNU find's -printf is needed, as on a host.
func TestDirCommandOnADirectory(t *testing.T) {
	if exec.Command("sh", "-c", "find . -maxdepth 0 -printf ''").Run() != nil {
		t.Skip("find has no -printf here")
	}
	bin, base := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "rootlesskit"), []byte("#!/bin/sh\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(dir string) (string, error) {
		cmd := exec.Command("sh", "-c", DirCommand(dir))
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	dir := filepath.Join(base, "blog")
	for _, f := range []string{"data/a", "data/b", "compose.yaml"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run(dir)
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got, err := probeDir(answered{out: out}, dir)
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !got.Exists || got.Files != 3 || strings.Join(got.Entries, ",") != "compose.yaml,data" || got.Bytes <= 0 {
		t.Errorf("read %+v from %q", got, out)
	}
	if out, err := run(filepath.Join(base, "gone")); err != nil || out != "" {
		t.Errorf("a missing directory answered %q, %v", out, err)
	}
	if os.Geteuid() == 0 {
		return
	}
	locked := filepath.Join(dir, "data")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0o755)
	if out, err := run(dir); err == nil || strings.Contains(out, "present") {
		t.Errorf("an unreadable directory answered %q, %v", out, err)
	}
}

// DirCommandFor without --delete-data, run by a shell on a real directory
// whose rendered files are gone: the directories they were in go, and the
// stack directory once it is empty, while the app's data keeps every empty
// directory in it.
func TestDirCommandForKeepsTheEmptyDirectoriesInData(t *testing.T) {
	bin, base := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "rootlesskit"), []byte("#!/bin/sh\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, files []File) {
		t.Helper()
		cmd := exec.Command("sh", "-c", DirCommandFor(dir, files, false))
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	rendered := func(dir string, rels ...string) []File {
		var files []File
		for _, rel := range rels {
			files = append(files, File{Entry: render.ManifestFile{Path: strings.TrimPrefix(filepath.Join(dir, rel), "/")}})
		}
		return files
	}
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }

	db := filepath.Join(base, "db")
	for _, rel := range []string{"etc", "pgdata/pg_notify", "pgdata/pg_twophase", "cache"} {
		if err := os.MkdirAll(filepath.Join(db, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(db, "pgdata/PG_VERSION"), []byte("18"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(db, rendered(db, "compose.yaml", "etc/app.conf"))
	for _, rel := range []string{"pgdata/pg_notify", "pgdata/pg_twophase", "cache"} {
		if !exists(filepath.Join(db, rel)) {
			t.Errorf("%s was deleted", rel)
		}
	}
	if exists(filepath.Join(db, "etc")) {
		t.Error("etc, which held a rendered file, is left")
	}

	gone := filepath.Join(base, "gone")
	if err := os.MkdirAll(filepath.Join(gone, "etc/conf.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	run(gone, rendered(gone, "compose.yaml", "etc/conf.d/app.conf"))
	if exists(gone) {
		t.Error("a stack directory holding only rendered directories is left")
	}
	run(filepath.Join(base, "missing"), rendered(filepath.Join(base, "missing"), "etc/app.conf"))
}
