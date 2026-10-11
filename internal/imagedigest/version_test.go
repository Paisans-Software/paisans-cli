package imagedigest_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/imagedigest"
)

const (
	amd64Manifest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	armManifest   = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	attestation   = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	configBlob    = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
)

// labelled stands in for a registry holding one multi platform build, whose
// configuration carries labels, behind the same anonymous challenge as
// ghcr.io's. With labels empty the configuration has none.
func labelled(t *testing.T, labels string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			fmt.Fprint(w, `{"token":"anon"}`)
			return
		}
		seen = append(seen, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer anon" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="test"`, srv.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		base := "/v2/paisans-software/mbin/"
		switch r.URL.Path {
		case base + "manifests/" + indexDigest:
			fmt.Fprintf(w, `{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[
				{"digest":%q,"platform":{"os":"linux","architecture":"arm64"}},
				{"digest":%q,"platform":{"os":"unknown","architecture":"unknown"}},
				{"digest":%q,"platform":{"os":"linux","architecture":"amd64"}}]}`, armManifest, attestation, amd64Manifest)
		case base + "manifests/" + amd64Manifest:
			fmt.Fprintf(w, `{"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":%q}}`, configBlob)
		case base + "blobs/" + configBlob:
			fmt.Fprintf(w, `{"architecture":"amd64","config":{"Labels":{%s}}}`, labels)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func mbinRef(srv *httptest.Server) string {
	return strings.TrimPrefix(srv.URL, "https://") + "/paisans-software/mbin:stack-1@" + indexDigest
}

func TestVersionReadsTheLabelOfTheAmd64Build(t *testing.T) {
	srv, seen := labelled(t, `"org.opencontainers.image.version":"1.15.0-paisans","org.opencontainers.image.title":"mbin"`)
	got, err := imagedigest.Registry{Client: srv.Client()}.Version(mbinRef(srv))
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.15.0-paisans" {
		t.Errorf("got %q", got)
	}
	for _, path := range *seen {
		if strings.Contains(path, armManifest) || strings.Contains(path, attestation) {
			t.Errorf("read %s, which is not the amd64 build", path)
		}
	}
}

// A build without the label has no version to show, which is not a failure.
func TestVersionIsEmptyWithoutTheLabel(t *testing.T) {
	srv, _ := labelled(t, "")
	got, err := imagedigest.Registry{Client: srv.Client()}.Version(mbinRef(srv))
	if err != nil || got != "" {
		t.Errorf("got %q, %v; want nothing and no error", got, err)
	}
}

func TestVersionNeedsADigest(t *testing.T) {
	if _, err := (imagedigest.Registry{}).Version("ghcr.io/paisans-software/mbin:stack-1"); err == nil {
		t.Error("a tag was read as a build")
	}
}

// The cache reads each build's label once for the run.
func TestTheCacheReadsAVersionOnce(t *testing.T) {
	srv, seen := labelled(t, `"org.opencontainers.image.version":"1.15.0-paisans"`)
	c := imagedigest.Cached(imagedigest.Registry{Client: srv.Client()})
	for range 2 {
		if got, err := c.Version(mbinRef(srv)); err != nil || got != "1.15.0-paisans" {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	blobs := 0
	for _, path := range *seen {
		if strings.Contains(path, "/blobs/") {
			blobs++
		}
	}
	if blobs != 1 {
		t.Errorf("read the configuration %d times", blobs)
	}
}
