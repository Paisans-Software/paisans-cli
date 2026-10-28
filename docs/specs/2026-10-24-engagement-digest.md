# A weekly digest email for every member

Status: approved by the founder in session, 2026-10-24. Implemented by
`feat/engagement-blueprint` here, `feat/weekly-digest` in
`paisans-engagement-service` and `feat/engagement-image` in
`paisans-blueprints`. A line marked *provisional* is the author's call
and open to correction.

## Why

A member who misses a week of a community misses the conversations that
mattered in it, and nothing brings them back. Mbin sends mail for little more
than account recovery, and its notifications reach only members who already
follow the thread or the magazine. A short weekly email that says "this is
what happened, and this is what you have not seen yet" is the cheapest way to
bring a lapsed member back to a conversation while it is still alive.

It has to be personal to be worth opening. An email that lists threads the
member already read, or wrote, teaches them to ignore it. So the digest
leaves out what each member has already taken part in, and it is not sent at
all in a week where nothing is left.

## What gets built

Three pieces, each in its own repository:

1. **`paisans-engagement-service`**, a new repository in the organisation: a
   Go service and its container image. It holds the schedule, the queries
   against each app it reads, the email, and the one public endpoint,
   unsubscribe. Go, *provisional*, because every other
   toolkit service is Go and shares `pocketid-go` and the release workflow.
2. **An `engagement` blueprint** in `paisans-blueprints`: the container, its
   own database, its public route, and the mail settings it reads.
3. **A toolkit change** in `paisans-cli`: a way for one app to be given a
   read only role on another app's database. No blueprint does this today.

## Goals

* Every member gets one email a week, automatically, once the stack enables
  the blueprint. There is no sign up.
* The email is personal: it leaves out what the member has already joined.
* Unsubscribing takes one click from the email, with no login.
* The service reads Mbin and changes nothing in it.
* A second app (a forum, a wiki) can be added as a source later without
  changing the email or the schedule.

## Non goals

* **No push notifications.** Mbin's Web Push fires only for its own
  notifications, and there is no API for a custom one. Push waits until that
  changes.
* **No web interface beyond the web digest.** The service publishes the
  unsubscribe link and the web digest, the one page a member reads, linked
  from the Matrix message (`docs/specs/2026-10-27-engagement-matrix.md`,
  *The web digest*). Links in the email point straight at Mbin. No admin
  panel, no preferences page, no landing page, no archive of past digests.
* **No per member settings.** No timezone, no choice of day, no frequency.
  One schedule for the whole deployment.
* **No reading history.** The service never records which threads a member
  viewed. It does not read access logs and it does not ask Mbin to track
  views. "Already seen" is inferred from what the member did, below.

## Anti goals

These would make the digest a failure even if it technically works:

* Showing a member content from a magazine or a user they blocked, a
  magazine they are banned from, or anything deleted, moderated or marked
  NSFW.
* Sending a member two digests for the same week.
* Sending to an address that unsubscribed.
* Sending a digest with nothing new in it.
* Holding a credential that can log in as a member.

## Where the data comes from

**A read only role on Mbin's Postgres database.** The service builds every
member's email, and Mbin's API shows each caller only its own votes,
bookmarks and notifications, so the API cannot answer "what has this member
already joined" for anyone but the caller. The role is read only at the
database, not by convention: the service cannot write to Mbin even if its
code tries.

That role is the toolkit change. The `engagement` blueprint declares which
app it reads (Mbin, for now), and `apply` creates the role and renders its
credentials into the engagement service's `.env`. The exact shape of the
declaration and its checks is left to the builder, following *A config key
is placed, not interpreted* in `README.md`.

**The source app must be clustered.** The grant is created in the
cluster's Postgres, where `apply` already creates each clustered app's role
(see *`deployment apply` creates each clustered app's role and database* in
`README.md`), and the service reaches it through the local HAProxy like any
clustered app (*Rule 2*). A pinned app's database sits on its own compose
network with only the role its image created, so a pinned source is refused
by a check (Eg: `engagement-source-not-clustered`) whose message says the
digest reads clustered apps only. This limit is documented in three places:
the `engagement` blueprint's section in `README.md`, the blueprint's own
comments beside the check, and the `paisans-engagement-service`
repository's `README.md`, so a deployer meets it before deploying rather
than at `validate`.

**The grant is an allowlist, never the whole database** (*provisional*).
Mbin's database holds credentials: the `sessions` table (sessions are
stored in Postgres through `PdoSessionHandler`), remember me tokens, OAuth
tokens, and the password hash and TOTP secret and backup codes on `user`.
A role that can read any of those can log in as a member, which is an anti
goal. So the source declares the tables it reads, `SELECT` is granted on
those alone, and on `user` it is granted column by column, leaving out
every secret column. A grant that names a credential table or column is
refused by a check. The allowlist lives beside the source's queries, so a
Mbin release that changes the schema fails the grant loudly at `apply`
rather than widening it.

The service also has **its own database** for its own state: who
unsubscribed, which member was sent which week, and which threads each
sent digest showed.

## What the email says

The layout was chosen from mockups in session: a headline, the community's
week in numbers, one featured thread, then a short list.

From top to bottom:

1. **A heading with the week's dates.**
2. **Three numbers for the whole community:** new threads, comments and new
   members over the week. These are the same for every member, and they
   count the deployment's own content and accounts only.
3. **One featured thread:** the hottest local thread this member has not
   joined. It is never federated content.
   It shows its magazine, its comment count and how many people took part,
   and a short excerpt from its highest voted comment.
4. **Three to five more threads** in a compact list, each with its magazine
   and one figure (*provisional* on the count).
5. **A button** to the Mbin front page.
6. **An unsubscribe link** at the bottom.

**Names.** The meta title, `KBIN_META_TITLE`, appears everywhere in the
email that names the community: the subject line, the heading and the
footer. The button alone uses the instance title, `KBIN_TITLE`: "See
everything on `<instance title>`". The blueprint computes both from the
configuration the same way the Mbin blueprint does (see
`docs/specs/2026-10-18-mbin-locked-instance-settings.md`: the instance
title is the app's display name, the meta title is "`<display name>` ·
`<community.name>`"). Both are locked settings in the paisans Mbin fork, so
the configuration is the one place they live, and the digest never
disagrees with the site.

**Format** (*provisional*): one message with an HTML part and a plain text
part. The HTML uses inline styles and a single column so that it reads the
same in the common mail clients.

## Which threads, for whom

### What counts as a thread

Both kinds of Mbin content: threads (links and articles, the `entry` and
`entry_comment` tables) and microblog posts (`post` and `post_comment`).
Mbin keeps them in separate tables, each with its own votes, favourites,
notifications and URL shape, so every rule below applies to both, and the
week's numbers count both: "new threads" counts entries and posts,
"comments" counts comments on either. In the email they appear side by
side, ranked together; a microblog post is labelled as one (*provisional*
on the label).

### Hot

Threads and microblog posts from the deployment's own magazines, plus
federated content within the limits below, active within the past seven
days. How they are scored is left to the builder: votes, comment count and
the number of distinct people taking part are the inputs the conversation
named. A thread in a magazine the member subscribes to ranks higher for
that member.

### Federated content

Mbin stores what federated in from other servers in its own database (a row
with a non null `ap_id`), and a local member's votes, comments and
bookmarks on it are local rows, so *Already seen* applies to it unchanged
and its links point at the deployment's own copy. Founder decision,
2026-10-24: it appears in a member's digest within three limits, because
the community's moderators never reviewed it and an email sent in the
community's name reads as endorsing what it shows.

* **Only from magazines the member subscribes to themselves.** A remote
  magazine nobody here follows never reaches anyone's digest, and one
  member's subscription never puts it in another member's.
* **Never the featured thread.** It appears in the compact list only.
* **Never quoted.** No excerpt from a remote post or comment appears in the
  email; a federated item shows its title, its magazine and one figure.

Federated content is scored so that it does not crowd out local
conversation: this server sees only the votes and comments that reached
it, and a busy remote magazine would otherwise outrank everything local.
How it is weighted or capped is left to the builder. Every rule in *Always
excluded* still applies, and a remote item's NSFW flag is whatever its
origin set.

### Already seen

A thread is **dropped** from a member's digest if the member did any of:

* wrote it, or commented anywhere in it;
* voted on it or boosted it;
* bookmarked it;
* had a notification about it and opened it;

A thread is **ranked lower, not dropped**, if it is in a magazine the member
subscribes to and the member visited Mbin during the week. Mbin's
`user.last_active` is the only visit signal, and it updates at most once a
day, so it says the member was there, not that they saw this thread
(*provisional*).

### Already sent

Founder decision, 2026-10-24: a thread an earlier digest showed the member
comes back only when it has new comments this week, and then it is shown by
this week's activity. Its excerpt is the best comment posted this week, and
its figure is its new comments this week rather than its total. A thread
that came back for votes alone has nothing new to show and is left out. A
returning thread is ranked by this week's comments and people alone, so its
history does not carry it, and it may still be the featured thread
(*provisional* on the ranking and on leaving out a vote-only return).
Unsubscribing forgets which threads a member was shown.

### Always excluded

Whatever the score, a member never sees:

* anything from a magazine or a user they blocked;
* anything from a magazine they are banned from;
* anything deleted, removed by a moderator, or marked NSFW;
* anything from another server, unless it passes *Federated content*
  below.

### Who receives one

Local Mbin accounts with a verified email address. Never banned, deleted,
unverified, bot or remote accounts, and never an address that unsubscribed
(*provisional* on the list).

If every candidate thread is dropped for a member, that member gets no email
that week.

**A member is their Pocket ID identity** (*provisional*): the OIDC subject,
which Mbin stores as `user.oauthOidcId`. Every row in the service's own
database (unsubscribes, sends) and every unsubscribe token names
that subject, never an Mbin user id, so a second source that knows the same
person by the same subject joins without a migration. An Mbin account with
no subject is not a member for the digest's purposes and gets no email.
One deployment runs one digest, however many sources it reads.

The same members may also receive the week on Matrix, from the Digest Bot.
Who qualifies for that is in `docs/specs/2026-10-27-engagement-matrix.md`,
*Who qualifies for Matrix*.

## When

**8:00 in the server's local time, once a week.** No configuration: one
time for the whole deployment. "Server" means the host the sending copy
runs on: the container takes the host's time zone (*provisional*: the
blueprint mounts the host's `/etc/localtime` read only), since containers
otherwise run in UTC and nothing in the toolkit sets a zone. A host left at
UTC sends at 08:00 UTC. The day is Monday, covering the seven days
before it (*provisional*; the conversation fixed the time, not the day).

**Exactly one copy runs the send.** A deployment with several sites must not
email a member once per site. The blueprint either pins the service to one
site, or lets it run clustered and has the copies agree on one sender (Eg:
a Postgres advisory lock in its own database). The builder picks, with
reference to *Rule 5* in `README.md`.

**A send is recorded per member per week** in the service's own database
before the next member is started, so a restart or a retry partway through
resumes without sending anyone a second copy.

## The public endpoint

The service answers on its own hostname through the gateway, without the
Pocket ID gate: a member clicking from their inbox may not be signed in.

### Unsubscribe

* Every email carries a link containing a token signed by the service. The
  token names the member, so it needs no login and cannot be altered to
  unsubscribe someone else.
* One request unsubscribes. There is no confirmation step to get past. The
  response is a plain sentence saying it worked, which is the only page the
  service renders.
* The same URL goes in the `List-Unsubscribe` and `List-Unsubscribe-Post`
  headers (RFC 8058), which the large mail providers require of bulk mail.
  A one click `POST` and a `GET` from the link both unsubscribe.

## Mail

The blueprint reads the deployment's `smtp:` block like the other blueprints
that send mail (see *Mail is declared once and overridden per app* in
`README.md`), with the same per app override. It joins
`blueprints.SendsMail`. A deployment with no mail host renders the service
but the blueprint warns, as `mbin-without-smtp` does for Mbin, since a
digest that cannot be sent is a misconfiguration worth saying out loud.

## More apps later

Each app the service reads is one source in the code. A source answers two
questions for a week: which items were hot, and which of those a given
member already joined. Mbin is the only source now. The email and the
schedule do not know which source an item came from beyond its name and
link, so a second source adds one module and one read only grant.

## Left to the builder

* How hot threads are scored, and how federated content is weighted or
  capped against local content.
* The shape of the read only grant in `paisans-stack.yaml`, and the checks
  that refuse a bad one.
* The engagement service's own placement, provided exactly one copy sends.
* The unsubscribe token's format and signing key. The
  key is a secret of the blueprint, rotatable with `secrets rotate`.
* The service's own schema.
* The mail template's markup.

## Open

* The day of the week (Monday is provisional).
* Reading a pinned source app, which needs a network path to its compose
  project and a role created inside its own Postgres.
