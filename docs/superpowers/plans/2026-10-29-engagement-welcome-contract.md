# Welcoming new members: the cross-repo contract

> **For agentic workers:** this is not a task list. It fixes every name that
> crosses a repository boundary for `docs/specs/2026-10-29-engagement-welcome.md`.
> Each repo's plan implements its side and uses these names exactly as written.
> A plan that finds one of them impossible stops and reports it; it never
> renames on its own.

**Spec:** `docs/specs/2026-10-29-engagement-welcome.md`

| Repo | Plan |
|------|------|
| `Paisans-Software/mbin` | `src/mbin-paisans/plans/14-agent-role.md` (workspace, never committed) |
| `Paisans-Software/paisans-cli` | `docs/superpowers/plans/2026-10-29-engagement-welcome-toolkit.md` |
| `Paisans-Software/paisans-blueprints` | `docs/superpowers/plans/2026-10-29-engagement-welcome-catalog.md` (in the catalog repo) |
| `Paisans-Software/paisans-engagement-service` | `docs/superpowers/plans/2026-10-29-engagement-welcome.md` (in the service repo) |

No release is cut in any repo. Pull requests merge into each repo's
integration branch only.

## Mbin fork

**Role.** `ROLE_AGENT`, stored in `User::$roles` like `ROLE_ADMIN`. Helper
`User::isAgent(): bool`.

**Console command.** `mbin:user:agent <username> [--remove]`: grants or
revokes `ROLE_AGENT`, mirroring `mbin:user:admin`.

**OAuth scopes** (listed in `config/packages/league_oauth2_server.yaml`):

| Scope | Security role |
|-------|---------------|
| `agent:magazine:subscribe` | `ROLE_OAUTH2_AGENT:MAGAZINE:SUBSCRIBE` |
| `agent:magazine:create` | `ROLE_OAUTH2_AGENT:MAGAZINE:CREATE` |

**Endpoints.** Both require `ROLE_AGENT` on the token's user *and* the scope.

| Method and path | Scope | Success |
|-----------------|-------|---------|
| `PUT /api/agent/users/{user_id}/magazines/{magazine_id}/subscription` | `agent:magazine:subscribe` | `204`, idempotent |
| `DELETE /api/agent/users/{user_id}/magazines/{magazine_id}/subscription` | `agent:magazine:subscribe` | `204`, idempotent |
| `POST /api/agent/magazines` | `agent:magazine:create` | `201`, body: Mbin's existing `MagazineResponseDto` |

`POST /api/agent/magazines` takes the same JSON body as the existing
`POST /api/moderate/magazine/new` (`name`, `title`, `description`,
`isAdult`, `isPostingRestrictedToMods`, ...) and creates the magazine with
the agent as owner, ignoring `MBIN_RESTRICT_MAGAZINE_CREATION`.

Status codes:

* `401` no or bad token. `403` token lacks the scope, or its user lacks
  `ROLE_AGENT`.
* `404` user or magazine does not exist.
* `400` target refused: the magazine is private, the user is remote,
  banned, deleted, an admin, or an agent. Body names the reason.

**Declared agent client** (fork-only, `fork/agent_client_bootstrap`). Read
from the environment by `mbin:agent:sync`, which `docker/docker-entrypoint.sh`
runs after migrations when `MBIN_AGENT_CLIENT_ID` is non-empty:

| Variable | Meaning |
|----------|---------|
| `MBIN_AGENT_CLIENT_ID` | OAuth client identifier; empty means no agent client |
| `MBIN_AGENT_CLIENT_SECRET` | the client secret, plaintext |
| `MBIN_AGENT_CLIENT_SCOPES` | space-separated scopes the client may request |
| `MBIN_AGENT_USERNAME` | the client's bot account's username |
| `MBIN_AGENT_CONTACT_EMAIL` | the client's contact email |

The command creates or updates the client (client-credentials grant only,
confidential) and its bot account so they match, grants `ROLE_AGENT` to that
account, and touches nothing else.

## Toolkit (`paisans-cli`)

**Consumer values.** `render.Consumer` gains
`Values map[string]string`. A blueprint declares the values its consumers
see; for engagement the toolkit computes them in Go.

**Engagement settings**:

| Setting | Type | Default | Refused when |
|---------|------|---------|--------------|
| `community_room_name` | string | derived from `community.name` (spec, *The community room*) | blank, holds a control character, or has no letter or digit (`engagement-community-room-name-invalid`); unset, the derived name is refused at `community.name` when it holds a control character |
| `welcome_email_daily_limit` | integer | `50` | not an integer (`blueprint-setting-invalid`, e.g. `"12"` or `2.5`), or less than 1 (`engagement-welcome-limit-invalid`) |

**Room alias localpart** computed once, in Go: lowercase, every run of
characters outside `a-z0-9` replaced by one `-`, leading and trailing `-`
trimmed. "Riverside Community" is `riverside-community`.

**Engagement as Mbin's consumer.** The `mbin` blueprint provides role
`forum`. The engagement blueprint consumes
`{role: forum, optional: true, shared_secrets: [mbin_agent_client_secret]}`
and generates `mbin_agent_client_secret` (format password, rotation cost
restart, self-rotated). The existing `EngagementSource()` read path is left
as it is.

**Consumer values the engagement app exposes**:

| Key | Value |
|-----|-------|
| `community_room_alias` | the alias localpart, e.g. `riverside-community` |
| `mbin_agent_client_id` | `engagement-agent` |
| `mbin_agent_username` | the engagement `bot_localpart` setting |

**Engagement `.env` additions**:

| Variable | Value |
|----------|-------|
| `ENGAGEMENT_MBIN_API_URL` | Mbin's mesh address, `http://<mesh>:8080`, where the service calls the API |
| `ENGAGEMENT_MBIN_AGENT_CLIENT_ID` | `engagement-agent` |
| `ENGAGEMENT_MBIN_AGENT_CLIENT_SECRET` | the shared secret |
| `ENGAGEMENT_COMMUNITY_ROOM_NAME` | the `community_room_name` setting or its default |
| `ENGAGEMENT_COMMUNITY_ROOM_ALIAS` | the alias localpart |
| `ENGAGEMENT_WELCOME_DAILY_LIMIT` | the `welcome_email_daily_limit` setting |
| `ENGAGEMENT_OPERATOR_EMAILS` | the deployment's operators' emails, comma-separated, in `operators:` order |

The three Mbin agent lines render only when the app links to a `forum`
provider. The two room lines render only inside the existing Matrix block.

## Catalog (`paisans-blueprints`)

* `mbin` blueprint: `provides: [{role: forum}]`. For each consumer that has
  `mbin_agent_client_id` in `Values`, its `.env` renders the five
  `MBIN_AGENT_*` variables: client id and username from `Values`, secret
  from `Secrets.mbin_agent_client_secret`, scopes
  `agent:magazine:subscribe agent:magazine:create read write moderate:magazine_admin:update`,
  contact email the deployment's admin contact the blueprint already
  renders. Requires the toolkit and Mbin versions that carry the above.
* `synapse` blueprint: for a consumer with `community_room_alias` in
  `Values`, `homeserver.yaml` renders
  `auto_join_rooms: ["#<alias>:<server_name>"]` and
  `autocreate_auto_join_rooms: false`.

## Service

Reads the seven new variables above. Mbin welcomes run when the three
`ENGAGEMENT_MBIN_*` agent variables are set; Matrix welcomes when the
existing Matrix variables and the two room variables are set; the daily
email when SMTP is set. Each logs one line when it is off.

Mbin bot token: `POST {ENGAGEMENT_MBIN_API_URL}/token`,
`grant_type=client_credentials`, the client id and secret, `scope` the
space-separated scopes above.

Welcome unsubscribe tokens carry purpose `welcome`; digest tokens keep their
current form and mean `digest`.
