// Package gatewaymove moves the gateway role from one site to another as an
// overlap, never a cutover: `paisans gateway move`. The configuration names
// the new gateway, and the old one is found from what it still holds: this
// deployment's gateway Caddy or deployment record on a site that no longer
// has the role, or this deployment's address records still pointing at a
// site other than the gateway. README's *Moving the gateway* is the design,
// and docs/specs/2026-10-11-gateway-move.md the approved specification.
//
// It is built the way internal/siteremove is: staged, each stage planned
// from live state and ending at a gate that stops the command with its
// evidence, so a run after a fixed problem resumes at the first stage with
// anything left to do. Every gate runs on every run, so the new gateway is
// verified on the run that deletes the old gateway's records, and again on
// the run that stops it.
package gatewaymove

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// Options is what the caller hands in besides the configuration.
type Options struct {
	// DNSProvider makes the provider records are added and deleted through,
	// from acme.provider and the token. Nil is dns.For; tests hand in a fake.
	DNSProvider func(name, token string) (dns.Provider, error)
	// Probes reach the new gateway over HTTPS, tell the time and wait. The
	// zero value is DefaultProbes.
	Probes Probes
	// ApplySite applies a site's infra stack, as deployment apply --site <site> --only
	// infra --execute. The hand-off off a site that keeps the monitor calls
	// it once the site no longer holds the gateway's record, so that its
	// Caddy reloads with the monitor's routes alone.
	ApplySite func(site string) error
	// CheckSite is ApplySite's dry run. An executed move off a site that
	// keeps the monitor runs it before the first stage, so a refusal the
	// hand-off's apply would meet after DNS has moved stops the move at the
	// start instead.
	CheckSite func(site string) error
}

// dnsTimeout bounds one plan or one run against the provider.
const dnsTimeout = 5 * time.Minute

// The stages, in order.
const (
	stageRecord = iota + 1
	stageServe
	stageAdd
	stageRemove
	stageWait
	stageStop
	stageHandOff
)

// Stage is one stage of the move: its steps, then its gate.
type Stage struct {
	Number int
	Name   string
	// Steps are what the stage will do, one line each.
	Steps []string
	// Kept are what the stage found and may not touch, each with why. They
	// stop the stage's run.
	Kept []string
	// Short names the gate in a few words; Gate says it whole.
	Short, Gate string
	// Skipped says why the stage does nothing, empty when it runs.
	Skipped string

	run  func() error
	gate func() error
}

// Evidence is what one site's host holds of a gateway.
type Evidence struct {
	// Caddy are this deployment's gateway Caddy containers on the host.
	Caddy []hostprep.CaddyContainer
	// Record is whether the host holds this deployment's record.
	Record bool
	// Marker is the move's own record of when the old gateway's DNS records
	// were deleted, nil for none.
	Marker *Marker
	// HostSites are the files in the deployment's caddy.d the Caddy serves
	// for the host's owner.
	HostSites []string
	// Routes is whether the site's Caddyfile names a hostname the gateway
	// serves. It is read only on a site whose roles run Caddy, where the
	// Caddy itself is not evidence: it is what still finds a site that keeps
	// the monitor once its record and marker are gone, until the hand-off has
	// reloaded it.
	Routes bool
}

func (e Evidence) any() bool { return len(e.Caddy) > 0 || e.Record || e.Marker != nil || e.Routes }

// Marker is written on the old gateway's host once its DNS records are
// deleted, so that a run that stops during the wait resumes it rather than
// starting it again or skipping it.
type Marker struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	DeletedAt time.Time `json:"deleted_at"`
	// TTL is the longest time to live of the deleted records, in seconds.
	TTL int `json:"ttl"`
}

// Until is when every resolver has forgotten the deleted records.
func (m Marker) Until() time.Time { return m.DeletedAt.Add(time.Duration(m.TTL) * time.Second) }

// CaddyfilePath is the Caddyfile on a site's host that runs the toolkit's
// Caddy.
func CaddyfilePath(d deployment.Deployment) string {
	return "/" + render.Caddyfile(d)
}

// legacyCaddyfilePath is where a toolkit before the Caddyfile moved into
// render.CaddyConfigDir wrote it. An apply leaves it behind, so it is read
// only on a host that has no Caddyfile at CaddyfilePath.
func legacyCaddyfilePath(d deployment.Deployment) string {
	return d.Path("infra", "caddy", "Caddyfile")
}

// MarkerPath is the marker on the old gateway's host, beside the deployment
// record in the deployment's state directory.
func MarkerPath(d deployment.Deployment) string { return d.StatePath("gateway-move.json") }

// markerWriteCommand replaces the marker as every record in StateDir is
// written: under the deployment's lock, StateDir made 700 when absent, and
// by a rename so a reader never sees half of it.
func markerWriteCommand(d deployment.Deployment, content string) string {
	return strings.Join([]string{
		"set -e",
		d.StateLockCommand("the gateway move marker"),
		"f=" + quote(MarkerPath(d)),
		"tmp=$(mktemp " + quote(d.StatePath(".gateway-move.XXXXXX")) + ")",
		`trap 'rm -f "$tmp"' EXIT`,
		"printf %s " + quote(base64.StdEncoding.EncodeToString([]byte(content))) + ` | base64 -d > "$tmp"`,
		`mv "$tmp" "$f"`,
	}, "; ")
}

// removeRecordsCommand deletes the deployment record and the marker from
// the old gateway under the deployment's lock, so the delete never lands
// between an apply's read of the record and its write. It always runs as
// the deployment's user on its own StateDir, where the lock prelude is
// that user's to run.
func removeRecordsCommand(d deployment.Deployment) string {
	return d.StateLockCommand("the gateway move's records") + "; rm -f -- " + quote(deployrecord.Path(d)) + " " + quote(MarkerPath(d))
}

// Plan is a whole move, decided from the live deployment.
type Plan struct {
	// From is the old gateway's site, empty when there is nothing to move;
	// To is the configuration's gateway.
	From, To string
	Stages   []*Stage
	// Notes are what the move leaves for later, on purpose.
	Notes []string
	// Report receives each stage as a section. Nil discards it.
	Report ui.Reporter

	cfg        *config.Config
	transports map[string]apply.Transport
	provider   dns.Provider
	probes     Probes
	evidence   Evidence
	// shared is whether From runs Caddy for a monitor, which the move hands
	// off rather than stops.
	shared    bool
	applySite func(site string) error
	checkSite func(site string) error
}

func (p *Plan) reporter() ui.Reporter {
	if p.Report == nil {
		return ui.Discard
	}
	return p.Report
}

func (p *Plan) dep() deployment.Deployment { return p.cfg.Deployment() }

func refuse(hint, explain string) error { return &ui.Problem{Hint: hint, Explain: explain} }

// Build finds the old gateway and plans the move to the configuration's
// one gateway. It reads every other site's host and the DNS provider, and
// writes nothing. A plan with no From has nothing to move.
func Build(cfg *config.Config, secrets *config.Secrets, transports map[string]apply.Transport, o Options) (*Plan, error) {
	gateways := cfg.GatewaySites()
	if len(gateways) != 1 {
		return nil, refuse(fmt.Sprintf("%d sites hold the gateway role", len(gateways)), "A move goes to exactly one gateway, the one site "+config.FileName+" gives the role; the gateway it leaves is found from what that site still runs, so it is never declared as well. Give the role to one site.")
	}
	to := gateways[0]
	if cfg.Sites[to].PublicAddress == "" {
		return nil, refuse("sites."+to+".public_address is not set", "The move checks the new gateway through the address the internet reaches it on, and points the hostnames at it. Declare it.")
	}
	probes := o.Probes
	if probes.Get == nil {
		probes = DefaultProbes()
	}
	p := &Plan{To: to, cfg: cfg, transports: transports, probes: probes, applySite: o.ApplySite, checkSite: o.CheckSite}

	// The DNS stages need the provider's API; a move it cannot reach is
	// refused rather than left half done, since the old gateway may only be
	// stopped once no record points at it.
	name := cfg.ACME.Provider
	if name == "" || !slices.Contains(dns.Implemented(), name) {
		return nil, refuse("DNS record management is not available for acme.provider "+fmt.Sprintf("%q", name), "The move adds the new gateway's address records and deletes the old one's through the provider's API, which exists for "+strings.Join(dns.Implemented(), ", ")+". Without it, move the gateway by hand as README, \"Moving the gateway\", describes.")
	}
	token := ""
	if secrets != nil {
		token = secrets.External["acme_dns_token"]
	}
	providerFor := o.DNSProvider
	if providerFor == nil {
		providerFor = dns.For
	}
	provider, err := providerFor(name, token)
	if err != nil {
		return nil, err
	}
	p.provider = provider

	// The old gateway: a site without the role that still holds what a
	// gateway holds. A site whose roles run Caddy (a monitor in ingress mode
	// paisans) holds its Caddy legitimately, so there the record, the marker
	// and the gateway's routes are the evidence, never the Caddy.
	held := map[string]Evidence{}
	for _, site := range cfg.SiteNames() {
		if site == to {
			continue
		}
		t, ok := transports[site]
		if !ok {
			return nil, fmt.Errorf("gateway move: no way to reach %s", site)
		}
		read := inspect
		if cfg.Sites[site].RunsCaddy() {
			read = func(t apply.Transport, d deployment.Deployment) (Evidence, error) { return inspectDeparting(t, cfg) }
		}
		ev, err := read(t, cfg.Deployment())
		if err != nil {
			return nil, err
		}
		if ev.any() {
			held[site] = ev
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout)
	defer cancel()
	pointed, err := dns.FormerGateways(ctx, provider, cfg)
	if err != nil {
		return nil, err
	}
	candidates := slices.Sorted(maps.Keys(held))
	for _, s := range pointed {
		if !slices.Contains(candidates, s) {
			candidates = append(candidates, s)
		}
	}
	slices.Sort(candidates)
	switch len(candidates) {
	case 0:
		return p, nil
	case 1:
	default:
		return nil, refuse("more than one site still holds a gateway: "+strings.Join(candidates, ", "), "A move leaves one gateway for another. Finish the moves one at a time: give the gateway role to one of them, apply it and run the move, then move it on.")
	}
	p.From = candidates[0]
	p.shared = cfg.Sites[p.From].RunsCaddy()
	// A monitor that moved off the old gateway with it keeps a record there
	// until it is changed by hand, as for any monitor that moves; the stages
	// below move only the gateway's hostnames, and the last stops or reloads
	// the Caddy that record still reaches.
	behind, err := dns.MonitorRecordsAt(ctx, provider, cfg, p.From)
	if err != nil {
		return nil, err
	}
	if len(behind) > 0 {
		return nil, refuse(fmt.Sprintf("%s still point at %s", strings.Join(behind, ", "), p.From),
			fmt.Sprintf("A monitor serves each from another site now, but its record was made for %s, and the move changes only the gateway's hostnames: stopping %s's Caddy would take it down. Nothing was changed. Point each at the address of the site that serves it now, at your DNS provider (README.md, \"Moving an existing monitor\"), then run the move again.", p.From, p.From))
	}
	p.evidence = held[p.From]
	p.evidence.Marker = p.ours(p.evidence.Marker)

	// The DNS plan is read once here, for the dry run; every run plans it
	// again from a fresh listing.
	mp, err := p.planDNS()
	if err != nil {
		return nil, err
	}
	if len(mp.Conflicts) > 0 {
		var lines []string
		for _, e := range mp.Conflicts {
			lines = append(lines, fmt.Sprintf("- %s %s: %s", e.Type, e.Name, e.Detail))
		}
		return nil, refuse(fmt.Sprintf("%d of %s's records conflict with what the provider holds", len(mp.Conflicts), p.To), strings.Join(lines, "\n")+"\nNothing was changed. Change each at your DNS provider, then run the move again.")
	}

	p.Stages = []*Stage{p.recordStage(), p.serveStage(), p.addStage(mp), p.removeStage(mp), p.waitStage(mp)}
	if p.shared {
		p.Stages = append(p.Stages, p.releaseStage(), p.handOffStage())
		// A run stopped at the hand-off finds the site by its routes alone:
		// the record and the marker are gone and DNS has moved, so every
		// stage before the hand-off is done, and running them again would
		// write a marker and wait out a TTL for nothing.
		if !p.evidence.Record && p.evidence.Marker == nil && mp.Clear() {
			for _, st := range p.Stages[:len(p.Stages)-1] {
				st.Skipped = p.From + " was released already: it holds neither the record nor the marker, and no record points at it"
			}
		}
		p.Notes = append(p.Notes, fmt.Sprintf("%s keeps its Caddy, which serves the monitor's hostnames alone once handed off.", p.From))
	} else {
		p.Stages = append(p.Stages, p.stopStage())
		p.Notes = append(p.Notes,
			fmt.Sprintf("%s keeps 80 and 443 open until its next site prepare (or apply without --site) after the move, which removes them.", p.From),
			fmt.Sprintf("The old Caddy's files and certificates stay on %s, recorded as left over; the new gateway issued its own.", p.From),
		)
	}
	if len(p.evidence.HostSites) > 0 {
		held := "The old Caddy is not stopped while it does"
		if p.shared {
			held = p.From + " is not handed off while it does, since a monitor's Caddy does not serve them"
		}
		p.Notes = append(p.Notes, fmt.Sprintf("%s's %[2]s holds %[3]s. %[4]s: move those files to %[5]s's %[2]s, or delete them, then run the move again.", p.From, render.HostSitesDir(p.dep()), strings.Join(p.evidence.HostSites, ", "), held, p.To))
	}
	return p, nil
}

// inspectCommand prints the gateway Caddy's containers and the files in
// the deployment's caddy.d. set -e makes a Docker that cannot be asked a failure, never
// an empty answer.
func inspectCommand(d deployment.Deployment) string {
	return "set -e; " + hostprep.GatewayCaddyProbe(d) + "; for f in " + quote(render.HostSitesDir(d)) + `/*.caddy; do if [ -e "$f" ]; then echo "site $f"; fi; done`
}

// inspect reads what a site's host holds of a gateway.
func inspect(t apply.Transport, d deployment.Deployment) (Evidence, error) {
	var ev Evidence
	t = apply.Prefetch(t, []string{inspectCommand(d)}, []string{deployrecord.Path(d), MarkerPath(d)})
	out, err := t.Run(inspectCommand(d))
	if err != nil {
		return ev, fmt.Errorf("%s: asking for this deployment's gateway Caddy: %w", t.Describe(), err)
	}
	ev.Caddy = hostprep.ParseGatewayCaddy(out)
	for _, line := range strings.Split(out, "\n") {
		if f, ok := strings.CutPrefix(strings.TrimSpace(line), "site "); ok {
			ev.HostSites = append(ev.HostSites, f)
		}
	}
	_, found, err := t.ReadFile(deployrecord.Path(d))
	if err != nil {
		return ev, fmt.Errorf("%s: reading %s: %w", t.Describe(), deployrecord.Path(d), err)
	}
	ev.Record = found
	content, found, err := t.ReadFile(MarkerPath(d))
	if err != nil {
		return ev, fmt.Errorf("%s: reading %s: %w", t.Describe(), MarkerPath(d), err)
	}
	if found {
		var m Marker
		if err := json.Unmarshal([]byte(content), &m); err != nil {
			return ev, fmt.Errorf("%s: %s is not a gateway move's marker (%v). Delete it by hand and run again: the move then waits a whole TTL once more", t.Describe(), MarkerPath(d), err)
		}
		ev.Marker = &m
	}
	return ev, nil
}

// Holds reports whether the host t reaches holds what a gateway of d's
// holds: its gateway Caddy, its deployment record or a move's marker. apply
// without --site asks it of every site without the gateway role, to know
// whether a move is still to run.
func Holds(t apply.Transport, d deployment.Deployment) (bool, error) {
	ev, err := inspect(t, d)
	return ev.any(), err
}

// HoldsRecordOrMarker reports whether the host t reaches holds d's deployment
// record or a move's marker, and not its Caddy. Only a gateway is ever given
// the record, so on a site that runs Caddy for a monitor and no longer holds
// the gateway role, either one is a gateway being moved off a site that keeps
// the monitor.
func HoldsRecordOrMarker(t apply.Transport, d deployment.Deployment) (bool, error) {
	for _, path := range []string{deployrecord.Path(d), MarkerPath(d)} {
		_, found, err := t.ReadFile(path)
		if err != nil {
			return false, fmt.Errorf("%s: reading %s: %w", t.Describe(), path, err)
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

// Departing reports whether the host t reaches, of a site whose roles run
// Caddy for a monitor, still holds what a gateway being handed off holds: the
// record, the marker, or a Caddyfile naming a hostname the gateway serves.
// apply without --site asks it of such sites, to know whether a move is still
// to run.
func Departing(t apply.Transport, cfg *config.Config) (bool, error) {
	ev, err := inspectDeparting(t, cfg)
	return ev.any(), err
}

// inspectDeparting is inspect for a site whose roles run Caddy: its Caddy is
// legitimately there, so it is not evidence, and its routes are while that
// Caddy runs. A plain move leaves the old Caddy's files behind, so a site
// that gains the monitor role later holds a Caddyfile naming the gateway's
// hostnames with no Caddy serving it until its first apply as a monitor,
// which rewrites it: that is not a hand-off waiting to finish.
func inspectDeparting(t apply.Transport, cfg *config.Config) (Evidence, error) {
	ev, err := inspect(t, cfg.Deployment())
	if err != nil {
		return ev, err
	}
	running := len(ev.Caddy) > 0
	ev.Caddy = nil
	if !running {
		return ev, nil
	}
	routes, err := gatewayRoutes(t, cfg)
	if err != nil {
		return ev, err
	}
	ev.Routes = len(routes) > 0
	return ev, nil
}

// gatewayRoutes are the hostnames the gateway serves that the Caddyfile on
// the host t reaches names as a site block, sorted: the one at CaddyfilePath,
// or on a host last applied before it moved there, the one at
// legacyCaddyfilePath. A missing Caddyfile names none.
func gatewayRoutes(t apply.Transport, cfg *config.Config) ([]string, error) {
	var content string
	found := false
	for _, path := range []string{CaddyfilePath(cfg.Deployment()), legacyCaddyfilePath(cfg.Deployment())} {
		var err error
		if content, found, err = t.ReadFile(path); err != nil {
			return nil, fmt.Errorf("%s: reading %s: %w", t.Describe(), path, err)
		}
		if found {
			break
		}
	}
	if !found {
		return nil, nil
	}
	served := map[string]bool{}
	for _, h := range dns.GatewayHostnames(cfg) {
		served[h.Name] = true
	}
	var out []string
	for _, line := range strings.Split(content, "\n") {
		head, ok := strings.CutSuffix(strings.TrimRight(line, " \t"), "{")
		if !ok || line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		for _, name := range strings.FieldsFunc(head, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
			if served[name] && !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

func (p *Plan) from() apply.Transport { return p.transports[p.From] }

// ours is m when it is this move's, from From to To, and nil otherwise: a
// marker an earlier move left, Eg: one rolled back, says nothing about when
// this move's records went.
func (p *Plan) ours(m *Marker) *Marker {
	if m == nil || m.From != p.From || m.To != p.To {
		return nil
	}
	return m
}

func (p *Plan) planDNS() (*dns.MovePlan, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout)
	defer cancel()
	return dns.BuildMove(ctx, p.provider, p.cfg, p.From)
}

// recordStage carries the old gateway's deployment record to the new one:
// it may list names the configuration no longer declares, which only a
// removal command may take out, and the record on the old gateway is
// deleted when it stops.
func (p *Plan) recordStage() *Stage {
	st := &Stage{
		Number: stageRecord, Name: "record",
		Short: p.To + "'s record holds " + p.From + "'s",
		Gate:  fmt.Sprintf("the deployment record on %s lists every name the record on %s lists", p.To, p.From),
	}
	if !p.evidence.Record {
		st.Skipped = p.From + " holds no deployment record"
		return st
	}
	hosts := func() map[string]registry.Runner {
		return map[string]registry.Runner{p.From: p.from(), p.To: p.transports[p.To]}
	}
	if !p.recordCarried() {
		st.Steps = append(st.Steps, fmt.Sprintf("carry the deployment record from %s to %s", p.From, p.To))
	}
	st.run = func() error {
		res, err := deployrecord.Update(hosts(), p.dep(), deployrecord.Adding(deployrecord.Record{}), p.probes.Now())
		if err != nil {
			return err
		}
		if e := res.Missed[p.To]; e != nil {
			return fmt.Errorf("the record was not written to %s: %w", p.To, e)
		}
		return nil
	}
	st.gate = func() error {
		if !p.recordCarried() {
			return fmt.Errorf("the deployment record on %s does not list everything %s's does", p.To, p.From)
		}
		return nil
	}
	return st
}

// recordCarried reports whether the new gateway's record lists every name
// the old one's does.
func (p *Plan) recordCarried() bool {
	d := p.dep()
	old, n, _ := deployrecord.Gather(map[string]registry.Runner{p.From: p.from()}, d)
	if n == 0 {
		return true
	}
	cur, m, _ := deployrecord.Gather(map[string]registry.Runner{p.To: p.transports[p.To]}, d)
	if m == 0 {
		return false
	}
	for _, kind := range []string{"sites", "apps", "pocket_id_groups"} {
		for _, name := range old.List(kind) {
			if !cur.Lists(kind, name) {
				return false
			}
		}
	}
	return true
}

// serveStage is the new gateway serving every hostname, checked through its
// own address before any record points at it. It has no steps: the new
// gateway is brought up by applying it, and this only checks it did.
func (p *Plan) serveStage() *Stage {
	return &Stage{
		Number: stageServe, Name: "serve",
		Short: p.To + " answers for every hostname",
		Gate:  fmt.Sprintf("every hostname the gateway serves answers HTTPS through %s's public address, with a certificate valid for it and a status below 500", p.To),
		gate:  p.verify,
	}
}

// addStage creates the new gateway's address records beside the old
// gateway's, so both serve while resolvers pick up the new address.
func (p *Plan) addStage(mp *dns.MovePlan) *Stage {
	st := &Stage{
		Number: stageAdd, Name: "dns add",
		Short: "every hostname has " + p.To + "'s address",
		Gate:  fmt.Sprintf("listing the records again finds %s's address record at every hostname the gateway serves, unproxied, beside %s's", p.To, p.From),
	}
	for _, e := range mp.Creates {
		st.Steps = append(st.Steps, fmt.Sprintf("create %s %s -> %s (zone %s)", e.Type, e.Name, e.Content, e.Zone))
	}
	st.run = func() error {
		fresh, err := p.planDNS()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout)
		defer cancel()
		return dns.ExecuteMoveAdd(ctx, p.provider, fresh, p.reporter())
	}
	st.gate = func() error {
		fresh, err := p.planDNS()
		if err != nil {
			return err
		}
		if !fresh.Ready() {
			return fmt.Errorf("%d record(s) for %s are still missing", len(fresh.Creates)+len(fresh.Conflicts), p.To)
		}
		return nil
	}
	return st
}

// removeStage deletes the old gateway's address records, only after the
// new gateway answered for every hostname on this run, and writes the
// marker the wait reads.
func (p *Plan) removeStage(mp *dns.MovePlan) *Stage {
	st := &Stage{
		Number: stageRemove, Name: "dns remove",
		Short: "no hostname points at " + p.From,
		Gate:  fmt.Sprintf("listing the records again finds no address record at a hostname the gateway serves pointing at %s, and the time they went is recorded on %s", p.From, p.From),
	}
	for _, e := range mp.Deletes {
		st.Steps = append(st.Steps, fmt.Sprintf("delete %s %s -> %s (zone %s, record %s)", e.Type, e.Name, e.Content, e.Zone, e.ID))
	}
	for _, e := range mp.Kept {
		st.Kept = append(st.Kept, fmt.Sprintf("%s %s -> %s: %s", e.Type, e.Name, e.Content, strings.Join(e.Reasons, "; ")))
	}
	if p.evidence.Marker == nil && len(mp.Deletes) == 0 {
		st.Steps = append(st.Steps, "record on "+p.From+" when its records went")
	}
	if len(st.Steps) > 0 {
		st.Steps = append([]string{"check " + p.To + " answers for every hostname, again"}, st.Steps...)
	}
	st.run = func() error {
		// The founder's gate: the old gateway's records are deleted only
		// once the new one is verified, and verified now, not on some
		// earlier run.
		if err := p.verify(); err != nil {
			return err
		}
		fresh, err := p.planDNS()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout)
		defer cancel()
		if err := dns.ExecuteMoveRemove(ctx, p.provider, fresh, p.reporter()); err != nil {
			return err
		}
		ev, err := inspect(p.from(), p.dep())
		if err != nil {
			return err
		}
		if len(fresh.Deletes) == 0 && p.ours(ev.Marker) != nil {
			return nil
		}
		// Records deleted on this run restart the wait; with none deleted
		// and no marker (deleted by hand, or never there), the wait is the
		// provider's automatic TTL, which every record dns init makes has.
		ttl := fresh.TTL()
		if ttl == 0 {
			ttl = dns.DefaultTTL
		}
		m := Marker{From: p.From, To: p.To, DeletedAt: p.probes.Now().UTC(), TTL: ttl}
		data, _ := json.Marshal(m)
		if out, err := p.from().Run(markerWriteCommand(p.dep(), string(data)+"\n")); err != nil {
			return fmt.Errorf("%s: writing %s: %w: %s", p.From, MarkerPath(p.dep()), err, strings.TrimSpace(out))
		}
		return nil
	}
	st.gate = func() error {
		fresh, err := p.planDNS()
		if err != nil {
			return err
		}
		if !fresh.Clear() {
			return fmt.Errorf("%d record(s) still point at %s", len(fresh.Deletes)+len(fresh.Kept), p.From)
		}
		ev, err := inspect(p.from(), p.dep())
		if err != nil {
			return err
		}
		if p.ours(ev.Marker) == nil {
			return fmt.Errorf("%s holds no record of when its DNS records went", p.From)
		}
		return nil
	}
	return st
}

// waitStage waits out the deleted records' TTL, during which the old
// gateway keeps serving every visitor whose resolver still has its
// address.
func (p *Plan) waitStage(mp *dns.MovePlan) *Stage {
	st := &Stage{
		Number: stageWait, Name: "wait",
		Short: "the old records' TTL has passed",
		Gate:  fmt.Sprintf("the longest TTL of %s's deleted records has passed since they went", p.From),
	}
	switch m := p.evidence.Marker; {
	case m == nil:
		ttl := mp.TTL()
		if ttl == 0 {
			ttl = dns.DefaultTTL
		}
		st.Steps = append(st.Steps, fmt.Sprintf("wait %s once the records are gone, while %s serves stragglers", time.Duration(ttl)*time.Second, p.From))
	case p.probes.Now().Before(m.Until()):
		st.Steps = append(st.Steps, fmt.Sprintf("wait %s more, while %s serves stragglers", m.Until().Sub(p.probes.Now()).Round(time.Second), p.From))
	}
	marker := func() (*Marker, error) {
		ev, err := inspect(p.from(), p.dep())
		if err != nil {
			return nil, err
		}
		m := p.ours(ev.Marker)
		if m == nil {
			return nil, fmt.Errorf("%s holds no record of when its DNS records went", p.From)
		}
		return m, nil
	}
	st.run = func() error {
		m, err := marker()
		if err != nil {
			return err
		}
		if left := m.Until().Sub(p.probes.Now()); left > 0 {
			s := p.reporter().Step(fmt.Sprintf("wait %s for resolvers to forget %s's address", left.Round(time.Second), p.From))
			p.probes.Sleep(left)
			s.Done("")
		}
		return nil
	}
	st.gate = func() error {
		m, err := marker()
		if err != nil {
			return err
		}
		if left := m.Until().Sub(p.probes.Now()); left > 0 {
			return fmt.Errorf("%s still to wait", left.Round(time.Second))
		}
		return nil
	}
	return st
}

// stopStage stops and removes the old gateway's Caddy, and then what marks
// it as the old gateway: its deployment record and the marker. It refuses
// unless the new gateway answers for every hostname, on this run, and while
// the host's owner still has sites in the deployment's caddy.d that only this Caddy
// serves.
func (p *Plan) stopStage() *Stage {
	d := p.dep()
	st := &Stage{
		Number: stageStop, Name: "stop",
		Short: p.From + " is no longer a gateway",
		Gate:  fmt.Sprintf("%s runs no gateway Caddy of this deployment's and holds neither its deployment record nor the move's marker", p.From),
	}
	for _, c := range p.evidence.Caddy {
		st.Steps = append(st.Steps, fmt.Sprintf("stop and remove %s's gateway Caddy (container %s, %s)", p.From, c.ID, c.State))
	}
	if p.evidence.Record {
		st.Steps = append(st.Steps, "remove the deployment record from "+p.From)
	}
	st.Steps = append(st.Steps, "remove the move's marker from "+p.From)
	for _, f := range p.evidence.HostSites {
		st.Kept = append(st.Kept, f+": the host owner's site, which only this Caddy serves")
	}
	st.run = func() error {
		if err := p.verify(); err != nil {
			return err
		}
		ev, err := inspect(p.from(), d)
		if err != nil {
			return err
		}
		if len(ev.HostSites) > 0 {
			return refuse(fmt.Sprintf("%s's Caddy still serves the host owner's sites", p.From), fmt.Sprintf("%s holds %s, which only this Caddy serves. Move them to %s's %s, or delete them, then run the move again.", render.HostSitesDir(d), strings.Join(ev.HostSites, ", "), p.To, render.HostSitesDir(d)))
		}
		for _, c := range ev.Caddy {
			s := p.reporter().Step("stop " + p.From + "'s gateway Caddy")
			if out, err := p.from().Run(fmt.Sprintf("docker stop -t 30 %s >/dev/null && docker rm %s >/dev/null", c.ID, c.ID)); err != nil {
				err = fmt.Errorf("%s: stopping container %s: %w: %s", p.From, c.ID, err, strings.TrimSpace(out))
				s.Fail(err)
				return err
			}
			s.Done("")
		}
		// The marker goes last: until it does, a run finds this site again
		// and resumes here.
		s := p.reporter().Step("forget " + p.From + " as a gateway")
		if out, err := p.from().Run(removeRecordsCommand(d)); err != nil {
			err = fmt.Errorf("%s: %w: %s", p.From, err, strings.TrimSpace(out))
			s.Fail(err)
			return err
		}
		s.Done("")
		return nil
	}
	st.gate = func() error {
		ev, err := inspect(p.from(), d)
		if err != nil {
			return err
		}
		if ev.any() {
			return fmt.Errorf("%s still holds a gateway's: %d Caddy container(s), record %t, marker %t", p.From, len(ev.Caddy), ev.Record, ev.Marker != nil)
		}
		return nil
	}
	return st
}

// releaseStage is stopStage for an old gateway that keeps the monitor: its
// Caddy keeps running, since the monitor needs it, and what marks it as the
// old gateway goes, so that applying it renders the monitor it now is. The
// host owner's sites refuse it as they refuse a stop: a monitor's Caddy does
// not serve the deployment's caddy.d, so the hand-off would take them dark.
func (p *Plan) releaseStage() *Stage {
	d := p.dep()
	st := &Stage{
		Number: stageStop, Name: "stop",
		Short: p.From + " is no longer a gateway",
		Gate:  fmt.Sprintf("%s holds neither this deployment's record nor the move's marker; its Caddy runs on for the monitor", p.From),
	}
	if p.evidence.Record {
		st.Steps = append(st.Steps, "remove the deployment record from "+p.From)
	}
	st.Steps = append(st.Steps, "remove the move's marker from "+p.From)
	for _, f := range p.evidence.HostSites {
		st.Kept = append(st.Kept, f+": the host owner's site, which the monitor's Caddy does not serve")
	}
	st.run = func() error {
		if err := p.verify(); err != nil {
			return err
		}
		ev, err := inspect(p.from(), d)
		if err != nil {
			return err
		}
		if len(ev.HostSites) > 0 {
			return refuse(fmt.Sprintf("%s's Caddy still serves the host owner's sites", p.From), fmt.Sprintf("%s holds %s, which a monitor's Caddy does not serve, so handing %s off would take them dark. Move them to %s's %s, or delete them, then run the move again.", render.HostSitesDir(d), strings.Join(ev.HostSites, ", "), p.From, p.To, render.HostSitesDir(d)))
		}
		s := p.reporter().Step("forget " + p.From + " as a gateway")
		if out, err := p.from().Run(removeRecordsCommand(d)); err != nil {
			err = fmt.Errorf("%s: %w: %s", p.From, err, strings.TrimSpace(out))
			s.Fail(err)
			return err
		}
		s.Done("")
		return nil
	}
	st.gate = func() error {
		ev, err := inspect(p.from(), d)
		if err != nil {
			return err
		}
		if ev.Record || ev.Marker != nil {
			return fmt.Errorf("%s still holds a gateway's: record %t, marker %t", p.From, ev.Record, ev.Marker != nil)
		}
		return nil
	}
	return st
}

// handOffStage applies the old gateway that keeps the monitor, now that it
// holds neither the record nor the marker, so that its Caddy reloads with the
// monitor's routes alone.
func (p *Plan) handOffStage() *Stage {
	st := &Stage{
		Number: stageHandOff, Name: "hand off",
		Short: p.From + " serves the monitor alone",
		Gate:  fmt.Sprintf("%s's Caddyfile names no hostname the gateway serves", p.From),
	}
	if p.evidence.Routes {
		st.Steps = append(st.Steps, fmt.Sprintf("apply %s's infra stack, so that its Caddy serves the monitor's hostnames alone", p.From))
	}
	st.run = func() error {
		if p.applySite == nil {
			return fmt.Errorf("no way to apply %s was given to the move", p.From)
		}
		return p.applySite(p.From)
	}
	st.gate = func() error {
		routes, err := gatewayRoutes(p.from(), p.cfg)
		if err != nil {
			return err
		}
		if len(routes) > 0 {
			return fmt.Errorf("%s's Caddyfile still names %s", p.From, strings.Join(routes, ", "))
		}
		return nil
	}
	return st
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Pending is how many stages have steps to run.
func (p *Plan) Pending() int {
	n := 0
	for _, st := range p.Stages {
		if st.Skipped == "" && len(st.Steps) > 0 {
			n++
		}
	}
	return n
}

func stageTitle(st *Stage) string { return fmt.Sprintf("stage %d, %s", st.Number, st.Name) }

// Show lists the plan: a section per stage, each step and the gate marked
// pending, and what a stage may not touch as a refusal, since it stops the
// run.
func (p *Plan) Show(r ui.Reporter) {
	if p.From == "" {
		r.Section("gateway move")
		r.Step(p.To+" is the only gateway").End(ui.OK, "")
		return
	}
	r.Section(fmt.Sprintf("gateway move %s -> %s", p.From, p.To))
	for _, st := range p.Stages {
		r.Section(stageTitle(st))
		if st.Skipped != "" {
			r.Step("skip "+st.Name+": "+st.Skipped).End(ui.OK, "")
			continue
		}
		for _, s := range st.Steps {
			r.Step(s).End(ui.Pending, "")
		}
		for _, k := range st.Kept {
			why := "The move deletes only records this deployment's dns init made for the old gateway. While this one points there the old gateway is not stopped."
			if st.Number == stageStop {
				why = fmt.Sprintf("Move it to %s's %s, or delete it, before this stage runs.", p.To, render.HostSitesDir(p.dep()))
			}
			r.Refuse("keep "+k, why)
		}
		r.Step("gate: "+st.Short).End(ui.Pending, "")
		r.Detail("%s", st.Gate)
	}
}

// Execute runs the stages in order. A stage's run is made only when it has
// steps; its gate always runs, so a resumed move proves each stage again
// before moving past it.
func Execute(p *Plan) error {
	if p.From == "" {
		return nil
	}
	r := p.reporter()
	if err := p.checkHandOff(); err != nil {
		return err
	}
	for _, st := range p.Stages {
		r.Section(stageTitle(st))
		if st.Skipped != "" {
			r.Step("skip " + st.Name + ": " + st.Skipped).Done("")
			continue
		}
		if st.run != nil && (len(st.Steps) > 0 || len(st.Kept) > 0) {
			if err := st.run(); err != nil {
				return p.fail(st, err)
			}
		}
		g := r.Step("gate: " + st.Short)
		g.Detail("%s", st.Gate)
		if st.gate != nil {
			if err := st.gate(); err != nil {
				g.Fail(err)
				return p.fail(st, fmt.Errorf("gate: %w", err))
			}
		}
		g.Done("passed")
	}
	return nil
}

// checkHandOff tries the hand-off's apply as a dry run before any stage
// runs, when the move has a hand-off left to do. Its own refusals, such as a
// secrets file other than the one beside the configuration or no admin key
// to sign with, would otherwise meet the move at stage 7, after DNS has
// moved and the record is gone.
func (p *Plan) checkHandOff() error {
	if !p.shared || p.checkSite == nil {
		return nil
	}
	last := p.Stages[len(p.Stages)-1]
	if last.Number != stageHandOff || last.Skipped != "" || len(last.Steps) == 0 {
		return nil
	}
	if err := p.checkSite(p.From); err != nil {
		return fmt.Errorf("the hand-off's apply of %s would be refused, so nothing was changed: %w\nFix that, then run %s gateway move --execute again", p.From, err, progname.Cmd())
	}
	return nil
}

func (p *Plan) fail(st *Stage, err error) error {
	head := fmt.Sprintf("gateway move stopped at stage %d (%s)", st.Number, st.Name)
	next := "Fix the cause and run " + progname.Cmd() + " gateway move --execute again: it resumes at the first stage with anything left to do"
	if st.Number == stageHandOff {
		// The record and the marker are gone by now, so the old site applies
		// like any other, with whatever flags its own refusal asks for.
		next = fmt.Sprintf("Fix the cause, then apply %s directly: %s deployment apply --site %s --only infra --execute (add the --overwrite it names if a file was edited on the host). %s gateway move --execute also resumes here", p.From, progname.Cmd(), p.From, progname.Cmd())
	}
	if pr := ui.Around(head, err, "Nothing after it ran.\n"+next); pr != nil {
		return pr
	}
	return fmt.Errorf("%s, and nothing after it ran: %w\n%s", head, err, next)
}
