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

const (
	patroniEnvPath = "/srv/paisans/f2a9/infra/patroni.env"
	// oldHosts is the fixture's ETCD3_HOSTS, which a Patroni created
	// before a join runs with.
	oldHosts = "10.44.0.1:2379,10.44.0.2:2379,10.44.0.3:2379"
)

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
	host.etcdHosts = oldHosts
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
	if !hinted(p, "runs with other etcd hosts than its patroni.env names") {
		t.Errorf("the plan does not say Patroni runs with other etcd hosts: %+v", p.Notes)
	}
	if hinted(p, "recreates Patroni") {
		t.Errorf("the plan warns of a recreate it does not make: %+v", p.Notes)
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
	host.etcdHosts = oldHosts
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

// hinted reports whether a note of the plan has a hint containing text.
func hinted(p *apply.Plan, text string) bool {
	for _, n := range p.Notes {
		if strings.Contains(n.Hint, text) {
			return true
		}
	}
	return false
}

const infraComposePath = "/srv/paisans/f2a9/infra/compose.yaml"

// withInfra is the fixture's render with home-a's patroni.env and its
// infrastructure compose file passed through env and compose.
func withInfra(t *testing.T, env, compose func(string) string) *render.Plan {
	t.Helper()
	changed := withPatroniEnv(t, env)
	for i, file := range changed.Files {
		if file.Path == "home-a"+infraComposePath {
			changed.Files[i].Content = compose(file.Content)
		}
	}
	return changed
}

// changeService changes service's definition in a compose file, with a
// field no rendered service sets.
func changeService(service string) func(string) string {
	return func(c string) string {
		return strings.Replace(c, "\n  "+service+":\n", "\n  "+service+":\n    stop_signal: SIGINT\n", 1)
	}
}

func same(c string) string { return c }

// An `up -d` of the infrastructure stack replaces Patroni whenever its
// patroni.env holds etcd hosts it was not created with, as Compose replaces
// a container whose env_file content changed. So an infrastructure change
// beside new etcd hosts, or a stopped service to start, names every service
// but a running Patroni, with --no-deps, and Patroni keeps running.
func TestAnInfraUpLeavesARunningPatroniOut(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stopped  string
		rendered func(t *testing.T) *render.Plan
	}{
		{"compose change beside new etcd hosts", "", func(t *testing.T) *render.Plan { return withInfra(t, moreEtcdHosts, changeService("haproxy")) }},
		{"stopped garage after new etcd hosts", "garage", func(t *testing.T) *render.Plan { return withPatroniEnv(t, same) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noWait(t)
			host := applied(t, "home-a")
			host.etcdHosts = oldHosts
			if tc.stopped != "" {
				// The etcd hosts were written by an earlier apply.
				hostsOnly := withPatroniEnv(t, moreEtcdHosts)
				first, err := apply.Build("home-a", hostsOnly, acmeModule(t), host)
				if err != nil {
					t.Fatal(err)
				}
				if err := apply.Execute(first, host); err != nil {
					t.Fatal(err)
				}
				host.stopped = map[string]string{"infra/" + tc.stopped: "exited"}
				host.commands = nil
			}
			rendered := tc.rendered(t)
			if tc.stopped != "" {
				rendered = withPatroniEnv(t, moreEtcdHosts)
			}
			p, err := apply.Build("home-a", rendered, acmeModule(t), host)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Actions) == 0 || p.Actions[0].Stack != "infra" || !p.Actions[0].Recreate {
				t.Fatalf("plans %+v, want the infra stack's up -d", p.Actions)
			}
			up := p.Actions[0].Command(apply.Fixture)
			if !strings.Contains(up, " up -d --no-deps ") || strings.Contains(up, "patroni") || !strings.Contains(up, "'haproxy'") {
				t.Errorf("the infra stack's up -d is %q, want every service but patroni", up)
			}
			if hinted(p, "recreates Patroni") {
				t.Errorf("the plan warns of a recreate it does not make: %+v", p.Notes)
			}
			if err := apply.Execute(p, host); err != nil {
				t.Fatal(err)
			}
			if !host.ran(up) {
				t.Errorf("did not run %q: %v", up, host.commands)
			}
		})
	}
}

// A change to the patroni service itself is one only a new container
// takes, so the `up -d` names every service and the plan warns.
func TestAPatroniServiceChangeRecreatesAndWarns(t *testing.T) {
	host := applied(t, "home-a")
	host.etcdHosts = oldHosts
	p, err := apply.Build("home-a", withInfra(t, moreEtcdHosts, changeService("patroni")), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) == 0 || p.Actions[0].Stack != "infra" || len(p.Actions[0].Up) != 0 || !p.Actions[0].Recreate {
		t.Fatalf("plans %+v, want the whole infra stack's up -d", p.Actions)
	}
	if !hinted(p, "recreates Patroni on home-a") {
		t.Errorf("the plan does not warn: %+v", p.Notes)
	}
}

// A Patroni whose etcd hosts still name a current member is left running,
// and the plan says it runs with other hosts. One whose hosts name no current
// member could not reach etcd after a restart of its container, so the plan
// recreates it alone, with the warning, even with nothing else to do.
func TestPatroniRunningWithOldEtcdHosts(t *testing.T) {
	t.Run("a current member left", func(t *testing.T) {
		host := applied(t, "home-a")
		host.etcdHosts = "10.44.0.1:2379,10.44.0.9:2379"
		p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Actions) != 0 {
			t.Errorf("plans %+v", p.Actions)
		}
		if !hinted(p, "runs with other etcd hosts") || hinted(p, "recreates Patroni") {
			t.Errorf("notes %+v", p.Notes)
		}
	})
	t.Run("no current member left", func(t *testing.T) {
		noWait(t)
		host := applied(t, "home-a")
		host.etcdHosts = "10.44.0.8:2379,10.44.0.9:2379"
		host.replicaHas = []string{"account", "auth", "talk", "wiki"}
		p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Actions) == 0 || p.Actions[0].Stack != "infra" {
			t.Fatalf("plans %+v, want Patroni recreated", p.Actions)
		}
		if got := p.Actions[0].Command(apply.Fixture); got != apply.RecreatePatroni(apply.Fixture) {
			t.Errorf("recreates with %q", got)
		}
		if !hinted(p, "recreates Patroni on home-a") {
			t.Errorf("the plan does not warn: %+v", p.Notes)
		}
		if err := apply.Execute(p, host); err != nil {
			t.Fatal(err)
		}
		if !host.ran(apply.RecreatePatroni(apply.Fixture)) {
			t.Errorf("Patroni was not recreated: %v", host.commands)
		}
	})
}

// A running Patroni that does not say which etcd hosts it has may be
// replaced by the stack's `up -d`, so the `up -d` names every service and the
// plan warns. A stopped one is left to the `up -d`, which starts it and
// loses no leader.
func TestPatroniWhoseEtcdHostsCannotBeRead(t *testing.T) {
	t.Run("running", func(t *testing.T) {
		host := applied(t, "home-a")
		host.etcdHosts = ""
		p, err := apply.Build("home-a", withInfra(t, moreEtcdHosts, changeService("haproxy")), acmeModule(t), host)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Actions) == 0 || p.Actions[0].Stack != "infra" || len(p.Actions[0].Up) != 0 || !p.Actions[0].Recreate {
			t.Fatalf("plans %+v, want the whole infra stack's up -d", p.Actions)
		}
		var why string
		for _, n := range p.Notes {
			if strings.Contains(n.Hint, "recreates Patroni on home-a") {
				why = n.Text
			}
		}
		if !strings.Contains(why, "could not read the etcd hosts Patroni runs with") {
			t.Errorf("the plan does not warn why: %+v", p.Notes)
		}
	})
	t.Run("stopped", func(t *testing.T) {
		host := applied(t, "home-a")
		host.etcdHosts = ""
		host.stopped = map[string]string{"infra/patroni": "exited"}
		p, err := apply.Build("home-a", withPatroniEnv(t, moreEtcdHosts), acmeModule(t), host)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Actions) == 0 || p.Actions[0].Stack != "infra" || len(p.Actions[0].Up) != 0 {
			t.Fatalf("plans %+v, want the whole infra stack's up -d to start Patroni", p.Actions)
		}
		if hinted(p, "recreates Patroni") {
			t.Errorf("a stopped Patroni is warned of: %+v", p.Notes)
		}
	})
}

// The running hosts are compared as a set, and they must still name this
// site's own etcd member: it starts with Patroni after a reboot, while any
// other may be down. A restart planned for another file becomes a forced
// recreate of the services it named and Patroni.
func TestPatroniRunningWithoutItsOwnEtcdMember(t *testing.T) {
	t.Run("reordered", func(t *testing.T) {
		host := applied(t, "home-a")
		host.etcdHosts = "10.44.0.3:2379,10.44.0.1:2379,10.44.0.2:2379"
		p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Actions) != 0 || hinted(p, "etcd hosts") || hinted(p, "Patroni") {
			t.Errorf("plans %+v, notes %+v", p.Actions, p.Notes)
		}
	})
	t.Run("restart becomes a recreate", func(t *testing.T) {
		host := applied(t, "home-a")
		host.etcdHosts = "10.44.0.2:2379,10.44.0.3:2379"
		changed := plan(t)
		for i, file := range changed.Files {
			if file.Path == "home-a/srv/paisans/f2a9/infra/haproxy/haproxy.cfg" {
				changed.Files[i].Content = file.Content + "# a later change\n"
			}
		}
		p, err := apply.Build("home-a", changed, acmeModule(t), host)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Actions) == 0 || p.Actions[0].Stack != "infra" {
			t.Fatalf("plans %+v", p.Actions)
		}
		want := "docker compose -f /srv/paisans/f2a9/infra/compose.yaml up -d --no-deps --force-recreate 'haproxy' 'patroni'"
		if got := p.Actions[0].Command(apply.Fixture); got != want {
			t.Errorf("runs %q, want %q", got, want)
		}
		if !hinted(p, "recreates Patroni on home-a") {
			t.Errorf("the plan does not warn: %+v", p.Notes)
		}
	})
}

// An `up -d` that named the other services and stopped part way is owed as
// a forced recreate of those services alone, so the next apply does not
// replace the running Patroni it left out.
func TestAnOwedNarrowedUpStaysNarrowed(t *testing.T) {
	host := applied(t, "home-a")
	host.files["/srv/paisans/f2a9/.paisans-pending.json"] = `{"version":1,"actions":[{"stack":"infra","recreate":true,"up":["etcd","haproxy"]}]}`
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) == 0 || p.Actions[0].Stack != "infra" {
		t.Fatalf("plans %+v", p.Actions)
	}
	want := "docker compose -f /srv/paisans/f2a9/infra/compose.yaml up -d --no-deps --force-recreate 'etcd' 'haproxy'"
	if got := p.Actions[0].Command(apply.Fixture); got != want {
		t.Errorf("runs %q, want %q", got, want)
	}
	if hinted(p, "recreates Patroni") {
		t.Errorf("the plan warns of a recreate it does not make: %+v", p.Notes)
	}
}

// A narrowed `up -d` is recorded as owed with the services it names.
func TestANarrowedUpIsOwedNarrowed(t *testing.T) {
	host := applied(t, "home-a")
	host.fail = "/infra/compose.yaml up -d"
	p, err := apply.Build("home-a", withInfra(t, moreEtcdHosts, changeService("haproxy")), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err == nil {
		t.Fatal("the failing up -d did not stop the apply")
	}
	record := host.files["/srv/paisans/f2a9/.paisans-pending.json"]
	if !strings.Contains(record, `"up"`) || strings.Contains(record, "patroni") {
		t.Errorf("owed record %s", record)
	}
}
