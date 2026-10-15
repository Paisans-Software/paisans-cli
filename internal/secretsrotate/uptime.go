package secretsrotate

import (
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// What the uptime fork does with the two secrets at boot, read in its source
// (Paisans-Software/uptime) at v1.1.0-oidc.4, the tag the blueprint pins
// (internal/blueprints):
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
	if app.Blueprint != config.BlueprintUptime {
		return fmt.Errorf("apps.%s is %s, and admin_password is a secret of %s", r.app, app.Blueprint, config.BlueprintUptime)
	}
	return runsSomewhere(e, r.app)
}

// Checked has nothing to do: the fork checks the password against the value
// it started with, and stage 3 starts it with the next one.
func (r uptimeAdmin) Checked(e Env, next string) error { return nil }

func (r uptimeAdmin) Accepts(e Env, next string) (bool, error) { return true, nil }

func (r uptimeAdmin) Users(e Env) []Use { return appUses(e, r.app) }

// heartbeatGap is what a heartbeat rotation costs, as the plan says it.
// From the first monitor restarting with the next token until the site's
// heartbeat service is recreated with it, the site still pushes the old one,
// which a monitor that already holds the next answers 404 to. That is one
// scoped apply and health gate per monitor, then the pusher's, and a monitor
// counts the heartbeat missed only after the 60 second interval and its 120
// second grace (internal/render/heartbeat.go, heartbeatGraceSeconds), which
// those applies normally finish within. A run that stops in between leaves
// the pushes refused until it is resumed.
const heartbeatGap = "the site's heartbeat, which the monitors may refuse while they restart with the next token and until the site pushes it, normally within the grace before a heartbeat counts as missed"

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
		if e.Cfg.Apps[name].Blueprint != config.BlueprintUptime {
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

// operatorsHeartbeatGap is what an operators heartbeat rotation costs, for the
// same reason as heartbeatGap: from the first monitor restarting with the
// next token until the active Pocket ID instance's pusher is recreated with
// it, that instance still pushes the old one, which a monitor already
// holding the next answers 404 to. The pushers are recreated one site at a
// time, so the active instance may be the last. Nobody is signed out, and
// Pocket ID keeps running throughout.
const operatorsHeartbeatGap = "the operators heartbeat, which the monitors may refuse while they restart with the next token and until the active instance pushes it, normally within the grace before a heartbeat counts as missed"

// operatorsHeartbeat rotates the token a Pocket ID app's instances push the
// operator reconciler's answer with.
type operatorsHeartbeat struct {
	Generated
	app, key string
}

// OperatorsHeartbeat is the rotation of apps.<app>.operators_heartbeat_token, the
// token every instance of a Pocket ID app pushes the operators heartbeat with
// (internal/render/operatorsheartbeat.go). The monitors learn it first, as for
// a site's heartbeat: every uptime app, on its own site, is applied in turn,
// which rewrites its monitors.json and restarts it so the seed reconcile
// adopts the token. Every monitor, the one on a site the app runs on
// included, since every monitor's seed names the app's operators heartbeat.
// Then each site running the app, its own stack, whose
// operators-heartbeat/heartbeat.env changes. Only the operators-heartbeat
// service reads that file, and `up -d` replaces only the service whose configuration
// changed, so Pocket ID, its reconciler and its database are left running. A
// deployment with no monitor has no user: no pusher is rendered, and the
// token is replaced in the file and handed to nobody.
func OperatorsHeartbeat(app string) Rotator {
	return ContinuityHeartbeat(app, "operators_heartbeat_token")
}

// ContinuityHeartbeats is every token a Pocket ID app's instances push a
// continuity monitor with: the operators heartbeat's and the DNS continuity
// check's two (internal/render/dnscontinuity.go).
var ContinuityHeartbeats = []string{"operators_heartbeat_token", "dns_super_admins_heartbeat_token", "dns_2fa_heartbeat_token"}

// ContinuityHeartbeat is the rotation of one of ContinuityHeartbeats. Each
// is handed out exactly as the operators heartbeat's is: every monitor seeds
// it, and the pusher beside every instance pushes with it from
// operators-heartbeat/heartbeat.env. A DNS token in a deployment that renders
// no DNS check is replaced in the file and handed to monitors that do not
// seed it, which costs a restart of each and nothing else.
func ContinuityHeartbeat(app, key string) Rotator {
	return operatorsHeartbeat{Generated: Generated{SecretPath: "apps." + app + "." + key}, app: app, key: key}
}

func (r operatorsHeartbeat) Cost() Cost { return Cost{Downtime: operatorsHeartbeatGap} }

// Precheck refuses an app that is not a Pocket ID app, and one that runs on
// no site.
func (r operatorsHeartbeat) Precheck(e Env) error {
	app, ok := e.Cfg.Apps[r.app]
	if !ok {
		return fmt.Errorf("the configuration declares no app %q", r.app)
	}
	if app.Blueprint != config.BlueprintPocketID {
		return fmt.Errorf("apps.%s is %s, and %s is a secret of %s", r.app, app.Blueprint, r.key, config.BlueprintPocketID)
	}
	return runsSomewhere(e, r.app)
}

// Checked has nothing to do: the monitors check the token, and they are
// handed it as the first users.
func (r operatorsHeartbeat) Checked(e Env, next string) error { return nil }

func (r operatorsHeartbeat) Accepts(e Env, next string) (bool, error) { return true, nil }

// Users is every monitor, in app order, each on its own site, then every
// site running the app, its own stack on each: the monitors
// render.operatorsHeartbeatTargets has the app push to, and its pushers.
func (r operatorsHeartbeat) Users(e Env) []Use {
	var out []Use
	placed := render.AppSites(e.Cfg)
	for _, name := range e.Cfg.AppNames() {
		if e.Cfg.Apps[name].Blueprint != config.BlueprintUptime {
			continue
		}
		for _, site := range placed[name] {
			out = append(out, Use{Site: site, Stacks: []string{name}})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return append(out, appUses(e, r.app)...)
}
