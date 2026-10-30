package doctor

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// The fixtures below are what a live two data site deployment answered, with
// its members renamed to home-a and home-b and its mesh addresses moved to
// the fixture's 10.44.0.x: the replica home-a is 10.44.0.1, the leader home-b
// 10.44.0.2, and the witness vm 10.44.0.3.
const (
	healthyCluster = `{"members": [{"name": "home-a", "role": "sync_standby", "state": "streaming", "api_url": "http://10.44.0.1:8008/patroni", "host": "10.44.0.1", "port": 5432, "timeline": 3, "receive_lag": 0, "receive_lsn": "0/14289BA0", "replay_lag": 0, "replay_lsn": "0/14289BA0", "lag": 0, "lsn": "0/14289BA0"}, {"name": "home-b", "role": "leader", "state": "running", "api_url": "http://10.44.0.2:8008/patroni", "host": "10.44.0.2", "port": 5432, "timeline": 3}], "scope": "paisans"}`
	healthySync    = `{"leader":"home-b","quorum":0,"sync_standby":"home-a"}`
	healthyEtcd    = `[{"endpoint":"http://10.44.0.3:2379","health":true,"took":"4.025774ms"},{"endpoint":"http://10.44.0.2:2379","health":true,"took":"15.013954ms"},{"endpoint":"http://10.44.0.1:2379","health":true,"took":"4.241161ms"}]`
	healthyVersion = `{"etcdserver":"3.5.16","etcdcluster":"3.5.0"}`

	// The stuck case: home-b led and is gone, home-a came back as a replica
	// that /sync no longer names.
	stuckCluster = `{"members": [{"name": "home-a", "role": "replica", "state": "running", "api_url": "http://10.44.0.1:8008/patroni", "host": "10.44.0.1", "port": 5432, "timeline": 2, "lag": 1048576}], "scope": "paisans"}`
	stuckSync    = `{"leader":"home-b","quorum":0,"sync_standby":null}`
	stuckLog     = `paisans-f2a9-infra-patroni-1  | 2026-10-07 21:14:03,512 INFO: following a different leader because i am not the healthiest node
paisans-f2a9-infra-patroni-1  | 2026-10-07 21:14:13,498 INFO: following a different leader because i am not the healthiest node`

	// docker inspect on a container Docker could not start at boot, seen live.
	bindFailedInspect = `{"State":{"Status":"exited","Running":false,"Paused":false,"Restarting":false,"OOMKilled":false,"Dead":false,"Pid":0,"ExitCode":128,"Error":"failed to set up container networking: driver failed programming external connectivity on endpoint paisans-f2a9-talk-app-1 (2238d4...): failed to bind host port 10.44.0.2:8080/tcp: cannot assign requested address","StartedAt":"2026-10-07T20:01:12Z","FinishedAt":"2026-10-07T20:01:12Z"},"RestartCount":0}`
)

func fixture(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Every site logs in with alice's key alone: the fixture's second
	// operator, bob, is there because validate requires two.
	for name, s := range cfg.Sites {
		var keys []config.NamedKey
		for _, k := range s.SSH.Keys {
			if k.Name == "alice" {
				keys = append(keys, k)
			}
		}
		s.SSH.Keys = keys
		cfg.Sites[name] = s
	}
	return cfg
}

func reachAll(sites ...string) []SiteReach {
	var out []SiteReach
	for _, s := range sites {
		out = append(out, SiteReach{Site: s, Destination: "ubuntu@" + s + ".local"})
	}
	return out
}

func healthyInput() Input {
	now := time.Unix(1_760_000_000, 0)
	return Input{
		Sites: []string{"home-a", "home-b", "vm"},
		Reach: reachAll("home-a", "home-b", "vm"),
		Etcd:  EtcdProbe{Site: "home-a", Health: healthyEtcd, Version: healthyVersion},
		Patroni: PatroniProbe{
			Cluster: map[string]string{"home-a": healthyCluster, "home-b": healthyCluster},
			Sync:    healthySync,
		},
		Containers: []SiteContainers{{Site: "home-a", PS: `{"Names":"paisans-f2a9-infra-etcd-1","State":"running","Status":"Up 2 hours","Labels":"community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}`}},
		Standby: []StandbyProbe{{App: "auth", Instances: []apply.Instance{
			{Site: "home-a", State: apply.Active}, {Site: "home-b", State: apply.Standby}}}},
		Clocks: []ClockSample{{Site: "home-a", Before: now, After: now.Add(200 * time.Millisecond), Out: "1760000000.150000000\n"}},
	}
}

func find(t *testing.T, findings []Finding, section, contains string) Finding {
	t.Helper()
	for _, f := range findings {
		if f.Section == section && strings.Contains(f.Line, contains) {
			return f
		}
	}
	t.Fatalf("no %s finding containing %q in:\n%s", section, contains, dump(findings))
	return Finding{}
}

func dump(findings []Finding) string {
	rec := &ui.Recorder{Verbose_: true}
	Report{Findings: findings}.Show(rec)
	return rec.Lines()
}

func TestHealthyDeploymentHasNothingToSay(t *testing.T) {
	report := Diagnose(fixture(t), healthyInput())
	if report.Failed() || report.Count(Warn) > 0 {
		t.Fatalf("a healthy deployment reported problems:\n%s", dump(report.Findings))
	}
	find(t, report.Findings, SectionEtcd, "quorum: 3 of 3 members healthy (needs 2)")
	find(t, report.Findings, SectionEtcd, "version: server 3.5.16, cluster 3.5.0")
	find(t, report.Findings, SectionPatroni, "leader: home-b, running on timeline 3")
	find(t, report.Findings, SectionPatroni, "home-a: sync_standby, streaming, lag 0 bytes")
	find(t, report.Findings, SectionContainers, "home-a: 1 paisans container(s), all running")
	find(t, report.Findings, SectionPocketID, "auth: one active instance")
	find(t, report.Findings, SectionClocks, "home-a: 0.05 s ahead of this workstation")
	if report.Sites != 3 || report.Reached != 3 {
		t.Errorf("sites %d reached %d", report.Sites, report.Reached)
	}
}

// Each check is a step whose result is the finding. A failure carries its
// recovery as the explanation of a refusal, shown at every verbosity, since
// doctor is read in the middle of an outage.
func TestShowShape(t *testing.T) {
	rec := &ui.Recorder{}
	Report{Sites: 3, Reached: 2, Findings: []Finding{
		{Section: SectionPatroni, Level: Fail, Line: "no primary", More: []string{"recover:"}},
		{Section: SectionReach, Level: OK, Line: "home-a: answers"},
		{Section: SectionClocks, Level: Warn, Line: "home-b: 2 s ahead", More: []string{"check time sync"}},
	}}.Show(rec)
	var got []string
	for _, e := range rec.Events {
		got = append(got, e.Kind+" "+e.Text+" | "+e.Extra)
	}
	// Details show only with --verbose, so a recorder keeps them for the
	// terminal to drop.
	want := []string{
		"detail 3 site(s), 2 reached | ",
		"section reach | ",
		"step check reach home-a | ",
		"done check reach home-a | answers",
		"section patroni | ",
		"refuse check patroni: no primary | recover:",
		"section clocks | ",
		"warn check clocks home-b: 2 s ahead | check time sync",
		"detail doctor changes nothing | ",
		"result 1 check passed, 1 warning, 1 failed. | ",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A check that could not be made found nothing right, so it is not shown or
// counted as passed: it is a warning naming the check and why it was
// skipped, and the summary counts it apart.
func TestSkipIsAWarningNotAPass(t *testing.T) {
	rec := &ui.Recorder{}
	Report{Sites: 1, Reached: 1, Findings: []Finding{
		{Section: SectionReach, Level: OK, Line: "home-a: answers"},
		{Section: SectionLeftovers, Level: Skip, Line: "home-a: the host's inventory could not be read", More: []string{"sudo: a password is required"}},
	}}.Show(rec)
	if rec.Has("done", "check leftovers") {
		t.Errorf("a skipped check ended as a step that passed:\n%s", rec.Lines())
	}
	i := rec.Index("warn", "check leftovers home-a skipped: the host's inventory could not be read")
	if i < 0 || rec.Events[i].Extra != "sudo: a password is required" {
		t.Errorf("a skipped check is not a warning with its cause:\n%s", rec.Lines())
	}
	if !rec.Has("result", "1 check passed, 1 skipped.") {
		t.Errorf("summary does not count the skip apart:\n%s", rec.Lines())
	}
}

// A site that does not answer is a Fail that says what is missing while it
// is gone, from its roles and its place in the configuration.
func TestUnreachableSiteSaysWhatIsLost(t *testing.T) {
	cfg := fixture(t)
	findings := Reach(cfg, []SiteReach{
		{Site: "home-a", Destination: "ubuntu@home-a.local"},
		{Site: "home-b", Destination: "ubuntu@home-b.local", Err: "ssh: connect to host home-b.local port 22: Operation timed out"},
		{Site: "vm", Destination: "ubuntu@vm.example.org", Err: "ssh: connect to host vm.example.org port 22: Connection refused"},
	})
	b := find(t, findings, SectionReach, "home-b: ssh to ubuntu@home-b.local did not answer (Operation timed out)")
	if b.Level != Fail {
		t.Fatalf("level %v", b.Level)
	}
	text := strings.Join(b.More, "\n")
	for _, want := range []string{"roles: data, apps", "one of 3 etcd votes (quorum needs 2)", "a Patroni member", "a Garage node", "its copy of account, auth, digest, talk, wiki", "web, pinned here and running nowhere else"} {
		if !strings.Contains(text, want) {
			t.Errorf("home-b's loss lacks %q:\n%s", want, text)
		}
	}
	vm := strings.Join(find(t, findings, SectionReach, "vm:").More, "\n")
	for _, want := range []string{"its gateway, which every public hostname but the monitor's goes through", "one of 3 etcd votes", "chat, gate, notify, pinned here"} {
		if !strings.Contains(vm, want) {
			t.Errorf("vm's loss lacks %q:\n%s", want, vm)
		}
	}
	if find(t, findings, SectionReach, "home-a:").Level != OK {
		t.Error("home-a answered")
	}
}

// A monitor's absence is the one loss nothing else reports.
func TestAnUnreachableMonitorSaysNothingReportsFailures(t *testing.T) {
	cfg := fixture(t)
	findings := Reach(cfg, []SiteReach{
		{Site: "watch", Destination: "ubuntu@watch.example.org", Err: "ssh: connect to host watch.example.org port 22: Connection refused"},
	})
	text := strings.Join(find(t, findings, SectionReach, "watch:").More, "\n")
	for _, want := range []string{"the uptime monitor, so nothing reports the other sites' failures while it is gone", "health, stats, pinned here"} {
		if !strings.Contains(text, want) {
			t.Errorf("watch's loss lacks %q:\n%s", want, text)
		}
	}
}

// A monitor whose one app is Beszel's hub runs no uptime monitor, so its
// absence does not claim to take one away.
func TestAMonitorWithoutUptimeLosesNoUptimeMonitor(t *testing.T) {
	cfg := fixture(t)
	for name, app := range cfg.Apps {
		if app.Blueprint == config.BlueprintUptime {
			delete(cfg.Apps, name)
		}
	}
	if got := Losses(cfg, "watch"); strings.Contains(got, "the uptime monitor") {
		t.Errorf("watch, holding only the stats app, loses %q", got)
	}
}

func TestEtcdWithoutQuorumFails(t *testing.T) {
	in := healthyInput()
	in.Etcd.Health = `[{"endpoint":"http://10.44.0.1:2379","health":false,"took":"5.0s","error":"context deadline exceeded"},{"endpoint":"http://10.44.0.2:2379","health":false,"took":"5.0s","error":"dial tcp 10.44.0.2:2379: connect: connection refused"},{"endpoint":"http://10.44.0.3:2379","health":true,"took":"3ms"}]
Error: unhealthy cluster`
	f := find(t, Etcd(fixture(t), in), SectionEtcd, "no quorum: 1 of 3 members healthy (needs 2)")
	if f.Level != Fail {
		t.Fatalf("level %v", f.Level)
	}
	text := strings.Join(f.More, "\n")
	for _, want := range []string{"home-b: dial tcp 10.44.0.2:2379: connect: connection refused", "Start the members that are not healthy: home-a, home-b", "demotes itself"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
}

func TestEtcdOneMemberDownWarns(t *testing.T) {
	in := healthyInput()
	in.Etcd.Health = `[{"endpoint":"http://10.44.0.1:2379","health":true},{"endpoint":"http://10.44.0.2:2379","health":true},{"endpoint":"http://10.44.0.3:2379","health":false,"error":"connection refused"}]`
	if f := find(t, Etcd(fixture(t), in), SectionEtcd, "2 of 3"); f.Level != Warn {
		t.Fatalf("level %v", f.Level)
	}
}

func TestEtcdNotReached(t *testing.T) {
	in := healthyInput()
	in.Etcd = EtcdProbe{Tried: []string{"home-a: service \"etcd\" is not running"}}
	if f := find(t, Etcd(fixture(t), in), SectionEtcd, "no etcd member reached"); f.Level != Fail {
		t.Fatalf("level %v", f.Level)
	}
}

// A cluster version stuck at 3.0.0 under a 3.5 server is the founding member
// that never ran, which leaves Patroni waiting forever.
func TestEtcdUnsettledVersionFails(t *testing.T) {
	in := healthyInput()
	in.Etcd.Health = `[{"endpoint":"http://10.44.0.1:2379","health":true},{"endpoint":"http://10.44.0.2:2379","health":true},{"endpoint":"http://10.44.0.3:2379","health":false,"error":"connection refused"}]`
	in.Etcd.Version = `{"etcdserver":"3.5.16","etcdcluster":"3.0.0"}`
	f := find(t, Etcd(fixture(t), in), SectionEtcd, "version: server 3.5.16, cluster 3.0.0")
	if f.Level != Fail {
		t.Fatalf("level %v", f.Level)
	}
	text := strings.Join(f.More, "\n")
	for _, want := range []string{"decideClusterVersion", "/v3alpha", "patroni/dcs/etcd3.py", "`paisans deployment apply --site vm --execute`"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
}

func TestEtcdVersionSkippedWithoutCurl(t *testing.T) {
	in := healthyInput()
	in.Etcd.Version = noCurl + "\n"
	if f := find(t, Etcd(fixture(t), in), SectionEtcd, "curl is not installed on home-a"); f.Level != Skip {
		t.Fatalf("level %v", f.Level)
	}
}

func stuckInput() Input {
	return Input{
		Sites: []string{"home-a", "home-b", "vm"},
		Reach: []SiteReach{
			{Site: "home-a", Destination: "ubuntu@home-a.local"},
			{Site: "home-b", Destination: "ubuntu@home-b.local", Err: "ssh: connect to host home-b.local port 22: Operation timed out"},
			{Site: "vm", Destination: "ubuntu@vm.example.org"},
		},
		Patroni: PatroniProbe{
			Cluster: map[string]string{"home-a": stuckCluster},
			Sync:    stuckSync,
			Logs:    map[string]string{"home-a": stuckLog},
		},
	}
}

// When the old leader answers ssh but cannot run its Patroni, the first
// recovery names what is in the way: a Docker that does not answer, or an
// admission that is pending or did not pass, never `up -d patroni`.
func TestStartAdviceNamesWhatStopsTheOldLeader(t *testing.T) {
	cfg := fixture(t)
	for _, c := range []struct {
		name string
		set  func(*Input)
		want string
	}{
		{"docker down", func(in *Input) {
			in.Docker = []DockerProbe{{Site: "home-b", Err: "Cannot connect to the Docker daemon"}}
		}, "  1. Start home-b's Docker: its host answers ssh, but its rootless Docker does not answer, and the docker section says how."},
		{"pending", func(in *Input) {
			in.Admission = []AdmissionProbe{{Site: "home-b", Class: pendingClass("home-b")}}
		}, "  1. Have home-b's owner admit this deployment again: it is pending"},
		{"admission failed", func(in *Input) {
			in.Admission = []AdmissionProbe{{Site: "home-b", Err: "unreadable"}}
		}, "  1. Have home-b's owner admit this deployment again: its host answers ssh, but its admission did not pass"},
	} {
		in := stuckInput()
		in.Reach[1].Err = ""
		c.set(&in)
		f := find(t, Patroni(cfg, in), SectionPatroni, "no primary")
		text := strings.Join(f.More, "\n")
		if !strings.Contains(text, c.want) || strings.Contains(text, "up -d patroni") {
			t.Errorf("%s: want %q:\n%s", c.name, c.want, text)
		}
	}
}

// The motivating case. home-b led with home-a as its synchronous standby;
// home-a went down, home-b committed alone and dropped it from /sync, then
// home-b went down. home-a came back and will not promote. The advice is to
// start home-b first, and only then, with its warnings, to force home-a.
func TestStuckReplicaIsDiagnosedAndStartingTheLeaderComesFirst(t *testing.T) {
	findings := Patroni(fixture(t), stuckInput())
	f := find(t, findings, SectionPatroni, "no primary: home-a is a replica and will not promote while home-b, which led, is gone")
	if f.Level != Fail {
		t.Fatalf("level %v", f.Level)
	}
	text := strings.Join(f.More, "\n")
	command := "docker compose -f /srv/paisans/f2a9/infra/compose.yaml exec patroni patronictl -c /home/postgres/postgres.yml failover --candidate home-a --force"
	start := strings.Index(text, "  1. Start home-b (ssh ubuntu@home-b.local). When it returns it takes the leader lock again, and home-a catches up from it. No data is lost.")
	force := strings.Index(text, "  2. Only if home-b is lost permanently: promote home-a by hand. On home-a (ssh ubuntu@home-a.local):")
	cmd := strings.Index(text, "       "+command)
	if start < 0 || force < 0 || cmd < 0 || !(start < force && force < cmd) {
		t.Fatalf("want start home-b, then the forced failover with its command, in that order (%d %d %d):\n%s", start, force, cmd, text)
	}
	warnings := []string{
		"WARNING: every commit home-b made after home-a stopped being its synchronous standby is lost",
		"WARNING: if home-b returns later its data has diverged, and Patroni must rewind or reinitialise it",
		"WARNING: this changes member data. It is a human's decision, and never something to run unattended.",
	}
	for _, w := range warnings {
		i := strings.Index(text, w)
		if i < cmd {
			t.Errorf("warning %q missing or before the command:\n%s", w, text)
		}
	}
	for _, want := range []string{
		"/sync names home-b as the leader and none as its synchronous standby",
		"cluster.synchronous_strict false",
		"is_healthiest_node",
		"manual_failover_process_no_leader",
		"asynchronous node",
		"evidence, home-a's Patroni log:",
		"following a different leader because i am not the healthiest node",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
}

// A replica /sync does name is not the stuck case: the diagnosis is the
// generic one, and any forced failover still carries the warnings.
func TestNoLeaderWithTheReplicaInSyncIsGeneric(t *testing.T) {
	in := stuckInput()
	in.Patroni.Sync = `{"leader":"home-b","quorum":0,"sync_standby":"home-a"}`
	f := find(t, Patroni(fixture(t), in), SectionPatroni, "no primary: no member holds the leader lock (home-a)")
	text := strings.Join(f.More, "\n")
	for _, want := range []string{"is_lagging", "maximum_lag_on_failover", "1. Start the members that are not in the cluster: home-b (ssh ubuntu@home-b.local)", "WARNING: every commit", "following a different leader"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	in.Patroni.Sync = ""
	if f := find(t, Patroni(fixture(t), in), SectionPatroni, "no member holds the leader lock"); f.Level != Fail {
		t.Fatalf("level %v", f.Level)
	}
}

func TestNoDatabaseSiteReached(t *testing.T) {
	in := stuckInput()
	in.Reach[0].Err = "Operation timed out"
	f := find(t, Patroni(fixture(t), in), SectionPatroni, "no database site reached")
	if f.Level != Fail {
		t.Fatalf("level %v", f.Level)
	}
}

func TestLaggingReplicaWarns(t *testing.T) {
	in := healthyInput()
	in.Patroni.Cluster["home-a"] = strings.Replace(healthyCluster, `"lag": 0`, `"lag": 33554432`, 1)
	in.Patroni.Cluster["home-b"] = in.Patroni.Cluster["home-a"]
	if f := find(t, Patroni(fixture(t), in), SectionPatroni, "home-a: sync_standby, streaming, lag 33554432 bytes"); f.Level != Warn {
		t.Fatalf("level %v", f.Level)
	}
}

// A leader with no synchronous standby, under synchronous mode, is the state
// the stuck case starts from, so it is said while it can still be fixed.
func TestNoSyncStandbyWarns(t *testing.T) {
	in := healthyInput()
	alone := strings.Replace(healthyCluster, `"role": "sync_standby"`, `"role": "replica"`, 1)
	in.Patroni.Cluster = map[string]string{"home-a": alone}
	if f := find(t, Patroni(fixture(t), in), SectionPatroni, "no synchronous standby"); f.Level != Warn {
		t.Fatalf("level %v", f.Level)
	}
}

func TestParseSync(t *testing.T) {
	s, err := ParseSync(`{"leader":"home-b","quorum":0,"sync_standby":"home-a, home-c"}`)
	if err != nil || !s.Names("HOME-A") || !s.Names("home-c") || !s.Names("home-b") || s.Names("vm") {
		t.Fatalf("%+v %v", s, err)
	}
	s, _ = ParseSync(stuckSync)
	if s.Names("home-a") || len(s.Standbys()) != 0 {
		t.Fatalf("a null sync_standby names nobody but the leader: %+v", s)
	}
	if s, _ := ParseSync(""); s.Names("home-b") {
		t.Fatal("an empty /sync names nobody")
	}
}

func TestContainerThatStartedBeforeWg0(t *testing.T) {
	sites := []SiteContainers{{
		Site: "home-b",
		PS: `{"Names":"paisans-f2a9-infra-etcd-1","State":"running","Status":"Up 3 minutes","Labels":"community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01,com.docker.compose.project=paisans-f2a9-infra"}
{"Names":"paisans-f2a9-talk-app-1","State":"exited","Status":"Exited (128) 3 minutes ago","Labels":"community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01,com.docker.compose.service=app,com.docker.compose.project=paisans-f2a9-talk"}`,
		Down: []ContainerProbe{{
			Entry:   PSEntry{Names: "paisans-f2a9-talk-app-1", State: "exited", Status: "Exited (128) 3 minutes ago", Labels: "community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01,com.docker.compose.service=app,com.docker.compose.project=paisans-f2a9-talk"},
			Inspect: bindFailedInspect,
		}},
	}}
	f := find(t, Containers(fixture(t).Deployment(), sites, ""), SectionContainers, "home-b: paisans-f2a9-talk-app-1 exited (exit 128, restarted 0 time(s)): failed to set up container networking")
	if f.Level != Fail {
		t.Fatalf("level %v", f.Level)
	}
	text := strings.Join(f.More, "\n")
	for _, want := range []string{"Its Docker daemon started it before psns-f2a9 was up", "`docker start paisans-f2a9-talk-app-1` on home-b, as the deployment's user", "`paisans deployment apply --site home-b --recreate talk --execute`", "`paisans doctor --site home-b`"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "site prepare") {
		t.Errorf("the advice names site prepare, which orders nothing at boot:\n%s", text)
	}
}

func TestRestartingContainerThatCannotReachItsDatabase(t *testing.T) {
	sites := []SiteContainers{{
		Site: "home-a",
		PS:   `{"Names":"paisans-f2a9-wiki-app-1","State":"restarting","Status":"Restarting (1) 5 seconds ago","Labels":"community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}`,
		Down: []ContainerProbe{{
			Entry:   PSEntry{Names: "paisans-f2a9-wiki-app-1", State: "restarting", Labels: "community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"},
			Inspect: `{"State":{"Status":"restarting","Restarting":true,"ExitCode":1,"Error":""},"RestartCount":14}`,
			Logs:    "starting\nError: connect ECONNREFUSED 127.0.0.1:5000\nSequelizeConnectionRefusedError: connection refused\n",
		}},
	}}
	f := find(t, Containers(fixture(t).Deployment(), sites, ""), SectionContainers, "paisans-f2a9-wiki-app-1 restarting (exit 1, restarted 14 time(s))")
	if !strings.Contains(strings.Join(f.More, "\n"), "cannot reach its database") {
		t.Fatalf("%v", f.More)
	}
}

func TestOtherDownContainerShowsItsLastLines(t *testing.T) {
	sites := []SiteContainers{{
		Site: "vm",
		PS:   `{"Names":"paisans-f2a9-chat-app-1","State":"exited","Status":"Exited (1)","Labels":"community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}`,
		Down: []ContainerProbe{{
			Entry:   PSEntry{Names: "paisans-f2a9-chat-app-1", State: "exited", Labels: "community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"},
			Inspect: `{"State":{"Status":"exited","ExitCode":1,"Error":""},"RestartCount":0}`,
			Logs:    "one\ntwo\nthree\nfour\n",
		}},
	}}
	f := find(t, Containers(fixture(t).Deployment(), sites, ""), SectionContainers, "paisans-f2a9-chat-app-1 exited (exit 1")
	if got := strings.Join(f.More, "|"); got != "last log lines:|  two|  three|  four" {
		t.Fatalf("got %q", got)
	}
}

// The beszel hub's admin service is a one shot, restart "no": exited with
// code 0 it is done, as the apply's health gate counts it, and the site
// reads as healthy. Exited 1, or exited 0 under another policy, it is down.
func TestAFinishedOneShotIsNotDown(t *testing.T) {
	const labels = "community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"
	for _, c := range []struct {
		name, inspect string
		down          bool
	}{
		{"restart no, exit 0", `{"State":{"Status":"exited","ExitCode":0,"Error":""},"RestartCount":0,"RestartPolicy":"no"}`, false},
		{"restart no, exit 1", `{"State":{"Status":"exited","ExitCode":1,"Error":""},"RestartCount":0,"RestartPolicy":"no"}`, true},
		{"unless-stopped, exit 0", `{"State":{"Status":"exited","ExitCode":0,"Error":""},"RestartCount":0,"RestartPolicy":"unless-stopped"}`, true},
		{"policy unread, exit 0", `{"State":{"Status":"exited","ExitCode":0,"Error":""},"RestartCount":0}`, true},
	} {
		sites := []SiteContainers{{
			Site: "vm",
			PS: `{"Names":"paisans-f2a9-stats-app-1","State":"running","Status":"Up 2 hours (healthy)","Labels":"` + labels + `"}` + "\n" +
				`{"Names":"paisans-f2a9-stats-admin-1","State":"exited","Status":"Exited (0) 2 hours ago","Labels":"` + labels + `"}`,
			Down: []ContainerProbe{{
				Entry:   PSEntry{Names: "paisans-f2a9-stats-admin-1", State: "exited", Labels: labels},
				Inspect: c.inspect,
			}},
		}}
		got := Containers(fixture(t).Deployment(), sites, "")
		failed := false
		for _, f := range got {
			if f.Level == Fail {
				failed = true
			}
		}
		if failed != c.down {
			t.Errorf("%s: down %v, want %v: %+v", c.name, failed, c.down, got)
		}
		if !c.down {
			find(t, got, SectionContainers, "vm: 1 paisans container(s), all running, and 1 one shot finished")
		}
	}
}

// Docker reports health in `docker ps` two ways: Docker 29.5 and later in
// HealthStatus, earlier releases only in Status. Both read the same, and a
// container with no healthcheck has none.
func TestPSEntryHealth(t *testing.T) {
	for _, c := range []struct {
		e    PSEntry
		want string
	}{
		{PSEntry{State: "running", Status: "Up 6 seconds (unhealthy)", HealthStatus: "unhealthy"}, "unhealthy"},
		{PSEntry{State: "running", Status: "Up 6 seconds (unhealthy)"}, "unhealthy"},
		{PSEntry{State: "running", Status: "Up 6 seconds (health: starting)"}, "starting"},
		{PSEntry{State: "running", Status: "Up 2 hours (healthy)"}, "healthy"},
		{PSEntry{State: "running", Status: "Up 6 seconds", HealthStatus: "none"}, ""},
		{PSEntry{State: "running", Status: "Up 6 seconds"}, ""},
		{PSEntry{State: "exited", Status: "Exited (1) 3 minutes ago"}, ""},
	} {
		if got := c.e.Health(); got != c.want {
			t.Errorf("%+v: health %q, want %q", c.e, got, c.want)
		}
	}
	if !(PSEntry{State: "running", Status: "Up 6 seconds (unhealthy)"}).Down() {
		t.Error("a running container whose healthcheck reports unhealthy needs a look")
	}
	for _, s := range []string{"Up 6 seconds (health: starting)", "Up 2 hours (healthy)", "Up 2 hours"} {
		if (PSEntry{State: "running", Status: s}).Down() {
			t.Errorf("%q is not down", s)
		}
	}
}

// A container that runs but fails its healthcheck is a failure, and the
// finding carries what the check printed, as `docker inspect` recorded it on
// Docker Engine 29.7.2, so the operator sees why without asking Docker again.
func TestUnhealthyContainerShowsItsHealthOutput(t *testing.T) {
	labels := "community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01,com.docker.compose.project=paisans-f2a9-wiki"
	sites := []SiteContainers{{
		Site: "home-a",
		PS: `{"Names":"paisans-f2a9-infra-etcd-1","State":"running","Status":"Up 2 hours","Labels":"community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
{"Names":"paisans-f2a9-wiki-bootstrap-1","State":"running","Status":"Up 6 minutes (unhealthy)","HealthStatus":"unhealthy","Labels":"` + labels + `"}`,
		Down: []ContainerProbe{{
			Entry:   PSEntry{Names: "paisans-f2a9-wiki-bootstrap-1", State: "running", Status: "Up 6 minutes (unhealthy)", HealthStatus: "unhealthy", Labels: labels},
			Inspect: `{"State":{"Status":"running","Running":true,"Paused":false,"Restarting":false,"OOMKilled":false,"Dead":false,"Pid":8880,"ExitCode":0,"Error":"","StartedAt":"2026-10-15T03:45:11.948468594Z","FinishedAt":"0001-01-01T00:00:00Z","Health":{"Status":"unhealthy","FailingStreak":3,"Log":[{"Start":"2026-10-15T03:45:14.033937428Z","End":"2026-10-15T03:45:14.066271636Z","ExitCode":1,"Output":"login failed\n"},{"Start":"2026-10-15T03:45:16.067314471Z","End":"2026-10-15T03:45:16.097151804Z","ExitCode":1,"Output":"login failed\n"},{"Start":"2026-10-15T03:45:18.099243138Z","End":"2026-10-15T03:45:18.130928055Z","ExitCode":1,"Output":"GET /healthz: 503\ngroup Members: missing\n"}]}},"RestartCount":0}`,
			Logs:    "listening on :3000\n",
		}},
	}}
	got := Containers(fixture(t).Deployment(), sites, "")
	for _, f := range got {
		if f.Level == OK {
			t.Errorf("an unhealthy container leaves no site all running: %+v", f)
		}
	}
	f := find(t, got, SectionContainers, "home-a: paisans-f2a9-wiki-bootstrap-1 running but unhealthy, failed its last 3 health check(s)")
	if f.Level != Fail {
		t.Fatalf("level %v", f.Level)
	}
	if text := strings.Join(f.More, "|"); text != "last health check output:|  login failed|  GET /healthz: 503|  group Members: missing|last log lines:|  listening on :3000" {
		t.Fatalf("got %q", text)
	}
}

// A healthcheck still in its start period has not failed. It is a warning
// beside the site's line, which still counts it as running, because doctor
// cannot yet say the container works.
func TestStartingContainerIsAWarning(t *testing.T) {
	id := "community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"
	sites := []SiteContainers{{
		Site: "vm",
		PS: `{"Names":"paisans-f2a9-infra-etcd-1","State":"running","Status":"Up 2 hours (healthy)","Labels":"` + id + `"}
{"Names":"paisans-f2a9-wiki-bootstrap-1","State":"running","Status":"Up 6 seconds (health: starting)","Labels":"` + id + `"}
{"Names":"paisans-f2a9-chat-app-1","State":"running","Status":"Up 6 seconds","HealthStatus":"none","Labels":"` + id + `"}`,
	}}
	got := Containers(fixture(t).Deployment(), sites, "")
	if f := find(t, got, SectionContainers, "vm: 3 paisans container(s), all running"); f.Level != OK {
		t.Fatalf("%+v", f)
	}
	f := find(t, got, SectionContainers, "vm: paisans-f2a9-wiki-bootstrap-1 running, its healthcheck has not passed yet")
	if f.Level != Warn || !strings.Contains(strings.Join(f.More, "\n"), "only once Docker reports it unhealthy") {
		t.Fatalf("%+v", f)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v, want the site's line and one warning", got)
	}
}

func TestPocketIDActiveCount(t *testing.T) {
	none := Standby(fixture(t).Deployment(), []StandbyProbe{{App: "auth", Instances: []apply.Instance{{Site: "home-a", State: apply.Standby}, {Site: "home-b", State: apply.Unreachable, Detail: "did not answer ssh"}}}}, true)
	f := find(t, none, SectionPocketID, "auth: no active instance, so sign in is down")
	if f.Level != Fail || !strings.Contains(strings.Join(f.More, "\n"), "logs app") || !strings.Contains(strings.Join(f.More, "\n"), "no database primary") {
		t.Fatalf("%+v", f)
	}
	two := Standby(fixture(t).Deployment(), []StandbyProbe{{App: "auth", Instances: []apply.Instance{{Site: "home-a", State: apply.Active}, {Site: "home-b", State: apply.Active}}}}, false)
	if f := find(t, two, SectionPocketID, "2 sites have an active instance"); f.Level != Fail {
		t.Fatalf("%+v", f)
	}
}

// The host's clock is compared with the middle of the round trip, so a slow
// connection does not read as drift.
func TestClockOffsetSubtractsHalfTheRoundTrip(t *testing.T) {
	before := time.Unix(1_760_000_000, 0)
	c := ClockSample{Site: "home-a", Before: before, After: before.Add(4 * time.Second), Out: "1760000002.000000000"}
	offset, rtt, err := c.Offset()
	if err != nil || offset.Abs() > time.Millisecond || rtt != 4*time.Second {
		t.Fatalf("offset %v rtt %v err %v", offset, rtt, err)
	}
	drift := Clocks([]ClockSample{{Site: "vm", Before: before, After: before, Out: "1760000002.500000000"}})
	f := find(t, drift, SectionClocks, "vm: 2.50 s ahead of this workstation")
	if f.Level != Warn || !strings.Contains(strings.Join(f.More, "\n"), "probing_status.go") {
		t.Fatalf("%+v", f)
	}
	if f := find(t, Clocks([]ClockSample{{Site: "vm", Before: before, After: before, Out: "1759999999.200000000"}}), SectionClocks, "vm: 0.80 s behind"); f.Level != OK {
		t.Fatalf("%+v", f)
	}
}

// With a primary up, an app that cannot reach its database is not waiting on
// Patroni, and the advice says so and recreates its stack. A container whose
// own network is unreachable, as seen live after a failed boot, gets the
// same recreate whether or not there is a primary.
func TestContainerDatabaseAdviceKnowsWhetherThereIsAPrimary(t *testing.T) {
	probe := func(logs string) []SiteContainers {
		return []SiteContainers{{
			Site: "home-a",
			PS:   `{"Names":"paisans-f2a9-auth-app-1","State":"restarting","Status":"Restarting (1) 5 seconds ago","Labels":"community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}`,
			Down: []ContainerProbe{{
				Entry:   PSEntry{Names: "paisans-f2a9-auth-app-1", State: "restarting", Labels: "community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01,com.docker.compose.project=paisans-f2a9-auth,com.docker.compose.service=app"},
				Inspect: `{"State":{"Status":"restarting","Restarting":true,"ExitCode":1,"Error":""},"RestartCount":34}`,
				Logs:    logs,
			}},
		}}
	}
	refused := "ERR Failed to run pocket-id error=\"failed to ping Postgres database: dial tcp 10.44.0.1:5000: connect: connection refused\"\n"
	f := find(t, Containers(fixture(t).Deployment(), probe(refused), "home-b"), SectionContainers, "paisans-f2a9-auth-app-1 restarting")
	more := strings.Join(f.More, "\n")
	if !strings.Contains(more, "although home-b is the primary") || !strings.Contains(more, "--only auth --recreate auth") {
		t.Errorf("with a primary up:\n%s", more)
	}
	unreachable := "ERR Failed to run pocket-id error=\"failed to ping Postgres database: dial tcp 10.44.0.1:5000: connect: network is unreachable\"\n"
	for _, primary := range []string{"", "home-b"} {
		f := find(t, Containers(fixture(t).Deployment(), probe(unreachable), primary), SectionContainers, "paisans-f2a9-auth-app-1 restarting")
		more := strings.Join(f.More, "\n")
		if !strings.Contains(more, "no route to the mesh address") || !strings.Contains(more, "--only auth --recreate auth") {
			t.Errorf("network unreachable, primary %q:\n%s", primary, more)
		}
	}
}

// Doctor asks Docker for this deployment's containers by label, and a
// container labelled with another deployment's id, or with none, is not
// reported even when the output carries it.
func TestContainersAreThisDeploymentsByLabel(t *testing.T) {
	d := fixture(t).Deployment()
	if cmd := ContainersCommand(d); !strings.Contains(cmd, "--filter 'label=community.paisans.deployment="+d.ID+"'") || strings.Contains(cmd, "name=") {
		t.Errorf("the listing does not filter on the deployment label: %s", cmd)
	}
	other := "community.paisans.deployment=0c1d2e3f-4a5b-4c6d-8e7f-8091a2b3c4d5"
	sites := []SiteContainers{{
		Site: "home-a",
		PS: `{"Names":"paisans-f2a9-talk-app-1","State":"running","Status":"Up","Labels":"community.paisans.deployment=` + d.ID + `"}
{"Names":"paisans-0c1d-talk-app-1","State":"exited","Status":"Exited (1)","Labels":"` + other + `"}
{"Names":"paisans-f2a9-wiki-app-1","State":"exited","Status":"Exited (1)","Labels":"com.docker.compose.project=paisans-f2a9-wiki"}`,
		Down: []ContainerProbe{
			{Entry: PSEntry{Names: "paisans-0c1d-talk-app-1", State: "exited", Labels: other}},
			{Entry: PSEntry{Names: "paisans-f2a9-wiki-app-1", State: "exited", Labels: "com.docker.compose.project=paisans-f2a9-wiki"}},
		},
	}}
	got := Containers(d, sites, "")
	if len(got) != 1 || got[0].Level != OK || got[0].Line != "home-a: 1 paisans container(s), all running" {
		t.Fatalf("got %+v, want one OK line counting only this deployment's container", got)
	}
}

// The reason in an unreachable site's line is ssh's, without the host ssh
// names again: the line already names the site and its destination.
func TestUnreachableLineNamesTheHostOnce(t *testing.T) {
	cfg := fixture(t)
	for err, want := range map[string]string{
		"ssh: connect to host home-b.local port 22: Connection refused":                              "home-b: ssh to ubuntu@home-b.local did not answer (Connection refused)",
		"ssh: Could not resolve hostname home-b.local: nodename nor servname provided, or not known": "home-b: ssh to ubuntu@home-b.local did not answer (the name does not resolve: nodename nor servname provided, or not known)",
		"kex_exchange_identification: read: Connection reset by peer":                                "home-b: ssh to ubuntu@home-b.local did not answer (kex_exchange_identification: read: Connection reset by peer)",
	} {
		findings := Reach(cfg, []SiteReach{{Site: "home-b", Destination: "ubuntu@home-b.local", Err: err}})
		if findings[0].Line != want {
			t.Errorf("%q:\n got %q\nwant %q", err, findings[0].Line, want)
		}
	}
}

// A gateway that also runs the monitor takes every public hostname with it,
// the monitor's included.
func TestASharedGatewaysLossIsEveryHostname(t *testing.T) {
	cfg := fixture(t)
	delete(cfg.Sites, "watch")
	vm := cfg.Sites["vm"]
	vm.Roles = append(vm.Roles, config.RoleMonitor)
	cfg.Sites["vm"] = vm
	for name, app := range cfg.Apps {
		if app.Blueprint == config.BlueprintUptime {
			app.Placement.Site = "vm"
			cfg.Apps[name] = app
		}
	}
	got := Losses(cfg, "vm")
	for _, want := range []string{"its gateway, which every public hostname goes through", "the uptime monitor"} {
		if !strings.Contains(got, want) {
			t.Errorf("vm's loss lacks %q: %s", want, got)
		}
	}
	if strings.Contains(got, "but the monitor's") {
		t.Errorf("vm's loss excepts the monitor it runs: %s", got)
	}
}

// A gateway that runs a monitor, beside a second monitor serving its own
// hostname, does not take that hostname with it.
func TestASharedGatewaysLossExceptsAnotherMonitor(t *testing.T) {
	cfg := fixture(t)
	vm := cfg.Sites["vm"]
	vm.Roles = append(vm.Roles, config.RoleMonitor)
	cfg.Sites["vm"] = vm
	if got := Losses(cfg, "vm"); !strings.Contains(got, "every public hostname but the monitor's goes through") {
		t.Errorf("vm's loss does not except watch's monitor: %s", got)
	}
}
