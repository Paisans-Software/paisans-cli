# Mbin locked Instance, Meta and Federation settings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every field in Mbin's Meta, Instance and Federation admin settings is rendered from `paisans-stack.yaml` and locked, except the federation allow-list's contents.

**Architecture:** Three repositories, in order. The Mbin fork makes its last seven settings lockable and releases 1.16.0+paisans. The CLI gives templates the app's display name and the community name, under a new blueprint format `paisans.blueprint/v3` so older binaries skip the catalog release that uses them. The core catalog's mbin blueprint renders and locks every setting, and is released as v0.5.0; the CLI's test copy of the catalog then moves to it.

**Tech Stack:** PHP 8.5 / Symfony 7 (fork, run through `mbin-paisans/dev`), Go (CLI), Go `text/template` + CEL (blueprint).

**Spec:** `docs/specs/2026-10-18-mbin-locked-instance-settings.md` (in the CLI repo). Read it before any task.

## Global Constraints

- Defaults, setting keys and forced rules are the spec's table, verbatim. Do not invent others.
- Fork: commit messages one line with `FIX:`/`FEAT:` prefix plus a `Co-Authored-By` trailer; no em dashes in prose; `php-cs-fixer` clean; branch `fork/<thing>` from `origin/develop`; fork-only, no upstream branch.
- Fork release: `docs/03-contributing/05-releasing.md`. This is a minor bump: `1.15.3+paisans` to `1.16.0+paisans`. Version bump in its own PR; release PR `develop` into `main` titled `Release v1.16.0+paisans`.
- CLI: PRs into `develop` (protected, PR only). Conventional commit messages (`feat:`, `fix:`, `test:`). Docs describe current state only; never write rejected alternatives.
- Blueprint catalog: PR into `main`, then tag `v0.5.0` and push the tag. No CI there; the CLI's tests are the check, run against the local checkout with `CORE_CATALOG_DIR`.
- Never `git stash`. Worktrees in each repo's `.worktrees/`.
- Never touch the production deployment.

## Review Focus

1. **Gated app with `private_instance: false` or `federation_allow_list: false`.** Validate must refuse it, naming the key; a silent override would leave the gate's API reasoning unsound. Test in Task 5.
2. **App with no `display_name` and no `community.name`.** Instance title is the app's key and the meta title is that alone, never a dangling " · ". Test in Task 5.
3. **`federation: false` on a gated app.** Renders `KBIN_FEDERATION_ENABLED=false`; validate does not demand authorized fetch; the monitor seeds no signed-fetch probe for it. Test in Task 5.
4. **A string where a boolean belongs** (`registrations: "no"`). Refused by validate, like `authorized-fetch-not-a-boolean`. Test in Task 5.
5. **A meta title or description holding `"`, `#` or `$`.** The `.env` value round-trips intact through `envquote`. Test in Task 5.

---

### Task 1: Fork: the seven remaining settings become lockable

Repo: `src/mbin-paisans/mbin`, worktree `.worktrees/lock-instance-settings`, branch `fork/lock_instance_settings` from `origin/develop`. Fresh worktree: `cp .env.example .env`, `../../../dev 'composer install'`, `../../../dev 'composer -d tools install'`. Functional tests need `docker compose -f docker/tests/compose.yaml up -d` from the main checkout.

**Files:**
- Modify: `src/Service/SettingsManager.php` (constructor, `load()`, `LOCKABLE_SETTINGS`)
- Modify: `config/services.yaml` (seven parameters, seven `SettingsManager` arguments)
- Modify: `templates/admin/settings.html.twig` (seven `_locked_setting` includes)
- Modify: `.env.example`, `.env.example_docker` (document the seven variables, commented, with Mbin's defaults)
- Modify: `docs/02-admin/03-optional-features/11-locked_settings.md` (the table)
- Test: `tests/Unit/Service/SettingsManagerLockedSettingsTest.php`, `tests/Functional/Controller/Admin/AdminLockedSettingsTest.php`, `tests/Functional/Controller/Api/Instance/Admin/InstanceSettingsUpdateLockedApiTest.php`

**Interfaces:**
- Produces: environment variables, each named as the setting, with these fallbacks when unset (today's hard-coded values in `load()`): `KBIN_MERCURE_ENABLED` false, `KBIN_FEDERATED_SEARCH_ONLY_LOGGEDIN` true, `MBIN_SIDEBAR_SECTIONS_RANDOM_LOCAL_ONLY` false, `MBIN_SIDEBAR_SECTIONS_USERS_LOCAL_ONLY` false, `MBIN_SSO_REGISTRATIONS_ENABLED` true, `MBIN_RESTRICT_MAGAZINE_CREATION` false, `MBIN_SSO_SHOW_FIRST` false. All seven are in `SettingsManager::LOCKABLE_SETTINGS`, which then equals every `SettingsDto` property.

- [ ] **Step 1: Write the failing tests**

Unit (`SettingsManagerLockedSettingsTest`): update `createManager` for the seven new constructor arguments, then
- `testEveryInstanceSettingIsLockable`: `LOCKABLE_SETTINGS`, sorted, equals the names of `SettingsDto`'s public properties, sorted. It replaces `testSettingWithoutAnEnvironmentFallbackIsRefused`, whose example setting becomes lockable; keep that test only if a non-lockable property still exists.
- `testNewlyLockableSettingsReadTheEnvironmentOverADatabaseRow`: for each of the seven, a database row holding the opposite of the environment value is ignored when the setting is locked.
- `testNewlyLockableSettingsKeepTheirDefaultsWhenUnset`: unlocked, no row, constructed with the fallbacks above, `load()` gives exactly those values.

Functional:
- `AdminLockedSettingsTest::testLockedFieldsRenderDisabledWithANote` covers the seven: each renders disabled with the note when locked.
- `InstanceSettingsUpdateLockedApiTest::testApiRefusesAChangeToALockedSetting` covers one of the seven (`MBIN_SSO_REGISTRATIONS_ENABLED`), answering 400.

- [ ] **Step 2: Run them, expect failures**

Run: `../../../dev './bin/phpunit tests/Unit/Service/SettingsManagerLockedSettingsTest.php'`
Expected: FAIL (constructor arguments, missing lockable names).

- [ ] **Step 3: Implement**

Seven constructor parameters, typed `bool`, bound in `services.yaml` with `%env(bool:default:<param>:<VAR>)%` style defaults so an unset variable keeps today's value (follow how `mbin_downvotes_mode` names its default parameter). `load()` reads `?? $this->...` for each instead of the literal. Add the seven `_locked_setting.html.twig` includes beside their widgets, the seven names to `LOCKABLE_SETTINGS`, the seven rows to the docs table.

- [ ] **Step 4: Run tests, linters**

Run: `../../../dev './bin/phpunit tests/Unit'`, then `../../../dev './bin/phpunit tests/Functional/Controller/Admin/AdminLockedSettingsTest.php tests/Functional/Controller/Api/Instance/Admin'`, then `../../../dev './tools/vendor/bin/php-cs-fixer fix --dry-run'`
Expected: all pass; cs-fixer reports nothing beyond the generated, untracked `config/reference.php`.

- [ ] **Step 5: Commit, push, PR into `develop`, merge when checks pass**

Commit: `FEAT: make the remaining instance settings lockable from the environment`

### Task 2: Fork: release 1.16.0+paisans

- [ ] **Step 1:** Branch `fork/version_1_16_0` from `origin/develop`, `mbin_version: '1.16.0+paisans'` in `config/services.yaml`, PR, merge when checks pass.
- [ ] **Step 2:** Run the four `git diff --stat origin/main..origin/develop` checks from the release doc; PR `develop` into `main`, `Release v1.16.0+paisans`, body with the three required sections. The every-deploy section says: seven new optional environment variables, unset keeps today's behaviour, all seven now lockable.
- [ ] **Step 3:** Merge when the six checks pass. Verify: `gh release list --limit 1` shows `v1.16.0+paisans`; `docker manifest inspect ghcr.io/paisans-software/mbin:1.16.0-paisans` succeeds.

### Task 3: CLI: blueprint format v3 and the two names

Repo: `src/paisans-stack`, worktree `.worktrees/mbin-locked-settings`, branch `feat/mbin-locked-instance-settings` from `origin/develop`. Commit the spec and this plan on it first.

**Files:**
- Modify: `internal/blueprints/blueprint.go:16-22,180` (the format constants and the check)
- Modify: `internal/blueprints/heartbeat.go:52` (heartbeat allowed from v2 on, not only at the newest)
- Modify: `internal/render/appview.go` (`appValues` fields and where they are set, around line 406)
- Modify: `docs/development.md` (the blueprint format paragraph near line 1745) and wherever `docs/` documents blueprint keys and template values
- Test: `internal/blueprints/blueprint_test.go` (or the file already testing `apiVersion`), `internal/render/appview_test.go`

**Interfaces:**
- Produces: `blueprints.APIVersion = "paisans.blueprint/v3"`, `blueprints.APIVersionV2 = "paisans.blueprint/v2"`, `blueprints.APIVersionV1` unchanged; `Parse` reads all three. v3 is v2 plus two template values:
  - `appValues.DisplayName string`, `cfg.ClientName(app)`
  - `appValues.CommunityName string`, `cfg.Community.Name`

- [ ] **Step 1: Failing tests**
- `Parse` accepts a v3 blueprint, and a v3 blueprint with a heartbeat.
- `Parse` still refuses a heartbeat in a v1 blueprint, with the existing message naming the format that adds it (v2).
- `TestTemplatesReadTheDisplayAndCommunityNames` in `appview_test.go`: an app with `display_name: Talk` in a community named `Paisans` renders `.DisplayName` as `Talk` and `.CommunityName` as `Paisans`; with no `display_name` `.DisplayName` is the app's key.
- [ ] **Step 2:** `go test ./internal/blueprints/ ./internal/render/` fails.
- [ ] **Step 3:** Implement as the Interfaces say. The heartbeat check becomes "refused when the format is v1".
- [ ] **Step 4:** `go test ./internal/blueprints/ ./internal/render/` passes; `go vet ./...` clean.
- [ ] **Step 5:** Commit `feat: blueprint format v3 gives templates the display and community names`. No PR yet; Task 5 completes the branch.

### Task 4: Catalog: the mbin blueprint renders and locks every setting

Repo: `src/blueprints`, branch `feat/mbin-locked-instance-settings` from `origin/main`, worktree `.worktrees/mbin-locked-settings`.

**Files:**
- Modify: `blueprints/mbin/blueprint.yaml`
- Modify: `blueprints/mbin/.env.secret.tmpl`

**Interfaces:**
- Consumes: `.DisplayName`, `.CommunityName` (Task 3); `.Setting key fallback`, `.SettingBool key fallback`; `envquote`; `yesno`.
- Produces: `.env` keys and `MBIN_LOCKED_SETTINGS` for every row of the spec's table; setting keys exactly as the table spells them.

- [ ] **Step 1:** `apiVersion: paisans.blueprint/v3`. Image comment: the templates now need `1.16.0+paisans`'s seven lockable settings; the track `1.*.*-paisans` is unchanged since the newest 1.x carries them.
- [ ] **Step 2:** Template. Every Meta and Instance and Federation key from the table, string values through `envquote`, booleans through `yesno`. `KBIN_TITLE` defaults to `.DisplayName`; `KBIN_META_TITLE` defaults to `.DisplayName` + ` · ` + `.CommunityName`, or `.DisplayName` alone when `.CommunityName` is empty. `MBIN_PRIVATE_INSTANCE` and `MBIN_USE_FEDERATION_ALLOW_LIST` are `true` when gated, else their settings (default false). `MBIN_SSO_ONLY_MODE` stays as today; the non-OIDC branch renders `SSO_ONLY_MODE=false` so the lock has a value. `KBIN_FEDERATION_ENABLED` from `federation`, default true. Keep `MBIN_LOCKED_SETTINGS={{ .LockedSettings "," }}`.
- [ ] **Step 3:** `blueprint.yaml`. `locked_settings` names every rendered setting with no `when:` (each is now rendered in every case). `setting_keys` maps each Mbin key to its setting. `federation: {federates: true, setting: federation}`. `checks`: a bool check per boolean key in the voice of `authorized-fetch-not-a-boolean`; `mbin-downvotes-mode-unknown` (values from the fork's `App\Utils\DownvotesMode`); `mbin-gated-needs-private-instance` and `mbin-gated-needs-allow-list`, refusing a gated app that sets either false.
- [ ] **Step 4:** Verify against the CLI: in the CLI worktree, `CORE_CATALOG_DIR=<this worktree> scripts/fetch-core-testdata.sh v0.5.0`, then `go test ./...`. Expected failures are only the render tests Task 5 rewrites; any parse or validate failure is fixed here.
- [ ] **Step 5:** Commit `feat(mbin): render and lock every Meta, Instance and Federation setting`, push, PR into `main`, merge, then `git tag v0.5.0 && git push origin v0.5.0` on the merge commit.

### Task 5: CLI: test copy at v0.5.0, and the render and validate tests

Same branch as Task 3.

**Files:**
- Modify: `internal/blueprints/testdata/core/`, `internal/blueprints/testdata/core.release` (via `scripts/fetch-core-testdata.sh v0.5.0`, no `CORE_CATALOG_DIR`)
- Modify: `internal/render/mbin_env_test.go`
- Test: the validate test file holding the mbin checks (find with `grep -rn "authorized-fetch-not-a-boolean" internal/validate`)

- [ ] **Step 1:** Fetch the release copy. `mbinLockableSettings` gains the seven names, cited at `Paisans-Software/mbin v1.16.0+paisans`.
- [ ] **Step 2: Tests**, each a separate `Test` function:
  - `TestMbinLockedSettingsAreRendered` passes unchanged in intent (locked equals every rendered database setting, all lockable), now with every database setting rendered in every case.
  - `TestMbinTitlesComeFromTheDisplayAndCommunityNames`: `display_name: Talk`, community `Paisans` gives `KBIN_TITLE=Talk`, `KBIN_META_TITLE=Talk · Paisans`; no community name gives `KBIN_META_TITLE=Talk`; no `display_name` gives the app key `talk` for both.
  - `TestMbinSettingsOverrideTheirDefaults`: `title`, `meta_title`, `meta_description: 'He said "hi" #1 $5'` round-trip through `envValue`.
  - `TestMbinDefaults`: the table's defaults for an app that sets nothing (registrations `false`, SSO registrations `true`, Mercure `false`, federated search `true`, federation `true`, downvotes `enabled`, the rest as the table).
  - `TestMbinGatedForcesPrivateAndAllowList` and its ungated twin with both settings true.
  - `TestMbinFederationOff`: `federation: false` on a gated app renders `KBIN_FEDERATION_ENABLED=false`, and the uptime seed has no signed-fetch probe for it.
  - Validate: a gated app with `private_instance: false` is refused naming `apps.talk.settings.private_instance`; same for `federation_allow_list`; `registrations: "no"` refused; `downvotes_mode: sideways` refused; `federation: false` on a gated app is not refused for authorized fetch.
- [ ] **Step 3:** `go test ./...` passes; `go vet ./...` clean; `gofmt -l .` empty.
- [ ] **Step 4:** Docs: wherever `docs/` lists mbin's settings, list the new keys with their defaults, describing current state only.
- [ ] **Step 5:** Commit, push, PR into `develop` with the spec, plan and code; merge when checks pass.
