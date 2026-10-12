# paisans-invite Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `Paisans-Software/paisans-invite`, a Go service where members make invite links, invitees agree to the code of conduct and choose their details before any account exists, admins optionally approve, and approved accounts are created at Pocket ID through exactly two writes.

**Architecture:** One Go binary with three parts that know nothing of each other's internals: `internal/store` (every SQL statement, Postgres through pgx), `internal/idp` (the provider interface, with `internal/idp/pocketid` as the only package that sends HTTP to the provider), and `internal/web` (pages and handlers). `internal/flow` holds the business rules and is the only caller of store and idp together. Templates and the stylesheet are embedded; the container has no volume.

**Tech Stack:** Go 1.25, `github.com/jackc/pgx/v5`, `github.com/coreos/go-oidc/v3`, `golang.org/x/oauth2`, `golang.org/x/time/rate`, `github.com/yuin/goldmark`, `github.com/wneessen/go-mail`, `html/template`, Postgres 18 for tests, Pocket ID v2.18.0 and Mailpit for the contract test.

**Spec:** `paisans-stack` `docs/specs/2026-10-12-invite-service.md`. Read it before starting; this plan argues from it.

**Where this runs:** a new public repository `Paisans-Software/paisans-invite`. Task 1 creates it. It is cloned locally to `paisans.community/src/paisans-invite/`, which needs a line in that repository's root `.gitignore` (done in Task 1, on its own branch there). Worktrees go in `src/paisans-invite/.worktrees/<name>`, ignored by the new repo's own `.gitignore`. **Never use `git stash`**; other sessions share the stash.

## Global Constraints

- Module path: `github.com/paisans-software/paisans-invite`. Go directive `go 1.25.8`, matching `paisans-stack`.
- Image: `ghcr.io/paisans-software/paisans-invite:<version>`; git tags `vX.Y.Z`, image tags without the `v`.
- Pocket ID writes are exactly two: `POST /api/users` with body fields `username`, `email`, `emailVerified` (always `false`), `displayName` and nothing else; and `POST /api/users/:id/one-time-access-email` with an empty JSON object. Never `userGroupIds`, never `isAdmin`.
- Pocket ID username rule: `^[a-zA-Z0-9]([a-zA-Z0-9_.@-]*[a-zA-Z0-9])?$`, at most 50 characters. Display name at most 100 characters.
- Invite tokens: 32 random bytes, base64url without padding in the link, only their SHA-256 stored.
- Email hash: HMAC-SHA256 keyed by `INVITE_EMAIL_HASH_KEY` over `strings.ToLower(strings.TrimSpace(email))`.
- Code of conduct version: lowercase hex SHA-256 of the file's bytes.
- Settings ranges: `invite_quota` 1–100 (default 3), `invite_lifetime_days` 1–365 (14), `application_wait_days` 1–365 (14), `decline_retention_days` 1–3650 (183), `require_approval` default `true`.
- Never log, audit or render into an error message: an invite token, the API key, the session key, the email hash key, an email address or a name.
- Every write the service makes (its own tables or the provider) produces one `audit` row and one log line, with IDs only.
- `CHANGELOG.md` in Keep a Changelog format; every pull request into `develop` adds its bullet, one sentence of at most 35 words, or its head commit starts `kacl-skip:`.
- Plain CSS, no build step. The copied Pocket ID tokens keep the BSD-2-Clause notice and name the Pocket ID version, v2.18.0.

### The environment contract

The toolkit's `invite` kind renders exactly these variables. Names are part of the interface between the two repositories; do not rename one without the other.

| Variable | Meaning |
|----------|---------|
| `INVITE_LISTEN_ADDR` | listen address, default `:8080` |
| `INVITE_PUBLIC_URL` | `https://<hostname>`, no trailing slash |
| `INVITE_DATABASE_URL` | Postgres URL through the site's HAProxy |
| `INVITE_SESSION_KEY` | 32 bytes as unpadded base64url (43 characters), the toolkit's default secret shape |
| `INVITE_EMAIL_HASH_KEY` | the same shape |
| `INVITE_OIDC_ISSUER` | the identity app's URL |
| `INVITE_OIDC_CLIENT_ID`, `INVITE_OIDC_CLIENT_SECRET` | the service's own client |
| `INVITE_MEMBER_GROUP`, `INVITE_ADMIN_GROUP` | group names in the `groups` claim |
| `INVITE_IDP_URL` | the identity app's URL, for the admin API and the logo |
| `INVITE_IDP_API_KEY` | the static API key |
| `INVITE_SMTP_HOST`, `INVITE_SMTP_PORT`, `INVITE_SMTP_USER`, `INVITE_SMTP_PASSWORD`, `INVITE_SMTP_FROM` | mail |
| `INVITE_SMTP_TLS` | `none`, `starttls` or `tls` |
| `INVITE_ACCENT_COLOR` | a CSS colour, or empty for the default |
| `INVITE_QUOTA`, `INVITE_REQUIRE_APPROVAL`, `INVITE_LIFETIME_DAYS`, `INVITE_APPLICATION_WAIT_DAYS`, `INVITE_DECLINE_RETENTION_DAYS` | the settings |
| `INVITE_COC_PATH` | path to the mounted code of conduct, default `/etc/paisans-invite/code-of-conduct.md` |
| `INVITE_TRUST_FORWARDED` | `true` behind the gateway's Caddy: client address is the rightmost `X-Forwarded-For` entry |

## Review Focus

1. **The same invite submitted twice at once, or from two sites.** Exactly one application is stored; the other request sees the neutral "this invite can't be used" page. Pinned in Task 3 (`TestConsumeInviteOnce`).
2. **A username differing only in case from an existing account or a pending application** (`Alice` vs `alice`). Refused as taken. Pinned in Task 3 (`TestPendingUsernameCaseInsensitive`) and Task 5 (`TestUsernameTakenCaseInsensitive`).
3. **An email with surrounding spaces or capitals that was declined before** (` Bob@Example.org `). It matches the earlier decline. Pinned in Task 2 (`TestHashNormalises`).
4. **Pocket ID answers the create but the link email fails, then the admin presses retry.** No second account; the link is sent. Pinned in Task 6 (`TestRetryDoesNotCreateTwice`).
5. **An invitee page opened by someone who is not signed in, with a stale CSRF token or a token for a different invite.** Refused with 403 and nothing stored. Pinned in Task 10 (`TestInviteeCSRFBoundToInvite`).

---

### Task 1: Repository, configuration, CI

**Files:**
- Create: `go.mod`, `.gitignore`, `README.md`, `CHANGELOG.md`, `internal/config/config.go`, `internal/config/config_test.go`, `.github/workflows/test.yml`, `.github/workflows/changelog.yml`
- Modify (in `paisans.community`, its own branch `chore/ignore-src-paisans-invite`): `.gitignore`

**Interfaces:**
- Produces: `config.Load(getenv func(string) string) (Config, error)` and

```go
type Config struct {
	ListenAddr, PublicURL, DatabaseURL string
	SessionKey, EmailHashKey           []byte // 32 bytes each
	OIDCIssuer, OIDCClientID, OIDCClientSecret string
	MemberGroup, AdminGroup            string
	IDPURL, IDPAPIKey                  string
	SMTP                               SMTP
	AccentColor                        string
	Quota                              int
	RequireApproval                    bool
	InviteLifetime, ApplicationWait, DeclineRetention time.Duration
	CoCPath                            string
	TrustForwarded                     bool
}
type SMTP struct{ Host string; Port int; User, Password, From, TLS string }
```

- [ ] **Step 1: Create the repository and clone it**

The founder approved the repository in the spec. Create it public, empty, under the organisation, with `develop` as the default branch:

```bash
gh repo create Paisans-Software/paisans-invite --public --description "Member invites with the code of conduct before the account"
cd /Users/wash/Developer/paisans.community/src
git clone git@github.com:Paisans-Software/paisans-invite.git
cd paisans-invite
git checkout -b develop
```

In `paisans.community` (not the new repo), on branch `chore/ignore-src-paisans-invite`, add beside `src/uptime/`:

```gitignore
# src/paisans-invite/ is the invite service (Paisans-Software/paisans-invite)
# that the toolkit's `invite` kind runs: its own git repo and remote.
src/paisans-invite/
```

Commit that there: `gitignore: src/paisans-invite is the invite service's own repository`.

- [ ] **Step 2: Scaffold**

`go.mod`:

```
module github.com/paisans-software/paisans-invite

go 1.25.8
```

`.gitignore`:

```gitignore
.worktrees/
/paisans-invite
```

`CHANGELOG.md`:

```markdown
# Changelog

All notable changes to this project are documented here, in the
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format.

## [Unreleased]

### Added

- Members make invite links within a quota; invitees agree to the code of conduct and choose their details before an account exists, with optional admin approval.
```

`README.md`: one paragraph saying what the service is, that it is deployed by `paisans-stack`'s `invite` kind, the environment table from this plan's Global Constraints, and "Run the tests: `INVITE_TEST_DATABASE_URL=postgres://… go test ./...`".

- [ ] **Step 3: Write the failing config test**

`internal/config/config_test.go`:

```go
package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func env(over map[string]string) func(string) string {
	key := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	base := map[string]string{
		"INVITE_PUBLIC_URL": "https://invite.example.org", "INVITE_DATABASE_URL": "postgres://x",
		"INVITE_SESSION_KEY": key, "INVITE_EMAIL_HASH_KEY": key,
		"INVITE_OIDC_ISSUER": "https://id.example.org", "INVITE_OIDC_CLIENT_ID": "c", "INVITE_OIDC_CLIENT_SECRET": "s",
		"INVITE_MEMBER_GROUP": "members", "INVITE_ADMIN_GROUP": "admins",
		"INVITE_IDP_URL": "https://id.example.org", "INVITE_IDP_API_KEY": "k",
		"INVITE_SMTP_HOST": "mail", "INVITE_SMTP_PORT": "587", "INVITE_SMTP_FROM": "a@example.org", "INVITE_SMTP_TLS": "starttls",
	}
	for k, v := range over {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func TestDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":8080" || c.Quota != 3 || !c.RequireApproval ||
		c.InviteLifetime != 14*24*time.Hour || c.ApplicationWait != 14*24*time.Hour ||
		c.DeclineRetention != 183*24*time.Hour || c.CoCPath != "/etc/paisans-invite/code-of-conduct.md" {
		t.Fatalf("defaults wrong: %+v", c)
	}
}

func TestRefusals(t *testing.T) {
	for name, over := range map[string]map[string]string{
		"short session key":  {"INVITE_SESSION_KEY": base64.RawURLEncoding.EncodeToString(make([]byte, 16))},
		"quota zero":         {"INVITE_QUOTA": "0"},
		"quota too big":      {"INVITE_QUOTA": "101"},
		"lifetime too long":  {"INVITE_LIFETIME_DAYS": "366"},
		"retention too long": {"INVITE_DECLINE_RETENTION_DAYS": "3651"},
		"bad tls":            {"INVITE_SMTP_TLS": "ssl"},
		"bad bool":           {"INVITE_REQUIRE_APPROVAL": "yes"},
		"missing api key":    {"INVITE_IDP_API_KEY": ""},
		"trailing slash":     {"INVITE_PUBLIC_URL": "https://invite.example.org/"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(env(over)); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestErrorsNeverEchoSecrets(t *testing.T) {
	_, err := Load(env(map[string]string{"INVITE_SESSION_KEY": "not-base64-SECRETVALUE"}))
	if err == nil || strings.Contains(err.Error(), "SECRETVALUE") {
		t.Fatalf("error must name the variable, not the value: %v", err)
	}
}
```

- [ ] **Step 4: Run it to see it fail**

Run: `go test ./internal/config/`
Expected: FAIL, `undefined: Load`.

- [ ] **Step 5: Implement `internal/config/config.go`**

```go
// Package config reads the service's environment. The variable names are the
// contract with paisans-stack's invite kind.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type SMTP struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
	TLS      string
}

type Config struct {
	ListenAddr, PublicURL, DatabaseURL         string
	SessionKey, EmailHashKey                   []byte
	OIDCIssuer, OIDCClientID, OIDCClientSecret string
	MemberGroup, AdminGroup                    string
	IDPURL, IDPAPIKey                          string
	SMTP                                       SMTP
	AccentColor                                string
	Quota                                      int
	RequireApproval                            bool
	InviteLifetime, ApplicationWait            time.Duration
	DeclineRetention                           time.Duration
	CoCPath                                    string
	TrustForwarded                             bool
}

func Load(getenv func(string) string) (Config, error) {
	var errs []error
	req := func(k string) string {
		v := getenv(k)
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", k))
		}
		return v
	}
	opt := func(k, def string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return def
	}
	key := func(k string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(req(k))
		if err != nil || len(b) != 32 {
			errs = append(errs, fmt.Errorf("%s must be 32 bytes as unpadded base64url", k))
			return nil
		}
		return b
	}
	intIn := func(k, def string, lo, hi int) int {
		n, err := strconv.Atoi(opt(k, def))
		if err != nil || n < lo || n > hi {
			errs = append(errs, fmt.Errorf("%s must be a whole number from %d to %d", k, lo, hi))
		}
		return n
	}
	boolean := func(k, def string) bool {
		switch opt(k, def) {
		case "true":
			return true
		case "false":
			return false
		}
		errs = append(errs, fmt.Errorf("%s must be true or false", k))
		return false
	}
	days := func(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

	c := Config{
		ListenAddr:       opt("INVITE_LISTEN_ADDR", ":8080"),
		PublicURL:        req("INVITE_PUBLIC_URL"),
		DatabaseURL:      req("INVITE_DATABASE_URL"),
		SessionKey:       key("INVITE_SESSION_KEY"),
		EmailHashKey:     key("INVITE_EMAIL_HASH_KEY"),
		OIDCIssuer:       req("INVITE_OIDC_ISSUER"),
		OIDCClientID:     req("INVITE_OIDC_CLIENT_ID"),
		OIDCClientSecret: req("INVITE_OIDC_CLIENT_SECRET"),
		MemberGroup:      req("INVITE_MEMBER_GROUP"),
		AdminGroup:       req("INVITE_ADMIN_GROUP"),
		IDPURL:           strings.TrimRight(req("INVITE_IDP_URL"), "/"),
		IDPAPIKey:        req("INVITE_IDP_API_KEY"),
		SMTP: SMTP{
			Host: req("INVITE_SMTP_HOST"), Port: intIn("INVITE_SMTP_PORT", "587", 1, 65535),
			User: getenv("INVITE_SMTP_USER"), Password: getenv("INVITE_SMTP_PASSWORD"),
			From: req("INVITE_SMTP_FROM"), TLS: opt("INVITE_SMTP_TLS", "starttls"),
		},
		AccentColor:      getenv("INVITE_ACCENT_COLOR"),
		Quota:            intIn("INVITE_QUOTA", "3", 1, 100),
		RequireApproval:  boolean("INVITE_REQUIRE_APPROVAL", "true"),
		InviteLifetime:   days(intIn("INVITE_LIFETIME_DAYS", "14", 1, 365)),
		ApplicationWait:  days(intIn("INVITE_APPLICATION_WAIT_DAYS", "14", 1, 365)),
		DeclineRetention: days(intIn("INVITE_DECLINE_RETENTION_DAYS", "183", 1, 3650)),
		CoCPath:          opt("INVITE_COC_PATH", "/etc/paisans-invite/code-of-conduct.md"),
		TrustForwarded:   boolean("INVITE_TRUST_FORWARDED", "false"),
	}
	switch c.SMTP.TLS {
	case "none", "starttls", "tls":
	default:
		errs = append(errs, errors.New("INVITE_SMTP_TLS must be none, starttls or tls"))
	}
	if strings.HasSuffix(c.PublicURL, "/") {
		errs = append(errs, errors.New("INVITE_PUBLIC_URL must not end in /"))
	}
	return c, errors.Join(errs...)
}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/config/`
Expected: PASS.

- [ ] **Step 7: CI**

`.github/workflows/test.yml`:

```yaml
name: test
on:
  pull_request:
  push:
    branches: [develop, main]
jobs:
  test:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:18
        env:
          POSTGRES_PASSWORD: test
          POSTGRES_DB: invite
        ports: ["5432:5432"]
        options: >-
          --health-cmd "pg_isready -U postgres -d invite"
          --health-interval 2s --health-timeout 5s --health-retries 20
    env:
      INVITE_TEST_DATABASE_URL: postgres://postgres:test@localhost:5432/invite?sslmode=disable
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: go vet ./...
      - run: go test -race ./...
```

`.github/workflows/changelog.yml`: fails a pull request into `develop` when `CHANGELOG.md`'s `[Unreleased]` section is unchanged, unless the head commit's subject starts with `kacl-skip:` or `[kacl-skip]`, and fails any `[Unreleased]` bullet over 35 words:

```yaml
name: changelog
on:
  pull_request:
    branches: [develop]
jobs:
  changelog:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - name: Unreleased changed, or kacl-skip
        run: |
          subject=$(git log -1 --format=%s "${{ github.event.pull_request.head.sha }}")
          case "$subject" in kacl-skip:*|"[kacl-skip]"*) echo "skipped by $subject"; exit 0;; esac
          section() { awk '/^## \[Unreleased\]/{f=1;next} /^## \[/{f=0} f' "$1"; }
          git show "origin/${{ github.base_ref }}:CHANGELOG.md" > base.md
          if diff -q <(section base.md) <(section CHANGELOG.md) >/dev/null; then
            echo "::error::[Unreleased] in CHANGELOG.md did not change"; exit 1
          fi
      - name: Bullets are at most 35 words
        run: |
          awk '/^## \[Unreleased\]/{f=1;next} /^## \[/{f=0} f && /^- /' CHANGELOG.md |
          while IFS= read -r line; do
            n=$(printf '%s' "${line#- }" | wc -w)
            if [ "$n" -gt 35 ]; then echo "::error::bullet has $n words: $line"; exit 1; fi
          done
```

- [ ] **Step 8: Commit and push**

```bash
go mod tidy
git add -A
git commit -m "feat: repository, configuration and CI"
git push -u origin develop
```

---

### Task 2: Store schema, migrations and the email hash

**Files:**
- Create: `internal/store/store.go`, `internal/store/migrations/0001_init.sql`, `internal/store/storetest/storetest.go`, `internal/store/store_test.go`, `internal/emailhash/emailhash.go`, `internal/emailhash/emailhash_test.go`

**Interfaces:**
- Produces: `store.Open(ctx, url string) (*Store, error)`, `(*Store).Migrate(ctx) error`, `(*Store).Ping(ctx) error`, `(*Store).Close()`; `storetest.New(t) *store.Store` (skips when `INVITE_TEST_DATABASE_URL` is unset, creates a fresh schema per test); `emailhash.Sum(key []byte, email string) []byte`.

- [ ] **Step 1: Failing tests**

`internal/emailhash/emailhash_test.go`:

```go
package emailhash

import (
	"bytes"
	"testing"
)

func TestHashNormalises(t *testing.T) {
	k := bytes.Repeat([]byte{1}, 32)
	if !bytes.Equal(Sum(k, " Bob@Example.org "), Sum(k, "bob@example.org")) {
		t.Fatal("trim and lower-case must not change the hash")
	}
	if bytes.Equal(Sum(k, "bob@example.org"), Sum(bytes.Repeat([]byte{2}, 32), "bob@example.org")) {
		t.Fatal("the key must change the hash")
	}
}
```

`internal/store/store_test.go`:

```go
package store_test

import (
	"context"
	"testing"

	"github.com/paisans-software/paisans-invite/internal/store/storetest"
)

func TestMigrateTwice(t *testing.T) {
	s := storetest.New(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate must be a no-op: %v", err)
	}
	if err := s.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/emailhash/ ./internal/store/...`
Expected: FAIL, undefined `Sum`, `storetest`.

- [ ] **Step 3: Implement**

`internal/emailhash/emailhash.go`:

```go
// Package emailhash keys a declined address so a database copy alone cannot
// test a guessed address against it.
package emailhash

import (
	"crypto/hmac"
	"crypto/sha256"
	"strings"
)

func Sum(key []byte, email string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(strings.ToLower(strings.TrimSpace(email))))
	return m.Sum(nil)
}
```

`internal/store/migrations/0001_init.sql`:

```sql
CREATE TABLE invites (
  id               bigserial PRIMARY KEY,
  token_hash       bytea NOT NULL UNIQUE,
  inviter_sub      text NOT NULL,
  inviter_username text NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  expires_at       timestamptz NOT NULL,
  state            text NOT NULL CHECK (state IN ('open','applied','used','expired','revoked','declined'))
);
CREATE INDEX invites_inviter_live ON invites (inviter_sub) WHERE state IN ('open','applied');

CREATE TABLE applications (
  id             bigserial PRIMARY KEY,
  invite_id      bigint NOT NULL UNIQUE REFERENCES invites(id),
  username       text NOT NULL,
  email          text NOT NULL,
  display_name   text NOT NULL,
  coc_version    text NOT NULL,
  agreed_at      timestamptz NOT NULL,
  state          text NOT NULL CHECK (state IN ('pending','link_unsent')),
  idp_user_id    text,
  decided_by_sub text,
  decided_at     timestamptz
);
CREATE UNIQUE INDEX applications_username ON applications (lower(username));

CREATE TABLE declines (
  id          bigserial PRIMARY KEY,
  email_hmac  bytea NOT NULL,
  declined_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX declines_email ON declines (email_hmac);

CREATE TABLE introductions (
  id               bigserial PRIMARY KEY,
  username         text NOT NULL,
  inviter_sub      text NOT NULL,
  inviter_username text NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit (
  id        bigserial PRIMARY KEY,
  at        timestamptz NOT NULL DEFAULT now(),
  actor_sub text,
  action    text NOT NULL,
  subject   text NOT NULL
);
```

`internal/store/store.go`:

```go
// Package store holds every SQL statement the service runs.
package store

import (
	"context"
	"embed"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrateLock is the advisory lock key every site takes before migrating, so
// two sites starting together do not both run the same migration.
const migrateLock = 0x696e7669746501 // "invite\x01"

type Store struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close()                         { s.pool.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrateLock); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrateLock)
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY)`); err != nil {
		return err
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		var done bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name=$1)`, name).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		sql, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
```

`internal/store/storetest/storetest.go`:

```go
// Package storetest gives each test its own migrated schema in the database
// named by INVITE_TEST_DATABASE_URL, and skips when it is unset.
package storetest

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/paisans-software/paisans-invite/internal/store"
)

func New(t *testing.T) *store.Store {
	t.Helper()
	base := os.Getenv("INVITE_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("INVITE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("t_%d", rand.Uint64())
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close(context.Background())
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}
```

- [ ] **Step 4: Run the tests**

Run: `go mod tidy && INVITE_TEST_DATABASE_URL=postgres://postgres:test@localhost:5432/invite?sslmode=disable go test ./internal/emailhash/ ./internal/store/...`
(Start one locally with `docker run --rm -d -p 5432:5432 -e POSTGRES_PASSWORD=test -e POSTGRES_DB=invite postgres:18`.)
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: schema, migrations and the keyed email hash"
```

---

### Task 3: Store operations

**Files:**
- Create: `internal/store/invites.go`, `internal/store/applications.go`, `internal/store/records.go`, `internal/store/ops_test.go`

**Interfaces:**
- Consumes: `Store`, `storetest.New`.
- Produces:

```go
var ErrQuota = errors.New("invite quota reached")
var ErrNotUsable = errors.New("invite cannot be used")
var ErrUsernameTaken = errors.New("username taken")
var ErrNotFound = errors.New("not found")

type Invite struct {
	ID int64; InviterSub, InviterUsername string
	CreatedAt, ExpiresAt time.Time; State string
}
type Application struct {
	ID, InviteID int64; Username, Email, DisplayName, CoCVersion string
	AgreedAt time.Time; State, IDPUserID string
	InviterSub, InviterUsername string // joined from invites
}
type NewApplication struct{ Username, Email, DisplayName, CoCVersion string }
type DeclineHistory struct{ Count int; Last time.Time }

func (s *Store) CreateInvite(ctx context.Context, tokenHash []byte, inviterSub, inviterUsername string, expires time.Time, quota int) (Invite, error) // quota <= 0 means unlimited
func (s *Store) ListInvites(ctx context.Context, inviterSub string) ([]Invite, error) // newest first, every state
func (s *Store) RevokeInvite(ctx context.Context, id int64, inviterSub string) error  // only open, only own; ErrNotFound otherwise
func (s *Store) OpenInvite(ctx context.Context, tokenHash []byte, now time.Time) (Invite, error) // ErrNotUsable unless open and unexpired
func (s *Store) Apply(ctx context.Context, tokenHash []byte, now time.Time, a NewApplication) (Application, error) // consumes the invite; ErrNotUsable, ErrUsernameTaken
func (s *Store) PendingUsernameTaken(ctx context.Context, username string) (bool, error)
func (s *Store) Application(ctx context.Context, id int64) (Application, error)
func (s *Store) PendingApplications(ctx context.Context) ([]Application, error) // oldest first, pending and link_unsent
func (s *Store) MarkLinkUnsent(ctx context.Context, id int64, idpUserID, adminSub string) error
func (s *Store) CompleteApplication(ctx context.Context, id int64, adminSub string) error // invite used, introductions row, application deleted
func (s *Store) DeclineApplication(ctx context.Context, id int64, adminSub string, emailHMAC []byte) error // decline row, invite declined, application deleted
func (s *Store) DeclineHistory(ctx context.Context, emailHMAC []byte) (DeclineHistory, error)
func (s *Store) Audit(ctx context.Context, actorSub, action, subject string) error
```

Every mutating method above except `Audit` writes its own `audit` row inside the same transaction, with `action` one of `invite.create`, `invite.revoke`, `application.create`, `application.link_unsent`, `application.complete`, `application.decline`, and `subject` the numeric ID as text.

- [ ] **Step 1: Failing tests**

`internal/store/ops_test.go`:

```go
package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-invite/internal/store"
	"github.com/paisans-software/paisans-invite/internal/store/storetest"
)

var ctx = context.Background()

func hash(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }

func newApp(u string) store.NewApplication {
	return store.NewApplication{Username: u, Email: u + "@example.org", DisplayName: u, CoCVersion: "v"}
}

func TestQuota(t *testing.T) {
	s := storetest.New(t)
	exp := time.Now().Add(time.Hour)
	for i := 0; i < 2; i++ {
		if _, err := s.CreateInvite(ctx, hash(fmt.Sprint(i)), "sub", "alice", exp, 2); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateInvite(ctx, hash("x"), "sub", "alice", exp, 2); !errors.Is(err, store.ErrQuota) {
		t.Fatalf("want ErrQuota, got %v", err)
	}
	if _, err := s.CreateInvite(ctx, hash("y"), "admin", "root", exp, 0); err != nil {
		t.Fatalf("quota 0 is unlimited: %v", err)
	}
}

func TestQuotaConcurrent(t *testing.T) {
	s := storetest.New(t)
	exp := time.Now().Add(time.Hour)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.CreateInvite(ctx, hash(fmt.Sprint("c", i)), "sub", "alice", exp, 3); err == nil {
				mu.Lock(); ok++; mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if ok != 3 {
		t.Fatalf("want exactly 3 invites, got %d", ok)
	}
}

func TestConsumeInviteOnce(t *testing.T) {
	s := storetest.New(t)
	if _, err := s.CreateInvite(ctx, hash("t"), "sub", "alice", time.Now().Add(time.Hour), 3); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.Apply(ctx, hash("t"), time.Now(), newApp(fmt.Sprint("u", i)))
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, store.ErrNotUsable) {
			t.Fatalf("unexpected error %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("want exactly one application, got %d", ok)
	}
}

func TestExpiredInviteNotUsable(t *testing.T) {
	s := storetest.New(t)
	s.CreateInvite(ctx, hash("old"), "sub", "alice", time.Now().Add(-time.Minute), 3)
	if _, err := s.OpenInvite(ctx, hash("old"), time.Now()); !errors.Is(err, store.ErrNotUsable) {
		t.Fatalf("want ErrNotUsable, got %v", err)
	}
	if _, err := s.Apply(ctx, hash("old"), time.Now(), newApp("bob")); !errors.Is(err, store.ErrNotUsable) {
		t.Fatalf("want ErrNotUsable, got %v", err)
	}
}

func TestPendingUsernameCaseInsensitive(t *testing.T) {
	s := storetest.New(t)
	exp := time.Now().Add(time.Hour)
	s.CreateInvite(ctx, hash("a"), "sub", "alice", exp, 3)
	s.CreateInvite(ctx, hash("b"), "sub", "alice", exp, 3)
	if _, err := s.Apply(ctx, hash("a"), time.Now(), newApp("Bob")); err != nil {
		t.Fatal(err)
	}
	if taken, _ := s.PendingUsernameTaken(ctx, "bob"); !taken {
		t.Fatal("bob must be taken by pending Bob")
	}
	if _, err := s.Apply(ctx, hash("b"), time.Now(), newApp("bob")); !errors.Is(err, store.ErrUsernameTaken) {
		t.Fatalf("want ErrUsernameTaken, got %v", err)
	}
	if inv, err := s.OpenInvite(ctx, hash("b"), time.Now()); err != nil || inv.State != "open" {
		t.Fatalf("a refused application must leave the invite open: %v %v", inv, err)
	}
}

func TestCompleteAndDecline(t *testing.T) {
	s := storetest.New(t)
	exp := time.Now().Add(time.Hour)
	s.CreateInvite(ctx, hash("a"), "sub", "alice", exp, 1)
	a, _ := s.Apply(ctx, hash("a"), time.Now(), newApp("bob"))
	if err := s.CompleteApplication(ctx, a.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Application(ctx, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("completed application must be deleted")
	}
	// a used invite returns the slot
	if _, err := s.CreateInvite(ctx, hash("b"), "sub", "alice", exp, 1); err != nil {
		t.Fatalf("used invite must free the slot: %v", err)
	}
	c, _ := s.Apply(ctx, hash("b"), time.Now(), newApp("carol"))
	h := hash("carol@example.org")
	if err := s.DeclineApplication(ctx, c.ID, "admin", h); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.DeclineHistory(ctx, h); d.Count != 1 {
		t.Fatalf("want one decline, got %+v", d)
	}
	if _, err := s.CreateInvite(ctx, hash("c"), "sub", "alice", exp, 1); err != nil {
		t.Fatalf("declined invite must free the slot: %v", err)
	}
}

func TestRevokeOnlyOwnOpen(t *testing.T) {
	s := storetest.New(t)
	inv, _ := s.CreateInvite(ctx, hash("a"), "sub", "alice", time.Now().Add(time.Hour), 3)
	if err := s.RevokeInvite(ctx, inv.ID, "other"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("must not revoke another member's invite")
	}
	if err := s.RevokeInvite(ctx, inv.ID, "sub"); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/store/...`
Expected: FAIL, undefined methods.

- [ ] **Step 3: Implement**

`internal/store/invites.go`:

```go
package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrQuota         = errors.New("invite quota reached")
	ErrNotUsable     = errors.New("invite cannot be used")
	ErrUsernameTaken = errors.New("username taken")
	ErrNotFound      = errors.New("not found")
)

type Invite struct {
	ID                          int64
	InviterSub, InviterUsername string
	CreatedAt, ExpiresAt        time.Time
	State                       string
}

func audit(ctx context.Context, tx pgx.Tx, actor, action string, id int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit (actor_sub, action, subject) VALUES (NULLIF($1,''), $2, $3)`,
		actor, action, strconv.FormatInt(id, 10))
	return err
}

func (s *Store) CreateInvite(ctx context.Context, tokenHash []byte, inviterSub, inviterUsername string, expires time.Time, quota int) (Invite, error) {
	var inv Invite
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialises one member's creates across every site, so the count
		// and the insert cannot interleave.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('invite-quota:' || $1, 0))`, inviterSub); err != nil {
			return err
		}
		if quota > 0 {
			var live int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM invites WHERE inviter_sub=$1 AND state IN ('open','applied')`, inviterSub).Scan(&live); err != nil {
				return err
			}
			if live >= quota {
				return ErrQuota
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO invites (token_hash, inviter_sub, inviter_username, expires_at, state)
			VALUES ($1,$2,$3,$4,'open') RETURNING id, inviter_sub, inviter_username, created_at, expires_at, state`,
			tokenHash, inviterSub, inviterUsername, expires).
			Scan(&inv.ID, &inv.InviterSub, &inv.InviterUsername, &inv.CreatedAt, &inv.ExpiresAt, &inv.State); err != nil {
			return err
		}
		return audit(ctx, tx, inviterSub, "invite.create", inv.ID)
	})
	return inv, err
}

func (s *Store) ListInvites(ctx context.Context, inviterSub string) ([]Invite, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, inviter_sub, inviter_username, created_at, expires_at, state
		FROM invites WHERE inviter_sub=$1 ORDER BY created_at DESC`, inviterSub)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Invite, error) {
		var i Invite
		err := r.Scan(&i.ID, &i.InviterSub, &i.InviterUsername, &i.CreatedAt, &i.ExpiresAt, &i.State)
		return i, err
	})
}

func (s *Store) RevokeInvite(ctx context.Context, id int64, inviterSub string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE invites SET state='revoked' WHERE id=$1 AND inviter_sub=$2 AND state='open'`, id, inviterSub)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return audit(ctx, tx, inviterSub, "invite.revoke", id)
	})
}

func (s *Store) OpenInvite(ctx context.Context, tokenHash []byte, now time.Time) (Invite, error) {
	var i Invite
	err := s.pool.QueryRow(ctx, `SELECT id, inviter_sub, inviter_username, created_at, expires_at, state
		FROM invites WHERE token_hash=$1 AND state='open' AND expires_at > $2`, tokenHash, now).
		Scan(&i.ID, &i.InviterSub, &i.InviterUsername, &i.CreatedAt, &i.ExpiresAt, &i.State)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invite{}, ErrNotUsable
	}
	return i, err
}
```

`internal/store/applications.go`:

```go
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Application struct {
	ID, InviteID                                 int64
	Username, Email, DisplayName, CoCVersion     string
	AgreedAt                                     time.Time
	State, IDPUserID                             string
	InviterSub, InviterUsername                  string
}

type NewApplication struct{ Username, Email, DisplayName, CoCVersion string }

const appColumns = `a.id, a.invite_id, a.username, a.email, a.display_name, a.coc_version, a.agreed_at,
	a.state, COALESCE(a.idp_user_id,''), i.inviter_sub, i.inviter_username`

func scanApp(r pgx.Row) (Application, error) {
	var a Application
	err := r.Scan(&a.ID, &a.InviteID, &a.Username, &a.Email, &a.DisplayName, &a.CoCVersion, &a.AgreedAt,
		&a.State, &a.IDPUserID, &a.InviterSub, &a.InviterUsername)
	return a, err
}

func (s *Store) Apply(ctx context.Context, tokenHash []byte, now time.Time, n NewApplication) (Application, error) {
	var a Application
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var inviteID int64
		err := tx.QueryRow(ctx, `UPDATE invites SET state='applied'
			WHERE token_hash=$1 AND state='open' AND expires_at > $2 RETURNING id`, tokenHash, now).Scan(&inviteID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotUsable
		}
		if err != nil {
			return err
		}
		var id int64
		err = tx.QueryRow(ctx, `INSERT INTO applications (invite_id, username, email, display_name, coc_version, agreed_at, state)
			VALUES ($1,$2,$3,$4,$5,$6,'pending') RETURNING id`,
			inviteID, n.Username, n.Email, n.DisplayName, n.CoCVersion, now).Scan(&id)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "applications_username" {
			return ErrUsernameTaken // the whole transaction rolls back, so the invite stays open
		}
		if err != nil {
			return err
		}
		if err := audit(ctx, tx, "", "application.create", id); err != nil {
			return err
		}
		a, err = scanApp(tx.QueryRow(ctx, `SELECT `+appColumns+` FROM applications a JOIN invites i ON i.id=a.invite_id WHERE a.id=$1`, id))
		return err
	})
	return a, err
}

func (s *Store) PendingUsernameTaken(ctx context.Context, username string) (bool, error) {
	var taken bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM applications WHERE lower(username)=lower($1))`, username).Scan(&taken)
	return taken, err
}

func (s *Store) Application(ctx context.Context, id int64) (Application, error) {
	a, err := scanApp(s.pool.QueryRow(ctx, `SELECT `+appColumns+` FROM applications a JOIN invites i ON i.id=a.invite_id WHERE a.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Application{}, ErrNotFound
	}
	return a, err
}

func (s *Store) PendingApplications(ctx context.Context) ([]Application, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+appColumns+` FROM applications a JOIN invites i ON i.id=a.invite_id ORDER BY a.agreed_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Application, error) { return scanApp(r) })
}

func (s *Store) MarkLinkUnsent(ctx context.Context, id int64, idpUserID, adminSub string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE applications SET state='link_unsent', idp_user_id=$2, decided_by_sub=NULLIF($3,''), decided_at=now() WHERE id=$1`, id, idpUserID, adminSub)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return audit(ctx, tx, adminSub, "application.link_unsent", id)
	})
}

func (s *Store) CompleteApplication(ctx context.Context, id int64, adminSub string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var inviteID int64
		var username string
		err := tx.QueryRow(ctx, `DELETE FROM applications WHERE id=$1 RETURNING invite_id, username`, id).Scan(&inviteID, &username)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE invites SET state='used' WHERE id=$1`, inviteID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO introductions (username, inviter_sub, inviter_username)
			SELECT $1, inviter_sub, inviter_username FROM invites WHERE id=$2`, username, inviteID); err != nil {
			return err
		}
		return audit(ctx, tx, adminSub, "application.complete", id)
	})
}

func (s *Store) DeclineApplication(ctx context.Context, id int64, adminSub string, emailHMAC []byte) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var inviteID int64
		err := tx.QueryRow(ctx, `DELETE FROM applications WHERE id=$1 AND state='pending' RETURNING invite_id`, id).Scan(&inviteID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE invites SET state='declined' WHERE id=$1`, inviteID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO declines (email_hmac) VALUES ($1)`, emailHMAC); err != nil {
			return err
		}
		return audit(ctx, tx, adminSub, "application.decline", id)
	})
}
```

`internal/store/records.go`:

```go
package store

import (
	"context"
	"time"
)

type DeclineHistory struct {
	Count int
	Last  time.Time
}

func (s *Store) DeclineHistory(ctx context.Context, emailHMAC []byte) (DeclineHistory, error) {
	var d DeclineHistory
	var last *time.Time
	err := s.pool.QueryRow(ctx, `SELECT count(*), max(declined_at) FROM declines WHERE email_hmac=$1`, emailHMAC).Scan(&d.Count, &last)
	if last != nil {
		d.Last = *last
	}
	return d, err
}

func (s *Store) Audit(ctx context.Context, actorSub, action, subject string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO audit (actor_sub, action, subject) VALUES (NULLIF($1,''), $2, $3)`, actorSub, action, subject)
	return err
}
```

- [ ] **Step 4: Run the tests**

Run: `INVITE_TEST_DATABASE_URL=… go test -race ./internal/store/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: store invites, applications, declines and audit"
```

---

### Task 4: Sweep

**Files:**
- Create: `internal/store/sweep.go`, `internal/store/sweep_test.go`

**Interfaces:**
- Produces: `type SweepResult struct{ InvitesExpired, ApplicationsExpired, DeclinesDeleted int64; Ran bool }` and `func (s *Store) Sweep(ctx context.Context, now time.Time, applicationWait, declineRetention time.Duration) (SweepResult, error)`. `Ran` is false when another site holds the sweep lock.

- [ ] **Step 1: Failing test**

`internal/store/sweep_test.go`:

```go
package store_test

import (
	"testing"
	"time"

	"github.com/paisans-software/paisans-invite/internal/store/storetest"
)

func TestSweep(t *testing.T) {
	s := storetest.New(t)
	now := time.Now()
	s.CreateInvite(ctx, hash("old"), "sub", "alice", now.Add(-time.Minute), 0)
	s.CreateInvite(ctx, hash("app"), "sub", "alice", now.Add(time.Hour), 0)
	s.Apply(ctx, hash("app"), now.Add(-20*24*time.Hour), newApp("bob"))
	s.CreateInvite(ctx, hash("d"), "sub", "alice", now.Add(time.Hour), 0)
	d, _ := s.Apply(ctx, hash("d"), now, newApp("carol"))
	s.DeclineApplication(ctx, d.ID, "admin", hash("carol"))

	r, err := s.Sweep(ctx, now.Add(200*24*time.Hour), 14*24*time.Hour, 183*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Ran || r.InvitesExpired != 1 || r.ApplicationsExpired != 1 || r.DeclinesDeleted != 1 {
		t.Fatalf("unexpected sweep: %+v", r)
	}
	if pending, _ := s.PendingApplications(ctx); len(pending) != 0 {
		t.Fatal("stale application must be deleted")
	}
}
```

Note the invite created with `now.Add(time.Hour)` and applied is `applied`, not `open`, so only `old` counts as an expired invite; the application agreed 20 days ago is past a 14-day wait.

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/store/ -run TestSweep`
Expected: FAIL, undefined `Sweep`.

- [ ] **Step 3: Implement `internal/store/sweep.go`**

```go
package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

const sweepLock = 0x696e7669746502

type SweepResult struct {
	InvitesExpired, ApplicationsExpired, DeclinesDeleted int64
	Ran                                                  bool
}

func (s *Store) Sweep(ctx context.Context, now time.Time, applicationWait, declineRetention time.Duration) (SweepResult, error) {
	var r SweepResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, sweepLock).Scan(&r.Ran); err != nil || !r.Ran {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE invites SET state='expired' WHERE state='open' AND expires_at <= $1`, now)
		if err != nil {
			return err
		}
		r.InvitesExpired = tag.RowsAffected()
		tag, err = tx.Exec(ctx, `WITH gone AS (
			DELETE FROM applications WHERE state='pending' AND agreed_at <= $1 RETURNING invite_id)
			UPDATE invites SET state='expired' WHERE id IN (SELECT invite_id FROM gone)`, now.Add(-applicationWait))
		if err != nil {
			return err
		}
		r.ApplicationsExpired = tag.RowsAffected()
		tag, err = tx.Exec(ctx, `DELETE FROM declines WHERE declined_at <= $1`, now.Add(-declineRetention))
		if err != nil {
			return err
		}
		r.DeclinesDeleted = tag.RowsAffected()
		if r.InvitesExpired+r.ApplicationsExpired+r.DeclinesDeleted > 0 {
			_, err = tx.Exec(ctx, `INSERT INTO audit (action, subject) VALUES ('sweep', $1)`,
				fmtCounts(r))
		}
		return err
	})
	return r, err
}

func fmtCounts(r SweepResult) string {
	return "invites=" + itoa(r.InvitesExpired) + " applications=" + itoa(r.ApplicationsExpired) + " declines=" + itoa(r.DeclinesDeleted)
}
```

Add `func itoa(n int64) string { return strconv.FormatInt(n, 10) }` to `records.go` with the `strconv` import.

- [ ] **Step 4: Run the tests**

Run: `INVITE_TEST_DATABASE_URL=… go test ./internal/store/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: hourly sweep of expired invites, stale applications and old declines"
```

---

### Task 5: Identity provider interface and the Pocket ID adapter

**Files:**
- Create: `internal/idp/idp.go`, `internal/idp/idptest/fake.go`, `internal/idp/pocketid/client.go`, `internal/idp/pocketid/client_test.go`, `internal/idp/pocketid/writesurface_test.go`

**Interfaces:**
- Produces:

```go
package idp
var UsernameRule = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9_.@-]*[a-zA-Z0-9])?$`)
const MaxUsername, MaxDisplayName = 50, 100
type User struct{ ID, Username, Email string; IsAdmin bool }
type NewUser struct{ Username, Email, DisplayName string }
type Provider interface {
	UsernameTaken(ctx context.Context, username string) (bool, error)
	EmailTaken(ctx context.Context, email string) (bool, error)
	FindUser(ctx context.Context, username string) (User, bool, error)
	AdminEmails(ctx context.Context) ([]string, error)
	CreateUser(ctx context.Context, u NewUser) (User, error)
	SendLoginLink(ctx context.Context, userID string) error
}
```

`pocketid.New(baseURL, apiKey string, hc *http.Client) *Client` implements `idp.Provider`. `idptest.Fake` is an in-memory `idp.Provider` with fields `Users []idp.User`, `FailSendLink error`, `FailCreate error`, and counters `Created, LinksSent int`, safe for concurrent use.

`AdminEmails` is a read (`GET /api/users`, every page, keeping users with `isAdmin` and a non-empty email); it is how the service knows whom to tell that an application is waiting. It is the third read the spec's adapter table implies and adds no write.

- [ ] **Step 1: Failing tests**

`internal/idp/pocketid/client_test.go`:

```go
package pocketid

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-invite/internal/idp"
)

type call struct{ Method, Path, Query, Key string; Body map[string]any }

func fake(t *testing.T, users []map[string]any) (*Client, *[]call) {
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Key: r.Header.Get("X-API-Key")}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			json.Unmarshal(b, &c.Body)
		}
		calls = append(calls, c)
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/users":
			json.NewEncoder(w).Encode(map[string]any{"data": users, "pagination": map[string]any{"totalPages": 1, "currentPage": 1}})
		case r.Method == "POST" && r.URL.Path == "/api/users":
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"id": "new-id", "username": c.Body["username"], "email": c.Body["email"]})
		case r.Method == "POST" && r.URL.Path == "/api/users/new-id/one-time-access-email":
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "KEY", srv.Client()), &calls
}

func TestUsernameTakenCaseInsensitive(t *testing.T) {
	c, _ := fake(t, []map[string]any{{"id": "1", "username": "Alice", "email": "a@x.org"}})
	if taken, err := c.UsernameTaken(context.Background(), "alice"); err != nil || !taken {
		t.Fatalf("want taken, got %v %v", taken, err)
	}
	if taken, _ := c.UsernameTaken(context.Background(), "alic"); taken {
		t.Fatal("a search hit that is not an exact match is not taken")
	}
}

func TestEmailTaken(t *testing.T) {
	c, _ := fake(t, []map[string]any{{"id": "1", "username": "a", "email": "A@X.org"}})
	if taken, _ := c.EmailTaken(context.Background(), " a@x.org"); !taken {
		t.Fatal("email match ignores case and spaces")
	}
}

func TestCreateUserBody(t *testing.T) {
	c, calls := fake(t, nil)
	u, err := c.CreateUser(context.Background(), idp.NewUser{Username: "bob", Email: "b@x.org", DisplayName: "Bob"})
	if err != nil || u.ID != "new-id" {
		t.Fatalf("%v %v", u, err)
	}
	got := (*calls)[0]
	if got.Key != "KEY" {
		t.Fatal("API key header missing")
	}
	want := map[string]bool{"username": true, "email": true, "emailVerified": true, "displayName": true}
	for k := range got.Body {
		if !want[k] {
			t.Fatalf("unexpected body field %q", k)
		}
	}
	if got.Body["emailVerified"] != false {
		t.Fatal("emailVerified must be false")
	}
}

func TestSendLoginLink(t *testing.T) {
	c, calls := fake(t, nil)
	if err := c.SendLoginLink(context.Background(), "new-id"); err != nil {
		t.Fatal(err)
	}
	if (*calls)[0].Path != "/api/users/new-id/one-time-access-email" {
		t.Fatal((*calls)[0].Path)
	}
}

func TestAdminEmails(t *testing.T) {
	c, _ := fake(t, []map[string]any{
		{"id": "1", "username": "a", "email": "a@x.org", "isAdmin": true},
		{"id": "2", "username": "b", "email": "b@x.org", "isAdmin": false},
		{"id": "3", "username": "c", "isAdmin": true},
	})
	got, err := c.AdminEmails(context.Background())
	if err != nil || len(got) != 1 || got[0] != "a@x.org" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestErrorsDoNotEchoBodies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, "SECRET-IN-BODY")
	}))
	defer srv.Close()
	_, err := New(srv.URL, "KEY", srv.Client()).CreateUser(context.Background(), idp.NewUser{Username: "x", Email: "x@x.org"})
	if err == nil || strings.Contains(err.Error(), "SECRET-IN-BODY") || strings.Contains(err.Error(), "KEY") {
		t.Fatalf("error must carry status and path only: %v", err)
	}
}
```

`internal/idp/pocketid/writesurface_test.go`:

```go
package pocketid

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The service is approved to make exactly two writes at the identity
// provider. This test fails on a third, on a write moved to another
// function, and on any request body field beyond the approved ones. A
// failure is a question for the founder, not a test to fix.
func TestWriteSurface(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	writes := map[string]int{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			if imp.Path.Value == `"net/http"` && imp.Name != nil {
				t.Fatalf("%s imports net/http under an alias", f)
			}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					if id, ok := x.X.(*ast.Ident); ok && id.Name == "http" {
						switch x.Sel.Name {
						case "MethodPost", "MethodPut", "MethodPatch", "MethodDelete":
							writes[fn.Name.Name+":"+x.Sel.Name]++
						}
					}
				case *ast.BasicLit:
					switch strings.Trim(x.Value, `"`) {
					case "POST", "PUT", "PATCH", "DELETE":
						t.Errorf("%s: write method spelled as a string literal; use http.Method*", fset.Position(x.Pos()))
					}
				}
				return true
			})
		}
	}
	want := map[string]int{"CreateUser:MethodPost": 1, "SendLoginLink:MethodPost": 1}
	if !reflect.DeepEqual(writes, want) {
		t.Fatalf("write surface changed: got %v, want %v", writes, want)
	}
	var fields []string
	for i := 0; i < reflect.TypeOf(createUserRequest{}).NumField(); i++ {
		fields = append(fields, reflect.TypeOf(createUserRequest{}).Field(i).Tag.Get("json"))
	}
	sort.Strings(fields)
	if !reflect.DeepEqual(fields, []string{"displayName", "email", "emailVerified", "username"}) {
		t.Fatalf("createUserRequest fields changed: %v", fields)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/idp/...`
Expected: FAIL, undefined `New`, `createUserRequest`.

- [ ] **Step 3: Implement**

`internal/idp/idp.go`:

```go
// Package idp is the service's only view of the identity provider.
package idp

import (
	"context"
	"regexp"
)

var UsernameRule = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9_.@-]*[a-zA-Z0-9])?$`)

const (
	MaxUsername    = 50
	MaxDisplayName = 100
)

type User struct {
	ID, Username, Email string
	IsAdmin             bool
}

type NewUser struct{ Username, Email, DisplayName string }

type Provider interface {
	UsernameTaken(ctx context.Context, username string) (bool, error)
	EmailTaken(ctx context.Context, email string) (bool, error)
	FindUser(ctx context.Context, username string) (User, bool, error)
	AdminEmails(ctx context.Context) ([]string, error)
	CreateUser(ctx context.Context, u NewUser) (User, error)
	SendLoginLink(ctx context.Context, userID string) error
}
```

`internal/idp/pocketid/client.go`:

```go
// Package pocketid is the only package that sends HTTP to the identity
// provider. Its write surface is pinned by writesurface_test.go.
package pocketid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/paisans-software/paisans-invite/internal/idp"
)

type Client struct {
	base, key string
	hc        *http.Client
}

func New(baseURL, apiKey string, hc *http.Client) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), key: apiKey, hc: hc}
}

type userDTO struct {
	ID       string  `json:"id"`
	Username string  `json:"username"`
	Email    *string `json:"email"`
	IsAdmin  bool    `json:"isAdmin"`
}

func (u userDTO) user() idp.User {
	e := ""
	if u.Email != nil {
		e = *u.Email
	}
	return idp.User{ID: u.ID, Username: u.Username, Email: e, IsAdmin: u.IsAdmin}
}

// createUserRequest is the whole body of the one user-creating write. Its
// fields are pinned by TestWriteSurface.
type createUserRequest struct {
	Username      string `json:"username"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"emailVerified"`
	DisplayName   string `json:"displayName"`
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("identity provider %s %s: %w", method, pathOnly(path), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("identity provider %s %s: status %d", method, pathOnly(path), resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func pathOnly(p string) string { return strings.SplitN(p, "?", 2)[0] }

func (c *Client) search(ctx context.Context, term string) ([]idp.User, error) {
	var users []idp.User
	for page := 1; ; page++ {
		q := url.Values{"search": {term}, "pagination[page]": {fmt.Sprint(page)}, "pagination[limit]": {"100"}}
		var res struct {
			Data       []userDTO `json:"data"`
			Pagination struct {
				TotalPages int `json:"totalPages"`
			} `json:"pagination"`
		}
		if err := c.do(ctx, http.MethodGet, "/api/users?"+q.Encode(), nil, &res); err != nil {
			return nil, err
		}
		for _, u := range res.Data {
			users = append(users, u.user())
		}
		if page >= res.Pagination.TotalPages {
			return users, nil
		}
	}
}

func (c *Client) FindUser(ctx context.Context, username string) (idp.User, bool, error) {
	users, err := c.search(ctx, username)
	if err != nil {
		return idp.User{}, false, err
	}
	for _, u := range users {
		if strings.EqualFold(u.Username, username) {
			return u, true, nil
		}
	}
	return idp.User{}, false, nil
}

func (c *Client) UsernameTaken(ctx context.Context, username string) (bool, error) {
	_, found, err := c.FindUser(ctx, username)
	return found, err
}

func (c *Client) EmailTaken(ctx context.Context, email string) (bool, error) {
	email = strings.TrimSpace(email)
	users, err := c.search(ctx, email)
	if err != nil {
		return false, err
	}
	for _, u := range users {
		if strings.EqualFold(u.Email, email) {
			return true, nil
		}
	}
	return false, nil
}

func (c *Client) AdminEmails(ctx context.Context) ([]string, error) {
	users, err := c.search(ctx, "")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, u := range users {
		if u.IsAdmin && u.Email != "" {
			out = append(out, u.Email)
		}
	}
	return out, nil
}

func (c *Client) CreateUser(ctx context.Context, u idp.NewUser) (idp.User, error) {
	var out userDTO
	body := createUserRequest{Username: u.Username, Email: strings.TrimSpace(u.Email), EmailVerified: false, DisplayName: u.DisplayName}
	if err := c.do(ctx, http.MethodPost, "/api/users", body, &out); err != nil {
		return idp.User{}, err
	}
	slog.Info("identity provider write", "action", "user.create", "user_id", out.ID)
	return out.user(), nil
}

func (c *Client) SendLoginLink(ctx context.Context, userID string) error {
	if err := c.do(ctx, http.MethodPost, "/api/users/"+url.PathEscape(userID)+"/one-time-access-email", struct{}{}, nil); err != nil {
		return err
	}
	slog.Info("identity provider write", "action", "user.login_link", "user_id", userID)
	return nil
}
```

`internal/idp/idptest/fake.go`:

```go
// Package idptest is an in-memory identity provider for tests.
package idptest

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/paisans-software/paisans-invite/internal/idp"
)

type Fake struct {
	mu                       sync.Mutex
	Users                    []idp.User
	FailCreate, FailSendLink error
	Created, LinksSent       int
}

func (f *Fake) UsernameTaken(_ context.Context, n string) (bool, error) {
	_, ok, _ := f.FindUser(context.Background(), n)
	return ok, nil
}

func (f *Fake) EmailTaken(_ context.Context, e string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.Users {
		if strings.EqualFold(u.Email, strings.TrimSpace(e)) {
			return true, nil
		}
	}
	return false, nil
}

func (f *Fake) FindUser(_ context.Context, n string) (idp.User, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.Users {
		if strings.EqualFold(u.Username, n) {
			return u, true, nil
		}
	}
	return idp.User{}, false, nil
}

func (f *Fake) AdminEmails(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, u := range f.Users {
		if u.IsAdmin {
			out = append(out, u.Email)
		}
	}
	return out, nil
}

func (f *Fake) CreateUser(_ context.Context, n idp.NewUser) (idp.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailCreate != nil {
		return idp.User{}, f.FailCreate
	}
	f.Created++
	u := idp.User{ID: fmt.Sprintf("id-%d", f.Created), Username: n.Username, Email: n.Email}
	f.Users = append(f.Users, u)
	return u, nil
}

func (f *Fake) SendLoginLink(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailSendLink != nil {
		return f.FailSendLink
	}
	f.LinksSent++
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/idp/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: identity provider interface, Pocket ID adapter and its write-surface test"
```

---

### Task 6: Mail, code of conduct, and the flow service

**Files:**
- Create: `internal/mail/mail.go`, `internal/mail/mailtest/mailtest.go`, `internal/coc/coc.go`, `internal/coc/coc_test.go`, `internal/flow/flow.go`, `internal/flow/flow_test.go`

**Interfaces:**
- Consumes: `store.*`, `idp.Provider`, `idptest.Fake`, `emailhash.Sum`.
- Produces:

```go
package mail
type Sender interface{ Send(ctx context.Context, to []string, subject, body string) error }
func NewSMTP(c config.SMTP) (Sender, error)
// mailtest.Recorder implements Sender, records Messages []Message{To []string; Subject, Body string}, safe for concurrent use.

package coc
type CoC struct{ Version string; HTML template.HTML }
func Load(path string) (CoC, error)

package flow
var ErrInvalidUsername, ErrInvalidEmail, ErrInvalidName, ErrUsernameTaken, ErrEmailTaken, ErrNotAgreed error
type Member struct{ Sub, Username string; Admin bool }
type Settings struct{ Quota int; RequireApproval bool; InviteLifetime time.Duration; PublicURL string; EmailHashKey []byte }
type Applicant struct{ Username, Email, DisplayName string; Agreed bool }
type Pending struct{ store.Application; Declines store.DeclineHistory }
type Service struct{ /* unexported */ }
func New(st *store.Store, p idp.Provider, m mail.Sender, c coc.CoC, s Settings, now func() time.Time) *Service
func (s *Service) CreateInvite(ctx, m Member) (link string, inv store.Invite, err error)
func (s *Service) Invites(ctx, m Member) ([]store.Invite, error)
func (s *Service) Revoke(ctx, m Member, id int64) error
func (s *Service) OpenInvite(ctx, token string) (store.Invite, error) // store.ErrNotUsable for every bad token
func (s *Service) Apply(ctx, token string, a Applicant) (approvedNow bool, err error)
func (s *Service) Pending(ctx) ([]Pending, error)
func (s *Service) Approve(ctx, admin Member, id int64) error
func (s *Service) Decline(ctx, admin Member, id int64) error
func (s *Service) CoC() coc.CoC
func TokenHash(token string) ([]byte, bool) // false for anything not 43 base64url chars decoding to 32 bytes
```

`Approve` serves both a first approval and a retry: if the application is `link_unsent` it calls `FindUser`, never `CreateUser`.

- [ ] **Step 1: Failing tests**

`internal/coc/coc_test.go`:

```go
package coc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "coc.md")
	os.WriteFile(p, []byte("# Be kind\n\n<script>x</script>"), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Version) != 64 || !strings.Contains(string(c.HTML), "<h1>Be kind</h1>") || strings.Contains(string(c.HTML), "<script>") {
		t.Fatalf("%+v", c)
	}
	os.WriteFile(p, []byte("# Be kinder"), 0o600)
	c2, _ := Load(p)
	if c2.Version == c.Version {
		t.Fatal("an edit is a new version")
	}
}
```

`internal/flow/flow_test.go`:

```go
package flow_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-invite/internal/coc"
	"github.com/paisans-software/paisans-invite/internal/flow"
	"github.com/paisans-software/paisans-invite/internal/idp"
	"github.com/paisans-software/paisans-invite/internal/idp/idptest"
	"github.com/paisans-software/paisans-invite/internal/mail/mailtest"
	"github.com/paisans-software/paisans-invite/internal/store"
	"github.com/paisans-software/paisans-invite/internal/store/storetest"
)

var ctx = context.Background()
var alice = flow.Member{Sub: "s-alice", Username: "alice"}
var root = flow.Member{Sub: "s-root", Username: "root", Admin: true}

func setup(t *testing.T, approval bool) (*flow.Service, *idptest.Fake, *mailtest.Recorder) {
	st := storetest.New(t)
	f := &idptest.Fake{Users: []idp.User{{ID: "r", Username: "root", Email: "root@x.org", IsAdmin: true}}}
	m := &mailtest.Recorder{}
	s := flow.New(st, f, m, coc.CoC{Version: "v1"}, flow.Settings{
		Quota: 2, RequireApproval: approval, InviteLifetime: 14 * 24 * time.Hour,
		PublicURL: "https://invite.example.org", EmailHashKey: make([]byte, 32),
	}, time.Now)
	return s, f, m
}

func token(t *testing.T, link string) string {
	i := strings.LastIndex(link, "/i/")
	if i < 0 {
		t.Fatalf("bad link %q", link)
	}
	return link[i+3:]
}

func bob() flow.Applicant {
	return flow.Applicant{Username: "bob", Email: "bob@example.org", DisplayName: "Bob", Agreed: true}
}

func TestApprovalFlow(t *testing.T) {
	s, f, m := setup(t, true)
	link, _, err := s.CreateInvite(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	now, err := s.Apply(ctx, token(t, link), bob())
	if err != nil || now {
		t.Fatalf("with approval on, nothing is created yet: %v %v", now, err)
	}
	if f.Created != 0 || len(m.To("root@x.org")) != 1 || len(m.To("bob@example.org")) != 1 {
		t.Fatalf("admins and the invitee are told, no account yet: created=%d", f.Created)
	}
	p, _ := s.Pending(ctx)
	if err := s.Approve(ctx, root, p[0].ID); err != nil {
		t.Fatal(err)
	}
	if f.Created != 1 || f.LinksSent != 1 {
		t.Fatalf("created=%d links=%d", f.Created, f.LinksSent)
	}
}

func TestNoApprovalCreatesAtOnce(t *testing.T) {
	s, f, _ := setup(t, false)
	link, _, _ := s.CreateInvite(ctx, alice)
	now, err := s.Apply(ctx, token(t, link), bob())
	if err != nil || !now || f.Created != 1 || f.LinksSent != 1 {
		t.Fatalf("now=%v err=%v created=%d", now, err, f.Created)
	}
}

func TestApplyRefusals(t *testing.T) {
	s, f, _ := setup(t, true)
	f.Users = append(f.Users, idp.User{ID: "x", Username: "Taken", Email: "used@example.org"})
	link, _, _ := s.CreateInvite(ctx, alice)
	tok := token(t, link)
	for name, c := range map[string]struct {
		a    flow.Applicant
		want error
	}{
		"not agreed":     {flow.Applicant{Username: "bob", Email: "b@x.org", DisplayName: "B"}, flow.ErrNotAgreed},
		"bad username":   {flow.Applicant{Username: "-bob", Email: "b@x.org", DisplayName: "B", Agreed: true}, flow.ErrInvalidUsername},
		"long username":  {flow.Applicant{Username: strings.Repeat("a", 51), Email: "b@x.org", DisplayName: "B", Agreed: true}, flow.ErrInvalidUsername},
		"taken username": {flow.Applicant{Username: "taken", Email: "b@x.org", DisplayName: "B", Agreed: true}, flow.ErrUsernameTaken},
		"used email":     {flow.Applicant{Username: "bob", Email: "USED@example.org", DisplayName: "B", Agreed: true}, flow.ErrEmailTaken},
		"bad email":      {flow.Applicant{Username: "bob", Email: "nope", DisplayName: "B", Agreed: true}, flow.ErrInvalidEmail},
		"empty name":     {flow.Applicant{Username: "bob", Email: "b@x.org", DisplayName: " ", Agreed: true}, flow.ErrInvalidName},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Apply(ctx, tok, c.a); !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
	if _, err := s.OpenInvite(ctx, tok); err != nil {
		t.Fatal("refused applications must leave the invite usable")
	}
}

func TestBadTokensAllLookTheSame(t *testing.T) {
	s, _, _ := setup(t, true)
	for _, tok := range []string{"", "short", strings.Repeat("A", 43), "!!!!"} {
		if _, err := s.OpenInvite(ctx, tok); !errors.Is(err, store.ErrNotUsable) {
			t.Fatalf("%q: %v", tok, err)
		}
	}
}

func TestRetryDoesNotCreateTwice(t *testing.T) {
	s, f, _ := setup(t, true)
	link, _, _ := s.CreateInvite(ctx, alice)
	s.Apply(ctx, token(t, link), bob())
	p, _ := s.Pending(ctx)
	f.FailSendLink = errors.New("smtp down")
	if err := s.Approve(ctx, root, p[0].ID); err == nil {
		t.Fatal("want the send failure reported")
	}
	p, _ = s.Pending(ctx)
	if p[0].State != "link_unsent" {
		t.Fatalf("state %q", p[0].State)
	}
	f.FailSendLink = nil
	if err := s.Approve(ctx, root, p[0].ID); err != nil {
		t.Fatal(err)
	}
	if f.Created != 1 || f.LinksSent != 1 {
		t.Fatalf("created=%d links=%d", f.Created, f.LinksSent)
	}
}

func TestDeclineRecordsHistoryAndNotifies(t *testing.T) {
	s, f, m := setup(t, true)
	f.Users = append(f.Users, idp.User{ID: "a", Username: "alice", Email: "alice@x.org"})
	link, _, _ := s.CreateInvite(ctx, alice)
	s.Apply(ctx, token(t, link), bob())
	p, _ := s.Pending(ctx)
	if err := s.Decline(ctx, root, p[0].ID); err != nil {
		t.Fatal(err)
	}
	if len(m.To("bob@example.org")) != 1 || len(m.To("alice@x.org")) != 1 {
		t.Fatal("invitee and inviter are told")
	}
	link2, _, _ := s.CreateInvite(ctx, alice)
	a := bob()
	a.Email = " Bob@Example.org "
	s.Apply(ctx, token(t, link2), a)
	p, _ = s.Pending(ctx)
	if p[0].Declines.Count != 1 {
		t.Fatalf("decline history not shown: %+v", p[0].Declines)
	}
}

func TestApproveRechecksUsername(t *testing.T) {
	s, f, _ := setup(t, true)
	link, _, _ := s.CreateInvite(ctx, alice)
	s.Apply(ctx, token(t, link), bob())
	f.Users = append(f.Users, idp.User{ID: "z", Username: "Bob", Email: "z@x.org"})
	p, _ := s.Pending(ctx)
	if err := s.Approve(ctx, root, p[0].ID); !errors.Is(err, flow.ErrUsernameTaken) || f.Created != 0 {
		t.Fatalf("err=%v created=%d", err, f.Created)
	}
}

func TestNonAdminCannotDecide(t *testing.T) {
	s, _, _ := setup(t, true)
	link, _, _ := s.CreateInvite(ctx, alice)
	s.Apply(ctx, token(t, link), bob())
	p, _ := s.Pending(ctx)
	if err := s.Approve(ctx, alice, p[0].ID); err == nil {
		t.Fatal("members cannot approve")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/coc/ ./internal/flow/`
Expected: FAIL, undefined packages.

- [ ] **Step 3: Implement**

`internal/mail/mail.go`:

```go
// Package mail sends the service's own mail. The login link is the identity
// provider's mail, not this package's.
package mail

import (
	"context"
	"fmt"
	"log/slog"

	gomail "github.com/wneessen/go-mail"

	"github.com/paisans-software/paisans-invite/internal/config"
)

type Sender interface {
	Send(ctx context.Context, to []string, subject, body string) error
}

type smtpSender struct {
	c    config.SMTP
	opts []gomail.Option
}

func NewSMTP(c config.SMTP) (Sender, error) {
	opts := []gomail.Option{gomail.WithPort(c.Port)}
	switch c.TLS {
	case "none":
		opts = append(opts, gomail.WithTLSPolicy(gomail.NoTLS))
	case "starttls":
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	case "tls":
		opts = append(opts, gomail.WithSSL())
	}
	if c.User != "" {
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthPlain), gomail.WithUsername(c.User), gomail.WithPassword(c.Password))
	}
	return &smtpSender{c: c, opts: opts}, nil
}

func (s *smtpSender) Send(ctx context.Context, to []string, subject, body string) error {
	client, err := gomail.NewClient(s.c.Host, s.opts...)
	if err != nil {
		return err
	}
	for _, rcpt := range to {
		m := gomail.NewMsg()
		if err := m.From(s.c.From); err != nil {
			return err
		}
		if err := m.To(rcpt); err != nil {
			return fmt.Errorf("recipient address rejected")
		}
		m.Subject(subject)
		m.SetBodyString(gomail.TypeTextPlain, body)
		if err := client.DialAndSendWithContext(ctx, m); err != nil {
			return fmt.Errorf("send mail: %w", err)
		}
	}
	slog.Info("mail sent", "subject", subject, "recipients", len(to))
	return nil
}
```

`internal/mail/mailtest/mailtest.go`:

```go
package mailtest

import (
	"context"
	"sync"
)

type Message struct {
	To            []string
	Subject, Body string
}

type Recorder struct {
	mu       sync.Mutex
	Messages []Message
	Fail     error
}

func (r *Recorder) Send(_ context.Context, to []string, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Fail != nil {
		return r.Fail
	}
	r.Messages = append(r.Messages, Message{To: to, Subject: subject, Body: body})
	return nil
}

func (r *Recorder) To(addr string) []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Message
	for _, m := range r.Messages {
		for _, t := range m.To {
			if t == addr {
				out = append(out, m)
			}
		}
	}
	return out
}
```

`internal/coc/coc.go`:

```go
// Package coc loads the code of conduct an invitee agrees to.
package coc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"os"

	"github.com/yuin/goldmark"
)

type CoC struct {
	Version string
	HTML    template.HTML
}

func Load(path string) (CoC, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return CoC{}, err
	}
	var buf bytes.Buffer
	// goldmark's default renderer omits raw HTML, so the file cannot inject
	// script into the page.
	if err := goldmark.Convert(src, &buf); err != nil {
		return CoC{}, err
	}
	sum := sha256.Sum256(src)
	return CoC{Version: hex.EncodeToString(sum[:]), HTML: template.HTML(buf.String())}, nil
}
```

`internal/flow/flow.go`:

```go
// Package flow is the service's rules: the only package that uses the store,
// the identity provider and mail together.
package flow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/paisans-software/paisans-invite/internal/coc"
	"github.com/paisans-software/paisans-invite/internal/emailhash"
	"github.com/paisans-software/paisans-invite/internal/idp"
	imail "github.com/paisans-software/paisans-invite/internal/mail"
	"github.com/paisans-software/paisans-invite/internal/store"
)

var (
	ErrInvalidUsername = errors.New("username must be 1 to 50 letters, numbers, dots, underscores, hyphens or @, starting and ending with a letter or number")
	ErrInvalidEmail    = errors.New("that does not look like an email address")
	ErrInvalidName     = errors.New("display name must be 1 to 100 characters")
	ErrUsernameTaken   = errors.New("that username is taken")
	ErrEmailTaken      = errors.New("an account already uses that email address")
	ErrNotAgreed       = errors.New("you need to agree to the code of conduct")
	ErrForbidden       = errors.New("forbidden")
)

type Member struct {
	Sub, Username string
	Admin         bool
}

type Settings struct {
	Quota           int
	RequireApproval bool
	InviteLifetime  time.Duration
	PublicURL       string
	EmailHashKey    []byte
}

type Applicant struct {
	Username, Email, DisplayName string
	Agreed                       bool
}

type Pending struct {
	store.Application
	Declines store.DeclineHistory
}

type Service struct {
	st  *store.Store
	p   idp.Provider
	m   imail.Sender
	coc coc.CoC
	s   Settings
	now func() time.Time
}

func New(st *store.Store, p idp.Provider, m imail.Sender, c coc.CoC, s Settings, now func() time.Time) *Service {
	return &Service{st: st, p: p, m: m, coc: c, s: s, now: now}
}

func (s *Service) CoC() coc.CoC { return s.coc }

func TokenHash(token string) ([]byte, bool) {
	if len(token) != 43 {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return nil, false
	}
	h := sha256.Sum256([]byte(token))
	return h[:], true
}

func (s *Service) CreateInvite(ctx context.Context, m Member) (string, store.Invite, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", store.Invite{}, err
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)
	h, _ := TokenHash(tok)
	quota := s.s.Quota
	if m.Admin {
		quota = 0
	}
	inv, err := s.st.CreateInvite(ctx, h, m.Sub, m.Username, s.now().Add(s.s.InviteLifetime), quota)
	if err != nil {
		return "", store.Invite{}, err
	}
	slog.Info("write", "action", "invite.create", "invite_id", inv.ID)
	return s.s.PublicURL + "/i/" + tok, inv, nil
}

func (s *Service) Invites(ctx context.Context, m Member) ([]store.Invite, error) {
	return s.st.ListInvites(ctx, m.Sub)
}

func (s *Service) Revoke(ctx context.Context, m Member, id int64) error {
	if err := s.st.RevokeInvite(ctx, id, m.Sub); err != nil {
		return err
	}
	slog.Info("write", "action", "invite.revoke", "invite_id", id)
	return nil
}

func (s *Service) OpenInvite(ctx context.Context, token string) (store.Invite, error) {
	h, ok := TokenHash(token)
	if !ok {
		return store.Invite{}, store.ErrNotUsable
	}
	return s.st.OpenInvite(ctx, h, s.now())
}

func (a Applicant) check() (Applicant, error) {
	a.Username = strings.TrimSpace(a.Username)
	a.Email = strings.TrimSpace(a.Email)
	a.DisplayName = strings.TrimSpace(a.DisplayName)
	if !a.Agreed {
		return a, ErrNotAgreed
	}
	if len(a.Username) > idp.MaxUsername || !idp.UsernameRule.MatchString(a.Username) {
		return a, ErrInvalidUsername
	}
	if addr, err := mail.ParseAddress(a.Email); err != nil || addr.Address != a.Email {
		return a, ErrInvalidEmail
	}
	if n := utf8.RuneCountInString(a.DisplayName); n == 0 || n > idp.MaxDisplayName {
		return a, ErrInvalidName
	}
	return a, nil
}

// available is checked at application and again at approval. A provider
// outage at application time defers the check rather than refusing.
func (s *Service) available(ctx context.Context, username, email string, strict bool) error {
	taken, err := s.p.UsernameTaken(ctx, username)
	if err != nil && strict {
		return err
	}
	if taken {
		return ErrUsernameTaken
	}
	taken, err = s.p.EmailTaken(ctx, email)
	if err != nil && strict {
		return err
	}
	if taken {
		return ErrEmailTaken
	}
	return nil
}

func (s *Service) Apply(ctx context.Context, token string, in Applicant) (bool, error) {
	h, ok := TokenHash(token)
	if !ok {
		return false, store.ErrNotUsable
	}
	a, err := in.check()
	if err != nil {
		return false, err
	}
	if taken, err := s.st.PendingUsernameTaken(ctx, a.Username); err != nil {
		return false, err
	} else if taken {
		return false, ErrUsernameTaken
	}
	if err := s.available(ctx, a.Username, a.Email, false); err != nil {
		return false, err
	}
	app, err := s.st.Apply(ctx, h, s.now(), store.NewApplication{Username: a.Username, Email: a.Email, DisplayName: a.DisplayName, CoCVersion: s.coc.Version})
	if errors.Is(err, store.ErrUsernameTaken) {
		return false, ErrUsernameTaken
	}
	if err != nil {
		return false, err
	}
	slog.Info("write", "action", "application.create", "application_id", app.ID)
	if !s.s.RequireApproval {
		return true, s.approve(ctx, "", app)
	}
	if err := s.m.Send(ctx, []string{app.Email}, "We received your application",
		"Thank you for agreeing to the code of conduct. An admin will review your application, and you will get another email when they have.\n"); err != nil {
		slog.Warn("could not acknowledge an application", "error", err)
	}
	s.notifyAdmins(ctx)
	return false, nil
}

func (s *Service) notifyAdmins(ctx context.Context) {
	admins, err := s.p.AdminEmails(ctx)
	if err == nil && len(admins) > 0 {
		err = s.m.Send(ctx, admins, "An invite application is waiting",
			"Someone has accepted an invite and is waiting for approval.\n\n"+s.s.PublicURL+"/admin\n")
	}
	if err != nil {
		slog.Warn("could not notify admins of a waiting application", "error", err)
	}
}

func (s *Service) Pending(ctx context.Context) ([]Pending, error) {
	apps, err := s.st.PendingApplications(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Pending, 0, len(apps))
	for _, a := range apps {
		d, err := s.st.DeclineHistory(ctx, emailhash.Sum(s.s.EmailHashKey, a.Email))
		if err != nil {
			return nil, err
		}
		out = append(out, Pending{Application: a, Declines: d})
	}
	return out, nil
}

func (s *Service) Approve(ctx context.Context, admin Member, id int64) error {
	if !admin.Admin {
		return ErrForbidden
	}
	app, err := s.st.Application(ctx, id)
	if err != nil {
		return err
	}
	return s.approve(ctx, admin.Sub, app)
}

func (s *Service) approve(ctx context.Context, adminSub string, app store.Application) error {
	userID := app.IDPUserID
	if app.State == "link_unsent" {
		u, found, err := s.p.FindUser(ctx, app.Username)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("account %d was created but cannot be found at the identity provider", app.ID)
		}
		userID = u.ID
	} else {
		if err := s.available(ctx, app.Username, app.Email, true); err != nil {
			return err
		}
		u, err := s.p.CreateUser(ctx, idp.NewUser{Username: app.Username, Email: app.Email, DisplayName: app.DisplayName})
		if err != nil {
			return err
		}
		userID = u.ID
		s.st.Audit(ctx, adminSub, "idp.user_create", fmt.Sprint(app.ID))
		if err := s.st.MarkLinkUnsent(ctx, app.ID, userID, adminSub); err != nil {
			return err
		}
	}
	if err := s.p.SendLoginLink(ctx, userID); err != nil {
		return fmt.Errorf("the account exists but its sign-in link was not sent; retry: %w", err)
	}
	s.st.Audit(ctx, adminSub, "idp.login_link", fmt.Sprint(app.ID))
	if err := s.st.CompleteApplication(ctx, app.ID, adminSub); err != nil {
		return err
	}
	slog.Info("write", "action", "application.complete", "application_id", app.ID)
	return nil
}

func (s *Service) Decline(ctx context.Context, admin Member, id int64) error {
	if !admin.Admin {
		return ErrForbidden
	}
	app, err := s.st.Application(ctx, id)
	if err != nil {
		return err
	}
	if err := s.st.DeclineApplication(ctx, id, admin.Sub, emailhash.Sum(s.s.EmailHashKey, app.Email)); err != nil {
		return err
	}
	slog.Info("write", "action", "application.decline", "application_id", id)
	if err := s.m.Send(ctx, []string{app.Email}, "About your invitation",
		"Thank you for your interest. Your application to join was not accepted.\n"); err != nil {
		slog.Warn("could not tell the invitee of a decline", "error", err)
	}
	if inviter, found, err := s.p.FindUser(ctx, app.InviterUsername); err == nil && found && inviter.Email != "" {
		if err := s.m.Send(ctx, []string{inviter.Email}, "An invite you sent was declined",
			"An application made with one of your invites was declined. Your invite slot is free again.\n"); err != nil {
			slog.Warn("could not tell the inviter of a decline", "error", err)
		}
	}
	return nil
}
```

When approval is off, `Apply` calls `approve` with an empty admin sub, so the audit rows carry no actor; that is the record that the deployment's setting made the decision.

- [ ] **Step 4: Run the tests**

Run: `go mod tidy && INVITE_TEST_DATABASE_URL=… go test -race ./internal/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: invite, application, approval and decline flows"
```

---

### Task 7: Sessions, OIDC sign-in, CSRF and rate limits

**Files:**
- Create: `internal/web/session.go`, `internal/web/session_test.go`, `internal/web/auth.go`, `internal/web/guard.go`, `internal/web/guard_test.go`

**Interfaces:**
- Produces:

```go
type Session struct{ Sub, Username string; Admin bool; CSRF string; Exp int64 }
type sessions struct{ key []byte; secure bool }
func (s sessions) write(w http.ResponseWriter, sess Session)
func (s sessions) read(r *http.Request) (Session, bool) // false if missing, tampered or expired
func (s sessions) clear(w http.ResponseWriter)
func inviteeCSRF(key []byte, tokenHash []byte) string
func clientIP(r *http.Request, trustForwarded bool) string
type limiter struct{ /* per-IP token buckets, 10 per minute, burst 20, idle buckets dropped after 10 minutes */ }
func newLimiter() *limiter
func (l *limiter) allow(ip string) bool
type authenticator struct{ /* go-oidc provider, verifier, oauth2 config, member and admin group names, sessions */ }
func newAuthenticator(ctx context.Context, cfg config.Config, s sessions) (*authenticator, error)
func (a *authenticator) login(w http.ResponseWriter, r *http.Request)    // GET /auth/login
func (a *authenticator) callback(w http.ResponseWriter, r *http.Request) // GET /auth/callback
```

Cookies: `invite_session` (8 hours) and `invite_login` (10 minutes, holds state, nonce and PKCE verifier), both `HttpOnly`, `SameSite=Lax`, `Secure` when `PublicURL` is `https`, value `base64url(json) + "." + base64url(HMAC-SHA256(key, json))`.

- [ ] **Step 1: Failing tests**

`internal/web/session_test.go`:

```go
package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionRoundTrip(t *testing.T) {
	s := sessions{key: make([]byte, 32)}
	rec := httptest.NewRecorder()
	s.write(rec, Session{Sub: "x", Username: "alice", CSRF: "c", Exp: time.Now().Add(time.Hour).Unix()})
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	got, ok := s.read(req)
	if !ok || got.Username != "alice" {
		t.Fatalf("%v %v", got, ok)
	}
}

func TestSessionTamperAndExpiry(t *testing.T) {
	s := sessions{key: make([]byte, 32)}
	for name, c := range map[string]*http.Cookie{
		"tampered": {Name: "invite_session", Value: "eyJTdWIiOiJ4In0.AAAA"},
		"garbage":  {Name: "invite_session", Value: "nope"},
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.AddCookie(c)
		if _, ok := s.read(req); ok {
			t.Fatal(name)
		}
	}
	rec := httptest.NewRecorder()
	s.write(rec, Session{Sub: "x", Exp: time.Now().Add(-time.Minute).Unix()})
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	if _, ok := s.read(req); ok {
		t.Fatal("expired session accepted")
	}
}
```

`internal/web/guard_test.go`:

```go
package web

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:5555"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if got := clientIP(r, true); got != "203.0.113.9" {
		t.Fatalf("trusted: rightmost entry, got %s", got)
	}
	if got := clientIP(r, false); got != "10.0.0.2" {
		t.Fatalf("untrusted: remote address, got %s", got)
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter()
	allowed := 0
	for i := 0; i < 30; i++ {
		if l.allow("1.2.3.4") {
			allowed++
		}
	}
	if allowed != 20 {
		t.Fatalf("burst should be 20, got %d", allowed)
	}
	if !l.allow("5.6.7.8") {
		t.Fatal("buckets are per address")
	}
}

func TestInviteeCSRFDiffersPerInvite(t *testing.T) {
	k := make([]byte, 32)
	if inviteeCSRF(k, []byte("a")) == inviteeCSRF(k, []byte("b")) {
		t.Fatal("token must be bound to the invite")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/web/`
Expected: FAIL, undefined symbols.

- [ ] **Step 3: Implement**

`internal/web/session.go`:

```go
package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type Session struct {
	Sub, Username string
	Admin         bool
	CSRF          string
	Exp           int64
}

type sessions struct {
	key    []byte
	secure bool
}

func sign(key []byte, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, key)
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil)), nil
}

func verify(key []byte, s string, v any) bool {
	payload, mac, ok := strings.Cut(s, ".")
	if !ok {
		return false
	}
	b, err1 := base64.RawURLEncoding.DecodeString(payload)
	got, err2 := base64.RawURLEncoding.DecodeString(mac)
	if err1 != nil || err2 != nil {
		return false
	}
	m := hmac.New(sha256.New, key)
	m.Write(b)
	if !hmac.Equal(got, m.Sum(nil)) {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

func (s sessions) cookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode}
}

func (s sessions) write(w http.ResponseWriter, sess Session) {
	v, err := sign(s.key, sess)
	if err != nil {
		return
	}
	http.SetCookie(w, s.cookie("invite_session", v, int(time.Until(time.Unix(sess.Exp, 0)).Seconds())))
}

func (s sessions) read(r *http.Request) (Session, bool) {
	c, err := r.Cookie("invite_session")
	if err != nil {
		return Session{}, false
	}
	var sess Session
	if !verify(s.key, c.Value, &sess) || time.Now().Unix() >= sess.Exp {
		return Session{}, false
	}
	return sess, true
}

func (s sessions) clear(w http.ResponseWriter) { http.SetCookie(w, s.cookie("invite_session", "", -1)) }

func inviteeCSRF(key, tokenHash []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("invitee-csrf:"))
	m.Write(tokenHash)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
```

`internal/web/guard.go`:

```go
package web

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

func clientIP(r *http.Request, trustForwarded bool) string {
	if trustForwarded {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type bucket struct {
	l    *rate.Limiter
	seen time.Time
}

type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	last    time.Time
}

func newLimiter() *limiter { return &limiter{buckets: map[string]*bucket{}, last: time.Now()} }

func (l *limiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.last) > time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.seen) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.last = now
	}
	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{l: rate.NewLimiter(rate.Every(6*time.Second), 20)}
		l.buckets[ip] = b
	}
	b.seen = now
	return b.l.Allow()
}
```

`internal/web/auth.go`:

```go
package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"slices"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/paisans-software/paisans-invite/internal/config"
)

type authenticator struct {
	verifier            *oidc.IDTokenVerifier
	oauth               oauth2.Config
	memberGrp, adminGrp string
	s                   sessions
}

type loginState struct {
	State, Nonce, Verifier string
	Exp                    int64
}

func randomString() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func newAuthenticator(ctx context.Context, cfg config.Config, s sessions) (*authenticator, error) {
	p, err := oidc.NewProvider(ctx, cfg.OIDCIssuer)
	if err != nil {
		return nil, err
	}
	return &authenticator{
		verifier: p.Verifier(&oidc.Config{ClientID: cfg.OIDCClientID}),
		oauth: oauth2.Config{
			ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCClientSecret, Endpoint: p.Endpoint(),
			RedirectURL: cfg.PublicURL + "/auth/callback",
			Scopes:      []string{oidc.ScopeOpenID, "profile", "email", "groups"},
		},
		memberGrp: cfg.MemberGroup, adminGrp: cfg.AdminGroup, s: s,
	}, nil
}

func (a *authenticator) login(w http.ResponseWriter, r *http.Request) {
	st := loginState{State: randomString(), Nonce: randomString(), Verifier: oauth2.GenerateVerifier(), Exp: time.Now().Add(10 * time.Minute).Unix()}
	v, _ := sign(a.s.key, st)
	http.SetCookie(w, a.s.cookie("invite_login", v, 600))
	http.Redirect(w, r, a.oauth.AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier)), http.StatusFound)
}

func (a *authenticator) callback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("invite_login")
	var st loginState
	if err != nil || !verify(a.s.key, c.Value, &st) || time.Now().Unix() > st.Exp || r.URL.Query().Get("state") != st.State {
		http.Error(w, "Sign-in expired. Please try again.", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, a.s.cookie("invite_login", "", -1))
	tok, err := a.oauth.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		http.Error(w, "Sign-in failed.", http.StatusBadGateway)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := a.verifier.Verify(r.Context(), raw)
	if err != nil || idt.Nonce != st.Nonce {
		http.Error(w, "Sign-in failed.", http.StatusBadGateway)
		return
	}
	var claims struct {
		Sub               string   `json:"sub"`
		PreferredUsername string   `json:"preferred_username"`
		Groups            []string `json:"groups"`
	}
	if err := idt.Claims(&claims); err != nil {
		http.Error(w, "Sign-in failed.", http.StatusBadGateway)
		return
	}
	admin := slices.Contains(claims.Groups, a.adminGrp)
	if !admin && !slices.Contains(claims.Groups, a.memberGrp) {
		http.Error(w, "Only members can make invites.", http.StatusForbidden)
		return
	}
	a.s.write(w, Session{Sub: claims.Sub, Username: claims.PreferredUsername, Admin: admin, CSRF: randomString(), Exp: time.Now().Add(8 * time.Hour).Unix()})
	http.Redirect(w, r, "/", http.StatusFound)
}
```

- [ ] **Step 4: Run the tests**

Run: `go mod tidy && go test ./internal/web/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: signed sessions, OIDC sign-in, CSRF tokens and per-address rate limits"
```

---

### Task 8: Look and feel — layout, stylesheet, templates

**Files:**
- Create: `internal/web/static/style.css`, `internal/web/static/NOTICE-pocket-id.txt`, `internal/web/templates/layout.html`, `internal/web/templates/home.html`, `internal/web/templates/admin.html`, `internal/web/templates/invite.html`, `internal/web/templates/message.html`, `internal/web/render.go`, `internal/web/render_test.go`

**Interfaces:**
- Produces: `type view struct{ Title, Accent, LogoURL, CSRF string; Session *Session; Data any; Error string }`, `type renderer struct{ t *template.Template; accent, logo string }`, `func newRenderer(accent, idpURL string) (*renderer, error)`, `func (r *renderer) page(w http.ResponseWriter, status int, name string, v view)`; `//go:embed static` as `staticFS`.

- [ ] **Step 1: Failing test**

`internal/web/render_test.go`:

```go
package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRendererAccentIsSanitised(t *testing.T) {
	r, err := newRenderer(`red;}</style><script>`, "https://id.example.org")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.page(rec, 200, "message.html", view{Title: "Hi", Data: map[string]string{"Heading": "Hello", "Body": "b"}})
	out := rec.Body.String()
	if strings.Contains(out, "<script>") {
		t.Fatal("accent must not inject markup")
	}
	if !strings.Contains(out, "https://id.example.org/api/application-images/logo") {
		t.Fatal("logo comes from the identity provider")
	}
}

func TestStylesheetCarriesNotice(t *testing.T) {
	b, err := staticFS.ReadFile("static/style.css")
	if err != nil || !strings.Contains(string(b), "Pocket ID v2.18.0") || !strings.Contains(string(b), "BSD-2-Clause") {
		t.Fatal("style.css must name its source version and licence")
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/web/ -run 'Renderer|Stylesheet'`
Expected: FAIL.

- [ ] **Step 3: Implement**

`internal/web/static/style.css` — copy the `:root` and `.dark` variable blocks verbatim from `https://raw.githubusercontent.com/pocket-id/pocket-id/v2.18.0/frontend/src/app.css` (fetch it; do not retype). The `.dark` block becomes `@media (prefers-color-scheme: dark) { :root { … } }`. Above it:

```css
/*
 * Design tokens copied from Pocket ID v2.18.0, frontend/src/app.css.
 * Copyright (c) 2024, Elias Schneider. BSD-2-Clause; see NOTICE-pocket-id.txt.
 * When Pocket ID changes its look, re-copy these blocks and update the version above.
 */
```

Below the tokens, the components (ours):

```css
*, ::before, ::after { box-sizing: border-box; border-color: var(--border); }
html { font-family: ui-sans-serif, system-ui, sans-serif; }
body { margin: 0; background: var(--muted); color: var(--foreground); min-height: 100vh; }
main { max-width: 40rem; margin: 0 auto; padding: 2rem 1rem; }
header { display: flex; align-items: center; gap: .75rem; margin-bottom: 1.5rem; }
header img { height: 2rem; width: auto; }
.card { background: var(--card); color: var(--card-foreground); border: 1px solid var(--border);
        border-radius: calc(var(--radius) + 4px); padding: 1.5rem; margin-bottom: 1rem; }
h1 { font-size: 1.5rem; margin: 0 0 1rem; } h2 { font-size: 1.125rem; margin: 0 0 .75rem; }
label { display: block; font-size: .875rem; font-weight: 500; margin: 1rem 0 .375rem; }
input[type=text], input[type=email] { width: 100%; height: 2.5rem; padding: 0 .75rem; font: inherit;
        background: transparent; color: inherit; border: 1px solid var(--input); border-radius: calc(var(--radius) - 2px); }
input:focus-visible, button:focus-visible { outline: 2px solid var(--ring); outline-offset: 2px; }
.check { display: flex; gap: .5rem; align-items: flex-start; margin: 1rem 0; }
button { height: 2.5rem; padding: 0 1rem; font: inherit; font-weight: 500; cursor: pointer;
        border: 0; border-radius: calc(var(--radius) - 2px); background: var(--accent-color, var(--primary)); color: var(--primary-foreground); }
button.secondary { background: var(--secondary); color: var(--secondary-foreground); }
button.destructive { background: var(--destructive); color: #fff; }
.alert { border: 1px solid var(--border); border-radius: calc(var(--radius) - 2px); padding: .75rem 1rem; margin-bottom: 1rem; }
.alert.error { border-color: var(--destructive); color: var(--destructive); }
.muted { color: var(--muted-foreground); font-size: .875rem; }
.coc { max-height: 24rem; overflow: auto; border: 1px solid var(--border); border-radius: calc(var(--radius) - 2px); padding: 0 1rem; }
table { width: 100%; border-collapse: collapse; font-size: .875rem; }
td, th { text-align: left; padding: .5rem .25rem; border-bottom: 1px solid var(--border); vertical-align: top; }
.link { font-family: ui-monospace, monospace; word-break: break-all; background: var(--muted); padding: .5rem; border-radius: calc(var(--radius) - 4px); }
form.inline { display: inline; }
```

`internal/web/static/NOTICE-pocket-id.txt`: the full BSD-2-Clause text from `https://raw.githubusercontent.com/pocket-id/pocket-id/v2.18.0/LICENSE`, copied verbatim.

`internal/web/templates/layout.html`:

```html
{{define "layout"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<link rel="stylesheet" href="/static/style.css">
{{if .Accent}}<style>:root{--accent-color: {{.Accent}};}</style>{{end}}
</head>
<body><main>
<header><img src="{{.LogoURL}}" alt=""><span>{{if .Session}}Signed in as {{.Session.Username}}{{end}}</span></header>
{{if .Error}}<div class="alert error" role="alert">{{.Error}}</div>{{end}}
{{template "content" .}}
</main></body></html>{{end}}
```

`html/template` escapes `{{.Accent}}` inside `<style>` as CSS; the renderer additionally only passes an accent matching `^(#[0-9a-fA-F]{3,8}|[a-zA-Z]+|(rgb|hsl|oklch)a?\([0-9.,%\s/]+\))$`, otherwise none.

`internal/web/templates/home.html`:

```html
{{define "content"}}
<div class="card">
  <h1>Invite someone</h1>
  <p class="muted">{{.Data.Remaining}}</p>
  {{with .Data.NewLink}}<p>Send this link to the person you are inviting. It works once{{with $.Data.Expires}}, until {{.}}{{end}}.</p><p class="link">{{.}}</p>{{end}}
  <form method="post" action="/invites"><input type="hidden" name="csrf" value="{{.CSRF}}"><button>Make an invite link</button></form>
</div>
<div class="card">
  <h2>Your invites</h2>
  {{if .Data.Invites}}<table><tr><th>Made</th><th>State</th><th></th></tr>
  {{range .Data.Invites}}<tr><td>{{.CreatedAt.Format "2 Jan 2006"}}</td><td>{{.State}}</td>
  <td>{{if eq .State "open"}}<form class="inline" method="post" action="/invites/{{.ID}}/revoke"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button class="secondary">Revoke</button></form>{{end}}</td></tr>{{end}}
  </table>{{else}}<p class="muted">You have not made any invites yet.</p>{{end}}
</div>
{{if .Session.Admin}}<p><a href="/admin">Applications waiting for approval</a></p>{{end}}
{{end}}
```

`internal/web/templates/admin.html`:

```html
{{define "content"}}
<div class="card"><h1>Applications</h1>
{{if .Data}}<table><tr><th>Who</th><th>Invited by</th><th>Applied</th><th>Declined before</th><th></th></tr>
{{range .Data}}<tr>
<td>{{.DisplayName}}<br><span class="muted">{{.Username}} · {{.Email}}</span></td>
<td>{{.InviterUsername}}</td>
<td>{{.AgreedAt.Format "2 Jan 2006"}}</td>
<td>{{if .Declines.Count}}{{.Declines.Count}} time(s), last {{.Declines.Last.Format "2 Jan 2006"}}{{else}}never{{end}}</td>
<td>{{if eq .State "link_unsent"}}<p class="muted">Account created; sign-in link not sent.</p>
<form class="inline" method="post" action="/admin/applications/{{.ID}}/approve"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button>Retry sending</button></form>
{{else}}<form class="inline" method="post" action="/admin/applications/{{.ID}}/approve"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button>Approve</button></form>
<form class="inline" method="post" action="/admin/applications/{{.ID}}/decline"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button class="destructive">Decline</button></form>{{end}}</td>
</tr>{{end}}</table>{{else}}<p class="muted">Nothing is waiting.</p>{{end}}
</div>
<p><a href="/">Back to your invites</a></p>
{{end}}
```

`internal/web/templates/invite.html`:

```html
{{define "content"}}
<div class="card"><h1>You have been invited</h1>
<p>{{.Data.Inviter}} invited you. Please read the code of conduct first.</p>
<div class="coc">{{.Data.CoC}}</div>
<form method="post">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<div class="check"><input type="checkbox" id="agree" name="agree" value="yes" required><label for="agree" style="margin:0">I have read the code of conduct and agree to it.</label></div>
<label for="username">Username</label><input type="text" id="username" name="username" value="{{.Data.Username}}" maxlength="50" required autocomplete="username">
<p class="muted">Other members see this, and it cannot be changed later. Letters, numbers, dots, underscores and hyphens.</p>
<label for="display_name">Display name</label><input type="text" id="display_name" name="display_name" value="{{.Data.DisplayName}}" maxlength="100" required autocomplete="name">
<label for="email">Email</label><input type="email" id="email" name="email" value="{{.Data.Email}}" required autocomplete="email">
<p class="muted">Your sign-in link will be sent here.</p>
<p><button>Join</button></p>
</form></div>
{{end}}
```

`internal/web/templates/message.html`:

```html
{{define "content"}}<div class="card"><h1>{{.Data.Heading}}</h1><p>{{.Data.Body}}</p></div>{{end}}
```

`internal/web/render.go`:

```go
package web

import (
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"regexp"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

var accentRule = regexp.MustCompile(`^(#[0-9a-fA-F]{3,8}|[a-zA-Z]+|(rgb|hsl|oklch)a?\([0-9.,%\s/]+\))$`)

type view struct {
	Title, Accent, LogoURL, CSRF string
	Session                      *Session
	Data                         any
	Error                        string
}

type renderer struct {
	pages        map[string]*template.Template
	accent, logo string
}

func newRenderer(accent, idpURL string) (*renderer, error) {
	r := &renderer{pages: map[string]*template.Template{}, logo: idpURL + "/api/application-images/logo"}
	if accentRule.MatchString(accent) {
		r.accent = accent
	}
	for _, name := range []string{"home.html", "admin.html", "invite.html", "message.html"} {
		t, err := template.ParseFS(templateFS, "templates/layout.html", "templates/"+name)
		if err != nil {
			return nil, err
		}
		r.pages[name] = t
	}
	return r, nil
}

func (r *renderer) page(w http.ResponseWriter, status int, name string, v view) {
	v.Accent, v.LogoURL = r.accent, r.logo
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' "+r.logo+"; style-src 'self' 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	w.WriteHeader(status)
	if err := r.pages[name].ExecuteTemplate(w, "layout", v); err != nil {
		slog.Error("render", "page", name, "error", err)
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/web/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: pages styled with Pocket ID's design tokens"
```

---

### Task 9: Member and admin handlers

**Files:**
- Create: `internal/web/server.go`, `internal/web/member.go`, `internal/web/admin.go`, `internal/web/server_test.go`

**Interfaces:**
- Consumes: `flow.Service`, `sessions`, `authenticator`, `renderer`, `limiter`, `store.Store` (for `/healthz` only, through an interface `pinger`).
- Produces:

```go
type Options struct{ Flow *flow.Service; Sessions sessions; Auth http.Handler /* serves /auth/ */; Render *renderer; Health pinger; TrustForwarded bool; SessionKey []byte }
type pinger interface{ Ping(context.Context) error }
func NewServer(o Options) http.Handler
func New(ctx context.Context, cfg config.Config, f *flow.Service, health pinger) (http.Handler, error) // builds sessions, authenticator, renderer from cfg
```

Routes (Go 1.22 patterns):

| Route | Who | Does |
|-------|-----|------|
| `GET /healthz` | anyone | 200 `ok` if the database answers, else 503 |
| `GET /static/` | anyone | the embedded stylesheet and notice |
| `GET /auth/login`, `GET /auth/callback` | anyone | sign-in |
| `POST /auth/logout` | session + CSRF | clears the session |
| `GET /{$}` | member | home: remaining quota, invites, new link from `?new=` flash cookie |
| `POST /invites` | member + CSRF | create; on `store.ErrQuota` re-render with the error |
| `POST /invites/{id}/revoke` | member + CSRF | revoke |
| `GET /admin` | admin | queue |
| `POST /admin/applications/{id}/approve` | admin + CSRF | approve or retry |
| `POST /admin/applications/{id}/decline` | admin + CSRF | decline |

A request without a session to a member route redirects to `/auth/login`. A member without admin on an admin route gets 403. A failed CSRF check gets 403 and changes nothing. The newly made link is shown once, on the response to `POST /invites` itself (render the home page directly with `NewLink` set, status 200), never placed in a URL or a cookie.

- [ ] **Step 1: Failing tests**

`internal/web/server_test.go` builds a server with `NewServer`, a real `flow.Service` over `storetest.New` and `idptest.Fake`, `Auth` set to a stub handler, and helpers that mint a session cookie directly with `sessions.write`:

```go
package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-invite/internal/coc"
	"github.com/paisans-software/paisans-invite/internal/flow"
	"github.com/paisans-software/paisans-invite/internal/idp"
	"github.com/paisans-software/paisans-invite/internal/idp/idptest"
	"github.com/paisans-software/paisans-invite/internal/mail/mailtest"
	"github.com/paisans-software/paisans-invite/internal/store/storetest"
)

type harness struct {
	h    http.Handler
	s    sessions
	fake *idptest.Fake
	flow *flow.Service
}

func newHarness(t *testing.T, approval bool) harness {
	st := storetest.New(t)
	fake := &idptest.Fake{Users: []idp.User{{ID: "r", Username: "root", Email: "root@x.org", IsAdmin: true}}}
	key := make([]byte, 32)
	f := flow.New(st, fake, &mailtest.Recorder{}, coc.CoC{Version: "v", HTML: "<p>Be kind</p>"}, flow.Settings{
		Quota: 1, RequireApproval: approval, InviteLifetime: time.Hour, PublicURL: "https://invite.example.org", EmailHashKey: key,
	}, time.Now)
	r, _ := newRenderer("", "https://id.example.org")
	s := sessions{key: key}
	return harness{h: NewServer(Options{Flow: f, Sessions: s, Auth: http.NotFoundHandler(), Render: r, Health: st, SessionKey: key}), s: s, fake: fake, flow: f}
}

func (h harness) as(sess Session, method, path string, form url.Values) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.s.write(rec, sess)
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	out := httptest.NewRecorder()
	h.h.ServeHTTP(out, req)
	return out
}

var member = Session{Sub: "s-alice", Username: "alice", CSRF: "tok", Exp: time.Now().Add(time.Hour).Unix()}
var admin = Session{Sub: "s-root", Username: "root", Admin: true, CSRF: "tok", Exp: time.Now().Add(time.Hour).Unix()}

func TestAnonymousRedirectsToLogin(t *testing.T) {
	h := newHarness(t, true)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 302 || rec.Header().Get("Location") != "/auth/login" {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestCreateInviteShowsLinkOnceAndEnforcesQuota(t *testing.T) {
	h := newHarness(t, true)
	rec := h.as(member, "POST", "/invites", url.Values{"csrf": {"tok"}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "https://invite.example.org/i/") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = h.as(member, "POST", "/invites", url.Values{"csrf": {"tok"}})
	if !strings.Contains(rec.Body.String(), "quota") {
		t.Fatal("second invite past a quota of 1 must be refused with a message")
	}
	if rec = h.as(admin, "POST", "/invites", url.Values{"csrf": {"tok"}}); !strings.Contains(rec.Body.String(), "/i/") {
		t.Fatal("admins have no quota")
	}
}

func TestCSRFRequired(t *testing.T) {
	h := newHarness(t, true)
	if rec := h.as(member, "POST", "/invites", url.Values{"csrf": {"wrong"}}); rec.Code != 403 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestMemberCannotSeeAdmin(t *testing.T) {
	h := newHarness(t, true)
	if rec := h.as(member, "GET", "/admin", nil); rec.Code != 403 {
		t.Fatalf("got %d", rec.Code)
	}
	if rec := h.as(member, "POST", "/admin/applications/1/approve", url.Values{"csrf": {"tok"}}); rec.Code != 403 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestAdminApproves(t *testing.T) {
	h := newHarness(t, true)
	link, _, _ := h.flow.CreateInvite(context.Background(), flow.Member{Sub: "s-alice", Username: "alice"})
	tok := link[strings.LastIndex(link, "/")+1:]
	h.flow.Apply(context.Background(), tok, flow.Applicant{Username: "bob", Email: "bob@x.org", DisplayName: "Bob", Agreed: true})
	rec := h.as(admin, "GET", "/admin", nil)
	if !strings.Contains(rec.Body.String(), "bob@x.org") {
		t.Fatal("queue must list the application")
	}
	p, _ := h.flow.Pending(context.Background())
	rec = h.as(admin, "POST", "/admin/applications/"+itoa(p[0].ID)+"/approve", url.Values{"csrf": {"tok"}})
	if rec.Code != 303 || h.fake.Created != 1 {
		t.Fatalf("%d created=%d", rec.Code, h.fake.Created)
	}
}

func TestHealthz(t *testing.T) {
	h := newHarness(t, true)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
}
```

Add `func itoa(n int64) string { return strconv.FormatInt(n, 10) }` in `server.go` (with `strconv`).

- [ ] **Step 2: Run them to see them fail**

Run: `INVITE_TEST_DATABASE_URL=… go test ./internal/web/`
Expected: FAIL, undefined `NewServer`.

- [ ] **Step 3: Implement**

`internal/web/server.go`:

```go
package web

import (
	"context"
	"crypto/subtle"
	"io/fs"
	"net/http"
	"strconv"

	"github.com/paisans-software/paisans-invite/internal/config"
	"github.com/paisans-software/paisans-invite/internal/flow"
)

type pinger interface{ Ping(context.Context) error }

type Options struct {
	Flow           *flow.Service
	Sessions       sessions
	Auth           http.Handler
	Render         *renderer
	Health         pinger
	TrustForwarded bool
	SessionKey     []byte
}

type server struct {
	Options
	limit *limiter
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func New(ctx context.Context, cfg config.Config, f *flow.Service, health pinger) (http.Handler, error) {
	s := sessions{key: cfg.SessionKey, secure: len(cfg.PublicURL) > 8 && cfg.PublicURL[:8] == "https://"}
	a, err := newAuthenticator(ctx, cfg, s)
	if err != nil {
		return nil, err
	}
	auth := http.NewServeMux()
	auth.HandleFunc("GET /auth/login", a.login)
	auth.HandleFunc("GET /auth/callback", a.callback)
	r, err := newRenderer(cfg.AccentColor, cfg.IDPURL)
	if err != nil {
		return nil, err
	}
	return NewServer(Options{Flow: f, Sessions: s, Auth: auth, Render: r, Health: health, TrustForwarded: cfg.TrustForwarded, SessionKey: cfg.SessionKey}), nil
}

func NewServer(o Options) http.Handler {
	s := &server{Options: o, limit: newLimiter()}
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.Handle("GET /auth/", o.Auth)
	mux.HandleFunc("POST /auth/logout", s.member(s.logout))
	mux.HandleFunc("GET /{$}", s.member(s.home))
	mux.HandleFunc("POST /invites", s.member(s.createInvite))
	mux.HandleFunc("POST /invites/{id}/revoke", s.member(s.revoke))
	mux.HandleFunc("GET /admin", s.admin(s.queue))
	mux.HandleFunc("POST /admin/applications/{id}/approve", s.admin(s.approve))
	mux.HandleFunc("POST /admin/applications/{id}/decline", s.admin(s.decline))
	mux.HandleFunc("GET /i/{token}", s.limited(s.invitePage))
	mux.HandleFunc("POST /i/{token}", s.limited(s.inviteSubmit))
	return securityHeaders(mux)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	if err := s.Health.Ping(r.Context()); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok"))
}

type sessionHandler func(http.ResponseWriter, *http.Request, Session)

func (s *server) member(h sessionHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.Sessions.read(r)
		if !ok {
			http.Redirect(w, r, "/auth/login", http.StatusFound)
			return
		}
		if r.Method == http.MethodPost && subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(sess.CSRF)) != 1 {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		h(w, r, sess)
	}
}

func (s *server) admin(h sessionHandler) http.HandlerFunc {
	return s.member(func(w http.ResponseWriter, r *http.Request, sess Session) {
		if !sess.Admin {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		h(w, r, sess)
	})
}

func (s *server) limited(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.limit.allow(clientIP(r, s.TrustForwarded)) {
			http.Error(w, "Too many requests. Please wait a minute.", http.StatusTooManyRequests)
			return
		}
		h(w, r)
	}
}

func (sess Session) member() flow.Member {
	return flow.Member{Sub: sess.Sub, Username: sess.Username, Admin: sess.Admin}
}

func (s *server) logout(w http.ResponseWriter, r *http.Request, _ Session) {
	s.Sessions.clear(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
```

`internal/web/member.go`:

```go
package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/paisans-software/paisans-invite/internal/store"
)

type homeData struct {
	Invites            []store.Invite
	NewLink, Expires   string
	Remaining          string
}

func (s *server) homeView(r *http.Request, sess Session, extra homeData) (homeData, error) {
	invites, err := s.Flow.Invites(r.Context(), sess.member())
	if err != nil {
		return homeData{}, err
	}
	extra.Invites = invites
	if sess.Admin {
		extra.Remaining = "As an admin you can make as many invites as you need."
	} else {
		live := 0
		for _, i := range invites {
			if i.State == "open" || i.State == "applied" {
				live++
			}
		}
		extra.Remaining = fmt.Sprintf("You have %d invite(s) in use of your %d.", live, s.Flow.Quota())
	}
	return extra, nil
}

func (s *server) home(w http.ResponseWriter, r *http.Request, sess Session) {
	d, err := s.homeView(r, sess, homeData{})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Render.page(w, 200, "home.html", view{Title: "Invites", Session: &sess, CSRF: sess.CSRF, Data: d})
}

func (s *server) createInvite(w http.ResponseWriter, r *http.Request, sess Session) {
	link, inv, err := s.Flow.CreateInvite(r.Context(), sess.member())
	v := view{Title: "Invites", Session: &sess, CSRF: sess.CSRF}
	var extra homeData
	switch {
	case errors.Is(err, store.ErrQuota):
		v.Error = "You have reached your invite quota. An invite frees up when it is used, expires or is revoked."
	case err != nil:
		s.fail(w, err)
		return
	default:
		extra = homeData{NewLink: link, Expires: inv.ExpiresAt.Format("2 Jan 2006")}
	}
	d, err := s.homeView(r, sess, extra)
	if err != nil {
		s.fail(w, err)
		return
	}
	v.Data = d
	s.Render.page(w, 200, "home.html", v)
}

func (s *server) revoke(w http.ResponseWriter, r *http.Request, sess Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err == nil {
		err = s.Flow.Revoke(r.Context(), sess.member(), id)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) fail(w http.ResponseWriter, err error) {
	slog.Error("request failed", "error", err)
	s.Render.page(w, 500, "message.html", view{Title: "Something went wrong", Data: map[string]string{
		"Heading": "Something went wrong", "Body": "Please try again in a minute."}})
}
```

Add to `flow.Service`: `func (s *Service) Quota() int { return s.s.Quota }` (with a one-line test in `flow_test.go`).

`internal/web/admin.go`:

```go
package web

import (
	"net/http"
	"strconv"
)

func (s *server) queue(w http.ResponseWriter, r *http.Request, sess Session) {
	p, err := s.Flow.Pending(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	v := view{Title: "Applications", Session: &sess, CSRF: sess.CSRF, Data: p, Error: r.URL.Query().Get("error")}
	s.Render.page(w, 200, "admin.html", v)
}

func (s *server) decide(w http.ResponseWriter, r *http.Request, sess Session, approve bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if approve {
		err = s.Flow.Approve(r.Context(), sess.member(), id)
	} else {
		err = s.Flow.Decline(r.Context(), sess.member(), id)
	}
	if err != nil {
		// The message is the flow's own sentence, never the provider's body;
		// pass it through a query string the template escapes.
		http.Redirect(w, r, "/admin?error="+urlQuery(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *server) approve(w http.ResponseWriter, r *http.Request, sess Session) { s.decide(w, r, sess, true) }
func (s *server) decline(w http.ResponseWriter, r *http.Request, sess Session) { s.decide(w, r, sess, false) }
```

With `func urlQuery(s string) string { return url.QueryEscape(s) }` in `admin.go` (import `net/url`).

- [ ] **Step 4: Run the tests**

Run: `INVITE_TEST_DATABASE_URL=… go test -race ./internal/web/`
Expected: PASS except the invitee routes, which Task 10 adds (`s.invitePage`, `s.inviteSubmit` do not compile yet). To keep this task green, add both as `func (s *server) invitePage(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }` and the same for `inviteSubmit` in a new `internal/web/invitee.go`; Task 10 replaces them.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: member and admin pages"
```

---

### Task 10: Invitee pages

**Files:**
- Modify: `internal/web/invitee.go`
- Create: `internal/web/invitee_test.go`

**Interfaces:**
- Consumes: `flow.Service.OpenInvite`, `flow.Service.Apply`, `flow.Service.CoC`, `flow.TokenHash`, `inviteeCSRF`.

Behaviour:
- `GET /i/{token}`: on any `OpenInvite` error, the neutral page ("This invite can't be used. It may have been used already, expired or been withdrawn. Ask the person who invited you for a new one.") with status 404. Otherwise the form, with `CSRF = inviteeCSRF(SessionKey, tokenHash)` and the inviter's username.
- `POST /i/{token}`: CSRF must equal `inviteeCSRF(SessionKey, tokenHash)` for this token, else 403 and nothing stored. Then `Apply`. `store.ErrNotUsable` gives the neutral page. A validation error re-renders the form, status 422, with the values the person typed and the error's own sentence. Success shows either "Check your email — your sign-in link is on its way." (approved now) or "Thank you. An admin will review your application, and you will get an email." Every response sets `Referrer-Policy: no-referrer` (already global) so the token never leaves in a Referer.

- [ ] **Step 1: Failing tests**

`internal/web/invitee_test.go`:

```go
package web

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-invite/internal/flow"
)

func (h harness) invite(t *testing.T) (string, string) {
	return h.inviteAs(t, flow.Member{Sub: "s-alice", Username: "alice"})
}

func (h harness) inviteAs(t *testing.T, m flow.Member) (string, string) {
	link, _, err := h.flow.CreateInvite(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	tok := link[strings.LastIndex(link, "/")+1:]
	th, _ := flow.TokenHash(tok)
	return tok, inviteeCSRF(make([]byte, 32), th)
}

func post(h harness, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "203.0.113.1:1"
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	return rec
}

func form(csrf string) url.Values {
	return url.Values{"csrf": {csrf}, "agree": {"yes"}, "username": {"bob"}, "display_name": {"Bob"}, "email": {"bob@x.org"}}
}

func TestInvitePageShowsCoC(t *testing.T) {
	h := newHarness(t, true)
	tok, _ := h.invite(t)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, httptest.NewRequest("GET", "/i/"+tok, nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Be kind") || !strings.Contains(rec.Body.String(), "alice") {
		t.Fatalf("%d", rec.Code)
	}
}

func TestBadTokenIsNeutral(t *testing.T) {
	h := newHarness(t, true)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, httptest.NewRequest("GET", "/i/nope", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "can't be used") {
		t.Fatalf("%d", rec.Code)
	}
}

func TestInviteeCSRFBoundToInvite(t *testing.T) {
	h := newHarness(t, true)
	tokA, _ := h.invite(t)
	_, csrfB := h.inviteAs(t, flow.Member{Sub: "s-root", Username: "root", Admin: true}) // admins have no quota
	if rec := post(h, "/i/"+tokA, form(csrfB)); rec.Code != 403 {
		t.Fatalf("csrf from another invite accepted: %d", rec.Code)
	}
	if rec := post(h, "/i/"+tokA, form("")); rec.Code != 403 {
		t.Fatalf("missing csrf accepted: %d", rec.Code)
	}
	if p, _ := h.flow.Pending(context.Background()); len(p) != 0 {
		t.Fatal("nothing may be stored")
	}
}

func TestSubmitAndReuse(t *testing.T) {
	h := newHarness(t, true)
	tok, csrf := h.invite(t)
	if rec := post(h, "/i/"+tok, form(csrf)); rec.Code != 200 || !strings.Contains(rec.Body.String(), "admin will review") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec := post(h, "/i/"+tok, form(csrf)); rec.Code != 404 {
		t.Fatalf("second use must be the neutral page: %d", rec.Code)
	}
}

func TestValidationKeepsInput(t *testing.T) {
	h := newHarness(t, true)
	tok, csrf := h.invite(t)
	f := form(csrf)
	f.Set("username", "-bad")
	rec := post(h, "/i/"+tok, f)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "bob@x.org") || !strings.Contains(rec.Body.String(), "Username must") {
		t.Fatalf("%d", rec.Code)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `INVITE_TEST_DATABASE_URL=… go test ./internal/web/ -run 'Invite|Token|Submit|Validation'`
Expected: FAIL, the stubs answer 404 to everything.

- [ ] **Step 3: Implement `internal/web/invitee.go`**

```go
package web

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/paisans-software/paisans-invite/internal/flow"
	"github.com/paisans-software/paisans-invite/internal/store"
)

type inviteData struct {
	Inviter                      string
	CoC                          any
	Username, DisplayName, Email string
}

func (s *server) unusable(w http.ResponseWriter) {
	s.Render.page(w, http.StatusNotFound, "message.html", view{Title: "Invite", Data: map[string]string{
		"Heading": "This invite can't be used",
		"Body":    "It may have been used already, expired or been withdrawn. Ask the person who invited you for a new one.",
	}})
}

func (s *server) invitePage(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	inv, err := s.Flow.OpenInvite(r.Context(), tok)
	if err != nil {
		s.unusable(w)
		return
	}
	th, _ := flow.TokenHash(tok)
	s.Render.page(w, 200, "invite.html", view{Title: "You have been invited", CSRF: inviteeCSRF(s.SessionKey, th),
		Data: inviteData{Inviter: inv.InviterUsername, CoC: s.Flow.CoC().HTML}})
}

func (s *server) inviteSubmit(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	th, ok := flow.TokenHash(tok)
	if !ok {
		s.unusable(w)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(inviteeCSRF(s.SessionKey, th))) != 1 {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	a := flow.Applicant{
		Username: r.PostFormValue("username"), Email: r.PostFormValue("email"),
		DisplayName: r.PostFormValue("display_name"), Agreed: r.PostFormValue("agree") == "yes",
	}
	now, err := s.Flow.Apply(r.Context(), tok, a)
	switch {
	case errors.Is(err, store.ErrNotUsable):
		s.unusable(w)
	case errors.Is(err, flow.ErrInvalidUsername), errors.Is(err, flow.ErrInvalidEmail), errors.Is(err, flow.ErrInvalidName),
		errors.Is(err, flow.ErrUsernameTaken), errors.Is(err, flow.ErrEmailTaken), errors.Is(err, flow.ErrNotAgreed):
		inv, oerr := s.Flow.OpenInvite(r.Context(), tok)
		if oerr != nil {
			s.unusable(w)
			return
		}
		s.Render.page(w, http.StatusUnprocessableEntity, "invite.html", view{Title: "You have been invited",
			CSRF: inviteeCSRF(s.SessionKey, th), Error: capitalise(err.Error()),
			Data: inviteData{Inviter: inv.InviterUsername, CoC: s.Flow.CoC().HTML, Username: a.Username, DisplayName: a.DisplayName, Email: a.Email}})
	case err != nil:
		s.fail(w, err)
	case now:
		s.Render.page(w, 200, "message.html", view{Title: "Welcome", Data: map[string]string{
			"Heading": "Welcome", "Body": "Check your email — your sign-in link is on its way."}})
	default:
		s.Render.page(w, 200, "message.html", view{Title: "Thank you", Data: map[string]string{
			"Heading": "Thank you", "Body": "An admin will review your application, and you will get an email."}})
	}
}

// capitalise starts a flow error sentence with a capital; they are ASCII.
func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
```

- [ ] **Step 4: Run all tests**

Run: `INVITE_TEST_DATABASE_URL=… go test -race ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: invitee pages — code of conduct, details, and the neutral refusal"
```

---

### Task 11: The binary, the image and the release workflow

**Files:**
- Create: `cmd/paisans-invite/main.go`, `Dockerfile`, `.dockerignore`, `.github/workflows/release.yml`

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Implement `cmd/paisans-invite/main.go`**

```go
// Command paisans-invite serves member invites. See the README for its
// environment.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/paisans-software/paisans-invite/internal/coc"
	"github.com/paisans-software/paisans-invite/internal/config"
	"github.com/paisans-software/paisans-invite/internal/flow"
	"github.com/paisans-software/paisans-invite/internal/idp/pocketid"
	"github.com/paisans-software/paisans-invite/internal/mail"
	"github.com/paisans-software/paisans-invite/internal/store"
	"github.com/paisans-software/paisans-invite/internal/web"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("exiting", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	c, err := coc.Load(cfg.CoCPath)
	if err != nil {
		return err
	}
	sender, err := mail.NewSMTP(cfg.SMTP)
	if err != nil {
		return err
	}
	provider := pocketid.New(cfg.IDPURL, cfg.IDPAPIKey, &http.Client{Timeout: 15 * time.Second})
	f := flow.New(st, provider, sender, c, flow.Settings{
		Quota: cfg.Quota, RequireApproval: cfg.RequireApproval, InviteLifetime: cfg.InviteLifetime,
		PublicURL: cfg.PublicURL, EmailHashKey: cfg.EmailHashKey,
	}, time.Now)
	handler, err := web.New(ctx, cfg, f, st)
	if err != nil {
		return err
	}

	go sweep(ctx, st, cfg)

	srv := &http.Server{Addr: cfg.ListenAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	slog.Info("listening", "addr", cfg.ListenAddr, "coc_version", c.Version, "require_approval", cfg.RequireApproval)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func sweep(ctx context.Context, st *store.Store, cfg config.Config) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		r, err := st.Sweep(ctx, time.Now(), cfg.ApplicationWait, cfg.DeclineRetention)
		if err != nil {
			slog.Warn("sweep failed", "error", err)
		} else if r.Ran {
			slog.Info("sweep", "invites_expired", r.InvitesExpired, "applications_expired", r.ApplicationsExpired, "declines_deleted", r.DeclinesDeleted)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
```

The image has no shell, so the container's health check is the binary itself: add a `healthcheck` subcommand. At the top of `main`, before `run`:

```go
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		addr := os.Getenv("INVITE_LISTEN_ADDR")
		if addr == "" {
			addr = ":8080"
		}
		resp, err := http.Get("http://127.0.0.1" + addr[strings.LastIndex(addr, ":"):] + "/healthz")
		if err != nil || resp.StatusCode != 200 {
			os.Exit(1)
		}
		os.Exit(0)
	}
```

(import `strings`).

- [ ] **Step 2: Dockerfile**

```dockerfile
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /paisans-invite ./cmd/paisans-invite

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /paisans-invite /paisans-invite
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s CMD ["/paisans-invite", "healthcheck"]
ENTRYPOINT ["/paisans-invite"]
```

`.dockerignore`: `.git`, `.worktrees`, `.github`.

- [ ] **Step 3: Release workflow** `.github/workflows/release.yml`

```yaml
# A tag vX.Y.Z publishes ghcr.io/paisans-software/paisans-invite:X.Y.Z; a push
# to develop publishes develop and sha-<short>. The toolkit's invite kind pins
# a released tag.
name: release
on:
  push:
    branches: [develop]
    tags: ["v*"]
  workflow_dispatch:
permissions:
  contents: read
  packages: write
jobs:
  image:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-qemu-action@v3
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - id: meta
        uses: docker/metadata-action@v5
        with:
          images: ghcr.io/paisans-software/paisans-invite
          tags: |
            type=semver,pattern={{version}}
            type=ref,event=branch
            type=sha
      - uses: docker/build-push-action@v6
        with:
          context: .
          platforms: linux/amd64,linux/arm64
          push: true
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
```

- [ ] **Step 4: Build and smoke-run locally**

```bash
go build ./... && go vet ./...
docker build -t paisans-invite:dev .
docker run --rm paisans-invite:dev 2>&1 | head -3
```

Expected: the container exits with a JSON log line naming the missing `INVITE_*` variables, and no value of any variable.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: the binary, its image and the release workflow"
```

---

### Task 12: Contract test against a real Pocket ID

**Files:**
- Create: `internal/idp/pocketid/contract_test.go`, `.github/workflows/contract.yml`, `contract/compose.yaml`

**Interfaces:**
- Consumes: `pocketid.New`.

The test is behind the build tag `contract` and reads `CONTRACT_IDP_URL`, `CONTRACT_API_KEY`, `CONTRACT_MAILPIT_URL`, `CONTRACT_GROUP_ID`.

- [ ] **Step 1: `contract/compose.yaml`**

```yaml
services:
  mailpit:
    image: axllent/mailpit:v1.27
    ports: ["8025:8025"]
  pocket-id:
    image: ghcr.io/pocket-id/pocket-id:v2.18.0
    ports: ["1411:1411"]
    env_file: [pocket-id.env]
    volumes: ["pid-data:/app/data"]
volumes:
  pid-data:
```

- [ ] **Step 2: `.github/workflows/contract.yml`**

```yaml
name: contract
on:
  pull_request:
  push:
    branches: [develop]
jobs:
  contract:
    runs-on: ubuntu-latest
    env:
      KEY: contract-static-api-key-0123456789
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Start Pocket ID and Mailpit
        working-directory: contract
        run: |
          cat > pocket-id.env <<EOF
          APP_URL=http://localhost:1411
          ENCRYPTION_KEY=$(openssl rand -base64 32)
          STATIC_API_KEY=$KEY
          SMTP_HOST=mailpit
          SMTP_PORT=1025
          SMTP_TLS=none
          SMTP_FROM=id@example.org
          EMAIL_ONE_TIME_ACCESS_AS_ADMIN_ENABLED=true
          EOF
          docker compose up -d
          for i in $(seq 60); do curl -fs localhost:1411/healthz && break; sleep 2; done
      - name: Create the default group, then restart Pocket ID with it
        working-directory: contract
        run: |
          id=$(curl -fs -X POST localhost:1411/api/user-groups -H "X-API-Key: $KEY" -H 'Content-Type: application/json' \
               -d '{"name":"members","friendlyName":"Members"}' | jq -r .id)
          echo "SIGNUP_DEFAULT_USER_GROUP_IDS=[\"$id\"]" >> pocket-id.env
          echo "UI_CONFIG_DISABLED=true" >> pocket-id.env
          echo "CONTRACT_GROUP_ID=$id" >> "$GITHUB_ENV"
          docker compose up -d --force-recreate pocket-id
          for i in $(seq 60); do curl -fs localhost:1411/healthz && break; sleep 2; done
      - name: Contract test
        env:
          CONTRACT_IDP_URL: http://localhost:1411
          CONTRACT_API_KEY: ${{ env.KEY }}
          CONTRACT_MAILPIT_URL: http://localhost:8025
        run: go test -tags contract -run Contract -v ./internal/idp/pocketid/
```

The two-step start mirrors what the toolkit's `apply` does: a group's ID exists only once the group does, so Pocket ID is started, the group made, and Pocket ID recreated with its ID.

- [ ] **Step 3: `internal/idp/pocketid/contract_test.go`**

```go
//go:build contract

package pocketid

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/paisans-software/paisans-invite/internal/idp"
)

func TestContract(t *testing.T) {
	base, key := os.Getenv("CONTRACT_IDP_URL"), os.Getenv("CONTRACT_API_KEY")
	c := New(base, key, &http.Client{Timeout: 10 * time.Second})
	ctx := context.Background()
	name := fmt.Sprintf("contract%d", time.Now().UnixNano()%1e9)

	if taken, err := c.UsernameTaken(ctx, name); err != nil || taken {
		t.Fatalf("fresh name: %v %v", taken, err)
	}
	u, err := c.CreateUser(ctx, idp.NewUser{Username: name, Email: name + "@example.org", DisplayName: "Contract"})
	if err != nil {
		t.Fatal(err)
	}
	if taken, _ := c.UsernameTaken(ctx, name); !taken {
		t.Fatal("created user not found by search")
	}
	if taken, _ := c.EmailTaken(ctx, name+"@EXAMPLE.org"); !taken {
		t.Fatal("email search must ignore case")
	}

	// The user landed in the signup default group, with no groups sent.
	req, _ := http.NewRequest(http.MethodGet, base+"/api/users/"+u.ID+"/groups", nil)
	req.Header.Set("X-API-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var groups []struct{ ID string `json:"id"` }
	json.NewDecoder(resp.Body).Decode(&groups)
	resp.Body.Close()
	if len(groups) != 1 || groups[0].ID != os.Getenv("CONTRACT_GROUP_ID") {
		t.Fatalf("want exactly the default group, got %+v", groups)
	}

	if err := c.SendLoginLink(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	// Mailpit received a message addressed to the new user.
	var found bool
	for i := 0; i < 20 && !found; i++ {
		resp, err := http.Get(os.Getenv("CONTRACT_MAILPIT_URL") + "/api/v1/search?query=to:" + name + "@example.org")
		if err == nil {
			var res struct{ Total int `json:"total"` }
			json.NewDecoder(resp.Body).Decode(&res)
			resp.Body.Close()
			found = res.Total > 0
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !found {
		t.Fatal("no login email reached Mailpit")
	}
}
```

- [ ] **Step 4: Run it locally**

Run the two workflow steps by hand in `contract/`, then: `CONTRACT_IDP_URL=http://localhost:1411 CONTRACT_API_KEY=… CONTRACT_MAILPIT_URL=http://localhost:8025 CONTRACT_GROUP_ID=… go test -tags contract -run Contract -v ./internal/idp/pocketid/`
Expected: PASS. If `POST /api/user-groups` needs different fields at v2.18.0, read `backend/internal/dto/user_group_dto.go` at that tag and correct the workflow, not the adapter.

- [ ] **Step 5: Commit, open the pull request**

```bash
git add -A && git commit -m "test: contract test against Pocket ID v2.18.0 and Mailpit"
git push
```

The first tasks pushed straight to `develop` while the repository was empty. Before this task, ask the founder to protect `main` and `develop` as `paisans-stack`'s are (pull requests only, no bypass); from then on every change goes through a branch and a pull request into `develop`. Opening a pull request is allowed when asked; posting comments or reviews is never.

---

### Task 13: First release

- [ ] **Step 1:** With CI green on `develop`, ask the founder to approve `v0.1.0`. On approval: open a pull request `develop → main` whose body lists the `[Unreleased]` bullets; once merged, move `[Unreleased]` to `## [0.1.0] - <date>` on `main` in that same pull request, tag `v0.1.0` on the merge commit and push the tag. The release workflow publishes `ghcr.io/paisans-software/paisans-invite:0.1.0`.
- [ ] **Step 2:** Confirm the manifest exists anonymously: `docker manifest inspect ghcr.io/paisans-software/paisans-invite:0.1.0` lists `linux/amd64` and `linux/arm64`. Record the tag; the toolkit plan pins it.
