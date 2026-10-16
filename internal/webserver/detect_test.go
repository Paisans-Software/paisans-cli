package webserver_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/webserver"
)

// fakeHost answers a command by the first key it contains, in order, and
// keeps every command it was asked. A command no key matches fails as a
// missing program would.
type fakeHost struct {
	answers []answer
	asked   []string
}

type answer struct {
	key string
	out string
	err error
}

func (f *fakeHost) Run(command string) (string, error) {
	f.asked = append(f.asked, command)
	for _, a := range f.answers {
		if strings.Contains(command, a.key) {
			return a.out, a.err
		}
	}
	return "sh: not found", errors.New("exit status 127")
}

func (f *fakeHost) ran(key string) bool {
	for _, c := range f.asked {
		if strings.Contains(c, key) {
			return true
		}
	}
	return false
}

const caddyPS = "caddy-1\tcaddy:2.8\t0.0.0.0:80->80/tcp, 0.0.0.0:443->443/tcp, [::]:443->443/tcp\ndb\tpostgres:16\t5432/tcp\n"

const caddyMounts = `[{"Type":"bind","Source":"/opt/caddy/Caddyfile","Destination":"/etc/caddy/Caddyfile"},{"Type":"volume","Source":"/var/lib/docker/volumes/caddy_data/_data","Destination":"/data"}]`

// caddyAutomatic is a Caddy that obtains its own certificates, with a
// second server bound to a private address only.
const caddyAutomatic = `{"apps":{"http":{"servers":{
  "srv0":{"listen":[":443"],"routes":[{"match":[{"host":["blog.example.org"]}]}]},
  "srv1":{"listen":["100.64.0.7:443"],"routes":[{"match":[{"host":["admin.example.org"]}]}]}
}}}}`

const caddyFiles = `{"apps":{"http":{"servers":{
  "srv0":{"listen":[":443"],"routes":[{"match":[{"host":["blog.example.org"]}]}]}
}},"tls":{"certificates":{"load_files":[
  {"certificate":"/etc/ssl/blog.example.org/fullchain.pem","key":"/etc/ssl/blog.example.org/privkey.pem"}
]}}}}`

func show(t *testing.T, ws webserver.WebServer) string {
	t.Helper()
	var buf bytes.Buffer
	webserver.Show(&buf, external(t, "127.0.0.1:8480"), ws)
	return buf.String()
}

// A Caddy in a container that publishes 443, which obtains its own
// certificates: its admin API, reached inside the container, says so, and
// the sheet is its snippet alone, with an issuer of its own and no
// certificate files.
func TestDetectsADockerCaddyThatObtainsItsOwnCertificates(t *testing.T) {
	plain := &fakeHost{answers: []answer{
		{key: "docker ps", out: caddyPS},
		{key: "docker inspect", out: caddyMounts},
		{key: "curl", err: errors.New("exit status 7")},
		{key: "docker exec caddy-1 wget", out: caddyAutomatic},
	}}
	sudo := &fakeHost{}
	ws := webserver.Detect(webserver.Host{Plain: plain, Sudo: sudo}, external(t, "127.0.0.1:8480"))
	if ws.Server != webserver.ServerCaddy || ws.Container != "caddy-1" || ws.Config != "/opt/caddy/Caddyfile" || ws.Certs != webserver.CertsAutomatic {
		t.Fatalf("got %+v", ws)
	}
	if ws.Main != "/etc/caddy/Caddyfile" || len(ws.Mounts) != 2 || ws.Mounts[0] != (webserver.Mount{Source: "/opt/caddy/Caddyfile", Destination: "/etc/caddy/Caddyfile"}) {
		t.Errorf("main %q, mounts %+v", ws.Main, ws.Mounts)
	}
	if len(sudo.asked) != 0 {
		t.Errorf("sudo was used: %v", sudo.asked)
	}
	want := "web server: caddy (docker container caddy-1, config /opt/caddy/Caddyfile), obtains its own certificates"
	if got := ws.Summary(); got != want {
		t.Errorf("summary\n got %q\nwant %q", got, want)
	}
	out := show(t, ws)
	if !strings.Contains(out, want) || !strings.Contains(out, "health.example.org {") {
		t.Errorf("sheet:\n%s", out)
	}
	if !strings.Contains(out, "\ttls {\n    \t\tissuer acme\n") {
		t.Errorf("sheet has no issuer of its own:\n%s", out)
	}
	for _, absent := range []string{"\ttls /", "# tls", "proxy_pass", "VirtualHost", "YOUR CERTIFICATE"} {
		if strings.Contains(out, absent) {
			t.Errorf("sheet has %q:\n%s", absent, out)
		}
	}
	if !strings.Contains(out, "100.64.0.7:443") || !strings.Contains(out, ":443") {
		t.Errorf("sheet does not say which listener the site belongs on:\n%s", out)
	}
}

// A Caddy that loads certificate files for its other sites gets a tls line
// following their pattern.
func TestDetectsADockerCaddyThatLoadsCertificateFiles(t *testing.T) {
	plain := &fakeHost{answers: []answer{
		{key: "docker ps", out: caddyPS},
		{key: "docker inspect", out: caddyMounts},
		{key: "curl", out: caddyFiles},
	}}
	ws := webserver.Detect(webserver.Host{Plain: plain}, external(t, "127.0.0.1:8480"))
	if ws.Server != webserver.ServerCaddy || ws.Certs != webserver.CertsFiles {
		t.Fatalf("got %+v", ws)
	}
	out := show(t, ws)
	if !strings.Contains(out, "tls /etc/ssl/health.example.org/fullchain.pem /etc/ssl/health.example.org/privkey.pem") {
		t.Errorf("sheet:\n%s", out)
	}
	if strings.Contains(out, "proxy_pass") {
		t.Errorf("sheet has the nginx snippet:\n%s", out)
	}
}

// docker refused to the ssh user is asked again through sudo.
func TestDetectAsksDockerThroughSudoWhenDenied(t *testing.T) {
	plain := &fakeHost{answers: []answer{
		{key: "docker", out: "permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock", err: errors.New("exit status 1")},
	}}
	sudo := &fakeHost{answers: []answer{
		{key: "docker ps", out: "web\tnginx:1.27\t0.0.0.0:443->443/tcp\n"},
		{key: "docker inspect", out: `[{"Source":"/srv/web/nginx.conf","Destination":"/etc/nginx/nginx.conf"}]`},
		{key: "docker exec web nginx -T", out: "# configuration file /etc/nginx/nginx.conf:\nserver_name blog.example.org;\nssl_certificate /etc/ssl/blog.example.org/cert.pem;\nssl_certificate_key /etc/ssl/blog.example.org/key.pem;\n"},
	}}
	ws := webserver.Detect(webserver.Host{Plain: plain, Sudo: sudo}, external(t, "127.0.0.1:8480"))
	if !sudo.ran("docker ps") || ws.Server != webserver.ServerNginx || ws.Container != "web" || ws.Config != "/srv/web/nginx.conf" {
		t.Fatalf("got %+v\nsudo asked %v", ws, sudo.asked)
	}
	if ws.CertFile != "/etc/ssl/health.example.org/cert.pem" || ws.KeyFile != "/etc/ssl/health.example.org/key.pem" {
		t.Errorf("certificate files %q %q", ws.CertFile, ws.KeyFile)
	}
}

// docker refusing the ssh user leaves the server undetermined, and says why,
// naming no flag.
func TestDetectSaysDockerRefusesTheSSHUser(t *testing.T) {
	plain := &fakeHost{answers: []answer{
		{key: "docker", out: "permission denied while trying to connect to the Docker daemon socket", err: errors.New("exit status 1")},
		{key: "ss -ltnp", out: "State Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n"},
	}}
	ws := webserver.Detect(webserver.Host{Plain: plain}, external(t, "127.0.0.1:8480"))
	if ws.Server != "" || !strings.Contains(ws.Why, "docker refuses the ssh user") || strings.Contains(ws.Why, "--sudo") {
		t.Fatalf("got %+v", ws)
	}
}

// nginx on the host, found by its listening socket, with certificates from
// certbot: the sheet names the certificate certbot will write for the new
// name and the command that obtains it the way the others were.
func TestDetectsABareMetalNginxWithCertbot(t *testing.T) {
	plain := &fakeHost{answers: []answer{
		{key: "docker", out: "__PAISANS_NO_DOCKER__\n"},
	}}
	sudo := &fakeHost{answers: []answer{
		{key: "ss -ltnp", out: "State  Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n" +
			"LISTEN 0      511    0.0.0.0:80        0.0.0.0:*     users:((\"nginx\",pid=812,fd=7),(\"nginx\",pid=811,fd=7))\n" +
			"LISTEN 0      511    0.0.0.0:443       0.0.0.0:*     users:((\"nginx\",pid=812,fd=6),(\"nginx\",pid=811,fd=6))\n" +
			"LISTEN 0      4096   127.0.0.1:8480    0.0.0.0:*     users:((\"docker-proxy\",pid=990,fd=4))\n"},
		{key: "nginx -T", out: "nginx: the configuration file /etc/nginx/nginx.conf syntax is ok\n# configuration file /etc/nginx/nginx.conf:\nhttp {\n# configuration file /etc/nginx/sites-enabled/blog:\nserver {\n    server_name blog.example.org;\n    ssl_certificate /etc/letsencrypt/live/blog.example.org/fullchain.pem; # managed by Certbot\n    ssl_certificate_key /etc/letsencrypt/live/blog.example.org/privkey.pem; # managed by Certbot\n}\n"},
		{key: "renewal", out: "authenticator = nginx\n"},
	}}
	ws := webserver.Detect(webserver.Host{Plain: plain, Sudo: sudo}, external(t, "127.0.0.1:8480"))
	if ws.Server != webserver.ServerNginx || ws.Container != "" || ws.Process != "nginx" || ws.Config != "/etc/nginx/nginx.conf" || ws.Main != ws.Config {
		t.Fatalf("got %+v", ws)
	}
	if ws.CertFile != "/etc/letsencrypt/live/health.example.org/fullchain.pem" || ws.KeyFile != "/etc/letsencrypt/live/health.example.org/privkey.pem" {
		t.Errorf("certificate files %q %q", ws.CertFile, ws.KeyFile)
	}
	if ws.Certbot != "certbot certonly --nginx -d health.example.org" {
		t.Errorf("certbot %q", ws.Certbot)
	}
	out := show(t, ws)
	for _, want := range []string{
		"web server: nginx (process nginx, config /etc/nginx/nginx.conf)",
		"ssl_certificate     /etc/letsencrypt/live/health.example.org/fullchain.pem;",
		"ssl_certificate_key /etc/letsencrypt/live/health.example.org/privkey.pem;",
		"certbot certonly --nginx -d health.example.org",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	for _, absent := range []string{"reverse_proxy", "VirtualHost", "YOUR CERTIFICATE"} {
		if strings.Contains(out, absent) {
			t.Errorf("sheet has %q:\n%s", absent, out)
		}
	}
}

// A webroot certbot is named with its webroot.
func TestCertbotKeepsTheWebroot(t *testing.T) {
	plain := &fakeHost{answers: []answer{{key: "docker", out: "__PAISANS_NO_DOCKER__\n"}}}
	sudo := &fakeHost{answers: []answer{
		{key: "ss -ltnp", out: "LISTEN 0 511 *:443 *:* users:((\"nginx\",pid=812,fd=6))\n"},
		{key: "nginx -T", out: "# configuration file /etc/nginx/nginx.conf:\nssl_certificate /etc/letsencrypt/live/blog.example.org/fullchain.pem;\nssl_certificate_key /etc/letsencrypt/live/blog.example.org/privkey.pem;\n"},
		{key: "renewal", out: "authenticator = webroot\nwebroot_path = /var/www/html,\n"},
	}}
	ws := webserver.Detect(webserver.Host{Plain: plain, Sudo: sudo}, external(t, "127.0.0.1:8480"))
	if ws.Certbot != "certbot certonly --webroot -w /var/www/html -d health.example.org" {
		t.Errorf("certbot %q", ws.Certbot)
	}
}

// With nothing to go on the sheet is the generic one, every snippet, with
// Caddy's tls line commented out and a line saying what is not known.
func TestUnknownWebServerFallsBackToEverySnippet(t *testing.T) {
	plain := &fakeHost{answers: []answer{
		{key: "docker", out: "__PAISANS_NO_DOCKER__\n"},
		{key: "ss -ltnp", out: "LISTEN 0 4096 127.0.0.1:8480 0.0.0.0:*\n"},
	}}
	ws := webserver.Detect(webserver.Host{Plain: plain}, external(t, "127.0.0.1:8480"))
	if ws.Server != "" || ws.Why == "" {
		t.Fatalf("got %+v", ws)
	}
	out := show(t, ws)
	for _, want := range []string{
		"web server: not determined",
		"\t# tls /etc/ssl/health.example.org/fullchain.pem /etc/ssl/health.example.org/privkey.pem",
		"reverse_proxy 127.0.0.1:8480",
		"proxy_pass http://127.0.0.1:8480;",
		"ProxyPass        / http://127.0.0.1:8480/",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

// A host ssh cannot reach is a server not determined, with why.
func TestDetectOnAnUnreachableHost(t *testing.T) {
	down := errors.New("watch cannot be reached over ssh")
	plain := &fakeHost{answers: []answer{{key: "", err: down}}}
	ws := webserver.Detect(webserver.Host{Plain: plain}, external(t, "127.0.0.1:8480"))
	if ws.Server != "" || !strings.Contains(ws.Why, "cannot be reached") {
		t.Fatalf("got %+v", ws)
	}
}

// Detection only reads: no command it runs writes, reloads or restarts.
func TestDetectOnlyReads(t *testing.T) {
	plain := &fakeHost{answers: []answer{
		{key: "docker ps", out: caddyPS},
		{key: "docker inspect", out: caddyMounts},
		{key: "curl", out: caddyFiles},
	}}
	webserver.Detect(webserver.Host{Plain: plain}, external(t, "127.0.0.1:8480"))
	for _, c := range plain.asked {
		for _, verb := range []string{"reload", "restart", "POST", "PATCH", "PUT", "DELETE", "rm ", "tee ", ">"} {
			if strings.Contains(c, verb) && !strings.Contains(c, "2>/dev/null") && !strings.Contains(c, ">/dev/null") {
				t.Errorf("%q writes (%s)", c, verb)
			}
		}
	}
}

// detectCaddy is Detect on a Caddy in a container that obtains its own
// certificates, whose Caddyfile reads conf.
func detectCaddy(t *testing.T, conf string) webserver.WebServer {
	t.Helper()
	plain := &fakeHost{answers: []answer{
		{key: "docker ps", out: caddyPS},
		{key: "docker inspect", out: caddyMounts},
		{key: "curl", out: caddyAutomatic},
		{key: "docker exec caddy-1 cat '/etc/caddy/Caddyfile'", out: conf},
	}}
	ws := webserver.Detect(webserver.Host{Plain: plain}, external(t, "127.0.0.1:8480"))
	if ws.Certs != webserver.CertsAutomatic {
		t.Fatalf("got %+v", ws)
	}
	return ws
}

// The global options say whether a DNS challenge applies to every site:
// through cert_issuer, which the site's own issuer replaces, or through
// acme_dns, which Caddy gives every ACME issuer, the site's included.
func TestDetectReadsTheGlobalDNSChallenge(t *testing.T) {
	const head = "web server: caddy (docker container caddy-1, config /opt/caddy/Caddyfile), obtains its own certificates"
	for _, c := range []struct {
		name, conf string
		dns        webserver.GlobalDNS
		summary    string
	}{
		{"no challenge", "{\n\temail admin@example.org\n}\nimport /etc/caddy/paisans.d/*\n", webserver.DNSNone, head + " (this site uses HTTP-01)"},
		{"no global options", "example.org {\n\trespond ok\n}\n", webserver.DNSNone, head + " (this site uses HTTP-01)"},
		{"commented out", "{\n\t# acme_dns cloudflare {$CF_API_TOKEN}\n}\n", webserver.DNSNone, head + " (this site uses HTTP-01)"},
		{"cert_issuer without dns", "{\n\tcert_issuer acme {\n\t\tdir https://acme.example.org/directory\n\t}\n}\n", webserver.DNSNone, head + " (this site uses HTTP-01)"},
		{"cert_issuer", "{\n\temail admin@example.org\n\tcert_issuer acme {\n\t\tdns cloudflare {$CF_API_TOKEN}\n\t}\n}\n", webserver.DNSCertIssuer, head + " (DNS-01 globally; this site uses HTTP-01)"},
		{"cert_issuer on one line", "{\n\tcert_issuer acme { dns cloudflare {$CF_API_TOKEN} }\n}\n", webserver.DNSCertIssuer, head + " (DNS-01 globally; this site uses HTTP-01)"},
		{"acme_dns", "{\n\temail admin@example.org\n\tacme_dns cloudflare {$CF_API_TOKEN}\n}\n", webserver.DNSACME, head + " (DNS-01 globally through acme_dns, this site included)"},
		{"acme_dns beside cert_issuer", "{\n\tcert_issuer acme {\n\t\tdns cloudflare {$CF_API_TOKEN}\n\t}\n\tacme_dns cloudflare {$CF_API_TOKEN}\n}\n", webserver.DNSACME, head + " (DNS-01 globally through acme_dns, this site included)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ws := detectCaddy(t, c.conf)
			if ws.DNS != c.dns {
				t.Errorf("dns %v, want %v", ws.DNS, c.dns)
			}
			if got := ws.Summary(); got != c.summary {
				t.Errorf("summary\n got %q\nwant %q", got, c.summary)
			}
		})
	}
}

// With Host.Docker set, every docker command Detect runs starts with it;
// without, with plain docker.
func TestDetectDockerCommand(t *testing.T) {
	for docker, prefix := range map[string]string{"": "docker", "docker -H unix:///run/x.sock": "docker -H unix:///run/x.sock"} {
		plain := &fakeHost{answers: []answer{
			{key: " ps --format", out: caddyPS},
			{key: " inspect --format", out: caddyMounts},
			{key: "curl", err: errors.New("exit status 7")},
			{key: " exec caddy-1 wget", out: caddyAutomatic},
		}}
		webserver.Detect(webserver.Host{Plain: plain, Docker: docker}, external(t, "127.0.0.1:8480"))
		for _, want := range []string{prefix + " ps --format", prefix + " inspect --format", prefix + " exec caddy-1"} {
			if !plain.ran(want) {
				t.Errorf("Docker %q: no command contains %q in %q", docker, want, plain.asked)
			}
		}
	}
}
