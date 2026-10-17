package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/dns/dnstest"
	"github.com/paisans-software/paisans-cli/internal/doctor"
)

// doctorToken is the DNS token in this file's secrets. It is its own, so
// that registering it as a secret hides nothing another test looks for.
const doctorToken = "doctor-dns-token-not-a-secret-5e1a"

// cloudflareDoctor writes the fixture configuration with Cloudflare as its
// provider and vm's public address declared, and secrets holding
// doctorToken, and returns their paths.
func cloudflareDoctor(t *testing.T) (configPath, secretsPath string) {
	t.Helper()
	data, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), "    endpoint: vm.example.org:51820\n",
		"    endpoint: vm.example.org:51820\n    public_address: 203.0.113.10\n", 1)
	edited = strings.Replace(edited, "  provider: desec\n", "  provider: cloudflare\n", 1)
	secrets, err := os.ReadFile(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	withToken := strings.Replace(string(secrets), `acme_dns_token: "fixture-not-a-secret-desec"`, `acme_dns_token: "`+doctorToken+`"`, 1)
	if !strings.Contains(edited, "provider: cloudflare") || !strings.Contains(withToken, doctorToken) {
		t.Fatal("the fixtures no longer have the lines this test edits")
	}
	dir := t.TempDir()
	configPath, secretsPath = filepath.Join(dir, "paisans-stack.yaml"), filepath.Join(dir, "secrets.enc.yaml")
	if err := os.WriteFile(configPath, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretsPath, []byte(withToken), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, secretsPath
}

// tokenCalls records each place's token check, and stands in for the
// provider at every place.
type tokenCalls struct {
	mu     sync.Mutex
	places []string
	tokens []string
	files  []string
}

func (c *tokenCalls) add(place, token, file string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.places = append(c.places, place)
	c.tokens = append(c.tokens, token)
	c.files = append(c.files, file)
}

func withTokenChecks(t *testing.T, site func(check dns.TokenCheck) dns.TokenResult, workstation func(check dns.TokenCheck, token string) dns.TokenResult, provider dns.Provider) *tokenCalls {
	t.Helper()
	calls := &tokenCalls{}
	savedSite, savedWorkstation, savedFor := doctorSiteToken, workstationTokenCheck, doctorDNSFor
	t.Cleanup(func() { doctorSiteToken, workstationTokenCheck, doctorDNSFor = savedSite, savedWorkstation, savedFor })
	doctorSiteToken = func(_ context.Context, check dns.TokenCheck, host dns.Runner, envFile string) dns.TokenResult {
		calls.add(check.Place, "", envFile)
		return site(check)
	}
	workstationTokenCheck = func(_ context.Context, check dns.TokenCheck, token string) dns.TokenResult {
		calls.add(check.Place, token, "")
		return workstation(check, token)
	}
	doctorDNSFor = func(name, token string) (dns.Provider, error) {
		calls.add("provider", token, "")
		return provider, nil
	}
	return calls
}

func tokenPassed(check dns.TokenCheck) dns.TokenResult {
	return dns.TokenResult{Status: dns.TokenPassed, Place: check.Place, Zone: "example.org",
		Finding: "cloudflare accepts the DNS token from " + check.Place + "; zone example.org; record edit not checked"}
}

// vmOnly is vm alone, answering reach and its Caddy log.
func vmOnly(cfg *config.Config, log string) (map[string]doctorHost, *[]string) {
	var sent, writes []string
	return map[string]doctorHost{
		"vm.example.org": {name: "vm.example.org", sent: &sent, writes: &writes, answers: map[string]string{
			doctor.ReachCommand:                      "",
			doctor.CaddyLogCommand(cfg.Deployment()): log,
		}},
	}, &sent
}

// Doctor checks the token from each Caddy site, with the caddy.env Caddy
// reads there, and from this workstation, with the token from --secrets,
// then reads the zone's records with it.
func TestDoctorChecksTheDNSTokenFromEveryPlace(t *testing.T) {
	configPath, secretsPath := cloudflareDoctor(t)
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	hosts, _ := vmOnly(cfg, quietCaddyLog)
	withDoctorHosts(t, cfg, hosts)
	zone := dnstest.New()
	zone.Zones["example.org"] = "zone-1"
	calls := withTokenChecks(t, tokenPassed, func(check dns.TokenCheck, _ string) dns.TokenResult { return tokenPassed(check) }, zone)
	rec := withRecorder(t, true)
	_ = runDoctor([]string{"--config", configPath, "--secrets", secretsPath, "--site", "vm"})

	want := []string{"vm", dns.WorkstationPlace, "provider"}
	if strings.Join(calls.places, ",") != strings.Join(want, ",") {
		t.Fatalf("checked %v, want %v", calls.places, want)
	}
	if calls.files[0] != dns.CaddyEnvFile(cfg.Deployment()) || calls.tokens[1] != doctorToken || calls.tokens[2] != doctorToken {
		t.Errorf("the site's check reads %q; the workstation's had %q", calls.files[0], calls.tokens[1:])
	}
	for _, line := range []string{"check caddy certificates vm", "check dns token vm", "check dns token workstation", "check dns records", "check secrets"} {
		if !rec.Has("step", line) {
			t.Errorf("no line %q:\n%s", line, rec.Lines())
		}
	}
	if !strings.Contains(rec.Lines(), "has no A record") || len(zone.Deletes) != 0 {
		t.Errorf("an empty zone is not reported, or was written to:\n%s", rec.Lines())
	}
}

// Secrets that will not open skip the workstation's check with the reason,
// and every other check runs.
func TestDoctorWithoutSecretsChecksEveryOtherPlace(t *testing.T) {
	configPath, _ := cloudflareDoctor(t)
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	hosts, _ := vmOnly(cfg, quietCaddyLog)
	withDoctorHosts(t, cfg, hosts)
	calls := withTokenChecks(t, tokenPassed, func(check dns.TokenCheck, _ string) dns.TokenResult { return tokenPassed(check) }, dnstest.New())
	rec := withRecorder(t, true)
	missing := filepath.Join(t.TempDir(), "secrets.enc.yaml")
	_ = runDoctor([]string{"--config", configPath, "--secrets", missing, "--site", "vm"})
	if strings.Join(calls.places, ",") != "vm" {
		t.Errorf("checked %v, want vm alone", calls.places)
	}
	if !rec.Has("warned", "check secrets") || !strings.Contains(rec.Lines(), "skipped: cannot open ") ||
		!rec.Has("explain", "Not checked because of this: dns token workstation, dns records.") {
		t.Errorf("the skip does not say why, or what it kept from running:\n%s", rec.Lines())
	}
}

// echoingProvider is a provider that puts the token it was given in its
// errors, as a careless one could.
type echoingProvider struct{ *dnstest.Provider }

func (echoingProvider) Records(context.Context, string, string) ([]dns.Record, error) {
	return nil, errors.New("HTTP 403: token " + doctorToken + " is not allowed to list records")
}

// The token never reaches the terminal: not from a provider that echoes it
// in a finding or an error, and not in a detail or a trace.
func TestDoctorPrintsNoToken(t *testing.T) {
	configPath, secretsPath := cloudflareDoctor(t)
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	zone := dnstest.New()
	zone.Zones["example.org"] = "zone-1"
	for name, workstation := range map[string]func(dns.TokenCheck, string) dns.TokenResult{
		"in the token check's finding": func(check dns.TokenCheck, token string) dns.TokenResult {
			return dns.TokenResult{Status: dns.TokenFailed, Place: check.Place, Finding: "cloudflare refuses " + token, Advice: "the token " + token + " is refused"}
		},
		"in the records' error": func(check dns.TokenCheck, _ string) dns.TokenResult { return tokenPassed(check) },
	} {
		t.Run(name, func(t *testing.T) {
			hosts, _ := vmOnly(cfg, quietCaddyLog)
			withDoctorHosts(t, cfg, hosts)
			withTokenChecks(t, tokenPassed, workstation, echoingProvider{zone})
			rec := withRecorder(t, true)
			err := runDoctor([]string{"--config", configPath, "--secrets", secretsPath, "--site", "vm"})
			out := rec.Lines()
			if !strings.Contains(out, "check dns token workstation") {
				t.Fatalf("the workstation's check did not run:\n%s", out)
			}
			if strings.Contains(out, doctorToken) || (err != nil && strings.Contains(err.Error(), doctorToken)) {
				t.Errorf("the token reached the output:\n%s", out)
			}
		})
	}

	// And doctor strips it itself, before anything prints.
	d := doctorReads{cfg: cfg, token: &workstationToken{path: secretsPath}}
	if p := d.Secrets(); p.Err != "" {
		t.Fatal(p.Err)
	}
	withTokenChecks(t, tokenPassed, func(check dns.TokenCheck, token string) dns.TokenResult {
		return dns.TokenResult{Status: dns.TokenFailed, Finding: "refuses " + token, Advice: token}
	}, echoingProvider{zone})
	if r := d.WorkstationToken(); strings.Contains(r.Finding+r.Advice, doctorToken) {
		t.Errorf("the token is in the result: %+v", r)
	}
	p, err := d.DNSProvider()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Records(context.Background(), "zone-1", "talk.example.org"); err == nil || strings.Contains(err.Error(), doctorToken) {
		t.Errorf("the provider's error carries the token: %v", err)
	}
	if err := p.Create(context.Background(), "zone-1", dns.Record{}); err == nil {
		t.Error("doctor's provider wrote a record")
	}
}

// A Caddy log showing the lockout keeps doctor from calling the provider
// from anywhere.
func TestDoctorCallsNothingDuringALockout(t *testing.T) {
	configPath, secretsPath := cloudflareDoctor(t)
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	locked := `{"level":"error","ts":1792024608.576382,"logger":"tls.obtain","msg":"could not get certificate from issuer","identifier":"talk.example.org","issuer":"acme.zerossl.com-v2-DV90","error":"[talk.example.org] solving challenges: presenting for challenge: adding temporary record for zone \"example.org.\": got error status: HTTP 429: [{Code:10502 Message:Too many authentication failures. Please try again later. ErrorChain:[]}] (order=https://acme.zerossl.com/v2/DV90/order/x) (ca=https://acme.zerossl.com/v2/DV90)"}`
	hosts, _ := vmOnly(cfg, "now 1792024700\nstarted 2026-10-15T00:00:00Z\n"+locked+"\n")
	withDoctorHosts(t, cfg, hosts)
	calls := withTokenChecks(t, tokenPassed, func(check dns.TokenCheck, _ string) dns.TokenResult { return tokenPassed(check) }, dnstest.New())
	rec := withRecorder(t, true)
	if err := runDoctor([]string{"--config", configPath, "--secrets", secretsPath, "--site", "vm"}); err == nil {
		t.Error("a lockout is not a failure")
	}
	if len(calls.places) != 0 {
		t.Errorf("called the provider from %v during a lockout", calls.places)
	}
	if !strings.Contains(rec.Lines(), "fail: check caddy certificates vm cloudflare is locking the DNS token out") ||
		!strings.Contains(rec.Lines(), "so as not to feed Cloudflare's lockout") {
		t.Errorf("the lockout is not reported:\n%s", rec.Lines())
	}
}
