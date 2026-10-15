package siteremove_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/siteremove"
)

const seed = root + "/health/monitors.json"

// The last stage reseeds the monitor from the end state: once home-b is
// out, watch's monitors.json no longer names it, and the monitor restarts
// on the new file, so the fork deletes home-b's managed monitors. The seed
// is no longer among the files the report says an apply owes.
func TestTheRemovalReseedsTheMonitorLast(t *testing.T) {
	w := setup(t)
	if !strings.Contains(w.hosts["watch"].files[seed], `"home-b — ping"`) {
		t.Fatal("the monitor does not ping home-b before the removal")
	}
	p := w.mustBuild("home-b", siteremove.Options{})
	last := p.Stages[len(p.Stages)-1]
	if last.Name != "monitor" || !hasStep(p, last.Number, "watch", "update", "health/monitors.json") || !hasStep(p, last.Number, "watch", "restart", "health") {
		t.Fatalf("the last stage is not the monitor's reseed:\n%v", last.Steps)
	}
	if remains := strings.Join(p.Remains(), "\n"); strings.Contains(remains, "monitors.json") {
		t.Errorf("the seed is still reported as owed to an apply:\n%s", remains)
	}
	if err := siteremove.Execute(p); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.hosts["watch"].files[seed], "home-b") {
		t.Errorf("the monitor still names home-b:\n%s", w.hosts["watch"].files[seed])
	}
	if w.hosts["watch"].ran(root+"/health/compose.yaml restart") != 1 {
		t.Error("the monitor was not restarted on the new seed")
	}
}

// A reseed that fails stops at its stage, after the site is out of the
// cluster and the configuration, and names the apply that finishes it: site
// remove cannot be run again for a site the configuration no longer declares.
func TestAFailedReseedNamesTheApply(t *testing.T) {
	w := setup(t)
	w.failOnce = root + "/health/compose.yaml restart"
	err := siteremove.Execute(w.mustBuild("home-b", siteremove.Options{}))
	if err == nil || !strings.Contains(err.Error(), "stage 6") || !strings.Contains(err.Error(), "paisans apply --site watch --only health --execute") {
		t.Fatalf("want a stage 6 failure naming the apply, got %v", err)
	}
	if strings.Contains(err.Error(), "run site remove again") {
		t.Errorf("the failure says to run site remove again, which no longer declares the site: %v", err)
	}
	for _, m := range w.etcd {
		if m.Name == "home-b" {
			t.Error("home-b is still an etcd member")
		}
	}
}

// A monitor that does not come back healthy stops at its stage with the
// cause read from its logs, the logs themselves only with -v, and the apply
// that finishes it.
func TestAnUnhealthyMonitorNamesItsCauseAndTheApply(t *testing.T) {
	w := setup(t)
	w.unhealthy = "health"
	err := siteremove.Execute(w.mustBuild("home-b", siteremove.Options{}))
	if err == nil {
		t.Fatal("an unhealthy monitor passed")
	}
	shortAndVerbose(t, err, "site remove home-b stopped at stage 6 (monitor)", "stack health is not healthy", "The removal itself is done", "paisans apply --site watch --only health --execute")
	if strings.Contains(err.Error(), "run site remove again") {
		t.Errorf("the failure says to run site remove again, which no longer declares the site: %v", err)
	}
}

// An app that does not come back healthy after HAProxy's restart stops the
// removal at stage 2 with the cause read from its logs, the logs themselves
// only with -v, and how to resume.
func TestAnAppUnhealthyAfterHAProxyNamesItsCause(t *testing.T) {
	w := setup(t)
	w.unhealthy = "talk"
	err := siteremove.Execute(w.mustBuild("home-b", siteremove.Options{}))
	if err == nil {
		t.Fatal("an unhealthy app passed")
	}
	shortAndVerbose(t, err, "site remove home-b stopped at stage 2 (out of the cluster)", "stack talk is not healthy", "run site remove again")
}

// A deployment with no monitor site skips the stage. The monitor site itself
// cannot be the one removed: its uptime app is pinned to it, which is refused.
func TestNoMonitorSiteSkipsTheReseed(t *testing.T) {
	w := newWorld(t, func(text string) string {
		text = regexp.MustCompile(`(?s)\n  # The monitor's own machine.*?\n\ncluster:`).ReplaceAllString(text, "\n\ncluster:")
		return regexp.MustCompile(`(?s)\n  health:\n    blueprint: uptime.*?\n\n# The deployment's mail server`).ReplaceAllString(text, "\n\n# The deployment's mail server")
	})
	t.Cleanup(siteremove.SetFast())
	t.Cleanup(siteremove.SetInspect(w.inspect))
	if _, ok := w.cfg.Sites["watch"]; ok || len(w.cfg.MonitorSites()) != 0 {
		t.Fatal("the edit left the monitor site in place")
	}
	p := w.mustBuild("home-b", siteremove.Options{})
	last := p.Stages[len(p.Stages)-1]
	if last.Name != "monitor" || last.Skipped == "" {
		t.Fatalf("the stage is not skipped: %+v", last)
	}
	if err := siteremove.Execute(p); err != nil {
		t.Fatal(err)
	}
}
