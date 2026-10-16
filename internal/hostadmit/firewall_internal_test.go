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
		if got := ruleHoldsLow(rule); got != want {
			t.Errorf("%q holds 80 or 443: %v, want %v", rule, got, want)
		}
	}
}
