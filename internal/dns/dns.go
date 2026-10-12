// Package dns works out which public DNS records a deployment needs, compares
// them with what its DNS provider holds, and creates the ones that are
// missing.
//
// The records are derived, never declared. Every app hostname, the media
// hostname and every site endpoint given as a name already appear in
// paisans-stack.yaml, and the address each one points at is a site's
// public_address. A second list of records in the configuration would be a
// second source of truth for the same names, and the one that drifts.
//
// Init only ever creates. It never updates and never deletes, because a
// record it did not create is somebody else's, exactly as a file on a host
// that no apply wrote is somebody else's. A record that disagrees with what
// the deployment needs is a conflict, and any conflict stops the whole run
// before a single record is written.
//
// Prune is the one path that deletes, and only a record that passes every
// rule in prune.go: the toolkit's comment, a name that is this deployment's,
// and no longer wanted; and either an A or AAAA record pointing at a site's
// public address, or, for dns prune, a TXT or MX record holding exactly a
// no-mail content init writes (mail.go). dns prune and site remove's DNS
// stage both plan through it. A gateway move (move.go) is the one other
// path, and deletes only through ExecutePrune too: the old gateway's address
// records at the gateway's hostnames, once every record of the new one's is
// present. Nothing in this package updates a record.
//
// It reaches no host. The only thing it talks to is the DNS provider's API,
// from the workstation, with the token from the decrypted secrets.
package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/kinds"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// Record is one DNS record as a provider reports it.
type Record struct {
	Type    string
	Name    string
	Content string
	// Proxied is a CDN in front of the address rather than the address itself.
	// Cloudflare calls this proxying. A proxied record that otherwise matches is
	// a conflict, not a match: see the README section on `dns init`.
	Proxied bool
	// ID is the provider's identifier for the record, which is what a delete
	// names. It is empty on a record about to be created.
	ID string
	// Comment is the provider's free text note on the record. Prune reads it
	// as necessary and never as sufficient: anyone with the zone can edit it.
	Comment string
	// Priority is an MX record's preference, and zero for every other type.
	Priority int
	// TTL is the record's time to live in seconds as the provider reports
	// it, with a provider's automatic setting given as the seconds it
	// stands for. Zero is unknown. It is read, never written: a record is
	// created with the provider's automatic TTL.
	TTL int
}

// Want is a record the deployment needs.
type Want struct {
	Type    string
	Name    string
	Content string
	// Sources are the configuration keys that require this record, so that a
	// conflict can name the line in paisans-stack.yaml it is about.
	Sources []string
	// Priority is an MX record's preference.
	Priority int
	// Advisory is a record the deployment relies on but is somebody else's
	// to write: the DMARC policy of a domain with mail of its own. It is
	// checked and warned about, never created, and prune never counts it as
	// wanted.
	Advisory bool
	// Leftover is a record that must not be there if this deployment's dns
	// init made it: a no-mail record at a name that has since been given mail.
	// One found is a conflict that dns prune resolves; none is Clear. It is
	// never created, and prune never counts it as wanted.
	Leftover bool
	// ViaGateway is an address record for a hostname the gateway serves: an
	// app's hostname or media hostname, not a monitor's or a site endpoint's.
	// These are the records a gateway move adds and deletes.
	ViaGateway bool
	// Former are the public addresses of every declared site other than the
	// gateway, on a ViaGateway want: one of them may be the gateway this one
	// replaces, whose record at the name waits for `gateway move` rather than
	// being a conflict.
	Former []string
}

// Provider is a DNS provider's API, reduced to the things this package does
// with it. There is no update here on purpose: a method that does not exist
// cannot be called by mistake. Delete's only caller is ExecutePrune, which
// deletes nothing its plan did not pass through every rule of the builder
// that made it: BuildPrune or BuildSiteRemoval under prune.go's rules, or
// BuildMove under move.go's.
type Provider interface {
	// Name is the provider's name as acme.provider spells it, for messages.
	Name() string
	// LookupZone reports the identifier of the zone whose apex is exactly
	// name, if the credential can see one.
	LookupZone(ctx context.Context, name string) (id string, found bool, err error)
	// Records returns every record of every type whose name is exactly name.
	Records(ctx context.Context, zoneID, name string) ([]Record, error)
	// Create adds one record. It must never replace an existing one.
	Create(ctx context.Context, zoneID string, record Record) error
	// AllRecords returns every record in the zone, every page of them. A
	// partial list must be an error, never a short answer.
	AllRecords(ctx context.Context, zoneID string) ([]Record, error)
	// Delete removes the one record with this identifier.
	Delete(ctx context.Context, zoneID, recordID string) error
}

// Desired derives the records a deployment needs from its configuration.
//
// It returns every problem at once rather than the first, for the same reason
// validate does.
func Desired(cfg *config.Config) ([]Want, error) {
	var problems []string
	byKey := map[string]*Want{} // type + " " + name
	var order []string
	add := func(typ, name, content, source string) {
		key := typ + " " + name
		if existing, ok := byKey[key]; ok {
			if existing.Content != content {
				problems = append(problems, fmt.Sprintf(
					"%s needs %s %s to be %s, but %s needs it to be %s. One name can point at one place; give them different names.",
					source, typ, name, content, strings.Join(existing.Sources, ", "), existing.Content))
				return
			}
			existing.Sources = append(existing.Sources, source)
			return
		}
		byKey[key] = &Want{Type: typ, Name: name, Content: content, Sources: []string{source}}
		order = append(order, key)
	}
	addSite := func(site config.Site, name, source string) {
		add("A", name, site.PublicAddress, source)
		if site.PublicAddress6 != "" {
			add("AAAA", name, site.PublicAddress6, source)
		}
	}

	// Every public hostname is served by the gateway, except a monitor's,
	// which the monitor serves from its own address so that it does not go
	// dark with the gateway it reports on.
	var viaGateway []Hostname
	refusedMonitor := map[string]bool{}
	for _, h := range hostnames(cfg) {
		monitor, ok := render.ServedBy(cfg, h.App)
		if !ok {
			viaGateway = append(viaGateway, h)
			continue
		}
		site := cfg.Sites[monitor]
		if site.PublicAddress == "" {
			if !refusedMonitor[monitor] {
				refusedMonitor[monitor] = true
				problems = append(problems, fmt.Sprintf(
					"sites.%s.public_address is not set. %s holds the monitor role and serves its own hostnames, so they point at it, and the address the internet reaches it on cannot be guessed from here. Declare it, for example public_address: 203.0.113.20.",
					monitor, monitor))
			}
			continue
		}
		addSite(site, normalise(h.Name), h.Key)
	}
	gatewayNames := map[string]bool{}
	var former []string
	if len(viaGateway) > 0 {
		gateways := cfg.GatewaySites()
		switch {
		case len(gateways) == 0:
			problems = append(problems, "no site holds the gateway role, so there is no address for any hostname to point at. Give one site the gateway role and a public_address.")
		case len(gateways) > 1:
			problems = append(problems, fmt.Sprintf(
				"%d sites hold the gateway role (%s). Which of them a hostname should point at is a design that does not exist yet, so this refuses rather than picking one. Publish these records by hand, or give the gateway role to one site.",
				len(gateways), strings.Join(gateways, ", ")))
		default:
			gateway := cfg.Sites[gateways[0]]
			if gateway.PublicAddress == "" {
				problems = append(problems, fmt.Sprintf(
					"sites.%s.public_address is not set. %s holds the gateway role, so every hostname points at it, and the address the internet reaches it on cannot be guessed from here. Declare it, for example public_address: 203.0.113.10.",
					gateways[0], gateways[0]))
			} else {
				for _, h := range viaGateway {
					addSite(gateway, normalise(h.Name), h.Key)
					gatewayNames[normalise(h.Name)] = true
				}
				own := siteAddresses(cfg, gateways[0])
				for _, name := range cfg.SiteNames() {
					if name == gateways[0] {
						continue
					}
					for _, ip := range siteAddresses(cfg, name) {
						if !slices.Contains(former, ip) && !mapHas(own, ip) {
							former = append(former, ip)
						}
					}
				}
				sort.Strings(former)
			}
		}
	}

	// A site endpoint given as a name gets a record too. Beside a
	// public_address it points there, and the mesh dials the address itself
	// (config.Site.DialEndpoint), so the name is the operator's to use; with
	// no public_address the other sites' WireGuard dials the name, and there
	// is no address here to give it. An endpoint given as an address, or as
	// a port alone, names nothing.
	for _, siteName := range cfg.SiteNames() {
		site := cfg.Sites[siteName]
		host := endpointHost(site.Endpoint)
		if host == "" || net.ParseIP(host) != nil {
			continue
		}
		if site.PublicAddress == "" {
			problems = append(problems, fmt.Sprintf(
				"sites.%s.public_address is not set, but sites.%s.endpoint names %s, which the other sites dial. Declare the address that name should resolve to, or give the endpoint as an address.",
				siteName, siteName, host))
			continue
		}
		addSite(site, normalise(host), fmt.Sprintf("sites.%s.endpoint", siteName))
	}

	if len(problems) > 0 {
		return nil, &RefusedError{Problems: problems}
	}
	out := make([]Want, 0, len(order))
	for _, key := range order {
		w := *byKey[key]
		// A name that is a site endpoint too is not the gateway's alone,
		// so a move leaves it to dns init.
		if gatewayNames[w.Name] && allHostnames(w.Sources) {
			w.ViaGateway = true
			w.Former = former
		}
		out = append(out, w)
	}
	out = append(out, mailWants(cfg, out)...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Type < out[j].Type
	})
	return out, nil
}

// Hostname is one public hostname the configuration names for an app.
type Hostname struct {
	// Key is the configuration key that names it.
	Key string
	// Name is the hostname as written.
	Name string
	// App is the app it belongs to, and Main whether it is the app's own
	// hostname rather than one under hostnames.
	App  string
	Main bool
}

// GatewayHostnames is every hostname the gateway serves: each app's
// hostnames, declared and derived, except a monitor's, which the monitor
// serves itself. It is what a gateway move checks the new gateway answers
// for, and the names its DNS stages touch.
func GatewayHostnames(cfg *config.Config) []Hostname {
	var out []Hostname
	for _, h := range hostnames(cfg) {
		if _, ok := render.ServedBy(cfg, h.App); !ok {
			out = append(out, h)
		}
	}
	return out
}

// hostnames is every hostname the configuration names for an app.
func hostnames(cfg *config.Config) []Hostname {
	var out []Hostname
	for _, appName := range cfg.AppNames() {
		app := cfg.Apps[appName]
		if app.Hostname != "" {
			out = append(out, Hostname{Key: fmt.Sprintf("apps.%s.hostname", appName), Name: app.Hostname, App: appName, Main: true})
		}
		roles := make([]string, 0, len(app.Hostnames))
		for role := range app.Hostnames {
			roles = append(roles, role)
		}
		sort.Strings(roles)
		for _, role := range roles {
			if role == kinds.MediaRole && kinds.MediaHostname(cfg, app) == "" {
				// An app keeping its files on its own disk serves them on its
				// own hostname, so a media hostname would point at no route.
				// validate refuses a declared one; this keeps dns from
				// publishing it if that is ever bypassed.
				continue
			}
			out = append(out, Hostname{Key: fmt.Sprintf("apps.%s.hostnames.%s", appName, role), Name: app.Hostnames[role], App: appName})
		}
	}
	// Each app keeping objects in Garage serves them on a media hostname of
	// its own, derived when the app does not declare one under hostnames.media
	// (a declared one is already in the loop above). An app keeping its files
	// on its own disk has none, and gets no record for one.
	for _, appName := range cfg.AppNames() {
		app := cfg.Apps[appName]
		if !kinds.MediaHostnameIsDerived(cfg, app) {
			continue
		}
		if media := kinds.MediaHostname(cfg, app); media != "" {
			out = append(out, Hostname{Key: fmt.Sprintf("apps.%s.hostnames.media (derived)", appName), Name: media, App: appName})
		}
	}
	return out
}

// allHostnames reports whether every source of a want is an app's hostname.
func allHostnames(sources []string) bool {
	for _, s := range sources {
		if !strings.HasPrefix(s, "apps.") {
			return false
		}
	}
	return true
}

func mapHas(m map[string]string, v string) bool {
	for _, x := range m {
		if x == v {
			return true
		}
	}
	return false
}

// RefusedError is every reason the configuration cannot be turned into
// records.
type RefusedError struct {
	Problems []string
}

func (e *RefusedError) Error() string {
	out := "dns: the records this deployment needs cannot be worked out:"
	for _, p := range e.Problems {
		out += "\n  " + p
	}
	return out
}

func endpointHost(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(endpoint); err == nil {
		return host
	}
	return strings.Trim(endpoint, "[]")
}

func normalise(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

// Action is what happens to one wanted record.
type Action string

const (
	// Present is a record that already exists exactly as wanted.
	Present Action = "present"
	// Create is a record that does not exist and will be created.
	Create Action = "create"
	// Conflict is a record whose name already holds something else.
	Conflict Action = "conflict"
	// Warn is an advisory record that is missing or too weak. Nothing is
	// written for it and it stops nothing.
	Warn Action = "warn"
	// Clear is a leftover record that is not there. It is not shown.
	Clear Action = "clear"
	// Moving is an address record for a hostname the gateway serves, at a
	// name that still holds the record this deployment's dns init made for
	// the gateway before this one. Set that record aside and the name is
	// fine. Nothing is written for it here: `gateway move` adds the new
	// gateway's record once that gateway answers, and deletes the old one
	// once it is verified, so a move is never a conflict and never a
	// cutover.
	Moving Action = "moving"
)

// Entry is one wanted record and what will be done about it.
type Entry struct {
	Want
	Zone   string
	zoneID string
	Action Action
	// Detail says what is in the way, for a conflict.
	Detail string
}

// Plan is every wanted record, compared with the provider.
type Plan struct {
	Provider string
	Entries  []Entry
	// comment is what every record this plan creates carries.
	comment string
}

// RecordComment is written on every record dns init creates for deployment
// d, paisans-<token>: created by paisans dns init, so that a person looking
// at the zone can tell which records the toolkit made and for which
// deployment. Prune reads it back, but as a necessary condition and never a
// sufficient one: a comment anyone can edit is not a claim anyone can trust,
// so a record also has to sit at one of this deployment's names and point at
// one of its sites' addresses before prune will remove it. A record carrying
// another deployment's token is that deployment's, and is never this one's
// to consider.
func RecordComment(d deployment.Deployment) string {
	return d.Prefix() + ": created by paisans dns init"
}

// Conflicts returns the entries that stop an execute.
func (p *Plan) Conflicts() []Entry { return p.only(Conflict) }

// Moving returns the entries that wait for `gateway move`.
func (p *Plan) Moving() []Entry { return p.only(Moving) }

// Creates returns the entries an execute will create.
func (p *Plan) Creates() []Entry { return p.only(Create) }

func (p *Plan) only(a Action) []Entry {
	var out []Entry
	for _, e := range p.Entries {
		if e.Action == a {
			out = append(out, e)
		}
	}
	return out
}

// Build compares the wanted records with what the provider holds, for
// deployment d. It reads and never writes.
func Build(ctx context.Context, provider Provider, d deployment.Deployment, wants []Want) (*Plan, error) {
	plan := &Plan{Provider: provider.Name(), comment: RecordComment(d)}
	zones := map[string]zone{}
	wantedTypes := map[string]map[string]bool{}
	for _, w := range wants {
		if wantedTypes[w.Name] == nil {
			wantedTypes[w.Name] = map[string]bool{}
		}
		wantedTypes[w.Name][w.Type] = true
	}
	existing := map[string][]Record{}
	for _, w := range wants {
		z, err := FindZone(ctx, provider, w.Name, zones)
		if err != nil {
			return nil, err
		}
		records, ok := existing[w.Name]
		if !ok {
			records, err = provider.Records(ctx, z.id, w.Name)
			if err != nil {
				return nil, err
			}
			existing[w.Name] = records
		}
		var action Action
		var detail string
		if w.Leftover {
			action = Clear
		} else {
			action, detail = classify(w, records, wantedTypes[w.Name])
		}
		if w.Leftover || w.Advisory {
			if r, ok := ownNoMail(w, records, plan.comment); ok {
				action, detail = Conflict, leftover(w, r)
			}
		}
		if action == Conflict && w.ViaGateway {
			if rest, old := setAsideFormer(w, records, plan.comment); len(old) > 0 {
				if a, _ := classify(w, rest, wantedTypes[w.Name]); a != Conflict {
					action, detail = Moving, fmt.Sprintf("%s still points at %s, the gateway before this one; paisans gateway move adds %s beside it once the new gateway answers, and deletes it once every hostname is verified", w.Name, strings.Join(old, ", "), w.Content)
				}
			}
		}
		plan.Entries = append(plan.Entries, Entry{Want: w, Zone: z.name, zoneID: z.id, Action: action, Detail: detail})
	}
	return plan, nil
}

// setAsideFormer splits the records at a want's name into the address
// records this deployment's dns init made for a former gateway (carrying
// comment, unproxied, and pointing at one of w.Former) and the rest. It
// returns the rest, and the former addresses found.
func setAsideFormer(w Want, records []Record, comment string) ([]Record, []string) {
	var rest []Record
	var old []string
	for _, r := range records {
		typ := strings.ToUpper(r.Type)
		if (typ == "A" || typ == "AAAA") && r.Comment == comment && !r.Proxied {
			if ip := canonical(r.Content); ip != "" && slices.Contains(w.Former, ip) {
				if !slices.Contains(old, r.Content) {
					old = append(old, r.Content)
				}
				continue
			}
		}
		rest = append(rest, r)
	}
	return rest, old
}

// canonical is an address in net.IP's own spelling, or empty for one that is
// not an address.
func canonical(s string) string {
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil {
		return ""
	}
	return ip.String()
}

// classify decides what one wanted record needs, given every record already
// at its name.
func classify(w Want, records []Record, wanted map[string]bool) (Action, string) {
	if w.Type == "TXT" || w.Type == "MX" {
		return classifyMail(w, records)
	}
	var same []Record
	for _, r := range records {
		switch {
		case strings.EqualFold(r.Type, "CNAME"):
			return Conflict, fmt.Sprintf("a CNAME to %s already exists at this name. A name with a CNAME can hold no other record, and this never removes one it did not create.", r.Content)
		case strings.EqualFold(r.Type, w.Type):
			same = append(same, r)
		}
	}
	// An address record of the other family that nobody asked for splits
	// traffic: clients on that family go somewhere else, silently.
	for _, r := range records {
		other := ""
		switch {
		case w.Type == "A" && strings.EqualFold(r.Type, "AAAA") && !wanted["AAAA"]:
			other = "public_address6"
		case w.Type == "AAAA" && strings.EqualFold(r.Type, "A") && !wanted["A"]:
			other = "public_address"
		}
		if other != "" {
			return Conflict, fmt.Sprintf("a %s record pointing at %s already exists at this name, and this deployment declares no %s. Clients on that address family would reach it instead. Remove it, or declare %s on the site.", r.Type, r.Content, other, other)
		}
	}
	if len(same) == 0 {
		return Create, ""
	}
	for _, r := range same {
		if r.Content != w.Content {
			return Conflict, fmt.Sprintf("a %s record pointing at %s already exists at this name. This never replaces a record it did not create; change or remove it by hand if %s is right.", r.Type, r.Content, w.Content)
		}
	}
	for _, r := range same {
		if r.Proxied {
			return Conflict, "the record exists with this address but is proxied through the provider's CDN. A proxied name does not resolve to the gateway, so WireGuard cannot dial it and the gateway does not see client addresses. Turn proxying off for it."
		}
	}
	return Present, ""
}

// ErrConflict is what Execute returns, wrapped, when the plan has a conflict:
// a record that points somewhere else, which only the operator can change at
// the provider. Running again finds it again.
var ErrConflict = errors.New("dns: conflicting records")

// conflictError is Execute's refusal of a plan with n conflicts.
type conflictError struct{ n int }

func (e conflictError) Error() string {
	return fmt.Sprintf("dns: %d conflicting record(s), listed above. Nothing was created: a partial set of records is a deployment some names reach and others do not", e.n)
}

// Unwrap is the refusal as an operator reads it, which in turn is
// ErrConflict.
func (e conflictError) Unwrap() error {
	hint := fmt.Sprintf("%d DNS records conflict with the configuration", e.n)
	if e.n == 1 {
		hint = "1 DNS record conflicts with the configuration"
	}
	return &ui.Problem{
		Hint:    hint,
		Explain: "Each is listed above. Nothing was created: a partial set of records is a deployment some names reach and others do not. Change each at your DNS provider, or run paisans dns prune --execute for one that says so, then run again.",
		Cause:   ErrConflict,
	}
}

// Execute creates every missing record, then reads each back. It refuses to
// write anything if the plan has a conflict.
//
// It stops at the first failure. Records created before it stay, and a re-run
// finds them present, so a run that failed partway resumes rather than
// starting over.
func Execute(ctx context.Context, provider Provider, plan *Plan, r ui.Reporter) error {
	if conflicts := plan.Conflicts(); len(conflicts) > 0 {
		return conflictError{len(conflicts)}
	}
	for _, e := range plan.Creates() {
		step := r.Step(fmt.Sprintf("create %s %s", e.Type, e.Name))
		step.Detail("%s, zone %s", display(e.Type, e.Content, e.Priority), e.Zone)
		record := Record{Type: e.Type, Name: e.Name, Content: e.Content, Priority: e.Priority, Comment: plan.comment}
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
		if action, _ := classify(e.Want, back, map[string]bool{e.Type: true, "A": true, "AAAA": true}); action != Present {
			err := fmt.Errorf("dns: %s %s was created but does not read back as %s unproxied. Check it in the provider's console before re-running", e.Type, e.Name, display(e.Type, e.Content, e.Priority))
			step.Fail(err)
			return err
		}
		step.Detail("read back as %s, unproxied", display(e.Type, e.Content, e.Priority))
		step.Done("")
	}
	return nil
}

// Show reports a plan marked the way host prepare marks its plan: each
// record to create as pending, under --verbose each one already present as
// done, and a conflict as a refusal, since Execute will not create anything
// while one stands. Each line names the record's type, name, address and
// zone.
func (p *Plan) Show(r ui.Reporter) {
	r.Section("dns " + p.Provider)
	for _, e := range p.Entries {
		content := display(e.Type, e.Content, e.Priority)
		record := fmt.Sprintf("%s %s -> %s (zone %s)", e.Type, e.Name, content, e.Zone)
		switch e.Action {
		case Create:
			r.Step("create "+record).End(ui.Pending, "")
		case Conflict:
			wanted := "wanted"
			if e.Leftover || e.Advisory {
				wanted = "remove with dns prune"
			}
			r.Refuse(fmt.Sprintf("%s %s conflicts with a record at the provider", e.Type, e.Name),
				fmt.Sprintf("%s\n%s: %s -> %s (zone %s)", e.Detail, wanted, e.Name, content, e.Zone))
		case Clear:
		case Moving:
			r.Note(fmt.Sprintf("%s %s waits for paisans gateway move", e.Type, e.Name), e.Detail)
		case Warn:
			r.Note(fmt.Sprintf("%s %s is not this toolkit's to write, and is missing or weak", e.Type, e.Name),
				fmt.Sprintf("%s\nexpected at least: %s -> %s (zone %s)", e.Detail, e.Name, content, e.Zone))
		default:
			if r.Verbose() {
				r.Step(record).End(ui.OK, "")
			}
		}
	}
	if n := len(p.Conflicts()); n > 0 {
		r.Detail("%d record(s) conflict with what the provider already holds. Nothing will be created until that is resolved.", n)
	}
}
