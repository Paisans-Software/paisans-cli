package siteadd_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/siteadd"
)

const witHeartbeat = "/srv/paisans/f2a9/infra/heartbeat/heartbeat.env"

// newWitnessWorld is the deployment before a join that brings in a new
// witness site as well as the new data site: home-a alone in the cluster and
// in etcd, vm a gateway that stays one, watch the monitor, and home-b and
// wit, a witness on a host of its own, both prepared and empty.
func newWitnessWorld(t *testing.T) *world {
	t.Helper()
	cfg, secrets := endState(t)
	vm := cfg.Sites["vm"]
	vm.Roles = []config.Role{config.RoleGateway}
	cfg.Sites["vm"] = vm
	cfg.Sites["wit"] = config.Site{
		Roles:    []config.Role{config.RoleWitness},
		Address:  "10.44.0.5",
		Endpoint: "198.51.100.30:51820",
		SSH:      vm.SSH,
	}
	cfg.Etcd.Members = []string{"home-a", "home-b", "wit"}
	secrets.Sites["wit"] = config.SiteSecrets{WireGuardPrivateKey: "REREREREREREREREREREREREREREREREREREREREREQ=", HeartbeatToken: "4444444444444444444444444444eeee", BeszelToken: "b444444444444444444444444444eeeeb444444444444444444444444444eeee"}

	w := &world{t: t, cfg: cfg, secrets: secrets, hosts: map[string]*host{}, clusterID: 0xc1a5, nextID: 1,
		running: map[string]bool{"home-a": true}, patroni: map[string]bool{"home-a": true},
		served: map[string]string{}, etcdHosts: map[string]string{}, starting: map[string]int{}}
	for _, name := range cfg.SiteNames() {
		w.hosts[name] = &host{w: w, name: name, files: map[string]string{}}
	}

	before, _ := endState(t)
	delete(before.Sites, "home-b")
	before.Sites["vm"] = vm
	before.Cluster.Sites = []string{"home-a"}
	before.Etcd.Members = []string{"home-a"}
	rendered, err := render.Build(before, secrets)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"home-a", "vm", "watch"} {
		p, err := apply.Build(name, rendered, "", w.hosts[name])
		if err != nil {
			t.Fatal(err)
		}
		if err := apply.Execute(p, w.hosts[name]); err != nil {
			t.Fatal(err)
		}
		w.hosts[name].commands = nil
		w.hosts[name].writes = 0
	}
	w.served["home-a"] = w.hosts["home-a"].files["/srv/paisans/f2a9/infra/haproxy/haproxy.cfg"]
	w.etcd = []apply.EtcdMember{{ID: 1, Name: "home-a", PeerURLs: []string{"http://10.44.0.1:2380"}, ClientURLs: []string{"http://10.44.0.1:2379"}}}
	w.members = []map[string]any{{"Member": "home-a", "Role": "Leader", "State": "running", "Replay Lag": ""}}
	return w
}

// A new witness joins in the data site's join: preflight checks its blank host
// with the new site's, the mesh stage brings it in and gates on it, and the
// etcd stage adds it first, a learner started with the membership it joins
// and promoted, before the data site is added.
func TestAJoinBringsInANewWitnessFirst(t *testing.T) {
	defer siteadd.SetFast()()
	var checked [][]string
	defer siteadd.RecordPreflight(&checked)()
	w := newWitnessWorld(t)
	p := build(t, w)

	if len(checked) != 1 || strings.Join(checked[0], ",") != "home-b,wit" {
		t.Errorf("preflight checked %v, want the new site and the new witness", checked)
	}
	for _, want := range []struct {
		stage      int
		site, verb string
	}{
		{2, "wit", "create"}, {2, "wit", "mesh"},
		{3, "wit", "add"}, {3, "wit", "write"}, {3, "wit", "start"}, {3, "wit", "promote"},
		{3, "home-b", "add"},
	} {
		if !hasStep(p, want.stage, want.site, want.verb) {
			t.Errorf("stage %d has no %s %s step:\n%v", want.stage, want.verb, want.site, steps(p, want.stage))
		}
	}
	var order []string
	for _, s := range steps(p, 3) {
		if s.Verb == "add" {
			order = append(order, s.Site)
		}
	}
	if strings.Join(order, ",") != "wit,home-b" {
		t.Errorf("learners added in the order %v, want the witness first", order)
	}
	for _, s := range steps(p, 4) {
		if s.Site == "wit" {
			t.Errorf("a witness has no replica, and stage 4 plans one on it: %v", s)
		}
	}

	if err := siteadd.Execute(p); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(w.voters(), ","); got != "home-a,home-b,wit" {
		t.Errorf("voters %s", got)
	}
	// The witness was added and promoted before the data site was added.
	var adds []string
	for _, c := range w.hosts["home-a"].commands {
		switch {
		case strings.Contains(c, "member add"):
			adds = append(adds, c)
		case strings.Contains(c, "member promote"):
			adds = append(adds, "promote")
		}
	}
	if len(adds) < 3 || !strings.Contains(adds[0], "member add wit ") || adds[1] != "promote" || !strings.Contains(adds[2], "member add home-b ") {
		t.Errorf("etcd changes on home-a, in order:\n%s", strings.Join(adds, "\n"))
	}
	wit := w.hosts["wit"]
	compose := wit.files["/srv/paisans/f2a9/infra/compose.yaml"]
	if !strings.Contains(compose, "--initial-cluster-state=existing") || !strings.Contains(compose, "--initial-cluster=home-a=http://10.44.0.1:2380,wit=http://10.44.0.5:2380\n") {
		t.Errorf("wit's etcd was not started as a joiner of two:\n%s", compose)
	}
	// compose reads every env_file as it loads the project, so the heartbeat
	// file the witness's stack names is written before its etcd starts.
	if _, ok := wit.files[witHeartbeat]; !ok {
		t.Error("wit's heartbeat.env was not written, so compose could not load its stack to start etcd")
	}
	if !slices.ContainsFunc(wit.commands, func(c string) bool { return strings.HasSuffix(c, "up -d etcd") }) {
		t.Error("wit's etcd was not started")
	}
	for _, c := range wit.commands {
		if strings.HasSuffix(c, "compose.yaml up -d") {
			t.Errorf("wit's whole stack was started: %s", c)
		}
	}

	// A finished join plans nothing more, and does not check the witness's
	// host as a blank one again.
	checked = nil
	again := build(t, w)
	if again.Pending() {
		t.Error("a finished join with a new witness still plans work")
	}
	if len(checked) != 0 {
		t.Errorf("a finished join ran preflight again: %v", checked)
	}
}

// A join stopped after the witness was added and before it started resumes
// with the witness: it is not added again, and its host is not checked as a
// blank one, since the mesh stage has already written to it.
func TestAJoinStoppedAtTheNewWitnessResumes(t *testing.T) {
	defer siteadd.SetFast()()
	w := newWitnessWorld(t)
	w.failOnce = "up -d etcd"
	if err := siteadd.Execute(build(t, w)); err == nil || !strings.Contains(err.Error(), "stage 3") {
		t.Fatalf("want a stage 3 failure, got %v", err)
	}
	if got := strings.Join(w.voters(), ","); got != "home-a" {
		t.Errorf("voters after the stop: %s", got)
	}
	var checked [][]string
	defer siteadd.RecordPreflight(&checked)()
	p := build(t, w)
	if len(checked) != 0 {
		t.Errorf("a started join ran preflight again: %v", checked)
	}
	// The witness's learner never started, so it is removed and added
	// again under a new ID rather than started under the old one.
	if !hasStep(p, 3, "home-a", "remove") || !hasStep(p, 3, "wit", "add") || !hasStep(p, 3, "wit", "start") {
		t.Errorf("the witness's unstarted learner is not removed, added again and started: %v", steps(p, 3))
	}
	if err := siteadd.Execute(p); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(w.voters(), ","); got != "home-a,home-b,wit" {
		t.Errorf("voters %s", got)
	}
}

// site add on a witness refuses, saying it joins in a data site's site add.
func TestSiteAddOnAWitnessSaysWhereItJoins(t *testing.T) {
	w := newWitnessWorld(t)
	_, err := siteadd.Build(w.cfg, w.secrets, "wit", w.transports())
	if err == nil || !strings.Contains(err.Error(), "site add <data site>") {
		t.Errorf("site add on a witness: %v", err)
	}
}
