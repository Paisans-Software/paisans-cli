# Account signup, bans and recovery: the toolkit side, implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The `account` blueprint renders paisans-account-service v0.2.0's environment: `settings.signup` (`invite` or `open`) replaces `settings.invites`, `join.<domain>` replaces `invite.<domain>` for every account app, and the operator reconciler also reports a member of `operators` who is not a yaml operator.

**Architecture:** `internal/blueprints` owns the setting names, the `join` role and `JoinHostname`; `RoleHostnames` is the one resolver the gateway, DNS and validate read, so changing it there moves all three. `internal/validate` refuses the configuration the service would refuse. `internal/render` and the reserved templates produce the `.env`, the code of conduct and the two Caddy snippets. The reconciler gains a third read-only check inside its existing pass.

**Tech Stack:** Go, `text/template`, Caddyfile snippets, golden trees under `internal/render/testdata`.

**Spec:** `docs/specs/2026-10-14-account-signup-bans-recovery.md`, building on `docs/specs/2026-10-14-account-service.md` and `docs/specs/2026-10-14-operators-and-management.md`. The service's contract is `internal/config/config.go` and `README.md` of paisans-account-service at tag `v0.2.0`.

## Global Constraints

- `ACCOUNT_SIGNUP` is `invite` or `open`, required; `ACCOUNT_INVITES` is gone.
- `ACCOUNT_JOIN_PUBLIC_URL` is always rendered, `https://<join hostname>`, no trailing `/`, a different host from `ACCOUNT_PUBLIC_URL`; `ACCOUNT_INVITE_PUBLIC_URL` is gone.
- `ACCOUNT_OPERATORS` is the yaml operators' keys, sorted, comma separated.
- `ACCOUNT_COC_PATH`, `ACCOUNT_REQUIRE_APPROVAL`, `ACCOUNT_APPLICATION_WAIT_DAYS`, `ACCOUNT_DECLINE_RETENTION_DAYS`, `ACCOUNT_LOGIN_LINK_DAYS` render in both modes; the code of conduct is required and mounted in both.
- `ACCOUNT_INVITE_QUOTA` and `ACCOUNT_INVITE_LIFETIME_DAYS` render only with `signup: invite`.
- Rules: `account-signup-unset`, `account-setting-invalid` for any other signup value, `account-invite-settings-under-open-signup`. `account-serves-nothing` and `account-invite-hostname-without-invites` are removed.
- Hostname role `join`, `blueprints.JoinRole`, `blueprints.JoinHostname`: `hostnames.join` or `join.<domain>`.
- `internal/operatorreconciler` imports no toolkit package outside its tests and makes only `GET` requests.
- Names change outright: no alias, no migration code. Say "blueprint". No em dashes in new text. Docs describe current state; rejected alternatives only in `docs/decisions.md`.

## Review Focus

- A stale `hostnames.invite` in an old file: the `invite` role no longer exists, so `unknown-hostname-role` refuses it rather than it being silently dropped. Test in Task 2.
- `signup: true` or `signup: "Invite"` (case or type mistakes): `account-setting-invalid`, never treated as invite. Test in Task 2.
- Open mode `join.` must not pass `/i/<token>` or `/admin`, and the primary must still proxy bare `/` (the member home) while 404ing `/i/*`. Asserted in Task 3.
- A Pocket ID user disabled while still in `operators`, or the static API key's user, must not fail the reverse check: only enabled people count. Test in Task 5.
- A deployment with no `operators` group: reported once as "no group named operators", not once per user. Test in Task 5.

---

### Task 1: blueprints, the `signup` setting and the `join` role

**Files:**
- Modify: `internal/blueprints/account.go`, `internal/blueprints/hostnames.go`, `internal/blueprints/blueprint.go` (knownRoles), `internal/blueprints/reserved/account/blueprint.yaml` (`hostname_roles: ["join"]`)
- Rename: `internal/blueprints/reserved/account/caddy.snippet.invite.tmpl` to `caddy.snippet.join.tmpl`
- Test: `internal/blueprints/hostnames_role_test.go`

**Interfaces:**
- Produces: `const AccountSignupSetting = "signup"`, `const SignupInvite = "invite"`, `const SignupOpen = "open"`, `func AccountSignup(app config.App) string` (the declared value when it is one of the two, else ""), `var AccountInviteSettings = []string{"invite_quota", "invite_lifetime_days"}`, `const JoinRole = "join"`, `func JoinHostname(cfg *config.Config, app config.App) string` (empty for a non-account app or an empty domain).

- [ ] **Step 1: failing tests** in `hostnames_role_test.go`: `JoinHostname` is `join.example.org` for an account app with `signup: open`, with `signup: invite`, and with signup unset; `hostnames.join` wins; `RoleHostnames` lists `{join, join.example.org, Derived: true}` for every account app.
- [ ] **Step 2:** `go test ./internal/blueprints` fails (undefined `JoinHostname`).
- [ ] **Step 3: implement.**

```go
const JoinRole = "join"

func JoinHostname(cfg *config.Config, app config.App) string {
	if !HasHostnameRole(app.Blueprint, JoinRole) {
		return ""
	}
	if declared := strings.TrimSpace(app.Hostnames[JoinRole]); declared != "" {
		return declared
	}
	if cfg.Community.Domain == "" {
		return ""
	}
	return JoinRole + "." + cfg.Community.Domain
}
```

`RoleHostnames` has `case JoinRole:` calling `JoinHostname`, `Derived` when `hostnames.join` is blank.

- [ ] **Step 4:** `go test ./internal/blueprints` passes.
- [ ] **Step 5:** commit `feat: the account blueprint's join hostname and signup setting`.

### Task 2: validate

**Files:**
- Modify: `internal/validate/account.go`, `internal/validate/validate.go` (call site, `InviteRole` uses become `JoinRole`, duplicate-hostname help text)
- Fixtures: rename `account-invites-unset.yaml` to `account-signup-unset.yaml`, `account-invite-settings-without-invites.yaml` to `account-invite-settings-under-open-signup.yaml`; delete `account-serves-nothing.yaml`, `account-invite-hostname-without-invites.yaml`; every fixture's `invites: true` becomes `signup: invite`.
- Test: `internal/validate/account_test.go`, `internal/validate/validate_test.go`

**Interfaces:**
- Consumes: Task 1's names.

- [ ] **Step 1: failing tests:** `TestRulesFire` rows for `account-signup-unset` and `account-invite-settings-under-open-signup`; `signup` of `true`, `"Invite"`, `"closed"` is `account-setting-invalid` at `apps.account.settings.signup`; with `signup: open` and no invite settings nothing is refused; under open signup each of `invite_quota`, `invite_lifetime_days` is refused by its own key while `require_approval`, `application_wait_days`, `decline_retention_days`, `login_link_days` are accepted; the code of conduct is required under open signup; `hostnames.invite` is `unknown-hostname-role`; a wiki on `join.example.org` is a duplicate of the derived join hostname in both modes.
- [ ] **Step 2:** `go test ./internal/validate` fails.
- [ ] **Step 3: implement** `accountSignup()` replacing `accountInvites()`; `accountCodeOfConduct`, `accountSettingInvalid` and `account-login-link-disabled` read every account app, since both modes take applications and send a sign-in link.
- [ ] **Step 4:** passes.
- [ ] **Step 5:** commit `feat: validate settings.signup in place of settings.invites`.

### Task 3: render, templates and goldens

**Files:**
- Modify: `internal/render/account.go`, `internal/render/site.go` (`InviteRole` to `JoinRole`), `internal/blueprints/reserved/account/{.env.secret.tmpl,caddy.snippet.tmpl,caddy.snippet.join.tmpl,compose.yaml.tmpl,code-of-conduct.md.tmpl}`, `internal/render/testdata/deployment.yaml`
- Create: `internal/render/opensignup_test.go`, golden tree `internal/render/testdata/golden-open-signup/`
- Test: `internal/render/account_test.go`, `internal/render/pocketid_snippet_test.go`

**Interfaces:**
- Consumes: `blueprints.AccountSignup`, `blueprints.JoinHostname`.
- Produces: `accountValues{Signup, JoinHostname, Operators string, ...}`.

- [ ] **Step 1: failing tests:** `accountServiceEnv` is exactly v0.2.0's names (`ACCOUNT_SIGNUP`, `ACCOUNT_JOIN_PUBLIC_URL`, `ACCOUNT_OPERATORS` in, `ACCOUNT_INVITES`, `ACCOUNT_INVITE_PUBLIC_URL` out) and is checked in both modes, with `ACCOUNT_INVITE_QUOTA` and `ACCOUNT_INVITE_LIFETIME_DAYS` absent under open; `ACCOUNT_OPERATORS=alice,bob`; `ACCOUNT_JOIN_PUBLIC_URL=https://join.example.org`, and the declared one with `hostnames.join`; the code of conduct renders in both modes; the join snippet under invite has `@join path /i/* /static/*`, under open has a matcher for exactly `/` with `method GET POST` and one for `/static/*`, and neither passes `/i/*` under open; the primary snippet refuses `/i/*` in both modes and contains no rule for bare `/`; a `join.example.org` host block exists and is ungated in both modes; `TestGoldenOpenSignup` compares the open-signup tree.
- [ ] **Step 2:** `go test ./internal/render` fails.
- [ ] **Step 3: implement.** The join snippet:

```
{{- if eq .Account.Signup "open" }}
@register {
	path /
	method GET POST
}
@static path /static/*
...
{{- else }}
@join path /i/* /static/*
{{- end }}
```

Each passing matcher goes in its own `handle`, with a final `handle { respond 404 }`.

- [ ] **Step 4:** `go test ./internal/render -update`, read every diff, then `go test ./internal/render` passes.
- [ ] **Step 5:** commit `feat: render ACCOUNT_SIGNUP, ACCOUNT_JOIN_PUBLIC_URL and ACCOUNT_OPERATORS`.

### Task 4: dns and the create wizard

**Files:**
- Modify: `internal/dns/dns.go` (comment), `internal/dns/dns_test.go`, `internal/create/{answers.go,render.go,wizard.go,paisans-stack.yaml.tmpl}`, `internal/create/testdata/*.yaml`, `internal/create/*_test.go`, `cmd/paisans/create_test.go`, `examples/paisans-stack.example.yaml`

**Interfaces:**
- Produces: `create.Account{Hostname, Signup, CodeOfConductPath, CodeOfConduct}`.

- [ ] **Step 1: failing tests:** dns lists `join.example.org` derived for the account app in both modes; the wizard writes `signup: invite` by default and `signup: open` when the second choice is picked, with `code_of_conduct` in both.
- [ ] **Step 2:** fail.
- [ ] **Step 3: implement:** the wizard asks `Choose("How do people join?", ["a member's invite link", "a registration page anyone can open"])` and always asks for the code of conduct file.
- [ ] **Step 4:** `go test ./internal/create -update`, read diffs, `go test ./...` passes.
- [ ] **Step 5:** commit `feat: paisans create asks how people join`.

### Task 5: the operator reconciler's reverse check

**Files:**
- Modify: `internal/operatorreconciler/operatorreconciler.go`
- Test: `internal/operatorreconciler/operatorreconciler_test.go`

**Interfaces:**
- Consumes: the existing `Operators`, `OperatorsGroup`, `counts`. No new import.

- [ ] **Step 1: failing tests** (rows in `TestThePassChecksOperatorsAndAdministrators`): carol in `operators` gives `carol is in operators and not an operator`; carol disabled in `operators` is healthy; the static key's user in `operators` is healthy; with no `operators` group only `no group named operators`.
- [ ] **Step 2:** fails.
- [ ] **Step 3: implement** after check 1:

```go
// 3. Every enabled member of operators is an operator.
if grp := groups[OperatorsGroup]; grp != nil {
	var extra []string
	for _, u := range users {
		if counts(u) && u.InGroup(grp.ID) && !slices.Contains(operators, u.Username) {
			extra = append(extra, u.Username)
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		failures = append(failures, name+" is in operators and not an operator")
	}
}
```

- [ ] **Step 4:** passes; the tagged integration test runs if Docker and a Pocket ID image are available.
- [ ] **Step 5:** commit `feat: the operator reconciler reports a member of operators who is not one`.

### Task 6: documentation

**Files:** `README.md` (Accounts, the gateway, the reconciler), `CHANGELOG.md` `[Unreleased]`, `docs/decisions.md`, `docs/development.md`, the Status lines of `docs/specs/2026-10-14-account-signup-bans-recovery.md`, `docs/specs/2026-10-14-operators-and-management.md` and `docs/specs/2026-10-14-account-service.md`.

- [ ] **Step 1:** rewrite the affected README text to describe `signup`, `join.`, the routes per mode and the reverse check; one CHANGELOG bullet (at most 35 words); one decisions entry.
- [ ] **Step 2:** verify: links resolve, YAML blocks parse, `grep` the diff for em dashes in added lines.
- [ ] **Step 3:** commit `docs: account signup, join. and the operators group check`.
