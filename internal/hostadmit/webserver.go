package hostadmit

import (
	"encoding/json"
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
// record, which web-server show fills its snippet from. The record is the
// server's, not each app's: one web server fronts every app a request
// hands it, so the first app's hostname is enough to find it. A Caddy
// whose admin API any local user can reach on TCP is warned about with the
// fix; the server is the owner's, so it is never refused.
func planWebServer(p *planner) error {
	apps := p.in.Request.WebServer
	if len(apps) == 0 {
		return nil
	}
	target := webserver.Target{App: apps[0].App, Hostname: apps[0].Hostname, Mode: config.ManagedByExternal, Listen: apps[0].Listen}
	ws := webserver.Detect(webserver.Host{Plain: p.t, Docker: systemDocker}, target)
	var admin caddyAdminState
	if ws.Server == webserver.ServerCaddy {
		admin = caddyTCPAdmin(p, ws)
	}
	p.record.WebServer = recordWebServer(ws, admin.tcp)
	switch {
	case ws.Why != "" && admin.local && strings.Contains(ws.Why, "admin API"):
		// The record keeps Why for web-server show; the owner's Caddy is
		// as it should be, so admit does not warn about it.
		p.present("web server: Caddy's admin API is not on TCP, so admit read none of its certificates")
	case ws.Why != "":
		p.warn("web server: "+hostText(ws.Why), "Admit records what it could tell; paisans web-server show asks you for the rest when the operators run it.")
	default:
		p.present(hostText(ws.Summary()))
	}
	where := hostText(ws.Config)
	if where == "" {
		where = "its Caddyfile"
	}
	switch {
	case admin.tcp != "":
		fix := "Put admin unix//run/caddy-admin.sock, or admin off, in the global options block of " + where
		if strings.HasSuffix(ws.Config, ".json") {
			fix = `Set "admin": {"listen": "unix//run/caddy-admin.sock"}, or "admin": {"disabled": true}, in ` + where
		}
		p.warn("web server: Caddy's admin API answers on TCP at "+hostText(admin.tcp),
			"Any local user, every admitted deployment's user among them, can replace that Caddy's configuration through it. "+fix+", and restart Caddy: a reload sends the new configuration to the new admin address, which is not listening yet.")
	case admin.unread:
		p.warn("web server: Caddy's admin address could not be read",
			"Its command line names no configuration admit could read, and its default endpoint did not answer. Check the admin option in its configuration: on anything but a unix socket, or off, any local user, every admitted deployment's user among them, can replace that Caddy's configuration.")
	}
	return nil
}

// caddyAdminState is where the owner's Caddy serves its admin API: tcp
// is the TCP address a local user reaches, "" for none; local is its
// configuration putting the API on a unix socket or off; unread is a
// Caddy on the host whose configuration admit could not read and whose
// default endpoint did not answer.
type caddyAdminState struct {
	tcp           string
	local, unread bool
}

// hostText is text from the host made safe to print: escape sequences
// shown and never drawn, secrets redacted.
func hostText(s string) string { return ui.Redact(visibleLines(s)) }

// caddyTCPAdmin reads where the owner's Caddy serves its admin API. For a
// Caddy on the host, its configuration, a Caddyfile or JSON, names the
// address first, so one bound wider than localhost is named as it is.
// Its path comes from a process any local user can start, so the file is
// read as root only as a regular file, never under /proc, /sys or /dev,
// where a read can block or consume (/proc/kmsg passes -f), its first 64
// KiB alone and for five seconds at most, and only when every directory
// on its resolved path is root's and writable by root alone, as
// refuseUnsafe holds admit's own paths: below another's, a link could be
// swapped in between the check and the read. Such a path is not read, and
// the plan says so; the server is the owner's, so admit goes on to the
// default endpoint, as it does when the file names no address, or cannot
// be read: that endpoint, when it answers from the host. A Caddy
// in a container whose endpoint the host cannot reach is no local user's
// to reach either.
func caddyTCPAdmin(p *planner, ws webserver.WebServer) caddyAdminState {
	onHost := ws.Container == ""
	if onHost && ws.Config != "" {
		conf, err := p.t.Run(caddyConfigRead(ws.Config))
		if dir, ok := strings.CutPrefix(strings.TrimSpace(conf), unsafeMarker+" "); err == nil && ok {
			p.warn("web server: Caddy's configuration "+hostText(ws.Config)+" was not read",
				hostText(dir)+" is not root's alone, so what the path names could change between admit's check and its read. Check the admin option in it yourself: on anything but a unix socket, or off, any local user can replace that Caddy's configuration.")
		} else if err == nil {
			if addr, set := caddyAdmin(conf); set {
				return caddyAdminState{tcp: addr, local: addr == ""}
			}
		}
	}
	if out, err := p.t.Run("curl -s --max-time 3 " + caddyAdminURL); err == nil && strings.HasPrefix(strings.TrimSpace(out), "{") {
		return caddyAdminState{tcp: "localhost:2019"}
	}
	return caddyAdminState{unread: onHost && ws.Config == ""}
}

// unsafeMarker starts the line caddyConfigRead prints in place of the
// file for a path below a directory that is not root's alone.
const unsafeMarker = "paisans-unsafe"

// caddyConfigRead is the command that reads the Caddy configuration at
// path as caddyTCPAdmin says, printing unsafeMarker and the directory in
// place of the file when a directory above it is another's.
func caddyConfigRead(path string) string {
	return `( f=$(readlink -e -- ` + apply.ShellQuote(path) + `) || exit 1
case $f in /proc/*|/sys/*|/dev/*) exit 1 ;; esac
d=$f
while :; do
  d=${d%/*}; [ -n "$d" ] || d=/
  s=$(stat -c '%u %a' -- "$d") || exit 1
  if [ "${s%% *}" != 0 ] || [ $((0${s#* } & 022)) -ne 0 ]; then echo "` + unsafeMarker + ` $d"; exit 0; fi
  [ "$d" != / ] || break
done
[ -f "$f" ] && timeout 5 head -c 65536 -- "$f" )`
}

// caddyAdmin reads the admin setting of a Caddy configuration: in JSON,
// its admin object; in a Caddyfile, the admin directive of its global
// options block, the block a Caddyfile opens with. It returns the TCP
// address, "" for off or a unix socket, and whether the configuration
// sets one at all.
func caddyAdmin(conf string) (string, bool) {
	if json.Valid([]byte(conf)) {
		var c struct {
			Admin *struct {
				Disabled bool   `json:"disabled"`
				Listen   string `json:"listen"`
			} `json:"admin"`
		}
		if json.Unmarshal([]byte(conf), &c) != nil || c.Admin == nil || !c.Admin.Disabled && c.Admin.Listen == "" {
			return "", false
		}
		if c.Admin.Disabled || strings.HasPrefix(c.Admin.Listen, "unix/") {
			return "", true
		}
		return c.Admin.Listen, true
	}
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
		case depth == 1 && f[0] == "admin" && len(f) >= 2 && f[1] != "{":
			if f[1] == "off" || strings.HasPrefix(f[1], "unix/") {
				return "", true
			}
			return f[1], true
		case strings.HasSuffix(strings.TrimSpace(lines[i]), "{"):
			depth++
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
