package hostcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// Versions are read from their leading numbers, whatever the distribution
// appends, and compared numerically.
func TestVersionParsing(t *testing.T) {
	for _, c := range []struct {
		in   string
		want [3]int
		ok   bool
	}{
		{"27.5.1", [3]int{27, 5, 1}, true},
		{"27.5.1+dfsg", [3]int{27, 5, 1}, true},
		{"27.5.1-0ubuntu3~24.04.2", [3]int{27, 5, 1}, true},
		{"26.1.3-ubuntu", [3]int{26, 1, 3}, true},
		{"2.29.7-1ubuntu1", [3]int{2, 29, 7}, true},
		{"2.40.3+ds1", [3]int{2, 40, 3}, true},
		{"v2.2.0", [3]int{2, 2, 0}, true},
		{"25.0.0-rc.1", [3]int{25, 0, 0}, true},
		{" 28.1 ", [3]int{28, 1, 0}, true},
		{"", [3]int{}, false},
		{"dev", [3]int{}, false},
	} {
		got, ok := parseVersion(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseVersion(%q) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
	for _, c := range []struct {
		v, min string
		want   bool
	}{
		{"25.0.0", "25.0.0", true},
		{"24.0.7", "25.0.0", false},
		{"25.0.0+dfsg", "25.0.0", true},
		{"27.5.1-0ubuntu3~24.04.2", "25.0.0", true},
		{"2.9.0", "2.24.0", false},
		{"2.24.0", "2.24.0", true},
		{"2.100.0", "2.24.0", true},
		{"3.0.0", "2.24.0", true},
		{"5.4.0", "2.24.0", true},
		{"", "2.24.0", false},
	} {
		if got := AtLeast(c.v, c.min); got != c.want {
			t.Errorf("AtLeast(%q, %q) = %v", c.v, c.min, got)
		}
	}
}

// The probe itself, run by sh against a stand in docker on PATH: each
// version on its line, and a compose plugin that fails reads as absent
// rather than failing the probe.
func TestDockerProbeShell(t *testing.T) {
	for _, c := range []struct {
		name, script string
		want         Docker
	}{
		{"current", "case \"$1 $2\" in 'version --format') echo '27.5.1 27.5.1' ;; 'compose version') echo 2.33.1 ;; esac", Docker{Present: true, Version: "27.5.1", Client: "27.5.1", ComposeWorks: true, Compose: "2.33.1"}},
		{"no compose", "case \"$1 $2\" in 'version --format') echo '24.0.7 24.0.7' ;; 'compose version') echo \"docker: 'compose' is not a docker command.\" >&2; exit 1 ;; esac", Docker{Present: true, Version: "24.0.7", Client: "24.0.7", ComposeMissing: true}},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"+c.script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", "-c", dockerProbe)
		cmd.Env = append(os.Environ(), "PATH="+dir+":/usr/bin:/bin")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", c.name, err, out)
		}
		got := parseDocker(string(out))
		got.Packages = nil
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v from %q, want %+v", c.name, got, out, c.want)
		}
	}
	// A daemon that does not answer fails the probe, as before.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\necho 'Cannot connect to the Docker daemon'; exit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", dockerProbe)
	cmd.Env = append(os.Environ(), "PATH="+dir+":/usr/bin:/bin")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("a daemon that does not answer passed: %s", out)
	}
}
