package apply_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// fastResolve replaces the wait for the mesh's endpoint names with one that
// counts its sleeps instead of sleeping.
func fastResolve(t *testing.T) *int {
	t.Helper()
	slept := 0
	t.Cleanup(apply.SetResolveWait(6*time.Second, 2*time.Second, func(time.Duration) { slept++ }))
	return &slept
}

// polls are the commands that asked the host which endpoint names resolve.
func (h *fakeHost) polls() []string {
	var out []string
	for _, command := range h.commands {
		if strings.Contains(command, "getent ahosts") {
			out = append(out, command)
		}
	}
	return out
}

// A host that looked a peer's name up before its record existed holds the
// "not found" for as long as the zone's negative TTL, and wg treats that as
// permanent. The cache is flushed and the names asked for before wg-quick
// runs, and only the names: an address needs no resolving.
func TestHostnameEndpointsAreFlushedAndResolvedBeforeTheMesh(t *testing.T) {
	fastResolve(t)
	host := newHost()
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	polls := host.polls()
	if len(polls) != 1 {
		t.Fatalf("asked %d times whether the endpoint names resolve, want once: %v", len(polls), host.commands)
	}
	poll := polls[0]
	for _, want := range []string{"command -v resolvectl", "resolvectl flush-caches", "'vm.example.org'"} {
		if !strings.Contains(poll, want) {
			t.Errorf("the probe does not carry %q:\n%s", want, poll)
		}
	}
	if strings.Contains(poll, "198.51.100.2") {
		t.Errorf("the probe resolves an address:\n%s", poll)
	}
	// watch's endpoint names a host, but watch declares public_address, so
	// the rendered file dials the address and the name is nobody's to wait on.
	if strings.Contains(poll, "watch.example.org") {
		t.Errorf("the probe waits on a name the rendered file does not dial:\n%s", poll)
	}
	if strings.Index(poll, "flush-caches") > strings.Index(poll, "getent") {
		t.Errorf("the names are asked for before the cache is flushed:\n%s", poll)
	}
	start := host.indexOf("systemctl enable --now wg-quick@psns-f2a9")
	if start < 0 || host.indexOf("getent ahosts") > start {
		t.Errorf("wg-quick ran before the endpoint names were resolved: %v", host.commands)
	}
}

// syncconf resolves the endpoints too, so a peer change waits for them the
// same way a start does.
func TestASyncedMeshResolvesItsEndpointsFirst(t *testing.T) {
	fastResolve(t)
	host := applied(t, "home-a")
	host.files["/etc/wireguard/psns-f2a9.conf"] = strings.Replace(
		host.files["/etc/wireguard/psns-f2a9.conf"], "PersistentKeepalive = 25", "PersistentKeepalive = 30", 1)
	adopt(t, host, "/etc/wireguard/psns-f2a9.conf")
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	sync := host.indexOf("wg syncconf psns-f2a9")
	poll := host.indexOf("getent ahosts")
	if sync < 0 || poll < 0 || poll > sync {
		t.Errorf("the peers were synced before their names were resolved: %v", host.commands)
	}
}

// A name whose record was created a moment ago resolves on a later poll, and
// the mesh starts then. Each poll flushes again: the first lookup after a
// flush caches its own "not found" if the record is not there yet.
func TestANameThatResolvesOnALaterPollLetsTheMeshStart(t *testing.T) {
	slept := fastResolve(t)
	host := newHost()
	host.unresolved = map[string]int{"vm.example.org": 1}
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	polls := host.polls()
	if len(polls) != 2 || *slept != 1 {
		t.Errorf("%d polls and %d sleeps, want 2 and 1", len(polls), *slept)
	}
	for _, poll := range polls {
		if !strings.Contains(poll, "resolvectl flush-caches") {
			t.Errorf("a poll did not flush the cache first:\n%s", poll)
		}
	}
	if !host.ran("systemctl enable --now wg-quick@psns-f2a9") {
		t.Error("the mesh was not started once its names resolved")
	}
}

// A name that never resolves stops the apply before the interface is touched,
// with the advice a failed wg-quick gets for the same cause.
func TestANameThatNeverResolvesStopsBeforeTheMesh(t *testing.T) {
	slept := fastResolve(t)
	host := newHost()
	host.unresolved = map[string]int{"vm.example.org": -1}
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	err = apply.Execute(p, host)
	if err == nil {
		t.Fatal("an apply carried on with a peer whose name does not resolve")
	}
	if len(host.polls()) != 4 || *slept != 3 {
		t.Errorf("%d polls and %d sleeps over a 6s wait polled every 2s, want 4 and 3", len(host.polls()), *slept)
	}
	if host.ran("wg-quick@psns-f2a9") || host.ran("wg syncconf") {
		t.Errorf("the interface was touched: %v", host.commands)
	}
	if host.firstCompose() >= 0 {
		t.Errorf("a stack was started with the mesh down: %v", host.commands)
	}
	var problem *ui.Problem
	if !errors.As(err, &problem) {
		t.Fatalf("the failure is not a problem for the operator:\n%v", err)
	}
	if !strings.HasPrefix(problem.Hint, "mesh: ") {
		t.Errorf("hint = %q", problem.Hint)
	}
	want := "vm.example.org does not resolve from home-a; create its DNS record (paisans dns init) and apply again."
	if !strings.Contains(problem.Explain, want) {
		t.Errorf("the explanation does not give the DNS advice:\n%s", problem.Explain)
	}
	if strings.Contains(problem.Explain, "watch.example.org") {
		t.Errorf("the explanation names a name that resolves:\n%s", problem.Explain)
	}
}

// Every peer written as an address: nothing to resolve, so nothing is asked.
func TestAddressEndpointsAreNotResolved(t *testing.T) {
	fastResolve(t)
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets(filepath.Join("..", "render", "testdata", "secrets.fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, address := range map[string]string{"vm": "198.51.100.3:51820", "watch": "[2001:db8::4]:51820"} {
		site := cfg.Sites[name]
		site.Endpoint = address
		cfg.Sites[name] = site
	}
	built, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	host := newHost()
	p, err := apply.Build("home-a", built, acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if host.ran("getent") || host.ran("resolvectl") {
		t.Errorf("addresses were resolved: %v", host.polls())
	}
	if !host.ran("systemctl enable --now wg-quick@psns-f2a9") {
		t.Error("the mesh was not started")
	}
}

// A mesh that needs nothing resolves nothing: the wait is for wg-quick, not
// for every apply.
func TestAnUntouchedMeshResolvesNothing(t *testing.T) {
	fastResolve(t)
	host := applied(t, "home-a")
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if host.ran("getent") || host.ran("resolvectl") {
		t.Errorf("an untouched mesh resolved its endpoints: %v", host.polls())
	}
}

// The probe itself, in sh: a host without resolvectl, or whose
// systemd-resolved is not running, still answers which names resolve, and
// the probe exits zero either way.
func TestTheResolveProbeNeedsNoResolvectl(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	stub := func(t *testing.T, dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!"+sh+"\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name       string
		resolvectl string
	}{
		{"absent", ""},
		{"resolved not running", "echo 'Failed to flush caches' >&2; exit 1"},
		{"running", "exit 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stub(t, dir, "getent", `[ "$2" = ok.example.org ] && echo "192.0.2.1 STREAM ok.example.org" && exit 0; exit 2`)
			if tc.resolvectl != "" {
				stub(t, dir, "resolvectl", tc.resolvectl)
			}
			cmd := exec.Command(sh, "-c", apply.ResolveProbe([]string{"ok.example.org", "missing.example.org"}))
			cmd.Env = []string{"PATH=" + dir}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("the probe failed: %v\n%s", err, out)
			}
			if string(out) != "missing.example.org\n" {
				t.Errorf("the probe printed %q, want only the name that does not resolve", out)
			}
		})
	}
}
