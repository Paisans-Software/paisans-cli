package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/acme"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/appremove"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// removeHost is how `app remove` reaches a site. Tests replace it.
var removeHost = func(name string, site config.Site) appremove.Host {
	return siteTransport(name, site, "")
}

// removeClients is Pocket ID's API on site, for `app remove`. It goes
// through clientAPI, the same path `oidc client create` takes.
var removeClients = func(cfg *config.Config, site, key string) appremove.Clients {
	return clientAPI(cfg, site, "", key)
}

// runAppRemove takes an app that has left paisans-stack.yaml off every host. See
// internal/appremove for what it touches and how each thing is proven to be
// the app's, and README's *`app remove` takes an app off its hosts*.
//
// Like every command that changes a host, it plans from what it reads and
// changes nothing without --execute. Deleting member data takes
// --delete-data and the app's name typed at a terminal, because nothing
// brings it back: a command line can sit in a script or a shell history and
// be run again by somebody who did not mean it, and a typed name cannot.
// --yes types it only where the environment sets PAISANS_ALLOW_YES=1, which
// a copied command line does not carry.
func runAppRemove(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("app remove", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	secretsPath := fs.String("secrets", "", "path to the secrets file (default: secrets.enc.yaml beside the config)")
	execute := fs.Bool("execute", false, "actually stop, remove and delete")
	deleteData := fs.Bool("delete-data", false, "also delete the app's database, Garage bucket and keys, named volumes and data directories; asks for the app's name at a terminal")
	answers := yesFlag(fs)
	// The app comes first, as `app remove talk --execute`, or after the flags.
	app := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		app, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if app == "" && fs.NArg() > 0 {
		app = fs.Arg(0)
		if err := fs.Parse(fs.Args()[1:]); err != nil {
			return err
		}
	}
	ans, err := answers()
	if err != nil {
		return err
	}
	// Once every flag is read, those after the app's name included, as
	// the reporter and the login recording take --execute and -v from them.
	r := reporter()
	if fs.NArg() > 0 {
		return fmt.Errorf("app remove takes one app: "+progname.Name()+" app remove <app> [--execute] [--delete-data]. Got extra argument(s): %s", strings.Join(fs.Args(), " "))
	}
	if app == "" {
		return fmt.Errorf("app remove: name the app: %s app remove <app> [--execute] [--delete-data]", progname.Name())
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
	if err := appremove.Refusal(cfg, app); err != nil {
		return err
	}
	// Refused before any host is read, so an unattended run with
	// --delete-data stops with nothing asked of anything.
	if *execute && *deleteData && !ans.canAnswer(stdin) {
		return fmt.Errorf("app remove: --delete-data deletes member data, which nothing brings back, so it asks for the app's name at a terminal and stdin is not one. Run it from an interactive shell, or answer it with --yes and PAISANS_ALLOW_YES=1. Nothing was changed")
	}
	if *secretsPath == "" {
		*secretsPath = filepath.Join(filepath.Dir(*configPath), "secrets.enc.yaml")
	}
	secrets, err := config.LoadDeploymentSecrets(*secretsPath, cfg.ID)
	if err != nil {
		return err
	}
	if err := refuseMidRotation(secrets, "app remove"); err != nil {
		return err
	}

	hosts := map[string]appremove.Host{}
	in := appremove.Input{DeleteData: *deleteData}
	for _, site := range cfg.SiteNames() {
		hosts[site] = removeHost(site, cfg.Sites[site])
		st, err := ui.Get(r, "read "+site, func() (appremove.SiteState, error) {
			return appremove.ProbeSite(cfg, app, site, hosts[site])
		})
		if err != nil {
			return fmt.Errorf("app remove: %w. Every site is read before anything changes, so nothing was changed", err)
		}
		in.Sites = append(in.Sites, st)
	}

	var clients appremove.Clients
	if idp := pocketIDApp(cfg); idp != "" {
		where, err := pocketIDSite(r, cfg, idp, "", "app remove")
		if err != nil {
			return err
		}
		key, _ := secrets.Apps[idp]["static_api_key"].(string)
		if key == "" {
			return fmt.Errorf("app remove: secrets apps.%s.static_api_key is empty, so Pocket ID cannot be asked for %s's client. Nothing was changed", idp, app)
		}
		clients = removeClients(cfg, where, key)
		if in.Client, err = ui.Get(r, "read "+app+"'s client", func() (appremove.ClientState, error) {
			ctx := context.Background()
			return appremove.ProbeClient(ctx, clients, app, cfg.ClientName(app), idp, where, secrets.OIDCClients[app].ClientID)
		}); err != nil {
			return fmt.Errorf("app remove: %w. Nothing was changed", err)
		}
	}
	if *deleteData {
		if err := ui.Run(r, "read "+app+"'s data", func() error {
			var err error
			if in.Database, err = appremove.ProbeDatabase(cfg, app, hosts); err != nil {
				return err
			}
			if len(cfg.Storage.Garage.Sites) > 0 {
				in.Storage, err = appremove.ProbeStorage(cfg, secrets, app, hosts[cfg.Storage.Garage.Sites[0]])
			}
			return err
		}); err != nil {
			return fmt.Errorf("app remove: %w. Nothing was changed", err)
		}
	}

	plan, err := appremove.Build(cfg, app, in)
	if err != nil {
		return err
	}
	// The monitor is reseeded last, from the render without the app, so
	// its checks of the app go with it. Each monitor site is host checked
	// as apply would check it.
	if len(cfg.MonitorSites()) > 0 {
		transports := map[string]apply.Transport{}
		for _, site := range cfg.MonitorSites() {
			transports[site] = hosts[site]
		}
		if _, err := gateSites(r, cfg, cfg.MonitorSites(), func(site string) hostcheck.Transport { return transports[site] }); err != nil {
			return err
		}
		rendered, err := render.Build(cfg, secrets)
		if err != nil {
			return err
		}
		if err := ui.Run(r, "read the monitor", func() error {
			return plan.PlanMonitors(cfg, rendered, acme.Module(cfg.ACME.Provider), transports)
		}); err != nil {
			return err
		}
	}
	// The app, and each group only it named, leave every gateway's
	// deployment record, so secrets prune may remove their secrets.
	deployed, _ := readDeploymentRecord(r, cfg, func(gw string) registry.Runner { return registryHost(gw, cfg.Sites[gw], "") })
	forget := func(execute bool) bool {
		return forgetInRecords(r, cfg, appRecordNames(cfg, app, deployed), app, execute)
	}
	if plan.Empty() {
		recordLeft := forget(*execute)
		where := "on any site or at Pocket ID"
		if *deleteData {
			where = "on any site, at Pocket ID, in Postgres or in Garage"
		}
		reportRemains(r, appremove.Remains(plan, secrets, nil))
		if recordLeft {
			r.Result("Nothing of %s was found %s. Re-run with --execute to take it out of the deployment record.", app, where)
			return nil
		}
		r.Result("Nothing of %s was found %s. Nothing to do.", app, where)
		return nil
	}
	if !*execute || r.Verbose() {
		plan.Show(r)
	}

	touched := map[string]registry.Runner{}
	for _, s := range plan.Sites {
		if !s.Empty() {
			touched[s.Site] = registryHost(s.Site, cfg.Sites[s.Site], "")
		}
	}
	if plan.Client.Delete != nil {
		touched[plan.Client.Site] = registryHost(plan.Client.Site, cfg.Sites[plan.Client.Site], "")
	}
	if db := plan.Database; db != nil && (db.DropDatabase || db.DropRole) {
		touched[db.Leader] = registryHost(db.Leader, cfg.Sites[db.Leader], "")
	}
	if st := plan.Storage; st != nil && (len(st.Buckets) > 0 || len(st.Keys) > 0) {
		touched[st.Anchor] = registryHost(st.Anchor, cfg.Sites[st.Anchor], "")
	}
	for _, m := range plan.Monitors {
		if m.Pending() {
			touched[m.Site] = registryHost(m.Site, cfg.Sites[m.Site], "")
		}
	}
	if _, err := admittedHosts(r, cfg, touched); err != nil {
		return err
	}

	if !*execute {
		forget(false)
		reportRemains(r, appremove.Remains(plan, secrets, nil))
		r.Result("Nothing changed. Re-run with --execute to apply.")
		return nil
	}
	if *deleteData {
		if err := confirmName(ans, stdin, stdout, app); err != nil {
			return err
		}
	}
	runner := &appremove.Executor{Hosts: hosts, Clients: clients, Report: r}
	if err := runner.Execute(plan); err != nil {
		var me *appremove.MonitorError
		if errors.As(err, &me) {
			return fmt.Errorf("app remove: %w", err)
		}
		return fmt.Errorf("app remove: %w\nEvery step is safe to repeat: run the same command again to resume", err)
	}
	forget(true)
	reportRemains(r, appremove.Remains(plan, secrets, runner.Kept))
	r.Result("%s is removed.", app)
	return nil
}

// confirmName asks for the app's name at the terminal and refuses anything
// else. --yes, through a, answers it with the name.
func confirmName(a answerer, stdin io.Reader, stdout io.Writer, app string) error {
	if a.yes {
		return confirmWord(a, stdin, stdout, app, ui.Danger(stdout, fmt.Sprintf("This permanently deletes %s's member data above.", app)))
	}
	if !stdinIsTerminal(stdin) {
		return fmt.Errorf("app remove: --delete-data asks for the app's name at a terminal, and stdin is not one. Run it from an interactive shell, or answer it with --yes and PAISANS_ALLOW_YES=1. Nothing was changed")
	}
	prompt(stdout, ui.Danger(stdout, fmt.Sprintf("This permanently deletes %s's member data above.", app)), app)
	answer, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && answer == "" {
		return fmt.Errorf("app remove: no answer read. Nothing was changed")
	}
	if strings.TrimSpace(answer) != app {
		return fmt.Errorf("app remove: %q is not %s. Nothing was changed", strings.TrimSpace(answer), app)
	}
	return nil
}

// reportRemains tells the operator what a removal leaves for them to see to:
// a secret still in the secrets file, DNS records, data and whatever was kept
// on a host. Each shows without --verbose, the line's first sentence as the
// hint an operator scans for and the rest under it, since the rest names the
// object (which client, which path) and the hint alone cannot be acted on.
// After site remove --execute the configuration no longer declares the site,
// so a second run with --verbose could not show it either.
func reportRemains(r ui.Reporter, lines []string) {
	for _, l := range lines {
		hint, detail, _ := strings.Cut(l, ". ")
		r.Note(hint, detail)
	}
}
