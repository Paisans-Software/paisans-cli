package version

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
)

// Build is the build of paisans that wrote a record: its Version, the
// commit it was built from and whether that tree had uncommitted changes.
// Every record the toolkit writes embeds it, so its keys sit beside the
// record's own. Version is the field anything orders by; Commit and
// Modified tell two builds of one version apart, for diagnosis.
//
// Commit is a lower-case hex commit id (in full, or the 12 digits a module
// pseudo-version carries), or the release tag a build from a tarball or of
// a tagged module was made from (Eg: v0.3.0 or v0.3.0-rc.1), which names its
// commit. A record always names one.
type Build struct {
	Version  string `json:"toolkit_version"`
	Commit   string `json:"toolkit_commit"`
	Modified bool   `json:"toolkit_modified"`
}

// CommitVar is the variable a build sets the commit with when the source
// holds no git metadata, as a release tarball does:
//
//	go build -ldflags "-X github.com/paisans-software/paisans-cli/internal/version.buildCommit=<full commit or release tag>" ./cmd/paisans
const CommitVar = "github.com/paisans-software/paisans-cli/internal/version.buildCommit"

// buildCommit is the commit this binary was built from, when its build set
// one with -ldflags -X CommitVar=...: make release-binaries sets it, and a
// packager building from a release tarball sets it to the release's tag or
// full commit. CheckLinked refuses a value no record may hold.
var buildCommit string

// TestCommit is the commit of a test binary, which go test builds without
// VCS information: tests write records as any build does.
const TestCommit = "0000000000000000000000000000000000000000"

// BuildKeys are the keys a record holds its Build under. A record must
// hold all three.
var BuildKeys = []string{"toolkit_version", "toolkit_commit", "toolkit_modified"}

var current = sync.OnceValue(func() Build {
	info, _ := debug.ReadBuildInfo()
	b := FromBuildInfo(Version, buildCommit, info)
	if b.Commit == "" && testing.Testing() {
		b.Commit = TestCommit
	}
	return b
})

// Current is this binary's build. Its Commit is empty when no source gave
// one; Writable refuses that build any command that writes a record.
func Current() Build { return current() }

// CheckLinked is nil unless the build set CommitVar to a value no record
// may hold, as a short or upper-case commit: every command refuses then, so
// a packager learns of it before the binary writes anything.
func CheckLinked() error {
	if buildCommit == "" {
		return nil
	}
	// The 12 hex digits a pseudo-version carries are its own; a build that
	// sets the commit gives it in full.
	if p := (Build{Version: Version, Commit: buildCommit}).Problems(); len(p) > 0 || len(buildCommit) == 12 && hexRE.MatchString(buildCommit) {
		return fmt.Errorf("this paisans was built with -X %s=%q, which is not a full 40-character lower-case commit or a release tag (vX.Y.Z or vX.Y.Z-<pre-release>). Build it again with one", CommitVar, buildCommit)
	}
	return nil
}

// Writable is nil when this build names its commit, and otherwise the
// refusal of any command that writes a record: a record always says which
// build wrote it.
func Writable() error { return Current().Writable() }

// Writable is nil when b names its commit, and otherwise the refusal of a
// command that would write a record with it.
func (b Build) Writable() error {
	if b.Commit != "" {
		return nil
	}
	return fmt.Errorf("this paisans %s was built without naming its commit, and every record it writes must say which build wrote it, so it runs only commands that write nothing. Build it from a git checkout, or with -ldflags \"-X %s=<full commit or release tag>\"", b.Version, CommitVar)
}

// pseudoRE finds the commit in a module pseudo-version, the 12 hex digits
// after its 14-digit time (Eg: v0.0.0-20261017120000-519299d5cb00).
var pseudoRE = regexp.MustCompile(`(?:^v[0-9]+\.[0-9]+\.[0-9]+-|\.)[0-9]{14}-([0-9a-f]{12})(?:\+[0-9A-Za-z.-]+)?$`)

// releaseTag reports whether s is a release tag: v and a version Compare
// orders.
func releaseTag(s string) bool {
	v, ok := strings.CutPrefix(s, "v")
	return ok && Valid(v)
}

// FromBuildInfo is the build of release with its commit from the first
// source that gives one:
//
//  1. ldCommit, set at build time with -ldflags -X; the tree counts as
//     modified only when info's vcs.revision is the same commit and
//     vcs.modified says so, since a tarball build has no tree to modify;
//  2. info's vcs.revision, with vcs.modified;
//  3. info's Main.Version for a build of the module (go install ...@v),
//     the commit a pseudo-version carries or the tag itself, never
//     modified.
//
// With none, Commit is empty.
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
	case releaseTag(mainVersion):
		b.Commit = mainVersion
	}
	return b
}

// ShortCommit is the commit as a person reads it: a hex commit cut to 12
// characters and marked -dirty when the tree was modified, a tag as it is.
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
// "0.3.0-dev (519299d5cb00-dirty)" or "0.3.0 (v0.3.0)": the version alone
// for a build that names no commit, which writes no record.
func (b Build) String() string {
	if b.Commit == "" {
		return b.Version
	}
	return fmt.Sprintf("%s (%s)", b.Version, b.ShortCommit())
}

var hexRE = regexp.MustCompile(`^([0-9a-f]{12}|[0-9a-f]{40}|[0-9a-f]{64})$`)

// Problems is each way b is not a build paisans records, the values half of
// CheckBuild: a version Compare cannot order, a commit that is neither a
// lower-case hex commit id nor a release tag, or a modified tree whose
// commit is not a hex id.
func (b Build) Problems() []string {
	var out []string
	if !Valid(b.Version) {
		out = append(out, fmt.Sprintf("toolkit_version: %q is not a version, MAJOR.MINOR.PATCH with an optional -<pre-release>", b.Version))
	}
	if !hexRE.MatchString(b.Commit) && !releaseTag(b.Commit) {
		out = append(out, fmt.Sprintf("toolkit_commit: %q is not a lower-case hex commit id or a release tag", b.Commit))
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

// NewerError is a record whose build is otherwise sound and whose version
// this paisans cannot order: a newer paisans, whose versions take a form
// this one does not know, may have written it.
type NewerError struct {
	Build Build
}

func (e *NewerError) Error() string {
	return fmt.Sprintf("it was written by paisans %q, a version this paisans %s cannot order, so a newer paisans may have written it. Upgrade paisans", e.Build.Version, Version)
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
// toolkit_modified a boolean. It is the build read, a *NoBuildError saying
// why not, or a *NewerError, with the build read, when the version alone is
// one this paisans cannot order: a non-empty string with no space or
// control character.
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
	if len(problems) > 0 {
		return Build{}, &NoBuildError{Problems: problems}
	}
	if !Valid(b.Version) && wellFormed(b.Version) {
		// The commit is checked as this paisans knows it; a newer one's
		// version decides nothing else here.
		rest := b
		rest.Version = Version
		if len(rest.Problems()) == 0 {
			return b, &NewerError{Build: b}
		}
	}
	if problems = b.Problems(); len(problems) > 0 {
		return Build{}, &NoBuildError{Problems: problems}
	}
	return b, nil
}

// wellFormed reports whether v could be a version some paisans writes: not
// empty, with no space or control character.
func wellFormed(v string) bool {
	return v != "" && !strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' || r == 0x7f })
}

// RequireBuild is CheckBuild of the JSON object data: nil when it holds a
// build paisans records, a *NoBuildError or *NewerError when it does not.
// data that is not a JSON object is left to the record's own parser to
// refuse, and is nil here.
func RequireBuild(data []byte) error {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return nil
	}
	_, err := CheckBuild(obj)
	return err
}

// IsNewer reports whether err is a *NewerError.
func IsNewer(err error) bool {
	var n *NewerError
	return errors.As(err, &n)
}
