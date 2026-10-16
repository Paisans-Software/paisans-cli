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
	wantOwner(t, "output drop", split("table inet own {\n\tchain out {\n\t\ttype filter hook output priority 0; policy accept;\n\t\tip daddr 192.0.2.1 drop\n\t}\n}"), "inet own out: ip daddr 192.0.2.1 drop")
	wantOwner(t, "ingress policy", split("table netdev own {\n\tchain in {\n\t\ttype filter hook ingress device \"eth0\" priority 0; policy drop;\n\t}\n}"), "netdev own in: policy drop")
	wantOwner(t, "prerouting reject", split("table inet own {\n\tchain pre {\n\t\ttype filter hook prerouting priority 0; policy accept;\n\t\ttcp dport 25 reject\n\t\tmeta mark set 0x1\n\t}\n}"), "inet own pre: tcp dport 25 reject")
	wantOwner(t, "prerouting without a drop", split("table inet own {\n\tchain pre {\n\t\ttype filter hook prerouting priority 0; policy accept;\n\t\tmeta mark set 0x1\n\t}\n}"))
}

// A drop or reject policy on iptables-nft's INPUT or FORWARD, with no rule,
// is the owner's firewall, unless ufw's or Docker's chains are there too.
func TestADropPolicyIsAFirewall(t *testing.T) {
	wantOwner(t, "input drop", split("table ip filter {\n\tchain INPUT {\n\t\ttype filter hook input priority filter; policy drop;\n\t}\n}"), "ip filter INPUT: policy drop")
	wantOwner(t, "forward drop", split("table ip6 filter {\n\tchain FORWARD {\n\t\ttype filter hook forward priority filter; policy drop;\n\t}\n}"), "ip6 filter FORWARD: policy drop")
	wantOwner(t, "input drop beside Tailscale", split(strings.Replace(strings.Join(tailscaleRuleset(t), "\n"), "type filter hook input priority filter; policy accept;", "type filter hook input priority filter; policy drop;", 1)), "ip filter INPUT: policy drop")
}

// A jump from INPUT or FORWARD is followed: a ts-* chain is Tailscale's
// only when every rule in it is about Tailscale's interface, port, mark or
// ranges, and any other chain is judged by its rules. Docker's and ufw's
// chains are known by their exact names, not a prefix.
func TestJumpsAreFollowed(t *testing.T) {
	base := func(input, chains string) []string {
		return split("table ip filter {\n" + chains + "\tchain INPUT {\n\t\ttype filter hook input priority filter; policy accept;\n\t\t" + input + "\n\t}\n}")
	}
	wantOwner(t, "ts-mine", base("counter packets 1 bytes 2 jump ts-mine", "\tchain ts-mine {\n\t\tiifname \"tailscale0\" accept\n\t\ttcp dport 22 accept\n\t}\n"), "ip filter ts-mine: tcp dport 22 accept")
	wantOwner(t, "DOCKERMINE", base("goto DOCKERMINE", "\tchain DOCKERMINE {\n\t\ttcp dport 22 accept\n\t}\n"), "ip filter DOCKERMINE: tcp dport 22 accept")
	wantOwner(t, "ufw-mine", base("jump ufw-mine", "\tchain ufw-mine {\n\t\ttcp dport 22 drop\n\t}\n"), "ip filter ufw-mine: tcp dport 22 drop")
	wantOwner(t, "DOCKER-USER", base("jump DOCKER-USER", "\tchain DOCKER-USER {\n\t\tip saddr 192.0.2.0/24 drop\n\t}\n"), "ip filter DOCKER-USER: ip saddr 192.0.2.0/24 drop")
	wantOwner(t, "a chain that only returns", base("jump mine", "\tchain mine {\n\t\tcounter packets 0 bytes 0 return\n\t}\n"))
	wantOwner(t, "a loop", base("jump a", "\tchain a {\n\t\tjump b\n\t}\n\tchain b {\n\t\tjump a\n\t}\n"))
	wantOwner(t, "a chain that is not there", base("jump gone", ""), "ip filter INPUT: jump gone")
}

// A brace in a quoted string, a comment's, does not end a chain or open
// one, so it cannot hide the rule after it.
func TestQuotedBracesDoNotHideARule(t *testing.T) {
	for _, brace := range []string{"{", "}"} {
		lines := split("table ip filter {\n\tchain ts-input {\n\t\tiifname \"tailscale0\" accept comment \"" + brace + "\"\n\t}\n\tchain INPUT {\n\t\ttype filter hook input priority filter; policy accept;\n\t\tjump ts-input\n\t\ttcp dport 22 accept\n\t}\n}")
		wantOwner(t, "comment "+brace, lines, "ip filter INPUT: tcp dport 22 accept")
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
