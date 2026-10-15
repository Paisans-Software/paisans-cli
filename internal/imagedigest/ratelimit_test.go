package imagedigest_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/imagedigest"
)

// manifestGETs counts the manifest GETs among the paths a registry saw, the
// requests Docker Hub counts against an address's anonymous allowance.
func manifestGETs(seen []string) int {
	n := 0
	for _, p := range seen {
		if strings.Contains(p, "/manifests/") {
			n++
		}
	}
	return n
}

// Platforms reads the index alone: its entries name every platform, so no
// entry's own manifest or configuration is fetched.
func TestPlatformsReadsOneManifest(t *testing.T) {
	srv, seen := labelled(t, "")
	got, err := imagedigest.Registry{Client: srv.Client()}.Platforms(mbinRef(srv))
	if err != nil || strings.Join(got, ",") != "amd64,arm64" {
		t.Fatalf("got %v, %v", got, err)
	}
	// The first request is the anonymous challenge, answered 401.
	if n := manifestGETs(*seen); n != 2 {
		t.Errorf("asked for %d manifests, want the index once after the challenge: %v", n, *seen)
	}
	for _, p := range *seen {
		if strings.Contains(p, "/blobs/") {
			t.Errorf("read a configuration, %s", p)
		}
	}
}

// limited is a registry that answers every request 429, naming when to ask
// again, and counts the GETs it was sent.
func limited(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	gets := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets++
		}
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	return srv, &gets
}

// A 429 is its own answer, naming when to ask again, and Digest does not
// follow a refused HEAD with a GET, which would be refused too and counted.
func TestARateLimitIsSaidAndNotRetriedWithAGet(t *testing.T) {
	srv, gets := limited(t)
	ref := strings.TrimPrefix(srv.URL, "https://") + "/paisans-software/mbin:1.15.1-paisans"
	r := imagedigest.Registry{Client: srv.Client()}
	_, err := r.Digest(ref)
	if !errors.Is(err, imagedigest.ErrRateLimited) || !strings.Contains(err.Error(), "retry after 3600") {
		t.Errorf("Digest: %v", err)
	}
	if *gets != 0 {
		t.Errorf("a refused HEAD was followed by %d GETs", *gets)
	}
	if _, err := r.Platforms(ref); !errors.Is(err, imagedigest.ErrRateLimited) {
		t.Errorf("Platforms: %v", err)
	}
	if _, err := r.Track(strings.TrimSuffix(ref, "1.15.1-paisans") + "1.*.*-paisans"); !errors.Is(err, imagedigest.ErrRateLimited) {
		t.Errorf("Track: %v", err)
	}
}

// A host that rate limits is down for the rest of the run: every further
// request would be refused as well, and counted.
func TestARateLimitMarksTheHostDown(t *testing.T) {
	srv, gets := limited(t)
	host := strings.TrimPrefix(srv.URL, "https://")
	c := imagedigest.Cached(imagedigest.Registry{Client: srv.Client()})
	if _, err := c.Platforms(host + "/a/b:1@sha256:" + strings.Repeat("1", 64)); !errors.Is(err, imagedigest.ErrRateLimited) {
		t.Fatalf("first: %v", err)
	}
	before := *gets
	got, err := c.Platforms(host + "/a/c:1@sha256:" + strings.Repeat("2", 64))
	if got != nil || err != nil || *gets != before {
		t.Errorf("a down host was asked again: %v, %v, %d GETs", got, err, *gets-before)
	}
}

// Store keeps a pinned reference's platforms across runs, so a later run
// asks the registry nothing; a tag, whose build can change, is not kept.
func TestTheStoreKeepsAPinnedReferencesPlatforms(t *testing.T) {
	srv, seen := labelled(t, "")
	dir := t.TempDir()
	first := imagedigest.Cached(imagedigest.Registry{Client: srv.Client()})
	first.Store = dir
	if _, err := first.Platforms(mbinRef(srv)); err != nil {
		t.Fatal(err)
	}
	asked := len(*seen)
	next := imagedigest.Cached(imagedigest.Registry{Client: srv.Client()})
	next.Store = dir
	got, err := next.Platforms(mbinRef(srv))
	if err != nil || strings.Join(got, ",") != "amd64,arm64" || len(*seen) != asked {
		t.Errorf("the next run got %v, %v, asking %d more", got, err, len(*seen)-asked)
	}
	tag := strings.TrimSuffix(mbinRef(srv), "@"+indexDigest)
	if _, err := next.Platforms(strings.Replace(tag, ":develop", ":1.15.1-paisans", 1)); err != nil {
		t.Fatal(err)
	}
	again := imagedigest.Cached(imagedigest.Registry{Client: srv.Client()})
	again.Store = dir
	before := len(*seen)
	again.Platforms(strings.Replace(tag, ":develop", ":1.15.1-paisans", 1))
	if len(*seen) == before {
		t.Error("a tag's platforms were kept across runs")
	}
}
