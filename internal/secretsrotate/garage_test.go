package secretsrotate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// garageWorld is render's fixture deployment, rendered and applied, with a
// Garage node on each of storage.garage.sites. Each node runs with the
// rpc_secret and admin_token its garage.toml had when it last started, and a
// logical clock orders file writes and container starts the way the host's
// own clock does. Every command run and every event is recorded.
type garageWorld struct {
	t     *testing.T
	cfg   *config.Config
	store *memStore
	hosts map[string]*garageHost
	clock int64
	log   []string
	// restartFails names a site whose restart fails; down a site whose node
	// does not come back after a restart; unreadable a site whose node's
	// state cannot be read.
	restartFails, down, unreadable string
}

type garageHost struct {
	w     *garageWorld
	site  string
	files map[string]string
	cmds  []string
	// The node: whether it runs, when it last started, the garage.toml
	// values it runs with, and when garage.toml was last written.
	up              bool
	started, mtime  int64
	rpc, adminToken string
	id              string
}

func newGarageWorld(t *testing.T, garageSites ...string) *garageWorld {
	t.Helper()
	oldSleep, oldWait, oldPoll := sleep, garageWait, garagePoll
	sleep, garageWait, garagePoll = func(time.Duration) {}, 3*time.Second, time.Second
	t.Cleanup(func() { sleep, garageWait, garagePoll = oldSleep, oldWait, oldPoll })

	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets(filepath.Join("..", "render", "testdata", "secrets.fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(garageSites) > 0 {
		cfg.Storage.Garage.Sites = garageSites
		cfg.Storage.Garage.Replication = min(cfg.Storage.Garage.Replication, len(garageSites))
	}
	rendered, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	w := &garageWorld{t: t, cfg: cfg, store: &memStore{s: secrets}, hosts: map[string]*garageHost{}}
	for _, name := range cfg.SiteNames() {
		sum := sha256.Sum256([]byte(name))
		w.hosts[name] = &garageHost{w: w, site: name, files: map[string]string{}, id: hex.EncodeToString(sum[:])}
	}
	var manifests = map[string]*render.Manifest{}
	for _, f := range rendered.Files {
		site, rel, _ := strings.Cut(f.Path, "/")
		if rel == render.ManifestName {
			continue
		}
		w.hosts[site].files["/"+rel] = f.Content
		m := manifests[site]
		if m == nil {
			m = &render.Manifest{Version: 1}
			manifests[site] = m
		}
		m.Files = append(m.Files, render.ManifestFile{Path: rel, SHA256: sum(f.Content), Mode: fmt.Sprintf("%04o", f.Mode)})
	}
	for site, m := range manifests {
		data, _ := json.Marshal(m)
		w.hosts[site].files[cfg.Deployment().Manifest()] = string(data)
	}
	for _, site := range cfg.Storage.Garage.Sites {
		h := w.hosts[site]
		h.mtime = w.tick()
		h.start()
	}
	return w
}

func (w *garageWorld) tick() int64 { w.clock += 1000; return w.clock }

func (w *garageWorld) toml() string { return "/" + apply.GarageConfig(w.cfg.Deployment()) }

var tomlValue = map[string]*regexp.Regexp{
	"rpc_secret":  regexp.MustCompile(`(?m)^rpc_secret = "(.*)"$`),
	"admin_token": regexp.MustCompile(`(?m)^admin_token = "(.*)"$`),
}

func (h *garageHost) value(key string) string {
	m := tomlValue[key].FindStringSubmatch(h.files[h.w.toml()])
	if m == nil {
		return ""
	}
	return m[1]
}

func (h *garageHost) start() {
	h.up, h.started = true, h.w.tick()
	h.rpc, h.adminToken = h.value("rpc_secret"), h.value("admin_token")
}

func (h *garageHost) Describe() string { return h.site }

func (h *garageHost) ReadFile(path string) (string, bool, error) {
	c, ok := h.files[path]
	return c, ok, nil
}

func (h *garageHost) WriteFile(path, content string, mode uint32) error {
	h.files[path] = content
	if path == h.w.toml() {
		h.mtime = h.w.tick()
		h.w.log = append(h.w.log, "write "+h.site)
	}
	return nil
}

func (h *garageHost) RunInput(command, stdin string) (string, error) {
	h.w.t.Errorf("unexpected command with stdin on %s: %s", h.site, command)
	return "", errors.New("unexpected")
}

func (h *garageHost) Run(command string) (string, error) {
	w := h.w
	h.cmds = append(h.cmds, command)
	compose := w.cfg.Deployment().ComposeCmd("infra")
	switch {
	case strings.Contains(command, "ps -aq garage"):
		if w.unreadable == h.site {
			return "Cannot connect to the Docker daemon", errors.New("exit status 1")
		}
		if h.id == "" || (!h.up && h.started == 0) {
			return "absent\n", nil
		}
		key := "rpc_secret"
		if strings.Contains(command, "admin_token") {
			key = "admin_token"
		}
		return fmt.Sprintf("%s %t %d %d\n", sum(h.value(key)), h.up, h.started, h.mtime), nil
	case command == compose+" restart garage":
		w.log = append(w.log, "restart "+h.site)
		if w.restartFails == h.site {
			return "Error response from daemon", errors.New("exit status 1")
		}
		h.start()
		if w.down == h.site {
			h.up = false
		}
		return "", nil
	case command == compose+" exec -T garage /garage node id -q":
		if !h.up {
			return "Error: could not connect", errors.New("exit status 1")
		}
		return h.id + "@" + w.cfg.Sites[h.site].Address + ":3901\n", nil
	case command == compose+" exec -T garage /garage status":
		if !h.up {
			return "Error: could not connect", errors.New("exit status 1")
		}
		w.log = append(w.log, "status "+h.site)
		var b strings.Builder
		b.WriteString("==== HEALTHY NODES ====\nID                Hostname  Address  Tags  Zone  Capacity  DataAvail\n")
		for _, site := range w.cfg.Storage.Garage.Sites {
			o := w.hosts[site]
			if o.up && o.rpc == h.rpc {
				fmt.Fprintf(&b, "%s  %s  %s:3901  []  %s  100.0 GB  1 TB\n", o.id[:16], site, w.cfg.Sites[site].Address, site)
			}
		}
		return b.String(), nil
	}
	w.t.Errorf("unexpected command on %s: %s", h.site, command)
	return "", errors.New("unexpected")
}

func (w *garageWorld) env() Env {
	sites := map[string]apply.Transport{}
	for name, h := range w.hosts {
		sites[name] = h
	}
	return Env{
		Cfg:   w.cfg,
		Store: w.store,
		Sites: sites,
		Apply: func(site string, s *config.Secrets, stacks []string) error {
			w.t.Errorf("a Garage rotation ran a whole scoped apply on %s", site)
			return nil
		},
		Healthy: func(site string, stacks []string) error {
			w.log = append(w.log, "healthy "+site+" "+strings.Join(stacks, ","))
			return nil
		},
		Now: time.Now,
	}
}

func (w *garageWorld) rotate(r Rotator) error {
	w.t.Helper()
	e := w.env()
	run, err := Plan(e, []Rotator{r})
	if err != nil {
		return err
	}
	return run.Execute(e, nil)
}

// noSecretOnArgv fails when a value appears in any command string.
func (w *garageWorld) noSecretOnArgv(values ...string) {
	w.t.Helper()
	for _, h := range w.hosts {
		for _, c := range h.cmds {
			for _, v := range values {
				if v != "" && strings.Contains(c, v) {
					w.t.Errorf("a secret is on a command line on %s: %s", h.site, c)
				}
			}
		}
	}
}

// before reports whether every entry in log starting with a comes before
// every entry starting with b.
func before(log []string, a, b string) bool {
	lastA, firstB := -1, len(log)
	for i, l := range log {
		if strings.HasPrefix(l, a) {
			lastA = i
		}
		if strings.HasPrefix(l, b) && i < firstB {
			firstB = i
		}
	}
	return lastA < firstB
}

func TestGarageRPCRestartsEveryNodeTogether(t *testing.T) {
	w := newGarageWorld(t)
	old := w.store.s.Storage.Garage.RPCSecret
	r := GarageRPC()
	if r.Path() != "storage.garage.rpc_secret" {
		t.Fatalf("path %q", r.Path())
	}
	if got := r.Cost(); got != (Cost{Downtime: "storage, while every Garage node restarts together"}) {
		t.Fatalf("cost %+v", got)
	}
	uses := r.Users(w.env())
	if len(uses) != 1 || uses[0].Do == nil {
		t.Fatalf("users %+v, want a single step that does the whole of it", uses)
	}
	if err := w.rotate(r); err != nil {
		t.Fatal(err)
	}
	next := w.store.s.Storage.Garage.RPCSecret
	if next == old || len(next) != 64 {
		t.Fatalf("the file holds %q, was %q", next, old)
	}
	for _, site := range []string{"home-a", "home-b"} {
		h := w.hosts[site]
		if h.rpc != next || h.value("rpc_secret") != next {
			t.Errorf("%s runs with %q and its garage.toml says %q, want the next value", site, h.rpc, h.value("rpc_secret"))
		}
	}
	for _, want := range []string{"write home-a", "write home-b", "restart home-a", "restart home-b", "status home-a", "status home-b"} {
		if !slices.Contains(w.log, want) {
			t.Fatalf("log %v has no %q", w.log, want)
		}
	}
	if !before(w.log, "write ", "restart ") || !before(w.log, "restart ", "status ") || !before(w.log, "status ", "healthy ") {
		t.Fatalf("log %v: want every garage.toml written, then every node restarted, then every node's status, then the health gate", w.log)
	}
	for _, site := range []string{"vm", "watch"} {
		if len(w.hosts[site].cmds) != 0 {
			t.Errorf("%s runs no Garage and was sent %v", site, w.hosts[site].cmds)
		}
	}
	if len(w.store.s.Rotations) != 0 {
		t.Fatalf("rotations left: %v", w.store.s.Rotations)
	}
	w.noSecretOnArgv(old, next)
}

// Review Focus 4: a deployment with one Garage node still restarts that
// node, and the plan says storage is briefly down.
func TestGarageRPCOneNodeStillRestarts(t *testing.T) {
	w := newGarageWorld(t, "home-a")
	e := w.env()
	run, err := Plan(e, []Rotator{GarageRPC()})
	if err != nil {
		t.Fatal(err)
	}
	rec := &ui.Recorder{Verbose_: true}
	run.Show(rec)
	if !strings.Contains(rec.Lines(), "briefly unavailable: storage, while every Garage node restarts together") {
		t.Fatalf("the plan does not say storage is down:\n%s", rec.Lines())
	}
	if err := run.Execute(e, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(w.log, "restart home-a") {
		t.Fatalf("log %v: the one node was not restarted", w.log)
	}
	if w.hosts["home-a"].rpc != w.store.s.Storage.Garage.RPCSecret {
		t.Fatal("the one node does not run with the promoted value")
	}
}

// A resumed run re-runs the step; where every node already runs with the
// next value it writes nothing and restarts nothing, and only waits.
func TestGarageRPCRerunOnlyWaits(t *testing.T) {
	w := newGarageWorld(t)
	e := w.env()
	r := GarageRPC()
	s := roundTrip(w.store.s)
	if err := s.Set(r.Path(), strings.Repeat("ab", 32)); err != nil {
		t.Fatal(err)
	}
	do := r.Users(e)[0].Do
	if err := do(e, s); err != nil {
		t.Fatal(err)
	}
	w.log = nil
	if err := do(e, s); err != nil {
		t.Fatal(err)
	}
	for _, l := range w.log {
		if !strings.HasPrefix(l, "status ") {
			t.Fatalf("log %v: a re-run did more than wait", w.log)
		}
	}
	if len(w.log) == 0 {
		t.Fatal("a re-run did not wait for every node")
	}
}

// A run stopped after one node was restarted restarts every node, since a
// node on the old value cannot talk to one on the next.
func TestGarageRPCRestartsAllWhenOneLags(t *testing.T) {
	w := newGarageWorld(t)
	w.restartFails = "home-b"
	e := w.env()
	r := GarageRPC()
	s := roundTrip(w.store.s)
	if err := s.Set(r.Path(), strings.Repeat("cd", 32)); err != nil {
		t.Fatal(err)
	}
	do := r.Users(e)[0].Do
	err := do(e, s)
	if err == nil || !strings.Contains(err.Error(), "home-b") {
		t.Fatalf("err %v, want home-b's restart named", err)
	}
	if !slices.Contains(w.log, "restart home-a") {
		t.Fatalf("log %v: home-b's failure stopped home-a's restart", w.log)
	}
	if slices.ContainsFunc(w.log, func(l string) bool { return strings.HasPrefix(l, "status ") }) {
		t.Fatalf("log %v: waited for health with a node restart failed", w.log)
	}
	w.restartFails, w.log = "", nil
	if err := do(e, s); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(w.log, "restart home-a") || !slices.Contains(w.log, "restart home-b") {
		t.Fatalf("log %v: want every node restarted together", w.log)
	}
}

func TestGarageRPCWaitIsBounded(t *testing.T) {
	w := newGarageWorld(t)
	w.down = "home-b"
	err := w.rotate(GarageRPC())
	if err == nil || !strings.Contains(err.Error(), "home-b") || !strings.Contains(err.Error(), "within") {
		t.Fatalf("err %v, want a timeout naming home-b", err)
	}
	if len(w.store.s.Rotations) != 1 {
		t.Fatalf("rotations %v, want the next value kept for the next run", w.store.s.Rotations)
	}
}

func TestGarageRPCRefusesAnEditedFile(t *testing.T) {
	w := newGarageWorld(t)
	w.hosts["home-b"].files[w.toml()] += "\n# edited by hand\n"
	err := w.rotate(GarageRPC())
	if err == nil || !strings.Contains(err.Error(), "home-b") {
		t.Fatalf("err %v, want home-b's edited garage.toml refused", err)
	}
	for _, l := range w.log {
		if strings.HasPrefix(l, "write ") || strings.HasPrefix(l, "restart ") {
			t.Fatalf("log %v: something changed before the refusal", w.log)
		}
	}
}

func TestGarageAdminTokenOneNodeAtATime(t *testing.T) {
	w := newGarageWorld(t)
	old := w.store.s.Storage.Garage.AdminToken
	r := GarageAdminToken()
	if r.Path() != "storage.garage.admin_token" {
		t.Fatalf("path %q", r.Path())
	}
	if r.Cost() != (Cost{}) {
		t.Fatalf("cost %+v, want none", r.Cost())
	}
	if got := usesOf(w.env(), r); !slices.Equal(got, []string{"home-a:infra", "home-b:infra"}) {
		t.Fatalf("users %v", got)
	}
	if err := w.rotate(r); err != nil {
		t.Fatal(err)
	}
	next := w.store.s.Storage.Garage.AdminToken
	if next == old || next == "" {
		t.Fatalf("the file holds %q", next)
	}
	want := []string{"write home-a", "restart home-a", "status home-a", "status home-b", "healthy home-a infra",
		"write home-b", "restart home-b", "status home-a", "status home-b", "healthy home-b infra"}
	if !slices.Equal(w.log, want) {
		t.Fatalf("log\n%v\nwant\n%v", w.log, want)
	}
	for _, site := range []string{"home-a", "home-b"} {
		if w.hosts[site].adminToken != next {
			t.Errorf("%s runs with the old admin token", site)
		}
	}
	w.noSecretOnArgv(old, next)
}

func TestGarageAdminTokenRerunRestartsNothing(t *testing.T) {
	w := newGarageWorld(t)
	e := w.env()
	r := GarageAdminToken()
	s := roundTrip(w.store.s)
	if err := s.Set(r.Path(), "next-admin-token"); err != nil {
		t.Fatal(err)
	}
	do := r.Users(e)[0].Do
	if err := do(e, s); err != nil {
		t.Fatal(err)
	}
	w.log = nil
	if err := do(e, s); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(w.log, func(l string) bool { return !strings.HasPrefix(l, "status ") }) {
		t.Fatalf("log %v: a re-run did more than wait", w.log)
	}
}

func TestGarageRotationsRefuseWithoutGarage(t *testing.T) {
	for _, r := range []Rotator{GarageRPC(), GarageAdminToken()} {
		w := newGarageWorld(t)
		w.cfg.Storage.Garage.Sites = nil
		err := w.rotate(r)
		if err == nil || !strings.Contains(err.Error(), "storage.garage.sites") {
			t.Errorf("%s: err %v, want a refusal naming storage.garage.sites", r.Path(), err)
		}
	}
}

// Every node's state is read before any garage.toml is written, so a node
// that cannot be read stops the rotation with no file anywhere changed.
func TestGarageRPCReadsEveryNodeBeforeWriting(t *testing.T) {
	w := newGarageWorld(t)
	w.unreadable = w.cfg.Storage.Garage.Sites[len(w.cfg.Storage.Garage.Sites)-1]
	if err := w.rotate(GarageRPC()); err == nil {
		t.Fatal("an unreadable node passed")
	}
	for _, entry := range w.log {
		if strings.HasPrefix(entry, "write ") || strings.HasPrefix(entry, "restart ") {
			t.Fatalf("changed a node before every node was read: %v", w.log)
		}
	}
}
