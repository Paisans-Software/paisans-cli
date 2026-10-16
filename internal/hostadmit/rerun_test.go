package hostadmit_test

import (
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

// The site no longer needs the public ports: the re-run takes 80 and 443
// out of the firewall, drops the low ports from the shared file, restores
// the value recorded before, and writes the record without the grant.
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
