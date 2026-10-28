# The weekly digest on Matrix, from a Digest Bot

Status: draft, 2026-10-27, awaiting the founder's approval of the whole.
The founder settled the shared secret, the admin API's exposure and the
rate limit approach in session the same day. It extends
`docs/specs/2026-10-24-engagement-digest.md`, which stays the description of
the digest itself. A line marked *provisional* is the author's call and open
to correction.

## Why

The digest email reaches a member in their inbox, where it competes with
everything else they get. A community that runs Matrix also has a place its
members already talk in. A short message there on Monday, from an account
that plainly says what it is, puts the week's threads where the member is
looking, without asking them to open a long email.

The message is a nudge, not a second copy of the email: the titles of the
first few threads and a link to the whole digest. The full digest becomes a
web page so the nudge has something to link to.

## What gets built

1. **`paisans-engagement-service`**: a Matrix client that runs as one bot
   account, sends the Monday message, reads replies, and keeps each member's
   Matrix setting; and a web page for each sent digest.
2. **The `engagement` blueprint** in `paisans-cli` (reserved,
   `internal/blueprints/reserved/engagement`): renders where the homeserver
   is and the credentials the service needs to run the bot and look members
   up.
3. **The `synapse` blueprint** in `paisans-blueprints`: declares the MAS
   admin client when an engagement app consumes the homeserver, mounts MAS's
   admin API on the mesh only, and refuses it at the gateway.
4. **A toolkit change** in `paisans-cli`: a provider's templates can see the
   apps that consume its role, and a secret can be shared between a provider
   and its consumer (see *The bot account*).

## Goals

* Every member with a Matrix account on the deployment's homeserver gets a
  Monday DM from the Digest Bot, automatically. There is no sign up.
* Email and Matrix are independent. Unsubscribing from the email does not
  stop the DMs, and stopping the DMs does not stop the email.
* A member stops the DMs from Matrix itself, by replying `stop` or by
  leaving the room, and starts them again by replying `start`, `resume` or
  `restore`, or by messaging the bot.
* Each member's digest for a week can be read as a web page behind a signed
  link, so the DM has somewhere to point.
* A deployment without a homeserver runs the digest exactly as today.

## Non goals

* **No encrypted rooms.** The bot never holds Matrix encryption keys or a
  device that takes part in encryption. Its rooms are unencrypted, and kept
  that way (see *The room*).
* **No link to the web page from the email.** The email is already long and
  is complete as it is. The web page exists for the DM.
* **No preferences page.** On and off from Matrix is the whole of the
  setting. A web toggle on the engagement hostname that turns DMs back on is
  a likely later feature; this design keeps a place for it (see *The member's
  setting*) and does not build it.
* **No federation.** The bot talks only to local accounts on the
  deployment's own homeserver.
* **No other commands.** The bot does not answer questions, search, or show
  past digests.

## Anti goals

These would make it a failure even if it technically works:

* Messaging a member who said `stop` or left the bot's room.
* Inviting a member again, unprompted, after they left or rejected the bot's
  room.
* Sending a member two DMs for the same week.
* Sending a DM in a week the member has nothing new.
* A web digest link that shows one member's digest to anyone who alters it,
  or that keeps working indefinitely.
* The bot reading or replying in a room it did not create.

## The Monday message

It goes out in the same run as the email, to every member who qualifies for
Matrix (below) and has something new that week. A member with nothing new
gets neither an email nor a DM.

Shape (*provisional* on the wording):

```
Your weekly digest from <meta title>

• <thread title>
• <thread title>
• <thread title>

Read the whole digest: <web digest link>

Reply stop to stop these messages.
```

* **The heading** names the community by the meta title, as the email's
  subject does.
* **Three titles**, each linking to the thread on Mbin: the first three
  threads of that member's digest in the order the email shows them, the
  featured thread first. Microblog posts have no title and are skipped
  (*provisional*). Federated items appear by title as they do in the email,
  never quoted. Fewer than three threads shows what there is; a digest with
  only microblog posts shows the heading, the link and the footer.
* **The link** is the member's web digest for that week.
* **The footer** says how to stop, on every message, not just the first.

It is sent as `m.notice` with a plain `body` and an HTML `formatted_body`
(*provisional*), so clients render the links and a well behaved bot never
answers it.

## Who qualifies for Matrix

A member, as the digest defines one (a local Mbin account with a Pocket ID
subject, never banned, deleted, bot or remote), who also has an account on
the deployment's homeserver linked to the same Pocket ID subject, and whose
Matrix setting is on.

A verified email address is **not** required for Matrix (*provisional*):
Matrix does not use it. An email unsubscribe has no effect here.

**Finding the member's Matrix account.** MAS provisions every account from
Pocket ID and records the upstream subject on the account's upstream link.
The service asks MAS's admin API which user is linked to this subject at this
deployment's upstream provider, and reads that user's username, which is the
localpart: the member is `@<username>:<server_name>`. It never guesses from
the Mbin username or `preferred_username`, which can differ. A member with no
linked Matrix account is skipped silently, and asked again the next week. The
answer may be cached in the service's own database; a cached account that
MAS no longer has is looked up again.

The upstream provider's id is `stableULID(<synapse app name>)` in the synapse
blueprint's `mas.yaml`, so the toolkit can render it for the service.

## The room

One room per member, created by the bot. Created the first Monday a member
qualifies, or when a member asks to start (below).

* **Created as a direct chat**: `preset: private_chat` (never
  `trusted_private_chat`, which gives the member the bot's power level), `is_direct: true`, the member invited, `m.federate: false`,
  and the bot's display name `Digest Bot` (*provisional* on the name's
  spelling and on the avatar, which is none).
* **Never encrypted.** The room is created with no `m.room.encryption`
  state, and its power levels require level 100 to send
  `m.room.encryption`, which only the bot holds; the member is at 0.
  Synapse refuses a member's attempt to turn encryption on, from any client.
  The homeserver blueprint leaves `encryption_enabled_by_default_for_room_type`
  unset, which is off, and must keep it that way; the service checks the
  room's state after creating it and abandons a room that came out encrypted
  (*provisional* on checking).
* **History visible from the invite** (`history_visibility: invited`), so a
  member who accepts the invite late sees the messages sent while it waited.
* A member who never accepts the invite keeps getting the Monday message in
  it; a pending invite is not a no.

## The member's setting

Each member has one Matrix setting, **on** or **off**, in the service's own
database, keyed by the Pocket ID subject like everything else there. It
starts on. Beside it the service keeps the member's current room, if any.

| What happens | Setting | Room | The bot replies |
|---|---|---|---|
| The member sends `stop` in the bot's room | off | kept | "Stopped. Reply start to get them again." |
| The member sends `start`, `resume` or `restore` in the bot's room | on | kept | "Started. Your next digest comes on Monday." |
| The member sends anything else in the bot's room | unchanged | kept | one line naming `stop` and `start` |
| The member leaves the room, or rejects its invite | off | forgotten | nothing; the bot leaves too |
| The member invites the bot to a room of their own | on | a new one | the bot rejects the invite, creates a new room, invites the member and says "Started. Your next digest comes on Monday." |

* Commands are matched case insensitively after trimming whitespace and
  surrounding punctuation, as the whole message (*provisional*). `stop` while
  already off and `start` while already on answer the same way and change
  nothing.
* **Leaving counts as stop.** The bot never recreates a room or invites
  again on its own after a member left or rejected one. Only the member
  turning it back on does: by inviting the bot, or by the later web toggle,
  which would set the setting on and have the bot create a fresh room. The
  bot never rejoins a room it left.
* The bot only reads events in rooms it created and still holds a member's
  record for. Messages anywhere else are ignored.
* Ignoring its own messages and `m.notice` events, the bot never answers a
  notice, so two bots cannot loop.
* An invite from anyone who is not a member (no Mbin account with a subject,
  a remote user) is rejected with nothing else done.

## The web digest

Each sent digest, email or DM, can be read at
`<engagement public URL>/d/<token>` (*provisional* on the path).

* **It is what was sent that week, frozen.** The service stores the digest it
  built for that member and week (or enough to render it identically) at send
  time and renders the page from that. It does not recompute, so the page and
  the DM's three titles always agree. Same content as the email, same names:
  the heading, the numbers, the featured thread, the list, the button to
  Mbin. No unsubscribe link: this page is not the email (*provisional*).
* **The token names the member and the week**, is signed with
  `ENGAGEMENT_SIGNING_KEY`, and cannot be read as an unsubscribe token or an
  unsubscribe token as it: the two carry a different purpose in what is
  signed.
* **It expires** four weeks after the send (*provisional*), and the stored
  digest is deleted then too. An expired, unknown or altered token gets the
  same 404 as a bad unsubscribe link.
* **No login and no gate.** The engagement app is already ungated, so the
  member's DM link opens wherever they tap it.
* **`GET` and `HEAD` only.** Opening it records nothing: the service still
  keeps no reading history. The page carries `Referrer-Policy: no-referrer`
  and `X-Robots-Tag: noindex`, and `Cache-Control: private, no-store`
  (*provisional*), since the URL is a credential to one member's digest.
* The gateway forwards `/d/` as well as `/u/` and `/healthz`.

## Sending

* **Same run, same lock.** Matrix sends happen in the Monday run that sends
  the email, by the one copy that sends (the existing single sender rule).
  A member's DM is recorded per member per week, separately from their
  email, before the next member is started.
* **No duplicates.** Each DM is sent with `PUT
  /rooms/{room}/send/m.room.message/{txnId}` and a transaction id derived from
  the member and the week, so a retry after an unknown outcome is
  deduplicated by the homeserver.
* **Failures.** A homeserver that cannot be reached stops the Matrix half of
  the run and retries it the same day, as an unreachable mail relay does,
  without holding up the email. A refusal for one member (their account was
  deactivated, the room is gone) is recorded as failed for that member and
  week and not retried; a vanished room is forgotten so that the next week
  creates a new one, provided the member did not leave it.
* **Room creation is slower still.** Synapse's default room creation limit
  allows about one room a minute after a burst, so the first Monday a
  large community turns the bot on, not every member gets a room that day.
  The Matrix half stops when the send day ends; a member not reached gets
  their first DM the next week (author's call).
* **Paced, never exempt.** Founder decision, 2026-10-27: the bot is not a
  Synapse admin and is not exempted from rate limits. The run staggers its
  sends to stay inside Synapse's default message rate for a user
  (`rc_message`), and a `429` is honoured by waiting the `retry_after_ms` it
  names, not counted as a failure. A few hundred members take minutes; the
  run has the whole day. The bot's replies to commands share the same
  budget, so a reply during the run may wait its turn.

## Listening

The service keeps a `/sync` long poll open as the bot for as long as it runs,
on the one copy that holds the sender lock (*provisional*: one listener, so
two copies never both answer). The sync token is stored in the service's own
database, so a restart neither replays old commands nor loses new ones.
A reply is handled within a few seconds; nothing about it waits for Monday.

## The bot account

One account, localpart `digest-bot` by default, display name `Digest Bot`
(*provisional* on the spelling), on the deployment's homeserver. It is not a
member, never logs in through Pocket ID, has no upstream link, and is not a
Synapse admin. It is created by `deployment apply` or by the service itself
at start through MAS's admin API, never by hand (the builder picks which).

**The localpart is a setting of the engagement app**, `bot_localpart`,
defaulting to `digest-bot` (author's call). MAS takes a member's localpart
from their Pocket ID `preferred_username`, so on a deployment where Synapse
already has members the name may be taken. When it is taken by an account
the service did not create, the bot does not start, and apply (or the
service's log, whichever creates it) says the name is taken and names the
setting to change. It never adopts an existing account.

### The MAS admin client

The service needs MAS admin access, to look members up by Pocket ID subject
and to create the bot account and obtain its access token. MAS grants it to
a client declared in its configuration with the `urn:mas:admin` scope,
through the client credentials grant.

**Declared by the synapse blueprint, with a secret shared with the
consumer.** Founder decision, 2026-10-27. The synapse blueprint's `mas.yaml`
declares one admin client for each engagement app that consumes the
homeserver, and the engagement app's `.env` carries the same client id and
secret. Today a role passes only non-secret values from provider to consumer
(`.Linked`, `internal/render/appview.go`), so this needs two toolkit
changes, both general rather than engagement specific:

1. **A provider's templates can see the apps that consume its role**, with
   what each consumer exposes to it.
2. **A secret shared across a role**: generated once, stored once in the
   deployment's secrets, readable by both the provider's and the consumer's
   templates, and rotatable with `secrets rotate`, which re-renders and
   restarts both apps. A rotation that updates one side only is the failure
   to design out.

The builder designs both in the blueprint schema, following *A config key is
placed, not interpreted* in `README.md`, documents them in
`internal/blueprints.Parse` with every other key, and covers them with the
validate checks a third party catalog author needs.

### The admin API stays off the internet

Founder decision, 2026-10-27. MAS's listener does not mount the `adminapi`
resource today, and the gateway sends everything outside `/_matrix` and
`/_synapse` to MAS, so mounting it on that listener would publish
`/api/admin/v1/*`. Instead:

* **A second MAS listener** carries `adminapi` only, bound to the mesh
  address, never a public one. The web listener does not mount it.
* **The gateway refuses `/api/admin` and everything under it** on every
  hostname the synapse app answers on, so a later change that mounts it on
  the web listener still does not publish it.
* **The service reaches it over the mesh.** The consumed role carries the
  admin listener's mesh address as a linked value, which the toolkit renders
  into the engagement app's `.env`. The engagement app is clustered and
  Synapse is pinned, so the address names the site Synapse runs on.

### The bot's access token

The service obtains the bot's client server API token through the admin
client, as a personal session with the narrowest scope that lets it sync,
create rooms, send and manage its own memberships:
`urn:matrix:client:api:* urn:matrix:client:device:DIGESTBOT`, never
`urn:synapse:admin:*`. Synapse needs the device for transaction ids to
deduplicate a send; the bot never uploads keys to it, so the device takes no
part in encryption. The token is stored in the service's own database. It renews it
when the homeserver refuses it.

The builder verifies each MAS admin API endpoint used (user lookup by
upstream link subject, user creation, issuing the bot's session or token)
against MAS's source and pins the reading in a comment with the tag read, as
the synapse blueprint does for every key it uses. The builder records the
oldest MAS release that has all of them (author's call): the service checks
MAS's version at start and logs once that Matrix is off when it is older,
and the synapse blueprint's README names that version. If an endpoint does
not exist in any 1.x release, that is a question back to the founder, not a
workaround.

## Configuration

The `engagement` blueprint consumes the `matrix-homeserver` role, as Element
does, with the same `homeserver` setting to pick one when there are several.
No homeserver: the Matrix half is off, the service renders no Matrix
variables, and nothing else changes. The service starts the bot only when
every Matrix variable is set, and logs once that it is off otherwise.

New variables (*provisional* on names), each documented in the service's
README beside the existing ones:

| Variable | Meaning |
|---|---|
| `ENGAGEMENT_MATRIX_URL` | the homeserver's `base_url` |
| `ENGAGEMENT_MATRIX_SERVER_NAME` | its `server_name`, the name in every identifier |
| `ENGAGEMENT_MAS_ADMIN_URL` | MAS's admin listener, on the mesh |
| `ENGAGEMENT_MAS_CLIENT_ID`, `ENGAGEMENT_MAS_CLIENT_SECRET` | the admin client, shared with the synapse app |
| `ENGAGEMENT_MAS_UPSTREAM_PROVIDER` | the upstream provider id members are linked through |
| `ENGAGEMENT_MATRIX_BOT_LOCALPART` | the `bot_localpart` setting, default `digest-bot` |

The service reaches the homeserver's client server API by its public
hostname through the gateway (*provisional*), and MAS's admin API over the
mesh only, as above.

## The service's own database

New, beside unsubscribes, sends and shown threads (shape left to the
builder):

* each member's Matrix setting, their Matrix user id, and their current
  room;
* the record of each week's DM per member, separate from the email's;
* each sent digest's stored content, for the web page, deleted at expiry;
* the bot's sync token.

Email unsubscribe forgets which threads a member was shown, as today. With
Matrix still on, the shown threads now also matter for the DM. Founder
decision, 2026-10-27, that the two channels are independent, so
*provisional*: shown threads are forgotten only when both channels are off.

## Docs and checks

* The service's README describes the bot, the commands, the web page, the
  new variables and the new tables, in the same voice as the rest.
* The `engagement` blueprint's section in `README.md` says it consumes the
  homeserver role and that the bot is then on.
* The existing spec's non goal "No web interface" is amended to name the web
  digest as the one exception, and its "Who receives one" points here for
  Matrix.
* A validate check refuses a deployment whose engagement app picks a
  homeserver setting that names no homeserver.

## Release order

The toolkit first, with consumer visibility and the shared secret, released;
then the service with the bot and the web page; then the synapse blueprint's
admin client, admin listener and gateway rule, in a catalog release that
requires that toolkit release. Core floats to its newest release, so a
synapse template that used the new keys before a toolkit understood them
would break every older toolkit. A service with no Matrix variables runs
exactly as v0.1.0, so the service can ship before the catalog.

## Left to the builder

* The schema, the token format beyond what is stated, and the web page's
  markup (the email's template, reused, is the obvious start).
* The schema shape of consumer visibility and the shared secret, within
  *The MAS admin client*.
* Whether the bot account is created by `apply` or by the service.
* How the run spaces its sends within Synapse's default rate.
* Which Matrix client library, if any; plain HTTP against the client server
  API is enough for what this needs and carries no crypto.

## Open

* The web toggle that turns DMs back on, later.
* Whether a member should be able to stop the DMs from the web page as well.
