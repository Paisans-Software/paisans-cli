package hostadmit

import (
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/webserver"
)

// caddyAdminURL is Caddy's default admin endpoint.
const caddyAdminURL = "http://localhost:2019/config/"

// planWebServer detects, as root, the owner's web server in front of a
// site with web_server.managed_by: external, with the detection web-server
// show runs, since the deployment's user cannot: the system Docker socket,
// ss -p, nginx -T and /etc/letsencrypt are root's. The result goes into the
// record, which web-server show fills its snippet from. A Caddy whose admin
// API any local user can reach on TCP is warned about with the fix; the
// server is the owner's, so it is never refused.
func planWebServer(p *planner) error {
	apps := p.in.Request.WebServer
	if len(apps) == 0 {
		return nil
	}
	target := webserver.Target{App: apps[0].App, Hostname: apps[0].Hostname, Mode: config.ManagedByExternal, Listen: apps[0].Listen}
	ws := webserver.Detect(webserver.Host{Plain: p.t}, target)
	admin := ""
	if ws.Server == webserver.ServerCaddy {
		admin = caddyTCPAdmin(p, ws)
	}
	p.record.WebServer = recordWebServer(ws, admin)
	if ws.Why != "" {
		p.warn("web server: "+hostText(ws.Why), "Admit records what it could tell; paisans web-server show asks you for the rest when the operators run it.")
	} else {
		p.present(hostText(ws.Summary()))
	}
	if admin != "" {
		where := hostText(ws.Config)
		if where == "" {
			where = "its Caddyfile"
		}
		p.warn("web server: Caddy's admin API answers on TCP at "+hostText(admin),
			"Any local user, every admitted deployment's user among them, can replace that Caddy's configuration through it. Put admin unix//run/caddy-admin.sock, or admin off, in the global options block of "+where+", and reload Caddy.")
	}
	return nil
}

// hostText is text from the host made safe to print: escape sequences
// shown and never drawn, secrets redacted.
func hostText(s string) string { return ui.Redact(visibleLines(s)) }

// caddyTCPAdmin is the TCP address the owner's Caddy answers its admin
// API on, "" for none: the default endpoint when it answers from the host,
// or, for a Caddy on the host itself, the admin address its Caddyfile's
// global options name. A Caddy in a container whose endpoint the host
// cannot reach is no local user's to reach either.
func caddyTCPAdmin(p *planner, ws webserver.WebServer) string {
	if out, err := p.t.Run("curl -s --max-time 3 " + caddyAdminURL); err == nil && strings.HasPrefix(strings.TrimSpace(out), "{") {
		return "localhost:2019"
	}
	if ws.Container != "" || ws.Config == "" {
		return ""
	}
	conf, err := p.t.Run("cat " + apply.ShellQuote(ws.Config))
	if err != nil {
		return ""
	}
	if addr, set := caddyAdmin(conf); set && addr != "" {
		return addr
	}
	return ""
}

// caddyAdmin reads the admin directive of a Caddyfile's global options
// block, the block a Caddyfile opens with: its TCP address, "" for off or
// a unix socket, and whether the block sets one at all.
func caddyAdmin(conf string) (string, bool) {
	lines := strings.Split(conf, "\n")
	i := 0
	for i < len(lines) && (strings.TrimSpace(lines[i]) == "" || strings.HasPrefix(strings.TrimSpace(lines[i]), "#")) {
		i++
	}
	if i == len(lines) || strings.TrimSpace(lines[i]) != "{" {
		return "", false
	}
	for depth := 1; i+1 < len(lines) && depth > 0; {
		i++
		f := strings.Fields(lines[i])
		switch {
		case len(f) == 0:
		case f[0] == "}":
			depth--
		case strings.HasSuffix(strings.TrimSpace(lines[i]), "{"):
			depth++
		case depth == 1 && f[0] == "admin" && len(f) >= 2:
			if f[1] == "off" || strings.HasPrefix(f[1], "unix/") {
				return "", true
			}
			return f[1], true
		}
	}
	return "", false
}

// recordWebServer is what the record keeps of a detection: strings only,
// so the record reads the same to any toolkit of its version. The strings
// are the host's, so each is made safe to print.
func recordWebServer(ws webserver.WebServer, adminTCP string) *admission.WebServer {
	certs := map[webserver.Certs]string{webserver.CertsAutomatic: "automatic", webserver.CertsFiles: "files", webserver.CertsFolder: "folder"}[ws.Certs]
	return &admission.WebServer{
		Server: string(ws.Server), Container: hostText(ws.Container), Process: hostText(ws.Process),
		Config: hostText(ws.Config), Main: hostText(ws.Main), Certs: certs,
		CertFile: hostText(ws.CertFile), KeyFile: hostText(ws.KeyFile), Folder: hostText(ws.Folder), Certbot: hostText(ws.Certbot),
		AdminTCP: hostText(adminTCP), Why: hostText(ws.Why),
	}
}
