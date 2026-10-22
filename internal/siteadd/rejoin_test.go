package siteadd_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/apply/etcdwaltest"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/siteadd"
)

const etcdRecord = "/srv/paisans/f2a9/infra/etcd-initial"

const etcdRemovedFile = "/srv/paisans/f2a9/.paisans/etcd-removed"

// removedWitnessWorld is newWorld with vm a witness that left etcd earlier:
// it once joined through a site add, with home-c, a data site since removed,
// and its host kept the data and the record of that join. home-a's host
// records the removal of vm's member, 7f3a, as site remove does.
func removedWitnessWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	vm := w.hosts["vm"]
	vm.files[etcdDB] = "member 7f3a, removed"
	vm.files[etcdWAL] = string(etcdwaltest.Head(0x7f3a, w.clusterID))
	vm.files[etcdData+"/member/wal/0.tmp"] = "preallocated"
	w.hosts["home-a"].files[etcdRemovedFile] = "vm c1a5 7f3a\n"
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
	if strings.Join(order, ",") != "stop,clear,add,write,start,promote" {
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
		if strings.HasPrefix(path, etcdData+"/") && path != etcdDB && path != etcdWAL {
			t.Errorf("%s is left from the removed member", path)
		}
		if path == etcdDB && strings.Contains(content, "removed") {
			t.Error("vm's etcd still holds the removed member's data")
		}
	}
	if string(etcdwaltest.Head(0x7f3a, w.clusterID)) == vm.files[etcdWAL] {
		t.Error("vm's write-ahead log is still the removed member's")
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

// A learner an earlier run added and that never started, the state a
// rejoin left before site add cleared old data, is cleared before it is
// started: it is not added again, and stage 3 passes.
func TestAnUnstartedLearnerFromAnEarlierRunIsCleared(t *testing.T) {
	defer siteadd.SetFast()()
	w := removedWitnessWorld(t)
	w.nextID++
	w.etcd = append(w.etcd, apply.EtcdMember{ID: w.nextID, PeerURLs: []string{"http://10.44.0.3:2380"}, IsLearner: true})
	p := build(t, w)
	var order []string
	for _, s := range steps(p, 3) {
		if s.Title == "remove unstarted learner vm" || s.Site == "vm" {
			order = append(order, s.Verb)
		}
	}
	if strings.Join(order, ",") != "stop,remove,clear,add,write,start,promote" {
		t.Errorf("vm, an unstarted learner, is planned %v", order)
	}
	if err := siteadd.Execute(p); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(w.voters(), ","); got != "home-a,home-b,vm" {
		t.Errorf("voters %s", got)
	}
	vm := w.hosts["vm"]
	cleared, started := ranIndex(vm, "rootlesskit rm -rf"), ranIndex(vm, "up -d etcd")
	if cleared < 0 || started < cleared {
		t.Errorf("the data was cleared at %d and etcd started at %d", cleared, started)
	}
	// A learner that received data before it failed cannot start again
	// under its ID, so it is removed and added again under a new one.
	removed, added := ranIndex(w.hosts["home-a"], "member remove 2"), ranIndex(w.hosts["home-a"], "member add vm ")
	if removed < 0 || added < removed {
		t.Errorf("vm's old learner was removed at %d and it was added again at %d", removed, added)
	}
	for _, m := range w.etcd {
		if m.ID == 2 {
			t.Errorf("vm's old learner, member 2, is still in etcd: %v", w.etcd)
		}
	}
	// The removal was recorded on the other members' hosts before it was
	// made.
	if !strings.Contains(w.hosts["home-a"].files[etcdRemovedFile], "vm c1a5 2\n") {
		t.Errorf("home-a does not record the learner's removal:\n%s", w.hosts["home-a"].files[etcdRemovedFile])
	}
	if recorded := ranIndex(w.hosts["home-a"], "vm c1a5 2'"); recorded < 0 || recorded > removed {
		t.Errorf("the removal was recorded at %d and made at %d", recorded, removed)
	}
	if stopped, cleared := ranIndex(vm, "ids=$(docker ps"), ranIndex(vm, "rootlesskit rm -rf"); stopped < 0 || cleared < stopped {
		t.Errorf("vm's container was removed at %d and its data cleared at %d", stopped, cleared)
	}
}

// A voter, and a learner that has started, are never cleared: their data is
// their own. A run that stopped at the promotion resumes with vm a started
// learner, and nothing in its data directory or home-a's is touched.
func TestAVoterOrAStartedLearnerIsNeverCleared(t *testing.T) {
	defer siteadd.SetFast()()
	w := newWorld(t)
	w.promoteRefusals = siteadd.PromoteAttempts()
	if err := siteadd.Execute(build(t, w)); err == nil || !strings.Contains(err.Error(), "promote") {
		t.Fatalf("want a stop at the promotion, got %v", err)
	}
	vm := w.hosts["vm"]
	if vm.ran("rootlesskit rm -rf") != 1 {
		t.Fatalf("vm was cleared %d times before its add", vm.ran("rootlesskit rm -rf"))
	}
	vm.files[etcdData+"/.marker"] = "kept"
	w.hosts["home-a"].files[etcdData+"/.marker"] = "kept"

	p := build(t, w)
	for _, site := range []string{"home-a", "vm"} {
		if hasStep(p, 3, site, "clear") {
			t.Errorf("%s, a voter or a started learner, is planned a clear: %v", site, steps(p, 3))
		}
	}
	if err := siteadd.Execute(p); err != nil {
		t.Fatal(err)
	}
	if vm.ran("rootlesskit rm -rf") != 1 {
		t.Error("vm, a started learner on the resumed run, was cleared again")
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

// A joiner whose host runs an etcd answering for another cluster is refused
// when the join is planned, naming both clusters, and nothing on any host is
// removed or deleted.
func TestAJoinerRunningAnotherClustersEtcdIsRefused(t *testing.T) {
	defer siteadd.SetFast()()
	w := removedWitnessWorld(t)
	w.otherCluster = map[string]uint64{"vm": 0xbeef}
	_, err := siteadd.Build(w.cfg, w.secrets, "home-b", w.transports())
	refusedUntouched(t, w, err, "vm's etcd data, /srv/paisans/f2a9/infra/etcd, belongs to etcd cluster beef (its running etcd answers for it)", "read on home-a, is cluster c1a5", "the host's owner moves it off the host", "Nothing was changed")
}

// A removed member's etcd that still answers reports the cluster it was
// removed from, this one, and is cleared as any other.
func TestAJoinerAnsweringForThisClusterIsCleared(t *testing.T) {
	defer siteadd.SetFast()()
	w := removedWitnessWorld(t)
	w.otherCluster = map[string]uint64{"vm": 0xc1a5}
	p := build(t, w)
	delete(w.otherCluster, "vm")
	if err := siteadd.Execute(p); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(w.voters(), ","); got != "home-a,home-b,vm" {
		t.Errorf("voters %s", got)
	}
}

// A control voter whose answer carries no cluster ID leaves nothing to
// compare a joiner's etcd with, and the guard protects a deletion, so the
// join is refused before any container is removed or data deleted.
func TestAJoinIsRefusedWhenTheClusterIDCannotBeRead(t *testing.T) {
	defer siteadd.SetFast()()
	w := removedWitnessWorld(t)
	w.clusterID = 0
	_, err := siteadd.Build(w.cfg, w.secrets, "home-b", w.transports())
	refusedUntouched(t, w, err, "site add home-b", "vm", "home-a", "cluster ID could not be read")
	if len(w.hosts["vm"].files[etcdDB]) == 0 {
		t.Error("vm's etcd data is gone")
	}
}

// otherClusterData gives vm's host the data of a member of another etcd
// cluster, beef, in place of the removed member's.
func otherClusterData(w *world) {
	w.hosts["vm"].files[etcdDB] = "other cluster's data"
	w.hosts["vm"].files[etcdWAL] = string(etcdwaltest.Head(0xabc, 0xbeef))
}

// refusedUntouched checks that err refuses, that it says why with each of
// want, and that nothing on any host was stopped, removed or deleted.
func refusedUntouched(t *testing.T, w *world, err error, want ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("not refused")
	}
	for _, s := range want {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("the refusal lacks %q: %v", s, err)
		}
	}
	for _, bad := range []string{"stop that etcd", "every remaining member also holds"} {
		if strings.Contains(err.Error(), bad) {
			t.Errorf("the refusal says %q: %v", bad, err)
		}
	}
	for name, h := range w.hosts {
		if h.ran("rootlesskit rm -rf") != 0 || h.ran("docker stop") != 0 {
			t.Errorf("%s had a container removed or data deleted", name)
		}
	}
	if w.hosts["home-a"].ran("member remove") != 0 {
		t.Error("a member was removed from etcd")
	}
}

// Another cluster's etcd stopped on the joiner's host leaves nothing to ask,
// and its data on disk is still found: an operator who stops it after a
// refusal and runs site add again is refused again, by its write-ahead log.
func TestAStoppedOtherClustersDataIsRefusedOnDisk(t *testing.T) {
	defer siteadd.SetFast()()
	w := removedWitnessWorld(t)
	otherClusterData(w)
	w.otherCluster = map[string]uint64{"vm": 0xbeef}
	_, err := siteadd.Build(w.cfg, w.secrets, "home-b", w.transports())
	refusedUntouched(t, w, err, "belongs to etcd cluster beef (its running etcd answers for it)")

	delete(w.otherCluster, "vm")
	w.running["vm"] = false
	_, err = siteadd.Build(w.cfg, w.secrets, "home-b", w.transports())
	refusedUntouched(t, w, err, "belongs to etcd cluster beef (its write-ahead logs name it)", "the host's owner moves it off the host", "Nothing was changed")
	if w.hosts["vm"].files[etcdDB] != "other cluster's data" {
		t.Error("vm's data changed")
	}
}

// An etcd that runs and does not answer, Eg one of another cluster that
// lost its quorum, whose client port hangs until etcdctl's timeout, is not
// what decides: its data on disk is, and another cluster's is refused.
func TestAnEtcdThatTimesOutIsJudgedByItsData(t *testing.T) {
	defer siteadd.SetFast()()
	timeout := [2]string{`{"level":"warn","msg":"retrying of unary invoker failed","error":"context deadline exceeded"}` + "\nError: context deadline exceeded", "exit status 1"}
	w := removedWitnessWorld(t)
	otherClusterData(w)
	w.probeOut = map[string][2]string{"vm": timeout}
	_, err := siteadd.Build(w.cfg, w.secrets, "home-b", w.transports())
	refusedUntouched(t, w, err, "belongs to etcd cluster beef (its write-ahead logs name it)", "Nothing was changed")
}

// An unstarted learner whose etcd runs and has not published serves no
// client request, so asking it times out. The run removes its container,
// reads its data on disk, removes the learner and adds it again: the
// recovery is not refused for the timeout.
func TestARunningUnpublishedLearnerIsRecovered(t *testing.T) {
	defer siteadd.SetFast()()
	w := removedWitnessWorld(t)
	w.nextID++
	w.etcd = append(w.etcd, apply.EtcdMember{ID: w.nextID, PeerURLs: []string{"http://10.44.0.3:2380"}, IsLearner: true})
	w.running["vm"] = true
	w.probeOut = map[string][2]string{"vm": {`{"level":"warn","msg":"retrying of unary invoker failed","error":"context deadline exceeded"}` + "\nError: context deadline exceeded", "exit status 1"}}
	p := build(t, w)
	delete(w.probeOut, "vm")
	if err := siteadd.Execute(p); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(w.voters(), ","); got != "home-a,home-b,vm" {
		t.Errorf("voters %s", got)
	}
}

// etcdctl's warning lines come first on the combined output; the answer
// after them is still read, and another cluster's is refused.
func TestAnAnswerAfterAWarningLineIsRead(t *testing.T) {
	defer siteadd.SetFast()()
	warn := `{"level":"warn","ts":"2026-10-22T00:00:00Z","logger":"etcd-client","caller":"v3/retry_interceptor.go:62","msg":"retrying of unary invoker failed"}` + "\n"
	w := removedWitnessWorld(t)
	otherClusterData(w)
	w.probeOut = map[string][2]string{"vm": {warn + `{"header":{"cluster_id":48879},"members":[{"ID":2748,"name":"vm","peerURLs":["http://10.44.0.3:2380"]}]}`, ""}}
	_, err := siteadd.Build(w.cfg, w.secrets, "home-b", w.transports())
	refusedUntouched(t, w, err, "belongs to etcd cluster beef (its running etcd answers for it)")

	// This cluster's answer after a warning, with this cluster's recorded
	// data, clears.
	w = removedWitnessWorld(t)
	w.probeOut = map[string][2]string{"vm": {warn + `{"header":{"cluster_id":49573},"members":[]}`, ""}}
	p := build(t, w)
	delete(w.probeOut, "vm")
	if err := siteadd.Execute(p); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(w.voters(), ","); got != "home-a,home-b,vm" {
		t.Errorf("voters %s", got)
	}
}

// Data not shown to be a member this cluster removed for the site, or
// nothing, is refused when the join is planned, whatever else it is, and
// nothing is stopped, removed or deleted.
func TestDataNotShownToBeARemovedMemberIsRefused(t *testing.T) {
	defer siteadd.SetFast()()
	for name, c := range map[string]struct {
		setup func(*world)
		want  string
	}{
		"a member directory with more than a failed start and no log": {func(w *world) {
			delete(w.hosts["vm"].files, etcdWAL)
			delete(w.hosts["vm"].files, etcdData+"/member/wal/0.tmp")
			w.hosts["vm"].files[etcdData+"/member/snap/0000000000000002-0000000000000005.snap"] = "snap"
		}, "no write-ahead log and more than a failed start leaves"},
		"a log that does not decode": {func(w *world) { w.hosts["vm"].files[etcdWAL] = "not a WAL" }, "does not decode"},
		"an unreadable directory":    {func(w *world) { w.dataUnreadable = map[string]bool{"vm": true} }, "could not be read"},
		"a symbolic link":            {func(w *world) { w.dataLink = map[string]bool{"vm": true} }, "is a symbolic link"},
		"a linked member directory": {func(w *world) {
			w.hosts["vm"].files = map[string]string{etcdData + "/member": "symlink:/elsewhere"}
		}, "holds a symbolic link, member"},
		"a file at the data path": {func(w *world) {
			w.hosts["vm"].files[etcdData] = "a file"
		}, "is not a directory"},
		"a member moved aside by hand": {func(w *world) {
			w.hosts["vm"].files = map[string]string{etcdData + "/member.bak/wal/" + "0000000000000000-0000000000000000.wal": string(etcdwaltest.Head(0xabc, 0xbeef))}
		}, "holds member.bak"},
		"a stray file beside the member": {func(w *world) { w.hosts["vm"].files[etcdData+"/notes.txt"] = "x" }, "holds notes.txt"},
		"a second log of another cluster": {func(w *world) {
			w.hosts["vm"].files[etcdData+"/member/wal/0000000000000001-0000000000000009.wal"] = string(etcdwaltest.Head(0xabc, 0xbeef))
		}, "disagree"},
		"a member this cluster has no record of removing": {func(w *world) {
			w.hosts["vm"].files[etcdWAL] = string(etcdwaltest.Head(0x9999, w.clusterID))
		}, "member 9999's of this cluster, which no host of this deployment records as removed for vm"},
		"a removal recorded for another site": {func(w *world) {
			w.hosts["home-a"].files[etcdRemovedFile] = "home-c c1a5 7f3a\n"
		}, "which no host of this deployment records as removed for vm"},
		"a removal recorded in another cluster": {func(w *world) {
			w.hosts["home-a"].files[etcdRemovedFile] = "vm beef 7f3a\n"
		}, "which no host of this deployment records as removed for vm"},
		"a member the cluster has now": {func(w *world) {
			w.hosts["vm"].files[etcdWAL] = string(etcdwaltest.Head(1, w.clusterID))
			w.hosts["home-a"].files[etcdRemovedFile] = "vm c1a5 1\n"
		}, "member 1's, which this cluster has now (home-a)"},
	} {
		t.Run(name, func(t *testing.T) {
			w := removedWitnessWorld(t)
			c.setup(w)
			_, err := siteadd.Build(w.cfg, w.secrets, "home-b", w.transports())
			refusedUntouched(t, w, err, c.want, "it is not deleted", "Nothing was changed")
		})
	}
}

// An unstarted learner beside another cluster's data is refused when the
// join is planned: the learner is not removed from etcd.
func TestAnUnstartedLearnerBesideForeignDataIsNotRemoved(t *testing.T) {
	defer siteadd.SetFast()()
	w := removedWitnessWorld(t)
	otherClusterData(w)
	w.nextID++
	w.etcd = append(w.etcd, apply.EtcdMember{ID: w.nextID, PeerURLs: []string{"http://10.44.0.3:2380"}, IsLearner: true})
	_, err := siteadd.Build(w.cfg, w.secrets, "home-b", w.transports())
	refusedUntouched(t, w, err, "belongs to etcd cluster beef")
	if len(w.etcd) != 2 {
		t.Errorf("etcd after: %v", w.etcd)
	}
}

// What a failed start leaves, member/snap/db alone and no larger than a
// fresh one, holds no member, and is cleared.
func TestAFailedStartsDataIsCleared(t *testing.T) {
	defer siteadd.SetFast()()
	w := removedWitnessWorld(t)
	w.hosts["home-a"].files[etcdRemovedFile] = ""
	vm := w.hosts["vm"]
	delete(vm.files, etcdWAL)
	delete(vm.files, etcdData+"/member/wal/0.tmp")
	vm.files[etcdDB] = strings.Repeat("\x00", 1024)
	if err := siteadd.Execute(build(t, w)); err != nil {
		t.Fatal(err)
	}
	if vm.ran("rootlesskit rm -rf") != 1 {
		t.Error("the failed start's data was not cleared")
	}
}

// A data directory that exists and is empty is nothing to lose.
func TestAnEmptyDataDirectoryIsCleared(t *testing.T) {
	defer siteadd.SetFast()()
	w := newWorld(t)
	w.hosts["vm"].dataDir = true
	if err := siteadd.Execute(build(t, w)); err != nil {
		t.Fatal(err)
	}
}

// A container restarting in a loop is never seen running, so the data is
// read on disk; it is this cluster's, and the container is removed before
// the data is read.
func TestARestartingContainerIsRemovedAndItsDataCleared(t *testing.T) {
	defer siteadd.SetFast()()
	w := removedWitnessWorld(t)
	w.restarting = map[string]bool{"vm": true}
	if err := siteadd.Execute(build(t, w)); err != nil {
		t.Fatal(err)
	}
	if w.restarting["vm"] {
		t.Error("vm's restarting container was not removed")
	}
	vm := w.hosts["vm"]
	if stopped, read := ranIndex(vm, "ids=$(docker ps"), ranIndex(vm, "paisans-etcd-data"); stopped < 0 || read < 0 {
		t.Errorf("vm's container was removed at %d and its data read at %d", stopped, read)
	}
	if got := strings.Join(w.voters(), ","); got != "home-a,home-b,vm" {
		t.Errorf("voters %s", got)
	}
}

// What is checked when the join is planned is checked again before the
// clear runs: another cluster's data that appears on the host after
// planning, with or without its etcd running, stops stage 3. The container
// is removed first, as it always is, and the data is not touched.
func TestTheCheckRunsAgainBeforeTheClear(t *testing.T) {
	defer siteadd.SetFast()()
	for name, change := range map[string]func(*world){
		"a running etcd of another cluster": func(w *world) {
			otherClusterData(w)
			w.otherCluster = map[string]uint64{"vm": 0xbeef}
		},
		"another cluster's data on disk": otherClusterData,
	} {
		t.Run(name, func(t *testing.T) {
			w := removedWitnessWorld(t)
			p := build(t, w)
			change(w)
			err := siteadd.Execute(p)
			if err == nil || !strings.Contains(err.Error(), "stage 3") || !strings.Contains(err.Error(), "belongs to etcd cluster beef") || !strings.Contains(err.Error(), "its etcd data was not touched") {
				t.Fatalf("want a stage 3 refusal, got %v", err)
			}
			vm := w.hosts["vm"]
			if vm.ran("rootlesskit rm -rf") != 0 || vm.files[etcdDB] != "other cluster's data" {
				t.Error("vm's data was deleted")
			}
			if w.hosts["home-a"].ran("member add vm ") != 0 {
				t.Error("vm was added to etcd")
			}
		})
	}
}

// Data that changes after planning to another removed member of this
// cluster, recorded for the site, is cleared at run time.
func TestARecordedMemberFoundAtRunTimeIsCleared(t *testing.T) {
	defer siteadd.SetFast()()
	w := removedWitnessWorld(t)
	p := build(t, w)
	w.hosts["vm"].files[etcdWAL] = string(etcdwaltest.Head(0x9999, w.clusterID))
	w.hosts["home-a"].files[etcdRemovedFile] += "vm c1a5 9999\n"
	if err := siteadd.Execute(p); err != nil {
		t.Fatal(err)
	}
	if string(etcdwaltest.Head(0x9999, w.clusterID)) == w.hosts["vm"].files[etcdWAL] {
		t.Error("vm's data was not cleared")
	}
}

// A learner that publishes between the plan and the run, before its
// container is removed, has started: its data is its own and is not
// cleared, and it is not removed from etcd.
func TestALearnerThatStartsBeforeTheRunIsKept(t *testing.T) {
	defer siteadd.SetFast()()
	w := removedWitnessWorld(t)
	w.nextID++
	w.etcd = append(w.etcd, apply.EtcdMember{ID: w.nextID, PeerURLs: []string{"http://10.44.0.3:2380"}, IsLearner: true})
	p := build(t, w)
	// It publishes and writes its own data.
	w.etcd[len(w.etcd)-1].Name = "vm"
	vm := w.hosts["vm"]
	vm.files[etcdDB] = w.memberRecord("vm")
	vm.files[etcdWAL] = string(etcdwaltest.Head(w.nextID, w.clusterID))
	if err := siteadd.Execute(p); err != nil {
		t.Fatal(err)
	}
	if vm.ran("rootlesskit rm -rf") != 0 {
		t.Error("a started learner's data was cleared")
	}
	if w.hosts["home-a"].ran("member remove") != 0 {
		t.Error("a started learner was removed from etcd")
	}
	if got := strings.Join(w.voters(), ","); got != "home-a,home-b,vm" {
		t.Errorf("voters %s", got)
	}
}
