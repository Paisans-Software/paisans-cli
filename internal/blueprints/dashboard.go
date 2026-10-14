package blueprints

import (
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// DashboardLinkSetting is the app setting that replaces the blueprint's
// DashboardPath. It is a setting rather than a config key because the toolkit
// reads it and sends it to the identity provider; nothing renders it.
const DashboardLinkSetting = "sso_dashboard_link"

// DashboardPath is the path on an app's own hostname that the identity
// provider's dashboard tile opens, for a blueprint that knows a better one than
// its front page. Every other blueprint gets "/".
func DashboardPath(blueprint config.Blueprint) string {
	if p := of(blueprint).DashboardPath; p != "" {
		return p
	}
	return "/"
}

// ConsentSetting is the app setting that asks Pocket ID to show its consent
// screen on a person's first sign in through the app's client. Absent or
// false, the client skips it: the toolkit made the client for the community's
// own app, so the screen asks a member to approve something already theirs.
const ConsentSetting = "sso_consent"

// genericSettings are the app settings the toolkit itself reads for every
// app with a client at the identity provider, whatever its blueprint. A
// blueprint declares only what its own templates read, so it never lists
// these, and an app setting one is not a typo.
var genericSettings = []string{DashboardLinkSetting, ConsentSetting}

// IsGenericSetting reports whether key is one of the settings the toolkit
// reads for every app, so validate does not call it unknown to a blueprint
// that declares settings of its own.
func IsGenericSetting(key string) bool { return slices.Contains(genericSettings, key) }

// LaunchURL is the address the identity provider's dashboard opens for an
// app: https://<hostname> plus link, or the blueprint's DashboardPath when link
// is empty. A bare "/" gives https://<hostname> with no trailing slash, which
// is what this toolkit sent before it knew any path, so a client made then is
// already right and no update is planned for it.
func LaunchURL(blueprint config.Blueprint, hostname, link string) string {
	if link == "" {
		link = DashboardPath(blueprint)
	}
	if link == "/" {
		return "https://" + hostname
	}
	return "https://" + hostname + link
}

// ToolkitLaunchURLs are the launch URLs this toolkit itself would have set on
// an app's client, by any release, without the operator choosing one: the
// bare https://<hostname>, its only default before a blueprint could name a
// path, and the blueprint's current default. A client holding one of these was
// shaped by the toolkit, not by a person, so `oidc client create` may move it
// to the effective launch URL. Any other value is an operator's and is never
// overwritten.
//
// A value is added here only when a release has actually sent it. Widening
// this list is the one way a toolkit change could overwrite an operator's
// choice, so it is a list of observed defaults, not a pattern.
func ToolkitLaunchURLs(blueprint config.Blueprint, hostname string) []string {
	out := []string{"https://" + hostname}
	if d := LaunchURL(blueprint, hostname, ""); d != out[0] {
		out = append(out, d)
	}
	return out
}

// CheckDashboardLink returns why a sso_dashboard_link value is unusable, or
// nil. It must be a path starting with a single "/". A full URL is refused
// rather than accepted: the host is always the app's own hostname, so a
// second place to spell it is one that can disagree with `hostname`, and a
// tile pointing at another site is not this setting's job.
func CheckDashboardLink(v any) error {
	// A path a tile sends members to, given as the example.
	const example = "/oauth/oidc/connect"
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("is %v, not a string. It is a path on the app's own hostname, as in %s", v, example)
	}
	switch {
	case !strings.HasPrefix(s, "/"):
		return fmt.Errorf("is %q, which is not a path. Give only the path, starting with /, as in %s: the host is always the app's own hostname, so a full URL would be a second place to spell it", s, example)
	case strings.HasPrefix(s, "//"):
		return fmt.Errorf("is %q, which starts with // and so reads as a host rather than a path. Give a path starting with a single /, as in %s", s, example)
	case strings.ContainsAny(s, " \t\r\n"):
		return fmt.Errorf("is %q, which contains whitespace. Give a path with any spaces percent encoded", s)
	}
	return nil
}
