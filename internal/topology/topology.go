// Package topology classifies a deployment's declared sites at the start of
// every run, once: ready, pending or down, from what the deployment says has
// joined, never from the site's own host alone, since a host that evicted the
// deployment, one that is down and one that never admitted it look alike from
// outside. A run renders the effective topology, the declaration less its
// pending sites, and runs on the sites it has. See the spec's "A deployment
// with sites missing" (docs/specs/2026-10-15-rootless-and-host-admit.md).
package topology

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/progname"
)

// Class is what a site is to this run.
type Class int

const (
	// Ready is a site that answers with an admission covering its roles.
	// It is not the zero Class, so a site no classification names is never
	// taken for ready.
	Ready Class = iota + 1
	// Pending is a site the deployment has not joined that cannot be used
	// yet: it is left out of the topology, as if not yet added.
	Pending
	// Down is a joined site that does not let the run in: it is kept as
	// declared, a peer and a member, and only its own steps are skipped.
	Down
)

func (c Class) String() string {
	switch c {
	case Ready:
		return "ready"
	case Pending:
		return "pending"
	case Down:
		return "down"
	}
	return "unclassified"
}

// Site is one declared site's class, and for a pending or down one, why.
type Site struct {
	Name  string
	Class Class
	Why   string
	// Request is a pending site whose own host has to admit this
	// deployment: its login or its admission is why it is pending, so its
	// host's owner is sent a request. A site pending for another reason,
	// Eg: its only route into the mesh is through a pending site, may be
	// admitted already.
	Request bool
}

// RecordRead is a site's admission record as read, or why it was not.
type RecordRead struct {
	Record admission.Record
	Err    error
}

// Evidence is what a run read about its sites. Gather reads it; Classify
// judges it.
type Evidence struct {
	// Login is each site's answer to its login, nil when it let the run in.
	Login map[string]error
	// Records is the admission record of each site that answered.
	Records map[string]RecordRead
	// Joined is each source of evidence that was read, by name, and the
	// sites it lists as part of the deployment.
	Joined map[string][]string
	// Unread is each source that could not be read, with why in its name,
	// and the sites it could have listed.
	Unread []Source
	// DeployedApps is the apps the deployment record lists as deployed.
	DeployedApps []string
}

// Source is a source of evidence that could not be read. Sites is the
// sites it could have listed, nil for one that can list any declared site:
// only those sites are held back by its silence.
type Source struct {
	Name  string
	Sites []string
}

// Classification is the run's view of its sites.
type Classification struct {
	Sites map[string]Site
	// Held is the apps left out of the run because Garage has fewer nodes
	// than its replication, sorted.
	Held []string
	// Warnings are what the run says about what it holds back.
	Warnings []string

	// joined and deployed are what Classify read, kept so that Wait can
	// judge again what hangs on the pending set.
	joined   map[string]bool
	deployed []string
}

// DownLine is a down site's line. It cannot say whether the host is down or
// its owner evicted the deployment, so it says both.
func DownLine(site string) string {
	return fmt.Sprintf("%[1]s: its login is refused or it does not answer: the host is down, or its owner evicted this deployment. %[2]s site remove --site %[1]s --host-gone removes it from the deployment.", site, progname.Cmd())
}

// Classify judges ev. It refuses only where a site's class cannot be told
// safely: a site that would be pending because nothing lists it while
// evidence could not be read, a joined site whose admission no longer
// covers it or whose every route into the mesh is pending, and an app the
// deployment runs that Garage could not hold.
func Classify(cfg *config.Config, ev Evidence) (*Classification, error) {
	joined := map[string]bool{}
	c := &Classification{Sites: map[string]Site{}, joined: joined, deployed: ev.DeployedApps}
	for _, sites := range ev.Joined {
		for _, s := range sites {
			joined[s] = true
		}
	}
	// pending is the one way a site becomes pending: nothing that was read
	// lists it. While a source that could have listed it could not be read,
	// that silence is not evidence, so the run stops rather than drop a site
	// that may have joined. A source that could never list the site, etcd
	// for a site that is no voter, holds nothing back.
	pending := func(name, why string, request bool) error {
		var unread []string
		for _, src := range ev.Unread {
			if src.Sites == nil || slices.Contains(src.Sites, name) {
				unread = append(unread, src.Name)
			}
		}
		if len(unread) > 0 {
			return fmt.Errorf("%s, and whether it has joined the deployment cannot be told: %s could not be read. A joined site left out of the mesh and the cluster would be cut off from them, so the run stops. Run it again once it can be read", why, strings.Join(unread, ", and "))
		}
		c.Sites[name] = Site{Name: name, Class: Pending, Why: why, Request: request}
		return nil
	}
	for _, name := range cfg.SiteNames() {
		if err := ev.Login[name]; err != nil {
			if joined[name] {
				c.Sites[name] = Site{Name: name, Class: Down, Why: DownLine(name)}
				continue
			}
			if err := pending(name, fmt.Sprintf("%s: its login is refused or it does not answer (%v), and the deployment has not joined it: its host's owner has not admitted it yet", name, err), true); err != nil {
				return nil, err
			}
			continue
		}
		rr := ev.Records[name]
		var why string
		switch {
		case errors.Is(rr.Err, admission.ErrNotAdmitted):
			why = "its host has not admitted this deployment"
		case rr.Err != nil:
			why = "its admission record cannot be read: " + rr.Err.Error()
		default:
			if diff := rr.Record.Differences(cfg, name); len(diff) > 0 {
				why = "its admission does not cover it: " + strings.Join(diff, "; ")
			}
		}
		switch {
		case why == "":
			c.Sites[name] = Site{Name: name, Class: Ready}
		case joined[name]:
			return nil, fmt.Errorf("%s has joined this deployment, and %s. The host's owner admits it again (sudo %s site admit --request %s.admit, from %s site request --site %s > %s.admit) before the run goes on", name, why, progname.Default, name, progname.Cmd(), name, name)
		default:
			if err := pending(name, name+": "+why, true); err != nil {
				return nil, err
			}
		}
	}
	relayed := func(name, why string) error { return pending(name, why, false) }
	if err := c.relays(cfg, joined, relayed); err != nil {
		return nil, err
	}
	if err := c.garage(cfg, ev.DeployedApps); err != nil {
		return nil, err
	}
	return c, nil
}

// relays makes pending each site that dials out only and whose every route
// into the mesh, a site with a dial endpoint, is pending. A joined site in
// that place refuses the run instead, since the run never drops a joined
// site from the mesh. One pass is enough: only sites without an endpoint
// change, and only sites with one are routes.
func (c *Classification) relays(cfg *config.Config, joined map[string]bool, pending func(name, why string) error) error {
	var endpoints, live []string
	for _, name := range cfg.SiteNames() {
		if cfg.Sites[name].DialEndpoint() != "" {
			endpoints = append(endpoints, name)
			if c.Sites[name].Class != Pending {
				live = append(live, name)
			}
		}
	}
	if len(endpoints) == 0 || len(live) > 0 {
		return nil
	}
	through := endpoints[0] + ", which is pending"
	if len(endpoints) > 1 {
		through = strings.Join(endpoints, ", ") + ", each of which is pending"
	}
	for _, name := range cfg.SiteNames() {
		s := c.Sites[name]
		if s.Class == Pending || cfg.Sites[name].DialEndpoint() != "" {
			continue
		}
		why := fmt.Sprintf("%s: its only route into the mesh is through %s", name, through)
		if joined[name] {
			if s.Class == Down {
				continue
			}
			return fmt.Errorf("%s has joined this deployment, and its only route into the mesh is through %s. The run stops rather than cut it off; admit one of them first", name, through)
		}
		if err := pending(name, why); err != nil {
			return err
		}
	}
	return nil
}

// garage holds back storage when the Garage nodes the run keeps, ready and
// down, number fewer than its replication: every app the declaration keeps in
// object storage is held, so none is ever rendered with local storage, and
// one already deployed refuses the run, since its objects would be left
// behind.
func (c *Classification) garage(cfg *config.Config, deployed []string) error {
	g := cfg.Storage.Garage
	if len(g.Sites) == 0 {
		return nil
	}
	kept := 0
	for _, s := range g.Sites {
		if c.Sites[s].Class != Pending {
			kept++
		}
	}
	if kept >= g.Replication {
		return nil
	}
	for _, name := range cfg.AppNames() {
		if blueprints.InObjectStorage(cfg, cfg.Apps[name]) {
			if slices.Contains(deployed, name) {
				return fmt.Errorf("garage has %d of its sites in the run and keeps %d copies, so it cannot take objects, and %s keeps its uploads there and is deployed. The run stops rather than run %s without its storage. Admit the pending Garage sites first", kept, g.Replication, name, name)
			}
			c.Held = append(c.Held, name)
		}
	}
	if len(c.Held) > 0 {
		c.Warnings = append(c.Warnings, fmt.Sprintf("storage held back: Garage has %d of its sites in the run and keeps %d copies, so %s wait until the pending Garage sites are admitted", kept, g.Replication, strings.Join(c.Held, ", ")))
	}
	return nil
}

// Wait makes each of names pending for this run, each a ready site the
// deployment has not joined whose join cannot go on, with why, Eg: a data
// site whose site add waits until every joined site answers. It is left out
// of the topology as a pending site is, and what hangs on the pending set,
// the routes into the mesh and the storage held back, is judged again.
func (c *Classification) Wait(cfg *config.Config, names []string, why string) error {
	for _, name := range names {
		switch {
		case c.joined[name]:
			return fmt.Errorf("%s cannot wait to join: it has joined this deployment", name)
		case !c.Ready(name):
			return fmt.Errorf("%s cannot wait to join: it is %s", name, c.Sites[name].Class)
		}
		c.Sites[name] = Site{Name: name, Class: Pending, Why: name + ": " + why}
	}
	pending := func(name, why string) error {
		c.Sites[name] = Site{Name: name, Class: Pending, Why: why}
		return nil
	}
	if err := c.relays(cfg, c.joined, pending); err != nil {
		return err
	}
	c.Held, c.Warnings = nil, nil
	return c.garage(cfg, c.deployed)
}

func (c *Classification) of(class Class) []string {
	var out []string
	for name, s := range c.Sites {
		if s.Class == class {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// Pending is the pending sites, sorted.
func (c *Classification) Pending() []string { return c.of(Pending) }

// Down is the down sites, sorted.
func (c *Classification) Down() []string { return c.of(Down) }

// Degraded reports whether the run is without any declared site.
func (c *Classification) Degraded() bool { return len(c.Pending())+len(c.Down()) > 0 }

// Ready reports whether site is ready.
func (c *Classification) Ready(site string) bool { return c.Sites[site].Class == Ready }

// StorageHeld reports whether the run holds back storage.
func (c *Classification) StorageHeld() bool { return len(c.Held) > 0 }

// Effective is the topology the run renders: cfg less its pending sites and
// the apps pinned to them, and less the apps held back. The declaration is
// not changed.
func (c *Classification) Effective(cfg *config.Config) *config.Config {
	out := cfg.WithoutJoining(c.Pending()...)
	if len(c.Held) == 0 {
		return out
	}
	copied := *out
	copied.Apps = map[string]config.App{}
	for name, app := range out.Apps {
		if !slices.Contains(c.Held, name) {
			copied.Apps[name] = app
		}
	}
	return &copied
}

// Refuse is what stops the run: the gateway pending or down, no ready data
// site while an app needs a database, etcd without a quorum among its
// members that answer, a Patroni cluster none of whose members answer, and
// site naming a site that is pending or down.
func (c *Classification) Refuse(cfg *config.Config, site string) error {
	if site != "" {
		if _, ok := cfg.Sites[site]; !ok {
			return fmt.Errorf("--site %s: no site of that name is declared", site)
		}
		if !c.Ready(site) {
			return fmt.Errorf("--site %s: %s", site, c.Sites[site].Why)
		}
	}
	for _, gw := range cfg.GatewaySites() {
		if !c.Ready(gw) {
			return fmt.Errorf("the gateway %s is %s, and every public hostname goes through it: %s", gw, c.Sites[gw].Class, c.Sites[gw].Why)
		}
	}
	eff := c.Effective(cfg)
	needsDB := slices.ContainsFunc(eff.AppNames(), func(name string) bool { return blueprints.UsesPostgres(eff.Apps[name].Blueprint) })
	if needsDB && !slices.ContainsFunc(cfg.DataSites(), c.Ready) {
		return fmt.Errorf("no data site is ready, and %s need a database: %s", strings.Join(dbApps(eff), ", "), strings.Join(c.whys(cfg.DataSites()), "; "))
	}
	if members := eff.Etcd.Members; len(members) > 0 {
		ready := 0
		for _, m := range members {
			if c.Ready(m) {
				ready++
			}
		}
		if ready <= len(members)/2 {
			return fmt.Errorf("etcd has %d of %d members ready, and needs %d for a quorum: %s", ready, len(members), len(members)/2+1, strings.Join(c.whys(members), "; "))
		}
	}
	if sites := eff.Cluster.Sites; len(sites) > 0 && !slices.ContainsFunc(sites, c.Ready) {
		return fmt.Errorf("no member of the Patroni cluster is ready: %s", strings.Join(c.whys(sites), "; "))
	}
	return nil
}

func (c *Classification) whys(sites []string) []string {
	var out []string
	for _, s := range sites {
		if w := c.Sites[s].Why; w != "" {
			out = append(out, w)
		}
	}
	return out
}

func dbApps(cfg *config.Config) []string {
	var out []string
	for _, name := range cfg.AppNames() {
		if blueprints.UsesPostgres(cfg.Apps[name].Blueprint) {
			out = append(out, name)
		}
	}
	return out
}
