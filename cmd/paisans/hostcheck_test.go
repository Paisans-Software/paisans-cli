package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// inventoryHost answers only the host check's probes, and records every
// command, so a test can see that nothing but the inventory ran.
type inventoryHost struct {
	answers map[string]string
	ran     []string
}

func (h *inventoryHost) Describe() string { return "ubuntu@host.example.org" }
func (h *inventoryHost) Run(command string) (string, error) {
	h.ran = append(h.ran, command)
	// Asked before the answers, whose "docker network inspect" is the
	// inventory's probe and would also match apply's.
	if out, ok := absentNetworks(command); ok {
		return out, nil
	}
	for key, out := range h.answers {
		if strings.Contains(command, key) {
			return out, nil
		}
	}
	return "", fmt.Errorf("not an inventory probe: %s", command)
}
func (h *inventoryHost) ReadFile(string) (string, bool, error) { return "", false, nil }

// webHost runs another user's web server on 80 and 443, outside Docker.
func webHost() *inventoryHost {
	return &inventoryHost{answers: map[string]string{
		`echo "uid`:                  "uid 1001\nsubuid paisans-f2a9:165536:65536\n",
		"docker version":             "absent\n",
		"dpkg-query":                 "",
		"ss -Hltnue":                 "tcp LISTEN 0 511 0.0.0.0:80 0.0.0.0:* uid:1002 ino:1 sk:1 <->\ntcp LISTEN 0 511 0.0.0.0:443 0.0.0.0:* uid:1002 ino:2 sk:2 <->\n",
		"ip -o link":                 "1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536\n2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500\n",
		"ip -j route":                `[{"dst":"default","dev":"eth0"}]`,
		"/srv/paisans/f2a9/caddy.d'": "",
		"docker inspect":             "",
		"docker volume inspect":      "",
		"docker network inspect":     "",
	}}
}

func hostCheckFixture(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("../../internal/hostcheck/testdata/hostcheck.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// A conflict is refused with the conflict named in the error, which prints
// at any verbosity, the host check's step marked failed, and nothing but the
// inventory's own probes reached the host.
func TestTheGateRefusesAConflictHavingOnlyLooked(t *testing.T) {
	h := webHost()
	rec := &ui.Recorder{}
	_, err := hostGate(rec, hostCheckFixture(t), "edge", h)
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "*:80/tcp (Caddy): claimed by sites.edge.roles (gateway), held by a listener of another user's (uid 1002)") {
		t.Errorf("the refusal does not name the conflict: %v", err)
	}
	if !rec.Has("fail", "host check") {
		t.Errorf("the host check is not marked failed:\n%s", rec.Lines())
	}
	probes := []string{`echo "uid`, "docker version", "dpkg-query", "ss -Hltnue", "ip -o link", "ip -j route", "/srv/paisans/f2a9/caddy.d'"}
	for _, c := range h.ran {
		known := false
		for _, p := range probes {
			known = known || strings.Contains(c, p)
		}
		if !known {
			t.Errorf("the gate ran something other than the inventory: %s", c)
		}
	}
}

// The host check is one step whose result is the host's class, with what it
// found marked under it.
func TestTheGateIsOneStepWhoseResultIsTheClass(t *testing.T) {
	rec := &ui.Recorder{Verbose_: true}
	if _, err := hostGate(rec, hostCheckFixture(t), "home-a", webHost()); err != nil {
		t.Fatal(err)
	}
	i := rec.Index("done", "host check")
	if i < 0 || rec.Events[i].Extra != "shared" {
		t.Fatalf("the host check did not end with its class:\n%s", rec.Lines())
	}
	// What it found is marked under the step's own line, with --verbose.
	if f := rec.Index("done", "docker"); f < i || rec.Events[f].Extra != "absent" {
		t.Errorf("Docker is not marked after the host check:\n%s", rec.Lines())
	}

	empty := webHost()
	empty.answers["ss -Hltnue"] = ""
	clean := &ui.Recorder{}
	if _, err := hostGate(clean, hostCheckFixture(t), "home-a", empty); err != nil {
		t.Fatal(err)
	}
	if i := clean.Index("done", "host check"); i < 0 || clean.Events[i].Extra != "clean" {
		t.Errorf("an empty host did not end clean:\n%s", clean.Lines())
	}
}

// A shared host is let through, and read as shared: its firewall is host
// admit's.
func TestTheGateLetsASharedHostThrough(t *testing.T) {
	report, err := hostGate(ui.Discard, hostCheckFixture(t), "home-a", webHost())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Shared() {
		t.Error("a host with another user's web server is not shared")
	}
}

// A command that changes several sites checks each before any changes,
// and learns which are shared.
func TestGateSitesChecksEachAndNamesTheShared(t *testing.T) {
	cfg := hostCheckFixture(t)
	hosts := map[string]*inventoryHost{"home-a": webHost(), "edge": webHost()}
	reach := func(site string) hostcheck.Transport { return hosts[site] }
	if _, err := gateSites(ui.Discard, cfg, []string{"home-a", "edge"}, reach); err == nil {
		t.Fatal("a conflict on edge was not refused")
	}
	shared, err := gateSites(ui.Discard, cfg, []string{"home-a"}, reach)
	if err != nil {
		t.Fatal(err)
	}
	if !shared["home-a"] {
		t.Errorf("shared %v", shared)
	}
}

// rotate-key's switch is an apply, and on a shared site it keeps images as
// apply does there.
func TestTheRotateKeySwitchKeepsImagesOnASharedSite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(freshSite), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	secrets := &config.Secrets{Version: 1}
	if _, err := secretsgen.Fill(cfg, secrets); err != nil {
		t.Fatal(err)
	}
	transports := map[string]apply.Transport{"home-a": emptyHost{}}
	for shared, want := range map[bool]bool{true: true, false: false} {
		s := applySwitch{cfg: cfg, app: "talk", transports: transports, shared: map[string]bool{"home-a": shared}}
		plan, err := s.plan("home-a", secrets)
		if err != nil {
			t.Fatal(err)
		}
		if plan.KeepImages != want {
			t.Errorf("shared %v: keep images %v", shared, plan.KeepImages)
		}
	}
}
