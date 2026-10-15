# Wiki.js: what each group may do, and drift

Status: approved by the founder on 2026-10-14, not implemented. Amends
`docs/specs/2026-10-13-wikijs-blueprint.md`, sections *Groups* and *The
bootstrap*. Citations are Wiki.js at v2.5.315.

A new wiki has to be usable by its members the day it is deployed, keep what
only admins should read away from everyone else, and be public only where its
operator said so. It also has to say when someone has changed in Wiki.js a
setting the configuration owns, rather than leave the change in place until a
restart silently undoes it.

## How Wiki.js decides

A request passes only if the person's groups, taken together, hold the
permission globally **and** a page rule grants it on that path
(`server/core/auth.js:221-292`). Only `manage:system` skips the page rules.
Rules from every group a person is in are pooled: the matching rule with the
longest path wins, and at equal length a deny beats an allow
(`_applyPageRuleSpecificity`, `server/core/auth.js:416-436`). A rule that
does not hold the role being checked is skipped (`auth.js:252`).

Every operator is in both the admin group and the member group, so a deny rule
on `members` applies to admins as well. No seeded rule is a deny.

## What each group gets

| Group | Written | Permissions | Page rules |
|---|---|---|---|
| admin group | every run | every permission except `manage:system` and `manage:api` | `paisans-admin`: allow, `START ''`, every page role |
| member group | when the bootstrap creates it | `read:pages`, `write:pages`, `read:source`, `read:history`, `read:assets`, `write:assets`, `read:comments`, `write:comments` | `paisans-members`: allow, `REGEX ^(?!admins(/\|$))`, the same roles |
| `Guests` (ID 2) | every run | none; with `guest_paths`, `read:pages`, `read:assets`, `read:comments` | one allow `START <path>` per `guest_paths` entry, the same roles |
| `Administrators` (ID 1) | never | | |

Every page role is the list the page rule editor offers
(`client/components/admin/admin-groups-edit-rules.vue:215-229`).

### The admin group

Rewritten on every run, permissions and the `paisans-admin` rule alike. Any
other rule on it is the admins' and is kept.

### The member group

Seeded once: the bootstrap writes these permissions and the
`paisans-members` rule in the run that creates the group, replacing Wiki.js's
defaults (`server/app/data.yml:143-158`), and never edits the group again.
What members may do from then on is the community's admins', in Wiki.js.
A wiki whose member group already exists keeps what it has.

Members edit pages but do not move, rename or delete them (`manage:pages`,
`delete:pages`), which stay with admins: an edit is undone from the page's
history, a move or a delete is not. `write:scripts` and `write:styles` are
never a member's: a member's script would run in an admin's session.

### Pages only admins read

`admins/` and everything under it. The member rule is an allow whose regex
matches every path except those, so on `admins/` no member rule applies and
nothing grants a member anything there; the admin rule, `START ''`, does
grant an admin. On every other path both match, the member rule's longer path
wins, and both are allows. The prefix is fixed. Wiki.js refuses a page rule
regex it judges unsafe (`server/graph/resolvers/group.js`, on `update`); this
one has no repetition.

### Guests

A guest reads nothing unless the app is ungated and names `guest_paths`. Each
entry becomes a read-only rule. `Guests` is written on every run, so a guest's
access is the configuration's and a change made in Wiki.js is reported (below)
and undone at the next run.

An image is checked against its own folder's path, not the page's, so a
public page whose images are filed outside `guest_paths` shows guests broken
images. The README says so.

## New setting

```yaml
settings:
  # Paths a guest may read, each a prefix with no leading slash. Empty means
  # the wiki is sign in only.
  guest_paths: {type: list, default: []}
```

The bootstrap reads it as `GUEST_PATHS`, comma separated. New checks:

* `wikijs-guest-paths-gated` (refuse): `guest_paths` is not empty and the app
  is gated. The gate turns away a guest before Wiki.js sees them.
* `wikijs-guest-paths-admins` (refuse): an entry is `admins` or starts with
  `admins/`.
* `wikijs-guest-path-name` (refuse): an entry with a leading slash, a comma,
  or a character the env file cannot carry bare, as for `synced_groups`.

## Drift

The bootstrap reads every setting it writes on every run, every
`DRIFT_INTERVAL` (default `15m`), and reports when Wiki.js no longer holds
what the configuration says. It writes nothing when it finds a difference:
the next run, at the next start of its container, puts the configuration
back.

**Compared:** the strategy list (each strategy's key, enabled state, order and
the config fields the bootstrap writes), the site settings it writes, the
`Guests` group's permissions and page rules, the admin group's permissions,
and its `paisans-admin` rule.

**Not compared:** the member group and every other synced group after
creation, any other rule on the admin group, navigation, theme, users and
membership. Those are the admins'.

**Compared as meaning, not bytes.** Permissions and roles as sets, rules by
`id`, strategies by key, URLs without a trailing slash. Wiki.js reorders and
fills defaults on save, and a byte comparison would report drift that is not.

**Reported** on `GET /healthz` as 503 `drifted:` followed by each difference,
naming the setting and both values, as one log line per difference, and to
the monitor (below). The OAuth client secret is never printed: a difference in
it reads `client secret differs`. Each poll logs in as the break glass admin,
since a Wiki.js session expires, and holds no lock: it writes nothing, and
each instance of a cluster placed wiki polls its own. A failed poll (Wiki.js
unreachable, a login refused) is a 503 with that error, as a failed run is.

**Docker's health is not drift.** The image's healthcheck reads `GET /livez`,
which answers 200 once a run has converged and goes on answering 200 whatever
a later poll finds; 503 while a run is in progress or after one failed.
Drift is reported on `/healthz` and to the monitor only, so a drifted
bootstrap is a healthy container, and `apply`'s health gate, which counts an
unhealthy container failed (`internal/apply/health.go:100-101`), never stops
on it.

### The settings heartbeat

The bootstrap pushes its state to the monitor, as the operator reconciler's
answer is pushed (README, *The operators heartbeat is a service beside the
reconciler*). Every 60 seconds, apart from the poll, it reads its own last
result and, to every URL in `HEARTBEAT_URLS` (space separated, each a
monitor's `https://<monitor hostname>/ping/<token>`):

| Last result | Push |
|---|---|
| converged, no drift | `GET <url>` |
| drifted, or a run or a poll failed | `POST <url>/fail`, `Content-Type: text/plain`, the `/healthz` body as the request body |
| a run in progress | nothing |

Each push has a 10 second timeout and two retries, runs beside the others so
a slow monitor delays none, and logs a line when it fails. An empty
`HEARTBEAT_URLS` pushes nothing.

Every instance of a cluster placed wiki pushes to the one token. Drift is a
property of the database they share, so they agree; an instance that cannot
reach its own Wiki.js is already shown by that site's direct check.

### A catalog blueprint may declare a heartbeat

```yaml
heartbeat:
  # The monitor is named "<app> — <name>".
  name: "settings"
```

For a blueprint that declares one, Paisans:

* generates `apps.<app>.heartbeat_token` at `init` and in `apply`'s dry run,
  with the other generated secrets, in the site heartbeat token's shape, and
  `render` refuses an app whose token is missing or any other shape;
* adds a push monitor per app to the uptime seed, interval 60 and grace 120,
  as the site and operators heartbeats have;
* gives the app's templates `.HeartbeatURLs`: the ping URL of every monitor,
  space separated, empty in a deployment with no monitor. The template that
  carries it is a 0600 secret file;
* rotates the token in `secrets rotate`, by name or with `--all`: every
  monitor learns it first, then the app is recreated with it.

`heartbeat` is a new key, so the wikijs blueprint's `apiVersion` changes with
it, and a toolkit that cannot read it uses the newest catalog release it can.

### Who else sees it

* **`paisans doctor`.** A container that runs but whose healthcheck reports
  unhealthy is a failure, with the last lines of its health output. Today
  doctor counts a running container as up whatever its health
  (`internal/doctor/containers.go:65`). This applies to every blueprint; for
  the bootstrap it means a run that failed, not drift.
* **`apply`** does not read drift. Recreating the stack runs the bootstrap,
  which converges.

## Order of work

1. **paisans-cli:** a catalog blueprint's heartbeat; doctor's unhealthy
   finding. Released before the catalog uses them.
2. **wikijs-bootstrap:** the member seed, `GUEST_PATHS`, the drift poll,
   `/livez`, the heartbeat. A new minor, `0.2`.
3. **paisans-blueprints:** `guest_paths` and its checks, the heartbeat, the
   bootstrap track `0.2.*`.

Each through a pull request.

## To verify before building

* That Wiki.js accepts `^(?!admins(/|$))` as a page rule. Its safe regex check
  has to pass a lookahead.
* What `authentication.activeStrategies` returns for a strategy's client
  secret, and whether it can be compared at all.
