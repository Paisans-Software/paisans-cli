package main

import (
	"runtime/debug"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/version"
)

// A build says its release and the commit it was built from, cut to 12
// characters and marked when the tree was dirty; a build with no commit
// recorded says its release alone.
func TestVersionFrom(t *testing.T) {
	vcs := func(modified string) []debug.BuildSetting {
		return []debug.BuildSetting{{Key: "vcs.revision", Value: "519299d5cb009da4904cdf53c2025f1ad609f102"}, {Key: "vcs.modified", Value: modified}}
	}
	for _, c := range []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{"clean", &debug.BuildInfo{Settings: vcs("false")}, "0.3.0 (519299d5cb00)"},
		{"dirty", &debug.BuildInfo{Settings: vcs("true")}, "0.3.0 (519299d5cb00-dirty)"},
		{"no commit", &debug.BuildInfo{}, "0.3.0"},
		{"no build information", nil, "0.3.0"},
	} {
		if got := versionFrom("0.3.0", c.info); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

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
