package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// healthOnItsAddress is the example with health dialled on its
// public_address, no endpoint: a listen port, 51820, that init owns.
func healthOnItsAddress(body string) string {
	return strings.Replace(body, "    endpoint: health.example.org:51820\n", "", 1)
}

// portHeld is admit's refusal of health for the listen port 51820, with
// the free port admit found.
func portHeld(free int) error {
	return &portMoved{Refusal: claimRefused("apps", "WireGuard listen port 51820/udp"), Held: 51820, Free: free}
}

// initHealthHeld runs init at a terminal, admitting health, the one site
// not admitted, whose listen port another deployment holds, answered by
// script.
func initHealthHeld(t *testing.T, edit func(string) string, script []error, prepare func(map[string]*initFake)) (string, *ui.Recorder, *[]string) {
	t.Helper()
	sites := fakeSites()
	sites["health"].unadmitted = true
	if prepare != nil {
		prepare(sites)
	}
	path, rec := initWorld(t, edit, sites, nil)
	ran := scriptAdmitFromInit(t, sites, map[string][]error{"health": script})
	if out, err := runInitAt(t, path, true, "y\n"); err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, rec.Lines())
	}
	return path, rec, ran
}

// A listen port another deployment holds, on a site whose port init owns,
// moves to the free port admit found: the file names it beside
// public_address, every comment kept, and init says what to open, then
// admits the site again on it.
func TestInitMovesAListenPortAnotherDeploymentHolds(t *testing.T) {
	path, rec, ran := initHealthHeld(t, healthOnItsAddress, []error{portHeld(51827), nil}, nil)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sites["health"].Endpoint != ":51827" || cfg.Sites["health"].ListenPort() != 51827 {
		t.Errorf("health's endpoint is %q, want \":51827\"", cfg.Sites["health"].Endpoint)
	}
	if !strings.Contains(string(data), "    public_address: 203.0.113.20\n    endpoint: \":51827\"\n") || !strings.Contains(string(data), "# What is in front of the monitor.") {
		t.Errorf("the endpoint was not written beside public_address with the comments kept:\n%s", data)
	}
	want := "sites.health.endpoint: 51820/udp is held by deployment " + otherID + "; moved to :51827. Open 51827/udp in any firewall in front of the host, and forward it where the host is behind NAT."
	if !rec.Has("detail", want) || !rec.Has("done", "move health's WireGuard listen port") {
		t.Errorf("init did not report the move:\n%s", rec.Lines())
	}
	if strings.Join(*ran, ",") != "health+port,health" || !rec.Has("done", "check health's admission") {
		t.Errorf("health was not admitted again on the new port: admit ran %v\n%s", *ran, rec.Lines())
	}
}

// An endpoint written as a host and a port is the port a router or a
// dynamic DNS name forwards, so init never moves it: admit refuses as it
// found the host, and the file is unchanged.
func TestInitKeepsAListenPortItDoesNotOwn(t *testing.T) {
	var before []byte
	path, rec, ran := initHealthHeld(t, func(body string) string { before = []byte(body); return body }, []error{portHeld(51827)}, nil)
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "endpoint: health.example.org:51820\n") || len(data) != len(before) {
		t.Errorf("the file changed:\n%s", data)
	}
	if strings.Join(*ran, ",") != "health" {
		t.Errorf("admit ran %v, want once with no port to move", *ran)
	}
	i := rec.Index("note", "health has not admitted this deployment yet, so init skipped it")
	if i < 0 || !strings.Contains(rec.Events[i].Extra, "holds WireGuard listen port 51820/udp") || !strings.Contains(rec.Events[i].Extra, "Give this site's endpoint another port") {
		t.Errorf("init did not skip health with the refusal:\n%s", rec.Lines())
	}
}

// Once the deployment has been applied, the port is in every other
// site's WireGuard file, and init does not move it.
func TestInitKeepsAListenPortAfterTheFirstApply(t *testing.T) {
	example, err := config.Load(filepath.Join("..", "..", "examples", "paisans-stack.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	path, rec, ran := initHealthHeld(t, healthOnItsAddress, []error{portHeld(51827)}, func(sites map[string]*initFake) {
		sites["home-a"].files = map[string]string{example.Deployment().Manifest(): "{}"}
	})
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sites["health"].Endpoint != "" {
		t.Errorf("health's endpoint was written: %q", cfg.Sites["health"].Endpoint)
	}
	if strings.Join(*ran, ",") != "health+port" || rec.Has("done", "move health's WireGuard listen port") {
		t.Errorf("init moved the port of an applied deployment: admit ran %v\n%s", *ran, rec.Lines())
	}
	if !rec.Has("note", "health has not admitted this deployment yet, so init skipped it") {
		t.Errorf("init did not skip health:\n%s", rec.Lines())
	}
}

// A listen port held beside the gateway role is refused for the role: no
// port frees the host, so nothing is written and the advice is the
// role's.
func TestInitDoesNotMoveAListenPortHeldBesideARole(t *testing.T) {
	refusal := claimRefused("gateway", "WireGuard listen port 51820/udp", "the gateway role")
	path, rec, ran := initHealthHeld(t, healthOnItsAddress, []error{refusal}, nil)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sites["health"].Endpoint != "" {
		t.Errorf("health's endpoint was written: %q", cfg.Sites["health"].Endpoint)
	}
	if strings.Join(*ran, ",") != "health+port" {
		t.Errorf("admit ran %v, want once", *ran)
	}
	i := rec.Index("note", "health has not admitted this deployment yet, so init skipped it")
	if i < 0 || !strings.Contains(rec.Events[i].Extra, "holds the gateway role") || strings.Contains(rec.Events[i].Extra, "another port") {
		t.Errorf("init did not skip health with the gateway advice alone:\n%s", rec.Lines())
	}
}

// An admitted site init cannot read may hold the manifest, so the port
// stays, and a note names the site, since the refusal says nothing of why.
func TestInitSaysWhichSiteKeptAListenPort(t *testing.T) {
	sites := fakeSites()
	sites["health"].unadmitted = true
	sites["home-a"].broken = errors.New("permission denied")
	path, rec := initWorld(t, healthOnItsAddress, sites, nil)
	ran := scriptAdmitFromInit(t, sites, map[string][]error{"health": {portHeld(51827)}})
	// init then stops at the subnet, which it cannot settle unread.
	_, _ = runInitAt(t, path, true, "y\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sites["health"].Endpoint != "" || strings.Join(*ran, ",") != "health+port" {
		t.Errorf("health's endpoint %q, admit ran %v: want the port kept", cfg.Sites["health"].Endpoint, *ran)
	}
	i := rec.Index("note", "health's WireGuard listen port was not moved")
	if i < 0 || !strings.Contains(rec.Events[i].Extra, "init could not read home-a (") || !strings.Contains(rec.Events[i].Extra, "permission denied") {
		t.Errorf("init did not name the site it could not read:\n%s", rec.Lines())
	}
}
