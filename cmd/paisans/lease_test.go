package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/lease"
)

// heldBy makes every site hold another command's live lease, which only a
// takeover replaces, and records each take.
func heldBy(t *testing.T, who string) *[]bool {
	t.Helper()
	var takes []bool
	saved := leaseTake
	leaseTake = func(sites map[string]lease.Runner, d deployment.Deployment, h lease.Holder, takeover bool) (*lease.Lease, map[string]error, error) {
		takes = append(takes, takeover)
		if !takeover {
			return nil, nil, &lease.HeldError{Other: lease.Other{Site: "home-a", Who: who, Command: "deployment apply"}}
		}
		return saved(sites, d, h, takeover)
	}
	t.Cleanup(func() { leaseTake = saved })
	return &takes
}

// An apply finding another command's live lease is refused before its
// gather, its review or any step, naming who holds it.
func TestApplyIsRefusedWhileAnotherCommandHoldsTheLease(t *testing.T) {
	ran := fakeConverge(t, nil)
	heldBy(t, "bob@desk")
	err := converge(t, "--execute")
	if err == nil || !strings.Contains(err.Error(), "another command is changing this deployment") || !strings.Contains(err.Error(), "deployment apply by bob@desk holds the deployment lease") || !strings.Contains(err.Error(), "--break-lease") {
		t.Fatalf("err = %v", err)
	}
	if len(*ran) != 0 {
		t.Errorf("a refused apply ran %v", *ran)
	}
}

// A dry run changes nothing, so it takes no lease and is not refused by one.
func TestADryRunTakesNoLease(t *testing.T) {
	fakeConverge(t, nil)
	takes := heldBy(t, "bob@desk")
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	if len(*takes) != 0 {
		t.Errorf("a dry run took the lease %d times", len(*takes))
	}
}

// --break-lease asks for the word and takes the lease over; --yes types it.
func TestBreakLeaseTakesItOver(t *testing.T) {
	ran := fakeConverge(t, nil)
	takes := heldBy(t, "bob@desk")
	if err := converge(t, "--execute", "--break-lease"); err == nil || !strings.Contains(err.Error(), "asks for the word break") {
		t.Fatalf("a takeover with no terminal gives %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("an unanswered takeover ran %v", *ran)
	}
	allowYes(t)
	*takes = nil
	if err := converge(t, "--execute", "--break-lease", "--yes"); err != nil {
		t.Fatal(problemText(err))
	}
	if len(*takes) != 2 || (*takes)[0] || !(*takes)[1] {
		t.Errorf("takes %v, want a refused take then a takeover", *takes)
	}
	if len(*ran) == 0 {
		t.Error("the takeover ran no step")
	}
	if runLease != nil {
		t.Error("the run left its lease held")
	}
}

// A step apply runs as part of the run takes no lease of its own: the run
// holds it.
func TestAStepOfTheRunTakesNoLeaseOfItsOwn(t *testing.T) {
	fakeConverge(t, nil)
	var takes int
	saved := leaseTake
	leaseTake = func(sites map[string]lease.Runner, d deployment.Deployment, h lease.Holder, takeover bool) (*lease.Lease, map[string]error, error) {
		takes++
		return saved(sites, d, h, takeover)
	}
	t.Cleanup(func() { leaseTake = saved })
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(problemText(err))
	}
	if takes != 1 {
		t.Errorf("the run took the lease %d times, want once", takes)
	}
}

// publish holds the lease too: beside another command's live lease it is
// refused before it gathers, and pushes nothing.
func TestPublishIsRefusedWhileAnotherCommandHoldsTheLease(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	f.editDraft(t, "# next\n")
	heldBy(t, "bob@desk")
	var out bytes.Buffer
	var err error
	captureStdout(t, func() {
		err = runDeployment([]string{"publish", "--config", f.cfgPath, "--execute"}, noTerminalStdin(), &out)
	})
	if err == nil || !strings.Contains(err.Error(), "another command is changing this deployment") {
		t.Fatalf("err = %v", err)
	}
	if h, _ := f.sys.Head(); h != f.shas[0] || f.pushed() {
		t.Fatalf("a refused publish moved the head to %s, pushed %v", h, f.pushed())
	}
}
