// Package coretest gives tests the core catalog without the network. It is
// imported only from test files, once per test package that reads a core
// blueprint or the foundation, in a core_test.go:
//
//	import _ "github.com/paisans-software/paisans-stack/internal/blueprints/coretest"
//
// At init it makes the test copy, internal/blueprints/testdata/core, into a
// git repository with one commit, tagged with the release
// internal/blueprints/testdata/core.release names, points
// PAISANS_CORE_CATALOG at it, and allows git only local repositories. A test
// then takes the same path a workstation does, listing the releases,
// fetching the newest into a catalog cache and reading it, and can never
// reach a forge. See docs/specs/2026-10-13-images-follow-tracks.md, "The core
// catalog follows its newest release".
package coretest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// completeMark is the mark internal/blueprints reads as a commit fetched in
// full.
const completeMark = ".paisans-complete"

// installed is the release's directory in a catalog cache, shared by every
// test in every package: it is keyed by the test copy's content, so a run
// never sees another copy, and is written once and never changed, so no
// test can disturb another.
var installed string

func init() {
	if err := os.Setenv("GIT_ALLOW_PROTOCOL", "file"); err != nil {
		panic(err)
	}
	repo, dir, err := install()
	if err != nil {
		panic("coretest: " + err.Error())
	}
	installed = dir
	if err := os.Setenv("PAISANS_CORE_CATALOG", "file://"+repo); err != nil {
		panic(err)
	}
	if err := os.Setenv("PAISANS_CATALOG_CACHE", filepath.Dir(dir)); err != nil {
		panic(err)
	}
}

// Cache is a fresh catalog cache for one test, holding the core release, and
// points PAISANS_CATALOG_CACHE at it for the test's duration. The release is
// a link to the shared copy, so what a test fetches into its cache is its own
// and the core blueprints are read from the same place in every test.
func Cache(t testing.TB) string {
	t.Helper()
	cache := t.TempDir()
	if err := os.Symlink(installed, filepath.Join(cache, filepath.Base(installed))); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PAISANS_CATALOG_CACHE", cache)
	return cache
}

// install makes the test copy a tagged repository under the system's
// temporary directory, and installs its commit in a catalog cache beside it,
// unless an earlier run made the same ones. It returns the repository and
// the commit's directory in the cache.
func install() (repo, dir string, err error) {
	repo, err = repository()
	if err != nil {
		return "", "", err
	}
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("reading the test copy's commit: %w", err)
	}
	commit := strings.TrimSpace(string(out))
	dir = filepath.Join(repo+"-cache", commit)
	if _, err := os.Stat(filepath.Join(dir, completeMark)); err == nil {
		return repo, dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", "", err
	}
	work, err := os.MkdirTemp(filepath.Dir(dir), ".install-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(work)
	err = filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repo, p)
		if rel == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(work, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(work, rel), data, 0o644)
	})
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(filepath.Join(work, completeMark), nil, 0o644); err != nil {
		return "", "", err
	}
	if err := os.Rename(work, dir); err != nil {
		if _, statErr := os.Stat(filepath.Join(dir, completeMark)); statErr != nil {
			return "", "", err
		}
	}
	return repo, dir, nil
}

// repository makes the test copy a tagged repository, unless an earlier run
// made the same one, and returns its path.
func repository() (string, error) {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot find the test copy")
	}
	testdata := filepath.Join(filepath.Dir(self), "..", "testdata")
	raw, err := os.ReadFile(filepath.Join(testdata, "core.release"))
	if err != nil {
		return "", err
	}
	tag := strings.TrimSpace(string(raw))
	src := filepath.Join(testdata, "core")
	files := map[string][]byte{}
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return "", err
	}
	sum := cacheKey(tag, files)
	base := filepath.Join(os.TempDir(), "paisans-coretest")
	repo := filepath.Join(base, hex.EncodeToString(sum[:])[:16])
	if _, err := os.Stat(filepath.Join(repo, ".git", "refs", "tags", tag)); err == nil {
		return repo, nil
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	work, err := os.MkdirTemp(base, ".install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	for rel, data := range files {
		dest := filepath.Join(work, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return "", err
		}
	}
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"add", "--all"},
		{"-c", "user.name=coretest", "-c", "user.email=coretest@example.org", "commit", "--quiet", "--no-gpg-sign", "-m", "test copy"},
		{"tag", tag},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2026-10-13T00:00:00Z", "GIT_COMMITTER_DATE=2026-10-13T00:00:00Z", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), out)
		}
	}
	if err := os.Rename(work, repo); err != nil {
		// Another package's tests installed it first, which is as good.
		if _, statErr := os.Stat(filepath.Join(repo, ".git", "refs", "tags", tag)); statErr == nil {
			return repo, nil
		}
		return "", err
	}
	return repo, nil
}

// cacheKey names the repository an earlier run made from the same tag and
// files, so that a change to either makes a new one. It is not a blueprint
// digest and nothing compares it with one; it only has to change when the
// test copy does. It cannot call into internal/blueprints, whose own tests
// import this package.
func cacheKey(tag string, files map[string][]byte) [sha256.Size]byte {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	fmt.Fprintf(h, "%s\n", tag)
	for _, name := range names {
		sum := sha256.Sum256(files[name])
		fmt.Fprintf(h, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}
