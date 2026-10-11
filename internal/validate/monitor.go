package validate

import (
	"fmt"
	"net"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// monitorRoles checks where the uptime monitor may run. README.md: "The
// monitor has a site of its own".
//
// A monitor exists to report the failures of everything else, so it must not
// share a failure with what it reports. On the gateway it would go dark with
// the edge it is meant to watch; on the witness, etcd's tiebreaker and the
// thing that reports etcd losing quorum would fail together, and the witness
// is usually the gateway machine anyway. Both are refused, with no override.
// Beside data or apps it is allowed with a warning: a small deployment may
// have no other machine, and the warning says which failures go unreported.
//
// The monitor serves its own hostname from its own public address, so that
// address must be declared. Founder decision.
func (c *checker) monitorRoles() {
	for _, name := range c.cfg.SiteNames() {
		site := c.cfg.Sites[name]
		if !site.Has(config.RoleMonitor) {
			continue
		}
		key := fmt.Sprintf("sites.%s.roles", name)
		if site.Has(config.RoleGateway) {
			c.refuse("monitor-on-gateway", key,
				"monitor shares a site with the gateway",
				"holds both monitor and gateway. The monitor's job is to report the gateway's failures, and on the same machine it goes dark with the gateway, at exactly the moment it is needed. Put the monitor on another site.")
		}
		if site.Has(config.RoleWitness) {
			c.refuse("monitor-on-witness", key,
				"monitor shares a site with the witness",
				"holds both monitor and witness. etcd's tiebreaker and the thing that reports etcd losing quorum must not fail together, and the witness is usually the gateway machine as well. Put the monitor on another site.")
		}
		if site.Has(config.RoleData) || site.Has(config.RoleApps) {
			c.warn("monitor-shares-a-site", key,
				"monitor shares a site with data or apps",
				"holds monitor beside data or apps. A small deployment may have no other machine, but the monitor cannot report its own host failing, so losing %s takes the database or the apps there down with nothing left to say so. A separate small machine reports it.", name)
		}
		if !c.hostsUptime(name) {
			c.refuse("monitor-without-uptime", key,
				"monitor site runs no uptime app",
				"holds the monitor role, but no app of kind uptime is pinned to %s, so the site would run nothing the role exists for. Pin the uptime app here, or drop the role.", name)
		}
		if site.PublicAddress == "" {
			c.refuse("monitor-without-public-address", fmt.Sprintf("sites.%s.public_address", name),
				"monitor site has no public address",
				"is not set, and %s holds the monitor role. The monitor serves its own hostname from its own address rather than through the gateway, so the address the internet reaches it on has to be declared: its hostname's DNS record points there. Declare it, for example public_address: 203.0.113.20.", name)
		}
	}
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if app.Kind != config.KindUptime || app.Placement.Mode != config.PlacementPinned {
			continue
		}
		site, ok := c.cfg.Sites[app.Placement.Site]
		if !ok || site.Has(config.RoleMonitor) {
			continue // undeclared-site reports a missing one
		}
		c.refuse("uptime-needs-a-monitor-site", fmt.Sprintf("apps.%s.placement", name),
			"uptime app is pinned to a site without the monitor role",
			"pins the monitor to %s, which does not hold the monitor role. The monitor runs on a site of its own role, never the gateway or the witness, and serves its own hostname rather than through the gateway. Give a site roles: [monitor] and pin it there.", app.Placement.Site)
	}
}

// nothingWatches warns when the deployment declares no app of kind uptime.
// README.md: "The monitor has a site of its own".
//
// Every other rule here is about where a monitor may run, and a deployment
// with none passes all of them. That is allowed, since a first install may
// have one machine and the monitor needs a second, but it is a risk worth a
// line in every validate: a gateway or a database that dies is then noticed
// by a member, not by a check. A warning rather than a refusal, because the
// deployment is coherent without one.
func (c *checker) nothingWatches() {
	for _, name := range c.cfg.AppNames() {
		if c.cfg.Apps[name].Kind == config.KindUptime {
			return
		}
	}
	c.warn("no-uptime-monitor", "apps",
		"no uptime monitor is declared",
		"declares no app of kind uptime, so nothing watches this deployment: a site, the gateway or the database going down is noticed by a member before an admin. Give a second machine roles: [monitor] with a public_address, pin an uptime app to it, and apply it (README.md, \"Monitoring\").")
}

// hostsUptime reports whether an uptime app is pinned to a site.
func (c *checker) hostsUptime(site string) bool {
	for _, name := range c.cfg.PinnedTo(site) {
		if c.cfg.Apps[name].Kind == config.KindUptime {
			return true
		}
	}
	return false
}

// webServer checks a site's web_server block. README.md: "The monitor has a site
// of its own".
//
// Only a monitor may carry one: a gateway always runs the toolkit's Caddy,
// and every other site is reached through the gateway, so the block would be
// ignored anywhere else. listen is where Docker publishes the app for the
// operator's own web server, so it belongs to managed_by external alone, which
// cannot work without it. Docker publishes a port in front of ufw, so a
// listen address the internet could reach would expose the app around the
// proxy whatever the firewall says: only loopback, a private LAN address
// (RFC 1918, or carrier grade NAT space as Tailscale uses) or the site's own
// mesh address are accepted, and all but loopback with a warning.
// One listen publishes one app, so an external monitor hosts the monitor and
// nothing else: a second app pinned there would be routed by nothing.
// include_dir is where apply writes that app's file for the operator's web
// server, so it too belongs to managed_by external, and it is written into that
// server's configuration and into shell commands, so it is held to a plain
// absolute path.
func (c *checker) webServer() {
	for _, name := range c.cfg.SiteNames() {
		site := c.cfg.Sites[name]
		if site.WebServer == nil {
			continue
		}
		key := fmt.Sprintf("sites.%s.web_server", name)
		if !site.Has(config.RoleMonitor) {
			c.refuse("web-server-outside-monitor", key,
				"web_server is declared on a site without the monitor role",
				"is declared on %s, which does not hold the monitor role. A gateway always runs the toolkit's Caddy, and every other site is reached through the gateway, so the block would be ignored. Remove it, or give the site the monitor role.", name)
			continue
		}
		managedBy, listen := site.WebServerManagedBy(), site.WebServer.Listen
		if dir := site.WebServer.IncludeDir; !dir.Unset() && managedBy != config.ManagedByExternal {
			c.refuse("web-server-include-dir-managed-by", key+".include_dir",
				"web_server include_dir does not fit its managed_by",
				"is set, but managed_by is paisans, where the toolkit's own Caddy serves the app and no other web server includes anything. Remove include_dir, or set managed_by: external.")
		} else if p := config.IncludeDirProblem(dir.Path); dir.Path != "" && p != "" {
			c.refuse("web-server-include-dir-path", key+".include_dir",
				"web_server include_dir is not a plain absolute path",
				"is %q, which %s. It is written into your web server's configuration and into commands on the host, so it must be an absolute path in its clean form, without spaces, quotes or glob characters, for example /etc/nginx/paisans.d.", dir.Path, p)
		}
		switch {
		case managedBy == config.ManagedByPaisans && listen != "":
			c.refuse("web-server-listen-managed-by", key+".listen",
				"web_server listen does not fit its managed_by",
				"is set, but managed_by is paisans, where the toolkit's own Caddy serves the app and nothing is published for another web server. Remove listen, or set managed_by: external if your own web server is to proxy to it.")
			continue
		case managedBy == config.ManagedByExternal && listen == "":
			c.refuse("web-server-listen-managed-by", key+".listen",
				"web_server listen does not fit its managed_by",
				"is required with managed_by: external. It is where the app is published for your web server to proxy to, for example 127.0.0.1:8480.")
			continue
		case managedBy != config.ManagedByExternal:
			continue
		}
		if _, network, err := net.ParseCIDR(render.WebServerNetwork); err == nil {
			if _, mesh, err := net.ParseCIDR(c.cfg.Mesh.Subnet); err == nil && (mesh.Contains(network.IP) || network.Contains(mesh.IP)) {
				c.refuse("web-server-network-overlaps-mesh", key,
					"web_server network overlaps the mesh subnet",
					"puts the app on the compose network %s, which this toolkit pins so that the address your web server's connections arrive from is known, and mesh.subnet %s overlaps it. The mesh would be routed into a Docker bridge. Declare a mesh outside %s.", render.WebServerNetwork, c.cfg.Mesh.Subnet, render.WebServerNetwork)
			}
		}
		if pinned := c.cfg.PinnedTo(name); len(pinned) > 1 {
			c.refuse("web-server-external-serves-one-app", key+".listen",
				"an external web server serves one app but several are pinned",
				"publishes one app, but %s are pinned to %s. With managed_by: external nothing but your own web server is in front of the site, and it is handed the monitor alone, so the others would be routed by nothing. Pin them elsewhere, or use managed_by: paisans.", strings.Join(pinned, ", "), name)
		}
		host, _, ok := site.WebServer.ListenHostPort()
		if !ok {
			continue // the loader has already reported this
		}
		ip := net.ParseIP(host)
		switch {
		case ip.IsLoopback():
		case host == site.Address || (!c.cfg.Mesh.Contains(host) && (ip.IsPrivate() || cgnat.Contains(ip))):
			c.warn("web-server-listen-bypasses-firewall", key+".listen",
				"web_server listens on a non-loopback address the firewall misses",
				"is %s, which is not loopback. Docker publishes a port with its own iptables rules, in front of ufw, so the firewall does not protect it: anything that can reach %s can reach the app around your web server. Prefer 127.0.0.1 when the web server runs on this machine.", listen, host)
		default:
			c.refuse("web-server-listen-public", key+".listen",
				"web_server listens on a public address",
				"is %s. The app would be published where the proxy is not in front of it, and Docker's rules bypass ufw. Use loopback (127.0.0.1), a private LAN address (RFC 1918, or 100.64.0.0/10 as Tailscale uses), or this site's own mesh address, %s.", listen, site.Address)
		}
	}
}
