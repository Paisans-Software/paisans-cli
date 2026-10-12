package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/configstore"
	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/registry"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"gopkg.in/yaml.v3"
)

// What follows is `paisans config`: the signed copies of the configuration
// every site keeps (docs/specs/2026-10-12-config-store.md). publish puts the
// local files on the sites as a new version, pull brings the sites' newest
// trusted version to the local files, log lists every version, and recover
// writes the newest onto a workstation that has nothing. Every decision about
// which version to trust is made here, on the workstation, from the
// operator's own anchor; the sites only store.

// partialWord is what the operator types to go on with fewer than a majority
// of sites reached directly: a site that lies can hide newer versions only
// from a workstation that reaches no honest site, and a majority makes that
// unlikely.
const partialWord = "partial"

// configSiteTransport is how a config command reaches a site paisans-stack.yaml
// declares: its ssh section, with sudo, since the store is root's. Tests
// replace it.
var configSiteTransport = func(name string, site config.Site) apply.Transport {
	return siteTransport(name, site, "", true)
}

// configSignWith is what git's user.signingKey is given to sign with the
// public key line: key:: and the line, so the ssh-agent signs. Tests replace
// it with a private key file.
var configSignWith = func(line string) string { return "key::" + line }

// configNow is the clock a version's commit date is written by. Tests
// replace it.
var configNow = time.Now

func runConfig(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return &ui.Problem{Hint: "config takes one subcommand", Explain: "Use publish, pull, log or recover."}
	}
	switch args[0] {
	case "publish":
		return runConfigPublish(args[1:], stdin, stdout)
	case "pull":
		return runConfigPull(args[1:], stdin, stdout)
	case "log":
		return runConfigLog(args[1:], stdin, stdout)
	case "recover":
		return runConfigRecover(args[1:], stdin, stdout)
	}
	return &ui.Problem{Hint: fmt.Sprintf("config has no subcommand %q", args[0]), Explain: "Use publish, pull, log or recover."}
}

// configHelp is the --config flag's help, the same for every subcommand.
const configHelp = "path to the deployment declaration; the store's state is kept in .paisans/ beside it"

// workstationOf is the workstation whose paisans-stack.yaml is at path. The three
// files of a version have fixed names, so the configuration must be named
// paisans-stack.yaml for the files on disk to be the ones a version holds.
func workstationOf(path string) (configstore.Local, error) {
	if filepath.Base(path) != config.FileName {
		return configstore.Local{}, &ui.Problem{Hint: "--config must name a file called " + config.FileName, Explain: "A stored version holds " + config.FileName + ", secrets.enc.yaml and .sops.yaml by those names, so the files beside --config must carry them. Got " + path + "."}
	}
	return configstore.Local{Dir: filepath.Dir(path)}, nil
}

// configTransports reaches every site cfg declares.
func configTransports(cfg *config.Config) map[string]apply.Transport {
	out := map[string]apply.Transport{}
	for _, name := range cfg.SiteNames() {
		out[name] = configSiteTransport(name, cfg.Sites[name])
	}
	return out
}

// anchorFor is the workstation's anchor, with root pinned in place of a
// recorded one. A root that differs from the recorded pin is refused: the
// pin is what the operator trusted before, and replacing it is not a pull's
// side effect.
func anchorFor(l configstore.Local, root string) (configstore.Anchor, error) {
	a, err := l.Anchor()
	if err != nil {
		return a, err
	}
	if root == "" {
		return a, nil
	}
	if !shaShape(root) {
		return a, &ui.Problem{Hint: fmt.Sprintf("--root %q is not a commit id", root), Explain: "Give the full hash of the version to pin, as paisans config log prints it on a workstation that has it."}
	}
	if a.Root != "" && a.Root != root {
		return a, &ui.Problem{Hint: "--root differs from the pinned root", Explain: fmt.Sprintf("This workstation pinned %s in .paisans/config-root. Drop --root, or remove that file if the pin is wrong.", a.Root)}
	}
	a.Root = root
	return a, nil
}

func shaShape(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	return strings.Trim(s, "0123456789abcdef") == ""
}

// rollbackWord is what the operator types to go on from a version older than
// one this workstation has seen: a site that deletes the newer versions can
// serve an old, genuine one, and applying it would put back old secrets.
const rollbackWord = "rollback"

// gatherMode is what a gather may do. execute, for a run with --execute,
// asks each site to sync with its peers first and asks for rollback at a
// terminal; without it no site is changed and a rollback is only reported.
// log reports a rollback without saying --execute asks. accepted is a
// rollback the operator accepted earlier in this run, which is not asked
// again. noAnchor, when set, replaces what a missing anchor's refusal says
// to do.
type gatherMode struct {
	execute, log bool
	accepted     *configstore.SeenChange
	noAnchor     string
}

// gatherAt gathers from sites, reporting each, and explains a missing anchor.
// A dry run changes no site, and says so. A head older than one this
// workstation has seen goes on, with --execute, only once the operator types
// rollback at a terminal. Nothing is written to the seen record here: the
// gather carries the change (a newer head, or the accepted older one), and
// the command records it with Local.RecordSeen once it commits its change.
func gatherAt(r ui.Reporter, l configstore.Local, sites map[string]apply.Transport, configured int, id string, a configstore.Anchor, m gatherMode, stdin io.Reader, stdout io.Writer) (configstore.Gathered, error) {
	names := slices.Sorted(maps.Keys(sites))
	if err := settleSudo(r, names, func(n string) apply.Transport { return sites[n] }); err != nil {
		return configstore.Gathered{}, err
	}
	s := r.Step("gather the versions every site holds")
	g, err := configstore.GatherAnchored(l, sites, configured, id, a, configstore.GatherOptions{Sync: m.execute})
	var rb *configstore.RollbackError
	if errors.As(err, &rb) {
		s.End(ui.Warned, "older than this workstation has seen")
		why := rb.Error() + ". A site that deleted the newer versions can serve an old one, and applying it would put back old secrets."
		switch {
		case m.execute && m.accepted != nil && m.accepted.Lower && m.accepted.Revision == rb.Revision && m.accepted.SHA == rb.SHA:
			g.AcceptRollback(rb)
		case m.execute:
			if err := confirmWord(stdin, stdout, rollbackWord, why+" Proceed only if you know why the newer version is gone."); err != nil {
				return g, err
			}
			g.AcceptRollback(rb)
		case m.log:
			r.Note("the newest version is older than one seen here", why)
		default:
			r.Note("--execute asks for rollback", why)
		}
		s = r.Step("proceed from the older version")
		err = nil
	}
	if err != nil {
		s.Fail(err)
		if errors.Is(err, configstore.ErrNoAnchor) {
			todo := m.noAnchor
			if todo == "" {
				todo = "Pin the first version you trust with --root <sha>, or run paisans admin register to sign the version you hold."
			}
			return g, &ui.Problem{
				Hint:    "no version to start trusting from",
				Explain: err.Error() + ". A history found only on the sites could have been written by anyone with root on them. " + todo,
				Cause:   err,
			}
		}
		return g, err
	}
	s.Done(fmt.Sprintf("%d of %d sites answered directly", len(g.Direct()), configured))
	if !m.execute {
		r.Note("the sites were not asked to sync with their peers first", "A dry run changes no site. --execute starts each site's sync before reading it, so versions only its peers hold arrive too.")
	}
	for _, rc := range g.Reached {
		switch {
		case rc.Err != nil:
			r.Step(rc.Site).End(ui.Warned, "not reached")
			r.Explain(rc.Err.Error())
		case rc.SyncErr != "":
			r.Step(rc.Site).End(ui.Warned, "answered; its sync with its peers did not complete")
			r.Explain(rc.SyncErr)
		default:
			r.Step(rc.Site).End(ui.OK, "answered")
		}
	}
	return g, nil
}

// describe is a version in a line: revision, short hash and signer.
func describe(g configstore.Gathered, sha string) string {
	v := g.Versions[sha]
	who := g.Signers[sha]
	if who == "" {
		who = v.SignerFP
	}
	return fmt.Sprintf("revision %d, %s, signed by %s", v.Revision, sha[:12], who)
}

// forkProblem is the refusal for a fork, naming each head.
func forkProblem(g configstore.Gathered, more ...string) error {
	var heads []string
	for _, h := range g.Verdict.Heads {
		heads = append(heads, "- "+describe(g, h))
	}
	return &ui.Problem{Hint: "the sites hold a fork", Explain: "Two trusted versions build on one parent:\n" + strings.Join(heads, "\n") + "\nMerge their files by hand, then publish with paisans config publish --merge." + strings.Join(append([]string{""}, more...), " ")}
}

// confirmPartial asks for partial when fewer than a majority of the
// configured sites answered directly, before anything is written.
func confirmPartial(stdin io.Reader, stdout io.Writer, g configstore.Gathered, configured int) error {
	if g.Majority {
		return nil
	}
	return confirmWord(stdin, stdout, partialWord, fmt.Sprintf("Only %d of the %d sites answered directly. A site that answered could be serving an older version and hiding newer ones, which only the sites that did not answer would show.", len(g.Direct()), configured))
}

// signingKeyFor is the public key line a publish signs with: the recorded
// signing key, which the agent must hold, or, when none is recorded, the
// one agent key that belongs to an admin of the base (the local paisans-stack.yaml
// for a first version).
func signingKeyFor(l configstore.Local, local *config.Config) (string, error) {
	held := agentKeys()
	if recorded, err := recordedKey(l, held, ""); err != nil || recorded != "" {
		return recorded, err
	}
	base := local
	head, err := l.Head()
	if err != nil {
		return "", err
	}
	if head != "" {
		cache, err := l.Cache()
		if err != nil {
			return "", err
		}
		f, err := cache.Files(head)
		if err != nil {
			return "", &ui.Problem{Hint: "the base version is not in the local store", Explain: "Run paisans config pull, which gathers it again.", Cause: err}
		}
		if base, err = config.LoadBytes(f.Paisans); err != nil {
			return "", err
		}
	}
	var found []string
	for _, lines := range adminsHolding(base, held) {
		for _, line := range lines {
			if !slices.Contains(found, line) {
				found = append(found, line)
			}
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", &ui.Problem{Hint: "no key in your ssh-agent belongs to an admin", Explain: "A version is signed with an admin's key, listed under admins in the version it builds on. Load yours with ssh-add, or have another admin publish the change that adds it."}
	}
	return "", &ui.Problem{Hint: "your ssh-agent holds more than one admin key", Explain: "Which one signs is recorded and used from then on, so it is not guessed. Remove all but one from the agent for this publish (ssh-add -d)."}
}

// recordedKey is the signing key .paisans/signing-key records, and "" when
// none is. A recorded key the agent does not hold (held is the agent's key
// lines) is refused, and otherwise ends what the refusal says to do.
func recordedKey(l configstore.Local, held []string, otherwise string) (string, error) {
	recorded, err := l.SigningKey()
	if err != nil || recorded == "" {
		return recorded, err
	}
	k, _, _ := config.ParseKeyLine(recorded)
	if agentHolds(held, k.Fingerprint) {
		return recorded, nil
	}
	return "", &ui.Problem{Hint: "your ssh-agent does not hold your signing key", Explain: fmt.Sprintf("This workstation signs with %s, recorded in .paisans/signing-key. Load it with ssh-add and run this again%s.", k.Fingerprint, otherwise)}
}

// agentHolds reports whether one of held, the agent's key lines, has the
// fingerprint fp.
func agentHolds(held []string, fp string) bool {
	for _, line := range held {
		if h, _, err := config.ParseKeyLine(line); err == nil && h.Fingerprint == fp {
			return true
		}
	}
	return false
}

// adminsHolding is, for each admin cfg lists, the agent's key lines (held)
// that are among that admin's keys. An admin none of whose keys is held is
// left out.
func adminsHolding(cfg *config.Config, held []string) map[string][]string {
	out := map[string][]string{}
	for id, admin := range cfg.Admins {
		for _, al := range admin.KeyLines() {
			a, _, err := config.ParseKeyLine(al)
			if err != nil {
				continue
			}
			for _, line := range held {
				if h, _, err := config.ParseKeyLine(line); err == nil && h.Fingerprint == a.Fingerprint && !slices.Contains(out[id], line) {
					out[id] = append(out[id], line)
				}
			}
		}
	}
	return out
}

func runConfigPublish(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("config publish", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, configHelp)
	merge := fs.Bool("merge", false, "publish a version whose parents are every head, after merging them by hand")
	execute := fs.Bool("execute", false, "sign the version and push it to the sites")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return &ui.Problem{Hint: "config publish takes no argument", Explain: "Got " + strings.Join(fs.Args(), " ") + "."}
	}
	l, err := workstationOf(*configPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	r := reporter()
	r.Section("publish " + *configPath)
	key, err := signingKeyFor(l, cfg)
	if err != nil {
		return err
	}
	k, _, _ := config.ParseKeyLine(key)
	sites := configTransports(cfg)
	if !*execute {
		a, err := l.Anchor()
		if err != nil {
			return err
		}
		g, err := gatherAt(r, l, sites, len(sites), cfg.ID, a, gatherMode{}, stdin, stdout)
		if err != nil {
			return err
		}
		head, err := l.Head()
		if err != nil {
			return err
		}
		parents, err := configstore.Parents(g, head, *merge)
		if err != nil {
			if g.Verdict.Fork && !*merge {
				return forkProblem(g)
			}
			return err
		}
		// The same check --execute makes, so a dry run never promises a
		// version --execute refuses to sign.
		if err := configstore.CheckSigner(l, parents, cfg, key); err != nil {
			return err
		}
		what := "the first version"
		if len(parents) == 0 && !g.Majority {
			r.Warn(fmt.Sprintf("only %d of %d sites answered directly", len(g.Direct()), len(sites)), "A first version starts the deployment's history, so --execute asks for the word "+partialWord+" at a terminal.")
		}
		if len(parents) > 0 {
			var on []string
			for _, p := range parents {
				on = append(on, describe(g, p))
			}
			what = "a version on " + strings.Join(on, " and ")
		}
		r.Step("publish "+what).End(ui.Pending, "signed with "+k.Fingerprint+"; --execute signs and pushes it")
		return nil
	}
	a, err := l.Anchor()
	if err != nil {
		return err
	}
	g, err := gatherAt(r, l, sites, len(sites), cfg.ID, a, gatherMode{execute: true}, stdin, stdout)
	if err != nil {
		return err
	}
	// A first version starts the history. A site that answered empty may be
	// hiding one the sites that did not answer hold, so without a majority
	// the operator says partial before a second history begins.
	head, err := l.Head()
	if err != nil {
		return err
	}
	if parents, err := configstore.Parents(g, head, *merge); err == nil && len(parents) == 0 && len(g.Direct()) > 0 {
		if err := confirmPartial(stdin, stdout, g, len(sites)); err != nil {
			return err
		}
	}
	s := r.Step("sign and push the version, signed with " + k.Fingerprint)
	sha, pushed, err := configstore.PublishFrom(l, g, sites, key, configSignWith(key), *merge, configNow())
	var pe *configstore.PushError
	switch {
	case errors.As(err, &pe) && len(pushed) > 0:
		s.End(ui.Warned, fmt.Sprintf("%s reached %s", sha[:12], strings.Join(pushed, ", ")))
		for _, site := range slices.Sorted(maps.Keys(pe.Failed)) {
			r.Warn(site+" did not take the version", fmt.Sprintf("%v. It gets the version from its peers' sync, or from publishing again.", pe.Failed[site]))
		}
	case err != nil:
		s.Fail(err)
		var rb *configstore.RollbackError
		if errors.As(err, &rb) {
			return &ui.Problem{Hint: "the sites' newest version is older than one seen here", Explain: rb.Error() + ". Nothing was published: a version on it would build on an old one. Find out why the newer version is gone; paisans config log shows what each site holds.", Cause: err}
		}
		if errors.Is(err, configstore.ErrNoAnchor) {
			return &ui.Problem{Hint: "no version to start trusting from", Explain: err.Error() + ". Run paisans config pull --root <sha>, or paisans admin register.", Cause: err}
		}
		return err
	default:
		s.Done(fmt.Sprintf("%s reached %s", sha[:12], strings.Join(pushed, ", ")))
	}
	r.Result("published %s", sha)
	return nil
}

func runConfigPull(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("config pull", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, configHelp)
	root := fs.String("root", "", "pin this version as the root, the version trust starts from")
	execute := fs.Bool("execute", false, "write the sites' newest version to the local files")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return &ui.Problem{Hint: "config pull takes no argument", Explain: "Got " + strings.Join(fs.Args(), " ") + "."}
	}
	l, err := workstationOf(*configPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	a, err := anchorFor(l, *root)
	if err != nil {
		return err
	}
	r := reporter()
	r.Section("pull into " + *configPath)
	sites := configTransports(cfg)
	g, err := gatherAt(r, l, sites, len(sites), cfg.ID, a, gatherMode{execute: *execute}, stdin, stdout)
	if err != nil {
		return err
	}
	if len(g.Direct()) == 0 {
		return &ui.Problem{Hint: "no site answered", Explain: "Nothing was pulled.", Cause: configstore.UnreachedError(g)}
	}
	if g.Verdict.Fork {
		return forkProblem(g)
	}
	if len(g.Verdict.Heads) == 0 {
		r.Result("the sites hold no version yet; paisans config publish makes the first")
		return nil
	}
	head := g.Verdict.Heads[0]
	base, err := l.Head()
	if err != nil {
		return err
	}
	cache, err := l.Cache()
	if err != nil {
		return err
	}
	headFiles, err := cache.Files(head)
	if err != nil {
		return err
	}
	localFiles, err := l.Files()
	if err != nil {
		return err
	}
	edited := !localFiles.Equal(headFiles)
	if base != "" && edited {
		if bf, err := cache.Files(base); err == nil {
			edited = !localFiles.Equal(bf)
		}
	}
	switch {
	case base == head:
		if *execute {
			if err := recordPull(l, g, *root); err != nil {
				return err
			}
		}
		if edited {
			r.Result("up to date with %s; the local edits are not published yet", describe(g, head))
		} else {
			r.Result("up to date with %s", describe(g, head))
		}
		return nil
	case edited:
		if handled, err := pullMergingSecrets(r, l, g, cfg.ID, head, base, *root, len(sites), localFiles, headFiles, *execute, stdin, stdout); handled {
			return err
		}
		return pullOverEdits(r, l, g, head, base, localFiles, headFiles, *execute, stdout)
	}
	if !*execute {
		r.Step("move the local files to "+describe(g, head)).End(ui.Pending, "--execute writes them")
		if !g.Majority {
			r.Warn(fmt.Sprintf("only %d of %d sites answered directly", len(g.Direct()), len(sites)), "--execute asks for the word "+partialWord+" at a terminal.")
		}
		return nil
	}
	if err := confirmPartial(stdin, stdout, g, len(sites)); err != nil {
		return err
	}
	if err := l.Checkout(head); err != nil {
		return err
	}
	if err := recordPull(l, g, *root); err != nil {
		return err
	}
	r.Result("moved the local files to %s", describe(g, head))
	return nil
}

// recordPull is what a pull with --execute records once it has passed its
// last confirmation: the change to the seen record its gather carries, and
// root, when given, as the pinned root.
func recordPull(l configstore.Local, g configstore.Gathered, root string) error {
	if err := l.RecordSeen(g); err != nil {
		return err
	}
	if root != "" {
		return l.SetRoot(root)
	}
	return nil
}

// pullOverEdits refuses a pull that would overwrite local edits made on an
// older base, and shows what differs: paisans-stack.yaml as a diff, the secrets by
// key name only. With execute the head's files go to .paisans/incoming/ for
// a merge by hand.
func pullOverEdits(r ui.Reporter, l configstore.Local, g configstore.Gathered, head, base string, local, theirs configstore.Files, execute bool, stdout io.Writer) error {
	if err := showDifference(stdout, local, theirs); err != nil {
		return err
	}
	where := "--execute writes the head's files to " + l.Incoming() + " to merge from"
	if execute {
		if err := l.WriteIncoming(head); err != nil {
			return err
		}
		where = "The head's files are in " + l.Incoming() + "."
	}
	from := "no recorded base"
	if base != "" {
		from = "base " + base[:12]
	}
	return &ui.Problem{
		Hint:    "the local files have edits the sites' newest version lacks",
		Explain: fmt.Sprintf("They were edited from %s, and the sites hold %s. %s Merge them by hand, then publish with paisans config publish --merge.", from, describe(g, head), where),
	}
}

// showDifference prints how the local files differ from the head's:
// paisans-stack.yaml as a diff, the secrets by key name only, never a value.
func showDifference(stdout io.Writer, local, theirs configstore.Files) error {
	diff, err := unifiedDiff(local.Paisans, theirs.Paisans, "local/"+config.FileName, "head/"+config.FileName)
	if err != nil {
		return err
	}
	if diff != "" {
		fmt.Fprint(stdout, diff)
	}
	for _, line := range secretsKeyChanges(local.Secrets, theirs.Secrets) {
		fmt.Fprintln(stdout, line)
	}
	if !bytesEqual(local.SOPS, theirs.SOPS) {
		fmt.Fprintln(stdout, ".sops.yaml differs")
	}
	return nil
}

func bytesEqual(a, b []byte) bool { return string(a) == string(b) }

// unifiedDiff is git's unified diff of two texts, "" when they are equal.
func unifiedDiff(a, b []byte, nameA, nameB string) (string, error) {
	dir, err := os.MkdirTemp("", "paisans-diff-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	pa, pb := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.WriteFile(pa, a, 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(pb, b, 0o600); err != nil {
		return "", err
	}
	cmd := exec.Command("git", "-c", "core.quotePath=false", "diff", "--no-index", "--no-color", "--src-prefix=", "--dst-prefix=", pa, pb)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.Output()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return "", fmt.Errorf("diff "+config.FileName+": %w", err)
	}
	s := strings.ReplaceAll(string(out), pa, nameA)
	return strings.ReplaceAll(s, pb, nameB), nil
}

// secretsKeyChanges names each key of the secrets file that is only on one
// side or whose stored value differs, never a value. The file is compared
// as stored, encrypted, and sops's own metadata is left out; a re-encrypted
// file differs at every key.
func secretsKeyChanges(local, theirs []byte) []string {
	a, errA := flatKeys(local)
	b, errB := flatKeys(theirs)
	if errA != nil || errB != nil {
		if !bytesEqual(local, theirs) {
			return []string{"secrets.enc.yaml differs, and cannot be read as YAML to say where"}
		}
		return nil
	}
	var out []string
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		va, inA := a[k]
		vb, inB := b[k]
		switch {
		case inA && !inB:
			out = append(out, "secrets: "+k+" only in the local file")
		case !inA && inB:
			out = append(out, "secrets: "+k+" only in the head")
		case va != vb:
			out = append(out, "secrets: "+k+" differs")
		}
	}
	return out
}

// flatKeys is every leaf of a YAML document by dotted key, sops's metadata
// left out.
func flatKeys(data []byte) (map[string]string, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	delete(doc, "sops")
	out := map[string]string{}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, c := range t {
				p := k
				if prefix != "" {
					p = prefix + "." + k
				}
				walk(p, c)
			}
		default:
			out[prefix] = fmt.Sprint(t)
		}
	}
	walk("", doc)
	return out, nil
}

func runConfigLog(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("config log", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, configHelp)
	root := fs.String("root", "", "judge from this root, as if pinned, without recording it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return &ui.Problem{Hint: "config log takes no argument", Explain: "Got " + strings.Join(fs.Args(), " ") + "."}
	}
	l, err := workstationOf(*configPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	a, err := anchorFor(l, *root)
	if err != nil {
		return err
	}
	r := reporter()
	r.Section("versions of " + *configPath)
	sites := configTransports(cfg)
	g, err := gatherAt(r, l, sites, len(sites), cfg.ID, a, gatherMode{log: true}, stdin, stdout)
	if err != nil {
		return err
	}
	shas := make([]string, 0, len(g.Versions))
	for sha := range g.Versions {
		shas = append(shas, sha)
	}
	sort.Slice(shas, func(i, j int) bool {
		vi, vj := g.Versions[shas[i]], g.Versions[shas[j]]
		if vi.Revision != vj.Revision {
			return vi.Revision > vj.Revision
		}
		return shas[i] < shas[j]
	})
	for _, sha := range shas {
		v := g.Versions[sha]
		who := g.Signers[sha]
		if who == "" {
			who = v.SignerFP
		}
		state := "valid"
		if !g.Verdict.Trusted[sha] {
			state = "not trusted: " + g.Verdict.Reasons[sha]
		}
		if slices.Contains(g.Verdict.Heads, sha) {
			state += ", head"
		}
		holders := strings.Join(g.Holders[sha], ", ")
		if holders == "" {
			holders = "no site that answered"
		}
		fmt.Fprintf(stdout, "%d  %s  %s  %s  %s  held by %s\n", v.Revision, sha[:12], who, v.Committed.Format(time.RFC3339), state, holders)
	}
	if len(shas) == 0 {
		r.Result("no site holds a version yet")
	}
	return nil
}

func runConfigRecover(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("config recover", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, configHelp)
	var from pathList
	fs.Var(&from, "from", "a host of the deployment to recover from, user@host[:port] (repeatable)")
	root := fs.String("root", "", "pin this version as the root, the version trust starts from")
	id := fs.String("id", "", "the deployment's id, when a host's registry names more than one")
	execute := fs.Bool("execute", false, "write the recovered files and state")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return &ui.Problem{Hint: "config recover takes no argument", Explain: "Got " + strings.Join(fs.Args(), " ") + "."}
	}
	if len(from) == 0 {
		return &ui.Problem{Hint: "config recover needs --from", Explain: "Name a host of the deployment with --from user@host[:port], Eg: --from admin@203.0.113.9; every site the recovered configuration names is reached after it."}
	}
	l, err := workstationOf(*configPath)
	if err != nil {
		return err
	}
	if l.HasFiles() {
		return &ui.Problem{Hint: "there are configuration files here already", Explain: "Recovery writes onto a workstation with nothing, so it never overwrites a file. Use paisans config pull where the files exist, or recover into an empty directory with --config."}
	}
	a, err := anchorFor(l, *root)
	if err != nil {
		return err
	}
	r := reporter()
	r.Section("recover into " + l.Dir)
	hosts := map[string]apply.Transport{}
	for _, f := range from {
		dest, err := config.ParseDestination(f)
		if err != nil {
			return sshFlagProblem(f, err)
		}
		hosts[dest.String()] = reachDestination(dest, true)
	}
	deploymentID, err := recoverID(hosts, *id)
	if err != nil {
		return err
	}
	g, err := gatherAt(r, l, hosts, len(hosts), deploymentID, a, gatherMode{execute: *execute}, stdin, stdout)
	if err != nil {
		return err
	}
	if len(g.Verdict.Heads) == 0 {
		return &ui.Problem{Hint: "no host named holds a version", Explain: "Nothing was recovered. Name another host with --from.", Cause: configstore.UnreachedError(g)}
	}
	// Every site a trusted head names is reached, not only the hosts given,
	// so a host serving an old version is outvoted by the rest. With a fork,
	// every head's sites are reached.
	sites, err := recoverSites(l, g, deploymentID)
	if err != nil {
		return err
	}
	r.Section("every site the newest versions name")
	g, err = gatherAt(r, l, sites, len(sites), deploymentID, a, gatherMode{execute: *execute, accepted: g.Seen}, stdin, stdout)
	if err != nil {
		return err
	}
	if g.Verdict.Fork {
		if !*execute {
			return forkProblem(g, "--execute writes each head's files to "+l.Incoming()+"/<sha>/.")
		}
		for _, h := range g.Verdict.Heads {
			if err := l.WriteIncomingFor(h); err != nil {
				return err
			}
		}
		return forkProblem(g, "Each head's files are in "+l.Incoming()+"/<sha>/; nothing else was written.")
	}
	head := g.Verdict.Heads[0]
	pin := a.Root
	if pin == "" {
		if roots := g.Roots(head); len(roots) == 1 {
			pin = roots[0]
		}
	}
	if !*execute {
		r.Step("write the files of "+describe(g, head)).End(ui.Pending, "--execute writes them")
		if !g.Majority {
			r.Warn(fmt.Sprintf("only %d of %d sites answered directly", len(g.Direct()), len(sites)), "--execute asks for the word "+partialWord+" at a terminal.")
		}
		return nil
	}
	if err := confirmPartial(stdin, stdout, g, len(sites)); err != nil {
		return err
	}
	if err := l.Checkout(head); err != nil {
		return err
	}
	if err := recordPull(l, g, pin); err != nil {
		return err
	}
	r.Result("recovered %s into %s", describe(g, head), l.Dir)
	return nil
}

// recoverSites reaches every site a trusted head of g declares, by name,
// from the newest head that declares it. Each head must be the deployment
// being recovered.
func recoverSites(l configstore.Local, g configstore.Gathered, id string) (map[string]apply.Transport, error) {
	cache, err := l.Cache()
	if err != nil {
		return nil, err
	}
	sites := map[string]apply.Transport{}
	for _, h := range g.Verdict.Heads {
		f, err := cache.Files(h)
		if err != nil {
			return nil, err
		}
		cfg, err := config.LoadBytes(f.Paisans)
		if err != nil {
			return nil, fmt.Errorf("version %s: %w", h, err)
		}
		if cfg.ID != id {
			return nil, &ui.Problem{Hint: "a recovered version is another deployment's", Explain: fmt.Sprintf("Version %s declares id %s, and the hosts' store is %s's.", h, cfg.ID, id)}
		}
		for name, t := range configTransports(cfg) {
			if _, ok := sites[name]; !ok {
				sites[name] = t
			}
		}
	}
	return sites, nil
}

// recoverID is the deployment a recovery is for: id when given, else the one
// deployment the hosts' registries name. More than one is refused, since
// which one to recover is the operator's to say, and so is an entry that is
// not a deployment id: whoever has root on a host can write its registry,
// and the id becomes part of every command a recovery runs on every site.
func recoverID(hosts map[string]apply.Transport, id string) (string, error) {
	if id != "" {
		if !deployment.ValidID(id) {
			return "", &ui.Problem{Hint: fmt.Sprintf("--id %q is not a deployment id", id), Explain: "Give the full id, as paisans host deployments lists it."}
		}
		return id, nil
	}
	ids := map[string]bool{}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(hosts)) {
		reg, err := registry.Read(hosts[name])
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		for _, found := range slices.Sorted(maps.Keys(reg.Deployments)) {
			if !deployment.ValidID(found) {
				return "", &ui.Problem{Hint: name + " lists a deployment id that is not one", Explain: fmt.Sprintf("Its registry names %q, which is not a deployment id, so nothing is read from any host with it. Whoever can write that registry may not be trusted; recover from another host with --from, or name the deployment with --id.", found)}
			}
			ids[found] = true
		}
	}
	switch len(ids) {
	case 1:
		for found := range ids {
			return found, nil
		}
	case 0:
		return "", &ui.Problem{Hint: "no host named lists a deployment", Explain: "Name the deployment with --id, or another host with --from.", Cause: errors.Join(errs...)}
	}
	list := slices.Sorted(maps.Keys(ids))
	return "", &ui.Problem{Hint: "the hosts list more than one deployment", Explain: "Name the one to recover with --id: " + strings.Join(list, ", ") + "."}
}
