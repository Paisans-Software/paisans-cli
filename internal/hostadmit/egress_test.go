package hostadmit_test

import (
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
			Users: "1001, 165536-231071", Mesh: requestFor("vm").MeshSubnet, Resolvers4: []string{"127.0.0.53"}})
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
	goldenCase{name: "egress-admitted", host: egressAdmitted(""), site: "vm"},
	goldenCase{name: "egress-table-changed", host: egressAdmitted("table inet paisans_f2a9 {}\n"), site: "vm"},
	goldenCase{name: "egress-table-unloaded", site: "vm", host: func() *fakeHost { return egressAdmitted("")().with("egress-table", "absent") }},
	goldenCase{name: "egress-mesh-lan-added", site: "vm", flags: hostadmit.Flags{MeshLAN: true}, host: egressAdmitted("")},
	goldenCase{name: "egress-file-link-refused", site: "vm", host: func() *fakeHost {
		h := home()
		h.dropKeyed("file", egressFile)
		return h.add("file", egressFile+" link")
	}},
)

func TestEgressPlans(t *testing.T) { runGolden(t, egressCases) }
