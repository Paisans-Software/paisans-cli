// Package selfcheck asks a site that runs the toolkit's Caddy, from outside,
// whether it answers as this deployment. The deployment's user cannot read the host's ruleset or
// another user's sockets, so a port another server took after admission,
// and above all one a DNAT rule takes with no listener, is invisible from
// the host. That Caddy answers /.well-known/paisans/<token> with the token;
// anything else that answers there is another server.
package selfcheck

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/dns"
)

// Path is where the gateway's Caddy answers as the deployment.
func Path(d deployment.Deployment) string { return "/.well-known/paisans/" + d.Token() }

// Body is what it answers with: the token, and nothing else.
func Body(d deployment.Deployment) string { return d.Token() }

// Answer is what one request got: its status, at most 4 KiB of its body, and
// a redirect's Location.
type Answer struct {
	Status   int
	Body     string
	Location string
}

// Fetch requests scheme://host+path from this machine, connecting to
// address, on 443 for https and 80 for http, whatever host resolves to, and
// never following a redirect.
type Fetch func(ctx context.Context, scheme, address, host, path string) (Answer, error)

// Probe is how a check reaches the world, replaced in tests. Wait is how
// long a gateway that does not answer yet is waited for: Caddy obtains its
// certificate over DNS-01 after it starts, and answers nothing on 443 until
// then.
type Probe struct {
	Fetch Fetch
	Now   func() time.Time
	Sleep func(time.Duration)
	Wait  time.Duration
}

// DefaultProbe reaches the real world from the operator's machine.
func DefaultProbe() Probe {
	return Probe{Fetch: fetch, Now: time.Now, Sleep: time.Sleep, Wait: 2 * time.Minute}
}

// ports is where fetch connects for each scheme.
var ports = map[string]string{"https": "443", "http": "80"}

// fetch does not judge the certificate: the body is what tells this
// deployment from another server, and whether the certificate is valid is
// the certificate check's to say, which a staging ACME directory would fail.
func fetch(ctx context.Context, scheme, address, host, path string) (Answer, error) {
	port := ports[scheme]
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, net.JoinHostPort(address, port))
			},
			TLSClientConfig: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, //nolint:gosec // the body, not the certificate, identifies the deployment
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+host+path, nil)
	if err != nil {
		return Answer{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Answer{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return Answer{Status: resp.StatusCode, Body: string(body), Location: resp.Header.Get("Location")}, nil
}

// Target is one gateway, asked through its public address for one of the
// hostnames it serves.
type Target struct {
	Deployment          deployment.Deployment
	Site, Address, Host string
}

// TargetFor is site's check: its public_address, and the first hostname its
// Caddy serves, a gateway's for the deployment and a monitor's its own.
// false for a site that runs no Caddy of the toolkit's, one with no
// public_address to ask through, or one with no hostname to ask for.
func TargetFor(cfg *config.Config, site string) (Target, bool) {
	s, ok := cfg.Sites[site]
	if !ok || !s.RunsCaddy() || s.PublicAddress == "" {
		return Target{}, false
	}
	names := dns.GatewayHostnames(cfg)
	if !s.Has(config.RoleGateway) {
		names = dns.ServedHostnames(cfg, site)
	}
	if len(names) == 0 {
		return Target{}, false
	}
	return Target{Deployment: cfg.Deployment(), Site: site, Address: s.PublicAddress, Host: names[0].Name}, true
}

// Conflict is a port that answers, but not as this deployment.
type Conflict struct {
	Port         int
	Site, Detail string
}

func (c *Conflict) Error() string {
	return fmt.Sprintf("%d on %s answers, but not as this deployment: %s", c.Port, c.Site, c.Detail)
}

// Unanswered is a port that gave no answer at all: URL asked through
// Address, and why the request failed.
type Unanswered struct {
	Port               int
	Site, URL, Address string
	Err                error
}

func (u *Unanswered) Error() string {
	return fmt.Sprintf("%d on %s does not answer %s through %s: %v", u.Port, u.Site, u.URL, u.Address, u.Err)
}

func (u *Unanswered) Unwrap() error { return u.Err }

// notYet is an answer worth waiting for: nothing listening, nothing
// answering, or a handshake the server fails, as Caddy fails every one
// while it has no certificate. Over TCP, Go reports an alert the server
// sends as a *net.OpError whose Op is "remote error" (tls.AlertError is
// QUIC's form), and a server that closes the connection mid handshake as
// an EOF.
func notYet(err error) bool {
	var ne net.Error
	var op *net.OpError
	var tlsAlert tls.AlertError
	switch {
	case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ECONNRESET):
		return true
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return true
	case errors.As(err, &op) && op.Op == "remote error", errors.As(err, &tlsAlert):
		return true
	}
	return errors.As(err, &ne) && ne.Timeout()
}

// Check asks t on 443 for the token, waiting up to p.Wait while nothing
// answers, then on 80 for the redirect to the same URL on HTTPS. Any other
// answer is a Conflict.
func Check(ctx context.Context, p Probe, t Target) error {
	path, want := Path(t.Deployment), Body(t.Deployment)
	deadline := p.Now().Add(p.Wait)
	var a Answer
	var err error
	for {
		a, err = p.Fetch(ctx, "https", t.Address, t.Host, path)
		if err == nil || !notYet(err) || !p.Now().Before(deadline) {
			break
		}
		p.Sleep(5 * time.Second)
	}
	if err != nil {
		return &Unanswered{Port: 443, Site: t.Site, URL: "https://" + t.Host + path, Address: t.Address, Err: err}
	}
	if a.Status != http.StatusOK || strings.TrimSpace(a.Body) != want {
		return &Conflict{Port: 443, Site: t.Site, Detail: describe(a)}
	}
	a, err = p.Fetch(ctx, "http", t.Address, t.Host, path)
	if err != nil {
		return &Unanswered{Port: 80, Site: t.Site, URL: "http://" + t.Host + path, Address: t.Address, Err: err}
	}
	if a.Status < 300 || a.Status > 399 || a.Location != "https://"+t.Host+path {
		return &Conflict{Port: 80, Site: t.Site, Detail: describe(a) + ", where this deployment's Caddy redirects to https://" + t.Host + path}
	}
	return nil
}

// describe is an answer in a line: its status, and its body or redirect
// shortened and stripped of anything that would redraw a terminal.
func describe(a Answer) string {
	out := fmt.Sprintf("status %d", a.Status)
	if a.Location != "" {
		out += ", redirecting to " + printable(a.Location)
	}
	if b := strings.TrimSpace(a.Body); b != "" {
		if len(b) > 80 {
			b = b[:80] + "..."
		}
		out += ", body " + fmt.Sprintf("%q", printable(b))
	}
	return out
}

func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}
