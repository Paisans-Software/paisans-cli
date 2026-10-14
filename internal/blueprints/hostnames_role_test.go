package blueprints_test

import (
	"slices"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// RoleHostnames is the one place every consumer reads an app's hostnames
// beyond its primary from, so a role whose condition does not hold reaches
// none of them, even when it is declared.
func TestRoleHostnames(t *testing.T) {
	cfg := &config.Config{Community: config.Community{Domain: "example.org"}}
	cfg.Storage.Garage.Sites = []string{"home-a"}
	account := func(invites any, hostnames map[string]string) config.App {
		settings := map[string]any{"identity_app": "auth"}
		if invites != nil {
			settings["invites"] = invites
		}
		return config.App{Blueprint: config.BlueprintAccount, Hostname: "account.example.org", Hostnames: hostnames, Settings: settings}
	}
	for _, tc := range []struct {
		name string
		app  config.App
		want []blueprints.RoleHostname
	}{
		{"invites on, derived", account(true, nil),
			[]blueprints.RoleHostname{{Role: "invite", Hostname: "invite.example.org", Derived: true}}},
		{"invites on, declared", account(true, map[string]string{"invite": "join.example.org"}),
			[]blueprints.RoleHostname{{Role: "invite", Hostname: "join.example.org"}}},
		{"invites off, stale declared", account(false, map[string]string{"invite": "invite.example.org"}), nil},
		{"invites unset", account(nil, nil), nil},
		{"invites a string", account("true", nil), nil},
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
	if got := blueprints.InviteHostname(cfg, account(false, map[string]string{"invite": "invite.example.org"})); got != "" {
		t.Errorf("InviteHostname with invites off = %q, want empty", got)
	}
}
