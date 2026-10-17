package doctor

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/topology"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// events is a log the nodes of a test graph write to from their goroutines.
type events struct {
	mu   sync.Mutex
	list []string
}

func (e *events) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, s)
}

func (e *events) has(s string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Contains(e.list, s)
}

func (e *events) index(s string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Index(e.list, s)
}

// node is a test node that logs its start and end, and ends with outcome.
func node(log *events, name string, outcome Outcome, hard []Need, soft ...string) Node {
	return Node{Name: name, Section: name, Hard: hard, Soft: soft,
		Gather: func(Input) func(*Input) {
			log.add("start " + name)
			time.Sleep(time.Millisecond)
			log.add("end " + name)
			return nil
		},
		Outcome: func(Input) Outcome { return outcome },
		Judge: func(Input) []Finding {
			level := OK
			if outcome == Failed {
				level = Fail
			}
			return []Finding{{Section: name, Level: level, Line: name + " said so"}}
		},
	}
}

// A node never runs before its hard needs pass. A failed hard need skips
// what needs it, and what needs that, with no read of their own. A soft
// need is waited for and its failure skips nothing. An any-of need passes
// on one.
func TestGraphNeeds(t *testing.T) {
	log := &events{}
	g := NewGraph(nil)
	g.Add(node(log, "a", Passed, nil))
	g.Add(node(log, "x", Failed, nil))
	g.Add(node(log, "b", Passed, []Need{Hard("a")}))
	g.Add(node(log, "c", Passed, []Need{Hard("x")}))
	g.Add(node(log, "d", Passed, []Need{Hard("c")}))
	g.Add(node(log, "s", Passed, nil, "x"))
	g.Add(node(log, "any", Passed, []Need{AnyOf("x", "a")}))
	g.Add(node(log, "none", Passed, []Need{AnyOf("x", "c")}))
	g.Add(node(log, "free", Passed, []Need{AnyOf()}))
	rec := &ui.Recorder{}
	report, _ := g.Run(rec, 0)

	if log.index("start b") < log.index("end a") {
		t.Errorf("b started before a passed: %v", log.list)
	}
	for _, name := range []string{"c", "d", "none"} {
		if log.has("start " + name) {
			t.Errorf("%s ran although a hard need failed", name)
		}
		if rec.Has("step", "check "+name) {
			t.Errorf("%s has a line of its own:\n%s", name, rec.Lines())
		}
	}
	if !log.has("start s") || log.index("start s") < log.index("end x") {
		t.Errorf("s, whose soft need failed, did not run after it: %v", log.list)
	}
	if !log.has("start any") || !log.has("start free") {
		t.Errorf("an any-of need with one pass, or with none to wait on, kept its node from running: %v", log.list)
	}
	if report.Pruned != 3 {
		t.Errorf("pruned %d, want 3", report.Pruned)
	}
	i := rec.Index("explain", "Not checked because of this")
	if i < 0 || rec.Events[i].Text != "Not checked because of this: c, d, none." {
		t.Errorf("x's line does not list what it kept from running:\n%s", rec.Lines())
	}
	if !strings.Contains(report.Summary(), "3 skipped") {
		t.Errorf("summary %q does not count the pruned checks as skipped", report.Summary())
	}
}

// A node that skips itself reads nothing, has a line saying why, and skips
// what needs it.
func TestGraphUnlessSkipsWithoutReading(t *testing.T) {
	log := &events{}
	g := NewGraph(nil)
	g.Add(node(log, "lockout", Passed, nil))
	token := node(log, "dns token vm", Passed, nil, "lockout")
	token.Site, token.Subject = "vm", "vm"
	token.Unless = func(Input) string { return "not asked, so as not to feed Cloudflare's lockout" }
	g.Add(token)
	g.Add(node(log, "dns records", Passed, []Need{Hard("dns token vm")}))
	rec := &ui.Recorder{}
	report, _ := g.Run(rec, 0)
	if log.has("start dns token vm") || log.has("start dns records") {
		t.Errorf("a skipped node read: %v", log.list)
	}
	if !rec.Has("warned", "check dns token vm") || !strings.Contains(rec.Lines(), "skipped: not asked, so as not to feed Cloudflare's lockout") {
		t.Errorf("the skip has no line of its own:\n%s", rec.Lines())
	}
	if !rec.Has("explain", "Not checked because of this: dns records.") || report.Pruned != 1 || report.Count(Skip) != 1 {
		t.Errorf("pruned %d, skipped %d:\n%s", report.Pruned, report.Count(Skip), rec.Lines())
	}
}

// Every node that is ready runs at once: two with no needs both start
// before either ends.
func TestGraphRunsWhatIsReadyAtOnce(t *testing.T) {
	var started sync.WaitGroup
	started.Add(2)
	release := make(chan struct{})
	g := NewGraph(nil)
	for _, name := range []string{"a", "b"} {
		g.Add(Node{Name: name, Section: name, Gather: func(Input) func(*Input) {
			started.Done()
			<-release
			return nil
		}})
	}
	go func() {
		started.Wait()
		close(release)
	}()
	finished := make(chan struct{})
	go func() {
		g.Run(ui.Discard, 0)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("two ready nodes did not run at once")
	}
}

// A need on a node added after it is a mistake in the code that builds the
// graph, and panics there rather than hanging a run.
func TestGraphRefusesANeedNotAddedBefore(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()
	g := NewGraph(nil)
	g.Add(Node{Name: "b", Hard: []Need{Hard("a")}})
}

// fixtureReads answers the checks' reads from an Input, the way the hosts
// it records answered, and keeps every read it was asked.
type fixtureReads struct {
	in  Input
	log *events
	// wait, when set, is called with each read before it answers.
	wait func(read string)
	// standbyFrom is the sites Standby was told it may ask.
	standbyFrom map[string]bool
}

func (f *fixtureReads) asked(read string) {
	f.log.add(read)
	if f.wait != nil {
		f.wait(read)
	}
}

func (f *fixtureReads) Reach(site string) SiteReach {
	f.asked("reach " + site)
	for _, r := range f.in.Reach {
		if r.Site == site {
			return r
		}
	}
	return SiteReach{Site: site, Destination: "ubuntu@" + site + ".local"}
}

func (f *fixtureReads) Mesh(site string) MeshProbe {
	f.asked("mesh " + site)
	for _, p := range f.in.Mesh {
		if p.Site == site {
			return p
		}
	}
	return MeshProbe{Site: site, LinkErr: "not in the fixture"}
}

func (f *fixtureReads) EtcdHealth(member string) (string, string) {
	f.asked("etcd health " + member)
	if member == f.in.Etcd.Site {
		return f.in.Etcd.Health, ""
	}
	return "", "service \"etcd\" is not running"
}

func (f *fixtureReads) EtcdVersion(member string) (string, string) {
	f.asked("etcd version " + member)
	return f.in.Etcd.Version, f.in.Etcd.VersionErr
}

func (f *fixtureReads) PatroniCluster(site string) (string, string) {
	f.asked("patroni " + site)
	if out, ok := f.in.Patroni.Cluster[site]; ok {
		return out, ""
	}
	if why, ok := f.in.Patroni.ClusterErr[site]; ok {
		return "", why
	}
	return "", "connection refused"
}

func (f *fixtureReads) Sync(member string) (string, string) {
	f.asked("sync " + member)
	return f.in.Patroni.Sync, f.in.Patroni.SyncErr
}

func (f *fixtureReads) PatroniLog(site string) string {
	f.asked("patroni log " + site)
	return f.in.Patroni.Logs[site]
}

func (f *fixtureReads) Containers(site string) SiteContainers {
	f.asked("containers " + site)
	for _, c := range f.in.Containers {
		if c.Site == site {
			return c
		}
	}
	return SiteContainers{Site: site, PSErr: "not in the fixture"}
}

func (f *fixtureReads) Settings(site, app string) SettingsProbe {
	f.asked("settings " + site + " " + app)
	for _, p := range f.in.Settings {
		if p.Site == site && p.App == app {
			return p
		}
	}
	return SettingsProbe{Site: site, App: app, NotRunning: true}
}

func (f *fixtureReads) Standby(app string, reached map[string]bool) StandbyProbe {
	f.asked("standby " + app)
	f.standbyFrom = reached
	for _, p := range f.in.Standby {
		if p.App == app {
			return p
		}
	}
	return StandbyProbe{App: app, Instances: []apply.Instance{{Site: "home-a", State: apply.Active}}}
}

func (f *fixtureReads) Clock(site string) ClockSample {
	f.asked("clock " + site)
	for _, c := range f.in.Clocks {
		if c.Site == site {
			return c
		}
	}
	return ClockSample{Site: site, Err: "not in the fixture"}
}

func (f *fixtureReads) Leftovers(site string) LeftoverProbe {
	f.asked("leftovers " + site)
	return LeftoverProbe{Site: site}
}

func (f *fixtureReads) SelfCheck(site string) SelfCheckProbe {
	f.asked("self-check " + site)
	for _, p := range f.in.SelfChecks {
		if p.Site == site {
			return p
		}
	}
	return SelfCheckProbe{Site: site, Host: "talk.example.org", Address: "203.0.113.30"}
}

// fixtureConfig is the fixture the reads answer from, loaded once.
var fixtureConfig = sync.OnceValue(func() *config.Config {
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		panic(err)
	}
	return cfg
})

func (f *fixtureReads) Admission(site string) AdmissionProbe {
	f.asked("admission " + site)
	for _, p := range f.in.Admission {
		if p.Site == site {
			return p
		}
	}
	return AdmissionProbe{Site: site, Found: true, Record: admittedRecord(fixtureConfig(), site)}
}

func (f *fixtureReads) User(site string) UserProbe {
	f.asked("user " + site)
	for _, p := range f.in.Users {
		if p.Site == site {
			return p
		}
	}
	return UserProbe{Site: site, Out: "uid 1000\nuser ubuntu\ngroups ubuntu\n"}
}

func (f *fixtureReads) Docker(site string) DockerProbe {
	f.asked("docker " + site)
	for _, p := range f.in.Docker {
		if p.Site == site {
			return p
		}
	}
	return DockerProbe{Site: site, Out: rootlessDocker}
}

func (f *fixtureReads) Linger(site string) LingerProbe {
	f.asked("linger " + site)
	for _, p := range f.in.Linger {
		if p.Site == site {
			return p
		}
	}
	return LingerProbe{Site: site, Out: "linger yes\nmesh-order yes\n"}
}

func (f *fixtureReads) Sudo(site string) SudoProbe {
	f.asked("sudo " + site)
	for _, p := range f.in.SudoRights {
		if p.Site == site {
			return p
		}
	}
	return SudoProbe{Site: site, Out: sudoList(helperEntry)}
}

// fixtureInputs is every Input the section tests diagnose, by name.
func fixtureInputs() map[string]Input {
	out := map[string]Input{"healthy": healthyInput(), "stuck": stuckInput()}

	in := healthyInput()
	in.Etcd.Health = `[{"endpoint":"http://10.44.0.1:2379","health":false,"took":"5.0s","error":"context deadline exceeded"},{"endpoint":"http://10.44.0.2:2379","health":false,"took":"5.0s","error":"dial tcp 10.44.0.2:2379: connect: connection refused"},{"endpoint":"http://10.44.0.3:2379","health":true,"took":"3ms"}]
Error: unhealthy cluster`
	out["etcd without quorum"] = in

	in = healthyInput()
	in.Etcd.Health = `[{"endpoint":"http://10.44.0.1:2379","health":true},{"endpoint":"http://10.44.0.2:2379","health":true},{"endpoint":"http://10.44.0.3:2379","health":false,"error":"connection refused"}]`
	out["etcd one member down"] = in

	in = healthyInput()
	in.Etcd = EtcdProbe{Tried: []string{"home-a: service \"etcd\" is not running"}}
	out["etcd not reached"] = in

	in = healthyInput()
	in.Etcd.Health = `[{"endpoint":"http://10.44.0.1:2379","health":true},{"endpoint":"http://10.44.0.2:2379","health":true},{"endpoint":"http://10.44.0.3:2379","health":false,"error":"connection refused"}]`
	in.Etcd.Version = `{"etcdserver":"3.5.16","etcdcluster":"3.0.0"}`
	out["etcd version unsettled"] = in

	in = healthyInput()
	in.Etcd.Version = noCurl + "\n"
	out["etcd without curl"] = in

	in = stuckInput()
	in.Patroni.Sync = `{"leader":"home-b","quorum":0,"sync_standby":"home-a"}`
	out["no leader, replica in sync"] = in

	in = stuckInput()
	in.Reach = slices.Clone(in.Reach)
	in.Reach[0].Err = "Operation timed out"
	out["no database site reached"] = in

	in = healthyInput()
	in.Patroni.Cluster["home-a"] = strings.Replace(healthyCluster, `"lag": 0`, `"lag": 33554432`, 1)
	in.Patroni.Cluster["home-b"] = in.Patroni.Cluster["home-a"]
	out["lagging replica"] = in

	in = healthyInput()
	in.Patroni.Cluster = map[string]string{"home-a": strings.Replace(healthyCluster, `"role": "sync_standby"`, `"role": "replica"`, 1)}
	out["no sync standby"] = in
	return out
}

// For every fixture Input, the graph's judges give Diagnose's findings, in
// Diagnose's order: the graph changes when checks run and how they are
// printed, not what they find.
func TestGraphFindsWhatDiagnoseFinds(t *testing.T) {
	cfg := fixture(t)
	for name, in := range fixtureInputs() {
		t.Run(name, func(t *testing.T) {
			want := Diagnose(cfg, in)
			got := Checks(cfg, in.Sites, nil).Judge(in, true)
			if !reflect.DeepEqual(got.Findings, want.Findings) {
				t.Errorf("graph:\n%s\nDiagnose:\n%s", dump(got.Findings), dump(want.Findings))
			}
		})
	}
}

// What a run reports is its nodes' judges over the Input it gathered, with
// the pruned nodes left out: the record is complete.
func TestGraphRunReportsItsInput(t *testing.T) {
	cfg := fixture(t)
	for name, fix := range fixtureInputs() {
		t.Run(name, func(t *testing.T) {
			g := Checks(cfg, fix.Sites, &fixtureReads{in: fix, log: &events{}})
			report, in := g.Run(ui.Discard, 0)
			again := g.Judge(in, false)
			if !reflect.DeepEqual(report.Findings, again.Findings) || report.Pruned != again.Pruned {
				t.Errorf("run:\n%s\njudged again:\n%s", dump(report.Findings), dump(again.Findings))
			}
		})
	}
}

// A site that does not answer is one ✗ line, which says once what was not
// checked because of it. None of those checks has a line, none reads the
// site, and the summary counts each as skipped.
func TestPruningIsReportedOnce(t *testing.T) {
	cfg := fixture(t)
	fix := stuckInput()
	reads := &fixtureReads{in: fix, log: &events{}}
	rec := &ui.Recorder{}
	report, _ := Checks(cfg, fix.Sites, reads).Run(rec, 0)

	var lines []string
	for _, e := range rec.Events {
		if e.Kind == "step" && strings.Contains(e.Text, "home-b") {
			lines = append(lines, e.Text)
		}
	}
	if !reflect.DeepEqual(lines, []string{"check reach home-b"}) {
		t.Errorf("home-b's lines are %v, want reach alone:\n%s", lines, rec.Lines())
	}
	want := "Not checked because of this: admission, user, docker, docker engine, linger, sudo rights, mesh, patroni, containers, settings talk, clocks, leftovers on home-b."
	if !rec.Has("explain", want) {
		t.Errorf("reach home-b does not say what it kept from running, want %q:\n%s", want, rec.Lines())
	}
	// No etcd member answered in this fixture either, which kept etcd
	// version from running: every pruned check is on one list, once.
	listed := 0
	for _, e := range rec.Events {
		if list, ok := strings.CutPrefix(e.Text, "Not checked because of this: "); ok && e.Kind == "explain" {
			listed += strings.Count(list, ",") + strings.Count(list, ";") + 1
		}
	}
	if report.Pruned != 13 || listed != report.Pruned || report.Count(Skip) != 1 || !strings.Contains(report.Summary(), "14 skipped") {
		t.Errorf("pruned %d, listed %d, summary %q", report.Pruned, listed, report.Summary())
	}
	for _, read := range reads.log.list {
		if strings.HasSuffix(read, " home-b") && read != "reach home-b" {
			t.Errorf("read home-b after it did not answer: %s", read)
		}
	}
}

// A site that answers but is pending is one warning, with the site request
// line, and nothing more is read from it.
func TestAPendingSiteIsCheckedNoFurther(t *testing.T) {
	cfg := fixture(t)
	fix := healthyInput()
	fix.Admission = []AdmissionProbe{{Site: "home-b", Class: topology.Site{Name: "home-b", Class: topology.Pending, Why: "home-b: its host has not admitted this deployment", Request: true}}}
	// home-a leads a cluster home-b has not joined, as it would while
	// home-b is pending.
	fix.Patroni.Cluster = map[string]string{"home-a": homeALeadsAlone, "home-b": homeALeadsAlone}
	fix.Standby = []StandbyProbe{{App: "auth", Instances: []apply.Instance{{Site: "home-a", State: apply.Active}}}}
	reads := &fixtureReads{in: fix, log: &events{}}
	rec := &ui.Recorder{}
	report, _ := Checks(cfg, fix.Sites, reads).Run(rec, 0)

	f := find(t, report.Findings, SectionAdmission, "home-b: pending: this deployment is not admitted on its host")
	if f.Level != Warn || !strings.Contains(strings.Join(f.More, "\n"), "`paisans site request --site home-b > home-b.admit`") {
		t.Errorf("home-b: %+v", f)
	}
	want := "Not checked because of this: user, docker, docker engine, linger, sudo rights, mesh, patroni, containers, settings talk, clocks, leftovers on home-b."
	if !rec.Has("explain", want) {
		t.Errorf("admission home-b does not say what it kept from running, want %q:\n%s", want, rec.Lines())
	}
	for _, read := range reads.log.list {
		if strings.HasSuffix(read, " home-b") && read != "reach home-b" && read != "admission home-b" {
			t.Errorf("read home-b after it was found pending: %s", read)
		}
	}
	for _, f := range report.Findings {
		if f.Section == SectionReach || f.Section == SectionAdmission {
			continue
		}
		if strings.Contains(f.Line+"\n"+strings.Join(f.More, "\n"), "home-b") {
			t.Errorf("a finding past admission names the pending home-b: %+v", f)
		}
	}
}

// homeALeadsAlone is a cluster home-a leads with no other member.
const homeALeadsAlone = `{"members": [{"name": "home-a", "role": "leader", "state": "running", "api_url": "http://10.44.0.1:8008/patroni", "host": "10.44.0.1", "port": 5432, "timeline": 3}], "scope": "paisans"}`

// A data site whose Docker does not answer is not in the cluster because
// of that, and Patroni says so rather than pointing at a containers check
// that never ran there.
func TestPatroniNamesADataSiteWhoseDockerDoesNotAnswer(t *testing.T) {
	cfg := fixture(t)
	fix := healthyInput()
	fix.Patroni.Cluster = map[string]string{"home-a": homeALeadsAlone, "home-b": homeALeadsAlone}
	fix.Docker = []DockerProbe{{Site: "home-b", Out: "host unix:///run/user/1000/docker.sock\n", Err: "Cannot connect to the Docker daemon at unix:///run/user/1000/docker.sock. Is the docker daemon running?"}}
	report, _ := Checks(cfg, fix.Sites, &fixtureReads{in: fix, log: &events{}}).Run(&ui.Recorder{}, 0)
	f := find(t, report.Findings, SectionPatroni, "home-b: not a cluster member")
	if !strings.Contains(f.Line, "its Docker does not answer: see docker") {
		t.Errorf("home-b: %+v", f)
	}
}

// A check that reads across sites asks only the sites whose admission
// passed: a pending site that answers ssh is never asked for etcd's health,
// Patroni's log or a Pocket ID instance, even as the first etcd member.
func TestAPendingSiteIsNotAskedAcrossSites(t *testing.T) {
	cfg := fixture(t)
	cfg.Etcd.Members = []string{"home-b", "home-a", "vm"}
	fix := healthyInput()
	fix.Admission = []AdmissionProbe{{Site: "home-b", Class: pendingClass("home-b")}}
	// No member names a leader, so the patroni check reads each data
	// site's log.
	fix.Patroni.Cluster = map[string]string{"home-a": strings.Replace(healthyCluster, `"role": "leader"`, `"role": "replica"`, 1)}
	reads := &fixtureReads{in: fix, log: &events{}}
	report, _ := Checks(cfg, fix.Sites, reads).Run(&ui.Recorder{}, 1)

	for _, read := range reads.log.list {
		if strings.HasSuffix(read, " home-b") && read != "reach home-b" && read != "admission home-b" {
			t.Errorf("read home-b after it was found pending: %s", read)
		}
	}
	if !reads.log.has("patroni log home-a") {
		t.Errorf("the patroni check did not read home-a's log: %v", reads.log.list)
	}
	if reads.standbyFrom["home-b"] || !reads.standbyFrom["home-a"] {
		t.Errorf("standby may ask %v, want home-a and never home-b", reads.standbyFrom)
	}
	find(t, report.Findings, SectionEtcd, "asked from home-a")

	// With no member answering, etcd says why it did not ask home-b.
	fix.Etcd = EtcdProbe{}
	reads = &fixtureReads{in: fix, log: &events{}}
	report, _ = Checks(cfg, fix.Sites, reads).Run(&ui.Recorder{}, 1)
	f := find(t, report.Findings, SectionEtcd, "no etcd member reached")
	if !strings.Contains(strings.Join(f.More, "\n"), "home-b: not asked: its admission did not pass (see admission)") {
		t.Errorf("etcd does not say why home-b was not asked: %+v", f)
	}
}

// classifiedReads is fixtureReads with the run's classification, as
// cmd/paisans has it once a site did not answer.
type classifiedReads struct {
	*fixtureReads
	c *topology.Classification
}

func (r classifiedReads) Classification() *topology.Classification { return r.c }

// A site that answers and whose record covers it is still pending when its
// every route into the mesh is through a pending site: here home-a, which
// dials out only, while home-b, vm and watch, the sites with an endpoint,
// refuse its login. The classification places it, from evidence a real run
// reads, and no check across sites asks it.
func TestARelayPendingSiteIsNotAskedAcrossSites(t *testing.T) {
	cfg := fixture(t)
	refused := errors.Join(apply.ErrLoginRefused, errors.New("Permission denied (publickey)"))
	ev := topology.Evidence{Login: map[string]error{}, Records: map[string]topology.RecordRead{}, Joined: map[string][]string{}}
	fix := healthyInput()
	fix.Sites = cfg.SiteNames()
	fix.Reach = nil
	for _, name := range cfg.SiteNames() {
		if cfg.Sites[name].DialEndpoint() == "" {
			ev.Records[name] = topology.RecordRead{Record: admittedRecord(cfg, name)}
			fix.Reach = append(fix.Reach, SiteReach{Site: name, Destination: "ubuntu@" + name + ".local"})
			continue
		}
		ev.Login[name] = refused
		fix.Reach = append(fix.Reach, SiteReach{Site: name, Destination: "ubuntu@" + name + ".local", Err: "Permission denied (publickey)."})
	}
	c, err := topology.Classify(cfg, ev)
	if err != nil {
		t.Fatal(err)
	}
	if c.Sites["home-a"].Class != topology.Pending {
		t.Fatalf("the fixture's home-a is %v, want pending through its relays", c.Sites["home-a"].Class)
	}
	fix.Etcd = EtcdProbe{}
	reads := &fixtureReads{in: fix, log: &events{}}
	report, in := Checks(cfg, fix.Sites, classifiedReads{reads, c}).Run(&ui.Recorder{}, 1)

	if reads.log.has("etcd health home-a") {
		t.Errorf("asked relay-pending home-a for etcd's health: %v", reads.log.list)
	}
	if reads.standbyFrom["home-a"] {
		t.Errorf("standby may ask %v, never relay-pending home-a", reads.standbyFrom)
	}
	if in.Admitted(cfg)["home-a"] {
		t.Errorf("relay-pending home-a is admitted for checks across sites")
	}
	f := find(t, report.Findings, SectionEtcd, "no etcd member reached")
	if !strings.Contains(strings.Join(f.More, "\n"), "home-a: "+notClassified) {
		t.Errorf("etcd does not say why home-a was not asked: %+v", f)
	}
}

// A daemon that does not answer stops what runs docker on its site, and
// nothing else there.
func TestDockerDownStopsWhatRunsDocker(t *testing.T) {
	cfg := fixture(t)
	fix := healthyInput()
	fix.Docker = []DockerProbe{{Site: "home-a", Out: "host unix:///run/user/1000/docker.sock\n", Err: "Cannot connect to the Docker daemon"}}
	reads := &fixtureReads{in: fix, log: &events{}}
	rec := &ui.Recorder{}
	Checks(cfg, fix.Sites, reads).Run(rec, 0)

	for _, read := range []string{"containers home-a", "settings home-a talk", "leftovers home-a", "patroni home-a"} {
		if reads.log.has(read) {
			t.Errorf("%s ran although home-a's Docker did not answer", read)
		}
	}
	for _, read := range []string{"linger home-a", "sudo home-a", "mesh home-a", "clock home-a", "containers home-b"} {
		if !reads.log.has(read) {
			t.Errorf("%s did not run", read)
		}
	}
	if !rec.Has("explain", "Not checked because of this: docker engine, patroni, containers, settings talk, leftovers on home-a.") {
		t.Errorf("docker home-a does not say what it kept from running:\n%s", rec.Lines())
	}
}

// The gateway is asked from outside even when its ssh does not answer.
func TestSelfCheckNeedsNoSsh(t *testing.T) {
	cfg := fixture(t)
	fix := healthyInput()
	fix.Reach = []SiteReach{
		{Site: "home-a", Destination: "ubuntu@home-a.local"},
		{Site: "home-b", Destination: "ubuntu@home-b.local"},
		{Site: "vm", Destination: "ubuntu@vm.example.org", Err: "ssh: connect to host vm.example.org port 22: Operation timed out"},
	}
	reads := &fixtureReads{in: fix, log: &events{}}
	Checks(cfg, fix.Sites, reads).Run(ui.Discard, 0)
	if !reads.log.has("self-check vm") {
		t.Errorf("the gateway was not asked from outside: %v", reads.log.list)
	}
	if reads.log.has("self-check home-a") {
		t.Errorf("a site with no Caddy of the toolkit's was asked: %v", reads.log.list)
	}
}

// A monitor running Caddy that the classification found down is still
// asked from outside, and a failure there is a warning pointing to reach,
// as the monitor's absence is, never a second failure.
func TestSelfCheckOfADownMonitorWarns(t *testing.T) {
	cfg := fixture(t)
	fix := healthyInput()
	fix.Sites = append(fix.Sites, "watch")
	fix.Reach = append(fix.Reach, SiteReach{Site: "watch", Destination: "ubuntu@watch.example.org", Err: "ssh: connect to host watch.example.org port 22: Operation timed out"})
	fix.SelfChecks = []SelfCheckProbe{{Site: "watch", Host: "health.example.org", Address: "203.0.113.20",
		Err: "nothing answers on 443 at 203.0.113.20: dial tcp 203.0.113.20:443: i/o timeout"}}
	c := &topology.Classification{Sites: map[string]topology.Site{
		"watch": {Name: "watch", Class: topology.Down, Why: topology.DownLine("watch")}}}
	reads := &fixtureReads{in: fix, log: &events{}}
	report, _ := Checks(cfg, fix.Sites, classifiedReads{reads, c}).Run(&ui.Recorder{}, 1)
	if !reads.log.has("self-check watch") {
		t.Errorf("down watch was not asked from outside: %v", reads.log.list)
	}
	f := find(t, report.Findings, SectionSelfCheck, "watch:")
	if f.Level != Warn {
		t.Errorf("down watch's self-check is %v, want a warning:\n%s", f.Level, dump(report.Findings))
	}
	if more := strings.Join(f.More, "\n"); !strings.Contains(more, "(see reach)") {
		t.Errorf("down watch's self-check does not point to reach:\n%s", more)
	}
}

// A gateway the classification found pending was never deployed, so it is
// not asked from outside: a warning says admission is why.
func TestSelfCheckSkipsAPendingGateway(t *testing.T) {
	cfg := fixture(t)
	fix := healthyInput()
	c := &topology.Classification{Sites: map[string]topology.Site{
		"vm": {Name: "vm", Class: topology.Pending, Request: true}}}
	reads := &fixtureReads{in: fix, log: &events{}}
	report, _ := Checks(cfg, fix.Sites, classifiedReads{reads, c}).Run(&ui.Recorder{}, 1)
	if reads.log.has("self-check vm") {
		t.Errorf("pending vm was asked from outside: %v", reads.log.list)
	}
	f := find(t, report.Findings, SectionSelfCheck, "vm:")
	if f.Level != Warn || !strings.Contains(f.Line, "admission") {
		t.Errorf("pending vm's self-check is %v %q, want a warning naming admission", f.Level, f.Line)
	}
}

// With one node at a time, the nodes run in the graph's order, every site's
// check of one kind before the next kind, which is the order doctor read
// in before it ran in parallel, and the lines print in Diagnose's order.
func TestBoundOfOneRunsInOrder(t *testing.T) {
	cfg := fixture(t)
	fix := healthyInput()
	reads := &fixtureReads{in: fix, log: &events{}}
	rec := &ui.Recorder{}
	report, in := Checks(cfg, fix.Sites, reads).Run(rec, 1)
	want := []string{
		"reach home-a", "reach home-b", "reach vm",
		"admission home-a", "admission home-b", "admission vm",
		"user home-a", "user home-b", "user vm",
		"docker home-a", "docker home-b", "docker vm",
		"linger home-a", "linger home-b", "linger vm",
		"sudo home-a", "sudo home-b", "sudo vm",
		"mesh home-a", "mesh home-b", "mesh vm",
		"etcd health home-a", "etcd version home-a",
		"patroni home-a", "patroni home-b", "sync home-a",
		"containers home-a", "containers home-b", "containers vm",
		"settings home-a talk", "settings home-b talk",
		"standby auth",
		"clock home-a", "clock home-b", "clock vm",
		"leftovers home-a", "leftovers home-b", "leftovers vm",
		"self-check vm",
	}
	if !reflect.DeepEqual(reads.log.list, want) {
		t.Errorf("reads in order:\n%s\nwant:\n%s", strings.Join(reads.log.list, "\n"), strings.Join(want, "\n"))
	}
	// The lines are the findings, in Diagnose's order.
	var titles []string
	for _, e := range rec.Events {
		if e.Kind == "step" {
			titles = append(titles, e.Text)
		}
	}
	var findings []string
	for _, f := range Diagnose(cfg, in).Findings {
		findings = append(findings, f.Section)
	}
	var sections []string
	for _, f := range report.Findings {
		sections = append(sections, f.Section)
	}
	if !reflect.DeepEqual(sections, findings) || len(titles) != len(report.Findings) {
		t.Errorf("lines %v\nfindings %v\nDiagnose %v", titles, sections, findings)
	}
}

// On a terminal, a check that ends first prints first, whatever the graph's
// order, and each line prints whole, never broken by another's.
func TestStreamingPrintsAsChecksEnd(t *testing.T) {
	cfg := fixture(t)
	fix := stuckInput()
	releaseA := make(chan struct{})
	reads := &fixtureReads{in: fix, log: &events{}, wait: func(read string) {
		if read == "reach home-a" {
			<-releaseA
		}
	}}
	var out bytes.Buffer
	rep := ui.NewTerminalForTest(&out, false, func() time.Time { return time.Unix(0, 0) })
	done := make(chan struct{})
	go func() {
		Checks(cfg, []string{"home-a", "home-b"}, reads).Run(rep, 0)
		close(done)
	}()
	// home-b's failure ends before home-a answers.
	for !reads.log.has("reach home-b") {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(releaseA)
	<-done
	text := out.String()
	b := strings.Index(text, "✗\x1b[0m check reach home-b: ssh to ubuntu@home-b.local did not answer (Operation timed out)")
	a := strings.Index(text, "✓\x1b[0m check reach home-a: ubuntu@home-a.local answers")
	if a < 0 || b < 0 || b > a {
		t.Errorf("home-b's failure, which ended first, does not print first (%d, %d):\n%q", b, a, text)
	}
}

func ExampleGraph_notChecked() {
	g := NewGraph(nil)
	for _, n := range []Node{
		{Name: "mesh home-b", Site: "home-b"},
		{Name: "settings home-b talk", Site: "home-b"},
		{Name: "etcd"},
	} {
		g.Add(n)
	}
	fmt.Println(g.notChecked([]int{0, 1, 2}))
	// Output: Not checked because of this: mesh, settings talk on home-b; etcd.
}
