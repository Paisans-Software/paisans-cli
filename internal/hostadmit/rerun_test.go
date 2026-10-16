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

// A record written before mesh_subnet joined it is rewritten when the owner
// admits again, rather than refused: the run plans that one write, of the
// record as admit writes it now.
func TestARerunRewritesARecordWithoutMeshSubnet(t *testing.T) {
	flags := hostadmit.Flags{}
	h := admittedHost(requestFor("vm"), flags)
	const path = "/etc/paisans/admitted.f2a9.json"
	current := h.files[path]
	old := strings.Replace(current, "  \"mesh_subnet\": \"10.44.0.0/24\",\n", "", 1)
	if old == current {
		t.Fatalf("the record has no mesh_subnet to remove:\n%s", current)
	}
	h.file(path, old).withFile(path, old, "644", 0)
	plan, err := hostadmit.Build(h, input(t, "vm", flags))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].File == nil || plan.Steps[0].File.Content != current {
		t.Errorf("the re-run plans:\n%s", plan.Text())
	}
}
