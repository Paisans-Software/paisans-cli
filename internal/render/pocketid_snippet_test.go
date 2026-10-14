package render_test

import (
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/render"
)

const authSnippet = "vm/srv/paisans/f2a9/infra/caddy/snippets/auth.caddy"

// refusedPaths reads every `@name path ...` matcher that a `respond @name
// 404` uses in a rendered snippet and returns its patterns.
func refusedPaths(t *testing.T, snippet string) []string {
	t.Helper()
	matchers := map[string][]string{}
	for _, m := range regexp.MustCompile(`(?m)^@(\S+) path (.+)$`).FindAllStringSubmatch(snippet, -1) {
		matchers[m[1]] = strings.Fields(m[2])
	}
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^respond @(\S+) 404$`).FindAllStringSubmatch(snippet, -1) {
		out = append(out, matchers[m[1]]...)
	}
	return out
}

// refused reports whether a request path is matched by a pattern the way
// Caddy's path matcher does for these patterns: lower-cased and cleaned, with
// a trailing * a prefix match.
func refused(patterns []string, reqPath string) bool {
	p := path.Clean(strings.ToLower(reqPath))
	if strings.HasSuffix(reqPath, "/") && p != "/" {
		p += "/"
	}
	for _, pat := range patterns {
		if prefix, ok := strings.CutSuffix(pat, "*"); ok {
			if strings.HasPrefix(p, prefix) {
				return true
			}
		} else if p == pat {
			return true
		}
	}
	return false
}

// Pocket ID's first-admin setup takes no authentication and is open while
// it has no user, so the gateway refuses it, its pages, and every spelling of
// them, while the signup routes that signup links use stay open.
func TestPocketIDSnippetRefusesFirstAdminSetup(t *testing.T) {
	plan, err := render.Build(fixture(t), fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	snippet, ok := planFiles(plan)[authSnippet]
	if !ok {
		t.Fatalf("%s was not rendered", authSnippet)
	}
	patterns := refusedPaths(t, snippet)
	for _, p := range []string{
		"/api/signup/setup", "/api/signup/setup/", "/API/Signup/Setup", "//api//signup/setup",
		"/api/signup/./setup", "/setup", "/setup/", "/signup/setup", "/signup/setup/",
		"/signup/setup/__data.json",
	} {
		if !refused(patterns, p) {
			t.Errorf("%s is not refused; refused patterns are %v", p, patterns)
		}
	}
	for _, p := range []string{
		"/", "/api/signup", "/signup", "/signup/add-passkey", "/api/signup-tokens",
		"/settings", "/login", "/lc/ABC", "/api/users/me",
	} {
		if refused(patterns, p) {
			t.Errorf("%s is refused; refused patterns are %v", p, patterns)
		}
	}
}
