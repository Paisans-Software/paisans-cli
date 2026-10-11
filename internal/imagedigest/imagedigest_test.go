package imagedigest_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/imagedigest"
)

const indexDigest = "sha256:b401d1cb18074026a535a1facc5f6d9d35bc62fdff480f3b1fd91042c8e48186"

// registry stands in for ghcr.io as it answered on 2026-10-11: a manifest
// request without a token is refused with a Bearer challenge naming its own
// token service, which hands out an anonymous pull token, and a HEAD with
// that token carries the index's digest in Docker-Content-Digest.
func registry(t *testing.T, header bool) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/token":
			if r.URL.Query().Get("scope") != "repository:paisans-software/caddy:pull" {
				http.Error(w, "bad scope "+r.URL.Query().Get("scope"), http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"token":"anon"}`)
		case r.URL.Path == "/v2/paisans-software/caddy/manifests/2":
			if r.Header.Get("Authorization") != "Bearer anon" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="test",scope="repository:paisans-software/caddy:pull"`, srv.URL))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if !strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json") {
				http.Error(w, "no index type accepted", http.StatusNotAcceptable)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			if header {
				w.Header().Set("Docker-Content-Digest", indexDigest)
			}
			if r.Method == http.MethodGet {
				fmt.Fprint(w, `{"schemaVersion":2}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func reference(srv *httptest.Server, tag string) string {
	return strings.TrimPrefix(srv.URL, "https://") + "/paisans-software/caddy:" + tag
}

func TestDigestFollowsTheAnonymousChallenge(t *testing.T) {
	srv, seen := registry(t, true)
	got, err := imagedigest.Registry{Client: srv.Client()}.Digest(reference(srv, "2"))
	if err != nil {
		t.Fatal(err)
	}
	if got != indexDigest {
		t.Errorf("got %s, want %s", got, indexDigest)
	}
	want := []string{"HEAD /v2/paisans-software/caddy/manifests/2", "GET /token", "HEAD /v2/paisans-software/caddy/manifests/2"}
	if strings.Join(*seen, "|") != strings.Join(want, "|") {
		t.Errorf("asked %v, want %v", *seen, want)
	}
}

// A registry that leaves the header off a HEAD is answered by the manifest's
// own bytes, which is how the distribution API defines its digest.
func TestDigestHashesTheManifestWithoutTheHeader(t *testing.T) {
	srv, _ := registry(t, false)
	got, err := imagedigest.Registry{Client: srv.Client()}.Digest(reference(srv, "2"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(`{"schemaVersion":2}`))
	if want := "sha256:" + hex.EncodeToString(sum[:]); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestDigestReportsAMissingTag(t *testing.T) {
	srv, _ := registry(t, true)
	_, err := imagedigest.Registry{Client: srv.Client()}.Digest(reference(srv, "3"))
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("got %v, want a missing tag reported", err)
	}
}

// Only a tag is resolved: a digest already names its build, and no tag means
// latest, which validate refuses for the gateway.
func TestDigestRefusesWhatHasNoTag(t *testing.T) {
	for _, ref := range []string{
		"ghcr.io/paisans-software/caddy",
		"ghcr.io/paisans-software/caddy@" + indexDigest,
		"ghcr.io/paisans-software/caddy:2@" + indexDigest,
	} {
		if _, err := (imagedigest.Registry{}).Digest(ref); err == nil {
			t.Errorf("%s was resolved", ref)
		}
	}
}

func TestDigestReportsAnUnreachableRegistry(t *testing.T) {
	srv, _ := registry(t, true)
	ref := reference(srv, "2")
	srv.Close()
	if _, err := (imagedigest.Registry{Client: srv.Client()}).Digest(ref); err == nil {
		t.Fatal("a closed registry answered")
	}
}
