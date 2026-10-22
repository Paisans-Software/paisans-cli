package siteremove_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/siteremove"
)

const etcdData = root + "/infra/etcd"

// seedEtcdData gives each site's host the data its etcd member keeps.
func (w *world) seedEtcdData(sites ...string) {
	for _, site := range sites {
		h := w.hosts[site]
		h.files[etcdData+"/member/snap/db"] = "member of " + site
		h.files[etcdData+"/.lock"] = "lock"
	}
}

func etcdDataLeft(h *host) []string {
	var left []string
	for path := range h.files {
		if strings.HasPrefix(path, etcdData+"/") {
			left = append(left, path)
		}
	}
	return left
}

func emptiesEtcdData(p *siteremove.Plan, stage int, site string) bool {
	return hasStep(p, stage, site, "empty", site+"'s etcd data, "+etcdData) && hasStep(p, stage, site, "empty", `rootlesskit rm -rf -- "$f"`)
}

// A data site leaving etcd, with three voters staying, has its etcd data
// emptied once its member is removed and its etcd stopped, and the plan
// names it. Every site that stays keeps its data.
func TestADataSiteLeavingEtcdHasItsDataEmptied(t *testing.T) {
	w := setup(t)
	w.seedEtcdData("home-a", "home-b", "home-c", "vm")
	p := w.mustBuild("home-b", siteremove.Options{})
	if !emptiesEtcdData(p, 2, "home-b") {
		t.Errorf("stage 2 does not empty home-b's etcd data:\n%s", printed(p))
	}
	for _, site := range []string{"home-a", "home-c", "vm"} {
		if hasStep(p, 2, site, "empty", "") {
			t.Errorf("stage 2 empties %s's etcd data, and it stays:\n%s", site, printed(p))
		}
	}
	execute(t, p)
	if left := etcdDataLeft(w.hosts["home-b"]); len(left) > 0 {
		t.Errorf("home-b keeps etcd data: %v", left)
	}
	for _, site := range []string{"home-a", "home-c", "vm"} {
		if len(etcdDataLeft(w.hosts[site])) != 2 {
			t.Errorf("%s's etcd data was touched", site)
		}
	}
	if stop, empty := w.index("home-b: if [ -f"), w.index("home-b: set -e; if [ -L '"+etcdData+"'"); empty < 0 || empty < stop {
		t.Errorf("home-b's etcd data was emptied at %d, its etcd stopped at %d", empty, stop)
	}
	if removed, empty := w.index("member remove 2222"), w.index("home-b: set -e; if [ -L '"+etcdData+"'"); removed < 0 || empty < removed {
		t.Errorf("home-b's etcd data was emptied at %d, its member removed at %d", empty, removed)
	}
}

// A data site leaving two data sites and a witness takes the gateway's
// witness out of etcd with it, and both have their etcd data emptied: the
// witness's once its container is removed.
func TestADataSiteLeavingWithTheWitnessEmptiesBoth(t *testing.T) {
	w := witnessWorld(t)
	w.seedEtcdData("home-a", "home-b", "vm")
	p := w.mustBuild("home-b", siteremove.Options{})
	for _, site := range []string{"home-b", "vm"} {
		if !emptiesEtcdData(p, 2, site) {
			t.Errorf("stage 2 does not empty %s's etcd data:\n%s", site, printed(p))
		}
	}
	if hasStep(p, 2, "home-a", "empty", "") {
		t.Errorf("stage 2 empties home-a's etcd data, and home-a stays:\n%s", printed(p))
	}
	execute(t, p)
	for _, site := range []string{"home-b", "vm"} {
		if left := etcdDataLeft(w.hosts[site]); len(left) > 0 {
			t.Errorf("%s keeps etcd data: %v", site, left)
		}
	}
	if len(etcdDataLeft(w.hosts["home-a"])) != 2 {
		t.Error("home-a's etcd data was touched")
	}
	if stop, empty := w.index("vm: ids=$(docker ps"), w.index("vm: set -e; if [ -L '"+etcdData+"'"); stop < 0 || empty < stop {
		t.Errorf("vm's etcd data was emptied at %d, its container removed at %d", empty, stop)
	}
	for _, c := range []struct{ site, id string }{{"home-b", "2222"}, {"vm", "3333"}} {
		if removed, empty := w.index("member remove "+c.id), w.index(c.site+": set -e; if [ -L '"+etcdData+"'"); removed < 0 || empty < removed {
			t.Errorf("%s's etcd data was emptied at %d, its member removed at %d", c.site, empty, removed)
		}
	}
}

// A run stopped after the members were removed still empties their data
// when it resumes: the step reads the configuration, not etcd, so it is
// planned again, and emptying an empty directory is nothing.
func TestAResumedRemovalEmptiesEtcdDataItDidNotReach(t *testing.T) {
	w := witnessWorld(t)
	w.seedEtcdData("home-a", "home-b", "vm")
	w.failOnce = "set -e; if [ -L '" + etcdData + "'"
	err := siteremove.Execute(w.mustBuild("home-b", siteremove.Options{}))
	if err == nil || !strings.Contains(err.Error(), "stopped at stage 2") || !strings.Contains(err.Error(), "emptying its etcd data") {
		t.Fatalf("err = %v", err)
	}
	if len(etcdDataLeft(w.hosts["home-b"])) != 2 {
		t.Fatal("home-b's etcd data went though the empty failed")
	}
	again := w.mustBuild("home-b", siteremove.Options{})
	if hasStep(again, 2, "home-a", "remove", "home-b's etcd member") {
		t.Errorf("the resumed plan removes home-b's member again:\n%s", printed(again))
	}
	for _, site := range []string{"home-b", "vm"} {
		if !emptiesEtcdData(again, 2, site) {
			t.Errorf("the resumed plan does not empty %s's etcd data:\n%s", site, printed(again))
		}
	}
	execute(t, again)
	for _, site := range []string{"home-b", "vm"} {
		if left := etcdDataLeft(w.hosts[site]); len(left) > 0 {
			t.Errorf("%s keeps etcd data after the resumed run: %v", site, left)
		}
	}
}

// With --host-gone the leaving site is never reached, so only the witness's
// data is emptied, and the report says the leaving host keeps its etcd data
// and that site add clears it.
func TestACutoffEmptiesTheWitnessesEtcdDataAndSaysWhatStays(t *testing.T) {
	w, zone := cutoffWorld(t, "home-b", twoAndAWitness...)
	w.seedEtcdData("home-a", "home-b", "vm")
	p := w.mustBuild("home-b", cutoffOptions(zone))
	if !emptiesEtcdData(p, 4, "vm") {
		t.Errorf("the etcd stage does not empty the witness's etcd data:\n%s", printed(p))
	}
	for _, st := range p.Stages {
		for _, s := range st.Steps {
			if s.Site == "home-b" && s.Verb == "empty" {
				t.Errorf("stage %d empties the cut-off host's etcd data: %v", st.Number, s)
			}
		}
	}
	remains := strings.Join(p.Remains(), "\n")
	if !strings.Contains(remains, "Its etcd data, "+etcdData+", stays there") || !strings.Contains(remains, "site add clears it") {
		t.Errorf("the report does not say home-b keeps its etcd data:\n%s", remains)
	}
	execute(t, p)
	if left := etcdDataLeft(w.hosts["vm"]); len(left) > 0 {
		t.Errorf("vm keeps etcd data: %v", left)
	}
	if removed, empty := w.index("member remove 3333"), w.index("vm: set -e; if [ -L '"+etcdData+"'"); removed < 0 || empty < removed {
		t.Errorf("vm's etcd data was emptied at %d, its member removed at %d", empty, removed)
	}
	if len(etcdDataLeft(w.hosts["home-b"])) != 2 || len(etcdDataLeft(w.hosts["home-a"])) != 2 {
		t.Error("the cut-off site's or the remaining voter's etcd data was touched")
	}
}

// A data site cut off with no witness leaving: nothing is emptied anywhere,
// and the report still says its host keeps its etcd data.
func TestACutoffWithoutAWitnessEmptiesNothing(t *testing.T) {
	w, zone := cutoffWorld(t, "home-b")
	w.seedEtcdData("home-a", "home-b", "home-c", "vm")
	p := w.mustBuild("home-b", cutoffOptions(zone))
	if strings.Count(printed(p), "empty etcd data") != 0 {
		t.Errorf("the plan empties etcd data with no witness leaving:\n%s", printed(p))
	}
	if !strings.Contains(strings.Join(p.Remains(), "\n"), "Its etcd data, "+etcdData+", stays there") {
		t.Errorf("the report does not say home-b keeps its etcd data:\n%s", strings.Join(p.Remains(), "\n"))
	}
	execute(t, p)
	for _, site := range []string{"home-a", "home-b", "home-c", "vm"} {
		if len(etcdDataLeft(w.hosts[site])) != 2 {
			t.Errorf("%s's etcd data was touched", site)
		}
	}
}

// A witness with no other role, removed itself while three data sites stay
// voters, leaves etcd like any member: its etcd stopped and its data
// emptied, and nothing else's.
func TestAWitnessRemovedItselfHasItsEtcdDataEmptied(t *testing.T) {
	w := newWorld(t,
		replace("  vm:\n    roles: [gateway, witness]", "  box:\n    roles: [witness]\n    address: 10.44.0.6\n    endpoint: 203.0.113.60:51820\n    ssh:\n      host: box.local\n      user: ubuntu\n      keys: [alice, bob]\n  vm:\n    roles: [gateway]"),
		replace("members: [home-a, home-b, home-c, vm]", "members: [home-a, home-b, home-c, box]"),
	)
	t.Cleanup(siteremove.SetFast())
	t.Cleanup(siteremove.SetInspect(w.inspect))
	w.seedEtcdData("home-a", "home-b", "home-c", "box")
	p := w.mustBuild("box", siteremove.Options{})
	if !emptiesEtcdData(p, 2, "box") {
		t.Errorf("stage 2 does not empty box's etcd data:\n%s", printed(p))
	}
	execute(t, p)
	if left := etcdDataLeft(w.hosts["box"]); len(left) > 0 {
		t.Errorf("box keeps etcd data: %v", left)
	}
	if removed, empty := w.index("member remove "), w.index("box: set -e; if [ -L '"+etcdData+"'"); removed < 0 || empty < removed {
		t.Errorf("box's etcd data was emptied at %d, its member removed at %d", empty, removed)
	}
	for _, site := range []string{"home-a", "home-b", "home-c"} {
		if len(etcdDataLeft(w.hosts[site])) != 2 {
			t.Errorf("%s's etcd data was touched", site)
		}
	}
}

// Each member site remove takes out of etcd is recorded, before its removal,
// on the host of every etcd member that stays and of a witness that stays a
// site: site add deletes that site's old etcd data later only on such a
// record.
func TestEachRemovedMemberIsRecordedBeforeItsRemoval(t *testing.T) {
	const list = root + "/.paisans/etcd-removed"
	t.Run("a data site and the witness", func(t *testing.T) {
		w := witnessWorld(t)
		execute(t, w.mustBuild("home-b", siteremove.Options{}))
		for _, site := range []string{"home-a", "vm"} {
			if got := w.hosts[site].files[list]; !strings.Contains(got, "home-b c1a5 2222\n") {
				t.Errorf("%s does not record home-b's removal:\n%s", site, got)
			}
		}
		if got := w.hosts["home-a"].files[list]; !strings.Contains(got, "vm c1a5 3333\n") {
			t.Errorf("home-a does not record the witness's removal:\n%s", got)
		}
		for _, c := range []struct{ line, id string }{{"home-b c1a5 2222", "2222"}, {"vm c1a5 3333", "3333"}} {
			if recorded, removed := w.index("home-a: set -e; mkdir -p '"+root+"/.paisans'; f='"+list+"'; touch \"$f\"; grep -qxF '"+c.line+"'"), w.index("member remove "+c.id); recorded < 0 || removed < recorded {
				t.Errorf("%s was recorded at %d and removed at %d", c.line, recorded, removed)
			}
		}
	})
	t.Run("a data site of three", func(t *testing.T) {
		w := setup(t)
		execute(t, w.mustBuild("home-b", siteremove.Options{}))
		for _, site := range []string{"home-a", "home-c", "vm"} {
			if got := w.hosts[site].files[list]; !strings.Contains(got, "home-b c1a5 2222\n") {
				t.Errorf("%s does not record home-b's removal:\n%s", site, got)
			}
		}
	})
	t.Run("--host-gone", func(t *testing.T) {
		w, zone := cutoffWorld(t, "home-b", twoAndAWitness...)
		execute(t, w.mustBuild("home-b", cutoffOptions(zone)))
		if got := w.hosts["home-a"].files[list]; !strings.Contains(got, "home-b c1a5 2222\n") || !strings.Contains(got, "vm c1a5 3333\n") {
			t.Errorf("home-a does not record both removals:\n%s", got)
		}
		if _, ok := w.hosts["home-b"].files[list]; ok {
			t.Error("the cut-off host was written")
		}
	})
}
