package selfcheck_test

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/selfcheck"
)

// gateway is a real Caddy stand-in on 127.0.0.1: TLS on one port, whose
// handshake fails, as Caddy's does, until it holds a certificate after
// failing handshakes, and plain HTTP on another, redirecting to HTTPS.
func gateway(t *testing.T, failing int64) func() {
	t.Helper()
	var handshakes atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("f2a9"))
	}))
	srv.TLS = &tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		if handshakes.Add(1) <= failing {
			return nil, errors.New("no certificate for talk.example.org yet")
		}
		return &srv.TLS.Certificates[0], nil
	}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://"+r.Host+r.URL.Path, http.StatusPermanentRedirect)
	}))
	_, https, _ := net.SplitHostPort(srv.Listener.Addr().String())
	_, port80, _ := net.SplitHostPort(plain.Listener.Addr().String())
	restore := selfcheck.UsePorts(https, port80)
	return func() { restore(); srv.Close(); plain.Close() }
}

func realProbe() selfcheck.Probe {
	p := selfcheck.DefaultProbe()
	now := time.Unix(0, 0)
	p.Now = func() time.Time { return now }
	p.Sleep = func(d time.Duration) { now = now.Add(d) }
	return p
}

var local = selfcheck.Target{Deployment: d, Site: "vm", Address: "127.0.0.1", Host: "talk.example.org"}

// A gateway whose Caddy has no certificate yet fails the handshake with an
// alert; that is waited for, and the check passes once it has one.
func TestSelfCheckWaitsThroughAFailedHandshake(t *testing.T) {
	defer gateway(t, 3)()
	if err := selfcheck.Check(context.Background(), realProbe(), local); err != nil {
		t.Fatalf("a gateway that holds its certificate after three handshakes: %v", err)
	}
}

func TestSelfCheckGivesUpOnAHandshakeThatNeverSucceeds(t *testing.T) {
	defer gateway(t, 1<<40)()
	err := selfcheck.Check(context.Background(), realProbe(), local)
	if err == nil || !strings.Contains(err.Error(), "443 on vm does not answer") {
		t.Fatalf("got %v", err)
	}
}
