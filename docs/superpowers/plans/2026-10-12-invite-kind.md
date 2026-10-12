# The `invite` Kind Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Teach `paisans` a new app kind, `invite`, that deploys `ghcr.io/paisans-software/paisans-invite` with cluster placement, its own Postgres role, its own OIDC client, mail, two generated keys, the identity app's static API key and accent colour, and the deployment's code of conduct mounted read-only.

**Architecture:** A kind in this toolkit is not one struct but an entry in each of several per-kind tables (catalogue, port, health, gate, config file, volumes, mail, OIDC client shape) plus validate rules, secret keys and a template set. This plan adds `invite` to each, following the `uptime` and `outline` entries, and renders one file no kind has had before: the code of conduct, taken from the app's settings and mounted read-only.

**Tech Stack:** Go 1.25, the toolkit's own packages (`internal/config`, `internal/kinds`, `internal/validate`, `internal/secretsgen`, `internal/render`), golden-file tests.

**Spec:** `docs/specs/2026-10-12-invite-service.md`. The service it deploys is planned in `docs/superpowers/plans/2026-10-12-paisans-invite-service.md`; its **environment contract** table there is the interface this plan renders. Read both.

**Depends on:** `paisans-invite` `v0.1.0` published (that plan's Task 13). Until then, Task 1 pins the kind to `ghcr.io/paisans-software/paisans-invite:develop` so the toolkit work can proceed; Task 6 moves it to the release.

**Where:** `paisans-stack`, a worktree `.worktrees/invite-kind` on branch `feat/invite-kind` from `develop`, one pull request into `develop`. Never `git stash`.

> **Amendment, controller ruling 2026-10-12 (supersedes every mention of a code-of-conduct *file* below).** The toolkit's configuration store versions and syncs only `paisans.yaml`, the secrets file and `.sops.yaml`; configs are also loaded from it with `config.LoadBytes`, where `cfg.Path` is empty. So `code_of_conduct` holds the **Markdown text itself** (a YAML block scalar in `paisans.yaml`), not a path. Consequences: no `kinds.InviteCoCPath`; `invite-code-of-conduct-missing` refuses a missing, empty (after trimming) or non-string value; `inviteValuesFor` takes the text from the setting (no `os.ReadFile`); fixtures and the example carry the text inline (no `code-of-conduct.md` files in testdata or examples); the rendered `code-of-conduct.md` in the app's data directory and its read-only mount stay exactly as below, so the service's `INVITE_COC_PATH` is unchanged. Review Focus 2 becomes: "a code of conduct given as a path-like string is just text; nothing reads files".

## Global Constraints

- Kind name `invite`; constant `config.KindInvite`.
- Placement: `cluster` only. Rule `invite-must-be-clustered`.
- Container port `8080`; health `GET /healthz` answers `200`.
- Settings and ranges, copied from the spec: `identity_app` (required, a `pocket-id` app), `invite_quota` 1–100 (3), `require_approval` bool (true), `invite_lifetime_days` 1–365 (14), `application_wait_days` 1–365 (14), `decline_retention_days` 1–3650 (183), `login_link_days` 1–31 (7; how long the sign-in link Pocket ID emails lasts, and what the invitee's page says), `code_of_conduct` (required, the Markdown text inline in `paisans.yaml`). Plus `member_group` and `admin_group`, the OIDC group settings every client-bearing kind reads (defaults `members`, `admins`).
- Generated secrets: `apps.<invite>.email_hash_key`, `apps.<invite>.session_key`, both the default `password()` shape (32 random bytes, unpadded base64url). Plus `database_password`, which `UsesPostgres` already brings.
- The environment variable names are exactly the service plan's contract table; rename nothing.
- `validate` refuses: an `identity_app` missing or not `pocket-id`; that app's `allow_user_signups` set to anything but `disabled`; that app's `email_one_time_access_as_admin` set to `false`; that app's `email_one_time_access_as_unauthenticated` not set to `true` (rule `invite-recovery-disabled`; founder decision 2026-10-12: a member whose 7-day link expired recovers through Pocket ID's email-code sign-in); no SMTP host and sender for the invite app; a `code_of_conduct` that is missing, blank or not text; a placement other than `cluster`; a setting out of range.
- The example configuration's `signup_default_groups` becomes `[members]`, and its `allow_user_signups` becomes `disabled`.
- README: no rejected alternatives; describe what is, not what was weighed.

## Review Focus

1. **`allow_user_signups` unset on the identity app.** Pocket ID's default is `disabled`, so unset passes; only an explicit `withToken` or `open` is refused. Pinned in Task 2 (`TestInviteSignupsUnsetIsFine`).
2. **A code of conduct that looks like a path (`code-of-conduct.md`).** It is text; nothing reads files. Pinned in Task 2 (`TestInviteCodeOfConductIsText`) and Task 4 (`TestInviteCoCRendered`).
3. **A renamed identity host, or two `pocket-id` apps, or the identity app named in `identity_app` differing from the one `identityProviderURL` finds.** A changed `hostname` re-renders `INVITE_IDP_URL` and `INVITE_OIDC_ISSUER` and the next apply recreates the invite stack (Task 4 `TestInviteFollowsTheIdentityHostname`, `TestInviteEnvChangedByIdentityHostnameRecreates`). The invite app's issuer, API key and accent colour all come from the app named in `identity_app`, never from a search. Pinned in Task 4 (`TestInviteReadsItsNamedIdentityApp`).
4. **An accent colour unset, or `default`, on the identity app.** `INVITE_ACCENT_COLOR` renders empty and the service uses its own default. Pinned in Task 4 (`TestInviteAccent`).
5. **Re-running `init` after the invite app exists.** The two keys are never regenerated (secretsgen fills only missing keys). Pinned in Task 3 (`TestInviteKeysAreStable`).

---

### Task 1: Register the kind

**Files:**
- Modify: `internal/config/config.go:54-66` (constant, `knownKinds`)
- Modify: `internal/kinds/kinds.go` (catalogue after `KindUptime` at `:167`; `SendsMail` at `:197-199`; `configFiles` at `:322-334`)
- Modify: `internal/kinds/health.go` (map at `:9-59`)
- Modify: `internal/kinds/gate.go` (map, beside `KindUptime` at `:217`)
- Modify: `internal/kinds/volumes.go` (`RepositoryVolumes` at `:101-105`)
- Modify: `internal/kinds/mbin.go` (`OIDCClient` switch at `:85-158`)
- Modify: `internal/render/plan.go:51-65` (`appPort`)
- Test: `internal/kinds/kinds_test.go`, `internal/kinds/health_test.go:37` (`TestOnlyUptimePocketIDAndMbinSendMail`), `internal/kinds/volumes_test.go:14`

**Interfaces:**
- Produces: `config.KindInvite`; `kinds.OIDCClient(config.KindInvite, hostname, …)` returning a spec with `CallbackURL: "https://" + hostname + "/auth/callback"`, `PKCE: true`, `MemberGroupSetting: "member_group"`, `AdminGroupSetting: "admin_group"`, `DefaultMemberGroup: MembersGroup`, `DefaultAdminGroup: AdminsGroup`, `ReadsGroups: true`.

- [ ] **Step 1: Failing tests**

Add to `internal/kinds/kinds_test.go`:

```go
func TestInviteKind(t *testing.T) {
	if !UsesPostgres(config.KindInvite) {
		t.Fatal("invite keeps its state in Postgres")
	}
	if !SendsMail(config.KindInvite) {
		t.Fatal("invite sends mail")
	}
	spec, ok := OIDCClient(config.KindInvite, "invite.example.org", "invite")
	if !ok || spec.CallbackURL != "https://invite.example.org/auth/callback" || !spec.PKCE || !spec.ReadsGroups {
		t.Fatalf("%+v", spec)
	}
	if spec.DefaultMemberGroup != MembersGroup || spec.DefaultAdminGroup != AdminsGroup {
		t.Fatalf("%+v", spec)
	}
	if g := gates[config.KindInvite]; g.Gateable {
		t.Fatal("invitees are not members; the gate must never sit in front of invite")
	}
}
```

Adjust the `OIDCClient` call to its real signature in `mbin.go:85` (it may take the app or settings rather than a name). Rename `TestOnlyUptimePocketIDAndMbinSendMail` to `TestOnlyTheseKindsSendMail` and add `config.KindInvite` to its expected set. Add `config.KindInvite` to the kind list in `TestEveryDefaultImageHasItsVolumesRecorded`.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/kinds/ ./internal/config/`
Expected: FAIL, undefined `config.KindInvite`.

- [ ] **Step 3: Implement**

`internal/config/config.go`:

```go
	KindInvite      Kind = "invite"
```

and `KindInvite: true` in `knownKinds`.

`internal/kinds/kinds.go`, after the `KindUptime` entry:

```go
	config.KindInvite: {
		// paisans-invite: member invites with the code of conduct before
		// the account (docs/specs/2026-10-12-invite-service.md). Cluster
		// placement only, so the PostgresService row is the cluster's role
		// and database, never a container of its own.
		{Name: "app", Image: "ghcr.io/paisans-software/paisans-invite:develop", Purpose: "the invite service"},
		{Name: PostgresService, Purpose: "its role and database on the cluster"},
	},
```

`SendsMail`:

```go
	return kind == config.KindUptime || kind == config.KindPocketID || kind == config.KindMbin || kind == config.KindInvite
```

`configFiles`: `config.KindInvite: {ConfigEnv, ".env"},`

`health.go`: `config.KindInvite: {"/healthz", "200"},` with the comment `// paisans-invite's /healthz answers 200 when its database does.`

`gate.go`: `config.KindInvite: {Gateable: false},` with the comment `// Invitees reach /i/* before they have an account; a member gate would lock them out.`

`volumes.go`, `RepositoryVolumes`: `"ghcr.io/paisans-software/paisans-invite": nil, // distroless, declares no VOLUME`. (Confirm with `docker image inspect --format '{{json .Config.Volumes}}'` on the pinned image; it must print `null`.)

`mbin.go`, in `OIDCClient`:

```go
	case config.KindInvite:
		// paisans-invite's callback is GET /auth/callback with PKCE S256
		// (internal/web/auth.go); it reads the groups claim to tell members
		// from admins.
		return OIDCClientSpec{
			CallbackURL:        "https://" + hostname + "/auth/callback",
			LaunchURL:          LaunchURL(kind, hostname, ""),
			PKCE:               true,
			MemberGroupSetting: "member_group",
			AdminGroupSetting:  "admin_group",
			DefaultMemberGroup: MembersGroup,
			DefaultAdminGroup:  AdminsGroup,
			ReadsGroups:        true,
		}, true
```

`internal/render/plan.go`, `appPort`: `config.KindInvite: 8080,`

- [ ] **Step 4: Run the whole suite**

Run: `go vet ./... && go test ./...`
Expected: PASS except `TestEveryKindShipsACommittedTemplateSet`, which Task 4 satisfies. To keep this task green, create the template directory now with a placeholder-free minimal `internal/render/templates/invite/compose.yaml.tmpl` copied from Task 4 Step 3; Task 4 completes the set.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: the invite kind's catalogue, port, health, gate and OIDC client"
```

---

### Task 2: Validate rules

**Files:**
- Create: `internal/validate/invite.go`, `internal/validate/invite_test.go`
- Create fixtures: `internal/validate/testdata/invite-must-be-clustered.yaml`, `invite-identity-app.yaml`, `invite-signups-not-disabled.yaml`, `invite-login-link-disabled.yaml`, `invite-recovery-disabled.yaml`, `invite-without-smtp.yaml`, `invite-code-of-conduct-missing.yaml`, `invite-setting-invalid.yaml`, and `internal/validate/testdata/code-of-conduct.md`
- Modify: `internal/validate/validate.go:121-205` (register the checks in `Check()`), `internal/validate/validate_test.go:26-130` (`TestRulesFire` rows), `internal/validate/testdata/valid.yaml` (add an `invite` app so every fixture starts valid)

**Interfaces:**
- Produces rules (all `Refuse`): `invite-must-be-clustered`, `invite-identity-app`, `invite-signups-not-disabled`, `invite-login-link-disabled`, `invite-without-smtp`, `invite-code-of-conduct-missing`, `invite-setting-invalid`; and `kinds.InviteCoCPath(cfgPath, setting string) string` in `internal/kinds/invite.go` (absolute setting used as is, otherwise joined to `filepath.Dir(cfgPath)`), used by validate and render alike.

- [ ] **Step 1: Failing tests**

Add the `invite` app to `testdata/valid.yaml` (its `auth` app must not set `allow_user_signups` to anything but `disabled`, must set `email_one_time_access_as_unauthenticated: true`, and the file must have an `smtp:` block; it already has one if uptime is in it):

```yaml
  invite:
    kind: invite
    hostname: invite.example.org
    placement: cluster
    settings:
      identity_app: auth
      code_of_conduct: code-of-conduct.md
```

`testdata/code-of-conduct.md`: `# Be kind`.

Each fixture is `valid.yaml` with one thing broken:

| Fixture | Change |
|---------|--------|
| `invite-must-be-clustered` | `placement: { pinned: <a site in valid.yaml> }` |
| `invite-identity-app` | `identity_app: nope` |
| `invite-signups-not-disabled` | the `auth` app gets `allow_user_signups: withToken` |
| `invite-login-link-disabled` | the `auth` app gets `email_one_time_access_as_admin: false` |
| `invite-recovery-disabled` | the `auth` app's `email_one_time_access_as_unauthenticated` removed |
| `invite-without-smtp` | the top-level `smtp:` block removed (and any app needing it adjusted so only this rule fires; if uptime then fires its warning, that is a warning, not a refusal) |
| `invite-code-of-conduct-missing` | `code_of_conduct: missing.md` |
| `invite-setting-invalid` | `invite_quota: 0` |

Rows in `TestRulesFire`, e.g. `{"invite-must-be-clustered", "invite-must-be-clustered", validate.Refuse},` for each.

`internal/validate/invite_test.go`:

```go
package validate_test

import (
	"testing"

	"github.com/paisans-software/paisans-stack/internal/validate"
)

func TestInviteSignupsUnsetIsFine(t *testing.T) {
	r := checkFixture(t, "valid.yaml")
	if f := findings(r, "invite-signups-not-disabled"); len(f) != 0 {
		t.Fatalf("unset allow_user_signups is Pocket ID's default, disabled: %v", f)
	}
}

func TestInviteSettingRanges(t *testing.T) {
	for key, bad := range map[string]any{
		"invite_quota": 101, "invite_lifetime_days": 0, "application_wait_days": 366,
		"decline_retention_days": 3651, "require_approval": "yes",
	} {
		r := withInviteSetting(t, key, bad)
		if len(findings(r, "invite-setting-invalid")) != 1 {
			t.Fatalf("%s=%v not refused", key, bad)
		}
	}
	if len(findings(withInviteSetting(t, "invite_quota", 100), "invite-setting-invalid")) != 0 {
		t.Fatal("100 is in range")
	}
}
```

Write `checkFixture` and `withInviteSetting` beside the existing helpers in `pocketid_test.go:13-36` if equivalents do not already exist (`withAuth` there is the pattern: load `valid.yaml`, change one setting, run `validate.Check`).

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/validate/`
Expected: FAIL; the rules do not exist.

- [ ] **Step 3: Implement**

`internal/kinds/invite.go`:

```go
package kinds

import "path/filepath"

// InviteSettingRanges are the integer settings of the invite kind and their
// inclusive ranges, from docs/specs/2026-10-12-invite-service.md.
var InviteSettingRanges = map[string][2]int{
	"invite_quota":           {1, 100},
	"invite_lifetime_days":   {1, 365},
	"application_wait_days":  {1, 365},
	"decline_retention_days": {1, 3650},
	"login_link_days":        {1, 31},
}

// InviteSettingDefaults are what the service is told when a setting is unset.
var InviteSettingDefaults = map[string]string{
	"invite_quota": "3", "require_approval": "true", "invite_lifetime_days": "14",
	"application_wait_days": "14", "decline_retention_days": "183", "login_link_days": "7",
}

// InviteCoCPath is where the code of conduct is read from: an absolute
// setting as written, a relative one beside paisans.yaml.
func InviteCoCPath(cfgPath, setting string) string {
	if filepath.IsAbs(setting) {
		return setting
	}
	return filepath.Join(filepath.Dir(cfgPath), setting)
}
```

`internal/validate/invite.go`:

```go
package validate

import (
	"fmt"
	"os"
	"strconv"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

func (c *checker) inviteApps() []string {
	var out []string
	for _, name := range sortedAppNames(c.cfg) { // use the package's existing sorted-name helper
		if c.cfg.Apps[name].Kind == config.KindInvite {
			out = append(out, name)
		}
	}
	return out
}

func (c *checker) inviteMustBeClustered() {
	for _, name := range c.inviteApps() {
		if c.cfg.Apps[name].Placement.Mode != config.PlacementCluster {
			c.refuse("invite-must-be-clustered", fmt.Sprintf("apps.%s.placement", name),
				"invite must have cluster placement",
				"An invite must survive the loss of a site, so the invite service keeps its state in the cluster's Postgres and runs on every cluster site. Set placement: cluster.")
		}
	}
}

func (c *checker) inviteIdentityApp() {
	for _, name := range c.inviteApps() {
		idp, _ := c.cfg.Apps[name].Settings["identity_app"].(string)
		if app, ok := c.cfg.Apps[idp]; !ok || app.Kind != config.KindPocketID {
			c.refuse("invite-identity-app", fmt.Sprintf("apps.%s.settings.identity_app", name),
				"identity_app must name a pocket-id app",
				"The invite service creates each approved member's account at the pocket-id app named here, and signs members in through it. %q is not a pocket-id app in this configuration.", idp)
			continue
		}
		s := c.cfg.Apps[idp].Settings
		if v, set := s["allow_user_signups"]; set && v != "disabled" {
			c.refuse("invite-signups-not-disabled", fmt.Sprintf("apps.%s.settings.allow_user_signups", idp),
				"pocket id signups bypass the code of conduct",
				"With an invite app, new accounts come only from invites, after the code of conduct. Pocket ID's own signup links would admit people past it. Set allow_user_signups: disabled or remove it.")
		}
		if v := s["email_one_time_access_as_unauthenticated"]; v != true {
			c.refuse("invite-recovery-disabled", fmt.Sprintf("apps.%s.settings.email_one_time_access_as_unauthenticated", idp),
				"members could not recover an expired sign-in link",
				"A new member whose 7-day sign-in link expired gets a fresh one from Pocket ID's sign-in page with an email code. Set email_one_time_access_as_unauthenticated: true.")
		}
		if v, set := s["email_one_time_access_as_admin"]; set && v == false {
			c.refuse("invite-login-link-disabled", fmt.Sprintf("apps.%s.settings.email_one_time_access_as_admin", idp),
				"approved members could not be sent a sign-in link",
				"The invite service has Pocket ID email each approved member a one-time sign-in link, which Pocket ID refuses while email_one_time_access_as_admin is false.")
		}
	}
}

func (c *checker) inviteWithoutSMTP() {
	for _, name := range c.inviteApps() {
		if !c.cfg.SMTPFor(name).CanMail() {
			c.refuse("invite-without-smtp", fmt.Sprintf("apps.%s", name),
				"invite needs mail",
				"The invite service emails admins when an application is waiting and tells invitees the outcome. Declare an smtp: block with a host and from_address.")
		}
	}
}

func (c *checker) inviteCodeOfConduct() {
	for _, name := range c.inviteApps() {
		setting, _ := c.cfg.Apps[name].Settings["code_of_conduct"].(string)
		path := kinds.InviteCoCPath(c.cfg.Path, setting)
		if fi, err := os.Stat(path); setting == "" || err != nil || !fi.Mode().IsRegular() {
			c.refuse("invite-code-of-conduct-missing", fmt.Sprintf("apps.%s.settings.code_of_conduct", name),
				"the code of conduct file is missing",
				"Every invitee agrees to this file before an account exists. Put it beside paisans.yaml and name it here; %s was not found.", path)
		}
	}
}

func (c *checker) inviteSettingInvalid() {
	for _, name := range c.inviteApps() {
		s := c.cfg.Apps[name].Settings
		for key, rng := range kinds.InviteSettingRanges {
			v, set := s[key]
			if !set {
				continue
			}
			n, ok := v.(int)
			if !ok || n < rng[0] || n > rng[1] {
				c.refuse("invite-setting-invalid", fmt.Sprintf("apps.%s.settings.%s", name, key),
					"invite setting out of range",
					"%s must be a whole number from %s to %s.", key, strconv.Itoa(rng[0]), strconv.Itoa(rng[1]))
			}
		}
		if v, set := s["require_approval"]; set {
			if _, ok := v.(bool); !ok {
				c.refuse("invite-setting-invalid", fmt.Sprintf("apps.%s.settings.require_approval", name),
					"invite setting out of range", "require_approval must be true or false.")
			}
		}
	}
}
```

Match the real helper names: the sorted app iterator, how YAML integers arrive in `Settings` (`int` or `uint64`; check `writefreelySettingsInRange` at `validate.go:1345-1372` and copy its conversion), and the `c.refuse` argument order at `validate.go:110-116`. Register all five methods in `Check()`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/validate/ && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: validate the invite kind's placement, identity app, mail, code of conduct and settings"
```

---

### Task 3: Secrets

**Files:**
- Modify: `internal/secretsgen/secretsgen.go:216-280` (`appSecretKeys`), `:510-572` (`owed` text)
- Modify: `internal/render/database.go:63-78` (`requiredAppSecrets`)
- Test: `internal/secretsgen/secretsgen_test.go`

**Interfaces:**
- Produces: `apps.<invite>.email_hash_key`, `apps.<invite>.session_key` generated by `init`; `render` refuses an invite app missing either.

- [ ] **Step 1: Failing tests**

```go
func TestInviteKeysAreStable(t *testing.T) {
	cfg := configWithApp(t, "invite", config.KindInvite) // use the file's existing builder
	s := &config.Secrets{}
	Fill(cfg, s)
	first := s.Apps["invite"]["session_key"]
	if len(first) != 43 || len(s.Apps["invite"]["email_hash_key"]) != 43 {
		t.Fatalf("want 32 bytes as unpadded base64url: %q", first)
	}
	Fill(cfg, s)
	if s.Apps["invite"]["session_key"] != first {
		t.Fatal("init must never replace an existing key")
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/secretsgen/ -run Invite`
Expected: FAIL; the keys are not generated.

- [ ] **Step 3: Implement**

`appSecretKeys`, beside the uptime case:

```go
	if app.Kind == config.KindInvite {
		// email_hash_key keys the hash of a declined address; session_key
		// signs the sign-in cookie (docs/specs/2026-10-12-invite-service.md).
		keys = append(keys, "email_hash_key", "session_key")
	}
```

`requiredAppSecrets`:

```go
	config.KindInvite: {
		{"email_hash_key", "paisans-invite refuses to start without INVITE_EMAIL_HASH_KEY"},
		{"session_key", "paisans-invite refuses to start without INVITE_SESSION_KEY"},
	},
```

`owed`: a line for the invite kind matching the uptime one's wording.

- [ ] **Step 4: Run the tests**

Run: `go test ./...`
Expected: PASS except `TestExampleSecretsMatchAppSecretKeys` once Task 5 adds the app to the example; it passes now because the example has no invite app yet.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: init generates the invite service's email hash and session keys"
```

---

### Task 4: Render

**Files:**
- Create: `internal/render/templates/invite/compose.yaml.tmpl`, `internal/render/templates/invite/.env.secret.tmpl`, `internal/render/templates/invite/caddy.snippet.tmpl`, `internal/render/templates/invite/code-of-conduct.md.tmpl`, `internal/render/invite.go`, `internal/render/invite_test.go`, `internal/render/testdata/code-of-conduct.md`
- Modify: `internal/render/appview.go` (new fields on `appValues`; the kind branch in `values()` near `:463`; `oidcFor` switch at `:530-536`)
- Modify: `internal/render/testdata/deployment.yaml` (add an invite app), `internal/render/testdata/secrets.fixture.yaml` (its keys and OIDC client)
- Regenerate: `internal/render/testdata/golden/**`

**Interfaces:**
- Consumes: `kinds.InviteCoCPath`, `kinds.InviteSettingDefaults`, `p.appSecret(app, key)`, `p.cfg.Apps[idp].Settings`.
- Produces on `appValues`: `Invite inviteValues` with `IdentityURL, APIKey, Accent, CoC string` and `Settings map[string]string` (every setting with defaults applied).

- [ ] **Step 1: Failing tests**

Add to `testdata/deployment.yaml`:

```yaml
  invite:
    kind: invite
    hostname: invite.example.org
    placement: cluster
    settings:
      identity_app: auth
      invite_quota: 5
      code_of_conduct: code-of-conduct.md
```

`testdata/code-of-conduct.md`: `# Be kind\n\nTo each other.\n`. Add `invite` entries to `secrets.fixture.yaml`: `apps.invite.database_password`, `email_hash_key`, `session_key` (any 43-character values) and `oidc_clients.invite` (`client_id`, `client_secret`).

`internal/render/invite_test.go`:

```go
package render

import (
	"strings"
	"testing"
)

func TestInviteCoCRendered(t *testing.T) {
	files := renderFixture(t) // the helper TestGoldenTree uses; returns path -> content
	coc := findFile(t, files, "/invite/code-of-conduct.md")
	if !strings.Contains(coc, "To each other.") {
		t.Fatal("the code of conduct must be the file beside deployment.yaml, verbatim")
	}
	env := findFile(t, files, "/invite/.env")
	for _, want := range []string{
		"INVITE_PUBLIC_URL=https://invite.example.org",
		"INVITE_OIDC_ISSUER=https://",
		"INVITE_QUOTA=5",
		"INVITE_REQUIRE_APPROVAL=true",
		"INVITE_DECLINE_RETENTION_DAYS=183",
		"INVITE_LOGIN_LINK_DAYS=7",
		"INVITE_TRUST_FORWARDED=true",
		"INVITE_COC_PATH=/etc/paisans-invite/code-of-conduct.md",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestInviteReadsItsNamedIdentityApp(t *testing.T) {
	files := renderFixture(t)
	env := findFile(t, files, "/invite/.env")
	auth := findFile(t, files, "/auth/.env")
	key := lineValue(auth, "STATIC_API_KEY")
	if key == "" || lineValue(env, "INVITE_IDP_API_KEY") != key {
		t.Fatal("the API key must be the named identity app's static key")
	}
}
```

Also add:

```go
func TestInviteAccent(t *testing.T) {
	files := renderFixture(t)
	env := findFile(t, files, "/invite/.env")
	auth := findFile(t, files, "/auth/.env")
	want := lineValue(auth, "ACCENT_COLOR")
	if want == "default" {
		want = ""
	}
	if got := lineValue(env, "INVITE_ACCENT_COLOR"); got != want {
		t.Fatalf("accent %q, want the identity app's %q", got, want)
	}
}
```

And the founder's requirement that a renamed identity host reaches the service:

```go
func TestInviteFollowsTheIdentityHostname(t *testing.T) {
	cfg, secrets := fixtureConfig(t) // the loader TestGoldenTree uses
	first := renderEnv(t, cfg, secrets, "/invite/.env")
	auth := cfg.Apps["auth"]
	auth.Hostname = "login.renamed.example"
	cfg.Apps["auth"] = auth
	second := renderEnv(t, cfg, secrets, "/invite/.env")
	for _, key := range []string{"INVITE_IDP_URL", "INVITE_OIDC_ISSUER"} {
		if got := lineValue(second, key); got != "https://login.renamed.example" {
			t.Fatalf("%s = %q after the identity hostname changed", key, got)
		}
		if lineValue(first, key) == lineValue(second, key) {
			t.Fatalf("%s did not change", key)
		}
	}
}
```

In `internal/apply/apply_test.go`, beside `TestAnEnvFileChangeRecreates` (`:1639`), add `TestInviteEnvChangedByIdentityHostnameRecreates`: plan a site whose host already holds the invite stack rendered with the old identity hostname, change the identity app's `hostname`, plan again, and assert the invite stack is planned for recreate because its `.env` changed. Reuse that test's fixtures and assertions; if the existing test is already generic over any app's `.env`, a second case naming the invite stack is enough.

Write `findFile` (first rendered path ending in the suffix) and `lineValue` (value of `KEY=` in a dotenv) in the test file if no equivalent exists; reuse `TestGoldenTree`'s rendering call for `renderFixture`.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/render/ -run Invite`
Expected: FAIL.

- [ ] **Step 3: Implement**

`internal/render/invite.go`:

```go
package render

import (
	"fmt"
	"os"
	"strconv"

	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// inviteValues is what the invite kind's templates need beyond any app's.
type inviteValues struct {
	IdentityURL, APIKey, Accent, CoC string
	Settings                         map[string]string
}

// inviteValuesFor reads the identity app the invite app names, never one
// found by search, and the code of conduct from beside paisans.yaml.
func (p *planner) inviteValuesFor(name string) (inviteValues, error) {
	app := p.cfg.Apps[name]
	idp, _ := app.Settings["identity_app"].(string)
	idApp, ok := p.cfg.Apps[idp]
	if !ok {
		return inviteValues{}, fmt.Errorf("apps.%s.settings.identity_app: %q is not an app", name, idp)
	}
	setting, _ := app.Settings["code_of_conduct"].(string)
	coc, err := os.ReadFile(kinds.InviteCoCPath(p.cfg.Path, setting))
	if err != nil {
		return inviteValues{}, fmt.Errorf("apps.%s.settings.code_of_conduct: %w", name, err)
	}
	v := inviteValues{
		IdentityURL: "https://" + idApp.Hostname,
		APIKey:      p.appSecret(idp, "static_api_key"),
		CoC:         string(coc),
		Settings:    map[string]string{},
	}
	if a, ok := idApp.Settings["accent_color"].(string); ok && a != "default" {
		v.Accent = a
	}
	for k, def := range kinds.InviteSettingDefaults {
		v.Settings[k] = def
		switch x := app.Settings[k].(type) {
		case int:
			v.Settings[k] = strconv.Itoa(x)
		case bool:
			v.Settings[k] = strconv.FormatBool(x)
		}
	}
	return v, nil
}
```

Match how integers arrive in `Settings` (see Task 2's note). If `idApp.Hostname` is not the field name, use the one `identityProviderURL()` at `appview.go:565-572` uses.

`appview.go`: add `Invite inviteValues` to `appValues`; in `values()` beside the uptime branch:

```go
	if planned.Kind == config.KindInvite {
		iv, err := p.inviteValuesFor(planned.Name)
		if err != nil {
			return appValues{}, err
		}
		v.Invite = iv
	}
```

and in `oidcFor`'s switch:

```go
	case config.KindInvite:
		redirect = "https://" + planned.Hostname + "/auth/callback"
```

The invite service's issuer and API base both come from `v.Invite.IdentityURL`, which is built from the identity app's `hostname` in `paisans.yaml` (founder requirement 2026-10-12: the service never learns Pocket ID's address any other way). `INVITE_OIDC_ISSUER` and `INVITE_IDP_URL` are therefore always the same value.

`templates/invite/compose.yaml.tmpl`:

```yaml
# Rendered by paisans. Do not edit: `paisans apply` overwrites this file.
#
# {{ .App.Name }} has cluster placement: no Postgres service of its own, and a
# connection to the local HAProxy on {{ .DBHost }}:{{ .DBPort }}. Its state is
# all in that database, so the container has no volume.
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
    volumes:
      - {{ .DataPath }}/code-of-conduct.md:/etc/paisans-invite/code-of-conduct.md:ro
```

`templates/invite/code-of-conduct.md.tmpl`:

```
{{ .Invite.CoC }}
```

Check the trailing newline: if the template adds one, the rendered file differs from the source by one byte; trim in the template with `{{- .Invite.CoC -}}` and confirm byte equality in `TestInviteCoCRendered` (compare against the source file's contents).

`templates/invite/.env.secret.tmpl`:

```
# Rendered by paisans. Do not edit: `paisans apply` overwrites this file.
# Names are paisans-invite's environment contract.
INVITE_LISTEN_ADDR=:{{ .App.Port }}
INVITE_PUBLIC_URL={{ .PublicURL }}
INVITE_DATABASE_URL={{ .DSN }}
INVITE_SESSION_KEY={{ .Secret "session_key" }}
INVITE_EMAIL_HASH_KEY={{ .Secret "email_hash_key" }}
INVITE_OIDC_ISSUER={{ .Invite.IdentityURL }}
INVITE_OIDC_CLIENT_ID={{ .OIDC.ClientID }}
INVITE_OIDC_CLIENT_SECRET={{ .OIDC.ClientSecret }}
INVITE_MEMBER_GROUP={{ .OIDCMemberGroup }}
INVITE_ADMIN_GROUP={{ .OIDCAdminGroup }}
INVITE_IDP_URL={{ .Invite.IdentityURL }}
INVITE_IDP_API_KEY={{ .Invite.APIKey }}
INVITE_SMTP_HOST={{ .SMTP.Host }}
INVITE_SMTP_PORT={{ .SMTP.Port }}
INVITE_SMTP_USER={{ .SMTP.Username }}
INVITE_SMTP_PASSWORD={{ .SMTP.Password }}
INVITE_SMTP_FROM={{ .SMTP.FromAddress }}
INVITE_SMTP_TLS={{ if .SMTP.Secure }}tls{{ else }}starttls{{ end }}
INVITE_ACCENT_COLOR={{ .Invite.Accent }}
INVITE_QUOTA={{ index .Invite.Settings "invite_quota" }}
INVITE_REQUIRE_APPROVAL={{ index .Invite.Settings "require_approval" }}
INVITE_LIFETIME_DAYS={{ index .Invite.Settings "invite_lifetime_days" }}
INVITE_APPLICATION_WAIT_DAYS={{ index .Invite.Settings "application_wait_days" }}
INVITE_DECLINE_RETENTION_DAYS={{ index .Invite.Settings "decline_retention_days" }}
INVITE_LOGIN_LINK_DAYS={{ index .Invite.Settings "login_link_days" }}
INVITE_COC_PATH=/etc/paisans-invite/code-of-conduct.md
INVITE_TRUST_FORWARDED=true
```

Check `.DSN`'s format is a `postgres://` URL pgx accepts (it is what outline's `DATABASE_URL` uses); check how `OIDCMemberGroup`/`OIDCAdminGroup` are called (methods with or without arguments) in an existing template.

`templates/invite/caddy.snippet.tmpl`, copied from uptime's shape with nothing refused:

```
# Rendered by paisans. Do not edit: `paisans apply` overwrites this file.
#
# Routing for {{ .App.Name }}, imported by the host block for {{ .Hostname }}.
# Never gated: invitees reach /i/* before they have an account.
reverse_proxy{{ range .Upstreams }} {{ . }}{{ end }} {
{{- if .UpstreamElsewhere }}
	import upstream_single
{{- end }}
	header_up X-Forwarded-Proto https
}
```

- [ ] **Step 4: Regenerate goldens and read them**

Run: `go test ./internal/render -update && git diff --stat internal/render/testdata/`
Read every new file under `golden/*/srv/paisans/f2a9/invite/` by hand: the `.env` has no empty `INVITE_IDP_API_KEY`, `INVITE_DATABASE_URL` points at the site's HAProxy, the code of conduct matches the source byte for byte, and only cluster sites carry the app. Then `go test ./...`.
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: render the invite service's compose, environment, routing and code of conduct"
```

---

### Task 5: Example configuration and README

**Files:**
- Modify: `examples/paisans.example.yaml` (`auth` at `:272-296`; add the invite app after uptime at `:385-397`), `examples/secrets.example.yaml`, add `examples/code-of-conduct.md`
- Modify: `README.md`: a new `## Invites` section beside `## Monitoring` (`:501`); the per-kind OIDC table (`:5692`); the volumes table (`:5125-5130`); the mail paragraph (`:2038-2049`); the pinned-only/cluster-only text (`:239`); the Pocket ID signup paragraph (around `:5441` and `:5488`) so its example is `signup_default_groups: [members]` and `allow_user_signups: disabled`
- Test: `internal/secretsgen/examples_test.go:19` (already enforces the secrets example), the example-validates test

- [ ] **Step 1: Change the example**

In `auth`'s settings: `allow_user_signups: disabled` and `signup_default_groups: [members]`; keep `email_one_time_access_as_unauthenticated: true` (already there, and now required by `invite-recovery-disabled`). Add:

```yaml
  # Members bring new people in. The invitee agrees to the code of conduct
  # beside this file and chooses their details before any account exists;
  # with require_approval an admin approves first. See "Invites" in the README.
  invite:
    kind: invite
    hostname: invite.example.org
    placement: cluster
    settings:
      identity_app: auth
      invite_quota: 3
      require_approval: true
      code_of_conduct: code-of-conduct.md
```

`examples/code-of-conduct.md`: a two-line example that says it is an example and must be replaced with the community's own.

Add the invite app's keys and OIDC client to `examples/secrets.example.yaml` in the file's placeholder style.

- [ ] **Step 2: README `## Invites`**

Write it from the spec, in the README's voice (statements of what is, with file references), covering: what the service does; the settings table (same as the spec's); that `placement: cluster` is required and why; the two Pocket ID writes and that the service holds the deployment's static API key, so a leak is answered by `paisans secrets set apps.<auth>.static_api_key` and an apply of every site, then a check of Pocket ID for admins, clients and API keys nobody made; that `allow_user_signups` must be `disabled` and `email_one_time_access_as_admin` must not be `false`; the code of conduct file and that its SHA-256 is the version each person agreed to; `email_hash_key` and `session_key`, and what rotating each does (decline history lost; everyone signed out of the invite service). Add the invite row to each cross-kind table listed above.

- [ ] **Step 3: Run the whole suite**

Run: `go vet ./... && go test ./... && go run ./cmd/paisans validate --config examples/paisans.example.yaml`
Expected: PASS, and the example has no refusals.

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "docs: the invite kind in the example configuration and the README"
```

---

### Task 6: Pin the release and open the pull request

- [ ] **Step 1:** When `paisans-invite` `v0.1.0` exists, change the catalogue image to `ghcr.io/paisans-software/paisans-invite:0.1.0`, with a comment recording that the tag was checked against ghcr.io anonymously and on what date (the uptime entry's wording at `kinds.go:167`). Move the `RepositoryVolumes` entry to `ImageVolumes` for that exact reference if the file's convention is exact references for pinned tags. Regenerate goldens; `go test ./...`.
- [ ] **Step 2:** Push `feat/invite-kind` and open a pull request into `develop`, body in the repository's style with the `🤖 Generated with [Claude Code](https://claude.com/claude-code)` footer. Do not mention Outline pages or internal tickets in the title or body. Do not post comments or reviews. Report the PR link in the session; merging is the founder's call.
