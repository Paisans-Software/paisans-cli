package main

import (
	"fmt"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/appadmin"
	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// adminTransport is how a call to Pocket ID's admin API reaches a host.
// Tests replace it.
var adminTransport = func(t apply.SSHTransport) appadmin.Transport { return t }

// runApp dispatches `paisans app <subcommand>`.
func runApp(args []string) error {
	if len(args) >= 1 && args[0] == "remove" {
		return runAppRemove(args[1:], os.Stdin, os.Stdout)
	}
	return fmt.Errorf("app takes one subcommand: paisans app remove <app>. An admin is declared under admins and made by apply; paisans admin magic-link gives one a fresh sign-in link")
}

// adminGroups is every admin group an app that signs in through Pocket ID
// reads, sorted and without duplicates: the groups apply puts a declared
// admin in.
func adminGroups(cfg *config.Config) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range cfg.AppNames() {
		app := cfg.Apps[name]
		spec, ok := blueprints.OIDCClient(app.Kind, app.Hostname, name)
		if !ok {
			continue
		}
		if group, _ := spec.Groups(app); group != "" && !seen[group] {
			seen[group] = true
			out = append(out, group)
		}
	}
	sort.Strings(out)
	return out
}

// memberGroups is the member group of every app that signs in through the
// identity provider, sorted and without duplicates, leaving out any that is
// also an admin group: the groups a declared admin joins so that every such
// app admits them.
func memberGroups(cfg *config.Config) []string {
	seen := map[string]bool{}
	for _, group := range adminGroups(cfg) {
		seen[group] = true
	}
	var out []string
	for _, name := range cfg.AppNames() {
		app := cfg.Apps[name]
		spec, ok := blueprints.OIDCClient(app.Kind, app.Hostname, name)
		if !ok {
			continue
		}
		if _, group := spec.Groups(app); group != "" && !seen[group] {
			seen[group] = true
			out = append(out, group)
		}
	}
	sort.Strings(out)
	return out
}

// pocketIDKey reads the static API key for a Pocket ID app from the secrets
// file. It is held only in memory and sent only on curl's stdin.
func pocketIDKey(cfg *config.Config, configPath, secretsPath, app string) (string, error) {
	if secretsPath == "" {
		secretsPath = filepath.Join(filepath.Dir(configPath), "secrets.enc.yaml")
	}
	secrets, err := config.LoadDeploymentSecrets(secretsPath, cfg.ID)
	if err != nil {
		return "", err
	}
	key, _ := secrets.Apps[app]["static_api_key"].(string)
	if key == "" {
		return "", fmt.Errorf("secrets apps.%s.static_api_key is empty. Run `paisans init` to generate it and `paisans apply` to render it into Pocket ID, then re-run", app)
	}
	return key, nil
}

// pocketIDBase is where Pocket ID answers from the host it runs on: the port
// it publishes on that site's mesh address, and nowhere else.
func pocketIDBase(cfg *config.Config, site string) string {
	return fmt.Sprintf("http://%s:%d", cfg.Sites[site].Address, render.AppPort(config.KindPocketID))
}

// adminSite picks the site to run an app's commands on. A pinned app has one.
// A clustered app runs on every apps site against one shared database, so any
// of them will do, and the first in sorted order is chosen so that the same
// command always reaches the same host. --site overrides it, but only to a
// site the app actually runs on.
func adminSite(cfg *config.Config, appName, override, command string) (string, error) {
	app := cfg.Apps[appName]
	var candidates []string
	switch app.Placement.Mode {
	case config.PlacementPinned:
		candidates = []string{app.Placement.Site}
	case config.PlacementCluster:
		candidates = cfg.AppsSites()
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("%s: %s runs on no site", command, appName)
	}
	if override == "" {
		return candidates[0], nil
	}
	if !slices.Contains(candidates, override) {
		return "", fmt.Errorf("%s: %s does not run on %s. It runs on %s", command, appName, override, strings.Join(candidates, ", "))
	}
	return override, nil
}

// standbyLook is how the admin commands ask a site what its Pocket ID is
// doing. Tests replace it.
var standbyLook = func(t apply.SSHTransport) apply.Transport { return t }

// pocketIDSite is adminSite for a Pocket ID that runs on more than one site:
// with no --site, the site whose instance is active, since the others stand
// by with their port closed and an API call there would be refused. It asks
// without sudo, as the API calls themselves run: the active site is found by
// /healthz on its mesh address, which needs no root, and a standby merely
// reads as down when docker is not the deploy user's. An explicit --site is
// used as given, through adminSite's own check. Asking each site is a step
// of r's, since ssh answers only once every site has.
func pocketIDSite(r ui.Reporter, cfg *config.Config, appName, override, command string) (string, error) {
	if override != "" || !slices.Contains(apply.StandbyApps(cfg), appName) {
		return adminSite(cfg, appName, override, command)
	}
	var list []apply.Instance
	site, err := ui.Get(r, "find the active "+appName, func() (string, error) {
		site, l, err := activeInstance(cfg, appName, "", "")
		list = l
		return site, err
	})
	if err != nil {
		return "", fmt.Errorf("%s: %w, so there is no one site to call:\n%sName one with --site once exactly one is active", command, err, apply.DescribeInstances(list))
	}
	return site, nil
}

// activeInstance is the site whose instance of a Pocket ID app running on
// several sites is active, with what every site answered, or why there is no
// one such site. site, when named, is reached through destination, as the
// command running on it was told with --ssh.
func activeInstance(cfg *config.Config, appName, site, destination string) (string, []apply.Instance, error) {
	transports := map[string]apply.Transport{}
	for _, name := range cfg.AppsSites() {
		override := ""
		if name == site {
			override = destination
		}
		transports[name] = standbyLook(siteTransport(name, cfg.Sites[name], override, false))
	}
	list := apply.LookAtInstances(cfg, appName, transports)
	if _, err := apply.OneActive(cfg.Deployment(), appName, list); err != nil {
		return "", list, err
	}
	for _, in := range list {
		if in.State == apply.Active {
			return in.Site, list, nil
		}
	}
	return "", list, fmt.Errorf("no active instance of %s", appName)
}
