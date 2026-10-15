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

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

const wgConf = "/srv/paisans/f2a9/psns-f2a9.conf"

// wgHelper is the start of every command sent to the WireGuard helper for
// the fixture's interface, and wgInstalled is where its put installs the
// mesh file on a real host.
const (
	wgHelper    = "sudo -n '/usr/local/libexec/paisans/wg-psns-f2a9' "
	wgInstalled = "/etc/wireguard/psns-f2a9.conf"
)

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
	// failOnce fails the first command containing it, after failSkip of
	// them have run, and only on failHost when that is set.
	failOnce string
	failSkip int
	failHost string
	// sick makes etcd answer one endpoint unhealthy.
	sick bool
	// patroni overrides a data site's role and state in `patronictl list`.
	patroni map[string][2]string
	// paused is Patroni's maintenance mode.
	paused bool
	// promotedAt is where in log each site's key was promoted.
	promotedAt map[string]int
	log        []string
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
	if m.secrets != nil {
		for name, site := range s.Sites {
			if old := m.secrets.Sites[name]; old.WireGuardNextPrivateKey != "" && site.WireGuardNextPrivateKey == "" {
				m.promotedAt[name] = len(m.log)
			}
		}
	}
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
	if command == wgHelper+"put" {
		out, err := h.Run(command)
		if err == nil {
			h.files[wgInstalled] = stdin
		}
		return out, err
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
	if m.failOnce != "" && strings.Contains(command, m.failOnce) && (m.failHost == "" || m.failHost == h.name) {
		if m.failSkip > 0 {
			m.failSkip--
		} else {
			m.failOnce = ""
			return "failed by the test", errors.New("exit status 1")
		}
	}
	if out, ok, err := chainedRead(command, h.Run); ok {
		return out, err
	}
	if field, ok := strings.CutPrefix(command, wgHelper+"show "); ok && field != "latest-handshakes" {
		if out, ok := showRunning(field, h.run); ok {
			return out, nil
		}
	}
	switch {
	case strings.HasPrefix(command, "wg "), strings.HasPrefix(command, "wg-quick "), strings.Contains(command, "wg syncconf"):
		return "", fmt.Errorf("%s: %q reached the mesh without the WireGuard helper", h.name, command)
	case command == "true":
		return "", nil
	case command == wgHelper+"put":
		return "", nil
	case command == wgHelper+"sync", command == wgHelper+"up", command == wgHelper+"restart":
		h.run = h.files[wgInstalled]
		return "", nil
	case command == wgHelper+"show latest-handshakes":
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
	case strings.Contains(command, "getent ahosts"):
		return "", nil
	case strings.Contains(command, "endpoint health --cluster"):
		var out []map[string]any
		for i, name := range m.cfg.Etcd.Members {
			// A member is healthy while its tunnel to every other member is
			// up, which a swap part way breaks.
			up := true
			for _, other := range m.cfg.Etcd.Members {
				up = up && (other == name || m.tunnel(name, other))
			}
			out = append(out, map[string]any{"endpoint": "http://" + m.cfg.Sites[name].Address + ":2379", "health": up && !(m.sick && i == 1)})
		}
		data, _ := json.Marshal(out)
		return string(data), nil
	case strings.Contains(command, "patronictl") && strings.HasSuffix(command, "show-config"):
		if m.paused {
			return "loop_wait: 10\npause: true\n", nil
		}
		return "loop_wait: 10\n", nil
	case strings.Contains(command, "patronictl") && strings.HasSuffix(command, "pause --wait"):
		if m.paused {
			return "Error: Cluster is already paused", errors.New("exit status 1")
		}
		m.paused = true
		return "Success: cluster management is paused\n", nil
	case strings.Contains(command, "patronictl") && strings.HasSuffix(command, "resume --wait"):
		if !m.paused {
			return "Error: Cluster is not paused", errors.New("exit status 1")
		}
		m.paused = false
		return "Success: cluster management is resumed\n", nil
	case strings.Contains(command, "patronictl") && strings.Contains(command, "list -f json"):
		var out []map[string]any
		for i, name := range m.cfg.Cluster.Sites {
			role, state := "Replica", "streaming"
			if i == 0 {
				role, state = "Leader", "running"
			}
			if s, ok := m.patroni[name]; ok {
				role, state = s[0], s[1]
			}
			out = append(out, map[string]any{"Member": name, "Role": role, "State": state})
		}
		data, _ := json.Marshal(out)
		return string(data), nil
	}
	// apply's own plumbing around a scoped write: what it runs is not this
	// test's subject, and it succeeds.
	return "", nil
}

// tunnel is whether a and b each list the key the other runs, or, for two
// sites that do not peer directly, whether both have a tunnel to a site with
// a dial endpoint, their relay.
func (m *mesh) tunnel(a, b string) bool {
	if !m.cfg.Sites[a].PeersDirectly(m.cfg.Sites[b]) {
		for _, r := range m.cfg.SiteNames() {
			if r != a && r != b && m.cfg.Sites[r].DialEndpoint() != "" && m.tunnel(a, r) && m.tunnel(b, r) {
				return true
			}
		}
		return false
	}
	ha, hb := m.hosts[a], m.hosts[b]
	return contains(ha.peers(), hb.runsKey()) && contains(hb.peers(), ha.runsKey())
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// newMesh is every fixture site applied from the configuration as it stands,
// each interface running its file.
func newMesh(t *testing.T, edits ...func(*config.Config)) *mesh {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range edits {
		edit(cfg)
	}
	secrets, err := config.LoadSecrets(filepath.Join("..", "render", "testdata", "secrets.fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := &mesh{t: t, cfg: cfg, hosts: map[string]*host{}, secrets: secrets, promotedAt: map[string]int{}}
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
		h.files[wgInstalled], h.run = h.files[wgConf], h.files[wgConf]
		reg := registry.Registry{Version: registry.Version, Deployments: map[string]registry.Entry{}}
		id, e := registry.For(cfg, name, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
		reg.Deployments[id] = e
		data, err := registry.Encode(reg)
		if err != nil {
			t.Fatal(err)
		}
		h.files[registry.Path] = string(data)
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

// joining makes site's host a fresh one, no claim and no files, and every
// other site's mesh as an apply without it rendered it, since it has never
// run.
func (m *mesh) joining(site string) {
	m.hosts[site].files, m.hosts[site].run = map[string]string{}, ""
	rendered, err := render.Build(m.cfg.WithoutJoining(site), m.secrets)
	if err != nil {
		m.t.Fatal(err)
	}
	d := m.cfg.Deployment()
	for name, h := range m.hosts {
		content := renderedFile(rendered, name, d.WireGuardConf())
		if name == site || content == "" {
			continue
		}
		h.files[wgConf], h.files[wgInstalled], h.run = content, content, content
		var man render.Manifest
		if err := json.Unmarshal([]byte(h.files[d.Manifest()]), &man); err != nil {
			m.t.Fatal(err)
		}
		for i := range man.Files {
			if man.Files[i].Path == d.WireGuardConf() {
				man.Files[i].SHA256 = sum(content)
			}
		}
		data, _ := json.MarshalIndent(man, "", "  ")
		h.files[d.Manifest()] = string(data) + "\n"
	}
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
		{"' sync", Swap},
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
				// One read, so the failed one is the gate's answer, and
				// not the check before the rekey starts.
				healthWait = healthPoll
				m.failSkip = 1
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
	for _, tc := range []struct {
		name  string
		state map[string][2]string
		want  string
	}{
		{"no leader", map[string][2]string{"home-a": {"Replica", "streaming"}}, "0 members lead"},
		{"two leaders", map[string][2]string{"home-b": {"Leader", "running"}}, "2 members lead"},
		{"a replica not streaming", map[string][2]string{"home-b": {"Replica", "running"}}, "home-b is running, not streaming"},
		{"a stopped leader", map[string][2]string{"home-a": {"Leader", "stopped"}}, "the leader home-a is stopped"},
	} {
		m.patroni = tc.state
		if _, err := m.build("vm"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	m.patroni = nil
	if m.saves != 0 {
		t.Error("a refused rekey wrote the secrets")
	}
}

// undialled takes home-b's endpoint away, so home-a and home-b reach each
// other only through vm, the first dialled site: vm relays.
func undialled(cfg *config.Config) {
	s := cfg.Sites["home-b"]
	s.Endpoint, s.PublicAddress = "", ""
	cfg.Sites["home-b"] = s
}

// A relay's rekey pauses Patroni before its swap and resumes it once every
// handshake is back and the cluster is healthy, so the moment home-a and
// home-b cannot reach each other cannot fail Patroni over. A site nothing
// relays through pauses nothing.
func TestARelaysRekeyPausesPatroni(t *testing.T) {
	m := newMesh(t, undialled)
	p, err := BuildAll(m.cfg, m, m.transports())
	if err != nil {
		t.Fatal(err)
	}
	relays := map[string]string{}
	for _, s := range p.Sites {
		relays[s.Name] = strings.Join(s.Relays, ",")
	}
	if relays["vm"] != "home-a,home-b" || relays["home-a"] != "" || relays["watch"] != "" {
		t.Fatalf("relays: %v", relays)
	}
	rec := &ui.Recorder{}
	p.Show(rec)
	if !strings.Contains(rec.Lines(), "pause Patroni") {
		t.Errorf("the plan does not say Patroni pauses:\n%s", rec.Lines())
	}
	if err := run(t, p); err != nil {
		t.Fatal(err)
	}
	pause, resume, vmSync, vmPromoted := -1, -1, -1, -1
	for i, line := range m.log {
		switch {
		case strings.Contains(line, "patronictl") && strings.HasSuffix(line, "pause --wait"):
			if pause >= 0 {
				t.Error("Patroni was paused twice")
			}
			pause = i
		case strings.Contains(line, "patronictl") && strings.HasSuffix(line, "resume --wait"):
			resume = i
		case vmSync < 0 && pause >= 0 && strings.HasPrefix(line, "vm: ") && strings.Contains(line, "' sync"):
			vmSync = i
		}
	}
	vmPromoted = m.promotedAt["vm"]
	if pause < 0 || vmSync < pause || resume < vmSync || vmPromoted < resume {
		t.Errorf("pause %d, vm's sync %d, resume %d, promotion %d", pause, vmSync, resume, vmPromoted)
	}
	if m.paused {
		t.Error("Patroni was left paused")
	}
}

// A relay's swap that stops leaves Patroni paused, and the run that resumes
// it finishes the swap, then resumes Patroni.
func TestAStoppedRelaySwapResumesPatroni(t *testing.T) {
	m := newMesh(t, undialled)
	p, err := m.build("vm")
	if err != nil {
		t.Fatal(err)
	}
	m.failOnce, m.failHost = "' sync", "vm"
	if err := run(t, p); err == nil || !strings.Contains(err.Error(), "Patroni stays paused") {
		t.Fatalf("err = %v", err)
	}
	if !m.paused {
		t.Fatal("Patroni was resumed with the swap part way")
	}
	again, err := m.build("vm")
	if err != nil {
		t.Fatal(err)
	}
	if err := run(t, again); err != nil {
		t.Fatal(err)
	}
	if m.paused || len(m.secrets.RekeyPending()) != 0 {
		t.Errorf("paused %v, pending %v", m.paused, m.secrets.RekeyPending())
	}
}

// A Patroni paused by something else stops a new site's rekey before it
// starts.
func TestARekeyRefusesAPausedPatroni(t *testing.T) {
	m := newMesh(t)
	m.paused = true
	if _, err := m.build("home-a"); err == nil || !strings.Contains(err.Error(), "this run did not pause it") {
		t.Errorf("err = %v", err)
	}
}

// A site declared and not brought up yet, a replacement waiting for its
// apply, is neither rekeyed nor written to, and no peer is handed it.
func TestARekeyLeavesASiteNotBroughtUpAlone(t *testing.T) {
	m := newMesh(t)
	m.joining("watch")
	p, err := BuildAll(m.cfg, m, m.transports())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range p.Sites {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "home-a,home-b,vm" || strings.Join(p.Joining, ",") != "watch" {
		t.Fatalf("rekeys %v, joining %v", names, p.Joining)
	}
	if err := run(t, p); err != nil {
		t.Fatal(err)
	}
	if len(m.hosts["watch"].files) != 0 {
		t.Error("the site not brought up was written to")
	}
	if _, err := m.build("watch"); err == nil || !strings.Contains(err.Error(), "holds no claim") {
		t.Errorf("naming it: %v", err)
	}
}

// A site's swap renders every other site with the key its host runs: a peer
// part way through its own rekey keeps its new key on every host.
func TestASwapKeepsAPeerMidRekeyOnItsNewKey(t *testing.T) {
	m := newMesh(t)
	vm := m.secrets.Sites["vm"]
	running := vm.WireGuardPrivateKey
	vm.WireGuardPrivateKey, vm.WireGuardNextPrivateKey = "SUlJSUlJSUlJSUlJSUlJSUlJSUlJSUlJSUlJSUlJSUk=", running
	m.secrets.Sites["vm"] = vm
	a := m.secrets.Sites["home-a"]
	a.WireGuardNextPrivateKey = "REREREREREREREREREREREREREREREREREREREREREQ="
	m.secrets.Sites["home-a"] = a
	p, err := m.build("home-a")
	if err != nil {
		t.Fatal(err)
	}
	p.Report = &ui.Recorder{}
	if err := p.swap("home-a", m.secrets, publicOf(t, a.WireGuardNextPrivateKey)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"home-a", "home-b", "watch"} {
		if !contains(m.hosts[name].peers(), publicOf(t, running)) {
			t.Errorf("%s no longer lists vm's new key", name)
		}
	}
}

// A swap stopped part way, the peers on the new key and the site still on
// the old, leaves the cluster unhealthy: the next run finishes that swap
// before it checks the cluster, rather than refusing on the state the swap
// itself left.
func TestAPartialSwapResumesOnAnUnhealthyCluster(t *testing.T) {
	m := newMesh(t)
	p, err := m.build("vm")
	if err != nil {
		t.Fatal(err)
	}
	m.failOnce, m.failHost = "' sync", "vm"
	if err := run(t, p); err == nil {
		t.Fatal("the swap that missed vm passed")
	}
	if m.tunnel("vm", "home-a") {
		t.Fatal("the test needs the tunnel down")
	}
	again, err := m.build("home-a")
	if err != nil {
		t.Fatalf("a stopped swap does not resume: %v", err)
	}
	var names []string
	for _, s := range again.Sites {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "vm,home-a" || again.Sites[0].From != Swap || strings.Join(again.Finishing, ",") != "vm" {
		t.Fatalf("plan: %v, from %d, finishing %v", names, again.Sites[0].From, again.Finishing)
	}
	if err := run(t, again); err != nil {
		t.Fatal(err)
	}
	if len(m.secrets.RekeyPending()) != 0 || !m.tunnel("vm", "home-a") {
		t.Error("the resumed run did not finish vm")
	}
}

// An --all run that stops between sites resumes with the sites it had left:
// a site it already promoted is not rekeyed again.
func TestAnAllRunResumesWithoutRekeyingAgain(t *testing.T) {
	m := newMesh(t)
	p, err := BuildAll(m.cfg, m, m.transports())
	if err != nil {
		t.Fatal(err)
	}
	// home-a's check before and after its swap pass; home-b's first fails.
	m.failOnce, m.failSkip = "endpoint health", 2
	if err := run(t, p); err == nil {
		t.Fatal("the run did not stop")
	}
	promoted := m.secrets.Sites["home-a"].WireGuardPrivateKey
	if got := strings.Join(m.secrets.MeshRekeyRemaining, ","); got != "home-b,vm,watch" {
		t.Fatalf("remaining: %s", got)
	}
	again, err := BuildAll(m.cfg, m, m.transports())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range again.Sites {
		names = append(names, s.Name)
	}
	if !again.Resumed || strings.Join(names, ",") != "home-b,vm,watch" {
		t.Fatalf("resumed %v with %v", again.Resumed, names)
	}
	if err := run(t, again); err != nil {
		t.Fatal(err)
	}
	if m.secrets.Sites["home-a"].WireGuardPrivateKey != promoted {
		t.Error("home-a was rekeyed a second time")
	}
	if len(m.secrets.MeshRekeyRemaining) != 0 {
		t.Errorf("the record outlived the run: %v", m.secrets.MeshRekeyRemaining)
	}
}

// A site whose host lost its claim, while its peers still list its key, is
// not taken for one not brought up yet: a rekey would leave it out while it
// runs.
func TestARekeyRefusesASiteThatOnlyLostItsClaim(t *testing.T) {
	m := newMesh(t)
	delete(m.hosts["watch"].files, registry.Path)
	if _, err := m.build("vm"); err == nil || !strings.Contains(err.Error(), "watch's host holds no claim") || !strings.Contains(err.Error(), "lists its WireGuard key as a peer") {
		t.Errorf("err = %v", err)
	}
}
