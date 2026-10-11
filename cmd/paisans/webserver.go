package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"github.com/paisans-software/paisans-stack/internal/webserver"
)

// webServerProbes is how `web-server check` reaches DNS and the web server. Tests
// replace it.
var webServerProbes = webserver.DefaultProbes

// webServerHost reaches a monitor site to read its web server: as the ssh
// user, and, with sudo, through sudo with the answer every transport to the
// host shares (sudoAuthFor). Inside apply that is the password the run
// settled at its start; run by hand, it is asked once, and only when a
// command needs sudo.
func webServerHost(cfg *config.Config, site string, sudo bool) webserver.Host {
	return webServerHostVia(cfg, site, "", sudo)
}

// webServerHostVia is webServerHost through ssh, verbatim, in place of the
// site's ssh section when it is set, as apply --site --ssh reaches it.
func webServerHostVia(cfg *config.Config, site, ssh string, sudo bool) webserver.Host {
	h := webserver.Host{Plain: siteTransport(site, cfg.Sites[site], ssh, false)}
	if sudo {
		h.Sudo = siteTransport(site, cfg.Sites[site], ssh, true)
	}
	return h
}

// findWebServer finds the web server in front of an app on its monitor
// site, through h, reading only. Tests replace it.
var findWebServer = func(h webserver.Host, t webserver.Target) webserver.WebServer {
	return webserver.Detect(h, t)
}

// runWebServer dispatches `paisans web-server show` and `paisans web-server check`.
// Neither changes anything. With managed_by: external, show reads the
// monitor's web server over ssh to print only the snippet that fits it,
// and check, which looks at the monitor from this machine as a visitor
// would, reads it too when a check fails, to name it.
func runWebServer(args []string) error {
	if len(args) < 1 || (args[0] != "show" && args[0] != "check") {
		return fmt.Errorf("web-server takes one subcommand, show or check: paisans web-server show --app <name>")
	}
	sub := args[0]
	fs := flag.NewFlagSet("web-server "+sub, flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", "paisans.yaml", "path to the deployment declaration")
	appName := fs.String("app", "", "the app pinned to a monitor site, by the name it has in the configuration")
	sudo := fs.Bool("sudo", true, "read the monitor's web server through sudo where the ssh user cannot, as docker and ss may need")
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
			ws, _ = ui.Get(r, "read the web server on "+target.Site, func() (webserver.WebServer, error) {
				return findWebServer(webServerHost(cfg, target.Site, *sudo), target), nil
			})
		}
		// Redacted as the reporter's lines are, since what it found on
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
			ws, _ := ui.Get(r, "read the web server on "+target.Site, func() (webserver.WebServer, error) {
				return findWebServer(webServerHost(cfg, target.Site, *sudo), target), nil
			})
			r.Note(ws.Summary(), "paisans web-server show --app "+target.App+" prints what to add to it")
		}
		return fmt.Errorf("web-server check for %s: fix what is marked FAIL and run it again", target.App)
	}
	r.Result("%s passed.", plural(len(results), "check"))
	return nil
}
