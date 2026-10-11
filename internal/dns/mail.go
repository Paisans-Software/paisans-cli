package dns

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// A name that publishes an address can be the sender of a forged message,
// whether or not anybody ever sends mail from it, and a receiver has nothing
// to refuse that forgery by unless the name says it sends none. So every name
// dns init publishes says so, and the domain says it on behalf of every name
// under it, unless the name has mail of its own.
//
// The records are the ones a DNS provider publishes for a domain that sends no
// mail:
//
//   - SPF "v=spf1 -all" on the domain and on every hostname. SPF applies only
//     to the exact name it is published at, and a wildcard TXT does not reach
//     a name that holds any other record, so each hostname needs its own.
//   - A null MX (RFC 7505) on the domain: it accepts no mail, so nothing is
//     delivered to it and no bounce is sent on its behalf.
//   - An empty key at "*._domainkey" under the domain: no selector there
//     verifies a signature.
//   - DMARC at "_dmarc" under the domain: reject what fails, on the domain
//     (p) and on every name under it (sp), with strict alignment. A receiver
//     with no policy at a subdomain's own _dmarc applies the domain's sp, so
//     this one record covers every hostname for receivers that enforce DMARC.
//
// A name has mail of its own when community.mail_domains lists it, or when an
// smtp from_address an app sends with is at it. The second is derived, so a
// sender the toolkit configures can never be locked out by the toolkit's own
// records. A mail name gets none of these records: whoever runs its mail owns
// its SPF, its MX, its DKIM keys and, at the domain, its DMARC policy. The
// hostnames under it still get their own SPF, and dns init warns when the
// domain's DMARC policy, which they then rely on, is missing or weaker than
// reject.

// The lockdown contents. These are what dns init writes and the only TXT and
// MX contents dns prune will delete.
const (
	spfLockdown   = "v=spf1 -all"
	dkimLockdown  = "v=DKIM1; p="
	dmarcLockdown = "v=DMARC1; p=reject; sp=reject; adkim=s; aspf=s;"
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
		if !kinds.SendsMail(app.Kind) || app.SMTP == nil {
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

	spf := map[string][]string{} // name -> sources
	var names []string
	addSPF := func(name string, sources ...string) {
		if _, ok := mail[name]; ok {
			return
		}
		if _, ok := spf[name]; !ok {
			names = append(names, name)
		}
		for _, s := range sources {
			if !contains(spf[name], s) {
				spf[name] = append(spf[name], s)
			}
		}
	}
	if domain != "" {
		addSPF(domain, "community.domain")
	}
	for _, w := range addresses {
		addSPF(w.Name, w.Sources...)
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, Want{Type: "TXT", Name: name, Content: spfLockdown, Sources: spf[name]})
	}

	if domain == "" {
		return out
	}
	dmarc := "_dmarc." + domain
	if sources, ok := mail[domain]; ok {
		out = append(out, Want{Type: "TXT", Name: dmarc, Content: dmarcLockdown, Sources: sources, Advisory: true})
		return out
	}
	source := []string{"community.domain"}
	out = append(out,
		Want{Type: "MX", Name: domain, Content: nullMX, Priority: 0, Sources: source},
		Want{Type: "TXT", Name: "*._domainkey." + domain, Content: dkimLockdown, Sources: source},
		Want{Type: "TXT", Name: dmarc, Content: dmarcLockdown, Sources: source},
	)
	return out
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
			if w.Advisory {
				return Warn, fmt.Sprintf("%s has mail of its own (%s), so its DMARC policy is whoever runs that mail's to set, and it is %q. Every hostname under it relies on that policy, and it is weaker than rejecting what fails on the domain and on every name under it: p=reject, sp=reject and no pct below 100.", strings.TrimPrefix(w.Name, "_dmarc."), strings.Join(w.Sources, ", "), txtText(r.Content))
			}
			return Conflict, mailConflict(p, w, r)
		}
	}
	if missing {
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
