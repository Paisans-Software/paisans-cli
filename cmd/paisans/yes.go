package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// allowYesVariable is the environment variable --yes needs set to 1. A
// command line copied from a log, a runbook or a shell history carries
// --yes and not the environment it ran in, so the copy alone never
// approves anything unseen.
const allowYesVariable = "PAISANS_ALLOW_YES"

// answerer answers the confirmations a command asks for: the typed words
// and the y/N questions. The zero value leaves each to the operator at a
// terminal. One made from --yes gives each answer itself, as typing it
// would, and prints a line for each, so the run's log records what was
// accepted unseen.
//
// It answers nothing only a person can: a request's digest (site admit
// --request takes it as --digest), a new SSH login through ufw, a login
// to use, or a value to paste. Each of those is asked or refused as it is
// without --yes.
type answerer struct{ yes bool }

// yesFlag registers --yes on fs. What it returns, called once fs is
// parsed, is the command's answerer, or the refusal of --yes when the
// environment does not set PAISANS_ALLOW_YES=1. It is the one place that
// variable is read.
func yesFlag(fs *flag.FlagSet) func() (answerer, error) {
	yes := fs.Bool("yes", false, "answer every confirmation as typing it would, printing each answer; needs "+allowYesVariable+"=1 in the environment")
	name := fs.Name()
	return func() (answerer, error) {
		if !*yes {
			return answerer{}, nil
		}
		if os.Getenv(allowYesVariable) != "1" {
			return answerer{}, &ui.Problem{
				Hint:    "--yes needs " + allowYesVariable + "=1 in the environment",
				Explain: "--yes approves what " + name + " would ask you to confirm, unseen, so it also needs " + allowYesVariable + "=1: a command line copied from a log or a runbook never approves anything on its own. Run it as " + allowYesVariable + "=1 paisans " + name + " ... --yes. Nothing was read or changed.",
			}
		}
		return answerer{yes: true}, nil
	}
}

// canAnswer reports whether a confirmation can be answered: by --yes, or
// by the operator, when stdin is a terminal.
func (a answerer) canAnswer(stdin io.Reader) bool { return a.yes || stdinIsTerminal(stdin) }

// approved prints the line for one answer --yes gave: what it approved.
func (a answerer) approved(stdout io.Writer, what string) {
	fmt.Fprintf(stdout, "approved by --yes: %s\n", what)
}

// yesNo puts a y/N question: explain, which says what a yes does, then
// question at the operator's terminal, where only y or yes says yes. With
// --yes it prints explain and says yes itself, printing what it approved.
func (a answerer) yesNo(stdin io.Reader, stdout io.Writer, explain, question, what string) (bool, error) {
	fmt.Fprint(stdout, explain)
	if a.yes {
		fmt.Fprintln(stdout)
		a.approved(stdout, what)
		return true, nil
	}
	fmt.Fprintf(stdout, "%s [y/N] ", question)
	line, err := readTypedLine(stdin)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}
