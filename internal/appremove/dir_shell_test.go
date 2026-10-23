package appremove

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
