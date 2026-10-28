# Engagement on Matrix: the contract between the three repositories

Spec: `docs/specs/2026-10-27-engagement-matrix.md`. Three plans implement it,
one per repository, and each relies on this page for the names the others
use. A plan may not rename anything here; a change to it is a change to all
three plans.

| Repo | Branch | Worktree |
|---|---|---|
| paisans-cli | `feat/engagement-matrix` | `src/paisans-stack/.worktrees/engagement-matrix` |
| paisans-engagement-service | `feat/matrix-bot` (from `develop`) | `src/paisans-engagement-service/.worktrees/matrix-bot` |
| paisans-blueprints | `feat/engagement-matrix` (from `main`) | `src/blueprints/.worktrees/engagement-matrix` |

Release order: toolkit, then service, then catalog.

## Toolkit: new blueprint schema (general, not engagement specific)

1. **`app.mesh_address` in `provides` expressions.** A CEL string: the mesh
   address of the site a pinned app runs on; `""` for a clustered app. Only
   a provider's `values` need it; adding it to `celAppIn` makes it available
   everywhere `celAppIn` is.
2. **`consumes[].optional: true`.** With the consume's setting unset, the
   consumer links to the deployment's one app providing the role, if there
   is exactly one; with none, the role is simply not linked; with several,
   validate refuses and names the setting. Without `optional`, behaviour is
   unchanged (setting unset means the consumer's own hostname).
3. **`consumes[].shared_secrets: [<name>, ...]`.** Each name is one of the
   consumer's own `secrets.generate` names. The secret is stored once, under
   the consumer. The provider's templates see it through `.Consumers`.
   `secrets rotate <consumer> <name>` re-renders and restarts the linked
   provider as well as the consumer.
4. **Template functions:**
   - `.HasLinked "<role>"` → `bool`: whether the role is linked.
   - `.LinkedApp "<role>"` → `string`: the providing app's name in the
     deployment; fails the render when the provider is outside the
     deployment or the role is not linked.
   - `.Consumers "<role>"` → `[]Consumer`, sorted by `Name`, for a provider:
     every app in the deployment whose blueprint consumes `<role>` and links
     to this app. `Consumer` has `Name string` and `Secrets map[string]string`
     (only that consumer's `shared_secrets`).
   - `.LinkedOffers "<role>" "<value>"` → `bool`: whether the linked
     provider declares that value. The engagement template guards its Matrix
     block on `.LinkedOffers "matrix-homeserver" "mas_admin_url"`, so a
     toolkit released before the catalog renders Matrix off, not a failure.
   - `stableULID` already exists and is used as-is on both sides.

## Toolkit: the reserved `engagement` blueprint

* `consumes: [{role: matrix-homeserver, setting: homeserver, optional: true, shared_secrets: [mas_admin_client_secret]}]`
* `secrets.generate` gains `mas_admin_client_secret` (password format, rotation re-renders both apps).
* A setting `bot_localpart`, default `digest-bot`, validated as a Matrix
  localpart (`[a-z0-9._=/-]+`, lowercase).
* `.env.secret.tmpl` renders, only when `.HasLinked "matrix-homeserver"`:

| Variable | Value |
|---|---|
| `ENGAGEMENT_MATRIX_URL` | `.Linked "matrix-homeserver" "base_url"` |
| `ENGAGEMENT_MATRIX_SERVER_NAME` | `.Linked "matrix-homeserver" "server_name"` |
| `ENGAGEMENT_MAS_ADMIN_URL` | `.Linked "matrix-homeserver" "mas_admin_url"` |
| `ENGAGEMENT_MAS_CLIENT_ID` | `stableULID (printf "%s-mas-admin" .App.Name)` |
| `ENGAGEMENT_MAS_CLIENT_SECRET` | `.Secret "mas_admin_client_secret"` |
| `ENGAGEMENT_MAS_UPSTREAM_PROVIDER` | `stableULID (.LinkedApp "matrix-homeserver")` |
| `ENGAGEMENT_MATRIX_BOT_LOCALPART` | the `bot_localpart` setting |

  Unlinked: none of these lines is rendered.
* The gateway forwards `/d/*` as well as `/u/*` and `/healthz`.

## Catalog: the `synapse` blueprint

* `provides.matrix-homeserver.values` gains
  `mas_admin_url: "'http://' + app.mesh_address + ':18010'"`.
* `mas.yaml` gains a second listener `admin`, resource `adminapi` only,
  bound to `[::]:18010` inside the container; compose publishes it on the
  mesh address only. The `web` listener does not mount `adminapi`.
* The admin client's token comes from `/oauth2/token` on the public web
  listener (`ENGAGEMENT_MATRIX_URL`'s hostname); the bot's own token from
  `/api/admin/v1/personal-sessions`. Minimum MAS: 1.5.0.
* For each `.Consumers "matrix-homeserver"` entry that has
  `mas_admin_client_secret` in its `Secrets`, `mas.yaml` declares a static
  client with `client_id: stableULID (printf "%s-mas-admin" .Name)`,
  `client_auth_method: client_secret_basic`, that secret, and whatever MAS
  needs to let it use the client credentials grant with `urn:mas:admin`
  (policy data `admin_clients`, verified against MAS source).
* The compose publishes 18010 on the mesh address only.
* The gateway snippet refuses `/api/admin` and everything under it with 404
  on every hostname of the app.
* The catalog release requires the toolkit release that has items 1-4.

## Service: environment

The seven variables above. The bot starts only when all seven are set, and
logs once that Matrix is off otherwise. The web digest is at `/d/{token}`.
