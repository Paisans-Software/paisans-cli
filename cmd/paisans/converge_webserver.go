package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

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

// webServerNow and webServerSleep are the clock a web server check waits on.
// Tests replace them.
var (
	webServerNow   = time.Now
	webServerSleep = time.Sleep
)

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
// external app's site file written (convergeWebServerFiles), then the app
// checked (convergeWebServer). apply without --site runs it once, after
// every other step; apply --site runs it for its own site. It returns what
// the run ends on when something is left: broken when what apply needed on
// a host could not be done, otherwise the operator's web server.
func webServerStep(r ui.Reporter, cfg *config.Config, configPath string, scope webServerScope) (end *ui.Problem, broken bool) {
	files := convergeWebServerFiles(r, cfg, configPath, scope)
	checked, failed := convergeWebServer(r, cfg, files, scope)
	// An app whose site file step warned is named once, by the app.
	warned := slices.Clone(checked)
	for _, title := range files.warned {
		if name := strings.TrimPrefix(title, "site file for "); !slices.Contains(warned, name) {
			warned = append(warned, name)
		}
	}
	if failed = append(files.failed, failed...); len(failed) > 0 {
		return webServerBroken(failed), true
	}
	if len(warned) > 0 {
		return webServerFailed(checked, warned), false
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

// convergeWebServer checks each app served through a web server the operator
// runs, as `paisans web-server check --app <app>` does, a line per app in a
// section of its own. A failed check is a warning on the app's line, with the
// failing checks, each error said once, and one set of instructions. Where
// the app's site file step warned or failed, the check only restates it, so
// it is not run and the app has no line of its own but with --verbose,
// where it gives no instructions of its own; the app counts as failing its
// check either way. Where the site file is in
// place and unchanged, the app is checked once and, when that fails, the
// server's configuration is validated, and a server in a container
// reloaded and the app checked again, which takes in an edit of the
// operator's. Otherwise a check that may still come is waited on, as
// webServerResults says. Only the apps on the sites scope
// covers are checked. It returns the apps that failed their check, and the
// steps that failed outright.
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
		// A site file in place and unchanged is checked once, then
		// reloaded and waited on; one whose step warned fails until the
		// operator acts, so is not waited on.
		again := sf != nil && sf.until == "" && sf.path != "" && !sf.reloaded
		var results []webserver.Result
		var waited time.Duration
		if again || (sf != nil && sf.until != "") {
			results = webServerResultsOnce(s, target)
		} else {
			results, waited = webServerResults(s, target, sf)
		}
		var advice string
		if !passed(results) && again {
			advice, waited = reloadAgain(s, sf, target, &results)
		}
		for _, res := range results {
			mark := "✓"
			if !res.OK {
				mark = "✗"
			}
			s.Detail("%s %s: %s", mark, res.Name, res.Detail)
		}
		if passed(results) {
			if waited >= time.Second {
				s.Done(fmt.Sprintf("%s, after %ds", target.Hostname, int(waited.Seconds())))
			} else {
				s.Done(target.Hostname)
			}
			continue
		}
		var ws webserver.WebServer
		if sf != nil && sf.ws.Server != "" {
			ws = sf.ws
		} else if sf == nil || sf.until == "" {
			// What serves it is read only now, as the ssh user, so a
			// passing app costs no ssh. The line
			// spins while it is read.
			s.Progress("read the web server on " + target.Site)
			ws = findWebServer(scope.serverHost(cfg, target.Site), target)
		}
		s.End(ui.Warned, target.Hostname)
		warned = append(warned, app)
		checks := failedChecks(results, ws)
		if sf != nil && sf.until != "" {
			// The site file step said what to do; this follows from it.
			if len(checks) == 1 {
				r.Explain(fmt.Sprintf("%s, expected until %s, see above.", checks[0], sf.until))
			} else {
				r.Explain(fmt.Sprintf("%s\nThese are expected until %s, see above.", strings.Join(checks, "\n"), sf.until))
			}
			continue
		}
		if advice == "" {
			advice = fmt.Sprintf("Add what paisans web-server show --app %s prints to the web server in front of it, then run paisans web-server check --app %s.", app, app)
			if sf != nil && sf.path != "" {
				advice = fmt.Sprintf("Its site file, %s, is in place, and %s runs it. Read %s's log for why it does not serve %s, then run paisans web-server check --app %s.", sf.path, ws.Server, ws.Server, target.Hostname, app)
			}
		}
		r.Explain(strings.Join(append(checks, ws.Summary(), advice), "\n"))
	}
	return warned, failed
}

// reloadAgain validates the configuration of the server in front of an app
// whose site file is in place and unchanged, but whose check failed: the
// server may run a configuration older than an edit of the operator's. A
// server in a container is reloaded and the app checked once more, waiting
// as after any reload; one on the host itself is the operator's to reload.
// It returns the advice for a check that still fails, empty for the usual,
// and how long the check waited.
func reloadAgain(s ui.Step, sf *siteFile, target webserver.Target, results *[]webserver.Result) (string, time.Duration) {
	s.Progress("validate " + string(sf.ws.Server))
	reload, err := webserver.ValidateAndReload(sf.h, sf.ws)
	switch {
	case err != nil:
		return fmt.Sprintf("%s\nNothing was reloaded. Fix the configuration, then run paisans apply --execute again.", err), 0
	case reload.Kind == webserver.OperatorReloads:
		return fmt.Sprintf("%s's configuration is valid, and %s runs on the host itself, so apply leaves its reload to you. If it has not been reloaded since its site file was written, reload it:\n\n    %s\n\nthen run paisans web-server check --app %s.", sf.ws.Server, sf.ws.Server, reload.Command, target.App), 0
	case reload.Kind == webserver.Restarted:
		s.Detail("%s's reload failed, so its container was restarted: %s", sf.ws.Server, reload.Why)
	default:
		s.Detail("%s validated and reloaded", sf.ws.Server)
	}
	var waited time.Duration
	*results, waited = webServerResults(s, target, sf)
	return "", waited
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

// webServerWait is how long a web server check waits, at most, for what a
// just reloaded web server may not serve yet; webServerFirstPause is its
// first pause between two checks, doubled after each.
const (
	webServerWait       = time.Minute
	webServerFirstPause = 2 * time.Second
)

// webServerResults checks target, and while what fails is only what a web
// server obtaining a certificate fails, checks it again after a pause that
// doubles each time, for webServerWait in all. Waiting cannot fix any other
// failure, such as a name that does not resolve, so that ends it at once.
// The step says while it waits what for. It returns the last check's
// results and how long it waited.
func webServerResults(s ui.Step, target webserver.Target, sf *siteFile) ([]webserver.Result, time.Duration) {
	start := webServerNow()
	pause := webServerFirstPause
	for {
		results := webServerResultsOnce(s, target)
		waited := webServerNow().Sub(start)
		if !mayStillCome(results) || waited >= webServerWait {
			return results, waited
		}
		s.Progress(waitingFor(results, sf))
		webServerSleep(min(pause, webServerWait-waited))
		pause *= 2
	}
}

// webServerResultsOnce checks target once.
func webServerResultsOnce(s ui.Step, target webserver.Target) []webserver.Result {
	s.Progress("reach " + target.Hostname)
	return convergeWebServerCheck(target)
}

// mayStillCome reports whether results failed, and only on the checks a
// certificate the web server is still obtaining fails: the certificate and
// the sign in redirect.
func mayStillCome(results []webserver.Result) bool {
	failed := false
	for _, res := range results {
		if res.OK {
			continue
		}
		if res.Name != webserver.CheckCertificate && res.Name != webserver.CheckSignIn {
			return false
		}
		failed = true
	}
	return failed
}

// waitingFor is what a check that may still come waits for, as its step
// says it: the certificate, of the web server in front when it is known,
// or the sign in redirect when that alone fails.
func waitingFor(results []webserver.Result, sf *siteFile) string {
	for _, res := range results {
		if !res.OK && res.Name == webserver.CheckCertificate {
			if sf != nil && sf.ws.Server != "" {
				return fmt.Sprintf("waiting for %s's certificate", sf.ws.Server)
			}
			return "waiting for the certificate"
		}
	}
	return "waiting for the sign in redirect"
}

// webServerFailed is how a run whose every step ran ends when what is left
// is the operator's web server: apps that failed their check, or a site
// file step that warned. It is not complete, since a visitor cannot reach
// the app as they should, or will not once the server restarts. Alone it
// is the run's warning; beside a failure it is part of it.
func webServerFailed(checked, warned []string) *ui.Problem {
	if len(warned) == len(checked) {
		return &ui.Problem{
			Hint:    fmt.Sprintf("Deployment complete, but %s failed its web server check: %s", plural(len(checked), "app"), andList(checked)),
			Explain: "Every step ran. Fix each app's web server as noted above, then run paisans web-server check --app <app> to check it again.",
		}
	}
	return &ui.Problem{
		Hint:    "Deployment complete, but the web server needs you: " + andList(warned),
		Explain: "Every step ran. Do what is noted above for each, then run paisans web-server check --app <app> to check each app again.",
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
