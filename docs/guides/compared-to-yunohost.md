# How Paisans differs from YunoHost

YunoHost and Paisans both put self-hosting within reach of people who are not
full-time sysadmins. They answer different questions.

* **YunoHost:** "I want one server at home running a lot of apps, managed from
  a browser."
* **Paisans:** "A group wants a private, federated community that runs across
  several members' machines, survives losing one, and grows without a data
  migration."

## What they share

* Running on hardware you control, with certificates and DNS handled for you.
* Single sign-on across the apps: YunoHost through its LDAP directory and
  portal, Paisans through Pocket ID with passkeys.
* Catalogs of packaged apps.
* Backups and a health check: `yunohost backup` and its diagnosis page;
  backups to S3 and `paisans doctor` here.

## What is different

| | YunoHost | Paisans |
|---|---|---|
| What it is | An operating system layer on one Debian server | One static binary an operator runs from their machine that manages a suite of Docker containers across one or more server hosts, reaching them over SSH |
| How you drive it | A web admin panel, or the `yunohost` CLI on the server | One file, `paisans-stack.yaml`, saved locally and synced to every server in the deployment to avoid total loss. Every command shows its plan and changes nothing without `--execute` |
| Machines | One | One or more sites, joined by a WireGuard mesh |
| Database | Each app's own, on the server | Postgres under Patroni from the first install, a cluster of one until a second site joins |
| Files | Local disk | Garage object storage from the first install, replicated when a site is added |
| Losing a machine | The server is the deployment | A site can be added, or removed even when its host is not reached, while the rest keep running |
| Packaging | Install scripts that run on the host | Docker Compose blueprints, with no scripts on the host. A third party blueprint runs code only inside the containers it declares |
| Catalog | Hundreds of apps of every kind | A core catalog of blueprints for private or public federated communities. Any person or organisation can publish a catalog of their own |
| Mail | A full mail server | None; apps send through an existing, external SMTP server such as Brevo or Mailgun, unless a third party blueprint provides one |
| Operators | The admins of one server; recovery is that server's backups | Built for operators leaving and machines being lost; see below |

## Operator continuity and recovery

A community outlives any one of its operators, the people who run its
machines, so Paisans watches for the loss of an operator before it happens and
has a way back after it does.

* **One operator away from lockout is never silent.** `validate` refuses a
  deployment with no operator whose keys are on every site that runs Pocket
  ID, and warns about one, because one lost workstation would leave nobody
  able to run the deployment. The operator reconciler reads Pocket ID and
  fails its health check while only one operator is declared, or when an
  operator's account is missing, disabled, or outside the member group or
  `operators`, so the monitor raises it while someone can still fix it.
* **The DNS zone is held the same way.** When the zone is at Cloudflare, every
  operator must be a Super Administrator of the account holding it, and there
  must be at least two, because an account nobody can add
  members to is recovered only at the registrar.
* **A lost workstation loses nothing.** Every site keeps signed copies of every
  version of the configuration and the encrypted secrets, so an operator with an
  empty machine recovers them from any host.
* **A lost passkey is a new link.** `paisans operator magic-link` gives a declared
  operator a fresh one-time sign-in link.
* **An operator who leaves is closed out.** The secrets file is re-encrypted
  without them, the mesh is rekeyed, and every other secret is rotated.
  A site whose host the others can no longer reach is cut off with `site
  remove --host-gone` and its role handed to a new host.

## Why Paisans is built this way

Moving a running federated community is the expensive part. Remote servers
cache the URLs of its media and the identity of its accounts, and those
references cannot be recalled. So Paisans starts every deployment in the shape
a multi-site deployment needs, even when it has one site. Adding a second site
is then an addition, and never a conversion of live member data.

That costs a single-site deployment some memory and extra moving parts, which
is only worth paying for a community that will use what it buys.

## Choosing between them

### YunoHost is the better choice if any of these is true

* [ ] One person runs it, and nobody else will ever need to take it over.
* [ ] It will only ever be one machine, and losing that machine for a day or
  two while you restore a backup is acceptable.
* [ ] You want apps outside federated community software: file sync, photos,
  password managers, media servers.
* [ ] You want to host members' mailboxes yourselves.
* [ ] Whoever runs it wants a browser panel, not a terminal and a YAML file.
* [ ] Your DNS is not at Cloudflare and you do not want to move it. Cloudflare
  is the one DNS provider Paisans drives today.
* [ ] You need it in production now. Check the status at the top of the
  README before choosing Paisans.

### Paisans is the better choice if most of these are true

* [ ] Two or more people share running it, and the community has to outlive
  any one of them.
* [ ] It federates (forums, chat, a blog) and its hostnames will be cached by
  servers you do not control.
* [ ] It may grow past one machine, or onto machines different members own.
* [ ] It has to keep running when one of those machines is lost.
* [ ] Your members can sign in with passkeys, through one identity provider
  for every app.
* [ ] Mail goes out through an external SMTP service, and nobody needs a
  mailbox on the community's own domain.
* [ ] An operator is comfortable running a command line tool and reviewing its
  plan before saying `--execute`.

### Examples

**A household server.** One person wants file sync, a photo library and email
for their family on a mini PC in a cupboard. One machine, one admin, mailboxes,
apps outside the Paisans catalog: **YunoHost.**

**Trying self-hosting out.** Someone wants to install a dozen apps, keep the
ones they like and remove the rest from a browser. A wide catalog and a web
panel matter more than anything Paisans adds: **YunoHost.**

**A club's forum, chat and wiki.** Three organizers of a local club want a
private forum, a Matrix chat and a wiki for their members. Each has a small
machine at home, and the club should not go dark when one of them moves away
or loses interest. Shared operators, federation, more than one machine:
**Paisans.**

**Starting small, planning to grow.** A community starts on one cloud VM with
a forum and a blog, and expects members to bring hardware within the year.
Federated content is published from day one, so the domain and media URLs must
never move: **Paisans**, on one site now, with sites added later.

**A community that also wants email.** As the club above, but members expect
an address on the community's domain. Paisans does not host mail. Either run
Paisans and give mail to a hosted provider on that domain, or, if one box is
enough, **YunoHost** does both.
