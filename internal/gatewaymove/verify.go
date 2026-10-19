package gatewaymove

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/dns"
)

// Probes are how a move reaches the world besides ssh and the DNS
// provider: an HTTPS request to one address, a clock and a sleep. Each is
// replaced in tests, so no test touches the network or waits.
type Probes struct {
	// Get requests https://<host><path> from this machine, connecting to
	// address on port 443 instead of resolving host, and verifying the
	// certificate against host as a browser would. It returns the status,
	// without following a redirect.
	Get   func(ctx context.Context, address, host, path string) (int, error)
	Now   func() time.Time
	Sleep func(time.Duration)
}

// DefaultProbes reaches the real world from the operator's machine.
func DefaultProbes() Probes {
	return Probes{Get: get, Now: time.Now, Sleep: time.Sleep}
}

func get(ctx context.Context, address, host, path string) (int, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			// Every connection goes to the address under test, whatever the
			// hostname resolves to: that is what lets the new gateway be
			// checked before DNS points at it.
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, net.JoinHostPort(address, "443"))
			},
			TLSClientConfig: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+path, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// Check is one hostname asked through one of the new gateway's addresses.
type Check struct {
	Host, Address, Path string
}

// checks are every hostname the gateway serves, through each of site's
// public addresses: an app's own hostname at its blueprint's health route, and
// every other hostname at /.
func checks(cfg *config.Config, site string) []Check {
	s := cfg.Sites[site]
	addresses := []string{s.PublicAddress}
	if s.PublicAddress6 != "" {
		addresses = append(addresses, s.PublicAddress6)
	}
	var out []Check
	for _, h := range dns.GatewayHostnames(cfg) {
		path := "/"
		if h.Main {
			if health, ok := blueprints.HealthFor(cfg.Apps[h.App].Blueprint); ok && health.Path != "" {
				path = health.Path
			}
		}
		for _, a := range addresses {
			out = append(out, Check{Host: h.Name, Address: a, Path: path})
		}
	}
	return out
}

// verify asks every check of the new gateway, every one whatever the
// others found, and fails naming each that did not pass.
//
// A check passes when the gateway answers HTTPS with a certificate valid
// for the hostname and a status below 500. That is what the gateway alone
// decides: it holds the certificate, issued over DNS-01 before any record
// points at it, and Caddy answers 502 or 503 when it cannot reach the app
// (the upstream_unavailable snippet). A redirect, Eg: to sign in through the
// visibility gate, is the app answering, so it passes.
func (p *Plan) verify() error {
	var failed []string
	for _, c := range checks(p.cfg, p.To) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		status, err := p.probes.Get(ctx, c.Address, c.Host, c.Path)
		cancel()
		switch {
		case err != nil:
			failed = append(failed, fmt.Sprintf("https://%s%s through %s: %v", c.Host, c.Path, c.Address, err))
		case status >= 500:
			failed = append(failed, fmt.Sprintf("https://%s%s through %s answered %d", c.Host, c.Path, c.Address, status))
		}
	}
	if len(failed) == 0 {
		return nil
	}
	return fmt.Errorf("%s does not answer for every hostname yet, so nothing that depends on it is done:\n  %s\nApply it (paisans deployment apply --site %s --execute) and read its Caddy's log for a certificate it could not obtain. An address this machine has no route to (IPv6 on many home connections) cannot be checked from here; run the move from a machine that can reach it", p.To, strings.Join(failed, "\n  "), p.To)
}
