# Paisans

Toolkit for installing and operating a paisans community stack: a private,
federated community on hardware its organizers control.

**Status: in development.** It runs a staging deployment; nothing runs on it
in production yet.

Written in **Go**, distributed as a single static binary so an operator needs no
runtime. [docs/decisions.md](docs/decisions.md) records why.

## What it does

It installs a complete community stack on every site `paisans-stack.yaml` declares,
one machine or several, then adds sites later as a manually invoked, additive
step, without touching application configuration.
Every command that changes a host prints its plan and changes nothing on a
server unless given `--execute`. One dry run writes a local file: `paisans
apply` without `--site` may add missing generated secrets to the secrets file,
and says so when it does. Each step is one line; `--verbose` (`-v`) shows the
reasons, values and requests behind it.

```
paisans create                             # a new deployment's paisans-stack.yaml, from questions
paisans init                               # an id, a mesh subnet and the secrets; offers to admit each site not admitted
paisans site request --site vm > vm.admit  # the request the host's owner admits the site from
sudo paisans site admit --request vm.admit --execute   # on the host, by its owner: the deployment's own user and rootless Docker
paisans site prepare --site vm --execute   # the user's keys, configuration store and daemon settings on an admitted host
paisans site prepare --all --execute       # the same on every site, one at a time
paisans deployment apply --execute                    # the whole deployment, from paisans-stack.yaml to a running stack
paisans deployment apply --site vm --execute          # render the site and bring it up
paisans site add home-b --execute          # join another data site to the running cluster
paisans app remove blog --execute          # take an app that has left paisans-stack.yaml off its hosts
paisans site remove home-b --execute       # take a site out of the deployment
paisans doctor                             # what is unhealthy, and how to recover
```

`paisans help` lists every command in a line each, `paisans help <command>`
(or `paisans <command> --help`) gives one command's usage, details and flags,
and [docs/commands.md](docs/commands.md) describes each one: what it does,
when to run it on its own rather than through `paisans deployment apply`, its flags, and
what it changes.

## How Paisans differs from YunoHost

YunoHost and Paisans both put self-hosting within reach of people who are not
full-time sysadmins. They answer different questions.

* **YunoHost:** "I want one server at home running a lot of apps, managed from
  a browser."
* **Paisans:** "A group wants a private, federated community that runs across
  several members' machines, survives losing one, and grows without a data
  migration."

[docs/guides/compared-to-yunohost.md](docs/guides/compared-to-yunohost.md)
sets out what the two share and where they differ.

## Design rules that everything else follows from

### Rule 1: the cluster always runs, even when it has one node

A single-site install runs Postgres under Patroni, with etcd and a local
HAProxy, as a cluster of one, so that a second site is an addition and never
a *conversion* performed on live member data. Running the control plane from the start
costs a single-site adopter roughly 150 MB and some extra moving parts. It buys
an upgrade path that cannot corrupt anything.

### Rule 2: applications always talk to a local HAProxy

Every stack with `cluster` placement connects to the HAProxy on its own site,
at that site's mesh address and port 5000 (Eg: `10.44.0.1:5000` on `home-a`),
from the first install. That HAProxy holds a backend list that initially has one
entry. Adding a site grows that list. No application configuration changes,
ever: an instance is only ever given its own site's address, and that address
does not move when another site joins. This one indirection is what makes a
second site an operation rather than a project.

The address is the mesh one rather than `127.0.0.1` because every application
runs in a bridge networked compose container, and inside one of those the
loopback address is the container itself, so a DSN naming it reaches nothing.
Host networking for the apps would fix that and cost the isolation of each
stack's own sidecars, and two kinds listening on the same port would collide.
The Docker bridge gateway would work only if HAProxy bound an address that
differs per compose network and does not exist until Docker creates it. The
mesh address exists as soon as the deployment's WireGuard interface is up, before anything else starts, and
is already declared in the configuration. HAProxy listens on the mesh address
alone, for a command run on the host as much as for the applications (see
"Egress keeps the deployment off the owner's network").

Pinned stacks talk to their own database directly and are not affected — see
rule 5.

### Rule 3: object storage from the first install

The same logic as rule 1, applied to files. **Garage ships out of the box**,
single-node on the first install (`storage init`, which a whole `deployment apply` runs),
replicated across sites when one is added (`storage add`).

Starting with media on local disk and moving it later is a migration, not an
addition — and for a federated instance it is a migration with consequences
outside the deployment. Mbin's own procedure is sync, shut down, sync again, and
its documentation warns that changing media URLs breaks links on remote
instances that have already cached them. Remote servers hold references we
cannot recall.

What Garage actually absorbs is narrower than "all file storage", and the
toolkit should not overstate it:

| Stack | S3 support | Notes |
|-------|-----------|-------|
| Mbin | full | `S3_KEY`/`S3_SECRET`/`S3_BUCKET`/`S3_REGION`/`S3_ENDPOINT`. The Docker image's entrypoint then switches `public_uploads_filesystem` to the S3 adapter itself; only a bare metal or VM install edits `config/packages/oneup_flysystem.yaml` by hand. Upstream strongly advises a media reverse proxy so URLs stay stable across provider changes |
| Wiki.js | none needed | Wiki.js 2.x writes every upload to its `assetData` table in Postgres and serves it from there (`server/models/assets.js:128-139,226-228` at `v2.5.316`); its S3 storage modules only push copies outward (`supportedModes: [push]`). So the `wikijs` blueprint has no bucket and no media hostname, and the database's backup is the wiki's |
| Synapse | partial | `synapse-s3-storage-provider` is a *storage provider* that supplements the media store. **A local media directory is still required.** `store_synchronous: True` writes to S3 immediately; the bucket prefix cannot be changed once media exists |
| Pocket ID | full, plus better | `FILE_BACKEND` takes `filesystem` (default), `s3`, or **`database`**. See the note below — `database` is the recommendation |
| WriteFreely | full, and public | the kind means the [writefreely-wisp](https://github.com/josephquigley/writefreely-wisp) fork, which adds `[storage] type = s3`. With S3 the fork is direct only: it writes images and never serves them, requires `[storage] image_url_base`, and redirects its own `/uploads/` route there, so the blog's bucket is served on the blog's media hostname like Mbin's. That needs a fork build containing writefreely-wisp#171; an older one ignores the key and streams images itself |

**Pocket ID should use `FILE_BACKEND=database`, not S3.** Its uploads are
profile pictures and admin-uploaded branding — on a real deployment, about a
megabyte in total. Putting them in Postgres means they ride streaming
replication with everything else: a promoted site has them already, with no sync
job and no reconciliation. It also removes a dependency from the one service
that gates every other one — if object storage is unavailable, sign-in still
works. Consistency with the other stacks is worth less here than keeping the
identity provider's dependency list as short as possible.

Left on the default `filesystem` backend, `data/uploads` holds
`profile-pictures/<userId>.png`, generated initials avatars under
`profile-pictures/defaults/`, and `application-images/` (favicon, background,
email logo). None of it is in the database. A site promoted without it serves
broken avatars and silently reverts to stock branding mid-incident — not an
outage, but an alarming one to look at, and the branding is configured state
that an admin set deliberately.

That leaves **Synapse as the only stack with unavoidable node-local state**,
because its S3 support supplements the media store rather than replacing it. Its
local media directory has to be replicated out of band, or accepted as lost on
promotion. Either is defensible; leaving it undecided is not.

**Each app that stores objects serves them on a hostname of its own**, a
sibling of the app's: an app at `talk.example.org` serves its media at
`talk-media.example.org`. The name is derived, `<label>-media.<domain>` from the
first label of the app's hostname, and an app can choose another under
`hostnames.media`. Every one of them needs a DNS record pointing at the
gateway, the same as the app's own hostname. The endpoint an app writes through
is a mesh address, and a browser cannot reach one: without a public hostname
the toolkit would render an app that uploads successfully and publishes a URL
nothing outside the mesh can fetch, and a federating instance that has cached
such a URL keeps it.

One hostname per app, rather than one for the deployment with a bucket as a
path under it, because a hostname is the unit DNS can move. Pointing one app's
media at a CDN, another provider or another Garage is a record change for that
app and no other; a path under a shared hostname can only move with every other
app's. Two apps' uploads on two hostnames are also two browser origins, so a
file one app stored cannot script against another's.

The name is a **sibling, never a child**, and never a name for the backend.
`media.talk.example.org` sits under the app's own host, so a cookie the app
scopes to that host reaches every media request and a file served there could
set cookies the app reads back; that is refused as
`media-hostname-under-an-app-hostname`. `garage.` or `s3.` would name what
answers today rather than whose objects these are, which is the one thing about
the URL that should never change. A declared name must be a hostname
(`media-hostname-is-not-a-hostname`) under `community.domain`
(`media-hostname-outside-the-domain`), because its certificate is issued over
DNS-01 against the deployment's own zone. A derived name is checked the same
way, and so is a clash with any other hostname in the deployment
(`duplicate-hostname`): a derived name nobody wrote is still a site address,
and it is the claim most likely to collide unseen.

These checks live in `internal/validate` rather than as structural errors in
`internal/config`. `internal/kinds`, which knows which kinds use object
storage, already imports `internal/config`; `config` asking `kinds` back would
be an import cycle.

**Media is public to anyone holding the link, and a media hostname is never
gated.** A federating server fetching an image is a machine with no account; it
will not sign a request and it will not follow a redirect to a passkey prompt,
so a gate would break every federated image. That holds for the app whose own
hostname is gated, too: the gate stays on the app's hostname and does not
follow it to its media.

**A public bucket's objects are served without a credential, and a private
bucket's are not.** Garage's S3 API refuses every unauthenticated request
outright, with `403 Forbidden: Garage does not support anonymous access yet`,
and that is the only mode it has. Garage's separate web endpoint is the one
that serves an object anonymously, so the toolkit renders it as `[s3_web]` in
`garage.toml` and grants `garage bucket website --allow` on public buckets
only.

The gateway is what joins the two, one site block per media hostname:

| Kind | Objects | The media hostname goes to | Published as |
|------|---------|----------------------------|--------------|
| Mbin | public | Garage's web endpoint on 3902, `Host` rewritten to `<bucket>.web.garage.internal` | `KBIN_STORAGE_URL=https://<media host>` |
| WriteFreely (the fork) | public | the same | `[storage] image_url_base = https://<media host>` |

The rewritten `Host` is an internal suffix that resolves nowhere on purpose, so
there is no DNS record or certificate for it. Because a hostname is one bucket,
the path is the object key and nothing is stripped from it.

**Every public media hostname sends `Content-Security-Policy: default-src
'none'; style-src 'unsafe-inline'; sandbox` and `X-Content-Type-Options:
nosniff`.** Mbin and the blog store what they are given byte for byte, an SVG
that carries script among it, and no S3 provider can store a response header on
an object, so the gateway is the only place one can be set. The sandbox keeps
such a file inert when it is opened directly rather than through an `<img>`.

Which kinds are public is a property of the kind, in `internal/kinds`, not a
setting. A kind whose objects only signed in people read has its bucket
never routed to the web endpoint at all; the core catalog has none today, since
Wiki.js keeps its uploads in Postgres and has no bucket. There is no configuration
key that can get this wrong in either direction.

`TestEachAppsMediaHostnameServesOnlyItsOwnBucket` in `internal/garage` is what
keeps that honest, against a real Garage behind a real Caddy.

### Rule 4: the domain is a one-way door

Federation identity is the hostname. Mbin's actor keys are bound to it, and
remote instances have recorded it. Changing a community's domain after it has
federated is a rebuild, not a rename.

`init --domain` should say so at the prompt, in plain words. It is the one
choice here that cannot be walked back, and it is made in the first thirty
seconds by someone who has not read anything yet.

### Rule 5: placement is a per-app property, and pinned is the plain case

Not every app can follow a failover. Synapse is the first example — its S3
support supplements the media store rather than replacing it, so a local media
directory is always required — but this is a property every app has, not a
Synapse special case.

**For the `synapse` kind it is not a choice: `placement: cluster` is refused.**
The media store cannot leave the node, and the kind renders its own Postgres
plus the initialisation hook that creates the authentication service's database
beside it, neither of which exists on a clustered site. A clustered homeserver
would render and then meet the missing database at the first sign in. That
refusal is a check of its own, `homeserver-must-be-pinned`, because the kind
does run a Postgres service and the general check would let it through. The
general check, `cluster-placement-without-a-cluster`, refuses `element`, which
is a static client with no server side state, `oauth2-proxy`, whose session is
a cookie, and `uptime`, whose database is a SQLite file: a kind with no
Postgres service has nothing to join, and cluster placement would render it
onto every apps site with storage of its own. `account` is the opposite case:
its state is in the cluster's Postgres and it must outlive a site, so it takes
`cluster` and nothing else (`account-must-be-clustered`).

`writefreely` is not in that list: the kind runs the
[writefreely-wisp](https://github.com/josephquigley/writefreely-wisp) fork,
which adds Postgres and S3 support upstream WriteFreely does not have, so the
blog can join the cluster like any other app. **The fork's image is required,
not interchangeable with upstream's:** upstream's own image rejects the
configuration this kind renders, because it has no `[database]` Postgres
driver and no `[storage]` section at all. See `docs/decisions.md` for the
record.

**"The authentication service's database" is Matrix Authentication Service,
and the `synapse` kind renders it as a second container beside the
homeserver, not as a setting inside it.** Synapse stopped being where a
member logs in: it is a resource server now, and MAS is what owns the login,
holds sessions, and talks to the identity provider on the homeserver's
behalf. The identity provider itself sits upstream of MAS, one hop further
out than every other application's OIDC client, which is why its client
takes a different redirect URI, in the form
`https://<hostname>/upstream/callback/<ulid>`. `deployment apply` creates the client with
that URI, as it does every other app's, and `paisans init --verbose` prints it
under the line naming the sign in clients apply will create, because a client
with the wrong path fails at the end of the first sign in rather than when it
is made. MAS keeps its own Postgres
database beside the homeserver's, which is part of why `placement: cluster`
is refused above: a clustered site has nowhere to put either.

So each stack in the inventory declares where it runs:

| Placement | What it means | Availability |
|-----------|---------------|--------------|
| `cluster` | joins the HA Postgres cluster, follows its primary | survives losing one site |
| `pinned: <site>` | ordinary self-contained deployment at one named location | that location's |

```yaml
placement: cluster
placement: { pinned: vm }
```

**`pinned` is the plain case and needs nothing built.** It is the app exactly as
upstream ships it: its own compose file, its own Postgres container, its own
volumes. No etcd, no Patroni, no HAProxy, no shared cluster. A single-site
install is every app pinned to the only site, which is why this costs a new
adopter nothing. Its uploaded files go to Garage when the deployment runs one,
or with `storage: local` into a directory of its own stack, so that they go
with it too (see *A pinned app may keep its files on its own disk*, below).

`cluster` is the opt-in variant: drop the stack's own `postgres` service, point
it at its own site's HAProxy (rule 2), put its media in Garage. One compose
template per app renders either shape, and the placement picks which.

Pinned is not restricted to a cloud VM — pinning to a home is equally valid. The
cloud VM is one location among several.

**An app that keeps its data outside the cluster can only be pinned, and
`cluster` is refused for it.** Cluster placement means running on every site
with the apps role and sharing one database through the local proxy. An
application storing its data anywhere else gets neither half: it would be
rendered onto each of those sites with its own separate storage, so one
hostname would serve two deployments that diverge the moment anybody writes,
and a failover would move readers between them. Element, oauth2-proxy and
uptime are the cases that exist: a static client with no server side state, a
session kept in a cookie rather than a database, and a monitor keeping a
SQLite file. Pinning any of them is not a downgrade, because a single site is
the only availability its storage allows. WriteFreely is not in this group,
because the fork it runs keeps its data in Postgres; see Rule 5 above.

#### A pinned app may keep its files on its own disk

`apps.<name>.storage` says where an app keeps the files its members upload:
`s3`, a bucket in the deployment's Garage, or `local`, a directory in the
app's own stack under `/srv/paisans/<token>/<stack>/`, bind mounted like the
rest of a pinned stack. Founder decision.

```yaml
apps:
  blog:
    blueprint: writefreely
    placement: { pinned: home-a }
    storage: local
```

**Left out, it is `s3` when `storage.garage.sites` names a site and `local`
when it names none.** The first is Rule 3: object storage from the first
install, so a pinned app that later joins the cluster has nothing to migrate.
The second is because there is no S3 to write to, and S3 settings with no
endpoint would only render an app whose every upload fails.

**`local` is the plain case taken all the way down.** The files sit beside the
app's database in its stack directory, so they move with it in the one `tar`
(*Moving a pinned app*), and nothing about the app depends on another site.
The app has no bucket, no key and no media hostname: `storage init` and
`storage add` provision nothing for it, `dns init` publishes no
`<label>-media` record, the gateway routes no media hostname, and `init`
generates no S3 key. It serves its files on its own hostname, where upstream
serves them: Mbin under `/media` and the writefreely-wisp fork under
`/uploads/`. A gated app opens Mbin's and the
blog's file paths past the gate, exactly as a media hostname is never gated,
because a federating server fetching an image has no gate session. Mbin runs
as an unprivileged user and its image does not take ownership of a bind
mounted upload directory, so in local mode its stack hands that one directory
to the application's user when the container starts.

**`local` is valid only with `placement: pinned`, and `cluster` with `local`
is refused.** A clustered app runs on every apps site, and each would keep its
own directory: an upload on one site would be missing on the others, and a
failover would move readers onto the copy without it. The same refusal covers
a clustered app in a deployment with no Garage, whose default is `local`.

**A pinned app in `s3` may use a Garage node on another site, and nothing
warns about it.** Founder decision. Its uploads then depend on that Garage as
well as on its own site, which is the half-pinned shape described below, for
its files alone; `storage: local` is how to pin the files too.

The key applies to every kind that stores files, and means what each kind can
do:

| Kind | `storage` |
|------|-----------|
| `mbin`, `writefreely` | `s3` or `local` |
| `synapse`, `uptime` | `local` only. They have no S3 form here (Synapse's provider supplements its media store, the monitor keeps SQLite under `/data`), so `s3` is refused; `local` is accepted, since it says what they do anyway |
| `pocket-id` | refused. Pocket ID chooses with its own `settings.file_backend` (Rule 3) |
| `element`, `oauth2-proxy` | refused. They store no files |
| `wikijs` | refused. Wiki.js keeps every upload in Postgres (Rule 3), so there are no files for the key to place |

Changing the key on an app that already holds files moves none of them. The
toolkit does not copy files between a bucket and a disk, and a bucket left
behind stays in Garage with its key in the secrets file. For a federated app
the media URLs change as well, which Rule 3 explains is not recoverable on
remote instances.

#### A pinned app must be pinned all the way down

The tempting half-measure is to pin the app but leave it using the shared
cluster. Do not. It then needs **both** its own location and the cluster site to
be reachable, so its availability becomes the product of two sites — **worse
than either alone** — and every query crosses the WAN.

**A half-pinned app is less available than either site.** Pinning moves a
failure domain; it does not remove one. App, database and media go together or
the placement is a lie.

Backups still centralise to Garage. Only replication and failover are given up.

#### Moving a pinned app

Because a pinned stack is self-contained, relocating it is ordinary work.

**Both hosts need the same architecture** (`sites.<name>.arch`). The tar
carries the app's Postgres data directory, which only a host of the same
architecture can read. Between amd64 and arm64, move the database with
`pg_dump` on the old site and a restore on the new one, and move the files as
below.

```
docker compose down                     # stop first — see below
tar czf stack.tgz /srv/paisans/<token>/<stack>
scp stack.tgz newhost:
# on the new host: untar, open firewall, docker compose up -d
# then update the DNS A record
```

Four things make that sequence true rather than nearly true:

* **Stop the stack before archiving.** Tarring a live Postgres volume produces a
  torn copy that may restore and then fail later. Stop it, or `pg_dump` instead.
* **Use bind mounts under `/srv/paisans/<token>/<stack>/`, not named volumes.** This is why the
  tar is one line instead of a `--volumes-from` dance, so the toolkit lays
  pinned stacks out that way from the start.
* **Lower the DNS TTL before the move**, not during it.
* **The hostname must not change** — same name, new address. For an ActivityPub
  app the domain is its identity (see rule 4); moving hosts is routine, moving
  domains is a rebuild.

For a stack with large media, two `rsync` passes — one while running, one after
stopping — beat a single tar.

`app move` should exist as a command, and should say plainly that it involves
downtime. It is a migration, not a configuration flip.

#### Do not pin an app onto the witness without thinking

If the cloud VM is both the etcd witness and the host for a pinned app, they
compete. etcd's stability depends on fsync latency, and a busy application with
its own database is not a quiet neighbour. Disk contention there means missed
heartbeats, spurious elections, and **the database failing over for no reason**
because something else was compacting.

In order of preference: a separate VM for pinned apps; the same VM with etcd's
data directory on its own device; or a box big enough, with monitoring.

The toolkit **warns** here (`pinned-app-on-witness`) rather than refusing.
Unlike a co-located witness, which is incoherent, this is merely risky, and the
risk is acceptable if it is chosen rather than stumbled into.

#### There is one HA cluster, and that is deliberate

Multiple named Postgres clusters are **out of scope**.

The motivating idea — "high availability for the forum but not the wiki" — turns
out not to exist. One Postgres cluster holds many databases, and streaming
replication is physical: the whole cluster replicates together. So every
database in it gets HA **for free**. There is no per-database switch, and
nothing is saved by excluding an app. Leaving it in costs nothing.

An app that must not share the cluster gets `pinned` placement.

**A useful consequence: exactly one Patroni per data-bearing host.** Pinned apps
run plain Postgres with no Patroni, and there is only ever one cluster. So the
`/dev/watchdog` device has exactly one claimant, contention cannot arise, and
fencing is available to the one Patroni that needs it. Ruling out multiple
clusters removed an entire class of problem rather than merely deferring it.

#### Consequence for routing

The gateway's Caddy configuration becomes inventory-driven: a pinned app's
hostname points at its location, everything else follows the current primary.
A template job, not an orchestrator job.

It is assembled rather than written: each blueprint ships its own Caddy snippet in
its template set, and the gateway file holds only what is cross-cutting. See
*The template set owns everything app-specific, routing and shims included*.

#### Failing over, and saying so when nothing can serve

Two things are cross-cutting enough to live in the gateway file as named
snippets rather than in every kind.

**`upstream_failover`**, imported inside the proxy of every app on more than
one apps site: `lb_policy first`, `lb_try_duration 5s`, `fail_duration 30s`,
`dial_timeout 2s`, `response_header_timeout 60s`. `first`, not round robin, because the apps sites are not
interchangeable during a failover. A dial that fails is retried on the next
site within the same request, for a POST too: Caddy 2.11 retries any method
only when the connection never opened, so no body was sent. A dead site on
the mesh answers nothing, not even a reset, so the dial timeout is the whole
price of finding it dead; once found, it is skipped for `fail_duration`, and
then one request tries it again. Checked against a live Caddy: with one site
refusing and the other up, a POST is answered by the live one with no error.
A pinned app has nowhere to fail over to and does not import it.

**The wait for headers is bounded at 60s.** A request can go out on a
connection the gateway opened to a site before that site died. Nothing answers
it and nothing resets it, so without a bound it waits for TCP to give up, which
takes minutes, and the reader sees a page that never loads. 60s is twice the
30s long poll a Matrix client holds open on `/sync`, the longest wait for
headers any kind here makes on purpose.

**`upstream_single`**, imported inside the proxy of an app, or a Garage media
route, served by exactly one site on a machine other than the gateway's:
`dial_timeout 2s` and `response_header_timeout 60s`, the same two bounds
without the failover. That site can go down while the gateway stays up, and
the bounds turn it into `upstream_unavailable`'s 503 within seconds instead of
a request that hangs. A route to an app on the gateway itself imports neither,
since that app goes down with the gateway.

**`upstream_unavailable`**, imported in every host block, media included:
when Caddy itself fails to reach any upstream, it answers 503 with
`Retry-After: 120` instead of its own 502, 503 or 504. Without it, the status
depends on how the sites died: a refused or timed out dial, or a site dying
mid request, is 502; every upstream already marked down is 503. A fediverse
server delivering to an inbox may treat a 502 as a permanent failure and drop
the activity, which is the one loss nobody can recover later; 503 with
`Retry-After` is the status that asks it to come back. It catches only errors
Caddy raised: an application's own 502, 503 or 504 response (Garage's 503 for
lost quorum among them) is passed through unchanged, so it never hides an
application's real answer. It does also turn an auth gate's failure into 503,
which is the right answer there too.

## Accounts

### Operators and community admins

A deployment has two groups of people with power over it, and the toolkit keeps
them apart because they hold different things.

| | Operators | Community admins |
|---|---|---|
| Run | the machines | the community |
| Declared in | `operators:` in `paisans-stack.yaml` | membership of the Pocket ID group `admins` |
| Hold | SSH keys on the sites, the age keys that decrypt the secrets, the right to sign a configuration version | administrator rights in every app that reads `admins` |
| Do | `site prepare`, `deployment apply`, `secrets`, `deployment publish` | moderate, approve applications, administer the apps |
| At least | one, and two to keep a lost workstation from stopping the deployment (`operators-none`, `operators-fewer-than-two`) | none required |

**Every operator is also a community admin**, so that a new deployment has
someone to run it: `deployment apply` puts each operator in every app's admin group (see
*`deployment apply` makes the declared operators and hands each a login link*).
**A community admin is not an operator**, because running the community needs
no shell on a host and no secret, and handing those out with moderation rights
would put every admin's workstation inside the deployment's blast radius.
Anyone besides the operators joins `admins` by promotion, which the toolkit
does not perform: `paisans-stack.yaml` declares only the operators. The
operators also form a group of their own, `operators`, which
the infrastructure apps read for operator access: the uptime monitor admits
`operators` by default (see *Monitoring*). The operator reconciler checks
that Pocket ID agrees with the `operators:` section (see *The operator
reconciler checks Pocket ID against the operators*).
`docs/specs/2026-10-14-operators-and-management.md` is the approved design.

**`paisans operator add` declares one more operator.** It writes their entry
under `operators` and their name in the `ssh.keys` of the sites they may log
in to, and leaves every other line of `paisans-stack.yaml` as it was. At a
terminal it asks `create`'s questions for whatever its flags did not give;
without one it takes `--name`, `--email`, `--key` and `--site` (one site,
or several separated by commas) or `--all-sites` and asks nothing. It reaches
no host: the next `deployment apply --execute` gives them their sites and their
Pocket ID account. Their age public key goes into `.sops.yaml` by hand,
followed by `sops updatekeys secrets.enc.yaml`.

**A new operator joins by a signed invite, in five steps,** once the two hold
each other's SSH public key:

1. The existing operator runs `paisans operator invite carol --key carol.pub >
   carol.invite`. The invite names the deployment, its domain, the root of the
   history they trust and each site, and holds nothing secret.
2. The new operator runs `paisans operator register --invite carol.invite
   --inviter-key wash.pub > carol.request`, in any directory. It checks the
   invite against the inviter's key, takes their age key for the deployment's
   id from a `SOPS_AGE_*` variable, an existing keystore item or
   `--age-private-key-file`, or else makes a new keystore item, and writes a
   signed request. The invite waits as pending, and the age key stays bound to
   the deployment, in the workstation's data directory beside its saved
   deployments: `invites/<deployment-id>/` and `age/<deployment-id>.recipient`.
   Answering again names the bound key, wherever it is found.
3. The existing operator, or any other, runs `paisans operator add --request
   carol.request`. It checks the request and its invite, declares the operator,
   names their age key in `.sops.yaml` and re-encrypts `secrets.enc.yaml` to
   it, with the plaintext unchanged. Accepting the same request again changes
   only what is missing.
4. The inviter runs `paisans deployment apply --execute`, which gives the new
   operator their sites and their Pocket ID account.
5. The new operator runs `paisans operator register --execute` in an empty
   directory, which takes the pending invite, or the one `--id <deployment-id>`
   names when several are pending. It recovers the deployment from the invite's
   root and sites, trusting no other root, signs their attestation, and says
   whether the secrets open with their age key, and if not, whose step is
   missing.

Neither party edits `.sops.yaml` or runs `sops`, and both files travel over
any channel since each is signed. `paisans operator age-key` shows an
operator's age public key for a deployment, making it first when there is none.
See *`paisans operator add`*, *`paisans operator invite`* and *`paisans operator
register`* in [docs/commands.md](docs/commands.md).

### Who manages accounts: `management`

Signup, bans and a member's way back in after a lost link are managed in one
place, and the `pocket-id` app declares which:

```yaml
apps:
  auth:
    blueprint: pocket-id
    hostname: id.example.org
    settings:
      management: paisans       # or pocket-id; required
```

**`management` is required, never defaulted** (`pocket-id-management-unset`,
which also refuses any value but `paisans` or `pocket-id`), because it decides
whether anybody is a Pocket ID administrator at all, and that is not a choice
to make by omission.

| | `paisans` | `pocket-id` |
|---|---|---|
| An `account` app | required (`pocket-id-without-account`) | refused (`account-under-pocket-id-management`) |
| `allow_user_signups` | refused if set (`pocket-id-setting-managed`); rendered `disabled` | the operator's choice |
| `email_one_time_access_as_unauthenticated` | refused if set (`pocket-id-setting-managed`); rendered `true` | the operator's choice |
| `email_one_time_access_as_admin` | refused if `false` (`account-login-link-disabled`) | the operator's choice |
| SMTP host and sender | required (`pocket-id-paisans-without-smtp`) | optional |
| `signup_default_groups` | must name each account app's member group (`pocket-id-signup-groups-lack-member-group`) | the operator's choice |
| Operators' `isAdmin` at Pocket ID | never set by `deployment apply` | set by `deployment apply` |
| Signup, bans, recovery | the account service | Pocket ID's admin pages |

Each row has its reason:

* **Under `paisans` nobody is a Pocket ID administrator.** The account service
  manages accounts, so a Pocket ID administrator would be a second place that
  can make, ban or rename a member, outside the community's process. The
  operator reconciler reports any enabled administrator it finds.
* **The settings the toolkit renders are refused when declared**, because a
  declared value could only disagree with the rendered one. Signup is closed,
  since new accounts come only from the account service; the email code is
  open, since that is how a member whose sign-in link expired signs in again.
* **Mail is required under `paisans`** because recovery is an email code, and
  the account service will not start without a mail server either.
* **`signup_default_groups` must hold the account app's member group under
  `paisans`**, `members` unless the account app sets `member_group`. The
  account service creates every account with no group and Pocket ID puts it in
  these, so without the member group an accepted invite or application makes
  an account that is a member of nothing.
* **An account app is refused under `pocket-id`**, because two places that
  each think they decide who joins would disagree.
* **Under `pocket-id` the operators are Pocket ID's administrators**, because
  Pocket ID's admin pages are where accounts are managed there, and somebody
  has to be able to open them.

### The account service

The `account` blueprint runs [`Paisans-Software/paisans-account-service`](https://github.com/Paisans-Software/paisans-account-service),
the community's account management under `management: paisans`. It is where
people join, where community admins ban them, and where an account is
recovered, so that none of it needs a Pocket ID administrator.

* **Signup.** With `signup: invite`, a signed in member makes an invite link;
  with `signup: open`, anyone opens the registration page. Either way the
  person reads the community's code of conduct, agrees to it, and chooses a
  username, a display name and an email address, and an address under a
  current ban is refused. With `require_approval` on, a community admin then
  approves or declines the application; only after approval does an account
  exist at Pocket ID, and Pocket ID emails the new member a one-time sign-in
  link.
* **Bans.** A community admin finds a member at `/admin/members` and bans them
  with a reason: the service disables the user at Pocket ID and keeps a keyed
  hash of their address, so they cannot sign up again. `/admin/bans` lists
  every ban and lifts one. An operator cannot be banned there, because an
  operator is removed by editing `paisans-stack.yaml`: the toolkit renders
  their usernames as `ACCOUNT_OPERATORS`, and the service refuses them. A ban
  does not end a session an app keeps for itself.
* **A member's own account and recovery.** A member changes their own address
  (confirmed by a code sent to the new one) and name at `/account/email` and
  `/account/name`, since nobody edits their own Pocket ID account. A member
  who has lost both passkey and address is recovered at `/admin/recovery`,
  opened by one community admin and approved by a different one. Operators
  are refused both, and recover with `paisans operator magic-link`.
* **A member's groups.** A group is what grants privilege: `admins` makes a
  community admin of every app that reads it, and an app's member group
  admits a member to that app. So promoting and demoting are one act, at
  `/admin/members/<id>/groups`: any community admin adds any user to any
  group or takes them out of one, themselves and other admins included,
  because there is one tier of community admin. The exception is
  `operators`, shown but never changed there, since `paisans-stack.yaml`
  alone says who is an operator; an operator's other groups can be changed,
  and the next `deployment apply` puts them back in the groups it gives every operator.
  A change takes effect at the user's next sign in: a session an app already
  holds keeps the groups it was given until it expires.

The design is in `docs/specs/2026-10-12-invite-service.md`,
`docs/specs/2026-10-14-account-service.md`,
`docs/specs/2026-10-14-account-signup-bans-recovery.md` and
`docs/specs/2026-10-14-account-promotion.md`.

The app exists because it is declared, and `management: paisans` requires it:
the toolkit never adds it or drops it on its own, so the declaration is the
one place to read whether a deployment has it.

```yaml
apps:
  account:
    blueprint: account
    hostname: account.example.org
    placement: cluster
    settings:
      signup: invite
      invite_quota: 3
      require_approval: true
      code_of_conduct: |
        ## Our code of conduct

        Be kind. Keep what is shared here inside the community.
    hostnames:
      join: join.example.org   # optional; join.<domain> when unset
```

| Setting | Takes | Default |
|---------|-------|---------|
| `signup` | `invite` or `open` | required |
| `invite_quota` | `signup: invite` only. 1 to 100: live invites a member may hold at once; community admins have no quota | `3` |
| `require_approval` | `true` or `false` | `true` |
| `invite_lifetime_days` | `signup: invite` only. 1 to 365: how long an unused invite lasts | `14` |
| `application_wait_days` | 1 to 365: how long an application waits for a community admin before it is dropped | `14` |
| `decline_retention_days` | 1 to 3650: how long a declined address is remembered | `183` |
| `login_link_days` | 1 to 31: how long the sign-in link Pocket ID emails lasts, and the number of days the applicant's page says | `7` |
| `code_of_conduct` | the Markdown text itself, a YAML block scalar | required |
| `member_group`, `admin_group` | the Pocket ID groups its OIDC client admits | `members`, `admins` |

**`signup` is declared, never defaulted.** It is access policy, like `gated`:
it decides whether a stranger can apply at all. `invite_quota` and
`invite_lifetime_days` belong to invite links and are refused with
`signup: open`, since the service is never told them there; every other
setting applies in both modes.

### Two hostnames

| Hostname | Serves |
|---|---|
| the app's `hostname`, `account.<domain>` in the example | everything a signed in member or community admin uses: sign in, the home page at `/`, a member's own address and name, making and revoking invites, `/admin`, the application queue, bans and recovery |
| `join.<domain>`, or `hostnames.join` | only the pages for someone with no account yet, and their static files: `/i/<token>` with `signup: invite`, the registration page at `/` with `signup: open` |

Every account app has the join hostname, because every one takes signups in
one mode or the other. Its name is `join.` under `community.domain` unless
`hostnames.join` names another. Every consumer of an app's hostnames (the
gateway, `dns init`, `validate`'s collision checks) reads them through one
resolver, `blueprints.RoleHostnames`, so the gateway, DNS and the collision
checks always agree on it.

**Each hostname serves only its own routes**, enforced twice. The service
registers each route with the host of its public URL, so a request for a
route on the other hostname gets a 404, and that holds for a request that
reaches it directly on the mesh. The gateway's join snippet passes `/i/*` and
`/static/*` with `signup: invite`, and exactly `/` (`GET` and `POST`) and
`/static/*` with `signup: open`, and answers 404 to everything else. The
primary snippet answers 404 to `/i/*` in both modes and proxies its own `/`,
which is the member's home page. The session cookie carries no `Domain`
attribute, so the origin someone signing up opens never holds a session.

**It is cluster only** (`account-must-be-clustered`). Its state is in the
cluster's Postgres, under a role and database of its own, and it runs on every
apps site, so an invite, an application or a ban survives the loss of a site.
It listens on container port `8082`, because `8080` belongs to a clustered
Mbin on the same site, and answers `GET /healthz` with `200` when its database does. Neither
hostname is put behind the gate: someone signing up has no account yet, and
the service checks every sign in itself.

**The identity app** is the deployment's one `pocket-id` app. The account app
names none, and `identity_app` among its settings is refused
(`account-setting-unknown`), because a setting that names one would read as a
choice the deployment does not offer: with one Pocket ID there is nothing to
choose. The account app's issuer, API key and accent colour all come from that
app, and its two Pocket ID addresses differ:

| Variable | Value | Why |
|----------|-------|-----|
| `ACCOUNT_OIDC_ISSUER` | `https://<pocket-id hostname>` | An OIDC issuer is the URL Pocket ID names itself by, which is its `APP_URL`; a token whose issuer does not match is refused. It carries no key. |
| `ACCOUNT_IDP_URL` | `http://<site mesh address>:1413` | The API, with the static API key, through the site's own HAProxy to the active Pocket ID, so the key travels only over the mesh, which WireGuard encrypts between sites. |
| `ACCOUNT_IDP_PRIVATE_NETWORKS` | `mesh.subnet` | The networks the service accepts `ACCOUNT_IDP_URL` in besides the private ranges, set to the mesh. |

Renaming the identity hostname re-renders the account service's `.env`, and
the next `deployment apply` recreates the account stack. The account app is cluster
only, so every site it runs on runs HAProxy and has the listener (see
*Pocket ID runs on every apps site, and one of them is active*).

### What it writes at Pocket ID

Four calls, and nothing else:

* **Create the user**: `POST /api/users` with the username, email address and
  display name. No group and no admin flag, so the new account takes the
  Pocket ID app's `signup_default_groups` and is a member from its first
  sign in.
* **Send the sign-in email**: `POST /api/users/<id>/one-time-access-email`
  with a `ttl` of `login_link_days × 24` hours. Pocket ID sends the mail
  itself.
* **Update the user**: `PUT /api/users/<id>`, with every other field sent back
  as read, to disable or re-enable a banned member, to set a verified address
  after an address change or a recovery, and to set a member's name. A ban
  never touches groups, so a lifted ban returns everything the member had.
* **Replace the user's groups**: `PUT /api/users/<id>/user-groups`, which sets
  the user's whole list of groups at once. The service reads the user's
  groups immediately before, sends that list with the admin's additions and
  removals applied, and refuses a list that would change who is in
  `operators`, so two admins at once never undo each other's unrelated
  changes.

It authenticates with the Pocket ID app's `static_api_key`, the same key the
toolkit uses, and a Pocket ID API key carries no scopes: the key is all of
Pocket ID. **A leak is answered by rotating it across the deployment.** Run
`paisans secrets rotate apps.<auth>.static_api_key`, which re-applies Pocket ID
and the account stack, so the account service is handed the next key with it.
Then check Pocket ID for administrators, clients and API keys nobody made.

### What `validate` refuses

| Rule | Refused or warned |
|---|---|
| `account-signup-unset` | `settings.signup` missing. Refused |
| `account-invite-settings-under-open-signup` | `invite_quota` or `invite_lifetime_days` with `signup: open`. Refused: the service is never told it, so it would read as configured and do nothing |

The identity app is held to the one setting every signup depends on that the
toolkit does not render itself:

| The identity app | Refused | Rule |
|---|---|---|
| `email_one_time_access_as_admin` | `false`. Pocket ID refuses to send an approved member's sign-in link while it is off | `account-login-link-disabled` |

Its signup and its email code are not checked here, because the account app
exists only under `management: paisans`, where the toolkit renders both itself
and refuses either one declared (*Who manages accounts: `management`*).

Also refused: a `code_of_conduct` that is missing, empty or not text
(`account-code-of-conduct-missing`), and a setting out of range
(`account-setting-invalid`, which also refuses a `signup` that is neither
`invite` nor `open`). Refused as well, because every account app signs
members in through Pocket ID and the service will not start without a mail server: a
deployment with no `pocket-id` app (`account-without-pocket-id`), one with
more than one (`account-one-identity-app`, since the account app's OIDC
client, issuer and API key all come from the deployment's Pocket ID, and with
two they could come from different ones), a pocket-id app whose `management`
is `pocket-id` (`account-under-pocket-id-management`), an `identity_app`
setting (`account-setting-unknown`, refused for that key), and no SMTP host
and sender for the app (`account-without-smtp`). Any other setting the
blueprint does not read is a warning under the same rule, since it renders
nothing. The service sends its own notices (an application waiting, an
applicant's outcome, an address change) through the deployment's `smtp:`
block, which the app's own `smtp:` fields may override; the sign-in link is
Pocket ID's mail.

### The code of conduct

`code_of_conduct` is the text, not a path, so it is versioned and synced with
the rest of `paisans-stack.yaml` through every site's configuration store. `render`
writes it to `code-of-conduct.md` in the app's data directory and the container
mounts it read-only, in either signup mode. Its SHA-256 is the
version recorded with each person's agreement, so editing the text is a new
version without anyone numbering it.

### Keys

`init` generates two, 32 random bytes each, and never regenerates them:

| Secret | What it does | Rotating it |
|---|---|---|
| `apps.<account>.email_hash_key` | keys the HMAC of a declined address, so a copy of the database alone cannot test a guessed address | `secrets rotate` refuses it and `--all` keeps it; only `paisans secrets set` changes it, and that forgets every declined address |
| `apps.<account>.session_key` | signs the service's sign-in cookie | signs everyone out of the account service and nothing else |

## Monitoring

The `uptime` kind runs the [`Paisans-Software/uptime`](https://github.com/Paisans-Software/uptime)
fork. It is **pinned only**, because it keeps SQLite on local disk, so
`cluster` is refused by the same rule that refuses Element and oauth2-proxy,
and it is pinned to a site holding the `monitor` role (below). The design and
the reasoning behind each choice are in
`docs/specs/2026-10-07-uptime-monitoring.md`, as amended by
`docs/specs/2026-10-08-monitor-role-and-host-check.md`.

### The monitor has a site of its own

A monitor exists to report everything else failing, so it is best on a
machine that fails with nothing it reports. `monitor` is a site role, and the
uptime app is pinned to a site that holds it. Founder decision.

| Combination | Result | Rule |
|---|---|---|
| `monitor` with `gateway`, `witness`, `data` or `apps` | warned, once per site | `monitor-shares-a-site` |
| `web_server` on a site holding `gateway` | refused | `web-server-on-gateway` |
| `monitor` with no uptime app pinned to it | refused | `monitor-without-uptime` |
| an uptime app pinned to a site without `monitor` | refused | `uptime-needs-a-monitor-site` |
| `monitor` without `public_address` | refused | `monitor-without-public-address` |
| no app with blueprint `uptime` at all | warned | `no-uptime-monitor` |

A machine of its own is the recommendation: a small VM or box declared
`roles: [monitor]`, which needs no other role. A deployment with no machine to
spare may put the monitor beside the gateway, the witness, `data` or `apps`,
and validate warns once, naming what it shares (Founder decision,
2026-10-12). Such a monitor still checks every app and container on its
machine and every other site, but it cannot report its own machine failing:
when that goes down the monitor goes with it, and no alert is sent. Beside the
witness, nothing reports etcd losing its tiebreaker either. Beside the
gateway, the gateway's Caddy serves the monitor's hostname, so that site
carries no `web_server` block (`web-server-on-gateway`). `roles: []` stays
legal for a site hosting a pinned app of any other kind.

`paisans gateway move` hands the gateway off a site that keeps the monitor
rather than stopping its Caddy: until the hand-off, that site keeps every
gateway route, and then its Caddy reloads with the monitor's alone (see
*Moving the gateway*, "A site that keeps the monitor"). Moving the monitor
with the gateway works too, but the move changes only the gateway's
hostnames: it refuses while the health page's record still points at the old
machine, so change that record first, as in *Moving an existing monitor*
below.

**It serves its own hostname.** An app pinned to a monitor site is left out
of the gateway's Caddyfile, and `dns init` points its hostname at the
monitor's `public_address` (and `public_address6`), which is why that address
is required. A monitor reached only through the gateway would go dark at
exactly the moment it is needed. A monitor beside the gateway is the
exception: the gateway's own Caddy serves it, from the same address. On any
other monitor site, what is in front of it is the site's `web_server` block,
which only a monitor site may carry (`web-server-outside-monitor`):

```yaml
sites:
  health:
    roles: [monitor]
    address: 10.44.0.4
    public_address: 203.0.113.20  # the mesh dials it here, on 51820
    web_server:
      managed_by: external        # paisans (default) or external
      listen: 127.0.0.1:8480      # external only, and required there
```

* **`managed_by: paisans`**, the default, runs the toolkit's Caddy on the monitor,
  the same image the gateway runs, in its `infra` stack with host networking
  and certificates over DNS-01 through `acme.provider`, with a host block for
  each app pinned there and nothing else. It claims tcp 80 and 443, and
  `site admit --public-ports` opens both. `acme.provider` and `external.acme_dns_token` are
  required wherever this Caddy runs, as they are on a gateway, which puts the
  DNS token on the monitor too: a second machine holding a credential that
  can edit the whole zone. To narrow it, CNAME every `_acme-challenge` record
  into a challenge only zone and scope the token to that zone, as the header
  of the rendered `caddy.env` describes (and *Certificates use DNS-01,
  everywhere* in `docs/development.md`); then the token on either machine can
  write challenges and nothing else.
* **`managed_by: external`** runs the app and nothing in front of it, for a machine
  that already runs a web server on 80 and 443. The app is published on
  `listen` alone: nothing dials its mesh address, since the gateway does not
  route it and the monitor does not check its own container. Its compose
  network is pinned at `10.255.255.0/29`, outside Docker's default pools, so
  the address the web server's connections arrive from is known; `validate`
  refuses a mesh over it (`web-server-network-overlaps-mesh`), and the host
  check refuses a host where a foreign network or route already holds it.
  The certificate and its renewal belong to whoever runs that web server: the
  toolkit cannot know how an unfamiliar server obtains one, and must not edit
  a configuration it does not own. A machine that was the gateway and
  switches to `managed_by: external` in the same edit that moves the gateway
  off it no longer runs the toolkit's Caddy, so `paisans gateway move` treats
  it as an ordinary old gateway and stops that Caddy: the health page is down
  until the operator's web server serves it. Move the gateway first, then
  switch the web server in an apply of its own. `paisans deployment apply` without `--site` writes each such app's reference snippet and
  checks it with `paisans web-server check`'s checks once every step has run, and
  `paisans deployment apply --site` on such a monitor does the same for the apps the monitor
  serves once its stacks are up. Each app has a line in a `web server` section,
  and each is checked once, without waiting. A line that fails is a warning, `!`,
  with the checks that failed under it, an error several of them share said
  once, then the web server admit found in front of the app and the commands
  for the owner to finish with. The run ends on a warning,
  `! Deployment complete, but 1 app failed its web server check`, with exit
  status 0: every step ran, and the web server is the owner's. What apply
  itself could not do, such as reaching the monitor's host over ssh or writing
  a reference file, is a failure, `✗`, and the run ends failed with exit
  status 1. A dry run lists the check without running it. `deployment apply --site` ends
  the same way in place of `Applied <n> files to <site>.`.
  `docs/guides/behind-your-own-web-server.md` walks through it.

**The toolkit renders, the owner installs.** On a site with `managed_by:
external`, `deployment apply` runs the app on `listen` and writes the snippet for the web
server in front of it to `/srv/paisans/<token>/web-server/<app>.caddy` or
`<app>.conf`, filled in from what `site admit` detected as root: the server,
its configuration and how it obtains certificates. When admit found no server
it knows, it writes one for each of Caddy, nginx and Apache (`<app>.caddy`,
`<app>.nginx.conf` and `<app>.apache.conf`). Apply puts the file back when it
is missing or has been edited. The deployment's user cannot write the owner's
configuration, test it or reload the server, and nothing grants it that, so
the owner copies the snippet in; `paisans web-server show --app <app>` prints
it with the rest of the hand-off sheet, read from the same admission record.

**apply checks from outside and warns until the owner's copy serves.** After
writing the snippet, `deployment apply` runs `web-server check` for the app once, without
waiting. Until the check passes, the web server step ends on a warning naming
`paisans web-server show --app <app>` for the owner. When a change to
`paisans-stack.yaml` rewrites the snippet, the warning says so, because the
owner's copy is then older than it.

**Removing an app names the block to remove.** `app remove` and `site remove`
delete the reference file, which is the deployment's, and say which block the
owner removes from their web server. `validate` refuses
`web_server.include_dir` (`web-server-include-dir`): nothing is written into
the owner's configuration, so there is no directory to name.

`listen` belongs to `managed_by: external` alone, which cannot work without it
(`web-server-listen-managed-by`). The mesh address is admitted whole by the firewall's
`allow in on psns-<token>`, so a `listen` anything else can reach would expose
the app around the web server. The web server runs on the
same host, so only an IPv4 loopback address or the site's own mesh address is
accepted, with a port written without leading zeros, the rule `site request`
holds the site's request to (`web-server-listen-not-this-host`); loopback is the one
to use, and the mesh address warns that every site of the deployment reaches it
(`web-server-listen-bypasses-firewall`). One `listen` publishes one app, so an
external monitor hosts the monitor alone (`web-server-external-serves-one-app`).

Either way the app is rendered for a proxy in front of it:
`PUBLIC_BASE_URL=https://<hostname>`, from which the fork builds its sign in
callback, and `TRUST_PROXY` set to exactly where that proxy connects from,
because the login rate limiter is keyed on the client address and whatever is
trusted may claim any client address. Behind the monitor's own Caddy that is
the site's own mesh address, `/32`, since that Caddy runs on the host and dials
the app there. For a loopback `listen` it is the pinned network's gateway,
`10.255.255.1/32`, because Docker hands a connection on a loopback publish to
the container from that gateway, never from 127.0.0.1. A mesh `listen` is
reached by the web server's own address, which Docker's forwarding keeps and
which the toolkit knows only by its network, so it is the mesh subnet.

Switching an existing site between the two `managed_by` values moves that pin, and the
network on the host has to move with it, or `TRUST_PROXY` names a gateway the
web server's connections no longer come from. `deployment apply` therefore asks the host
how each stack it recreates has its compose network addressed, and where that
differs from the compose file (a pin the file declares and the network lacks,
or `10.255.255.0/29` left on a network the file no longer pins), it plans a
`docker compose down` before the `up`, shown in the plan with the reason.
`down` removes the network, and `up` creates it as declared. Only IPv4
subnets are compared, and a gateway only where the file declares one, so
what Docker fills in for itself never takes a stack down. The old
containers' anonymous volumes are kept, since the new containers start on
fresh ones and the old may hold data. The infrastructure stack is never
taken down this way, because that stops the database and the gateway
together; a mismatch there is a note in the plan telling the operator to
take it down at a time the site can be out and apply with `--recreate
infra`. A probe Docker cannot answer stops the plan. Compose 2.31 and
later recreates a network whose recorded configuration changed, but leaves
one with no record alone (created by an older Compose or by hand), so `deployment apply`
reads the network itself rather than relying on that. A dry run only reads.

**Helping an operator behind their own web server.** Two commands, `web-server
show` and `web-server check`, neither of which changes anything.
How to run them, and their flags, are under *`paisans web-server show` and
`paisans web-server check`* in [docs/commands.md](docs/commands.md).

`show` prints the hostname, the upstream (`listen`) and the health path; what
the web server must do (terminate TLS for the hostname, pass `Host`, set
`X-Forwarded-For` and `X-Forwarded-Proto`, refuse what the toolkit's own edge
refuses, below); the snippet for the web server in front of the monitor; and
the ufw warning when `listen` is not loopback. It reads the web server that
`site admit` detected as root from the site's admission record, over ssh as the
site's ssh user, and detects nothing itself: the server and its container or
process, its configuration, and how it obtains certificates. A Caddy that
obtains its own certificates gets a site level `tls { issuer acme }`, one that
loads certificate files gets a `tls` line following their pattern for the new
name, and where a server is bound to a private address only the snippet says
which listener the site belongs on. For nginx and Apache with certificates
under `/etc/letsencrypt/live/` it names the files certbot will write for the
new name and the `certbot certonly` command, with the authenticator the
existing renewals use. A line above the snippet
says what was found, such as `web server: caddy (docker container caddy-1,
config /opt/caddy/Caddyfile), obtains its own certificates`, so it can be
checked before anything is copied.

The site's own issuer is there so that its certificate never depends on a DNS
challenge the operator set for their other sites, whose token may not cover
the monitor's zone. With no DNS provider, Caddy obtains it over HTTP-01 or
TLS-ALPN-01, which needs only what the monitor already needs: the hostname's
A (and AAAA) record pointing at the host, and port 80, or 443 for TLS-ALPN-01,
reachable from the internet. The operator's global `email` still applies. The
issuer replaces a global `cert_issuer acme { dns … }`, but not a global
`acme_dns`: Caddy gives that to every ACME issuer without a DNS provider of
its own, the site's included (`fillInGlobalACMEDefaults` in
`caddyconfig/httpcaddyfile/tlsapp.go`, Caddy v2.11.7), and then uses the DNS
challenge alone. Admit does not record the Caddyfile's global options, so
neither `show` nor `deployment apply` warns about it: an owner whose global options set
`acme_dns` needs that token to cover the hostname's zone, or rewrites it as a
`cert_issuer acme { dns … }`, which lets the site use HTTP-01 and drops
Caddy's default ZeroSSL fallback issuer for the sites without one of their
own. When admit found no server it knows, that line says why and the sheet
carries the Caddy, nginx and Apache snippets, with Caddy's `tls` line commented
out. `check` reports pass or fail, with the fix, for: the hostname
resolving to the monitor's `public_address` (and `public_address6`) and to
nothing else, so a stale record still on the gateway fails;
`https://<hostname>/healthz` answering with a certificate valid for the
hostname, and its days to expiry; the sign in redirect naming a callback under
`https://<hostname>/`; and the published port refusing the connection, or not
answering, on the public addresses. Any other dial failure, such as no route
from where the check runs, says nothing about the port: one conclusive answer
still passes, naming the family that could not be reached (commonly IPv6 from
a home connection), and with none the check fails as inconclusive. With `managed_by:
external` a check that fails also reads the web server, as `show` does, and
names it, so the fix lands on the right configuration; a passing check reaches
no host. In `managed_by: paisans` there is nothing to hand off, and `check`
runs only the first two.

**Moving an existing monitor.** Once the uptime app moves to a monitor site,
`dns init` updates the A record its own earlier run made at the uptime
hostname from the gateway's address to the monitor's. A record there that it
did not make is a `conflict`, and `dns prune` keeps it too, since the name is
still wanted. Change that one by hand at the provider, then run `dns init`
again.

**What it checks is generated, never typed.** `deployment apply` renders `monitors.json`
from this configuration and the fork reconciles it at every start:

* every app gets a **public** check at its hostname, through DNS, the gateway
  and its certificate (whose expiry the fork warns about at 14 days), and a
  **direct** check on each site it runs on, straight to the container with the
  app's own `Host`. The public check alone cannot see an app behind a gate,
  which redirects to sign in without asking the app, nor a clustered app dead
  on one site while Caddy routes round it.
* both ask the kind's own health route (`kinds.HealthFor`), established from
  each pinned image's source and, where possible, by running it, and chosen so
  the gateway routes it to the same service the direct check reaches.
* a gated app gets a **gate** check instead, a request for `/` with no
  session that expects the body only the gate's redirect to sign in carries,
  proving the edge and the gate: a private app redirects `/` to its own sign-in
  page, so the status alone would pass with the gate gone. Its public check stays only when its health route is a dedicated one,
  which is open past the gate; a health route of `/` is the front page and
  never is; Mbin's and WriteFreely's `/healthz` are dedicated. A gated app
  that federates also gets a **signed fetch** check: an unsigned ActivityPub
  request for a path the app refuses whatever content exists, expecting the
  refusal: `/` for Mbin and a blog's outbox for WriteFreely, both 401. Both
  apps lock the setting (Mbin's `MBIN_LOCKED_SETTINGS`, WriteFreely's
  `locked_settings`), and the check stays: it is what proves the lock holds.
* a gated Mbin also gets a **private API** check on each site, straight to the
  container like the direct check: an anonymous `GET /api/entries`, expecting
  401. A gated Mbin runs as a private instance, which refuses its API to a
  request with no token; through the edge the gate would answer first, so the
  app is asked directly. It is what notices an image that ignores
  `MBIN_PRIVATE_INSTANCE`.
* every site but the monitor's own gets a **ping** over the mesh, and a
  **heartbeat** the other way: the site's host pushes to the monitor's public
  hostname once a minute, over the internet and never the mesh, from a
  `heartbeat` service in its infrastructure stack (below). The two paths fail
  separately, which is what tells the failures apart:

  | Ping over the mesh | Heartbeat over the internet | What it is |
  |---|---|---|
  | down | fresh | the mesh: the tunnel between the two is torn, the site is up |
  | up | stale | the site's own way out: its WAN is down, the mesh still carries it |
  | every site down | every site fresh | the monitor's own tunnel, not the sites |
  | down | stale | the host, or the site's power and line together |

  A ping alone reports every one of those the same way. Founder decision;
  the design is the amendment of 2026-10-09 in
  `docs/specs/2026-10-07-uptime-monitoring.md`.
* every site Pocket ID runs on gets a check on its **operator reconciler**
  service, `<app> — operator reconciler (<site>)`, which asks `/livez` and
  expects 200: that site's reconciler is running its passes, standby or not.
  It says nothing about the operators, which only the active instance
  checks.
* every Pocket ID app gets one **operators heartbeat**, `operator continuity`:
  a push monitor its active instance feeds from the reconciler's `/healthz`,
  so it fails while an operator is not an enabled member in the member group
  and in `operators`, a Pocket ID administrator is not an operator, or only
  one operator is declared (see
  *The operator reconciler checks Pocket ID against the operators*), and
  follows whichever site is active across a failover with one history
  (below). It is named for the community, not the app, because every
  instance pushes to the one token; a deployment with more than one Pocket ID
  app prefixes each continuity monitor with the app key, since the seed tells
  monitors apart by name.
* every app whose blueprint declares a **heartbeat** gets one push monitor,
  `<app> — <name>`, fed by a service of the blueprint's own under the app's
  `heartbeat_token`, with the site heartbeat's schedule (see *A blueprint can
  come from a catalog*).
* a deployment on Cloudflare that has pasted `external.dns_members_token`
  gets two more beside it, `operator continuity: dns super admins` and
  `operator continuity: dns 2fa`, fed the same way from the reconciler's DNS
  check (see *The reconciler checks the DNS provider's account*).
* the monitor does not watch itself: its seed has no public or direct check
  of its own app, and no ping or heartbeat of its own site. A monitor that is
  down cannot report it, and a check from the monitor to its own host can only
  pass. Every other app on the monitor's site is checked like any other. The
  path in front of the monitor, DNS, the web server and the certificate, is
  still exercised from outside it: every other site pushes its heartbeat to
  the monitor's public hostname, so a failure there leaves every heartbeat
  stale at once.
* every other `uptime` app pinned to a different `monitor` site gets a
  check of its public URL, `https://<hostname>/healthz` expecting 200, which
  also carries the fork's 14 day certificate warning for that hostname. A
  monitor cannot report its own death, so a monitor on a separate site is
  what notices one that died, and the ping of its site, above, says whether
  it was the host. A monitor on the same site is not checked: it shares the
  host, so it would go down with it and could say nothing about losing it.

Every other app's public check runs from a machine that is not the
gateway, so a dead gateway shows as every public check failing while the
direct checks stay green.

**The heartbeat is a service, not a cron job.** Each watched site's
infrastructure stack runs `heartbeat`: curl's own image, on a track like
every other image the stack runs (see *Images follow tracks*), around a rendered `heartbeat/push.sh` that
once a minute requests `https://<monitor hostname>/ping/<token>` for every
monitor off the site and sleeps beside the requests, so a monitor that is slow
or gone stretches nothing. It is rendered, applied, reconciled and removed with
the stack, and a site that is removed takes its pusher with it; nothing is
installed on the host outside Docker. The token is generated at `init` as
`sites.<site>.heartbeat_token`, 32 lowercase hex characters, the one shape
the fork's seed accepts, and `render` refuses a site whose token is missing
or any other shape rather than let the fork invent a token the host does not
have. One token per site serves every monitor, because the fork keeps tokens
unique within one instance only. The URLs carry it, so they live in a 0600
`heartbeat/heartbeat.env` the service reads through `env_file`, and the 0644
compose file names neither. The push is a plain outbound HTTPS request, which
the host firewall allows by default and every app's federation already
relies on; nothing is opened for it.

The monitor's seed expects each heartbeat at interval 60 with a grace of 120,
which the fork adds to the interval before a site is stale: 180 s, one push
missed and the next one late, since a push that fails outright takes about
33 s (10 s timeout, two retries). The threshold is the same two as every
other check; the fork evaluates heartbeats every 15 s, so it costs one such
tick. The monitor's own site pushes to no one with one monitor, and so gets
no heartbeat, and a monitor site with no other role in `managed_by: external`
therefore still runs no infrastructure stack (in `managed_by: paisans` its Caddy
already gives it one). With two monitors on two sites every site pushes to
both, each monitor site pushes to the other, and each seed expects the
other's site, which is what notices a monitor's host dying. Declaring a
monitor changes `heartbeat.env` on every site, so a new one reaches a host at
that host's next `deployment apply`; `deployment apply` recreates the `heartbeat` service alone for
it, as `up -d` replaces only the service whose configuration changed, and on
the gateway no gate is engaged for it. A deployment with no monitor renders
no pusher.

**Topology commands reseed the monitor.** The seed is correct whenever the
monitor site is applied, and nothing else applies it: `deployment apply --site` is one site at
a time, and `site add`, `site remove`, `storage add` and `app remove` move
files on the sites they change, which the monitor's is not. Left there, a
monitor keeps pinging a site that is gone and alerting for it until somebody
applies the monitor by hand, and never pings a site that joined. So each of
those commands ends with one more stage, the monitor: every monitor site's
`uptime` stack applied on its own (`deployment apply --only <app>`), which writes the
seed rendered from the configuration as it now stands and restarts the
monitor on it, and the fork then adds the checks that are new and deletes
the ones whose site or app is gone; a joining site's heartbeat rides the same
reseed, and its pusher comes with its own infrastructure stack, which the
join applies. The stage's gate reads the seed back and
holds the stack to `deployment apply`'s health gate. It is the last stage on purpose:
the change itself is done without it, and an out of date monitor is a
nuisance, not a wrong cluster, so a failure there stops nothing that already
passed and names the exact `deployment apply` that finishes it. A deployment with no
monitor site skips the stage, and `validate` warns that nothing watches it
(`no-uptime-monitor`): a first install may have one machine, and the monitor
needs a second, so it is a warning rather than a refusal. Every one of these
commands therefore has to reach the monitor's host, and host checks it as
`deployment apply` would.

**The operators heartbeat is a service beside the reconciler.** Each Pocket
ID stack, on every site the app runs on, runs `operators-heartbeat`: the
heartbeat's curl image around a rendered `operators-heartbeat/push.sh` that
once a minute asks the reconciler's `/healthz` over the stack's own network. A
standby answers 200 `standby`, and nothing is pushed. The active instance
answers 200 when every check passes, pushed as success to
`https://<monitor hostname>/ping/<token>`, or 503 with one line saying what is
wrong, pushed to `.../ping/<token>/fail` with that line as a plain text body.
No answer, or any other status, pushes nothing. Every instance pushes to the
same token, `apps.<app>.operators_heartbeat_token`, generated at `init` (and by
`deployment apply`'s dry run, with the other generated secrets) in the site heartbeat
token's shape and refused by `render` the same way, so the one monitor hears
whichever instance is active, and silence past the grace means no instance is.
It pushes to every monitor, the one on its own site included: what it carries
is the operators' standing at Pocket ID, not the site's reachability, and the active instance
may be the one beside a monitor. The interval, grace and threshold are the
site heartbeat's, and a deployment with no monitor renders no pusher. The
fork keeps the body with the ping and shows its first line in the monitor's
recent pings; the incident and the alert say only that the heartbeat
reported a failure. The URLs carry the token, so they live in
`OPERATORS_HEARTBEAT_URLS` in a 0600 `operators-heartbeat/heartbeat.env` the
service reads through `env_file`, as the site heartbeat's do. `secrets rotate`
replaces the token, by name or with `--all`: every monitor learns it first, then the pusher on each site is
recreated with it (*`secrets rotate` replaces a secret where it is checked and
where it is used*).

The same pusher relays the DNS check's two answers when the deployment
renders it. Each cycle it also asks `/healthz/dns-super-admins` and
`/healthz/dns-2fa`, with the same table of answers, and pushes each to its
own monitor under its own token, `apps.<app>.dns_super_admins_heartbeat_token`
and `apps.<app>.dns_2fa_heartbeat_token`, whose URLs are two more variables
in the same 0600 `operators-heartbeat/heartbeat.env`. One pusher serves all three, so the check
adds no service, image or port.

Monitors the file creates are tagged `managed` and belong to it. Monitors an
operator makes by hand, and the channels an operator attaches to any monitor, are
never touched. An operator who ticks **Attach to new managed monitors** on their
channel is subscribed to apps added later; nobody's recipients are in YAML.

**Beside etcd** is why the monitor renders `RETENTION_VACUUM=false`, on
every site it runs on. The fork's full VACUUM rewrites the whole file every six
hours, which is the one burst of I/O worth refusing next to a member whose heartbeats are measured in hundreds of
milliseconds, and at steady state it reclaims nothing that SQLite would not
reuse anyway. It matters wherever the monitor shares a machine with an etcd
member, which `monitor-shares-a-site` warns about on a data site.

**At the edge** the monitor's Caddy refuses `/badge/*`, `/metrics` and the
token API `/api/v1/*` with 404 whatever the app's own settings say, and
`web-server show` carries the same refusal into each snippet for an
operator's own web server. The app is Express with its default routing, which
ignores case and a trailing slash, so every refusal does too: `/metrics/` and
`/Metrics` are refused as well. `/metrics` is public whenever no API token
exists, which here is always. The UI's own session authenticated JSON under
`/api/sites` passes, because the dashboard's live refresh and the response
time chart fetch it. `/ping/*` stays open: it is where every site's heartbeat
arrives, and where a heartbeat monitor added by hand is pinged.

**The public status page** at `https://<hostname>/status` (with
`/status.json` and the `/status.rss` feed) is on by default, so members can
see whether an app is down without an account. It shows each app's check at
its public hostname, `<app> — public`, and nothing else: every other check
names a mesh address, a site or a token, so the seed marks each one hidden.
Every entry in the seed names whether it is shown, so the file decides it at
every boot. A monitor an admin adds by hand starts hidden too (the fork's
default from `1.1.0-oidc.6`), and is shown only by unticking **Hide from
public status page**. `settings.public_status_page: false` turns the page off
in the monitor and refuses `/status*` at the edge and in `web-server show`'s
snippets.

**Sign in** is the identity provider, admitting exactly the group named in
`settings.admin_group`, `operators` when unset. The health page is a tool
for the people who run the machines, and `deployment apply` puts every operator in
`operators`, so a new deployment's monitor admits its operators with nothing
declared. A community that wants its community admins in too names `admins`,
which every operator is also in; `validate` warns (`admin-group-not-admins`)
about a group that is neither, since nobody the deployment records decides
who is in it. The login page goes straight to
it, and password sign-in is off for everyone but one account, kept as the
break glass: Pocket ID is one of the things being watched, and a monitor you
cannot open while it is down is no use. To use it, open
`https://<hostname>/login?local=1` and sign in as `admin` with
`apps.<app>.admin_password` from the secrets file, which `init` generates
(the fork's `README.md:302` at `v1.1.0-oidc.4`). For the same reason the monitor cannot sit behind the gate
(`gated-on-ungateable-blueprint`): the gate signs in through Pocket ID,
and would put the break glass behind the sign-in it is the fallback for.

**It starts as root of its container and drops.** `deployment apply` writes the seed as
the deployment's user, 0600, because it carries the SMTP password; the data
directory is a bind mount the user's rootless Docker creates. The fork's
entrypoint, root of the container's user namespace, hands both to `node` and
drops to it. An image older than that entrypoint ignores the seed.

## Adding a site, and the constraint that shapes it

etcd quorum is a majority: 1 member survives nothing but works; 3 members
survive one loss; **2 members survive nothing and are strictly worse than 1.**

So a join that grows the voters takes the cluster from one to three in a single
operation: the second site and a witness together. It can never rest at two.

With `etcd.members` left out (see *`etcd.members` and `cluster.sites` follow
from the roles*), that is one edit to the roles: the new site gets `data`
and the site in the third location gets `witness`, in the same change.
Giving a second site `data` alone derives two voters, which `validate`
refuses, suggesting a witness in a third location first, and naming the
other way out: `etcd.members` written with one site, which gives up automatic
failover. The toolkit never demands the witness; it refuses only the two
voters (see *Nothing here is permanent*).

A new data site must also have the data sites' architecture, which preflight
compares (see *Architectures*). Its OS release may differ from theirs: Postgres
runs in a container with the image's own glibc, so the host's does not decide
collation.

## The witness is a third location, not a cloud account

A witness is a voting member that stores no data and serves no traffic. It
exists so that when two sites cannot reach each other, exactly one of them can
still form a majority and know it is safe to serve.

**A small always-on cloud VM is the suggested default**, for two reasons: it is
the third failure domain most organizers can actually obtain, and it doubles as
the public entry point for sites on residential connections whose addresses
change. One box, two jobs.

**It is not required.** What is required is a location that fails independently
of both sites. Another organizer's house works. A machine at a workplace, or a
friend's spare room, works.

The `witness` role is what makes a site a voter. With `etcd.members` left out,
every site holding it is one, before the data sites; written, the list says
which sites vote and the role only says what the site runs.

### A witness must never share a failure domain with a voter

Running a witness container beside the database on a single server is worse than
having no witness at all, and the toolkit must refuse it.

One site with one etcd member has quorum 1. It works, and the only thing that
stops it is the machine itself — which would stop Postgres anyway. Add a witness
on that same machine and quorum becomes 2 of 2: now the witness container
crashing takes the database down with it, while Postgres is perfectly healthy.
The machine dying still loses everything, because both members were always in
the same place. A tiebreaker between two things that can always reach each other,
or are both gone at once, breaks no ties.

**One site correctly runs exactly one etcd member.** That is a stable supported
configuration, not a compromise awaiting a fix, and the toolkit should not
pretend otherwise by installing a placeholder.

### The voters that survive one loss must still dial each other

A witness buys the ability to lose one site. That only holds if the voters
left standing can reach each other, and on this mesh reachability is decided
by who is dialled: two sites peer directly when the other dials one of them,
at its `public_address` or its `endpoint` (see *Where the mesh dials a site*),
and two sites that are never dialled never peer at all. Their traffic goes
through the first dialled site, and that path has no fallback,
because the configuration is static and WireGuard does not reroute.

So take the obvious shape, two homes that are not dialled and a VM as gateway and
witness. The VM is the only thing either home dials, and every packet between
the homes goes through it. The VM dying is the exact failure the witness
exists to survive, and it does the opposite: home-a and home-b lose each
other as well as the VM, each is one voter of three, neither has a majority,
Patroni on both loses its DCS, and the database is read-only everywhere.
The site that held no data took the cluster down.

**The toolkit refuses this** (`voters-share-a-relay`): with three or more
etcd members, at least two of them must be dialled. That is the whole
rule, and it is exactly the property that matters. A dialled voter
reaches every other voter, so the voters surviving the loss of any one site
still reach each other precisely when at least one survivor is dialled, and
with one dialled voter in the cluster there is a site, that voter, whose loss
leaves none. Two dialled voters cover every single loss. Two voters are
refused on their own, and one voter has nobody to be partitioned from.

The fix is to make one home dialled: a `public_address` where it has a static
one, or else an `endpoint`. A dynamic DNS name kept current by the router and
one forwarded UDP port is enough: that home is then
dialled by the other, the two peer directly, and the VM is a tiebreaker again
rather than a hub. The example declares `home-a` that way. The relay through
the VM (see *`site add`: a second data site, and the one hard problem*)
remains how a site that is not dialled reaches another that is not, which is fine
for traffic that can wait out an outage and never fine for quorum.

### `cluster.sites` and the `data` role name the same sites

The `data` role says a site runs Patroni and stores the database.
`cluster.sites` is the list every HAProxy's backends are rendered from, the
list `deployment apply` waits on for a leader, and the membership `doctor` and `site
add` check against. A site in one and not the other is a replica the
deployment half has.

Left out, `cluster.sites` is every data site, so the two cannot disagree.
Written, it is used as written, and the toolkit refuses a cluster member
without the role (`cluster-site-without-data-role`) and the inverse
(`data-site-not-in-cluster`), a data site the list leaves out, which would
run a Patroni that nothing routes to or watches.

### Asynchronous replication and automatic failover

`cluster.synchronous: true` makes every commit wait for a standby, which is
what lets a failover keep every write the leader acknowledged (Patroni:
"Synchronous mode makes sure that successfully committed transactions will
not be lost at failover"). Turning it off buys write latency over a slow
link, and the price is paid at the worst moment: with three voters Patroni
promotes a replica on its own, and under asynchronous replication that
replica may be behind. Every commit the old leader acknowledged after the
replica's last received WAL is gone, and Patroni promotes anyway as long as
the gap is within `maximum_lag_on_failover`, "the maximum bytes a follower
may lag to be able to participate in leader election" (Patroni's dynamic
configuration reference, which says plainly that "when using asynchronous
replication a failover can cause lost transactions").

That is a trade an operator may choose, so `validate` warns rather than
refuses (`async-automatic-failover`), and only where it applies: a cluster of
two or more sites with three or more voters. With one voter nothing promotes
anyone, so there is nothing to lose that way.

### Nothing here is permanent

A witness holds no data, so relocating one is cheap in principle: remove the
member, add the new one. Start with whatever third location is available and
move it when a better one appears. No command does the move yet; the table
says what does exist.

If a gateway VM is in use, the third location already exists before anyone asks
for a witness: the join gives the witness role to a machine already running,
rather than provisioning a new one.

| Stage | etcd members | Witness |
|-------|--------------|---------|
| first apply, no gateway | 1 | none: correct, not missing |
| first apply with a gateway | 1 | the gateway serves every app and holds no witness role yet |
| second data site, by `site add` | 3 | given to the gateway, or to a site in a third location, in the same change |
| later | 3 | not built: no command moves a witness to another site; `site remove` takes one out |

A witness is required only as a concept; the toolkit never demands one.
Declining it means **giving up automatic failover**, and no command configures
a separate mode for that: a deployment founded with several data sites and one
etcd member, `etcd.members` written with that one site, runs plain streaming
replication, and promoting a replica is a manual step. That is a legitimate
choice. It is a different mode, not a slightly degraded version of the same
one, and the toolkit says so plainly. `validate` does
(`one-voter-no-failover`): a cluster of two or more sites with one etcd member
gets a warning saying that one member elects nobody, that while the voter's
site is down every other site's Patroni loses etcd and stops taking writes, and
that promoting the replica is a manual step. A warning and not a refusal,
because the mode works as described; what it must not do is pass as the
automatic one. Two voters are refused rather than warned of (`two-etcd-voters`),
since they are strictly worse than one, and the refusal's first suggestion is a
witness.

One voter does not grow by itself. `site add` joins a data site only when
`etcd.members` lists it, because its stages are an etcd join, and listing it
beside one voter makes two. So growing a running one-voter cluster through
`site add` means going from one voter to three in the same change, which is
what adding a witness with the new data site does (see *Adding a site, and the
constraint that shapes it*).

## Configuration

A deployment is declared in two files, kept in its own directory (usually its
own private git repo). The toolkit ships examples under `examples/`.

| File | Contents | Committable |
|------|----------|-------------|
| `paisans-stack.yaml` | topology, sites, clusters, apps, placements, versions | yes, plain |
| `secrets.enc.yaml` | every credential, encrypted with sops + age | yes, encrypted |

They are separate on purpose. Editing topology is the common operation, and
with one combined file every edit would decrypt everything into an editor and a
temporary file. Split, a hostname change needs no key at all, and a change can
be reviewed by someone who cannot read the secrets.

See [`examples/paisans-stack.example.yaml`](examples/paisans-stack.example.yaml) and
[`examples/secrets.example.yaml`](examples/secrets.example.yaml).
[`examples/home-sites.yaml`](examples/home-sites.yaml) is a smaller layout whose
data sites are home connections nothing can dial.

### `paisans create` writes a new deployment's file, and only that

The example declares four sites, eight apps and object storage, and someone
starting a community has to work out which of it they need and which role
combinations validate refuses. `paisans create` asks instead, in the words of
what a community needs (forums, a wiki, a blog, chat, a health page), and
writes a commented `paisans-stack.yaml`.

It offers three shapes, each a role layout validate accepts with no refusal:
one machine; one home and a VPS, where the VPS is the gateway and carries no
witness, because a witness there would make two etcd voters and two survive
nothing; and two homes and a VPS, where one home is dialled too, because with
only the VPS dialled losing it would leave each voter alone. A custom shape
takes any roles and leaves the judgement to validate. What follows from the
answers is decided rather than asked: every app that can join the cluster
joins it when any site holds data, so that adding a data site later is an
addition rather than a move; Garage runs on the data sites whenever a
clustered app stores objects, since a clustered app may not keep files on one
disk; the chat server goes where no database is, since it binds Patroni's
ports; and any layout with two or more data sites, a custom one included, is
written with `cluster.synchronous: true` and `synchronous_strict: false`, so a
failover loses no acknowledged write.

For each app that signs in through Pocket ID it also asks the name shown at
sign in, which becomes the app's `display_name` (see *`oidc client create`
makes an app's client at Pocket ID*). Enter takes the app's key, which is
what the client is named without one, and then nothing is written.

It asks for operators, each with a username, a name, an email address and
their public keys, and offers a second after the first, since `validate` warns
about one (`operators-fewer-than-two`). It writes them as the `operators:`
section, named in every site's `ssh.keys`. The first operator, usually whoever runs
`create`, is offered each public key in `~/.ssh/*.pub` on the workstation
before pasting any; a certificate, or a key the loader refuses, is not offered.
A key another operator gave is refused as it is pasted, since two operators
sharing a key cannot be told apart on a host. It then asks who manages accounts
and writes the answer as the `pocket-id` app's `management` (see *Who manages
accounts: `management`*), since that is never defaulted. With the account
service it adds an `account` app, asking its hostname (`account.<domain>` by
default), how people join (`signup: invite` or `open`), and the Markdown file that
holds the code of conduct, whose text it writes into `code_of_conduct`. With
Pocket ID it asks `allow_user_signups` and the two email settings,
`email_one_time_access_as_unauthenticated` and
`email_one_time_access_as_admin`. The health page's `admin_group` is written
as `operators`.

Mail is required under the account service, because `validate` refuses
`management: paisans` without it: recovery is an email code and the account
service will not start without a mail server. Under Pocket ID it is optional,
and a deployment that skips it gets no `smtp:` block; sign in then mails no
operator a login link and no member a code, and the health page alerts
nobody, so its operators sign in only through a printed link.

It writes one file and nothing else. It asks for no secret, because `init` is
the one command responsible for the secrets file, and reaches no host, because
the sites may not exist yet. It refuses a file that exists: an existing
deployment is edited, not recreated. The rendered bytes are checked exactly as
`validate` checks a file before the operator sees them. A refused file is
written only when the operator asks for it as it stands, so that their answers
are not lost; `validate` and `deployment apply` refuse it until it is fixed by hand.

### The config declares intent; etcd holds truth

Nothing about current state belongs in these files — not which node is primary,
not replication lag. `paisans doctor` asks Patroni for both, which keeps them
in etcd. A config file that starts recording status drifts, and someone
trusts a stale value during an incident.

### Architectures

A site may be amd64 or arm64. The toolkit reads each host's architecture
(`dpkg --print-architecture`) the first time apply, `site prepare` or `site
add` reaches it, and records it as `sites.<name>.arch`. An operator never
needs to write the line; one written by hand is checked against the host
exactly as a recorded one is. It exists so that `validate` can check the
rule below without reaching a host, and so that a host replaced under the
same name by one of another architecture is refused rather than joined. To
accept new hardware, delete the line, and the next run records what the host
reports.

A whole apply reads every site's architecture before it plans, and records
the missing keys before it publishes the signed configuration. `deployment apply
--site`, `site prepare --site` and `site add` record their own site's key
when run on their own, not as a step of a whole apply. A dry run records
nothing. A host that contradicts its recorded value is refused: "<site>:
paisans-stack.yaml records arch <a>, and the host reports <b>. If this is new
hardware, delete the arch line from <site> and run again." Any other
architecture is refused by name.

**Every data site has the same architecture, whichever it is.** Postgres's
data directory and its WAL are written in the host's byte order and
alignment, and Patroni's replication, a failover and `site add`'s
`pg_basebackup` copy them byte for byte. Everything else may differ: apps
sites, an app on SQLite or pinned to its own site, Pocket ID, Garage, etcd,
the gateway, the monitor and the witness. Each of them talks over the
network and copies no database between machines. `validate` refuses data
sites that differ (`data-sites-differ-in-arch`), and apply checks again after
it records the keys. Either refusal says what to do: give the data role only
to sites of one architecture, or, if a recorded value is wrong, delete that
site's arch line and run again.

**Every image must have a build for its site.** Apply reads which platforms
each image was published for when it plans, and refuses one with no build
for the site's architecture before anything is pulled; a variant such as
arm64/v8 counts as arm64. A platform list it cannot read, or a tag it cannot
resolve, becomes a note, not a refusal: the pull says so if the image does
not run. A clustered app needs a build for every apps site's architecture.
A whole apply checks every site's images right after it reads the
architectures, before it prepares any host, so a dry run of a deployment no
host of which is prepared yet still shows the refusal, and `site add` checks
the new site's images before it changes any site.

### `etcd.members` and `cluster.sites` follow from the roles

A site's roles already say which sites hold the database and which are
witnesses, so both lists are optional, and a file that leaves them out says
each fact once:

| Key | Left out | Written |
|-----|----------|---------|
| `etcd.members` | every site with the `witness` role, in name order, then every site with the `data` role, in name order | used exactly as written |
| `cluster.sites` | every site with the `data` role, in name order | used exactly as written |

Witnesses come first because a new deployment is founded witness first (see
*A new deployment is applied witness first*); nothing else reads either list
for its order. Write a key only to choose something the roles do not say:
`etcd.members: [home-a]` beside two data sites runs one voter by choice,
which gives up automatic failover (`validate` warns, `one-voter-no-failover`;
see *Nothing here is permanent*), and listing only some data sites runs
fewer voters than there are data sites. A written key that is empty, or has
no value, is refused: an empty `etcd.members` leaves Patroni nowhere to keep
its leader, and an empty `cluster.sites` says nothing that leaving it out
does not. A finding about a list the file left out names it as `etcd.members
(derived from the roles)`, so an operator who never wrote the key is not sent
looking for it.

`site remove`, the one command that edits either list, edits only a key the
file has. With the key left out, the site's block going, or the witness
role coming off the witness, is the whole edit.
`docs/specs/2026-10-10-derived-members.md` is the approved specification.

### A deployment has an id, and a host knows every deployment on it

`paisans-stack.yaml` carries a top level `id`: a random version 4 UUID, lowercase,
in canonical form.

```yaml
version: 1
id: f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01
```

`paisans init` writes it when it is missing, as one line after `version:`,
leaving every other byte of the file as it was, and never changes one that is
there. `validate` refuses a file without one, or with one that is not a
lowercase version 4 UUID, and says that `init` adds it. **The id never
changes**, because everything a deployment owns on a host is named from it:
a new id is a new deployment, and the old one's containers, directories and
records would belong to nobody.

The id is the identity; its first four hex digits are the **token**, and the
token is what names carry, because a name is read by people and a full UUID in
every container name is noise. With the id above the token is `f2a9`, and:

| What | Name or path |
|------|--------------|
| a compose project | `paisans-<token>-<stack>`, Eg: `paisans-f2a9-talk`, so a container is `paisans-f2a9-talk-app-1` |
| everything apply renders on a host | under `/srv/paisans/<token>/`: `/srv/paisans/f2a9/<stack>/`, `/srv/paisans/f2a9/.paisans-manifest.json` |
| every service and every compose network | the Docker label `community.paisans.deployment=<id>`, the full id |
| a firewall rule `site admit` adds | the ufw comment `paisans-<token>: <why>` |
| the WireGuard interface | `psns-<token>`, Eg: `psns-f2a9`, from `/etc/wireguard/psns-f2a9.conf` by `wg-quick@psns-f2a9`; `deployment apply` renders that file as `/srv/paisans/f2a9/psns-f2a9.conf` and the WireGuard helper `/usr/local/libexec/paisans/wg-psns-f2a9` installs it (see *Two deployments on one host never share a mesh*) |
| `site admit`'s own files | the admission record `/etc/paisans/admitted.<token>.json`, `paisans-<token>-watchdog.service` and, ufw rules tagged `paisans-<token>:` |
| `site prepare`'s own files | under `/srv/paisans/<token>/.paisans/`: the authorized keys sidecar `authorized_keys.owned`, the configuration store `config/<token>.git` with its `.peers` and `.sync.json`, and `bin/config-sync.sh`; and the user's units `paisans-<token>-config-daemon.service`, `-config-sync.service` and `-config-sync.timer` |
| a DNS record `dns init` creates | the comment `paisans-<token>: created by paisans dns init` |

Code that decides whether a Docker object is this deployment's matches the
label, and the full id in it, never a name: `doctor` lists containers with
`docker ps -a --filter label=community.paisans.deployment=<id>`, and `prune`
removes a volume only when it carries that label with this id. A name is
chosen by whoever creates the object, and two deployments on one host share
every naming convention, so a name can only ever say what something is
called. The firewall, unit and DNS markers carry the token: ufw comments
and record comments are read by people too, and the host registry is what
makes the token unique on a host.

**Several deployments may share a host.** Each has its own root, its own
compose projects and its own markers, so none of them reads another's files
as its own or replaces another's containers. What the token cannot do alone
is guarantee that: four hex digits of a random id collide rarely, not never.
So **every host keeps a registry** at `/var/lib/paisans/registry.json`,
root's and 0600, in a directory others may enter but not list (0711), listing
every deployment its owner has admitted:

```json
{"version":1,"deployments":{
"f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01":{"token":"f2a9","root":"/srv/paisans/f2a9","domain":"example.org","site":"vm","claimed_at":"2026-10-01T00:00:00Z","interface":"psns-f2a9","listen_port":51820,"subnet":"10.44.0.0/24","address":"10.44.0.3","roles":"gateway"}
}}
```

`site admit`, which the host's owner runs as root, **claims the host**. The
claim is one shell command run under `flock` on
`/var/lib/paisans/registry.lock`: it reads the registry, refuses if another
id already holds this deployment's token or root (or its WireGuard interface,
listen port or an overlapping mesh subnet, see the next section, or the
gateway or data role, see below), and otherwise adds this deployment's entry,
or refreshes it, in a temporary file it then moves over the registry. A
refusal names the other deployment's id and domain, or its site where its
entry has no domain, and changes nothing, on
that host or any other. The lock is what serialises two admissions of one
host at once, and the move is what keeps a registry from ever being half
written. The merge is done with awk, which a host has before anything is
installed on it, so the file keeps one deployment per line, and a registry in
any other layout is refused rather than guessed at. An entry leaves the
registry one way: `site evict` deletes this deployment's line, under the same
lock and with the same kind of awk program, and every other line stays as it
was. A gateway whose Caddy is kept for the host owner's sites keeps its line,
marked `"kept":"caddy"`, because that Caddy still holds the gateway role and
the root (see *`site remove` takes a site out*).

The registry is root's, so a deployment never reads or writes it. What a
deployment learns about its host it learns from its **admission record**,
`/etc/paisans/admitted.<token>.json`, which `site admit` writes beside the
claim: root's and 0644, in a directory the deployment's user lists and reads
but cannot write. Every command that changes a host, `deployment apply`, `site prepare`,
`storage init`, `storage add`, `storage rotate-key`, `site add`, `site
remove`, `prune`, `operator magic-link`, `app remove`, `oidc client create`,
`secrets rotate` and `failover test`, reads the record of each site it will
write to, as the deployment's user, before its first change. A host with no
record has not admitted the deployment, or has evicted it, and a record that
does not cover the site (its user, SSH and WireGuard ports, mesh subnet, forwarding, the grants its roles need and, for `managed_by: external`, the web server) is
refused with the request line the owner needs. Nothing on any host has
changed when it refuses.

**A host runs one deployment's gateway and one deployment's data site.** The
gateway binds ports 80 and 443 on every interface, and its Caddy imports
`/srv/paisans/<token>/caddy.d`; a data site's Patroni holds the host's
`/dev/watchdog`, which has one claimant (see *There is one HA cluster, and
that is deliberate*). Neither can be split between two deployments, so each
entry records the site's `roles`, sorted and joined with commas, and the
claim refuses a
deployment claiming `gateway` on a host where another already holds
`gateway`, and the same for `data`. The refusal says to use another host for
one of them. Every other role shares a host freely.

**A site is one machine, and a machine runs at most one site of a
deployment.** Everything above is named for the deployment and nothing
narrower: one root, one manifest, one set of compose projects, one WireGuard
interface with one `Address`, one registry entry. Two sites of one deployment
on one machine would write the same files, replace each other's containers and
give one interface two addresses. Nothing needs them there: roles combine on
one site (`roles: [data, apps, gateway]`), and a second environment, Eg: a
staging copy, is a second deployment with its own `id`, which the registry
keeps apart from the first on any host they share. So the claim refuses a host
that already runs another site of this deployment, naming that site and its
mesh address, before anything is written, and a dry run refuses the same way.
Which site a host runs is read from this deployment's admission record, which
names it; on a host with no record, from the `Address` in
`/srv/paisans/<token>/psns-<token>.conf`, matched to the site
`paisans-stack.yaml` gives that address. Reading the name first is what lets a site's address change: the
record still names the site, and `deployment apply` restarts the interface with the new
`Address` (see *`deployment apply` brings the mesh interface up before anything binds to
it*). A host left by a site that `paisans-stack.yaml` no longer declares is cleaned
with `site remove --force` before another site takes it.

`validate` refuses what a file alone can show to be one machine: two sites
with one mesh address (`duplicate-site-address`), and two sites reached at one
ssh host and port, or dialled at one endpoint (`sites-share-a-machine`). Two
sites behind one router may share its public address, each with ports of its
own forwarded to it; that is two machines and is accepted.

A dry run reads the same record and refuses the same way, so it shows the
refusal the real run would meet. `preflight` reads it on the site being
added. `paisans site admit` claims the host for the deployment when its owner
admits it, from the request alone, so a host admitted from a request and one
admitted from the configuration hold the same entry.

### Where the mesh dials a site

Two sites peer directly when one of them is dialled by the other. Which
address a site is dialled on follows from two of its keys, `public_address`
and `endpoint`:

```yaml
sites:
  vm:                                  # dialled on 203.0.113.10:51820
    address: 10.44.0.3
    public_address: 203.0.113.10
  home-a:                              # dialled by its dynamic DNS name
    address: 10.44.0.1
    endpoint: home-a.example.org:51820
  home-b:                              # dialled by nobody: it dials out
    address: 10.44.0.2
```

| `public_address` | `endpoint` | The other sites dial |
|------------------|------------|----------------------|
| set | absent | `public_address` on 51820 |
| set | `":<port>"` | `public_address` on that port |
| set | a name and a port | `public_address` on that port; the name still gets its A record from `dns init` |
| set or absent | an address and a port | the endpoint as written |
| absent | a name and a port | the endpoint as written, resolved by each peer's WireGuard |
| absent | absent | nothing: the site only dials out |

A site with a `public_address` is dialled at that address rather than through a
name that resolves to it, so the mesh never waits on DNS for it: WireGuard
resolves a name once, as the interface starts, and a name whose record does not
exist yet fails the whole interface. A name is dialled only where there is no
address to dial instead, which is the dynamic DNS case: a home whose router
forwards the port. `public_address6` alone is not dialled, because a peer with
no IPv6 route could never reach it; a site reached only over IPv6 writes its
endpoint as an address, `"[2001:db8::10]:51820"`.

So a site with a `public_address` is always dialled, and must accept UDP on its
port: `site admit` opens it on the host, and a cloud provider's own firewall,
where there is one, has to let it through as well. `validate` refuses an
endpoint that is neither `host:port` nor `:port`, a port outside 1 to 65535, and
a port alone on a site with no `public_address`.

A deployment can keep its data on home connections that nothing can dial at
all, with no static address, no dynamic DNS name and no forwarded port. The
homes dial out, and the sites that are dialled are small VPSs: the gateway, and
a second witness so that two of the three etcd voters are dialled (see *The
voters that survive one loss must still dial each other*).
[`examples/home-sites.yaml`](examples/home-sites.yaml) is that layout, with
what it survives.

### Two deployments on one host never share a mesh

Every site of a deployment sits on its mesh: a WireGuard interface with an
address in `mesh.subnet`, which every service binds and every app trusts for
forwarded client addresses. Two deployments on one host each need their own,
and three things about a mesh can collide there: the interface's name, the UDP
port it listens on, and the subnet it routes.

**Each deployment has its own interface, `psns-<token>`.** One interface
carries one address, one private key and one peer list, so a second
deployment's peers in the first one's file would be routed by the wrong mesh.
The name is derived from the token like every other name (`psns-f2a9`), and
everything that touches the interface uses it: `/etc/wireguard/psns-f2a9.conf`,
`wg-quick@psns-f2a9`, its helper `/usr/local/libexec/paisans/wg-psns-f2a9`, the firewall's
`allow in on psns-f2a9` and Docker's boot ordering. It is nine characters,
inside Linux's limit of fifteen: `IFNAMSIZ` is 16 bytes including the
terminating NUL (Linux v6.8, `include/uapi/linux/if.h`).

**A site listens on the port it is dialled on.** That port (see *Where the
mesh dials a site*) is the `ListenPort` rendered for the site and the UDP port
`site admit` opens there. Behind NAT, forward that same port. A site that is
never dialled is rendered without `ListenPort` and opens no port: WireGuard
then picks one ("Optional; if not specified, chosen randomly",
wireguard-tools v1.0.20210914, `src/man/wg.8`), and the firewall lets in the
replies to what it sends as part of that exchange. Two deployments sharing a host behind NAT
therefore never choose ports at all; two sharing a host that is dialled give
it two different ports, with `endpoint: ":<port>"` where the host has a
`public_address`.

**The registry refuses a clash.** Each entry records the deployment's
`interface`, `listen_port`, `subnet` and this site's `address`, and the claim
`site admit` makes, described above, refuses, in the same locked command, when another deployment
on the host holds the same interface, the same listen port, or a mesh subnet
overlapping this one's in either direction (one inside the other, or equal).
The refusal is one short sentence a clash, naming the other deployment's id,
its domain or site and the value that clashes.

**The host itself is checked too.** A mesh subnet also collides with networks
no registry knows about: a LAN, another VPN, a Docker network, or a range
Docker will hand to its next network. Whichever route the kernel prefers takes
the other side's traffic. So `deployment apply` and `site prepare`, dry run or not, read
the host as the deployment's user before they change it, and refuse naming the
clash when the mesh subnet overlaps:

| Source | Read with |
|--------|-----------|
| a route in any table, other than one through this deployment's own interface, so a VPN that keeps its routes in a table of its own (Tailscale's 52, wg-quick's 51820) is seen | `ip -j route show table all` |
| an address and its network, on any other interface | `ip -j addr` |
| a Docker network's subnet | `docker network inspect` over `docker network ls -q`, `IPAM.Config` |
| a pool Docker allocates new networks from | `default-address-pools` in the user's `~/.config/docker/daemon.json` when set, and otherwise Docker's defaults: `172.17.0.0/16`, `172.18.0.0/16`, `172.19.0.0/16`, `172.20.0.0/14`, `172.24.0.0/14`, `172.28.0.0/14` and `192.168.0.0/16` (moby v27.5.1, `libnetwork/ipamutils/utils.go`, `localScopeDefaultNetworks`, which `daemon/daemon.go` uses unless the pools are configured) |

A pool counts before any network holds it, because the next compose project's
first `up` may take a range from it. `preflight` runs the same check on the
site being added.

**`init` chooses the subnet.** Until this deployment is on any site, `paisans
init` reaches every site over ssh as the deployment's user, as the commands
that reach a host do. A host that has not admitted the deployment yet refuses
that login. `init` lists every such site, and why, before it settles
anything. At a terminal, without `--yes`, it asks whether to admit each now
from this machine, and on a yes runs `site admit --site`'s own flow, asking
for the owner's login, each grant and the plan's approval, then goes on with
the site admitted. Otherwise it prints the `site admit` and `site request`
lines that admit the site, and goes on without it, because `site admit`
checks the subnet against the registry, which is root's, and refuses one
that overlaps another deployment's. `--yes` never admits a host. When
`mesh.subnet` is missing, or is declared but overlaps
something, it gathers everything the table above finds on each host it
reached, and rolls a random `10.<a>.<b>.0/24` from
`crypto/rand` that overlaps none of it, rolling again on a collision, up to 32
times; past that it refuses, naming every network that blocked a roll. A /24
holds 254 sites and the rest of `10.0.0.0/8` offers 65536 of them, so a
second roll almost always clears. It writes `mesh.subnet` and moves every
site's `address` into the new network with its host number kept (`10.44.0.3`
becomes `10.212.37.3`), replacing each value where it stands so comments and
layout survive, and says what it chose and why:

```
paisans-stack.yaml: rolled mesh.subnet 10.212.37.0/24 (10.44.0.0/24 overlapped a route on home-a).
  sites.home-a.address 10.44.0.1 -> 10.212.37.1
```

A declared subnet that overlaps nothing is kept as it is. A site that cannot
be reached makes `init` refuse with nothing written, secrets included, since an
unchecked host is exactly where a collision would go unseen. A clash on the
interface is reported the same way, before anything is written.

**`init` moves a listen port another deployment holds, when the port is its
own.** When `site admit`, run from `init`, is refused for the site's WireGuard
listen port and nothing else, and the site is dialled on its `public_address`
with no `endpoint` or with `endpoint: ":<port>"`, admit tries the held port
plus a random step of 2 to 10, then each later try a new step up from the
last, up to 10 tries. A try fails when another deployment in the registry
holds the port, something else on the host holds it (the registry knows only
deployments), or it is above 65535. Something else is a bound UDP socket, a
UDP port the system Docker publishes, running or for a stopped container with
`restart: always`, or a port a DNAT or redirect rule matches, since a
forwarded port has no socket of its own. A negated rule holds every port it
does not name, and a rule with no port match holds every port. A listener,
ruleset or rule admit cannot read stops the search, with the refusal and why;
a host with neither `nft` nor `iptables-save` is one, since no reader is no
proof of no rules, so its port is never moved. `init` writes the first free
port as `endpoint: ":<port>"`, keeping comments as the subnet write does, says
which port to open in any firewall in front of the host and to forward behind
NAT, and admits the site again on it. An endpoint written as a host and a port
names a router or a dynamic DNS name forwarding that exact port, so it is
never moved; a clash on the gateway or data role is not freed by any port, so
the refusal advises on the role alone; and once any site holds the manifest
the port is in every other site's WireGuard file, so it is not moved either.
An admitted site `init` cannot read for the manifest leaves the port as it is
too, with a note naming the site. In each of those cases the refusal stands.

**Once any site holds this deployment's manifest, the subnet is fixed.**
`init` never changes it again, and an unreachable site no longer stops it; it
says the subnet is deployed. From then on `deployment apply` and `site prepare` only
check it, and the advice on a clash is that the other network moves: every
app trusts the mesh subnet and every service binds an address in it, so moving
it is a rebuild of every site.

`validate` refuses a file without `mesh.subnet` and says to run `init`, and
refuses one that is not an IPv4 network, one larger than a /24, one
overlapping `0.0.0.0/8`, `127.0.0.0/8`, `169.254.0.0/16` or `224.0.0.0/3`,
and one outside the private ranges `10.0.0.0/8`, `172.16.0.0/12` and
`192.168.0.0/16`. The hosts' firewalls trust the whole mesh, so a larger one
could cover an owner's LAN; an admitted host's egress table accepts the mesh
before it drops loopback and link-local, so a mesh over those would let the
deployment reach the owner's local services and the cloud metadata address,
and this network and multicast route nothing a mesh could use. Every other
deployment's egress table on a shared host drops the private ranges and no
other range a mesh may use, so a mesh outside them would be one that
deployment's user could reach. It refuses a site address outside the mesh,
and one that is the mesh's network or broadcast address.

`doctor` has a `mesh` section, checked on every site it reaches: the
interface exists and is up (`ip -j link show dev psns-<token>`), and the live
overlap test above passes. An overlap is a `FAIL` naming both sides, with the
advice that the subnet is fixed once deployed and the other network has to
move.

### The image an app runs is declared, not implied

`blueprint` selects which template set renders. It does not decide which image runs.
Keeping those separate is what lets an operator move to a fork, hold a version
back, or take an upgrade on their own schedule, without the toolkit shipping a
release for it:

```yaml
apps:
  talk:
    blueprint: mbin
    hostname: talk.example.org
    placement: cluster
    images:
      app: ghcr.io/example-org/mbin:v1.2.3
```

Every key is optional and an unset one takes the default its blueprint names,
usually a track (see *Images follow tracks*), so an adopter who never opens this
stanza runs the newest release on it.

**A map, not a single `image`.** A stack is several containers, and naming only
the application one would work until the first operator needed a different
Valkey. The keys are service names in the blueprint's compose template, which also
means an unknown key is a typo the toolkit can refuse rather than ignore.

**One full reference per key, not `image` plus `tag`.** Switching to a fork
changes registry, repository and tag together. Split into two fields, a
half-finished switch parses cleanly and pulls something nobody intended.

**Changing `images` is not changing `blueprint`.** A fork that renames an environment
variable, moves a config path or adds a required setting needs a different
template set, and that is a new blueprint. The `images` map is for a different
build of the same software, and pointing it at software that merely resembles
the original produces a stack that renders and then fails at boot.

**Floating tags are refused, not warned about.** `latest` makes two runs of
`deployment apply` produce different deployments from identical inputs, which contradicts
the premise that the config plus the secrets reconstruct the stack. That is
incoherent rather than risky, so it takes a refusal by the rule above. A digest
(`@sha256:...`) is accepted and is the stronger form.

**An image named by a tag runs the build the tag named when apply planned.**
`deployment apply` asks the registry, while it plans, which build each tag an app's stack
names points at now, and renders that digest beside the tag on every line
naming it (Eg: `ghcr.io/example-org/mbin:v1.2.3@sha256:...`). A tag written
into a compose file never changes, and `deployment apply` recreates a stack only when its
file changes, so without this a new build behind a tag, a series tag moved to
a new release or a version tag rebuilt for a security fix, would never reach a
host. With it, a new build is a changed compose file: the dry run
lists it, by app and service, with the builds it goes between and their
versions where the image carries the `org.opencontainers.image.version` label,

```
talk  app  ghcr.io/example-org/mbin:v1-fork  1.15.0-fork (sha256:1a2b3c4d5e6f) -> 1.16.0-fork (sha256:3c4d5e6f7a8b)
```

and the apply pulls, checks, validates and health gates the stack as for any
image change. Every service of a stack naming one image runs one build of it:
Mbin's application and its messenger consumers are pinned together, and a
plan whose rewrite would leave one of them on the bare tag stops. Each tag is
resolved once per run, so every site and service an apply plans gets the same
build. The registry is asked from the workstation, anonymously; when it cannot
be reached, the plan keeps what the stack already runs of the same tag and
says so, and a stack that runs none is refused until it can be. A registry
that could not be reached is not asked again for the rest of the run, so an
outage costs one timeout rather than one per image.

**An apply that moves nothing asks the registry for no manifest.** Docker Hub
limits how many manifests an address may fetch anonymously, and counts every
`GET` of one; listing tags and asking a tag's digest (a `HEAD`) are not
counted. The only manifest apply reads is the platform list it checks each
build against a site's architecture with, and it skips that for a build the
site's host already runs, on the architecture the site's manifest recorded
at the last apply: a digest names one platform list, which cannot change
under it, and the host running the build proves it runs there. A host rebuilt
on another architecture is checked again, since its recorded architecture no
longer matches. The check made before any host is read keeps each digest's
answer on the workstation, under the catalog cache's `platforms/`, for the
same reason. A registry that answers 429 is rate limiting this workstation:
it is treated as unreachable for the rest of the run, the plan keeps what each
site runs and says the registry is rate limiting, with when to retry when it
says, and a stack that runs none is refused until the limit resets. `--only` and a stack an
apply holds back resolve nothing: they keep what the host runs, and nothing
about them is listed or refused.

A stack an older toolkit applied runs its tags bare. Its first apply pins
each to the build the tag names then, and the dry run says `pins` rather
than calling it a new build, since which build the host runs under the bare
tag is not known; the compose file changes, so the stack is recreated once,
on the build pinned. The manifest,
`/srv/paisans/<token>/.paisans-manifest.json`, records each service's build
under `pins`: the stack, the service, the reference as configured, the one
pinned, when it was resolved, and whether it came from the registry, was kept,
was held for the app's other sites, or was configured as a digest.

A digest in `images` is taken as written, so it, or a version tag the
publisher does not rebuild, holds an app on one build until the operator
changes it. Commands that rewrite a compose file for a narrower reason
(`site add`, `site remove`, `storage add`, `storage rotate-key`, `secrets
rotate`) resolve
nothing and keep whatever each stack runs, whatever the configuration now
names (another tag, a moved tag, another repository), so none of them
moves an image nobody planned; the next `deployment apply` shows and makes that move.
The one image they resolve is a track that runs nowhere on the host yet,
such as a new site's etcd in `site add`, since a host cannot pull a pattern:
it starts on the newest build on the track.

### Images follow tracks

The toolkit names no image itself. Every image a deployment runs by default is
named by the **core catalog**, Paisans-Software/paisans-blueprints: an app's
in its blueprint, and the toolkit's own (etcd, Spilo, HAProxy, Garage, the
gateway's Caddy, the heartbeat, Pocket ID and its operator reconciler, the gate,
the account service and the monitor) in its `foundation.yaml`. Each is a
**track**: a repository and a tag pattern in which each `*` matches one run of
digits.

```yaml
pocket-id: "ghcr.io/pocket-id/pocket-id:v2.*.*"
haproxy: "docker.io/library/haproxy:3.0.*-alpine"
```

`deployment apply` and `init` list the repository's tags, take the newest that matches the
whole pattern, and render it with its digest, once per run, so every site in a
run gets the same build. A patch or a minor release therefore reaches a
deployment on its next apply, with no toolkit release and nobody editing
anything; the dry run names each image that moves, from the build it leaves to
the one it moves to. `3.0.*-alpine` matches `3.0.11-alpine` and neither
`3.1.0-alpine` nor `3.0.11-alpine3.20`, so a pattern says exactly how far a
track may move, and the catalog stops each one where an upgrade stops being
routine: etcd at 3.5, RabbitMQ at 3.13, a Postgres major never, since a major
is data and `cluster.postgres_version` chooses it.

**The core catalog follows its newest release too.** Every run lists the
catalog's release tags (`vMAJOR.MINOR.PATCH`, pre-releases never) and reads the
newest one this toolkit can: one whose blueprints and foundation it parses, and
whose tracks stay inside the versions its own code is written against (Pocket
ID's API v2, etcd v3, Garage v1, HAProxy 3, Caddy 2, oauth2-proxy v7). A newer
release it cannot read is skipped, and the run says which and why, so an older
toolkit keeps working on the releases it understands. The deployment section of
the run names the release it read. A deployment that pins `catalogs.core` to a
tag and commit keeps that release until `catalog update` moves it; its images
still follow their tracks. When the catalog's forge cannot be reached, the run
uses the release this workstation last read.

**Holding one image back, or moving it ahead of its track,** is a
configuration key: `apps.<name>.images` for an app's, `acme.image` for the
gateway's Caddy, and `infra` for the infrastructure stack's:

    infra:
      images:            # etcd, haproxy, garage, heartbeat
        haproxy: "docker.io/library/haproxy:3.2.*-alpine"
      postgres:
        "18":
          spilo: "ghcr.io/zalando/spilo-18:4.1-p3"

Each wins over the track, exact or a track of its own, and needs no catalog
release. `infra.postgres` is keyed by Postgres major and applies only while
`cluster.postgres_version` is that major. An etcd, HAProxy, Garage or Zalando
Spilo override stays inside the versions the toolkit is written against, and a
digest there needs a tag beside it; a Spilo from another provider is accepted
with a warning. The heartbeat override also replaces the operators heartbeat
in every Pocket ID stack. `paisans images` lists every image each site runs,
the build the next apply runs, and any override behind the release it
replaced. It exits 0, or 3 with `--check` when a build would move or an
override lags, for a schedule. A registry that cannot be reached shows
`unknown` under newest for the images it could not resolve; only a site that
cannot be read makes it exit 1, after printing the whole table, with that
site's rows `unknown`.

**An app on several sites moves to a new build on all of them at once.** A
clustered app runs one database from every site it is on, and two builds
against one database is how a migration runs twice or not at all. `deployment apply`
without `--site` plans every site and moves them together. `deployment apply --site`
reads the app's compose file on its other sites, keeps the app on the build
they run, and says so, naming a newer build when the tag points at one; moving
the app takes an apply that covers all its sites. Sites that already run
different builds are refused, naming each site's, and the operator chooses with
an apply that covers all of them. A site still on a bare tag beside one pinned
to a build of that tag is not a disagreement: both are held on the pinned
build.

**The forks' blueprints follow their own release tags.** The `mbin` and
`writefreely` blueprints default to a track on each fork's releases
(`ghcr.io/paisans-software/mbin:1.*.*-paisans`,
`ghcr.io/josephquigley/writefreely-wisp:0.21.*-wisp`), whose tags carry the
fork's suffix, so no upstream build can match. A release outside the track
arrives with a core catalog release that moves it, or sooner by naming it in
`images`, which runs it against templates written for the one before.

**A version change is not verified to be safe.** Many of these applications run
schema migrations at boot, against live member data, and a downgrade after one
is usually not a downgrade at all. The toolkit cannot know which release does
that, and must not imply it checked: it warns on a changed reference, names the
backup contract, and proceeds. Verifying upstream's upgrade notes is the
operator's work.

**`paisans check` compares the running image against the declared one.** Someone
will eventually run `docker compose pull` on a host, and the difference between
what is declared and what is running is exactly the kind of drift that is
invisible until a rebuild produces a different stack.

### A config key is placed, not interpreted

An app takes two maps that look alike and are not.

**`settings` is input the toolkit reasons about.** A template asks for each key
by name, and some are validated or acted on: `s3_bucket` is a setting because
`storage init` creates it, and
`sso_dashboard_link` is one because `deployment apply` and `oidc client create` send it
to the identity provider (see *`oidc client create` makes an app's client at Pocket
ID*). An Mbin app's `queue` is one because it decides which services the stack
runs (see *Mbin's queues are in Postgres by default*). A key no template asks
for is ignored without a word, except on a Pocket ID app, where `validate`
warns, `pocket-id-setting-unknown` (see *Pocket ID's configuration is
rendered from paisans-stack.yaml*). That is the direction in which choosing
wrong is silent, and the reason this section exists: an application option put
under `settings` renders nothing and says nothing.

**`config` is passthrough.** The toolkit does not interpret the key. It does not
know whether the application has such an option, does not check the value's
type, and decides nothing with it. It places the key, in that file's own syntax,
in the file the app's kind renders, and has no other opinion about it. A typo
renders cleanly and is the application's to ignore or refuse at boot.

The kinds do not share a format, so a key means something different per kind:

| Format | Kinds | File | A key means |
|---|---|---|---|
| env | mbin, wikijs, pocket-id, oauth2-proxy, uptime, account | `.env` | the variable name |
| ini | writefreely | `config.ini` | `section.key`, exactly one dot |
| yaml | synapse | `homeserver.yaml` | a nested path, any depth |
| json | element | `config.json` | a nested path, any depth |

One of each:

```yaml
apps:
  talk:
    blueprint: mbin
    config:
      MBIN_DEFAULT_THEME: dark                  # env: a variable in .env
  blog:
    blueprint: writefreely
    config:
      app.max_blogs: 3                          # ini: max_blogs in [app]
  chat:
    blueprint: synapse
    config:
      require_auth_for_profile_requests: true   # yaml: a top level key
  web:
    blueprint: element
    config:
      default_theme: dark                       # json: a top level key
```

Values are scalars: a string, a number or a boolean. A list or a map is
refused, because no format here has one way to write it that every application
reads the same.

**Where the key lands.** Env appends a labelled block to the rendered `.env`,
quoting a value only where compose's `env_file` would otherwise rewrite it, so
the application receives it byte for byte. Ini
inserts the key into the section that is already there, after its last
assignment, rather than repeating the section header; a section the template
does not write is created at the end. Yaml appends a new top level key as its
own block and leaves the rest of `homeserver.yaml` byte for byte as rendered.
A key under a mapping the template already writes, such as
`database.args.sslmode`, makes the file be re-emitted: comments survive, but the
blank lines between top level keys do not. Json keeps the template's key order.
In every format except json, which has no comments, a comment says `config`
put the key there: above the appended block in env and yaml, above the inserted
lines in ini, and on the key itself when yaml re-emits under an existing
mapping.

Three mistakes are knowable from the file alone, so `paisans validate` refuses
them:

**A dotted key on an env file is refused as
`config-key-is-nested-in-an-env-file`.** Env has no nesting, so a dot is a typo
for an underscore rather than a path.

**A key named like a credential is refused as `config-key-looks-like-a-secret`.**
`paisans-stack.yaml` is plaintext and meant to be committed; the value belongs in
`secrets.enc.yaml`. The check is by name: the key is lowercased, `_`, `-` and
`.` are removed, and what is left is searched for `password`, `secret`,
`token`, `apikey` and `privatekey`, so `SENDGRID_API_KEY` and `privateKey` are
both caught. It will not catch a credential called something else. It refuses
the one thing it can see and does not claim to be a policy.

Being by name, it also refuses some keys that are not credentials at all.
WriteFreely's `app.disable_password_auth` is a switch that turns the password
login off, and it is refused because its name contains `password`. Such a key
needs a `settings` key or a template change.

**An env key starting `COMPOSE_` or `DOCKER_` is refused as
`config-key-steers-compose`.** `deployment apply` runs `docker compose -f
/srv/paisans/<token>/<app>/compose.yaml up -d`, and compose reads the `.env` beside that file
for its own settings as well as handing it to the application. With
`docker compose config` on Docker Compose v5.4.0, run on the operator's Mac
rather than a host, `COMPOSE_PROJECT_NAME=other` in that `.env` renamed the
project to `other` and `COMPOSE_PROFILES` switched on a service the compose
file kept behind a profile. `DOCKER_HOST` in the same `.env` did not redirect
`docker compose ps` in that run; `DOCKER_` is refused anyway, as the docker
CLI's own namespace, so the rule does not depend on knowing which of those
compose honours from a file.

**A key the template already writes is refused when rendering, as
`config-key-already-rendered`.** The message names the key and the file, and
either the template that writes it or, when there is one, the `settings` key
that is the sanctioned input for the same value. `config` adds; it never
overrides. Two sources of truth for one value is how a deployment ends up with
a setting nobody can locate, and an override would route around decisions the
templates encode: `database.type = sqlite3` into a blog that belongs in the
cluster is refused this way, because the template writes `type = postgres`.

The same rule covers a key a template leaves out on purpose, because no
collision would ever catch one of those coming back. The message says the
template omits it by decision, and why. The list is limited to what a
template's own comments say it leaves out on purpose, and today that is:

* Synapse: `oidc_providers`, `oidc_config`, `saml2_config`, `cas_config`,
  `jwt_config` and `password_config`, and any path beneath them, such as
  `oidc_config.enabled`. The homeserver is a resource server behind Matrix
  Authentication Service, and an identity provider or password database of its
  own would be a second way in.
* WriteFreely: `uploads.dir`. Uploads go to S3, and `dir` is read only by the
  local image store, so it would do nothing.

Any other key the toolkit does not render is passed through, so this list is
the whole of what is enforced beyond collisions. To change a template owned
value, use its `settings` key if it has one. If it has none, the answer is a
pull request that makes it one, or a template change, and both are reviewable
where a local override is not.

For the env kinds the check reaches past `.env`. A variable the stack's
`compose.yaml` sets under `environment:`, or interpolates as `${VAR}`, is
refused too, because compose gives `environment:` precedence over `env_file`
and the `.env` value would be dropped without a word. That precedence was
observed with `docker compose config` on Docker Compose v5.4.0 on the
operator's Mac; the version on any host was not checked.

**Some keys cannot be written at all.**

* A key whose own name contains a dot. In yaml and json a dot is always a path
  separator, so Element's `setting_defaults` key `UIFeature.feedback` cannot be
  expressed. A path that could mean an existing dotted key, such as anything
  under `default_server_config.m.homeserver`, is refused as ambiguous rather
  than guessed at.
* An ini section whose name contains a dot. `[oauth.generic]` is unreachable,
  because a key names exactly one section and one key.
* The ini section `DEFAULT`, which is the parser's name for the lines before
  any header.
* An ini value that WriteFreely's parser would read as something else: one
  containing `;` or `#`, which it takes as a comment; one with leading or
  trailing whitespace, which it trims; one that starts with a quote or a
  backtick, which it strips; one ending in a backslash, which joins the next
  line onto it; and one with a newline.

**WriteFreely reads a passthrough setting from `config.ini` once.** The fork
keeps most of `[app]` and `[uploads]` in its database, moving each from
`config.ini` on the first start against an empty database, and from then on
the database is in force for it. The template's own database settings are not
moved: the file locks them (see *Mbin and WriteFreely lock what the toolkit
renders*), so the fork reads them from the file on every start. A passthrough
key is not locked, because it is the operator's, and so it is moved once like
any other. That was run: a key added to `config` after the first start is
rendered, and when the integration test restarted the blog once against the
existing database, the fork logged `config.ini disagrees with the database on
app.max_blogs; the database value is in force` and `writefreely settings get`
still reported `0`, the value the database already held. From then on the
setting is the blog admins', on its settings page or with `writefreely
settings set <name> <value>`. This is the fork's design, not the toolkit's.

**What has been run, and what has not.** The ini insertion is proven against the
software: `go test -tags writefreely_integration ./internal/render/` boots the
pinned fork against the `config.ini` this toolkit renders, with a Postgres
beside it, and reads the value back with `writefreely settings get`, along with
the same file less that one line as a control. `PAISANS_WF_IMAGE` runs it
against another build, such as a release newer than the one the kind names. The yaml and json merges are
proven against Go's parsers only. Nothing here runs Synapse or Element, so the
tests show that the rendered file parses and carries the value at the path
asked for, not that those applications accept it.

### Three kinds of secret

Most of `secrets.enc.yaml` is machine-authored. Nobody invents forty passwords.

| Kind | Examples | Origin |
|------|----------|--------|
| **Generated** | Postgres superuser/admin/replication passwords, Garage keys, WireGuard private keys and heartbeat tokens, Pocket ID's operators heartbeat token and its DNS check's hash key and two heartbeat tokens, the heartbeat token of each app whose blueprint declares a heartbeat, Mercure JWT, RabbitMQ and Valkey passwords, Mbin's OAuth2 server keypair | created at `init`, never typed or seen |
| **Pasted** | Cloudflare API token, SMTP password (`external.smtp_password`, or per app), the optional DNS members token (`external.dns_members_token`), an operator's address at the DNS provider when it is not their email (`operators.<operator>.dns_provider_email`) | issued elsewhere, supplied by a human |
| **Captured** | OIDC client secrets | belong to a running service; recorded here, by `deployment apply` or `oidc client create` for Pocket ID |

**`init` never replaces a generated secret; `secrets rotate` does, on
purpose.** A secret is held where it is checked and where it is used, so a new
value in the file alone breaks every service still on the old one. Re-running
`init` must be safe, so it only fills in what is missing. Replacing one is a
staged change with its own command (*`secrets rotate` replaces a secret where
it is checked and where it is used*).

**Mbin's OAuth2 keypair is generated, not placed.** Mbin signs the tokens it
issues to API clients and apps with an RSA key that its image does not create;
upstream's Docker install has the operator run `openssl genrsa -des3 ... 4096`
on the host (`docs/02-admin/01-installation/02-docker.md` in the Mbin
repository). `init` generates the same thing instead: 4096 bits, the private
half encrypted with the app's `oauth_passphrase`, both halves kept in the
secrets file and rendered to `/srv/paisans/<token>/<app>/oauth/`. A host step is what the
toolkit exists to remove, and keeping the pair in the secrets file is what
gives every apps site under cluster placement the same one, so a token one
site issues verifies on another. `init` never replaces it, and replacing it is
visible to members: a new key signs out every API client at once, which is why
`secrets rotate` asks before it does (*`secrets rotate` replaces a secret where
it is checked and where it is used*).

**It is also the one private key rendered 0644, on purpose.** `deployment apply` writes
every file as root, and Mbin reads the key as uid 1000 after its entrypoint
drops privileges, so a 0600 key is a key Mbin cannot open. The key is
encrypted, and the passphrase that opens it is only in the 0600 `.env`, so a
host user who can read `private.pem` holds ciphertext. That keeps the generic
writer free of any one image's runtime user and leaves the entrypoint's own
privilege drop in place.

The captured case matters. An identity provider holds a client secret and the
app needs the identical value; recording it here makes it reproducible instead
of existing on exactly one host. Creating one is a privileged mutation, and
for the deployment's own Pocket ID the approval is the declaration: an app in
`paisans-stack.yaml` whose kind has a known client shape approves that client, so
`deployment apply --execute` creates it as part of setting the app up, and a dry run
prints every mutation first. For Pocket ID the value is not even captured.
The toolkit generates it, records it here, and then hands it to Pocket ID,
which accepts a caller supplied secret; see *`oidc client create` makes an
app's client at Pocket ID*.

**A pasted or captured secret is one line, and the file is refused otherwise.**
Each is rendered onto one line of an env file (Eg: `ACME_DNS_TOKEN=` in
Caddy's), so a line break inside one would start a line of its own that sets
any other variable of that service, such as Pocket ID's `STATIC_API_KEY`. No
token, password, ID or address any provider issues holds one, so a control
character or a Unicode line or paragraph separator in one is a paste or a
decryption gone wrong. `secrets set` and `secrets rotate` refuse such a value,
and loading the file refuses one already in it, naming the key and never the
value; `secrets set` on that key is the repair, and reads the file with that
one value passed over. Generated secrets are not held to it, since a PEM key
is several lines. The templates also write these values through the same
quoting a passthrough key gets, so a `$` or `#` in a token reaches the service
as it was issued.

**The file names its format and its deployment.** Its first two keys are
`version`, the secrets file's own format (currently 2, and independent of
`paisans-stack.yaml`'s `version`), and `id`, the id of the deployment it belongs to,
the same value as `paisans-stack.yaml`'s `id`:

```yaml
version: 2
id: f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01
cluster:
  ...
```

Every write of the file sets both, whether by `init`, `deployment apply`, `secrets set` or
`oidc client create`. A file with a newer `version` is refused, since a newer
toolkit wrote it. A version 1 file is read as it is and written as version 2,
the first with the backup section: a toolkit that reads only version 1 would
drop that section, and with it the two keys every backup needs, so it must
refuse the file instead. A file whose `id` is not `paisans-stack.yaml`'s is refused by every
command that reads it:

```
secrets.enc.yaml belongs to deployment 0c1d…, not f2a9…
```

It holds another deployment's credentials and the state captured from its
running services: OIDC client credentials and Pocket ID group IDs name objects
at that deployment's Pocket ID, and used here they would quietly point at
things this deployment does not have. To start this deployment fresh, move the
file aside and run `paisans init`; `paisans deployment apply` creates a new one as well. A
file without an `id` or a `version` is read, and has both from its next write.

**Pasted and captured secrets go in through a pipe, or a prompt with echo
off.** `paisans secrets set <dotted.key>` reads the value from stdin, writes it
into `secrets.enc.yaml` re-encrypted to the recipients in `.sops.yaml`, and
prints only `Set <key>.`:

```
security find-generic-password -s acme -w | paisans secrets set external.acme_dns_token
```

Every other way of getting a value into the file leaves a copy: an argument is
in shell history and in `ps`, an echoed prompt is in a scrollback buffer, and
`sops secrets.enc.yaml` opens the whole decrypted file in an editor that may
keep swap or backup files. Given a key at a terminal, it asks for that key's
value on the controlling terminal with echo off rather than reading what is
typed on stdin, says whether the key is already set (never its value), and
writes nothing on an empty answer. That is how a set credential is replaced by
hand, Eg: a DNS token reissued before `paisans secrets rotate`.

Given no key at a terminal, `paisans secrets set` asks for each secret still
owed in turn, saying why each is owed, and reads the answer on the controlling
terminal with echo off, which leaves no copy on the screen. The owed list is
the one `init` and `deployment apply` print, so the operator is asked for exactly what
they were told to supply. Each answer is written before the next is asked, so
an interrupted walk keeps what was typed; an empty answer skips the key, which
stays owed and is named at the end. A sign in client is never owed, so it is
not asked for: it is pending, a client ID and a secret together that `deployment apply
--execute` creates at Pocket ID and records.
With no key and a pipe on stdin it refuses, since one value could belong to
any owed key. It accepts
a key that is already set (a rotation), a key `init` reports as owed or
pending (a client made by hand may be recorded), or any key
under `external`; it refuses a generated key nobody created, because `init`
creates those with the shapes their consumers require, and it refuses an unknown
top level section, because a value written where nothing reads it looks
delivered and is not.

**Secrets are listed by path, never by value.** `paisans secrets list` prints
every secret the configuration needs, and every other one the file holds for
something it still declares, by the dotted path `secrets set` and `secrets
rotate` take: whether the file holds a value there, which of the three kinds it
is, and the path `secrets rotate` takes for it. It prints no value, no part of
one and no length, because an operator looking for a path has no use for the
value, and a value on a terminal is in its scrollback. The kind says who fills
an unset one: `init`, `secrets set`, or `deployment apply --execute`. See *`paisans
secrets list`* in [docs/commands.md](docs/commands.md).

**Secrets nothing declares are pruned.** A site or an app taken out of
`paisans-stack.yaml` leaves its secrets behind. A secret is orphaned when it names
something the configuration does not declare:

| Secret | Orphaned when |
|---|---|
| `sites.<name>` | no site `<name>` is declared |
| `apps.<name>` | no app `<name>` is declared |
| `oidc_clients.<name>` | no app `<name>` is declared |
| `pocket_id_groups.<name>` | no Pocket ID app's `signup_default_groups` names `<name>` |
| `backup.destinations.<name>` | no backup destination `<name>` is declared |

The restic password and WAL-G's key are never orphaned, even with no `backup`
section: they decrypt every backup already taken.

**What is deployed is recorded on each gateway**, so that a site or an app
missing from `paisans-stack.yaml` by mistake (commented out, renamed, a bad merge)
does not lose its secrets. `/srv/paisans/<token>/.paisans/deployed.json` on every
gateway lists the names of the sites, apps and Pocket ID groups deployed, and
nothing else. It is the deployment user's, in that user's mode 700 state
directory, written under the deployment's one lock there and left 0644 by a
rename. It is the deployment user's own file, read as that user, so `deployment apply --site` of a
site that is not a gateway reads every gateway's record for its
warnings without more than its ssh login. A gateway's `deployment apply` adds every name the yaml declares and never
takes one out, so an `deployment apply` of a yaml mid-edit cannot shrink it. Only the
removal commands take names out, on every gateway: `site remove` in its stage
2, `site remove --force` for a site no longer declared, and `app remove` for
the app and each group no remaining Pocket ID app names. Cleaning a gateway's
host deletes its record.

The gateways' records are kept in step, name by name. Every add of a name
makes a new random tag, and a removal records the tags of the adds it saw; a
name is deployed while it has an add no removal saw. A gateway's `deployment apply` adds
every declared name with a new tag, deployed or not, so an add made on a
gateway that missed a removal survives that removal, and a gateway that missed
a removal does not bring the name back. Every change merges the records of the
gateways that answer, all their tags, makes its own, and writes the result to
every gateway that answered; when every gateway answered, it is compacted
first, to one tag per deployed name. A gateway that does not answer is warned
about, never a failure, and the next change brings it up to date; one whose
record another command changed meanwhile is warned about too, with "run this
command again". The record also carries an `updated_at`, the writer's clock,
for people reading the file; nothing compares it.

| In `paisans-stack.yaml` | In a record | `init` and `deployment apply` | `secrets prune` |
|---|---|---|---|
| yes | either | nothing | keeps its secrets |
| no | yes | warn that it is deployed but no longer declared, with what to do, whether or not it has secrets | keeps its secrets, and says why |
| no | no | warn that its secrets are orphaned | removes them |

`init` and `deployment apply` warn by key and never by value, and never refuse: a gateway
whose record cannot be read is a warning too, and one not applied since the
record existed is silent. `deployment apply` without `--site` reads each gateway's record
once, before its first step, and shows each of these warnings once, under
`secrets`; the applies it runs as steps leave them to it. `paisans secrets prune` reads every gateway's record
and refuses when one cannot be read or has none: apply the gateway first, or
pass `--without-record` to trust `paisans-stack.yaml` alone, which with `--execute`
asks for the word `prune` at a terminal, or takes it from `--yes` (see
*`--yes` answers confirmations in a script*). It lists what it would remove and
changes nothing; with `--execute` it removes them and writes the file,
encrypted to the same recipients, and refuses an encrypted file with no
`.sops.yaml` beside it rather than writing it in plaintext. Pruning an `oidc_clients` entry does not
delete the client at Pocket ID, which it says: delete it there by hand if that
Pocket ID still runs. Pruning a backup destination's credentials does not delete
its key at the provider either, which it says too. A removed site's WireGuard key is safe to prune once no
site's mesh lists it, which is true after a full `site remove`, or after every
remaining site's next `deployment apply`. How to run it, and its flags, are under
*`paisans secrets prune`* in [docs/commands.md](docs/commands.md).

**A site running the toolkit's Caddy without its DNS token is refused at
`render` and `deployment apply`**: a gateway, or a monitor in `managed_by` `paisans`. `init`
lists `external.acme_dns_token` as owed rather than refusing, because an
install is assembled in steps. Rendering without it is different: the gateway's
Caddy starts, loads its configuration, and fails every DNS-01 challenge, so no
hostname gets a certificate and the apply reports success. The first person to
find out would be a visitor.

**A hand edited Garage key is refused as `garage-key-is-malformed`.** A
generated S3 access key ID is the literal `GK` followed by exactly 24
lowercase hex characters, and a generated secret key is exactly 64 lowercase
hex characters; a generator only ever produces that shape, so this cannot fire
on a generated value. It exists for a hand edited `secrets.enc.yaml`, where
Garage would otherwise refuse the credential at provisioning time on a host,
after it has already imported some keys, with a message about hex encoding
that names no field and no file. This check lives in `internal/secretsgen`
as the exported `CheckGarageKeys(cfg, secrets)`, rather than in
`internal/validate`: `validate.Check` takes only a `*config.Config` and never
sees the secrets file, so it has nothing to check this against. `Fill` calls
it for `init`, and `render` and `deployment apply` both call it themselves right after
`config.LoadSecrets`, because both load secrets directly and never call
`Fill`: a key hand edited into the file after the last `init` has to be
caught on every path that can reach a host, not only on the one that
happens to regenerate secrets. `site add`, `site remove`, `storage init`,
`storage add` and `storage rotate-key` call it the same way, right after they
load the secrets, and `secrets set` calls it with the new value in place,
before it writes the file, so a malformed key is refused there rather than at
the next render.

### `secrets rotate` replaces a secret where it is checked and where it is used

After an operator who could read `secrets.enc.yaml` leaves, or a site is cut
off with `site remove --host-gone`, every secret they could read has to change,
or their copy of it keeps working. **`paisans secrets rotate <path>... |
--all`** does it without anyone editing a file on a host. A secret is named by
its path in the secrets file, Eg: `cluster.standby_password`; Mbin's OAuth2
passphrase, encryption key and keypair are one unit, named `apps.<app>.oauth`,
because a private key the passphrase beside it does not open is an Mbin that
cannot sign a token. `--all` is every secret in scope the deployment has. How
to run it is under *`paisans secrets rotate`* in
[docs/commands.md](docs/commands.md);
[docs/specs/2026-10-12-secrets-rotate.md](docs/specs/2026-10-12-secrets-rotate.md)
is the approved design.

**One secret at a time, in four stages, each ending at a gate:**

| Stage | Work | Gate |
|---|---|---|
| 1. Next value | a new value generated on the workstation, or for a pasted secret the one its issuer gave, written to the secrets file under `rotations`, beside the current one | the file decrypts and holds it |
| 2. Where it is checked | the side that verifies the secret learns the new value: `ALTER ROLE` on the Patroni leader or in a pinned app's own Postgres, `rabbitmqctl` in Mbin's broker; for the DNS token, a read only zone lookup at the provider with the next token; nothing, where the checking side reads the secret only from what stage 3 renders | the checking side accepts the new value |
| 3. Where it is used | every stack that presents the secret, by scoped `deployment apply`, one site at a time | each stack healthy before the next site |
| 4. Promote | the next value becomes the secret's own, and its entry under `rotations` is removed | the file decrypts and holds only the new value |

**The checking side goes first** because some services read a secret only
when they first initialise: the Postgres roles Spilo creates at bootstrap, a
pinned app's own Postgres, and RabbitMQ's default user keep the old value
however often the file is rendered again. Changing it inside the service
before any user presents it means a user that reconnects with the new value
is let in. Stage 3 renders from the secrets with the next value in place,
while the file still names the old one, so a user is handed exactly the value
the checking side already accepts.

**The next value lives in the file from stage 1**, so a run that stops after
Postgres holds the new password and before the apps do still has it. A rotation
is pending while its path is under `rotations`; a re-run finishes the pending
ones first, asks the checking side whether it already accepts the value, and
hands the users the same value again, which changes nothing on a user that
already presents it. A `--all` run records what it has left
(`rotate_remaining`), so a stopped `--all` run resumes with those and does not
rotate again a secret it finished. Because an operator who runs `--all` expects
every secret, the resume is said at every verbosity and in the result, and a
recorded path the deployment no longer has a rotation of, Eg: an app since
removed, is refused rather than left on the list for ever. **`--all --restart`**
discards the record and rotates the full list again; it is refused while a
rotation is pending, which has to finish first. Every write is encrypted to the
recipients `.sops.yaml` names, so once the departed operator is out of it and the
file is re-encrypted, a next value is never readable by them.

**`deployment apply` refuses while a rotation is pending**, with or without `--site`, and
names the `secrets rotate` that finishes it: some hosts may already check or
present the next value, and `deployment apply` renders the old one and would hand it back
to them. So does every other command that renders from the secrets file and
reaches a host, for the same reason: `site add`, `site remove`, `storage add`,
`storage rotate-key`, `app remove` and `mesh rekey`. The commands `secrets
rotate` runs in its place in the order, `storage rotate-key` and `oidc client
create`, are reached only once every rotation before them is promoted, so none
is pending then. A rotation that cannot finish is ended by hand (*A rotation
that cannot finish*). **`secrets rotate` refuses while a `mesh rekey` is
pending**, naming the rekey: its scoped applies render each site's current
WireGuard key, which a site part way through its rekey may no longer run.
Finishing either command first costs nothing but its resume.

**What each secret costs:**

| Secret | Checked by | Used by | Cost to members |
|---|---|---|---|
| `external.acme_dns_token` | the provider, asked for the zone with the next token at stage 2 | each site running the toolkit's Caddy | none |
| `external.smtp_password`, `apps.<app>.smtp_password` | the mail provider, logged in to with the next password from this machine, then from every site running an app that uses it, before any site is handed it | each app that logs in to send mail with it, which takes a `username` | none |
| `oidc_clients.<app>.client_secret` | Pocket ID | the app | none |
| `apps.<app>.oauth` (Mbin's OAuth2 unit) | Mbin | Mbin | every API client and app signs in again |
| Mbin `app_secret`, oauth2-proxy `cookie_secret` and `session_store_password`, uptime `session_secret`, account `session_key` | the app | the app | signed in browser sessions of that app end |
| Synapse `mas_signing_key`, `mas_matrix_secret` | MAS, the homeserver | MAS, the homeserver | Matrix sessions end, on the stricter reading |
| `apps.<app>.s3_access_key_id` and `s3_secret_access_key` | Garage | the app | none |
| Pocket ID `static_api_key`, uptime `admin_password` | the app | the toolkit, the operator, and the account service, whose stack is re-applied with it | none |
| account `email_hash_key` | the service | the service | kept: `secrets rotate` refuses it and `--all` leaves it alone; `paisans secrets set` changes it and forgets every declined address |
| `cluster.superuser_password`, `cluster.admin_password`, `cluster.standby_password` | Postgres | Patroni on every data site | reconnects; each replica's Patroni restarts in turn and the leader last, after a switchover |
| `apps.<app>.database_password` | Postgres | the app | reconnects |
| Mbin `rabbitmq_password`, `valkey_password`, `mercure_jwt_secret` | the broker, the cache, the hub | Mbin | a restart of the Mbin stack on each site in turn |
| `storage.garage.admin_token` | each Garage node | nothing: the toolkit drives Garage through its command line | none |
| `storage.garage.rpc_secret` | every Garage node | every Garage node | storage unavailable while every node restarts together |
| `sites.<site>.heartbeat_token` | the monitor | the site's heartbeat | its pushes refused while the monitors restart and the pusher switches, normally within the grace before a heartbeat counts as missed |
| Pocket ID `operators_heartbeat_token`, `dns_super_admins_heartbeat_token`, `dns_2fa_heartbeat_token` | the monitor | the operators heartbeat on every site the app runs on | its pushes refused while the monitors restart and the pushers switch, normally within the grace before a heartbeat counts as missed |
| `apps.<app>.heartbeat_token`, for a blueprint that declares a heartbeat | the monitor | the app, on every site it runs on | its pushes refused while the monitors restart and the app switches, normally within the grace before a heartbeat counts as missed |
| Pocket ID `dns_email_hash_key` | the reconciler | the reconciler, with the hashed mapping rendered under it | a restart of the Pocket ID stack on each site in turn |

Mbin's `app_secret` signs its four month remember me cookie, not the sessions
it keeps in Postgres, so an open session lasts until it ends and the next sign
in is asked for. MAS's documentation does not say whether Matrix sessions
survive either of its secrets changing, so the plan takes the stricter reading.
A cluster with one data member has no replica to switch over to, so its
Patroni restarts in place and the database is unavailable for that restart;
the plan says so.

**A heartbeat token reaches the monitors before the pusher.** The monitor
checks a heartbeat token, and its seed is the only place it learns one, so
stage 3 first applies each monitor that checks the token, its uptime app's own
stack on its own site, one at a time, which rewrites its seed with the next
token and restarts it to adopt it. Only then is the pusher handed the token.

* **`sites.<site>.heartbeat_token`** is checked by every monitor off that
  site, a monitor never watching its own. The pusher is the site's
  infrastructure stack, where the changed `heartbeat/heartbeat.env` recreates
  the `heartbeat` service alone. A site no monitor watches renders no pusher,
  so its token is replaced in the file and handed to nobody.
* **A Pocket ID app's `operators_heartbeat_token`**, and its DNS check's two
  tokens the same way, is checked by every monitor,
  the one on a site the app runs on included. The pusher is the app's own
  stack on each of its sites in turn, where the changed
  `operators-heartbeat/heartbeat.env` recreates the `operators-heartbeat` service
  alone: `up -d` replaces only the service whose configuration changed, so
  Pocket ID, its reconciler and its database keep running and nobody is
  signed out. A deployment with no monitor renders no pusher, so the token is
  replaced in the file and handed to nobody.
* **An app's own `heartbeat_token`**, for a blueprint that declares a
  heartbeat, is checked by every monitor in the same way. The pusher is the
  app's own stack on each of its sites in turn, whose templates render
  `.HeartbeatURLs` with the token, so the blueprint's service that reads them
  is recreated with it. A deployment with no monitor renders no URL, so the
  token is replaced in the file and handed to nobody.

From the first monitor learning the token until the pusher sends it, a monitor
that already holds it answers the old one with 404. That lasts one scoped
apply and health gate per monitor and then the pusher's, which normally
finishes within the grace the monitor allows before counting a heartbeat
missed: the 60 second push interval and 120 seconds after it. A run that
stops in between leaves the pushes refused until it is resumed.

**`--all` runs them in the table's order**: what works from the internet
first, the DNS token, the mail passwords, client secrets and signing keys;
then what only the mesh reaches. The client secrets and S3 keys run as their
own commands already do, in their place in the order: `oidc client create
--rotate-secret`, then each site running the app applied in turn, and
`storage rotate-key --app`, each deleting the old secret only once every site
runs the new one (*`oidc client create` makes an app's client at Pocket ID*,
*Replacing an app's key: `storage rotate-key`*). `--all` refuses a secrets
file that lacks a secret `init` generates, naming `paisans init`, since a run
that skipped one would leave it as the departed operator knew it.

**A pasted secret is replaced at its issuer, and pasted when the run asks.**
The DNS token and the SMTP passwords are issued by a provider, and nothing
here can issue them. When every host using one runs the value the secrets file
holds, the file has nothing new to hand out, so `--execute` asks for the new
value at a terminal: it names the path and where it is replaced (the
`acme.provider` for the DNS token, the mail provider for an SMTP password),
the operator replaces it there, and pastes the new one with echo off. Every
pasted secret in the run is asked for before any host is reached, with the
`sign out` confirmation, so a run never stops part way to wait on a person
(Founder decision). The answer goes under `rotations` as the next value,
exactly as a generated secret's does, and stage 4 makes it the file's own, so
a stopped run resumes with it and asks for nothing again. An empty answer
stops the run with nothing changed and names the two other ways: set the new
value first with `paisans secrets set <path>`, or pass `--replaced <path>` for
one already replaced and applied. An answer equal to the value the hosts run is
refused as unchanged. Without a terminal the run is refused before anything
changes, with the steps that need none: replace it at its issuer, `paisans
secrets set <path> < file`, and run `secrets rotate` again. Where the issuer
keeps the old value valid beside the new one, revoke the old one there once
the rotation finishes; where it replaces the value in place, the apps that use
it fail to authenticate until stage 3 reaches them.

**A pasted secret already set anew is not asked for.** One replaced with
`paisans secrets set`, which some host does not run yet, has its next value in
the file already, and `secrets rotate` hands that value to every stack that
renders it, one site at a time.

**The DNS token is tried before any Caddy gets it.** Stage 2 looks up the
community domain's zone at the provider with the next token, the read only
call `dns init` makes, and a token that cannot see the zone stops the
rotation there, before stage 3. A pasted token is tried the same way as soon
as it is given, and one set with `secrets set` when the run is planned, so a
mistyped token is refused before it is written under `rotations`, where a
resumed run would keep it. A provider whose record management is not
implemented here (`dns init` lists those that are) is not checked, and the
plan says so.

**An SMTP password is tried before any app gets it.** A pasted one logs in to
the mail server, from the machine the command runs on, and sends the
operators one test message as soon as it is given, so a mistyped password, or
a sender the provider refuses, is refused before it is written under
`rotations`. One set with `secrets set` sent its message when it was set, and
logs in when the run is planned. Stage 2 logs in with it again from this
machine, then from every site that will be handed it, and no site is handed it
until every one accepts. A refusal at either point, or a site that cannot make
the try, stops the run there, before stage 3 and before any app is handed it.
A server this machine cannot reach is a warning, and the sites alone try it.
No other message is sent (*Mail settings are tried before they are used*).

**Which value a host runs is read per secret, not per file.** The lines that
carry the secret are the ones a render with any other value changes; the host
prints the SHA-256 of each line of its own copy of that file, so only hashes
cross ssh and nothing secret is on a command line; and the host runs the value
when every carrying line's hash is among them. A file that differs only
elsewhere, Eg: a comment an upgrade changed in `caddy.env`, or an app setting
not yet applied, would otherwise pass for a secret replaced, and its old value
be handed out again. A secret replaced and then applied, as a removal with
`--host-gone` does with the DNS token, reads the same as one never replaced, so
the operator says which: **`--replaced <path>`** names a pasted secret every
host using it already runs from the file. It is checked the same way, and
refused for a generated secret, for one some host does not run yet, and for one
whose rotation is under way; a path it accepts is listed in the plan as
replaced earlier and not rotated. No OIDC client secret is pasted: every one,
the homeserver's and the gate's included, is minted by `oidc client create
--rotate-secret`.

**A rotation that signs members out asks first.** When the plan includes one,
`--execute` asks the operator to type `sign out` at a terminal before any host
is reached, and without a terminal it is refused unless `--yes` types it. After
a gateway is cut off, since it saw every session cookie, signing members out is
the point (Founder decision).

**A secret every node must share cannot be held in two values at once.**
Garage's `rpc_secret` is the key of every node's RPC handshake, so a node on
the old value and one on the new cannot talk. Every Garage node is written the
new value and restarted together, with one node as much as with several, and
storage is unavailable until every node lists every other as healthy. `--all`
includes it (Founder decision).

**Some secrets are kept, and the plan lists each with why:**

* **Pocket ID's `encryption_key` and Matrix Authentication Service's
  `mas_encryption_secret`** encrypt data already stored. Changing either of
  them makes that data unreadable, and none of those projects offers a
  re-encryption the toolkit could drive. An operator who held one also holds a copy of the data it
  protects, so rotating it would take nothing back.
* **Wiki.js's `local_admin_password`.** Wiki.js keeps the break glass
  admin's password hashed in its own database once setup has run, and nothing
  changes it there, so a rotated secret would no longer sign in and the
  bootstrap could not configure the wiki (see *Wiki.js is configured by its
  bootstrap*). Its blueprint says so with `never_rotate`, and `secrets rotate`
  refuses it with that reason.
* **`cluster.admin_password`, where the admin role cannot log in.** Spilo
  creates it `NOLOGIN` at the Spilo 4 tags `render` pins, which run Patroni 4,
  so no login checks its password and a new one would change nothing. Where
  the role logs in, it is rotated like `standby`.
* **Mbin's `rabbitmq_password`, where the queue is not `rabbitmq`.** No broker
  runs, so no account has the password, and it is kept unused.
* **The account service's `email_hash_key`.** The service stores only the
  keyed hash of each declined address, so a new key matches none of them and
  rotating it forgets every declined address. `secrets rotate` refuses it by
  name and `--all` keeps it; only `paisans secrets set` changes it.
* **WireGuard keys**, which are `mesh rekey`'s (*Rekeying the mesh: `mesh
  rekey`*).
* **`backup.restic_password` and `backup.walg_libsodium_key`** encrypt every
  backup already taken, and a new one would not re-encrypt them: whoever held
  the old one can still read any copy they made. What keeps an operator who left
  out of the backups is the destinations' credentials.
* **Each backup destination's credentials**, which its provider issued. Issue a
  new key there, pipe it in with `paisans secrets set`, apply, then delete the
  old key at the provider.

**Refused before anything changes:** etcd or Patroni unhealthy, since a
password changed on a limping cluster can leave a member unable to rejoin; a
site that does not answer over ssh, since it would keep the old value and
break when the old one is retired; a pasted secret to be asked for with no
terminal to ask at, or whose answer is empty, unchanged, or a DNS token that
cannot see the zone; a pending rotation whose stage cannot be read from the
hosts, or that cannot finish; and a pending `mesh rekey`.

**The signed copies.** The applies a rotation runs hand each stack its next
value and nothing else: they neither ask for the review nor publish a version,
since nothing an operator declared changed. The rotated secrets file is a local
edit like any other, so the next `deployment apply --execute` publishes it, and `deployment
publish` can publish it sooner. A secrets merge refuses while either side has a
rotation under way (*Secrets are merged key by key*).

### A rotation that cannot finish

A rotation is pending until it is promoted, and while it is, `deployment apply` and every
other command that renders refuses (*`secrets rotate` replaces a secret where
it is checked and where it is used*). Almost every pending rotation is
finished by running `secrets rotate` again. A few cannot be: the secret's app
was taken out of `paisans-stack.yaml` after the rotation began, the cluster's admin
role turned out `NOLOGIN` after its rotation began, or a RabbitMQ password
begins with a dash, which `rabbitmqctl` reads as an option. Each refusal over
one of these names this section. Ending it is done by hand, in the secrets
file, because it settles which value is the secret's, and that has to be read
from the hosts first.

1. **Find which value the side that checks it accepts.** The current value is
   the one under the secret's own path; the next value is the one under
   `rotations.<path>`. Where the plan can read it, `paisans secrets rotate`
   says whether the checking side accepts the next value. For a Postgres
   role, log in with each; for RabbitMQ, `rabbitmqctl authenticate_user` with
   each inside the broker, though a next value that begins with a dash was
   never sent, so the broker still accepts the current one; for a secret its app checks itself, the value the
   app's running container was started with. A secret whose app is gone has no
   checking side left.
2. **Edit the file with `sops secrets.enc.yaml`**, which opens it decrypted in
   `$EDITOR` and encrypts it again to its recipients on save. Delete the entry
   `<path>` under `rotations`, and delete `<path>` from `rotate_remaining` when
   it is listed there. If the checking side accepts the next value, or some
   hosts already present it, copy it into the secret's own path in the same
   edit first, so the file names what the hosts check (for
   `apps.<app>.oauth`, each of the four keys the next value lists). If it
   accepts the current value, or nothing checks it any more, delete the entry
   alone.
3. **Run `paisans deployment apply`**, which now renders the value the file names to
   every host, and `paisans secrets rotate <path>` again when the secret
   still has to be replaced.

### Mail is declared once and overridden per app

An app that sends mail reads a deployment wide `smtp:` block: `host`, `port`,
`security` (`starttls`, `tls` or `none`), `username`, `from_address`, `from_name`. An
app may declare its own `smtp:` with any of those fields, and each one it names
replaces the deployment's while the rest are inherited, so a different sender
or a different account is one line rather than a copy of the whole block. The
password is a secret: `apps.<app>.smtp_password` when present, otherwise
`external.smtp_password`, which `init` lists as owed, and `secrets set` asks
for, only once an app resolves both a mail host and a `username`. A relay with
no `username`, such as a test deployment's Mailpit under `security: none`, is
sent to without logging in, so it takes no password: none is asked for, and
`secrets rotate --all` leaves out one that is set.

Four blueprints read it (`blueprints.SendsMail`): the account service, which takes it as
`ACCOUNT_SMTP_*` in its `.env` for its notices to community admins and applicants; the
uptime monitor, which puts it in its seed file; Pocket ID, which takes it as
`SMTP_HOST`, `SMTP_PORT`, `SMTP_TLS`, `SMTP_USER`, `SMTP_PASSWORD` and
`SMTP_FROM` in its `.env` (see *Pocket ID's configuration is rendered from
paisans-stack.yaml*); and Mbin, which takes it as `MAILER_DSN` and its sender address
(see *Mbin sends little mail, and says where from*). The password goes into
that `.env` beside the other credentials it already carries, written 0600 like
every rendered file that holds a secret, and quoted so that a `$` or a `#` a
mail provider issued arrives as issued. An `smtp:` block on any other kind is
refused, `smtp-on-a-kind-without-mail`, because an override nothing reads is
ignored without a word.

Two fields mean slightly less to Pocket ID than to the monitor, because of
what Pocket ID can express (Mbin's own differences are in its section):

* **`from_name` is not used.** Pocket ID has no sender name setting; its mail
  goes out under its application name (`email/module.go:212` at tag
  `v2.14.0`), which is `settings.app_name`. A `from_name` on the Pocket ID
  app's own `smtp:` block is therefore a warning,
  `pocket-id-smtp-from-name`, naming `app_name` instead. The deployment's
  `from_name` is not warned about, since the monitor uses it.
* **`from_address` must be an address alone.** Pocket ID refuses to start
  unless `SMTP_FROM` is an email address (`dto/app_config_dto.go:30`), so
  `validate` refuses anything else for it, `pocket-id-smtp-from-not-an-email`.
  A host with no sender at all starts and sends mail with an empty sender,
  which is a warning, `pocket-id-smtp-without-sender`.

Each consumer is given the mode in its own terms. Pocket ID takes it as
`SMTP_TLS` and the account service as `ACCOUNT_SMTP_TLS`, both spelled
`starttls`, `tls` or `none`. Mbin's DSN is `smtps://` for `tls`,
`smtp://...?require_tls=true` for `starttls` and `smtp://...?auto_tls=false`
for `none`. The monitor's `smtp_secure` is nodemailer's boolean `secure`,
`true` for `tls` alone; `false` upgrades with STARTTLS when the server offers
it and otherwise sends in plaintext. A missing port defaults to 465 for `tls`,
25 for `none` and 587 otherwise.

**`security: none` is plaintext, and is accepted only for a host on a private
network.** It exists for a relay the deployment runs itself, on the mesh or
beside the app as a compose service, which takes mail without TLS and
forwards it over TLS. On such a network the risk is accepted: anything that
can read the network's traffic can read the mail and the SMTP password, and
nothing else can. Every accepted `none` is a warning, `smtp-plaintext`, which
says so. The host must be one of these, in the deployment's `smtp:` block or
in an app's own after its overrides apply:

* an IP address in loopback, RFC 1918, RFC 6598 (`100.64.0.0/10`) or IPv6
  unique local space, or inside `mesh.subnet`;
* a name with no dot that starts with a letter: a compose service name.

Anything else is refused, `smtp-plaintext-public-host`. `validate` resolves
no names, because the answer at validate time says nothing about the answer
an app gets later, on another host, from another resolver; so a name with a
dot is refused even when it resolves to a private address, and the refusal
asks for the relay's mesh address instead. A service name must start with a
letter because a resolver may read a name of digits, or a hex one such as
`0x08080808`, as an IPv4 address.

Pocket ID and the account service will not send a username and password over
an unencrypted connection to any host but localhost (Go's `net/smtp`
`PlainAuth`, and the same check in Pocket ID's mailer, `emailer/smtp/smtp.go`
in `italypaleale/go-kit` v1.2.4 at Pocket ID v2.18.0; go-mail v0.8.1's
`smtp/auth_plain.go` in the account service at v0.2.0). For either, `none`
with a username is refused, `smtp-plaintext-with-username`: every mail would
fail at authentication. Point them at a relay that needs no username, or give
them `starttls` or `tls`. The monitor and Mbin send credentials over
plaintext. The monitor also upgrades with STARTTLS when a `none` relay offers
it, and then checks the relay's certificate, so a relay for `none` should not
offer STARTTLS with a certificate the monitor cannot verify.

### Mail settings are tried before they are used

A mail setting that is wrong, or a password the provider has revoked,
fails silently: nothing breaks at apply, and the first sign is a sign in link
or an account notice that never arrives. So the toolkit tries them (Founder
decision, 2026-10-14). **A check** takes an app's effective settings, the
`smtp:` blocks resolved as above, and its password (`apps.<app>.smtp_password`
when set, otherwise `external.smtp_password`), connects to `host` on its port,
does TLS from the first byte for `tls` or STARTTLS for `starttls`, verifying
the server's certificate for that host name against this machine's trust
store, and logs in with `AUTH` (PLAIN, or LOGIN where PLAIN is not offered).
A server whose certificate this machine does not trust for that name fails
the check, as it fails any client that verifies it. `none` stays plaintext,
which `validate` allows only toward a private relay. A relay with no
`username` takes no login, so its check connects and greets.

**It connects from a site that runs the app, through ssh.** The connection is
ssh's stdio forward from that site (`ssh -W <host>:<port>`, over the shared
connection of *One connection per host, shared*), so the site's host resolves
the name and its own route carries the connection: the check passes or fails
on the egress the app itself will use, and it reaches a relay only the mesh
or the site's private network can. The SMTP conversation runs on the
workstation, so nothing is installed or run on a host for it, and the password
crosses only the ssh connection and then the path the app's own mail takes. The
forward needs the host's sshd to allow TCP forwarding, which is OpenSSH's
default (`AllowTcpForwarding`, sshd_config(5)). Over the shared connection, a
host that refuses it and an address the host cannot connect to both reach the
check as ssh's `Session open refused by peer`, so the check says the site
could not open a connection to the server and names both causes.

**When, and how much:**

| Command | What it tries | On failure |
|---|---|---|
| `secrets set` an SMTP password | from this machine, logs in with the new password and sends one test message | refused; nothing is written. A server this machine cannot reach is a warning, and the password is written for `deployment apply` to try |
| `secrets rotate` an SMTP password | from this machine, a pasted one logs in and sends one test message when pasted; one set with `secrets set` logs in when the run is planned; stage 2 logs in again from this machine, then from every site that will be handed it, before any is | refused before anything is written under `rotations`, or stopped at stage 2 before any site is handed it; a server this machine cannot reach is a warning, and the sites alone try it |
| `deployment apply`, with or without `--site` | every app it would start logs in, from each site it runs on; nothing is sent | refused before any host is changed, naming the app, where its settings are declared, and the server's reply, or that the site could not open a connection to the server |
| `validate` | every app logs in, from each site it runs on; nothing is sent | a warning |

**A mail server on the deployment's own mesh is tried once the mesh is up.**
A `host` that is an address inside `mesh.subnet`, Eg: a relay run on one of
the sites, is reached only across the mesh: until `deployment apply` has
brought the mesh up, the address is on no interface and no site has a route
to it. So before each such check, apply and `validate` read whether the
deployment's WireGuard interface is up on the site the check is made from
and on the site that holds the address. While either is not, the check is
not made, with a warning that it cannot be tried before the mesh exists, and
the apply goes on: the first apply of a deployment whose mail server is on
its own mesh founds it, and the apply after it tries the server. A mail
server reached any other way is tried before anything changes, mesh or not,
and on a running deployment, whose mesh is up, a mesh server is tried the
same way.

A changed credential is the only thing that sends mail, because a login alone
cannot tell whether the provider accepts the sender: a provider may take the
login and still refuse a `from_address` the account does not own. `deployment apply` and
`validate` run often, against settings that already worked, so they only log
in. A password `secrets set` wrote has sent its message already, so a rotation
of it logs in and sends no second one.

Apps with the same settings and password share a check. `deployment apply` and `validate`
make one per site, since each site has its own route to the server; `secrets
set` and `secrets rotate` make one per sender, from the first site running one
of its apps, since what they test is the credential and each sender gets one
message.

**The test message goes to every operator** the configuration declares, by
their `email`, in the order of their names: a changed mail credential is every
operator's business, and each of them can say whether it arrived. It is plain
text, sent from the app's `from_address` (with its `from_name`), with the
subject `Paisans SMTP test: <path> for <domain>`, and it names the community,
the command, the apps, the server and the site it was sent from. The password
is not in it, and no output shows the password: each is registered as a
secret, and a failure quotes only the stage and the server's reply. With no
operator email to send to, `secrets set` and `secrets rotate` refuse the
password, since a message to nobody proves nothing.

**What is not tried, and why it is a warning.** A host that only the app's own
container resolves, a compose service name or a loopback address, is not one
the site's host reaches, so its settings are listed as not checked. So are
settings whose password is not set yet: `init` and `deployment apply` already name that
secret as owed. An SMTP password no app logs in with, or whose apps run on no
site, is written by `secrets set` with nothing to try.

**`validate` works away from the hosts.** It judges the file, and a mail
server is outside the file, so every mail failure is a warning there and
`deployment apply` is the gate that refuses. A site it cannot reach, or a secrets file it
cannot read, skips the check with a warning rather than failing; `--offline`
skips it without trying, for an editor's save hook or a machine with no route
to the sites.

### Rendered configuration is a build artifact

`paisans deployment apply` renders it. Nobody edits it, and `deployment apply` refuses to clobber a
rendered file that has local modifications until the difference is resolved.

```
WORKSTATION                        HOST                          CONTAINER
───────────                        ────                          ─────────
paisans-stack.yaml      ─┐
secrets.enc.yaml  ─┤ render
age key           ─┘   │
                       │ ssh push
                       ▼
                  /srv/paisans/<token>/<stack>/.env       (plaintext, 0600) ──► env vars
                  /srv/paisans/<token>/<stack>/config.ini (plaintext, 0600) ──► bind mount
                  /srv/paisans/<token>/<stack>/compose.yaml
                  caddy/, haproxy.cfg, patroni env
                                                       docker compose up -d
```

**`init` makes the age key of a deployment built from scratch**, one with no
secrets file and no `.sops.yaml` yet. It keeps the private key in the
workstation's own keystore, the macOS Keychain or the Secret Service
(`secret-tool`), as service `paisans-age` with the deployment id as the
account, and writes the public key to `.sops.yaml`. The key is never printed,
because a secret on a terminal is in a scrollback buffer and this one opens
every other. With no keystore, `init` refuses unless you name a file with
`--age-private-key-file <path>`, outside the directory that holds
`paisans-stack.yaml`, since that directory is usually a git repository.
`init` names the keystore item or file it kept the key in, without
`--verbose`, since the operator has to know where the only copy is; with a
file, it also prints the `export SOPS_AGE_KEY_FILE=` line later commands need.

**With a `SOPS_AGE_*` variable set, `init` uses that key instead of making
one**, because every later command decrypts with what the variable names and a
key made beside it would never be used. It reads the variable the way sops
does, writes the key's public half to `.sops.yaml`, keeps nothing, and says on
the step, without `--verbose`, which variable the key came from and its public
key (`age key: from SOPS_AGE_KEY_CMD, public key age1…`). An SSH key, from
`SOPS_AGE_SSH_PRIVATE_KEY_FILE` or `SOPS_AGE_SSH_PRIVATE_KEY_CMD`, becomes an
SSH recipient. sops reads every variable that is set and tries every key they
hold, so `init` does too, and refuses unless together they hold exactly one
key: `.sops.yaml` is written for one, and nothing says which of several is
the deployment's. To encrypt to several, write `.sops.yaml` yourself. A
variable that is set and gives no key is refused rather than answered with a
new key nobody chose, and so is `--age-private-key-file` beside one, since it
asks for a key the variable already names.

**The keystore, or the file you named, then holds the only copy.** Losing it
loses every secret in the file, so add a second recipient to `.sops.yaml`,
another operator's key or an offline backup key, and run `sops updatekeys
secrets.enc.yaml`. With a key from a variable, `init` cannot know whether
anything else holds it, and warns only that the file is encrypted to that one
key; the remedy is the same.

**`secrets.enc.yaml` exists only encrypted.** Every command that writes it
encrypts it to the recipients in `.sops.yaml`, and with `.sops.yaml` missing or
naming no recipient it refuses before it changes anything. For a file that is
already encrypted, the refusal names the age recipients its sops metadata
records. When `.sops.yaml` is missing, it also prints the command that writes
it back with them, which refuses to replace a `.sops.yaml` that exists:

```
sh -c "set -C; printf 'creation_rules:\n  - age: age1…\n' > /path/to/.sops.yaml"
```

When `.sops.yaml` is there but names no recipient Paisans reads (the `age`
field of each entry under `creation_rules`), it prints no command, so nothing
pasted can replace your rules. A recipient in the file's metadata that is not
an age public key is never printed, and then no command is either.

A plaintext `secrets.enc.yaml` is refused wherever a deployment reads it:
`init`, `deployment apply`, `validate`, publishing a version, pulling or recovering one,
and every other command that opens the secrets. `doctor` reports it, and a
missing `.sops.yaml`, as a failed check and runs the rest. The refusal names
the fix, `sops --encrypt --in-place secrets.enc.yaml` with your age public key
in `.sops.yaml`.

Every later command finds the key on its own: with no `SOPS_AGE_*` variable
set, paisans reads the keystore item for the deployment id and decrypts with
it in memory. When the item is missing, the keystore cannot be read, or the
item's key is not a recipient, sops' own search still runs. A variable you
set always wins, and a command that decrypts with one says so at every
verbosity when the keystore holds the deployment's key and the variable does
not name it, because the key you saved is then not the one in use.
`SOPS_AGE_KEY_CMD`, a command that prints the key, keeps it out of files and
the shell's environment, and is what `sops` itself needs when you run it by
hand; `init --verbose` prints the line for the keystore item, and a variable
set to it is not reported, since it names the saved key.
`SOPS_AGE_KEY_FILE` (the key's path), `SOPS_AGE_KEY` (the key itself) and
`sops/age/keys.txt` in your config directory are read too.

`sops` and `age` are installed on the workstation only. Hosts do not have them.
Containers never know they exist, and nothing is written *into* a container —
Compose passes env at start. Changing a secret in an `.env` therefore means
re-render plus `compose up -d` to **recreate**; a `restart` will not pick it up.
A secret in a bind-mounted file is cheaper to change, for the reason below.

#### A file `deployment apply` no longer renders is left over, and stays recorded

Taking an app out of `paisans-stack.yaml`, or moving it to another site, leaves its
files and containers on the host. **`deployment apply` never deletes them**, because
stopping a stack and removing its data is a decision with member data in it,
and an apply that only meant to change a configuration must not make it.

**It keeps them recorded.** The manifest is the only proof that a file under
the root is this deployment's, so a whole apply keeps the entry of every file
it no longer renders, marked `"leftover": true` with `"leftover_since"`, the
time a whole apply first found it so. The file stays this deployment's rather
than turning into one nobody can account for, and a file rendered again loses
the mark. A scoped apply, and one run with `--only`, marks nothing and keeps
every entry outside what it writes exactly as it was.

The dry run and the real one end with a `left over` section listing them:
each such file, and each of this deployment's compose projects still on the
host (by the deployment label with this id and a `paisans-<token>-<stack>`
project) that the site no longer renders, saying that `deployment apply` leaves them in
place. `doctor` reports the same, as a warning (see *`paisans doctor` reports
what is stuck and how to recover*). Both name `paisans app remove <app>` for
each app the leftovers belong to, which is the command that takes them off
(see *`app remove` takes an app off its hosts*).

#### A blueprint can come from a catalog

The blueprints built into the toolkit are the reserved four (`pocket-id`, the
gate, `account`, `uptime`). The first party ones, `mbin`, `wikijs` and the
rest, are the **core catalog**'s, which the toolkit fetches at its newest
release this toolkit reads (see *Images follow tracks*). Any other comes from a **catalog**: a git repository of blueprints the deployment
declares and pins to a commit, and an app names it qualified:

```yaml
catalogs:
  example/blueprints:
    url: https://github.com/example/paisans-blueprints
    tag: v1.0.0
    commit: 4f2a0c0e9b1d7a3f5e6c8b2d4a6f8e0c1b3d5f7a
apps:
  notes:
    blueprint: example/blueprints/journal
    hostname: notes.example.org
    placement: { pinned: vm }
```

**A catalog's repository is named `paisans-<name>`**, as a Homebrew tap's is
named `homebrew-<name>`, and the catalog is named `<owner>/<name>` with the
prefix left out: `example/paisans-blueprints` is the catalog
`example/blueprints`, and `paisans catalog add example/blueprints` reads it
from GitHub. The name says where a blueprint comes from wherever it is read,
and two authors' catalogs cannot share one. A catalog may be given a name of
its own instead; its repository is still `paisans-<name>`.

**An unqualified name is never a catalog's.** `mbin` is always the core
catalog's, and a reserved name is refused from every catalog, so no catalog
can stand in for the identity provider. **The pin is a commit**, because a
deployment must render the same bytes for every operator and on every run; a
tag beside it must still name that commit, so a tag moved after the pin was
written is refused. Loading the configuration fetches each catalog an app
names once, with the `git` binary and the operator's own git credentials,
into a cache keyed by the commit, one per machine. A fetch takes the commit's
trees and the files of only the blueprints in use, so a catalog of many
blueprints costs a deployment only the ones it runs.

**A catalog is trusted by its pin, not by who published it.** Nothing checks
a release's author: a pin is a commit the operator chose, and moving it is
reviewed. `catalog add` and `catalog update` show the release they read and,
for each app, what it changes, and the grant review asks for approval before
a moved pin renders on a site (see *`deployment apply` and `site prepare` review what
`paisans-stack.yaml` grants*). The
core catalog may be pinned to a release of its own or a mirror, like any other;
unpinned, it moves to each new release it can read, and the grant review shows
that move as it shows any moved pin.
The sites never fetch a catalog: they trust the operator's signature on the
configuration version that carries the blueprints (see *Every site keeps
signed copies of the configuration*).

**A catalog blueprint brings rules of its own, and is held to what it leaves
out.** Its `checks` are CEL conditions over the app's declaration and the
sites it runs on, each run by `validate` under the blueprint's own rule id as a
refusal or a warning, with a message that may name the value it refuses; its
`settings` are checked by type (string, bool, int, email, enum, or `list`,
a list of names, for a setting such as a set of groups), and a key it does not
read is warned about. A
setting may declare a `default` of its own type, which the blueprint's
templates and CEL expressions see for an app that leaves the key out; a value
the app sets always wins. The default is never written into the
configuration, only into what that blueprint reads, because the built in
blueprints give a setting that is absent a meaning of its own (an absent
`authorized_fetch` is on), and filling defaults everywhere would change those
answers silently. `blueprint lint` fails a template whose literal `.Setting`
or `.SettingBool` fallback differs from the declared default, so a default is
stated once. A
blueprint with no health route is still watched, by a TCP connect to its port
on each site and its front page at the public hostname answering anything
below 500, with a warning (`blueprint-without-health`) since an app up and
broken goes unnoticed; `health: {tcp: true}` declares an app that serves no
HTTP. A blueprint keeping a database the backups cannot read as files (MySQL)
must say how to dump and restore it, or a deployment with backups refuses it
(`blueprint-without-backup`). A gated app whose blueprint says the gate leaves
something open past it, and enforces no setting to close it, is warned about
(`gated-leaves-open`).

**A catalog blueprint may declare a heartbeat**, for an app that knows
something about itself no check from outside can ask, Eg: whether its
settings still hold what the configuration says:

```yaml
apiVersion: paisans.blueprint/v2
heartbeat:
  name: "settings"   # the monitor is named "<app> — settings"
```

Each app of it gets a token, `apps.<app>.heartbeat_token`, generated at
`init` (and by `deployment apply`'s dry run, with the other generated secrets) in the
site heartbeat token's shape, and refused by `render` when it is missing or of
any other shape, monitor or not, so a monitor added later never meets an app
without one. Every monitor's seed gets one push monitor per app,
`<app> — <name>`, with the site heartbeat's interval, grace and threshold. The
blueprint's templates read `.HeartbeatURLs`, every monitor's
`https://<monitor hostname>/ping/<token>`, space separated, in app order, and
empty in a deployment with no monitor, and hand it to a service of the
blueprint's own that pushes. The URLs carry the token, so the template holding
them is a `.secret.tmpl`. Every instance pushes to the one token and to every
monitor, the one on its own site included, as the operators heartbeat does
(see *The operators heartbeat is a service beside the reconciler*), so a
cluster placed app keeps one history whichever instance pushes. `heartbeat` is
read only from `apiVersion: paisans.blueprint/v2`, which is
`paisans.blueprint/v1` with that one key: a toolkit that reads only v1 refuses
the format by name and uses the newest catalog release it can read, rather
than rendering the app without its monitor. `secrets rotate` replaces the
token, by name or with `--all` (*A heartbeat token reaches the monitors before
the pusher*).

#### A blueprint ships a template set, not a single `.env`

Not every application is configured through environment variables, and the
stacks named in this document are already split on it. Mbin reads `.env`.
WriteFreely reads an INI file, `config.ini`, whose `[server]`, `[app]`,
`[database]`, `[storage]` and `[oauth.generic]` sections carry everything it
needs, SSO client credentials and `disable_password_auth` included. The
`[database]` and `[storage]` sections are the writefreely-wisp fork's own,
read from its `config/config.go` and `config/storage.go` rather than from
upstream's [config reference](https://writefreely.org/docs/latest/admin/config),
which documents neither. Synapse reads
[`homeserver.yaml`](https://element-hq.github.io/synapse/latest/usage/configuration/homeserver_sample_config.html).

So the unit the toolkit ships per application is a **blueprint**: a directory
holding a `blueprint.yaml`, which records what the toolkit knows about the
application, and the templates `deployment apply` renders, every one of them
(`docs/specs/2026-10-12-blueprints-and-catalogs.md`). An app names its
blueprint with `blueprint:`; elsewhere in this document and in the code
(`internal/kinds`, `config.Kind`) the same thing is called the app's kind.

```
blueprints/mbin/                            blueprints/writefreely/
  blueprint.yaml                              blueprint.yaml
  compose.yaml.tmpl                           compose.yaml.tmpl
  .env.secret.tmpl                            config.ini.secret.tmpl
  caddy.snippet.tmpl                          .env.secret.tmpl
  caddy.snippet.media.tmpl                    caddy.snippet.tmpl
  valkey.conf.tmpl                            caddy.snippet.media.tmpl
  oauth/private.pem.tmpl
  oauth/public.pem.tmpl
```

**A template's path is its destination.** The layout of a blueprint's templates
mirrors the layout under `/srv/paisans/<token>/<stack>/` on the host, so where a rendered file
lands is read off the tree rather than held in a mapping somewhere else. A file
the application expects at a path inside its own image is bind-mounted there by
that kind's `compose.yaml`. The name carries the rest: `.secret` before `.tmpl`
is dropped from the rendered name and makes the file 0600, every other file is
0644 so the container's own user can read it, and a `caddy.snippet.*` template
is rendered onto the gateway under `infra/caddy/etc/snippets/`, because that is
where Caddy reads it.

Nothing in the secrets path is env-specific: every rule above holds file by
file, whatever the format. A rendered `config.ini` is a
build artifact, is written 0600, receives its secrets at render time on the
workstation, and is refused a clobber when locally edited, on exactly the same
terms as a rendered `.env`.

One consequence runs against the intuition and is worth stating plainly.
Environment variables are readable through `docker inspect` and
`/proc/<pid>/environ`, as the limits below say. A bind-mounted file at 0600 is
not. **File-configured applications are the better case here, not the awkward
one**, and where an application accepts either, the file is preferred.

Reload differs as well. A changed `.env` needs `compose up -d` to recreate the
container, because Compose passes environment only at start. A changed
bind-mounted file needs a restart at most, and the container keeps its identity.
`deployment apply` takes the narrower action rather than recreating a stack it does not
have to, since a recreate is an outage however brief.

#### The template set owns everything app-specific, routing and shims included

The template directory is not just the files an application reads at startup. It
is **everything the deployment needs that is knowledge about that application**,
and two categories are easy to leave out.

**Its Caddy snippet.** Routing is rarely uniform across a stack. Mbin's
documentation advises a media reverse proxy so URLs survive a provider change
(rule 3). A Matrix homeserver needs its `.well-known` responses and federation
listener. An auth gate has to be told which callback paths to leave alone or it
breaks the sign-in it exists to protect. Each of those is a fact about one
application, so it lives beside that application's other templates and arrives
and leaves with it.

**Its shims.** Where upstream ships a file the deployment has to override, the
override is a template like any other. Rule 3 names the first candidate, and it
turned out to be the cautionary case. Mbin's
`config/packages/oneup_flysystem.yaml` defines both a local adapter and an S3
adapter, and binds uploads to the local one:

```yaml
  filesystems:
    public_uploads_filesystem:
      adapter: default_adapter
      #adapter: kbin.s3_adapter
```

On a bare metal or VM install, setting the `S3_*` variables alone appears to
work and silently keeps writing to local disk, and the fix is editing this file.
[Mbin's S3 documentation](https://docs.joinmbin.org/admin/optional-features/s3_storage/)
says that edit is "only needed on bare metal/VM, not Docker", because the Docker
image's entrypoint makes it: with `APP_ENV=prod` (the image's default) and
`S3_KEY` set, it runs `sed -i` on that file at every start, switching the
adapter to `kbin.s3_adapter`. On the image this toolkit runs, the variables in
`.env` are the whole switch, and the toolkit renders no override.

A rendered copy bind-mounted over the file was the first design, and it is worse
than none: `sed -i` cannot replace a bind-mounted file, the entrypoint runs
under `set -e`, and the container exits before it serves anything. So **before
shimming a file, read the image's entrypoint.** A file it edits in place cannot
be mounted over, and a file it edits is usually one the image already handles.
Entrypoint wrappers, module configuration and other dropped-in override files
that the image leaves alone are templates like any other.

**A shim is a rendered, readable file in the template directory, never a private
image build.** Baking one into an image moves it somewhere an operator cannot
read during an incident, and quietly makes the `images` reference above a lie
about what is running.

A shim is also a deliberate divergence from a file that upstream owns, and
upstream can change that file underneath it. There is no way to remove that
cost, so make it visible instead: **a shim template names the upstream path it
overrides and what it changes**, in a comment at the top, and a changed image
reference is the thing to go looking at when a stack starts behaving as though
the override is not there.

Per-app snippets keep two costs bounded. Adding an application is a snippet
of its own that no other application shares, so the cost of adding one does
not grow with how many are already there. And a mistake in one application's
routing belongs to one host block.

That leaves the gateway's own template holding only what is genuinely
cross-cutting: TLS and the DNS-01 configuration, the trusted proxy setting
(fixed to the mesh subnet, not configurable), the auth gate, and one host block
per app that includes that app's snippet. A snippet never hardcodes where its
application runs; it receives the location from the inventory at render, which
is what keeps a pinned app and a cluster app the same shape.

One consequence follows from assembling a shared file out of per-app parts, and
it takes a gate: **`deployment apply` validates the assembled gateway configuration before
reloading it, and refuses to reload one that does not validate.** A rendered
snippet that is wrong should cost the operator an error message on the
workstation, not the public address of every application at once.

**A routing change gets exactly one action on Caddy.** When only the Caddyfile
or its snippets changed, that action is a reload, which drops no connection;
the infrastructure stack is not acted on at all, because a restart of Caddy on
top of the reload would be a second action for one change, and the one that
drops connections. When Caddy's compose file, environment or image also
changed, the stack's `up -d` is the action, and the reload waits for it: a
Caddy `up -d` replaced started on the new routing and is not reloaded, and one
`up -d` left in place (because only another service's configuration changed)
is reloaded afterwards, told apart by its container ID. A stopped Caddy has
nothing to reload and is started instead. The reload comes second so that it is
never spent on a container about to be replaced.

**Caddy mounts its configuration as one directory, never a file in it.** The
Caddyfile and the snippets are rendered into `infra/caddy/etc/`, which the
container mounts read only at `/etc/caddy`. `deployment apply` replaces a file
by renaming a new one over it, and a bind mount of a single file keeps the
inode it started with (moby/moby#15793), so a Caddy that mounted its Caddyfile
on its own reloaded the file it was created with: the reload reported "config
is unchanged", and a hostname added since had no site block, so its TLS
handshake failed. A directory mount resolves each name when it is opened, so
the reload reads what the apply wrote. Only the Caddyfile and snippets are in
that directory, because the container sees all of it: `caddy.env`, `data` and
`config` stay beside it. A gateway applied before the move keeps its old
`infra/caddy/Caddyfile` and `infra/caddy/snippets/` as left over files, which
nothing mounts any more: the first apply after it replaces the Caddy, since its
compose file changed, and the files are the operator's to delete.

#### An app may answer on more than one hostname

```yaml
apps:
  chat:
    blueprint: synapse
    hostname: matrix.example.org
    hostnames:
      wellknown: example.org
```

Each hostname gets its own host block and its own snippet, because a second
hostname almost always exists to serve something *different*. A homeserver is
the worked example: the API lives on one name, and the `.well-known`
delegation documents live on whatever name appears in user identifiers. Routing
them identically would publish the entire API on the apex.

The key is a **role**, not a label: it selects which snippet the kind ships. A
role a kind does not understand is refused, so one missing import can never
stop the gateway loading its whole configuration.

**A hostname may be claimed once, by one app and one role.** Two claims on one
name are refused. Caddy will not choose between two site blocks that hold the
same address, and it refuses the whole file rather than the block, so a name
typed twice takes every hostname in the deployment down rather than the one
that was duplicated. This is easiest to hit because an app can declare
several: setting one app's `hostname` to the value another app already uses for
a role reads as two unrelated lines.

**For a homeserver, `hostnames.wellknown` is not only routing. It is
`server_name`.** It becomes the part after the colon in every user identifier
the homeserver ever mints, and it is written into every room that homeserver
has joined. It cannot be changed once an account or a room exists: a homeserver
restored under a different `server_name` is, to its own history, a different
server. Editing this key on a deployment that is already running is a rebuild
and a migration of member data, not a configuration change, and nothing in this
toolkit can undo it for you or warn you at the moment you do it, because
`deployment apply` compares the configuration against templates rather than against what
some earlier `deployment apply` already put on a host.

Choose it once, before the first `deployment apply`, and choose the name you want members
to have. Declaring no `wellknown` hostname is a legitimate choice with the same
weight: `server_name` is then the app's own hostname, the homeserver serves its
own delegation documents there, and that name is equally permanent.

#### The visibility gate is a per-app property

```yaml
apps:
  talk:
    blueprint: mbin
    hostname: talk.example.org
    gated: true
    auto_login: true
  blog:
    blueprint: writefreely
    hostname: blog.example.org
    auto_login: true
  chat:
    blueprint: synapse
    hostname: matrix.example.org
    gated: false
```

Two booleans, both declared on the app rather than implied by which Caddy
snippet somebody remembered to import, because they are access policy, and
access policy belongs in the configuration, not in a hand edited include.

| `gated` | `auto_login` | Someone with no session | A member the gate has signed in |
|---|---|---|---|
| `false` | `false` | reads the app as it is | reads the app as it is |
| `true` | `false` | is sent to sign in | is let through, and meets the app's own sign-in button |
| `false` | `true` | reads the app as it is; its sign-in page signs in at the gate first | is signed straight into the app |
| `true` | `true` | is sent to sign in | is let through and signed straight into the app |

`gated: true` admits members, and admins, who can see everything a member can.
Both default to `false`.

**A gated hostname gates every request except the ones that carry their own
authentication, which the app verifies.** Gating only browser navigations
would leave every page readable to a client that did not ask for HTML. So the
gate covers everything, and four classes go to the app without it:

| Class | Matched by | Who authenticates it |
|---|---|---|
| ActivityPub read | a `GET` whose `Accept` names `application/activity+json` or `application/ld+json` | the app, by signed fetch against its allow list; Caddy passes back only an ActivityPub, JSON-LD or JRD answer, and turns any other success into an empty 404 |
| ActivityPub delivery | a `POST` whose `Content-Type` names either, on the kind's inboxes only | the app, which verifies the delivery's signature |
| Open paths | the kind's open paths: its OIDC callback, a native app's OAuth chain, federation discovery, the assets those pages load, a dedicated health route | nothing; they hold no private content |
| Token paths | the kind's token paths **and** `Authorization: Bearer` | the app, which refuses a token it did not issue |

Everything else is redirected to sign in when there is no session, `curl`
included, and refused with the gate's own 403 when the session belongs to
someone who is not a member. Every hostname, gated or not, strips
`X-Auth-Request-*` from the inbound request before anything else sees it:
those headers are only ever the gate's verified answer, and an app that
trusted one on an ungated hostname would be trusting whoever sent the
request. Which paths a kind needs open is knowledge
about that application, so it lives in `internal/kinds` beside the rest, read
off the pinned tag's source with a citation for each. A token path is recorded
only once the kind's source shows a token that reads content cannot be had
without a member's sign-in; until then a native app's API calls meet the gate.
Mbin's `/api/*` is one, because a gated Mbin runs as a private instance,
which refuses any API request without a signed in user, so its mobile apps
work behind the gate.

**Anyone can send an ActivityPub header, so a gated app that federates must
refuse an unsigned read.** Caddy cannot verify an HTTP signature without a
plugin, and the gateway runs no plugin whose failure to build would stop the
identity provider loading, so the app is where the check lives. That is
authorized fetch, on by default for Mbin and WriteFreely (see *Mbin and
WriteFreely lock what the toolkit renders*). A gated federating app that would
not refuse an unsigned read is refused (`gated-without-signed-fetch`):
one whose kind records no way to, one with `settings.authorized_fetch: false`,
and a WriteFreely with `private: false`, since only private mode makes a blog
read from its allowlist alone. The monitor proves the setting is still on (see
*Monitoring*). Reading only from instances on an allow list is a federation
policy choice, and it is the founder's.

**Auto-login is declared on the app and described by the kind.** A member the
gate has signed in already holds a session at the identity provider, so the
app's own "Log in with" button is one pointless click. Where a kind records its
sign-in route and the cookies that mean "already signed in", a request for its
front page or its sign-in page, from a browser holding the gate's session
cookie, is checked with the gate and, for a member, sent straight into that
route. Someone the gate refuses is sent back to the page they asked for and
sees the app as they would have. Either way it happens once every two minutes
per browser, so a member the app's own client refuses is not redirected
forever. It never fires on a deep link: neither Mbin's nor WriteFreely's
sign-in route takes a destination, and a deep link sent through one would land
on the front page. `auto_login: true` on a kind with no such route is refused
(`auto-login-without-a-rule`), because it would do nothing. Wiki.js's rule
covers `/` and `/login` and nothing else, because Wiki.js does the rest
itself: any other page answers a guest with its refusal page and a Login
button, and keeps the page asked for in its `loginRedirect` cookie, so a
member following a deep link clicks once and lands where the link pointed
(observed, `docs/specs/2026-10-13-wikijs-blueprint-findings.md`). Founder
decision: the one click stays.

**On a public app, the sign-in page signs in at the gate.** Nobody passes the
gate on a public app, so a visitor there would only hold the gate's session if
they had signed in somewhere gated. Instead, a visit to the app's sign-in page
with no gate session goes through the gate's sign-in and comes back holding
one, so auto-login fires: the app's own "Log in" link becomes the community's
sign-in, and one sign-in covers every app. Nothing else on a public app meets
the gate. While the gate is down, a browser holding its session meets the
edge's error on a public app's front page and sign-in page, the two paths
auto-login checks; every other page, and every visitor without that session,
reads the app as before.

**The gate is enforced at the gateway, and only there. An app's published host
port is not behind it.** The gate renders as `forward_auth` inside the
gateway's Caddy site block, so it covers requests that arrive through the
gateway's hostname. It does not cover the port the app's own compose file
publishes: `talk` above is `gated: true`, and its stack publishes port 8080,
so anything that can open that port reaches Mbin with no gate in front of it at
all. The same is true of every gated app.

**So every published port binds the site's mesh address and nothing else**
(Eg: `"10.44.0.1:8080:8080"`, never `"8080:8080"`). The gateway reaches every
app over the mesh, so the mesh is the only interface a port needs, and the
members of the mesh are the hosts this deployment already trusts. The bind
address is the control, so that nothing depends on the firewall: a rootless
daemon's published port is a listener of the user's port forwarder, which ufw's
rules cover, but a port published on all interfaces of a host with a public
address would be there for anything those rules admit. A cloud VM holding the
apps role is exactly that host. The one port published
elsewhere is the uptime app's on a monitor with `managed_by: external`, the one
app that setting allows, which binds the site's `listen` for the operator's own
web server, under the rules in *The monitor has a site of its own*; nothing
that kind serves can be gated.

What remains the operator's is the mesh itself. A port on the mesh address is
reachable from every mesh peer, so a compromised peer reaches the app ungated.
`gated: true` is not protection against that, and nothing in this file
claims it is. It also means the mesh interface has to be up before the app stacks start,
because Docker cannot publish on an address the host does not have yet; that
is the ordering that step 4 under *`init`, one site, no mesh* already requires.

**Two things on one site binding the same port is refused (`port-collision`).**
Infrastructure runs with host networking and every app publishes on its site's
mesh address, so on one site they all share one set of ports, and whichever
starts second fails only when its container does. That is how it was found on
a real data and apps site: Spilo's bg_mon took 8080 before Mbin could publish
there. `validate` lists what each site binds (WireGuard's 51820/udp, etcd's
2379 and 2380, Postgres, the Patroni API and bg_mon on a data site, HAProxy's
cluster and stats ports where it runs and its Pocket ID API port, 1413, where
it runs beside a pocket-id app, Garage's 3900 to 3903, Caddy's 80 and
443 on every address of the gateway and of a monitor in `managed_by` `paisans`,
and every app's published ports, counting a clustered app on every apps site,
a pinned one on its own, and one on a monitor in `managed_by` `external` at
its `listen`) and refuses
any two that overlap, naming both and the port. The list is read from the same
constants the rendered files use, so the check cannot drift from what lands on
a host. It is a refusal rather than a warning because the configuration cannot
work as written. The fix is the operator's: pin one of the two elsewhere, or
move the role that brings the other. Ports are fixed because a port that
depended on what else is on the site would change when an app is added, and
the gateway's routes with it.
Some combinations are refused by this today, and a homeserver pinned to a data
site is the one worth knowing: Synapse's 8008 is the Patroni API's, and MAS's
8009 is bg_mon's.

**Leaving `gated` out of an app's stanza means the same thing as
`gated: false`: ungated, reachable by anyone who can resolve the hostname.**
That default has to be stated here, not only in a doc comment, because it is
the one setting where forgetting it is indistinguishable from choosing it: an
app with no `gated` key renders exactly like an app that explicitly opted out,
and nothing at render time flags the absence as a decision skipped rather than
made.

**A gate on a kind that cannot sit behind one is refused**
(`gated-on-ungateable-kind`), which is the rule below enforced rather
than merely stated. The case that makes this load bearing is a negative one. A
Matrix hostname must never be gated: a Matrix client is not a browser and will
not follow a redirect to a passkey prompt, so gating a homeserver's API or its
`.well-known` apex breaks federation and every client's login, not just one
member's. Pocket ID is the sign-in flow, so gating it gates the login. The monitor is what an operator opens when the sites it watches
are down, and the gate signs in at the identity provider on those sites, so a
gated monitor is unreadable at exactly the moment it exists for, and its break
glass password sign-in (see *Monitoring*) would sit behind the sign-in
it is the fallback for; its own OIDC client already admits only
`settings.admin_group`, so the gate would add no access control, only a
dependency. Leaving gating to a hand edited include means that failure
shows up as clients mysteriously unable to log in, with nothing in the
configuration saying why. A declared `gated: false` makes the absence of a
gate a fact about the app that a reviewer can see, rather than an inference
from what nobody wrote.

**Nobody declares the gate.** When any app sets `gated` or `auto_login`, the
toolkit runs it as an app named `gate`, of blueprint `oauth2-proxy`, at
`gate.<community.domain>`, pinned to the gateway site. It holds nothing
durable, so it sits with the edge it serves: it is down only when the gateway
is, and that takes every app with it anyway. It is two containers: the gate,
admitting the `members` group, which fails closed, so a session outside the
group is refused, never waved through; and a Valkey beside it holding the
sessions, in memory only. The session cookie carries only a ticket naming a
session in that store, because a refresh must never have to change the
cookie: the gateway's `forward_auth` sends none of the gate's response headers
to the browser, so a changed cookie would never arrive and the next request
would spend a refresh token the identity provider had already replaced. A
session lost with the store costs its member one pass through sign in at an
identity provider that still knows them. Everything an app gets, it gets too:
a client at the identity provider, generated secrets, a DNS record from
`dns init` and a check on the monitor. Its session cookie is scoped to the
community's domain with a leading dot, so one sign-in covers every app.
Four things follow, and each is refused:

* **Declaring an `oauth2-proxy` app.** The gate is the toolkit's; the apps it
  was for set `gated` or `auto_login` instead.
* **An app named `gate`, or no gateway site**, while an app uses the gate,
  since the gate needs both the name and the gateway.
* **A gated or auto-login app on a hostname outside the community's domain**
  (`uses-gate-outside-the-domain`). The browser never sends the gate's cookie
  there, so a gated app would send every member back to sign in forever and
  auto-login would never fire.
* **An app using the gate whose member group is not `members`**
  (`oidc-member-group-disagrees-with-gate`). The gate admits one group and the
  app's client the other, so they would disagree about who a member is.

#### The settings gating needs are checked on both sides

The gate leaves ActivityPub, and for Mbin a token holder's API calls, to the
app, so some of the app's own settings are part of the gate: Mbin's private
instance mode, allow list and authorized fetch, WriteFreely's private mode and
authorized fetch. Each kind records which ones it needs and when, in
`internal/kinds` beside its gate record, and how to read them from a running
copy. Every one is rendered into the file the app reads and named in that
file's lock list, so the app reads it from disk and nothing inside the app can
change it. Nothing the toolkit does writes an app's database.

* **`validate`** refuses a `config:` key the stack already writes
  (`config-key-already-rendered`), the same rule render applies, so a key that
  gating has made redundant or contradictory is refused before `deployment apply` touches
  a host. Whether a template writes a key can depend on the rest of the file,
  so `validate` renders the deployment to find out, with secrets it generates
  in memory and throws away: which keys a template writes never depends on a
  secret's value.
* **`doctor`** reads each running copy and fails when one of these settings is
  off or not locked; see *`paisans doctor` reports what is stuck and how to
  recover*. `doctor --execute` removes a `config:` key that only repeats what
  gating renders.

See `docs/specs/2026-10-12-gate-settings.md`.

### `storage init` provisions object storage

`deployment apply` renders files and reconciles them against a manifest it wrote; that is
what makes its refusal to clobber a locally edited file trustworthy. Garage's
cluster layout, each app's S3 key, and each app's bucket are not files. They
are state inside a running service that no manifest describes, so they are a
separate command: `paisans storage init --site <name>`.

It checks what a site's Garage node already has and creates only what is
missing, so running it again is safe. An app with `storage: local` keeps its
files on its own disk and gets neither key nor bucket (see *A pinned app may
keep its files on its own disk*). It assigns the cluster layout only when
the site is the one Garage site; joining several is `storage add`, below. Like `deployment apply`, it prints a plan and
writes nothing without `--execute`.

The plan marks its lines the way `deployment apply`'s dry run does: `○` and the step's
title (`assign layout role`, `deployment apply layout`, `import key for <app>`, `create bucket for
<app>`, `grant key for <app>`, `allow website for <app>`) for what is still
missing, its full sentence under it with `-v`; `✓` for what Garage already
has, shown with `-v`. Piped output says `todo` and `ok` instead.

It has to run after the infrastructure stack is up, because Garage has to be
reachable to be asked what it already has. **`paisans deployment apply --site` alone
leaves object storage unusable**: the containers come up, but no bucket exists and no
application key can reach one, so the failure an adopter meets is an
application error with no obvious cause, not a message naming a missing step.
`storage init` is that missing step, and `paisans deployment apply` without `--site` runs
it once every site is up, or `storage add` in its place when
`storage.garage.sites` lists more than one site.

#### More than one Garage site: `storage add`

**One Garage site needs nothing extra.** Run `paisans storage init --site
<name> --execute` after the infrastructure stack is up and it does the whole
job, layout included.

**Several Garage sites are joined by `paisans storage add`**, and by nothing
done by hand. A server is changed only by the toolkit. `storage init` lays a
node out only when it is the one Garage site; with several it refuses a node
that has no role and names `storage add`, because assigning one node alone
either fails (`layout apply` refuses fewer nodes than the replication factor)
or, at replication 1, starts a second cluster that never meets the first.

```sh
paisans deployment apply --site home-b       # writes garage.toml, starts an isolated node
paisans storage add               # dry run: every stage, read from live state
paisans storage add --execute     # add --change-replication if the factor changes
```

It is cluster-wide and takes no `--site`. A layout at replication 3 cannot
grow one node at a time, since Garage refuses a layout with fewer storage
nodes than the factor, and another factor is a whole-cluster stop; a per-site
command would have to refuse half its invocations. It reads every node and
plans only what differs, in gated stages: the nodes as they are, the
replication reset when one is needed, connect, one layout version with each
node in a zone named after its site (only distinct zones spread copies across
sites), sync, provisioning planned by `storage init`'s own planner and found
present, the gateway's media routes, a smoke test that writes a probe and
reads it back through every node and through the app's own media hostname,
and last the monitor's reseed, so that a site that exists for Garage alone
gets its ping (see *Topology commands reseed the monitor*). The design, with
Garage v1.0.1's source behind each gate, is
`docs/specs/2026-10-07-multisite-garage.md`.

**It does not wait for Garage.** Copying data to a new node runs at the speed
of the slowest site's upload, which on a home line can be hours. A stage
waiting on Garage exits with status 75, "try again later", and the next run
reads live state and carries on; `--wait <duration>` polls instead. Everything
a resumed run needs is on the hosts, so any machine can resume it, and an
operator's terminal is never held hostage to a residential uplink.

**`storage.garage.sites` is a preference order, not a set.** The first site
serves every media read the gateway passes on, for every app's media
hostname, and takes every app's writes
(`S3_ENDPOINT`); the others serve only when the ones before them are down. A
Garage node that receives a write sends the other copies itself, and a home
line's upload is its slow direction, so the site with the best upload goes
first. A new site joins at the end of the list and moves up only after
`storage add` has passed, because a node with no role answers every bucket as
missing; `storage add` refuses the other order. Media failover is
passive: `lb_policy first`, `lb_try_duration` so the request that finds a
node dead is retried on the next, and `unhealthy_status 503` for a node that
has lost quorum.

**What one site down costs depends on replication and consistency.** Garage
v1.0.1's quorums, each observed on real containers as well as read from its
source:

| Layout | Uploads with one site down | Reads |
|---|---|---|
| replication 2 on two sites, `consistent` | fail until it returns | work |
| replication 2 on two sites, `dangerous` | work; an upload is confirmed once one copy exists | work |
| replication 2 on three sites | the ones landing on the down site fail, about two in three | work |
| replication 3 on three sites | work | work |

Garage has no data-less tie breaker: a layout gateway node stores nothing and
does not count toward an object's write quorum, so availability with a site
down takes a third copy on a third host, with the disk for it.
`storage.garage.consistency` sets the mode, `consistent` by default, and
validation warns on `dangerous` and on the two-site trade above. What
`dangerous` risks: an upload exists on one disk until the other site catches
up, uploads made during an outage are single copy until it returns, and a
read can miss a recent change, including a delete, after a failover.
Changing the mode is an ordinary `deployment apply`.

**Changing `replication` is not an ordinary apply.** Garage refuses to start
when its stored layout was built at another factor, so `deployment apply` refuses to
write such a `garage.toml`. `storage add --change-replication` runs the only
procedure Garage documents, which it calls unsupported: every node at the old
factor is stopped before any is changed (a node that meets a peer at a higher
factor exits), each stored layout is moved aside to
`meta/cluster_layout.rf<N>` rather than deleted, `garage.toml` is rewritten,
and the cluster is laid out again. Media is unavailable for about a minute.
Before it stops anything it waits for every node to have caught up, so nothing
written at `dangerous` exists on one node only, and it records each bucket's
object count on a host, to be compared once the data has moved.

**A host that exists only for Garage has the `storage` role.** A third copy
on a third host is what keeps uploads working with a site down, and that host
need not be a data, apps, gateway or witness site. It renders WireGuard and an
infrastructure stack with Garage alone. Garage is still placed by
`storage.garage.sites`, so the role on a site that list does not name is
refused; a data or apps site that runs Garage needs no such role.

**Each site can advertise its own capacity.** `storage.garage.capacities` maps
a site to a size and overrides `storage.garage.capacity` for it, so a small
host can share a layout with large ones: Garage spreads partitions in
proportion to capacity, and at replication 3 on three nodes every node still
holds a full copy, so the smallest bounds what the cluster can store. A changed
capacity reassigns that node alone, in one new layout version, and the sync
gate waits for the rebalance. `layout show` prints capacity in decimal units to
one decimal place (`3GiB` shows as `3.2 GB`), so the two are compared in bytes
within 2%, and a change the rounding hides plans nothing.

**`storage add --stop-test` proves a node can go.** In the smoke stage it stops
Garage on the last listed site, never the first, reads the probe through the
rest and the media hostname, writes and reads a second probe, and starts the
node again whatever happened. It runs only where uploads survive one node
down: replication 3 on three or more sites, or replication 2 or more at
`dangerous`, where it opens the single-copy window that setting accepts, for
about a minute. Elsewhere it is refused before anything stops. Founder
decision.

**Removing or replacing a node is not built**, by founder decision, except for
the node of a site removed with `site remove --host-gone`, which is skipped as
dead (founder decision, 2026-10-11; below). After `garage layout remove` the
old node must stay online until its data has moved, because Garage does not
track block migration: a removed node's blocks become deletable only ten
minutes after their references drop, and were observed leaving between about 10
and 20 minutes after the apply. A dead node needs `layout skip-dead-nodes`,
possibly with `--allow-missing-data`. Both are decisions about losing data, and
each gets its own gate; `docs/specs/2026-10-07-garage-gaps.md` records what was
learned.

**A cut-off Garage node is skipped as dead.** Garage keeps an old layout
version live until every node in it has acknowledged and synced the new one,
and a node whose host is not reached never will. So `site remove --host-gone`
removes the node from the layout, applies it, and runs `garage layout
skip-dead-nodes --version <n>`, which raises the acknowledgement of every node
that is down to that version (Garage v1.0.1, `src/garage/cli/layout.rs`,
`cmd_layout_skip_dead_nodes`). The remaining nodes still sync from each other.
When they hold a copy of every partition, which needs
`storage.garage.replication` of 2 or more and at least that many nodes
remaining, nothing is lost and nothing is asked. Otherwise the objects only the
cut-off node held are lost: the plan says so, the operator types `lose
objects`, and the command passes `--allow-missing-data`, which raises every
node's sync too. The gate waits for one live layout version on every remaining
node, and for an empty resync queue unless objects were given up, since their
blocks are retried and fail for as long as anything references them. Founder
decision.

#### Replacing an app's key: `storage rotate-key`

An app's S3 key is replaced when it may have leaked, Eg: a key ID committed to
a public history. **`paisans storage rotate-key --app <app>`** does it, dry run
by default and in gated stages, like `storage add`:

| Stage | What it does | Gate |
|---|---|---|
| 1. secrets | generates a new pair, the way `init` does, keeps the current one as `apps.<app>.s3_previous_access_key_id` and `s3_previous_secret_access_key`, and writes the file encrypted to its recipients | the file holds both pairs |
| 2. Garage | imports the new key under the app's name and grants it read, write and owner on the app's bucket, beside the old key; website access is the bucket's and is left as it is | `bucket info` lists the new key ID with `RWO` |
| 3. switch | applies the app alone, as `deployment apply --site <site> --only <app>`, on every site running it | `deployment apply --only` has nothing left to do, and the app passed apply's health gate |
| 4. prove | from each of those sites, writes a probe with the new key through the S3 address the app is rendered with, reads it back, deletes it | byte for byte |
| 5. retire | deletes the old key from Garage, then removes the previous pair from the secrets | Garage no longer holds the old key ID |

The plan prints both key IDs, so an operator can see which one is retired, and
never a secret key. **It is staged because the app must keep a working key
the whole time.** The secrets are written before Garage hears of the new key,
so Garage never holds a key the secrets file does not; the old key stays in
Garage and granted until the app has been switched and the new key proven, so
a run that stops anywhere before stage 5 leaves the app running on whichever
key it was last started with. A previous pair in the secrets file is what says
a rotation is in progress, and a re-run plans from it rather than generating a
second key. Every gate runs on every execute, so the old key is deleted only by
a run that has just seen stages 3 and 4 pass. Garage never lets a deleted key's
ID be imported again (`handle_import_key` in v1.0.1's
`src/garage/admin/key.rs`), which is one more reason the deletion is last.

Keys are found, granted and deleted by ID alone, because both keys carry the
app's name while a rotation runs and Garage does not keep names unique.

### `dns init` creates the records a deployment needs

Every public name a deployment answers on needs a DNS record, and the toolkit
creates them, not the operator and not an agent working by hand. A record typed
into a provider's console is a record nothing checks against the configuration,
and the first anyone hears of a typo is a visitor who cannot reach the site.
Founder requirement. How to run it, and its flags, are under *`paisans dns
init`* in [docs/commands.md](docs/commands.md).

**The records are derived, never declared.** Each app's `hostname`, each of its
role `hostnames`, and the media hostname of each app that keeps objects in
Garage, derived or declared, get an A record pointing at the gateway site's
`public_address`, except an app pinned to a monitor site, whose names point at
that monitor's own `public_address` (see *The monitor has a site of its
own*). A site whose `endpoint` is a name rather than
an address gets an A record pointing at that site's own `public_address`, so
the name the operator wrote resolves there; the mesh itself dials the address
(see *Where the mesh dials a site*). An endpoint given as an address, or as a
port alone, names nothing and gets no record. Where a site also
declares `public_address6`, each of its names gets an AAAA record as well.
Deriving them keeps every hostname written in one place, since a second copy is
the one that drifts and sends visitors somewhere else.

```yaml
sites:
  vm:
    roles: [gateway, witness]
    address: 10.44.0.3
    endpoint: vm.example.org:51820     # optional: gets an A record; the mesh dials public_address
    public_address: 203.0.113.10       # what the internet reaches, not the interface
    # public_address6: 2001:db8::10    # optional, adds AAAA records
```

**`public_address` is declared, not discovered.** The address a host sees on
its own interface is often not the one the internet sees, behind NAT or a cloud
provider's one to one mapping, and a guessed address published in DNS is cached
for the record's lifetime. `validate` refuses a value that is not an address of
the right family, and one the internet cannot reach: private space (RFC 1918
and RFC 4193), carrier grade NAT (100.64.0.0/10), loopback, link local, or an
address inside `mesh.subnet`. A missing `public_address` is not refused by
`validate`, because a deployment may manage its DNS some other way. `dns init`
refuses it, naming the site, wherever a record would need it.

**`dns init` creates, and updates only its own address records. It never
deletes.** Removing a record the configuration no longer implies is `dns
prune`, below, under rules of its own. For each record it needs, the provider
holds one of four things:

| Provider holds | Plan |
|----------------|------|
| the same record, unproxied | `✓`, shown with `-v` |
| nothing at that name of that type | `○ create` |
| the A or AAAA record `dns init` made for this deployment, pointing at an address it no longer declares there | `○ update`, naming the old address |
| a different address, a CNAME, a proxied copy, or an A or AAAA the deployment does not declare | `✗`, a conflict |

**An update touches only what this deployment's `dns init` wrote.** A site's
`public_address` can change, and each record pointing at the old one would
otherwise have to be deleted by hand before `dns init` could write the new
one. The record updated carries exactly this deployment's comment,
`paisans-<token>: created by paisans dns init`, is unproxied, and is the only
record of its type at the name; with its address changed, the name holds
exactly what is wanted. A record with no comment, another deployment's, two
records of the type, or a CNAME or stray record beside it, is a conflict. A
record pointing at another declared site is not updated: it waits for
`gateway move`, below. That is why a gateway role move keeps the old site
declared, with its `public_address`, until `paisans gateway move` finishes:
an address no declared site holds reads as the gateway's own changed address,
and `dns init` updates the record to the new gateway at once.

Each line names the record whole, its type, name, address and zone. Piped
output says `ok`, `todo` and `FAIL` instead:

```
dns cloudflare
  ○ create A talk.example.org -> 203.0.113.10 (zone example.org)
  ✓ A vm.example.org -> 203.0.113.10 (zone example.org)
```

**Any conflict refuses the whole run before a single record is written.** This
is the same rule `deployment apply` follows for a file it did not write: a record that
disagrees with the configuration, and that this deployment's `dns init` did not
make, is somebody's, and overwriting it would take
down whatever they pointed it at. The whole run stops because half a set of
records is a deployment some names reach and others do not, which is harder to
diagnose than a refusal. A stray AAAA is a conflict because clients on IPv6 would reach
it instead of the gateway, silently. After `--execute`, every record created or
updated is read back and compared.

**Records are DNS only, never proxied through a provider's CDN.** A proxied
name resolves to the CDN's addresses rather than the gateway's, so a WireGuard
endpoint behind it cannot be dialled at all, and every request reaches Caddy
from the CDN rather than the client, undoing the client address handling the
mesh subnet setting exists for. A proxied record that otherwise matches is a
conflict, not a match.

**One gateway only, for now.** With several gateway sites, which address a
hostname should resolve to is a design that does not exist yet. `dns init`
refuses rather than picking one. Moving the gateway role to another site is
not an update either: the records `dns init` made for the old gateway wait for
`gateway move`, which adds the new gateway's records beside them and deletes
them only once the new gateway is verified, the overlap in *Make it an
overlap, not a cutover*.

**It reaches no host.** It talks to the DNS provider's API from the
workstation, using `external.acme_dns_token`, the same zone scoped token the
gateway uses to answer ACME challenges. A second credential with the same reach
would be one more thing to rotate and leak. The token travels only in a request
header and is redacted from every error. Finding which zone holds a name means
reading the zone, so the token needs read access to the zone as well as
permission to edit its records. The zone is found by trying each suffix of the
name, longest first, so a delegated subzone wins over its parent.

**Cloudflare is implemented. Other providers are not yet**, deSEC included,
although both answer ACME challenges. Answering a challenge and managing
ordinary records are different code, and `dns init` refuses an unimplemented
provider by name rather than pretending. Until one is implemented, create the
records it would have created by hand, unproxied.

**Every name says whether it has mail.** A name that publishes an address can
be forged as the sender of a message whether or not anybody sends mail from
it, and a receiver refuses the forgery only when the name says it sends none.
So `dns init` publishes, beside the address records, the records a DNS
provider recommends for a domain that sends no mail. A receiver enforcing
DMARC applies the registrable domain's policy to every name under it that has
none of its own, so one DMARC policy covers every hostname, and a hostname
gets no record of its own unless no policy the deployment may write covers
it:

| Name | Record | What it tells a receiver |
|------|--------|--------------------------|
| `community.domain` | TXT `v=spf1 -all` | no server may send as the domain |
| `community.domain` | MX `0 .` (RFC 7505's null MX) | the domain accepts no mail, so nothing is delivered to it and no bounce is sent in its name |
| `*._domainkey.<community.domain>` | TXT `v=DKIM1; p=` | no selector under the domain has a key to verify a signature with |
| `_dmarc.<community.domain>` | TXT `v=DMARC1; p=reject; sp=reject; adkim=s; aspf=s;` | reject what fails, on the domain (`p`) and on every name under it (`sp`) |
| `_dmarc.<registrable domain>`, when `community.domain` is below it and the conditions below hold | TXT `v=DMARC1; p=reject; sp=reject; adkim=s; aspf=s;` | a receiver finding no policy at a name's own `_dmarc` applies the registrable domain's (`example.org` for `talk.staging.example.org`), never `community.domain`'s, so this one record covers every hostname below it |
| `_dmarc.<hostname>`, for every name that gets an address record, when the registrable domain's policy is one `dns init` only checks or may not write, and does not reject | TXT `v=DMARC1; p=reject; sp=reject; adkim=s; aspf=s;` | each hostname carries the policy the registrable domain does not: below a registrable domain above `community.domain`, or under a `community.domain` with mail of its own |
| a hostname that is itself a registrable domain | TXT `v=spf1 -all` | a DMARC policy there would govern names the deployment does not run, so the hostname says it sends no mail with SPF, which applies to that exact name alone |

When `community.domain` is below its registrable domain, every name a policy
there would govern is one the deployment may not run, so `dns init` writes
that policy only where it changes nothing for anybody's mail. It creates
`_dmarc.<registrable domain>` when all of these hold:

* the DNS provider holds the registrable domain's zone;
* the zone has no DMARC policy there, since one is never replaced;
* no name in the zone, or in a zone delegated from it, has mail and no DMARC
  policy of its own. A name has mail when it holds an MX record other than
  the null MX, an SPF record naming a sender, or a DKIM key at a selector
  under it, or when it is a CNAME to a name that receives mail or names a
  sender, which `dns init` resolves through public DNS. A sender `dns init`
  gives a policy in the same run (see below)
  has one of its own; a hostname whose own policy the registrable domain's
  would cover does not, since that policy is then never written. The domain
  itself never has one of its own: the policy written there is its;
* no sender the configuration names under the registrable domain (see
  *A name with mail of its own* below) lacks a DMARC policy of its own, even
  one whose mail's DNS is not there yet;
* every zone delegated from it is held by the DNS provider, so nothing under
  the domain is out of sight.

A policy already there that rejects (`p=reject`, `sp=reject`, no `pct` below
100) with strict alignment (`adkim=s`, `aspf=s`), whoever wrote it, and is the
only one there, covers the hostnames, and `dns init` writes no
`_dmarc.<hostname>` and no `_dmarc.<community.domain>`. Relaxed alignment
covers nothing, since a forger passes it with any sibling name's SPF. Nor
does the policy cover a hostname when a name between the two holds a policy
that is not as strict, delegates its policy by a CNAME, or is delegated to
nameservers the DNS provider does not hold: a receiver walking up the tree
under DMARCbis may read that one first.
The run that creates the policy still gives each hostname its own, since
nothing says the provider let it be written until it reads back; `dns prune
--execute` then removes them. A policy that is weaker, or a missing one that
`dns init` cannot write, is a `!` note naming what stops it, and each
hostname carries its own policy instead. So is more than one policy there,
which a receiver applies none of, and a CNAME delegating the policy to a
DMARC service, which `dns init` does not follow.

The policy `dns init` wrote is a conflict once a sender the configuration
names under the domain has no policy of its own, or the domain itself has an
MX record or an SPF record naming a sender, since that mail is rejected
under it: give the sender its own policy, or delete this one by hand. The
rest of the zone is read only before the policy is written, not on every
run, so mail someone adds elsewhere under the domain later is theirs to give
a policy. A registrable domain the DNS provider does not hold is not read.

For `talk.example.org` with `community.domain: example.org`, and
`talk.staging.example.org` with `community.domain: staging.example.org`, the
whole tree's no-mail DMARC is `_dmarc.example.org`. A relay's sending name,
such as `send.example.org`, keeps the policy at its own `_dmarc`, which a
receiver reads first.

**A name with mail of its own gets none of them.** Some communities run mail
at their domain through a hosted service (Eg: Google Workspace, Fastmail or
Proton Mail), and its SPF, MX, DKIM keys and DMARC policy are that service's
to configure. List such names under `community.mail_domains`:

```yaml
community:
  domain: example.org
  mail_domains: [example.org]
```

A sender in an `smtp` block makes its own domain one of these without being
listed: the domain of `smtp.from_address`, and of every `from_address` an app
that sends mail declares for itself, so the toolkit never locks out mail it
configured. Every other name is locked down as above, the hostnames under a
mail domain included. Under a `community.domain` with mail of its own, each
hostname gets its own DMARC policy while the domain's does not reject, and
none once it does. A hostname under a mail name below the registrable domain
is covered like any other, by the registrable domain's policy. `dns init` checks the DMARC policy of every mail name
under `community.domain`: a policy weaker than `p=reject` and `sp=reject` with
no `pct` below 100 is a `!` note, never a conflict and never replaced. A
missing policy is a note too while the name holds an MX record, since whoever
runs that mail sets it. With no MX, the name only sends, through a relay, and
`dns init` creates `v=DMARC1; p=reject; sp=reject;` at its `_dmarc`. Its
relaxed alignment passes mail the relay signs for any name under the same
registrable domain, such as a subdomain the relay has verified. `dns prune`
never deletes it. `validate` refuses an entry that is not a hostname
(`mail-domain-is-not-a-hostname`) or is outside `community.domain`
(`mail-domain-outside-the-domain`), since `dns init` publishes nothing there.

Only a record saying the same kind of thing competes with one of these. Any
other TXT record at the name, a provider's verification code for one, is left
alone and stops nothing. A record of the same kind that says something else
is a conflict, and so is a second SPF record, since a receiver treats two as an
error and ignores both. A DMARC policy is compared by what it makes a receiver
do, so one that adds a reporting address to `p=reject` is present, not a
conflict. At a hostname, `dns init` writes no SPF record but reads the one
there: one that lets a server send is a conflict, since the hostname has mail
nobody listed and the DMARC policy covering it would reject that mail. An SPF record that lets a server send, or an MX that receives, is the
likeliest conflict, and it says which line to add: the name has mail, and
belongs in `community.mail_domains`.

**`dns prune` removes what `dns init` made and nothing wants any more.** A
deployment that renames or drops an app's media hostname is left holding the
old name's A record: `dns init` created it, and no line of the configuration
implies it now. Founder decision: the toolkit removes such a record, not an
operator in the provider's console, and `dns prune` is the one command that
does, since `dns init` never deletes. A deletion by hand is exactly the
unchecked edit to the zone that `dns init` exists to replace, and the next one
deletes the wrong name. How to run it, and its flags, are under *`paisans dns
prune`* in [docs/commands.md](docs/commands.md).

**`--name <fqdn>` vouches for one name outside the domain.** A hostname
configured explicitly beside `community.domain` (Eg: a media name
`staging-media.example.org` for the domain `staging.example.org`) and later
dropped leaves a record no rule can tie to this deployment, so prune keeps it
and says so. Naming it with `--name` lifts the scope rule for that exact name
and nothing else: the comment, the address and the not-wanted rules still
apply, and a `--name` that matches no record `dns init` created is refused, so
a typo cannot widen anything. The lift is per name because a zone usually
holds more than one deployment's names. The same holds for the registrable
domain's DMARC policy `dns init` wrote, once `community.domain` moves out from
under that domain: `--name _dmarc.example.org` removes it.


A record is deleted only when every one of these holds:

| Rule | Why |
|------|-----|
| it carries exactly the comment this deployment's `dns init` writes, `paisans-<token>: created by paisans dns init` | a record without it was never this deployment's: one with another token is another deployment's, and one with no token was never the toolkit's |
| it is an A or AAAA record holding an address of that record's family, or a TXT or MX record holding exactly one of the no-mail contents above at the kind of name `dns init` writes it at | those are the only records `dns init` creates, an IPv4-mapped address in an AAAA record would otherwise match an IPv4 address, and a TXT or MX that says anything but "no mail here" is somebody's mail |
| its name is `community.domain`, a name under it, or a name the configuration produces now | every media hostname, derived or declared, is under the domain, and so was the shared one per app hostnames replaced |
| for an address record, its address is a site's `public_address` or `public_address6` | it pointed at this deployment's own host. A no-mail record points at nothing; its exact content ties it to the toolkit instead |
| no record of its type is wanted at its name | prune removes names, it does not repoint them. A DMARC policy `dns init` only checks is not wanted, except the registrable domain's, which the hostnames below rely on. A hostname's own policy is not wanted while the registrable domain's rejects |

**A name given mail is a prune, then the provider.** Listing a name in
`community.mail_domains` stops `dns init` wanting its no-mail records, and
`dns prune --execute` deletes them. Do that before adding the mail service's
own records: its SPF beside the toolkit's `v=spf1 -all` would be two SPF
records, which a receiver ignores, and the null MX would turn its mail away.

Until the prune has run, `dns init` refuses, and so does `deployment apply` without
`--site`, which runs it before any site is applied. At every name with mail under `community.domain` it looks for a
no-mail record this deployment's `dns init` made (its comment, and exactly a
no-mail content), and each one it finds is a conflict naming `dns prune
--execute` as the fix, shown as `remove with dns prune` in place of what is
wanted. That holds after the fact too: a mail service's records added before
the prune leave the toolkit's beside them, and the next run says so. A no-mail
record without this deployment's comment is somebody's choice and is left
alone, as prune leaves it.

**The comment is necessary and never sufficient.** A comment is free text that
anyone with the zone can write, on a record the toolkit never made, and a rule
that trusted it alone would let one edit in a console steer a deletion. The name and address rules tie a record
to this deployment by things a comment cannot forge. The cost is that a stale
record outside `community.domain`, at a name the configuration no longer names,
is kept: nothing left in the configuration says it was ever this deployment's.

A wanted name still pointing at an old address is kept too. Replacing its
address would be an update, which nothing in the toolkit does; `dns init`
reports it as a conflict for a human to resolve.

The plan marks each record the way `dns init` does, named whole with its
zone and record id. A record to delete is `○ delete`. Every record that
carries the comment and fails a rule is `✓ keep`, shown with `-v` with each
rule it fails under it, so a record that was expected to go says why it did
not. One kept only for its name, which every other rule would delete, is the
operator's to vouch for or delete by hand, so it is `! keep` with its reason at
every verbosity. Piped output says `todo`, `ok` and `WARN` instead:

```
dns prune cloudflare
  ○ delete A media.example.org -> 203.0.113.10 (zone example.org, record rec-1)
  ! keep A old.example.net -> 203.0.113.10 (zone example.net, record rec-2)
       old.example.net is outside community.domain (example.org) and is not a
       name this configuration produces, so nothing says it was ever this
       deployment's. If it was, name it with --name old.example.net
  ✓ keep A talk.example.org -> 203.0.113.10 (zone example.org, record rec-3)
      this configuration still wants a record of type A at this name (apps.talk.hostname)
```

 Prune lists every record in each zone the deployment's names live in,
every page of the listing, because it decides from what is missing from the
configuration as well as from what is in it. After `--execute` it lists each
zone again and fails if any deleted record is still there; a run that stopped
partway plans again from a fresh listing, so a re-run resumes.

`site remove` deletes a removed site's records with the same planner and the
same delete, comparing against the configuration before and without the site
rather than as it stands (see *`site remove` takes a site out*). That is the
one case prune cannot cover: once the site is out of `paisans-stack.yaml`, its
address is no longer declared, and prune keeps every record pointing at it.

### A data site's watchdog is declared, not assumed

Patroni fences itself with `/dev/watchdog`: if it stops renewing the timer
while it is leader, the machine reboots before another node's promotion can
leave two primaries taking writes. The rendered Patroni runs with watchdog mode
`required`, set in `SPILO_CONFIGURATION` because Spilo passes no `PATRONI_*`
variable through to Patroni, and maps `/dev/watchdog` into its container, so
on a data site without a device compose will not even create the container.

Which device a host has is a property of the machine, not of the deployment: a
board with an Intel TCO timer (`iTCO_wdt`), an AMD one (`sp5100_tco`), a server
with a BMC (`ipmi_watchdog`), a VM with an emulated one (`i6300esb`), or a small
cloud instance with nothing. So it is a per site key, read only where the site
holds the data role:

```yaml
sites:
  home-a:
    roles: [data, apps]
    address: 10.44.0.1
    ssh:
      host: home-a.local
      keys: [alice]
    watchdog: auto   # auto | required | softdog; auto when absent
```

| Mode | What `site prepare` does | What is rendered |
|------|---------------------------|------------------|
| `auto` | uses the device the host has, whatever its driver; with none, loads and persists `softdog` and prints a warning | `required`, device mapped |
| `required` | refuses unless a driver other than `softdog` is present | `required`, device mapped |
| `softdog` | loads and persists `softdog`; warns only when a hardware or hypervisor watchdog is present, because Patroni opens that device rather than softdog | `required`, device mapped |

**`softdog` is a fallback, never the assumption.** It is a kernel timer, so it
reboots a machine whose Patroni hung but not one whose kernel did, and a hung
kernel is exactly the case where the node cannot fence itself any other way. A
hardware or hypervisor watchdog is better wherever one exists, and `auto` takes
the real one when it is there.

**`off` is not a mode.** A data site's Patroni needs a watchdog to fail over
safely: a member without one can keep acting as leader after it has lost its
lock. No other site uses one, so the loader refuses `watchdog: off` on any
site, and the host's owner admits a data site only with `paisans site admit
--watchdog`.

**Only one process can hold the device, and it has to be Patroni.** `site
prepare` refuses a host where systemd's own runtime watchdog
(`RuntimeWatchdogSec`) is set or a `watchdog` daemon is running, rather than
leaving Patroni to fail to open the device at its first start. This is the
host side of the one Patroni per data bearing host rule in *There is one HA
cluster, and that is deliberate*.

### A host's memory is measured against its roles

A host short of memory keeps running until the kernel's OOM killer picks a
process to kill, and it runs slowly before that. An etcd member that
stalls answers Patroni's reads late, and a witness that stalls costs the
cluster its tiebreaker. So `site admit` warns in its plan, and `doctor` on
every run, when a host has less memory than what it runs needs. It is a
warning and never a refusal: a host under its floor runs, and may be all
its owner has.

The floor is the sum of what the host runs, each part once, compared with
`MemTotal` in `/proc/meminfo`, which the kernel reports a little below the
machine's RAM. A part two roles run is counted once: a monitor beside the
gateway is served by the gateway's Caddy, and a data site that is also a
Garage site runs one Garage. The numbers are set from what each role runs,
not measured on hosts, and live as named constants in `internal/hostmem`.

| Part | Floor | Counted where | Basis |
|------|-------|---------------|-------|
| The host | 512 MiB | every host | Ubuntu Server's own services, an apt or unattended-upgrades run among them, the deployment user's rootless Docker (dockerd, containerd, RootlessKit), WireGuard and the heartbeat container |
| etcd | 256 MiB | the members of `etcd.members`: `data` and `witness` sites | an etcd v3.5 member holding Patroni's few keys, with room for its snapshots and compactions |
| PostgreSQL, Patroni and HAProxy | 1 GiB | `cluster.sites`: the `data` sites | Spilo's PostgreSQL with its shared buffers and a few connections per app, Patroni's Python process, and HAProxy in front |
| Garage | 256 MiB | the sites `storage.garage.sites` names, whatever their roles | its metadata database and the buffers of the uploads it is handling |
| Caddy | 128 MiB | the gateway, and a monitor with `web_server.managed_by: paisans` | the toolkit's Caddy, its certificates and connections |
| oauth2-proxy | 64 MiB | the gateway the gate app is pinned to, when an app is gated | the visibility gate, and its session store, capped at 16 MiB |
| The uptime monitor | 256 MiB | `monitor` sites | a Node.js process |
| Pocket ID and an app | 1 GiB | `apps` sites | Pocket ID and one app beside it. Mbin, with its PHP workers and Valkey, or Wiki.js needs more of its own, so an apps site running several needs more than its floor |

**`doctor` counts what the configuration places; `site admit` estimates by
role.** Doctor reads `paisans-stack.yaml`, so it knows each column above.
Admit has only the request, which names the site's roles and whether it
binds ports 80 and 443, which is whether it runs Caddy. So admit counts
Garage on every `data` and `storage` site, as `storage.garage.sites`
usually places it, and oauth2-proxy on every gateway, and the two can
differ by Garage's 256 MiB or oauth2-proxy's 64 MiB.

By role, a witness needs 768 MiB, a monitor running Caddy 896 MiB, a
gateway and witness 960 MiB, a gateway, witness and monitor 1216 MiB, and
a data and apps site 3072 MiB.

**Swap counts toward the floor, up to a quarter of it.** Swap holds what a
host keeps and rarely touches, which is what lets it ride out an apt run,
but a process whose working memory is swapped out slows to the speed of
the disk, etcd and PostgreSQL above all, so at least three quarters of the
floor must be RAM. Only swap that is on counts, and in admit's plan so
does the swapfile `--swapfile` makes. Swap on a zram device does not
count: it compresses what it holds into RAM, so it stretches RAM rather
than adding to it, and `site admit` offers a swapfile beside zram as it
does where there is no swap. A 1 GiB VPS as gateway and witness is under its
960 MiB with no swap, and over it with a swapfile; a 458 MiB one is under
it either way.

**Each deployment is checked on its own.** A host admitted to two
deployments is held to each one's floor apart, never their sum: neither
deployment reads what the other runs there. The host's owner adds them up.

**Inside a container, `MemTotal` is the machine's.** A host that is itself
a container, such as an LXC guest, reads the RAM of the machine it runs on
unless lxcfs or the like presents the container's limit as
`/proc/meminfo`, so the check can pass on a container whose limit is far
below its floor.

### Decryption happens on a workstation, not on a host

Rendering locally and pushing means no age key ever reaches a host. That
follows the existing posture: on a shared host, users with `sudo` or `docker`
membership are root-equivalent, so a decryption key there is readable by
someone other than its owner.

Hosts end up with plaintext `.env` regardless — Compose requires it — so the
gain is not host hardening. It is **blast radius**: root on one host yields that
host's rendered secrets, not the gateway's WireGuard key or another site's
credentials. The complete set exists only encrypted.

Two honest limits, which belong in any doc that describes this:

* **Encryption protects the secret everywhere it is stored, not where it is
  used.** The host's `.env` is plaintext. What changes is that it becomes a
  disposable build artifact rather than the only copy in existence.
* **Environment variables are visible** in `docker inspect` and
  `/proc/<pid>/environ`. `env_file` does not change that. Encrypted at rest
  does not mean hidden at runtime.

### Backup and restore

`paisans backup` must produce **four** things. Any three of them is a partial
restore that looks complete until someone goes looking for their account.

| Artifact | Holds | Without it |
|----------|-------|------------|
| `paisans-stack.yaml` | topology | nothing knows what to build, unless a site holds a version of it (every site does; see *Every site keeps signed copies of the configuration*) |
| `secrets.enc.yaml` | credentials | every secret must be regenerated and every OIDC client re-minted, unless a site holds a version of it and an operator's age key still decrypts it |
| Database dumps / WAL archive | member accounts, posts, settings | the community is empty |
| Object storage | media, uploads, attachments | posts render with broken images |

The config files capture **infrastructure**, not **application state**. Groups,
magazines, moderation, collections, and anything set through an admin panel
live in the database. A community's identity provider configuration is
particularly easy to assume is in the config when it is not.

The age private keys are the fifth thing, and they must be backed up somewhere
other than the repo they decrypt. A copy of the first two on every site is no
substitute: the encrypted file on a site is readable only with an age key, so
losing every operator's key still loses every credential.

The first two need no backup of their own beyond the sites, because every site
keeps every signed version of them and `paisans deployment recover` writes them onto
an empty workstation. The sites are one failure domain's worth of copies,
though: a copy outside them, in object storage, waits for the backup design,
which is not built.

## Every site keeps signed copies of the configuration

`paisans-stack.yaml`, `secrets.enc.yaml` and `.sops.yaml` live on operators' workstations
(see *Backup and restore*), so a lost workstation without a copy elsewhere loses
the topology and every credential. Every site therefore keeps every version of
the three files, each signed by the operator who published it, and an operator with an
empty workstation can recover them from any host.
`docs/specs/2026-10-12-config-store.md` is the approved specification, and
*`paisans config`* and *`paisans operator register`* in
[docs/commands.md](docs/commands.md) give the commands' mechanics.

The copies are recovery copies now and are built to become the deployment's
shared source of truth: every version records its parent, two versions from one
parent are a fork, and `deployment apply` merges a draft older than the sites'
newest version forward before it deploys anything. The one loss this cannot protect is every operator losing their age
key, which is why *Rendered configuration is a build artifact* tells every
deployment to add a second recipient to `.sops.yaml`.

**A version is a signed git commit of exactly the three files**, and, when
its apps use blueprints from a catalog, the core catalog included, the
snapshot of those blueprints under
`blueprints/<catalog commit>/<blueprint>/` with a `blueprints/SHA256SUMS`
beside it. A commit holding anything else, or a snapshot whose files do not
match its SHA256SUMS, is invalid. The snapshot is what lets a removal or a
recovery read a blueprint whose catalog has gone, and what makes every operator
and every site render the same bytes; publishing takes it from where the
blueprints were resolved, and checking a version out installs it where they are
resolved from. Git names a version's files by SHA-1, so the SHA-256 list is
what the snapshot's integrity rests on beyond the signature. It is signed with the publishing operator's SSH
key by git's `gpg.format=ssh`, so signing goes through the operator's ssh-agent
and needs no new key. The signature covers the tree, and so the files, because
nothing in the files can prove who wrote them: encrypting to an age recipient
needs only the recipient's public key, which every stored `.sops.yaml` names, so
anyone could write a secrets file that decrypts for every operator. A version
carries `Paisans-Revision: <n>`, one more than its highest parent. Nothing
decides by the commit date, because any host's clock can be wrong and anyone
writing a copy can set it. Each version is kept under `refs/versions/<sha>`, a
name that is its own hash, so two versions never compete for one. A store
creates a version's ref only where it is absent and never moves or deletes one
(see *How a site stores them*).

### Trust starts from the operator's own signature, never from the sites

A site keeps whatever it is sent, so every decision is made on the workstation.
It starts from something the operator holds that nobody else can forge: the
newest version signed by the operator's own SSH key, or a root hash the operator
pins with `--root`. **A history found only on the sites is never trusted on its
own, however consistent it looks**, because someone with root on those sites
could have written it, signed with their own key and listing themselves as an
operator. The operator's key is the signing key the deployment's system copy
records (see *This workstation keeps one system copy of each deployment*); on a
workstation that records none, a pull, a log or a recovery anchors on the
public key the operator names with `--key`, which anchors only on versions it
signed itself, under one root, that the rules below already trust from that
root, of this deployment's id. A key alone cannot tell this deployment's
history from one it signed before a redeploy under the same id, where an
operator since removed may still be listed, so a newer version by another
operator, or the key's versions under two roots, need `--root`. A pinned root
anchors alone, and `--key` is then ignored. A pull or a recovery records the
named key when the agent holds it and an operator of the head lists it. With
neither an own signature nor a pin, a pull or a recovery is refused and asks
for one.

From that anchor a version is trusted when it passes three rules, each of which
closes a forgery:

| Rule | Because |
|------|---------|
| Shape: the tree holds exactly the three files and at most a sound blueprint snapshot, the signature is good, and the revision is one more than the highest parent's | a version of any other shape could carry files nothing reads, and a wrong revision breaks the order the head is chosen by |
| The signer: every parent is trusted, and the signing key belongs to an operator listed in every parent's `paisans-stack.yaml` | someone who is not an operator cannot extend the history, however many sites they control |
| Removal is final, key by key: when a trusted version drops a key a parent listed (an operator removed, or a key rotated out), every version signed by that key that is not an ancestor of the removal is not trusted | an operator removed for cause cannot publish a later-dated version that undoes it; a version that is not trusted removes nothing, so a forger cannot use one to remove a real operator |

The rule works key by key, not operator by operator, because rotating one of an
operator's keys out must bar that key without barring the operator's others. Removals
in the anchor's own history always stand, since the operator signed after them.
When two operators each remove the other and neither removal is in the anchor's
history, the toolkit refuses and shows both to the operator. Revision and hash
cannot decide it, because whoever signs chooses both. A root outside the
anchor's history is never trusted, for the same reason: only the anchor can say
where the true history starts, so a second root written on a site is refused.

**The head is the trusted version with the highest revision that no other
trusted version builds on.** Two such versions are a fork, which stops `deployment apply`
until `paisans deployment publish --merge` resolves it.

**Removing an operator who could decrypt the file** is also the operator's to
finish by hand: take their age key out of `.sops.yaml` and re-encrypt the file
under a new data key (`sops rotate --rm-age`) before publishing the version that
removes them. Nothing in `paisans` checks that this was done. A version that
only drops them from `operators` would leave every later write readable to them,
since the file is still encrypted to their key.

### An old version is made visible, not impossible

A version that was really published verifies forever, so someone who controls a
site can delete the newer ones there and serve an old one. Applying it would put
back old secrets, undoing a `storage rotate-key`. No signature can prove a
version is the newest, so two memories make an old one visible:

| Memory | What it does | Why it is there |
|--------|--------------|-----------------|
| The workstation | the system copy records the newest revision seen; a head with a lower one is refused until the operator types `rollback`, which re-bases the record to the head chosen once the command commits its change. A run that stops, and every dry run, leaves the record as it was | the site the workstation reaches may be the one lying; a record nothing could lower would trap an operator restoring from an old backup, so the typed word lowers it deliberately |
| The hosts | each host's grant record also holds the hash and revision of the version last applied to it, and `deployment apply` refuses an older revision, or another version at the same revision, unless the operator types `rollback` | a host that has had a newer version is a witness a deleted ref cannot erase; the same revision under another hash is a version another operator applied there, which this apply would replace |

So a host attacker can roll back only the hosts they hold. What stays is a fresh
workstation that reaches only sites the attacker controls: it can be served an
old, genuine version, and applying it reaches only those sites.

### How a site stores them

`site prepare` gives each site, as the deployment's user, under
`/srv/paisans/<token>/.paisans/`:

* a bare repository at `config/<token>.git`, the user's;
* a read-only `git daemon`, the user's systemd unit
  `paisans-<token>-config-daemon`, bound to the site's mesh address and
  serving only that directory. The mesh is already let in whole by the
  firewall, and a mesh peer reads `paisans-stack.yaml` and the encrypted
  secrets, both of which a peer is trusted with already. The address exists
  only while the deployment's WireGuard unit is up, and a user unit cannot be
  ordered after the system's `wg-quick`, so the unit's start waits for that
  address, however long it takes, and the daemon binds it once it is there; a
  daemon that stops later is restarted after ten seconds. It is enabled under linger, so it runs without a
  login, and `site prepare` starts it whether or not the mesh is up yet;
* a timer, the user's `paisans-<token>-config-sync`, every 15 minutes, that
  fetches the versions each peer's mesh address serves, so a site that was
  down when a version was published learns of it.

Every store, and the workstation's own, takes versions in by one rule, whether
from a peer's sync, a push or a gather. A fetch straight into `refs/versions/`
would fast-forward a version's ref to a child of it, which hides the version and
makes every later honest copy of it fail. So the fetch lands in a quarantine of
its own, `refs/incoming/<id>/`, and each ref there named for the commit it
points to becomes `refs/versions/<sha>` only where that ref is absent. An
existing ref never moves, every other ref is ignored, and nothing is pruned, so
no version is ever deleted; the quarantine refs are removed once read. A store
also bundles only correctly named versions, so a ref planted under another name
never travels.

A site that `site remove` takes out stops being a peer, so it neither fetches
nor serves. A version is tens of kilobytes, so thousands take a few megabytes
and nothing is pruned.

### Gathering, and why every site is asked

`pull`, `recover` and `deployment apply` gather the same way before they decide anything:

1. With `--execute`, every site reached starts its sync with its peers and the
   command waits, so a version only a site the operator cannot log in to holds
   (Eg: a site whose `ssh.keys` does not name them) arrives through one they can. A
   dry run and `deployment log` start no sync, because a command changes a host only
   with `--execute`; they read what each site holds now.
2. Every site is reached over ssh as the deployment's user, who owns the
   store: those the configuration names, or, for a recovery, those the trusted heads
   name, not only the hosts given on the command line.
3. The copies are combined, and only then are the rules applied and the head
   chosen, so a site serving an old version is superseded by the newer versions the
   rest hold.
4. Each site that did not answer is named with its error, and a command that
   reaches no site is refused.

### Commands

```sh
paisans operator register --execute   # sign an attestation, record your key
paisans deployment publish --execute      # sign the draft, push to every site
paisans deployment pull --execute         # merge the sites' newest into the draft
paisans deployment log                    # every version, signer, holding sites
paisans deployment publish --merge --execute   # one version on every head of a fork
paisans deployment recover --from admin@203.0.113.9 --key ~/.ssh/id_ed25519.pub --execute   # empty workstation
```

* **`paisans operator register`** gives an operator who has never published an anchor
  of their own, since they have signed nothing. It finds the key in their
  ssh-agent that matches one listed under their entry in `operators` (or the one
  `--key` names), gathers, and signs an attestation: a version with the head's
  tree and the head as its only parent. It records the key as the system copy's
  signing key, and every later `publish` and `deployment apply` signs with it.
  It attests only a head whose files equal the system copy's, since an
  attestation says "this workstation holds this version"; otherwise it shows the
  difference. A draft is neither compared nor touched. For a deployment with no
  system copy here, `--root` pins the root, and register makes the system copy
  from the version the pin leads to. It changes only the sites' stores and the
  system copy, and nothing without `--execute`. It goes ahead on whichever
  sites answer.
* **`paisans deployment publish`** commits the draft's files with the version
  it was drafted from as parent, signs, and pushes to every site it reaches. It
  refuses when that version is not the sites' head, naming `deployment pull`,
  when no loaded key belongs to an operator the parent lists, and when no site
  answers; a site that missed it gets it from its peers. A first version, with
  no trusted head among the sites that answered, starts the history. It goes
  ahead on whichever sites answer.
* **`paisans deployment pull`** moves the system copy to the head and merges
  the head into the draft, as *A draft behind the sites is merged forward*
  describes; in a folder with no draft it starts one.
* **`paisans deployment recover`** onto a workstation with no system copy of
  the deployment gathers from the
  named hosts and every site the trusted heads name. It anchors on a version
  the rules trust that the operator's key named with `--key` signed, or on
  `--root`
  with the hash of a version the operator trusts, kept somewhere other than the
  workstation that was lost, because a history found only on the sites is
  never trusted on its own; a pinned root anchors alone. `--from` takes any
  login that reaches the host, the owner's as well as the deployment's: the
  admission records under `/etc/paisans` are readable by every login, so
  recover learns the deployment and its user from them and reaches the host
  again as that user. `--id` names
  the deployment when the records name more than one. With `--execute` it makes
  the system copy and starts a draft in the directory of `--config`, which must
  hold none of the three files. A fork is refused, writing nothing, because which
  head wins is for an operator to decide, never an order: an operator who holds
  the files merges it with `publish --merge`. A deployment whose system copy here
  has a version is refused, naming `checkout` and `pull`, unless `--root` names
  another root than the one it pinned, or the copy pinned none, as after the
  deployment was made again under its id; the copy replaced is moved whole into `replaced/` of the data
  directory, never removed.
* **`deployment apply`** gathers before the review, so a fork is refused and a
  draft behind the sites' newest version is merged forward before the operator
  is asked anything; after
  the approval it publishes the very bytes it loaded and applies, so the store
  shows the declared state even when the apply stops part way, and each host
  records the version it was given. A version that reaches no site is refused,
  naming `site prepare` for a site with no store yet, because a version no site
  holds is not one the hosts can be checked against.

`deployment` holds these commands and `apply` together because a configuration
is a deployment: `deployment apply` puts the version on the hosts, and the rest
publish, move and recover that same version.

### This workstation keeps one system copy of each deployment

The workstation keeps one copy of each deployment it runs, the **system copy**,
in one place it can always find: the platform's data directory,
`~/Library/Application Support/Paisans` on macOS, where an application keeps the
data it makes, and `$XDG_DATA_HOME/paisans` elsewhere (`~/.local/share/paisans`
when that is unset), by the XDG Base Directory convention. `$PAISANS_DATA_DIR`
replaces either. The system copy is the only state Paisans trusts on the
workstation. `docs/specs/2026-10-24-system-copy.md` is the specification.

```sh
paisans deployment list                              # every system copy here
paisans deployment fetch --ssh admin@203.0.113.9     # fill one for every deployment a host holds
paisans deployment checkout --execute                # start a draft in this empty directory
paisans deployment checkout --id <id> --execute      # the same, with several system copies here
```

* **A system copy is `deployments/<id>/`** in the data directory: the three
  files of the version this workstation last deployed or synced, and beside
  them the store of every signed version gathered from the sites and the trust
  state, the pinned root, the recorded signing key, the newest revision seen
  and when each version reached this workstation. The secrets in it are
  encrypted as everywhere else, and the directories are `0700`, since together
  they name every community the operator runs.
* **It changes only on a deploy or a sync.** `deployment apply --execute` and
  `deployment publish --execute` make the version they published its files.
  Every command that gathers from the sites records what they hold in its store
  and when each version arrived; only a deploy and `deployment pull` move its
  files to a newer head, and an attestation by `operator register` moves its
  head to the attestation, whose files are the same. Every command that moves
  the head records the version in the copy's `saved.json`, which `deployment
  list` shows and `deployment checkout` checks the head against.
* **It is made only from the sites or a deploy:** the first deploy of a new
  deployment, `deployment recover`, the join of `operator register`, and
  `operator register --root`, since a pinned root anchors alone; `deployment
  fetch` fills one from a host. A draft of a deployment whose sites hold a
  version but whose system copy is missing here is refused, naming the
  recovery.
* **One command writes a system copy at a time.** A writer holds a lock in
  `locks/` of the data directory while it writes, and a second is refused,
  naming the first.
* **`deployment fetch` reads a host's registry, through sudo,** and gathers every
  deployment in it, changing nothing on the host. A system copy with a version
  keeps it: fetch records the versions the host holds in its store, and leaves
  its files, head and record as they are, naming `deployment pull`, which moves
  it. One with no version takes the newest trusted version. A deployment with
  no system copy here has no anchor, and a history found only on a host is never trusted
  on its own, so its newest versions are set aside in the system copy, never as
  its files, and listed `unverified`. Pinning one is a recovery:
  `deployment recover --root <sha>`, with a hash checked with another
  operator.
* **Ids from a host are checked before they become a path.** The registry is a
  file anyone with root on the host can write, and an id names a directory
  here.

### A folder holds a draft

The operator edits a deployment in any folder they like, often a git
repository. The folder holds a **draft**: `paisans-stack.yaml`,
`secrets.enc.yaml` and `.sops.yaml`, and nothing else of Paisans. No command
writes trust state into a folder, so two folders of one deployment never judge
the sites differently.

* **Which deployment.** A draft names its deployment by the `id` in its
  `paisans-stack.yaml`. In a folder with no draft, a command uses the only system
  copy on the workstation, or the one `--id` names.
* **Every command that writes the files writes the draft**, and in a folder
  with no draft first starts one from the system copy: `init`, `operator add`,
  `secrets set`, `secrets rotate`, `secrets prune`, `storage rotate-key`,
  `oidc client create`, `catalog add`, `doctor --execute` and the rest.
  `deployment checkout` is that start alone; `--head <sha>` writes another
  trusted head of the system copy's store, the files a hand merge for
  `publish --merge` starts from. A checkout never overwrites a file.
* **Commands that answer about trust read the system copy**: `deployment log`,
  `operator invite`'s root and signing key, `operator register`'s attestation.
  Other read-only commands read the draft where there is one, and the system
  copy where there is none.
* **The "drafted from" line.** The first line of a draft's `paisans-stack.yaml`
  names the version it was drafted from:

  ```yaml
  # paisans: drafted from revision 10 (3f9c2a1e…)
  ```

  with the version's full hash. Paisans writes it whenever it puts files into a
  folder: on starting a draft, on merging a newer version into it, and on
  deploying it, when it names the version just published. It is stripped from
  every version before signing. It decides only the merge base: a line that is
  missing, garbled, or names a version the system copy's trusted history does
  not hold is ignored, and the base is the system copy's version, which the
  review names. A draft without the line, such as one written by hand, is never
  refused.

### A draft behind the sites is merged forward

`deployment apply` and `deployment pull` sync the system copy from the sites
first. When the sites' newest version is the draft's base,
the draft is the next version. When it is newer, because another operator
deployed from any workstation or from another folder of this one, the system
copy moves to it and the draft is merged forward by a three-way merge: the base
is the version the "drafted from" line names, ours is the draft, theirs is the
sites' newest.

* `paisans-stack.yaml` and `.sops.yaml` merge path by path on their YAML node
  trees. A list is one value, and comments and key order come from the draft.
  `creation_rules` in `.sops.yaml` is always one value, since its recipients
  decide who can read the secrets. A file with a YAML anchor, an alias or a
  merge key (`<<`) is refused, since a merge cannot place one; write out the
  values they share by hand.
* Versions after the base that only sign its files again, as an attestation by
  `operator register` does, leave nothing to merge: `apply` deploys the draft
  on the sites' newest, and `pull` rewrites only the draft's "drafted from"
  line and says the draft is up to date.
* The secrets merge key by key after decryption, as *Secrets are merged key by
  key* describes.
* A path both sides changed differently is a conflict: the draft keeps its own
  value there, and the conflicting paths are listed once, a secret by its key
  only. The merged draft's "drafted from" line names the sites' newest version,
  so the next apply deploys whatever the operator settles.
* With `--execute` the merged draft is written and the command stops,
  deploying nothing: the operator reviews it and runs apply again. A dry run
  records what the sites hold and writes no draft. `deployment publish` refuses
  a draft behind the sites, naming `deployment pull`.
* A fork on the sites is refused, naming `deployment publish --merge`.

Before approval, `deployment apply`'s review shows the draft against the sites'
newest version: `paisans-stack.yaml` as a diff, the secret keys added, removed
or changed, never their values, and whether `.sops.yaml` differs. It also flags
changes that look like undoing a newer version: for each path or secret key the
draft changes, Paisans walks the trusted history back from the head, by first
parents, to the newest revision that changed it, and when the draft puts back
the value from before that revision the line says so:

```
sites.home-b: added back; revision 15 (signed by alice) removed it.
```

A key with a dot in it is one segment, written in brackets:
`sites["home.b"]`. A deliberate undo and an accidental one look the same, so a
flag informs and never refuses. A draft with no usable "drafted from" line also
gets a warning when one of its files was last saved before the version that
brought the sites' newest files reached this workstation, since it may not
include that version's changes; an attestation after it brings no change, and
is not the one the warning waits for.

### Secrets are merged key by key

Two edits of the encrypted file cannot be merged as text. A merge forward
decrypts the base, the draft's file and the head's in memory with this
workstation's age key and compares them key by key: changes to different keys
merge, and two different changes to one key are a conflict, named by key and
never by value, where the draft keeps its own value. The merged file is
encrypted again to the merged `.sops.yaml`'s recipients. To take the head's
value for a conflicting key instead, set it with `paisans secrets set`; a
`pocket_id_groups` key, which `deployment apply` records and `secrets set` does
not accept, is edited in the file with `sops` instead. A side with a `paisans mesh rekey` under way (a site with a next
WireGuard key, or sites a `--all` run has left) is refused, naming its sites and
`mesh rekey` to finish it first on the side that holds it: a rekey's state is a
sequence over live hosts, so combining two such states key by key could promote
a key no host runs, and a rekey finishes in minutes. A side with a `paisans
secrets rotate` under way (a secret with a next value under `rotations`, or
paths a `--all` run has left under `rotate_remaining`) is refused the same way,
naming the paths and `secrets rotate` to finish it on the side that holds it: a
rotation moves the checking side to the next value before its users, so
combining two such states key by key could promote a value no host's checking
side accepts. A file that cannot be
decrypted and parsed here is refused without quoting its error, because a parse
error can quote a value.

### Backups are declared in `paisans-stack.yaml`, and may be added later

The `backup` section says how often a run happens and where the backups go.
The design is `docs/specs/2026-10-11-backups.md`. What exists so far is the
section and its validation; nothing runs a backup yet.

```yaml
backup:
  schedule: daily               # daily | weekly | monthly | yearly
  at: "03:00"                   # UTC
  destinations:                 # the first is the primary
    - name: b2
      endpoint: https://s3.us-west-004.backblazeb2.com
      region: us-west-004
      bucket: example-backups
      keep: { daily: 7, weekly: 4, monthly: 6, yearly: 1 }
```

Each destination is any S3 compatible endpoint, and keeps its own retention:
`keep` says how many of each period's newest backup survive, which is restic's
own model. `prune` defaults to true; declaring `keep` is what approves the
deletions pruning makes.

**A deployment may run without backups, and is told so on every run.** No
`backup` section, or `enabled: false`, warns `backups-off` from `validate` and
`deployment apply`, and nothing else about backups is checked. Adding the section later is
an ordinary `deployment apply`, with nothing to convert first; the spec's *Turning backups
on later* says what each part does when it does.

While backups are on, `validate` refuses:

* **`backup-without-destinations`**: backups that go nowhere.
* **`backup-schedule-unknown`** and **`backup-at-not-a-time`**: a schedule
  other than the four periods, or a start that is not `HH:MM`, quoted so YAML
  does not read it as a number.
* **`backup-destination-incomplete`**: a destination without its endpoint,
  region or bucket. All three are needed to reach it.
* **`backup-destination-name`** and **`backup-destination-duplicate`**: a name
  that is not lowercase letters, digits and hyphens, or one used twice. The
  name is part of the destination's secrets' keys,
  `backup.destinations.<name>.access_key_id`, so it has to be one.
* **`backup-destination-same-place`**: two destinations with one endpoint,
  bucket and prefix. They would be one repository pruned twice, not two
  copies, and the single destination warning would not see it.
* **`backup-endpoint-not-a-url`**: an endpoint with anything past its scheme,
  host and port: a path, a query, a fragment. The bucket goes in `bucket`.
* **`backup-endpoint-has-credentials`**: a user name or password before an `@`
  in the endpoint. `paisans-stack.yaml` is plaintext and meant to be committed, so
  the credential belongs in the secrets, and no finding prints it back.
* **`backup-bucket-name`**: a bucket name S3's naming rules do not allow: 3 to
  63 lowercase letters, digits, dots and hyphens, starting and ending with a
  letter or a digit, no two dots together, not shaped like an IP address.
* **`backup-prefix-not-a-path`**: a prefix with a slash at either end, an empty
  segment, `.` or `..`, or a character outside letters, digits, dots, hyphens
  and underscores. It names a place inside the bucket, and only one.
* **`backup-keep-empty`**: pruning with nothing kept, which would delete every
  backup.
* **`backup-keep-negative`** and **`backup-number-negative`**: a negative count.
* **`backup-keep-never-filled`**: a tier finer than the schedule, Eg:
  `keep.daily` with `schedule: weekly`, which can never hold more than one
  backup per period.
* **`pinned-postgres-without-walg`**: a pinned app whose `images.postgres` is
  not `ghcr.io/paisans-software/postgres-walg`. That database would have no
  backup and nothing else would say so. The app either runs that image or opts
  out with `apps.<name>.backup: false`.

And warns, on every run:

* **`backup-app-excluded`**: each app with `backup: false`. Nothing it owns is
  backed up, so a database without a backup is always something the
  declaration says.
* **`backup-prune-off`**: a destination that is never pruned, whose bucket
  only grows.
* **`backup-single-destination`**: one destination is one failure domain.
* **`backup-without-a-monitor`**: no `uptime` app, so a failed run would be
  reported to nobody.

**Backups that are on have four kinds of secret.** `init` generates two, and
never shows them: `backup.restic_password`, which opens the restic repository
in every destination, and `backup.walg_libsodium_key`, 32 bytes as hex, which
encrypts every object WAL-G writes. A restore reads both from the secrets
file, so losing that file loses the backups too. Each destination's S3 key is
issued by its provider and owed, and `init` names it with what it needs: read,
write, list and delete on that bucket, and nothing else.

```
paisans secrets set backup.destinations.b2.access_key_id < key-id-file
paisans secrets set backup.destinations.b2.secret_access_key < secret-file
```

Backups that are off generate and owe nothing.

## Bootstrapping a site

`deployment apply` renders configuration on a workstation and pushes it over SSH — but the
WireGuard mesh those files create does not exist yet. First contact with any host
has to happen over a path that is not the tunnel.

That is what the `ssh` section of a site is: the **bootstrap route**, and the
record of who may use it. Its host is a LAN address, a public hostname,
whatever the operator can actually reach on day one.

**It must be a real address the operator already has.** Not an overlay network,
not a name that only resolves inside one deployment's private mesh. A toolkit
that quietly depended on one would work on the machines it was written for and
fail on every fork.

### Every site has an `ssh` section

Every site, the gateway included, declares one. Its `keys` names operators
from the top level `operators` section, and the site authorises every key
those operators list. Operators are the people who run the machines; a
community admin who runs none of them is not declared here (see *Operators
and community admins*, under *Accounts*):

```yaml
operators:
  alice:
    name: Alice Example    # required: a first and a last name
    email: alice@example.org
    keys: |                # required: one whole .pub line per line
      ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice@example.org
      ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgIC alice@laptop
  bob:
    name: Bob Example
    email: bob@example.org
    keys: |
      ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB bob@example.org

sites:
  vm:
    roles: [gateway, witness]
    address: 10.44.0.3
    public_address: 203.0.113.10
    ssh:
      host: 203.0.113.10   # optional: defaults to public_address, then endpoint's host
      user: paisans-1a2b   # optional: defaults to paisans-<token>, written here by the first --execute
      port: 22             # optional: empty or 0 means 22
      keys: [alice, bob]   # required: the operators who may log in
```

| Key | Required | Refused when |
|-----|----------|--------------|
| `host` | only without `public_address` or `endpoint` | neither a hostname nor an IP address (so `user@host` is refused: the user has its own key) |
| `user` | no: empty means the deployment's own user, `paisans-<token>` | not a lowercase letter or underscore followed by lowercase letters, digits, underscores or hyphens, 32 characters at most; `root`; another deployment's `paisans-<token>` |
| `port` | no | outside 1 to 65535 |
| `keys` | yes | not a list; empty; an operator the `operators` section does not declare; an operator listed twice |

Each entry of `operators`:

| Key | Required | Refused when |
|-----|----------|--------------|
| the entry's name | | not a lowercase letter or digit followed by lowercase letters, digits, dots, underscores or hyphens, 32 characters at most |
| `name` | yes | not at least a first and a last name: split at its first space, either part is empty, Eg: `Alice` is refused and `Alice Example` is not |
| `email` | yes | not an email address alone, Eg: `alice@example.org` |
| `keys` | yes | no key; a line that is not an OpenSSH public key; a key with options (`from=`, `command=`, `restrict`); a type other than ed25519, ECDSA, a security key's `sk-` types or RSA of 3072 bits or more, so a certificate too; the same key twice, or under two operators, by fingerprint |

`host` defaults to `public_address`, and without that to the host in
`endpoint`, because on most sites they are the same address written twice.
`endpoint` comes last because it may name a router that forwards only
WireGuard's port; there the connection fails and nothing is changed. The user name is held to a conservative form because it
goes into commands and a file name on the host, and so is an operator's name,
which `site prepare` records on the host beside each of their keys so an
operator can trace a key there back to this file. Options are refused rather than carried: a line `site prepare` writes
and later compares has to mean the same thing everywhere, and a restricted key
is added to `authorized_keys` by hand, where `site prepare` leaves it alone.

`user` defaults to `paisans-<token>`, the prefix every name the deployment owns
on a host already carries, so a deployment that names no user logs in as one of
its own rather than as a user another deployment shares. The user must exist on
the host either way.

A host refuses that user until its owner has admitted the deployment, so a
command whose login as the default user is refused says the site is not
admitted there, with the `paisans site request --site <site>` line whose file
the owner admits it from, rather than that a key was not accepted. Any run with
`--execute` that logs in as the default writes it into `user`, whatever the
command, so the file names who logs in; a name already there is never
changed, and a write that fails is a warning, since the login it records has
worked. A run without `--execute` writes nothing: it notes what `--execute`
records, as it does for `arch`, because a dry run changes nothing.

Every command runs docker against the login user's own rootless daemon: its
first command on a site asks `id -u`, once a run, and every command after it
runs with `DOCKER_HOST=unix:///run/user/<uid>/docker.sock`, so docker never
reaches the host's system socket.

A single string in place of the section, `ssh: home-a.local`, is refused, and
the refusal says the section to write and its keys (`host`,
`user`, `port` and `keys`). A site's access has one way to write it (founder decision), so there is
nothing to read twice, and the string has nowhere to put the keys.

#### A host's owner admits a site from a request

An owner who does not have the deployment's configuration admits a site
from a request: `paisans site request --site <site> > <site>.admit` writes
it, and the owner admits the site from that file. The request holds what
admission needs and nothing secret: the deployment's id, the site's name
and its roles, sorted, the site's user, its SSH and WireGuard ports, its
mesh address and the mesh subnet, whether it has an endpoint, the grants
its roles need (the watchdog for a data site, ports 80 and 443 for a
gateway or a monitor running the toolkit's Caddy), the apps a monitor
behind the owner's web server hands it, and each operator's public keys.

Those keys get a shell on the owner's machine, so the request is only as
trustworthy as the way it travelled. `site request` prints a digest of it
on stderr: the first 80 bits of its SHA-256 as sixteen characters of
Crockford base32 in four groups, `xxxx-xxxx-xxxx-xxxx`. The owner is shown
the same digest of the file they were given and compares the two over a
channel they trust, such as a call. A digest covers what admission acts on
only if every byte it covers is read, so a request is refused unless it is
exactly the bytes `site request` writes for what it holds: a comment, an
anchor, a second document or rewritten line endings cannot carry something
the owner was not shown. Keys travel without their comments, which are free
text a forger could vary while searching for a request with a matching
digest.

A request is read as strictly as a configuration. An unknown version, a field
missing or unknown, a site name or a role the configuration would refuse, a
role listed twice or out of order, grants other than the ones its roles need,
apps handed to the owner's web server on a site with no monitor role, a user
name `ssh.user` would refuse, `root`, a `paisans-` name other than the
deployment's own, a key that is not a plain public key, and an address
outside the subnet are each refused, and so is a mesh subnet overlapping
`0.0.0.0/8`, loopback, link-local or multicast: the egress table accepts the
mesh subnet before it drops the owner's loopback and link-local ranges, so a
subnet over them would let the deployment reach what the table keeps it from.
So is one outside the private ranges `10.0.0.0/8`, `172.16.0.0/12` and
`192.168.0.0/16`: another deployment's table on a shared host drops these and
no other range a mesh may use. `site request` applies the same checks, so it
never writes a request the owner's side would refuse, and `validate` holds
the configuration to each rule it can check from the file, `ssh.user`
included, so a configuration it accepts gets a request. A refusal `site
request` can still reach names the configuration key to change beside the
request's field.

Admitting needs ufw installed: `site admit` never installs it. When ufw is
not active, admit enables it only with `--enable-ufw`, a yes at a
terminal, or `--yes` with `--site`, behind a timer that turns it off again unless a new SSH login gets
through, and only once nothing of the owner's that answers from off the host
would be blocked, or the owner has said it may be (see *SSH is never shut out* below, and *If SSH stops answering after
admit enables ufw* in
[docs/guides/host-owners.md](docs/guides/host-owners.md)). Ports 80 and 443
belong to one deployment per host. The egress table fences the deployment's
user off the owner's machine, within the limits *Egress keeps the deployment
off the owner's network* names. `paisans site admit` and
`paisans site evict` are in `docs/commands.md`.

#### Keys belong to operators, and sites name operators

An operator's keys are the same on every site they reach, so they are written
once, under the operator, and each site names the people who may log in. A key
written out per site is a key that gets rotated on some sites and missed on
others; written once, rotating it is one edit and a `site prepare` of each site
that names the operator.

**At least two operators should reach the identity provider.** One lost
workstation must not leave nobody able to run `deployment apply`, sign a configuration
version or reach the identity provider's hosts. `validate` refuses a deployment
with no operator whose keys are on every site that runs a `pocket-id` app
(`operators-none`), since nobody could run it, and warns about one
(`operators-fewer-than-two`). One is a warning rather than a refusal because a
deployment being rolled out, or a small community, may have one operator until
a second is found; the operator reconciler fails the operators heartbeat while
only one is declared, so the monitor keeps saying so (see *The operator
reconciler checks Pocket ID against the operators*). An operator counts only
with a key on each of those sites, since running the deployment means reaching
every one of them. A deployment with no `pocket-id` app is not held to it.

`email` is required so that every key in the file leads to somebody who can
say whether it is still theirs. `name` is required because the operator's
Pocket ID account is given it: apply splits it at its first space into the
account's first and last name (see *`deployment apply` makes the declared operators and hands each a login link*). The entry's own name is the operator's
Pocket ID username, which is fixed once the account exists. A key belongs to
one operator: two operators sharing a key cannot be told apart on a host, and
taking one of them off a site would leave the other's access resting on a key
nobody listed for them.

#### Public keys, not a path to a private key

`paisans-stack.yaml` is one file that every operator of the deployment shares and
commits. A private key is one operator's, on one operator's machine, at a path that is
different on everybody's. So the file lists **public** keys, every operator's,
which are safe to share and which are the same everywhere, and it never names a
private key or where one lives. The toolkit never reads a private key at all.

The same list is the record of who may log in: `site prepare` makes those keys
the login user's authorized keys (see *site prepare manages the login user's
authorized keys*). Giving an operator a site is adding their name to its
`ssh.keys` and preparing again; taking it away is removing the name and
preparing again.

#### How the operator's ssh uses them

Every command that reaches a site runs the operator's own `ssh` (see *Why ssh
is shelled out to and sops is not* in `docs/development.md`) as:

```
ssh -p <port> -o IdentitiesOnly=yes -o ConnectTimeout=10 -i <key-1.pub> -i <key-2.pub> <user>@<host> <command>
```

Each listed key is written to a file of its own, 0600, in a fresh 0700
temporary directory that is removed when the command returns. `-i` names a
**public** key file on purpose. OpenSSH documents exactly this: ssh(1) on `-i`
and ssh_config(5) on `IdentityFile` say a public key file may be given "to use
the corresponding private key that is loaded in ssh-agent(1) when the private
key file is not present locally", and ssh_config(5) adds that `IdentityFile`
"may be used in conjunction with `IdentitiesOnly` to select which identities in
an agent are offered during authentication" (OpenSSH 9.9p2's pages). So each
operator's ssh offers the listed keys, finds whichever one's private half is in
that operator's agent, and signs with it there.

`ConnectTimeout=10` bounds how long ssh waits for a site to answer. Without
it ssh uses "the default system TCP timeout" (ssh_config(5)), which is 75
seconds on macOS for a host that is switched off or a VM that has been
deleted, and a connection that never opened is tried three times (see *A
connection that never opened is tried again*): minutes, with nothing to show
for them, before a command could say the site is gone. A host that is up opens
its connection in well under a second. The timeout also covers the protocol
handshake, but not an operator's answer to ssh's question about a new host
key: an answer given after the timeout has passed still connects.

`IdentitiesOnly=yes` is what makes it a selection. Without it ssh also offers
every other key the agent holds, first, and a server that allows a handful of
attempts can refuse the operator before it reaches the right one. Several `-i`
are tried in order, so the toolkit does not need to know which operator is running
it.

What this asks of an operator: **the private key must be in their agent**
(`ssh-add -l` lists it), or named by an `IdentityFile` in their own
`~/.ssh/config`, which `IdentitiesOnly` still honours. On macOS the agent is
the one launchd starts on demand (`com.openssh.ssh-agent`, its socket in
`SSH_AUTH_SOCK`). The Keychain holds passphrases, not keys:
`ssh-add --apple-use-keychain` stores a key's passphrase there, and
`ssh-add --apple-load-keychain` adds keys to the agent using the stored ones
(ssh-add(1) on macOS), which is how a key gets back into the agent after a
login without typing anything. An agent that keeps keys elsewhere (a
hardware token's, a password manager's) works the same way, since all ssh sees
is the agent.

Everything else in `~/.ssh/config` still applies (`ProxyJump`, `known_hosts`,
`ServerAliveInterval`), except where the command line already says: `-p` and
the `user@` win over that file's `Port` and `User`, since ssh_config(5) takes
"the first obtained value" and command line options come first. `BatchMode` is
not set, so a first connection can still ask the operator to accept a host key,
which keeps host key checking on from the very first connection.

#### One connection per host, shared

Each command also carries `-o ControlMaster=auto -o ControlPath=<dir>/%C -o
ControlPersist=60`, on the `--ssh` route too. The first command to a host opens
a connection and the rest of the run's commands to it go over that one, rather
than each paying for its own handshake and authentication. `<dir>` is a fresh
0700 directory the process makes on first use, under the system temporary
directory or, where that path is too long for a unix socket (as on macOS),
under `/tmp`. Before the process exits, whatever the outcome, it asks each
shared connection to close (`ssh -O exit`) and removes the directory. These
options take precedence over any `ControlMaster`, `ControlPath` or
`ControlPersist` in `~/.ssh/config`.

A shared connection that has gone is not an error: ssh finds the socket dead,
removes it and connects afresh. A command whose shared connection drops while
it runs fails with ssh's status 255 and is not retried, since it may have run.

ssh asks on the terminal itself to accept a host key it does not know, so
every connection attempt to a host is made with the progress display held
until that host has answered once. A key whose passphrase ssh asks for on
every connection is the one prompt this does not cover: after the first
connection the spinner runs while ssh asks, and can draw over the question. A
key held by an agent (above) is never asked for.

#### Commands run in sh, whatever the login shell is

sshd hands the command ssh sends to the login user's shell, and every command
the toolkit builds is sh syntax. A login shell with other quoting rules misreads
it: fish fails on the nested quoting of `sudo sh -c '<command>'` with "quotes
are not balanced" before anything runs. So each command is sent base64 encoded,
as

```
sh -c 'eval "$(printf %s <base64> | base64 -d)"'
```

which holds no quote, backslash or space inside its single quotes, so sh, bash,
zsh, fish and csh all read it the same way. sh then decodes the command and
runs it, so its output, its exit status and its stdin are the command's own.
`--ssh` is sent the same way. The host needs `sh` and `base64` (GNU coreutils),
nothing more.

#### A connection that never opened is tried again

A connect timeout on a host that answers seconds later stopped the first real
`storage add` twice, half way through. So a command whose connection never
opened is tried again: three attempts in all, two and then four seconds apart,
with one line on stderr per retry naming the destination, shown only with
`--verbose`: a retry that succeeds is not news. After the third, the
error says the host could not be reached, and a caller never reads that as an
answer about the host (see *Never infer host state from a failed probe*). The
one exception is a run's classification of its sites, which tries each once
(see *A deployment with sites missing*).

Only that failure is retried, and it is recognised by both of two signs. ssh
"exits with the exit status of the remote command or with 255 if an error
occurred" (ssh(1)), so 255 alone cannot tell ssh's error from a remote command
that exited 255 itself; and its last line must be one of ssh's connection
errors (`Operation timed out`, `Connection timed out`, `Connection refused`,
`Connection reset`, `No route to host`, `kex_exchange_identification`, or the
pre authentication `Connection closed by <host> port <port>`). A session that
had opened before it dropped (`client_loop: ...`, or `Connection to <host>
closed by remote host`) is not retried, because the command may have run.

Only a connection that never opened is retried, because a command that ran
and failed is the host's answer, and running it again could repeat whatever it
did first. A connection that never opened ran nothing, so a retry cannot do
anything twice. File writes are retried too, safely, because each lands in a
temporary file that is then renamed. `ConnectionAttempts` in the operator's
`~/.ssh/config` is still honoured, and the retry is built in because a default
that depends on every operator's own file is not a default.

#### Never infer host state from a failed probe

A probe that never reached the host has not answered. Read as an answer, an
ssh timeout on the command that sets the stored layout aside in `storage add`
would say the aside name already exists from an earlier attempt, which nothing
checked. Every command that asks a host a question (`storage add`, `site add`,
`deployment apply`) follows one rule: when the probe's own
command fails to reach the host, the error says the host state could not be
read, carries ssh's message, and the command stops there.

What a probe concludes comes only from what the host printed. The set aside
script prints a marker when the layout is still in place and another when the
aside name is taken, and the message is built from those, never from the
failure alone. A failed read of the counts file, of the metadata directory, of
a node's ID, of HAProxy's statistics or of a mesh ping is an error rather than
"absent", "no layout", "does not answer", "lists too few sites" or "cannot
reach". And `site add` does not roll a stage back on an unreachable host: the
stage may well have worked, and a rollback would most likely fail to reach the
same host anyway. A probe that ran and failed is still an answer, and is read
as one (a ping with no reply, a Garage that refuses).

Reading a failed probe as "absent, so do it again" is safe only when doing it
again is harmless. Here it is not: counting objects again after a reset has stopped the nodes replaces the
numbers the provision gate compares against, and restarting HAProxy stops every
database app on the site.

#### `--ssh` replaces the whole section

Every command that reaches only one site's host takes `--ssh <destination>`,
the escape hatch for a route the section cannot describe. A command that
reaches several sites (`site add`, `preflight`, `storage add`, `deployment apply` without
`--site`) takes none, because one destination cannot describe several. When it
is given, it is passed to ssh
verbatim, exactly as before the section existed, and the section's user, host,
port and keys are **not** used for the connection. It replaces all of them
rather than one, so what is used is always either everything the file says or
exactly what the operator typed, never a blend. `site prepare` still manages
the section's user's authorized keys under `--ssh`, because those come from the
file, not from the connection.

#### A sudo that wants a password is asked for once, on a terminal

Every command that acts on a deployment logs in as the site's ssh user and
runs nothing through sudo but the WireGuard helper, which its one sudoers
line allows without a password. Three commands are the host owner's: `site
admit` and `site evict`, which change the host as root, and `site
deployments`, which reads the registry and every deployment's directories,
root's. With `--request` (or, for evict, `--deployment` and `--user`),
admit and evict run as root on the host itself,
under the owner's own `sudo`; reached from a workstation, with `--site` and
the owner's login in `--ssh`, they and `site deployments` run through sudo
(`site deployments`' `--sudo`, on by default). Before the first such
command, Paisans asks the host `sudo -n true`. `-n` makes sudo fail at
once instead of waiting for a password, so the answer is immediate.

**A host whose sudo needs no password** runs each command as
`sudo sh -c '<command>'`, and nobody is asked anything.

**A host whose sudo wants one** (sudo answers "a password is required") gets
it from the operator, once per destination per run, on the controlling
terminal:

```
sudo password for owner@203.0.113.10, host home-a-01:
```

The prompt names the destination ssh reached and the name the host gives
itself (`uname -n`). An address alone is often one the operator does not
recognise, and typing a root password needs certainty about which machine is
asking: the hostname says which machine answered. A transport that carries a
site's name opens the prompt with `site <name>`, as `paisans-stack.yaml` names
it; `site deployments` reads no configuration, so its prompt names none. The
hostname is left out when the host does not give one or gives anything beyond
letters, digits, dots, hyphens and underscores, because it is printed on the
operator's terminal and a host must not write terminal escapes into the prompt
for its own password.

The prompt reads `/dev/tty` with echo off, not stdin, because stdin may
already carry a value (`secrets set` reads one from it).

While the prompt waits, the progress display is held. The first sudo command
runs inside an open step, and that step's spinner redraws its line every tenth
of a second, which would erase the prompt and leave the operator watching a
spinner that waits on them without saying so. Once answered, right or wrong,
the prompt is erased, so the report reads on without it; a wrong password is
said by the error that follows.
The password is checked once, and every command after it runs as
`sudo -k -S -p '' sh -c '<fence>; <command>'`, with the password as the first
line of that ssh session's stdin, a fence line after it, and then whatever
stdin the command itself takes:

* `-S` reads the password from stdin, and `-p ''` prints no prompt into
  output the toolkit parses.
* `-k` ignores a cached credential, so sudo asks on every command where it
  asks at all.
* sudo reads the password up to the newline, one byte at a time, so it takes
  nothing that follows.
* Whether sudo reads that line at all is the host's to decide: a NOPASSWD
  rule matching the command, or a PAM module that authenticates without a
  password, lets the command through without reading it. The line would then
  be the top of the command's own input, and a command reading stdin would
  print it, write it into a file or run it. So the command never reads that
  stdin directly: the shell sudo starts first reads up to the fence, at most
  two lines, which is the fence alone when sudo took the password and the
  password and then the fence when it did not. Only what follows the fence
  reaches the command, whichever way the host went. The fence is random for
  each command, so nothing a command is sent can be mistaken for it.
* ssh runs with `-T`, whatever the operator's ssh config says, so the
  command has no terminal on the host: a terminal echoes what arrives on
  stdin back into the output, the password included.

The password is held in memory for the run and shared by every connection to
the same destination. It never appears on a command line, where `ps` on either
machine would show it, and never on disk. The next run asks again.

It is also registered as a secret the moment it is typed, and everything the
toolkit shows passes through one redaction that replaces each registered
secret with `[redacted]`: every line the progress display writes, the error a
command ends on, the transport's own errors, and the sheet `web-server show`
prints. A host's output is quoted to the operator in many places (a warning's
detail, a failure's cause under `--verbose`), and a host can print back what
it was sent, so leaving the password out is not left to each of them.

**What stops a run, and what it says:**

| The host | What happens |
|----------|--------------|
| wants a password, and there is no terminal (cron, CI) | refused before any command runs; the message says an unattended run needs a NOPASSWD sudoers rule for the ssh user |
| rejects the password typed | reported with sudo's own message, and not sent again in this run, since repeated failures can lock the account where the host counts them |
| refuses the user outright | sudo's own message; no password is asked for |
| does not answer the probe | the host could not be reached, as for any probe (see *Never infer host state from a failed probe*); nothing about sudo is concluded or kept |

`site evict` asks for the deployment's user name at a terminal unless `--yes`
types it, and `site admit --request` for the request's digest unless
`--digest` gives it, which `--yes` never does. `site admit --site` asks nothing once every grant is passed, `--ssh`
is given, and ufw is active or `--enable-ufw` is passed, with
`--enable-ufw-block-listed` where enabling it blocks a port that answers now, and `site
deployments` asks nothing, so an unattended run of either needs a NOPASSWD
sudoers rule for the login `--ssh` names. Whether to write one is a
decision about the host, made by whoever owns it, and Paisans never makes
it; an agent never runs admit or evict unattended,
whatever sudo allows (`docs/deployment-agent-rules.md`).

`site deployments` reports each of these as a sudo problem
(`apply.ErrSudo`), separately from a host that does not answer, so it
points at sudoers rather than at the network. `site deployments --sudo=false`
skips sudo entirely, and with it everything above.

### The host check: what is already on a host decides how much is touched

`deployment apply` removes the images it superseded and `prune` removes every anonymous
volume nothing mounted, and `site prepare`, `deployment apply`, `site add` and `prune`
all bind ports. That is right on a dedicated host and wrong on one where
somebody else runs a web server or a database. So before any of them changes
anything, in a dry run as well as with `--execute`, it runs the **host
check**, as the deployment's user, on the site it is about to change and
reports its class as one step:

```
home-a (ubuntu@home-a.local)
  ok   host check: clean
```

`shared` stands where `clean` does for a host that runs other services, and a
conflict ends the step as `FAIL` with each conflict in the error. `--verbose`
adds under the step what the check found, each marked `✓` (`ok` when piped):
the Docker and compose versions, each note, and each foreign resource
that makes the host shared, which reads `not paisans`:

```
home-a (ubuntu@home-a.local)
  ✓ host check: shared
      home-a (ubuntu@home-a.local) is shared
  ✓ docker: 29.5.0, compose 2.40.0 (docker-ce)
  ✓ *:8080/tcp held by a listener of another user's (uid 1001): not paisans
  ✓ shared: superseded images are kept, and prune removes only volumes labelled with this deployment's id, as everywhere
```

`storage rotate-key` and `storage add` run the check too, since they apply
files on the sites they change: rotate-key checks every site the app runs on, storage add every
Garage site, the gateway and every monitor site, all before the first change.
`site add`, `site remove` and `app remove` check every monitor site as well,
because each ends by applying the monitor's stack with a new seed.

The check has three parts, in `internal/hostcheck`:

* **Claims**, from `paisans-stack.yaml` alone: every port the site will bind, with
  its protocol, its address and the key that makes the site bind it
  (`render.SiteListeners`, the list the renderer itself uses, so the check
  cannot drift from what is deployed), the `psns-<token>` interface, the mesh
  subnet as a route, and every subnet the site's stacks pin
  (`render.SiteNetworks`, today a monitor's network behind the operator's own
  web server).
* **Inventory**, read only, one command per fact, run as the deployment's
  user and in the C locale, because the parsers read dpkg's and ss's English
  words. It needs no root: it asks `id -u` and `/etc/subuid` for the user's
  UID and subordinate range, then Docker's version, the docker CLI's, the
  compose plugin's and Docker's package, every container of the user's own
  daemon with its compose project, PID and published ports, volumes,
  networks and their subnets, `ss -Hltnue`, whose `-e` names each listener's
  owner by UID and whose lack of `-p` leaves its process unnamed, `ip -o
  link`, `ip -j route`, the site files in `/srv/paisans/<token>/caddy.d`, and
  whether this deployment's manifest, `/srv/paisans/<token>/.paisans-manifest.json`,
  exists and records `psns-<token>.conf`. It does not read the firewall,
  which is `site admit`'s.
* **Classification.** A container or network is the toolkit's when it
  carries the label `community.paisans.deployment` with this deployment's id,
  the same rule `prune` follows. Another deployment's containers on the same
  host are foreign like anyone else's, whatever their names. A listener is
  told by its owner's UID. It is the toolkit's when its UID is the
  deployment user's own or in the user's subordinate range, which is every
  process its rootless Docker runs, the port forwarder included, or when it
  is the kernel's WireGuard socket, on the port the site is dialled on, for a
  `psns-<token>` the manifest records. A listener of root's, of a system
  user's (a UID below 1000, as the distribution's own daemons run), or on
  loopback is the base system and counts as neither, `sshd`,
  `systemd-resolved` and the rest among them. A listener of any other user's
  is foreign, and so makes the host shared.

| Class | When | What the command does |
|---|---|---|
| clean | nothing foreign; an empty Docker install is clean | proceeds; nothing in this check reads or changes the firewall, which is `site admit`'s |
| shared | something foreign, holding nothing the site claims | proceeds, touching only what is the toolkit's (below) |
| conflict | something foreign holds a claim | refuses, changes nothing, one line per conflict |

A listener conflicts when the ports and protocols match and either side binds
every address or both bind the same one, which is exactly when the second
bind fails. That holds for a base system listener too, root's, a system
user's or a loopback one: a loopback Postgres does not make a host shared,
and leaves the toolkit's bind on the mesh address free, while one on every
address stops it. A foreign container's published ports count whether or not
anything listens, because Docker without its userland proxy publishes with no
listener, and a stopped container binds again when it starts. A foreign
Docker network whose subnet overlaps the mesh conflicts, because containers
would be handed the mesh's addresses. A Docker network over a subnet the site
pins conflicts unless it belongs to the stack that pins it, even when it is
another of this deployment's stacks (one an app left behind when it was
renamed), because compose would refuse to create the pinned network halfway
through an apply; when the holder is this deployment's, the conflict names
the `docker compose -p <project> down` that clears it. A route conflicts when
it equals the mesh subnet or a pinned one, or lies inside it, because the
kernel picks the longest matching prefix and it would take that traffic. A
route broader than the mesh (a
provider's 10.0.0.0/8 private network) is shorter than the one `psns-<token>`
adds, so the mesh still wins; it is printed as a note. A `psns-<token>` no
manifest records conflicts, and so does the WireGuard socket on its listen
port: the interface carries the deployment's name, but nothing says what it
runs. The line names the deployment's WireGuard helper's `down`, which takes
it away.

A conflict line names the resource, the `paisans-stack.yaml` key that claims it, and
what holds it (a container and its compose project, or a process and its PID).
**There is no override.** The operator moves the other service or changes the
configuration, because the claim cannot be taken while something else holds
it: the bind fails all the same, only later and halfway through a change.

On a **shared** host:

* **The firewall is the owner's.** `site admit` adds the deployment's own
  `paisans-<token>:` rules and never sets ufw's default policy, so nothing
  in this check, or in any command that runs as the deployment's user, reads
  or changes it.
* **No image is removed**, as with `deployment apply --keep-images`, and nothing is
  swept, because an image the toolkit renders (`caddy`, `postgres`) may be
  what a foreign project runs.
* **Docker is the deployment's own**: the check reads the user's rootless
  daemon, and the owner's Docker is never touched (see *Docker is judged by
  its version, not its source*).

The class is computed on every run and never stored: a host becomes shared
the day someone installs something beside the deployment. `deployment apply`'s manifest
gate still refuses to overwrite a file it has no record of; the host check
runs before it and covers what a manifest cannot see: ports, interfaces and
other users' listeners.

**The gateway answers as itself.** The deployment's user cannot read the
host's ruleset or another user's sockets, so a port taken after admission, and
above all one taken by a DNAT rule with no listener, is invisible to the
check. The gateway's Caddy serves `/.well-known/paisans/<token>`, answering
with the deployment's token, and `deployment apply` ends the gateway's step by fetching
it through the community's public address on 443 (and on 80, expecting the
redirect). Any other answer is a conflict, a 443 answering in anything but
TLS among them: the step fails with "443 on <site> answers, but not as this
deployment", whatever the host check saw. A gateway
with no `public_address` or hostname to ask for warns "not checked", and one
that does not answer fails with an account of hairpin NAT: a gateway at home,
reached from its own network, needs the router to send its public address back
inside.

#### Docker is judged by its version, not its source

Each deployment runs its own rootless Docker daemon as its own user, beside
whatever Docker the host's owner runs, and `site admit` sets it up. What the
Paisans depends on is what that daemon and compose can do, and that is a
version, so admit refuses, on any host and whatever its class:

* **An Engine older than 29.5.** From 29.5 a rootless container with host
  networking shares the host's network namespace
  ([Docker: rootless troubleshooting](https://docs.docker.com/engine/security/rootless/troubleshoot/)).
  Every infrastructure container is `network_mode: host`, bound to the
  site's mesh address, so on an older Engine no other site reaches it. The
  refusal names the version found and leaves the upgrade to the owner, since
  upgrading Docker restarts every container the owner runs.
* **A Docker that cannot run rootless**: one that is not Docker's own
  `docker-ce` and has no `dockerd-rootless-setuptool.sh`, such as Ubuntu's
  `docker.io`. The rootless extras come from Docker's repository, which
  installs beside `docker-ce` only, and admit never replaces packages the
  owner chose.
* **A Docker snap, however new.** Its confinement keeps Docker from bind
  mounting under `/srv`, where every stack keeps its data, and snapd refreshes
  it, restarting the daemon and every container, on snapd's schedule rather
  than the owner's. A version number reveals neither.

The host check, which `site prepare`, `deployment apply` and the others run as the
deployment's user, reads the user's own daemon and refuses one older than
engine 25.0.0 or compose 2.24.0, the first releases with
`healthcheck.start_interval`, which the rendered stacks use; admit has
already held it to 29.5.
[docs/specs/2026-10-11-docker-version-gate.md](docs/specs/2026-10-11-docker-version-gate.md)
lists every Docker feature Paisans depends on, with the release that
introduced it. Nothing is upgraded or removed: either stops every container
the owner runs, and that is a decision for whoever started them.

### `site prepare` keeps what is the deployment's user's

`deployment apply` assumes a host whose owner has admitted the deployment (see *A host's
owner admits a site from a request*): the deployment's own user with its
rootless Docker, the WireGuard tools, the firewall's rules, and on a data
site a `/dev/watchdog` for Patroni. Those are root's, so `site admit` makes
them, once, and `site prepare` makes none of them. What is left is the user's
own, and **none of that is done by hand either**. A step an operator performs
from a wiki page is a step done differently on every host, and the first time
it matters is a rebuild at the worst possible moment. So it is a command,
`site prepare`. How to run it, and its flags, are under *`paisans site
prepare`* in [docs/commands.md](docs/commands.md).

It reaches the host through the site's `ssh` section with the operator's own
ssh, exactly as `deployment apply` does, and so runs as the deployment's user: it
installs nothing and writes nothing that is root's. It checks the admission
record against the configuration first, refusing on any difference with the
request line the owner needs; reads the watchdog `site admit` set up; and
keeps the user's `daemon.json`, the configuration store with its units, and
the user's authorized keys. It probes read only, plans only what is missing,
and changes nothing without `--execute`; a prepared host plans nothing, so
re-running it after a failure resumes rather than restarts. It is not part of
`deployment apply --site` for the same reason `storage init` is not: the user's keys,
store and daemon configuration are host state that no rendered manifest
describes, so they are probed rather than compared. `deployment apply` without `--site`
runs each of them as a command of its own (see *`paisans deployment apply` with no
`--site` converges the whole deployment*).

#### Operating systems are profiles

Everything that depends on the operating system lives in a **host profile**,
which `site admit` and `site prepare` choose alike,
chosen from `/etc/os-release` by `ID` and `VERSION_ID` together. A profile
answers these questions: how to install the packages, how to enable services,
how to load a kernel module now and at boot, how to drive the firewall, how to
run the user's configuration store units, and how to hold Docker's packages. Its shell lives as templates in a directory under
`internal/hostprep/profiles/`. Adding Debian or Fedora is adding a directory
and a registration, not editing the command.

`ubuntu 22.04`, `ubuntu 24.04` and `ubuntu 26.04` ship, and share the templates in
`profiles/ubuntu/`: what differs between them, such as the codename Docker's
apt source names, is read from the host's `/etc/os-release`. A profile matches
the release as well as `ID`, so a release nobody has checked the templates
against is refused rather than written to as if it were another. A host no
profile matches is refused, naming what is supported:

```
debian 12 is not supported. Use one of the following:
ubuntu 22.04
ubuntu 24.04
ubuntu 26.04
```

#### Docker comes from Docker's repository when it is absent

Where Docker is there, admit leaves it as it is and adds only the rootless
extras beside it, pinned to the version installed so that apt has nothing to
upgrade; it refuses a Docker that cannot run rootless and an Engine older
than 29.5, rather than upgrading or replacing either (see *Docker is judged
by its version, not its source*).

Where Docker is absent, `site admit` installs Docker Engine and the compose
plugin from Docker's own apt repository, as
[Docker's install page](https://docs.docker.com/engine/install/ubuntu/)
describes. An install needs one source, and that one gives every host the
toolkit admits the same engine from the same source, whenever it was admitted.
Docker's system daemons are kept from starting while its packages install and
are then disabled, so only the deployments' rootless daemons run. What admit
installed is listed in `/etc/paisans/installed`, root's and mode 644. `site
evict` keeps the packages and that list when the last deployment goes, so a
later admit on the host, of any deployment, knows the system daemons as its
own and leaves them disabled. One that has run since, with no admission
record to say admit disabled it, may be running the owner's containers, so
admit refuses it before changing anything and names both ways on: remove
its lines from the list to keep it as the owner's, or disable it, remove
Docker's network store under its data-root, reboot, and admit again. The
data-root is read from `/etc/docker/daemon.json` written as `"data-root":
"/absolute/path"` on one line; one admit cannot read may hold a network
store, so the daemon is taken to have run. containerd has run when its root
directory is there, which it creates as it first starts: `/var/lib/containerd`,
or the `root` `/etc/containerd/config.toml` sets on one line as `root =
"/absolute/path"`. A
daemon admit installed that never ran, enabled by its package and held
while it installed, is disabled; its networks are read as none, since it
never made any. A package an admit killed part way left on the list and on
disk is admit's: the next admit installs it again and disables its units
with Docker's, unless one has run.

Where Docker is absent and the host has packages Docker's page lists as
conflicting (`containerd`, `runc` and the rest), they are **refused, not
removed**: installing Docker's packages would remove them, and on a host
already running containers from them that stops those containers, which is a
decision for whoever started them. The refusal names the packages found, and
leaves removing them to the owner. A unit of a package admit is about to
install that is already enabled or running, such as a containerd the owner
installed from another source, is refused the same way: the disable after
the install would stop it. When its package is on disk but not configured,
the refusal says to finish that install or purge the package. Every `apt-get` runs non-interactively.

#### The user's Docker starts after the mesh interface

Every app publishes its port on its site's mesh address, which exists only once
`wg-quick@psns-<token>` has brought the interface up. If the user's Docker
starts first at boot, those containers fail with `cannot assign requested
address`, and Docker does not retry a container that failed while setting up
its network, so the site comes back with its app stacks down. `site admit`
therefore writes a systemd drop-in for the user's manager,
`user@<uid>.service`, with `Wants=` and `After=wg-quick@psns-<token>.service`,
so the manager, its Docker daemon and its containers start after the mesh. A
second drop-in on the mesh unit skips it, rather than failing it, until `deployment apply`
writes its conf, so a fresh host shows no failed unit. A manager running when
admit runs is not restarted: it takes the order at its next start. Each
deployment on a host has its own, and `site evict` removes them.

#### The firewall follows the admission

`site admit` gives the deployment these inbound rules, and no others, from the
request alone; the deployment's user cannot change them:

| Rule | Sites |
|------|-------|
| the site's `ssh.port`, 22 unless declared | every site |
| the port the site is dialled on, UDP, WireGuard | sites with a `public_address` or an `endpoint` (see *Where the mesh dials a site*) |
| everything arriving on `psns-<token>` | every site |
| 80/tcp, 443/tcp, 443/udp | sites admitted with `--public-ports`: a gateway, or a monitor in `managed_by: paisans` |

Nothing is listed per site. A site that gains the gateway role needs the
`--public-ports` grant, which is the owner's to give, so the change is another
`site admit` for it (see *A host's owner admits a site from a request*). A
monitor behind the operator's own web server opens neither: that server holds
both, and its rules are the operator's. No rule names a `br-+` bridge, because
a rootless container's traffic to the mesh address leaves through the user's
own process, not a host bridge.

#### site prepare gives each site a configuration store

A site cannot keep signed copies of the configuration (see *Every site keeps
signed copies of the configuration*) until something exists on it to keep them
in, and `deployment apply` publishes a version before it changes anything else, so the
store has to exist first. `site prepare` therefore plans, under *configuration
store* in its plan, only what the host lacks, all of it the deployment user's
and under `/srv/paisans/<token>/.paisans/`: the bare repository at
`config/<token>.git`, made only where its `HEAD` is absent so that a re-run
never touches versions already there; the sync script at `bin/config-sync.sh`;
a file listing every other site's mesh address, which the sync reads; and the
read-only daemon unit and the sync timer, `paisans-<token>-config-daemon` and
`paisans-<token>-config-sync`, units of the user's own systemd manager under
linger, named for the token so `site remove` finds them. `git` is `site
admit`'s. A site added later gets the peers it lacks by a `site prepare` on the
existing sites, which rewrites the list.

#### site admit owns its rules, and only its rules

Every rule `site admit` adds carries a ufw comment, `paisans-<token>: <why>`,
and that comment is the whole of ownership. A rule another deployment on the
host added carries that deployment's token, and is as foreign as an
owner's. A later admit compares what the request derives with what `ufw show
added` prints, and decides per rule.

The plan marks its lines the way `deployment apply`'s dry run does: `○` and the step's
title for a change still to make, its full sentence under it with `-v`; `✓`
for what is already there, shown with `-v`; `!` for a warning. Piped output
says `todo`, `ok` and `WARN` instead.

| ufw holds | The plan says | What happens |
|-----------|---------------|--------------|
| nothing matching | `○ allow <rule>` | added, with the comment |
| the rule, with the comment | `✓` | nothing |
| the rule, with the comment, another action | `○ allow <rule>` | re-added as an allow, replacing the hand edit |
| the rule, with no comment | `✓ …: without the paisans comment` | nothing; it is the owner's, reported and left alone |
| the rule, with someone else's comment | `✓ …: not paisans` | nothing; it satisfies the rule |
| the same traffic, another action (`limit`, `deny`) | `!` | nothing; a `deny` or `reject` on SSH is refused |
| a commented rule nothing derives any more | `○ remove ufw rule <rule>` | deleted, last |
| any uncommented or foreign rule | `✓ …: not paisans` if it bears on a derived rule | nothing, ever |

A commented rule nothing derives is removed, so a site admitted again without
`--public-ports` stops serving 80 and 443 without someone having to notice.
Every other rule stays, so the rule an owner added for their monitoring agent
is never deleted. The comment separates the two without a state file that could drift
from the host.

Rules are matched the way ufw matches them, on everything except action and
comment. That is not a choice: ufw keeps one rule per match and **adding a rule
that differs only in comment or action replaces the existing one in place**
(ufw 0.36.2, the version Ubuntu 24.04 ships: `UFWRule.match` in `common.py`,
`set_rule` in `backend_iptables.py`; Ubuntu 22.04's 0.36.1 prints the same
`ufw show added` and `ufw status numbered` for every rule shape Paisans
adds). So adding ours over a rule someone else commented would rewrite their
comment, and adding an allow over their `limit` would loosen it. Both are left
alone.

An uncommented rule is the owner's, whether or not it matches a derived rule:
it is reported and left alone, and it stays until removed by hand.

**SSH is never removed**, not even an allow `site admit` added itself. With
incoming denied by default, that deletion drops the next connection, every
later command arrives over it, and recovering takes a console. A
stale SSH allow costs almost nothing; a wrong removal costs the host. A `deny`
or `reject` on the SSH port is refused, whatever the port is.

**Moving `ssh.port` adds the new allow and keeps the old one.** `site admit`
does not change the port sshd listens on, and it cannot tell whether sshd
already listens on the new one; if it does not, the old allow is the only way
back in. The old allow is recognised by its comment (`paisans-<token>: ssh, the
bootstrap route`), and deleting it is left to the owner, so the plan warns:

```
  ○ allow 2222/tcp
  ! firewall: old SSH allow on 22 kept; delete it yourself once SSH on 2222 works
```

Removals run after every addition, so a rule is only taken away once
everything that replaces it is in place.

The mesh is let in whole, by interface, rather than port by port. etcd,
Patroni, Garage and HAProxy all listen on the mesh address, and a new mesh
service would otherwise be one more rule to remember on every site.

**SSH is never shut out.** Admit never sets ufw's default policy. When ufw is
not active and the owner passes `--enable-ufw`, every allow, SSH first, is in
place before `ufw --force enable`, which does not stop to ask whether to
disrupt existing connections, and a timer turns ufw off again unless a new SSH
login gets through, which `--yes` never confirms (see `paisans site admit` in
[docs/commands.md](docs/commands.md)).

**Enabling ufw never silently blocks the owner's services.** Before it enables
ufw, admit reads every listener off loopback, TCP and UDP, and every port the
owner's Docker publishes or a NAT rule receives. A port that neither sshd, the
deployment's own rules nor a rule of the owner's allows stops admit, which
lists it with what serves it and the line that keeps it reachable: `sudo
ufw allow` for a listener, `sudo ufw route allow` to the translated address
for what NAT receives, which ufw decides as routed traffic. The system
Docker's publishes on its iptables backend stay reachable, its rules ahead
of ufw's. `--enable-ufw-block-listed`, or a second yes at a terminal, which
`--yes` never gives, enables ufw over them, and the plan names each.

**Nothing depends on the firewall for an app's port.** A rootless daemon's
published port is a listener of its port forwarder, under the deployment user's
UID, so ufw's rules apply to it. The renderer still publishes every app's port
on the site's mesh address rather than on every interface; a monitor behind the
operator's own web server publishes on its site's `listen` instead, which
`validate` refuses on a public address. The mesh address is admitted whole by
`allow in on psns-<token>`, so the binding is what keeps an app from being
reached on any other address.

#### Egress keeps the deployment off the owner's network

Every infrastructure container shares the host's network namespace, so
without a rule it reaches the owner's services bound to `127.0.0.1`, and the
owner's LAN. `site admit` loads an nftables table, `inet paisans_<token>`,
from `paisans-<token>-egress.service`, which is ordered before the user's
manager, so no container starts unfiltered at boot. Its `output` chain
matches the user's UID and its whole subordinate range, since a container's
users map into that range and the UID alone would miss them. A table that
changed is written beside its file, checked with `nft -c`, and only then
moved into place, and loaded in one transaction, so a table nft refuses
never becomes the one the next boot loads.

The chain opens with two things before the rules below. It accepts the
replies to flows someone else opened (`ct direction reply` with bit 27 of the
conntrack mark clear), TCP on any port, since a TCP reply reaches only its
own socket's peer; and UDP only from port 443, with the public ports grant,
for HTTP/3. Then it sets bit 27 on every flow the user opens, before any
accept or drop, so a flow the user opened under an accept never counts as a
reply. The rules, in order:

1. Accept the mesh subnet.
2. Accept port 53, TCP and UDP, to the host's resolvers, read at admit time
   from `/etc/resolv.conf` and, under systemd-resolved, from the stub's
   upstream list.
3. Drop `127.0.0.0/8`, `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`,
   `169.254.0.0/16`, `100.64.0.0/10`, `::1`, `fc00::/7` and `fe80::/10`, and
   NAT64's translations of the same IPv4 ranges, and the local-use prefix
   `64:ff9b:1::/48` whole, so a DNS64 host does not open the owner's LAN
   through the translator.
4. On a gateway (a site granted `--public-ports`, or whose `web_server` is
   external), accept TCP 80 and 443 to the host's own addresses, and the
   answers its own 80 and 443 send back.
5. Drop every address of the host itself, its broadcast and its multicast.
   This keeps the deployment off the owner's services bound to `0.0.0.0` or
   `::`, and off mDNS, SSDP and broadcast UDP on the LAN.
6. Drop the host's on-link IPv6 prefixes, read from the routing tables at
   admit time: a home LAN's IPv6 is global address space, so rule 3 does not
   cover it.

The order is load-bearing. Rule 1 comes first so the mesh, which the
deployment's services need, is never caught by the drops. Rule 4 follows
rule 3, since loopback addresses are local too, and precedes rule 5, which
would drop the gateway's own ports, and rule 6, since the host's own IPv6
address lies in an on-link prefix.

Rule 3 drops the deployment's dials to its own loopback too, so the
deployment stays off loopback entirely. Every listener the toolkit renders
for etcd, Patroni, Postgres, HAProxy and Garage binds the site's mesh address
alone, and every command `deployment apply`, `doctor`, `site add`, `site remove`,
`storage add` and `secrets rotate` run on a host dials a mesh address, which
rule 1 accepts. Caddy serves its admin API on a unix socket inside its
container, where `caddy reload` run in that container reaches it. A test
fails any rendered file for a host-networked container, and any string in the
CLI's code, that names loopback without a reviewed reason.

WireGuard's UDP is sent by the kernel, not by the user, so the chain above
never sees it, and the user writes each peer's endpoint. The WireGuard helper
marks the socket of every conf it installs with a mark of its own for the
deployment, and a second chain, `mesh`, matches that mark after the
endpoint's name is resolved. It drops loopback, link-local, multicast, the
host's own addresses, and the private and shared ranges, as above, and the
host's on-link IPv6 prefixes. The public endpoints a mesh normally dials
pass. The mark's bits overlap Kubernetes's, so a host running Kubernetes is
outside what admit supports.

`--mesh-lan` adds an accept in the `mesh` chain for the private prefix of the
host's own LAN, no wider than a /16, so a deployment's sites can dial each
other there. The plan says plainly that the deployment can then send UDP to
that LAN. Without it, sites on one LAN dial each other's public address,
which needs a router that loops it back.

**Known limits.**

**An owner service bound to `0.0.0.0` or `::` is reachable on the mesh
address**, by the deployment and by its other sites, since rule 1 accepts the
mesh subnet. Owners should bind admin services to `127.0.0.1` or to their
LAN address.

**The deployment's user may listen on any address and port no owner's
listener holds**, and answer whoever connects. An owner's client that means a
service which is down, or bound to `127.0.0.1` while the client tries `[::1]`,
can reach the deployment's listener instead. No table can tell such a client
from a visitor.

**Routes the table does not read.** Only on-link IPv6 routes are read, so an
on-link public IPv4 subnet, or an owner's VPN that routes non-private IPv4
ranges, is outside rule 3, and the deployment reaches it. Rule 1 names no
interface: admit refuses a mesh subnet that overlaps a route at admit time,
but a route added later on another interface would let the deployment reach
that network through it.

**Flows opened while the table is absent.** A reload of `nftables.service`
flushes the ruleset and loads the table again, and a flow the user opens in
between carries no mark. Its replies stay acceptable for as long as the user
keeps it alive, which matters only for UDP from 443 with the public ports
grant.

A deployment that needs something on the owner's network asks the owner.
There is no supported way to open the table to one service: an accept in
another table does not override its drop, and a rule added to it is gone at
the next load from its file (boot, an nftables reload or restart, an admit
that reloads it). The owner serves it on the host's mesh address, which rule
1 accepts, or at a public address on another machine. The owner's view of all this is
*What the deployment can reach* in
[docs/guides/host-owners.md](docs/guides/host-owners.md).

#### site prepare manages the login user's authorized keys

`site admit` first writes every key of every operator in `ssh.keys` as an
authorized key of the login user (`ssh.user`, or `paisans-<token>` when it is
left out), and `site prepare`, run as that user, maintains them from then on,
in `~<user>/.ssh/authorized_keys`. When the directory or the file is missing it
is created, 0700 and 0600, owned by the user. A user the host lacks is refused
by name: creating users is `site admit`'s. The user is this deployment's
alone, so no other deployment's keys share the file, and adding or removing a
co-operator is an operator's change that needs no owner.

`authorized_keys` may still hold keys someone else put there: the provider's,
the owner's own login, a restricted key added by hand. So `site prepare` adds
only listed keys, and deletes a line only when **it wrote that line itself**.
A listed key that was already there is never recorded and so never removed: it
is often the key the operator logs in with, and nothing on the host says
whether another way in exists.

The deployment records its claims in a sidecar,
`/srv/paisans/<token>/.paisans/authorized_keys.owned`, the user's, 0600, in its
mode 700 state directory, one line per key:

```
<mark> <fingerprint> <name>
```

| Mark | Meaning |
|------|---------|
| `added` | `site admit` or `site prepare` appended the line to `authorized_keys` |

Only an `added` line is one of this deployment's claims; a line of any other shape is no claim. Keys are
compared by fingerprint, so a key whose comment was changed is still the same
key, and the deployment keeps its own name for it.

For a listed key:

| `authorized_keys` holds | The sidecar | The plan says | What happens |
|-------------------------|-------------|---------------|--------------|
| nothing for the key | | `○ authorize ssh key` | appended verbatim, recorded `added` |
| the key | lists it | `✓` | nothing; `○ rename ssh key` if the key now belongs to a different operator |
| the key | does not list it | `✓` | nothing; not recorded, so never removed |
| the key, only with options | | `✓ …: not paisans` | nothing; adding the plain key would undo the restriction |

For a key the sidecar lists that `ssh.keys` no longer does:

| `authorized_keys` holds | Mark | The plan says | What happens |
|-------------------------|------|---------------|--------------|
| nothing for the key | `added` | `○ forget ssh key` | dropped from the sidecar |
| the key | `added` | `○ remove ssh key` | that exact line deleted, last of all |

Any key the sidecar does not list is never
mentioned and never touched. With `-v`, the keys' part of a plan reads:

```
  ○ authorize ssh key
      ssh: authorize key SHA256:RXm/ruZ0eTzRXKwi1AQEDynB0VgHQ2ac9KPSFdf/YnA (bob) for the deployment's user
  ○ remove ssh key
      ssh: remove key SHA256:baqJQcVDEweKmw1OiZxGooCG2MGxYtwsQQzzOstxmiA (carol) from /home/paisans-f2a9/.ssh/authorized_keys, which site prepare or site admit added and ssh.keys no longer lists
  ✓ ssh: key SHA256:kmYcvdi2GkPeWxB6XLjrZB8JHsy2Hm8luHMFp9GMvqk (alice) authorized for the deployment's user, not by site prepare, which never removes it
```

To trace a key on the host back to its operator, take its fingerprint with
`ssh-keygen -lf` and search the sidecar:

```
grep -H SHA256:RXm/ruZ0eTzRXKwi1AQEDynB0VgHQ2ac9KPSFdf/YnA /srv/paisans/f2a9/.paisans/authorized_keys.owned
```

**Ownership lives in the sidecar, not in the key's comment.** The comment is
how an operator recognises a key (`alice@laptop`), and leaving it alone keeps
what they see in the one place they look. Each step rewrites the sidecar along
with its change, so a stopped run resumes: a key appended but not yet recorded
stays unrecorded, and so is never removed, and a sidecar entry whose key
someone already deleted is forgotten, so a copy they add by hand later is never
taken for `site prepare`'s.

**One lock, and a removal decides again inside it.** Every write to the
sidecar or to `authorized_keys` runs under the deployment's one lock,
`/srv/paisans/<token>/.paisans/lock`, taken with `flock`, the same lock every
record in that directory is written under, so two commands of the deployment
never interleave their writes.

**Nothing is removed until everything is in place.** Removals run after every
addition, and a removal goes through a temporary file given the original's
mode and renamed over it, so sshd never reads a half written file. A plan that
would leave the user with none of the listed keys is refused. The key the
current connection used is not removed either, by construction: the transport
offers only listed keys, so the session's key is a listed one, unless `--ssh`
or the operator's own `IdentityFile` chose another.

**The review is the check on whose keys it adds.** `paisans-stack.yaml` is one
file every operator of a deployment edits, and whoever runs `site prepare` runs
it with their own access, so any operator can get a key of theirs made an
authorized key of the login user through someone else's run. That is a shell as
the deployment's user, with its rootless Docker, and nothing the owner's
machine has beyond it: the user is unprivileged and fenced off the owner's
machine (see *A host's owner admits a site from a request*). The review (see
*`deployment apply` and `site prepare` review what `paisans-stack.yaml` grants*) shows
every key a run would add, with its operator and fingerprint, and an
`--execute` run asks for `approve` at a terminal before anything changes, or
prints each key `--yes` approves.

#### The watchdog

On a data site, `site admit --watchdog` gives the deployment's user the
device the site's `watchdog` mode asks for, and `site prepare` reads that it
is the user's, or a UID in the user's subordinate range once Patroni's
container has taken it, as Patroni needs; *A data site's watchdog is declared, not
assumed* has the modes. It reports which driver it found, from
`/sys/class/watchdog/*/identity`.

On Ubuntu, `site admit` persists `softdog` with a small systemd unit that runs
`modprobe softdog` before `systemd-logind` starts the lingering users'
managers, whose Docker starts Patroni, not with a line in
`/etc/modules-load.d`. That would be the obvious place and it does not work:
Ubuntu's kernel package blacklists the watchdog drivers, `softdog` among them,
and `systemd-modules-load` honours the blacklist
([Launchpad bug 1535840](https://bugs.launchpad.net/bugs/1535840)),
so the line would be skipped at every boot while looking correct. `modprobe`
named on a command line does not apply the blacklist. This is the kind of
difference a profile exists to hold: a distribution that does not blacklist
`softdog` can use `modules-load.d`.

### `deployment apply` and `site prepare` review what `paisans-stack.yaml` grants

`paisans-stack.yaml` is one file every operator of a deployment edits and commits, and
whoever runs `deployment apply` runs it with their own access. So any operator can grant
themselves a host through someone else's run: by adding their name to a
site's `ssh.keys`, or replacing the key under an operator it already names, which
makes `site prepare` install their key for the deployment's user; or
by pointing `apps.<name>.images` at an image they control, which `deployment apply` pulls
and runs with whatever the template mounts. A plan line saying `○ authorize
ssh key` is easy to miss, and with `--execute` it is not shown at all. So both
commands show what changes and ask before it does. Before its review,
`deployment apply` shows how the configuration differs from the sites' newest
version: `paisans-stack.yaml` as a diff, the secrets by key only.
`docs/specs/2026-10-11-config-review.md` is the approved specification.

**The grant** is the part of the configuration that decides who may log in to
a host and what code runs on it. It is computed from the configuration and the
render, never from the host, so nothing on the host chooses what is reviewed:

| Part | Holds | Comes from |
|---|---|---|
| login | `ssh.user`, or `paisans-<token>` when it is left out | the site's `ssh` section |
| keys | per key: its operator, its SHA256 fingerprint and its comment | every key of every operator in the site's `ssh.keys` |
| images | per service: its stack, its name, the image reference as the compose file writes it, and whether it is a foundation image (see below) | every `compose.yaml` the render writes for the site, the infrastructure's (which runs with `network_mode: host`) and the apps' |
| blueprints | per app whose blueprint a catalog provides, the core catalog included: the blueprint, its tag and commit, and the SHA-256 of its files | the catalog blueprints of the apps on the site: a catalog's author wrote those templates, so a moved pin chooses code as an image does, and a new core catalog release moves the core blueprints of every deployment that does not pin core itself |

A key is the same key when its fingerprint is, so a new comment or a new operator
for it is a change of that key. An image is the same when its reference is,
which is why `validate` refuses a floating tag for an app. An app's settings,
`config` and `smtp` are not reviewed: they configure an image the review
already covers, and choose no code. A field added later that does choose code
(a command, a hook, a mount) joins the review in the same change.

**A catalog blueprint's compose file is held to what a stack may ask of its
host.** The render refuses one that sets `privileged`, `cap_add`, `devices`,
`cgroup_parent` or a `security_opt` entry other than `no-new-privileges:true` or
`no-new-privileges=true`; that shares the host's network, process,
IPC, UTS, user or cgroup namespace, or another container's; or that mounts the
Docker socket or any host path outside the stack's own directory. Each is a way
past the stack, into the deployment's user and its rootless daemon. The blueprints built
into the toolkit are reviewed with its code instead, which is how the uptime
monitor keeps the capability its ping needs.
The two explicit true forms of `no-new-privileges` are allowed because they
[prevent processes gaining additional privileges](https://docs.docker.com/reference/cli/docker/container/run/#security-opt).
Every entry is checked, so mixing a hardening option with an option that disables
confinement is refused.

**The review prints only what it can show.** A key comment, an operator's name or
email, or an image reference holding a control character (Eg: ESC, DEL, or a
C1 control such as CSI) is refused when the file is loaded, naming the field.
A terminal reads those characters as commands, so a comment carrying one could
move the cursor and redraw a review line, Eg: dress a `+ key` up as another
operator's `~ key`. A host file is not loaded through that check, so as a second
line, everything the review and `doctor` print
from the configuration or from a host shows such a character as an escape
(Eg: `\x1b`), never raw.

**Each host records the grant last applied**, in
`/srv/paisans/<token>/.paisans/granted.json`, the deployment user's, in its
mode 700 state directory. It holds fingerprints and image references, nothing
secret. Each part is written by the command that applies it, once that
command's steps for the site have all succeeded: the login and keys by `site
prepare`, the images by `deployment apply`. What the
operator approves is written earlier, as soon as they type approve and before
the first change, in the record's `approved` part: a run that fails part way
leaves the applied parts as they were, and the next run applies what was
approved without asking again, because the operator saw exactly those entries
and approving is not undone by a step that failed after it. Applying an entry
moves it from the approvals to its part. An entry approved as one thing and
now granted as another (Eg: an image moved on again) is asked about again.
It is written under the deployment's lock in that directory, by a temporary file and
a rename, and only over the file that was read, so two commands writing
different parts lose neither. A host with no record, which is every host of a
deployment built before it existed, has an empty baseline: every entry is new
and is reviewed once. That is the record being made, not a migration. A record
this toolkit cannot read is not rewritten; deleting it by hand makes the next
run review everything again. `site remove` deletes it last.

**The review** compares each entry of the grant with the record:

| Compared with the record | Shown as | Needs approval |
|---|---|---|
| in both, the same | not shown | no |
| in the grant only | `+ key SHA256:… (bob, bob@laptop) may log in as ubuntu` | **yes** |
| in both, different | `~ image talk/app: ghcr.io/…:1.14 → ghcr.io/…:1.15` | **yes** |
| in the record only | `- key SHA256:… (carol, carol@desk)` | no |
| a foundation image, in the grant only or changed | under `foundation images (required):` | no |
| approved on the host earlier, not applied yet | counted on the site's line: `3 approved earlier, not applied yet` | no |

Each site's changes are listed in groups, each under its heading: foundation
images first, then removals under `removed, nothing to approve:`, and last
what needs approval under its count, Eg: `5 entries to approve:`, so the count
heads exactly the entries it counts, just above the prompt.

A removal is shown and not approved, because it only takes away.

**A foundation image is shown and not approved.** A foundation image is an
image the render takes from `foundation.yaml` for one of the toolkit's own
services, and only when that file came from the official core catalog. All of
these must hold:

1. The render itself took the image from `foundation.yaml` for that service,
   never matched it by name: another service of the same compose file that a
   template points at the same reference is reviewed.
2. The core catalog is a release of the official repository: a release tag
   (`vMAJOR.MINOR.PATCH`, Eg: `v1.2.3`; a tag without the `v` is not one) of
   `https://github.com/Paisans-Software/paisans-blueprints` names the commit
   read, as that repository itself says, and the toolkit read it from a git
   checkout whose origin is that URL and whose `HEAD` is that commit. The URL
   may differ only by a trailing slash, a `.git` suffix and the case of its
   host. The official repository's answer about the tag is recorded in the
   checkout, so a run that cannot reach it keeps the exemption. Not official: a
   commit no release tag of the official repository names (Eg: an unmerged
   branch, or a pull request's head, which the repository serves all the same),
   a core pin that names no tag, a core from `PAISANS_CORE_CATALOG`, from a
   `catalogs.core.url`, from any mirror, from a cached commit another URL
   fetched, or from an installed configuration store snapshot (bytes another
   operator published). When `catalogs.core` pins a commit and sets no `url`, the
   commit is fetched from the official URL before an installed snapshot is
   used, so an honest pin to a release keeps the exemption; with a `url`, it is
   fetched from there, and is not official.
3. The service is the toolkit's own: one of the infrastructure stack's unless
   `infra` declares it, the gateway's Caddy unless `acme.image` declares one, a
   pinned app's own database unless `apps.<name>.images` declares one, a
   service the toolkit adds to a stack (Eg: the operators heartbeat), or a
   service written with `foundation:` (see *Images follow tracks*) in a
   reserved blueprint or a core catalog one.

The team reviews `foundation.yaml` in the official repository's pull requests
and releases it with a release tag there, so the images of a release fetched
from that URL at that commit were checked before any deployment reads them, and
asking every operator to approve them again at every apply would repeat a check
already made. A commit the team has not released, a third party catalog and a
mirror of the core catalog are not what that team reviewed and released. So a
service a third party catalog's blueprint writes with `foundation:` needs
approval, though it runs a reviewed reference, because that catalog chose to
run it and nobody on the team reviewed the choice; and with a core catalog read
from any other URL, every image needs approval, the infrastructure stack's
included.
Foundation images are still listed, apart from the rest, so the operator sees
what moves. An image a blueprint names itself with `image:` (Eg: Wiki.js,
Mbin, WriteFreely, or any third party catalog's) needs approval, and so does
any image the configuration declares in `apps.<name>.images`, `infra` or
`acme.image`, since nobody reviewed those choices but the operator who made
them. Whether an image is the
foundation's is decided by where the render took it, never by its name: a
catalog blueprint whose `image:` names the foundation's own reference needs
approval, because a name is what anyone can write. Keys, the web server's
directory and catalog blueprints need approval whatever they name. The record
holds foundation images like every other, marked as the foundation's, so it
stays the complete baseline the next review compares with.

The review is one line per site, pending when anything needs approval, with every change
under it at every verbosity, with `--execute` as well as without: the login and
the keys first, then the images stack by stack, then the web server, so a long
list stays readable. A host with no record of the parts a review covers counts its
entries as new and says `first review` on its line, because there is no
record yet: the first `--execute` on such a host asks once for the whole list,
which is the record being made, and later runs show only what changes.
`site prepare` reviews the login and the keys; `deployment apply --site` the images,
narrowed to the named stacks by `--only`; `site
add` the new site's images, since its stage 4 applies that site's whole
render, recording them once that stage's gate has passed; `deployment apply` with no
`--site` every part of every site, since it runs them all.

**The dry run shows the review and asks nothing.** It ends with what
`--execute` will ask for.

**`--execute` asks once.** When anything needs approval, the command asks the
operator to type `approve` at a terminal, through the same prompt `site remove
--delete-data` uses, before its first change, so that an operator who does not approve leaves the host
exactly as it was. Anything else, or end of input, stops the command with
nothing changed. `deployment apply` with no `--site` asks once for every site, before
its first step; each step it runs then checks that it applies what was
approved, and refuses anything that changed since, rather than asking again. An `init` it runs comes first, since
it changes no host and the images are rendered from the secrets it generates.

**No terminal, no approval.** When something needs approval and standard input
is not a terminal, the command is refused before any host changes, saying to
run it at a terminal. Approving is the control. `--yes` answers the question
only where the environment sets `PAISANS_ALLOW_YES=1`, and prints a line for
each entry it approves, naming the site and the key or image (see *`--yes`
answers confirmations in a script*).

**Nothing to approve, nothing asked.** A run that adds or changes no key and no
image outside the foundation asks nothing, terminal or not, so routine applies
stay scriptable.

**Who approved.** When exactly one operator's key in `paisans-stack.yaml` is loaded in
the operator's ssh agent (`ssh-add -l`), each part a run adds to also records
`approved_by: <operator>`; otherwise it records no name. It is a label for the
next reader, not a check: anyone can load any key they hold. Who wrote a change
is the repository's history, which the toolkit cannot see.

The review is the operator's check, made at the terminal of whoever runs the
command.

### `--yes` answers confirmations in a script

Every command that asks for a confirmation takes `--yes`, which answers it as
typing it would, so the command runs from a script with no terminal on stdin:
each word a command asks to have typed (`approve`, `create`,
`rollback`, `prune`, `sign out`, `revoked`, `lose writes`, `lose objects`, a
site's, an app's or a user's name), and each y/N question (`site admit`'s
grants, and its ufw question with `--site`). `docs/commands.md` lists the
commands. Without `--yes` every question is asked at a terminal, and refused
off one, as the sections above say.

**Each answer is printed.** After what the prompt would have said, a line
`approved by --yes: <what>` names what was accepted unseen, so the run's log
holds it. The review prints a line per entry, `approved by --yes: <site>:
<entry>`, naming the key or image.

**It needs the environment too.** `--yes` refuses, with nothing read or
changed, unless `PAISANS_ALLOW_YES=1` is set. A command line copied from a
log, a runbook or a shell history carries `--yes` and not the environment it
ran in, so the copy alone never approves anything.

**What only a person answers.** `--yes` never answers `site admit
--request`'s digest: `--digest <digest>` gives it, compared exactly as the
typed one, and `--yes --request --execute` without it refuses before
anything is read. The digest is what shows the request's keys were not
changed on their way. Nor does it answer the new SSH login admit asks for
once `--request` has enabled ufw, which shows SSH still works before the
fallback that turns ufw off again is stopped: with `--request`, `--yes`
refuses up front wherever admit would enable ufw, and `site admit --site`,
whose own login as the deployment's user proves it, is the way to run that
from a workstation. It does not pick a login for `--site` either, so `site
admit` and `site evict` with `--yes` need `--ssh`. A value to paste, such as
`secrets rotate`'s or `secrets set`'s, is asked for as it is without it.

### Where WireGuard keys are generated

On the workstation, not on the host — which is the opposite of the usual advice,
for a reason.

Generating on the host and reading the public key back means the private key
never traverses anything. But then it exists in exactly one place, it is not in
`secrets.enc.yaml`, and rebuilding a dead node produces a *new* identity that
every peer must be reconfigured to accept.

Generating on the workstation puts the key in the encrypted file with everything
else. Rebuilding a node from bare metal restores the **same** identity and no
peer changes at all. The key travels over SSH — the same channel already trusted
to carry every other rendered secret.

Replacing a key, once someone who could read the secrets file should no longer
hold it, is `mesh rekey`, one site at a time (see *Rekeying the mesh: `mesh
rekey`*).

### `init` — one site, no mesh

There is nothing to tunnel to yet.

```
paisans init               # steps 1 and 2
paisans deployment apply --execute    # steps 3 to 6
```

`init` reaches a host only to read it. `deployment apply` without `--site` runs `init`
itself when it has work, so the second command alone does all six.

1. Check the configuration, and the mesh subnet against every host. Refuse
   loudly rather than proceed on a warning.
2. Generate every secret — WireGuard keypair, database passwords, object storage
   keys — into `secrets.enc.yaml`.
3. Render and push.
4. **Bring up the mesh interface with this site's address and an empty peer list.**
5. Spilo as a cluster of one, etcd as a single member, HAProxy with one backend,
   Garage single-node.
6. Apps start, pointed at this site's own HAProxy on its mesh address, port
   5000.

Step 4 is the one that is easy to skip and expensive to add later. An interface with
zero peers still provides `10.44.0.1`, and every service binds to it from the
first install. Joining a site then only **adds peers** — no service is
reconfigured and no address changes.

Same principle as running the cluster at one node: build the final shape
immediately, then grow it.

### `deployment apply` brings the mesh interface up before anything binds to it

Step 4 is `deployment apply`'s job, and it runs after the files are written and before any
container is started, checked or restarted. A stack started first fails to bind
its mesh address, and Docker restarts it in a loop rather than reporting it, so
the apply would claim success over a site where nothing listens.

**Every step goes through the WireGuard helper.** The deployment's user
cannot create an interface, write `/etc/wireguard` or start a system unit,
so `site admit` installs one root-owned script per deployment,
`/usr/local/libexec/paisans/wg-psns-<token>`, and one sudoers line that
lets that user run it and nothing else as root. `deployment apply` writes the mesh
file as `/srv/paisans/<token>/psns-<token>.conf`, its own copy, which it
can read back and compare like any other file it renders, and hands it to
the helper's `put` before every start, sync or restart; `put` installs it
as `/etc/wireguard/psns-<token>.conf`, mode 600. Its `down`, which `site
remove` runs, stops and disables the interface and deletes that file. `put`
accepts only the keys `deployment apply` renders, each value matching its pattern, the `ListenPort` the
owner admitted, and an `Address` and `AllowedIPs` inside the mesh subnet,
and refuses the whole file otherwise, because wg-quick runs a file's
`PreUp`, `PostUp`, `PreDown` and `PostDown` as root and adds a route for
each `AllowedIPs`: a helper that installed any file would hand root, or the
host's routing, to whoever could run it. A file it refuses stops the apply
with the line it refused, and a host where sudo will not run it stops the
apply naming `paisans site admit`.

What it runs depends on what changed, and the narrower action wins here as it
does for stacks:

| `psns-<token>.conf` | Interface | Action |
|---------------------|-----------|--------|
| new | any | `put`, then the helper's `up`: `systemctl enable --now wg-quick@psns-<token>` |
| unchanged | absent | the same, so a rebooted or hand stopped site recovers on the next apply |
| unchanged | down | `put`, then the helper's `restart`, since a down link carries nothing whatever it holds |
| unchanged | up, running the file | nothing, and no `put` |
| unchanged | up, differing in a way only wg-quick applies | `put`, then the helper's `restart` |
| unchanged | up, differing in anything else the file sets | `put`, then the helper's `sync` |
| changed peers | up | `put`, then the helper's `sync`: `wg syncconf` from `wg-quick strip`, in place |
| changed `Address` or `MTU` | up | `put`, then the helper's `restart`: `systemctl restart wg-quick@psns-<token>` |

**A peer change is synced rather than restarted.** A restart takes the
interface down, and on a data site that partitions etcd and Patroni for as long as it is down; with election timeouts
measured in seconds, adding a site could start a failover on every existing one.
`wg syncconf` changes peers without touching the interface. It cannot apply the
lines wg-quick handles itself, because `wg-quick strip` removes them before `wg`
sees the file, so a change to one of those is the one case that restarts.
Nor does it add routes, which wg-quick does at start for each peer's
`AllowedIPs`; that is safe here only because every peer's `AllowedIPs` sits
inside the mesh subnet, which the interface's own `Address` already routes
(wireguard-tools, `src/wg-quick/linux.bash`).

**An up interface is read back even when its file did not change.**
Writing the file and handing it over are separate commands, so a run that
stopped between them leaves the file as rendered and the interface on what
it ran before. `deployment apply` reads the interface's addresses and MTU with `ip`,
and its key, `ListenPort`, peers, each peer's `AllowedIPs` and
`PersistentKeepalive` with the helper's `show`. It restarts the interface
when it lacks the file's `Address` or runs another `MTU`, which only
wg-quick applies, and syncs it when any of the rest differs. A peer's
`Endpoint` is not compared: `wg` reports the address a peer last sent from,
resolved and after any NAT, which differs from the file on a mesh that
works, so a run stopped after a change to nothing but an `Endpoint` is
completed by the next change to the mesh file.

"Up" is read from the kernel, not from the unit: `ip -br addr show dev
psns-<token>` for its state and addresses and `ip -o link show dev
psns-<token>` for its MTU, neither of which needs privilege. An interface
brought up by hand serves every service just as well, and starting the unit
on top of it would fail on an interface that already exists.

**The interface is read back before any stack starts.** After it starts,
restarts or syncs the mesh, `deployment apply` reads `ip -br addr show dev psns-<token>` and
carries on only when the interface exists, is not down and holds the site's
`Address`. The unit's exit status alone is not enough: an etcd started over a
missing interface crash loops on `bind: cannot assign requested address`, and
the apply would blame the stack. When the mesh is not up, whether the unit
failed or the interface is simply absent, the apply stops with
`mesh: wg-quick@psns-<token> did not come up` and explains it from the unit's
journal, which the deployment's user reads with the helper's `log`
(`sudo /usr/local/libexec/paisans/wg-psns-<token> log`, the unit's last 200
lines, of which `deployment apply` keeps 20): wg-quick's own error line by default,
the last 20 lines with `-v`. A peer
`Endpoint` whose name does not resolve from the site is the common case, and
is said as such: create its DNS record with `paisans dns init` and apply again.

**Peer endpoint names are resolved before WireGuard runs.** `wg` resolves an
`Endpoint` once and treats "not found" as final, and systemd-resolved keeps a
"not found" for the zone's negative TTL, which can be many minutes. A site that
looked a peer's name up before `dns init` created its record would fail
wg-quick long after the record exists. So whenever `deployment apply` is about to start,
restart or sync the mesh and a peer's rendered `Endpoint` is a name rather
than an address (a site with an `endpoint` name and no `public_address`), it
first runs the step `resolve mesh endpoints`: it clears the site's
DNS cache (`resolvectl flush-caches`, skipped on a host without systemd-resolved)
and asks `getent ahosts` for every such name, in one command, again every 2
seconds for up to 30 seconds, flushing before each try. A name that still does
not resolve stops the apply before the interface is touched, with
`mesh: wg-quick@psns-<token> was not started` and the same advice: create its DNS
record with `paisans dns init` and apply again. A mesh with nothing to do, or
whose peers are all addresses, resolves nothing.

### `deployment apply` keeps the rootless namespace's `src_valid_mark` at 0

The deployment user's rootless Docker keeps its bridges in a network namespace
of RootlessKit's, and RootlessKit's port driver hands each connection to a
published port into it with a transparent dial: a socket bound to the real
client's address, so that an app sees the gateway's mesh address and trusts
its `X-Forwarded-For`, which every app's trusted proxy range (the mesh subnet)
depends on. The dial's replies come home by a mark, `0x64`, and a routing table
of RootlessKit's own whose one route is local
(rootless-containers/rootlesskit, `pkg/port/builtin/child/child.go`).

**At `net.ipv4.conf.all.src_valid_mark` 1 that dial fails.** The kernel then
checks a reply's source address with the reply's mark, finds the container's
address in that local table, takes it for one of the namespace's own, and drops
the reply (`ip_route_input_noref` returns `-EINVAL`). RootlessKit gives the dial
one second and dials again without the client's address. So every new
connection to every app waits a second, which a gateway's kept alive
connections hide until a browser opens several at once, and the app sees the
Docker bridge's gateway, outside the range it trusts a forwarded address from:
Pocket ID's rate limit and audit log see one address for every member. A
namespace copies the host's values when it is made, and Tailscale sets the
host's to 1 for a reverse path workaround of its own
(tailscale/tailscale, `wgengine/router/osrouter/router_linux.go`), so a host
that runs Tailscale for its owner gives the deployment the wait.

**`deployment apply` sets it to 0 in the namespace, as the deployment's user.**
It writes 0 to every `/proc/sys/net/ipv4/conf/*/src_valid_mark` there: `all`,
which the kernel ORs with each device's own value, each device's, and
`default`, which a device made later starts with. The namespace belongs to
that user's user namespace, so this needs no root, and the host's own value,
which Tailscale needs, is not touched. A restart of the daemon makes the
namespace afresh with the host's values, so `deployment apply` also installs
`~/.config/systemd/user/docker.service.d/paisans-src-valid-mark.conf`, whose
`ExecStartPost` writes them again at every start, and has the user's manager
reload it. Its command starts with `-`, so a failure there never stops the
daemon from starting: the daemon then runs with the wait, and `paisans doctor`
says so.

Before any stack moves, after the mesh, `deployment apply` reads the drop-in
and the namespace's highest value. It installs the drop-in when it is absent
or differs, then writes the values when one reads 1, and stops the apply if either
fails, so no container is started into a namespace with the wait. A read that
fails asks for nothing: it is not a gate, and `paisans doctor` reports a
namespace it cannot read. An apply narrowed with `--only`, and a plan another
command scopes to a few files, does not read it: each moves only what it names.
The drop-in counts as missing when the user's manager has not loaded it, as
after a reload that failed once the file was in place.

`paisans doctor` reads the highest value with its rootless Docker check and
fails a site whose namespace holds 1, naming Tailscale and
`paisans deployment apply`. It warns when the namespace holds 0 but the host
holds 1 and the user's manager has not loaded the drop-in, since the daemon's
next start copies the 1 again.

### `paisans deployment apply` with no `--site` converges the whole deployment

`paisans deployment apply` without `--site` takes `paisans-stack.yaml` to a complete, running
stack, and keeps it there. It reads whether `init` has work and which etcd
members have been founded, plans the commands below in the order the
deployment needs them, and with `--execute` runs them in the same process,
stopping at the first failure. The table is that order. Every host's
`site prepare` runs at the same time, up to eight hosts at once, and so does
every site's image pull after it; every other step runs after the one before
it:

| Phase | Command | When |
|---|---|---|
| configuration | `init` | no id, no mesh subnet, or a generated secret missing |
| hosts | `site prepare --site <s>` | every site; a prepared host plans nothing |
| pull | each site's images (below) | every site; a host that has every image its apply needs pulls nothing |
| dns | `dns init` | always; a record pointing elsewhere stops it, to be changed by hand |
| founding | `deployment apply --site <s>`: each witness in `etcd.members`, then `deployment apply --site <g> --hold-apps` for each gateway outside it, then the other members | a member is still being founded (below); a witness that is founded already is applied here too while a data member is still being founded, since that member's apply refuses until a witness's etcd runs; each gateway outside `etcd.members` is applied with every app stack on it held back, its mesh, infrastructure and Caddy only, because the data members' apps fetch Pocket ID's discovery document through it as they start, and its Caddy starts before they answer. Its full apply comes among the other sites |
| joining | `site add <s>` | the cluster runs and a data member has not joined it; a joining witness is joined by that `site add`, and gets one of its own only when no data member joins, which stops the run |
| other sites | `deployment apply --site <s>`, monitor sites last | first each site that joined with `roles: [data, apps]`, which starts its apps, and then each gateway in `etcd.members`, so its routes gain the new apps site; then every site not in `etcd.members`, and then every Garage site no earlier step applies or joins, since storage works through Garage's container |
| storage | `storage init --site <g>` for one Garage site, `storage add` for several | any Garage site |
| pass two | `deployment apply --site <s>` for every site, monitor sites last, a monitor in `etcd.members` too | always: it resumes a founding stop or a replica's wait for the databases, and moves what earlier steps changed |
| gateway | `gateway move` | a site without the gateway role still holds this deployment's gateway (see *Moving the gateway*) |

**The configuration is published once the stores exist.** The version `deployment apply`
applies is gathered before the review, so a fork or files older than the sites'
newest version are refused before anything is asked (see *Every site keeps
signed copies of the configuration*). It is published after the `site prepare`
steps, which create every site's store, and before any other step changes a
host: it cannot be pushed to a store that does not exist yet, and `site
prepare`'s own changes are reviewed already. Publishing earlier than the first
change keeps the store showing the declared state even if the run stops part
way. A run that stops between prepare and publish has granted keys without
publishing the version, and the next run publishes it. A version that reaches no
site is refused, naming `site prepare`, because the hosts are checked against
what the store holds. `deployment apply --site` without a store refuses in the same words.
An apply that records what it made at Pocket ID, a client's credentials or a
signup group's ID, writes `secrets.enc.yaml` after the version was published.
Before its host records the version applied, it publishes the bytes it wrote
as the next revision, which the host records; as a step of `deployment apply` without
`--site`, every later step applies that revision.
Gathering reaches every site as the deployment's user and needs the standard file names
beside `paisans-stack.yaml`, so a deployment with renamed files cannot use `deployment apply`
until they are renamed; and a dry run needs an operator's key in the ssh-agent too,
because it checks the key `--execute` would sign with.

**DNS comes before any site is applied.** Each site's `wg-quick` resolves the
`endpoint` names it dials, those of sites with no `public_address`, as the
mesh interface starts, and one name that does
not resolve fails the whole interface, so etcd then cannot bind its mesh
address. `dns init` needs only the provider's API and the configuration, never
a host, and nothing in it waits for the stack: Caddy's certificates use DNS
challenges. A record pointing elsewhere therefore stops the run before
install, with nothing applied.

**Every site's images are pulled on every host before anything is
installed.** The install applies one site at a time, and an apply pulls the
images its stacks name as it recreates them, so on a first deployment each
host's downloads, gigabytes each, would run in turn while every other host sat
idle. The pull stage plans each site exactly as its `deployment apply --site` would, and
pulls on the host the images that plan would pull: the rendered images the host
does not have, of the stacks the apply will recreate. Each host pulls a few
images at a time, every host at once. Each apply after it finds its images
present. It needs only the user's Docker, so it follows prepare
directly; ahead of `dns`, a host short of space stops the run before a record is published for a
deployment that cannot start. The same plan carries the free space check (see
*`deployment apply` checks free space before it pulls*), so a host short of space is
refused here, before any host has pulled a layer, with the words its apply
would use, and `--min-free` reaches this check as it reaches each apply.
Anything else that plan would refuse, such as a file edited on the host, is
left to the site's apply, which reports it at its own step. The plan names
Caddy at the build its tag resolved to (see *The provider is declared, and
its module has to be in the binary*), and an apply resolves each tag once for
the whole run, so the build pulled here is the one every later step plans,
even if the tag moves in between. A registry that refuses an image,
`unauthorized` or `denied`, is reported as a private image, with what to do.

**Every Garage site is applied before storage.** `storage init` and `storage
add` read and change Garage's layout through its running container. A
founded member's first apply in the run is otherwise pass two, after storage,
so a Garage site whose containers had gone (Eg: removed to free disk space)
stopped every run at storage with "service garage is not running", before the
apply that would start it. A Garage site no earlier step applies or joins is
applied in *other sites*; on a site already running it plans nothing.

Run again, it reads live state again and carries on, so the same command is
the first install, the resume after a failure, and every later change. Each
step is the command an operator would type, so it plans, gates, checks the
host's admission and refuses as that command does. Then, before the
first step that changes a host, it shows one review of what `paisans-stack.yaml`
grants on every site, and asks once when anything outside the foundation is
added or changed (see
*`deployment apply` and `site prepare` review what `paisans-stack.yaml` grants*); each step then
applies what was approved, and refuses anything that changed since. An `init`
with work to do runs ahead of the review: it writes only local files, and the
images reviewed are rendered from the secrets it generates. `-v` reaches every step, and `--keep-images` and
`--min-free` each `deployment apply`; `--ssh`, `--overwrite`, `--only` and `--recreate`
are about one site, and are refused without `--site`. A stop names its step and how
to carry on: usually by running it again, but a DNS record pointing elsewhere
is first changed at the provider, and a `storage add` gate still waiting on
Garage to move data exits with status 75, to be run again later. A data site's founding stop is expected, not a
failure: pass two applies it again. So is a replica's stop for the app
databases, on any of its site's applies: which data site leads is known only
once Patroni runs, so a later visit applies it again, and one still waiting on
its last visit is applied once more after every other step, by which time the
leader's site has created them. Still waiting then, it stops the run. It never takes anything out: a site or an
app the yaml no longer declares is reported by the deployment record's warning,
with the command that removes it. Once Pocket ID runs, every operator the
`operators` section declares is made a Pocket ID user and a community admin
and handed a login link (see *`deployment apply` makes the declared operators and hands each a login link*).
What only the operator can decide is left to its own command: a credential
the toolkit cannot generate (`secrets set`), and a DNS record pointing
elsewhere. `deployment apply --site <name>` is unchanged, for one site at a time.
`docs/specs/2026-10-09-apply-converge.md` is the approved specification.

The output does not follow the phases. After `configuration` and
`etcd members`, which reads each member's founding record on a line of its own
(`read <s>'s etcd record`, `founded` or `not founded yet`), because founding
and joining are decided from them, it has four sections: `prepare`, one line
per site for its `site prepare`; `pull`, one line per site for its images;
`dns`; and `install`, one line per site for every step from founding to pass
two that is about it, in the order the run first applies each site, then a
`storage` line for `storage init` or `storage add` when there is Garage. After
them, a `pocket id` section has a line per operator for each Pocket ID app
whose sites' steps are all up to date, or have run (`<operator> on <app>`),
since one not running yet cannot answer: `registered` for an operator who has
a passkey, what the run made otherwise, and in a dry run what it would make.
Then, when an app is served behind the operator's own web
server, a `web server` section: with `--execute` a line for each reference
snippet written and a line per app for its web server check, and in a
dry run a line per app, `--execute will check it` (see *The monitor has a site
of its own*). Last, a note names `secrets set` for each credential still owed,
since only the operator can supply it. A run with `--execute` ends
`Deployment complete.`; one where an app failed its web server check, or its site
file step warned, ends on a warning naming those apps instead and exits 0, or
1 when something else failed as well, a web server step that could not reach
or write what it needs on the host included. With
`--execute` the prepare lines are a block of spinners, one per host, each with
its own elapsed time; a host that finishes leaves the block with its mark,
above the hosts still running, so the lines end in the order their hosts
finish. While a line runs, the step its command is on follows the elapsed time
in parentheses, the innermost one when steps nest, replaced as each starts; a
host's prepare names the file or unit it is writing. It is gone from the line
once the line ends, and output without a terminal never shows it:

```
prepare
  ✓ vm: 1.8s
  ✓ watch: 2.0s
  ⠹ home-b: 38s (write config-sync.sh)
  ⠹ home-a: 38s (start the configuration store)
```

The install steps still run one at a time, in the order of the table, so the
install lines are a block that keeps its order: a site's line is a dim `·`
until the run reaches it, spins while one of its steps runs, and between its
steps, such as a founding stop and pass two, is a dim `·` again with the time
its steps have taken so far and what it waits for: the sites a founding stop
waits on to run etcd, the site a replica waits on to create its databases, or
otherwise the places the run visits before it comes back. Its time is its
steps' together, and it ends `✓` once its last step has:

```
install
  · vm: 22.0s, paused until home-a, home-b, watch and storage are done
  · home-a: 31.0s, paused until home-b runs etcd
  ⠹ home-b: 41s (wait for Patroni primary)
  · watch
  · storage
```

The pull lines are drawn the same way, each ending with how many images its
host pulled, or `nothing to pull`; `-v` lists each image under its site's line
with the time it took. Under each line, at every verbosity, is every image whose
build that site's apply moves, with the builds it goes between, as a dry run of
the one site lists it. The install lines say only how long each site took, so
this is where a run that brings an app a new release says which app, and to
what.

A host whose prepare fails does not stop the others: each is prepared, each
failure is marked on its own line, and the run then stops before install with
every failed host named. A host whose pull fails is the same: every other host
finishes its pulls, and the run stops before `dns` with each failed host named
and why. Without a terminal each prepare line is printed once, whole, when its
host finishes, and with `-v` a host's own output follows its
line, never another host's. An ssh host key question a
host asks during prepare holds the block, one question at a time.

A step that fails marks its site's line `✗` with the time its steps took, the
other lines are left as they stand, and the stop that follows names the step
and how to resume:

```
install
  · vm: 22.0s, paused until home-a, home-b, watch and storage are done
  · home-a: 31.0s, paused until home-b runs etcd
  ✗ home-b: 41.0s
  · watch
  · storage
```

While they run the steps print nothing of their own but their notes, what only
the operator can do, which follow their site's line; their warnings are kept
for `-v`. `-v` lists each step under its site's line, with its result and its
own output. Without a terminal each install line is printed once, when it and
every line above it have ended, so they keep their order; at a stop, the lines
that did not end are printed as waiting:

```
install
  ✓ vm: 64.3s
      ✓ deployment apply --site vm: 22.0s
      ✓ deployment apply --site vm: 42.3s
  ✓ home-a: 89.5s
      ✓ deployment apply --site home-a: paused until home-b runs etcd
        home-a is waiting on home-b to start etcd.
      ✓ deployment apply --site home-a: 58.5s
  ...
```

`dns init` reports its records as it does when typed, so a conflicting record
it lists is on screen above the stop that asks for it to be changed. It leaves
out the configuration's findings, which the run showed at its start, and its
own closing line, since the run ends on its own.

Its dry run changes nothing on the servers. When the yaml has its id and mesh
subnet and the secrets file reads but lacks a generated secret, it generates the
missing ones into that file, as `init` would: only what is missing, nothing
overwritten, encrypted to the recipients in `.sops.yaml`. Its `init` line names
what it wrote, never a value, and the closing line says the secrets were added;
the later steps are then checked rather than waiting on init. A missing id,
subnet or secrets file is still left to `init`, which `--execute` runs first.
Every host's prepare is checked at the same time, drawn as with `--execute`. A
prepare line names each kind of change its site's plan makes, in the order
docker (the user's `daemon.json`), ssh keys, configuration store. Each
install line is its site's steps' own dry runs together, and the storage line
its step's: `✓` when every one is up to date, a dim `·` with `after prepare` (or
`after init`) while a step waits on one that has not run, `○` when there is
work, with what each step found, and `✗` when a step would refuse, with the
refusal's hint after the site and what to do below it. A refusal outranks a
wait, and a wait outranks work; a step that waits on another install step is
work. `-v` lists each step under its line with its own status. Each pull line is
the plan its site's apply would make: `○` with how many images it would pull and
the free space they land in, `✓` `nothing to pull`, `✗` with the free space
refusal `--execute` would stop at, or a dim `·` `after site prepare` while its
site's prepare has work, since a host whose Docker cannot answer has no images to ask about.
Under a line that is not waiting is each image whose build its apply would move:

```
configuration
  ✓ init: generated 4 secrets into secrets.enc.yaml (cluster.admin_password, cluster.standby_password, cluster.superuser_password and 1 more)

prepare
  ○ home-a: docker, ssh keys, configuration store
  ✓ home-b
  ✓ vm
  ✓ watch

pull
  · home-a: after site prepare --site home-a
  ○ home-b: 3 images to pull, 12.4 GiB free
       talk  app  ghcr.io/example-org/mbin:v1-fork  1.15.0-fork (sha256:1a2b3c4d5e6f) -> 1.16.0-fork (sha256:3c4d5e6f7a8b)
  ○ vm: 1 image to pull, 6.2 GiB free
  ✓ watch: nothing to pull

dns
  ○ dns init: 2 records to create

install
  ○ vm
  · home-a: after prepare
  ○ home-b
  ○ watch
  · storage: after prepare
...
Nothing changed on the servers. Added 4 generated secrets to secrets.enc.yaml. Re-run with --execute to apply. ...
```

`etcd.members` here is the list as written or as derived from the roles. So a
site given the `data` role on a founded cluster, with the key left out, is a
member no founding record lists, and joins through `site add`, exactly as it
would written into `etcd.members` by hand.

### A deployment with sites missing

Operators admit sites one host at a time, and a deployment that works on fewer
sites should not wait for all of them. So every run classifies every declared
site, once, at its start, and every step of the run uses that classification.
`deployment apply` without `--site` and `deployment apply --site` both do: the second reaches every
site's host too, so one site's files never disagree with another's about who is
in the mesh.

**Whether a site has joined is read from the deployment, never from the site's
own host.** A host that has evicted the deployment, one that is down and one
that never admitted it look alike from outside: the login is refused or
nothing answers. So the run reads the gateway's deployed record, which lists
the sites deployed, and etcd's and Patroni's membership from any member that
answers.

| The site's host | Joined | The site is | Its place in the topology | The run |
|---|---|---|---|---|
| answers, and its admission record covers its roles | either | **ready** | as declared | normal |
| refuses the login, does not answer, or is admitted without a grant its roles need | no | **pending** | left out: no mesh peer, no etcd or Patroni member, no Garage node, no DNS record, and its apps held back | goes on, degraded |
| refuses the login or does not answer | yes | **down** | kept as declared: still a peer and a member, its records kept | goes on, degraded, without that site's steps |

**A site that does not answer is tried once.** The classification's login
makes one connection attempt, not the three a command makes (see *A connection
that never opened is tried again*): a host that is switched off waits out ssh's
connect timeout on every attempt, so the retries would only multiply the wait.
A site whose classification login never connected is down for the rest of the
run, and every later command to it through the site's transport fails at once.
A command's own first contact failing is not kept: the next command, a wait
for Patroni's primary among them, tries again. A blip that a retry would
have ridden out reads as the site not answering, and the next run reaches it.

A down site is never removed by a run. The cluster keeps its member, as etcd
and Patroni keep any member that is down, and only `site remove` takes it out.
Its line cannot say whether the host is down or its owner evicted the
deployment, so it says both, and names `paisans site remove --site <site>
--host-gone`. A joined site whose admission no longer covers it stops the run
instead, naming the `site admit` its owner runs again, and so does a joined
site whose only route into the mesh is through pending sites: the run never
drops a joined site from the mesh.

**Pending sites are left out as if not yet added.** The run plans the
**effective topology**, the declaration less its pending sites, and renders
every ready site from it. A pending data site joins through the path `site
add` takes once its host admits it; the next `deployment apply` finds it ready, with no
other command. A site whose only route into the mesh is a pending site's
`endpoint` is pending too, with that reason. Garage with fewer ready nodes
than its replication holds back storage, and the apps that need it, with a
warning.

**What still refuses the run:** the gateway pending or down; no ready data
site while an app needs a database; etcd or Patroni without a quorum among the
members that answer; and `--site` naming a site that is pending or down. Each
holds because the run would otherwise decide the cluster's membership from a
partial view: a site that may have joined is never dropped on silence, and
when a source that could have listed a site cannot be read, the run stops
rather than guess.

**What it says.** The plan opens with a `pending` section: a line per pending
or down site with its reason and, for a site pending for its own login or
admission, its `paisans site request --site <site> > <site>.admit` line (a site
pending through a relay has none), then what the deployment is without (the
wording of `doctor`'s Reach row). The run ends `Applied, degraded: <n> sites
pending or down (<names>)` and exits 0, since every step it could take ran.
While a joined site is down, a site joining through `site add` waits too, with
the line `site add <site>: waits until every joined site answers`, and the run
ends degraded. `--require-all` makes a degraded run, or its dry run, exit 1, for
automation that must not pass on one. `doctor` reports a pending or down site
as a `WARN` in its Reach row (or its Admission row, for a pending site that
answers), a `FAIL` on a gateway, with the same account of
what the deployment is without.

**The declaration stays the only record.** The configuration store keeps
`paisans-stack.yaml` as written; the effective topology is derived on every run
and never published, so admitting a site creates no version. Each site's
manifest records which sites were pending when its files were rendered, and the
checks that compare a host with a render (an edited file, `doctor`'s
Leftovers) use that same view, so a file rendered without a pending site is
not drift.

A rekey with sites down is in `docs/specs/2026-10-15-mesh-rekey-sites-down.md`.

### A new deployment is applied witness first

The first time a deployment's sites are applied, the order matters, and it is
one order only: **every witness in `etcd.members` first, then the data sites.**
After that first round, sites are applied in any order.

The reason is etcd, not Patroni. A new etcd cluster starts at cluster version
3.0 and raises it only once its leader has heard a version from **every**
founding member: one member that has never started leaves the decision open
(etcd v3.5.16, `server/etcdserver/cluster_util.go`, `decideClusterVersion`
returns nothing while any member's version is unknown). Quorum is not enough.
Patroni reads that version from `/version` and, below 3.3, talks to etcd under
`/v3alpha` (Patroni v4.1.0, `patroni/dcs/etcd3.py`), a prefix etcd 3.5 does
not serve. It logs `waiting on etcd` and never takes the leader key, so a data
site's apply polls for a primary until it times out, with nothing on screen
pointing at the member that is missing. That happened on the first three-site
deployment: both data sites, applied before the witness, timed out, and the
cluster formed seconds after the witness was applied.

The witness goes first because nothing waits on it: it runs no Patroni, so its
apply finishes on its own and its etcd is running for the sites after it. Two
gates enforce the rest, and both apply only while **this site's etcd member is
being founded**, which `deployment apply` reads as the absence of `/srv/paisans/<token>/infra/etcd-initial`:

| Gate | When | What `deployment apply` does |
|------|------|-------------------|
| witness first | a data site's member is being founded and a witness in `etcd.members` runs no etcd | refuses to touch the infrastructure stack, and names the witness to apply |
| founding stop | a data site's member is being founded and another member runs no etcd yet | brings the infrastructure stack up, so this site's etcd joins the set, then stops before the wait for a primary, naming the sites still to apply; the next apply resumes |

So three sites go: the witness; the first data site, which stops after its
infrastructure; the second, whose etcd completes the set, so it waits for a
primary and carries on; then the first again, which resumes. Applying the two
data sites at the same time is safe but saves nothing: each can see the other's
etcd not yet running and stop after its infrastructure, and then each is
applied once more.

**Both gates are about founding, and only founding.** A member that has run
once has settled the cluster version for good, and a witness that is down
afterwards must never hold up a data site's apply: riding out that outage is
what the witness is for. The gate reads the record on the site being applied
rather than the live cluster, so it costs an ordinary apply no extra host.

**A founding member alone answers nothing, so the gates read its record
instead.** The witness, applied first, runs etcd by itself until the first data
site's member joins, and until then it serves no client request at all: in etcd
v3.5.16 a member waits to publish itself to the cluster before it serves, and
publishing needs a quorum, so `member list`, `endpoint status` and `/health` all
hang. So whether a member runs is asked of Docker (`compose ps --status running
etcd`), not of etcd. The check against the live membership asks `member list` as
always; when that fails on a member while this site is being founded, `deployment apply`
reads that member's own `etcd-initial`. If it records
`initial-cluster-state=new`, the member belongs to exactly the cluster its
`initial-cluster` names, so that list is compared with `etcd.members` as a live
membership would be. A member with no such record is an error, and so is one
that answers nothing when this site is past founding: that is a cluster in
trouble, not one being born.

### `deployment apply` creates each clustered app's role and database

Step 6 needs something step 5 does not provide. Patroni creates its superuser,
its replication user and an `admin` role from `patroni.env`, but nothing creates
`talk` or `docs`, and an app started without its role fails to authenticate and
is restarted in a loop. So on a site in `cluster.sites`, after the
infrastructure stack and before the first app stack, `deployment apply`:

1. waits up to three minutes for Patroni's `GET /cluster` to name a running
   leader;
2. if the leader is another site in `cluster.sites`, creates nothing, since a
   replica cannot create roles, and reads its own catalogue, which streams the
   leader's: when every clustered app's database and role is there it goes on
   to the app stacks, and otherwise it stops, naming the databases missing and
   the leader's site to apply first;
3. if the leader is not in `cluster.sites` at all, stops, naming the leader it
   saw and the sites it expected;
4. otherwise sends one psql script on stdin to the Spilo container that, per
   clustered app using Postgres, creates the role if it is missing, **sets its
   password every time**, and creates the database owned by it if missing.

A timeout, an unknown leader, a replica missing a database or a psql failure
stops the apply before any app stack starts, and the next apply resumes at the
same place. It is a gate, not a warning: an app
pointed at a role that does not exist has nothing to fall back on.

**Setting the password every time is what makes rotation an addition.** A
changed `apps.<name>.database_password` renders a new `.env`, and the role
accepts it on the same apply, with no manual `ALTER ROLE` on the primary.

**The SQL goes on stdin, never on a command line.** A command line is visible in
`ps` to every user on the host and is quoted back in ssh errors. For the same
reason the script switches off statement logging for its own session first:
Spilo ships `log_statement = 'ddl'`, and `CREATE ROLE` and `ALTER ROLE` are DDL,
so the server log would otherwise hold every password
(zalando/spilo, `postgres-appliance/scripts/configure_spilo.py`).

**It runs as `postgres` over the container's own socket**, which Spilo's
`pg_hba` trusts (`local all all trust`, same file), so the toolkit passes no
database credential to do it. `CREATE DATABASE` cannot run in a transaction or a
`DO` block, so the conditional creates use psql's `\gexec`, which runs a
generated statement at top level.

**An app whose role would be `postgres`, `admin`, `standby` or `pg_*` is
refused**, because the bootstrap would set that app's password on a role the
cluster uses itself.

**A replica waits for the leader's databases.** Which data site leads is
settled by Patroni as the cluster forms, not by the order sites are applied.
On the first deployment the second data site's apply found Patroni a replica
before the leader's site had created anything, started its apps, and Pocket ID
failed `password authentication failed for user "auth"` in a loop. The read is
by name only, `pg_database` and `pg_roles`, so no password goes into it, and
it is polled like the wait for a leader, since a replica's Postgres that has
only just started may not answer yet.

**Only a declared site can be the reason to leave the work.** The rendered Patroni names
each member after its site, so "the leader is not me" can be read as "the
leader is that site" only when the name is one of `cluster.sites`. On the first
real host the member was named after the machine instead, and an apply that
took any other name for a replica skipped the databases and started every app
with none. A name nobody declared means that pin did not hold, which is a
fault to fix rather than someone else's work, so it is a gate. It is not
polled out like a missing leader: a running member's name does not change.

`/cluster` is asked rather than `/primary` because it answers both questions:
`/primary` returns the same 503 to a replica as to a node still running initdb,
while `/cluster` names the leader (Patroni v4.1.0, `docs/rest_api.rst`).

### `deployment apply` restarts only the infrastructure service whose file changed

The infrastructure stack is one compose project holding etcd, Patroni, HAProxy,
Garage, the toolkit's Caddy and the heartbeat pusher, each where the site's
roles call for it. Restarting the whole project for a change to one bind
mounted file would make a new `garage.toml` restart Patroni, which on the
primary is a failover, and HAProxy, which drops every app's database
connection. Each service's bind mounted files live in a directory named for
it (`haproxy/`, `garage/`, `caddy/`), so a restart names the services
whose directories changed: `docker compose -f /srv/paisans/<token>/infra/compose.yaml restart
garage`. A file at the top of the stack restarts the whole project, and
an environment or compose change is a recreate, which Compose limits to
the services whose configuration changed.

**The directory is the mapping because the templates are the toolkit's own.**
Their layout already says which service reads each file, with no YAML parser in
the path of every apply.

**A bind mounted file changed beside a recreate is restarted after it.** The
recreate is `up -d`, and Compose replaces only a container whose configuration
changed; a bind mounted file is not configuration to Compose. So a
`haproxy.cfg` changed in the same apply as a `patroni.env` left HAProxy running
on the old file, and since `haproxy.cfg` is a single file bind mount that a new
file is renamed over, the running container keeps the old inode
(moby/moby#15793). After the `up -d`, `deployment apply` restarts each service whose
bind mounted file changed and whose container `up -d` left in place, and none
it replaced. Which is which comes from the container IDs `docker compose ps`
reports before and after, not from `up -d`'s progress lines, which are for
people. Only those are restarted, because a container `up -d` has just
replaced would otherwise be restarted a second time, and for Patroni on
the primary that is a second failover. A forced recreate (`--recreate`, or a
stack a stopped apply owes) replaces every container, so it needs none of
this.

### etcd compacts its history and states its quota

etcd keeps every revision of every key until it is compacted, and its auto
compaction is off by default. Patroni rewrites its leader key on every loop, so
an uncompacted store grows until it reaches the backend quota, raises a
`NOSPACE` alarm and refuses writes; Patroni then cannot renew the leader lock
and the primary demotes itself. Every member is rendered with
`--auto-compaction-mode=periodic --auto-compaction-retention=1h` and
`--quota-backend-bytes=2147483648` (flag names from etcd v3.5.16,
`server/etcdmain/help.go`). An hour is far more history than anything reads.
The quota is etcd's own default, 2 GiB (`DefaultQuotaBytes` in
`server/etcdserver/quota.go`), made explicit so that a later etcd changing its
default does not move it silently. It is no smaller because compaction
frees pages for reuse but the file never shrinks without a defrag, which
nothing schedules, so a tight quota is an alarm waiting on a burst.

These are runtime flags, read on every start, unlike `--initial-cluster` and
`--initial-cluster-state`, which etcd reads only on a member's first start and
which `/srv/paisans/<token>/infra/etcd-initial` keeps fixed. Changing them is an ordinary
change to the infrastructure compose file: `deployment apply` recreates the `etcd`
service on the site it is applying and leaves the record alone. On a live
deployment that is one member at a time, one site per apply, and with three
voters a member down for its recreate leaves the other two as quorum. Apply
the sites one after another, not at once.

### A stopped apply force-recreates what it still owes

A stack whose action did not finish is in an unknown state, and Compose cannot
see that. On a real host an `up -d` for the Mbin stack failed part way, on a
host port already in use, and left the `app` container created but attached to
no network. The next apply resumed from the pending record and ran the same
plain `up -d`. Compose compared the configuration, found it unchanged, and
started the half built container, which ran with no networks and no routes.

So a stack the pending record still owes runs `docker compose up -d
--force-recreate`, which replaces its containers whatever Compose thinks of
them. The record drops each stack as soon as its action finishes, so only the
stack that stopped and the ones after it are forced, not the ones that were
already fine. An operator who finds a stack in the same state with no record
names it: `deployment apply --recreate <stack>`, one stack per flag, refused for a stack
the site does not render, and planned even when nothing changed. The ordering
holds either way: the infrastructure stack first, the databases, then apps.

**Only those stacks are forced**, because a forced recreate replaces every
container of a stack, and a recreate is an outage however brief.

### `deployment apply` checks each stack before the next one moves

`docker compose up -d` and `restart` exit as soon as containers start. On a
real host that let an apply report success while Mbin's app could not reach its
database: the containers were up, and nothing was asking whether they worked.

So after each stack's action `deployment apply` waits, up to five minutes and polling
every five seconds, until every container of that compose project is running
and every one that defines a healthcheck reports `healthy`. It reads `docker
compose ps --all --format json`, `--all` because an exited container is exactly
what the check is for (docker/compose, `cmd/compose/ps.go`). A container with
no healthcheck counts once it runs: the toolkit cannot invent a check an image
does not ship, and refusing such a stack would refuse most of them.

| What `ps` shows | What `deployment apply` does |
|-----------------|-------------------|
| every container running, healthchecks `healthy` | moves on to the next stack |
| a container still `created`, or a healthcheck `starting` | keeps polling |
| a container `exited`, `dead` or `restarting`, or a healthcheck `unhealthy` | stops now |
| still polling after five minutes | stops |

A stop names the stack and the services, with one line of cause for each read
from its last 30 log lines (`docker compose logs --tail 30`), and starts
nothing after it:

```
✗ apply stopped at deployment apply --site site-b
  site-b: stack infra is not healthy: etcd (restarting, exit code 0).
  etcd: listen tcp 10.45.0.1:2380: bind: cannot assign requested address
  Nothing after this stack was started; the next apply recreates it.
  Run paisans deployment apply --execute again to resume.
```

The cause is the newest line that reads as a failure: for a JSON log (etcd,
most Go services) the `error` field, else `err`, `msg` or `message`, of a line
whose level is error, fatal, panic, dpanic, crit or critical, never its stack
trace; for any other log a line that says error, fatal or panic. A log with no such line adds none. The
30 lines themselves follow with `-v`. `site add`, `site remove`, `storage
add` and `app remove` stop the same way when a stack they start or the
monitor they reseed is not healthy, with their own advice after the cause.
The stack stays
in the pending record, so the next apply resumes at it and, by the section
above, recreates it outright. The infrastructure stack is checked like any
other, which is also the check on the gateway's Caddy after a reload or an
image change; Patroni's own wait for a primary still gates the database
bootstrap, because a running Spilo container is not yet a primary.

**A stop refers to `paisans doctor` only where a doctor check names its
cause**, because a referral to a command that then reports everything ok is
worse than none. The causes are one table in `internal/diagnosis`, which
apply, init and doctor share. A service whose log shows `tls: internal error`
fetching a hostname a Caddy site serves gets a line saying that Caddy has no
certificate for it (Caddy's answer to a handshake for a hostname it holds no
certificate for, observed on staging), and a service that cannot reach its
database gets one pointing at doctor's Patroni check:

```
  oauth2-proxy: Caddy on vm has no certificate for id.example.org, so it refuses TLS for it. Run paisans doctor to see why it could not get one.
```

A TLS error on a hostname Paisans does not serve gets no referral. A site ssh
cannot reach, in `deployment apply` or in `init`'s read of every site, ends with `Run
paisans doctor --site <site>`, whose reach check says what the deployment is
without while it is gone. `gateway move` calls the DNS provider without the
token check `dns init` makes, so a refusal there (Cloudflare's `9109` or
`10502`) is worded as that check words it, with `Run paisans doctor`. Any
other failure is reported as above, with no referral.

**Failing on the first `restarting` rather than waiting it out is deliberate.**
Every rendered service carries `restart: unless-stopped`, so `restarting` means
the process already crashed once after its dependencies were up, and Docker will
keep trying forever without telling anyone. A timeout stops the apply too, for
the same reason the other gates refuse rather than warn: an apply
that reports success over a broken stack is how this was found.

### `deployment apply` restarts the apps whose database path changed

An app with `cluster` placement reaches the database through its own site's
HAProxy (rule 2), so a restarted HAProxy, or a Patroni that is recreated and
hands the primary to another member, closes every connection that app holds.
Most apps reconnect. Mbin does not: on a real host, after `site add` restarted
HAProxy, Mbin answered HTTP 500 from then on (`SSL SYSCALL error: EOF
detected`, then `no connection to the server`), because its FrankenPHP workers
keep their connections open across requests and never open a new one. Its
healthcheck does not touch the database, so the container stayed `healthy` and
nothing in the toolkit noticed. Pocket ID recovered on its own.

The toolkit owns this (founder decision): whatever it does that changes the
database path restarts the app stacks that depend on it, and says so in the
plan. In `deployment apply` that is a change to the infrastructure stack that moves
HAProxy or Patroni: `haproxy.cfg` or `patroni.env` changed, the `haproxy` or
`patroni` service changed in `compose.yaml`, or the action restarts or force
recreates either. Then every app on the site with `cluster` placement and a
kind that uses Postgres gets a restart after the infrastructure stack's action
and health gate and after the database bootstrap, and is held to the same gate.
The dry run lists the restart, and `-v` gives its reason under it:

```
  ○ restart talk
      its database path changed (HAProxy/Patroni moved), and an app's open database connections do not survive that
```

An app that already has its own restart or recreate keeps that one action,
with the reason added; it is not acted on twice. A change that moves only etcd,
Garage or Caddy restarts no app: a restart of the infrastructure stack is
narrowed to the services whose files changed (see "`deployment apply` restarts only the
infrastructure service whose file changed"), and a recreate replaces only the
services whose configuration Compose sees changed. A pinned app has its own
Postgres and is not affected. `--only` without `infra` moves nothing under the
apps and triggers nothing; `--only infra` that restarts HAProxy restarts the
apps with it, because leaving them on dead connections is the incident.
`site add` stops the apps around its HAProxy restart, and `failover test`
restarts them on every apps site after each switchover; see those sections.

A `restart` is enough. `docker compose restart` stops and starts the same
container, so its process tree is new and every connection it held is gone,
while its configuration is not reread. Checked with Docker Compose v5.4.0 on
Engine 29.7.2: across a restart the container ID stayed the same, the start
time changed, and every process inside, PID 1 and its children, had a new
PID.

**The toolkit's answer is a restart.** Reconnection inside each app is worth
reporting upstream, but the toolkit deploys apps it does not write and cannot
guarantee it for every one of them; a restart it can guarantee. **Only a change
to the database path restarts them:** each restart is an outage, however brief,
and most infrastructure changes (a Garage setting, a Caddy route, an etcd timing) never
touch the database path.

### Mbin's locks are split: flock by default, Postgres for the few that cross sites

Mbin takes its Symfony Lock store from `LOCK_DSN`
(`config/packages/lock.yaml` in the paisans fork at `v1.13.3+paisans`), and
the image bakes in `LOCK_DSN=flock`, a lock file inside one container. Two of
Mbin's locks exist to stop two messenger consumers doing the same work at once:
`UpdateActorHandler`'s `update_actor_<hash>` lock, and the scheduler lock the
fork is adding so only one consumer generates the recurring schedule. The
toolkit runs two consumers per apps site, on every apps site, so under `flock`
neither lock protects anything, even on one site.

The toolkit renders two variables:

```
LOCK_DSN=flock
CLUSTER_LOCK_DSN=postgresql://talk:...@10.44.0.1:5000/talk?serverVersion=18&charset=utf8
```

`CLUSTER_LOCK_DSN` is `DATABASE_URL` unchanged, reached the same way: through
the site's HAProxy on its mesh address for a clustered app (rule 2), the
`postgres` container beside it for a pinned one, as the app's own role. **It is
provisional: no fork release reads it yet.** The planned fork change gives the
two locks above a named `cluster` lock resource: a Postgres advisory lock store
on a Doctrine connection of its own, wrapped so that a failed lock call closes
the connection and retries once. Until an image carrying that change is
deployed, the variable is inert and both locks stay on `flock`, which is what
runs today.

**Why the default store stays `flock`.** The first version of this change set
`LOCK_DSN` itself to `postgresql+advisory://...`, and review found it would
have broken posting. With a lock store configured, every Symfony rate limiter
takes the default lock factory (framework-bundle v7.4.14,
`FrameworkExtension.php:3465-3466`) and acquires it on every reservation, and
Mbin rate-limits votes, entries, posts, comments, magazines and the API. Those
run in FrankenPHP's web workers, which loop for the life of the container. The
advisory store opens a private Doctrine DBAL connection (`new Configuration()`,
no middleware), and DBAL 4.4.3 reopens a connection on its own only after an
error it classes as a lost connection, which for Postgres is a
`terminating connection` message (`ExceptionConverter.php`), not a socket
closed under it. HAProxy closes a session idle for `timeout client`, 30
minutes. A web worker that went half an hour without a rate-limited action
would then fail every vote, post and comment until the container restarted.
Mbin's own connection does not have this problem: DoctrineBundle's
`idle_connection_ttl` (600 s) reopens it in web workers, and Mbin closes it
after every message in consumers. Limiter state lives in the site's own Valkey,
so a lock across sites buys a limiter nothing; leaving them on `flock` costs
nothing.

**Why advisory locks for the rest.** An advisory lock is held by a database
session and is visible to every session on the same server, so every consumer
on every site contends for the one lock, because every site's HAProxy routes to
the one primary. Advisory keys are scoped to a database, so another app's locks
on the same cluster cannot collide. A lock is released when its session ends,
so a consumer that dies takes its locks with it.

**They work through HAProxy** because HAProxy runs Postgres in `mode tcp`: a
client's session is one TCP connection to the primary for as long as HAProxy
keeps it open, which is what a session-level lock needs. A transaction-pooling
proxy (Eg: pgbouncer in transaction mode) would silently break them, and must
not be put in front of an app that uses them.

**What the locks do in a failure.**

* **Switchover or failover.** The old primary's sessions end, and advisory
  locks are never written to WAL, so the new primary starts with none. Holders
  are not told; for a moment two consumers may each believe they hold the same
  lock. For an actor refresh or a daily cleanup that is harmless.
* **An apps site dies while the primary lives elsewhere.** The primary does not
  see the site's sessions end until TCP keepalive gives up on them. Spilo
  4.1-p2 sets `tcp_keepalives_idle` 900 and `tcp_keepalives_interval` 100, so
  with the kernel's default nine probes that is about 30 minutes, during which
  the dead site's locks stay held. A held actor lock makes the surviving site
  skip that actor's refresh, which the next fetch queues again; a held
  scheduler lock delays the daily schedule. Lower keepalives would shorten
  this; the toolkit does not set them yet.
* **A consumer whose lock connection died** (idle cut, switchover) fails a
  lock until the planned reconnecting store reopens it. Without that wrapper
  it fails until the process restarts, which `--time-limit=3600`, a crash on
  the scheduler poll, or the restarts `deployment apply`, `site add` and `failover test`
  make when they move the database path (see "`deployment apply` restarts the apps whose
  database path changed") all bring about. The failed message is retried.

### Mbin's queues are in Postgres by default

Mbin hands every federated activity and background job to Symfony Messenger.
Upstream runs RabbitMQ for it, and so did this toolkit, one broker per Mbin
stack, so one per apps site. Mbin's inbox controllers answer 200 as soon as an
activity is on the broker, and a remote server that got the 200 never sends it
again. An apps site lost with work queued therefore loses it for good, and
deliveries it had queued for other servers are stranded with it; the other
site cannot see either.

So the queues live in the database by default (`settings.queue: postgres`):

```
MESSENGER_TRANSPORT_DSN=doctrine://default?check_delayed_interval=1000&redeliver_timeout=900
```

Symfony's Doctrine transport keeps each message as a row in the app's own
database, which is synchronously replicated, and consumers on every site take
rows with `SELECT ... FOR UPDATE SKIP LOCKED`, so the surviving site's
consumers carry on with the dead site's backlog. There is no `rabbitmq` or
`amqproxy` in the stack, and no broker credential in its `.env`.

* `doctrine://default` is the app's own connection, so it reaches the database
  exactly as `DATABASE_URL` does, through the site's HAProxy or the pinned
  `postgres`. A message a handler dispatches is inserted inside that handler's
  transaction and commits or rolls back with its writes.
* `check_delayed_interval=1000` (milliseconds; the default is a minute). One
  consumer process reads every queue over one session, and a LISTEN/NOTIFY
  wake-up popped by one queue's receiver is lost to the others, which then wait
  for this poll.
* `redeliver_timeout=900` (seconds; the default is an hour). A message a dead
  consumer had taken is handed out again after it. It must stay longer than any
  handler runs; Mbin's HTTP client caps a request at 15 seconds.

**It needs the paisans fork.** Upstream's `messenger.yaml` gives every
transport AMQP options the Doctrine transport refuses (`Unknown option found:
[queues, exchange]`), and names no queue, so on `doctrine://` they would all
share one. The fork's `App\Messenger\DoctrineTransportFactory` drops those
options and gives each transport its own queue name, leaving the YAML and the
AMQP path untouched. It ships from fork release `1.14.0-paisans`, the toolkit's
default image. An app that declares an older Mbin image under `images` must
also declare `settings.queue: rabbitmq`, or its queues fail at the first
dispatch.

**RabbitMQ is an opt-in**, `settings.queue: rabbitmq`, rendered exactly as
before, for an operator on one large site who wants the broker's throughput.
`validate` warns (`mbin-rabbitmq-across-sites`) when it is chosen for an app on
more than one apps site, and refuses any other value (`mbin-queue-unknown`).

**Postgres is the default even on one site** (founder decision). With
RabbitMQ the default for one site, adding a second would change the backend,
and `site add` would carry a queue drain: an addition would become a
migration.

**Switching an existing stack is not automated.** `deployment apply` renders the new
`.env` and compose file, but does not drain the old broker, and compose leaves
the removed `rabbitmq` and `amqproxy` containers running as orphans. Run one
consumer against the old AMQP DSN until `messenger:stats` reads nothing and the
broker's `delay_*` retry queues are empty (retries can be up to about 21 hours
out), or accept losing pending retries; then `docker compose up -d
--remove-orphans` in the stack.

What it costs: every message is at least three commits (insert, take,
delete), each waiting for the synchronous replica on the other site, and the
table churns, which is vacuum's work. For a community of this size that is
noise; an instance that outgrows it is the operator `queue: rabbitmq` exists
for.

### Mbin sends little mail, and says where from

Every member signs in to Mbin through the identity provider, so Mbin sends
little mail of its own: its `/contact` form, and the few account mails it
still has (resending an activation link to an unverified account, a password
reset, an email change). It sends no notification or digest mail. That is
still mail a community wants delivered, a contact form message above all, so
the kind reads the `smtp` block like the other two (see *Mail is declared once
and overridden per app*) and renders three variables into its `.env`.

**Mbin with a client at the identity provider is rendered `SSO_ONLY_MODE=true`,
because mail makes its own registration real.** The image's defaults leave
password registration at `/register` open. While mail goes nowhere its
verification link never arrives, but a working transport delivers it, and
anyone who could reach the page could make an account the provider and its
member group never see. `SSO_ONLY_MODE` closes `/register`; accounts are still
made on first sign in.

**`MAILER_DSN`** is the transport, the only place Symfony Mailer reads it
from (`config/packages/mailer.yaml`). It is built in Go from the app's
effective `smtp` settings, so it can be tested:

| `security` | `MAILER_DSN` | What Symfony does |
|------------|--------------|-------------------|
| `tls` | `smtps://user:pass@host:port` | implicit TLS from the first byte |
| `starttls` | `smtp://user:pass@host:port?require_tls=true` | STARTTLS, and refuses to send if the server does not offer it |

`require_tls` is there because plain `smtp://` upgrades only when the server
offers STARTTLS and otherwise sends in plaintext (`EsmtpTransport.php` at
symfony/mailer v7.4.14); with it, a stripped STARTTLS stops the mail instead
of exposing the password. The port is `port`, or 465 and 587 as for the other
kinds. The username and password are percent encoded in full, because Symfony
splits the DSN with PHP's `parse_url` and an `@`, `:`, `/`, `#` or `?` a
provider issued would otherwise move the split; with no `username` there is no
userinfo at all. A password holding all of those, with quotes, `%`, `$` and a
space, was rendered, read by Docker Compose v5.4.0 as an `env_file`, and
parsed by symfony/mailer v7.4.14 on PHP 8.4 back to the same bytes.

With **no SMTP host**, `MAILER_DSN` is `null://null`, Symfony's discard
transport. The image's own baked `.env` names `smtp://mailserver`, a host
that does not exist in this stack, so leaving the variable out would make
every send fail rather than be dropped. `validate` warns,
`mbin-without-smtp`: contact form messages and those account mails are
discarded, and the app otherwise works, which is why it is a warning.

**`KBIN_SENDER_EMAIL`** is the From address of every mail Mbin sends: the
effective `from_address`, or `noreply@<hostname>` when none resolves, an
address Mbin accepts and nothing sends from while the transport is the null
one. Mbin builds every sender as `new Address(KBIN_SENDER_EMAIL, KBIN_DOMAIN)`
(ResetPasswordController, ContactManager, SentUserConfirmationEmailHandler,
SendApplicationAnswerMailHandler), so **`from_name` is not used**: the
display name is always the instance's hostname. Symfony's `Address` throws on
anything but an address alone, so a `from_address` like `Talk
<talk@example.org>` is refused, `mbin-smtp-from-not-an-email`, since no mail
could be sent with it. A mail server and no sender is a warning,
`mbin-smtp-without-sender`: Mbin then sends as `noreply@<hostname>`, which
`dns init` publishes as sending no mail unless it is a mail domain, so
receivers are likely to reject it.

**`KBIN_CONTACT_EMAIL`** is `settings.contact_email`, the admins' address:
where the contact form sends each message, the address `/api/info` publishes,
and the subject of Mbin's Web Push VAPID token. Without it, it is the sender
address, so a message reaches whoever reads the sending mailbox rather than
nobody. Anything but an address alone is refused,
`mbin-contact-email-not-an-email`, because each of those three uses needs one.
Nothing checks that the address receives mail: one at `community.domain`
needs that domain in `community.mail_domains`, or `dns init`'s null MX refuses
every message sent to it.

```yaml
apps:
  talk:
    blueprint: mbin
    settings:
      contact_email: admins@example.org
```

**Every setting on Mbin's Meta, Instance and Federation admin pages is the
configuration's.** Each is a key under `apps.<app>.settings`, rendered into the
`.env` and locked, so a value left out takes the default below rather than
whatever an admin last saved. Changing one is an edit to `paisans-stack.yaml`
and `paisans deployment apply`. Admins keep the federation allow list's
contents: which instances are allowed stays theirs to edit in Mbin. The
General section's language, theme and JavaScript switch stay theirs as well.

| Setting | Mbin setting | Default |
|---|---|---|
| `title` | `KBIN_TITLE` | the app's `display_name`, else the app's name |
| `meta_title` | `KBIN_META_TITLE` | `<display name> · <community.name>`, or the display name alone with no community name |
| `meta_description` | `KBIN_META_DESCRIPTION` | empty |
| `meta_keywords` | `KBIN_META_KEYWORDS` | empty |
| `downvotes_mode` | `MBIN_DOWNVOTES_MODE` | `enabled`; also `hidden` or `disabled` |
| `header_logo` | `KBIN_HEADER_LOGO` | `false` |
| `registrations` | `KBIN_REGISTRATIONS_ENABLED` | `false`: members join through the account service and the identity provider |
| `sso_registrations` | `MBIN_SSO_REGISTRATIONS_ENABLED` | `true`: a member's first sign in makes their account |
| `sso_show_first` | `MBIN_SSO_SHOW_FIRST` | `false` |
| `avatar_override` | `MBIN_SSO_AVATAR_OVERRIDE` | `true` |
| `captcha` | `KBIN_CAPTCHA_ENABLED` | `false` |
| `mercure` | `KBIN_MERCURE_ENABLED` | `false` |
| `admin_only_oauth_clients` | `KBIN_ADMIN_ONLY_OAUTH_CLIENTS` | `false` |
| `federated_search_only_logged_in` | `KBIN_FEDERATED_SEARCH_ONLY_LOGGEDIN` | `true` |
| `restrict_magazine_creation` | `MBIN_RESTRICT_MAGAZINE_CREATION` | `false` |
| `sidebar_random_local_only` | `MBIN_SIDEBAR_SECTIONS_RANDOM_LOCAL_ONLY` | `false` |
| `sidebar_users_local_only` | `MBIN_SIDEBAR_SECTIONS_USERS_LOCAL_ONLY` | `false` |
| `new_users_need_approval` | `MBIN_NEW_USERS_NEED_APPROVAL` | `false`: the account service already vets them |
| `private_instance` | `MBIN_PRIVATE_INSTANCE` | `true` when gated, which cannot be turned off; `false` otherwise |
| `federation` | `KBIN_FEDERATION_ENABLED` | `true` |
| `federation_page` | `KBIN_FEDERATION_PAGE_ENABLED` | `true` |
| `federation_allow_list` | `MBIN_USE_FEDERATION_ALLOW_LIST` | `true` when gated, which cannot be turned off; `false` otherwise |

The instance title sits beside the logo, and matches what the identity
provider shows at sign in. The meta title is the browser tab, the feed title
and nodeinfo's `nodeName`, what other servers and directories call the
instance. A boolean written as anything but `true` or `false` is refused
(`mbin-<setting>-not-a-boolean`, `avatar-override-not-a-boolean`), a title
that is not text likewise, and so is an unknown `downvotes_mode`
(`mbin-downvotes-mode-unknown`). A gated app that sets `private_instance` or
`federation_allow_list` false is refused (`mbin-gated-needs-private-instance`,
`mbin-gated-needs-federation-allow-list`).

**`federation: false` turns federation off**, and the toolkit then treats the
app as one that does not federate: authorized fetch is no longer required of
it when gated, and the monitor runs no signed fetch check against it.

**`avatar_override: false` keeps each member's picture the identity
provider's.** The fork fetches the `picture` claim again on every sign in and
replaces the avatar when the image has changed. With the default, `true`, a
member may upload a picture of their own instead, which stops the refetch for
them until they remove it. With `false` a member signed in through the
provider cannot upload or remove an avatar.

```yaml
apps:
  talk:
    blueprint: mbin
    display_name: Talk
    settings:
      meta_description: Where the community talks.
      avatar_override: false
```

These need the fork at `v1.16.0+paisans` or later, the first that can lock
every one of them.

**These settings are the configuration's, and an admin cannot change them in
Mbin.** Mbin keeps them, like most of its settings, as database rows once an
admin saves its settings page, and from then on its `SettingsManager` reads
the row and ignores the environment. The `.env` therefore also renders
`MBIN_LOCKED_SETTINGS`, naming every Mbin database setting it sets:
`KBIN_DOMAIN`, `KBIN_SENDER_EMAIL`, `KBIN_CONTACT_EMAIL`,
`MBIN_AUTHORIZED_FETCH`, `MBIN_SSO_ONLY_MODE` (rendered as `SSO_ONLY_MODE`,
true when the app has a client at the identity provider) and every setting in
the table above. See *Mbin and WriteFreely lock what the toolkit renders*.
`MAILER_DSN` is not a database setting and always follows the configuration.

### Mbin and WriteFreely lock what the toolkit renders

Both apps keep most of their settings in their database and offer them on an
admin page, so a setting rendered from `paisans-stack.yaml` would hold only until an
admin saved that page, after which the database would win and `paisans-stack.yaml`
would describe a deployment that no longer exists. Founder instruction: the
toolkit locks every database setting it renders, so `paisans-stack.yaml` stays the
only source and an admin cannot drift from it through the app.

* **Mbin** reads each setting named in `MBIN_LOCKED_SETTINGS` from the
  environment on every read, never writes it on a save, shows it disabled on
  the settings page, and refuses it through the settings API with a 400. A
  name it cannot lock makes it answer every request with an error rather than
  leave a setting silently unlocked. The list is in *Mbin sends little mail,
  and says where from*.
* **WriteFreely** reads each setting named in `config.ini`'s
  `locked_settings`, a line before the first section, from the file on every
  start, never moves it into its database, shows it greyed out on the admin
  page, and refuses it from `writefreely settings set`. A name it does not know
  or a value it would refuse stops it at startup. The toolkit locks
  `app.site_name`, `app.theme`, `app.open_registration`,
  `app.min_username_len`, `app.federation`, `app.public_stats`, `app.private`,
  `app.authorized_fetch`, `app.local_timeline`, `uploads.enabled` and
  `uploads.max_size_mb`. A `min_username_len` or `uploads_max_size_mb` the fork
  would refuse is therefore refused by `validate` first
  (`writefreely-setting-invalid`). `app.federation_allowlist` is not rendered,
  so the allowlist stays the blog admins' to edit.

Each list is derived beside its template, and a test renders the file and
checks both directions against the fork's own list of database settings, so
a setting the template gains without being locked fails the test. A
passthrough key under `config` is the operator's, is not locked, and behaves
as the app's own settings do (see *A config key is placed, not interpreted*).

**Authorized fetch is on by default.** `settings.authorized_fetch` on an
`mbin` or `writefreely` app makes the app refuse an ActivityPub request that
carries no valid HTTP signature, and it is `true` when absent:

```yaml
apps:
  talk:
    blueprint: mbin
    settings:
      authorized_fetch: true   # the default
```

| Kind | Rendered as | What it leaves open |
|---|---|---|
| `mbin` | `MBIN_AUTHORIZED_FETCH`, locked | every `GET` that is not an ActivityPub route, and of those the instance actor, webfinger, host-meta, both nodeinfo routes and the JSON-LD contexts (`AuthorizedFetchSubscriber.php:52-59,83-90` at v1.15.0+paisans). Inbox deliveries are verified separately, always |
| `writefreely` | `[app] authorized_fetch`, locked | discovery (everything under `/.well-known/`, and nodeinfo) and the instance actor (`authorized_fetch.go:35-50` at v0.21.1+wisp). It also requires a signature on every inbox delivery |

What stays open is what a peer needs before it can sign anything: our
instance actor carries the key it verifies our requests with, so gating it
would leave two such servers each waiting on the other. A peer that does not
sign its fetches cannot read either app. `false` turns it off for an ungated app, and a gated app refuses it
(`gated-without-signed-fetch`), as does anything but a YAML boolean
(`authorized-fetch-not-a-boolean`).

**Both answer a status-only `/healthz`.** 200 with an empty body when the
database answers (and, for Mbin, its cache), an empty 503 when not, with no
session and no redirect, on a private instance too. It is a dedicated route,
so on a gated hostname it is open past the gate and the monitor's public check
reaches the app (see *Monitoring*).

These three are what the kinds render and what any release they name must
read. The forks have carried them since Mbin 1.15.0+paisans and WriteFreely
0.21.1+wisp, and the kinds name Mbin 1.15.1+paisans and WriteFreely
0.21.1+wisp.

The known limit: **WriteFreely logs a warning on every start that it could not
add `settings_location = database` to `config.ini`.** The file is mounted
read only, and the fork adds that marker after its first import. Rendering it
is not a fix: the fork refuses to start when the marker is present and its
database holds no settings, which is every new blog's first start. The warning
changes nothing.

### Wiki.js is configured by its bootstrap

A community's wiki is where its admins keep what only admins should read and
its members keep what everyone should, so who may read what has to follow the
groups people are in at Pocket ID, without anyone copying memberships by hand.
The core catalog's `wikijs` blueprint runs Wiki.js 2.5, which does that
itself: its OAuth2 sign-in puts each person in the wiki groups their groups
claim names, on every sign in, and its page rules gate content by path per
group. Every Wiki.js fact below is cited at `v2.5.316`, the release the
blueprint's track was read at, and every Pocket ID fact at `v2.14.0`. The
design is `docs/specs/2026-10-13-wikijs-blueprint.md`, and what was observed
rather than read is in its findings file beside it.

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

**Wiki.js reads only its database from the environment.** Its first admin,
sign-in providers, site URL and groups live in its database and change through
its GraphQL API. So the blueprint runs a second service beside it, the
bootstrap (`Paisans-Software/wikijs-bootstrap`, published as
`ghcr.io/paisans-software/wikijs-bootstrap`), which sets them. It runs once per
start of its container, then stays up and reports healthy once that run has
converged, so `deployment apply`'s health gate waits until Wiki.js is configured (an
exited container counts as failed, whatever its status). A configuration
change therefore reaches Wiki.js when `deployment apply` recreates the stack. It reaches
Wiki.js by service name on the stack's own network, never through the gateway,
and holds no Pocket ID API key.

One run waits for Wiki.js and its database, takes a Postgres advisory lock so
that one instance's bootstrap at a time does the rest, finishes setup if the
database is empty, signs in as the break glass admin, and sets:

* the sign-in strategies: `pocketid`, Wiki.js's OAuth2 module pointed at
  Pocket ID, first, and the local strategy after it;
* the site: its public URL, the title, description and logo from the settings,
  auto-login on, the local sign-in form hidden, the proxy trusted, and the
  upload limit;
* the built in `Guests` group with no permissions and no page rules, so the
  wiki is sign in only;
* the admin group's permissions;
* every `synced_groups` group missing from Wiki.js, created with Wiki.js's
  defaults.

Setup is finished only on an empty `settings` table and under the lock,
because Wiki.js's `/finalize` deletes every user and every group before it
seeds (`server/setup.js:204-223`): a second call against a wiki in use would
empty it. An instance that started before another finished setup keeps
serving the setup page until it restarts, so the bootstrap shares the app's
process namespace and runs as its user, uid 1000, to send it `SIGTERM`; its
restart policy brings it back reading a set up database. Those are the
bootstrap's only writes, each logged on a line of its own. It never deletes a
group or a user, never changes a group's members, never edits a group other
than `Guests` and the admin group, and never edits a page, because a service
the deployment runs is approved as a whole only while every kind of write it
makes is named.

**`/finalize` is closed at the gateway.** It takes no authentication and no
CSRF token (`server/setup.js:76`), so whoever reaches it first while the wiki
is in setup mode owns the wiki. The blueprint's route refuses it on the wiki's
hostname in every form, for every method, always. The app port on the mesh
address is not gated, like every app's, so a mesh peer reaches `/finalize`
there whenever an instance is in setup mode: the first deploy's setup window,
or up to about two minutes on a stale clustered instance before its bootstrap
stops it. Outside those windows Wiki.js does not serve it at all, since a set
up instance never mounts the setup routes.

**Pocket ID is the only way in for members, through Wiki.js's generic OAuth2
module asking for the `groups` scope.** Pocket ID sends the `groups` claim only
to a client that asks for that scope (`oidc/claims_service.go:157`), and the
OAuth2 module passes on whatever scope it is configured with
(`server/modules/authentication/oauth2/authentication.js:21`), so the
bootstrap configures `openid profile email groups`. The client's callback, PKCE and groups are in
the table under *`oidc client create` makes an app's client at Pocket ID*. The
Pocket ID client stays restricted to the member and admin groups, as every
app's is: that restriction is who may sign in at all, and the groups claim
decides only what a signed in person may do. Pocket ID asks nobody to consent,
since its consent screen is off for every client the toolkit makes, and
`sso_consent: true` turns it on (same section).

**Auto-login is on at both layers.** Wiki.js sends `/login` to its first
strategy, `pocketid`, and the gate's rule sends `/` and `/login` there for a
member it already let through, so a member with a Pocket ID session comes back
signed in with no prompt. A deep link takes one click: Wiki.js answers a guest
asking for any page but `/` with its refusal page and a Login button, keeping
the page in its `loginRedirect` cookie, and after the click the member lands on
it (observed). Founder decision: the one click stays (see *The visibility gate
is a per-app property*).

**Membership is Pocket ID's; what a group may do is Wiki.js's.** On every sign
in Wiki.js adds the person to each existing group their claim names and,
when the claim is present, removes them from every group it does not
(`oauth2/authentication.js:36-48`: group mapping, which the bootstrap turns on,
acts only on a groups claim that is a list), matching names exactly and case
sensitively (`:40`). A person added to a group
by hand in Wiki.js loses it at their next sign in, so every group a member
should be in is a Pocket ID group and is listed in `synced_groups`, which
defaults to the admin group and the member group. The bootstrap creates each
one missing in Wiki.js, so a new wiki has its groups before anyone signs in,
and `deployment apply` creates each one missing at Pocket ID, empty, through the
blueprint's `oidc.extra_groups_setting`. Creating an identity provider group
beyond an app's own takes explicit approval under
`docs/deployment-agent-rules.md`, so `deployment apply` lists those groups and asks for
the word `create` at a terminal, or takes it from `--yes`; without either it
holds the app back and sends Pocket ID nothing. They are never added to the client's allowed groups, so who
may sign in is unchanged. Permissions and page rules are set by the
community's admins in Wiki.js: gating a set of pages by path, Eg: `/admin/` for
the admin group only, is a page rule an admin writes.

**The admin group gets every permission except `manage:system` and
`manage:api`.** Founder decision. Each setting the bootstrap owns is a
`manage:system` mutation (`site.graphql:69`, `authentication.graphql:70-91`),
and someone holding `manage:groups` without `manage:system` cannot grant it,
change a group that has it, or put anyone in one
(`server/graph/resolvers/group.js:50-64,181-195`). `manage:api` goes with it
because an API key made with full access acts as `Administrators`
(`server/models/apiKeys.js`), which makes `manage:api` `manage:system` by
another route. So only the built in `Administrators` group has it, and only
the break glass admin is in it. A change in Wiki.js to anything the bootstrap
sets is possible only for that admin and is undone at the bootstrap's next run.
`site_title`, `site_description` and `logo_url` are settings for the same
reason: changing them in Wiki.js needs `manage:system`, which no member has.
`logo_url` must be reachable without signing in, because the sign-in page shows
it to someone who has not; a logo uploaded into the wiki is not, since
`Guests` cannot read assets.

`validate` refuses a `synced_groups` that leaves out the admin group, because
nobody but the break glass admin could then administer the wiki; that refusal
is provisional, inferred rather than decided. It also refuses a name the
bootstrap's environment file could not carry unchanged, a built in group
(`Administrators`, `Guests`) as either group, an empty `site_title`, and an
`upload_max_mb` outside 1 to 1024.

**Uploads live in Postgres, so a wiki has no bucket** (Rule 3). Wiki.js caps an
upload at `upload_max_mb` MiB per file, 5 by default
(`server/app/data.yml:96-98`), and the gateway's request body limit for the
hostname follows it with room for the request's framing, because a limit lower
at the edge than in the app fails an upload with no message a member can read.
The one path the image declares as a volume, `/wiki/data/content`, is bound
under the stack's directory like every other (*Every volume an image declares
is mounted, and `deployment apply` checks it*).

**Cluster placement works from the first version.** Founder decision. Under
`cluster` Wiki.js runs on every apps site against one database, with
`HA_ACTIVE=true`: each instance listens on Postgres `LISTEN/NOTIFY` and reloads
configuration, sign-in and pages when another changes them
(`server/core/db.js:229-264`). Each instance also has its own bootstrap, which
converges the instance it sits beside. A clustered wiki connects with
`DATABASE_URL` ending `?ssl=no-verify`, which is encrypted without checking the
cluster's certificate, what `sslmode=require` means to the other apps. The
parameter differs because the connection string parser in Wiki.js's image
ignores `sslmode` and reads `ssl`: with `sslmode` it connected without TLS, and
the cluster's `pg_hba.conf` refuses every connection without it (observed). A
pinned wiki talks to the Postgres in its own project, which serves no TLS, and
its DSN has neither. Still reasoned, not observed: that the `LISTEN` reconnects
after a Patroni failover moves the primary (it did across a restart of one
Postgres), and a sign in completed through a second instance.

The blueprint is gateable. Founder decision. Once `Guests` has no permissions
Wiki.js refuses guests every page, upload and GraphQL query, but before sign in
it still writes an upload to disk before checking permission
(`server/controllers/upload.js:13-22`) and answers differently for a page that
exists and one that does not (`server/controllers/common.js:144-170,260-271`).
The gate closes both, and puts everything Wiki.js exposes before sign in
behind Pocket ID; the OAuth2 start and callback paths stay open, as an app's
own sign-in routes are for every gateable blueprint.

#### Recovering the break glass admin

The break glass admin is Wiki.js's user 1: a local account, `wikijs-admin@`
the community's domain, in `Administrators`, made by setup with the wiki's
`local_admin_password` from the secrets file. It is how the bootstrap signs in
and the one account with `manage:system`, and Wiki.js will not remove it from
`Administrators` (`server/graph/resolvers/group.js:137`). Its password is kept
rather than rotated (see *`secrets rotate` replaces a secret where it is
checked and where it is used*).

**Hiding the local form hides it from the page, not the account.** The
bootstrap sets `authHideLocal`, so Wiki.js's sign-in page never shows the
local form to a member, but Wiki.js still keeps a legacy sign-in page for old
browsers that ignores it. A request for `/login` with `legacy` in its query
string, or from a user agent containing `Trident`, gets that page, skipping
auto-login and `authHideLocal` (`server/controllers/auth.js:28-34` at
`v2.5.316`); it lists every form strategy, `local` among them
(`server/models/authentication.js:46-57`); and a `POST /login` under the same
condition signs in with no `authHideLocal` check (`server/controllers/auth.js:101-113`).
What protects the account is therefore the gate on a gated wiki, the generated
password, and Wiki.js's sign-in rate limit on that route (`bruteforce.prevent`,
`:101`), not the hidden form. The same legacy page is the recovery: when an
admin needs `manage:system` (a setting the bootstrap does not own, or a wiki
whose admin group was lost), it needs no change to the wiki at all.
**Reasoned from Wiki.js's source at `v2.5.316`; nobody has run it yet.**

1. In a browser, set the user agent to one containing `Trident` (a browser's
   developer tools can override it). The legacy form posts to `/login` with no
   query string (`server/views/legacy/login.pug:10`), so `?legacy=1` alone
   shows the form and the sign in is then redirected back to the modern page;
   the user agent carries both requests.

2. Open `https://<hostname>/login`. On a gated wiki this needs a Pocket ID
   session the gate admits. The gate's auto-login rule matches `/login`
   whatever its query string, because Caddy's `path` matcher reads the path
   alone, so a browser holding the gate's session and no wiki session is sent
   to Pocket ID once; the rule then stands aside for two minutes (its loop
   breaker cookie). Open `/login` again.

3. Choose the local strategy and sign in as `wikijs-admin@<community domain>`
   with the wiki's `local_admin_password`.

4. Make the change, sign out, and drop the user agent override. Anything
   changed that the bootstrap sets (*Wiki.js is configured by its
   bootstrap*) is put back at its next run, which every restart of the app and
   every `deployment apply` that recreates the stack starts.

### Pocket ID runs on every apps site, and one of them is active

Pocket ID v2.14.0 admits one instance per database. Its actor runtime,
francis, caps the hosts a database may register at one unless
`EnvConfig.HAEnabled` (`backend/internal/bootstrap/actors_bootstrap.go:47-53`),
and that field is "intentionally not bound to an environment variable while
HA support is still being completed" (`internal/common/env_config.go:91-94`).
A second instance is refused at admission, which counts only hosts whose last
health check is within 90 s with HA off (francis v0.1.0-beta.23,
`components/postgres/postgres-cluster.go:55-93`), logs "it appears that
there's already one instance of Pocket ID running" and exits 1, the status of
every other failure (`bootstrap.go:126-128`, `cmds/root.go:21-24`). A
refused instance never starts its router (`bootstrap.go:121`), so its port is
closed.

So Pocket ID with `cluster` placement renders on every apps site like any
other clustered app, and every site runs it through a standby wrapper,
`/srv/paisans/<token>/<app>/paisans-standby.sh`, mounted read only and set as the service's
entrypoint, with the image's own entrypoint and command as its arguments:

* it streams the child's output to the container's log, keeping the last 50
  lines;
* it forwards a stop to the child as SIGTERM, so an active instance shuts down
  cleanly and deregisters (francis `host/local/host.go:377-393`), and
  `stop_grace_period` is 30 s so that outlasts Pocket ID's own 10 s actor grace;
* on a non zero exit whose last lines carry the refusal, it touches
  `/tmp/paisans-standby`, waits `PAISANS_STANDBY_RETRY` seconds (15 by
  default) and tries again;
* on any other exit, it exits with the child's status;
* it keeps the state file, and its copy at `/paisans/run/standby` that the
  operator reconciler reads, through each retry, so a waiting standby never
  reads as down while its retry is being refused, and removes it once the
  retried instance is admitted: when the image's healthcheck first passes,
  asked every second, or after `PAISANS_STANDBY_HOLD` seconds (10 by default)
  of running without passing it, so an admitted instance that never serves
  reads as down rather than as a standby.

The healthcheck becomes `[ -f /tmp/paisans-standby ] || /app/pocket-id
healthcheck`, so a standby is healthy, and `deployment apply`'s gate passes it like any
other stack. Whichever site starts first wins; when it stops cleanly, a
standby takes over at its next retry, and when it dies, within the 90 s its
registration takes to age plus a retry. Both were observed with two real
v2.14.0 instances against one Postgres: a clean stop handed over in about two
seconds at a 3 s retry, and a `docker kill` of the active one in 49 s.

**The refusal text is recorded once for Pocket ID v2**, in
`kinds.PocketIDStandbyMarker`. The foundation's track cannot leave v2, so a
release that changes the text is a toolkit change, made when the next major
is, and `validate` warns (`pocket-id-standby-marker-unknown`) for an
operator's own image from another repository. **It fails safe**: a marker that stops matching makes the
wrapper exit like any other failure, Docker's restart policy retries it more
slowly, and the standby site's stack reads `restarting`, which `deployment apply`'s gate
reports. Nothing runs twice; francis refuses the second instance either way.

**Exactly one is active, and the toolkit checks it.** No one stack can see
that, and the gateway answers from whichever site is first, so after any
`deployment apply --execute` that acted on the stack, and in `failover test`'s preflight
and after each switchover, the toolkit asks every apps site the app runs on:

```
pocket-id auth: one active instance
  home-a   active      /healthz answered on 10.44.0.1:1411
  home-b   standby     another instance holds the database
```

A site is `standby` when the state file is present, `active` when `/healthz`
answers on its mesh address (the address and port the gateway dials), `down`
otherwise, and `absent` before it has the stack. None active is waited for, up
to three minutes, then fails: sign in is down. Two active fails at once,
since waiting would leave both serving. A site that cannot be reached is shown
and not counted. `operator magic-link` and `oidc client create` call the active
site the same way unless `--site` names one, and so does `deployment apply`'s client step.

**The gateway needs nothing new.** The route lists every apps site in order
under `upstream_failover`, as every multi site app does, with no active
health check. A request finds the active site by a dial to the standby's
closed port, which is refused at once and marks only the standby down for
`fail_duration`. That is reasoned from the closed port; Docker's userland
proxy accepting a connection it cannot forward would turn it into a failed
request instead, and the first multi site run should look.

**A service that calls the API reaches the active instance through
HAProxy.** Every site that runs HAProxy, in a deployment with a pocket-id
app, renders a second listener beside the cluster port, on its mesh address
at port 1413:

```
listen pocket-id
    bind 10.44.0.1:1413
    option httpchk GET /healthz
    http-check expect status 204
    default-server inter 3s fall 3 rise 2 on-marked-down shutdown-sessions
    server home-a 10.44.0.1:1411 check
    server home-b 10.44.0.2:1411 check
```

One `server` line for every site that runs the pocket-id app, at its mesh
address and Pocket ID's port. A standby refuses connections on that port, so
its check fails and only the active instance is up, and a takeover moves the
route within three failed checks, nine seconds, with no change on any site.
It is on every HAProxy site rather than only where a caller runs, so a
service added later that needs the API finds it already there. The account
service is its caller today, and the static API key it sends never leaves the
mesh. That is reasoned from the standby's closed port, not yet observed
through a takeover.

**When upstream binds HA mode to a variable**, delete the wrapper and the
healthcheck override and set it: every site then serves, and the check
becomes "at least one active". Until then, Mbin on a second apps site has
limits of its own, recorded in `docs/specs/2026-10-07-pocket-id-standby.md`:
its cache is per site, its scheduled tasks run per site until the fork's
scheduler lock lands, and the second site's web workers serve only during a
failover. Its sessions are in Postgres and survive one.

### The operator reconciler checks Pocket ID against the operators

Pocket ID is the record of who is a member of the community; the `operators:`
section is the record of who runs the machines. Nothing else holds the two to
each other, and an operator whose account was banned, disabled or taken out
of a group would keep their SSH keys and secrets while losing the sign-in a
recovery needs. So the `pocket-id` blueprint runs a second service beside
Pocket ID on every site it runs on, `reconciler`, released from
`Paisans-Software/paisans-operator-reconciler-service` and published as
`ghcr.io/paisans-software/operator-reconciler`, the core catalog
`foundation.yaml`'s `operator-reconciler` on the track `0.3.*`.
`docs/specs/2026-10-14-operators-and-management.md` is the approved design,
and `docs/specs/2026-10-08-admin-reconciler.md` is kept as the record of an
earlier one.

**It reads and never writes.** Pocket ID is the record of membership, so the
reconciler reports when it disagrees with the yaml rather than changing
either one. Its only requests are `GET`s, and its tests fail on any other
method. Every two hours, and once when it starts, it reads Pocket ID's users
and the groups it checks (after one minute instead when it could not read
Pocket ID, which starts alongside it), and holds them to four checks:

1. **Every operator in the yaml is a member.** For each operator, a Pocket ID
   user with that username exists, is not disabled, and is in the community's
   member group and in `operators`. A failure names the operator and what is
   missing, Eg: `operator alice is not in operators`. A group that does not
   exist is one failure of its own (`no group named operators`) rather than
   one per operator.
2. **Every Pocket ID administrator is an operator.** Every enabled user with
   `isAdmin`, the static API key's own user
   (`00000000-0000-0000-0000-000000000000`) left out because it is the
   reconciler's credential rather than a person, is an operator. Under
   `management: paisans` there is none at all, so any one is a failure (see
   *Who manages accounts: `management`*). An administrator nobody declared
   holds all of Pocket ID outside the yaml, the review and the signed
   history.
3. **Every member of `operators` is an operator.** Every enabled user in the
   group `operators`, the static API key's user left out as above, is an
   operator in the yaml. A failure names them, Eg: `carol is in operators and
   not an operator`. An operator taken out of the yaml keeps the group until
   someone removes it at Pocket ID, and every app that reads `operators` for
   operator access goes on granting it, so the pass says so rather than
   letting the yaml and the group drift apart.
4. **At least two operators are declared.** A yaml with one operator fails,
   Eg: `only one operator, alice: declare a second so one lost workstation
   cannot stop the deployment`. `validate` only warns about one, so that a
   deployment can run while a second is found, and this check keeps the
   monitor saying so until one is.

**An operator banned or removed at Pocket ID fails check 1 until they are
taken out of the yaml.** That is a manual step by design: removing an operator
from `operators:`, and their name from every site's `ssh.keys`, is how an
operator is removed, and the next `site prepare` of each site takes their keys
off it, which the reconciler cannot do and must not guess at. `deployment apply` never deletes or disables a Pocket ID user, so the
person stays a member until the community removes them.

`/healthz`, published on the site's mesh address at port 1412, serves the
last pass: 200 when every check passes, and 503 when any fails, when Pocket
ID does not answer, before the first pass, and once the last pass is more
than two and a half hours old. The body is one line naming each failure by
username, never by email, and a standby's is `standby`, with a 200. The
uptime blueprint does not ask it directly: the active instance's
`operators-heartbeat` pushes what it says to the monitor's
`operator continuity` (*Monitoring*), so a deployment whose operators and
Pocket ID disagree gets an incident from whichever site is active.
The monitor's per site check asks `/livez`, which fails only when the pass
loop has hung, and so does Docker's healthcheck: a new deployment has no
operators at Pocket ID until `deployment apply` makes them, and `deployment apply`'s health gate
must not hold the Pocket ID stack back on that.

It reads what it checks from the rendered `.env`, and refuses to start
without it, since a reconciler that knew no operators would report a
deployment healthy that it had not checked:

| Variable | Value |
|---|---|
| `PAISANS_OPERATORS` | the operators' usernames, comma separated, sorted |
| `PAISANS_MEMBER_GROUP` | the community's member group, `members` |
| `PAISANS_MANAGEMENT` | `paisans` or `pocket-id` |

**It reaches Pocket ID as `app` on the stack's own network**, with the
`STATIC_API_KEY` the same `.env` already carries, so the key is on no host and
in no stack it was not already. **Beside a standby it does nothing:** the
standby wrapper writes its marker to `/srv/paisans/<token>/<app>/run/standby` as well as to
`/tmp`, the reconciler mounts that directory read only, and a reconciler that finds the
marker reports `standby` with a 200.

**Its image is released from its own repository**,
`Paisans-Software/paisans-operator-reconciler-service`, where only a `vX.Y.Z`
tag publishes. A change in this repository reaches a deployment's
reconciler only through the environment the blueprint renders for it.

**The groups it reads are `operators` and the member group.** `validate`
warns (`admin-group-not-admins`) about an app, in a deployment running Pocket
ID, whose admin group is neither `admins` nor `operators`, since that app's
administrators are a group that neither the community nor the operators
section decides.

### The reconciler checks the DNS provider's account

Whoever controls the zone controls every hostname the deployment serves. A
Cloudflare account whose Super Administrators have all gone keeps resolving,
and its Administrators keep editing records, but Cloudflare's Administrator
role "Cannot manage members"
([Cloudflare: Roles](https://developers.cloudflare.com/fundamentals/manage-members/roles/)),
so nobody can add the next administrator, and once the last one leaves the
zone is recovered only at the registrar.

**Every operator must be an accepted Super Administrator of every account
holding the zone or a zone above it, and there must be at least two.** The
operators are the ones held to it because they run the machines and hold the
credentials a recovery needs, and the floor of two is `validate`'s floor of
operators (*Keys belong to operators, and sites name operators*): one is a
single loss from none. Administrator with access to the zone does not count,
a pending invitation does not count, and Cloudflare members who are not
operators are allowed. An operator is found by their Pocket ID username, and
compared by the address their Pocket ID account holds; one with no enabled
account is left out here, since the reconciler's first check already reports
them. Founder decision; `docs/specs/2026-10-13-dns-admin-continuity.md` is
the record of the design.

**The check is optional, and Cloudflare only.** It renders when
`acme.provider` is `cloudflare` and `external.dns_members_token` is pasted: a
token with Account Settings Read on each account and Zone Read on every zone
above the domain (a zone it cannot read is skipped silently, as if absent),
and nothing else. It is not `acme_dns_token`, which edits records and lives
only beside Caddy, because this one is rendered into the Pocket ID stack on
every apps site, and a leak there must not be control of every hostname.

**It runs on the reconciler's pass**, on the active instance only, and makes
two kinds of read at Cloudflare: every active zone holding `community.domain`
or a name above it, asked suffix by suffix as `dns init` finds a zone, for
their accounts, and each account's members. A parent zone counts because its
account controls the delegation to a child, so an operator must be a
Super Administrator of every such account; usually there is one.
It writes nothing there. It answers on `/healthz/dns-super-admins` and
`/healthz/dns-2fa` with `/healthz`'s contract, and the operators heartbeat
pusher relays both (*Monitoring*). Each body is a count of operators, such as
`2 of 2 operators are cloudflare super admins` or
`1 of 3 operators not cloudflare super admin (1 pending)`: the monitors exist to
say the community is at risk, and the reconciler's log names who by Pocket ID
username. When Cloudflare cannot be read, both fail with
`cloudflare unreadable:` and the status, never the response or the token.

**An operator whose address at Cloudflare differs** is mapped in the secrets
file, `operators.<operator>.dns_provider_email`, by the operator's key in
`paisans-stack.yaml`, and set with `paisans secrets set
operators.<operator>.dns_provider_email`. The configuration is copied to every site in plaintext,
and the secrets file only encrypted, so the address stays where only a
workstation reads it. No address reaches a host: `deployment apply` renders each mapping
as two HMAC-SHA256 hashes under `apps.<app>.dns_email_hash_key`, and the
reconciler compares every address under the same key. `render` refuses a
mapping that names no declared operator, or that would let one membership
count for two operators. A host taken over while the reconciler runs is not protected:
the token can list the account's members directly, which is why it reads and
does nothing else.

### `deployment apply` checks free space before it pulls

A small host fills up with images. The first real host had a 10 GB root disk,
and with the infrastructure stack and Mbin deployed it was 81% used with 1.8 GB
free; one Mbin application image is about 1.4 GB, and an upgrade pulls the new
one while the old one is still on disk. A pull that runs out of space fails
part way through `docker compose up -d`, after the old containers have
stopped, and a full disk takes the database and every log with it.

So `Build` asks the host which of the site's rendered images it already has
(`docker image inspect`, one round trip for all of them). When a stack that
will be recreated names one it does not have, the action will pull it, and
`deployment apply` reads the free space on Docker's data root (`docker info --format
'{{.DockerRootDir}}'`, then `df -B1 --output=avail`). Below the threshold,
`deployment apply` refuses before writing anything, says how much is free and how much it
needs, quotes what `docker system df` says is reclaimable, and names
`--min-free`. The dry run shows the same numbers on its `disk space` step: the
free space on the step's line, and under `--verbose` a `disk:` line with the
threshold and the images to pull. A
`restart` pulls nothing and is not checked, and nor is a plan that moves no
stack.

The threshold is 3 GiB, one Mbin image and room beside it, and `deployment apply
--min-free <size>` (Eg: `2G`; K, M, G and T are binary, as `df -h` means them)
changes it for one run, for an operator who knows the pull fits.

**The threshold is a fixed floor.** A registry's manifest gives the
compressed size of each layer, not what it unpacks to, and layers already on
the host are shared, so a pull's size cannot be known in advance without
registry credentials from the host. A fixed floor is crude and is right about
the case that matters: a small disk nearly full. **It refuses** for the reason
every other gate refuses: the failure it guards against happens with the old
stack already stopped.

### `deployment apply` prunes the images it superseded

The free space check above refuses an upgrade on a full disk; this is what
keeps the disk from filling. Every upgrade leaves the old image behind, and on
a small host a second Mbin image is most of the space left.

So after a stack's action passes the health gate, `deployment apply` removes images that
are all three of:

* from a repository that stack's compose file names,
* not an image any stack of this site renders, by ID or by tag, so two apps
  pinning different tags of one repository never prune each other's, and
* not used by any container, running or stopped, by name (`docker ps -a`) or by
  image ID (`docker container inspect`), since a container whose tag moved on
  shows only the ID.

Each goes with `docker image rm <id>`. A removal that fails is a warning and
not a stop: the stack is already healthy, and an image left behind costs disk,
not service. The dry run lists what is on the host and would be superseded as
a `prune old images` step under each stack, which under `-v` names each image
as `repository:tag` (or `repository@id` when it has no tag left); `Execute`
reads the host again
after the gate, because the action has just moved containers off the old
image. Waiting for the gate is the point: until the new image is healthy, the
old one is what a rollback would run. `deployment apply --keep-images` skips pruning for
one run, for an operator who wants to keep that image a while longer.

**A whole apply ends by sweeping.** Pruning after a stack's gate reaches only
what that stack's action superseded, so three kinds of old image would stay
for good: one an earlier `--keep-images` run kept or whose removal failed, in a
stack this apply does not move; any image at all when an apply moves nothing;
and every image of an app taken out of the configuration, whose repository no
stack renders any more. So the site's manifest records every image repository
its stacks have rendered, with each reference rendered from it, and each apply
adds what it renders to that record and drops nothing: it is what the stack
uses and has used. As its last step, once every stack is healthy and the
manifest is written, every whole `deployment apply` removes each image that is all three
of:

* from a repository in that record, or one the site renders now,
* not an image any site of the deployment renders, by ID or by tag, so a
  digest-pinned image (Eg: the gateway's Caddy, which `docker image ls` shows
  untagged) is kept by its ID, and an image the pull stage fetched for a stack
  not started yet is kept because it is rendered, and
* not used by any container, running or stopped, the same test as above.

The dry run lists them as one `sweep old images` step, and its summary line
counts them (Eg: `2 old images to sweep`). Each failed read or removal is a
warning, never a failed apply. A manifest written before the record existed
starts it at the next apply.

Only a **whole** apply sweeps. One narrowed by `--only`, the pass that holds
apps back until their sign-in clients exist, and the scoped applies inside
`site add`, `storage add` and the others are each a step of a larger change,
and the whole apply that finishes it sweeps. A step that swept could remove
the image a later step of the same change is about to start from.

**The toolkit removes only images of repositories it rendered.** An image no
container uses may still be one the toolkit never pulled and knows nothing
about: an operator's own tools, an image staged by hand for a later apply. A
repository that never appeared in a compose file this deployment rendered is
never in the record, so the sweep never reaches it. **No previous versions are
kept by default** because of the disk this is for: on a 10 GB host one extra
Mbin image is 1.4 GB, and `--keep-images`, which skips the sweep as well,
covers the rollback case when an operator actually wants it.

**Another deployment on the host stops the sweep.** Two deployments may share
one Docker engine, and what the other renders is in its own files, which this
apply does not read. A host whose containers include another deployment's is
shared and removes nothing (*The host check: what is already on a host decides
how much is touched*); a host holding another deployment's root under
`/srv/paisans/` with no container running sweeps nothing either, and the plan
says which root it found. Another site of the same deployment is never on
the host, since a site is one machine (*A deployment has an id, and a host
knows every deployment on it*), so the sweep keeps what this site renders and
nothing on behalf of other sites.

### Every volume an image declares is mounted, and `deployment apply` checks it

An image's `VOLUME` is a path Docker gives every new container an anonymous
volume for, unless the container has a mount at exactly that path. Compose
replaces containers on every recreate, and each new one gets a new volume;
the old one stays on disk, unused, with no label saying whose it was. A real
apps site collected 18 that way, about 830 MB: the Mbin image declares
`VOLUME /app/var/`, the template mounted only `/app/var/log` beneath it, and
every recreate of `app` and of each messenger replica left one behind holding
`cache/` and `log/`. An anonymous volume is also outside `/srv/paisans/<token>/<stack>/`, so
it breaks the bind mount rule above in a second way: `app move`'s tar would
not carry it.

Two facts about Docker decide the design, both observed on Engine 29.7.2 with
Compose 5.4.0: a plain, a forced and an image changing recreate each made a
fresh anonymous volume and abandoned the old one, and a bind at a parent path
(`/app`) did not stop Docker making one at the declared path (`/app/var`)
beneath it. So **only a mount at exactly the declared path counts**, and a
trailing slash is not a difference.

**Every kind's template mounts every path its images declare.** State gets a
bind under `/srv/paisans/<token>/<stack>/`; throwaway gets a tmpfs. Each pinned image was
inspected on 2026-10-07. On 2026-10-13 every one but the two forks' was read
from the registry for each platform it is published for, amd64 and arm64
among them, and every platform declared the same paths. Mbin's is published
for linux/amd64 alone:

| Image | Declares | Mounted as |
|-------|----------|------------|
| mbin 1.15.1-paisans (app, messenger), inspected 2026-10-13 | `/app/var/` | tmpfs, 256m. Symfony's compiled cache, rebuilt by the entrypoint's `cache:clear` on every start; the app cache is in Valkey. The log bind mounts stay inside it |
| rabbitmq 3.13.7-management-alpine | `/var/lib/rabbitmq` | bind, `rabbitmq_data` |
| wiki 2.5.316, read from its `dev/build/Dockerfile:49` | `/wiki/data/content` | bind, `/srv/paisans/<token>/<app>/content`. Uploads live in Postgres and no storage module the bootstrap turns on writes here, so it holds nothing a member wrote; the bind keeps Docker from making an anonymous volume at each recreate |
| postgres 16-trixie, 17-trixie (inspected 2026-10-11), 16-alpine, 17-alpine | `/var/lib/postgresql/data` | bind, `/srv/paisans/<token>/<app>/postgres` |
| postgres 18-trixie (inspected 2026-10-11), 18-alpine | `/var/lib/postgresql` | bind, `/srv/paisans/<token>/<app>/postgres`, never at `/var/lib/postgresql/data` (below) |
| account (`paisans-account-service`, distroless) | nothing | its state is in Postgres; the code of conduct is bind mounted read-only from `/srv/paisans/<token>/<app>/code-of-conduct.md` at `/etc/paisans-account-service/code-of-conduct.md` |
| uptime 1.1.0-oidc.4 | `/data` | bind, `/srv/paisans/<token>/<app>/data`. Inspected at 1.1.0-oidc.3, built from the same Dockerfile |
| amqproxy, valkey, pocket-id, synapse, MAS, element-web, writefreely-wisp 0.21.2-wisp (inspected 2026-10-13, both platforms), oauth2-proxy, spilo 16 to 18, etcd, haproxy, garage, caddy, operator-reconciler, wikijs-bootstrap 0.1 (read from its Dockerfile), curl (the heartbeat and the operators heartbeat) | nothing | |

For Postgres 18 the path is more than a leak. Its image sets `PGDATA` to
`/var/lib/postgresql/18/docker` and its entrypoint refuses to start when
something is mounted at `/var/lib/postgresql/data`, the path 16 and 17 declare,
so a mount there would leave a pinned app at the default `postgres_version`
unable to start its database at all. The mount follows the official image's
own tag (an app may pin an older Postgres than the cluster runs) and the cluster's version otherwise.

**Mbin's cache is a tmpfs rather than a bind under the deployment's root.** The two messenger
replicas would share one bind and race over one cache directory, and the cache
has no value across a restart: the entrypoint clears it each time.

Three things keep it from happening again:

* **A registry test.** `TestEveryDeclaredImageVolumeIsMounted`, run with
  `-tags registry_integration`, resolves every track to the build it names
  today, reads what that build declares on every platform it is published
  for, and fails when a rendered service leaves a declared path unmounted.
* **A refusal in `deployment apply`.** A track moves, and an operator's own `images`
  override is not the catalog's. So `deployment apply` inspects every image
  of every stack it acts on (`docker image inspect --format '{{json
  .Config.Volumes}}'`) and compares each declared path with the binds, named
  volumes and tmpfs mounts of that service's rendered compose file. An
  unmounted path refuses the apply before anything is written or started, and
  names the stack, service, image, path and the fix. An image the host does not
  have yet is pulled first, after the free space check, which is the pull `up
  -d` would make anyway. The dry run reports it with the disk space check, and `--verbose` shows its
  detail (`volumes: 3 image(s) inspected: every declared path is mounted`).
* **A cleanup after each recreate.** Before a recreate, `deployment apply` records the
  anonymous volumes the stack's containers mount; once the stack passes its
  health gate, it removes those nothing mounts now. A failure is a warning, as
  for images, and a failed health gate removes nothing.

Refuse, not warn: the leak is certain, and a warning is the state the real site
was already in with nobody reading it. Only stacks the apply acts on are
checked, because a stack left alone creates no container and so no volume.

For what accumulated before any of this, `paisans prune --site <site>` lists
every dangling volume with its size and top level entries, and `--execute`
removes the ones that are this deployment's: those carrying the label
`community.paisans.deployment` with this deployment's id. Every other volume is
kept: one labelled with another id is another deployment's, and one with no
such label, anonymous ones included, cannot be attributed to any deployment on
a host that several may share. `deployment apply` itself removes the anonymous volumes its
own recreates abandon, read from its own containers before they are replaced,
so those do not wait for `prune`. The plan says what its verdicts rest on at
the top, then marks each volume the way `site prepare` marks its plan: `○
remove volume <name>` for one it would remove, and with `-v` `✓ keep volume
<name>` for one it keeps, each with its whole name, size, top level entries
and reason under it, and the total last. Piped output says `todo` and `ok`
instead. An anonymous volume's 64 character name is shortened to its first 12
in the title.

**`prune` lists first and removes only this deployment's volumes**, the same
rule `deployment apply` follows for images. **Every declared
volume is mounted** because each recreate otherwise costs a volume, and a
throwaway path is not bound under the deployment's root because a shared cache directory
between replicas is a race, not state.

### `deployment apply` makes the declared operators and hands each a login link

A deployment that has just come up runs every app and lets nobody in: Pocket
ID has no users, and its users sign in with passkeys, so there is no password
to hand anyone. The `operators` section already says who runs the deployment,
so a whole `deployment apply --execute`, once Pocket ID runs, makes each of them a Pocket
ID user: **the operator's key is the Pocket ID username**, `email` is the
address, marked verified for the reason *`operator magic-link` gives a
declared operator a fresh sign-in link* gives, and `name` is split at its
first space into a first and a last name. Each is put in the group
`operators`, in `members`, in every app's member group and in every app's
admin group, so every operator is also a community admin (see *Operators and
community admins*). Declaring the operator is the approval for all of it.

**`isAdmin` follows `management`.** Under `management: pocket-id` apply makes
each operator a Pocket ID administrator, because Pocket ID's admin pages are
where accounts are managed there. Under `management: paisans` it never sets
the flag, because nobody is a Pocket ID administrator there, and it never
clears one either: a flag set by hand is the operator reconciler's to report
(*The operator reconciler checks Pocket ID against the operators*), not
apply's to undo on its own.

**The gateway refuses Pocket ID's own first-admin setup, always**, so the
window between Pocket ID starting and apply making the operators is not a race
with the internet. *The gateway exposes only the Pocket ID routes its
configuration turns on* gives the rule. Apply makes operators through the
static API key and never needs the setup route.

**An operator is a member of every app too**, because an app that gates
on its member group would otherwise refuse its own administrator. Mbin reads
`settings.member_group` and refuses a login whose claim lacks it unless the
linked account is already an Mbin administrator, which a first login never
is; releases of the fork before 1.15.1 did so even when the claim named the
admin group. Joining the member groups makes every app admit its
administrators, whatever it reads, including one that reads no group itself
and relies on the client's restriction at Pocket ID. A member group that is
also an admin group is joined once. Founder decision.

The username is set when the account is created and never changed after:
the pocket-id blueprint turns account editing off (`allow_own_account_edit`,
under *Pocket ID's configuration is rendered from paisans-stack.yaml*), so
nobody can rename themselves or change their email, and apply never renames
an account. Renaming an operator's key in the `operators` section therefore
makes a second account rather than renaming the first. An account is found
by its username, then by its email, so one renamed from Pocket ID's admin
page is still found by its address. Only an operator found by neither is
created. Since the username is the operator's key, `validate` refuses a key
Pocket ID would not take, one that does not end in a letter or digit
(`pocket-id-operator-username`), when the deployment runs Pocket ID.

**An operator who has registered a passkey gets nothing more.** That is the
sign the account was taken up, and Pocket ID records it as a `PASSKEY_ADDED`
event in its audit log, which apply reads. **Every other operator gets a new
login link on every run**, valid for 24 hours and for one sign in. Pocket ID
keeps no way to read a link back once issued, so a run cannot reuse an earlier
one, and issuing one does not revoke the others: an earlier link stays valid
until it is used or expires.

**Every link is printed at the end of the run**, after its result, whether
output is a terminal or a pipe, so that it can be passed on by any means. A
blank line sets the list apart before and after, and on a terminal each link
is in italics. A link signs in as its operator: treat it as a password. It is
printed once and kept nowhere.

```
pocket id
  ✓ alice on auth          registered
  ✓ fern on auth           created, link issued
  ✓ email fern a login link   sent

Login links, each valid for 24 hours and for one sign in. Treat each as a password:
  fern  https://id.example.org/lc/ABCDEFGHJKLM
```

**When the smtp settings resolve a host and a sender, Pocket ID emails each
operator a link as well.** `email_one_time_access_as_admin` is then `true`
unless the settings say otherwise, and apply asks Pocket ID to email the
operator. The emailed link is Pocket ID's own, a second link beside the
printed one. Pocket ID answers before it sends and logs only a failure, so
apply reads its log for 30 seconds and marks the line `failed:` with Pocket
ID's error, or `sent` when none was logged. `sent` means no failure was
reported, not that the mail arrived. A failed email fails nothing: the
printed link is there.

An operator who cannot be made fails the run, after every other operator has
been tried and the links already issued are printed. **Taking an operator out
of the section changes nothing in Pocket ID**: apply never deletes or
disables a Pocket ID user, so the person stays a member until the community
removes them, and the operator reconciler stops expecting them. A dry run
reads Pocket ID and plans, one `○` line per operator, and issues nothing.
`docs/specs/2026-10-11-apply-admin-login-links.md` is the approved
specification of the login links.

### `operator magic-link` gives a declared operator a fresh sign-in link

Pocket ID users sign in with passkeys, so there is no password to hand an
operator, and an operator cannot sign in at all until they have registered
one. `deployment apply` makes every declared operator a user and gives each a one-time
sign-in link until they register a passkey (see above). Two cases are left: a
link that expired before it was used, when no apply is due, and an operator
who lost their passkey, whom apply gives nothing more. Both are the same
command:

```sh
paisans operator magic-link alice              # finds alice's account, changes nothing
paisans operator magic-link alice --execute    # issues the link, prints it once
```

It issues a link and does nothing else. It takes an operator by their key
under `operators`, and refuses any other name before a host is reached: an
operator is declared, never made on the side. The account and its groups are
apply's, so a declared operator with no account yet is pointed at apply, and
a disabled account is refused rather than re-enabled. The link lives a day,
as apply's do, and Pocket ID emails one too when it emails apply's. The flags
are under *`paisans operator magic-link`* in [docs/commands.md](docs/commands.md).

The link is Pocket ID's own mechanism: an administrator issues a one-time access token
for a user (`POST /api/users/:id/one-time-access-token`,
`onetimeaccess/module.go:68` at `v2.14.0`), and `{APP_URL}/lc/{token}` signs
that user in once, the same link the binary's `one-time-access-token`
subcommand prints (`cmds/one_time_access_token.go:78`). The operator opens it
and registers a passkey. A link lives over fifteen minutes on purpose: a TTL of
fifteen minutes or less gets a 6 character code
(`onetimeaccess/service.go:271-274` at `v2.14.0`), which the login page accepts
only when unauthenticated email login is enabled; on a host without it the
page waits for 12 characters and the link is unusable.

**The link is a credential, and it is handled like one.** It is printed once,
to the terminal running the command, only with `--execute`, with its expiry.
It never appears in a plan line or an error and is never written to the
secrets file.

**An operator's email is created verified** (`"emailVerified": true`,
`dto/user_dto.go:29`, stored as sent at `service/user_service.go:299`), and an
existing account whose email is not verified is marked so: a `PUT
/api/users/<id>` that sends every field back as read with only
`emailVerified` changed, since that update overwrites the whole user
(`service/user_service.go:495-519`). Without it Pocket ID sends
`email_verified: false`, and the paisans Mbin fork refuses an OIDC sign in whose
email matches an existing local account unless the provider marked it
verified. That guard stops anyone taking over an account by claiming its
address at the provider, and it is right; declaring an operator is vouching
for the address in `operators`, which is the verification it asks for.

The calls go through Pocket ID's REST API with its static API key; see *The
toolkit administers Pocket ID through its static API key*.

### Pocket ID's configuration is rendered from paisans-stack.yaml

The `pocket-id` kind always renders `UI_CONFIG_DISABLED=true`, and every
application setting Pocket ID has is then rendered too, from
`apps.<app>.settings` and the `smtp:` block. Founder decision, 2026-10-08.
With that variable set, Pocket ID v2.14.0 reads its whole application
configuration from the environment at startup and ignores what its database
holds (`backend/internal/appconfig/service.go:33-40`, `:205-249`); a variable
left unset takes Pocket ID's own default (`appconfig/model.go:120-175`).

The reason is the one behind every rendered file (see *Rendered configuration
is a build artifact*). A setting made in the admin page lives in the database,
where nobody reviews it, a diff does not show it and `paisans-stack.yaml` does not
know it. Rendered, it is one reviewed line, every instance of a Pocket ID on
more than one apps site reads the same `.env`, so a promoted standby signs
people in exactly as the active one did, and restoring the database cannot
quietly roll a setting back.

**What is locked is the Application Configuration admin page and nothing
else.** Its form is read only and its update API refuses
(`service.go:120-123`, `:157-159`). Users, groups and their members, OIDC
clients, custom claims, signup tokens, the logo and branding images, and the
test email are untouched by the variable and are administered in the admin UI
as before. A value set in the admin page before the variable was rendered is
ignored from the first start with it, not carried over: declare it here.

**Changing a setting is a config edit and an apply.** Edit
`apps.<app>.settings`, then `paisans deployment apply --site <site>` for each site the app
runs on. The `.env` changes, so the apply recreates Pocket ID's containers,
which is a restart of a few seconds during which nobody can sign in.

| Setting | Variable | Takes |
|---|---|---|
| `app_name` | `APP_NAME` | 2 to 30 characters; also the sender name on its mail |
| `session_duration` | `SESSION_DURATION` | minutes, 1 to 43200 |
| `home_page_url` | `HOME_PAGE_URL` | where a signed in person lands, Eg: `/settings/apps` |
| `emails_verified` | `EMAILS_VERIFIED` | `true` or `false` |
| `accent_color` | `ACCENT_COLOR` | a CSS colour, or `default` |
| `disable_animations` | `DISABLE_ANIMATIONS` | `true` or `false` |
| `allow_own_account_edit` | `ALLOW_OWN_ACCOUNT_EDIT` | `false` only, and rendered `false` when unset; see below |
| `allow_user_signups` | `ALLOW_USER_SIGNUPS` | `disabled`, `withToken` or `open` under `management: pocket-id`; under `paisans` refused if set, and rendered `disabled` |
| `signup_default_groups` | `SIGNUP_DEFAULT_USER_GROUP_IDS` | a list of group names; see below |
| `require_user_email` | `REQUIRE_USER_EMAIL` | `true` or `false` |
| `email_login_notification` | `EMAIL_LOGIN_NOTIFICATION_ENABLED` | `true` or `false` |
| `email_one_time_access_as_unauthenticated` | `EMAIL_ONE_TIME_ACCESS_AS_UNAUTHENTICATED_ENABLED` | `true` or `false` under `management: pocket-id`; under `paisans` refused if set, and rendered `true` |
| `email_one_time_access_as_admin` | `EMAIL_ONE_TIME_ACCESS_AS_ADMIN_ENABLED` | `true` or `false`; `true` when unset and the smtp settings resolve a host and a sender |
| `email_api_key_expiration` | `EMAIL_API_KEY_EXPIRATION_ENABLED` | `true` or `false` |
| `email_verification` | `EMAIL_VERIFICATION_ENABLED` | `true` or `false` |
| `webauthn_user_verification` | `WEBAUTHN_USER_VERIFICATION` | `required` or `preferred` |
| `webauthn_allow_synced_passkeys` | `WEBAUTHN_ALLOW_SYNCED_PASSKEYS` | `true` or `false` |
| `webauthn_authenticator_attachment` | `WEBAUTHN_AUTHENTICATOR_ATTACHMENT` | `any`, `platform` or `cross-platform` |

Mail settings come from the `smtp:` block, not from here (see *Mail is
declared once and overridden per app*). `file_backend` is the one setting
older than the rest (see *Rule 3: object storage from the first install*).
`management` is outside the table because it renders no variable of its own:
it decides who manages accounts, and under `paisans` the toolkit renders the
two settings the table marks (see *Who manages accounts: `management`*,
under *Accounts*). It is required.

```yaml
apps:
  auth:
    blueprint: pocket-id
    hostname: id.example.org
    placement: cluster
    settings:
      management: paisans            # required: paisans or pocket-id
      file_backend: database
      app_name: Example ID
      session_duration: 10080        # a week
      home_page_url: /settings/apps
      signup_default_groups: [members]
      email_verification: true
      allow_own_account_edit: false  # the only value it takes
```

Under `management: pocket-id` the same app declares Pocket ID's own signup
and email code, since they are the operator's choice there:

```yaml
apps:
  auth:
    blueprint: pocket-id
    hostname: id.example.org
    settings:
      management: pocket-id
      allow_user_signups: withToken
      email_one_time_access_as_unauthenticated: true
```

**Nobody can edit their own account.** The kind renders
`ALLOW_OWN_ACCOUNT_EDIT=false` whether or not the setting is declared, and
`validate` refuses `true` (`pocket-id-account-edit-on`), as it refuses the
variable under `config`. A Pocket ID username is the identity other apps know
a person by, such as their fediverse handle in Mbin, and those apps do not
follow a rename: a renamed user signs in to them as somebody new. With editing
off a user cannot change their email either, which keeps apply finding each
declared operator's account (see *`deployment apply` makes the declared operators and hands each a login link*). The setting stays in the table so that a configuration
can say so where it is read. Pocket ID's own admin page can still edit any
account, and that is the way to correct one; a username changed there breaks
the same identities in the other apps.

**Pocket ID checks every value when it starts, and will not start on one it
refuses** (`validateEnvConfig`, `service.go:252-279`, which applies the admin
page's API rules in `dto/app_config_dto.go:16-64`). A bad value would show up
as a Pocket ID restarting in a loop on every apps site at once, after the
apply, so `validate` refuses it first: `pocket-id-setting-invalid`, naming the
key. Where the admin page's form is stricter than its API (an application name
of at least 2 characters, a session of at most 30 days), the form's rule is
the one checked, because those are the values Pocket ID was built to be given.
An email toggle turned on without an SMTP host and sender is refused too,
`pocket-id-email-without-smtp`, as the admin page refuses it: Pocket ID would
start and every mail the toggle promises would fail. A setting under a key the
kind does not read is a warning, `pocket-id-setting-unknown`, because for this
kind it is most likely a misspelt option that nothing else can now set.

**What the table does not model goes under `config`, by variable name.** LDAP,
`SIGNUP_DEFAULT_CUSTOM_CLAIMS`, `CIMD_URL_ALLOWLIST` and
`SMTP_SKIP_CERT_VERIFY` are placed in the `.env` as written and checked by
nobody but Pocket ID at startup (see *A config key is placed, not
interpreted*). LDAP is not supported in practice, because its bind password is
a credential and a credential is refused in `config`
(`config-key-looks-like-a-secret`). A variable the kind renders from a setting
or from the `smtp:` block is refused in `config` whether or not that setting is
declared, `pocket-id-config-key-has-a-setting`, so each value has one place to
be changed.

#### Signup default groups are resolved by apply

`signup_default_groups` names the groups every new user is put in. Pocket ID
puts a user in them when the user is created without groups of their own,
whether by signing up or by an administrator (`service/user_service.go:337-345`,
`:365-401`). It takes them as a JSON array of group IDs, and a group's ID
exists only once the group does, on the running instance. So the names are
declared and the IDs are found by `deployment apply`, which records them in the secrets
file under `pocket_id_groups.<name>`, beside the client IDs under
`oidc_clients`, and `render` reads them from there. Recording rather than
looking up at render time keeps `render` a function of the two files: every
instance renders the same list, and a dry run renders what the apply will.

The step runs on every apply of a site that runs Pocket ID, within the
identity step (see *`oidc client create` makes an app's client at Pocket ID*).
On `--execute` Pocket ID is started first with the IDs recorded so far, and
once it answers, each name is looked up at the active instance. A group that
does not exist is created, exactly as the client step creates the groups a
client admits, and its ID recorded; a group whose recorded ID differs from the
instance's, as after a database started afresh, has the instance's recorded in
its place. Pocket ID skips an ID it does not hold without a word
(`user_service.go:374-379`), so a stale ID left in place would quietly put new
users in nothing. The site is then planned again, the `.env` carries the IDs,
and Pocket ID is recreated with them. A dry run marks each group to
record or create `○`, and with `--verbose` each already recorded and present
`✓`, and changes nothing. A dry run of a site whose Pocket ID has not started
yet cannot ask it, so it marks the groups' step with a dim `·`, waiting on
the start. Once every ID is
recorded and present, a re-run plans nothing.

A Pocket ID that cannot be asked holds nothing back and fails nothing: a
recorded ID is rendered as it is, and a name with none is left out of the
`.env`, which says so in a comment, until a re-run resolves it.

### The gateway exposes only the Pocket ID routes its configuration turns on

Pocket ID serves some routes to anyone, with no sign in, for features its
configuration can turn off. **A route that serves only a feature the rendered
configuration turns off is closed at the gateway**, which answers 404 to every
method on it, because a feature that is off has no visitor to serve and an
open route is one more way in. The pocket-id blueprint's Caddy snippet holds
the rule, each route beside its condition and the Pocket ID source it rests
on. It reads the settings as the `.env` renders them, where a setting left
out takes Pocket ID's default (`appconfig/model.go:120-175` at v2.14.0), and
nothing else.

| Paths, and everything below each | Closed when | Pocket ID v2.14.0 |
|---|---|---|
| `/api/signup/setup`, `/signup/setup`, `/setup` | always | `POST /api/signup/setup` takes no authentication and makes its caller an administrator (`usersignup/module.go:86-87`) while no user exists besides the static API key's (`usersignup/service.go:220-229`). Nothing turns it off |
| `/api/signup`, `/signup`, `/st/` | `allow_user_signups` is `disabled` or unset (the default, `appconfig/model.go:130`) | the signup page refuses when disabled (`frontend/src/routes/signup/+page.svelte:38-41`), but `POST /api/signup` takes an administrator's signup token whatever the setting (`usersignup/service.go:51-52`). `/st/<token>` redirects to `/signup` |
| `/api/one-time-access-email`, `/login/alternative/email` | `email_one_time_access_as_unauthenticated` is not `true` (the default is false, `appconfig/model.go:144`) | Pocket ID refuses the request (`onetimeaccess/service.go:66-67`) and does not offer the page (`frontend/src/routes/login/alternative/+page.svelte:30-36`) |

**The setup routes are closed on every deployment.** That route is open on
every new deployment from Pocket ID's start until apply makes the declared
operators, and again after a restore that holds no users. On a public hostname
whoever reaches it first in that window owns the identity provider.

**Under `management: paisans` signup is closed and the email code is
open.** Accounts there come only from the account service, so the toolkit
renders `allow_user_signups` as `disabled` and
`email_one_time_access_as_unauthenticated` as `true`, and refuses either one
declared (*Who manages accounts: `management`*, under *Accounts*). The
snippet needs no change for it: it reads the rendered values, so the signup
routes are closed, the signup tokens an administrator could otherwise hand
out included, and the email-code routes stay open, which is how a member
whose sign-in link expired signs in again. Under `management: pocket-id`
Pocket ID's own settings decide, as the operator declared them, and a Pocket
ID with `allow_user_signups: open` is a public self-service identity
provider whose signup pages stay open.

**Nothing the toolkit or the account app calls is closed.** Apply, `operator
magic-link` and `oidc client create` reach Pocket ID at a site's own address,
from that host, and never pass the gateway; the operator reconciler reaches it on
its stack's own network. The account app calls only `/api/users`,
`/api/user-groups` and `/api/users/<id>/one-time-access-email`, with the static
API key, through its site's HAProxy on the mesh, and signs in through OIDC. Sign in,
`/lc/` login links, `/login/alternative/code` and `/api/signup-tokens`, an
administrator's route, stay open on every deployment.

Each path is matched with everything below it, and Caddy's path matcher
compares the decoded, cleaned path case insensitively (`MatchPath` in
`modules/caddyhttp/matchers.go` at v2.11.7), so a trailing slash, a change of
case, a doubled slash, a dot segment or an escaped letter reaches the same
refusal.

**The gateway refuses every request that carries an API key.** Before any
other rule, the snippet answers 403 to a request with an `X-API-Key` header,
on every path and for every method, whatever its value, an empty one
included, and whatever else the request carries, a valid session cookie
included:

```caddy
@api_key header X-API-Key *
respond @api_key 403
```

Pocket ID reads an API key from that header alone
(`middleware/api_key_auth.go:38` at v2.14.0), and Caddy's header matcher
compares the name case insensitively, so `x-api-key` and `X-API-KEY` are
refused too. The web interface signs in with the `access_token` session
cookie and never sends the header, so sign in, passkeys, the logo, OIDC
discovery and every admin page are untouched. The static API key therefore
works only on the mesh, where the toolkit and the account service send it.
Members' own API keys, which Pocket ID lets a user create, are refused the
same way: a member who scripts against Pocket ID does it from a machine on
the mesh.

### The toolkit administers Pocket ID through its static API key

Pocket ID is the one application whose first users, the operators, and whose
clients this toolkit creates for itself, and every one of those is a call to its REST
API. What makes the calls possible without a browser is `STATIC_API_KEY`
(`backend/internal/common/env_config.go:59` at tag `v2.14.0`, at least 16
characters at `:182-184`): a request whose `X-API-Key` header equals it
authenticates as a synthetic administrator that Pocket ID creates on first use
and deletes at startup once the variable is unset
(`apikey/service.go:31-36`, `:156-163`, `:221-259`;
`middleware/api_key_auth.go:38`). `init` generates it as
`apps.<app>.static_api_key`, the `.env` renders it, and `render` refuses a
Pocket ID app without it, as it refuses one without its `ENCRYPTION_KEY`.

A call goes from the workstation over the operator's own ssh to the site the
app runs on, where `curl` calls the port Pocket ID publishes on the mesh
address. On more than one apps site that is the site whose instance is active
(see *Pocket ID runs on every apps site, and one of them is active*), unless
`--site` names one. The command line is one constant, `curl --silent --show-error
--globoff --config -`; the URL, the key and any request body are a curl
configuration on stdin. A command line is in the process table, readable by
every user on the host, for as long as it runs; stdin is not. `curl` runs on
the host because the image has none (`docker/Dockerfile-prebuilt` is Alpine
plus `su-exec`), and the host has it from `site prepare`.

The key is a standing admin credential on every apps site that runs Pocket ID,
which is worth stating plainly. It adds nothing to what root on that host
already holds: the same `.env` carries the database connection string and the
encryption key, and either is all of Pocket ID. It works only over the mesh:
the port is bound to the mesh address and nowhere else, and the gateway, the
one way to Pocket ID from the internet, refuses any request that carries an
`X-API-Key` header (see *The gateway exposes only the Pocket ID routes its
configuration turns on*).

### `oidc client create` makes an app's client at Pocket ID

An app that signs members in through Pocket ID needs a client there, and the
app needs that client's ID and secret. The toolkit creates the client and
records both in the secrets file, where `render` already reads them
(`oidc_clients.<app>.client_id` and `.client_secret`).

**`deployment apply` does this itself, for every app it starts whose kind has a client
shape** (`kinds.OIDCClient`, today `mbin`, `wikijs`, `writefreely`,
`uptime`, `synapse` and `oauth2-proxy`). Creating an app's
client at the deployment's own Pocket ID is part of setting the app up, and
declaring the app in `paisans-stack.yaml` is the approval for that client. Founder
decision, 2026-10-08. So a first apply of a site needs nothing run before it:

```sh
paisans deployment apply --site home-a             # shows the clients with the rest of the plan
paisans deployment apply --site home-a --execute   # creates, records and renders them
```

The step is the command's own code, so everything below holds for both. On a
site that runs Pocket ID, `--execute` starts the infrastructure and Pocket ID
first, with every other app stack held back, waits until exactly one Pocket ID
instance answers `/healthz`, ensures the clients, and only then plans the site
again and starts the rest, so each app renders the credentials just recorded.
On any other site the clients come first. On a site that runs Pocket ID the
step also resolves its signup default groups, after the clients (see *Signup
default groups are resolved by apply*). A dry run probes and prints the
client lines with the rest of the plan and sends nothing; on a site whose
Pocket ID is not running yet it says the clients are made once it answers.

**An app is held back rather than started without its client.** When Pocket
ID cannot be reached, an app with no recorded client is skipped with the
reason and the rest of the site is applied; a re-run once Pocket ID answers
finishes it, and the apply does not fail for it. The end of the apply lists
what was held back and why, including any `--overwrite` in a held stack, which
is not written and has to be named again; a `--recreate` of a held stack is
remembered and carried out when the stack starts. A held app's route on the
site's own Caddy (a gateway, or a monitor serving its own apps) is held with
it and written by the pass that starts the app, because a route reloaded
first would point at the app as it was, or at one still without its client.
The one exception is a route the host does not have yet: the Caddyfile
imports every route by path and Caddy will not load an import of a missing
file, so a new app's route is written in the first pass and goes live then,
pointing wherever the app runs. A gateway routes to apps on other sites as
well, so the app may already be serving there; it gets its client in the
second pass of its own site's apply. A deployment that declares
no `pocket-id` app has no identity step, and its apps are applied as before,
without sign in. An app whose client is already
recorded is not held back by an unreachable Pocket ID: its client was right
when it was made, and an outage elsewhere is no reason to stop an app that can
already sign people in. An existing client that differs from what the app
needs refuses that app alone, as the command does; the rest of the site is
applied and `deployment apply` exits non zero, because a person has to fix the client.
Everything `--execute` would refuse before writing (a file edited on the host,
too little disk for the pulls, an unmounted image volume) is checked on the
whole site before the first pass, so a refusal still leaves the host and
Pocket ID untouched. The secrets file is written through the same recorder, re-encrypted to the
recipients in `.sops.yaml`, and a plan that would write an encrypted file with
no recipient is refused before Pocket ID is sent anything.

**`deployment apply` creates a client when nothing is recorded for the app, or when what
is recorded is a client Pocket ID does not have.** If the secrets file holds a
client ID and secret and Pocket ID has no client of that ID or the client's name, as after
Pocket ID is started on a fresh database, `deployment apply` creates the client and records
its credentials over the stale ones, saying so in one line
(`talk: recorded client <id> is not at Pocket ID; created a new one`); a dry
run lists the create with the rest of the plan. Nothing can be signing in with a
client that does not exist, so nothing is lost. Two cases are refused for that
app instead, naming `oidc client create --app <name>`, which creates or
re-records the client: credentials only partly recorded, and a client with
the client's name under another ID than the recorded one. That client may be what a
running app signs people in with, so replacing it is a decision, not a repair.

`deployment apply` never rotates a secret. The command stays for that and for running the
step alone. How to run it, and its flags, are under *`paisans oidc client
create`* in [docs/commands.md](docs/commands.md).

For an Mbin app that declares no groups, so that its member group is
`members` and its admin group `admins`, and a Pocket ID that already has both
groups, the dry run marks each mutation it would make `○`:

```
  ○ create OIDC client talk
  ○ allow groups members, admins on talk
  ○ create client secret for talk
Nothing changed. Re-run with --execute to apply.
```

A group that does not exist yet adds a `create group <name>` line before the
client. `--verbose` puts under each line the request it would send, and
after them marks what Pocket ID already has `✓`. Piped output says `todo`
and `ok` instead:

```
  ○ create OIDC client talk
      create client talk: POST /api/oidc/clients {"name":"talk","callbackURLs":["https://talk.example.org/oauth/oidc/verify"],"isPublic":false,"pkceEnabled":true,"isGroupRestricted":true,"launchURL":"https://talk.example.org/oauth/oidc/connect"}
  ○ allow groups members, admins on talk
      allow groups members, admins on client talk: PUT /api/oidc/clients/<id>/allowed-user-groups with exactly members, admins
  ○ create client secret for talk
      create client secret for talk: generated on this workstation, written to oidc_clients.talk.client_id and oidc_clients.talk.client_secret, then sent to POST /api/oidc/clients/<id>/secrets. Never printed
  ✓ group members
  ✓ group admins
```

**Each line is a Pocket ID mutation.** For an app
declared in `paisans-stack.yaml` the declaration approves its client, and for the
gate, which no file declares, so does setting `gated` or `auto_login` on an
app; the plan is
what lets an operator see what that approval does, in a dry run of either
`deployment apply` or the command, before `--execute`, and `--verbose` shows exactly what
each request sends. The same probe runs
again on `--execute`, so what executes is what a fresh probe plans.

**A client is named by its app's `display_name`, and otherwise after its
app, except the gate's, which carries the community's name.** Pocket ID heads
its sign in page "Sign in to" the client's name
(`frontend/src/routes/interaction/+page.svelte:111,117` at v2.14.0), so the
name is what a person signing in reads, and an app's key is often not what
they should: `apps.wiki.display_name: Wiki` puts "Wiki" there. The gate's
sign in is the community's front door rather than an app a member picked, so
`community.name` is what they should read there. When the configuration gives
no `community.name`, the gate's client is named `gate` like any other app's.
The name is also how the toolkit finds the client, so it is found under the
name the configuration gives it, or, when Pocket ID holds no client of that
name, under the client ID recorded for the app. A recorded client found that
way under another name, as after `community.name` or a `display_name`
changes, is renamed in place: one `PUT` that sends every other field back as
it is, so its ID, its secret and its group restriction are untouched and
nobody's session is affected. A client of the old name with no recorded ID is
never renamed, since nothing proves this deployment made it.

Because the name is how a client is found, `validate` refuses a name the
client could not keep. Two apps whose clients would share a name are refused
(`duplicate-client-name`), since the second would find the first's client. A
`display_name` Pocket ID would refuse or change is refused too
(`display-name-invalid`): longer than the 50 characters Pocket ID accepts
(`dto/oidc_dto.go:42` at v2.14.0, `binding:"required,max=50"`), with space
at either end, or not in Unicode normal form C, which Pocket ID normalises the
name to on the way in (`unorm:"nfc"` on the same field), so that it would
never be found under the name written. A `display_name` on an app whose
blueprint has no client names nothing, and is warned about
(`display-name-without-a-client`).

The client is the app blueprint's, not a choice made at the command line, and
**every member facing blueprint's client is restricted at Pocket ID to its member
group and its admin group**, so a person outside both is refused there before
the app sees them. The two groups are `apps.<app>.settings.member_group` and
`settings.admin_group`, defaulting to `members` and `admins`
(`blueprints.MembersGroup`, `blueprints.AdminsGroup`), and the command creates
whichever is missing. WriteFreely reads no admin group, so its client's is
always `admins` and only `member_group` is a setting; the uptime monitor
has one group, `settings.admin_group`, used as both, which defaults to
`operators` (`blueprints.OperatorsGroup`), because the health page is the tool
of the people who run the machines (see *Monitoring*). Synapse's client is its
authentication service's, which reads no admin group, so like WriteFreely's
it is `admins` plus `member_group`. The gate's is `members` and `admins`, with no
setting, the same two groups its compose file admits: a community admin can see
everything a member can (Founder decision). The defaults exist because an
empty pair would leave an open client: anyone who could make a Pocket ID account could sign in to the
app. Which app reads the groups claim itself differs by kind, and the table
says so, because for a kind that does not, the restriction is the whole
control and the operator should know that nothing else stands behind it:

| Kind | Callback | PKCE | Groups claim |
|---|---|---|---|
| `mbin` | `/oauth/oidc/verify`, the paisans fork's verify route | on: the fork always sends a code challenge | read. `OAUTH_OIDC_MEMBER_GROUP` and `OAUTH_OIDC_ADMIN_GROUP` are rendered from the two settings (`.env.example_docker:243-273` at `v1.13.3+paisans`): a sign in outside the member group is refused by the fork as well, and the admin group's members become administrators. The fork asks for the `groups` scope only when one of the two is set (`src/Provider/Oidc.php:45-46`), which the defaults guarantee |
| `wikijs` | `/login/pocketid/callback`, built from the key of the sign-in strategy the bootstrap names `pocketid` (`server/core/auth.js:85` at `v2.5.316`) | off: Wiki.js's OAuth2 module gives `passport-oauth2` no PKCE option, so it sends no code challenge (`server/modules/authentication/oauth2/authentication.js:13-23`) | read. The bootstrap asks for the `groups` scope, without which Pocket ID sends no groups claim (`oidc/claims_service.go:157` at `v2.14.0`), and on every sign in Wiki.js puts the person in each Wiki.js group the claim names and takes them out of every other (`oauth2/authentication.js:36-48`). Both group settings are read: `admin_group` gets every Wiki.js permission but `manage:system` and `manage:api` (see *Wiki.js is configured by its bootstrap*) |
| `writefreely` | `/oauth/callback/generic`, built by `configureGenericOauth` and not configurable (`oauth.go:249` at writefreely-wisp `ff9dceb`) | off: nothing in the fork sends a code challenge | not read. The restriction at Pocket ID is the whole control |
| `account` | `/auth/callback` on the primary hostname (`internal/web/auth.go`) | on: every sign in sends an S256 challenge | read. It tells members from admins by the groups claim, and the client admits `member_group` and `admin_group` (defaults `members`, `admins`) |
| `uptime` | `/login/oidc/callback` (`src/lib/oidc.js:26`) | on: every sign in sends an S256 challenge (`src/lib/oidc.js:96-103`) | read. `settings.admin_group` is both groups: its members administer the monitor and nobody else may sign in |
| `synapse` | `/upstream/callback/<ulid>`, the authentication service's callback for its upstream provider (`crates/router/src/endpoints.rs:742-762` at MAS `v1.24.0`), with the id derived from the app's name | on: `mas.yaml` sets `pkce_method: always`, the S256 mode (`crates/config/src/sections/upstream_oauth2.rs:420-436`, `crates/cli/src/sync.rs:289-298`), so every sign in sends an S256 challenge (`crates/handlers/src/upstream_oauth2/cache.rs:137`, `crates/oidc-client/src/requests/authorization_code.rs:242-256`) | not read: `mas.yaml` requests no `groups` scope. The restriction at Pocket ID is the whole control |
| `oauth2-proxy` | `/oauth2/callback` on the gate's hostname: no redirect URL is rendered, so oauth2-proxy builds it from the proxy prefix and the request's host (`oauthproxy.go:175-178,1113-1135` at `v7.15.4`) | on: the `.env` sets `OAUTH2_PROXY_CODE_CHALLENGE_METHOD=S256`, and with a method set every sign in sends a challenge (`oauthproxy.go:833-851`) | read. The gate admits a session in any group of `OAUTH2_PROXY_ALLOWED_GROUPS` (`providers/provider_default.go:125-138`), which is rendered as `members,admins`, the same two groups as its client; oauth2-proxy splits that value on commas (`pkg/apis/options/load.go:27-55`, viper `v1.21.0` `viper.go:977-1022`) |

PKCE is set per client the way the app behaves, not on as a matter of course:
a Pocket ID client with PKCE on refuses every request that carries no code
challenge (`oidc/authorization_service.go:750-755` at `v2.14.0`), so turning
it on for Wiki.js or WriteFreely would refuse every sign in, while a client
with it off accepts a challenge when one is sent. Pocket ID fills the
`groups` claim with group names (`oidc/claims_service.go:157-162`), so the
names rendered into Mbin's `.env` are the names the client admits, read from
the same settings, and a passthrough `config` key for either is refused
(`config-key-already-rendered`), because a second place to type the name is
one that can disagree with the client. All four are confidential clients.
Putting a user in the admin group is apply's job, for every declared
operator, not this command's; anyone else joins `admins` by promotion (see
*Operators and community admins*).

**`validate` holds the groups to the gate.** The gate admits `members`, and
`admins` as every client does by default, and
an app using the gate has a client admitting that app's `member_group`. When
the two name different groups, someone is let through the gate and refused by
the app, or admitted by the app on a path the gate leaves open, and nothing
says which group "member" means; so an app setting `gated` or `auto_login`
whose member group is not `members` is refused
(`oidc-member-group-disagrees-with-gate`).
A `member_group` or `admin_group` that is blank or not a string is refused
too (`oidc-member-group-not-a-name`): the default would hold and the client
would still be restricted, but what the operator wrote reads as a restriction
to a group that does not exist. A group an app has apply create (its
extra groups setting) is refused when Pocket ID would not take the name,
which is 2 to 50 characters (`oidc-group-not-a-name`); otherwise apply would
fail on it after validate passed. And a gateable kind whose client would admit
every account is refused outright (`oidc-client-unrestricted`); every such
kind has a default member group today, so this is the floor under that.

**The client is created with a launch URL** (`launchURL`, `dto/oidc_dto.go:52`).
Pocket ID's dashboard lists only clients that have one
(`service/oidc_service.go:673-680` and `:758-765` at `v2.14.0`), so a client
without it works for signing in and is invisible to the members looking for the
app; the first real host's founder saw no apps at all. The launch URL is only
what the app's tile on that dashboard opens; signing in from the app itself
uses the callback URL and does not read it.

It is `https://<hostname>` plus a path. Each kind names its own
(`kinds.DashboardPath`): Mbin's is `/oauth/oidc/connect`, the paisans fork's
route that starts the OIDC flow (`config/mbin_routes/security.yaml:146-148` at
`v1.13.3+paisans`), because the bare host lands a member signed out with a log
in button still to press. Every other kind's is `/`, written as the bare host
with no trailing slash, which is what clients got before kinds named a path.
An app may replace the kind's path:

```yaml
apps:
  talk:
    blueprint: mbin
    hostname: talk.example.org
    settings:
      sso_dashboard_link: /magazines   # tile opens https://talk.example.org/magazines
```

`validate` refuses a value that is not a path starting with a single `/`,
including a full URL. The host is always the app's own hostname; accepting a
URL would be a second place to spell it, one that can disagree with
`hostname`.

**Pocket ID's consent screen is off for every client the toolkit makes.** The
client is for the community's own app, so the screen would ask a member to
approve something already theirs on their first sign in. `deployment apply` and `oidc
client create` therefore send `skipConsent: true`, and the gate's client
likewise. An app that wants the screen sets `sso_consent: true`:

```yaml
apps:
  talk:
    settings:
      sso_consent: true   # Pocket ID asks a member to consent on first sign in
```

`validate` refuses a value that is not true or false, because a string such as
`"yes"` would read as off and the screen the operator asked for would never
show. Unlike a launch URL, an existing client's consent setting is always made
to match the configuration, in either direction: it is the configuration's to
say and nothing else depends on the old value. A differing client plans `set
consent screen for <app>`, a `PUT /api/oidc/clients/<id>` that sends every field
back as read with only `skipConsent` changed, as the launch URL update does.

**An existing client's launch URL is changed only when the toolkit put it
there.** That means empty, which nobody chose, or one of the toolkit's own
defaults (`blueprints.ToolkitLaunchURLs`): the bare `https://<hostname>`, and
the blueprint's default when it names a path. Either plans `set launch URL for <app>`: a `PUT
/api/oidc/clients/<id>` that sends every field back as read with only
`launchURL` changed, because the update overwrites the whole client. That keeps
the callback URLs, PKCE, the public flag, the group restriction (sending it
false would clear the allowed groups) and the federated credentials; it sends no
secret and no logo URL, and Pocket ID ignores a secret sent there anyway. So a
Mbin client made with the bare host moves to the connect route on the next run:

```
  set launch URL for client talk: PUT /api/oidc/clients/<id> with launchURL "https://talk.example.org/oauth/oidc/connect" and every other field sent back as it is now
```

A blueprint that has no launch URL (`no_launch: true` under its `oidc`
block: the `account` and `oauth2-proxy` blueprints) is created without one, and
an existing client holding one of those same toolkit values plans `clear launch
URL for <app>`: a `PUT /api/oidc/clients/<id>` that sends `launchURL` as an
empty string and every other field back as read.

Any other value is an operator's and is never overwritten. With a launch URL
the plan reports it as already there, not the one wanted; without one, it says
the blueprint does not use it and leaves it as it is. If `sso_dashboard_link` is set and the client holds a
different custom value, the command also warns, naming both, and still changes
nothing: clear the launch URL in Pocket ID's admin UI and re-run to use the
setting, or remove the setting or make it match to keep the client's. The list
of toolkit defaults is explicit rather than a rule like "anything under the
app's hostname", because such a rule would overwrite a tile an operator pointed
at a magazine on purpose. A value joins the list only once a release has
actually sent it.

**The secret is never printed and never captured.** Pocket ID accepts a client
secret the caller supplies (`dto/oidc_dto.go:77-83` at `v2.14.0`), so the
toolkit generates it on the workstation, writes it into the secrets file,
re-encrypted to the recipients in `.sops.yaml`, and only then sends it. The
order is the point. A run interrupted between the two leaves a recorded secret
that Pocket ID does not hold; the next run sees that, because Pocket ID keeps
each secret's first four characters (`model/oidc.go:43`), and sends the
recorded one. The other order would leave a live secret at Pocket ID that is
recorded nowhere. If the secrets file cannot be written, Pocket ID is sent
nothing.

**It is idempotent from the probe.** An existing client with the right
callback, PKCE and restriction is marked `✓`, and its secret is left alone
unless `--rotate-secret` is given. A rotation adds a secret and leaves the old
one valid, because Pocket ID allows several (`controller/oidc_controller.go:292`),
so the app keeps signing people in until `deployment apply` renders the new one. A client
that differs from what the app needs is refused, with what differs, rather than
reshaped: updating a client rewrites every field, and a client somebody shaped
by hand is not this command's to change. **Differing in either direction is a
refusal.** A client allowing less than the app needs cannot sign anyone in; a
client allowing more is a privilege leak, so an extra callback URL (a sign in
started here could finish at another host) and an extra allowed group (its
members are exactly who the member group was meant to keep out) are refused by
name, as is PKCE on for an app that sends
no challenge. The allow step, when it runs, sends exactly the app's groups,
never a union with what the client held. A launch URL that is empty or a
toolkit default is the one exception, set as above, because neither is a
choice anybody made. After `--execute` it probes again and
fails unless a fresh plan is empty.

### `app remove` takes an app off its hosts

Taking an app out of `paisans-stack.yaml` and applying stops routing to it and
leaves the rest on its hosts, as the section on left over files says.
`app remove` is the decision to take the rest away, made for one app by name.
How to run it, and its flags, are under *`paisans app remove`* in
[docs/commands.md](docs/commands.md).

**It refuses an app still in `paisans-stack.yaml`**, and the infrastructure stack,
which is never an app. It reads every site before it changes any, and refuses
when any site still holds a rendered file of the app's that no whole apply has
marked left over. The mark is the proof the command rests on: a whole apply
marks the files of an app it no longer renders, so a site whose entries for
the app are all marked has been applied since the app left, and on the gateway
that apply took the app's routes out of the Caddyfile. An unmarked entry means
the gateway may still route to the app, so the answer is `paisans deployment apply` on the
site named, then this command again. When nothing of the app is found
anywhere it says so and exits 0.

**It touches only what is provably this deployment's and this app's.** Each
thing below is proven a different way, because each is recorded in a
different place:

| What | Proven by | Without `--delete-data` | With it |
|---|---|---|---|
| containers and networks | this deployment's label and the `paisans-<token>-<app>` compose project, both | stopped and removed | the same, with the containers' anonymous volumes |
| rendered files | a manifest entry marked left over, under the app's stack directory or named for one of its routes | deleted when the content still hashes to the entry, which is then dropped; a file edited on the host is kept with its entry | the same |
| the stack directory | its path, `/srv/paisans/<token>/<app>` | the directories rendered files were in removed once empty, and the stack directory with them; what the app wrote there is kept, with the empty directories in it, which a database's data directory needs | deleted, unless it holds a file edited on the host |
| named volumes | the label and the project | kept | deleted |
| its client at Pocket ID | the ID recorded under `oidc_clients.<app>`, held by a client with the expected name: the name in the newest version of this workstation's configuration history that declares the app, else the name the current configuration gives it (its `display_name`, else the app's, or the community's for the gate) | deleted | deleted |
| its database and role | named `render.DBIdentifier(<app>)` on the cluster's leader, the database owned by that role | kept | dropped |
| its Garage buckets and keys | the key IDs recorded under `apps.<app>`, and a bucket only those keys are granted on | kept | the bucket emptied and deleted, then the keys |

A container or network without this deployment's label is never selected,
whatever its name, because the selection is Docker's own label filter. A
client with the client's name and no recorded ID could have been made by hand,
so it is kept and named for an administrator to delete; nothing is deleted at
Pocket ID by name alone. A database whose owner is another role, a name a
declared app still uses, and a bucket another key can reach are kept the same
way, each with the reason. The client step runs once, from the site `oidc
client create` calls. When a client ID is recorded for the app and the
expected name comes from the current configuration, because the workstation
holds no configuration history or no version in it declares the app, it
warns, and `paisans deployment pull --execute` fetches the history the sites
hold.

**Deleting member data needs a person at a terminal, or `--yes`.**
`--delete-data` plans the deletions, and on `--execute` asks for the app's
name to be typed. With `--execute`, stdin that is not a terminal is refused
before any host is read, unless `--yes` types the name, which it does only
where the environment sets `PAISANS_ALLOW_YES=1`: a command line in a script
or a shell history can be run again by somebody who did not mean it, and
nothing brings the data back.

**Every step is safe to repeat.** On each site, in order: the containers and
networks, the files and then their manifest entries, the directory and, with
`--delete-data`, the named volumes. Then the client at Pocket ID, then the
database, then each bucket and the keys. Each step reads what is there when it
runs and does nothing once it is done, and the first failure stops the run, so
the same command run again resumes where it stopped. The manifest is read
again just before it is written, and written as `deployment apply` writes it, whole
through a temporary file moved into place, keeping every entry that is not
the app's. An entry whose file is already gone is dropped, which is what a
run stopped between the two leaves.

**The monitor is reseeded last.** Every monitor site's `uptime` stack is
applied on its own once the app is off every host, so the seed rendered
without the app replaces the one that still checked it and the restarted
monitor deletes those checks (see *Topology commands reseed the monitor*).
That is the one step `app remove` runs on a site the app never ran on, and it
is planned from the render and the monitor's host rather than from the left
over marks. A failure there comes after everything of the app's is gone and
names the `deployment apply` that finishes it.

**It leaves three things and says so.** The app's secrets stay in the secrets
file, which it never edits, so they are listed for removal with `sops`; it does
take the app, and each Pocket ID group only it named, out of every gateway's
deployment record, so `paisans secrets prune` can remove them instead (see
*Three kinds of secret*). Its
DNS records stay until `paisans dns prune --execute` deletes the ones
`dns init` made. And everything kept above is listed again at the end, with
why.

### Adding the gateway and the witness

The straightforward case, because the VM has a stable address and is the one
thing everything else can dial. There is no command for joining it on its own:
the VM is declared in `paisans-stack.yaml` like any other site, with its roles, its
mesh address, its `public_address` and its `ssh` section, and the whole
deployment is applied (see *`paisans deployment apply` with no `--site` converges the
whole deployment*):

```
paisans deployment apply             # every step it would run, and what each would change
paisans deployment apply --execute   # runs them, stopping at the first failure
```

For a VM joining a running site, home-a, as `roles: [gateway]`, the whole
apply reads what runs and plans the steps below. If home-a served as the
gateway until then, handing the role over is the change *Moving the gateway*
describes.

1. `init`, which generates the VM's WireGuard keypair on the workstation and
   records it in the secrets file (see *Where WireGuard keys are generated*).
2. `site prepare` on every site: the VM gets the keys of the operators its
   `ssh.keys` names, its configuration store and its daemon settings, and
   home-a, already prepared, plans nothing. Its owner admitted the VM before,
   which gave it the user, Docker, WireGuard and a firewall following its roles.
3. The pull: the VM pulls the images its apply will start, at the same time as
   any other host with images to pull, and home-a, which has them, pulls
   nothing.
4. `dns init`, for any record the VM's names need.
5. `deployment apply --site vm`, among the sites outside `etcd.members`. The VM's mesh
   interface lists home-a as a peer with no endpoint: home-a dials out.
6. Pass two, `deployment apply --site` on every site. Home-a's mesh interface gains the VM
   as a peer **with** an endpoint, and `PersistentKeepalive` so the NAT mapping
   stays open, synced through the helper so nothing on home-a restarts (see
   *`deployment apply` brings the mesh interface up before anything binds to it*). Each
   of these applies ends by sweeping the images its site's stacks no longer
   use (see *`deployment apply` prunes the images it superseded*).

The run ends `Deployment complete.`
Each apply reads its mesh interface back before any stack starts. None waits
for a handshake, because a tunnel to a site outside etcd carries no quorum: if
it does not come up, the VM's own traffic waits and nothing that already runs
is touched. The handshake gate belongs to the change that puts quorum across
the tunnel, `site add`'s stage 2, which verifies a handshake in both
directions before any etcd membership changes (see *The staged gate, and the
half-joined site*).

etcd stays a single member. The VM does not become a voter yet, because two
voters are worse than one. With `etcd.members` left out, the `witness` role is
what would make it one, so the VM joins as `gateway` alone and gains `witness`
in the change that adds the second data site, below. Declared with both roles
beside one data site, it would make two voters, which `validate` refuses
(`two-etcd-voters`), saying to take the witness role off while there is one
data site to vote.

### `site add`: a second data site, and the one hard problem

Home-b is declared in `paisans-stack.yaml` at its end state, `roles: [data]`, and the
VM gains the `witness` role in the same edit, so the voters go from one to
three at once (see *Adding a site, and the constraint that shapes it*). The
whole apply then runs `init` for home-b's secrets, `site prepare` on
home-b, the pull of home-b's images, `dns init`, a `site add` for each data
site the running cluster has not admitted, the storage step, and pass two. `site add` is the staged join
below: it admits the VM to etcd first and home-b after it, one learner at a
time, then starts home-b's replica. The VM has no `site add` of its own: a
witness joins inside the data site's join, because a witness joining alone
would make the voters even, and `site add vm` refuses saying so.

Home-b reaches the VM the same way home-a does. Fine.

**But home-a and home-b are both behind NAT with changing addresses, and neither
has an endpoint the other can dial.** WireGuard needs one side to know where to
send the first packet, and neither does.

**The toolkit relays home↔home through the VM.** It works immediately with no
new machinery, and it keeps the networking to static configuration with no
daemon and no coordination service, which is why plain WireGuard was chosen.
The cost is latency: with `synchronous_mode: true` every commit waits a round
trip, so writes pay home-a → VM → home-b. Siting the VM near the homes largely
mitigates that and costs nothing, so site it there and then measure.

The relay carries traffic; it must not carry quorum. When both homes are
voters beside the VM, the VM is the one path between them, and its loss
leaves every voter alone (see *The voters that survive one loss must still
dial each other*). `validate` refuses that shape, and the way out is the
same dynamic DNS name and forwarded port that makes one home dialled: the
homes then peer directly and the relay is only ever a convenience.

### The staged gate, and the half-joined site

**Never touch etcd membership until every tunnel is verified.** The reason is
arithmetic: going from one member to three with both new members unreachable
leaves one of three, no majority, and **the cluster stops.** A working
single-site deployment can be taken down by trying to add to it.

So a join is staged, and every stage is a gate:

| Stage | Gate before proceeding |
|-------|------------------------|
| 1. Preflight | every check passes |
| 2. WireGuard pushed and up | **handshake verified in both directions, from both sides** |
| 3. etcd one to three, one learner at a time | all three members report healthy |
| 4. Spilo joins, clones, streams | replication lag converging |
| 5. Synchronous mode, when configured | a `Sync Standby` exists |
| 6. HAProxy backends | HAProxy routes only to the primary |
| 7. Each existing replica's `patroni.env`, one at a time | the replica streams again |
| 8. The monitor's seed, with the new site | the monitor runs on a seed that matches the render |

Failing at stage 2 rolls back cleanly: drop the peer entries, leave the running
cluster untouched. Failing at stage 3 or later is harder to undo, which is
exactly why stage 2's gate must be strict.

`site add` must be **idempotent**: re-running after a fixed network problem
resumes rather than restarting.

### `site add` is built for sites that dial each other

`paisans site add <site>` implements the table above for one case: a new site
with `roles: [data]` joining a running cluster of one, together with the
witness that makes the voters three (an existing site gaining the witness role,
or a new site holding it, which preflight checks as a blank host like the new
site), and **every site dialled**, by its
`public_address` or its `endpoint` (see *Where the mesh dials a site*).
A site with neither is refused, naming the relay design above, which is still a
design: the first deployment that needs it builds it. A witness has no `site
add` of its own: it joins in the data site's join, witnesses first in stage 3,
and `site add <witness>` refuses saying so. A second Garage node
is `storage add`'s (see *More than one Garage site: `storage add`*), and
removing a site is `site remove`'s (below). Apps on
the new site come after the join rather than in it, since no stage starts an
app stack. A site declared `roles: [data, apps]` joins through the whole
apply, in one run: its `site add` joins it with the data role alone and none of its apps, the
site's own `deployment apply` then starts its apps, and the gateway's apply follows,
whose routes gain the site. `site add` typed alone refuses such a site and
names that run.
Pocket ID stands by there while another site's instance is active (see
*Pocket ID runs on every apps site, and one of them is active*). `docs/specs/2026-10-07-site-add.md` is the approved specification.

The whole apply runs `site add` for each data site the running cluster has not
admitted. Running it on its own, and its flags, are under *`paisans site add`*
in [docs/commands.md](docs/commands.md).

The configuration is the end state, and `site add` plans only what differs
from the live deployment: the `psns-<token>.conf` on each site, `etcdctl member list`,
`patronictl list` and `show-config`, and HAProxy's statistics. A stage the live
state already satisfies plans no steps, and its gate is still checked, so a
re-run proves each stage again and resumes at the first gate that fails.
Preflight is the exception: it checks a host the join has not touched, and once
stage 2 has run the new site's own mesh interface holds the port it is dialled on,
which the ports check refuses. A join is recognised as started from what only
stage 2 or later leaves (the new site's `psns-<token>.conf`, or its etcd member), and the plan says preflight is
skipped and why.

| Stage | What runs | Gate |
|-------|-----------|------|
| 2. Mesh | each site's `psns-<token>.conf`, as a scoped `deployment apply`, then the helper's `sync` (the new site's interface is started with its `up`) | a handshake younger than two minutes for every pair, read on both ends, and a ping of the new mesh address from every site |
| 3. etcd | for each joiner, every witness first: a joiner whose member has not started, one etcd does not list or a learner that never started, has any etcd container of the deployment removed and its data directory, `infra/etcd`, emptied (below, *A joiner starts from an empty data directory*); `member add --learner` unless etcd lists it, its compose file, record and every `env_file` its infrastructure stack names written (`patroni.env`, `caddy/caddy.env`, `heartbeat/heartbeat.env`), `up -d etcd` alone, `member promote` retried until etcd accepts it | `endpoint health --cluster` all healthy, and the voters are exactly `etcd.members` |
| 4. Replica | the new site's remaining files by a whole `deployment apply`, then `up -d`, which starts Patroni beside the running etcd | the member `streaming`, its replay lag zero or falling over three samples five seconds apart |
| 5. Cluster configuration | `patronictl edit-config --force -q -s synchronous_mode=true -s synchronous_mode_strict=<value>` when the live values differ | a member with the role `Sync Standby` |
| 6. HAProxy | `haproxy.cfg` as a scoped `deployment apply`; the site's cluster database apps stopped, HAProxy alone restarted, the apps started and each through `deployment apply`'s health gate | the statistics list every cluster site, the leader `UP` and every replica `DOWN` |
| 7. Replicas' `patroni.env` | on each existing replica, one at a time: `patroni.env` as a scoped `deployment apply`, then `up -d --no-deps --force-recreate --timeout 60 patroni`; nothing when the cluster had no replica | the replica runs with the new `ETCD3_HOSTS`, and its own Patroni, asked through its REST API from inside the container, answers `running` and `streaming` before the next is touched; `patronictl list` reads a member key the old process may have written, so it is not asked |
| 8. Monitor | every monitor site's `uptime` stack applied on its own (`deployment apply --only <app>`), so its `monitors.json` gains the new site's ping and direct checks and the monitor restarts on it; skipped when no site holds the monitor role (see *Topology commands reseed the monitor*) | the seed on the host matches the render and the stack is healthy; a failure here stops nothing of the join and names the `deployment apply` that finishes it |

**Learners, one at a time.** A full voter added to a cluster of one makes the
quorum two before it has started; if it then fails to start, the cluster stops.
A learner does not vote, so the cluster keeps its quorum while it catches up,
and etcd refuses the promotion until it has (`ErrLearnerNotReady`, etcd
v3.5.16), which is why the promotion is retried with a doubling delay rather
than timed. Between the witness's promotion and the new site's, the
cluster has two voters; that is the price of one learner at a time, and it
lasts one stage.

**A joiner starts from an empty data directory.** etcd starts from a data
directory that holds a member as that member, and once a member is removed the
cluster never accepts its ID again (etcd v3.5.16, `server/etcdmain/etcd.go`
skips bootstrap when the member directory exists). A site whose member left
keeps `/srv/paisans/<token>/infra/etcd` on its host: a witness that `site
remove` took out of etcd, a site taken out with `site remove --host-gone` whose
host comes back, or a removal that stopped part way. Started from that data,
its new learner would be the removed member, exit, and restart in a loop, and
it stays in etcd's member list as a learner that never started. So before a
joiner whose member has not started is added, its data directory is emptied,
and the directory itself kept for the container to mount. A learner an earlier
run added that never started may still have received a snapshot or log entries
before it stopped, and the leader remembers how far it got, so it is not
started again from empty data under the same ID: its container is removed,
it is read again and, still unstarted, removed from etcd, its data emptied,
and it is added again under a new ID. A learner that started meanwhile keeps
its data and is carried on.

**The data is deleted only on evidence of whose it is.** `site add` reads the
data path on disk through `rootlesskit`, after removing any etcd container of
this deployment there (found by its labels, so stopping it loses nothing),
and deletes it only when it is one of:

| What the path holds | Why deleting it loses nothing |
|---------------------|-------------------------------|
| nothing, an empty directory, or directories with no file in them (Eg a bare `member/`) | there is nothing |
| etcd's member layout, every write-ahead log in it decoding (framing, record, CRC-32C chain) to the same member and cluster, the cluster being this one and the member one this cluster removed for this site | a removed member's data, whose ID the cluster never accepts again |

The cluster ID alone is not that evidence. etcd derives a founding member's
ID from its peer URLs and the cluster token, and the cluster's ID from its
founders' (etcd v3.5.16, `membership/member.go` and `cluster.go`), so two
clusters founded on the same mesh addresses with the same token share both:
a deployment founded again on the same addresses, or a copied configuration.
So the member ID must also be one this cluster removed for this site: each
`site remove` and each learner `site add` replaces records the removal, before
making it, in `.paisans/etcd-removed` on the hosts of the members that stay,
and `site add` reads those records from every host. A member ID the cluster
has now is refused whatever is recorded. A cluster founded since founding
tokens were recorded (below, *A member keeps the flags it was born with*) has
founder IDs of its own. One founded before, on the fixed token `paisans`,
does not, so a removed founder's data there is accepted on its recorded ID
and the cluster ID, which another cluster of this deployment founded on the
same addresses would also match; that is the risk that remains.

This cluster's data is deleted rather than kept because it holds only
Patroni's store, runtime state (the leader lock, sync state, the `initialize`
key and the dynamic configuration) that every remaining member also holds; it
is not operator data, so `--delete-data` does not govern it. Anything else is
refused before anything is deleted, naming the site and what was found:
another cluster's ID, a member this cluster has no record of removing for the
site or has now, logs that disagree or do not decode, any name outside etcd's
layout (Eg: a `member.bak` moved aside by hand), a symbolic link anywhere,
a path that is not a directory or cannot be read. A member directory with no
write-ahead log is refused too, with the command that deletes it by hand,
`rootlesskit rm -rf -- /srv/paisans/<token>/infra/etcd/member`, for the
operator to run once it is certain to be theirs to lose. etcd leaves a
database alone when it exits before it reaches a peer, Eg a learner that
never started, but nothing in a database says whose it is or whether it holds
keys: one rewritten by a defrag looks like a fresh one. When the join is planned
nothing is stopped, so an etcd running there is also asked, and one that
answers for another cluster is refused then; its answer never permits the
delete. A refusal deletes nothing: data of another cluster is its owner's to
move off the host, or the site is taken out of this deployment. A voter whose
answer carries no cluster ID refuses the join. The check runs when the join
is planned and again before each clear. A voter, or a learner that has
started, is never cleared. The files are the etcd container's, which under rootless Docker
are expected, not yet observed, to be owned by a subordinate UID, so each is
deleted through `rootlesskit`, as `site remove` deletes its own. Such a joiner
is also given the membership as it stands, not the flags its host records
from its earlier join (below).

**A member keeps the flags it was born with.** etcd reads
`--initial-cluster` and `--initial-cluster-state` only on a member's first
start and ignores them once its data directory exists. They are inert on a
running member, so the only thing changing them can do is make `deployment apply` see a
new compose file and recreate a healthy member. Each etcd host therefore has
`/srv/paisans/<token>/infra/etcd-initial`, a rendered file holding the flags its member
started with, which every later render repeats. A member founded now also
records its `--initial-cluster-token`: chosen at random for each founding
(the deployment's prefix and 64 random bits), taken by a later founder from
the record of one that has it, so every founder of one cluster agrees, and
giving the cluster IDs of its own (above). Run one `deployment apply` of a
deployment at a time while its etcd is being founded: two runs founding at
once each choose a token before either is recorded, and founders with
different tokens do not form one cluster. A member whose record has no token
renders the fixed token `paisans` it was founded or joined with, so nothing
running is rendered differently; a joiner's token is not read by etcd. A founder records `new` and the
founding set. A joiner records `existing` and the membership **right after its
own `member add`**, which etcd requires exactly: a joiner whose
`--initial-cluster` counts a different number of members than the cluster has
is refused with "member count is unequal" (etcd v3.5.16,
`membership/cluster.go`, `ValidateClusterAndAssignIDs`). When the witness joins
first, the new data site is not a member yet, so the two joiners' flags differ.
A host applied before the record existed has its compose file's own flags read
instead, so its first apply under this rule changes nothing it runs. Writing
the record never acts on a stack, because no container reads it. Only a member
that has started keeps its record: a joiner that has not started is given the
membership as it stands, since a record its host keeps from a member that left
names a past membership.

The specification named the file `etcd-founders` and gave every joiner all of
`etcd.members`; both were corrected here for the reason above.

**`deployment apply` refuses a half grown cluster.** While the live membership (learners
included) differs from `etcd.members`, `deployment apply` on a site that is, or is
configured to be, an etcd member refuses to write or act on its infrastructure
stack, and says to run `site add`. An operator who edited the configuration
and ran `deployment apply` first would otherwise start an etcd the cluster never admitted.
An app only change on the same site is let through.

**Existing sites move one file each.** A whole `deployment apply` of the primary's site
would recreate its Patroni, because `patroni.env` now lists every etcd member:
a failover in the middle of a join. So stages 2, 6 and 7 use a scoped `deployment apply`,
which compares, refuses on conflict, writes and records exactly like a whole
one, but only for the named file, and runs only the one command that file
needs.

**A replica's `patroni.env` is applied; the leader's waits.** Patroni reads the
etcd hosts in it only to reach etcd at start: with `use_proxies` off, the
default and what Spilo renders from `ETCD3_HOSTS`, it then asks etcd for the
cluster's members and uses those, refreshing them as it runs (Patroni v4.1.0,
`patroni/dcs/etcd.py`, `_load_machines_cache` and `_refresh_machines_cache`).
An out of date list is harmless at runtime and matters at the next start. So
stage 7 recreates each existing replica's Patroni with its new file, one at a
time, which is a replica restart HAProxy does not notice since it routes only
to the primary, and waits for it to stream again before the next; a replica
that leads by then is left alone. The primary's is left, and the plan says
so: `deployment apply` of that site writes it. `site remove` does the same
with the same code.

**An etcd hosts change alone does not recreate Patroni.** When the only
difference in a site's `patroni.env` is `ETCD3_HOSTS`, `deployment apply`
writes and records the file and leaves Patroni running, and its plan says so.
The rendered file sets no proxy, so Spilo configures Patroni with `use_proxies`
off (Spilo `configure_spilo.py`, `get_dcs_config`; Patroni v4.1.0,
`patroni/dcs/etcd.py`, `get_etcd_client`), and Patroni uses the list only to
fill its members when it starts or after every member it knows has failed,
refreshing them from etcd's member list otherwise (`_load_machines_cache`,
`_refresh_machines_cache`). The leader therefore keeps the leader key through a
join: `deployment apply` without `--site` joins a site through `site add` and
then, in pass two, applies every site, the leader's among them.

**A running Patroni is left out of an `up -d` that has no reason to replace
it.** Compose replaces a container whose `env_file` content changed, so once
`patroni.env` holds etcd hosts the running Patroni was not created with, a
plain `up -d` of the infrastructure stack for any other reason (a changed
compose file or environment file, a stopped Garage to start) would replace
Patroni too. So `deployment apply` takes whether Patroni runs from its read of
which containers run, and asks a running one which etcd hosts it has
(`printenv ETCD3_HOSTS`; nothing else in the container is compared with the
file). When nothing Patroni reads changed (`patroni.env` beyond
`ETCD3_HOSTS`, its service in the compose file, a file it mounts), the stack's
`up -d` names every other service, with `--no-deps`, and an apply that stops
part way owes a forced recreate of those services alone. A running Patroni
that does not answer is planned as one the `up -d` replaces, with the warning
below, since leaving it out would rest on an answer the apply does not have.
A stopped one is started by the `up -d` from the file as written. A `docker
restart`, a reboot or the restart policy keeps the container, and with it the
hosts it was created with, so the plan says when they differ from the file,
compared as sets. They are enough while they name the site's own etcd member,
which starts with Patroni after a reboot and from which Patroni learns the
rest; once they do not, the plan recreates Patroni alone
(`up -d --no-deps --force-recreate --timeout 60 patroni`).

**A leader that moves is reported, never left silent.** A change Patroni reads
only in a new container, a forced recreate of the infrastructure stack, a
restart that includes Patroni, a running Patroni that does not say its etcd
hosts and a running list without the site's own etcd member all replace or
restart Patroni, and on the leader a standby takes the leader key while it
restarts. The plan says so before it runs, at every verbosity.
After it runs, an apply whose infrastructure stack's action leaves another site
leading warns, naming both sites, the action and the files that changed, and
`deployment apply` without `--site` repeats each warning once its steps are
done. Whether the site leads is asked of its own Patroni (`/patroni`) just
before that action, and who leads is read from `/cluster` by the wait for a
primary after it.

**HAProxy is restarted, not reloaded.** Its configuration is a single file bind
mount, `deployment apply` replaces a file by renaming a new one over it, and a bind mount
of a single file keeps the inode it started with (moby/moby#15793), so a
reloaded HAProxy would read the old file. Restarting HAProxy alone remounts it,
and is what `deployment apply` itself does for a changed `haproxy.cfg` (see "`deployment apply`
restarts only the infrastructure service whose file changed"). The gate
reads HAProxy's statistics from a listener on the site's mesh address, port
8404 (Eg: `10.44.0.1:8404`), as CSV. That listener is new in every rendered `haproxy.cfg`, so a deployment that applies
before growing restarts its infrastructure stack once for it.

**The apps that use the database are stopped around HAProxy's restart.** On
the first real join, stage 6 restarted HAProxy under a running Mbin, and Mbin
answered HTTP 500 from then on (`SSL SYSCALL error: EOF detected`, then `no
connection to the server`) until it was recreated by hand. Its FrankenPHP
workers keep their database connections open across requests and do not
reconnect when one is closed, and its healthcheck does not touch the database,
so the container stayed `healthy` throughout. Pocket ID recovered on its own.
So on each site whose HAProxy restarts, stage 6 first stops every app stack
there with `cluster` placement and a kind that uses Postgres (`docker compose
-f /srv/paisans/<token>/<app>/compose.yaml stop`), restarts HAProxy, starts them again (`up
-d`), and holds each to `deployment apply`'s health gate; a failure stops the join there
with the cause read from the app's logs, as `deployment apply` shows it, and the logs
themselves with `-v`. The plan lists the outage: stop, restart, start, check. A
pinned app has its own Postgres and is left alone. This is one instance of a
rule the toolkit owns: whatever changes the database path restarts the apps
that depend on it (see "`deployment apply` restarts the apps whose database path
changed").

### `site remove` takes a site out

The reverse of `site add`, for one site by name. The site stays declared in
`paisans-stack.yaml` while the command runs, because its `ssh` section is how its
host is reached and its roles say what it holds; everything else is computed
from the configuration with the site taken out, the end state, and the last
stage writes that back to the file. `docs/specs/2026-10-08-site-remove.md` is
the approved specification. How to run it, and its flags, are under
*`paisans site remove`* in [docs/commands.md](docs/commands.md).

**It refuses before anything changes**, with the evidence and what to do:

| Refused | Because |
|---|---|
| the only gateway | nothing else would answer for the community's hostnames; move the gateway first |
| the only apps site while an app is placed `cluster`, or a site an app is pinned to | the app would run nowhere; give another site the role, or move the app, first |
| the only data site | the database would have nowhere to live |
| the first site in `storage.garage.sites` while apps are declared | every app writes its objects through that node; put another Garage site first and apply every site running an app |
| a Garage site whose removal leaves fewer nodes than `storage.garage.replication`, or another Garage node unhealthy | the objects on it would have nowhere to go |
| a site in the end state's `storage.garage.sites` with no node in Garage's live layout, or fewer layout nodes left without the site than `storage.garage.replication` | `garage layout apply` would refuse halfway through stage 1; run `storage add` first |
| Garage layout changes staged and not applied, other than the site's own removal | `garage layout apply` commits every staged change, so they would go live with the removal; apply them with `storage add`, or revert them with `garage layout revert --yes`, first. The site's own removal, staged by a run stopped before its apply, is resumed |
| an end state `validate` refuses | Eg: two data sites left as two etcd voters, when three data sites go to two; add a witness first |
| one data site out of two and a witness, when the witness role is that site's only one and no app is pinned to it | it would be left with no role; give it another first, or `site remove` it afterwards |
| one data site out of two and a witness, with either remaining voter unhealthy | going from two voters to one is a change both must commit |
| any other etcd member unhealthy, or too few healthy members left for a quorum | a membership change on a limping cluster is how it stops |
| a site in the end state's `etcd.members` that etcd does not list, or lists as a learner | its join never finished, so fewer voters would remain than the file says; finish its `deployment apply` first. It counts as unhealthy in the quorum |
| a remaining member whose etcd answers for a different cluster than the others', by the cluster ID in `etcdctl member list`, or its member IDs where no ID is given | Eg: its data directory was wiped and it founded a cluster of its own; every remaining member whose etcd runs is asked, and the odd one out is named. Run `doctor` on it |
| any other Patroni member not running or streaming | the cluster changes under the stages below |
| the site holds the leader and no member is a streaming `Sync Standby` | only a synchronous standby takes over without losing writes the leader acknowledged |
| a host that does not answer over ssh | what is on it cannot be read; fix ssh, or say `--host-gone` |
| `external.acme_dns_token` empty while a site runs the toolkit's Caddy | the same refusal as `render` and `deployment apply`: stage 2 reloads the gateway's routes, and a Caddy without its token obtains no certificate |
| `--delete-data` without a terminal or `--yes`, or with `--host-gone` | the same rule as `app remove`; and nothing is deleted on a host that is not reached |

**Six stages, each ending at a gate.** A removal with `--host-gone` runs its
own stages instead, the mesh cut first (see *Removing a site whose host is not
reached: `--host-gone`*). Each is planned from live state, so a stage already
done plans no steps, its gate is still checked, and a re-run after a fixed
problem resumes at the first stage with anything left to do. The last one is
the exception, since the stage before it is what makes a re-run impossible: a
failure there names the `deployment apply` that finishes it.

| Stage | What runs | Gate |
|-------|-----------|------|
| 1. Data out of the site | the leader switched over to the `Sync Standby` when the site holds it; `synchronous_mode` turned off when one data site remains, since a leader waiting on a standby that is leaving stops taking writes; Patroni stopped on the site and its member key deleted from etcd; its Garage node removed from the layout and the layout applied | another site leads and `patronictl list` no longer lists the site, with a `Sync Standby` when the end state wants one; no layout row for the site, and every remaining node settled (one live layout version, an empty resync queue) within an hour, and a run that times out resumes at this gate while Garage carries on copying |
| 2. Out of the cluster | `etcdctl member remove`, then the site's etcd stopped and its etcd data directory emptied (below); with two data sites and a witness, the witness's member next (below); on every remaining site, the files whose render changes with the site gone, by scoped `deployment apply`: `psns-<token>.conf` (the helper's `sync`), `haproxy.cfg` (HAProxy restarted with its database apps stopped around it, as `site add` does) and the gateway's routes (validated, then Caddy reloaded); then each remaining replica's `patroni.env`, one at a time, as `site add`'s stage 7 does; last, the site taken out of every remaining gateway's deployment record | the voters are exactly the end state's and all healthy; no remaining site has the site's key as a WireGuard peer; HAProxy lists exactly the end state's cluster sites with the leader `UP`; each replica streaming again before the next |
| 3. Clean the host | below. When the site holds the active Pocket ID instance, the plan says that once this stage stops it, sign in is unavailable for a few seconds while a standby takes over, up to about 90 seconds if the instance does not stop cleanly | checked last: nothing of this deployment's left but what the plan said it keeps |
| 4. DNS | the address records this deployment's `dns init` made for the site, deleted at the provider (below, *The site's DNS records go with it*); skipped, saying why in one line, when `acme.provider` is not set, its token is not in the secrets, or the site has no public address | listing the zones again finds nothing left to delete |
| 5. Config | the site taken out of `paisans-stack.yaml`: its block, its name in `cluster.sites`, `etcd.members` and `storage.garage.sites` where the file writes them, its capacity; with two data sites and a witness, the witness out of a written `etcd.members` and its `witness` role, in the same write; every other byte as it was | the file loads and no longer declares it |
| 6. Monitor | every remaining monitor site's `uptime` stack applied on its own, so its `monitors.json`, rendered from the end state, no longer names the site and the restarted monitor deletes its ping and direct checks; skipped when no site holds the monitor role (see *Topology commands reseed the monitor*). The monitor site itself is never the one removed, since its app is pinned to it | the seed on the host matches the render and the stack is healthy; a failure here comes after the removal and names the `deployment apply` that finishes it |

**Two data sites and a witness go to one voter.** Taking one data site out of
two and a witness would leave two etcd voters, and two are worse than one. With
one data site left, the witness has no tie to break, so when the end state's
etcd members would be exactly the remaining data site and a site that is a
member only because it is the witness, that site leaves etcd in the same
removal and loses the `witness` role, keeping its others; the end state must
still validate. In stage 2 the leaving site's member goes first; then, once
etcd's voters are exactly the remaining data site and the witness, both
healthy, the witness's member is removed, leaving one voter with a quorum of
one, and the witness's etcd container is stopped and removed. It is found by this
deployment's label, the infrastructure project and the `etcd` service, since
the witness's `infra/compose.yaml`, written by the same scoped `deployment apply` as its
mesh file, no longer declares it. Each leaving member's etcd data directory,
`infra/etcd`, is then emptied through `rootlesskit` and the directory kept,
the data site's and the witness's alike, and in any removal of an etcd member:
the data is a removed member's, which etcd never accepts again, and holds
nothing the remaining voters lack. A run resumed after a member was removed
empties it too. Before each member is removed, the removal (site, cluster ID,
member ID) is recorded in `.paisans/etcd-removed` on the hosts of the members
that stay and of a witness that stays a site, which is the record `site add`
needs before it deletes that site's data if it rejoins (see *A joiner starts
from an empty data directory*). With `--host-gone` the leaving host is not reached, so its etcd
data stays there and the report says so; `site add` clears a joiner's data
for itself (see *A joiner starts from an empty data directory*) and does not
depend on this. Two voters going to one is a change both
must commit, so it needs both healthy. A re-run reads which members are left,
so one stopped between the two removals does only the second.

**What stage 2 does not move.** The leader's `patroni.env`, as in `site add`:
recreating its Patroni would be a failover, and the out of date etcd hosts in
it are harmless while it runs (see *A replica's `patroni.env` is applied; the
leader's waits*). The report names `paisans deployment apply --site <site>`,
which writes it without recreating Patroni when `ETCD3_HOSTS` is its only
change (see *An etcd hosts change alone does not recreate Patroni*).
Any other file whose render changes is listed the same way. etcd's
`--initial-cluster` flags are not among them: each member's are rendered from
what its host records (see *A member keeps the flags it was born with*).

**The host keeps everything that is not provably this deployment's, and
everything that is root's.** The command logs in as the deployment's user, the
one its admission record names, and refuses any other login, root above all.
Each removal is proven a different way, because each is recorded in a
different place. What is this deployment's and kept is listed, with why; what
is not this deployment's is left alone and not listed:

| What | Proven by | Removed |
|---|---|---|
| containers and networks | the deployment label carrying this id, by Docker's own filter, in the user's own rootless daemon | stopped and removed; with `--delete-data` their anonymous volumes too. One without that label is not this deployment's, so it stays and is not listed |
| named volumes | the same label | only with `--delete-data` |
| images | run by the containers above, or named by a rendered `compose.yaml` the manifest proves, and used by no other container in the user's daemon, running or stopped | removed by ID, without `-f`, after the containers and before the files, so a re-run after a stop in between still finds them; one Docker refuses, because something started from it meanwhile, is kept and named with Docker's reason |
| rendered files | an entry in this deployment's manifest whose hash the file still has | deleted, then the manifest; a file edited on the host is kept and named |
| units | named `paisans-<token>-*` in the user's own systemd directory, `~/.config/systemd/user` | disabled, stopped, deleted; the user's manager reloaded |
| authorized keys | nothing can prove one: a key `site admit` or `site prepare` added is a key the operator logs in with | never removed. A key may be the only way into the host, and nothing on it says whether another exists; the operator deletes one by hand once it is no longer needed. This deployment's record of which keys it added, `/srv/paisans/<token>/.paisans/authorized_keys.owned`, is its own file, so that is deleted |
| the deployment's directory | its path, `/srv/paisans/<token>`, the user's | the directories rendered files were in removed once empty; what apply did not write, the data in its bind mounts with every empty directory in it (PostgreSQL's data directory needs its own), is kept, and `--delete-data` empties it, through rootlesskit so the files a container's user owns go too, unless a file in it was edited. The directory itself stays, because its parent `/srv/paisans` is root's, for `site evict` |
| the deployment record, on a gateway | `/srv/paisans/<token>/.paisans/deployed.json` | deleted |
| the mesh interface | its name, `psns-<token>`, which `site admit` gives the deployment's helper and nothing else | when the interface is up, or `systemctl is-enabled` or `is-active` (which need no root) says its unit is, as one whose start failed at the last boot still is; an answer that is not one of systemctl's states stops the plan and the gate rather than reading as a unit that is gone: after the containers, taken down by the WireGuard helper's `down`, which stops and disables `wg-quick@psns-<token>` and deletes `/etc/wireguard/psns-<token>.conf`, the site's private key. Left up, or enabled to come back at boot, it holds the site's listen port and interface name, and a `site add` of the same site refuses both. The helper and its sudoers line stay for `site evict` |
| the configuration store | `/srv/paisans/<token>/.paisans/config/<token>.git`, its `<token>.peers` and `<token>.sync.json`, a `<token>.incoming.bundle` a cut-off push left behind, and `/srv/paisans/<token>/.paisans/bin/`; the units that serve and sync it are the user's `paisans-<token>-*` and go with the units above | deleted after the units, so the daemon and the sync have stopped using them. The versions on this host go with it; every other site still holds them, which is what makes a site's removal not a loss of the configuration's history |
| the grant record | `/srv/paisans/<token>/.paisans/granted.json` (see *`deployment apply` and `site prepare` review what `paisans-stack.yaml` grants*) | deleted last: left behind, a later deployment with the same token would be reviewed against a baseline it never applied |

**What is root's is the owner's.** The WireGuard helper and its sudoers line,
the ufw rules, the deployment's user and its home, `/srv/paisans`, the
admission record and the registry entry are not touched. The plan ends with
the line the host's owner runs to remove them,
`sudo paisans site evict --deployment <id> --user <user> --execute`. Until
they do, the host is still admitted, and the site can be added back:
`paisans site prepare --site <site> --execute` first, since its
configuration store and units went with the removal, then `site add`.

The check that nothing of this deployment's that its user can remove is left
comes last.

**A gateway's Caddy is kept while it serves the host owner's sites.** The
site blocks in `/srv/paisans/<token>/caddy.d` (see *The gateway host's own
sites live in the deployment's `caddy.d`*) go dark if the gateway's Caddy stops. So while
`internal/ownership` finds a `*.caddy` file there and this deployment's Caddy
container exists, that container stays, and its configuration is reduced to
the owner's sites. `docs/specs/2026-10-10-gateway-caddy-kept.md` is the
approved specification.

* **The reduced Caddyfile** is built from the one on the host: its global
  options block (the ACME email and DNS provider, so the owner's certificates
  renew), the snippets a file in `caddy.d` may import
  (`upstream_unavailable`, `upstream_failover`, `upstream_single`) and `import
  /etc/caddy.d/*.caddy`, and no site block of the deployment's. A Caddyfile
  lacking any of these is refused, and nothing is changed.
* **It goes first, and without a gap.** The reduced file is staged in the
  snippets directory, validated inside the running container and loaded with `caddy
  reload`, which keeps the running configuration if it fails. Only then is it
  written over `Caddyfile`, in place, and read back. Any failure puts the
  original back, reloads it, and stops before anything else is removed. A
  Caddy that is not running is refused: start it and run again.
* **What stays:** the container and its image, and under
  `/srv/paisans/<token>/infra/` its `compose.yaml`, `caddy/etc/Caddyfile`,
  `caddy/caddy.env` (the DNS token, for renewals), `caddy/data` and
  `caddy/config` (the certificates) and the `caddy/etc/snippets` directory,
  emptied; and the ufw allows for 80 and 443, which are `site admit`'s. These stay with `--delete-data` too, and the plan says so.
  The manifest stays, listing exactly those files. Everything else goes as in
  the table above.
* **The admission and the registry entry stay**, still holding the gateway role
  and the token, so nothing else takes ports 80 and 443 or the directory from
  under it. `site evict` stops the Caddy and deletes `caddy.d`, so the plan
  says the owner moves the sites first.
* **Run it again** and, while the owner's sites are there, nothing changes and
  the report says Caddy is kept and for which files. Once `caddy.d` holds
  no site file, the next run removes Caddy and its files, as for any
  container. No flag removes it sooner.
* **Another deployment meets it.** While the kept deployment's admission
  holds the gateway role, `site admit` refuses the gateway role or
  `--public-ports` for another one. The owner moves the sites and runs
  `site evict` for the old deployment.

**`--host-gone` never reaches the host.** It is for a host that is dead, and
for one that still runs but that the other operators can no longer reach, and
it cuts the site off the mesh before anything else changes (see *Removing a
site whose host is not reached: `--host-gone`*).

**The site's DNS records go with it.** Stage 4 deletes the A and AAAA
records this deployment's `dns init` made for the site, through the same
provider and token as `dns init` and `dns prune`, and under `dns prune`'s rules
with the configuration before and without the site in place of the
configuration as it stands. A record is deleted only when every one of these
holds:

| Rule | Why |
|------|-----|
| it carries exactly this deployment's comment, `paisans-<token>: created by paisans dns init` | anything else was never this deployment's |
| it is an A or AAAA record, holding an address of that record's family | the only records that point at the site. A no-mail TXT record `dns init` made at the site's endpoint name points at nothing, so it is left for `dns prune`, which deletes it once the configuration no longer names the endpoint |
| its name is `community.domain`, a name under it, or one the configuration before removal produces | it sits where this deployment's names sit |
| its address is the site's own `public_address` or `public_address6` | it points at the host being removed |
| the configuration without the site wants no record of its type at its name | a name still wanted is kept |
| no remaining site declares the same address | a shared address may be the other site's record, so it is kept and the plan says so |

Every record carrying the comment that fails a rule is listed as kept with its
reasons; one kept although it points at the site's address shows them at every
verbosity. The dry run reads the provider and changes nothing:

```
stage 4, dns
  ○ delete A blog.example.org → 203.0.113.7
  ! keep A home-b.example.org → 203.0.113.7
    203.0.113.7 is also sites.home-a.public_address, which stays, so the record may be home-a's
  ○ gate: no record of home-b's is left
```

`--execute` plans again from a fresh listing, deletes each record by its id,
and lists each zone again to confirm it is gone. A provider error stops the
stage, naming what was deleted and what was not; running the same command
again resumes, since `paisans-stack.yaml` still declares the site until stage 5.
Without `acme.provider`, without its token in the secrets, for a site with
no public address, or when the provider cannot be read as the plan is made,
the stage is skipped with one line saying why, and the removal goes on.

**It leaves two things and says so,** as `app remove` does: the site's
WireGuard key and heartbeat token in the secrets file, which it never edits,
for `paisans secrets prune` (see *Pasted and captured secrets go in through a
pipe*); and everything kept above, with why.

#### `--force` cleans one host and nothing else

A full removal checks the cluster first, and when the other hosts are gone
every one of its refusals holds, so nothing cleans the host that is left.
`--force` runs stage 3 alone against one host: stages 1, 2, 5 and 6 do not
run, none of their refusals are checked, no other site is reached but the
gateways, which take a site `paisans-stack.yaml` no longer declares out of their
deployment record, and neither
`paisans-stack.yaml` nor the secrets file is edited. A site `paisans-stack.yaml` still
declares has no DNS stage, since its records are still wanted; for one it does
not declare, the DNS stage follows and is skipped, saying its records are
deleted by hand, since nothing names its public address.
`docs/specs/2026-10-09-site-remove-force.md` is the approved specification.
Examples of each form are under *`paisans site remove`* in
[docs/commands.md](docs/commands.md).

**Which host.** `--ssh user@host[:port]` when given, and it may be any host.
Otherwise the declared site's own `ssh` section. A site no longer declared
needs `--ssh`. Any host is safe to name, because nothing on it is touched
unless it carries this deployment's id or the token taken from it: the same
proofs as the table above, images included. A host this deployment never used
plans nothing.

The site's roles, which decide whether a gateway's Caddy is kept for the host
owner's sites, come from this deployment's admission record on the host, so they are right
for an undeclared site and for a host the site has left. With no record, the
declared roles are used.

**The site's own host asks first.** When the host is the declared site's own,
its cluster still counts on it: its etcd member, its Patroni replica, its
Garage node and its place in every other site's mesh stay until a full `site
remove`, and its next `deployment apply` deploys it again. The plan says so, and
`--execute` asks for the site's name at a terminal, as `--delete-data` does,
and `--yes` types it for either question. Any other host is not asked about.

| Refused | Because |
|---|---|
| an undeclared site without `--ssh` | there is no way to reach its host |
| `--ssh` not of the form `user@host[:port]` | the user is who the command logs in as; Eg: `admin@203.0.113.9` |
| `--ssh` without `--force` | a full removal reaches the site through its `ssh` section |
| `--ssh` naming a host whose admission record is another site `paisans-stack.yaml` still declares | the record is the proof of whose host it is; name that site instead |
| `--force`, `--ssh` or `--id` with `--host-gone` | they name a host to clean, and `--host-gone` reaches none |
| a host that does not answer over ssh | what is on it cannot be read |
| the site's own host, or `--delete-data`, with `--execute`, no terminal and no `--yes` | it takes a running site from under its cluster, or deletes member data |

**A lost deployment.** When the data and gateway hosts are destroyed and the
monitors survive on hosts that also run other people's services:

1. Take the monitor sites and their `uptime` apps out of `paisans-stack.yaml`, then
   run `paisans site remove <monitor> --force --ssh <its host> --execute
   --delete-data` once per monitor, from a terminal. Only what is this
   deployment's goes; the owner's containers, images, Caddy and ufw rules stay.
2. The destroyed sites stay declared, keeping their identity in the secrets
   file. Change their `ssh` sections and addresses to the new hosts, Eg:
   `203.0.113.20`, and `deployment apply` deploys them there. The new gateway's first
   `deployment apply` writes a fresh deployment record from the yaml, which no longer
   names the monitors.
3. `paisans secrets prune --execute` reads that record and removes the
   monitors' and their apps' secrets.
4. Run `paisans dns init --execute`. It updates each record it made for this
   deployment at a name still wanted to the address now declared; a record
   it did not make that points at a lost address is a conflict, changed by
   hand at the provider. The monitors' records at names no longer wanted are
   deleted by hand too.

#### `--id` cleans a host when `paisans-stack.yaml` is lost

`--force` names the deployment by the `id` in `paisans-stack.yaml`. When that file
is lost, the id is still on every host the deployment reached, in its
admission record. `paisans site deployments`, the host owner's command, lists
the records, and `--id` cleans the host of the deployment one names, logged in
as the deployment's user and reading neither `paisans-stack.yaml` nor the
secrets file. The deployment is found by the admission record of the login
itself, whose user is the one the record names; a login with no record of its
own is refused, naming the user to log in as, or the `site evict` line the
owner runs when no record names it. `docs/specs/2026-10-10-remove-without-config.md`
is the approved specification. How to run both, and their flags, are under
*`paisans site deployments`* and *`paisans site remove`* in
[docs/commands.md](docs/commands.md).

`site deployments` is run by the host's owner, through sudo, and marks each
admission record with a check mark: the deployment id, the user, the site, its roles,
whether the watchdog and public ports were granted, and when. A record it
cannot parse is a warning, `!`. With sudo it goes on to the registry's
entries (id, token, domain, site, roles and root) and, under *not in the
registry*, warns of any `/srv/paisans/<token>` directory no entry names, which
no command can name and the owner removes by hand. Piped output says `ok` and
`WARN` instead. It changes nothing. It never lists partly: when the records,
the registry or either directory cannot be read, it fails, naming what failed,
and prints nothing, rather than show a host that seems to hold less than it
does. `--sudo=false` lists the admission records alone, which any user reads.

`--id` is the full id or its token, and must be the deployment the login's own
record names; another is refused, naming the deployment the login belongs to.
The host stage is the one `--force` runs, with the site and roles from the
record and the ssh user from `--ssh`, so it removes the same things, proven
the same way: containers and networks by label, images by ID without `-f`,
named volumes only with `--delete-data`, the files the manifest proves, the
user's units, the keys record, the contents of `/srv/paisans/<token>` (empty
directories, or everything with `--delete-data`), the deployment record, and
the grant record. What is root's stays, and the plan ends with the `site
evict` line the owner runs.

What needs the rest of `paisans-stack.yaml` is not done. Every `--id` run, dry run or not, warns
`DNS records for this deployment, if any exist, were not modified`: there is no
provider or token to delete them with. The line under it is the comment to
search for at the provider, and the address when `--ssh` named one.
A gateway's Caddy is kept for the owner's sites exactly as with a
configuration, since the reduced Caddyfile is built from the one on the host.

Nothing says whether the deployment still runs elsewhere, so `--execute`
always asks for the site's name at a terminal, or takes it from `--yes`. The question names the owner's
sites a kept Caddy goes on serving, and the data `--delete-data` deletes.

| Refused | Because |
|---|---|
| `--id` without `--force`, or without `--ssh` | `--id` names what `--force` cleans off the host `--ssh` names |
| `--id` with `--config`, `--secrets` or a site | it reads no configuration, and the admission record names the site |
| `--id` neither an id nor four hex digits | it matches no record; Eg: `--id f2a9` |
| the login's admission record is another deployment's, or the login has none | everything removed is named from the id and its token; the refusal names the user to log in as |
| `--execute` without a terminal or `--yes` | it asks for the site's name |

#### Removing a site whose host is not reached: `--host-gone`

`--host-gone` takes a site out without ever reaching its host. It covers two
cases, which look the same to the cluster: a host that is dead and is never
coming back, and one that still runs and that the other operators can no longer
log in to, because an operator stopped cooperating and their host is still a
data site, a Garage node, an apps site or the gateway.
`docs/specs/2026-10-11-site-evict.md` is the approved specification. How to run
it is under *`paisans site remove`* in [docs/commands.md](docs/commands.md),
and the order of everything around it is in
[docs/guides/when-an-operator-leaves.md](docs/guides/when-an-operator-leaves.md).

**Being a WireGuard peer of the remaining sites is being in the cluster.**
etcd listens on plain `http://` with no client certificates, Patroni's REST
API has no authentication, every data site shares one set of Postgres
passwords and every Garage node one `rpc_secret`, and every published port
binds the mesh address (see *So every published port binds the site's mesh
address*). Each site has its own WireGuard key, each peer entry is a `/32`, and
there is no preshared key, so removing the site's public key from every
remaining site's mesh file and syncing it through the helper drops it: no remaining
site accepts a handshake from that key or routes anything to its address.
Cutting the peer is what takes the site out of the cluster.

**So the cut comes first, and the host is never reached.** A plain removal
cuts the mesh last, after the host has handed over its data. A host that still
runs is a peer until it is cut, its Patroni keeps writing its member key, and
a switchover needs the leader to step down, which neither a dead host nor one
run by someone else does; a dead Garage node never acknowledges a new layout
either. So `--host-gone` orders the stages from the cut:

| Stage | What runs | Gate |
|---|---|---|
| 1. Cut | on every remaining site at once, `psns-<token>.conf` without the site, by scoped `deployment apply`, synced through the helper, and a gateway's routes without its upstreams, validated and reloaded | no remaining site lists the site's key as a peer, and the helper's `show` on each has no handshake with it |
| 2. DNS | the site's address records deleted, as `site remove`'s DNS stage does, and also at names the configuration still wants (below) | listing the zones again finds nothing left to delete |
| 3. Database | when the site leads, a wait for Patroni's own failover: the cut leader cannot renew its leader key, the key expires with Patroni's `ttl` (30 seconds by default), and a remaining member takes over; then `synchronous_mode` off when one data site remains, and the site's member key deleted from etcd, which it can no longer rewrite | a remaining site leads, and `patronictl list` no longer lists the site |
| 4. etcd | `etcdctl member remove` from the remaining voters, and with two data sites and a witness the witness's member too, its etcd container removed and its etcd data directory emptied, as `site remove` does | the voters are exactly the end state's and all healthy |
| 5. Storage | the site's Garage node out of the layout, then `garage layout skip-dead-nodes` (below, *A cut-off Garage node is skipped as dead*) | no layout row for the site, and every remaining node settled |
| 6. The rest of the cluster | HAProxy without its backend, each remaining replica's `patroni.env` one at a time, and the site out of every remaining gateway's deployment record, as `site remove`'s stage 2 does | HAProxy lists exactly the end state's cluster sites with the leader `UP`; each replica streams again |
| 7. Config | the site taken out of `paisans-stack.yaml`, as `site remove`'s stage 5 | the file loads without it |
| 8. Monitor | every monitor reseeded, as `site remove`'s stage 6 | as there |

Stage 1 is strict: a site that did not take the cut is named and nothing after
it runs. A partial cut is safe to leave, because every peer that dropped the
key no longer talks to the site, and a re-run completes it. Every stage is
planned from live state, as in a plain removal, so a run stopped anywhere
resumes; a remaining site whose mesh file was written by a run that stopped
before its interface took it is handed the file again.

**Patroni's health is required of the members that stay, as in any removal,
with two exceptions.** When the cut-off site leads, its replicas' streaming
state is its to decide: a leader in hostile hands can stop them streaming, and
refusing on that would let it block its own removal. Their lag is shown
instead, and is what `lose writes` weighs. And once the cut has started on any
remaining site, a failover may be under way and the members are read as they
move; the database stage waits for it and its gate is what has to hold, which
is what lets a re-run complete a partial cut.

**The DNS stage deletes the records at names still wanted, too.** A host that
is not reached may be in hostile hands, and every visitor a record sends it is
one whose session cookie it can read; a dead one answers nothing at those names
either way. So rule 5 of *The site's DNS records go with it* is lifted: a
record at a name the configuration without the site still wants is deleted as
long as the other rules hold, which tie it to the cut-off host alone. That name
answers nothing until the `deployment apply` that brings up the replacement creates its
record at the new address.

**Refused before anything changes**, besides `site remove`'s own refusals:

| Refused | Because |
|---|---|
| the remaining healthy etcd voters are not a majority of the voters etcd has now | `member remove` needs a quorum of the current voters, the cut-off one counted; recovery is restoring etcd from a remaining member, outside the toolkit |
| two or more remaining sites with no dial endpoint, and the site the only remaining one with an endpoint | they peer only with a dialled site and relay everything else through it, so cutting it cuts them; give one an endpoint first |
| the only data site | nothing remains to fail over to; recovery is a restore from backup |
| a gateway whose DNS token, read from the secrets file, does not work against the provider | the cut-off host holds the token it was rendered, on its disk if it is dead, so it is revoked at the provider and a new one set with `secrets set` first; a token that fails is most often the revoked one, still in the file, and the DNS stage could not delete the records with it |
| a site declared for the first time whose host does not answer over ssh | whether it is new is read from its host's admission record |
| `--execute` without a terminal or `--yes` | cutting a site off is not undone by running it again |
| `--force`, `--ssh`, `--id` or `--delete-data` | each names a host to change, and `--host-gone` reaches none |

**What it asks at the terminal.** `--execute` asks for each word in turn, and
`--yes` types each:

* `revoked`, when the site holds the gateway. The DNS token can change the
  community's records from anywhere, and the toolkit cannot revoke it: it holds
  the token, not a credential that manages tokens. The token in the secrets
  file is also tried against the provider before anything changes, where a
  provider whose record management is implemented allows it; otherwise the
  report says it was not checked. It is asked first, before the secrets file
  is read, since the new token is set with `secrets set` while it waits: the
  token tried and every secret written back are the file as it is then.
* the site's name, always.
* `lose writes`, when the site leads and no remaining member is a streaming
  `Sync Standby`. The plan names the most current replica and its lag in
  received WAL, since a failover promotes it and any commit the cut-off leader
  acknowledged after its last received WAL is lost. Replication is
  asynchronous unless `cluster.synchronous` is set, so this is the usual case
  for a leader. With a streaming `Sync Standby` nothing is asked: it takes over
  losing nothing. Founder decision.
* `lose objects`, when the site's Garage node held objects nothing else does
  (below).

Patroni is read again immediately before the cut, after the answers: which
site leads, and who would take over from it, decide whether `lose writes` is
asked and what it agrees to, and either can change while the questions wait
at a terminal. When the cut-off site has taken or lost the leader, or the
Sync Standby or the replica a failover would promote is another, the run stops
before anything changes and says to run it again, which reads it afresh.

**A replacement is a new site, declared in the same edit, and the old site is
cut off first.** The site's name keys its WireGuard key and heartbeat token in
the secrets, its etcd and Patroni member names, its Garage zone, its DNS
records and every host's records of it, so a new name keeps the old host and
the new one apart in all of them and in every log. One edit of
`paisans-stack.yaml` declares the new site with the roles it takes over and
pins to it any app pinned to the old one; then `site remove <old> --host-gone
--execute`, then `paisans deployment apply`, which prepares the new host, applies it and
creates its records. Cutting the old site off first means nothing waits on a
site that will not answer, and the old host is out of the mesh before the new
one joins. Founder decision.

* A site the configuration declares whose host holds no claim of this
  deployment is the replacement. The removal neither reaches it, nor claims
  it, nor gates on it. A missing claim alone is not enough: a claim can be
  lost, and an `ssh` section can reach the wrong host, and a site that ran
  taken for new would be left out of the cut while its peers still talk to it.
  So when a remaining site lists its key as a WireGuard peer, or a gateway's
  deployment record lists it, the removal refuses, naming the site and what
  says it ran. `mesh rekey` decides which sites are not brought up yet the
  same way.
* Its secrets are generated first when the secrets file does not hold them,
  by the same code `init` runs, on the workstation, and written encrypted to
  `.sops.yaml`'s recipients once every question is answered. A file that
  changed after it was read is refused rather than overwritten, since the write
  is the whole file and would lose the change.
* The cut is rendered without it. The cut must not wait on a host or a DNS
  record that does not exist yet, and `deployment apply` stops before the mesh is synced while
  a peer's endpoint name does not resolve; the `deployment apply` that brings it up adds
  it as a peer on every site.
* The only gateway or the only apps site is not refused when the
  configuration declares its replacement. Between the cut and that
  `deployment apply` the community's names answer nothing, and the report says so. The
  only data site and the etcd majority are refused: a replacement there is a
  restore, outside the toolkit.

**What the host still holds** is in the closing report, by name, with the `site
evict` line its owner removes all of it with if the host ever runs again:
everything it was rendered, valid until rotated. Its own WireGuard key no
longer works anywhere. The DNS token goes first, since it can point the
community's names anywhere. The rest is `secrets rotate --all` once the mesh is
rekeyed, which takes what works from the internet first (the OIDC client
secrets, signing keys and the apps' S3 keys), then the Postgres, Garage and
heartbeat secrets, which only the mesh reaches once it is closed (*`secrets
rotate` replaces a secret where it is checked and where it is used*). A copy of
the database and of the objects cannot be taken back.

**An operator who could decrypt the secrets file holds every site's secrets,**
including every other site's WireGuard private key, and WireGuard accepts a
handshake from any address that proves a key. So their age key comes out of
`.sops.yaml` and the file is re-encrypted under a new data key, and every
remaining site's key is rotated with `mesh rekey --all` (*Rekeying the mesh*)
before the replacement joins. The guide has the order.

### Rekeying the mesh: `mesh rekey`

`paisans mesh rekey <site>... | --all` replaces sites' WireGuard keys.
How to run it is under *`paisans mesh rekey`* in
[docs/commands.md](docs/commands.md).

**One site at a time.** A site whose key changes loses its tunnels until every
peer knows the new key, and two sites down at once can cost etcd its quorum.
Each site goes through four stages, each ending at a gate:

| Stage | What runs | Gate |
|---|---|---|
| 1. New key | a private key generated on the workstation, as `init` generates one, and written to the secrets file as `sites.<site>.wireguard_next_private_key`, beside the current | the file decrypts and holds it |
| 2. Swap | every peer's mesh file with the new public key in place of the old, then the site's own with the new private key, each by scoped `deployment apply`, synced through the helper; the peers at once and the site last | every peer that lists the site has a handshake with it under the new key |
| 3. Health | nothing | etcd healthy at every voter; exactly one Patroni member leading and `running`, and every other data site a member and `streaming` |
| 4. Promote | the next key becomes `wireguard_private_key`, and the old one is deleted | the file decrypts and holds only the new key |

**A stopped run resumes.** A next key in the secrets file says the site is mid
rekey, and which public key its host runs, read through the WireGuard helper's
`show public-key`, says whether stage 2 is done. A peer whose file was written
but whose interface was not handed it is synced again. A site left mid rekey
goes first in the next run, whether it is named or not, and its swap is
finished before the cluster's health is read: a swap that stopped part way
leaves its peers disagreeing about its key, which is often exactly why the
cluster is unhealthy, so refusing on that health would leave the run unable to
repair what it broke. The health check refuses only before a site's rekey
starts. An `--all` run records the sites it has left in the secrets file
(`mesh_rekey_remaining`), each taken off as its key is promoted, so a stopped
`--all` run resumes with the sites it had left and does not rekey again a site
it finished. Until stage 4, render still reads `wireguard_private_key`, the
old key, so `deployment apply`, with or without `--site`, refuses while any site has a
next key in the secrets file and names the `mesh rekey` that finishes it: an
`deployment apply` then would hand the old key back to every peer and cut the site's
tunnels, and a refusal costs nothing but the rekey's resume.

**A relay's swap pauses Patroni.** A site with no dial endpoint routes
everything it has no peer for through the first site that has one (*`site add`:
a second data site, and the one hard problem*), so two undialled sites reach
each other only through that relay. Swapping the relay's key cuts them apart
for the moment it takes, and the relay itself with them. `validate` keeps two
voters dialled, but etcd can still be left without a leader while it re-forms
when several voters have no endpoint, and a Patroni leader that cannot renew
its key then demotes itself and another member can be promoted, for a partition
that heals in seconds. So around a relay's swap the cluster is put in
maintenance mode with `patronictl pause --wait`, which returns once every
member has seen it (Patroni v4.1.0, `patroni/ctl.py`); paused, Patroni keeps
the roles it has and still reports them. The pause is read back from
`patronictl show-config`, since patronictl reports a failed request without
failing. etcd re-forms on its own once the tunnels are back, and the health
gate waits for it, bounded; only then does `patronictl resume --wait` run. A
run that stops in between leaves Patroni paused, on purpose, since resuming it
before etcd has its voters back is the failover the pause exists to prevent,
and the run that resumes the swap resumes Patroni after it. The plan says which
sites relay through the site and that the cluster pauses for its swap. A
deployment of one dialled VM and homes with no endpoint can therefore rekey
every site, its relay included, which the runbook for an operator who leaves
depends on.

**Refused before anything changes:** an unhealthy etcd or Patroni, since every
site's swap takes its tunnels down for a moment; a Patroni paused by anything
but this rekey's own relay swap, since that is someone else's maintenance; and
a site that does not answer over ssh, since every site is a peer of some site
rekeyed.

### Preflight

Half the design's assumptions are mechanically checkable, and every unchecked one
will eventually be violated:

* SSH reachable; privilege escalation works
* Docker present and recent enough
* **Architecture matches the existing data sites**: a data directory is not
  portable between architectures. The OS release need not match, since
  Postgres's collation library is the container image's glibc, not the host's
* Kernel WireGuard available; `/dev/watchdog` present
* Ports free: 51820, 2379/2380, 5000
* The mesh subnet does not collide with anything the host already routes
* Clock synchronised — etcd and Patroni both depend on it
* Storage is local, not network-attached, with room
* **Round-trip time measured to every existing site**, and etcd's heartbeat and
  election timeout derived from it rather than guessed

That last one turns a documented manual step into an automatic one, which is the
difference between a tuning rule people follow and one they read once.

#### What `paisans preflight` checks, and how

`paisans preflight --site <new>` runs the checks on their own and changes
nothing; `site add` runs the same checks as its first stage. Each finding is
`✓`, `!` or `✗` (`ok`, `WARN` or `FAIL` when piped), and any refusal stops the
join. A check that
could not be made (a host that did not answer, output that did not parse) is
a refusal, not a skip: preflight exists so that every assumption was looked
at, and "could not look" is not "looked and it was fine".

| Check | Where | How | Refused when |
|---|---|---|---|
| SSH | every site | `echo paisans-answers`, as the site's ssh user | the host does not answer, ssh cannot log in, or the answer lacks `paisans-answers`, so the login ran no command |
| Clock | every site | `timedatectl show -p NTPSynchronized --value` | not `yes` |
| Host | new site | the host check (see *The host check*), as the deployment's user | something foreign holds a claim, or its Docker is a snap or older than the toolkit needs |
| Prepared | new site | `site prepare`'s own plan | it has any step left |
| Admission | new site | `/etc/paisans/admitted.<token>.json`, read as the deployment's user | the host has not admitted this deployment, or its record does not cover the site: its user, SSH and WireGuard ports, mesh subnet, forwarding, the grants its roles need or, for `managed_by: external`, the web server. The finding names the `paisans site request` line the host's owner needs. Another deployment holding this one's token, root, interface, listen port, a mesh subnet overlapping this one's, or the gateway or data role this one claims is `site admit`'s refusal, in the registry the owner's side keeps |
| Platform | new data site, against each existing data site | `dpkg --print-architecture` | the architecture differs |
| WireGuard | new site | `/sys/module/wireguard`, else `modprobe -n wireguard` | neither |
| Watchdog | new data site | `/dev/watchdog` exists | it does not |
| Ports | new site | the host check's `ss -Hltnue` against its claims | anything listens where the site will bind: the port it is dialled on, UDP, on a site with a `public_address` or an `endpoint`; 2379 or 2380 for an etcd member; 5432, 8008 or 8009 for a data site; the cluster port and 8404 where the site runs HAProxy; 3900 to 3903 for a Garage site; 80 and 443 for a gateway or a monitor in `managed_by: paisans`; `listen` for a monitor in `managed_by: external`; each app's published port |
| Mesh | new site | `ip -j route show table all`, `ip -j addr`, `docker network inspect`, the user's `~/.config/docker/daemon.json` | a route, an address, a Docker network or a Docker address pool overlaps `mesh.subnet`, other than a route or address on this deployment's own interface (see *Two deployments on one host never share a mesh*) |
| Routes | new site | `ip -j route` | a route overlaps `mesh.subnet`, other than one through `psns-<token>` |
| Disk | new data site | the leader's `sum(pg_database_size(...))`, through its Patroni container's `psql`; `df` on the deepest existing directory towards `/srv/paisans/<token>/infra/postgres` | free space is under that plus 2 GiB |
| Storage | new site | `docker info --format '{{.DockerRootDir}}'`, then `findmnt -no FSTYPE,SOURCE --target` on Docker's root and on the deployment's root, `/srv/paisans/<token>` (or the deepest directory towards it that exists) | either is a network filesystem (`nfs`, `nfs4`, `cifs`, `smb3`, `glusterfs`, `ceph`, `fuse.sshfs` and the others preflight lists); a type that is neither that nor a local block filesystem (`ext4`, `xfs`, `btrfs`, `zfs`, `f2fs`) warns |
| Round trip | new site to every other site | three TCP connects to the site's `public_address` on its ssh port, timed with bash's `/dev/tcp` and `$EPOCHREALTIME`; the median is used | another site has no `public_address` to measure to, or `etcd.election_timeout_ms` is under five round trips |

Three of these need a word.

**The round trip warns as well as refusing.** It warns when `etcd.heartbeat_ms`
is under one round trip, and when the election timeout is under ten: etcd's own
tuning guide (v3.5 docs, *Tuning*) puts the heartbeat at "around 0.5-1.5x the
round-trip time" and says election timeouts "must be at least 10 times the
round-trip time". The refusal sits at five, the spec's number, so a deployment
that tuned close to etcd's guidance is warned rather than stopped. The list
above asks for the timings to be derived from the measurement; the site add
spec (`docs/specs/2026-10-07-site-add.md`) checks them instead, because a
derived value would change the rendered etcd flags on every member, and
changing those on a running cluster is an operation of its own.

**Docker's presence is covered indirectly**: `site admit` installs or refuses
it, and the host check, which the `Host` row runs, reads the deployment's own
daemon.

**Storage is judged by filesystem type, and a network one is refused.**
Postgres acknowledges a commit, and etcd agrees a write, once fsync returns.
On NFS, SMB, GlusterFS, CephFS or a FUSE mount of a remote, what fsync
promises depends on the server, its export options and the client's cache,
and a network blip stalls it for as long as the blip lasts, which for etcd is
a missed heartbeat and an election for nothing. Docker's root is checked as
well as the deployment's root, because it holds every container's writable layer and an
operator may have moved it. A type in neither list warns rather than refuses:
it is not known to be wrong, and a list of every filesystem is not one this
toolkit can keep. The check cannot see through a block device: an iSCSI LUN
or a Ceph RBD image carries an ordinary `ext4` or `xfs` and passes, so a
network disk presented as a local one is still the operator's to know about.

The ports check reads listeners, not owners. After the mesh stage the new
site's own mesh interface holds the port it is dialled on, so preflight is the gate for a join that
has not begun, and a join resumed past stage 1 must not be sent back through
it.

### `failover test`: a switchover on purpose

A cluster that has never failed over has an untested failover, and the first
test should not be an outage. `paisans failover test` moves the Patroni
primary to another data site and back, and it is gated like `site add`: it
checks before it moves anything, every switch ends at a gate, and a gate that
does not pass stops it where it is rather than switching back blind.

1. **Checks.** Every member of `cluster.sites` is in the cluster, the leader
   is `running` and every other member `streaming` with lag zero; a Sync
   Standby exists when `cluster.synchronous` is true; every app stack is
   healthy on every site it runs on, by the same judgement as `deployment apply`'s health
   gate; and every app answers through the gateway, an HTTPS request from the
   workstation to its hostname answering under 500 (a redirect to a login page
   counts). Pocket ID is asked for `/.well-known/openid-configuration`, the
   document every client of it fetches first. A Pocket ID on more than one
   apps site must have exactly one active instance (see *Pocket ID runs on
   every apps site, and one of them is active*). Lag is looked at up to three
   times, because Patroni compares a replica with the leader's last reported
   position and a busy moment shows bytes that are gone on the next look.
2. **Switch.** In the leader's Patroni container, `patronictl -c
   /home/postgres/postgres.yml switchover --leader <current> --candidate
   <other> --force`. The candidate is the Sync Standby where there is one,
   since that is who Patroni would promote on a real failure. The flags are
   Patroni v4.1.0's (`patroni/ctl.py`): `--leader` is checked against the
   live leader, so a plan made against a leader that has since changed fails
   rather than switching the wrong way, and `--force` with no `--scheduled`
   means now. The config path is the one Spilo runs Patroni with
   (`postgres-appliance/runit/patroni/run`, at every Spilo tag the toolkit
   pins), a link to the `/run/postgres.yml` Spilo writes at start.
3. **Gate.** Polled for up to three minutes: the candidate leads and the old
   leader is `streaming`. The gate reads `/cluster` rather than trusting
   patronictl's exit status, because when Patroni refuses a switchover
   patronictl prints `Switchover failed` and exits normally.
4. **Restart the database apps.** Every app with `cluster` placement and a
   kind that uses Postgres is restarted on **every** apps site (`docker
   compose -f /srv/paisans/<token>/<app>/compose.yaml restart`), not only the old primary's:
   a leader change closes every client's connection to the old primary
   wherever the client runs, and an app whose workers never reconnect, as
   Mbin's do not (see "`deployment apply` restarts the apps whose database path
   changed"), answers 500 until restarted while its container stays
   `healthy`. A pinned app has its own Postgres and is left alone. A failed
   restart stops the test there.
5. **Gate.** Polled for up to three minutes: the cluster still as above, and
   every app healthy and answering again.
6. **Switch back**, the same way, through the same gates and restarts.

It is a dry run by default, which prints the checks, both commands and the
expected interruption: writes fail from the old primary's demotion until each
site's HAProxy marks the new one up, which with the rendered `inter 3s` and
`rise 2` is several seconds after promotion, twice. Connections to the old
primary are closed when it is marked down (`on-marked-down
shutdown-sessions`), and reads through HAProxy pause too, since it routes only
to the primary. The plan lists each app restart after each switch, a further
few seconds per app. The apps are restarted because some never reconnect on
their own, and their healthchecks do not notice.

### `paisans doctor` reports what is stuck and how to recover

An outage is the worst time to remember which container to look at, which etcd
key holds the answer, or which Patroni log line means what.
`paisans doctor` reaches every declared site, or only those named with
`--site` (repeatable or comma-separated), reads its state, and prints one line per finding,
`ok`, `WARN` or `FAIL`, with how to recover under each `FAIL`. A check it could
not make is a `WARN` saying it was skipped and why, never an `ok`: it found
nothing right, and the closing line counts it apart from the checks that
passed. It exits 1 when any finding is `FAIL`, so a script or a monitor can
tell a healthy deployment from one that needs a human.

It **changes no host**. Every command it sends is a read: a list, a status, a
key, a log tail. The recoveries it describes are printed for a human to run,
because doctor is run in the middle of an outage and the one recovery that
touches member data, a forced failover, loses commits. Which commits a
community keeps is a human's decision. Its changes are to the local
`paisans-stack.yaml`, and only with `--execute`: it records the `ssh.user` of a
site it logs in to as the deployment's own user, as any run with `--execute`
does, and before it loads the file, it removes
every `config:` key that only repeats a value gating makes the template write
(Eg: `MBIN_USE_FEDERATION_ALLOW_LIST: true` on a gated Mbin), which `validate`
otherwise refuses as `config-key-already-rendered`. The file then says the
same thing with one fewer place to say it. A key with any other value
contradicts the gate, and changing it would change policy, so it is left for
the operator. A file the edit leaves unloadable is restored.

A site that does not answer is itself a finding, never a crash: every other
check runs with the sites that did. Whether it is pending, its host not yet
having admitted the deployment, or down, having joined and gone quiet, is
read from the deployment, never from the site, as `deployment apply` reads it (see *A
deployment with sites missing*), and either is a `WARN`, since the deployment
runs on the sites it has; a gateway is a `FAIL`, since `deployment apply` refuses to run
without one. Nothing more is asked of that site, except the self-check,
which asks from outside, since
a read over a connection that already failed only fails again, and the checks
not made because of it are named once, on its line (`Not checked because of
this: mesh, containers, clocks on home-b.`), rather than each failing in turn,
because a cascade of errors buries the one line that matters. The closing
line counts them as skipped. Each ssh attempt gives up after ten seconds, as
every command's does (see *How the operator's ssh uses them*). Like
`preflight` it takes no `--ssh`, since one override cannot name several hosts,
and it runs its commands as each site's ssh user.

The checks run at once, as many as are ready, and each prints whole, naming
its site, as it ends, so an operator in an outage reads the first answer while
the slow ones still run, and a deployment of several sites is read in the time
of its slowest site rather than the sum of them. A check starts only once the
checks it needs have answered: everything on a site needs its reach and then
its admission record, except the self-check, which asks from outside and needs
no ssh; every per-site check that runs `docker` there (Patroni, containers,
settings, leftovers) needs the site's own Docker to answer; the checks across
sites try each admitted member in turn; etcd needs a member that answered, and
the containers and Pocket ID checks wait for Patroni's, whose verdict their
advice uses. So a cause always prints before
what it causes. A site runs at most eight commands at once, under the ten
sessions sshd allows on the one connection Paisans shares per destination
(`MaxSessions`, sshd_config(5)); sites are not bounded against each other,
since each is a machine of its own. When any check failed, the failures are
listed once more at the end, under `failed, deepest first:`, in the order
below, because in an interleaved run the first failure on screen is the first
to end, not the first to fix.

A pending site skips the rest of its checks, and a Docker daemon that does not
answer skips the per-site checks that run `docker` there (Patroni,
containers, settings, leftovers). The checks that read across
sites (etcd, Patroni's `/sync` and logs, Pocket ID's instances) ask only sites
whose admission passed and that the run's classification placed neither pending
nor down; each site they did not ask is listed with `not asked: its admission
did not pass (see admission)` or `not asked: the run's classification found it
pending or down, as apply would`. The classification is made once, the first
time a site does not answer or its record does not cover it. A site that
answers and is covered, but whose every route into the mesh is through a
pending site, keeps its own checks; only the checks across sites leave it out.
Warnings alone, a pending or down data site among them, leave the exit status
at 0.

The checks, from the bottom of the stack up, so that the first `FAIL` of the
closing list is usually the cause of those after it:

| Check | How | FAIL when |
|---|---|---|
| Reach | `true` over ssh, on every site asked | the site does not answer and is neither pending nor down, or it is the gateway. A pending or down site is a `WARN`, saying what the deployment is without while it is gone: the gateway, an etcd vote, a Patroni member, a Garage node, its copy of each clustered app, the apps pinned to it; a pending site with the `paisans site request` line, a down one with the `site remove --host-gone` line |
| Admission | the admission record, `/etc/paisans/admitted.<token>.json`, read as the site's ssh user | the record cannot be read, is missing, differs from `paisans-stack.yaml` or lacks a grant the site's roles need, and the deployment has joined the site or the run cannot tell whether it has. When it has not, the site is pending: a `WARN` (a `FAIL` on the gateway) with the `site request` line, and nothing more is asked of the site |
| User | `id -u`, `id -un`, `id -nG` and `id -G` | the user has uid 0, is in a group `site admit` refuses (`admitrules.PrivilegedGroups`), or is in a group of GID 0 under any name. Groups doctor cannot read are a `WARN` |
| Docker | `docker version` and `docker info`'s security options, against the user's own socket | the daemon does not answer or is not rootless. When it does not answer, nothing that runs `docker` on the site is checked. An answer doctor cannot read is a `WARN` |
| Docker engine | the version `docker version` read | older than 29.5 |
| Docker source mark | the highest `src_valid_mark` under `/proc/sys/net/ipv4/conf` in RootlessKit's network namespace, read with the Docker check by entering the namespace as the user | it is 1, so every new connection to an app waits a second and the app sees the bridge's address: the recovery is `paisans deployment apply --execute`. See *`deployment apply` keeps the rootless namespace's `src_valid_mark` at 0*. A value doctor cannot read is a `WARN` |
| Linger | `loginctl show-user <user> --property=Linger --value`, and whether the drop-in `/etc/systemd/system/user@<uid>.service.d/paisans-mesh.conf` exists | linger is off: the user's Docker stops when the last login ends and does not start at boot. Or the drop-in is missing, so the user's manager is not ordered after the mesh. An answer doctor cannot read, the mesh-order line included, is a `WARN` |
| Sudo rights | `LC_ALL=C sudo -n -l`, or `/usr/bin/sudo.ws -n -l` where sudo is sudo-rs | sudo lets the user run anything but the WireGuard helper as root, carries a Defaults entry or runas line beyond admit's one rule, or does not let it run the helper. An answer doctor cannot read, such as a translated one, is a `WARN` |
| Swap | `/proc/meminfo`, `/proc/swaps` (zram devices among them), `lsblk`, `/etc/fstab`, the zram-generator configuration and `/swapfile`, as `site admit` reads them, read as the deployment's user | never; no swap on beyond zram is a `WARN`, saying whether the host has swap that is not on, which its owner turns on, or none, for which `site admit` offers a swapfile. A read doctor cannot make, or an answer missing its RAM or swap line, is a `WARN` and skips the memory check |
| Memory | the same read, against the floor of what `paisans-stack.yaml` places on the site (*A host's memory is measured against its roles*) | never; RAM, with swap that is on and not zram counting up to a quarter of the floor, under the floor is a `WARN`, naming each part of it. Each deployment on a host is checked against its own floor |
| Mesh | `ip -j link show dev psns-<token>`, then the overlap test of *Two deployments on one host never share a mesh*: `ip -j route show table all`, `ip -j addr`, `docker network inspect` and the user's `~/.config/docker/daemon.json`, on every site that answered | the interface is missing or down, or anything outside it overlaps `mesh.subnet`, named with the subnet. The advice is that the subnet is fixed once deployed and the other network moves |
| etcd quorum | `etcdctl endpoint health -w json` in the etcd container of the first member that answers, naming every member of `etcd.members` by its mesh address | fewer than half the members plus one are healthy. One member down with quorum intact is a `WARN` |
| etcd version | `curl` on the host to the member's `http://<mesh address>:2379/version`; skipped, and said so, where the host has no `curl` | the cluster version is `3.0.0` under a 3.5 server |
| Patroni | `/cluster` from every database site's Patroni, through `curl` in its container as `deployment apply` asks it, and Patroni's `/sync` key from etcd | there is no running leader. A replica that is not `streaming`, or more than 16 MiB behind, is a `WARN`, and so is a leader with no Sync Standby while `cluster.synchronous` is true |
| Containers | `docker ps -a --filter label=community.paisans.deployment=<id>` on every site, then `docker inspect` and the end of `docker logs` for each container not running or unhealthy | any container is not running, or runs while its healthcheck reports `unhealthy`, shown with the last lines its healthchecks printed. A healthcheck still `starting` is a `WARN`, since it has not failed but the container is not yet known to work; a container with no healthcheck is judged by running alone |
| Settings | for each app setting `gated`, the read its kind records, run in the app's running container on each site it runs on: Mbin's environment and `MBIN_LOCKED_SETTINGS`, WriteFreely's `writefreely settings list` | a setting the gate leaves to the app (private instance mode, the allow list, authorized fetch, WriteFreely's private mode) is off or not locked. The advice is `paisans deployment apply --site <site>`, which renders the file that writes and locks it and recreates the stack, or a newer image when the app does not honour the lock. See *The settings gating needs are checked on both sides* |
| Pocket ID | the same question `deployment apply` asks after acting on a Pocket ID stack on more than one site, asked once | no site, or more than one, has an active instance |
| Clocks | `date +%s.%N` on each site, against the workstation's clock taken on either side of the call, less half the round trip | never; more than 1 s off is a `WARN` |
| Leftovers | the host check's inventory (*The host check: what is already on a host decides how much is touched*) on every site that answered, and the manifest; read as the deployment's user, and skipped when the inventory cannot be read. A file is judged against the manifest's own view, which names the sites that were pending when it was rendered, so a file rendered without a pending site is not a leftover | never. Anything of this deployment's the site no longer renders (a compose project by label and name, a manifest entry marked left over) is a `WARN`, with the note that `deployment apply` does not remove it. On the gateway, each `*.caddy` in `/srv/paisans/<token>/caddy.d` is listed as `info`: foreign, and served by this deployment's Caddy. That directory is the only way anything foreign is served by it, since the Caddyfile is this deployment's and Caddy runs in the host's network namespace |
| Self-check | from this workstation, `/.well-known/paisans/<token>` through the public address of each site that runs Paisans' Caddy (the gateway, and a monitor with `web_server.managed_by: paisans`), on 443 and on 80, with the check `deployment apply` runs at the end of the gateway's step. It asks once and never waits for a certificate, and needs no ssh | the site answers as something else, a 443 answering in plain HTTP or another protocol than TLS among them, or does not answer on 443 or 80, naming the port and URL: something else on the host holds the port, which the deployment's user cannot see. A port that gives no answer says how: a refused connection points at the site's Caddy and the forwarding to it, a timeout at the router or firewall on the way. A site with no `public_address` or no hostname of its own is a `WARN`, not asked from outside; so is a pending site, which is not asked since `deployment apply` has not deployed its Caddy, and a down site's failure (a `FAIL` on the gateway), which points at its reach |
| Caddy certificates | the caddy container's log on every site that runs Paisans' Caddy, `docker compose logs caddy` from the container's start or the last hour, whichever is later, kept to Caddy's `tls.obtain` lines; for each hostname the site serves, the newest outcome | the newest outcome is a failure. Hostnames failing for one cause are one finding naming them all: the provider refusing the token from this server (Cloudflare's `9109`, with the address it judged), its lockout (`10502`), the certificate authority's rate limit with when to retry, or the error's own last clause. It reads no secret and calls no provider, so it runs for every provider |
| DNS token | from each Caddy site, with the `ACME_DNS_TOKEN` in its `caddy.env`, read there as the site's ssh user and never sent anywhere; and from the workstation, with `external.acme_dns_token` from `--secrets` (`secrets.enc.yaml` beside the configuration by default), opened with the age key as `deployment apply` opens it. Each asks the provider for the zone holding `community.domain`, then verifies the token | the provider refuses the token from that place, does not answer, sees no zone holding the domain, or reports the token not `active`. Secrets that cannot be opened, Eg: no age key on this workstation, skip the workstation's check and the records with the reason, and every other check runs |
| DNS records | the plan `dns init` builds, which reads and never writes, from the workstation once its token check passed | a hostname of the deployment holds a record pointing somewhere else (a visitor is sent there; `paisans dns init --execute` updates a record it made for this deployment, and any other is deleted at the provider by hand, since `dns prune` keeps one whose name is still wanted), or holds none (`paisans dns init --execute` creates it). A record waiting for `gateway move` is `info`, and a weak advisory record a `WARN`, as `dns init` shows them |

The provider judges the address a call comes from (Cloudflare's Client IP
Address Filtering), so the token is checked from every place that uses it: a
token allowed from the workstation and not the gateway passes `dns init` and
fails every certificate Caddy asks for. The zone lookup comes first because
the filter applies to it and not to verify, so a token refused from a place is
refused by its first call there and nothing more is asked. Calls with a refused
token are also what keep Cloudflare's lockout, `10502`, going (observed on
staging, where it held while Caddy retried and ended once the filter admitted
the gateway), so while any Caddy log shows a `10502` in the last five minutes
no token check is made from anywhere, and each says so: fix the token's
filter, and Caddy's next scheduled attempt succeeds with nothing restarted.
For a provider doctor has no check for, the token and records checks are
skipped and say so; Caddy's log is what tells whether it gets certificates.

The health check names each member rather than using `--cluster`, because
`--cluster` first asks the cluster for its member list (etcd v3.5.16,
`etcdctl/ctlv3/command/ep_command.go`, `endpointsFromCluster`), and that call
fails without quorum, which is when the check matters. Without quorum Patroni
cannot renew its leader key and the primary demotes itself (Patroni v4.1.0,
`patroni/ha.py`, `_handle_dcs_error`), so the advice is to start the members
that are not healthy. `/sync` is read with `--consistency=s`, from the
answering member's own copy, for the same reason.

The version check catches a founding member that never ran. A new etcd
cluster decides its version only from every member's version
(`decideClusterVersion` in `server/etcdserver/cluster_util.go`, etcd v3.5.16,
returns nothing while any is unknown), and until then reports the minimum,
`3.0.0`. Patroni reads that and falls back to the `/v3alpha` API
(`patroni/dcs/etcd3.py`), which etcd 3.5 does not serve, so it waits on etcd
forever and no primary appears. The advice is to apply the members not
running; see *A new deployment is applied witness first*.

A container that failed with `cannot assign requested address` started before
the mesh interface at boot: it publishes its port on the mesh address, which
exists only once `wg-quick@psns-<token>` is up, and Docker does not retry a
container whose network setup failed. The advice is `docker start <container>`
on the site as the deployment's user, or `paisans deployment apply --site <site>
--recreate <stack> --execute`, and, if it happens at every boot, the Linger
row, which says whether the user's manager is ordered after the mesh; see
*The user's Docker starts after the mesh interface*. A container Docker
keeps restarting while its log says it
cannot reach the database points at the Patroni finding when there is no
primary: it comes back on its own once there is one. When there is a primary,
it is not waiting on Patroni, and the advice is to check the site's HAProxy and
to recreate the stack with `paisans deployment apply --site <site> --only <stack>
--recreate <stack> --execute`. A log saying `network is unreachable` gets the
same recreate whatever Patroni says: the container's own network has no route
to the mesh address, which a container whose network setup failed at boot and
was then started again was left with on a staging host, and restarting it does
not rebuild that network. Any other shows its last three log lines.

The clock threshold is etcd's: its peer prober logs "prober found high clock
drift" past one second (etcd v3.5.16,
`server/etcdserver/api/rafthttp/probing_status.go`), and etcd's leases and
Patroni's leader key expire by time. The advice is to check time sync with
`timedatectl`, and that a suspended VM's clock stops while it is paused. If
every site is off by the same amount, the workstation is the one to check.

#### A replica that will not promote

The case doctor was written for: two data sites with `synchronous: true` and
`synchronous_strict: false`. `home-b` leads with `home-a` as its Sync Standby.
`home-a` goes down; `home-b` goes on committing alone, which is what
`synchronous_strict: false` permits, and takes `home-a` out of `/sync`. Then
`home-b` goes down too. `home-a` comes back, stays a replica, and logs
"following a different leader because i am not the healthiest node".

That is Patroni refusing to lose data. While synchronous mode is active, only
a member `/sync` names may race for the leader lock (Patroni v4.1.0,
`patroni/ha.py`, `is_healthiest_node`: `if not self.is_quorum_commit_mode()
and not self.cluster.sync.matches(self.state_handler.name, True): return
False`), because `home-a` may be missing every commit `home-b` made after
`home-a` stopped being its standby.

doctor reports it when `/sync` names a leader that is not in the cluster and
none of the replicas left, with the replica's own log lines matching
`healthiest`, `lag`, `timeline` or `lock` as evidence, and two recoveries in
this order:

1. **Start `home-b`.** When it returns it takes the leader lock again and
   `home-a` catches up from it. No data is lost. This is the recovery to
   choose whenever `home-b` can come back.
2. **Only if `home-b` is lost permanently**, promote `home-a` by hand, on
   `home-a`:

   ```
   docker compose -f /srv/paisans/<token>/infra/compose.yaml exec patroni patronictl -c /home/postgres/postgres.yml failover --candidate home-a --force
   ```

   Patroni promotes the candidate a failover names even when `/sync` does not
   name it (`manual_failover_process_no_leader` returns true for it), and
   `--force` skips patronictl's "Are you sure you want to failover to the
   asynchronous node" (`patroni/ctl.py`). It is printed with three warnings:
   every commit `home-b` made after `home-a` stopped being its synchronous
   standby is lost (sign ins, posts, the metadata of uploads); if `home-b`
   returns later its data has diverged, and Patroni must rewind or
   reinitialise it, which permanently discards those commits; and it changes
   member data, so it is a human's decision and never something to run
   unattended.

A cluster with no leader whose replica `/sync` does name, or with no `/sync`
at all, gets the general diagnosis instead: a replica also holds back when it
is further behind than `maximum_lag_on_failover` (`patroni/ha.py`,
`is_lagging`), when its watchdog is unusable, or when etcd has no quorum. The
advice is to start the members missing from the cluster, and any forced
failover it mentions carries the same warnings.

## Moving the gateway

A single site runs `roles: [data, apps, gateway]` — the same machine serving the
public internet and holding the database. That is correct for one site, and total
failure of that site is the expected behaviour of having one site.

Once a **second data site** exists, the gateway should move off both of them.

The reason is that otherwise the failover does not reach the public path. If
home-a is the gateway and home-a dies, the database fails over in about a minute
— but public DNS still points at home-a, and nothing is reachable until a record
changes and propagates. Automatic database failover sitting behind a manual DNS
change is not automatic.

The gateway's job is to be **the address that never moves**, and a residential
connection cannot be that.

### Make it an overlap, not a cutover

The move should never be "stop here, start there."

```
1. The new gateway comes up, obtains certificates, and serves,
   while the old one is still serving
2. Verify the new gateway answers correctly for every hostname
3. Add the new gateway's address records beside the old one's,
   then delete the old one's
4. Wait out the old TTL; the old gateway keeps serving stragglers throughout
5. Stop the old gateway
```

No gap, and reversible at any point before step 5: delete the new records and
the old gateway is still running and still published. That is the difference
between an easy migration and a nervous one.

Step 3 publishes both gateways at once rather than replacing one record with
another. While both addresses are published every visitor reaches a gateway
that serves, whichever address their resolver hands them, and the old one's
records go only once the new one has been verified (Founder decision).

This works because the new gateway can reach the apps over the mesh, which was
established when its site joined. The ordering already holds.

`paisans gateway move` performs steps 2 to 5 as six gated stages, each planned
from live state and each ending at a gate that stops the run with its
evidence, so a run after a fix resumes at the first stage with anything left
to do. Like every command that changes a deployment it is a dry run until
`--execute`.

| # | Stage | Does | Gate |
|---|---|---|---|
| 1 | record | merges the old gateway's deployment record into the new one's | the new gateway's record lists every name the old one's lists |
| 2 | serve | nothing | the new gateway answers for every hostname |
| 3 | dns add | creates the new gateway's address records beside the old one's | every one is present, unproxied |
| 4 | dns remove | checks the new gateway again, then deletes the old one's records | no hostname the gateway serves points at the old gateway |
| 5 | wait | sleeps out the longest TTL of the deleted records | that time has passed |
| 6 | stop | checks the new gateway again, then stops and removes the old Caddy; on a site that keeps the monitor, removes the record and the marker and leaves Caddy running | the old site holds nothing of a gateway's; on a site that keeps the monitor, neither the record nor the marker |
| 7 | hand off | only on a site that keeps the monitor: applies its `infra` stack, as `paisans deployment apply --site <old> --only infra --execute` | the old site's Caddyfile names no hostname the gateway serves |

**Answering** means: every hostname the gateway serves, each app's hostnames
and media hostnames but never a monitor's, requested over HTTPS from the
operator's machine **through the new gateway's own public address** (and
`public_address6` when declared), whatever the name resolves to. An app's own
hostname is asked at its kind's health route, any other at `/`. It passes with
a certificate valid for the name and a status below 500, because that is what
the gateway alone decides: Caddy answers 502 or 503 when it cannot reach the
app, and a redirect such as the visibility gate's sign in is the app
answering. The check runs at stage 2's gate, and again on the same run before
anything is deleted and before anything is stopped, so neither ever acts on a
check from an earlier run.

**What is deleted** is narrow on purpose (Founder decision): an A or AAAA
record carrying this deployment's `dns init` comment, not proxied, at a
hostname the gateway serves, pointing at the old gateway's public address and
not the new one's, and only once every record the new gateway needs is
present. A record pointing at the old gateway that fails any of that, such as
one made by hand, is listed with why, nothing is deleted while it stands, and
the old gateway is not stopped, since that name would still send visitors to
it. Records at every other name are `dns prune`'s and `site remove`'s, under
their own rules.

**The wait** is the longest TTL of the deleted records, from the time of the
last delete, which stage 4 writes on the old gateway's host
(`/srv/paisans/<token>/.paisans/gateway-move.json`) so that a run stopped during the
wait resumes it. Every record `dns init` makes carries the provider's automatic
TTL, 300 seconds at Cloudflare
([Cloudflare: Time to Live](https://developers.cloudflare.com/dns/manage-dns-records/reference/ttl/)),
so the records are already short lived and the wait is minutes. A record made
by hand with a long TTL is not the move's to delete, so it stops the move
instead of lengthening it.

**The old gateway is found, never declared.** `paisans-stack.yaml` names one
gateway, the new one. The old one is the site without the role whose host
still has this deployment's gateway Caddy, its deployment record or the move's
marker, or whose public address this deployment's records at the gateway's
hostnames still point at. On a site whose roles run Caddy for a monitor, its
Caddy is no evidence; a Caddyfile that still names a hostname the gateway
serves is. The configuration declares the end state, and what
is still running is a fact about the hosts, so it is read from them; a site
that cannot be asked stops the move rather than reading as no gateway. The
move ends by removing each of those, so a finished move finds nothing and says
so. Two sites that both still hold a gateway are refused: one move at a time.

**A site that keeps the monitor** is handed off rather than stopped: its
Caddy serves the monitor, so it runs on. Until the hand-off, every apply of
that site, with or without `--site`, renders it as the gateway it still is,
because its host still holds the deployment record or the move's marker:
its Caddyfile keeps every gateway route beside the monitor's, the gate's
snippets and the host owner's sites. Stage 6 removes the record and the
marker, and stage 7 applies the site's `infra` stack, which now renders the
monitor it is, so its Caddy reloads with the monitor's routes alone. Before
the first stage, an executed move tries that apply as a dry run, so a
refusal it would meet at stage 7, after DNS has moved, stops the move at the
start instead; `--keep-images` and `--min-free` are passed to it. A run that
stops between the two finds the site again by the gateway routes its
Caddyfile still names, skips every stage before the hand-off, and hands it
off; if the hand-off itself fails, `paisans deployment apply --site <old> --only infra
--execute` finishes it directly. The host owner's sites in
`/srv/paisans/<token>/caddy.d` stop stage 6 as they stop a plain move, since a monitor's
Caddy does not serve them. `paisans deployment apply` without `--site` asks such a site
too, so it ends with the move.

**The move needs the provider's API.** It adds and deletes through record
management (see *`dns init` creates the records a deployment needs*), so a
provider without it is refused and the steps above are done by hand.

### DNS-01 is what makes step 1 possible

With HTTP-01, a server can only obtain a certificate for a name that already
points at it — so the new gateway cannot hold valid certificates until DNS moves,
and DNS should not move to a server without them.

**DNS-01 does not require inbound reachability.** The new gateway proves control
of the domain through the DNS provider's API and can hold fully valid
certificates before serving a single request.

Consequence: no certificate state is migrated. The new gateway issues its own.

#### The provider is declared, and its module has to be in the binary

A DNS provider in Caddy is a Go module compiled into the binary with xcaddy, not
a setting. Upstream's image carries none, so a configuration naming a provider
cannot load on upstream's image.

```yaml
acme:
  provider: desec
```

That one field decides two things: the `acme_dns` directive in the gateway's
Caddyfile, and which image the gateway runs.

**Nothing is built on a host.** The gateway is also the etcd witness in the
suggested topology, and etcd's stability is a function of fsync latency, so a
compile there competes for exactly the resource that placement was chosen
around. Images with the modules compiled in are published at
`ghcr.io/paisans-software/caddy`, and the default is the core catalog
foundation's track on Caddy 2, `ghcr.io/paisans-software/caddy:2.*.*`.

**The track moves, and what a host runs does not move with it unplanned.** The
image is rebuilt whenever upstream Caddy or its base image ships a fix, and the
track follows every release, so a security fix reaches a deployment without a
toolkit release, and nothing crosses a major version unasked. `deployment apply` asks the
registry, while it plans, for the newest tag on the track and the build it
names, and renders both in the gateway's compose file (Eg:
`ghcr.io/paisans-software/caddy:2.11.7@sha256:...`). A new build is therefore a
changed compose file: the dry run shows it, naming the build that replaces the
running one, and the apply pulls it, asks it for its DNS module, validates the
configuration on it and waits for it to be healthy, as for any image change.
The registry is asked from the workstation, anonymously; when it cannot be
reached, the plan keeps the build the site already runs and says so, and a
site running none is refused until it can be. Each apply records the build in
the site's manifest, `/srv/paisans/<token>/.paisans-manifest.json`, under
`pins`, as it does every app's (see *The image an app runs is declared, not
implied*): the tag as configured, the reference the
compose file names, when it was resolved and whether the digest came from the
registry or was kept.

The cost is stated rather than hidden: a deployment's gateway depends on that
registry and on those images being rebuilt. The escape hatch is what keeps it
from being lock-in.

**A provider we publish no image for is supported, with an image of your own:**

```yaml
acme:
  provider: route53
  image: ghcr.io/example-org/caddy-route53:2.11.4
```

Declaring a provider with no published image and no `acme.image` is **refused**:
the module would never reach the gateway, so it would hold no certificate for
any hostname. Declaring upstream's own image is **refused** for the same reason,
and it is the only image a reference alone can be judged by.

An `acme.image` may name a tag that moves, as the default does, because it is
resolved the same way: `2`, `2.11` and `2.11.4` are each pinned to the build
they name when `deployment apply` plans, and a digest is taken as written. `latest`, or no
tag at all, is **refused**: it moves to the next Caddy major version the day one
ships, and a major version can change the Caddyfile syntax the gateway is
rendered with. The gateway's validation gate would stop that apply before the
new build replaced anything, and every apply after it, until the toolkit caught
up with the new major version. Naming the major version is the fix.

Everything else is checked at `deployment apply`, by asking the binary which modules it has
rather than inferring it from a name.

#### The token is checked from the server before Caddy uses it

A DNS token can be restricted to the addresses it is used from (Cloudflare's
Client IP Address Filtering), and the provider judges the address each call
comes from. A token that works from the workstation can be refused from the
gateway, and Caddy, refused, retries every hostname against every certificate
authority until the provider locks the token out, a lockout that holds while
the retries keep arriving.

So on each site whose Caddy is about to start, reload or be recreated, `deployment apply`
first runs `check DNS token` there: after the gateway's files are written and
its modules checked, before `validate gateway config`. It reads the token from
the `caddy.env` it has just written, on the host, and hands it to curl through
a configuration on stdin, so the token never leaves the host and is in no
command line. It looks up the zone holding `community.domain`, which the
address filter applies to, and then verifies that the token is `active`. A
refusal stops that site's apply with the gateway unchanged, still serving the
certificates it holds, and so does a provider API that does not answer,
because then nothing can tell a token Caddy can use from one it cannot.
Founder decision. A provider the toolkit has no check for is not gated, and
the step says so.

`dns init` and `dns prune` run the same check from the workstation before
their first records call, so a token filtered away from it is reported as
refused `from this workstation`, with the address the provider judged.

### Trusted proxies must be the mesh subnet — set this at `init`

This is the one item that is expensive to retrofit, and it fails quietly.

Applications behind a reverse proxy decide which upstream to trust for
`X-Forwarded-For`. If that is set to a specific host because that host was the
gateway on day one, then the moment a different node starts proxying, every
request arrives from an untrusted source. The symptoms are unpleasant and
indirect: wrong client addresses in logs and rate limiting, possibly broken
redirects, and for anything checking addresses during a session, broken logins.

**Set trusted proxies to the mesh subnet from the first install.** Then any node
in the mesh can front the apps and the gateway role becomes genuinely portable.

Same principle as apps connecting to their own site's HAProxy rather than a
named database host: an application should not know what is on the other side of an
indirection.

### What travels with the role

The **auth gate goes with the gateway.** It sits on the edge beside the reverse
proxy and is part of it, not a separate app. The toolkit should treat reverse
proxy and auth gate as one unit rather than letting someone move half of it.

OIDC callback URLs point at the public domain, not at the gateway host, so they
need no change. Worth stating plainly, because "will this break single sign-on"
is the first question anyone asks about this move.

### The gateway host's own sites live in the deployment's `caddy.d`

A gateway host often serves something that is not part of the community, Eg: a
blog its owner runs beside it. Only one thing can bind 80 and 443, so that site
goes through the gateway's Caddy too, and its site block lives in the
deployment's `/srv/paisans/<token>/caddy.d`. The gateway's Caddy mounts that
directory read only at `/etc/caddy.d`, and the Caddyfile ends with `import
/etc/caddy.d/*.caddy`, at the top level and after the toolkit's own snippets,
so each file there is a whole site block and may import
`upstream_unavailable`, `upstream_failover` or `upstream_single`.

**paisans never touches what is in it.** `deployment apply` creates the directory, the
deployment user's and 0755, when it is missing, and leaves an existing one as
it is. The host's owner writes into it as the deployment's user, Eg: `sudo
-u paisans-<token> tee /srv/paisans/<token>/caddy.d/blog.caddy`, never as
root: the user owns the directory and could put a link in a file's place,
which a root write would follow. paisans never writes a file
there, never records one in its manifest and never removes one, because what
is in it belongs to the host's owner rather than to the deployment. An empty
directory is fine: Caddy logs a warning for an import glob that matches no
file and starts (Caddy v2.11.7, `caddyconfig/caddyfile/parse.go`, `doImport`).
The directory stays on the host when the gateway role moves, and its files
move by hand to the new gateway's. `doctor` lists each file there, so whoever
moves or stops the gateway can see what else depends on it. `gateway move`
does not stop the old gateway's Caddy while its `caddy.d` holds a site file: it
stops at that stage, naming the files, and resumes there once they have moved
or gone. When `site remove` takes a gateway off a host whose `caddy.d` is in
use, it keeps the Caddy, reduced to those sites, so they stay served (see
*`site remove` takes a site out*).

### It is a config change

```yaml
sites:
  home-a: { roles: [data, apps] }        # gateway removed
  vm:     { roles: [gateway, witness] }  # gateway added
```

`paisans deployment apply` without `--site` then performs the overlap above. Three things
make that hold across its steps:

* **The old gateway stays open.** Its 80 and 443 allows are `site admit`'s,
  and no `deployment apply` step changes a firewall rule, so they stay until DNS has
  moved. Once the Caddy has stopped, the old host's owner admits the site again
  without `--public-ports`, which removes them.
* **`dns init` leaves a moving name to the move.** A hostname the gateway
  serves that still holds the record `dns init` made for another declared
  site is marked `moving`, shown as a note, and neither created nor counted as
  a conflict, so the run is not stopped before the new gateway is applied. A
  record pointing elsewhere that `dns init` did not make is still a conflict.
  An app moving from a monitor site to the gateway is marked the same way,
  since `dns init` cannot tell that site from a former gateway; no move
  follows, so its note says to change the record by hand at the provider.
  **Keep the old site declared, with its `public_address`, until `paisans
  gateway move` finishes.** `dns init` knows a former gateway only as another
  declared site's address. Once the old site's address is gone from the
  configuration, or the site itself, `dns init` cannot tell a moved role from
  a changed address, and updates its own records at the gateway's hostnames
  to the new gateway: a cutover before the new gateway is applied.
* **The move runs last.** `deployment apply` asks every site without the gateway role
  whether it still holds a gateway, and when one does, plans `gateway move`
  after pass two, in a `gateway` section of its own, waiting on the new
  gateway's apply. The new gateway is applied, comes up and obtains its
  certificates in the install steps; the move then verifies it, moves DNS,
  waits and stops the old Caddy.

The old Caddy's files and certificates stay on home-a, recorded as left over
by its apply. `paisans gateway move` runs the same stages alone, when the new
gateway has already been applied.

Rollback, before stage 6, is moving the role back and applying again: the
gateway it moves from is then vm, and the same stages move the records back.

## Where documentation lives

**Generic material is canonical here.** Architecture, runbooks and the recovery
procedure — at a level true for any community, naming no hosts — live in this
repository. A community that has lost everything can still read them, because
they are not hosted on the thing that failed.

That is the whole point. The outage that started this design took the stack down
and the documentation with it, including the instructions for recovering the
stack. Documentation that shares a failure domain with its subject is not
available when it is needed.

A template for [deployment agent rules](docs/deployment-agent-rules.md) is
included for the same reason. It carries the two things that transfer between
communities unaltered (decision authority, and the blast-radius tiers that say
how much human involvement an action requires), so that after a catastrophic
loss the rules governing the recovery are readable before the wiki is back.
Operators copy it into their own configuration repository as `AGENTS.md`, where
every agent reads it without being told to.

It is a **floor**, and it is not a Charter. A community's Charter is its
mission, values and code of conduct; it stays in the community's wiki and it
governs these rules rather than being replaced by them. Those values, and every
community-specific choice, are absent here by design.

**Community-specific material stays with the community.** Its current state,
decision log, deployment particulars and governance are not generic, and
publishing them here would leak a deployment's shape to a third party. That is
the same boundary described under *Relationship to `paisans.community`*, and it
is a privacy control rather than tidiness.

**Operators may mirror these documents into their own wiki**, so sysadmins read
them where they read everything else. Two rules keep that honest:

* **The mirror is one-way and labelled as a copy.** Edits happen here. Without
  that, the two drift and nobody can tell which is current — which is worse than
  having one copy.
* **Nothing crosses the other way.** Convenience is exactly how a hostname ends
  up in a public repository.

**Recovery needs four things, and only the first is here:** this documentation,
plus the deployment's config, its secrets, and its data. See *Backup and
restore*. Documentation tells you what to rebuild; it contains nothing to rebuild
it *from*.

## Where this repository lives

`Paisans-Software/paisans-cli`, public. Its Go module path is
`github.com/paisans-software/paisans-cli`. GitHub redirects the repository's
earlier names, and a redirect is a courtesy rather than a contract, so a clone
made under one should have its remote updated rather than relying on it.

## Relationship to `paisans.community`

`paisans.community` is one deployment: its compose files, its Caddy config, its
operational scripts. This repository is the generalisation — the part another
organizer could adopt without inheriting this community's specifics.

## License

Copyright the Paisans contributors.

This program is free software: you can redistribute it and/or modify it under
the terms of the GNU Affero General Public License as published by the Free
Software Foundation, either version 3 of the License, or (at your option) any
later version. SPDX: `AGPL-3.0-or-later`. The full text is in
[`LICENSE`](LICENSE).

Section 13 applies to anyone who offers a modified toolkit to users over a
network — a hosted installer, say: those users must be offered its source.
Running the unmodified toolkit to deploy a community does not trigger it, and
the applications a deployment runs keep their own licenses; the toolkit
configures them and does not link them.
