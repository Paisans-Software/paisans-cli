package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/ingress"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// convergeIngressCheck runs `paisans ingress check`'s checks for one target,
// from this machine, with the same probes. Tests replace it.
var convergeIngressCheck = func(t ingress.Target) []ingress.Result {
	return ingress.Check(context.Background(), t, ingressProbes())
}

// convergeSleep is the wait between two attempts at an ingress check. Tests
// replace it.
var convergeSleep = time.Sleep

// ingressAttempts and ingressPause are how long an ingress check that may
// still come right is tried: about half a minute, for an app that has just
// started or a certificate the web server is still obtaining.
const (
	ingressAttempts = 7
	ingressPause    = 5 * time.Second
)

// externalApps is every app served through a web server the operator runs:
// pinned to a monitor site in ingress mode external.
func externalApps(cfg *config.Config) []string {
	var out []string
	for _, app := range cfg.AppNames() {
		if _, _, ok := render.ExternalListen(cfg, app); ok {
			out = append(out, app)
		}
	}
	return out
}

// convergeIngress checks each app served through a web server the operator
// runs, as `paisans ingress check --app <app>` does, a line per app in a
// section of its own. A failed check is a warning naming what failed, with
// the failing checks' own lines, the web server found in front of the app
// and how to fix it. It returns the apps that failed.
func convergeIngress(r ui.Reporter, cfg *config.Config, sudo bool) []string {
	apps := externalApps(cfg)
	if len(apps) == 0 {
		return nil
	}
	r.Section("ingress")
	var failed []string
	for _, app := range apps {
		s := r.Step(app)
		advice := fmt.Sprintf("Add what paisans ingress show --app %s prints to the web server in front of it, then run paisans ingress check --app %s.", app, app)
		target, err := ingress.For(cfg, app)
		if err != nil {
			s.Fail(err)
			failed = append(failed, app)
			r.Note(app+": could not check its ingress ("+errLine(err)+")", advice)
			continue
		}
		results := ingressResults(s, target)
		var names, lines []string
		for _, res := range results {
			mark := "✓"
			if !res.OK {
				mark = "✗"
				names = append(names, res.Name)
				lines = append(lines, res.Detail)
			}
			s.Detail("%s %s: %s", mark, res.Name, res.Detail)
		}
		if len(names) == 0 {
			s.Done(target.Hostname)
			continue
		}
		// What serves it is read only now, through the sudo the run
		// settled, so a passing app costs no ssh. The line spins while
		// it is read.
		s.Progress("read the web server on " + target.Site)
		ws := ingressWebServer(cfg, target, sudo)
		s.End(ui.Failed, target.Hostname)
		failed = append(failed, app)
		checks := "check"
		if len(names) > 1 {
			checks += "s"
		}
		r.Note(fmt.Sprintf("%s: the %s %s failed", app, andList(names), checks), strings.Join(append(lines, ws.Summary(), advice), "\n"))
	}
	return failed
}

// ingressResults checks target, and checks it again, a few seconds apart,
// while what fails may be only the app or its certificate not being ready
// yet. A failure that waiting cannot fix, such as a name that does not
// resolve, ends it at once.
func ingressResults(s ui.Step, target ingress.Target) []ingress.Result {
	s.Progress("reach " + target.Hostname)
	for attempt := 1; ; attempt++ {
		results := convergeIngressCheck(target)
		if attempt == ingressAttempts || !mayStillCome(results) {
			return results
		}
		s.Progress(fmt.Sprintf("reach %s, again (%d of %d)", target.Hostname, attempt+1, ingressAttempts))
		convergeSleep(ingressPause)
	}
}

// mayStillCome reports whether results failed, and only on the checks an
// app or certificate that is not ready yet fails: the certificate and the
// sign in redirect.
func mayStillCome(results []ingress.Result) bool {
	failed := false
	for _, res := range results {
		if res.OK {
			continue
		}
		if res.Name != ingress.CheckCertificate && res.Name != ingress.CheckSignIn {
			return false
		}
		failed = true
	}
	return failed
}

// ingressFailed is how a run whose every step ran ends when an app failed
// its ingress check: not complete, since a visitor cannot reach it as they
// should.
func ingressFailed(apps []string) error {
	return &ui.Problem{
		Hint:    fmt.Sprintf("Deployment complete, but %s failed its ingress check: %s", plural(len(apps), "app"), andList(apps)),
		Explain: "Every step ran. Fix each app's web server as noted above, then run paisans ingress check --app <app> to check it again.",
	}
}

// convergeIngressPending is a dry run's ingress section: a line per app
// served through a web server the operator runs, which --execute checks
// once every step has run.
func convergeIngressPending(r ui.Reporter, cfg *config.Config) {
	apps := externalApps(cfg)
	if len(apps) == 0 {
		return
	}
	r.Section("ingress")
	for _, app := range apps {
		r.Step(app).End(ui.Waiting, "checked with --execute")
	}
}
