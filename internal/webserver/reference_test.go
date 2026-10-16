package webserver_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/webserver"
)

var dep = deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}

var status = webserver.Target{App: "status", Site: "watch", Hostname: "status.example.org", Listen: "127.0.0.1:8480", ListenHost: "127.0.0.1", ListenPort: 8480, HealthPath: "/", HealthExpect: "200"}

// One reference file per app for the server admit found, in the
// deployment's own root; one per server when admit found none.
func TestReferences(t *testing.T) {
	refs := webserver.References(dep, status, webserver.WebServer{Server: webserver.ServerNginx})
	if len(refs) != 1 || refs[0].Path != "/srv/paisans/f2a9/web-server/status.conf" || !strings.Contains(refs[0].Content, "proxy_pass http://127.0.0.1:8480") {
		t.Errorf("nginx: %+v", refs)
	}
	refs = webserver.References(dep, status, webserver.WebServer{})
	var paths []string
	for _, r := range refs {
		paths = append(paths, r.Path)
	}
	if strings.Join(paths, " ") != "/srv/paisans/f2a9/web-server/status.caddy /srv/paisans/f2a9/web-server/status.nginx.conf /srv/paisans/f2a9/web-server/status.apache.conf" {
		t.Errorf("unknown server: %v", paths)
	}
	if !strings.Contains(refs[0].Content, "for the host's owner") {
		t.Errorf("header: %s", refs[0].Content)
	}
}

// The record's detection, every field, back into the type Show reads.
func TestFromRecord(t *testing.T) {
	ws := webserver.FromRecord(&admission.WebServer{Server: "caddy", Process: "caddy", Config: "/etc/caddy/Caddyfile", Main: "/etc/caddy/Caddyfile", Certs: "automatic"})
	if ws.Server != webserver.ServerCaddy || ws.Certs != webserver.CertsAutomatic || ws.Main != "/etc/caddy/Caddyfile" {
		t.Errorf("got %+v", ws)
	}
	if ws := webserver.FromRecord(nil); ws.Server != "" {
		t.Errorf("nil record: %+v", ws)
	}
}
