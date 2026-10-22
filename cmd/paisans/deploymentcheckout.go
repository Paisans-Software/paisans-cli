package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/localstore"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// What follows is `paisans deployment checkout`: a deployment saved on this
// workstation (deployment list) copied into an empty directory, so its files
// can be edited and published from there. It reads the saved deployments
// only and reaches no host.
//
// The copy is a workstation in its own right, and it is worth having only
// because it carries the saved copy's trust: the store with every version and
// its signature, the signing key and pinned root trust starts from, the head
// the files are, and the highest revision seen. Without them deployment pull
// and publish would treat the directory as a new workstation with nothing to
// trust from. So the trust state is checked before anything is written, from
// the saved copy's own anchor, and checked again in the copy once it is
// written; a copy that does not judge the same is removed.

// checkoutState is the trust state a checkout copies from .paisans/, in the
// order it is written. config-head is last of them, since it says which
// version the files are, and the files are written after it. The store,
// .paisans/store.git, is written first, through a bundle, and incoming/,
// a pull's scratch, is not copied.
var checkoutState = []string{"config-root", "config-seen", "signing-key", "config-head"}

// checkoutFiles are the three files of a version, written last.
var checkoutFiles = []string{config.FileName, "secrets.enc.yaml", ".sops.yaml"}

func runDeploymentCheckout(args []string) error {
	fs := newFlagSet("deployment checkout", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "where to write the deployment declaration, in an empty directory; the store's state is written to .paisans/ beside it")
	execute := fs.Bool("execute", false, "write the files and the store's state")
	pos, err := parseAll(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return &ui.Problem{Hint: "deployment checkout takes one deployment", Explain: "Name it by its id, a unique start of the id, or its domain, as paisans deployment list prints them."}
	}
	path, err := filepath.Abs(*configPath)
	if err != nil {
		return err
	}
	dst, err := workstationOf(path)
	if err != nil {
		return err
	}
	if dst.HasFiles() {
		return &ui.Problem{Hint: "there are configuration files here already", Explain: "Checkout writes onto a workstation with nothing, so it never overwrites a file. Use paisans deployment pull where the files exist, or check out into an empty directory with --config."}
	}
	if held := heldState(dst.Dir); len(held) > 0 {
		return &ui.Problem{Hint: "there is store state here already", Explain: "Checkout writes onto a workstation with nothing, so it never overwrites a file, and " + strings.Join(held, ", ") + " would be replaced. Check out into an empty directory with --config."}
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	saved, err := findSaved(st, pos[0])
	if err != nil {
		return err
	}
	src := configstore.Local{Dir: saved.Dir}
	trust, err := judgeSaved(src, saved.Saved)
	if err != nil {
		return err
	}
	r := reporter()
	what := fmt.Sprintf("revision %d, %s", saved.Revision, saved.SHA[:12])
	r.Section(fmt.Sprintf("check out %s (%s), %s, from %s", saved.ID, orDash(saved.Domain), what, saved.Dir))
	if !*execute {
		for _, p := range checkoutPaths(src, dst) {
			r.Step("write "+p).End(ui.Pending, "--execute will write it")
		}
		r.Result("nothing was written; --execute writes %s into %s", what, dst.Dir)
		return nil
	}
	s := r.Step("copy the store, its state and the files into " + dst.Dir)
	var differs *differsError
	if err := copyCheckout(src, dst, saved.ID, trust); errors.As(err, &differs) {
		s.Fail(err)
		return &ui.Problem{Hint: "the copy does not judge as the saved copy does", Explain: errLine(err) + ". What was written is removed; the saved copy is as it was.", Cause: err}
	} else if err != nil {
		s.Fail(err)
		return err
	}
	s.Done(saved.SHA[:12] + " trusted, as in the saved copy")
	r.Note("the saved copy may be behind the sites", "Run paisans deployment pull --config "+path+" to bring any newer version the sites hold.")
	r.Result("checked out %s of %s into %s", what, saved.ID, dst.Dir)
	return nil
}

// heldState is the trust state already in dir's .paisans/, which a checkout
// would replace.
func heldState(dir string) []string {
	var held []string
	for _, name := range append([]string{"store.git"}, checkoutState...) {
		p := filepath.Join(dir, ".paisans", name)
		if _, err := os.Lstat(p); err == nil {
			held = append(held, p)
		}
	}
	return held
}

// findSaved is the saved deployment what names: its id, the one id that
// starts with it, or the one deployment with it as its domain.
func findSaved(st localstore.Store, what string) (localstore.Listed, error) {
	listed, err := st.List()
	if err != nil {
		return localstore.Listed{}, err
	}
	var found []localstore.Listed
	for _, l := range listed {
		if l.ID == what {
			found = []localstore.Listed{l}
			break
		}
		if strings.HasPrefix(l.ID, what) || (l.Err == nil && l.Domain != "" && l.Domain == what) {
			found = append(found, l)
		}
	}
	switch len(found) {
	case 0:
		return localstore.Listed{}, &ui.Problem{Hint: fmt.Sprintf("no saved deployment is %q", what), Explain: "Name one by its id, a unique start of the id, or its domain, as paisans deployment list prints them. deployment fetch --ssh <user@host> saves every deployment a host holds."}
	case 1:
	default:
		var ids []string
		for _, l := range found {
			ids = append(ids, l.ID)
		}
		return localstore.Listed{}, &ui.Problem{Hint: fmt.Sprintf("%q names more than one saved deployment", what), Explain: "Give more of the id: " + strings.Join(ids, ", ") + ". paisans deployment list prints them."}
	}
	l := found[0]
	if l.Err != nil {
		return l, &ui.Problem{Hint: "the saved copy of " + l.ID + " cannot be read", Explain: errLine(l.Err) + ", in " + l.Dir + ". deployment fetch --ssh <user@host> saves it again from a host that holds it.", Cause: l.Err}
	}
	recoverLine := "paisans deployment recover --from <user@host> --id " + l.ID + " --root <sha> --config <empty directory>/" + config.FileName + " --execute"
	switch l.Status {
	case localstore.Verified:
		return l, nil
	case localstore.Unverified:
		return l, &ui.Problem{Hint: "the saved copy of " + l.ID + " is unverified", Explain: fmt.Sprintf("It was fetched from a host this workstation holds no key or root for, so it has no trusted version to check out; its newest versions are only in %s/<sha>/. Check the hash with another operator, then pin it: %s.", filepath.Join(l.Dir, ".paisans", "incoming"), recoverLine)}
	case localstore.Fork:
		return l, &ui.Problem{Hint: "the saved copy of " + l.ID + " is a fork", Explain: fmt.Sprintf("Two trusted versions build on one parent, so there is no one version to check out; each head's files are in %s/<sha>/. Recover the deployment into an empty directory instead: %s.", filepath.Join(l.Dir, ".paisans", "incoming"), recoverLine)}
	}
	return l, &ui.Problem{Hint: fmt.Sprintf("the saved copy of %s has status %q", l.ID, l.Status), Explain: "Only a verified copy is checked out. deployment fetch --ssh <user@host> saves it again."}
}

// savedTrust is what the trust rules make of a saved copy, from its own
// anchor: the version its files are, and every version its store holds.
type savedTrust struct {
	head     string
	revision int
	anchor   configstore.Anchor
	versions []string
	files    configstore.Files
}

// judgeSaved judges the saved copy src as deployment log would, from its own
// anchor and reaching no site, and refuses one whose head is not a trusted
// version at the revision its record says, or whose files are not that
// version's.
func judgeSaved(src configstore.Local, rec localstore.Saved) (savedTrust, error) {
	again := "deployment fetch --ssh <user@host> saves it again from a host that holds it."
	if _, err := os.Stat(filepath.Join(src.Dir, ".paisans", "store.git", "HEAD")); err != nil {
		return savedTrust{}, &ui.Problem{Hint: "the saved copy of " + rec.ID + " has no store", Explain: "Nothing in " + filepath.Join(src.Dir, ".paisans", "store.git") + " to check the version against. " + again, Cause: err}
	}
	head, err := src.Head()
	if err != nil {
		return savedTrust{}, err
	}
	if head == "" || head != rec.SHA {
		return savedTrust{}, &ui.Problem{Hint: "the saved copy of " + rec.ID + " is not the version it records", Explain: fmt.Sprintf("Its record names %s and %s names %q. %s", rec.SHA, filepath.Join(src.Dir, ".paisans", "config-head"), head, again)}
	}
	t, err := judge(src, rec.ID)
	if errors.Is(err, configstore.ErrNoAnchor) {
		return t, &ui.Problem{Hint: "the saved copy of " + rec.ID + " has no version to start trusting from", Explain: err.Error() + ". Check the root's hash with another operator, then pin it: paisans deployment recover --from <user@host> --id " + rec.ID + " --root <sha> --config <empty directory>/" + config.FileName + " --execute.", Cause: err}
	}
	if err != nil {
		return t, err
	}
	if t.head != head || t.revision != rec.Revision {
		return t, &ui.Problem{Hint: "the saved copy of " + rec.ID + " is not trusted from its own anchor", Explain: fmt.Sprintf("Its record names revision %d, %s, and its store does not hold that as a trusted version. %s", rec.Revision, rec.SHA, again)}
	}
	files, err := readCheckoutFiles(src.Dir)
	if err != nil {
		return t, err
	}
	if !bytes.Equal(files.Paisans, t.files.Paisans) || !bytes.Equal(files.Secrets, t.files.Secrets) || !bytes.Equal(files.SOPS, t.files.SOPS) {
		return t, &ui.Problem{Hint: "the saved copy of " + rec.ID + " has files that are not its version", Explain: fmt.Sprintf("The files in %s differ from revision %d, %s, which it records. %s", src.Dir, rec.Revision, rec.SHA[:12], again)}
	}
	return t, nil
}

// judge is l's head as its own anchor judges it: the head, trusted, with its
// revision and files, and every version l's store holds. A head older than
// one l has seen is kept, as l has it; deployment pull asks about it there
// as it would here.
func judge(l configstore.Local, id string) (savedTrust, error) {
	var t savedTrust
	head, err := l.Head()
	if err != nil {
		return t, err
	}
	if t.anchor, err = l.Anchor(); err != nil {
		return t, err
	}
	g, err := configstore.GatherAnchored(l, nil, 0, id, t.anchor, configstore.GatherOptions{})
	var rb *configstore.RollbackError
	if errors.As(err, &rb) {
		err = nil
	}
	if err != nil {
		return t, err
	}
	t.versions = slices.Sorted(maps.Keys(g.Versions))
	if !g.Verdict.Trusted[head] {
		return t, nil
	}
	t.head, t.revision = head, g.Versions[head].Revision
	cache, err := l.Cache()
	if err != nil {
		return t, err
	}
	t.files, err = cache.Files(head)
	return t, err
}

// differsError is a checkout that does not judge as the saved copy did.
type differsError struct{ what string }

func (e *differsError) Error() string { return "the copy " + e.what }

// sameTrust checks the checkout dst judges as the saved copy did: the same
// anchor, the same head at the same revision, trusted, the same versions in
// its store, and the head's files.
func sameTrust(dst configstore.Local, id string, want savedTrust) error {
	got, err := judge(dst, id)
	if errors.Is(err, configstore.ErrNoAnchor) {
		return &differsError{"has no version to start trusting from"}
	}
	if err != nil {
		return err
	}
	switch {
	case !sameAnchor(got.anchor, want.anchor):
		return &differsError{fmt.Sprintf("is anchored on %+v, the saved copy on %+v", got.anchor, want.anchor)}
	case got.head != want.head || got.revision != want.revision:
		return &differsError{fmt.Sprintf("trusts head %q at revision %d, the saved copy %s at %d", got.head, got.revision, want.head, want.revision)}
	case !slices.Equal(got.versions, want.versions):
		return &differsError{fmt.Sprintf("holds %d versions, the saved copy %d", len(got.versions), len(want.versions))}
	}
	files, err := readCheckoutFiles(dst.Dir)
	if err != nil {
		return err
	}
	if !bytes.Equal(files.Paisans, want.files.Paisans) || !bytes.Equal(files.Secrets, want.files.Secrets) || !bytes.Equal(files.SOPS, want.files.SOPS) {
		return &differsError{"has files that are not the head's"}
	}
	return nil
}

// readCheckoutFiles reads the three files in dir as they are on disk.
func readCheckoutFiles(dir string) (configstore.Files, error) {
	var f configstore.Files
	for i, dst := range []*[]byte{&f.Paisans, &f.Secrets, &f.SOPS} {
		data, err := os.ReadFile(filepath.Join(dir, checkoutFiles[i]))
		if err != nil {
			return f, err
		}
		*dst = data
	}
	return f, nil
}

// checkoutPaths is every path a checkout of src into dst writes, in full.
func checkoutPaths(src, dst configstore.Local) []string {
	state := filepath.Join(dst.Dir, ".paisans")
	out := []string{filepath.Join(state, "store.git")}
	if _, err := os.Lstat(filepath.Join(state, ".gitignore")); err != nil {
		out = append(out, filepath.Join(state, ".gitignore"))
	}
	for _, name := range checkoutState {
		if _, err := os.Lstat(filepath.Join(src.Dir, ".paisans", name)); err == nil {
			out = append(out, filepath.Join(state, name))
		}
	}
	for _, name := range checkoutFiles {
		out = append(out, filepath.Join(dst.Dir, name))
	}
	return out
}

// copyCheckout writes src's store, trust state and files into dst. The store
// is brought over as a bundle, which carries every version with its
// signature, through the same receive rule a gather uses. Each file is
// created, never replaced, with the mode the saved copy's has, so the
// secrets and the signing key stay as private as they were. The copy is then
// judged from its own anchor and must judge as the saved copy did
// (sameTrust). On a failure, that check's included, everything written is
// removed.
func copyCheckout(src, dst configstore.Local, id string, t savedTrust) (err error) {
	if err := config.CheckSecretsBytes(filepath.Join(src.Dir, "secrets.enc.yaml"), t.files.Secrets); err != nil {
		return err
	}
	if err := os.MkdirAll(dst.Dir, 0o755); err != nil {
		return err
	}
	_, statErr := os.Lstat(filepath.Join(dst.Dir, ".paisans"))
	madeState := errors.Is(statErr, fs.ErrNotExist)
	var written []string
	defer func() {
		if err == nil {
			return
		}
		for _, p := range slices.Backward(written) {
			os.RemoveAll(p)
		}
		if madeState {
			os.RemoveAll(filepath.Join(dst.Dir, ".paisans"))
		}
	}()
	from, err := src.Cache()
	if err != nil {
		return err
	}
	bundle, err := from.Bundle()
	if err != nil {
		return err
	}
	if bundle == nil {
		return fmt.Errorf("%s holds no version", filepath.Join(src.Dir, ".paisans", "store.git"))
	}
	if _, err := os.Lstat(filepath.Join(dst.Dir, ".paisans", ".gitignore")); errors.Is(err, fs.ErrNotExist) {
		written = append(written, filepath.Join(dst.Dir, ".paisans", ".gitignore"))
	}
	written = append(written, filepath.Join(dst.Dir, ".paisans", "store.git"))
	to, err := dst.Cache()
	if err != nil {
		return err
	}
	if err := to.FetchBundle(bundle); err != nil {
		return err
	}
	for _, name := range checkoutState {
		p, err := copyNew(filepath.Join(src.Dir, ".paisans", name), filepath.Join(dst.Dir, ".paisans", name))
		if p != "" {
			written = append(written, p)
		}
		if err != nil {
			return err
		}
	}
	for _, name := range checkoutFiles {
		p, err := copyNew(filepath.Join(src.Dir, name), filepath.Join(dst.Dir, name))
		if p != "" {
			written = append(written, p)
		}
		if err != nil {
			return err
		}
	}
	return sameTrust(dst, id, t)
}

// copyNew creates dst with src's contents and mode, and refuses a dst that
// exists. A src that does not exist is nothing to copy. It returns dst once
// it created it, so a failure after that can remove it.
func copyNew(src, dst string) (string, error) {
	info, err := os.Lstat(src)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", src)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return dst, err
	}
	if err := f.Close(); err != nil {
		return dst, err
	}
	// The umask may have narrowed the mode OpenFile was given.
	return dst, os.Chmod(dst, info.Mode().Perm())
}

// sameAnchor reports whether two anchors start trust from the same keys and
// the same pinned root.
func sameAnchor(a, b configstore.Anchor) bool {
	return a.Root == b.Root && a.Named == b.Named && slices.Equal(a.Signers, b.Signers)
}
