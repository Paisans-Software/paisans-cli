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

func runOperatorRegister(args []string, stdin io.Reader, stdout io.Writer) error {
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
	l, err := workstationOf(*configPath)
	if err != nil {
		return err
	}
	// Where the files are not there and an answer left an invite pending,
	// register joins: the invite names the root and the key. --key or --root
	// without --id is a plain register, which a pending invite does not
	// take over.
	if l.HasFiles() && *id != "" {
		return &ui.Problem{Hint: "--id selects a pending invite to join, and " + ui.ShortPath(l.Dir) + " holds the files", Explain: "A join writes the files into a directory without them. Here register registers, and reads the deployment from " + config.FileName + "; run it without --id."}
	}
	if !l.HasFiles() && (*id != "" || (*keyPath == "" && *root == "")) {
		invite, ok, err := selectPending(*id)
		if err != nil {
			return err
		}
		if ok {
			if *keyPath != "" || *root != "" {
				return &ui.Problem{Hint: "--key and --root do not apply to a join", Explain: "The pending invite names the key you sign with and the root trust starts from."}
			}
			return joinInvite(reporter(), l, invite, *execute, ans, stdin, stdout)
		}
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
	return attest(r, l, cfg, key, pin, *execute, ans, stdin, stdout)
}

// attest signs, with execute, an attestation of the sites' newest version
// with key, once it equals the files l holds, pushes it to cfg's sites, and
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
			Hint:    "the local files differ from the sites' newest version",
			Explain: fmt.Sprintf("The sites hold %s. An attestation says you hold that version's files, so it signs only a version equal to yours. Bring your files to it, Eg: with %s deployment pull, or check the difference above with the operator who signed it, then run this again. Nothing was written.", describe(g, de.Head), progname.Cmd()),
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
