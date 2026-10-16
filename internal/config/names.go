package config

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// ValidUserName reports whether s is a login name this toolkit accepts, the
// rule sites.<s>.ssh.user is held to: a lowercase letter or underscore, then
// lowercase letters, digits, underscores or hyphens, at most 32 characters.
func ValidUserName(s string) bool { return unixUser.MatchString(s) }

// DeploymentUserProblem says why user cannot be the login user of the
// deployment whose id is id, "" when it can: a name ValidUserName accepts,
// never root, and never another deployment's paisans-<token> user, whose
// authorized_keys host admit would otherwise write into. sites.<s>.ssh.user,
// a request's user and an admission record's user are all held to it.
func DeploymentUserProblem(user, id string) string {
	own := deployment.Deployment{ID: id}.Prefix()
	switch {
	case !ValidUserName(user):
		return fmt.Sprintf("%q is not a user name paisans accepts", user)
	case user == "root":
		return "root is never a deployment's user"
	case strings.HasPrefix(user, "paisans-") && deployment.ValidID(id) && user != own:
		return fmt.Sprintf("%s is another deployment's name; this deployment's own user is %s", user, own)
	}
	return ""
}

// ValidOperatorName reports whether s is a name an operator may have under
// operators, the name recorded on a host beside each of their keys.
func ValidOperatorName(s string) bool { return keyName.MatchString(s) }

// ValidSiteName reports whether s is a name a site may have under sites:
// lowercase letters, digits and hyphens, starting with a letter or digit, at
// most 63. The configuration's sites and a request's site are held to it.
func ValidSiteName(s string) bool { return blueprintName.MatchString(s) }

// ValidAppName reports whether s is a name an app may have under apps.
func ValidAppName(s string) bool { return blueprintName.MatchString(s) }

// ValidHostname reports whether s is a public hostname, HostnameProblem's
// rule: the one rule for an app's hostnames in the configuration, for a
// request's web_server hostnames and for validate's media and mail names.
// Each is written lowercase, its one canonical spelling, so comparing two
// names as strings is comparing them as DNS does. ssh.host names a machine
// to log in to, which may be a single label, and has a rule of its own.
func ValidHostname(s string) bool { return HostnameProblem(s) == "" }

// HostnameProblem says why a name is not a usable public hostname, or returns
// empty when it is one. It is the RFC 1123 shape, written lowercase: labels of
// 1 to 63 letters, digits and hyphens, not starting or ending with a hyphen,
// at least two of them, 253 characters in all.
//
// Lowercase is required rather than folded. Caddy and DNS both ignore case,
// but the name is also written into an application's configuration and from
// there into URLs other servers store, and two spellings of one name are two
// URLs to anything comparing them as strings.
func HostnameProblem(host string) string {
	switch {
	case host == "":
		return "it is empty"
	case len(host) > 253:
		return "it is longer than 253 characters"
	case host != strings.ToLower(host):
		return "it contains capital letters; write it lowercase"
	case strings.Contains(host, "://") || strings.ContainsAny(host, "/:"):
		return "it carries a scheme, a port or a path; give the name alone"
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return "it is a single label, not a name under a domain"
	}
	for _, label := range labels {
		switch {
		case label == "":
			return "it has an empty label"
		case len(label) > 63:
			return fmt.Sprintf("its label %q is longer than 63 characters", label)
		case label[0] == '-' || label[len(label)-1] == '-':
			return fmt.Sprintf("its label %q starts or ends with a hyphen", label)
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return fmt.Sprintf("its label %q contains %q, and only letters, digits and hyphens are allowed", label, r)
			}
		}
	}
	return ""
}
