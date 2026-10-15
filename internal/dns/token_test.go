package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// tokenFake is the part of Cloudflare's API v4 a token check calls: the zone
// lookup and both verify endpoints. It answers as observed on staging: the
// user endpoint refuses an account owned token with 1000 although the
// token works, and Client IP Address Filtering applies to the zone lookup
// and not to verify.
type tokenFake struct {
	mu sync.Mutex
	// token is the one value the fake accepts.
	token string
	// zones maps a zone's name to its id; every zone is in account acct-1.
	zones map[string]string
	// status is the token's status at verify.
	status string
	// zoneError answers every zone lookup with this code and message.
	zoneCode    int
	zoneMessage string
	zoneHTTP    int
	// echo plants the token in every error message and in the body.
	echo bool
	// calls is every path asked for, with its query, in order.
	calls []string
}

func newTokenFake(token string) *tokenFake {
	return &tokenFake{token: token, zones: map[string]string{"example.org": "zone-1"}, status: "active"}
}

func (f *tokenFake) serve(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(server.Close)
	return server.URL + "/client/v4"
}

func (f *tokenFake) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *tokenFake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/client/v4")
	f.calls = append(f.calls, path+"?"+r.URL.RawQuery)
	reply := func(status int, result any, code int, message string) {
		body := map[string]any{"success": code == 0, "errors": []any{}, "result": result}
		if code != 0 {
			if f.echo {
				message += " " + r.Header.Get("Authorization")
			}
			body["errors"] = []any{map[string]any{"code": code, "message": message}}
		}
		if f.echo {
			body["messages"] = []any{map[string]any{"code": 1, "message": "you sent " + f.token}}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		reply(http.StatusBadRequest, nil, 6003, "Invalid request headers")
		return
	}
	account := strings.HasPrefix(f.token, "cfat_")
	switch {
	case path == "/zones":
		if f.zoneCode != 0 {
			reply(f.zoneHTTP, nil, f.zoneCode, f.zoneMessage)
			return
		}
		name := r.URL.Query().Get("name")
		result := []any{}
		if id, ok := f.zones[name]; ok {
			result = append(result, map[string]any{"id": id, "name": name, "account": map[string]string{"id": "acct-1", "name": "an account"}})
		}
		reply(http.StatusOK, result, 0, "")
	case path == "/user/tokens/verify":
		if account {
			reply(http.StatusUnauthorized, nil, 1000, "Invalid API Token")
			return
		}
		reply(http.StatusOK, map[string]string{"id": "tok-1", "status": f.status}, 0, "")
	case path == "/accounts/acct-1/tokens/verify":
		if !account {
			reply(http.StatusUnauthorized, nil, 1000, "Invalid API Token")
			return
		}
		reply(http.StatusOK, map[string]string{"id": "tok-1", "status": f.status}, 0, "")
	default:
		reply(http.StatusNotFound, nil, 7003, "Could not route to "+path)
	}
}

const (
	userToken    = "placeholder-user-token-4b1e-not-a-secret"
	accountToken = "cfat_placeholder-account-token-9d2c-not-a-secret"
)

func workstationCheck(base string) TokenCheck {
	return TokenCheck{Provider: "cloudflare", Domain: "talk.example.org", Place: WorkstationPlace, From: "this workstation", base: base}
}

// fakeHost runs the remote command the way a host would, against the fake
// API: it reads the URL the command asks for, sends the token the host's
// caddy.env holds, and prints what the command prints. It also keeps every
// command, so a test can look for the token in them.
type fakeHost struct {
	token    string
	commands []string
	// down makes every call fail as curl does when nothing answers.
	down bool
}

var curlURL = regexp.MustCompile(`'(https?://[^']+)'`)

func (h *fakeHost) Run(command string) (string, error) {
	h.commands = append(h.commands, command)
	if h.down {
		return "curl: (28) Connection timed out after 20001 milliseconds\n", fmt.Errorf("exit status 28")
	}
	kind := "user"
	switch {
	case h.token == "":
		return "none\n", nil
	case strings.HasPrefix(h.token, "cfat_"):
		kind = "account"
	}
	m := curlURL.FindStringSubmatch(command)
	if m == nil {
		return "", fmt.Errorf("no URL in the command")
	}
	req, _ := http.NewRequest(http.MethodGet, m[1], nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "curl: (7) Failed to connect\n", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// The host replaces the token in what curl printed before it leaves.
	out := strings.ReplaceAll(string(body), h.token, "[redacted]")
	return fmt.Sprintf("%s\n%s\n%d\n", kind, out, resp.StatusCode), nil
}

// Both transports are the same check, so every case runs through both.
func runBoth(t *testing.T, fake *tokenFake, fn func(t *testing.T, r TokenResult)) {
	t.Helper()
	base := fake.serve(t)
	t.Run("workstation", func(t *testing.T) {
		fake.calls = nil
		fn(t, workstationCheck(base).FromWorkstation(context.Background(), fake.token))
	})
	t.Run("site", func(t *testing.T) {
		fake.calls = nil
		check := TokenCheck{Provider: "cloudflare", Domain: "talk.example.org", Place: "vm", base: base}
		fn(t, check.OnSite(context.Background(), &fakeHost{token: fake.token}, "/srv/paisans/tok/infra/caddy/caddy.env"))
	})
}

func TestTokenCheckPasses(t *testing.T) {
	for _, token := range []string{userToken, accountToken} {
		fake := newTokenFake(token)
		runBoth(t, fake, func(t *testing.T, r TokenResult) {
			if r.Status != TokenPassed {
				t.Fatalf("want a pass, got %+v", r)
			}
			if r.Zone != "example.org" || !strings.Contains(r.Finding, "zone example.org") || !strings.Contains(r.Finding, "record edit not checked") {
				t.Errorf("the pass names the zone and what it did not check: %+v", r)
			}
			// The account endpoint for a cfat_ token, with the zone's
			// account, and the user endpoint for any other.
			wantVerify, otherVerify := "/user/tokens/verify", "/accounts/"
			if strings.HasPrefix(token, "cfat_") {
				wantVerify, otherVerify = "/accounts/acct-1/tokens/verify", "/user/"
			}
			if fake.count(wantVerify) != 1 || fake.count(otherVerify) != 0 {
				t.Errorf("verify at %s only, calls %v", wantVerify, fake.calls)
			}
			// Suffix by suffix, longest first, as FindZone asks.
			if fake.count("/zones?") != 2 {
				t.Errorf("want two zone lookups (talk.example.org, example.org), calls %v", fake.calls)
			}
		})
	}
}

func TestTokenCheckRefusals(t *testing.T) {
	cases := map[string]struct {
		setup   func(*tokenFake)
		code    int
		finding string
		address string
		// verify is whether a verify call may follow.
		verify bool
	}{
		"filtered away from this place": {
			setup: func(f *tokenFake) {
				f.zoneCode, f.zoneHTTP, f.zoneMessage = 9109, 403, "Cannot use the access token from location: 203.0.113.7"
			},
			code: 9109, finding: "cloudflare refuses the DNS token from %s (203.0.113.7)", address: "203.0.113.7",
		},
		"locked out": {
			setup: func(f *tokenFake) {
				f.zoneCode, f.zoneHTTP, f.zoneMessage = 10502, 429, "Too many authentication failures"
			},
			code: 10502, finding: "cloudflare is locking the DNS token out",
		},
		"not a token cloudflare knows": {
			setup: func(f *tokenFake) {
				f.zoneCode, f.zoneHTTP, f.zoneMessage = 1000, 401, "Invalid API Token"
			},
			code: 1000, finding: "cloudflare does not accept the DNS token",
		},
		"no zone holds the domain": {
			setup:   func(f *tokenFake) { f.zones = map[string]string{} },
			finding: "no zone that cloudflare can see holds talk.example.org",
		},
		"disabled": {
			setup:   func(f *tokenFake) { f.status = "disabled" },
			finding: "the DNS token is disabled at cloudflare", verify: true,
		},
		"expired": {
			setup:   func(f *tokenFake) { f.status = "expired" },
			finding: "the DNS token is expired at cloudflare", verify: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newTokenFake(userToken)
			tc.setup(fake)
			runBoth(t, fake, func(t *testing.T, r TokenResult) {
				if r.Status != TokenFailed || r.Unanswered {
					t.Fatalf("want a refusal, got %+v", r)
				}
				from := "this workstation"
				if r.Place == "vm" {
					from = "vm"
				}
				want := tc.finding
				if strings.Contains(want, "%s") {
					want = fmt.Sprintf(want, from)
				}
				if !strings.Contains(r.Finding, want) {
					t.Errorf("finding %q, want it to contain %q", r.Finding, want)
				}
				if r.Code != tc.code || r.Address != tc.address {
					t.Errorf("code %d address %q, want %d %q", r.Code, r.Address, tc.code, tc.address)
				}
				if r.Advice == "" {
					t.Errorf("a refusal says what to do: %+v", r)
				}
				if verified := fake.count("/user/tokens/verify") > 0; verified != tc.verify {
					t.Errorf("verify called %t, want %t: %v", verified, tc.verify, fake.calls)
				}
				// One refused call per place: the walk stops at the first.
				if tc.code != 0 && fake.count("/zones?") != 1 {
					t.Errorf("a refused lookup is not repeated: %v", fake.calls)
				}
			})
		})
	}
}

func TestTokenCheckNoAnswer(t *testing.T) {
	t.Run("workstation", func(t *testing.T) {
		check := workstationCheck("http://127.0.0.1:1/client/v4")
		r := check.FromWorkstation(context.Background(), userToken)
		if r.Status != TokenFailed || !r.Unanswered || !strings.Contains(r.Finding, "cloudflare did not answer from this workstation") {
			t.Fatalf("got %+v", r)
		}
		if strings.Contains(r.Finding+r.Advice, userToken) {
			t.Errorf("the token is in the result: %+v", r)
		}
	})
	t.Run("site", func(t *testing.T) {
		check := TokenCheck{Provider: "cloudflare", Domain: "talk.example.org", Place: "vm"}
		r := check.OnSite(context.Background(), &fakeHost{token: userToken, down: true}, "/srv/paisans/tok/infra/caddy/caddy.env")
		if r.Status != TokenFailed || !r.Unanswered || !strings.Contains(r.Finding, "cloudflare did not answer from vm") || !strings.Contains(r.Finding, "Connection timed out") {
			t.Fatalf("got %+v", r)
		}
	})
}

func TestTokenCheckNoCheckForProvider(t *testing.T) {
	check := TokenCheck{Provider: "desec", Domain: "example.org", Place: WorkstationPlace}
	r := check.FromWorkstation(context.Background(), userToken)
	if r.Status != TokenNoCheck || !strings.Contains(r.Finding, "no check for desec's tokens") {
		t.Fatalf("got %+v", r)
	}
	host := &fakeHost{token: userToken}
	r = check.OnSite(context.Background(), host, "/x")
	if r.Status != TokenNoCheck || len(host.commands) != 0 {
		t.Fatalf("a provider with no check runs nothing: %+v, %v", r, host.commands)
	}
	if TokenNoCheck.String() == TokenPassed.String() {
		t.Error("no check reads as a pass")
	}
}

func TestTokenCheckSiteWithoutToken(t *testing.T) {
	check := TokenCheck{Provider: "cloudflare", Domain: "example.org", Place: "vm"}
	r := check.OnSite(context.Background(), &fakeHost{}, "/srv/paisans/tok/infra/caddy/caddy.env")
	if r.Status != TokenFailed || !strings.Contains(r.Finding, "ACME_DNS_TOKEN") {
		t.Fatalf("got %+v", r)
	}
}

// The remote command reads the token on the host. The workstation never
// holds it, so it cannot be in the command; and the Authorization header is
// only ever built by the host's printf, piped into curl's configuration on
// stdin, never in curl's arguments.
func TestTokenCheckRemoteCommand(t *testing.T) {
	fake := newTokenFake(accountToken)
	base := fake.serve(t)
	host := &fakeHost{token: accountToken}
	check := TokenCheck{Provider: "cloudflare", Domain: "talk.example.org", Place: "vm", base: base}
	if r := check.OnSite(context.Background(), host, "/srv/paisans/tok/infra/caddy/caddy.env"); r.Status != TokenPassed {
		t.Fatalf("got %+v", r)
	}
	if len(host.commands) != 3 {
		t.Fatalf("want two lookups and a verify, got %d commands", len(host.commands))
	}
	for _, c := range host.commands {
		if strings.Contains(c, accountToken) || strings.Contains(c, "cfat_placeholder") {
			t.Errorf("the token is in the command: %s", c)
		}
		if !strings.Contains(c, "s/^ACME_DNS_TOKEN=//p' '/srv/paisans/tok/infra/caddy/caddy.env'") {
			t.Errorf("the token is not read from caddy.env: %s", c)
		}
		header := strings.Index(c, "Authorization")
		if header < 0 || strings.Count(c, "Authorization") != 1 {
			t.Fatalf("one Authorization header, built on the host: %s", c)
		}
		rest := c[header:]
		pipe := strings.Index(rest, "| curl ")
		if pipe < 0 || !strings.Contains(rest[pipe:], " -K - ") {
			t.Errorf("the header goes to curl only through its configuration on stdin: %s", c)
		}
		if strings.Contains(rest[pipe:], "Authorization") || strings.Contains(rest[pipe:], " -H ") {
			t.Errorf("curl's arguments carry a header: %s", c)
		}
	}
}

// The token, planted in the provider's error message and its body, reaches
// no finding or advice, from either place.
func TestTokenCheckNoTokenInOutput(t *testing.T) {
	for _, code := range []int{9109, 10502, 1000, 7003} {
		fake := newTokenFake(userToken)
		fake.echo = true
		fake.zoneCode, fake.zoneHTTP, fake.zoneMessage = code, 403, "Cannot use the access token from location: 203.0.113.7"
		runBoth(t, fake, func(t *testing.T, r TokenResult) {
			if r.Status != TokenFailed {
				t.Fatalf("got %+v", r)
			}
			if text := fmt.Sprintf("%+v", r); strings.Contains(text, userToken) {
				t.Errorf("code %d: the token is in the result: %s", code, text)
			}
		})
	}
	// A status the provider made up out of the token is not repeated.
	fake := newTokenFake(userToken)
	fake.status = userToken
	runBoth(t, fake, func(t *testing.T, r TokenResult) {
		if r.Status != TokenFailed || strings.Contains(fmt.Sprintf("%+v", r), userToken) {
			t.Errorf("got %+v", r)
		}
	})
}

// The remote command, run by a real sh against a curl that echoes the
// configuration it was given on stdin: the token is replaced before
// anything is printed, the kind and status lines are where get reads them,
// and curl's exit status is the command's.
func TestRemoteGetInAShell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	curl := "#!/bin/sh\ncfg=$(cat)\nprintf 'got %s and again %s\\n403' \"$cfg\" \"$cfg\"\nexit ${CURL_EXIT:-0}\n"
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(curl), 0o755); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(dir, "caddy.env")
	run := func(contents, exit string) (string, error) {
		if err := os.WriteFile(env, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", "-c", remoteGet(env, "https://api.example.org/zones?name=example.org"))
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "CURL_EXIT="+exit)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run("# rendered\nACME_DNS_TOKEN="+accountToken+"\n", "0")
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if strings.Contains(out, accountToken) || strings.Count(out, "[redacted]") != 2 {
		t.Errorf("the token is not replaced in what curl printed: %q", out)
	}
	if !strings.HasPrefix(out, "account\n") || !strings.HasSuffix(out, "\n403\n") || !strings.Contains(out, `header = "Authorization: Bearer [redacted]"`) {
		t.Errorf("unexpected shape: %q", out)
	}

	out, err = run("ACME_DNS_TOKEN="+userToken+"\n", "28")
	if err == nil || !strings.HasPrefix(out, "user\n") {
		t.Errorf("curl's failure is the command's: %v %q", err, out)
	}

	if out, err = run("OTHER=1\n", "0"); err != nil || out != "none\n" {
		t.Errorf("a file without the token: %v %q", err, out)
	}
	os.Remove(env)
	cmd := exec.Command("sh", "-c", remoteGet(env, "https://api.example.org/"))
	if got, err := cmd.CombinedOutput(); err != nil || string(got) != "none\n" {
		t.Errorf("a missing file: %v %q", err, got)
	}
}
