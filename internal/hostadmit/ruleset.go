package hostadmit

import (
	"net/netip"
	"regexp"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// ownerFirewall reads nft list ruleset and names each part of it that is
// the owner's own firewall, which admit will not put ufw over. Tailscale,
// ufw and Docker each put rules in the same iptables-nft tables as an owner
// would, so it reads chain by chain and rule by rule, not table by table:
//
//   - Admit's own egress tables, inet paisans_<token>, are skipped.
//   - A chain that no hook reaches is read only when a hooked chain jumps or
//     goes to it.
//   - In a table iptables-nft manages (ip or ip6, filter, nat, mangle, raw
//     or security), a chain hooked on input or forward is read rule by rule.
//     A rule that only jumps or goes to one of ufw's chains or Docker's,
//     known by their exact names, is theirs. A jump to a ts-* chain is
//     Tailscale's when every rule in that chain is about Tailscale's
//     interface, port, mark or ranges (tailscaleRule); a jump to any other
//     chain, DOCKER-USER among them, which is where an owner filters
//     Docker's traffic, is followed and that chain read the same way. A
//     return is no one's. Any other rule is the owner's, named
//     "<family> <table> <chain>: <rule>". A drop or reject policy is the
//     owner's, "<family> <table> <chain>: policy drop", unless the chain
//     jumps to ufw's chains, since ufw sets that policy itself, or is
//     FORWARD and jumps to Docker's, since Docker does.
//   - In any other table, a chain hooked on input or forward is the owner's
//     whether or not it holds a rule, named "<family> <table> <chain>".
//   - A chain hooked on ingress, prerouting or output, in any table, is the
//     owner's where it drops or rejects: a drop or reject policy, or a rule
//     that ends in drop or reject, followed through its jumps as above. The
//     drop Docker 29 puts in raw PREROUTING for each published port is
//     Docker's (dockerRawDrop).
//
// Braces inside a quoted string, a comment's, do not open or close a
// block. Each name is passed through ui.Visible, since it quotes the host.
func ownerFirewall(ruleset []string) []string {
	chains := parseRuleset(ruleset)
	var out []string
	for _, c := range chains.order {
		ch := chains.byKey[c]
		if ch.hook == "" || egressTableRe.MatchString(ch.family+" "+ch.table) {
			continue
		}
		incoming := ch.hook == "input" || ch.hook == "forward"
		filtering := incoming || ch.hook == "ingress" || ch.hook == "prerouting" || ch.hook == "output"
		if !filtering {
			continue
		}
		if incoming && !iptablesTable(ch.family, ch.table) {
			out = append(out, ui.Visible(ch.name()))
			continue
		}
		if dropPolicy(ch.policy) && !policyIsOthers(ch) {
			out = append(out, ui.Visible(ch.name()+": policy "+ch.policy))
		}
		out = append(out, chains.judge(ch, incoming, map[string]bool{ch.key(): true})...)
	}
	return out
}

// chain is one chain of the ruleset.
type chain struct {
	family, table, chain string
	// hook is the hook it is attached to, "" for a chain only a jump reaches;
	// policy its policy, "" for none.
	hook, policy string
	rules        []string
}

func (c *chain) key() string  { return c.family + " " + c.table + " " + c.chain }
func (c *chain) name() string { return c.key() }

type ruleset struct {
	byKey map[string]*chain
	order []string
}

// The patterns a ruleset is read with.
var (
	// egressTableRe is admit's own egress table, deployment.EgressTable.
	egressTableRe = regexp.MustCompile(`^inet paisans_[0-9a-f]{4}$`)
	hookRe        = regexp.MustCompile(`^type \S+ hook (\S+)`)
	policyRe      = regexp.MustCompile(`\bpolicy (\w+);`)
	counterRe     = regexp.MustCompile(`\s*\bcounter packets \d+ bytes \d+`)
	jumpRe        = regexp.MustCompile(`^(jump|goto) (\S+)$`)
	verdictDropRe = regexp.MustCompile(`\b(drop|reject)\b`)
	// dockerRawDrop is the rule Docker 29 adds to raw PREROUTING for each
	// published port, so nothing outside a bridge reaches a container's
	// address directly; seen on docker:29-dind with a published port.
	dockerRawDrop = regexp.MustCompile(`^ip6? daddr \S+ iifname != "(docker0|br-[0-9a-f]{12})" drop$`)
)

// parseRuleset reads the ruleset's tables, chains, hooks, policies and
// rules. Sets, maps and other blocks are skipped by their braces, counted
// outside quoted strings.
func parseRuleset(lines []string) ruleset {
	rs := ruleset{byKey: map[string]*chain{}}
	var family, table string
	var cur *chain
	depth := 0
	for _, line := range lines {
		s := strings.TrimSpace(line)
		// nft prints iptables-nft's warning on the line it reached, even
		// within a rule.
		if i := strings.Index(s, "# Warning:"); i >= 0 {
			s = strings.TrimSpace(s[:i])
		}
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		bare := unquoted(s)
		switch {
		case depth == 0 && strings.HasPrefix(s, "table "):
			if f := strings.Fields(s); len(f) >= 3 {
				family, table = f[1], f[2]
			}
		case depth == 1 && strings.HasPrefix(s, "chain "):
			if f := strings.Fields(s); len(f) >= 2 {
				cur = &chain{family: family, table: table, chain: f[1]}
				if _, seen := rs.byKey[cur.key()]; !seen {
					rs.order = append(rs.order, cur.key())
				}
				rs.byKey[cur.key()] = cur
			}
		case depth == 2 && cur != nil && bare != "}":
			if m := hookRe.FindStringSubmatch(s); m != nil {
				cur.hook = m[1]
				if p := policyRe.FindStringSubmatch(s); p != nil {
					cur.policy = p[1]
				}
			} else if !strings.HasPrefix(s, "policy ") && !strings.HasPrefix(s, "comment ") && !strings.HasPrefix(s, "type ") {
				if r := strings.TrimSpace(counterRe.ReplaceAllString(s, "")); r != "" {
					cur.rules = append(cur.rules, r)
				}
			}
		}
		depth += strings.Count(bare, "{") - strings.Count(bare, "}")
		if depth < 0 {
			depth = 0
		}
		if depth <= 1 {
			cur = nil
		}
	}
	return rs
}

// unquoted is s with every double-quoted string, and its escapes, removed.
func unquoted(s string) string {
	var b strings.Builder
	in := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case in && c == '\\':
			i++
		case c == '"':
			in = !in
		case !in:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// judge names the owner's rules in ch, following its jumps. incoming is
// whether the hook that reached it is input or forward, where every rule
// filters; elsewhere only a drop or reject does.
func (rs ruleset) judge(ch *chain, incoming bool, seen map[string]bool) []string {
	var out []string
	for _, r := range ch.rules {
		if m := jumpRe.FindStringSubmatch(r); m != nil {
			target := m[2]
			if ufwChain(target) || dockerChains[target] {
				continue
			}
			next, ok := rs.byKey[ch.family+" "+ch.table+" "+target]
			if !ok {
				out = append(out, ui.Visible(ch.name()+": "+r))
				continue
			}
			if seen[next.key()] {
				continue
			}
			seen[next.key()] = true
			if strings.HasPrefix(target, "ts-") {
				out = append(out, rs.judgeTailscale(next)...)
				continue
			}
			out = append(out, rs.judge(next, incoming, seen)...)
			continue
		}
		if r == "return" {
			continue
		}
		if !incoming && (!verdictDropRe.MatchString(r) || ch.chain == "PREROUTING" && ch.table == "raw" && dockerRawDrop.MatchString(r)) {
			continue
		}
		out = append(out, ui.Visible(ch.name()+": "+r))
	}
	return out
}

// judgeTailscale names each rule of a ts-* chain that is not Tailscale's.
func (rs ruleset) judgeTailscale(ch *chain) []string {
	var out []string
	for _, r := range ch.rules {
		if !tailscaleRule(r) {
			out = append(out, ui.Visible(ch.name()+": "+r))
		}
	}
	return out
}

// The shape of Tailscale's rules, from Tailscale 1.102.5's on iptables-nft:
// its interface, its WireGuard port, the mark it sets on what it forwards,
// and its CGNAT and IPv6 ranges.
var (
	tailscaleMarkRe = regexp.MustCompile(`meta mark & 0x00ff0000 == 0x00040000|mark and 0xff00ffff xor 0x40000`)
	addrRe          = regexp.MustCompile(`\b[sd]addr (!= )?(\S+)`)
	tailscaleRanges = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("fd7a:115c:a1e0::/48")}
)

// tailscaleRule reports whether r is about Tailscale alone: its interface,
// its port, its mark, or an address in its ranges.
func tailscaleRule(r string) bool {
	if strings.Contains(r, `"tailscale0"`) || strings.Contains(r, "udp dport 41641 ") || tailscaleMarkRe.MatchString(r) {
		return true
	}
	for _, m := range addrRe.FindAllStringSubmatch(r, -1) {
		p, err := netip.ParsePrefix(m[2])
		if err != nil {
			a, aerr := netip.ParseAddr(m[2])
			if aerr != nil {
				continue
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		for _, ts := range tailscaleRanges {
			if ts.Contains(p.Addr()) && p.Bits() >= ts.Bits() {
				return true
			}
		}
	}
	return false
}

// iptablesTable reports whether family and table are one iptables-nft
// manages.
func iptablesTable(family, table string) bool {
	if family != "ip" && family != "ip6" {
		return false
	}
	switch table {
	case "filter", "nat", "mangle", "raw", "security":
		return true
	}
	return false
}

func dropPolicy(p string) bool { return p == "drop" || p == "reject" }

// policyIsOthers reports whether ch's drop policy is ufw's, which sets one
// on every chain it jumps from, or Docker's, which sets one on FORWARD.
func policyIsOthers(ch *chain) bool {
	for _, r := range ch.rules {
		m := jumpRe.FindStringSubmatch(r)
		if m == nil {
			continue
		}
		if ufwChain(m[2]) || ch.chain == "FORWARD" && dockerChains[m[2]] {
			return true
		}
	}
	return false
}

// dockerChains are the chains Docker 29 writes on iptables-nft, by exact
// name, as docker:29-dind printed them, with the isolation and ingress
// chains earlier releases and swarm write. DOCKER-USER is the owner's to
// fill, so it is not here: a jump to it is followed.
var dockerChains = map[string]bool{
	"DOCKER": true, "DOCKER-FORWARD": true, "DOCKER-BRIDGE": true, "DOCKER-CT": true,
	"DOCKER-INTERNAL": true, "DOCKER-ISOLATION-STAGE-1": true, "DOCKER-ISOLATION-STAGE-2": true,
	"DOCKER-INGRESS": true,
}

// ufwChains are ufw's chains by exact name, as ufw on Ubuntu 24.04 writes
// them for IPv4; IPv6's are the same with ufw6- for ufw-.
var ufwChains = map[string]bool{}

func init() {
	for _, n := range []string{
		"before-logging-input", "before-logging-output", "before-logging-forward",
		"before-input", "before-output", "before-forward",
		"after-input", "after-output", "after-forward",
		"after-logging-input", "after-logging-output", "after-logging-forward",
		"reject-input", "reject-output", "reject-forward",
		"track-input", "track-output", "track-forward",
		"logging-deny", "logging-allow",
		"skip-to-policy-input", "skip-to-policy-output", "skip-to-policy-forward",
		"not-local",
		"user-input", "user-output", "user-forward",
		"user-logging-input", "user-logging-output", "user-logging-forward",
		"user-limit", "user-limit-accept",
	} {
		ufwChains["ufw-"+n] = true
		ufwChains["ufw6-"+n] = true
	}
}

func ufwChain(name string) bool { return ufwChains[name] }
