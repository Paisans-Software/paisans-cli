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
