package main

import (
	"flag"
	"fmt"
	"io"
	"runtime/debug"

	"github.com/paisans-software/paisans-stack/internal/version"
)

// runVersion prints the binary's version and the commit it was built from.
func runVersion(args []string, w io.Writer) error {
	fs := newFlagSet("version", flag.ContinueOnError)
	commonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("version takes no arguments, and was given %q", fs.Arg(0))
	}
	fmt.Fprintf(w, "paisans %s\n", toolkitVersion())
	return nil
}

// toolkitVersion is this binary's version, with the commit the Go toolchain
// recorded it was built from.
func toolkitVersion() string {
	info, _ := debug.ReadBuildInfo()
	return versionFrom(version.Version, info)
}

// versionFrom is release followed by the commit info records, cut to 12
// characters and marked -dirty when the tree had uncommitted changes, as
// "0.1.0 (519299d5cb00-dirty)". It is release alone when info records no
// commit, as for a build outside a git checkout. The commit is what tells
// two builds of one version apart.
func versionFrom(release string, info *debug.BuildInfo) string {
	if info == nil {
		return release
	}
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
	if revision == "" {
		return release
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	return fmt.Sprintf("%s (%s%s)", release, revision, dirty)
}
