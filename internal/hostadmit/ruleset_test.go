package hostadmit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture reads one of testdata's rulesets, each nft list ruleset as a
// real host printed it:
//   - tailscale-nft-ruleset.txt: Tailscale 1.102.5 on iptables-nft, its
//     tailnet addresses replaced with placeholders;
//   - ufw-active-nft-ruleset.txt: ufw enabled with 22/tcp allowed, in
//     ubuntu:24.04;
//   - docker-29-nft-ruleset.txt: Docker 29.9.0 with a second network and a
//     published port, in docker:29-dind.
func fixture(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func tailscaleRuleset(t *testing.T) []string { return fixture(t, "tailscale-nft-ruleset.txt") }

// insertAfter is lines with extra added after the first line equal to
// after once trimmed, the next being found from start.
func insertAfter(t *testing.T, lines []string, start, after string, extra ...string) []string {
	t.Helper()
	seen := false
	for i, l := range lines {
		s := strings.TrimSpace(l)
		if s == start {
			seen = true
		}
		if seen && s == after {
			return append(append(append([]string{}, lines[:i+1]...), extra...), lines[i+1:]...)
		}
	}
	t.Fatalf("no %q after %q", after, start)
	return nil
}

func split(ruleset string) []string { return strings.Split(ruleset, "\n") }

// wantOwner checks ownerFirewall names exactly want.
func wantOwner(t *testing.T, name string, lines []string, want ...string) {
	t.Helper()
	got := ownerFirewall(lines)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s: ownerFirewall = %q, want %q", name, got, want)
	}
}

// Tailscale's rules filter only what reaches its own interface, port and
// ranges, so a host running it has no firewall of its owner's.
func TestTailscaleIsNotTheOwnersFirewall(t *testing.T) {
	wantOwner(t, "tailscale", tailscaleRuleset(t))
}

// ufw's and Docker's own rulesets, as each writes them, are not the
// owner's firewall: ufw's chains are known by name, and the DROP policy ufw
// sets on INPUT and FORWARD is ufw's; Docker's chains are known by name,
// and the drop Docker 29 puts in raw PREROUTING for a published port is
// Docker's.
func TestUFWAndDockerAreNotTheOwnersFirewall(t *testing.T) {
	wantOwner(t, "ufw", fixture(t, "ufw-active-nft-ruleset.txt"))
	wantOwner(t, "docker", fixture(t, "docker-29-nft-ruleset.txt"))
	// Docker sets FORWARD's policy to DROP on a host it enables forwarding
	// on.
	docker := strings.Join(fixture(t, "docker-29-nft-ruleset.txt"), "\n")
	docker = strings.Replace(docker, "type filter hook forward priority filter; policy accept;", "type filter hook forward priority filter; policy drop;", 1)
	wantOwner(t, "docker forward drop", split(docker))
}

// One rule of the owner's own beside Tailscale's is a firewall, named by
// its table, its chain and the rule.
func TestAnOwnersRuleBesideTailscaleIsAFirewall(t *testing.T) {
	lines := insertAfter(t, tailscaleRuleset(t), "table ip filter {", "counter packets 85770 bytes 7094850 jump ts-input", "\t\ttcp dport 22 accept")
	wantOwner(t, "owner rule", lines, "ip filter INPUT: tcp dport 22 accept")
}

// An owner's own table hooking input or forward is a firewall, rules or
// none, whatever its chain is named.
func TestAnOwnersTableIsAFirewall(t *testing.T) {
	wantOwner(t, "with a rule", split("table inet filter {\n\tchain input {\n\t\ttype filter hook input priority filter; policy drop;\n\t\tct state established,related accept\n\t}\n}"), "inet filter input")
	wantOwner(t, "alone", split("table inet filter {\n\tchain input {\n\t\ttype filter hook input priority filter; policy accept;\n\t}\n}"), "inet filter input")
	wantOwner(t, "forward", split("table inet owner {\n\tchain fwd {\n\t\ttype filter hook forward priority 0; policy drop;\n\t}\n}"), "inet owner fwd")
	wantOwner(t, "named like Tailscale's", split("table inet own {\n\tchain ts-mine {\n\t\ttype filter hook input priority 0; policy drop;\n\t\ttcp dport 22 accept\n\t}\n}"), "inet own ts-mine")
}

// Admit's own egress table drops on its output hook and is admit's, not
// the owner's; an owner's table that drops on output is a firewall.
func TestOutputIngressAndPreroutingDropsAreAFirewall(t *testing.T) {
	egress := "table inet paisans_f2a9 {\n\tset private {\n\t\ttype ipv4_addr\n\t\tflags interval\n\t\telements = { 10.0.0.0/8,\n\t\t\t     192.168.0.0/16 }\n\t}\n\tchain users {\n\t\ttype filter hook output priority 0; policy accept;\n\t\tip daddr @private drop\n\t}\n}"
	wantOwner(t, "admit's egress table", split(egress))
	// Only its output hook is admit's: an input hook in a table of that
	// name is judged like any other.
	wantOwner(t, "an input hook in admit's table", split("table inet paisans_f2a9 {\n\tchain in {\n\t\ttype filter hook input priority 0; policy drop;\n\t}\n}"), "inet paisans_f2a9 in")
	wantOwner(t, "output drop", split("table inet own {\n\tchain out {\n\t\ttype filter hook output priority 0; policy accept;\n\t\tip daddr 192.0.2.1 drop\n\t}\n}"), "inet own out: ip daddr 192.0.2.1 drop")
	wantOwner(t, "ingress policy", split("table netdev own {\n\tchain in {\n\t\ttype filter hook ingress device \"eth0\" priority 0; policy drop;\n\t}\n}"), "netdev own in: policy drop")
	wantOwner(t, "prerouting reject", split("table inet own {\n\tchain pre {\n\t\ttype filter hook prerouting priority 0; policy accept;\n\t\ttcp dport 25 reject\n\t\tmeta mark set 0x1\n\t}\n}"), "inet own pre: tcp dport 25 reject", "inet own pre: meta mark set 0x1")
	// Every rule on a hook counts unless it is a known shape: a mark set
	// on prerouting can steer what reaches input as surely as a drop.
	wantOwner(t, "prerouting without a drop", split("table inet own {\n\tchain pre {\n\t\ttype filter hook prerouting priority 0; policy accept;\n\t\tmeta mark set 0x1\n\t}\n}"), "inet own pre: meta mark set 0x1")
	wantOwner(t, "postrouting policy", split("table inet own {\n\tchain post {\n\t\ttype nat hook postrouting priority srcnat; policy drop;\n\t}\n}"), "inet own post: policy drop")
	wantOwner(t, "egress policy", split("table netdev own {\n\tchain out {\n\t\ttype filter hook egress device \"eth0\" priority 0; policy drop;\n\t}\n}"), "netdev own out: policy drop")
}

// A drop or reject policy on iptables-nft's INPUT or FORWARD, with no rule,
// is the owner's firewall, unless ufw's or Docker's chains are there too.
func TestADropPolicyIsAFirewall(t *testing.T) {
	wantOwner(t, "input drop", split("table ip filter {\n\tchain INPUT {\n\t\ttype filter hook input priority filter; policy drop;\n\t}\n}"), "ip filter INPUT: policy drop")
	wantOwner(t, "forward drop", split("table ip6 filter {\n\tchain FORWARD {\n\t\ttype filter hook forward priority filter; policy drop;\n\t}\n}"), "ip6 filter FORWARD: policy drop")
	wantOwner(t, "input drop beside Tailscale", split(strings.Replace(strings.Join(tailscaleRuleset(t), "\n"), "type filter hook input priority filter; policy accept;", "type filter hook input priority filter; policy drop;", 1)), "ip filter INPUT: policy drop")
}

// A jump is followed only when it is exactly a known shape, to a chain
// whose rules are each a known shape too. Any other jump or goto, to a
// chain named like Tailscale's, ufw's or Docker's or not, is the owner's
// rule itself.
func TestOnlyKnownJumpsAreFollowed(t *testing.T) {
	base := func(input, chains string) []string {
		return split("table ip filter {\n" + chains + "\tchain INPUT {\n\t\ttype filter hook input priority filter; policy accept;\n\t\t" + input + "\n\t}\n}")
	}
	wantOwner(t, "ts-mine", base("counter packets 1 bytes 2 jump ts-mine", "\tchain ts-mine {\n\t\tiifname \"tailscale0\" accept\n\t}\n"), "ip filter INPUT: jump ts-mine")
	wantOwner(t, "DOCKERMINE", base("goto DOCKERMINE", "\tchain DOCKERMINE {\n\t\ttcp dport 22 accept\n\t}\n"), "ip filter INPUT: goto DOCKERMINE")
	wantOwner(t, "ufw-mine", base("jump ufw-mine", "\tchain ufw-mine {\n\t\ttcp dport 22 drop\n\t}\n"), "ip filter INPUT: jump ufw-mine")
	wantOwner(t, "DOCKER-USER from INPUT", base("jump DOCKER-USER", "\tchain DOCKER-USER {\n\t}\n"), "ip filter INPUT: jump DOCKER-USER")
	wantOwner(t, "a chain that only returns", base("jump mine", "\tchain mine {\n\t\tcounter packets 0 bytes 0 return\n\t}\n"), "ip filter INPUT: jump mine")
	wantOwner(t, "a loop", base("jump a", "\tchain a {\n\t\tjump b\n\t}\n\tchain b {\n\t\tjump a\n\t}\n"), "ip filter INPUT: jump a")
	wantOwner(t, "a chain that is not there", base("jump gone", ""), "ip filter INPUT: jump gone")
	wantOwner(t, "a vmap", base("tcp dport vmap { 22 : jump ts-input }", "\tchain ts-input {\n\t}\n"), "ip filter INPUT: tcp dport vmap { 22 : jump ts-input }")
	wantOwner(t, "a conditional jump to Tailscale's chain", base("iifname \"eth0\" jump ts-input", "\tchain ts-input {\n\t}\n"), "ip filter INPUT: iifname \"eth0\" jump ts-input")
	// DOCKER-USER, jumped to from FORWARD as Docker does, is followed: empty
	// or return alone it is Docker's, any other rule in it the owner's.
	forward := func(user string) []string {
		return split("table ip filter {\n\tchain DOCKER-USER {\n" + user + "\t}\n\tchain FORWARD {\n\t\ttype filter hook forward priority filter; policy accept;\n\t\tcounter packets 0 bytes 0 jump DOCKER-USER\n\t}\n}")
	}
	wantOwner(t, "DOCKER-USER empty", forward(""))
	wantOwner(t, "DOCKER-USER return", forward("\t\tcounter packets 0 bytes 0 return\n"))
	wantOwner(t, "DOCKER-USER with a rule", forward("\t\tip saddr 192.0.2.0/24 drop\n"), "ip filter DOCKER-USER: ip saddr 192.0.2.0/24 drop")
}

// Each way a rule was hidden from the classifier before, now named.
func TestHiddenRulesAreNamed(t *testing.T) {
	// A conditional goto on prerouting, to a chain that drops.
	wantOwner(t, "goto on prerouting", split("table ip mangle {\n\tchain X {\n\t\tdrop\n\t}\n\tchain PREROUTING {\n\t\ttype filter hook prerouting priority mangle; policy accept;\n\t\tiifname \"eth0\" goto X\n\t}\n}"), "ip mangle PREROUTING: iifname \"eth0\" goto X")
	// Docker's per-port drop names one host, inside the network Docker
	// masquerades for the same bridge, and that bridge.
	nat := "table ip nat {\n\tchain POSTROUTING {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n\t\tip saddr 172.19.0.0/16 oifname != \"br-179c4f2598a0\" counter packets 0 bytes 0 xt target \"MASQUERADE\"\n\t\tip saddr 172.18.0.0/16 oifname != \"docker0\" counter packets 0 bytes 0 xt target \"MASQUERADE\"\n\t}\n}\n"
	raw := func(family, table, rule string) []string {
		return split(nat + "table " + family + " " + table + " {\n\tchain PREROUTING {\n\t\ttype filter hook prerouting priority raw; policy accept;\n\t\t" + rule + "\n\t}\n}")
	}
	wantOwner(t, "docker's drop", raw("ip", "raw", "ip daddr 172.18.0.2 iifname != \"docker0\" counter packets 0 bytes 0 drop"))
	wantOwner(t, "docker's drop on a bridge", raw("ip", "raw", "ip daddr 172.19.0.2 iifname != \"br-179c4f2598a0\" drop"))
	wantOwner(t, "the host's own address", raw("ip", "raw", "ip daddr 203.0.113.5 iifname != \"docker0\" drop"), "ip raw PREROUTING: ip daddr 203.0.113.5 iifname != \"docker0\" drop")
	wantOwner(t, "another bridge's network", raw("ip", "raw", "ip daddr 172.19.0.2 iifname != \"docker0\" drop"), "ip raw PREROUTING: ip daddr 172.19.0.2 iifname != \"docker0\" drop")
	wantOwner(t, "no masquerade at all", split("table ip raw {\n\tchain PREROUTING {\n\t\ttype filter hook prerouting priority raw; policy accept;\n\t\tip daddr 172.18.0.2 iifname != \"docker0\" drop\n\t}\n}"), "ip raw PREROUTING: ip daddr 172.18.0.2 iifname != \"docker0\" drop")
	wantOwner(t, "a prefix", raw("ip", "raw", "ip daddr 0.0.0.0/1 iifname != \"docker0\" drop"), "ip raw PREROUTING: ip daddr 0.0.0.0/1 iifname != \"docker0\" drop")
	wantOwner(t, "an inet raw table", raw("inet", "raw", "ip daddr 172.18.0.2 iifname != \"docker0\" drop"), "inet raw PREROUTING: ip daddr 172.18.0.2 iifname != \"docker0\" drop")
	wantOwner(t, "another interface", raw("ip", "raw", "ip daddr 172.18.0.2 iifname != \"eth0\" drop"), "ip raw PREROUTING: ip daddr 172.18.0.2 iifname != \"eth0\" drop")
	// A set whose comment holds the warning's text, and a brace, on one line.
	wantOwner(t, "warning in a comment", split("table ip filter {\n\tset s { type ipv4_addr; comment \"# Warning: table ip filter is managed by iptables-nft, do not touch! }\" }\n\tchain INPUT {\n\t\ttype filter hook input priority filter; policy accept;\n\t\ttcp dport 22 drop\n\t}\n}"), "ip filter INPUT: tcp dport 22 drop")
	// ufw's chain names are ufw's only in iptables-nft's own filter tables.
	wantOwner(t, "ufw's name in an inet table", split("table inet own {\n\tchain ufw-user-input {\n\t\ttcp dport 22 drop\n\t}\n\tchain input {\n\t\ttype filter hook input priority 0; policy accept;\n\t\tjump ufw-user-input\n\t}\n}"), "inet own input")
	wantOwner(t, "ufw's jump in an ip nat table", split("table ip nat {\n\tchain ufw-before-input {\n\t\tdrop\n\t}\n\tchain PREROUTING {\n\t\ttype nat hook prerouting priority dstnat; policy accept;\n\t\tjump ufw-before-input\n\t}\n}"), "ip nat PREROUTING: jump ufw-before-input")
	// A rule in Tailscale's chain that names its interface and is not one
	// of Tailscale's own.
	lines := insertAfter(t, tailscaleRuleset(t), "table ip filter {", "udp dport 41641 counter packets 22 bytes 1320 accept", "\t\tiifname \"tailscale0\" tcp dport 22 drop")
	wantOwner(t, "owner rule in ts-input", lines, "ip filter ts-input: iifname \"tailscale0\" tcp dport 22 drop")
	// Lines the parser does not know are the owner's, never skipped.
	wantOwner(t, "an unknown line", split("table ip filter {\n\tfrobnicate x\n}"), "unreadable ruleset line: frobnicate x")
	wantOwner(t, "another comment", split("# Note: something\ntable ip filter {\n}"), "unreadable ruleset line: # Note: something")
}

// A brace in a quoted string, a comment's, does not end a chain or open
// one, so it cannot hide the rule after it.
func TestQuotedBracesDoNotHideARule(t *testing.T) {
	for _, brace := range []string{"{", "}"} {
		lines := split("table ip filter {\n\tchain ts-input {\n\t\tiifname \"tailscale0\" accept comment \"" + brace + "\"\n\t}\n\tchain INPUT {\n\t\ttype filter hook input priority filter; policy accept;\n\t\tjump ts-input\n\t\ttcp dport 22 accept\n\t}\n}")
		wantOwner(t, "comment "+brace, lines, "ip filter ts-input: iifname \"tailscale0\" accept comment \""+brace+"\"", "ip filter INPUT: tcp dport 22 accept")
	}
}

// The probe's ruleset lines reach Facts, classified; a ruleset nft could
// not print, and rules only iptables-legacy holds, are carried for the
// firewall part to refuse on, since admit cannot see the owner's firewall
// in either.
func TestParseFactsClassifiesTheRuleset(t *testing.T) {
	var b strings.Builder
	for _, l := range tailscaleRuleset(t) {
		b.WriteString("ruleset " + l + "\n")
	}
	f, err := parseFacts(b.String()+"end\n", "paisans-f2a9")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Ruleset) != len(tailscaleRuleset(t)) || len(f.OwnerFirewall) != 0 || f.RulesetUnknown || len(f.LegacyRules) != 0 {
		t.Errorf("ruleset %d lines, owner firewall %q, unknown %v, legacy %q", len(f.Ruleset), f.OwnerFirewall, f.RulesetUnknown, f.LegacyRules)
	}
	f, err = parseFacts("ruleset table inet filter {\nruleset \tchain input {\nruleset \t\ttype filter hook input priority 0; policy drop;\nruleset \t}\nruleset }\nend\n", "paisans-f2a9")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.OwnerFirewall) != 1 {
		t.Errorf("owner firewall %q", f.OwnerFirewall)
	}
	f, err = parseFacts("ruleset unknown\nlegacy -A INPUT -p tcp -m tcp --dport 22 -j ACCEPT\nlegacy :FORWARD DROP [0:0]\nend\n", "paisans-f2a9")
	if err != nil {
		t.Fatal(err)
	}
	if !f.RulesetUnknown || len(f.Ruleset) != 0 || len(f.LegacyRules) != 2 || f.LegacyRules[0] != "-A INPUT -p tcp -m tcp --dport 22 -j ACCEPT" {
		t.Errorf("unknown %v, ruleset %q, legacy %q", f.RulesetUnknown, f.Ruleset, f.LegacyRules)
	}
}

// legacy unknown is a host whose legacy tables are loaded and could not be
// saved: rules admit cannot see.
func TestParseFactsReadsLegacyUnknown(t *testing.T) {
	f, err := parseFacts("legacy unknown\nend\n", "paisans-f2a9")
	if err != nil {
		t.Fatal(err)
	}
	if !f.LegacyUnknown || len(f.LegacyRules) != 0 {
		t.Errorf("legacy unknown %v, rules %q", f.LegacyUnknown, f.LegacyRules)
	}
}

// nft's quoted strings have no escape: "a\" is a string ending in a
// backslash, as nft 1.0.9 prints a set holding it. Read as an escape, the
// quote would stay open and the chain after it would vanish.
func TestABackslashDoesNotEscapeAQuote(t *testing.T) {
	ruleset := "table ip filter {\n\tchain X {\n\t\tiifname { \"b\", \"a\\\" } drop\n\t}\n\n\tchain INPUT {\n\t\ttype filter hook input priority filter; policy drop;\n\t\ttcp dport 443 drop\n\t}\n}"
	wantOwner(t, "backslash", split(ruleset), "ip filter INPUT: policy drop", "ip filter INPUT: tcp dport 443 drop")
}

// A ruleset whose braces do not balance, or a line left inside an open
// quote, is not one the parser understands: the whole ruleset is named
// unreadable, as an unknown ruleset is.
func TestAnUnbalancedRulesetIsUnreadable(t *testing.T) {
	for name, ruleset := range map[string]string{
		"brace": "table ip filter {\n\tchain INPUT {\n\t\ttype filter hook input priority filter; policy accept;\n\t}\n",
		"quote": "table ip filter {\n\tchain INPUT {\n\t\ttype filter hook input priority filter; policy accept;\n\t\tcomment \"open\n\t}\n}",
	} {
		got := ownerFirewall(split(ruleset))
		if len(got) == 0 || got[len(got)-1] != "unreadable ruleset: its braces or quotes do not balance" {
			t.Errorf("%s: ownerFirewall = %q", name, got)
		}
	}
}
