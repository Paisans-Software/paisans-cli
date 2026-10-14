package blueprints_test

import (
	"slices"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// RoleHostnames is the one place every consumer reads an app's hostnames
// beyond its primary from, so a role whose condition does not hold reaches
// none of them, even when it is declared.
func TestRoleHostnames(t *testing.T) {
	cfg := &config.Config{Community: config.Community{Domain: "example.org"}}
	cfg.Storage.Garage.Sites = []string{"home-a"}
	account := func(signup any, hostnames map[string]string) config.App {
		settings := map[string]any{}
		if signup != nil {
			settings["signup"] = signup
		}
		return config.App{Blueprint: config.BlueprintAccount, Hostname: "account.example.org", Hostnames: hostnames, Settings: settings}
	}
	for _, tc := range []struct {
		name string
		app  config.App
		want []blueprints.RoleHostname
	}{
		// Every account app takes signups, so every one has join., whatever
		// signup says: validate refuses a bad value by its own rule.
		{"invite signup, derived", account("invite", nil),
			[]blueprints.RoleHostname{{Role: "join", Hostname: "join.example.org", Derived: true}}},
		{"open signup, derived", account("open", nil),
			[]blueprints.RoleHostname{{Role: "join", Hostname: "join.example.org", Derived: true}}},
		{"open signup, declared", account("open", map[string]string{"join": "welcome.example.org"}),
			[]blueprints.RoleHostname{{Role: "join", Hostname: "welcome.example.org"}}},
		{"signup unset", account(nil, nil),
			[]blueprints.RoleHostname{{Role: "join", Hostname: "join.example.org", Derived: true}}},
		{"derived media", config.App{Blueprint: config.Blueprint("mbin"), Hostname: "talk.example.org"},
			[]blueprints.RoleHostname{{Role: "media", Hostname: "talk-media.example.org", Derived: true}}},
		{"local files, declared media", config.App{Blueprint: config.Blueprint("mbin"), Hostname: "talk.example.org", Storage: config.StorageLocal, Hostnames: map[string]string{"media": "m.example.org"}}, nil},
		{"declared wellknown", config.App{Blueprint: config.Blueprint("synapse"), Hostname: "chat.example.org", Hostnames: map[string]string{"wellknown": "example.org"}},
			[]blueprints.RoleHostname{{Role: "wellknown", Hostname: "example.org"}}},
	} {
		if got := blueprints.RoleHostnames(cfg, tc.app); !slices.Equal(got, tc.want) {
			t.Errorf("%s: RoleHostnames = %+v, want %+v", tc.name, got, tc.want)
		}
	}
	if got := blueprints.JoinHostname(cfg, config.App{Blueprint: config.Blueprint("mbin"), Hostname: "talk.example.org"}); got != "" {
		t.Errorf("JoinHostname of a forum = %q, want empty", got)
	}
}

func TestAccountSignup(t *testing.T) {
	for value, want := range map[any]string{"invite": "invite", "open": "open", "Invite": "", "closed": "", true: "", nil: ""} {
		app := config.App{Blueprint: config.BlueprintAccount, Settings: map[string]any{}}
		if value != nil {
			app.Settings["signup"] = value
		}
		if got := blueprints.AccountSignup(app); got != want {
			t.Errorf("AccountSignup(signup: %v) = %q, want %q", value, got, want)
		}
	}
}
