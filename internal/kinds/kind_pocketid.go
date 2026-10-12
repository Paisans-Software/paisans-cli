package kinds

// pocketIDDef is the pocket-id kind.
var pocketIDDef = Def{
	Services: []Service{
		{Name: "app", Image: "ghcr.io/pocket-id/pocket-id:v2.14.0", Purpose: "the application"},
		// Built from cmd/admin-reconciler by .github/workflows/admin-reconciler-image.yml
		// and published by the tag admin-reconciler-v0.1.0. Checked against
		// ghcr.io on 2026-10-08 with an anonymous pull token, which resolved
		// the tag to this digest: an OCI image index for linux/amd64 and
		// linux/arm64.
		{Name: "reconciler", Image: "ghcr.io/paisans-software/admin-reconciler:0.1.0@sha256:008ca36e1637062bc1bde592b3a3997d9e0642e9fabed6264ce473be6ed6574d", Purpose: "the admin reconciler"},
		{Name: PostgresService, Purpose: "its own database, when the app is pinned"},
	},
	Port:         1411,
	ConfigFormat: ConfigEnv,
	ConfigFile:   ".env",
	SettingKeys:  pocketIDSettingKeys(),
	// 204 No Content, not 200 (backend/internal/controller/
	// healthz_controller.go:18,30-31 at v2.14.0); ran the image.
	Health: Health{"/healthz", "204"},
	// The sign-in flow itself: gating it would gate the login.
	Gate: GateSpec{Gateable: false},
	// Its whole application configuration is rendered (see
	// PocketIDSettings), its mail settings among it.
	SendsMail: true,
	RequiredSecrets: []RequiredSecret{
		// env_config.go:167-169 at tag v2.14.0.
		{"encryption_key", "Pocket ID refuses to start without an ENCRYPTION_KEY of at least 16 bytes"},
		// Not needed to start, but without it the toolkit cannot create an
		// administrator or a client, and Pocket ID deletes the synthetic
		// user an earlier key created (apikey/service.go:31-36).
		{"static_api_key", "it is the only credential `paisans app admin create`, `paisans oidc client create` and apply's client step can reach Pocket ID's API with"},
	},
	// Pocket ID will not start without an ENCRYPTION_KEY of at least 16 bytes
	// (backend/internal/common/env_config.go:167-169 at tag v2.14.0). A
	// generated password is 32 random bytes, base64 encoded. It encrypts
	// stored secrets, so like every generated secret it is never replaced
	// once set.
	//
	// static_api_key is how this toolkit administers Pocket ID without a
	// browser: STATIC_API_KEY authenticates the X-API-Key header as a
	// synthetic administrator (apikey/service.go:156-163 and :221-259,
	// middleware/api_key_auth.go:38) and must be at least 16 characters
	// (env_config.go:182-184). README, "The toolkit administers Pocket ID
	// through its static API key", says why it is generated here.
	//
	// admins_heartbeat_token is what every instance of the app pushes the
	// admins group's health to each monitor with, and what each monitor's
	// seed names for the app (internal/render/adminsheartbeat.go). One per
	// app rather than per site, so that the one monitor follows whichever
	// instance is active.
	AppSecrets:  []string{"encryption_key", "static_api_key", "admins_heartbeat_token"},
	SelfRotated: []string{"static_api_key"},
}

// pocketIDSettingKeys is FILE_BACKEND, owned by file_backend, and every
// setting in PocketIDSettings, each owning its variable. They are taken from
// that table rather than listed again, so a setting added there is an owner
// without a second edit. signup_default_groups is not among them: its
// variable carries recorded IDs, which the setting chooses only by way of
// apply. validate refuses a passthrough key for it, and for every other
// variable a setting renders, whether or not the setting is declared
// (pocket-id-config-key-has-a-setting).
func pocketIDSettingKeys() []SettingKey {
	out := []SettingKey{{Key: "FILE_BACKEND", File: ".env", Setting: "file_backend"}}
	for _, s := range PocketIDSettings {
		out = append(out, SettingKey{Key: s.Env, File: ".env", Setting: s.Key})
	}
	return out
}
