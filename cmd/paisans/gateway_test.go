package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// holdsGateway makes site's host answer that it still holds this
// deployment's gateway Caddy.
func holdsGateway(t *testing.T, site string, err error) {
	t.Helper()
	saved := convergeHoldsGateway
	convergeHoldsGateway = func(_ *config.Config, s string, _ bool) (bool, error) { return s == site, err }
	t.Cleanup(func() { convergeHoldsGateway = saved })
}

// A site that was the gateway and still runs its Caddy is moved from last,
// once every site, the new gateway included, has been applied.
func TestConvergeMovesTheGatewayLast(t *testing.T) {
	ran := fakeConverge(t, nil)
	holdsGateway(t, "home-a", nil)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	want := append(commands(blankPlan()), "gateway move")
	sameLines(t, *ran, want)
}

func TestConvergePlansNoMoveWithoutAFormerGateway(t *testing.T) {
	ran := fakeConverge(t, nil)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	for _, r := range *ran {
		if strings.HasPrefix(r, "gateway") {
			t.Errorf("planned %q with no former gateway", r)
		}
	}
}

// A site that cannot be asked stops the run before anything is applied:
// read as no gateway, an old one would go on running unnoticed.
func TestConvergeStopsWhenASiteCannotBeAskedForAGateway(t *testing.T) {
	ran := fakeConverge(t, nil)
	holdsGateway(t, "", errors.New("ssh: connect to host home-a.local: timed out"))
	if err := converge(t, "--execute"); err == nil {
		t.Fatal("want a stop")
	}
	if len(*ran) != 0 {
		t.Errorf("ran %v", *ran)
	}
}

// The dry run's gateway line waits on the new gateway's apply.
func TestConvergeGatewayMoveWaitsOnTheNewGatewaysApply(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	steps := convergePlan(cfg, convergeState{MoveGateway: true})
	last := len(steps) - 1
	if steps[last].Title != "gateway move" {
		t.Fatalf("last step %q", steps[last].Title)
	}
	waits := convergeWaits(cfg, steps, last)
	if strings.Join(waits, ", ") != "host prepare --site vm, apply --site vm" {
		t.Errorf("waits on %v", waits)
	}
}
