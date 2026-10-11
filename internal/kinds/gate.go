package kinds

import (
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// GateSpec is what a kind needs from the gateway's Caddy to sit behind the
// visibility gate. See docs/specs/2026-10-08-visibility-gate.md.
//
// On a gated hostname every request goes to the gate except the classes that
// carry their own authentication, which the app verifies. This record names
// those classes for one kind. Every path in it is read off the pinned tag's
// source and cited beside it, because a path missing here breaks sign-in or
// federation, and a path here that should not be opens the app.
type GateSpec struct {
	// Gateable is false for a kind whose clients will not follow a redirect
	// to a passkey prompt, or that is part of the sign-in flow itself.
	Gateable bool

	// OpenPaths reach the app with no gate session: the app's own OIDC
	// callback, a native app's OAuth chain, federation discovery, and the
	// static assets those pages load. Caddy path matcher syntax. The kind's
	// health route is added to these when it is a dedicated route (see
	// OpenPathsFor), so it is not repeated here.
	OpenPaths []string

	// InboxPaths take an ActivityPub delivery: a POST whose Content-Type is
	// ActivityPub reaches the app on these, and only on these, because a
	// POST changes state before any response could be filtered. The app
	// verifies the delivery's signature.
	InboxPaths []string

	// TokenPaths reach the app with no gate session when the request carries
	// a bearer token, and only then. A kind gets one only once its
	// source shows a token that reads content cannot be had without a
	// member's sign-in.
	TokenPaths []string

	// AutoLogin skips the app's own "Log in with" button for someone the gate
	// already let through.
	AutoLogin []AutoLoginRule

	// SignedFetch is how a federating kind refuses an unsigned ActivityPub
	// read. Nil for a kind that does not federate, or that cannot refuse one,
	// in which case it cannot be gated while it federates.
	SignedFetch *SignedFetch
}

// AutoLoginRule redirects a request on Paths to the app's OIDC start route,
// Target, when the gate has a session for it and none of AbsentCookies is
// present. AbsentCookies are how the kind says "already signed in here".
type AutoLoginRule struct {
	Paths         []string
	Target        string
	AbsentCookies []string
}

// SignedFetch names the settings that make a kind refuse an unsigned
// ActivityPub read and read only from instances on its allow list, and the
// probe a monitor uses to prove they are on. Probe is a path the app refuses
// unsigned whatever content exists, with {hostname} standing for the app's
// own, and Expect the status it answers.
type SignedFetch struct {
	Settings []string
	Probe    string
	Expect   string
}

var gates = map[config.Kind]GateSpec{
	// Cited at mbin v1.14.0+paisans (da8b8c34), the tag the pinned image is
	// built from.
	config.KindMbin: {
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
		},
		// The four routes InboxSignatureSubscriber verifies before dispatch
		// (InboxSignatureSubscriber.php:51-56, activity_pub.yaml:35-94).
		InboxPaths: []string{"/i/inbox", "/f/inbox", "/u/*/inbox", "/m/*/inbox"},
		// Empty: /api/* read endpoints answer anonymous callers
		// (EntriesRetrieveApi.php:72-80, security.yaml:148), and anonymous
		// POST /api/client mints a client (CreateClientApi.php:95,122-139).
		AutoLogin: []AutoLoginRule{{
			Paths: []string{"/", "/login"},
			// OidcController::connect takes no destination
			// (OidcController.php:14-20).
			Target: "/oauth/oidc/connect",
			// The session and remember-me cookies, PHP's and Symfony's
			// defaults (framework.yaml:26-31, security.yaml:59-66).
			AbsentCookies: []string{"PHPSESSID", "REMEMBERME"},
		}},
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
		SignedFetch: &SignedFetch{
			Settings: []string{"MBIN_AUTHORIZED_FETCH", "MBIN_USE_FEDERATION_ALLOW_LIST"},
			Probe:    "/",
			Expect:   "401",
		},
	},
	// Cited at writefreely-wisp ff9dceb, except signed fetch, which is cited
	// at v0.21.1+wisp, the first stack-1 release.
	config.KindWriteFreely: {
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
		},
	},
	// Cited at outline v1.10.0 (65be53c3).
	config.KindOutline: {
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
	config.KindElement: {Gateable: true},
	// A Matrix client is not a browser and will not follow a redirect to a
	// passkey prompt, and neither will a federating server.
	config.KindSynapse: {Gateable: false},
	// The sign-in flow itself: gating it would gate the login.
	config.KindPocketID:    {Gateable: false},
	config.KindOAuth2Proxy: {Gateable: false},
	// The monitor is what an admin opens when the sites it watches are down.
	// The gate signs in at the identity provider on those sites, so a gated
	// monitor is unreadable at exactly the moment it exists for, and its
	// break glass password sign-in (README, "Sign in", under the uptime
	// kind) would sit behind the sign-in it is the fallback for. Its own
	// OIDC client admits only settings.admin_group, so the gate would add no
	// access control, only a dependency.
	config.KindUptime: {Gateable: false},
}

// GateFor returns a kind's gate record. A kind with none recorded is not
// gateable.
func GateFor(kind config.Kind) GateSpec { return gates[kind] }

// GateRecorded reports whether a kind has a gate record at all, so a new kind
// cannot ship without somebody deciding whether it can be gated.
func GateRecorded(kind config.Kind) bool {
	_, ok := gates[kind]
	return ok
}

// OpenPathsFor is a kind's open paths with its health route added when that
// route is dedicated. `/` is never added: it is the front page, and opening
// it would ungate the app.
func OpenPathsFor(kind config.Kind) []string {
	out := append([]string(nil), gates[kind].OpenPaths...)
	if h, ok := HealthFor(kind); ok && h.Path != "/" {
		out = append(out, h.Path)
	}
	return out
}

// SignedFetchFor is how an app refuses an unsigned ActivityPub read, or nil
// when it does not: when settings.authorized_fetch turns authorized fetch
// off, or, for WriteFreely, when private mode is off, since without it the
// blog has no allowlist and admits a signature from any instance.
func SignedFetchFor(app config.App) *SignedFetch {
	sf := gates[app.Kind].SignedFetch
	if sf == nil || !AuthorizedFetch(app) {
		return nil
	}
	if app.Kind == config.KindWriteFreely {
		if private, ok := app.Settings["private"].(bool); ok && !private {
			return nil
		}
	}
	return sf
}

// AuthorizedFetchSetting is the app setting that turns authorized fetch, a
// valid HTTP signature on every ActivityPub read, on or off for the mbin and
// writefreely kinds. Absent means on. README, "Mbin and WriteFreely lock
// what the toolkit renders", has the reasoning.
const AuthorizedFetchSetting = "authorized_fetch"

// AuthorizedFetch reports whether an app's settings leave authorized fetch
// on: true unless settings.authorized_fetch is false. A value that is not a
// boolean reads as on here, and validate refuses it by name.
func AuthorizedFetch(app config.App) bool {
	if on, ok := app.Settings[AuthorizedFetchSetting].(bool); ok {
		return on
	}
	return true
}

// SignedFetchProbe is the path a monitor requests, unsigned, to prove an app
// still refuses an unsigned read.
func (s SignedFetch) SignedFetchProbe(hostname string) string {
	return strings.ReplaceAll(s.Probe, "{hostname}", hostname)
}

// Federates reports whether an app answers ActivityPub. WriteFreely can have
// federation turned off in its settings; Mbin always federates.
func Federates(app config.App) bool {
	switch app.Kind {
	case config.KindMbin:
		return true
	case config.KindWriteFreely:
		if on, ok := app.Settings["federation"].(bool); ok {
			return on
		}
		return true
	}
	return false
}
