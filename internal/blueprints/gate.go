package blueprints

import (
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// GateSpec is what a blueprint needs from the gateway's Caddy to sit behind the
// visibility gate. See docs/specs/2026-10-08-visibility-gate.md.
//
// On a gated hostname every request goes to the gate except the classes that
// carry their own authentication, which the app verifies. This record names
// those classes for one blueprint. Every path in it is read off the pinned
// tag's source and cited beside it, because a path missing here breaks sign-in
// or federation, and a path here that should not be opens the app.
type GateSpec struct {
	// Gateable is false for a blueprint whose clients will not follow a
	// redirect to a passkey prompt, or that is part of the sign-in flow itself.
	Gateable bool

	// OpenPaths reach the app with no gate session: the app's own OIDC
	// callback, a native app's OAuth chain, federation discovery, and the
	// static assets those pages load. Caddy path matcher syntax. The
	// blueprint's health route is added to these when it is a dedicated route
	// (see OpenPathsFor), so it is not repeated here.
	OpenPaths []string

	// InboxPaths take an ActivityPub delivery: a POST whose Content-Type is
	// ActivityPub reaches the app on these, and only on these, because a
	// POST changes state before any response could be filtered. The app
	// verifies the delivery's signature.
	InboxPaths []string

	// TokenPaths reach the app with no gate session when the request carries
	// a bearer token, and only then. A blueprint gets one only once its
	// source shows a token that reads content cannot be had without a
	// member's sign-in.
	TokenPaths []string

	// AutoLogin skips the app's own "Log in with" button for someone the gate
	// already let through. An app takes it only with auto_login: true.
	AutoLogin []AutoLoginRule

	// SignInPath is the app's own sign-in page. On a public app with
	// auto_login, a visit there with no gate session goes through the gate's
	// sign-in first and comes back with one, so auto-login can fire: the
	// app's own "Log in" link becomes the community's sign-in. Empty for a
	// blueprint with no auto-login.
	SignInPath string

	// PrivateProbe is a read the app refuses with no token while it is gated,
	// asked of the app directly rather than through the edge, where the gate
	// answers first. It is what notices an image that ignores the setting
	// that makes it refuse. Nil for a blueprint with none.
	PrivateProbe *Probe

	// LeavesOpen is what the gate passes through to the app, so what the
	// app must close itself while gated: activitypub, api_tokens. A gated
	// app whose blueprint lists one and enforces no gate setting is warned
	// about.
	LeavesOpen []string

	// SignedFetch is how a federating blueprint refuses an unsigned ActivityPub
	// read. Nil for a blueprint that does not federate, or that cannot refuse
	// one, in which case it cannot be gated while it federates.
	SignedFetch *SignedFetch
}

// Probe is a path a monitor requests and the status it must answer.
type Probe struct {
	Path   string
	Expect string
}

// AutoLoginRule redirects a request on Paths to the app's OIDC start route,
// Target, when the gate has a session for it and none of AbsentCookies is
// present. AbsentCookies are how the blueprint says "already signed in here".
type AutoLoginRule struct {
	Paths         []string
	Target        string
	AbsentCookies []string
}

// SignedFetch names the settings that make a blueprint refuse an unsigned
// ActivityPub read and read only from instances on its allow list, and the
// probe a monitor uses to prove they are on. Probe is a path the app refuses
// unsigned whatever content exists, with {hostname} standing for the app's
// own, and Expect the status it answers.
type SignedFetch struct {
	Settings []string
	Probe    string
	Expect   string
	// Requires is a boolean setting without which the app cannot refuse an
	// unsigned read even with authorized fetch on, so that set false it has
	// no signed fetch. Absent counts as on. Empty for a blueprint with none.
	Requires string
}

// GateFor returns a blueprint's gate record. A blueprint with none recorded is
// not gateable.
func GateFor(blueprint config.Blueprint) GateSpec { return of(blueprint).Gate }

// GateRecorded reports whether a blueprint has a gate record at all, so a new
// blueprint cannot ship without somebody deciding whether it can be gated.
func GateRecorded(blueprint config.Blueprint) bool {
	_, ok := For(blueprint)
	return ok
}

// OpenPathsFor is a blueprint's open paths with its health route added when
// that route is dedicated. `/` is never added: it is the front page, and
// opening it would ungate the app.
func OpenPathsFor(blueprint config.Blueprint) []string {
	out := append([]string(nil), of(blueprint).Gate.OpenPaths...)
	if h, ok := HealthFor(blueprint); ok && h.Path != "/" {
		out = append(out, h.Path)
	}
	return out
}

// SignedFetchFor is how an app refuses an unsigned ActivityPub read, or nil
// when it does not: when settings.authorized_fetch turns authorized fetch
// off, or when the setting its SignedFetch Requires is false.
func SignedFetchFor(app config.App) *SignedFetch {
	sf := of(app.Blueprint).Gate.SignedFetch
	if sf == nil || !AuthorizedFetch(app) {
		return nil
	}
	if sf.Requires != "" {
		if on, ok := app.Settings[sf.Requires].(bool); ok && !on {
			return nil
		}
	}
	return sf
}

// AuthorizedFetchSetting is the app setting that turns authorized fetch, a
// valid HTTP signature on every ActivityPub read, on or off for the mbin and
// writefreely blueprints. Absent means on. README, "Mbin and WriteFreely lock
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

// Federates reports whether an app answers ActivityPub: whether its blueprint
// does, unless the blueprint's FederationSetting is false.
func Federates(app config.App) bool {
	d := of(app.Blueprint)
	if !d.Federates {
		return false
	}
	if d.FederationSetting != "" {
		if on, ok := app.Settings[d.FederationSetting].(bool); ok {
			return on
		}
	}
	return true
}
