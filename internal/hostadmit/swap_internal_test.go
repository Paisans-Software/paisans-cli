package hostadmit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runSwapCommand runs swapCommand's script with sh, its paths moved under
// a temporary directory and fallocate, dd, mkswap and swapon replaced by
// stubs: fallocate fails when fallocateFails, dd when ddFails. fstab, with
// DIR for the directory, is /etc/fstab, absent when empty. It returns the
// directory and the script's error.
func runSwapCommand(t *testing.T, fstab string, fallocateFails, ddFails bool, before func(dir string)) (string, error) {
	t.Helper()
	dir, bin := t.TempDir(), t.TempDir()
	stub := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if fallocateFails {
		stub("fallocate", "exit 1")
	} else {
		stub("fallocate", `printf fallocate > "$3"`)
	}
	if ddFails {
		stub("dd", `for a; do case $a in of=*) printf partial > "${a#of=}" ;; esac; done; exit 1`)
	} else {
		stub("dd", `for a; do case $a in of=*) printf dd > "${a#of=}" ;; esac; done`)
	}
	stub("mkswap", `printf ' mkswap' >> "$1"`)
	stub("swapon", `printf '%s\n' "$1" > "`+dir+`/on"`)
	if fstab != "" {
		if err := os.WriteFile(filepath.Join(dir, "fstab"), []byte(strings.ReplaceAll(fstab, "DIR", dir)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if before != nil {
		before(dir)
	}
	script := strings.ReplaceAll(swapCommand(2<<30), swapPath, dir+swapPath)
	script = strings.ReplaceAll(script, "/etc/fstab", dir+"/fstab")
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("%s", out)
	}
	return dir, err
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// The swapfile is made, the fstab line added on a line of its own, and
// swapon run; a second run adds no second line.
func TestSwapCommandMakesTheSwapfile(t *testing.T) {
	root := "UUID=abc / ext4 defaults 0 1"
	dir, err := runSwapCommand(t, root, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir+"/swapfile"); got != "fallocate mkswap" {
		t.Errorf("swapfile: %q", got)
	}
	if got, want := read(t, dir+"/fstab"), root+"\n"+dir+"/swapfile none swap sw 0 0\n"; got != want {
		t.Errorf("fstab:\n%q\nwant\n%q", got, want)
	}
	if got := read(t, dir+"/on"); got != dir+"/swapfile\n" {
		t.Errorf("swapon: %q", got)
	}
	if _, err := os.Stat(dir + "/swapfile.paisans-tmp"); !os.IsNotExist(err) {
		t.Errorf("the temporary file is left: %v", err)
	}
	if fi, err := os.Stat(dir + "/swapfile"); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("swapfile mode: %v %v", fi.Mode(), err)
	}

}

// A swapfile line already in fstab is not added again.
func TestSwapCommandAddsNoSecondFstabLine(t *testing.T) {
	fstab := "UUID=abc / ext4 defaults 0 1\nDIR/swapfile none swap sw 0 0\n"
	dir, err := runSwapCommand(t, fstab, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, dir+"/fstab"), strings.ReplaceAll(fstab, "DIR", dir); got != want {
		t.Errorf("fstab:\n%q\nwant\n%q", got, want)
	}
}

// A file system fallocate cannot fill gets dd's file instead.
func TestSwapCommandFallsBackToDD(t *testing.T) {
	dir, err := runSwapCommand(t, "", true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir+"/swapfile"); got != "dd mkswap" {
		t.Errorf("swapfile: %q", got)
	}
	if got, want := read(t, dir+"/fstab"), dir+"/swapfile none swap sw 0 0\n"; got != want {
		t.Errorf("fstab: %q", got)
	}
}

// A dd that fails leaves no temporary file, no swapfile and fstab as it was.
func TestSwapCommandCleansUpAFailure(t *testing.T) {
	dir, err := runSwapCommand(t, "x / ext4 defaults 0 1\n", true, true, nil)
	if err == nil {
		t.Fatal("a failed dd succeeded")
	}
	for _, f := range []string{"/swapfile.paisans-tmp", "/swapfile", "/on"} {
		if _, err := os.Stat(dir + f); !os.IsNotExist(err) {
			t.Errorf("%s is left: %v", f, err)
		}
	}
	if got := read(t, dir+"/fstab"); got != "x / ext4 defaults 0 1\n" {
		t.Errorf("fstab: %q", got)
	}
}

// A /swapfile that appeared since the probe is never moved over.
func TestSwapCommandKeepsASwapfileThatAppeared(t *testing.T) {
	dir, err := runSwapCommand(t, "", false, false, func(dir string) {
		os.WriteFile(dir+"/swapfile", []byte("theirs"), 0o600)
	})
	if err == nil {
		t.Fatal("moved over a swapfile that appeared")
	}
	if got := read(t, dir+"/swapfile"); got != "theirs" {
		t.Errorf("swapfile: %q", got)
	}
	if _, err := os.Stat(dir + "/swapfile.paisans-tmp"); !os.IsNotExist(err) {
		t.Errorf("the temporary file is left: %v", err)
	}
}
