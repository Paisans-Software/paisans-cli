package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/progname"
)

// The DNS checks ask whether Caddy can get its certificates, and whether
// the DNS provider accepts the token from every place that uses it: each
// Caddy site, where Caddy answers DNS-01 with the token in its caddy.env,
// and the workstation, where dns init calls the provider with the token in
// the secrets. The provider judges the address a call comes from, so a
// check from one place says nothing about another.
//
// A refused call is what feeds Cloudflare's lockout, so the graph makes at
// most one refused call per place: the zone lookup comes first in each
// token check, the records listing needs the workstation's to have passed,
// and nothing at the provider is asked while any Caddy log shows the
// lockout. See docs/specs/2026-10-15-doctor-check-graph.md, The DNS checks.

// DNSReads is how the DNS checks read: Caddy's log on a site, and the
// token from each place. A Reads that is a DNSReads too gets the DNS
// checks in Checks' graph.
type DNSReads interface {
	// CaddyLog runs CaddyLogCommand on a Caddy site.
	CaddyLog(site string) CaddyLogProbe
	// Secrets opens the secrets file, which holds the workstation's token.
	// The token stays with the DNSReads and never enters the Input.
	Secrets() SecretsProbe
	// SiteToken checks the token in the site's caddy.env, from the site.
	SiteToken(site string) dns.TokenResult
	// WorkstationToken checks the token in the secrets, from here. It is
	// asked only once Secrets opened them.
	WorkstationToken() dns.TokenResult
	// DNSProvider is the provider's API holding the secrets' token, for the
	// records check, which only reads.
	DNSProvider() (dns.Provider, error)
}

// Sections of the DNS checks, after the others: they explain why Caddy
// cannot serve, which is above everything a site runs.
const (
	SectionCaddy   = "caddy certificates"
	SectionSecrets = "secrets"
	SectionToken   = "dns token"
	SectionRecords = "dns records"
)

// CaddyLogProbe is what CaddyLogCommand printed on one Caddy site.
type CaddyLogProbe struct {
	Site string
	Out  string
	Err  string
}

func (p CaddyLogProbe) site() string { return p.Site }

// SecretsProbe is whether the secrets file opened, and why not.
type SecretsProbe struct {
	// Asked is set once the file was tried.
	Asked bool
	Path  string
	// Err is why it did not open, one line, and More the rest of it.
	Err  string
	More []string
}

// RecordsProbe is the plan dns init would make, read and never executed.
type RecordsProbe struct {
	Asked   bool
	Entries []dns.Entry
	Err     string
}

// lockoutWindow is how far back a 10502 in Caddy's log keeps doctor from
// calling the provider. How long Cloudflare's lockout outlasts the last
// refused call is not documented; five minutes is the conservative reading
// of the staging log, in which every 10502 followed a refused call within
// seconds.
const lockoutWindow = 5 * 60

// CaddyLogCommand reads the caddy container's certificate log lines on a
// Caddy site: the host's clock, when the container started, then the last
// hour of its log, kept to the lines of Caddy's tls.obtain logger. The log
// is read from the container's start or the last hour, whichever is later;
// the start is applied here, on the workstation, since comparing two times
// in sh needs a date that parses them.
func CaddyLogCommand(d deployment.Deployment) string {
	compose := d.ComposeCmd("infra")
	return `c=$(` + compose + ` ps -q caddy) || exit $?; ` +
		`echo "now $(date +%s)"; ` +
		`if [ -z "$c" ]; then echo "no container"; exit 0; fi; ` +
		`echo "started $(docker inspect -f '{{.State.StartedAt}}' $c)"; ` +
		`o=$(` + compose + ` logs --no-color --no-log-prefix --since 1h caddy 2>&1) || { printf '%s\n' "$o" | tail -n 3; exit 1; }; ` +
		`printf '%s\n' "$o" | grep -F '"logger":"tls.obtain"'; exit 0`
}

// caddyEntry is one line of Caddy's JSON log, the fields the summary reads
// (observed on staging: logger tls.obtain, msg, identifier, issuer, error).
type caddyEntry struct {
	TS         float64 `json:"ts"`
	Logger     string  `json:"logger"`
	Msg        string  `json:"msg"`
	Identifier string  `json:"identifier"`
	Issuer     string  `json:"issuer"`
	Error      string  `json:"error"`
}

const (
	caddyFailed   = "could not get certificate from issuer"
	caddyObtained = "certificate obtained successfully"
)

// caddyLog is CaddyLogCommand's output, read.
type caddyLog struct {
	// now is the host's clock when it was read, in seconds; zero when it
	// did not say.
	now       float64
	container bool
	entries   []caddyEntry
}

func parseCaddyLog(out string) caddyLog {
	var l caddyLog
	var started time.Time
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "now "):
			l.now, _ = strconv.ParseFloat(strings.TrimPrefix(line, "now "), 64)
		case strings.HasPrefix(line, "started "):
			l.container = true
			started, _ = time.Parse(time.RFC3339Nano, strings.TrimPrefix(line, "started "))
		case strings.HasPrefix(line, "{"):
			var e caddyEntry
			if json.Unmarshal([]byte(line), &e) != nil || e.Logger != "tls.obtain" {
				continue
			}
			if e.Msg != caddyFailed && e.Msg != caddyObtained {
				continue
			}
			// A line from before the container started is a container
			// that is gone, whose failures this one may not repeat.
			if !started.IsZero() && e.TS > 0 && e.TS < float64(started.Unix()) {
				continue
			}
			l.entries = append(l.entries, e)
		}
	}
	return l
}

var (
	// providerCode is the provider's code inside Caddy's error, which
	// quotes libdns: {Code:9109 Message:...}.
	providerCode = regexp.MustCompile(`Code:(\d+)\b`)
	// refusedFrom is the address in a 9109 message. Only an address is
	// read from the message, so nothing else of it reaches a finding.
	refusedFrom = regexp.MustCompile(`from location: ?([0-9A-Fa-f.:]*[0-9A-Fa-f])`)
	// retryAfter is when an ACME rateLimited problem says to try again,
	// as Let's Encrypt words it.
	retryAfter = regexp.MustCompile(`retry after (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} UTC)`)
	// caddyContext is the order and CA Caddy appends to an error.
	caddyContext = regexp.MustCompile(`(\s*\((order|ca)=[^)]*\))+\s*$`)
)

// Lockout reports whether a Caddy site's log shows Cloudflare's lockout,
// 10502, in the last five minutes of the host's clock, or at all when the
// host gave no clock to judge by.
func Lockout(p CaddyLogProbe) bool {
	l := parseCaddyLog(p.Out)
	for _, e := range l.entries {
		if e.Msg == caddyFailed && caddyCode(e.Error) == 10502 && (l.now == 0 || e.TS >= l.now-lockoutWindow) {
			return true
		}
	}
	return false
}

func caddyCode(text string) int {
	if m := providerCode.FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// certificateCause is why Caddy could not get a certificate, as one
// finding: the hostnames failing for one cause are one finding, since six
// lines saying 9109 is a cascade that buries the line that matters.
type certificateCause struct {
	key, line, advice string
	hostnames         []string
}

// classifyCertificate is the cause of one failure, from its error.
func classifyCertificate(e caddyEntry) (key, line, advice string) {
	switch code := caddyCode(e.Error); {
	case code == 9109:
		address := ""
		if m := refusedFrom.FindStringSubmatch(e.Error); m != nil {
			address = " (" + m[1] + ")"
		}
		return "9109" + address, "cloudflare refuses this server's DNS token" + address, dns.FilterAdvice
	case code == 10502:
		return "10502", "cloudflare is locking the DNS token out", dns.LockoutAdvice
	case strings.Contains(e.Error, "rateLimited"):
		// RFC 8555, section 6.7.
		when := ""
		if m := retryAfter.FindStringSubmatch(e.Error); m != nil {
			when = m[1]
		}
		advice := "Caddy tries again on its own; nothing to restart. See https://letsencrypt.org/docs/rate-limits/."
		if when != "" {
			advice = "It says to retry after " + when + ". " + advice
		}
		return "rateLimited " + when, "the certificate authority is rate limiting", advice
	}
	cause := lastClause(e.Error)
	return "other " + cause, cause, "This is Caddy's own error, from its log."
}

// lastClause is the last clause of Caddy's error, without the order and CA
// it appends, cut as apply's health gate cuts a log line.
func lastClause(text string) string {
	text = caddyContext.ReplaceAllString(strings.TrimSpace(text), "")
	if i := strings.LastIndex(text, ": "); i >= 0 && i+2 < len(text) {
		text = text[i+2:]
	}
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > 160 {
		text = string(r[:157]) + "..."
	}
	if text == "" {
		return "caddy logged a failure with no error"
	}
	return text
}

// CaddyCertificates judges each Caddy site's log: for each hostname the
// site serves, the newest outcome, and a finding for each cause that is the
// newest for any of them.
func CaddyCertificates(cfg *config.Config, probes []CaddyLogProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		out = append(out, caddyCertificates(cfg, p)...)
	}
	return out
}

func caddyCertificates(cfg *config.Config, p CaddyLogProbe) []Finding {
	if p.Err != "" {
		return []Finding{{Section: SectionCaddy, Level: Fail, Line: p.Site + ": caddy's log could not be read: " + p.Err,
			More: []string{"The containers check says whether caddy is running."}}}
	}
	l := parseCaddyLog(p.Out)
	if !l.container {
		return []Finding{{Section: SectionCaddy, Level: Skip, Line: p.Site + ": there is no caddy container whose log to read",
			More: []string{"The containers check says why."}}}
	}
	served := map[string]bool{}
	var order []string
	for _, h := range dns.ServedHostnames(cfg, p.Site) {
		name := strings.ToLower(strings.TrimSuffix(h.Name, "."))
		if !served[name] {
			served[name] = true
			order = append(order, name)
		}
	}
	newest := map[string]caddyEntry{}
	for _, e := range l.entries {
		name := strings.ToLower(e.Identifier)
		if served[name] {
			newest[name] = e
		}
	}
	var causes []*certificateCause
	var obtained []string
	for _, name := range order {
		e, ok := newest[name]
		if !ok {
			continue
		}
		if e.Msg == caddyObtained {
			obtained = append(obtained, name)
			continue
		}
		key, line, advice := classifyCertificate(e)
		i := slices.IndexFunc(causes, func(c *certificateCause) bool { return c.key == key })
		if i < 0 {
			causes = append(causes, &certificateCause{key: key, line: line, advice: advice})
			i = len(causes) - 1
		}
		causes[i].hostnames = append(causes[i].hostnames, name)
	}
	if len(causes) == 0 {
		f := Finding{Section: SectionCaddy, Level: OK, Line: p.Site + ": no certificate failure in caddy's log"}
		if len(obtained) > 0 {
			f.More = []string{"obtained: " + strings.Join(obtained, ", ")}
		}
		return []Finding{f}
	}
	var out []Finding
	for _, c := range causes {
		line := c.line
		if strings.HasPrefix(c.key, "rateLimited") {
			line += " " + strings.Join(c.hostnames, ", ")
		}
		out = append(out, Finding{Section: SectionCaddy, Level: Fail, Line: p.Site + ": " + line,
			More: []string{fmt.Sprintf("no certificate for %d %s: %s", len(c.hostnames), plural(len(c.hostnames), "hostname"), strings.Join(c.hostnames, ", ")), c.advice}})
	}
	return out
}

// Secrets is whether the secrets file opened: a file that did not is a
// skipped check, since doctor's subject is the deployment and not this
// workstation's keys, and the workstation's token check is skipped with it.
func Secrets(p SecretsProbe) []Finding {
	if !p.Asked {
		return nil
	}
	if p.Err != "" {
		return []Finding{{Section: SectionSecrets, Level: Skip, Line: "cannot open " + p.Path + ": " + p.Err, More: p.More}}
	}
	return []Finding{{Section: SectionSecrets, Level: OK, Line: "opened " + p.Path}}
}

// Tokens is each place's token check, the Caddy sites in order and then
// the workstation.
func Tokens(in Input) []Finding {
	var out []Finding
	for _, place := range append(slices.Clone(in.Sites), dns.WorkstationPlace) {
		out = append(out, tokenFindings(in.Tokens, place)...)
	}
	return out
}

func tokenFindings(results []dns.TokenResult, place string) []Finding {
	var out []Finding
	for _, r := range results {
		if r.Place != place {
			continue
		}
		f := Finding{Section: SectionToken, Line: place + ": " + r.Finding}
		switch r.Status {
		case dns.TokenPassed:
			f.Level = OK
		case dns.TokenFailed:
			f.Level = Fail
		default:
			f.Level = Skip
		}
		if r.Advice != "" {
			f.More = []string{r.Advice}
		}
		out = append(out, f)
	}
	return out
}

func tokenOutcome(results []dns.TokenResult, place string) Outcome {
	for _, r := range results {
		if r.Place == place {
			switch r.Status {
			case dns.TokenPassed:
				return Passed
			case dns.TokenFailed:
				return Failed
			}
			return Skipped
		}
	}
	return Failed
}

// Records is the zone compared with the records the deployment needs, as
// dns init compares them: each hostname that holds something else, or
// nothing, is a failure of its own, since a visitor is sent wherever the
// record points; the records as wanted are one line.
func Records(p RecordsProbe) []Finding {
	if !p.Asked {
		return nil
	}
	if p.Err != "" {
		return []Finding{{Section: SectionRecords, Level: Fail, Line: "the zone's records could not be read: " + p.Err}}
	}
	var fails, rest []Finding
	present := 0
	for _, e := range p.Entries {
		record := fmt.Sprintf("%s %s", e.Type, e.Name)
		wanted := fmt.Sprintf("wanted: %s -> %s (zone %s)", record, recordContent(e.Want), e.Zone)
		switch e.Action {
		case dns.Present:
			present++
		case dns.Conflict:
			first, more, _ := strings.Cut(e.Detail, ". ")
			advice := "A visitor is sent there. Delete it at the provider by hand, because paisans dns init updates only a record it made itself and paisans dns prune keeps one whose name is still wanted."
			switch {
			case e.Parent:
				wanted = fmt.Sprintf("this deployment's: %s -> %s (zone %s)", record, recordContent(e.Want), e.Zone)
				advice = "Give each its own DMARC policy, or delete this one at the provider by hand; " + progname.Name() + " dns init then gives each hostname its own."
			case e.Guard:
				wanted = fmt.Sprintf("checked: %s holds no SPF record that lets a server send (zone %s)", e.Name, e.Zone)
				advice = "If the hostname sends mail, list it in community.mail_domains; otherwise remove that record at the provider."
			case e.Leftover || e.Advisory:
				advice = progname.Name() + " dns prune --execute removes it."
			case strings.Contains(first, "proxied"):
				advice = more
			}
			fails = append(fails, Finding{Section: SectionRecords, Level: Fail, Line: record + ": " + strings.TrimSuffix(first, "."), More: []string{wanted, advice}})
		case dns.Create:
			advice := progname.Name() + " dns init --execute creates it."
			if e.Type == "A" || e.Type == "AAAA" {
				advice = "Nothing can reach it there. " + advice
			}
			fails = append(fails, Finding{Section: SectionRecords, Level: Fail, Line: fmt.Sprintf("%s has no %s record", e.Name, e.Type), More: []string{wanted, advice}})
		case dns.Update:
			fails = append(fails, Finding{Section: SectionRecords, Level: Fail, Line: fmt.Sprintf("%s points at an address this deployment no longer declares", record),
				More: []string{wanted, e.Detail + ". paisans dns init --execute updates it."}})
		case dns.Moving:
			rest = append(rest, Finding{Section: SectionRecords, Level: Info, Line: record + " waits for " + progname.Name() + " gateway move", More: []string{e.Detail}})
		case dns.Warn:
			rest = append(rest, Finding{Section: SectionRecords, Level: Warn, Line: record + " is not this toolkit's to write, and is missing or weak",
				More: []string{e.Detail, "expected at least: " + strings.TrimPrefix(wanted, "wanted: ")}})
		}
	}
	out := append(fails, rest...)
	if present > 0 || len(out) == 0 {
		out = append(out, Finding{Section: SectionRecords, Level: OK, Line: fmt.Sprintf("%d %s present as the deployment needs", present, plural(present, "record"))})
	}
	return out
}

func recordContent(w dns.Want) string {
	if w.Type == "MX" {
		return fmt.Sprintf("%d %s", w.Priority, w.Content)
	}
	return w.Content
}

// providerChecked reports whether doctor has a check for the provider's
// tokens and records: the providers internal/dns implements.
func providerChecked(provider string) bool {
	return slices.Contains(dns.Implemented(), provider)
}

// addDNS adds the DNS checks for the Caddy sites among sites. They exist
// only when acme.provider is set and a site asked runs Paisans' Caddy:
// with --site, the workstation's checks too only when a named site is a
// Caddy site, since a run limited to some sites asks about those alone.
func addDNS(g *Graph, cfg *config.Config, sites []string, reads DNSReads) {
	if cfg.ACME.Provider == "" {
		return
	}
	var caddy []string
	for _, site := range cfg.CaddySites() {
		if slices.Contains(sites, site) {
			caddy = append(caddy, site)
		}
	}
	if len(caddy) == 0 {
		return
	}
	provider := cfg.ACME.Provider

	var logs []string
	for _, site := range caddy {
		name := SectionCaddy + " " + site
		logs = append(logs, name)
		g.Add(Node{Name: name, Section: SectionCaddy, Site: site, Subject: site,
			Hard: []Need{Hard(SectionDocker + " " + site)},
			Gather: func(Input) func(*Input) {
				p := reads.CaddyLog(site)
				return func(in *Input) { in.Caddy = append(in.Caddy, p) }
			},
			Outcome: func(in Input) Outcome {
				for _, p := range bySite(in.Caddy, site, CaddyLogProbe.site) {
					if p.Err != "" {
						return Failed
					}
				}
				return Passed
			},
			Judge: func(in Input) []Finding { return CaddyCertificates(cfg, bySite(in.Caddy, site, CaddyLogProbe.site)) },
		})
	}

	// Nothing at the provider is asked while a Caddy log shows its lockout,
	// and nothing at all for a provider doctor has no check for.
	unless := func(in Input) string {
		if !providerChecked(provider) {
			return "doctor has no check for " + provider + "'s tokens; caddy certificates is what the log says"
		}
		for _, p := range in.Caddy {
			if Lockout(p) {
				return "not asked, so as not to feed Cloudflare's lockout, which caddy's log on " + p.Site + " shows. Fix the token's Client IP Address Filtering; Caddy's next scheduled attempt then succeeds, with nothing restarted"
			}
		}
		return ""
	}

	var secrets []Need
	if providerChecked(provider) {
		g.Add(Node{Name: SectionSecrets, Section: SectionSecrets,
			Gather: func(Input) func(*Input) {
				p := reads.Secrets()
				p.Asked = true
				return func(in *Input) { in.Secrets = p }
			},
			Outcome: func(in Input) Outcome {
				if in.Secrets.Err != "" {
					return Skipped
				}
				return Passed
			},
			Judge: func(in Input) []Finding { return Secrets(in.Secrets) },
		})
		secrets = []Need{Hard(SectionSecrets)}
	}

	for _, site := range caddy {
		g.Add(Node{Name: SectionToken + " " + site, Section: SectionToken, Site: site, Subject: site,
			Hard:   []Need{Hard(SectionAdmission + " " + site)},
			Soft:   logs,
			Unless: unless,
			Gather: func(Input) func(*Input) {
				r := reads.SiteToken(site)
				r.Place = site
				return func(in *Input) { in.Tokens = append(in.Tokens, r) }
			},
			Outcome: func(in Input) Outcome { return tokenOutcome(in.Tokens, site) },
			Judge:   func(in Input) []Finding { return tokenFindings(in.Tokens, site) },
		})
	}
	workstation := SectionToken + " " + dns.WorkstationPlace
	g.Add(Node{Name: workstation, Section: SectionToken, Subject: dns.WorkstationPlace,
		Hard:   secrets,
		Soft:   logs,
		Unless: unless,
		Gather: func(Input) func(*Input) {
			r := reads.WorkstationToken()
			r.Place = dns.WorkstationPlace
			return func(in *Input) { in.Tokens = append(in.Tokens, r) }
		},
		Outcome: func(in Input) Outcome { return tokenOutcome(in.Tokens, dns.WorkstationPlace) },
		Judge:   func(in Input) []Finding { return tokenFindings(in.Tokens, dns.WorkstationPlace) },
	})

	// What the zone holds does not depend on where it is asked from, so the
	// records are read from the workstation alone.
	g.Add(Node{Name: SectionRecords, Section: SectionRecords,
		Hard: []Need{Hard(workstation)},
		Gather: func(Input) func(*Input) {
			p := RecordsProbe{Asked: true}
			if plan, err := buildRecords(cfg, reads); err != nil {
				p.Err = strings.TrimPrefix(err.Error(), "dns: ")
			} else {
				p.Entries = plan.Entries
			}
			return func(in *Input) { in.Records = p }
		},
		Outcome: func(in Input) Outcome {
			if in.Records.Err != "" {
				return Failed
			}
			return Passed
		},
		Judge: func(in Input) []Finding { return Records(in.Records) },
	})
}

// buildRecords is the plan dns init builds, which reads and never writes.
func buildRecords(cfg *config.Config, reads DNSReads) (*dns.Plan, error) {
	provider, err := reads.DNSProvider()
	if err != nil {
		return nil, err
	}
	wants, err := dns.Desired(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return dns.Build(ctx, provider, cfg.Deployment(), wants)
}
