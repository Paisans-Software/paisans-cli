# ops-notifier implementation plan

> **For agentic workers:** implement task by task; each task ends with its
> tests passing. The design is `docs/specs/2026-10-29-ops-notifier.md`.

**Goal:** Beszel alerts reach each operator in a direct Matrix room, kept
true by a colocated service, with no step by hand.

**Architecture:** a Go service, `paisans-ops-notifier-service`, runs pinned
beside the homeserver. It signs in to MAS as an admin client and to the
Beszel hub as a superuser of its own, keeps one bot, one room per operator
and one webhook per operator, and stores what it did in a state file. The
toolkit renders it as the reserved blueprint `ops-notifier`, which consumes
`matrix-homeserver` (colocated) and a new `beszel-hub` role.

**Tech stack:** Go 1.25, `net/http`, `golang.org/x/time/rate`; the toolkit's
blueprint, render and validate packages.

---

## Part 1: the service repository

Repository `Paisans-Software/paisans-ops-notifier-service`, MIT, default
branch `develop`, layout copied from `paisans-engagement-service`.

### Task 1.1: skeleton

- `go.mod` (`github.com/paisans-software/paisans-ops-notifier-service`),
  `LICENSE`, `README.md`, `CHANGELOG.md`, `.gitignore`, `.dockerignore`.
- `.github/workflows/test.yml` (vet, `go test -race` on pull requests and on
  pushes to `develop` and `main`), `changelog.yml` (the engagement service's),
  `release.yml` (a `v*` tag publishes `ghcr.io/paisans-software/paisans-ops-notifier-service:X.Y.Z`
  for amd64 and arm64; nothing else publishes).
- `Dockerfile`: a build stage, then `gcr.io/distroless/static-debian12:nonroot`,
  `HEALTHCHECK` running the binary's `healthcheck` subcommand.

### Task 1.2: configuration (`internal/config`)

Read and check every `OPS_NOTIFIER_*` variable; refuse an empty operator
list, a missing secret or a URL that does not parse. Test: a full
environment loads; each missing variable is named in the error; no secret
appears in an error.

### Task 1.3: MAS and Matrix clients

Copy `internal/mas` (with `mastest`) and `internal/matrix` (with
`matrixtest`) from the engagement service, keeping their MAS v1.28.0 and
Synapse citations. Add `Join` to `matrix`.
Tests come with them.

### Task 1.4: PocketBase client (`internal/beszel`)

`AuthSuperuser`, `Users`, `ExternalSubject(userID, provider)`,
`Settings(userID)` (with the record id, or none), `CreateSettings(userID)`,
`SetWebhooks(recordID, settings)`, `DeleteUser(userID)`. A re-auth on 401.
`beszeltest`: a fake hub holding users, external auths and settings in
memory, with the superuser check and the creation hook's key dropping.

### Task 1.5: state (`internal/state`)

One JSON file, `state.json`, written to a temporary file and renamed, mode
0600, guarded by a mutex. Test: round trip; a torn write leaves the old file.

### Task 1.6: the bot (`internal/bot`)

Port the engagement service's `bot/start.go` (account, adopt, token, renew,
call) onto the state file. Device `OPSBOT`. Test against `mastest` and
`matrixtest`: first start creates the user and session; a refused token is
regenerated; a gone session is created again; a taken name is refused.

### Task 1.7: webhooks (`internal/notify`)

`URL(token, host, room)`, `Ours(url, host, room) bool`, and
`Apply(webhooks, host, room, token) (out, changed)`, `Remove(webhooks, host,
rooms...)`. Test: other URLs untouched; an old token rewritten; an edited
room no longer ours.

### Task 1.8: the reconcile pass (`internal/reconcile`)

The spec's *The loop*, steps 1 to 4. Tests over `beszeltest`, `mastest` and
`matrixtest`: a new operator gets a room and a URL; a second pass changes
nothing; a removed URL is an opt out with a notice; a left room is an opt out;
an encrypted room is replaced and the URL moved; a non-operator with an
`oidc` link is deleted and their room left; the break glass user and a user
with no link are kept; a regenerated token is rewritten; an operator with no
Matrix account is skipped.

### Task 1.9: the listener (`internal/reconcile`, `listen.go`)

`/sync` long-poll as the engagement service's `Listen`, invitations only on
the first sync. Invitation from an operator: unencrypted, join and ask;
encrypted, join, notice, leave, open a fresh room and ask; from anyone else,
reject. `subscribe` in the pending or current room writes the URL and clears
the opt out. Leave or ban of the operator in their room is an opt out.
Tests for each.

### Task 1.10: `main`

`serve` (the default): load config and state, start the bot (retrying with
backoff while MAS or the homeserver is down), run the pass on a ticker and
the listener, serve `/healthz`. `healthcheck`: GET `/healthz` on the listen
port, exit 0 on 200.

## Part 2: the toolkit (`paisans-cli`)

### Task 2.1: `beszel` provides `beszel-hub`

`blueprint.yaml`: `provides: [{role: beszel-hub, values: {url: "'http://' + app.mesh_address + ':8084'"}}]`.
`setup.sh.secret.tmpl`: for each `.Consumers "beszel-hub"` with
`beszel_superuser_password`, upsert the superuser `paisans-link-<name>@<domain>`;
delete every other `paisans-link-` superuser. Golden: the stats app's
`setup.sh`.

### Task 2.2: the reserved blueprint `ops-notifier`

`internal/blueprints/reserved/ops-notifier/`: `blueprint.yaml` (v4; services
`app` from foundation `ops-notifier`; port 8085; `/healthz`; not gateable;
the two consumes; settings `homeserver`, `hub`, `bot_localpart`,
`bot_display_name`; generated and self rotated `mas_admin_client_secret`,
`beszel_superuser_password`; storage local only), `compose.yaml.tmpl`,
`.env.secret.tmpl`, `caddy.snippet.tmpl`. `config.BlueprintOpsNotifier`;
`blueprints.FoundationOpsNotifier`, optional in a foundation.

### Task 2.3: render values

`opsNotifierValues`: the operator emails, the break glass email, the
notifier's own superuser email. Tests and goldens: add an `ops-notifier` app
to `internal/render/testdata/deployment.yaml` beside a synapse app if the
fixture has one, else a unit render test.

### Task 2.4: validate

`ops-notifier-without-homeserver`, `ops-notifier-without-hub`,
`one-ops-notifier`, with fixtures; `colocated-app-elsewhere` already applies.

### Task 2.5: docs

README: a section under *Server and container stats*, the rules table, the
secrets table rows. CHANGELOG: one bullet. `testdata/core/foundation.yaml`:
the `ops-notifier` track.

## Part 3: the core catalog

A `paisans-blueprints` pull request adding
`ops-notifier: "ghcr.io/paisans-software/paisans-ops-notifier-service:0.1.*"`
to `foundation.yaml`. It waits for the service's first tag and a toolkit
release that knows the blueprint; it is not merged before both.
