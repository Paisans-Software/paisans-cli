# Beszel Server Stats Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A reserved `beszel` blueprint runs Beszel's hub on the monitor site, and every site's `infra` stack runs a `beszel-agent` that dials it over the mesh.

**Architecture:** The hub is a reserved blueprint beside `uptime` (`internal/blueprints/reserved/beszel`), whose templates read a Go computed `.Beszel` value (systems list, keys). The agent is an infra service gated on the deployment declaring a `beszel` app, fed a per site token (`sites.<site>.beszel_token`) and the hub's Ed25519 public key, a new paired secret format.

**Tech Stack:** Go, text/template, Docker Compose, `golang.org/x/crypto/ssh`, `crypto/ed25519`.

**Spec:** `docs/specs/2026-10-29-beszel-server-stats.md`. Read it before any task.

## Global Constraints

- No em dashes in anything written: docs, comments, commits (`AGENTS.md`).
- No rejected alternatives in docs, comments or commits.
- Say "blueprint", never "kind".
- Blueprint name `beszel`; constant `config.BlueprintBeszel`; wizard app key `stats`, hostname label `status.health`.
- Foundation keys `beszel` and `beszel-agent`, both optional (`optionalFoundationNames`), `supportedTags` prefix `0.21.`.
- Hub: container port 8090, blueprint `port: 8084` published on the mesh address, mapped `"{{ .MeshAddress }}:8084:8090"`.
- Health: `{path: "/api/health", status: [200]}`.
- OIDC callback `/api/oauth2-redirect`, PKCE on, `admin_group` setting default `operators` used as admin and member group.
- Hub env: `APP_URL`, `USER_CREATION=true`, `OAUTH_DEFAULT_USER_ROLE=user`, `SHARE_ALL_SYSTEMS=true`, `DISABLE_SSH=true`, `USER_EMAIL=beszel-admin@<domain>`, `USER_PASSWORD`, `TRUSTED_PROXY_IPS` from `.TrustProxy`, `CHECK_UPDATES=false`.
- Agent env: `HUB_URL=http://<hub mesh address>:8084`, `TOKEN`, `KEY`, `SYSTEM_NAME=<site>`, `DISABLE_SSH=true`, `DOCKER_HOST=unix:///var/run/docker.sock`; file `infra/beszel-agent/beszel-agent.env`, mode 0600.
- Socket mount: `${PAISANS_DOCKER_SOCKET:?run by paisans deployment apply}:/var/run/docker.sock:ro`.
- Rule names: `beszel-needs-a-monitor-site`, `one-beszel`.
- Packages under test per task only; the full `go vet ./... && go test ./...` once in the last task. CI runs no Go tests.
- Every commit ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never `git stash`.

## Review Focus

1. A deployment with no `beszel` app renders byte for byte what it did before: no agent, no token, no foundation lookup of `beszel-agent` (a catalog without the keys must still plan). Test in Task 4.
2. `site remove` drops the site from `config.yml` and re-applies the hub; `site add` adds it. Test in Task 5.
3. Rotating `sites.<site>.beszel_token` does not mark the gateway's compose changed. Test in Task 4.
4. `hub_key_public` is always the half of `hub_key`, after generation and after a group rotation. Test in Task 1.
5. A `beszel` app's `admin_password` routed by `secrets rotate` goes to the beszel path, not `UptimeAdmin`'s precheck refusal. Test in Task 5.

---

### Task 1: Ed25519 paired secret formats

**Files:**
- Modify: `internal/blueprints/secrets.go` (constants :18-35, `secretSpecs` :128-175)
- Modify: `internal/secretsgen/secretsgen.go` (`generate` :323, `keypairs` :355, `fillKeypair` :382)
- Modify: `internal/secretsgen/replace.go` (:42 paired refusal, :84)
- Modify: `internal/secretsrotate/declared.go` (`appGroup` :101-175)
- Test: `internal/blueprints/blueprint_test.go`, `internal/secretsgen/secretsgen_test.go`, `internal/secretsrotate/declared_test.go`

**Interfaces:**
- Produces: `blueprints.FormatEd25519OpenSSH = "ed25519-openssh"`, `blueprints.FormatEd25519Public = "ed25519-public"`. Secret spec `{name: hub_key, format: ed25519-openssh, group: hub}` and `{name: hub_key_public, format: ed25519-public, of: hub_key, group: hub}`. No `passphrase`.
- Produces: `secretsgen.ed25519Keypair() (private, public string, err error)`: private is `ssh.MarshalPrivateKey(priv, "")` PEM, public is `ssh.MarshalAuthorizedKey` trimmed of its newline.

- [ ] **Step 1: Write failing tests**
  - `TestParseEd25519Pair`: a blueprint declaring the two specs above parses; `ed25519-public` without `of` is refused with an error containing `names the ed25519-openssh secret`; `ed25519-openssh` with `passphrase` is refused.
  - `TestFillEd25519Pair`: Fill on that blueprint yields `hub_key` starting `-----BEGIN OPENSSH PRIVATE KEY-----`, and `hub_key_public` starting `ssh-ed25519 ` that equals `ssh.MarshalAuthorizedKey` of the parsed private key's public half.
  - `TestRotateEd25519GroupKeepsHalves`: rotating group `hub` replaces both and the halves still match.
- [ ] **Step 2:** `go test ./internal/blueprints ./internal/secretsgen ./internal/secretsrotate -run Ed25519` fails.
- [ ] **Step 3:** Implement. Extend the format error string at secrets.go:142 to list both names. In `fillKeypair` branch on format: no passphrase for Ed25519; derive a missing public half with `ssh.ParseRawPrivateKey`. `ReplacementIn` keeps refusing a single paired key. `appGroup` gets an Ed25519 branch beside the RSA one.
- [ ] **Step 4:** Tests pass.
- [ ] **Step 5:** Commit `feat: add the ed25519-openssh and ed25519-public secret formats`.

### Task 2: Foundation names and the blueprint constant

**Files:**
- Modify: `internal/blueprints/foundation.go` (:30-56 names, :80 `supportedTags`)
- Modify: `internal/blueprints/testdata/core/foundation.yaml`
- Modify: `internal/config/config.go:73-80`
- Test: `internal/blueprints/foundation_test.go` (create if absent beside existing foundation tests)

**Interfaces:**
- Produces: `blueprints.FoundationBeszel = "beszel"`, `blueprints.FoundationBeszelAgent = "beszel-agent"`, `config.BlueprintBeszel config.Blueprint = "beszel"`.

- [ ] **Step 1: Failing tests:** `TestFoundationBeszelOptional`: a foundation file without either key parses; `FoundationImage(FoundationBeszelAgent)` then errors naming `beszel-agent`. `TestFoundationBeszelTags`: `ghcr.io/henrygd/beszel/beszel:0.21.*` passes `CheckFoundationImage`, `:0.22.*` is refused.
- [ ] **Step 2:** Run `go test ./internal/blueprints -run FoundationBeszel`: FAIL.
- [ ] **Step 3:** Add names to `optionalFoundationNames`, tags `{"0.21."}` each; add both tracks to the fixture exactly as the core catalog's `foundation.yaml` on `feat/beszel-images` writes them.
- [ ] **Step 4:** PASS. **Step 5:** Commit `feat: read the beszel and beszel-agent foundation tracks`.

### Task 3: The reserved `beszel` blueprint (the hub)

**Files:**
- Create: `internal/blueprints/reserved/beszel/blueprint.yaml`, `compose.yaml.tmpl`, `.env.secret.tmpl`, `caddy.snippet.tmpl`, `setup.sh.secret.tmpl`, `data/config.yml.secret.tmpl`, `data/id_ed25519.secret.tmpl`
- Create: `internal/render/beszel.go`, `internal/render/beszel_test.go`
- Modify: `internal/render/appview.go` (field beside `UptimeSeed`; fill in `values()` beside :493)
- Modify: `internal/validate/monitor.go`, `internal/validate/validate.go` (`Check()`), `internal/validate/testdata/`, `internal/validate/validate_test.go`
- Modify: `internal/hostmem/hostmem.go` (`Runs`, `ForRoles`, `ForSite`, `Parts`)

**Interfaces:**
- Consumes: Task 1 formats, Task 2 names.
- Produces: `appValues.Beszel beszelValues` with `type beszelValues struct { ConfigYAML string; HubKey string; HubKeyPublic string; AdminEmail string }`; `func (p *planner) beszelValues(app string) (beszelValues, error)`; `func beszelSystems(cfg *config.Config, secrets *config.Secrets) ([]beszelSystem, error)` with `type beszelSystem struct { Name, Host, Token string }` in site name order. Later tasks call `beszelSystems`.

blueprint.yaml: services `{name: app, foundation: beszel}`, `{name: admin, foundation: beszel}`, `{name: setup, foundation: heartbeat}`; `database: {engine: sqlite}`; `port: 8084`; `health` as Global Constraints; `gate: {gateable: false}`; settings `admin_group`; `storage: {local_only: true, local_only_reason: "the hub keeps its SQLite database and its key under /beszel_data"}`; `oidc` as Global Constraints; secrets generate `admin_password` and the Task 1 pair; every upstream value cited beside it at `v0.21.1` as the uptime blueprint does.

compose: `admin` runs `["superuser", "upsert", "${BESZEL_ADMIN_EMAIL}", "${BESZEL_ADMIN_PASSWORD}"]` with `restart: "no"` and the data bind; `app` `depends_on: {admin: {condition: service_completed_successfully}}`; `setup` runs `/bin/sh /setup.sh`, `restart: on-failure`, reaches the hub at `http://app:8090`. `setup.sh`: wait for `/api/health` 200; `POST /api/collections/_superusers/auth-with-password`; `PATCH /api/collections/users` with `{"oauth2":{"enabled":true,"providers":[{"name":"oidc","displayName":"Pocket ID","clientId":…,"clientSecret":…,"authURL":.OIDC.AuthorizationEndpoint,"tokenURL":.OIDC.TokenEndpoint,"userInfoURL":.OIDC.UserinfoEndpoint,"pkce":true}]}}`, values through `jsonquote`; exit 0 on success, nonzero otherwise; then sleep forever so `restart: on-failure` does not loop. The builder verifies the PATCH shape against PocketBase v0.40.5 `apis/collection.go` / `core/collection_model_auth.go` (`oauth2` field names) before writing it.

- [ ] **Step 1: Failing tests:**
  - `TestBeszelSystems`: fixture with sites home-a, home-b, watch yields three systems in name order, Host each mesh address, Token each `sites.<s>.beszel_token`.
  - `TestBeszelConfigYAML`: the rendered `stats/data/config.yml` parses as YAML with `systems[].name/host/token/users`, `users` an empty list, mode 0600.
  - `TestBeszelKeyFile`: `stats/data/id_ed25519` equals `hub_key`, mode 0600.
  - Validate table rows: fixture `beszel-off-monitor` refused `beszel-needs-a-monitor-site`; `beszel-twice` refused `one-beszel`.
  - `TestFloorBeszel`: `ForSite` on the site holding the `stats` app sets `Beszel`, and `Parts()` lists `FloorBeszel`.
- [ ] **Step 2:** `go test ./internal/render ./internal/validate ./internal/hostmem -run 'Beszel|beszel'`: FAIL.
- [ ] **Step 3:** Implement. Add a `stats` app (`blueprint: beszel`, `hostname: status.health.example.org`, pinned `watch`) to `internal/render/testdata/deployment.yaml` and its secrets to `testdata/secrets.fixture.yaml`. `FloorBeszel = 64 * mib`, comment "set from the hub at rest on a staging host", to be corrected in Task 7.
- [ ] **Step 4:** PASS; `go test ./internal/render -update` and read the golden diff: only `watch` gains `stats/` and a Caddy host block.
- [ ] **Step 5:** Commit `feat: add the reserved beszel blueprint, Beszel's hub on the monitor`.

### Task 4: The per site agent and its token

**Files:**
- Modify: `internal/config/secrets.go:158` (`SiteSecrets.BeszelToken string \`yaml:"beszel_token,omitempty"\``), `internal/config/secrets_set.go:40-43,111-118`, `internal/configstore/merge.go:177,259,310`
- Modify: `internal/secretsgen/secretsgen.go:194-203`, `declared.go:99`, `replace.go:61`
- Modify: `internal/render/plan.go` (`siteView.Beszel *beszelAgent`), `internal/render/site.go` (:113 `infraImages.beszelAgent`, :124 `infraOrigins` resolving it only when set, :327-354 data map, :372-392 files, :534 `runsInfra`), `internal/render/images.go:282`, `internal/render/templates/infra-compose.yaml.tmpl`, create `internal/render/templates/beszel-agent.env.tmpl`
- Modify: `internal/apply/apply.go:960,1866-1868`, `internal/apply/transport.go` `RemoteCommand`
- Test: `internal/render/beszel_test.go`, `internal/secretsgen/secretsgen_test.go`, `internal/apply/apply_test.go`, `internal/apply/transport_test.go`

**Interfaces:**
- Consumes: `beszelSystems`, `FoundationBeszelAgent`, `config.BlueprintBeszel`.
- Produces: `type beszelAgent struct { HubURL, Token, Key, Name string }`; `const beszelAgentService = "beszel-agent"` in apply.

- [ ] **Step 1: Failing tests:**
  - `TestNoBeszelRendersNothing`: a config without a beszel app renders no `infra/beszel-agent/`, no `beszel-agent:` in any infra compose, and plans with a foundation fixture lacking both keys.
  - `TestBeszelAgentEveryInfra`: with the `stats` app, every site's infra compose has `beszel-agent:` with `network_mode: host` and the exact socket mount from Global Constraints; each `beszel-agent.env` is 0600 and holds `HUB_URL=http://10.44.0.4:8084`, its own token, `SYSTEM_NAME=<site>`.
  - `TestFillBeszelToken`: Fill generates `sites.<s>.beszel_token` (hex32) for every site only when a beszel app is declared; `generatedNames` agrees.
  - `TestBeszelEnvNotGateway`: a change to `infra/beszel-agent/beszel-agent.env` leaves `gatewayComposeChanged` false.
  - `TestRemoteCommandExportsSocket`: `RemoteCommand("unix:///run/user/1001/docker.sock", "true")` decodes to a script exporting `PAISANS_DOCKER_SOCKET='/run/user/1001/docker.sock'`; a non `unix://` host exports nothing extra.
- [ ] **Step 2:** Run the touched packages with `-run 'Beszel|Socket'`: FAIL.
- [ ] **Step 3:** Implement. The token is hex32 (`hexSecret(32)`), shape checked like `heartbeatTokenShape`.
- [ ] **Step 4:** PASS; regenerate goldens and read the diff: every site gains the agent, nothing else moves.
- [ ] **Step 5:** Commit `feat: run a beszel agent in every site's infra stack`.

### Task 5: Topology changes and rotation

**Files:**
- Modify: `internal/apply/monitor.go:51`, `internal/siteremove/monitor.go:86`
- Modify: `cmd/paisans/secretsrotate.go:589,606,798-802`; create `internal/secretsrotate/beszel.go`
- Test: `internal/apply/monitor_test.go`, `internal/siteremove/monitor_test.go`, `internal/secretsrotate/beszel_test.go`

**Interfaces:**
- Produces: `secretsrotate.BeszelToken(site string) Rotator` whose `Users()` are the beszel app's site then that site's `infra`; `secretsrotate.BeszelAdmin(app string) Rotator` applying the hub's stack.

- [ ] **Step 1: Failing tests:** `PlanMonitorReseeds` includes the beszel app after a site add and a site remove; `reseeded()` applies the hub stack whole; `secrets rotate sites.home-a.beszel_token` plans `watch` (hub) then `home-a` infra; `secrets rotate apps.stats.admin_password` reaches `BeszelAdmin`, not the uptime precheck.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS.
- [ ] **Step 5:** Commit `feat: re-render the beszel hub on site changes, and rotate its secrets`.

### Task 6: The wizard

**Files:**
- Modify: `internal/create/shapes.go` (beside `health` :113, const block :163), `internal/create/wizard.go` (:441-491), `internal/create/render.go:165`
- Test: `internal/create/*_test.go`, goldens via `go test ./internal/create -update`

- [ ] **Step 1: Failing test:** with health yes, the wizard asks `Server and container stats (Beszel)?` default yes, and a yes declares `stats` with `blueprint: beszel`, `hostname: status.health.example.org`, pinned to the monitor site; with health no, the question is not asked and no `stats` app exists.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS, goldens read.
- [ ] **Step 5:** Commit `feat: offer beszel after the health page in paisans create`.

### Task 7: Documentation and the whole suite

**Files:**
- Modify: `README.md` (*Monitoring*: a `### Server and container stats` section with the rules table, the agent, the socket's reach, cgroup delegation), `docs/development.md` (*Run*), `CHANGELOG.md` (one *Added* bullet, at most 35 words)

- [ ] **Step 1:** Write the docs from the spec; every upstream fact cited as the spec cites it.
- [ ] **Step 2:** `gofmt -l .` prints nothing; `go vet ./...` and `go test ./...` pass.
- [ ] **Step 3:** Commit `docs: describe beszel server stats`.

The staging run (spec, *Testing*) uses a binary built from a fresh checkout of this branch, with `PAISANS_CORE_CATALOG` pointing at the core catalog's `feat/beszel-images` checkout, before the PR merges. It corrects `FloorBeszel` from what it measures.

Not in this plan: a cgroup delegation drop-in or a `doctor` check. `site admit` already writes `Delegate=cpu cpuset io memory pids` for `user@.service` on every host, so the delegation is in place.
