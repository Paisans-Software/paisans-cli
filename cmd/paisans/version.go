package main

import (
	"runtime/debug"
	"strings"
)

// toolkitVersion is this binary's version, as the Go toolchain recorded it.
func toolkitVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "devel"
	}
	return versionFrom(info)
}

// versionFrom is the tag the binary was built from, or, for a build between
// tags, the commit (with -dirty when the tree had changes), or devel when
// neither was recorded. A build between tags carries a pseudo-version, which
// says no more than the commit and is three times as long.
func versionFrom(info *debug.BuildInfo) string {
	var revision, dirty string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	v := info.Main.Version
	if v != "" && v != "(devel)" && (revision == "" || !strings.Contains(v, revision)) {
		return v
	}
	if revision == "" {
		return "devel"
	}
	return revision + dirty
}
