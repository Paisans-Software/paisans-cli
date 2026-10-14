# Operators and Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Name operators apart from community admins, make account management a declared `management` choice on the `pocket-id` app, and turn the admin reconciler into a read-only operator reconciler.

**Architecture:** One pull request into `paisans-stack`'s `develop`, then one into the core catalog's `main`. The `admins:` section of `paisans-stack.yaml` becomes `operators:` everywhere it is read; `validate` gains the `management` rules and the two-operator floor; `apply` makes operators Pocket ID users without `isAdmin` under `management: paisans`; the reconciler loses its write path and checks operators against Pocket ID instead.

**Tech Stack:** Go 1.25, Go templates, YAML fixtures, golden-file render tests, a GitHub Actions image workflow.

**Spec:** `docs/specs/2026-10-14-operators-and-management.md`. Read it first; this plan argues from it.

## Global Constraints

- Names, verbatim from the spec: `operators:` (fields `name`, `email`, `keys`); `paisans operator register`, `paisans operator magic-link`; group `operators`; `cmd/operator-reconciler`; `internal/operatorreconciler`; image `ghcr.io/paisans-software/operator-reconciler`; foundation key `operator-reconciler`; tags `operator-reconciler-v*`; heartbeat `operators-heartbeat`; monitor `operator continuity`; `.env` variables `PAISANS_OPERATORS`, `PAISANS_MEMBER_GROUP`, `PAISANS_MANAGEMENT`.
- Rules, verbatim: `operators-fewer-than-two`, `pocket-id-management-unset`, `pocket-id-without-account`, `account-under-pocket-id-management`, `pocket-id-setting-managed`, `pocket-id-paisans-without-smtp`, `account-without-pocket-id`, `account-one-identity-app` (kept), `account-setting-unknown` (refuses `identity_app`).
- `management` is required, never defaulted; values `paisans` and `pocket-id`.
- The reconciler makes only `GET` requests. Its tests fail on any other method.
- The image workflow publishes only on `operator-reconciler-v*` tags; a push to `develop` publishes nothing.
- No alias for any old name and no conversion code: deployments are redeployed.
- Say "blueprint", never "kind", in everything written, rewording "kind" in passages already changed. Docs describe current state only; rejected alternatives only in `docs/decisions.md`. No em dashes in new text. No Outline or ticket references.
- `CHANGELOG.md` `[Unreleased]` bullets: one sentence of at most 35 words each.
- Never `git stash`. Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Names the spec leaves open, settled here

These follow the spec's "every name changes outright" for names that hang off the renamed ones:

| Was | Is |
|---|---|
| secrets `admins.<id>.dns_provider_email` | secrets `operators.<id>.dns_provider_email` (its keys are operators) |
| secret `apps.<app>.admins_heartbeat_token` | `apps.<app>.operators_heartbeat_token` |
| stack directory and service `admins-heartbeat` | `operators-heartbeat` |
| monitors `admin continuity`, `admin continuity: dns ...` | `operator continuity`, `operator continuity: dns ...` |
| rule `pocket-id-admin-username` | `pocket-id-operator-username` |
| `config.Admin`, `Config.Admins`, `SSH.Admins` | `config.Operator`, `Config.Operators`, `SSH.Operators` |

## Review Focus

1. An operator whose key is on one Pocket ID site but not another must not count toward the floor: the floor is about running the whole deployment. Pinned in Task 2 (operator counts only with a key on every site running a `pocket-id` app).
2. A `management: paisans` deployment that sets `email_one_time_access_as_unauthenticated: true` (the same value the toolkit renders) is still refused, since a declared managed setting could only disagree later. Pinned in Task 3.
3. A user who is an operator and also an enabled Pocket ID administrator under `management: paisans` fails check 2 (a hand-set flag is the reconciler's to report). Pinned in Task 7.
4. The static API key's user (`00000000-...`) never fails check 2 in either mode. Pinned in Task 7.
5. `paisans app admin create` on a `pocket-id` app under `management: paisans` would make a Pocket ID administrator the reconciler then reports; it is refused. Pinned in Task 5.

---

### Task 1: `operators:` replaces `admins:`

**Files:**
- Modify: `internal/config/{config.go,ssh.go,secrets.go,secrets_set.go}`, `internal/configstore/{local.go,merge.go,trust.go}`, `cmd/paisans/{config.go,hostallow.go,review.go}`, `internal/siteremove/evict.go`, `internal/render/dnscontinuity.go`, every fixture and test holding `admins:`, `examples/*.yaml`, `internal/create/*`.

**Interfaces:**
- Produces: `config.Operator{Name, Email, Keys}`, `Config.Operators map[string]Operator` (`yaml:"operators"`), `SSH.Operators []string` (`yaml:"keys"`), `config.OperatorSecrets`, `Secrets.Operators` (`yaml:"operators,omitempty"`), `secrets set operators.<id>.dns_provider_email`.

- [ ] Rename the Go identifiers with `gofmt -r` and `sed`, the yaml tags by hand, and every error, help text and comment for this section from admin to operator.
- [ ] Rewrite every fixture: `sed -i '' 's/^admins:/operators:/'` across `internal/**/testdata`, `examples`, and the inline YAML in tests.
- [ ] `go build ./... && go test ./...`: every failure is a rename miss; fix and rerun until green.
- [ ] Commit `feat: operators: replaces admins: in paisans-stack.yaml`.

### Task 2: the two-operator floor

**Files:** `internal/validate/operators.go` (new), `internal/validate/operators_test.go` (new), `internal/validate/testdata/operators-fewer-than-two.yaml` (new), `internal/validate/pocketid.go`, every fixture with one operator.

- [ ] Write `TestOperatorsFewerThanTwo`: the valid fixture passes; `operators-fewer-than-two.yaml` (one operator) refuses; an operator whose key is on `home-a` only, with Pocket ID clustered on `home-a` and `home-b`, does not count; a deployment with no `pocket-id` app is not held to it.
- [ ] Run it: FAIL, rule unknown.
- [ ] Implement:

```go
// operatorsFewerThanTwo refuses a deployment whose Pocket ID sites admit
// fewer than two operators: one lost workstation must not leave nobody able
// to run the deployment. An operator counts only with a key on every site
// that runs a pocket-id app.
func (c *checker) operatorsFewerThanTwo() {
	sites := c.pocketIDSites()
	if len(sites) == 0 {
		return
	}
	n := 0
	for _, id := range sortedKeys(c.cfg.Operators) {
		on := true
		for _, s := range sites {
			on = on && slices.Contains(c.cfg.Sites[s].SSH.Operators, id)
		}
		if on {
			n++
		}
	}
	if n < 2 { c.refuse("operators-fewer-than-two", "operators", ...) }
}
```

- [ ] Add a second operator `bob` to `valid.yaml` and to every fixture with one, on every site's `ssh.keys`; rename `pocket-id-admin-username` to `pocket-id-operator-username`.
- [ ] `go test ./internal/validate/ ./...` green. Commit `feat: validate refuses fewer than two operators`.

### Task 3: `management` on the `pocket-id` app

**Files:** `internal/blueprints/pocketid_settings.go`, `internal/validate/pocketid.go`, `internal/validate/account.go`, `internal/validate/validate.go`, fixtures, render goldens.

**Interfaces:**
- Produces: `blueprints.PocketIDManagementSetting = "management"`, `blueprints.ManagementPaisans = "paisans"`, `blueprints.ManagementPocketID = "pocket-id"`, `blueprints.PocketIDManagement(settings map[string]any) string`, `blueprints.PocketIDManagedSettings = []string{"allow_user_signups", "email_one_time_access_as_unauthenticated"}`.

- [ ] Tests: `pocket-id-management-unset` (unset and an unknown value), `pocket-id-setting-managed` for each managed setting under `paisans` (even set to the rendered value), `pocket-id-without-account`, `pocket-id-paisans-without-smtp`, `account-under-pocket-id-management`, and `pocket-id` mode accepting `allow_user_signups: open` with no account app and no SMTP. `TestPocketIDSettingsForManagement`: under `paisans` the rendered settings hold `allow_user_signups: disabled` and `email_one_time_access_as_unauthenticated: true`.
- [ ] Run them: FAIL.
- [ ] `PocketIDSettingsFor` adds the two managed values under `paisans`; `management` joins `PocketIDOtherSettings`; the rules go in `internal/validate/pocketid.go`. `account-signups-not-disabled` and `account-recovery-disabled` are removed: under `paisans` the settings they read are refused when set and rendered right, and under `pocket-id` there is no account app. `account-login-link-disabled` stays as it is.
- [ ] Add `management: paisans` to every fixture with a `pocket-id` app; `go test ./internal/render -update`, read the golden diff (the `.env` gains `ALLOW_USER_SIGNUPS=disabled`; the gateway snippet keeps the signup block). Add a `management: pocket-id` render test that leaves signup to the operator's value.
- [ ] Commit `feat: pocket-id declares who manages accounts`.

### Task 4: the account app finds its identity provider

**Files:** `internal/blueprints/account.go`, `internal/render/account.go`, `internal/secretsrotate/appsecret.go`, `internal/validate/account.go`, fixtures.

**Interfaces:** Produces `config.(*Config).PocketIDApp() (string, bool)`: the one `pocket-id` app, false with none or more than one.

- [ ] Tests: `account-without-pocket-id` (no `pocket-id` app), `account-one-identity-app` (two), `account-setting-unknown` refusing `identity_app`; render reads the issuer from the only `pocket-id` app; secret rotation of `static_api_key` still re-renders the account app.
- [ ] Remove `identity_app` from `AccountOtherSettings`, the fixtures and the examples; replace `accountIdentityApp` with the three rules; render and rotation call `PocketIDApp`.
- [ ] Commit `feat: the account app uses the deployment's one pocket-id app`.

### Task 5: `apply` makes operators

**Files:** `cmd/paisans/converge_operators.go` (renamed from `converge_admins.go`), `cmd/paisans/converge.go`, `cmd/paisans/app.go`, `internal/appadmin/{appadmin.go,pocketid.go}`, `internal/blueprints/groups.go`, `internal/blueprints/reserved/uptime/blueprint.yaml`, `internal/validate/validate.go`, tests.

**Interfaces:**
- Produces: `blueprints.OperatorsGroup = "operators"`; `appadmin.Request.Member bool` ("a member, never an identity provider administrator": create with `isAdmin` false, never plan `ActionGrantAdmin`, never clear the flag).

- [ ] Tests: under `paisans` an operator is created with `isAdmin: false` and an existing one without the flag gets no grant; under `pocket-id` with it; both join `operators`, `members`, every member group and every admin group. `app admin create` on a `pocket-id` app under `paisans` is refused.
- [ ] Implement: the request's admin groups are `adminGroups(cfg)` plus `operators`, its member groups `memberGroups(cfg)` plus `members`; `Member` is `management == paisans`. Remove `convergeAnyAdmin`, which a deployment with a `pocket-id` app and fewer than two operators can no longer reach.
- [ ] The uptime blueprint's `default_admin_group` is `operators`; `uptime-needs-an-admin-group` goes (a default now exists); `admin-group-not-admins` accepts `admins` and `operators` and is reworded for community admins.
- [ ] Commit `feat: apply makes each operator a member, and an administrator only under pocket-id management`.

### Task 6: `paisans operator`

**Files:** `cmd/paisans/operatorregister.go`, `cmd/paisans/operatormagiclink.go` (renamed), `cmd/paisans/main.go`, `cmd/paisans/help.go`, tests.

- [ ] Rename `runAdmin*` to `runOperator*`, the command word `admin` to `operator`, and every message. Tests call `paisans operator magic-link` and expect `paisans admin` to be an unknown command.
- [ ] Commit `feat: paisans operator replaces paisans admin`.

### Task 7: the operator reconciler

**Files:** `git mv cmd/admin-reconciler cmd/operator-reconciler`, `git mv internal/adminreconciler internal/operatorreconciler`, `.github/workflows/operator-reconciler-image.yml` (renamed), `.github/workflows/release.yml`, `internal/blueprints/foundation.go`, `internal/blueprints/testdata/core/foundation.yaml`, `internal/blueprints/reserved/pocket-id/*`, `internal/render/{operatorsheartbeat.go,dnscontinuity.go,uptime.go}`, `internal/render/templates/operators-heartbeat*.tmpl`, `internal/secretsrotate/uptime.go`, `cmd/paisans/secretsrotate.go`, goldens.

**Interfaces:**
- `operatorreconciler.PocketID interface { FindGroup(name string) (*pocketid.Group, error); Users() ([]pocketid.User, error) }`.
- `Reconciler{API, Standby, Now, Log, DNS, Operators []string, MemberGroup, Management string}`.

- [ ] Write the table test over a fake that records methods and fails on anything but `GET`: operator present; missing; disabled; outside `members`; outside `operators`; an administrator who is an operator and one who is not, under each mode; the static key's user; a missing group. DNS continuity is evaluated against operators found by username.
- [ ] Run: FAIL.
- [ ] Implement the pass:

```go
func (g *Reconciler) pass() Result {
	if g.Standby() { return Result{Healthy: true, Message: "standby", standby: true} }
	users, err := g.API.Users()  // retry on error
	members, err := g.API.FindGroup(g.MemberGroup)
	operators, err := g.API.FindGroup(OperatorsGroup)
	// check 1: each operator exists, is enabled, in both groups
	// check 2: each enabled isAdmin user other than SyntheticUserID is an
	//          operator, and under management paisans there is none
}
```

  `/healthz` keeps its contract. `cmd/operator-reconciler` reads `PAISANS_OPERATORS`, `PAISANS_MEMBER_GROUP`, `PAISANS_MANAGEMENT` and refuses to start without them.
- [ ] The pocket-id `.env` renders the three variables; the foundation key, the compose comments, the heartbeat service, its secret, its monitors and the rotation names change. The workflow triggers on `push: tags: ["operator-reconciler-v*"]` only.
- [ ] `go test ./internal/render -update`; read every golden diff. Run `go test ./internal/operatorreconciler/ ./cmd/operator-reconciler/` and, with Docker, `go test -tags pocketid_integration ./internal/operatorreconciler/`.
- [ ] Commit `feat: the operator reconciler reads Pocket ID and writes nothing`.

### Task 8: `paisans create`

**Files:** `internal/create/{answers.go,wizard.go,render.go,paisans-stack.yaml.tmpl,checks.go}`, its tests and testdata.

- [ ] Tests: the wizard asks for `management`; `paisans` adds an `account` app (hostname `account.<domain>`, `invites`, and with invites on the code of conduct read from a Markdown file) and keeps SMTP required; `pocket-id` asks `allow_user_signups` and the two email settings and lets mail be skipped. The written file passes `validate` in each mode and has an `operators:` section with the wizard asking for at least two operators.
- [ ] Implement; rename the admins section to operators.
- [ ] Commit `feat: create asks who manages accounts and writes operators`.

### Task 9: documentation

**Files:** `README.md`, `docs/{commands.md,development.md,decisions.md,deployment-agent-rules.md}`, `docs/guides/*.md`, `docs/specs/2026-10-14-repository-split.md`, `docs/specs/2026-10-14-pocket-id-api-private.md`, `AGENTS.md`, `CHANGELOG.md`, `examples/*`.

- [ ] README's sections on admins, the reconciler, Pocket ID's configuration, invites and the gateway describe operators, community admins, `management` and the operator reconciler as they are, rewritten in place. `docs/decisions.md` gains one entry. The two named specs take the new names only. `AGENTS.md` names the new tags and workflow. One `CHANGELOG.md` bullet per operator-visible change, at most 35 words.
- [ ] Check: every YAML block parses, every command uses a flag `cmd/paisans` defines, every linked path exists, no em dash in added lines, no "kind" in changed passages.
- [ ] Commit `docs: operators, community admins and who manages accounts`.

### Task 10: the core catalog

**Where:** `src/blueprints`, worktree `.worktrees/operator-reconciler-image`, branch `feat/operator-reconciler-image` from `origin/main`.

- [ ] In `foundation.yaml` replace the `admin-reconciler` line, and a comment naming the admin reconciler, with `operator-reconciler: "ghcr.io/paisans-software/operator-reconciler:0.3.*"`; grep for other mentions.
- [ ] PR into `main`, merge with a merge commit. No release, no tag.
