package version

import (
	"encoding/json"
	"fmt"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
)

// Build is the build of paisans that wrote a record: its Version, the
// commit it was built from and whether that tree had uncommitted changes.
// Every record the toolkit writes embeds it, so its keys sit beside the
// record's own. Version is the field anything orders by; Commit and
// Modified tell two builds of one version apart, for diagnosis. Commit is
// empty for a build the Go toolchain recorded no commit for, as for one
// built outside a git checkout.
type Build struct {
	Version  string `json:"toolkit_version"`
	Commit   string `json:"toolkit_commit"`
	Modified bool   `json:"toolkit_modified"`
}

// BuildKeys are the keys a record holds its Build under. A record must
// hold all three.
var BuildKeys = []string{"toolkit_version", "toolkit_commit", "toolkit_modified"}

var current = sync.OnceValue(func() Build {
	info, _ := debug.ReadBuildInfo()
	return FromBuildInfo(Version, info)
})

// Current is this binary's build.
func Current() Build { return current() }

// FromBuildInfo is the build of release that info describes: the commit
// and the modified flag the Go toolchain recorded, none when info is nil or
// records no commit.
func FromBuildInfo(release string, info *debug.BuildInfo) Build {
	b := Build{Version: release}
	if info == nil {
		return b
	}
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			b.Commit = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if b.Commit != "" {
		b.Modified = modified
	}
	return b
}

// ShortCommit is the commit cut to 12 characters, marked -dirty when the
// tree was modified, "" when the build recorded no commit.
func (b Build) ShortCommit() string {
	if b.Commit == "" {
		return ""
	}
	c := b.Commit
	if len(c) > 12 {
		c = c[:12]
	}
	if b.Modified {
		c += "-dirty"
	}
	return c
}

// String is the version and the short commit, as "0.3.0 (519299d5cb00)"
// or "0.3.0-dev (519299d5cb00-dirty)": the version alone when the build
// recorded no commit.
func (b Build) String() string {
	if c := b.ShortCommit(); c != "" {
		return fmt.Sprintf("%s (%s)", b.Version, c)
	}
	return b.Version
}

var commitRE = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// Problems is each way b is not a build paisans records: a version Compare
// cannot order, a commit that is not a full lower-case hex commit id, or a
// modified tree with no commit.
func (b Build) Problems() []string {
	var out []string
	if !Valid(b.Version) {
		out = append(out, fmt.Sprintf("toolkit_version: %q is not a version, MAJOR.MINOR.PATCH or MAJOR.MINOR.PATCH-dev", b.Version))
	}
	if b.Commit != "" && !commitRE.MatchString(b.Commit) {
		out = append(out, fmt.Sprintf("toolkit_commit: %q is not a full lower-case hex commit id, or empty", b.Commit))
	}
	if b.Commit == "" && b.Modified {
		out = append(out, "toolkit_modified: true, and toolkit_commit is empty, so there is no tree it could say was modified")
	}
	return out
}

// NoBuildError is a record that does not hold every one of BuildKeys, so it
// does not say which build of paisans wrote it.
type NoBuildError struct {
	Missing []string
}

func (e *NoBuildError) Error() string {
	return "it does not say which build of paisans wrote it (" + strings.Join(e.Missing, ", ") + " missing)"
}

// MissingBuild names each of BuildKeys obj does not hold.
func MissingBuild(obj map[string]json.RawMessage) []string {
	var out []string
	for _, k := range BuildKeys {
		if _, ok := obj[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

// RequireBuild is a *NoBuildError when the JSON object data does not hold
// every one of BuildKeys, nil when it does. data that is not a JSON object
// is left to the record's own parser to refuse, and is nil here.
func RequireBuild(data []byte) error {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return nil
	}
	if missing := MissingBuild(obj); len(missing) > 0 {
		return &NoBuildError{Missing: missing}
	}
	return nil
}
