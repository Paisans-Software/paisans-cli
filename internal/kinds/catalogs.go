package kinds

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// A deployment's apps may name blueprints from catalogs: git repositories it
// declares under `catalogs` and pins to a commit (docs/specs/2026-10-12-
// blueprints-and-catalogs.md, "Catalogs"). Loading the configuration resolves
// them: each catalog an app names is fetched once into a cache keyed by its
// commit, and the blueprints the apps name are read from it and recorded
// beside the built in ones, under their qualified names (example/journal).

func init() {
	config.ResolveBlueprints = resolve
}

// CatalogAPIVersion is the catalog format this toolkit reads.
const CatalogAPIVersion = "paisans.catalog/v1"

// mu guards defs and sources, which a configuration's catalogs add to.
var mu sync.RWMutex

// sources is, for each kind read from a catalog, the directory holding its
// blueprint: its templates are read from there.
var sources = map[config.Kind]string{}

// catalogFile is a catalog's catalog.yaml.
type catalogFile struct {
	APIVersion      string `yaml:"apiVersion"`
	Name            string `yaml:"name"`
	RequiresToolkit string `yaml:"requires_toolkit"`
}

// CacheDir is where fetched catalogs are kept, one directory per commit: the
// user's cache directory unless PAISANS_CATALOG_CACHE names another. A
// commit's content never changes, so a directory there is never refreshed,
// only used or fetched; losing the cache costs a fetch.
func CacheDir() (string, error) {
	if dir := os.Getenv("PAISANS_CATALOG_CACHE"); dir != "" {
		return dir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "paisans-stack", "catalogs"), nil
}

// completeMark is written last into a fetched catalog's directory, so a
// fetch that was interrupted is fetched again rather than used half done.
const completeMark = ".paisans-complete"

// stateDir is, inside a fetched catalog's .git, what the toolkit records
// about the checkout: a mark per blueprint whose files are on disk, the tags
// checked to name the commit, and the lock. It sits inside .git so that the
// checkout holds only the catalog's own files.
const stateDir = "paisans"

// Fetch is the directory holding a catalog at its pinned commit, fetched
// into the cache when it is not there yet. A fetch takes the commit's trees
// and no file contents, and checks out only catalog.yaml: Checkout adds the
// blueprints in use. A tag, when given, must name the commit.
func Fetch(cat config.Catalog) (string, error) {
	cache, err := CacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, cat.Commit)
	if _, err := os.Stat(filepath.Join(dir, completeMark)); err == nil {
		return dir, checkTag(dir, cat)
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	work, err := os.MkdirTemp(cache, ".fetch-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", cat.URL},
	} {
		if out, err := git(work, args...); err != nil {
			return "", fmt.Errorf("preparing a checkout of %s: %s", cat.URL, out)
		}
	}
	if out, err := git(work, "fetch", "--quiet", "--depth", "1", "--filter=blob:none", "origin", cat.Commit); err != nil {
		// A forge that answers but refuses the commit has no such commit;
		// one that does not answer could not be fetched at all.
		if _, lsErr := git("", "ls-remote", cat.URL, "HEAD"); lsErr == nil {
			return "", fmt.Errorf("%s has no commit %s: %s", cat.URL, cat.Commit, out)
		}
		return "", fmt.Errorf("fetching %s: %s", cat.URL, out)
	}
	for _, args := range [][]string{
		{"sparse-checkout", "set", "--no-cone", "/catalog.yaml"},
		{"-c", "advice.detachedHead=false", "checkout", "--quiet", cat.Commit},
	} {
		if out, err := git(work, args...); err != nil {
			return "", fmt.Errorf("checking out %s at %s: %s", cat.URL, cat.Commit, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(work, ".git", stateDir, "complete"), 0o755); err != nil {
		return "", err
	}
	if err := checkTag(work, cat); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(work, completeMark), nil, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(work, dir); err != nil {
		// Another fetch of the same commit finished first, which is as good.
		if _, statErr := os.Stat(filepath.Join(dir, completeMark)); statErr == nil {
			return dir, checkTag(dir, cat)
		}
		return "", err
	}
	return dir, nil
}

// fetched reports whether dir is a catalog Fetch made, rather than an
// installed snapshot, which holds its blueprints' files already.
func fetched(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git", stateDir))
	return err == nil
}

// Checkout makes sure the named blueprints' files are in a fetched catalog,
// fetching the contents of the ones that are not. A name the catalog does
// not hold is left for ReadFrom to refuse.
func Checkout(dir string, names ...string) error {
	if !fetched(dir) {
		return nil
	}
	var missing []string
	for _, name := range names {
		if !config.BlueprintName(name) {
			return fmt.Errorf("%q is not a blueprint name", name)
		}
		if !checkedOut(dir, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	unlock, err := lock(dir)
	if err != nil {
		return err
	}
	defer unlock()
	held, err := Blueprints(dir)
	if err != nil {
		return err
	}
	var patterns, paths []string
	for _, name := range missing {
		if !checkedOut(dir, name) && slices.Contains(held, name) {
			patterns = append(patterns, "/blueprints/"+name+"/")
			paths = append(paths, "blueprints/"+name)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	// Adding a pattern checks out what it covers; checking the paths out
	// again restores a blueprint whose files an interrupted checkout left
	// partly written, which git would otherwise keep as local changes.
	if out, err := git(dir, append([]string{"sparse-checkout", "add"}, patterns...)...); err != nil {
		return fmt.Errorf("checking out %s: %s", strings.Join(missing, ", "), out)
	}
	if out, err := git(dir, append([]string{"checkout", "--quiet", "HEAD", "--"}, paths...)...); err != nil {
		return fmt.Errorf("checking out %s: %s", strings.Join(missing, ", "), out)
	}
	for _, p := range paths {
		if err := os.WriteFile(filepath.Join(dir, ".git", stateDir, "complete", path.Base(p)), nil, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// checkedOut reports whether a blueprint's files are on disk in a fetched
// catalog. Its mark is written only once git has finished, so a checkout
// that was interrupted is checked out again.
func checkedOut(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git", stateDir, "complete", name))
	return err == nil
}

// lock takes a fetched catalog's lock, which two processes adding
// blueprints to one checkout need: git sparse-checkout rewrites one file for
// the whole checkout. The lock is released when the process exits, however
// it exits, so a crash leaves nothing to clean up.
func lock(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, ".git", stateDir, "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking %s: %w", dir, err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// checkTag refuses a pin whose tag does not name its commit. A depth 1 fetch
// of a commit brings no tags, so the forge is asked, once per tag: a tag
// checked is recorded in the checkout and not asked about again.
func checkTag(dir string, cat config.Catalog) error {
	if cat.Tag == "" || !fetched(dir) {
		return nil
	}
	mark := filepath.Join(dir, ".git", stateDir, "tags", hex.EncodeToString([]byte(cat.Tag)))
	if _, err := os.Stat(mark); err == nil {
		return nil
	}
	got, err := TagCommit(cat.URL, cat.Tag)
	if err != nil {
		return err
	}
	if got != cat.Commit {
		return fmt.Errorf("%s's tag %s names %s, not the pinned %s. A tag that moved is refused; `paisans catalog update` moves a pin on purpose", cat.URL, cat.Tag, got, cat.Commit)
	}
	if err := os.MkdirAll(filepath.Dir(mark), 0o755); err != nil {
		return err
	}
	return os.WriteFile(mark, []byte(cat.Tag+"\n"), 0o644)
}

// git runs git, in dir when it is not empty, and returns its combined output.
func git(dir string, args ...string) (string, error) {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

// ReadCatalog reads a fetched catalog's catalog.yaml.
func ReadCatalog(dir string) (catalogFile, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "catalog.yaml"))
	if err != nil {
		return catalogFile{}, fmt.Errorf("it has no catalog.yaml, so it is not a catalog")
	}
	var c catalogFile
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return catalogFile{}, fmt.Errorf("catalog.yaml: %w", err)
	}
	if c.APIVersion != CatalogAPIVersion {
		return catalogFile{}, fmt.Errorf("catalog.yaml: apiVersion is %q, and this toolkit reads %s", c.APIVersion, CatalogAPIVersion)
	}
	return c, nil
}

// Blueprints is the names of the blueprints a fetched catalog holds, sorted.
// They are read from the commit's trees, so no blueprint's files need be on
// disk; an installed snapshot is read from its directory.
func Blueprints(dir string) ([]string, error) {
	if fetched(dir) {
		out, err := git(dir, "ls-tree", "-d", "--name-only", "HEAD", "blueprints/")
		if err != nil {
			return nil, fmt.Errorf("listing its blueprints: %s", out)
		}
		var names []string
		for _, line := range strings.Split(out, "\n") {
			if name, ok := strings.CutPrefix(line, "blueprints/"); ok && name != "" {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("it has no blueprints directory")
		}
		sort.Strings(names)
		return names, nil
	}
	entries, err := os.ReadDir(filepath.Join(dir, "blueprints"))
	if err != nil {
		return nil, fmt.Errorf("it has no blueprints directory")
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// ReadFrom reads one blueprint from a fetched catalog, checking it out
// first.
func ReadFrom(dir, name string) (*Def, error) {
	if err := Checkout(dir, name); err != nil {
		return nil, err
	}
	bdir := filepath.Join(dir, "blueprints", name)
	raw, err := os.ReadFile(filepath.Join(bdir, blueprints.File))
	if err != nil {
		return nil, fmt.Errorf("the catalog has no blueprint %s", name)
	}
	hasTemplate := func(file string) bool {
		_, err := os.Stat(filepath.Join(bdir, filepath.FromSlash(file)))
		return err == nil
	}
	return Parse(name, raw, hasTemplate)
}

// resolve reads every catalog blueprint the configuration's apps name, and
// the core catalog's blueprints when the configuration pins core to a
// commit of its own, and records them. It returns a problem per app or
// catalog it could not resolve.
func resolve(cfg *config.Config) []string {
	var problems []string
	fetched := map[string]string{}
	fetch := func(name string) (string, bool) {
		if dir, ok := fetched[name]; ok {
			return dir, dir != ""
		}
		cat := cfg.Catalogs[name]
		if name == config.CoreCatalog && cat.URL == "" {
			pin, err := blueprints.CorePin()
			if err != nil {
				problems = append(problems, fmt.Sprintf("catalogs.core: %v", err))
				fetched[name] = ""
				return "", false
			}
			cat.URL = pin.URL
		}
		if dir, ok := snapshotDir(cat.Commit); ok {
			fetched[name] = dir
			return dir, true
		}
		dir, err := Fetch(cat)
		if err == nil {
			_, err = ReadCatalog(dir)
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("catalogs.%s: %v.", name, err))
			fetched[name] = ""
			return "", false
		}
		fetched[name] = dir
		return dir, true
	}

	if core, ok := cfg.Catalogs[config.CoreCatalog]; ok && core.Commit != "" {
		if dir, ok := fetch(config.CoreCatalog); ok {
			problems = append(problems, overrideCore(dir)...)
		}
	}

	for _, app := range cfg.AppNames() {
		kind := cfg.Apps[app].Kind
		catalog, name := config.SplitBlueprint(kind)
		key := "apps." + app + ".blueprint"
		if catalog == "" {
			if _, ok := For(kind); !ok {
				problems = append(problems, fmt.Sprintf("%s: unknown blueprint %q.", key, kind))
			}
			continue
		}
		if reservedName(name) {
			problems = append(problems, fmt.Sprintf("%s: %q takes the name of a reserved blueprint, which no catalog may provide. The identity provider, the gate, the invite service and the monitor are built into the toolkit.", key, kind))
			continue
		}
		dir, ok := fetch(catalog)
		if !ok {
			continue
		}
		d, err := ReadFrom(dir, name)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v.", key, err))
			continue
		}
		register(kind, d, filepath.Join(dir, "blueprints", name))
	}
	return problems
}

// overrideCore replaces the built in core blueprints with the ones a
// configuration's own core pin holds. A reserved blueprint is never among
// them: a core catalog that ships one is refused.
func overrideCore(dir string) []string {
	names, err := Blueprints(dir)
	if err == nil {
		err = Checkout(dir, names...)
	}
	if err != nil {
		return []string{fmt.Sprintf("catalogs.core: %v.", err)}
	}
	var problems []string
	for _, name := range names {
		if reservedName(name) {
			problems = append(problems, fmt.Sprintf("catalogs.core: ships %s, a reserved blueprint, which no catalog may provide.", name))
			continue
		}
		d, err := ReadFrom(dir, name)
		if err != nil {
			problems = append(problems, fmt.Sprintf("catalogs.core: %v.", err))
			continue
		}
		register(config.Kind(name), d, filepath.Join(dir, "blueprints", name))
	}
	return problems
}

// reservedName reports whether a name is a reserved blueprint's.
func reservedName(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	d, ok := defs[config.Kind(name)]
	return ok && d.Reserved
}

// register records a blueprint read from a catalog.
func register(kind config.Kind, d *Def, dir string) {
	mu.Lock()
	defer mu.Unlock()
	defs[kind] = d
	sources[kind] = dir
}

// Source is the directory a catalog blueprint was read from, and false for a
// built in one.
func Source(kind config.Kind) (string, bool) {
	mu.RLock()
	defer mu.RUnlock()
	dir, ok := sources[kind]
	return dir, ok
}

// Templates is every known blueprint's templates as one filesystem, each at
// its name (mbin/, example/journal/) with its blueprint.yaml left out: the
// built in ones from the binary, and the ones a configuration's catalogs
// added from where they were fetched.
var Templates fs.FS = templateFS{}

type templateFS struct{}

// locate is the filesystem and path inside it for a path in Templates.
func (templateFS) locate(name string) (fs.FS, string, error) {
	if !fs.ValidPath(name) || name == "." {
		return nil, "", fs.ErrNotExist
	}
	mu.RLock()
	var rest, dir string
	found := false
	for k, d := range sources {
		p := string(k)
		if name == p || strings.HasPrefix(name, p+"/") {
			dir, rest, found = d, strings.TrimPrefix(strings.TrimPrefix(name, p), "/"), true
			break
		}
	}
	mu.RUnlock()
	if !found {
		return blueprints.Templates, name, nil
	}
	if rest == blueprints.File {
		return nil, "", fs.ErrNotExist
	}
	if rest == "" {
		rest = "."
	}
	return hidden{os.DirFS(dir)}, rest, nil
}

func (t templateFS) Open(name string) (fs.File, error) {
	fsys, p, err := t.locate(name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return fsys.Open(p)
}

func (t templateFS) ReadDir(name string) ([]fs.DirEntry, error) {
	fsys, p, err := t.locate(name)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	return fs.ReadDir(fsys, p)
}

func (t templateFS) Stat(name string) (fs.FileInfo, error) {
	fsys, p, err := t.locate(name)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	return fs.Stat(fsys, p)
}

// hidden is a blueprint's directory with its blueprint.yaml left out of
// every listing, so a renderer walking it renders every file it finds.
type hidden struct{ fs.FS }

func (h hidden) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(h.FS, name)
	if err != nil {
		return nil, err
	}
	out := entries[:0]
	for _, e := range entries {
		if name == "." && e.Name() == blueprints.File {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func (h hidden) Open(name string) (fs.File, error) {
	if path.Clean(name) == blueprints.File {
		return nil, &fs.PathError{Op: "open", Path: name, Err: errors.New("a blueprint's description is not a template")}
	}
	return h.FS.Open(name)
}

// The snapshot. Every version of the configuration store carries the catalog
// blueprints its apps use (configstore.Files.Blueprints), keyed
// <catalog commit>/<blueprint>/<file>. Installing it makes those blueprints
// resolvable with no fetch: a checked out or recovered version renders, and a
// removal reads the blueprint of what it removes, even when a catalog has
// gone. A snapshot holds only the blueprints the apps use, so a catalog read
// from one has exactly those.

// snapshotsDir is where installed snapshots are kept, one directory per
// catalog commit with a blueprints/ directory inside, the shape of a fetched
// catalog.
func snapshotsDir() (string, error) {
	cache, err := CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "snapshots"), nil
}

// snapshotDir is an installed snapshot of a catalog commit, if there is one.
func snapshotDir(commit string) (string, bool) {
	base, err := snapshotsDir()
	if err != nil {
		return "", false
	}
	dir := filepath.Join(base, commit)
	if _, err := os.Stat(filepath.Join(dir, completeMark)); err != nil {
		return "", false
	}
	return dir, true
}

// Snapshot is the snapshot of every catalog blueprint the configuration's
// apps use, read from where each was resolved. A configuration that uses no
// catalog has an empty one.
func Snapshot(cfg *config.Config) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, app := range cfg.AppNames() {
		kind := cfg.Apps[app].Kind
		catalog, name := config.SplitBlueprint(kind)
		if catalog == "" {
			catalog = config.CoreCatalog
		}
		cat, declared := cfg.Catalogs[catalog]
		dir, fromCatalog := Source(kind)
		if !declared || !fromCatalog || cat.Commit == "" {
			continue
		}
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[cat.Commit+"/"+name+"/"+filepath.ToSlash(rel)] = data
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SnapshotOf is Snapshot for a configuration held as bytes, which is how the
// configuration store reads one. A configuration that does not load has no
// snapshot rather than an error: the store reads local files to compare them
// as well as to publish them, and publishing loads the file itself first, so
// an invalid one never reaches a signature.
func SnapshotOf(paisans []byte) (map[string][]byte, error) {
	cfg, err := config.LoadBytes(paisans)
	if err != nil {
		return nil, nil
	}
	return Snapshot(cfg)
}

// InstallSnapshot writes a snapshot where resolution finds it. A commit
// already installed is left as it is: its content is the commit's, and the
// configuration store checked the snapshot's SHA-256 before handing it over.
func InstallSnapshot(files map[string][]byte) error {
	base, err := snapshotsDir()
	if err != nil {
		return err
	}
	byCommit := map[string]map[string][]byte{}
	for p, data := range files {
		commit, rest, ok := strings.Cut(p, "/")
		if !ok {
			return fmt.Errorf("snapshot path %s has no commit", p)
		}
		if byCommit[commit] == nil {
			byCommit[commit] = map[string][]byte{}
		}
		byCommit[commit][rest] = data
	}
	for commit, rel := range byCommit {
		if _, ok := snapshotDir(commit); ok {
			continue
		}
		if err := os.MkdirAll(base, 0o755); err != nil {
			return err
		}
		work, err := os.MkdirTemp(base, ".install-")
		if err != nil {
			return err
		}
		for p, data := range rel {
			dest := filepath.Join(work, "blueprints", filepath.FromSlash(p))
			if !strings.HasPrefix(dest, filepath.Join(work, "blueprints")+string(filepath.Separator)) {
				os.RemoveAll(work)
				return fmt.Errorf("snapshot path %s leaves its directory", p)
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				os.RemoveAll(work)
				return err
			}
			if err := os.WriteFile(dest, data, 0o644); err != nil {
				os.RemoveAll(work)
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(work, completeMark), nil, 0o644); err != nil {
			os.RemoveAll(work)
			return err
		}
		if err := os.Rename(work, filepath.Join(base, commit)); err != nil {
			os.RemoveAll(work)
			if _, ok := snapshotDir(commit); !ok {
				return err
			}
		}
	}
	return nil
}
