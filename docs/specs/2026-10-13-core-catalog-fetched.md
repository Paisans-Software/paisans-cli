# The core catalog is fetched, not embedded

Date: 2026-10-13. Status: draft, for the founder's approval. Not implemented.
Amends *The core catalog* in `docs/specs/2026-10-12-blueprints-and-catalogs.md`.

The binary carries a copy of the core catalog today: `scripts/sync-core-catalog.sh`
copies one commit into `internal/blueprints/core/`, and `go:embed` builds it in.
A catalog that grows to many blueprints grows every binary with it, and every
workstation carries every blueprint whether its deployment runs it or not.

From here, the binary carries only the core catalog's **pin**, and fetches the
blueprints it needs into the per machine catalog cache that third party
catalogs already use. Each fetch takes the commit's trees and only the files of
the blueprints the configuration names.

## Settled

Founder decisions, 2026-10-13, in session:

* **Fetch less.** A catalog is fetched with a blobless partial clone of its
  pinned commit, and only the blueprints in use are checked out.
* **No offline support.** The toolkit reaches its deployment's servers over the
  internet, so it can reach the catalog's forge too.
* **Tests use a committed copy** of a known core commit, fetched by a script,
  not synthetic blueprints.
* **The reserved blueprints stay embedded:** `pocket-id`, the gate, `invite`
  and `uptime`.
* **One cache per machine**, the one third party catalogs already use.

## What the binary carries

`internal/blueprints/core/` holds only `pin.yaml`:

```yaml
# Written by scripts/pin-core-catalog.sh.
url: https://github.com/Paisans-Software/paisans-blueprints
commit: 35adcdddb539d6fb4770418f5d18e2e13494e456
blueprints:
  element: <sha256>
  mbin: <sha256>
  outline: <sha256>
  synapse: <sha256>
  writefreely: <sha256>
```

The digest is per blueprint, `blueprints.Digest` over that blueprint's
directory, so a fetch that checks out one blueprint can check it alone. The
binary's pin is still what makes a release reproducible: a given binary renders
one core commit, and every file it reads from that commit is checked against
the digest the release was tested with. That check matters more than it did:
with catalogs unsigned, it is what says the core files are the ones the
toolkit's maintainers released.

`scripts/sync-core-catalog.sh` becomes `scripts/pin-core-catalog.sh <commit>`.
It fetches the commit, computes each blueprint's digest and writes `pin.yaml`.
It no longer copies anything into the binary.

## The fetch

Every catalog, core and third party, is fetched the same way, into
`<cache>/<commit>/`, the directory `kinds.CacheDir` already names:

1. `git init`, then `git fetch --depth 1 --filter=blob:none origin <commit>`.
   This takes the commit and its trees, and no file contents.
2. `git sparse-checkout set --no-cone /catalog.yaml /blueprints/<name>/...` for
   the blueprints the configuration names, then a checkout of the commit, which
   fetches only those files.
3. A blueprint needed later, by another configuration on the same machine, is
   added to the same directory with `git sparse-checkout add` and a checkout.

Fetching a commit by its id is checked against GitHub: a blobless fetch of
`35adcdd` took 10 objects, and checking out `catalog.yaml` and `blueprints/mbin`
then took 9 more.

**What is complete is tracked per blueprint.** The single `.paisans-complete`
mark becomes one mark per checked out blueprint, written once its files are on
disk and its digest has been checked. A blueprint without its mark is checked
out again. Two processes adding blueprints to one directory take a lock file in
it first, since `git sparse-checkout` rewrites one file for the whole checkout.

**The names come from the trees.** `git ls-tree` on the fetched commit lists
`blueprints/` without fetching a file, so `blueprint list`, the create wizard
and the check that an unqualified name is a core blueprint all work from the
first step alone.

**A tag beside a pin** is checked as it is today, with `git ls-remote` against
the commit, since a depth 1 fetch of a commit brings no tags.

## What reads the core catalog

* **Loading a configuration** fetches the core blueprints its apps name, at the
  configuration's `catalogs.core` pin or the binary's. `config.knownKinds`
  loses its core names: an unqualified name is valid when it is reserved or is
  a directory under the core commit's `blueprints/`.
* **`kinds.For`** of a core blueprint reads it from the cache. The built in
  records `internal/kinds/def.go` loads at `init` are the reserved four only.
* **The configuration store's snapshot** carries core blueprints as it carries
  third party ones, keyed by the core commit. A recovery or a version checked
  out then needs no fetch for what it renders. The snapshot was skipped for
  core while the binary carried it.
* **The grant review** shows a core blueprint's commit and digest when the core
  pin moves, as it does for a third party catalog, since a new toolkit release
  now moves the core pin of every deployment that does not pin core itself.

## Tests

`internal/blueprints/testdata/core/` holds the blueprints of the core commit
the binary pins, written by `scripts/pin-core-catalog.sh` beside `pin.yaml`. It
is fetched, never written by hand, so the tests render the real blueprints.
`TestTheTestCopyMatchesThePin` checks each blueprint's digest against
`pin.yaml`, so a test copy that drifts from the pin fails rather than testing
something the binary will not fetch.

The copy lives in `testdata/`, which `go build` ignores, so it never reaches a
binary. A package `internal/blueprints/coretest`, imported only from test
files, installs it into a temporary catalog cache at its `init` and points
`PAISANS_CATALOG_CACHE` there. Each test package that reads a core blueprint
imports it once, in a `core_test.go`:

```go
package render_test

import _ "github.com/paisans-software/paisans-stack/internal/blueprints/coretest"
```

The tests then take the same fetch path a workstation does and find the
blueprints already in the cache, so no test reaches the network.

## The work

1. `pin.yaml` with per blueprint digests, `scripts/pin-core-catalog.sh`, and the
   test copy with `coretest`.
2. The partial clone and sparse checkout in `kinds`, for every catalog, with
   per blueprint marks and the lock.
3. Core read from the cache: `config.knownKinds` and `kinds` built in records
   reduced to the reserved four, `internal/blueprints/core/` emptied but for the
   pin, and core in the snapshot and the grant review.
4. README, `docs/commands.md`, `docs/development.md`, and the blueprints spec's
   status.

## Open

* **The Go code that still names core blueprints.** About forty places outside
  tests name `mbin`, `outline`, `synapse`, `element` or `writefreely`
  (`internal/validate`, `internal/create`, `internal/render/appview.go`,
  `internal/secretsgen`, `cmd/paisans/secretsrotate.go`). They keep working,
  since they compare names, but each is a rule the catalog cannot change. They
  are not moved by this spec.
