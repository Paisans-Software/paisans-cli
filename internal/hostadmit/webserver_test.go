package hostadmit_test

import (
	"slices"
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
	goldenCase{name: "webserver-caddy-host-tcp-admin", focus: []string{"admission record"}, site: "status", host: func() *fakeHost {
		return onHost("caddy")().answer("localhost:2019/config/", caddyAPI)
	}},
	goldenCase{name: "webserver-caddy-host-unix-admin", focus: []string{"admission record"}, site: "status", host: func() *fakeHost {
		return onHost("caddy")().answer("ps -o args= -C caddy", "caddy run --config /etc/caddy/Caddyfile\n").
			answer("readlink -e -- '/etc/caddy/Caddyfile'", "{\n\tadmin unix//run/caddy-admin.sock\n}\nexample.org {\n}\n")
	}},
	goldenCase{name: "webserver-caddy-container-tcp-admin", focus: []string{"admission record"}, site: "status", host: func() *fakeHost {
		return inContainer("caddy:2")().answer("localhost:2019/config/", caddyAPI)
	}},
	goldenCase{name: "webserver-caddy-container-internal-admin", focus: []string{"admission record"}, site: "status", host: inContainer("caddy:2")},
	goldenCase{name: "webserver-nginx-host", focus: []string{"admission record"}, site: "status", host: func() *fakeHost {
		return onHost("nginx")().answer("nginx -T", "# configuration file /etc/nginx/nginx.conf:\nserver {\n  server_name status.example.org;\n  ssl_certificate /etc/letsencrypt/live/status.example.org/fullchain.pem;\n  ssl_certificate_key /etc/letsencrypt/live/status.example.org/privkey.pem;\n}\n")
	}},
	goldenCase{name: "webserver-nginx-container", focus: []string{"admission record"}, site: "status", host: func() *fakeHost {
		return inContainer("nginx:1.27")().answer("nginx -T", "# configuration file /etc/nginx/nginx.conf:\n")
	}},
	goldenCase{name: "webserver-apache-host", focus: []string{"admission record"}, site: "status", host: func() *fakeHost {
		return onHost("apache2")().answer("grep -rhiE", "SSLCertificateFile /etc/ssl/certs/status.pem\nSSLCertificateKeyFile /etc/ssl/private/status.key\nServerName status.example.org\n")
	}},
	goldenCase{name: "webserver-apache-container", focus: []string{"admission record"}, site: "status", host: inContainer("httpd:2.4")},
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
		{`{"admin":{"listen":"0.0.0.0:2019"},"apps":{}}`, "0.0.0.0:2019", true},
		{`{"admin":{"disabled":true}}`, "", true},
		{`{"admin":{"listen":"unix//run/caddy.sock"}}`, "", true},
		{`{"apps":{}}`, "", false},
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
			answer("readlink -e -- '/etc/caddy/Caddyfile'", conf)
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

// The configuration Caddy runs with names its admin address before the
// default endpoint does, so a Caddy bound wider than localhost is named
// as it is; a Caddy on the host whose configuration admit cannot find is
// warned about, since its admin address was not read; a Caddy whose
// admin API is off or on a unix socket is present, not warned about, and
// the configuration is read bounded in size and time, only as a regular
// file and never under /proc, /sys or /dev, where a read can block.
func TestCaddyAdminIsReadFromItsConfiguration(t *testing.T) {
	caddy := func(args, read, conf string) *fakeHost {
		h := onHost("caddy")().answer("localhost:2019/config/", caddyAPI)
		if args != "" {
			h.answer("ps -o args= -C caddy", args+"\n").answer(read, conf)
		}
		return h
	}
	for _, c := range []struct {
		name       string
		host       *fakeHost
		warn, miss string
	}{
		{"a Caddyfile binding every address", caddy("caddy run --config /etc/caddy/Caddyfile", "readlink -e -- '/etc/caddy/Caddyfile'", "{\n\tadmin 0.0.0.0:2019\n}\n"), "answers on TCP at 0.0.0.0:2019", "localhost"},
		{"a JSON configuration", caddy("caddy run --config /etc/caddy/caddy.json", "readlink -e -- '/etc/caddy/caddy.json'", `{"admin":{"listen":"0.0.0.0:2019"}}`), "answers on TCP at 0.0.0.0:2019", "localhost"},
		{"no configuration on its command line", onHost("caddy")(), "admin address could not be read", "answers on TCP"},
	} {
		plan, err := hostadmit.Build(c.host, input(t, "status", hostadmit.Flags{}))
		if err != nil {
			t.Fatal(err)
		}
		var hints []string
		for _, w := range plan.Warnings {
			hints = append(hints, w.Hint)
		}
		all := strings.Join(hints, "\n")
		if !strings.Contains(all, c.warn) || strings.Contains(all, c.miss) {
			t.Errorf("%s: warnings\n%s", c.name, all)
		}
		for _, w := range plan.Warnings {
			if c.name == "a JSON configuration" && strings.Contains(w.Hint, "answers on TCP") && !strings.Contains(w.Detail, `"admin": {"listen": "unix//run/caddy-admin.sock"}`) {
				t.Errorf("a JSON configuration is given a Caddyfile's fix: %q", w.Detail)
			}
		}
	}
	h := caseNamed(t, "webserver-caddy-host-unix-admin").host()
	plan, err := hostadmit.Build(h, input(t, "status", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range plan.Warnings {
		if strings.HasPrefix(w.Hint, "web server:") {
			t.Errorf("a Caddy with its admin API on a unix socket is warned about: %q", w.Hint)
		}
	}
	if !slices.Contains(plan.Present, "web server: Caddy's admin API is not on TCP, so admit read none of its certificates") {
		t.Errorf("present: %q", plan.Present)
	}
	for _, c := range h.ran {
		if !strings.Contains(c, "readlink -e -- '/etc/caddy/Caddyfile'") {
			continue
		}
		for _, want := range []string{"/proc/*|/sys/*|/dev/*) exit 1", "stat -c '%u %a'", `[ -f "$f" ] && timeout 5 head -c 65536 -- "$f"`} {
			if !strings.Contains(c, want) {
				t.Errorf("the configuration's read lacks %q: %s", want, c)
			}
		}
	}
	plan, _ = hostadmit.Build(caseNamed(t, "webserver-caddy-host-tcp-admin").host(), input(t, "status", hostadmit.Flags{}))
	for _, w := range plan.Warnings {
		if strings.Contains(w.Hint, "answers on TCP") && (!strings.Contains(w.Detail, "restart Caddy") || strings.Contains(w.Detail, "reload Caddy")) {
			t.Errorf("the fix does not say to restart Caddy: %q", w.Detail)
		}
	}
}

// The configuration is read only when every directory on its resolved path
// is root's and writable by root alone: below another's, a link could be
// swapped in between the check and the read. Such a path is skipped with a
// warning naming it, and admit goes on.
func TestCaddyConfigBelowAnothersDirectoryIsNotRead(t *testing.T) {
	h := onHost("caddy")().answer("ps -o args= -C caddy", "caddy run --config /srv/www/Caddyfile\n").
		answer("readlink -e -- '/srv/www/Caddyfile'", "paisans-unsafe /srv/www\n")
	plan, err := hostadmit.Build(h, input(t, "status", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	var warned bool
	for _, w := range plan.Warnings {
		warned = warned || strings.Contains(w.Hint, "/srv/www/Caddyfile was not read") && strings.Contains(w.Detail, "/srv/www is not root's alone")
	}
	if !warned {
		t.Errorf("no warning naming the path:\n%s", plan.Text())
	}
	for _, c := range h.ran {
		if strings.Contains(c, "readlink -e -- '/srv/www/Caddyfile'") && !strings.Contains(c, "stat -c '%u %a'") {
			t.Errorf("the read checks no directory above the file: %s", c)
		}
	}
}

// A configuration skipped as below another's directory still leaves the
// default endpoint to probe: a Caddy answering on localhost:2019 is
// warned about as any other.
func TestCaddyConfigNotReadStillProbesTheDefaultEndpoint(t *testing.T) {
	h := onHost("caddy")().answer("ps -o args= -C caddy", "caddy run --config /srv/www/Caddyfile\n").
		answer("readlink -e -- '/srv/www/Caddyfile'", "paisans-unsafe /srv/www\n").
		answer("curl -s --max-time 3", "{}\n")
	plan, err := hostadmit.Build(h, input(t, "status", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	var tcp bool
	for _, w := range plan.Warnings {
		tcp = tcp || strings.Contains(w.Hint, "answers on TCP")
	}
	if !tcp {
		t.Errorf("the default endpoint was not probed:\n%s", plan.Text())
	}
}
