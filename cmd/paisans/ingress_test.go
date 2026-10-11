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

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/ingress"
)

func TestIngressShowPrintsTheSheet(t *testing.T) {
	var err error
	out := captureStdout(t, func() { err = runIngress([]string{"show", "--app", "status", "--config", fixtureConfig()}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "nothing to hand off") {
		t.Fatalf("the fixture's monitor runs the toolkit's Caddy:\n%s", out)
	}
}

func TestIngressRefusesAnAppTheGatewayServes(t *testing.T) {
	if err := runIngress([]string{"show", "--app", "talk", "--config", fixtureConfig()}); err == nil || !strings.Contains(err.Error(), "gateway") {
		t.Fatalf("err = %v", err)
	}
}

func TestIngressRefusesAnUnknownSubcommand(t *testing.T) {
	if err := runIngress([]string{"fix", "--app", "status"}); err == nil || !strings.Contains(err.Error(), "show or check") {
		t.Fatalf("err = %v", err)
	}
	if err := runIngress([]string{"check", "--config", fixtureConfig()}); err == nil || !strings.Contains(err.Error(), "--app") {
		t.Fatalf("err = %v", err)
	}
}

type noDNS struct{}

func (noDNS) LookupHost(context.Context, string) ([]string, error) {
	return nil, errors.New("no such host")
}

// check fails, and exits non zero, when an item fails. The probes reach
// nothing: the resolver knows no names and every connection is refused.
func TestIngressCheckFailsOnAFailedItem(t *testing.T) {
	saved := ingressProbes
	t.Cleanup(func() { ingressProbes = saved })
	refuse := func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("refused") }
	ingressProbes = func() ingress.Probes {
		return ingress.Probes{
			Resolver: noDNS{},
			Client:   &http.Client{Transport: &http.Transport{DialContext: refuse}},
			Dial:     refuse,
			Now:      time.Now,
		}
	}
	rec := withRecorder(t, false)
	err := runIngress([]string{"check", "--app", "status", "--config", fixtureConfig()})
	out := rec.Lines()
	if err == nil || !rec.Has("fail", "check dns") || !rec.Has("fail", "check certificate") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

// The monitor's host is read as the ssh user, and through sudo with the
// answer every transport to that host shares (sudoAuthFor): inside apply,
// the password settled at the start of the run, so reading the web server
// asks for nothing more.
func TestIngressHostSharesTheRunsSudo(t *testing.T) {
	cfg := fixture(t)
	h := ingressHost(cfg, "watch", true)
	plain, ok := h.Plain.(apply.SSHTransport)
	if !ok || plain.Sudo {
		t.Fatalf("plain %#v", h.Plain)
	}
	sudo, ok := h.Sudo.(apply.SSHTransport)
	if !ok || !sudo.Sudo || sudo.Auth == nil || sudo.Auth != sudoAuthFor(sudo.Describe()) {
		t.Fatalf("sudo %#v", h.Sudo)
	}
	if h := ingressHost(cfg, "watch", false); h.Sudo != nil {
		t.Errorf("--sudo=false still reads through sudo: %#v", h.Sudo)
	}
}

// externalFixture is the fixture with watch in ingress mode external,
// written to a file for a command to load.
func externalFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(data), "    public_address: 203.0.113.20\n", "    public_address: 203.0.113.20\n    ingress:\n      mode: external\n      listen: 127.0.0.1:8480\n", 1)
	path := filepath.Join(t.TempDir(), "paisans.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, err := config.Load(path); err != nil || cfg.Sites["watch"].IngressMode() != config.IngressExternal {
		t.Fatalf("%v", err)
	}
	return path
}

// show prints the snippet for the web server it finds on the monitor's
// host, under the line naming it.
func TestIngressShowPrintsTheDetectedServer(t *testing.T) {
	asked := fakeWebServer(t, caddyAutomatic)
	var err error
	out := captureStdout(t, func() {
		err = runIngress([]string{"show", "--app", "status", "--config", externalFixture(t)})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*asked, webServerAsks{"status sudo=true"}) {
		t.Errorf("asked %v", *asked)
	}
	if !strings.Contains(out, caddyAutomatic.Summary()) || strings.Contains(out, "proxy_pass") || strings.Contains(out, "tls /etc/ssl") {
		t.Errorf("sheet:\n%s", out)
	}
	if out2 := captureStdout(t, func() {
		runIngress([]string{"show", "--app", "status", "--sudo=false", "--config", externalFixture(t)})
	}); !slices.Equal(*asked, webServerAsks{"status sudo=true", "status sudo=false"}) {
		t.Errorf("asked %v:\n%s", *asked, out2)
	}
}
