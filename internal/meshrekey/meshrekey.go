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

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-cli/internal/acme"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/meshkey"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/ui"
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
	// Relays are the sites whose traffic to each other goes through this
	// one. Its swap cuts them off from each other for a moment, so Patroni
	// is paused around it.
	Relays []string
}

// Plan is a whole run: the sites in order.
type Plan struct {
	Sites  []Site
	Report ui.Reporter
	// Joining are the sites the configuration declares whose hosts hold no
	// claim of this deployment yet, Eg: a replacement declared before an
	// eviction. They are no site's peer until the apply that brings them up,
	// so a rekey neither writes to them nor renders them.
	Joining []string

	// Finishing are the sites a run that stopped left with a next key, which
	// this run completes first whether or not they were named.
	Finishing []string
	// Resumed is an --all run picking up the sites an earlier --all run had
	// left, which the secrets file records.
	Resumed bool

	cfg        *config.Config
	store      Store
	transports map[string]apply.Transport
	all        bool
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
	return build(cfg, store, sites, false, transports)
}

// BuildAll plans the rekey of every site the configuration declares and its
// hosts run, in the configuration's order.
func BuildAll(cfg *config.Config, store Store, transports map[string]apply.Transport) (*Plan, error) {
	return build(cfg, store, nil, true, transports)
}

func build(cfg *config.Config, store Store, sites []string, all bool, transports map[string]apply.Transport) (*Plan, error) {
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
	for _, name := range cfg.SiteNames() {
		t, ok := transports[name]
		if !ok {
			return nil, fmt.Errorf("mesh rekey: no way to reach site %s", name)
		}
		if out, err := t.Run("true"); err != nil {
			return nil, fmt.Errorf("mesh rekey: %s does not answer over ssh (%v: %s). Every site is a peer of a site being rekeyed and has its mesh file changed, so every one must answer. Fix ssh and run again. Nothing was changed", name, err, strings.TrimSpace(out))
		}
	}
	secrets, err := store.Load()
	if err != nil {
		return nil, err
	}
	if p.Joining, err = meshkey.Joining(cfg, secrets, "", transports); err != nil {
		return nil, fmt.Errorf("mesh rekey: %w", err)
	}
	for _, name := range sites {
		if contains(p.Joining, name) {
			return nil, fmt.Errorf("mesh rekey: %s holds no claim of this deployment yet, so it runs no key to replace. The apply that brings it up renders the key the secrets hold. Nothing was changed", name)
		}
	}
	p.all = all
	if all {
		// An --all run that stopped resumes with the sites it had left, so a
		// site it already promoted is not rekeyed a second time.
		if remaining := secrets.MeshRekeyRemaining; len(remaining) > 0 {
			p.Resumed = true
			for _, name := range remaining {
				if _, ok := cfg.Sites[name]; ok && !contains(p.Joining, name) {
					sites = append(sites, name)
				}
			}
		} else {
			for _, name := range cfg.SiteNames() {
				if !contains(p.Joining, name) {
					sites = append(sites, name)
				}
			}
		}
	}
	// A site with a next key is part way through a rekey, and goes first,
	// named or not: until its swap is complete its peers may disagree about
	// its key, which is often why the cluster is unhealthy, and no other
	// site's rekey starts on a cluster in that state.
	var pending []string
	for _, name := range secrets.RekeyPending() {
		if contains(p.Joining, name) {
			continue
		}
		pending = append(pending, name)
		p.Finishing = append(p.Finishing, name)
	}
	var ordered []string
	for _, name := range cfg.SiteNames() {
		if contains(pending, name) {
			ordered = append(ordered, name)
		}
	}
	for _, name := range sites {
		if !contains(ordered, name) {
			ordered = append(ordered, name)
		}
	}
	sites = ordered
	p.cfg = cfg.WithoutJoining(p.Joining...)
	running, _, err := meshkey.AsRunning(secrets, p.dep(), p.transports)
	if err != nil {
		return nil, fmt.Errorf("mesh rekey: %w", err)
	}
	current, err := render.Build(p.cfg, running)
	if err != nil {
		return nil, err
	}
	// A run that starts with a new site's rekey needs a healthy cluster
	// first. One that finishes a stopped swap checks health after it, since
	// the partial swap may be what is unhealthy.
	if len(pending) == 0 && len(sites) > 0 {
		if err := p.ready(); err != nil {
			return nil, fmt.Errorf("mesh rekey: %v. A rekey takes each site's tunnels down for a moment, which a cluster already short of a member may not survive. Fix it (`paisans doctor` says how) and run again. Nothing was changed", err)
		}
	}
	for _, name := range sites {
		s := Site{Name: name, From: NewKey}
		if len(p.cfg.Cluster.Sites) > 0 {
			s.Relays = relayed(current, p.cfg, name)
		}
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

// relayed is the sites whose traffic to some other site goes through site:
// each one's mesh file routes the whole mesh subnet through site (render,
// renderWireGuard: a site with no dial endpoint relays through the first
// that has one) and lists no peer of its own for some other site. While
// site's key is swapped those sites cannot reach each other, so more than one
// site is cut off at once.
func relayed(rendered *render.Plan, cfg *config.Config, site string) []string {
	conf := cfg.Deployment().WireGuardConf()
	var out []string
	for _, name := range cfg.SiteNames() {
		if name == site {
			continue
		}
		peers := map[string]string{}
		for _, block := range strings.Split(renderedFile(rendered, name, conf), "[Peer]")[1:] {
			var peer, allowed string
			for _, line := range strings.Split(block, "\n") {
				line = strings.TrimSpace(line)
				if v, ok := strings.CutPrefix(line, "# "); ok && peer == "" {
					peer = v
				}
				if v, ok := strings.CutPrefix(line, "AllowedIPs = "); ok {
					allowed = v
				}
			}
			peers[peer] = allowed
		}
		route, ok := peers[site]
		if !ok || strings.HasSuffix(route, "/32") {
			continue
		}
		for _, other := range cfg.SiteNames() {
			if _, direct := peers[other]; other != name && other != site && !direct {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

// runningKey is the public key site's interface runs.
func (p *Plan) runningKey(site string) (string, error) {
	return meshkey.RunningKey(site, p.transports[site], p.dep())
}

// Show lists the plan: a section per site, each remaining stage pending.
func (p *Plan) Show(r ui.Reporter) {
	if p.Resumed {
		r.Note("mesh rekey: an earlier --all run stopped", "This run rekeys only the sites it had left, which the secrets file records.")
	}
	if len(p.Finishing) > 0 {
		r.Note("mesh rekey: "+strings.Join(p.Finishing, ", ")+" stopped part way", "A run that stopped left a next key, and this run finishes that rekey first.")
	}
	for _, s := range p.Sites {
		r.Section("rekey " + s.Name)
		for _, st := range p.stages(s) {
			r.Step(st.title).End(ui.Pending, "")
			r.Detail("%s", st.work)
			r.Step("gate: "+st.gate).End(ui.Pending, "")
		}
	}
}

type stageText struct {
	from              Stage
	title, work, gate string
}

func (p *Plan) stages(s Site) []stageText {
	all := []stageText{
		{NewKey, "new key for " + s.Name, "the cluster checked healthy, then a new private key, generated here, written to the secrets file as sites." + s.Name + ".wireguard_next_private_key beside the current one", "the file decrypts and holds it"},
	}
	if len(s.Relays) > 0 {
		all = append(all, stageText{Swap, "pause Patroni", fmt.Sprintf("`patronictl pause --wait`: %s reach each other only through %s, so its swap cuts them apart for a moment, which can cost etcd its quorum; paused, Patroni fails nothing over for a partition that heals in seconds", joinAnd(s.Relays), s.Name), "Patroni reports itself paused"})
	}
	all = append(all,
		stageText{Swap, "swap " + s.Name + "'s key", "every peer's mesh file with the new public key in place of the old, then " + s.Name + "'s own with the new private key, each handed to its interface with `wg syncconf`; the peers at once and " + s.Name + " last", "every peer has a handshake with " + s.Name + " under the new key"},
		stageText{Health, "check the cluster", "nothing changes", fmt.Sprintf("etcd healthy at every voter; exactly one Patroni member leading and running, and every other data site streaming; within %s", healthWait)},
	)
	if len(s.Relays) > 0 {
		all = append(all, stageText{Health, "resume Patroni", "`patronictl resume --wait`, once the cluster is healthy again", "Patroni no longer reports itself paused"})
	}
	all = append(all, stageText{Health, "promote " + s.Name + "'s key", "the next key becomes sites." + s.Name + ".wireguard_private_key in the secrets file, and the old one is deleted", "the file decrypts and holds only the new key"})
	var out []stageText
	for _, st := range all {
		if st.from >= s.From {
			out = append(out, st)
		}
	}
	return out
}

func joinAnd(list []string) string {
	if len(list) < 2 {
		return strings.Join(list, "")
	}
	return strings.Join(list[:len(list)-1], ", ") + " and " + list[len(list)-1]
}

// Execute rekeys each site in turn, every stage ending at its gate. A stage
// that fails stops the run: the next run resumes there. An --all run records
// the sites it has left in the secrets file first, and each promotion takes
// its site off the record, so a stopped --all run resumes without rekeying
// what it already finished.
func Execute(p *Plan) error {
	if p.all && !p.Resumed && len(p.Sites) > 0 {
		var names []string
		for _, s := range p.Sites {
			names = append(names, s.Name)
		}
		if err := p.record(names); err != nil {
			return fmt.Errorf("mesh rekey: recording the sites --all rekeys: %w. Nothing was changed", err)
		}
	}
	for _, s := range p.Sites {
		if err := p.rekey(s); err != nil {
			return fmt.Errorf("mesh rekey %s stopped: %w. Nothing after it ran. Fix the cause and run mesh rekey again: it resumes where this stopped. Until it finishes apply refuses: it renders the key the secrets file holds as current, which is the old one until the last stage", s.Name, err)
		}
	}
	return nil
}

// record writes the sites an --all run has left to the secrets file, and
// reads it back.
func (p *Plan) record(names []string) error {
	secrets, err := p.store.Load()
	if err != nil {
		return err
	}
	secrets.MeshRekeyRemaining = names
	if err := p.store.Save(secrets); err != nil {
		return err
	}
	if secrets, err = p.store.Load(); err != nil || strings.Join(secrets.MeshRekeyRemaining, " ") != strings.Join(names, " ") {
		return errors.Join(err, fmt.Errorf("the secrets file does not hold the sites it was written with"))
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
		// Only before a site's rekey starts: once it has, its own swap is
		// finished first, whatever the cluster's health.
		step := r.Step("check the cluster before " + s.Name + "'s rekey")
		if err := p.ready(); err != nil {
			step.Fail(err)
			return fmt.Errorf("%v. A rekey takes the site's tunnels down for a moment, which a cluster already short of a member may not survive; nothing of %s's rekey has started. Fix it (`paisans doctor` says how)", err, s.Name)
		}
		step.Done("healthy")
		step = r.Step("new key for " + s.Name)
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
	// A relay whose host already runs its next key has had its swap: a run
	// that stopped after it pauses nothing, and resumes a pause it left.
	if len(s.Relays) > 0 && s.From < Health {
		if err := p.pause(s); err != nil {
			return err
		}
	}
	if err := p.swap(s.Name, secrets, newPublic); err != nil {
		if len(s.Relays) > 0 {
			err = fmt.Errorf("%w. Patroni stays paused, so a partition cannot fail it over, and the run that resumes this swap resumes Patroni after it", err)
		}
		return err
	}
	step := r.Step("check the cluster")
	if err := poll(healthWait, healthPoll, p.health); err != nil {
		step.Fail(err)
		if len(s.Relays) > 0 {
			return fmt.Errorf("gate: %w. Patroni stays paused, since resuming it before etcd has every voter back could fail it over; the next run resumes it once the cluster is healthy", err)
		}
		return fmt.Errorf("gate: %w", err)
	}
	step.Done("healthy")
	if len(s.Relays) > 0 {
		if err := p.resume(); err != nil {
			return err
		}
	}

	step = r.Step("promote " + s.Name + "'s key")
	secrets, err = p.store.Load()
	if err != nil {
		step.Fail(err)
		return err
	}
	site = secrets.Sites[s.Name]
	site.WireGuardPrivateKey, site.WireGuardNextPrivateKey = next, ""
	secrets.Sites[s.Name] = site
	secrets.MeshRekeyRemaining = without(secrets.MeshRekeyRemaining, s.Name)
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
	// Every other site is rendered with the key its host runs, so a peer
	// part way through a rekey of its own keeps it (meshkey).
	swapped, _, err := meshkey.AsRunning(secrets, p.dep(), p.transports)
	if err != nil {
		return err
	}
	s := swapped.Sites[site]
	s.WireGuardPrivateKey = s.WireGuardNextPrivateKey
	swapped.Sites[site] = s
	rendered, err := render.Build(p.cfg, swapped)
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
	if err := apply.SyncMesh(sp, t); err != nil {
		return fmt.Errorf("%s: %s: %w", name, apply.WireGuardSync.Title(p.dep()), err)
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

// health is nil when etcd answers healthy at every voter, and Patroni has
// exactly one member leading and running and every other data site
// streaming: the cluster a site's rekey may take a tunnel from. A paused
// Patroni still reports its roles and states, so this reads the same during
// a relay's swap.
func (p *Plan) health() error { return ClusterHealth(p.cfg, p.transports) }

// ClusterHealth is nil when etcd answers healthy at every voter cfg declares,
// and Patroni has exactly one member leading and running and every other
// data site streaming. It is the gate mesh rekey holds every site's rekey
// to, and `secrets rotate` refuses a run on a cluster that fails it, since
// a password changed on a limping cluster can leave a member unable to
// rejoin. transports reaches the first etcd voter and the first data site;
// it only reads. A deployment that declares neither is healthy.
func ClusterHealth(cfg *config.Config, transports map[string]apply.Transport) error {
	d := cfg.Deployment()
	if len(cfg.Etcd.Members) > 0 {
		at := transports[cfg.Etcd.Members[0]]
		if at == nil {
			return fmt.Errorf("%s: no way to reach it to read etcd's health", cfg.Etcd.Members[0])
		}
		out, err := at.Run(apply.Etcdctl(d, "endpoint health --cluster -w json"))
		var health []endpointHealth
		if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &health); jerr != nil {
			if err != nil {
				return fmt.Errorf("%s: etcd's endpoint health: %v", cfg.Etcd.Members[0], err)
			}
			return fmt.Errorf("%s: etcd's endpoint health is not JSON", cfg.Etcd.Members[0])
		}
		var sick, missing []string
		for _, h := range health {
			if !h.Health {
				sick = append(sick, h.Endpoint)
			}
		}
		sort.Strings(sick)
		if len(sick) > 0 {
			return fmt.Errorf("etcd is unhealthy at %s", strings.Join(sick, ", "))
		}
		for _, name := range cfg.Etcd.Members {
			at := "//" + cfg.Sites[name].Address + ":"
			found := false
			for _, h := range health {
				found = found || strings.Contains(h.Endpoint, at)
			}
			if !found {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("etcd answers for no endpoint of the voter %s", strings.Join(missing, ", "))
		}
	}
	if len(cfg.Cluster.Sites) > 0 {
		at := cfg.Cluster.Sites[0]
		if transports[at] == nil {
			return fmt.Errorf("%s: no way to reach it to read Patroni's members", at)
		}
		out, err := transports[at].Run(d.ComposeCmd("infra") + " exec -T patroni patronictl -c /home/postgres/postgres.yml list -f json")
		if err != nil {
			return fmt.Errorf("%s: patronictl list: %v", at, err)
		}
		var members []map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &members); err != nil {
			return fmt.Errorf("%s: patronictl list is not JSON", at)
		}
		// Role and State are Patroni v4.1.0's (patroni/ctl.py,
		// output_members): the leader is "Leader" and "running", and a
		// replica that replicates is "streaming".
		var problems, leaders []string
		listed := map[string]bool{}
		for _, m := range members {
			name, _ := m["Member"].(string)
			role, _ := m["Role"].(string)
			state, _ := m["State"].(string)
			listed[name] = true
			switch {
			case role == "Leader":
				leaders = append(leaders, name)
				if state != "running" {
					problems = append(problems, "the leader "+name+" is "+state)
				}
			case state != "streaming":
				problems = append(problems, name+" is "+state+", not streaming")
			}
		}
		if len(leaders) != 1 {
			problems = append(problems, fmt.Sprintf("%d members lead, not one", len(leaders)))
		}
		for _, name := range cfg.Cluster.Sites {
			if !listed[name] {
				problems = append(problems, name+" is not a member")
			}
		}
		if len(problems) > 0 {
			return fmt.Errorf("Patroni: %s", strings.Join(problems, "; "))
		}
	}
	return nil
}

// ready is health, and Patroni not paused. A rekey pauses Patroni only
// around a relay's swap and resumes it after; one paused otherwise is someone
// else's maintenance, and a new site's rekey does not start under it.
func (p *Plan) ready() error {
	if err := p.health(); err != nil {
		return err
	}
	paused, err := p.paused()
	if err != nil {
		return err
	}
	if paused {
		return fmt.Errorf("Patroni is paused (maintenance mode), and this run did not pause it. Resume it with `patronictl resume` once whatever paused it is done")
	}
	return nil
}

// patronictl runs patronictl inside the first data site's Patroni container.
func (p *Plan) patronictl(args string) (string, error) {
	at := p.cfg.Cluster.Sites[0]
	out, err := p.transports[at].Run(p.dep().ComposeCmd("infra") + " exec -T patroni patronictl -c /home/postgres/postgres.yml " + args)
	if err != nil {
		return out, fmt.Errorf("%s: patronictl %s: %w: %s", at, args, err, strings.TrimSpace(out))
	}
	return out, nil
}

// paused is whether Patroni is in maintenance mode. `patronictl pause` sets
// pause: true in the cluster's dynamic configuration, which `patronictl
// show-config` prints, and `resume` clears it (Patroni v4.1.0, patroni/ctl.py,
// toggle_pause).
func (p *Plan) paused() (bool, error) {
	if len(p.cfg.Cluster.Sites) == 0 {
		return false, nil
	}
	out, err := p.patronictl("show-config")
	if err != nil {
		return false, err
	}
	var live map[string]any
	if err := yaml.Unmarshal([]byte(out), &live); err != nil {
		return false, fmt.Errorf("%s: patronictl show-config is not YAML: %w", p.cfg.Cluster.Sites[0], err)
	}
	return fmt.Sprint(live["pause"]) == "true", nil
}

// pause puts Patroni in maintenance mode before a relay's swap. The swap cuts
// the sites that relay through it off from each other for a moment, which can
// cost etcd its quorum; a leader that cannot renew its key would then demote
// itself and another member could be promoted, for a partition that heals in
// seconds. Paused, Patroni leaves the roles as they are. `--wait` returns
// once every member has seen it (Patroni v4.1.0, patroni/ctl.py,
// wait_until_pause_is_applied), and the pause is read back, since patronictl
// reports a failed request without failing. A cluster already paused, by a
// run of this relay's rekey that stopped, is left paused.
func (p *Plan) pause(s Site) error {
	step := p.reporter().Step("pause Patroni for " + s.Name + "'s swap")
	paused, err := p.paused()
	if err != nil {
		step.Fail(err)
		return err
	}
	if !paused {
		if _, err := p.patronictl("pause --wait"); err != nil {
			step.Fail(err)
			return err
		}
		if paused, err = p.paused(); err != nil || !paused {
			err = errors.Join(err, fmt.Errorf("Patroni does not report itself paused after `patronictl pause`"))
			step.Fail(err)
			return err
		}
	}
	step.Done(strings.Join(s.Relays, ", ") + " relay through " + s.Name)
	return nil
}

// resume takes Patroni out of maintenance mode once the swap is done, every
// peer has a handshake under the new key and the cluster is healthy again.
func (p *Plan) resume() error {
	step := p.reporter().Step("resume Patroni")
	paused, err := p.paused()
	if err == nil && !paused {
		step.Done("not paused")
		return nil
	}
	if err == nil {
		if _, err = p.patronictl("resume --wait"); err == nil {
			if paused, err = p.paused(); err == nil && paused {
				err = fmt.Errorf("Patroni still reports itself paused after `patronictl resume`")
			}
		}
	}
	if err != nil {
		step.Fail(err)
		return fmt.Errorf("%w. Patroni is paused and does not fail over until it is resumed: run mesh rekey again, or `patronictl resume`", err)
	}
	step.Done("resumed")
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

func without(list []string, s string) []string {
	var out []string
	for _, item := range list {
		if item != s {
			out = append(out, item)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
