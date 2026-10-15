package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/webserver"
)

func TestWebServerShowPrintsTheSheet(t *testing.T) {
	var err error
	out := captureStdout(t, func() { err = runWebServer([]string{"show", "--app", "health", "--config", fixtureConfig()}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "nothing to hand off") {
		t.Fatalf("the fixture's monitor runs the toolkit's Caddy:\n%s", out)
	}
}

func TestWebServerRefusesAnAppTheGatewayServes(t *testing.T) {
	if err := runWebServer([]string{"show", "--app", "talk", "--config", fixtureConfig()}); err == nil || !strings.Contains(err.Error(), "gateway") {
		t.Fatalf("err = %v", err)
	}
}

func TestWebServerRefusesAnUnknownSubcommand(t *testing.T) {
	if err := runWebServer([]string{"fix", "--app", "health"}); err == nil || !strings.Contains(err.Error(), "show or check") {
		t.Fatalf("err = %v", err)
	}
	if err := runWebServer([]string{"check", "--config", fixtureConfig()}); err == nil || !strings.Contains(err.Error(), "--app") {
		t.Fatalf("err = %v", err)
	}
}

type noDNS struct{}

func (noDNS) LookupHost(context.Context, string) ([]string, error) {
	return nil, errors.New("no such host")
}

// check fails, and exits non zero, when an item fails. The probes reach
// nothing: the resolver knows no names and every connection is refused.
func TestWebServerCheckFailsOnAFailedItem(t *testing.T) {
	saved := webServerProbes
	t.Cleanup(func() { webServerProbes = saved })
	refuse := func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("refused") }
	webServerProbes = func() webserver.Probes {
		return webserver.Probes{
			Resolver: noDNS{},
			Client:   &http.Client{Transport: &http.Transport{DialContext: refuse}},
			Dial:     refuse,
			Now:      time.Now,
		}
	}
	rec := withRecorder(t, false)
	err := runWebServer([]string{"check", "--app", "health", "--config", fixtureConfig()})
	out := rec.Lines()
	if err == nil || !rec.Has("fail", "check dns") || !rec.Has("fail", "check certificate") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

// The monitor's host is read as the ssh user, never through sudo.
func TestWebServerHostReadsAsTheSSHUser(t *testing.T) {
	h := webServerHost(fixture(t), "watch")
	if plain, ok := h.Plain.(apply.SSHTransport); !ok || plain.Sudo {
		t.Fatalf("plain %#v", h.Plain)
	}
	if h.Sudo != nil {
		t.Errorf("read through sudo: %#v", h.Sudo)
	}
}

// externalFixture is the fixture with watch on managed_by: external,
// written to a file for a command to load.
func externalFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(data), "    public_address: 203.0.113.20\n", "    public_address: 203.0.113.20\n    web_server:\n      managed_by: external\n      listen: 127.0.0.1:8480\n", 1)
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, err := config.Load(path); err != nil || cfg.Sites["watch"].WebServerManagedBy() != config.ManagedByExternal {
		t.Fatalf("%v", err)
	}
	return path
}

// show prints the snippet for the web server it finds on the monitor's
// host, under the line naming it.
func TestWebServerShowPrintsTheDetectedServer(t *testing.T) {
	asked := fakeWebServer(t, caddyAutomatic)
	var err error
	out := captureStdout(t, func() {
		err = runWebServer([]string{"show", "--app", "health", "--config", externalFixture(t)})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*asked, webServerAsks{"health sudo=false"}) {
		t.Errorf("asked %v", *asked)
	}
	if !strings.Contains(out, caddyAutomatic.Summary()) || strings.Contains(out, "proxy_pass") || strings.Contains(out, "tls /etc/ssl") {
		t.Errorf("sheet:\n%s", out)
	}
}

// acmeDNSCaddy is a Caddy that obtains its own certificates with a global
// acme_dns, which reaches the toolkit's site too.
var acmeDNSCaddy = webserver.WebServer{
	Server: webserver.ServerCaddy, Container: "caddy-1", Config: "/opt/caddy/Caddyfile", Main: "/etc/caddy/Caddyfile",
	Mounts: []webserver.Mount{{Source: "/opt/caddy", Destination: "/etc/caddy"}},
	Certs:  webserver.CertsAutomatic, DNS: webserver.DNSACME, ACMEDNS: []string{"acme_dns cloudflare {$CF_API_TOKEN}"},
}

// show warns, outside the sheet, that a global acme_dns reaches the site,
// and still prints the site with its own issuer.
func TestWebServerShowWarnsOfAGlobalACMEDNS(t *testing.T) {
	fakeWebServer(t, acmeDNSCaddy)
	rec := withRecorder(t, false)
	var err error
	out := captureStdout(t, func() {
		err = runWebServer([]string{"show", "--app", "health", "--config", externalFixture(t)})
	})
	if err != nil {
		t.Fatal(err)
	}
	if i := rec.Index("warn", "acme_dns"); i < 0 || !strings.Contains(rec.Events[i].Extra, "cert_issuer acme {") {
		t.Errorf("no warning:\n%s", rec.Lines())
	}
	if !strings.Contains(out, "issuer acme") || strings.Contains(out, "cert_issuer") {
		t.Errorf("sheet:\n%s", out)
	}
}
