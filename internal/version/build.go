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
// Modified tell two builds of one version apart, for diagnosis.
//
// Commit is never empty. It is one of: a full lower-case hex commit id; the
// 12 hex digits a module pseudo-version carries; the module tag a build of
// a tagged module was made from (Eg: v0.3.0-rc.1), which names its commit;
// or Unknown, for a build that no source gave a commit.
type Build struct {
	Version  string `json:"toolkit_version"`
	Commit   string `json:"toolkit_commit"`
	Modified bool   `json:"toolkit_modified"`
}

// Unknown is the Commit of a build no source gave a commit: built from a
// source tree with no git metadata and no commit set at build time.
const Unknown = "unknown"

// buildCommit is the commit this binary was built from, when its build set one:
//
//	go build -ldflags "-X github.com/paisans-software/paisans-cli/internal/version.buildCommit=$(git rev-parse HEAD)" ./cmd/paisans
//
// make release-binaries sets it, and a packager building from a source
// tarball, which holds no git metadata, sets it to the release's commit.
var buildCommit string

// BuildKeys are the keys a record holds its Build under. A record must
// hold all three.
var BuildKeys = []string{"toolkit_version", "toolkit_commit", "toolkit_modified"}

var current = sync.OnceValue(func() Build {
	info, _ := debug.ReadBuildInfo()
	return FromBuildInfo(Version, buildCommit, info)
})

// Current is this binary's build.
func Current() Build { return current() }

// pseudoRE finds the commit in a module pseudo-version, the 12 hex digits
// after its 14-digit time (Eg: v0.0.0-20261017120000-519299d5cb00).
var pseudoRE = regexp.MustCompile(`(?:^v[0-9]+\.[0-9]+\.[0-9]+-|\.)[0-9]{14}-([0-9a-f]{12})(?:\+[0-9A-Za-z.-]+)?$`)

// tagRE is a module version that is a tag.
var tagRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

// FromBuildInfo is the build of release with its commit from the first
// source that gives one:
//
//  1. ldCommit, set at build time with -ldflags -X; the tree counts as
//     modified only when info's vcs.revision is the same commit and
//     vcs.modified says so, since a tarball build has no tree to modify;
//  2. info's vcs.revision, with vcs.modified;
//  3. info's Main.Version for a build of the module (go install ...@v),
//     the commit a pseudo-version carries or the tag itself, never
//     modified;
//  4. Unknown, never modified.
func FromBuildInfo(release, ldCommit string, info *debug.BuildInfo) Build {
	b := Build{Version: release}
	var revision, mainVersion string
	var modified bool
	if info != nil {
		mainVersion = info.Main.Version
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				revision = s.Value
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
	}
	switch {
	case ldCommit != "":
		b.Commit, b.Modified = ldCommit, revision == ldCommit && modified
	case revision != "":
		b.Commit, b.Modified = revision, modified
	case pseudoRE.MatchString(mainVersion):
		b.Commit = pseudoRE.FindStringSubmatch(mainVersion)[1]
	case tagRE.MatchString(mainVersion):
		b.Commit = mainVersion
	default:
		b.Commit = Unknown
	}
	return b
}

// ShortCommit is the commit as a person reads it: a hex commit cut to 12
// characters and marked -dirty when the tree was modified, a tag or Unknown
// as it is.
func (b Build) ShortCommit() string {
	c := b.Commit
	if hexRE.MatchString(c) && len(c) > 12 {
		c = c[:12]
	}
	if b.Modified {
		c += "-dirty"
	}
	return c
}

// String is the version and the short commit, as "0.3.0 (519299d5cb00)",
// "0.3.0-dev (519299d5cb00-dirty)", "0.3.0 (v0.3.0)" or "0.3.0 (unknown)".
func (b Build) String() string {
	return fmt.Sprintf("%s (%s)", b.Version, b.ShortCommit())
}

var hexRE = regexp.MustCompile(`^([0-9a-f]{12}|[0-9a-f]{40}|[0-9a-f]{64})$`)

// Problems is each way b is not a build paisans records, the values half of
// CheckBuild: a version Compare cannot order, a commit that is none of the
// forms Build names, or a modified tree whose commit is not a hex id.
func (b Build) Problems() []string {
	var out []string
	if !Valid(b.Version) {
		out = append(out, fmt.Sprintf("toolkit_version: %q is not a version, MAJOR.MINOR.PATCH or MAJOR.MINOR.PATCH-dev", b.Version))
	}
	if !hexRE.MatchString(b.Commit) && !tagRE.MatchString(b.Commit) && b.Commit != Unknown {
		out = append(out, fmt.Sprintf("toolkit_commit: %q is not a lower-case hex commit id, a module tag, or %q", b.Commit, Unknown))
	}
	if b.Modified && !hexRE.MatchString(b.Commit) {
		out = append(out, fmt.Sprintf("toolkit_modified: true, and toolkit_commit %q is no tree that could be modified", b.Commit))
	}
	return out
}

// NoBuildError is a record that does not say which build of paisans wrote
// it: a key of BuildKeys missing, named in Missing, or holding a value that
// is not a build paisans records, each in Problems.
type NoBuildError struct {
	Missing  []string
	Problems []string
}

func (e *NoBuildError) Error() string {
	return "it does not say which build of paisans wrote it (" + e.Detail() + ")"
}

// Detail is what is wrong with the build: the keys missing, or each value
// paisans never records.
func (e *NoBuildError) Detail() string {
	if len(e.Missing) > 0 {
		return strings.Join(e.Missing, ", ") + " missing"
	}
	return strings.Join(e.Problems, "; ")
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

// CheckBuild is the one check of a record's build, which every reader of a
// record makes: each of BuildKeys present in obj, toolkit_version a version
// Compare orders, toolkit_commit one of the forms Build names, and
// toolkit_modified a boolean. It is the build read, or a *NoBuildError saying why not.
func CheckBuild(obj map[string]json.RawMessage) (Build, error) {
	if missing := MissingBuild(obj); len(missing) > 0 {
		return Build{}, &NoBuildError{Missing: missing}
	}
	var b Build
	var problems []string
	for _, k := range BuildKeys {
		if strings.TrimSpace(string(obj[k])) == "null" {
			return Build{}, &NoBuildError{Problems: []string{k + ": null"}}
		}
	}
	if json.Unmarshal(obj["toolkit_version"], &b.Version) != nil {
		problems = append(problems, fmt.Sprintf("toolkit_version: %s is not a string", obj["toolkit_version"]))
	}
	if json.Unmarshal(obj["toolkit_commit"], &b.Commit) != nil {
		problems = append(problems, fmt.Sprintf("toolkit_commit: %s is not a string", obj["toolkit_commit"]))
	}
	if json.Unmarshal(obj["toolkit_modified"], &b.Modified) != nil {
		problems = append(problems, fmt.Sprintf("toolkit_modified: %s is not true or false", obj["toolkit_modified"]))
	}
	if len(problems) == 0 {
		problems = b.Problems()
	}
	if len(problems) > 0 {
		return Build{}, &NoBuildError{Problems: problems}
	}
	return b, nil
}

// RequireBuild is CheckBuild of the JSON object data: nil when it holds a
// build paisans records, a *NoBuildError when it does not. data that is not
// a JSON object is left to the record's own parser to refuse, and is nil
// here.
func RequireBuild(data []byte) error {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return nil
	}
	_, err := CheckBuild(obj)
	return err
}
