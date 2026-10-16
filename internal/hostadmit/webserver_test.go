package hostadmit_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

const caddyAPI = `{"apps":{"http":{"servers":{"srv0":{"listen":[":443"]}}},"tls":{}}}`

// onHost is a web server running as a process on the host, listening on 443.
func onHost(process string) func() *fakeHost {
	return func() *fakeHost {
		return newBlank().answer("ps --format", "").
			answer("ss -ltnp", `LISTEN 0 511 0.0.0.0:443 0.0.0.0:* users:(("`+process+`",pid=700,fd=6))`+"\n")
	}
}

// inContainer is one in the owner's Docker, publishing 443.
func inContainer(image string) func() *fakeHost {
	return func() *fakeHost {
		return newBlank().with("system-socket", "present").
			answer("ps --format", "edge\t"+image+"\t0.0.0.0:443->443/tcp\n").
			answer("inspect --format", "[]")
	}
}

var webserverCases = addCases(
	goldenCase{name: "webserver-caddy-host-tcp-admin", site: "status", host: func() *fakeHost {
		return onHost("caddy")().answer("localhost:2019/config/", caddyAPI)
	}},
	goldenCase{name: "webserver-caddy-host-unix-admin", site: "status", host: func() *fakeHost {
		return onHost("caddy")().answer("ps -o args= -C caddy", "caddy run --config /etc/caddy/Caddyfile\n").
			answer("cat '/etc/caddy/Caddyfile'", "{\n\tadmin unix//run/caddy-admin.sock\n}\nexample.org {\n}\n")
	}},
	goldenCase{name: "webserver-caddy-container-tcp-admin", site: "status", host: func() *fakeHost {
		return inContainer("caddy:2")().answer("localhost:2019/config/", caddyAPI)
	}},
	goldenCase{name: "webserver-caddy-container-internal-admin", site: "status", host: inContainer("caddy:2")},
	goldenCase{name: "webserver-nginx-host", site: "status", host: func() *fakeHost {
		return onHost("nginx")().answer("nginx -T", "# configuration file /etc/nginx/nginx.conf:\nserver {\n  server_name status.example.org;\n  ssl_certificate /etc/letsencrypt/live/status.example.org/fullchain.pem;\n  ssl_certificate_key /etc/letsencrypt/live/status.example.org/privkey.pem;\n}\n")
	}},
	goldenCase{name: "webserver-nginx-container", site: "status", host: func() *fakeHost {
		return inContainer("nginx:1.27")().answer("nginx -T", "# configuration file /etc/nginx/nginx.conf:\n")
	}},
	goldenCase{name: "webserver-apache-host", site: "status", host: func() *fakeHost {
		return onHost("apache2")().answer("grep -rhiE", "SSLCertificateFile /etc/ssl/certs/status.pem\nSSLCertificateKeyFile /etc/ssl/private/status.key\nServerName status.example.org\n")
	}},
	goldenCase{name: "webserver-apache-container", site: "status", host: inContainer("httpd:2.4")},
)

func TestWebServerPlans(t *testing.T) { runGolden(t, webserverCases) }

// A Caddy whose admin API answers on TCP is warned about with the fix; one
// on a unix socket is not.
func TestCaddyAdminAPIWarning(t *testing.T) {
	for name, want := range map[string]bool{"webserver-caddy-host-tcp-admin": true, "webserver-caddy-host-unix-admin": false, "webserver-caddy-container-tcp-admin": true, "webserver-caddy-container-internal-admin": false} {
		for _, c := range webserverCases {
			if c.name != name {
				continue
			}
			plan, err := hostadmit.Build(c.host(), input(t, c.site, c.flags))
			if err != nil {
				t.Fatal(err)
			}
			var warned bool
			for _, w := range plan.Warnings {
				warned = warned || strings.Contains(w.Detail, "admin unix//run/caddy-admin.sock")
			}
			if warned != want {
				t.Errorf("%s: warned %v, want %v", name, warned, want)
			}
		}
	}
}

// The Caddyfile's global options name the admin endpoint; a site block's
// admin-looking text does not.
func TestCaddyAdmin(t *testing.T) {
	for _, c := range []struct {
		conf, addr string
		set        bool
	}{
		{"{\n\tadmin :2020\n}\n", ":2020", true},
		{"{\n\tadmin off\n}\n", "", true},
		{"{\n\tadmin unix//run/caddy.sock\n}\n", "", true},
		{"{\n\temail a@example.org\n}\nexample.org {\n\trespond admin\n}\n", "", false},
		{"example.org {\n}\n", "", false},
		{"{\n\tadmin :2020 {\n\t\torigins localhost\n\t}\n}\n", ":2020", true},
	} {
		addr, set := hostadmit.CaddyAdmin(c.conf)
		if addr != c.addr || set != c.set {
			t.Errorf("%q: %q %v, want %q %v", c.conf, addr, set, c.addr, c.set)
		}
	}
}

// What the host prints about its web server reaches the owner's terminal
// and the record only after ui.Visible: an escape sequence in a process
// name is shown and never drawn.
func TestWebServerHostTextIsVisible(t *testing.T) {
	h := onHost("evil\x1b[2J")()
	plan, err := hostadmit.Build(h, input(t, "status", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range plan.Warnings {
		if strings.ContainsRune(w.Hint+w.Detail, '\x1b') {
			t.Errorf("warning carries a raw escape: %q", w.Hint)
		}
	}
}

func recordOf(t *testing.T, c goldenCase) *admission.WebServer {
	t.Helper()
	rec, err := hostadmit.RecordOf(c.host(), input(t, c.site, c.flags))
	if err != nil {
		t.Fatal(err)
	}
	return rec.WebServer
}

func caseNamed(t *testing.T, name string) goldenCase {
	t.Helper()
	for _, c := range webserverCases {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("no case %s", name)
	return goldenCase{}
}

// The record keeps what web-server show reads: the server, where it runs,
// how it has certificates and the TCP admin address.
func TestWebServerRecord(t *testing.T) {
	for name, want := range map[string]admission.WebServer{
		"webserver-caddy-host-tcp-admin":           {Server: "caddy", Process: "caddy", Certs: "automatic", AdminTCP: "localhost:2019"},
		"webserver-caddy-host-unix-admin":          {Server: "caddy", Process: "caddy", Config: "/etc/caddy/Caddyfile", Main: "/etc/caddy/Caddyfile", Why: "its admin API did not answer on localhost:2019"},
		"webserver-caddy-container-tcp-admin":      {Server: "caddy", Container: "edge", Main: "/etc/caddy/Caddyfile", Certs: "automatic", AdminTCP: "localhost:2019"},
		"webserver-caddy-container-internal-admin": {Server: "caddy", Container: "edge", Main: "/etc/caddy/Caddyfile", Why: "its admin API did not answer on localhost:2019"},
		"webserver-nginx-host":                     {Server: "nginx", Process: "nginx", Config: "/etc/nginx/nginx.conf", Main: "/etc/nginx/nginx.conf", Certs: "files", CertFile: "/etc/letsencrypt/live/status.example.org/fullchain.pem", KeyFile: "/etc/letsencrypt/live/status.example.org/privkey.pem", Certbot: "certbot certonly --nginx -d status.example.org"},
	} {
		got := recordOf(t, caseNamed(t, name))
		if got == nil || *got != want {
			t.Errorf("%s: record %+v, want %+v", name, got, want)
		}
	}
}

// A request without web_server leaves the record's web server nil.
func TestWebServerRecordNilWithoutRequest(t *testing.T) {
	rec, err := hostadmit.RecordOf(newBlank(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	if rec.WebServer != nil {
		t.Errorf("record %+v, want nil", rec.WebServer)
	}
}

// Admit asks Docker through the system socket, never the environment's.
func TestWebServerDockerIsTheSystemSocket(t *testing.T) {
	h := caseNamed(t, "webserver-nginx-container").host()
	if _, err := hostadmit.Build(h, input(t, "status", hostadmit.Flags{})); err != nil {
		t.Fatal(err)
	}
	var docker int
	for _, c := range h.ran {
		if strings.Contains(c, "docker ps") || strings.Contains(c, "docker inspect") || strings.Contains(c, "docker exec") {
			t.Errorf("plain docker: %s", c)
		}
		if strings.Contains(c, "docker -H unix:///var/run/docker.sock ") {
			docker++
		}
	}
	if docker < 3 {
		t.Errorf("%d commands name the system socket, want ps, inspect and exec: %q", docker, h.ran)
	}
}

// A Caddyfile's admin directive is read end to end, whatever its form.
func TestCaddyAdminFromTheCaddyfile(t *testing.T) {
	for conf, want := range map[string]string{
		"{\n\tadmin :2020\n}\n":                               ":2020",
		"{\n\tadmin :2020 {\n\t\torigins localhost\n\t}\n}\n": ":2020",
		"{\n\tadmin off\n}\n":                                 "",
	} {
		h := onHost("caddy")().answer("ps -o args= -C caddy", "caddy run --config /etc/caddy/Caddyfile\n").
			answer("cat '/etc/caddy/Caddyfile'", conf)
		rec, err := hostadmit.RecordOf(h, input(t, "status", hostadmit.Flags{}))
		if err != nil {
			t.Fatal(err)
		}
		if rec.WebServer.AdminTCP != want {
			t.Errorf("%q: AdminTCP %q, want %q", conf, rec.WebServer.AdminTCP, want)
		}
		plan, _ := hostadmit.Build(h, input(t, "status", hostadmit.Flags{}))
		warned := false
		for _, w := range plan.Warnings {
			warned = warned || strings.Contains(w.Hint, "answers on TCP")
		}
		if warned != (want != "") {
			t.Errorf("%q: warned %v", conf, warned)
		}
	}
}
