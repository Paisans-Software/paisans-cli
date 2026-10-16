package hostadmit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tailscaleRuleset is a real ruleset from a host running Tailscale 1.102.5
// on iptables-nft, its tailnet addresses replaced with placeholders.
func tailscaleRuleset(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "tailscale-nft-ruleset.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

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
			out := append(append(append([]string{}, lines[:i+1]...), extra...), lines[i+1:]...)
			return out
		}
	}
	t.Fatalf("no %q after %q", after, start)
	return nil
}

// Tailscale's rules filter only what reaches its own interface and port,
// so a host running it has no firewall of its owner's: its chains are
// ts-*, and the iptables INPUT and FORWARD chains only jump to them.
func TestTailscaleIsNotTheOwnersFirewall(t *testing.T) {
	if got := ownerFirewall(tailscaleRuleset(t)); len(got) != 0 {
		t.Errorf("Tailscale's rules read as the owner's firewall: %q", got)
	}
}

// One rule of the owner's own beside Tailscale's is a firewall, named by
// its table, its chain and the rule.
func TestAnOwnersRuleBesideTailscaleIsAFirewall(t *testing.T) {
	lines := insertAfter(t, tailscaleRuleset(t), "table ip filter {", "counter packets 85770 bytes 7094850 jump ts-input", "\t\ttcp dport 22 accept")
	got := ownerFirewall(lines)
	if len(got) != 1 || got[0] != "ip filter INPUT: tcp dport 22 accept" {
		t.Errorf("ownerFirewall = %q", got)
	}
}

// An owner's own table hooking input is a firewall, rules or none.
func TestAnOwnersTableIsAFirewall(t *testing.T) {
	for name, ruleset := range map[string]string{
		"with a rule": "table inet filter {\n\tchain input {\n\t\ttype filter hook input priority filter; policy drop;\n\t\tct state established,related accept\n\t}\n}",
		"alone":       "table inet filter {\n\tchain input {\n\t\ttype filter hook input priority filter; policy accept;\n\t}\n}",
		"forward":     "table inet owner {\n\tchain fwd {\n\t\ttype filter hook forward priority 0; policy drop;\n\t}\n}",
	} {
		got := ownerFirewall(strings.Split(ruleset, "\n"))
		if len(got) != 1 || !strings.HasPrefix(got[0], "inet ") {
			t.Errorf("%s: ownerFirewall = %q", name, got)
		}
	}
	// A table with sets and an output hook, as admit's own egress table
	// has, filters nothing incoming.
	egress := "table inet paisans_f2a9 {\n\tset private {\n\t\ttype ipv4_addr\n\t\tflags interval\n\t\telements = { 10.0.0.0/8,\n\t\t\t     192.168.0.0/16 }\n\t}\n\tchain users {\n\t\ttype filter hook output priority 0; policy accept;\n\t\tip daddr @private drop\n\t}\n}"
	if got := ownerFirewall(strings.Split(egress, "\n")); len(got) != 0 {
		t.Errorf("an output hook read as a firewall: %q", got)
	}
}

// ufw's rules, on iptables-nft, jump from INPUT and FORWARD to its own
// chains, and Docker's to DOCKER*: neither is the owner's own firewall.
func TestUFWAndDockerAreNotTheOwnersFirewall(t *testing.T) {
	ruleset := `# Warning: table ip filter is managed by iptables-nft, do not touch!
table ip filter {
	chain ufw-before-input {
		iifname "lo" counter packets 0 bytes 0 accept
		ct state related,established counter packets 1 bytes 2 accept
	}

	chain INPUT {
		type filter hook input priority filter; policy drop;
		counter packets 10 bytes 20 jump ufw-before-logging-input
		counter packets 10 bytes 20 jump ufw-before-input
		counter packets 0 bytes 0 jump ufw-after-input
	}

	chain FORWARD {
		type filter hook forward priority filter; policy drop;
		counter packets 0 bytes 0 jump DOCKER-USER
		counter packets 0 bytes 0 jump DOCKER-FORWARD
		counter packets 0 bytes 0 jump ufw-before-forward
	}
}
table ip6 filter {
	chain INPUT {
		type filter hook input priority filter; policy drop;
		counter packets 0 bytes 0 jump ufw6-before-input
	}
}`
	if got := ownerFirewall(strings.Split(ruleset, "\n")); len(got) != 0 {
		t.Errorf("ufw's and Docker's rules read as the owner's firewall: %q", got)
	}
}

// The probe's ruleset lines reach Facts, classified.
func TestParseFactsClassifiesTheRuleset(t *testing.T) {
	var b strings.Builder
	for _, l := range tailscaleRuleset(t) {
		b.WriteString("ruleset " + l + "\n")
	}
	f, err := parseFacts(b.String()+"end\n", "paisans-f2a9")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Ruleset) != len(tailscaleRuleset(t)) || len(f.OwnerFirewall) != 0 {
		t.Errorf("ruleset %d lines, owner firewall %q", len(f.Ruleset), f.OwnerFirewall)
	}
	f, err = parseFacts("ruleset table inet filter {\nruleset \tchain input {\nruleset \t\ttype filter hook input priority 0; policy drop;\nruleset \t}\nruleset }\nend\n", "paisans-f2a9")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.OwnerFirewall) != 1 {
		t.Errorf("owner firewall %q", f.OwnerFirewall)
	}
}
