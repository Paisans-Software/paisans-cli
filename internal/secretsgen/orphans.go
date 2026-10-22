package secretsgen

import (
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"github.com/paisans-software/paisans-cli/internal/progname"
)

// Orphan is a secret naming something the configuration no longer declares:
// a site, an app or a backup destination taken out of paisans-stack.yaml, or a
// Pocket ID group no signup_default_groups names. Nothing reads it any more.
// It is reported by key and never by value, and only `paisans secrets prune
// --execute` removes it.
type Orphan struct {
	Key string
	// Why is what it names that is gone, for the report.
	Why string
	// Leaves is what removing it leaves elsewhere, empty when nothing.
	Leaves string
}

// Orphans is every orphaned secret, sorted by key. A name deployed lists is
// still running, whatever the yaml says, and its secrets are not orphans;
// nil deployed is no record, and the yaml alone decides.
func Orphans(cfg *config.Config, secrets *config.Secrets, deployed *deployrecord.Record) []Orphan {
	held := func(kind, name string) bool { return deployed != nil && deployed.Lists(kind, name) }
	var out []Orphan
	for name := range secrets.Sites {
		if _, ok := cfg.Sites[name]; !ok && !held("sites", name) {
			out = append(out, Orphan{Key: "sites." + name, Why: "names a site " + config.FileName + " does not declare"})
		}
	}
	for name := range secrets.Apps {
		if _, ok := cfg.Apps[name]; !ok && !held("apps", name) {
			out = append(out, Orphan{Key: "apps." + name, Why: "names an app " + config.FileName + " does not declare"})
		}
	}
	for name := range secrets.OIDCClients {
		if _, ok := cfg.Apps[name]; !ok && !held("apps", name) {
			out = append(out, Orphan{Key: "oidc_clients." + name, Why: "names an app " + config.FileName + " does not declare",
				Leaves: name + "'s sign-in client still exists at Pocket ID, if that Pocket ID still runs; delete it there"})
		}
	}
	// A destination's credentials, once paisans-stack.yaml no longer names it. The
	// restic password and WAL-G's key never are: they decrypt every backup
	// already taken, wherever it is.
	destinations := map[string]bool{}
	if cfg.Backup != nil {
		for _, d := range cfg.Backup.Destinations {
			destinations[d.Name] = true
		}
	}
	for name := range secrets.Backup.Destinations {
		if !destinations[name] {
			out = append(out, Orphan{Key: "backup.destinations." + name, Why: "names a backup destination " + config.FileName + " does not declare",
				Leaves: "its key still exists at the provider that issued it; delete it there"})
		}
	}
	named := map[string]bool{}
	for _, name := range cfg.AppNames() {
		if cfg.Apps[name].Blueprint == config.BlueprintPocketID {
			for _, g := range blueprints.PocketIDSignupGroups(cfg.Apps[name].Settings) {
				named[g] = true
			}
		}
	}
	for group := range secrets.PocketIDGroups {
		if !named[group] && !held("pocket_id_groups", group) {
			out = append(out, Orphan{Key: "pocket_id_groups." + group, Why: "names a group no signup_default_groups names"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Dropped is every name the record lists that paisans-stack.yaml does not declare:
// something taken out of the yaml while it still runs, by mistake or before
// its removal command. It is reported whether or not it has secrets.
func Dropped(cfg *config.Config, deployed *deployrecord.Record) []Orphan {
	if deployed == nil {
		return nil
	}
	const why = "is deployed but " + config.FileName + " no longer declares it"
	declared := deployrecord.FromConfig(cfg)
	var out []Orphan
	for _, n := range deployed.Sites {
		if !declared.Lists("sites", n) {
			out = append(out, Orphan{Key: "sites." + n, Why: why, Leaves: "restore it, or take it out with `" + progname.Name() + " site remove " + n + "`, or `" + progname.Name() + " site remove " + n + " --force --ssh <its host>` if its host is gone"})
		}
	}
	for _, n := range deployed.Apps {
		if !declared.Lists("apps", n) {
			out = append(out, Orphan{Key: "apps." + n, Why: why, Leaves: "restore it, or take it out with `" + progname.Name() + " app remove " + n + "`"})
		}
	}
	for _, n := range deployed.PocketIDGroups {
		if !declared.Lists("pocket_id_groups", n) {
			out = append(out, Orphan{Key: "pocket_id_groups." + n, Why: why, Leaves: "restore an app naming it in signup_default_groups, or `" + progname.Name() + " app remove` the app that named it"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Prune removes each orphan from secrets. The caller writes the file.
func Prune(secrets *config.Secrets, orphans []Orphan) {
	for _, o := range orphans {
		kind, name, _ := strings.Cut(o.Key, ".")
		switch kind {
		case "sites":
			delete(secrets.Sites, name)
		case "apps":
			delete(secrets.Apps, name)
		case "oidc_clients":
			delete(secrets.OIDCClients, name)
		case "pocket_id_groups":
			delete(secrets.PocketIDGroups, name)
		case "backup":
			delete(secrets.Backup.Destinations, strings.TrimPrefix(name, "destinations."))
		}
	}
}
