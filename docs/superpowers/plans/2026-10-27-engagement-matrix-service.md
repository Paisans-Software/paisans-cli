# The Digest Bot in paisans-engagement-service: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The engagement service sends each Matrix-qualifying member a Monday `m.notice` DM from a Digest Bot account, listens for `stop`/`start` and room changes, and serves each sent digest as a frozen, signed, expiring web page at `/d/{token}`.

**Architecture:** Two new HTTP clients over plain `net/http` + `encoding/json`: `internal/mas` (MAS admin API, client credentials) and `internal/matrix` (Synapse client-server API, paced, 429-aware). `internal/bot` owns the bot account, rooms, delivery and the `/sync` listener, and runs only on the copy holding a new Postgres advisory lock. `internal/run` gains a frozen digest per member per week (built once, stored, shared by email, DM and web page) and a second, Matrix pass after the email pass.

**Tech Stack:** Go 1.25.8, pgx/v5, go-mail (unchanged), `golang.org/x/time/rate` (new; token buckets that match Synapse's own `per_second`/`burst_count` model), `net/http/httptest` fakes for MAS and Synapse.

**Spec:** `docs/specs/2026-10-27-engagement-matrix.md` in the paisans-cli worktree (`src/paisans-stack/.worktrees/engagement-matrix`), extending `docs/specs/2026-10-24-engagement-digest.md`. Names are bound by `docs/superpowers/plans/2026-10-27-engagement-matrix-contract.md` in the same worktree: do not rename anything it names.

**Worktree:** `/Users/wash/Developer/paisans.community/src/paisans-engagement-service/.worktrees/matrix-bot`, branch `feat/matrix-bot`. Every path below is relative to it.

## Why plain HTTP for Matrix

The bot uses nine client-server endpoints (`auth_metadata`, `whoami`, `profile`, `createRoom`, room state, `send`, `leave`, `forget`, `sync`), carries no encryption, and has to own its pacing and 429 handling anyway. A typed client of about 300 lines over `net/http` is smaller than any dependency and holds no crypto code at all, which is what the spec's non goal asks for.

## What was verified against MAS and Synapse source

Read with `gh api .../contents/...?ref=<tag>` on 2026-10-27. Each implementation task pins these in a comment beside the call, in the form the synapse blueprint uses ("read from `<path>` at tag `<tag>`").

MAS, tag **v1.28.0** (`crates/handlers/src/admin/v1/mod.rs`, `.../model.rs`, `.../params.rs`, `.../call_context.rs`):

| Use | Endpoint | Shape |
|---|---|---|
| version check | `GET /api/admin/v1/version` | `{"version":"v1.28.0"}` (`VERGEN_GIT_DESCRIBE`, so may carry `-N-gHASH`) |
| member lookup | `GET /api/admin/v1/upstream-oauth-links?filter[provider]=<ulid>&filter[subject]=<sub>&page[first]=1` | JSON:API page: `data[].attributes.{provider_id,subject,user_id(nullable),human_account_name}`; 404 if the provider id is unknown |
| link of a user | same, `filter[user]=<ulid>&filter[provider]=<ulid>` | as above |
| user by id | `GET /api/admin/v1/users/{id}` | `data.id`, `data.attributes.{username,created_at,locked_at,deactivated_at,admin,legacy_guest}`; 404 |
| user by localpart | `GET /api/admin/v1/users/by-username/{username}` | as above; 404 |
| bot creation | `POST /api/admin/v1/users` `{"username":...}` | 201; 409 for "User already exists" and "Username is reserved by the homeserver"; 400 invalid |
| bot token | `POST /api/admin/v1/personal-sessions` `{"actor_user_id","human_name","scope"}` (no `expires_in` = no expiry) | 201, `data.id`, `data.attributes.access_token` (only on create); a `urn:matrix:client:device:<id>` scope makes MAS provision that device in Synapse |
| token renewal | `POST /api/admin/v1/personal-sessions/{id}/regenerate` `{}` | 201 with a new `access_token`; 404/403/422 when the session is gone, not ours, or revoked |
| auth | every admin route needs a bearer token whose session has `urn:mas:admin` (else 401) | client credentials token from the OAuth token endpoint |

**Oldest MAS release with all of them: v1.5.0** (personal sessions arrive there; `version` since v1.4.0; users, by-username and the subject-filtered link list since v1.0.0; `personal_sessions/add.rs` at v1.5.0 provisions devices identically to v1.28.0). `POST /users` only accepts `displayname` from v1.18.0, so the bot sets its display name through the client-server profile API instead, which keeps the floor at v1.5.0.

Synapse, tag **v1.160.0** (the version the synapse blueprint pins):

* `synapse/api/auth/mas.py`: a token without `urn:matrix:client:api:*` is refused; the device id comes from a `urn:matrix:client:device:` scope, and with none the requester has no device and no access token id.
* `synapse/rest/client/transactions.py` `_get_transaction_key`: a requester with neither device id nor access token id hits `assert requester.access_token_id is not None`, so **`PUT /send/{txnId}` returns 500 for a token scoped `urn:matrix:client:api:*` alone.** The bot token is therefore scoped `urn:matrix:client:api:* urn:matrix:client:device:DIGESTBOT` (see Global Constraints). The fixed device also keys Synapse's persisted transaction dedup (`event_txn_id_device_id`), so a retried send is deduplicated across token renewals and restarts.
* `synapse/rest/client/auth_metadata.py`: `GET /_matrix/client/v1/auth_metadata` serves MAS's OAuth metadata, including `token_endpoint`. MAS's admin listener carries `adminapi` only, so the client credentials token comes from `/oauth2/token` on MAS's public web listener, which shares the homeserver's hostname (`ENGAGEMENT_MATRIX_URL`); the service reads its exact URL from this metadata rather than assuming the path, then uses the token against `ENGAGEMENT_MAS_ADMIN_URL` for `/api/admin/v1/*`. The catalog side confirms from MAS v1.28.0: static clients get every grant type, and `policy.data.admin_clients` grants `urn:mas:admin`.
* MAS grants `urn:matrix:client:api:*` only to interactive grants, so a client credentials token can never act as the bot; the bot's token comes from `POST /api/admin/v1/personal-sessions`, which is what sets the v1.5.0 floor.
* `synapse/handlers/room.py`: `createRoom` takes from both the general request limiter (`rc_message`) and `rc_room_creation`.
* `docs/usage/configuration/config_documentation.md` defaults: `rc_message` 0.2/s burst 10; `rc_room_creation` 0.016/s burst 10; `rc_invites.per_issuer` 0.3/s burst 10.

## Global Constraints

* Environment, exactly: `ENGAGEMENT_MATRIX_URL`, `ENGAGEMENT_MATRIX_SERVER_NAME`, `ENGAGEMENT_MAS_ADMIN_URL`, `ENGAGEMENT_MAS_CLIENT_ID`, `ENGAGEMENT_MAS_CLIENT_SECRET`, `ENGAGEMENT_MAS_UPSTREAM_PROVIDER`, `ENGAGEMENT_MATRIX_BOT_LOCALPART`. The bot starts only when all seven are set; otherwise one log line says Matrix is off. A service with none of them behaves exactly as v0.1.0.
* Bot localpart validated as `^[a-z0-9._=/-]+$` (the contract's rule). Display name `Digest Bot`. Device id `DIGESTBOT`.
* Bot token scope: `urn:matrix:client:api:* urn:matrix:client:device:DIGESTBOT`. Never `urn:synapse:admin:*`, never `urn:mas:admin`. The admin client token scope is `urn:mas:admin` only.
* MAS floor: `v1.5.0`. Older, or a version that does not parse: log once that Matrix is off and name the version.
* Pacing defaults: messages 0.2/s burst 10 (sends and command replies share it; `createRoom` also takes one); rooms 0.016/s burst 10. A 429 waits `retry_after_ms` from the body, else `Retry-After` seconds, else 5s, and is never a failure.
* Monday notice, plain body exactly:
  ```
  Your weekly digest from <meta title>

  • <thread title>
  • <thread title>
  • <thread title>

  Read the whole digest: <web digest link>

  Reply stop to stop these messages.
  ```
  `msgtype` `m.notice`, `format` `org.matrix.custom.html`, `formatted_body` the same text with each title an `<a href>` to its thread and the link an `<a href>`.
* Replies, exactly: stop `Stopped. Reply start to get them again.`; start/resume/restore `Started. Your next digest comes on Monday.`; anything else `Reply stop to stop these messages, or start to get them again.` All sent as `m.notice`.
* Web digest: `GET` and `HEAD /d/{token}` only; headers `Referrer-Policy: no-referrer`, `X-Robots-Tag: noindex`, `Cache-Control: private, no-store`; expires 28 days after the send time, row deleted then; any bad, unknown or expired token gets the same 404 and the same body as a bad unsubscribe link.
* The service's README and code comments describe current state only. No rejected alternatives in README, comments, commits or the PR.
* Nothing that names a member (OIDC subject, Matrix id, email) reaches a log line; log Mbin member ids as today. Tokens and secrets never reach a log line or an error string.
* Commits: one sentence subject in the repo's existing style (no conventional prefix), ending with the session's attribution trailer. Never `git stash`.
* Tests: `go test ./...` with `ENGAGEMENT_TEST_DATABASE_URL` set (setup below). Before each commit: `gofmt -l .` prints nothing and `go vet ./...` passes; CI runs only the changelog check.

### Postgres for tests

A dedicated container exists on this Mac (created for this plan, separate from `mbin-tests-db`):

```bash
docker start engagement-test-pg 2>/dev/null || docker run -d --name engagement-test-pg \
  -p 127.0.0.1:55434:5432 -e POSTGRES_PASSWORD=test -e POSTGRES_DB=engagement_test postgres:18
export ENGAGEMENT_TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:55434/engagement_test?sslmode=disable'
```

The whole v0.1.0 suite passes against it today. Every test command below assumes the variable is exported; a test that reports `SKIP ... ENGAGEMENT_TEST_DATABASE_URL is not set` has not run.

## Review Focus

1. **A send retried after an unknown outcome.** The PUT reached Synapse, the service crashed before `FinishDM`. The retry must use the identical path and txnId so Synapse deduplicates it; the member sees one message. (Test: Task 11 `TestDeliverRetryReusesTxnID`.)
2. **Thread titles with markup.** A title like `<b>Tom & Jerry</b> [x](y)` must be escaped in `formatted_body` and appear verbatim in `body`. (Test: Task 11 `TestNoticeEscapesTitles`.)
3. **Commands as people type them.** `STOP!`, `  Start. `, `"resume"`, `stop please`, an emoji, an empty body. The first three act, the rest get the help line. (Test: Task 12 `TestCommandParsing`.)
4. **Rate limits without `retry_after_ms`.** A 429 carrying only `Retry-After: 2`, or neither, must still wait and retry, not count as a failure. (Test: Task 7 `TestRateLimitedWithoutBodyHint`.)
5. **A send week near a daylight saving change, in a non-UTC zone.** The token's week date, the `web_digests` row and the `dm_sends` row must agree on the Monday's local date. (Test: Task 15 `TestWeekDateAgreesInLocalZone`.)

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/config/config.go` | `Matrix` block: the seven variables, `Enabled`, `Missing`, validation |
| `internal/token/token.go` | `SignDigest` / `VerifyDigest` beside the unsubscribe token |
| `internal/store/migrations/0003_matrix.sql` | `matrix_members`, `dm_sends`, `bot_state` |
| `internal/store/migrations/0004_web_digests.sql` | `web_digests` |
| `internal/store/matrix.go` | member setting, account, room; bot state |
| `internal/store/dm.go` | DM claims |
| `internal/store/webdigest.go` | frozen digests, expiry, purge |
| `internal/store/lock.go` | the bot lock |
| `internal/store/digest.go` | `Unsubscribe` and `Shown` changes |
| `internal/mbin/queries.go`, `mbin.go` | members without a verified email; `IsMember` |
| `internal/matrix/client.go` | transport, errors, 429, pacing |
| `internal/matrix/rooms.go` | rooms, state, send, leave, sync |
| `internal/matrix/matrixtest/server.go` | fake Synapse |
| `internal/mas/client.go` | MAS admin client |
| `internal/mas/mastest/server.go` | fake MAS |
| `internal/bot/start.go` | version check, bot account, token, display name |
| `internal/bot/deliver.go` | `Prepare`, rooms, `Deliver`, `TxnID` |
| `internal/bot/notice.go` | the Monday notice and the replies |
| `internal/bot/listen.go` | `/sync` loop, commands, membership, invites |
| `internal/bot/supervise.go` | lock, `Holder`, start/retry/off |
| `internal/mail/render.go`, `templates/digest.html` | `RenderPage` |
| `internal/web/server.go` | `/d/{token}`, `Options` |
| `internal/run/run.go` | frozen digest, email pass, Matrix pass, purge |
| `cmd/paisans-engagement-service/main.go` | wiring |
| `README.md`, `CHANGELOG.md` | docs |

---

### Task 1: Matrix configuration

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces:
  ```go
  type Matrix struct {
      URL, ServerName, MASAdminURL, ClientID, ClientSecret, UpstreamProvider, BotLocalpart string
  }
  func (m Matrix) Enabled() bool      // all seven non-empty
  func (m Matrix) Missing() []string  // names of the unset variables, in the table's order
  // Config gains: Matrix Matrix
  ```

- [ ] **Step 1: Write the failing tests** in `config_test.go`, reusing its existing env helper:
  - `TestMatrixOffWithoutVariables`: base env only → `err == nil`, `!c.Matrix.Enabled()`, `len(c.Matrix.Missing()) == 7`.
  - `TestMatrixOnWithAllSeven`: all seven set (`https://matrix.example.org`, `example.org`, `http://10.0.0.5:18010`, a 26 char ULID, `s3cret`, a 26 char ULID, `digest-bot`) → `Enabled()`, fields equal inputs.
  - `TestMatrixPartialIsOff`: six set, secret missing → `err == nil`, `!Enabled()`, `Missing() == []string{"ENGAGEMENT_MAS_CLIENT_SECRET"}`.
  - `TestMatrixValidation` (table, all seven set, one bad each, `err` must name the variable): `ENGAGEMENT_MATRIX_URL=matrix.example.org` (no scheme), `=https://m.example.org/` (trailing slash), same two for `ENGAGEMENT_MAS_ADMIN_URL`, `ENGAGEMENT_MATRIX_BOT_LOCALPART=Digest` and `=digest bot`, `ENGAGEMENT_MAS_UPSTREAM_PROVIDER=nope`, `ENGAGEMENT_MAS_CLIENT_ID=nope`, `ENGAGEMENT_MATRIX_SERVER_NAME` containing `\n`.

- [ ] **Step 2:** `go test ./internal/config/` → FAIL (`c.Matrix undefined`).

- [ ] **Step 3: Implement.** Read the seven with `getenv`. Validate only when `Enabled()`: the two URLs with the existing http(s)/no-trailing-slash loop; localpart against `^[a-z0-9._=/-]+$`; client id and provider as 26 characters of Crockford base32 (`^[0-9A-HJKMNP-TV-Z]{26}$`, upper case, which is what `stableULID` prints); no line breaks in any of the seven. A doc comment on `Matrix` says the bot runs only when all seven are set.

- [ ] **Step 4:** `go test ./internal/config/` → PASS.

- [ ] **Step 5: Commit** `Read the seven Matrix variables, and turn the bot on only when all are set`.

---

### Task 2: The web digest token

**Files:**
- Modify: `internal/token/token.go`
- Test: `internal/token/token_test.go`

**Interfaces:**
- Produces:
  ```go
  func SignDigest(key []byte, subject string, week time.Time) string
  // VerifyDigest returns the subject and the week's local date as YYYY-MM-DD.
  func VerifyDigest(key []byte, tok string) (subject, weekDate string, ok bool)
  ```
  Payload is base64url(`subject + "\x00" + week.Format("2006-01-02")`), MAC is HMAC-SHA256 over `"digest\x00" + subject + "\x00" + date`. Same `strict` decoding and CR/LF refusal as `Verify`.

- [ ] **Step 1: Write the failing tests:**
  ```go
  func TestDigestRoundTrip(t *testing.T) {
      wk := time.Date(2026, 10, 26, 8, 0, 0, 0, time.FixedZone("x", -5*3600))
      sub, date, ok := VerifyDigest(key, SignDigest(key, "sub-1", wk))
      if !ok || sub != "sub-1" || date != "2026-10-26" { t.Fatal(sub, date, ok) }
  }
  func TestDigestAndUnsubscribeDoNotCross(t *testing.T) {
      if _, ok := Verify(key, SignDigest(key, "sub-1", wk)); ok { t.Fatal("digest token read as unsubscribe") }
      if _, _, ok := VerifyDigest(key, Sign(key, "sub-1")); ok { t.Fatal("unsubscribe token read as digest") }
  }
  ```
  Also `TestDigestAltered`: flipping one payload character to name `sub-2`, changing the date, a wrong key, an empty subject, a bad date (`2026-13-40`), a missing NUL, CR/LF anywhere → `ok == false`.

- [ ] **Step 2:** `go test ./internal/token/` → FAIL.
- [ ] **Step 3: Implement** in `token.go`, splitting the payload at the last NUL and checking the date with `time.Parse("2006-01-02", ...)`. Update the package comment: it signs the unsubscribe links and the web digest links.
- [ ] **Step 4:** `go test ./internal/token/` → PASS.
- [ ] **Step 5: Commit** `Sign web digest links with their own purpose, member and week`.

---

### Task 3: Store: each member's Matrix setting, and the bot's own state

**Files:**
- Create: `internal/store/migrations/0003_matrix.sql`, `internal/store/matrix.go`, `internal/store/matrix_test.go`
- Modify: `internal/store/digest.go` (`Unsubscribe`, `Shown`), every caller of them (`internal/web/server.go`, `internal/run/run.go`) with the minimal signature change
- Test: `internal/store/digest_test.go`

**Interfaces:**
- Produces:
  ```go
  type MatrixMember struct {
      Subject   string
      Enabled   bool   // a member with no row is on
      MASUserID string // "" until looked up
      UserID    string // @localpart:server_name, "" until looked up
      RoomID    string // "" when there is no current room
  }
  func (s *Store) MatrixMember(ctx context.Context, subject string) (MatrixMember, error)
  func (s *Store) MatrixMemberByRoom(ctx context.Context, roomID string) (MatrixMember, bool, error)
  func (s *Store) MatrixMemberByUser(ctx context.Context, userID string) (MatrixMember, bool, error)
  func (s *Store) SetMatrixAccount(ctx context.Context, subject, masUserID, userID string) error
  func (s *Store) SetMatrixRoom(ctx context.Context, subject, roomID string) error
  func (s *Store) ForgetMatrixRoom(ctx context.Context, subject, roomID string) error // only if it is still that room
  func (s *Store) SetMatrixEnabled(ctx context.Context, subject string, on bool) error
  func (s *Store) LeftMatrix(ctx context.Context, subject, roomID string) error      // off and room forgotten, one transaction
  func (s *Store) BotState(ctx context.Context, key string) (string, bool, error)
  func (s *Store) SetBotState(ctx context.Context, key, value string) error
  func (s *Store) DeleteBotState(ctx context.Context, key string) error
  // Changed:
  func (s *Store) Unsubscribe(ctx context.Context, subject string, matrix bool) error
  func (s *Store) Shown(ctx context.Context, subject string, before time.Time) (map[Item]bool, error) // weeks strictly before `before`'s date
  ```
- Migration `0003_matrix.sql`:
  ```sql
  CREATE TABLE matrix_members (
    subject text PRIMARY KEY,
    enabled boolean NOT NULL DEFAULT true,
    mas_user_id text,
    user_id text UNIQUE,
    room_id text UNIQUE,
    at timestamptz NOT NULL DEFAULT now()
  );
  CREATE TABLE dm_sends (
    subject text NOT NULL,
    week date NOT NULL,
    status text NOT NULL CHECK (status IN ('pending','sent','failed')),
    at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (subject, week)
  );
  CREATE TABLE bot_state (key text PRIMARY KEY, value text NOT NULL);
  ```
  (`dm_sends` is used from Task 4; it lives here so all Matrix tables arrive in one migration.)

**The shown-threads rule** (spec: forgotten only when both channels are off): `Unsubscribe(ctx, subject, matrix)` deletes `shown` rows when `!matrix` (the deployment has no homeserver) or the member's Matrix setting is off. `SetMatrixEnabled(…, false)` and `LeftMatrix` delete them when the member is in `unsubscribes`. Each in one transaction.

- [ ] **Step 1: Write the failing tests** in `matrix_test.go` (Postgres):
  - `TestMatrixMemberDefaultsOn`: unknown subject → `Enabled == true`, ids empty.
  - `TestMatrixAccountAndRoom`: `SetMatrixAccount("sub-1","01J…","@one:example.org")`, `SetMatrixRoom("sub-1","!r:example.org")` → `MatrixMemberByRoom("!r:example.org")` and `MatrixMemberByUser("@one:example.org")` both return sub-1 with all fields.
  - `TestForgetRoomOnlyIfCurrent`: room `!a`, `ForgetMatrixRoom("sub-1","!b")` keeps `!a`; `ForgetMatrixRoom("sub-1","!a")` clears it.
  - `TestLeftMatrix`: room set, `LeftMatrix` → `Enabled == false`, `RoomID == ""`.
  - `TestShownForgottenOnlyWhenBothOff`: record shown for sub-1 (via `Claim`+`Finish`), then
    `Unsubscribe(ctx,"sub-1",true)` → shown kept; `SetMatrixEnabled("sub-1",false)` → shown gone. Second subject: `SetMatrixEnabled(false)` first → kept; `Unsubscribe(…,true)` → gone. Third: `Unsubscribe(…,false)` → gone at once.
  - `TestBotStateRoundTrip`: missing key → `found == false`; set, overwrite, delete.
  - In `digest_test.go`: `TestShownBeforeWeek`: shown rows for 2026-10-19 and 2026-10-26 → `Shown(ctx,"sub-1", week 2026-10-26)` returns only the 10-19 items. Update existing callers in tests to the new signatures (`Unsubscribe(ctx, s, false)` keeps v0.1.0 behaviour).
- [ ] **Step 2:** `go test ./internal/store/` → FAIL.
- [ ] **Step 3: Implement** `matrix.go` (upserts with `INSERT … ON CONFLICT (subject) DO UPDATE`, `at=now()`), the migration, and the two changed methods. `internal/web/server.go` passes `false` for now (Task 14 wires the real value); `run.go` passes `week` to `Shown`.
- [ ] **Step 4:** `go test ./...` → PASS.
- [ ] **Step 5: Commit** `Keep each member's Matrix setting, account and room, and the bot's own state`.

---

### Task 4: Store: DM claims and frozen digests

**Files:**
- Create: `internal/store/dm.go`, `internal/store/webdigest.go`, `internal/store/migrations/0004_web_digests.sql`, `internal/store/dm_test.go`, `internal/store/webdigest_test.go`

**Interfaces:**
- Produces:
  ```go
  func (s *Store) ClaimDM(ctx context.Context, subject string, week time.Time) (bool, error)
  func (s *Store) FinishDM(ctx context.Context, subject string, week time.Time, status Status, shown ...Item) error // StatusSent or StatusFailed
  func (s *Store) ReleaseDM(ctx context.Context, subject string, week time.Time) error

  const DigestLifetime = 28 * 24 * time.Hour
  // SaveDigest stores content as the member's digest for the week unless one is already
  // stored, and returns whichever is stored. expires_at = week + DigestLifetime.
  func (s *Store) SaveDigest(ctx context.Context, subject string, week time.Time, content []byte) ([]byte, error)
  func (s *Store) SavedDigest(ctx context.Context, subject string, week time.Time) ([]byte, bool, error)
  func (s *Store) DigestsForWeek(ctx context.Context, week time.Time) (map[string][]byte, error)
  // WebDigest is the stored digest only while now is before its expiry.
  func (s *Store) WebDigest(ctx context.Context, subject, weekDate string, now time.Time) ([]byte, bool, error)
  func (s *Store) PurgeDigests(ctx context.Context, now time.Time) (int64, error)
  ```
- Migration `0004_web_digests.sql`:
  ```sql
  -- Each member's digest for a week as it was sent, for the web page. Deleted at expiry.
  CREATE TABLE web_digests (
    subject text NOT NULL,
    week date NOT NULL,
    content jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (subject, week)
  );
  CREATE INDEX web_digests_expires_at ON web_digests (expires_at);
  ```
  All `week` arguments use the existing `weekDate` (local calendar date).

- [ ] **Step 1: Write the failing tests:**
  - `TestClaimDMOnce` / `TestFinishDMRecordsShown` / `TestReleaseDMOnlyPending`: mirror `TestClaimOnlyOnce`, `TestFinishRecordsShown`, `TestReleaseOnlyPending`, against `dm_sends`; a DM claim and an email claim for the same member and week are independent (`Claim` true and `ClaimDM` true).
  - `TestSaveDigestKeepsFirst`: `SaveDigest(a)` then `SaveDigest(b)` → both return `a` (compare as JSON, since jsonb normalises whitespace).
  - `TestWebDigestExpires`: saved for week W; `WebDigest(…, W+27d)` found; `WebDigest(…, W+28d)` not found; `PurgeDigests(W+28d)` returns 1 and `SavedDigest` then finds nothing.
  - `TestDigestsForWeek`: two subjects in W, one in W-7 → map of the two.
- [ ] **Step 2:** `go test ./internal/store/` → FAIL.
- [ ] **Step 3: Implement.** `SaveDigest` is `INSERT … ON CONFLICT DO NOTHING` then `SELECT content`.
- [ ] **Step 4:** `go test ./internal/store/` → PASS.
- [ ] **Step 5: Commit** `Record each week's DM apart from the email, and keep each sent digest for four weeks`.

---

### Task 5: Store: the bot lock

**Files:**
- Create: `internal/store/lock.go`, `internal/store/lock_test.go`

**Interfaces:**
- Produces:
  ```go
  // botLock is held by the one copy that runs the Digest Bot.
  const botLock = 0x656e676167650002 // "engage\x00\x02"
  type Lock struct{ /* pinned *pgxpool.Conn, lost chan */ }
  // TryBotLock takes the bot lock on a connection of its own. ok is false when another
  // session holds it. The lock lasts until Release or until the connection fails,
  // which closes Lost.
  func (s *Store) TryBotLock(ctx context.Context) (l *Lock, ok bool, err error)
  func (l *Lock) Lost() <-chan struct{}
  func (l *Lock) Release()
  ```
  Implementation: `pool.Acquire`, `SELECT pg_try_advisory_lock($1)`; a goroutine pings the connection every 15 seconds (`SELECT 1`) and closes `lost` on failure; `Release` runs `pg_advisory_unlock`, and on failure closes the connection as `Migrate` does.

- [ ] **Step 1: Write the failing tests:** `TestBotLockExclusive`: take the lock with `s.TryBotLock` (ok true); a second `s.TryBotLock` → ok false (it runs on another pooled connection, so another session); from `storetest.Conn(t, s)`, `SELECT pg_try_advisory_lock($1)` with the same key → false; after `Release`, `s.TryBotLock` → ok true. `TestBotLockLost`: take it, terminate its backend from the second connection (`SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND objid=…`), expect `Lost()` closed within 20s.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** `go test ./internal/store/ -run BotLock` → PASS.
- [ ] **Step 5: Commit** `Hold one lock for the copy that runs the Digest Bot`.

---

### Task 6: Mbin: members without a verified email, and IsMember

**Files:**
- Modify: `internal/mbin/queries.go`, `internal/mbin/mbin.go`, `internal/digest/digest.go` (comment on `Member.Email`)
- Test: `internal/mbin/mbin_test.go`

**Interfaces:**
- Produces:
  - `memberRule` drops `u.is_verified` and `u.email IS NOT NULL`; `membersSQL` selects `CASE WHEN u.is_verified THEN u.email END` (scanned into a `*string`), so `digest.Member.Email` is `""` for a member who cannot be mailed.
  - `func (s *Source) IsMember(ctx context.Context, subject string) (bool, error)`: `SELECT EXISTS (SELECT 1 FROM "user" u WHERE u.oauth_oidc_id = $1 AND <memberRule>)`.
  - The allowlist is unchanged.

- [ ] **Step 1: Update `TestMembersRule`**: users 3 (`is_verified=false`) and 8 (`email=nil`) are now members with `Email: ""`; the rest unchanged. Add `TestIsMember`: user 1's subject → true; user 4 (banned) → false; `"nobody"` → false. `TestQueriesStayInsideAllowlist` must still pass with the new statement added to its list.
- [ ] **Step 2:** `go test ./internal/mbin/` → FAIL.
- [ ] **Step 3: Implement.** Update the `memberRule` comment: a member is a local, live person with a Pocket ID subject; an address is mailed only when verified.
- [ ] **Step 4:** `go test ./internal/mbin/ ./internal/run/` → PASS (run's fake members all have emails, so its tests are unaffected).
- [ ] **Step 5: Commit** `Count members without a verified address, who can still get the digest on Matrix`.

---

### Task 7: Matrix client: transport, errors, 429 and pacing, and the fake Synapse

**Files:**
- Create: `internal/matrix/client.go`, `internal/matrix/client_test.go`, `internal/matrix/matrixtest/server.go`
- Modify: `go.mod`, `go.sum` (`go get golang.org/x/time@latest`)

**Interfaces:**
- Produces:
  ```go
  type Pacing struct {
      MessagesPerSecond float64; MessageBurst int
      RoomsPerSecond    float64; RoomBurst    int
  }
  // DefaultPacing is Synapse's default rc_message and rc_room_creation (docs/usage/
  // configuration/config_documentation.md at tag v1.160.0).
  var DefaultPacing = Pacing{0.2, 10, 0.016, 10}

  func New(baseURL string, hc *http.Client, p Pacing) *Client
  func (c *Client) SetToken(token string)          // safe for concurrent use
  // Error is a refusal: a 4xx other than 429.
  type Error struct{ Status int; Code, Message string }
  var ErrUnavailable = errors.New("homeserver unavailable") // network error, timeout, 5xx
  func IsCode(err error, code string) bool          // e.g. "M_UNKNOWN_TOKEN", "M_FORBIDDEN", "M_NOT_FOUND"

  func (c *Client) AuthMetadata(ctx context.Context) (tokenEndpoint string, err error) // GET /_matrix/client/v1/auth_metadata, no token
  func (c *Client) WhoAmI(ctx context.Context) (userID, deviceID string, err error)
  func (c *Client) DisplayName(ctx context.Context, userID string) (string, error)    // "" on 404
  func (c *Client) SetDisplayName(ctx context.Context, userID, name string) error
  ```
  Internals: `do(ctx, method, path string, limiters []*rate.Limiter, body, out any) error`. Each attempt waits on its limiters, sends with `Authorization: Bearer`, and on 429 sleeps (`c.sleep`, a field defaulting to a ctx-aware timer, overridable in tests) for `retry_after_ms`, else `Retry-After` seconds, else 5s, then tries again with no attempt limit beyond `ctx`. Path segments are escaped with `url.PathEscape`. Error strings carry method, path template and status/errcode, never the token or a request body.

- `matrixtest.Server` (used by Tasks 8, 10–12, 15): `New(t, serverName string) *Server` with an embedded `*httptest.Server`. Fields and methods the later tasks use:
  ```go
  Token          string        // the bearer it accepts; others get 401 M_UNKNOWN_TOKEN
  Device         string        // whoami's device_id
  TokenEndpoint  string        // what auth_metadata returns
  Down           atomic.Bool   // every request answers 503
  func (s *Server) RateLimitNext(n int, retryAfterMS int, header string) // next n requests get 429
  func (s *Server) Sends() []Sent       // Sent{Room, TxnID string; Content map[string]any}, deduplicated by (room, txn)
  func (s *Server) SendAttempts() int   // PUTs received, duplicates included
  func (s *Server) Rooms() []Room       // Room{ID string; Request map[string]any; Encrypted bool; Left bool; Forgotten bool}
  func (s *Server) EncryptNewRooms(bool)
  func (s *Server) SetMembership(room, user, membership string)
  func (s *Server) RefuseSend(room string, status int, errcode string)
  func (s *Server) QueueSync(next string, body string) // served in order to /sync; then blocks until ctx or timeout with an empty response
  func (s *Server) SyncSince() []string                // the since values received
  func (s *Server) Profile(user string) string
  ```

- [ ] **Step 1: Write the failing tests** (`client_test.go`, against `matrixtest`, pacing set high: `Pacing{1000,1000,1000,1000}` unless the test is about pacing):
  - `TestWhoAmI`: returns user and device; the request carried `Authorization: Bearer <token>`.
  - `TestUnknownToken`: wrong token → `IsCode(err,"M_UNKNOWN_TOKEN")`, `errors.Is(err, ErrUnavailable) == false`, and `err.Error()` does not contain the token.
  - `TestUnavailable`: `Down` → `errors.Is(err, ErrUnavailable)`; a closed server (`srv.Close()`) → same.
  - `TestRateLimitedRetries`: `RateLimitNext(2, 1500, "")` → call succeeds; recorded sleeps are `[1500ms, 1500ms]`.
  - `TestRateLimitedWithoutBodyHint` (Review Focus 4): `RateLimitNext(1, 0, "2")` → sleep `2s`; `RateLimitNext(1, 0, "")` → sleep `5s`; both calls succeed.
  - `TestPacingSpacesSends`: `Pacing{MessagesPerSecond: 20, MessageBurst: 1, …}`, three `SetDisplayName` calls are not paced (profile is not a message), but three `do` calls with the message limiter take at least 100ms. (Test the limiter through an unexported helper in the same package.)
  - `TestAuthMetadataNoToken`: returns `TokenEndpoint`; request carried no `Authorization` header.
- [ ] **Step 2:** `go test ./internal/matrix/...` → FAIL.
- [ ] **Step 3: Implement** `client.go` and `matrixtest/server.go`. The client's `http.Client` default timeout is 60s; the sync call (Task 8) sets its own deadline.
- [ ] **Step 4:** `go test ./internal/matrix/...` → PASS.
- [ ] **Step 5: Commit** `Talk to the homeserver over plain HTTP, paced to Synapse's default limits and waiting out every 429`.

---

### Task 8: Matrix client: rooms, sends and sync

**Files:**
- Create: `internal/matrix/rooms.go`, `internal/matrix/rooms_test.go`
- Modify: `internal/matrix/matrixtest/server.go`

**Interfaces:**
- Consumes: Task 7's `Client`, `do`, `Error`, `ErrUnavailable`, `matrixtest.Server`.
- Produces:
  ```go
  // CreateDirectRoom creates the bot's room for one member. It takes from both limiters,
  // as Synapse's createRoom does (synapse/handlers/room.py at tag v1.160.0).
  func (c *Client) CreateDirectRoom(ctx context.Context, bot, member string) (roomID string, err error)
  func (c *Client) Encrypted(ctx context.Context, roomID string) (bool, error)          // GET state m.room.encryption/, 404 → false
  func (c *Client) Membership(ctx context.Context, roomID, userID string) (string, error) // GET state m.room.member/{user}, 404 → ""
  func (c *Client) Send(ctx context.Context, roomID, txnID string, content any) error   // PUT …/send/m.room.message/{txnID}, message limiter
  func (c *Client) Leave(ctx context.Context, roomID string) error                      // POST …/leave, also rejects an invite
  func (c *Client) Forget(ctx context.Context, roomID string) error
  func (c *Client) Sync(ctx context.Context, since string, timeout time.Duration) (*SyncResponse, error)

  type Event struct {
      Type, Sender, EventID string
      StateKey *string `json:"state_key"`
      Content  json.RawMessage
  }
  type SyncResponse struct {
      NextBatch string `json:"next_batch"`
      Rooms struct {
          Join   map[string]struct{ Timeline struct{ Events []Event } }      `json:"join"`
          Invite map[string]struct{ InviteState struct{ Events []Event } `json:"invite_state"` } `json:"invite"`
          Leave  map[string]struct{ Timeline struct{ Events []Event } }      `json:"leave"`
      }
  }
  ```
  `CreateDirectRoom` body, exactly:
  ```json
  {"preset":"private_chat","is_direct":true,"invite":["<member>"],
   "creation_content":{"m.federate":false},
   "initial_state":[{"type":"m.room.history_visibility","state_key":"","content":{"history_visibility":"invited"}}],
   "power_level_content_override":{
     "users":{"<bot>":100},"users_default":0,"events_default":0,"state_default":100,
     "invite":100,"kick":100,"ban":100,"redact":100,
     "events":{"m.room.encryption":100,"m.room.power_levels":100,"m.room.history_visibility":100,
               "m.room.name":100,"m.room.topic":100,"m.room.avatar":100,"m.room.tombstone":100,"m.room.server_acl":100}}}
  ```
  `private_chat`, not `trusted_private_chat`: the trusted preset raises every invitee to the creator's power level, which would let the member turn encryption on.
  `Sync` sends `timeout` (ms), `since` when non-empty, and an inline `filter`:
  `{"room":{"timeline":{"types":["m.room.message","m.room.member"],"limit":50},"state":{"types":[]},"ephemeral":{"types":[]},"account_data":{"types":[]}},"presence":{"types":[]},"account_data":{"types":[]}}`; its request deadline is `timeout + 30s`.

- [ ] **Step 1: Write the failing tests:**
  - `TestCreateDirectRoomBody`: the fake's recorded request equals the JSON above for bot `@digest-bot:example.org`, member `@one:example.org` (compare decoded maps).
  - `TestEncryptedAndMembership`: fake room with/without encryption; `Membership` for `leave`, `join`, absent → `""`.
  - `TestSendIdempotentPath`: two `Send(room,"t1",c)` → `SendAttempts()==2`, `len(Sends())==1`; the path was `/_matrix/client/v3/rooms/%21r%3Aexample.org/send/m.room.message/t1`.
  - `TestSendRefused`: `RefuseSend(room,403,"M_FORBIDDEN")` → `IsCode(err,"M_FORBIDDEN")`, not `ErrUnavailable`.
  - `TestSyncDecodes`: queued body with one joined-room message, one invite and one left room → fields decoded; `SyncSince()` records `""` then the first `next_batch`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** `go test ./internal/matrix/...` → PASS.
- [ ] **Step 5: Commit** `Create the bot's unencrypted direct rooms, send with a transaction id, and sync`.

---

### Task 9: MAS admin client, and the fake MAS

**Files:**
- Create: `internal/mas/client.go`, `internal/mas/client_test.go`, `internal/mas/mastest/server.go`

**Interfaces:**
- Produces:
  ```go
  func New(adminURL, clientID, clientSecret string, hc *http.Client) *Client
  func (c *Client) SetTokenEndpoint(u string)
  var (
      ErrUnavailable = errors.New("MAS unavailable") // network error, timeout, 5xx, token endpoint unreachable
      ErrTaken       = errors.New("username taken")  // 409 from POST /users
      ErrSessionGone = errors.New("personal session gone") // 403, 404 or 422 from regenerate
  )
  type Version struct{ Major, Minor, Patch int }
  var MinVersion = Version{1, 5, 0}
  func ParseVersion(s string) (Version, bool) // "v1.28.0", "1.28.0", "v1.28.0-3-gabc" → ok; "v0.0.0-test" parses (and is older); "" or "main" → false
  func (v Version) Less(o Version) bool
  func (c *Client) Version(ctx context.Context) (string, error)

  type User struct{ ID, Username string; CreatedAt time.Time; Deactivated, Locked bool }
  func (c *Client) User(ctx context.Context, id string) (User, bool, error)
  func (c *Client) UserByUsername(ctx context.Context, username string) (User, bool, error)
  func (c *Client) LinkedUserID(ctx context.Context, provider, subject string) (string, bool, error)  // false also when user_id is null
  func (c *Client) LinkedSubject(ctx context.Context, provider, userID string) (string, bool, error)
  func (c *Client) HasLinks(ctx context.Context, userID string) (bool, error)
  func (c *Client) CreateUser(ctx context.Context, username string) (User, error)
  type Session struct{ ID, Token string }
  func (c *Client) CreateSession(ctx context.Context, userID, humanName, scope string) (Session, error)
  func (c *Client) RegenerateSession(ctx context.Context, sessionID string) (token string, err error)
  ```
  Admin token: `POST <token endpoint>` with HTTP Basic `client_id:client_secret`, form `grant_type=client_credentials&scope=urn:mas:admin`; cached until 60s before `expires_in`; a 401 from an admin route drops the cache and retries that call once. Query strings built with `url.Values` (`filter[provider]`, `filter[subject]`, `filter[user]`, `page[first]=1`). Each method's doc comment pins its source file at tag v1.28.0 (table above). Errors never carry the secret, a token, or the subject.

- `mastest.Server`: `New(t) *Server` with fields/methods: `ClientID, Secret string`, `VersionString string` (default `v1.28.0`), `Down atomic.Bool`, `AddUser(username string, created time.Time) string` (returns the ULID), `Link(provider, subject, userID string)`, `Deactivate(id string)`, `Users() []mas.User`, `Sessions() []SessionRec{ID, UserID, Scope, Token, HumanName string}`, `TokenRequests() int`, `RevokeSession(id)`, `ExpireTokens()` (next admin call gets 401). It enforces `urn:mas:admin` on admin routes and answers JSON:API shapes as in v1.28.0 (`data.id`, `data.attributes`).

- [ ] **Step 1: Write the failing tests:**
  - `TestParseVersion` (table above) and `TestVersionFloor`: `v1.4.1` is less than `MinVersion`, `v1.5.0` and `v1.28.0` are not.
  - `TestClientCredentials`: first call fetches a token with Basic auth and the form above (fake asserts both); a second call reuses it (`TokenRequests()==1`); after `ExpireTokens()` the next call refetches and succeeds.
  - `TestLinkedUserID`: linked → id; unlinked subject → false; link with null user → false; the request's decoded query has exactly `filter[provider]`, `filter[subject]`, `page[first]`.
  - `TestCreateUserTaken`: existing username → `errors.Is(err, ErrTaken)`.
  - `TestCreateSessionScope`: `CreateSession(id,"Digest Bot","urn:matrix:client:api:* urn:matrix:client:device:DIGESTBOT")` → fake recorded that scope and no `expires_in` key; returned token non-empty.
  - `TestRegenerateGone`: revoked session → `ErrSessionGone`.
  - `TestUnavailableHidesSecret`: `Down` → `ErrUnavailable`; `err.Error()` contains neither the secret nor a token.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** `go test ./internal/mas/...` → PASS.
- [ ] **Step 5: Commit** `Look members up and manage the bot account through MAS's admin API`.

---

### Task 10: Bot start: version, account, token, display name

**Files:**
- Create: `internal/bot/start.go`, `internal/bot/start_test.go`

**Interfaces:**
- Consumes: `matrix.Client` (Tasks 7–8), `mas.Client` (Task 9), `store.Store` bot state (Task 3).
- Produces:
  ```go
  const (
      DisplayName = "Digest Bot"
      Device      = "DIGESTBOT"
      Scope       = "urn:matrix:client:api:* urn:matrix:client:device:" + Device
  )
  type Config struct{ ServerName, Provider, Localpart string }
  type MemberChecker interface{ IsMember(ctx context.Context, subject string) (bool, error) } // *mbin.Source
  // ErrOff wraps a reason the bot cannot run until the configuration or MAS changes.
  var ErrOff = errors.New("matrix is off")
  // ErrUnavailable wraps matrix.ErrUnavailable and mas.ErrUnavailable for callers.
  var ErrUnavailable = errors.New("matrix unavailable")
  type Bot struct{ /* clients, store, members, cfg, userID */ }
  func Start(ctx context.Context, m *matrix.Client, a *mas.Client, st *store.Store, members MemberChecker, cfg Config) (*Bot, error)
  func (b *Bot) UserID() string // "@<localpart>:<server_name>"
  // renew obtains a new token (regenerate, else create) and stores it; call retries fn once
  // after renewing when fn fails with M_UNKNOWN_TOKEN.
  func (b *Bot) call(ctx context.Context, fn func() error) error
  ```
  Bot state keys (constants in `start.go`): `mas_user_id`, `localpart`, `session_id`, `access_token`, `create_intent` (`<localpart> <RFC3339 time>`), `sync_token` (Task 12).

  `Start`, in order:
  1. `a.Version` → `ParseVersion`; unparseable or `< MinVersion` → `ErrOff` with "MAS <v> is older than 1.5.0".
  2. `m.AuthMetadata` → `a.SetTokenEndpoint`.
  3. Account. Stored `mas_user_id` with stored `localpart == cfg.Localpart` and `a.User` found and not deactivated → ours. Otherwise `a.UserByUsername(cfg.Localpart)`:
     - not found → `SetBotState(create_intent)`, `a.CreateUser`; `ErrTaken` → taken (below); store `mas_user_id`, `localpart`, drop `create_intent`, `session_id`, `access_token`.
     - found → ours only if `create_intent` names this localpart, the user's `CreatedAt` is no earlier than the intent time minus 5 minutes, and `!HasLinks` (a create whose answer was lost); then store as above. Else taken.
     - taken → `ErrOff` whose message names the localpart, `ENGAGEMENT_MATRIX_BOT_LOCALPART` and the engagement app's `bot_localpart` setting. Never adopt.
  4. Token. Stored `access_token` → `SetToken`, `WhoAmI`; ok when user is `UserID()` and device is `Device`. Otherwise renew: `RegenerateSession(session_id)`, or on `ErrSessionGone`/no session `CreateSession(id, DisplayName, Scope)`; store both; `WhoAmI` again.
  5. `DisplayName(UserID())` ≠ `DisplayName` → `SetDisplayName` (only on change, since Synapse copies a profile change into every room).
  Any `ErrUnavailable` from either client → wrap `ErrUnavailable` (transient); any other unexpected error → returned as is (transient).

- [ ] **Step 1: Write the failing tests** (fake MAS + fake Synapse + storetest; the fake Synapse accepts whatever token the fake MAS last issued, via a hook `syn.Accept(func(token string) (user, device string, ok bool))` reading `mas.Sessions()`):
  - `TestStartCreatesBot`: fresh → MAS user `digest-bot` exists; one session with scope `Scope`; `Profile("@digest-bot:example.org") == "Digest Bot"`; bot state holds `mas_user_id`, `session_id`, `access_token`.
  - `TestStartReusesEverything`: second `Start` on the same store → no new user, no new session, no profile write.
  - `TestStartTakenLocalpart`: `AddUser("digest-bot", …)` plus `Link(provider,"sub-x",id)` before start → `errors.Is(err, ErrOff)`, message contains `ENGAGEMENT_MATRIX_BOT_LOCALPART` and `bot_localpart`; no session created.
  - `TestStartRecoversLostCreate`: `create_intent` stored at T, user created at T+1s with no links → adopted.
  - `TestStartOldMAS`: `VersionString = "v1.4.1"` → `ErrOff` naming `v1.4.1`; `"main"` → `ErrOff`.
  - `TestStartRenewsRefusedToken`: stored token the fake no longer accepts → `RegenerateSession` called, new token stored.
  - `TestStartUnavailable`: `mas.Down` → `errors.Is(err, ErrUnavailable)`, not `ErrOff`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** `go test ./internal/bot/` → PASS.
- [ ] **Step 5: Commit** `Create the Digest Bot account through MAS, refuse a taken name, and keep its token`.

---

### Task 11: Bot delivery: rooms, the Monday notice, sends

**Files:**
- Create: `internal/bot/deliver.go`, `internal/bot/notice.go`, `internal/bot/deliver_test.go`, `internal/bot/notice_test.go`

**Interfaces:**
- Consumes: `Bot`, `call` (Task 10); `store.MatrixMember`, `SetMatrixAccount`, `SetMatrixRoom`, `ForgetMatrixRoom`, `LeftMatrix` (Task 3); `matrix.CreateDirectRoom`, `Encrypted`, `Membership`, `Send`, `Leave`, `Forget` (Task 8); `mas.LinkedUserID`, `User` (Task 9); `digest.Digest`.
- Produces:
  ```go
  type Notice struct{ Body, FormattedBody string }
  func (n Notice) Content() map[string]any // msgtype m.notice, body, format org.matrix.custom.html, formatted_body
  func Weekly(d digest.Digest, meta, link string) Notice
  func Reply(text string) Notice             // body only, m.notice
  const (
      ReplyStopped = "Stopped. Reply start to get them again."
      ReplyStarted = "Started. Your next digest comes on Monday."
      ReplyHelp    = "Reply stop to stop these messages, or start to get them again."
  )
  // TxnID is "digest-<YYYY-MM-DD>-<first 16 hex of sha256(subject)>": the same member and
  // week always give the same id, and the id does not reveal the subject.
  func TxnID(subject string, week time.Time) string

  type Target struct{ Subject, UserID, RoomID string }
  // Prepare says whether the member gets a DM: setting on and a live Matrix account
  // linked to their subject at the configured provider. Members without one: ok false, nil.
  func (b *Bot) Prepare(ctx context.Context, subject string) (Target, bool, error)
  type Outcome int
  const ( Sent Outcome = iota + 1; Refused )
  // Deliver makes the member's room if they have none and sends n with txnID.
  func (b *Bot) Deliver(ctx context.Context, t Target, txnID string, n Notice) (Outcome, error)
  // ErrEncrypted: a new room came out encrypted; the room is abandoned. Wraps ErrUnavailable,
  // since every room would, and the Matrix half stops for the run.
  var ErrEncrypted error
  ```
  `Weekly`: titles are `d.Featured` then `d.More`, skipping `digest.KindPost`, first three. Body lines join with `\n` exactly as in Global Constraints (`• ` prefix, blank lines, no thread section when there are no titles: heading, blank line, link line, blank line, footer). `formatted_body` built with `html/template` (escape every title, URL and the meta title): `<p>Your weekly digest from META</p><ul><li><a href="URL">TITLE</a></li>…</ul><p>Read the whole digest: <a href="LINK">LINK</a></p><p>Reply stop to stop these messages.</p>`, `<ul>` omitted with no titles.

  `Prepare`: setting off → false. Cached `MASUserID` → `mas.User`; not found → clear and look up again; found but deactivated or locked → false. No cache → `LinkedUserID(provider, subject)` → `User` → `SetMatrixAccount(subject, id, "@"+username+":"+server)`.

  `Deliver`:
  1. No room → `CreateDirectRoom(bot, user)`; `Encrypted` true → `Leave`, `Forget`, `ErrEncrypted`; else `SetMatrixRoom`. A 4xx on create → `Refused`.
  2. `Send(room, txnID, n.Content())` via `call`. `ErrUnavailable` → returned (the caller releases the claim). 4xx → `Membership(room, user)`: `leave` or `ban` → `LeftMatrix`; anything else → `ForgetMatrixRoom` (the next week makes a new room); then `Refused`.

- [ ] **Step 1: Write the failing tests:**
  - `notice_test.go`: `TestWeeklyBody`: digest with featured entry "Grinders", More = post, entry "Beans", entry "Kettles", entry "Filters" → `Body ==` the exact text with `• Grinders`, `• Beans`, `• Kettles`; link and footer lines exact. `TestWeeklyOnlyPosts`: body is `"Your weekly digest from Talk · Example\n\nRead the whole digest: L\n\nReply stop to stop these messages."`. `TestNoticeEscapesTitles` (Review Focus 2): title `<b>Tom & Jerry</b> [x](y)` → `FormattedBody` contains `&lt;b&gt;Tom &amp; Jerry&lt;/b&gt;` and no `<b>`; `Body` contains the title verbatim. `TestContentIsNotice`: `msgtype == "m.notice"`, `format == "org.matrix.custom.html"`.
  - `TestTxnIDStable`: same inputs → same id; different subject or week → different; the id does not contain the subject.
  - `deliver_test.go` (fakes + store, bot from `Start`):
    - `TestPrepareLooksUpAndCaches`: linked member → target with `@one:example.org`; second call makes no link lookup (fake counter).
    - `TestPrepareUnlinked` / `TestPrepareDeactivated` / `TestPrepareOff` → `ok == false`, `err == nil`.
    - `TestDeliverCreatesRoomOnce`: two deliveries (different txn) → one room, two sends, room stored.
    - `TestDeliverAbandonsEncryptedRoom`: `EncryptNewRooms(true)` → `errors.Is(err, ErrEncrypted)`, room `Left` and `Forgotten`, no room stored, no send.
    - `TestDeliverVanishedRoom`: `RefuseSend(room,403,"M_FORBIDDEN")`, membership `join` → `Refused`, room forgotten, setting on. With membership `leave` → `Refused`, setting off.
    - `TestDeliverRetryReusesTxnID` (Review Focus 1): deliver, then deliver again with the same txn (as after a crash before `FinishDM`) → `SendAttempts()==2`, `len(Sends())==1`, both PUT paths identical.
    - `TestDeliverUnavailable`: `syn.Down` → `errors.Is(err, ErrUnavailable)`, no room stored.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** `go test ./internal/bot/` → PASS.
- [ ] **Step 5: Commit** `Send the Monday notice in each member's own unencrypted room, once per week`.

---

### Task 12: Bot listener

**Files:**
- Create: `internal/bot/listen.go`, `internal/bot/listen_test.go`

**Interfaces:**
- Consumes: Tasks 3, 8, 9, 10, 11.
- Produces:
  ```go
  // Command reduces a message to "stop", "start" or "".
  func Command(body string) string
  // Listen long-polls /sync as the bot until ctx ends, storing the sync token after each
  // batch is handled. It returns only when ctx ends; errors are logged and retried with a
  // wait that doubles from 1s to 1 minute.
  func (b *Bot) Listen(ctx context.Context) error
  func (b *Bot) handle(ctx context.Context, r *matrix.SyncResponse, initial bool) error
  ```
  `Command`: `strings.TrimFunc(body, unicode.IsSpace || unicode.IsPunct)`, lower-cased with `strings.ToLower`; `stop` → `"stop"`; `start`, `resume`, `restore` → `"start"`; else `""`.

  `Listen`: since = `BotState(sync_token)`; with none, the first response is handled with `initial = true` (invites only, no timeline). Sync timeout 30s. Token stored with `SetBotState(sync_token, next_batch)` after `handle` returns nil. `M_UNKNOWN_TOKEN` goes through `call` (renewal).

  `handle`, per the spec's table (reply txn ids are `"reply-" + first 16 hex of sha256(event_id)`, so a replayed batch is deduplicated by the homeserver):
  | Event | Condition | Action |
  |---|---|---|
  | `join[room]` `m.room.message` | room is a member's current room (`MatrixMemberByRoom`), sender is that member's `UserID`, `msgtype` not `m.notice`, sender not the bot | `stop` → `SetMatrixEnabled(false)`, reply `ReplyStopped`; `start` → `SetMatrixEnabled(true)`, reply `ReplyStarted`; else reply `ReplyHelp` |
  | `join[room]` `m.room.member`, `state_key` = the member | membership `leave` or `ban` | `LeftMatrix`, `Leave`, `Forget`; no reply |
  | `leave[room]` (the bot left or was removed) | room is a member's current room | `ForgetMatrixRoom` |
  | `invite[room]` | inviter (sender of the `m.room.member` invite for the bot in `invite_state`) is `@x:<server_name>`, `UserByUsername(x)` found, `LinkedSubject(provider, id)` found, `IsMember(subject)` true | `Leave` (rejects), leave and forget the member's old room if any, `SetMatrixAccount`, `SetMatrixEnabled(true)`, `CreateDirectRoom` (encryption check as in `Deliver`), `SetMatrixRoom`, send `ReplyStarted` (txn from the invite event id) |
  | `invite[room]` | anything else (remote user, unknown, not a member) | `Leave` only |
  | anything else | | ignored |

- [ ] **Step 1: Write the failing tests** (`QueueSync` drives `handle` through `Listen` with a ctx cancelled once the queue drains, or call `handle` directly):
  - `TestCommandParsing` (Review Focus 3, table): `"stop"`, `"STOP!"`, `"  Start. "`, `"\"resume\""`, `"Restore"` act; `"stop please"`, `"🛑"`, `""`, `"start?!stop"` → `""`.
  - `TestStopAndStart`: member with room; message `STOP` → setting off, one send with body `ReplyStopped` as `m.notice`; `stop` again → still off, same reply; `start` → on, `ReplyStarted`.
  - `TestHelpReply`: `hello` → `ReplyHelp`, setting unchanged.
  - `TestIgnoresNoticesSelfAndStrangers`: an `m.notice` from the member, a message from the bot, a message from another user in the member's room, a message in an unknown room → no sends.
  - `TestLeaveCountsAsStop`: member `leave` event → setting off, room forgotten, bot `Left` and `Forgotten`, no send; a later `Prepare` → false; a later `Deliver` is never reached, and no new room is created on any following sync.
  - `TestInviteFromMember`: invite from `@one:example.org` (linked, Mbin member via a fake `MemberChecker`) → invite room `Left`, one new room with `Request` as Task 8, `ReplyStarted` sent there, setting on.
  - `TestForeignInvite`: invite from `@x:remote.org`, and from a local user with no link → `Left`, nothing else.
  - `TestSyncTokenStored`: two queued batches → `SyncSince() == ["", "b1", "b2"]` (third call blocks), bot state `sync_token == "b2"`; a fresh `Listen` on the same store starts from `b2`. `TestInitialSyncIgnoresTimeline`: no stored token, first batch carries a `stop` message → no send, setting on.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** `go test ./internal/bot/` → PASS.
- [ ] **Step 5: Commit** `Listen for stop and start, treat leaving as stop, and answer a member's invite with a fresh room`.

---

### Task 13: Bot supervisor

**Files:**
- Create: `internal/bot/supervise.go`, `internal/bot/supervise_test.go`

**Interfaces:**
- Consumes: `Start`, `Listen`, `ErrOff`, `store.TryBotLock`.
- Produces:
  ```go
  type Holder struct{ /* atomic.Pointer[Bot] */ }
  func (h *Holder) Get() *Bot // nil when this copy is not running the bot
  type Deps struct {
      Matrix *matrix.Client; MAS *mas.Client; Store *store.Store
      Members MemberChecker; Config Config
      Retry time.Duration // wait between lock attempts and after a transient Start error; main passes time.Minute
  }
  // Supervise runs until ctx ends: take the lock (retrying every Retry), Start the bot,
  // publish it in h, Listen; on lock loss or ctx end, clear h. ErrOff is logged once at
  // Error level with its reason, the lock is released, and Supervise returns.
  func Supervise(ctx context.Context, d Deps, h *Holder)
  ```

- [ ] **Step 1: Write the failing tests:** `TestSuperviseRunsBot` (`Get()` becomes non-nil within 2s); `TestSuperviseOneHolder` (two `Supervise` on the same schema: exactly one `Holder` non-nil after 2s); `TestSuperviseOffLogsOnce` (old MAS → returns, one log record containing "Matrix is off", `Get()` nil; capture with a `slog` handler as `run_test.go` does); `TestSuperviseRetriesUnavailable` (`mas.Down` for the first attempt, cleared → bot starts; `Retry: 10*time.Millisecond`).
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** `go test ./internal/bot/` → PASS.
- [ ] **Step 5: Commit** `Run the Digest Bot on the one copy that holds the bot lock`.

---

### Task 14: The web digest page

**Files:**
- Modify: `internal/mail/render.go`, `internal/mail/templates/digest.html`, `internal/web/server.go`
- Test: `internal/mail/render_test.go`, `internal/web/server_test.go`

**Interfaces:**
- Consumes: `token.VerifyDigest` (Task 2), `store.WebDigest` (Task 4), `store.Unsubscribe(ctx, subject, matrix)` (Task 3).
- Produces:
  ```go
  // mail: the email's HTML, without the unsubscribe link.
  func RenderPage(d digest.Digest, n Names, siteURL string, from, to time.Time) (string, error)
  // web:
  type Options struct {
      Store *store.Store; Key []byte; Names mail.Names; SiteURL string
      Matrix bool             // the deployment has a homeserver; passed to Unsubscribe
      Now    func() time.Time // nil means time.Now
  }
  func NewServer(o Options) http.Handler
  ```
  Template: wrap the unsubscribe `<a>` and the `<br>` before it in `{{if .Unsubscribe}}`; add `<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Your week on {{.Meta}}</title></head>`. The email is unchanged apart from the head.

  Routes: `GET /d/{token}` renders; `HEAD /d/{token}` answers the same status and headers with no body; `/d/{token}` with any other method → `invalid`. Valid token → `WebDigest(subject, date, now)` → `json.Unmarshal` into `digest.Digest` → `RenderPage(d, names, siteURL, week-7d, week)` where `week` is the date at 00:00 local (only "Jan 2" is shown). On `/d/` responses set `X-Robots-Tag: noindex` and `Cache-Control: private, no-store`, `Content-Type: text/html; charset=utf-8`. `invalid`'s body becomes `This link is not valid.` for both routes, so the two 404s are identical. A store error → 500 with a plain sentence, nothing logged that names the member.

- [ ] **Step 1: Write the failing tests:**
  - `render_test.go`: `TestRenderPageHasNoUnsubscribe` (no `Unsubscribe`, no `/u/`; contains the featured title and the button); `TestRenderPageMatchesEmail` (the page equals `Render(…).HTML` with the unsubscribe link and its `<br>` removed — compute by rendering both).
  - `server_test.go`: `TestWebDigest` (saved digest → 200, body contains the featured title, the three headers exact); `TestWebDigestHead` (200, empty body); `TestWebDigestBadTokens` (altered, other member's subject with this MAC, unsubscribe token, expired at `week+28d` via `Now`, unknown week → each 404 with body `This link is not valid.`, byte-identical to a bad `/u/` response); `TestWebDigestRecordsNothing` (row counts of every table unchanged by GET and HEAD); `TestWebDigestOtherMethods` (`POST`, `PUT`, `DELETE` → 404). Update `badBody` and the `NewServer` calls in the existing tests.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** `go test ./internal/mail/ ./internal/web/` → PASS.
- [ ] **Step 5: Commit** `Serve each sent digest at a signed link that expires after four weeks`.

---

### Task 15: The run: one frozen digest, the email pass, the Matrix pass

**Files:**
- Modify: `internal/run/run.go`
- Test: `internal/run/run_test.go`, new `internal/run/matrix_test.go`

**Interfaces:**
- Consumes: `store.SaveDigest`, `SavedDigest`, `DigestsForWeek`, `PurgeDigests`, `ClaimDM`, `FinishDM`, `ReleaseDM`, `MatrixMember`, `Shown(…, before)`; `bot.Target`, `bot.Outcome`, `bot.Weekly`, `bot.TxnID`, `bot.ErrUnavailable`; `token.SignDigest`.
- Produces:
  ```go
  type DM interface {
      Prepare(ctx context.Context, subject string) (bot.Target, bool, error)
      Deliver(ctx context.Context, t bot.Target, txnID string, n bot.Notice) (bot.Outcome, error)
  }
  // Runner gains:
  //   Matrix           func() DM // nil, or returns nil, when this copy runs no bot
  //   MatrixConfigured bool      // all seven variables set
  //   Sender may be nil: no mail host, so no email pass.
  ```
  `Week(ctx, week)`:
  1. Read members, hot, stats (unchanged).
  2. **Email pass**, per member. `emailOn := r.Sender != nil && m.Email != ""`; `matrixOn := r.MatrixConfigured && MatrixMember(subject).Enabled`. Neither → skip. `emailOn` → `Claim` as today; not claimed and `!matrixOn` → skip. Then `digestFor` (below); an error releases a held email claim and is returned at the end, as today. With an email claim: unsubscribed → `StatusUnsubscribed`; empty → `StatusEmpty`; else render and send from the frozen digest exactly as today.
  3. **Matrix pass**, when `r.Matrix != nil && r.Matrix() != nil`, under a context that ends at `week + 24h` (a weekly notice is never sent the next day). `DigestsForWeek(week)`; for each member of step 1 in order with a stored digest and `matrixOn`: `Prepare` (false → skip, no row); `ClaimDM` (false → skip); `Deliver(target, bot.TxnID(subject, week), bot.Weekly(d, r.Names.Meta, r.PublicURL+"/d/"+token.SignDigest(r.Key, subject, week)))`. `Sent` → `FinishDM(StatusSent, sent(d)...)`; `Refused` → `FinishDM(StatusFailed)` and a warning with the Mbin member id; `ErrUnavailable` (from either call) → `ReleaseDM`, stop the pass, return the error. Finish and release use the `WithoutCancel` + 10s pattern already in `Week`.
  4. Both passes always run; their errors are joined. One `slog.Info("matrix run", …)` with counts beside the existing `digest run` line.

  `digestFor(ctx, m, from, week, items, stats) (digest.Digest, bool, error)`: `SavedDigest` hit → unmarshal; else `View`, `Shown(subject, week)`, `Build`; empty → `(_, false, nil)` with nothing stored; else `SaveDigest(json.Marshal(d))` and unmarshal what it returns, so concurrent copies use one digest.

  `Loop` gains: at most once an hour, `PurgeDigests(now())`, logging the count; a purge error is logged, never fatal.

- [ ] **Step 1: Write the failing tests.** `matrix_test.go` uses a `fakeDM` (records `Deliver` calls, scriptable outcome/error per subject, `Prepare` false for listed subjects) and `storetest`:
  - `TestDMSentOnceInSameRun`: 3 members, Sender recorder and fakeDM → 3 emails, 3 deliveries; `dm_sends` all `sent`; second `Week` → no new email or delivery.
  - `TestDMAndEmailShowSameDigest`: the notice's first title equals the email's featured title; the link verifies with `token.VerifyDigest` to the member and `2026-10-26`; `WebDigest` for it is found.
  - `TestDMWithoutVerifiedEmail`: member with `Email: ""` → no email, one delivery, no `sends` row.
  - `TestDMIgnoresEmailUnsubscribe`: unsubscribed member → `sends` status `unsubscribed`, one delivery.
  - `TestMatrixOffSkipsDM`: `SetMatrixEnabled(false)` → email sent, no delivery.
  - `TestEmptyDigestNoDM`: joined member → no email, no delivery, no `web_digests` row.
  - `TestHomeserverDownStopsMatrixOnly`: fakeDM returns `bot.ErrUnavailable` for member 2 → all 3 emails sent, member 1 `sent`, member 2 no row, member 3 not attempted, `Week` returns an error wrapping `bot.ErrUnavailable`; with the fake healed, the next `Week` delivers to 2 and 3 only.
  - `TestRefusedIsNotRetried`: `Refused` for member 1 → `failed`; next `Week` → no delivery to 1.
  - `TestNoBotOnThisCopy`: `Matrix` returns nil → emails sent, no `dm_sends` rows, `Week` returns nil.
  - `TestNoMailHostStillDMs`: `Sender: nil`, Matrix on → deliveries, no `sends` rows.
  - `TestFrozenDigestSurvivesShown`: run the email pass, then a fresh `Runner` (as a restart) runs `Week` → the digest it delivers equals the stored one, although `shown` now has this week's rows.
  - `TestWeekDateAgreesInLocalZone` (Review Focus 5): `week = time.Date(2026, 3, 30, 8, 0, 0, 0, mustLoad("Europe/Berlin"))` (the Monday after the DST change) → the `dm_sends` row, the `web_digests` row and the token's date are all `2026-03-30`.
  - `TestLoopPurgesExpired` (in `run_test.go`): a digest saved for a week 29 days before the clock → gone after one tick.
  Update `runner(...)` and the existing tests for the new fields (`Matrix: nil`).
- [ ] **Step 2:** `go test ./internal/run/` → FAIL.
- [ ] **Step 3: Implement.** Update `Week`'s doc comment to describe both passes.
- [ ] **Step 4:** `go test ./...` → PASS.
- [ ] **Step 5: Commit** `Build each member's digest once a week and send it by email and on Matrix in the same run`.

---

### Task 16: Wiring in main

**Files:**
- Modify: `cmd/paisans-engagement-service/main.go`
- Test: `cmd/paisans-engagement-service/main_test.go`

**Interfaces:**
- Consumes: everything above.
- Produces: `serve()`:
  - `cfg.Matrix.Enabled()` false → `slog.Info("Matrix is off", "missing", cfg.Matrix.Missing())` once.
  - The Mbin source is opened, and `run.Loop` started, when `cfg.SMTP.Host != "" || cfg.Matrix.Enabled()`; the SMTP warning stays, reworded "no mail host is configured, so no digest email is sent".
  - Matrix on: `matrix.New(cfg.Matrix.URL, &http.Client{Timeout: 60*time.Second}, matrix.DefaultPacing)`, `mas.New(cfg.Matrix.MASAdminURL, cfg.Matrix.ClientID, cfg.Matrix.ClientSecret, &http.Client{Timeout: 30*time.Second})`, a `bot.Holder`, `go bot.Supervise(ctx, bot.Deps{…, Members: src, Retry: time.Minute}, h)`.
  - `runner.Matrix = func() run.DM { if b := h.Get(); b != nil { return b }; return nil }` (an explicit nil, never a typed nil in the interface); `runner.MatrixConfigured = cfg.Matrix.Enabled()`.
  - `web.NewServer(web.Options{Store: st, Key: cfg.SigningKey, Names: …, SiteURL: cfg.MbinURL, Matrix: cfg.Matrix.Enabled()})`.
  - The Mbin pool is closed only after both the loop and the supervisor have returned (one `sync.WaitGroup`, waited on in the existing deferred shutdown with its 10s limit).

- [ ] **Step 1: Write the failing test** `TestRunnerMatrixNilIsUntyped`: extract `dmFrom(h *bot.Holder) func() run.DM` and assert `dmFrom(&bot.Holder{})() == nil`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** `go build ./... && go test ./...` → PASS.
- [ ] **Step 5: Smoke check:** `ENGAGEMENT_LISTEN_ADDR=127.0.0.1:18083 ENGAGEMENT_PUBLIC_URL=http://x ENGAGEMENT_DATABASE_URL=$ENGAGEMENT_TEST_DATABASE_URL ENGAGEMENT_MBIN_DATABASE_URL=$ENGAGEMENT_TEST_DATABASE_URL ENGAGEMENT_MBIN_URL=http://m ENGAGEMENT_INSTANCE_TITLE=T ENGAGEMENT_META_TITLE=M ENGAGEMENT_SIGNING_KEY=$(head -c32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=') go run ./cmd/paisans-engagement-service` → logs `Matrix is off` with seven missing names and `listening`; `curl -s localhost:18083/healthz` → `ok`; Ctrl-C exits cleanly. (Running it migrates the shared test database's `public` schema; drop it afterwards with `docker exec engagement-test-pg psql -U postgres -d engagement_test -c 'DROP SCHEMA public CASCADE; CREATE SCHEMA public'`.)
- [ ] **Step 6: Commit** `Start the Digest Bot when the homeserver is configured`.

---

### Task 17: README and CHANGELOG

**Files:**
- Modify: `README.md`, `CHANGELOG.md`

- [ ] **Step 1: README**, in the existing voice, current state only:
  - Opening paragraph: the digest goes by email and, on a deployment with a homeserver, as a Monday message from the Digest Bot.
  - New section **The Digest Bot**: who qualifies (a member whose Matrix account MAS links to their Pocket ID subject at the configured provider; no verified email needed; independent of the email unsubscribe); the message's shape; the room (one per member, `private_chat`, `is_direct`, not federated, history from the invite, member at power level 0 and encryption at 100, a room that comes out encrypted is abandoned); the commands table (the spec's table, with the exact replies); sending (same run, separate record, txnId, paced at Synapse's default `rc_message` and `rc_room_creation`, 429 waited out, failures as in the spec, the Matrix half ends with the send day); listening (only on the copy holding the bot lock, sync token kept); the account (localpart from `ENGAGEMENT_MATRIX_BOT_LOCALPART`, created through MAS, a taken name stops the bot and the log names the variable and the `bot_localpart` setting; token scope `urn:matrix:client:api:* urn:matrix:client:device:DIGESTBOT`, not a Synapse admin; MAS 1.5.0 or later, checked at start).
  - **The web digest**: `/d/{token}`, frozen at send time, signed apart from unsubscribe, 28 days then deleted, GET and HEAD only, the three headers, the 404.
  - The "only page" paragraph: the pages are `/u/{token}` and `/d/{token}`; the gateway forwards both and `GET /healthz`.
  - Unsubscribe bullet: it forgets shown threads only when the member's Matrix setting is also off, or the deployment has no homeserver.
  - "Who gets it": local Mbin accounts with an OIDC subject; email needs a verified address.
  - Environment table: the seven variables with the contract's meanings, a note that the bot runs only when all seven are set.
  - Own database paragraph: unsubscribes, email sends, shown threads, each member's Matrix setting, account and room, DM sends, stored digests (deleted at expiry), and the bot's own state (sync token, access token, MAS ids).
- [ ] **Step 2: CHANGELOG** under `## [Unreleased]`, `### Added`, each bullet at most 35 words:
  - `A Monday Matrix message from a Digest Bot account, with the week's first three threads and a link, for every member with a linked Matrix account on the deployment's homeserver.`
  - `Members stop the messages by replying stop or leaving the room, and start them again by replying start, resume or restore, or by inviting the bot.`
  - `Each sent digest can be read for four weeks at a signed /d/ link.`
  - `### Changed`: `Unsubscribing from the email forgets which threads a member was shown only when their Matrix messages are off too.` and `A bad unsubscribe link answers "This link is not valid."`
- [ ] **Step 3:** Check the bullets: `awk '/^## \[Unreleased\]/{f=1;next} /^## \[/{f=0} f && /^- /' CHANGELOG.md | while IFS= read -r l; do printf '%s\n' "${l#- }" | wc -w; done` → every count ≤ 35.
- [ ] **Step 4:** `gofmt -l .` empty, `go vet ./...` clean, `go test ./...` PASS with the database set (no SKIP lines for store, run, web, bot, mbin).
- [ ] **Step 5: Commit** `Describe the Digest Bot, the web digest and the new variables`.
