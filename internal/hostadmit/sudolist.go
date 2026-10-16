package hostadmit

import "strings"

// SudoList is what sudo -l printed for one user: its rules, each entry of
// the Defaults that apply to it, and each runas or command-specific
// Defaults line, as sudo printed them.
type SudoList struct {
	Rules, Defaults, Specific []string
}

// ReadSudoList reads what sudo -n -l printed under LC_ALL=C, as root for a
// user (-U) or as the user itself: a header line per section, each entry
// indented below it, long lines wrapped onto more indented lines. A user
// sudo refuses outright reads as a list with nothing in it. ok is false
// for a line it does not know, or a listing with neither rules nor a
// refusal, so no caller takes an answer it cannot read for one granting
// nothing.
func ReadSudoList(lines []string) (list SudoList, ok bool) {
	section, understood := "", false
	var defaults []string
	for _, l := range lines {
		l = strings.TrimRight(l, "\r")
		indented := strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")
		entry := strings.TrimSpace(l)
		switch {
		case entry == "":
			section = ""
		case !indented && strings.HasPrefix(l, "Matching Defaults entries for "):
			section = "defaults"
		case !indented && strings.HasPrefix(l, "Runas and Command-specific defaults for "):
			section = "specific"
		case !indented && strings.Contains(l, " may run the following commands on "):
			section, understood = "rules", true
		case !indented && strings.Contains(l, " is not allowed to run sudo on "),
			!indented && strings.HasPrefix(l, "Sorry, user ") && strings.Contains(l, " may not run sudo on "):
			section, understood = "", true
		case indented && section == "defaults":
			defaults = append(defaults, entry)
		case indented && section == "specific":
			list.Specific = append(list.Specific, entry)
		case indented && section == "rules":
			list.Rules = append(list.Rules, entry)
		default:
			return SudoList{}, false
		}
	}
	// Defaults wrap after a comma, and a comma inside a value is escaped.
	for _, e := range splitUnescaped(strings.Join(defaults, " "), ',') {
		if e = strings.TrimSpace(e); e != "" {
			list.Defaults = append(list.Defaults, e)
		}
	}
	return list, understood
}

// HelperRule is the one rule host admit grants a deployment's user, as
// sudo -l lists it: the WireGuard helper, as root, with no password.
func HelperRule(helper string) string { return "(root) NOPASSWD: " + helper }

// BeyondHelper is what in l reaches past the helper: every rule but
// HelperRule(helper); every Defaults entry sudoDefaultProblem names, with
// why; and every runas or command-specific Defaults line, which sudo -l
// lists apart from the rule it changes. host admit refuses a user with any
// of them, and doctor fails one.
func (l SudoList) BeyondHelper(helper string) (rules, defaults []string) {
	for _, r := range l.Rules {
		if r != HelperRule(helper) {
			rules = append(rules, r)
		}
	}
	for _, d := range l.Defaults {
		if why := sudoDefaultProblem(d); why != "" {
			defaults = append(defaults, d+": "+why)
		}
	}
	for _, d := range l.Specific {
		defaults = append(defaults, d+": set for one runas user or command alone")
	}
	return rules, defaults
}

// splitUnescaped splits s at each sep that no backslash escapes.
func splitUnescaped(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case sep:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
