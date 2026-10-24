package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/create"
	"github.com/paisans-software/paisans-cli/internal/operatorinvite"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// What follows is `paisans operator invite` (docs/specs/2026-10-24-operator-invite.md,
// *The invite*): an operator the deployment lists writes a signed invite for
// a new operator, who answers it with operator register --invite. It runs
// beside the deployment's files and reaches no host.

func runOperatorInvite(args []string, stdout io.Writer) error {
	fs := newFlagSet("operator invite", flag.ExitOnError)
	commonFlags(fs)
	configPath := fs.String("config", config.FileName, configHelp)
	keyArg := fs.String("key", "", "the new operator's public key: a .pub file, or the key line itself")
	pos, err := parseAll(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return &ui.Problem{Hint: "operator invite takes one username", Explain: "Name the operator the invitee will be, Eg: " + progname.Cmd() + " operator invite carol --key carol.pub."}
	}
	username := pos[0]
	if *keyArg == "" {
		return &ui.Problem{Hint: "operator invite needs --key", Explain: "Give the new operator's public key: their .pub file, or one key line."}
	}
	lines, err := keyLines(*keyArg)
	if err != nil {
		return err
	}
	if len(lines) != 1 {
		return &ui.Problem{Hint: "--key must give one key", Explain: fmt.Sprintf("Got %d. An invite is for one key.", len(lines))}
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	// The checks operator add makes of a new operator's name and key. The
	// fields an invite does not carry are filled with values they accept.
	declared := create.Declared{Operators: cfg.Operators, Sites: cfg.SiteNames()}
	if err := create.CheckNewOperator(create.NewOperator{
		ID:    username,
		Name:  "Invited Operator",
		Email: "invited@example.org",
		Keys:  lines,
		Sites: declared.Sites[:min(1, len(declared.Sites))],
	}, declared); err != nil {
		return flagProblem(err)
	}
	// CheckNewOperator proved the line is a key: type, base64, then a comment.
	fields := strings.Fields(lines[0])
	key := fields[0] + " " + fields[1]

	l, err := workstationOf(*configPath)
	if err != nil {
		return err
	}
	signing, inviter, err := inviterOf(l, cfg)
	if err != nil {
		return err
	}
	root, err := trustedRoot(l, cfg.ID)
	if err != nil {
		return err
	}

	inv := operatorinvite.Invite{
		Version:    operatorinvite.Version,
		Deployment: cfg.ID,
		Domain:     cfg.Community.Domain,
		Root:       root,
		Inviter:    inviter,
		Username:   username,
		Key:        key,
	}
	for _, name := range cfg.SiteNames() {
		s := cfg.Sites[name]
		inv.Sites = append(inv.Sites, operatorinvite.Site{Name: name, Host: s.SSHHost(), User: s.SSHUser(), Port: s.SSH.PortOrDefault()})
	}
	if problems := inv.Check(); len(problems) > 0 {
		return &ui.Problem{Hint: "the invite would not be valid", Explain: strings.Join(problems, "; ") + ". Nothing was written."}
	}
	body := operatorinvite.EncodeInvite(inv)
	sig, err := operatorinvite.Sign(signing, operatorinvite.InviteNamespace, body)
	if err != nil {
		return &ui.Problem{Hint: "the invite could not be signed", Explain: "The signature is made through your ssh-agent, which must hold " + inviter.Fingerprint + ".", Cause: err}
	}
	_, err = stdout.Write(operatorinvite.Signed(body, sig))
	return err
}

// inviterOf is the key the invite is signed with, the one .paisans/signing-key
// records and the agent holds, and the operator cfg lists it under.
func inviterOf(l configstore.Local, cfg *config.Config) (string, operatorinvite.Inviter, error) {
	noKey := &ui.Problem{Hint: "this workstation records no signing key", Explain: "An invite is signed with the key of an operator the deployment lists, which " + progname.Cmd() + " operator register records. Run it first."}
	recorded, err := l.SigningKey()
	if err != nil {
		return "", operatorinvite.Inviter{}, err
	}
	if recorded == "" {
		return "", operatorinvite.Inviter{}, noKey
	}
	line, err := recordedKey(l, agentKeys(), "")
	if err != nil {
		return "", operatorinvite.Inviter{}, err
	}
	k, _, _ := config.ParseKeyLine(line)
	for _, id := range slices.Sorted(maps.Keys(cfg.Operators)) {
		for _, al := range cfg.Operators[id].KeyLines() {
			if a, _, err := config.ParseKeyLine(al); err == nil && a.Fingerprint == k.Fingerprint {
				return line, operatorinvite.Inviter{Name: id, Fingerprint: k.Fingerprint}, nil
			}
		}
	}
	return "", operatorinvite.Inviter{}, &ui.Problem{Hint: "no operator lists the signing key", Explain: fmt.Sprintf("%s is recorded in .paisans/signing-key but %s lists it under no operator. Run %s operator register with a key an operator lists.", k.Fingerprint, config.FileName, progname.Cmd())}
}

// trustedRoot is the root this workstation trusts: the pin in
// .paisans/config-root, else the one root of the newest trusted version
// among those it has gathered. It reads no site and writes nothing: the
// store is opened as it is on disk, and a workstation without one knows no
// root.
func trustedRoot(l configstore.Local, id string) (string, error) {
	a, err := l.Anchor()
	if err != nil {
		return "", err
	}
	if a.Root != "" {
		return a.Root, nil
	}
	cache, ok, err := l.HeldStore()
	if err != nil {
		return "", err
	}
	if !ok {
		return "", unknownRoot(nil)
	}
	g, err := configstore.JudgeHeld(l, cache, id, a)
	// A head older than one this workstation has seen still has the root
	// it had: a rollback changes which version is newest, never where the
	// history starts, so the root is read from the judged heads either way.
	var rb *configstore.RollbackError
	if err != nil && !errors.As(err, &rb) {
		return "", unknownRoot(err)
	}
	if len(g.Verdict.Heads) == 1 {
		if roots := g.Roots(g.Verdict.Heads[0]); len(roots) == 1 {
			return roots[0], nil
		}
	}
	return "", unknownRoot(nil)
}

func unknownRoot(cause error) error {
	return &ui.Problem{
		Hint:    "this workstation does not know the deployment's root",
		Explain: "The invite names the root of the history this workstation trusts, and there is no .paisans/config-root and no single root among the versions it has gathered. Run " + progname.Cmd() + " deployment pull, which gathers them and records the root.",
		Cause:   cause,
	}
}
