package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/dns"
	"github.com/paisans-software/paisans-stack/internal/gatewaymove"
)

// probeHost answers the gateway Caddy probe and file reads.
type probeHost struct {
	caddy bool
	files map[string]string
}

func (h probeHost) Describe() string { return "fake" }
func (h probeHost) Run(c string) (string, error) {
	if strings.Contains(c, "docker ps -a") {
		if h.caddy {
			return "caddy c0ffee running\n", nil
		}
		return "", nil
	}
	return "", errors.New("unexpected command: " + c)
}
func (h probeHost) RunInput(c, _ string) (string, error) { return h.Run(c) }
func (h probeHost) ReadFile(p string) (string, bool, error) {
	c, ok := h.files[p]
	return c, ok, nil
}
func (h probeHost) WriteFile(string, string, uint32) error { return errors.New("unexpected write") }

// apply without --site asks a monitor site for a hand-off, never for its
// Caddy, which is its own: a running Caddy alone is not a gateway; one whose
// Caddyfile still names the gateway's hostnames is. A plain site's Caddy is.
func TestHoldsGatewayOnAMonitorSiteCountsRoutesNotItsCaddy(t *testing.T) {
	cfg := sharedFixture(t)
	path := gatewaymove.CaddyfilePath(cfg.Deployment())
	gateway := dns.GatewayHostnames(cfg)[0].Name + " {\n}\n"
	for _, tc := range []struct {
		name, site string
		host       probeHost
		want       bool
	}{
		{"a monitor's own Caddy", "watch", probeHost{caddy: true, files: map[string]string{path: "status.example.org {\n}\n"}}, false},
		{"a monitor still serving the gateway", "watch", probeHost{caddy: true, files: map[string]string{path: gateway}}, true},
		{"a plain site's Caddy", "home-a", probeHost{caddy: true, files: map[string]string{}}, true},
	} {
		got, err := holdsGatewayOn(cfg, tc.site, tc.host)
		if err != nil || got != tc.want {
			t.Errorf("%s: %v %v, want %v", tc.name, got, err, tc.want)
		}
	}
}
