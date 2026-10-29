# Beszel alerts in Matrix: `ops-notifier`

Status: drafted 2026-10-29 at the founder's direction. The founder chose a
bot that messages each operator directly, so that each keeps their own
alert rules; account deletion when an operator leaves; both opt-out signals
below; and `subscribe` after inviting the bot to turn alerts back on. Every
line marked *provisional* is the author's call and open to correction.

## Why

Beszel's hub (`docs/specs/2026-10-29-beszel-server-stats.md`) keeps alert
rules per user, and sends each alert to that user's own notification
settings: a list of email addresses and a list of Shoutrrr URLs
(`internal/alerts/alerts.go:214-275` at Beszel v0.21.1). Email works once the
hub has the deployment's mail settings. Matrix works too, through Shoutrrr's
`matrix://` service, but only with a Matrix account to send as, a room to
send to and a URL holding both in the operator's settings. Nothing an
operator can do in Beszel's UI makes the account or the room, and a URL
pasted by hand stops working the day its token is renewed.

`ops-notifier` is the small service that keeps all of that true: one bot,
one direct room per operator, and the right URL in each operator's Beszel
settings, for as long as they are an operator.

## What gets built

1. **`Paisans-Software/paisans-ops-notifier-service`**, a Go service and its
   image, in the layout of `paisans-engagement-service`, whose MAS and Matrix
   clients it reuses.
2. **A reserved blueprint, `ops-notifier`**, in `paisans-cli`, colocated with
   the Matrix homeserver (`colocate: true`, blueprint format v4).
3. **A role the `beszel` blueprint provides, `beszel-hub`**, and the hub's
   side of the credential the notifier signs in with.
4. **A track in the core catalog's `foundation.yaml`**, `ops-notifier`, once
   the service has a released tag.

## Goals

* Every operator who has signed in to Beszel and has a Matrix account gets a
  direct room with the bot and receives their own Beszel alerts there, with no
  step by hand.
* An operator who no longer wants them stops them from Beszel or from their
  Matrix client, and starts them again from their Matrix client.
* An operator who leaves the operators group keeps nothing in Beszel: their
  account, rules and settings go.
* A renewed bot token reaches every operator's settings without anyone
  noticing.

## Non goals

* **No alert rules are seeded.** Each operator sets the rules they care
  about in Beszel's UI, as before. Paging belongs to the uptime monitor.
* **No shared operators room.** Alerts are per user in Beszel, so they go to
  the user who set the rule.
* **Email is not this service's.** The hub sends it with the deployment's mail
  settings, and Beszel gives every new user their own address as a
  recipient (`internal/users/users.go:62-86`). It is the fallback when the
  homeserver is down.

## Upstream, as read

| Claim | Source, at the tag named |
|---|---|
| An alert is sent to the owning user's `user_settings.settings` `emails` and `webhooks` | Beszel v0.21.1 `internal/alerts/alerts.go:214-275` |
| Only an `admin` user's webhooks use plain Shoutrrr; any other user's go through a client that refuses private, loopback and other internal addresses | `internal/alerts/alerts.go:243-253`; `internal/alerts/notification_client.go:20,43-53` |
| Matrix is among the services that client sends to, its login included | `internal/alerts/notification_client.go:92-130`, "Matrix can log in during Initialize" at :118 |
| `user_settings.user` and `alerts.user` cascade on the user's deletion | `internal/migrations/0_collections_snapshot_0_20_0.go`, the `user` relations of `alerts` (:37), `alerts_history` (:196) and `user_settings` (:590) |
| A new `user_settings` record is given `chartTime` and the user's email, and any other key passed at creation is dropped | `internal/users/users.go:62-86`, bound at `internal/hub/hub.go:111` |
| The UI saves settings as the stored object with its own keys merged in, so keys it does not know survive | `internal/site/src/lib/api.ts:57-67` |
| `matrix://:<token>@<host>/?rooms=<room>` with no user sends with the token as an access token and logs in with nothing | Shoutrrr v0.22.0 `pkg/services/chat/matrix/matrix_service.go:93-95`; `docs/services/chat/matrix/index.md` |
| A superuser signs in at `POST /api/collections/_superusers/auth-with-password`, and the token is sent as `Authorization`, with or without `Bearer` | PocketBase v0.40.5 `apis/record_auth.go:31`; `apis/middlewares.go:214-217` |
| An OAuth2 sign in is recorded in `_externalAuths` as `recordRef`, `provider` and `providerId`, the provider's subject | `core/external_auth_model.go:19,81-103` |
| The MAS admin API takes a client credentials token with scope `urn:mas:admin`; personal sessions are created at `/api/admin/v1/personal-sessions` and regenerated at `.../{id}/regenerate`; upstream links are listed by provider and subject | MAS v1.28.0, as `paisans-engagement-service` `internal/mas/client.go` cites it |
| Synapse shares `m.room.encryption` in an invite's stripped state by default | Synapse v1.160.0 `docs/usage/configuration/config_documentation.md`, `room_prejoin_state` |

## Who is an operator

The operators are `operators:` in `paisans-stack.yaml`, rendered into the
service as their email addresses. The hub admits only the operators group at
Pocket ID, so every Beszel user who signed in through it was an operator when
they did; the rendered list says who still is.

A Beszel user is matched by the email on their Beszel account, which Pocket
ID's claims set, against that list, ignoring case (*provisional*: the email
is what the configuration and the hub both hold, and an operator's email is
theirs to keep current at Pocket ID).

Two kinds of Beszel user are never touched: the break glass user,
`beszel-admin@<domain>`, and any user with no `oidc` record in
`_externalAuths`, since only a sign in through Pocket ID makes one. The
service also refuses to start with an empty operator list, so a rendering
mistake cannot read as "everyone left".

## From a Beszel user to a Matrix user

No Pocket ID call is needed:

1. The user's `_externalAuths` record for provider `oidc` holds their Pocket
   ID subject as `providerId`.
2. MAS links the same subject to a MAS user through the homeserver's
   upstream provider, whose id is the toolkit's `stableULID` of the
   homeserver app, as the engagement service reads it.
3. That MAS user's username is the Matrix localpart, so the operator is
   `@<username>:<server_name>`.

An operator with no MAS link yet has never signed in to Matrix. They are
skipped, and tried again on every pass.

## The bot

The bot is a MAS user, `ops-bot` by default (the app's `bot_localpart`
setting), made and kept the way the engagement service keeps its own:

* A client credentials token from MAS's token endpoint, which the
  homeserver's auth metadata names, with scope `urn:mas:admin`.
* The user, created when missing and never adopted when another account
  holds the name.
* A personal session with no expiry, scope `urn:matrix:client:api:*` and the
  fixed device `OPSBOT`, regenerated when the homeserver refuses its token
  and created again when MAS says the session is gone.

Its display name is the app's `bot_display_name` setting, `Ops Bot` by
default.

## The rooms

For each operator the bot creates a room with preset `private_chat`,
`is_direct`, the operator invited, history visible from the invitation and
`m.federate` false. Its power levels keep every state change with the bot,
`m.room.encryption` among them, so the operator cannot turn encryption on in
a room Shoutrrr must send to unencrypted. This is the engagement service's
own direct room (`internal/matrix/rooms.go`), reused as is.

The bot posts one line when it creates the room: that Beszel alerts arrive
there, and how to stop them.

A room found encrypted on any pass is logged by room id only, left and
forgotten, and replaced by a new one, with the operator's URL moved to it.

## The URL

The service writes, into the operator's `user_settings.settings.webhooks`:

```
matrix://:<bot access token>@<homeserver hostname>/?rooms=<room id>
```

* The homeserver's **public** hostname, from the `matrix-homeserver` link's
  `base_url`, since the operator is a Beszel `user` and their webhooks go
  through the client that refuses internal addresses.
* The service recognises its own URL by scheme, host and `rooms`, never by
  the whole string, since the token in it changes when the session is
  regenerated. A URL of its own with an old token is rewritten in place.
* Every other webhook and every email address is left exactly as it is.
* When the operator has no `user_settings` record yet, which happens until
  they first open the hub's UI, the service creates it, then writes the
  webhook in a second request, because a key passed at creation is dropped.

The token is readable by the operator in their own settings. Operators
already hold every secret of the deployment, so this widens nothing.

## State

The service keeps its state in one file under its data directory, a bind
mount on the homeserver's site, written atomically and mode 0600:

* the bot: its MAS user id, localpart, personal session id, access token and
  `/sync` position;
* per Beszel user id: the operator's Matrix id, their room, whether the URL
  was written (`provisioned`), whether they opted out (`opted_out`), and a
  room they invited the bot to and have not yet confirmed (`pending_room`).

The record is what makes an opt-out readable: Beszel keeps no history of a
setting, so "the URL is gone" means something only beside "the service wrote
it". Losing the file re-provisions every operator, opted out ones included,
so the data directory belongs in the app's backup like any pinned app's.

## The loop

A reconcile pass runs at start and then every five minutes (*provisional*),
and the bot long-polls `/sync` in between for room events. Each pass, as the
hub's superuser:

1. Lists the hub's users.
2. For each user who is not an operator, and is neither the break glass user
   nor without an `oidc` link: **deletes the Beszel user**, which deletes
   their alerts and settings by cascade, leaves and forgets their room, and
   drops their record.
3. For each operator with a Matrix id:
   * With no room: creates the room, writes the URL, marks them provisioned.
   * With a room the operator has left or been banned from: an opt out (b),
     below.
   * With an encrypted room: replaces it, as above.
   * Provisioned and not opted out, with no URL of ours in their webhooks: an
     opt out (a), below.
   * With a URL of ours carrying an old token: rewrites it.
4. Drops the record of any Beszel user who no longer exists, leaving their
   room.

Every step is idempotent, so a pass that fails part way is finished by the
next. Log lines name Beszel user ids and room ids, never a token, a password
or an email address.

## Opting out

Either signal turns an operator's alerts off:

* **(a) The URL is gone from their Beszel settings** while the record says
  the service wrote it. An edited URL of ours no longer matches, and counts as
  removed; the edited one is theirs and is left alone. The service marks them
  opted out and, in their room, posts: "Beszel alerts are off. Reply
  `subscribe` to turn them back on."
* **(b) They leave or reject their room**, or are banned from it. The
  service removes its URL, marks them opted out, and leaves and forgets the
  room.

An opted out operator is never given the URL again by a pass.

## Opting back in (*provisional*)

* **In their room**, replying `subscribe` writes the URL again and clears the
  opt out. `subscribe` from an operator already subscribed there confirms it.
* **After leaving the room**, the operator invites the bot to any room. The
  bot accepts an invitation only from a current operator, found from the
  inviter's Matrix id through MAS and then their Beszel user; any other is
  rejected.
  * An unencrypted room: the bot joins and asks for `subscribe`. On it, that
    room becomes the operator's room, the URL is written for it and the opt
    out is cleared. The room's power levels are the operator's, so a later
    switch to encryption is caught by the next pass, which replaces the room.
  * An encrypted room, which Synapse shows in the invitation's stripped
    state: the bot joins, posts a plain notice that alerts cannot be sent to
    an encrypted room, leaves, and opens a new room of its own as above,
    asking for `subscribe` there.

Any other message from the operator in their room is answered with a line on
how to stop and start (*provisional*).

## Offboarding

An operator removed from `operators:` stops being able to sign in at once,
since Pocket ID's group decides that. Their Beszel account would keep its
rules and its email recipient, and Beszel would keep sending them alerts, so
the next apply renders the shorter list and the next pass deletes the
account. Someone added back later starts afresh at their next sign in.

## Placement and links

`ops-notifier` consumes two roles, both `optional: true`, so that the
deployment's one provider of each is linked without naming its hostname:

```yaml
consumes:
  - {role: matrix-homeserver, setting: homeserver, optional: true, shared_secrets: [mas_admin_client_secret], colocate: true}
  - {role: beszel-hub, setting: hub, optional: true, shared_secrets: [beszel_superuser_password]}
```

* **`colocate: true` on the homeserver** pins the notifier where the
  homeserver runs (`colocated-app-elsewhere`). The MAS admin secret it holds
  can make a token for any Matrix user, so it stays on the host that already
  runs MAS, and MAS's admin listener is reached on that host's mesh address.
* **The hub's credential is the notifier's own.** A link's shared secrets
  flow from consumer to provider, and nothing flows the other way, so the
  notifier generates `beszel_superuser_password` and shares it with the hub.
  The `beszel` blueprint's `setup.sh` reads `.Consumers "beszel-hub"` and
  makes each consumer a PocketBase superuser,
  `paisans-link-<app>@<domain>`, with that password, at every start. It
  deletes any other `paisans-link-` superuser, so removing the notifier
  removes its access. The break glass password never leaves the hub's site,
  and rotating either credential touches only its holder and the hub.
* **`beszel-hub` offers `url`**, `http://<the hub's mesh address>:8084`, the
  hub's published port. The notifier reaches the hub's API over the mesh.

### Validate

| Combination | Result | Rule |
|---|---|---|
| an `ops-notifier` app and no `matrix-homeserver` in the deployment, or one without `mas_admin_url` | refused | `ops-notifier-without-homeserver` |
| an `ops-notifier` app and no `beszel` app | refused | `ops-notifier-without-hub` |
| more than one `ops-notifier` app | refused | `one-ops-notifier` |
| an `ops-notifier` app placed unlike its homeserver | refused | `colocated-app-elsewhere` |

## The service's own surface

Every app has a hostname, so the notifier has one, which the gateway routes.
Its Caddy snippet proxies `GET /healthz` alone, which answers `ok` while the
process runs and its state file is readable (*provisional*); nothing else is
served. The container publishes its port on the site's mesh address only, as
every app does, runs as the deployment user so it can write its bind mounted
data directory, and listens for nothing else.

## Security

* **The MAS admin secret** is the most powerful thing the notifier holds:
  with it, any Matrix user's token can be made. Colocation keeps it on the host
  that already runs MAS, so a takeover of any other host, the gateway
  included, does not reach it.
* **The hub's superuser password** crosses the mesh in plain HTTP inside
  WireGuard, from the homeserver's site to the hub's. A container on either
  host cannot capture it: under rootless Docker a container's `NET_RAW` is
  held in the rootless user namespace and the kernel refuses packet sockets
  on the host's network namespace to it (issue #580 has the reading). That
  password reaches Beszel's settings and stats only.
* **The bot's token** sits in each operator's Beszel settings. It can send
  as the bot and read the bot's rooms. A regenerated session replaces it
  everywhere on the next pass.

## Tests

* The service: unit tests over fake MAS, Matrix and PocketBase servers, in
  the engagement service's `mastest` and `matrixtest` style, for every pass
  step, both opt out signals, both re-subscribe paths, offboarding, token
  rewriting and an encrypted room.
* The toolkit: the blueprint parses; the four validate rules with fixtures;
  the rendered `.env`, compose file, Caddy snippet and the hub's `setup.sh`
  in the golden tree; the shared secrets reach the hub and MAS.

## Not verified against a live system

Every PocketBase, Beszel, MAS and Synapse behaviour above is read from source
or documentation at the tags named. None of it has run against a live hub or
homeserver: the first apply on a staging deployment is that test.
