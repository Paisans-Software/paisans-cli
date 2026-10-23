package selfcheck_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/selfcheck"
)

var d = deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}

var target = selfcheck.Target{Deployment: d, Site: "vm", Address: "203.0.113.20", Host: "talk.example.org"}

// answers is a fake world: what https and http each answer, in turn.
type answers struct {
	https, http []result
	asked       []string
}

type result struct {
	a   selfcheck.Answer
	err error
}

func (w *answers) fetch(_ context.Context, scheme, address, host, path string) (selfcheck.Answer, error) {
	w.asked = append(w.asked, scheme+"://"+address+"/"+host+path)
	list := &w.https
	if scheme == "http" {
		list = &w.http
	}
	r := (*list)[0]
	if len(*list) > 1 {
		*list = (*list)[1:]
	}
	return r.a, r.err
}

func probe(w *answers) selfcheck.Probe {
	now := time.Unix(0, 0)
	return selfcheck.Probe{Fetch: w.fetch, Now: func() time.Time { return now }, Sleep: func(d time.Duration) { now = now.Add(d) }, Wait: 2 * time.Minute}
}

var (
	ours     = result{a: selfcheck.Answer{Status: 200, Body: "f2a9"}}
	redirect = result{a: selfcheck.Answer{Status: 308, Location: "https://talk.example.org/.well-known/paisans/f2a9"}}
	refused  = result{err: fmt.Errorf("dial: %w", syscall.ECONNREFUSED)}
)

func TestTheDeploymentsOwnGatewayPasses(t *testing.T) {
	w := &answers{https: []result{ours}, http: []result{redirect}}
	if err := selfcheck.Check(context.Background(), probe(w), target); err != nil {
		t.Fatal(err)
	}
	if strings.Join(w.asked, "\n") != "https://203.0.113.20/talk.example.org/.well-known/paisans/f2a9\nhttp://203.0.113.20/talk.example.org/.well-known/paisans/f2a9" {
		t.Errorf("asked %q", w.asked)
	}
}

// Anything else that answers is not this deployment, and says so in the
// spec's words.
func TestSelfCheckRefusesAnotherServer(t *testing.T) {
	for name, w := range map[string]*answers{
		"another body":          {https: []result{{a: selfcheck.Answer{Status: 200, Body: "<html>welcome to nginx</html>"}}}, http: []result{redirect}},
		"a 404":                 {https: []result{{a: selfcheck.Answer{Status: 404}}}, http: []result{redirect}},
		"a redirect on 443":     {https: []result{{a: selfcheck.Answer{Status: 302, Location: "https://elsewhere.example/f2a9"}}}, http: []result{redirect}},
		"80 elsewhere":          {https: []result{ours}, http: []result{{a: selfcheck.Answer{Status: 301, Location: "https://other.example.org/"}}}},
		"80 serves no redirect": {https: []result{ours}, http: []result{ours}},
		"an echo of the path":   {https: []result{{a: selfcheck.Answer{Status: 200, Body: "/.well-known/paisans/f2a9\n"}}}, http: []result{redirect}},
	} {
		err := selfcheck.Check(context.Background(), probe(w), target)
		var c *selfcheck.Conflict
		if !errors.As(err, &c) || !strings.Contains(err.Error(), "on vm answers, but not as this deployment") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// While Caddy obtains its certificate nothing answers, or the handshake
// fails; that is waited for, up to Wait, and then named.
func TestSelfCheckWaitsForTheCertificateThenGivesUp(t *testing.T) {
	w := &answers{https: []result{refused, refused, ours}, http: []result{redirect}}
	if err := selfcheck.Check(context.Background(), probe(w), target); err != nil {
		t.Errorf("a gateway that answers after two tries: %v", err)
	}
	w = &answers{https: []result{refused}, http: []result{redirect}}
	err := selfcheck.Check(context.Background(), probe(w), target)
	if err == nil || !strings.Contains(err.Error(), "443 on vm does not answer") {
		t.Errorf("a gateway that never answers: %v", err)
	}
}

// A port that gives no answer is named, with what it was asked for.
func TestSelfCheckNamesTheSilentPort(t *testing.T) {
	w := &answers{https: []result{ours}, http: []result{refused}}
	err := selfcheck.Check(context.Background(), probe(w), target)
	var u *selfcheck.Unanswered
	if !errors.As(err, &u) || u.Port != 80 || !strings.HasPrefix(u.URL, "http://") || !strings.HasPrefix(err.Error(), "80 on vm does not answer http://") {
		t.Errorf("80 refused: %v", err)
	}
}

// How a port stayed silent is told apart: a refusal, a timeout, and
// anything else the check cannot place.
func TestSelfCheckClassifiesTheSilence(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want selfcheck.Silence
	}{
		"refused":      {refused.err, selfcheck.Refused},
		"dial timeout": {&net.OpError{Op: "dial", Net: "tcp", Err: timeoutError{}}, selfcheck.TimedOut},
		"deadline":     {fmt.Errorf("get: %w", context.DeadlineExceeded), selfcheck.TimedOut},
		"unreachable":  {fmt.Errorf("dial: %w", syscall.EHOSTUNREACH), selfcheck.Unexplained},
	} {
		w := &answers{https: []result{ours}, http: []result{{err: c.err}}}
		err := selfcheck.Check(context.Background(), probe(w), target)
		var u *selfcheck.Unanswered
		if !errors.As(err, &u) || u.Why != c.want {
			t.Errorf("%s: %v, %+v", name, err, u)
		}
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
