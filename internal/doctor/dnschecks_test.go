package doctor

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/dns/dnstest"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// The staging log, with addresses and hostnames replaced: a 9109 for a
// hostname, a 10502 for another against a second CA, and one obtained.
const (
	stagingRefused  = `{"level":"error","ts":1792024602.9312034,"logger":"tls.obtain","msg":"could not get certificate from issuer","identifier":"gate.example.org","issuer":"acme-v02.api.letsencrypt.org-directory","error":"[gate.example.org] solving challenges: presenting for challenge: adding temporary record for zone \"example.org.\": got error status: HTTP 403: [{Code:9109 Message:Cannot use the access token from location: 203.0.113.7 ErrorChain:[]}] (order=https://acme-staging-v02.api.letsencrypt.org/acme/order/1/2) (ca=https://acme-staging-v02.api.letsencrypt.org/directory)"}`
	stagingLocked   = `{"level":"error","ts":1792024608.576382,"logger":"tls.obtain","msg":"could not get certificate from issuer","identifier":"join.example.org","issuer":"acme.zerossl.com-v2-DV90","error":"[join.example.org] solving challenges: presenting for challenge: adding temporary record for zone \"example.org.\": got error status: HTTP 429: [{Code:10502 Message:Too many authentication failures. Please try again later. ErrorChain:[]}] (order=https://acme.zerossl.com/v2/DV90/order/x) (ca=https://acme.zerossl.com/v2/DV90)"}`
	stagingObtained = `{"level":"info","ts":1792025000.0,"logger":"tls.obtain","msg":"certificate obtained successfully","identifier":"wiki.example.org","issuer":"acme-v02.api.letsencrypt.org-directory"}`
)

// caddyLogOut is CaddyLogCommand's output: the host's clock, the
// container's start, and lines.
func caddyLogOut(now string, lines ...string) string {
	return "now " + now + "\nstarted 2026-10-15T00:00:00.000000000Z\n" + strings.Join(lines, "\n") + "\n"
}

// refusedFor is the staging 9109 line for another hostname.
func refusedFor(host string) string {
	return strings.ReplaceAll(stagingRefused, "gate.example.org", host)
}

// lockedFor is the staging 10502 line for another hostname.
func lockedFor(host string) string {
	return strings.ReplaceAll(stagingLocked, "join.example.org", host)
}

// dnsFixture is the fixture deployment with Cloudflare as its provider.
func dnsFixture(t *testing.T) *config.Config {
	cfg := fixture(t)
	cfg.ACME.Provider = "cloudflare"
	return cfg
}

// servedBy is the first n hostnames site's Caddy serves, in the order it
// serves them, of those that are not in the staging log's lines.
func servedBy(t *testing.T, cfg *config.Config, site string, n int) []string {
	t.Helper()
	var out []string
	for _, h := range dns.ServedHostnames(cfg, site) {
		switch h.Name {
		case "wiki.example.org", "gate.example.org", "join.example.org":
			continue
		}
		if len(out) < n {
			out = append(out, h.Name)
		}
	}
	if len(out) < n {
		t.Fatalf("%s serves %d hostnames, want %d", site, len(out), n)
	}
	return out
}

// The staging log gives one finding for the 9109, naming every hostname it
// failed for, with the address Cloudflare judged and its advice; a
// hostname whose newest line obtained a certificate is fine, and one the
// site does not serve is not its finding.
func TestCaddyLogGivesOneFindingPerCause(t *testing.T) {
	cfg := dnsFixture(t)
	// gate's 9109 is the staging line; join's 10502 came first and a 9109
	// after it, as they interleaved on staging; four more hostnames failed
	// as gate did.
	others := servedBy(t, cfg, "vm", 4)
	lines := []string{stagingLocked, stagingRefused, refusedFor("join.example.org")}
	for _, h := range others {
		lines = append(lines, refusedFor(h))
	}
	var hosts []string
	for _, h := range dns.ServedHostnames(cfg, "vm") {
		switch h.Name {
		case "gate.example.org", "join.example.org", others[0], others[1], others[2], others[3]:
			hosts = append(hosts, h.Name)
		}
	}
	// wiki failed first, and then obtained one.
	lines = append(lines, refusedFor("wiki.example.org"), stagingObtained)
	got := CaddyCertificates(cfg, []CaddyLogProbe{{Site: "vm", Out: caddyLogOut("1792026000", lines...)}})
	if len(got) != 1 {
		t.Fatalf("want one finding, got:\n%s", dump(got))
	}
	f := got[0]
	if f.Level != Fail || f.Line != "vm: cloudflare refuses this server's DNS token (203.0.113.7)" {
		t.Errorf("line %q", f.Line)
	}
	want := "no certificate for 6 hostnames: " + strings.Join(hosts, ", ")
	if len(f.More) != 2 || f.More[0] != want || f.More[1] != dns.FilterAdvice {
		t.Errorf("more %q\nwant %q", f.More, want)
	}
	if strings.Contains(strings.Join(f.More, " "), "wiki.example.org") {
		t.Errorf("wiki obtained its certificate last, and is still named: %q", f.More)
	}
}

// A line from before the container started is not this container's, and
// a log with only successes is fine.
func TestCaddyLogReadsFromTheContainersStart(t *testing.T) {
	cfg := dnsFixture(t)
	out := "now 1792026000\nstarted 2026-10-15T00:40:00Z\n" + refusedFor("talk.example.org") + "\n" + stagingObtained + "\n"
	got := CaddyCertificates(cfg, []CaddyLogProbe{{Site: "vm", Out: out}})
	if len(got) != 1 || got[0].Level != OK || !reflect.DeepEqual(got[0].More, []string{"obtained: wiki.example.org"}) {
		t.Errorf("got:\n%s", dump(got))
	}
}

// Any other error is reported with its own last clause, and a rate limit
// with when to retry.
func TestCaddyLogReportsOtherCausesInTheirOwnWords(t *testing.T) {
	cfg := dnsFixture(t)
	other := strings.Replace(refusedFor("talk.example.org"), `got error status: HTTP 403: [{Code:9109 Message:Cannot use the access token from location: 203.0.113.7 ErrorChain:[]}]`, `dial tcp: lookup api.cloudflare.com: no such host`, 1)
	limited := strings.Replace(refusedFor("blog.example.org"), `solving challenges: presenting for challenge: adding temporary record for zone \"example.org.\": got error status: HTTP 403: [{Code:9109 Message:Cannot use the access token from location: 203.0.113.7 ErrorChain:[]}]`, `HTTP 429 urn:ietf:params:acme:error:rateLimited - too many certificates (5) already issued for this exact set of domains in the last 168h0m0s, retry after 2026-10-16 09:00:00 UTC: see https://letsencrypt.org/docs/rate-limits/`, 1)
	got := CaddyCertificates(cfg, []CaddyLogProbe{{Site: "vm", Out: caddyLogOut("1792026000", other, limited)}})
	var lines []string
	for _, f := range got {
		lines = append(lines, f.Line)
	}
	want := []string{"vm: the certificate authority is rate limiting blog.example.org", "vm: no such host"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines %q\nwant %q", lines, want)
	}
	if !strings.Contains(got[0].More[1], "retry after 2026-10-16 09:00:00 UTC") {
		t.Errorf("the rate limit does not say when: %q", got[0].More)
	}
}

// dnsReads is fixtureReads with the DNS reads: Caddy's log per site, the
// secrets, each place's token check and a provider, every one logged.
type dnsReads struct {
	*fixtureReads
	logs       map[string]string
	secretsErr string
	tokens     map[string]dns.TokenResult
	provider   *dnstest.Provider
}

func (d *dnsReads) CaddyLog(site string) CaddyLogProbe {
	d.asked("caddy log " + site)
	return CaddyLogProbe{Site: site, Out: d.logs[site]}
}

func (d *dnsReads) Secrets() SecretsProbe {
	d.asked("secrets")
	return SecretsProbe{Path: "secrets.enc.yaml", Err: d.secretsErr}
}

func (d *dnsReads) token(place string) dns.TokenResult {
	if r, ok := d.tokens[place]; ok {
		return r
	}
	return dns.TokenResult{Status: dns.TokenPassed, Finding: "cloudflare accepts the DNS token from " + place + "; zone example.org; record edit not checked"}
}

func (d *dnsReads) SiteToken(site string) dns.TokenResult {
	d.asked("token " + site)
	return d.token(site)
}

func (d *dnsReads) WorkstationToken() dns.TokenResult {
	d.asked("token workstation")
	return d.token(dns.WorkstationPlace)
}

func (d *dnsReads) DNSProvider() (dns.Provider, error) {
	d.asked("provider")
	if d.provider == nil {
		return nil, errors.New("no provider in the fixture")
	}
	return d.provider, nil
}

// dnsInput is the healthy fixture with the monitor's site asked too, so
// both Caddy sites are.
func dnsInput() Input {
	in := healthyInput()
	in.Sites = append(in.Sites, "watch")
	in.Reach = reachAll(in.Sites...)
	return in
}

func newDNSReads(in Input) *dnsReads {
	return &dnsReads{fixtureReads: &fixtureReads{in: in, log: &events{}}, logs: map[string]string{}, tokens: map[string]dns.TokenResult{}}
}

func (d *dnsReads) askedFor(prefix string) []string {
	var out []string
	for _, read := range d.log.list {
		if strings.HasPrefix(read, prefix) {
			out = append(out, read)
		}
	}
	return out
}

// Every DNS check is a node: Caddy's log on each Caddy site, then the
// token from each, then the workstation's and the records, each asked once
// its needs are met.
func TestDNSChecksAreNodes(t *testing.T) {
	cfg := dnsFixture(t)
	in := dnsInput()
	reads := newDNSReads(in)
	reads.provider = dnstest.New()
	g := Checks(cfg, in.Sites, reads)
	for _, name := range []string{"caddy certificates vm", "caddy certificates watch", "secrets", "dns token vm", "dns token watch", "dns token workstation", "dns records"} {
		if !g.Has(name) {
			t.Errorf("no node %q in %v", name, g.Names())
		}
	}
	if g.Has("caddy certificates home-a") || g.Has("dns token home-a") {
		t.Errorf("a site without Caddy has DNS nodes: %v", g.Names())
	}
	rec := &ui.Recorder{}
	g.Run(rec, 1)
	want := []string{"caddy log vm", "caddy log watch", "secrets", "token vm", "token watch", "token workstation", "provider"}
	var got []string
	for _, read := range reads.log.list {
		for _, w := range want {
			if read == w {
				got = append(got, read)
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DNS reads %v, want %v", got, want)
	}
	for _, line := range []string{"check dns token workstation", "check dns records", "check secrets"} {
		if !rec.Has("step", line) {
			t.Errorf("no line %q:\n%s", line, rec.Lines())
		}
	}
}

// A site whose token Cloudflare refuses is that site's ✗ line, with the
// advice; the other places are asked all the same.
func TestASiteRefusedIsItsOwnFailure(t *testing.T) {
	cfg := dnsFixture(t)
	in := dnsInput()
	reads := newDNSReads(in)
	reads.provider = dnstest.New()
	reads.tokens["vm"] = dns.TokenResult{Status: dns.TokenFailed, Code: 9109, Address: "203.0.113.7",
		Finding: "cloudflare refuses the DNS token from vm (203.0.113.7)", Advice: dns.FilterAdvice}
	rec := &ui.Recorder{}
	report, _ := Checks(cfg, in.Sites, reads).Run(rec, 1)
	var found bool
	for _, f := range report.Findings {
		if f.Section == SectionToken && f.Level == Fail && f.Line == "vm: cloudflare refuses the DNS token from vm (203.0.113.7)" && reflect.DeepEqual(f.More, []string{dns.FilterAdvice}) {
			found = true
		}
	}
	if !found {
		t.Errorf("no failure for vm's token:\n%s", dump(report.Findings))
	}
	if len(reads.askedFor("token ")) != 3 {
		t.Errorf("the other places were not asked: %v", reads.log.list)
	}
}

// Secrets that will not open, Eg: no age key on this workstation, skip the
// workstation's token check and the records with a reason, and every other
// check runs: doctor's subject is the deployment, not this workstation.
func TestSecretsThatWillNotOpenSkipOnlyTheWorkstation(t *testing.T) {
	cfg := dnsFixture(t)
	in := dnsInput()
	reads := newDNSReads(in)
	reads.secretsErr = "no age key for this deployment on this workstation"
	rec := &ui.Recorder{}
	report, _ := Checks(cfg, in.Sites, reads).Run(rec, 0)
	if len(reads.askedFor("token workstation")) != 0 || len(reads.askedFor("provider")) != 0 {
		t.Errorf("the workstation's checks ran without secrets: %v", reads.log.list)
	}
	if len(reads.askedFor("token ")) != 2 || len(reads.askedFor("caddy log ")) != 2 {
		t.Errorf("the sites' checks did not all run: %v", reads.log.list)
	}
	if !rec.Has("warned", "check secrets") || !strings.Contains(rec.Lines(), "skipped: cannot open secrets.enc.yaml: no age key for this deployment on this workstation") {
		t.Errorf("the secrets line does not say why:\n%s", rec.Lines())
	}
	if !rec.Has("explain", "Not checked because of this: dns token workstation, dns records.") {
		t.Errorf("the skipped checks are not named:\n%s", rec.Lines())
	}
	if report.Failed() {
		t.Errorf("a workstation without the key failed the deployment:\n%s", dump(report.Findings))
	}
}

// A 10502 in any Caddy log in the last five minutes skips every token
// check, from every place, and nothing at the provider is called: refused
// calls are what keep the lockout going.
func TestLockoutSkipsEveryTokenCheckAndCallsNothing(t *testing.T) {
	cfg := dnsFixture(t)
	in := dnsInput()
	reads := newDNSReads(in)
	reads.provider = dnstest.New()
	reads.logs["watch"] = caddyLogOut("1792024700", lockedFor("health.example.org"))
	rec := &ui.Recorder{}
	report, again := Checks(cfg, in.Sites, reads).Run(rec, 0)
	if calls := append(reads.askedFor("token "), reads.askedFor("provider")...); len(calls) != 0 {
		t.Errorf("the provider was called during a lockout: %v", calls)
	}
	for _, place := range []string{"vm", "watch", "workstation"} {
		if !rec.Has("warned", "check dns token "+place) {
			t.Errorf("dns token %s has no skipped line:\n%s", place, rec.Lines())
		}
	}
	if !strings.Contains(rec.Lines(), "skipped: not asked, so as not to feed Cloudflare's lockout, which caddy's log on watch shows. Fix the token's Client IP Address Filtering; Caddy's next scheduled attempt then succeeds, with nothing restarted") {
		t.Errorf("the skip does not say why, or what ends it:\n%s", rec.Lines())
	}
	if f := report.Findings; !strings.Contains(dump(f), "cloudflare is locking the DNS token out") {
		t.Errorf("caddy certificates watch does not report the lockout:\n%s", dump(f))
	}
	if j := Checks(cfg, in.Sites, reads).Judge(again, false); !reflect.DeepEqual(j.Findings, report.Findings) {
		t.Errorf("judged again:\n%s\nrun:\n%s", dump(j.Findings), dump(report.Findings))
	}

	// Ten minutes on, the same log is no lockout, and every place is asked.
	reads = newDNSReads(in)
	reads.provider = dnstest.New()
	reads.logs["watch"] = caddyLogOut("1792025300", lockedFor("health.example.org"))
	Checks(cfg, in.Sites, reads).Run(ui.Discard, 0)
	if len(reads.askedFor("token ")) != 3 {
		t.Errorf("an old 10502 kept the token from being checked: %v", reads.log.list)
	}
}

// A provider doctor has no check for skips the token and records checks
// with that reason, as skipped and not passed; Caddy's log is read for
// every provider.
func TestAProviderWithoutACheckIsSkipped(t *testing.T) {
	cfg := fixture(t) // desec
	in := dnsInput()
	reads := newDNSReads(in)
	rec := &ui.Recorder{}
	report, _ := Checks(cfg, in.Sites, reads).Run(rec, 0)
	if len(reads.askedFor("caddy log ")) != 2 {
		t.Errorf("caddy's log was not read: %v", reads.log.list)
	}
	if got := append(reads.askedFor("token "), append(reads.askedFor("secrets"), reads.askedFor("provider")...)...); len(got) != 0 {
		t.Errorf("asked %v of a provider doctor has no check for", got)
	}
	skipped := 0
	for _, f := range report.Findings {
		if f.Section == SectionToken && f.Level == Skip {
			skipped++
		}
	}
	if !strings.Contains(rec.Lines(), "skipped: doctor has no check for desec's tokens; caddy certificates is what the log says") || skipped != 3 || report.Pruned != 1 {
		t.Errorf("skipped %d, pruned %d:\n%s", skipped, report.Pruned, rec.Lines())
	}
}

// --site with no Caddy site asks nothing of the provider and has no DNS
// checks.
func TestASiteRunWithoutCaddyHasNoDNSChecks(t *testing.T) {
	cfg := dnsFixture(t)
	g := Checks(cfg, []string{"home-a"}, newDNSReads(dnsInput()))
	for _, name := range g.Names() {
		if strings.HasPrefix(name, "dns ") || strings.HasPrefix(name, "caddy ") || name == "secrets" {
			t.Errorf("a run on home-a alone has %s", name)
		}
	}
}

// A record at one of the deployment's hostnames that points elsewhere is a
// failure naming the hostname and what the zone holds; a hostname with no
// record fails with dns init's advice; the records as wanted are one line.
func TestRecordsThatPointElsewhere(t *testing.T) {
	cfg := dnsFixture(t)
	vm := cfg.Sites["vm"]
	vm.PublicAddress = "203.0.113.10"
	cfg.Sites["vm"] = vm
	in := dnsInput()
	wants, err := dns.Desired(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := dnstest.New()
	p.Zones["example.org"] = "zone-1"
	var talk dns.Want
	for _, w := range wants {
		switch {
		case w.Name == "talk.example.org" && w.Type == "A":
			talk = w
			p.InZone["zone-1"] = append(p.InZone["zone-1"], dns.Record{ID: "elsewhere", Type: "A", Name: w.Name, Content: "198.51.100.9"})
		case w.Name == "wiki.example.org":
			// wiki has no record at all.
		case !w.Advisory && !w.Leftover:
			p.InZone["zone-1"] = append(p.InZone["zone-1"], dns.Record{ID: w.Type + w.Name, Type: w.Type, Name: w.Name, Content: w.Content, Priority: w.Priority})
		}
	}
	if talk.Name == "" {
		t.Fatal("the fixture wants no A record for talk.example.org")
	}
	reads := newDNSReads(in)
	reads.provider = p
	report, _ := Checks(cfg, in.Sites, reads).Run(ui.Discard, 0)

	var records []Finding
	for _, f := range report.Findings {
		if f.Section == SectionRecords {
			records = append(records, f)
		}
	}
	var conflicts []Finding
	for _, f := range records {
		if f.Level == Fail && strings.Contains(f.Line, "talk.example.org") {
			conflicts = append(conflicts, f)
		}
	}
	if len(conflicts) != 1 || !strings.Contains(conflicts[0].Line, "198.51.100.9") || conflicts[0].More[0] != fmt.Sprintf("wanted: A talk.example.org -> %s (zone example.org)", talk.Content) ||
		!strings.Contains(conflicts[0].More[1], "Delete it at the provider by hand") {
		t.Errorf("want one failure naming talk.example.org and 198.51.100.9:\n%s", dump(records))
	}
	var missing bool
	for _, f := range records {
		if f.Level == Fail && f.Line == "wiki.example.org has no A record" && strings.Contains(strings.Join(f.More, " "), "paisans dns init --execute") {
			missing = true
		}
	}
	if !missing {
		t.Errorf("no failure for wiki.example.org's missing record:\n%s", dump(records))
	}
	last := records[len(records)-1]
	if last.Level != OK || !strings.HasSuffix(last.Line, "present as the deployment needs") {
		t.Errorf("the records present are not one line: %q", last.Line)
	}
	if len(p.Deletes) != 0 {
		t.Errorf("the records check deleted %v", p.Deletes)
	}
}

// The DNS checks' judges give Diagnose's findings for an Input that holds
// their probes, as every other check's do.
func TestDNSGraphFindsWhatDiagnoseFinds(t *testing.T) {
	cfg := dnsFixture(t)
	in := dnsInput()
	in.Caddy = []CaddyLogProbe{{Site: "vm", Out: caddyLogOut("1792026000", stagingRefused, stagingObtained)}, {Site: "watch", Err: "permission denied"}}
	in.Secrets = SecretsProbe{Asked: true, Path: "secrets.enc.yaml"}
	in.Tokens = []dns.TokenResult{
		{Place: dns.WorkstationPlace, Status: dns.TokenPassed, Finding: "cloudflare accepts the DNS token from this workstation"},
		{Place: "vm", Status: dns.TokenFailed, Finding: "cloudflare refuses the DNS token from vm (203.0.113.7)", Advice: dns.FilterAdvice},
	}
	in.Records = RecordsProbe{Asked: true, Entries: []dns.Entry{{Want: dns.Want{Type: "A", Name: "talk.example.org", Content: "203.0.113.10"}, Zone: "example.org", Action: dns.Create}}}
	want := Diagnose(cfg, in)
	got := Checks(cfg, in.Sites, newDNSReads(in)).Judge(in, true)
	if !reflect.DeepEqual(got.Findings, want.Findings) {
		t.Errorf("graph:\n%s\nDiagnose:\n%s", dump(got.Findings), dump(want.Findings))
	}
}

// A hostname's SPF check that fails says what was checked, and that the
// hostname's mail belongs in community.mail_domains.
func TestRecordsHostnameSPFCheck(t *testing.T) {
	e := dns.Entry{Want: dns.Want{Type: "TXT", Name: "talk.example.org", Content: "v=spf1 -all", Guard: true}, Zone: "example.org", Action: dns.Conflict, Detail: "an SPF record that lets a server send already exists at this name. More."}
	got := Records(RecordsProbe{Asked: true, Entries: []dns.Entry{e}})
	if len(got) == 0 || got[0].Level != Fail || len(got[0].More) != 2 ||
		!strings.HasPrefix(got[0].More[0], "checked: talk.example.org") || !strings.Contains(got[0].More[1], "community.mail_domains") {
		t.Errorf("got %+v", got)
	}
}

// A record dns init made that points at an old address fails, and says that
// dns init updates it.
func TestRecordsOwnRecordAtAnOldAddress(t *testing.T) {
	e := dns.Entry{Want: dns.Want{Type: "A", Name: "talk.example.org", Content: "203.0.113.10"}, Zone: "example.org", Action: dns.Update,
		Detail: "dns init made it for this deployment, pointing at 198.51.100.7, which the configuration no longer declares there"}
	got := Records(RecordsProbe{Asked: true, Entries: []dns.Entry{e}})
	if len(got) == 0 || got[0].Level != Fail || got[0].Line != "A talk.example.org points at an address this deployment no longer declares" || len(got[0].More) != 2 ||
		!strings.Contains(got[0].More[1], "198.51.100.7") || !strings.Contains(got[0].More[1], "paisans dns init --execute updates it") {
		t.Errorf("got %+v", got)
	}
}
