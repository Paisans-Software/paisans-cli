package imagedigest_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/imagedigest"
)

func TestPatternNewest(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		tags    []string
		want    string
	}{
		{"v2.*.*", []string{"v2.9.0", "v2.14.0", "v2.10.3", "v3.0.0", "v2.14.0-rc1", "latest"}, "v2.14.0"},
		{"3.0.*-alpine", []string{"3.0.9-alpine", "3.0.11-alpine", "3.0.11-alpine3.20", "3.1.0-alpine", "3.0.12"}, "3.0.11-alpine"},
		{"4.0-p*", []string{"4.0-p2", "4.0-p10", "4.1-p1", "4.0-p3"}, "4.0-p10"},
		{"1.1.*-oidc.*", []string{"1.1.0-oidc.4", "1.1.0-oidc.12", "1.1.1-oidc.1"}, "1.1.1-oidc.1"},
		{"17.*-trixie", []string{"17.6-trixie", "17.10-trixie", "17-trixie", "18.0-trixie"}, "17.10-trixie"},
		{"v1.*.*", []string{"v2.0.0"}, ""},
	} {
		p, err := imagedigest.Compile(tc.pattern)
		if err != nil {
			t.Fatalf("Compile(%q): %v", tc.pattern, err)
		}
		if got := p.Newest(tc.tags); got != tc.want {
			t.Errorf("%s: Newest = %q, want %q", tc.pattern, got, tc.want)
		}
	}
}

func TestCompileRefuses(t *testing.T) {
	for _, pattern := range []string{"v2.0.0", "v2.**", "v2.*/x", ""} {
		if _, err := imagedigest.Compile(pattern); err == nil {
			t.Errorf("Compile(%q) accepted it", pattern)
		}
	}
}

func TestPrefix(t *testing.T) {
	p, _ := imagedigest.Compile("v2.*.*")
	if got := p.Prefix(); got != "v2." {
		t.Errorf("Prefix = %q", got)
	}
}

func TestIsTrackAndOnTrack(t *testing.T) {
	if !imagedigest.IsTrack("ghcr.io/pocket-id/pocket-id:v2.*.*") || imagedigest.IsTrack("ghcr.io/pocket-id/pocket-id:v2.14.0") || imagedigest.IsTrack("localhost:5000/x") {
		t.Error("IsTrack is wrong")
	}
	track := "ghcr.io/pocket-id/pocket-id:v2.*.*"
	for ref, want := range map[string]bool{
		"ghcr.io/pocket-id/pocket-id:v2.14.0":                true,
		"ghcr.io/pocket-id/pocket-id:v2.14.0@sha256:abcdef": true,
		"ghcr.io/pocket-id/pocket-id:v3.0.0@sha256:abcdef":  false,
		"ghcr.io/example/pocket-id:v2.14.0":                  false,
		"ghcr.io/pocket-id/pocket-id":                        false,
	} {
		if got := imagedigest.OnTrack(track, ref); got != want {
			t.Errorf("OnTrack(%s) = %v, want %v", ref, got, want)
		}
	}
}

// paged is a registry listing paisans-software/caddy's tags over pages,
// each naming the next in a Link header as the distribution spec does.
func paged(t *testing.T, pages ...[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/paisans-software/caddy/tags/list" {
			http.NotFound(w, r)
			return
		}
		page := 0
		fmt.Sscan(r.URL.Query().Get("last"), &page)
		if page+1 < len(pages) {
			w.Header().Set("Link", fmt.Sprintf(`</v2/paisans-software/caddy/tags/list?n=2&last=%d>; rel="next"`, page+1))
		}
		fmt.Fprintf(w, `{"tags":["%s"]}`, strings.Join(pages[page], `","`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTrackReadsEveryPage(t *testing.T) {
	srv := paged(t, []string{"2.8.0", "2.9.1"}, []string{"2.10.0", "3.0.0"})
	host := strings.TrimPrefix(srv.URL, "https://")
	r := imagedigest.Registry{Client: srv.Client()}
	got, err := r.Track(host + "/paisans-software/caddy:2.*.*")
	if err != nil {
		t.Fatal(err)
	}
	if want := host + "/paisans-software/caddy:2.10.0"; got != want {
		t.Errorf("Track = %s, want %s", got, want)
	}
	_, err = r.Track(host + "/paisans-software/caddy:4.*")
	if !errors.Is(err, imagedigest.ErrNotFound) || !strings.Contains(err.Error(), "3.0.0") {
		t.Errorf("a track with no tag: %v", err)
	}
}

type tracker struct{ asked int }

func (f *tracker) Digest(string) (string, error) { return "", nil }
func (f *tracker) Track(ref string) (string, error) {
	f.asked++
	return fmt.Sprintf("example.org/x:1.%d", f.asked), nil
}

func TestCacheAnswersATrackOnce(t *testing.T) {
	f := &tracker{}
	c := imagedigest.Cached(f)
	a, _ := c.Track("example.org/x:1.*")
	b, _ := c.Track("example.org/x:1.*")
	if a != b || f.asked != 1 {
		t.Errorf("asked %d times, got %s then %s", f.asked, a, b)
	}
}
