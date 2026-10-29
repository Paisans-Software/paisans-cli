package validate_test

import (
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
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

// With a homeserver linked, the bot's messages link to the web digest, which
// names the member by the gate's header, so an ungated engagement app is
// warned about: every link would answer not found. Gated, or with no
// homeserver, nothing is said.
func TestEngagementWithMatrixNotGated(t *testing.T) {
	for _, c := range []struct {
		name          string
		gated, matrix bool
		warned        bool
	}{
		{"matrix, not gated", false, true, true},
		{"matrix, gated", true, true, false},
		{"no matrix, not gated", false, false, false},
	} {
		cfg := load(t, "engagement-valid")
		if c.matrix {
			cfg.Apps["chat"] = config.App{Blueprint: "synapse", Hostname: "chat.example.org", Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-a"}}
		}
		app := cfg.Apps["digest"]
		app.Gated = c.gated
		cfg.Apps["digest"] = app
		var level validate.Level
		var at string
		for _, f := range validate.Check(cfg).Findings {
			if f.Rule == "engagement-not-gated" {
				level, at = f.Level, f.Key
			}
		}
		switch {
		case c.warned && (at != "apps.digest.gated" || level != validate.Warn):
			t.Errorf("%s: finding at %q level %v, want a warning at apps.digest.gated", c.name, at, level)
		case !c.warned && at != "":
			t.Errorf("%s: warned at %s", c.name, at)
		}
	}
}

// refusedAt is where rule refuses the engagement-valid fixture with the
// digest app's settings replaced by settings, "" when it does not.
func refusedAt(t *testing.T, rule string, settings map[string]any) string {
	t.Helper()
	cfg := load(t, "engagement-valid")
	app := cfg.Apps["digest"]
	app.Settings = settings
	cfg.Apps["digest"] = app
	for _, f := range validate.Check(cfg).Findings {
		if f.Rule == rule {
			if f.Level != validate.Refuse {
				t.Errorf("%v: %s is a %s, want a refusal", settings, f.Rule, f.Level)
			}
			return f.Key
		}
	}
	return ""
}

// The community room's name is one line a member reads, and it must leave
// a letter or digit for the room's alias.
func TestEngagementCommunityRoomName(t *testing.T) {
	const key = "apps.digest.settings.community_room_name"
	for name, refused := range map[string]bool{
		"":               true,
		"   ":            true,
		"a\tb":           true,
		"!!!":            true,
		"Riverside Hall": false,
		"Riverside-Club": false,
	} {
		at := refusedAt(t, "engagement-community-room-name-invalid", map[string]any{"community_room_name": name})
		switch {
		case refused && at != key:
			t.Errorf("%q: refused at %q, want %s", name, at, key)
		case !refused && at != "":
			t.Errorf("%q: refused at %s", name, at)
		}
	}
}

// The daily welcome email reaches at least one member.
func TestEngagementWelcomeLimit(t *testing.T) {
	const key = "apps.digest.settings.welcome_email_daily_limit"
	for limit, refused := range map[int]bool{0: true, -1: true, 1: false, 50: false} {
		at := refusedAt(t, "engagement-welcome-limit-invalid", map[string]any{"welcome_email_daily_limit": limit})
		switch {
		case refused && at != key:
			t.Errorf("%d: refused at %q, want %s", limit, at, key)
		case !refused && at != "":
			t.Errorf("%d: refused at %s", limit, at)
		}
	}
}

// Unset, the room is named for the community, so a community name that
// would break the .env line is refused at the community's name.
func TestEngagementDerivedCommunityRoomName(t *testing.T) {
	cfg := load(t, "engagement-valid")
	cfg.Community.Name = "Riverside\nHall"
	var at string
	for _, f := range validate.Check(cfg).Findings {
		if f.Rule == "engagement-community-room-name-invalid" {
			at = f.Key
		}
	}
	if at != "community.name" {
		t.Errorf("refused at %q, want community.name", at)
	}
}

// A limit that is not a whole number is the setting's type, not this rule.
func TestEngagementWelcomeLimitNotAnInt(t *testing.T) {
	for _, limit := range []any{"12", 2.5} {
		cfg := load(t, "engagement-valid")
		app := cfg.Apps["digest"]
		app.Settings = map[string]any{"welcome_email_daily_limit": limit}
		cfg.Apps["digest"] = app
		var typed, ranged bool
		for _, f := range validate.Check(cfg).Findings {
			switch f.Rule {
			case "blueprint-setting-invalid":
				typed = typed || f.Key == "apps.digest.settings.welcome_email_daily_limit"
			case "engagement-welcome-limit-invalid":
				ranged = true
			}
		}
		if !typed || ranged {
			t.Errorf("%#v: blueprint-setting-invalid %v, engagement-welcome-limit-invalid %v; want only the first", limit, typed, ranged)
		}
	}
}

// The service calls the forum's API on its own site's mesh address, so a
// forum that is not clustered like it is refused.
func TestEngagementForumNotClustered(t *testing.T) {
	cfg := load(t, "engagement-valid")
	var talk string
	for _, name := range cfg.AppNames() {
		if cfg.Apps[name].Blueprint == "mbin" {
			talk = name
		}
	}
	app := cfg.Apps[talk]
	app.Placement = config.Placement{Mode: config.PlacementPinned, Site: firstSite(cfg)}
	cfg.Apps[talk] = app
	var at string
	for _, f := range validate.Check(cfg).Findings {
		if f.Rule == "engagement-forum-not-clustered" {
			if f.Level != validate.Refuse {
				t.Errorf("%s is a %s, want a refusal", f.Rule, f.Level)
			}
			at = f.Key
		}
	}
	if at != "apps.digest.settings.forum" {
		t.Errorf("refused at %q, want apps.digest.settings.forum", at)
	}
	if at := refusedAt(t, "engagement-forum-not-clustered", nil); at != "" {
		t.Errorf("a clustered forum refused at %s", at)
	}
}

func firstSite(cfg *config.Config) string {
	for name := range cfg.Sites {
		return name
	}
	return ""
}
