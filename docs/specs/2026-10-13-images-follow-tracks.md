# Images follow tracks in the core catalog

Date: 2026-10-13. Status: approved in conversation by the founder,
2026-10-13. Implemented 2026-10-13; the sections below describe what was
built, which differs from the first draft in four places: a track is written
as one reference, the volume check stays on the host, a command narrower than
apply keeps what runs and resolves only what runs nowhere, and a workstation
that cannot reach the forge falls back on the release it last read.
Amends *What the binary carries* in `docs/specs/2026-10-13-core-catalog-fetched.md`.

Every image a deployment runs is fixed when the toolkit is built today. The
infrastructure template names etcd `v3.5.16`, HAProxy `3.0-alpine` and Garage
`v1.0.1`; the reserved blueprints embedded in the binary name Pocket ID
`v2.14.0` and the gate's oauth2-proxy; the core blueprints name their own
images, but the binary pins the core catalog by digest, so a new image there
still needs a toolkit release. Pocket ID, Postgres, Spilo and Caddy publish
security fixes on their own schedules, and each one reaches a deployment only
after a toolkit release, and after every operator notices it and upgrades.

From here, the binary names no image. The core catalog names a **track** for
each one, a repository and a tag pattern, and every `apply` and `init` runs the
newest release on each track at that moment. The core catalog itself follows
its newest release too, so changing a track needs no toolkit release either.

## Settled

Founder decisions, 2026-10-13, in session:

* **Tracks, resolved at apply time.** The catalog declares a track; apply asks
  the registry for the newest tag on it. Patch and minor releases reach a
  deployment on its next apply, with nobody editing anything.
* **The core catalog holds every track,** the toolkit's own services
  included. No separate repository: one place to bump, one release to tag.
* **The core catalog follows its newest release.** `pin.yaml` and its
  digests go.
* **Go keeps only what it is written against:** the major versions its code
  speaks to, such as Pocket ID's API and etcd's. A track outside them is
  refused.

## The foundation file

`foundation.yaml`, next to `catalog.yaml` at the core catalog's root, names a
track for every service whose template is the toolkit's own. Those services'
templates stay in the binary (the infrastructure stack, and the reserved
blueprints `pocket-id`, the gate, `invite` and `uptime`), so the file holds
images only.

A track is written as one image reference whose tag is the pattern, so it
reads like the reference it resolves to and every place that takes an image
takes a track:

```yaml
apiVersion: paisans.foundation/v1
images:
  etcd: "gcr.io/etcd-development/etcd:v3.5.*"
  haproxy: "docker.io/library/haproxy:3.0.*-alpine"
  garage: "docker.io/dxflrs/garage:v1.*.*"
  caddy: "ghcr.io/paisans-software/caddy:2.*.*"
  heartbeat: "docker.io/curlimages/curl:8.*.*"
  pocket-id: "ghcr.io/pocket-id/pocket-id:v2.*.*"
  oauth2-proxy: "quay.io/oauth2-proxy/oauth2-proxy:v7.*.*"
  admin-reconciler: "ghcr.io/paisans-software/admin-reconciler:0.1.*"
  uptime: "ghcr.io/paisans-software/uptime:1.1.*-oidc.*"
  invite: "ghcr.io/paisans-software/paisans-invite:0.1.*"
postgres:
  # Keyed by Postgres major. A major is data, so the deployment's
  # postgres_version chooses it and a track never moves it.
  "17":
    spilo: "ghcr.io/zalando/spilo-17:4.0-p*"
    app: "docker.io/library/postgres:17.*-trixie"
```

Every image is required, and a key the toolkit does not know is refused, as a
blueprint's is. A reserved blueprint's service names its image by its key here
(`foundation: pocket-id`) instead of an `image`.

A core blueprint's `services[].image` is a track the same way:

```yaml
services:
  - {name: "app", image: "ghcr.io/paisans-software/mbin:1.*.*-paisans", purpose: "..."}
```

A blueprint from any other catalog may write either a track or an exact
reference, so a third party catalog that pins its images keeps working.

## Resolving a track

A tag pattern is literal text in which each `*` matches one run of digits. A
tag matches when the whole tag matches; the newest is the one whose numbers,
read left to right, are highest. `3.0.*-alpine` matches `3.0.11-alpine` and
not `3.0.11-alpine3.20` or `3.1.0-alpine`, so a pattern says exactly how far a
track may move.

Resolution, once per plan:

1. List the repository's tags, every page, anonymously, through
   `internal/imagedigest`. One plan resolves each track once, so every site in
   it runs the same build.
2. Take the newest matching tag. A track with no matching tag is refused,
   naming the pattern and the newest tag the registry does have.
3. Turn it into a digest and render `repository:tag@sha256:...`, as the
   gateway's Caddy is rendered today. The digest is what a host pulls, so the
   build shown in the plan is the build that runs.
4. Check the resolved build's platforms against each site's, as apply checks
   every image today. A build missing a platform a site needs is refused.

Only `apply` and `init` move a deployment to newer builds. A command that
writes a compose file for a narrower reason, such as `site add` or `storage
add`, keeps whatever each stack runs on the host, as it does today, so it
never moves an image nobody planned. The one image it resolves is a track that
runs nowhere on that host yet, a new site's etcd in `site add` for one, since a
host cannot pull a pattern: it starts on the newest build on the track, which
for etcd and Spilo is a patch release at most from the rest of the cluster,
inside the line the track allows. A compose file still naming a track is never
written.

The dry run reports each build that moves, through the image move report apply
already prints: `pocket-id v2.14.0 → v2.15.1`. Each site's manifest keeps the
resolved reference of every service, and the deployment section of the run
names the core catalog release it read.

An operator's own `apps.<name>.images` entry, or `acme.image`, wins over the
track; it may be exact or a track of its own. It is also how an operator holds a deployment on one
build when a new release on a track misbehaves.

## What Go keeps

The toolkit's code is written against particular major versions of some of
these services: Pocket ID's REST API (`internal/pocketid` cites v2), etcd's
`etcdctl` (v3), Spilo's and Patroni's environment, and where Postgres keeps its
data (moved in 18, `PostgresDataMount`). For each, Go declares the tag patterns
it supports, and a foundation track that can match a tag outside them is
refused. A pattern whose first component is fixed (`v2.*.*`) sits inside a
supported major (`v2.*.*`); one that is not (`v*.*.*`) is refused, so a catalog
cannot move a deployment onto an API the binary has never spoken to.

The core blueprints' tracks need no such declaration. Nothing in Go is written
against Mbin or Outline, and each blueprint's own checks are its contract.

`PocketIDStandbyMarker`, today keyed by image reference, becomes one constant
for the supported Pocket ID major. A marker that stops matching still fails
safe, as its comment records.

## The core catalog follows its newest release

`internal/blueprints/core/pin.yaml`, `blueprints.CorePin`, the per blueprint
digests and `scripts/pin-core-catalog.sh` go. The binary carries the core
catalog's URL and nothing else about it.

A deployment that does not pin the core catalog in `catalogs` uses the newest
release of it that this binary can read: the newest semver tag, pre-releases
skipped, as `catalog add` already chooses for any catalog, whose blueprints
carry an `apiVersion` this binary reads, whose `foundation.yaml` does too, and
whose foundation tracks sit inside the majors Go supports. A newer release
that fails any of those is skipped, and the plan says which release it used
and which it skipped and why. At most five releases are tried, newest first. An older binary therefore keeps working on the
releases it understands instead of failing on one it does not.

A change an older binary cannot read changes the `apiVersion`, so that rule is
what makes the skip safe. A new key in a blueprint is such a change, because
blueprints are decoded strictly.

A deployment that pins the core catalog in `catalogs`, by tag and commit,
keeps that pin, and `catalog update` moves it, as it moves any catalog's. That
is how an operator holds the blueprints still while images keep following
their tracks.

`Paisans-Software/paisans-blueprints` starts tagging releases, `v1.0.0` first,
because a catalog with no release has nothing for this rule to choose.

Tests keep the committed copy in `internal/blueprints/testdata/core/`, and the
release it is in `internal/blueprints/testdata/core.release`.
`scripts/fetch-core-testdata.sh <tag>` replaces the pin script and refreshes
both. `internal/blueprints/coretest` makes the copy a local repository tagged
with that release and points `PAISANS_CORE_CATALOG`, which also names a
fork's own core catalog, at it, so a test takes the path a workstation takes
and never the network.

A configuration store version's snapshot carries the foundation beside the
blueprints, as `<commit>/foundation.yaml`, so a recovered version renders its
infrastructure with no forge.

## The volume check stays on the host

`ImageVolumes` and `RepositoryVolumes` go: no table keyed by a reference can
hold a reference that is chosen at apply time.

`apply` already inspects every image of every stack it acts on, on the host,
and refuses before anything is written when a declared `VOLUME` is not
mounted; an image the host does not have is pulled and inspected before the
first write. That check sees every build, whatever chose it, so it stays the
guard. `TestEveryDeclaredImageVolumeIsMounted`, behind the
`registry_integration` tag, now resolves every track and reads what its build
declares from the registry, so a catalog release that moves a track can be
checked before it ships. `TestRecordedVolumesMatchEveryPlatform` goes with the
table.

## When the registry or the forge does not answer

* **A running deployment.** A track that cannot be resolved keeps the build
  the host runs, when it is on the same track, and the plan warns, naming the
  registry and the error. A core catalog whose releases cannot be listed is
  read at the release this workstation last read, from its cache or an
  installed snapshot, and the run says so. Apply goes on, because a registry
  outage is not a reason to stop correcting everything else.
* **A deployment's first apply, or `init`.** There is no running build to
  keep, so apply refuses, naming what it could not reach. A workstation that
  never read the core catalog cannot read it offline either.

There is no copy of the catalog in the binary to fall back on. A host pulls
every image it runs from the same registries, so a deployment that cannot
reach them could not start anyway.

## Work list

1. **Tracks.** The pattern, its matching and its ordering, paginated tag
   listing in `internal/imagedigest`, and track resolution with the
   unreachable cases above. A blueprint's `image` reads a track or an exact
   reference.
2. **The foundation file.** Its format, reading it from the core catalog, the
   majors Go supports, and the infrastructure template and reserved
   blueprints taking their images from it. Caddy's `acme.Published` and
   `SpiloTag`'s table go.
3. **The volume tables go**, and the render test reads the registry.
4. **Builds stay put outside apply**, and a track that runs nowhere on a host
   is resolved for the command that starts it.
5. **The core catalog floats.** The pin goes, the newest readable release is
   chosen, `fetch-core-testdata.sh` replaces the pin script.
6. **paisans-blueprints.** `foundation.yaml`, tracks in every core blueprint,
   and the first release tag. This lands before step 5 merges, since step 5
   needs a release to choose.
7. **Docs.** README, `docs/commands.md`, `docs/development.md` and a decision
   record.
