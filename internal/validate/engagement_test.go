package validate_test

import (
	"testing"

	"github.com/paisans-software/paisans-cli/internal/validate"
)

// The Digest Bot's localpart is one Matrix accepts, or MAS refuses the
// account; anything else is refused at the setting.
func TestEngagementBotLocalpart(t *testing.T) {
	for _, c := range []struct {
		localpart string
		refused   bool
	}{
		{"Digest", true},
		{"digest bot", true},
		{"bot@x", true},
		{"community-bot", false},
		{"a.b_c=d/e-1", false},
	} {
		cfg := load(t, "engagement-valid")
		app := cfg.Apps["digest"]
		app.Settings = map[string]any{"bot_localpart": c.localpart}
		cfg.Apps["digest"] = app
		var at string
		for _, f := range validate.Check(cfg).Findings {
			if f.Rule == "engagement-bot-localpart-invalid" {
				if f.Level != validate.Refuse {
					t.Errorf("%q: %s is a %s, want a refusal", c.localpart, f.Rule, f.Level)
				}
				at = f.Key
			}
		}
		switch {
		case c.refused && at != "apps.digest.settings.bot_localpart":
			t.Errorf("%q: refused at %q, want apps.digest.settings.bot_localpart", c.localpart, at)
		case !c.refused && at != "":
			t.Errorf("%q: refused at %s, and it is a Matrix localpart", c.localpart, at)
		}
	}
}

// The bot's display name is any name a member would read, but not an empty
// one or one that breaks the .env line it is rendered into.
func TestEngagementBotDisplayName(t *testing.T) {
	for _, c := range []struct {
		name    string
		refused bool
	}{
		{"Community Bot", false},
		{"Paisans Bot", false},
		{"", true},
		{"   ", true},
		{"Two\nLines", true},
		{"Tab\tName", true},
	} {
		cfg := load(t, "engagement-valid")
		app := cfg.Apps["digest"]
		app.Settings = map[string]any{"bot_display_name": c.name}
		cfg.Apps["digest"] = app
		var at string
		for _, f := range validate.Check(cfg).Findings {
			if f.Rule == "engagement-bot-display-name-invalid" {
				if f.Level != validate.Refuse {
					t.Errorf("%q: %s is a %s, want a refusal", c.name, f.Rule, f.Level)
				}
				at = f.Key
			}
		}
		switch {
		case c.refused && at != "apps.digest.settings.bot_display_name":
			t.Errorf("%q: refused at %q, want apps.digest.settings.bot_display_name", c.name, at)
		case !c.refused && at != "":
			t.Errorf("%q: refused at %s", c.name, at)
		}
	}
}
