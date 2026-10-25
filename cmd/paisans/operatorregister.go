package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/draft"
	"github.com/paisans-software/paisans-cli/internal/localstore"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// What follows is `paisans operator register` (docs/specs/2026-10-12-config-store.md,
// *paisans operator register*). An operator who has never published has signed
// nothing, so their workstation has no anchor of its own to trust the sites'
// versions from. register signs an attestation of the sites' newest version,
// once it equals the files the operator already holds, and records the key:
// from then on the operator's own signature is where trust starts.

func runOperator(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) > 0 && args[0] == "add" {
		return runOperatorAdd(args[1:], stdin, stdout)
	}
	if len(args) > 0 && args[0] == "register" {
		return runOperatorRegister(args[1:], stdin, stdout)
	}
	if len(args) > 0 && args[0] == "age-key" {
		return runOperatorAgeKey(args[1:], stdout)
	}
	if len(args) > 0 && args[0] == "invite" {
		return runOperatorInvite(args[1:], stdout)
	}
	if len(args) > 0 && args[0] == "magic-link" {
		return runOperatorMagicLink(args[1:])
	}
	if len(args) > 0 && args[0] == "grants" {
		return runOperatorGrants(args[1:])
	}
	if len(args) == 0 {
		return &ui.Problem{Hint: "operator takes one subcommand", Explain: "Use add, register, age-key, invite, magic-link or grants."}
	}
	return &ui.Problem{Hint: fmt.Sprintf("operator has no subcommand %q", args[0]), Explain: "Use add, register, age-key, invite, magic-link or grants."}
}

func runOperatorRegister(args []string, stdin io.Reader, stdout io.Writer) (err error) {
	fs := newFlagSet("operator register", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, configHelp)
	keyPath := fs.String("key", "", "the public key file (.pub) to sign with; the ssh-agent must hold it")
	root := fs.String("root", "", "pin this version as the root, the version trust starts from")
	execute := fs.Bool("execute", false, "sign the attestation, push it to the sites, and record the key")
	answers := yesFlag(fs)
	invite := fs.String("invite", "", "answer this invite file with a signed request on stdout, instead of registering")
	inviterKey := fs.String("inviter-key", "", "with --invite: the inviting operator's public key, a .pub file or the key line")
	name := fs.String("name", "", "with --invite: your full name, first and last; asked for at a terminal when not given")
	email := fs.String("email", "", "with --invite: your email address; asked for at a terminal when not given")
	id := fs.String("id", "", "with no files here: join the invite pending for this deployment id, when invites to several are pending")
	ageFile := fs.String("age-private-key-file", "", "with --invite: keep a new age key in this file, outside the configuration's directory, rather than the keystore; an existing file is read")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return &ui.Problem{Hint: "operator register takes no argument", Explain: "Got " + strings.Join(fs.Args(), " ") + "."}
	}
	if *invite != "" {
		if *keyPath != "" || *root != "" || *execute || *id != "" {
			return &ui.Problem{Hint: "--invite answers an invite and registers nothing", Explain: "--key, --root, --id and --execute belong to registering, which comes after the invite is accepted."}
		}
		return registerInvite(*invite, *inviterKey, *name, *email, *ageFile, *configPath, stdin, stdout)
	}
	for _, stray := range []struct{ flag, value string }{{"--inviter-key", *inviterKey}, {"--name", *name}, {"--email", *email}, {"--age-private-key-file", *ageFile}} {
		if stray.value != "" {
			return &ui.Problem{Hint: stray.flag + " needs --invite", Explain: "It answers an invite: " + progname.Cmd() + " operator register --invite <file> --inviter-key <key>."}
		}
	}
	ans, err := answers()
	if err != nil {
		return err
	}
	d, err := draftAt(*configPath)
	if err != nil {
		return err
	}
	// Where the folder holds no draft and an answer left an invite pending,
	// register joins: the invite names the root and the key. --key or --root
	// without --id is a plain register, which a pending invite does not
	// take over.
	if has := d.HasAny(); has && *id != "" {
		return &ui.Problem{Hint: "--id selects a pending invite to join, and " + ui.ShortPath(d.Dir) + " holds the files", Explain: "A join writes the files into a directory without them. Here register registers, and reads the deployment from " + config.FileName + "; run it without --id."}
	} else if !has && (*id != "" || (*keyPath == "" && *root == "")) {
		invite, ok, err := selectPending(*id)
		if err != nil {
			return err
		}
		if ok {
			if *keyPath != "" || *root != "" {
				return &ui.Problem{Hint: "--key and --root do not apply to a join", Explain: "The pending invite names the key you sign with and the root trust starts from."}
			}
			return joinInvite(reporter(), d, invite, *execute, ans, stdin, stdout)
		}
	}
	sys, sites, r, done, err := registerSystem(reporter(), *configPath, *id, d, *root, *execute)
	if err != nil {
		return err
	}
	defer func() { err = done(err) }()
	a, err := anchorFor(sys, *root)
	if err != nil {
		return err
	}
	pin := a.Root
	if sites != nil {
		// A system copy this run makes holds nothing yet: the version the
		// pin leads to is gathered first, from the sites the draft names,
		// and the operators and the key are read from it.
		r.Section("find the version " + shortSHA(pin) + " leads to")
		m := gatherMode{answers: ans, noAnchor: "Pin the first version you trust with --root <sha>; an operator who has one can read its hash from " + progname.Cmd() + " deployment log."}
		if _, err := gatherAt(r, sys, configTransports(sites), sites.ID, configstore.Anchor{Root: pin}, m, stdin, stdout); err != nil {
			return err
		}
	}
	cfg, err := registerConfig(sys, pin)
	if err != nil {
		return err
	}
	key, err := registerKey(sys, cfg, *keyPath, stdout)
	if err != nil {
		return err
	}
	k, _, _ := config.ParseKeyLine(key)
	r.Section("register " + k.Fingerprint + " for " + *configPath)
	return attest(r, sys, cfg, key, pin, *execute, ans, stdin, stdout)
}

// registerSystem is the system copy register attests from, staged, the
// reporter to report the run through, and done, which
// ends the run's hold on it and returns the run's error, runErr, as it
// should be reported. A draft of a deployment with no system copy
// here is refused, as systemFor refuses it, unless root pins the root: a
// pinned root anchors alone, so register makes the system copy, and sites is
// the draft's configuration, which names the sites to gather from. Making
// it holds the lock from before the system copy is looked for until done,
// dry run or not, and works in a directory of its own under the data
// directory; done puts that in place as the system copy only when the run
// recorded a head there, as a dry run does not, and removes it. Otherwise
// --execute holds the lock until done.
func registerSystem(r ui.Reporter, configPath, id string, d draft.Draft, root string, execute bool) (sys configstore.Local, sites *config.Config, staged ui.Reporter, done func(runErr error) error, err error) {
	done = func(runErr error) error { return runErr }
	st, err := openStore()
	if err != nil {
		return sys, nil, r, done, err
	}
	release, locked := func() {}, false
	if c := createdFrom(d, id, root); c != nil {
		if release, err = st.Lock(c.ID); err != nil {
			return sys, nil, r, done, err
		}
		locked = true
		_, ok, err := st.Held(c.ID)
		if err != nil {
			release()
			return sys, nil, r, done, err
		}
		if !ok {
			sys, staged, end, err := stageSystem(r, st, "register", c.ID, "it held no system copy, only what a run that made none left.", []string{localstore.SavedFile}, release, nil)
			if err != nil {
				return sys, nil, r, done, err
			}
			return sys, c, staged, end, nil
		}
		if !execute {
			release()
			release, locked = func() {}, false
		}
	}
	sys, _, deploymentID, err := systemFor(configPath, id)
	if err != nil {
		release()
		return sys, nil, r, done, err
	}
	if execute && !locked {
		if release, err = st.Lock(deploymentID); err != nil {
			return sys, nil, r, done, err
		}
	}
	return sys, nil, r, func(runErr error) error { release(); return runErr }, nil
}

// stageSystem is where a run that makes deployment id's system copy works,
// a directory of its own under the data directory and outside
// deployments/, named for the command, so nothing it gathers is a system
// copy until it records a head; staged, the reporter r naming the system
// copy's place wherever its output names that directory, for the run to
// report through; and done, which ends the run. The caller holds the lock
// on id, release, which done releases, as stageSystem does when it fails.
// done puts the directory in place as the system copy only when the run
// recorded a head there, as a dry run does not, then runs placed, when
// set, on the system copy in its place, and removes the directory. What
// the system copy's place held, but the entries keep names, is moved
// aside, never removed, and a warning names each entry, where it went, and
// why, which says why that is no system copy to keep. done returns the
// run's error, runErr, naming the system copy where it named the
// directory, or why the system copy is not in place.
func stageSystem(r ui.Reporter, st localstore.Store, command, id, why string, keep []string, release func(), placed func(sys configstore.Local) error) (sys configstore.Local, staged ui.Reporter, done func(runErr error) error, err error) {
	sys, err = st.Staging(command + "-" + id + "-")
	if err != nil {
		release()
		return configstore.Local{}, r, nil, err
	}
	staging := sys.Dir
	dest := filepath.Join(st.Base, "deployments", id)
	return sys, renamingReporter{r, staging, dest}, func(runErr error) error {
		defer release()
		var after error
		if head, err := sys.Head(); err == nil && head != "" {
			aside, moved, err := settle(staging, dest, keep...)
			if len(moved) > 0 {
				r.Warn("what "+dest+" held is moved aside to "+aside, "It held "+strings.Join(moved, ", ")+"; "+why+" The system copy this run made is in its place, and nothing was removed. Remove "+aside+" once you know nothing in it is needed.")
			}
			if err != nil {
				what := "and pushed it as reported above"
				if runErr != nil {
					what = "and stopped before the sites were known to hold it (" + errLine(runErr) + "); they may not have it"
				}
				left := ""
				if aside != "" {
					left = " Entries " + dest + " held may be in " + aside + " already, and others may remain in " + dest + "; move those aside first."
				}
				return &ui.Problem{Hint: "the new system copy of " + id + " could not be put in place", Explain: "This run recorded its head, " + shortSHA(head) + ", in " + staging + ", " + what + "." + left + " Move its entries into " + dest + ", the paisans state directory .paisans last: " + errLine(err) + ".", Cause: err}
			}
			if placed != nil {
				if l, err := st.Local(id); err != nil {
					after = err
				} else {
					after = placed(l)
				}
			}
		}
		if err := os.RemoveAll(staging); err != nil {
			r.Warn("the directory the run worked in is left in "+staging, errLine(err)+". It holds no system copy; remove it.")
		}
		if runErr = renamed(runErr, staging, dest); runErr == nil {
			return after
		}
		return runErr
	}, nil
}

// settle puts the system copy made in staging at dest. Whatever dest holds
// is moved aside first, entry by entry, never removed, into a directory of
// its own under replaced/ of the data directory, outside deployments/, so
// it is never read as a system copy; aside names it, and moved each entry
// it took. The entries keep names stay in dest. When nothing stays, staging
// itself becomes dest by one rename; otherwise its entries go in one by
// one, .paisans, which holds the head, last, so dest is a system copy only
// once its files are there.
func settle(staging, dest string, keep ...string) (aside string, moved []string, err error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", nil, err
	}
	entries, err := os.ReadDir(dest)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", nil, err
	}
	kept := 0
	for _, e := range entries {
		if slices.Contains(keep, e.Name()) {
			kept++
			continue
		}
		if aside == "" {
			replaced := filepath.Join(filepath.Dir(filepath.Dir(dest)), "replaced")
			if err := os.MkdirAll(replaced, 0o700); err != nil {
				return "", nil, err
			}
			if aside, err = os.MkdirTemp(replaced, filepath.Base(dest)+"-"); err != nil {
				return "", nil, err
			}
		}
		if err := os.Rename(filepath.Join(dest, e.Name()), filepath.Join(aside, e.Name())); err != nil {
			return aside, moved, err
		}
		moved = append(moved, e.Name())
	}
	if kept == 0 {
		if err := os.Remove(dest); err != nil && !errors.Is(err, os.ErrNotExist) {
			return aside, moved, err
		}
		return aside, moved, os.Rename(staging, dest)
	}
	made, err := os.ReadDir(staging)
	if err != nil {
		return aside, moved, err
	}
	const state = ".paisans"
	var names []string
	for _, e := range made {
		if e.Name() != state {
			names = append(names, e.Name())
		}
	}
	if len(names) < len(made) {
		names = append(names, state)
	}
	for _, name := range names {
		if err := os.Rename(filepath.Join(staging, name), filepath.Join(dest, name)); err != nil {
			return aside, moved, err
		}
	}
	return aside, moved, nil
}

// renamed is err with each mention of the directory from, where the run
// worked and which is gone once it ends, naming to instead. A Problem err
// holds, at any depth, is what is shown, so it is the one renamed, its
// Cause with it.
func renamed(err error, from, to string) error {
	if err == nil {
		return nil
	}
	var p *ui.Problem
	if errors.As(err, &p) {
		q := *p
		q.Hint = renamePath(q.Hint, from, to)
		q.Explain = renamePath(q.Explain, from, to)
		q.Cause = renamed(q.Cause, from, to)
		return &q
	}
	if renamePath(err.Error(), from, to) == err.Error() {
		return err
	}
	return renamedError{err, from, to}
}

type renamedError struct {
	err      error
	from, to string
}

func (e renamedError) Error() string { return renamePath(e.err.Error(), e.from, e.to) }
func (e renamedError) Unwrap() error { return e.err }

// renamePath is s with each mention of the path from, as a whole path or a
// path under it, naming to instead: a longer name that starts with from is
// another path, and is left.
func renamePath(s, from, to string) string {
	if from == "" {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, from)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := i + len(from)
		b.WriteString(s[:i])
		if end == len(s) || !pathByte(s[end]) {
			b.WriteString(to)
		} else {
			b.WriteString(from)
		}
		s = s[end:]
	}
}

// pathByte reports whether c continues a file name. A dot does not: it
// ends the sentence a path closes far more often than it starts a suffix.
func pathByte(c byte) bool {
	return c == '-' || c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}

// renamingReporter is a Reporter whose output names to wherever it would
// name from: a run's output while it works in a directory that becomes the
// system copy.
type renamingReporter struct {
	ui.Reporter
	from, to string
}

func (r renamingReporter) n(s string) string { return renamePath(s, r.from, r.to) }

func (r renamingReporter) Section(title string) { r.Reporter.Section(r.n(title)) }
func (r renamingReporter) Step(title string) ui.Step {
	return renamingStep{r.Reporter.Step(r.n(title)), r}
}
func (r renamingReporter) Warn(hint, detail string)   { r.Reporter.Warn(r.n(hint), r.n(detail)) }
func (r renamingReporter) Note(hint, detail string)   { r.Reporter.Note(r.n(hint), r.n(detail)) }
func (r renamingReporter) Explain(text string)        { r.Reporter.Explain(r.n(text)) }
func (r renamingReporter) Refuse(hint, expl string)   { r.Reporter.Refuse(r.n(hint), r.n(expl)) }
func (r renamingReporter) Trace(label, output string) { r.Reporter.Trace(r.n(label), r.n(output)) }
func (r renamingReporter) Verbatim(lines []string) {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = r.n(l)
	}
	r.Reporter.Verbatim(out)
}
func (r renamingReporter) Detail(format string, args ...any) {
	r.Reporter.Detail("%s", r.n(fmt.Sprintf(format, args...)))
}
func (r renamingReporter) Result(format string, args ...any) {
	r.Reporter.Result("%s", r.n(fmt.Sprintf(format, args...)))
}

type renamingStep struct {
	ui.Step
	r renamingReporter
}

func (s renamingStep) Done(result string)           { s.Step.Done(s.r.n(result)) }
func (s renamingStep) Fail(err error)               { s.Step.Fail(renamed(err, s.r.from, s.r.to)) }
func (s renamingStep) End(m ui.Mark, result string) { s.Step.End(m, s.r.n(result)) }
func (s renamingStep) Progress(text string)         { s.Step.Progress(s.r.n(text)) }
func (s renamingStep) Detail(format string, args ...any) {
	s.Step.Detail("%s", s.r.n(fmt.Sprintf(format, args...)))
}

// createdFrom is the draft's configuration when register may make its
// deployment's system copy: root pins a root and d is a draft that loads,
// of the deployment id names when id is given. A draft that does not load,
// or names no deployment, is left to systemFor to report.
func createdFrom(d draft.Draft, id, root string) *config.Config {
	if root == "" || !d.Has() {
		return nil
	}
	c, err := config.Load(filepath.Join(d.Dir, config.FileName))
	if err != nil || !deployment.ValidID(c.ID) || (id != "" && id != c.ID) {
		return nil
	}
	return c
}

// attest signs, with execute, an attestation of the sites' newest version
// with key, once it equals the files l holds (the system copy's, or the
// join's folder), pushes it to cfg's sites, and
// records the key. Trust starts from pin and from any version key signed.
func attest(r ui.Reporter, l configstore.Local, cfg *config.Config, key, pin string, execute bool, ans answerer, stdin io.Reader, stdout io.Writer) error {
	k, _, _ := config.ParseKeyLine(key)

	// Trust starts from the pin, and from any version this key signed
	// before; with neither, the sites' history alone is never trusted.
	anchor := configstore.Anchor{Root: pin, Signers: []string{k.Fingerprint}}
	sites := configTransports(cfg)
	m := gatherMode{
		execute:  execute,
		answers:  ans,
		noAnchor: "Pin the first version you trust with --root <sha>; an operator who has one can read its hash from " + progname.Cmd() + " deployment log.",
	}
	g, err := gatherAt(r, l, sites, cfg.ID, anchor, m, stdin, stdout)
	if err != nil {
		return err
	}
	if len(g.Direct()) == 0 {
		return &ui.Problem{Hint: "no site answered", Explain: "Nothing was attested.", Cause: configstore.UnreachedError(g)}
	}
	if g.Verdict.Fork {
		return forkProblem(g)
	}
	head, write, err := configstore.AttestTarget(l, g, pin)
	var de *configstore.DiffersError
	switch {
	case errors.As(err, &de):
		if err := showDifference(stdout, de.Local, de.Theirs); err != nil {
			return err
		}
		return &ui.Problem{
			Hint:    "the system copy's files differ from the sites' newest version",
			Explain: fmt.Sprintf("The sites hold %s. An attestation says this workstation holds that version's files, so it signs only a version equal to the system copy's. Bring the system copy to it with %s deployment pull, or check the difference above with the operator who signed it, then run this again. Nothing was written.", describe(g, de.Head), progname.Cmd()),
		}
	case err != nil:
		return err
	}
	// The same check the attestation makes, so a dry run never promises
	// what --execute refuses.
	if err := configstore.CheckSigner(l, []string{head}, cfg, key); err != nil {
		return err
	}
	own := g.Versions[head].SignerFP == k.Fingerprint

	if !execute {
		if own {
			r.Step("attest "+describe(g, head)).End(ui.Pending, "your key signed it already")
		} else {
			r.Step("attest "+describe(g, head)).End(ui.Pending, fmt.Sprintf("revision %d, signed with %s", g.Versions[head].Revision+1, k.Fingerprint))
		}
		if write {
			r.Step("write the files of "+describe(g, head)).End(ui.Pending, "--execute will write them")
		}
		return nil
	}
	s := r.Step("sign and push the attestation, signed with " + k.Fingerprint)
	sha, err := attestOnce(l, g, sites, pin, key, s, r)
	if err != nil {
		return err
	}
	recordHead(r, l, "attested by operator register")
	r.Result("registered %s; version %s is your anchor", k.Fingerprint, sha)
	return nil
}

// attestOnce signs and pushes the attestation (configstore.AttestFrom) on
// l, ending s from what the push did. An attestation no site took is
// taken back whole, as a deploy's is (deployDraft): l's files byte for
// byte, what it records and the version dropped from its store, so the
// store names no version only this workstation holds, which another
// operator's next deploy would make a fork of. Once a site took it nothing
// takes it back, and a site that missed it is warned about.
func attestOnce(l configstore.Local, g configstore.Gathered, sites map[string]apply.Transport, pin, key string, s ui.Step, r ui.Reporter) (string, error) {
	before, err := heldFiles(l.Dir)
	if err != nil {
		s.Fail(err)
		return "", err
	}
	undo, err := l.Mark()
	if err != nil {
		s.Fail(err)
		return "", err
	}
	sha, pushed, err := configstore.AttestFrom(l, g, sites, pin, key, configSignWith(key), configNow())
	var pe *configstore.PushError
	switch {
	case errors.As(err, &pe) && len(pushed) > 0:
		s.End(ui.Warned, fmt.Sprintf("%s reached %s", sha[:12], strings.Join(pushed, ", ")))
		for _, site := range slices.Sorted(maps.Keys(pe.Failed)) {
			r.Warn(site+" did not take the attestation", fmt.Sprintf("%v. It gets it from its peers' sync, or from running this again.", pe.Failed[site]))
		}
	case err != nil:
		s.Fail(err)
		if back := errors.Join(putBack(l.Dir, before), undo(sha)); back != nil {
			return "", errors.Join(err, back)
		}
		return "", err
	default:
		s.Done(sha[:12])
	}
	return sha, nil
}

// registerConfig is the configuration register reads the operators and the
// sites from: the system copy's paisans-stack.yaml, or, in a system copy
// without the files and with a root pinned, the version the pin leads to in
// its store, which a recovery's dry run fills.
func registerConfig(l configstore.Local, pin string) (*config.Config, error) {
	if l.HasFiles() {
		return config.Load(filepath.Join(l.Dir, config.FileName))
	}
	if pin == "" {
		return nil, &ui.Problem{Hint: "the system copy holds no configuration files", Explain: "operator register attests the version this workstation holds, and reads the sites from its " + config.FileName + ", and " + l.Dir + " has none. Pin the first version you trust with --root <sha> to attest the version it leads to and write its files."}
	}
	g, err := configstore.GatherAnchored(l, nil, "", configstore.Anchor{Root: pin}, configstore.GatherOptions{})
	var rb *configstore.RollbackError
	if errors.As(err, &rb) {
		err = nil
	}
	if err != nil || len(g.Verdict.Heads) == 0 {
		return nil, &ui.Problem{Hint: "the pinned root is not in this workstation's store", Explain: "With no files here, the sites are read from the version the pin leads to, which this workstation must have gathered before. Run " + progname.Cmd() + " deployment recover --from <user@host> --root " + pin + ", which finds them from a host.", Cause: err}
	}
	if g.Verdict.Fork {
		return nil, forkProblem(g)
	}
	cache, err := l.Cache()
	if err != nil {
		return nil, err
	}
	f, err := cache.Files(g.Verdict.Heads[0])
	if err != nil {
		return nil, err
	}
	return config.LoadBytes(f.Paisans)
}

// registerKey is the public key line register signs with: the one --key
// names, else the one the system copy records, else the one key in the
// ssh-agent that an operator of cfg lists. Which operator the operator is, and
// which key, are recorded and used from then on, so neither is guessed.
func registerKey(l configstore.Local, cfg *config.Config, keyPath string, stdout io.Writer) (string, error) {
	held := agentKeys()
	if keyPath != "" {
		data, err := os.ReadFile(keyPath)
		if err != nil {
			return "", &ui.Problem{Hint: "cannot read --key", Explain: errLine(err) + ".", Cause: err}
		}
		k, opts, err := config.ParseKeyLine(strings.TrimSpace(string(data)))
		if err != nil || len(opts) > 0 {
			return "", &ui.Problem{Hint: "--key must name a public key file", Explain: "Give the .pub file of your key, Eg: ~/.ssh/id_ed25519.pub. The private key stays in your ssh-agent, which signs."}
		}
		if !agentHolds(held, k.Fingerprint) {
			return "", &ui.Problem{Hint: "your ssh-agent does not hold the key --key names", Explain: fmt.Sprintf("The attestation is signed through the agent, so the private key of %s must be loaded in it. Load it with ssh-add and run this again.", k.Fingerprint)}
		}
		return k.Line, nil
	}
	if recorded, err := recordedKey(l, held, ", or name the key to register with --key"); err != nil || recorded != "" {
		return recorded, err
	}
	byAdmin := operatorsHolding(cfg, held)
	switch len(byAdmin) {
	case 0:
		for _, line := range held {
			fmt.Fprintln(stdout, line)
		}
		explain := "Your ssh-agent holds the keys above, and " + config.FileName + " lists none of them under operators."
		if len(held) == 0 {
			explain = "Your ssh-agent holds no key. Load yours with ssh-add."
		}
		return "", &ui.Problem{Hint: "no key in your ssh-agent belongs to an operator", Explain: explain + " Your public key goes under your own entry in operators (operators.<id>.keys), and another operator publishes that change, since only an operator the sites' newest version lists can sign. Then pull it and run this again."}
	case 1:
		for id, lines := range byAdmin {
			if len(lines) == 1 {
				return lines[0], nil
			}
			return "", &ui.Problem{Hint: "your ssh-agent holds more than one of " + id + "'s keys", Explain: "Which one signs is recorded and used from then on, so it is not guessed. Name it with --key <path.pub>."}
		}
	}
	ids := slices.Sorted(maps.Keys(byAdmin))
	return "", &ui.Problem{Hint: "your ssh-agent holds keys of more than one operator: " + strings.Join(ids, ", "), Explain: "Which operator you are is not guessed. Name your own key with --key <path.pub>."}
}
