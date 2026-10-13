package imagedigest_test

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/imagedigest"
)

const attestation = "sha256:3333333333333333333333333333333333333333333333333333333333333333"

// build is one platform of a multi platform image: its architecture and the
// labels and volumes its configuration carries, each a JSON object body
// (Eg: `"org.opencontainers.image.version":"1.15.0"`), empty for none.
type build struct {
	arch    string
	labels  string
	volumes string
}

// manifestOf and configOf name a platform's manifest and configuration blob
// by digest, distinct per architecture.
func manifestOf(arch string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("manifest "+arch)))
}

func configOf(arch string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("config "+arch)))
}

// published stands in for a registry holding one multi platform build, an
// attestation entry beside its platforms, behind the same anonymous
// challenge as ghcr.io's.
func published(t *testing.T, builds ...build) (*httptest.Server, *[]string) {
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
		if r.URL.Path == base+"manifests/"+indexDigest || r.URL.Path == base+"manifests/1.15.1-paisans" {
			entries := []string{fmt.Sprintf(`{"digest":%q,"platform":{"os":"unknown","architecture":"unknown"}}`, attestation)}
			for _, b := range builds {
				entries = append(entries, fmt.Sprintf(`{"digest":%q,"platform":{"os":"linux","architecture":%q}}`, manifestOf(b.arch), b.arch))
			}
			fmt.Fprintf(w, `{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[%s]}`, strings.Join(entries, ","))
			return
		}
		for _, b := range builds {
			switch r.URL.Path {
			case base + "manifests/" + manifestOf(b.arch):
				fmt.Fprintf(w, `{"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":%q}}`, configOf(b.arch))
				return
			case base + "blobs/" + configOf(b.arch):
				fmt.Fprintf(w, `{"architecture":%q,"config":{"Labels":{%s},"Volumes":{%s}}}`, b.arch, b.labels, b.volumes)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// labelled is published with amd64 and arm64 builds carrying the same labels.
func labelled(t *testing.T, labels string) (*httptest.Server, *[]string) {
	t.Helper()
	return published(t, build{arch: "arm64", labels: labels}, build{arch: "amd64", labels: labels})
}

func mbinRef(srv *httptest.Server) string {
	return strings.TrimPrefix(srv.URL, "https://") + "/paisans-software/mbin:develop@" + indexDigest
}

func TestVersionIsTheLabelEveryPlatformAgreesOn(t *testing.T) {
	srv, seen := labelled(t, `"org.opencontainers.image.version":"1.15.0-paisans","org.opencontainers.image.title":"mbin"`)
	got, err := imagedigest.Registry{Client: srv.Client()}.Version(mbinRef(srv))
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.15.0-paisans" {
		t.Errorf("got %q", got)
	}
	for _, path := range *seen {
		if strings.Contains(path, attestation) {
			t.Errorf("read %s, the attestation entry", path)
		}
	}
}

// An image published for arm64 alone has a version too.
func TestVersionReadsAnArm64OnlyBuild(t *testing.T) {
	srv, _ := published(t, build{arch: "arm64", labels: `"org.opencontainers.image.version":"2.0.0"`})
	got, err := imagedigest.Registry{Client: srv.Client()}.Version(mbinRef(srv))
	if err != nil || got != "2.0.0" {
		t.Errorf("got %q, %v; want 2.0.0", got, err)
	}
}

// Platforms that disagree are each named, so the dry run never shows one
// platform's version as the whole image's.
func TestVersionNamesEachPlatformWhenTheyDisagree(t *testing.T) {
	srv, _ := published(t,
		build{arch: "arm64", labels: `"org.opencontainers.image.version":"1.15.1"`},
		build{arch: "amd64", labels: `"org.opencontainers.image.version":"1.15.0"`})
	got, err := imagedigest.Registry{Client: srv.Client()}.Version(mbinRef(srv))
	if err != nil {
		t.Fatal(err)
	}
	if want := "1.15.0 (amd64), 1.15.1 (arm64)"; got != want {
		t.Errorf("got %q, want %q", got, want)
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
	if _, err := (imagedigest.Registry{}).Version("ghcr.io/paisans-software/mbin:develop"); err == nil {
		t.Error("a tag was read as a build")
	}
}

// The cache reads each build's label once for the run.
func TestTheCacheReadsAVersionOnce(t *testing.T) {
	srv, seen := labelled(t, `"org.opencontainers.image.version":"1.15.0-paisans"`)
	c := imagedigest.Cached(imagedigest.Registry{Client: srv.Client()})
	if got, err := c.Version(mbinRef(srv)); err != nil || got != "1.15.0-paisans" {
		t.Fatalf("got %q, %v", got, err)
	}
	first := len(*seen)
	if got, err := c.Version(mbinRef(srv)); err != nil || got != "1.15.0-paisans" {
		t.Fatalf("got %q, %v", got, err)
	}
	if len(*seen) != first {
		t.Errorf("the second ask read the registry again: %v", (*seen)[first:])
	}
}

// An image published for one platform has a manifest, not an index; its
// label is read from that manifest's configuration.
func TestVersionReadsASinglePlatformImage(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "/v2/paisans-software/mbin/"
		switch r.URL.Path {
		case base + "manifests/" + indexDigest:
			fmt.Fprintf(w, `{"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":%q}}`, configOf("arm64"))
		case base + "blobs/" + configOf("arm64"):
			fmt.Fprint(w, `{"architecture":"arm64","os":"linux","config":{"Labels":{"org.opencontainers.image.version":"3.1.0"}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	got, err := imagedigest.Registry{Client: srv.Client()}.Version(mbinRef(srv))
	if err != nil || got != "3.1.0" {
		t.Errorf("got %q, %v; want 3.1.0", got, err)
	}
}

func TestPlatformsNamesEveryLinuxBuild(t *testing.T) {
	srv, _ := published(t, build{arch: "amd64"}, build{arch: "arm64"})
	got, err := imagedigest.Registry{Client: srv.Client()}.Platforms(mbinRef(srv))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "amd64,arm64" {
		t.Errorf("got %v", got)
	}
}
