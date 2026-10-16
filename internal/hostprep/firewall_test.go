package hostprep_test

import (
	"testing"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

// An owner's allow that matches a derived rule but carries no paisans
// comment is theirs: it is reported present and nothing rewrites it, and an
// inactive ufw is neither enabled nor given a default policy here.
func TestAnOwnersMatchingAllowIsLeftAsItIs(t *testing.T) {
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		t.Fatal(err)
	}
	d := deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
	h := &fakeHost{files: map[string]string{}, responses: map[string]string{
		"ufw show added": "ufw present\nstatus inactive\nrule allow 22/tcp\n",
	}}
	s, err := profile.Firewall(h, d, []hostprep.Rule{{Port: 22, Proto: "tcp", SSH: true, Why: "ssh, the bootstrap route"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Steps) != 0 {
		t.Errorf("steps for the owner's allow: %+v", s.Steps)
	}
}
