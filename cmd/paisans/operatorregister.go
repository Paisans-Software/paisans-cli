package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
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
	if len(args) > 0 && args[0] == "register" {
		return runOperatorRegister(args[1:], stdin, stdout)
	}
	if len(args) > 0 && args[0] == "magic-link" {
		return runOperatorMagicLink(args[1:])
	}
	if len(args) > 0 && args[0] == "grants" {
		return runOperatorGrants(args[1:])
	}
	if len(args) == 0 {
		return &ui.Problem{Hint: "operator takes one subcommand", Explain: "Use register, magic-link or grants."}
	}
	return &ui.Problem{Hint: fmt.Sprintf("operator has no subcommand %q", args[0]), Explain: "Use register, magic-link or grants."}
}

func runOperatorRegister(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("operator register", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, configHelp)
	keyPath := fs.String("key", "", "the public key file (.pub) to sign with; the ssh-agent must hold it")
	root := fs.String("root", "", "pin this version as the root, the version trust starts from")
	execute := fs.Bool("execute", false, "sign the attestation, push it to the sites, and record the key")
	answers := yesFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return &ui.Problem{Hint: "operator register takes no argument", Explain: "Got " + strings.Join(fs.Args(), " ") + "."}
	}
	ans, err := answers()
	if err != nil {
		return err
	}
	l, err := workstationOf(*configPath)
	if err != nil {
		return err
	}
	a, err := anchorFor(l, *root)
	if err != nil {
		return err
	}
	pin := a.Root
	cfg, err := registerConfig(l, *configPath, pin)
	if err != nil {
		return err
	}
	key, err := registerKey(l, cfg, *keyPath, stdout)
	if err != nil {
		return err
	}
	k, _, _ := config.ParseKeyLine(key)
	r := reporter()
	r.Section("register " + k.Fingerprint + " for " + *configPath)

	// Trust starts from the pin, and from any version this key signed
	// before; with neither, the sites' history alone is never trusted.
	anchor := configstore.Anchor{Root: pin, Signers: []string{k.Fingerprint}}
	sites := configTransports(cfg)
	m := gatherMode{
		execute:  *execute,
		answers:  ans,
		noAnchor: "Pin the first version you trust with --root <sha>; an operator who has one can read its hash from " + progname.Name() + " deployment log.",
	}
	g, err := gatherAt(r, l, sites, len(sites), cfg.ID, anchor, m, stdin, stdout)
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
			Hint:    "the local files differ from the sites' newest version",
			Explain: fmt.Sprintf("The sites hold %s. An attestation says you hold that version's files, so it signs only a version equal to yours. Bring your files to it, Eg: with "+progname.Name()+" deployment pull, or check the difference above with the operator who signed it, then run this again. Nothing was written.", describe(g, de.Head)),
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

	if !*execute {
		if own {
			r.Step("attest "+describe(g, head)).End(ui.Pending, "your key signed it already")
		} else {
			r.Step("attest "+describe(g, head)).End(ui.Pending, fmt.Sprintf("revision %d, signed with %s", g.Versions[head].Revision+1, k.Fingerprint))
		}
		if write {
			r.Step("write the files of "+describe(g, head)).End(ui.Pending, "--execute will write them")
		}
		if !g.Majority {
			r.Warn(fmt.Sprintf("only %d of %d sites answered directly", len(g.Direct()), len(sites)), "--execute will ask for the word "+partialWord+" at a terminal.")
		}
		return nil
	}
	// The attestation becomes this workstation's anchor, so it is made only
	// from a majority, or once the operator says partial.
	if err := confirmPartial(ans, stdin, stdout, g, len(sites)); err != nil {
		return err
	}
	s := r.Step("sign and push the attestation, signed with " + k.Fingerprint)
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
		return err
	default:
		s.Done(sha[:12])
	}
	r.Result("registered %s; version %s is your anchor", k.Fingerprint, sha)
	return nil
}

// registerConfig is the configuration register reads the operators and the
// sites from: the local paisans-stack.yaml, or, on a workstation without the files
// and with a root pinned, the version the pin leads to in the workstation's
// own store, which a recovery's dry run fills.
func registerConfig(l configstore.Local, path, pin string) (*config.Config, error) {
	if l.HasFiles() {
		return config.Load(path)
	}
	if pin == "" {
		return nil, &ui.Problem{Hint: "there are no configuration files here", Explain: "operator register attests the version you hold, and reads the sites from " + config.FileName + ". Run it beside your files with --config, or pin the first version you trust with --root <sha> to attest the version it leads to and write its files."}
	}
	g, err := configstore.GatherAnchored(l, nil, 0, "", configstore.Anchor{Root: pin}, configstore.GatherOptions{})
	var rb *configstore.RollbackError
	if errors.As(err, &rb) {
		err = nil
	}
	if err != nil || len(g.Verdict.Heads) == 0 {
		return nil, &ui.Problem{Hint: "the pinned root is not in this workstation's store", Explain: "With no files here, the sites are read from the version the pin leads to, which this workstation must have gathered before. Run " + progname.Name() + " deployment recover --from <user@host> --root " + pin + ", which finds them from a host.", Cause: err}
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
// names, else the one .paisans/signing-key records, else the one key in the
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
