package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/create"
	"github.com/paisans-software/paisans-cli/internal/operatorinvite"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// What follows is `paisans operator add`: declare one more operator in an
// existing paisans-stack.yaml, the entry under operators and their name in
// the ssh.keys of the sites they may log in to. At a terminal it asks for
// whatever its flags did not give, with create's questions; without one it
// takes the flags alone, and refuses when one is missing rather than guess.
// It writes the one local file, once validate refuses nothing it did not
// refuse already, and reaches no host: apply does that.

func runOperatorAdd(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("operator add", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, configHelp)
	name := fs.String("name", "", "the operator's full name, first and last")
	email := fs.String("email", "", "the operator's email address")
	var keys pathList
	var sites nameList
	fs.Var(&keys, "key", "a public key file (.pub), or one key line itself (repeatable)")
	fs.Var(&sites, "site", "a site the operator may log in to (repeatable, or comma-separated)")
	allSites := fs.Bool("all-sites", false, "let the operator log in to every site")
	request := fs.String("request", "", "accept this signed request, which answers an invite and gives the username, name, email and keys")
	pos, err := parseAll(fs, args)
	if err != nil {
		return err
	}
	if *request != "" {
		var given []string
		if len(pos) > 0 {
			given = append(given, "the username")
		}
		for _, f := range []struct {
			name string
			set  bool
		}{{"--name", *name != ""}, {"--email", *email != ""}, {"--key", len(keys) > 0}} {
			if f.set {
				given = append(given, f.name)
			}
		}
		if len(given) > 0 {
			return &ui.Problem{Hint: strings.Join(given, ", ") + " cannot be given beside --request", Explain: "The request gives the username, name, email and key. Nothing was written."}
		}
		if *allSites && len(sites) > 0 {
			return &ui.Problem{Hint: "--all-sites and --site both name the sites", Explain: "Give one of them."}
		}
		return runOperatorAddRequest(reporter(), *configPath, *request, sites, *allSites, stdin, stdout)
	}
	if len(pos) > 1 {
		return &ui.Problem{Hint: "operator add takes one username", Explain: "Got " + strings.Join(pos, " ") + "."}
	}
	if *allSites && len(sites) > 0 {
		return &ui.Problem{Hint: "--all-sites and --site both name the sites", Explain: "Give one of them."}
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	declared := create.Declared{Operators: cfg.Operators}
	for s := range cfg.Sites {
		declared.Sites = append(declared.Sites, s)
	}
	slices.Sort(declared.Sites)

	op := create.NewOperator{Name: *name, Email: *email, Sites: sites}
	if len(pos) == 1 {
		op.ID = pos[0]
	}
	if *allSites {
		op.Sites = slices.Clone(declared.Sites)
	}
	for _, k := range keys {
		lines, err := keyLines(k)
		if err != nil {
			return err
		}
		op.Keys = append(op.Keys, lines...)
	}

	if stdinIsTerminal(stdin) {
		p := create.NewPrompter(stdin, stdout)
		if op, err = create.AskNewOperator(p, op, declared); err != nil {
			if errors.Is(err, create.ErrAborted) {
				return &ui.Problem{Hint: "input ended before every question was answered", Explain: "Nothing was written."}
			}
			return flagProblem(err)
		}
	} else {
		if err := operatorAddFlagsGiven(op, *allSites); err != nil {
			return err
		}
		if err := create.CheckNewOperator(op, declared); err != nil {
			return flagProblem(err)
		}
	}

	r := reporter()
	r.Section("operator add " + op.ID)
	data, err := os.ReadFile(*configPath)
	if err != nil {
		return err
	}
	entry := config.Operator{Name: op.Name, Email: op.Email, Keys: strings.Join(op.Keys, "\n") + "\n"}
	out, err := config.AddOperatorBytes(data, op.ID, entry, op.Sites)
	if err != nil {
		return &ui.Problem{Hint: "operator add cannot edit " + ui.ShortPath(*configPath) + " in place", Explain: err.Error() + ". Add the operator by hand: see " + progname.Cmd() + " help operator add. Nothing was written."}
	}
	before, err := checkRendered(data)
	if err != nil {
		return err
	}
	after, err := checkRendered(out)
	if err != nil {
		return err
	}
	added := newFindings(before, after)
	if len(added.Findings) > 0 {
		reportValidation(r, *configPath, added)
	}
	if added.Refused() {
		return &ui.Problem{Hint: "validate refuses the file with " + op.ID + " added", Explain: "Nothing was written."}
	}
	s := r.Step("declare " + op.ID + " in " + *configPath)
	if err := config.WriteDeclaration(*configPath, out); err != nil {
		s.Fail(err)
		return err
	}
	s.Done(siteSummary(op.Sites))
	r.Result("%s is declared. Nothing has reached a host yet.", op.ID)
	inv := progname.Cmd()
	fmt.Fprintln(stdout, "\nNext:")
	fmt.Fprintf(stdout, "  # to let %s read the secrets: add their age public key to .sops.yaml, then\n", op.ID)
	fmt.Fprintln(stdout, "  sops updatekeys secrets.enc.yaml")
	fmt.Fprintf(stdout, "  %s deployment apply --execute   # gives %s their sites, publishes the change, makes their Pocket ID account and prints their login link\n", inv, op.ID)
	fmt.Fprintf(stdout, "  # then %s, on their own workstation, once the files are there:\n", op.ID)
	fmt.Fprintf(stdout, "  %s operator register --execute\n", inv)
	return nil
}

// operatorAddFlagsGiven refuses a run without a terminal that leaves a
// question unanswered, naming the flag that answers it.
func operatorAddFlagsGiven(op create.NewOperator, allSites bool) error {
	var missing []string
	if op.ID == "" {
		missing = append(missing, "the username")
	}
	if op.Name == "" {
		missing = append(missing, "--name")
	}
	if op.Email == "" {
		missing = append(missing, "--email")
	}
	if len(op.Keys) == 0 {
		missing = append(missing, "--key")
	}
	if len(op.Sites) == 0 && !allSites {
		missing = append(missing, "--site or --all-sites")
	}
	if len(missing) == 0 {
		return nil
	}
	return &ui.Problem{
		Hint:    "operator add is missing " + strings.Join(missing, ", "),
		Explain: "stdin is not a terminal, so nothing is asked. Give them as flags, Eg: " + progname.Cmd() + " operator add carol --name 'Carol Example' --email carol@example.org --key carol.pub --all-sites, or run it at a terminal. Nothing was written.",
	}
}

// flagProblem is a flag's answer the prompt would refuse, said as a problem.
func flagProblem(err error) error {
	var fe *create.FlagError
	if errors.As(err, &fe) {
		return &ui.Problem{Hint: fe.Flag + " is refused", Explain: fe.Err.Error() + ". Nothing was written."}
	}
	return err
}

// keyLines is the keys one --key gives: the value itself when it is a key
// line, and otherwise every line of the file it names but blank lines and
// comments.
func keyLines(v string) ([]string, error) {
	if _, _, err := config.ParseKeyLine(v); err == nil {
		return []string{strings.TrimSpace(v)}, nil
	}
	data, err := os.ReadFile(v)
	if err != nil {
		return nil, &ui.Problem{Hint: "--key is neither a public key nor a file that can be read", Explain: err.Error() + ". Nothing was written."}
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return nil, &ui.Problem{Hint: "--key names a file that holds no key", Explain: v + " has no line but blank lines and comments. Nothing was written."}
	}
	return out, nil
}

// newFindings is the findings after that before did not have, so an
// operator added to a file validate already faults is judged on what the
// addition changed.
func newFindings(before, after validate.Result) validate.Result {
	seen := map[string]bool{}
	for _, f := range before.Findings {
		seen[f.Rule+"\x00"+f.Key+"\x00"+f.Message] = true
	}
	var out validate.Result
	for _, f := range after.Findings {
		if !seen[f.Rule+"\x00"+f.Key+"\x00"+f.Message] {
			out.Findings = append(out.Findings, f)
		}
	}
	return out
}

func siteSummary(sites []string) string { return "on " + strings.Join(sites, ", ") }

// What follows is `paisans operator add --request`
// (docs/specs/2026-10-24-operator-invite.md, *operator add --request*): any
// operator accepts a new operator's signed request, which answers an invite
// an operator of this deployment signed. It declares them as operator add
// does, adds their age key to .sops.yaml and re-encrypts the secrets to the
// new set. Every check is made and all three files are computed before the
// first is written, and a write that fails puts back the files written
// before it.

// writeDeclaration writes paisans-stack.yaml. Tests replace it.
var writeDeclaration = config.WriteDeclaration

// askNewOperator asks at a terminal for what a request leaves to the
// approver. Tests replace it.
var askNewOperator = create.AskNewOperator

func runOperatorAddRequest(r ui.Reporter, configPath, requestPath string, sites []string, allSites bool, stdin io.Reader, stdout io.Writer) error {
	signed, err := readRequestFile(requestPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	// The root the invite must name is the one the system copy trusts.
	sys, _, _, err := systemFor(configPath, "")
	if err != nil {
		return err
	}
	r.Section("operator add --request")

	step := r.Step("check request")
	q, _, err := operatorinvite.ParseRequest(signed)
	if err != nil {
		step.Fail(err)
		return &ui.Problem{Hint: "the request cannot be accepted", Explain: "Nothing was written. Ask for the request again, as an attachment.", Cause: err}
	}
	// ParseRequest checked the invite for shape alone. InviteSigner checks
	// its signature, and the key that made it must be an operator's here:
	// an invite anyone could sign would let anyone in.
	inv, signer, err := operatorinvite.InviteSigner([]byte(q.Invite))
	if err != nil {
		step.Fail(err)
		return &ui.Problem{Hint: "the invite inside the request cannot be accepted", Explain: "Nothing was written.", Cause: err}
	}
	inviter, ok := operatorListing(cfg, signer)
	if !ok {
		err := &ui.Problem{Hint: "no operator of this deployment signed the invite", Explain: fmt.Sprintf("The invite inside the request is signed by %s, and %s lists that key under no operator. Only an invite an operator here wrote can be accepted. Nothing was written.", keyFingerprint(signer), config.FileName)}
		step.Fail(err)
		return err
	}
	if inv.Deployment != cfg.ID {
		err := &ui.Problem{Hint: "the invite is for another deployment", Explain: fmt.Sprintf("It names the deployment %s, and %s is %s. Accept it beside that deployment's files. Nothing was written.", inv.Deployment, ui.ShortPath(configPath), cfg.ID)}
		step.Fail(err)
		return err
	}
	root, err := trustedRoot(sys, cfg.ID)
	if err != nil {
		step.Fail(err)
		return err
	}
	if inv.Root != root {
		err := &ui.Problem{Hint: "the invite names another root", Explain: fmt.Sprintf("It names the root %s, and the history this workstation trusts starts at %s. The new operator would recover a history this deployment does not have: the inviter's workstation trusts another root than this one. Ask the inviter to run %s deployment pull, check that a root's line of their %s deployment log is %s, and write a new invite. Nothing was written.", inv.Root, root, progname.Cmd(), progname.Cmd(), root)}
		step.Fail(err)
		return err
	}
	step.Done("for " + inv.Username + ", invited by " + inviter)

	key := keyFingerprint(inv.Key)
	fmt.Fprintf(stdout, "\nAccepting %s as an operator of %s:\n", inv.Username, cfg.Community.Domain)
	fmt.Fprintf(stdout, "  username    %s\n", inv.Username)
	fmt.Fprintf(stdout, "  name        %s\n", q.Name)
	fmt.Fprintf(stdout, "  email       %s\n", q.Email)
	fmt.Fprintf(stdout, "  ssh key     %s\n", key)
	fmt.Fprintf(stdout, "  age key     %s\n", q.Age)
	fmt.Fprintf(stdout, "  invited by  %s, %s\n", inviter, keyFingerprint(signer))

	declared := create.Declared{Operators: cfg.Operators, Sites: cfg.SiteNames()}
	existing, already := cfg.Operators[inv.Username]
	if already && !sameKeys(existing.KeyLines(), inv.Key) {
		return &ui.Problem{Hint: inv.Username + " is declared with another key", Explain: fmt.Sprintf("%s lists %s with keys other than %s, the key the invite names. A username belongs to one operator: invite them under another, or remove the entry first. Nothing was written.", config.FileName, inv.Username, key)}
	}
	if already && (allSites || len(sites) > 0) {
		flag := "--site"
		if allSites {
			flag = "--all-sites"
		}
		return &ui.Problem{Hint: flag + " cannot change the sites of " + inv.Username + ", who is declared already", Explain: "Accepting the request again only adds their age key and re-encrypts the secrets. Edit their sites in " + config.FileName + ", or run it again without " + flag + ". Nothing was written."}
	}
	op := create.NewOperator{ID: inv.Username, Name: q.Name, Email: q.Email, Keys: []string{inv.Key}, Sites: sites}
	if allSites {
		op.Sites = slices.Clone(declared.Sites)
	}
	if !already {
		// Everything but the sites comes from the request, so a refusal of
		// it is the request's and is not asked again.
		probe := op
		if len(probe.Sites) == 0 {
			probe.Sites = declared.Sites[:min(1, len(declared.Sites))]
		}
		if err := create.CheckNewOperator(probe, declared); err != nil {
			return requestProblem(err)
		}
	}

	// What .sops.yaml and the secrets need: the age key named, and the
	// secrets encrypted to it.
	dir := filepath.Dir(configPath)
	sopsPath, secretsPath := filepath.Join(dir, config.SOPSConfigName), filepath.Join(dir, "secrets.enc.yaml")
	oldSOPS, err := os.ReadFile(sopsPath)
	if err != nil {
		return &ui.Problem{Hint: "there is no " + config.SOPSConfigName + " to add the age key to", Explain: errLine(err) + ". Nothing was written.", Cause: err}
	}
	newSOPS, err := config.AddSOPSRecipient(dir, q.Age)
	if err != nil {
		return &ui.Problem{Hint: config.SOPSConfigName + " cannot be given the age key", Explain: err.Error() + ". Nothing was written."}
	}
	oldSecrets, err := os.ReadFile(secretsPath)
	if err != nil {
		return &ui.Problem{Hint: "the secrets file cannot be read", Explain: errLine(err) + ". Nothing was written.", Cause: err}
	}
	var newSecrets []byte
	if !slices.Contains(config.FileRecipients(oldSecrets), q.Age) {
		// Re-encrypted to every recipient the new .sops.yaml names, the
		// plaintext as it is, before anything is asked or written.
		step := r.Step("re-encrypt the secrets in memory")
		if fields := config.HandSetMetadata(oldSecrets); len(fields) > 0 {
			err := config.HandSetMetadataProblem(secretsPath, fields)
			step.Fail(err)
			return err
		}
		recipients, err := sopsRecipients(newSOPS)
		if err != nil {
			step.Fail(err)
			return err
		}
		if newSecrets, err = config.ReencryptSecretsBytes(secretsPath, oldSecrets, cfg.ID, recipients); err != nil {
			step.Fail(err)
			return &ui.Problem{Hint: "this workstation cannot open the secrets", Explain: "Re-encrypting " + ui.ShortPath(secretsPath) + " to " + inv.Username + "'s age key takes opening it with yours. Nothing was written.", Cause: err}
		}
		step.Done("with your age key, to " + strconv.Itoa(len(recipients)) + " recipients")
	}

	if already && newSecrets == nil && bytes.Equal(newSOPS, oldSOPS) {
		r.Result("%s is declared with this key, %s names their age key and the secrets are encrypted to it: nothing to change.", inv.Username, config.SOPSConfigName)
		operatorAddRequestNext(stdout, inv.Username)
		return nil
	}

	var newDecl []byte
	if !already {
		if stdinIsTerminal(stdin) {
			p := create.NewPrompter(stdin, stdout)
			if op, err = askNewOperator(p, op, declared); err != nil {
				if errors.Is(err, create.ErrAborted) {
					return &ui.Problem{Hint: "input ended before every question was answered", Explain: "Nothing was written."}
				}
				return flagProblem(err)
			}
			// Only the sites are the approver's to answer: the rest is the
			// request's, and the key above all.
			if op.ID != inv.Username || op.Name != q.Name || op.Email != q.Email || !slices.Equal(op.Keys, []string{inv.Key}) {
				return &ui.Problem{Hint: "the answers changed what the request gives", Explain: "The username, name, email and key are the request's, and only the sites are asked. Nothing was written."}
			}
		} else {
			if len(op.Sites) == 0 {
				return &ui.Problem{Hint: "operator add --request is missing --site or --all-sites", Explain: "stdin is not a terminal, so nothing is asked. Give the sites " + inv.Username + " may log in to, Eg: " + progname.Cmd() + " operator add --request " + requestPath + " --all-sites, or run it at a terminal. Nothing was written."}
			}
			if err := create.CheckNewOperator(op, declared); err != nil {
				return flagProblem(err)
			}
		}
		data, err := os.ReadFile(configPath)
		if err != nil {
			return err
		}
		entry := config.Operator{Name: op.Name, Email: op.Email, Keys: strings.Join(op.Keys, "\n") + "\n"}
		if newDecl, err = config.AddOperatorBytes(data, op.ID, entry, op.Sites); err != nil {
			return &ui.Problem{Hint: "operator add cannot edit " + ui.ShortPath(configPath) + " in place", Explain: err.Error() + ". Add the operator by hand: see " + progname.Cmd() + " help operator add. Nothing was written."}
		}
		before, err := checkRendered(data)
		if err != nil {
			return err
		}
		after, err := checkRendered(newDecl)
		if err != nil {
			return err
		}
		added := newFindings(before, after)
		if len(added.Findings) > 0 {
			reportValidation(r, configPath, added)
		}
		if added.Refused() {
			return &ui.Problem{Hint: "validate refuses the file with " + op.ID + " added", Explain: "Nothing was written."}
		}
	}

	// The writes, each through a file beside its target renamed over it. A
	// failure puts back what was written before it.
	var undo []func() error
	fail := func(s ui.Step, err error) error {
		s.Fail(err)
		for i := len(undo) - 1; i >= 0; i-- {
			if uerr := undo[i](); uerr != nil {
				return &ui.Problem{Hint: "a write failed and the files could not all be put back", Explain: fmt.Sprintf("%s, then putting back: %s. %s, %s and secrets.enc.yaml may not match: %s deployment pull puts back the deployment's newest version, then run this again.", errLine(err), errLine(uerr), config.FileName, config.SOPSConfigName, progname.Cmd()), Cause: err}
			}
		}
		return &ui.Problem{Hint: "a write failed", Explain: errLine(err) + ". Every file is as it was before the command.", Cause: err}
	}
	if !bytes.Equal(newSOPS, oldSOPS) {
		s := r.Step("add " + inv.Username + "'s age key to " + config.SOPSConfigName)
		if err := config.ReplaceFile(sopsPath, newSOPS); err != nil {
			return fail(s, err)
		}
		undo = append(undo, func() error { return config.ReplaceFile(sopsPath, oldSOPS) })
		s.Done(q.Age)
	}
	if newSecrets != nil {
		s := r.Step("re-encrypt secrets.enc.yaml")
		if err := config.WriteSecretsFile(secretsPath, newSecrets); err != nil {
			return fail(s, err)
		}
		undo = append(undo, func() error { return config.WriteSecretsFile(secretsPath, oldSecrets) })
		s.Done("to " + inv.Username + " as well")
	}
	if newDecl != nil {
		s := r.Step("declare " + op.ID + " in " + configPath)
		if err := writeDeclaration(configPath, newDecl); err != nil {
			return fail(s, err)
		}
		s.Done(siteSummary(op.Sites))
		r.Result("%s is declared and can read the secrets. Nothing has reached a host yet.", op.ID)
	} else {
		r.Result("%s was declared already and can now read the secrets. Nothing has reached a host yet.", op.ID)
	}
	operatorAddRequestNext(stdout, inv.Username)
	return nil
}

func operatorAddRequestNext(stdout io.Writer, id string) {
	inv := progname.Cmd()
	fmt.Fprintln(stdout, "\nNext:")
	fmt.Fprintf(stdout, "  %s deployment apply --execute   # gives %s their sites, publishes the change, makes their Pocket ID account and prints their login link\n", inv, id)
	fmt.Fprintf(stdout, "  # then %s, on their own workstation, in the directory where they answered the invite:\n", id)
	fmt.Fprintf(stdout, "  %s operator register --execute\n", inv)
}

// readRequestFile is the request at path, read no further than the largest
// request ParseRequest accepts, so a larger file is refused by it unread.
func readRequestFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, &ui.Problem{Hint: "--request cannot be read", Explain: errLine(err) + ". Nothing was written.", Cause: err}
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, operatorinvite.MaxSize+1))
	if err != nil {
		return nil, &ui.Problem{Hint: "--request cannot be read", Explain: errLine(err) + ". Nothing was written.", Cause: err}
	}
	return data, nil
}

// operatorListing is the operator cfg lists the key line under.
func operatorListing(cfg *config.Config, line string) (string, bool) {
	k, _, err := config.ParseKeyLine(line)
	if err != nil {
		return "", false
	}
	for _, id := range slices.Sorted(maps.Keys(cfg.Operators)) {
		for _, al := range cfg.Operators[id].KeyLines() {
			if a, _, err := config.ParseKeyLine(al); err == nil && a.Fingerprint == k.Fingerprint {
				return id, true
			}
		}
	}
	return "", false
}

// sameKeys is whether lines are the one key line, by fingerprint.
func sameKeys(lines []string, line string) bool {
	if len(lines) != 1 {
		return false
	}
	return keyFingerprint(lines[0]) == keyFingerprint(line)
}

// keyFingerprint is a key line's SHA256 fingerprint, or the line itself
// when it is not a key.
func keyFingerprint(line string) string {
	k, _, err := config.ParseKeyLine(line)
	if err != nil {
		return line
	}
	return k.Fingerprint
}

// requestProblem is a refusal of what the request gave, said as the
// request's rather than a flag's.
func requestProblem(err error) error {
	var fe *create.FlagError
	if !errors.As(err, &fe) {
		return err
	}
	if strings.HasPrefix(fe.Flag, "--site") {
		return flagProblem(err)
	}
	field := strings.TrimPrefix(strings.Fields(fe.Flag + " ")[0], "--")
	if fe.Flag == "the username" {
		field = "username"
	}
	return &ui.Problem{Hint: "the request's " + field + " is refused", Explain: fe.Err.Error() + ". Nothing was written."}
}

// sopsRecipients is the recipients a .sops.yaml of data names, as
// config.Recipients reads them, read from a scratch copy so that nothing
// beside the deployment changes.
func sopsRecipients(data []byte) ([]string, error) {
	scratch, err := os.MkdirTemp("", "paisans-request-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	if err := os.WriteFile(filepath.Join(scratch, config.SOPSConfigName), data, 0o600); err != nil {
		return nil, err
	}
	return config.Recipients(scratch)
}
