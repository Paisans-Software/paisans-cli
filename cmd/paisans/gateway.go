package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/gatewaymove"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// gatewayMoveOptions is how a gateway move reaches the DNS provider and the
// new gateway. Tests replace it.
var gatewayMoveOptions = func() gatewaymove.Options {
	return gatewaymove.Options{DNSProvider: dnsProviderFor}
}

// gatewayTransport is how a gateway move reaches a site. Tests replace it.
var gatewayTransport = func(name string, site config.Site) apply.Transport {
	return siteTransport(name, site, "")
}

// runGateway dispatches `paisans gateway move`.
func runGateway(args []string) error {
	if len(args) < 1 || args[0] != "move" {
		return fmt.Errorf("gateway takes one subcommand, move: paisans gateway move [--execute]")
	}
	return runGatewayMove(args[1:])
}

// runGatewayMove moves the gateway role to the site paisans-stack.yaml gives it,
// from the site that still serves as the gateway, in gated stages (README,
// "Moving the gateway"). It takes no site: the new gateway is the
// configuration's, and the old one is found from what it still runs.
func runGatewayMove(args []string) error {
	return referProvider(gatewayMoveCommand(args))
}

// gatewayMoveCommand is runGatewayMove before a provider's refusal of the
// token is worded as the token check words it.
func gatewayMoveCommand(args []string) error {
	fs := newFlagSet("gateway move", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	secretsPath := fs.String("secrets", "", "path to the secrets (default: secrets.enc.yaml beside the config)")
	execute := fs.Bool("execute", false, "actually run the stages, stopping at the first gate that fails")
	keepImages := fs.Bool("keep-images", false, "passed to the hand-off's apply of a site that keeps the monitor")
	minFree := fs.String("min-free", "", "passed to the hand-off's apply of a site that keeps the monitor")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	r := reporter()
	if fs.NArg() > 0 {
		return fmt.Errorf("gateway move takes no site: the new gateway is the one " + config.FileName + " gives the role, and the old one is found from what it still runs")
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
		transports[name] = gatewayTransport(name, cfg.Sites[name])
	}
	options := gatewayMoveOptions()
	flags := handOffFlags{config: *configPath, secrets: *secretsPath, verbose: r.Verbose(), keepImages: *keepImages, minFree: *minFree}
	if options.ApplySite == nil {
		// The hand-off off a site that keeps the monitor applies its infra
		// stack as typed by hand, with this move's own files and options, so
		// it claims, reviews and gates exactly as apply does. Only an executed
		// move reaches it, after its dry run has passed before the first
		// stage.
		options.ApplySite = func(site string) error {
			return runApply(handOffArgs(site, flags, true), os.Stdin, os.Stdout)
		}
		options.CheckSite = func(site string) error {
			return runApply(handOffArgs(site, flags, false), os.Stdin, os.Stdout)
		}
	}
	plan, err := ui.Get(r, "find the gateway to move from", func() (*gatewaymove.Plan, error) {
		return gatewaymove.Build(cfg, secrets, transports, options)
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
	if err := claimSites(r, cfg, *execute, plan.From, plan.To); err != nil {
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

// handOffFlags are the move's own options that its hand-off's apply takes.
type handOffFlags struct {
	config, secrets     string
	verbose, keepImages bool
	minFree             string
}

// handOffArgs is apply --site <site> --only infra with the move's own
// options, executed or as its dry run.
func handOffArgs(site string, f handOffFlags, execute bool) []string {
	args := []string{"--site", site, "--only", "infra", "--config", f.config}
	if f.secrets != "" {
		args = append(args, "--secrets", f.secrets)
	}
	if f.verbose {
		args = append(args, "-v")
	}
	if f.keepImages {
		args = append(args, "--keep-images")
	}
	if f.minFree != "" {
		args = append(args, "--min-free", f.minFree)
	}
	if execute {
		args = append(args, "--execute")
	}
	return args
}
