package kinds

// oauth2ProxyDef is the oauth2-proxy kind.
var oauth2ProxyDef = Def{
	Services: []Service{
		// One instance, admitting members and admins: it runs the sign-in and
		// answers the gateway's forward_auth for every app that sets gated
		// or auto_login. It listens on 4180, this record's Port, spelled out
		// in the compose and Caddy snippet templates too, because a Service
		// here carries no port field and a port nobody declared is a port
		// nobody can route to.
		//
		// v7.15.4 checked against quay.io on 2026-09-15 by requesting the tag
		// through quay.io's API, which resolved (an OCI image index with ten
		// child manifests).
		{Name: "app", Image: "quay.io/oauth2-proxy/oauth2-proxy:v7.15.4", Purpose: "the gate, admitting members, listening on 4180"},
	},
	Port:         4180,
	ConfigFormat: ConfigEnv,
	ConfigFile:   ".env",
	// /ping answers before any auth and needs no Host
	// (pkg/middleware/healthcheck.go:44-53 at v7.15.4); ran the image: 200
	// OK.
	Health: Health{"/ping", "200"},
	// Part of the sign-in flow itself: gating it would gate the login. The
	// gate is never declared, so this can only be reached through a test.
	Gate: GateSpec{Gateable: false},
	// oauth2-proxy v7.15.4, the gate: the toolkit renders no redirect
	// URL, so the callback is the proxy prefix, /oauth2, plus /callback
	// (oauthproxy.go:53,175-178, pkg/apis/options/options.go:103) on the
	// host the request came in on (oauthproxy.go:1113-1135), and
	// /oauth2/start is reached only on the gate's own hostname
	// (caddy.snippet.gates.tmpl). PKCE is on: the .env renders
	// OAUTH2_PROXY_CODE_CHALLENGE_METHOD=S256, and with a method set
	// every sign in sends a challenge (oauthproxy.go:833-851). It reads
	// the groups claim (pkg/apis/options/providers.go:10) and admits a
	// session in any group of OAUTH2_PROXY_ALLOWED_GROUPS
	// (providers/provider_default.go:125-138), which compose.yaml renders
	// from these two groups, so the gate and its client admit the same
	// people: members, and admins, since an admin can see everything a
	// member can (founder decision, 2026-10-12). Neither is a setting. No
	// launch URL: the gate has no page of its own.
	OIDC: &OIDCDef{
		Callback:           callbackPath("/oauth2/callback"),
		NoLaunch:           true,
		PKCE:               true,
		DefaultAdminGroup:  AdminsGroup,
		DefaultMemberGroup: MembersGroup,
		ReadsGroups:        true,
	},
	AppSecrets:  []string{"cookie_secret"},
	SelfRotated: []string{"cookie_secret"},
}
