package kinds

// uptimeDef is the uptime kind.
var uptimeDef = Def{
	Services: []Service{
		// The Paisans-Software/uptime fork (josephquigley/uptime until
		// 2026-10-07): OIDC sign in gated on a group, a seed file
		// (SEED_FILE), TRUST_PROXY and an entrypoint that drops from root
		// from 1.1.0-oidc.2; bulk channel actions and the SMTP lock when the
		// seed supplies SMTP from 1.1.0-oidc.3, the first release published
		// from the organisation; a seed entry's heartbeat_token taken as the
		// monitor's token from 1.1.0-oidc.4, which the per site heartbeat
		// in internal/render/heartbeat.go depends on. Its database is SQLite
		// under /data, which is why the kind is pinned only: it has no
		// Postgres service, so cluster-placement-without-a-cluster refuses
		// it.
		//
		// NOTE THE MISSING `v`: the git tag is v1.1.0-oidc.4 and
		// docker/metadata-action's semver pattern strips it. 1.1.0-oidc.3
		// was checked against ghcr.io on 2026-10-07 by requesting the
		// manifest for that exact tag anonymously (200) and run as root with
		// a seed carrying SMTP (healthy, seed applied); 1.1.0-oidc.4 is the
		// release carrying the heartbeat token, pinned ahead of its
		// publication, and is to be checked the same way once published.
		{Name: "app", Image: "ghcr.io/paisans-software/uptime:1.1.0-oidc.4", Purpose: "the monitor"},
	},
	// Not the fork's default 3000: Outline publishes 3000, and two apps on
	// one site publish on the same mesh address.
	Port:         3001,
	ConfigFormat: ConfigEnv,
	ConfigFile:   ".env",
	// src/server.js:63 at 1.1.0-oidc.1. Used for one monitor's public check of
	// another on a separate site; a monitor does not watch itself
	// (internal/render/uptime.go).
	Health: Health{"/healthz", "200"},
	// The monitor is what an admin opens when the sites it watches are down.
	// The gate signs in at the identity provider on those sites, so a gated
	// monitor is unreadable at exactly the moment it exists for, and its
	// break glass password sign-in (README, "Sign in", under the uptime
	// kind) would sit behind the sign-in it is the fallback for. Its own
	// OIDC client admits only settings.admin_group, so the gate would add no
	// access control, only a dependency.
	Gate: GateSpec{Gateable: false},
	// The fork keeps everything, its SQLite database included, under /data.
	LocalFilesOnly: true,
	SendsMail:      true,
	// The Paisans-Software/uptime fork at 1.1.0-oidc.2: the callback is
	// /login/oidc/callback (src/lib/oidc.js:26), and every sign in sends
	// an S256 code challenge (src/lib/oidc.js:96-103), so PKCE is on. Its
	// one group is settings.admin_group, used as both: its members are
	// the monitor's administrators, and nobody else may sign in, so the
	// client is restricted to it and Pocket ID refuses everyone else
	// before the monitor sees them. It has no default: validate requires
	// it (uptime-needs-an-admin-group).
	OIDC: &OIDCDef{
		Callback:           callbackPath("/login/oidc/callback"),
		PKCE:               true,
		AdminGroupSetting:  "admin_group",
		MemberGroupSetting: "admin_group",
		ReadsGroups:        true,
	},
	RequiredSecrets: []RequiredSecret{
		// src/config.js at 1.1.0-oidc.1: ADMIN_PASS falls back to "admin" and
		// SESSION_SECRET to a value invented per boot.
		{"admin_password", "the monitor falls back to the password `admin` when ADMIN_PASS is unset"},
		{"session_secret", "without it every restart signs every admin out"},
	},
	// ADMIN_PASS is the break glass sign in for when the identity provider is
	// down, which is one of the things this app watches; the fork falls back
	// to the literal "admin" when it is unset, so it is generated rather than
	// optional. SESSION_SECRET signs the session cookie; unset, the fork
	// invents one per boot and every restart signs everyone out.
	AppSecrets:  []string{"admin_password", "session_secret"},
	SelfRotated: []string{"session_secret"},
}
