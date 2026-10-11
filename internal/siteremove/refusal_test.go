package siteremove_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/siteremove"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

func replace(old, new string) func(string) string {
	return func(s string) string {
		if !strings.Contains(s, old) {
			panic("no " + old)
		}
		return strings.Replace(s, old, new, 1)
	}
}

// Every refusal stops before anything changes, with what to do.
func TestRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		site  string
		edits []func(string) string
		world func(*world)
		opts  siteremove.Options
		want  string
		// hint, when set, is the Problem's hint main prints.
		hint string
	}{
		{name: "the only gateway", site: "vm",
			edits: []func(string) string{replace("placement: { pinned: vm }", "placement: { pinned: home-a }")},
			want:  "only gateway", hint: "vm is the only gateway"},
		{name: "the only apps site", site: "home-b",
			edits: []func(string) string{replace("  home-a:\n    roles: [data, apps]", "  home-a:\n    roles: [data]"), replace("placement: { pinned: home-a }\n    # An ini", "placement: { pinned: watch }\n    # An ini")},
			want:  "only apps site", hint: "home-b is the only apps site"},
		{name: "an app pinned to it", site: "home-a", want: "pinned to it", hint: "apps are pinned to home-a"},
		{name: "the only data site", site: "home-b",
			edits: []func(string) string{
				replace("  home-a:\n    roles: [data, apps]", "  home-a:\n    roles: [apps]"),
				replace("  home-c:\n    roles: [data]", "  home-c:\n    roles: [storage]"),
				replace("sites: [home-a, home-b, home-c]\n  port", "sites: [home-b]\n  port"),
				replace("members: [home-a, home-b, home-c, vm]", "members: [home-b]"),
			},
			want: "only data site", hint: "home-b is the only data site"},
		{name: "too few Garage nodes", site: "home-b",
			edits: []func(string) string{replace("replication: 2", "replication: 3")},
			want:  "would hold objects at storage.garage.replication 3", hint: "too few Garage nodes would be left without home-b"},
		{name: "an end state that does not validate", site: "home-b",
			edits: []func(string) string{replace("members: [home-a, home-b, home-c, vm]", "members: [home-a, home-b, vm]")},
			want:  "two etcd voters", hint: "the configuration without home-b would be refused"},
		{name: "the first Garage site", site: "home-a",
			edits: []func(string) string{replace("placement: { pinned: home-a }\n    # An ini", "placement: { pinned: watch }\n    # An ini"), replace("placement: { pinned: home-a }\n    settings:\n      homeserver", "placement: { pinned: watch }\n    settings:\n      homeserver"), replace("placement: { pinned: home-a }\n  status", "placement: { pinned: watch }\n  status")},
			want:  "first in storage.garage.sites", hint: "home-a is first in storage.garage.sites"},
		{name: "--host-gone with --delete-data", site: "home-b", opts: siteremove.Options{HostGone: true, DeleteData: true}, want: "Drop one of them", hint: "--host-gone and --delete-data cannot be used together"},
		{name: "an unhealthy etcd member", site: "home-b", world: func(w *world) { w.etcdDown["home-c"] = true }, want: "home-c's etcd is unhealthy"},
		{name: "no quorum after", site: "home-b", world: func(w *world) { w.etcdDown["home-c"], w.etcdDown["vm"] = true, true }, want: "which is no quorum"},
		{name: "an unhealthy Patroni member", site: "home-b", world: func(w *world) { w.member("home-c")["State"] = "starting" }, want: "home-c is starting, not streaming"},
		{name: "the leader with no Sync Standby", site: "home-b", world: func(w *world) {
			w.member("home-a")["Role"], w.member("home-a")["State"] = "Replica", "streaming"
			w.member("home-b")["Role"], w.member("home-b")["State"] = "Leader", "running"
		}, want: "no member is a streaming Sync Standby"},
		{name: "an unhealthy Garage node", site: "home-b", world: func(w *world) { w.garageDown["home-c"] = true }, want: "home-c (cccccccccccccccc) are not healthy"},
		{name: "an etcd.members site etcd does not list", site: "home-b", world: func(w *world) { w.dropEtcd("home-c") }, want: "home-c is in etcd.members but is not an etcd member yet"},
		{name: "no quorum counting a member etcd does not list", site: "home-b", world: func(w *world) { w.dropEtcd("home-c"); w.etcdDown["vm"] = true }, want: "1 of 3 remaining members are healthy"},
		{name: "an etcd.members site that is a learner", site: "home-b", world: func(w *world) { w.learner("home-c") }, want: "home-c is still a learner"},
		{name: "a Garage site not in the layout", site: "home-b",
			edits: []func(string) string{replace("replication: 2", "replication: 1")},
			world: func(w *world) { delete(w.layout, "cccccccccccccccc") }, want: "home-c is in storage.garage.sites but not in Garage's layout yet"},
		{name: "a live layout too small without the site", site: "home-b", world: func(w *world) { delete(w.layout, "cccccccccccccccc") }, want: "1 node(s) in Garage's layout would stay without home-b, fewer than storage.garage.replication 2"},
		{name: "a staged role assignment", site: "home-b", world: func(w *world) { w.assigned["dddddddddddddddd"] = "home-z" },
			want: "site remove home-b: Garage has staged layout changes that site remove would apply along with its own. Apply them with paisans storage add, or revert them with garage layout revert --yes, first. Nothing was changed"},
		{name: "another node's staged removal", site: "home-b", world: func(w *world) { w.staged["cccccccccccccccc"] = true }, want: "Garage has staged layout changes"},
		{name: "its own staged removal beside another change", site: "home-b", world: func(w *world) { w.staged["bbbbbbbbbbbbbbbb"], w.redundancy = true, "2" }, want: "Garage has staged layout changes"},
		{name: "a staged zone redundancy", site: "home-b", world: func(w *world) { w.redundancy = "2" }, want: "Garage has staged layout changes"},
		{name: "the first etcd in a cluster of its own", site: "home-b", world: func(w *world) { w.splitEtcd("home-a") },
			want: "site remove home-b: home-a's etcd belongs to a different cluster than home-c's and vm's, so its membership cannot be changed safely. Run paisans doctor on home-a. Nothing was changed"},
		{name: "a later etcd in a cluster of its own", site: "home-b", world: func(w *world) { w.splitEtcd("vm") },
			want: "vm's etcd belongs to a different cluster than home-a's and home-c's"},
		{name: "one of two answering etcds in a cluster of its own", site: "home-b", world: func(w *world) { w.splitEtcd("home-a"); w.hosts["vm"].etcdUp = false },
			want: "home-a's etcd belongs to a different cluster than home-c's, so"},
		{name: "an etcd in a cluster of its own, no cluster IDs", site: "home-b", world: func(w *world) {
			w.splitEtcd("home-a")
			w.clusterID, w.hosts["home-a"].ownCluster.cluster = 0, 0
		}, want: "home-a's etcd belongs to a different cluster than home-c's and vm's"},
		{name: "a host that does not answer", site: "home-b", world: func(w *world) { w.unreachable["home-b"] = true }, want: "--host-gone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, tc.edits...)
			t.Cleanup(siteremove.SetFast())
			t.Cleanup(siteremove.SetInspect(w.inspect))
			if tc.world != nil {
				tc.world(w)
			}
			_, err := w.build(tc.site, tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			// A cluster of its own is that site's fault, not the others'.
			if strings.Contains(tc.want, "different cluster") && strings.Contains(err.Error(), "not an etcd member yet") {
				t.Errorf("a healthy site is blamed: %v", err)
			}
			if tc.hint != "" {
				var p *ui.Problem
				if !errors.As(err, &p) || p.Hint != tc.hint || p.Explain == "" {
					t.Errorf("not the Problem main prints: %#v", p)
				}
			}
			for name, h := range w.hosts {
				for _, c := range h.commands {
					for _, verb := range []string{"member remove", "switchover", "stop", "layout remove", "syncconf", "restart", "ufw delete", "rm -"} {
						if strings.Contains(c, verb) {
							t.Errorf("a refused removal ran %q on %s", c, name)
						}
					}
				}
			}
		})
	}
}

// dropEtcd is a site in etcd.members whose join never finished: etcd does
// not list it, and nothing of etcd runs on its host.
func (w *world) dropEtcd(site string) {
	var kept []apply.EtcdMember
	for _, m := range w.etcd {
		if m.Name != site {
			kept = append(kept, m)
		}
	}
	w.etcd = kept
	w.hosts[site].etcdUp, w.hosts[site].etcdContainer = false, false
}

// learner makes a site's etcd member a learner, joined but not promoted.
func (w *world) learner(site string) {
	for i := range w.etcd {
		if w.etcd[i].Name == site {
			w.etcd[i].IsLearner = true
		}
	}
}

func TestRefusalNamesAnUndeclaredSite(t *testing.T) {
	cfg, _, _ := worldConfig(t)
	err := siteremove.Refusal(cfg, "home-z", siteremove.Options{})
	if err == nil || !strings.Contains(err.Error(), "declares no site") {
		t.Errorf("err = %v", err)
	}
	var p *ui.Problem
	if !errors.As(err, &p) || !strings.HasSuffix(p.Hint, "declares no site home-z") || !strings.Contains(p.Explain, "Declared sites are") {
		t.Errorf("not the Problem main prints: %#v", p)
	}
}

// A removal resumed between its own `garage layout remove` and `layout
// apply` finds only its own removal staged, and carries on.
func TestItsOwnStagedRemovalIsNotRefused(t *testing.T) {
	w := setup(t)
	w.staged["bbbbbbbbbbbbbbbb"] = true
	if err := siteremove.Execute(w.mustBuild("home-b", siteremove.Options{})); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.layout["bbbbbbbbbbbbbbbb"]; ok {
		t.Error("home-b's node is still in the layout")
	}
}

// Every remaining etcd in one cluster, with or without cluster IDs in its
// answer, is one view: the removal plans and runs as it always has.
func TestAgreeingEtcdViewsRemoveAsBefore(t *testing.T) {
	for _, id := range []uint64{0xc1a5, 0} {
		w := setup(t)
		w.clusterID = id
		if err := siteremove.Execute(w.mustBuild("home-b", siteremove.Options{})); err != nil {
			t.Fatalf("cluster ID %x: %v", id, err)
		}
	}
}
