package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/webserver"
)

// webServerProbes is how `web-server check` reaches DNS and the web server. Tests
// replace it.
var webServerProbes = webserver.DefaultProbes

// webServerRecordHost reaches a monitor site to read its admission record,
// as its login: the deployment's own user, unless ssh.user names another.
// Tests replace it.
var webServerRecordHost = func(cfg *config.Config, site string) admission.Reader {
	return siteTransport(site, cfg.Sites[site], "")
}

// recordedWebServer is the web server site admit found in front of the
// target's site, as its admission record holds it.
func recordedWebServer(cfg *config.Config, target webserver.Target) (webserver.WebServer, error) {
	rec, err := admission.Read(webServerRecordHost(cfg, target.Site), cfg.Deployment().Token())
	if err != nil {
		return webserver.WebServer{}, admissionProblem(target.Site, cfg.Deployment().Token(), err)
	}
	return webserver.FromRecord(rec.WebServer), nil
}

// runWebServer dispatches `paisans web-server show` and `paisans web-server check`.
// Neither changes anything. With managed_by: external, show reads the
// site's admission record over ssh, which holds the web server site admit
// found, to print only the snippet that fits it, and check, which looks at
// the monitor from this machine as a visitor would, reads it too when a
// check fails, to name it.
func runWebServer(args []string) error {
	if len(args) < 1 || (args[0] != "show" && args[0] != "check") {
		return fmt.Errorf("web-server takes one subcommand, show or check: paisans web-server show --app <name>")
	}
	sub := args[0]
	fs := newFlagSet("web-server "+sub, flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	appName := fs.String("app", "", "the app pinned to a monitor site, by the name it has in the configuration")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	r := reporter()
	if sub == "show" {
		// stdout carries the sheet alone, so what validate and the sheet's
		// own warning have to say goes to stderr.
		r = errReporter(r)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("web-server %s: unexpected argument(s) %s", sub, strings.Join(fs.Args(), " "))
	}
	if *appName == "" {
		return fmt.Errorf("web-server %s: --app is required", sub)
	}
	cfg, err := loadChecked(r, *configPath)
	if err != nil {
		return err
	}
	target, err := webserver.For(cfg, *appName)
	if err != nil {
		return err
	}
	if sub == "show" {
		// The sheet is the command's product, to be read and copied into a
		// web server's configuration, so it goes to stdout whole and not
		// through the reporter, which hides its details by default.
		if hint, detail, ok := webserver.ListenWarning(target); ok {
			r.Warn(hint, detail)
		}
		var ws webserver.WebServer
		if target.External() {
			ws, err = ui.Get(r, "read the admission record of "+target.Site, func() (webserver.WebServer, error) {
				return recordedWebServer(cfg, target)
			})
			if err != nil {
				return err
			}
			if hint, detail, ok := webserver.ACMEDNSWarning(target, ws); ok {
				r.Warn(hint, detail)
			}
		}
		// Redacted as the reporter's lines are, since what admit found on
		// the host is printed with the sheet.
		webserver.Show(ui.RedactWriter(os.Stdout), target, ws)
		return nil
	}
	r.Section(fmt.Sprintf("web-server %s at %s", target.App, target.Hostname))
	results, _ := ui.Get(r, "reach "+target.Hostname, func() ([]webserver.Result, error) {
		return webserver.Check(context.Background(), target, webServerProbes()), nil
	})
	if webserver.Report(r, results) {
		if target.External() {
			if ws, err := ui.Get(r, "read the admission record of "+target.Site, func() (webserver.WebServer, error) {
				return recordedWebServer(cfg, target)
			}); err == nil {
				r.Note(ws.Summary(), "paisans web-server show --app "+target.App+" prints what the host's owner installs in it")
			}
		}
		return fmt.Errorf("web-server check for %s: fix what is marked FAIL and run it again", target.App)
	}
	r.Result("%s passed.", plural(len(results), "check"))
	return nil
}
