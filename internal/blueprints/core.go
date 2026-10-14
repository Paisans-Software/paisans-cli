package blueprints

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// The core catalog. The binary carries only its URL. The first time a core
// blueprint or the foundation is asked for, the newest release of the core
// catalog this binary can read is chosen, fetched into the catalog cache when
// it is not there, and every blueprint and the foundation in it are read
// (docs/specs/2026-10-13-images-follow-tracks.md, "The core catalog follows
// its newest release"). A configuration that pins core to a release of its
// own replaces them all.

// CoreURL is where the core catalog is published. PAISANS_CORE_CATALOG
// names another, for a fork's own core catalog and for tests.
const CoreURL = "https://github.com/Paisans-Software/paisans-blueprints"

// coreURL is CoreURL, or PAISANS_CORE_CATALOG when it is set.
func coreURL() string {
	if url := os.Getenv("PAISANS_CORE_CATALOG"); url != "" {
		return url
	}
	return CoreURL
}

// coreTries is how many releases, newest first, are tried before giving up
// on finding one this binary reads.
const coreTries = 5

// core is whether the core catalog has been read, which release, what was
// skipped, and what went wrong if reading failed. It is read once per
// process.
var core struct {
	sync.Mutex
	done    bool
	err     error
	release config.Catalog
	skipped []string
	wizard  []WizardEntry
}

// LoadCore reads the core catalog from url, coreURL() when empty, unless it
// has been read already. The answer of the first call holds for the process.
func LoadCore(url string) error {
	core.Lock()
	defer core.Unlock()
	if core.done {
		return core.err
	}
	core.done = true
	if url == "" {
		url = coreURL()
	}
	core.release, core.skipped, core.err = readCore(url)
	return core.err
}

// CoreRelease is the core catalog release this process read, and each newer
// release it skipped with why, for the plan to name.
func CoreRelease() (config.Catalog, []string) {
	core.Lock()
	defer core.Unlock()
	return core.release, append([]string(nil), core.skipped...)
}

// readCore chooses the newest release at url whose blueprints and foundation
// this binary reads, and records them. A forge that cannot be reached falls
// back on the release this workstation last read, from its cache or from an
// installed snapshot, since a release that rendered once still renders.
func readCore(url string) (config.Catalog, []string, error) {
	releases, err := Releases(url)
	if err != nil {
		last, ok := lastCore(url)
		if !ok {
			return config.Catalog{}, nil, fmt.Errorf("the core catalog: %w", err)
		}
		dir, ok := coreDir(last)
		if !ok {
			return config.Catalog{}, nil, fmt.Errorf("the core catalog: %w, and the release %s last read here is no longer in the cache", err, last.Tag)
		}
		if err := readCoreAt(dir, last); err != nil {
			return config.Catalog{}, nil, err
		}
		return last, []string{fmt.Sprintf("the core catalog could not be listed (%v), so the release last read here, %s, was used", err, last.Tag)}, nil
	}
	var skipped []string
	for i, rel := range releases {
		if i == coreTries {
			break
		}
		cat := config.Catalog{URL: url, Tag: rel.Tag, Commit: rel.Commit}
		dir, err := Fetch(cat)
		if err == nil {
			err = readCoreAt(dir, cat)
		}
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", rel.Tag, err))
			continue
		}
		rememberCore(cat)
		return cat, skipped, nil
	}
	return config.Catalog{}, skipped, fmt.Errorf("the core catalog at %s has no release this toolkit reads: %s. A newer toolkit reads the newer releases", url, strings.Join(skipped, "; "))
}

// coreDir is where a core release can be read from: the cache, or an
// installed snapshot.
func coreDir(cat config.Catalog) (string, bool) {
	cache, err := CacheDir()
	if err == nil {
		dir := filepath.Join(cache, cat.Commit)
		if _, err := os.Stat(filepath.Join(dir, completeMark)); err == nil {
			return dir, true
		}
	}
	return snapshotDir(cat.Commit)
}

// readCoreAt reads every blueprint and the foundation of the core catalog
// at dir, and records them only when every one reads: a release with one
// wrong is skipped whole, not half known.
func readCoreAt(dir string, cat config.Catalog) error {
	// An installed snapshot holds only the blueprints and the foundation a
	// configuration used, not the catalog's own file, and so no wizard,
	// which only a new deployment's `paisans create` asks.
	var wizard []WizardEntry
	if _, err := os.Stat(filepath.Join(dir, "catalog.yaml")); err == nil || fetched(dir) {
		c, err := ReadCatalog(dir)
		if err != nil {
			return err
		}
		wizard = c.Wizard
	}
	f, err := readFoundation(dir)
	if err != nil {
		return err
	}
	names, err := Blueprints(dir)
	if err == nil {
		err = Checkout(dir, names...)
	}
	if err != nil {
		return err
	}
	read := map[string]*Def{}
	at := map[string]string{}
	for _, name := range names {
		if reservedName(name) {
			return fmt.Errorf("it ships %s, a reserved blueprint, which no catalog may provide", name)
		}
		d, err := ReadFrom(dir, name)
		if err != nil {
			return err
		}
		// Recorded at its real path, so a cache reached through a link reads
		// from where the files are.
		bdir, err := filepath.EvalSymlinks(filepath.Join(dir, "blueprints", name))
		if err != nil {
			return err
		}
		read[name], at[name] = d, bdir
	}
	dropCore()
	for name, d := range read {
		register(config.Blueprint(name), d, at[name])
	}
	setFoundation(f)
	core.wizard = wizard
	return nil
}

// Wizard is what `paisans create` offers, as the core catalog it read says.
func Wizard() ([]WizardEntry, error) {
	if err := LoadCore(""); err != nil {
		return nil, err
	}
	core.Lock()
	defer core.Unlock()
	if len(core.wizard) == 0 {
		return nil, fmt.Errorf("the core catalog at %s offers nothing to `paisans create`", core.release.Tag)
	}
	return append([]WizardEntry(nil), core.wizard...), nil
}

// lastCoreFile records, in the catalog cache, the core release this
// workstation last read, for readCore's fallback.
const lastCoreFile = "core-release.yaml"

func rememberCore(cat config.Catalog) {
	cache, err := CacheDir()
	if err != nil {
		return
	}
	raw, err := yaml.Marshal(cat)
	if err != nil {
		return
	}
	// Best effort: a fallback that is missing costs only an offline run.
	_ = os.MkdirAll(cache, 0o755)
	_ = os.WriteFile(filepath.Join(cache, lastCoreFile), raw, 0o644)
}

func lastCore(url string) (config.Catalog, bool) {
	cache, err := CacheDir()
	if err != nil {
		return config.Catalog{}, false
	}
	raw, err := os.ReadFile(filepath.Join(cache, lastCoreFile))
	if err != nil {
		return config.Catalog{}, false
	}
	var cat config.Catalog
	if yaml.Unmarshal(raw, &cat) != nil || cat.URL != url || cat.Commit == "" {
		return config.Catalog{}, false
	}
	return cat, true
}

// isCore reports whether a blueprint is named the way a core one is:
// unqualified and not reserved.
func isCore(blueprint config.Blueprint) bool {
	return !strings.Contains(string(blueprint), "/") && !reservedName(string(blueprint))
}

// dropCore drops every core blueprint recorded so far.
func dropCore() {
	mu.Lock()
	defer mu.Unlock()
	for k, d := range defs {
		if !d.Reserved && !strings.Contains(string(k), "/") {
			delete(defs, k)
			delete(sources, k)
		}
	}
}

// overrideCore reads the core catalog a configuration pins, at dir, in place
// of the newest release, and counts the core as read so the newest is not
// read over it.
func overrideCore(dir string, cat config.Catalog) []string {
	core.Lock()
	defer core.Unlock()
	core.done, core.err, core.release, core.skipped = true, nil, cat, nil
	if err := readCoreAt(dir, cat); err != nil {
		core.err = fmt.Errorf("catalogs.core: %w", err)
		return []string{core.err.Error() + "."}
	}
	return nil
}

// CorePin is the catalog a blueprint's catalog name stands for in cfg. For
// the core catalog that is the configuration's own pin when it has one, and
// else the release this process read, which it reads first if it has not.
func CorePin(cfg *config.Config, catalog string) config.Catalog {
	cat := cfg.Catalogs[catalog]
	if catalog != config.CoreCatalog || cat.Commit != "" {
		return cat
	}
	LoadCore(cat.URL)
	release, _ := CoreRelease()
	if cat.URL == "" {
		cat.URL = release.URL
	}
	cat.Tag, cat.Commit = release.Tag, release.Commit
	return cat
}
