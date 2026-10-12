package kinds

// synapseDef is the synapse kind.
var synapseDef = Def{
	Services: []Service{
		// Three containers, because a homeserver does not authenticate anyone
		// any more. Synapse is a resource server, Matrix Authentication
		// Service owns the login, and the identity provider is upstream of
		// MAS rather than of Synapse.
		//
		// NOTE THE MISSING `v`. Synapse tags as vX.Y.Z and MAS tags as X.Y.Z.
		// Checked against ghcr.io on 2026-09-15 by requesting the manifest for
		// each: `1.24.0` resolved (200), `v1.24.0` did not (404), and neither
		// did `1.25.0`. The registry's tag list was paged through in full and
		// 1.24.0 is the newest release; 1.25.0-rc.0 exists but is a release
		// candidate.
		{Name: "app", Image: "ghcr.io/element-hq/synapse:v1.160.0", Purpose: "the homeserver, which only serves the API"},
		{Name: "mas", Image: "ghcr.io/element-hq/matrix-authentication-service:1.24.0", Purpose: "Matrix Authentication Service, which owns the login"},
		{Name: PostgresService, Purpose: "its own database, when the app is pinned"},
	},
	Port: 8008,
	// It also renders a `.env`, but only for its Postgres container's
	// password. The application reads the file below, so that is where a
	// passthrough key means anything.
	ConfigFormat: ConfigYAML,
	ConfigFile:   "homeserver.yaml",
	// Synapse's header says the file "deliberately declares no identity
	// provider of its own and no local password database". These six are
	// Synapse's blocks for those: oidc_providers, oidc_config (its deprecated
	// single provider form), saml2_config, cas_config and jwt_config each
	// describe a way to register or log in, and password_config the local
	// password logins. Read in docs/usage/configuration/config_documentation.md
	// at tag v1.160.0, the version the kind pins, on 2026-10-04.
	OmittedKeys: []OmittedKey{
		{Key: "cas_config", Why: synapseIdentity},
		{Key: "jwt_config", Why: synapseIdentity},
		{Key: "oidc_config", Why: synapseIdentity},
		{Key: "oidc_providers", Why: synapseIdentity},
		{Key: "password_config", Why: synapseIdentity},
		{Key: "saml2_config", Why: synapseIdentity},
	},
	// Not /health, although Synapse has one (synapse/rest/health.py:38-50 at
	// v1.160.0): the gateway sends only /_matrix/* and /_synapse/* to the
	// homeserver and everything else to MAS, whose listener has no health
	// resource, so the public check would ask the wrong service. The client
	// versions endpoint answers without a token
	// (synapse/rest/client/versions.py:40,50-60) on both routes.
	Health: Health{"/_matrix/client/versions", "200"},
	// A Matrix client is not a browser and will not follow a redirect to a
	// passkey prompt, and neither will a federating server.
	Gate: GateSpec{Gateable: false},
	// A homeserver answers its API on the primary name and its delegation
	// documents on whatever name appears in user identifiers, which is usually
	// the apex. Routing them identically would publish the whole API on the
	// apex as well.
	HostnameRoles: []string{WellknownRole},
	// Synapse's S3 storage provider supplements its media store rather than
	// replacing it, so a local media directory is always required. Its media
	// store is a directory the homeserver owns, which is also why the kind is
	// pinned to one node.
	LocalFilesOnly: true,
	// The relying party is the homeserver's authentication service, MAS
	// at tag v1.24.0, so the callback is its upstream provider's,
	// MASRedirectURI (crates/router/src/endpoints.rs:742-762). PKCE is on:
	// mas.yaml sets pkce_method: always, the S256 mode
	// (crates/config/src/sections/upstream_oauth2.rs:420-436,
	// crates/cli/src/sync.rs:289-298), whose methods are S256 alone
	// (crates/handlers/src/upstream_oauth2/cache.rs:137), so every sign
	// in sends an S256 challenge
	// (crates/oidc-client/src/requests/authorization_code.rs:242-256).
	// mas.yaml requests no groups scope and imports no groups claim, so
	// the restriction at Pocket ID is the whole control. No launch URL:
	// the homeserver's hostname serves no page for a member, and a Matrix
	// client is where they go.
	OIDC: &OIDCDef{
		Callback:           MASRedirectURI,
		NoLaunch:           true,
		PKCE:               true,
		MemberGroupSetting: "member_group",
		DefaultAdminGroup:  AdminsGroup,
		DefaultMemberGroup: MembersGroup,
	},
	// Three, because the homeserver no longer authenticates anyone and Matrix
	// Authentication Service in front of it needs its own.
	AppSecrets:  []string{"mas_encryption_secret", "mas_matrix_secret", "mas_signing_key"},
	SelfRotated: []string{"mas_signing_key", "mas_matrix_secret"},
}

// synapseIdentity is why each of Synapse's identity blocks is left out.
const synapseIdentity = "this homeserver is a resource server and Matrix Authentication Service owns every login, so an identity provider or a password database of its own would be a second way in that bypasses the group restriction at the identity provider"
