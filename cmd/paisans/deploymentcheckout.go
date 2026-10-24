package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/draft"
	"github.com/paisans-software/paisans-cli/internal/localstore"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// What follows is `paisans deployment checkout`: a draft started in a folder
// with none, from the system copy of a deployment on this workstation
// (deployment list). It reads the system copy only, writes nothing to it, and
// reaches no host. The folder gets the three files and nothing else; the
// first line of paisans-stack.yaml names the version they were drafted from.
//
// The system copy is judged from its own anchor before anything is written,
// so a draft always starts from a version this workstation trusts. --head
// writes another trusted head of the system copy's store instead, the files
// a hand merge for deployment publish --merge starts from.

// checkoutFiles are the three files of a version.
var checkoutFiles = []string{config.FileName, configstore.FileSecrets, configstore.FileSOPS}

func runDeploymentCheckout(args []string) error {
	fs := newFlagSet("deployment checkout", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "where to write the draft's deployment declaration, in a folder with no draft")
	id := fs.String("id", "", "the deployment to check out, when this workstation holds more than one")
	head := fs.String("head", "", "a trusted head of the system copy's store to write in place of the system copy's own version, as deployment log lists them")
	execute := fs.Bool("execute", false, "write the draft")
	pos, err := parseAll(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return &ui.Problem{Hint: "deployment checkout takes no arguments", Explain: "Name the deployment with --id, as " + progname.Cmd() + " deployment list prints it, when this workstation holds more than one."}
	}
	path, err := filepath.Abs(*configPath)
	if err != nil {
		return err
	}
	d, err := draftAt(path)
	if err != nil {
		return err
	}
	if held := draftFiles(d); len(held) > 0 {
		return &ui.Problem{Hint: "there is a draft here already", Explain: "Checkout starts a draft in a folder with none, so it never overwrites a file, and " + strings.Join(held, ", ") + " would be replaced. Use " + progname.Cmd() + " deployment pull where the draft is, or check out into another folder with --config."}
	}
	sys, _, deploymentID, err := systemFor(path, *id)
	if err != nil {
		return err
	}
	var m draft.Marker
	var files configstore.Files
	if *head == "" {
		saved, err := verifiedRecord(sys, deploymentID)
		if err != nil {
			return err
		}
		if _, err := judgeSaved(sys, saved); err != nil {
			return err
		}
		if m, files, err = systemHead(sys); err != nil {
			return err
		}
	} else if m, files, err = trustedHead(sys, deploymentID, *head); err != nil {
		return err
	}
	if err := config.CheckSecretsBytes(filepath.Join(sys.Dir, configstore.FileSecrets), files.Secrets); err != nil {
		return err
	}
	r := reporter()
	what := fmt.Sprintf("revision %d, %s", m.Revision, shortSHA(m.SHA))
	r.Section(fmt.Sprintf("check out %s, %s, from the system copy in %s", deploymentID, what, sys.Dir))
	if !*execute {
		for _, name := range checkoutFiles {
			r.Step("write "+filepath.Join(d.Dir, name)).End(ui.Pending, "--execute will write it")
		}
		r.Result("nothing was written; --execute writes %s into %s", what, d.Dir)
		return nil
	}
	s := r.Step("write the draft into " + d.Dir)
	if err := d.Write(files, m); err != nil {
		s.Fail(err)
		return &ui.Problem{Hint: "the draft could not be written", Explain: errLine(err) + ". The system copy is as it was. Fix the cause and run " + progname.Cmd() + " deployment checkout again, or check out into another folder with --config.", Cause: err}
	}
	s.Done("drafted from " + what)
	r.Note("the system copy may be behind the sites", "Run "+progname.Cmd()+" deployment pull --config "+path+" to bring any newer version the sites hold.")
	r.Result("checked out %s of %s into %s", what, deploymentID, d.Dir)
	return nil
}

// verifiedRecord is the record of deployment id's system copy, refused
// unless it says the copy holds a verified version: an unverified or forked
// copy has no one trusted version to draft from.
func verifiedRecord(sys configstore.Local, id string) (localstore.Saved, error) {
	st, err := openStore()
	if err != nil {
		return localstore.Saved{}, err
	}
	rec, ok, err := st.Load(id)
	if err != nil {
		return rec, &ui.Problem{Hint: "the system copy of " + id + " cannot be read", Explain: errLine(err) + ", in " + sys.Dir + ". deployment fetch --ssh <user@host> saves it again from a host that holds it.", Cause: err}
	}
	if !ok {
		return rec, &ui.Problem{Hint: "the system copy of " + id + " has no record", Explain: "Nothing in " + sys.Dir + " says which version it holds. deployment fetch --ssh <user@host> saves it again from a host that holds it."}
	}
	recoverLine := progname.Cmd() + " deployment recover --from <user@host> --id " + id + " --root <sha> --config <empty directory>/" + config.FileName + " --execute"
	switch rec.Status {
	case localstore.Verified:
		return rec, nil
	case localstore.Unverified:
		return rec, &ui.Problem{Hint: "the system copy of " + id + " is unverified", Explain: fmt.Sprintf("It was fetched from a host this workstation holds no key or root for, so it has no trusted version to check out; its newest versions are only in %s/<sha>/. Check the hash with another operator, then pin it: %s.", filepath.Join(sys.Dir, ".paisans", "incoming"), recoverLine)}
	case localstore.Fork:
		return rec, &ui.Problem{Hint: "the system copy of " + id + " is a fork", Explain: fmt.Sprintf("Two trusted versions build on one parent, so there is no one version to check out. Check out each head with --head <sha>, as deployment log lists them, or recover the deployment instead: %s.", recoverLine)}
	}
	return rec, &ui.Problem{Hint: fmt.Sprintf("the system copy of %s has status %q", id, rec.Status), Explain: "Only a verified copy is checked out. deployment fetch --ssh <user@host> saves it again."}
}

// trustedHead is version sha of the system copy's store when its own anchor
// judges it a trusted head, as a draft starts from it.
func trustedHead(sys configstore.Local, id, sha string) (draft.Marker, configstore.Files, error) {
	t, err := judge(sys, id)
	if errors.Is(err, configstore.ErrNoAnchor) {
		return draft.Marker{}, configstore.Files{}, &ui.Problem{Hint: "the system copy of " + id + " has no version to start trusting from", Explain: err.Error() + ". " + recoverHint(id), Cause: err}
	}
	if err != nil {
		return draft.Marker{}, configstore.Files{}, err
	}
	if !slices.Contains(t.heads, sha) {
		return draft.Marker{}, configstore.Files{}, &ui.Problem{Hint: fmt.Sprintf("%q is not a trusted head of %s", sha, id), Explain: "--head takes the full hash of a head the system copy's store holds, as its own anchor judges it: " + strings.Join(t.heads, ", ") + "."}
	}
	cache, _, err := sys.HeldStore()
	if err != nil {
		return draft.Marker{}, configstore.Files{}, err
	}
	return headOf(cache, id, sha)
}

// savedTrust is what the trust rules make of a system copy, from its own
// anchor: the version its files are, and every version its store holds.
type savedTrust struct {
	head     string
	revision int
	anchor   configstore.Anchor
	versions []string
	files    configstore.Files
	// seen is .paisans/config-seen as it reads: revision and hash.
	seen string
	// fork is whether the trusted versions the store holds end in more
	// than one head.
	fork bool
	// heads is every trusted head the store holds.
	heads []string
}

// judgeSaved judges the system copy src as deployment log would, from its own
// anchor and reaching no site, and refuses one whose head is not a trusted
// version at the revision its record says, or whose files are not that
// version's.
func judgeSaved(src configstore.Local, rec localstore.Saved) (savedTrust, error) {
	again := "deployment fetch --ssh <user@host> saves it again from a host that holds it."
	if _, err := os.Stat(filepath.Join(src.Dir, ".paisans", "store.git", "HEAD")); err != nil {
		return savedTrust{}, &ui.Problem{Hint: "the system copy of " + rec.ID + " has no store", Explain: "Nothing in " + filepath.Join(src.Dir, ".paisans", "store.git") + " to check the version against. " + again, Cause: err}
	}
	head, err := src.Head()
	if err != nil {
		return savedTrust{}, err
	}
	if head == "" || head != rec.SHA {
		return savedTrust{}, &ui.Problem{Hint: "the system copy of " + rec.ID + " is not the version it records", Explain: fmt.Sprintf("Its record names %s and %s names %q. %s", rec.SHA, filepath.Join(src.Dir, ".paisans", "config-head"), head, again)}
	}
	t, err := judge(src, rec.ID)
	if errors.Is(err, configstore.ErrNoAnchor) {
		return t, &ui.Problem{Hint: "the system copy of " + rec.ID + " has no version to start trusting from", Explain: err.Error() + ". Check the root's hash with another operator, then pin it: " + progname.Cmd() + " deployment recover --from <user@host> --id " + rec.ID + " --root <sha> --config <empty directory>/" + config.FileName + " --execute.", Cause: err}
	}
	if err != nil {
		return t, err
	}
	if t.fork {
		return t, &ui.Problem{Hint: "the system copy of " + rec.ID + " holds a fork", Explain: "Its store holds two trusted versions on one parent, so the version it records is not the one to start a draft from. Recover the deployment into an empty directory instead: " + progname.Cmd() + " deployment recover --from <user@host> --id " + rec.ID + " --root <sha> --config <empty directory>/" + config.FileName + " --execute."}
	}
	if t.head != head || t.revision != rec.Revision {
		return t, &ui.Problem{Hint: "the system copy of " + rec.ID + " is not trusted from its own anchor", Explain: fmt.Sprintf("Its record names revision %d, %s, and its store does not hold that as a trusted version. %s", rec.Revision, rec.SHA, again)}
	}
	files, err := readCheckoutFiles(src.Dir)
	if err != nil {
		return t, err
	}
	if !bytes.Equal(files.Paisans, t.files.Paisans) || !bytes.Equal(files.Secrets, t.files.Secrets) || !bytes.Equal(files.SOPS, t.files.SOPS) {
		return t, &ui.Problem{Hint: "the system copy of " + rec.ID + " has files that are not its version", Explain: fmt.Sprintf("The files in %s differ from revision %d, %s, which it records. %s", src.Dir, rec.Revision, shortSHA(rec.SHA), again)}
	}
	return t, nil
}

// judge is l's head as its own anchor judges it: the head, trusted, with its
// revision and files, and every version l's store holds. A head older than
// one l has seen is kept, as l has it; deployment pull asks about it as it
// would here. It reads l and writes nothing to it, so a dry run leaves the
// system copy as it was.
func judge(l configstore.Local, id string) (savedTrust, error) {
	var t savedTrust
	cache, ok, err := l.HeldStore()
	if err != nil {
		return t, err
	}
	if !ok {
		return t, fmt.Errorf("%s holds no store", filepath.Join(l.Dir, ".paisans"))
	}
	head, err := l.Head()
	if err != nil {
		return t, err
	}
	if t.anchor, err = l.Anchor(); err != nil {
		return t, err
	}
	g, err := configstore.JudgeHeld(l, cache, id, t.anchor)
	var rb *configstore.RollbackError
	if errors.As(err, &rb) {
		err = nil
	}
	if err != nil {
		return t, err
	}
	t.versions = slices.Sorted(maps.Keys(g.Versions))
	t.fork = g.Verdict.Fork
	t.heads = g.Verdict.Heads
	rev, sha, err := l.Seen()
	if err != nil {
		return t, err
	}
	t.seen = fmt.Sprintf("%d %s", rev, sha)
	if !g.Verdict.Trusted[head] {
		return t, nil
	}
	t.head, t.revision = head, g.Versions[head].Revision
	t.files, err = cache.Files(head)
	return t, err
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

// shortSHA is the first 12 characters of a commit id, as deployment list
// prints it, or all of a shorter one.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
