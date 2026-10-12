package render

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// The admins heartbeat: the health of a Pocket ID app's admins group, pushed
// to every monitor by whichever of its instances is active. The admin
// reconciler beside each instance answers /healthz with the group's health on
// the active instance and with `standby` on every other
// (docs/specs/2026-10-08-admin-reconciler.md). A small service beside it, in
// the app's own stack on every site the app runs on, asks that once a minute
// and pushes what it hears to one heartbeat monitor per app, under one token
// every instance shares. So the monitor follows the active instance across a
// failover, keeps one history of the group's health, and reads silence past
// the grace as there being no active instance. The per site check on the
// reconciler (uptime.go) asks /livez instead, which is that site's reconciler
// service alone. docs/specs/2026-10-07-uptime-monitoring.md, the amendment of
// 2026-10-12, has the design.
//
// What the fork does with a push, read from its source at v1.1.0-oidc.4 on
// 2026-10-11: a status segment of fail, failure or down is a failure
// (src/routes/ping.js:61-70), on GET, HEAD, POST or PUT alike (:118-128). The
// body of a POST is kept raw when no parser of the app's took it
// (captureRawBody, :16-51), which is why a failure is sent as text/plain:
// express.urlencoded, which server.js:60 mounts, would otherwise parse the
// reason as a form. The body is stored with the ping and as the monitor's
// last_heartbeat_body (src/monitor.js:517-548), and shown, first line, in the
// recent pings of the monitor's own page (views/site-detail.ejs:298-299). It
// is not the incident's message, which is the fixed "heartbeat reported
// failure (exit 1)" (src/monitor.js:552-554), so an alert says that the group
// is short and the monitor's page says why.

// adminsHeartbeatName is the seed's name for a Pocket ID app's admins
// heartbeat, built from the app key so a renamed hostname updates it in place.
func adminsHeartbeatName(app string) string { return app + " — admin continuity" }

// adminsHeartbeatService is the pusher's compose service, and the directory
// of the app's stack its two files sit in.
const adminsHeartbeatService = "admins-heartbeat"

// adminsHeartbeatTargets is every monitor a Pocket ID app's instances push
// to: each uptime app, in app order, the site heartbeat's targets
// (heartbeatTargets) without its rule leaving out a monitor on the pushing
// site. That rule is about the push proving the site reachable, which a push
// from the monitor's own host cannot; this push carries the group's health,
// which is the same news from any host, and the instance that is active may
// be the one beside a monitor. Empty with no monitor, so nothing is rendered.
func adminsHeartbeatTargets(cfg *config.Config) []heartbeatTarget {
	var out []heartbeatTarget
	for _, name := range cfg.AppNames() {
		app := cfg.Apps[name]
		if app.Kind == config.KindUptime {
			out = append(out, heartbeatTarget{App: name, Hostname: app.Hostname})
		}
	}
	return out
}

// adminsHeartbeatToken is a Pocket ID app's token from the secrets, refused
// by name when it is missing or not what the fork accepts, as a site's
// heartbeat token is (heartbeatToken).
func (p *planner) adminsHeartbeatToken(app string) (string, error) {
	const key = "admins_heartbeat_token"
	token := p.appSecret(app, key)
	if token == "" {
		return "", fmt.Errorf("secrets apps.%s.%s: required for a deployment with a monitor, which expects every instance of %s to push the admins group's health. Run `paisans init` to generate it", app, key, app)
	}
	if !heartbeatTokenShape.MatchString(token) {
		return "", fmt.Errorf("secrets apps.%s.%s: is not 32 lowercase hex characters, the one shape the monitor's seed accepts. It would replace it with a token of its own, and every instance would push at a URL the monitor answers 404 to. Run `paisans init` after removing it, which generates one in the right shape", app, key)
	}
	return token, nil
}

// adminsHeartbeatFiles is the pusher's two files in a Pocket ID app's stack
// on one site, nil when there is no monitor to push to. The URLs carry the
// token, so they go in a 0600 env file only the pusher reads, never the
// app's own .env, which holds the static API key the pusher has no use
// for; the script is a separate 0644 file with no secret in it. A changed
// env file recreates the pusher alone (apply, isEnvironment, and `up -d`
// replacing only the service whose configuration changed), which is how a new
// monitor's URL reaches a host without touching Pocket ID.
func (p *planner) adminsHeartbeatFiles(base string, app plannedApp) ([]File, error) {
	targets := adminsHeartbeatTargets(p.cfg)
	if app.Kind != config.KindPocketID || len(targets) == 0 {
		return nil, nil
	}
	token, err := p.adminsHeartbeatToken(app.Name)
	if err != nil {
		return nil, err
	}
	urls := make([]string, 0, len(targets))
	for _, t := range targets {
		urls = append(urls, HeartbeatURL(t.Hostname, token))
	}
	data := map[string]any{"App": app, "URLs": strings.Join(urls, " "), "ReconcilerPort": reconcilerPort}
	env, err := p.renderTemplate("admins-heartbeat.env.tmpl", data)
	if err != nil {
		return nil, err
	}
	push, err := p.renderTemplate("admins-heartbeat-push.sh.tmpl", data)
	if err != nil {
		return nil, err
	}
	return []File{
		{Path: base + p.dep().RelPath(app.Name, adminsHeartbeatService, "heartbeat.env"), Content: env, Mode: 0o600},
		{Path: base + p.dep().RelPath(app.Name, adminsHeartbeatService, "push.sh"), Content: push, Mode: 0o644},
	}, nil
}
