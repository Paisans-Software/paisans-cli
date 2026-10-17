package siteremove_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/dns/dnstest"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/siteremove"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// evictWorld is the world with a zone the gateway's DNS token can see, the
// site to evict marked as cut off when its peers drop it.
func evictWorld(t *testing.T, site string, edits ...func(string) string) (*world, *dnstest.Provider) {
	t.Helper()
	w := newWorld(t, append([]func(string) string{func(text string) string {
		return strings.Replace(text, "  provider: desec", "  provider: cloudflare", 1)
	}}, edits...)...)
	t.Cleanup(siteremove.SetFast())
	t.Cleanup(siteremove.SetInspect(w.inspect))
	w.evicted = site
	zone := dnstest.New()
	zone.Zones["example.org"] = "zone-1"
	return w, zone
}

func evictOptions(zone *dnstest.Provider) siteremove.Options {
	return siteremove.Options{Evict: true, DNSProvider: zone.For("cloudflare")}
}

// leadersAway moves what pins home-a and leads Garage's list elsewhere, so
// home-a, the leader, can be evicted.
func leadersAway(text string) string {
	text = strings.ReplaceAll(text, "placement: { pinned: home-a }", "placement: { pinned: home-c }")
	return strings.Replace(text, "    sites: [home-a, home-b, home-c]\n", "    sites: [home-c, home-b, home-a]\n", 1)
}

// replacement declares home-d, a new gateway and apps site, and pins vm's app
// to it: the one edit of paisans-stack.yaml that replaces vm.
func replacement(text string) string {
	text = strings.Replace(text, "  vm:\n    roles: [gateway, witness]", `  home-d:
    roles: [gateway, apps]
    address: 10.44.0.6
    endpoint: home-d.example.org:51820
    public_address: 203.0.113.60
    ssh:
      host: home-d.example.org
      user: ubuntu
      keys: [alice, bob]
  vm:
    roles: [gateway, witness]`, 1)
	text = strings.Replace(text, "    endpoint: vm.example.org:51820\n", "    endpoint: vm.example.org:51820\n    public_address: 203.0.113.10\n", 1)
	return strings.ReplaceAll(text, "placement: { pinned: vm }", "placement: { pinned: home-d }")
}

// joining makes site's host a fresh one: no admission record, no files, in no cluster,
// and every other site's mesh as an apply without it rendered it, since it
// has never run.
func (w *world) joining(site string) {
	w.hosts[site] = &host{w: w, name: site, files: map[string]string{}, images: map[string]bool{}}
	rendered, err := render.Build(w.cfg.WithoutJoining(site), w.secrets)
	if err != nil {
		w.t.Fatal(err)
	}
	for name, h := range w.hosts {
		if name == site {
			continue
		}
		for _, f := range rendered.Files {
			if f.Path != name+wgConf {
				continue
			}
			h.files[wgConf] = f.Content
			running := f.Content
			h.wgRun = &running
			var m render.Manifest
			if err := json.Unmarshal([]byte(h.files[dep.Manifest()]), &m); err != nil {
				w.t.Fatal(err)
			}
			for i := range m.Files {
				if "/"+m.Files[i].Path == wgConf {
					m.Files[i].SHA256 = hexSum(f.Content)
				}
			}
			data, _ := json.MarshalIndent(m, "", "  ")
			h.files[dep.Manifest()] = string(data) + "\n"
		}
	}
	var etcd = w.etcd[:0]
	for _, m := range w.etcd {
		if m.Name != site {
			etcd = append(etcd, m)
		}
	}
	w.etcd = etcd
	for id, zone := range w.layout {
		if zone == site {
			delete(w.layout, id)
		}
	}
	delete(w.served, site)
}

func publicKey(t *testing.T, w *world, site string) string {
	t.Helper()
	key, err := render.PublicKey(w.secrets.Sites[site].WireGuardPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func execute(t *testing.T, p *siteremove.Plan) {
	t.Helper()
	rec := &ui.Recorder{}
	p.Report = rec
	if err := siteremove.Execute(p); err != nil {
		t.Fatalf("%v\n%s", err, rec.Lines())
	}
}

// An eviction cuts first, then waits for the cluster to let go, and never
// reaches the evicted host.
func TestAnEvictionCutsFirstAndNeverReachesTheHost(t *testing.T) {
	w, zone := evictWorld(t, "home-b")
	p := w.mustBuild("home-b", evictOptions(zone))
	var names []string
	for _, st := range p.Stages {
		names = append(names, st.Name)
	}
	if got := strings.Join(names, ", "); got != "cut, dns, database, etcd, storage, the rest of the cluster, config, monitor" {
		t.Fatalf("stages: %s", got)
	}
	for _, want := range []struct {
		stage            int
		site, verb, text string
	}{
		{1, "home-a", "mesh", ""},
		{1, "vm", "mesh", ""},
		{3, "home-a", "delete", "/service/paisans/members/home-b"},
		{4, "home-a", "remove", "etcd member"},
		{5, "home-a", "remove", "bbbbbbbbbbbbbbbb"},
		{5, "home-a", "skip", "layout skip-dead-nodes --version 4"},
		{6, "home-a", "restart", "HAProxy"},
		{7, "home-b", "remove", "sites.home-b"},
	} {
		if !hasStep(p, want.stage, want.site, want.verb, want.text) {
			t.Errorf("stage %d has no %s %s step with %q:\n%s", want.stage, want.verb, want.site, want.text, printed(p))
		}
	}
	if hasStep(p, 5, "home-a", "skip", "--allow-missing-data") {
		t.Error("replication 2 on three nodes loses nothing, and the plan allows missing data")
	}
	if p.LoseObjects() || p.LoseWrites() != nil {
		t.Error("nothing is lost here, and the plan asks to lose something")
	}
	execute(t, p)
	key := publicKey(t, w, "home-b")
	for name, h := range w.hosts {
		if name != "home-b" && strings.Contains(h.runningWG(), key) {
			t.Errorf("%s still runs home-b as a peer", name)
		}
	}
	if n := w.hosts["home-b"].calls(); n != 0 {
		t.Errorf("the evicted host was reached %d times", n)
	}
	if w.member("home-b") != nil {
		t.Error("home-b is still a Patroni member")
	}
	for _, m := range w.etcd {
		if m.Name == "home-b" {
			t.Error("home-b is still an etcd member")
		}
	}
	for _, zone := range w.layout {
		if zone == "home-b" {
			t.Error("home-b is still in Garage's layout")
		}
	}
	if len(w.skips) != 1 || w.skips[0] != "--version 4" {
		t.Errorf("skip-dead-nodes ran as %v", w.skips)
	}
	cfg, err := config.Load(w.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Sites["home-b"]; ok {
		t.Error("paisans-stack.yaml still declares home-b")
	}
	// The cut comes before anything the cluster does.
	firstCut, firstDel := -1, -1
	for i, line := range w.log {
		if firstCut < 0 && strings.Contains(line, "' sync") {
			firstCut = i
		}
		if firstDel < 0 && strings.Contains(line, "del /service") {
			firstDel = i
		}
	}
	if firstCut < 0 || firstDel < 0 || firstCut > firstDel {
		t.Errorf("the cut (%d) did not come before the member key's delete (%d)", firstCut, firstDel)
	}
	report := strings.Join(p.Remains(), "\n")
	for _, want := range []string{"Rotate the rest in this order", "sops rotate --rm-age", "mesh rekey --all", "cannot be taken back"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report lacks %q:\n%s", want, report)
		}
	}
}

// When the evicted site leads, a streaming Sync Standby takes over by itself
// once the leader is cut off, and nothing is asked.
func TestAnEvictedLeaderFailsOverToTheSyncStandby(t *testing.T) {
	w, zone := evictWorld(t, "home-a", leadersAway)
	p := w.mustBuild("home-a", evictOptions(zone))
	if p.LoseWrites() != nil {
		t.Fatalf("a Sync Standby streams, and the plan asks to lose writes: %+v", p.LoseWrites())
	}
	if !hasStep(p, 3, "home-b", "wait", "the streaming Sync Standby home-b takes it over") {
		t.Errorf("no wait for the failover:\n%s", printed(p))
	}
	execute(t, p)
	if w.leader() != "home-b" {
		t.Errorf("the leader is %s", w.leader())
	}
	if n := w.hosts["home-a"].calls(); n != 0 {
		t.Errorf("the evicted host was reached %d times", n)
	}
}

// Without a streaming Sync Standby the most current replica is promoted, and
// the plan says how far behind it is so the operator can type lose writes.
func TestAnEvictedLeaderWithoutASyncStandbyNamesTheLag(t *testing.T) {
	w, zone := evictWorld(t, "home-a", leadersAway, func(text string) string {
		return strings.Replace(text, "  synchronous: true", "  synchronous: false", 1)
	})
	w.syncMode = false
	for _, m := range w.members {
		if m["Role"] == "Sync Standby" {
			m["Role"] = "Replica"
		}
	}
	w.member("home-b")["Receive Lag"] = 4.0
	w.member("home-c")["Receive Lag"] = 1.5
	p := w.mustBuild("home-a", evictOptions(zone))
	lag := p.LoseWrites()
	if lag == nil || lag.Replica != "home-c" || !lag.Known || lag.MB != 1.5 {
		t.Fatalf("lose writes = %+v", lag)
	}
	if text := p.LoseWritesText(); !strings.Contains(text, "home-c") || !strings.Contains(text, "1.5 MB behind") {
		t.Errorf("text: %s", text)
	}
	execute(t, p)
	if l := w.leader(); l == "home-a" || l == "" {
		t.Errorf("the leader is %q", l)
	}
}

// A replica of 1 holds each object once, so skipping the evicted node loses
// what only it held: the plan says so and passes --allow-missing-data.
func TestAnEvictedGarageNodeAtReplicationOneLosesObjects(t *testing.T) {
	one := func(text string) string {
		return strings.Replace(text, "    sites: [home-a, home-b, home-c]\n    replication: 2", "    sites: [home-a, home-b, home-c]\n    replication: 1", 1)
	}
	w, zone := evictWorld(t, "home-b", one)
	p := w.mustBuild("home-b", evictOptions(zone))
	if !p.LoseObjects() {
		t.Fatal("replication 1 loses objects, and the plan does not say so")
	}
	if !hasStep(p, 5, "home-a", "skip", "--allow-missing-data") || !hasStep(p, 5, "home-a", "skip", "Objects only home-b held are lost") {
		t.Errorf("stage 5:\n%s", printed(p))
	}
	execute(t, p)
	if len(w.skips) != 1 || !strings.Contains(w.skips[0], "--allow-missing-data") {
		t.Errorf("skip-dead-nodes ran as %v", w.skips)
	}
}

// The replacement is declared in the same edit: the eviction leaves its host
// alone, its peer out of the cut, and the gateway's records pointing at the
// evicted host go even at names the replacement will answer.
func TestAnEvictedGatewayWithItsReplacementDeclared(t *testing.T) {
	w, zone := evictWorld(t, "vm", replacement)
	w.joining("home-d")
	zone.InZone["zone-1"] = []dns.Record{
		{ID: "rec-talk", Type: "A", Name: "talk.example.org", Content: "203.0.113.10", Comment: comment},
		{ID: "rec-hand", Type: "A", Name: "hand.example.org", Content: "203.0.113.10"},
	}
	p := w.mustBuild("vm", evictOptions(zone))
	if got := strings.Join(p.Joining(), ","); got != "home-d" {
		t.Fatalf("joining: %s", got)
	}
	if !p.Gateway() {
		t.Error("vm is the gateway")
	}
	execute(t, p)
	if len(zone.Deletes) != 1 || zone.Deletes[0] != "rec-talk" {
		t.Errorf("deleted %v", zone.Deletes)
	}
	d := w.hosts["home-d"]
	if len(d.commands) != 0 || len(d.files) != 0 {
		t.Errorf("home-d was changed: %v", d.commands)
	}
	newKey := publicKey(t, w, "home-d")
	for name, h := range w.hosts {
		if name != "vm" && strings.Contains(h.runningWG(), newKey) {
			t.Errorf("%s was handed home-d as a peer before it is prepared", name)
		}
	}
	report := strings.Join(p.Remains(), "\n")
	if !strings.Contains(report, "run `paisans apply` to bring up home-d") || !strings.Contains(report, "answers nothing until that apply") {
		t.Errorf("no apply, or no dark names, in the report:\n%s", report)
	}
	if n := w.hosts["vm"].calls(); n != 0 {
		t.Errorf("the evicted host was reached %d times", n)
	}
}

// A gateway's eviction refuses before anything changes while the token in
// the secrets file does not work: the revoked one is still there.
func TestAnEvictedGatewayNeedsAWorkingToken(t *testing.T) {
	w, _ := evictWorld(t, "vm", replacement)
	w.joining("home-d")
	revoked := dnstest.New()
	_, err := w.build("vm", evictOptions(revoked))
	if err == nil || !strings.Contains(err.Error(), "does not work against cloudflare") || !strings.Contains(err.Error(), "secrets set external.acme_dns_token") {
		t.Fatalf("err = %v", err)
	}
	for name, h := range w.hosts {
		for _, c := range h.commands {
			if strings.Contains(c, "' sync") || strings.Contains(c, "member remove") {
				t.Errorf("%s ran %q", name, c)
			}
		}
	}
}

// Refusals that need live state, each before anything changes.
func TestAnEvictionRefuses(t *testing.T) {
	t.Run("quorum", func(t *testing.T) {
		w, zone := evictWorld(t, "home-b")
		w.etcdDown["home-a"], w.etcdDown["home-c"] = true, true
		_, err := w.build("home-b", evictOptions(zone))
		if err == nil || !strings.Contains(err.Error(), "not a majority of the voters etcd has now") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a joining host that does not answer", func(t *testing.T) {
		w, zone := evictWorld(t, "vm", replacement)
		w.joining("home-d")
		w.unreachable["home-d"] = true
		_, err := w.build("vm", evictOptions(zone))
		if err == nil || !strings.Contains(err.Error(), "home-d does not answer over ssh") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("the only gateway with no replacement", func(t *testing.T) {
		w, zone := evictWorld(t, "vm", func(text string) string {
			return strings.ReplaceAll(text, "placement: { pinned: vm }", "placement: { pinned: home-c }")
		})
		_, err := w.build("vm", evictOptions(zone))
		if err == nil || !strings.Contains(err.Error(), "Declare its replacement") {
			t.Errorf("err = %v", err)
		}
	})
}

// Two remaining sites with no endpoint peer only through the evicted site,
// their one relay: cutting it cuts them.
func TestAnEvictionRefusesToCutTheOnlyRelay(t *testing.T) {
	text := `version: 1
id: f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01
community: { name: Relay, domain: example.org }
mesh: { subnet: 10.44.0.0/24 }
acme: { provider: cloudflare }
operators:
  alice:
    name: Alice Example
    email: alice@example.org
    keys: |
      ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice@example.org
sites:
  hub:
    roles: [gateway]
    address: 10.44.0.1
    endpoint: 203.0.113.1:51820
    ssh: { host: hub.local, user: ubuntu, keys: [alice] }
  home-a:
    roles: [apps]
    address: 10.44.0.2
    ssh: { host: home-a.local, user: ubuntu, keys: [alice] }
  home-b:
    roles: [apps, gateway]
    address: 10.44.0.3
    ssh: { host: home-b.local, user: ubuntu, keys: [alice] }
`
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	err = siteremove.RefuseLostRelay(cfg, "hub")
	if err == nil || !strings.Contains(err.Error(), "home-a and home-b reach the mesh only through hub") {
		t.Errorf("err = %v", err)
	}
	if err := siteremove.RefuseLostRelay(cfg, "home-a"); err != nil {
		t.Errorf("home-a relays nothing: %v", err)
	}
}

// A run stopped at any stage resumes there: the next Build plans nothing for
// what is done, and the run completes.
func TestAnEvictionResumesFromEachStage(t *testing.T) {
	for _, tc := range []struct {
		fail string
		// stage is where the run stops; done are steps a resumed plan must
		// not plan again.
		stage int
		done  []struct {
			stage            int
			site, verb, text string
		}
	}{
		{"' sync", 1, nil},
		{"del /service", 3, []struct {
			stage            int
			site, verb, text string
		}{{1, "home-a", "mesh", ""}}},
		{"member remove", 4, []struct {
			stage            int
			site, verb, text string
		}{{1, "vm", "mesh", ""}, {3, "home-a", "delete", "members/home-b"}}},
		{"skip-dead-nodes", 5, []struct {
			stage            int
			site, verb, text string
		}{{4, "home-a", "remove", "etcd member"}, {5, "home-a", "remove", "bbbbbbbbbbbbbbbb"}}},
		{"restart haproxy", 6, []struct {
			stage            int
			site, verb, text string
		}{{5, "home-a", "skip", "skip-dead-nodes"}}},
	} {
		t.Run(tc.fail, func(t *testing.T) {
			w, zone := evictWorld(t, "home-b")
			w.failOnce = tc.fail
			err := siteremove.Execute(w.mustBuild("home-b", evictOptions(zone)))
			if err == nil || !strings.Contains(err.Error(), "stopped at stage") || !strings.Contains(err.Error(), "--evict again") {
				t.Fatalf("err = %v", err)
			}
			again := w.mustBuild("home-b", evictOptions(zone))
			for _, d := range tc.done {
				if hasStep(again, d.stage, d.site, d.verb, d.text) {
					t.Errorf("the resumed plan does %s %s %q again:\n%s", d.verb, d.site, d.text, printed(again))
				}
			}
			if !again.Pending() {
				t.Error("a stopped eviction has nothing left to do")
			}
			execute(t, again)
			if w.member("home-b") != nil || len(w.layout) != 2 {
				t.Error("the resumed eviction did not finish")
			}
			if n := w.hosts["home-b"].calls(); n != 0 {
				t.Errorf("the evicted host was reached %d times", n)
			}
		})
	}
}

// sudo refusing the helper while the cut's gate reads handshakes is the
// host's owner's problem, said as such.
func TestAnEvictionSaysWhenSudoRefusesTheHelper(t *testing.T) {
	w, zone := evictWorld(t, "home-b")
	w.failOnce, w.failOut = "show latest-handshakes", "sudo: a password is required\n"
	err := siteremove.Execute(w.mustBuild("home-b", evictOptions(zone)))
	if !errors.Is(err, apply.ErrHelperRefused) || !strings.Contains(err.Error(), "could not be run on home-a") || !strings.Contains(err.Error(), "site admit") {
		t.Fatalf("want the helper's sudo refusal as the owner's problem, got %v", err)
	}
}

// A site whose mesh file was written by a run that stopped before its
// interface took it is handed the file again, and nothing else is.
func TestAPartialCutCompletesOnTheSiteThatMissedIt(t *testing.T) {
	w, zone := evictWorld(t, "home-b")
	w.failOnce = "' sync"
	err := siteremove.Execute(w.mustBuild("home-b", evictOptions(zone)))
	if err == nil || !strings.Contains(err.Error(), "did not take on every remaining site") {
		t.Fatalf("err = %v", err)
	}
	key := publicKey(t, w, "home-b")
	var missed []string
	for _, name := range []string{"home-a", "home-c", "vm", "watch"} {
		if strings.Contains(w.hosts[name].runningWG(), key) {
			missed = append(missed, name)
		}
	}
	if len(missed) != 1 {
		t.Fatalf("want one site to have missed the cut, got %v", missed)
	}
	again := w.mustBuild("home-b", evictOptions(zone))
	var cut []string
	for _, s := range steps(again, 1) {
		if s.Verb == "mesh" || s.Verb == "update" {
			cut = append(cut, s.Site)
		}
	}
	if strings.Join(cut, ",") != missed[0] {
		t.Errorf("the resumed cut touches %v, want only %s", cut, missed[0])
	}
}

// A remaining site part way through a mesh rekey, its host and every peer
// already on its next key, keeps that key through the cut: the cut renders
// it with the key its host runs, not the one the secrets call current.
func TestTheCutKeepsAPeerMidRekeyOnItsNewKey(t *testing.T) {
	w, zone := evictWorld(t, "home-b")
	// Every host was rendered with home-c's key as it is now; the secrets
	// say that key is the next one, as a rekey stopped after its swap
	// leaves them.
	c := w.secrets.Sites["home-c"]
	next := c.WireGuardPrivateKey
	c.WireGuardPrivateKey, c.WireGuardNextPrivateKey = "SUlJSUlJSUlJSUlJSUlJSUlJSUlJSUlJSUlJSUlJSUk=", next
	w.secrets.Sites["home-c"] = c
	nextPublic, err := render.PublicKey(next)
	if err != nil {
		t.Fatal(err)
	}
	p := w.mustBuild("home-b", evictOptions(zone))
	execute(t, p)
	for _, name := range []string{"home-a", "vm", "watch"} {
		if !strings.Contains(w.hosts[name].runningWG(), nextPublic) {
			t.Errorf("%s no longer runs home-c's new key", name)
		}
	}
	if !strings.Contains(w.hosts["home-c"].runningWG(), "PrivateKey = "+next) {
		t.Error("home-c no longer runs its new key")
	}
}

// A site evicted part way through its own rekey may be listed by its next
// key: a cut that stopped part way is completed on the site that missed it,
// and the gate looks for either key.
func TestAPartialCutOfASiteMidRekeyCompletes(t *testing.T) {
	w, zone := evictWorld(t, "home-b")
	b := w.secrets.Sites["home-b"]
	running := b.WireGuardPrivateKey
	b.WireGuardPrivateKey, b.WireGuardNextPrivateKey = "SUlJSUlJSUlJSUlJSUlJSUlJSUlJSUlJSUlJSUlJSUk=", running
	w.secrets.Sites["home-b"] = b
	key, err := render.PublicKey(running)
	if err != nil {
		t.Fatal(err)
	}
	w.failOnce = "' sync"
	if err := siteremove.Execute(w.mustBuild("home-b", evictOptions(zone))); err == nil {
		t.Fatal("the cut that missed a site passed")
	}
	execute(t, w.mustBuild("home-b", evictOptions(zone)))
	for name, h := range w.hosts {
		if name != "home-b" && strings.Contains(h.runningWG(), key) {
			t.Errorf("%s still runs home-b as a peer", name)
		}
	}
}

// hostileLeader is home-a leading with its replicas stopped from streaming,
// as a leader in hostile hands can make them.
func hostileLeader(t *testing.T) (*world, *dnstest.Provider) {
	w, zone := evictWorld(t, "home-a", leadersAway)
	for _, name := range []string{"home-b", "home-c"} {
		w.member(name)["Role"], w.member(name)["State"] = "Replica", "running"
	}
	return w, zone
}

// A leader that stops its replicas streaming does not block its own
// eviction: the plan asks to lose writes instead, and goes on.
func TestAHostileLeaderCannotBlockItsEviction(t *testing.T) {
	w, zone := hostileLeader(t)
	p, err := w.build("home-a", evictOptions(zone))
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if p.LoseWrites() == nil {
		t.Error("no streaming Sync Standby, and the plan asks to lose nothing")
	}
	execute(t, p)
	if l := w.leader(); l == "home-a" || l == "" {
		t.Errorf("the leader is %q", l)
	}
}

// A cut that stopped part way, the replicas not streaming, resumes: both
// while the evicted site still leads, and once another has taken over and
// the replicas have not caught up with it yet.
func TestAPartialCutResumesWithReplicasNotStreaming(t *testing.T) {
	t.Run("the evicted site leads", func(t *testing.T) {
		w, zone := hostileLeader(t)
		w.failOnce = "' sync"
		if err := siteremove.Execute(w.mustBuild("home-a", evictOptions(zone))); err == nil {
			t.Fatal("the partial cut passed")
		}
		execute(t, w.mustBuild("home-a", evictOptions(zone)))
		if w.member("home-a") != nil {
			t.Error("home-a is still a member")
		}
	})
	t.Run("another site leads", func(t *testing.T) {
		w, zone := evictWorld(t, "home-a", leadersAway)
		w.failOnce = "' sync"
		if err := siteremove.Execute(w.mustBuild("home-a", evictOptions(zone))); err == nil {
			t.Fatal("the partial cut passed")
		}
		w.member("home-a")["Role"], w.member("home-a")["State"] = "Replica", "stopped"
		w.member("home-b")["Role"], w.member("home-b")["State"] = "Leader", "running"
		w.member("home-c")["Role"], w.member("home-c")["State"] = "Replica", "running"
		p, err := w.build("home-a", evictOptions(zone))
		if err != nil {
			t.Fatalf("a partial cut does not resume: %v", err)
		}
		w.member("home-c")["State"] = "streaming"
		execute(t, p)
	})
}

// When the evicted site does not lead, the members that stay must still be
// healthy before anything changes.
func TestAnEvictedReplicaNeedsTheRestStreaming(t *testing.T) {
	w, zone := evictWorld(t, "home-c")
	w.member("home-b")["State"] = "running"
	if _, err := w.build("home-c", evictOptions(zone)); err == nil || !strings.Contains(err.Error(), "home-b is running, not streaming") {
		t.Errorf("err = %v", err)
	}
}

// Patroni is read again immediately before the cut: the leader, and who
// would take over from it, decide what the operator agreed to, and either
// having changed since the plan was read stops the run before anything
// changes.
func TestAnEvictionRereadsTheLeaderBeforeTheCut(t *testing.T) {
	noSync := func(w *world) {
		w.syncMode = false
		for _, m := range w.members {
			if m["Role"] == "Sync Standby" {
				m["Role"] = "Replica"
			}
		}
		w.member("home-b")["Receive Lag"] = 4.0
		w.member("home-c")["Receive Lag"] = 1.5
	}
	asyncConfig := func(text string) string {
		return strings.Replace(text, "  synchronous: true", "  synchronous: false", 1)
	}
	for _, tc := range []struct {
		name   string
		site   string
		edits  []func(string) string
		before func(*world)
		after  func(*world)
		want   string
	}{
		{"the evicted site took the leader", "home-b", nil, nil, func(w *world) {
			w.member("home-a")["Role"], w.member("home-a")["State"] = "Replica", "streaming"
			w.member("home-b")["Role"], w.member("home-b")["State"] = "Leader", "running"
		}, "home-b took the Patroni leader"},
		{"the evicted site lost the leader", "home-a", []func(string) string{leadersAway}, nil, func(w *world) {
			w.member("home-a")["Role"], w.member("home-a")["State"] = "Replica", "streaming"
			w.member("home-b")["Role"], w.member("home-b")["State"] = "Leader", "running"
		}, "home-a no longer holds the Patroni leader"},
		{"another replica is now the most current", "home-a", []func(string) string{leadersAway, asyncConfig}, noSync, func(w *world) {
			w.member("home-b")["Receive Lag"] = 0.5
		}, "which `lose writes` agreed to, changed"},
		{"a Sync Standby now streams", "home-a", []func(string) string{leadersAway, asyncConfig}, noSync, func(w *world) {
			w.member("home-b")["Role"] = "Sync Standby"
		}, "Sync Standby that would take over changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, zone := evictWorld(t, tc.site, tc.edits...)
			if tc.before != nil {
				tc.before(w)
			}
			p := w.mustBuild(tc.site, evictOptions(zone))
			tc.after(w)
			err := siteremove.Execute(p)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Nothing was changed") {
				t.Fatalf("err = %v", err)
			}
			for name, h := range w.hosts {
				for _, c := range h.commands {
					if strings.Contains(c, "' sync") {
						t.Errorf("%s ran %q", name, c)
					}
				}
			}
		})
	}
}

// A site whose host lost its admission record is not taken for a new one while anything
// says it ran in this deployment: a remaining site listing its key, or a
// gateway's deployment record listing it. Taken for new, it would be left out
// of the cut while its peers still talk to it.
func TestAnEvictionRefusesASiteThatOnlyLostItsClaim(t *testing.T) {
	t.Run("a peer lists its key", func(t *testing.T) {
		w, zone := evictWorld(t, "home-b")
		path, _ := admitted(t, w.cfg, "home-c")
		delete(w.hosts["home-c"].files, path)
		_, err := w.build("home-b", evictOptions(zone))
		if err == nil || !strings.Contains(err.Error(), "home-c's host has not admitted this deployment") || !strings.Contains(err.Error(), "home-a lists its WireGuard key as a peer") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the gateway's record lists it", func(t *testing.T) {
		w, zone := evictWorld(t, "home-b", replacement)
		w.joining("home-d")
		w.hosts["vm"].files[deployrecord.Path(dep)] = deployrecord.Encode(deployrecord.Record{Sites: []string{"home-a", "home-b", "home-c", "home-d", "vm", "watch"}})
		_, err := w.build("home-b", evictOptions(zone))
		if err == nil || !strings.Contains(err.Error(), "home-d's host has not admitted this deployment") || !strings.Contains(err.Error(), "deployment record on the gateway lists it") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("nothing does", func(t *testing.T) {
		w, zone := evictWorld(t, "home-b", replacement)
		w.joining("home-d")
		p, err := w.build("home-b", evictOptions(zone))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(p.Joining(), ","); got != "home-d" {
			t.Errorf("joining: %s", got)
		}
	})
}
