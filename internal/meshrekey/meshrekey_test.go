package meshrekey

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

const wgConf = "/etc/wireguard/psns-f2a9.conf"

var (
	privateRe = regexp.MustCompile(`(?m)^PrivateKey = (\S+)`)
	peerRe    = regexp.MustCompile(`(?m)^PublicKey = (\S+)`)
)

// mesh is the fixture deployment's hosts in maps: each one's files, and the
// mesh file its interface was last handed. A handshake between two sites is
// up when each runs a peer entry for the key the other runs, which is what
// WireGuard needs of both ends.
type mesh struct {
	t       *testing.T
	cfg     *config.Config
	hosts   map[string]*host
	secrets *config.Secrets
	saves   int
	// failOnce fails the first command containing it.
	failOnce string
	// sick makes etcd answer one endpoint unhealthy.
	sick bool
	log  []string
}

type host struct {
	m     *mesh
	name  string
	files map[string]string
	run   string
	down  bool
}

func (m *mesh) Load() (*config.Secrets, error) {
	data, err := json.Marshal(m.secrets)
	if err != nil {
		return nil, err
	}
	var out config.Secrets
	return &out, json.Unmarshal(data, &out)
}

func (m *mesh) Save(s *config.Secrets) error {
	m.saves++
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	var out config.Secrets
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	m.secrets = &out
	return nil
}

func (h *host) Describe() string { return h.name }

func (h *host) ReadFile(path string) (string, bool, error) {
	if h.down {
		return "", false, fmt.Errorf("%s: %w", h.name, apply.ErrUnreachable)
	}
	c, ok := h.files[path]
	return c, ok, nil
}

func (h *host) WriteFile(path, content string, mode uint32) error {
	h.files[path] = content
	return nil
}

func (h *host) RunInput(command, stdin string) (string, error) {
	if path, ok := strings.CutPrefix(command, "cat > '"); ok {
		h.files[strings.TrimSuffix(path, "'")] = stdin
		return "", nil
	}
	return h.Run(command)
}

func publicOf(t *testing.T, private string) string {
	t.Helper()
	key, err := render.PublicKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func (h *host) runsKey() string {
	m := privateRe.FindStringSubmatch(h.run)
	if m == nil {
		return ""
	}
	return publicOf(h.m.t, m[1])
}

func (h *host) peers() []string {
	var out []string
	for _, m := range peerRe.FindAllStringSubmatch(h.run, -1) {
		out = append(out, m[1])
	}
	return out
}

func (h *host) Run(command string) (string, error) {
	m := h.m
	m.log = append(m.log, h.name+": "+command)
	if h.down {
		return "ssh: connect to host port 22: Operation timed out", fmt.Errorf("%s: %w", h.name, apply.ErrUnreachable)
	}
	if m.failOnce != "" && strings.Contains(command, m.failOnce) {
		m.failOnce = ""
		return "failed by the test", errors.New("exit status 1")
	}
	switch {
	case command == "true":
		return "", nil
	case strings.Contains(command, "wg syncconf"):
		h.run = h.files[wgConf]
		return "", nil
	case command == "wg show psns-f2a9 public-key":
		return h.runsKey() + "\n", nil
	case command == "wg show psns-f2a9 peers":
		return strings.Join(h.peers(), "\n") + "\n", nil
	case command == "wg show psns-f2a9 latest-handshakes":
		var b strings.Builder
		mine := h.runsKey()
		for _, key := range h.peers() {
			at := 0
			for _, other := range m.hosts {
				if other != h && other.runsKey() == key && contains(other.peers(), mine) {
					at = 1760000000
				}
			}
			fmt.Fprintf(&b, "%s\t%d\n", key, at)
		}
		return b.String(), nil
	case strings.Contains(command, "ip -br addr show dev psns-f2a9"):
		address := ""
		for _, line := range strings.Split(h.run, "\n") {
			if v, ok := strings.CutPrefix(line, "Address = "); ok {
				address = v
			}
		}
		return "psns-f2a9        UNKNOWN        " + address + "\n", nil
	case strings.Contains(command, "ip link show psns-f2a9"):
		return "up\n", nil
	case strings.Contains(command, "getent ahosts"):
		return "", nil
	case strings.Contains(command, "endpoint health --cluster"):
		var out []map[string]any
		for i, name := range m.cfg.Etcd.Members {
			out = append(out, map[string]any{"endpoint": "http://" + m.cfg.Sites[name].Address + ":2379", "health": !(m.sick && i == 1)})
		}
		data, _ := json.Marshal(out)
		return string(data), nil
	case strings.Contains(command, "patronictl") && strings.Contains(command, "list -f json"):
		var out []map[string]any
		for i, name := range m.cfg.Cluster.Sites {
			state := "streaming"
			if i == 0 {
				state = "running"
			}
			out = append(out, map[string]any{"Member": name, "State": state})
		}
		data, _ := json.Marshal(out)
		return string(data), nil
	}
	// apply's own plumbing around a scoped write: what it runs is not this
	// test's subject, and it succeeds.
	return "", nil
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// newMesh is every fixture site applied from the configuration as it stands,
// each interface running its file.
func newMesh(t *testing.T) *mesh {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets(filepath.Join("..", "render", "testdata", "secrets.fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := &mesh{t: t, cfg: cfg, hosts: map[string]*host{}, secrets: secrets}
	rendered, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Deployment()
	for _, name := range cfg.SiteNames() {
		h := &host{m: m, name: name, files: map[string]string{}}
		var entries []render.ManifestFile
		for _, f := range rendered.Files {
			rel, ok := strings.CutPrefix(f.Path, name+"/")
			if !ok || rel == render.ManifestName {
				continue
			}
			h.files["/"+rel] = f.Content
			entries = append(entries, render.ManifestFile{Path: rel, SHA256: sum(f.Content), Mode: fmt.Sprintf("%04o", f.Mode)})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
		manifest, _ := json.MarshalIndent(render.Manifest{Version: 1, Files: entries}, "", "  ")
		h.files[d.Manifest()] = string(manifest) + "\n"
		h.run = h.files[wgConf]
		m.hosts[name] = h
	}
	saved := []any{sleep, handshakeWait, handshakePoll, healthWait, healthPoll}
	sleep = func(time.Duration) {}
	handshakeWait, handshakePoll, healthWait, healthPoll = 3*time.Second, time.Second, 3*time.Second, time.Second
	t.Cleanup(func() {
		sleep = saved[0].(func(time.Duration))
		handshakeWait, handshakePoll = saved[1].(time.Duration), saved[2].(time.Duration)
		healthWait, healthPoll = saved[3].(time.Duration), saved[4].(time.Duration)
	})
	return m
}

func (m *mesh) transports() map[string]apply.Transport {
	out := map[string]apply.Transport{}
	for name, h := range m.hosts {
		out[name] = h
	}
	return out
}

func (m *mesh) build(sites ...string) (*Plan, error) {
	return Build(m.cfg, m, sites, m.transports())
}

func run(t *testing.T, p *Plan) error {
	t.Helper()
	rec := &ui.Recorder{}
	p.Report = rec
	err := Execute(p)
	if err != nil {
		t.Log(rec.Lines())
	}
	return err
}

// A rekey of every site leaves each on a new key, every peer running it, and
// the secrets holding only the new keys.
func TestRekeyingEverySite(t *testing.T) {
	m := newMesh(t)
	before := map[string]string{}
	for name, s := range m.secrets.Sites {
		before[name] = s.WireGuardPrivateKey
	}
	p, err := m.build(m.cfg.SiteNames()...)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p.Sites {
		if s.From != NewKey {
			t.Errorf("%s starts at %d", s.Name, s.From)
		}
	}
	if err := run(t, p); err != nil {
		t.Fatal(err)
	}
	for _, name := range m.cfg.SiteNames() {
		s := m.secrets.Sites[name]
		if s.WireGuardPrivateKey == before[name] || s.WireGuardNextPrivateKey != "" {
			t.Errorf("%s: key changed %v, next left %q", name, s.WireGuardPrivateKey != before[name], s.WireGuardNextPrivateKey)
		}
		key := publicOf(t, s.WireGuardPrivateKey)
		if m.hosts[name].runsKey() != key {
			t.Errorf("%s does not run its new key", name)
		}
		for other, h := range m.hosts {
			if other != name && strings.Contains(h.run, publicOf(t, before[name])) {
				t.Errorf("%s still runs %s's old key", other, name)
			}
		}
	}
}

// One site at a time: a site's swap is done, gated and promoted before the
// next site's new key exists.
func TestOneSiteAtATime(t *testing.T) {
	m := newMesh(t)
	p, err := m.build("home-a", "home-b")
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	saves := 0
	m2 := &recordingStore{mesh: m, on: func(s *config.Secrets) {
		saves++
		order = append(order, fmt.Sprintf("%v/%v", s.Sites["home-a"].WireGuardNextPrivateKey != "", s.Sites["home-b"].WireGuardNextPrivateKey != ""))
	}}
	p.store = m2
	if err := run(t, p); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, " "); got != "true/false false/false false/true false/false" {
		t.Errorf("next keys at each save: %s", got)
	}
}

type recordingStore struct {
	*mesh
	on func(*config.Secrets)
}

func (r *recordingStore) Save(s *config.Secrets) error {
	r.on(s)
	return r.mesh.Save(s)
}

// A run stopped at any stage resumes there, and finishes.
func TestARekeyResumes(t *testing.T) {
	for _, tc := range []struct {
		fail string
		from Stage
	}{
		{"wg syncconf", Swap},
		{"endpoint health", Health},
	} {
		t.Run(tc.fail, func(t *testing.T) {
			m := newMesh(t)
			p, err := m.build("vm")
			if err != nil {
				t.Fatal(err)
			}
			old := m.secrets.Sites["vm"].WireGuardPrivateKey
			m.failOnce = tc.fail
			if tc.fail == "endpoint health" {
				// One read, so the failed one is the gate's answer.
				healthWait = healthPoll
			}
			if err := run(t, p); err == nil || !strings.Contains(err.Error(), "resumes where this stopped") {
				t.Fatalf("err = %v", err)
			}
			if m.secrets.Sites["vm"].WireGuardNextPrivateKey == "" || m.secrets.Sites["vm"].WireGuardPrivateKey != old {
				t.Fatal("a stopped rekey left no next key, or promoted it")
			}
			next := m.secrets.Sites["vm"].WireGuardNextPrivateKey
			again, err := m.build("vm")
			if err != nil {
				t.Fatal(err)
			}
			if again.Sites[0].From != tc.from {
				t.Errorf("resumes at %d, want %d", again.Sites[0].From, tc.from)
			}
			healthWait = 3 * time.Second
			if err := run(t, again); err != nil {
				t.Fatal(err)
			}
			if got := m.secrets.Sites["vm"]; got.WireGuardPrivateKey != next || got.WireGuardNextPrivateKey != "" {
				t.Errorf("the resumed rekey did not promote the key it started with")
			}
			if m.hosts["vm"].runsKey() != publicOf(t, next) {
				t.Error("vm does not run its new key")
			}
		})
	}
}

// Refused before anything changes.
func TestARekeyRefuses(t *testing.T) {
	m := newMesh(t)
	m.sick = true
	if _, err := m.build("vm"); err == nil || !strings.Contains(err.Error(), "etcd is unhealthy") {
		t.Errorf("unhealthy etcd: %v", err)
	}
	m.sick = false
	m.hosts["watch"].down = true
	if _, err := m.build("vm"); err == nil || !strings.Contains(err.Error(), "watch does not answer over ssh") {
		t.Errorf("a site that does not answer: %v", err)
	}
	m.hosts["watch"].down = false
	if _, err := m.build("nowhere"); err == nil || !strings.Contains(err.Error(), "declares no site nowhere") {
		t.Errorf("an undeclared site: %v", err)
	}
	if m.saves != 0 {
		t.Error("a refused rekey wrote the secrets")
	}
}

// The one dialled site is every undialled site's path; rekeying it takes
// them down with it.
func TestARekeyRefusesTheOnlyRelay(t *testing.T) {
	m := newMesh(t)
	for _, name := range []string{"home-b", "watch"} {
		s := m.cfg.Sites[name]
		s.Endpoint, s.PublicAddress = "", ""
		m.cfg.Sites[name] = s
	}
	if err := refuseLostRelay(m.cfg, []string{"vm"}); err == nil || !strings.Contains(err.Error(), "only site with a dial endpoint") {
		t.Errorf("err = %v", err)
	}
	if err := refuseLostRelay(m.cfg, []string{"home-a"}); err != nil {
		t.Errorf("home-a relays nothing: %v", err)
	}
}
