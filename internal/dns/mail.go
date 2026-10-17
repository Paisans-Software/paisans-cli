package dns

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/publicsuffix"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// A name that publishes an address can be the sender of a forged message,
// whether or not anybody ever sends mail from it, and a receiver has nothing
// to refuse that forgery by unless the name says it sends none. A receiver
// enforcing DMARC reads the registrable domain's policy for every name under
// it that has none of its own, so one policy that rejects says it for every
// hostname, and a hostname gets a record of its own only where no policy the
// deployment may write covers it.
//
// The records are the ones a DNS provider publishes for a domain that sends no
// mail:
//
//   - SPF "v=spf1 -all" on the domain.
//   - A null MX (RFC 7505) on the domain: it accepts no mail, so nothing is
//     delivered to it and no bounce is sent on its behalf.
//   - An empty key at "*._domainkey" under the domain: no selector there
//     verifies a signature.
//   - DMARC at "_dmarc" under the domain: reject what fails, on the domain
//     (p) and on every name under it (sp), with strict alignment. When
//     community.domain is the registrable domain this one record covers
//     every hostname under it.
//   - Below the registrable domain, the same DMARC at "_dmarc" under that
//     domain, since community.domain's policy reaches none of the names
//     under it and the registrable domain's reaches every one. That domain
//     holds names the deployment does not run, so the policy is written only
//     where there is none and nothing under the domain has mail without a
//     policy of its own (parentHazards); otherwise it is checked, and warned
//     about when missing or weaker than reject.
//   - Where the registrable domain's policy is one dns init only checks or
//     may not write, and does not reject, the same DMARC at every
//     hostname's own "_dmarc" (CoveredBy), and at community.domain's.
//   - SPF "v=spf1 -all" on a hostname that is itself a registrable domain:
//     a DMARC policy there would govern names the deployment does not run,
//     and SPF applies to that exact name alone.
//
// A name has mail of its own when community.mail_domains lists it, or when an
// smtp from_address an app sends with is at it. The second is derived, so a
// sender the toolkit configures can never be locked out by the toolkit's own
// records. A mail name gets none of these records: whoever runs its mail owns
// its SPF, its MX, its DKIM keys and its DMARC policy. The hostnames under it
// get their own DMARC policy while its policy does not reject, and dns init
// warns when a mail name's DMARC policy, under community.domain, is missing
// or weaker than reject.
//
// The one exception is a mail name that holds no MX record and no DMARC
// policy. It sends mail, through a relay, and receives none, so nobody
// runs a mail service there whose policy dns init would be overriding, and
// dns init writes dmarcSender. Relaxed alignment lets mail the relay signs
// for any name under the same organizational domain pass. An existing
// policy, however weak, is still only warned about.

// The lockdown contents. These are what dns init writes and the only TXT and
// MX contents dns prune will delete.
const (
	spfLockdown   = "v=spf1 -all"
	dkimLockdown  = "v=DKIM1; p="
	dmarcLockdown = "v=DMARC1; p=reject; sp=reject; adkim=s; aspf=s;"
	// dmarcSender is the policy for a domain that sends mail and receives
	// none. dns prune never deletes it: the domain's mail depends on it.
	dmarcSender = "v=DMARC1; p=reject; sp=reject;"
	// nullMX is RFC 7505's: preference 0, target the root.
	nullMX = "."
)

// MailNames are the names whose mail is somebody else's to configure, each
// with the configuration keys that make it one. They come from
// community.mail_domains, and from the domain of every smtp from_address an
// app that sends mail uses.
func MailNames(cfg *config.Config) map[string][]string {
	out := map[string][]string{}
	add := func(name, source string) {
		name = normalise(name)
		if name == "" {
			return
		}
		for _, s := range out[name] {
			if s == source {
				return
			}
		}
		out[name] = append(out[name], source)
	}
	for _, name := range cfg.Community.MailDomains {
		add(name, "community.mail_domains")
	}
	sender := func(address string) string {
		at := strings.LastIndex(address, "@")
		if at < 0 {
			return ""
		}
		return address[at+1:]
	}
	if domain := sender(cfg.SMTP.FromAddress); domain != "" {
		add(domain, "smtp.from_address")
	}
	for _, appName := range cfg.AppNames() {
		app := cfg.Apps[appName]
		if !blueprints.SendsMail(app.Blueprint) || app.SMTP == nil {
			continue
		}
		if domain := sender(app.SMTP.FromAddress); domain != "" {
			add(domain, fmt.Sprintf("apps.%s.smtp.from_address", appName))
		}
	}
	return out
}

// mailWants are the lockdown records for a deployment whose address records
// are addresses, already derived and deduplicated.
func mailWants(cfg *config.Config, addresses []Want) []Want {
	mail := MailNames(cfg)
	domain := normalise(cfg.Community.Domain)
	var out []Want

	// hosts are the names without mail of their own that get an address
	// record, each with the keys that publish it.
	hosts := map[string][]string{}
	var names []string
	for _, w := range addresses {
		if _, ok := mail[w.Name]; ok {
			continue
		}
		if _, ok := hosts[w.Name]; !ok {
			names = append(names, w.Name)
		}
		for _, s := range w.Sources {
			if !contains(hosts[w.Name], s) {
				hosts[w.Name] = append(hosts[w.Name], s)
			}
		}
	}
	sort.Strings(names)

	if domain == "" {
		return out
	}
	// A mail name may hold a no-mail record from before it had mail, and
	// that record would turn its mail away. Each is looked for, at a name in
	// this deployment's zones, so that dns init stops until prune has
	// removed it.
	var mailNames []string
	for name := range mail {
		if name == domain || strings.HasSuffix(name, "."+domain) {
			mailNames = append(mailNames, name)
		}
	}
	sort.Strings(mailNames)
	for _, name := range mailNames {
		sources := mail[name]
		out = append(out, Want{Type: "TXT", Name: name, Content: spfLockdown, Sources: sources, Leftover: true})
		if name == domain {
			out = append(out,
				Want{Type: "MX", Name: name, Content: nullMX, Sources: sources, Leftover: true},
				Want{Type: "TXT", Name: "*._domainkey." + name, Content: dkimLockdown, Sources: sources, Leftover: true},
			)
		}
	}
	dmarc := "_dmarc." + domain
	// parent is the registrable domain above community.domain, or empty
	// when community.domain is one.
	parent := orgDomain(domain)
	if parent == domain {
		parent = ""
	}
	for _, name := range mailNames {
		if name != domain {
			out = append(out, Want{Type: "TXT", Name: "_dmarc." + name, Content: dmarcLockdown, Sources: mail[name], Advisory: true})
		}
	}
	// A receiver that finds no policy at a name's own _dmarc uses the
	// registrable domain's. Where that is community.domain's own lockdown
	// policy, it covers the hostname. Where it is a policy dns init only
	// checks or may not write, the hostname gets its own, Covered while that
	// one rejects. A hostname that is itself a registrable domain gets SPF
	// instead: a DMARC policy there would govern names the deployment does
	// not run.
	_, domainMail := mail[domain]
	for _, name := range names {
		if name == domain {
			continue
		}
		org := orgDomain(name)
		switch {
		case org == "":
		case org == name:
			out = append(out, Want{Type: "TXT", Name: name, Content: spfLockdown, Sources: hosts[name]})
		case org == domain && !domainMail:
		default:
			w := Want{Type: "TXT", Name: "_dmarc." + name, Content: dmarcLockdown, Sources: hosts[name]}
			if org == domain || org == parent {
				w.CoveredBy = "_dmarc." + org
			}
			out = append(out, w)
		}
	}
	source := []string{"community.domain"}
	if parent != "" {
		out = append(out, Want{Type: "TXT", Name: "_dmarc." + parent, Content: dmarcLockdown, Sources: source, Advisory: true, Parent: true})
	}
	if sources, ok := mail[domain]; ok {
		out = append(out, Want{Type: "TXT", Name: dmarc, Content: dmarcLockdown, Sources: sources, Advisory: true})
		return out
	}
	coveredBy := ""
	if parent != "" {
		coveredBy = "_dmarc." + parent
	}
	out = append(out,
		Want{Type: "TXT", Name: domain, Content: spfLockdown, Sources: source},
		Want{Type: "MX", Name: domain, Content: nullMX, Priority: 0, Sources: source},
		Want{Type: "TXT", Name: "*._domainkey." + domain, Content: dkimLockdown, Sources: source},
		Want{Type: "TXT", Name: dmarc, Content: dmarcLockdown, Sources: source, CoveredBy: coveredBy},
	)
	return out
}

// hasDMARC reports whether any of records says DMARC.
func hasDMARC(records []Record) bool {
	for _, r := range records {
		if competes(purposeDMARC, r) {
			return true
		}
	}
	return false
}

// parentHazards lists what in the registrable domain's zone z, and in every
// zone delegated from it, has mail that a policy written at the domain would
// govern: a name with no DMARC policy of its own that receives mail (an MX
// other than the null MX), names senders in its SPF record, or publishes a
// DKIM key. own are names that will hold a policy this run creates. A zone
// delegated to nameservers the provider does not show is a hazard too:
// nothing says what mail its names have.
func parentHazards(ctx context.Context, provider Provider, z zone, own map[string]bool) ([]string, error) {
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
		if strings.HasPrefix(name, "_dmarc.") && competes(purposeDMARC, r) {
			policy[strings.TrimPrefix(name, "_dmarc.")] = true
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
		}
		if what != "" && !policy[host] {
			hazards = append(hazards, what)
		}
	}
	sort.Strings(hazards)
	return slices.Compact(hazards), nil
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

// satisfiesAll reports whether every DMARC policy among records rejects.
func satisfiesAll(records []Record) bool {
	for _, r := range records {
		if competes(purposeDMARC, r) && !satisfies(purposeDMARC, r) {
			return false
		}
	}
	return true
}

// orgDomain is a name's registrable domain under the public suffix list,
// which is where a DMARC receiver looks for a policy the name has none of,
// or empty for a name that has none.
func orgDomain(name string) string {
	org, err := publicsuffix.EffectiveTLDPlusOne(name)
	if err != nil {
		return ""
	}
	return org
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// txtText is a TXT record's text as a receiver reads it. A provider may
// report it as one or more quoted strings, which a receiver joins with
// nothing between them, or bare.
func txtText(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, `"`) {
		return content
	}
	var b strings.Builder
	in, escaped := false, false
	for _, c := range content {
		switch {
		case escaped:
			b.WriteRune(c)
			escaped = false
		case in && c == '\\':
			escaped = true
		case c == '"':
			in = !in
		case in:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// tags parses a DKIM or DMARC record's "k=v; k=v" text. Tag names are
// compared without case; values are trimmed.
func tags(text string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(text, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return out
}

// purpose is what a mail record is for: spf, dmarc, dkim or mx. A want's
// purpose follows from its content; a record's, from its content and the
// want it is compared with.
type purpose string

const (
	purposeSPF   purpose = "spf"
	purposeDMARC purpose = "dmarc"
	purposeDKIM  purpose = "dkim"
	purposeMX    purpose = "mx"
)

func wantPurpose(w Want) purpose {
	if w.Type == "MX" {
		return purposeMX
	}
	switch {
	case strings.HasPrefix(w.Content, "v=spf1"):
		return purposeSPF
	case strings.HasPrefix(w.Content, "v=DMARC1"):
		return purposeDMARC
	default:
		return purposeDKIM
	}
}

// competes reports whether r says the same kind of thing as a want of
// purpose p, so that the two cannot both stand. Any other record at the name
// is somebody's verification code or note, and does not compete.
func competes(p purpose, r Record) bool {
	typ := strings.ToUpper(r.Type)
	if p == purposeMX {
		return typ == "MX"
	}
	if typ != "TXT" {
		return false
	}
	text := strings.ToLower(txtText(r.Content))
	switch p {
	case purposeSPF:
		return text == "v=spf1" || strings.HasPrefix(text, "v=spf1 ")
	case purposeDMARC:
		return strings.HasPrefix(strings.ReplaceAll(text, " ", ""), "v=dmarc1")
	default:
		// Every TXT record at a DKIM selector's name is read as a key.
		return true
	}
}

// satisfies reports whether r already says what a want of purpose p says.
//
// DMARC is compared by what it makes a receiver do, not by its text: an
// operator who adds a reporting address must not turn the record into a
// conflict on every later run.
func satisfies(p purpose, r Record) bool {
	text := txtText(r.Content)
	switch p {
	case purposeSPF:
		return strings.EqualFold(strings.Join(strings.Fields(text), " "), spfLockdown)
	case purposeDKIM:
		key, ok := tags(text)["p"]
		return ok && key == ""
	case purposeDMARC:
		t := tags(text)
		sp, ok := t["sp"]
		if !ok {
			sp = t["p"]
		}
		pct, ok := t["pct"]
		return strings.EqualFold(t["p"], "reject") && strings.EqualFold(sp, "reject") && (!ok || pct == "100")
	default:
		return r.Priority == 0 && isRoot(r.Content)
	}
}

func isRoot(target string) bool {
	return strings.TrimSpace(target) == "."
}

// sendOnly reports whether a missing advisory DMARC policy is dns init's to
// write: nothing at its name says DMARC, and the domain it covers, whose
// records are at, holds no MX record.
func sendOnly(w Want, records, at []Record) bool {
	if !w.Advisory || w.Parent || wantPurpose(w) != purposeDMARC {
		return false
	}
	for _, r := range records {
		if competes(purposeDMARC, r) {
			return false
		}
	}
	for _, r := range at {
		if strings.EqualFold(r.Type, "MX") {
			return false
		}
	}
	return true
}

// classifyMail is classify for a lockdown record.
func classifyMail(w Want, records []Record) (Action, string) {
	p := wantPurpose(w)
	var same []Record
	for _, r := range records {
		if strings.EqualFold(r.Type, "CNAME") {
			return Conflict, fmt.Sprintf("a CNAME to %s already exists at this name. A name with a CNAME can hold no other record, and this never removes one it did not create.", r.Content)
		}
		if competes(p, r) {
			same = append(same, r)
		}
	}
	missing := len(same) == 0
	for _, r := range same {
		if !satisfies(p, r) {
			if w.Parent {
				return Warn, fmt.Sprintf("%s is the registrable domain above community.domain, and its DMARC policy is %q. A receiver applies it to every name under it that has no policy of its own; every name this deployment publishes has one, but the domain holds names it does not run, so its policy is whoever runs the domain's to set: p=reject, sp=reject and no pct below 100 protects them all.", strings.TrimPrefix(w.Name, "_dmarc."), txtText(r.Content))
			}
			if w.Advisory {
				return Warn, fmt.Sprintf("%s has mail of its own (%s), so its DMARC policy is whoever runs that mail's to set, and it is %q. Every hostname under it relies on that policy, and it is weaker than rejecting what fails on the domain and on every name under it: p=reject, sp=reject and no pct below 100.", strings.TrimPrefix(w.Name, "_dmarc."), strings.Join(w.Sources, ", "), txtText(r.Content))
			}
			return Conflict, mailConflict(p, w, r)
		}
	}
	if missing {
		if w.Parent {
			return Warn, fmt.Sprintf("%s is the registrable domain above community.domain, and has no DMARC policy. A receiver applies its policy to every name under it that has none of its own; every name this deployment publishes has one, but the domain holds names it does not run, so its policy is whoever runs the domain's to set. Without p=reject and sp=reject there, nothing tells a receiver to refuse mail forged from those names.", strings.TrimPrefix(w.Name, "_dmarc."))
		}
		if w.Advisory {
			return Warn, fmt.Sprintf("%s has mail of its own (%s), so its DMARC policy is whoever runs that mail's to set, and there is none. Every hostname under it relies on that policy: without p=reject and sp=reject there, nothing tells a receiver to refuse mail forged from them.", strings.TrimPrefix(w.Name, "_dmarc."), strings.Join(w.Sources, ", "))
		}
		return Create, ""
	}
	if p == purposeSPF && len(same) > 1 {
		return Conflict, fmt.Sprintf("%d SPF records exist at this name. A receiver treats more than one as an error and ignores them all; remove all but one by hand.", len(same))
	}
	return Present, ""
}

func mailConflict(p purpose, w Want, r Record) string {
	text := txtText(r.Content)
	name := w.Name
	switch p {
	case purposeSPF:
		return fmt.Sprintf("an SPF record already exists at this name: %q. A name can hold only one, and this never replaces a record it did not create. If %s sends mail, add it to community.mail_domains; otherwise remove that record.", text, name)
	case purposeMX:
		return fmt.Sprintf("an MX record pointing at %s with preference %d already exists at this name, so it receives mail. If it should, add %s to community.mail_domains; otherwise remove that record.", r.Content, r.Priority, name)
	case purposeDKIM:
		return fmt.Sprintf("a TXT record that is not an empty DKIM key already exists at this name: %q. If %s signs mail, add it to community.mail_domains; otherwise remove that record.", text, strings.TrimPrefix(name, "*._domainkey."))
	default:
		domain := strings.TrimPrefix(name, "_dmarc.")
		return fmt.Sprintf("a DMARC policy already exists at this name: %q. It is weaker than rejecting what fails on the domain and on every name under it (p=reject, sp=reject, no pct below 100). If %s has mail, add it to community.mail_domains; otherwise tighten that record by hand.", text, domain)
	}
}

// ownNoMail finds, among records at a want's name, a no-mail record of the
// want's kind that this deployment's dns init made: one carrying comment and
// holding exactly what dns init writes there. At a name with mail of its own
// such a record is a leftover, and prune's to remove.
func ownNoMail(w Want, records []Record, comment string) (Record, bool) {
	p := wantPurpose(w)
	for _, r := range records {
		if r.Comment == comment && competes(p, r) && isLockdown(r) {
			return r, true
		}
	}
	return Record{}, false
}

// leftover is the conflict for a no-mail record dns init made at a name that
// now has mail of its own.
func leftover(w Want, r Record) string {
	name := strings.TrimPrefix(strings.TrimPrefix(w.Name, "_dmarc."), "*._domainkey.")
	return fmt.Sprintf("%s has mail of its own (%s), but the no-mail %s %s that this deployment's dns init made is still here. That name's mail records are whoever runs its mail's to set, and this one says it has none. Run paisans dns prune --execute to delete it, then run again.",
		name, strings.Join(w.Sources, ", "), r.Type, display(strings.ToUpper(r.Type), r.Content, r.Priority))
}

// isLockdown reports whether r is exactly one of the records mailWants
// produces, at the kind of name it produces it at. dns prune deletes no other
// TXT or MX record, so a record that says anything but "no mail here" is
// never the toolkit's to remove, whatever its comment says.
func isLockdown(r Record) bool {
	name := normalise(r.Name)
	switch strings.ToUpper(r.Type) {
	case "MX":
		return r.Priority == 0 && isRoot(r.Content)
	case "TXT":
		text := strings.Join(strings.Fields(txtText(r.Content)), " ")
		switch {
		case strings.HasPrefix(name, "_dmarc."):
			return text == dmarcLockdown
		case strings.HasPrefix(name, "*._domainkey."):
			return text == dkimLockdown
		default:
			return text == spfLockdown
		}
	}
	return false
}

// display is a record's content as a plan line shows it.
func display(typ, content string, priority int) string {
	switch typ {
	case "MX":
		return strconv.Itoa(priority) + " " + content
	case "TXT":
		return strconv.Quote(txtText(content))
	}
	return content
}
