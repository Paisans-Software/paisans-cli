# Wiki.js blueprint: what the spike observed

A throwaway spike, run on 2026-10-13 against the spec
`docs/specs/2026-10-13-wikijs-blueprint.md`, to observe the items its
*To verify before building* section lists as reasoned from source only.
Everything ran locally in Docker, in one compose project, and was torn down
with `docker compose down -v` afterwards. Nothing from it is kept but this
file.

Images: `postgres:18`, `ghcr.io/requarks/wiki:2.5.316` (twice, `wiki-a` and
`wiki-b`), `ghcr.io/pocket-id/pocket-id:v2.14.0`, and `alpine:3.22` for the
`probe` (sharing `wiki-a`'s process namespace) and a `client` container that
ran every `curl` from inside the compose network.

**Sign-in was driven with `curl`, not a browser.** Pocket ID is passkey only,
so a Pocket ID session was obtained with a one time access token minted
through its admin API, and the OAuth2 redirects were then followed by hand
with a cookie jar. Everything said below about a consent screen is what
Pocket ID's `/authorize` and interaction API answered, not what a browser
rendered.

## Deviations from the brief, and why

* **Certificate ownership.** Docker Desktop's bind mount presented the key as
  owned by root with mode 0600, which the `postgres` user cannot read and a
  `chown` from inside a container does not change. The Postgres service
  instead mounted the files at `/tls-src` and its entrypoint copied them to
  `/tls` owned by `postgres` before starting. The server ran with the brief's
  `ssl=on`, certificate, key and `pg_hba.conf` unchanged.
* **The DSN.** `?sslmode=no-verify`, the brief's value, did not produce a TLS
  connection (step 1). After recording that, the rest of the spike used
  `?ssl=no-verify`, so that the later steps could run at all.
* **Hostnames.** Pocket ID refused to start with `APP_URL=http://pocketid:1411`:
  WebAuthn rejects `pocketid` as its relying party ID because it is not a
  domain. It ran as `http://pid.example.org:1411`, a network alias on the
  compose network, with `ALLOW_INSECURE_CALLBACK_URLS=true`. The wiki was
  finalised with `siteUrl` `http://wiki-a:3000` rather than
  `http://localhost:3001`, so that the callback Wiki.js builds from it
  resolves from the `client` container.
* **Compose shape.** Pocket ID and `client` were in the compose file from
  step 1; `wiki-a` and `wiki-b` both had `restart: unless-stopped` from the
  start.
* **Two bootstrap writes made by hand before the deep link test.** The
  `Guests` group was given no permissions and no page rules, and
  `authAutoLogin` was set to `true`, as the spec's bootstrap does in step 5.
  Without them a guest can read pages and `/login` shows a form, so the deep
  link test would have meant nothing.
* **Cookies.** Pocket ID sets its session cookie `Secure`, and `curl` will not
  store a `Secure` cookie received over plain http, so it was written into the
  jar by hand.

## Step 1: Postgres over TLS, two instances in setup mode

**Run.** The compose file from the brief, with `pg_hba.conf`:

```
local all all trust
hostnossl all all all reject
hostssl all all all scram-sha-256
```

**Observed with `sslmode=no-verify`.** Neither wiki connected. Postgres logged
`pg_hba.conf rejects connection for host ..., user "wiki", database "wiki", no
encryption` for both, and each wiki logged `Database Connection Error: 28000`
until it gave up retrying.

The cause is in the image. Wiki.js passes `DATABASE_URL` to knex 0.21.7 as a
string, and knex parses it itself (`node_modules/knex/lib/knex.js:50`,
`lib/util/parse-connection.js`) with its own bundled `pg-connection-string`
2.3.0. That version knows `ssl=true|1|0` and the `sslcert`, `sslkey` and
`sslrootcert` parameters, and nothing about `sslmode`: it passes `sslmode`
through as an unknown property and leaves `ssl` unset. node-postgres 8.16.3
then receives an object, not a connection string, so its own `sslmode`
handling never runs, and it connects without TLS.

Five DSN variants were tried from inside the Wiki.js container with the
image's own knex, against the same server:

| Query string | Environment | Result |
|---|---|---|
| `?sslmode=no-verify` | | rejected, no encryption |
| `?sslmode=require` | | rejected, no encryption |
| `?ssl=true` | | refused: self signed certificate (verifies) |
| `?ssl=no-verify` | | **TLSv1.3, connected** |
| `?sslmode=no-verify` | `PGSSLMODE=no-verify` | **TLSv1.3, connected** |

`?ssl=no-verify` works because knex's old parser passes the string
`no-verify` through untouched and node-postgres
(`lib/connection-parameters.js:88-91`) maps `ssl: 'no-verify'` to
`{rejectUnauthorized: false}`. `PGSSLMODE` works because node-postgres reads
it when `ssl` is unset.

**Observed with `ssl=no-verify`.** Both instances logged `Database Connection
Successful`, then `DB Configuration is empty or incomplete. Switching to Setup
mode...` and `Browse to http://YOUR-SERVER-IP:3000/ to complete setup!`.
`pg_stat_ssl` showed every connection from both wikis as `ssl = t`,
`TLSv1.3`, including the `LISTEN "wiki"` connection.

**Against the spec.** Contradicts it: `sslmode=no-verify` in `DATABASE_URL` is
not honoured, and a clustered wiki rendered as the spec says would never
connect. `ssl=no-verify` gives exactly the behaviour the spec wants
(encrypted, certificate not checked).

## Step 2: finalise through wiki-a only, then wiki-b

**Run.** `POST /finalize` on `wiki-a` with `adminEmail` `admin@example.org`,
the throwaway password, `siteUrl` `http://wiki-a:3000` and `telemetry` false.
Then `POST /graphql` with `{__typename}` on both.

**Observed.** `/finalize` answered `{"ok":true,"redirectPath":"/","redirectPort":3000}`.
The `settings` table then held 15 rows. `wiki-a`'s `/graphql` answered 200;
`wiki-b`'s answered **404**, and `GET /` on `wiki-b` still served the page
titled `Wiki.js Setup`. `wiki-b`'s `/healthz` answered 200 while it was in
setup mode. `wiki-b` also logged a `TypeError: Cannot read properties of
undefined (reading 'sendError')` at `server/setup.js:395` for the 404, which
is setup mode's error handler failing and does not affect anything else.

**Against the spec.** Agrees: an instance that started before setup finished
stays in setup mode. Worth knowing for the bootstrap's step 1: `/healthz`
answers 200 in setup mode too, so it says the process is up, not that setup is
done.

## Step 3: stop wiki-a's process from the probe

**Run.** From `probe` (`pid: "service:wiki-a"`), listed `/proc`, read
`/proc/1/status`, then ran `kill -TERM 1` **as uid 1000**, the user the spec
says the bootstrap runs as (`docker compose exec --user 1000 probe kill -TERM 1`).

**Observed.** In the shared namespace `node server` is PID 1, running as uid
and gid 1000. The `kill` succeeded. Both `wiki-a` and `probe` were restarted
by Docker (restart count 1 each, both with exit code 0, `probe` started about
70 ms after `wiki-a`). `wiki-a`'s container ID did not change, so `probe`'s
`pid` mode, `container:<that ID>`, still pointed at it. The `/proc` listing
repeated from `probe` showed `node server` as PID 1 again, a new host PID, so
`probe` shares the new namespace. `wiki-a` was serving GraphQL again within a
few seconds.

**Against the spec.** Agrees: `SIGTERM` from a sibling sharing the namespace,
as uid 1000, stops the app; the sibling dies with it, both come back under
`restart: unless-stopped`, and the sibling is in the restarted app's
namespace. This holds for a restart; a *recreated* app container gets a new ID,
and the sibling then has to be recreated with it (compose did so when the
spike's compose file changed).

## Step 4: OAuth2 against Pocket ID

**Run.** Pocket ID was started with `STATIC_API_KEY` and `ENCRYPTION_KEY`.
Through its admin API (`X-API-Key`): a group `members`, an admin user, a
member user in `members`, and an OIDC client with callback
`http://wiki-a:3000/login/pocketid/callback`, PKCE off, `isGroupRestricted`
true and allowed groups `members`, created with the same fields the toolkit's
`internal/pocketid` `NewOIDCClient` sends (which has no `skipConsent`, so the
client had `skipConsent: false`). A secret was minted for it.

On `wiki-a`, logged in as the local admin with the brief's
`authentication.login` mutation (`Login success`), then sent
`updateStrategies` with `pocketid` (strategy `oauth2`, order 0, enabled, self
registration on, no domain list) and `local` (order 1), the `pocketid` config
being the spec's *Sign-in* table plus the client ID and secret and Pocket ID's
`/authorize`, `/api/oidc/token`, `/api/oidc/userinfo` and
`/api/oidc/end-session` endpoints from its discovery document. Then
`groups.create(name:"members")`, which made group 3. Both answered success,
and `activeStrategies` listed `pocketid` order 0 and `local` order 1.

The sign-in: a one time access token for the member was minted with
`POST /api/users/:id/one-time-access-token` and exchanged with
`POST /api/one-time-access-token/:token`, which set Pocket ID's `access_token`
session cookie. Then, with a cookie jar, from the `client` container:

1. `GET wiki-a/deep/link/page` as a guest: **403**, the `Unauthorized` page,
   with a `loginRedirect` cookie set. No redirect.
2. `GET wiki-a/login`: 302 to `/login/pocketid`.
3. `GET wiki-a/login/pocketid`: 302 to Pocket ID's `/authorize` with
   `response_type=code`, the callback above, `scope=openid profile email groups`,
   a `state` and the client ID.
4. `GET` that `/authorize`: 302 to `/interaction?interaction=<id>`. The
   interaction API reported `requiredSteps: ["consent"]` with the four scopes.
5. `POST /api/oidc/interactions/<id>/complete` with `{"step":"consent"}`
   returned `/authorize?interaction=<id>`, which answered 303 to the wiki's
   callback with a `code`, `iss`, `scope` and the same `state`.
6. `GET` the callback: 302 to **`/deep/link/page`**, with a `jwt` cookie set.

**Observed, point by point.**

* **Consent screen.** Pocket ID asked for consent on the first sign-in, with
  the client as the toolkit creates it. A second sign-in of the same user,
  from a fresh Pocket ID session, went straight from `/authorize` to the
  callback with no interaction. With `skipConsent` set to `true` on the client
  (`PUT /api/oidc/clients/:id`, every other field sent back as read), a second
  member who had never signed in went straight through on the first try.
  This matches Pocket ID's source: `consentRequired` is
  `!hasAlreadyAuthorizedClient && !clientSkipsConsent`
  (`backend/internal/oidc/authorization_service.go:77-81`).
* **Groups.** The member's Wiki.js account (provider key `pocketid`, provider
  ID equal to the Pocket ID user ID, name from the `name` claim, email from
  `email`) was in group `members` (ID 3) and no other.
* **Deep link.** The callback sent the member to the page in the
  `loginRedirect` cookie. A guest asking for a page other than the home page
  is not sent to `/login`, though: Wiki.js answers 403 with an `Unauthorized`
  page that has a Login button, and sets the cookie
  (`server/controllers/common.js:463-477`). Only the home page (`/`) is
  redirected to `/login` (observed: `GET /` as a guest, 302 to `/login`, then
  the same flow, landing on `/`).
* **wiki-b.** After `docker compose restart wiki-b`, it left setup mode and
  logged `Authentication Strategy Pocket ID: [ OK ]` with no
  `updateStrategies` of its own. Its `/login` redirected to `/login/pocketid`,
  which redirected to Pocket ID with the same scope and client. Its
  `redirect_uri` was `wiki-a`'s, because the callback is built from the site
  `host` setting, which both share. In a deployment both instances sit behind
  one public hostname, so that is the right value. A sign-in was not completed
  through `wiki-b`.

**Against the spec.**

* The OAuth2 module, the `groups` scope, the claims from userinfo, the group
  mapping, the `/login/pocketid/callback` path and `loginRedirect` all agree.
* Consent **contradicts** "comes back from Pocket ID without a prompt": the
  first sign-in of each person to a client with `skipConsent: false` shows a
  consent screen, and the toolkit creates clients that way and never changes
  the field. Setting `skipConsent: true` on the client removes it.
* The deep link **contradicts** "A guest asking for any page is sent to
  `/login`": that is true only of the home page. Any other page answers 403
  with a Login button and the cookie set, so a member following a deep link
  with no `jwt` cookie clicks once, then lands on the page. The gate's
  `auto_login` rule as written (`/` and `/login`) does not cover it.

## Step 5: HA listening across a database restart

**Run.** As a control, `site.updateConfig(title:"Before restart")` on `wiki-a`,
then `{site{config{title}}}` on both. Then `docker compose restart postgres`,
30 seconds' wait, `site.updateConfig(title:"After restart")` on `wiki-a`, and
the same read on both, with the local admin's token.

**Observed.** The control propagated (`wiki-b` logged `Received event
reloadConfig from instance ...`). Both wikis logged `Connection Error:
Connection ended unexpectedly` when Postgres went down. After the restart the
new title was read back from `wiki-b` as `After restart`, and `wiki-b` logged
a second `Received event reloadConfig`. The listener connection was TLS
(`pg_stat_ssl`).

**Against the spec.** Agrees for a restart of the same server: the `LISTEN`
reconnects and `HA_ACTIVE` keeps working. A Patroni failover, where the
primary moves to another host behind the proxy, was **not observed**.

## Contradicts the spec

1. **`sslmode=no-verify` is not honoured.** Wiki.js 2.5.316's knex parses
   `DATABASE_URL` with a `pg-connection-string` that ignores `sslmode`, so the
   connection is made without TLS and `hostnossl ... reject` refuses it.
   `?ssl=no-verify` (or `PGSSLMODE=no-verify`) gives an encrypted, unverified
   connection. The spec's *Placement and high availability* section, and the
   bootstrap's use of the same DSN, need the other parameter.
2. **Pocket ID shows a consent screen on each person's first sign-in** with a
   client created as the toolkit creates it (`skipConsent: false`). The
   toolkit's client settings do nothing about it; `skipConsent: true` does.
3. **A guest's deep link is not sent to `/login`.** Only `/` is. Any other
   page answers 403 with a Login button and sets `loginRedirect`; after one
   click the member lands on the page.

Not observed: `LISTEN` across a Patroni failover, and a sign-in completed
through the second instance.
