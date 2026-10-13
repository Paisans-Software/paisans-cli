package kinds

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// The core catalog. The binary carries only its pin, internal/blueprints/
// core/pin.yaml: the commit and each blueprint's SHA-256. The first time a
// core blueprint is asked for, every one at the pin is read from the
// catalog cache, fetched into it when it is not there, and checked against
// its SHA-256, which is what says the files are the ones the toolkit's
// maintainers released (docs/specs/2026-10-13-core-catalog-fetched.md). A
// configuration that pins core to a commit of its own replaces them all.

// core is whether the core blueprints have been read, and what went wrong
// if reading them failed. It is read once per process.
var core struct {
	sync.Mutex
	done bool
	err  error
}

// LoadCore reads every core blueprint at the binary's pin from url, the
// pin's own when empty, unless they have been read already. The answer of
// the first call holds for the process.
func LoadCore(url string) error {
	core.Lock()
	defer core.Unlock()
	if core.done {
		return core.err
	}
	core.done = true
	core.err = readCore(url)
	return core.err
}

func readCore(url string) error {
	pin, err := blueprints.CorePin()
	if err != nil {
		return err
	}
	if url == "" {
		url = pin.URL
	}
	dir, err := Fetch(config.Catalog{URL: url, Commit: pin.Commit})
	if err != nil {
		// A snapshot of the commit holds the core blueprints a configuration
		// uses, which is what a recovery with no forge needs.
		snap, ok := snapshotDir(pin.Commit)
		if !ok {
			return fmt.Errorf("the core catalog: %w", err)
		}
		dir = snap
	}
	names, err := Blueprints(dir)
	if err == nil {
		err = Checkout(dir, names...)
	}
	if err != nil {
		return fmt.Errorf("the core catalog at %s: %w", pin.Commit[:12], err)
	}
	// Every blueprint is checked before any is recorded, so a core catalog
	// with one wrong is not half known.
	read := map[string]*Def{}
	at := map[string]string{}
	for _, name := range names {
		if reservedName(name) {
			return fmt.Errorf("the core catalog at %s ships %s, a reserved blueprint, which no catalog may provide", pin.Commit[:12], name)
		}
		want, ok := pin.Blueprints[name]
		if !ok {
			return fmt.Errorf("the core catalog at %s holds %s, which this toolkit's pin does not name", pin.Commit[:12], name)
		}
		if err := checkCoreDigest(dir, name, want); err != nil {
			return err
		}
		d, err := ReadFrom(dir, name)
		if err != nil {
			return fmt.Errorf("the core catalog: %w", err)
		}
		// Recorded at its real path, so a cache reached through a link reads
		// from where the files are.
		bdir, err := filepath.EvalSymlinks(filepath.Join(dir, "blueprints", name))
		if err != nil {
			return err
		}
		read[name], at[name] = d, bdir
	}
	for name, d := range read {
		register(config.Kind(name), d, at[name])
	}
	return nil
}

// checkCoreDigest refuses a core blueprint whose files are not the ones the
// pin names. A checkout that does not match is checked out once more first,
// since a cache can be damaged by more than a forge.
func checkCoreDigest(dir, name, want string) error {
	bdir := filepath.Join(dir, "blueprints", name)
	got, err := blueprints.Digest(os.DirFS(bdir), ".")
	if err == nil && got == want {
		return nil
	}
	if fetched(dir) {
		os.Remove(filepath.Join(dir, ".git", stateDir, "complete", name))
		if err := Checkout(dir, name); err != nil {
			return err
		}
		got, err = blueprints.Digest(os.DirFS(bdir), ".")
		if err == nil && got == want {
			return nil
		}
	}
	if err != nil {
		return fmt.Errorf("the core blueprint %s: %w", name, err)
	}
	return fmt.Errorf("the core blueprint %s in %s has SHA-256 %s, and this toolkit's pin says %s. Delete that directory to fetch it again", name, dir, got, want)
}

// isCore reports whether a kind is named as a core blueprint is: unqualified
// and not reserved.
func isCore(kind config.Kind) bool {
	return !strings.Contains(string(kind), "/") && !reservedName(string(kind))
}

// replaceCore drops every core blueprint recorded so far, for a
// configuration whose own core pin replaces them, and counts the core as
// read so the binary's pin is not read over them.
func replaceCore() {
	core.Lock()
	defer core.Unlock()
	core.done, core.err = true, nil
	mu.Lock()
	defer mu.Unlock()
	for k, d := range defs {
		if !d.Reserved && !strings.Contains(string(k), "/") {
			delete(defs, k)
			delete(sources, k)
		}
	}
}

// CorePin is the catalog a blueprint's catalog name stands for in cfg, with
// the core catalog's URL and commit filled in from the binary's pin when the
// configuration leaves them out.
func CorePin(cfg *config.Config, catalog string) config.Catalog {
	cat := cfg.Catalogs[catalog]
	if catalog != config.CoreCatalog {
		return cat
	}
	if pin, err := blueprints.CorePin(); err == nil {
		if cat.URL == "" {
			cat.URL = pin.URL
		}
		if cat.Commit == "" {
			cat.Commit = pin.Commit
		}
	}
	return cat
}
