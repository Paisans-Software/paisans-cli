package kinds

import "github.com/paisans-software/paisans-stack/internal/config"

// OIDCClientSpec is what an app's client at the identity provider has to look
// like for the app to sign anyone in. `paisans apply` and `paisans oidc client
// create` create a client to it and refuse an existing one that differs.
type OIDCClientSpec struct {
	// CallbackURL is the one redirect URI the client must allow.
	CallbackURL string
	// LaunchURL is where Pocket ID's dashboard sends a member who picks the
	// app: the app's own address plus the kind's DashboardPath. A client
	// without one is not listed there. An app's sso_dashboard_link setting
	// replaces the path; see LaunchURL.
	LaunchURL string
	// PKCE is whether the app always sends a code challenge. A Pocket ID
	// client with PKCE on refuses every request without one
	// (oidc/authorization_service.go:750-755 at v2.14.0) and one with it off
	// accepts a challenge when sent, so this is on only for an app that
	// always sends one, and off for an app that never does.
	PKCE bool
	// AdminGroupSetting and MemberGroupSetting name the app `settings` keys
	// holding the admin group and the member group. The member group is the
	// one a person must be in to sign in, and the client is restricted at
	// Pocket ID to it and the admin group. A kind whose admin group is not an
	// operator's choice leaves AdminGroupSetting empty.
	AdminGroupSetting  string
	MemberGroupSetting string
	// DefaultAdminGroup and DefaultMemberGroup hold when the settings are
	// absent. A member facing kind always has a default member group, so an
	// empty declaration never gets an unrestricted client.
	DefaultAdminGroup  string
	DefaultMemberGroup string
	// ReadsGroups is whether the app reads the groups claim itself, mapping
	// the admin group to its administrators and refusing a sign in outside
	// the member group. For a kind that does not, the restriction at Pocket
	// ID is the whole control.
	ReadsGroups bool
}

// Groups is the admin group and the member group an app's client admits:
// the app's settings, or the kind's defaults. A blank or non-string setting
// counts as absent (see groupSetting).
func (s OIDCClientSpec) Groups(app config.App) (admin, member string) {
	return groupSetting(app.Settings, s.AdminGroupSetting, s.DefaultAdminGroup),
		groupSetting(app.Settings, s.MemberGroupSetting, s.DefaultMemberGroup)
}

// AdminGroupSource is the configuration key an app's admin group is read
// from, for a message telling an operator where to set it, or the kind when
// it is not a setting.
func (s OIDCClientSpec) AdminGroupSource(app string) string {
	if s.AdminGroupSetting == "" {
		return "apps." + app + ".kind"
	}
	return "apps." + app + ".settings." + s.AdminGroupSetting
}

// OIDCDef is how a kind's client must look, before an app's hostname and name
// are known. Every callback is read off the pinned image's source and cited in
// the kind's record, because a client with the wrong one fails at the end of
// the first sign in, after the member has authenticated.
type OIDCDef struct {
	// Callback is the one redirect URI the client must allow, for an app of
	// the kind named app served at hostname. Only synapse's depends on the
	// app's name.
	Callback func(hostname, app string) string
	// NoLaunch is set for a kind whose hostname serves no page for a member,
	// so the identity provider's dashboard lists no tile for it.
	NoLaunch bool

	// The rest are OIDCClientSpec's fields of the same names.
	PKCE               bool
	AdminGroupSetting  string
	MemberGroupSetting string
	DefaultAdminGroup  string
	DefaultMemberGroup string
	ReadsGroups        bool
}

// callbackPath is a Callback for a kind whose redirect URI is a fixed path on
// the app's own hostname.
func callbackPath(path string) func(hostname, app string) string {
	return func(hostname, _ string) string { return "https://" + hostname + path }
}

// OIDCClient is the client spec for app, an app of a kind served at
// hostname, and whether this toolkit knows one for it.
func OIDCClient(kind config.Kind, hostname, app string) (OIDCClientSpec, bool) {
	o := of(kind).OIDC
	if o == nil {
		return OIDCClientSpec{}, false
	}
	spec := OIDCClientSpec{
		CallbackURL:        o.Callback(hostname, app),
		PKCE:               o.PKCE,
		AdminGroupSetting:  o.AdminGroupSetting,
		MemberGroupSetting: o.MemberGroupSetting,
		DefaultAdminGroup:  o.DefaultAdminGroup,
		DefaultMemberGroup: o.DefaultMemberGroup,
		ReadsGroups:        o.ReadsGroups,
	}
	if !o.NoLaunch {
		spec.LaunchURL = LaunchURL(kind, hostname, "")
	}
	return spec, true
}
