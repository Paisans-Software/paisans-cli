package hostadmit

import (
	"net/netip"
	"regexp"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// ownerFirewall reads nft list ruleset and names each part of it that is
// the owner's own firewall, which admit will not put ufw over.
//
// It fails closed by construction. Every rule in a chain attached to any
// hook, of any family and priority, and every policy other than accept,
// is the owner's unless it is, exactly, one of the shapes in allowlist:
// what ufw, Docker 29 and Tailscale write, each only in the iptables-nft
// tables they write it in. A jump or goto is followed only when the rule
// that makes it is itself an allowlisted shape marked follow; the chain it
// reaches is then read the same way. Anything else, a conditional jump, a
// verdict map, a line the parser does not know, is the owner's. Admit's
// own egress table, inet paisans_<token>, is admit's where it hooks output,
// which is all it hooks; any other hook in a table of that name is judged.
// A ruleset whose braces or quotes do not balance is not one the parser
// understands, and is named unreadable whole.
//
// A chain hooked on input or forward in a table iptables-nft does not
// manage is the owner's whether or not it holds a rule, since only the
// owner makes one, and is named "<family> <table> <chain>". Elsewhere a
// rule is named "<family> <table> <chain>: <rule>", its counter left out,
// a policy "<family> <table> <chain>: policy <policy>", and a line the
// parser cannot place "unreadable ruleset line: <line>". Each name is
// passed through ui.Visible, since it quotes the host.
func ownerFirewall(lines []string) []string {
	rs := parseRuleset(lines)
	out := rs.unreadable
	for _, key := range rs.order {
		ch := rs.chains[key]
		if ch.hook == "" || ch.hook == "output" && egressTableRe.MatchString(ch.family+" "+ch.table) {
			continue
		}
		if (ch.hook == "input" || ch.hook == "forward") && !iptablesTable(ch.family, ch.table) {
			out = append(out, ui.Visible(ch.key()))
			continue
		}
		if ch.policy != "accept" && !policyAllowed(ch) {
			policy := ch.policy
			if policy == "" {
				policy = "unknown"
			}
			out = append(out, ui.Visible(ch.key()+": policy "+policy))
		}
		out = append(out, rs.judge(ch, map[string]bool{ch.key(): true})...)
	}
	if rs.unbalanced {
		out = append(out, "unreadable ruleset: its braces or quotes do not balance")
	}
	return out
}

// shape is one rule ufw, Docker or Tailscale writes, allowed in the named
// chains of the named iptables-nft tables. Its rule is matched token by
// token against a rule with its counter left out; a token in angle
// brackets is a placeholder, checked by placeholder. follow marks a jump
// or goto whose target is read against the allowlist in turn. inBridge
// marks a shape whose <host4> must be inside a network Docker masquerades
// for the shape's <bridge>, in the same ruleset's ip nat POSTROUTING, so
// only a container's address is Docker's to drop.
//
// Adding a shape needs a real nft list ruleset that shows it, kept in
// testdata beside the three this list was taken from: ufw active on
// Ubuntu 24.04 (ufw-active-nft-ruleset.txt), Docker 29.9.0 with a second
// network and a published port (docker-29-nft-ruleset.txt), and Tailscale
// 1.102.5 on iptables-nft (tailscale-nft-ruleset.txt).
type shape struct {
	families, table, chains string
	rule                    string
	follow, inBridge        bool
}

// allowlist is every rule shape that is not the owner's.
var allowlist = []shape{
	// ufw: its hook chains jump to its own chains, which are ufw's whole.
	{"ip", "filter", "INPUT FORWARD OUTPUT", "jump <ufw4>", false, false},
	{"ip6", "filter", "INPUT FORWARD OUTPUT", "jump <ufw6>", false, false},

	// Docker 29: FORWARD jumps to its chains, DOCKER-USER is the owner's
	// to fill and is read; NAT for published ports and outbound traffic;
	// raw PREROUTING drops what reaches a container's address from outside
	// its bridge, one host at a time.
	{"ip ip6", "filter", "FORWARD", "jump DOCKER-USER", true, false},
	{"ip ip6", "filter", "FORWARD", "jump DOCKER-FORWARD", false, false},
	{"ip ip6", "filter", "DOCKER-USER", "return", false, false},
	{"ip ip6", "nat", "PREROUTING", `xt match "addrtype" jump DOCKER`, false, false},
	{"ip", "nat", "OUTPUT", `ip daddr != 127.0.0.0/8 xt match "addrtype" jump DOCKER`, false, false},
	{"ip6", "nat", "OUTPUT", `ip6 daddr != ::1 xt match "addrtype" jump DOCKER`, false, false},
	{"ip", "nat", "POSTROUTING", `ip saddr <net4> oifname != <bridge> xt target "MASQUERADE"`, false, false},
	{"ip", "raw", "PREROUTING", "ip daddr <host4> iifname != <bridge> drop", false, true},

	// Tailscale: the hook chains jump to its three chains, whose rules are
	// about its interface, its port, its mark and its ranges; it marks
	// connections in mangle.
	{"ip ip6", "filter", "INPUT", "jump ts-input", true, false},
	{"ip ip6", "filter", "FORWARD", "jump ts-forward", true, false},
	{"ip ip6", "nat", "POSTROUTING", "jump ts-postrouting", true, false},
	{"ip", "filter", "ts-input", `ip saddr <ts4> iifname "lo" accept`, false, false},
	{"ip ip6", "filter", "ts-input", `iifname "tailscale0" accept`, false, false},
	{"ip ip6", "filter", "ts-input", "udp dport 41641 accept", false, false},
	{"ip", "filter", "ts-input", `ip saddr 100.115.92.0/23 iifname != "tailscale0" return`, false, false},
	{"ip", "filter", "ts-input", `ip saddr 100.64.0.0/10 iifname != "tailscale0" drop`, false, false},
	{"ip6", "filter", "ts-input", `ip6 saddr <ts6> iifname "lo" accept`, false, false},
	{"ip ip6", "filter", "ts-forward", `iifname "tailscale0" meta mark set mark and 0xff00ffff xor 0x40000`, false, false},
	{"ip ip6", "filter", "ts-forward", "meta mark & 0x00ff0000 == 0x00040000 accept", false, false},
	{"ip", "filter", "ts-forward", `ip saddr 100.64.0.0/10 oifname "tailscale0" drop`, false, false},
	{"ip ip6", "filter", "ts-forward", `oifname "tailscale0" accept`, false, false},
	{"ip", "nat", "ts-postrouting", "meta mark & 0x00ff0000 == 0x00040000 masquerade", false, false},
	{"ip6", "nat", "ts-postrouting", `meta mark & 0x00ff0000 == 0x00040000 xt target "MASQUERADE"`, false, false},
	{"ip ip6", "mangle", "PREROUTING", "ct state related,established ct mark and 0xff0000 != 0x0 meta mark set ct mark and 0xff0000", false, false},
	{"ip ip6", "mangle", "OUTPUT", "ct state new meta mark & 0x00ff0000 != 0x00000000 ct mark set mark and 0xff0000", false, false},
}

// policyAllowed reports whether ch's policy is one ufw or Docker sets:
// drop on iptables-nft's INPUT or FORWARD when ch jumps to ufw's chains,
// which ufw's defaults set, and drop on FORWARD when it jumps to Docker's,
// which Docker sets when it enables forwarding.
func policyAllowed(ch *chain) bool {
	if ch.policy != "drop" || ch.table != "filter" || (ch.family != "ip" && ch.family != "ip6") {
		return false
	}
	for _, r := range ch.rules {
		s, ok := match(ch, r)
		if !ok {
			continue
		}
		if strings.HasPrefix(s.rule, "jump <ufw") && (ch.chain == "INPUT" || ch.chain == "FORWARD") {
			return true
		}
		if s.rule == "jump DOCKER-FORWARD" && ch.chain == "FORWARD" {
			return true
		}
	}
	return false
}

// judge names every rule of ch that is not an allowlisted shape, and reads
// the chain each allowlisted jump marked follow reaches.
func (rs ruleset) judge(ch *chain, seen map[string]bool) []string {
	var out []string
	for _, r := range ch.rules {
		s, ok := match(ch, r)
		if ok && s.inBridge {
			ok = rs.inBridge(strings.Fields(r)[2], strings.Fields(r)[5])
		}
		if !ok {
			out = append(out, ui.Visible(ch.key()+": "+r))
			continue
		}
		if !s.follow {
			continue
		}
		f := strings.Fields(r)
		next, found := rs.chains[ch.family+" "+ch.table+" "+f[len(f)-1]]
		if !found {
			out = append(out, ui.Visible(ch.key()+": "+r))
			continue
		}
		if seen[next.key()] {
			continue
		}
		seen[next.key()] = true
		out = append(out, rs.judge(next, seen)...)
	}
	return out
}

// inBridge reports whether addr is inside a network Docker masquerades for
// bridge, read from the ruleset's ip nat POSTROUTING rules of Docker's
// shape.
func (rs ruleset) inBridge(addr, bridge string) bool {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	post, ok := rs.chains["ip nat POSTROUTING"]
	if !ok {
		return false
	}
	for _, r := range post.rules {
		if s, ok := match(post, r); !ok || !strings.Contains(s.rule, "MASQUERADE") {
			continue
		}
		f := strings.Fields(r)
		if f[5] != bridge {
			continue
		}
		if p, err := netip.ParsePrefix(f[2]); err == nil && p.Contains(a) {
			return true
		}
	}
	return false
}

// match finds the allowlisted shape rule r of ch is, if any.
func match(ch *chain, r string) (shape, bool) {
	if !iptablesTable(ch.family, ch.table) {
		return shape{}, false
	}
	got := strings.Fields(r)
	for _, s := range allowlist {
		if s.table != ch.table || !contains(s.families, ch.family) || !contains(s.chains, ch.chain) {
			continue
		}
		want := strings.Fields(s.rule)
		if len(want) != len(got) {
			continue
		}
		same := true
		for i := range want {
			if want[i] != got[i] && !placeholder(want[i], got[i]) {
				same = false
				break
			}
		}
		if same {
			return s, true
		}
	}
	return shape{}, false
}

func contains(list, word string) bool {
	for _, w := range strings.Fields(list) {
		if w == word {
			return true
		}
	}
	return false
}

// The ranges and names placeholders accept.
var (
	tailscale4 = netip.MustParsePrefix("100.64.0.0/10")
	tailscale6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
	bridgeRe   = regexp.MustCompile(`^"(docker0|br-[0-9a-f]{12})"$`)
)

// placeholder reports whether got is what the placeholder want accepts:
//   - <host4>: one IPv4 address, no prefix;
//   - <net4>: an IPv4 network in its plain form;
//   - <ts4>, <ts6>: one address in Tailscale's IPv4 or IPv6 range;
//   - <bridge>: Docker's default bridge or one of its networks' bridges;
//   - <ufw4>, <ufw6>: one of ufw's chains, by exact name.
func placeholder(want, got string) bool {
	switch want {
	case "<host4>":
		a, err := netip.ParseAddr(got)
		return err == nil && a.Is4()
	case "<net4>":
		p, err := netip.ParsePrefix(got)
		return err == nil && p.Addr().Is4() && p.Masked() == p
	case "<ts4>":
		a, err := netip.ParseAddr(got)
		return err == nil && a.Is4() && tailscale4.Contains(a)
	case "<ts6>":
		a, err := netip.ParseAddr(got)
		return err == nil && a.Is6() && tailscale6.Contains(a)
	case "<bridge>":
		return bridgeRe.MatchString(got)
	case "<ufw4>":
		return strings.HasPrefix(got, "ufw-") && ufwChains[strings.TrimPrefix(got, "ufw-")]
	case "<ufw6>":
		return strings.HasPrefix(got, "ufw6-") && ufwChains[strings.TrimPrefix(got, "ufw6-")]
	}
	return false
}

// ufwChains are ufw's chains, as ufw on Ubuntu 24.04 names them after its
// ufw- (IPv4) or ufw6- (IPv6) prefix.
var ufwChains = func() map[string]bool {
	m := map[string]bool{}
	for _, n := range strings.Fields(`
		before-logging-input before-logging-output before-logging-forward
		before-input before-output before-forward
		after-input after-output after-forward
		after-logging-input after-logging-output after-logging-forward
		reject-input reject-output reject-forward
		track-input track-output track-forward`) {
		m[n] = true
	}
	return m
}()

// chain is one chain of the ruleset.
type chain struct {
	family, table, chain string
	// hook is the hook it is attached to, "" for a chain only a jump
	// reaches; policy its policy, "" when none could be read.
	hook, policy string
	// rules are its rules, counters left out.
	rules []string
}

func (c *chain) key() string { return c.family + " " + c.table + " " + c.chain }

type ruleset struct {
	chains map[string]*chain
	order  []string
	// unreadable names each line the parser could not place.
	unreadable []string
	// unbalanced is a ruleset whose braces do not close by its end, or
	// with a line that ends inside a quoted string.
	unbalanced bool
}

// The patterns a ruleset is read with.
var (
	// egressTableRe is admit's own egress table, deployment.EgressTable.
	egressTableRe = regexp.MustCompile(`^inet paisans_[0-9a-f]{4}$`)
	hookRe        = regexp.MustCompile(`^type \S+ hook (\S+) `)
	policyRe      = regexp.MustCompile(`\bpolicy (\w+);`)
	// warningRe is the only comment nft prints in a ruleset: iptables-nft's
	// warnings, on a line of their own or after the rule it reached.
	warningRe = regexp.MustCompile(`^# Warning: (table \S+ \S+ is managed by iptables-nft, do not touch!|XT target \S+ not found)$`)
	// blockKeywords open a block in a table that filters nothing by
	// itself: a set, a map, a stateful object.
	blockKeywords = map[string]bool{"set": true, "map": true, "flowtable": true, "counter": true, "quota": true, "limit": true, "ct": true, "secmark": true, "synproxy": true}
)

// parseRuleset reads the ruleset's tables, chains, hooks, policies and
// rules. Braces and # are read outside quoted strings only. A line it
// cannot place is kept in unreadable, never skipped.
func parseRuleset(lines []string) ruleset {
	rs := ruleset{chains: map[string]*chain{}}
	var family, table string
	var cur *chain
	depth := 0
	inChain := false
	continued := false
	for _, line := range lines {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		// iptables-nft's warning, alone or after the part of a rule nft
		// printed before it; the rule then goes on on the next line.
		cut := false
		if i := commentAt(s); i >= 0 {
			if !warningRe.MatchString(s[i:]) {
				rs.unreadable = append(rs.unreadable, ui.Visible("unreadable ruleset line: "+s))
				continue
			}
			s, cut = strings.TrimSpace(s[:i]), true
			if s == "" {
				continue
			}
		}
		if strings.Count(s, `"`)%2 != 0 {
			rs.unbalanced = true
		}
		bare := unquoted(s)
		start := depth
		depth += strings.Count(bare, "{") - strings.Count(bare, "}")
		if depth < 0 {
			depth, rs.unbalanced = 0, true
		}
		word, _, _ := strings.Cut(s, " ")
		switch {
		case continued && cur != nil && len(cur.rules) > 0:
			cur.rules[len(cur.rules)-1] = normalise(cur.rules[len(cur.rules)-1] + " " + s)
		case start == 0:
			if f := strings.Fields(s); len(f) == 4 && f[0] == "table" && f[3] == "{" {
				family, table = f[1], f[2]
			} else if s != "}" {
				rs.unreadable = append(rs.unreadable, ui.Visible("unreadable ruleset line: "+s))
			}
		case start == 1:
			f := strings.Fields(s)
			switch {
			case len(f) == 3 && f[0] == "chain" && f[2] == "{":
				cur = &chain{family: family, table: table, chain: f[1]}
				if _, seen := rs.chains[cur.key()]; !seen {
					rs.order = append(rs.order, cur.key())
				}
				rs.chains[cur.key()] = cur
				inChain = true
			case blockKeywords[word] && strings.Contains(bare, "{"):
				inChain = false
			case s == "}" || word == "flags" || word == "comment":
			default:
				rs.unreadable = append(rs.unreadable, ui.Visible("unreadable ruleset line: "+s))
			}
		case start == 2 && inChain && cur != nil:
			switch {
			case s == "}":
			case word == "type":
				m := hookRe.FindStringSubmatch(s + " ")
				if m == nil {
					rs.unreadable = append(rs.unreadable, ui.Visible("unreadable ruleset line: "+s))
					break
				}
				cur.hook = m[1]
				if p := policyRe.FindStringSubmatch(s); p != nil {
					cur.policy = p[1]
				}
			case word == "policy":
				if p := policyRe.FindStringSubmatch(s); p != nil {
					cur.policy = p[1]
				}
			case word == "comment" || word == "devices":
			default:
				cur.rules = append(cur.rules, normalise(s))
			}
		case start >= 3 && inChain && cur != nil && len(cur.rules) > 0:
			// A rule nft printed over several lines, a verdict map's.
			cur.rules[len(cur.rules)-1] = normalise(cur.rules[len(cur.rules)-1] + " " + s)
		}
		continued = cut
		if depth <= 1 {
			inChain = false
		}
	}
	if depth != 0 {
		rs.unbalanced = true
	}
	return rs
}

// normalise is a rule with its counter left out and its spaces single.
func normalise(r string) string {
	f := strings.Fields(r)
	var out []string
	for i := 0; i < len(f); i++ {
		if f[i] == "counter" && i+4 < len(f) && f[i+1] == "packets" && f[i+3] == "bytes" && digits(f[i+2]) && digits(f[i+4]) {
			i += 4
			continue
		}
		out = append(out, f[i])
	}
	return strings.Join(out, " ")
}

func digits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// commentAt is the index of the first # outside a quoted string, or -1.
// nft's quoted strings have no escape character: a backslash is itself.
func commentAt(s string) int {
	in := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			in = !in
		case !in && c == '#':
			return i
		}
	}
	return -1
}

// unquoted is s with every double-quoted string removed. nft's quoted
// strings have no escape character, so a backslash ends nothing.
func unquoted(s string) string {
	var b strings.Builder
	in := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			in = !in
		case !in:
			b.WriteByte(c)
		}
	}
	return b.String()
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
