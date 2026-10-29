package secretsgen

import (
	"sort"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// Source is where a secret's value comes from, one of the three kinds the
// secrets file holds (see the package comment).
type Source string

const (
	// Generated is made by `paisans init` and never seen.
	Generated Source = "generated"
	// Pasted is issued by somebody outside the deployment and set with
	// `paisans secrets set`.
	Pasted Source = "pasted"
	// Captured is issued by a running service of the deployment and
	// recorded by apply.
	Captured Source = "captured"
)

// Declared is one secret the configuration needs, by its dotted path in the
// secrets file, and where its value comes from. It carries no value, so
// nothing listing it can print one.
type Declared struct {
	Path   string
	Source Source
}

// Declare is every secret the configuration needs, sorted by path, whether
// or not secrets holds it yet: what init generates, what is owed from
// elsewhere, and what apply records. A pasted secret that has a stand in is
// listed only where it is the one read: external.smtp_password while an app
// that logs in to send mail has no password of its own, and an app's own
// apps.<app>.smtp_password once it is set.
func Declare(cfg *config.Config, secrets *config.Secrets) []Declared {
	var out []Declared
	add := func(source Source, paths ...string) {
		for _, p := range paths {
			out = append(out, Declared{Path: p, Source: source})
		}
	}
	add(Generated, generatedNames(cfg)...)

	if len(cfg.CaddySites()) > 0 {
		add(Pasted, "external.acme_dns_token")
	}
	if SMTPPasswordUsed(cfg, secrets, "external.smtp_password") {
		add(Pasted, "external.smtp_password")
	}
	for _, name := range smtpLogins(cfg) {
		if ownSMTPPassword(secrets, name) {
			add(Pasted, "apps."+name+".smtp_password")
		}
	}
	if cfg.Backup.On() {
		for _, d := range cfg.Backup.Destinations {
			add(Pasted, "backup.destinations."+d.Name+".access_key_id", "backup.destinations."+d.Name+".secret_access_key")
		}
	}

	groups := map[string]bool{}
	for _, name := range cfg.AppNames() {
		app := cfg.Apps[name]
		if _, ok := blueprints.OIDCClient(app.Blueprint, app.Hostname, name); ok {
			add(Captured, "oidc_clients."+name+".client_id", "oidc_clients."+name+".client_secret")
		}
		if app.Blueprint == config.BlueprintPocketID {
			for _, g := range blueprints.PocketIDSignupGroups(app.Settings) {
				if !groups[g] {
					groups[g] = true
					add(Captured, "pocket_id_groups."+g)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// generatedNames is every secret Fill generates for cfg, by name, without
// generating any: the same walk, in the same terms. A test holds the two to
// each other.
func generatedNames(cfg *config.Config) []string {
	out := []string{
		"cluster.superuser_password",
		"cluster.admin_password",
		"cluster.standby_password",
		"storage.garage.admin_token",
		"storage.garage.rpc_secret",
	}
	if cfg.Backup.On() {
		out = append(out, "backup.restic_password", "backup.walg_libsodium_key")
	}
	for _, name := range cfg.SiteNames() {
		out = append(out, "sites."+name+".wireguard_private_key", "sites."+name+".heartbeat_token")
		if cfg.BeszelApp() != "" {
			out = append(out, "sites."+name+".beszel_token")
		}
	}
	for _, name := range cfg.AppNames() {
		for _, key := range appSecretKeys(cfg, cfg.Apps[name]) {
			out = append(out, "apps."+name+"."+key)
		}
	}
	sort.Strings(out)
	return out
}
