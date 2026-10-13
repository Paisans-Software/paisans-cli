package kinds

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// A locked setting with a condition is locked only while it holds, and its
// order is the blueprint's.
func TestLockedSettingsFollowTheirConditions(t *testing.T) {
	d, err := Parse("example", []byte(strings.Replace(minimal, "config: {format: env, file: .env}", `config:
  format: env
  file: .env
  locked_settings:
    - SITE_NAME
    - {name: SSO_ONLY, when: app.oidc}
    - {name: PRIVATE, when: app.gated}`, 1)), always)
	if err != nil {
		t.Fatal(err)
	}
	register("test/locked", d, t.TempDir())
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		delete(defs, "test/locked")
		delete(sources, "test/locked")
	})
	for _, c := range []struct {
		gated, oidc bool
		want        string
	}{
		{false, false, "SITE_NAME"},
		{true, false, "SITE_NAME,PRIVATE"},
		{true, true, "SITE_NAME,SSO_ONLY,PRIVATE"},
	} {
		got := strings.Join(LockedSettingsFor(config.App{Kind: "test/locked", Gated: c.gated}, c.oidc), ",")
		if got != c.want {
			t.Errorf("gated %v, oidc %v: %s, want %s", c.gated, c.oidc, got, c.want)
		}
	}
}

// A provider declares what it offers, each value a string over the app, and
// a consumer says which setting names its provider.
func TestALinkMistakeIsRefused(t *testing.T) {
	for change, want := range map[string]string{
		"provides:\n  - {role: Mail Server, values: {x: \"'a'\"}}\n":                                    "provides[0].role",
		"provides:\n  - {role: matrix-homeserver}\n":                                                    "provides[0].values",
		"provides:\n  - {role: matrix-homeserver, values: {base_url: \"1 + 1\"}}\n":                     "provides[0].values.base_url",
		"consumes:\n  - {role: matrix-homeserver}\n":                                                    "consumes[0].setting",
		"consumes:\n  - {role: Nothing, setting: x}\n":                                                  "consumes[0].role",
		"provides:\n  - {role: matrix-homeserver, values: {base_url: \"'https://' + app.hostname\"}}\n": "",
		"consumes:\n  - {role: matrix-homeserver, setting: homeserver}\n":                               "",
	} {
		_, err := Parse("example", []byte(minimal+change), always)
		switch {
		case want == "" && err != nil:
			t.Errorf("%q: %v", change, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%q: got %v, want %q", change, err, want)
		}
	}
}

// A provider's values are its blueprint's expressions over the app, and one
// outside the deployment offers what any provider of the role says of its
// hostname alone.
func TestLinkValuesAreTheProvidersExpressions(t *testing.T) {
	cfg := &config.Config{Apps: map[string]config.App{"chat": {Kind: "synapse", Hostname: "chat.example.org", Hostnames: map[string]string{WellknownRole: "example.org"}}}}
	got, err := LinkValues(cfg, "chat", "matrix-homeserver")
	if err != nil || got["base_url"] != "https://chat.example.org" || got["server_name"] != "example.org" {
		t.Errorf("got %v, %v", got, err)
	}
	away, err := LinkValuesAt("matrix-homeserver", "matrix.example.net")
	if err != nil || away["base_url"] != "https://matrix.example.net" || away["server_name"] != "matrix.example.net" {
		t.Errorf("outside the deployment: %v, %v", away, err)
	}
	if _, err := LinkValuesAt("nothing-provides-this", "x.example.org"); err == nil {
		t.Error("a role nobody provides was answered")
	}
}
