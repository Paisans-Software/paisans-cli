package webserver

import (
	"encoding/json"
	"net"
	"regexp"
	"slices"
	"strings"
)

// Runner runs a command on the monitor's host and returns its output. A
// non zero exit is an error, with the output still returned.
type Runner interface {
	Run(command string) (string, error)
}

// Host is how Detect reaches the monitor's machine over ssh: Plain as the
// ssh user, and Sudo through sudo, nil when sudo is not to be used. Sudo is
// used only where Plain cannot read what is needed, so a host that needs
// none is never asked for a password.
type Host struct {
	Plain Runner
	// Sudo is set by no command: each reads its web server as the ssh user.
	Sudo Runner
	// Docker is the command that starts each docker command Detect runs,
	// "docker" when empty. Admit sets it to name the system socket.
	Docker string
}

// docker is the command every docker command Detect runs starts with.
func (h Host) docker() string {
	if h.Docker == "" {
		return "docker"
	}
	return h.Docker
}

// Server is a web server Detect recognises.
type Server string

const (
	ServerCaddy   Server = "caddy"
	ServerNginx   Server = "nginx"
	ServerApache  Server = "apache"
	ServerTraefik Server = "traefik"
	ServerHAProxy Server = "haproxy"
)

// Certs is how the web server comes by its certificates.
type Certs int

const (
	// CertsUnknown is not determined: WebServer.Why says why.
	CertsUnknown Certs = iota
	// CertsAutomatic is a Caddy that obtains its own.
	CertsAutomatic
	// CertsFiles is a server that loads files: CertFile and KeyFile are the
	// ones for the target's hostname, following the pattern its others
	// use, or empty when they follow none.
	CertsFiles
	// CertsFolder is a Caddy that loads every certificate in Folder.
	CertsFolder
)

// GlobalDNS is the DNS challenge a Caddy's global options set.
type GlobalDNS int

const (
	// DNSUnread is a Caddyfile that was not read.
	DNSUnread GlobalDNS = iota
	// DNSNone is global options that set no DNS challenge.
	DNSNone
	// DNSCertIssuer is a DNS challenge in a global cert_issuer, which the
	// toolkit's site, with an issuer of its own, does not use.
	DNSCertIssuer
	// DNSACME is a global acme_dns, which Caddy gives the toolkit's site
	// too: WebServer.ACMEDNS is the directive.
	DNSACME
)

// WebServer is what Detect found in front of the app's host, enough for Show
// to print only the snippet that applies and for the operator to check it.
type WebServer struct {
	Server Server
	// Container is the docker container it runs in, and Process the process
	// listening on 443 or 80 on the host itself; one of them is set.
	Container string
	Process   string
	// Config is its configuration's path on the host, where known, and
	// Main the same as the server itself reads it: inside the container,
	// for one in a container.
	Config string
	Main   string
	// Mounts are the container's mounts, host path and container path.
	Mounts []Mount
	Certs  Certs
	// CertFile and KeyFile are the certificate files for the target's
	// hostname, with CertsFiles; Example is one of the server's own, when
	// they follow no pattern to name the target's by.
	CertFile, KeyFile string
	Example           string
	// Folder is the folder a Caddy loads its certificates from.
	Folder string
	// Certbot obtains the certificate for the target's hostname, when
	// certbot obtained the server's others.
	Certbot string
	// Listen is the address of the Caddy server the site belongs on, and
	// Private each of its listeners bound to a private address only.
	Listen  string
	Private []string
	// DNS is the DNS challenge a Caddy that obtains its own certificates
	// sets in its global options, and ACMEDNS the acme_dns directive's
	// lines, as written, when that is how.
	DNS     GlobalDNS
	ACMEDNS []string
	// Why is what could not be determined, and why.
	Why string
}

// Summary is the line the sheet opens its snippet with: what was found and
// where, so the operator can check it before copying anything.
func (w WebServer) Summary() string {
	if w.Server == "" {
		why := w.Why
		if why == "" {
			why = "the host was not read"
		}
		return "web server: not determined (" + why + ")"
	}
	var where []string
	if w.Container != "" {
		where = append(where, "docker container "+w.Container)
	} else if w.Process != "" {
		where = append(where, "process "+w.Process)
	}
	if w.Config != "" {
		where = append(where, "config "+w.Config)
	}
	s := "web server: " + string(w.Server)
	if len(where) > 0 {
		s += " (" + strings.Join(where, ", ") + ")"
	}
	switch {
	case w.Certs == CertsAutomatic:
		s += ", obtains its own certificates"
		switch w.DNS {
		case DNSNone:
			s += " (this site uses HTTP-01)"
		case DNSCertIssuer:
			s += " (DNS-01 globally; this site uses HTTP-01)"
		case DNSACME:
			s += " (DNS-01 globally through acme_dns, this site included)"
		}
	case w.Certs == CertsFolder:
		s += ", loads its certificates from " + w.Folder
	case w.Certbot != "":
		s += ", certificates from certbot"
	case w.Certs == CertsFiles:
		s += ", loads its certificates from files"
	case w.Why != "":
		s += ", certificates not determined: " + w.Why
	}
	return s
}

// noDocker is what the docker listing prints on a host without docker.
const noDocker = "__PAISANS_NO_DOCKER__"

func dockerPS(docker string) string {
	return "if command -v docker >/dev/null 2>&1; then " + docker + " ps --format '{{.Names}}\t{{.Image}}\t{{.Ports}}'; else echo " + noDocker + "; fi"
}

// Detect finds the web server in front of t's app on its host, reading
// only: a container publishing 443 or 80, asked as the ssh user and then
// through sudo when docker refuses it; otherwise the process listening on
// 443 or 80; then, for Caddy, its admin API, and for nginx and Apache the
// certificate files their configuration names. Whatever it cannot read
// leaves that part undetermined, with why, and never fails the caller.
func Detect(h Host, t Target) WebServer {
	var ws WebServer
	docker, containers, why := listContainers(h)
	for _, port := range []string{"443", "80"} {
		for _, c := range containers {
			if slices.Contains(c.ports, port) {
				if s := serverOf(c.image); s != "" {
					ws.Server, ws.Container = s, c.name
					break
				}
			}
		}
		if ws.Server != "" {
			break
		}
	}
	if ws.Server == "" {
		process, ok, ssWhy := listening(h)
		switch {
		case ok:
			ws.Server, ws.Process = serverOf(process), process
			// A container on the host's network publishes nothing:
			// its process listens itself, under its own name.
			for _, c := range containers {
				if len(c.ports) == 0 && serverOf(c.image) == ws.Server && ws.Server != "" {
					ws.Container, ws.Process = c.name, ""
					break
				}
			}
			if ws.Server == "" {
				ws.Why = process + " listens on 443, which is not a web server this knows"
				return ws
			}
		case why != "":
			ws.Why = why
			return ws
		default:
			ws.Why = ssWhy
			return ws
		}
	}
	if ws.Container != "" {
		ws.Mounts = containerMounts(docker, h.docker(), ws.Container)
		ws.Config = containerConfig(ws.Mounts, ws.Server)
		if paths := configPaths[ws.Server]; len(paths) > 0 {
			ws.Main = paths[0]
		}
	}
	exec := func(command string) (string, error) {
		if ws.Container != "" {
			return docker.Run(h.docker() + " exec " + ws.Container + " " + command)
		}
		return read(h, command)
	}
	switch ws.Server {
	case ServerCaddy:
		caddy(h, exec, &ws, t)
	case ServerNginx:
		out, err := exec("nginx -T 2>&1")
		if err != nil {
			ws.Why = "nginx -T failed: " + firstLine(out, err)
			break
		}
		if m := regexp.MustCompile(`(?m)^# configuration file (\S+):$`).FindStringSubmatch(out); m != nil {
			if ws.Config == "" {
				ws.Config = m[1]
			}
			ws.Main = m[1]
		}
		files(h, &ws, t, out, `ssl_certificate`, `ssl_certificate_key`, `server_name`, "--nginx")
	case ServerApache:
		dir := "/etc/apache2 /etc/httpd"
		if ws.Container != "" {
			dir = "/usr/local/apache2/conf"
		} else if ws.Config == "" {
			ws.Config = map[string]string{"apache2": "/etc/apache2/apache2.conf", "httpd": "/etc/httpd/conf/httpd.conf"}[ws.Process]
		}
		out, _ := exec("grep -rhiE '^[[:space:]]*(SSLCertificateFile|SSLCertificateKeyFile|ServerName|ServerAlias)[[:space:]]' " + dir + " 2>/dev/null")
		files(h, &ws, t, out, `SSLCertificateFile`, `SSLCertificateKeyFile`, `Server(?:Name|Alias)`, "--apache")
	default:
		ws.Why = "there is no snippet for " + string(ws.Server)
	}
	if ws.Main == "" && ws.Container == "" {
		ws.Main = ws.Config
	}
	return ws
}

// Mount is one of a container's mounts.
type Mount struct{ Source, Destination string }

type container struct {
	name, image string
	ports       []string
}

var hostPort = regexp.MustCompile(`:(\d+)->`)

// listContainers lists the running containers, as the ssh user and then
// through sudo when docker refuses it, and returns the runner that could.
// why is set when docker is there but could not be listed.
func listContainers(h Host) (Runner, []container, string) {
	runner := h.Plain
	out, err := runner.Run(dockerPS(h.docker()))
	if err != nil && strings.Contains(strings.ToLower(out+err.Error()), "permission denied") {
		if h.Sudo == nil {
			return nil, nil, "docker refuses the ssh user"
		}
		runner = h.Sudo
		out, err = runner.Run(dockerPS(h.docker()))
	}
	if err != nil {
		return nil, nil, firstLine(out, err)
	}
	if strings.TrimSpace(out) == noDocker {
		return nil, nil, ""
	}
	var list []container
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 2 {
			continue
		}
		c := container{name: f[0], image: f[1]}
		if len(f) > 2 {
			for _, m := range hostPort.FindAllStringSubmatch(f[2], -1) {
				if !slices.Contains(c.ports, m[1]) {
					c.ports = append(c.ports, m[1])
				}
			}
		}
		list = append(list, c)
	}
	return runner, list, ""
}

var ssProcess = regexp.MustCompile(`users:\(\("([^"]+)"`)

// listening names the process listening on 443, or else on 80, on the host
// itself. ss names another user's process only to root, so it runs through
// sudo when there is sudo.
func listening(h Host) (string, bool, string) {
	runner := h.Sudo
	if runner == nil {
		runner = h.Plain
	}
	out, err := runner.Run("ss -ltnp")
	if err != nil {
		return "", false, firstLine(out, err)
	}
	for _, port := range []string{"443", "80"} {
		for _, line := range strings.Split(out, "\n") {
			f := strings.Fields(line)
			if len(f) < 4 || f[0] != "LISTEN" || !strings.HasSuffix(f[3], ":"+port) {
				continue
			}
			if m := ssProcess.FindStringSubmatch(line); m != nil {
				return m[1], true, ""
			}
		}
	}
	why := "nothing found listening on 443 or 80"
	if h.Sudo == nil {
		why += ", and without sudo ss names no other user's process"
	}
	return "", false, why
}

// serverOf is the web server an image or a process name is.
func serverOf(name string) Server {
	name = strings.ToLower(name)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name, _, _ = strings.Cut(name, ":")
	for _, s := range []struct {
		server Server
		names  []string
	}{
		{ServerCaddy, []string{"caddy"}},
		{ServerNginx, []string{"nginx", "openresty"}},
		{ServerApache, []string{"httpd", "apache"}},
		{ServerTraefik, []string{"traefik"}},
		{ServerHAProxy, []string{"haproxy"}},
	} {
		for _, n := range s.names {
			if strings.Contains(name, n) {
				return s.server
			}
		}
	}
	return ""
}

// configPaths is where each server's image reads its configuration.
var configPaths = map[Server][]string{
	ServerCaddy:   {"/etc/caddy/Caddyfile"},
	ServerNginx:   {"/etc/nginx/nginx.conf", "/etc/nginx/conf.d"},
	ServerApache:  {"/usr/local/apache2/conf/httpd.conf"},
	ServerTraefik: {"/etc/traefik/traefik.yml"},
	ServerHAProxy: {"/usr/local/etc/haproxy/haproxy.cfg"},
}

// containerMounts reads a container's mounts.
func containerMounts(docker Runner, command, name string) []Mount {
	out, err := docker.Run(command + " inspect --format '{{json .Mounts}}' " + name)
	if err != nil {
		return nil
	}
	var mounts []Mount
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &mounts) != nil {
		return nil
	}
	return mounts
}

// containerConfig is the host path of a container's configuration: the
// mount its image reads it from.
func containerConfig(mounts []Mount, s Server) string {
	for _, path := range configPaths[s] {
		for _, m := range mounts {
			switch {
			case m.Destination == path:
				return m.Source
			case strings.HasPrefix(path, strings.TrimSuffix(m.Destination, "/")+"/"):
				return m.Source + strings.TrimPrefix(path, strings.TrimSuffix(m.Destination, "/"))
			}
		}
	}
	return ""
}

// caddyJSON is the part of Caddy's configuration, as its admin API serves
// it, that says where certificates come from and which servers listen where.
type caddyJSON struct {
	Apps struct {
		HTTP struct {
			Servers map[string]struct {
				Listen []string `json:"listen"`
				Routes []struct {
					Match []struct {
						Host []string `json:"host"`
					} `json:"match"`
				} `json:"routes"`
			} `json:"servers"`
		} `json:"http"`
		TLS struct {
			Certificates struct {
				LoadFiles []struct {
					Certificate string `json:"certificate"`
					Key         string `json:"key"`
				} `json:"load_files"`
				LoadFolders []string `json:"load_folders"`
			} `json:"certificates"`
		} `json:"tls"`
	} `json:"apps"`
}

const caddyAdmin = "http://localhost:2019/config/"

// caddy reads a Caddy's configuration from its admin API: on the host, and
// for a container that publishes its ports, inside the container.
func caddy(h Host, exec func(string) (string, error), ws *WebServer, t Target) {
	out, err := h.Plain.Run("curl -s --max-time 3 " + caddyAdmin)
	if (err != nil || !strings.HasPrefix(strings.TrimSpace(out), "{")) && ws.Container != "" {
		out, err = exec("wget -qO- -T 3 " + caddyAdmin)
	}
	if ws.Config == "" && ws.Container == "" {
		if args, err := h.Plain.Run("ps -o args= -C caddy"); err == nil {
			if m := regexp.MustCompile(`--config\s+(\S+)`).FindStringSubmatch(args); m != nil {
				ws.Config = m[1]
			}
		}
	}
	var cfg caddyJSON
	if err != nil || json.Unmarshal([]byte(strings.TrimSpace(out)), &cfg) != nil {
		ws.Why = "its admin API did not answer on localhost:2019"
		return
	}
	var hosts, names []string
	for name, s := range cfg.Apps.HTTP.Servers {
		names = append(names, name)
		for _, r := range s.Routes {
			for _, m := range r.Match {
				hosts = append(hosts, m.Host...)
			}
		}
	}
	slices.Sort(names)
	for _, name := range names {
		for _, l := range cfg.Apps.HTTP.Servers[name].Listen {
			l = strings.TrimPrefix(l, "tcp/")
			host, port, err := net.SplitHostPort(l)
			if err != nil || port != "443" {
				continue
			}
			if public(host, t) {
				if ws.Listen == "" {
					ws.Listen = l
				}
			} else {
				ws.Private = append(ws.Private, l)
			}
		}
	}
	certs := cfg.Apps.TLS.Certificates
	switch {
	case len(certs.LoadFiles) > 0:
		ws.Certs = CertsFiles
		f := certs.LoadFiles[0]
		cert, ok1 := renamed(f.Certificate, hosts, t.Hostname)
		key, ok2 := renamed(f.Key, hosts, t.Hostname)
		if ok1 && ok2 {
			ws.CertFile, ws.KeyFile = cert, key
		} else {
			ws.Example = f.Certificate
		}
	case len(certs.LoadFolders) > 0:
		ws.Certs, ws.Folder = CertsFolder, certs.LoadFolders[0]
	default:
		ws.Certs = CertsAutomatic
		main := ws.Main
		if main == "" {
			main = ws.Config
		}
		if main == "" {
			return
		}
		if conf, err := exec("cat " + quote(main)); err == nil {
			ws.DNS, ws.ACMEDNS = globalDNS(conf)
		}
	}
}

// globalDNS reads the DNS challenge a Caddyfile's global options block
// sets: an acme_dns directive, with its lines as written, or a dns
// subdirective in a cert_issuer.
func globalDNS(conf string) (GlobalDNS, []string) {
	depth, options := caddyBlocks(conf)
	if options < 0 {
		return DNSNone, nil
	}
	lines := strings.Split(conf, "\n")
	dns, issuer := DNSNone, false
	for i := options + 1; i < len(lines) && depth[i] > 0; i++ {
		toks := caddyTokens(lines[i])
		if len(toks) == 0 {
			continue
		}
		switch {
		case depth[i] == 1 && toks[0] == "acme_dns":
			found := []string{strings.TrimSpace(lines[i])}
			for j := i + 1; j < len(lines) && depth[j] > 1; j++ {
				found = append(found, strings.TrimSpace(lines[j]))
			}
			return DNSACME, found
		case depth[i] == 1:
			issuer = toks[0] == "cert_issuer"
			if issuer && slices.Contains(toks, "dns") {
				dns = DNSCertIssuer
			}
		case issuer && depth[i] == 2 && toks[0] == "dns":
			dns = DNSCertIssuer
		}
	}
	return dns, nil
}

// public reports whether a listener's host takes connections from the
// internet: every address, or a public one.
func public(host string, t Target) bool {
	if host == "" || host == "0.0.0.0" || host == "::" || host == t.PublicAddress || host == t.PublicAddress6 {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !cgnat.Contains(ip)
}

// files reads the certificate files an nginx or Apache configuration names,
// from conf, its directives as the patterns cert, key and names, and names
// the target's by the same pattern. Under /etc/letsencrypt/live/ they are
// certbot's, and the command that obtains the target's is named too.
func files(h Host, ws *WebServer, t Target, conf, cert, key, names, plugin string) {
	directive := func(name string) []string {
		var out []string
		re := regexp.MustCompile(`(?mi)^\s*` + name + `\s+([^;\s#]+(?:\s+[^;\s#]+)*)\s*;?`)
		for _, m := range re.FindAllStringSubmatch(conf, -1) {
			out = append(out, strings.Fields(m[1])...)
		}
		return out
	}
	certs, keys := directive(cert), directive(key)
	if len(certs) == 0 || len(keys) == 0 {
		ws.Why = "its configuration names no certificate file"
		return
	}
	ws.Certs = CertsFiles
	const live = "/etc/letsencrypt/live/"
	if strings.HasPrefix(certs[0], live) {
		ws.CertFile, ws.KeyFile = live+t.Hostname+"/fullchain.pem", live+t.Hostname+"/privkey.pem"
		ws.Certbot = certbot(h, t, plugin)
		return
	}
	hosts := directive(names)
	c, ok1 := renamed(certs[0], hosts, t.Hostname)
	k, ok2 := renamed(keys[0], hosts, t.Hostname)
	if ok1 && ok2 {
		ws.CertFile, ws.KeyFile = c, k
		return
	}
	ws.Example = certs[0]
}

// certbot is the command that obtains a certificate for the target's
// hostname as certbot obtained the others, from their renewal settings, or
// with the server's own plugin when those cannot be read.
func certbot(h Host, t Target, plugin string) string {
	out, err := read(h, "grep -hE '^(authenticator|webroot_path) *=' /etc/letsencrypt/renewal/*.conf")
	method := plugin
	if err == nil {
		values := map[string]string{}
		for _, line := range strings.Split(out, "\n") {
			k, v, ok := strings.Cut(line, "=")
			if ok {
				if _, seen := values[strings.TrimSpace(k)]; !seen {
					values[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), ",")
				}
			}
		}
		switch a := values["authenticator"]; {
		case a == "webroot" && values["webroot_path"] != "":
			method = "--webroot -w " + strings.Split(values["webroot_path"], ",")[0]
		case a != "":
			method = "--" + a
		}
	}
	return "certbot certonly " + method + " -d " + t.Hostname
}

// renamed is path with the first of hosts it contains replaced by hostname.
func renamed(path string, hosts []string, hostname string) (string, bool) {
	for _, host := range hosts {
		if host != "" && !strings.ContainsAny(host, "*$~") && strings.Contains(path, host) {
			return strings.Replace(path, host, hostname, 1), true
		}
	}
	return "", false
}

// read runs a command as the ssh user, and through sudo when that fails.
func read(h Host, command string) (string, error) {
	out, err := h.Plain.Run(command)
	if err != nil && h.Sudo != nil {
		return h.Sudo.Run(command)
	}
	return out, err
}

// firstLine is the first line of what a failed command said, or of its
// error when it said nothing.
func firstLine(out string, err error) string {
	text := strings.TrimSpace(out)
	if text == "" {
		text = strings.TrimSpace(err.Error())
	}
	line, _, _ := strings.Cut(text, "\n")
	return strings.TrimRight(line, ".:")
}
