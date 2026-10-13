package kinds_test

import (
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// The monitor's client returns to the fork's own callback, always sends a
// code challenge, and admits exactly the group named in settings.admin_group:
// that group is both the one whose members become administrators and the one
// a person must be in to sign in at all, so the client is restricted to it at
// Pocket ID and a non-admin is refused before the monitor sees them.
func TestUptimesClientIsRestrictedToItsAdminGroup(t *testing.T) {
	spec, ok := kinds.OIDCClient(config.KindUptime, "health.example.org", "health")
	if !ok {
		t.Fatal("the toolkit does not know an uptime client")
	}
	if spec.CallbackURL != "https://health.example.org/login/oidc/callback" {
		t.Errorf("callback %s", spec.CallbackURL)
	}
	if !spec.PKCE {
		t.Error("the fork always sends a code challenge, so PKCE must be on")
	}
	app := config.App{Kind: config.KindUptime, Hostname: "health.example.org", Settings: map[string]any{"admin_group": "admins"}}
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
	spec, _ := kinds.OIDCClient(config.KindMbin, "talk.example.org", "talk")
	admin, member := spec.Groups(config.App{Kind: config.KindMbin})
	if admin != kinds.AdminsGroup || member != kinds.MembersGroup {
		t.Errorf("groups admin=%q member=%q, want %s and %s", admin, member, kinds.AdminsGroup, kinds.MembersGroup)
	}
	if !spec.ReadsGroups {
		t.Error("the fork reads the groups claim, so the spec must say so")
	}
	chosen := config.App{Kind: config.KindMbin, Settings: map[string]any{"admin_group": "mods", "member_group": "crew"}}
	admin, member = spec.Groups(chosen)
	if admin != "mods" || member != "crew" {
		t.Errorf("groups admin=%q member=%q", admin, member)
	}
	if got := spec.AdminGroupSource("talk"); got != "apps.talk.settings.admin_group" {
		t.Errorf("admin group source %q", got)
	}
	// A blank or non-string value is as good as absent: the default holds
	// rather than an unrestricted client slipping through on a typo.
	blank := config.App{Kind: config.KindMbin, Settings: map[string]any{"admin_group": "  ", "member_group": false}}
	admin, member = spec.Groups(blank)
	if admin != kinds.AdminsGroup || member != kinds.MembersGroup {
		t.Errorf("blank settings gave admin=%q member=%q", admin, member)
	}
}

// Outline and WriteFreely read no groups claim, so their clients are
// restricted at Pocket ID to the member group and admins, and nothing else:
// the restriction is the whole control. Neither sends a code challenge in the
// configuration the toolkit renders, and a Pocket ID client with PKCE on
// refuses a request without one, so PKCE is off.
func TestOutlineAndWriteFreelyClientsAreRestrictedAndSendNoChallenge(t *testing.T) {
	cases := []struct {
		kind     config.Kind
		callback string
	}{
		{config.KindOutline, "https://docs.example.org/auth/oidc.callback"},
		{config.KindWriteFreely, "https://docs.example.org/oauth/callback/generic"},
	}
	for _, tc := range cases {
		spec, ok := kinds.OIDCClient(tc.kind, "docs.example.org", "docs")
		if !ok {
			t.Errorf("the toolkit does not know a %s client", tc.kind)
			continue
		}
		if spec.CallbackURL != tc.callback {
			t.Errorf("%s callback %s, want %s", tc.kind, spec.CallbackURL, tc.callback)
		}
		if spec.PKCE {
			t.Errorf("%s: PKCE on, but the app sends no code challenge", tc.kind)
		}
		if spec.ReadsGroups {
			t.Errorf("%s: says it reads the groups claim, and it does not", tc.kind)
		}
		if spec.LaunchURL != "https://docs.example.org" {
			t.Errorf("%s launch URL %s", tc.kind, spec.LaunchURL)
		}
		admin, member := spec.Groups(config.App{Kind: tc.kind})
		if admin != kinds.AdminsGroup || member != kinds.MembersGroup {
			t.Errorf("%s groups admin=%q member=%q", tc.kind, admin, member)
		}
		_, member = spec.Groups(config.App{Kind: tc.kind, Settings: map[string]any{"member_group": "crew"}})
		if member != "crew" {
			t.Errorf("%s member group %q with member_group set", tc.kind, member)
		}
	}
}

// Every kind that can sit behind the gate and has a client gets a restricted
// one from an empty declaration. An unrestricted client on a member facing
// app would admit anyone with a Pocket ID account.
func TestEveryGateableClientIsRestrictedByDefault(t *testing.T) {
	for _, kind := range []config.Kind{config.KindMbin, config.KindOutline, config.KindWriteFreely, config.KindElement, config.KindUptime} {
		spec, ok := kinds.OIDCClient(kind, "app.example.org", "app")
		if !ok || !kinds.GateFor(kind).Gateable {
			continue
		}
		app := config.App{Kind: kind}
		if kind == config.KindUptime {
			app.Settings = map[string]any{"admin_group": "admins"}
		}
		if _, member := spec.Groups(app); member == "" {
			t.Errorf("%s: an empty declaration gives an unrestricted client", kind)
		}
	}
}

// The homeserver's client belongs to its authentication service: it returns
// to that service's callback for the app's upstream provider, always sends a
// code challenge because mas.yaml sets pkce_method: always, and is restricted
// to the member group and admins, which is the whole control, since the
// service reads no groups claim. It has no dashboard tile.
func TestSynapsesClientIsTheAuthenticationServices(t *testing.T) {
	spec, ok := kinds.OIDCClient(config.KindSynapse, "matrix.example.org", "chat")
	if !ok {
		t.Fatal("the toolkit does not know a synapse client")
	}
	if spec.CallbackURL != kinds.MASRedirectURI("matrix.example.org", "chat") {
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
	admin, member := spec.Groups(config.App{Kind: config.KindSynapse})
	if admin != kinds.AdminsGroup || member != kinds.MembersGroup {
		t.Errorf("groups admin=%q member=%q", admin, member)
	}
	_, member = spec.Groups(config.App{Kind: config.KindSynapse, Settings: map[string]any{"member_group": "crew"}})
	if member != "crew" {
		t.Errorf("member group %q with member_group set", member)
	}
}

// The gate's client returns to oauth2-proxy's own callback on the gate's
// hostname, sends the S256 challenge the rendered .env asks for, and admits
// members and admins, whatever the settings say: an admin can see everything
// a member can, and the gate's compose file renders the same two groups.
func TestTheGatesClientAdmitsMembersAndAdmins(t *testing.T) {
	spec, ok := kinds.OIDCClient(config.KindOAuth2Proxy, "auth-gate.example.org", "gate")
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
	app := config.App{Kind: config.KindOAuth2Proxy, Settings: map[string]any{"member_group": "crew", "admin_group": "mods"}}
	admin, member := spec.Groups(app)
	if admin != kinds.AdminsGroup || member != kinds.MembersGroup {
		t.Errorf("groups admin=%q member=%q, want %s and %s", admin, member, kinds.AdminsGroup, kinds.MembersGroup)
	}
}
