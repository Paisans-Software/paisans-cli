package hostprep_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

var egress = hostprep.EgressData{
	Token: "f2a9", Table: "paisans_f2a9", Mark: "0x5053f2a9",
	Users: "1001, 165536-231071", Mesh: "10.44.0.0/24",
	Resolvers4: []string{"127.0.0.53"},
}

// The table matches the user's UID and its whole subordinate range in its
// UID chain, since a container's users map into that range, and the mesh's
// mark in its mark chain, which drops the host's own addresses before any
// --mesh-lan accept.
func TestEgressTableRendersTheUsersRangeAndTheMark(t *testing.T) {
	text, err := hostprep.EgressTable(egress)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"table inet paisans_f2a9 {",
		"meta skuid { 1001, 165536-231071 } ip daddr 10.44.0.0/24 accept",
		"meta skuid { 1001, 165536-231071 } ip daddr { 127.0.0.53 } meta l4proto { tcp, udp } th dport 53 accept",
		"meta skuid { 1001, 165536-231071 } ip daddr { 127.0.0.0/8, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 100.64.0.0/10 } drop",
		"meta skuid { 1001, 165536-231071 } ip6 daddr { ::1, fc00::/7, fe80::/10 } drop",
		"meta mark 0x5053f2a9 fib daddr type { local, broadcast, multicast } drop",
		"meta mark 0x5053f2a9 ip6 daddr { ::1, fe80::/10, ff00::/8, fc00::/7 } drop",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the table lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "ip6 daddr {  }") || strings.Count(text, "meta mark 0x5053f2a9 ") != 3 {
		t.Errorf("the table names a resolver or a LAN it was not given:\n%s", text)
	}
	lan := egress
	lan.LAN = "192.168.1.0/24"
	lan.Resolvers6 = []string{"fd00::1"}
	text, _ = hostprep.EgressTable(lan)
	local := strings.Index(text, "fib daddr type")
	accept := strings.Index(text, "meta mark 0x5053f2a9 ip daddr 192.168.1.0/24 accept")
	private := strings.Index(text, "meta mark 0x5053f2a9 ip daddr {")
	if local < 0 || accept < local || private < accept {
		t.Errorf("with --mesh-lan, the host's own addresses are not dropped first, then the LAN accepted, then the private ranges dropped:\n%s", text)
	}
	if strings.Count(text, "192.168.1.0/24") != 1 {
		t.Errorf("--mesh-lan reaches beyond the mark chain:\n%s", text)
	}
	if !strings.Contains(text, "meta skuid { 1001, 165536-231071 } ip6 daddr { fd00::1 } meta l4proto { tcp, udp } th dport 53 accept") {
		t.Errorf("the table lacks the IPv6 resolver:\n%s", text)
	}
}

// The unit loads the table before the user's manager, and is required by
// it, so a table that fails to load keeps every container from starting.
// It follows nftables.service, whose configuration on Ubuntu flushes the
// whole ruleset: a reload of nftables loads the table again, and a restart
// or stop restarts or stops it, so the table is never gone while the unit
// reads as active.
func TestEgressUnitIsRequiredByTheUsersManager(t *testing.T) {
	text, err := hostprep.EgressUnit(hostprep.EgressUnitData{Token: "f2a9", User: "paisans-f2a9", Table: "paisans_f2a9", File: "/etc/paisans/egress.f2a9.nft", UID: "1001"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Before=network-pre.target user@1001.service", "After=nftables.service", "RequiredBy=user@1001.service", "ExecStart=/usr/sbin/nft -f /etc/paisans/egress.f2a9.nft", "ExecReload=/usr/sbin/nft -f /etc/paisans/egress.f2a9.nft", "PartOf=nftables.service", "ReloadPropagatedFrom=nftables.service"} {
		if !strings.Contains(text, want) {
			t.Errorf("the unit lacks %q:\n%s", want, text)
		}
	}
}

// Where nft is installed and the test runs as root, the table parses, with
// and without --mesh-lan and an IPv6 resolver.
func TestEgressTableParses(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("nft -c needs root")
	}
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("no nft")
	}
	lan := egress
	lan.LAN = "192.168.1.0/24"
	lan.Resolvers6 = []string{"fd00::1"}
	for _, d := range []hostprep.EgressData{egress, lan} {
		text, err := hostprep.EgressTable(d)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "egress.nft")
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("nft", "-c", "-f", path).CombinedOutput(); err != nil {
			t.Fatalf("nft -c: %v\n%s\n%s", err, out, text)
		}
	}
}
