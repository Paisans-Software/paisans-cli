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

// ValidHostname reports whether s holds only what a DNS name holds, the rule
// an app's hostname is held to.
func ValidHostname(s string) bool { return nameChars(s) }
