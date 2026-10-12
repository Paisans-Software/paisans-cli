package main

import (
	"runtime/debug"
	"testing"
)

// A tagged build says its tag; a build between tags says its commit, not the
// pseudo-version the toolchain makes of it; a build with nothing recorded
// says devel.
func TestVersionFrom(t *testing.T) {
	vcs := func(modified string) []debug.BuildSetting {
		return []debug.BuildSetting{{Key: "vcs.revision", Value: "519299d5cb009da4904cdf53c2025f1ad609f102"}, {Key: "vcs.modified", Value: modified}}
	}
	for _, c := range []struct {
		version  string
		settings []debug.BuildSetting
		want     string
	}{
		{"v0.3.0", vcs("false"), "v0.3.0"},
		{"v0.0.0-20261012190447-519299d5cb00+dirty", vcs("true"), "519299d5cb00-dirty"},
		{"v0.0.0-20261012190447-519299d5cb00", vcs("false"), "519299d5cb00"},
		{"(devel)", vcs("false"), "519299d5cb00"},
		{"(devel)", nil, "devel"},
	} {
		info := &debug.BuildInfo{Main: debug.Module{Version: c.version}, Settings: c.settings}
		if got := versionFrom(info); got != c.want {
			t.Errorf("%s: got %q, want %q", c.version, got, c.want)
		}
	}
}
