package secretsrotate

import (
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// beszelAdmin rotates a beszel app's break glass password.
type beszelAdmin struct {
	Generated
	app string
}

// BeszelAdmin is the rotation of apps.<app>.admin_password for Beszel's hub:
// the password of its break glass superuser and user, for when the identity
// provider is down. The hub reads USER_PASSWORD on a new install only, so the
// hub's own stack is the one user: its apply recreates the admin one shot,
// which sets the superuser's password, and the setup container, which sets
// the user's, since both take the hub's .env (the beszel blueprint's
// compose.yaml). It signs nobody out and costs members nothing: the operator
// is the one who uses it.
func BeszelAdmin(app string) Rotator {
	return beszelAdmin{Generated: Generated{SecretPath: "apps." + app + ".admin_password"}, app: app}
}

func (r beszelAdmin) Cost() Cost { return Cost{} }

// Precheck refuses an app that is not a beszel hub, and one that runs on no
// site.
func (r beszelAdmin) Precheck(e Env) error {
	app, ok := e.Cfg.Apps[r.app]
	if !ok {
		return fmt.Errorf("the configuration declares no app %q", r.app)
	}
	if app.Blueprint != config.BlueprintBeszel {
		return fmt.Errorf("apps.%s is %s, and this admin_password rotation is for %s", r.app, app.Blueprint, config.BlueprintBeszel)
	}
	return runsSomewhere(e, r.app)
}

// Checked has nothing to do: the hub's own stack sets the password from the
// value it is applied with, which stage 3 hands it.
func (r beszelAdmin) Checked(e Env, next string) error { return nil }

func (r beszelAdmin) Accepts(e Env, next string) (bool, error) { return true, nil }

func (r beszelAdmin) Users(e Env) []Use { return appUses(e, r.app) }

// beszelToken rotates the token a site's agent registers with.
type beszelToken struct {
	Generated
	site string
}

// BeszelToken is the rotation of sites.<site>.beszel_token. The hub learns it
// first: it lists one system per site with that site's token in
// data/config.yml, so its stack is applied with the next value, which
// rewrites the file and restarts the hub on it. Then the site's
// infrastructure stack, whose beszel-agent.env changes and so recreates the
// agent with it. Until that second apply the agent presents the old token,
// which the hub refuses, so the site's graphs gap for the length of the two
// applies and nothing else is affected.
func BeszelToken(site string) Rotator {
	return beszelToken{Generated: Generated{SecretPath: "sites." + site + ".beszel_token"}, site: site}
}

func (r beszelToken) Cost() Cost { return Cost{} }

// Precheck refuses a site the configuration does not declare, and a
// deployment with no beszel hub, whose tokens nothing reads.
func (r beszelToken) Precheck(e Env) error {
	if _, ok := e.Cfg.Sites[r.site]; !ok {
		return fmt.Errorf("the configuration declares no site %q", r.site)
	}
	if e.Cfg.BeszelApp() == "" {
		return fmt.Errorf("sites.%s.beszel_token is a secret of %s, and the configuration declares no app of it", r.site, config.BlueprintBeszel)
	}
	return nil
}

// Checked has nothing to do: the hub takes the token from its config file.
func (r beszelToken) Checked(e Env, next string) error { return nil }

func (r beszelToken) Accepts(e Env, next string) (bool, error) { return true, nil }

// Users is the hub's stack on its site, then the site's infrastructure
// stack.
func (r beszelToken) Users(e Env) []Use {
	hub := e.Cfg.BeszelApp()
	if hub == "" {
		return nil
	}
	out := appUses(e, hub)
	return append(out, Use{Site: r.site, Stacks: []string{"infra"}})
}
