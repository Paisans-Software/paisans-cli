package dns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/progname"
)

// The DNS token check asks the provider, from one place, whether it accepts
// the token from there. The provider judges the address a call comes from
// (Cloudflare's Client IP Address Filtering), so a check from the workstation
// says nothing about a site and the other way round, and the same check runs
// from each: FromWorkstation with the token from the secrets file, and OnSite
// on the host, with the token Caddy holds there.
//
// It makes two calls, the second only when the first passed:
//
//  1. The zone lookup dns init makes, GET /zones?name=<name>, suffix by
//     suffix as FindZone asks, until the zone holding the domain is found.
//     Client IP Address Filtering applies to it, so it is the call that
//     catches a token filtered away from this place, and it proves the token
//     can read the zone, which DNS-01 needs.
//  2. Verify, for the token's status. Cloudflare does not apply the address
//     filter to verify, which is why it comes second and never alone.
//     https://developers.cloudflare.com/fundamentals/api/how-to/restrict-tokens/
//
// A refused call is what feeds Cloudflare's lockout, so a refused lookup is
// not followed by anything.

// TokenStatus is how a DNS token check ended.
type TokenStatus int

const (
	// TokenPassed is a token the provider accepted from the place, and
	// reports active.
	TokenPassed TokenStatus = iota
	// TokenFailed is a token the provider refused, or a provider that did
	// not answer (see TokenResult.Unanswered).
	TokenFailed
	// TokenNoCheck is a provider Paisans has no token check for. It is
	// neither a pass nor a failure, and is reported as skipped.
	TokenNoCheck
)

func (s TokenStatus) String() string {
	switch s {
	case TokenPassed:
		return "pass"
	case TokenFailed:
		return "fail"
	default:
		return "no check"
	}
}

// WorkstationPlace is TokenCheck.Place for the operator's own machine.
const WorkstationPlace = "workstation"

// TokenResult is what one place's check found. Finding and Advice never
// carry the token: only the provider's error codes and messages, a status
// word and the zone's name are read from a response, and a message is
// stripped of the token before it is kept.
type TokenResult struct {
	Status TokenStatus
	// Place is TokenCheck.Place: WorkstationPlace or a site's name.
	Place string
	// Finding is one line saying what was found.
	Finding string
	// Advice is what to do about it, empty on a pass.
	Advice string
	// Code is the provider's error code, when it gave one: Eg: 9109 for a
	// token filtered away from this place, 10502 for its lockout.
	Code int
	// Address is the address the provider judged, from a 9109 message.
	Address string
	// Zone is the zone holding the domain, once the lookup found it.
	Zone string
	// Unanswered is a failure in which the provider never judged the token:
	// no answer, or an answer that is not its API's.
	Unanswered bool
}

// Passed reports whether the check passed.
func (r TokenResult) Passed() bool { return r.Status == TokenPassed }

// TokenCheck is the check from one place.
type TokenCheck struct {
	// Provider is acme.provider.
	Provider string
	// Domain is community.domain; the check finds the zone holding it.
	Domain string
	// Place names the result: WorkstationPlace or the site's name.
	Place string
	// From is how a finding names the place (Eg: "this workstation", "this
	// server"). Place when empty.
	From string
	// base is the API root. A field only so that tests can point it at an
	// httptest server, as cloudflare.base is.
	base string
}

// Runner is how OnSite reaches a host. apply.Transport is one.
type Runner interface {
	Run(command string) (string, error)
}

// CaddyEnvFile is where a site's Caddy reads its DNS token, ACME_DNS_TOKEN,
// as render writes it.
func CaddyEnvFile(d deployment.Deployment) string {
	return d.Path("infra", "caddy", "caddy.env")
}

// tokenChecks is every provider a token check exists for.
var tokenChecks = map[string]bool{"cloudflare": true}

// FromWorkstation checks token from this machine, over HTTPS.
func (c TokenCheck) FromWorkstation(ctx context.Context, token string) TokenResult {
	if r, ok := c.noCheck(); ok {
		return r
	}
	if strings.TrimSpace(token) == "" {
		return c.fail(TokenResult{
			Finding: "the secrets hold no DNS token",
			Advice:  "Set external.acme_dns_token to the token " + c.Provider + " issued for the zone.",
		})
	}
	return c.run(ctx, &httpCaller{token: token, base: c.apiBase(), client: &http.Client{Timeout: 30 * time.Second}})
}

// OnSite checks the token in envFile on the host host reaches, from that
// host. The token is read there and never leaves it: it is in no command
// line, and the workstation never holds it.
func (c TokenCheck) OnSite(ctx context.Context, host Runner, envFile string) TokenResult {
	if r, ok := c.noCheck(); ok {
		return r
	}
	return c.run(ctx, &remoteCaller{host: host, envFile: envFile, base: c.apiBase()})
}

func (c TokenCheck) apiBase() string {
	if c.base != "" {
		return c.base
	}
	return "https://api.cloudflare.com/client/v4"
}

func (c TokenCheck) from() string {
	if c.From != "" {
		return c.From
	}
	return c.Place
}

func (c TokenCheck) noCheck() (TokenResult, bool) {
	if tokenChecks[c.Provider] {
		return TokenResult{}, false
	}
	return TokenResult{
		Status:  TokenNoCheck,
		Place:   c.Place,
		Finding: "paisans has no check for " + c.Provider + "'s tokens",
		Advice:  "Caddy's log is what says whether it gets certificates with it.",
	}, true
}

func (c TokenCheck) fail(r TokenResult) TokenResult {
	r.Status, r.Place = TokenFailed, c.Place
	return r
}

// errNoToken is a host whose caddy.env holds no token.
var errNoToken = errors.New("no token")

// errLineBreak is a host whose caddy.env holds a token with a newline or a
// carriage return in it, which the check refuses to send (remoteGet).
var errLineBreak = errors.New("the token holds a line break")

// unreadableEnv is a host whose caddy.env could not be read at all, with
// why. It is not errNoToken: caddy.env is root's and 0600, so a check that
// runs without root sees a file it cannot read, which says nothing about
// the token in it.
type unreadableEnv struct{ reason string }

func (e unreadableEnv) Error() string { return e.reason }

// tokenCaller makes one GET at the provider's API from one place.
type tokenCaller interface {
	// get returns the HTTP status and body, and an error only when there
	// was no answer.
	get(ctx context.Context, path string, query url.Values) (status int, body []byte, err error)
	// accountOwned reports whether the token is one an account owns
	// (cfat_), as the last get found.
	accountOwned() bool
	// redact strips the token from text, as far as this place can.
	redact(text string) string
}

func (c TokenCheck) run(ctx context.Context, caller tokenCaller) TokenResult {
	lookup := &zoneLookup{caller: caller, check: c, accounts: map[string]string{}}
	z, err := FindZone(ctx, lookup, c.Domain, nil)
	if lookup.failed != nil {
		return *lookup.failed
	}
	if err != nil {
		// FindZone's error, split into its first sentence and the rest.
		text := caller.redact(strings.TrimPrefix(err.Error(), "dns: "))
		finding, advice, _ := strings.Cut(text, ". ")
		return c.fail(TokenResult{Finding: finding, Advice: advice})
	}

	// An account owned token is verified at its account, with the account
	// the zone lookup returned; the user endpoint answers it 1000 Invalid
	// API Token although the token works (observed on staging).
	// https://developers.cloudflare.com/api/resources/user/subresources/tokens/methods/verify/
	path := "/user/tokens/verify"
	if caller.accountOwned() {
		account := lookup.accounts[z.id]
		if account == "" {
			return c.fail(TokenResult{
				Zone:    z.name,
				Finding: "cloudflare named no account for zone " + z.name,
				Advice:  "An account token is verified at its account, and the zone lookup is what names it.",
			})
		}
		path = "/accounts/" + url.PathEscape(account) + "/tokens/verify"
	}
	status, body, err := caller.get(ctx, path, nil)
	if err != nil {
		return c.unanswered(caller, err)
	}
	var verified struct {
		Status string `json:"status"`
	}
	if r, ok := c.judge(caller, status, body, &verified); !ok {
		r.Zone = z.name
		return r
	}
	if verified.Status != "active" {
		word := "not active"
		if verified.Status == "disabled" || verified.Status == "expired" {
			word = verified.Status
		}
		return c.fail(TokenResult{
			Zone:    z.name,
			Finding: "the DNS token is " + word + " at cloudflare",
			Advice:  "Cloudflare accepts only an active token. Enable it, or issue a new one and put it in external.acme_dns_token, then apply again.",
		})
	}
	return TokenResult{
		Status:  TokenPassed,
		Place:   c.Place,
		Zone:    z.name,
		Finding: "cloudflare accepts the DNS token from " + c.from() + "; zone " + z.name + "; record edit not checked",
	}
}

// zoneLookup is FindZone's Provider for the check: only LookupZone is ever
// called. It keeps the first failure as a finding, so the check reports
// what the provider said rather than FindZone's wrapping of it, and each
// zone's account, which an account token is verified at.
type zoneLookup struct {
	caller   tokenCaller
	check    TokenCheck
	accounts map[string]string
	failed   *TokenResult
}

func (l *zoneLookup) Name() string { return "cloudflare" }

func (l *zoneLookup) LookupZone(ctx context.Context, name string) (string, bool, error) {
	status, body, err := l.caller.get(ctx, "/zones", url.Values{"name": {name}, "per_page": {"50"}})
	if err != nil {
		r := l.check.unanswered(l.caller, err)
		l.failed = &r
		return "", false, errors.New(r.Finding)
	}
	var zones []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Account struct {
			ID string `json:"id"`
		} `json:"account"`
	}
	if r, ok := l.check.judge(l.caller, status, body, &zones); !ok {
		l.failed = &r
		return "", false, errors.New(r.Finding)
	}
	for _, z := range zones {
		if strings.EqualFold(z.Name, name) {
			l.accounts[z.ID] = z.Account.ID
			return z.ID, true, nil
		}
	}
	return "", false, nil
}

func (l *zoneLookup) Records(context.Context, string, string) ([]Record, error) {
	return nil, errors.New("dns: the token check reads no records")
}
func (l *zoneLookup) Create(context.Context, string, Record) error {
	return errors.New("dns: the token check writes nothing")
}
func (l *zoneLookup) Update(context.Context, string, Record) error {
	return errors.New("dns: the token check writes nothing")
}
func (l *zoneLookup) AllRecords(context.Context, string) ([]Record, error) {
	return nil, errors.New("dns: the token check reads no records")
}
func (l *zoneLookup) Delete(context.Context, string, string) error {
	return errors.New("dns: the token check writes nothing")
}

func (c TokenCheck) unanswered(caller tokenCaller, err error) TokenResult {
	if errors.Is(err, errNoToken) {
		return c.fail(TokenResult{
			Finding: "caddy.env on " + c.from() + " holds no ACME_DNS_TOKEN",
			Advice:  "apply renders it from external.acme_dns_token. Set that in the secrets and apply again.",
		})
	}
	if errors.Is(err, errLineBreak) {
		return c.fail(TokenResult{
			Finding: "caddy.env on " + c.from() + " holds an ACME_DNS_TOKEN with a line break in it",
			Advice:  "Nothing was sent to the provider. No token holds one, and curl would read the rest of it as another option. Set the token again with `" + progname.Name() + " secrets set external.acme_dns_token` and apply.",
		})
	}
	var unreadable unreadableEnv
	if errors.As(err, &unreadable) {
		return c.fail(TokenResult{
			Unanswered: true,
			Finding:    "cannot read caddy.env on " + c.from() + ": " + unreadable.reason,
			Advice:     "Nothing judged the token. caddy.env is root's, so the check reads it through sudo; check that the ssh user has sudo there, and that apply has rendered the file.",
		})
	}
	return c.fail(TokenResult{
		Unanswered: true,
		Finding:    "cloudflare did not answer from " + c.from() + ": " + caller.redact(err.Error()),
		Advice:     "Nothing judged the token. Check that " + c.from() + " reaches api.cloudflare.com over HTTPS.",
	})
}

// FilterAdvice is what to do about Cloudflare's 9109, a token refused from
// the address a call came from. Doctor gives the same advice for the 9109
// it reads in Caddy's log.
const FilterAdvice = "Add the address to the token's Client IP Address Filtering, or remove the filter. " +
	"It is the address in Cloudflare's message, because that is what Cloudflare judged, and it can differ from the site's public_address behind NAT."

// LockoutAdvice is what to do about Cloudflare's 10502, its lockout, which
// it holds while calls with a refused token keep arriving (observed on
// staging): fixing the token ends it.
const LockoutAdvice = "It does so while calls with a refused token keep arriving, as Caddy's retries do. " +
	"Fix what it refuses the token for, usually its Client IP Address Filtering; after that Caddy's next scheduled attempt succeeds, with nothing restarted."

// location is the address in a 9109 message: "Cannot use the access token
// from location: 203.0.113.7".
var location = regexp.MustCompile(`location: ?([0-9A-Fa-f.:]+)`)

// judge reads an answer's envelope: success, errors[].code and
// errors[].message, and the result into result. Nothing else of the body is
// read, so a provider that echoed a credential elsewhere could not put it
// on the terminal. ok is false with the failure when the call did not
// succeed.
func (c TokenCheck) judge(caller tokenCaller, status int, body []byte, result any) (TokenResult, bool) {
	var envelope struct {
		Success bool `json:"success"`
		Errors  []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return c.fail(TokenResult{
			Unanswered: true,
			Finding:    fmt.Sprintf("cloudflare answered from %s with HTTP %d and a body that is not its API's", c.from(), status),
			Advice:     "Nothing judged the token. Something between " + c.from() + " and api.cloudflare.com answered in its place; check again once it is out of the way.",
		}), false
	}
	if envelope.Success && status/100 == 2 {
		if result != nil && len(envelope.Result) > 0 {
			if err := json.Unmarshal(envelope.Result, result); err != nil {
				return c.fail(TokenResult{
					Unanswered: true,
					Finding:    fmt.Sprintf("cloudflare answered from %s in a shape this toolkit does not read", c.from()),
					Advice:     "Nothing judged the token.",
				}), false
			}
		}
		return TokenResult{}, true
	}
	for _, e := range envelope.Errors {
		switch e.Code {
		case 9109:
			address := ""
			if m := location.FindStringSubmatch(e.Message); m != nil {
				address = m[1]
			}
			finding := "cloudflare refuses the DNS token from " + c.from()
			if address != "" {
				finding += " (" + address + ")"
			}
			return c.fail(TokenResult{
				Code:    9109,
				Address: address,
				Finding: finding,
				Advice:  FilterAdvice,
			}), false
		case 10502:
			return c.fail(TokenResult{
				Code:    10502,
				Finding: "cloudflare is locking the DNS token out",
				Advice:  LockoutAdvice,
			}), false
		case 1000:
			return c.fail(TokenResult{
				Code:    1000,
				Finding: "cloudflare does not accept the DNS token",
				Advice:  "It was deleted, rolled or mistyped. Put the current token in external.acme_dns_token and apply again.",
			}), false
		}
	}
	var reasons []string
	code := 0
	for _, e := range envelope.Errors {
		if code == 0 {
			code = e.Code
		}
		reasons = append(reasons, fmt.Sprintf("[%d] %s", e.Code, caller.redact(e.Message)))
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "no reason given")
	}
	return c.fail(TokenResult{
		Code:    code,
		Finding: fmt.Sprintf("cloudflare refuses the DNS token from %s: HTTP %d: %s", c.from(), status, strings.Join(reasons, "; ")),
		Advice:  "The token needs read access to the zone as well as permission to edit its records.",
	}), false
}

// httpCaller calls the API from this machine. The token travels only in the
// Authorization header, and every error is stripped of it.
type httpCaller struct {
	token  string
	base   string
	client *http.Client
}

func (h *httpCaller) accountOwned() bool { return strings.HasPrefix(h.token, "cfat_") }

func (h *httpCaller) redact(text string) string {
	return strings.ReplaceAll(text, h.token, "[redacted]")
}

func (h *httpCaller) get(ctx context.Context, path string, query url.Values) (int, []byte, error) {
	target := h.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, errors.New(h.redact(err.Error()))
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Accept", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, nil, errors.New(h.redact(err.Error()))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, errors.New(h.redact(err.Error()))
	}
	// The body is read only for the envelope's fields, but it is stripped
	// here too, so that no later reader of it can find the token.
	return resp.StatusCode, []byte(h.redact(string(body))), nil
}

// remoteCaller calls the API from a host, with the token its caddy.env
// holds. The command reads the token into a shell variable and hands it to
// curl in a configuration on stdin, so it is in no process's argument list:
// printf is a shell builtin, and curl reads its header from -K -, which is
// how this toolkit sends every secret to curl (garage.CurlStdin). curl
// prints no request header without -v. What curl printed is stripped of the
// token on the host before it is printed, because the workstation never
// holds the token and so could not strip it.
//
// The command prints the token's kind first (account, user, or none for a
// file without one), then the body, then the HTTP status on a line of its
// own.
type remoteCaller struct {
	host    Runner
	envFile string
	base    string
	account bool
}

func (r *remoteCaller) accountOwned() bool { return r.account }

// redact has nothing to strip on the workstation: the host already did.
func (r *remoteCaller) redact(text string) string { return text }

func (r *remoteCaller) get(_ context.Context, path string, query url.Values) (int, []byte, error) {
	target := r.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	out, err := r.host.Run(remoteGet(r.envFile, target))
	if err != nil {
		return 0, nil, errors.New(curlCause(out, err))
	}
	kind, rest, _ := strings.Cut(out, "\n")
	if reason, ok := strings.CutPrefix(kind, "unreadable "); ok {
		return 0, nil, unreadableEnv{strings.TrimSpace(strings.TrimPrefix(reason, "sed: "))}
	}
	switch strings.TrimSpace(kind) {
	case "none":
		return 0, nil, errNoToken
	case "linebreak":
		return 0, nil, errLineBreak
	case "account":
		r.account = true
	case "user":
		r.account = false
	default:
		return 0, nil, fmt.Errorf("the host printed something other than the check's answer")
	}
	rest = strings.TrimRight(rest, "\n")
	cut := strings.LastIndex(rest, "\n")
	if cut < 0 {
		return 0, nil, fmt.Errorf("the host printed no HTTP status")
	}
	status, convErr := strconv.Atoi(strings.TrimSpace(rest[cut+1:]))
	if convErr != nil {
		return 0, nil, fmt.Errorf("the host printed no HTTP status")
	}
	return status, []byte(rest[:cut]), nil
}

// remoteGet is the host's command for one GET. A literal string is
// replaced by a POSIX loop rather than sed, because sed would need the
// token in its arguments. A file sed cannot read, missing or not readable
// by the user the command runs as, prints "unreadable" and sed's error
// first, so that it is told apart from a file that holds no token. A token
// holding a newline or a carriage return prints "linebreak" and sends
// nothing: curl reads its configuration a line at a time, so the rest of
// such a token would be read as another option of a curl run through sudo.
func remoteGet(envFile, target string) string {
	return "f=" + quote(envFile) + "; " +
		`e=$(sed -n 1p "$f" 2>&1 >/dev/null); if [ $? -ne 0 ]; then printf 'unreadable %s\n' "$e" | head -n 1; exit 0; fi; ` +
		envTokenRead +
		`if [ -z "$t" ]; then echo none; exit 0; fi; ` +
		`cr=$(printf '\r'); case $t in *"$cr"*|*'
'*) echo linebreak; exit 0;; esac; ` +
		`case $t in cfat_*) echo account;; *) echo user;; esac; ` +
		curlQuoted +
		`o=$(printf 'header = "Authorization: Bearer %s"\n' "$q" | curl -sS --max-time 20 -w '\n%{http_code}' -K - ` + quote(target) + `); s=$?; ` +
		`r=; while :; do case $o in *"$t"*) r=$r${o%%"$t"*}'[redacted]'; o=${o#*"$t"};; *) break;; esac; done; ` +
		`printf '%s\n' "$r$o"; exit $s`
}

// envTokenRead is the host's commands that set t to ACME_DNS_TOKEN's value
// in the file "$f" names, as docker compose hands it to Caddy. Render writes
// it with configmerge.EnvValue: bare, or double quoted with backslash,
// double quote, dollar and newline escaped, and this undoes that quoting
// exactly, in the shell, one character at a time. Decoding it there rather
// than on the workstation keeps the token on the host and out of every
// argument list: [ is a shell builtin, as printf is.
const envTokenRead = `t=$(sed -n 's/^ACME_DNS_TOKEN=//p' "$f"); ` +
	`case $t in \"*\") t=${t#\"}; t=${t%\"}; u=; ` +
	`while [ -n "$t" ]; do r=${t#?}; c=${t%"$r"}; t=$r; ` +
	`if [ "$c" = '\' ]; then r=${t#?}; c=${t%"$r"}; t=$r; if [ "$c" = n ]; then c='
'; fi; fi; u=$u$c; done; t=$u;; esac; `

// curlQuoted is the host's commands that set q to the token in t, escaped
// for a double quoted string in curl's configuration, where a backslash
// starts an escape and a double quote ends the string: each is written
// behind a backslash, so curl sends the token as it was issued. It walks the
// token one character at a time, as envTokenRead does, so the token is in no
// argument list.
const curlQuoted = `q=; r0=$t; while [ -n "$r0" ]; do r=${r0#?}; c=${r0%"$r"}; r0=$r; ` +
	`case $c in \\|\") q=$q\\$c;; *) q=$q$c;; esac; done; `

// curlCause is the line of a failed command that says why: curl's own
// error when it printed one, the transport's error otherwise.
func curlCause(out string, err error) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "curl: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "curl: "))
		}
	}
	text := err.Error()
	if first, _, ok := strings.Cut(text, "\n"); ok {
		text = first
	}
	return text
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
