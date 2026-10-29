package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/paisans-software/pocketid-go"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/oidcclient"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// oidcTransport is how `oidc client create` reaches the identity provider's
// host. Tests replace it. It never uses sudo: curl needs no root.
var oidcTransport = func(t apply.SSHTransport) pocketid.Transport { return t }

// runOIDC dispatches `paisans oidc <subcommand>`.
func runOIDC(args []string) error {
	if len(args) < 2 || args[0] != "client" || args[1] != "create" {
		return fmt.Errorf("oidc takes one subcommand, client create: %s oidc client create --app <name> [--execute]", progname.Cmd())
	}
	return runOIDCClientCreate(args[2:])
}

// runOIDCClientCreate creates an app's client at the deployment's Pocket ID
// and records its credentials in the secrets file.
//
// Every step is a Pocket ID mutation a human approves, so the command prints
// each one with what it sends and changes nothing without --execute. The
// client secret is generated here, written into the secrets file, and only
// then sent; it is never printed. `paisans deployment apply` then renders it into the
// app like any other secret.
func runOIDCClientCreate(args []string) error {
	fs := newFlagSet("oidc client create", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	id := idFlag(fs)
	secretsPath := fs.String("secrets", "", "path to the secrets file (default: secrets.enc.yaml beside the config)")
	appName := fs.String("app", "", "the app the client is for, by the name it has in the configuration")
	rotate := fs.Bool("rotate-secret", false, "add a new secret even when the recorded one is live; the old one stays valid until deleted")
	site := fs.String("site", "", "the site whose Pocket ID to call (default: its pinned site, or the apps site whose instance is active)")
	destination := fs.String("ssh", "", "ssh destination, used verbatim in place of the site's ssh section (its user, host, port and keys are then ignored)")
	execute := fs.Bool("execute", false, "actually create and record")
	breakLease := breakLeaseFlag(fs)
	answers := yesFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r := reporter()
	if fs.NArg() > 0 {
		return fmt.Errorf("oidc client create: unexpected argument(s) %s", strings.Join(fs.Args(), " "))
	}
	if *appName == "" {
		return fmt.Errorf("oidc client create: --app is required")
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
	app, ok := cfg.Apps[*appName]
	if !ok {
		return fmt.Errorf("oidc client create: %s declares no app %q. Declared apps are %s", *configPath, *appName, strings.Join(cfg.AppNames(), ", "))
	}
	desired, ok := clientDesired(cfg, *appName, *rotate)
	if !ok {
		return fmt.Errorf("oidc client create: a %s app has no client at the identity provider. Blueprints with one: %s", app.Blueprint, strings.Join(blueprintsWithClients(), ", "))
	}
	idp := pocketIDApp(cfg)
	if idp == "" {
		return fmt.Errorf("oidc client create: %s declares no pocket-id app to create the client in", *configPath)
	}
	where, err := pocketIDSite(r, cfg, idp, *site, "oidc client create")
	if err != nil {
		return err
	}
	// The calls need no root, and only a run that will change something
	// checks the host's admission.
	if *execute {
		if _, err := admittedHosts(r, cfg, map[string]registry.Runner{where: registryHost(where, cfg.Sites[where], *destination)}); err != nil {
			return err
		}
		// The client is the deployment's, which apply makes as this
		// release knows it; a newer release's is not remade by this one.
		ans, err := answers()
		if err != nil {
			return err
		}
		release, err := guardSites(r, cfg, []string{where}, func(string) apply.Transport { return siteTransport(where, cfg.Sites[where], *destination) }, "oidc client create", guard{execute: true, lease: leaseOptions{breakLease: *breakLease, answers: ans, stdin: os.Stdin, stdout: os.Stdout}})
		if err != nil {
			return err
		}
		defer release()
	}

	if *secretsPath == "" {
		*secretsPath = filepath.Join(filepath.Dir(*configPath), "secrets.enc.yaml")
	}
	secrets, err := config.LoadDeploymentSecrets(*secretsPath, cfg.ID)
	if err != nil {
		return err
	}
	recipients, err := config.Recipients(filepath.Dir(*secretsPath))
	if err != nil {
		return err
	}
	// Refused before anything is planned, because the secret is written
	// before Pocket ID is sent it and a run that cannot write must not start.
	if secrets.Encrypted && len(recipients) == 0 {
		return config.NoRecipientProblem(*secretsPath)
	}
	key, _ := secrets.Apps[idp]["static_api_key"].(string)
	if key == "" {
		return fmt.Errorf("oidc client create: secrets apps.%s.static_api_key is empty. Run `%s init` to generate it and `%s deployment apply` to render it into Pocket ID, then re-run", idp, progname.Cmd(), progname.Cmd())
	}

	ctx := context.Background()
	api := clientAPI(cfg, where, *destination, key)
	plan, err := ui.Get(r, "read "+*appName+"'s client", func() (*oidcclient.Plan, error) {
		return planClient(api, desired, recordedClient(secrets, *appName))
	})
	if err != nil {
		return fmt.Errorf("oidc client create: %w", err)
	}
	// A dry run lists the plan; --execute reports each mutation as it is
	// made, and lists the plan first only with --verbose.
	if !*execute || r.Verbose() {
		listClientPlan(r, *appName, idp, where, plan)
	}
	for _, w := range plan.Warnings {
		r.Warn(w, "")
	}
	if len(plan.Steps) == 0 {
		return nil
	}
	if !*execute {
		r.Result("Nothing changed. Re-run with --execute to apply.")
		return nil
	}

	rec := &secretsRecorder{app: *appName, path: *secretsPath, secrets: secrets, recipients: recipients}
	plan.Report = r
	if err := oidcclient.Execute(ctx, plan, api, rec, secretsgen.ClientSecret); err != nil {
		return fmt.Errorf("oidc client create: %w", err)
	}
	if rec.wrote {
		r.Detail("recorded oidc_clients.%s.client_id and oidc_clients.%s.client_secret", *appName, *appName)
		if len(recipients) == 0 {
			warnPlaintext(r, *secretsPath)
		}
	}
	r.Detail("`%s deployment apply --site <site> --execute` on each site running %s renders the client", progname.Cmd(), *appName)
	r.Result("%s's client is in place.", *appName)
	return nil
}

// warnPlaintext is the warning for secrets written to a file that no age
// recipient beside it covers. Only the explicit test mode writes one
// (config.PlaintextSecretsVariable); anywhere else the write is refused.
func warnPlaintext(r ui.Reporter, path string) {
	r.Warn(path+" was written in plaintext", fmt.Sprintf("%s is PLAINTEXT, because %s=1 is set and no %s beside it names an age recipient. That mode is for tests and fixtures; a real deployment encrypts this file.", path, config.PlaintextSecretsVariable, config.SOPSConfigName))
}

// secretsRecorder writes a client's credentials into the secrets file,
// re-encrypted to the recipients beside it, as `secrets set` does.
type secretsRecorder struct {
	app        string
	path       string
	secrets    *config.Secrets
	recipients []string
	wrote      bool
	// written is the bytes the last write put in the file.
	written []byte
}

func (r *secretsRecorder) Record(clientID, clientSecret string) error {
	// Written with no recipient, an encrypted file would come back as
	// plaintext. Callers refuse this before planning; this is the floor.
	if r.secrets.Encrypted && len(r.recipients) == 0 {
		return fmt.Errorf("%s is encrypted, but no %s beside it names a recipient, so the client's credentials could not be written back encrypted", r.path, config.SOPSConfigName)
	}
	if err := r.secrets.Set("oidc_clients."+r.app+".client_id", clientID); err != nil {
		return err
	}
	if err := r.secrets.Set("oidc_clients."+r.app+".client_secret", clientSecret); err != nil {
		return err
	}
	written, err := config.WriteSecretsBytes(r.path, r.secrets, r.recipients)
	if err != nil {
		return err
	}
	r.wrote, r.written = true, written
	return nil
}

// blueprintsWithClients is every built in blueprint that signs in through
// the identity provider, for a refusal that lists them.
func blueprintsWithClients() []string {
	var out []string
	for _, blueprint := range blueprints.Builtin() {
		if _, ok := blueprints.OIDCClient(blueprint, "example.org", "app"); ok {
			out = append(out, string(blueprint))
		}
	}
	return out
}
