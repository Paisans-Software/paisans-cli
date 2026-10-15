# `paisans gateway move`

Date: 2026-10-11. Status: proposed, for the founder's review. The DNS
deletion it makes is a founder decision (docs/decisions.md, 2026-10-11, *A
gateway move deletes the old gateway's address records once the new one is
verified*); the rest of the shape is proposed here.

README's *Moving the gateway* describes a five step overlap: the new gateway
comes up and serves while the old one still does, it is verified answering for
every hostname, DNS is repointed, the old records' TTL is waited out, and only
then is the old gateway stopped. Before this, moving the gateway role in
`paisans-stack.yaml` and applying rendered Caddy on the new site and nothing more,
and `dns init` stopped on every hostname, since each still pointed at the old
gateway and init never changes a record. This builds the missing steps.

## Scope

In:

* One gateway to another, both declared sites, with record management for
  `acme.provider` (Cloudflare today).
* The new gateway's address records added beside the old one's, the old one's
  deleted once the new one is verified, the TTL waited out, and the old
  gateway's Caddy stopped and removed.
* The deployment record the old gateway holds carried to the new one.
* `host prepare` keeping the old gateway's 80 and 443 open until the move
  stops its Caddy, and `dns init` leaving the hostnames that are mid move to
  the move.
* `apply` without `--site` running the move last, when a site without the
  gateway role still holds a gateway.

Out, each for a stated reason:

* **Applying the new gateway.** That is `apply --site <new>`, or `apply`
  without `--site`. The move checks that it serves; it does not bring it up.
* **The host owner's sites in `/srv/caddy.d`.** They belong to the host's
  owner, not to the deployment (README, *The gateway host's own sites live in
  `/srv/caddy.d`*), and their files move by hand. While the old gateway's
  `/srv/caddy.d` holds one, the old Caddy is not stopped.
* **A provider without record management.** The move refuses rather than
  doing half of it; README's manual steps still apply.
* **More than one gateway at once.** The configuration names one gateway,
  and a move goes from one site to it.

## The operator's flow

```yaml
sites:
  home-a: { roles: [data, apps] }        # gateway removed
  vm:     { roles: [gateway, witness] }  # gateway added
```

```
paisans apply --execute                  # applies vm, then runs the move last
paisans gateway move                     # or by hand: dry run, every stage
paisans gateway move --execute
```

## Which gateway it moves from

The configuration declares the end state: exactly one site with the gateway
role, the new gateway. `validate` is unchanged and does not admit two gateway
sites for the move, because the old one is never declared again: it is found
from what it still holds, which is the only place that fact lives once the
role has moved. A site other than the new gateway, and not running Caddy for a
monitor, is the old gateway when any of these is true:

* its host has this deployment's gateway Caddy container (labelled with the
  deployment id, compose project `paisans-<token>-infra`, service `caddy`),
  running or stopped;
* its host holds this deployment's deployment record,
  `/var/lib/paisans/deployed.<token>.json`, which only a gateway is given;
* its host holds the move's marker (below);
* an address record this deployment's `dns init` made, at a hostname the
  gateway serves, points at one of its public addresses.

The last covers an old gateway someone stopped by hand: its records still send
visitors there, and the move still adds, verifies and deletes. A site that
cannot be asked is an error, never "no gateway", for the reason README gives
under *Never infer host state from a failed probe*. Two sites that each hold a
gateway are refused; a move goes from one to one. None means nothing to move.

The move ends by deleting every one of these, so a finished move finds nothing
and a re-run says so.

## The stages

Each stage is planned from live state and ends at a gate that stops the run
with its evidence. Every gate runs on every run, so a run after a fix resumes
at the first stage with anything left to do, and proves each stage before it
again.

| # | Stage | Does | Gate |
|---|---|---|---|
| 1 | record | merges the old gateway's deployment record into the new one's (`deployrecord.Update` over both) | the new gateway's record lists every name the old one's lists |
| 2 | serve | nothing | the new gateway answers for every hostname (below) |
| 3 | dns add | creates the new gateway's A (and AAAA) record at every hostname the gateway serves, beside the old one's, with `dns init`'s comment, and reads each back | every such record is present, unproxied |
| 4 | dns remove | checks the new gateway again, deletes the old gateway's records under the rules below, and writes the marker | no address record at a gateway hostname points at the old gateway, and the marker is on its host |
| 5 | wait | sleeps until the marker's time plus the longest TTL of the deleted records | that time has passed |
| 6 | stop | checks the new gateway again, refuses while `/srv/caddy.d` holds a site file, stops and removes the old Caddy container, then removes the deployment record and the marker from the old host | the old host holds none of the four signs above |

### What "answers" means

Every hostname the gateway serves (each app's hostname, its other hostnames
and its media hostname, never a monitor's), requested from the operator's
machine over HTTPS **through the new gateway's public address**, and its
`public_address6` when declared, whatever the name resolves to. An app's own
hostname is asked at its kind's health route (`kinds.HealthFor`), every other
hostname at `/`. A hostname passes with a certificate valid for it and a status
below 500. That is what the gateway alone decides: it holds certificates
through DNS-01 before any record points at it, and Caddy answers 502 or 503
when it cannot reach the app. A redirect, such as the visibility gate's sign
in, is the app answering, and passes.

The check runs at stage 2's gate, again at the start of stage 4's run before
anything is deleted, and again at the start of stage 6's run before anything
is stopped. A failure stops the stage with every hostname that failed.

### What is deleted, and only then

Founder decision. A record is deleted only when all of these hold:

1. it carries this deployment's `dns init` comment;
2. it is an A or AAAA record, not proxied;
3. its name is a hostname the gateway serves in this configuration;
4. its address is the old gateway's `public_address` or `public_address6`,
   and not also one of the new gateway's;
5. every record the new gateway needs at every such name is present;

and the run deleting it has just verified the new gateway. A record pointing
at the old gateway that fails 1 or 2 is listed as kept, with why, and while one
stands nothing is deleted and the old gateway is not stopped: that name would
still send visitors to it. A record at any other name, a site endpoint's or a
monitor's or one the configuration no longer has, is not considered; `dns
prune` and `site remove` keep their own rules. Deletes go through
`dns.ExecutePrune`, which lists the zone again to confirm each.

### The marker and the wait

Stage 4 writes `/var/lib/paisans/gateway-move.<token>.json` on the old
gateway's host, root's and 0600 like the deployment record beside it: the old
and new site, the time, and the longest TTL of the records it deleted. A
provider's automatic TTL counts as the seconds it stands for, 300 at
Cloudflare. With no record left to delete and no marker (deleted by hand, or
never made by `dns init`), it writes one with the provider's automatic TTL. A
run that deletes more records later writes the marker again, so the wait
always runs from the last delete. A run interrupted during the wait resumes it
from the marker rather than starting it again. The marker goes last in stage
6, so a run that stopped part way finds the old gateway again.

No TTL is lowered beforehand. Every record `dns init` creates carries the
provider's automatic TTL, already short, and nothing in the toolkit updates a
record. A record someone made by hand with a long TTL is not the move's to
delete, so it stops the move in stage 4 rather than lengthening the wait.

## Around the move

**`host prepare`** derives 80 and 443 from the gateway role, and removes an
allow nothing derives. A site that has just lost the role would lose both at
the prepare that `apply` runs first, cutting off the old gateway before DNS
moved. So a site without the role keeps the gateway's two allows, with the
gateway's own comments, while this deployment's gateway Caddy is on its host,
and says so with a warning. The first prepare after the move stopped it
removes them. A Docker that cannot be asked fails the prepare.

**`dns init`** marks an address record wanted at a gateway hostname as
`moving` when that name still holds the record `dns init` made for another
declared site, unproxied, and the name is otherwise fine. It is neither a
conflict nor created: the move adds it once the new gateway answers. The plan
shows it as a note naming `paisans gateway move`. A record pointing elsewhere
that `dns init` did not make is still a conflict.

**`apply` without `--site`** asks every site without the gateway role, and not
running Caddy for a monitor, whether it holds this deployment's gateway Caddy,
record or marker. When one does, `gateway move` is planned after pass two,
waiting on the new gateway's prepare and apply, under a section of its own,
`gateway`. A site that cannot be asked stops the run before anything is
applied. The move runs last because it needs the new gateway applied and
answering, and because `apply` is the command that takes `paisans-stack.yaml` to a
running stack: README's *It is a config change* says the move is what applying
a moved role does.

**Left in place:** the old Caddy's files under `infra/caddy/`, recorded as left
over by the old site's apply, its certificates, and its image.

## Refusals

* Not exactly one site with the gateway role.
* The new gateway has no `public_address`.
* `acme.provider` has no record management, or the token is missing.
* Two sites each hold a gateway.
* A record conflicts with one the new gateway needs (as `dns init` reports it).
* The old gateway declares no public address apart from the new one's.

## Testing

In `internal/gatewaymove`, against fake hosts, a fake DNS provider and fake
HTTPS probes:

* the stages in order, and a whole move leaving nothing of the old gateway:
  no Caddy, no record, no marker, every hostname at the new address only, the
  old record's names carried, and one TTL waited;
* the deletes are exactly the old gateway's records at gateway hostnames; a
  monitor's, an endpoint's, a stale name's and another deployment's record are
  kept;
* the new gateway not answering stops at stage 2 with nothing created,
  deleted or stopped, and a re-run after the fix finishes;
* a record the move may not delete stops stage 4 with nothing deleted and the
  old gateway running;
* a failed delete resumes, and the wait starts after the last delete;
* the check failing at stage 6 keeps the old Caddy, and a resume does not wait
  again;
* a site file in `/srv/caddy.d` keeps the old Caddy until it is gone;
* a site that cannot be asked is an error; two former gateways are refused;
  records alone find the old gateway; no trace of one is nothing to move.

In `internal/dns`: `Desired` marking the gateway hostnames; `dns init`'s
`moving`; `BuildMove`'s deletes and kept records, its TTL, and that nothing is
deleted before the new records exist or while a kept record stands.

In `internal/hostprep`: a former gateway keeping 80 and 443 while its Caddy is
there and losing them after; a failed probe is an error.

In `cmd/paisans`: `apply` planning the move last only when a site holds a
former gateway, waiting on the new gateway's apply, and stopping when a site
cannot be asked.

Nothing here has been run against a real host or a real provider.
