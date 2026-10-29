// Package webserver helps put a monitor site's app behind the web server
// the host's owner runs: which server it is (Detect, which site admit runs
// as root and records), what that server must do, filled in for this
// deployment (Show, and the reference files apply writes in the
// deployment's own root), and whether it works, checked from the
// operator's machine (Check).
//
// Show and the reference files read paisans-stack.yaml and the admission
// record alone, and Detect only reads the host. Check resolves a name,
// makes two HTTPS requests and opens one TCP connection, as any visitor
// could. Nothing here writes, tests or reloads the owner's web server: the
// toolkit renders, the owner installs.
package webserver

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// Target is one app a monitor site serves, and what its web server needs.
type Target struct {
	App      string
	Site     string
	Hostname string
	Mode     config.ManagedBy
	// Listen is where the app is published for the operator's web server,
	// as written in paisans-stack.yaml, and ListenHost and ListenPort the same
	// split. All three are empty with managed_by: paisans.
	Listen     string
	ListenHost string
	ListenPort int
	// PublicAddress and PublicAddress6 are where the hostname must resolve.
	PublicAddress  string
	PublicAddress6 string
	// HealthPath and HealthExpect are the blueprint's health route and the
	// statuses that mean it is up, from blueprints.HealthFor.
	HealthPath   string
	HealthExpect string
	// PublicStatusPage is an uptime app whose public status page is on
	// (blueprints.PublicStatusPage), so /status* is not refused.
	PublicStatusPage bool
}

// External reports whether a web server the operator runs is in front of
// the app.
func (t Target) External() bool { return t.Mode == config.ManagedByExternal }

// Upstream is the URL the operator's web server proxies to.
func (t Target) Upstream() string { return "http://" + t.Listen }

// For builds the target for an app pinned to a monitor site. Every other app
// is served by the gateway's Caddy, which the toolkit runs, so there is
// nothing to hand off for it.
func For(cfg *config.Config, app string) (Target, error) {
	a, ok := cfg.Apps[app]
	if !ok {
		return Target{}, fmt.Errorf("web-server: %s declares no app %q. Declared apps are %s", cfg.Path, app, strings.Join(cfg.AppNames(), ", "))
	}
	if blueprints.Hostless(a.Blueprint) {
		return Target{}, fmt.Errorf("web-server: %s is a %s app, whose blueprint says hostname: none: it serves no website, so no web server is in front of it", app, a.Blueprint)
	}
	site, ok := render.ServedBy(cfg, app)
	if !ok {
		return Target{}, fmt.Errorf("web-server: %s is served by the gateway's Caddy, which the toolkit runs, so there is nothing to hand off. Only an app pinned to a site holding the monitor role has one", app)
	}
	s := cfg.Sites[site]
	health, _ := blueprints.HealthFor(a.Blueprint)
	t := Target{
		App: app, Site: site, Hostname: a.Hostname, Mode: s.WebServerManagedBy(),
		PublicAddress: s.PublicAddress, PublicAddress6: s.PublicAddress6,
		HealthPath: health.Path, HealthExpect: health.Expect,
		PublicStatusPage: a.Blueprint == config.BlueprintUptime && blueprints.PublicStatusPage(a),
	}
	if t.HealthPath == "" {
		t.HealthPath, t.HealthExpect = "/", "200"
	}
	if t.External() && s.WebServer != nil {
		host, port, ok := s.WebServer.ListenHostPort()
		if !ok {
			return Target{}, fmt.Errorf("web-server: sites.%s.web_server.listen %q is not an address and a port", site, s.WebServer.Listen)
		}
		t.Listen, t.ListenHost, t.ListenPort = s.WebServer.Listen, host, port
	}
	return t, nil
}
