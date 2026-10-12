# Secrets Rotate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `paisans secrets rotate <path>... | --all` replaces every generated secret that can be replaced without losing data, where it is checked and where it is used, in staged, gated, resumable runs.

**Architecture:** One package, `internal/secretsrotate`, plans and executes a rotation per secret in four stages (next value, checking side, users, promote), the shape `internal/rotatekey` and `internal/meshrekey` already use. Each kind of secret is a `Rotator` that says how its checking side learns the new value and which stacks on which sites present it. The next value lives in the secrets file from stage 1, so a stopped run resumes; `apply` refuses while one is pending.

**Tech Stack:** Go, sops in-process (existing), the toolkit's SSH transport and scoped `apply`, `psql` inside Spilo and inside a pinned app's Postgres, `rabbitmqctl`, Garage's own restart.

**Spec:** `docs/specs/2026-10-12-secrets-rotate.md`. Read it first. This branch is cut from `feat/site-evict` (PR #180), whose `internal/meshrekey` is the pattern for next values, resume and the `apply` refusal.

## Global Constraints

- Follow `AGENTS.md` exactly: lowercase imperative subjects, long "why" bodies wrapped at 80 columns, `Founder decision.` where the founder decided; NO em dashes anywhere; no rejected alternatives; refuse rather than warn where incoherent; staged, gated, idempotent, resumable.
- Every commit message ends with exactly: `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`
- NEVER run `git stash`. Never push.
- Out of scope, listed as kept with the reason: `apps.<pocket-id>.encryption_key`, `apps.<synapse>.mas_encryption_secret`. WireGuard keys are `mesh rekey`'s.
- Order for `--all`, exactly: `external.acme_dns_token`; OIDC client secrets; Mbin OAuth keypair with `oauth_passphrase` and `oauth_encryption_key`; Mbin `app_secret`, oauth2-proxy `cookie_secret`, uptime `session_secret`; MAS `mas_signing_key`, `mas_matrix_secret`; apps' S3 keys; Pocket ID `static_api_key`, uptime `admin_password`; `cluster.superuser_password`, `cluster.admin_password`, `cluster.standby_password`, apps' `database_password`; Mbin `rabbitmq_password`, `valkey_password`, `mercure_jwt_secret`; `storage.garage.admin_token`; `storage.garage.rpc_secret`; `sites.<s>.heartbeat_token`.
- Typed words: `sign out` before any rotation that signs members out (Founder decision); without a terminal such a run is refused.
- `storage.garage.rpc_secret` is in `--all`; every Garage node restarts with the new value together; the plan says storage is unavailable for that restart (Founder decision).
- Next values: `secrets.enc.yaml` key `rotations` (map of secret path to next value) and `rotate_remaining` (list of paths a `--all` run has left). A rotation is pending while its path is in `rotations`.
- SQL that sets a password is sent on stdin with statement logging off, as `internal/apply/database.go` `bootstrapSQL` does, so no password reaches a log or `argv`.
- Refusals before anything changes: etcd or Patroni unhealthy; a site that does not answer over ssh; a pasted secret whose value equals what the hosts were last rendered with; a pending rotation whose stage cannot be read from the hosts; a `mesh rekey` pending.

## Review Focus

1. A rotation stopped after stage 2 (Postgres already holds the new password, the apps still the old): the re-run must finish stage 3 with the next value, not generate another. Test in Task 3.
2. An app with `cluster` placement on two apps sites: stage 3 applies one site at a time and each must be healthy before the next; the second site briefly runs the old value. Test in Task 4.
3. Mbin with `queue: postgres` (the default): `rabbitmq_password` has no user, so it is listed as kept-unused, not rotated, not an error. Test in Task 6.
4. A deployment with one Garage node: `rpc_secret` still restarts that node and the plan says storage is briefly down. Test in Task 7.
5. Running `secrets rotate` while `mesh rekey` is pending, and `apply` while a rotation is pending: both refuse, naming the command that finishes the other. Test in Task 9.

Implementation inputs every task may use (verified by exploration of this tree, line numbers approximate):
- `apply` actions: env or compose file change gives `compose up -d`; any other bind-mounted file gives a stack restart; an infra file restarts its own service (`internal/apply/apply.go` about 800-925, `infraService` about 1588).
- Clustered apps' roles: on the Patroni leader's data site, `apply` runs `ALTER ROLE <app> WITH LOGIN PASSWORD` for every clustered app (`internal/apply/database.go` about 62-111, 236-258). `patroniCompose`, `psql`, `bootstrapSQL`, `patroniLeader` live there; `internal/patroni` has `Leader`, `Parse`, `Exec`, `ClusterCommand`.
- Patroni env rollout: `internal/apply/patronienv.go` (`RecreatePatroni`, `patronictlList`, `ownStatus`, printenv of the running container); switchover `internal/failover` `SwitchoverCommand`.
- Generators in `internal/secretsgen/secretsgen.go`: `password` (about 723), `hexSecret` (about 597, 158), `rsaPrivateKeyPEM` (about 336), `oauthKeypairPEM` (about 421), `GarageKeyPair` (about 649), `ClientSecret` (about 753, exported).
- Existing rotations: `internal/rotatekey` (stages, `Build`, `Execute`, `Show`, `Switch` interface) and `cmd/paisans/rotatekey.go`; `cmd/paisans/oidc.go` `--rotate-secret`.
- Next-value pattern: `internal/meshrekey/meshrekey.go` (`Store{Load, Save}`, reload and compare after each write, resume by reading the host) and `cmd/paisans/meshrekey.go` (`refuseMidRekey`, called from `cmd/paisans/converge.go` and `cmd/paisans/main.go`).

---

### Task 1: Next values in the secrets file, and the generators

**Files:**
- Modify: `internal/config/secrets.go` (fields), `internal/config/secrets_set.go` (if `Get`/`Set` need a `rotations.` prefix: they do not; access the map directly)
- Modify: `internal/secretsgen/secretsgen.go` (export generators)
- Create: `internal/secretsgen/replace.go`
- Test: `internal/config/secrets_rotations_test.go`, `internal/secretsgen/replace_test.go`

**Interfaces:**
- Produces:

```go
// in config.Secrets
Rotations       map[string]string `yaml:"rotations,omitempty"`
RotateRemaining []string          `yaml:"rotate_remaining,omitempty"`
func (s *Secrets) RotationPending() []string // sorted paths in Rotations

// in secretsgen
func Replacement(path string, s *config.Secrets) (string, error)
func ReplacementOAuthKeypair(passphrase string) (privatePEM, publicPEM string, err error)
```

`Replacement` returns a value of the right shape for each in-scope path: `password()` for cluster passwords, `admin_token`, `database_password`, `app_secret`, `cookie_secret`, `session_secret`, `admin_password`, `static_api_key`, `rabbitmq_password`, `valkey_password`, `mercure_jwt_secret`, `oauth_passphrase`, `oauth_encryption_key`, `mas_matrix_secret`; `hexSecret(32)` for `storage.garage.rpc_secret`; `hexSecret(16)` for `sites.<s>.heartbeat_token`; `rsaPrivateKeyPEM()` for `mas_signing_key`. It refuses the two out-of-scope encryption secrets and any WireGuard key with an error naming why, and any path `Get` does not know.

- [ ] **Step 1: Write failing tests**: a secrets file with `rotations` and `rotate_remaining` round-trips through `WriteSecrets` and `LoadSecrets` (use the package's existing memory-keystore fixtures); `RotationPending` is sorted; `Replacement` gives a 64-hex `rpc_secret`, a value matching `^[0-9a-f]{32}$` for a heartbeat token, a PEM `PRIVATE KEY` for `mas_signing_key`, a distinct value on each call for a password, and refuses `apps.id.encryption_key` (pocket-id kind) and `sites.vm.wireguard_private_key`.
- [ ] **Step 2: Run, see FAIL.** `go test ./internal/config ./internal/secretsgen -run 'Rotation|Replacement' -v`
- [ ] **Step 3: Implement.** Decide the app kind from `s.Apps` keys via the configuration only where needed; when `Replacement` needs the kind, take it as a parameter instead (`Replacement(path string, kind config.Kind)`) and update the interface line above in your report.
- [ ] **Step 4: Run, see PASS.** `go test ./...`
- [ ] **Step 5: Commit** `feat: the secrets file holds a secret's next value while it is rotated`

---

### Task 2: The rotation engine

**Files:**
- Create: `internal/secretsrotate/secretsrotate.go`, `internal/secretsrotate/rotator.go`
- Test: `internal/secretsrotate/secretsrotate_test.go`

**Interfaces:**
- Consumes: Task 1.
- Produces:

```go
type Cost struct {
	SignsOut   bool   // asks "sign out"
	Downtime   string // "" or what is briefly unavailable, Eg: "storage, for one restart"
	Reconnects bool
}
type Site struct{ Name string; T apply.Transport }
type Env struct {
	Cfg     *config.Config
	Store   Store // Load() (*config.Secrets, error); Save(*config.Secrets) error; reload and compare after each Save
	Sites   map[string]apply.Transport
	Apply   func(site string, s *config.Secrets, stacks []string) error // scoped apply of those stacks with these secrets
	Healthy func(site string, stacks []string) error
	Now     func() time.Time
}
type Rotator interface {
	Path() string
	Cost() Cost
	// Checked makes the side that verifies the secret accept next. It must be
	// safe to run again, and Accepts reports whether it already does.
	Checked(e Env, next string) error
	Accepts(e Env, next string) (bool, error)
	// Users lists, in order, the sites and stacks that present the secret.
	Users(e Env) []Use
}
type Use struct{ Site string; Stacks []string }
type Stage int // 1 next value, 2 checked, 3 users, 4 promote
func Plan(e Env, rs []Rotator) (*Run, error)
func (r *Run) Show(w ui.Reporter)
func (r *Run) Execute(e Env, confirm func(word, what string) error) error
```

Execution per rotator: stage 1 writes `Rotations[path] = Replacement(...)` and saves, unless one is already pending (keep it: Review Focus 1); stage 2 runs `Checked` unless `Accepts`; stage 3 runs `Apply` then `Healthy` for each `Use` in order, stopping on the first failure; stage 4 moves the next value into the secret (`Set(path, next)`), deletes it from `Rotations`, removes the path from `RotateRemaining`, and saves. `--all` writes `RotateRemaining` with every path at the start, and a resumed run does pending paths first, then `RotateRemaining`. Before any stage of a run that contains a `Cost.SignsOut` rotator, `confirm("sign out", ...)` must succeed.

- [ ] **Step 1: Write failing tests** with fake rotators recording calls and a fake `Store`: a full run executes the stages in order; a run stopped after stage 2 resumes at stage 3 with the same next value (Review Focus 1); a stopped stage 3 resumes at the failed `Use`; promote clears `Rotations` and `RotateRemaining`; a `SignsOut` rotator without confirmation runs nothing; the plan text names each rotator's cost.
- [ ] **Step 2: Run, see FAIL.** `go test ./internal/secretsrotate -v`
- [ ] **Step 3: Implement**, following `internal/meshrekey`'s `Store` with reload and compare after every save.
- [ ] **Step 4: Run, see PASS.**
- [ ] **Step 5: Commit** `feat: rotate a secret in staged, resumable runs`

---

### Task 3: Postgres: the cluster's roles and the apps' database passwords

**Files:**
- Create: `internal/secretsrotate/postgres.go`
- Test: `internal/secretsrotate/postgres_test.go`

**Interfaces:**
- Consumes: Task 2; `internal/apply/database.go` (`patroniCompose`, `psql`, statement-logging-off pattern of `bootstrapSQL`), `internal/patroni` (`Leader`), `internal/apply/patronienv.go`, `internal/failover`.
- Produces: `func ClusterRole(role string) Rotator` for `superuser` (role `postgres`), `admin`, `standby` (the role names Spilo uses: read them from `internal/render/templates/patroni.env.tmpl` and cite the line); `func AppDatabase(app string) Rotator`.

Behaviour:
- `ClusterRole`: `Checked` runs on the leader's site, through `patroniCompose ... psql` with SQL on stdin: `SET log_statement = 'none'; ALTER ROLE <role> WITH PASSWORD '<next>';` (quote with the existing literal quoting in `database.go`). `Accepts` tries a login with the next value over the local socket with password auth forced (`PGPASSWORD` on stdin through `env`, `psql -h 127.0.0.1`), never on argv. `Users`: every data site's `infra` stack with `patroni.env`, replicas first one at a time, the leader last, its `Apply` recreating Patroni after a switchover to a streaming replica (reuse the rollout `site add` stage 7 uses). Cost: `Reconnects`.
- `AppDatabase` for a clustered app: `Checked` is the same `ALTER ROLE` on the leader (the role is the app name, as `bootstrapSQL` uses); `Users` every site running the app. For a pinned app with its own `postgres` service: `Checked` runs `ALTER ROLE` inside that app stack's `postgres` container (`compose exec -T postgres psql -U <app>`), since `POSTGRES_PASSWORD` is read only at initdb; `Users` the pinned site.

- [ ] **Step 1: Write failing tests** with a fake transport recording commands and stdin: the password appears only on stdin, never in a command string; the leader is chosen from `patronictl list` output; replicas are applied before the leader and the leader after a switchover; a pinned app's role is altered in its own container; resume after `Checked` skips it when `Accepts` is true (Review Focus 1).
- [ ] **Step 2: Run, see FAIL.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run, see PASS.**
- [ ] **Step 5: Commit** `feat: rotate the cluster's roles and the apps' database passwords`

---

### Task 4: Secrets an app checks itself

**Files:**
- Create: `internal/secretsrotate/appsecret.go`
- Test: `internal/secretsrotate/appsecret_test.go`

**Interfaces:**
- Produces: `func AppSelf(app, key string, cost Cost) Rotator` for secrets checked by the same stack that uses them: Mbin `app_secret`, `mercure_jwt_secret`, `valkey_password` (valkey reads `--requirepass` from its command line at each recreate); oauth2-proxy `cookie_secret`; uptime `session_secret`; MAS `mas_signing_key`, `mas_matrix_secret` (both in the `synapse` stack, which restarts together); Pocket ID `static_api_key`. `func MbinOAuth(app string) Rotator` rotates `oauth_passphrase`, `oauth_encryption_key` and the keypair as one unit (the private key is encrypted with the passphrase): its next value is a small YAML document holding all four, its `Users` every apps site running the app (all must end on the same pair).

`Checked` is a no-op and `Accepts` true for these; `Users` lists every site running the app, one at a time (Review Focus 2). Costs: `SignsOut` for Mbin OAuth, `app_secret`, `cookie_secret`, `session_secret`, `mas_signing_key`, `mas_matrix_secret`. Before marking a cost, check what upstream says for each and cite it in a code comment beside the rotator: Mbin (Symfony `APP_SECRET`), oauth2-proxy cookie sessions without a session store, the uptime fork (source at `/Users/wash/Developer/paisans.community/src/uptime` if present), MAS's configuration reference. Where you cannot verify, keep `SignsOut` true (the stricter reading, since it only adds a confirmation) and say so in the comment.

- [ ] **Step 1: Write failing tests:** a cluster-placed Mbin on two apps sites is applied site by site with a health check between; the OAuth unit promotes four keys together; costs as listed.
- [ ] **Step 2: Run, see FAIL.** **Step 3: Implement.** **Step 4: Run, see PASS.**
- [ ] **Step 5: Commit** `feat: rotate the secrets an app checks itself`

---

### Task 5: Uptime's admin password and the heartbeat tokens

**Files:**
- Create: `internal/secretsrotate/uptime.go`
- Test: `internal/secretsrotate/uptime_test.go`

**Interfaces:**
- Produces: `func UptimeAdmin(app string) Rotator`, `func Heartbeat(site string) Rotator`.

First establish, from the uptime fork's source (`/Users/wash/Developer/paisans.community/src/uptime`; if absent, from the image's published source at the tag the kind pins in `internal/kinds`), and cite it in a comment: whether `ADMIN_PASS` is applied to an existing admin at boot, and whether the boot-time seed reconcile of `monitors.json` updates an existing monitor's push token. If both are applied at boot, `Checked` is a no-op and `Users` is the monitor site's `uptime` stack (then, for a heartbeat, the pushing site's `infra` heartbeat service). If either is not, report the task as `DONE_WITH_CONCERNS` and implement the rotator to refuse with a message naming what the fork lacks; do not invent an API.

Heartbeat order: the monitor learns the new token first (reseed through `apply.PlanMonitorReseeds`), then the pushing site's heartbeat service; between the two the push is refused for at most one interval, which the plan states.

- [ ] Steps 1 to 5 as above. Commit `feat: rotate uptime's admin password and the heartbeat tokens`

---

### Task 6: RabbitMQ

**Files:**
- Create: `internal/secretsrotate/rabbitmq.go`
- Test: `internal/secretsrotate/rabbitmq_test.go`

**Interfaces:**
- Produces: `func MbinRabbitMQ(app string) Rotator`.

RabbitMQ applies `RABBITMQ_DEFAULT_PASS` only to an empty data directory, and the `rabbitmq_data` volume persists; verify against RabbitMQ's Docker image documentation and cite it. `Checked`: `compose exec -T rabbitmq rabbitmqctl change_password <user> -` with the password on stdin if `rabbitmqctl` accepts it there (verify; if it only takes argv, run it through `sh -c 'read -r p; rabbitmqctl change_password <user> "$p"'` with the password on stdin, so it never reaches the ssh command line). `Accepts`: `rabbitmqctl authenticate_user` the same way. `Users`: the Mbin stack on each site running it. An app with `queue` other than `rabbitmq` has no RabbitMQ: the rotator reports the secret kept and unused (Review Focus 3).

- [ ] Steps 1 to 5. Commit `feat: rotate mbin's rabbitmq password inside the broker`

---

### Task 7: Garage

**Files:**
- Create: `internal/secretsrotate/garage.go`
- Test: `internal/secretsrotate/garage_test.go`

**Interfaces:**
- Produces: `func GarageAdminToken() Rotator`, `func GarageRPC() Rotator`.

`GarageAdminToken`: no user in the toolkit; `Users` each Garage site's `infra` garage service, one at a time. `GarageRPC`: `Cost{Downtime: "storage, while every Garage node restarts together"}`; `Users` is a single step that writes every Garage site's `garage.toml` with the next value through `apply.Scope` (files only, no actions), then restarts `garage` on every Garage site at once, then waits for `garage status` on each to list every node healthy. With one node it still restarts that node (Review Focus 4).

- [ ] Steps 1 to 5. Commit `feat: rotate garage's secrets, its rpc secret on every node at once`

---

### Task 8: Pasted secrets and the existing rotations

**Files:**
- Create: `internal/secretsrotate/wrapped.go`
- Test: `internal/secretsrotate/wrapped_test.go`

**Interfaces:**
- Produces: `func DNSToken() Rotator`, `func OIDCClient(app string, run func(app string) error) Rotator`, `func AppS3(app string, run func(app string) error) Rotator`.

`DNSToken`: no next value is generated; the operator has already set the new token with `secrets set`. `Plan` refuses when the value in the file is what the gateway's `caddy.env` was rendered with (compare the rendered file's hash with the manifest on the host), naming `paisans secrets set external.acme_dns_token`. `Users`: the gateway and every monitor site whose Caddy is the toolkit's, Caddy recreated with its gates. `OIDCClient` and `AppS3` delegate to the existing `oidc client create --rotate-secret` and `storage rotate-key --app` flows passed in as `run`, then apply the app's sites; they keep their own staging and are listed in the plan in their place in the order. After `OIDCClient`, the old client secret is deleted at Pocket ID (find the delete call in `internal/oidcclient`; if none exists, add it there with its test, since an old secret that stays valid is a secret not rotated).

- [ ] Steps 1 to 5. Commit `feat: rotate the dns token, oidc client secrets and s3 keys in order`

---

### Task 9: `paisans secrets rotate`, and `apply` refusing a pending rotation

**Files:**
- Create: `cmd/paisans/secretsrotate.go`
- Modify: `cmd/paisans/secrets.go` (`runSecrets` dispatch: `rotate`), `cmd/paisans/main.go` (usage), `cmd/paisans/converge.go` and `cmd/paisans/main.go` (call the new refusal beside `refuseMidRekey`)
- Test: `cmd/paisans/secretsrotate_test.go`

Behaviour: `paisans secrets rotate <path>... | --all [--execute]`. Paths may also name a whole app's unit (`apps.<app>.oauth`). `--all` builds the rotators in the Global Constraints order from the configuration (only apps and sites that exist; kept secrets listed with their reason). Refusals from the Global Constraints, checked before anything changes. A run containing a `SignsOut` rotation asks `sign out` through `confirmWord`; no terminal, refused. `refuseMidRotation` refuses `apply` while `RotationPending()` is non-empty, naming `paisans secrets rotate`; `secrets rotate` refuses while a `mesh rekey` is pending (Review Focus 5).

- [ ] **Step 1: Write failing tests** through the fake terminal: `--all` order; kept secrets listed; `sign out` asked and refused without a terminal; `apply` refused mid-rotation; rotate refused mid-rekey.
- [ ] Steps 2 to 4.
- [ ] **Step 5: Commit** `feat: paisans secrets rotate`

---

### Task 10: Documentation

**Files:**
- Modify: `README.md` (a section *`secrets rotate` replaces a secret where it is checked and where it is used*, near *Three kinds of secret*, with the reasoning, the four stages, the order, the costs and why the two encryption secrets are kept; update *Three kinds of secret* where it says generated secrets are never replaced)
- Modify: `docs/commands.md`, `docs/development.md` (*Run*, *What is not here yet*: not run against a real host)
- Modify: `docs/guides/when-an-admin-leaves.md` and `docs/guides/how-a-deployment-protects-itself.md`: replace every "until `secrets rotate`" and the *Not built yet* entry with the command, keeping the order the runbook gives.

- [ ] Write; verify every command example against `cmd/paisans`; links resolve; no em dashes; no real hosts.
- [ ] Commit `docs: secrets rotate, and the runbook rotates everything`

---

## Self-review notes

- Spec coverage: scope and kept secrets (1, 9), stages and next value (1, 2), checking side for first-boot secrets (3, 6), costs and `sign out` (2, 4, 9), Garage together (7), pasted secrets (8), refusals (9), order (9), docs (10).
- Interfaces: `Rotations`, `RotateRemaining`, `RotationPending`, `Replacement` (1); `Rotator`, `Env`, `Use`, `Cost`, `Plan`, `Run` (2); constructors per task, consumed by Task 9.
