package main

import (
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/appadmin"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/registry"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

// runAdminMagicLink issues one admin the configuration declares a one-time
// sign-in link at Pocket ID, and nothing else. It is for a first sign in whose
// link expired before it was used and for an admin who lost their passkey:
// either way they open the link, sign in once, and register a passkey. The
// account, its groups and its administrator flag are apply's, which makes
// every declared admin, so an admin with no account yet is pointed there. The
// link is printed once, as apply prints its own, and Pocket ID emails one as
// well when it emails apply's.
func runAdminMagicLink(args []string) error {
	fs := newFlagSet("admin magic-link", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, configHelp)
	secretsPath := fs.String("secrets", "", "path to the secrets file, read for Pocket ID's API key (default: secrets.enc.yaml beside the config)")
	app := fs.String("app", "", "the Pocket ID app (default: the only one)")
	site := fs.String("site", "", "the site whose Pocket ID to call (default: the pinned site, or the site whose instance is active)")
	destination := fs.String("ssh", "", "ssh destination, used verbatim in place of the site's ssh section (its user, host, port and keys are then ignored)")
	execute := fs.Bool("execute", false, "issue the link")
	pos, err := parseAll(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return &ui.Problem{Hint: "admin magic-link takes one admin", Explain: "paisans admin magic-link <admin>, by their key under admins."}
	}
	id := pos[0]
	// The link goes to stdout and nothing else does, so a script can read it.
	r := errReporter(reporter())

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	result := validate.Check(cfg)
	reportFindings(r, *configPath, result)
	if result.Refused() {
		return refused(*configPath, len(result.Refusals()), "")
	}
	if _, ok := cfg.Admins[id]; !ok {
		return &ui.Problem{Hint: id + " is not a declared admin", Explain: fmt.Sprintf("Only an admin under admins in %s is given a link. Add them there and run apply, which makes their account and gives them one.", *configPath)}
	}
	pid, err := magicLinkApp(cfg, *app)
	if err != nil {
		return err
	}
	where, err := pocketIDSite(r, cfg, pid, *site, "admin magic-link")
	if err != nil {
		return err
	}
	key, err := pocketIDKey(cfg, *configPath, *secretsPath, pid)
	if err != nil {
		return fmt.Errorf("admin magic-link: %w", err)
	}
	req := adminRequest(cfg, pid, id)
	req.APIKey, req.APIBase = key, pocketIDBase(cfg, where)
	transport := adminTransport(siteTransport(where, cfg.Sites[where], *destination, false))

	r.Section(fmt.Sprintf("%s on %s", pid, where))
	what := fmt.Sprintf("a one-time sign-in link for %s, valid %s and for one sign in", id, humanTTL(adminLinkTTL))
	if req.EmailLink {
		what += ", and Pocket ID's email of one to " + req.Email
	}
	if !*execute {
		if _, err := ui.Get(r, "find "+id+" in "+pid, func() (appadmin.Outcome, error) {
			return appadmin.MagicLink(transport, req, false)
		}); err != nil {
			return fmt.Errorf("admin magic-link: %w", err)
		}
		r.Step("issue "+what).End(ui.Pending, "")
		r.Result("Nothing changed. Re-run with --execute to issue it.")
		return nil
	}
	// The calls need no root, but the registry a run that changes something
	// claims its host in is root's.
	if err := claimHosts(r, cfg, true, map[string]registry.Runner{where: registryHost(where, cfg.Sites[where], *destination, true)}); err != nil {
		return err
	}
	s := r.Step("issue " + what)
	outcome, err := appadmin.MagicLink(transport, req, true)
	if err != nil {
		s.Fail(err)
		return fmt.Errorf("admin magic-link: %w", err)
	}
	s.Done("")
	d := declaredAdmin{App: pid, ID: id, Email: req.Email, Link: outcome.LoginLink, ExpiresIn: outcome.ExpiresIn, Emailed: outcome.Emailed}
	if d.Emailed {
		convergeEmailResults(r, cfg, pid, convergeOptions{Config: *configPath, Secrets: *secretsPath}, []declaredAdmin{d})
	} else if !req.EmailLink {
		r.Note("Pocket ID "+pid+" emails no sign-in links", adminEmailOffWhy(cfg, pid))
	}
	r.Result("%s can sign in once with the link below and register a passkey.", id)
	printLoginLinks([]declaredAdmin{d})
	return nil
}

// magicLinkApp is the Pocket ID app a link is issued at: the one named, or
// the only one the configuration declares.
func magicLinkApp(cfg *config.Config, named string) (string, error) {
	var pids []string
	for _, name := range cfg.AppNames() {
		if cfg.Apps[name].Blueprint == config.BlueprintPocketID {
			pids = append(pids, name)
		}
	}
	sort.Strings(pids)
	switch {
	case named != "":
		if cfg.Apps[named].Blueprint != config.BlueprintPocketID {
			return "", &ui.Problem{Hint: named + " is not a Pocket ID app", Explain: "--app names the Pocket ID app to issue the link at: " + strings.Join(pids, ", ") + "."}
		}
		return named, nil
	case len(pids) == 1:
		return pids[0], nil
	case len(pids) == 0:
		return "", &ui.Problem{Hint: "no Pocket ID app is declared", Explain: "A sign-in link is Pocket ID's, and the configuration declares no pocket-id app."}
	}
	return "", &ui.Problem{Hint: "several Pocket ID apps are declared", Explain: "Name the one to issue the link at with --app: " + strings.Join(pids, ", ") + "."}
}
