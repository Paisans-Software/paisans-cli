package siteadd_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/siteadd"
)

const etcdRecord = "/srv/paisans/f2a9/infra/etcd-initial"

// removedWitnessWorld is newWorld with vm a witness that left etcd earlier:
// it once joined through a site add, with home-c, a data site since removed,
// and its host kept the data and the record of that join.
func removedWitnessWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	vm := w.hosts["vm"]
	vm.files[etcdDB] = "member 7f3a, removed"
	vm.files[etcdData+"/member/wal/0.wal"] = "wal"
	vm.files[etcdData+"/.lock"] = "lock"
	// The record as that join's apply wrote it, in vm's manifest.
	stale := render.EtcdInitial{
		State:   render.EtcdStateExisting,
		Cluster: "home-a=http://10.44.0.1:2380,home-c=http://10.44.0.9:2380,vm=http://10.44.0.3:2380",
	}
	rendered, err := render.Build(w.cfg, w.secrets, render.WithEtcdInitial("vm", stale))
	if err != nil {
		t.Fatal(err)
	}
	ap, err := apply.Build("vm", rendered, "", vm, apply.Scope(render.EtcdInitialPath(w.cfg.Deployment())))
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(ap, vm); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(vm.files[etcdRecord], "home-c") {
		t.Fatalf("vm's record is not the earlier join's:\n%s", vm.files[etcdRecord])
	}
	vm.commands, vm.writes = nil, 0
	return w
}

func stepText(p *siteadd.Plan, stage int, site, verb string) string {
	for _, s := range steps(p, stage) {
		if s.Site == site && s.Verb == verb {
			return s.Text
		}
	}
	return ""
}

func ranIndex(h *host, sub string) int {
	for i, c := range h.commands {
		if strings.Contains(c, sub) {
			return i
		}
	}
	return -1
}

// A site whose etcd member was removed and whose host kept the data rejoins
// from an empty data directory: the plan names the clear, the data goes
// before its etcd starts, its dotfiles too, and it is started with the
// membership as it stands rather than the record of its earlier join. Stage
// 3 passes.
func TestARemovedMemberRejoinsFromClearedData(t *testing.T) {
	defer siteadd.SetFast()()
	w := removedWitnessWorld(t)
	p := build(t, w)

	text := stepText(p, 3, "vm", "clear")
	for _, want := range []string{"vm's old etcd data", "/srv/paisans/f2a9/infra/etcd", "a removed member's, which etcd refuses to rejoin with", `rootlesskit rm -rf -- "$f"`} {
		if !strings.Contains(text, want) {
			t.Errorf("the plan's clear step on vm lacks %q:\n%v", want, steps(p, 3))
		}
	}
	var order []string
	for _, s := range steps(p, 3) {
		if s.Site == "vm" {
			order = append(order, s.Verb)
		}
	}
	if strings.Join(order, ",") != "clear,add,write,start,promote" {
		t.Errorf("vm's etcd steps run in the order %v", order)
	}
	if !strings.Contains(stepText(p, 3, "vm", "write"), "--initial-cluster=home-a=http://10.44.0.1:2380,vm=http://10.44.0.3:2380") {
		t.Errorf("vm is planned with the record of its earlier join: %s", stepText(p, 3, "vm", "write"))
	}

	if err := siteadd.Execute(p); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(w.voters(), ","); got != "home-a,home-b,vm" {
		t.Errorf("voters %s", got)
	}
	vm := w.hosts["vm"]
	for path, content := range vm.files {
		if strings.HasPrefix(path, etcdData+"/") && path != etcdDB {
			t.Errorf("%s is left from the removed member", path)
		}
		if path == etcdDB && strings.Contains(content, "removed") {
			t.Error("vm's etcd still holds the removed member's data")
		}
	}
	cleared, started := ranIndex(vm, "rootlesskit rm -rf"), ranIndex(vm, "up -d etcd")
	if cleared < 0 || started < cleared {
		t.Errorf("the data was cleared at %d and etcd started at %d", cleared, started)
	}
	if !strings.Contains(vm.commands[cleared], "docker stop $ids") {
		t.Errorf("vm's etcd container is not removed before its data: %s", vm.commands[cleared])
	}
	if got := w.hosts["home-a"].ran("rootlesskit rm -rf"); got != 0 {
		t.Errorf("home-a, a voter, had its etcd data cleared %d times", got)
	}
}

// A clear that fails stops the join before the learner is added, so the
// cluster is unchanged and the next run clears it again.
func TestAFailedClearStopsBeforeTheLearnerIsAdded(t *testing.T) {
	defer siteadd.SetFast()()
	w := removedWitnessWorld(t)
	w.failOnce = "rootlesskit rm -rf"
	err := siteadd.Execute(build(t, w))
	if err == nil || !strings.Contains(err.Error(), "stage 3") || !strings.Contains(err.Error(), "clearing its old etcd data") {
		t.Fatalf("want a stage 3 stop at the clear, got %v", err)
	}
	if w.hosts["home-a"].ran("member add vm ") != 0 {
		t.Error("vm was added to etcd though its data was not cleared")
	}
	if ranIndex(w.hosts["vm"], "up -d etcd") >= 0 {
		t.Error("vm's etcd was started though its data was not cleared")
	}
	if err := siteadd.Execute(build(t, w)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(w.voters(), ","); got != "home-a,home-b,vm" {
		t.Errorf("voters after the second run %s", got)
	}
}

// A new site with no etcd data is cleared all the same: there is nothing to
// delete, and nothing else on its host is touched.
func TestAFreshJoinerIsClearedHarmlessly(t *testing.T) {
	defer siteadd.SetFast()()
	w := newWorld(t)
	p := build(t, w)
	if stepText(p, 3, "home-b", "clear") == "" {
		t.Errorf("home-b, not in etcd, has no clear step: %v", steps(p, 3))
	}
	if err := siteadd.Execute(p); err != nil {
		t.Fatal(err)
	}
	b := w.hosts["home-b"]
	if b.ran("rootlesskit rm -rf") != 1 {
		t.Errorf("home-b was cleared %d times", b.ran("rootlesskit rm -rf"))
	}
	if _, ok := b.files["/srv/paisans/f2a9/infra/compose.yaml"]; !ok {
		t.Error("the clear took a file outside etcd's data directory")
	}
}

// A site already in etcd's member list is never cleared: a voter, nor a
// learner a stopped run added. Its data is its own.
func TestAMemberInTheListIsNeverCleared(t *testing.T) {
	defer siteadd.SetFast()()
	w := removedWitnessWorld(t)
	w.failOnce = "up -d etcd"
	if err := siteadd.Execute(build(t, w)); err == nil || !strings.Contains(err.Error(), "stage 3") {
		t.Fatalf("want a stage 3 failure, got %v", err)
	}
	vm := w.hosts["vm"]
	if vm.ran("rootlesskit rm -rf") != 1 {
		t.Fatalf("vm was cleared %d times before its add", vm.ran("rootlesskit rm -rf"))
	}
	// What the resumed run must leave alone: anything in the data directory
	// of vm, a learner now, and home-a's, a voter's.
	vm.files[etcdData+"/.marker"] = "kept"
	w.hosts["home-a"].files[etcdData+"/.marker"] = "kept"

	p := build(t, w)
	for _, site := range []string{"home-a", "vm"} {
		if hasStep(p, 3, site, "clear") {
			t.Errorf("%s, in the member list, is planned a clear: %v", site, steps(p, 3))
		}
	}
	if !hasStep(p, 3, "home-b", "clear") {
		t.Errorf("home-b, not in the member list, has no clear: %v", steps(p, 3))
	}
	if err := siteadd.Execute(p); err != nil {
		t.Fatal(err)
	}
	if vm.ran("rootlesskit rm -rf") != 1 {
		t.Errorf("vm, a learner on the resumed run, was cleared again")
	}
	for _, site := range []string{"home-a", "vm"} {
		if w.hosts[site].files[etcdData+"/.marker"] != "kept" {
			t.Errorf("%s's etcd data was touched", site)
		}
	}
	if got := strings.Join(w.voters(), ","); got != "home-a,home-b,vm" {
		t.Errorf("voters %s", got)
	}
}
