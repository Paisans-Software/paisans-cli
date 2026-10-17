package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"
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

// cover makes each no-mail DMARC policy that would be created Covered where
// the policy a receiver reads in its place is already there and rejects. One
// this plan creates covers nothing yet: until it reads back, nothing says
// the provider let it be written, and dns prune removes the policies it
// covers once it does.
func (p *Plan) cover(ctx context.Context, provider Provider, zones map[string]zone, existing map[string][]Record) error {
	covering := map[string]Entry{}
	for _, e := range p.Entries {
		if e.Type == "TXT" && wantPurpose(e.Want) == purposeDMARC {
			covering[e.Name] = e
		}
	}
	for i, e := range p.Entries {
		if e.CoveredBy == "" || e.Action != Create {
			continue
		}
		c, ok := covering[e.CoveredBy]
		if !ok || c.Action == Create || c.Action == Conflict {
			continue
		}
		holds, err := coverHolds(ctx, provider, zones, existing, e.Name, e.CoveredBy, c.held)
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
// "_dmarc" name read, and zones each zone found.
func coverHolds(ctx context.Context, provider Provider, zones map[string]zone, records map[string][]Record, name, coveredBy string, covering []Record) (bool, error) {
	if !oneRejecting(covering) {
		return false, nil
	}
	org := strings.TrimPrefix(coveredBy, "_dmarc.")
	host := strings.TrimPrefix(name, "_dmarc.")
	for _, a, ok := strings.Cut(host, "."); ok && a != org && strings.HasSuffix(a, "."+org); _, a, ok = strings.Cut(a, ".") {
		at, read := records["_dmarc."+a]
		if !read {
			z, err := FindZone(ctx, provider, "_dmarc."+a, zones)
			if err != nil && !errors.Is(err, errNoZone) {
				return false, err
			}
			if err == nil {
				if at, err = provider.Records(ctx, z.id, "_dmarc."+a); err != nil {
					return false, err
				}
			}
			records["_dmarc."+a] = at
		}
		if hasDMARC(at) && !oneRejecting(at) {
			return false, nil
		}
	}
	return true, nil
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
	sort.SliceStable(p.Entries, func(i, j int) bool {
		a, b := p.Entries[i], p.Entries[j]
		return a.Name < b.Name || a.Name == b.Name && a.Type < b.Type
	})
	return nil
}

// governed lists the mail the registrable domain's policy w, this
// deployment's own, governs that dns init knows of without reading the
// zone: each of w.Senders with no policy of its own, and an MX other than
// the null MX or an SPF record naming a sender at the domain itself.
func (p *Plan) governed(ctx context.Context, provider Provider, z zone, w Want, zones map[string]zone, existing map[string][]Record) ([]string, error) {
	read := func(name string) ([]Record, error) {
		if records, ok := existing[name]; ok {
			return records, nil
		}
		nz, err := FindZone(ctx, provider, name, zones)
		if errors.Is(err, errNoZone) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		records, err := provider.Records(ctx, nz.id, name)
		existing[name] = records
		return records, err
	}
	var out []string
	own := p.ownPolicies()
	for _, s := range w.Senders {
		records, err := read("_dmarc." + s)
		if err != nil {
			return nil, err
		}
		if !own[s] && noPolicy(records) {
			out = append(out, fmt.Sprintf("%s, a sender this configuration names", s))
		}
	}
	org := strings.TrimPrefix(w.Name, "_dmarc.")
	records, err := read(org)
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		switch {
		case strings.EqualFold(r.Type, "MX") && !(r.Priority == 0 && isRoot(r.Content)):
			out = append(out, fmt.Sprintf("an MX record at %s", org))
		case strings.EqualFold(r.Type, "TXT") && competes(purposeSPF, r) && !satisfies(purposeSPF, r):
			out = append(out, fmt.Sprintf("an SPF record naming senders at %s", org))
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
		if strings.HasPrefix(name, "_dmarc.") && (competes(purposeDMARC, r) || strings.EqualFold(r.Type, "CNAME")) {
			policy[strings.TrimPrefix(name, "_dmarc.")] = true
		}
	}
	delete(policy, strings.TrimPrefix(w.Name, "_dmarc."))
	for _, name := range w.Senders {
		if !policy[name] {
			hazards = append(hazards, fmt.Sprintf("%s, a sender this configuration names", name))
		}
	}
	for _, r := range records {
		name := normalise(r.Name)
		typ := strings.ToUpper(r.Type)
		var host, what string
		switch {
		case typ == "MX" && !(r.Priority == 0 && isRoot(r.Content)):
			host, what = name, fmt.Sprintf("an MX record at %s", name)
		case strings.Contains(name, "._domainkey."):
			_, host, _ = strings.Cut(name, "._domainkey.")
			if typ == "CNAME" || typ == "TXT" && tags(txtText(r.Content))["p"] != "" {
				what = fmt.Sprintf("a DKIM key at %s", name)
			}
		case typ == "TXT" && !strings.HasPrefix(name, "_") && competes(purposeSPF, r) && !satisfies(purposeSPF, r):
			host, what = name, fmt.Sprintf("an SPF record naming senders at %s", name)
		case typ == "CNAME" && !strings.HasPrefix(name, "_") && !strings.HasPrefix(name, "*."):
			// A name that is a CNAME has its target's MX and SPF.
			if !policy[name] && lookupMail(ctx, name) {
				host, what = name, fmt.Sprintf("a CNAME at %s to %s, which has mail", name, r.Content)
			}
		}
		if what != "" && !policy[host] {
			hazards = append(hazards, what)
		}
	}
	sort.Strings(hazards)
	return slices.Compact(hazards), nil
}

// lookupMail reports whether name, resolved through public DNS, receives mail
// (an MX other than the null MX) or names a sender in its SPF record. A
// lookup that fails other than by the name not existing says it might.
var lookupMail = func(ctx context.Context, name string) bool {
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
		if !(m.Pref == 0 && isRoot(m.Host)) {
			return true
		}
	}
	txt, err := resolver.LookupTXT(ctx, name)
	if err != nil && unknown(err) {
		return true
	}
	for _, t := range txt {
		r := Record{Type: "TXT", Content: t}
		if competes(purposeSPF, r) && !satisfies(purposeSPF, r) {
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

// dropCovered removes from wanted each no-mail DMARC policy in wants that a
// rejecting policy covers, for dns prune. comment is this deployment's
// RecordComment; zones caches each zone found.
//
// A no-mail DMARC policy whose name a rejecting policy covers is not
// wanted. The covering policy is judged as dns init judges it, without
// any record of this deployment's this prune deletes, since one
// covering only until it is deleted covers nothing.
func dropCovered(ctx context.Context, provider Provider, comment string, wants []Want, wanted map[string][]string, zones map[string]zone) error {
	covering := map[string][]Record{}
	held := map[string][]Record{}
	for _, w := range wants {
		if w.CoveredBy == "" {
			continue
		}
		kept, ok := covering[w.CoveredBy]
		if !ok {
			z, err := FindZone(ctx, provider, w.CoveredBy, zones)
			if err != nil && !errors.Is(err, errNoZone) {
				return err
			}
			if err == nil {
				records, err := provider.Records(ctx, z.id, w.CoveredBy)
				if err != nil {
					return err
				}
				for _, r := range records {
					if r.Comment == comment && isLockdown(r) && wanted["TXT "+w.CoveredBy] == nil {
						continue
					}
					kept = append(kept, r)
				}
			}
			covering[w.CoveredBy] = kept
		}
		holds, err := coverHolds(ctx, provider, zones, held, w.Name, w.CoveredBy, kept)
		if err != nil {
			return err
		}
		if holds {
			delete(wanted, w.Type+" "+w.Name)
		}
	}
	return nil
}
