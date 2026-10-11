package main

import (
	"flag"
	"fmt"
	"path/filepath"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/gatewaymove"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

// gatewayMoveOptions is how a gateway move reaches the DNS provider and the
// new gateway. Tests replace it.
var gatewayMoveOptions = func() gatewaymove.Options {
	return gatewaymove.Options{DNSProvider: dnsProviderFor}
}

// gatewayTransport is how a gateway move reaches a site. Tests replace it.
var gatewayTransport = func(name string, site config.Site, sudo bool) apply.Transport {
	return siteTransport(name, site, "", sudo)
}

// runGateway dispatches `paisans gateway move`.
func runGateway(args []string) error {
	if len(args) < 1 || args[0] != "move" {
		return fmt.Errorf("gateway takes one subcommand, move: paisans gateway move [--execute]")
	}
	return runGatewayMove(args[1:])
}

// runGatewayMove moves the gateway role to the site paisans.yaml gives it,
// from the site that still serves as the gateway, in gated stages (README,
// "Moving the gateway"). It takes no site: the new gateway is the
// configuration's, and the old one is found from what it still runs.
func runGatewayMove(args []string) error {
	fs := flag.NewFlagSet("gateway move", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", "paisans.yaml", "path to the deployment declaration")
	secretsPath := fs.String("secrets", "", "path to the secrets (default: secrets.enc.yaml beside the config)")
	execute := fs.Bool("execute", false, "actually run the stages, stopping at the first gate that fails")
	sudo := fs.Bool("sudo", true, "run remote commands through sudo, since /var/lib/paisans is root's")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	r := reporter()
	if fs.NArg() > 0 {
		return fmt.Errorf("gateway move takes no site: the new gateway is the one paisans.yaml gives the role, and the old one is found from what it still runs")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	result := validate.Check(cfg)
	if !insideApply {
		reportFindings(r, *configPath, result)
	}
	if result.Refused() {
		return refused(*configPath, len(result.Refusals()), "")
	}
	if *secretsPath == "" {
		*secretsPath = filepath.Join(filepath.Dir(*configPath), "secrets.enc.yaml")
	}
	secrets, err := config.LoadDeploymentSecrets(*secretsPath, cfg.ID)
	if err != nil {
		return err
	}
	if !secrets.Encrypted {
		warnUnencrypted(r, *secretsPath)
	}
	transports := map[string]apply.Transport{}
	for _, name := range cfg.SiteNames() {
		transports[name] = gatewayTransport(name, cfg.Sites[name], *sudo)
	}
	plan, err := ui.Get(r, "find the gateway to move from", func() (*gatewaymove.Plan, error) {
		return gatewaymove.Build(cfg, secrets, transports, gatewayMoveOptions())
	})
	if err != nil {
		return err
	}
	if plan.From == "" {
		dryRunFound("", nil)
		if !*execute || r.Verbose() {
			plan.Show(r)
		}
		if !insideApply {
			r.Result("%s is the only gateway. Nothing to move.", plan.To)
		}
		return nil
	}
	// The move writes to both gateways' hosts: the record and the marker,
	// and the old Caddy stopped.
	if err := claimSites(r, cfg, *execute, *sudo, plan.From, plan.To); err != nil {
		return err
	}
	if !*execute || r.Verbose() {
		plan.Show(r)
	}
	reportRemains(r, plan.Notes)
	if !*execute {
		dryRunFound("move the gateway from "+plan.From, nil)
		r.Result("Nothing changed. Re-run with --execute to move the gateway from %s to %s.", plan.From, plan.To)
		return nil
	}
	plan.Report = r
	if err := gatewaymove.Execute(plan); err != nil {
		return err
	}
	if !insideApply {
		r.Result("The gateway moved from %s to %s: every gate passed.", plan.From, plan.To)
	}
	return nil
}
