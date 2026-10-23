package validate

import (
	"fmt"
	"net"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// monitorRoles checks where the uptime monitor may run. README.md: "The
// monitor has a site of its own".
//
// A monitor exists to report the failures of everything else, and it cannot
// report the machine it runs on. A machine of its own is the recommendation,
// but a deployment may have no machine to spare, so a monitor beside any other
// role is allowed with one warning that names what it shares and what then
// goes unreported. Founder decision, 2026-10-12, which made the gateway and
// the witness warnings rather than refusals. Storage is left out: a monitor's
// absence hides nothing about it.
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
		var shared []string
		for _, r := range []config.Role{config.RoleGateway, config.RoleWitness, config.RoleData, config.RoleApps} {
			if site.Has(r) {
				shared = append(shared, string(r))
			}
		}
		if len(shared) > 0 {
			witness := ""
			if site.Has(config.RoleWitness) {
				witness = " Nothing reports etcd losing its tiebreaker either."
			}
			c.warn("monitor-shares-a-site", key,
				"monitor shares a site",
				"holds monitor beside %s. It cannot report %s failing: when it goes down, the monitor goes with it and no alert is sent.%s A small machine of its own with roles: [monitor] reports it.", andList(shared), name, witness)
		}
		if !c.hostsUptime(name) {
			c.refuse("monitor-without-uptime", key,
				"monitor site runs no uptime app",
				"holds the monitor role, but no uptime app is pinned to %s, so the site would run nothing the role exists for. Pin the uptime app here, or drop the role.", name)
		}
		if site.PublicAddress == "" {
			c.refuse("monitor-without-public-address", fmt.Sprintf("sites.%s.public_address", name),
				"monitor site has no public address",
				"is not set, and %s holds the monitor role. The monitor serves its own hostname from its own address rather than through the gateway, so the address the internet reaches it on has to be declared: its hostname's DNS record points there. Declare it, for example public_address: 203.0.113.20.", name)
		}
	}
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if app.Blueprint != config.BlueprintUptime || app.Placement.Mode != config.PlacementPinned {
			continue
		}
		site, ok := c.cfg.Sites[app.Placement.Site]
		if !ok || site.Has(config.RoleMonitor) {
			continue // undeclared-site reports a missing one
		}
		c.refuse("uptime-needs-a-monitor-site", fmt.Sprintf("apps.%s.placement", name),
			"uptime app is pinned to a site without the monitor role",
			"pins the monitor to %s, which does not hold the monitor role. The monitor runs on a site holding the monitor role, and serves its own hostname from it. Give a site roles: [monitor] and pin it there.", app.Placement.Site)
	}
}

// nothingWatches warns when the deployment declares no uptime app.
// README.md: "The monitor has a site of its own".
//
// Every other rule here is about where a monitor may run, and a deployment
// with none passes all of them. That is allowed, but it is a risk worth a
// line in every validate: a gateway or a database that dies is then noticed
// by a member, not by a check. A warning rather than a refusal, because the
// deployment is coherent without one.
func (c *checker) nothingWatches() {
	for _, name := range c.cfg.AppNames() {
		if c.cfg.Apps[name].Blueprint == config.BlueprintUptime {
			return
		}
	}
	c.warn("no-uptime-monitor", "apps",
		"no uptime monitor is declared",
		"declares no uptime app, so nothing watches this deployment: a site, the gateway or the database going down is noticed by a member before an admin. Declare an uptime app pinned to a site with the monitor role, ideally a small machine of its own with a public_address, and apply it (README.md, \"Monitoring\").")
}

// hostsUptime reports whether an uptime app is pinned to a site.
func (c *checker) hostsUptime(site string) bool {
	for _, name := range c.cfg.PinnedTo(site) {
		if c.cfg.Apps[name].Blueprint == config.BlueprintUptime {
			return true
		}
	}
	return false
}

// andList joins words as a sentence does: "a", "a and b", "a, b and c".
func andList(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// webServer checks a site's web_server block. README.md: "The monitor has a site
// of its own".
//
// Only a monitor may carry one: a gateway always runs the toolkit's Caddy,
// and every other site is reached through the gateway, so the block would be
// ignored anywhere else. A monitor beside the gateway is served by the
// gateway's Caddy, so it carries none either. listen is where Docker publishes the app for the
// operator's own web server, so it belongs to managed_by external alone, which
// cannot work without it. The web server runs on the same host, so nothing
// beyond it needs the port, and any other address would put the app where
// other machines reach it around the proxy: only loopback or the site's own
// mesh address is accepted, and the mesh address with a warning, since the
// firewall lets the mesh in whole.
// One listen publishes one app, so an external monitor hosts the monitor and
// nothing else: a second app pinned there would be routed by nothing.
// include_dir is refused, since apply writes only into the deployment's own
// root.
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
		if site.Has(config.RoleGateway) {
			c.refuse("web-server-on-gateway", key,
				"web_server is declared on a gateway",
				"is declared on %s, which also holds the gateway role. The gateway's Caddy serves the monitor there, so the block would be ignored, or would hand 80 and 443 to a second web server that Caddy already holds. Remove the block.", name)
			continue
		}
		managedBy, listen := site.WebServerManagedBy(), site.WebServer.Listen
		if !site.WebServer.IncludeDir.Unset() {
			c.refuse("web-server-include-dir", key+".include_dir",
				"web_server include_dir is not a setting",
				"is set. apply writes nothing into your web server's configuration: it writes a reference snippet for each app under %s/web-server/ on the site, and the host's owner installs it in their web server (%s web-server show --app <app> prints it). Remove include_dir.", c.cfg.Deployment().Root(), progname.Cmd())
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
		switch {
		case !config.ProxiesToThisHost(listen, site.Address):
			c.refuse("web-server-listen-not-this-host", key+".listen",
				"web_server listens beyond loopback and the site's mesh address",
				"is %s. Your web server runs on this host and reaches the app on loopback, so nothing beyond the host needs the port, and any other address puts the app where other machines can reach it directly, around your web server, held back only by whatever the host's firewall allows. Use loopback, 127.0.0.1, or this site's own mesh address, %s.", listen, site.Address)
		case strings.HasPrefix(listen, site.Address+":"):
			c.warn("web-server-listen-bypasses-firewall", key+".listen",
				"web_server listens on the mesh address, which every site reaches",
				"is %s, which is not loopback. The firewall lets the mesh in whole, so anything that can reach %s over the mesh, every site of this deployment, can reach the app around your web server. Prefer 127.0.0.1.", listen, site.Address)
		}
	}
}
