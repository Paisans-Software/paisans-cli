package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/user"
	"slices"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/lease"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// breakWord is what an operator types to take over a live lease.
const breakWord = "break"

// runLease is the lease the running command holds, so a command it runs as
// a step (apply --site inside apply) takes none of its own: the run holds
// it for every step.
var runLease *lease.Lease

// leaseTake takes a lease on sites. Tests replace it.
var leaseTake = lease.Take

// leaseWho is who a lease names: the workstation's user and host. Tests
// replace it.
var leaseWho = func() string {
	name := "someone"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "a workstation"
	}
	return name + "@" + host
}

// leaseOptions is how a command takes the lease: --break-lease, and how the
// word it asks for is answered.
type leaseOptions struct {
	breakLease bool
	answers    answerer
	stdin      io.Reader
	stdout     io.Writer
}

// breakLeaseFlag adds --break-lease to fs.
func breakLeaseFlag(fs *flag.FlagSet) *bool {
	return fs.Bool("break-lease", false, breakLeaseHelp)
}

// breakLeaseHelp is --break-lease's help.
const breakLeaseHelp = "take over the deployment lease another command holds, once you know it is no longer running; asks for the word " + breakWord

// takeLease takes the deployment's lease for command on every site in
// names, before the command publishes or changes anything, and returns what
// releases it. A command run as a step of one holding it takes nothing.
// Another command's live lease refuses the run, naming it, unless
// --break-lease and the word take it over. See internal/lease for why.
func takeLease(r ui.Reporter, cfg *config.Config, names []string, host func(name string) apply.Transport, command string, o leaseOptions) (release func(), err error) {
	if runLease != nil {
		return func() {}, nil
	}
	h, err := lease.NewHolder(leaseWho(), command)
	if err != nil {
		return nil, err
	}
	sites := map[string]lease.Runner{}
	for _, name := range names {
		sites[name] = host(name)
	}
	d := cfg.Deployment()
	s := r.Step("take the deployment lease")
	l, unreached, err := leaseTake(sites, d, h, false)
	var held *lease.HeldError
	if errors.As(err, &held) && o.breakLease {
		s.End(ui.Warned, held.Error())
		what := fmt.Sprintf("%s. Taking it over lets this run change the deployment while that one may still be changing it: do so only once you know it is not running.", held.Error())
		if err := confirmWord(o.answers, o.stdin, o.stdout, breakWord, what); err != nil {
			return nil, err
		}
		s = r.Step("take over the deployment lease")
		l, unreached, err = leaseTake(sites, d, h, true)
	}
	if errors.As(err, &held) {
		s.End(ui.Failed, held.Error())
		return nil, &ui.Problem{
			Hint: "another command is changing this deployment",
			Explain: fmt.Sprintf("%s. It expires %s after its last renewal, so a command that is no longer running stops blocking within that. Wait for it to end, or, once you know it is not running, run again with --break-lease. Nothing was changed.",
				held.Error(), lease.TTL),
			Cause: err,
		}
	}
	if err != nil {
		s.Fail(err)
		return nil, &ui.Problem{Hint: "could not take the deployment lease", Explain: errLine(err) + ". Nothing was changed.", Cause: err}
	}
	s.Done(fmt.Sprintf("held by %s on %s", h.Who, andList(slices.Sorted(maps.Keys(sitesCovered(l, names))))))
	for _, site := range slices.Sorted(maps.Keys(unreached)) {
		r.Warn(site+" did not take the deployment lease", errLine(unreached[site])+". A command run from another workstation that reaches only "+site+" is not stopped by this one.")
	}
	runLease = l
	return func() {
		if lost := l.Lost(); lost != nil {
			r.Warn("this run lost the deployment lease", errLine(lost)+". Another command may have changed the deployment while this one ran: run "+progname.Cmd()+" deployment log to see what each site applied.")
		}
		for site, err := range l.Release() {
			r.Warn("the deployment lease was not released on "+site, errLine(err)+". It expires "+lease.TTL.String()+" after this run's last renewal.")
		}
		runLease = nil
	}, nil
}

func sitesCovered(l *lease.Lease, names []string) map[string]bool {
	out := map[string]bool{}
	for _, n := range names {
		if l.Covers(n) {
			out[n] = true
		}
	}
	return out
}

// leaseTopic is the lease, as the help of each command that takes it says.
var leaseTopic = "With --execute it first takes the deployment lease on every site it reaches and holds it to its end, renewing it every " + lease.Heartbeat.String() + ", so no other command changes the deployment while it runs, and then refuses a site a newer paisans last applied. Another command's live lease refuses it, naming who holds it and since when; a lease not renewed for " + lease.TTL.String() + " has expired and is taken. --break-lease takes over a live one once you know its command is no longer running, and asks for the word " + breakWord + "; --yes types it. A dry run takes none."
