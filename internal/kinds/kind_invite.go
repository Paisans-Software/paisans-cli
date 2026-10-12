package kinds

// inviteDef is the invite kind.
var inviteDef = Def{
	Services: []Service{
		// paisans-invite: member invites with the code of conduct before
		// the account (docs/specs/2026-10-12-invite-service.md). Cluster
		// placement only, so the PostgresService row is the cluster's role
		// and database, never a container of its own.
		//
		// 0.1.0 (git tag v0.1.0, without the `v` here) was checked against
		// ghcr.io on 2026-10-12 by requesting the manifest for that exact
		// tag anonymously (amd64 and arm64), and inspected after a pull: it
		// declares no VOLUME.
		{Name: "app", Image: "ghcr.io/paisans-software/paisans-invite:0.1.0", Purpose: "the invite service"},
		{Name: PostgresService, Purpose: "its role and database on the cluster"},
	},
	// Not the service's default 8080: Mbin publishes 8080, and both are
	// clustered, so they share every apps site's mesh address. The service
	// reads its port from INVITE_LISTEN_ADDR, which this toolkit renders, and
	// its image's health check probes the same variable.
	Port:         8082,
	ConfigFormat: ConfigEnv,
	ConfigFile:   ".env",
	// paisans-invite's /healthz answers 200 when its database does.
	Health: Health{"/healthz", "200"},
	// Invitees reach /i/* before they have an account; a member gate would
	// lock them out.
	Gate:      GateSpec{Gateable: false},
	SendsMail: true,
	// paisans-invite's callback is GET /auth/callback with PKCE S256
	// (internal/web/auth.go); it reads the groups claim to tell members
	// from admins.
	OIDC: &OIDCDef{
		Callback:           callbackPath("/auth/callback"),
		PKCE:               true,
		MemberGroupSetting: "member_group",
		AdminGroupSetting:  "admin_group",
		DefaultMemberGroup: MembersGroup,
		DefaultAdminGroup:  AdminsGroup,
		ReadsGroups:        true,
	},
	RequiredSecrets: []RequiredSecret{
		{"email_hash_key", "paisans-invite refuses to start without INVITE_EMAIL_HASH_KEY"},
		{"session_key", "paisans-invite refuses to start without INVITE_SESSION_KEY"},
	},
	// email_hash_key keys the hash of a declined address; session_key signs
	// the sign-in cookie (docs/specs/2026-10-12-invite-service.md).
	AppSecrets:  []string{"email_hash_key", "session_key"},
	SelfRotated: []string{"session_key"},
}
