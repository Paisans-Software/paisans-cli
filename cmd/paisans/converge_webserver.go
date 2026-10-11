package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"github.com/paisans-software/paisans-stack/internal/webserver"
)

// convergeWebServerCheck runs `paisans web-server check`'s checks for one target,
// from this machine, with the same probes. Tests replace it.
var convergeWebServerCheck = func(t webserver.Target) []webserver.Result {
	return webserver.Check(context.Background(), t, webServerProbes())
}

// externalApps is every app served through a web server the operator runs:
// pinned to a monitor site with managed_by: external.
func externalApps(cfg *config.Config) []string {
	var out []string
	for _, app := range cfg.AppNames() {
		if _, _, ok := render.ExternalListen(cfg, app); ok {
			out = append(out, app)
		}
	}
	return out
}

// convergeWebServer checks each app served through a web server the operator
// runs, as `paisans web-server check --app <app>` does, a line per app in a
// section of its own. A failed check is a warning naming what failed, with
// the failing checks' own lines, the web server found in front of the app
// and how to fix it. It returns the apps that failed. headed is whether the
// section is already open, with the steps writing the apps' site files.
func convergeWebServer(r ui.Reporter, cfg *config.Config, sudo, headed bool) []string {
	apps := externalApps(cfg)
	if len(apps) == 0 {
		return nil
	}
	if !headed {
		r.Section("web server")
	}
	var failed []string
	for _, app := range apps {
		s := r.Step(app)
		advice := fmt.Sprintf("Add what paisans web-server show --app %s prints to the web server in front of it, then run paisans web-server check --app %s.", app, app)
		target, err := webserver.For(cfg, app)
		if err != nil {
			s.Fail(err)
			failed = append(failed, app)
			r.Note(app+": could not check its web server ("+errLine(err)+")", advice)
			continue
		}
		results := webServerResults(s, target)
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
		ws := findWebServer(cfg, target, sudo)
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

// webServerResults checks target once. The web server in front of it is the
// operator's, so a check that fails is reported as it stands.
func webServerResults(s ui.Step, target webserver.Target) []webserver.Result {
	s.Progress("reach " + target.Hostname)
	return convergeWebServerCheck(target)
}

// webServerFailed is how a run whose every step ran ends when an app failed
// its web server check: not complete, since a visitor cannot reach it as they
// should. Alone it is the run's warning; beside a failure it is part of it.
func webServerFailed(apps []string) *ui.Problem {
	return &ui.Problem{
		Hint:    fmt.Sprintf("Deployment complete, but %s failed its web server check: %s", plural(len(apps), "app"), andList(apps)),
		Explain: "Every step ran. Fix each app's web server as noted above, then run paisans web-server check --app <app> to check it again.",
	}
}

// convergeWebServerPending is a dry run's web server section: a line per app
// served through a web server the operator runs, which --execute checks
// once every step has run.
func convergeWebServerPending(r ui.Reporter, cfg *config.Config) {
	apps := externalApps(cfg)
	if len(apps) == 0 {
		return
	}
	r.Section("web server")
	for _, app := range apps {
		r.Step(app).End(ui.Waiting, "checked with --execute")
	}
}
