package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/draft"
	"github.com/paisans-software/paisans-cli/internal/localstore"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// What follows is the deploy of a draft (docs/specs/2026-10-24-system-copy.md,
// *Deploying a draft*), the path every change to a live deployment takes:
// deployment apply --execute and deployment publish --execute sign the
// draft's files as a version on the one the draft was drafted from, push it
// to the sites, and make it the system copy's. The system copy is written
// under its lock, and only once the sites hold the version; a dry run
// records in the system copy's store the versions it gathered, and nothing
// else.

// deployer is a deploy of the draft d to deployment id, as its gather found
// the system copy.
type deployer struct {
	id string
	d  draft.Draft
	// sys is the system copy the gather read. For a deployment with none
	// here it is the scratch directory the gather used, and the first
	// deploy makes the system copy; held says which.
	sys  configstore.Local
	held bool
	// head is the system copy's head at the gather, and base the version
	// the draft builds on, "" for a deployment's first.
	head, base string
	// again is the command that deploys again, and source what the system
	// copy's record says the version was deployed from.
	again, source string
}

// gatheredDeploy is a deploy and the gather it goes on from. done removes
// the scratch directory a deployment with no system copy gathered into.
type gatheredDeploy struct {
	deployer
	g    configstore.Gathered
	done func()
}

// gatherDeploy gathers the versions the sites hold into the system copy of
// cfg's deployment, for a deploy of d, and finds the draft's base from
// paisans, its paisans-stack.yaml as read. A deployment with no system copy
// here gathers into a scratch directory instead: when the sites hold a
// version of it, it is refused, since trust in one starts only from a
// recovery or a join; when they hold none, it is a new deployment, and its
// first deploy makes the system copy. The caller calls done.
func gatherDeploy(r ui.Reporter, d draft.Draft, cfg *config.Config, sites map[string]apply.Transport, paisans []byte, m gatherMode, stdin io.Reader, stdout io.Writer) (gatheredDeploy, error) {
	t := gatheredDeploy{deployer: deployer{id: cfg.ID, d: d}, done: func() {}}
	st, err := openStore()
	if err != nil {
		return t, err
	}
	sys, held, err := st.Held(cfg.ID)
	if err != nil {
		return t, err
	}
	if !held {
		scratch, err := localstore.Scratch("paisans-gather-")
		if err != nil {
			return t, err
		}
		sys, t.done = scratch, func() { os.RemoveAll(scratch.Dir) }
	}
	t.sys, t.held = sys, held
	if t.head, err = sys.Head(); err != nil {
		return t, err
	}
	a, err := sys.Anchor()
	if err != nil {
		return t, err
	}
	m.noAnchor = noAnchorDeploy(cfg.ID)
	if t.g, err = gatherAt(r, sys, sites, cfg.ID, a, m, stdin, stdout); err != nil {
		return t, err
	}
	if !held && len(t.g.Verdict.Heads) > 0 {
		return t, &ui.Problem{Hint: "this workstation holds no system copy of " + cfg.ID, Explain: "The sites hold versions of it. " + noAnchorDeploy(cfg.ID)}
	}
	t.base = draftBase(r, sys, t.g, paisans, t.head)
	return t, nil
}

// noAnchorDeploy is what a deploy's refusal for a system copy with nothing
// to start trusting from says to do.
func noAnchorDeploy(id string) string {
	return "Trust in a deployment's versions starts only from what a recovery, a join or a deploy recorded here. " + recoverHint(id)
}

// draftBase is the version a draft builds on: the one its "drafted from"
// line names, in paisans, when the system copy's gather g trusts it, and
// otherwise head, the system copy's, which the review then names. A
// system copy with no head has no base to give: the draft is a first
// version.
func draftBase(r ui.Reporter, sys configstore.Local, g configstore.Gathered, paisans []byte, head string) string {
	m, ok := draft.ReadMarker(paisans)
	if ok && g.Verdict.Trusted[m.SHA] {
		return m.SHA
	}
	if head == "" {
		return ""
	}
	why := `This draft has no "drafted from" line`
	if ok {
		why = fmt.Sprintf(`This draft's "drafted from" line names %s, a version the system copy's trusted history does not hold`, shortSHA(m.SHA))
	}
	rev := g.Versions[head].Revision
	if cache, held, err := sys.HeldStore(); rev == 0 && err == nil && held {
		rev = cache.Revision(head)
	}
	r.Note("the draft is taken as drafted from the system copy's version", fmt.Sprintf("%s; it is taken as drafted from revision %d, the system copy's.", why, rev))
	return head
}

// deploy deploys files, the draft's as the version publish signs them,
// under the system copy's lock. It is refused, ending s, the step that
// publishes, while another command holds the lock, and when another deploy
// overtook this one, the system copy having moved since the gather. The
// first deploy of a new deployment makes the system copy (stageSystem),
// and one that fails leaves none, and leaves what the system copy's place
// held as it was. A deploy records the version in the system copy's
// saved.json, which deployment list shows.
func (p *deployer) deploy(r ui.Reporter, s ui.Step, files configstore.Files, publish func(sys configstore.Local) (sha string, rev int, err error)) error {
	st, err := openStore()
	if err != nil {
		s.Fail(err)
		return err
	}
	unlock, err := st.Lock(p.id)
	if err != nil {
		s.Fail(err)
		var locked *localstore.LockedError
		if errors.As(err, &locked) {
			return &ui.Problem{Hint: "another command is writing the system copy", Explain: sentence(locked.Error()) + " Nothing was published. Once it is done, run " + progname.Cmd() + " " + p.again + " again.", Cause: err}
		}
		return err
	}
	defer unlock()
	sys, held, err := st.Held(p.id)
	head := ""
	if err == nil && held {
		head, err = sys.Head()
	}
	if err != nil {
		s.Fail(err)
		return err
	}
	if held != p.held || head != p.head {
		moved := fmt.Errorf("the system copy moved from %s to %s", headName(p.head), headName(head))
		s.Fail(moved)
		return &ui.Problem{Hint: "another deploy overtook this one", Explain: fmt.Sprintf("The system copy of %s moved from %s to %s since this command gathered the sites' versions, so what it would sign no longer builds on the newest. Nothing was published. Run %s %s again, which gathers again.", p.id, headName(p.head), headName(head), progname.Cmd(), p.again), Cause: moved}
	}
	if held {
		sha, _, err := deployDraft(r, sys, p.d, files, func() (string, int, error) { return publish(sys) })
		if err != nil {
			return err
		}
		p.sys, p.head = sys, sha
		recordHead(r, sys, p.source)
		return nil
	}
	// The first deploy makes the system copy as a recovery does, in a
	// directory of its own that is put in place once it records a head.
	// The lock is this function's.
	sys, staged, done, err := stageSystem(r, st, "deploy", p.id, "it held no system copy, only what a run that made none left.", nil, func() {}, nil)
	if err != nil {
		s.Fail(err)
		return err
	}
	sha, _, err := deployDraft(staged, sys, p.d, files, func() (string, int, error) { return publish(sys) })
	if err == nil {
		recordHead(staged, sys, p.source)
	}
	if err := done(err); err != nil {
		return err
	}
	if sys, held, err = st.Held(p.id); err != nil || !held {
		return errors.Join(err, fmt.Errorf("the system copy of %s is not in place", p.id))
	}
	p.sys, p.held, p.head = sys, true, sha
	return nil
}

// headName is a system copy's head in a refusal.
func headName(sha string) string {
	if sha == "" {
		return "no version"
	}
	return shortSHA(sha)
}

// recordHead records that the head of the system copy sys moved: its
// saved.json names the version the head now is, verified, with source as
// where it came from. It is what deployment list shows, and what checkout
// checks the head against, so every command that moves a system copy's
// head records through it: a deploy, a pull or an apply that merged
// forward, an attestation, a recovery or a join, and a fetch into a copy
// with no head. sys may be the directory a run makes a system copy in,
// which keeps the record when it is put in place. A failure is a warning:
// the version is the system copy's already.
func recordHead(r ui.Reporter, sys configstore.Local, source string) {
	err := func() error {
		head, err := sys.Head()
		if err != nil || head == "" {
			return errors.Join(err, errors.New("the system copy has no head"))
		}
		cache, err := sys.Cache()
		if err != nil {
			return err
		}
		cfg, err := config.Load(filepath.Join(sys.Dir, config.FileName))
		if err != nil {
			return err
		}
		return localstore.RecordIn(sys.Dir, localstore.Saved{ID: cfg.ID, Domain: cfg.Community.Domain, SHA: head, Revision: cache.Revision(head), Status: localstore.Verified, Source: source, SavedAt: configNow().UTC()})
	}()
	if err != nil {
		r.Warn("the system copy's record was not written", errLine(err)+". The version is the system copy's; deployment list shows it without its record, and checkout refuses it until a pull or a deploy records it.")
	}
}

// deployDraft deploys d: it makes f, d's files as the version publish signs
// them, the system copy's files, and runs publish, which signs and pushes
// that version and returns it. publish returns an error only when no site
// holds the version, with the version it committed, if any: that publish
// is taken back, the system copy put back as it was, its files byte for
// byte, what it records and the version dropped from its store
// (configstore.Local.Mark). Once a site took the version nothing takes it
// back, and what fails after is a warning. One that succeeds
// rewrites d's "drafted from" line to name the version, and leaves the rest
// of d as it is, whatever was edited there since it was read. The system
// copy's files are written without the blueprints f holds, which are
// installed already: they are the ones the draft was read with. The caller
// holds the system copy's lock.
func deployDraft(r ui.Reporter, sys configstore.Local, d draft.Draft, f configstore.Files, publish func() (sha string, rev int, err error)) (string, int, error) {
	before, err := heldFiles(sys.Dir)
	if err != nil {
		return "", 0, err
	}
	undo, err := sys.Mark()
	if err != nil {
		return "", 0, err
	}
	f.Blueprints = nil
	if err := configstore.WriteFilesTo(sys.Dir, f); err != nil {
		return "", 0, errors.Join(err, putBack(sys.Dir, before))
	}
	sha, rev, err := publish()
	if err != nil {
		if back := errors.Join(putBack(sys.Dir, before), undo(sha)); back != nil {
			return "", 0, errors.Join(err, back)
		}
		return "", 0, err
	}
	if err := d.Mark(draft.Marker{Revision: rev, SHA: sha}); err != nil {
		r.Warn(`the draft's "drafted from" line was not rewritten`, errLine(err)+fmt.Sprintf(". The draft is deployed as revision %d (%s); the next deploy of it takes the system copy's version as its base.", rev, sha))
	}
	return sha, rev, nil
}

// heldFile is one of the three files as it was on disk.
type heldFile struct {
	data []byte
	mode os.FileMode
}

// heldFiles is each of the three files in dir, those absent left out.
func heldFiles(dir string) (map[string]heldFile, error) {
	out := map[string]heldFile{}
	for _, name := range []string{config.FileName, configstore.FileSecrets, configstore.FileSOPS} {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		out[name] = heldFile{data: data, mode: info.Mode().Perm()}
	}
	return out, nil
}

// putBack writes the three files in dir back as held has them, each through
// a rename with its mode, and removes those held lacks.
func putBack(dir string, held map[string]heldFile) error {
	var errs []error
	for _, name := range []string{config.FileName, configstore.FileSecrets, configstore.FileSOPS} {
		path := filepath.Join(dir, name)
		h, ok := held[name]
		if !ok {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		if err := writeFileAtomic(path, h.data); err != nil {
			errs = append(errs, err)
			continue
		}
		errs = append(errs, os.Chmod(path, h.mode))
	}
	return errors.Join(errs...)
}
