package kinds

// mbinDef is the mbin kind.
var mbinDef = Def{
	Services: []Service{
		// The paisans fork, not upstream: it carries the generic OIDC
		// provider this kind's .env configures, which upstream does not
		// have, and the Doctrine transport decorator the default
		// settings.queue (postgres) needs: on an image without it,
		// MESSENGER_TRANSPORT_DSN=doctrine:// fails every dispatch.
		//
		// stack-1 is the fork's toolkit channel: the latest release a
		// maintainer has promoted as working with what this toolkit renders
		// (docs/specs/2026-10-11-stack-channels.md). The stack-1 contract is
		// this kind's templates: MBIN_LOCKED_SETTINGS, MBIN_AUTHORIZED_FETCH,
		// MBIN_PRIVATE_INSTANCE read from the environment, and the /healthz
		// route the monitor checks. Its first promotion is 1.15.0+paisans,
		// the first release carrying all four, published as the image tag
		// 1.15.0-paisans (linux/amd64 only, so an arm64 apps site cannot run
		// it). The tag moves by design, so RepositoryVolumes records what the
		// image declares.
		{Name: "app", Image: "ghcr.io/paisans-software/mbin:stack-1", Purpose: "the application, and its messenger consumers"},
		// The three sidecars upstream's compose.yaml runs beside it. The
		// messenger consumers are not here: they run the application's own
		// code, so they always take the app's image, and a key that let them
		// diverge would only be a way to run two versions of one schema.
		//
		// Upstream names a floating tag for each; these are the releases
		// those tags resolved to when checked against Docker Hub on
		// 2026-10-06, by requesting the manifest for each tag and comparing
		// digests. 3-management-alpine and 3.13.7-management-alpine returned
		// the same OCI image index, as did trixie and 9.1.2-trixie, and
		// latest and 3.2.0. RabbitMQ 3.13 is the last 3.x series and upstream
		// still runs it; a move to 4.x is a broker upgrade to make
		// deliberately, not a default to drift into.
		{Name: "amqproxy", Image: "docker.io/cloudamqp/amqproxy:3.2.0", Purpose: "the AMQP connection pool in front of the broker"},
		{Name: "rabbitmq", Image: "docker.io/library/rabbitmq:3.13.7-management-alpine", Purpose: "the message broker for federation and background work"},
		{Name: "valkey", Image: "docker.io/valkey/valkey:9.1.2-trixie", Purpose: "the cache"},
		{Name: PostgresService, Purpose: "its own database, when the app is pinned"},
	},
	Port:         8080,
	ConfigFormat: ConfigEnv,
	ConfigFile:   ".env",
	SettingKeys: []SettingKey{
		{Key: "MBIN_AUTHORIZED_FETCH", File: ".env", Setting: "authorized_fetch"},
		{Key: "OAUTH_OIDC_ADMIN_GROUP", File: ".env", Setting: "admin_group"},
		{Key: "OAUTH_OIDC_MEMBER_GROUP", File: ".env", Setting: "member_group"},
		{Key: "S3_BUCKET", File: ".env", Setting: "s3_bucket"},
		{Key: "S3_REGION", File: ".env", Setting: "s3_region"},
	},
	// The paisans fork's status-only route, from v1.15.0+paisans, the first
	// stack-1 release: GET or HEAD, 200 with an empty body when Postgres and
	// the cache answer, an empty 503 when either does not
	// (config/mbin_routes/health.yaml, HealthController.php:27-38). Its own
	// firewall has no security (security.yaml:34-37), so it answers without a
	// session on a private instance too.
	Health: Health{"/healthz", "200"},
	// Cited at mbin v1.14.0+paisans (da8b8c34), the tag the pinned image is
	// built from.
	Gate: GateSpec{
		Gateable: true,
		OpenPaths: []string{
			// OIDC: /oauth/oidc/connect and its /oauth/oidc/verify callback,
			// which the browser reaches mid-login (security.yaml routes
			// 146-154).
			"/oauth/*",
			// The native app's OAuth chain, which runs in a WebView with no
			// gate cookie, ending at the sign-in page (league routes.php:9-14,
			// routes 27-39).
			"/authorize", "/consent", "/token", "/login",
			// Discovery, which a peer needs before it can sign anything:
			// AuthorizedFetchSubscriber::UNGATED_ROUTES
			// (AuthorizedFetchSubscriber.php:52-59, activity_pub.yaml:1-27,
			// 160-164). Webfinger answers JRD, not ActivityPub, so it is not
			// covered by the ActivityPub class.
			"/.well-known/webfinger", "/.well-known/host-meta", "/.well-known/nodeinfo",
			"/nodeinfo/*", "/i/actor", "/contexts", "/contexts.jsonld",
			// What the sign-in page loads (webpack.config.js:12,19,
			// base.html.twig:48-59, assets/app.js:9).
			"/build/*", "/assets/*", "/favicon.ico", "/favicon.svg", "/mbin_logo.svg",
			"/manifest.json", "/sw.js", "/custom-style",
			// What a native app calls before it holds a token: it registers
			// its own client, then reads what the instance is. Mbin answers
			// these to anyone, private instance or not (security.yaml:143-146
			// at v1.15.0+paisans). Exact paths, not prefixes: the rest of
			// /api/instance/* stays behind the gate.
			//
			// Registering a client grants nothing by itself. A public client
			// still needs /authorize, which needs a member's sign-in. A
			// confidential client_credentials client comes with a bot user
			// (CreateClientApi.php:122-138), created unverified (User.php:188),
			// and the API firewall's user checker refuses an unverified user
			// (security.yaml:27, UserChecker.php:55), so its token reads
			// nothing unless an admin verifies that bot. Anonymous
			// registration is rate limited to two clients per six hours
			// (rate_limiter.yaml:9-12).
			"/api/client", "/api/info", "/api/instance",
		},
		// The four routes InboxSignatureSubscriber verifies before dispatch
		// (InboxSignatureSubscriber.php:51-56, activity_pub.yaml:35-94).
		InboxPaths: []string{"/i/inbox", "/f/inbox", "/u/*/inbox", "/m/*/inbox"},
		// A native app reads and writes through /api/* with the token its
		// WebView sign-in earned, and holds no gate session. Safe only
		// because a gated Mbin runs as a private instance
		// (MBIN_PRIVATE_INSTANCE, locked, rendered for every gated Mbin and
		// tested in render's mbin_env_test.go): Mbin's catch-all access rule
		// then refuses any request without a signed in user
		// (security.yaml:152), and /api sits on a stateless OAuth2 firewall
		// that refuses a token it did not issue (security.yaml:22-27). A
		// public Mbin answers /api/* reads with no user at all, so without
		// that mode this path would rest on the firewall alone.
		TokenPaths: []string{"/api/*"},
		AutoLogin: []AutoLoginRule{{
			Paths: []string{"/", "/login"},
			// OidcController::connect takes no destination
			// (OidcController.php:14-20).
			Target: "/oauth/oidc/connect",
			// The session and remember-me cookies, PHP's and Symfony's
			// defaults (framework.yaml:26-31, security.yaml:59-66).
			AbsentCookies: []string{"PHPSESSID", "REMEMBERME"},
		}},
		// The sign-in page Symfony sends an unauthenticated visitor to
		// (security.yaml:90 at v1.15.0+paisans).
		SignInPath: "/login",
		// MBIN_AUTHORIZED_FETCH, on unless settings.authorized_fetch turns it
		// off, and MBIN_USE_FEDERATION_ALLOW_LIST when gated, both named in
		// MBIN_LOCKED_SETTINGS, so an admin cannot turn either off. The
		// subscriber gates every ap_* GET route except the instance actor
		// and discovery (AuthorizedFetchSubscriber.php:52-59,83-90 at
		// v1.15.0+paisans). `/` asking for ActivityPub is ap_instance_front
		// (activity_pub.yaml:29), which needs no content and is not in that
		// list, so it answers 401 unsigned (AuthorizedFetchSubscriber.php:
		// 148-153). The monitor proves it all the same: it is what shows the
		// lock holds.
		// A gated Mbin runs as a private instance, so its API refuses a read
		// with no token (see MbinPrivateAPIProbe).
		PrivateProbe: &Probe{Path: MbinPrivateAPIProbe, Expect: "401"},
		SignedFetch: &SignedFetch{
			Settings: []string{"MBIN_AUTHORIZED_FETCH", "MBIN_USE_FEDERATION_ALLOW_LIST"},
			Probe:    "/",
			Expect:   "401",
		},
	},
	// Each is rendered into its .env and named in MBIN_LOCKED_SETTINGS, which
	// the paisans fork reads from the environment on every read and never
	// from its database (SettingsManager.php:104 at v1.15.1+paisans), so the
	// container's environment is what Mbin enforces.
	GateSettings: []GateSetting{
		{
			Name: "MBIN_PRIVATE_INSTANCE", Want: "true", When: gated,
			Why:       "the gate lets a request with a bearer token reach /api/*, and without private instance mode Mbin answers its API reads with no user at all",
			ConfigKey: "MBIN_PRIVATE_INSTANCE",
		},
		{
			Name: "MBIN_USE_FEDERATION_ALLOW_LIST", Want: "true", When: gated,
			Why:       "the gate leaves ActivityPub to Mbin, and without the allow list Mbin answers a signed read from any instance",
			ConfigKey: "MBIN_USE_FEDERATION_ALLOW_LIST",
		},
		{
			Name: "MBIN_AUTHORIZED_FETCH", Want: "true", When: gatedFederating,
			Why:       "the gate leaves ActivityPub to Mbin, and without authorized fetch Mbin answers an unsigned read",
			ConfigKey: "MBIN_AUTHORIZED_FETCH", SettingKey: "authorized_fetch",
		},
	},
	LiveSettings: mbinLive{},
	// Mbin always federates.
	Federates: true,
	// Every kind that stores objects serves them on a hostname of its own.
	// The set has to agree with UsesObjectStorage, and a test holds it to
	// that: a kind that stores objects without this role would publish URLs
	// nothing routes, and a kind with the role and no bucket would route a
	// hostname to nothing.
	HostnameRoles: []string{MediaRole},
	ObjectStorage: true,
	// A federating server fetching an image is a machine with no account
	// here, and a remote instance caches the URL it was given.
	PublicObjects: true,
	// Under /media, served as static files by the web server in its own
	// container (docs/02-admin/01-installation/02-docker.md, "Uploaded media
	// files", at the paisans fork's tag v1.14.0+paisans), and the federated
	// copy of a post points a remote instance there.
	LocalMediaPaths: []string{"/media/*"},
	// Its contact form and the few account mails it still sends, as
	// MAILER_DSN and its sender address (see MbinContactEmailSetting).
	SendsMail:     true,
	DashboardPath: MbinDashboardPath,
	// The paisans fork's generic OIDC provider, read at tag
	// v1.13.3+paisans: the callback is MbinRedirectURI; PKCE is on because
	// the fork's OidcClient extends KnpU's OAuth2PKCEClient and always
	// sends a code challenge (src/Security/Oidc/OidcClient.php:18); and
	// OAUTH_OIDC_ADMIN_GROUP and OAUTH_OIDC_MEMBER_GROUP, which the .env
	// template renders from these two settings, name the groups it reads
	// from the `groups` claim (.env.example_docker:243-273). The groups
	// scope is requested only when one of them is set
	// (src/Provider/Oidc.php:45-46), which is why both have defaults: an
	// empty pair would leave the client unrestricted and the claim
	// unread.
	OIDC: &OIDCDef{
		Callback:           func(hostname, _ string) string { return MbinRedirectURI(hostname) },
		PKCE:               true,
		AdminGroupSetting:  "admin_group",
		MemberGroupSetting: "member_group",
		DefaultAdminGroup:  AdminsGroup,
		DefaultMemberGroup: MembersGroup,
		ReadsGroups:        true,
	},
	// The broker, the cache and the Mercure hub each need one, and so do
	// Symfony (APP_SECRET) and Mbin's own OAuth2 server for API clients
	// (OAUTH_PASSPHRASE for its private key, OAUTH_ENCRYPTION_KEY for the
	// tokens it issues). Upstream's .env.example_docker ships a placeholder
	// for each of the last three, and the image bakes those placeholders in,
	// so leaving one unset runs on a value anyone can read in upstream's
	// repository. The OAuth key pair is derived from the passphrase rather
	// than generated on its own.
	AppSecrets: []string{
		"mercure_jwt_secret", "rabbitmq_password", "valkey_password",
		"app_secret", "oauth_passphrase", "oauth_encryption_key",
		"oauth_private_key", "oauth_public_key",
	},
	SelfRotated: []string{"app_secret", "mercure_jwt_secret", "valkey_password"},
}
