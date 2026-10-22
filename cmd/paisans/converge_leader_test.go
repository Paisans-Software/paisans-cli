package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// A step whose apply moved Patroni's leader reports it under its install
// line, which shows only with -v. The run says it again once its steps are
// done, as a warning of its own, at every verbosity, and says it whether
// the run went on to finish or stopped at a later step.
func TestConvergeSaysTheLeaderMoved(t *testing.T) {
	for _, stop := range []bool{false, true} {
		var fail map[string]error
		if stop {
			fail = map[string]error{"storage add": errors.New("the layout did not apply")}
		}
		fakeConverge(t, fail)
		rec := recordConverge(t)
		savedRun := convergeRun
		moved := false
		convergeRun = func(args []string) error {
			if isApply(args) && siteOf(args) == "home-a" && !moved {
				moved = true
				noteLeaderMove(apply.LeaderMove{From: "home-a", To: "home-b", Reason: "this apply ran `recreate infra` on home-a"})
			}
			return savedRun(args)
		}
		err := converge(t, "--execute")
		convergeRun = savedRun
		if (err != nil) != stop {
			t.Fatalf("stop %t: %v", stop, err)
		}
		var warned []ui.Event
		for _, e := range rec.Events {
			if e.Kind == "warn" && strings.Contains(e.Text, "leader moved") {
				warned = append(warned, e)
			}
		}
		if len(warned) != 1 {
			t.Fatalf("stop %t: want one warning of the move, got %d:\n%s", stop, len(warned), rec.Lines())
		}
		if w := warned[0]; w.Text != "Patroni's leader moved from home-a to home-b" || !strings.Contains(w.Extra, "recreate infra") {
			t.Errorf("stop %t: warned %q: %q", stop, w.Text, w.Extra)
		}
	}
}

// A run in which no apply moved the leader says nothing about it.
func TestConvergeSaysNothingOfALeaderThatStayed(t *testing.T) {
	fakeConverge(t, nil)
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if rec.Has("warn", "leader moved") {
		t.Errorf("a move was reported:\n%s", rec.Lines())
	}
}
