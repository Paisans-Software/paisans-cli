package siteadd_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/siteadd"
)

const seed = "/srv/paisans/f2a9/health/monitors.json"

// The last stage reseeds the monitor: once home-b has joined, watch's
// monitors.json names it, and the monitor is restarted on the new file. A
// second build has nothing left to do there either.
func TestTheJoinReseedsTheMonitorLast(t *testing.T) {
	defer siteadd.SetFast()()
	w := newWorld(t)
	if strings.Contains(w.hosts["watch"].files[seed], "home-b") {
		t.Fatal("the monitor already knows home-b before the join")
	}
	p := build(t, w)
	last := p.Stages[len(p.Stages)-1]
	if last.Name != "monitor" || !hasStep(p, last.Number, "watch", "update") || !hasStep(p, last.Number, "watch", "restart") {
		t.Fatalf("the last stage is not the monitor's reseed:\n%v", last.Steps)
	}
	if err := siteadd.Execute(p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.hosts["watch"].files[seed], `"home-b — ping"`) {
		t.Errorf("the monitor was not reseeded with home-b:\n%s", w.hosts["watch"].files[seed])
	}
	if w.hosts["watch"].ran("/srv/paisans/f2a9/health/compose.yaml restart") != 1 {
		t.Error("the monitor was not restarted on the new seed")
	}
	again := build(t, w)
	if n := len(steps(again, last.Number)); n != 0 {
		t.Errorf("a finished join still plans %d monitor step(s): %v", n, steps(again, last.Number))
	}
	if err := siteadd.Execute(again); err != nil {
		t.Errorf("re-running a finished join fails the monitor's gate: %v", err)
	}
}

// A reseed that fails stops the join at its stage with the apply that
// finishes it by hand, and nothing before it is undone: the site is joined.
func TestAFailedReseedNamesTheApplyAndKeepsTheJoin(t *testing.T) {
	defer siteadd.SetFast()()
	w := newWorld(t)
	w.failOnce = "/srv/paisans/f2a9/health/compose.yaml restart"
	err := siteadd.Execute(build(t, w))
	if err == nil || !strings.Contains(err.Error(), "stage 8") || !strings.Contains(err.Error(), "paisans deployment apply --site watch --only health --execute") {
		t.Fatalf("want a stage 8 failure naming the apply, got %v", err)
	}
	if got := strings.Join(w.voters(), ","); got != "home-a,home-b,vm" {
		t.Errorf("the join was undone: voters %s", got)
	}
}

// A monitor that does not come back healthy stops the join at its stage with
// the cause read from its logs, the logs themselves only with -v, and the
// apply that finishes it by hand.
func TestAnUnhealthyMonitorNamesItsCauseAndTheApply(t *testing.T) {
	defer siteadd.SetFast()()
	w := newWorld(t)
	w.unhealthy = "health"
	err := siteadd.Execute(build(t, w))
	if err == nil {
		t.Fatal("an unhealthy monitor passed")
	}
	shortAndVerbose(t, err, "stopped at stage 8 (monitor)", "stack health is not healthy", "The join itself is done", "paisans deployment apply --site watch --only health --execute")
}

// A deployment with no monitor site skips the stage.
func TestNoMonitorSiteSkipsTheReseed(t *testing.T) {
	defer siteadd.SetFast()()
	w := newWorld(t)
	delete(w.cfg.Sites, "watch")
	delete(w.cfg.Apps, "stats")
	delete(w.cfg.Apps, "health")
	delete(w.hosts, "watch")
	p := build(t, w)
	last := p.Stages[len(p.Stages)-1]
	if last.Name != "monitor" || len(last.Steps) != 0 || !strings.Contains(last.Gate, "no site holds the monitor role") {
		t.Fatalf("the stage is not skipped: %q %v", last.Gate, last.Steps)
	}
	if err := siteadd.Execute(p); err != nil {
		t.Fatal(err)
	}
}
