// Package localstore is the workstation's own copy of every deployment it has
// applied or fetched: the three files of the version last saved, with the
// configuration store's state beside them, one directory per deployment id
// under the operator's data directory.
//
// A deployment's files usually sit in a directory the operator chose, often a
// git repository, and that directory is easy to lose or to forget the path
// of. Every deploy (deployment apply --execute, deployment publish
// --execute) makes the version it published the copy here, and
// paisans deployment fetch fills it from a host, so an operator can always
// find each deployment they run without remembering where its files were.
//
// Each deployment's directory is a configstore workstation in its own right
// (paisans-stack.yaml, secrets.enc.yaml, .sops.yaml and .paisans/), so a
// fetch judges what a host serves by the same trust rules as deployment pull,
// anchored on the key that signed the version apply saved, and every
// paisans command that takes --config works on a saved copy as it is.
package localstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// DirVariable names a directory to use in place of the platform's data
// directory, for an operator who keeps their data elsewhere and for tests.
const DirVariable = "PAISANS_DATA_DIR"

// SavedFile is the record beside a deployment's files that says what they
// are, for deployment list.
const SavedFile = "saved.json"

// Status is how far a saved copy is trusted.
type Status string

const (
	// Verified is a version the trust rules chose as the head: what apply
	// published, or what a fetch judged from the anchor apply recorded.
	Verified Status = "verified"
	// Unverified is a version a host served for a deployment this
	// workstation holds no anchor for. Its files are set aside in
	// .paisans/incoming/<sha>/ and never written as the copy's own.
	Unverified Status = "unverified"
	// Fork is two trusted heads, each set aside in .paisans/incoming/<sha>/.
	Fork Status = "fork"
)

// Saved is what saved.json records about one deployment's copy.
type Saved struct {
	ID       string    `json:"id"`
	Domain   string    `json:"domain,omitempty"`
	SHA      string    `json:"sha"`
	Revision int       `json:"revision"`
	Status   Status    `json:"status"`
	Source   string    `json:"source"`
	SavedAt  time.Time `json:"saved_at"`
}

// Base is where saved deployments live: $PAISANS_DATA_DIR when set; on macOS
// ~/Library/Application Support/Paisans, where an application keeps the data
// it makes; elsewhere $XDG_DATA_HOME/paisans, or ~/.local/share/paisans when
// that is unset, by the XDG Base Directory convention.
func Base() (string, error) {
	if dir := os.Getenv(DirVariable); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home directory to keep saved deployments in: %w. Set %s", err, DirVariable)
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Paisans"), nil
	}
	// The specification ignores a relative XDG_DATA_HOME.
	if xdg := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "paisans"), nil
	}
	return filepath.Join(home, ".local", "share", "paisans"), nil
}

// Store is the saved deployments under one base directory.
type Store struct{ Base string }

// Open is the store at Base.
func Open() (Store, error) {
	base, err := Base()
	return Store{Base: base}, err
}

func (s Store) root() string { return filepath.Join(s.Base, "deployments") }

// Local is deployment id's copy as a configstore workstation. The id is
// checked first: a fetch reads it from a host's registry, which whoever has
// root on that host can write, and it becomes a path here.
func (s Store) Local(id string) (configstore.Local, error) {
	if !deployment.ValidID(id) {
		return configstore.Local{}, fmt.Errorf("%q is not a deployment id", id)
	}
	// The copies hold encrypted secrets and name every community the
	// operator runs, which is nobody else's business on a shared machine.
	if err := os.MkdirAll(s.root(), 0o700); err != nil {
		return configstore.Local{}, err
	}
	dir := filepath.Join(s.root(), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return configstore.Local{}, err
	}
	return configstore.Local{Dir: dir}, nil
}

// Held is deployment id's copy when the store holds one, opened without
// creating anything, and false when it holds none. A copy is a directory
// with a store or a head: what a recovery, a join, a fetch or a deploy
// wrote. An empty directory, or one holding only a record, is none, so
// nothing but those writers ever makes a copy that trust is read from.
func (s Store) Held(id string) (configstore.Local, bool, error) {
	if !deployment.ValidID(id) {
		return configstore.Local{}, false, fmt.Errorf("%q is not a deployment id", id)
	}
	dir := filepath.Join(s.root(), id)
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return configstore.Local{}, false, nil
	}
	if err != nil {
		return configstore.Local{}, false, err
	}
	if !info.IsDir() {
		return configstore.Local{}, false, fmt.Errorf("%s is not a directory", dir)
	}
	l := configstore.Local{Dir: dir}
	ok, err := isCopy(l)
	if err != nil || !ok {
		return configstore.Local{}, false, err
	}
	return l, true, nil
}

// isCopy reports whether l holds a store or a head.
func isCopy(l configstore.Local) (bool, error) {
	if _, ok, err := l.HeldStore(); err != nil || ok {
		return ok, err
	}
	head, err := l.Head()
	return head != "", err
}

// locksDir holds one lock file per deployment, outside deployments/, so
// taking a lock never makes a directory that reads as a copy.
func (s Store) locksDir() string { return filepath.Join(s.Base, "locks") }

// LockedError is a lock another writer holds.
type LockedError struct {
	ID string
	// Holder is what the lock file says: the holder's pid and command line.
	Holder string
}

func (e *LockedError) Error() string {
	holder := e.Holder
	if holder == "" {
		holder = "another process"
	} else if pid, cmd, ok := strings.Cut(holder, " "); ok {
		holder = "pid " + pid + " (" + cmd + ")"
	} else {
		holder = "pid " + holder
	}
	return "the system copy of " + e.ID + " is being written by " + holder
}

// Lock takes the lock on deployment id's copy, <Base>/locks/<id>.lock,
// which every command that writes the copy holds while it does, so two
// writers never interleave. It does not wait: a lock another process holds
// is a *LockedError naming it. The lock is the operating system's, released
// when its holder exits, so a lock file a process left behind when it died
// does not block the next. It is taken before a copy exists as well, by a
// recovery, and creates nothing under deployments/. The returned func
// releases the lock and leaves the file.
func (s Store) Lock(id string) (unlock func(), err error) {
	if !deployment.ValidID(id) {
		return nil, fmt.Errorf("%q is not a deployment id", id)
	}
	if err := os.MkdirAll(s.locksDir(), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.locksDir(), id+".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder, _ := io.ReadAll(io.LimitReader(f, 4096))
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &LockedError{ID: id, Holder: strings.TrimSpace(string(holder))}
		}
		return nil, fmt.Errorf("locking %s: %w", f.Name(), err)
	}
	unlock = func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
	line := strconv.Itoa(os.Getpid()) + " " + strings.Join(os.Args, " ") + "\n"
	if err := f.Truncate(0); err != nil {
		unlock()
		return nil, err
	}
	if _, err := f.WriteAt([]byte(line), 0); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

// Record writes rec as deployment rec.ID's saved.json.
func (s Store) Record(rec Saved) error {
	l, err := s.Local(rec.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(l.Dir, ".saved-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(l.Dir, SavedFile))
}

// Load is deployment id's saved.json, and false when it has none.
func (s Store) Load(id string) (Saved, bool, error) {
	if !deployment.ValidID(id) {
		return Saved{}, false, fmt.Errorf("%q is not a deployment id", id)
	}
	data, err := os.ReadFile(filepath.Join(s.root(), id, SavedFile))
	if errors.Is(err, os.ErrNotExist) {
		return Saved{}, false, nil
	}
	if err != nil {
		return Saved{}, false, err
	}
	var rec Saved
	if err := json.Unmarshal(data, &rec); err != nil {
		return Saved{}, false, fmt.Errorf("%s: %w", filepath.Join(s.root(), id, SavedFile), err)
	}
	return rec, true, nil
}

// Listed is one directory in the store: its record, or why it has none that
// reads.
type Listed struct {
	Saved
	Dir string
	Err error
}

// List is every copy in the store, by id. A copy whose record is missing or
// unreadable is listed with Err, not left out, since it still holds whatever
// was written into it. A directory that is no copy, holding no store and no
// head, is left out: there is nothing in it to list.
func (s Store) List() ([]Listed, error) {
	entries, err := os.ReadDir(s.root())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Listed
	for _, e := range entries {
		if !e.IsDir() || !deployment.ValidID(e.Name()) {
			continue
		}
		l := Listed{Dir: filepath.Join(s.root(), e.Name())}
		if ok, err := isCopy(configstore.Local{Dir: l.Dir}); err == nil && !ok {
			continue
		} else if err != nil {
			l.ID, l.Err = e.Name(), err
			out = append(out, l)
			continue
		}
		rec, ok, err := s.Load(e.Name())
		switch {
		case err != nil:
			l.Err = err
		case !ok:
			l.Err = fmt.Errorf("no %s", SavedFile)
		}
		if ok && err == nil {
			l.Saved = rec
		}
		l.ID = e.Name()
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
