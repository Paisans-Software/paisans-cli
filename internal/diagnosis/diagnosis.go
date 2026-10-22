// Package diagnosis is the causes an error from apply, init or dns refers to
// `paisans doctor` for: each one a matcher over an error or a log line, the
// sentence the operator reads, and the doctor check that diagnoses it.
//
// An error refers to doctor only where a doctor check names its cause,
// because a referral to a command that then reports everything ok is worse
// than none. So an error nothing here recognises keeps its own text and gets
// no referral.
//
// It is a package of its own, importing nothing of the toolkit, because
// internal/doctor imports internal/apply, so apply cannot import doctor, and
// a second copy of these causes would drift from the first.
package diagnosis

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/progname"
)

// Referral is what an error says about a cause doctor diagnoses.
type Referral struct {
	// Check is the doctor check that names the cause, as doctor reports it:
	// Eg: `caddy certificates vm`, `reach home-b`, `patroni`.
	Check string
	// Sentence is what the operator reads, ending with the referral to
	// `paisans doctor`.
	Sentence string
}

// Served is each hostname a Caddy site serves, to that site's name. The
// TLS cause is recognised only on these, since a TLS error on a hostname
// Paisans does not serve is not Caddy's, and doctor would not explain it.
type Served map[string]string

// logCauses are the causes recognised in a failing service's log, each a
// matcher over one line.
var logCauses = []func(line string, served Served) (Referral, bool){
	noCertificate,
	noDatabase,
}

// FromLog is the referral for the newest line of logs that a cause matches,
// read newest first as the health gate reads a tail, or false when no line
// matches one.
func FromLog(logs string, served Served) (Referral, bool) {
	lines := strings.Split(logs, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		for _, match := range logCauses {
			if r, ok := match(lines[i], served); ok {
				return r, true
			}
		}
	}
	return Referral{}, false
}

// tlsInternalError is the alert Caddy answers a handshake with for a
// hostname it holds no certificate for (observed on staging, 2026-10-15), in
// Go's words and in OpenSSL's.
var tlsInternalError = regexp.MustCompile(`(?i)tls: internal error|tlsv1 alert internal error`)

// hostnameLike is any dotted name in a line, the candidates for the hostname
// a TLS failure was for.
var hostnameLike = regexp.MustCompile(`[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)+`)

// noCertificate is a TLS internal error fetching a hostname a Caddy site
// serves: that Caddy has no certificate for it, and doctor's caddy
// certificates check reads its log for why.
func noCertificate(line string, served Served) (Referral, bool) {
	if len(served) == 0 || !tlsInternalError.MatchString(line) {
		return Referral{}, false
	}
	for _, name := range hostnameLike.FindAllString(line, -1) {
		host := strings.ToLower(name)
		if site, ok := served[host]; ok {
			return Referral{
				Check:    "caddy certificates " + site,
				Sentence: fmt.Sprintf("Caddy on %s has no certificate for %s, so it refuses TLS for it. Run "+progname.Name()+" doctor to see why it could not get one.", site, host),
			}, true
		}
	}
	return Referral{}, false
}

// databaseUnreachable is what an app that cannot reach its database prints:
// PDO's connection SQLSTATEs (class 08), libpq's and pgx's connection
// failures, Postgres refusing while it starts or stops, and a refused
// connection to Postgres' port from Go or Node. Narrower than any line that
// says "database", since the referral is to a check of whether there is a
// primary, and an app's own database error is not that.
var databaseUnreachable = regexp.MustCompile(`(?i)SQLSTATE\[08\d{3}\]|could not connect to (the )?(database|server|postgres)|connection to server .* failed|the database system is (starting up|shutting down|in recovery mode|not yet accepting connections)|failed to connect to .*(host=|postgres|database)|ping postgres|(dial tcp|connect ECONNREFUSED) \S+:5432\b`)

func noDatabase(line string, _ Served) (Referral, bool) {
	if !databaseUnreachable.MatchString(line) {
		return Referral{}, false
	}
	return Referral{
		Check:    "patroni",
		Sentence: "It cannot reach its database. Run " + progname.Name() + " doctor, whose Patroni check says whether there is a primary.",
	}, true
}

// Unreachable is the referral for a site whose ssh did not answer: doctor's
// reach check for that site says what the deployment is without while it is
// gone.
func Unreachable(site string) Referral {
	return Referral{
		Check:    "reach " + site,
		Sentence: fmt.Sprintf("Run "+progname.Name()+" doctor --site %s, which says what the deployment is without while it is gone.", site),
	}
}

// providerCode is one Cloudflare error code in an error's text, in the form
// internal/dns prints (`[9109] ...`) or the form Caddy logs from libdns
// (`Code:9109 Message:...`).
func providerCode(code string) *regexp.Regexp {
	return regexp.MustCompile(`(?:\[|Code:)` + code + `\b`)
}

var (
	ipRefused = providerCode("9109")
	lockedOut = providerCode("10502")
	// refusedFrom is the address in a 9109 message. Only an address is
	// read, so nothing else of the provider's message, which could echo a
	// credential, reaches the sentence.
	refusedFrom = regexp.MustCompile(`from location: ([0-9A-Fa-f.:]+[0-9A-Fa-f])`)
)

// tokenReferral is how each provider referral ends: doctor checks the token
// from the workstation and from each Caddy site.
func tokenReferral() string {
	return "Run " + progname.Name() + " doctor, which checks the token from every place that uses it."
}

// Provider is the referral for a DNS provider's refusal of the token, worded
// as doctor's token check words it: Cloudflare's 9109, the token refused
// from this address, and its 10502, the lockout. site is where the call came
// from, empty for the workstation. False for any other error, which keeps
// its own text.
func Provider(err error, site string) (Referral, bool) {
	if err == nil {
		return Referral{}, false
	}
	place, check := site, "dns token "+site
	if site == "" {
		place, check = "this workstation", "dns token workstation"
	}
	msg := err.Error()
	switch {
	case ipRefused.MatchString(msg):
		from := ""
		if m := refusedFrom.FindStringSubmatch(msg); m != nil {
			from = " (" + m[1] + ")"
		}
		return Referral{Check: check, Sentence: fmt.Sprintf("cloudflare refuses the token from %s%s. Add the address to the token's Client IP Address Filtering, or remove the filter. %s", place, from, tokenReferral())}, true
	case lockedOut.MatchString(msg):
		return Referral{Check: check, Sentence: "cloudflare is locking the token out. Refused calls keep the lockout going, and fixing the token's Client IP Address Filtering ends it: Caddy's next scheduled attempt then succeeds with nothing restarted. " + tokenReferral()}, true
	}
	return Referral{}, false
}

// Mentions reports whether text already refers to doctor, so a referral is
// added once however many layers an error passes through.
func Mentions(text string) bool {
	return strings.Contains(text, progname.Name()+" doctor")
}
