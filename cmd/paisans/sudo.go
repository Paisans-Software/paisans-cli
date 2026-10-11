package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// sudoAuths holds one SudoAuth per destination for the whole run, so a site
// whose transport is built several times (a map of every site, then the one
// a stage needs) asks for its sudo password once.
var (
	sudoAuthsMu sync.Mutex
	sudoAuths   = map[string]*apply.SudoAuth{}
)

func sudoAuthFor(destination string) *apply.SudoAuth {
	sudoAuthsMu.Lock()
	defer sudoAuthsMu.Unlock()
	if auth, ok := sudoAuths[destination]; ok {
		return auth
	}
	var prompt func(string) (string, error)
	if terminalAvailable() {
		prompt = promptOnTerminal
	}
	auth := apply.NewSudoAuth(prompt)
	sudoAuths[destination] = auth
	return auth
}

// settleSudo settles sudo on every host of a run that reaches several,
// before the run reaches any of them, so each password is asked at the start
// rather than whenever a later step first needs that host. sites is in the
// order they are asked; host is each one's transport, the one the steps
// build, so the answer kept is the one they find.
//
// Under a sudo section, each site's line is ✓ once settled, password
// accepted or none needed. A refusal stops the run there, its line ✗ and its
// problem returned. A host that did not answer, or would not log ssh in, is
// not a stop: its line waits, and the first step that reaches it asks then
// and reports it as it would have. Nothing is printed for a run whose hosts
// have no sudo to settle.
func settleSudo(r ui.Reporter, sites []string, host func(string) apply.Transport) error {
	var names []string
	hosts := map[string]apply.Transport{}
	for _, name := range sites {
		if t := host(name); apply.SettlesSudo(t) {
			names = append(names, name)
			hosts[name] = t
		}
	}
	if len(names) == 0 {
		return nil
	}
	r.Section("sudo")
	for _, name := range names {
		line := r.Step(name)
		err := apply.PrepareSudo(hosts[name])
		switch {
		case err == nil:
			// End rather than Done: Done would time the line, and the
			// time is the operator's typing.
			line.End(ui.OK, "")
		case errors.Is(err, apply.ErrSudo):
			line.Fail(err)
			return err
		default:
			line.End(ui.Waiting, "not reached; asked when a step reaches it")
		}
	}
	return nil
}

// openTTY opens the controlling terminal. The password is read there rather
// than from stdin, because stdin may already carry something else (`secrets
// set` and `app admin create` read a value from it), and because a run with
// no controlling terminal, from cron or CI, is exactly the run that must not
// be asked. Tests replace it.
var openTTY = func() (*os.File, error) { return os.OpenFile("/dev/tty", os.O_RDWR, 0) }

func terminalAvailable() bool {
	tty, err := openTTY()
	if err != nil {
		return false
	}
	defer tty.Close()
	return isTerminal(tty)
}

// promptOnTerminal reads a sudo password from the controlling terminal with
// echo off, with the reporter's drawing held. The first sudo command runs
// inside an open step, so without the hold the spinner's next frame would
// erase the prompt and leave the operator watching a spinner that waits on
// them without saying so. The prompt is erased once answered, so the
// spinner redraws on the row it took.
func promptOnTerminal(host string) (string, error) {
	resume := holdOutput()
	defer resume()
	return readPassword(host)
}

// readPassword asks on the controlling terminal. Tests replace it, since a
// test has no terminal to type into.
var readPassword = func(host string) (string, error) {
	tty, err := openTTY()
	if err != nil {
		return "", err
	}
	defer tty.Close()
	fd := int(tty.Fd())
	cols, _, err := term.GetSize(fd)
	if err != nil {
		cols = 0
	}
	return askPassword(tty, sudoPrompt(host), cols, promptErasable(), func() ([]byte, error) { return term.ReadPassword(fd) })
}

// promptErasable reports whether the terminal may be drawn on to erase the
// prompt: not when TERM=dumb or NO_COLOR asks for plain output, as the
// reporter draws nothing then either, and not when the output is piped.
func promptErasable() bool {
	return os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" && outputsAreTerminals()
}

// outputsAreTerminals reports whether stdout and stderr both write straight
// to a terminal. Piped (paisans apply | tee log), they reach it through
// another process at its own pace, so what is on the rows above the cursor
// when the prompt is answered is not known, and erasing could take a line of
// the report. Tests replace it.
var outputsAreTerminals = func() bool {
	return term.IsTerminal(int(os.Stdout.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
}

func sudoPrompt(host string) string { return "sudo password for " + host + ": " }

// askPassword writes prompt to tty, a terminal cols wide, reads the answer
// with read, and, when erase is set, erases the prompt, whether the answer
// was read or not: what follows says whether it was right. Echo is off while
// read waits, so the newline that ends the prompt is written here, and then,
// from the row below the prompt, a carriage return and, for each row the
// prompt took, a cursor up and a line clear. The cursor ends at the start of
// the prompt's first row, which is empty, where the held spinner redraws.
// Without erase the prompt stays, and a blank line sets what follows apart.
//
// The erase counts rows from where the cursor is, so it relies on nothing
// else reaching the terminal while the prompt waited: the hold keeps the
// reporter quiet, and promptErasable asks for no erase when the output is
// piped, where it would reach the terminal unheld.
func askPassword(tty io.Writer, prompt string, cols int, erase bool, read func() ([]byte, error)) (string, error) {
	fmt.Fprint(tty, prompt)
	password, err := read()
	fmt.Fprintln(tty)
	if erase {
		fmt.Fprint(tty, "\r"+strings.Repeat("\x1b[1A\x1b[2K", promptRows(prompt, cols)))
	} else {
		fmt.Fprintln(tty)
	}
	if err != nil {
		return "", err
	}
	return string(password), nil
}

// promptRows is how many rows prompt takes on a terminal cols wide: one
// when the width is not known, since a terminal too narrow to say is also
// too unusual to guess for.
func promptRows(prompt string, cols int) int {
	n := utf8.RuneCountInString(prompt)
	if cols <= 0 || n <= cols {
		return 1
	}
	return (n + cols - 1) / cols
}
