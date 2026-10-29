# Welcoming new members: toolkit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The toolkit renders everything paisans-engagement-service needs to welcome newcomers, and hands the mbin and synapse blueprints the values they need from the engagement app.

**Architecture:** `render.Consumer` gains `Values`, filled in Go for an engagement consumer. The engagement blueprint consumes a new `forum` role (Mbin provides it in the catalog) with a shared `mbin_agent_client_secret`, and gains two settings. The room name default and alias localpart are computed once, in `internal/render/welcome.go`.

**Tech Stack:** Go, text/template, the reserved engagement blueprint, the test copy of the core catalog.

**Spec:** `docs/specs/2026-10-29-engagement-welcome.md`; contract `docs/superpowers/plans/2026-10-29-engagement-welcome-contract.md` (both on `docs/engagement-welcome-spec`).

## Global Constraints

- Names exactly as the contract's *Toolkit* section: `Values`, `community_room_name`, `welcome_email_daily_limit`, `mbin_agent_client_secret`, `forum`, `community_room_alias`, `mbin_agent_client_id`, `mbin_agent_username`, `engagement-agent`, and the seven `ENGAGEMENT_*` variables.
- Word list (whole word, any case): Community, Communities, Collective, Commons, Club, Society, Guild, Circle, Network, Group, Hub, Crew.
- Alias localpart: lowercase, each run outside `a-z0-9` becomes one `-`, ends trimmed of `-`.
- `welcome_email_daily_limit` default `50`, at least 1.
- Writing says "blueprint", describes current state only.
- Before the PR: `gofmt -l .` empty, `go vet ./...`, `go test ./...`.

## Review Focus

- Community name with no word from the list and trailing spaces ("Riverside ") gives "Riverside Community", not "Riverside  Community": test in Task 1.
- A word inside another word ("Clubhouse") does not count: test in Task 1.
- A room name that reduces to an empty alias ("!!!") is refused by validate, since Synapse cannot have alias `#:server`: test in Task 3.
- Deployment with no community name: the default derives from the digest's meta title fallback: test in Task 4.
- Operators map order: `operators:` is a YAML map, so emails are rendered in operator-name order (deterministic, golden-stable): test in Task 4.

---

### Task 1: Room name and alias

**Files:** Create `internal/render/welcome.go`, `internal/render/welcome_test.go`.

**Interfaces:** Produces `func CommunityRoomName(community string) string`, `func RoomAliasLocalpart(name string) string`.

- [ ] Test `TestCommunityRoomName`: "Riverside"→"Riverside Community"; "Riverside Guild"→"Riverside Guild"; "riverside community"→"riverside community"; "Riverside Clubhouse"→"Riverside Clubhouse Community"; " Riverside "→"Riverside Community"; "Fixture Community"→"Fixture Community".
- [ ] Test `TestRoomAliasLocalpart`: "Riverside Community"→"riverside-community"; "  Café & Co.  "→"caf-co"; "A--B"→"a-b"; "!!!"→"".
- [ ] Run, fail, implement (`strings.Fields` for words, `strings.EqualFold`), pass, commit.

### Task 2: Engagement blueprint and the forum role

**Files:** Modify `internal/blueprints/reserved/engagement/blueprint.yaml`, `internal/blueprints/engagement_test.go`, `internal/blueprints/testdata/core/blueprints/mbin/blueprint.yaml`, `internal/render/testdata/secrets.fixture.yaml`.

- [ ] Extend `TestEngagementBlueprintConsumesAHomeserver` (rename `TestEngagementBlueprintConsumes`): consumes equals `[{matrix-homeserver, homeserver, optional, [mas_admin_client_secret]}, {forum, forum, optional, [mbin_agent_client_secret]}]`; `mbin_agent_client_secret` is a password, restart, self-rotated; settings `forum` string, `community_room_name` string (no default), `welcome_email_daily_limit` int default 50.
- [ ] Implement in the blueprint yaml; test catalog mbin gains `provides: [{role: forum, values: {api_url: "'http://' + app.mesh_address + ':8080'"}}]`; fixture secrets gain `digest.mbin_agent_client_secret: "fixture-not-a-secret-digest-mbin-agent"`. Pass, commit.

### Task 3: Validate rules

**Files:** Modify `internal/validate/engagement.go`, `internal/validate/engagement_test.go`.

- [ ] Test `TestEngagementCommunityRoomName`: "" , "   ", "a\tb", "!!!" refused with `engagement-community-room-name-invalid` at `apps.<app>.settings.community_room_name`; "Riverside Hall" passes.
- [ ] Test `TestEngagementWelcomeLimit`: 0, -1 refused with `engagement-welcome-limit-invalid` at `apps.<app>.settings.welcome_email_daily_limit`; 1, 50 pass.
- [ ] Implement (non-string or non-int left to `blueprint-setting-invalid`), pass, commit.

### Task 4: Consumer values and the engagement env

**Files:** Modify `internal/render/links.go`, `internal/render/engagement.go`, `internal/blueprints/reserved/engagement/.env.secret.tmpl`, `internal/render/links_test.go`, `internal/render/engagement_test.go`, goldens under `internal/render/testdata/golden*`.

**Interfaces:** Produces `Consumer{Name string; Secrets map[string]string; Values map[string]string}`; engagementValues gains `CommunityRoomName`, `CommunityRoomAlias`, `WelcomeDailyLimit int`, `OperatorEmails string`.

- [ ] Update `TestConsumersCarryTheSharedSecret` so `digest` carries `Values{community_room_alias: fixture-community, mbin_agent_client_id: engagement-agent, mbin_agent_username: community-bot}` and other consumers `Values{}`. Add `TestForumConsumersCarryTheMbinSecret` on `talk`'s `Consumers("forum")`.
- [ ] Test `TestEngagementWelcomeEnv`: `ENGAGEMENT_MBIN_API_URL` is the talk mesh address `:8080`, client id `engagement-agent`, secret the fixture, room name "Fixture Community", alias "fixture-community", limit 50, operators "alice@example.org,bob@example.org".
- [ ] Test `TestEngagementWelcomeSettings`: `community_room_name: "Riverside Hall"`, `welcome_email_daily_limit: 12` render through; room alias follows.
- [ ] Test `TestEngagementWithoutHomeserverRendersNoMatrix` also asserts no `ENGAGEMENT_COMMUNITY_ROOM_*` line.
- [ ] Implement; regenerate goldens with the package's update flag; review the diff; pass; commit.

### Task 5: README and changelog

- [ ] README engagement section: the two settings, the forum link and its shared secret, the seven variables, the two validate rules in the rule table; `.Consumers` entries carry `.Values`.
- [ ] CHANGELOG `[Unreleased]` bullet. Commit.
