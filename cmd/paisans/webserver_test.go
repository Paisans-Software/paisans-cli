package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
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

// recordedServer serves the monitor's admission record, with the web
// server ws admit detected, to web-server show and check, through a host
// that records every command it is sent.
func recordedServer(t *testing.T, path string, ws *admission.WebServer) *manifestHost {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	req, err := admission.Build(cfg, "watch")
	if err != nil {
		t.Fatal(err)
	}
	rec := admission.NewRecord(req, req.Grants, "", time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC))
	rec.WebServer = ws
	b, err := admission.EncodeRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	h := &manifestHost{files: map[string]string{admittedPath(cfg): string(b)}}
	saved := webServerRecordHost
	webServerRecordHost = func(*config.Config, string) admission.Reader { return h }
	t.Cleanup(func() { webServerRecordHost = saved })
	return h
}

// The monitor's web server is what admit recorded: show runs nothing on
// the host to find it, no docker ps, no ss.
func TestWebServerHostReadsAsTheSSHUser(t *testing.T) {
	path := externalFixture(t)
	h := recordedServer(t, path, &admission.WebServer{Server: "caddy", Process: "caddy", Config: "/etc/caddy/Caddyfile", Main: "/etc/caddy/Caddyfile", Certs: "automatic"})
	captureStdout(t, func() {
		if err := runWebServer([]string{"show", "--app", "health", "--config", path}); err != nil {
			t.Fatal(err)
		}
	})
	for _, c := range h.ran {
		if strings.Contains(c, "docker ps") || strings.HasPrefix(c, "ss ") || strings.Contains(c, " ss -") {
			t.Errorf("ran %s", c)
		}
	}
	if len(h.ran) != 0 {
		t.Errorf("ran %v", h.ran)
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
	// It hosts the uptime app alone (web-server-external-serves-one-app), so
	// the fixture's Beszel hub goes.
	text = strings.Replace(text, "  stats:\n    blueprint: beszel\n    hostname: status.health.example.org\n    placement: { pinned: watch }\n", "", 1)
	// The notifier serves that hub, so it goes too.
	text = strings.Replace(text, "  notify:\n    blueprint: ops-notifier\n    hostname: notify.example.org\n    placement: { pinned: vm }\n", "", 1)
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, err := config.Load(path); err != nil || cfg.Sites["watch"].WebServerManagedBy() != config.ManagedByExternal {
		t.Fatalf("%v", err)
	}
	return path
}

// show prints the snippet for the web server the admission record names,
// under the line naming it.
func TestWebServerShowPrintsTheDetectedServer(t *testing.T) {
	path := externalFixture(t)
	recorded := &admission.WebServer{Server: "nginx", Process: "nginx", Config: "/etc/nginx/nginx.conf", Main: "/etc/nginx/nginx.conf", Certs: "files", CertFile: "/etc/letsencrypt/live/health.example.org/fullchain.pem", KeyFile: "/etc/letsencrypt/live/health.example.org/privkey.pem"}
	recordedServer(t, path, recorded)
	var err error
	out := captureStdout(t, func() {
		err = runWebServer([]string{"show", "--app", "health", "--config", path})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, webserver.FromRecord(recorded).Summary()) || !strings.Contains(out, "proxy_pass") || strings.Contains(out, "reverse_proxy") {
		t.Errorf("sheet:\n%s", out)
	}
}
