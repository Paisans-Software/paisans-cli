package blueprints_test

import (
	"fmt"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// The monitor's client returns to the fork's own callback, always sends a
// code challenge, and admits exactly the group named in settings.admin_group:
// that group is both the one whose members become administrators and the one
// a person must be in to sign in at all, so the client is restricted to it at
// Pocket ID and a non-admin is refused before the monitor sees them.
func TestUptimesClientIsRestrictedToItsAdminGroup(t *testing.T) {
	spec, ok := blueprints.OIDCClient(config.BlueprintUptime, "health.example.org", "health")
	if !ok {
		t.Fatal("the toolkit does not know an uptime client")
	}
	if spec.CallbackURL != "https://health.example.org/login/oidc/callback" {
		t.Errorf("callback %s", spec.CallbackURL)
	}
	if !spec.PKCE {
		t.Error("the fork always sends a code challenge, so PKCE must be on")
	}
	app := config.App{Blueprint: config.BlueprintUptime, Hostname: "health.example.org", Settings: map[string]any{"admin_group": "admins"}}
	admin, member := spec.Groups(app)
	if admin != "admins" || member != "admins" {
		t.Errorf("groups admin=%q member=%q, want both admins", admin, member)
	}
	if got := spec.AdminGroupSource("health"); got != "apps.health.settings.admin_group" {
		t.Errorf("admin group source %q", got)
	}
}

// Mbin's groups are settings with the toolkit's defaults: a declaration that
// names neither gets a client restricted to members and admins, and the fork
// reads the same two names, so a member-only Mbin needs nothing declared.
func TestMbinsClientIsRestrictedToMembersAndAdminsByDefault(t *testing.T) {
	spec, _ := blueprints.OIDCClient(config.Blueprint("mbin"), "talk.example.org", "talk")
	admin, member := spec.Groups(config.App{Blueprint: config.Blueprint("mbin")})
	if admin != blueprints.AdminsGroup || member != blueprints.MembersGroup {
		t.Errorf("groups admin=%q member=%q, want %s and %s", admin, member, blueprints.AdminsGroup, blueprints.MembersGroup)
	}
	if !spec.ReadsGroups {
		t.Error("the fork reads the groups claim, so the spec must say so")
	}
	chosen := config.App{Blueprint: config.Blueprint("mbin"), Settings: map[string]any{"admin_group": "mods", "member_group": "crew"}}
	admin, member = spec.Groups(chosen)
	if admin != "mods" || member != "crew" {
		t.Errorf("groups admin=%q member=%q", admin, member)
	}
	if got := spec.AdminGroupSource("talk"); got != "apps.talk.settings.admin_group" {
		t.Errorf("admin group source %q", got)
	}
	// A blank or non-string value is as good as absent: the default holds
	// rather than an unrestricted client slipping through on a typo.
	blank := config.App{Blueprint: config.Blueprint("mbin"), Settings: map[string]any{"admin_group": "  ", "member_group": false}}
	admin, member = spec.Groups(blank)
	if admin != blueprints.AdminsGroup || member != blueprints.MembersGroup {
		t.Errorf("blank settings gave admin=%q member=%q", admin, member)
	}
}

// Wiki.js's and WriteFreely's clients are restricted at Pocket ID to the
// member group and admins. WriteFreely reads no groups claim, so for it the
// restriction is the whole control; Wiki.js reads the claim as well. Neither
// sends a code challenge in the configuration the toolkit renders, and a
// Pocket ID client with PKCE on refuses a request without one, so PKCE is off.
func TestWikiAndWriteFreelyClientsAreRestrictedAndSendNoChallenge(t *testing.T) {
	cases := []struct {
		blueprint   config.Blueprint
		callback    string
		readsGroups bool
	}{
		{config.Blueprint("wikijs"), "https://app.example.org/login/pocketid/callback", true},
		{config.Blueprint("writefreely"), "https://app.example.org/oauth/callback/generic", false},
	}
	for _, tc := range cases {
		spec, ok := blueprints.OIDCClient(tc.blueprint, "app.example.org", "app")
		if !ok {
			t.Errorf("the toolkit does not know a %s client", tc.blueprint)
			continue
		}
		if spec.CallbackURL != tc.callback {
			t.Errorf("%s callback %s, want %s", tc.blueprint, spec.CallbackURL, tc.callback)
		}
		if spec.PKCE {
			t.Errorf("%s: PKCE on, but the app sends no code challenge", tc.blueprint)
		}
		if spec.ReadsGroups != tc.readsGroups {
			t.Errorf("%s: reads the groups claim %v, want %v", tc.blueprint, spec.ReadsGroups, tc.readsGroups)
		}
		if spec.LaunchURL != "https://app.example.org" {
			t.Errorf("%s launch URL %s", tc.blueprint, spec.LaunchURL)
		}
		admin, member := spec.Groups(config.App{Blueprint: tc.blueprint})
		if admin != blueprints.AdminsGroup || member != blueprints.MembersGroup {
			t.Errorf("%s groups admin=%q member=%q", tc.blueprint, admin, member)
		}
		_, member = spec.Groups(config.App{Blueprint: tc.blueprint, Settings: map[string]any{"member_group": "crew"}})
		if member != "crew" {
			t.Errorf("%s member group %q with member_group set", tc.blueprint, member)
		}
	}
}

// Every blueprint that can sit behind the gate and has a client gets a
// restricted one from an empty declaration. An unrestricted client on a member
// facing app would admit anyone with a Pocket ID account.
func TestEveryGateableClientIsRestrictedByDefault(t *testing.T) {
	for _, blueprint := range []config.Blueprint{config.Blueprint("mbin"), config.Blueprint("wikijs"), config.Blueprint("writefreely"), config.Blueprint("element"), config.BlueprintUptime} {
		spec, ok := blueprints.OIDCClient(blueprint, "app.example.org", "app")
		if !ok || !blueprints.GateFor(blueprint).Gateable {
			continue
		}
		app := config.App{Blueprint: blueprint}
		if blueprint == config.BlueprintUptime {
			app.Settings = map[string]any{"admin_group": "admins"}
		}
		if _, member := spec.Groups(app); member == "" {
			t.Errorf("%s: an empty declaration gives an unrestricted client", blueprint)
		}
	}
}

// The homeserver's client belongs to its authentication service: it returns
// to that service's callback for the app's upstream provider, always sends a
// code challenge because mas.yaml sets pkce_method: always, and is restricted
// to the member group and admins, which is the whole control, since the
// service reads no groups claim. It has no dashboard tile.
func TestSynapsesClientIsTheAuthenticationServices(t *testing.T) {
	spec, ok := blueprints.OIDCClient(config.Blueprint("synapse"), "matrix.example.org", "chat")
	if !ok {
		t.Fatal("the toolkit does not know a synapse client")
	}
	if spec.CallbackURL != "https://matrix.example.org/upstream/callback/"+blueprints.StableULID("chat") {
		t.Errorf("callback %s, want the authentication service's", spec.CallbackURL)
	}
	if !spec.PKCE {
		t.Error("mas.yaml has MAS always send a code challenge, so PKCE must be on")
	}
	if spec.ReadsGroups {
		t.Error("MAS requests no groups claim, so the spec must not say it reads one")
	}
	if spec.LaunchURL != "" {
		t.Errorf("launch URL %s, want none", spec.LaunchURL)
	}
	admin, member := spec.Groups(config.App{Blueprint: config.Blueprint("synapse")})
	if admin != blueprints.AdminsGroup || member != blueprints.MembersGroup {
		t.Errorf("groups admin=%q member=%q", admin, member)
	}
	_, member = spec.Groups(config.App{Blueprint: config.Blueprint("synapse"), Settings: map[string]any{"member_group": "crew"}})
	if member != "crew" {
		t.Errorf("member group %q with member_group set", member)
	}
}

// The gate's client returns to oauth2-proxy's own callback on the gate's
// hostname, sends the S256 challenge the rendered .env asks for, and admits
// members and admins, whatever the settings say: an admin can see everything
// a member can, and the gate's compose file renders the same two groups.
func TestTheGatesClientAdmitsMembersAndAdmins(t *testing.T) {
	spec, ok := blueprints.OIDCClient(config.BlueprintOAuth2Proxy, "auth-gate.example.org", "gate")
	if !ok {
		t.Fatal("the toolkit does not know an oauth2-proxy client")
	}
	if spec.CallbackURL != "https://auth-gate.example.org/oauth2/callback" {
		t.Errorf("callback %s", spec.CallbackURL)
	}
	if !spec.PKCE {
		t.Error("the gate always sends a code challenge, so PKCE must be on")
	}
	if !spec.ReadsGroups {
		t.Error("the gate reads the groups claim, so the spec must say so")
	}
	if spec.LaunchURL != "" {
		t.Errorf("launch URL %s, want none", spec.LaunchURL)
	}
	app := config.App{Blueprint: config.BlueprintOAuth2Proxy, Settings: map[string]any{"member_group": "crew", "admin_group": "mods"}}
	admin, member := spec.Groups(app)
	if admin != blueprints.AdminsGroup || member != blueprints.MembersGroup {
		t.Errorf("groups admin=%q member=%q, want %s and %s", admin, member, blueprints.AdminsGroup, blueprints.MembersGroup)
	}
}

func TestExtraGroupsDropTheClientsOwnGroups(t *testing.T) {
	s := blueprints.OIDCClientSpec{AdminGroupSetting: "admin_group", MemberGroupSetting: "member_group", DefaultAdminGroup: "admins", DefaultMemberGroup: "members", ExtraGroupsSetting: "synced_groups"}
	app := config.App{Settings: map[string]any{"synced_groups": []any{"admins", "members", "moderators", "moderators"}}}
	if got := fmt.Sprint(s.ExtraGroups(app)); got != "[moderators]" {
		t.Errorf("ExtraGroups = %s", got)
	}
}

// The account service links to nothing of its own on the dashboard: its
// client has no launch URL, and says so.
func TestTheAccountClientHasNoLaunchURL(t *testing.T) {
	spec, ok := blueprints.OIDCClient(config.BlueprintAccount, "account.example.org", "account")
	if !ok {
		t.Fatal("the toolkit does not know an account client")
	}
	if spec.LaunchURL != "" {
		t.Errorf("launch URL %s, want none", spec.LaunchURL)
	}
	if !spec.NoLaunch {
		t.Error("the spec does not say the blueprint has no launch URL")
	}
}
