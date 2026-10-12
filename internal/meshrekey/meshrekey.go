// Package meshrekey is `paisans mesh rekey`: a site's WireGuard key replaced
// on every host that holds it, one site at a time.
//
// A site's key is in secrets.enc.yaml, so whoever could read that file holds
// it, and WireGuard accepts a handshake from any address that proves a key
// and moves the peer's endpoint to where it came from. A key in hostile hands
// can take over that site's tunnel to any peer it can send a packet to, so
// once such a reader is gone every site's key is replaced.
//
// One site at a time, because a site whose key changes loses its tunnels
// until every peer knows the new key, and two sites down at once can cost
// etcd its quorum. Each site goes through four stages, each ending at a
// gate: the new key written to the secrets file as the site's next key; the
// peers' mesh files and then the site's own handed the new key with `wg
// syncconf`; the cluster's health; and the next key promoted. A run that
// stops part way resumes: a next key in the file says a rekey is under way,
// and which key the host runs, read with `wg show`, says whether its swap is
// done. docs/specs/2026-10-11-site-evict.md, "Rekeying the mesh", is the
// approved design.
package meshrekey

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/paisans-software/paisans-stack/internal/acme"
	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/secretsgen"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// Store is the secrets file: read decrypted and written encrypted to its
// recipients, on the workstation.
type Store interface {
	Load() (*config.Secrets, error)
	Save(*config.Secrets) error
}

// Timing, replaced by tests. A peer that dials the site sends a keepalive
// every 25 seconds (wireguard.conf.tmpl), which starts a handshake under the
// new key well within the wait.
var (
	sleep         = time.Sleep
	handshakeWait = 2 * time.Minute
	handshakePoll = 5 * time.Second
	healthWait    = 2 * time.Minute
	healthPoll    = 5 * time.Second
)

// Stage is where one site's rekey stands.
type Stage int

const (
	// NewKey is a site with no next key: the whole rekey is ahead.
	NewKey Stage = 1
	// Swap is a site with a next key its host does not run yet.
	Swap Stage = 2
	// Health is a site whose host runs its next key.
	Health Stage = 3
)

// Site is one site's rekey as planned.
type Site struct {
	Name string
	// From is the stage the rekey starts at.
	From Stage
}

// Plan is a whole run: the sites in order.
type Plan struct {
	Sites  []Site
	Report ui.Reporter

	cfg        *config.Config
	store      Store
	transports map[string]apply.Transport
}

func (p *Plan) dep() deployment.Deployment { return p.cfg.Deployment() }

func (p *Plan) reporter() ui.Reporter {
	if p.Report == nil {
		return ui.Discard
	}
	return p.Report
}

// Build plans the rekey of sites, in the order given, reading every site and
// changing none. transports reaches every site the configuration declares,
// since each is a peer of some site being rekeyed.
func Build(cfg *config.Config, store Store, sites []string, transports map[string]apply.Transport) (*Plan, error) {
	if len(sites) == 0 {
		return nil, fmt.Errorf("mesh rekey: name the sites to rekey, or --all")
	}
	p := &Plan{cfg: cfg, store: store, transports: transports}
	seen := map[string]bool{}
	for _, name := range sites {
		if _, ok := cfg.Sites[name]; !ok {
			return nil, fmt.Errorf("mesh rekey: the configuration declares no site %s. Declared sites are %s", name, strings.Join(cfg.SiteNames(), ", "))
		}
		if seen[name] {
			return nil, fmt.Errorf("mesh rekey: %s is named twice", name)
		}
		seen[name] = true
	}
	if err := refuseLostRelay(cfg, sites); err != nil {
		return nil, err
	}
	for _, name := range cfg.SiteNames() {
		t, ok := transports[name]
		if !ok {
			return nil, fmt.Errorf("mesh rekey: no way to reach site %s", name)
		}
		if out, err := t.Run("true"); err != nil {
			return nil, fmt.Errorf("mesh rekey: %s does not answer over ssh (%v: %s). Every site is a peer of a site being rekeyed and has its mesh file changed, so every one must answer. Fix ssh and run again. Nothing was changed", name, err, strings.TrimSpace(out))
		}
	}
	if err := p.health(); err != nil {
		return nil, fmt.Errorf("mesh rekey: %v. A rekey takes each site's tunnels down for a moment, which a cluster already short of a member may not survive. Fix it (`paisans doctor` says how) and run again. Nothing was changed", err)
	}
	secrets, err := store.Load()
	if err != nil {
		return nil, err
	}
	for _, name := range sites {
		s := Site{Name: name, From: NewKey}
		if next := secrets.Sites[name].WireGuardNextPrivateKey; next != "" {
			s.From = Swap
			runs, err := p.runningKey(name)
			if err != nil {
				return nil, err
			}
			want, err := render.PublicKey(next)
			if err != nil {
				return nil, fmt.Errorf("secrets sites.%s.wireguard_next_private_key: %w", name, err)
			}
			if runs == want {
				s.From = Health
			}
		}
		p.Sites = append(p.Sites, s)
	}
	return p, nil
}

// refuseLostRelay refuses a run that rekeys the one site every undialled site
// relays through. Such a site peers only with dialled sites, and when there
// is one, its swap takes every undialled site off the mesh with it: more than
// one site down at once, which can cost etcd its quorum.
func refuseLostRelay(cfg *config.Config, sites []string) error {
	var dialled, undialled []string
	for _, name := range cfg.SiteNames() {
		if cfg.Sites[name].DialEndpoint() != "" {
			dialled = append(dialled, name)
		} else {
			undialled = append(undialled, name)
		}
	}
	if len(dialled) != 1 || len(undialled) == 0 {
		return nil
	}
	for _, name := range sites {
		if name == dialled[0] {
			return fmt.Errorf("mesh rekey: %s is the only site with a dial endpoint, so %s reach the mesh only through it, and its rekey would take them down with it. Give another site an endpoint (or a public_address) and apply it first. Nothing was changed", name, strings.Join(undialled, ", "))
		}
	}
	return nil
}

// runningKey is the public key site's interface runs. `wg show <interface>
// public-key` prints it (wireguard-tools, wg(8)).
func (p *Plan) runningKey(site string) (string, error) {
	out, err := p.transports[site].Run("wg show " + p.dep().Interface() + " public-key")
	if err != nil {
		return "", fmt.Errorf("%s: `wg show %s public-key`: %w: %s", site, p.dep().Interface(), err, strings.TrimSpace(out))
	}
	return strings.TrimSpace(out), nil
}

// Show lists the plan: a section per site, each remaining stage pending.
func (p *Plan) Show(r ui.Reporter) {
	for _, s := range p.Sites {
		r.Section("rekey " + s.Name)
		for _, st := range p.stages(s) {
			r.Step(st.title).End(ui.Pending, "")
			r.Detail("%s", st.work)
			r.Step("gate: "+st.gate).End(ui.Pending, "")
		}
	}
}

type stageText struct{ title, work, gate string }

func (p *Plan) stages(s Site) []stageText {
	all := []stageText{
		{"new key for " + s.Name, "a new private key, generated here, written to the secrets file as sites." + s.Name + ".wireguard_next_private_key beside the current one", "the file decrypts and holds it"},
		{"swap " + s.Name + "'s key", "every peer's mesh file with the new public key in place of the old, then " + s.Name + "'s own with the new private key, each handed to its interface with `wg syncconf`; the peers at once and " + s.Name + " last", "every peer has a handshake with " + s.Name + " under the new key"},
		{"check the cluster", "nothing changes", "etcd healthy with every voter, every Patroni member running or streaming"},
		{"promote " + s.Name + "'s key", "the next key becomes sites." + s.Name + ".wireguard_private_key in the secrets file, and the old one is deleted", "the file decrypts and holds only the new key"},
	}
	return all[s.From-1:]
}

// Execute rekeys each site in turn, every stage ending at its gate. A stage
// that fails stops the run: the next run resumes there.
func Execute(p *Plan) error {
	for _, s := range p.Sites {
		if err := p.rekey(s); err != nil {
			return fmt.Errorf("mesh rekey %s stopped: %w. Nothing after it ran. Fix the cause and run mesh rekey again: it resumes where this stopped. Run no apply on any site until it finishes: an apply renders the key the secrets file holds as current, which is the old one until the last stage", s.Name, err)
		}
	}
	return nil
}

func (p *Plan) rekey(s Site) error {
	r := p.reporter()
	r.Section("rekey " + s.Name)
	secrets, err := p.store.Load()
	if err != nil {
		return err
	}
	site := secrets.Sites[s.Name]
	if site.WireGuardNextPrivateKey == "" {
		step := r.Step("new key for " + s.Name)
		key, err := secretsgen.WireGuardPrivateKey()
		if err != nil {
			step.Fail(err)
			return err
		}
		site.WireGuardNextPrivateKey = key
		secrets.Sites[s.Name] = site
		if err := p.store.Save(secrets); err != nil {
			step.Fail(err)
			return err
		}
		if secrets, err = p.store.Load(); err != nil || secrets.Sites[s.Name].WireGuardNextPrivateKey != key {
			err = errors.Join(err, fmt.Errorf("the secrets file does not hold the next key it was written with"))
			step.Fail(err)
			return err
		}
		step.Done("written")
	}
	next := secrets.Sites[s.Name].WireGuardNextPrivateKey
	newPublic, err := render.PublicKey(next)
	if err != nil {
		return fmt.Errorf("secrets sites.%s.wireguard_next_private_key: %w", s.Name, err)
	}
	if err := p.swap(s.Name, secrets, newPublic); err != nil {
		return err
	}
	step := r.Step("check the cluster")
	if err := poll(healthWait, healthPoll, p.health); err != nil {
		step.Fail(err)
		return fmt.Errorf("gate: %w", err)
	}
	step.Done("healthy")

	step = r.Step("promote " + s.Name + "'s key")
	secrets, err = p.store.Load()
	if err != nil {
		step.Fail(err)
		return err
	}
	site = secrets.Sites[s.Name]
	site.WireGuardPrivateKey, site.WireGuardNextPrivateKey = next, ""
	secrets.Sites[s.Name] = site
	if err := p.store.Save(secrets); err != nil {
		step.Fail(err)
		return err
	}
	if secrets, err = p.store.Load(); err != nil || secrets.Sites[s.Name].WireGuardPrivateKey != next || secrets.Sites[s.Name].WireGuardNextPrivateKey != "" {
		err = errors.Join(err, fmt.Errorf("the secrets file does not hold the promoted key alone"))
		step.Fail(err)
		return err
	}
	step.Done("promoted")
	return nil
}

// swap hands every peer the new public key, then the site its new private
// key, and waits for a handshake under it on every peer that lists the site.
// Each file goes through a scoped apply, so a file edited on a host is
// refused as apply refuses it, and the manifest records what was written.
func (p *Plan) swap(site string, secrets *config.Secrets, newPublic string) error {
	r := p.reporter()
	swapped := *secrets
	swapped.Sites = map[string]config.SiteSecrets{}
	for name, s := range secrets.Sites {
		swapped.Sites[name] = s
	}
	s := swapped.Sites[site]
	s.WireGuardPrivateKey = s.WireGuardNextPrivateKey
	swapped.Sites[site] = s
	rendered, err := render.Build(p.cfg, &swapped)
	if err != nil {
		return err
	}
	conf := p.dep().WireGuardConf()
	var peers []string
	for _, name := range p.cfg.SiteNames() {
		if name != site && strings.Contains(renderedFile(rendered, name, conf), newPublic) {
			peers = append(peers, name)
		}
	}
	// Planned for every site first, so a file edited on any host stops the
	// swap before a single peer has changed.
	plans := map[string]*apply.Plan{}
	order := append(append([]string(nil), peers...), site)
	for _, name := range order {
		sp, err := apply.Build(name, rendered, acme.Module(p.cfg.ACME.Provider), p.transports[name], apply.Scope(conf))
		if err != nil {
			return err
		}
		if c := sp.Conflicts(); len(c) > 0 {
			return fmt.Errorf("%s's %s differs from what the last apply recorded, so somebody edited it on the host. Restore it, or copy what is wanted into the configuration, and run again", name, c[0].Path)
		}
		plans[name] = sp
	}
	for _, name := range order {
		step := r.Step("swap " + site + "'s key on " + name)
		if err := p.handTo(name, plans[name], site, newPublic); err != nil {
			step.Fail(err)
			return err
		}
		step.Done("")
	}
	step := r.Step("gate: every peer has a handshake with " + site + " under the new key")
	err = poll(handshakeWait, handshakePoll, func() error {
		for _, name := range peers {
			ok, err := p.handshake(name, newPublic)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("%s has no handshake with %s under the new key yet", name, site)
			}
		}
		return nil
	})
	if err != nil {
		step.Fail(err)
		return fmt.Errorf("gate: %w", err)
	}
	step.Done("passed")
	return nil
}

// handTo writes one host's mesh file and hands it to the interface. A file
// already written by a run that stopped before its sync is handed over again
// when the interface does not run it yet.
func (p *Plan) handTo(name string, sp *apply.Plan, site, newPublic string) error {
	t := p.transports[name]
	if len(sp.Writes()) > 0 || sp.WireGuard != apply.WireGuardNone {
		return apply.Execute(sp, t)
	}
	runs := false
	if name == site {
		key, err := p.runningKey(name)
		if err != nil {
			return err
		}
		runs = key == newPublic
	} else {
		out, err := t.Run("wg show " + p.dep().Interface() + " peers")
		if err != nil {
			return fmt.Errorf("%s: `wg show %s peers`: %w: %s", name, p.dep().Interface(), err, strings.TrimSpace(out))
		}
		runs = strings.Contains(out, newPublic)
	}
	if runs {
		return nil
	}
	if out, err := t.Run(apply.WireGuardSync.Command(p.dep())); err != nil {
		return fmt.Errorf("%s: %s: %w: %s", name, apply.WireGuardSync.Title(p.dep()), err, strings.TrimSpace(out))
	}
	return nil
}

// handshake reports whether site's interface has had a handshake with key.
// `wg show <interface> latest-handshakes` prints each peer's public key and
// the time of its last handshake, 0 for none (wireguard-tools, wg(8)).
func (p *Plan) handshake(site, key string) (bool, error) {
	out, err := p.transports[site].Run("wg show " + p.dep().Interface() + " latest-handshakes")
	if err != nil {
		return false, fmt.Errorf("%s: `wg show %s latest-handshakes`: %w: %s", site, p.dep().Interface(), err, strings.TrimSpace(out))
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == key {
			at, err := strconv.ParseInt(f[1], 10, 64)
			return err == nil && at > 0, nil
		}
	}
	return false, nil
}

func renderedFile(rendered *render.Plan, site, rel string) string {
	for _, f := range rendered.Files {
		if f.Path == site+"/"+rel {
			return f.Content
		}
	}
	return ""
}

// endpointHealth is one entry of `etcdctl endpoint health -w json` (etcd
// v3.5.16, etcdctl/ctlv3/command/ep_command.go, epHealth).
type endpointHealth struct {
	Endpoint string `json:"endpoint"`
	Health   bool   `json:"health"`
	Error    string `json:"error"`
}

// health is nil when etcd answers healthy for every voter and every Patroni
// member is running or streaming, the cluster a site's rekey may take a
// tunnel from.
func (p *Plan) health() error {
	d := p.dep()
	if len(p.cfg.Etcd.Members) > 0 {
		at := p.transports[p.cfg.Etcd.Members[0]]
		out, err := at.Run(apply.Etcdctl(d, "endpoint health --cluster -w json"))
		var health []endpointHealth
		if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &health); jerr != nil {
			if err != nil {
				return fmt.Errorf("%s: etcd's endpoint health: %v", p.cfg.Etcd.Members[0], err)
			}
			return fmt.Errorf("%s: etcd's endpoint health is not JSON", p.cfg.Etcd.Members[0])
		}
		var sick []string
		for _, h := range health {
			if !h.Health {
				sick = append(sick, h.Endpoint)
			}
		}
		sort.Strings(sick)
		if len(sick) > 0 {
			return fmt.Errorf("etcd is unhealthy at %s", strings.Join(sick, ", "))
		}
		if len(health) < len(p.cfg.Etcd.Members) {
			return fmt.Errorf("etcd answers for %d of %d voters", len(health), len(p.cfg.Etcd.Members))
		}
	}
	if len(p.cfg.Cluster.Sites) > 0 {
		at := p.cfg.Cluster.Sites[0]
		out, err := p.transports[at].Run(d.ComposeCmd("infra") + " exec -T patroni patronictl -c /home/postgres/postgres.yml list -f json")
		if err != nil {
			return fmt.Errorf("%s: patronictl list: %v", at, err)
		}
		var members []map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &members); err != nil {
			return fmt.Errorf("%s: patronictl list is not JSON", at)
		}
		var problems []string
		for _, m := range members {
			name, _ := m["Member"].(string)
			state, _ := m["State"].(string)
			if state != "running" && state != "streaming" {
				problems = append(problems, name+" is "+state)
			}
		}
		if len(members) < len(p.cfg.Cluster.Sites) {
			problems = append(problems, fmt.Sprintf("%d of %d data sites are Patroni members", len(members), len(p.cfg.Cluster.Sites)))
		}
		if len(problems) > 0 {
			return fmt.Errorf("Patroni: %s", strings.Join(problems, "; "))
		}
	}
	return nil
}

func poll(wait, every time.Duration, check func() error) error {
	n := 1
	if every > 0 && int(wait/every) > 0 {
		n = int(wait / every)
	}
	var err error
	for i := 0; i < n; i++ {
		if err = check(); err == nil {
			return nil
		}
		if errors.Is(err, apply.ErrUnreachable) {
			return err
		}
		if i < n-1 {
			sleep(every)
		}
	}
	return err
}
