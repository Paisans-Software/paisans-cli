package imagedigest_test

import (
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/imagedigest"
)

// moving is a registry whose tag moves on every question: the nth call
// answers sha256:n. fail makes the calls before it fail.
type moving struct {
	calls atomic.Int32
	fail  int32
	// gate, when set, holds every call until it is closed.
	gate chan struct{}
}

func (m *moving) Digest(string) (string, error) {
	n := m.calls.Add(1)
	if m.gate != nil {
		<-m.gate
	}
	if n <= m.fail {
		return "", errors.New("registry did not answer")
	}
	return fmt.Sprintf("sha256:%d", n), nil
}

// A tag that moves between two plans of one run still names the build the
// first plan resolved.
func TestACachedTagNamesItsFirstBuild(t *testing.T) {
	m := &moving{}
	c := imagedigest.Cached(m)
	for range 3 {
		got, err := c.Digest("ghcr.io/paisans-software/caddy:2")
		if err != nil || got != "sha256:1" {
			t.Fatalf("got %q, %v, want sha256:1", got, err)
		}
	}
	if got, _ := c.Digest("ghcr.io/paisans-software/caddy:3"); got != "sha256:2" {
		t.Errorf("another reference got %q, want its own resolution", got)
	}
	if n := m.calls.Load(); n != 2 {
		t.Errorf("asked the registry %d times, want once per reference", n)
	}
}

// A failed resolution is kept for the run like an answer: every plan of it
// sees the same thing, and a registry that timed out once is not waited on
// again.
func TestAFailedResolutionIsRemembered(t *testing.T) {
	m := &moving{fail: 1}
	c := imagedigest.Cached(m)
	for range 3 {
		if _, err := c.Digest("caddy:2"); err == nil {
			t.Fatal("a failed resolution answered later in the run")
		}
	}
	if n := m.calls.Load(); n != 1 {
		t.Errorf("asked the registry %d times, want once", n)
	}
}

// unreachable fails every reference on one registry host as an unreachable
// registry does, with a transport error, and answers every other.
type unreachable struct {
	host  string
	asked []string
}

func (u *unreachable) Digest(ref string) (string, error) {
	u.asked = append(u.asked, ref)
	if strings.HasPrefix(ref, u.host+"/") {
		return "", fmt.Errorf("asking %s for %s: %w", u.host, ref, &url.Error{Op: "Head", URL: "https://" + u.host + "/v2/", Err: errors.New("i/o timeout")})
	}
	return "sha256:1", nil
}

// A registry that could not be reached fails the run's later references on
// it at once, instead of a timeout each; other registries are still asked.
func TestAnUnreachableRegistryFailsFast(t *testing.T) {
	u := &unreachable{host: "ghcr.io"}
	c := imagedigest.Cached(u)
	if _, err := c.Digest("ghcr.io/paisans-software/mbin:develop"); err == nil {
		t.Fatal("an unreachable registry answered")
	}
	_, err := c.Digest("ghcr.io/paisans-software/caddy:2")
	if err == nil || !strings.Contains(err.Error(), "i/o timeout") {
		t.Errorf("a second image on the same registry got %v, want the first failure", err)
	}
	if got, err := c.Digest("outlinewiki/outline:1.10.0"); err != nil || got != "sha256:1" {
		t.Errorf("another registry got %q, %v", got, err)
	}
	if len(u.asked) != 2 {
		t.Errorf("asked %v, want the unreachable registry once", u.asked)
	}
}

// Sites planned at the same time ask the registry once between them.
func TestConcurrentPlansResolveOnce(t *testing.T) {
	m := &moving{gate: make(chan struct{})}
	c := imagedigest.Cached(m)
	var wg sync.WaitGroup
	got := make([]string, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], _ = c.Digest("caddy:2")
		}()
	}
	// Hold the one resolution until it has started, so every other caller
	// finds it under way or already answered.
	for m.calls.Load() == 0 {
		runtime.Gosched()
	}
	close(m.gate)
	wg.Wait()
	for i, g := range got {
		if g != "sha256:1" {
			t.Errorf("caller %d got %q", i, g)
		}
	}
	if n := m.calls.Load(); n != 1 {
		t.Errorf("asked the registry %d times, want once", n)
	}
}

// listing is a registry that lists newest as every repository's newest
// version, counting the questions.
type listing struct {
	moving
	newest string
	asked  atomic.Int32
}

func (l *listing) NewestVersion(string) (string, error) {
	l.asked.Add(1)
	return l.newest, nil
}

// The cache asks the registry it wraps for a repository's newest version,
// once per run, and answers empty for one that lists none.
func TestTheCacheAsksForTheNewestVersionOnce(t *testing.T) {
	l := &listing{newest: "1.15.0-paisans"}
	c := imagedigest.Cached(l)
	for range 2 {
		if got, err := c.NewestVersion("ghcr.io/paisans-software/mbin:develop"); err != nil || got != "1.15.0-paisans" {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	if n := l.asked.Load(); n != 1 {
		t.Errorf("asked %d times", n)
	}
	if got, err := imagedigest.Cached(&moving{}).NewestVersion("ghcr.io/paisans-software/mbin:develop"); got != "" || err != nil {
		t.Errorf("a registry that lists nothing answered %q, %v", got, err)
	}
}

// platformed is a resolver that answers Platforms and counts the questions,
// failing every Digest with a transport error when down is set.
type platformed struct {
	down  bool
	asked atomic.Int32
	err   error
}

func (p *platformed) Digest(ref string) (string, error) {
	if p.down {
		return "", &url.Error{Op: "Get", URL: ref, Err: errors.New("no route to host")}
	}
	return "sha256:1", nil
}

func (p *platformed) Platforms(string) ([]string, error) {
	p.asked.Add(1)
	return []string{"amd64", "arm64"}, p.err
}

// A platform list is asked once per reference, and a registry already known
// to be unreachable is not asked at all: its answer is unknown.
func TestThePlatformsAreAskedOncePerRefAndNeverOfADownHost(t *testing.T) {
	const ref = "ghcr.io/paisans-software/mbin:1@sha256:1"
	p := &platformed{}
	c := imagedigest.Cached(p)
	for range 2 {
		if got, err := c.Platforms(ref); err != nil || len(got) != 2 {
			t.Fatalf("got %v, %v", got, err)
		}
	}
	if n := p.asked.Load(); n != 1 {
		t.Errorf("asked %d times", n)
	}

	down := &platformed{down: true}
	c = imagedigest.Cached(down)
	if _, err := c.Digest(ref); err == nil {
		t.Fatal("digest should have failed")
	}
	if got, err := c.Platforms(ref); got != nil || err != nil {
		t.Errorf("got %v, %v", got, err)
	}
	if n := down.asked.Load(); n != 0 {
		t.Errorf("asked a host known to be down %d times", n)
	}
}

// A transport failure of Platforms itself marks the host down, so the next
// reference on it is not asked.
func TestAPlatformsTransportFailureMarksTheHostDown(t *testing.T) {
	p := &platformed{err: &url.Error{Op: "Get", URL: "x", Err: errors.New("timeout")}}
	c := imagedigest.Cached(p)
	if _, err := c.Platforms("ghcr.io/paisans-software/mbin:1@sha256:1"); err == nil {
		t.Fatal("expected the failure")
	}
	if got, err := c.Platforms("ghcr.io/paisans-software/caddy:2@sha256:2"); got != nil || err != nil {
		t.Errorf("got %v, %v", got, err)
	}
	if n := p.asked.Load(); n != 1 {
		t.Errorf("asked %d times", n)
	}
}
