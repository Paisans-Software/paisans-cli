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

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"gopkg.in/yaml.v3"
)

// What follows is `paisans deployment`: the signed copies of the configuration
// every site keeps (docs/specs/2026-10-12-config-store.md). publish puts the
// local files on the sites as a new version, pull brings the sites' newest
// trusted version to the local files, log lists every version, and recover
// writes the newest onto a workstation that has nothing. Every decision about
// which version to trust is made here, on the workstation, from the
// operator's own anchor; the sites only store.

// configSiteTransport is how a config command reaches a site paisans-stack.yaml
// declares: its ssh section. Tests replace it.
var configSiteTransport = func(name string, site config.Site) apply.Transport {
	return siteTransport(name, site, "")
}

// configSignWith is what git's user.signingKey is given to sign with the
// public key line: key:: and the line, so the ssh-agent signs. Tests replace
// it with a private key file.
var configSignWith = func(line string) string { return "key::" + line }

// configNow is the clock a version's commit date is written by. Tests
// replace it.
var configNow = time.Now

func runDeployment(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return &ui.Problem{Hint: "deployment takes one subcommand", Explain: "Use apply, list, checkout, fetch, publish, pull, log or recover."}
	}
	switch args[0] {
	case "apply":
		return runApply(args[1:], stdin, stdout)
	case "list":
		return runDeploymentList(args[1:], stdout)
	case "checkout":
		return runDeploymentCheckout(args[1:])
	case "fetch":
		return runDeploymentFetch(args[1:])
	case "publish":
		return runDeploymentPublish(args[1:], stdin, stdout)
	case "pull":
		return runDeploymentPull(args[1:], stdin, stdout)
	case "log":
		return runDeploymentLog(args[1:], stdin, stdout)
	case "recover":
		return runDeploymentRecover(args[1:], stdin, stdout)
	}
	return &ui.Problem{Hint: fmt.Sprintf("deployment has no subcommand %q", args[0]), Explain: "Use apply, list, checkout, fetch, publish, pull, log or recover."}
}

// configHelp is the --config flag's help, the same for every subcommand.
const configHelp = "path to the deployment declaration; the store's state is kept in .paisans/ beside it"

// workstationOf is the workstation whose paisans-stack.yaml is at path. The three
// files of a version have fixed names, so the configuration must be named
// paisans-stack.yaml for the files on disk to be the ones a version holds.
// Dir is absolute, so every message naming it or what is under it says where
// the files are when read later or elsewhere, not ".".
func workstationOf(path string) (configstore.Local, error) {
	if filepath.Base(path) != config.FileName {
		return configstore.Local{}, &ui.Problem{Hint: "--config must name a file called " + config.FileName, Explain: "A stored version holds " + config.FileName + ", secrets.enc.yaml and .sops.yaml by those names, so the files beside --config must carry them. Got " + path + "."}
	}
	dir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return configstore.Local{}, &ui.Problem{Hint: "the directory of --config cannot be resolved", Explain: errLine(err) + ".", Cause: err}
	}
	return configstore.Local{Dir: dir}, nil
}

// statePath is where l records name: what a refusal naming a record says
// to look at.
func statePath(l configstore.Local, name string) string {
	return filepath.Join(l.Dir, ".paisans", name)
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
		return a, &ui.Problem{Hint: fmt.Sprintf("--root %q is not a commit id", root), Explain: "Give the full hash of the root to pin, as " + progname.Cmd() + " deployment log prints it on a root's line."}
	}
	if a.Root != "" && a.Root != root {
		return a, &ui.Problem{Hint: "--root differs from the pinned root", Explain: fmt.Sprintf("This workstation pinned %s in %s. Drop --root, or remove that file if the pin is wrong.", a.Root, statePath(l, "config-root"))}
	}
	a.Root = root
	return a, nil
}

// namedAnchor adds the key keyPath names, a public key file, to a, for pull,
// log and recover, when the workstation records no signing key. It is marked
// Named, so configstore.Judge lets it anchor only on versions it signed
// itself, under one root, that the trust rules trust already, and the gather
// refuses a trusted version of another deployment. With a root pinned the
// root anchors alone: the key is ignored, and ignored says so.
//
// The result maps the key, by fingerprint, to its line when the ssh-agent
// holds it: the key recordAnchorKey may record. A key the agent does not
// hold anchors and is never recorded, since it may be a colleague's.
func namedAnchor(a configstore.Anchor, keyPath string) (_ configstore.Anchor, held map[string]string, ignored bool, err error) {
	if keyPath == "" {
		return a, nil, false, nil
	}
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return a, nil, false, &ui.Problem{Hint: "cannot read --key", Explain: errLine(err) + ".", Cause: err}
	}
	k, opts, err := config.ParseKeyLine(strings.TrimSpace(string(data)))
	if err != nil || len(opts) > 0 {
		return a, nil, false, &ui.Problem{Hint: "--key must name a public key file", Explain: "Give the .pub file of a key you trust to have signed the deployment's versions, Eg: ~/.ssh/id_ed25519.pub."}
	}
	if len(a.Signers) > 0 {
		if !slices.Contains(a.Signers, k.Fingerprint) {
			return a, nil, false, &ui.Problem{Hint: "--key differs from the recorded signing key", Explain: fmt.Sprintf("This workstation signs with %s, its recorded signing key, and trust starts from it. Drop --key.", a.Signers[0])}
		}
		return a, nil, false, nil
	}
	if a.Root != "" {
		return a, nil, true, nil
	}
	a.Signers, a.Named = []string{k.Fingerprint}, true
	lines := map[string]string{}
	if agentHolds(agentKeys(), k.Fingerprint) {
		lines[k.Fingerprint] = k.Line
	}
	return a, lines, false, nil
}

// recordAnchorKey records, as the signing key, the key in held (from
// namedAnchor) that signed the version g's trust starts from, so later
// publishes sign with it. It records it only when the agent holds it and it
// is a key of an operator the head lists: a removed key, or a colleague's
// public key named with --key, anchors and is not recorded. A pinned root
// that anchored alone, or a fork, records nothing.
func recordAnchorKey(l configstore.Local, g configstore.Gathered, held map[string]string) error {
	at := g.Verdict.AnchorAt
	if at == "" || len(held) == 0 || g.Verdict.Fork || len(g.Verdict.Heads) != 1 {
		return nil
	}
	line, ok := held[g.Versions[at].SignerFP]
	if !ok {
		return nil
	}
	cache, err := l.Cache()
	if err != nil {
		return err
	}
	f, err := cache.Files(g.Verdict.Heads[0])
	if err != nil {
		return err
	}
	head, err := config.LoadBytes(f.Paisans)
	if err != nil {
		return err
	}
	if len(operatorsHolding(head, []string{line})) == 0 {
		return nil
	}
	return l.SetSigningKey(line)
}

// noteKeyIgnored tells the operator a --key a pinned root made moot.
func noteKeyIgnored(r ui.Reporter, ignored bool) {
	if ignored {
		r.Note("--key ignored: a root is pinned", "The pinned root anchors alone, so the key neither adds to nor changes what is trusted, and it is not recorded.")
	}
}

// keyHelp is the --key flag's help for pull, log and recover.
const keyHelp = "a public key file (.pub) you trust, to start trust from when .paisans/signing-key records none; recorded as the signing key when your ssh-agent holds it"

// noAnchorNamed is what a pull, log or recover refusal for a missing anchor
// says to do, with otherwise added for a workstation that has files.
func noAnchorNamed(otherwise string) string {
	return "Name your own operator key's public key file with --key <path.pub>, loaded with ssh-add so it is recorded as this workstation's signing key, or pin the first version you trust with --root <sha>" + otherwise + "."
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
	// answers is how rollback is answered: at the terminal, or by --yes.
	answers answerer
}

// gatherAt gathers from sites, reporting each, and explains a missing anchor.
// A dry run changes no site, and says so. A head older than one this
// workstation has seen goes on, with --execute, only once the operator types
// rollback at a terminal. Nothing is written to the seen record here: the
// gather carries the change (a newer head, or the accepted older one), and
// the command records it with Local.RecordSeen once it commits its change.
func gatherAt(r ui.Reporter, l configstore.Local, sites map[string]apply.Transport, id string, a configstore.Anchor, m gatherMode, stdin io.Reader, stdout io.Writer) (configstore.Gathered, error) {
	s := r.Step("gather the versions every site holds")
	g, err := configstore.GatherAnchored(l, sites, id, a, configstore.GatherOptions{Sync: m.execute})
	var rb *configstore.RollbackError
	if errors.As(err, &rb) {
		s.End(ui.Warned, "older than this workstation has seen")
		why := rb.Error() + ". A site that deleted the newer versions can serve an old one, and applying it would put back old secrets."
		switch {
		case m.execute && m.accepted != nil && m.accepted.Lower && m.accepted.Revision == rb.Revision && m.accepted.SHA == rb.SHA:
			g.AcceptRollback(rb)
		case m.execute:
			if err := confirmWord(m.answers, stdin, stdout, rollbackWord, why+" Proceed only if you know why the newer version is gone."); err != nil {
				return g, err
			}
			g.AcceptRollback(rb)
		case m.log:
			r.Note("the newest version is older than one seen here", why)
		default:
			r.Note("--execute will ask for rollback", why)
		}
		s = r.Step("proceed from the older version")
		err = nil
	}
	if err != nil {
		s.Fail(err)
		var ns *configstore.PinNotServedError
		if errors.As(err, &ns) {
			return g, &ui.Problem{Hint: "the sites serve none of the pinned root's history", Explain: ns.Error() + ". Either this workstation's pin is out of date, the deployment having been redeployed under the same id since it was pinned, or a site that answered holds a history it should not, such as a host that was never evicted. Check which sites answered, above. If the pin is out of date, get the current root from another operator, the full hash on a root's line of their " + progname.Cmd() + " deployment log, and recover from it into an empty directory: " + progname.Cmd() + " deployment recover --from <user@host> --root <sha> --config <empty directory>/paisans-stack.yaml.", Cause: err}
		}
		if errors.Is(err, configstore.ErrNoAnchor) {
			todo := m.noAnchor
			if todo == "" {
				todo = "Pin the first version you trust with --root <sha>, or run " + progname.Cmd() + " operator register to sign the version you hold."
			}
			return g, &ui.Problem{
				Hint:    "no version to start trusting from",
				Explain: err.Error() + ". A history found only on the sites could have been written by anyone with root on them. " + todo,
				Cause:   err,
			}
		}
		return g, err
	}
	done := fmt.Sprintf("%d of %d sites answered directly", len(g.Direct()), len(sites))
	if at := g.Verdict.AnchorAt; at != "" && slices.Contains(a.Signers, g.Versions[at].SignerFP) {
		done += ", anchored on " + g.Versions[at].SignerFP
	}
	s.Done(done)
	// A stack of one site has no peers, so a sync would bring nothing.
	if !m.execute && len(sites) > 1 {
		r.Note("the sites were not asked to sync with their peers first", "A dry run changes no site. --execute will start each site's sync before reading it, so versions only its peers hold arrive too.")
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
	return &ui.Problem{Hint: "the sites hold a fork", Explain: "Two trusted versions build on one parent:\n" + forkHeads(g) + "\nMerge their files by hand into one draft, writing each head's files into a folder of its own with " + progname.Cmd() + " deployment checkout --head <sha>, then publish it with " + progname.Cmd() + " deployment publish --merge." + strings.Join(append([]string{""}, more...), " ")}
}

// forkHeads lists the heads of a fork, one line each.
func forkHeads(g configstore.Gathered) string {
	var heads []string
	for _, h := range g.Verdict.Heads {
		heads = append(heads, "- "+describe(g, h))
	}
	return strings.Join(heads, "\n")
}

// signingKeyFor is the public key line a publish signs with: the recorded
// signing key, which the agent must hold, or, when none is recorded, the
// one agent key that belongs to an operator of the base (the local paisans-stack.yaml
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
			return "", &ui.Problem{Hint: "the base version is not in the local store", Explain: "Run " + progname.Cmd() + " deployment pull, which gathers it again.", Cause: err}
		}
		if base, err = config.LoadBytes(f.Paisans); err != nil {
			return "", err
		}
	}
	var found []string
	for _, lines := range operatorsHolding(base, held) {
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
		return "", &ui.Problem{Hint: "no key in your ssh-agent belongs to an operator", Explain: "A version is signed with an operator's key, listed under operators in the version it builds on. Load yours with ssh-add, or have another operator publish the change that adds it."}
	}
	return "", &ui.Problem{Hint: "your ssh-agent holds more than one operator key", Explain: "Which one signs is recorded and used from then on, so it is not guessed. Remove all but one from the agent for this publish (ssh-add -d)."}
}

// recordedKey is the signing key l records, and "" when none is. A
// recorded key the agent does not hold (held is the agent's key lines) is
// refused, and otherwise ends what the refusal says to do.
func recordedKey(l configstore.Local, held []string, otherwise string) (string, error) {
	recorded, err := l.SigningKey()
	if err != nil || recorded == "" {
		return recorded, err
	}
	k, _, _ := config.ParseKeyLine(recorded)
	if agentHolds(held, k.Fingerprint) {
		return recorded, nil
	}
	return "", &ui.Problem{Hint: "your ssh-agent does not hold your signing key", Explain: fmt.Sprintf("This workstation signs with %s, recorded in %s. Load it with ssh-add and run this again%s.", k.Fingerprint, statePath(l, "signing-key"), otherwise)}
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

// operatorsHolding is, for each operator cfg lists, the agent's key lines (held)
// that are among that operator's keys. An operator none of whose keys is held is
// left out.
func operatorsHolding(cfg *config.Config, held []string) map[string][]string {
	out := map[string][]string{}
	for id, operator := range cfg.Operators {
		for _, al := range operator.KeyLines() {
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

func runDeploymentPublish(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("deployment publish", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, configHelp)
	merge := fs.Bool("merge", false, "publish a version whose parents are every head, after merging them by hand")
	execute := fs.Bool("execute", false, "sign the version and push it to the sites")
	answers := yesFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return &ui.Problem{Hint: "deployment publish takes no argument", Explain: "Got " + strings.Join(fs.Args(), " ") + "."}
	}
	ans, err := answers()
	if err != nil {
		return err
	}
	d, err := draftAt(*configPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	// A version holds the secrets encrypted, and only encrypted.
	if err := config.CheckSecretsFile(filepath.Join(d.Dir, configstore.FileSecrets)); err != nil {
		return err
	}
	raw, err := d.Files()
	if err != nil {
		return &ui.Problem{Hint: "the files of a version cannot be read", Explain: "publish signs the draft's " + config.FileName + ", secrets.enc.yaml and .sops.yaml from " + d.Dir + " as a version. " + errLine(err) + ".", Cause: err}
	}
	files, err := asVersion(raw)
	if err != nil {
		return err
	}
	r := reporter()
	r.Section("publish " + *configPath)
	sites := configTransports(cfg)
	t, err := gatherDeploy(r, d, cfg, sites, raw.Paisans, gatherMode{execute: *execute, answers: ans}, stdin, stdout)
	defer t.done()
	if err != nil {
		return err
	}
	g := t.g
	t.again, t.source = "deployment publish --execute", "published from "+d.Dir
	parents, err := configstore.Parents(g, t.base, *merge)
	if err != nil {
		if g.Verdict.Fork && !*merge {
			return forkProblem(g)
		}
		return err
	}
	// After the gather, which brings the base into the system copy's
	// store: the key is checked against the operators the base lists, the
	// same check for a dry run as for --execute, so a dry run never
	// promises a version --execute refuses to sign.
	key, err := signingKeyFor(t.sys, cfg)
	if err != nil {
		return err
	}
	if err := configstore.CheckSigner(t.sys, parents, cfg, key); err != nil {
		return err
	}
	k, _, _ := config.ParseKeyLine(key)
	if !*execute {
		what := "the first version"
		if len(parents) > 0 {
			var on []string
			for _, p := range parents {
				on = append(on, describe(g, p))
			}
			what = "a version on " + strings.Join(on, " and ")
		}
		r.Step("publish "+what).End(ui.Pending, "signed with "+k.Fingerprint)
		return nil
	}
	s := r.Step("sign and push the version, signed with " + k.Fingerprint)
	// The revision is the one publishFiles gives the version: its base's
	// when the draft is that version, else one past its parents'. It is
	// found from the gather, so nothing can fail once a site took it.
	revision := 1
	for _, p := range parents {
		revision = max(revision, g.Versions[p].Revision+1)
	}
	var sha string
	err = t.deploy(r, s, files, func(sys configstore.Local) (string, int, error) {
		var pushed []string
		var err error
		sha, pushed, err = configstore.PublishOn(sys, g, t.base, files, sites, key, configSignWith(key), *merge, configNow())
		rev := revision
		if !*merge && sha != "" && sha == t.base {
			rev = g.Versions[sha].Revision
		}
		var pe *configstore.PushError
		switch {
		case errors.As(err, &pe) && len(pushed) > 0:
			s.End(ui.Warned, fmt.Sprintf("%s reached %s", sha[:12], strings.Join(pushed, ", ")))
			for _, site := range slices.Sorted(maps.Keys(pe.Failed)) {
				r.Warn(site+" did not take the version", fmt.Sprintf("%v. It gets the version from its peers' sync, or from publishing again.", pe.Failed[site]))
			}
		case err != nil:
			s.Fail(err)
			return sha, 0, err
		default:
			s.Done(sha[:12])
		}
		return sha, rev, nil
	})
	if err != nil {
		return err
	}
	r.Result("published %s", sha)
	return nil
}

func runDeploymentPull(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("deployment pull", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, configHelp)
	root := fs.String("root", "", "pin this version as the root, the version trust starts from")
	keyPath := fs.String("key", "", keyHelp)
	execute := fs.Bool("execute", false, "write the sites' newest version to the local files")
	answers := yesFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return &ui.Problem{Hint: "deployment pull takes no argument", Explain: "Got " + strings.Join(fs.Args(), " ") + "."}
	}
	ans, err := answers()
	if err != nil {
		return err
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
	a, held, ignored, err := namedAnchor(a, *keyPath)
	if err != nil {
		return err
	}
	r := reporter()
	r.Section("pull into " + *configPath)
	noteKeyIgnored(r, ignored)
	sites := configTransports(cfg)
	m := gatherMode{execute: *execute, answers: ans, noAnchor: noAnchorNamed(", or run " + progname.Cmd() + " operator register to sign the version you hold")}
	g, err := gatherAt(r, l, sites, cfg.ID, a, m, stdin, stdout)
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
		r.Result("the sites hold no version yet; %s deployment publish makes the first", progname.Cmd())
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
			if err := recordPull(l, g, *root, held); err != nil {
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
		if handled, err := pullMergingSecrets(r, l, g, cfg.ID, head, base, *root, held, localFiles, headFiles, *execute, ans, stdin, stdout); handled {
			return err
		}
		return pullOverEdits(r, l, g, head, base, localFiles, headFiles, *execute, stdout)
	}
	if !*execute {
		r.Step("move the local files to "+describe(g, head)).End(ui.Pending, "--execute will write them")
		return nil
	}
	if err := l.Checkout(head); err != nil {
		return err
	}
	if err := recordPull(l, g, *root, held); err != nil {
		return err
	}
	r.Result("moved the local files to %s", describe(g, head))
	return nil
}

// recordPull is what a pull with --execute records once it has passed its
// last confirmation: the change to the seen record its gather carries,
// root, when given, as the pinned root, and the key of held (namedAnchor's)
// that anchored the gather as the signing key.
func recordPull(l configstore.Local, g configstore.Gathered, root string, held map[string]string) error {
	if err := l.RecordSeen(g); err != nil {
		return err
	}
	if root != "" {
		if err := l.SetRoot(root); err != nil {
			return err
		}
	}
	return recordAnchorKey(l, g, held)
}

// pullOverEdits refuses a pull that would overwrite local edits made on an
// older base, and shows what differs: paisans-stack.yaml as a diff, the secrets by
// key name only. With execute the head's files go to .paisans/incoming/ for
// a merge by hand.
func pullOverEdits(r ui.Reporter, l configstore.Local, g configstore.Gathered, head, base string, local, theirs configstore.Files, execute bool, stdout io.Writer) error {
	if err := showDifference(stdout, local, theirs); err != nil {
		return err
	}
	where := "--execute will write the head's files to " + l.Incoming() + " to merge from"
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
		Explain: fmt.Sprintf("They were edited from %s, and the sites hold %s. %s Merge them by hand, then publish with %s deployment publish --merge.", from, describe(g, head), where, progname.Cmd()),
	}
}

// showDifference prints how the local files differ from the head's:
// paisans-stack.yaml as a diff, the secrets by key name only, never a value.
func showDifference(stdout io.Writer, local, theirs configstore.Files) error {
	lines, err := difference(local, theirs, localAndHead)
	if err != nil {
		return err
	}
	for _, line := range lines {
		fmt.Fprintln(stdout, line)
	}
	return nil
}

// sides names the two sides of a difference: dirA and dirB prefix
// paisans-stack.yaml in the diff, and inA and inB end the line for a secret
// only one side holds.
type sides struct{ dirA, dirB, inA, inB string }

// localAndHead is how pull and register name the sides: the local files,
// then the head's.
var localAndHead = sides{dirA: "local/", dirB: "head/", inA: "the local file", inB: "the head"}

// difference is how b's files differ from a's, as lines: paisans-stack.yaml
// as a unified diff, the secrets by key name only, never a value, and
// whether .sops.yaml differs. Each line is shown as ui.Visible shows it.
func difference(a, b configstore.Files, s sides) ([]string, error) {
	diff, err := unifiedDiff(a.Paisans, b.Paisans, s.dirA+config.FileName, s.dirB+config.FileName)
	if err != nil {
		return nil, err
	}
	var out []string
	if diff != "" {
		for _, line := range strings.Split(strings.TrimSuffix(diff, "\n"), "\n") {
			out = append(out, ui.Visible(line))
		}
	}
	out = append(out, secretsKeyChanges(a.Secrets, b.Secrets, s)...)
	if !bytesEqual(a.SOPS, b.SOPS) {
		out = append(out, ".sops.yaml differs")
	}
	return out, nil
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
	// git names an absolute path without its leading slash once the
	// prefixes are empty, and either form is replaced by its name.
	s := string(out)
	for _, n := range [][2]string{{pa, nameA}, {pb, nameB}, {strings.TrimPrefix(pa, "/"), nameA}, {strings.TrimPrefix(pb, "/"), nameB}} {
		s = strings.ReplaceAll(s, n[0], n[1])
	}
	return s, nil
}

// secretsKeyChanges names each key of the secrets file that is only on one
// side, as s names the sides, or whose stored value differs, never a value.
// The file is compared as stored, encrypted, and sops's own metadata is left
// out; a re-encrypted file differs at every key.
func secretsKeyChanges(local, theirs []byte, s sides) []string {
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
			out = append(out, "secrets: "+ui.Visible(k)+" only in "+s.inA)
		case !inA && inB:
			out = append(out, "secrets: "+ui.Visible(k)+" only in "+s.inB)
		case va != vb:
			out = append(out, "secrets: "+ui.Visible(k)+" differs")
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

func runDeploymentLog(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("deployment log", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, configHelp)
	root := fs.String("root", "", "judge from this root, as if pinned, without recording it")
	keyPath := fs.String("key", "", keyHelp)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return &ui.Problem{Hint: "deployment log takes no argument", Explain: "Got " + strings.Join(fs.Args(), " ") + "."}
	}
	// log judges from the system copy, and the gather adds what the sites
	// hold to its store; its files, head and records stay as they are.
	sys, d, _, err := systemFor(*configPath, "")
	if err != nil {
		return err
	}
	cfg, path, err := readConfig(sys, d)
	if err != nil {
		return err
	}
	a, err := anchorFor(sys, *root)
	if err != nil {
		return err
	}
	// log records nothing, so the key namedAnchor adds is only judged from.
	a, _, ignored, err := namedAnchor(a, *keyPath)
	if err != nil {
		return err
	}
	r := reporter()
	r.Section("versions of " + path)
	noteKeyIgnored(r, ignored)
	sites := configTransports(cfg)
	m := gatherMode{log: true, noAnchor: noAnchorNamed(", or run " + progname.Cmd() + " operator register to sign the version you hold")}
	g, err := gatherAt(r, sys, sites, cfg.ID, a, m, stdin, stdout)
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
		// A root's full hash is what --root takes.
		if len(v.Parents) == 0 {
			state += ", root " + sha
		}
		holders := strings.Join(g.Holders[sha], ", ")
		if holders == "" {
			holders = "no site that answered"
		}
		// A version that does not say which build published it is not
		// trusted, and its state says so.
		by := ""
		if v.BuildOK {
			by = "  by paisans " + v.Build.String()
		}
		fmt.Fprintf(stdout, "%d  %s  %s  %s%s  %s  held by %s\n", v.Revision, sha[:12], who, v.Committed.Format(time.RFC3339), by, state, holders)
	}
	if len(shas) == 0 {
		r.Result("no site holds a version yet")
	}
	return nil
}

func runDeploymentRecover(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("deployment recover", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, configHelp)
	var from pathList
	fs.Var(&from, "from", "a host of the deployment to recover from, user@host[:port] or an alias of your ssh config (repeatable)")
	root := fs.String("root", "", "pin this version as the root, the version trust starts from")
	keyPath := fs.String("key", "", keyHelp)
	id := fs.String("id", "", "the deployment's id, when the hosts' logins belong to more than one")
	execute := fs.Bool("execute", false, "write the recovered files and state")
	answers := yesFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return &ui.Problem{Hint: "deployment recover takes no argument", Explain: "Got " + strings.Join(fs.Args(), " ") + "."}
	}
	ans, err := answers()
	if err != nil {
		return err
	}
	if len(from) == 0 {
		return &ui.Problem{Hint: "deployment recover needs --from", Explain: "Name a host of the deployment with --from user@host[:port], or an alias of your ssh config, Eg: --from admin@203.0.113.9 or --from home-a; every site the recovered configuration names is reached after it."}
	}
	l, err := workstationOf(*configPath)
	if err != nil {
		return err
	}
	if l.HasFiles() {
		return &ui.Problem{Hint: "there are configuration files here already", Explain: "Recovery writes onto a workstation with nothing, so it never overwrites a file. Use " + progname.Cmd() + " deployment pull where the files exist, or recover into an empty directory with --config."}
	}
	a, err := anchorFor(l, *root)
	if err != nil {
		return err
	}
	a, held, ignored, err := namedAnchor(a, *keyPath)
	if err != nil {
		return err
	}
	r := reporter()
	r.Section("recover into " + l.Dir)
	noteKeyIgnored(r, ignored)
	var dests []config.Destination
	for _, f := range from {
		dest, err := parseDestination(f)
		if err != nil {
			return sshFlagProblem(f, err)
		}
		dests = append(dests, dest)
	}
	return recoverFiles(r, l, dests, a, *id, held, *execute, ans, stdin, stdout, nil)
}

// recoverFiles is a recovery onto l, a workstation without files, from
// dests, trusting from a: it finds the deployment (id when given), gathers
// from every site its newest versions name, and, with execute, writes the
// head's files and records the root and, from held, the signing key. accept,
// when set, runs on each head's configuration before anything is written,
// and a refusal from it is returned as it is; with accept, which only the
// join of operator register sets, a fork is refused with nothing written.
func recoverFiles(r ui.Reporter, l configstore.Local, dests []config.Destination, a configstore.Anchor, id string, held map[string]string, execute bool, ans answerer, stdin io.Reader, stdout io.Writer, accept func(head *config.Config) error) error {
	deploymentID, hosts, err := recoverID(r, dests, id)
	if err != nil {
		return err
	}
	noAnchor := noAnchorNamed("")
	g, err := gatherAt(r, l, hosts, deploymentID, a, gatherMode{execute: execute, answers: ans, noAnchor: noAnchor}, stdin, stdout)
	if err != nil {
		return err
	}
	if len(g.Verdict.Heads) == 0 {
		return &ui.Problem{Hint: "no host named holds a version", Explain: "Nothing was recovered. Name another host with --from.", Cause: configstore.UnreachedError(g)}
	}
	// Every site a trusted head names is reached, not only the hosts given,
	// so a host serving an old version is superseded by the newer versions the
	// rest hold. With a fork, every head's sites are reached.
	sites, err := recoverSites(l, g, deploymentID)
	if err != nil {
		return err
	}
	r.Section("every site the newest versions name")
	g, err = gatherAt(r, l, sites, deploymentID, a, gatherMode{execute: execute, accepted: g.Seen, answers: ans, noAnchor: noAnchor}, stdin, stdout)
	if err != nil {
		return err
	}
	if accept != nil {
		// A fork is said as one before any head is judged, since each
		// head's refusal would name only itself.
		if g.Verdict.Fork {
			return &ui.Problem{Hint: "the sites hold a fork", Explain: "Two trusted versions build on one parent:\n" + forkHeads(g) + "\nAn operator who holds the files resolves it with " + progname.Cmd() + " deployment publish --merge; run " + progname.Cmd() + " operator register --execute here again once they have. No configuration file was written."}
		}
		if err := acceptHeads(l, g, accept); err != nil {
			return err
		}
	}
	if g.Verdict.Fork {
		if !execute {
			return forkProblem(g, "--execute will write each head's files to "+l.Incoming()+"/<sha>/.")
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
	// A key alone cannot tell this deployment's history from an older
	// incarnation every answering site serves, so the root it leads to is
	// the operator's to confirm, by its full hash.
	if a.Named && pin != "" {
		r.Warn("confirm the root "+pin+" with another operator before applying", "--key trusted the history it leads to, and a key alone cannot tell this deployment's history from an older incarnation every site that answered serves. Compare it with their .paisans/config-root, or a root's line of their "+progname.Cmd()+" deployment log.")
	}
	if !execute {
		r.Step("write the files of "+describe(g, head)).End(ui.Pending, "--execute will write them")
		return nil
	}
	if err := l.Checkout(head); err != nil {
		return err
	}
	if err := recordPull(l, g, pin, held); err != nil {
		return err
	}
	if files, err := l.Files(); err == nil {
		key, _ := l.SigningKey()
		saveVersion(r, l, head, g.Versions[head].Revision, files, key, "recovered into "+l.Dir)
	}
	r.Result("recovered %s into %s", describe(g, head), l.Dir)
	return nil
}

// acceptHeads runs accept on the configuration of each of g's heads, from
// the workstation's store.
func acceptHeads(l configstore.Local, g configstore.Gathered, accept func(head *config.Config) error) error {
	cache, err := l.Cache()
	if err != nil {
		return err
	}
	for _, h := range g.Verdict.Heads {
		f, err := cache.Files(h)
		if err != nil {
			return err
		}
		cfg, err := config.LoadBytes(f.Paisans)
		if err != nil {
			return fmt.Errorf("version %s: %w", h, err)
		}
		if err := accept(cfg); err != nil {
			return err
		}
	}
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

// recoverID is the deployment a recovery is for, and each host reached as
// its user: id when given, else the one deployment the hosts' admission
// records under /etc/paisans name. A login with a record of its own names
// that record's deployment; any other login, the host's owner's, names every
// deployment the host admits, since an operator recovering a lost workstation
// knows how they reach the host and not the deployment's user, whose name
// comes from the id that was lost. More than one is refused, since which one
// to recover is the operator's to say. A record whose deployment is not an id
// never parses, so it names nothing here: the id becomes part of every
// command a recovery runs on every site.
//
// A host whose login is not the user its record of the deployment names is
// reached again as that user, at the same address and port: the store is
// that user's, and admit gave it the operators' keys. A host with no such
// record is kept as named, so the gather says what it holds.
func recoverID(r ui.Reporter, dests []config.Destination, id string) (string, map[string]apply.Transport, error) {
	if id != "" && !deployment.ValidID(id) {
		return "", nil, &ui.Problem{Hint: fmt.Sprintf("--id %q is not a deployment id", id), Explain: "Give the full id, as " + progname.Cmd() + " site deployments lists it."}
	}
	hosts := map[string]apply.Transport{}
	listed := map[string][]admission.Record{}
	ids := map[string]bool{}
	var errs []error
	var why []string
	slices.SortFunc(dests, func(a, b config.Destination) int { return strings.Compare(a.String(), b.String()) })
	dests = distinctLogins(dests)
	for _, dest := range dests {
		name := dest.String()
		hosts[name] = reachDestination(dest)
		login, recs, err := admission.Listed(hosts[name])
		if err == nil && len(recs) == 0 {
			err = fmt.Errorf("%s: %w for any deployment", admission.Dir, admission.ErrNotAdmitted)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			hint, _ := ui.Describe(err)
			why = append(why, fmt.Sprintf("- %s: %s", name, hint))
			continue
		}
		listed[name] = recs
		if i := slices.IndexFunc(recs, func(rec admission.Record) bool { return rec.User == login }); i >= 0 {
			recs = recs[i : i+1]
		}
		for _, rec := range recs {
			ids[rec.Deployment] = true
		}
	}
	if id == "" {
		switch len(ids) {
		case 0:
			return "", nil, &ui.Problem{Hint: "no host named holds an admission record", Explain: strings.Join(append(why, "Name another host of the deployment with --from."), "\n"), Cause: errors.Join(errs...)}
		case 1:
			for found := range ids {
				id = found
			}
		default:
			list := slices.Sorted(maps.Keys(ids))
			return "", nil, &ui.Problem{Hint: "the hosts admit more than one deployment", Explain: "Name the one to recover with --id: " + strings.Join(list, ", ") + "."}
		}
	}
	for _, dest := range dests {
		name := dest.String()
		i := slices.IndexFunc(listed[name], func(rec admission.Record) bool { return rec.Deployment == id })
		if i < 0 || listed[name][i].User == dest.User {
			continue
		}
		as := dest
		as.User = listed[name][i].User
		delete(hosts, name)
		hosts[as.String()] = reachDestination(as)
		r.Note(fmt.Sprintf("%s: reached as %s", name, as.User), "the deployment's user, as the host's admission record names it")
	}
	return id, hosts, nil
}
