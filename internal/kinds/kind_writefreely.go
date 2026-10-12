package kinds

// writefreelyDef is the writefreely kind.
var writefreelyDef = Def{
	Services: []Service{
		// This kind means the writefreely-wisp fork, not upstream WriteFreely.
		// The fork adds Postgres and S3 support upstream does not have, so the
		// blog joins the same Patroni cluster and the same Garage node as
		// everything else instead of a SQLite file nothing else can reach.
		// Upstream's image has no [database] Postgres driver and no [storage]
		// section at all, so it will reject the configuration this kind
		// renders; do not point this kind at ghcr.io/writefreely/writefreely.
		//
		// stack-1 is the fork's toolkit channel: the latest release a
		// maintainer has promoted as working with what this toolkit renders
		// (docs/specs/2026-10-11-stack-channels.md). The stack-1 contract is
		// this kind's templates: config.ini's locked_settings, [app]
		// authorized_fetch, the S3 storage with image_url_base, and the
		// /healthz route the monitor checks. Its first promotion is
		// 0.21.1+wisp, the first release carrying locked settings, published
		// as the image tag 0.21.1-wisp. The tag moves by design, so
		// RepositoryVolumes records what the image declares.
		{Name: "app", Image: "ghcr.io/josephquigley/writefreely-wisp:stack-1", Purpose: "the application"},
		{Name: PostgresService, Purpose: "its own database, when the app is pinned"},
	},
	// Not WriteFreely's default 8080: Mbin publishes 8080, and a pinned blog
	// on an apps site sits beside a clustered Mbin. WriteFreely reads its
	// port from the config.ini this toolkit renders, so the number is ours.
	Port: 8081,
	// It also renders a `.env`, but only for its Postgres container's
	// password. The application reads the file below, so that is where a
	// passthrough key means anything.
	ConfigFormat: ConfigINI,
	ConfigFile:   "config.ini",
	SettingKeys: []SettingKey{
		{Key: "app.authorized_fetch", File: "config.ini", Setting: "authorized_fetch"},
		{Key: "app.federation", File: "config.ini", Setting: "federation"},
		{Key: "app.min_username_len", File: "config.ini", Setting: "min_username_len"},
		{Key: "app.private", File: "config.ini", Setting: "private"},
		{Key: "app.site_name", File: "config.ini", Setting: "site_name"},
		{Key: "app.theme", File: "config.ini", Setting: "theme"},
		{Key: "storage.s3_bucket", File: "config.ini", Setting: "s3_bucket"},
		{Key: "storage.s3_region", File: "config.ini", Setting: "s3_region"},
		{Key: "uploads.max_size_mb", File: "config.ini", Setting: "uploads_max_size_mb"},
	},
	// The template's [uploads] comment says "dir is deliberately absent".
	OmittedKeys: []OmittedKey{
		{Key: "uploads.dir", Why: "uploads are stored in S3 through [storage], and dir names a path on one node that only the local image store reads, so it would do nothing here and suggest that it did"},
	},
	// The writefreely-wisp fork's status-only route, from v0.21.1+wisp, the
	// first stack-1 release: GET or HEAD, 200 with an empty body when the
	// database answers SELECT 1, an empty 503 when it does not, with no
	// session or private mode check (healthz.go:19-50 at develop f95d840).
	// healthz is a reserved username, so no blog can claim the path
	// (author/author.go:59).
	Health: Health{"/healthz", "200"},
	// Cited at writefreely-wisp ff9dceb, except signed fetch, which is cited
	// at v0.21.1+wisp, the first stack-1 release.
	Gate: GateSpec{
		Gateable: true,
		OpenPaths: []string{
			// OIDC: /oauth/generic, /oauth/callback/generic, and
			// /oauth/signup, the form a first sign-in submits
			// (oauth.go:320-322).
			"/oauth/*",
			// Discovery (routes.go:88-96). webfinger and host-meta open to
			// anyone once an allow list is set (handle.go:72-77); nodeinfo
			// still checks a signature, token or session in private mode.
			"/.well-known/webfinger", "/.well-known/host-meta", "/.well-known/nodeinfo", "/api/nodeinfo",
			// What the sign-in page loads, served by the static fallback with
			// no privacy check (routes.go:55, templates/base.tmpl:5-91).
			"/css/*", "/fonts/*", "/js/*", "/img/*", "/favicon.ico", "/local/custom.css",
		},
		// A blog's inbox, which checks the delivery's signature
		// (routes.go:181, activitypub.go:405-409).
		InboxPaths: []string{"/api/collections/*/inbox"},
		// Empty: tokens come only from a password sign-in or signup
		// (account.go:540), which disable_password_auth closes, but tokens
		// already issued keep working and carry no scopes (handle.go:281-292).
		AutoLogin: []AutoLoginRule{
			// A signed-in member has no reason to be on /login.
			// viewOauthInit takes no destination (oauth.go:135-166).
			{Paths: []string{"/login"}, Target: "/oauth/generic"},
			// wfu is set for anonymous visitors too (session.go:55-72), so
			// only its absence means anything: a first visit.
			{Paths: []string{"/"}, Target: "/oauth/generic", AbsentCookies: []string{"wfu"}},
		},
		// The sign-in page, the one the rule above sends to /oauth/generic.
		SignInPath: "/login",
		// authorized_fetch requires a valid signature on every ActivityPub
		// read and inbox delivery, public or private, and leaves only
		// discovery and the instance actor open (authorized_fetch.go:35-50 at
		// v0.21.1+wisp, develop f95d840). Private mode is what makes it read
		// only from the allowlist: with no allowlist, authorized_fetch admits
		// any signer (federation_allowlist.go:213-216), while private mode
		// admits a signer only through one (federation_allowlist.go:486-495),
		// and an allowlist needs private mode (federation_allowlist.go:84-86).
		// Both are locked in config.ini. See SignedFetchFor.
		//
		// A blog's outbox is an ActivityPub only route (authorized_fetch.go:
		// 28-33), refused before any lookup (authorized_fetch.go:72-98), so
		// an unsigned GET of any alias answers 401 (federation_allowlist.go:
		// 336) whether the blog exists or not.
		SignedFetch: &SignedFetch{
			Settings: []string{"app.authorized_fetch", "app.private"},
			Probe:    "/api/collections/{hostname}/outbox",
			Expect:   "401",
			// Without private mode the blog has no allowlist and admits a
			// signature from any instance.
			Requires: "private",
		},
	},
	// Each is rendered into config.ini's [app] and named in its
	// locked_settings, which the writefreely-wisp fork reads from the file and
	// refuses to change from its admin page or its own command line
	// (config/locked.go at v0.21.1+wisp).
	GateSettings: []GateSetting{
		{
			Name: "app.private", Want: "true", When: gated,
			Why:       "only private mode makes the blog read from its allowlist alone; without it authorized fetch admits any signer",
			ConfigKey: "app.private", SettingKey: "private",
		},
		{
			Name: "app.authorized_fetch", Want: "true", When: gatedFederating,
			Why:       "the gate leaves ActivityPub to the blog, and without authorized fetch it answers an unsigned read and accepts an unsigned delivery",
			ConfigKey: "app.authorized_fetch", SettingKey: "authorized_fetch",
		},
	},
	LiveSettings: writefreelyLive{},
	// Federation can be turned off in its settings.
	Federates:         true,
	FederationSetting: "federation",
	HostnameRoles:     []string{MediaRole},
	ObjectStorage:     true,
	// With `[storage] type = s3` the fork writes images to the bucket and
	// never reads them back to serve them. It requires `[storage]
	// image_url_base`, publishes every image URL under it, and answers its
	// own /uploads/ route with a redirect there, so the bucket has to answer
	// a reader with no credential or no image on the blog loads at all.
	PublicObjects: true,
	// Under /uploads/, served by uploadsHandler from the local image store
	// (imagestore.go:364-386 at ff9dceb, the commit the pinned image is built
	// from).
	LocalMediaPaths: []string{"/uploads/*"},
	// writefreely-wisp ff9dceb: the callback is app.Host +
	// "/oauth/callback/generic", built in configureGenericOauth
	// (oauth.go:249) and not configurable. Nothing in the fork sends a
	// code challenge, so PKCE is off. WriteFreely reads no groups claim
	// either; the restriction at Pocket ID is the whole control.
	OIDC: &OIDCDef{
		Callback:           callbackPath("/oauth/callback/generic"),
		PKCE:               false,
		MemberGroupSetting: "member_group",
		DefaultAdminGroup:  AdminsGroup,
		DefaultMemberGroup: MembersGroup,
	},
}
