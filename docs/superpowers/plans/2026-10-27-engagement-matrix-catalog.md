# Engagement on Matrix: the core catalog's synapse blueprint — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The synapse blueprint mounts Matrix Authentication Service's admin API on a mesh-only listener, declares one admin client for each engagement app that consumes the homeserver, offers the listener's address as `mas_admin_url`, and refuses `/api/admin` at the gateway.

**Architecture:** Everything is template and YAML in one blueprint directory of `paisans-blueprints`. MAS gets a second listener `admin` (resource `adminapi` only) on port 18010, published by compose on the site's mesh address only. `mas.yaml` ranges over `.Consumers "matrix-homeserver"` (a toolkit feature built on paisans-cli `feat/engagement-matrix`) and renders a static client plus a `policy.data.admin_clients` entry for each consumer that shares `mas_admin_client_secret`. The catalog has no tests of its own: every task is checked by rendering it through a scratch copy of the paisans-cli branch's golden test, and by running the rendered files in Docker.

**Tech Stack:** Go `text/template` (toolkit render), CEL (`provides` values), MAS 1.x YAML config, Caddyfile, Docker Compose.

**Spec:** `docs/specs/2026-10-27-engagement-matrix.md` in paisans-cli, and the binding contract `docs/superpowers/plans/2026-10-27-engagement-matrix-contract.md` beside this plan. Read both.

**Repository:** `/Users/wash/Developer/paisans.community/src/blueprints/.worktrees/engagement-matrix`, branch `feat/engagement-matrix` from `main` (at `v0.8.2`). All paths below are relative to it unless absolute. `CAT` means this path.

## Global Constraints

- Names are the contract's, verbatim: role `matrix-homeserver`, value `mas_admin_url`, listener `admin`, resource `adminapi`, port `18010`, secret `mas_admin_client_secret`, client id `stableULID (printf "%s-mas-admin" .Name)`, `client_auth_method: client_secret_basic`. Rename nothing.
- `mas_admin_url` is exactly `"'http://' + app.mesh_address + ':18010'"`.
- The web listener never mounts `adminapi`.
- Every MAS key added is cited in a comment with file and line, read at MAS tag **v1.28.0** (the tag this plan was read at; see *MAS findings*).
- Comments and docs describe current state only: no "no longer", no history, no rejected alternatives.
- Never `git stash`. Never mention Outline or tickets in commits, PRs or files.
- Nothing from the harness directory is committed, and nothing in the paisans-cli worktree is modified: the harness works on a copy.
- Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; a PR body ends with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`. Commit subjects follow the repo's `feat:` / `fix:` / `docs:` style.
- The repo has **no CHANGELOG file**. A release's note is its signed annotated tag message, one line: `core catalog X.Y.Z: <what changed>`.

## Review Focus

1. **A spelling of `/api/admin` that dodges the matcher** — `/API/Admin/v1/users`, `/api/%61dmin/v1/users`, `//api/admin/v1/users`, `/api/admin/` and a dot segment. A person expects 404 from the gateway for all of them. Pinned in Task 3, step 4.
2. **A deployment with no engagement app** (or only Element consuming the homeserver). The person expects MAS to start exactly as before: no `clients:` or `policy:` key, not an empty or null one. Pinned in Task 2, steps 4 and 7.
3. **Rotating `mas_admin_client_secret`.** The person expects the old secret to stop working and the new one to work once both apps restart. MAS re-syncs static clients on start. Pinned in Task 2, step 8.
4. **Removing the engagement app.** Its static client row stays in MAS's database (a start never prunes), but it must no longer get `urn:mas:admin`. Pinned in Task 2, step 8.
5. **The public listener.** `/api/admin/v1/*` on MAS's web listener (18009, which the gateway proxies to) must be 404 even with a valid admin token, so the API is unreachable even when the gateway rule is bypassed. Pinned in Task 2, step 8.

## MAS findings (read at v1.28.0, 2026-10-27)

Read with `gh api repos/element-hq/matrix-authentication-service/contents/<path>?ref=v1.28.0`:

- `crates/config/src/sections/http.rs:259-299`: resources are tagged `name`, lowercase. `AdminApi` is `name: adminapi`, "served at `/api/admin/v1`". `crates/handlers/src/admin/mod.rs:194-222` also serves `/api/spec.json` and the Swagger UI at `/api/doc/` on that listener. Binds are `address: "<host>:<port>"` (lines 85-108).
- `crates/config/src/sections/clients.rs:59-99,127-135`: a static client has `client_id` (a ULID, regex line 63), `client_auth_method`, and `client_secret` (flattened, so it sits at the client's top level). It has no grant types key: `client_secret_basic` requires `client_secret`.
- `crates/storage-pg/src/oauth2/client.rs:585-640`: `upsert_static` sets all four grant types, client_credentials among them, to `true` for every static client, and updates the secret on conflict.
- `crates/handlers/src/oauth2/token.rs:808-829`: the client credentials grant checks that grant type, then evaluates the authorization grant policy with `user: None`.
- `policies/authorization_grant/authorization_grant.rego:63-68`: `urn:mas:admin` is allowed for `grant_type == "client_credentials"` when `input.client.id` is in `data.admin_clients`. Lines 82-85: `urn:matrix:client:api:*` is allowed only for interactive grants. That matters to the service, not here: the bot's token comes from `/api/admin/v1/personal-sessions`, not from this client's grant.
- `crates/config/src/sections/policy.rs:143-145`: `policy.data` is arbitrary JSON passed to the policy. `docs/topics/admin-api.md` ("Automated tools") and `docs/reference/configuration.md:473-477` confirm `policy.data.admin_clients` as a list of client ids.
- `crates/cli/src/sync.rs:405-440`: `server` upserts every configured static client on start. A client that is no longer in the file is only warned about, and deleted only by `config sync --prune`.
- Oldest release: `/api/admin/v1/personal-sessions` first appears at **v1.5.0** (absent at v1.4.0). `/users` and `/upstream-oauth-links?filter[subject]` exist from v1.0.0. That makes **1.5.0** the oldest MAS release that has every endpoint the service uses. Task 2 confirms the figure against the service's README.

## Test harness (scratch, never committed)

The catalog repo has no CI and no tests. A catalog change is checked the way the toolkit checks its own test copy of the core catalog: paisans-cli's `internal/blueprints/coretest` turns `internal/blueprints/testdata/core` into a tagged git repo, and `internal/render`'s `TestGoldenTree` renders the fixture `internal/render/testdata/deployment.yaml` through it. That fixture already has `chat` (synapse, pinned to `vm`, mesh address `10.44.0.3`), `web` (element) and `digest` (engagement, clustered on `home-a`/`home-b`). The harness runs that test on a **copy** of the paisans-cli branch whose test catalog is replaced by `CAT`.

**Prerequisite. Stop if it fails.** The paisans-cli worktree `TK=/Users/wash/Developer/paisans.community/src/paisans-stack/.worktrees/engagement-matrix` must have contract items 1-4 committed (`app.mesh_address`, `optional`, `shared_secrets`, `.HasLinked`/`.LinkedApp`/`.Consumers`), the engagement blueprint's matrix lines, and `mas_admin_client_secret` for `digest` in `internal/render/testdata/secrets.fixture.yaml`. Check:

```sh
grep -n mesh_address $TK/internal/blueprints/cel.go
grep -rn 'Consumers' $TK/internal/render/*.go | grep -v _test | head -3
grep -n mas_admin_client_secret $TK/internal/render/testdata/secrets.fixture.yaml
```

Each must print a match. If one does not, the toolkit half is not ready: report that and do not start Task 1.

Let `H` be a new directory in your scratchpad. Write `$H/render.sh`:

```sh
#!/bin/sh
# render.sh <out> [no-digest]: render the toolkit fixture through CAT into <out>.
set -eu
H=$(cd "$(dirname "$0")" && pwd)
TK=/Users/wash/Developer/paisans.community/src/paisans-stack/.worktrees/engagement-matrix
CAT=/Users/wash/Developer/paisans.community/src/blueprints/.worktrees/engagement-matrix
rm -rf "$H/toolkit"
rsync -a --exclude .git "$TK/" "$H/toolkit/"
rsync -a --delete --exclude .git --exclude .worktrees "$CAT/" "$H/toolkit/internal/blueprints/testdata/core/"
if [ "${2:-}" = no-digest ]; then
  sed -i '' '/^  digest:$/,/^  health:$/{/^  health:$/!d;}' "$H/toolkit/internal/render/testdata/deployment.yaml"
fi
git -C "$TK" rev-parse --short HEAD > "$H/toolkit-commit"
(cd "$H/toolkit" && go test ./internal/render -run '^TestGoldenTree$' -update -count=1)
rm -rf "$1"; cp -R "$H/toolkit/internal/render/testdata/golden" "$1"
(cd "$H/toolkit" && go build -o "$H/paisans" ./cmd/paisans)
```

Then, before Task 1, on the unchanged catalog:

- [ ] `sh $H/render.sh $H/base` — PASS (log line `golden tree rewritten`).
- [ ] `diff -ru $TK/internal/render/testdata/golden $H/base`. The only expected differences come from the toolkit's test copy lagging the catalog: the `email:` section in `vm/.../chat/mas.yaml`, from catalog v0.8.2. Every task below diffs against `$H/base`, never against the toolkit's committed golden.
- [ ] `(cd $H/toolkit && go test ./internal/blueprints/ ./internal/render/ -count=1 2>&1 | grep -- '--- FAIL' | sort > $H/base-fails.txt)`. These failures come from the test copy changing, not from this work. Each task's "no new failures" check compares against this list.
- [ ] `$H/paisans blueprint lint $CAT/blueprints/synapse` — PASS.

`no-digest` deletes the `digest` app from the copy's fixture, for a deployment without an engagement app. If validate then refuses the fixture, the refusal names whatever else refers to `digest`: delete that from the copy too, never from `TK`.

Docker is available locally (29.x) and the e2e steps use it. Pin `ghcr.io/element-hq/matrix-authentication-service:1.28.0`, `postgres:17` and `caddy:2.11.4`.

---

### Task 1: The admin listener, its mesh-only port, and `mas_admin_url`

**Files:**
- Modify: `blueprints/synapse/blueprint.yaml:15-20` (mas service comment), `:25-33` (provides)
- Modify: `blueprints/synapse/mas.yaml.secret.tmpl:46-67` (listeners)
- Modify: `blueprints/synapse/compose.yaml.tmpl:63-68` (mas ports)
- Modify: `README.md` ("Writing a blueprint", the `provides` sentence)

**Interfaces:**
- Consumes: toolkit CEL variable `app.mesh_address` (string) and template field `.MeshAddress`.
- Produces: role value `matrix-homeserver.mas_admin_url` = `http://<mesh address>:18010`, which the toolkit renders as `ENGAGEMENT_MAS_ADMIN_URL`. MAS answers `/api/admin/v1/*` on container port 18010, published at `<mesh address>:18010`.

- [ ] **Step 1: State the expected render.** After this task, `diff -ru $H/base $H/t1` must show exactly these changes:
  - `vm/srv/paisans/f2a9/chat/mas.yaml`: a second listener after `web`, with `name: admin`, `resources: [ {name: adminapi} ]`, `binds: [ {address: "[::]:18010"} ]` and `proxy_protocol: false`. The `web` listener's resources are unchanged.
  - `vm/srv/paisans/f2a9/chat/compose.yaml`: under `mas.ports`, a second entry `"10.44.0.3:18010:18010"`.
  - `home-a/.../digest/.env` and `home-b/.../digest/.env`: `ENGAGEMENT_MAS_ADMIN_URL=http://10.44.0.3:18010`, quoted however the toolkit's template quotes it.
  - Nothing else.

- [ ] **Step 2: Run it before the change.** `sh $H/render.sh $H/t1`. Expected: the render fails on `mas_admin_url` (the toolkit's engagement `.env` asks for a value the role does not offer), or the `.env` lacks the line. Either way it is not the state step 1 describes.

- [ ] **Step 3: Implement.**
  - `blueprint.yaml` `provides[0].values`: add `mas_admin_url: "'http://' + app.mesh_address + ':18010'"`. Extend the comment above it in one sentence: mas_admin_url is MAS's admin API listener, on the mesh address of the site the homeserver is pinned to. Add a sentence to the `mas` service comment: MAS's admin API listens on 18010, one above its web listener, on the mesh only (mas.yaml and compose bind it).
  - `mas.yaml.secret.tmpl`: add the `admin` listener after `web`, binding `"[::]:18010"`. The comment says that it carries the admin API and nothing else, that compose publishes it on the mesh address only so the gateway never proxies to it, and that the web listener does not mount `adminapi`. Cite `crates/config/src/sections/http.rs:259-299 at tag v1.28.0`. Also change the header line "Every key below was read ... v1.24.0" to say keys are cited with the tag each was read at.
  - `compose.yaml.tmpl` `mas.ports`: add `"{{ .MeshAddress }}:18010:18010"`. Extend the existing mesh-only comment with one sentence: the second port is the admin API, which only apps on the mesh reach.
  - `README.md`: change "(Synapse's `matrix-homeserver` offers `base_url` and `server_name`)" to also list `mas_admin_url`.

- [ ] **Step 4: Run it again.** `sh $H/render.sh $H/t1 && diff -ru $H/base $H/t1`. Expected: exactly the step 1 diff.

- [ ] **Step 5: Check that MAS accepts the file.** Run:
  `docker run --rm -v $H/t1/vm/srv/paisans/f2a9/chat/mas.yaml:/data/mas.yaml:ro ghcr.io/element-hq/matrix-authentication-service:1.28.0 config check --config /data/mas.yaml`
  Expected: exit 0. A warning about the database being unreachable is acceptable. An error about `http.listeners` is not.

- [ ] **Step 6: Lint and check for no new failures.** `$H/paisans blueprint lint $CAT/blueprints/synapse` passes. `(cd $H/toolkit && go test ./internal/blueprints/ ./internal/render/ -count=1 2>&1 | grep -- '--- FAIL' | sort | comm -13 $H/base-fails.txt -)` prints nothing. `TestGoldenTree` does not count here, because `-update` rewrote it.

- [ ] **Step 7: Commit.**
  ```bash
  git -C $CAT add blueprints/synapse/blueprint.yaml blueprints/synapse/mas.yaml.secret.tmpl blueprints/synapse/compose.yaml.tmpl README.md
  git -C $CAT commit -m "feat(synapse): mount MAS's admin API on a mesh-only listener and offer it as mas_admin_url"
  ```

### Task 2: An admin client for each engagement app

**Files:**
- Modify: `blueprints/synapse/mas.yaml.secret.tmpl` (new `clients:` and `policy:` blocks, after `account:` and before the `email` conditional)
- Modify: `README.md` (new section `## Synapse's admin API`, after "First sign-in behind the gateway")

**Interfaces:**
- Consumes: `.Consumers "matrix-homeserver"` → `[]Consumer` sorted by `Name`. `Consumer.Name string`, `Consumer.Secrets map[string]string`. `stableULID string → string`. `jsonquote`. Task 1's admin listener.
- Produces: for each consumer `c` with a non-empty `index c.Secrets "mas_admin_client_secret"`, a static MAS client `client_id: stableULID (printf "%s-mas-admin" c.Name)`, `client_auth_method: client_secret_basic`, `client_secret: <that secret>`, listed in `policy.data.admin_clients`. The engagement side renders the same id and secret as `ENGAGEMENT_MAS_CLIENT_ID` / `ENGAGEMENT_MAS_CLIENT_SECRET`.

- [ ] **Step 1: State the expected render.** `diff -ru $H/t1 $H/t2` shows only `vm/.../chat/mas.yaml` gaining:
  ```yaml
  clients:
    - client_id: <ID>
      client_auth_method: client_secret_basic
      client_secret: "<SECRET>"
  policy:
    data:
      admin_clients:
        - <ID>
  ```
  Here `<ID>` and `<SECRET>` equal `ENGAGEMENT_MAS_CLIENT_ID` and `ENGAGEMENT_MAS_CLIENT_SECRET` in `$H/t2/home-a/srv/paisans/f2a9/digest/.env`, compared after stripping quotes. There is exactly one client: `web` (Element) consumes the role but shares no secret, so it gets none.

- [ ] **Step 2: Run it before the change.** `sh $H/render.sh $H/t2 && diff -ru $H/t1 $H/t2`. Expected: no diff, which is a FAIL against step 1.

- [ ] **Step 3: Implement.** In `mas.yaml.secret.tmpl`, first compute whether any consumer shares the secret (`{{- $admins := false }}{{ range ($.Consumers "matrix-homeserver") }}{{ if index .Secrets "mas_admin_client_secret" }}{{ $admins = true }}{{ end }}{{ end }}`). Only when it is true, render `clients:` and `policy:`, each ranging again with the same filter. Write `client_id` bare (a ULID needs no quoting) and `client_secret` through `jsonquote`. The comment above them:
  - each engagement app that links to this homeserver gets an admin client, with the secret it shares (`mas_admin_client_secret`, stored under that app and rotated with it);
  - a static client may use every grant type (`crates/storage-pg/src/oauth2/client.rs:585-640`), and `admin_clients` is what lets client credentials carry `urn:mas:admin` (`policies/authorization_grant/authorization_grant.rego:63-68`);
  - the client takes its token from `/oauth2/token` on the web listener and uses it on the admin listener;
  - a start re-syncs these clients from this file (`crates/cli/src/sync.rs:405-440`), so a rotated secret takes effect when MAS restarts. A client removed from the file keeps its database row but leaves `admin_clients`, so it can no longer get admin access.

  Cite `clients.rs:59-99,127-135` and `policy.rs:143-145`, all at tag v1.28.0. When no consumer shares the secret, render a single comment line in place of both blocks, saying no engagement app links here so MAS has no admin client.

- [ ] **Step 4: Run it again, with and without the engagement app.** `sh $H/render.sh $H/t2 && diff -ru $H/t1 $H/t2` shows exactly step 1. Then `sh $H/render.sh $H/t2-none no-digest`. In `$H/t2-none/vm/.../chat/mas.yaml`, `grep -c '^clients:\|^policy:'` prints `0`, and the file still has the `admin` listener.

- [ ] **Step 5: README.** Add `## Synapse's admin API`, one short paragraph per point:
  - MAS's admin API is on a listener of its own, port 18010 on the site's mesh address, and nowhere else. The gateway refuses `/api/admin` on the homeserver's hostnames.
  - The blueprint declares one admin client for each engagement app that links to the homeserver, with the client secret the two share. Rotating it (`paisans secrets rotate <engagement app> mas_admin_client_secret`) re-renders and restarts both.
  - The admin client needs Matrix Authentication Service 1.5.0 or newer, the first release with every admin API endpoint the engagement service uses.

  Before committing, open the service's README on branch `feat/matrix-bot` (`/Users/wash/Developer/paisans.community/src/paisans-engagement-service/.worktrees/matrix-bot/README.md`). If it names a different oldest MAS release, use that one.

- [ ] **Step 6: Lint and check for no new failures.** As in Task 1, step 6.

- [ ] **Step 7: MAS e2e, setup.** In `$H/mas`, write a `compose.yaml` with two services:
  - `postgres` (`postgres:17`, `POSTGRES_USER/PASSWORD/DB` all `mas`);
  - `mas` (the pinned MAS image), with `command: ["server", "--config", "/data/mas.yaml", "--config", "/data/override.yaml"]`, `depends_on: [postgres]`, volumes `$H/t2/vm/srv/paisans/f2a9/chat/mas.yaml:/data/mas.yaml:ro` and `./override.yaml:/data/override.yaml:ro`, and ports `127.0.0.1:18009:18009` and `127.0.0.1:18010:18010`.

  `override.yaml` sets `database: {host: postgres, port: 5432, username: mas, password: mas, database: mas}`. If MAS fails to start on the fixture's upstream provider or SMTP host, also add `upstream_oauth2: {providers: []}` or `email: {transport: blackhole}`. Neither is under test.

  `docker compose -p catmas up -d`, then poll `curl -sf http://127.0.0.1:18009/.well-known/openid-configuration` until it answers (60 s at most). Then repeat the start with the `t2-none` file. MAS starts, and its log has no config error.

- [ ] **Step 8: MAS e2e, assertions.** Take `ID` and `SECRET` from `$H/t2/home-a/srv/paisans/f2a9/digest/.env`. `T` is the `access_token` from `curl -s -u "$ID:$SECRET" -d grant_type=client_credentials -d scope=urn:mas:admin http://127.0.0.1:18009/oauth2/token`. Expect each of these:
  - the token response is 200 with an `access_token` and `scope` of `urn:mas:admin`;
  - `curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $T" http://127.0.0.1:18010/api/admin/v1/users` → `200`;
  - the same request to port **18009** → `404` (Review Focus 5);
  - the token request with `-u "$ID:wrong"` → `401`;
  - **rotation** (Review Focus 3): write a new `client_secret` into a copy of the rendered mas.yaml and mount that copy instead, then `docker compose -p catmas up -d --force-recreate mas`. The old secret → `401`, the new one → `200`;
  - **removal** (Review Focus 4): add `policy: {data: {admin_clients: []}}` to `override.yaml` and recreate `mas`. The token request with the valid secret is refused (400 `invalid_scope` or `access_denied`; record which), and no token is issued.

  Then `docker compose -p catmas down -v`.

- [ ] **Step 9: Commit.**
  ```bash
  git -C $CAT add blueprints/synapse/mas.yaml.secret.tmpl README.md
  git -C $CAT commit -m "feat(synapse): declare a MAS admin client for each engagement app that links to the homeserver"
  ```

### Task 3: The gateway refuses `/api/admin`

**Files:**
- Modify: `blueprints/synapse/caddy.snippet.tmpl:87-97` (before the bare `handle`)
- Modify: `blueprints/synapse/caddy.snippet.wellknown.tmpl:29-35` (before the bare `handle`)

**Interfaces:**
- Consumes: nothing new.
- Produces: on every hostname the synapse app answers, `/api/admin` and `/api/admin/*` get `respond 404` from Caddy and never reach an upstream.

- [ ] **Step 1: State the expected render.** `diff -ru $H/t2 $H/t3` shows two handles, `handle /api/admin {` and `handle /api/admin/* {`, each with body `respond 404`, in `vm/.../infra/caddy/etc/snippets/chat.caddy` and in `chat-wellknown.caddy`. Nothing else changes.

- [ ] **Step 2: Write the live check, `$H/caddy-check.sh <out>`.** It runs `caddy:2.11.4` with this Caddyfile:
  ```
  { auto_https off
    admin off }
  :8080 { import /snippets/chat.caddy }
  :8081 { import /snippets/chat-wellknown.caddy }
  ```
  It mounts `<out>/vm/srv/paisans/f2a9/infra/caddy/etc/snippets` at `/snippets`, publishes 127.0.0.1:18080 and :18081, and asserts status codes with `curl --path-as-is --max-time 15 -s -o /dev/null -w '%{http_code}'`. The upstreams at 10.44.0.3 are unreachable, so a proxied request returns `502`. That code is how the script tells "reached MAS" from "refused".

  | Port | Path | Expect |
  |---|---|---|
  | 18080 | `/api/admin` | 404 |
  | 18080 | `/api/admin/` | 404 |
  | 18080 | `/api/admin/v1/users` | 404 |
  | 18080 | `/API/Admin/v1/users` | 404 |
  | 18080 | `/api/%61dmin/v1/users` | 404 |
  | 18080 | `//api/admin/v1/users` | 404 |
  | 18080 | `/api/x/../admin/v1/users` | 404 |
  | 18080 | `/api/adminx` | 502 |
  | 18080 | `/_matrix/client/versions` | 502 |
  | 18080 | `/_matrix/client/v3/login` | 502 |
  | 18081 | `/api/admin/v1/users` | 404 |
  | 18081 | `/.well-known/matrix/server` | 200 |

  It prints each row that misses and exits non-zero if any does.

- [ ] **Step 3: Run it before the change.** `sh $H/caddy-check.sh $H/t2`. Expected: FAIL. The 18080 `/api/admin*` rows return 502, because they fall through to MAS.

- [ ] **Step 4: Implement.** In `caddy.snippet.tmpl`, add the two handles right before the bare `handle {`. The comment says:
  - MAS's admin API is mounted only on its admin listener, on the mesh;
  - these refuse `/api/admin` here so that it stays unpublished whatever MAS's web listener mounts;
  - both paths are longer than the bare handle and overlap nothing under `/_matrix`, `/_synapse` or `/.well-known/matrix`, so they take no request from the routes above (the file's own sorting note applies).

  In `caddy.snippet.wellknown.tmpl`, add the same two handles before its bare `handle`, with a one-line comment that they hold the refusal on this name too. Then run `sh $H/render.sh $H/t3 && diff -ru $H/t2 $H/t3`; the diff is exactly step 1. Run `sh $H/caddy-check.sh $H/t3`; PASS, all rows (Review Focus 1). If a row other than a lookalike fails, fix the matcher rather than the table.

- [ ] **Step 5: Lint and check for no new failures.** As in Task 1, step 6.

- [ ] **Step 6: Commit.**
  ```bash
  git -C $CAT add blueprints/synapse/caddy.snippet.tmpl blueprints/synapse/caddy.snippet.wellknown.tmpl
  git -C $CAT commit -m "feat(synapse): refuse /api/admin at the gateway on every hostname of the homeserver"
  ```

### Task 4: Require the toolkit release, and release the catalog

Begin this task only after the paisans-cli release that contains contract items 1-4 is published. Release order is toolkit, then service, then catalog. A branch build reports `0.3.0-dev`, which is older than any `0.3.0-rc.N`, so with this line in place `blueprint lint` fails on the harness build. That is why this task comes last and is checked with released binaries.

**Files:**
- Modify: `blueprints/synapse/blueprint.yaml:3-4` (after `name:`)

**Interfaces:**
- Produces: `requires_toolkit: ">= <V>"`, where `<V>` is the version of the first paisans-cli release whose CHANGELOG lists `.Consumers` / shared secrets. Find it with `gh release list -R Paisans-Software/paisans-cli` and that tag's `CHANGELOG.md`. The expected form is `0.3.0-rc.12`. Write it with no `v`.

- [ ] **Step 1: Build both binaries.** `<V>` and the release before it, `<P>`: `git -C $TK archive v<X> | tar -x -C $H/rel-<X>` for each, then `go build -o $H/paisans-<X> ./cmd/paisans` inside each. This reads tags without touching the worktree.

- [ ] **Step 2: Add the line.** Add `requires_toolkit: ">= <V>"` with a one-line comment: its templates read `.Consumers` and its role reads `app.mesh_address`, which a toolkit before `<V>` does not have, so that toolkit keeps the release before this one.

- [ ] **Step 3: Verify both binaries.** `$H/paisans-<V> blueprint lint $CAT/blueprints/synapse` → PASS. `$H/paisans-<P> blueprint lint $CAT/blueprints/synapse` → FAIL with `requires_toolkit: needs Paisans <V>, and this is <P>`.

- [ ] **Step 4: Check the fallback across releases.** Make a scratch clone: `git clone -q $CAT $H/cat-rel`, fetch `v0.8.2` into it, and tag its HEAD `v0.9.0` **in the scratch clone only**. In an empty directory, with `GIT_ALLOW_PROTOCOL=file PAISANS_CORE_CATALOG=file://$H/cat-rel PAISANS_CATALOG_CACHE=$H/cache-<X>`, run `paisans-<X> catalog list`. `<V>` reads `v0.9.0`. `<P>` reads `v0.8.2` and names `v0.9.0` as skipped, with the requires_toolkit reason.

- [ ] **Step 5: Commit, push, open the PR.**
  ```bash
  git -C $CAT add blueprints/synapse/blueprint.yaml
  git -C $CAT commit -m "feat(synapse): require the toolkit release that has role consumers and shared secrets"
  git -C $CAT push -u origin feat/engagement-matrix
  gh pr create -R Paisans-Software/paisans-blueprints --base main --head feat/engagement-matrix
  ```
  Title: `feat(synapse): a MAS admin client for the engagement digest, on a mesh-only admin listener`. The body says what the four commits do and how they were checked (the harness, the MAS e2e, the Caddy check). Do not mention plans, Outline or tickets. End it with the Claude Code footer.

- [ ] **Step 6: Merge and tag** (catalog release authority covers this, but production does not). Once checks pass, merge. On `main`'s merge commit:
  `git tag -s v0.9.0 -m "core catalog 0.9.0: synapse declares a Matrix Authentication Service admin client for each engagement app, on an admin listener on the mesh only; a toolkit before <V> keeps 0.8.2"`, then `git push origin v0.9.0`. If signing fails or prompts, stop and ask the human; never tag unsigned. Then `git tag -v v0.9.0` passes, and `paisans-<V> catalog list` against the real core reads `v0.9.0`.

---

## After this plan (not in it)

- paisans-cli refreshes `internal/blueprints/testdata/core` and `core.release` to `v0.9.0` and reviews the golden diff. That diff is the `$H/t3` diff from this plan. Until then the toolkit's own goldens show no admin client.
