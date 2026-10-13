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

// A link names a role the toolkit knows, and a consumer says which setting
// names its provider.
func TestALinkMistakeIsRefused(t *testing.T) {
	for change, want := range map[string]string{
		"provides: [mail-server]\n":                                       "not a role the toolkit links",
		"consumes:\n  - {role: matrix-homeserver}\n":                      "consumes[0].setting",
		"consumes:\n  - {role: nothing, setting: x}\n":                    "consumes[0].role",
		"provides: [matrix-homeserver]\n":                                 "",
		"consumes:\n  - {role: matrix-homeserver, setting: homeserver}\n": "",
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
