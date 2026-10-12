package kinds

// outlineDef is the outline kind.
var outlineDef = Def{
	Services: []Service{
		{Name: "app", Image: "outlinewiki/outline:1.10.0", Purpose: "the application"},
		{Name: PostgresService, Purpose: "its own database, when the app is pinned"},
	},
	Port:         3000,
	ConfigFormat: ConfigEnv,
	ConfigFile:   ".env",
	SettingKeys: []SettingKey{
		{Key: "AWS_REGION", File: ".env", Setting: "s3_region"},
		{Key: "AWS_S3_FORCE_PATH_STYLE", File: ".env", Setting: "s3_force_path_style"},
		{Key: "AWS_S3_UPLOAD_BUCKET_NAME", File: ".env", Setting: "s3_bucket"},
	},
	// Runs SELECT 1 and pings Redis, 500 when either fails
	// (server/main.ts:78-105 at v1.10.0).
	Health: Health{"/_health", "200"},
	// Cited at outline v1.10.0 (65be53c3).
	Gate: GateSpec{
		Gateable: true,
		OpenPaths: []string{
			// OIDC: /auth/oidc, its /auth/oidc.callback, and /auth/redirect
			// (oidcRouter.ts:63,277-289, routes/auth/index.ts:35-36). Named
			// rather than /auth/*, which would open every other sign-in
			// provider's routes too (Eg: email sign-in, which sends mail to
			// any address it is given).
			"/auth/oidc", "/auth/oidc.callback", "/auth/redirect",
			// What an OAuth client, an MCP client among them, needs before it
			// holds a token: discovery, registration and the token endpoint
			// (index.ts:116-170, oauth/index.ts:181-200). /oauth/authorize is
			// a browser navigation and stays behind the gate.
			"/.well-known/oauth-authorization-server*", "/.well-known/oauth-protected-resource*",
			"/oauth/token", "/oauth/register", "/oauth/revoke",
		},
		// An API key is created only from a signed-in session
		// (apiKeys.ts:23-27), and Outline's OAuth server issues tokens only
		// through authorization_code and refresh_token (OAuthInterface.ts:59).
		TokenPaths: []string{"/api/*", "/mcp"},
		// No auto-login: the sign-in screen redirects itself when OIDC is
		// the only provider (Login.tsx:79-93).
	},
	HostnameRoles: []string{MediaRole},
	// Its bucket holds the attachments of documents that are readable only to
	// members, and its server presigns every read it issues, so its objects
	// are not public. It has no LocalMediaPaths either: its local store
	// answers through its own API with the reader's session or a signature it
	// issued, so there is nothing to open.
	ObjectStorage: true,
	// outline v1.10.0: the callback is ${URL}/auth/${config.id}.callback
	// with the provider id oidc (plugins/oidc/server/auth/oidcRouter.ts:63,
	// registered at :249-253). PKCE is off because the toolkit renders the
	// four explicit OIDC_*_URI values, and the manual configuration branch
	// never passes pkce (plugins/oidc/server/auth/oidc.ts:27-35; only the
	// OIDC_ISSUER_URL branch does, at :55). Outline reads no groups claim,
	// so the restriction at Pocket ID is the whole control.
	OIDC: &OIDCDef{
		Callback:           callbackPath("/auth/oidc.callback"),
		PKCE:               false,
		MemberGroupSetting: "member_group",
		DefaultAdminGroup:  AdminsGroup,
		DefaultMemberGroup: MembersGroup,
	},
}
