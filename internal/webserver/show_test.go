package webserver_test

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/webserver"
)

// fixture is the render fixture: status pinned to watch, a monitor in
// managed_by: paisans with public_address 203.0.113.20.
func fixture(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// external is the fixture's monitor behind the operator's web server.
func external(t *testing.T, listen string) webserver.Target {
	t.Helper()
	cfg := fixture(t)
	watch := cfg.Sites["watch"]
	watch.WebServer = &config.WebServer{ManagedBy: config.ManagedByExternal, Listen: listen}
	cfg.Sites["watch"] = watch
	target, err := webserver.For(cfg, "status")
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func TestForReadsTheMonitor(t *testing.T) {
	target := external(t, "127.0.0.1:8480")
	want := webserver.Target{
		App: "status", Site: "watch", Hostname: "status.example.org",
		Mode: config.ManagedByExternal, Listen: "127.0.0.1:8480", ListenHost: "127.0.0.1", ListenPort: 8480,
		PublicAddress: "203.0.113.20", HealthPath: "/healthz", HealthExpect: "200",
	}
	if target != want {
		t.Fatalf("got  %+v\nwant %+v", target, want)
	}
}

// Only an app a monitor serves has a web server to hand off: the gateway's
// apps are the toolkit's own Caddy's.
func TestForRefusesAnAppTheGatewayServes(t *testing.T) {
	cfg := fixture(t)
	if _, err := webserver.For(cfg, "talk"); err == nil || !strings.Contains(err.Error(), "gateway") {
		t.Fatalf("talk: %v", err)
	}
	if _, err := webserver.For(cfg, "nothing"); err == nil || !strings.Contains(err.Error(), "declares no app") {
		t.Fatalf("an undeclared app: %v", err)
	}
}

func TestShowHandsOffAnExternalApp(t *testing.T) {
	var buf bytes.Buffer
	webserver.Show(&buf, external(t, "127.0.0.1:8480"), webserver.WebServer{})
	out := buf.String()
	for _, want := range []string{
		"status.example.org", "http://127.0.0.1:8480", "/healthz",
		"terminate TLS", "Host", "X-Forwarded-For", "X-Forwarded-Proto",
		"203.0.113.20",
		"reverse_proxy 127.0.0.1:8480",
		"proxy_pass http://127.0.0.1:8480;",
		"proxy_set_header X-Forwarded-Proto $scheme;",
		"ProxyPass        / http://127.0.0.1:8480/",
		`RequestHeader set X-Forwarded-Proto "https"`,
		"paisans web-server check --app status",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "YOUR CERTIFICATE"); n != 3 {
		t.Errorf("want a certificate marker in each of the three snippets, got %d:\n%s", n, out)
	}
	if strings.Contains(out, "ufw") {
		t.Errorf("a loopback listen needs no firewall warning:\n%s", out)
	}
}

func TestShowWarnsWhenListenIsNotLoopback(t *testing.T) {
	var buf bytes.Buffer
	target := external(t, "192.168.1.20:8480")
	webserver.Show(&buf, target, webserver.WebServer{})
	hint, detail, ok := webserver.ListenWarning(target)
	if !ok || !strings.Contains(hint, "ufw") || !strings.Contains(detail, "DOCKER-USER") {
		t.Fatalf("no warning: %q %q", hint, detail)
	}
	// The sheet is copied into a web server, so the warning is not in it.
	if strings.Contains(buf.String(), "WARNING") || !strings.Contains(buf.String(), "proxy_pass http://192.168.1.20:8480;") {
		t.Fatal(buf.String())
	}
}

func TestShowHasNothingToHandOffInPaisansMode(t *testing.T) {
	target, err := webserver.For(fixture(t), "status")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	webserver.Show(&buf, target, webserver.WebServer{})
	out := buf.String()
	if !strings.Contains(out, "nothing to hand off") || strings.Contains(out, "proxy_pass") || strings.Contains(out, "YOUR CERTIFICATE") {
		t.Fatal(out)
	}
}

// The toolkit's own edge refuses the monitor's status page, badges, /metrics
// and token API whatever the app's settings say. Behind the operator's web
// server that edge is theirs, so each snippet carries the same refusals.
func TestEverySnippetRefusesWhatTheToolkitsEdgeRefuses(t *testing.T) {
	var buf bytes.Buffer
	webserver.Show(&buf, external(t, "127.0.0.1:8480"), webserver.WebServer{})
	out := buf.String()
	for _, want := range []string{
		"@refused path /status* /badge/* /metrics /metrics/ /api/v1/*",
		"location ~* ^/(status|badge/|metrics/?$|api/v1/) {",
		`<LocationMatch "(?i)^/(status|badge/|metrics/?$|api/v1/)">`,
		"refuse /status*, /badge/*, /metrics and /api/v1/*",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

// refusalCases are paths the monitor's Express app routes to its status
// page, badges, metrics or token API, in the forms its non strict, case
// insensitive routing still accepts, and paths that must pass.
var refusalCases = map[string]bool{
	"/status": true, "/STATUS": true, "/status/x": true, "/badge/1": true, "/Badge/1": true,
	"/metrics": true, "/metrics/": true, "/Metrics": true, "/api/v1/x": true, "/api/V1/x": true,
	"/": false, "/healthz": false, "/login/oidc": false, "/api/sites": false, "/metricsx": false,
}

// caddyPathMatches is Caddy's path matcher for the forms the snippets use:
// exact, or a prefix when the pattern ends in *, and case insensitive
// ("Path matches are exact but case-insensitive", caddyserver.com/docs/
// caddyfile/matchers).
func caddyPathMatches(patterns []string, path string) bool {
	path = strings.ToLower(path)
	for _, p := range patterns {
		p = strings.ToLower(p)
		if strings.HasSuffix(p, "*") && strings.HasPrefix(path, strings.TrimSuffix(p, "*")) || p == path {
			return true
		}
	}
	return false
}

func TestEachSnippetRefusesEveryFormOfTheRefusedPaths(t *testing.T) {
	var buf bytes.Buffer
	webserver.Show(&buf, external(t, "127.0.0.1:8480"), webserver.WebServer{})
	out := buf.String()

	caddy := regexp.MustCompile(`@refused path (.+)`).FindStringSubmatch(out)
	nginx := regexp.MustCompile(`location ~\* (\S+) \{`).FindStringSubmatch(out)
	apache := regexp.MustCompile(`<LocationMatch "([^"]+)">`).FindStringSubmatch(out)
	if caddy == nil || nginx == nil || apache == nil {
		t.Fatalf("a snippet has no refusal:\n%s", out)
	}
	// nginx's ~* is a case insensitive match.
	nginxRe := regexp.MustCompile("(?i)" + nginx[1])
	apacheRe := regexp.MustCompile(apache[1])
	for path, refused := range refusalCases {
		if got := caddyPathMatches(strings.Fields(caddy[1]), path); got != refused {
			t.Errorf("caddy: %s refused %v, want %v", path, got, refused)
		}
		if got := nginxRe.MatchString(path); got != refused {
			t.Errorf("nginx: %s refused %v, want %v", path, got, refused)
		}
		if got := apacheRe.MatchString(path); got != refused {
			t.Errorf("apache: %s refused %v, want %v", path, got, refused)
		}
	}
}

// A site block pasted into a Caddyfile by hand goes where the toolkit's
// import would: after the global options block, which Caddy requires first.
func TestShowPlacesTheCaddySiteAfterTheGlobalOptions(t *testing.T) {
	var buf bytes.Buffer
	webserver.Show(&buf, external(t, "127.0.0.1:8480"), webserver.WebServer{Server: webserver.ServerCaddy})
	if !strings.Contains(buf.String(), "at the top level of the Caddyfile, after the global options block if it has one") {
		t.Fatal(buf.String())
	}
}

const siteIssuer = "\ttls {\n\t\tissuer acme\n\t}\n"

// A Caddy that obtains its own certificates gives the site an issuer of its
// own, so a DNS challenge set globally through cert_issuer does not apply
// to it. Where Caddy loads files, the site loads its own.
func TestCaddySnippetHasItsOwnIssuerOnlyWhereCaddyObtainsCertificates(t *testing.T) {
	target := external(t, "127.0.0.1:8480")
	for _, c := range []struct {
		name   string
		ws     webserver.WebServer
		issuer bool
	}{
		{"automatic", webserver.WebServer{Server: webserver.ServerCaddy, Certs: webserver.CertsAutomatic}, true},
		{"automatic with acme_dns", webserver.WebServer{Server: webserver.ServerCaddy, Certs: webserver.CertsAutomatic, DNS: webserver.DNSACME}, true},
		{"files", webserver.WebServer{Server: webserver.ServerCaddy, Certs: webserver.CertsFiles, CertFile: "/etc/ssl/s/fullchain.pem", KeyFile: "/etc/ssl/s/privkey.pem"}, false},
		{"folder", webserver.WebServer{Server: webserver.ServerCaddy, Certs: webserver.CertsFolder, Folder: "/etc/ssl/caddy"}, false},
		{"not determined", webserver.WebServer{Server: webserver.ServerCaddy}, false},
		{"nginx", webserver.WebServer{Server: webserver.ServerNginx, Certs: webserver.CertsFiles}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			content := webserver.FileContent(target, c.ws)
			if got := strings.Contains(content, siteIssuer); got != c.issuer {
				t.Errorf("issuer %v, want %v:\n%s", got, c.issuer, content)
			}
			var buf bytes.Buffer
			webserver.Show(&buf, target, c.ws)
			if got := strings.Contains(buf.String(), "    \ttls {\n    \t\tissuer acme\n    \t}\n"); got != c.issuer {
				t.Errorf("sheet: issuer %v, want %v:\n%s", got, c.issuer, buf.String())
			}
		})
	}
}

// A global acme_dns reaches the site's own issuer too, so the warning says
// so and gives the line rewritten as a cert_issuer, the provider's
// arguments as they are, but for a value that is not a placeholder.
func TestACMEDNSWarningRewritesTheLineAsACertIssuer(t *testing.T) {
	target := external(t, "127.0.0.1:8480")
	for _, c := range []struct {
		name, conf, found, rewrite string
	}{
		{
			"placeholder",
			"{\n\temail admin@example.org\n\tacme_dns cloudflare {$CF_API_TOKEN}\n}\n",
			"    acme_dns cloudflare {$CF_API_TOKEN}\n",
			"    cert_issuer acme {\n        dns cloudflare {$CF_API_TOKEN}\n    }\n",
		},
		{
			"literal value",
			"{\n\tacme_dns cloudflare abc123literal\n}\n",
			"    acme_dns cloudflare <value>\n",
			"    cert_issuer acme {\n        dns cloudflare <value>\n    }\n",
		},
		{
			"block",
			"{\n\tacme_dns cloudflare {\n\t\tapi_token {env.CF_API_TOKEN}\n\t}\n}\n",
			"    acme_dns cloudflare {\n        api_token {env.CF_API_TOKEN}\n    }\n",
			"    cert_issuer acme {\n        dns cloudflare {\n            api_token {env.CF_API_TOKEN}\n        }\n    }\n",
		},
		{
			"global dns",
			"{\n\tdns cloudflare {$CF_API_TOKEN}\n\tacme_dns\n}\n",
			"    acme_dns\n",
			"    cert_issuer acme {\n        dns\n    }\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			ws := detectCaddy(t, c.conf)
			hint, detail, ok := webserver.ACMEDNSWarning(target, ws)
			if !ok {
				t.Fatalf("no warning for %+v", ws)
			}
			if !strings.Contains(hint, "acme_dns") {
				t.Errorf("hint %q", hint)
			}
			for _, want := range []string{c.found, c.rewrite, "status.example.org", "ZeroSSL", "HTTP-01", "/opt/caddy/Caddyfile"} {
				if !strings.Contains(detail, want) {
					t.Errorf("detail lacks %q:\n%s", want, detail)
				}
			}
			if strings.Contains(detail, "abc123literal") {
				t.Errorf("detail prints a value from the Caddyfile:\n%s", detail)
			}
		})
	}
	for _, conf := range []string{"{\n\temail admin@example.org\n}\n", "{\n\tcert_issuer acme {\n\t\tdns cloudflare {$CF_API_TOKEN}\n\t}\n}\n"} {
		if hint, _, ok := webserver.ACMEDNSWarning(target, detectCaddy(t, conf)); ok {
			t.Errorf("a warning for %q: %s", conf, hint)
		}
	}
}
