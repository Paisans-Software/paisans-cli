# Weekly Engagement Digest Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every member of a Paisans deployment gets one personal weekly email about the Mbin conversations they have not joined yet, built by a new `paisans-engagement-service` and deployed by a new reserved `engagement` blueprint.

**Architecture:** Three repositories. (A) `Paisans-Software/paisans-engagement-service`, a Go service modelled file for file on `paisans-account-service`: it reads Mbin's Postgres through a read only role, keeps its own state in its own database, sends mail at 08:00 local time each Monday, and answers one public route, unsubscribe. (B) `paisans-cli` gains the reserved `engagement` blueprint, the read only role (created in the cluster bootstrap, granted after the app stacks are healthy), its validate rules, rendering and rotation. (C) `paisans-blueprints` gains the image track.

**Tech Stack:** Go 1.25.8, `pgx/v5` (pgxpool), `wneessen/go-mail`, stdlib `net/http`, `html/template`, `text/template`, `crypto/hmac`. Postgres 18 in tests.

**Spec:** `docs/specs/2026-10-24-engagement-digest.md` (this repository). Read it before any task.

## Global Constraints

* No em dashes in anything written: code comments, docs, commits, PR bodies. No rejected alternatives in any of them.
* App types are "blueprints", never "kinds".
* No real hostnames: examples use `example.org`.
* Never `git stash`. Commit WIP instead.
* Every commit ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
* The service never writes to Mbin's database, and the role it uses cannot.
* The read only role never sees: tables `sessions`, `rememberme_token`, `oauth2_access_token`, `oauth2_refresh_token`, `oauth2_authorization_code`, `oauth2_client`, `oauth2_user_consent`, `reset_password_request`, `user_push_subscription`, `site`, `settings`, `messenger_messages`, `message`, `message_thread`, `user_note`, `ap_activity`, `activity`; columns `password`, `totp_secret`, `totp_backup_codes`, `private_key`, `old_private_key`, `ip`, any `oauth_*_id` except `oauth_oidc_id`.
* The digest's schedule is fixed: Monday 08:00 in the container's local time, covering `[send - 7 days, send)`. No setting changes it.
* The only public route is unsubscribe. No other page exists.
* The email names the community with the meta title everywhere except the button, which reads `See everything on <instance title>`.
* A member is identified everywhere in the service's own database and tokens by Mbin's `"user".oauth_oidc_id`.
* Mbin is read only when it has `placement: cluster`.
* Service env vars carry the `ENGAGEMENT_` prefix. Its port is `8083`.

## Review Focus

1. A member who unsubscribed between two runs of the same week, or a second copy of the service started mid run: neither may produce a second email. Pinned in Task 2 (claim conflict test) and A7 (two runners, one store).
2. A week where Mbin's tables are not yet readable (first deploy, grants not run yet): the run logs one actionable error and retries on the next tick inside the day, without claiming members. Pinned in Task 7.
3. A featured thread whose top comment is by a user the member blocked, or by a remote user: the excerpt falls back to the next local comment or is omitted. Pinned in Task 4.
4. A thread title or comment carrying HTML or markdown: the HTML part escapes it and the excerpt is plain text of at most 200 characters. Pinned in Task 6.
5. An Mbin schema change that drops an allowlisted column: `apply` fails loudly on the grant instead of skipping it, once Mbin's tables exist. Pinned in Task 12.

---

# Part A: `paisans-engagement-service`

Working copy: `/Users/wash/Developer/paisans.community/src/paisans-engagement-service`, branch `feat/weekly-digest` from `develop`. The controller creates the GitHub repository, the initial `main` and `develop` commits (README stub, MIT `LICENSE` "Copyright (c) 2026 Joseph Quigley", `.gitignore` with `.worktrees/` and `/paisans-engagement-service`) before Task 1.

Mirror `paisans-account-service` (`src/paisans-account-service`) wherever this plan is silent: config closures, store/migrations, storetest, mail sanitising, slog JSON, `healthcheck` subcommand, Dockerfile, workflows. Read the matching file there before writing each one.

Tests needing Postgres read `ENGAGEMENT_TEST_DATABASE_URL` and skip when it is unset. Locally: `docker run -d --rm -p 55432:5432 -e POSTGRES_PASSWORD=test postgres:18` and `ENGAGEMENT_TEST_DATABASE_URL=postgres://postgres:test@localhost:55432/postgres?sslmode=disable`.

### Task 1: Scaffold, config, image and CI

**Files:**
- Create: `go.mod` (module `github.com/paisans-software/paisans-engagement-service`, `go 1.25.8`), `cmd/paisans-engagement-service/main.go`, `internal/config/config.go`, `internal/config/config_test.go`, `Dockerfile`, `.dockerignore`, `.github/workflows/test.yml`, `.github/workflows/release.yml`, `.github/workflows/changelog.yml`, `CHANGELOG.md`

**Interfaces:**
- Produces: `config.Load(getenv func(string) string) (config.Config, error)`;
  `type Config struct { ListenAddr, PublicURL, DatabaseURL, MbinDatabaseURL, MbinURL, InstanceTitle, MetaTitle string; SigningKey []byte; SMTP SMTP }`;
  `type SMTP struct { Host string; Port int; User, Password, From, FromName, TLS string }`

| Variable | Rule |
|---|---|
| `ENGAGEMENT_LISTEN_ADDR` | default `:8083` |
| `ENGAGEMENT_PUBLIC_URL` | required, `https://` or `http://`, no trailing `/` |
| `ENGAGEMENT_DATABASE_URL` | required |
| `ENGAGEMENT_MBIN_DATABASE_URL` | required |
| `ENGAGEMENT_MBIN_URL` | required, no trailing `/` |
| `ENGAGEMENT_INSTANCE_TITLE`, `ENGAGEMENT_META_TITLE` | required |
| `ENGAGEMENT_SIGNING_KEY` | 32 bytes, unpadded base64url |
| `ENGAGEMENT_SMTP_HOST` | optional: empty means no mail host, and the service sends nothing |
| `ENGAGEMENT_SMTP_FROM` | required when `ENGAGEMENT_SMTP_HOST` is set |
| `ENGAGEMENT_SMTP_PORT` | default `587`, 1 to 65535 |
| `ENGAGEMENT_SMTP_USER`, `_PASSWORD`, `_FROM_NAME` | optional |
| `ENGAGEMENT_SMTP_TLS` | `none`, `starttls` (default) or `tls` |

- [ ] **Step 1: Write failing tests** in `config_test.go`: `TestLoadValid` (every field read, defaults `:8083`, `587`, `starttls`), `TestLoadReportsEveryProblem` (empty env: one joined error naming each required variable), `TestLoadRejectsShortKey` (`"ENGAGEMENT_SIGNING_KEY must be 32 bytes as unpadded base64url"`), `TestLoadRejectsTrailingSlash` (both URLs), `TestLoadRejectsUnknownTLS` (`"ENGAGEMENT_SMTP_TLS must be none, starttls or tls"`).
- [ ] **Step 2: Run** `go test ./internal/config/` and see them fail.
- [ ] **Step 3: Implement `Load`** with account-service's closure style (`req`, `opt`, `key`, `intIn`), `errors.Join`.
- [ ] **Step 4: Run** `go test ./internal/config/`: PASS.
- [ ] **Step 5: `main.go`**: `main()` calls `run() error`; `healthcheck` subcommand probes `http://127.0.0.1:<port>/healthz`. `run` loads config and exits with `slog.Error("exiting", "error", err)` on failure; later tasks wire the rest. Dockerfile, `.dockerignore`, workflows copied from account-service with names changed, `contract.yml` omitted, test env var `ENGAGEMENT_TEST_DATABASE_URL`, image `ghcr.io/paisans-software/paisans-engagement-service`, `EXPOSE 8083`. `CHANGELOG.md` with the account-service header and `## [Unreleased]` / `### Added` / one bullet: "A weekly digest email for every member of a Paisans deployment, about the Mbin threads and microblog posts they have not joined yet."
- [ ] **Step 6: Verify** `go vet ./... && go build ./... && docker build -t engagement-test .` succeed.
- [ ] **Step 7: Commit** `Load the service's configuration and build its image`.

### Task 2: The service's own store

**Files:**
- Create: `internal/store/store.go`, `internal/store/migrations/0001_digest.sql`, `internal/store/digest.go`, `internal/store/storetest/storetest.go`, `internal/store/digest_test.go`

**Interfaces:**
- Produces:
  `store.Open(ctx context.Context, url string) (*Store, error)`, `(*Store).Migrate(ctx) error`, `Ping(ctx) error`, `Close()`;
  `(*Store).Unsubscribe(ctx, subject string) error` (idempotent);
  `(*Store).Unsubscribed(ctx, subject string) (bool, error)`;
  `(*Store).Claim(ctx, subject string, week time.Time) (bool, error)`: inserts `(subject, week, 'pending')`, returns false on conflict;
  `(*Store).Finish(ctx, subject string, week time.Time, status Status) error`;
  `type Status string` with `StatusSent = "sent"`, `StatusEmpty = "empty"`, `StatusFailed = "failed"`, `StatusUnsubscribed = "unsubscribed"`;
  `storetest.New(t) *store.Store` (per test schema, as account-service).

Schema:
```sql
CREATE TABLE unsubscribes (subject text PRIMARY KEY, at timestamptz NOT NULL DEFAULT now());
CREATE TABLE sends (
  subject text NOT NULL,
  week date NOT NULL,
  status text NOT NULL CHECK (status IN ('pending','sent','empty','failed','unsubscribed')),
  at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (subject, week)
);
```
`week` is the send date (the Monday), stored as a `date`.

- [ ] **Step 1: Write failing tests**: `TestUnsubscribeIsIdempotent`, `TestClaimOnlyOnce` (second `Claim` for the same subject and week returns false, from a second `storetest.Conn` too), `TestClaimNewWeek` (a later week claims again), `TestFinishRecordsStatus`.
- [ ] **Step 2: Run** with `ENGAGEMENT_TEST_DATABASE_URL` set: FAIL.
- [ ] **Step 3: Implement** `store.go` (migration lock constant of the service's own, `0x656e676167650001`) and `digest.go`.
- [ ] **Step 4: Run**: PASS.
- [ ] **Step 5: Commit** `Record unsubscribes and each member's weekly send`.

### Task 3: Unsubscribe tokens

**Files:**
- Create: `internal/token/token.go`, `internal/token/token_test.go`

**Interfaces:**
- Produces: `token.Sign(key []byte, subject string) string`; `token.Verify(key []byte, tok string) (subject string, ok bool)`.

Format: `b64url(subject) + "." + b64url(HMAC-SHA256(key, "unsubscribe\x00" + subject))`, `base64.RawURLEncoding`. No expiry: a link in a year old email still works.

- [ ] **Step 1: Write failing tests**: `TestRoundTrip`, `TestAlteredSubjectFails`, `TestOtherKeyFails`, `TestMalformedFails` (`""`, `"a"`, `"a.b.c"`, bad base64), `TestTokenIsURLSafe`.
- [ ] **Step 2: Run** `go test ./internal/token/`: FAIL.
- [ ] **Step 3: Implement** with `hmac.Equal`.
- [ ] **Step 4: Run**: PASS.
- [ ] **Step 5: Commit** `Sign unsubscribe links`.

### Task 4: Choosing a member's items

Pure Go, no database. This is where every rule in the spec's *Which threads, for whom* lives.

**Files:**
- Create: `internal/digest/digest.go`, `internal/digest/digest_test.go`

**Interfaces:**
- Produces:
```go
type Kind string // "entry" or "post"
const (KindEntry Kind = "entry"; KindPost Kind = "post")
type Key struct { Kind Kind; ID int64 }
type Magazine struct { ID int64; Name, Title string }
type Comment struct { AuthorID int64; Body string; Votes int } // local authors only
type Item struct {
    Key Key; Title, URL string; Magazine Magazine; AuthorID int64
    Federated bool; Comments, Participants, Score int
    TopComments []Comment // highest voted first, at most 3, empty for federated
}
type Member struct { ID int64; Subject, Email, Username string }
type View struct {
    Subscribed, BlockedMagazines, BannedMagazines map[int64]bool
    BlockedUsers map[int64]bool
    Joined map[Key]bool
    Visited bool // "user".last_active inside the window
}
type Stats struct { NewThreads, Comments, NewMembers int }
type Featured struct { Item Item; Excerpt string }
type Digest struct { Stats Stats; Featured *Featured; More []Item }
func Build(items []Item, v View, stats Stats) (Digest, bool)
```
`Build` returns false when the digest would hold no item.

Rules, each a test:
1. Drop an item whose key is in `Joined`, whose magazine is blocked or banned, or whose author is blocked.
2. A federated item survives only if its magazine is in `Subscribed`.
3. Rank by `weight * (Score + 2*Comments + 3*Participants)` where weight starts at 1, times 1.5 when the magazine is subscribed, times 0.5 when subscribed and `Visited`, times 0.5 when federated. Ties break by higher `Comments`, then lower `Key.ID`.
4. `Featured` is the highest ranked non federated item; nil when none.
5. `More` is the next items in rank order, at most 5, with at most 2 federated.
6. `Excerpt` is the first `TopComments` entry whose author is not in `BlockedUsers`, reduced to plain text by `Plain(body string, max int) string` (strip markdown emphasis, links to their text, collapse whitespace, cut at a word boundary to at most 200 runes with `…`). Empty when none qualifies.

- [ ] **Step 1: Write failing tests**: `TestDropsJoined`, `TestDropsBlockedMagazineBannedMagazineBlockedAuthor`, `TestFederatedNeedsOwnSubscription`, `TestFederatedNeverFeatured`, `TestFederatedCappedAtTwo`, `TestSubscribedRanksHigher`, `TestVisitedSubscribedRanksLower`, `TestMoreAtMostFive`, `TestExcerptSkipsBlockedAuthor`, `TestExcerptOmittedWhenNone`, `TestEmptyWhenEverythingDropped` (returns false), `TestNoLocalItemsMeansNoFeatured` (federated only still sends, `Featured == nil`), `TestPlain` table (markdown link, `**bold**`, newlines, 250 rune input cut to ≤ 200 with `…`).
- [ ] **Step 2: Run** `go test ./internal/digest/`: FAIL.
- [ ] **Step 3: Implement** `Build` and `Plain`.
- [ ] **Step 4: Run**: PASS.
- [ ] **Step 5: Commit** `Choose each member's featured thread and list`.

### Task 5: Reading Mbin

**Files:**
- Create: `internal/mbin/allowlist.go`, `internal/mbin/mbin.go`, `internal/mbin/queries.go`, `internal/mbin/mbin_test.go`, `internal/mbin/testdata/schema.sql`

**Interfaces:**
- Consumes: `digest.Item`, `digest.Member`, `digest.View`, `digest.Stats`, `digest.Key`.
- Produces:
```go
var Allowlist = map[string][]string{ ... } // table -> columns, exactly as below
type Source struct { ... }
func Open(ctx context.Context, url, siteURL string) (*Source, error)
func (s *Source) Close()
func (s *Source) Members(ctx context.Context) ([]digest.Member, error)
func (s *Source) Hot(ctx context.Context, from, to time.Time) ([]digest.Item, error)
func (s *Source) View(ctx context.Context, m digest.Member, from, to time.Time, items []digest.Item) (digest.View, error)
func (s *Source) Stats(ctx context.Context, from, to time.Time) (digest.Stats, error)
```

`Allowlist` (the toolkit's copy in Task 12 must match it; a comment in each says so):

| Table | Columns |
|---|---|
| `entry` | `id, user_id, magazine_id, slug, title, comment_count, score, is_adult, visibility, ap_id, created_at, last_active` |
| `entry_comment` | `id, user_id, entry_id, magazine_id, body, up_votes, favourite_count, visibility, is_adult, created_at` |
| `post` | `id, user_id, magazine_id, slug, body, comment_count, score, is_adult, visibility, ap_id, created_at, last_active` |
| `post_comment` | `id, user_id, post_id, magazine_id, body, up_votes, favourite_count, visibility, is_adult, created_at` |
| `entry_vote` | `user_id, entry_id, choice` |
| `post_vote` | `user_id, post_id, choice` |
| `favourite` | `user_id, entry_id, post_id` |
| `bookmark` | `user_id, entry_id, post_id` |
| `notification` | `user_id, status, entry_id, entry_comment_id, post_id, post_comment_id` |
| `magazine` | `id, name, title, ap_id, is_adult, visibility, marked_for_deletion_at, ap_deleted_at` |
| `magazine_subscription` | `user_id, magazine_id` |
| `magazine_block` | `user_id, magazine_id` |
| `magazine_ban` | `user_id, magazine_id, expired_at` |
| `user_block` | `blocker_id, blocked_id` |
| `user` | `id, username, email, is_verified, is_banned, is_deleted, marked_for_deletion_at, visibility, type, ap_id, oauth_oidc_id, last_active, created_at` |

SQL rules (schema facts are from Mbin 1.16; `"user"` is quoted):
* **Members:** `ap_id IS NULL AND is_verified AND NOT is_banned AND NOT is_deleted AND marked_for_deletion_at IS NULL AND visibility = 'visible' AND type = 'Person' AND email IS NOT NULL AND oauth_oidc_id IS NOT NULL`.
* **Hot** (entries and posts alike): `last_active >= from AND created_at < to`, item `visibility = 'visible'`, `NOT is_adult`, magazine visible (`visibility = 'visible' AND ap_deleted_at IS NULL AND marked_for_deletion_at IS NULL AND NOT is_adult`), author not banned or deleted. A federated item (`ap_id IS NOT NULL`) is a candidate only if at least one local member subscribes to its magazine. `Participants` is distinct comment authors inside the window plus the item's author. `TopComments`: up to 3 visible, non adult comments by local authors, ordered by `up_votes + favourite_count` descending, for local items only. A post's `Title` is `digest.Plain(body, 120)`. URL: `siteURL + "/m/" + magazine.name + "/t/" + id + "/" + (slug or "-")` for entries, `/p/` for posts. Cap at the 200 best by `score + comment_count`.
* **View:** subscriptions, blocks, bans with `expired_at IS NULL OR expired_at > now()`; `Joined` for the given item keys from: authored, commented (any comment of that entry or post), `entry_vote`/`post_vote` with `choice <> 0`, `favourite`, `bookmark`, `notification` with `status = 'read'` naming the item or one of its comments; `Visited` is `last_active >= from`.
* **Stats:** local only: entries plus posts created in the window, comments of both created in the window, members (by the *Members* rule) created in the window.

The test fixture `testdata/schema.sql` creates exactly the allowlisted tables and columns (plus `id` primary keys and the types the Mbin agent report gives), and the test runs every query as a role granted only `Allowlist`, so a query that reaches outside it fails.

- [ ] **Step 1: Write failing tests**: `TestAllowlistExcludesCredentials` (no table or column from Global Constraints), `TestQueriesStayInsideAllowlist` (create role, `GRANT SELECT (cols) ON t`, connect with `SET ROLE`, run all four methods), `TestMembersRule` (one row per excluded case: remote, unverified, banned, deleted, marked, bot `Service`, no email, no OIDC subject; one included), `TestHotExcludes` (adult item, adult magazine, trashed item, deleted magazine, federated with no local subscriber, outside window), `TestHotFederatedWithSubscriber`, `TestHotURLs` (entry with slug, post without slug → `/-`, remote magazine `foo@remote.example.org`), `TestTopCommentsLocalOnly`, `TestViewJoined` (each joined signal, both kinds, including a read notification about a comment), `TestViewExpiredBanIgnored`, `TestStatsLocalOnly`.
- [ ] **Step 2: Run** with the database: FAIL.
- [ ] **Step 3: Implement** in `queries.go` (SQL constants) and `mbin.go`. Use one query per method, with `= ANY($n)` for item id arrays.
- [ ] **Step 4: Run**: PASS.
- [ ] **Step 5: Commit** `Read the week's threads and each member's activity from Mbin`.

### Task 6: The email

**Files:**
- Create: `internal/mail/mail.go`, `internal/mail/render.go`, `internal/mail/templates/digest.html`, `internal/mail/templates/digest.txt`, `internal/mail/mailtest/mailtest.go`, `internal/mail/mail_test.go`, `internal/mail/render_test.go`

**Interfaces:**
- Consumes: `digest.Digest`, `config.SMTP`.
- Produces:
```go
type Names struct { Instance, Meta string }
type Message struct { To, Subject, Text, HTML, Unsubscribe string }
func Render(d digest.Digest, n Names, siteURL, unsubscribeURL string, from, to time.Time) (Message, error)
type Sender interface { Send(ctx context.Context, m Message) error }
func NewSMTP(c config.SMTP) (Sender, error)
// mailtest.Recorder implements Sender and keeps Messages []mail.Message
```

Copy, fixed:
* Subject: `Your week on <Meta>`.
* Heading: `Your week on <Meta>`, then the dates `Jan 2 – Jan 9` style (`Jan 2` format, an en dash is a date range, not prose).
* Stats row labels: `new threads`, `comments`, `new members`.
* Featured label: `!<magazine name> · the big one this week`.
* Button: `See everything on <Instance>`, linking `siteURL`.
* Footer: `You get this because you're a member of <Meta>.` then link text `Unsubscribe from the weekly digest`.
* A post in the list is labelled `microblog`.

`Send` builds one go-mail message: plain text body plus HTML alternative, `From` with `FromName`, headers `List-Unsubscribe: <url>` and `List-Unsubscribe-Post: List-Unsubscribe=One-Click`. TLS modes and error sanitising exactly as account-service `internal/mail/mail.go`.

- [ ] **Step 1: Write failing tests**: `TestRenderNames` (Meta in subject, heading and footer; Instance only in the button), `TestRenderEscapes` (title `<script>` appears escaped in HTML), `TestRenderNoFeatured`, `TestRenderTextPartHasEveryLink`, `TestRenderPostLabelled`, `TestSendHeaders` (in process fake SMTP listener as in account-service `mail_test.go`: both headers present, multipart/alternative), `TestSendErrorHasNoAddress`.
- [ ] **Step 2: Run** `go test ./internal/mail/...`: FAIL.
- [ ] **Step 3: Implement** with embedded templates; HTML is one centred column, inline styles, matching the approved mockup (option C).
- [ ] **Step 4: Run**: PASS.
- [ ] **Step 5: Commit** `Render and send the digest email`.

### Task 7: The weekly run

**Files:**
- Create: `internal/run/schedule.go`, `internal/run/run.go`, `internal/run/schedule_test.go`, `internal/run/run_test.go`

**Interfaces:**
- Consumes: `store.Store` methods, `mbin.Source` methods (through the interface below), `digest.Build`, `mail.Render`, `mail.Sender`, `token.Sign`.
- Produces:
```go
func SendTime(now time.Time) time.Time      // the latest Monday 08:00 in now.Location() not after now
func Due(now time.Time) (week time.Time, ok bool) // ok while now < SendTime(now)+24h
type Source interface {
    Members(ctx context.Context) ([]digest.Member, error)
    Hot(ctx context.Context, from, to time.Time) ([]digest.Item, error)
    View(ctx context.Context, m digest.Member, from, to time.Time, items []digest.Item) (digest.View, error)
    Stats(ctx context.Context, from, to time.Time) (digest.Stats, error)
}
type Runner struct { Store *store.Store; Source Source; Sender mail.Sender; Names mail.Names; SiteURL, PublicURL string; Key []byte }
func (r *Runner) Week(ctx context.Context, week time.Time) error
func Loop(ctx context.Context, r *Runner, now func() time.Time, tick time.Duration)
```

`Week`: read `Members`, `Hot` and `Stats` first; any error returns before claiming anyone (Review Focus 2). Then per member: `Claim` (skip on false); `Unsubscribed` → `Finish(StatusUnsubscribed)`; `View`, `Build`; empty → `Finish(StatusEmpty)`; else `Render` with `PublicURL + "/u/" + token.Sign(Key, subject)`, `Send`, then `Finish(StatusSent)` or `Finish(StatusFailed)` with the sanitised error logged. A failure for one member does not stop the others. Logs carry counts, never addresses.

`Loop`: every `tick` (main passes one minute), `Due(now())`; when ok, `Week` once per week value until it returns nil; an error is logged as `"digest run failed; it is retried on the next tick until the day ends"` with the error.

- [ ] **Step 1: Write failing tests**: `TestSendTime` table (Monday 07:59 → previous Monday 08:00; Monday 08:00 → itself; Sunday → the Monday before; a DST change week in `Europe/London`), `TestDue` (Monday 08:30 ok, Tuesday 08:01 not), `TestWeekSendsOncePerMember` (fake source, `mailtest.Recorder`, real store: run `Week` twice, one message per member), `TestTwoRunnersOneStore` (concurrent `Week` on two runners: each member exactly once), `TestUnsubscribedGetsNothing`, `TestEmptyDigestNotSent`, `TestSourceErrorClaimsNobody` (Hot fails: no `sends` rows), `TestOneFailureDoesNotStopOthers`, `TestUnsubscribeLinkVerifies`.
- [ ] **Step 2: Run** `go test ./internal/run/`: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run**: PASS.
- [ ] **Step 5: Commit** `Send the digest each Monday at 08:00, once per member`.

### Task 8: The unsubscribe route, wiring and README

**Files:**
- Create: `internal/web/server.go`, `internal/web/server_test.go`
- Modify: `cmd/paisans-engagement-service/main.go`, `README.md`

**Interfaces:**
- Consumes: `token.Verify`, `store.Unsubscribe`, `store.Ping`, `run.Loop`.
- Produces: `web.NewServer(st *store.Store, key []byte) http.Handler`.

Routes: `GET /healthz` (DB ping, `ok` or 503); `GET /u/{token}` and `POST /u/{token}`: valid token unsubscribes and answers 200 `text/plain` `You're unsubscribed from the weekly digest.`; invalid answers 404 `text/plain` `This unsubscribe link is not valid.`. Anything else is 404. Security headers as account-service `securityHeaders`.

`main.run`: config → open and migrate the store → `mbin.Open` → `mail.NewSMTP` → `go run.Loop(ctx, runner, time.Now, time.Minute)` → HTTP server with graceful shutdown, as account-service. With an empty `SMTP.Host` it logs once `"no mail host is configured, so no digest is sent"` and starts no loop; unsubscribe still answers. The toolkit warns about that deployment (`engagement-without-smtp`) rather than refusing it, so the container must stay healthy.

README: intro paragraph (what it sends, when, to whom), bold lead bullets for *Who gets it*, *What it leaves out*, *Federated content*, *Unsubscribe*; a section **"Mbin must be clustered"** stating the service reads Mbin only when Mbin has `placement: cluster`, because its read only role is created in the cluster's Postgres; `## Environment` table from Task 1; `## What it reads` listing `mbin.Allowlist` and the excluded credential tables; `## Tests`.

- [ ] **Step 1: Write failing tests**: `TestUnsubscribeGet`, `TestUnsubscribePost` (one click, `List-Unsubscribe=One-Click` body), `TestBadTokenIs404`, `TestOtherPathsAre404`, `TestHealthz`.
- [ ] **Step 2: Run**: FAIL. **Step 3: Implement.** **Step 4: Run** `go vet ./... && go test -race ./...` with the database: PASS.
- [ ] **Step 5: Commit** `Answer unsubscribe links and run the weekly digest`.

---

# Part B: the `engagement` blueprint in `paisans-cli`

Working copy: worktree `src/paisans-stack/.worktrees/engagement-blueprint`, branch `feat/engagement-blueprint` from `develop`. Read `AGENTS.md` first. Test packages that read blueprints blank import `internal/blueprints/coretest`; packages calling apply also `internal/apply/applytest`. Run package scoped tests per task; the full `go test ./... && go vet ./... && gofmt -l .` once at the end, then again in a fresh worktree of the pushed branch (`.gitignore` drops `.env*` files: check `git status --ignored internal/blueprints/reserved/engagement`).

### Task 9: The reserved blueprint

**Files:**
- Create: `internal/blueprints/reserved/engagement/blueprint.yaml`, `compose.yaml.tmpl`, `.env.secret.tmpl`, `caddy.snippet.tmpl`
- Modify: `internal/config/config.go:74-79` (`BlueprintEngagement Blueprint = "engagement"`), `internal/blueprints/foundation.go` (`FoundationEngagement = "engagement"`), `internal/blueprints/testdata/core/foundation.yaml` (`engagement: "ghcr.io/paisans-software/paisans-engagement-service:0.1.*"`)
- Test: `internal/blueprints/engagement_test.go`

**Interfaces:**
- Produces: blueprint `engagement`; secrets `digest_signing_key` (generated, `password` format, which is 32 random bytes as unpadded base64url, self rotated, `rotation_cost: restart`) and `source_database_password` (generated). `blueprints.SendsMail("engagement") == true`.

`blueprint.yaml`, modelled on `reserved/account/blueprint.yaml`: services `app` (`foundation: "engagement"`) and `postgres`; `database: {engine: postgres}`; `port: 8083`; `config: {format: env, file: ".env"}`; `health: {path: "/healthz", status: [200]}`; `gate: {gateable: false}` with the comment that a member clicking unsubscribe from an inbox may not be signed in; `mail: reads`; no `oidc`.

**`engagement` is an optional foundation name.** Add `optionalFoundationNames = map[string]bool{FoundationEngagement: true}` beside `foundationNames`; `ParseFoundation` accepts a name in either map, requires only `foundationNames`, so the newest published core catalog, which predates it, still reads. A deployment that declares an `engagement` app against a foundation without the track is refused at render with `"the core catalog's foundation.yaml has no engagement image; it needs a catalog release that publishes one"`.

`compose.yaml.tmpl`: as account's, plus `- /etc/localtime:/etc/localtime:ro` so the service's 08:00 is the host's.

`.env.secret.tmpl` renders every Task 1 variable: `ENGAGEMENT_LISTEN_ADDR=:8083`, `ENGAGEMENT_PUBLIC_URL=https://{{ .App.Hostname }}`, `ENGAGEMENT_DATABASE_URL={{ .DSN }}`, `ENGAGEMENT_SIGNING_KEY={{ .Secret "digest_signing_key" }}`, SMTP from `.SMTP.*` as account's lines 48 to 53, and the Mbin values from `.Engagement.*` (Task 11).

- [ ] **Step 1: Write failing tests**: `TestEngagementBlueprintParses` (port, gate not gateable, SendsMail, the two secrets), `TestFoundationWithoutEngagementParses`, `TestEngagementSecretNamesUnique` (covered by `TestSelfRotatedSecretsAreUnique`; run it).
- [ ] **Step 2: Run** `go test ./internal/blueprints/`: FAIL. **Step 3: Implement.** **Step 4: Run**: PASS.
- [ ] **Step 5: Commit** `feat(engagement): add the reserved engagement blueprint`.

### Task 10: Validate rules

**Files:**
- Create: `internal/validate/engagement.go`, fixtures `internal/validate/testdata/engagement-*.yaml`
- Modify: `internal/validate/validate.go:190-196` (call it beside the account rules), `internal/validate/validate_test.go` (`TestRulesFire` rows)

**Interfaces:**
- Produces: `func (c *checker) engagement()`; helper `config.(*Config).EngagementSource() (name string, ok bool)` returning the one app whose blueprint is `mbin`.

| Rule id | Level | Fires when |
|---|---|---|
| `engagement-one-per-deployment` | refuse | more than one `engagement` app |
| `engagement-must-be-clustered` | refuse | the engagement app is not `placement: cluster` |
| `engagement-without-source` | refuse | no `mbin` app |
| `engagement-one-source` | refuse | more than one `mbin` app |
| `engagement-source-not-clustered` | refuse | the `mbin` app is pinned; message: "is pinned. The engagement digest reads Mbin only when Mbin has cluster placement, because its read only role is created in the cluster's Postgres." |
| `engagement-without-smtp` | warn | `!cfg.CanMail(app)` |

Each call passes a hint of at most 100 characters (`TestEveryCallPassesAHint`) and an actionable message (`TestRefusalMessagesAreActionable`). The comment above `engagement-source-not-clustered` states the limit in full.

- [ ] **Step 1:** fixtures (each `valid.yaml` plus one breakage) and `TestRulesFire` rows. **Step 2:** `go test ./internal/validate/`: FAIL. **Step 3:** implement. **Step 4:** PASS, including `TestValidFixtureIsQuiet`.
- [ ] **Step 5: Commit** `feat(engagement): refuse an engagement digest without one clustered Mbin`.

### Task 11: Rendering

**Files:**
- Create: `internal/render/engagement.go`, `internal/render/engagement_test.go`
- Modify: `internal/render/appview.go` (field `Engagement engagementValues` beside `Account` at `:171`, filled beside `:497`), `internal/render/testdata/deployment.yaml` (app `digest`, blueprint `engagement`, `placement: cluster`, hostname `digest.example.org`), `internal/render/testdata/secrets.fixture.yaml`, golden trees via `go test ./internal/render -update`

**Interfaces:**
- Consumes: `config.EngagementSource()`.
- Produces:
```go
type engagementValues struct { SourceDSN, SourceURL, InstanceTitle, MetaTitle string }
func mbinTitles(cfg *config.Config, app string) (instance, meta string)
func ReaderRole(engagementApp string) string // DBIdentifier(app) + "_reader", exported for internal/apply
```
`.env.secret.tmpl` gains `ENGAGEMENT_MBIN_DATABASE_URL={{ .Engagement.SourceDSN }}`, `ENGAGEMENT_MBIN_URL={{ .Engagement.SourceURL }}`, `ENGAGEMENT_INSTANCE_TITLE={{ envquote .Engagement.InstanceTitle }}`, `ENGAGEMENT_META_TITLE={{ envquote .Engagement.MetaTitle }}`.

`mbinTitles` reproduces `catalog:blueprints/mbin/.env.secret.tmpl:83-84` in Go: instance is the Mbin app's `settings.title` when a non empty string, else `cfg.ClientName(mbin)`; meta is `settings.meta_title` when set, else `"<display> · <community.name>"` when `community.name` is set, else the display name. `SourceDSN` is `dsn(...)` with user `ReaderRole(app)`, password `source_database_password`, database the Mbin app's `DBIdentifier`. `SourceURL` is `https://` plus the Mbin app's hostname.

- [ ] **Step 1: Write failing tests**: `TestMbinTitles` (the four cases of `mbin_settings_test.go:13-38`, asserting the same strings Mbin renders), `TestEngagementEnv` (every `ENGAGEMENT_` key present in the rendered `.env`, DSN user `digest_reader`, Mbin's database name).
- [ ] **Step 2: Run** `go test ./internal/render/`: FAIL. **Step 3: Implement**, then `go test ./internal/render -update` and read the golden diff: only the new `digest` files and the gateway snippet change. **Step 4: Run**: PASS.
- [ ] **Step 5: Commit** `feat(engagement): render the digest's Mbin connection and titles`.

### Task 12: The read only role and its grants

**Files:**
- Create: `internal/apply/sourcegrant.go`, `internal/apply/sourcegrant_test.go`
- Modify: `internal/apply/database.go` (`Bootstrap` gains readers; `bootstrapSQL` creates them; `replicaSQL` checks them), `internal/apply/apply.go` (run grants after the app stack loop, before `writeManifest`, at `:1569`), `internal/apply/export_test.go`

**Interfaces:**
- Consumes: `render.ReaderRole`, `config.EngagementSource()`.
- Produces:
```go
var mbinAllowlist = map[string][]string{ ... } // identical to engagement-service internal/mbin/allowlist.go
type Reader struct { Role, Database string; password string }
func grantSQL(r Reader) string
func runSourceGrants(plan *Plan, t Transport) error
```

`bootstrapSQL`: for each engagement app with a clustered Mbin source, the same `CREATE ROLE ... LOGIN` / `ALTER ROLE ... PASSWORD` pair the app roles use, with the password from `secrets.Apps[<engagement app>]["source_database_password"]`, then `ALTER ROLE <reader> SET default_transaction_read_only = on`. `replicaSQL` checks the reader exists, as it does app roles. No grant here: Mbin's tables do not exist before its first start.

`grantSQL`: `\connect <mbin db>`, then one `DO` block: if `to_regclass('public.entry') IS NULL`, `RAISE NOTICE` that Mbin has not created its tables yet and the next apply grants; otherwise `GRANT CONNECT ON DATABASE`, `GRANT USAGE ON SCHEMA public`, and `GRANT SELECT (<cols>) ON public.<table>` for each allowlist entry (quote `"user"` via `QuoteIdent`). A missing column or table at that point is an error and fails the apply (Review Focus 5). Run through `apply.Psql` on stdin with `NoStatementLog`, only where `runBootstrap` would run the primary script, after every app stack is healthy.

- [ ] **Step 1: Write failing tests**: `TestAllowlistMatchesService` (the table and column set equals the Task 5 table, written out in the test), `TestAllowlistExcludesCredentials` (Global Constraints list), `TestBootstrapCreatesReadOnlyReader` (`BootstrapSQL` holds `CREATE ROLE "digest_reader" LOGIN` and `default_transaction_read_only`), `TestGrantSQLWaitsForMbinTables`, `TestGrantSQLColumnGrants` (`GRANT SELECT ("id", ... ) ON public."user"`), `TestGrantsRunAfterAppStacks` (ordering in `execute`, modelled on `TestDatabasesExistBeforeAnyAppStarts`), `TestNoPasswordIsOnACommandLine` still passes.
- [ ] **Step 2: Run** `go test ./internal/apply/`: FAIL. **Step 3: Implement.** **Step 4: Run**: PASS.
- [ ] **Step 5: Commit** `feat(engagement): give the digest a read only role on Mbin's tables`.

### Task 13: Rotation

**Files:**
- Modify: `internal/secretsrotate/postgres.go` (beside `AppDatabase` at `:343`), `cmd/paisans/secretsrotate.go:604-606`, tests beside each

**Interfaces:**
- Produces: `secretsrotate.SourceReader(app string) Rotation`: `ALTER ROLE <reader> PASSWORD` on the leader, a login check, then the engagement stack re-rendered site by site, exactly as `AppDatabase` does for an app role. `digest_signing_key` rotates through the existing `AppSelf` path with cost `restart` (existing unsubscribe links stop working; say so in its `why`).

- [ ] **Step 1: Write failing tests** mirroring `AppDatabase`'s: SQL on leader, login checked, engagement stacks re-applied; `secrets rotate apps.digest.source_database_password` resolves to `SourceReader`.
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** `go test ./internal/secretsrotate/ ./cmd/paisans/`: PASS.
- [ ] **Step 5: Commit** `feat(engagement): rotate the digest's Mbin reader password`.

### Task 14: Documentation

**Files:**
- Modify: `README.md` (new `### The engagement digest` after `### The account service`, and the backup table row: its database holds only unsubscribes and sends), `CHANGELOG.md` (`### Added` bullet, at most 35 words), `docs/specs/2026-10-24-engagement-digest.md` (Status: approved, implemented by this branch)

The README section says: what it sends and when; that it reads Mbin through a read only role limited to named tables and columns; **that Mbin must have `placement: cluster`** and the rule id that refuses otherwise; that the first digest after a new deployment needs Mbin to have started once and a later apply to grant; how to rotate both secrets; the unsubscribe hostname needs no gate.

- [ ] **Step 1:** write. **Step 2:** full `go test ./... && go vet ./... && gofmt -l .`. **Step 3: Commit** `docs: describe the engagement digest`.

---

# Part C: `paisans-blueprints`

Branch `feat/engagement-image` from `main` in `src/blueprints`.

### Task 15: Publish the track

**Files:**
- Modify: `foundation.yaml` (after `account`: comment "0.1, the first engagement digest." and `engagement: "ghcr.io/paisans-software/paisans-engagement-service:0.1.*"`), `README.md:38` (list `engagement` among the reserved blueprints)

- [ ] **Step 1:** edit. **Step 2:** `cd ../paisans-stack/.worktrees/engagement-blueprint && CORE_CATALOG_DIR=<this checkout> go test ./internal/blueprints/` passes. **Step 3: Commit** `Publish the engagement digest's image track`.

The PR body says two things. It merges after `paisans-engagement-service` v0.1.0 publishes its image, since a track with no matching tag fails every apply that resolves it. And a toolkit older than Part B refuses the unknown name `engagement` (`ParseFoundation`, `internal/blueprints/foundation.go`), so `readCore` skips this release for the one before it, which is the documented behaviour, not a break.

---

## Pull requests

* `paisans-engagement-service`: `feat/weekly-digest` into `develop`.
* `paisans-cli`: `docs/engagement-digest-design` (spec and this plan) into `develop`; `feat/engagement-blueprint` into `develop`.
* `paisans-blueprints`: `feat/engagement-image` into `main`.

No PR mentions Outline. No tag or release is cut: v0.1.0 of the service and the catalog release are the founder's.
