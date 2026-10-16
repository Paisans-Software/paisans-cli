package main

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// recordedHost serves files and keeps every command.
type recordedHost struct {
	files    map[string]string
	commands []string
	reads    []string
}

func (h *recordedHost) Run(c string) (string, error) {
	h.commands = append(h.commands, c)
	return "", nil
}
func (h *recordedHost) ReadFile(p string) (string, bool, error) {
	h.reads = append(h.reads, p)
	c, ok := h.files[p]
	return c, ok, nil
}
func (h *recordedHost) Describe() string { return "fake" }

func admittedPath(cfg *config.Config) string {
	p, _ := admission.Path(cfg.Deployment().Token())
	return p
}

// admittedRecord is the record admit writes for site with every grant its
// roles need.
func admittedRecord(t *testing.T, cfg *config.Config, site string) string {
	t.Helper()
	req, err := admission.Build(cfg, site)
	if err != nil {
		t.Fatal(err)
	}
	rec := admission.NewRecord(req, req.Grants, "", time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC))
	if cfg.Sites[site].WebServerManagedBy() == config.ManagedByExternal {
		rec.WebServer = &admission.WebServer{Server: "caddy", Process: "caddy"}
	}
	b, err := admission.EncodeRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// admittedFake is every site's host, for commands under test that reach a
// host through registryHost: each serves the admission record admit writes
// for its site in the fixture configuration, and keeps what it was asked.
type admittedFake struct {
	hosts map[string]*recordedHost
}

// checked reports whether any host's admission record was read.
func (f *admittedFake) checked() bool {
	for _, h := range f.hosts {
		if len(h.reads) > 0 {
			return true
		}
	}
	return false
}

// realAdmission makes the admission check read each host's record, which
// TestMain otherwise answers as admitted.
func realAdmission(t *testing.T) {
	t.Helper()
	saved := admissionForSite
	admissionForSite = admission.ForSite
	t.Cleanup(func() { admissionForSite = saved })
}

// admittedFixture is the fixture configuration's absolute path, taken while
// the tests run in the package directory: a test may change directory
// before it serves the records.
var admittedFixture, _ = filepath.Abs(fixtureConfig())

func withAdmittedFake(t *testing.T) *admittedFake {
	t.Helper()
	realAdmission(t)
	cfg, err := config.Load(admittedFixture)
	if err != nil {
		t.Fatal(err)
	}
	fake := &admittedFake{hosts: map[string]*recordedHost{}}
	saved := registryHost
	registryHost = func(name string, _ config.Site, _ string) registry.Runner {
		h := &recordedHost{files: map[string]string{admittedPath(cfg): admittedRecord(t, cfg, name)}}
		fake.hosts[name] = h
		return h
	}
	t.Cleanup(func() { registryHost = saved })
	return fake
}

// A host that has not admitted the deployment stops the command before
// anything changes, with the lines that admit it.
func TestAnUnadmittedHostIsRefused(t *testing.T) {
	realAdmission(t)
	cfg := fixture(t)
	h := &recordedHost{files: map[string]string{}}
	_, err := admittedHosts(&ui.Recorder{}, cfg, map[string]registry.Runner{"home-a": h})
	if err == nil {
		t.Fatal("an unadmitted host was accepted")
	}
	hint, explain := ui.Describe(err)
	for _, want := range []string{"home-a is not admitted", "paisans host request --site home-a > home-a.admit", "sudo paisans host admit --request home-a.admit"} {
		if !strings.Contains(hint+"\n"+explain, want) {
			t.Errorf("no %q in:\n%s\n%s", want, hint, explain)
		}
	}
	for _, c := range h.commands {
		if strings.Contains(c, "registry") {
			t.Errorf("the check reached the registry: %s", c)
		}
	}
}

// A record that covers the site lets the command through, and hands the
// caller the record.
func TestAnAdmittedHostIsAccepted(t *testing.T) {
	realAdmission(t)
	cfg := fixture(t)
	h := &recordedHost{files: map[string]string{admittedPath(cfg): admittedRecord(t, cfg, "home-a")}}
	recs, err := admittedHosts(&ui.Recorder{}, cfg, map[string]registry.Runner{"home-a": h})
	if err != nil || recs["home-a"].User != cfg.Sites["home-a"].SSHUser() {
		t.Fatalf("records %+v, %v", recs, err)
	}
}

// A record that no longer matches the site is refused with each difference
// and the same two lines.
func TestAMismatchedAdmissionIsRefused(t *testing.T) {
	realAdmission(t)
	cfg := fixture(t)
	h := &recordedHost{files: map[string]string{admittedPath(cfg): admittedRecord(t, cfg, "home-b")}}
	_, err := admittedHosts(&ui.Recorder{}, cfg, map[string]registry.Runner{"home-a": h})
	hint, explain := ui.Describe(err)
	for _, want := range []string{"home-a's admission does not match", "admitted as site home-b", "sudo paisans host admit --request home-a.admit"} {
		if !strings.Contains(hint+"\n"+explain, want) {
			t.Errorf("no %q in:\n%s\n%s", want, hint, explain)
		}
	}
}

// A mesh subnet changed in the configuration after admission is refused up
// front, naming both subnets and the two lines that fix it.
func TestAChangedMeshSubnetIsRefused(t *testing.T) {
	realAdmission(t)
	cfg := fixture(t)
	h := &recordedHost{files: map[string]string{admittedPath(cfg): admittedRecord(t, cfg, "home-a")}}
	admitted := cfg.Mesh.Subnet
	cfg.Mesh.Subnet = "10.77.33.0/24"
	_, err := admittedHosts(&ui.Recorder{}, cfg, map[string]registry.Runner{"home-a": h})
	hint, explain := ui.Describe(err)
	for _, want := range []string{"home-a's admission does not match", "the host admitted mesh subnet " + admitted + ", and mesh.subnet is 10.77.33.0/24", "paisans host request --site home-a > home-a.admit", "sudo paisans host admit --request home-a.admit"} {
		if !strings.Contains(hint+"\n"+explain, want) {
			t.Errorf("no %q in:\n%s\n%s", want, hint, explain)
		}
	}
}

// A record that cannot be read, such as one without mesh_subnet, is
// refused naming what is wrong and the fix: the owner moves it aside, and
// the operator and the owner run the two lines that admit the site again.
func TestAnUnreadableAdmissionIsRefusedWithTheFix(t *testing.T) {
	realAdmission(t)
	cfg := fixture(t)
	path := admittedPath(cfg)
	old := regexp.MustCompile(`(?m)^  "mesh_subnet": .*\n`).ReplaceAllString(admittedRecord(t, cfg, "home-a"), "")
	h := &recordedHost{files: map[string]string{path: old}}
	_, err := admittedHosts(&ui.Recorder{}, cfg, map[string]registry.Runner{"home-a": h})
	hint, explain := ui.Describe(err)
	for _, want := range []string{"home-a's admission record cannot be read", "mesh_subnet: missing", "move " + path + " aside", "paisans host request --site home-a > home-a.admit", "sudo paisans host admit --request home-a.admit"} {
		if !strings.Contains(hint+"\n"+explain, want) {
			t.Errorf("no %q in:\n%s\n%s", want, hint, explain)
		}
	}
}

// Each site's check is a step, so a spinner shows while ssh answers.
func TestAdmissionCheckIsAStep(t *testing.T) {
	realAdmission(t)
	cfg := fixture(t)
	host := func(site string) registry.Runner {
		return &recordedHost{files: map[string]string{admittedPath(cfg): admittedRecord(t, cfg, site)}}
	}
	rec := &ui.Recorder{}
	if _, err := admittedHosts(rec, cfg, map[string]registry.Runner{"vm": host("vm")}); err != nil {
		t.Fatal(err)
	}
	if !rec.Has("done", "check site's admission") {
		t.Errorf("one site's check is not a step:\n%s", rec.Lines())
	}
	rec = &ui.Recorder{}
	if _, err := admittedHosts(rec, cfg, map[string]registry.Runner{"vm": host("vm"), "home-a": host("home-a")}); err != nil {
		t.Fatal(err)
	}
	if !rec.Has("done", "check home-a's admission") || !rec.Has("done", "check vm's admission") {
		t.Errorf("each site's check is not a step of its own:\n%s", rec.Lines())
	}
}
