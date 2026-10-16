package hostadmit_test

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

// home is a host on a home LAN, as one admitted with --mesh-lan is.
func home() *fakeHost { return current().withUser().with("lan", "192.168.1.20/24") }

const (
	egressFile = "/etc/paisans/egress.f2a9.nft"
	egressUnit = "/etc/systemd/system/paisans-f2a9-egress.service"
)

// egressAdmitted is home with vm's egress table and unit written as admit
// writes them, the unit enabled and the table loaded; table, when not "",
// stands in for the table's file.
func egressAdmitted(table string) func() *fakeHost {
	return func() *fakeHost {
		want, err := hostprep.EgressTable(hostprep.EgressData{Token: "f2a9", Table: "paisans_f2a9", Mark: "0x5053f2a9",
			Users: "1001, 165536-231071", Mesh: requestFor("vm").MeshSubnet, Resolvers4: []string{"127.0.0.53"},
			LAN6: []netip.Prefix{netip.MustParsePrefix("2001:db8:1::/64")}})
		if err != nil {
			panic(err)
		}
		unit, err := hostprep.EgressUnit(hostprep.EgressUnitData{Token: "f2a9", User: "paisans-f2a9", Table: "paisans_f2a9", File: egressFile, UID: "1001"})
		if err != nil {
			panic(err)
		}
		if table == "" {
			table = want
		}
		return home().withFile(egressFile, table, "644", 0).withFile(egressUnit, unit, "644", 0).
			with("egress-enabled", "yes").with("egress-table", "present")
	}
}

var egressCases = addCases(
	goldenCase{name: "egress-fresh-user", host: current, site: "vm"},
	goldenCase{name: "egress-without-mesh-lan", host: home, site: "vm"},
	goldenCase{name: "egress-with-mesh-lan", host: home, site: "vm", flags: hostadmit.Flags{MeshLAN: true}},
	goldenCase{name: "egress-mesh-lan-public-refused", site: "vm", flags: hostadmit.Flags{MeshLAN: true}, host: func() *fakeHost { return current().withUser() }},
	goldenCase{name: "egress-mesh-lan-wide-refused", site: "vm", flags: hostadmit.Flags{MeshLAN: true}, host: func() *fakeHost { return home().with("lan", "10.20.30.40/8") }},
	goldenCase{name: "egress-mesh-lan-no-route-refused", site: "vm", flags: hostadmit.Flags{MeshLAN: true}, host: func() *fakeHost {
		h := home().with("default-dev", "absent")
		var kept [][2]string
		for _, kv := range h.facts {
			if kv[0] != "lan" {
				kept = append(kept, kv)
			}
		}
		h.facts = kept
		return h
	}},
	goldenCase{name: "egress-lan-resolver", site: "vm", host: func() *fakeHost {
		return home().file("/etc/resolv.conf", "nameserver 192.168.1.1\nnameserver fd00::1\nnameserver fe80::1%eth0\nnameserver 192.168.1.1\n")
	}},
	goldenCase{name: "egress-no-resolver", site: "vm", host: func() *fakeHost { return home().file("/etc/resolv.conf", "options edns0\n") }},
	goldenCase{name: "egress-admitted", site: "vm", flags: hostadmit.Flags{MeshLAN: true}, host: func() *fakeHost {
		return admittedHost(requestFor("vm"), hostadmit.Flags{MeshLAN: true})
	}},
	goldenCase{name: "egress-table-changed", host: egressAdmitted("table inet paisans_f2a9 {}\n"), site: "vm"},
	goldenCase{name: "egress-table-unloaded", site: "vm", host: func() *fakeHost { return egressAdmitted("")().with("egress-table", "absent") }},
	goldenCase{name: "egress-mesh-lan-added", site: "vm", flags: hostadmit.Flags{MeshLAN: true}, host: egressAdmitted("")},
	goldenCase{name: "egress-gateway", host: home, site: "home-a", flags: gateway},
	goldenCase{name: "egress-onlink6", site: "vm", host: func() *fakeHost {
		return home().add("lan6", "2001:db8:9::/56 eth1").add("lan6", "2001:db8:9:1::/64 eth1").add("lan6", "2001:db8:3::/64 wg0").
			add("lan6", "2001:db8:5::/64 docker0").add("lan6", "2001:db8:4::/64 psns-f2a9").add("lan6", "2001:db8:7::/64 lo").add("lan6", "fd00:1::/64 eth0")
	}},
	goldenCase{name: "egress-onlink6-changed", site: "vm", host: func() *fakeHost {
		return admittedHost(requestFor("vm"), hostadmit.Flags{}).with("lan6", "2001:db8:42::/64 eth0")
	}},
	goldenCase{name: "egress-file-link-refused", site: "vm", host: func() *fakeHost {
		h := home()
		h.dropKeyed("file", egressFile)
		return h.add("file", egressFile+" link")
	}},
)

func TestEgressPlans(t *testing.T) { runGolden(t, egressCases) }

// tableOf is the egress table a plan writes, "" when it writes none.
func tableOf(plan *hostadmit.Plan) string {
	for _, s := range plan.Steps {
		if s.File != nil && s.File.Path == egressFile {
			return s.File.Content
		}
	}
	return ""
}

// The deployment reaches 80 and 443 on the host's own addresses only on a
// site whose own Caddy holds them (the public ports grant) or whose
// owner's web server fronts it (web_server external).
func TestEgressGatewayAccept(t *testing.T) {
	for _, c := range []struct {
		site    string
		flags   hostadmit.Flags
		gateway bool
	}{{"vm", hostadmit.Flags{}, false}, {"home-b", dataSite, false}, {"home-a", gateway, true}, {"watch", hostadmit.Flags{PublicPorts: true}, true}, {"status", hostadmit.Flags{}, true}} {
		h := home()
		if c.site == "home-b" {
			h = communityHome()
		}
		plan, err := hostadmit.Build(h, input(t, c.site, c.flags))
		if err != nil {
			t.Fatalf("%s: %v", c.site, err)
		}
		table := tableOf(plan)
		if table == "" {
			t.Fatalf("%s: no table written:\n%s", c.site, plan.Text())
		}
		if got := strings.Contains(table, "fib daddr type local tcp dport { 80, 443 } accept"); got != c.gateway {
			t.Errorf("%s: the gateway accept is there %v, want %v:\n%s", c.site, got, c.gateway, table)
		}
		if !strings.Contains(table, "ip6 daddr { 2001:db8:1::/64 } drop") {
			t.Errorf("%s: the host's on-link prefix is not dropped:\n%s", c.site, table)
		}
	}
}

// A changed on-link prefix is a changed table: the re-run writes it and
// loads it again in place, and nothing else.
func TestEgressOnLinkPrefixChangeReplansTheTable(t *testing.T) {
	h := admittedHost(requestFor("vm"), hostadmit.Flags{}).with("lan6", "2001:db8:42::/64 eth0")
	plan, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, s := range plan.Steps {
		titles = append(titles, s.Title)
	}
	if strings.Join(titles, ", ") != "write the egress table, reload the egress table" {
		t.Errorf("a new on-link prefix plans %q", titles)
	}
	if table := tableOf(plan); !strings.Contains(table, "ip6 daddr { 2001:db8:42::/64 } drop") || strings.Contains(table, "2001:db8:1::") {
		t.Errorf("the table does not drop the new prefix alone:\n%s", table)
	}
}
