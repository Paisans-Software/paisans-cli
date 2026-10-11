package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/ingress"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// ingressProbes is how `ingress check` reaches DNS and the web server. Tests
// replace it.
var ingressProbes = ingress.DefaultProbes

// ingressHost reaches a monitor site to read its web server: as the ssh
// user, and, with sudo, through sudo with the answer every transport to the
// host shares (sudoAuthFor). Inside apply that is the password the run
// settled at its start; run by hand, it is asked once, and only when a
// command needs sudo.
func ingressHost(cfg *config.Config, site string, sudo bool) ingress.Host {
	h := ingress.Host{Plain: siteTransport(site, cfg.Sites[site], "", false)}
	if sudo {
		h.Sudo = siteTransport(site, cfg.Sites[site], "", true)
	}
	return h
}

// ingressWebServer finds the web server in front of an app on its monitor
// site, reading only. Tests replace it.
var ingressWebServer = func(cfg *config.Config, t ingress.Target, sudo bool) ingress.WebServer {
	return ingress.Detect(ingressHost(cfg, t.Site, sudo), t)
}

// runIngress dispatches `paisans ingress show` and `paisans ingress check`.
// Neither changes anything. In ingress mode external, show reads the
// monitor's web server over ssh to print only the snippet that fits it,
// and check, which looks at the monitor from this machine as a visitor
// would, reads it too when a check fails, to name it.
func runIngress(args []string) error {
	if len(args) < 1 || (args[0] != "show" && args[0] != "check") {
		return fmt.Errorf("ingress takes one subcommand, show or check: paisans ingress show --app <name>")
	}
	sub := args[0]
	fs := flag.NewFlagSet("ingress "+sub, flag.ContinueOnError)
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
		return fmt.Errorf("ingress %s: unexpected argument(s) %s", sub, strings.Join(fs.Args(), " "))
	}
	if *appName == "" {
		return fmt.Errorf("ingress %s: --app is required", sub)
	}
	cfg, err := loadChecked(r, *configPath)
	if err != nil {
		return err
	}
	target, err := ingress.For(cfg, *appName)
	if err != nil {
		return err
	}
	if sub == "show" {
		// The sheet is the command's product, to be read and copied into a
		// web server's configuration, so it goes to stdout whole and not
		// through the reporter, which hides its details by default.
		if hint, detail, ok := ingress.ListenWarning(target); ok {
			r.Warn(hint, detail)
		}
		var ws ingress.WebServer
		if target.External() {
			ws, _ = ui.Get(r, "read the web server on "+target.Site, func() (ingress.WebServer, error) {
				return ingressWebServer(cfg, target, *sudo), nil
			})
		}
		ingress.Show(os.Stdout, target, ws)
		return nil
	}
	r.Section(fmt.Sprintf("ingress %s at %s", target.App, target.Hostname))
	results, _ := ui.Get(r, "reach "+target.Hostname, func() ([]ingress.Result, error) {
		return ingress.Check(context.Background(), target, ingressProbes()), nil
	})
	if ingress.Report(r, results) {
		if target.External() {
			ws, _ := ui.Get(r, "read the web server on "+target.Site, func() (ingress.WebServer, error) {
				return ingressWebServer(cfg, target, *sudo), nil
			})
			r.Note(ws.Summary(), "paisans ingress show --app "+target.App+" prints what to add to it")
		}
		return fmt.Errorf("ingress check for %s: fix what is marked FAIL and run it again", target.App)
	}
	r.Result("%s passed.", plural(len(results), "check"))
	return nil
}
