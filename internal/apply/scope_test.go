package apply_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
)

const wgFile = "srv/paisans/f2a9/psns-f2a9.conf"

// appliedHost is home-a after a whole first apply of the fixture, with psns-f2a9 up.
func appliedHost(t *testing.T) *fakeHost {
	t.Helper()
	host := newHost()
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	host.commands = nil
	return host
}

// withNewPeer is the fixture with home-b moved to another endpoint, which
// changes the peer list in home-a's psns-f2a9.conf.
func withNewPeer(t *testing.T) func(*config.Config) {
	return func(cfg *config.Config) {
		site := cfg.Sites["home-b"]
		site.Endpoint = "198.51.100.20:51820"
		cfg.Sites["home-b"] = site
	}
}

// A scoped apply writes its one file, syncs the mesh, acts on no stack, and
// leaves the manifest able to recognise every other file as ours.
func TestAScopedApplyMovesOneFile(t *testing.T) {
	host := appliedHost(t)
	rendered := planWith(t, withNewPeer(t))

	p, err := apply.Build("home-a", rendered, acmeModule(t), host, apply.Scope(wgFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Changes) != 1 || p.Changes[0].Kind != apply.Update {
		t.Fatalf("want one update, got %v", p.Changes)
	}
	if p.WireGuard != apply.WireGuardSync || len(p.Actions) != 0 || p.GatewayChanging {
		t.Fatalf("a scoped mesh change planned %v %v %v", p.WireGuard, p.Actions, p.GatewayChanging)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if !host.ran(wgHelper + "sync") {
		t.Error("the new peer was not handed to psns-f2a9")
	}
	if host.ran("docker compose") {
		t.Errorf("a scoped apply acted on a stack: %v", host.commands)
	}

	whole, err := apply.Build("home-a", rendered, acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if c := whole.Conflicts(); len(c) != 0 {
		t.Fatalf("a whole apply after a scoped one sees conflicts: %v", c)
	}
	for _, c := range whole.Changes {
		if strings.HasSuffix(c.Path, "psns-f2a9.conf") && c.Kind != apply.Unchanged {
			t.Errorf("psns-f2a9.conf is %s after the scoped apply wrote it", c.Kind)
		}
	}
}

// A scoped apply of an unchanged mesh file still reads what the interface
// runs, so a run that stopped before the sync is completed by it too.
func TestAScopedApplySyncsAnInterfaceOnOtherPeers(t *testing.T) {
	host := appliedHost(t)
	cut := strings.Index(host.wgRun, "[Peer]\n# watch")
	if cut < 0 {
		t.Fatalf("the fixture's home-a has no peer watch:\n%s", host.wgRun)
	}
	host.wgRun = host.wgRun[:cut]
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.Scope(wgFile))
	if err != nil {
		t.Fatal(err)
	}
	if p.WireGuard != apply.WireGuardSync {
		t.Errorf("a scoped apply of an interface missing a peer plans %v, want a sync", p.WireGuard)
	}
}

// Rolling back restores the old file and the old record, and syncs the mesh
// again, so the next apply sees the old file as ours.
func TestAScopedApplyRollsBack(t *testing.T) {
	host := appliedHost(t)
	before := host.files["/"+wgFile]
	p, err := apply.Build("home-a", planWith(t, withNewPeer(t)), acmeModule(t), host, apply.Scope(wgFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	host.commands = nil
	if err := apply.Rollback(p, host); err != nil {
		t.Fatal(err)
	}
	if host.files["/"+wgFile] != before {
		t.Error("psns-f2a9.conf was not restored")
	}
	if host.files[wgInstalled] != before {
		t.Error("the helper was not handed the restored file")
	}
	if put, sync := host.indexOf(wgHelper+"put"), host.indexOf(wgHelper+"sync"); put < 0 || sync < put {
		t.Errorf("the restored file was not put before the sync: %v", host.commands)
	}
	if !host.ran(wgHelper + "sync") {
		t.Error("the restored peers were not handed to psns-f2a9")
	}
	again, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Conflicts()) != 0 || len(again.Writes()) != 0 {
		t.Errorf("after rollback the original render is not what the host holds: %v", again.Changes)
	}
}

func TestAScopeNamingNoRenderedFileIsRefused(t *testing.T) {
	host := appliedHost(t)
	if _, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.Scope("srv/paisans/f2a9/nothing/here")); err == nil {
		t.Error("a scope naming nothing rendered was accepted")
	}
}

// Two scoped applies of one site, both planned before either runs, as a
// staged command plans them: each records its own file, and the later one
// keeps what the earlier one recorded. A following apply whose render moves
// on again finds neither file edited, and still finds a hand edit made after
// both.
func TestScopedAppliesPlannedTogetherKeepEachOthersRecord(t *testing.T) {
	const haproxy = "srv/paisans/f2a9/infra/haproxy/haproxy.cfg"
	host := appliedHost(t)
	rendered := planWith(t, withNewPeer(t))
	for i := range rendered.Files {
		if rendered.Files[i].Path == "home-a/"+haproxy {
			rendered.Files[i].Content += "# without a backend\n"
		}
	}
	mesh, err := apply.Build("home-a", rendered, acmeModule(t), host, apply.Scope(wgFile))
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := apply.Build("home-a", rendered, acmeModule(t), host, apply.Scope(haproxy))
	if err != nil {
		t.Fatal(err)
	}
	if len(mesh.Writes()) != 1 || len(proxy.Writes()) != 1 {
		t.Fatalf("want one write each, got %v and %v", mesh.Writes(), proxy.Writes())
	}
	for _, p := range []*apply.Plan{mesh, proxy} {
		if err := apply.Execute(p, host); err != nil {
			t.Fatal(err)
		}
	}

	next := planWith(t, func(cfg *config.Config) {
		withNewPeer(t)(cfg)
		site := cfg.Sites["home-b"]
		site.Endpoint = "198.51.100.30:51820"
		cfg.Sites["home-b"] = site
	})
	for i := range next.Files {
		if next.Files[i].Path == "home-a/"+haproxy {
			next.Files[i].Content += "# with a new backend\n"
		}
	}
	scope := apply.Scope(wgFile, haproxy)
	again, err := apply.Build("home-a", next, acmeModule(t), host, scope)
	if err != nil {
		t.Fatal(err)
	}
	if c := again.Conflicts(); len(c) != 0 {
		t.Fatalf("files two scoped applies wrote read as edited on the host: %v", c)
	}

	host.files["/"+wgFile] += "# a hand edit\n"
	edited, err := apply.Build("home-a", next, acmeModule(t), host, scope)
	if err != nil {
		t.Fatal(err)
	}
	if c := edited.Conflicts(); len(c) != 1 || c[0].Path != "/"+wgFile {
		t.Errorf("a hand edit after both applies is not the one conflict: %v", c)
	}
}

// A manifest that is not readable when the record is written stops the
// apply with the reason, and is left as it is: nothing replaces it with a
// record of this plan's files alone.
func TestAnUnreadableManifestAtWriteStopsTheApply(t *testing.T) {
	host := appliedHost(t)
	p, err := apply.Build("home-a", planWith(t, withNewPeer(t)), acmeModule(t), host, apply.Scope(wgFile))
	if err != nil {
		t.Fatal(err)
	}
	host.files[manifestFile] = "{not json"
	err = apply.Execute(p, host)
	if err == nil || !strings.Contains(err.Error(), "is not readable as a manifest") {
		t.Fatalf("want the manifest refused, got %v", err)
	}
	if got := host.files[manifestFile]; got != "{not json" {
		t.Errorf("the unreadable manifest was replaced:\n%s", got)
	}
}

// A manifest gone when the record is written leaves a scoped apply's own
// files recorded and nothing else, so the next apply takes every other file
// that differs from its render for somebody else's, as it does on a host
// whose manifest was deleted.
func TestAManifestGoneAtWriteRecordsOnlyThePlansFiles(t *testing.T) {
	const haproxy = "srv/paisans/f2a9/infra/haproxy/haproxy.cfg"
	host := appliedHost(t)
	p, err := apply.Build("home-a", planWith(t, withNewPeer(t)), acmeModule(t), host, apply.Scope(wgFile))
	if err != nil {
		t.Fatal(err)
	}
	delete(host.files, manifestFile)
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	next := planWith(t, withNewPeer(t))
	for i := range next.Files {
		if next.Files[i].Path == "home-a/"+haproxy || next.Files[i].Path == "home-a/"+wgFile {
			next.Files[i].Content += "# moved on\n"
		}
	}
	again, err := apply.Build("home-a", next, acmeModule(t), host, apply.Scope(wgFile, haproxy))
	if err != nil {
		t.Fatal(err)
	}
	if c := again.Conflicts(); len(c) != 1 || c[0].Path != "/"+haproxy {
		t.Errorf("want only the unrecorded %s as a conflict, got %v", haproxy, c)
	}
}

// A plan made without the site's architecture records the one the manifest
// has when written, so an earlier stage's record of it is kept.
func TestAScopedApplyKeepsTheArchitectureRecordedSince(t *testing.T) {
	host := appliedHost(t)
	p, err := apply.Build("home-a", planWith(t, withNewPeer(t)), acmeModule(t), host, apply.Scope(wgFile))
	if err != nil {
		t.Fatal(err)
	}
	var m render.Manifest
	if err := json.Unmarshal([]byte(host.files[manifestFile]), &m); err != nil {
		t.Fatal(err)
	}
	m.Arch = "arm64"
	data, _ := json.Marshal(m)
	host.files[manifestFile] = string(data)
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	var written render.Manifest
	if err := json.Unmarshal([]byte(host.files[manifestFile]), &written); err != nil {
		t.Fatal(err)
	}
	if written.Arch != "arm64" {
		t.Errorf("the architecture recorded since the plan was made became %q", written.Arch)
	}
}
