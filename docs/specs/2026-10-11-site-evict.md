# Evicting a site whose host nobody else can reach

Date: 2026-10-11. Status: approved by the founder in session, 2026-10-11,
with the decisions under *Settled*. `secrets rotate`, the rotation of every
secret other than WireGuard keys, is its own spec and pull request.

The case: an admin stops cooperating and the deployment's other admins can no
longer log in to their host. The host still runs and is still a member of the
deployment, possibly as a data site (an etcd voter and a Patroni member, perhaps
the leader), a Garage node, an apps site with a Pocket ID instance, or the
gateway. The others want it out, and its role carried by a new host.

`site remove --host-gone` exists for a host that is dead. This spec says what it
does wrong for a host that is alive, what an eviction needs instead, and how a
replacement host takes the role over.

## What the mesh trusts

Every cluster service trusts the mesh and nothing else:

* etcd listens on `http://`, peer and client, with no TLS and no client
  certificates (`internal/render/templates/infra-compose.yaml.tmpl`).
* Patroni's REST API has no authentication configured
  (`internal/render/templates/patroni.env.tmpl`).
* Every data site shares one set of Postgres superuser, standby and admin
  passwords, and every Garage node one `rpc_secret` and `admin_token`.
* Every published port binds the site's mesh address (README, *So every
  published port binds the site's mesh address*), which already says a
  compromised peer reaches every app ungated.

So **being a WireGuard peer of the remaining sites is being in the cluster**,
and cutting the peer is what evicts. Each site has its own WireGuard key in
`secrets.enc.yaml`, each peer entry is a `/32`, and there is no preshared key.
Removing the site's public key from every remaining site's mesh file and
running `wg syncconf` drops it: the remaining sites no longer accept a
handshake from that key, and route nothing to its address.

## Why `site remove --host-gone` is not enough

1. **It cuts the mesh last.** Stage 1 moves the data off the site, and stage 2
   removes it from etcd and the mesh. A dead host does not mind the order. A
   live one is still a peer through stage 1, so its Patroni keeps writing its
   member key to etcd, and stage 1's gate, that `patronictl list` no longer
   lists it, waits for something that does not happen.
2. **A switchover needs the leader to cooperate.** When the site leads, stage 1
   runs `patronictl switchover`, which asks the leader to step down. A host
   that is cut off, or run by someone hostile, does not.
3. **Garage expects the node online.** After `garage layout remove` the old
   node must stay up until its blocks have moved. A node nobody controls needs
   `layout skip-dead-nodes`, which is not built, by founder decision.
4. **It changes no secret.** The host keeps everything it was rendered (below,
   *What the host still holds*), and an admin who could decrypt
   `secrets.enc.yaml` holds every secret of every site.

## The eviction: `site remove <site> --evict`

A mode of `site remove`, because the end state, the refusals about roles and
most of the stages are the same. `--evict` implies `--host-gone`: the host is
never reached. What changes is the order, the cut first, and what follows from
the cut.

| Stage | Work | Gate |
|---|---|---|
| 1. Cut | on every remaining site, the mesh file without the site, then `wg syncconf`; the gateway's routes without the site's upstreams, validated and reloaded | no remaining site lists the site's key as a peer; `wg show` on each remaining site has no handshake from it after the sync |
| 2. DNS | the site's address records deleted, as `site remove`'s DNS stage does, so the names stop sending visitors to the host as early as possible | listing the zones finds nothing left to delete |
| 3. Database | wait for Patroni: when the site led, its leader key expires once it cannot renew it, and a remaining member takes over by itself; then the site's member key deleted from etcd, which it can no longer rewrite | a remaining site leads; `patronictl list` no longer lists the site |
| 4. etcd | `etcdctl member remove`, from the remaining members | the voters are exactly the end state's and all healthy |
| 5. Storage | the site's Garage node out of the layout as a dead node (below, *A dead Garage node*) | no layout row for the site; the remaining nodes settled |
| 6. The rest of `site remove` | HAProxy and each replica's `patroni.env` as in its stage 2; config and monitor as in its stages 5 and 6 | as there |
| 7. Report | what the host still holds, which secrets to rotate and in what order, and the `apply` that brings up any replacement the configuration declares (below) | none |

Refusals, in addition to `site remove`'s own, before anything changes:

| Refused | Because |
|---|---|
| the remaining healthy etcd voters are not a majority of the current voters | `member remove` needs a quorum the remaining sites cannot make; recovery is restoring etcd from a remaining member, outside the toolkit |
| a remaining site reaches the mesh only through the site, as its relay | cutting the relay cuts that site too |
| the only data site | nothing remains to fail over to; recovery is a restore from backup |
| the site holds the gateway, and `external.acme_dns_token` was not confirmed replaced | the host holds that token, which can change the community's DNS records (below, *Recovering the gateway*) |
| without a terminal, or without the site's name typed | an eviction is not undone by running it again |

The only gateway and the only apps site are **not** refused when the
configuration already declares their replacement: an end state that validates
is enough, even though the replacement's host is not prepared yet. Between the
eviction and the `apply` that brings the replacement up, the community's names
answer nothing; the plan says so.

**When the site leads and no remaining member is a streaming Sync Standby**,
the plan shows the most current replica's lag, and the operator types `lose
writes` before the eviction goes on. A failover then promotes that replica, and
any commit the evicted leader acknowledged after the replica's last received
WAL is lost. Replication is asynchronous unless `cluster.synchronous` is set,
so this is the usual case for a leader. With a streaming Sync Standby nothing
is asked. Founder decision.

Stage 1 runs against every remaining site at once and its gate is strict: a
site that did not sync is named, and nothing after it runs. A partial cut is
still safe to leave, since every peer that dropped the key no longer talks to
it, and a re-run completes it.

## A dead Garage node

`layout skip-dead-nodes` is built for eviction only. Garage's layout keeps a
node until every partition it held has been copied, and an evicted node never
answers, so the layout is told to stop waiting for it.

* When the remaining nodes still hold every partition's data, which needs
  `storage.garage.replication` of 2 or more and at least that many remaining
  nodes, the node is skipped with no prompt.
* Otherwise objects held only on the evicted node are lost. The plan says so,
  and the operator types `lose objects` to go on, which passes
  `--allow-missing-data`. Founder decision.

## Replacing the host

**The replacement is a new site, under a new name, and the eviction always
comes first.** The site's name is the key of its WireGuard key and heartbeat
token in the secrets, its etcd and Patroni member names, its Garage node's tag,
its DNS records and its entry in every other host's records. A new name keeps
the old host and the new one apart in every one of those, and in every log.

The flow is one configuration edit and two commands:

1. In one edit of `paisans.yaml`: the new site declared, with the roles it
   takes over; any app pinned to the evicted site pinned to the new one.
2. `paisans site remove <old> --evict --execute`. The end state includes the
   new site, so the remaining sites' mesh files list it as a peer already, and
   its secrets are generated first if they are missing.
3. `paisans apply`, which prepares the new host, applies it and creates its DNS
   records, as for any site added to the configuration.

Evicting first means `site add` never has to wait for a site that will not
answer, and the old host is out of the mesh before the new one joins it. What
it costs is the time between the two commands when the evicted site was the
only gateway or the only apps site.

| The site held | After the eviction |
|---|---|
| nothing that only it held | `apply`, or `site add` for a data site |
| the only gateway or the only apps site | `apply`; the community is unreachable until it finishes |
| the only data site | refused; restore from backup |
| the etcd majority | refused; restore etcd from a remaining member, outside the toolkit |

## Recovering the gateway

The gateway is the one site that holds a credential usable from anywhere: the
DNS token, which `dns init` and Caddy's ACME challenge both use. The evicted
host keeps working copies of it. Until it is revoked, the host can point the
community's names back at itself.

So an eviction of a gateway site asks, at the terminal, whether the token has
been revoked at the provider and a new one set with `paisans secrets set
external.acme_dns_token`, and the operator types `revoked`. The toolkit cannot
revoke a token itself: it holds the token, not a credential that manages
tokens. It then checks the token in the secrets file against the provider
before anything changes, so stage 2 can delete the old records with it.

The replacement gateway is safe to bring up because nothing the evicted admin
holds reaches it:

* **No login.** The admin is out of `admins` and every `ssh.keys` before the
  `apply` that prepares the new host. That `apply` reviews every key it grants,
  since the new host has no record (docs/specs/2026-10-11-config-review.md),
  and the host's owner can make an allowlist with `host allow` before it.
* **No secret.** When the evicted admin could read `secrets.enc.yaml`, the file
  is re-encrypted under a new data key before the new site is declared, so its
  WireGuard key and every other secret generated for it are ones the admin never
  saw; and every remaining site's WireGuard key is rotated with `mesh rekey`
  before the new site joins, since any of them could otherwise be presented to
  the new gateway.
* **No route.** The evicted host is no remaining site's peer, so the new
  gateway's mesh file never lists it.

**What stays with the evicted host.** Its certificates for the community's
names, valid until they expire, and everything it saw while it terminated TLS:
session cookies and anything typed into a form. The runbook says to revoke the
certificates and to sign every member out; the second is part of `secrets
rotate`.

## Rekeying the mesh: `paisans mesh rekey`

```
paisans mesh rekey home-a            # dry run
paisans mesh rekey --all --execute   # every site, one at a time
```

One site at a time, because a site whose key changes loses its tunnels until
every peer knows the new key, and taking more than one down at once can cost
etcd its quorum.

| Stage | Work | Gate |
|---|---|---|
| 1. New key | a new private key generated on the workstation and written to the secrets file as the site's next key, beside the current one | the file decrypts and holds it |
| 2. Swap | the site's own mesh file with the new key, and every peer's mesh file with the new public key in place of the old, each with `wg syncconf`, the peers at once and the site last | every peer has a handshake with the site under the new key |
| 3. Health | none | etcd healthy at every voter; exactly one Patroni member leading and running, and every other data site streaming |
| 4. Promote | the next key becomes the site's key in the secrets file, the old one deleted | the file decrypts and holds only the new key |

A run stopped part way resumes: a site with a next key in the file is mid
rekey, and which key its host runs, read with `wg show`, says whether stage 2
is done. Such a site goes first in the next run, and its swap is finished
before health is read, since the partial swap may be what is unhealthy; the
health refusal applies only before a site's rekey starts. An `--all` run
records the sites it has left in the secrets file, so a stopped one resumes
without rekeying a site it already promoted.

A relay's swap pauses Patroni. Every site with no dial endpoint routes what it
has no peer for through the first site that has one, so swapping that relay's
key cuts the undialled sites apart for a moment, which can cost etcd its quorum
and, through it, let Patroni fail over for a partition that heals in seconds.
Around such a site's swap the cluster is put in maintenance mode with
`patronictl pause --wait`, read back from `patronictl show-config`; the swap
runs; etcd re-forms by itself once the tunnels return, and the health gate
waits for it with a bounded timeout; then `patronictl resume --wait`. The plan
names the sites that relay through the site and says the cluster pauses for
its swap. A run that stops in between leaves Patroni paused, since resuming it
before etcd has its voters back is the failover the pause prevents; the run
that resumes the swap resumes Patroni after it. A deployment of one dialled VM
and homes with no endpoint can so rekey every site, which the eviction's
runbook needs.

Refused, before anything changes: an unhealthy etcd or Patroni, a Patroni
paused by anything but this rekey's own relay swap, and a site that does not
answer over ssh.

## Runbooks

`docs/guides/when-an-admin-leaves.md` covers two cases, each as an ordered list
of commands: an admin who leaves on good terms, and an admin whose site is
evicted. Each step says what it closes and what still stays open after it.

## What the host still holds

Rendered on the host, and so known to whoever controls it:

| Secret | On | Rotation today |
|---|---|---|
| its own WireGuard key | every site | none needed once evicted |
| the external DNS token (`external.acme_dns_token`) | the gateway | `secrets set`, and revoking it at the provider. **First**: it can redirect the community's names |
| OIDC client secrets | apps sites | `oidc client create --rotate-secret` |
| Mbin's OAuth2 server keypair | apps sites running Mbin | none |
| apps' S3 keys | apps sites | `storage rotate-key --app` |
| Postgres superuser, standby and admin passwords; apps' database passwords | data and apps sites | none; `secrets set` alone would leave the roles in Postgres with the old ones |
| Garage `rpc_secret` and `admin_token` | Garage sites | none |
| heartbeat token | every site | none |
| a copy of the database and of the objects | data and Garage sites | cannot be taken back |

**An admin who was a recipient of `secrets.enc.yaml` holds all of the above for
every site**, including every other site's WireGuard private key. WireGuard
accepts a handshake from any address that proves a key, and moves the peer's
endpoint to where it came from, whatever endpoint the peer was configured
with, so a site key in hostile hands can take over that site's tunnel to any
peer it can send a packet to. Evicting
one site does not close that. It needs:

* their age key taken out of `.sops.yaml` and the file re-encrypted under a
  new data key (`sops rotate --rm-age`), not `sops updatekeys`, which wraps
  the same data key for the new recipients and so leaves it known to anyone who
  could read the file before;
* **every site's WireGuard key rotated**, with `mesh rekey --all` (below,
  *Rekeying the mesh*);
* every shared secret above rotated, in the order of the table, with `secrets
  rotate`, which changes each secret where it is checked and where it is used
  together (its own spec).

Credentials only the mesh can use matter less once the mesh is closed; the DNS
token, the OIDC secrets and the Mbin keypair work from the internet, and go
first.

## Settled

Founder decisions, 2026-10-11, in session:

1. **`--evict` is a mode of `site remove`**, with the cut first.
2. **A leader with no streaming Sync Standby** fails over after the operator
   types `lose writes`. `cluster.synchronous` stays off by default.
3. **A dead Garage node** is skipped from the layout; when objects would be
   lost, after the operator types `lose objects`.
4. **The eviction comes first**, and a replacement is declared in the same
   configuration edit and brought up by `apply`. `site add` never waits for a
   leaving site, and `gateway move` is not used to replace a gateway nobody can
   reach.
5. **`mesh rekey` ships with the eviction.** Every other secret's rotation is
   `secrets rotate`, in its own pull request.

Not built, and not part of this work: a record of who owns which site, which a
`validate` warning about one admin controlling the etcd majority would need.
