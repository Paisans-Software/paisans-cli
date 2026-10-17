package version

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// changelogAfterRC is a changelog whose pre-release took every change, so
// [Unreleased] is empty.
const changelogAfterRC = `# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0-rc.1] - 2026-10-17

### Added

- Something.

[Unreleased]: https://github.com/Paisans-Software/paisans-cli/compare/0.3.0-rc.1...master
[0.3.0-rc.1]: https://github.com/Paisans-Software/paisans-cli/compare/0.2.0...0.3.0-rc.1
`

// bumpRepo is a git repository holding the Makefile, a version constant of
// constant, and changelogAfterRC, with each tag given; it skips the test
// without make, git and uvx, which runs kacl-cli as the Makefile does.
func bumpRepo(t *testing.T, constant string, tags ...string) (string, func(args ...string) (string, error)) {
	t.Helper()
	if testing.Short() {
		t.Skip("runs make bump")
	}
	for _, tool := range []string{"make", "git", "uvx", "gofmt"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	makefile, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := map[string]string{
		"Makefile":                    string(makefile),
		"CHANGELOG.md":                changelogAfterRC,
		"internal/version/version.go": "package version\n\nconst Version = \"" + constant + "\"\n",
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	run := func(name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, c := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", "https://github.com/Paisans-Software/paisans-cli.git"},
		{"add", "-A"},
		{"commit", "-q", "-m", "init"},
	} {
		if out, err := run("git", c...); err != nil {
			t.Fatalf("git %v: %v: %s", c, err, out)
		}
	}
	for _, tag := range tags {
		if out, err := run("git", "tag", "-a", "-m", tag, tag); err != nil {
			t.Fatalf("git tag %s: %v: %s", tag, err, out)
		}
	}
	return dir, func(args ...string) (string, error) { return run("make", args...) }
}

// A release whose pre-release took every change cuts a section saying it is
// that pre-release's release, from an empty [Unreleased].
func TestBumpReleasesAPreReleaseUnchanged(t *testing.T) {
	dir, mk := bumpRepo(t, "0.3.0-rc.2", "v0.2.0", "v0.3.0-rc.1", "v0.3.0-rc.2")
	if out, err := mk("bump", "VERSION=0.3.0"); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	changelog, _ := os.ReadFile(filepath.Join(dir, "CHANGELOG.md"))
	section := string(changelog)[strings.Index(string(changelog), "## [0.3.0] - "):]
	section = section[:strings.Index(section, "## [0.3.0-rc.1]")]
	if !strings.Contains(section, "- The release of `0.3.0-rc.2`, unchanged since it.") {
		t.Errorf("the 0.3.0 section:\n%s", section)
	}
	constant, _ := os.ReadFile(filepath.Join(dir, "internal", "version", "version.go"))
	if !strings.Contains(string(constant), `const Version = "0.3.0"`) {
		t.Errorf("version.go:\n%s", constant)
	}
}

// With no pre-release of it, a release from an empty [Unreleased] is
// refused, and so is another pre-release.
func TestBumpRefusesAnEmptyReleaseWithNoPreRelease(t *testing.T) {
	_, mk := bumpRepo(t, "0.3.0-rc.1", "v0.2.0", "v0.3.0-rc.1")
	for _, v := range []string{"0.4.0", "0.3.0-rc.2"} {
		if out, err := mk("bump", "VERSION="+v); err == nil || !strings.Contains(out, "there is nothing to release") {
			t.Errorf("%s: %v: %s", v, err, out)
		}
	}
}
