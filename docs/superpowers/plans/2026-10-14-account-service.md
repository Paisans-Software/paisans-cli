# The Account Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the invite service and the toolkit's `invite` blueprint into the account service and the `account` blueprint: one app on `account.<domain>`, with a second hostname `invite.<domain>` that exists only when `settings.invites` is true.

**Architecture:** Three repositories, one pull request each. The service learns two public URLs and registers each route on its own host with Go's `ServeMux` host patterns. The toolkit renames the reserved blueprint, adds an `invites` setting with its `validate` rules, adds an `invite` hostname role behind one resolver every hostname consumer reads, and drops the identity-client condition from the `pocket-id` snippet. The core catalog swaps its `foundation.yaml` key.

**Tech Stack:** Go 1.25 (`net/http` `ServeMux` host patterns), Go templates, YAML, golden-file tests, Postgres-backed service tests.

**Spec:** `docs/specs/2026-10-14-account-service.md` in `paisans-stack`. Read it first; this plan argues from it.

## Global Constraints

- Names, verbatim from the spec: repository `Paisans-Software/paisans-account-service`; module `github.com/paisans-software/paisans-account-service`; command `cmd/paisans-account-service`; image `ghcr.io/paisans-software/paisans-account-service`; reserved blueprint `account`; `foundation.yaml` key `account`; code of conduct in the container `/etc/paisans-account-service/code-of-conduct.md`; environment prefix `ACCOUNT_`; first release `v0.2.0`.
- Every `INVITE_*` variable is renamed `ACCOUNT_*`. `INVITE_QUOTA` → `ACCOUNT_INVITE_QUOTA`, `INVITE_LIFETIME_DAYS` → `ACCOUNT_INVITE_LIFETIME_DAYS`; every other one keeps its suffix (`INVITE_SMTP_HOST` → `ACCOUNT_SMTP_HOST`). New: `ACCOUNT_PUBLIC_URL` (always), `ACCOUNT_INVITES` (`true`/`false`, always), `ACCOUNT_INVITE_PUBLIC_URL` (required with invites on, refused with them off). Neither URL may end in `/`.
- `settings.invites` is required, never defaulted.
- The `invite` hostname exists only with `invites: true`; derived `invite.<domain>` unless `hostnames.invite` is declared.
- No alias for any old name, no conversion code: deployments are redeployed.
- Say "blueprint", never "kind", in everything written. Docs describe current state only: no "no longer", no "previously", no rejected alternatives outside `docs/decisions.md`. No reference to Outline or tickets in commits or PRs.
- Never `git stash`. Never push to `main` or `develop`. Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; PR bodies end with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`. Never post PR comments or reviews.
- Every suite passes in a **fresh clone** of the branch before merge, not only in the worktree.
- ⚠ steps (repository rename, tags, releases, package deletion) are not in this plan's tasks; the controller confirms each with the founder at the time (Task 12).

## Review Focus

1. A request whose `Host` carries a port (`account.example.org:443`) or different case (`Account.Example.org`) must route as the bare lowercase host does — pinned in Task 4.
2. An operator who declares `hostnames.invite` equal to the primary hostname, or whose derived `invite.<domain>` equals another app's hostname, must be refused by `validate`, not rendered as two Caddy blocks for one name — pinned in Task 9.
3. A deployment that turns `invites` off after invites were issued: old invite links answer 404 on every host, the home page and `/admin` render without error, the sweeper keeps running — pinned in Task 4.
4. The service started with `ACCOUNT_PUBLIC_URL` and `ACCOUNT_INVITE_PUBLIC_URL` on the same host must refuse to start, since every route would collide — pinned in Task 2.
5. `dns init` and the gateway with invites off must not publish `invite.<domain>` even when an operator left a stale `hostnames.invite` (validate refuses it; the resolver must still return nothing if validate is bypassed) — pinned in Task 9.

---

## Part A — the service (`paisans-invite`, renamed later)

**Where:** `src/paisans-invite`, worktree `.worktrees/account-service` on branch `feat/account-service` from `origin/develop`. One PR into `develop`. Its `changelog.yml` requires the `[Unreleased]` section of `CHANGELOG.md` to change; bullets at most 35 words.

Tests that need Postgres read `ACCOUNT_TEST_DATABASE_URL` (after Task 1); run a throwaway Postgres for them:

```bash
docker run -d --rm --name acct-pg -e POSTGRES_PASSWORD=test -e POSTGRES_DB=account -p 55432:5432 postgres:17
export ACCOUNT_TEST_DATABASE_URL='postgres://postgres:test@localhost:55432/account?sslmode=disable'
```

### Task 1: Rename module, command, binary and image

**Files:**
- Modify: `go.mod:1`, every `.go` import of `github.com/paisans-software/paisans-invite`
- Move: `cmd/paisans-invite/` → `cmd/paisans-account-service/`
- Modify: `Dockerfile:6,9,12,13`, `.gitignore:2`, `.github/workflows/release.yml:1,28`, `.github/workflows/test.yml` (DB name, `INVITE_TEST_DATABASE_URL`), `internal/store/storetest.go:2,15,20,22`, `README.md` title and prose

**Interfaces:**
- Produces: module path `github.com/paisans-software/paisans-account-service`; binary `/paisans-account-service`; test DB variable `ACCOUNT_TEST_DATABASE_URL`.

- [ ] **Step 1: Rewrite the module path and move the command**

```bash
go mod edit -module github.com/paisans-software/paisans-account-service
grep -rl 'github.com/paisans-software/paisans-invite' --include='*.go' . | xargs sed -i '' 's#github.com/paisans-software/paisans-invite#github.com/paisans-software/paisans-account-service#g'
git mv cmd/paisans-invite cmd/paisans-account-service
```

- [ ] **Step 2: Rename the binary, image and test DB**

`Dockerfile`: `-o /paisans-account-service ./cmd/paisans-account-service`, `COPY --from=build /paisans-account-service /paisans-account-service`, `CMD ["/paisans-account-service", "healthcheck"]`, `ENTRYPOINT ["/paisans-account-service"]`. `.gitignore`: `/paisans-account-service`. `release.yml`: `images: ghcr.io/paisans-software/paisans-account-service` and the header comment naming that image and "the toolkit's `account` blueprint". `test.yml`: DB `account`, `ACCOUNT_TEST_DATABASE_URL: postgres://postgres:test@localhost:5432/account?sslmode=disable`. `storetest.go`: read `ACCOUNT_TEST_DATABASE_URL`.

- [ ] **Step 3: Build and test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS (DB tests run with `ACCOUNT_TEST_DATABASE_URL` exported).

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "Rename the module, command and image to paisans-account-service

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 2: Configuration — the ACCOUNT_ prefix and the two public URLs

**Files:**
- Modify: `internal/config/config.go:1-2,23-40,84-120`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces on `config.Config`: `AccountURL string` (from `ACCOUNT_PUBLIC_URL`), `Invites bool` (from `ACCOUNT_INVITES`, required, `true`/`false`), `InviteURL string` (from `ACCOUNT_INVITE_PUBLIC_URL`, empty when invites off). `PublicURL` is removed. Every other field keeps its name; its variable is `ACCOUNT_` + the old suffix, except `ACCOUNT_INVITE_QUOTA` and `ACCOUNT_INVITE_LIFETIME_DAYS`. Default `CoCPath` is `/etc/paisans-account-service/code-of-conduct.md`.
- Produces: `func (c Config) AccountHost() string` and `func (c Config) InviteHost() string` — the lowercase host of each URL without port, `""` for an empty URL.

- [ ] **Step 1: Write the failing tests**

In `config_test.go`, change the base environment helper to `ACCOUNT_*` names (with `ACCOUNT_PUBLIC_URL=https://account.example.org`, `ACCOUNT_INVITES=true`, `ACCOUNT_INVITE_PUBLIC_URL=https://invite.example.org`) and add:

```go
func TestInvitesURLs(t *testing.T) {
	cases := []struct {
		name    string
		set     map[string]string
		wantErr string
	}{
		{"invites on, both urls", nil, ""},
		{"invites unset", map[string]string{"ACCOUNT_INVITES": ""}, "ACCOUNT_INVITES"},
		{"invites not a bool", map[string]string{"ACCOUNT_INVITES": "yes"}, "ACCOUNT_INVITES"},
		{"invites on, no invite url", map[string]string{"ACCOUNT_INVITE_PUBLIC_URL": ""}, "ACCOUNT_INVITE_PUBLIC_URL"},
		{"invites off, invite url set", map[string]string{"ACCOUNT_INVITES": "false"}, "ACCOUNT_INVITE_PUBLIC_URL"},
		{"invites off, no invite url", map[string]string{"ACCOUNT_INVITES": "false", "ACCOUNT_INVITE_PUBLIC_URL": ""}, ""},
		{"invite url trailing slash", map[string]string{"ACCOUNT_INVITE_PUBLIC_URL": "https://invite.example.org/"}, "ACCOUNT_INVITE_PUBLIC_URL must not end in /"},
		{"account url trailing slash", map[string]string{"ACCOUNT_PUBLIC_URL": "https://account.example.org/"}, "ACCOUNT_PUBLIC_URL must not end in /"},
		{"same host", map[string]string{"ACCOUNT_INVITE_PUBLIC_URL": "https://ACCOUNT.example.org:443"}, "must be different hosts"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := config.Load(envWith(c.set))
			if c.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestHosts(t *testing.T) {
	cfg, err := config.Load(envWith(map[string]string{"ACCOUNT_PUBLIC_URL": "https://Account.Example.org:8443"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccountHost() != "account.example.org" || cfg.InviteHost() != "invite.example.org" {
		t.Fatalf("hosts: %q %q", cfg.AccountHost(), cfg.InviteHost())
	}
}
```

`envWith(set)` returns a `getenv` over the base map with `set` applied, an empty value meaning unset. Rename every existing `INVITE_` key in the file's other tests.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/config/ -run 'TestInvitesURLs|TestHosts' -v`
Expected: FAIL (`Invites`, `AccountHost` undefined).

- [ ] **Step 3: Implement**

In `Load`: replace every `"INVITE_` with `"ACCOUNT_`, then rename `ACCOUNT_QUOTA` → `ACCOUNT_INVITE_QUOTA` and `ACCOUNT_LIFETIME_DAYS` → `ACCOUNT_INVITE_LIFETIME_DAYS`. Replace `PublicURL: req("INVITE_PUBLIC_URL")` with `AccountURL: req("ACCOUNT_PUBLIC_URL")`. Add a required boolean:

```go
	invites := getenv("ACCOUNT_INVITES")
	switch invites {
	case "true", "false":
	default:
		errs = append(errs, errors.New("ACCOUNT_INVITES must be true or false"))
	}
	c.Invites = invites == "true"
	c.InviteURL = getenv("ACCOUNT_INVITE_PUBLIC_URL")
	switch {
	case c.Invites && c.InviteURL == "":
		errs = append(errs, errors.New("ACCOUNT_INVITE_PUBLIC_URL is required when ACCOUNT_INVITES is true"))
	case !c.Invites && c.InviteURL != "":
		errs = append(errs, errors.New("ACCOUNT_INVITE_PUBLIC_URL must be unset when ACCOUNT_INVITES is false"))
	}
	for name, u := range map[string]string{"ACCOUNT_PUBLIC_URL": c.AccountURL, "ACCOUNT_INVITE_PUBLIC_URL": c.InviteURL} {
		if strings.HasSuffix(u, "/") {
			errs = append(errs, fmt.Errorf("%s must not end in /", name))
		}
	}
	if c.InviteURL != "" && c.AccountHost() == c.InviteHost() {
		errs = append(errs, errors.New("ACCOUNT_PUBLIC_URL and ACCOUNT_INVITE_PUBLIC_URL must be different hosts"))
	}
```

Host helpers, using `net/url`:

```go
// AccountHost is the host members and admins use, lowercase and without a port.
func (c Config) AccountHost() string { return hostOf(c.AccountURL) }

// InviteHost is the host invitees use, or "" with invites off.
func (c Config) InviteHost() string { return hostOf(c.InviteURL) }

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || raw == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
```

Package doc: "the contract with paisans-stack's `account` blueprint". Error text for SMTP TLS: `ACCOUNT_SMTP_TLS`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/config/ -v`
Expected: PASS. (Other packages fail to compile until Tasks 3–5; that is expected.)

- [ ] **Step 5: Commit**

```bash
git add internal/config && git commit -m "config: ACCOUNT_ variables, ACCOUNT_INVITES and two public URLs

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 3: Links use the right base URL

**Files:**
- Modify: `internal/flow/flow.go:42-53,135,243`
- Test: `internal/flow/flow_test.go`

**Interfaces:**
- Consumes: `config.Config.AccountURL`, `InviteURL`.
- Produces: `flow.Settings` fields `AccountURL string`, `InviteURL string` replacing `PublicURL`.

- [ ] **Step 1: Write the failing test**

In `flow_test.go`, set `AccountURL: "https://account.example.org", InviteURL: "https://invite.example.org"` in the settings helper and add:

```go
func TestLinksUseTheirOwnHost(t *testing.T) {
	h := newFlowHarness(t, true) // the existing helper, approval on
	link, _, err := h.f.CreateInvite(h.ctx, h.member)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, "https://invite.example.org/i/") {
		t.Fatalf("invite link %q", link)
	}
	h.accept(t, link) // the existing helper that submits the invitee form
	if body := h.mail.last(t); !strings.Contains(body, "https://account.example.org/admin\n") {
		t.Fatalf("admin notice %q", body)
	}
}
```

Use the helper and method names the file already has for creating an invite, accepting it and reading the last sent mail; rename the identifiers above to match them.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/flow/ -run TestLinksUseTheirOwnHost -v`
Expected: FAIL to compile (`AccountURL` unknown).

- [ ] **Step 3: Implement**

`Settings`: replace `PublicURL string` with `AccountURL, InviteURL string`. Line 135: `return s.s.InviteURL + "/i/" + tok, inv, nil`. Line 243: `...+s.s.AccountURL+"/admin\n")`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/flow/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/flow && git commit -m "flow: invite links on the invite host, admin links on the account host

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 4: Routes registered per host

**Files:**
- Modify: `internal/web/server.go:18-69`, `internal/web/auth.go:66,82,87,93`, `internal/web/session.go:68,72,83`, `internal/web/member.go:14-46`, `internal/web/templates/home.html`
- Test: `internal/web/server_test.go`, `internal/web/hosts_test.go` (create)

**Interfaces:**
- Consumes: `config.Config.AccountHost()`, `InviteHost()`, `Invites`, `AccountURL`.
- Produces: `web.Options` gains `AccountHost string`, `InviteHost string`, `Invites bool`. `NewServer(o Options) http.Handler` registers routes as below. Cookie names `account_session`, `account_login`.

- [ ] **Step 1: Write the failing tests**

`hosts_test.go`:

```go
package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRoutesByHost(t *testing.T) {
	h := newHarness(t, true)
	cases := []struct {
		host, method, path string
		want               int
	}{
		{"invite.example.org", "GET", "/i/nope", http.StatusNotFound}, // unknown token: the page's own 404, routed
		{"account.example.org", "GET", "/i/nope", http.StatusNotFound},
		{"invite.example.org", "GET", "/admin", http.StatusNotFound},
		{"invite.example.org", "GET", "/", http.StatusNotFound},
		{"invite.example.org", "GET", "/auth/login", http.StatusNotFound},
		{"account.example.org", "GET", "/", http.StatusFound}, // to /auth/login
		{"ACCOUNT.example.org:443", "GET", "/", http.StatusFound},
		{"10.0.0.5:8082", "GET", "/healthz", http.StatusOK},
		{"invite.example.org", "GET", "/static/style.css", http.StatusOK},
		{"account.example.org", "GET", "/static/style.css", http.StatusOK},
		{"elsewhere.example.org", "GET", "/", http.StatusNotFound},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "http://"+c.host+c.path, nil)
		rec := httptest.NewRecorder()
		h.srv.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s %s%s: %d, want %d", c.method, c.host, c.path, rec.Code, c.want)
		}
	}
}

func TestInviteRouteReachesHandlerOnlyOnInviteHost(t *testing.T) {
	h := newHarness(t, true)
	link := h.makeInvite(t) // https://invite.example.org/i/<token>
	for host, want := range map[string]int{"invite.example.org": http.StatusOK, "account.example.org": http.StatusNotFound} {
		req := httptest.NewRequest("GET", "http://"+host+link[len("https://invite.example.org"):], nil)
		rec := httptest.NewRecorder()
		h.srv.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", host, rec.Code, want)
		}
	}
}

func TestInvitesOff(t *testing.T) {
	h := newHarnessInvites(t, false)
	for _, p := range []string{"/i/x"} {
		for _, host := range []string{"account.example.org", "invite.example.org"} {
			rec := httptest.NewRecorder()
			h.srv.ServeHTTP(rec, httptest.NewRequest("GET", "http://"+host+p, nil))
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s%s: %d", host, p, rec.Code)
			}
		}
	}
	rec := h.as(h.memberSession, "POST", "/invites", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /invites with invites off: %d", rec.Code)
	}
	if rec := h.as(h.memberSession, "GET", "/", nil); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Invite someone") {
		t.Errorf("home with invites off: %d %q", rec.Code, rec.Body.String())
	}
	if rec := h.as(h.adminSession, "GET", "/admin", nil); rec.Code != http.StatusOK {
		t.Errorf("admin with invites off: %d", rec.Code)
	}
}
```

Adapt `newHarness` in `server_test.go`: add `newHarnessInvites(t, invites bool)`; the harness sets `AccountHost: "account.example.org"`, `InviteHost: "invite.example.org"` (empty when invites off), `Invites`, and flow settings `AccountURL`/`InviteURL`; `as(...)` builds requests against `http://account.example.org`, so existing tests keep working; the invitee helpers build requests against `http://invite.example.org`. Add `makeInvite`, `memberSession`, `adminSession` if the harness names them differently — reuse what exists. Pick a static file that exists under `internal/web/static/` for the static rows.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/web/ -run 'TestRoutesByHost|TestInviteRoute|TestInvitesOff' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

`NewServer`:

```go
func NewServer(o Options) http.Handler {
	s := &server{Options: o, limit: newLimiter()}
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	files := http.StripPrefix("/static/", http.FileServerFS(static))
	a := o.AccountHost
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.Handle("GET "+a+"/static/", files)
	mux.Handle("GET "+a+"/auth/", o.Auth)
	mux.HandleFunc("POST "+a+"/auth/logout", s.member(s.logout))
	mux.HandleFunc("GET "+a+"/{$}", s.member(s.home))
	mux.HandleFunc("GET "+a+"/admin", s.admin(s.queue))
	if o.Invites {
		i := o.InviteHost
		mux.HandleFunc("POST "+a+"/invites", s.member(s.createInvite))
		mux.HandleFunc("POST "+a+"/invites/{id}/revoke", s.member(s.revoke))
		mux.HandleFunc("POST "+a+"/admin/applications/{id}/approve", s.admin(s.approve))
		mux.HandleFunc("POST "+a+"/admin/applications/{id}/decline", s.admin(s.decline))
		mux.HandleFunc("POST "+a+"/admin/applications/{id}/dismiss", s.admin(s.dismiss))
		mux.Handle("GET "+i+"/static/", files)
		mux.HandleFunc("GET "+i+"/i/{token}", s.limited(s.invitePage))
		mux.HandleFunc("POST "+i+"/i/{token}", s.limited(s.inviteSubmit))
	}
	return securityHeaders(lowerHost(mux))
}

// lowerHost lowercases the request's host before routing, because ServeMux
// matches a pattern's host exactly and browsers and proxies may send any case.
func lowerHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = strings.ToLower(r.Host)
		next.ServeHTTP(w, r)
	})
}
```

`ServeMux` strips the port from `r.Host` before matching a host pattern; the test row `ACCOUNT.example.org:443` pins both that and the lowercasing. If the port row fails, strip the port in `lowerHost` with `net.SplitHostPort` as well.

`server.go`'s `New`: `secure: strings.HasPrefix(cfg.AccountURL, "https://")`, and pass `AccountHost: cfg.AccountHost(), InviteHost: cfg.InviteHost(), Invites: cfg.Invites` into `Options`. `auth.go:66`: `RedirectURL: cfg.AccountURL + "/auth/callback"`. Cookie names: `invite_session` → `account_session`, `invite_login` → `account_login`. No `Domain` attribute is added anywhere.

`member.go`: with `!s.Invites`, `homeView` skips `Flow.Invites` and `Flow.Quota()` and sets `homeData.Invites` to `false` (add `InvitesOn bool` to `homeData`); `queue` with invites off renders `admin.html` with an empty queue. `home.html`: wrap the "Invite someone" and "Your invites" cards and the approval link in `{{if .Data.InvitesOn}}…{{end}}` (match the template's existing data path), and with invites off show one line: `<p>Signed in as {{.Session.Username}}.</p>` (use the field the session view already exposes). Title `view{Title: "Account"}`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/web/ -v`
Expected: PASS, including every existing test.

- [ ] **Step 5: Commit**

```bash
git add internal/web && git commit -m "web: register each route on its own host; invites off removes the invite routes

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 5: Wire main, README, changelog

**Files:**
- Modify: `cmd/paisans-account-service/main.go:1,28,90-95`, `README.md`, `CHANGELOG.md`, `internal/web/render.go:38`, `internal/idp/pocketid/hostname_test.go:16` (comments)

- [ ] **Step 1: Wire settings**

`main.go`: `os.Getenv("ACCOUNT_LISTEN_ADDR")` for the healthcheck; `flow.Settings{... AccountURL: cfg.AccountURL, InviteURL: cfg.InviteURL, ...}`; doc comment names the account service. `sweep` keeps running regardless of `Invites`.

- [ ] **Step 2: README**

Title `# paisans-account-service`; prose: the community's account service, run by paisans-stack's `account` blueprint; `account.` for members and admins, `invite.` for invitees when `ACCOUNT_INVITES` is true. Environment table: every variable from `config.go` with its `ACCOUNT_` name, plus `ACCOUNT_PUBLIC_URL`, `ACCOUNT_INVITES`, `ACCOUNT_INVITE_PUBLIC_URL`. Tests section: `ACCOUNT_TEST_DATABASE_URL=postgres://… go test ./...`.

- [ ] **Step 3: CHANGELOG**

Under `## [Unreleased]`:

```markdown
### Changed

- The service is paisans-account-service: module, command, binary and image renamed, every environment variable now starts with ACCOUNT_, and the cookies are account_session and account_login.

### Added

- ACCOUNT_INVITES switches invites on or off. With it on, invitee pages answer only on ACCOUNT_INVITE_PUBLIC_URL's host and everything else only on ACCOUNT_PUBLIC_URL's host.
```

- [ ] **Step 4: Full suite**

Run: `go vet ./... && go test -race ./...`
Expected: PASS. Then `grep -rn 'INVITE_\|paisans-invite' . --exclude-dir=.git` returns only `CHANGELOG.md`'s `0.1.0` history.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "Wire the two public URLs; README and changelog for the account service

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 6: Pull request into `develop`, merge

- [ ] Fresh clone: `git clone --branch feat/account-service <origin> /tmp-scratch/acct && cd … && go vet ./... && go test -race ./...` with `ACCOUNT_TEST_DATABASE_URL` set. Expected PASS.
- [ ] `git push -u origin feat/account-service`; `gh pr create --base develop` with a body describing the change (no Outline refs) ending in the Claude Code footer.
- [ ] `gh pr checks --watch` (test, contract, changelog). Fix and push until green.
- [ ] `gh pr merge --squash` (match the repo's recent merges: `gh pr list --state merged --limit 5`). No admin bypass; if a review is required, stop and report.

---

## Part B — the toolkit (`paisans-stack`)

**Where:** worktree `.worktrees/account-blueprint`, branch `feat/account-blueprint` created from `docs/account-service-spec` (so the spec and this plan ride in the same PR). One PR into `develop`. No CHANGELOG on `develop`.

Golden tree: `go test ./internal/render -update`, then read the diff. Main suite: `go vet ./... && go test ./...`.

### Task 7: Rename the `invite` blueprint to `account`

**Files:**
- Move: `internal/blueprints/reserved/invite/` → `internal/blueprints/reserved/account/`
- Move: `internal/blueprints/invite.go` → `account.go`; `internal/render/invite.go` → `account.go`; `internal/render/invite_test.go` → `account_test.go`; `internal/validate/invite.go` → `account.go`; `internal/validate/invite_test.go` → `account_test.go`; `internal/validate/testdata/invite-*.yaml` → `account-*.yaml`
- Modify: `internal/config/config.go:72,1198`, `internal/blueprints/foundation.go:40,47`, `internal/blueprints/testdata/core/foundation.yaml:38`, `internal/render/appview.go:476-482`, `internal/secretsrotate/appsecret.go:94`, `internal/validate/validate.go:185-190`, `internal/validate/validate_test.go:33-42`, `internal/validate/testdata/valid.yaml:91-93`, `internal/render/testdata/deployment.yaml`, `internal/render/testdata/secrets.fixture.yaml`, every test listed in the investigation (`secretsrotate/appsecret_test.go`, `secretsgen/*_test.go`, `blueprints/*_test.go`, `apply/*_test.go`, `render/stacks_test.go`, `render/render_test.go:869`, `cmd/paisans/oidc_test.go:52`, `validate/pocketid_test.go:18`)

**Interfaces:**
- Produces: `config.BlueprintAccount Blueprint = "account"`; `blueprints.FoundationAccount = "account"`; `blueprints.AccountSettingRanges`, `AccountOtherSettings`, `AccountSettingDefaults`; render `accountValues` and `func (p *planner) accountValuesFor(name string) (accountValues, error)`; app view field `Account accountValues`; validate `accountApps()`; rule ids `account-must-be-clustered`, `account-identity-app`, `account-one-identity-app`, `account-signups-not-disabled`, `account-recovery-disabled`, `account-login-link-disabled`, `account-without-smtp`, `account-code-of-conduct-missing`, `account-setting-invalid`, `account-setting-unknown`. The fixture app in `valid.yaml` and `deployment.yaml` is named `account` with hostname `account.example.org`.

- [ ] **Step 1: Move and rename**

```bash
git mv internal/blueprints/reserved/invite internal/blueprints/reserved/account
git mv internal/blueprints/invite.go internal/blueprints/account.go
git mv internal/render/invite.go internal/render/account.go
git mv internal/render/invite_test.go internal/render/account_test.go
git mv internal/validate/invite.go internal/validate/account.go
git mv internal/validate/invite_test.go internal/validate/account_test.go
for f in internal/validate/testdata/invite-*.yaml; do git mv "$f" "${f/invite-/account-}"; done
```

Then rename identifiers (`BlueprintInvite`→`BlueprintAccount`, `FoundationInvite`→`FoundationAccount`, `InviteSetting*`→`AccountSetting*`, `InviteOtherSettings`→`AccountOtherSettings`, `inviteValues*`→`accountValues*`, `.Invite.`→`.Account.` in templates, `inviteApps`→`accountApps`, rule id strings `invite-`→`account-`), the blueprint's `name: account`, `foundation: "account"`, the secret `why:` texts (`ACCOUNT_EMAIL_HASH_KEY`, `ACCOUNT_SESSION_KEY`), the test foundation key `account: "ghcr.io/paisans-software/paisans-account-service:0.2.*"`, and the fixture app name. Keep the setting keys `invite_quota`, `invite_lifetime_days`, `require_approval`, `application_wait_days`, `decline_retention_days`, `login_link_days`, `code_of_conduct` unchanged.

- [ ] **Step 2: Rename the environment**

`reserved/account/.env.secret.tmpl`: every `INVITE_` → `ACCOUNT_`, then `ACCOUNT_QUOTA` → `ACCOUNT_INVITE_QUOTA`, `ACCOUNT_LIFETIME_DAYS` → `ACCOUNT_INVITE_LIFETIME_DAYS`, `ACCOUNT_PUBLIC_URL={{ .PublicURL }}`, `ACCOUNT_COC_PATH=/etc/paisans-account-service/code-of-conduct.md`. `compose.yaml.tmpl`: the mount target `/etc/paisans-account-service/code-of-conduct.md`, the comment's `ACCOUNT_LISTEN_ADDR`.

- [ ] **Step 3: Goldens and suite**

Run: `go test ./internal/render -update && go vet ./... && go test ./...`
Expected: PASS. Read `git diff --stat internal/render/testdata`: the `invite/` golden directories become `account/`, `snippets/invite.caddy` becomes `snippets/account.caddy`, `.env` lines are `ACCOUNT_*`. Then `grep -rn 'BlueprintInvite\|FoundationInvite\|INVITE_\|paisans-invite\|"invite-' internal cmd` returns nothing.

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "Rename the invite blueprint to account

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 8: The `invites` setting and its rules

**Files:**
- Modify: `internal/blueprints/account.go`, `internal/validate/account.go`, `internal/validate/validate_test.go`, `internal/render/account.go`, `internal/blueprints/reserved/account/.env.secret.tmpl`
- Create: `internal/validate/testdata/account-invites-unset.yaml`, `account-invite-settings-without-invites.yaml`, `account-invite-hostname-without-invites.yaml`, `account-serves-nothing.yaml`
- Modify: `internal/validate/testdata/valid.yaml`, `internal/render/testdata/deployment.yaml` (add `invites: true`)

**Interfaces:**
- Produces: `blueprints.AccountInvitesSetting = "invites"`; `func AccountInvites(app config.App) bool` (true only when the setting is the YAML boolean `true`); `blueprints.AccountInviteSettings = []string{"invite_quota", "require_approval", "invite_lifetime_days", "application_wait_days", "decline_retention_days", "login_link_days", "code_of_conduct"}`. Rendered `ACCOUNT_INVITES=true|false`.

- [ ] **Step 1: Write the failing tests**

Fixtures, each `valid.yaml` with one change to the `account` app:
- `account-invites-unset.yaml`: no `invites` key.
- `account-invite-settings-without-invites.yaml`: `invites: false`, keeps `invite_quota: 3`, no `code_of_conduct`.
- `account-invite-hostname-without-invites.yaml`: `invites: false`, no invite settings, `hostnames: {invite: invite.example.org}`.
- `account-serves-nothing.yaml`: `invites: false`, no invite settings.

Rows in `TestRulesFire` (`validate_test.go`), with the level constants the table already uses:

```go
{"account-invites-unset", "account-invites-unset", refuse},
{"account-invite-settings-without-invites", "account-invite-settings-without-invites", refuse},
{"account-invite-hostname-without-invites", "account-invite-hostname-without-invites", refuse},
{"account-serves-nothing", "account-serves-nothing", warn},
```

In `account_test.go`, add: with `invites: false` and the identity app's `allow_user_signups: open` and `email_one_time_access_as_unauthenticated` unset, none of `account-signups-not-disabled`, `account-recovery-disabled`, `account-code-of-conduct-missing` fires. And `invites: "yes"` (a string) is refused as `account-setting-invalid`.

In `render/account_test.go`, assert the rendered `.env` contains `ACCOUNT_INVITES=true` for the fixture and `ACCOUNT_INVITES=false` when the app's `invites` is false.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/validate/ ./internal/render/ -run 'TestRulesFire|Account' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

`validate/account.go`, inside the per-app loop, first:

```go
	raw, set := app.Settings[blueprints.AccountInvitesSetting]
	if !set {
		c.refuse("account-invites-unset", fmt.Sprintf("apps.%s.settings.invites", name),
			"invites is required: true for an invite-only community, false for one without invites. It decides whether invite.<domain> exists.")
		continue
	}
	if _, ok := raw.(bool); !ok {
		c.refuse("account-setting-invalid", fmt.Sprintf("apps.%s.settings.invites", name), "invites must be true or false")
		continue
	}
	if !blueprints.AccountInvites(app) {
		for _, k := range blueprints.AccountInviteSettings {
			if _, ok := app.Settings[k]; ok {
				c.refuse("account-invite-settings-without-invites", fmt.Sprintf("apps.%s.settings.%s", name, k), k+" applies only with invites: true")
			}
		}
		if _, ok := app.Hostnames[blueprints.InviteRole]; ok {
			c.refuse("account-invite-hostname-without-invites", fmt.Sprintf("apps.%s.hostnames.invite", name), "hostnames.invite exists only with invites: true")
		}
		c.warn("account-serves-nothing", fmt.Sprintf("apps.%s.settings.invites", name), "with invites false the account app has no feature yet")
		continue
	}
	// every existing rule below runs only with invites on
```

Use the finding helpers `validate/account.go` already calls (`c.refuse`/`c.warn` stand for whatever they are named). `InviteRole` is defined in Task 9; define it here in `blueprints/hostnames.go` as `const InviteRole = "invite"` if Task 9 has not run, and Task 9 reuses it. Add `"invites"` to `AccountOtherSettings` so `account-setting-unknown` does not fire. `.env.secret.tmpl`: `ACCOUNT_INVITES={{ if .Account.Invites }}true{{ else }}false{{ end }}`, with `Invites bool` on `accountValues` set from `blueprints.AccountInvites`. With invites off, the template omits every `ACCOUNT_INVITE_*` and approval line and the code-of-conduct file: wrap them in `{{ if .Account.Invites }}`; `code-of-conduct.md.tmpl` renders only with invites on (if the blueprint renders files unconditionally, render an empty file and keep the mount — note which in the commit).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/render -update && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "account: settings.invites is required, and the invite rules apply only with it on

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 9: The `invite` hostname role and one resolver

**Files:**
- Modify: `internal/blueprints/hostnames.go`, `internal/blueprints/blueprint.go:150`, `internal/blueprints/reserved/account/blueprint.yaml`, `internal/render/site.go:875-930`, `internal/dns/dns.go:289-326`, `internal/validate/validate.go:1280,1402-1420,1496-1535`, `internal/render/appview.go` (invite URL for the `.env`)
- Create: `internal/blueprints/reserved/account/caddy.snippet.invite.tmpl`, `internal/blueprints/hostnames_role_test.go`
- Modify: `internal/blueprints/reserved/account/caddy.snippet.tmpl`, `.env.secret.tmpl`
- Test: `internal/render/account_test.go`, `internal/dns/dns_test.go`, `internal/validate/validate_test.go`

**Interfaces:**
- Produces:

```go
// InviteRole is the hostname an account app's invitees are served on.
const InviteRole = "invite"

// InviteHostname is where an account app's invitees are served, and empty
// unless settings.invites is true. A declared hostnames.invite wins;
// otherwise invite.<domain>.
func InviteHostname(cfg *config.Config, app config.App) string

// RoleHostname is one active hostname of an app beyond its primary.
type RoleHostname struct {
	Role, Hostname string
	// Derived is true when the configuration does not name it and the
	// toolkit made it, so a message can say which key to change.
	Derived bool
}

// RoleHostnames is every hostname an app answers on beyond its primary,
// sorted by role: each declared role, the media hostname when the app keeps
// objects in storage, and the invite hostname when invites are on. A role
// whose condition does not hold is left out even when declared.
func RoleHostnames(cfg *config.Config, app config.App) []RoleHostname
```

- [ ] **Step 1: Write the failing resolver test**

`hostnames_role_test.go`, table over: an account app with invites on and nothing declared → `[{invite, invite.example.org, true}]`; invites on and `hostnames.invite: join.example.org` → `[{invite, join.example.org, false}]`; invites off with a stale `hostnames.invite` → `[]`; an app with derived media → `[{media, talk-media.example.org, true}]`; an app keeping files on disk with a declared media → `[]`; a declared `wellknown` → `[{wellknown, …, false}]`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/blueprints/ -run RoleHostnames -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement the resolver**

```go
func InviteHostname(cfg *config.Config, app config.App) string {
	if !HasHostnameRole(app.Blueprint, InviteRole) || !AccountInvites(app) {
		return ""
	}
	if declared := strings.TrimSpace(app.Hostnames[InviteRole]); declared != "" {
		return declared
	}
	if cfg.Community.Domain == "" {
		return ""
	}
	return InviteRole + "." + cfg.Community.Domain
}

func RoleHostnames(cfg *config.Config, app config.App) []RoleHostname {
	var out []RoleHostname
	for _, role := range HostnameRoles(app.Blueprint) {
		switch role {
		case PrimaryRole:
			continue
		case MediaRole:
			if h := MediaHostname(cfg, app); h != "" {
				out = append(out, RoleHostname{role, h, MediaHostnameIsDerived(cfg, app)})
			}
		case InviteRole:
			if h := InviteHostname(cfg, app); h != "" {
				out = append(out, RoleHostname{role, h, strings.TrimSpace(app.Hostnames[InviteRole]) == ""})
			}
		default:
			if h := strings.TrimSpace(app.Hostnames[role]); h != "" {
				out = append(out, RoleHostname{role, h, false})
			}
		}
	}
	return out
}
```

`HostnameRoles` is already sorted. `blueprint.go:150`: `knownRoles = map[string]bool{MediaRole: true, WellknownRole: true, InviteRole: true}`. `reserved/account/blueprint.yaml`: `hostname_roles: [invite]`.

- [ ] **Step 4: Snippets**

`caddy.snippet.invite.tmpl`:

```
# Rendered by paisans. Do not edit: `paisans apply` overwrites this file.
#
# Invitees of {{ .App.Name }} on {{ .Hostname }}: the invite pages and their
# static files, and nothing else. Sign-in, the home page and /admin answer on
# the app's primary hostname only, and the service registers them there too.
@invitee path /i/* /static/*
handle @invitee {
	reverse_proxy{{ range .Upstreams }} {{ . }}{{ end }} {
{{- if gt (len .Upstreams) 1 }}
		import upstream_failover
{{- else if .UpstreamElsewhere }}
		import upstream_single
{{- end }}
		header_up X-Forwarded-Proto https
	}
}
respond 404
```

Primary `caddy.snippet.tmpl`: before the proxy, `@invitepages path /i/*` / `respond @invitepages 404` with a comment that invite pages answer on the invite hostname only. Match the upstream/failover lines to what the current primary snippet has.

- [ ] **Step 5: Consumers read the resolver**

`render/site.go` `routesFor`: replace the `roles := sortedKeys(app.Hostnames)` block and the media special-case with:

```go
		for _, rh := range blueprints.RoleHostnames(p.cfg, app) {
			routeGated, routeAutoLogin, routeBypass := app.Gated, app.AutoLogin, bypass
			if rh.Role == blueprints.MediaRole || rh.Role == blueprints.InviteRole {
				// Never gated: a federating server fetching an image is a
				// machine, and an invitee has no account yet.
				routeGated, routeAutoLogin, routeBypass = false, false, gateBypass{}
			}
			out = append(out, route{
				App: name, Role: rh.Role, Hostname: rh.Hostname, Upstreams: p.upstreams(name),
				Snippet: snippetMount + name + "-" + rh.Role + ".caddy",
				Gated: routeGated, AutoLogin: routeAutoLogin, Bypass: routeBypass,
			})
		}
```

Check `renderSnippets` (`site.go:526-557`) renders a snippet per route it is given; if it enumerates roles itself, make it iterate the same `RoleHostnames`.

`dns/dns.go` `hostnames`: replace both loops' role handling with one loop over `RoleHostnames`, key `apps.%s.hostnames.%s` plus ` (derived)` when `rh.Derived`.

`validate/validate.go`: in `usesGateOutsideTheDomain`, `mediaHostnameUnderAnAppHostname` and `duplicateHostname`, replace direct `app.Hostnames` reads and the media special cases with `RoleHostnames`, keyed the same way as `dns`. Keep `mediaHostnameShape` and `storage.go:78` (they check the declared value against storage) and `config.go:1187-1194` (character check on what is declared).

`.env.secret.tmpl`: `ACCOUNT_INVITE_PUBLIC_URL=https://{{ .Account.InviteHostname }}` inside `{{ if .Account.Invites }}`; `accountValues.InviteHostname` from `blueprints.InviteHostname`.

- [ ] **Step 6: Consumer tests**

- `render/account_test.go`: invites on → the gateway's Caddyfile has a host block for `invite.example.org` importing `/etc/caddy/snippets/account-invite.caddy`, the snippet exists and contains `respond 404`, `.env` has `ACCOUNT_INVITE_PUBLIC_URL=https://invite.example.org`; invites off → no `invite.example.org` anywhere under the rendered tree, no `account-invite.caddy`.
- `dns/dns_test.go`: invites on lists `invite.example.org` with key `apps.account.hostnames.invite (derived)`; invites off with a stale declared `hostnames.invite` lists nothing for it (Review Focus 5).
- `validate_test.go` / `validate/account_test.go`: `hostnames.invite: account.example.org` (the primary) is refused by the duplicate-hostname rule; an app whose primary hostname is `invite.example.org` beside an account app with derived invite is refused, the message naming `apps.account.hostnames.invite (derived)` (Review Focus 2).

- [ ] **Step 7: Run everything**

Run: `go test ./internal/render -update && go vet ./... && go test ./...`
Expected: PASS. Read the golden diff: a new `account-invite.caddy` and a new Caddyfile host block for `invite.example.org`, nothing else changed besides the primary snippet's `/i/*` refusal.

- [ ] **Step 8: Commit**

```bash
git add -A && git commit -m "account: the invite hostname role, present only with invites on, behind one resolver

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 10: The `pocket-id` signup condition reads its setting alone

**Files:**
- Modify: `internal/blueprints/reserved/pocket-id/caddy.snippet.tmpl:29-48`, `internal/render/appview.go:162-164,448`, `internal/render/account.go` (remove `identityClients`), `internal/render/pocketid_snippet_test.go`

- [ ] **Step 1: Update the tests first**

In `pocketid_snippet_test.go`: delete `TestPocketIDSnippetNamesTheInviteApp`; in `TestPocketIDSnippetExposesOnlyWhatTheConfigurationTurnsOn` remove the "invite present" dimension's effect on signup (the signup paths are closed exactly when `allow_user_signups` is unset or `disabled`); in `TestPocketIDSnippetNamedDeployments` keep both named cases — the invite deployment has `allow_user_signups: disabled` so its signup paths are still closed.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/render/ -run PocketIDSnippet -v`
Expected: FAIL (the template still closes signup for `open` with an identity client).

- [ ] **Step 3: Implement**

Template: `{{- if or (not $signups) (eq $signups "disabled") }}`, and drop the comment clause naming identity clients. Wherever the snippet's comments name the invite app or service as a caller (the passage on `/api/users/<id>/one-time-access-email` reached through the gateway), name the account service instead. Remove `IdentityClients` from the app view, its assignment, and `identityClients` (verify with `grep -rn IdentityClients\|identityClients internal` that nothing else reads them; `secretsrotate/appsecret.go` has its own lookup).

- [ ] **Step 4: Run**

Run: `go test ./internal/render -update && go vet ./... && go test ./...`
Expected: PASS; the golden `auth.caddy` loses the "identity_app of" clause.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "pocket-id: signup routes close on allow_user_signups alone

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 11: README and decisions; PR, merge

**Files:**
- Modify: `docs/specs/2026-10-14-pocket-id-api-private.md`, `docs/specs/2026-10-14-repository-split.md` (names only)
- Modify: `README.md` (`## Invites` :498-608 → `## Accounts`; mentions at :228-230, :1882, :1977, :2416-2420, :2533, :2607-2608, :2742, :6806, :7044, :7150-7198, :7397), `docs/decisions.md` (append), `docs/specs/2026-10-14-account-service.md` (`Status:` line)

- [ ] **Step 1: README**

Rewrite `## Invites` as `## Accounts`: the `account` blueprint; the example config from the spec; `invites` required; `account.` and `invite.` and which routes each serves; the derived `invite.<domain>`; the four new rules beside the renamed ones in the Pocket ID table; settings valid only with invites on. Update every other mention to `account`, `ACCOUNT_*`, "blueprint". The Pocket ID exposure section (:7150-7198): signup routes close on `allow_user_signups` alone; remove the identity-app paragraphs. Current state only.

- [ ] **Step 2: decisions.md**

Append `## 2026-10-14: The invite service is the account service, on account. and invite.` — founder decision; what was decided (names, `invites` required, `invite.` only with invites on, one app two hostnames, pocket-id signup on its setting alone, `-service` suffix for first-party service repositories); why (account management, promotion and banning move off Pocket ID admins; the invitee origin never holds a session); what was weighed (two apps sharing a database; one hostname split by path; deploying the app automatically from Pocket ID's settings).

- [ ] **Step 3: Sibling specs carry the new names**

In `docs/specs/2026-10-14-pocket-id-api-private.md` and `docs/specs/2026-10-14-repository-split.md`: `INVITE_IDP_URL` → `ACCOUNT_IDP_URL`, `INVITE_OIDC_ISSUER` → `ACCOUNT_OIDC_ISSUER`, `internal/render/invite.go` → `internal/render/account.go`, "the invite service" → "the account service", "the invite app" → "the account app". Change nothing else in them: their design stands.

- [ ] **Step 4: Status line**

Spec: `Status: approved in session on 2026-10-14, implemented in paisans-stack and paisans-account-service; not yet run end to end.`

- [ ] **Step 5: Fresh clone, PR, merge**

Fresh clone of `feat/account-blueprint` → `go vet ./... && go test ./...` PASS. Push, `gh pr create --base develop` (body: what and why, test results, the end-to-end check still owed; footer). No CI on this repo; `gh pr view --json mergeStateStatus` must be `CLEAN`. `gh pr merge --squash`. Remove the worktree and the local `feat/account-blueprint` and `docs/account-service-spec` branches.

---

## Part C — the core catalog and the ⚠ steps

### Task 12: Catalog PR, then the confirmed steps

- [ ] **Catalog (`paisans-blueprints`, branch `feat/account-service-image` from `origin/main`, worktree `.worktrees/account-service-image`):** in `foundation.yaml` replace the `invite:` line with `account: "ghcr.io/paisans-software/paisans-account-service:0.2.*"`; grep the repo for other `invite` references to the foundation key and update them. PR into `main`, merge. Do **not** tag a release.
- [ ] **Stop and ask the founder, one at a time, quoting exactly what will run:**
  1. ⚠ `gh repo rename paisans-account-service -R Paisans-Software/paisans-invite`; then in the operator workspace `git -C src/paisans-invite remote set-url origin git@github.com:Paisans-Software/paisans-account-service.git`, `mv src/paisans-invite src/paisans-account-service`, `git -C src/paisans-account-service worktree repair`, update the workspace `.gitignore` entry and `src/CLAUDE.md` (commit on a branch in the workspace repo, merge locally).
  2. ⚠ Release the account service `v0.2.0` (its develop → main flow and tag, per that repo's release process).
  3. ⚠ Release the core catalog.
  4. ⚠ Release the toolkit (founder runs the tag).
  5. ⚠ `gh api -X DELETE /orgs/Paisans-Software/packages/container/paisans-invite` (needs `delete:packages`).
- [ ] Report: PR URLs, merge commits, test results (fresh clones), what is still owed (the founder's end-to-end run on a test deployment).
