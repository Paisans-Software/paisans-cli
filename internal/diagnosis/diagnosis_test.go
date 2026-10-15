package diagnosis_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/diagnosis"
)

// stagingTLS is oauth2-proxy's line from the staging apply that stopped at
// `wait for gate`, with the domain replaced by example.org.
const stagingTLS = `oauth2-proxy-1  | [2026/10/15 09:12:44] [provider.go:55] Get "https://id.example.org/.well-known/openid-configuration": remote error: tls: internal error`

var served = diagnosis.Served{"id.example.org": "vm", "talk.example.org": "vm", "health.example.org": "health"}

func TestATLSInternalErrorOnAServedHostnameNamesCaddyAndDoctor(t *testing.T) {
	r, ok := diagnosis.FromLog("oauth2-proxy-1  | starting\n"+stagingTLS+"\n", served)
	if !ok {
		t.Fatal("the staging line got no referral")
	}
	want := "Caddy on vm has no certificate for id.example.org, so it refuses TLS for it. Run paisans doctor to see why it could not get one."
	if r.Sentence != want {
		t.Errorf("sentence %q\nwant %q", r.Sentence, want)
	}
	if r.Check != "caddy certificates vm" {
		t.Errorf("check %q", r.Check)
	}
}

// OpenSSL's words for the same alert, from a client that is not Go.
func TestOpenSSLsInternalErrorAlertIsTheSameCause(t *testing.T) {
	r, ok := diagnosis.FromLog("app-1  | curl: (35) OpenSSL/3.0.13: error:0A000438:SSL routines::tlsv1 alert internal error, connecting to https://talk.example.org/\n", served)
	if !ok || !strings.Contains(r.Sentence, "Caddy on vm has no certificate for talk.example.org") {
		t.Fatalf("got %+v, %v", r, ok)
	}
}

func TestATLSErrorOnAHostnamePaisansDoesNotServeGetsNoReferral(t *testing.T) {
	line := strings.ReplaceAll(stagingTLS, "id.example.org", "login.example.net")
	if r, ok := diagnosis.FromLog(line, served); ok {
		t.Fatalf("a referral for a hostname nobody here serves: %+v", r)
	}
	if r, ok := diagnosis.FromLog(stagingTLS, nil); ok {
		t.Fatalf("a referral with no served hostnames: %+v", r)
	}
}

func TestADatabaseThatCannotBeReachedReferstoPatroni(t *testing.T) {
	for _, line := range []string{
		"mbin-1  | SQLSTATE[08006] [7] could not connect to server: Connection refused",
		`app-1  | {"level":"fatal","msg":"failed to connect to ` + "`host=10.44.0.1 user=talk database=talk`" + `: dial error"}`,
		"app-1  | connection to server at \"10.44.0.1\", port 5432 failed: Connection refused",
		"app-1  | FATAL:  the database system is starting up",
		"app-1  | dial tcp 10.44.0.1:5432: connect: connection refused",
		"app-1  | Error: connect ECONNREFUSED 10.44.0.1:5432",
	} {
		r, ok := diagnosis.FromLog(line, served)
		if !ok {
			t.Errorf("%q: no referral", line)
			continue
		}
		if r.Sentence != "It cannot reach its database. Run paisans doctor, whose Patroni check says whether there is a primary." || r.Check != "patroni" {
			t.Errorf("%q: got %+v", line, r)
		}
	}
}

// The newest line that names a cause decides, as logCause reads a tail.
func TestTheNewestRecognisedLineDecides(t *testing.T) {
	logs := stagingTLS + "\napp-1  | dial tcp 10.44.0.1:5432: connect: connection refused\n"
	if r, _ := diagnosis.FromLog(logs, served); r.Check != "patroni" {
		t.Errorf("got %+v", r)
	}
}

func TestAnUnrecognisedLogGetsNoReferral(t *testing.T) {
	for _, logs := range []string{
		"",
		"etcd-1  | listen tcp 10.45.0.1:2380: bind: cannot assign requested address",
		"app-1  | panic: runtime error: invalid memory address",
		"app-1  | x509: certificate signed by unknown authority for id.example.org",
	} {
		if r, ok := diagnosis.FromLog(logs, served); ok {
			t.Errorf("%q: referral %+v", logs, r)
		}
	}
}

func TestAnUnreachableSiteRefersToDoctorForThatSite(t *testing.T) {
	r := diagnosis.Unreachable("home-b")
	if r.Sentence != "Run paisans doctor --site home-b, which says what the deployment is without while it is gone." || r.Check != "reach home-b" {
		t.Errorf("got %+v", r)
	}
}

// The provider's refusals, as dns.Cloudflare reports them, are worded as the
// token check's findings with the referral after.
func TestAProviderRefusalIsWordedAsTheTokenCheck(t *testing.T) {
	// dns.Cloudflare's form, and Caddy's, which quotes the provider's
	// libdns error.
	for _, refused := range []error{
		errors.New(`cloudflare: GET /zones/abc/dns_records: HTTP 403: [9109] Cannot use the access token from location: 203.0.113.7`),
		errors.New(`HTTP 403 - {Code:9109 Message:Cannot use the access token from location: 203.0.113.7}`),
	} {
		r, ok := diagnosis.Provider(refused, "")
		if !ok {
			t.Fatalf("%v: 9109 got no referral", refused)
		}
		if !strings.HasPrefix(r.Sentence, "cloudflare refuses the token from this workstation (203.0.113.7). Add the address to the token's Client IP Address Filtering, or remove the filter.") ||
			!strings.HasSuffix(r.Sentence, "Run paisans doctor, which checks the token from every place that uses it.") {
			t.Errorf("sentence %q", r.Sentence)
		}
		if r.Check != "dns token workstation" {
			t.Errorf("check %q", r.Check)
		}
	}
	if r, _ := diagnosis.Provider(errors.New("HTTP 403: [9109] Cannot use the access token from location: 203.0.113.7"), "vm"); !strings.HasPrefix(r.Sentence, "cloudflare refuses the token from vm (203.0.113.7).") || r.Check != "dns token vm" {
		t.Errorf("from a site: %+v", r)
	}

	locked := errors.New(`cloudflare: GET /zones: HTTP 429: [10502] Too many authentication failures`)
	r, ok := diagnosis.Provider(locked, "vm")
	if !ok || !strings.HasPrefix(r.Sentence, "cloudflare is locking the token out.") || !strings.Contains(r.Sentence, "Run paisans doctor") || r.Check != "dns token vm" {
		t.Errorf("10502: %+v, %v", r, ok)
	}

	if r, ok := diagnosis.Provider(errors.New("cloudflare: HTTP 500: upstream"), "vm"); ok {
		t.Errorf("an unrecognised provider error got %+v", r)
	}
	if _, ok := diagnosis.Provider(nil, "vm"); ok {
		t.Error("nil got a referral")
	}
}

// A provider that put the token in its message does not get it onto the
// terminal through the referral: only the address is carried over.
func TestAProviderRefusalCarriesNoTokenFromTheMessage(t *testing.T) {
	const token = "cfat_s3cr3tvalue"
	err := errors.New("HTTP 403: Code:9109 Message:Cannot use the access token " + token + " from location: 203.0.113.7")
	r, ok := diagnosis.Provider(err, "vm")
	if !ok {
		t.Fatal("no referral")
	}
	if strings.Contains(r.Sentence, token) {
		t.Errorf("the token reached the sentence: %q", r.Sentence)
	}
}
