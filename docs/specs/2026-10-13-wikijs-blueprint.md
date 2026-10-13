# Wiki.js is the default wiki

Status: design approved in session on 2026-10-13, not yet implemented. Founder
decisions throughout; the provisional points are marked as such, and *To verify
before building* lists what the design rests on but nobody has observed.

A community's wiki is where its admins keep what only admins should read and its
members keep what everyone should. That split has to follow the groups people
are in at Pocket ID, without someone copying memberships by hand. Wiki.js does
that natively: its OAuth2 sign-in puts each person in the wiki groups their
groups claim names, on every login, and its page rules gate content by path per
group. So the core catalog's wiki becomes a `wikijs` blueprint, running Wiki.js
2.5, and the `outline` blueprint is removed. Founder decision.

Four repositories change:

| Repository | Change |
|---|---|
| `Paisans-Software/wikijs-bootstrap` | new: the bootstrap, a one shot program that finishes Wiki.js's setup and keeps its sign-in, site settings and groups as the configuration says |
| `Paisans-Software/paisans-blueprints` | `blueprints/wikijs/` added, `blueprints/outline/` removed |
| `Paisans-Software/paisans-stack` | the `create` wizard, help and error text, the volume registry, the core pin, the test fixtures and the README |
| none | the deployment agent rules, if the founder widens the exception in *Groups at Pocket ID* |

Every citation of Wiki.js below is at `v2.5.316`, the newest 2.x release, which
is tagged in `requarks/wiki-v2` (2026-10-01). Pocket ID citations are at
`v2.14.0`, the version the `pocket-id` blueprint pins.

## Which Wiki.js

2.x. Version 3 is a beta whose own README says "NOT FOR PRODUCTION USE", whose
schema took 18 migrations in the two months to 2026-10-13, and which has no
documented upgrade from 2.x. The blueprint pins
`ghcr.io/requarks/wiki:2.5.316` by digest, which on 2026-10-13 was
`sha256:fffff288a52f76e5b8c19a4fbcd0f3e538e96edea642c4f2d813d54d59a876de`;
the implementation re-resolves it rather than trusting this line.

2.x is in maintenance. Its advisories so far (15, nine of them stored XSS) were
published within a day of their fixed release, and the access control ones
needed an existing account. That is why the blueprint is gateable: see *The
gate*.

## Where its files live

In Postgres. Wiki.js 2.x writes every upload to its `assetData` table and serves
it back from there (`server/models/assets.js:128-139,226-228`). Its S3 storage
modules only push copies outward (`supportedModes: [push]`), so the blueprint
has no `storage.s3`, no bucket and no media hostname. A Postgres backup is
therefore a backup of every page and every upload. That rests on backups
existing: `docs/specs/2026-10-11-backups.md` is designed and not yet built, and
until it is, a wiki's uploads are no safer than its database.

Nothing else needs to persist. `/wiki/data/cache` is emptied at every boot
(`server/core/system.js:28`). The image declares `/wiki/data/content` as a
volume (`dev/build/Dockerfile:47-51`), so the compose file binds it under the
stack's data path, as every declared volume is, rather than leaving Docker an
anonymous one; `internal/kinds/volumes.go` registers it.

Uploads are capped by Wiki.js at 5 MB per file and 10 files per request by
default (`server/app/data.yml:96-98`). `upload_max_mb` sets the first; the
gateway's request body limit for the hostname follows it, because a limit
lower at the edge than in the app fails an upload with no message the member
can read.

## Placement and high availability

Cluster placement is supported from the first version. Founder decision. Under
`cluster` Wiki.js runs on every apps site against one database through the local
proxy, so the compose file sets `HA_ACTIVE=true`: each instance then listens on
Postgres `LISTEN/NOTIFY` and reloads configuration, authentication and pages
when another instance changes them (`server/core/db.js:229-264`). Pinned, it
sets `HA_ACTIVE=false`. Every `DB_*` variable is set explicitly, because an
unset one becomes the string `"undefined"` (`server/helpers/config.js:22`).

Two instances against one empty database is the hard case, and *The bootstrap*
handles it.

## Sign-in

Pocket ID is the only way in for members, through Wiki.js's generic **OAuth2**
module, not its OpenID Connect one. The OpenID Connect module fixes its scopes
at `openid profile email` (`server/modules/authentication/oidc/definition.yml:10-13`,
applied at `server/models/users.js:313`), and Pocket ID emits the `groups`
claim only when the `groups` scope is asked for (`oidc/claims_service.go:157` at
v2.14.0). The OAuth2 module takes its scope as configuration and maps groups
the same way (`server/modules/authentication/oauth2/authentication.js:36-48`),
reading every claim from the userinfo endpoint:

| Key | Value |
|---|---|
| `scope` | `openid profile email groups` |
| `userIdClaim` | `sub`, Pocket ID's user ID |
| `emailClaim` | `email` |
| `displayNameClaim` | `name` |
| `pictureClaim` | `picture` |
| `mapGroups` | `true` |
| `groupsClaim` | `groups` |
| `enableCSRFProtection` | `true`, the OAuth2 `state` parameter |
| `useQueryStringForAccessToken` | `false`, so the token goes in the header |
| `logoutURL` | Pocket ID's end session endpoint |

The strategy's key is **`pocketid`**, chosen rather than generated. Wiki.js
stores whatever key it is sent (`server/graph/resolvers/authentication.js:209-238`)
and builds the callback from it (`server/core/auth.js:85`), so the callback is
always `/login/pocketid/callback` and the blueprint declares it for the Pocket ID
client before Wiki.js has ever started:

```yaml
oidc:
  callback: "/login/pocketid/callback"
  pkce: false            # passport-oauth2 here sends no code_challenge
  reads_groups: true
  admin_group_setting: "admin_group"
  member_group_setting: "member_group"
  default_admin_group: "admins"
  default_member_group: "members"
```

The Pocket ID client stays restricted to the member and admin groups, as for
every app: that restriction is who may sign in at all. The groups claim decides
only what a signed in person may do.

**Auto-login is on at both layers.** Wiki.js's own `authAutoLogin` sends
`/login` to the first strategy by `order` unless it is a form
(`server/controllers/auth.js:37-43`), so `pocketid` is order 0 and `local`
after it. A guest asking for any page is sent to `/login` with the page kept in
the `loginRedirect` cookie, so a deep link lands where it pointed. The gate's
rule skips that hop for someone it already let through:

```yaml
gate:
  auto_login:
    # Wiki.js's session is its jwt cookie (controllers/auth.js, the callback).
    - {paths: ["/", "/login"], target: "/login/pocketid", absent_cookies: ["jwt"]}
  sign_in_path: "/login"
```

A member who opens the wiki with a Pocket ID session passes the gate, is sent
to `/login/pocketid`, comes back from Pocket ID without a prompt and is signed
in with their groups set from the claim.

## Groups

### In Wiki.js

`synced_groups` names the Pocket ID groups that become Wiki.js groups, by
Pocket ID group **name**, and defaults to the admin group and the member group.
The bootstrap creates each one that does not exist in Wiki.js, under exactly
that name, because the mapping matches names exactly and case sensitively
(`oauth2/authentication.js:40`). A new deployment's wiki therefore has its
groups before anyone signs in. Founder decision.

Membership is Pocket ID's. On every login Wiki.js adds the person to each
existing group their claim names and **removes them from every group it does
not** (`oauth2/authentication.js:41-46`). A person added to a group by hand in
Wiki.js loses it at their next sign-in unless Pocket ID has them in a group of
the same name, so every group a member should be in is a Pocket ID group and is
listed in `synced_groups`. What a group may do (its permissions and page rules)
is Wiki.js's, set by the community's admins in Wiki.js; the bootstrap creates a
group with Wiki.js's defaults and never edits one it did not just create, with
the one exception of the admin group below. Gating a collection of pages by
path, Eg: `/admin/` for the admin group only, is a page rule an admin writes.

**The admin group gets every permission except `manage:system`.** Founder
decision. That keeps the settings the bootstrap owns out of every member's
reach, enforced by Wiki.js itself: each of them is a `manage:system` mutation
(`site.graphql:69`, `authentication.graphql:70-91`), and someone holding
`manage:groups` without `manage:system` cannot grant `manage:system`, change a
group that has it, or put anyone in one (`server/graph/resolvers/group.js:50-64,181-195`).
Only the built in `Administrators` group (ID 1) has it, and only the break
glass local admin is in it.

### At Pocket ID

A `synced_groups` entry with no Pocket ID group of that name is created at
Pocket ID by `apply`, empty, with the toolkit's existing group creation. An
empty group grants nothing until an admin adds people to it. Founder decision.

**Provisional:** creating a group is a mutation of the identity provider's
groups, which `docs/deployment-agent-rules.md` puts behind explicit approval
every time. Its exception covers an app's own client and restricting it to the
app's group, not other groups. So either the founder widens the exception to
"the groups an app's `synced_groups` names", which is a change to the tiers and
the founder's to make, or `apply` stops and asks before creating one. Until that
is decided, `apply` asks.

## The gate

The blueprint is gateable. Founder decision. Wiki.js already refuses guests
every page, upload and GraphQL query once the `Guests` group has no
permissions, but a source review at v2.5.316 found what it leaves open before
sign-in:

* `POST /u` writes up to 10 files of the upload limit to disk before it checks
  permission (`server/controllers/upload.js:13-22`), and the leftovers are
  purged only every 15 minutes, with no rate limit on the route.
* `/h/<path>` answers 404 or 200 and `/e/<path>` words its refusal differently
  depending on whether the page exists (`server/controllers/common.js:144-170,260-271`),
  and `/i/<id>` does the same for page IDs (`:294-308`).

The gate closes all of it, and puts every surface Wiki.js exposes before
sign-in (its GraphQL parser, `multer`, the passport callbacks) behind Pocket ID.
The OAuth2 start and callback paths are open paths, as an app's own sign-in
routes are for every gateable blueprint, and `/healthz` is probed privately.

## The bootstrap

Wiki.js 2.x reads only its database settings from the environment; its first
admin, sign-in providers, site URL and groups live in its database and change
through its GraphQL API. The bootstrap is what sets them. It is a Go program
in its own repository, `Paisans-Software/wikijs-bootstrap`, published as
`ghcr.io/paisans-software/wikijs-bootstrap` by that repository's release
workflow, and the blueprint pins it by digest as a second service (the
version and digests below stand for the ones its release produces):

```yaml
services:
  - {name: "app", image: "ghcr.io/requarks/wiki:2.5.316@sha256:...", purpose: "the application"}
  - {name: "bootstrap", image: "ghcr.io/paisans-software/wikijs-bootstrap:X.Y.Z@sha256:...", purpose: "finishes setup and keeps sign-in, site settings and groups as configured"}
  - {name: "postgres", purpose: "its own database, when the app is pinned"}
```

It lives outside the toolkit because no core blueprint is named in the
toolkit's Go: the blueprint carries its own behaviour, and an image it pins is
how that behaviour runs.

### When it runs

Once per start of its container, then it exits: `restart: on-failure`, so a
failed run is retried and a finished one stays finished. Every `apply` that
recreates the stack runs it again, which is when a configuration change reaches
Wiki.js. It reaches Wiki.js by service name on the stack's own network, never
through the gateway, and the database through the same host and port Wiki.js
uses, with the same credentials.

It shares the app's process namespace (`pid: "service:app"`, which the compose
policy allows because it reaches no further than the stack) and runs as the
app's user, uid 1000, so that it can stop a stale instance (step 3 below).

### What one run does

1. **Wait** for `GET /healthz` on the app to answer 200, and for the database
   to accept a connection.
2. **Lock.** Take a session level Postgres advisory lock with a fixed key, so
   that of every instance's bootstrap against one database exactly one runs
   steps 3 to 5 at a time.
3. **Set up, or not.** Read whether Wiki.js's `settings` table has rows, and
   ask the local instance whether it is in setup mode (an instance in setup
   mode answers every GET with the setup page, `server/setup.js`).
   * Empty table, instance in setup mode: call `POST /finalize` on the local
     instance with the local admin's email and password and the site URL, and
     wait until the table has rows and the instance has left setup mode
     (`server/setup.js:363`, about one second).
   * Rows present, instance in setup mode: this instance started before
     another finished setup and will serve the setup page until it restarts.
     Send it `SIGTERM`, which it handles by shutting down
     (`server/index.js:46`); its `restart: unless-stopped` brings it back, now
     reading a set up database. Wait for it, then go on.
   * Rows present, instance not in setup mode: nothing to do here.
   * Empty table, instance not in setup mode: refuse and exit non zero. It
     cannot happen unless the database was emptied under a running wiki, and
     nothing the bootstrap could do would be safe.

   **`/finalize` is called only on an empty table, under the lock.** It is not
   a setup step that can be repeated: it deletes every user and every group
   before seeding (`server/setup.js:204-223`), so a second call against a wiki
   in use would empty it.
4. **Log in** as the local admin, with the `authentication.login` mutation on
   the `local` strategy, for a session token. The API switch is never turned
   on: it governs API keys only (`server/core/auth.js`, the `api` claim
   branch), and a session token works whatever it says. No API key ever
   exists. Login is limited to five a minute, which one run per start never
   approaches.
5. **Converge**, with that token:
   * the strategy list: `local` at order 1, and `pocketid` (OAuth2, the
     configuration in *Sign-in*) at order 0, enabled, with self registration
     on and no domain list, so a person Pocket ID admits gets an account.
     `updateStrategies` replaces the whole list and refuses one without
     `local` (`resolvers/authentication.js:232-238`), so both are always sent;
   * the site: `host` (the public URL), `title`, `description`, `logoUrl`,
     `authAutoLogin: true`, `authHideLocal: true`,
     `securityTrustProxy: true`, `uploadMaxFileSize`;
   * the `Guests` group (ID 2): no permissions and no page rules, so the wiki
     is sign in only (`server/controllers/common.js:465-471`);
   * the admin group's permissions: every one except `manage:system`;
   * every `synced_groups` group missing from Wiki.js, created with Wiki.js's
     defaults.

   The settings changed on one instance reach the others through `HA_ACTIVE`,
   except authentication strategies, which Wiki.js loads at boot
   (`server/models/authentication.js:94-122`). *To verify before building*
   asks whether its reload event covers them; until that is observed, the
   bootstrap restarts its own instance after changing them, as in step 3.
6. **Release** the lock and exit 0.

### What it writes, and only that

The bootstrap is a service the deployment runs, so under
`docs/deployment-agent-rules.md` it is approved once as a whole, and that
approval holds only while every kind of write it makes is named here and in a
decision record, its tests fail on any other, and it logs every one. Its
writes are exactly:

| Write | Through |
|---|---|
| setup, once, on an empty database | `POST /finalize` |
| the strategy list | `authentication.updateStrategies` |
| the site settings named in step 5 | `site.updateConfig` |
| the `Guests` group's permissions and page rules | `groups.update`, ID 2 |
| the admin group's permissions | `groups.update` |
| a missing `synced_groups` group | `groups.create` |
| a stale instance's restart | `SIGTERM` to the app's process |

It reads the `settings` table and takes the advisory lock, and writes nothing
to the database directly. It never deletes a group or a user, never changes a
group's members, never edits a group other than `Guests` and the admin group,
and never edits a page. Each write is one log line: what, the target, the
result. A test fails if any other mutation or any direct SQL write appears in
its code.

### What it holds

The local admin's password, the Pocket ID client's ID and secret, the database
credentials Wiki.js has already, and the public configuration (`synced_groups`,
title, description, logo URL, upload limit). Its environment file is rendered
`0600`, as every `.secret.tmpl` is. It holds no Pocket ID API key and talks to
Pocket ID not at all.

### The gateway closes `/finalize`

`/finalize` takes no authentication and no CSRF token
(`server/setup.js:76`): whoever reaches it first owns the wiki. The blueprint's
Caddy snippet refuses `/finalize` on the wiki's hostname for every method,
always, so only the bootstrap, on the stack network, can ever reach it. The
same snippet sets the request body limit from `upload_max_mb`.

## Settings

```yaml
apps:
  wiki:
    blueprint: wikijs
    hostname: wiki.example.org
    settings:
      member_group: members            # who may sign in at all
      admin_group: admins
      synced_groups: [admins, members] # the default: the two groups above
      site_title: "Example Wiki"
      site_description: ""
      logo_url: ""                     # an absolute URL, or empty for Wiki.js's own
      upload_max_mb: 5
```

`site_title`, `site_description` and `logo_url` are the blueprint's because
changing them in Wiki.js needs `manage:system`, which no member has. Founder
decision. `logo_url` must be reachable without signing in, because the login
page shows it to someone who has not; a logo uploaded into the wiki is not,
since `Guests` cannot read assets. Validation refuses a `synced_groups` entry
that is not a valid Pocket ID group name. **Provisional:** it also refuses a
`synced_groups` that omits the admin group, without which nobody but the break
glass admin can administer the wiki; that refusal is inferred, not decided.

What the bootstrap sets and the blueprint does not expose: the strategy list,
`host`, `authAutoLogin`, `authHideLocal`, `securityTrustProxy`, the `Guests`
group and the admin group's permissions. A change to one of these in Wiki.js
is possible only for the break glass admin and is undone at the next run.

New secrets, generated at `init` like every other: `local_admin_password`, and
the database password every Postgres app has.

## The break glass admin

User 1, `local`, in `Administrators`, created by `/finalize` with an address on
the community's domain and the generated password. It is how the bootstrap
logs in and the one account with `manage:system`. Wiki.js protects it from
being removed from `Administrators` (`resolvers/group.js:137`). With auto-login
on and the local form hidden, nobody reaches it in a browser by accident:
`/login?all=1` skips auto-login (`controllers/auth.js:37`) but `authHideLocal`
still hides the form, so using it by hand means first turning `authHideLocal`
off in the `settings` table and restarting the app. That is deliberate. It is
written up as a recovery procedure in the README, not as a workflow.

## Removing Outline

The `outline` blueprint is deleted from the core catalog, and the toolkit stops
naming it:

* `internal/create/shapes.go`: the wiki question becomes "A wiki (Wiki.js)?",
  making the `wiki` app with the `wikijs` blueprint, whose default hostname is
  therefore `wiki.<domain>`.
* `cmd/paisans/help.go`, `cmd/paisans/oidc.go`, `internal/config/config.go`:
  help and error text that lists kinds.
* `internal/kinds/volumes.go`: Outline's entry replaced by Wiki.js's.
* `internal/blueprints/core/pin.yaml`: the pin follows the catalog release
  that carries `wikijs`.
* Tests. The validate fixtures' `docs` app becomes a `wikijs` app, and so does
  every test that used Outline as an ordinary Postgres app with OIDC. Tests of
  S3, media hostnames and Garage move to WriteFreely, the core blueprint that
  still stores in S3. Tests of rules only Outline had (its bucket name rules)
  go with it. The render golden trees are regenerated and their diff read.
* `README.md`, `AGENTS.md`, `examples/paisans-stack.example.yaml` and
  `examples/secrets.example.yaml`: Outline's sections rewritten for Wiki.js.
  Removing a section rather than rewriting it needs the founder's approval
  under `AGENTS.md`.
* `docs/decisions.md`: an entry recording the change of default wiki and why.

A deployment already running Outline is not converted. It is redeployed.

## Order of work

1. `wikijs-bootstrap`: the repository (its creation is the founder's, being
   a new public repository), the program, its tests, and a release whose image
   is published.
2. `paisans-blueprints`: `blueprints/wikijs/` pinning both images by digest,
   and `blueprints/outline/` removed, in one release of the catalog.
3. `paisans-stack`: the toolkit changes above, pinning that catalog release.

Each through a pull request; `main` and `develop` take nothing else.

## To verify before building

Each of these is reasoned from source, not observed, and the design depends on
it:

* That `apply`'s health gate treats a service that exits 0 as done rather than
  as a failed container, and waits for it. If it does not, the gate needs to
  learn one shot services before this ships.
* That `pg-pubsub` reconnects its `LISTEN` after a Patroni failover moves the
  primary. If it does not, instances stop hearing each other until restarted.
* Whether a strategy change made on one instance reaches the others through
  `HA_ACTIVE`, which decides whether step 5 must restart its own instance
  after one. The name of the event that carries it, if any, has not been read.
* That Pocket ID shows no consent screen on a first sign-in that asks for the
  `groups` scope, or what the toolkit's client settings do about one.
* Wiki.js's behaviour against Pocket ID end to end: the OAuth2 module's
  callback, the claims as userinfo returns them, the groups mapping, and the
  `loginRedirect` return to a deep link.
