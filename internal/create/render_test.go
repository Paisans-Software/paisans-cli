package create

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/secretsgen"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

const testID = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"

// testOrigin names a fixed binary and date, so the golden files do not change
// with the build or the day.
var testOrigin = Origin{Version: "v0.0.0-test", Date: "2026-01-02"}

// answersFor is what the wizard collects for shape, with every capability
// when all is set and none otherwise, every gateable app members only when
// gated is.
func answersFor(sh Shape, all, gated bool) Answers {
	a := Answers{
		Name:     "Example Community",
		Domain:   "example.org",
		Provider: "cloudflare",
		Admins: []Admin{
			{ID: "alice", Name: "Alice Example", Email: "alice@example.org", Keys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice@example.org"}},
			{ID: "bob", Name: "Bob Example", Email: "bob@example.org", Keys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB bob@example.org"}},
		},
		Shape: sh,
		SMTP: SMTP{Host: "smtp.example.org", Port: 587, Security: "starttls", Username: "community@example.org",
			FromAddress: "community@example.org", FromName: "Example Community"},
	}
	for i, s := range shapes[sh].sites {
		site := Site{Name: s.name, Roles: s.roles, SSHUser: "ubuntu"}
		if s.public {
			site.PublicAddress = fmt.Sprintf("203.0.113.%d", 10+i)
		} else {
			site.SSHHost = s.name + ".local"
		}
		if s.endpoint {
			site.Endpoint = s.name + ".example.org:51820"
		}
		a.Sites = append(a.Sites, site)
	}
	a.Apps = []App{{Key: signIn.key, Kind: signIn.kind, Hostname: "id.example.org"}}
	if !all {
		return a
	}
	for _, c := range capabilities {
		if c.twoSites && len(a.Sites) < 2 {
			continue
		}
		for _, s := range c.apps {
			a.Apps = append(a.Apps, App{Key: s.key, Kind: s.kind, Hostname: s.sub + ".example.org", MembersOnly: gated && s.gateable})
		}
	}
	a.Monitor = &Site{Name: monitorSite, Roles: []config.Role{config.RoleMonitor}, PublicAddress: "203.0.113.50", SSHUser: "ubuntu"}
	return a
}

func check(t *testing.T, data []byte) validate.Result {
	t.Helper()
	cfg, err := config.LoadBytes(data)
	if err != nil {
		t.Fatalf("the rendered file does not load: %v\n%s", err, data)
	}
	return validate.Check(cfg)
}

func rules(fs []validate.Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Rule)
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// TestNamedShapesValidate is what holds the claim that a named shape always
// validates: every one, with no apps and with every app, gated and not.
//
// The warnings each leaves are the known ones: the wizard declares no
// backups, with no health page there is no uptime monitor, and the high availability shape's Garage needs both
// homes for a write and pins the chat server to the witness, as the example
// does.
func TestNamedShapesValidate(t *testing.T) {
	noMonitor := []string{"backups-off", "no-uptime-monitor"}
	off := []string{"backups-off"}
	for _, c := range []struct {
		shape     Shape
		all       bool
		warn      []string
		onGateway bool
	}{
		{ShapeOneMachine, false, noMonitor, false},
		{ShapeOneMachine, true, off, false},
		{ShapeHomeAndVPS, false, noMonitor, false},
		{ShapeHomeAndVPS, true, off, false},
		{ShapeHighAvailability, false, noMonitor, false},
		{ShapeHighAvailability, true, []string{"backups-off", "garage-two-sites-stop-uploads", "pinned-app-on-witness"}, false},
		{ShapeOneMachine, true, []string{"backups-off", "monitor-shares-a-site"}, true},
		{ShapeHomeAndVPS, true, []string{"backups-off", "monitor-shares-a-site"}, true},
		{ShapeHighAvailability, true, []string{"backups-off", "garage-two-sites-stop-uploads", "monitor-shares-a-site", "pinned-app-on-witness"}, true},
	} {
		for _, gated := range []bool{false, true} {
			if !c.all && gated {
				continue
			}
			name := fmt.Sprintf("%s/all=%v/gated=%v/onGateway=%v", shapes[c.shape].label, c.all, gated, c.onGateway)
			t.Run(name, func(t *testing.T) {
				a := answersFor(c.shape, c.all, gated)
				if c.onGateway {
					a.Monitor, a.MonitorOnGateway = nil, true
				}
				data, err := Render(a, testID, testOrigin)
				if err != nil {
					t.Fatal(err)
				}
				result := check(t, data)
				for _, f := range result.Refusals() {
					t.Errorf("refused %s: %s: %s", f.Rule, f.Key, f.Message)
				}
				if got := rules(result.Warnings()); !slices.Equal(got, c.warn) {
					t.Errorf("warnings %v, want %v", got, c.warn)
				}
				if !t.Failed() {
					renders(t, data)
				}
				if t.Failed() {
					t.Logf("\n%s", data)
				}
			})
		}
	}
}

// renders builds every site's files from data with generated secrets and a
// DNS token, so a refusal only render makes is caught too.
func renders(t *testing.T, data []byte) {
	t.Helper()
	cfg, err := config.LoadBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	secrets := &config.Secrets{}
	if _, err := secretsgen.Fill(cfg, secrets); err != nil {
		t.Fatalf("generating secrets: %v", err)
	}
	if _, err := render.Build(cfg, secrets); err != nil {
		t.Errorf("render refuses it: %v", err)
	}
}

func TestQuotedAnswersReadBackUnchanged(t *testing.T) {
	a := answersFor(ShapeOneMachine, false, false)
	a.Name = `- "Paisans": a #community`
	a.Admins[0].Name = `Zoë "Z" O'Brien: #1`
	a.SMTP.FromName = "yes"
	data, err := Render(a, testID, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadBytes(data)
	if err != nil {
		t.Fatalf("does not load: %v\n%s", err, data)
	}
	if cfg.Community.Name != a.Name || cfg.Admins["alice"].Name != a.Admins[0].Name || cfg.SMTP.FromName != "yes" {
		t.Errorf("read back %q, %q, %q", cfg.Community.Name, cfg.Admins["alice"].Name, cfg.SMTP.FromName)
	}
	if !strings.Contains(string(data), "id: "+testID) {
		t.Errorf("the id is not written")
	}
}

// One machine with the health page beside its gateway renders the file in
// testdata, which validates with the shared-site warning.
func TestOneMachineWithTheMonitorOnTheGatewayRendersTheGoldenFile(t *testing.T) {
	a := answersFor(ShapeOneMachine, true, false)
	a.Monitor, a.MonitorOnGateway = nil, true
	data, err := Render(a, testID, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "one-machine-monitor-on-gateway.yaml")
	if *update {
		if err := os.WriteFile(golden, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if string(want) != string(data) {
		t.Errorf("%s differs from what the answers render; run with -update and read the diff", golden)
	}
	if got := rules(check(t, data).Warnings()); !slices.Contains(got, "monitor-shares-a-site") {
		t.Errorf("warnings %v", got)
	}
	if strings.Contains(string(data), "on a machine of its own") || !strings.Contains(string(data), "beside the gateway") {
		t.Errorf("the health page's comment does not say where it runs:\n%s", data)
	}
}
