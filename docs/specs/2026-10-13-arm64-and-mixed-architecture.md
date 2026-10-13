# arm64, and sites of different architectures

Date: 2026-10-13. Status: design approved by the founder in session,
2026-10-13; this written spec awaits review.

The toolkit installs onto amd64 hosts because nobody has tried anything else,
not because anything in it needs amd64. Every third party image it deploys is
published for linux/arm64 as well, and so are the toolkit's own (the invite
service, the uptime monitor, the admin reconciler, the gateway's Caddy and
writefreely-wisp). The tooling that assumed one platform, the volume table's
check and the label read behind `Version`, was made platform neutral first, in
its own change: both now read every linux platform of an image
(`imagedigest.Registry.Builds`), and the integration tests run natively on an
arm64 workstation.

What is left is the deployment itself: knowing each site's architecture,
refusing an image a site cannot run before anything is pulled, and refusing the
combinations of sites that copy Postgres's files byte for byte between machines
of different architectures.

## Scope

In:

* arm64 hosts, for every role.
* A deployment whose sites differ in architecture, wherever nothing copies a
  physical database between them.
* The checks that keep an unsupported combination from reaching a host.

Out, each for a stated reason:

* **Building or publishing any image.** An image without a linux/arm64 build
  is refused on an arm64 site, by name. The Mbin fork
  (`ghcr.io/paisans-software/mbin:1.15.1-paisans`) is published for
  linux/amd64 only, so an arm64 apps site cannot run Mbin until the fork
  publishes an arm64 build; that is the fork's work, and needs no change here
  once it lands.
* **Architectures other than amd64 and arm64.** The only host profile is
  Ubuntu 24.04, and these are the two the stack's images are published for.
  Anything else (armhf, riscv64, s390x) is refused by name.
* **Converting a deployment between architectures.** Replacing hardware is
  done the ways it is today (`site add` then `site remove` for a data site, a
  dump and restore for a pinned app), with the checks below saying which ways
  cross an architecture and which do not.

## What must share an architecture, and what may differ

Postgres's data directory is a machine-specific binary format: its pages, its
control file and its WAL are written in the host's byte order and alignment,
and its indexes in its collation. Every path that copies that directory, or the
WAL that rebuilds it, between machines needs the same architecture on both.

| Path | Copies | Rule |
|------|--------|------|
| Patroni replication between data sites, failover, `site add`'s `pg_basebackup` | the cluster's data directory and its WAL | every data site has the same architecture, whichever it is |
| Moving a pinned app (README, *Moving a pinned app*) | a tar of the stack directory, its Postgres data directory included | both hosts have the same architecture, or the move is a dump and restore |
| WAL-G restore of the cluster or a pinned database | a base backup and its WAL | the backup's architecture is the target site's |

Everything else may differ, because nothing in it is a physical copy of a
database:

* **An app on SQLite**, or pinned to its own site with its own Postgres, on
  any site. SQLite's file format is independent of the machine it was written
  on.
* **Pocket ID**, which keeps its data in the cluster through the local
  HAProxy, like any clustered app.
* **Garage**, which replicates objects over its own RPC.
* **etcd**, which replicates through Raft over the network.
* **The gateway, the monitor and the witness.** A gateway move copies nothing
  from the old gateway (docs/specs/2026-10-11-gateway-move.md).
* **A clustered app's apps sites**, among themselves and against the data
  sites: each runs its own containers against the cluster over the network.

## `sites.<name>.arch`

A new optional key on a site, `amd64` or `arm64`:

```yaml
sites:
  home-a:
    roles: [data, apps]
    address: 10.20.0.1
    arch: amd64        # written by the toolkit
```

**The toolkit writes it; an operator may delete it.** It records what the host
reported the first time the toolkit reached it, so that `validate` can check
the rules above without reaching any host, and so that a host replaced under
the same name by one of another architecture is noticed rather than joined.
An operator never needs to write it. Writing it by hand is accepted, and is
checked against the host like any recorded value.

The schema change is additive: a file without the key means what it meant
before. The decoder is strict, so a binary older than this change rejects a
file that carries the key; that is accepted.

### When it is written

**The architecture is read from the host**, with `dpkg --print-architecture`,
the same fact `host prepare` already reads for Docker's apt source
(`internal/hostprep/ubuntu.go`). The probe is shared rather than run twice.

**A whole `apply` probes before it prepares.** It asks every site for its
architecture, one site after another, before the host prepare stage. Every
site whose key is missing gets one, in a single edit of `paisans-stack.yaml`
that changes only the added lines, by the byte-level edit `config.SetIncludeDir`
and `config.SetMesh` use. Apply then reads the file again, as `init` does after
it writes, and only then prepares the hosts and publishes the signed copy.
Recording before publishing matters: each later `apply --site` refuses a file
whose bytes changed after it was published, so a key written after the
publish would make the next step refuse.

**`host prepare --site X` and `apply --site X`** record their own site's key
when it is missing, by the same edit. The next whole `apply` publishes it.

**`site add`** records the new site's key, from the same probe its preflight
uses.

Each write says what it did:

```
home-b: arch arm64 recorded in paisans-stack.yaml
```

The key is not added to config review's grants, which cover what lets someone
reach or change a host (login, keys, images, `include_dir`). An architecture
grants nothing. It is not added to the deployment record either: the file is
where it is read from.

### A recorded architecture the host contradicts

Every probe compares the host's answer with the recorded key. A difference
refuses the run before anything on that host changes:

```
home-b: paisans-stack.yaml records arch amd64, and the host reports arm64.
If this is new hardware, delete the arch line from home-b and run again.
```

The refusal protects a data site most: a host replaced by one of another
architecture would otherwise be prepared and then fail inside Patroni's
replication, or worse, be handed a restore it cannot read. Deleting the line
is the operator saying the change is meant; the run that follows records the
new value and applies every rule below to it.

**An architecture other than amd64 or arm64** is refused at the probe, naming
the site and what it reported, before anything is recorded:

```
home-c: the host reports armhf. The toolkit supports amd64 and arm64.
```

## The checks

### `validate`: the data sites share one architecture

`validate` reads the recorded keys and refuses a configuration whose data
sites (`cluster.sites`, which is every data site) record different
architectures, naming each with its value:

```
cluster.sites: home-a is amd64 and home-b is arm64. Patroni replicates the
data directory byte for byte, so every data site needs the same architecture.
```

A site with no recorded key is skipped, without a warning: a configuration
that has never been applied has none, and the first apply records them. The
same check runs again inside `apply`, after the probe has recorded every
missing key and before host prepare, so a key that was missing when
`validate` ran cannot carry a mixed cluster past it.

### `site add`: preflight compares architecture

Preflight's platform check (`osMatch`, `internal/preflight/preflight.go`)
compares a new data site with every existing data site on OS, version and
glibc release. It compares the architecture as well, from the same probe, and
refuses before `pg_basebackup`:

```
home-c: platform: arm64, but home-a runs amd64. A replica must match its
primary's architecture, or Postgres cannot read the data directory it copies.
```

### `apply`: every image has a build for its site

When apply resolves each site's images to their digests
(`internal/apply/images.go`, which already plans images per site), it reads the
platforms the resolved build was published for, through
`imagedigest.Registry.Builds` and its cache, and refuses an image that has no
`linux/<arch>` for the site it is planned on. This happens in the plan, before
any pull, on every site, so a dry run shows it.

A platform is matched on its architecture: an index entry `arm64/v8` is an
arm64 build. An image published as a single manifest rather than an index is
judged by the architecture in its configuration, which `Builds` already reads.

```
home-b (arm64): ghcr.io/paisans-software/mbin:1.15.1-paisans is published for linux/amd64 only.
```

A clustered app is planned on every apps site, so it needs a build for every
apps site's architecture; the refusal names each site it cannot run on. A
pinned app needs one for its own site. Nothing about Mbin is special: an arm64
site cannot run it today because this check says so, and can the day the fork
publishes an arm64 build.

## WAL-G backups record their architecture

An amendment to docs/specs/2026-10-11-backups.md, in its *Postgres* and
*Restore* sections.

**Each base backup records the architecture it was taken on**, beside the
metadata WAL-G already keeps for it: for the cluster, the data site Spilo took
it on; for a pinned database, its site.

**A restore compares it with the target site's recorded `arch`** and refuses a
difference before fetching anything, naming the backup, its architecture, the
site and the site's:

```
backup base_000000010000000000000042 was taken on amd64, and home-b is arm64.
A physical backup restores onto the architecture it was taken on; restore it
onto an amd64 site, then copy the data across with pg_dump.
```

**A backup with no recorded architecture** restores with a warning that its
architecture is unknown, rather than being refused, so a backup taken before
this change stays restorable.

Restoring one clustered app goes through a scratch instance and `pg_dump`, so
only the scratch instance's architecture must match the backup; the dump
crosses architectures freely. Restic's files and buckets are not affected.

## Moving a pinned app

The README's *Moving a pinned app* gains a first step: compare the two sites'
`arch`. The tar copies the app's Postgres data directory, which only a host of
the same architecture can read. Between architectures the move is a
`pg_dump` on the old site and a restore into the new one, and the app's files
travel as they do today. The move is a runbook, not a command, so this is a
warning in it, not a check.

## Documentation

* README: a short *Architectures* section under *Configuration*: amd64 and
  arm64 are supported; the toolkit records each site's `arch`; what must share
  one and what may differ; how to replace a host with one of another
  architecture (delete the line). The site key list gains `arch`.
* README, *Moving a pinned app*: the step above.
* README, *Adding a site*: the new site must share the data sites'
  architecture if it holds the data role.
* `docs/decisions.md`: one entry recording what is decided here.
* docs/specs/2026-10-11-backups.md: the amendment above, cross-referenced from
  this spec.

## Testing

Unit tests, each against a fake host or a fake registry:

* The probe: `dpkg --print-architecture` output parsed to `amd64` and `arm64`;
  any other value refused with the site's name.
* The write-back: a file missing one site's key gains exactly that line, and
  its other bytes are unchanged; a file with every key is not written.
* A whole apply records every missing key before host prepare and before the
  publish, and the published copy carries them.
* A recorded value the host contradicts refuses the run before anything on the
  host changes, and the message names both values and the line to delete.
* `validate` refuses data sites recording different architectures, and passes
  when one has no key.
* The same check refuses inside apply once the probe has filled a missing key.
* `osMatch` refuses a new data site of another architecture.
* The image check refuses an index without the site's platform, accepts an
  `arm64/v8` entry for an arm64 site, judges a single manifest by its
  configuration, and refuses a clustered app missing any apps site's platform.
* A WAL-G restore refuses a backup of another architecture, and warns on one
  with none recorded.

No new integration tag. The `registry_integration` volume test and the standby
integration test already run on both architectures.

## Phases

1. The `arch` key, the probe, the write-back and the mismatch refusal.
2. `validate`'s and apply's data site rule, and preflight's.
3. The image platform check in apply.
4. Documentation, including the pinned app move.
5. The WAL-G architecture record and restore check, with the backups work,
   since `postgres-walg` is not built yet.
