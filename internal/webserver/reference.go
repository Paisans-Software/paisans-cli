package webserver

import (
	"fmt"
	"path"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// ReferenceDir is where apply writes, in the deployment's own root, the
// snippet the host's owner installs in their web server for each app it
// serves. The deployment's user cannot write the owner's configuration, test
// it or reload the server, and nothing grants it that: the toolkit renders,
// the owner installs.
func ReferenceDir(d deployment.Deployment) string { return d.Path("web-server") }

// OwnReference reports whether p, as a manifest's web_server entry holds
// it, is one of d's reference files: absolute, clean, and directly in
// ReferenceDir. The manifest is the deployment user's own file, so an entry
// that is not is never read or deleted, whoever runs the command.
func OwnReference(d deployment.Deployment, p string) bool {
	if !strings.HasPrefix(p, "/") || path.Clean(p) != p || strings.Contains(p, "..") {
		return false
	}
	return path.Dir(p) == ReferenceDir(d)
}

// Reference is one reference file: an app's snippet for one server.
type Reference struct {
	App, Path, Content string
	Server             Server
}

// References is app's reference files: one for the server site admit found
// in front of the site, or, when it found none it knows, one for each of
// Caddy, nginx and Apache, so the owner has the right one either way.
func References(d deployment.Deployment, t Target, ws WebServer) []Reference {
	one := func(name string, s Server) Reference {
		w := ws
		w.Server = s
		return Reference{App: t.App, Path: ReferenceDir(d) + "/" + name, Content: FileContent(t, w), Server: s}
	}
	switch ws.Server {
	case ServerCaddy:
		return []Reference{one(t.App+".caddy", ServerCaddy)}
	case ServerNginx:
		return []Reference{one(t.App+".conf", ServerNginx)}
	case ServerApache:
		return []Reference{one(t.App+".conf", ServerApache)}
	}
	return []Reference{one(t.App+".caddy", ServerCaddy), one(t.App+".nginx.conf", ServerNginx), one(t.App+".apache.conf", ServerApache)}
}

// FileContent is a reference file for t: a header saying whose it is and
// what the owner does with it, then the snippet web-server show prints.
func FileContent(t Target, ws WebServer) string {
	header := fmt.Sprintf("# %s, the paisans app %s, written by paisans deployment apply for the host's owner.\n# Copy it into %s's configuration. apply rewrites it when "+config.FileName+" changes, and says so: then replace your copy.\n", t.Hostname, t.App, ws.Server)
	switch ws.Server {
	case ServerCaddy:
		return header + caddySnippet(t, ws)
	case ServerNginx:
		return header + nginxSnippet(t, ws)
	case ServerApache:
		return header + apacheSnippet(t, ws)
	}
	return ""
}

// FromRecord is the web server site admit detected, as the admission record
// holds it, in the form Show and the check read. A record with none is an
// unknown server.
func FromRecord(r *admission.WebServer) WebServer {
	if r == nil {
		return WebServer{}
	}
	certs := map[string]Certs{"automatic": CertsAutomatic, "files": CertsFiles, "folder": CertsFolder}[r.Certs]
	return WebServer{
		Server: Server(r.Server), Container: r.Container, Process: r.Process,
		Config: r.Config, Main: r.Main, Certs: certs,
		CertFile: r.CertFile, KeyFile: r.KeyFile, Folder: r.Folder, Certbot: r.Certbot, Why: r.Why,
	}
}
