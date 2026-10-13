package imagedigest_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/imagedigest"
)

// answering is a registry that challenges for an anonymous token, as the one
// in registry does, then answers every manifest request with manifest. The
// token service answers token, 200 handing out a token.
func answering(t *testing.T, token, manifest int) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if token != http.StatusOK {
				w.WriteHeader(token)
				return
			}
			fmt.Fprint(w, `{"token":"anon"}`)
		case r.Header.Get("Authorization") != "Bearer anon":
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="test"`, srv.URL))
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(manifest)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Each way a registry can fail is told apart, so apply can say what to do
// about it: a tag that does not exist, an image an anonymous pull may not
// read, and a registry that did not answer usefully.
func TestDigestTellsTheFailuresApart(t *testing.T) {
	cases := []struct {
		name            string
		token, manifest int
		notFound        bool
		denied          bool
	}{
		{"missing tag", http.StatusOK, http.StatusNotFound, true, false},
		{"manifest refused", http.StatusOK, http.StatusUnauthorized, false, true},
		{"manifest forbidden", http.StatusOK, http.StatusForbidden, false, true},
		{"token refused", http.StatusUnauthorized, http.StatusOK, false, true},
		{"token forbidden", http.StatusForbidden, http.StatusOK, false, true},
		{"server error", http.StatusOK, http.StatusServiceUnavailable, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := answering(t, c.token, c.manifest)
			_, err := imagedigest.Registry{Client: srv.Client()}.Digest(reference(srv, "develop"))
			if err == nil {
				t.Fatal("resolved")
			}
			if got := errors.Is(err, imagedigest.ErrNotFound); got != c.notFound {
				t.Errorf("ErrNotFound is %v for %v", got, err)
			}
			if got := errors.Is(err, imagedigest.ErrDenied); got != c.denied {
				t.Errorf("ErrDenied is %v for %v", got, err)
			}
		})
	}
}

// A registry that could not be reached at all is neither.
func TestAnUnreachableRegistryIsNeitherMissingNorDenied(t *testing.T) {
	srv, _ := registry(t, true)
	ref := reference(srv, "2")
	srv.Close()
	_, err := imagedigest.Registry{Client: srv.Client()}.Digest(ref)
	if err == nil || errors.Is(err, imagedigest.ErrNotFound) || errors.Is(err, imagedigest.ErrDenied) {
		t.Fatalf("got %v", err)
	}
}
