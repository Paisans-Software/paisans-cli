package config

// ValidUserName reports whether s is a login name this toolkit accepts, the
// rule sites.<s>.ssh.user is held to: a lowercase letter or underscore, then
// lowercase letters, digits, underscores or hyphens, at most 32 characters.
func ValidUserName(s string) bool { return unixUser.MatchString(s) }

// ValidOperatorName reports whether s is a name an operator may have under
// operators, the name recorded on a host beside each of their keys.
func ValidOperatorName(s string) bool { return keyName.MatchString(s) }

// ValidAppName reports whether s is a name an app may have under apps.
func ValidAppName(s string) bool { return blueprintName.MatchString(s) }

// ValidHostname reports whether s is a DNS name: dot-separated labels of
// letters, digits and hyphens, none empty or over 63 characters, at most 253
// in all. It is the rule ssh.host is held to.
func ValidHostname(s string) bool { return isHostname(s) }
