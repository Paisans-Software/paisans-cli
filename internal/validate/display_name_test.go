package validate_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/validate"
)

// Each case sets display names on the valid fixture and expects one rule at
// one key. The valid fixture's docs is Outline, which has a client; auth is
// Pocket ID itself, which does not.
func TestDisplayNameRules(t *testing.T) {
	cases := []struct {
		name  string
		names map[string]string
		rule  string
		key   string
		level validate.Level
	}{
		{"too long", map[string]string{"docs": strings.Repeat("w", 51)}, "display-name-invalid", "apps.docs.display_name", validate.Refuse},
		{"surrounding space", map[string]string{"docs": "Wiki "}, "display-name-invalid", "apps.docs.display_name", validate.Refuse},
		// "e" followed by a combining acute accent, which NFC composes.
		{"not NFC", map[string]string{"docs": "Café"}, "display-name-invalid", "apps.docs.display_name", validate.Refuse},
		{"another app's name", map[string]string{"docs": "health"}, "duplicate-client-name", "apps.docs.display_name", validate.Refuse},
		{"two display names alike", map[string]string{"docs": "Ours", "health": "Ours"}, "duplicate-client-name", "apps.health.display_name", validate.Refuse},
		{"no client", map[string]string{"auth": "Sign in"}, "display-name-without-a-client", "apps.auth.display_name", validate.Warn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := load(t, "valid")
			for app, name := range tc.names {
				a := cfg.Apps[app]
				a.DisplayName = name
				cfg.Apps[app] = a
			}
			result := validate.Check(cfg)
			f := findingFor(t, result, tc.rule)
			if f.Key != tc.key || f.Level != tc.level {
				t.Errorf("want %s at %s as %v, got %s as %v", tc.rule, tc.key, tc.level, f.Key, f.Level)
			}
		})
	}
}

// A display name that is valid and unique is quiet, the longest Pocket ID
// accepts included, counted in characters rather than bytes.
func TestDisplayNameQuiet(t *testing.T) {
	for _, name := range []string{"Wiki", "Café", strings.Repeat("é", 50)} {
		cfg := load(t, "valid")
		a := cfg.Apps["docs"]
		a.DisplayName = name
		cfg.Apps["docs"] = a
		if result := validate.Check(cfg); len(result.Findings) != 0 {
			t.Errorf("display name %q: %v", name, result.Findings)
		}
	}
}
