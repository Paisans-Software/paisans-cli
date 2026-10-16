package hostadmit_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

// A host admitted exactly as asked plans nothing: every step is idempotent,
// so the second run of the same admit finds everything present.
func TestASecondRunPlansNothing(t *testing.T) {
	for _, c := range []struct {
		site  string
		flags hostadmit.Flags
	}{{"vm", hostadmit.Flags{}}, {"home-a", gateway}, {"home-b", dataSite}, {"status", hostadmit.Flags{}}, {"store", hostadmit.Flags{}}, {"watch", hostadmit.Flags{PublicPorts: true}},
		{"vm", hostadmit.Flags{MeshLAN: true}}, {"vm", hostadmit.Flags{BridgeNetfilter: true}}} {
		h := admittedHost(requestFor(c.site), c.flags)
		plan, err := hostadmit.Build(h, input(t, c.site, c.flags))
		if err != nil {
			t.Fatalf("%s: %v", c.site, err)
		}
		if len(plan.Steps) != 0 {
			t.Errorf("%s: a second run plans:\n%s", c.site, plan.Text())
		}
	}
}

// The site no longer needs the public ports: the re-run drops the egress
// table's accept for 80 and 443 on the host, takes them out of the
// firewall, drops the low ports from the shared file, restores the value
// recorded before, and writes the record without the grant.
var rerunCases = addCases(goldenCase{name: "rerun-public-ports-removed", site: "watch", flags: hostadmit.Flags{PublicPorts: true}, host: func() *fakeHost {
	return admittedHost(requestFor("watch"), hostadmit.Flags{PublicPorts: true})
}})

func TestRerunPlans(t *testing.T) {
	c := rerunCases[0]
	req := requestFor("watch")
	req.Grants.PublicPorts = false
	in := hostadmit.Input{Request: req, Digest: admission.Digest(admission.Encode(req)), Now: admitted}
	plan, err := hostadmit.Build(c.host(), in)
	if err != nil {
		t.Fatal(err)
	}
	checkGoldenText(t, c.name, plan.Text())
}

// A record without mesh_subnet is refused like any record admit cannot
// read: the owner moves it aside and admits again.
func TestARerunRefusesARecordWithoutMeshSubnet(t *testing.T) {
	flags := hostadmit.Flags{}
	h := admittedHost(requestFor("vm"), flags)
	const path = "/etc/paisans/admitted.f2a9.json"
	current := h.files[path]
	old := strings.Replace(current, "  \"mesh_subnet\": \"10.44.0.0/24\",\n", "", 1)
	if old == current {
		t.Fatalf("the record has no mesh_subnet to remove:\n%s", current)
	}
	h.file(path, old).withFile(path, old, "644", 0)
	_, err := hostadmit.Build(h, input(t, "vm", flags))
	if err == nil || !strings.Contains(err.Error(), "mesh_subnet: missing") || !strings.Contains(err.Error(), "Move it aside and admit again") {
		t.Errorf("Build = %v", err)
	}
}
