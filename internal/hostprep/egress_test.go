package hostprep_test

import (
	"net/netip"
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

// Past the loopback and private drops, the UID chain drops every address
// of the host itself, its broadcast and multicast, and then the host's
// on-link IPv6 prefixes. Only a gateway, whose own Caddy or whose owner's
// web server answers 80 and 443 to the internet anyway, may first reach
// those two TCP ports on the host's own addresses. The mark chain drops
// the on-link prefixes as well, since a peer's endpoint could sit in one.
func TestEgressTableDropsTheHostAndItsOnLinkPrefixes(t *testing.T) {
	const uid = "meta skuid { 1001, 165536-231071 } "
	d := egress
	d.LAN6 = []netip.Prefix{netip.MustParsePrefix("2001:db8:1::/64"), netip.MustParsePrefix("2001:db8:2::/56")}
	for _, gateway := range []bool{false, true} {
		d.Gateway = gateway
		text, err := hostprep.EgressTable(d)
		if err != nil {
			t.Fatal(err)
		}
		private := strings.Index(text, uid+"ip6 daddr { ::1, fc00::/7, fe80::/10 } drop")
		web := strings.Index(text, uid+"fib daddr type local tcp dport { 80, 443 } accept")
		host := strings.Index(text, uid+"fib daddr type { local, broadcast, multicast } drop")
		lan6 := strings.Index(text, uid+"ip6 daddr { 2001:db8:1::/64, 2001:db8:2::/56 } drop")
		if private < 0 || host < private || lan6 < host {
			t.Errorf("gateway %v: the UID chain does not drop the private ranges, then the host, then its on-link prefixes:\n%s", gateway, text)
		}
		switch {
		case gateway && (web < private || web > host):
			t.Errorf("a gateway's 80 and 443 on the host are not accepted between the private drops and the host's:\n%s", text)
		case !gateway && web >= 0:
			t.Errorf("a site that is no gateway reaches 80 and 443 on the host:\n%s", text)
		}
		if !strings.Contains(text, "meta mark 0x5053f2a9 ip6 daddr { 2001:db8:1::/64, 2001:db8:2::/56 } drop") {
			t.Errorf("the mark chain does not drop the on-link prefixes:\n%s", text)
		}
		if strings.Count(text, "fib daddr type { local, broadcast, multicast } drop") != 2 {
			t.Errorf("the host's own addresses are not dropped once in each chain:\n%s", text)
		}
	}
	text, _ := hostprep.EgressTable(egress)
	if strings.Contains(text, "ip6 daddr {  }") || strings.Contains(text, "2001:db8") {
		t.Errorf("with no on-link prefix the table still drops one:\n%s", text)
	}
}

// The UID chain's first rule accepts replies: the host side of every port
// the deployment publishes runs as its user, so without it the answers to
// a visitor on loopback, the LAN or the host's own address would meet the
// drops below. The deployment's own flows are untouched, since their
// replies come in through input.
func TestEgressTableAcceptsRepliesFirst(t *testing.T) {
	for _, gateway := range []bool{false, true} {
		d := egress
		d.Gateway = gateway
		d.LAN6 = []netip.Prefix{netip.MustParsePrefix("2001:db8:1::/64")}
		text, err := hostprep.EgressTable(d)
		if err != nil {
			t.Fatal(err)
		}
		_, chain, _ := strings.Cut(text, "chain output {\n")
		var rules []string
		for _, l := range strings.Split(chain, "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") && !strings.HasPrefix(l, "type ") {
				rules = append(rules, l)
			}
		}
		if len(rules) == 0 || rules[0] != "ct direction reply accept" {
			t.Errorf("gateway %v: the output chain does not accept replies first:\n%s", gateway, text)
		}
		if strings.Count(text, "ct direction reply accept") != 1 {
			t.Errorf("gateway %v: replies are accepted outside the output chain:\n%s", gateway, text)
		}
	}
}

// An on-link prefix reaches the table only as a valid, masked IPv6 prefix;
// anything else is refused rather than rendered.
func TestEgressTableRefusesABadOnLinkPrefix(t *testing.T) {
	for _, bad := range []netip.Prefix{{}, netip.MustParsePrefix("192.168.1.0/24"), netip.MustParsePrefix("2001:db8:1::5/64"), netip.MustParsePrefix("::ffff:10.0.0.0/104")} {
		d := egress
		d.LAN6 = []netip.Prefix{bad}
		if text, err := hostprep.EgressTable(d); err == nil {
			t.Errorf("%v was rendered:\n%s", bad, text)
		}
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
// and without --mesh-lan, an IPv6 resolver, the gateway's accept and the
// on-link prefixes.
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
	lan.Gateway = true
	lan.LAN6 = []netip.Prefix{netip.MustParsePrefix("2001:db8:1::/64"), netip.MustParsePrefix("2001:db8:2::/56")}
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
