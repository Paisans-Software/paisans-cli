# How a deployment protects itself

This guide is for the admins of a paisans deployment. It walks through what can
go wrong between the people and machines that run one, and says for each case
what the toolkit does about it, what it does not, and what is left to you. The
step by step response to an admin leaving or being evicted is
[When an admin leaves](when-an-admin-leaves.md); this guide is the reasoning
behind it.

Each scenario ends with **Protects**, **Does not protect** and **You do**. Where
a protection is designed but not built, it says so.

## Who is trusted with what

Two kinds of trust run through the design, and keeping them apart explains most
of what follows.

**Admins are trusted with the deployment.** Every admin holds `paisans.yaml`
and can decrypt `secrets.enc.yaml`. With those two files an admin can change
anything: move an app, add a site, re-provision the whole stack onto their own
hardware. The toolkit does not try to protect admins from each other at that
level, because it cannot: anyone who can decrypt the secrets can run the
toolkit. Choosing admins is the protection, and removing one is what the
runbook is for.

**A host belongs to whoever runs it.** An admin who brings their own machine to
the deployment decides who logs in to it. The configuration names which admins
may reach a site (its `ssh.keys`), and the host's owner can bound that list on
the host itself. This is the one place where one admin is protected from
another: not over the deployment, but over their own machine.

**The mesh is trusted completely.** etcd, Patroni, Postgres replication, Garage
and every app's published port listen on the WireGuard mesh and trust whatever
reaches them there. Being a WireGuard peer of the other sites is being in the
cluster. So the mesh keys are the cluster's real credentials, and cutting a peer
is how a site is taken out.

## Someone adds their key to a host they were not given

**What could happen.** `paisans.yaml` is one shared file. Bob adds his name to
`home-a`'s `ssh.keys`, or replaces the key listed under `admins.alice`, and
commits. Later Alice runs `paisans apply` with her own access, and `host
prepare` installs Bob's key for the login user, which has `sudo` and Docker:
root.

**Protects.**

* **The review.** Each host records what this deployment last granted it.
  `apply` and `host prepare` show every key that would be added or changed
  since, with its admin, fingerprint and comment, and an `--execute` run asks
  the operator to type `approve` at a terminal before any host changes. No flag
  answers it, and without a terminal the run is refused.
* **The host's allowlist.** The owner of `home-a` can run `paisans host allow
  home-a alice` to write the fingerprints that may ever be added there, in
  `/etc/paisans/ssh-allow.<user>` on the host. `host prepare` then refuses to
  add any other key, whoever runs it. The list holds fingerprints, not names,
  so a key swapped in under Alice's name is refused too.

**Does not protect.** A host with no allowlist file allows every key, by
founder decision, so until its owner runs `host allow` the review is the only
check. Neither control removes a key already in `authorized_keys`.

**You do.** Read the review before typing `approve`, as you would read a diff
before merging. If you own a host, run `host allow` on it.

## Someone changes an image

**What could happen.** Bob changes an app's image in `paisans.yaml` to one he
controls. The next `apply` pulls and runs it on every site that runs the app,
on the mesh, with the app's secrets.

**Protects.** The review covers every image the render runs, infrastructure and
apps, and needs `approve` for any change, the same as a key. `validate` refuses
floating tags, so a reference that has been reviewed keeps meaning the same
image.

**Does not protect.** An image an admin approved without reading it.

**You do.** Treat an image change in the review as a code change.

## An admin leaves on good terms

**What could happen.** Nothing hostile. But the admin still holds keys on the
sites, a Pocket ID administrator account, and, if they were a recipient of the
secrets file, every secret as it was when they left.

**Protects.** Taking them out of `admins` and every `ssh.keys`, then preparing
each site, deletes every key `host prepare` added for them. Re-encrypting the
secrets file under a new data key keeps every later value from them. Rotating
secrets with `paisans secrets rotate --all`, after the mesh is rekeyed, makes
what they already hold worthless, except the two secrets that encrypt stored
data, which it keeps.

**Does not protect.** Nothing takes back the copies they hold: old versions of
the secrets file still open with their key. A key of theirs that was in
`authorized_keys` before the toolkit first ran is never removed by it, because
nothing on the host says whether it is the only way in.

**You do.** Follow *An admin who leaves on good terms* in the runbook, and decide
how much to rotate by how much you trust them.

## An admin goes silent, or locks the others out of their host

**What could happen.** An admin stops answering, or turns hostile, and the
others can no longer log in to the host they run. It is still in the mesh, so
it is still in the cluster: possibly the Postgres leader, an etcd voter, a
Garage node holding uploads, an apps site, or the gateway.

**Protects.** `paisans site remove <site> --evict` takes the site out without
ever reaching it, in an order chosen for a host that is alive and may resist:

1. **The cut, first.** Every remaining site drops the site as a WireGuard peer.
   From then on its handshakes are ignored, and it can reach no cluster service.
2. **DNS.** Its address records go, so the community's names stop sending
   visitors to it.
3. **The database.** The cut leader cannot renew its leadership in etcd, so a
   remaining member takes over on its own. With a synchronous standby nothing
   is lost. Without one, which is the default, the operator is shown how far
   behind the best replica is and types `lose writes` to go on.
4. **etcd, then storage.** Its etcd member is removed by the remaining voters,
   and its Garage node is dropped from the layout as dead. If some uploads
   existed only on that node, the operator types `lose objects` to go on.
5. **The rest** of an ordinary `site remove`: HAProxy, the replicas'
   configuration, `paisans.yaml`, the monitor.

A replacement host is declared in the same configuration edit, under a new
name, and brought up by `apply` afterwards. Its WireGuard key and every secret
generated for it are new.

**Does not protect.** The evicted host keeps what it had: a copy of the database
and uploads as they were when it was cut, and every secret it was rendered.
Eviction is refused when the remaining sites are not a majority of etcd's
voters, or when the site is the only data site; recovery then is a restore,
outside the toolkit.

**You do.** Follow *An admin whose site is evicted* in the runbook.

## The evicted site was the gateway

**What could happen.** The gateway terminates TLS for every member and holds
the DNS token. A hostile gateway can read every session cookie and form that
passes through it, and can point the community's names wherever it likes.

**Protects.** Evicting a gateway asks the operator to type `revoked`, confirming
the DNS token was revoked at the provider and a new one set, and checks the new
token works before changing anything. The replacement gateway is safe from the
evicted admin: their keys are out of the configuration before it is prepared,
the review shows every key it grants, its secrets are generated after the
secrets file stopped being encrypted to them, and it never lists the evicted
host as a peer.

**Does not protect.** The certificates the old gateway holds stay valid until
they expire or are revoked. What it saw while it served members cannot be
unseen.

**You do.** Revoke the token first, revoke the old certificates, and sign
members out with `paisans secrets rotate --all`, which ends the sessions the
rotated secrets sign and asks for `sign out` before it does.

## Someone holds another site's WireGuard key

**What could happen.** An admin who could decrypt `secrets.enc.yaml` holds every
site's WireGuard private key, not only their own. WireGuard accepts a handshake
from any address that proves a key, and then sends that peer's traffic to the
new address. So with `home-a`'s key, they can pose as `home-a` to any site whose
WireGuard port they can reach, and every cluster service believes them.

**Protects.** `paisans mesh rekey --all` gives each site a new key, one site at a
time so etcd keeps its quorum, updating every peer before moving on. Once a site
is rekeyed, its old key matches no peer anywhere and its handshakes are ignored.
A site that is never dialled opens no WireGuard port, so it cannot be reached
from outside at all.

**Does not protect.** Until the rekey finishes, the old keys work.

**You do.** Rekey right after re-encrypting the secrets file, whenever an admin
who was a recipient leaves or is evicted. Finish a rekey that stopped before
anything else: `apply` refuses while one is unfinished, since it would render
the old key back.

## A former recipient still has the secrets file

**What could happen.** Removing someone from `.sops.yaml` and running `sops
updatekeys` looks like it closes the file to them. It does not: `updatekeys`
re-wraps the same data key for the new recipients, and they already know it.

**Protects.** `sops rotate --in-place --rm-age <their key>` generates a new data
key, so nothing written afterwards opens with what they hold. `.sops.yaml` is
edited too, since every paisans command encrypts to its recipients.

**Does not protect.** Every copy of the file they had, and every version in the
repository's history, still opens with their key. Only changing a secret makes
their copy of it worthless.

**You do.** Re-encrypt before anything new is written to the file, then rekey
the mesh, then rotate with `paisans secrets rotate --all`.

## Someone who is not an admin gains root on one site

**What could happen.** A break-in, or a hosting provider with console access.
That host is a mesh peer, so whoever holds it reaches every cluster service and
every app's port over the mesh, and holds the secrets rendered on it.

**Protects.** They cannot decrypt `secrets.enc.yaml`, which never leaves the
admins' workstations with a key that opens it. Eviction takes the host out the
same way as an admin's, and the rotation that follows makes what it held
worthless.

**Does not protect.** While it is a peer, nothing on the mesh tells it apart
from an honest site. The README says so where it explains why every port binds
the mesh address: a compromised peer reaches every app ungated.

**You do.** Evict it, and rotate what it held with `paisans secrets rotate
--all`.

## Losing `paisans.yaml` or the secrets file

**What could happen.** The workstation dies and the repository holding the two
files goes with it. The secrets file holds every credential of the deployment,
and without it every secret must be generated again and every client created
again.

**Protects.** Every site keeps every version of `paisans.yaml`,
`secrets.enc.yaml` and `.sops.yaml`, each a git commit of exactly those three
files signed with the SSH key of the admin who published it. `paisans config
publish` pushes a version to every site, and `apply --execute` publishes the
version it applies, so the copies follow what was actually run. Each site
fetches from its peers over the mesh every 15 minutes, so one that was down
catches up. A version is tens of kilobytes and nothing is pruned.

The sites only store; the workstation decides what to trust, because someone
with root on a site can write anything there:

* **Trust starts from something only the operator holds:** the newest version
  signed by their own SSH key, or a root hash they pin with `--root`. A history
  found only on the sites is never trusted, however consistent, because whoever
  holds those sites could have written it and listed themselves as an admin. An
  admin who has never published gets an anchor of their own with `paisans admin
  register`, which signs the version they already hold.
* **A version counts only when signed by an admin listed in its parent.**
  Someone who is not an admin cannot extend the history, however many sites
  they control.
* **Removal is final.** Once a trusted version drops an admin's key, nothing
  later signed by that key is trusted, so an admin removed for cause cannot
  publish a version that undoes it.

A version that was really published stays valid forever, so someone holding a
site can hide the newer ones and serve an old one, whose secrets an apply would
put back. No signature can prove a version is the newest, so three memories make
an old one visible instead:

* **The workstation** remembers the newest revision it has seen, and refuses an
  older head until the operator types `rollback`.
* **A majority of sites.** `config pull`, `config recover`, `admin register`,
  the publish of a first version and `apply --execute` count the sites they
  reached directly over ssh, and ask for `partial` when that is no more than
  half. A lying site can hide newer versions only from a workstation that
  reaches no honest one.
* **The hosts.** Each host records the version last applied to it, and `apply`
  asks for `rollback` before giving it an older one.

When the workstation is gone, `paisans config recover --from <user@host>
--root <sha> --execute` in an empty directory gathers from the hosts named and
then every site the trusted heads name, and writes the three files. The root is
the hash of a version the operator trusts, as `paisans config log` prints it;
an empty workstation holds no signature of its own, so keep that hash
somewhere other than the workstation, or commit `.paisans/config-root`, which
the admins may share. When the sites hold two heads, `recover` writes each to
`.paisans/incoming/<sha>/` and leaves the choice to an admin.

**Does not protect.** A fresh workstation that reaches only sites an attacker
holds can be served an old, genuine version, and applying it reaches only those
sites. Nothing checks that a removed admin's age key was taken out of the
secrets file: that is the operator's step (see *A former recipient still has
the secrets file*).

**You do.** Keep the files in a repository with more than one copy anyway, keep
each admin's age key backed up somewhere other than that repository, and keep a
trusted root hash where a lost workstation does not take it.

What no design can protect is losing every admin's age key. That is total loss:
add a second recipient, another admin or an offline backup key, as soon as the
deployment exists.

## Not built yet

* **A record of who owns which site.** Without it, nothing warns when one
  admin's hosts hold a majority of etcd's voters, which would let that admin
  block an eviction.
