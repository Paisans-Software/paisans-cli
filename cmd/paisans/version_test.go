package main

import (
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
