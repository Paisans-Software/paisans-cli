// Package coretest gives tests the core catalog without the network. It is
// imported only from test files, once per test package that reads a core
// blueprint, in a core_test.go:
//
//	import _ "github.com/paisans-software/paisans-stack/internal/blueprints/coretest"
//
// At init it installs the test copy of the pinned commit,
// internal/blueprints/testdata/core, as a catalog cache, points
// PAISANS_CATALOG_CACHE there, and allows git only local repositories, so a
// test takes the same path a workstation does, finds the commit already
// fetched, and can never reach a forge. See docs/specs/2026-10-13-core-
// catalog-fetched.md, "Tests".
package coretest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
)

// completeMark is the mark internal/kinds reads as a commit fetched in full.
const completeMark = ".paisans-complete"

// installed is the commit's directory, shared by every test in every
// package: it is keyed by the pin, so a run never sees another pin's copy,
// and is written once and never changed, so no test can disturb another.
var installed string

func init() {
	dir, err := install()
	if err != nil {
		panic("coretest: " + err.Error())
	}
	installed = dir
	if err := os.Setenv("PAISANS_CATALOG_CACHE", filepath.Dir(dir)); err != nil {
		panic(err)
	}
	if err := os.Setenv("GIT_ALLOW_PROTOCOL", "file"); err != nil {
		panic(err)
	}
}

// Cache is a fresh catalog cache for one test, holding the core commit, and
// points PAISANS_CATALOG_CACHE at it for the test's duration. The commit is
// a link to the shared copy, so what a test fetches into its cache is its
// own and the core blueprints are read from the same place in every test.
func Cache(t testing.TB) string {
	t.Helper()
	cache := t.TempDir()
	if err := os.Symlink(installed, filepath.Join(cache, filepath.Base(installed))); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PAISANS_CATALOG_CACHE", cache)
	return cache
}

// install writes the test copy as the pinned commit's directory in a cache
// under the system's temporary directory, unless an earlier run did.
func install() (string, error) {
	pin, err := blueprints.CorePin()
	if err != nil {
		return "", err
	}
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot find the test copy")
	}
	src := filepath.Join(filepath.Dir(self), "..", "testdata", "core")
	sum := sha256.New()
	fmt.Fprintf(sum, "%s\n", pin.Commit)
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
	fmt.Fprintln(sum, blueprints.DigestFiles(files))
	cache := filepath.Join(os.TempDir(), "paisans-coretest", hex.EncodeToString(sum.Sum(nil))[:16])
	dir := filepath.Join(cache, pin.Commit)
	if _, err := os.Stat(filepath.Join(dir, completeMark)); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	work, err := os.MkdirTemp(cache, ".install-")
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
	if err := os.WriteFile(filepath.Join(work, completeMark), nil, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(work, dir); err != nil {
		// Another package's tests installed it first, which is as good.
		if _, statErr := os.Stat(filepath.Join(dir, completeMark)); statErr == nil {
			return dir, nil
		}
		return "", err
	}
	return dir, nil
}
