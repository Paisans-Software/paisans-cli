package imagedigest_test

import (
	"errors"
	"fmt"
	"runtime"
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

// A failed resolution is not kept: the next plan asks again.
func TestAFailedResolutionIsAskedAgain(t *testing.T) {
	m := &moving{fail: 1}
	c := imagedigest.Cached(m)
	if _, err := c.Digest("caddy:2"); err == nil {
		t.Fatal("the first resolution did not fail")
	}
	if got, err := c.Digest("caddy:2"); err != nil || got != "sha256:2" {
		t.Errorf("got %q, %v, want the second resolution", got, err)
	}
	if got, _ := c.Digest("caddy:2"); got != "sha256:2" {
		t.Errorf("got %q after an answer, want it kept", got)
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
