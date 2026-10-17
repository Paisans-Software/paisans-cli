package hostadmit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runPortsProbe runs portsProbe under sh with only the tools given, each a
// script printing its output, on top of the system's /usr/bin and /bin.
func runPortsProbe(t *testing.T, tools map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, out := range tools {
		script := "#!/bin/sh\ncat <<'PAISANS_EOF'\n" + out + "PAISANS_EOF\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", portsProbe)
	cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the probe failed: %v\n%s", err, out)
	}
	return string(out)
}

// A rule that only matches conntrack's dnat status, as Kubernetes and
// Docker filter rules do, is not a NAT rule; one that translates is, with
// or without a ct match beside it.
func TestThePortsProbeReadsOnlyTranslatingRules(t *testing.T) {
	out := runPortsProbe(t, map[string]string{"nft": `table ip filter {
	chain FORWARD {
		ct status dnat tcp dport 443 accept
	}
}
table ip nat {
	chain PREROUTING {
		tcp dport 443 dnat to 10.0.0.2
		ct status dnat tcp dport 80 redirect to :8080
	}
}
`})
	var dnat []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "dnat ") {
			dnat = append(dnat, l)
		}
	}
	if len(dnat) != 2 || !strings.Contains(dnat[0], "dnat to 10.0.0.2") || !strings.Contains(dnat[1], "redirect to :8080") {
		t.Errorf("the NAT rules read are %q", dnat)
	}
}

// A host with neither nft nor iptables-save has a ruleset admit cannot
// read, and the probe says so rather than print nothing.
func TestThePortsProbeSaysWhenNoRulesetReaderExists(t *testing.T) {
	out := runPortsProbe(t, nil)
	if !strings.Contains(out, "unread ruleset (no nft or iptables-save)") {
		t.Errorf("the probe printed:\n%s", out)
	}
}

// A negated destination port holds 80 or 443 unless it names both.
func TestANegatedPortHoldsWhatItDoesNotName(t *testing.T) {
	for rule, want := range map[string]bool{
		"tcp dport != { 80, 443 } dnat to 10.0.0.2": false,
		"tcp dport != 443 dnat to 10.0.0.2":         true,
		"tcp dport != 1-1000 dnat to 10.0.0.2":      false,
		"tcp dport != 1-100 dnat to 10.0.0.2":       true,
		"-p tcp -m tcp ! --dport 22 -j DNAT":        true,
	} {
		if got, _ := ruleHoldsLow(rule); got != want {
			t.Errorf("%q holds 80 or 443: %v, want %v", rule, got, want)
		}
	}
}

// A port operand that yields no port admit can read, a named set, a map
// or a concatenation, may hold 80 or 443, and does, unresolved, so the
// refusal can say so.
func TestAPortInASetOrMapHoldsUnresolved(t *testing.T) {
	for _, rule := range []string{
		"tcp dport . ip saddr { 443 . 1.2.3.4 } dnat ip to 10.0.0.2",
		"tcp dport { https } dnat ip to 10.0.0.2",
		"tcp dport { 22, @more } dnat ip to 10.0.0.2",
		"tcp dport @web dnat ip to 10.0.0.2",
		"dnat ip to tcp dport map { 443 : 10.0.0.2 }",
		"dnat ip to tcp dport map @ports",
		"tcp dport vmap @jumps",
		"tcp dport != @web dnat ip to 10.0.0.2",
	} {
		if holds, unresolved := ruleHoldsLow(rule); !holds || !unresolved {
			t.Errorf("%q: holds %v, unresolved %v", rule, holds, unresolved)
		}
	}
}

// IPV6 is read as ufw reads it: /etc/default/ufw, then /etc/ufw/ufw.conf,
// the key in any case, the last value winning.
func TestIPv6IsReadAsUFWReadsIt(t *testing.T) {
	dir := t.TempDir()
	defaults, conf := filepath.Join(dir, "default-ufw"), filepath.Join(dir, "ufw.conf")
	for _, c := range []struct{ defaults, conf, want string }{
		{"IPV6=yes\n", "ENABLED=no\n", "yes"},
		{"IPV6=\"yes\"\n", "", "yes"},
		{"IPV6=yes\n", "ENABLED=no\nIPV6=no\n", "no"},
		{"IPV6=yes\nipv6=no\n", "", "no"},
		{"IPV6=YES\n", "", "yes"},
		{"IPV6=yes # on\n", "", "yes # on"},
		{"", "", ""},
	} {
		os.WriteFile(defaults, []byte(c.defaults), 0o644)
		os.WriteFile(conf, []byte(c.conf), 0o644)
		out, err := exec.Command("sh", "-c", ipv6Read(defaults, conf)).CombinedOutput()
		if err != nil || string(out) != "ipv6 "+c.want+"\n" {
			t.Errorf("%q then %q: %q (%v), want ipv6 %s", c.defaults, c.conf, out, err, c.want)
		}
	}
	if !strings.Contains(ufwBeforeProbe, ipv6Read("/etc/default/ufw", "/etc/ufw/ufw.conf")) {
		t.Error("the probe does not read IPV6 from both of ufw's files")
	}
}
