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
	"github.com/paisans-software/paisans-cli/internal/draft"
	"github.com/paisans-software/paisans-cli/internal/grant"
	"github.com/paisans-software/paisans-cli/internal/localstore"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"gopkg.in/yaml.v3"
)

// What follows is `paisans deployment`: the signed copies of the configuration
// every site keeps (docs/specs/2026-10-12-config-store.md). publish puts a
// draft on the sites as a new version, pull brings the sites' newest trusted
// version into the system copy and merges it into the draft, log lists every
// version, and recover makes the system copy on a workstation that has
// none (docs/specs/2026-10-24-system-copy.md). Every decision about which
// version to trust is made here, on the workstation, from the system copy's
// anchor; the sites only store, and a draft's folder holds only the draft.

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
		defer openRunLog("deployment apply", args[1:])()
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
const configHelp = "path to the draft's deployment declaration; what trust is read from is kept in the system copy, never beside it"

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
	if err := rootShape(root); err != nil {
		return a, err
	}
	if a.Root != "" && a.Root != root {
		return a, &ui.Problem{Hint: "--root differs from the pinned root", Explain: fmt.Sprintf("The system copy pinned %s. Drop --root, or, if that pin is wrong, make the system copy again from the root you trust with %s deployment recover --from <user@host> --root %s --config <empty directory>/%s --execute, which replaces it and keeps the old one under replaced/ of the data directory.", a.Root, progname.Cmd(), root, config.FileName)}
	}
	a.Root = root
	return a, nil
}

// rootShape refuses a --root that is not a full commit id.
func rootShape(root string) error {
	if root != "" && !shaShape(root) {
		return &ui.Problem{Hint: fmt.Sprintf("--root %q is not a commit id", root), Explain: "Give the full hash of the root to pin, as " + progname.Cmd() + " deployment log prints it on a root's line."}
	}
	return nil
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
const keyHelp = "a public key file (.pub) you trust, to start trust from when the system copy records no signing key; recorded as its signing key when your ssh-agent holds it"

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
	if g.ArrivalErr != nil {
		r.Warn("the arrival of these versions could not be recorded", errLine(g.ArrivalErr)+". Apply cannot warn about a draft saved before the newest revision arrived.")
	}
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
			return "", &ui.Problem{Hint: "the base version is not in the system copy's store", Explain: "Run " + progname.Cmd() + " deployment pull, which gathers it again.", Cause: err}
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
	breakLease := breakLeaseFlag(fs)
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
	// publish holds the deployment's lease while it gathers and pushes, so
	// it never publishes beside a running apply. See internal/lease.
	if *execute {
		release, err := takeLease(r, cfg, cfg.SiteNames(), func(name string) apply.Transport { return sites[name] }, "deployment publish", leaseOptions{breakLease: *breakLease, answers: ans, stdin: stdin, stdout: stdout})
		if err != nil {
			return err
		}
		defer release()
	}
	t, err := gatherDeploy(r, d, cfg, sites, raw.Paisans, gatherMode{execute: *execute, answers: ans}, stdin, stdout)
	defer t.done()
	if err != nil {
		return err
	}
	g := t.g
	t.again, t.source = "deployment publish --execute", "published from "+d.Dir
	if heads := g.Verdict.Heads; !*merge && len(heads) == 1 && t.base != "" && heads[0] != t.base && unchangedSince(t.sys, g, t.base, heads[0]) {
		// Versions that only sign the base's files again, as an
		// attestation does, leave nothing to merge: the draft goes on
		// from the sites' newest, and its publish rewrites the line.
		t.base = heads[0]
	}
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
	err = t.deploy(r, s, files, func(sys configstore.Local, r ui.Reporter, s ui.Step) (string, int, error) {
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
	id := fs.String("id", "", "the deployment, in a folder with no draft, when this workstation holds more than one")
	root := fs.String("root", "", "pin this version as the root, the version trust starts from")
	keyPath := fs.String("key", "", keyHelp)
	execute := fs.Bool("execute", false, "move the system copy to the sites' newest version and merge it into the draft")
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
	sys, d, deploymentID, err := systemFor(*configPath, *id)
	if err != nil {
		return err
	}
	if !d.Has() {
		if held := draftFiles(d); len(held) > 0 {
			return &ui.Problem{Hint: "there is part of a draft in " + d.Dir, Explain: "It holds " + strings.Join(held, ", ") + " and no " + config.FileName + ", and starting a draft would replace them. Move them away, or use another folder."}
		}
	}
	cfg, _, err := readConfig(sys, d)
	if err != nil {
		return err
	}
	// The system copy's head before the gather: the base of a draft with
	// no usable "drafted from" line, and what moving the system copy
	// checks nothing else wrote since.
	before, err := sys.Head()
	if err != nil {
		return err
	}
	a, err := anchorFor(sys, *root)
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
	g, err := gatherAt(r, sys, sites, deploymentID, a, m, stdin, stdout)
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
		r.Result("the sites hold no version yet; %s deployment apply makes the first", progname.Cmd())
		return nil
	}
	head := g.Verdict.Heads[0]
	record := func(l configstore.Local) error { return recordPull(l, g, *root, held) }
	const pulled = "pulled by deployment pull"
	if !d.Has() {
		what := "start a draft of " + describe(g, head)
		if !*execute {
			r.Step(what).End(ui.Pending, "--execute will write it in "+d.Dir)
			return nil
		}
		if err := moveSystemCopy(r, sys, deploymentID, before, head, pulled, record); err != nil {
			return err
		}
		if err := startDraft(sys, d); err != nil {
			return err
		}
		r.Result("started a draft of %s in %s", describe(g, head), d.Dir)
		return nil
	}
	paisans, err := os.ReadFile(filepath.Join(d.Dir, config.FileName))
	if err != nil {
		return err
	}
	base := draftBase(r, sys, g, paisans, before)
	if base == "" {
		return &ui.Problem{Hint: "the system copy of " + deploymentID + " holds no version", Explain: "There is no version the draft in " + d.Dir + " was drafted from to merge the sites' newest into. " + recoverHint(deploymentID)}
	}
	if base == head {
		if *execute {
			if err := moveSystemCopy(r, sys, deploymentID, before, head, pulled, record); err != nil {
				return err
			}
		}
		r.Result("the draft is drafted from %s, the sites' newest", describe(g, head))
		return nil
	}
	// Versions that only sign the base's files again, as an attestation
	// does, leave nothing to merge: the draft is up to date, and only its
	// "drafted from" line moves to the sites' newest.
	if unchangedSince(sys, g, base, head) {
		what := `rewrite the draft's "drafted from" line to name ` + describe(g, head)
		if !*execute {
			r.Step(what).End(ui.Pending, "--execute will write it; the sites' newest has the draft's base's files")
			return nil
		}
		if err := moveSystemCopy(r, sys, deploymentID, before, head, pulled, record); err != nil {
			return err
		}
		s := r.Step(what)
		if err := d.Mark(draft.Marker{Revision: g.Versions[head].Revision, SHA: head}); err != nil {
			s.Fail(err)
			return err
		}
		s.Done("the files are the base's")
		r.Result("the draft is up to date with %s, the sites' newest", describe(g, head))
		return nil
	}
	merge, err := mergeForward(r, sys, g, d, deploymentID, base, head, before, pulled, *execute, record)
	if err != nil {
		return err
	}
	rev := merge.marker.Revision
	switch {
	case len(merge.conflicts) > 0 && *execute:
		return &ui.Problem{Hint: fmt.Sprintf("merged revision %d into this draft, with conflicts", rev), Explain: conflictsExplain(merge)}
	case len(merge.conflicts) > 0:
		return &ui.Problem{Hint: fmt.Sprintf("merging revision %d into this draft leaves conflicts", rev), Explain: conflictsExplain(merge) + " --execute writes the merge. Nothing was written."}
	case *execute:
		r.Result("merged %s into this draft; review it, then deploy it with %s deployment apply --execute", describe(g, head), progname.Cmd())
	}
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

// showDifference prints how the system copy's files differ from the head's:
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

// localAndHead is how register names the sides: the system copy's files,
// then the head's.
var localAndHead = sides{dirA: "system/", dirB: "head/", inA: "the system copy", inB: "the head"}

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
	out = append(out, secretsKeyChanges(a, b, s)...)
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
// side, as s names the sides, or whose value differs, never a value. The
// file is compared as stored, encrypted, and sops's own metadata is left
// out. Where this workstation can decrypt both sides, a key whose stored
// value differs is compared decrypted, so a file re-encrypted to other
// recipients differs at no key whose value it kept.
func secretsKeyChanges(local, theirs configstore.Files, s sides) []string {
	a, errA := flatKeys(local.Secrets)
	b, errB := flatKeys(theirs.Secrets)
	if errA != nil || errB != nil {
		if !bytesEqual(local.Secrets, theirs.Secrets) {
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
	var pa, pb map[string]string
	for _, k := range names {
		if va, vb := a[k], b[k]; va != vb && pa == nil {
			pa, pb = plainKeys(local), plainKeys(theirs)
		}
	}
	for _, k := range names {
		va, inA := a[k]
		vb, inB := b[k]
		if pa != nil && pb != nil && inA && inB && va != vb {
			va, vb = pa[k], pb[k]
		}
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

// plainKeys is f's secrets decrypted, by dotted key as flatKeys names them,
// or nil when this workstation cannot decrypt them. The deployment id that
// finds the key is f's paisans-stack.yaml's.
func plainKeys(f configstore.Files) map[string]string {
	var doc struct {
		ID string `yaml:"id"`
	}
	if err := yaml.Unmarshal(draft.StripMarker(f.Paisans), &doc); err != nil || doc.ID == "" {
		return nil
	}
	secrets, err := config.DecryptSecretsBytes(f.Secrets, doc.ID)
	if err != nil {
		return nil
	}
	data, err := yaml.Marshal(secrets)
	if err != nil {
		return nil
	}
	out, err := flatKeys(data)
	if err != nil {
		return nil
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
	sys, _, deploymentID, err := systemFor(*configPath, "")
	if err != nil {
		return err
	}
	// The sites gathered from are the system copy's files', never a
	// draft's: what log answers is about trust.
	cfg, _, err := readConfig(sys, draft.Draft{Dir: sys.Dir})
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
	r.Section("versions of " + deploymentID)
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
		return nil
	}
	head := ""
	if len(g.Verdict.Heads) == 1 {
		head = g.Verdict.Heads[0]
	}
	r.Section("what each site applied")
	for _, line := range appliedBySite(cfg, sites, head) {
		fmt.Fprintln(stdout, line)
	}
	return nil
}

// appliedBySite is a line per site, in name order, saying which version the
// site last finished applying, from its grant record, and what its pending
// record says a stopped apply still owes it. apply publishes a version
// before it changes any host, so that every site's store holds what is
// applied even when a run stops part way; the head every site holds is
// therefore not by itself what runs, and this is what says so. head is the
// one trusted head, or empty for none or a fork.
func appliedBySite(cfg *config.Config, sites map[string]apply.Transport, head string) []string {
	names := slices.Sorted(maps.Keys(sites))
	lines := make([]string, len(names))
	d := cfg.Deployment()
	atOnce(len(names), func(i int) {
		name := names[i]
		t := sites[name]
		rec, _, _, err := grant.Read(t, d)
		if err != nil {
			lines[i] = name + "  not read: " + errLine(err)
			return
		}
		owed, err := apply.ReadOwed(t, d)
		if err != nil {
			lines[i] = name + "  not read: " + errLine(err)
			return
		}
		line := name + "  no apply has finished here"
		if c := rec.Config; c != nil {
			where := "behind the head"
			switch {
			case c.Hash == head:
				where = "the head"
			case head == "":
				where = "no single head to compare with"
			}
			line = fmt.Sprintf("%s  applied revision %d, %s, %s", name, c.Revision, shortSHA(c.Hash), where)
		}
		if !owed.None() {
			line += "; still owes " + owed.String()
		}
		lines[i] = line
	})
	return lines
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
	execute := fs.Bool("execute", false, "make the system copy from the sites and start a draft of it")
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
	d, err := draftAt(*configPath)
	if err != nil {
		return err
	}
	// A recovery starts from nothing recorded: the root it pins, or the key
	// it names.
	if err := rootShape(*root); err != nil {
		return err
	}
	a, held, ignored, err := namedAnchor(configstore.Anchor{Root: *root}, *keyPath)
	if err != nil {
		return err
	}
	r := reporter()
	r.Section("recover into " + d.Dir)
	noteKeyIgnored(r, ignored)
	var dests []config.Destination
	for _, f := range from {
		dest, err := parseDestination(f)
		if err != nil {
			return sshFlagProblem(f, err)
		}
		dests = append(dests, dest)
	}
	_, _, done, err := recoverFiles(r, d, dests, a, *id, held, *execute, ans, stdin, stdout, recovery{source: "recovered into " + d.Dir, root: *root})
	return done(err)
}

// recoverFiles is a recovery, from dests and trusting from a, of a
// deployment this workstation holds no system copy of, into d, a folder
// with no draft: it finds the deployment (id when given), gathers from
// every site its newest versions name, and, with execute, makes the system
// copy the head's, recording there the root and, from held, the signing
// key. how.accept, when set, runs on each head's configuration before
// anything is recorded, and a refusal from it is returned as it is; with
// accept, which only the join of operator register sets, a fork is said as
// one before any head is judged.
//
// The run holds the system copy's lock and works in a directory of its own
// (stageSystem), sys, reporting through staged, which names the system
// copy's place for that directory; done ends it as stageSystem's done does, and, once
// the system copy is in place, runs how.promoted, records how.source as
// where it came from and starts a draft of its head in d. done is never nil, and the caller
// returns what it returns.
func recoverFiles(r ui.Reporter, d draft.Draft, dests []config.Destination, a configstore.Anchor, id string, held map[string]string, execute bool, ans answerer, stdin io.Reader, stdout io.Writer, how recovery) (sys configstore.Local, staged ui.Reporter, done func(runErr error) error, err error) {
	accept := how.accept
	done = func(runErr error) error { return runErr }
	if files := draftFiles(d); len(files) > 0 {
		return sys, r, done, &ui.Problem{Hint: "there is a draft in " + d.Dir + " already", Explain: "It holds " + strings.Join(files, ", ") + ". A recovery starts a draft only in a folder without one, so it never overwrites a file. Use " + progname.Cmd() + " deployment pull where the draft is, or name an empty directory with --config."}
	}
	deploymentID, hosts, err := recoverID(r, dests, id)
	if err != nil {
		return sys, r, done, err
	}
	st, err := openStore()
	if err != nil {
		return sys, r, done, err
	}
	release, err := st.Lock(deploymentID)
	if err != nil {
		return sys, r, done, err
	}
	why, err := heldAlready(st, deploymentID, how.root)
	if err != nil {
		release()
		return sys, r, done, err
	}
	outside := r
	sys, staged, end, err := stageSystem(r, st, "recover", deploymentID, why, nil, release, func(sys configstore.Local) error {
		if how.promoted != nil {
			how.promoted()
		}
		m, err := startRecovered(outside, sys, d, how.source)
		if err == nil && accept == nil {
			outside.Result("recovered revision %d, %s, into %s, and started a draft of it in %s", m.Revision, shortSHA(m.SHA), sys.Dir, d.Dir)
		}
		return err
	})
	if err != nil {
		return sys, r, done, err
	}
	done, r = end, staged
	noAnchor := noAnchorNamed("")
	g, err := gatherAt(r, sys, hosts, deploymentID, a, gatherMode{execute: execute, answers: ans, noAnchor: noAnchor}, stdin, stdout)
	if err != nil {
		return sys, r, done, err
	}
	if len(g.Verdict.Heads) == 0 {
		return sys, r, done, &ui.Problem{Hint: "no host named holds a version", Explain: "Nothing was recovered. Name another host with --from.", Cause: configstore.UnreachedError(g)}
	}
	// Every site a trusted head names is reached, not only the hosts given,
	// so a host serving an old version is superseded by the newer versions the
	// rest hold. With a fork, every head's sites are reached.
	sites, err := recoverSites(sys, g, deploymentID)
	if err != nil {
		return sys, r, done, err
	}
	r.Section("every site the newest versions name")
	g, err = gatherAt(r, sys, sites, deploymentID, a, gatherMode{execute: execute, accepted: g.Seen, answers: ans, noAnchor: noAnchor}, stdin, stdout)
	if err != nil {
		return sys, r, done, err
	}
	if accept != nil {
		// A fork is said as one before any head is judged, since each
		// head's refusal would name only itself.
		if g.Verdict.Fork {
			return sys, r, done, &ui.Problem{Hint: "the sites hold a fork", Explain: "Two trusted versions build on one parent:\n" + forkHeads(g) + "\nAn operator who holds the files resolves it with " + progname.Cmd() + " deployment publish --merge; run " + progname.Cmd() + " operator register --execute here again once they have. No configuration file was written."}
		}
		if err := acceptHeads(sys, g, accept); err != nil {
			return sys, r, done, err
		}
	}
	// A fork has no one version to make the system copy from, and the
	// versions gathered go with the directory the run worked in.
	if g.Verdict.Fork {
		return sys, r, done, &ui.Problem{Hint: "the sites hold a fork", Explain: "Two trusted versions build on one parent:\n" + forkHeads(g) + "\nAn operator who holds the files merges them into one version with " + progname.Cmd() + " deployment publish --merge; once they have, run this recovery again. Nothing was written."}
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
		r.Warn("confirm the root "+pin+" with another operator before applying", "--key trusted the history it leads to, and a key alone cannot tell this deployment's history from an older incarnation every site that answered serves. Compare it with a root's line of their "+progname.Cmd()+" deployment log.")
	}
	if !execute {
		r.Step("make the system copy from "+describe(g, head)+" and start a draft of it in "+d.Dir).End(ui.Pending, "--execute will write them")
		return sys, r, done, nil
	}
	// The head is recorded last, by the checkout: a run that stops before
	// it leaves no system copy.
	if err := recordPull(sys, g, pin, held); err != nil {
		return sys, r, done, err
	}
	if err := sys.Checkout(head); err != nil {
		return sys, r, done, err
	}
	return sys, r, done, nil
}

// recovery is how recoverFiles goes beyond a plain recovery.
type recovery struct {
	// accept, when set, judges each head's configuration (the join's).
	accept func(head *config.Config) error
	// source is where the system copy came from, as deployment list shows.
	source string
	// root is the root --root pinned, "" when none was given.
	root string
	// promoted, when set, runs once the system copy is in place.
	promoted func()
}

// heldAlready refuses a recovery or a join of deployment id when this
// workstation holds its system copy, judged from the copy's own state: they
// make one where there is none. A copy with no head holds no version, only
// a store a fetch set aside, and the recovery takes its place. A copy with
// a head is taken over only by a --root, root, that differs from the root
// it pinned, as after the deployment was made again under the same id. why
// says, for the warning that names what is moved aside, why the old copy
// is not kept in place.
func heldAlready(st localstore.Store, id, root string) (why string, err error) {
	sys, ok, err := st.Held(id)
	if err != nil {
		return "", err
	}
	if !ok {
		// What a directory that is no copy holds, a record alone or what a
		// run left, is moved aside only when there is something there.
		return "it held no system copy, only what a run that made none left.", nil
	}
	head, err := sys.Head()
	if err != nil {
		return "", err
	}
	if head == "" {
		return "it held no version, only what a fetch or a run set aside, so there was no system copy to keep.", nil
	}
	pinned, err := sys.Root()
	if err != nil {
		return "", err
	}
	if root != "" && root != pinned {
		if pinned == "" {
			return "it pinned no root, and --root names " + root + ", so the history it trusted is not known to be the one recovered.", nil
		}
		return "its pinned root " + pinned + " is not the root --root names, " + root + ", so the history it trusted is not the one recovered.", nil
	}
	again := "Start a draft from it with " + progname.Cmd() + " deployment checkout --id " + id + " --config <empty directory>/" + config.FileName + " --execute, and bring a draft to the sites' newest version with " + progname.Cmd() + " deployment pull."
	if root != "" {
		again += " --root replaces it only when it names another root than the one it pinned."
	}
	return "", &ui.Problem{Hint: "this workstation holds the system copy of " + id + " already", Explain: "A recovery or a join makes a system copy where there is none; it is in " + sys.Dir + ", at " + shortSHA(head) + ". " + again}
}

// startRecovered ends a recovery once its system copy, sys, is in place:
// it records source as where the system copy came from, which deployment
// list shows, and starts a draft of its head in d, which returns.
func startRecovered(r ui.Reporter, sys configstore.Local, d draft.Draft, source string) (draft.Marker, error) {
	m, f, err := systemHead(sys)
	if err != nil {
		return m, err
	}
	recordHead(r, sys, source)
	s := r.Step("start a draft in " + d.Dir)
	id := filepath.Base(sys.Dir)
	checkout := "The system copy is made; start a draft from it in an empty folder with " + progname.Cmd() + " deployment checkout --id " + id + " --config <empty directory>/" + config.FileName + " --execute."
	if files := draftFiles(d); len(files) > 0 {
		err := &ui.Problem{Hint: "no draft was started in " + d.Dir, Explain: "It holds " + strings.Join(files, ", ") + ", written while this run went on. " + checkout}
		s.Fail(err)
		return m, err
	}
	if err := d.Write(f, m); err != nil {
		s.Fail(err)
		return m, &ui.Problem{Hint: "no draft was started in " + d.Dir, Explain: errLine(err) + ". " + checkout, Cause: err}
	}
	s.Done(fmt.Sprintf("drafted from revision %d, %s", m.Revision, shortSHA(m.SHA)))
	return m, nil
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
