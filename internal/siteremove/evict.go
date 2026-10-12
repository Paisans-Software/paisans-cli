package siteremove

// `site remove --evict`: a site whose host still runs and that the other
// admins can no longer reach. Every cluster service trusts the mesh and
// nothing else, so being a WireGuard peer of the remaining sites is being in
// the cluster, and cutting the peer is what evicts. The cut comes first, and
// everything after it follows from the cut: Patroni fails over by itself once
// the cut leader cannot renew its key, etcd's member is removed by the
// remaining voters, and Garage's layout is told to stop waiting for a node
// that will never answer. The host is never reached. docs/specs/
// 2026-10-11-site-evict.md is the approved design.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/dns"
	"github.com/paisans-software/paisans-stack/internal/registry"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/storageadd"
)

// Stage numbers of an eviction after the cut and the DNS stage. The report
// that closes it is Remains, not a stage: it changes nothing.
const (
	evictDatabase = 3
	evictEtcd     = 4
	evictStorage  = 5
	evictRest     = 6
	evictConfig   = 7
	evictMonitor  = 8
)

var (
	// A cut leader's key expires with Patroni's ttl, 30 seconds by default,
	// and a remaining member promotes itself within a loop_wait, 10 seconds
	// by default, after that (Patroni v4.1.0, patroni/config.py,
	// __DEFAULT_CONFIG). The wait leaves room for both several times over.
	failoverWait = 3 * time.Minute
	failoverPoll = 5 * time.Second

	// A remaining site hands its interface the new mesh file at once; the
	// gate reads each one once.
	evictTimeout = 2 * time.Minute
)

// evictState is what an eviction read besides what every removal reads.
type evictState struct {
	// joining are the sites the configuration declares whose hosts hold no
	// claim of this deployment yet: a replacement declared in the same edit,
	// which the eviction neither dials nor gates, and `apply` brings up.
	joining map[string]bool
	// key is the evicted site's WireGuard public key.
	key string
	// stillPeers is, for each remaining site, whether its running interface
	// lists the key.
	stillPeers map[string]bool
	// cutDone is whether no remaining site's interface lists the key, which
	// a run that already cut finds.
	cutDone bool
	// cutRendered is the end state rendered without the joining sites, which
	// the cut writes: it must not wait on a host or a DNS record that does not
	// exist yet.
	cutRendered *render.Plan
	// failover is whether the site holds the Patroni leader.
	failover bool
	// loseWrites is the most current replica, when the site leads and no
	// remaining member is a streaming Sync Standby.
	loseWrites *Lag
	// loseObjects is whether objects only the evicted Garage node held are
	// lost when it is skipped.
	loseObjects bool
	// multiVersion is whether Garage's layout history has more than one live
	// version, read when the node has already left the layout.
	multiVersion bool
	// gateway is whether the site holds the gateway role; tokenUnchecked says
	// why its DNS token could not be checked, empty when it was.
	gateway        bool
	tokenUnchecked string
	// dark names what answers nothing between the eviction and the apply that
	// brings up the replacement, empty when something still answers.
	dark []string
}

// Lag is the most current remaining replica of a leader with no streaming
// Sync Standby, and how far behind it is in MB, when Patroni says.
type Lag struct {
	Replica string
	MB      float64
	Known   bool
}

// Joining is the sites an eviction leaves alone because their hosts hold no
// claim of this deployment yet, sorted.
func (p *Plan) Joining() []string {
	var out []string
	for name := range p.evict.joining {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// LoseWrites is the replica a failover would promote when the evicted site
// leads and no remaining member is a streaming Sync Standby, nil otherwise.
// The operator types `lose writes` before the eviction goes on.
func (p *Plan) LoseWrites() *Lag { return p.evict.loseWrites }

// LoseObjects is whether skipping the evicted Garage node loses objects only
// it held. The operator types `lose objects` before the eviction goes on.
func (p *Plan) LoseObjects() bool { return p.evict.loseObjects }

// Gateway is whether the evicted site holds the gateway role, whose DNS token
// the operator confirms revoked by typing `revoked`.
func (p *Plan) Gateway() bool { return p.evict.gateway }

// live is list without the joining sites.
func (p *Plan) live(list []string) []string {
	if len(p.evict.joining) == 0 {
		return list
	}
	var out []string
	for _, name := range list {
		if !p.evict.joining[name] {
			out = append(out, name)
		}
	}
	return out
}

// liveSites is every remaining site but the joining ones.
func (p *Plan) liveSites() []string { return p.live(p.end.SiteNames()) }

// liveEnd is the end state without the joining sites, and without the apps
// pinned to them, which run nowhere until the apply that brings them up.
func (p *Plan) liveEnd() *config.Config {
	joining := p.Joining()
	if len(joining) == 0 {
		return p.end
	}
	end := p.end
	for _, name := range joining {
		end = end.WithoutSite(name)
	}
	apps := map[string]config.App{}
	for name, app := range end.Apps {
		apps[name] = app
	}
	for _, site := range joining {
		for _, name := range p.end.PinnedTo(site) {
			delete(apps, name)
		}
	}
	end.Apps = apps
	return end
}

// JoiningSites reads, on every site cfg declares but site, whether its host
// holds this deployment's claim, and returns the ones that do not. A host
// that does not answer is an error: whether it is new cannot be told.
func JoiningSites(cfg *config.Config, site string, transports map[string]apply.Transport) ([]string, error) {
	var out []string
	for _, name := range cfg.SiteNames() {
		if name == site {
			continue
		}
		t, ok := transports[name]
		if !ok {
			return nil, fmt.Errorf("site remove %s: no way to reach site %s", site, name)
		}
		reg, err := registry.Read(t)
		if err != nil {
			if errors.Is(err, apply.ErrUnreachable) {
				return nil, fmt.Errorf("site remove %s: %s does not answer over ssh (%v). Every remaining site is read, and a site declared for the first time is read to see that its host holds no claim yet. Fix ssh and run again. Nothing was changed", site, name, err)
			}
			return nil, fmt.Errorf("site remove %s: %w", site, err)
		}
		if _, ok := reg.Deployments[cfg.ID]; !ok {
			out = append(out, name)
		}
	}
	return out, nil
}

// readJoining finds the joining sites, and takes them out of the transports,
// so that nothing in the eviction reaches them.
func (p *Plan) readJoining() error {
	names, err := JoiningSites(p.cfg, p.Site, p.transports)
	if err != nil {
		return err
	}
	p.evict.joining = map[string]bool{}
	for _, name := range names {
		p.evict.joining[name] = true
		delete(p.transports, name)
	}
	return nil
}

// probeEvict reads what only an eviction needs before the cluster is probed:
// the site's key on every remaining interface, the relays the remaining sites
// need, and the gateway's DNS token.
func (p *Plan) probeEvict() error {
	key, err := render.PublicKey(p.secrets.Sites[p.Site].WireGuardPrivateKey)
	if err != nil {
		return fmt.Errorf("secrets sites.%s.wireguard_private_key: %w", p.Site, err)
	}
	p.evict.key = key
	if err := p.refuseLostRelay(); err != nil {
		return err
	}
	p.evict.gateway = p.cfg.Sites[p.Site].Has(config.RoleGateway)
	if p.evict.gateway {
		if err := p.checkToken(); err != nil {
			return err
		}
	}
	p.evict.stillPeers = map[string]bool{}
	p.evict.cutDone = true
	for _, name := range p.liveSites() {
		listed, err := p.listsPeer(name, key)
		if err != nil {
			return fmt.Errorf("site remove %s: %w", p.Site, err)
		}
		p.evict.stillPeers[name] = listed
		p.evict.cutDone = p.evict.cutDone && !listed
	}
	if p.evict.cutRendered, err = p.render(p.liveEnd()); err != nil {
		return err
	}
	for _, role := range []config.Role{config.RoleGateway, config.RoleApps} {
		if !p.cfg.Sites[p.Site].Has(role) {
			continue
		}
		var still []string
		for _, name := range p.liveSites() {
			if p.end.Sites[name].Has(role) {
				still = append(still, name)
			}
		}
		if len(still) == 0 {
			p.evict.dark = append(p.evict.dark, string(role))
		}
	}
	return nil
}

// listsPeer reports whether site's running interface lists key as a peer.
// `wg show <interface> peers` prints one public key per line (wireguard-tools,
// wg(8)).
func (p *Plan) listsPeer(site, key string) (bool, error) {
	out, err := p.transports[site].Run("wg show " + p.dep().Interface() + " peers")
	if err != nil {
		return false, fmt.Errorf("%s: `wg show %s peers`: %w: %s", site, p.dep().Interface(), err, lastLines(out, 2))
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == key {
			return true, nil
		}
	}
	return false, nil
}

// refuseLostRelay refuses an eviction that would cut a remaining site off
// the mesh along with the evicted one.
func (p *Plan) refuseLostRelay() error {
	return lostRelay(p.cfg, p.Site, p.end, p.liveSites())
}

// RefuseLostRelay is refuseLostRelay for cfg as it stands, every remaining
// site counted as running.
func RefuseLostRelay(cfg *config.Config, site string) error {
	end := cfg.WithoutSite(site)
	return lostRelay(cfg, site, end, end.SiteNames())
}

// lostRelay is the refusal itself. A site with no dial endpoint peers only
// with sites that have one, and relays everything else through them (render,
// renderWireGuard). When the evicted site is the only dialled site left, two
// remaining sites with no endpoint could reach nothing.
func lostRelay(cfg *config.Config, site string, end *config.Config, live []string) error {
	if cfg.Sites[site].DialEndpoint() == "" {
		return nil
	}
	var dialled, undialled []string
	for _, name := range live {
		if end.Sites[name].DialEndpoint() != "" {
			dialled = append(dialled, name)
		} else {
			undialled = append(undialled, name)
		}
	}
	if len(dialled) > 0 || len(undialled) < 2 {
		return nil
	}
	return fmt.Errorf("site remove %s: %s reach the mesh only through %s, the only remaining site with a dial endpoint, as their relay, so cutting it cuts them too. Give one of them an endpoint (or a public_address) and apply it first, then run this again. Nothing was changed", site, joinAnd(undialled), site)
}

// checkToken checks, before anything changes, that the DNS token in the
// secrets file works against the provider: the evicted gateway holds the one
// it was rendered, and the operator replaces it with `secrets set` after
// revoking it. A token that does not work is most often the revoked one,
// still in the file, and the DNS stage could not delete the records with it.
func (p *Plan) checkToken() error {
	name := p.cfg.ACME.Provider
	token := ""
	if p.secrets != nil {
		token = p.secrets.External["acme_dns_token"]
	}
	switch {
	case name == "":
		p.evict.tokenUnchecked = "no DNS provider is declared"
		return nil
	case !contains(dns.Implemented(), name):
		p.evict.tokenUnchecked = name + " record management is not implemented"
		return nil
	case strings.TrimSpace(token) == "":
		p.evict.tokenUnchecked = "the secrets hold no DNS token"
		return nil
	}
	providerFor := p.DNSProvider
	if providerFor == nil {
		providerFor = dns.For
	}
	provider, err := providerFor(name, token)
	if err != nil {
		return fmt.Errorf("site remove %s: %w", p.Site, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout)
	defer cancel()
	if _, err := dns.FindZone(ctx, provider, strings.TrimSuffix(strings.ToLower(strings.TrimSpace(p.cfg.Community.Domain)), "."), nil); err != nil {
		return fmt.Errorf("site remove %s: the DNS token in the secrets file does not work against %s: %v. %s held the token as it was rendered, so it is revoked at %s and a new one set before the eviction: paisans secrets set external.acme_dns_token < token-file. Nothing was changed", p.Site, name, err, p.Site, name)
	}
	return nil
}

// evictQuorum refuses when the remaining healthy voters are not a majority
// of the voters etcd has now: `member remove` is a change the current voters
// commit, the evicted one counted, and it cannot answer.
func (p *Plan) evictQuorum(members []apply.EtcdMember, up int) error {
	voters := 0
	for _, m := range members {
		if !m.IsLearner {
			voters++
		}
	}
	if voters == 0 || up*2 > voters {
		return nil
	}
	return fmt.Errorf("site remove %s: without it, %d of etcd's %d voters are healthy, which is not a majority of the voters etcd has now, so removing its member could not commit. Recovery is restoring etcd from a remaining member, outside the toolkit. Nothing was changed", p.Site, up, voters)
}

// evictLeader decides how the leadership leaves the evicted site. A streaming
// Sync Standby takes it over losing nothing. Without one, the most current
// replica is promoted, and any commit the evicted leader acknowledged after
// that replica's last received WAL is lost, which the operator accepts by
// typing `lose writes`. The cut leader steps down on its own: it can no
// longer renew its leader key.
func (p *Plan) evictLeader() {
	p.evict.failover = true
	live := p.live(p.end.Cluster.Sites)
	for _, m := range p.patroni.members {
		if m.role() == "Sync Standby" && m.state() == "streaming" && contains(live, m.name()) {
			p.patroni.candidate = m.name()
			return
		}
	}
	var best *Lag
	for _, m := range p.patroni.members {
		if m.name() == p.Site || !contains(live, m.name()) {
			continue
		}
		mb, known := receivedLag(m)
		lag := &Lag{Replica: m.name(), MB: mb, Known: known}
		switch {
		case best == nil:
			best = lag
		case known && (!best.Known || mb < best.MB):
			best = lag
		}
	}
	if best == nil {
		best = &Lag{}
	}
	p.evict.loseWrites = best
}

// receivedLag is how far a replica is behind in received WAL, in MB, which
// bounds what a failover to it loses. Patroni 4 splits the old "Lag in MB"
// column into receive and replay lag; the older column is still read, and
// replay lag, never smaller, stands in when neither is there.
func receivedLag(m member) (float64, bool) {
	for _, key := range []string{"Receive Lag", "Lag in MB", "Replay Lag"} {
		if v, ok := m[key].(float64); ok {
			return v, true
		}
	}
	return 0, false
}

// probeDeadNode reads what skipping the evicted Garage node needs. Objects
// are kept when the remaining nodes still hold a copy of every partition:
// replication of 2 or more, and at least that many nodes remaining.
func (p *Plan) probeDeadNode(t apply.Transport, nodes int) error {
	r := p.cfg.Storage.Garage.Replication
	p.evict.loseObjects = !(r >= 2 && nodes >= r)
	if p.garage.node != "" {
		return nil
	}
	out, err := t.Run(gcmd(p, "layout history"))
	if err != nil {
		return fmt.Errorf("site remove %s: %s: `garage layout history`: %w: %s", p.Site, p.garage.anchor, err, lastLines(out, 3))
	}
	p.evict.multiVersion = !strings.Contains(out, storageadd.StableLayout)
	return nil
}

// buildEvict plans the eviction's stages, in the order the cut decides.
func (p *Plan) buildEvict() error {
	cut, err := p.buildCut()
	if err != nil {
		return err
	}
	p.Stages = append(p.Stages, cut)
	dnsSt, err := p.buildDNS(p.cfg, p.end)
	if err != nil {
		return err
	}
	dnsSt.Number = 2
	p.Stages = append(p.Stages, dnsSt, p.buildEvictDatabase(), p.buildEvictEtcd(), p.buildEvictStorage())
	rest, err := p.buildRest()
	if err != nil {
		return err
	}
	p.Stages = append(p.Stages, rest)
	cfgSt := p.buildConfig()
	cfgSt.Number = evictConfig
	p.Stages = append(p.Stages, cfgSt)
	monitor, err := p.buildMonitor()
	if err != nil {
		return err
	}
	monitor.Number = evictMonitor
	p.Stages = append(p.Stages, monitor)
	return nil
}

// buildCut is stage 1: on every remaining site at once, the mesh file
// without the site and `wg syncconf`, and the gateway's routes without its
// upstreams. A site that did not take it is named, and nothing after the
// stage runs. A partial cut is safe to leave: every peer that dropped the
// key no longer talks to it, and a re-run completes it.
func (p *Plan) buildCut() (*Stage, error) {
	st := &Stage{Number: 1, Name: "cut", Short: p.Site + " is no remaining site's peer"}
	keep := func(rel string) bool { return rel == p.dep().WireGuardConf() || p.isCaddyRouting(rel) }
	changes, _, err := p.planSiteChanges(st, p.liveSites(), p.evict.cutRendered, keep, false)
	if err != nil {
		return nil, err
	}
	st.Gate = fmt.Sprintf("no remaining site lists %s's WireGuard key as a peer, and `wg show` on each has no handshake from it", p.Site)
	if len(p.evict.joining) > 0 {
		st.Gate += fmt.Sprintf("; %s, not prepared yet, is neither dialled nor gated", joinAnd(p.Joining()))
	}
	st.run = func() error {
		var failed []string
		for _, c := range changes {
			if err := p.applyChange(c); err != nil {
				p.stop(err)
				failed = append(failed, fmt.Sprintf("%s: %v", c.site, err))
			}
		}
		if len(failed) > 0 {
			return fmt.Errorf("the cut did not take on every remaining site, and every site that took it no longer talks to %s:\n  %s", p.Site, strings.Join(failed, "\n  "))
		}
		return nil
	}
	st.gate = func() error {
		if err := p.meshGate(p.evict.key); err != nil {
			return err
		}
		return p.handshakeGate(p.evict.key)
	}
	return st, nil
}

// handshakeGate passes when no remaining site's interface has a handshake
// with key. `wg show <interface> latest-handshakes` prints a public key and
// a timestamp per peer (wireguard-tools, wg(8)), so a peer that was dropped
// is not printed at all.
func (p *Plan) handshakeGate(key string) error {
	for _, name := range p.liveSites() {
		out, err := p.transports[name].Run("wg show " + p.dep().Interface() + " latest-handshakes")
		if err != nil {
			return fmt.Errorf("%s: `wg show %s latest-handshakes`: %w: %s", name, p.dep().Interface(), err, lastLines(out, 2))
		}
		for _, line := range strings.Split(out, "\n") {
			if f := strings.Fields(line); len(f) > 0 && f[0] == key {
				return fmt.Errorf("%s still has a handshake with %s", name, p.Site)
			}
		}
	}
	return nil
}

// buildEvictDatabase is stage 3: Patroni's own failover once the cut leader
// cannot renew its key, then the site's member key deleted from etcd, which
// it can no longer rewrite.
func (p *Plan) buildEvictDatabase() *Stage {
	st := &Stage{Number: evictDatabase, Name: "database", Short: "a remaining site leads, without " + p.Site}
	if !contains(p.cfg.Cluster.Sites, p.Site) {
		st.Short = "no database here"
		st.Gate = fmt.Sprintf("none: %s holds no database", p.Site)
		return st
	}
	leads := p.patroni.leader == p.Site
	if leads {
		text := fmt.Sprintf("for Patroni's failover: %s cannot renew its leader key once it is cut off, the key expires with Patroni's ttl, and a remaining member takes over by itself", p.Site)
		switch {
		case p.patroni.candidate != "":
			text += fmt.Sprintf("; the streaming Sync Standby %s takes it over, losing no acknowledged write", p.patroni.candidate)
		case p.evict.loseWrites != nil:
			text += "; " + p.loseWritesText()
		}
		st.Steps = append(st.Steps, Step{Site: p.patroni.at, Verb: "wait", Title: "wait for a remaining site to lead", Text: text})
	}
	if p.patroni.sync && len(p.live(p.end.Cluster.Sites)) < 2 {
		st.Steps = append(st.Steps, Step{Site: p.patroni.at, Verb: "set", Title: "turn synchronous mode off", Text: "synchronous_mode=false in Patroni's dynamic configuration: one data site remains, and a leader that waits for a standby that is gone stops taking writes"})
	}
	if p.patroni.listed {
		st.Steps = append(st.Steps, Step{Site: p.etcd.control, Verb: "delete", Title: "delete " + p.Site + "'s Patroni member key", Text: fmt.Sprintf("%s's member key, %s, which it can no longer rewrite once it is cut off", p.Site, p.memberKey())})
	}
	st.Gate = fmt.Sprintf("`patronictl list` on %s shows another site leading and no %s", p.patroni.at, p.Site)
	if p.endWantsSync() {
		st.Gate += ", with a Sync Standby"
	}
	st.run = func() error {
		if leads {
			p.work("wait for a remaining site to lead")
			err := poll(failoverWait, failoverPoll, func() error {
				list, err := p.patroniList()
				if err != nil {
					return err
				}
				for _, m := range list {
					if m.role() == "Leader" && m.state() == "running" && m.name() != p.Site {
						return nil
					}
				}
				return fmt.Errorf("no remaining member leads yet")
			})
			if err != nil {
				return fmt.Errorf("no remaining member took over within %s: %w. Patroni promotes no replica further behind than maximum_lag_on_failover, which `patronictl show-config` on %s says; read `patronictl list` there", failoverWait, err, p.patroni.at)
			}
		}
		if p.patroni.sync && len(p.live(p.end.Cluster.Sites)) < 2 {
			p.work("turn synchronous mode off")
			if out, err := p.transports[p.patroni.at].Run(p.patronictl("edit-config --force -q -s synchronous_mode=false -s synchronous_mode_strict=false")); err != nil {
				return fmt.Errorf("%s: patronictl edit-config: %w: %s", p.patroni.at, err, lastLines(out, 3))
			}
		}
		if p.patroni.listed {
			p.work("delete " + p.Site + "'s Patroni member key")
			if out, err := p.transports[p.etcd.control].Run(apply.Etcdctl(p.dep(), "del "+p.memberKey())); err != nil {
				return fmt.Errorf("%s: deleting %s: %w: %s", p.etcd.control, p.memberKey(), err, lastLines(out, 3))
			}
		}
		return nil
	}
	st.gate = p.patroniGate
	return st
}

// loseWritesText says what a failover with no Sync Standby loses.
func (p *Plan) loseWritesText() string {
	l := p.evict.loseWrites
	if l.Replica == "" {
		return "no remaining replica is listed, so none can take over"
	}
	lag := "its lag is not known"
	if l.Known {
		lag = fmt.Sprintf("it is %g MB behind", l.MB)
	}
	return fmt.Sprintf("no remaining member is a streaming Sync Standby, so the most current replica, %s, is promoted, and %s: any commit %s acknowledged after %s's last received WAL is lost", l.Replica, lag, p.Site, l.Replica)
}

// LoseWritesText is the plan's account of what a failover with no Sync
// Standby loses, for the question asked before `lose writes` is typed.
func (p *Plan) LoseWritesText() string {
	if p.evict.loseWrites == nil {
		return ""
	}
	return strings.ToUpper(p.loseWritesText()[:1]) + p.loseWritesText()[1:] + "."
}

// buildEvictEtcd is stage 4: the site's etcd member removed by the remaining
// voters, and with two data sites and a witness the witness's too, as site
// remove's stage 2 removes them.
func (p *Plan) buildEvictEtcd() *Stage {
	st := &Stage{Number: evictEtcd, Name: "etcd", Short: "etcd's voters are the end state's"}
	if p.etcd.control == "" {
		st.Short = "no etcd here"
		st.Gate = "none: the deployment runs no etcd"
		return st
	}
	if p.etcd.member != nil {
		st.Steps = append(st.Steps, Step{Site: p.etcd.control, Verb: "remove", Title: "remove " + p.Site + "'s etcd member", Text: fmt.Sprintf("%s's etcd member %s: %s", p.Site, p.etcd.member.HexID(), apply.Etcdctl(p.dep(), "member remove "+p.etcd.member.HexID()))})
	}
	if p.witness != "" {
		if p.etcd.witness != nil {
			st.Steps = append(st.Steps, Step{Site: p.etcd.control, Verb: "remove", Title: "remove witness " + p.witness + "'s etcd member", Text: fmt.Sprintf("the witness %s's etcd member %s, leaving %s the one voter, since two voters are worse than one: %s", p.witness, p.etcd.witness.HexID(), p.end.Etcd.Members[0], apply.Etcdctl(p.dep(), "member remove "+p.etcd.witness.HexID()))})
		}
		if p.etcd.witnessRunning {
			st.Steps = append(st.Steps, Step{Site: p.witness, Verb: "stop", Title: "stop etcd on " + p.witness, Text: "its etcd, which a removed member cannot rejoin: " + p.stopWitnessEtcd()})
		}
	}
	st.Gate = fmt.Sprintf("etcd's voters are exactly %s and every member is healthy", strings.Join(sortedCopy(p.live(p.end.Etcd.Members)), ", "))
	st.run = func() error {
		if p.etcd.member != nil {
			if err := p.removeEtcdMember(); err != nil {
				return err
			}
		}
		if p.etcd.witness != nil {
			if err := p.removeWitness(); err != nil {
				return err
			}
		}
		if p.etcd.witness != nil || p.etcd.witnessRunning {
			p.work("stop etcd on " + p.witness)
			if out, err := p.transports[p.witness].Run(p.stopWitnessEtcd()); err != nil {
				return fmt.Errorf("%s: stopping its etcd: %w: %s", p.witness, err, lastLines(out, 3))
			}
		}
		return nil
	}
	st.gate = p.etcdGate
	return st
}

// skipCommand tells Garage's layout to stop waiting for the dead node at
// version, losing what only it held when lose is set.
func (p *Plan) skipCommand(version int) string {
	args := fmt.Sprintf("layout skip-dead-nodes --version %d", version)
	if p.evict.loseObjects {
		args += " --allow-missing-data"
	}
	return gcmd(p, args)
}

// buildEvictStorage is stage 5: the site's Garage node out of the layout as
// a dead node. Garage keeps an old layout version live until every node in
// it has acknowledged and synced the new one, and an evicted node never
// will, so `layout skip-dead-nodes` marks it as having done so (Garage
// v1.0.1, src/garage/cli/layout.rs, cmd_layout_skip_dead_nodes). With
// --allow-missing-data it also marks every node synced, which is the
// decision to lose what only the dead node held.
func (p *Plan) buildEvictStorage() *Stage {
	st := &Stage{Number: evictStorage, Name: "storage", Short: "no layout row for " + p.Site}
	if p.garage.anchor == "" {
		st.Short = "no objects here"
		st.Gate = fmt.Sprintf("none: %s is not a Garage node", p.Site)
		return st
	}
	skipAt := 0
	if p.garage.node != "" {
		skipAt = p.garage.version + 1
		st.Steps = append(st.Steps,
			Step{Site: p.garage.anchor, Verb: "remove", Title: "remove " + p.Site + " from the Garage layout", Text: fmt.Sprintf("%s's node %s from Garage's layout: %s", p.Site, p.garage.node, gcmd(p, "layout remove "+p.garage.node))},
			Step{Site: p.garage.anchor, Verb: "apply", Title: "apply the Garage layout", Text: "the layout: " + gcmd(p, fmt.Sprintf("layout apply --version %d", skipAt))},
		)
	} else if p.evict.multiVersion {
		skipAt = p.garage.version
	}
	if skipAt > 0 {
		text := fmt.Sprintf("%s's node as dead, so the layout stops waiting for it to acknowledge version %d: %s", p.Site, skipAt, p.skipCommand(skipAt))
		if p.evict.loseObjects {
			text += fmt.Sprintf(". Objects only %s held are lost: storage.garage.replication is %d, and the nodes that remain hold no other copy of them", p.Site, p.cfg.Storage.Garage.Replication)
		}
		st.Steps = append(st.Steps, Step{Site: p.garage.anchor, Verb: "skip", Title: "skip " + p.Site + " as a dead Garage node", Text: text})
	}
	st.Gate = fmt.Sprintf("no layout row for %s, and on every remaining Garage site one live layout version", p.Site)
	if !p.evict.loseObjects {
		st.Gate += fmt.Sprintf(" and an empty resync queue with no errors, %d reads in a row", garageSamples)
	}
	st.Gate += fmt.Sprintf(", within %s", garageWait)
	st.run = func() error {
		t := p.transports[p.garage.anchor]
		if p.garage.node != "" {
			p.work("remove " + p.Site + " from the Garage layout")
			if out, err := t.Run(gcmd(p, "layout remove "+p.garage.node)); err != nil {
				return fmt.Errorf("%s: `garage layout remove`: %w: %s", p.garage.anchor, err, lastLines(out, 3))
			}
			p.work("apply the Garage layout")
			if out, err := t.Run(gcmd(p, fmt.Sprintf("layout apply --version %d", skipAt))); err != nil {
				return fmt.Errorf("%s: `garage layout apply`: %w: %s", p.garage.anchor, err, lastLines(out, 3))
			}
		}
		if skipAt == 0 {
			return nil
		}
		// The new version may already be the only live one, when the dead
		// node held nothing of the old; skip-dead-nodes refuses then.
		out, err := t.Run(gcmd(p, "layout history"))
		if err != nil {
			return fmt.Errorf("%s: `garage layout history`: %w: %s", p.garage.anchor, err, lastLines(out, 3))
		}
		if strings.Contains(out, storageadd.StableLayout) {
			return nil
		}
		p.work("skip " + p.Site + " as a dead Garage node")
		if out, err := t.Run(p.skipCommand(skipAt)); err != nil {
			// Run again after a stop: the dead node's acknowledgement is
			// already raised, and Garage says it did nothing. The gate
			// decides whether the layout has settled.
			if !strings.Contains(out, "Nothing was done") {
				return fmt.Errorf("%s: `garage layout skip-dead-nodes`: %w: %s", p.garage.anchor, err, lastLines(out, 3))
			}
		}
		return nil
	}
	st.gate = p.garageGate
	return st
}

// buildRest is stage 6, the rest of site remove's stage 2 once the cluster
// has let the site go: HAProxy without its backend, each remaining replica's
// patroni.env, and the deployment record.
func (p *Plan) buildRest() (*Stage, error) {
	st := &Stage{Number: evictRest, Name: "the rest of the cluster", Short: "HAProxy and replicas without " + p.Site}
	keep := func(rel string) bool { return rel != p.dep().WireGuardConf() && !p.isCaddyRouting(rel) }
	changes, proxies, err := p.planSiteChanges(st, p.liveSites(), p.rendered, keep, true)
	if err != nil {
		return nil, err
	}
	forget, records, err := p.planForget(st)
	if err != nil {
		return nil, err
	}
	replicas, err := p.replicaEnvs(st)
	if err != nil {
		return nil, err
	}
	var gates []string
	if len(proxies) > 0 {
		gates = append(gates, fmt.Sprintf("HAProxy on %s lists exactly %s, the leader UP", strings.Join(proxies, ", "), strings.Join(sortedCopy(p.end.Cluster.Sites), ", ")))
	}
	if len(replicas) > 0 {
		gates = append(gates, "each replica streams again before the next is touched")
	}
	if len(gates) == 0 {
		st.Short = "nothing else names " + p.Site
		gates = append(gates, "none")
	}
	st.Gate = strings.Join(gates, "; ")
	st.run = func() error {
		for _, c := range changes {
			if err := p.applyChange(c); err != nil {
				return err
			}
		}
		if len(replicas) > 0 {
			if err := p.evictReplicas(replicas); err != nil {
				return err
			}
		}
		if forget {
			return p.runForget(records)
		}
		return nil
	}
	st.gate = func() error {
		if len(proxies) > 0 {
			return p.haproxyGate(proxies)
		}
		return nil
	}
	return st, nil
}

// evictReplicas applies each remaining replica's patroni.env, one at a
// time, as site remove does. Which member leads is read now, not when the
// plan was made: when the evicted site led, Patroni chose its successor in
// stage 3, and recreating the leader's Patroni would be another failover.
func (p *Plan) evictReplicas(replicas []*apply.ReplicaEnv) error {
	list, err := p.patroniList()
	if err != nil {
		return err
	}
	leader := ""
	for _, m := range list {
		if m.role() == "Leader" {
			leader = m.name()
		}
	}
	if leader == "" {
		return fmt.Errorf("no member leads, so no replica's Patroni is recreated")
	}
	for _, r := range replicas {
		if r.Site == leader {
			p.Notes = append(p.Notes, apply.LeaderPatroniEnvNote(leader))
			continue
		}
		p.work("recreate Patroni on "+r.Site).Detail("Patroni on %s, with its patroni.env up to date", r.Site)
		gate := apply.ReplicaEnvGate{At: p.transports[leader], Wait: replicaWait, Poll: replicaPoll, Sleep: sleep, Sync: p.endWantsSync()}
		if err := r.Execute(p.dep(), p.transports[r.Site], gate); err != nil {
			return err
		}
		p.open.Detail("%s streams again", r.Site)
	}
	return nil
}

// EvictReport is what an eviction leaves for the operator: what the host
// still holds, which secrets to rotate and in what order, and the apply that
// brings up a replacement. Nothing here is changed by this command.
func (p *Plan) EvictReport() []string {
	var out []string
	if p.evict.tokenUnchecked != "" {
		out = append(out, fmt.Sprintf("dns: the DNS token was not checked; check it yourself. %s held the token it was rendered, and %s, so this command could not try the one in the secrets file. Revoke the old one at the provider", p.Site, p.evict.tokenUnchecked))
	}
	if joining := p.Joining(); len(joining) > 0 {
		out = append(out, fmt.Sprintf("apply: run `paisans apply` to bring up %s. It prepares the new host, applies it, creates its DNS records, and adds it as a peer on every remaining site; until then it is in paisans.yaml and nowhere else", joinAnd(joining)))
	}
	if len(p.evict.dark) > 0 {
		out = append(out, fmt.Sprintf("names: the community answers nothing until that apply. %s was the only remaining %s site", p.Site, strings.Join(p.evict.dark, " and ")))
	}
	out = append(out,
		fmt.Sprintf("%s: the host still holds what it was rendered. Its WireGuard key no longer works on any remaining site. Rotate the rest in this order: the DNS token first (secrets set, and revoked at the provider), since it can point the community's names anywhere; the OIDC client secrets (oidc client create --rotate-secret); the apps' S3 keys (storage rotate-key --app); then the Postgres, Garage and heartbeat secrets, which secrets rotate will change where they are checked and where they are used together", p.Site),
		fmt.Sprintf("%s: a copy of the database and the objects stays on the host; it cannot be taken back", p.Site),
		"admins: when the evicted admin could decrypt the secrets file, take their age key out of .sops.yaml and re-encrypt under a new data key (sops rotate --rm-age), then rotate every remaining site's WireGuard key with paisans mesh rekey --all before the replacement joins. docs/guides/when-an-admin-leaves.md is the whole order",
	)
	return out
}
