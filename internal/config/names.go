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

// ValidAppName reports whether s is a name an app may have under apps.
func ValidAppName(s string) bool { return blueprintName.MatchString(s) }

// ValidHostname reports whether s is a DNS name: dot-separated labels of
// letters, digits and hyphens, none empty or over 63 characters, at most 253
// in all. It is the one rule for an app's hostname, in the configuration and
// in a request, and the rule ssh.host is held to.
func ValidHostname(s string) bool { return isHostname(s) }
