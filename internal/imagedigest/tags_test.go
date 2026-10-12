package imagedigest_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/imagedigest"
)

// tagged is a registry that challenges for an anonymous token, as registry
// does, and lists tags for the repository paisans-software/caddy.
func tagged(t *testing.T, tags string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			fmt.Fprint(w, `{"token":"anon"}`)
		case r.Header.Get("Authorization") != "Bearer anon":
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="test"`, srv.URL))
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/v2/paisans-software/caddy/tags/list":
			fmt.Fprintf(w, `{"name":"paisans-software/caddy","tags":%s}`, tags)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The newest version among a repository's tags is the one with the highest
// version number; channels, branches and other tags are not versions.
func TestNewestVersionLooksForTheHighestVersionTag(t *testing.T) {
	srv := tagged(t, `["stack-1","main","1.9.0-paisans","1.15.0-paisans","1.14.2-paisans","sha-0a1b2c3","latest"]`)
	got, err := imagedigest.Registry{Client: srv.Client()}.NewestVersion(reference(srv, "stack-1"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.15.0-paisans" {
		t.Errorf("got %q, want 1.15.0-paisans", got)
	}
}

// A repository with no version tag has no newest version, and that is not
// an error.
func TestNewestVersionOfARepositoryWithoutVersions(t *testing.T) {
	srv := tagged(t, `["stack-1","main"]`)
	got, err := imagedigest.Registry{Client: srv.Client()}.NewestVersion(reference(srv, "stack-1"))
	if err != nil || got != "" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestNewestVersionPicksByNumberNotText(t *testing.T) {
	for want, tags := range map[string][]string{
		"1.15.0-paisans": {"1.9.0-paisans", "1.15.0-paisans", "1.10.3-paisans"},
		"v2.0.0":         {"v1.99.9", "v2.0.0", "v1.100.0"},
		"0.21.1-wisp":    {"0.21.1-wisp", "0.21.0-wisp", "0.3.0"},
		"":               {"stack-1", "2", "2.11", "latest"},
	} {
		if got := imagedigest.Newest(tags); got != want {
			t.Errorf("Newest(%v) = %q, want %q", tags, got, want)
		}
	}
}
