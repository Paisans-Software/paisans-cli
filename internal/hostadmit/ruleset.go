package hostadmit

import (
	"regexp"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// ownerFirewall reads nft list ruleset and names each part of it that is
// the owner's own firewall, which admit will not put ufw over. It reads
// chain by chain, not table by table, because iptables-nft puts everyone's
// rules in the same ip filter table:
//
//   - A chain that no input or forward hook reaches filters nothing
//     incoming by itself, and is skipped.
//   - A hooked chain named ts-* is Tailscale's, which filters only its own
//     interface and port, and is skipped.
//   - In iptables-nft's own hooked chains, INPUT and FORWARD of the ip and
//     ip6 filter tables, a rule that only jumps to a ts-* (Tailscale),
//     ufw-* or ufw6-* (ufw), or DOCKER* (Docker) chain is theirs; any other
//     rule is the owner's, named "<family> <table> <chain>: <rule>".
//   - Any other chain an input or forward hook reaches is in a table the
//     owner made, and is the owner's whether or not it holds a rule, named
//     "<family> <table> <chain>".
//
// Each name is passed through ui.Visible, since it quotes the host.
func ownerFirewall(ruleset []string) []string {
	var out []string
	var family, table, chain string
	var hooked bool
	var rules []string
	inChain := false
	depth := 0
	for _, line := range ruleset {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		switch {
		case depth == 0 && strings.HasPrefix(s, "table "):
			f := strings.Fields(s)
			if len(f) >= 3 {
				family, table = f[1], f[2]
			}
		case depth == 1 && strings.HasPrefix(s, "chain "):
			f := strings.Fields(s)
			if len(f) >= 2 {
				chain, hooked, rules, inChain = f[1], false, nil, true
			}
		case depth == 2 && inChain:
			if hookRe.MatchString(s) {
				hooked = true
			} else if !strings.HasPrefix(s, "type ") && !strings.HasPrefix(s, "policy ") && !strings.HasPrefix(s, "comment ") && s != "}" {
				rules = append(rules, s)
			}
		}
		depth += strings.Count(s, "{") - strings.Count(s, "}")
		if depth < 0 {
			depth = 0
		}
		if inChain && depth <= 1 {
			inChain = false
			if hooked {
				out = append(out, judgeChain(family, table, chain, rules)...)
			}
		}
	}
	return out
}

// hookRe is the line that hooks a chain into incoming or forwarded traffic.
var hookRe = regexp.MustCompile(`^type \S+ hook (input|forward)\b`)

// othersJump is a rule that only jumps to, or goes to, a chain of
// Tailscale's, ufw's or Docker's, with or without its counter.
var othersJump = regexp.MustCompile(`^(counter packets \d+ bytes \d+ )?(jump|goto) (ts-|ufw-|ufw6-|DOCKER)\S*$`)

// counterRe is a rule's counter, left out of a rule's name.
var counterRe = regexp.MustCompile(`\s*\bcounter packets \d+ bytes \d+`)

// judgeChain names what in one hooked chain is the owner's.
func judgeChain(family, table, chain string, rules []string) []string {
	if strings.HasPrefix(chain, "ts-") {
		return nil
	}
	name := family + " " + table + " " + chain
	if (family == "ip" || family == "ip6") && table == "filter" && (chain == "INPUT" || chain == "FORWARD") {
		var out []string
		for _, r := range rules {
			if !othersJump.MatchString(r) {
				out = append(out, ui.Visible(name+": "+strings.TrimSpace(counterRe.ReplaceAllString(r, ""))))
			}
		}
		return out
	}
	return []string{ui.Visible(name)}
}
