package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/rotatekey"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// runStorageRotateKey replaces one app's Garage S3 key, stage by stage:
// internal/rotatekey. It is modelled on runStorageAdd: it reaches the first
// Garage site and every site running the app through their own ssh sections,
// prints the plan, and changes anything only with --execute.
func runStorageRotateKey(args []string) error {
	fs := newFlagSet("storage rotate-key", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	id := idFlag(fs)
	secretsPath := fs.String("secrets", "", "path to the secrets (default: secrets.enc.yaml beside the config)")
	appName := fs.String("app", "", "the app whose S3 key to replace, by the name it has in the configuration")
	execute := fs.Bool("execute", false, "actually run the stages, stopping at the first gate that fails")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r := reporter()
	if fs.NArg() > 0 {
		return fmt.Errorf("storage rotate-key: name the app with --app. %q is extra", fs.Arg(0))
	}
	if *appName == "" {
		return fmt.Errorf("storage rotate-key: --app is required. A key belongs to one app, and a rotation replaces one at a time")
	}

	if _, err := ensureDraft(*configPath, *id); err != nil {
		return err
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
	if err := refuseMidRotation(secrets, "storage rotate-key"); err != nil {
		return err
	}
	if err := secretsgen.CheckGarageKeys(cfg, secrets); err != nil {
		return err
	}
	// The rotation writes the secrets twice, and an encrypted file with no
	// recipient beside it would be written back in plaintext: refused before
	// anything reaches a host, the same as `secrets set`.
	recipients, err := config.Recipients(filepath.Dir(*secretsPath))
	if err != nil {
		return err
	}
	if secrets.Encrypted && len(recipients) == 0 {
		return config.NoRecipientProblem(*secretsPath)
	}

	transports := map[string]apply.Transport{}
	for _, name := range cfg.SiteNames() {
		transports[name] = siteTransport(name, cfg.Sites[name], "")
	}
	// The switch is an apply of the app on every site it runs on, so each
	// of those is host checked first, as apply checks it.
	sites := append([]string(nil), render.AppSites(cfg)[*appName]...)
	sort.Strings(sites)
	shared, err := gateSites(r, cfg, sites, func(site string) hostcheck.Transport { return transports[site] })
	if err != nil {
		return err
	}
	// Garage's first site, where the key is imported and deleted, and every
	// site the app runs on, where the switch applies it.
	if len(cfg.Storage.Garage.Sites) > 0 {
		if _, err := admittedSites(r, cfg, union(cfg.Storage.Garage.Sites[:1], render.AppSites(cfg)[*appName])...); err != nil {
			return err
		}
	}
	plan, err := ui.Get(r, "read "+*appName+"'s key", func() (*rotatekey.Plan, error) {
		return rotatekey.Build(rotatekey.Options{
			App:        *appName,
			Config:     cfg,
			Secrets:    secrets,
			Transports: transports,
			Switch:     applySwitch{cfg: cfg, app: *appName, transports: transports, shared: shared},
			Save: func(s *config.Secrets) error {
				return config.WriteSecrets(*secretsPath, s, recipients)
			},
		})
	})
	if err != nil {
		return err
	}
	if !*execute || r.Verbose() {
		plan.Show(r)
	}

	if !*execute {
		r.Result("Nothing changed. Re-run with --execute to apply.")
		return nil
	}
	plan.Report = r
	if err := rotatekey.Execute(plan); err != nil {
		return err
	}
	r.Result("%s now uses %s, and %s is deleted from Garage.", *appName, plan.NewKeyID, plan.OldKeyID)
	return nil
}

// scopedApplyPlan is `paisans deployment apply --site <site> --only <stack>...`
// planned with secrets, for a rotation's switch: the same plan, refusal on a
// host edit and health gate, scoped to stacks. A rotation rewrites an
// environment, not an image: no resolver, so each site keeps the build it
// runs rather than moving an app to a new one in the middle of a rotation.
// keepImages keeps superseded images too, as apply does on a shared host.
func scopedApplyPlan(cfg *config.Config, secrets *config.Secrets, site string, t apply.Transport, keepImages bool, stacks ...string) (*apply.Plan, error) {
	options := []apply.Option{apply.Only(stacks...), apply.ResolveImages(nil)}
	if keepImages {
		options = append(options, apply.KeepImages())
	}
	return planSiteApply(cfg, secrets, site, t, options...)
}

// applySwitch is rotatekey.Switch as `paisans deployment apply --site <site> --only
// <app>`: the same plan, the same refusal on a host edit, the same health
// gate.
type applySwitch struct {
	cfg        *config.Config
	app        string
	transports map[string]apply.Transport
	// shared is the host check's finding per site: a shared site keeps its
	// images, as apply does there.
	shared map[string]bool
}

func (a applySwitch) plan(site string, secrets *config.Secrets) (*apply.Plan, error) {
	plan, err := scopedApplyPlan(a.cfg, secrets, site, a.transports[site], a.shared[site], a.app)
	if err != nil {
		return nil, err
	}
	if c := plan.Conflicts(); len(c) > 0 {
		return nil, fmt.Errorf("%s's %s differs from what the last apply recorded, so somebody edited it on the host. Nothing was changed on %s. Restore it, or run `%s deployment apply --site %s --only %s --overwrite %s`, then run rotate-key again", site, c[0].Path, site, progname.Cmd(), site, a.app, c[0].Path)
	}
	return plan, nil
}

func (a applySwitch) Pending(site string, secrets *config.Secrets) ([]string, error) {
	plan, err := a.plan(site, secrets)
	if err != nil {
		return nil, err
	}
	var pending []string
	for _, c := range plan.Writes() {
		pending = append(pending, c.Kind.String()+" "+c.Path)
	}
	for _, action := range plan.Actions {
		verb := "restart"
		if action.Recreate || action.Force {
			verb = "recreate"
		}
		pending = append(pending, verb+" "+action.Stack)
	}
	return pending, nil
}

func (a applySwitch) Apply(site string, secrets *config.Secrets, r ui.Reporter) error {
	plan, err := a.plan(site, secrets)
	if err != nil {
		return err
	}
	plan.Report = r
	return apply.Execute(plan, a.transports[site])
}
