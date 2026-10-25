package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/meshrekey"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// rekeyHost is how `mesh rekey` reaches a site. Tests replace it.
var rekeyHost = func(name string, site config.Site) apply.Transport {
	return siteTransport(name, site, "")
}

// runMesh dispatches `paisans mesh <subcommand>`.
func runMesh(args []string) error {
	if len(args) >= 1 && args[0] == "rekey" {
		return runMeshRekey(args[1:])
	}
	return &ui.Problem{Hint: "mesh takes one subcommand, rekey", Explain: "Usage: " + progname.Cmd() + " mesh rekey <site>... | --all [--execute]"}
}

// secretsStore is the secrets file as meshrekey reads and writes it: loaded
// and decrypted here, written encrypted to the recipients .sops.yaml beside
// it names. sops and age run on the workstation only.
type secretsStore struct{ path, id string }

func (s secretsStore) Load() (*config.Secrets, error) {
	return config.LoadDeploymentSecrets(s.path, s.id)
}

func (s secretsStore) Save(secrets *config.Secrets) error {
	recipients, err := config.Recipients(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	if secrets.Encrypted && len(recipients) == 0 {
		return config.NoRecipientProblem(s.path)
	}
	return config.WriteSecrets(s.path, secrets, recipients)
}

// runMeshRekey replaces sites' WireGuard keys, one site at a time, each in
// gated stages: see internal/meshrekey and README's *Rekeying the mesh*.
// Like every command that changes a host, it plans from what it reads and
// changes nothing without --execute.
func runMeshRekey(args []string) error {
	fs := newFlagSet("mesh rekey", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	id := idFlag(fs)
	secretsPath := fs.String("secrets", "", "path to the secrets (default: secrets.enc.yaml beside the config)")
	execute := fs.Bool("execute", false, "actually rekey, one site at a time, stopping at the first gate that fails")
	all := fs.Bool("all", false, "rekey every site the configuration declares, in its order")
	var sites []string
	for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sites, args = append(sites, args[0]), args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	sites = append(sites, fs.Args()...)
	r := reporter()
	switch {
	case *all && len(sites) > 0:
		return &ui.Problem{Hint: "--all takes no site", Explain: "Name the sites to rekey, or pass --all for every one. Got " + strings.Join(sites, " ") + " too."}
	case !*all && len(sites) == 0:
		return &ui.Problem{Hint: "mesh rekey needs a site", Explain: "Name the sites to rekey, Eg: " + progname.Cmd() + " mesh rekey home-a, or pass --all for every one."}
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
	every := *all
	for _, name := range sites {
		if _, ok := cfg.Sites[name]; !ok {
			return &ui.Problem{Hint: fmt.Sprintf("%s declares no site %s", ui.ShortPath(*configPath), name), Explain: "Declared sites are " + strings.Join(cfg.SiteNames(), ", ") + "."}
		}
	}
	if *secretsPath == "" {
		*secretsPath = filepath.Join(filepath.Dir(*configPath), "secrets.enc.yaml")
	}
	store := secretsStore{path: *secretsPath, id: cfg.ID}
	secrets, err := store.Load()
	if err != nil {
		return err
	}
	if !secrets.Encrypted {
		warnUnencrypted(r, *secretsPath)
	}
	if err := refuseMidRotation(secrets, "mesh rekey"); err != nil {
		return err
	}
	transports := map[string]apply.Transport{}
	for _, name := range cfg.SiteNames() {
		transports[name] = rekeyHost(name, cfg.Sites[name])
	}
	plan, err := ui.Get(r, "read every site", func() (*meshrekey.Plan, error) {
		if every {
			return meshrekey.BuildAll(cfg, store, transports)
		}
		return meshrekey.Build(cfg, store, sites, transports)
	})
	if err != nil {
		return err
	}
	if !*execute || r.Verbose() {
		plan.Show(r)
	}
	// Every site is a peer of some site rekeyed, so every one is written to
	// and has its admission checked, but a site not brought up yet.
	if _, err := admittedSites(r, cfg, withoutNames(cfg.SiteNames(), plan.Joining)...); err != nil {
		return err
	}
	if !*execute {
		r.Result("Nothing changed. Re-run with --execute to rekey.")
		return nil
	}
	plan.Report = r
	if err := meshrekey.Execute(plan); err != nil {
		return err
	}
	var names []string
	for _, s := range plan.Sites {
		names = append(names, s.Name)
	}
	r.Result("%s rekeyed: every peer has a handshake under each new key, and %s holds only the new keys.", strings.Join(names, ", "), *secretsPath)
	return nil
}

// refuseMidRekey refuses a command that renders mesh files while a mesh
// rekey is unfinished. A site with a next key in the secrets file may already
// run it, and every peer that lists the site with it; render names the
// current key, the old one, so an apply would hand the old key back to those
// peers and cut the site's tunnels. Finishing the rekey promotes the key,
// after which render names the key the host runs.
func refuseMidRekey(secrets *config.Secrets) error {
	pending := secrets.RekeyPending()
	if len(pending) == 0 {
		return nil
	}
	return &ui.Problem{
		Hint:    "a mesh rekey is unfinished",
		Explain: fmt.Sprintf("The secrets file holds a next WireGuard key for %s, so a mesh rekey stopped part way. Its host may already run that key, and apply renders the old one, which would cut its tunnels. Finish the rekey first: %s mesh rekey %s --execute (or --all, when the run was --all), then apply. Nothing was changed.", strings.Join(pending, ", "), progname.Cmd(), strings.Join(pending, " ")),
	}
}
