package blueprints

import (
	"strings"
)

// The identity provider groups the toolkit names. MembersGroup and
// AdminsGroup are the two every member facing app's client is restricted to
// when its declaration names none. Both are the toolkit's defaults rather
// than fixed names: an app's settings may replace either, and `validate`
// holds the member group of an app using the gate to the gate's.
// OperatorsGroup is fixed: the operator reconciler checks it by name.
const (
	// MembersGroup is the group a person must be in to sign in to a member
	// facing app, and the one the gate's members instance admits.
	MembersGroup = "members"
	// AdminsGroup is the group whose members an app that maps administrators
	// makes administrators, and that every restricted client admits beside the
	// member group: the community's admins. apply puts every operator in it,
	// so a new deployment has someone to run it.
	AdminsGroup = "admins"
	// OperatorsGroup is the operators: every operator paisans-stack.yaml
	// declares, put there by apply and checked there by the operator
	// reconciler. An infrastructure app whose administrators are the people
	// who run the machines reads it, as the uptime blueprint does by default.
	OperatorsGroup = "operators"
)

// groupSetting reads a group name from settings, taking fallback when the
// key is absent, blank or not a string. A blank name is treated as absent so
// that a typo cannot leave a client unrestricted; validate refuses the typo
// itself (oidc-member-group-not-a-name).
func groupSetting(settings map[string]any, key, fallback string) string {
	if s, ok := settings[key].(string); ok && strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	return fallback
}
