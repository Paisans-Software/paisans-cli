package apply_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// An apply that recreates the leader's Patroni lets a standby take the
// leader key. The apply says so at every verbosity, naming both sites and
// the action that did it, and hands the move to whoever watches for one.
func TestAnApplyThatMovesTheLeaderSaysSo(t *testing.T) {
	noWait(t)
	host := newHost()
	host.leaderAfter = "home-b"
	host.replicaHas = []string{"account", "auth", "talk", "wiki"}
	var watched []apply.LeaderMove
	t.Cleanup(apply.OnLeaderMove(func(m apply.LeaderMove) { watched = append(watched, m) }))
	p := bootstrapPlan(t, host)
	said := &ui.Recorder{}
	p.Report = said
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	m := p.LeaderMoved
	if m == nil {
		t.Fatalf("the move was not recorded:\n%s", said.Lines())
	}
	if m.From != "home-a" || m.To != "home-b" {
		t.Errorf("recorded %+v, want home-a to home-b", *m)
	}
	i := said.Index("warn", "Patroni's leader moved from home-a to home-b")
	if i < 0 {
		t.Fatalf("no warning names the move:\n%s", said.Lines())
	}
	for _, want := range []string{"recreate infra", "home-a", "patroni.env"} {
		if !strings.Contains(said.Events[i].Extra, want) {
			t.Errorf("the warning's detail does not say %q: %s", want, said.Events[i].Extra)
		}
	}
	if len(watched) != 1 || watched[0] != *m {
		t.Errorf("the watcher saw %+v", watched)
	}
}

// An apply after which the site still leads, or which ran on a replica,
// moved nothing and says nothing about the leader.
func TestAnApplyThatLeavesTheLeaderSaysNothing(t *testing.T) {
	for _, tc := range []struct{ name, before, after string }{
		{"leader stays", "home-a", ""},
		{"replica", "home-b", ""},
		{"the other site leads throughout", "home-b", "home-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noWait(t)
			host := newHost()
			host.leader, host.leaderAfter = tc.before, tc.after
			host.replicaHas = []string{"account", "auth", "talk", "wiki"}
			var watched []apply.LeaderMove
			t.Cleanup(apply.OnLeaderMove(func(m apply.LeaderMove) { watched = append(watched, m) }))
			p := bootstrapPlan(t, host)
			said := &ui.Recorder{}
			p.Report = said
			if err := apply.Execute(p, host); err != nil {
				t.Fatal(err)
			}
			if p.LeaderMoved != nil || said.Has("warn", "leader moved") || len(watched) != 0 {
				t.Errorf("a move was reported: %+v %v\n%s", p.LeaderMoved, watched, said.Lines())
			}
		})
	}
}
