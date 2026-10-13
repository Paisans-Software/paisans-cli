# Images follow tracks in the core catalog

Date: 2026-10-13. Status: approved in conversation by the founder,
2026-10-13; this written spec awaits review.
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

```yaml
apiVersion: paisans.foundation/v1
images:
  etcd:             {repository: gcr.io/etcd-development/etcd, tags: "v3.5.*"}
  haproxy:          {repository: docker.io/library/haproxy, tags: "3.0.*-alpine"}
  garage:           {repository: docker.io/dxflrs/garage, tags: "v1.*.*"}
  caddy:            {repository: ghcr.io/paisans-software/caddy, tags: "2.*.*"}
  heartbeat:        {repository: docker.io/curlimages/curl, tags: "8.*.*"}
  pocket-id:        {repository: ghcr.io/pocket-id/pocket-id, tags: "v2.*.*"}
  admin-reconciler: {repository: ghcr.io/paisans-software/admin-reconciler, tags: "0.1.*"}
  oauth2-proxy:     {repository: quay.io/oauth2-proxy/oauth2-proxy, tags: "v7.*.*"}
  uptime:           {repository: ghcr.io/paisans-software/uptime, tags: "1.1.*-oidc.*"}
  invite:           {repository: ghcr.io/paisans-software/paisans-invite, tags: "0.1.*"}
postgres:
  # Keyed by Postgres major. A major is data, so the deployment's
  # postgres_version chooses it and a track never moves it.
  "16": {spilo: {repository: ghcr.io/zalando/spilo-16, tags: "3.3-p*"},
         app:   {repository: ghcr.io/paisans-software/postgres-walg, tags: "16.*"}}
  "17": {spilo: {repository: ghcr.io/zalando/spilo-17, tags: "4.0-p*"},
         app:   {repository: ghcr.io/paisans-software/postgres-walg, tags: "17.*"}}
```

The values above illustrate the shape. The real tracks are written when the
file is, each checked against its registry's published tags.

A core blueprint's `services[].image` becomes the same kind of track:

```yaml
services:
  - {name: "app", image: {repository: ghcr.io/paisans-software/mbin, tags: "1.15.*-paisans"}, purpose: "..."}
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
4. Read each resolved build's configuration for every platform the
   deployment's sites run, with `Registry.Builds`. A build missing a platform a
   site needs is refused, as today.

Only `apply` and `init` move a deployment to newer builds. A command that
changes part of a running deployment, such as `site add` or `failover`,
renders the builds the deployment record says are running, because a new site
on a newer etcd or Spilo than the rest of its cluster is not a state anyone
tested.

The dry run reports each build that moves, through the image move report apply
already prints: `pocket-id v2.14.0 → v2.15.1`. The deployment record keeps the
resolved reference of every service, and the core catalog commit it came from.

An operator's own `apps.<name>.images` entry, or `acme.image`, is exact and
wins over the track. It is also how an operator holds a deployment on one
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
and which it skipped and why. An older binary therefore keeps working on the
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

Tests keep the committed copy in `internal/blueprints/testdata/core/`.
`scripts/fetch-core-testdata.sh <tag>` replaces the pin script and refreshes
it, so a test reads a known release and never the network.

## The volume check reads the registry

`ImageVolumes` and `RepositoryVolumes` go: no table keyed by a reference can
hold a reference that is chosen at apply time.

The `VOLUME` paths a build declares are already in the configuration step 4
of *Resolving a track* reads. Apply refuses, before it reaches a host, a
rendered compose file that does not mount each one, as the table's render test
did. The check on the host's own copy of each image stays, for a reference the
registry would not answer for.

`TestRecordedVolumesMatchEveryPlatform` goes with the table. Its registry
reading is already `Registry.Builds`, which apply now calls.

## When the registry or the forge does not answer

* **A running deployment.** A track that cannot be resolved keeps the build
  the deployment record says runs, and the plan warns, naming the registry and
  the error. A core catalog that cannot be fetched keeps the release the
  record names. Apply goes on, because a registry outage is not a reason to
  stop correcting everything else.
* **A deployment's first apply, or `init`.** There is no running build to
  keep, so apply refuses, naming what it could not reach.

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
3. **The volume check** reads the registry; the tables and their registry test
   go.
4. **Builds stay put outside apply.** `site add`, `failover` and the other
   commands that render part of a deployment read the running builds from the
   deployment record.
5. **The core catalog floats.** The pin goes, the newest readable release is
   chosen, `fetch-core-testdata.sh` replaces the pin script.
6. **paisans-blueprints.** `foundation.yaml`, tracks in every core blueprint,
   and the first release tag. This lands before step 5 merges, since step 5
   needs a release to choose.
7. **Docs.** README, `docs/commands.md`, `docs/development.md` and a decision
   record.
