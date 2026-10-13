# The core catalog is fetched, not embedded

Date: 2026-10-13. Status: approved by the founder, 2026-10-13. Implemented
2026-10-13, in pull requests #250, #252, #253, #255, #256, #257, #258, #259
and #260, and Paisans-Software/paisans-blueprints#5; the work list below
was done in that order, with the test copy and `coretest` moving from the
first step to the third, where core is first read from the cache.
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
* **No core blueprint is named in Go** outside tests; see *Core blueprints are
  rules in the catalog, not names in Go*.

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

## Core blueprints are rules in the catalog, not names in Go

A fetched core blueprint can change with a new catalog commit, but Go code that
tests an app's name cannot. Every place outside tests that names `mbin`,
`outline`, `synapse`, `element` or `writefreely` goes, by founder decision,
2026-10-13, in one of the ways below.

**Rules become CEL checks in the blueprint.** The single app refusals and
warnings in `internal/validate` move to each blueprint's `checks`, under the
rule ids they have today, so an operator's output does not change:

| Rule | Blueprint |
|---|---|
| a bucket name Outline cannot handle, and one that is a substring of the media hostname | outline |
| a queue setting no backend is rendered for; RabbitMQ on more than one site | mbin |
| the mail settings `mbin_mail.go` checks | mbin |
| a federation setting that is not a real boolean | mbin, writefreely |
| a placement that makes the homeserver less available than its app | synapse |
| a WriteFreely numeric setting out of its range | writefreely |

Some of these read more than CEL sees today, so the environment gains
`app.name`, `app.sites` (the sites the app is placed on), `app.storage` (its
storage mode, after the Garage default, and its bucket names) and
`deployment.sites` (each site's name and roles), which the blueprints spec
lists but `internal/kinds/cel.go` does not yet declare. Each is part of the
helper contract.

**A check's message may be an expression.** Today's rules name what is wrong,
the bucket, the value, the number of sites, and a check's `message` is fixed
text. A check may give `message_expr` instead: a CEL expression over the same
variables, of type string, whose value is the message. A check may also give
`key_expr`, for a rule whose key depends on where a value came from, as the
mail rules' does (`smtp.from_address` or `apps.<name>.smtp.from_address`).

**Facts become fields the blueprint declares**, and the Go that tested a name
reads the field. Three already exist: `oidc.callback`, `oidc.pkce` and
`oidc.reads_groups` are parsed and declared by the core catalog, so the Go
beside them (`render/appview.go`'s callback paths, `kinds.MbinRedirectURI`,
`kinds.MASRedirectURI`, `secretsgen`'s reason by name) is deleted. New:

| Field | Replaces |
|---|---|
| `storage.local_only_reason` | why Synapse has no S3 form, in `validate/storage.go` |
| `services[].port` | the homeserver's authentication service, its second listener: published on the site's mesh address, given its own gateway upstreams and checked for collisions (`render/plan.go`, `appview.go`, `ports.go`, `site.go`) |
| `database.extra` | the authentication service's second database, `<database>_<name>`, which the blueprint's `initdb.d` creates |

**Template helpers are generic.** Catalog templates call helpers named for one
app today. Each becomes a helper of the contract that names none, or data the
blueprint already declares, and the core catalog's templates change with it:

| Today | Becomes |
|---|---|
| `MailerDSN` | `SMTPDSN`, the same Symfony DSN encoding |
| `MbinSenderEmail` | `SMTPSender`, with the fallback it has today |
| `MbinContactEmail` | `.Setting "contact_email" .SMTPSender` |
| `MbinLockedSettings`, `WriteFreelyLockedSettings` | `.LockedSettings "<separator>"`, read from the blueprint's `config.locked_settings`: each a name, or `{name, when}` with a CEL condition, which sees `app.oidc` |
| `MbinQueue` | `.Setting "queue" "postgres"` |
| `masUpstreamProviderID` | `stableULID`, a ULID derived from the app's name, so rendering stays deterministic |
| `.MASPort`, `.MASUpstreams`, `.MASDBName` | `index .ServicePorts "mas"`, `index .ServiceUpstreams "mas"`, `index .Databases "mas"` |

**Links between apps become a capability.** Element finds its homeserver, and
a homeserver's `server_name` depends on whether it delegates to a well known
host. A blueprint may now **provide** a named role and another may **consume**
it: synapse provides `matrix-homeserver`, with its `base_url` and
`server_name` worked out by the toolkit from the deployment as `appview.go`
does today, and element consumes it, naming the setting that holds its
provider's hostname (`consumes: [{role: matrix-homeserver, setting:
homeserver}]`). A template reads the consumed values as `.Linked
"matrix-homeserver" "server_name"`; a hostname no declared provider answers
on is taken as the provider's own. The role is named for what it is,
so a second homeserver blueprint provides the same one.

**Secrets say how they are made and what rotating them costs.** Value
formats, costs and the keys never rotated are chosen by key name in Go
today (`secretsgen`, `secretsrotate`, `cmd/paisans/secretsrotate.go`). Each
generated secret in a blueprint gains:

* `format`, one of a closed set the toolkit implements: `password`, `hex16`,
  `hex32`, `rsa-pem`, `rsa-keypair` for a private key encrypted under another
  of the app's secrets (its `passphrase`), and `rsa-public` for the public
  half `of` one.
* `rotation_cost`: `signs-out` or `restart`, which `secrets rotate` states
  before it asks.
* `never_rotate`, with the reason, for a key whose rotation would lose data,
  as Synapse's and Outline's encryption keys would.
* `group`, for keys that rotate together as one unit, as Mbin's OAuth
  passphrase, encryption key and keypair do.

**A rotation that needs the running app is an in-app action.** Only one does
today: Mbin's RabbitMQ password, which `rabbitmqctl` changes inside the
`rabbitmq` service before the stack is given the new value. Its blueprint
declares the step under the secret, `rotate`: the `service` to run in, the
`change` command, the `check` command that says the new value is accepted and
the `refused` output that means not yet, each with the value on stdin, and a
`when` condition with why the secret is `kept` when it does not hold, with the
boundary the blueprints spec sets for every in-app action (*Capabilities,
helpers and in-app actions*). The toolkit keeps the stages, the gates between
them and the order across sites.

**What resolves from the blueprint instead of a name:**

* **`app admin create`** refuses an app whose administrators come from an
  OIDC admin group, the ones whose blueprint sets `oidc.admin_group_setting`
  or `oidc.default_admin_group`, and points at the Pocket ID app. Only Mbin
  gets that message today.
* **The create wizard** keeps its menu in Go: which blueprint is offered as
  talk, docs, blog, chat and web, and which site each entry goes on when there
  are two. Everything else it asks of a blueprint, whether it can join the
  cluster and whether its files go to object storage when it does, it reads
  from the blueprint (`kinds.UsesPostgres`, the blueprint's `storage`) rather
  than from `clusterable` and `storesObjects`.

**The order across the two repositories.** The toolkit gains the CEL
variables, message expressions, the fields, the generic helpers, `.Linked` and
the secret fields first, while the Go rules still run. The core catalog then declares them, in a pull request to
`Paisans-Software/paisans-blueprints`, and the toolkit pins that commit. Only
then are the Go rules deleted, with the tests showing the same refusals and
warnings from the blueprints.

## The work

1. `pin.yaml` with per blueprint digests, `scripts/pin-core-catalog.sh`, and the
   test copy with `coretest`.
2. The partial clone and sparse checkout in `kinds`, for every catalog, with
   per blueprint marks and the lock.
3. Core read from the cache: `config.knownKinds` and `kinds` built in records
   reduced to the reserved four, `internal/blueprints/core/` emptied but for the
   pin, and core in the snapshot and the grant review.
4. In the toolkit, beside the Go rules, one pull request each: the CEL
   variables and message expressions; `services[].port`, `database.extra` and
   `storage.local_only_reason`; the generic helpers and `.Linked`; the secret
   fields and the in-app rotation step.
5. The core catalog declares them; the toolkit pins that commit and deletes
   the Go rules, the named helpers, `clusterable`, `storesObjects`, the Mbin
   admin refusal and the `config.Kind` constants for core blueprints.
6. README, `docs/commands.md`, `docs/development.md`, and the blueprints spec's
   status.

