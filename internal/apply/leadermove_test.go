package apply_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/render"
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

const patroniEnvPath = "/srv/paisans/f2a9/infra/patroni.env"

// withPatroniEnv is the fixture's render with home-a's patroni.env passed
// through edit.
func withPatroniEnv(t *testing.T, edit func(string) string) *render.Plan {
	t.Helper()
	changed := plan(t)
	for i, file := range changed.Files {
		if file.Path == "home-a"+patroniEnvPath {
			changed.Files[i].Content = edit(file.Content)
		}
	}
	return changed
}

func moreEtcdHosts(content string) string {
	return strings.Replace(content, "ETCD3_HOSTS=10.44.0.1:2379,10.44.0.2:2379,10.44.0.3:2379", "ETCD3_HOSTS=10.44.0.1:2379,10.44.0.2:2379,10.44.0.3:2379,10.44.0.4:2379", 1)
}

// A join changes ETCD3_HOSTS in every data site's patroni.env, and nothing
// else in it. Patroni reads that list only to reach etcd as it starts and
// asks etcd for its members from then on, so the file is written and
// recorded and Patroni is left running: recreating it on the leader would
// hand the leader key to a standby. The next apply finds nothing to do.
func TestAnEtcdHostsChangeAloneLeavesPatroniRunning(t *testing.T) {
	noWait(t)
	host := applied(t, "home-a")
	changed := withPatroniEnv(t, moreEtcdHosts)
	if got := renderedPatroniEnv(changed); !strings.Contains(got, "10.44.0.4:2379") {
		t.Fatalf("the fixture's ETCD3_HOSTS did not change:\n%s", got)
	}
	p, err := apply.Build("home-a", changed, acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	var written bool
	for _, c := range p.Writes() {
		written = written || (c.Path == patroniEnvPath && c.Kind == apply.Update)
	}
	if !written {
		t.Fatalf("patroni.env is not written: %v", p.Writes())
	}
	for _, a := range p.Actions {
		if a.Stack == "infra" {
			t.Errorf("an ETCD3_HOSTS change alone plans %+v on the infrastructure stack", a)
		}
	}
	if p.GatewayChanging {
		t.Error("an ETCD3_HOSTS change alone runs the gateway's gates")
	}
	var noted bool
	for _, n := range p.Notes {
		noted = noted || (n.Hint == "" && strings.Contains(n.Text, "ETCD3_HOSTS") && strings.Contains(n.Text, "not recreated"))
	}
	if !noted {
		t.Errorf("the plan does not say why Patroni is left running: %+v", p.Notes)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if host.files[patroniEnvPath] != renderedPatroniEnv(changed) {
		t.Error("patroni.env on the host is not the rendered one")
	}
	if host.ran("/srv/paisans/f2a9/infra/compose.yaml up -d") || host.ran("/srv/paisans/f2a9/infra/compose.yaml restart") {
		t.Errorf("the infrastructure stack moved: %v", host.commands)
	}
	again, err := apply.Build("home-a", changed, acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Writes()) != 0 || len(again.Actions) != 0 {
		t.Errorf("the next apply plans %v and %v", again.Writes(), again.Actions)
	}
}

// Any other change to patroni.env still needs Patroni recreated, since
// Spilo renders Patroni's configuration from it only as the container
// starts. The plan says, at every verbosity, that this can move the leader.
func TestAnotherPatroniEnvChangeRecreatesAndWarns(t *testing.T) {
	host := applied(t, "home-a")
	changed := withPatroniEnv(t, func(c string) string { return moreEtcdHosts(c) + "# a later change\n" })
	p, err := apply.Build("home-a", changed, acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 1 || p.Actions[0].Stack != "infra" || !p.Actions[0].Recreate {
		t.Fatalf("plans %+v, want the infra stack recreated", p.Actions)
	}
	var warned bool
	for _, n := range p.Notes {
		warned = warned || strings.Contains(n.Hint, "recreates Patroni on home-a")
	}
	if !warned {
		t.Errorf("the plan does not warn that Patroni is recreated: %+v", p.Notes)
	}
}

// A first apply creates patroni.env with no Patroni running to lose a
// leader, and says nothing about one.
func TestAFirstApplySaysNothingOfRecreatingPatroni(t *testing.T) {
	p, err := apply.Build("home-a", plan(t), acmeModule(t), newHost())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range p.Notes {
		if strings.Contains(n.Hint, "Patroni") || strings.Contains(n.Text, "ETCD3_HOSTS") {
			t.Errorf("a first apply notes %+v", n)
		}
	}
}

func renderedPatroniEnv(p *render.Plan) string {
	for _, f := range p.Files {
		if f.Path == "home-a"+patroniEnvPath {
			return f.Content
		}
	}
	return ""
}
