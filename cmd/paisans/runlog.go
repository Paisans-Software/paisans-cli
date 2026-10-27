package main

import (
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/localstore"
	"github.com/paisans-software/paisans-cli/internal/runlog"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// openRunLog starts the run log of command, args its arguments, under the
// data directory, and returns what ends it. Only a run that executes is
// logged: a dry run writes nothing, its log included. The log is a
// convenience: one that cannot be opened is not written, and the command
// runs the same.
// While one is open, every step and every command sent to a host is told to
// it (see internal/runlog).
func openRunLog(command string, args []string) (closeLog func()) {
	if runlog.Current() != nil || !writesRecords(args) {
		return func() {}
	}
	base, err := localstore.Base()
	if err != nil {
		return func() {}
	}
	l, err := runlog.Open(base, version.Current().Version, command, args)
	if err != nil {
		return func() {}
	}
	savedSteps, savedSSH := ui.StepLog, apply.SSHLog
	ui.StepLog, apply.SSHLog = logStep(l), logSSH
	return func() {
		ui.StepLog, apply.SSHLog = savedSteps, savedSSH
		l.Close()
	}
}

// logSSH tells the current run log of one command sent to site, naming what
// in Paisans sent it.
func logSSH(site, command string, took time.Duration, err error) {
	runlog.Current().SSH(site, ui.Redact(command), runlog.Caller("main.logSSH", "apply.SSHTransport."), took, err)
}

// logStep is l.Step with each title and result redacted: a failed step's
// result reaches the log whole, detail lines included.
func logStep(l *runlog.Log) func(string) func(string, string) {
	return func(title string) func(string, string) {
		end := l.Step(ui.Redact(title))
		return func(mark, result string) { end(mark, ui.Redact(result)) }
	}
}
