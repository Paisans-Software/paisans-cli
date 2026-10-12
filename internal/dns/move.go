package dns

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// A gateway move's DNS is the one place outside prune that deletes a record,
// and it deletes only through ExecutePrune. Founder decision
// (docs/decisions.md, 2026-10-11): it is one of the few automated DNS
// deletions allowed, and it is narrow on purpose. A record is deleted only
// when every one of these holds:
//
//  1. it carries exactly RecordComment of this deployment;
//  2. it is an A or AAAA record, not proxied;
//  3. its name is a hostname the gateway serves in this configuration
//     (GatewayHostnames), never a site endpoint's or a monitor's;
//  4. its address is the old gateway's public_address or public_address6,
//     and not also one of the new gateway's;
//  5. every record the new gateway needs at every such name is present, so
//     no name is left without the new gateway's address.
//
// The caller adds a sixth that this package cannot see: the new gateway has
// just been verified answering for every hostname. Any other record at such
// a name that points at the old gateway is kept and named, and while one
// stands nothing is deleted, since the old gateway cannot be stopped while a
// name still sends visitors to it.

// DefaultTTL is the wait, in seconds, for a deleted record whose TTL the
// provider did not report: Cloudflare's automatic TTL, which every record
// dns init creates carries (cloudflare.go, cfAutoTTL).
const DefaultTTL = cfAutoTTL

// MovePlan is a gateway move's DNS, compared with the provider: the new
// gateway's address records to create beside the old gateway's, and the old
// gateway's to delete once the new one is verified.
type MovePlan struct {
	Provider string
	// From is the old gateway's site and To the new one's.
	From, To string
	// Creates are the new gateway's records still missing, Present those
	// already there, and Conflicts names where something other than the two
	// gateways' records is in the way.
	Creates, Present, Conflicts []Entry
	// Deletes are the old gateway's records the rules above allow deleting.
	Deletes []PruneEntry
	// Kept are records pointing at the old gateway that the rules protect,
	// each with its reasons.
	Kept    []PruneEntry
	comment string
}

// Ready reports whether every record the new gateway needs is present.
func (p *MovePlan) Ready() bool { return len(p.Creates) == 0 && len(p.Conflicts) == 0 }

// Clear reports whether no record at a gateway hostname points at the old
// gateway any more.
func (p *MovePlan) Clear() bool { return len(p.Deletes) == 0 && len(p.Kept) == 0 }

// TTL is the longest time to live, in seconds, of the records the plan
// deletes: how long a resolver may go on answering with the old gateway's
// address once they are gone. A record whose TTL is unknown counts as
// DefaultTTL.
func (p *MovePlan) TTL() int {
	longest := 0
	for _, e := range p.Deletes {
		ttl := e.TTL
		if ttl <= 0 {
			ttl = DefaultTTL
		}
		longest = max(longest, ttl)
	}
	return longest
}

// moveWants are the address records the new gateway needs: Desired's, for
// the hostnames the gateway serves.
func moveWants(cfg *config.Config) ([]Want, error) {
	wants, err := Desired(cfg)
	if err != nil {
		return nil, err
	}
	var out []Want
	for _, w := range wants {
		if w.ViaGateway {
			out = append(out, w)
		}
	}
	return out, nil
}

// oldAddresses are from's public addresses, canonical, less any the new
// gateway shares: a record at a shared address is the new gateway's too.
func oldAddresses(cfg *config.Config, from, to string) []string {
	var out []string
	own := siteAddresses(cfg, to)
	for _, ip := range siteAddresses(cfg, from) {
		if !mapHas(own, ip) && !slices.Contains(out, ip) {
			out = append(out, ip)
		}
	}
	sort.Strings(out)
	return out
}

// BuildMove compares the gateway hostnames' records with the provider for a
// move from the site from to the configuration's one gateway. It reads and
// never writes.
func BuildMove(ctx context.Context, provider Provider, cfg *config.Config, from string) (*MovePlan, error) {
	gateways := cfg.GatewaySites()
	if len(gateways) != 1 {
		return nil, fmt.Errorf("dns: a gateway move needs exactly one site with the gateway role, and %d hold it", len(gateways))
	}
	to := gateways[0]
	if _, ok := cfg.Sites[from]; !ok || from == to {
		return nil, fmt.Errorf("dns: %q is not a declared site other than the gateway %s", from, to)
	}
	old := oldAddresses(cfg, from, to)
	if len(old) == 0 {
		return nil, fmt.Errorf("dns: %s declares no public address apart from %s's, so no record can be told apart as the old gateway's", from, to)
	}
	wants, err := moveWants(cfg)
	if err != nil {
		return nil, err
	}
	plan := &MovePlan{Provider: provider.Name(), From: from, To: to, comment: RecordComment(cfg.Deployment())}
	zones := map[string]zone{}
	wantedTypes := map[string]map[string]bool{}
	for _, w := range wants {
		if wantedTypes[w.Name] == nil {
			wantedTypes[w.Name] = map[string]bool{}
		}
		wantedTypes[w.Name][w.Type] = true
	}
	listed := map[string]bool{}
	for _, w := range wants {
		z, err := FindZone(ctx, provider, w.Name, zones)
		if err != nil {
			return nil, err
		}
		records, err := provider.Records(ctx, z.id, w.Name)
		if err != nil {
			return nil, err
		}
		action, detail := classify(w, notAt(records, old), wantedTypes[w.Name])
		e := Entry{Want: w, Zone: z.name, zoneID: z.id, Action: action, Detail: detail}
		switch action {
		case Create:
			plan.Creates = append(plan.Creates, e)
		case Present:
			plan.Present = append(plan.Present, e)
		default:
			plan.Conflicts = append(plan.Conflicts, e)
		}
		if listed[w.Name] {
			continue
		}
		listed[w.Name] = true
		for _, r := range records {
			typ := strings.ToUpper(r.Type)
			ip := canonical(r.Content)
			if (typ != "A" && typ != "AAAA") || !slices.Contains(old, ip) {
				continue
			}
			pe := PruneEntry{Record: r, Zone: z.name, zoneID: z.id, Action: Remove}
			if r.Comment != plan.comment {
				pe.Reasons = append(pe.Reasons, fmt.Sprintf("it does not carry %q, so this deployment's dns init did not make it; delete it by hand once %s serves every hostname", plan.comment, to))
			}
			if r.Proxied {
				pe.Reasons = append(pe.Reasons, "it is proxied through the provider's CDN, which dns init never does; change it by hand")
			}
			if len(pe.Reasons) > 0 {
				pe.Action = Keep
				plan.Kept = append(plan.Kept, pe)
				continue
			}
			plan.Deletes = append(plan.Deletes, pe)
		}
	}
	return plan, nil
}

// notAt is records less the address records pointing at one of addresses.
func notAt(records []Record, addresses []string) []Record {
	var out []Record
	for _, r := range records {
		typ := strings.ToUpper(r.Type)
		if (typ == "A" || typ == "AAAA") && slices.Contains(addresses, canonical(r.Content)) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// ExecuteMoveAdd creates the new gateway's missing records beside the old
// gateway's, with this deployment's comment, then reads each back. Like
// Execute it refuses to write anything while a conflict stands, and a re-run
// after a failure finds what it created present.
func ExecuteMoveAdd(ctx context.Context, provider Provider, plan *MovePlan, r ui.Reporter) error {
	if n := len(plan.Conflicts); n > 0 {
		return conflictError{n}
	}
	for _, e := range plan.Creates {
		step := r.Step(fmt.Sprintf("create %s %s", e.Type, e.Name))
		step.Detail("%s, zone %s, beside %s's", e.Content, e.Zone, plan.From)
		record := Record{Type: e.Type, Name: e.Name, Content: e.Content, Comment: plan.comment}
		if err := provider.Create(ctx, e.zoneID, record); err != nil {
			err = fmt.Errorf("dns: creating %s %s: %w", e.Type, e.Name, err)
			step.Fail(err)
			return err
		}
		back, err := provider.Records(ctx, e.zoneID, e.Name)
		if err != nil {
			err = fmt.Errorf("dns: reading back %s %s: %w", e.Type, e.Name, err)
			step.Fail(err)
			return err
		}
		if action, _ := classify(e.Want, notAt(back, e.Former), map[string]bool{e.Type: true, "A": true, "AAAA": true}); action != Present {
			err := fmt.Errorf("dns: %s %s was created but does not read back as %s unproxied. Check it in the provider's console before re-running", e.Type, e.Name, e.Content)
			step.Fail(err)
			return err
		}
		step.Done("")
	}
	return nil
}

// ExecuteMoveRemove deletes the old gateway's records the plan allows, through
// ExecutePrune, which confirms each delete. It deletes nothing unless every
// record the new gateway needs is present and no record pointing at the old
// gateway is protected: a name the move could not clear would still send
// visitors to a gateway about to stop. The caller verifies the new gateway
// first.
func ExecuteMoveRemove(ctx context.Context, provider Provider, plan *MovePlan, r ui.Reporter) error {
	if !plan.Ready() {
		return fmt.Errorf("dns: %d record(s) %s needs are not present yet, so no record of %s's is deleted", len(plan.Creates)+len(plan.Conflicts), plan.To, plan.From)
	}
	if len(plan.Kept) > 0 {
		var names []string
		for _, e := range plan.Kept {
			names = append(names, fmt.Sprintf("%s %s -> %s (%s)", e.Type, e.Name, e.Content, strings.Join(e.Reasons, "; ")))
		}
		return fmt.Errorf("dns: records point at %s that this move may not delete: %s. Nothing was deleted", plan.From, strings.Join(names, ", "))
	}
	return ExecutePrune(ctx, provider, &PrunePlan{Provider: plan.Provider, Entries: plan.Deletes, comment: plan.comment}, r)
}

// MonitorRecordsAt returns the hostnames of apps a monitor on another site
// serves whose records, made by dns init for this deployment, still point at
// one of site's addresses, sorted. A move moves only the gateway's hostnames,
// so a monitor that moved off the old gateway with it leaves its record
// behind, and stopping the old gateway's Caddy would take it down.
func MonitorRecordsAt(ctx context.Context, provider Provider, cfg *config.Config, site string) ([]string, error) {
	addresses := map[string]bool{}
	for _, ip := range siteAddresses(cfg, site) {
		addresses[canonical(ip)] = true
	}
	comment := RecordComment(cfg.Deployment())
	zones := map[string]zone{}
	var out []string
	for _, name := range cfg.AppNames() {
		monitor, ok := render.ServedBy(cfg, name)
		if !ok || monitor == site {
			continue
		}
		host := cfg.Apps[name].Hostname
		z, err := FindZone(ctx, provider, host, zones)
		if err != nil {
			return nil, err
		}
		records, err := provider.Records(ctx, z.id, host)
		if err != nil {
			return nil, err
		}
		for _, r := range records {
			typ := strings.ToUpper(r.Type)
			if (typ == "A" || typ == "AAAA") && r.Comment == comment && addresses[canonical(r.Content)] && !slices.Contains(out, host) {
				out = append(out, host)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// FormerGateways is every declared site other than the gateway that a
// record this deployment's dns init made, at a hostname the gateway serves,
// still points at: a gateway a move has not finished leaving. It reads and
// never writes.
func FormerGateways(ctx context.Context, provider Provider, cfg *config.Config) ([]string, error) {
	gateways := cfg.GatewaySites()
	if len(gateways) != 1 {
		return nil, nil
	}
	wants, err := moveWants(cfg)
	if err != nil {
		return nil, err
	}
	bySite := map[string]string{} // canonical address -> site
	for _, name := range cfg.SiteNames() {
		if name == gateways[0] {
			continue
		}
		for _, ip := range oldAddresses(cfg, name, gateways[0]) {
			bySite[ip] = name
		}
	}
	comment := RecordComment(cfg.Deployment())
	zones := map[string]zone{}
	seen := map[string]bool{}
	var out []string
	for _, w := range wants {
		if seen[w.Name] {
			continue
		}
		seen[w.Name] = true
		z, err := FindZone(ctx, provider, w.Name, zones)
		if err != nil {
			return nil, err
		}
		records, err := provider.Records(ctx, z.id, w.Name)
		if err != nil {
			return nil, err
		}
		for _, r := range records {
			typ := strings.ToUpper(r.Type)
			if (typ != "A" && typ != "AAAA") || r.Comment != comment {
				continue
			}
			if site := bySite[canonical(r.Content)]; site != "" && !slices.Contains(out, site) {
				out = append(out, site)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
