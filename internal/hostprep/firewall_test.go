package hostprep_test

import (
	"strings"
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

// relay is the route rule a relay's admit derives: forwarded traffic in on
// the mesh interface and back out of it, and nothing else.
var relay = hostprep.Rule{Interface: "psns-f2a9", Route: true, Why: "the mesh, relayed between sites that cannot dial each other"}

const relayLine = "route allow in on psns-f2a9 out on psns-f2a9 comment 'paisans-f2a9: the mesh, relayed between sites that cannot dial each other'"

func firewallPlan(t *testing.T, added string, rules []hostprep.Rule) hostprep.Section {
	t.Helper()
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		t.Fatal(err)
	}
	d := deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
	h := &fakeHost{files: map[string]string{}, responses: map[string]string{
		"ufw show added": "ufw present\nstatus active\n" + added,
	}}
	s, err := profile.Firewall(h, d, rules)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A relay with no route rule yet is given one, as `ufw route allow`, since
// a forward policy of DROP, ufw's default, otherwise drops what it relays.
func TestARelayIsGivenItsRouteRule(t *testing.T) {
	s := firewallPlan(t, "", []hostprep.Rule{relay})
	if len(s.Steps) != 1 || s.Steps[0].Command != "ufw "+relayLine {
		t.Fatalf("steps %+v, want one running ufw %s", s.Steps, relayLine)
	}
}

// The route rule as ufw prints it back is recognised as the deployment's,
// so a second admit plans nothing for it.
func TestARelaysRouteRuleIsPresent(t *testing.T) {
	s := firewallPlan(t, "rule "+relayLine+"\n", []hostprep.Rule{relay})
	if len(s.Steps) != 0 || len(s.Present) != 1 || !strings.Contains(s.Present[0], "routed from psns-f2a9 back out psns-f2a9") {
		t.Errorf("steps %+v, present %q", s.Steps, s.Present)
	}
}

// A site that no longer relays, and evict, which derives nothing, remove
// the route rule with `ufw route delete`: ufw refuses `ufw delete route`.
func TestARouteRuleNoLongerDerivedIsRemoved(t *testing.T) {
	s := firewallPlan(t, "rule "+relayLine+"\n", nil)
	want := "ufw route delete allow in on psns-f2a9 out on psns-f2a9 comment 'paisans-f2a9: the mesh, relayed between sites that cannot dial each other'"
	if len(s.Steps) != 1 || s.Steps[0].Label != "remove" || s.Steps[0].Command != want {
		t.Errorf("steps %+v, want one removal running %s", s.Steps, want)
	}
}

// An owner's route rule is theirs: it is neither removed nor rewritten.
func TestAnOwnersRouteRuleIsLeftAlone(t *testing.T) {
	s := firewallPlan(t, "rule route allow in on docker0 out on eth0\n", nil)
	if len(s.Steps) != 0 {
		t.Errorf("steps for the owner's route rule: %+v", s.Steps)
	}
}
