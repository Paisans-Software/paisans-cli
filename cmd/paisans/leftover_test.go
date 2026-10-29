package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/acme"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// apply's plan output ends with a "left over" section naming this
// deployment's stacks and files the site no longer renders, saying apply
// leaves them and naming `paisans app remove` for each app they belong to,
// and each container of a stack still rendered for a service its compose
// file no longer declares, naming the `docker rm -f` that takes it off. A whole plan shows what it is about to mark; a
// partial one shows the manifest's marks. Nothing left over prints nothing.
func TestTheApplyPlanListsLeftovers(t *testing.T) {
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
	rendered, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	id := cfg.Deployment().ID
	inv := &hostcheck.Inventory{
		Containers: []hostcheck.Container{
			{Name: "paisans-f2a9-talk-app-1", Deployment: id, Project: "paisans-f2a9-talk", PID: 1},
			{Name: "paisans-f2a9-docs-app-1", Deployment: id, Project: "paisans-f2a9-docs", PID: 2},
			{Name: "paisans-f2a9-talk-rabbitmq-1", Deployment: id, Project: "paisans-f2a9-talk", Service: "rabbitmq", PID: 3},
		},
		ManifestFiles: []render.ManifestFile{{Path: "srv/paisans/f2a9/docs/.env", Leftover: true, LeftoverSince: "2026-10-01T00:00:00Z"}},
	}

	whole, err := apply.Build("home-a", rendered, acme.Module(cfg.ACME.Provider), emptyHost{})
	if err != nil {
		t.Fatal(err)
	}
	rec := &ui.Recorder{Verbose_: true}
	if err := printLeftovers(rec, cfg, "home-a", inv, whole); err != nil {
		t.Fatal(err)
	}
	if !warned(rec, "left over: stack docs", "stack docs (compose project paisans-f2a9-docs, 1 of 1 running): paisans-f2a9-docs-app-1") ||
		!warned(rec, "`paisans app remove docs`", "") ||
		!rec.Has("detail", "left over: this deployment's, no longer rendered for home-a. apply leaves each one in place.") ||
		!rec.Has("detail", "`paisans app remove docs` takes docs's off every site") ||
		!warned(rec, "left over: service rabbitmq of stack talk (`docker rm -f paisans-f2a9-talk-rabbitmq-1`)", "service rabbitmq of stack talk (compose project paisans-f2a9-talk, 1 of 1 running): paisans-f2a9-talk-rabbitmq-1") ||
		!rec.Has("detail", "A left over service's containers keep running until removed") ||
		strings.Contains(rec.Lines(), "talk-app") || strings.Contains(rec.Lines(), "app remove talk") {
		t.Errorf("whole plan:\n%s", rec.Lines())
	}
	if strings.Contains(rec.Lines(), "docs/.env") {
		t.Errorf("a whole plan showed the old manifest's mark, not its own:\n%s", rec.Lines())
	}

	partial, err := apply.Build("home-a", rendered, acme.Module(cfg.ACME.Provider), emptyHost{}, apply.Only("talk"))
	if err != nil {
		t.Fatal(err)
	}
	rec = &ui.Recorder{Verbose_: true}
	if err := printLeftovers(rec, cfg, "home-a", inv, partial); err != nil {
		t.Fatal(err)
	}
	if !warned(rec, "left over: file /srv/paisans/f2a9/docs/.env", "file /srv/paisans/f2a9/docs/.env, left over since 2026-10-01T00:00:00Z") ||
		!warned(rec, "`paisans app remove docs`", "") {
		t.Errorf("partial plan:\n%s", rec.Lines())
	}

	rec = &ui.Recorder{Verbose_: true}
	if err := printLeftovers(rec, cfg, "home-a", &hostcheck.Inventory{}, whole); err != nil || len(rec.Events) != 0 {
		t.Errorf("nothing left over printed %q, %v", rec.Lines(), err)
	}
}

// warned reports whether rec holds a warning whose hint contains hint and
// whose detail contains detail.
func warned(rec *ui.Recorder, hint, detail string) bool {
	for _, e := range rec.Events {
		if e.Kind == "warn" && strings.Contains(e.Text, hint) && strings.Contains(e.Extra, detail) {
			return true
		}
	}
	return false
}

// heartbeatSite declares an uptime monitor pinned to home-b. Under the
// declaration every other site pushes a heartbeat, so home-a, which has no
// infrastructure role of its own, renders an infra stack; without home-b's
// monitor it renders none.
const heartbeatSite = `version: 1
id: f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01
community:
  name: Example
  domain: example.org
mesh:
  subnet: 10.44.0.0/24
acme:
  provider: desec
operators:
  alice:
    name: Alice Example
    email: alice@example.org
    keys: |
      ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice@example.org
sites:
  home-a:
    roles: [apps]
    address: 10.44.0.1
    ssh:
      host: home-a.local
      user: ubuntu
      keys: [alice]
  home-b:
    roles: [data, apps]
    address: 10.44.0.2
    ssh:
      host: home-b.local
      user: ubuntu
      keys: [alice]
cluster:
  sites: [home-b]
  port: 5000
  postgres_version: "18"
etcd:
  members: [home-b]
storage:
  garage:
    sites: [home-b]
    replication: 1
apps:
  uptime:
    blueprint: uptime
    hostname: health.example.org
    placement: { pinned: home-b }
`

func heartbeatConfig(t *testing.T) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(heartbeatSite), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func infraInventory(cfg *config.Config, pending ...string) *hostcheck.Inventory {
	return &hostcheck.Inventory{
		Containers:      []hostcheck.Container{{Name: "paisans-f2a9-infra-caddy-1", Deployment: cfg.Deployment().ID, Project: "paisans-f2a9-infra", PID: 3}},
		ManifestPending: pending,
	}
}

// heartbeatPlan is home-a's plan under cfg, built over a host with nothing
// on it.
func heartbeatPlan(t *testing.T, cfg *config.Config, options ...apply.Option) *apply.Plan {
	t.Helper()
	secrets := &config.Secrets{Version: 1}
	if _, err := secretsgen.Fill(cfg, secrets); err != nil {
		t.Fatal(err)
	}
	rendered, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := apply.Build("home-a", rendered, acme.Module(cfg.ACME.Provider), emptyHost{}, options...)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// A partial plan's leftover report is read against the view the manifest
// recorded: the declaration less the sites pending when this site's files
// were rendered. With the monitor pending, home-a rendered no infra stack, so
// the one running there is left over; the declaration alone would say it is
// rendered.
func TestLeftoversUseTheManifestsView(t *testing.T) {
	cfg := heartbeatConfig(t)
	plan := heartbeatPlan(t, cfg, apply.Only("infra"))
	if plan.Whole() {
		t.Fatal("the plan is whole")
	}

	rec := &ui.Recorder{Verbose_: true}
	if err := printLeftovers(rec, cfg, "home-a", infraInventory(cfg, "home-b"), plan); err != nil {
		t.Fatal(err)
	}
	if !warned(rec, "left over: stack infra", "stack infra") {
		t.Errorf("the infra stack, rendered nowhere in the manifest view, was not reported:\n%s", rec.Lines())
	}

	rec = &ui.Recorder{Verbose_: true}
	if err := printLeftovers(rec, cfg, "home-a", infraInventory(cfg), plan); err != nil || len(rec.Events) != 0 {
		t.Errorf("with nothing pending, infra is rendered and not left over: %q, %v", rec.Lines(), err)
	}
}

// A whole plan's report is read against this run's view, the pending list
// it records, not the old manifest's: it is about to write that manifest.
// With the monitor pending at the last render and ready now, this plan
// renders infra on home-a, so it is not left over; with the monitor pending
// now, it is, whatever the old manifest said.
func TestAWholePlansLeftoversUseThisRunsView(t *testing.T) {
	cfg := heartbeatConfig(t)
	rec := &ui.Recorder{Verbose_: true}
	if err := printLeftovers(rec, cfg, "home-a", infraInventory(cfg, "home-b"), heartbeatPlan(t, cfg)); err != nil || len(rec.Events) != 0 {
		t.Errorf("ready now, infra is rendered and not left over: %q, %v", rec.Lines(), err)
	}
	rec = &ui.Recorder{Verbose_: true}
	pending := heartbeatPlan(t, cfg.WithoutJoining("home-b"), apply.Pending("home-b"))
	if err := printLeftovers(rec, cfg, "home-a", infraInventory(cfg), pending); err != nil {
		t.Fatal(err)
	}
	if !warned(rec, "left over: stack infra", "stack infra") {
		t.Errorf("pending now, infra is not rendered and is left over:\n%s", rec.Lines())
	}
}

// A manifest naming its own site as pending does not fail the report: the
// site is classified against the view without the other sites it names.
func TestLeftoversIgnoreASiteNamedPendingInItsOwnManifest(t *testing.T) {
	cfg := heartbeatConfig(t)
	rec := &ui.Recorder{Verbose_: true}
	err := printLeftovers(rec, cfg, "home-a", infraInventory(cfg, "home-a", "home-b"), heartbeatPlan(t, cfg, apply.Only("infra")))
	if err != nil {
		t.Fatal(err)
	}
	if !warned(rec, "left over: stack infra", "stack infra") {
		t.Errorf("the other pending site was dropped with the site itself:\n%s", rec.Lines())
	}
}

// leftoverHost answers the host check's probes for a host running one
// container of this deployment, and holds the manifest.
type leftoverHost struct {
	container string
	manifest  string
}

func (d leftoverHost) Run(command string) (string, error) {
	switch {
	case strings.Contains(command, "id -u"):
		return "uid 1000\n", nil
	case strings.Contains(command, "docker ps"):
		return d.container + "\n", nil
	case strings.Contains(command, "docker version"):
		return "27.3.1 27.3.1\ncompose v2.29.0\n", nil
	}
	return "", nil
}

func (d leftoverHost) RunInput(command, _ string) (string, error) { return d.Run(command) }

func (d leftoverHost) ReadFile(path string) (string, bool, error) {
	if path == "/srv/paisans/f2a9/.paisans-manifest.json" {
		return d.manifest, true, nil
	}
	return "", false, nil
}

func (leftoverHost) WriteFile(string, string, uint32) error { return nil }
func (leftoverHost) Describe() string                       { return "fake" }

// Doctor's Leftovers reads the manifest from the host and reports against its
// view.
func TestDoctorLeftoversUseTheManifestsView(t *testing.T) {
	cfg := heartbeatConfig(t)
	container := `{"id":"` + strings.Repeat("e", 64) + `","name":"/paisans-f2a9-infra-caddy-1","image":"sha256:x","pid":3,"labels":{"com.docker.compose.project":"paisans-f2a9-infra","` + deployment.Label + `":"` + cfg.Deployment().ID + `"},"networks":{},"ports":{}}`

	p := gatherLeftovers(cfg, "home-a", leftoverHost{container, `{"version":1,"files":[],"pending":["home-b"]}`})
	if p.Err != "" || len(p.Report.Stacks) != 1 || p.Report.Stacks[0].Name != "infra" {
		t.Errorf("pending home-b: %+v", p)
	}
	p = gatherLeftovers(cfg, "home-a", leftoverHost{container, `{"version":1,"files":[]}`})
	if p.Err != "" || len(p.Report.Stacks) != 0 {
		t.Errorf("nothing pending: %+v", p)
	}
}
