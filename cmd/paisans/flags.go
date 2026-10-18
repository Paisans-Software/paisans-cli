package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// flagsOnly, set by a test, stops each command parseFlags parses for once its
// flags parse, so a test can prove a command accepts the arguments apply
// without --site hands it without running it.
var flagsOnly bool

// errFlagsOnly is what a command returns when flagsOnly stopped it.
var errFlagsOnly = errors.New("flags parsed; stopped before doing anything")

// parseFlags is fs.Parse for each command apply without --site runs as a
// step. With flagsOnly set it returns a bad flag or a stray argument as an
// error rather than exiting, and errFlagsOnly once the flags parse.
func parseFlags(fs *flag.FlagSet, args []string) error {
	if !flagsOnly {
		return fs.Parse(args)
	}
	fs.Init(fs.Name(), flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%s: %q is extra", fs.Name(), fs.Arg(0))
	}
	return errFlagsOnly
}

// commonFlags registers the flags every command takes, and returns the
// reporter for the command's stdout once the flags are parsed. -v and
// --verbose show the detail behind each step: reasons, configuration values,
// requests and retries. Without them a command prints one line per step.
func commonFlags(fs *flag.FlagSet) func() ui.Reporter {
	verbose := &verboseRun
	verboseFlags(fs, verbose)
	return func() ui.Reporter {
		r := reporterOverride
		if r == nil {
			r = ui.New(os.Stdout, *verbose)
		}
		routeRetries(r)
		routeHolds(r)
		routeKeyNotices(r)
		routeLoginNotes(r)
		// Once a command run with --execute has set it, the steps it runs
		// in this process leave it set, whatever their own flags.
		if executes(fs) {
			recordLogins = true
		}
		if fs.Lookup("execute") != nil {
			offersExecute = true
		}
		return r
	}
}

// verboseFlags registers -v and --verbose on fs, into verbose.
func verboseFlags(fs *flag.FlagSet, verbose *bool) {
	fs.BoolVar(verbose, "verbose", false, "show the detail behind each step: reasons, values, requests and retries")
	fs.BoolVar(verbose, "v", false, "short for --verbose")
}

// verboseRun is whether the command being run was given -v or --verbose, for
// the error that ends it, which main prints once the command has returned.
var verboseRun bool

// routeRetries sends an ssh retry to r as a detail of the open step: the
// error that ends the command says in full if the retries ran out. A command
// that moves its report to stderr (errReporter) moves the retries with it,
// because stdout there carries data a script reads and a retry line beside
// it would corrupt it.
func routeRetries(r ui.Reporter) {
	if c, ok := r.(interface{ RetryLog() io.Writer }); ok {
		apply.SetRetryLog(c.RetryLog())
		return
	}
	apply.SetRetryLog(detailWriter{r})
}

// holdOutput pauses the drawing of the reporter a command reports through,
// for as long as something else asks on the terminal. routeHolds points it at
// the reporter that draws, as routeRetries does for the retry lines, so that
// a command that moved its report to stderr holds the spinner it actually
// shows.
var holdOutput = func() (resume func()) { return func() {} }

// routeHolds holds r around the sudo password prompt and around ssh's first
// connection to each host, where ssh itself may ask to accept a host key.
func routeHolds(r ui.Reporter) {
	hold := func() (resume func()) { return ui.Hold(r) }
	holdOutput = hold
	apply.SetPromptHold(hold)
}

// routeKeyNotices sends r the notice that a SOPS_AGE_* variable decrypted
// the secrets while the keystore holds the deployment's own key, which the
// variable does not name. It is shown at every verbosity, once a command:
// the operator saved a key and is not using it, and a line only --verbose
// shows is one they never see. A command loads the secrets more than once,
// so a repeat says nothing new.
func routeKeyNotices(r ui.Reporter) {
	var mu sync.Mutex
	shown := map[string]bool{}
	config.KeystoreBypassed = func(variables []string, item string) {
		mu.Lock()
		defer mu.Unlock()
		if shown[item] {
			return
		}
		shown[item] = true
		are := " is"
		if len(variables) > 1 {
			are = " are"
		}
		names := andList(variables)
		r.Note(names+are+" set, so the saved age key is not used", item+" holds this deployment's age key, and paisans decrypts with what "+names+" names instead. Unset "+names+" to use the saved key.")
	}
}

// detailWriter turns each line written to it into a detail of the reporter.
type detailWriter struct{ r ui.Reporter }

func (d detailWriter) Write(p []byte) (int, error) {
	d.r.Detail("%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// reporterOverride, when set, is the reporter every command uses in place of
// stdout's. A test sets it to a ui.Recorder, so it asserts on the steps and
// marks a command reports rather than on how they are drawn.
var reporterOverride ui.Reporter

// errReporter is r's verbosity on stderr, for a command whose stdout carries
// data that a script reads.
func errReporter(r ui.Reporter) ui.Reporter {
	if reporterOverride != nil {
		return reporterOverride
	}
	e := ui.New(os.Stderr, r.Verbose())
	routeRetries(e)
	routeHolds(e)
	routeKeyNotices(e)
	routeLoginNotes(e)
	return e
}
