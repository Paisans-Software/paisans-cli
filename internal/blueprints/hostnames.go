package blueprints

import (
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// PrimaryRole is the hostname every app has, held in App.Hostname rather than
// in the Hostnames map.
const PrimaryRole = "primary"

// WellknownRole is the hostname a homeserver's delegation documents are served
// on. It is named here rather than spelled as a literal in three packages
// because it decides more than routing: the name in every user identifier is
// the name the delegation is served on, so it is also what server_name must
// be.
const WellknownRole = "wellknown"

// MediaRole is the hostname an app's stored objects are served on. Every
// blueprint that keeps objects in S3 has one, and unlike any other role it
// exists whether or not it is declared: MediaHostname derives it when the app
// does not name one under `hostnames`.
//
// It is one hostname per app rather than one for the deployment. A hostname
// can be pointed by DNS at a CDN, another provider or another Garage, one app
// at a time, and a path under a shared hostname cannot; and two apps' user
// uploads on two hostnames are two browser origins, so a file one app stored
// cannot script against another's.
const MediaRole = "media"

// JoinRole is the hostname an account app serves people who have no account
// yet on: the invite pages or the registration page. Every account app has
// one, with a derived name: JoinHostname.
const JoinRole = "join"

// MediaSuffix is what a derived media hostname appends to the label of the
// app's own hostname.
const MediaSuffix = "-media"

// HostnameRoles returns the roles a blueprint understands, sorted, always
// including the primary.
func HostnameRoles(blueprint config.Blueprint) []string {
	out := append([]string{PrimaryRole}, of(blueprint).HostnameRoles...)
	sort.Strings(out)
	return out
}

// ExtraRoles is every role beyond the primary that any blueprint understands,
// sorted and without duplicates. A route for one of them lands in a snippet
// named <app>-<role>.caddy, so this is also the set of suffixes an app's
// routing files can carry.
func ExtraRoles() []string {
	seen := map[string]bool{}
	var out []string
	for _, blueprint := range known() {
		for _, role := range of(blueprint).HostnameRoles {
			if !seen[role] {
				seen[role] = true
				out = append(out, role)
			}
		}
	}
	sort.Strings(out)
	return out
}

// HasHostnameRole reports whether a blueprint understands a role.
func HasHostnameRole(blueprint config.Blueprint, role string) bool {
	if role == PrimaryRole {
		return true
	}
	for _, r := range of(blueprint).HostnameRoles {
		if r == role {
			return true
		}
	}
	return false
}

// SnippetFor returns the template filename a role's routing lives in.
func SnippetFor(role string) string {
	if role == PrimaryRole {
		return "caddy.snippet.tmpl"
	}
	return "caddy.snippet." + role + ".tmpl"
}

// MediaHostname is where an app's stored objects are served from, and empty
// for an app that keeps none in object storage: a blueprint that stores none,
// and an app that keeps its files on its own disk (InObjectStorage), which
// serves them on its own hostname instead.
//
// An app that declares `hostnames.media` gets exactly that. Otherwise the name
// is derived as a sibling of the app's own hostname: the first label of that
// hostname, then MediaSuffix, under the community's domain. talk.example.org
// stores its objects at talk-media.example.org.
//
// A sibling and never a child. media.talk.example.org would sit under the
// app's own host, so a cookie the app scopes to its host with a Domain
// attribute would be sent with every media request, and a file served there
// would share that cookie scope. And never a name for the backend, such as
// garage. or s3.: the name is the app's, and what answers it may change.
//
// The derivation is deliberately not validated here. A derived name can still
// be wrong (a label too long once the suffix is added, or one that collides
// with another app's hostname), and internal/validate refuses those by the
// key an operator has to edit, which this package cannot name.
func MediaHostname(cfg *config.Config, app config.App) string {
	if !HasHostnameRole(app.Blueprint, MediaRole) || !InObjectStorage(cfg, app) {
		return ""
	}
	domain := cfg.Community.Domain
	if declared := strings.TrimSpace(app.Hostnames[MediaRole]); declared != "" {
		return declared
	}
	label, _, _ := strings.Cut(app.Hostname, ".")
	if label == "" || domain == "" {
		return ""
	}
	return label + MediaSuffix + "." + domain
}

// MediaHostnameIsDerived reports whether an app's media hostname comes from
// the derivation rather than from `hostnames.media`, so an error message can
// say which one the operator has to change. False for an app with no media
// hostname at all.
func MediaHostnameIsDerived(cfg *config.Config, app config.App) bool {
	return HasHostnameRole(app.Blueprint, MediaRole) && InObjectStorage(cfg, app) && strings.TrimSpace(app.Hostnames[MediaRole]) == ""
}

// JoinHostname is where an account app serves people who have no account
// yet, and empty for any other blueprint. A declared hostnames.join wins;
// otherwise join.<domain>. Every account app takes signups, invite or open,
// so every one has it.
//
// The derived name is the fixed label join under the community's domain.
// One account app exists per identity app, so two cannot derive the same
// name, and internal/validate refuses a clash with another app's hostname by
// the key an operator has to edit.
func JoinHostname(cfg *config.Config, app config.App) string {
	if !HasHostnameRole(app.Blueprint, JoinRole) {
		return ""
	}
	if declared := strings.TrimSpace(app.Hostnames[JoinRole]); declared != "" {
		return declared
	}
	if cfg.Community.Domain == "" {
		return ""
	}
	return JoinRole + "." + cfg.Community.Domain
}

// RoleHostname is one active hostname of an app beyond its primary.
type RoleHostname struct {
	Role, Hostname string
	// Derived is true when the configuration does not name it and the
	// toolkit made it, so a message can say which key to change.
	Derived bool
}

// RoleHostnames is every hostname an app answers on beyond its primary,
// sorted by role: each declared role, the media hostname when the app keeps
// objects in storage, and an account app's join hostname. A role whose
// condition does not hold is left out even when declared.
//
// It is the one place the gateway's routes, dns, and validate's collision
// checks read these names from, so a hostname that does not exist reaches
// none of them, whether or not validate has refused the line that names it.
func RoleHostnames(cfg *config.Config, app config.App) []RoleHostname {
	var out []RoleHostname
	for _, role := range HostnameRoles(app.Blueprint) {
		switch role {
		case PrimaryRole:
			continue
		case MediaRole:
			if h := MediaHostname(cfg, app); h != "" {
				out = append(out, RoleHostname{role, h, MediaHostnameIsDerived(cfg, app)})
			}
		case JoinRole:
			if h := JoinHostname(cfg, app); h != "" {
				out = append(out, RoleHostname{role, h, strings.TrimSpace(app.Hostnames[JoinRole]) == ""})
			}
		default:
			if h := strings.TrimSpace(app.Hostnames[role]); h != "" {
				out = append(out, RoleHostname{role, h, false})
			}
		}
	}
	return out
}
