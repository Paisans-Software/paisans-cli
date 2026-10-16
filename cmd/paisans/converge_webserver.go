package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/webserver"
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

// scopedApps is externalApps on the sites scope covers.
func scopedApps(cfg *config.Config, scope webServerScope) []string {
	var out []string
	for _, app := range externalApps(cfg) {
		if site, _ := render.ServedBy(cfg, app); scope.covers(site) {
			out = append(out, app)
		}
	}
	return out
}

// webServerStep is the web server step for the sites scope covers: each
// external app's reference file written (convergeWebServerFiles), then the
// app checked (convergeWebServer). apply without --site runs it once, after
// every other step; apply --site runs it for its own site. It returns what
// the run ends on when something is left: broken when what apply needed on
// a host could not be done, otherwise the operator's web server.
func webServerStep(r ui.Reporter, cfg *config.Config, configPath string, scope webServerScope) (end *ui.Problem, broken bool) {
	files := convergeWebServerFiles(r, cfg, configPath, scope)
	checked, failed := convergeWebServer(r, cfg, files, scope)
	if failed = append(files.failed, failed...); len(failed) > 0 {
		return webServerBroken(failed), true
	}
	if len(checked) > 0 {
		return webServerFailed(checked, files), false
	}
	return nil, false
}

// webServerEnd is how a run ends on what the web server step left, end,
// beside other, the run's own problem if it has one.
func webServerEnd(end *ui.Problem, broken bool, other error) error {
	switch {
	case end == nil:
		return other
	case other != nil:
		return errors.Join(end, other)
	case broken:
		// What apply needed on the host could not be done: that is not
		// the operator's web server, and the run fails.
		return end
	}
	// Every step ran, and what is left is the operator's own web server:
	// the run ends on a warning, not a failure.
	return &ui.Warning{Problem: *end}
}

// convergeWebServer checks each app served through the web server the host's
// owner runs, as `paisans web-server check --app <app>` does, a line per app
// in a section of its own. A failed check is a warning on the app's line,
// with the failing checks, each error said once, and what the owner does:
// install what web-server show prints, or, when this run rewrote the app's
// reference file, replace their copy with it. Where the app's reference
// file step failed, the check only restates it, so it is not run and the
// app has no line of its own but with --verbose; the app counts as failing
// its check either way. Each app is checked once and never waited on:
// nothing apply does makes the owner's server serve it, and web-server
// check is how the operator checks again once the owner has. Only the apps on the sites scope covers are
// checked. It returns the apps that failed their check, and the steps that
// failed outright.
func convergeWebServer(r ui.Reporter, cfg *config.Config, files webServerFiles, scope webServerScope) (warned, failed []string) {
	apps := scopedApps(cfg, scope)
	if len(apps) == 0 {
		return nil, nil
	}
	if !files.headed {
		r.Section("web server")
	}
	for _, app := range apps {
		sf := files.apps[app]
		if sf != nil && sf.until != "" && !r.Verbose() {
			warned = append(warned, app)
			continue
		}
		s := r.Step(app)
		target, err := webserver.For(cfg, app)
		if err != nil {
			s.Fail(err)
			r.Explain(errLine(err))
			failed = append(failed, app)
			continue
		}
		s.Progress("reach " + target.Hostname)
		results := convergeWebServerCheck(target)
		for _, res := range results {
			mark := "✓"
			if !res.OK {
				mark = "✗"
			}
			s.Detail("%s %s: %s", mark, res.Name, res.Detail)
		}
		if passed(results) {
			s.Done(target.Hostname)
			continue
		}
		var ws webserver.WebServer
		if sf != nil {
			ws = sf.ws
		}
		s.End(ui.Warned, target.Hostname)
		warned = append(warned, app)
		checks := failedChecks(results, ws)
		if sf != nil && sf.until != "" {
			// The reference file step said what to do; this follows from it.
			if len(checks) == 1 {
				r.Explain(fmt.Sprintf("%s, expected until %s, see above.", checks[0], sf.until))
			} else {
				r.Explain(fmt.Sprintf("%s\nThese are expected until %s, see above.", strings.Join(checks, "\n"), sf.until))
			}
			continue
		}
		r.Explain(strings.Join(append(checks, ws.Summary(), ownerAdvice(app, sf)), "\n"))
	}
	return warned, failed
}

// ownerAdvice is what the host's owner does for an app whose check failed:
// install what web-server show prints, or replace their copy when this run
// rewrote the reference file.
func ownerAdvice(app string, sf *siteFile) string {
	if sf != nil && sf.changed {
		return fmt.Sprintf("apply rewrote %s, so the host's owner replaces their copy with what paisans web-server show --app %s prints, then run paisans web-server check --app %s.", sf.path, app, app)
	}
	return fmt.Sprintf("The host's owner installs what paisans web-server show --app %s prints in their web server, then run paisans web-server check --app %s.", app, app)
}

// passed reports whether every check passed.
func passed(results []webserver.Result) bool {
	for _, res := range results {
		if !res.OK {
			return false
		}
	}
	return true
}

// httpsDetail is a request's failure as Check words it: the address, then
// the client's error, which repeats it.
var httpsDetail = regexp.MustCompile(`^https://[^/\s]+(/\S*): (?:Get "[^"]*": )?(.*)$`)

// failedChecks is a line per distinct error among the checks that failed,
// naming the checks that met it: a request by its path, any other check by
// its name. Caddy answers a name it has no certificate for with a TLS
// internal error, so behind a Caddy that error says so.
func failedChecks(results []webserver.Result, ws webserver.WebServer) []string {
	var whys []string
	labels := map[string][]string{}
	for _, res := range results {
		if res.OK {
			continue
		}
		label, why := res.Name, res.Detail
		if m := httpsDetail.FindStringSubmatch(res.Detail); m != nil {
			if p := strings.Trim(m[1], "/"); p != "" {
				label = p
			}
			why = m[2]
		}
		if _, ok := labels[why]; !ok {
			whys = append(whys, why)
		}
		labels[why] = append(labels[why], label)
	}
	var out []string
	for _, why := range whys {
		text := strings.TrimPrefix(why, "remote error: ")
		if why == "remote error: tls: internal error" && ws.Server == webserver.ServerCaddy {
			text += " (caddy has no certificate for this name)"
		}
		out = append(out, andList(labels[why])+": "+text)
	}
	return out
}

// webServerFailed is how a run whose every step ran ends when what is left
// is the owner's web server: apps that failed their check. It is not
// complete, since a visitor cannot reach the app as they should. It names,
// per app, what the host's owner does. Alone it is the run's warning;
// beside a failure it is part of it.
func webServerFailed(checked []string, files webServerFiles) *ui.Problem {
	var advice []string
	for _, app := range checked {
		advice = append(advice, ownerAdvice(app, files.apps[app]))
	}
	return &ui.Problem{
		Hint:    fmt.Sprintf("Deployment complete, but %s failed its web server check: %s", plural(len(checked), "app"), andList(checked)),
		Explain: "Every step ran. " + strings.Join(advice, "\n"),
	}
}

// webServerBroken is how a run ends when a web server step failed outright:
// what it needed on the host could not be read or written, or the
// configuration is wrong. That is not the operator's web server, so the
// run fails.
func webServerBroken(steps []string) *ui.Problem {
	return &ui.Problem{
		Hint:    fmt.Sprintf("Deployment not complete: %s failed: %s", plural(len(steps), "web server step"), andList(steps)),
		Explain: "Every other step ran. Fix what is noted above, then run paisans apply --execute again.",
	}
}

// convergeWebServerPending is a dry run's web server section: a line per app
// served through a web server the operator runs, on the sites scope covers,
// which --execute checks once every step has run.
func convergeWebServerPending(r ui.Reporter, cfg *config.Config, scope webServerScope) {
	apps := scopedApps(cfg, scope)
	if len(apps) == 0 {
		return
	}
	r.Section("web server")
	for _, app := range apps {
		r.Step(app).End(ui.Waiting, "checked with --execute")
	}
}
