package secretsrotate

import (
	"fmt"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// What the uptime fork does with the two secrets at boot, read in its source
// (Paisans-Software/uptime) at v1.1.0-oidc.4, the tag the kind pins
// (internal/kinds):
//
//   - ADMIN_PASS is the env super admin's password and is never stored: the
//     fork reads it into config.admin.pass when it starts (src/config.js, the
//     admin block) and compares a sign in against that value (src/auth.js,
//     envAdminCredentialsOk). The admin's name is reserved out of the users
//     table (src/lib/users.js), so no row holds an old password. A restart
//     with the new value is the whole of the rotation.
//   - The seed reconcile runs at every boot, after migrations and before the
//     monitor starts (src/lib/seed.js, its header, called from src/server.js),
//     and hands an existing managed monitor the token its entry carries:
//     seedToken accepts a heartbeat_token of 16 to 64 hex characters that no
//     other monitor holds, and reconcileMonitors passes it to both updateSite
//     and insertSite, which store it as given (src/lib/sitePayload.js,
//     updateSite's heartbeat token block). At 1.1.0-oidc.3 the reconcile
//     passed updateSite no token and an existing monitor kept its own, so the
//     tag is what makes a reseed enough.
//
// So neither has a checking side to change before the users: Checked has
// nothing to do, and the monitor's uptime stack is a user, restarted with the
// new value by stage 3's scoped apply.

// uptimeAdmin rotates an uptime app's break glass password.
type uptimeAdmin struct {
	Generated
	app string
}

// UptimeAdmin is the rotation of apps.<app>.admin_password, the uptime fork's
// ADMIN_PASS: the sign in for when the identity provider is down. Its only
// user is the app's own stack on its site, which reads the value when it
// starts. It signs nobody out and costs members nothing: the operator is the
// one who uses it.
func UptimeAdmin(app string) Rotator {
	return uptimeAdmin{Generated: Generated{SecretPath: "apps." + app + ".admin_password"}, app: app}
}

func (r uptimeAdmin) Cost() Cost { return Cost{} }

// Precheck refuses an app that is not an uptime monitor, and one that runs on
// no site.
func (r uptimeAdmin) Precheck(e Env) error {
	app, ok := e.Cfg.Apps[r.app]
	if !ok {
		return fmt.Errorf("the configuration declares no app %q", r.app)
	}
	if app.Kind != config.KindUptime {
		return fmt.Errorf("apps.%s is %s, and admin_password is a secret of %s", r.app, app.Kind, config.KindUptime)
	}
	return runsSomewhere(e, r.app)
}

// Checked has nothing to do: the fork checks the password against the value
// it started with, and stage 3 starts it with the next one.
func (r uptimeAdmin) Checked(e Env, next string) error { return nil }

func (r uptimeAdmin) Accepts(e Env, next string) (bool, error) { return true, nil }

func (r uptimeAdmin) Users(e Env) []Use { return appUses(e, r.app) }

// heartbeatGap is what a heartbeat rotation costs, as the plan says it.
// Between the monitors restarting with the next token and the site's
// heartbeat service being recreated with it, the site still pushes the old
// one, which the monitors answer 404 to. That is one apply and one health
// gate, shorter than the one minute interval the site pushes at, and the
// monitor counts the site stale only after the interval and its grace
// (internal/render/heartbeat.go, heartbeatGraceSeconds). A run that stops
// between the two leaves the pushes refused until it is resumed.
const heartbeatGap = "the site's heartbeat, which the monitors may refuse for at most one push interval between learning the next token and the site pushing it"

// heartbeat rotates the token a site pushes its heartbeat with.
type heartbeat struct {
	Generated
	site string
}

// Heartbeat is the rotation of sites.<site>.heartbeat_token. The monitors
// learn it first: each uptime app off the site, whose seed names the site's
// token, is applied in turn, which rewrites its monitors.json and restarts it
// so the seed reconcile adopts the token. That is the same scoped apply of
// the monitor's own stack that apply.PlanMonitorReseeds plans after a
// topology change, made here through Env.Apply so that it renders with the
// next value. Then the site's infrastructure stack, whose heartbeat.env
// changes and so recreates the heartbeat service alone
// (internal/apply/apply.go, heartbeatService). A site that no monitor
// watches, the one monitor's own, has no user: its token is replaced in the
// file and handed to nobody.
func Heartbeat(site string) Rotator {
	return heartbeat{Generated: Generated{SecretPath: "sites." + site + ".heartbeat_token"}, site: site}
}

func (r heartbeat) Cost() Cost { return Cost{Downtime: heartbeatGap} }

func (r heartbeat) Precheck(e Env) error {
	if _, ok := e.Cfg.Sites[r.site]; !ok {
		return fmt.Errorf("the configuration declares no site %q", r.site)
	}
	return nil
}

// Checked has nothing to do: the monitors check the token, and they are
// handed it as the first users.
func (r heartbeat) Checked(e Env, next string) error { return nil }

func (r heartbeat) Accepts(e Env, next string) (bool, error) { return true, nil }

// Users is every monitor off the site, in app order, each on its own site,
// then the site's infrastructure stack: the same monitors
// render.heartbeatTargets has the site push to.
func (r heartbeat) Users(e Env) []Use {
	var out []Use
	placed := render.AppSites(e.Cfg)
	for _, name := range e.Cfg.AppNames() {
		if e.Cfg.Apps[name].Kind != config.KindUptime {
			continue
		}
		for _, site := range placed[name] {
			if site == r.site {
				continue
			}
			out = append(out, Use{Site: site, Stacks: []string{name}})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return append(out, Use{Site: r.site, Stacks: []string{"infra"}})
}
