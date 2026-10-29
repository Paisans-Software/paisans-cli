package main

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/lease"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// guardEvents records, in order, each lease taken and each site checked.
func guardEvents(t *testing.T, newer map[string]string) *[]string {
	t.Helper()
	var events []string
	savedTake := leaseTake
	leaseTake = func(sites map[string]lease.Runner, d deployment.Deployment, h lease.Holder, takeover bool) (*lease.Lease, map[string]error, error) {
		events = append(events, "lease")
		return savedTake(sites, d, h, takeover)
	}
	appliedByNewer(t, newer)
	savedCheck := toolkitCheck
	toolkitCheck = func(site string, h apply.Transport, d deployment.Deployment) error {
		events = append(events, "check "+site)
		return savedCheck(site, h, d)
	}
	t.Cleanup(func() { leaseTake, toolkitCheck = savedTake, savedCheck })
	return &events
}

// noTransport is the host of a site whose check is faked, which reaches none.
func noTransport(string) apply.Transport { return nil }

// With --execute, a command's sites are checked once it holds the lease,
// so nothing can land between the check and its last write, and the lease
// is released when the check refuses.
func TestGuardSitesChecksUnderTheLease(t *testing.T) {
	cfg := fixtureCfg(t)
	events := guardEvents(t, nil)
	release, err := guardSites(&ui.Recorder{}, cfg, []string{"home-a"}, noTransport, "prune --site home-a", guard{execute: true})
	if err != nil {
		t.Fatal(problemText(err))
	}
	if strings.Join(*events, ", ") != "lease, check home-a" {
		t.Errorf("events %v, want the lease taken before the check", *events)
	}
	if runLease == nil {
		t.Fatal("the lease is not held after the guard")
	}
	release()
	if runLease != nil {
		t.Error("release left the lease held")
	}

	*events = nil
	events = guardEvents(t, map[string]string{"home-a": "99.0.0"})
	if _, err := guardSites(&ui.Recorder{}, cfg, []string{"home-a"}, noTransport, "prune --site home-a", guard{execute: true}); err == nil {
		t.Fatal("a site a newer paisans last applied passed the guard")
	}
	if runLease != nil {
		t.Error("a refused guard left the lease held")
	}
}

// A dry run takes no lease and is still checked.
func TestGuardSitesDryRunTakesNoLease(t *testing.T) {
	events := guardEvents(t, nil)
	release, err := guardSites(&ui.Recorder{}, fixtureCfg(t), []string{"home-a", "home-b"}, noTransport, "storage add", guard{})
	if err != nil {
		t.Fatal(problemText(err))
	}
	release()
	if strings.Join(*events, ", ") != "check home-a, check home-b" {
		t.Errorf("events %v, want both sites checked and no lease", *events)
	}
}

// A command run inside one holding the lease, as a rotation runs
// storage rotate-key, takes no lease of its own and checks nothing again.
func TestGuardSitesInsideAHolderDoesNothing(t *testing.T) {
	cfg := fixtureCfg(t)
	events := guardEvents(t, nil)
	outer, err := guardSites(&ui.Recorder{}, cfg, []string{"home-a"}, noTransport, "secrets rotate", guard{execute: true})
	if err != nil {
		t.Fatal(problemText(err))
	}
	defer outer()
	*events = nil
	inner, err := guardSites(&ui.Recorder{}, cfg, []string{"home-a"}, noTransport, "storage rotate-key --app talk", guard{execute: true})
	if err != nil {
		t.Fatal(problemText(err))
	}
	inner()
	if len(*events) != 0 {
		t.Errorf("the inner command did %v", *events)
	}
	if runLease == nil {
		t.Error("the inner command's release let go of the outer command's lease")
	}
}

// Another command's live lease refuses a guarded command before it checks
// or changes anything, naming who holds it.
func TestGuardSitesIsRefusedWhileAnotherCommandHoldsTheLease(t *testing.T) {
	events := guardEvents(t, nil)
	heldBy(t, "bob@desk")
	_, err := guardSites(&ui.Recorder{}, fixtureCfg(t), []string{"home-a"}, noTransport, "mesh rekey", guard{execute: true})
	if err == nil || !strings.Contains(problemText(err), "bob@desk") {
		t.Fatalf("err = %v", err)
	}
	for _, e := range *events {
		if strings.HasPrefix(e, "check") {
			t.Errorf("checked a site with the lease refused: %v", *events)
		}
	}
}
