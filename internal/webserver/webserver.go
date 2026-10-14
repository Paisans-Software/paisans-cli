// Package webserver helps an operator put a monitor site's app behind a web
// server: what that server must do, filled in for this deployment (Show),
// which server it is (Detect), the file that does it written into a
// directory the server includes (Sync), and whether it works, checked from
// the operator's machine (Check).
//
// Show reads paisans-stack.yaml alone, and Detect only reads the host. Check
// resolves a name, makes two HTTPS requests and opens one TCP connection, as
// any visitor could. Sync and Remove are the one part that changes a host,
// and they change only the toolkit's own files in the include directory,
// each recorded in the deployment's manifest, never the server's own
// configuration: the operator adds the one line that includes the
// directory.
package webserver

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
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
	// HealthPath and HealthExpect are the kind's health route and the
	// statuses that mean it is up, from blueprints.HealthFor.
	HealthPath   string
	HealthExpect string
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
