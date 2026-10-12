package gatewaymove_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployrecord"
	"github.com/paisans-software/paisans-stack/internal/dns"
	"github.com/paisans-software/paisans-stack/internal/dns/dnstest"
	"github.com/paisans-software/paisans-stack/internal/gatewaymove"
	"github.com/paisans-software/paisans-stack/internal/registry"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

const (
	comment = "paisans-f2a9: created by paisans dns init"
	oldIP   = "198.51.100.1"
	newIP   = "203.0.113.10"
)

// host is one site's machine: its files, this deployment's gateway Caddy
// containers, and the host owner's site files in /srv/caddy.d.
type host struct {
	name   string
	files  map[string]string
	caddy  []string
	sites  []string
	ran    []string
	failOn string
}

func (h *host) Describe() string { return h.name }

func (h *host) Run(command string) (string, error) {
	h.ran = append(h.ran, command)
	if h.failOn != "" && strings.Contains(command, h.failOn) {
		return "ssh: connect to host " + h.name + ": timed out", errors.New("exit status 255")
	}
	switch {
	case strings.Contains(command, "docker ps -a"):
		var b strings.Builder
		for _, id := range h.caddy {
			fmt.Fprintf(&b, "caddy %s running\n", id)
		}
		for _, s := range h.sites {
			fmt.Fprintf(&b, "site %s\n", s)
		}
		return b.String(), nil
	case strings.Contains(command, "/.deployed."):
		path := regexp.MustCompile(`f='([^']+)'`).FindStringSubmatch(command)[1]
		data, _ := base64.StdEncoding.DecodeString(regexp.MustCompile(`printf %s '([^']*)' \| base64 -d`).FindStringSubmatch(command)[1])
		h.files[path] = string(data)
		return "", nil
	case strings.HasPrefix(command, "docker stop"):
		id := strings.Fields(command)[4]
		h.caddy = slices.DeleteFunc(h.caddy, func(c string) bool { return c == id })
		return "", nil
	case strings.HasPrefix(command, "rm -f -- "):
		for _, q := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(command, -1) {
			delete(h.files, q[1])
		}
		return "", nil
	}
	return "", fmt.Errorf("unexpected command on %s: %s", h.name, command)
}

func (h *host) ReadFile(path string) (string, bool, error) {
	c, ok := h.files[path]
	return c, ok, nil
}

func (h *host) WriteFile(path, content string, _ uint32) error {
	h.files[path] = content
	return nil
}

func (h *host) RunInput(command, _ string) (string, error) { return h.Run(command) }

// world is a deployment whose gateway role has just moved from home-a to
// vm in paisans-stack.yaml, with vm applied: home-a still runs the old Caddy and
// holds the deployment record, and the zone still points every hostname
// the gateway serves at home-a.
type world struct {
	t       *testing.T
	cfg     *config.Config
	secrets *config.Secrets
	hosts   map[string]*host
	zone    *dnstest.Provider
	now     time.Time
	slept   time.Duration
	// down are the hostnames the new gateway does not answer for.
	down map[string]bool
	// downAfterWait makes every check fail once the wait has slept.
	downAfterWait bool
	gets          int
	// old are the ids of the records the move may delete; kept those it
	// must not.
	old, kept []string
}

func newWorld(t *testing.T, edits ...func(string) string) *world {
	t.Helper()
	data, err := os.ReadFile("../render/testdata/deployment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	text = strings.Replace(text, "  provider: desec", "  provider: cloudflare", 1)
	text = strings.Replace(text, "    endpoint: vm.example.org:51820\n", "    endpoint: vm.example.org:51820\n    public_address: "+newIP+"\n", 1)
	text = strings.Replace(text, "    address: 10.44.0.1\n", "    address: 10.44.0.1\n    public_address: "+oldIP+"\n", 1)
	for _, e := range edits {
		text = e(text)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets("../render/testdata/secrets.fixture.yaml")
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, cfg: cfg, secrets: secrets, hosts: map[string]*host{}, zone: dnstest.New(),
		now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), down: map[string]bool{}}
	for _, name := range cfg.SiteNames() {
		w.hosts[name] = &host{name: name, files: map[string]string{}}
	}
	d := cfg.Deployment()
	w.hosts["home-a"].caddy = []string{"c0ffee"}
	w.hosts["home-a"].files[deployrecord.Path(d)] = deployrecord.Encode(deployrecord.Record{Sites: cfg.SiteNames(), Apps: append(cfg.AppNames(), "retired")})
	w.hosts["vm"].caddy = []string{"beef"}
	w.hosts["vm"].files[deployrecord.Path(d)] = deployrecord.Encode(deployrecord.FromConfig(cfg))

	w.zone.Zones["example.org"] = "zone-1"
	add := func(r dns.Record) { w.zone.InZone["zone-1"] = append(w.zone.InZone["zone-1"], r) }
	for i, h := range dns.GatewayHostnames(cfg) {
		id := fmt.Sprintf("old-%d", i)
		add(dns.Record{ID: id, Type: "A", Name: h.Name, Content: oldIP, Comment: comment, TTL: 300})
		w.old = append(w.old, id)
	}
	// Records the move must leave alone, one reason each.
	add(dns.Record{ID: "keep-status", Type: "A", Name: "status.example.org", Content: "203.0.113.20", Comment: comment})
	add(dns.Record{ID: "keep-endpoint", Type: "A", Name: "vm.example.org", Content: newIP, Comment: comment})
	add(dns.Record{ID: "keep-gone", Type: "A", Name: "gone.example.org", Content: oldIP, Comment: comment})
	add(dns.Record{ID: "keep-other", Type: "A", Name: "other.example.org", Content: oldIP, Comment: "paisans-0c1d: created by paisans dns init"})
	return w
}

func (w *world) transports() map[string]apply.Transport {
	out := map[string]apply.Transport{}
	for name, h := range w.hosts {
		out[name] = h
	}
	return out
}

func (w *world) options() gatewaymove.Options {
	return gatewaymove.Options{
		DNSProvider: w.zone.For("cloudflare"),
		Probes: gatewaymove.Probes{
			Get: func(_ context.Context, address, hostname, _ string) (int, error) {
				w.gets++
				switch {
				case address != newIP:
					return 0, fmt.Errorf("asked %s, not the new gateway", address)
				case w.down[hostname], w.downAfterWait && w.slept > 0:
					return 0, errors.New("tls: failed to verify certificate")
				}
				return 200, nil
			},
			Now:   func() time.Time { return w.now },
			Sleep: func(d time.Duration) { w.slept += d; w.now = w.now.Add(d) },
		},
	}
}

func (w *world) build() (*gatewaymove.Plan, error) {
	return gatewaymove.Build(w.cfg, w.secrets, w.transports(), w.options())
}

func (w *world) mustBuild() *gatewaymove.Plan {
	w.t.Helper()
	p, err := w.build()
	if err != nil {
		w.t.Fatal(err)
	}
	return p
}

// run builds and executes the move, as one `gateway move --execute`.
func (w *world) run() error {
	p, err := w.build()
	if err != nil {
		return err
	}
	p.Report = &ui.Recorder{}
	return gatewaymove.Execute(p)
}

// at is the addresses the zone holds for name, sorted.
func (w *world) at(name string) []string {
	var out []string
	for _, r := range w.zone.InZone["zone-1"] {
		if r.Name == name && (r.Type == "A" || r.Type == "AAAA") {
			out = append(out, r.Content)
		}
	}
	sort.Strings(out)
	return out
}

func (w *world) moved(t *testing.T) {
	t.Helper()
	a := w.hosts["home-a"]
	if len(a.caddy) != 0 {
		t.Errorf("home-a's Caddy still runs: %v", a.caddy)
	}
	if len(a.files) != 0 {
		t.Errorf("home-a still holds %v", keys(a.files))
	}
	for _, h := range dns.GatewayHostnames(w.cfg) {
		if got := w.at(h.Name); !slices.Equal(got, []string{newIP}) {
			t.Errorf("%s points at %v, want only %s", h.Name, got, newIP)
		}
	}
	rec, _, _ := deployrecord.Gather(map[string]registry.Runner{"vm": w.hosts["vm"]}, w.cfg.Deployment())
	if !rec.Lists("apps", "retired") {
		t.Errorf("vm's record lost what home-a's listed: %v", rec.Apps)
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestTheMoveFindsTheOldGatewayAndPlansItsStagesInOrder(t *testing.T) {
	w := newWorld(t)
	p := w.mustBuild()
	if p.From != "home-a" || p.To != "vm" {
		t.Fatalf("from %q to %q, want home-a to vm", p.From, p.To)
	}
	var names []string
	for _, st := range p.Stages {
		names = append(names, st.Name)
	}
	if got := strings.Join(names, ", "); got != "record, serve, dns add, dns remove, wait, stop" {
		t.Errorf("stages: %s", got)
	}
	if w.gets != 0 || len(w.zone.Deletes) != 0 || len(w.hosts["home-a"].caddy) != 1 {
		t.Errorf("planning checked %d hostnames, deleted %v, or stopped Caddy", w.gets, w.zone.Deletes)
	}
	rec := &ui.Recorder{}
	p.Show(rec)
	for _, want := range []string{"create A talk.example.org -> " + newIP, "delete A talk.example.org -> " + oldIP, "wait 5m0s", "stop and remove home-a's gateway Caddy"} {
		if !strings.Contains(rec.Lines(), want) {
			t.Errorf("the plan does not say %q:\n%s", want, rec.Lines())
		}
	}
}

func TestTheMoveRunsEveryStageAndLeavesNothingOfTheOldGateway(t *testing.T) {
	w := newWorld(t)
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	w.moved(t)
	if w.slept != 300*time.Second {
		t.Errorf("waited %s, want the records' TTL, 5m0s", w.slept)
	}
	if p := w.mustBuild(); p.From != "" {
		t.Errorf("a run after the move found %s still to move", p.From)
	}
}

func TestDeletionTouchesOnlyTheOldGatewaysRecordsAtGatewayHostnames(t *testing.T) {
	w := newWorld(t)
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	got := append([]string(nil), w.zone.Deletes...)
	sort.Strings(got)
	want := append([]string(nil), w.old...)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("deleted %v, want exactly the old gateway's records %v", got, want)
	}
	for _, id := range []string{"keep-status", "keep-endpoint", "keep-gone", "keep-other"} {
		if !slices.Contains(w.zone.IDs("zone-1"), id) {
			t.Errorf("%s was deleted", id)
		}
	}
}

func TestNothingIsDeletedOrStoppedWhileTheNewGatewayDoesNotAnswer(t *testing.T) {
	w := newWorld(t)
	w.down["docs.example.org"] = true
	err := w.run()
	if err == nil || !strings.Contains(err.Error(), "stage 2 (serve)") {
		t.Fatalf("want a stop at stage 2: %v", err)
	}
	if len(w.zone.Deletes) != 0 || len(w.hosts["home-a"].caddy) != 1 || slices.Contains(w.at("talk.example.org"), newIP) {
		t.Errorf("a failed check went on: deleted %v, Caddy %v, talk %v", w.zone.Deletes, w.hosts["home-a"].caddy, w.at("talk.example.org"))
	}
	// Fixed, the same command resumes and finishes.
	delete(w.down, "docs.example.org")
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	w.moved(t)
}

func TestARecordTheMoveMayNotDeleteStopsItBeforeAnyDelete(t *testing.T) {
	w := newWorld(t)
	w.zone.InZone["zone-1"] = append(w.zone.InZone["zone-1"],
		dns.Record{ID: "hand", Type: "A", Name: "talk.example.org", Content: oldIP})
	err := w.run()
	if err == nil || !strings.Contains(err.Error(), "stage 4 (dns remove)") {
		t.Fatalf("want a stop at stage 4: %v", err)
	}
	if len(w.zone.Deletes) != 0 {
		t.Errorf("deleted %v while a record it may not delete points at home-a", w.zone.Deletes)
	}
	if len(w.hosts["home-a"].caddy) != 1 {
		t.Errorf("the old gateway was stopped while a record points at it")
	}
	if !slices.Contains(w.at("talk.example.org"), newIP) {
		t.Errorf("the new gateway's record was not added beside the old")
	}
}

func TestAFailedDeleteResumesAndWaitsFromTheLastDelete(t *testing.T) {
	w := newWorld(t)
	w.zone.FailDelete = w.old[2]
	if err := w.run(); err == nil || !strings.Contains(err.Error(), "stage 4 (dns remove)") {
		t.Fatalf("want a stop at stage 4: %v", err)
	}
	if _, marked := w.hosts["home-a"].files[gatewaymove.MarkerPath(w.cfg.Deployment())]; marked || w.slept != 0 {
		t.Errorf("the wait started before every record went")
	}
	w.zone.FailDelete = ""
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	w.moved(t)
	if w.slept != 300*time.Second {
		t.Errorf("waited %s, want 5m0s", w.slept)
	}
}

func TestTheOldGatewayIsNotStoppedWhenTheCheckFailsAtTheStop(t *testing.T) {
	w := newWorld(t)
	w.downAfterWait = true
	err := w.run()
	if err == nil || !strings.Contains(err.Error(), "stage 6 (stop)") {
		t.Fatalf("want a stop at stage 6: %v", err)
	}
	if len(w.hosts["home-a"].caddy) != 1 {
		t.Errorf("home-a's Caddy was stopped although the new gateway did not answer")
	}
	// Still failing, a re-run stops at the first gate that checks.
	if err := w.run(); err == nil || !strings.Contains(err.Error(), "stage 2 (serve)") {
		t.Fatalf("want a stop at stage 2: %v", err)
	}
	w.downAfterWait = false
	slept := w.slept
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	w.moved(t)
	if w.slept != slept {
		t.Errorf("a resumed move waited again, %s, after the TTL had passed", w.slept-slept)
	}
}

func TestTheHostOwnersSitesKeepTheOldCaddy(t *testing.T) {
	w := newWorld(t)
	w.hosts["home-a"].sites = []string{"/srv/caddy.d/blog.caddy"}
	err := w.run()
	if err == nil || !strings.Contains(err.Error(), "stage 6 (stop)") || !strings.Contains(err.Error(), "host owner's sites") {
		t.Fatalf("want a refusal at stage 6: %v", err)
	}
	if len(w.hosts["home-a"].caddy) != 1 {
		t.Errorf("the Caddy serving the owner's sites was stopped")
	}
	w.hosts["home-a"].sites = nil
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	w.moved(t)
}

func TestAnUnreachableSiteIsAnErrorNotNoGateway(t *testing.T) {
	w := newWorld(t)
	w.hosts["home-b"].failOn = "docker ps"
	if _, err := w.build(); err == nil {
		t.Fatal("a site that did not answer was read as holding no gateway")
	}
}

func TestNothingToMoveWithOneGatewayAndNoTraceOfAnother(t *testing.T) {
	w := newWorld(t)
	w.hosts["home-a"].caddy = nil
	w.hosts["home-a"].files = map[string]string{}
	var kept []dns.Record
	for _, r := range w.zone.InZone["zone-1"] {
		if r.Content != oldIP || r.ID == "keep-gone" || r.ID == "keep-other" {
			kept = append(kept, r)
		}
	}
	w.zone.InZone["zone-1"] = kept
	p := w.mustBuild()
	if p.From != "" || len(p.Stages) != 0 {
		t.Errorf("found %q to move from", p.From)
	}
	if err := gatewaymove.Execute(p); err != nil {
		t.Fatal(err)
	}
}

func TestRecordsAloneFindTheOldGateway(t *testing.T) {
	// The old Caddy was stopped by hand: the records still point at home-a,
	// and the move still adds, verifies and deletes before it is done.
	w := newWorld(t)
	w.hosts["home-a"].caddy = nil
	w.hosts["home-a"].files = map[string]string{}
	if p := w.mustBuild(); p.From != "home-a" {
		t.Fatalf("from %q, want home-a", p.From)
	}
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	for _, h := range dns.GatewayHostnames(w.cfg) {
		if got := w.at(h.Name); !slices.Equal(got, []string{newIP}) {
			t.Errorf("%s points at %v", h.Name, got)
		}
	}
}

func TestTwoFormerGatewaysAreRefused(t *testing.T) {
	w := newWorld(t)
	w.hosts["home-b"].caddy = []string{"d00d"}
	if _, err := w.build(); err == nil || !strings.Contains(err.Error(), "home-a, home-b") {
		t.Fatalf("want a refusal naming both: %v", err)
	}
}

func TestTheMoveRefusesWithoutRecordManagement(t *testing.T) {
	w := newWorld(t, func(s string) string { return strings.Replace(s, "  provider: cloudflare", "  provider: desec", 1) })
	if _, err := w.build(); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("want a refusal: %v", err)
	}
}

// A marker an earlier move left, Eg: one rolled back, does not cut this
// move's wait short.
func TestAnotherMovesMarkerDoesNotSkipTheWait(t *testing.T) {
	w := newWorld(t)
	w.hosts["home-a"].files[gatewaymove.MarkerPath(w.cfg.Deployment())] = `{"from":"home-a","to":"home-b","deleted_at":"2026-01-01T00:00:00Z","ttl":300}` + "\n"
	// The records went by hand, so nothing is deleted on this run either.
	w.zone.InZone["zone-1"] = slices.DeleteFunc(w.zone.InZone["zone-1"], func(r dns.Record) bool { return slices.Contains(w.old, r.ID) })
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	w.moved(t)
	if w.slept != 300*time.Second {
		t.Errorf("waited %s, want 5m0s", w.slept)
	}
}

// The old gateway kept the monitor: the move finds it through DNS but must
// not stop its Caddy, which still serves the monitor, so it is refused
// before anything runs.
func TestAMoveOffASiteThatKeepsTheMonitorIsRefused(t *testing.T) {
	w := newWorld(t, func(s string) string {
		s = strings.Replace(s, "  home-a:\n    roles: [data, apps]\n", "  home-a:\n    roles: [data, apps, monitor]\n", 1)
		return strings.Replace(s, "placement: { pinned: watch }", "placement: { pinned: home-a }", 1)
	})
	_, err := w.build()
	if err == nil || !strings.Contains(err.Error(), "keeps the monitor") {
		t.Fatalf("want the shared-site refusal, got %v", err)
	}
	if len(w.hosts["home-a"].caddy) != 1 {
		t.Error("home-a's Caddy was touched")
	}
	held, err := gatewaymove.HoldsRecordOrMarker(w.hosts["home-a"], w.cfg.Deployment())
	if err != nil || !held {
		t.Errorf("home-a holds the record: %v %v", held, err)
	}
	held, err = gatewaymove.HoldsRecordOrMarker(w.hosts["home-b"], w.cfg.Deployment())
	if err != nil || held {
		t.Errorf("home-b holds nothing: %v %v", held, err)
	}
}
