package render_test

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/render"
)

const authSnippet = "vm/srv/paisans/f2a9/infra/caddy/snippets/auth.caddy"

// refusedPaths reads every `@name path ...` matcher that a `respond @name
// 404` uses in a rendered snippet and returns its patterns.
func refusedPaths(snippet string) []string {
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

// Refused on every Pocket ID, whatever its configuration.
var setupPaths = []string{
	"/api/signup/setup", "/api/signup/setup/", "/API/Signup/Setup", "//api//signup/setup",
	"/api/signup/./setup", "/setup", "/setup/", "/signup/setup", "/signup/setup/",
	"/signup/setup/__data.json",
}

// Refused while self signup is closed.
var signupPaths = []string{
	"/api/signup", "/api/signup/", "/API/SIGNUP", "/signup", "/signup/", "/Signup",
	"/signup/add-passkey", "/signup/__data.json", "/st/ABCDEF",
}

// Refused while the email sign-in code is off.
var emailCodePaths = []string{
	"/api/one-time-access-email", "/api/one-time-access-email/", "/API/One-Time-Access-Email",
	"/login/alternative/email", "/login/alternative/email/__data.json",
}

// Never refused: sign in, the toolkit's and the account app's API calls, and
// administrators' routes.
var openPaths = []string{
	"/", "/login", "/login/alternative", "/login/alternative/code", "/lc/ABCDEF",
	"/api/one-time-access-token/ABCDEF", "/api/users", "/api/users/u1/one-time-access-email",
	"/api/users/u1/one-time-access-token", "/api/user-groups", "/api/signup-tokens",
	"/api/oidc/clients", "/api/oidc/token", "/authorize", "/.well-known/openid-configuration",
	"/api/application-images/logo", "/api/audit-logs/all", "/settings", "/settings/admin/users",
}

// exposureCase is one deployment: management paisans, with the account app
// and neither setting declared, since the toolkit renders both; or
// management pocket-id, with no account app and the two settings as given.
type exposureCase struct {
	name      string
	paisans   bool
	signups   any // nil is unset
	emailCode any // nil is unset
}

// signupClosed is the rendered allow_user_signups: disabled under paisans,
// and the declared value, whose default is disabled, under pocket-id.
func (c exposureCase) signupClosed() bool {
	return c.paisans || c.signups == nil || c.signups == "disabled"
}

// emailCodeClosed is the rendered email_one_time_access_as_unauthenticated:
// true under paisans, and the declared value, whose default is false, under
// pocket-id.
func (c exposureCase) emailCodeClosed() bool { return !c.paisans && c.emailCode != true }

// renderAuthSnippet renders the fixture with the Pocket ID app's management
// and its two settings replaced, keeping the account app under paisans and
// dropping it under pocket-id, and returns the gateway's snippet for it.
func renderAuthSnippet(t *testing.T, c exposureCase) string {
	t.Helper()
	cfg, secrets := fixture(t), fixtureSecrets(t)
	app := cfg.Apps["auth"]
	settings := map[string]any{}
	for k, v := range app.Settings {
		settings[k] = v
	}
	delete(settings, "allow_user_signups")
	delete(settings, "email_one_time_access_as_unauthenticated")
	settings["management"] = "pocket-id"
	if c.paisans {
		settings["management"] = "paisans"
	}
	if c.signups != nil {
		settings["allow_user_signups"] = c.signups
	}
	if c.emailCode != nil {
		settings["email_one_time_access_as_unauthenticated"] = c.emailCode
	}
	app.Settings = settings
	cfg.Apps["auth"] = app
	if !c.paisans {
		delete(cfg.Apps, "account")
		delete(secrets.Apps, "account")
		delete(secrets.OIDCClients, "account")
	}
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	snippet, ok := planFiles(plan)[authSnippet]
	if !ok {
		t.Fatalf("%s was not rendered", authSnippet)
	}
	return snippet
}

func exposureCases() []exposureCase {
	cases := []exposureCase{
		{name: "public self-service under pocket-id", signups: "open", emailCode: true},
		{name: "management paisans", paisans: true},
	}
	for _, signups := range []any{nil, "disabled", "withToken", "open"} {
		for _, emailCode := range []any{nil, false, true} {
			cases = append(cases, exposureCase{
				name:    fmt.Sprintf("pocket-id signups=%v email_code=%v", signups, emailCode),
				signups: signups, emailCode: emailCode,
			})
		}
	}
	return cases
}

// The gateway refuses Pocket ID's first-admin setup always, and each route
// that serves only a feature the rendered configuration turns off, while
// sign in and every route the toolkit and the account app call stay open.
func TestPocketIDSnippetExposesOnlyWhatTheConfigurationTurnsOn(t *testing.T) {
	for _, c := range exposureCases() {
		t.Run(c.name, func(t *testing.T) {
			patterns := refusedPaths(renderAuthSnippet(t, c))
			check := func(paths []string, want bool, what string) {
				for _, p := range paths {
					if got := refused(patterns, p); got != want {
						t.Errorf("%s (%s): refused is %v, want %v; refused patterns are %v", p, what, got, want, patterns)
					}
				}
			}
			check(setupPaths, true, "first-admin setup")
			check(signupPaths, c.signupClosed(), "self signup")
			check(emailCodePaths, c.emailCodeClosed(), "email sign-in code")
			check(openPaths, false, "always open")
		})
	}
}

// The two deployments the rule is written for, spelled out: a public
// self-service Pocket ID under management pocket-id keeps its signup pages,
// and a deployment under management paisans closes them and keeps the
// email-code recovery its members rely on.
func TestPocketIDSnippetNamedDeployments(t *testing.T) {
	public := refusedPaths(renderAuthSnippet(t, exposureCase{signups: "open", emailCode: true}))
	if refused(public, "/signup") || refused(public, "/api/signup") {
		t.Errorf("a public self-service Pocket ID refuses signup: %v", public)
	}
	invite := refusedPaths(renderAuthSnippet(t, exposureCase{paisans: true}))
	if !refused(invite, "/signup") || !refused(invite, "/api/signup") {
		t.Errorf("management paisans leaves signup open: %v", invite)
	}
	if refused(invite, "/api/one-time-access-email") || refused(invite, "/login/alternative/email") {
		t.Errorf("management paisans refuses the email-code recovery: %v", invite)
	}
}
