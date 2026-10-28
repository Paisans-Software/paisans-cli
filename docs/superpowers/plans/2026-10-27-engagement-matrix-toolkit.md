# Engagement on Matrix: the toolkit's part, Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give blueprints optional links, secrets shared between a consumer and its provider, provider visibility of its consumers and a CEL mesh address, and use them so the reserved `engagement` blueprint renders the Digest Bot's Matrix variables and forwards `/d/*`.

**Architecture:** Link resolution moves into one function in `internal/blueprints` (`LinkOf`), which render, validate and `secrets rotate` all call, so the three never disagree about which app a consume leads to. Render exposes the result to templates as `.HasLinked`, `.LinkedApp` and `.Consumers`; a shared secret stays stored once, under the consumer, and the provider reads it through `.Consumers`. `secrets rotate` treats a shared secret as self rotated and adds the linked provider's stack to its users.

**Tech Stack:** Go, `text/template`, `cel.dev/cel-go`, `gopkg.in/yaml.v3`.

**Spec:** `docs/specs/2026-10-27-engagement-matrix.md`. **Contract (binding):** `docs/superpowers/plans/2026-10-27-engagement-matrix-contract.md`, sections *Toolkit: new blueprint schema* and *Toolkit: the reserved engagement blueprint*. Read both before Task 1.

## Global Constraints

- Every name in the contract is used exactly as written: `app.mesh_address`, `optional`, `shared_secrets`, `.HasLinked`, `.LinkedApp`, `.Consumers`, `Consumer{Name, Secrets}`, `mas_admin_client_secret`, `bot_localpart`, `homeserver`, `mas_admin_url`, and the seven `ENGAGEMENT_*` variables.
- `ENGAGEMENT_MAS_CLIENT_ID` is `stableULID (printf "%s-mas-admin" .App.Name)`; `ENGAGEMENT_MAS_UPSTREAM_PROVIDER` is `stableULID (.LinkedApp "matrix-homeserver")`. The synapse blueprint computes the same two on its side.
- `bot_localpart` defaults to `digest-bot` and must match `^[a-z0-9._=/-]+$`.
- Without `optional`, a consume behaves exactly as today: setting unset means the consumer's own hostname.
- `.Consumers` is sorted by `Name`; `Secrets` holds only that consumer's `shared_secrets`.
- Work only in `src/paisans-stack/.worktrees/engagement-matrix`, on `feat/engagement-matrix`. Never `git stash`.
- No em dashes in anything written (code, comments, docs, commit messages). Docs describe current state only: no "no longer", no rejected alternatives.
- Each `CHANGELOG.md` bullet is one sentence of at most 35 words, under `[Unreleased]` / `### Added` (create the heading above `### Changed` if absent).
- Commits follow `git log`'s house style (`feat:` / `docs:`, lowercase subject, a body saying why) and end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Tests run per package while a task is in progress; Task 7 runs `go test ./... && go vet ./... && gofmt -l .`.

## Contract addition needing approval

**`.LinkedOffers "<role>" "<value>"` → `bool`** (Task 3) and its use in the engagement `.env` template's guard (Task 4). The contract renders the Matrix block "only when `.HasLinked "matrix-homeserver"`". The release order is toolkit, then service, then catalog, and core floats to its newest release. In the window between the toolkit release and the catalog release, a deployment running engagement and synapse links to a synapse whose `provides` has no `mas_admin_url`, and `.Linked "matrix-homeserver" "mas_admin_url"` fails the render by design (`appValues.Linked`), which fails `deployment apply` for the whole deployment. Guarding the block on `.LinkedOffers "matrix-homeserver" "mas_admin_url"` as well renders no Matrix lines in that window, which the service reads as Matrix off. It renames nothing and the other two plans do not see it. **Confirm with the founder before executing Task 3 Step 3's `LinkedOffers` part; if refused, drop it and the guard, and the release order must change so the catalog ships before any deployment runs the new toolkit with both apps.**

## Notes shared with the catalog plan

- **The test core copy.** `internal/blueprints/testdata/core` is at v0.7.0, whose synapse offers no `mas_admin_url`. Task 1 adds the contract's line to the copy's synapse `provides` so goldens render the Matrix block. Once the catalog release carrying the synapse change (v0.9.0) is tagged, `scripts/fetch-core-testdata.sh v0.9.0` replaces the copy, and its `provides` must come out identical; regenerate the goldens then for whatever else that release changes.
- **`requires_toolkit` and branch builds.** The catalog gates its synapse on `requires_toolkit`. A build of this branch reports the `-dev` version (`0.3.0-dev`), which meets only what the last release meets (`blueprints.meets`), so an end to end run of this branch against a catalog requiring the new release skips that release. Run e2e against a local catalog copy (`CORE_CATALOG_DIR` / `PAISANS_CORE_CATALOG`) without the requirement, or after `make bump` to the release.
- **MAS's admin port 18010.** It is written into the synapse templates, not declared as a service port (one port per service, and `mas` has 18009), so `render.SiteListeners`' collision check does not see it. No toolkit change, the author's call: no blueprint or infrastructure listener uses 18010, the gateway never routes it, and it binds only the synapse site's mesh address.

## Review Focus

1. **The toolkit released before the catalog.** A synapse whose `provides` lacks `mas_admin_url` must leave the digest's `.env` without any Matrix line and the render must succeed (Task 4, `TestEngagementWithAnOlderHomeserverRendersNoMatrix`).
2. **A deployment with no homeserver.** The digest's `.env` must carry none of the seven variables and otherwise be what it is today (Task 4, `TestEngagementWithoutHomeserverRendersNoMatrix`).
3. **A shared secret not yet in the secrets file** (an existing deployment before `init` or apply fills it): `.Consumers` must leave the key out of `Secrets` rather than hand the provider an empty secret (Task 4, `TestConsumersLeaveOutAnUnfilledSecret`).
4. **A provider pinned to a site the consumer does not run on** (synapse on a gateway): rotation must still hand the provider the next value, in a step of its own, before the consumer's sites (Task 6, `TestSharedSecretReachesTheProviderFirst`).
5. **An existing non-optional consumer** (Element): setting unset still links to its own hostname, and `.Linked` still fails for an undeclared value (Task 2, `TestLinkOfWithoutOptionalIsUnchanged`; Task 3, `TestLinkedStillFailsForAnUndeclaredValue`).

---

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `internal/blueprints/cel.go` | modify | declare and fill `app.mesh_address` |
| `internal/blueprints/links.go` | modify | `Consume.Optional`, `Consume.SharedSecrets`, their Parse checks, and link resolution: `Link`, `LinkOf`, `ProvidersOf`, `ConsumersOf`, `SharedSecretProvider` |
| `internal/blueprints/blueprint.go` | modify | call the shared secret check after secrets are parsed; Parse's doc comment documents the link keys |
| `internal/blueprints/testdata/core/blueprints/synapse/blueprint.yaml` | modify | the test copy gains `mas_admin_url` |
| `internal/blueprints/links_test.go`, `engagement_test.go` | modify | tests |
| `internal/render/links.go` | create | `Linked` and `linked` moved from `appview.go`, plus `HasLinked`, `LinkedApp`, `LinkedOffers`, `Consumers`, `Consumer`, `consumersFor` |
| `internal/render/appview.go` | modify | `values` resolves links through `blueprints.LinkOf` and fills `linkedApps` and `consumers` |
| `internal/render/links_test.go` | create | internal (package `render`) tests of the template functions |
| `internal/validate/links.go` | create | `link-provider-ambiguous`, `link-shared-secret-outside` |
| `internal/validate/links_test.go` | create | tests |
| `internal/validate/validate.go` | modify | call `c.links()` beside `c.engagement()` |
| `internal/validate/engagement.go` | modify | `engagement-bot-localpart-invalid` |
| `internal/blueprints/reserved/engagement/*` | modify | consume, secret, settings, `.env` lines, `/d/*` |
| `internal/render/testdata/secrets.fixture.yaml`, `internal/render/testdata/golden*/` | modify | fixture secret, regenerated goldens |
| `internal/secretsrotate/appsecret.go`, `declared.go` | modify | shared secret users and cost |
| `README.md`, `docs/development.md`, `docs/specs/2026-10-24-engagement-digest.md`, `CHANGELOG.md` | modify | docs |

---

### Task 1: `app.mesh_address` in CEL

**Files:**
- Modify: `internal/blueprints/cel.go` (the `celEnv` doc list, `celEnv`, `celApp`, `celAppIn`)
- Modify: `internal/blueprints/testdata/core/blueprints/synapse/blueprint.yaml` (`provides.matrix-homeserver.values`)
- Test: `internal/blueprints/links_test.go`

**Interfaces:**
- Produces: the CEL variable `app.mesh_address` (`cel.StringType`), everywhere `celEnv` compiles: `cfg.Sites[app.Placement.Site].Address` for a pinned app in `celAppIn`, `""` for a clustered app and always `""` in `celApp`.
- Produces: the test core copy's synapse offers `mas_admin_url`, which Task 4 relies on.

- [ ] **Step 1: Write the failing test** in `links_test.go`

```go
// A provider's values may name the mesh address of the site a pinned app
// runs on; a clustered app has none, and neither has one outside the
// deployment.
func TestMeshAddressIsThePinnedSites(t *testing.T) {
	cfg := &config.Config{
		Sites: map[string]config.Site{"vm": {Address: "10.44.0.3"}},
		Apps: map[string]config.App{
			"chat": {Blueprint: "synapse", Hostname: "chat.example.org", Placement: config.Placement{Mode: config.PlacementPinned, Site: "vm"}},
		},
	}
	got, err := LinkValues(cfg, "chat", "matrix-homeserver")
	if err != nil || got["mas_admin_url"] != "http://10.44.0.3:18010" {
		t.Errorf("pinned: %v, %v", got, err)
	}
	cfg.Apps["chat"] = config.App{Blueprint: "synapse", Hostname: "chat.example.org", Placement: config.Placement{Mode: config.PlacementCluster}}
	if got, _ := LinkValues(cfg, "chat", "matrix-homeserver"); got["mas_admin_url"] != "http://:18010" {
		t.Errorf("clustered: %v", got["mas_admin_url"])
	}
	if v := celApp(config.App{Placement: config.Placement{Mode: config.PlacementPinned, Site: "vm"}})["app.mesh_address"]; v != "" {
		t.Errorf("with no deployment around it: %q", v)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/blueprints/ -run TestMeshAddressIsThePinnedSites`
Expected: FAIL, `mas_admin_url` absent (the copy lacks it) and then an undeclared reference to `app.mesh_address`.

- [ ] **Step 3: Implement**

In `cel.go`: add `cel.Variable("app.mesh_address", cel.StringType)`, `"app.mesh_address": ""` in `celApp`, and in `celAppIn` set it to `cfg.Sites[app.Placement.Site].Address` when `app.Placement.Mode == config.PlacementPinned`. Add the bullet to the doc list: `app.mesh_address, the mesh address of the site a pinned app runs on, empty for a clustered app and in a condition`.

In the synapse test copy, under `values:` after `server_name`, add:

```yaml
      # MAS's admin listener, on the mesh only, which an engagement app
      # reads to look members up and to run its bot.
      mas_admin_url: "'http://' + app.mesh_address + ':18010'"
```

This copy is ahead of `core.release` (v0.7.0) until the catalog release that carries the same line; `scripts/fetch-core-testdata.sh <that tag>` then replaces it with no diff in this file's `provides`.

- [ ] **Step 4: Run the package**

Run: `go test ./internal/blueprints/...`
Expected: PASS.

- [ ] **Step 5: Commit** `feat: blueprints read app.mesh_address in provides expressions` (body: a provider pinned to a site can offer an address on the mesh, which a consumer reaches without the gateway; the test core copy's synapse offers mas_admin_url as the catalog release will).

---

### Task 2: `optional` and `shared_secrets` on a consume, and one link resolver

**Files:**
- Modify: `internal/blueprints/links.go`
- Modify: `internal/blueprints/blueprint.go` (`spec.def`, after `secretSpecs` and the heartbeat; Parse's doc comment)
- Test: `internal/blueprints/links_test.go`

**Interfaces:**
- Produces, in `links.go`:

```go
type Consume struct {
	Role          string   `yaml:"role"`
	Setting       string   `yaml:"setting"`
	Optional      bool     `yaml:"optional"`
	SharedSecrets []string `yaml:"shared_secrets"`
}

// Link is where one app's consume of a role leads.
type Link struct {
	Role     string
	Linked   bool   // false only for an optional consume with no provider
	Provider string // the providing app's name; "" for a provider outside the deployment
	Hostname string // the provider's hostname
}

func LinkOf(cfg *config.Config, app string, c Consume) (Link, error)
func ProvidersOf(cfg *config.Config, role string) []string            // apps providing role, in cfg.AppNames() order
func ConsumersOf(cfg *config.Config, provider, role string) []string  // apps whose consume of role links to provider, sorted
func SharedSecretProvider(cfg *config.Config, app, key string) (string, bool) // the in-deployment provider key is shared with
```

- `LinkOf` rules: the setting is `SettingsSeen(app)[c.Setting]` as a non-empty string. Set: the app among `cfg.AppNames()` whose `Hostname` equals it and whose blueprint provides `c.Role`, else `Provider: ""` with that hostname. Unset and not optional: the same, with the consumer's own hostname. Unset and optional: `ProvidersOf` has 0 → `Link{Role, Linked: false}`; 1 → it; more → error `apps.<app>.settings.<setting>: unset, and <n> apps provide <role> (<a>, <b>); name one by its hostname`.
- `ConsumersOf` and `SharedSecretProvider` skip an app whose `LinkOf` errors (validate refuses it).
- Parse refusals, each prefixed `consumes[i]`:
  - `.role: <role> is consumed twice` (a template reads one link per role)
  - `.shared_secrets[j]: <name> is not one of secrets.generate; a shared secret is the consumer's own`
  - `.shared_secrets[j]: <name> is not in secrets.self_rotated; rotating a shared secret re-renders the consumer and its provider, as a self rotated secret is`
  - `.shared_secrets[j]: <name> is shared twice`

- [ ] **Step 1: Write the failing tests** in `links_test.go`

```go
func TestASharedSecretMistakeIsRefused(t *testing.T) {
	secrets := "secrets:\n  generate:\n    - {name: s, format: password}\n  self_rotated: [s]\n"
	for change, want := range map[string]string{
		"consumes:\n  - {role: matrix-homeserver, setting: h, optional: true, shared_secrets: [s]}\n" + secrets:          "",
		"consumes:\n  - {role: matrix-homeserver, setting: h, shared_secrets: [nope]}\n" + secrets:                      "consumes[0].shared_secrets[0]: nope is not one of secrets.generate",
		"consumes:\n  - {role: matrix-homeserver, setting: h, shared_secrets: [s]}\nsecrets:\n  generate: [s]\n":         "is not in secrets.self_rotated",
		"consumes:\n  - {role: matrix-homeserver, setting: h, shared_secrets: [s, s]}\n" + secrets:                      "is shared twice",
		"consumes:\n  - {role: matrix-homeserver, setting: h}\n  - {role: matrix-homeserver, setting: i}\n":           "consumes[1].role: matrix-homeserver is consumed twice",
	} {
		_, err := Parse("example", []byte(minimal+change), always)
		switch {
		case want == "" && err != nil:
			t.Errorf("%q: %v", change, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%q: got %v, want %q", change, err, want)
		}
	}
}

func TestLinkOfAnOptionalConsume(t *testing.T)            // 0 providers: Linked false, no error; 1: Provider "chat", Hostname "chat.example.org"; 2 (chat, chat2): error containing "unset, and 2 apps provide matrix-homeserver (chat, chat2)"; setting "chat2.example.org": Provider "chat2"; setting "matrix.example.net": Linked true, Provider "", Hostname "matrix.example.net"
func TestLinkOfWithoutOptionalIsUnchanged(t *testing.T)   // Element-shaped consume, setting unset, one synapse elsewhere: Linked true, Provider "", Hostname is the consumer's own
func TestConsumersOfAndSharedSecretProvider(t *testing.T) // a registered test/consumer blueprint (optional, shared_secrets [s]) as apps "b" and "a" plus one synapse "chat": ConsumersOf(cfg, "chat", "matrix-homeserver") == [a b]; SharedSecretProvider(cfg, "a", "s") == ("chat", true); SharedSecretProvider(cfg, "a", "other") is false; with chat removed, SharedSecretProvider is false
```

Build `cfg` with `config.Config{Apps: ...}` and the core `synapse`; register the consumer blueprint with `register("test/consumer", d, t.TempDir())` and clean it up as `TestLockedSettingsFollowTheirConditions` does.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/blueprints/ -run 'SharedSecret|LinkOf|ConsumersOf'`
Expected: FAIL to compile (`LinkOf` undefined, unknown field `optional`).

- [ ] **Step 3: Implement** the types and functions above in `links.go`; `checkSharedSecrets(consumes []Consume, generate, selfRotated []string) error` called from `spec.def` after `generate` is built; the duplicate role check in `checkLinks`.

Rewrite the `Consume` doc comment to say what each field means, and add to Parse's doc comment a paragraph on links: `provides: [{role, values}]`, each value a string CEL expression over the app (`app.mesh_address` included); `consumes: [{role, setting, optional, shared_secrets}]`, where `setting` names the provider's hostname, `optional` means an unset setting links to the deployment's one provider of the role or to none, and `shared_secrets` names the consumer's own self rotated secrets that its provider's templates read through `.Consumers`.

- [ ] **Step 4: Run the package**

Run: `go test ./internal/blueprints/...`
Expected: PASS.

- [ ] **Step 5: Commit** `feat: a consume may be optional and share secrets with its provider`.

---

### Task 3: Template functions over links

**Files:**
- Create: `internal/render/links.go` (move `Linked` and `linked` here from `appview.go`)
- Modify: `internal/render/appview.go` (`appValues` fields, the consume loop in `values`)
- Create: `internal/render/links_test.go` (package `render`)
- Modify: `docs/development.md` (the *Links between apps* paragraph under *A kind ships a blueprint directory*), `CHANGELOG.md`

**Interfaces:**
- Consumes: `blueprints.LinkOf`, `blueprints.ConsumersOf`, `blueprints.Consumes`, `blueprints.Provides`, `blueprints.LinkValues`, `blueprints.LinkValuesAt` (Task 2).
- Produces, on `appValues`:

```go
type Consumer struct {
	Name    string
	Secrets map[string]string
}

func (v appValues) HasLinked(role string) bool
func (v appValues) LinkedApp(role string) (string, error)
func (v appValues) LinkedOffers(role, value string) bool          // contract addition, see above
func (v appValues) Consumers(role string) ([]Consumer, error)
```

- New unexported fields: `linkedApps map[string]string` (role to provider app name, `""` outside) and `consumers map[string][]Consumer` (role to consumers, one entry per role the blueprint provides, even when empty).
- `values`: for each consume, `LinkOf`; an error fails the render as `<app>: <err>`; unlinked adds nothing; linked fills `links` from `LinkValues` (in the deployment) or `LinkValuesAt` (outside) and `linkedApps`. For each role in `Provides`, `consumers[role]` is `ConsumersOf` mapped to `Consumer`, with `Secrets` holding each of that consumer's `shared_secrets` for the role whose value in `p.secrets.Apps[consumer]` is a non-empty string.
- Errors: `.Linked` on an unlinked role says `the role <role> is not linked: guard .Linked with .HasLinked` when the blueprint consumes it and keeps today's message otherwise; `.LinkedApp` fails `the role <role> is not linked` and `the provider of <role> at <hostname> is outside the deployment, so it has no app name`; `.Consumers` fails `the blueprint reads .Consumers "<role>", a role it does not provide`.

- [ ] **Step 1: Write the failing tests** in `internal/render/links_test.go`. A helper `valuesOf(t, cfg, secrets, app) appValues` builds `newPlanner`, `placeApps`, `plannedFor` and `values`. Load `testdata/deployment.yaml` and `testdata/secrets.fixture.yaml` with `config.Load` and `config.LoadSecrets`,.

```go
func TestHasLinkedAndLinkedApp(t *testing.T)                  // web (element, homeserver: chat.example.org): HasLinked true, LinkedApp "chat"; HasLinked("nothing") false
func TestLinkedAppOutsideTheDeploymentFails(t *testing.T)     // web.settings.homeserver = "matrix.example.net": HasLinked true, LinkedApp error contains "outside the deployment"
func TestConsumersOfAProvider(t *testing.T)                   // chat.Consumers("matrix-homeserver") == [{web, {}}] (digest joins it in Task 4)
func TestConsumersOfARoleNotProvidedFails(t *testing.T)       // web.Consumers("matrix-homeserver") errors "a role it does not provide"
func TestLinkedOffers(t *testing.T)                           // web: LinkedOffers("matrix-homeserver", "server_name") true, ("matrix-homeserver", "nothing") false, ("nothing", "x") false
func TestLinkedStillFailsForAnUndeclaredValue(t *testing.T)   // web.Linked("matrix-homeserver", "nothing") errors "offers no value"
```

The shared secret side of `.Consumers` and an optional consume with no provider are tested in Task 4, once `digest` consumes the role.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/render/ -run 'Linked|Consumers'`
Expected: FAIL to compile (`HasLinked` undefined).

- [ ] **Step 3: Implement** the functions and the `values` change as specified.

- [ ] **Step 4: Run the package**

Run: `go test ./internal/render/...`
Expected: PASS, golden tree unchanged (nothing consumes optionally yet).

- [ ] **Step 5: Docs.** In `docs/development.md`'s links paragraph add: `optional` and `shared_secrets`; `.HasLinked`, `.LinkedApp`, `.LinkedOffers` and `.Consumers`; that resolution is `blueprints.LinkOf`, read by render, validate and `secrets rotate`. Add to `CHANGELOG.md` under `### Added`:

`- Blueprints may mark a consumed role \`optional\` and share generated secrets with its provider through \`shared_secrets\`; templates read \`.HasLinked\`, \`.LinkedApp\` and \`.Consumers\`, and \`provides\` expressions read \`app.mesh_address\`.`

- [ ] **Step 6: Commit** `feat: templates see whether a role is linked, its provider, and a provider's consumers`.

---

### Task 4: The engagement blueprint on Matrix

**Files:**
- Modify: `internal/blueprints/reserved/engagement/blueprint.yaml`, `.env.secret.tmpl`, `caddy.snippet.tmpl`
- Modify: `internal/validate/engagement.go`, `internal/blueprints/engagement_test.go`, `internal/render/engagement_test.go`, `internal/validate/validate_test.go` (or a new `engagement_test.go` there)
- Modify: `internal/render/testdata/secrets.fixture.yaml` (`digest.mas_admin_client_secret: "fixture-not-a-secret-digest-mas-admin"`), golden trees under `internal/render/testdata/golden*`
- Modify: `README.md` (*The engagement digest*, *What `validate` refuses for the digest*, its secrets table), `docs/specs/2026-10-24-engagement-digest.md`, `CHANGELOG.md`

**Interfaces:**
- Consumes: Tasks 1 to 3.
- Produces, in `blueprint.yaml`:

```yaml
consumes:
  - {role: matrix-homeserver, setting: homeserver, optional: true, shared_secrets: [mas_admin_client_secret]}
settings:
  homeserver: {type: string}
  bot_localpart: {type: string, default: "digest-bot"}
# under secrets.generate:
    - {name: mas_admin_client_secret, format: password, rotation_cost: restart}
# secrets.self_rotated:
  self_rotated: ["digest_signing_key", "mas_admin_client_secret"]
```

  each with a comment saying why (the homeserver the Digest Bot runs on, unset meaning the deployment's one; the bot's localpart, a setting because MAS takes members' localparts from Pocket ID and the name may be taken; the admin client's secret, which the synapse app's `mas.yaml` declares beside this app's `.env`).
- `.env.secret.tmpl` gains, after the SMTP lines, a block rendered only when `and (.HasLinked "matrix-homeserver") (.LinkedOffers "matrix-homeserver" "mas_admin_url")`, with the seven lines exactly as the contract's table gives them, `ENGAGEMENT_MATRIX_BOT_LOCALPART={{ .Setting "bot_localpart" "digest-bot" }}`, and a comment saying that without a homeserver none is rendered and the service keeps Matrix off.
- `caddy.snippet.tmpl`: ``@public expression `path('/u/*') || path('/d/*') || (method('GET') && path('/healthz'))` ``, and its comment names the web digests at `/d/<token>`.
- `validate/engagement.go`: `engagement-bot-localpart-invalid`, refuse, key `apps.<app>.settings.bot_localpart`, hint `the bot's localpart is not a Matrix localpart`, when the setting is a string not matching `^[a-z0-9._=/-]+$` (a non-string is `blueprint-setting-invalid`'s already). Message gives the allowed characters and the default.

- [ ] **Step 1: Write the failing tests**

In `internal/blueprints/engagement_test.go`, extend `TestEngagementBlueprintParses`: `Consumes` is one `{matrix-homeserver, homeserver, true, [mas_admin_client_secret]}`; `mas_admin_client_secret` is in `AppSecrets`, `SecretOf` is `FormatPassword` with `CostRestart`, and in `SelfRotated`; `SettingsOf` has `homeserver` and `bot_localpart` with default `"digest-bot"`.

In `internal/render/engagement_test.go`:

```go
// The fixture's digest links to its one homeserver, chat on vm.
func TestEngagementMatrixEnv(t *testing.T) {
	env := planFiles(build(t))[digestEnvA]
	for key, want := range map[string]string{
		"ENGAGEMENT_MATRIX_URL":            "https://chat.example.org",
		"ENGAGEMENT_MATRIX_SERVER_NAME":    "example.org", // chat's wellknown hostname in the fixture; read it from deployment.yaml
		"ENGAGEMENT_MAS_ADMIN_URL":         "http://10.44.0.3:18010", // vm's address in deployment.yaml
		"ENGAGEMENT_MAS_CLIENT_ID":         blueprints.StableULID("digest-mas-admin"),
		"ENGAGEMENT_MAS_CLIENT_SECRET":     "fixture-not-a-secret-digest-mas-admin",
		"ENGAGEMENT_MAS_UPSTREAM_PROVIDER": blueprints.StableULID("chat"),
		"ENGAGEMENT_MATRIX_BOT_LOCALPART":  "digest-bot",
	} {
		if got := envValue(env, key); got != want {
			t.Errorf("%s=%s, want %s", key, got, want)
		}
	}
}
func TestEngagementBotLocalpartSetting(t *testing.T)               // digest settings.bot_localpart "weekly": ENGAGEMENT_MATRIX_BOT_LOCALPART=weekly
func TestEngagementUpstreamProviderMatchesMAS(t *testing.T)        // ENGAGEMENT_MAS_UPSTREAM_PROVIDER equals the upstream provider id in chat's rendered mas.yaml (the `- id:` under upstream_oauth2)
func TestEngagementWithoutHomeserverRendersNoMatrix(t *testing.T)  // delete chat and web: no line of the digest .env starts ENGAGEMENT_MATRIX_ or ENGAGEMENT_MAS_; every key TestEngagementEnv checks is unchanged
func TestEngagementWithAnOlderHomeserverRendersNoMatrix(t *testing.T) // package render (links_test.go): digest's values with links["matrix-homeserver"]["mas_admin_url"] deleted, p.renderFile("engagement/.env.secret.tmpl", v) succeeds and holds no ENGAGEMENT_MAS_ line
```

In `internal/render/links_test.go` (package `render`, from Task 3):

```go
func TestConsumersCarryTheSharedSecret(t *testing.T)       // chat.Consumers("matrix-homeserver") == [{digest, {mas_admin_client_secret: fixture-not-a-secret-digest-mas-admin}}, {web, {}}]
func TestConsumersLeaveOutAnUnfilledSecret(t *testing.T)   // the secret deleted from the secrets: digest is listed and its Secrets lacks the key
func TestAnOptionalConsumeWithNoProvider(t *testing.T)     // chat and web deleted: digest HasLinked false, LinkedApp errors "is not linked"
```

Extend `TestEngagementSnippetProxiesUnsubscribeAndHealth`'s wanted line to the new `@public` expression, and rename it `TestEngagementSnippetProxiesItsThreePaths`.

In validate: `TestEngagementBotLocalpart`: `"Digest"`, `"digest bot"`, `"bot@x"` each refused at `apps.digest.settings.bot_localpart`; `"digest-bot"`, `"a.b_c=d/e-1"` not; and `TestEngagementFixtureIsQuiet` still passes.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/blueprints/ ./internal/render/ ./internal/validate/ -run Engagement`
Expected: FAIL (no consume, no Matrix lines, no rule).

- [ ] **Step 3: Implement** the blueprint, template, snippet and rule changes, and the fixture secret.

- [ ] **Step 4: Regenerate the goldens and read the diff**

Run: `go test ./internal/render/ -run Golden -update && git diff --stat internal/render/testdata`
Expected: the digest `.env` on home-a and home-b gain the seven lines, the gateway's `digest.caddy` gains `path('/d/*')`, and any blueprint file hash recorded for synapse moves. Nothing else. Then `go test ./internal/render/...` PASSES without `-update`.

- [ ] **Step 5: Run the packages**

Run: `go test ./internal/blueprints/... ./internal/render/... ./internal/validate/...`
Expected: PASS.

- [ ] **Step 6: Docs**

`README.md`, *The engagement digest*: a bullet **On Matrix.** saying the app consumes the `matrix-homeserver` role, as Element does: with one homeserver in the deployment it links to it, with several `settings.homeserver` names one by hostname, with none the bot is off and no Matrix variable is rendered; that the bot is then on and DMs each member who has a Matrix account linked to the same Pocket ID subject (pointing to `docs/specs/2026-10-27-engagement-matrix.md`); that its localpart is `settings.bot_localpart`, `digest-bot` by default; that the MAS admin client it uses is declared by the synapse app with a secret the two share; and that a synapse blueprint which does not offer `mas_admin_url` leaves the bot off. Update **What the gateway forwards** to add `/d/*`, the web digests the DM links to. Add the row `engagement-bot-localpart-invalid` to its validate table and a secrets row for `apps.<engagement>.mas_admin_client_secret` (the MAS admin client's secret, shared with the synapse app; rotating it re-applies both stacks, the synapse app's first).

`docs/specs/2026-10-24-engagement-digest.md`: the non goal becomes **No web interface beyond the web digest**, saying the service publishes the unsubscribe link and the web digest, the one page a member reads, linked from the Matrix message (`docs/specs/2026-10-27-engagement-matrix.md`, *The web digest*), and keeping the rest of the list. *Who receives one* gains a closing paragraph: the same members may also receive the week on Matrix, from the Digest Bot, and who qualifies for that is in `docs/specs/2026-10-27-engagement-matrix.md`, *Who qualifies for Matrix*.

`CHANGELOG.md` under `### Added` (Task 5 extends the second bullet):

`- The \`engagement\` blueprint renders the Digest Bot's homeserver, MAS admin client and \`bot_localpart\` when the deployment runs a \`matrix-homeserver\`, and the gateway forwards its web digests at \`/d/*\`.`

`- \`validate\` refuses an engagement \`bot_localpart\` that is not a Matrix localpart.`

- [ ] **Step 7: Commit** `feat: the engagement app runs the Digest Bot when the deployment has a homeserver`.

---

### Task 5: Validate the links a catalog author can get wrong

**Files:**
- Create: `internal/validate/links.go`, `internal/validate/links_test.go`
- Modify: `internal/validate/validate.go` (call `c.links()` next to `c.engagement()`), `CHANGELOG.md`

**Interfaces:**
- Consumes: `blueprints.Consumes`, `blueprints.LinkOf`, `blueprints.ProvidersOf`, `blueprints.SettingsSeen`.
- Produces two refusals, for each app and each consume of its blueprint:
  - `link-provider-ambiguous`, key `apps.<app>.settings.<setting>`, hint `several apps provide the role this app links to`: an optional consume, setting unset, `ProvidersOf` longer than 1. Message names the role, every provider's hostname, and says to set the setting to one of them.
  - `link-shared-secret-outside`, key `apps.<app>.settings.<setting>` when set, `apps.<app>` otherwise, hint `a shared secret needs its provider in this deployment`: a consume with `shared_secrets`, `LinkOf` linked with `Provider == ""`. Message names the hostname and the role, says the secret can be handed only to an app this deployment renders, and lists the providers' hostnames (or says to remove the setting when there are none).

- [ ] **Step 1: Write the failing tests** in `links_test.go`, starting from `load(t, "engagement-valid")` and adding synapse apps in Go, pinned to `home-a` and `home-b`:

```go
func TestSeveralHomeserversNeedTheSetting(t *testing.T)     // two synapse apps, digest with no settings: refusedFor(result, "link-provider-ambiguous", "apps.digest.settings.homeserver"); setting it to one's hostname clears it
func TestAHomeserverSettingNamingNoHomeserver(t *testing.T) // one synapse, digest settings.homeserver "matrix.example.net": refusedFor(result, "link-shared-secret-outside", "apps.digest.settings.homeserver"), message contains "matrix.example.net" and the synapse's hostname
func TestOneHomeserverIsQuiet(t *testing.T)                 // one synapse, no setting: neither rule fires
func TestElementOutsideTheDeploymentIsStillAllowed(t *testing.T) // an element app whose homeserver names a host outside: neither rule fires (no shared secret, not optional)
```

These rely on Task 4's engagement consume.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/validate/ -run 'Homeserver|Element'`
Expected: FAIL (no such rule).

- [ ] **Step 3: Implement** `func (c *checker) links()` in `links.go`.

- [ ] **Step 4: Run the package**

Run: `go test ./internal/validate/...`
Expected: PASS.

- [ ] **Step 5: Docs.** Add the rows `link-provider-ambiguous` and `link-shared-secret-outside` to README's *What `validate` refuses for the digest* table (each says it applies to any blueprint's link, the engagement app's being the one that shares a secret), and replace Task 4's validate bullet in `CHANGELOG.md` with:

`- \`validate\` refuses an optional link with several providers and no setting, a link sharing a secret with a provider outside the deployment, and an engagement \`bot_localpart\` that is not a Matrix localpart.`

- [ ] **Step 6: Commit** `feat: validate refuses an ambiguous optional link and a shared secret leaving the deployment`.

---

### Task 6: `secrets rotate` hands a shared secret to the provider too

**Files:**
- Modify: `internal/secretsrotate/appsecret.go` (`appSelf.Users`), `internal/secretsrotate/declared.go` (`CostOf`)
- Test: `internal/secretsrotate/appsecret_test.go`
- Modify: `README.md` (*What each secret costs* table), `CHANGELOG.md`

**Interfaces:**
- Consumes: `blueprints.SharedSecretProvider` (Task 2), `render.AppSites`.
- `appSelf.Users`: when `SharedSecretProvider(e.Cfg, r.app, r.key)` names a provider, the uses are each of the provider's sites first, each step holding the provider's stack followed by the consumer's when the consumer runs there too, then each remaining consumer site in `render.AppSites` order with the consumer's stack alone. Otherwise unchanged (the `static_api_key` branch stays).
- `CostOf`: a key with a shared provider and `rotation_cost: restart` costs `Downtime: "the <app> and <provider> stacks, for one restart on each site in turn"`.

- [ ] **Step 1: Write the failing tests** in `appsecret_test.go`, adding `"digest": {"mas_admin_client_secret"}` to a copy of `selfKeysFor` used by these tests only and a `digest` engagement app with cluster placement to `newSelfWorld`'s config within the test:

```go
func TestSharedSecretReachesTheProviderFirst(t *testing.T)
// chat (synapse) pinned home-b, digest clustered on home-a and home-b:
// usesOf == ["home-b:chat,digest", "home-a:digest"];
// with chat pinned to home-c (no apps role): ["home-c:chat", "home-a:digest", "home-b:digest"];
// a full w.rotate leaves w.presented equal for home-b/chat and every digest site, all the next value.
func TestSharedSecretWithoutAProviderIsTheConsumersAlone(t *testing.T)
// chat deleted: usesOf == ["home-a:digest", "home-b:digest"]
func TestSharedSecretCostNamesBothStacks(t *testing.T)
// CostOf(cfg, "digest", "mas_admin_client_secret").Downtime == "the digest and chat stacks, for one restart on each site in turn"; with chat deleted, "the digest stack, for one restart on each site in turn"
```

Also `AppSelfBlueprint("mas_admin_client_secret")` is `config.BlueprintEngagement`, and `rotatorFor("apps.digest.mas_admin_client_secret", ...)` in `cmd/paisans` gives an `AppSelf` (add the case to an existing `cmd/paisans` rotate test if one lists rotators by path).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/secretsrotate/ -run SharedSecret`
Expected: FAIL (users are the consumer's alone; cost names one stack).

- [ ] **Step 3: Implement** as specified.

- [ ] **Step 4: Run the packages**

Run: `go test ./internal/secretsrotate/... ./cmd/paisans/...`
Expected: PASS.

- [ ] **Step 5: Docs.** Add a row to README's *What each secret costs*: `| engagement \`mas_admin_client_secret\`, and any secret a blueprint shares with its provider | MAS, through the synapse app's \`mas.yaml\` | the engagement app; the synapse stack is re-applied with it, first | none; a restart of both stacks |`. `CHANGELOG.md` under `### Added`:

`- \`secrets rotate\` re-applies the linked provider with a consumer's shared secret, so \`apps.<engagement>.mas_admin_client_secret\` reaches the synapse app before the engagement app.`

- [ ] **Step 6: Commit** `feat: secrets rotate hands a shared secret to the provider before the consumer`.

---

### Task 7: Whole-tree verification

- [ ] **Step 1:** Run `go test ./... && go vet ./... && gofmt -l .` from the worktree root. Expected: tests and vet pass, `gofmt -l` prints nothing.
- [ ] **Step 2:** `git diff develop...HEAD -- README.md docs CHANGELOG.md` and read it: no em dash in an added line (`git diff develop...HEAD | grep '^+' | grep -c $'\xe2\x80\x94'` prints 0), no "no longer", no rejected alternative, every section name cited exists.
- [ ] **Step 3:** Check each new CHANGELOG bullet is at most 35 words: `sed -n '/## \[Unreleased\]/,/## \[0/p' CHANGELOG.md | grep '^- ' | awk '{print NF}'`, each at most 35.
- [ ] **Step 4:** Grep the diff for a real hostname, address or secret (`git diff develop...HEAD | grep -E '^\+' | grep -Ev 'example\.(org|net)|10\.44\.0\.|fixture-not-a-secret'` and read what remains).
- [ ] **Step 5:** If anything changed, commit `chore: verification fixes`; otherwise nothing to commit. Report the commands run and their results.
