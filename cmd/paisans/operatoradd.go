package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/create"
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
	var keys, sites pathList
	fs.Var(&keys, "key", "a public key file (.pub), or one key line itself; once per key or file")
	fs.Var(&sites, "site", "a site the operator may log in to (repeatable)")
	allSites := fs.Bool("all-sites", false, "let the operator log in to every site")
	pos, err := parseAll(fs, args)
	if err != nil {
		return err
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
	if err := config.AddOperator(*configPath, op.ID, entry, op.Sites); err != nil {
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

func siteSummary(sites []string) string {
	if len(sites) == 0 {
		return "on no site"
	}
	return "on " + strings.Join(sites, ", ")
}
