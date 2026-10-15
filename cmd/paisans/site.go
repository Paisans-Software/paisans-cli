package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/grant"
	"github.com/paisans-software/paisans-cli/internal/hostarch"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/siteadd"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// runSiteAdd joins a new data site to the running cluster, stage by stage.
//
// It takes no --ssh: it reaches every site, and an override for one would
// leave the rest to their sections anyway, so every site is reached through
// its own ssh section.
//
// Its stage 4 applies the new site's whole render, every app and
// infrastructure image, as apply --site would, so it reviews the new site's
// images as apply --site does (review.go): shown before the first change,
// approved at a terminal with --execute when anything is added or changed,
// checked against the run's approval as a step of apply with no --site, and
// recorded once stage 4's gate has passed.
func runSiteAdd(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("site add", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	secretsPath := fs.String("secrets", "", "path to the secrets (default: secrets.enc.yaml beside the config)")
	execute := fs.Bool("execute", false, "actually run the stages, stopping at the first gate that fails")
	sudo := fs.Bool("sudo", true, "run remote commands through sudo, since /srv and /etc are not the deploy user's")
	// The site may come before or after the flags. flag stops at the first
	// argument that is not one, so a leading name is taken off first.
	var site string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		site, args = args[0], args[1:]
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	r := reporter()
	if site == "" && fs.NArg() > 0 {
		site = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return fmt.Errorf("site add: one site at a time; %q is extra", fs.Arg(0))
	}
	if site == "" {
		return fmt.Errorf("site add: name the site to add, Eg: paisans site add home-b")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	result := validate.Check(cfg)
	reportFindings(r, *configPath, result)
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
	if err := refuseMidRotation(secrets, "site add"); err != nil {
		return err
	}
	if err := secretsgen.CheckGarageKeys(cfg, secrets); err != nil {
		return err
	}
	if err := requireACMEToken(cfg, secrets, cfg.SiteNames()); err != nil {
		return err
	}

	transports := map[string]apply.Transport{}
	for _, name := range cfg.SiteNames() {
		transports[name] = siteTransport(name, cfg.Sites[name], "", *sudo)
	}
	// The site being added is the one whose host the join changes from
	// nothing; every other site already runs the deployment, and each is
	// checked when it is applied.
	if _, ok := cfg.Sites[site]; !ok {
		return fmt.Errorf("site add: %s declares no site %q. Declared sites are %s", *configPath, site, strings.Join(cfg.SiteNames(), ", "))
	}
	// The new site's architecture is recorded as host prepare records it;
	// preflight compares it with the data sites'.
	read, err := readArchitectures(r, cfg, []string{site}, func(name string) hostarch.Runner { return transports[name] })
	if err != nil {
		return err
	}
	if cfg, err = recordArchitectures(r, *configPath, cfg, read, *execute && reviewedByRun == nil); err != nil {
		return err
	}
	// Stage 4 pulls the new site's images only after the mesh, HAProxy and
	// etcd have changed on every site, so an image with no build for its
	// architecture is refused here, before anything is claimed.
	rendered, err := render.Build(cfg, secrets)
	if err != nil {
		return err
	}
	if err := checkPlatforms(r, cfg, rendered, []string{site}); err != nil {
		return err
	}
	host, err := hostGate(r, cfg, site, transports[site])
	if err != nil {
		return err
	}
	// The monitor's stack is applied last, with the new site in its seed,
	// so each monitor site is host checked as apply would check it.
	shared, err := gateSites(r, cfg, cfg.MonitorSites(), func(site string) hostcheck.Transport { return transports[site] })
	if err != nil {
		return err
	}
	// The review comes before the claim, which is the first change: an
	// operator who does not approve leaves every host as it was. Its images
	// are the new site's render, as stage 4 applies it. Keys are host
	// prepare's, which the new site has had already.
	g, err := grant.For(cfg, rendered, site)
	if err != nil {
		return err
	}
	review, approvedBy, err := stepReview(r, stdin, stdout, cfg, site, g, grant.Scope{Images: true}, transports[site], *execute, "paisans site add "+site+" --execute")
	if err != nil {
		return err
	}
	// A dry run shows it here too, ahead of the stages, and asks nothing.
	// As a step of apply without --site there is none: the run shows one for
	// every site.
	pending := 0
	if review != nil && !*execute {
		showReview(r, "review what "+config.FileName+" grants", review.review, false)
		pending = review.review.Pending()
	}
	// Site add writes to every site: the mesh, HAProxy and etcd change on
	// each, not only on the new one.
	if err := claimSites(r, cfg, *execute, *sudo, cfg.SiteNames()...); err != nil {
		return err
	}
	plan, err := ui.Get(r, "read every site", func() (*siteadd.Plan, error) {
		return siteadd.Build(cfg, secrets, site, transports)
	})
	if err != nil {
		return err
	}
	plan.KeepImages = host.Shared()
	plan.SharedSites = shared
	if !*execute || r.Verbose() {
		plan.Show(r)
	}

	if !*execute {
		dryRunFound(count(plan.PendingStages(), "stage"), nil)
		reportOwedApplies(r, plan.Owed)
		if pending > 0 {
			r.Result("Nothing changed. Re-run with --execute to apply; it asks you to type %s for the review above.", approveWord)
			return nil
		}
		r.Result("Nothing changed. Re-run with --execute to apply.")
		return nil
	}
	plan.Report = r
	if review != nil {
		plan.Replicated = func() error { return recordGrant(r, review, cfg.Deployment(), approvedBy) }
	}
	if err := siteadd.Execute(plan); err != nil {
		return err
	}
	reportOwedApplies(r, plan.Owed)
	r.Result("%s joined: every gate passed.", site)
	return nil
}
