package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/version"
)

// version prints one line naming the binary and its version, and refuses an
// argument rather than ignoring it.
func TestRunVersion(t *testing.T) {
	var b strings.Builder
	if err := runVersion(nil, &b); err != nil {
		t.Fatal(err)
	}
	if want := "paisans " + toolkitVersion() + "\n"; b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
	if !strings.HasPrefix(b.String(), "paisans "+version.Version) {
		t.Errorf("%q does not start with the version constant %s", b.String(), version.Version)
	}
	if err := runVersion([]string{"extra"}, &b); err == nil {
		t.Error("version accepted an argument")
	}
}

// A binary built with the commit set by -ldflags -X, as make
// release-binaries and a packager building from a tarball do, names it,
// with no git metadata to read: -buildvcs=false leaves none.
func TestALdflagsBuildNamesItsCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	const commit = "519299d5cb009da4904cdf53c2025f1ad609f102"
	bin := filepath.Join(t.TempDir(), "paisans")
	build := exec.Command("go", "build", "-buildvcs=false", "-ldflags", "-X github.com/paisans-software/paisans-cli/internal/version.buildCommit="+commit, "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := "paisans " + version.Version + " (519299d5cb00)\n"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}
