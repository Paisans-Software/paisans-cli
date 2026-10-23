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

// listen is a port on 127.0.0.1 that serve answers, used for https.
func listen(t *testing.T, serve func(net.Conn)) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go serve(c)
		}
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	restore := selfcheck.UsePorts(port, port)
	t.Cleanup(func() { restore(); _ = l.Close() })
}

// Another server that accepts the connection on 443 and answers in
// anything but TLS holds the port: that is a Conflict, named at once and
// not waited for, since no certificate makes it speak TLS.
func TestSelfCheckNamesAServerThatDoesNotSpeakTLS(t *testing.T) {
	for name, c := range map[string]struct {
		serve  func(t *testing.T)
		detail string
	}{
		"plain HTTP": {func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hello")) }))
			srv.Config.ErrorLog = log.New(io.Discard, "", 0)
			_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
			restore := selfcheck.UsePorts(port, port)
			t.Cleanup(func() { restore(); srv.Close() })
		}, "plain HTTP rather than TLS"},
		"another protocol": {func(t *testing.T) {
			listen(t, func(c net.Conn) { _, _ = c.Write([]byte("SSH-2.0-OpenSSH_9.6\r\n")); _ = c.Close() })
		}, "a protocol other than TLS"},
	} {
		t.Run(name, func(t *testing.T) {
			c.serve(t)
			p := realProbe()
			asked := 0
			fetch := p.Fetch
			p.Fetch = func(ctx context.Context, scheme, address, host, path string) (selfcheck.Answer, error) {
				asked++
				return fetch(ctx, scheme, address, host, path)
			}
			err := selfcheck.Check(context.Background(), p, local)
			var conflict *selfcheck.Conflict
			if !errors.As(err, &conflict) || conflict.Port != 443 || conflict.Detail != c.detail {
				t.Fatalf("got %v", err)
			}
			if asked != 1 {
				t.Errorf("asked %d times, want once", asked)
			}
		})
	}
}

// A closed port refuses the connection, and the check says so.
func TestSelfCheckNamesARefusedConnection(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	_ = l.Close()
	defer selfcheck.UsePorts(port, port)()
	p := realProbe()
	p.Wait = 0
	err = selfcheck.Check(context.Background(), p, local)
	var u *selfcheck.Unanswered
	if !errors.As(err, &u) || u.Port != 443 || u.Why != selfcheck.Refused {
		t.Fatalf("got %v", err)
	}
}

// A connection that nothing answers in time, here a listener that accepts
// and never speaks, timed out, and the check says so.
func TestSelfCheckNamesATimeout(t *testing.T) {
	listen(t, func(c net.Conn) { _, _ = io.Copy(io.Discard, c) })
	p := realProbe()
	p.Wait = 0
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := selfcheck.Check(ctx, p, local)
	var u *selfcheck.Unanswered
	if !errors.As(err, &u) || u.Port != 443 || u.Why != selfcheck.TimedOut {
		t.Fatalf("got %v", err)
	}
}
