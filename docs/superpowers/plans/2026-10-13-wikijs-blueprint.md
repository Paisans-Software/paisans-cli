# Wiki.js Blueprint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the core catalog's `outline` blueprint with a `wikijs` blueprint (Wiki.js 2.5.316 plus a bootstrap sidecar), with the toolkit changes that make it possible and the removal of Outline from the toolkit.

**Architecture:** A new Go program, `wikijs-bootstrap`, finishes Wiki.js's setup and converges its sign-in, site settings and groups through Wiki.js's GraphQL API, coordinating clustered instances with a Postgres advisory lock and stopping a stale setup-mode instance through a shared process namespace. The `wikijs` blueprint in `paisans-blueprints` runs Wiki.js and the bootstrap. `paisans-stack` gains two generic features (a `list` setting type, and an `oidc.extra_groups_setting` that creates further Pocket ID groups with confirmation), then pins the new catalog and drops Outline.

**Tech Stack:** Go 1.25 (`github.com/jackc/pgx/v5` for the bootstrap), Wiki.js 2.5.316 GraphQL, Docker Compose, Caddy snippets, GitHub Actions publishing multi-arch images to ghcr.io.

**Spec:** `docs/specs/2026-10-13-wikijs-blueprint.md` (this branch). Read it before any task; every task argues from it.

## Global Constraints

- Wiki.js image: `ghcr.io/requarks/wiki:2.5.316`, pinned by digest (re-resolve; 2026-10-13 value was `sha256:fffff288a52f76e5b8c19a4fbcd0f3e538e96edea642c4f2d813d54d59a876de`).
- Pocket ID citations are at `v2.14.0`; Wiki.js citations at `v2.5.316`.
- Strategy key is exactly `pocketid`; callback is exactly `/login/pocketid/callback`.
- OAuth2 scope is exactly `openid profile email groups`.
- Admin group permissions: every Wiki.js permission except `manage:system` and `manage:api`.
- `Guests` (group ID 2): no permissions, no page rules.
- `/finalize` is called only when the `settings` table is empty, under the advisory lock. Never otherwise.
- The bootstrap writes only what the spec's *What it writes, and only that* table lists; a test enforces it.
- Clustered DSN carries `?sslmode=no-verify`; pinned DSN carries no `sslmode`.
- No em dashes in anything written in `paisans-stack` (docs, comments, commits, PR text). No rejected alternatives outside `docs/decisions.md`.
- No real hostnames, addresses or secrets in any repo: examples use `example.org`.
- `main` and `develop` in every repo take pull requests only. Never push to them. Never tag or release: that is the human's action.
- Never `git stash` (shared across worktrees). Commit WIP instead.
- Commit trailer: `Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>`.
- Creating the `Paisans-Software/wikijs-bootstrap` GitHub repository is the human's action (Task 3 gate).

## Review Focus

1. **A second bootstrap racing the first on an empty database** must never call `/finalize` once the `settings` table has rows; the second one must stop its stale setup-mode instance instead. Pinned by Task 11's `TestSecondInstanceStopsItsStaleAppInsteadOfFinalizing`.
2. **An admin group whose existing page rules an admin wrote** must keep them when the bootstrap resets its permissions. Pinned by Task 11's `TestAdminGroupKeepsItsPageRules`.
3. **A `synced_groups` that does not include the admin group** must be refused by `paisans validate`, not discovered after deploy. Pinned by Task 13's `checks:` rule, Task 7's `TestSyncedGroupsMustHoldTheAdminGroup` and Task 16's fixture.
4. **A clustered DSN with `sslmode=no-verify`** must still connect from the bootstrap (pgx does not know `no-verify`). Pinned by Task 7's `TestNoVerifyBecomesRequireForPgx`.
5. **A Wiki.js that answers but whose login fails** (wrong password after someone reset it) must leave the bootstrap unhealthy with a message naming the break glass account, not crash-loop silently. Pinned by Task 11's `TestLoginFailureIsReportedNotRetriedSilently`.

---

## Phase 0: Observe what the spec reasons about

### Task 1: End-to-end spike (throwaway)

The spec's *To verify before building* items are observed here. Nothing in this task is kept except the findings file.

**Files:**
- Create (scratch, not committed anywhere): `spike/compose.yaml`, `spike/pg-tls/`
- Create: `docs/specs/2026-10-13-wikijs-blueprint-findings.md` in `paisans-stack` (this branch)

- [ ] **Step 1: Bring up Postgres with TLS, Pocket ID and two Wiki.js instances**

`spike/compose.yaml`:

```yaml
name: wikijs-spike
services:
  postgres:
    image: postgres:18
    environment: {POSTGRES_DB: wiki, POSTGRES_USER: wiki, POSTGRES_PASSWORD: spike-only-not-secret}
    command: ["-c", "ssl=on", "-c", "ssl_cert_file=/tls/server.crt", "-c", "ssl_key_file=/tls/server.key", "-c", "hba_file=/tls/pg_hba.conf"]
    volumes: ["./pg-tls:/tls:ro"]
  wiki-a:
    image: ghcr.io/requarks/wiki:2.5.316
    environment:
      DB_TYPE: postgres
      DATABASE_URL: postgresql://wiki:spike-only-not-secret@postgres:5432/wiki?sslmode=no-verify
      HA_ACTIVE: "true"
    ports: ["3001:3000"]
  wiki-b:
    image: ghcr.io/requarks/wiki:2.5.316
    environment:
      DB_TYPE: postgres
      DATABASE_URL: postgresql://wiki:spike-only-not-secret@postgres:5432/wiki?sslmode=no-verify
      HA_ACTIVE: "true"
    ports: ["3002:3000"]
  probe:
    image: alpine:3.22
    pid: "service:wiki-a"
    command: ["sleep", "infinity"]
    restart: unless-stopped
```

Generate a self-signed cert into `spike/pg-tls/` (`openssl req -x509 -newkey rsa:2048 -nodes -subj /CN=postgres -keyout server.key -out server.crt -days 2`, `chmod 600 server.key`, owned by uid 999), and a `pg_hba.conf` of:

```
local all all trust
hostnossl all all all reject
hostssl all all all scram-sha-256
```

Run: `docker compose -f spike/compose.yaml up -d`, then `docker compose logs wiki-a | tail -20`.
Expected: wiki-a and wiki-b reach "Setup mode" over TLS (no `pg_hba.conf rejects` error). Record whether `sslmode=no-verify` worked.

- [ ] **Step 2: Finalize through wiki-a only, then observe wiki-b**

Run: `curl -s -X POST localhost:3001/finalize -H 'content-type: application/json' -d '{"adminEmail":"admin@example.org","adminPassword":"spike-only-not-secret","siteUrl":"http://localhost:3001","telemetry":false}'`
Then: `curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:3002/graphql -H 'content-type: application/json' -d '{"query":"{__typename}"}'`
Expected: finalize `{"ok":true}`; wiki-b answers **404** (still in setup mode). Record it.

- [ ] **Step 3: Stop wiki-a's process from the probe sharing its namespace**

Run: `docker compose exec probe sh -c 'for p in /proc/[0-9]*; do tr "\0" " " < $p/cmdline; echo " <- $p"; done'` to see the node process, then `docker compose exec probe kill -TERM <pid>`; then `docker compose ps`.
Expected: wiki-a restarts (restart policy needs adding to wiki-a for this step: `restart: unless-stopped`). Record whether `probe` was killed with it and whether it came back and shares the new namespace (repeat the `/proc` listing).

- [ ] **Step 4: Configure OAuth2 against Pocket ID and sign in**

Add `ghcr.io/pocket-id/pocket-id:v2.14.0` to the compose file (`APP_URL=http://localhost:1411`, port 1411), create an admin, a group `members`, a user in it, and an OIDC client with callback `http://localhost:3001/login/pocketid/callback`, restricted to `members`. Then log in to wiki-a as the local admin with `mutation{authentication{login(username:"admin@example.org",password:"spike-only-not-secret",strategy:"local"){jwt responseResult{succeeded message}}}}` and send `authentication.updateStrategies` with `local` (order 1) and `pocketid` (strategyKey `oauth2`, order 0) using the config in the spec's *Sign-in* table, plus `groups.create(name:"members")`.
Expected, record each:
- whether Pocket ID shows a consent screen on first sign-in with the `groups` scope;
- whether the signed-in user lands in Wiki.js group `members`;
- whether `/login/pocketid` started from a deep link returns there (`loginRedirect`);
- whether wiki-b (restarted so it left setup mode) uses the new strategy without its own `updateStrategies`.

- [ ] **Step 5: Restart Postgres and check HA listening**

Run: `docker compose restart postgres`, wait 30 s, change the site title on wiki-a via `site.updateConfig`, then read it on wiki-b (`{site{config{title}}}` with an admin token).
Expected: record whether wiki-b sees the change after the database restart.

- [ ] **Step 6: Write the findings and stop if any contradicts the spec**

Write `docs/specs/2026-10-13-wikijs-blueprint-findings.md` with one section per step: what was run, what was observed. If any observation contradicts the spec (TLS fails, the probe does not come back, groups do not map, a consent screen blocks the flow), **stop and report to the human before Task 2**; the spec changes first.

- [ ] **Step 7: Commit the findings**

```bash
git add docs/specs/2026-10-13-wikijs-blueprint-findings.md
git commit -m "docs: what the Wiki.js spike observed" -m "Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Phase 1: `paisans-stack` generic features

Work in a new worktree off `origin/develop`: `git worktree add .worktrees/wikijs-toolkit -b feat/list-settings-and-extra-groups origin/develop`. Tasks 2 to 5 land in one pull request.

### Task 2: A `list` setting type, and `SettingList`/`SettingInt` for templates

**Files:**
- Modify: `internal/kinds/checks.go` (`settingTypes`, `SettingSpec.Problem`)
- Modify: `internal/kinds/blueprint.go` (the `settings.%s.type` error text)
- Modify: `internal/render/appview.go` (add `SettingList`, `SettingInt` beside `SettingBool`)
- Test: `internal/kinds/checks_test.go`, `internal/render/appview_test.go` (create the latter if absent; otherwise append)

**Interfaces:**
- Produces: `SettingSpec{Type: "list"}` accepting `[]any` of non-empty strings; `func (v appValues) SettingList(key string) []string`; `func (v appValues) SettingInt(key string, fallback int) int`.

- [ ] **Step 1: Write the failing tests**

In `internal/kinds/checks_test.go`:

```go
func TestAListSettingTakesNonEmptyStrings(t *testing.T) {
	s := SettingSpec{Type: "list"}
	if p := s.Problem([]any{"admins", "members"}); p != "" {
		t.Errorf("a list of names: %s", p)
	}
	for _, bad := range []any{"admins", []any{"admins", ""}, []any{"admins", 3}} {
		if s.Problem(bad) == "" {
			t.Errorf("%#v passed as a list of strings", bad)
		}
	}
}
```

In `internal/render/appview_test.go`:

```go
func TestSettingListAndIntReadTheSeenSettings(t *testing.T) {
	v := appValues{set: map[string]any{"groups": []any{"admins", "members"}, "mb": 12}}
	if got := fmt.Sprint(v.SettingList("groups")); got != "[admins members]" {
		t.Errorf("SettingList = %s", got)
	}
	if v.SettingList("absent") != nil {
		t.Error("an absent list is not nil")
	}
	if v.SettingInt("mb", 5) != 12 || v.SettingInt("absent", 5) != 5 {
		t.Error("SettingInt does not read the setting or its fallback")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/kinds/ -run TestAListSettingTakesNonEmptyStrings ./internal/render/ -run TestSettingListAndIntReadTheSeenSettings`
Expected: FAIL (the list passes as anything; `SettingList` undefined).

- [ ] **Step 3: Implement**

`internal/kinds/checks.go`:

```go
var settingTypes = map[string]bool{"string": true, "bool": true, "int": true, "email": true, "enum": true, "list": true}
```

and in `Problem`, before the closing `}` of the switch:

```go
	case "list":
		items, ok := v.([]any)
		if !ok {
			return fmt.Sprintf("is %v, not a list", v)
		}
		for i, item := range items {
			if str, ok := item.(string); !ok || strings.TrimSpace(str) == "" {
				return fmt.Sprintf("item %d is %v, not a name", i, item)
			}
		}
```

Update the SettingSpec doc comment's first line to "string, bool, int, email, enum with its values, or list (of strings)".

`internal/kinds/blueprint.go`, the type error:

```go
			return nil, fmt.Errorf("settings.%s.type: is %q; it must be string, bool, int, email, enum or list", key, spec.Type)
```

`internal/render/appview.go`, after `SettingBool`:

```go
// SettingList returns a declared list setting, or a catalog blueprint's
// default for it, as strings; nil when there is neither.
func (v appValues) SettingList(key string) []string {
	items, ok := v.set[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// SettingInt returns a declared whole number setting, or a catalog
// blueprint's default for it, or the fallback.
func (v appValues) SettingInt(key string, fallback int) int {
	if n, ok := v.set[key].(int); ok {
		return n
	}
	return fallback
}
```

- [ ] **Step 4: Run the tests, then the package suites**

Run: `go test ./internal/kinds/ ./internal/render/ ./internal/validate/`
Expected: PASS.

- [ ] **Step 5: Document the type in the README**

Find the README passage that lists setting types (`grep -n 'string, bool, int, email' README.md`) and add `list` with one sentence: "`list`, a list of names, for a setting such as a set of groups."

- [ ] **Step 6: Commit**

```bash
git add internal/kinds/checks.go internal/kinds/blueprint.go internal/kinds/checks_test.go internal/render/appview.go internal/render/appview_test.go README.md
git commit -F - <<'EOF'
feat: blueprint settings take a list of names

A catalog blueprint can now declare a setting of type list, a list of
non-empty strings, and read it in a template with .SettingList. Templates
also read whole numbers with .SettingInt, which int settings had no way to
render. The wikijs blueprint's synced_groups is the first list.

Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

### Task 3: `oidc.extra_groups_setting` finds or creates further groups

**Files:**
- Modify: `internal/kinds/blueprint.go` (`oidcSpec`, `OIDCDef` construction)
- Modify: `internal/kinds/oidc.go` (`OIDCDef`, `OIDCClientSpec`, `OIDCClient`, new `ExtraGroups`)
- Modify: `internal/oidcclient/oidcclient.go` (`Desired.ExtraGroups`, `Probe`, `Build`, `Step.Extra`)
- Modify: `cmd/paisans/clients.go` (`clientDesired`)
- Test: `internal/oidcclient/oidcclient_test.go`, `internal/kinds/oidc_client_test.go`

**Interfaces:**
- Consumes: Task 2's `list` type (the setting named by `extra_groups_setting` is a list).
- Produces: `func (s OIDCClientSpec) ExtraGroups(app config.App) []string` (reads `kinds.SettingsSeen(app)`, drops names equal to the member or admin group, keeps order, no duplicates); `Desired.ExtraGroups []string`; `Step.Extra bool` set on a `CreateGroup` step for an extra group.

- [ ] **Step 1: Write the failing tests**

`internal/oidcclient/oidcclient_test.go`:

```go
// Extra groups are created when missing, and never join the client's
// allowed groups, which stay the member and admin groups.
func TestExtraGroupsAreCreatedButNotAllowed(t *testing.T) {
	f := newFake()
	d := talk()
	d.MemberGroup = "members"
	d.ExtraGroups = []string{"moderators"}
	p := plan(t, f, d, Recorded{})
	var extra []string
	for _, s := range p.Steps {
		if s.Kind == CreateGroup && s.Extra {
			extra = append(extra, s.Group)
		}
	}
	if fmt.Sprint(extra) != "[moderators]" {
		t.Fatalf("extra group steps %v", extra)
	}
	rec := &memRecorder{}
	if err := Execute(p, api(f), rec, secretSource("extra-secret-not-real-0001")); err != nil {
		t.Fatal(err)
	}
	var allowed []string
	for _, g := range f.clients[0].AllowedUserGroups {
		allowed = append(allowed, g.Name)
	}
	if fmt.Sprint(allowed) != "[members admins]" {
		t.Errorf("allowed %v", allowed)
	}
}

// An extra group Pocket ID already has plans nothing.
func TestAnExistingExtraGroupIsPresent(t *testing.T) {
	f := newFake()
	d := talk()
	d.MemberGroup = "members"
	d.ExtraGroups = []string{"moderators"}
	if err := Execute(plan(t, f, d, Recorded{}), api(f), &memRecorder{}, secretSource("extra-secret-not-real-0002")); err != nil {
		t.Fatal(err)
	}
	for _, s := range plan(t, f, d, Recorded{}).Steps {
		if s.Kind == CreateGroup {
			t.Errorf("planned %s again", s.Group)
		}
	}
}
```

`internal/kinds/oidc_client_test.go`:

```go
func TestExtraGroupsDropTheClientsOwnGroups(t *testing.T) {
	s := OIDCClientSpec{AdminGroupSetting: "admin_group", MemberGroupSetting: "member_group", DefaultAdminGroup: "admins", DefaultMemberGroup: "members", ExtraGroupsSetting: "synced_groups"}
	app := config.App{Settings: map[string]any{"synced_groups": []any{"admins", "members", "moderators", "moderators"}}}
	if got := fmt.Sprint(s.ExtraGroups(app)); got != "[moderators]" {
		t.Errorf("ExtraGroups = %s", got)
	}
}
```

(`ExtraGroups` reads `SettingsSeen`, which returns `app.Settings` for a kind not from a catalog, so this test needs no catalog.)

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/oidcclient/ -run Extra ./internal/kinds/ -run TestExtraGroups`
Expected: FAIL to compile (`ExtraGroups`, `Extra`, `ExtraGroupsSetting` undefined).

- [ ] **Step 3: Implement the kinds side**

`internal/kinds/blueprint.go`, in `oidcSpec` after `ReadsGroups`:

```go
	// ExtraGroupsSetting names a list setting whose groups apply finds or
	// creates at the identity provider beside the member and admin groups.
	ExtraGroupsSetting string `yaml:"extra_groups_setting"`
```

and in the `&OIDCDef{...}` literal add `ExtraGroupsSetting: o.ExtraGroupsSetting,`.

`internal/kinds/oidc.go`: add `ExtraGroupsSetting string` to `OIDCDef` (after `ReadsGroups`, comment "ExtraGroupsSetting is OIDCClientSpec's field of the same name.") and to `OIDCClientSpec` with:

```go
	// ExtraGroupsSetting names an app `settings` key, a list, of further
	// groups the app reads from the groups claim. apply finds or creates each
	// at Pocket ID. They are not added to the client's allowed groups: who
	// may sign in stays the member and admin groups.
	ExtraGroupsSetting string
```

copy it in `OIDCClient` (`ExtraGroupsSetting: o.ExtraGroupsSetting,`), and add:

```go
// ExtraGroups is the app's further groups, from the setting
// ExtraGroupsSetting names, or the blueprint's default for it, without the
// member and admin groups and without repeats, in the order written.
func (s OIDCClientSpec) ExtraGroups(app config.App) []string {
	if s.ExtraGroupsSetting == "" {
		return nil
	}
	items, _ := SettingsSeen(app)[s.ExtraGroupsSetting].([]any)
	admin, member := s.Groups(app)
	seen := map[string]bool{admin: true, member: true, "": true}
	var out []string
	for _, item := range items {
		name, _ := item.(string)
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}
```

- [ ] **Step 4: Implement the oidcclient side**

`internal/oidcclient/oidcclient.go`: in `Desired` after `MemberGroup`:

```go
	// ExtraGroups are further groups the app reads from the groups claim.
	// Each is found or created, and none is allowed on the client.
	ExtraGroups []string
```

In `Step`, add `Extra bool` with the comment "Extra marks a CreateGroup step for one of Desired.ExtraGroups, which apply confirms before it runs."

In `Probe`, change the group loop to look up every group:

```go
	for _, name := range append(d.groups(), d.ExtraGroups...) {
```

In `Build`, after the existing `for _, g := range d.groups()` loop:

```go
	for _, g := range d.ExtraGroups {
		if s.Groups[g] == nil {
			body, _ := json.Marshal(map[string]string{"name": g, "friendlyName": g})
			p.Steps = append(p.Steps, Step{Kind: CreateGroup, Group: g, Extra: true, Title: "create group " + g, Detail: fmt.Sprintf("create group %s, which %s reads from the groups claim: POST /api/user-groups %s", g, d.App, body)})
		} else {
			p.Present = append(p.Present, "group "+g)
		}
	}
```

Do not touch `d.groups()`: it is also the allowed groups set.

`cmd/paisans/clients.go`, in `clientDesired` after `MemberGroup: memberGroup,`:

```go
		ExtraGroups:       spec.ExtraGroups(app),
```

- [ ] **Step 5: Run the tests and the suites they touch**

Run: `go test ./internal/kinds/ ./internal/oidcclient/ ./cmd/paisans/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/kinds internal/oidcclient cmd/paisans/clients.go
git commit -F - <<'EOF'
feat: a blueprint can name further groups for apply to create at Pocket ID

oidc.extra_groups_setting names a list setting of groups the app reads from
the groups claim beside its member and admin groups. apply finds each at
Pocket ID and plans its creation when missing. They are never added to the
client's allowed groups, so who may sign in is unchanged.

Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

### Task 4: `apply` asks before creating an extra group

**Files:**
- Modify: `cmd/paisans/clients.go` (`clientStep.confirm`, check before `Execute`)
- Modify: `cmd/paisans/main.go:647` area (wire `confirm` from `runApply`'s stdin/stdout)
- Test: `cmd/paisans/clients_test.go` (append; create if absent)

**Interfaces:**
- Consumes: Task 3's `Step.Extra`.
- Produces: `clientStep.confirm func(what string) error`; nil means "cannot ask".

- [ ] **Step 1: Write the failing test**

```go
// An extra group is created only once a person types the word. Without a
// way to ask, the app is refused and Pocket ID is sent nothing.
func TestExtraGroupCreationAsksFirst(t *testing.T) {
	p := oidcclient.Plan{Steps: []oidcclient.Step{{Kind: oidcclient.CreateGroup, Group: "moderators", Extra: true}}}
	c := &clientStep{}
	if err := c.approveExtraGroups("wiki", p); err == nil || !strings.Contains(err.Error(), "moderators") {
		t.Errorf("no way to ask, err = %v", err)
	}
	var asked string
	c.confirm = func(what string) error { asked = what; return nil }
	if err := c.approveExtraGroups("wiki", p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(asked, "moderators") {
		t.Errorf("asked %q", asked)
	}
	c.confirm = func(string) error { return errors.New("declined") }
	if err := c.approveExtraGroups("wiki", p); err == nil {
		t.Error("a declined answer let it through")
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./cmd/paisans/ -run TestExtraGroupCreationAsksFirst`
Expected: FAIL to compile (`approveExtraGroups`, `confirm` undefined).

- [ ] **Step 3: Implement**

In `clientStep` add:

```go
	// confirm asks a person at the terminal before apply creates a group an
	// app names in its extra groups. Creating an identity provider group
	// beyond an app's own takes explicit approval under the deployment agent
	// rules. Nil when apply cannot ask, which refuses the app instead.
	confirm func(what string) error
```

and the method:

```go
// approveExtraGroups asks once for every extra group plan p creates.
func (c *clientStep) approveExtraGroups(app string, p oidcclient.Plan) error {
	var groups []string
	for _, s := range p.Steps {
		if s.Kind == oidcclient.CreateGroup && s.Extra {
			groups = append(groups, s.Group)
		}
	}
	if len(groups) == 0 {
		return nil
	}
	what := fmt.Sprintf("apply will create the group(s) %s at Pocket ID, which %s reads from the groups claim. They start empty.", strings.Join(groups, ", "), app)
	if c.confirm == nil {
		return fmt.Errorf("%s names group(s) %s that Pocket ID does not have, and creating them asks at a terminal. Run apply from an interactive shell, or create them at Pocket ID first", app, strings.Join(groups, ", "))
	}
	if err := c.confirm(what); err != nil {
		return fmt.Errorf("group(s) %s for %s: %w", strings.Join(groups, ", "), app, err)
	}
	return nil
}
```

In the execute loop shown at `cmd/paisans/clients.go` (`for _, p := range plans { if len(p.plan.Steps) == 0 { continue } ...`), before `oidcclient.Execute`:

```go
		if err := c.approveExtraGroups(p.app, p.plan); err != nil {
			c.refuse(p.app, err)
			continue
		}
```

In `cmd/paisans/main.go`, right after `clients, err := newClientStep(...)` and its error check, when `clients != nil`:

```go
		clients.confirm = func(what string) error { return confirmWord(stdin, stdout, "create", what) }
```

(`runApply` has `stdin io.Reader, stdout io.Writer`, `main.go:460`.)

- [ ] **Step 4: Run the test and the package**

Run: `go test ./cmd/paisans/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/paisans/clients.go cmd/paisans/main.go cmd/paisans/clients_test.go
git commit -F - <<'EOF'
feat: apply asks at the terminal before it creates an app's extra groups

Creating an identity provider group beyond an app's own client and groups
takes explicit approval under the deployment agent rules, so apply lists the
groups and asks for the word create. Without a terminal it refuses the app
and sends Pocket ID nothing. Until the founder decides whether the rules'
exception covers these groups, this is how the approval is given.

Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

### Task 5: Validation refuses an extra groups setting that names no group

**Files:**
- Modify: `internal/validate/validate.go` (`oidcMemberGroupNotAName`)
- Create: `internal/validate/testdata/oidc-extra-groups-not-names.yaml`
- Modify: `internal/validate/validate_test.go` (the fixture table)

**Interfaces:**
- Consumes: Task 3's `OIDCClientSpec.ExtraGroupsSetting`.

The list type (Task 2) already refuses a non-list or an empty item through `blueprint-setting-invalid`, for a blueprint that declares the setting. This task covers the identity-provider meaning: a name Pocket ID would refuse.

- [ ] **Step 1: Read how fixtures are wired**

Run: `grep -n 'oidc-member-group-not-a-name' internal/validate/*_test.go`
Expected: the table row pairing the fixture with its rule ID. The new row follows the same shape.

- [ ] **Step 2: Write the failing fixture and row**

Copy `internal/validate/testdata/oidc-member-group-not-a-name.yaml` to `oidc-extra-groups-not-names.yaml`. Its first line becomes `# valid.yaml with a wikijs app whose synced_groups holds a name with a space.` and its `apps:` gains a `wiki` app using the testdata catalog's `wikijs` blueprint once it exists (Task 15 adds it to the testdata catalog). **Until Task 15, add this fixture's row with `t.Skip("needs the wikijs testdata blueprint, Task 15")`** and remove the skip in Task 15. The row expects rule ID `oidc-group-not-a-name`.

The fixture's app:

```yaml
  wiki:
    blueprint: wikijs
    hostname: wiki.example.org
    placement: cluster
    settings:
      synced_groups: [admins, members, "mod team"]
```

- [ ] **Step 3: Implement**

Keep the existing rule ID `oidc-member-group-not-a-name` unchanged. Inside `oidcMemberGroupNotAName`'s `for _, name := range c.cfg.AppNames()` loop, after the existing inner loop (where `name` is the app and `app` its declaration), add:

```go
		for _, group := range spec.ExtraGroups(app) {
			if pocketid.ValidGroupName(group) {
				continue
			}
			c.refuse("oidc-group-not-a-name", fmt.Sprintf("apps.%s.settings.%s", name, spec.ExtraGroupsSetting),
				"a group to create is not a group name",
				"%q is not a name Pocket ID takes for a group. Use letters, digits, dots, dashes and underscores.", group)
		}
```

Find the group name rule Pocket ID enforces: run `grep -rn 'func.*ValidGroupName\|groupNamePattern\|user_group.*name' internal/pocketid/` . If the toolkit has no such check yet, add to `internal/pocketid`:

```go
// ValidGroupName is whether Pocket ID v2.14.0 takes name for a group: the
// pattern its user group DTO binds the name to.
func ValidGroupName(name string) bool { return groupName.MatchString(name) }

var groupName = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)
```

and confirm the pattern against Pocket ID v2.14.0 `backend/internal/dto/user_group_dto.go` (the `binding` on `Name`) before committing; cite the line in the comment. (The scratch clone from the spike session is at the session scratchpad `pid214/src`; otherwise `git clone --depth 1 --branch v2.14.0 https://github.com/pocket-id/pocket-id.git`.)

- [ ] **Step 4: Run validate's suite**

Run: `go test ./internal/validate/ ./internal/pocketid/`
Expected: PASS (the new row skipped until Task 15).

- [ ] **Step 5: Commit, push the branch, open the pull request**

```bash
git add internal/validate internal/pocketid
git commit -F - <<'EOF'
feat: validate refuses a group to create that Pocket ID would not take

An app's extra groups are created at Pocket ID by apply, so a name Pocket
ID refuses would fail at apply instead of at validate. The rule uses the
pattern Pocket ID binds a group name to.

Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>
EOF
git push -u origin feat/list-settings-and-extra-groups
gh pr create --base develop --title "feat: list settings, and extra groups apply creates at Pocket ID" --body "$(cat <<'EOF'
Two generic additions a catalog blueprint needs to map Pocket ID groups into an app: a list setting type, and oidc.extra_groups_setting, naming groups apply finds or creates at Pocket ID without allowing them on the client. apply asks at the terminal before creating one, and validate refuses a name Pocket ID would not take.

Spec: docs/specs/2026-10-13-wikijs-blueprint.md

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Stop here for the human to review and merge before Phase 3 depends on it.

---

## Phase 2: `Paisans-Software/wikijs-bootstrap`

### Task 6 (gate): The human creates the repository

- [ ] **Step 1: Ask**

Ask the human to create `Paisans-Software/wikijs-bootstrap` (public, default branch `main`, branch protection matching `paisans-stack`: pull requests only, no bypass). Creating a public repository is outward facing and theirs. Wait for confirmation.

- [ ] **Step 2: Clone it into the community workspace**

```bash
cd /Users/wash/Developer/paisans.community/src
git clone https://github.com/Paisans-Software/wikijs-bootstrap.git
cd wikijs-bootstrap && printf '.worktrees/\n' > .gitignore
git worktree add .worktrees/initial -b feat/initial
```

Add `src/wikijs-bootstrap/` to the community repo's root `.gitignore` with a comment ("the Wiki.js bootstrap's own repository"), on a branch there, per `src/CLAUDE.md` *Adding something new here*.

### Task 7: Module, configuration from the environment

All paths in Tasks 7 to 12 are relative to `src/wikijs-bootstrap/.worktrees/initial`.

**Files:**
- Create: `go.mod`, `internal/config/config.go`, `internal/config/config_test.go`

**Interfaces:**
- Produces:

```go
type Config struct {
	WikiURL, DatabaseURL, PublicURL                   string
	AdminEmail, AdminPassword                         string
	ClientID, ClientSecret                            string
	AuthorizeURL, TokenURL, UserinfoURL, LogoutURL    string
	AdminGroup                                        string
	SyncedGroups                                      []string
	Title, Description, LogoURL                       string
	UploadMaxBytes                                    int
	HealthAddr                                        string
}
func FromEnv(get func(string) string) (Config, error)
func (c Config) PgxDSN() string // DatabaseURL with sslmode=no-verify replaced by sslmode=require
```

- [ ] **Step 1: `go mod init`**

```bash
go mod init github.com/paisans-software/wikijs-bootstrap
go mod edit -go=1.25
go get github.com/jackc/pgx/v5@latest
```

- [ ] **Step 2: Write the failing tests**

`internal/config/config_test.go`:

```go
package config

import (
	"strings"
	"testing"
)

func env(over map[string]string) func(string) string {
	base := map[string]string{
		"WIKI_URL": "http://app:3000", "DATABASE_URL": "postgresql://u:p@db:5432/wiki?sslmode=no-verify",
		"PUBLIC_URL": "https://wiki.example.org", "ADMIN_EMAIL": "wikijs-admin@example.org", "ADMIN_PASSWORD": "not-a-real-password",
		"OAUTH_CLIENT_ID": "cid", "OAUTH_CLIENT_SECRET": "not-a-real-secret",
		"OAUTH_AUTHORIZE_URL": "https://id.example.org/authorize", "OAUTH_TOKEN_URL": "https://id.example.org/api/oidc/token",
		"OAUTH_USERINFO_URL": "https://id.example.org/api/oidc/userinfo", "OAUTH_LOGOUT_URL": "https://id.example.org/api/oidc/end-session",
		"ADMIN_GROUP": "admins", "SYNCED_GROUPS": "admins,members", "SITE_TITLE": "Example Wiki",
		"SITE_DESCRIPTION": "", "LOGO_URL": "", "UPLOAD_MAX_MB": "5",
	}
	for k, v := range over {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func TestFromEnvReadsEverything(t *testing.T) {
	c, err := FromEnv(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.SyncedGroups, ",") != "admins,members" || c.UploadMaxBytes != 5*1024*1024 || c.HealthAddr != ":8090" {
		t.Errorf("%+v", c)
	}
}

func TestMissingRequiredIsNamed(t *testing.T) {
	_, err := FromEnv(env(map[string]string{"ADMIN_PASSWORD": ""}))
	if err == nil || !strings.Contains(err.Error(), "ADMIN_PASSWORD") {
		t.Errorf("err = %v", err)
	}
}

func TestSyncedGroupsMustHoldTheAdminGroup(t *testing.T) {
	_, err := FromEnv(env(map[string]string{"SYNCED_GROUPS": "members"}))
	if err == nil || !strings.Contains(err.Error(), "admins") {
		t.Errorf("err = %v", err)
	}
}

func TestNoVerifyBecomesRequireForPgx(t *testing.T) {
	c, _ := FromEnv(env(nil))
	if got := c.PgxDSN(); got != "postgresql://u:p@db:5432/wiki?sslmode=require" {
		t.Errorf("PgxDSN = %s", got)
	}
	c, _ = FromEnv(env(map[string]string{"DATABASE_URL": "postgresql://u:p@postgres:5432/wiki"}))
	if got := c.PgxDSN(); got != "postgresql://u:p@postgres:5432/wiki?sslmode=disable" {
		t.Errorf("pinned PgxDSN = %s", got)
	}
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./internal/config/`
Expected: FAIL to compile.

- [ ] **Step 4: Implement**

`internal/config/config.go`:

```go
// Package config reads the bootstrap's configuration from its environment,
// which the wikijs blueprint renders into bootstrap.env.
package config

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

type Config struct {
	WikiURL, DatabaseURL, PublicURL                string
	AdminEmail, AdminPassword                      string
	ClientID, ClientSecret                         string
	AuthorizeURL, TokenURL, UserinfoURL, LogoutURL string
	AdminGroup                                     string
	SyncedGroups                                   []string
	Title, Description, LogoURL                    string
	UploadMaxBytes                                 int
	HealthAddr                                     string
}

var required = []string{
	"WIKI_URL", "DATABASE_URL", "PUBLIC_URL", "ADMIN_EMAIL", "ADMIN_PASSWORD",
	"OAUTH_CLIENT_ID", "OAUTH_CLIENT_SECRET", "OAUTH_AUTHORIZE_URL", "OAUTH_TOKEN_URL",
	"OAUTH_USERINFO_URL", "OAUTH_LOGOUT_URL", "ADMIN_GROUP", "SYNCED_GROUPS", "SITE_TITLE", "UPLOAD_MAX_MB",
}

// FromEnv reads every variable, refusing a missing one by name.
func FromEnv(get func(string) string) (Config, error) {
	var missing []string
	for _, k := range required {
		if strings.TrimSpace(get(k)) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("not set: %s", strings.Join(missing, ", "))
	}
	mb, err := strconv.Atoi(get("UPLOAD_MAX_MB"))
	if err != nil || mb < 1 {
		return Config{}, fmt.Errorf("UPLOAD_MAX_MB is %q, not a whole number of megabytes", get("UPLOAD_MAX_MB"))
	}
	var groups []string
	for _, g := range strings.Split(get("SYNCED_GROUPS"), ",") {
		if g = strings.TrimSpace(g); g != "" && !slices.Contains(groups, g) {
			groups = append(groups, g)
		}
	}
	c := Config{
		WikiURL: strings.TrimRight(get("WIKI_URL"), "/"), DatabaseURL: get("DATABASE_URL"),
		PublicURL: strings.TrimRight(get("PUBLIC_URL"), "/"),
		AdminEmail: get("ADMIN_EMAIL"), AdminPassword: get("ADMIN_PASSWORD"),
		ClientID: get("OAUTH_CLIENT_ID"), ClientSecret: get("OAUTH_CLIENT_SECRET"),
		AuthorizeURL: get("OAUTH_AUTHORIZE_URL"), TokenURL: get("OAUTH_TOKEN_URL"),
		UserinfoURL: get("OAUTH_USERINFO_URL"), LogoutURL: get("OAUTH_LOGOUT_URL"),
		AdminGroup: get("ADMIN_GROUP"), SyncedGroups: groups,
		Title: get("SITE_TITLE"), Description: get("SITE_DESCRIPTION"), LogoURL: get("LOGO_URL"),
		UploadMaxBytes: mb * 1024 * 1024, HealthAddr: get("HEALTH_ADDR"),
	}
	if c.HealthAddr == "" {
		c.HealthAddr = ":8090"
	}
	// Without the admin group nobody but the break glass admin could
	// administer the wiki, so the blueprint's validation refuses this too.
	if !slices.Contains(c.SyncedGroups, c.AdminGroup) {
		return Config{}, fmt.Errorf("SYNCED_GROUPS (%s) does not include ADMIN_GROUP, %s", strings.Join(c.SyncedGroups, ","), c.AdminGroup)
	}
	return c, nil
}

// PgxDSN is DatabaseURL as pgx takes it. Wiki.js's node-postgres reads
// sslmode=no-verify (encrypt, do not check the certificate), which pgx does
// not know; pgx's require means the same. A DSN without sslmode is a pinned
// wiki's own Postgres, which serves no TLS, so it gets disable rather than
// pgx's default of prefer.
func (c Config) PgxDSN() string {
	u, err := url.Parse(c.DatabaseURL)
	if err != nil {
		return c.DatabaseURL
	}
	q := u.Query()
	switch q.Get("sslmode") {
	case "no-verify":
		q.Set("sslmode", "require")
	case "":
		q.Set("sslmode", "disable")
	}
	u.RawQuery = q.Encode()
	return u.String()
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/config/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/config .gitignore
git commit -m "feat: read the bootstrap's configuration from its environment" -m "Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 8: The Wiki.js client

**Files:**
- Create: `internal/wiki/client.go`, `internal/wiki/client_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:

```go
type Client struct { BaseURL string; HTTP *http.Client; Log func(format string, args ...any) }
func (c *Client) Healthy(ctx context.Context) error                       // GET /healthz is 200
func (c *Client) SetupMode(ctx context.Context) (bool, error)             // POST /graphql: 404 means setup mode
func (c *Client) Finalize(ctx context.Context, in Finalize) error
func (c *Client) Login(ctx context.Context, email, password string) (string, error)
func (c *Client) UpdateStrategies(ctx context.Context, token string, s []Strategy) error
func (c *Client) UpdateSite(ctx context.Context, token string, s Site) error
func (c *Client) Groups(ctx context.Context, token string) ([]GroupSummary, error)
func (c *Client) Group(ctx context.Context, token string, id int) (Group, error)
func (c *Client) CreateGroup(ctx context.Context, token, name string) (int, error)
func (c *Client) UpdateGroup(ctx context.Context, token string, g Group) error

type Finalize struct { AdminEmail, AdminPassword, SiteURL string }
type Strategy struct {
	Key, StrategyKey, DisplayName string
	Order int
	Enabled, SelfRegistration bool
	Config map[string]any // values wrapped as {"v": value} on the wire
}
type Site struct {
	Host, Title, Description, LogoURL string
	AutoLogin, HideLocal, TrustProxy bool
	UploadMaxFileSize int
}
type GroupSummary struct { ID int; Name string }
type PageRule struct { ID string `json:"id"`; Deny bool `json:"deny"`; Match string `json:"match"`; Roles []string `json:"roles"`; Path string `json:"path"`; Locales []string `json:"locales"` }
type Group struct { ID int; Name, RedirectOnLogin string; Permissions []string; PageRules []PageRule }
var ErrLogin = errors.New("login refused")
```

Every mutation method logs one line through `Log`: the mutation, its target, and `ok` or the error. Reads do not log.

- [ ] **Step 1: Write the failing tests against a fake Wiki.js**

`internal/wiki/client_test.go`:

```go
package wiki

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fake answers GraphQL by matching a word in the query, and records each.
type fake struct {
	setup   bool
	queries []string
	answer  map[string]string
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.URL.Path == "/healthz":
		w.WriteHeader(200)
	case f.setup && r.URL.Path == "/finalize":
		f.queries = append(f.queries, "finalize "+string(body))
		io.WriteString(w, `{"ok":true}`)
	case f.setup:
		http.NotFound(w, r)
	case r.URL.Path == "/graphql":
		var q struct{ Query string }
		json.Unmarshal(body, &q)
		f.queries = append(f.queries, q.Query)
		for word, a := range f.answer {
			if strings.Contains(q.Query, word) {
				io.WriteString(w, a)
				return
			}
		}
		io.WriteString(w, `{"data":{}}`)
	}
}

func client(f *fake) (*Client, *[]string) {
	var logged []string
	s := httptest.NewServer(f)
	return &Client{BaseURL: s.URL, HTTP: s.Client(), Log: func(format string, a ...any) {
		logged = append(logged, format)
	}}, &logged
}

func TestSetupModeIsA404OnGraphQL(t *testing.T) {
	c, _ := client(&fake{setup: true})
	if on, err := c.SetupMode(context.Background()); err != nil || !on {
		t.Errorf("setup mode = %v, %v", on, err)
	}
	c, _ = client(&fake{})
	if on, err := c.SetupMode(context.Background()); err != nil || on {
		t.Errorf("normal mode = %v, %v", on, err)
	}
}

func TestLoginReturnsTheJWTOrErrLogin(t *testing.T) {
	f := &fake{answer: map[string]string{"login": `{"data":{"authentication":{"login":{"jwt":"tok","responseResult":{"succeeded":true}}}}}`}}
	c, _ := client(f)
	if tok, err := c.Login(context.Background(), "a@example.org", "pw"); err != nil || tok != "tok" {
		t.Errorf("login = %q, %v", tok, err)
	}
	f.answer["login"] = `{"data":{"authentication":{"login":{"jwt":null,"responseResult":{"succeeded":false,"message":"Invalid email / username or password."}}}}}`
	if _, err := c.Login(context.Background(), "a@example.org", "pw"); !errors.Is(err, ErrLogin) {
		t.Errorf("err = %v", err)
	}
}

func TestStrategyConfigIsWrappedForWikiJS(t *testing.T) {
	f := &fake{answer: map[string]string{"updateStrategies": `{"data":{"authentication":{"updateStrategies":{"responseResult":{"succeeded":true}}}}}`}}
	c, logged := client(f)
	err := c.UpdateStrategies(context.Background(), "tok", []Strategy{{Key: "pocketid", StrategyKey: "oauth2", DisplayName: "Pocket ID", Config: map[string]any{"mapGroups": true}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.queries[0], `{\"v\":true}`) {
		t.Errorf("config not wrapped: %s", f.queries[0])
	}
	if len(*logged) != 1 {
		t.Errorf("logged %d lines for one write", len(*logged))
	}
}

func TestAFailedMutationIsAnError(t *testing.T) {
	f := &fake{answer: map[string]string{"updateConfig": `{"data":{"site":{"updateConfig":{"responseResult":{"succeeded":false,"message":"nope"}}}}}`}}
	c, _ := client(f)
	if err := c.UpdateSite(context.Background(), "tok", Site{Host: "https://wiki.example.org"}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/wiki/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement**

`internal/wiki/client.go`. Use GraphQL variables, never string interpolation of values. The exact documents:

```go
// Package wiki talks to one Wiki.js 2.5 instance: its setup endpoint and its
// GraphQL API, as the bootstrap needs them and no further.
package wiki

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

var ErrLogin = errors.New("login refused")

type Client struct {
	BaseURL string
	HTTP    *http.Client
	Log     func(format string, args ...any)
}

type Finalize struct{ AdminEmail, AdminPassword, SiteURL string }

type Strategy struct {
	Key, StrategyKey, DisplayName string
	Order                         int
	Enabled, SelfRegistration     bool
	Config                        map[string]any
}

type Site struct {
	Host, Title, Description, LogoURL string
	AutoLogin, HideLocal, TrustProxy  bool
	UploadMaxFileSize                 int
}

type GroupSummary struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type PageRule struct {
	ID      string   `json:"id"`
	Deny    bool     `json:"deny"`
	Match   string   `json:"match"`
	Roles   []string `json:"roles"`
	Path    string   `json:"path"`
	Locales []string `json:"locales"`
}

type Group struct {
	ID              int        `json:"id"`
	Name            string     `json:"name"`
	RedirectOnLogin string     `json:"redirectOnLogin"`
	Permissions     []string   `json:"permissions"`
	PageRules       []PageRule `json:"pageRules"`
}

const (
	qLogin = `mutation($u:String!,$p:String!){authentication{login(username:$u,password:$p,strategy:"local"){jwt responseResult{succeeded message}}}}`
	qStrategies = `mutation($s:[AuthenticationStrategyInput]!){authentication{updateStrategies(strategies:$s){responseResult{succeeded message}}}}`
	qSite = `mutation($host:String,$title:String,$description:String,$logoUrl:String,$autoLogin:Boolean,$hideLocal:Boolean,$trustProxy:Boolean,$upload:Int){site{updateConfig(host:$host,title:$title,description:$description,logoUrl:$logoUrl,authAutoLogin:$autoLogin,authHideLocal:$hideLocal,securityTrustProxy:$trustProxy,uploadMaxFileSize:$upload){responseResult{succeeded message}}}}`
	qGroups = `{groups{list{id name}}}`
	qGroup = `query($id:Int!){groups{single(id:$id){id name redirectOnLogin permissions pageRules{id deny match roles path locales}}}}`
	qCreateGroup = `mutation($n:String!){groups{create(name:$n){responseResult{succeeded message} group{id}}}}`
	qUpdateGroup = `mutation($id:Int!,$n:String!,$r:String!,$perm:[String]!,$rules:[PageRuleInput]!){groups{update(id:$id,name:$n,redirectOnLogin:$r,permissions:$perm,pageRules:$rules){responseResult{succeeded message}}}}`
)

type result struct {
	Succeeded bool   `json:"succeeded"`
	Message   string `json:"message"`
}

func (c *Client) Healthy(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/healthz", nil)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET /healthz: %s", resp.Status)
	}
	return nil
}

// SetupMode is whether the instance serves its setup wizard. In setup mode
// Wiki.js answers GET * and POST /finalize and 404s everything else
// (server/setup.js:68,76,382 at v2.5.316), so a POST to /graphql tells them
// apart without changing anything.
func (c *Client) SetupMode(ctx context.Context) (bool, error) {
	status, _, err := c.post(ctx, "/graphql", "", map[string]any{"query": "{__typename}"})
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusNotFound:
		return true, nil
	case http.StatusOK:
		return false, nil
	}
	return false, fmt.Errorf("POST /graphql answered %d", status)
}

// Finalize runs Wiki.js's setup. It deletes every user and group before
// seeding (server/setup.js:204-223), so the caller holds the advisory lock
// and has seen an empty settings table.
func (c *Client) Finalize(ctx context.Context, in Finalize) error {
	status, body, err := c.post(ctx, "/finalize", "", map[string]any{"adminEmail": in.AdminEmail, "adminPassword": in.AdminPassword, "siteUrl": in.SiteURL, "telemetry": false})
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("answered %d", status)
	}
	if err == nil {
		var r struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &r) != nil || !r.OK {
			err = fmt.Errorf("refused: %s", r.Error)
		}
	}
	c.logWrite("finalize", in.SiteURL, err)
	return err
}

func (c *Client) Login(ctx context.Context, email, password string) (string, error) {
	var out struct {
		Authentication struct {
			Login struct {
				JWT            *string `json:"jwt"`
				ResponseResult result  `json:"responseResult"`
			} `json:"login"`
		} `json:"authentication"`
	}
	if err := c.gql(ctx, "", qLogin, map[string]any{"u": email, "p": password}, &out); err != nil {
		return "", err
	}
	l := out.Authentication.Login
	if !l.ResponseResult.Succeeded || l.JWT == nil {
		return "", fmt.Errorf("%w for %s: %s", ErrLogin, email, l.ResponseResult.Message)
	}
	return *l.JWT, nil
}

func (c *Client) UpdateStrategies(ctx context.Context, token string, s []Strategy) error {
	in := make([]map[string]any, 0, len(s))
	for _, st := range s {
		cfg := make([]map[string]string, 0, len(st.Config))
		for k, v := range st.Config {
			wrapped, _ := json.Marshal(map[string]any{"v": v})
			cfg = append(cfg, map[string]string{"key": k, "value": string(wrapped)})
		}
		in = append(in, map[string]any{
			"key": st.Key, "strategyKey": st.StrategyKey, "displayName": st.DisplayName,
			"order": st.Order, "isEnabled": st.Enabled, "selfRegistration": st.SelfRegistration,
			"domainWhitelist": []string{}, "autoEnrollGroups": []int{}, "config": cfg,
		})
	}
	var out struct {
		Authentication struct {
			UpdateStrategies struct {
				ResponseResult result `json:"responseResult"`
			} `json:"updateStrategies"`
		} `json:"authentication"`
	}
	err := c.gql(ctx, token, qStrategies, map[string]any{"s": in}, &out)
	err = check(err, out.Authentication.UpdateStrategies.ResponseResult)
	c.logWrite("authentication.updateStrategies", fmt.Sprintf("%d strategies", len(s)), err)
	return err
}

func (c *Client) UpdateSite(ctx context.Context, token string, s Site) error {
	var out struct {
		Site struct {
			UpdateConfig struct {
				ResponseResult result `json:"responseResult"`
			} `json:"updateConfig"`
		} `json:"site"`
	}
	err := c.gql(ctx, token, qSite, map[string]any{
		"host": s.Host, "title": s.Title, "description": s.Description, "logoUrl": s.LogoURL,
		"autoLogin": s.AutoLogin, "hideLocal": s.HideLocal, "trustProxy": s.TrustProxy, "upload": s.UploadMaxFileSize,
	}, &out)
	err = check(err, out.Site.UpdateConfig.ResponseResult)
	c.logWrite("site.updateConfig", s.Host, err)
	return err
}

func (c *Client) Groups(ctx context.Context, token string) ([]GroupSummary, error) {
	var out struct {
		Groups struct {
			List []GroupSummary `json:"list"`
		} `json:"groups"`
	}
	err := c.gql(ctx, token, qGroups, nil, &out)
	return out.Groups.List, err
}

func (c *Client) Group(ctx context.Context, token string, id int) (Group, error) {
	var out struct {
		Groups struct {
			Single Group `json:"single"`
		} `json:"groups"`
	}
	err := c.gql(ctx, token, qGroup, map[string]any{"id": id}, &out)
	return out.Groups.Single, err
}

func (c *Client) CreateGroup(ctx context.Context, token, name string) (int, error) {
	var out struct {
		Groups struct {
			Create struct {
				ResponseResult result `json:"responseResult"`
				Group          struct {
					ID int `json:"id"`
				} `json:"group"`
			} `json:"create"`
		} `json:"groups"`
	}
	err := c.gql(ctx, token, qCreateGroup, map[string]any{"n": name}, &out)
	err = check(err, out.Groups.Create.ResponseResult)
	c.logWrite("groups.create", name, err)
	return out.Groups.Create.Group.ID, err
}

func (c *Client) UpdateGroup(ctx context.Context, token string, g Group) error {
	rules := g.PageRules
	if rules == nil {
		rules = []PageRule{}
	}
	perms := g.Permissions
	if perms == nil {
		perms = []string{}
	}
	var out struct {
		Groups struct {
			Update struct {
				ResponseResult result `json:"responseResult"`
			} `json:"update"`
		} `json:"groups"`
	}
	err := c.gql(ctx, token, qUpdateGroup, map[string]any{"id": g.ID, "n": g.Name, "r": g.RedirectOnLogin, "perm": perms, "rules": rules}, &out)
	err = check(err, out.Groups.Update.ResponseResult)
	c.logWrite("groups.update", fmt.Sprintf("%d %s", g.ID, g.Name), err)
	return err
}

func check(err error, r result) error {
	if err != nil {
		return err
	}
	if !r.Succeeded {
		return fmt.Errorf("refused: %s", r.Message)
	}
	return nil
}

func (c *Client) logWrite(what, target string, err error) {
	if c.Log == nil {
		return
	}
	if err != nil {
		c.Log("write %s %s: %v", what, target, err)
		return
	}
	c.Log("write %s %s: ok", what, target)
}

func (c *Client) gql(ctx context.Context, token, query string, vars map[string]any, out any) error {
	status, body, err := c.post(ctx, "/graphql", token, map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("POST /graphql answered %d", status)
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("POST /graphql: %w", err)
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("graphql: %s", env.Errors[0].Message)
	}
	return json.Unmarshal(env.Data, out)
}

func (c *Client) post(ctx context.Context, path, token string, payload any) (int, []byte, error) {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/wiki/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/wiki
git commit -m "feat: a Wiki.js client for setup, login, strategies, site and groups" -m "Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 9: Database state and the advisory lock

**Files:**
- Create: `internal/state/state.go`, `internal/state/state_test.go` (build tag `docker`)

**Interfaces:**
- Produces:

```go
type DB struct{ DSN string }
func (d DB) Lock(ctx context.Context) (unlock func(), err error)
func (d DB) SettingsPopulated(ctx context.Context) (bool, error)
const LockKey int64 = 0x57494b49424f4f54 // "WIKIBOOT"
```

- [ ] **Step 1: Write the failing test (needs Docker)**

`internal/state/state_test.go`:

```go
//go:build docker

package state

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A throwaway Postgres on a random port, removed after the test.
func postgres(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("docker", "run", "-d", "--rm", "-e", "POSTGRES_PASSWORD=test-only", "-p", "127.0.0.1::5432", "postgres:18").Output()
	if err != nil {
		t.Skip("docker unavailable")
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })
	port, _ := exec.Command("docker", "port", id, "5432").Output()
	dsn := "postgresql://postgres:test-only@" + strings.TrimSpace(string(port)) + "/postgres?sslmode=disable"
	for i := 0; i < 60; i++ {
		if _, err := (DB{DSN: dsn}).SettingsPopulated(context.Background()); err == nil {
			return dsn
		}
		time.Sleep(time.Second)
	}
	t.Fatal("postgres did not start")
	return ""
}

func TestNoSettingsTableIsNotPopulated(t *testing.T) {
	d := DB{DSN: postgres(t)}
	if got, err := d.SettingsPopulated(context.Background()); err != nil || got {
		t.Errorf("populated = %v, %v", got, err)
	}
}

func TestTheLockExcludesASecondHolder(t *testing.T) {
	d := DB{DSN: postgres(t)}
	unlock, err := d.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := d.Lock(ctx); err == nil {
		t.Fatal("a second holder took the lock")
	}
	unlock()
	if u2, err := d.Lock(context.Background()); err != nil {
		t.Fatal(err)
	} else {
		u2()
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test -tags docker ./internal/state/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement**

`internal/state/state.go`:

```go
// Package state reads what the bootstrap needs from Wiki.js's database and
// holds the lock that lets one instance's bootstrap at a time set it up. It
// never writes to the database.
package state

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// LockKey is the advisory lock every bootstrap against one database takes.
const LockKey int64 = 0x57494b49424f4f54

type DB struct{ DSN string }

// Lock blocks until this session holds the advisory lock, or ctx ends. The
// lock is a session lock on its own connection, released by unlock or by
// the connection closing.
func (d DB) Lock(ctx context.Context) (func(), error) {
	conn, err := pgx.Connect(ctx, d.DSN)
	if err != nil {
		return nil, fmt.Errorf("connecting for the lock: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", LockKey); err != nil {
		conn.Close(context.Background())
		return nil, fmt.Errorf("taking the lock: %w", err)
	}
	return func() {
		conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", LockKey)
		conn.Close(context.Background())
	}, nil
}

// SettingsPopulated is whether Wiki.js's settings table exists and has a
// row: whether setup has run against this database.
func (d DB) SettingsPopulated(ctx context.Context) (bool, error) {
	conn, err := pgx.Connect(ctx, d.DSN)
	if err != nil {
		return false, err
	}
	defer conn.Close(context.Background())
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass('settings') IS NOT NULL").Scan(&exists); err != nil || !exists {
		return false, err
	}
	var any bool
	err = conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM settings)").Scan(&any)
	return any, err
}
```

- [ ] **Step 4: Run the test**

Run: `go test -tags docker ./internal/state/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/state go.mod go.sum
git commit -m "feat: read whether setup has run, and hold the setup lock" -m "Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 10: Stopping the app's process

**Files:**
- Create: `internal/procs/procs.go`, `internal/procs/procs_test.go`

**Interfaces:**
- Produces: `type Stopper struct{ ProcRoot string; Self int; Signal func(pid int) error }`; `func (s Stopper) StopApp() error` finds the one process whose cmdline is `node ... server` and signals it; `func New() Stopper` uses `/proc`, `os.Getpid()` and `syscall.Kill(pid, syscall.SIGTERM)`.

- [ ] **Step 1: Write the failing test with a fake /proc**

```go
package procs

import (
	"os"
	"path/filepath"
	"testing"
)

func fakeProc(t *testing.T, procs map[string]string) string {
	root := t.TempDir()
	for pid, cmd := range procs {
		os.MkdirAll(filepath.Join(root, pid), 0o755)
		os.WriteFile(filepath.Join(root, pid, "cmdline"), []byte(cmd), 0o644)
	}
	return root
}

func TestStopsTheWikiJSProcessOnly(t *testing.T) {
	root := fakeProc(t, map[string]string{
		"1":  "node\x00--no-deprecation\x00server\x00",
		"7":  "/wikijs-bootstrap\x00",
		"12": "sh\x00-c\x00sleep 1\x00",
	})
	var signalled []int
	s := Stopper{ProcRoot: root, Self: 7, Signal: func(pid int) error { signalled = append(signalled, pid); return nil }}
	if err := s.StopApp(); err != nil {
		t.Fatal(err)
	}
	if len(signalled) != 1 || signalled[0] != 1 {
		t.Errorf("signalled %v", signalled)
	}
}

func TestNoWikiJSProcessIsAnError(t *testing.T) {
	s := Stopper{ProcRoot: fakeProc(t, map[string]string{"7": "/wikijs-bootstrap\x00"}), Self: 7, Signal: func(int) error { return nil }}
	if s.StopApp() == nil {
		t.Error("stopped nothing without saying so")
	}
}
```

- [ ] **Step 2: Run to see it fail**

Run: `go test ./internal/procs/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement**

```go
// Package procs stops the Wiki.js process the bootstrap shares a process
// namespace with (compose: pid: "service:app"). Wiki.js shuts down on
// SIGTERM (server/index.js:46 at v2.5.316) and its restart policy brings it
// back, reading the database afresh.
package procs

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

type Stopper struct {
	ProcRoot string
	Self     int
	Signal   func(pid int) error
}

func New() Stopper {
	return Stopper{ProcRoot: "/proc", Self: os.Getpid(), Signal: func(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }}
}

// StopApp signals the one process running `node ... server`, the image's
// command (dev/build/Dockerfile:56).
func (s Stopper) StopApp() error {
	entries, err := os.ReadDir(s.ProcRoot)
	if err != nil {
		return err
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == s.Self {
			continue
		}
		cmd, err := os.ReadFile(filepath.Join(s.ProcRoot, e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := bytes.Split(bytes.TrimRight(cmd, "\x00"), []byte{0})
		if len(args) >= 2 && filepath.Base(string(args[0])) == "node" && string(args[len(args)-1]) == "server" {
			return s.Signal(pid)
		}
	}
	return fmt.Errorf("no Wiki.js process in %s: is pid: service:app set on the bootstrap?", s.ProcRoot)
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/procs/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/procs
git commit -m "feat: stop a stale Wiki.js instance through the shared process namespace" -m "Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 11: Converge

**Files:**
- Create: `internal/converge/converge.go`, `internal/converge/converge_test.go`

**Interfaces:**
- Consumes: `config.Config` (Task 7); `wiki.*` types (Task 8); `state.DB` shape (Task 9); `procs.Stopper` shape (Task 10), each through the interfaces below.
- Produces:

```go
type Wiki interface {
	Healthy(ctx context.Context) error
	SetupMode(ctx context.Context) (bool, error)
	Finalize(ctx context.Context, in wiki.Finalize) error
	Login(ctx context.Context, email, password string) (string, error)
	UpdateStrategies(ctx context.Context, token string, s []wiki.Strategy) error
	UpdateSite(ctx context.Context, token string, s wiki.Site) error
	Groups(ctx context.Context, token string) ([]wiki.GroupSummary, error)
	Group(ctx context.Context, token string, id int) (wiki.Group, error)
	CreateGroup(ctx context.Context, token, name string) (int, error)
	UpdateGroup(ctx context.Context, token string, g wiki.Group) error
}
type State interface {
	Lock(ctx context.Context) (func(), error)
	SettingsPopulated(ctx context.Context) (bool, error)
}
type Stopper interface{ StopApp() error }
var ErrStopped = errors.New("stopped the stale instance; it restarts and so does this container")
func Run(ctx context.Context, c config.Config, w Wiki, s State, p Stopper, wait time.Duration) error
var AdminPermissions []string // every Wiki.js permission except manage:system and manage:api
```

- [ ] **Step 1: Write the failing tests**

`internal/converge/converge_test.go`:

```go
package converge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/wikijs-bootstrap/internal/config"
	"github.com/paisans-software/wikijs-bootstrap/internal/wiki"
)

type fakeWiki struct {
	setup      bool
	loginErr   error
	groups     map[int]wiki.Group
	next       int
	writes     []string
	strategies []wiki.Strategy
	site       wiki.Site
	onFinalize func()
}

func newWiki(setup bool) *fakeWiki {
	return &fakeWiki{setup: setup, next: 3, groups: map[int]wiki.Group{
		1: {ID: 1, Name: "Administrators", Permissions: []string{"manage:system"}},
		2: {ID: 2, Name: "Guests", RedirectOnLogin: "/", Permissions: []string{"read:pages"}, PageRules: []wiki.PageRule{{ID: "g", Match: "START", Roles: []string{"read:pages"}}}},
	}}
}

func (f *fakeWiki) Healthy(context.Context) error             { return nil }
func (f *fakeWiki) SetupMode(context.Context) (bool, error)   { return f.setup, nil }
func (f *fakeWiki) Finalize(context.Context, wiki.Finalize) error {
	f.writes = append(f.writes, "finalize")
	f.setup = false
	if f.onFinalize != nil {
		f.onFinalize()
	}
	return nil
}
func (f *fakeWiki) Login(context.Context, string, string) (string, error) {
	return "tok", f.loginErr
}
func (f *fakeWiki) UpdateStrategies(_ context.Context, _ string, s []wiki.Strategy) error {
	f.writes = append(f.writes, "updateStrategies")
	f.strategies = s
	return nil
}
func (f *fakeWiki) UpdateSite(_ context.Context, _ string, s wiki.Site) error {
	f.writes = append(f.writes, "updateConfig")
	f.site = s
	return nil
}
func (f *fakeWiki) Groups(context.Context, string) ([]wiki.GroupSummary, error) {
	var out []wiki.GroupSummary
	for _, g := range f.groups {
		out = append(out, wiki.GroupSummary{ID: g.ID, Name: g.Name})
	}
	return out, nil
}
func (f *fakeWiki) Group(_ context.Context, _ string, id int) (wiki.Group, error) {
	return f.groups[id], nil
}
func (f *fakeWiki) CreateGroup(_ context.Context, _ string, name string) (int, error) {
	f.writes = append(f.writes, "create "+name)
	id := f.next
	f.next++
	f.groups[id] = wiki.Group{ID: id, Name: name, RedirectOnLogin: "/", Permissions: []string{"read:pages"}}
	return id, nil
}
func (f *fakeWiki) UpdateGroup(_ context.Context, _ string, g wiki.Group) error {
	f.writes = append(f.writes, fmt.Sprintf("update %d", g.ID))
	f.groups[g.ID] = g
	return nil
}

type fakeState struct{ populated bool }

func (s *fakeState) Lock(context.Context) (func(), error)          { return func() {}, nil }
func (s *fakeState) SettingsPopulated(context.Context) (bool, error) { return s.populated, nil }

type fakeStopper struct{ stopped int }

func (p *fakeStopper) StopApp() error { p.stopped++; return nil }

func cfg() config.Config {
	return config.Config{WikiURL: "http://app:3000", PublicURL: "https://wiki.example.org", AdminEmail: "wikijs-admin@example.org", AdminPassword: "not-real", ClientID: "cid", ClientSecret: "not-real", AuthorizeURL: "https://id.example.org/authorize", TokenURL: "https://id.example.org/api/oidc/token", UserinfoURL: "https://id.example.org/api/oidc/userinfo", LogoutURL: "https://id.example.org/api/oidc/end-session", AdminGroup: "admins", SyncedGroups: []string{"admins", "members"}, Title: "Example Wiki", UploadMaxBytes: 5 << 20}
}

func TestAFreshDatabaseIsSetUpThenConverged(t *testing.T) {
	w, s := newWiki(true), &fakeState{}
	w.onFinalize = func() { s.populated = true }
	if err := Run(context.Background(), cfg(), w, s, &fakeStopper{}, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if w.writes[0] != "finalize" {
		t.Errorf("writes %v", w.writes)
	}
	if w.strategies[0].Key != "pocketid" || w.strategies[0].Order != 0 || w.strategies[1].Key != "local" {
		t.Errorf("strategies %+v", w.strategies)
	}
	if w.strategies[0].Config["scope"] != "openid profile email groups" || w.strategies[0].Config["mapGroups"] != true {
		t.Errorf("pocketid config %+v", w.strategies[0].Config)
	}
	if len(w.groups[2].Permissions) != 0 || len(w.groups[2].PageRules) != 0 {
		t.Errorf("guests %+v", w.groups[2])
	}
	if !w.site.AutoLogin || !w.site.HideLocal || !w.site.TrustProxy || w.site.Host != "https://wiki.example.org" {
		t.Errorf("site %+v", w.site)
	}
}

func TestSecondInstanceStopsItsStaleAppInsteadOfFinalizing(t *testing.T) {
	w, s, p := newWiki(true), &fakeState{populated: true}, &fakeStopper{}
	err := Run(context.Background(), cfg(), w, s, p, time.Millisecond)
	if !errors.Is(err, ErrStopped) || p.stopped != 1 {
		t.Fatalf("err %v, stopped %d", err, p.stopped)
	}
	if slices.Contains(w.writes, "finalize") {
		t.Error("finalized a database that had settings")
	}
}

func TestAnEmptiedDatabaseUnderARunningWikiIsRefused(t *testing.T) {
	w, s := newWiki(false), &fakeState{populated: false}
	if err := Run(context.Background(), cfg(), w, s, &fakeStopper{}, time.Millisecond); err == nil {
		t.Fatal("went on against a database with no settings and a wiki not in setup mode")
	}
	if len(w.writes) != 0 {
		t.Errorf("wrote %v", w.writes)
	}
}

func TestSyncedGroupsAreCreatedOnceAndAdminsGetPermissions(t *testing.T) {
	w, s := newWiki(false), &fakeState{populated: true}
	for i := 0; i < 2; i++ {
		if err := Run(context.Background(), cfg(), w, s, &fakeStopper{}, time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	creates := 0
	for _, x := range w.writes {
		if strings.HasPrefix(x, "create ") {
			creates++
		}
	}
	if creates != 2 {
		t.Errorf("created %d groups over two runs: %v", creates, w.writes)
	}
	var admins wiki.Group
	for _, g := range w.groups {
		if g.Name == "admins" {
			admins = g
		}
	}
	if slices.Contains(admins.Permissions, "manage:system") || slices.Contains(admins.Permissions, "manage:api") || !slices.Contains(admins.Permissions, "manage:groups") {
		t.Errorf("admins permissions %v", admins.Permissions)
	}
}

func TestAdminGroupKeepsItsPageRules(t *testing.T) {
	w, s := newWiki(false), &fakeState{populated: true}
	rule := wiki.PageRule{ID: "a", Match: "START", Path: "admin", Roles: []string{"read:pages"}}
	w.groups[3] = wiki.Group{ID: 3, Name: "admins", RedirectOnLogin: "/", PageRules: []wiki.PageRule{rule}}
	w.next = 4
	if err := Run(context.Background(), cfg(), w, s, &fakeStopper{}, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if len(w.groups[3].PageRules) != 1 || w.groups[3].PageRules[0].Path != "admin" {
		t.Errorf("page rules %+v", w.groups[3].PageRules)
	}
}

func TestOtherGroupsAreNeverEdited(t *testing.T) {
	w, s := newWiki(false), &fakeState{populated: true}
	w.groups[3] = wiki.Group{ID: 3, Name: "members", Permissions: []string{"read:pages", "write:pages"}}
	w.next = 4
	if err := Run(context.Background(), cfg(), w, s, &fakeStopper{}, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(w.writes, "update 3") {
		t.Errorf("edited members: %v", w.writes)
	}
}

func TestLoginFailureIsReportedNotRetriedSilently(t *testing.T) {
	w, s := newWiki(false), &fakeState{populated: true}
	w.loginErr = fmt.Errorf("%w: bad password", wiki.ErrLogin)
	err := Run(context.Background(), cfg(), w, s, &fakeStopper{}, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "wikijs-admin@example.org") || !strings.Contains(err.Error(), "break glass") {
		t.Errorf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/converge/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement**

`internal/converge/converge.go`:

```go
// Package converge is one bootstrap run: set Wiki.js up if this database
// never was, then bring its sign-in, site settings and groups to what the
// configuration says. Its writes are exactly those the spec names.
package converge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/paisans-software/wikijs-bootstrap/internal/config"
	"github.com/paisans-software/wikijs-bootstrap/internal/wiki"
)

type Wiki interface {
	Healthy(ctx context.Context) error
	SetupMode(ctx context.Context) (bool, error)
	Finalize(ctx context.Context, in wiki.Finalize) error
	Login(ctx context.Context, email, password string) (string, error)
	UpdateStrategies(ctx context.Context, token string, s []wiki.Strategy) error
	UpdateSite(ctx context.Context, token string, s wiki.Site) error
	Groups(ctx context.Context, token string) ([]wiki.GroupSummary, error)
	Group(ctx context.Context, token string, id int) (wiki.Group, error)
	CreateGroup(ctx context.Context, token, name string) (int, error)
	UpdateGroup(ctx context.Context, token string, g wiki.Group) error
}

type State interface {
	Lock(ctx context.Context) (func(), error)
	SettingsPopulated(ctx context.Context) (bool, error)
}

type Stopper interface{ StopApp() error }

var ErrStopped = errors.New("stopped the stale instance; it restarts and so does this container")

// guestsID is Wiki.js's built in Guests group (server/setup.js:246).
const guestsID = 2

// AdminPermissions is every Wiki.js 2.5 permission except manage:system and
// manage:api. manage:api lets its holder make a full access API key, which
// acts as Administrators (server/models/apiKeys.js), so it is manage:system
// by another route.
var AdminPermissions = []string{
	"read:pages", "write:pages", "manage:pages", "delete:pages",
	"write:styles", "write:scripts", "read:source", "read:history",
	"read:assets", "write:assets", "manage:assets",
	"read:comments", "write:comments", "manage:comments",
	"write:users", "manage:users", "write:groups", "manage:groups",
	"manage:navigation", "manage:theme",
}

func Run(ctx context.Context, c config.Config, w Wiki, s State, p Stopper, wait time.Duration) error {
	if err := until(ctx, wait, func() error { return w.Healthy(ctx) }); err != nil {
		return fmt.Errorf("Wiki.js never answered /healthz: %w", err)
	}
	unlock, err := s.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	populated, err := s.SettingsPopulated(ctx)
	if err != nil {
		return fmt.Errorf("reading the settings table: %w", err)
	}
	setup, err := w.SetupMode(ctx)
	if err != nil {
		return err
	}
	switch {
	case !populated && setup:
		if err := w.Finalize(ctx, wiki.Finalize{AdminEmail: c.AdminEmail, AdminPassword: c.AdminPassword, SiteURL: c.PublicURL}); err != nil {
			return fmt.Errorf("setup: %w", err)
		}
		if err := until(ctx, wait, func() error {
			if on, err := w.SetupMode(ctx); err != nil || on {
				return fmt.Errorf("still in setup mode (%v)", err)
			}
			return nil
		}); err != nil {
			return err
		}
	case populated && setup:
		if err := p.StopApp(); err != nil {
			return err
		}
		return ErrStopped
	case !populated && !setup:
		return errors.New("the settings table is empty but Wiki.js is not in setup mode: the database was emptied under a running wiki. Nothing was changed")
	}

	token, err := w.Login(ctx, c.AdminEmail, c.AdminPassword)
	if err != nil {
		return fmt.Errorf("the break glass admin %s could not log in, so nothing can be configured: %w", c.AdminEmail, err)
	}
	if err := w.UpdateStrategies(ctx, token, strategies(c)); err != nil {
		return err
	}
	if err := w.UpdateSite(ctx, token, wiki.Site{
		Host: c.PublicURL, Title: c.Title, Description: c.Description, LogoURL: c.LogoURL,
		AutoLogin: true, HideLocal: true, TrustProxy: true, UploadMaxFileSize: c.UploadMaxBytes,
	}); err != nil {
		return err
	}
	guests, err := w.Group(ctx, token, guestsID)
	if err != nil {
		return err
	}
	guests.Permissions, guests.PageRules = []string{}, []wiki.PageRule{}
	if err := w.UpdateGroup(ctx, token, guests); err != nil {
		return err
	}
	return groups(ctx, c, w, token)
}

func strategies(c config.Config) []wiki.Strategy {
	return []wiki.Strategy{
		{
			Key: "pocketid", StrategyKey: "oauth2", DisplayName: "Pocket ID", Order: 0, Enabled: true, SelfRegistration: true,
			Config: map[string]any{
				"clientId": c.ClientID, "clientSecret": c.ClientSecret,
				"authorizationURL": c.AuthorizeURL, "tokenURL": c.TokenURL, "userInfoURL": c.UserinfoURL,
				"userIdClaim": "sub", "displayNameClaim": "name", "emailClaim": "email", "pictureClaim": "picture",
				"mapGroups": true, "groupsClaim": "groups", "logoutURL": c.LogoutURL,
				"scope": "openid profile email groups", "useQueryStringForAccessToken": false, "enableCSRFProtection": true,
			},
		},
		{Key: "local", StrategyKey: "local", DisplayName: "Local", Order: 1, Enabled: true, Config: map[string]any{}},
	}
}

// groups creates each synced group Wiki.js lacks and sets the admin group's
// permissions, keeping its page rules. No other group is edited.
func groups(ctx context.Context, c config.Config, w Wiki, token string) error {
	list, err := w.Groups(ctx, token)
	if err != nil {
		return err
	}
	ids := map[string]int{}
	for _, g := range list {
		ids[g.Name] = g.ID
	}
	for _, name := range c.SyncedGroups {
		if _, ok := ids[name]; ok {
			continue
		}
		id, err := w.CreateGroup(ctx, token, name)
		if err != nil {
			return err
		}
		ids[name] = id
	}
	admin, err := w.Group(ctx, token, ids[c.AdminGroup])
	if err != nil {
		return err
	}
	admin.Permissions = AdminPermissions
	return w.UpdateGroup(ctx, token, admin)
}

// until retries f every wait for two minutes' worth of tries, or until ctx ends.
func until(ctx context.Context, wait time.Duration, f func() error) error {
	var err error
	for i := 0; i < 60; i++ {
		if err = f(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return err
}
```

Callers pass `wait = 2*time.Second` in production (two minutes).

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/converge/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/converge
git commit -m "feat: one bootstrap run: set up once, then converge sign-in, site and groups" -m "Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 12: Main, health, the write allowlist test, image and workflow

**Files:**
- Create: `cmd/wikijs-bootstrap/main.go`, `cmd/wikijs-bootstrap/main_test.go`
- Create: `internal/audit/audit_test.go`
- Create: `Dockerfile`, `.github/workflows/image.yml`, `README.md`

**Interfaces:**
- Consumes: Tasks 7 to 11.
- Produces: the binary `/wikijs-bootstrap` with subcommand `healthcheck`; image `ghcr.io/paisans-software/wikijs-bootstrap`; health on `:8090` (`GET /healthz`: 200 `converged`, 503 with the last error or `converging`).

- [ ] **Step 1: Write the failing tests**

`cmd/wikijs-bootstrap/main_test.go`:

```go
package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthSaysConvergingThenConvergedOrTheError(t *testing.T) {
	var s status
	check := func(code int, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if rec.Code != code || !strings.Contains(rec.Body.String(), body) {
			t.Errorf("%d %q, want %d %q", rec.Code, rec.Body.String(), code, body)
		}
	}
	check(503, "converging")
	s.set(errors.New("the break glass admin could not log in"))
	check(503, "break glass")
	s.set(nil)
	check(200, "converged")
}
```

`internal/audit/audit_test.go`:

```go
// Package audit holds the test that keeps the bootstrap's writes to the ones
// its spec names: any other GraphQL mutation, or any SQL that writes, fails it.
package audit

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var allowedMutations = []string{"login", "updateStrategies", "updateConfig", "create", "update"}

func sources(t *testing.T) map[string]string {
	out := map[string]string{}
	filepath.WalkDir("../..", func(path string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			b, _ := os.ReadFile(path)
			out[path] = string(b)
		}
		return nil
	})
	if len(out) == 0 {
		t.Fatal("no sources found")
	}
	return out
}

func TestOnlyTheNamedMutations(t *testing.T) {
	field := regexp.MustCompile(`(?s)mutation\([^)]*\)\{\w+\{(\w+)\(`)
	for path, src := range sources(t) {
		for _, m := range field.FindAllStringSubmatch(src, -1) {
			if !slices.Contains(allowedMutations, m[1]) {
				t.Errorf("%s: mutation %s is not one the spec names", path, m[1])
			}
		}
		if strings.Contains(src, "mutation{") || strings.Contains(src, "mutation {") {
			t.Errorf("%s: a mutation without variables escapes this check", path)
		}
	}
}

func TestNoSQLWrites(t *testing.T) {
	// A string literal that is a writing SQL statement, or a SELECT that
	// calls one in a subquery.
	starts := regexp.MustCompile(`(?i)^\s*(INSERT|UPDATE|DELETE|TRUNCATE|DROP|ALTER|CREATE|MERGE|COPY)\s`)
	inside := regexp.MustCompile(`(?i)\b(INSERT\s+INTO|DELETE\s+FROM|TRUNCATE|DROP\s|ALTER\s)`)
	literal := regexp.MustCompile("`[^`]*`|\"(?:[^\"\\\\]|\\\\.)*\"")
	for path, src := range sources(t) {
		for _, lit := range literal.FindAllString(src, -1) {
			body := strings.Trim(lit, "`\"")
			if starts.MatchString(body) || inside.MatchString(body) {
				t.Errorf("%s: SQL that writes: %s", path, lit)
			}
		}
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./cmd/... ./internal/audit/`
Expected: FAIL (`status` undefined; audit passes or fails depending on sources, so confirm it fails by temporarily adding a `mutation($x:Int!){pages{delete(` string to a scratch file, then remove it).

- [ ] **Step 3: Implement main**

`cmd/wikijs-bootstrap/main.go`:

```go
// The Wiki.js bootstrap: runs once per container start, then stays up and
// reports on /healthz, which its Docker healthcheck reads. See the spec in
// paisans-stack, docs/specs/2026-10-13-wikijs-blueprint.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/paisans-software/wikijs-bootstrap/internal/config"
	"github.com/paisans-software/wikijs-bootstrap/internal/converge"
	"github.com/paisans-software/wikijs-bootstrap/internal/procs"
	"github.com/paisans-software/wikijs-bootstrap/internal/state"
	"github.com/paisans-software/wikijs-bootstrap/internal/wiki"
)

type status struct {
	mu   sync.Mutex
	done bool
	err  error
}

func (s *status) set(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done, s.err = err == nil, err
}

func (s *status) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.done:
		fmt.Fprintln(w, "converged")
	case s.err != nil:
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, s.err)
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, "converging")
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		addr := os.Getenv("HEALTH_ADDR")
		if addr == "" {
			addr = ":8090"
		}
		resp, err := http.Get("http://127.0.0.1" + addr + "/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	c, err := config.FromEnv(os.Getenv)
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	s := &status{}
	go func() { log.Fatal(http.ListenAndServe(c.HealthAddr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.ServeHTTP(w, r) }))) }()

	w := &wiki.Client{BaseURL: c.WikiURL, HTTP: &http.Client{Timeout: 30 * time.Second}, Log: log.Printf}
	db := state.DB{DSN: c.PgxDSN()}
	for {
		err := converge.Run(context.Background(), c, w, db, procs.New(), 2*time.Second)
		s.set(err)
		if err == nil {
			log.Print("converged")
			select {}
		}
		if errors.Is(err, converge.ErrStopped) {
			log.Print(err)
			select {} // the namespace ends with the app; this container restarts with it
		}
		log.Printf("run failed, retrying in a minute: %v", err)
		time.Sleep(time.Minute)
	}
}
```

- [ ] **Step 4: Dockerfile, workflow, README**

`Dockerfile`:

```dockerfile
# The Wiki.js bootstrap's image: one static binary on distroless. It runs as
# uid 1000, the Wiki.js image's node user, so it may signal that process
# through the process namespace it shares with it.
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /wikijs-bootstrap ./cmd/wikijs-bootstrap

FROM gcr.io/distroless/static-debian12
COPY --from=build /wikijs-bootstrap /wikijs-bootstrap
USER 1000:1000
EXPOSE 8090
HEALTHCHECK --interval=10s --timeout=5s --start-period=30s CMD ["/wikijs-bootstrap", "healthcheck"]
ENTRYPOINT ["/wikijs-bootstrap"]
```

`.github/workflows/image.yml`: copy `paisans-stack/.github/workflows/admin-reconciler-image.yml` with these changes: `name: image`; triggers `push: branches: [main], tags: ["v*"]` and `workflow_dispatch`, no `paths` filter; Test step `go test ./...`; `images: ghcr.io/paisans-software/wikijs-bootstrap`; tags `type=semver,pattern={{version}}`, `type=ref,event=branch`, `type=sha`; `file: Dockerfile`.

`README.md`: what it is (one paragraph from the spec's *The bootstrap*), its environment variables (the `required` list from Task 7 plus `HEALTH_ADDR`, `SITE_DESCRIPTION`, `LOGO_URL`), what it writes (the spec's table), and "Releases: a tag `vX.Y.Z` publishes `X.Y.Z`; tagging is a human's action."

- [ ] **Step 5: Run everything**

Run: `go vet ./... && go test ./... && go test -tags docker ./internal/state/ && docker build -t wikijs-bootstrap:local .`
Expected: all PASS; the image builds.

- [ ] **Step 6: Commit, push, open the pull request**

```bash
git add cmd internal/audit Dockerfile .github README.md
git commit -m "feat: the bootstrap binary, its health endpoint, image and release workflow" -m "Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>"
git push -u origin feat/initial
gh pr create --base main --title "feat: the Wiki.js bootstrap" --body "$(cat <<'EOF'
Finishes Wiki.js's setup once per database and converges its sign-in, site settings and groups, for the wikijs blueprint in the core catalog. Its writes are exactly those the spec names, and a test fails on any other.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 7: Hand the release to the human**

Tell the human: after merge, `git tag v0.1.0 && git push origin v0.1.0` publishes `ghcr.io/paisans-software/wikijs-bootstrap:0.1.0`. Then resolve the digest:

```bash
docker buildx imagetools inspect ghcr.io/paisans-software/wikijs-bootstrap:0.1.0 --format '{{json .Manifest.Digest}}'
```

Record the digest for Task 13.

---

## Phase 3: `Paisans-Software/paisans-blueprints`

Work in `src/blueprints`: `git worktree add .worktrees/wikijs -b feat/wikijs origin/main`. Requires Phase 1 merged (the `list` type and `extra_groups_setting`) and Task 12's digest.

### Task 13: The `wikijs` blueprint

**Files:**
- Create: `blueprints/wikijs/blueprint.yaml`
- Create: `blueprints/wikijs/compose.yaml.tmpl`
- Create: `blueprints/wikijs/.env.secret.tmpl`
- Create: `blueprints/wikijs/bootstrap.env.secret.tmpl`
- Create: `blueprints/wikijs/caddy.snippet.tmpl`

**Interfaces:**
- Consumes: `.SettingList`, `.SettingInt` (Task 2); `oidc.extra_groups_setting` (Task 3); bootstrap image and environment (Tasks 7, 12).

- [ ] **Step 1: `blueprint.yaml`**

```yaml
# The wikijs blueprint, in the core catalog.
# paisans-stack reads it; internal/kinds.Parse there documents every key.
# Spec: paisans-stack docs/specs/2026-10-13-wikijs-blueprint.md.
apiVersion: paisans.blueprint/v1
name: wikijs
# The list setting type and oidc.extra_groups_setting.
requires_toolkit: ">= 0.0.0"
services:
  # Re-resolve the digest before committing: docker buildx imagetools inspect.
  - {name: "app", image: "ghcr.io/requarks/wiki:2.5.316@sha256:<digest from imagetools>", purpose: "the application"}
  - {name: "bootstrap", image: "ghcr.io/paisans-software/wikijs-bootstrap:0.1.0@sha256:<digest from Task 12 step 7>", purpose: "finishes setup and keeps sign-in, site settings and groups as configured"}
  - {name: "postgres", purpose: "its own database, when the app is pinned"}
database: {engine: postgres}
port: 3000
config:
  format: env
  file: ".env"
# Answers 200 while its database pool has connections
# (server/controllers/common.js:29 at v2.5.316).
health: {path: "/healthz", status: [200]}
gate:
  gateable: true
  # The OAuth2 start and callback, the app's own sign-in routes
  # (server/controllers/auth.js:52-75, server/core/auth.js:85).
  open_paths:
    - "/login/pocketid"
    - "/login/pocketid/callback"
  auto_login:
    # Wiki.js's session is its jwt cookie (controllers/auth.js:77).
    - {paths: ["/", "/login"], target: "/login/pocketid", absent_cookies: ["jwt"]}
  sign_in_path: "/login"
oidc:
  callback: "/login/pocketid/callback"
  # passport-oauth2 sends no code_challenge
  # (server/modules/authentication/oauth2/authentication.js:13-23).
  pkce: false
  reads_groups: true
  admin_group_setting: "admin_group"
  member_group_setting: "member_group"
  default_admin_group: "admins"
  default_member_group: "members"
  extra_groups_setting: "synced_groups"
settings:
  admin_group: {type: string, default: "admins"}
  member_group: {type: string, default: "members"}
  synced_groups: {type: list, default: ["admins", "members"]}
  site_title: {type: string, default: "Wiki"}
  site_description: {type: string, default: ""}
  logo_url: {type: string, default: ""}
  upload_max_mb: {type: int, default: 5}
secrets:
  generate:
    - {name: local_admin_password, rotation_cost: restart}
checks:
  # Without the admin group in synced_groups nobody but the break glass
  # admin can administer the wiki. Provisional: inferred, not decided.
  - id: wikijs-synced-groups-lack-admin-group
    level: refuse
    hint: synced_groups does not include the admin group
    key: settings.synced_groups
    rule: |-
      (has(app.settings.admin_group) ? app.settings.admin_group : 'admins') in app.settings.synced_groups
    message: "does not include the admin group. Add it, or nobody but the break glass admin can administer the wiki."
  - id: wikijs-upload-limit
    level: refuse
    hint: upload_max_mb is not between 1 and 1024
    key: settings.upload_max_mb
    rule: |-
      app.settings.upload_max_mb >= 1 && app.settings.upload_max_mb <= 1024
    message: "must be a whole number of megabytes from 1 to 1024."
```

Resolve `requires_toolkit`: run `git -C ../../paisans-stack log origin/develop --oneline -1` after Phase 1 merged and check how other blueprints state it (`grep -rn requires_toolkit blueprints/`); if none does, and the toolkit has had no release, ask the human what to write rather than guessing a version. Replace both `<digest ...>` with real digests before Step 6; the commit must contain no angle-bracket placeholder (`grep -n '<digest' blueprints/wikijs/` returns nothing).

- [ ] **Step 2: `compose.yaml.tmpl`**

```yaml
# Rendered by paisans. Do not edit: `paisans apply` overwrites this file.
name: {{ .Project }}

services:
  app:
    labels:
      {{ .DeploymentLabel }}: "{{ .DeploymentID }}"
    image: {{ .Image "app" }}
    restart: unless-stopped
    env_file:
      - .env
    # The mesh address only; the gateway, over the mesh, is the only thing
    # that should reach it.
    ports:
      - "{{ .MeshAddress }}:{{ .App.Port }}:{{ .App.Port }}"
    healthcheck:
      test: ["CMD", "curl", "-fsS", "http://localhost:{{ .App.Port }}/healthz"]
      interval: 15s
      timeout: 5s
      start_period: 60s
    volumes:
      # The volume the image declares (dev/build/Dockerfile:47-51). Uploads
      # live in Postgres, so it holds nothing a member wrote.
      - {{ .DataPath }}/content:/wiki/data/content
{{- if .App.Pinned }}
    depends_on:
      - postgres
{{- end }}

  bootstrap:
    labels:
      {{ .DeploymentLabel }}: "{{ .DeploymentID }}"
    image: {{ .Image "bootstrap" }}
    restart: unless-stopped
    env_file:
      - bootstrap.env
    # Shares the app's process namespace so it can stop a stale setup-mode
    # instance, and runs as the app's user so it may signal it.
    pid: "service:app"
    user: "1000:1000"
    depends_on:
      - app
{{- if .App.Pinned }}

  postgres:
    labels:
      {{ .DeploymentLabel }}: "{{ .DeploymentID }}"
    image: {{ .Image "postgres" }}
    restart: unless-stopped
    environment:
      POSTGRES_DB: {{ .DBName }}
      POSTGRES_USER: {{ .DBUser }}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:?set in .env}
    volumes:
      - {{ .DataPath }}/postgres:{{ .PostgresDataMount }}
{{- end }}

networks:
  default:
    labels:
      {{ .DeploymentLabel }}: "{{ .DeploymentID }}"
```

- [ ] **Step 3: `.env.secret.tmpl`**

```
# Rendered by paisans. Do not edit.
#
# Wiki.js 2.5 reads only its database from the environment; everything else
# is the bootstrap's (bootstrap.env).
PAISANS_APP={{ .App.Name }}
PAISANS_SITE={{ .App.Site }}
DB_TYPE=postgres
# Every DB_* set, since an unset one reads as the string "undefined"
# (server/helpers/config.js:22). DATABASE_URL wins over them
# (server/core/db.js:32).
DB_HOST={{ .DBHost }}
DB_PORT={{ .DBPort }}
DB_USER={{ .DBUser }}
DB_NAME={{ .DBName }}
DB_SSL=false
{{- if .App.Pinned }}
DB_PASS={{ .DBPassword }}
POSTGRES_PASSWORD={{ .DBPassword }}
DATABASE_URL={{ .DSN }}
HA_ACTIVE=false
{{- else }}
DB_PASS={{ .DBPassword }}
# The cluster rejects connections without TLS; no-verify encrypts without
# checking its certificate, as sslmode=require does for the other apps.
DATABASE_URL={{ .DSN }}?sslmode=no-verify
# Every apps site runs an instance against this database; they tell each
# other about changes over LISTEN/NOTIFY (server/core/db.js:229-264).
HA_ACTIVE=true
{{- end }}
```

- [ ] **Step 4: `bootstrap.env.secret.tmpl`**

```
# Rendered by paisans. Do not edit.
WIKI_URL=http://app:{{ .App.Port }}
{{- if .App.Pinned }}
DATABASE_URL={{ .DSN }}
{{- else }}
DATABASE_URL={{ .DSN }}?sslmode=no-verify
{{- end }}
PUBLIC_URL={{ .PublicURL }}
ADMIN_EMAIL=wikijs-admin@{{ .Domain }}
ADMIN_PASSWORD={{ .Secret "local_admin_password" }}
OAUTH_CLIENT_ID={{ .OIDC.ClientID }}
OAUTH_CLIENT_SECRET={{ .OIDC.ClientSecret }}
OAUTH_AUTHORIZE_URL={{ .OIDC.AuthorizationEndpoint }}
OAUTH_TOKEN_URL={{ .OIDC.TokenEndpoint }}
OAUTH_USERINFO_URL={{ .OIDC.UserinfoEndpoint }}
OAUTH_LOGOUT_URL={{ .OIDC.EndSessionEndpoint }}
ADMIN_GROUP={{ .OIDCAdminGroup }}
SYNCED_GROUPS={{ range $i, $g := .SettingList "synced_groups" }}{{ if $i }},{{ end }}{{ $g }}{{ end }}
SITE_TITLE={{ envquote (.Setting "site_title" "Wiki") }}
SITE_DESCRIPTION={{ envquote (.Setting "site_description" "") }}
LOGO_URL={{ .Setting "logo_url" "" }}
UPLOAD_MAX_MB={{ .SettingInt "upload_max_mb" 5 }}
```

Check the OIDC field names against `internal/render/appview.go`'s `oidcValues` (`grep -n 'Endpoint' internal/render/appview.go` in paisans-stack); if there is no end-session field, add `EndSessionEndpoint` there in Phase 4's Task 15 (Pocket ID's is `/api/oidc/end-session`, `controller/well_known_controller.go` at v2.14.0) and note it in that task's commit. Check `envquote`'s behaviour on a value Wiki.js reads verbatim (Compose strips the quotes from a quoted env_file value).

- [ ] **Step 5: `caddy.snippet.tmpl`**

```
# Rendered by paisans. Do not edit.
#
# /finalize takes no authentication (server/setup.js:76 at v2.5.316):
# whoever reaches it first owns the wiki. Only the bootstrap, on the stack
# network, may; the edge refuses it always.
respond /finalize 404
# Uploads are capped by Wiki.js at upload_max_mb; the edge allows a request
# that size plus its multipart framing.
request_body {
	max_size {{ add (.SettingInt "upload_max_mb" 5) 1 }}MB
}
reverse_proxy{{ range .Upstreams }} {{ . }}{{ end }} {
{{- if gt (len .Upstreams) 1 }}
	import upstream_failover
{{- else if .UpstreamElsewhere }}
	import upstream_single
{{- end }}
	header_up X-Forwarded-Proto https
}
```

The snippet template data is not `appValues`: check what a snippet is given (`grep -n 'Upstreams' internal/render/*.go` in paisans-stack) and whether it has the app's settings and an `add` function. If it lacks either, Task 15 adds them (the settings as `.SettingInt`, and `add` to the FuncMap at `internal/render/site.go:40`), with a golden test.

- [ ] **Step 6: README and commit**

In `README.md`, replace the `outline/` line with `wikijs/        Wiki.js, the wiki, with its bootstrap`.

```bash
git add blueprints/wikijs README.md
git commit -m "feat: the wikijs blueprint" -m "Wiki.js 2.5.316 with the wikijs-bootstrap sidecar, gateable, signing in through Pocket ID with groups mapped from the claim. Spec: paisans-stack docs/specs/2026-10-13-wikijs-blueprint.md." -m "Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 14: Remove the `outline` blueprint

**Files:**
- Delete: `blueprints/outline/`
- Modify: `blueprints/writefreely/compose.yaml.tmpl:27` (a comment naming Outline)

- [ ] **Step 1: Delete and fix the comment**

```bash
git rm -r blueprints/outline
sed -n 20,32p blueprints/writefreely/compose.yaml.tmpl
```

Rewrite the comment line that names Outline so it stands alone (describe WriteFreely's own reason without the comparison).

- [ ] **Step 2: Verify nothing else names it**

Run: `grep -rni outline . --exclude-dir=.git`
Expected: no output.

- [ ] **Step 3: Commit, push, open the pull request**

```bash
git add -A
git commit -m "chore: the outline blueprint is removed" -m "Wiki.js is the core catalog's wiki. A deployment running Outline is redeployed, not converted." -m "Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>"
git push -u origin feat/wikijs
gh pr create --base main --title "feat: wikijs replaces outline as the core catalog's wiki" --body "$(cat <<'EOF'
Adds the wikijs blueprint (Wiki.js 2.5.316 and its bootstrap) and removes outline. Needs a paisans-stack with the list setting type and oidc.extra_groups_setting.

Spec: paisans-stack docs/specs/2026-10-13-wikijs-blueprint.md

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Stop for the human to merge. Record the merge commit for Task 15.

---

## Phase 4: `paisans-stack` pins the catalog and drops Outline

Worktree off `origin/develop` after Phase 1 merged: `git worktree add .worktrees/wikijs-pin -b feat/wikijs-default-wiki origin/develop`.

### Task 15: Pin the catalog with `wikijs`, and the template data it needs

**Files:**
- Modify: `internal/blueprints/core/pin.yaml`, `internal/blueprints/testdata/core/**` (by script)
- Modify: `internal/render/appview.go` and/or `internal/render/site.go` only if Task 13 steps 4 and 5 found missing fields or functions
- Modify: `internal/kinds/volumes.go`
- Modify: `internal/validate/validate_test.go` (remove Task 5's skip)

- [ ] **Step 1: Check whether the binary still pins the catalog**

Run: `test -f internal/blueprints/core/pin.yaml && echo pinned; grep -n 'pin of it goes' docs/decisions.md`
If `pin.yaml` is gone (the 2026-10-13 decision "the core catalog follows its newest release" has been implemented), skip Step 2 and follow the mechanism that replaced it (read `docs/specs/2026-10-13-core-catalog-fetched.md`); the testdata copy still has to carry `wikijs`.

- [ ] **Step 2: Pin**

Run: `scripts/pin-core-catalog.sh <Task 14 merge commit>`
Expected: `pin.yaml` lists `wikijs`, not `outline`; `internal/blueprints/testdata/core/blueprints/wikijs/` exists.

- [ ] **Step 3: Volume registry**

In `internal/kinds/volumes.go`, replace the line `"outlinewiki/outline:1.10.0": {"/var/lib/outline/data"},` with the Wiki.js reference exactly as the blueprint writes it, and add the bootstrap's (it declares none):

```go
	"ghcr.io/requarks/wiki:2.5.316@sha256:<the blueprint's digest>":                 {"/wiki/data/content"},
	"ghcr.io/paisans-software/wikijs-bootstrap:0.1.0@sha256:<the blueprint's digest>": nil,
```

(with the real digests, copied from `blueprints/wikijs/blueprint.yaml`). Run: `go test -tags registry_integration -run TestRecordedVolumesMatchEveryPlatform ./internal/kinds/`
Expected: PASS.

- [ ] **Step 4: Template data, if Task 13 needed it**

If Task 13 found no `EndSessionEndpoint` on `oidcValues`: add it beside `UserinfoEndpoint`, filled from the identity provider URL plus `/api/oidc/end-session`, with a test in `internal/render` asserting the rendered value. If the Caddy snippet data lacks the settings or `add`: add `SettingInt` to the snippet's data and `"add": func(a, b int) int { return a + b }` to the FuncMap, with a golden test (Step 5 covers it).

- [ ] **Step 5: Remove Task 5's skip and run everything**

Run: `go vet ./... && go test ./...`
Expected: failures only in tests that still name Outline; those are Task 16. Note them.

- [ ] **Step 6: Commit**

```bash
git add -A internal scripts
git commit -m "feat: pin the core catalog with wikijs" -m "Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 16: Tests and fixtures move off Outline

**Files:**
- Modify: `internal/validate/testdata/*.yaml` (106 files), `internal/validate/validate_test.go`
- Delete: `internal/validate/testdata/outline-bucket-named-outline.yaml`, `internal/validate/testdata/outline-bucket-in-media-url.yaml` and their rows (`validate_test.go:46,102`)
- Modify: `internal/render/testdata/deployment.yaml`, `internal/render/testdata/secrets.fixture.yaml`, the golden trees (regenerated)
- Modify: every `_test.go` that names `outline` (`grep -rln outline --include=*_test.go .`)
- Create: `internal/validate/testdata/wikijs-synced-groups-lack-admin-group.yaml`

- [ ] **Step 1: Swap the validate fixtures' docs app**

Each fixture's `docs` app block is (verbatim in `valid.yaml:74-79`):

```yaml
  docs:
    blueprint: outline
    hostname: docs.example.org
    placement: cluster
    settings:
      s3_bucket: docs-uploads
```

Replace it in every file with:

```yaml
  wiki:
    blueprint: wikijs
    hostname: wiki.example.org
    placement: cluster
```

using a script that replaces exactly that block and reports any file where the block differs (the copies have drifted; fix those by hand):

```bash
python3 - <<'EOF'
import glob
old = """  docs:
    blueprint: outline
    hostname: docs.example.org
    placement: cluster
    settings:
      s3_bucket: docs-uploads
"""
new = """  wiki:
    blueprint: wikijs
    hostname: wiki.example.org
    placement: cluster
"""
for p in sorted(glob.glob('internal/validate/testdata/*.yaml')):
    t = open(p).read()
    if old in t:
        open(p, 'w').write(t.replace(old, new))
    elif 'outline' in t:
        print('by hand:', p)
EOF
```

Then `grep -rn 'docs\b\|outline' internal/validate/testdata/ | head` and fix what remains (other references to the app name `docs`, such as a gate or monitor list).

- [ ] **Step 2: Tests that used Outline for S3 move to WriteFreely**

For each test listed by `grep -rln 'outline' --include=*_test.go .`, decide by what it tests:
- S3, buckets, media hostnames, Garage: point it at the WriteFreely app (blueprint `writefreely`, which stores in S3). The fixture `valid.yaml` already has a `blog` app if it has one; check with `grep -n 'writefreely' internal/validate/testdata/valid.yaml`, and add one if absent.
- anything else (OIDC, gate, Postgres, display name, volumes): point it at the `wiki` app (`wikijs`).
- Outline-only rules: delete the test and its fixture.

Run `go test ./...` after each package.

- [ ] **Step 3: The admin group check gets a fixture**

`internal/validate/testdata/wikijs-synced-groups-lack-admin-group.yaml`: `valid.yaml` with the wiki app's `settings: {synced_groups: [members]}`; add its row expecting `wikijs-synced-groups-lack-admin-group`.

- [ ] **Step 4: Regenerate goldens and read the diff**

Run: `go test ./internal/render -update && go test ./internal/create -update && git diff --stat internal/render/testdata internal/create/testdata`
Then read `git diff internal/render/testdata/golden` in full. Expected: `docs` stacks gone, `wiki` stacks with `compose.yaml`, `.env`, `bootstrap.env` (0600), and a Caddy snippet containing `respond /finalize 404`; no `docs-media` hostname.

- [ ] **Step 5: Run everything**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add -A internal
git commit -m "chore: tests use wikijs for the wiki and writefreely for S3" -m "Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 17: The wizard, help and error text

**Files:**
- Modify: `internal/create/shapes.go:104-106`
- Modify: `internal/create/wizard_test.go` (`TestDisplayNameIsAskedAndKept`, `w.Answers.App("docs")`)
- Modify: `cmd/paisans/help.go:374`, `cmd/paisans/oidc.go:75`, `internal/config/config.go:1067`
- Regenerate: `internal/create/testdata/*.yaml`

- [ ] **Step 1: Update the test first**

In `TestDisplayNameIsAskedAndKept`, change the expected prompt to `"Name shown at sign in for the wiki [wiki]: "` and `w.Answers.App("docs")` to `w.Answers.App("wiki")`.

Run: `go test ./internal/create/ -run TestDisplayNameIsAskedAndKept`
Expected: FAIL.

- [ ] **Step 2: Change the capability**

```go
	{question: "A wiki (Wiki.js)?", def: true, apps: []appSpec{
		{key: "wiki", kind: "wikijs", sub: "wiki", gateable: true, what: "wiki"},
	}},
```

- [ ] **Step 3: Help and error text**

- `help.go:374`: `"Writes nothing without --execute. Supports the mbin, wikijs, writefreely and uptime kinds.",`
- `oidc.go:75`: replace `outline` with `wikijs` in the kinds list.
- `config.go:1067`: `for example mbin or wikijs`.

- [ ] **Step 4: Regenerate and run**

Run: `go test ./internal/create -update && go test ./internal/create/ ./cmd/paisans/ ./internal/config/`
Expected: PASS; `git diff internal/create/testdata` shows `wiki:` / `blueprint: wikijs` / `hostname: wiki.example.org`.

- [ ] **Step 5: Commit**

```bash
git add internal/create cmd/paisans internal/config
git commit -m "feat: create offers Wiki.js for the wiki" -m "Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 18: Documentation and the decision

**Files:**
- Modify: `README.md` (Outline sections: `grep -n -i outline README.md`), `AGENTS.md:177`, `examples/paisans-stack.example.yaml:304-324`, `examples/secrets.example.yaml:96-103`
- Modify: `docs/decisions.md` (append)

- [ ] **Step 1: Rewrite, do not delete**

Rewrite each Outline section of `README.md` for Wiki.js in place (deleting a section needs the human's approval under `AGENTS.md`; if a section has no Wiki.js counterpart, such as Outline's bucket naming bug, list it and ask before removing it). Include the break glass recovery procedure from the spec's *The break glass admin* as its own subsection. Facts about Wiki.js are cited to `v2.5.316` source as the spec cites them; add none from memory.

The example config's app:

```yaml
  wiki:
    blueprint: wikijs
    hostname: wiki.example.org
    # What Pocket ID's sign in page calls this app, as its client's name.
    display_name: Wiki
    placement: cluster
    settings:
      # Pocket ID groups that become Wiki.js groups. Membership follows
      # Pocket ID at every sign in; what each group may do is set in Wiki.js.
      synced_groups: [admins, members]
      site_title: "Example Wiki"
```

and in `secrets.example.yaml`, the wiki's entry: `database_password: "generated-at-init"` and `local_admin_password: "generated-at-init"`.

- [ ] **Step 2: The decision**

Append to `docs/decisions.md`:

```markdown
## 2026-10-13: Wiki.js is the core catalog's wiki, and Outline is removed

Founder decision, 2026-10-13. Spec: `docs/specs/2026-10-13-wikijs-blueprint.md`.

### What was decided

The core catalog's wiki is Wiki.js 2.5, as the `wikijs` blueprint with its
bootstrap, and the `outline` blueprint is removed. A deployment running
Outline is redeployed, not converted.

### Why

A community's wiki has to follow the groups its members are in at Pocket ID.
Wiki.js's OAuth2 sign-in maps the groups claim onto wiki groups at every
login, and its page rules gate content by path per group. Uploads live in
Postgres, so the database's backup is the wiki's.

### What was weighed

Outline reads no groups claim. Its group sync exists in source but no
plugin registers it in 1.10.0, and 1.11.0 gates it behind a paid
entitlement. Keeping it meant a SCIM bridge from Pocket ID, a second client
to carry it, and an API key only a signed in admin can make. Wiki.js 3 is a
beta its own README calls not for production use.
```

- [ ] **Step 3: Verify the documentation by hand**

Run: `git diff --stat && git diff | grep -n '—'` (expected: no em dash in added lines), `grep -rn -i 'outline' README.md AGENTS.md examples/` (expected: only historical mentions the human approved keeping), and parse every changed YAML example (`ruby -ryaml -e 'YAML.load_file(ARGV[0])' examples/paisans-stack.example.yaml`).

- [ ] **Step 4: Commit, push, open the pull request**

```bash
git add README.md AGENTS.md examples docs/decisions.md
git commit -m "docs: Wiki.js is the wiki, and why" -m "Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>"
go vet ./... && go test ./...
git push -u origin feat/wikijs-default-wiki
gh pr create --base develop --title "feat: Wiki.js replaces Outline as the default wiki" --body "$(cat <<'EOF'
Pins the core catalog release carrying the wikijs blueprint, moves the tests and fixtures off Outline, offers Wiki.js in create, and records the decision.

Spec: docs/specs/2026-10-13-wikijs-blueprint.md

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Also push `docs/wikijs-blueprint-spec` and open its pull request against `develop` if the human wants the spec and plan merged ahead of the code.
