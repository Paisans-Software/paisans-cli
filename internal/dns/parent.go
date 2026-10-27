package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"
	"time"
)

// The registrable domain above community.domain is the one name dns init
// writes at that holds names the deployment does not run. Everything that
// decides about its DMARC policy is here: whether the provider holds its
// zone (heldParent), whether the policy can be written (addParent,
// parentHazards), whether the policy dns init wrote still stands over no
// mail it knows of (governed), and which no-mail policies below it the
// policy covers (cover, coverHolds), for dns init and dns prune alike.

// splitParent separates the registrable domain's want from the rest.
func splitParent(wants []Want) (*Want, []Want) {
	var parent *Want
	rest := make([]Want, 0, len(wants))
	for _, w := range wants {
		if w.Parent {
			parent = &w
			continue
		}
		rest = append(rest, w)
	}
	return parent, rest
}

// heldParent is the zone holding the registrable domain's want w, and
// whether the provider holds one. A registrable domain the provider does not
// hold is not read, created or pruned.
func heldParent(ctx context.Context, provider Provider, w *Want, zones map[string]zone) (zone, bool, error) {
	if w == nil {
		return zone{}, false, nil
	}
	z, err := FindZone(ctx, provider, w.Name, zones)
	if errors.Is(err, errNoZone) {
		return zone{}, false, nil
	}
	if err != nil {
		return zone{}, false, err
	}
	return z, true, nil
}

// cover makes each no-mail DMARC policy that would be created, or is
// already there, Covered where the policy a receiver reads in its place is
// already there and rejects: nothing is written for it, and dns prune
// removes one this deployment made. A policy this plan creates covers
// nothing yet: until it reads back, nothing says the provider let it be
// written. A policy this plan creates or updates at a name between is
// judged as the plan writes it.
func (p *Plan) cover(ctx context.Context, provider Provider, zones map[string]zone, existing map[string][]Record) error {
	covering := map[string]Entry{}
	written := map[string][]Record{}
	for _, e := range p.Entries {
		if e.Type == "TXT" && wantPurpose(e.Want) == purposeDMARC {
			covering[e.Name] = e
			if e.Action == Create || e.Action == Update {
				written[e.Name] = []Record{{Type: e.Type, Name: e.Name, Content: e.Content}}
			}
		}
	}
	for i, e := range p.Entries {
		if e.CoveredBy == "" || e.Action != Create && e.Action != Present {
			continue
		}
		c, ok := covering[e.CoveredBy]
		if !ok || c.Action == Create || c.Action == Conflict {
			continue
		}
		holds, err := coverHolds(ctx, provider, zones, existing, written, e.Name, e.CoveredBy, c.held)
		if err != nil {
			return err
		}
		if holds {
			p.Entries[i].Action, p.Entries[i].Detail = Covered, "covered by the policy at "+e.CoveredBy
		}
	}
	return nil
}

// coverHolds reports whether covering, the records at coveredBy, cover the
// no-mail DMARC policy at name: they are one policy that rejects, and no
// name between holds a policy that does not. A receiver following RFC 7489
// reads coveredBy for a name with no policy of its own; one following
// DMARCbis walks up from the name and reads the first policy it finds, so a
// weaker one between would win. records caches the records at each
// "_dmarc" name read, and zones each zone found. written are the policies
// this plan creates or updates, by name: one there stands in for what the
// provider holds now.
//
// The policy must be strict, as the hostname's own is: under relaxed
// alignment a forger passes with any sibling name's SPF. A name between that
// is delegated to nameservers the provider does not hold is out of sight,
// and covers nothing.
func coverHolds(ctx context.Context, provider Provider, zones map[string]zone, records, written map[string][]Record, name, coveredBy string, covering []Record) (bool, error) {
	if !strictRejecting(covering) {
		return false, nil
	}
	org := strings.TrimPrefix(coveredBy, "_dmarc.")
	host := strings.TrimPrefix(name, "_dmarc.")
	for _, a, ok := strings.Cut(host, "."); ok && a != org && strings.HasSuffix(a, "."+org); _, a, ok = strings.Cut(a, ".") {
		// NS records at a name its own zone does not start at are a
		// delegation to nameservers the provider does not hold.
		z, err := FindZone(ctx, provider, a, zones)
		if err != nil && !errors.Is(err, errNoZone) {
			return false, err
		}
		if err == nil && z.name != a {
			at, _, err := readAt(ctx, provider, zones, records, a)
			if err != nil {
				return false, err
			}
			for _, r := range at {
				if strings.EqualFold(r.Type, "NS") {
					return false, nil
				}
			}
		}
		policy, planned := written["_dmarc."+a]
		if !planned {
			var err error
			if policy, _, err = readAt(ctx, provider, zones, records, "_dmarc."+a); err != nil {
				return false, err
			}
		}
		if !noPolicy(policy) && !strictRejecting(policy) {
			return false, nil
		}
	}
	return true, nil
}

// readAt is every record at name, in the zone that holds it, read once:
// records caches each name read. A name no zone the provider holds contains
// has none, and found is false.
func readAt(ctx context.Context, provider Provider, zones map[string]zone, records map[string][]Record, name string) ([]Record, bool, error) {
	if at, ok := records[name]; ok {
		return at, true, nil
	}
	z, err := FindZone(ctx, provider, name, zones)
	if errors.Is(err, errNoZone) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	at, err := provider.Records(ctx, z.id, name)
	if err != nil {
		return nil, false, err
	}
	records[name] = at
	return at, true, nil
}

// strictRejecting reports whether records hold exactly one DMARC policy, and
// it rejects with strict alignment for both DKIM and SPF.
func strictRejecting(records []Record) bool {
	if !oneRejecting(records) {
		return false
	}
	for _, r := range records {
		if competes(purposeDMARC, r) {
			t := tags(txtText(r.Content))
			return strings.EqualFold(t["adkim"], "s") && strings.EqualFold(t["aspf"], "s")
		}
	}
	return false
}

// hasMail reports whether r says its name receives mail, as an MX other than
// the null MX, or names a sender, as an SPF record other than the lockdown.
func hasMail(r Record) bool {
	switch strings.ToUpper(r.Type) {
	case "MX":
		return !(r.Priority == 0 && isRoot(r.Content))
	case "TXT":
		return competes(purposeSPF, r) && !satisfies(purposeSPF, r)
	}
	return false
}

// addParent plans the registrable domain's DMARC policy, after every other
// entry, since whether it can be written depends on which names this plan
// gives a policy of their own. parent may be nil.
func (p *Plan) addParent(ctx context.Context, provider Provider, parent *Want, zones map[string]zone, existing map[string][]Record) error {
	z, held, err := heldParent(ctx, provider, parent, zones)
	if err != nil || !held {
		return err
	}
	w := *parent
	records, err := provider.Records(ctx, z.id, w.Name)
	if err != nil {
		return err
	}
	existing[w.Name] = records
	action, detail := classify(w, records, nil)
	_, ours := ownNoMail(w, records, p.comment)
	switch {
	case action == Warn && noPolicy(records):
		// The whole zone is read only before the policy is written.
		hazards, err := parentHazards(ctx, provider, z, w, p.ownPolicies())
		if err != nil {
			return err
		}
		if len(hazards) == 0 {
			w.Advisory = false
			action, detail = Create, ""
		} else {
			detail = parentHeld(w, hazards)
		}
	case ours && action == Present:
		// The policy this deployment wrote must not stand over mail it
		// knows of: a sender the configuration names, and mail at the
		// domain itself.
		governed, err := p.governed(ctx, provider, z, w, zones, existing)
		if err != nil {
			return err
		}
		if len(governed) > 0 {
			action, detail = Conflict, parentGoverns(w, governed)
		}
	}
	p.Entries = append(p.Entries, Entry{Want: w, Zone: z.name, zoneID: z.id, Action: action, Detail: detail, held: records})
	return nil
}

// governed lists the mail the registrable domain's policy w, this
// deployment's own, governs that dns init knows of without reading the
// zone: each of w.Senders with no policy of its own, and an MX other than
// the null MX or an SPF record naming a sender at the domain itself.
func (p *Plan) governed(ctx context.Context, provider Provider, z zone, w Want, zones map[string]zone, existing map[string][]Record) ([]string, error) {
	var out []string
	own := p.ownPolicies()
	org := strings.TrimPrefix(w.Name, "_dmarc.")
	for _, s := range w.Senders {
		// A sender at the domain itself is governed by w, which is its
		// own policy and this deployment's.
		if s == org {
			out = append(out, fmt.Sprintf("%s, a sender this configuration names", s))
			continue
		}
		records, _, err := readAt(ctx, provider, zones, existing, "_dmarc."+s)
		if err != nil {
			return nil, err
		}
		if !own[s] && noPolicy(records) {
			out = append(out, fmt.Sprintf("%s, a sender this configuration names", s))
		}
	}
	records, _, err := readAt(ctx, provider, zones, existing, org)
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		if hasMail(r) {
			out = append(out, fmt.Sprintf("an %s record at %s", map[bool]string{true: "MX", false: "SPF"}[strings.EqualFold(r.Type, "MX")], org))
		}
	}
	return out, nil
}

// ownPolicies are the names that hold a DMARC policy of their own once this
// plan is executed, from what it creates: a receiver reads a name's own
// policy before the registrable domain's. A policy the registrable domain's
// would cover is not one: written there, that one is never created.
func (p *Plan) ownPolicies() map[string]bool {
	out := map[string]bool{}
	for _, e := range p.Entries {
		if e.Action == Create && e.CoveredBy == "" && wantPurpose(e.Want) == purposeDMARC {
			out[strings.TrimPrefix(e.Name, "_dmarc.")] = true
		}
	}
	return out
}

// parentHazards lists what in the registrable domain's zone z, and in every
// zone delegated from it, has mail that w, the policy at the domain, would
// govern: a name with no DMARC policy of its own that receives mail (an MX
// other than the null MX), names senders in its SPF record, or publishes a
// DKIM key, and a sender the configuration names (w.Senders) with no policy
// of its own. own are names that will hold a policy this run creates. The
// domain itself never has a policy of its own: w is it. A zone delegated to
// nameservers the provider does not show is a hazard too: nothing says what
// mail its names have.
func parentHazards(ctx context.Context, provider Provider, z zone, w Want, own map[string]bool) ([]string, error) {
	var records []Record
	queue := []zone{z}
	seen := map[string]bool{z.name: true}
	var hazards []string
	for len(queue) > 0 {
		at := queue[0]
		queue = queue[1:]
		list, err := provider.AllRecords(ctx, at.id)
		if err != nil {
			return nil, fmt.Errorf("dns: listing zone %s: %w", at.name, err)
		}
		for _, r := range list {
			name := normalise(r.Name)
			if !strings.EqualFold(r.Type, "NS") || name == at.name || seen[name] {
				continue
			}
			seen[name] = true
			id, found, err := provider.LookupZone(ctx, name)
			if err != nil {
				return nil, fmt.Errorf("dns: looking up zone %s: %w", name, err)
			}
			if found {
				queue = append(queue, zone{id: id, name: name})
			} else {
				hazards = append(hazards, fmt.Sprintf("%s, delegated to nameservers %s does not hold", name, provider.Name()))
			}
		}
		records = append(records, list...)
	}

	policy := map[string]bool{}
	for name := range own {
		policy[name] = true
	}
	for _, r := range records {
		name := normalise(r.Name)
		if strings.HasPrefix(name, "_dmarc.") && !noPolicy([]Record{r}) {
			policy[strings.TrimPrefix(name, "_dmarc.")] = true
		}
	}
	delete(policy, strings.TrimPrefix(w.Name, "_dmarc."))
	for _, name := range w.Senders {
		if !policy[name] {
			hazards = append(hazards, fmt.Sprintf("%s, a sender this configuration names", name))
		}
	}
	var cnames []Record
	for _, r := range records {
		name := normalise(r.Name)
		typ := strings.ToUpper(r.Type)
		var host, what string
		switch {
		case typ == "MX" && hasMail(r):
			host, what = name, fmt.Sprintf("an MX record at %s", name)
		case strings.Contains(name, "._domainkey."):
			_, host, _ = strings.Cut(name, "._domainkey.")
			if typ == "CNAME" || typ == "TXT" && tags(txtText(r.Content))["p"] != "" {
				what = fmt.Sprintf("a DKIM key at %s", name)
			}
		case typ == "TXT" && !strings.HasPrefix(name, "_") && hasMail(r):
			host, what = name, fmt.Sprintf("an SPF record naming senders at %s", name)
		case typ == "CNAME" && !strings.HasPrefix(name, "_") && !policy[name]:
			cnames = append(cnames, r)
		}
		if what != "" && !policy[host] {
			hazards = append(hazards, what)
		}
	}
	// A name that is a CNAME has its target's MX and SPF, and a wildcard
	// CNAME gives them to every name it reaches. They are resolved through
	// public DNS only while nothing else stops the policy, and only until
	// the first that has mail: one is enough to stop it.
	if len(hazards) == 0 {
		for _, r := range cnames {
			if lookupMail(ctx, r.Content) {
				hazards = append(hazards, fmt.Sprintf("a CNAME at %s to %s, which has mail", normalise(r.Name), r.Content))
				break
			}
		}
	}
	sort.Strings(hazards)
	return slices.Compact(hazards), nil
}

// lookupMail reports whether name, resolved through public DNS, receives mail
// (an MX other than the null MX) or names a sender in its SPF record. A
// lookup that fails other than by the name not existing says it might.
var lookupMail = func(ctx context.Context, name string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resolver := net.DefaultResolver
	unknown := func(err error) bool {
		var dnsErr *net.DNSError
		return !errors.As(err, &dnsErr) || !dnsErr.IsNotFound
	}
	mx, err := resolver.LookupMX(ctx, name)
	if err != nil && unknown(err) {
		return true
	}
	for _, m := range mx {
		if hasMail(Record{Type: "MX", Content: m.Host, Priority: int(m.Pref)}) {
			return true
		}
	}
	txt, err := resolver.LookupTXT(ctx, name)
	if err != nil && unknown(err) {
		return true
	}
	for _, t := range txt {
		if hasMail(Record{Type: "TXT", Content: t}) {
			return true
		}
	}
	return false
}

// parentHeld is the note for a registrable domain with no DMARC policy that
// dns init does not write one at, because of hazards.
func parentHeld(w Want, hazards []string) string {
	return fmt.Sprintf("%s is the registrable domain above community.domain, and has no DMARC policy. A receiver applies its policy to every name under it that has none of its own, and these have mail and no policy of their own, so the policy is whoever runs the domain's to set rather than this toolkit's to write: %s. Without p=reject and sp=reject there, each hostname this deployment publishes carries its own.",
		strings.TrimPrefix(w.Name, "_dmarc."), strings.Join(hazards, "; "))
}

// parentGoverns is the note for a registrable domain whose policy this
// deployment's dns init wrote, and which now governs names with mail.
func parentGoverns(w Want, hazards []string) string {
	return fmt.Sprintf("the DMARC policy at %s is the one this deployment's dns init wrote, when nothing under %s had mail. These have mail and no policy of their own, so a receiver rejects their mail under it: %s. Give each its own DMARC policy, or delete this one by hand and run dns init again, which then gives each hostname this deployment publishes its own.",
		w.Name, strings.TrimPrefix(w.Name, "_dmarc."), strings.Join(hazards, "; "))
}
