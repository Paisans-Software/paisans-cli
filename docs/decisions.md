# Toolkit decisions

One entry per settled decision about **this toolkit**. Newest last.

Decisions about a *community* — which software fills a capability, federation
policy, how that community governs itself — do not belong here. They belong in
that community's own records. This file is for choices that every adopter
inherits.

---

## 2026-09-02 — The toolkit is written in Go, as a single binary

**Decided.** Not Terraform, not Ansible, not shell.

### Why not Terraform

Terraform provisions cloud resources through provider APIs, converging a
dependency graph against a state file. This toolkit renders configuration files,
pushes them to Linux hosts, runs commands, and verifies results between stages.
Three mismatches:

* **A staged join is not a dependency graph.** "Verify the WireGuard handshake in
  both directions, and only then change etcd membership" is a procedure with a
  gate and a rollback. A graph expresses ordering, not verification-with-abort.
  The real logic would end up inside `remote-exec` provisioners, which upstream
  itself calls a last resort.
* **State becomes another artifact to keep consistent.** The backup contract
  already names four; a state file that must stay in step with the encrypted
  secrets would be a fifth, and the one that goes stale quietly.
* **Licence.** BUSL since 2023, which is an avoidable complication for something
  meant to be handed to people we have never met. OpenTofu exists if it is ever
  wanted.

Terraform *is* right for provisioning the cloud instance itself: create the
machine and its firewall rules. That should stay **optional**, because most
operators will click a button in a provider's console and requiring otherwise
raises the floor for no benefit.

DNS records used to sit in that list, left to the operator. They no longer do:
`paisans dns init` derives them from the configuration and creates them through
the DNS provider's API, because a record typed by hand is one nothing checks
against the configuration. Founder requirement. See *`dns init` creates the
records a deployment needs* in `README.md`.

### Why not Ansible

The closest fit of the alternatives, and a reasonable choice: agentless over SSH,
idempotent, good templating. Two things counted against it.

The staged operations need verification loops, conditional rollback and precise
failure messages, and those degrade quickly when expressed in YAML. And it puts
a working Python environment in front of every adopter, which is exactly the kind
of step where people stop.

### Why not shell

It matches what already exists and adds no dependency, which is a real argument.
But YAML parsing, templating and staged rollback in shell get bad fast, and this
toolkit's whole value is in the parts that would be worst.

### Why Go

**Distribution.** One static binary, no runtime, cross-compiled for macOS and
Linux. An operator downloads it and runs it. This is the same constraint that
ruled out Kubernetes: the floor for an adopter has to stay low, and every install
step is somewhere they can stop.

**The policy is the product.** Most of the design is refusals and warnings —
refuse a witness sharing a failure domain with a voter; refuse a two-voter
cluster; refuse to overwrite a locally edited `.env`; warn when pinning an app
onto the witness; verify tunnels before touching etcd membership. Each is a place
where a novice operator is caught before doing damage. That is program logic with
good error messages, not declarative configuration.

**sops and age are both Go**, so encryption and decryption can be embedded rather
than shelled out. That removes two installation steps from every workstation and
makes the secrets path harder to get wrong. *Verify the library surfaces before
relying on this.*

### What it is not

* **Not a daemon.** Every command is operator-invoked. Patroni and etcd are the
  only things that watch anything, deliberately.
* **Not an orchestrator.** It renders and pushes; Docker Compose runs things. The
  moment it starts scheduling, it is re-inventing what was already rejected.

### Consequences

* Contributors need a Go toolchain; operators need nothing.
* Templating uses the standard library, so no template engine is inherited.
* Anything shelled out to — `docker`, `wg`, `etcdctl`, `patronictl` — is a
  dependency on the *host*, and belongs in preflight.

---

## 2026-09-15 — This repository lives in the Paisans-Software organisation

**Decided.** `josephquigley/paisans-stack` became
`Paisans-Software/paisans-stack`, private, and the Go module path became
`github.com/paisans-software/paisans-stack`.

### Why

This toolkit is meant to be handed to organizers nobody here has met. A
repository under one person's account says the opposite of what the project
intends: that it is somebody's side project, that its continuity depends on one
account, and that an adopter is trusting an individual rather than a project.
An organisation can gain maintainers, survive a person losing interest, and own
the packages the toolkit tells adopters to pull.

The forks, `mbin-paisans` and `writefreely-wisp`, deliberately did not move.
Their audience is upstream maintainers who already recognise the account that
opens pull requests against them, and moving a fork under an organisation buys
nothing there while costing that recognition.

### Consequences

* GitHub redirects the old path, so open pull requests and existing clones
  survive. A redirect is a courtesy rather than a contract: a clone that
  predates the move has its remote updated rather than relying on one.
* The module path changed, which touched every import. It is renamed rather
  than left as a path that names a repository that no longer exists, because a
  module path that lies is worse than one that is inconvenient to change.
* `paisans.community`'s own `CLAUDE.md` names this repository as the one
  sanctioned fallback when its wiki is unreachable. That reference is part of a
  recovery path, so it is updated in the same change rather than left pointing
  at a redirect that a future outage would have to survive.
* Packages this project publishes now belong to the organisation, which is what
  makes `ghcr.io/paisans-software/...` a name an adopter can be asked to trust.

---

## 2026-09-15: The ACME DNS provider is declared, and its Caddy image is published

**Decided.** A deployment names its DNS provider in `acme.provider`, and pulls a
Caddy image with that provider's module compiled in from
`ghcr.io/paisans-software/caddy`. Nothing is built on a host.

### What this fixed as well as abstracted

The toolkit rendered upstream's `caddy:2-alpine` against a configuration saying
`acme_dns cloudflare`. A DNS provider in Caddy is a module compiled in with
xcaddy, and upstream's image carries none, so **the rendered gateway could not
have obtained a certificate at all.** Nobody had run it.

### Why not build on the gateway

It is what a deployment's own stack does, and it works anywhere with no registry
account. It was declined because the suggested topology puts the gateway role on
a small machine that is also the etcd witness, and etcd's stability is a
function of fsync latency. A compile there competes for exactly the resource
that placement was chosen around, and it repeats on every Caddy upgrade.

The cost is that a deployment's gateway now depends on our registry and on those
images being rebuilt. `acme.image` is what keeps that from being lock-in.

### Why one image with both modules

Switching provider is then a configuration edit with no image change, no rebuild
and no repull, which is the point of the abstraction. An adopter carries a module
they do not use, a few megabytes.

### Why the default is a digest

Superseded on 2026-10-11: the default now follows the major version tag and
`deployment apply` resolves it to a digest while it plans. See *The gateway's Caddy follows
its major version, resolved to a digest when apply plans*, below.

Every tag `caddy-dns` publishes moves, mirroring upstream, which rebuilds even a
patch tag when its base image gets a security fix. A digest is the only
reference that makes two runs of `deployment apply` deploy the same bytes.

### Why an unknown provider is allowed

A closed set would be consistent with how application kinds are treated, and it
would leave an adopter on another DNS provider waiting for us or forking.
Instead the provider is declared with an image that carries its module, and the
module is verified at apply time by asking the binary, since what is compiled
into a binary is not a property of its name.

---

## 2026-09-15: Synapse is a resource server and MAS owns authentication

**Decided.** The `synapse` kind renders three containers, not two: the
homeserver, Matrix Authentication Service in front of it, and a database. The
identity provider is upstream of MAS rather than of the homeserver.

### What was deleted

The kind used to render an `oidc_providers` block into `homeserver.yaml`, with
the `password_config` block that accompanied it. Both are gone, and so is
`registration_shared_secret`, which the kind never rendered but which any
`synapse generate` run produces and which an operator would otherwise be left
holding.

The old form is deleted rather than kept behind a setting. Two ways to
authenticate a homeserver is two ways to get it wrong, and the second one is
not a fallback: a deployment that kept both would have a second account
creation path that bypasses the group restriction at the identity provider,
which is the whole of the access policy for a Matrix stack. A homeserver
hostname cannot be gated, because a Matrix client is not a browser and will not
follow a redirect to a passkey prompt, so there is nothing else standing in
front of it.

### Where the shape came from

From a deployment that runs it, read on 2026-09-15: its compose file, its
gateway's host blocks and the design record written when it was built. That
deployment was verified on the wire at the time it was brought up, which is
more than the toolkit's previous shape could claim, since nobody had ever run
what the kind rendered.

Nothing of that community crossed into this repository. What was taken is which
services exist, which configuration keys they carry and what order the route
matchers go in. Every value here is the fixture's example domain or a template
variable.

### The stable block, not the experimental one

The task brief asked for `experimental_features.msc3861`. Synapse v1.160.0, the
version this kind pins, documents `matrix_authentication_service` instead, and
its configuration manual at that tag no longer mentions msc3861 anywhere. The
stable block is rendered and the test asserts it. Pinning the toolkit to the
experimental key would pin it to a shape the image it pins has moved past.

### The matcher lengths in the gateway snippet are the load bearing part

`/_matrix/client/*/login`, `/logout` and `/refresh` are split off to MAS ahead
of the `/_matrix/*` catch all. What puts them ahead is their length, not their
position: Caddy sorts routes of the same directive by path matcher length,
longest first with a trailing `*` trimmed, before it ever evaluates one
(`caddyconfig/httpcaddyfile/directives.go`, `sortRoutes`, read at v2.11.4, the
version this toolkit pins). A `handle` with no path matcher sorts last, which
is what makes the bare one MAS's fallback.

An earlier version of this record said Caddy evaluates `handle` blocks in the
order written and that reversing them would send every login to the homeserver.
That is wrong, and the counterexample was already in the rendered file: the
`/.well-known/matrix/*` handle is written after both catch alls and still wins.
The snippet is written in the order it sorts into so that reading it top to
bottom matches what happens, and nothing more rests on that.

The real hazard is a matcher rather than a move: a new handle under `/_matrix`
longer than one of the login paths and overlapping it would capture those
requests. So the test asserts the property that governs, which is that each
split path outranks the catch all on length. It cannot assert what Caddy then
does with the sorted routes, because the rendered text is all it has, and its
name says so.

### The apex gets a snippet of its own

The hostname holding the `wellknown` role serves `/.well-known/matrix/server`
and `/.well-known/matrix/client` and nothing else. Routing it like the primary
would publish the entire homeserver API on the name inside every user
identifier. That name is also what `server_name` is set to when the role is
declared, because delegation documents served on a name the homeserver does not
answer to point nowhere.

### One role, two databases

MAS keeps its own database beside the homeserver's, owned by the same role.
They cannot share one, because both services define a `users` table. The
deployment this was taken from gives MAS a role of its own; the toolkit does
not, because one app holding one credential is its rule everywhere else and a
second role per app would be a change to the secrets model rather than to this
kind.

## 2026-09-30: Garage provisioning is a separate verb, keyed one per app, and the facts behind it came from running Garage rather than reading about it

### Provisioning is not apply

`deployment apply` renders files and reconciles them against a manifest it wrote itself,
so every check it makes is "does this file match what was rendered". Garage's
cluster layout, its S3 keys and its buckets are not files. They are state
inside a running service that nothing on the workstation describes, reached
only by asking Garage itself. That needed a different kind of planner, so it
is a separate command, `paisans storage init --site <name>`, rather than a
step folded into `deployment apply`: check what a live command reports, and plan only
what is missing, in the order Garage requires.

### One S3 key per app, not one shared key

Each object storing app gets its own Garage key, generated at `init` and
never reused, and that key is granted read/write/owner on that app's bucket
and nothing else. A shared key was the alternative and it was rejected
outright: one key handed to every app would mean any single app's `.env`,
leaked or simply read by an operator debugging it, was enough to read,
rewrite and delete every other app's objects, not just its own. Per app keys
make that blast radius one app wide instead of the whole deployment, and the
integration test added here asserts the isolation directly: a key is allowed
on its own bucket and refused on another app's.

### The media hostname is one per deployment, not one per app

`storage.media_hostname` is a single hostname, and a bucket is a path under
it, rather than a hostname per app. An app's uploads still write through a
mesh address that only this deployment's nodes can reach; the media hostname
is the one address a browser, and a federating server that will never join
the mesh, is ever told about. One hostname is also the only shape that survives
a second Garage site being added later without renaming anything a remote
server has already cached.

### `Host` is forwarded unchanged, because Outline presigns

The gateway's media snippet passes the `Host` header through unrewritten.
Outline's uploads are a presigned POST that the browser submits directly
against the media hostname, and the signature Outline computed covers the
host it signed for. Rewriting `Host` to the upstream address would make every
presigned URL Outline issues fail to verify. Mbin never presigns anything
(it writes to Garage itself, server side, over the mesh, and only the URL it
publishes is the media hostname), so this rule exists for Outline's sake, not
both apps' equally. `docs/specs/2026-09-30-garage-provisioning.md` and its
plan previously said uploads "stay off the gateway" as a blanket claim; that
was true only for Mbin; Outline's uploads cross the gateway by design, and
both documents were corrected to say so.

### The formats, the ordering, and which commands are idempotent all came from running the image

None of the following came from Garage's documentation. All of it came from
running `dxflrs/garage:v1.0.1` by hand, once, while this was built, and again,
automated, in `internal/garage/integration_test.go`:

* an S3 access key ID is the literal `GK` followed by 24 lowercase hex
  characters, and a secret key is 64 lowercase hex characters; anything else
  is refused at `key import` time with a message naming the shape;
* a fresh node's layout must be assigned and applied before any key import or
  bucket command will succeed; before that, Garage answers with "could not
  reach quorum";
* `key import` and `bucket create` both fail loudly when the target already
  exists, which is what makes "was it missing" the right question for a
  planner to ask before running either; `bucket allow` and `bucket website
  --allow` are idempotent, and were planned on every run until real `bucket
  info` output had been seen. It prints `Website access: true` and an
  `Authorized keys:` section of `RWO  <key ID>  <key name>` rows, possibly
  after ANSI coloured log lines, so each step is now planned only when the
  website flag is not `true` or the app's key (by ID or name) lacks one of R,
  W and O. Output that does not parse plans both and the plan says so: a
  repeated set costs nothing, a missed grant leaves the app unable to write;
* `garage layout show` prints only the first 16 hex characters of a node's ID
  in its table rows, while `node id -q` prints the full one. The integration
  test caught a real bug this produced: matching the full ID against the
  whole combined command output never found a row that was already there,
  so every run replanned the layout step. Fixing the match to use the short
  prefix surfaced a second bug underneath it: `layout show`'s own RPC client
  logs "Connection established to \<node ID\>" for the local node it talks to,
  before it prints the table, and on a single node cluster that is always
  this node's own ID, present or not. Matching against the whole output made
  every fresh node look already provisioned. The fix narrows the match to the
  table between the `==== CURRENT CLUSTER LAYOUT ====` banner and the version
  line, which is the only part of the output that is actually about the
  layout.

The property that matters most and that no unit test can reach is that the
key ID the toolkit generates is the key ID Garage actually holds: if those
ever drift, every upload from that app fails with an opaque signature error
and nothing earlier in the sequence would say a word. The integration test
asserts it by reading the key back out of Garage with `garage key info` and
comparing it against the secrets file, not by comparing the toolkit against
itself. It also proves idempotency against the real thing: a second `Build`
after provisioning plans no `key import`, no `bucket create` and no layout
step. It is behind the `garage_integration` build tag, so the ordinary test
suite needs no Docker, and it is where this knowledge is kept honest against
whatever Garage ships next.

## 2026-10-04: A public bucket is served by Garage's web endpoint, and the gateway rewrites rather than republishes

### The S3 API has no anonymous mode at all

Garage's S3 API refuses every unauthenticated request, with
`403 Forbidden: Garage does not support anonymous access yet`, and there is no
setting, policy or grant that changes it. That is not a gap to be configured
around: a bucket policy is not the mechanism here, because the API has no
anonymous path to apply one to.

Garage's separate web endpoint is the only one that serves an object without a
credential, so the toolkit renders it as `[s3_web]` in `garage.toml` and runs
`garage bucket website --allow` on the buckets that need it. That endpoint
resolves a bucket from the request's `Host` rather than from the path, which
is what makes the gateway's job a rewrite rather than a proxy.

This is why any of it exists: a federating server fetching an image is a
machine with no account. It will not sign a request, and it will not follow a
redirect to a passkey prompt. Outline's reads are different only because
Outline presigns them for a person who is already signed in.

### The gateway rewrites, because remote instances have cached Mbin's URLs

The media hostname carries one route per public bucket. The bucket prefix is
stripped from the path and `Host` is rewritten to the vhost form the web
endpoint expects. The published URL does not change at all.

Publishing the vhost form instead was the alternative and it was rejected.
Mbin's `KBIN_STORAGE_URL` is baked into every media URL it has ever emitted,
and remote instances have recorded those URLs; nothing can recall them. A
change to the published form would break every image already federated, which
makes the published URL a one-way door for the same reason the domain is. A
rewrite at the gateway is reversible and costs one route per bucket.

This narrows, rather than contradicts, the earlier decision that `Host` is
forwarded unchanged. Everything that is not a public bucket still falls
through to the S3 API with `Host` untouched, and that is still load bearing
for exactly the reason given there: a SigV4 signature covers the host it was
signed with, so rewriting the header on the fallback would invalidate every
presigned URL Outline issues. The public bucket routes rewrite it because the
requests they carry are not signed with anything.

### Public is derived from the kind, and the declared field was rejected

`kinds.ServesObjectsPublicly` answers which kinds need this, and there is no
configuration key for it. A `public: true` field on an app was considered and
rejected: it can be set wrong in both directions, and both are bad in ways an
operator would not see. Set on Outline it would expose a private bucket to
anyone who can guess a key, with nothing failing to say so. Left off Mbin it
would silently break federated images, which looks like a remote instance's
problem rather than a local setting.

It is also derived from the kind rather than from "has a bucket", because a
kind can keep objects in S3 and serve them through its own application route,
never exposing the bucket. A future kind in that shape must not acquire a
public route just for having a bucket.

### The internal suffix resolves nowhere, on purpose

`root_domain` under `[s3_web]` is `.web.garage.internal`. It is not a real
name and there is no DNS record and no certificate for it anywhere. The web
endpoint needs a `Host` suffix to resolve a bucket from, and the gateway is
the only thing that ever sends one. A suffix under the community's own domain
would have been a name someone could point at something, and a name that
resolves is a name that can be reached; this one cannot be, from inside the
mesh or outside it.

### What keeps all of it honest

`TestAnonymousFetchReadsMbinsMediaAndNotOutlines` in
`internal/garage/integration_test.go`, behind the `garage_integration` build
tag. It starts both of the fixture's Garage nodes from their own rendered
`garage.toml`, a Caddy from the rendered `media.caddy` snippet and the
rendered image pin, provisions them through the real `garage.Build` and
`garage.Execute`, uploads an object into each bucket with the rendered
credential and a hand signed SigV4 request, and then makes three fetches
through the gateway with `Host` set to the media hostname:

* Mbin's object with no credential of any kind returns `200` and the object's
  bytes;
* Outline's object with no credential is refused with `403`, and the absence
  of the object's bytes is asserted alongside the status, because a `200`
  carrying an error document would pass a status check;
* Outline's object presigned returns `200` and the bytes, which is the
  property the fallback route exists for and the one most likely to break now
  that the routing has a branch.

Two facts about the rendered configuration fell out of running it rather than
reading it. The fixture declares replication 2, and `dxflrs/garage:v1.0.1`
refuses to apply a layout whose node count is below the replication factor, so
the rendered `garage.toml` cannot be provisioned on one node at all and the
test runs two. The per node `Build` and `Execute` reflect that: the first
site's layout apply is refused while only its own role is staged, the staged
role survives the refusal, and the second site's pass applies both at once.
Nothing in `garage.Build` stages both nodes before applying, and nothing in it
joins a node to a cluster either. The test does that join itself, with `garage
node connect`, and that is a step the toolkit never plans: nothing it renders
carries `bootstrap_peers`, Consul discovery or Kubernetes discovery, and a
shared `rpc_secret` authenticates a peer rather than finding one. Two nodes
started from the fixture's own rendered `garage.toml` files, on the mesh
subnet, with that shared secret and no `node connect`, each list only
themselves in `garage status`.

The consequence is operational rather than theoretical. **A multi site Garage
deployment needs a manual `garage node connect` before `paisans storage init`
can converge.** Without it each node is alone, so both sites' `layout apply`
is refused for a node count below the replication factor, and `Execute` stops
at the first failing step: no key, no bucket and no website grant is ever
created. This is written down in `README.md`, in the `storage init` section,
because a decision record is not where an operator looks for an instruction.
Fixing the planner so it plans the join is deliberately out of scope here.

## 2026-10-04: The `writefreely` kind now means the writefreely-wisp fork, not upstream

### This is a breaking change for a public toolkit

`writefreely` used to mean upstream WriteFreely, which has no Postgres driver
and no S3 support: it kept its data in a SQLite file in its own data
directory, and that was the whole reason the kind could only be pinned. The
kind now renders the [writefreely-wisp](https://github.com/josephquigley/writefreely-wisp)
fork instead, which adds both. Its `config.ini` carries `[database] type =
postgres` and `[storage] type = s3`, so the same `paisans-stack.yaml` that used
to render a SQLite file now renders a Postgres connection and an S3 bucket,
and the blog can join the cluster like any other app.

Nobody using this toolkit asked for that, and there is no way to opt out of
it short of pinning an older release of the toolkit itself: the `writefreely`
name did not change meaning for a new deployment only, it changed meaning for
every existing one the next time `paisans deployment apply` runs. That makes it a
breaking change, and because `paisans-stack` is public and handed to people
we do not know, it is documented in the open, in `README.md`, in the section
listing what each kind means, rather than left for an adopter to discover
from a container that will not start.

### A second kind for the fork was the rejected alternative

Adding `writefreely-wisp` as its own kind, leaving `writefreely` alone, was
considered. It was rejected because the two are not a stack and a variant of
a stack in the way, say, Mbin and a possible future fork of it would be: the
wisp fork is the only WriteFreely this toolkit can run correctly going
forward, since upstream's image actively rejects the configuration an
operator would otherwise want (Postgres and S3, instead of a SQLite file one
node owns). Keeping both names would mean the toolkit ships a kind, `writefreely`,
that is known to be the wrong choice for anyone who can use the other one,
with no way for validation to say so. One name that means the better thing is
simpler to maintain and to document than two names where one is a trap.

### The image is an unreleased `develop` build, pinned by digest

No tagged release of the fork carries Postgres or S3 support yet; both exist
only on its `develop` branch. The catalogue pins
`ghcr.io/josephquigley/writefreely-wisp@sha256:4d21f45879bd98c8485eb8169ea57fbab925f0cbd5a38ac3c3bdd79901d809ea`
rather than a tag, because `develop` is a moving branch and a tag that tracked
it would silently change what an existing deployment runs on its next
`deployment apply`. The digest was resolved against ghcr.io on 2026-10-04 with `docker
manifest inspect`, which returned a multi architecture index covering
linux/amd64 and linux/arm64, and confirmed by pulling the amd64 manifest and
checking the binary with `strings` for the `s3_secret_access_key` ini tag and
the Postgres `sslmode` field. The pin should move to a tagged release's
digest once one exists, and the comment in `internal/kinds/kinds.go` says so.

### The fork serves its own images, so its bucket stays private

The fork's `imagestore.go` streams uploads through its own `/uploads/` route
with `http.ServeContent`, from whichever store is configured, and never
emits or presigns an S3 URL. That means nothing anonymous ever needs to reach
the bucket directly, unlike Mbin's: `kinds.ServesObjectsPublicly` answers
false for `writefreely`, so the blog's bucket gets no `garage bucket website
--allow` and no route on the media hostname. The rendered `config.ini` says
this in a comment, next to `[storage]`, because that is where the next
person reading it will be standing and wondering why the blog is absent from
the media routing everything else gets.

## 2026-10-04: A deployment can pass a config key through, and the toolkit places it without interpreting it

The design is `docs/specs/2026-10-04-config-passthrough.md`. This records what
was decided and what running it changed.

### `settings` only reaches keys a template already reads

Every value in `settings` works because some template asks for it by name. A
key no template reads is silently ignored, so an adopter who wanted
WriteFreely's `app.max_blogs` or one more variable in Mbin's environment had
two options: fork the templates, or edit the rendered file on the host and have
the next `deployment apply` refuse the whole stack over a local modification. `config` is
a second per app map for exactly that gap. The toolkit places each key in the
file the kind renders, in that file's syntax, and has no opinion about it.
`settings` keeps meaning what it meant.

### The merge runs in Go after rendering, not as a trailing block in each template

A block appended by each template was the alternative, and it fails three ways:
it cannot nest a yaml path, it cannot put an ini key inside a section that
already exists, and it cannot notice that the template already wrote the same
key. All three matter, the last most, because noticing is what makes the
collision refusal below possible. Env and ini are edited as lines rather than
parsed and re-emitted, because a full round trip would reformat a file an
operator reads.

### Ini inserts into the existing section rather than repeating it

Appending a second `[app]` at the end would have been simpler. Whether a
repeated section merges with the first or shadows it is a property of the
particular parser, and this project has been wrong about a format's behaviour
more than once by reasoning instead of running. go-ini v1.67.0, the version the
fork pins, does merge a repeated section, but inserting after the last
assignment of the section that is already there needs no assumption about it
at all, so the design does not lean on that. The key lands after the section's
last assignment rather than before the next header, so the comment that
introduces the next section stays with it.

The values go-ini would misread are refused rather than escaped, each observed
by loading the writer's own output with that library: `;` or `#` anywhere, edge
whitespace, a leading quote or backtick, a newline, and a trailing backslash,
which joins the next line onto the value and makes the key after it vanish.
`DEFAULT` is refused as a section because go-ini merges it into the lines
before any header. A section name with a dot, `[oauth.generic]`, cannot be
reached, because a key names exactly one section and one key.

### The ini insertion was run against the fork, and that found the fork's own limit

`internal/render/writefreely_integration_test.go`, behind the
`writefreely_integration` tag, boots the pinned fork image against the
`config.ini` that `render.Build` produces from the fixture, with a Postgres
reachable under the rendered `host` and carrying the rendered credentials, and
asks `writefreely settings get app.max_blogs`. It reports `3`. The same file
less only the inserted line reports `0`, which is what makes the first result
evidence rather than a constant. No line of the rendered file was changed for
the boot: the S3 endpoint is unreachable in the test and the fork logs that and
starts without uploads, and it starts without reaching the identity provider.

It has its own tag rather than `garage_integration` because it touches no
Garage and its helpers belong beside the fixture in `internal/render`.

The run also showed something the spec did not anticipate. The fork moves most
of `[app]` and `[uploads]` into its database on the first start against an empty
database, and after that the database is in force. Started once more against
that existing database with the key added, the fork logged `config.ini
disagrees with the database on app.max_blogs; the database value is in force`
and still reported `0`. The test asserts that as well, because the README
states it and a fork release that changed it should fail here rather than leave
the README wrong. A passthrough key for those sections therefore takes effect
on a new blog only. That restart is the only one observed.

What follows was read from the fork's source at `ff9dcebe`, the revision the
pinned image's label names, and was not run. `importSettings` in
`settings_runtime.go` is commented as running "at every start", so the
disagreement line is expected on every boot rather than only the one seen.
`cmd/writefreely/settings.go` has `writefreely settings set <name> <value>`,
described as "running servers apply it on their next request"; it looks like
the way to change a running blog's setting, and nobody has used it here. The
drift check covers every database-bound key present in the file, which reads
as though the template's own keys in those sections would be ignored the same
way after the first start; that is untested too. This toolkit does not try to
reconcile the two.

On the boots that were run, the rendered compose file mounts `config.ini` read
only, and the fork logged that it could not add its `settings_location` marker
to the file or strip the moved keys from it. The fork's `docker-entrypoint.sh`
says "A read-only config.ini is fine: the keys are logged and ignored", which
is the fork's own claim and was not tested further here. It was left alone.

### Yaml appends a new top level key, and re-emits only under an existing mapping

`yaml.v3`'s node API keeps comments through a parse and re-emit, but not the
blank lines between top level keys. So a key whose top level name is absent
from the rendered `homeserver.yaml` is marshalled as its own block and appended,
and the rendered text is left byte for byte as it was. Only a key under a
mapping the template already writes, such as `database.args.sslmode`, makes the
file be re-emitted. Measured on the golden `homeserver.yaml`, that drops six
blank lines and nothing else: every comment, the indentation, the quoting and
the flow sequences survive. Restoring the blank lines safely needs care around
block scalars and the loss is cosmetic, so it was not attempted. The common
Synapse keys an adopter reaches for are new top level names and take the byte
identical path.

A dot in yaml or json is always a path separator, so a new key whose own name
contains one, such as Element's `setting_defaults` key `UIFeature.feedback`,
cannot be expressed. A path that could mean an existing dotted key, such as one
under `default_server_config.m.homeserver`, is refused as ambiguous rather than
silently creating a nested `m` object beside it.

### A collision is refused, not overridden

`config-key-already-rendered` stops the build when the template already writes
the key, and names the `settings` key that owns the value when there is one.
Overriding was the alternative and was rejected for two reasons. Two sources of
truth for one value is how a deployment ends up with a setting nobody can
locate. And a passthrough that could override would route around decisions the
templates encode: `database.type = sqlite3` into a blog that belongs in the
cluster is refused because the template writes `type = postgres`. The cost is
real and stated in the README: a template owned default cannot be changed
through `config`, only by a `settings` key or a template change, both of which
are reviewable.

### A key a template leaves out on purpose is refused the same way

A collision check alone does not stop `oidc_providers` going back into a
homeserver that is supposed to be a resource server, because the synapse
template deliberately writes no identity provider and no password database:
there is nothing to collide with. The final review found that
`oidc_providers`, `oidc_config.enabled` and `cas_config.enabled` all rendered.

So a key a template omits by decision is treated as owned by that template and
refused under the same rule name, with a message saying the template leaves it
out on purpose and why. One rule name keeps the README to one entry. The
entries are limited to keys the template's own comments name as deliberately
absent, so the table records decisions rather than guesses:

* synapse: `oidc_providers`, `oidc_config`, `saml2_config`, `cas_config`,
  `jwt_config` and `password_config`. The template's header says it
  "deliberately declares no identity provider of its own and no local password
  database"; these six are Synapse's blocks for those, read in its
  configuration manual at v1.160.0. The match is on the top level segment, so
  `oidc_config.enabled` is caught and `oidc_providers_note` is not.
* writefreely: `uploads.dir`, which the template's `[uploads]` comment calls
  "deliberately absent" because uploads go to S3.

No other template names a deliberately absent key, so no other kind has
entries. `registration_shared_secret`, which the synapse kind also never
renders, is not in the table because the template's comments do not name it;
the credential name check refuses it in `validate` before rendering.

For env kinds the check covers the stack's `compose.yaml` as well. Docker
Compose v5.4.0 was run on the operator's Mac, with the version on any host not
checked, with the same variable in `env_file` and in `environment:`, in both
map and list form, and `docker compose config` showed `environment:` winning
and the `env_file` value dropped without a warning. So a
variable `compose.yaml` sets under `environment:`, or interpolates as `${VAR}`,
is refused too. The interpolation case over-refuses on purpose: the only
references today are `${POSTGRES_PASSWORD:?...}`, which the credential check
already refuses.

### The secret check is by name, and says so

`config-key-looks-like-a-secret` lowercases a key, removes `_`, `-` and `.`,
and refuses it if what is left contains `password`, `secret`, `token`, `apikey`
or `privatekey`, because `paisans-stack.yaml` is plaintext and committed and a
passthrough map is where a token gets pasted at the end of a long day. The
separators are removed because the first version matched `apikey` and
`private_key` as written, and the final review found `SENDGRID_API_KEY` and
`privateKey` passing; the env spelling is the likeliest real case. It will not
catch a credential called anything else, and the message and the README both
say that, the same way `acme-image-is-stock-caddy` refuses the one thing it can
prove instead of claiming to be a policy. It also refuses some innocent names,
such as WriteFreely's `app.disable_password_auth`, and the README says so.

### An env key that would configure compose is refused

`deployment apply` runs `docker compose -f /srv/<app>/compose.yaml up -d`, so the stack's
`.env` is also the file compose reads for its own settings. With `docker compose
config` on Docker Compose v5.4.0, on the operator's Mac and with no container
started, `COMPOSE_PROJECT_NAME=other` in that `.env` renamed the project from
`probe` to `other`, and `COMPOSE_PROFILES=hidden` made a service behind that
profile active. The run was made from another directory with `-f`, as `deployment apply`
does. That `deployment apply` would then address different containers from the ones it
started before is reasoned from the renamed project, not run.

`config-key-steers-compose` refuses an env kind's key starting `COMPOSE_` or
`DOCKER_`. `DOCKER_HOST` in the same `.env` did not redirect `docker compose
ps` in that run, while the same variable in the shell did. `DOCKER_` is refused
anyway as the docker CLI's own namespace, so the rule does not depend on
knowing which of those compose honours from a file; no application variable is
known to need that prefix.

### The refusal for a kind with no config file was dropped

The spec called for refusing `config` on a kind that renders no config file.
Every kind in the catalogue renders one, so the refusal had nothing to refuse,
and a rule no input can reach cannot be shown to fail. It
was left out rather than kept for a kind that does not exist. A future kind
without a config file has to add it back.

### What is proven, and against what

The ini merge is proven against the application that reads it, above. The env
merge's quoting is proven against `docker compose config`, below. The yaml and json
merges are proven against Go's own parsers only: the tests show the rendered
file parses and carries the value at the requested path. Synapse reads
`homeserver.yaml` with Python and Element reads `config.json` in a browser, and
nothing here runs either. That is a known gap, not a claim. One instance of it
was found by reading rather than running: Synapse 1.160.0's
`synapse/config/repository.py` raises a `ConfigError` for `url_preview_enabled`
unless a list valued `url_preview_ip_range_blacklist` is also set, and a list is
not a value `config` can carry, which is why the fixture uses
`require_auth_for_profile_requests` instead.

### Env values are quoted only where compose would rewrite them

Run with `docker compose config` on Docker Compose v5.4.0, on the operator's
Mac; the version on any host was not checked, and no container was started.
`TestEnvQuotesWhatComposeWouldOtherwiseRewrite` pins the line forms that run
chose. What compose did to a hand written `env_file` line:

| Line in the file | What compose delivered |
|---|---|
| `K=a b` | `a b` |
| `K=a #b` | `a`: a space then `#` starts a comment |
| `K=a#b` | `a#b` |
| `K=$HOME/x` | the value of `$HOME`, then `/x` |
| `K="quoted"` | `quoted`: the quotes are stripped |
| `K=trail  ` and `K= lead` | `trail` and `lead`: edge whitespace is trimmed |
| `K=a\nb` unquoted | the four characters `a\nb` |
| `K='...'` | literal, for a space, `#`, `$`, `"` and backslash |
| `K="a\nb"` | `a`, a real newline, `b` |
| `K="a\$b"`, `K="a\\b"`, `K="say \"hi\""` | `a$b`, `a\b`, `say "hi"` |

So a value is written bare, the templates' style, when it has no leading or
trailing whitespace and none of `#`, `$`, `'`, `"`, backslash, tab or newline.
Otherwise it is double quoted, with backslash, `"`, `$` and newline escaped,
the four escapes compose undid inside double quotes. A carriage return or other
control character is refused, since no form was observed to carry one.

A file written by `configmerge.Env` itself was then read back the same way,
with 24 values covering each of those cases plus empty, numbers, booleans,
`=`, a URL with `#` and `&`, and non-ASCII text. Every value compose delivered
matched the Go input, after undoing the `$$` compose prints for a literal `$`
in its output. Reading `$$` as that escape is an inference from the output
being consistently doubled, not documented behaviour that was looked up.

## 2026-10-04: Each app's objects are served on a media hostname of its own

Founder decision. The design is the amendment *one media hostname per app* in
`docs/specs/2026-10-02-serving-public-objects.md`. This supersedes two earlier
entries: *The media hostname is one per deployment, not one per app*, under
the Garage provisioning decision, and the per bucket routing under *A public
bucket is served by Garage's web endpoint*.

### A hostname can move and a path cannot

Each app that stores objects gets `<label>-media.<domain>`, a sibling of its
own hostname, and can name another under `hostnames.media`. A hostname is the
unit DNS re-points: one app's media can move to a CDN, another provider or
another Garage without touching any other app. A bucket as a path under one
shared hostname could only move with all of them. Two hostnames are also two
browser origins, so one app's user uploads cannot script against another's.

The single hostname was kept before because remote instances would have cached
URLs in that form. Checked rather than assumed: `git tag` lists nothing, and
`origin/main` carries none of the media work, so nothing has been released and
no deployment has published a URL in either shape. There was nothing to keep
working, so the old hostname was removed rather than kept as a legacy route.

### A sibling, not a child, and not a backend name

`media.talk.example.org` was rejected because a cookie the app scopes to its
own host reaches every name under it. `garage.` and `s3.` were rejected
because they name what answers rather than whose objects these are, and what
answers is exactly the part that is meant to change. Both rules are refusals
in `validate`, alongside a hostname shape check, a declared name outside the
domain, and a derived name colliding with any other hostname.

The config key is a role in the existing `hostnames` map rather than a new
field, because a role is already what selects a hostname's snippet and what
the duplicate check walks. It differs from `wellknown` in one way: it exists
whether or not it is declared.

### Public is per kind, and the blog joined Mbin

writefreely-wisp#171 makes the fork's S3 storage direct only: it requires
`[storage] image_url_base` and never serves an image from the bucket itself.
`kinds.ServesObjectsPublicly` is therefore true for `writefreely` as well as
`mbin`, and the earlier entry *The fork serves its own images, so its bucket
stays private* no longer describes the fork. Outline stays private, behind its
own media hostname on the S3 API with `Host` unchanged.

The pinned fork image predates #171 and ignores `image_url_base`, which was
observed rather than assumed: `TestRenderedPassthroughKeyIsReadByWriteFreely`
boots that image against the rendered `config.ini`, key included. Until the pin
moves, the blog streams its own images and its media hostname carries nothing.

### No gate, and headers on the public ones

Media hostnames are public to anyone with the link and never gated, including
for an app whose own hostname is gated, because ActivityPub servers hotlink the
original URLs. Public media hostnames send `Content-Security-Policy:
default-src 'none'; style-src 'unsafe-inline'; sandbox` and
`X-Content-Type-Options: nosniff`, because the apps store SVGs byte for byte
and no S3 provider can store a header on an object. Outline's sends nosniff
only: its PDF previews are an `<embed>` of the attachment, and the sandbox
would stop the browser's PDF viewer. That consequence is reasoned, not
observed in a browser.

### Outline's bucket name is checked against its media URL

Outline v1.10.0 treats the bucket name appearing anywhere in
`AWS_S3_UPLOAD_BUCKET_URL` as virtual host addressing, by substring, and then
drops the bucket from the upload path while Garage needs it there. A hostname
per app makes that reachable (bucket `docs` on `docs-media.example.org`), so it
is refused as `outline-bucket-in-media-url`. Read from Outline's source, not
run against Outline.

### What was run

`TestEachAppsMediaHostnameServesOnlyItsOwnBucket`, behind the
`garage_integration` tag, against two `dxflrs/garage:v1.0.1` nodes from their
rendered `garage.toml` and a Caddy running the three rendered media snippets.
Mbin's and the blog's objects returned 200 anonymously with exactly one copy of
each header; Outline's returned 403 anonymously and 200 presigned for
`docs-media.example.org`; Outline's key asked for on `talk-media.example.org`
returned 404. With a `header_up Host` added to Outline's snippet, the presigned
fetch failed with Garage's `Invalid signature`, so the test can fail.

The Caddy was `ghcr.io/paisans-software/caddy:2.11.4`, through the
`PAISANS_TEST_CADDY_IMAGE` override this change adds, not the rendered 2.11.6
pin: that image is in a registry that needs a login the workstation did not
have. The snippets use only stock directives, none from the modules that image
adds, but it was not the pinned bytes.

---

## 2026-10-08: The admin reconciler adds Pocket ID administrators to `admins` on its own

Founder decision. The design is `docs/specs/2026-10-08-admin-reconciler.md`.

### What was decided

The `pocket-id` kind runs an admin reconciler beside every Pocket ID instance. Every
two hours it adds each enabled, non LDAP Pocket ID administrator who is not in
the `admins` group to it, and its `/healthz` fails while `admins` has fewer
than two members. It is always on, in every deployment, and the group is
always named `admins`.

### Why an unattended write is acceptable here

An agent needs a human's approval for every Pocket ID group change. The
reconciler is not an agent but a service the deployment runs, and is approved
once, as a whole, under *Services the deployment runs* in
`docs/deployment-agent-rules.md`, added in the same change. It qualifies
because Pocket ID administrators are the community's administrators: putting
them in `admins` changes nobody's power, only which apps recognise it. The
approval is exactly that write. The reconciler never removes a member, never
changes a user and never touches a group, its code is the only thing that
keeps it so (Pocket ID's API keys carry no scopes), its tests fail on any
other request that writes, and it logs every write. It gives an agent no
authority to make the same change by hand.

### What was run

`TestRealPocketIDAdministratorIsAdded`, behind the `pocketid_integration` tag,
against `ghcr.io/pocket-id/pocket-id:v2.14.0`: one administrator in `admins`,
one in `editors` only. One pass left the second in both groups, reported two
members, and left the static API key's user, an administrator with the fixed
ID `00000000-0000-0000-0000-000000000000`, out of the group and out of the
count. The standby wrapper's integration tests were run with the shared marker
added: present on standby, gone after a stop.

The image `ghcr.io/paisans-software/admin-reconciler:0.1.0` was built locally from
`cmd/admin-reconciler/Dockerfile`, then published by tagging `admin-reconciler-v0.1.0`.
The kind names it by that release's digest.


---

## 2026-10-08: Services the deployment runs are approved once, not write by write

Founder decision. The rule is *Services the deployment runs* in
`docs/deployment-agent-rules.md`.

### What was decided

The blast radius tiers govern actions an agent takes. A service the deployment
runs that makes a change those tiers gate, such as the admin reconciler's
write to `admins`, is approved once, as a whole, when a human approves its
design. That approval stands while every kind of write it makes is named in
its design and a decision record, its tests fail on any other write, and it
logs every write. Adding or widening a write is a new decision, in the tier
of the strictest write it would make.

### Why

The context differs. An agent decides at run time, from whatever it was told,
so each gated change needs a human at that moment. A service decides nothing:
what it may change is fixed in reviewed code before it runs, and the same
change happens the same way every time. Asking for approval per write would
put a human in front of a decision that was already made, and would make an
unattended service impossible.

The service grants an agent nothing. An agent making the same change by hand,
with the service's credential, or by changing what the service reads, is an
agent's action and is in the tier of the change itself.

---

## 2026-10-08: The visibility gate gates everything but what authenticates itself

Founder decision. The design is `docs/specs/2026-10-08-visibility-gate.md`.

### What was decided

An app's `visibility_gate` is `public`, `member` or `provisional`, replacing
`gate`. On a gated hostname every request goes to the gate except ActivityPub
reads, whose answers Caddy filters, ActivityPub deliveries to the kind's
inboxes, the kind's open paths, and its token paths carrying a bearer token.
What each kind needs past the gate, and how it auto-logs a member in, is
recorded in `internal/kinds` with a citation into the pinned tag's source.

A gated app that federates refuses every unsigned ActivityPub read and reads
only from instances on its allow list. That changes who can read the instance,
which is why it is the founder's decision and not a rendering detail.

### Why the gate is inverted

The gate used to match browser navigations only, which is the shape the
deployment it was modelled on settled on as a convenience boundary. It left
every page readable to any client that did not ask for HTML. The founder asked
for an app to be readable only by people signed in, and a gate a scraper walks
past does not do that.

### Why the ActivityPub answers are filtered

Anyone can ask for ActivityPub, and an app can answer that request with
something else. Read off Mbin v1.14.0+paisans: it picks a route by the first
acceptable type, a route with no ActivityPub form serves HTML whatever is asked
for, and its API read endpoints answer anonymous callers. Its signed fetch
checks only its ActivityPub routes. So Caddy passes back an ActivityPub,
JSON-LD or JRD success, turns any other success into an empty 404, and keeps
only the status of anything else. Deliveries are POSTs, which change state
before an answer could be filtered, so they reach the inboxes and nothing else.

### What was run

`caddy adapt` on the rendered gateway Caddyfile, with every gateable kind
gated, in `ghcr.io/paisans-software/caddy:2.11.7`. The filter and the
matchers were run against a stub upstream in the same image: an ActivityPub or
JRD answer passed through, HTML and JSON answers became empty 404s, a mixed
`Accept: text/html, application/activity+json` too, a 401 kept its status and
a 302 its `Location` with no body; `/u/*/inbox` matched one segment and not
two; `Authorization: Bearer` matched and `Basic` did not.

The rendered gateway configuration itself, every gateable kind gated, was then
run in the same image with its upstreams pointed at a stub app and a stub gate.
An anonymous page, and `curl` with no `Accept`, met the gate's redirect with
its marker body; an ActivityPub `Accept` on an HTML or API route got an empty
404 and on an actor its document; a `HEAD` and a POST off the inboxes met the
gate; a member reached the page, a provisional user the pending page, a forged
`X-Auth-Request-*` header was stripped, and Outline's API took a bearer token
but not `Basic`, with `/auth/email` behind the gate.

### What is still open

Mbin's token paths are empty, so its native apps meet the gate after signing
in, until its API is shown not to serve anonymous reads to a token minted
without a member. Mbin and WriteFreely keep `/` as their health route, so a
gated one has no public check through the edge, until the pinned images carry
the forks' `/healthz`.

---

## 2026-10-08: The admin reconciler removes members of `admins` who are no longer administrators, keeping one

Founder decision. The design is `docs/specs/2026-10-08-admin-reconciler.md`.

### What was decided

The admin reconciler's write set grows from one write to two. Besides adding
each enabled, non LDAP Pocket ID administrator to `admins`, every pass removes
each member of `admins` who is disabled or is not a Pocket ID administrator,
but only when the removal leaves at least one enabled member in the group.
LDAP managed users and the static API key's user are written in neither
direction. The approval under *Services the deployment runs* now covers
exactly these two writes.

### Why

Without removal, a Pocket ID administrator who was demoted or disabled kept
the apps' administration (Mbin reads `admins` through `OAUTH_OIDC_ADMIN_GROUP`,
the uptime monitor through its admin group) for as long as the group kept
them, and nothing but a human's group edit took it away. Pocket ID
administrators are the community's administrators; the group is meant to
equal them, in both directions.

### What was weighed

* **The floor is one enabled member, not one member.** A disabled member
  cannot sign in to any app, so a group whose only remaining member is
  disabled is as locked out as an empty one. The floor therefore counts the
  same members the health count does: enabled, the static key's user
  excluded. A group whose members are all disabled is left as it is.
* **Which member is kept when all are due.** No signal in Pocket ID says
  which former administrator is the better one to keep, so the choice is
  made deterministic rather than clever: removals go in username order and
  the last stays, the same one on every pass, named in the log and in
  `/healthz`. The group is unhealthy in that state anyway, with one member
  and no administrator to add, so the monitor raises an incident.
* **Adds before removals.** A swap of the group's only member for a new
  administrator then never passes through an empty group.
* **A failed add holds removals.** The group may be short of the
  administrator that add was for; the next pass starts over.
* **A partial read removes nobody.** The user list is paged and the client
  fails the whole read when any page fails, so a pass acts on the whole list
  or not at all. A user the list silently misses only makes the pass more
  cautious: an administrator missed is one fewer counted towards the floor,
  and a member missed is one not removed until the next pass.
* **LDAP managed users are untouched in both directions**, because the add
  rule leaves them out for a reason that applies equally to removal: the
  sync owns their groups, and a reconciler removing what a sync adds would
  fight it every pass. A reading of "mirror the add rule" that removed LDAP
  members was considered and set aside for that reason.
* **`paisans app admin create` does not conflict.** It creates the account
  as a Pocket ID administrator, or grants admin to an existing one, before it
  joins the admin groups, so every user it puts in `admins` is one the
  reconciler keeps there.

### What was run

The unit tests in `internal/adminreconciler`, with the write path check
extended to the two shapes and their rules, and a check that no removal
leaves the group without an enabled member. `TestRealPocketIDAdministratorIsAdded`,
behind the `pocketid_integration` tag, against `ghcr.io/pocket-id/pocket-id:v2.14.0`:
after the add, bob was demoted through `PUT /api/users/:id`, a second pass
removed him from `admins` and left him in `editors`, and reported one member.

---

## 2026-10-09: Every watched site pushes a heartbeat to each monitor

Founder decision. The design is the amendment of 2026-10-09 in
`docs/specs/2026-10-07-uptime-monitoring.md`.

### What was decided

The toolkit runs one scheduled job per site: a `heartbeat` service in the
site's infrastructure stack that once a minute requests
`https://<monitor hostname>/ping/<token>` for every monitor off the site,
over the internet and never the mesh. Each monitor's seed expects it
(`<site> — heartbeat`, interval 60, grace 120, threshold 2) with the site's
token, `sites.<site>.heartbeat_token`, generated at `init`. The uptime fork
takes the seeded token from `1.1.0-oidc.4`, which the kind now pins. The
spec's statement that the toolkit seeds no heartbeat because it runs no
scheduled job is superseded.

### Why

The ping over the mesh is one path, and a torn tunnel, a site whose line is
down, the monitor's own tunnel broken and a dead host all fail it the same
way. A second path that fails independently is what tells them apart, and
the reverse direction over the public internet is that path: ping down with
the heartbeat fresh is the mesh; ping up with the heartbeat stale is the
site's WAN; every ping down with every heartbeat fresh is the monitor's
tunnel; both down is the host.

### What was weighed

* **A service in the stack, not a systemd timer.** It is rendered, applied,
  reconciled and removed with everything else, leaves with the site, and
  needs nothing on the host outside Docker. The image is curl's own, pinned
  by tag like the stack's other images, around a rendered shell loop.
* **Grace 120.** The fork's staleness is interval plus grace, so 180 s: one
  push missed and the next late. A push that fails outright takes about 33 s
  and the loop sleeps beside its pushes, so the cadence holds at one minute
  whatever a slow or dead monitor does; 60 would be reached by one missed
  push plus that delay.
* **Threshold 2**, shared with every other check. The fork's watchdog runs
  every 15 s and counts a stale heartbeat towards the threshold like any
  failure, so it costs one tick.
* **One token per site, shared across monitors.** The fork keeps tokens
  unique within one instance only, so every monitor can hold the same one,
  and a host's env file carries one URL per monitor with the one token.
* **The monitor's own site pushes only to another monitor.** A push from the
  host a monitor runs on can only ever arrive. With one monitor its site
  pushes nowhere and a role-less monitor site still renders no
  infrastructure stack; with two, each monitor site pushes to the other and
  each seed expects the other's site, which is what notices a monitor's host
  dying.
* **The token is refused when missing or malformed.** The fork silently
  replaces a token it does not accept, which would leave the host pushing at
  a URL the monitor answers 404 to and reads as the site being down. `render`
  refuses by name and sends the operator to `init` instead.
* **The token stays out of the 0644 compose file.** The URLs carry it, so
  they live in a 0600 `heartbeat.env` read through `env_file`, and the
  script logs a failure's time and never its URL.
* **A `heartbeat.env` change engages no gateway gate.** `up -d` replaces only
  the service whose configuration changed, so a new monitor's URL reaching
  the gateway site recreates the heartbeat and never Caddy.
* **Nothing is opened in the firewall.** The push is outbound HTTPS, which
  the host allows by default and federation already relies on.
* **No migration.** A secrets file from before this renders nothing until
  `init` is re-run, which fills exactly the missing tokens, as it does for
  any site added later.

### What was run

`go build ./... && go vet ./... && go test ./...`, with the golden tree
regenerated and its diff read: the `heartbeat` service and its two files on
every watched site, three heartbeat entries in the monitor's seed, and the
image pin. `paisans validate` on `examples/paisans-stack.example.yaml`: 0 refusals.
Nothing was run against a host or the registry; the fork release and curl's
image tag are to be checked against their registries before this ships.

## 2026-10-11: Every published name says whether it has mail, and prune removes the no-mail records

Founder decision. The design is `docs/specs/2026-10-11-dns-mail-lockdown.md`.

### What was decided

`dns init` publishes the records a DNS provider recommends for a domain that
sends no mail: `v=spf1 -all` on `community.domain` and on every name it gives
an address record, and on the domain a null MX, an empty key at
`*._domainkey` and a DMARC policy of reject for the domain and every name
under it. A name with mail of its own gets none of them: one listed in
`community.mail_domains`, or the domain of any `from_address` the toolkit
configures. When the domain itself has mail, its DMARC policy is checked and
warned about, never written. `dns prune` may delete a TXT or MX record that
holds exactly one of those no-mail contents, which widens a delete path that
was address records only.

Amended the same day, founder decision: `dns init`, and so `deployment apply`, refuses
while a no-mail record this deployment made is still at a name that now has
mail, naming `dns prune --execute` as the fix, so the prune cannot be skipped
silently before a mail service's records are added.

### Why

A name with an address record can be forged as a sender whether or not anyone
sends from it. The domain's DMARC `sp=reject` covers every hostname for
receivers that enforce DMARC; the SPF record on each hostname covers
receivers that check SPF alone, and SPF applies only to the exact name. The
toolkit already knows every name and every sender it configures, so deriving
the records costs an operator nothing and cannot lock out the toolkit's own
mail. Prune has to be able to remove the no-mail records, because a
community that adopts hosted mail at its domain would otherwise be left with
two SPF records and a null MX, and its mail would fail.

### What was weighed

* **A list, not a switch.** `community.mail: external` would cover the common
  case of hosted mail at the domain, but not a domain with mail at the apex
  and a mailing list service on a subdomain. The list is the same length in
  the common case.
* **Hosted mail at the domain still locks down the hostnames.** Being entirely
  hands off once mail is declared would leave every hostname forgeable to
  receivers that check SPF alone.
* **DMARC is compared by effect.** Exact comparison would make a record an
  operator gave a reporting address a conflict on every run.
* **Prune ties a no-mail record by its exact content, not its address.** These
  records point at nothing, and a TXT or MX that says anything but "no mail"
  is somebody's mail, whatever its comment says.

### What was run

`go build ./... && go vet ./... && go test ./...`; the tests in
`internal/dns/mail_test.go` were checked to fail with `isLockdown` and the
DKIM comparison broken. `paisans validate` on `examples/paisans-stack.example.yaml`:
0 refusals. Nothing was run against a DNS provider.

## 2026-10-11: Mbin reads the deployment's mail settings, and the configuration owns them

Founder decision. The design is `docs/specs/2026-10-11-mbin-smtp.md`.

### What was decided

The `mbin` kind sends mail (`kinds.SendsMail`). Its `.env` renders
`MAILER_DSN` from the app's effective `smtp` settings, `smtps://` for `tls`
and `smtp://...?require_tls=true` for `starttls`, or `null://null` with a
warning (`mbin-without-smtp`) when no host resolves; `KBIN_SENDER_EMAIL` from
`from_address`; and `KBIN_CONTACT_EMAIL` from a new `settings.contact_email`,
falling back to the sender. `from_name` is not used, since Mbin always names
the sender after its domain.

Added the same day, founder decision: the `.env` also renders
`MBIN_LOCKED_SETTINGS`, naming every Mbin database setting the template sets,
so the paisans fork reads those from the environment on every read and an
admin cannot change them in Mbin; and a gated app renders
`MBIN_PRIVATE_INSTANCE=true`. Both are rendered ahead of a pinned image that
reads them, since an image without the feature ignores the variable.

### Why

Without these, Mbin ran on its image's placeholders: a transport naming a
host that does not exist, so a contact form message failed rather than being
sent, and addresses at `mbin.domain.tld`. The toolkit already resolves mail
per app for two kinds, so a third is the same code path. `require_tls` keeps
a stripped STARTTLS from sending the password in plaintext. The null
transport keeps a deployment without mail working, which is why its absence
is a warning. Locking the settings closes the drift where an admin's first
save freezes every rendered value in the database.

### What was weighed

* **The DSN in the template or in Go.** In Go, so the encoding of a password
  a provider issued can be tested; the template only places it.
* **Percent encoding the userinfo with Go's `url.UserPassword` or in full.**
  In full: `url.UserPassword` leaves RFC 3986 sub-delimiters such as `$`, `'`
  and `+` as they are, and full encoding takes PHP's `parse_url` out of the
  question entirely. Both decode the same through `rawurldecode`.
* **No transport at all without SMTP.** Leaving `MAILER_DSN` out falls back to
  the image's `smtp://mailserver`, which fails every send.
* **The sender with no `from_address`.** `noreply@<hostname>` rather than a
  refusal: harmless under the null transport, and a warning
  (`mbin-smtp-without-sender`) when a host resolves, because `dns init` says
  that name sends no mail.
* **Which settings to lock.** Every `SettingsDto` field the template renders,
  which adds `KBIN_DOMAIN` to the ones named in session, so that a test can
  derive the list rather than trust it. Passthrough keys are the operator's
  and stay unlocked.

### What was run

`go build ./... && go vet ./... && go test ./...`, `gofmt -l .` empty. The
DSN, rendering, locked list, validate and SendsMail tests were each checked to
fail with the behaviour they cover removed. The rendered `.env` was passed as
an `env_file` by Docker Compose v5.4.0 to PHP 8.4 with symfony/mailer
v7.4.14, which parsed a password holding `@ : / ? # % ' " $` and a space back
to the same bytes. Nothing was run against a mail server or a deployment.

---

## 2026-10-11: The gateway's Caddy follows its major version, resolved to a digest when apply plans

Founder decision.

### What was decided

The toolkit's default Caddy image is `ghcr.io/paisans-software/caddy:2`: the
repository and Caddy's major version, with no digest in the code. `paisans
apply` asks the registry, while it plans, which build that tag names, and
renders `ghcr.io/paisans-software/caddy:2@sha256:...` into the gateway's
compose file. Each apply records the build in the site's manifest, under
`caddy`, with the tag it came from and when it was resolved. `acme.image`
still wins, and may now name a moving tag too, since it is resolved the same
way; `latest` and an untagged reference stay refused, renamed
`acme-image-is-latest`. Application images keep the floating tag refusal they
had.

### Why

Caddy's image is rebuilt continuously for CVE fixes in Caddy and its base
image. With the digest pinned in code, every rebuild was a toolkit pull
request and a toolkit release before any deployment could take it. Following
the major version takes a rebuild with no release, and keeps a major version,
which can change the Caddyfile syntax, a deliberate step.

A bare tag in the compose file would not do it alone: `deployment apply` recreates a
stack only when its compose file changes, so a tag that never changes in the
file would never bring a new build to a host. Resolving at plan time makes a
new build a changed compose file, which the dry run shows and the apply
carries through the module check, the configuration validation and the health
gate, the same as any image change. It also keeps what a host runs exact and
recorded, which is what the digest pin was for.

### How

* The resolution is a lookup on the registry's HTTP API from the
  workstation, anonymous, reading the `Docker-Content-Digest` of the tag's
  manifest (`internal/imagedigest`). For a multi platform image that is the
  index digest, which is what `docker pull` records as the image's RepoDigest
  on the host; the two were compared for the published image on 2026-10-11 and
  matched. It needs nothing installed on a host, and a dry run that resolves
  changes nothing anywhere.
* `render` stays offline and renders the tag as configured, listing it per
  site in `Plan.CaddyImages`. `apply.Build` pins it with `ResolveImages`
  before comparing the file with the host's copy.
* When the registry cannot be reached, the plan keeps the build the site
  already runs, with a warning. A site running none is refused, since a tag
  written as it is would never move again.
* A command that rewrites a gateway's compose file for another reason
  (`site remove`, `site add`, `storage add`) passes no resolver, and keeps the
  build the site runs, so it never moves Caddy to a build nobody planned.
* The volume table records the image by repository, `kinds.RepositoryVolumes`,
  since no one reference to it stays fixed; apply's own inspection of the
  exact build on the host is unchanged.

### What follows elsewhere

`Paisans-Software/caddy-dns`'s `propose` job edits the digest in
`internal/acme/acme.go`. The reference there is now composed from a
repository constant, so the job's pattern finds nothing to replace and it
opens no pull request. The job is that repository's to retire.

### What was run

`go build ./... && go vet ./... && go test ./...`, `gofmt -l .` empty. The
resolver was run against ghcr.io and Docker Hub, and its digest for
`ghcr.io/paisans-software/caddy:2` matched the RepoDigest `docker pull`
recorded for the same tag. Nothing was run against a deployment.

## 2026-10-11: A site is one machine

**Decided.** Founder decision.

### What was decided

A site of a deployment is one machine, and a machine runs at most one site of
any one deployment. Two deployments may still share a machine, as before. The
toolkit refuses a second site of a deployment on a machine before anything is
written to it, and `validate` refuses the configurations that are certainly
one machine.

### Why

Everything a deployment owns on a host is named for the deployment and
nothing narrower: `/srv/paisans/<token>/`, its manifest, its compose projects
`paisans-<token>-<stack>`, its WireGuard interface `psns-<token>` and
`/etc/wireguard/psns-<token>.conf`, and its one entry in the host's registry.
Two sites of one deployment on one machine collide on every one of them. No
case needs them there: roles combine on one site, and a second environment is
a second deployment, which the registry keeps apart from the first on any
machine they share.

### How

* The host registry's claim, which every command that writes to a host makes
  first, with its dry run reading the same, refuses a host already running
  another site of this deployment and names that site and its mesh address.
  The site is read from this deployment's registry entry, which names it, so
  a site whose address changed in `paisans-stack.yaml` is still recognised as
  itself; on a host with no entry, from the `Address` in the deployment's
  WireGuard config. The awk merge refuses it again under the lock.
* `validate` refuses two sites with one mesh address
  (`duplicate-site-address`), and two reached at one ssh host and port or
  dialled at one endpoint (`sites-share-a-machine`). A public address shared
  by sites behind one router is accepted, since each is reached on ports of
  its own.
* `deployment apply` without `--site` prepares and pulls every site at once with no
  ordering between sites, and an apply's image sweep keeps only what its own
  site renders.

### What was run

`go build ./...`, `go vet ./...`, `go test ./... -count=1`, `-race` on the
changed packages, and `gofmt -l .` empty. Nothing was run against a host.

## 2026-10-11: Docker is judged by its version, not its source

**Decided.** Founder decision.

### What was decided

The host check stops caring where an existing Docker came from. It accepts
any source except a snap, provided `docker compose` works and the engine and
the compose plugin meet minimum versions: engine 25.0.0, daemon and CLI, and
compose 2.24.0. Below a minimum it refuses, naming the versions found and
required. A snap stays refused however new it is. When Docker is absent,
`host prepare` still installs it from Docker's own apt repository. Mixed or
broken installs remain caught by the existing check that `docker compose`
works, in `host prepare`.

### Why

What the toolkit depends on is what the engine and compose can do, and that is
a version. A package name stood in for it badly: Ubuntu's `docker.io` with
`docker-compose-v2` is current on a supported release, while a `docker-ce`
installed years ago and never upgraded is not, and only the version tells
them apart. Until now no version was checked at all: any Docker whose
`docker compose` answered passed, and one too old for the rendered stacks
failed only once an apply reached a stack it could not run.

A snap is the exception because its problems are not versioned: its
confinement keeps Docker from bind mounting under `/srv`, where every stack
keeps its data, and snapd refreshes it, restarting the daemon and every
container, on snapd's schedule. A version number reveals neither.

An install still has one source because it needs one, and Docker's repository
gives every host the toolkit prepares the same engine.

### How the minimums were set

From the code, not from memory: every Docker and Compose behaviour the
toolkit relies on was listed with the release that introduced it, and each
minimum is the newest of them. Both are set by `healthcheck.start_interval`
in the pocket-id template: Docker Engine 25.0.0 (API 1.44) and Docker Compose
2.24.0, which refuses the key on an older engine. The table, with citations,
is in `docs/specs/2026-10-11-docker-version-gate.md` and beside the constants
in `internal/hostcheck/docker.go`. One dependency is not established from a
citation: how Compose 2.24.0 reads quotes and escapes in an `env_file`, which
the toolkit's `.env` quoting was observed against on Compose 5.4.0 only. It
did not set a minimum.

### What was run

`go build ./...`, `go vet ./...`, `go test ./...`, and `gofmt -l .` empty.
Nothing was run against a host.

## 2026-10-11: A gateway move deletes the old gateway's address records once the new one is verified

Founder decision. The design is `docs/specs/2026-10-11-gateway-move.md`.

### What was decided

Moving the gateway is done with both gateways live at once. The new
gateway's address records are added beside the old one's, an ordinary create
as `dns init` makes, and only once the new gateway is up and verified serving
every hostname does the toolkit delete the old gateway's records. Then it
waits out their TTL, and only then stops the old gateway's Caddy, which it
refuses to do unless the verification passed.

The founder called the deletion one of the few safe, automated DNS deletions
the toolkit is allowed. It is narrow: an A or AAAA record carrying this
deployment's `dns init` comment, not proxied, at a hostname the gateway serves
in the configuration, pointing at the old gateway's public address and not the
new one's, deleted only when every record the new gateway needs is present and
after the verification passed on the same run. Every other deletion stays
where it was: `dns prune` and `site remove`'s DNS stage, under their own rules.

### Why

The gateway is the address that has to keep answering. A cutover, where the
old records are replaced and the old gateway stopped together, leaves every
visitor whose resolver still holds the old address with nothing, and cannot be
walked back once the old gateway is gone. With both serving, nothing is
unreachable at any point before the stop, and stopping the move before the
deletion leaves a working deployment. The deletion is safe to automate because
the condition that makes it safe is checkable from the workstation: the new
gateway answering every hostname through its own address, with valid
certificates, which DNS-01 lets it hold before any record points at it.

### What was weighed

The overlap and the deletion are the founder's. The two points after the
first below are the implementation's, proposed in the spec for review.

* **Updating the record in place.** The provider interface has no update, on
  purpose, and an update is a cutover: there is no moment with both addresses
  published.
* **Lowering the TTL first.** It needs an update too, and the records `dns
  init` makes already carry the provider's automatic TTL, five minutes at
  Cloudflare. The wait reads the deleted records' TTL instead.
* **Declaring both gateways during the move.** The configuration declares the
  end state, and `dns init` would have to choose an address per name. The old
  gateway is found from what it still runs, holds, and is pointed at.

### What was run

`go build ./... && go vet ./... && go test ./...`. The move's tests run
against a fake DNS provider and fake hosts; the deletion tests were checked to
fail with the old address filter removed. Nothing was run against a provider
or a deployment.

---

## 2026-10-11: A pinned app may keep its files on its own disk

Founder decision.

### What was decided

Every app takes `storage: local | s3`. Left out, it is `s3` when
`storage.garage.sites` names a site and `local` when it names none.
`local` is valid only with pinned placement; `cluster` with `local`, written
or by default, is refused (`storage-local-on-cluster`). A pinned app in `s3`
may use a Garage node on another site, with no warning. The key applies to
every kind that stores files: `mbin`, `outline` and `writefreely` take either
value; `synapse` and `uptime` have no S3 form, so `s3` is refused and `local`
accepted as what they do anyway; `pocket-id` is refused and pointed at its
`file_backend` setting; `element` and `oauth2-proxy` store no files and are
refused. `s3` with no Garage site and a declared `hostnames.media` on an app
in local mode are refused as well.

In local mode an app gets no bucket, key, grant, website access or media
hostname, so `storage init`, `storage add`, `dns init`, the gateway's media
route, `init`'s S3 key generation and `CheckGarageKeys` all skip it, and
`storage rotate-key` refuses it. Each kind renders upstream's own local
configuration: Mbin leaves `S3_KEY` out and sets `KBIN_STORAGE_URL` to its
own hostname under `/media`; Outline sets `FILE_STORAGE=local` with
`FILE_STORAGE_LOCAL_ROOT_DIR=/var/lib/outline/data`; the wisp fork renders
`[storage] type = local` and `[uploads] dir = /data/uploads`. Every one of
those directories was already a bind mount in the kind's compose file.

### Why

README Rule 5 called a pinned app "exactly as upstream ships it", yet every
kind that can use S3 was given Garage whatever its placement, so a pinned app
still depended on another site for its uploads. Defaulting to S3 where Garage
exists keeps Rule 3 for everyone who does not ask otherwise. Defaulting to
local where it does not replaces S3 settings with an empty endpoint, which
rendered an app whose every upload failed. Local under cluster is refused
because each apps site would keep its own diverging copy.

Allowing `local` on `synapse` and `uptime` rather than refusing it as
redundant: the value is true of those apps, so nothing written is ignored, and
an operator who sets `storage: local` on every pinned app is not refused for
consistency. The rule that refuses an override nothing reads (`smtp` on a kind
without mail) refuses because the setting would silently not happen; here it
already happens.

### What it needed beyond the settings

* **Ownership.** Docker creates a missing bind source as root. Mbin's
  entrypoint chowns `var/`, `/data` and `/config` but not `public/media`, and
  Outline runs as `nodejs` and nothing in its image chowns a bind there, so in local mode
  neither could write an upload. Their compose files wrap the image's
  entrypoint to chown the one directory first, Outline then dropping to
  `nodejs` with `setpriv`. The wisp fork's uploads are under `/data`, which
  holds its keys too, so it needs what it already needed.
* **The gate.** A media hostname is never gated, because a federating server
  fetching an image has no gate session. In local mode those files are on the
  app's own hostname, so a gated Mbin opens `/media/*` and a gated blog
  `/uploads/*` (`kinds.LocalMediaPaths`). Outline's local files answer through
  its API with the reader's session, and open nothing.

### What was weighed

* **Refusing `storage: local` on `synapse` and `uptime` as redundant.** Above.
* **A one-shot container to chown the directory.** apply's health gate fails a
  stack with an exited container, so the wrapper stays in the app's own
  service.

### What was run

`go build ./... && go vet ./... && go test ./...`, `gofmt -l .` empty. The
wrapped entrypoints were run through Docker Compose against the pinned
`ghcr.io/paisans-software/mbin:1.14.0-paisans` and `outlinewiki/outline:1.10.0`
images with a root owned bind at the upload directory: each wrote a file
there as uid 1000 and uid 1001 respectively. Nothing was run against a
deployment, and no app was started in local mode end to end.

## 2026-10-11: Every image the configuration names by a tag is resolved to a digest when apply plans

Founder decision, as the stack channel spec
(`docs/specs/2026-10-11-stack-channels.md`), sections *Resolution at plan
time*, *An app on several sites*, *What the operator sees*, *Overriding* and
*Validate*.

### What was decided

What the gateway's Caddy already did (see *The gateway's Caddy follows its
major version, resolved to a digest when apply plans*) now holds for every
image an app's stack runs. `render` lists, per site and compose file, every
image the configuration decides, the app stacks' and the gateway's Caddy, in
`Plan.Images`, which replaces `Plan.CaddyImages`. `apply.Build`, given a
resolver, resolves each tag without a digest once per run, pins every line
naming it, and stops when the lines rewritten differ from the count render
reported. A digest is taken as written. An unreachable registry keeps the
deployed build, with a warning, and refuses a stack that runs none.

An app on several sites stays on one build. `deployment apply` without `--site` resolves
once for every site and moves them together. `deployment apply --site` reads the app's
compose file on its other sites and keeps the app on the build they run,
warning when the tag names a newer one. Sites that already disagree are
refused, naming each site's build.

The manifest records each service's build under `pins` (stack, service,
configured image, reference, `resolved_at`, source), replacing `caddy`. The
dry run lists each moved image by app and service, with the builds it goes
between and their `org.opencontainers.image.version` labels when they have
them. validate warns, `image-channel-differs`, when an app's image names a
`stack-<N>` other than its kind's default.

### Why

A tag in a compose file never changes, and apply recreates a stack only when
its file changes, so a promotion to a fork's channel, or a version tag rebuilt
for a CVE, never reached a host. Resolving at plan time makes a new build a
changed compose file, which the dry run shows and the apply carries through
the same gates as any image change, and keeps what a host runs exact and
recorded.

Holding a clustered app together is for its database. Two builds against one
database is how a migration runs twice or not at all, so an apply that cannot
see every site of the app does not choose a build for it.

### How

* **The manifest record is `pins`, not `images`.** The spec names the record
  `images`, and the manifest's `images` key already holds the sweep's history
  of every repository a site has rendered. Reusing the key would have changed
  what an existing manifest's entries mean; the new record takes its own.
* **No conversion of a manifest's `caddy` record.** The toolkit never adopts
  an older layout. The build a host runs is read from its compose file, which
  is what "keep the deployed build" needs, so a `caddy` entry is ignored and
  disappears on the next manifest write.
* **The other sites are read from their compose files, not their manifests.**
  The compose file is what the host runs, and it is the same source the
  single site path already reads for its own build. A site whose file cannot
  be read is left out with a warning when this site runs a build to stay on,
  and refuses an apply that would start the app with no build to compare.
* **Every image of a clustered app's stack is held, not only the app's own.**
  The rule is about the app, and holding Valkey or RabbitMQ beside Mbin costs
  nothing but a later move.
* **`storage rotate-key` passes no resolver**, joining `site add`, `site
  remove` and `storage add`: a key rotation rewrites an environment, and an
  image moving in the middle of one is a change nobody planned.
* **The rewrite is per reference, counted against render's report.** Each
  `image:` line is matched whole, so a reference that shares a prefix with
  another is not counted as it.
* **Versions are read from the image configuration**, anonymously as the
  digest is, for linux/amd64 of a multi platform index. A build without the
  label shows its digest alone.
* **Without a resolver, whatever the host runs is kept**, whatever the
  configuration now names: another tag, a moved channel or another
  repository. Every caller without one has a purpose narrower than moving
  images, and the next apply shows and makes the move. With a resolver and an
  unreachable registry, only the same tag is kept, pinned or bare, since a
  different one is not the configuration's.
* **A bare tag is not a build.** A stack an older toolkit applied names its
  tags bare, so which build it runs is not known. Pinning one is a first pin,
  listed as `pins`, not a move, though the stack is recreated once. Across
  sites, a bare tag agrees with a digest of the same tag, and both are held
  on the pinned build.
* **A stack this apply leaves alone resolves nothing.** Outside `--only`, or
  held back with Except, a stack is pinned as with no resolver, so a registry
  outage for one app cannot refuse an apply of another, and the dry run lists
  only what moves.
* **The run's cache keeps failures too.** A reference that failed fails for
  the rest of the run, and a registry host that could not be reached fails
  every later reference on it at once.
* **Only a run over every site skips the other sites' builds.** apply
  without `--site` marks its run (`planningEverySite`); its `deployment apply --site`
  steps then hold nothing, and every site moves through one cache. A run that
  stops part way leaves the moved sites on the new build, an `deployment apply --site`
  then refuses naming each, and the next whole apply moves the rest.

### What was weighed

* **Naming the record `images` and moving the sweep history to a new key.**
  That reinterprets every existing manifest's `images` entries as pins, which
  is conversion by another name.
* **Reading a peer site's build from its manifest.** A manifest written
  before this change has no `pins`, and the compose file is the fact the
  manifest records.
* **Holding a clustered app in the steps of apply without `--site` too.**
  Each step would hold its site on the build the others run, so a clustered
  app would never move at all.
* **Comparing a bare tag with the digest it names now.** The registry says
  what the tag names today, not what a host pulled under it earlier.
* **Refusing an deployment apply --site whenever another site of the app cannot be
  read.** That refuses the apply an operator runs to repair a surviving site
  while another is down, and the surviving site's own build is one the
  cluster already runs.

### What was run

`go build ./... && go vet ./... && go test ./... -count=1`, `-race` on the
changed packages, `gofmt -l .` empty. `imagedigest.Registry` was run against
ghcr.io and Docker Hub for the digest and the version label of
`ghcr.io/paisans-software/caddy:2` (v2.11.7), `outlinewiki/outline:1.10.0`
(1.10.0) and `docker.io/library/rabbitmq:3.13.7-management-alpine` (no
label). Nothing was run against a deployment.

---

## 2026-10-11: Mbin and WriteFreely lock what the toolkit renders, with authorized fetch on by default

Founder instruction: "Update the toolkit to configure Mbin and WriteFreely
with the locked settings and authorized fetch configs by default (assuming the
keys are missing from the yaml)". The goal is that an admin cannot drift from
`paisans-stack.yaml` through the apps' admin pages. It is the toolkit side of the
`stack-1` template contract in `docs/specs/2026-10-11-stack-channels.md`.

### What was decided

* `settings.authorized_fetch`, a boolean for `mbin` and `writefreely`, true
  when absent. Mbin renders `MBIN_AUTHORIZED_FETCH` always and names it in
  `MBIN_LOCKED_SETTINGS`; WriteFreely renders `[app] authorized_fetch`.
* WriteFreely's `config.ini` opens with `locked_settings`, naming every
  database setting the template renders, derived in Go and checked by a test
  against the fork's registry in both directions.
* Both kinds' health route is `/healthz`, so a gated app's open paths gain it
  and the monitor's public check reaches the app.
* A gated app with `authorized_fetch: false` is refused by the existing
  `visibility-gate-without-signed-fetch`. A value that is not a boolean is
  refused (`authorized-fetch-not-a-boolean`), and so is a WriteFreely
  `min_username_len` or `uploads_max_size_mb` the fork would refuse
  (`writefreely-setting-invalid`), since a locked value it refuses stops the
  blog at startup.
* WriteFreely's signed fetch record is `app.authorized_fetch` with
  `app.private`, and its probe a blog outbox; a gated Mbin gains a direct
  check that anonymous `GET /api/entries` answers 401.
* Both kinds default to the `stack-1` channel tag.

### Why

Both apps keep their settings in a database once an admin saves, so a
rendered value held only until the first save. Locking is the fork feature
built for exactly this, and naming every rendered setting rather than a chosen
few is what lets a test derive the list instead of trusting it.

Authorized fetch on for a public app as well as a gated one: it costs a peer
nothing that signs its fetches, both forks leave the instance actor and
discovery open so two such servers can still bootstrap, and it means a
federating app's read surface answers only identified instances.

### What was weighed

* **Moving WriteFreely's signed fetch to `app.authorized_fetch` alone.** The
  brief for this change said so. Kept with `app.private` instead: with no
  allowlist, `authorized_fetch` admits a signature from any instance
  (federation_allowlist.go:213-216), and only private mode admits a signer
  solely through the allowlist (federation_allowlist.go:486-495), which an
  allowlist itself requires (federation_allowlist.go:84-86). Dropping private
  mode would loosen the gate's federation policy, the founder's 2026-10-08
  decision, from "reads only from its allow list" to "reads from any signer",
  and that is not this change's to make.
* **Keeping the instance actor as WriteFreely's probe.** It is one of the
  routes `authorized_fetch` leaves open, so under it the probe would test
  private mode only. The outbox is refused before any lookup, so it needs no
  blog to exist.
* **Locking `app.federation_allowlist`.** It is not rendered, and locking an
  absent key would lock it empty and take the allowlist away from the admins
  who maintain it.
* **Rendering `settings_location = database`.** The fork refuses to start
  when the marker is present and the database holds no settings, which is
  every new blog's first start, so the toolkit leaves it out and the fork's
  warning about a read-only file stays a known limit.
* **A signed fetch check on ungated apps too.** Their settings are locked, so
  nothing in the app can turn authorized fetch off; the gate's check stays
  because it proves the gate's security property, which a public app does not
  have.

### What was run

`go build ./... && go vet ./... && go test ./... -count=1`, `gofmt -l .`
empty, the golden trees regenerated and read. Against a local build of
writefreely-wisp develop f95d840 (0.21.1+wisp), `go test -tags
writefreely_integration ./internal/render/` with `PAISANS_WF_IMAGE`: the blog
started on the rendered `config.ini`, `writefreely settings list` marked all
eleven names locked at the file's values, `settings set
app.authorized_fetch false` was refused, `/healthz` answered 200 and an
unsigned GET of a blog outbox answered 401. Mbin's behaviour was read from the
fork's source at v1.15.0+paisans, not run; the 1.15.0-paisans image's
declared volume was inspected from the registry. The `stack-1` tags did not
exist on the registry when this was written, so nothing here pulls them.

## 2026-10-12: A declared admin joins every app's member group

Founder decision, in session, after a first login to a fresh deployment's
Mbin was refused with `OIDC login refused: <sub> is not in members`.

### What was decided

`deployment apply` puts each admin `paisans-stack.yaml` declares in every app's member group
as well as every app's admin group. The member groups are read from the same
keys `oidc client create` reads (`settings.member_group`, defaulting to
`members`); one that is also an admin group is joined once. `app admin
create`, which makes a user the operator names, is unchanged.

### Why

Mbin's member gate admits a login whose claim lacks the member group only
when the linked account is already an Mbin administrator, and a first login
has no linked account. A declared admin in `admins` alone was therefore
refused on the login that would have made them an administrator. The Mbin
fork now also admits a login its admin group claim names, but an app that
reads no group relies on its client's restriction at Pocket ID, and joining
the member groups makes every app admit its administrators without each one
having to know about admin groups.

This widens the standing approval for `deployment apply`'s writes to Pocket ID: adding a
declared admin to the member groups is part of declaring them, like the admin
groups before it.

### What was weighed

* **Relying on the Mbin fork's change alone.** It fixes Mbin, and only Mbin;
  any other app gating on its member group would repeat the failure.

### What was run

`go build ./... && go vet ./... && go test ./...`, `gofmt -l .` empty. The two
new tests (`TestAdminRequestJoinsTheMemberGroups`,
`TestPocketIDPutsADeclaredAdminInTheMemberGroups`) fail with the change
reverted and pass with it. Not run against a live Pocket ID.

## 2026-10-12: A gated Mbin lets a native app through on its token

Founder decision.

### What was decided

Mbin's gate record gains `/api/*` as a token path, so a request there with
`Authorization: Bearer` reaches Mbin with no gate session, and opens
`/api/client`, `/api/info` and `/api/instance` (exact paths) with no token.
The visibility gate spec's *Which kinds have token paths* has the reasoning
and the citations.

### Why

The gate record kept `/api/*` behind the gate because a public Mbin answers
its API reads to anyone. A gated Mbin has since become a private instance
(the 2026-10-11 record above), which refuses any request without a signed in
user, so a token path no longer opens anything. Keeping the API behind the
gate cost Mbin's mobile apps, which sign in through a WebView and then call
`/api/*` with no gate cookie. The three open routes are what such an app calls
before it holds a token.

### What was weighed

* **Gating `/api/client` and accepting broken mobile apps.** Registration
  alone grants nothing: a public client still needs a member's sign-in at
  `/authorize`, and a `client_credentials` client's bot user is created
  unverified, which the API firewall refuses. Anonymous registration can
  still add unverified bot users, rate limited to two per six hours, which
  was judged acceptable.
* **`KBIN_ADMIN_ONLY_OAUTH_CLIENTS`.** It closes anonymous registration, and
  with it the mobile apps' own registration.

### What was run

`go build ./... && go vet ./... && go test ./...`, with the golden tree
regenerated and its diff read. `TestMbinOpensItsAPIToATokenOnly` fails with
the gate record reverted and passes with it. Not run against a live Mbin or a
mobile app: which routes a given app calls before sign-in is read from Mbin's
access rules, not observed.

## 2026-10-12: The gate is two booleans on the app, and the toolkit runs it

Founder decisions, in session. The design is the amended
`docs/specs/2026-10-08-visibility-gate.md`.

### What was decided

* **Two per-app booleans, `gated` and `auto_login`**, both default false,
  replace `visibility_gate: public | member | provisional`. `gated: true` is
  the member gate. `auto_login: true` skips the app's own sign-in button for
  a member the gate has signed in, on a gated app or a public one.
* **The provisional concept is retired.** No gate admits a signed-in
  non-member, so the gate is one oauth2-proxy instance with
  `OAUTH2_PROXY_ALLOWED_GROUPS=members`, and the second instance, the 403
  redirect and the `/pending` page are gone. It admits admins as well since
  2026-10-12 (*The gate admits admins*, below).
* **The gate is never declared.** When any app sets either key, `config.Load`
  derives an app named `gate` at `gate.<community.domain>`, pinned to the
  first gateway site. A declared `oauth2-proxy` app is refused.
* **A public app can take auto-login.** Its auto-login paths check the gate's
  session with `forward_auth` before redirecting, and its sign-in page, with
  no gate session, signs in at the gate first and comes back holding one.
* **`validate` refuses** `auto_login` on a kind with no auto-login rule
  (`auto-login-without-a-rule`) and either key on an app whose hostname is
  outside the community's domain (`uses-gate-outside-the-domain`). The gate
  rules are renamed for the key: `gated-on-ungateable-kind`,
  `gated-without-signed-fetch`.

### Why

The gate took a declared app with a hostname and a placement, plus a
three-valued key on each app, to say one thing: whether members only may read
an app. The founder asked for a boolean. With the provisional group retired,
the two-instance split had nothing left to separate. Deriving the gate on the
gateway keeps it beside the edge it serves, so it is down only when the
gateway is. Auto-login on a public app gives single sign-on to apps that stay
readable by anyone; it checks the session rather than trusting the cookie's
name because, with no gate behind it, a stale or forged cookie would otherwise
push a visitor into a sign-in they never asked for.

### What was weighed

* **Running the gate in every deployment.** Chosen first, then replaced by
  running it whenever an app sets either key: with `auto_login` its own
  key, a deployment that wants neither needs no gate.
* **One key for both.** A public app with no auto-login, or a gated app with
  none, could not be expressed.
* **Keeping the `provisional` value as an escape hatch.** Retired with the
  concept.

### What was run

`go build ./... && go vet ./... && go test ./...`, `gofmt -l .` empty, the
golden trees regenerated and their diffs read. `paisans validate` on
`examples/paisans-stack.example.yaml`: two warnings, no refusals. The rendered
gateway configuration passed `caddy validate` in
`ghcr.io/paisans-software/caddy:2`. The public auto-login host block, run in
that image against a stub gate, behaved as specified: an anonymous visitor read
the app; `/login` with no session went to the gate's sign-in with the sign-in
breaker set; a member was sent to the OIDC route with the loop breaker set; a
refused session came back to its own URL with the loop breaker set, then read
the app; a member already signed into the app, or on a deep link, read it. With
the gate unreachable, a browser holding the gate's cookie got 502 on `/`, and
one without read the app. Not run against a live oauth2-proxy or Pocket ID.

## 2026-10-12: The settings gating needs are checked on both sides

Founder decisions. The design is `docs/specs/2026-10-12-gate-settings.md`.

### What was decided

* `validate` refuses `config-key-already-rendered`, which only render refused
  before, by rendering the deployment with secrets generated in memory and
  thrown away. Render keeps its own refusal.
* Each kind records, in `internal/kinds`, the settings `gated` needs and how
  to read them from a running copy: Mbin's private instance mode, allow list
  and authorized fetch, WriteFreely's private mode and authorized fetch.
  `doctor` reads every kind through that record and never branches on one.
* `doctor` gains a `settings` section, which fails a running copy with one
  of them off or not locked, and names `deployment apply` as the fix.
* `doctor --execute` removes from `paisans-stack.yaml` a `config:` key that only
  repeats a value gating renders. It changes no host.
* Only settings the toolkit writes and locks on disk are in scope. Nothing
  writes an app's database.

### Why

A key gating made redundant reached the operator only at render, which a
whole apply meets after `init` and `host prepare` have run. The gate leaves
ActivityPub, and for Mbin the API with a token, to the app, so those settings
are part of the gate, and nothing read them from a running app. Keeping each
kind's knowledge in its own record is what lets a new kind join without a
branch in `doctor`. Keeping to settings locked on disk means a wrong value is
always a file to render again, which `deployment apply` already does and checks.

### What was weighed

* **A second list of the keys each template writes, in `validate`.** Whether
  a template writes a key depends on the rest of the file (gating writes
  Mbin's allow list), so a list would go stale where rendering cannot.
* **Fixing an app's database from `doctor`**, through Mbin's `dbal:run-sql` or
  a new Mbin console command. Out of scope: every setting gating needs is
  locked on disk, so a database row cannot override it.
* **`doctor --execute` changing a contradicting value.** That changes policy;
  the operator edits it.

### What was run

`go build ./... && go vet ./... && go test ./...`, `gofmt -l .` empty. A copy
of a real `paisans-stack.yaml` with `MBIN_USE_FEDERATION_ALLOW_LIST` in a gated
Mbin's `config:` passes `validate` before this change and is refused after it,
in 1.3 seconds. Not run against a live Mbin or WriteFreely: each read is taken
from the fork's source (Mbin's `SettingsManager.php` at `v1.15.1+paisans`,
WriteFreely's `cmd/writefreely/settings.go` at `v0.21.1+wisp`).

## 2026-10-12: The gate admits admins

Founder decision.

### What was decided

* The gate admits `members` and `admins`:
  `OAUTH2_PROXY_ALLOWED_GROUPS=members,admins`, rendered from
  `kinds.MembersGroup` and `kinds.AdminsGroup` through the gate's client spec.
* The gate's client at Pocket ID is restricted to the same two groups, so the
  gate and its client admit the same people.

### Why

By definition an admin can see everything a member can, so the gate has no
reason to refuse one who is not also in `members`. Rendering the gate's groups
from its client spec keeps the two sides from naming different groups.

### What was run

`gofmt -l .` empty, `go vet ./...`, `go test ./...` and `go test -race ./...`.
oauth2-proxy `v7.15.4` reads `OAUTH2_PROXY_ALLOWED_GROUPS` through viper, which
splits a string on commas into a list (`pkg/apis/options/load.go:27-55`; viper
`v1.21.0`, `viper.go:977-1022`), read from source. Not run against a live gate.

## 2026-10-12: Applications are blueprints, read from signed catalogs

Founder decision. Design: `docs/specs/2026-10-12-blueprints-and-catalogs.md`.
Not implemented.

### What was decided

* An application is a blueprint: a `blueprint.yaml` and its templates. The
  logic a blueprint cannot declare stays in the binary as capabilities,
  helpers and in-app actions, named for their mechanism, and any blueprint may
  use them, third party ones included.
* Blueprints come from catalogs, git repositories a deployment declares by URL
  and pins to a signed tag. With none declared, the core catalog at a URL
  compiled into the binary is used, and it is used alongside any that are
  declared.
* The nouns are blueprint and catalog; an app's `kind:` key becomes
  `blueprint:`.
* `when` conditions and `checks` are CEL expressions.
* `pocket-id`, the gate, `invite` and `uptime` are reserved: built into the
  binary, never read from a catalog, and no catalog blueprint may take their
  names or their capabilities.
* Uptime is the only monitor. A blueprint without a health route is watched by
  a TCP check per site and a public check of `/`, with a warning. A deployment
  with no monitor stays a warning.
* Declaring an app approves its OIDC client whatever catalog its blueprint
  comes from, as it already does for a first party app.

### Why

Every application compiled into the binary means every new one waits for a
toolkit release. Declaring what the toolkit already keeps per kind in Go maps
and switches lets anyone publish an application while the writes that matter,
to Pocket ID, the cluster and Garage, stay the toolkit's own code. The reserved
four are the identity provider and the services that write to it, are derived
from the whole deployment, or watch it, so a catalog that could replace one
could take over sign in or blind the deployment to its own failures. Signed tags
and a snapshot in the configuration store keep what a deployment renders fixed,
reviewable and recoverable without the catalog.

### What was weighed

* **Executable plugins over a stdin and JSON protocol.** Arbitrary code would
  run on the workstation that holds the operator's credentials. Not needed
  while capabilities, helpers and in-app actions cover the first party
  applications.
* **Following a catalog's branch head, as Homebrew taps do.** Two admins a week
  apart would render different files from the same configuration.
* **Opening the monitor to other software** through a published monitoring
  plan and per monitor adapters. Founder decision: too complicated; uptime
  stays the only monitor.
* **Refusing a blueprint without a health route.** Leaving an app unwatched is
  worse than a weaker check, so the fallback checks it and warns.

### What was run

Nothing: a design. The uptime fork's TCP monitor type was read from its source
(`src/lib/sitePayload.js:18,127-134` at `b730e9b`).

## 2026-10-13: Fork images are pinned to release version tags, and the stack channels are retired

Founder decision. Supersedes `docs/specs/2026-10-11-stack-channels.md`, except
its sections on resolving tags to digests at plan time, which stand.

### What was decided

* The `mbin` and `writefreely` blueprints name each fork's latest release by
  its version tag: `ghcr.io/paisans-software/mbin:1.15.1-paisans` and
  `ghcr.io/josephquigley/writefreely-wisp:0.21.1-wisp`. A newer release is
  taken by changing the tag in the core catalog and moving this toolkit's pin
  to it.
* The `stack-<N>` channel tags are retired, never having been published. The
  forks' promote workflows are removed, and so are `kinds.Channel`, validate's
  `image-channel-differs` warning, apply's "no paisans-verified release yet"
  refusal and `render.ComposeImage.Default`, which existed only for it.
* Both forks' images are recorded in `kinds.ImageVolumes` by their exact
  reference, like every other image the toolkit names; `RepositoryVolumes`
  keeps only the gateway's Caddy.
* Resolving every tag to a digest when apply plans, and the manifest's
  `pins`, are unchanged.

### Why

No `stack-N` tag was ever pushed to the registry, so a deployment on either
kind's default could not resolve its image, and production already ran the
forks' version tags. A version tag in the blueprint ties each toolkit release
to the fork build its templates and tests ran against, which is what the
golden render tree and the volume table check, and the move to a new release
is a reviewed change to the catalog rather than a tag moved elsewhere.

### What was run

`go build ./... && go vet ./... && go test ./...` and `gofmt -l .` empty; the
golden trees regenerated with `go test ./internal/render -update`, and their
diff read: the blog's image tag and the manifest's hash of that file. Both
tags were confirmed as the newest releases with `gh release list` and
`docker buildx imagetools inspect` on 2026-10-13, which also gave the
declared volumes: `/app/var/` for Mbin (linux/amd64 only) and none for
WriteFreely on linux/amd64 and linux/arm64, and `go test -tags
registry_integration -run TestRecordedVolumesMatchEveryPlatform/ghcr
./internal/kinds/` agreed on both entries (Docker Hub's rate limit kept the
rest of that test from running). The core copy was synced from
`Paisans-Software/blueprints` with `scripts/sync-core-catalog.sh`. Nothing
was run against a deployment, and no image was moved, deleted or retagged.

## 2026-10-13: Catalogs are unsigned, trusted by their pin

Founder decision, 2026-10-13. Amends *Applications are blueprints, read from
signed catalogs* above.

### What was decided

* Nothing checks who published a catalog's release. The SSH tag signing
  built for the blueprints rollout is removed: the pin's `signers` and
  `unsigned` keys, `keys/allowed_signers`, `catalog add --trust` and
  `--unsigned`, `catalog update --trust`, the walk of keys from release to
  release, and the core release keys the binary was to carry.
* A catalog is trusted by its pin. The pin is a commit the operator chose, a
  tag beside it must still name that commit, `catalog add` and `catalog update`
  show what a release changes for each app, and the grant review asks before
  a moved pin renders on a site.
* The core catalog stays the toolkit's built in copy unless a deployment pins
  `catalogs.core` to a commit, which it now may, as it may any catalog.

### Why

The founder chose to rely on unsigned repositories for now. Signing asked a
catalog's author to keep a key safe and move to a new one through a chain of
signed releases, asked an operator to check a fingerprint by another channel,
and left the core catalog unpinnable until the founder recorded a release key.
What a deployment renders was never fixed by the signature: the commit pin
fixes it, and the grant review gates every move of it.

### What was weighed

* **GitHub release attestations** in place of SSH keys, drafted as an
  amendment spec and set aside with signing as a whole.
* **Keeping signing optional**, with `unsigned: true` as the default. Code that
  no pin uses is code that no deployment exercises.

### What this gives up

A compromised forge account, a transferred repository, or a deleted one whose
name is registered again can publish a release that `catalog update` would
offer. The operator still sees what it changes, and the grant review still
asks, before it renders anywhere.

## 2026-10-13: Sites may be arm64, and only physical database copies need one architecture

Founder decision. Spec: `docs/specs/2026-10-13-arm64-and-mixed-architecture.md`.

### What was decided

* amd64 and arm64 hosts are supported for every role. Any other architecture
  is refused by name.
* Each site's architecture is read from the host when apply, `host prepare`
  or `site add` first reaches it, and recorded by the toolkit as
  `sites.<name>.arch` in `paisans-stack.yaml`. A whole apply records every
  missing key before it prepares hosts and publishes the signed copy.
* A host that reports a different architecture from the recorded one is
  refused before anything on it changes. Deleting the line is how an operator
  accepts new hardware.
* Every data site must share one architecture, whichever it is: `validate`
  checks the recorded keys, apply checks again after recording, and `site add`
  preflight compares it with OS and glibc. The same holds for a pinned app's
  move by tar and for a WAL-G restore, whose base backups record their
  architecture.
* Everything else may mix: SQLite and pinned apps, Pocket ID, Garage, etcd,
  apps sites, the gateway, the monitor and the witness.
* Apply refuses, at plan time and before any pull, an image with no build for
  the architecture of a site it is planned on.
* Building images for arm64 is out of scope for the toolkit.

### Why

Postgres's data directory and WAL are machine-specific binary formats, so only
the paths that copy them byte for byte need matching hosts; everything else
talks over the network and has nothing to match. Recording the architecture
lets `validate` check those rules without reaching a host, and turns a host
replaced under the same name into a refusal instead of a broken replica.
Checking the image platforms in the plan names the gap (today, the Mbin fork's
amd64-only build) before a pull fails on the host.

### What was run

Nothing beyond reading the code; this records a design. Every image the
toolkit names was checked for linux/arm64 with `docker buildx imagetools
inspect` on 2026-10-13: all have it except `ghcr.io/paisans-software/mbin:1.15.1-paisans`.

## 2026-10-13: Core blueprints carry their own rules, links and secret recipes

Founder decision, 2026-10-13. Amends
`docs/specs/2026-10-13-core-catalog-fetched.md`.

### What was decided

* A blueprint check may give its message, and its key, as CEL expressions,
  so a rule moved out of `internal/validate` names the value it refuses as it
  does today.
* A blueprint may declare a second listening service (`services[].port`) and
  extra databases (`database.extra`); the homeserver's authentication service
  uses both.
* Template helpers named for one app become generic helpers of the contract,
  or read data the blueprint declares, and the core catalog's templates change
  with them.
* A generated secret declares its format, its rotation cost, whether it may
  ever be rotated, and the group it rotates with. Only a rotation that needs
  the running app is an in-app action.

### Why

The core catalog can now change with a catalog commit, and Go that names one
of its blueprints cannot. Each of these lets a blueprint say what Go says
today, so a third party blueprint can say it too.

### What was weighed

* **Fixed check messages**, which lose the value an operator has to change.
* **`ports.served_by_web_server`**, proposed in the spec before the code was
  read: the homeserver's second port is a second service, not a port nothing
  dials.
* **Keeping the named helpers** as part of the contract, which keeps Go naming
  core blueprints.
* **Moving only the RabbitMQ rotation**, which keeps formats and costs keyed
  by name in Go.

## 2026-10-13: Admins are declared, and `admin magic-link` replaces `app admin create`

Founder decision, 2026-10-13.

### What was decided

* Every admin declared under `admins` is an admin of every app that takes its
  administrators from a Pocket ID group, which `deployment apply` already makes so. No
  command makes an administrator outside that list.
* `app admin create` is removed.
* `paisans admin magic-link <admin>` gives a declared admin a one-time sign-in
  link, with Pocket ID's static API key, and does nothing else. It serves a
  first link that expired unused and a lost passkey alike, which is why it is
  named for what it hands out rather than for either case.

### Why

An administrator made on the side holds every app's admin rights while
`paisans-stack.yaml` does not show them. With `deployment apply` making the declared
admins, the one thing it cannot do is help an admin who already registered a
passkey and lost it, and a link is all that takes.

### What was weighed

* **Keeping `app admin create` for declared admins only**, or letting it act
  on an app other than Pocket ID: each kept a second path to the same
  accounts.
* **The names** `restore-access`, `recover` and `onboard` each fit one of the
  two cases only; `sign-in-link` and `auth-link`, where "auth" is also the
  wizard's name for the Pocket ID app and reads as authorization too.

## 2026-10-13: Images follow tracks in the core catalog, and the core catalog follows its newest release

Founder decision, 2026-10-13. Spec: `docs/specs/2026-10-13-images-follow-tracks.md`.

### What was decided

* The binary names no image. The core catalog names a track for each, a
  repository and a tag pattern, and `deployment apply` and `init` run the newest tag on
  it at that moment.
* The toolkit's own services take their tracks from `foundation.yaml` in the
  core catalog; there is no second repository.
* The core catalog follows its newest release this binary can read, and the
  binary's pin of it goes.
* Go declares the majors its code speaks to, and refuses a track beyond them.

### Why

Pocket ID, Postgres, Spilo and Caddy ship security fixes on their own
schedules. With every image fixed at build time, each fix waited for a toolkit
release and for every operator to upgrade.

### What was weighed

* **Keeping the core pin and resolving tracks under it:** patches would flow,
  but a new track, such as a new Postgres major or Pocket ID's next API, would
  still wait for a toolkit release.
* **Exact references in a floating catalog, bumped by hand or by a bot:** the
  most predictable, and the slowest, since nothing reaches a deployment until
  someone bumps it.
* **A separate foundation repository:** a second place to bump and a second
  release to tag, for images the core catalog can hold beside its blueprints.
* **A track as a mapping**, `{repository, tags}`: a second shape beside the
  reference every other image key takes, where a tag holding the pattern
  reads as the reference it resolves to.
* **Reading each resolved build's volumes from the registry at plan time:**
  the host check apply already makes sees every build, an operator's included,
  so a second check would only move the refusal earlier, at the cost of a
  configuration read per image and platform on every run.
* **Holding a narrower command to the builds the deployment record names:**
  every such command already keeps what the host runs, and the only image it
  must resolve is one nothing runs, for which there is no build to hold.
* This reverses the per blueprint digest pin of
  `docs/specs/2026-10-13-core-catalog-fetched.md`, which was what said core
  files were the ones a release was tested with. With catalogs unsigned and
  images chosen at apply time, a binary no longer fixes what a deployment
  runs, so the pin bought little beyond a delay.

## 2026-10-13: Every Pocket ID administrator is a Cloudflare Super Administrator, and the reconciler reports it

Founder decision. The design is `docs/specs/2026-10-13-dns-admin-continuity.md`.

### What was decided

* Every enabled Pocket ID administrator must be an accepted Super
  Administrator of every Cloudflare account holding the zone or a zone above
  it, and there must be at least two. Only active zones count. Administrator with access to the zone does not count. Extra
  Cloudflare members are allowed.
* The admin reconciler checks it on its existing pass, reading Cloudflare and
  writing nothing there, and reports it through two heartbeat monitors per
  Pocket ID app: `admin continuity: dns super admins` and
  `admin continuity: dns 2fa`.
* The admins heartbeat is renamed `admin continuity`, named for the community
  rather than the app; with more than one `pocket-id` app each name is
  prefixed by its app key.
* Their bodies are counts, naming nobody.
* An admin whose Cloudflare address differs is mapped in the secrets file,
  `admins.<id>.dns_provider_email`, one to one, and only keyed hashes of
  addresses reach a host.
* The reconciler reads with its own token, `external.dns_members_token`,
  Account Settings Read and Zone Read only. Without it nothing is rendered.

### Why

An account whose Super Administrators are all gone keeps resolving, but
nobody can add the next administrator, and when the last Administrator goes
the zone is recovered only at the registrar. A Super Administrator can also
revoke every other, so the requirement is that each community administrator
holds that power, not that some do. Counts are enough to tell admins and
members that the community is at risk; who is missing is in the log.

### What was weighed

* **Requiring any membership with access to the zone.** It keeps records
  editable and lets succession lapse unseen.
* **Requiring only two Super Administrators among the administrators.** It
  leaves administrators who cannot add a successor, which the one-rule form
  reports.
* **Reusing `external.acme_dns_token`**, rescoped. A token that edits the zone
  would sit on every apps site.
* **The mapping in `paisans-stack.yaml`**, copied in plaintext to every site,
  or **as a Pocket ID custom claim**, carried to every app that signs in.
* **Masked or full addresses in the bodies.**
* **Reporting Cloudflare members who match no administrator.** Extra members
  are a community's own choice.

### What was run

`GET /accounts/{id}/members` and `GET /zones?name=<domain>&status=active`
against a real free account, values withheld (the spec's *Observed*). The
image `ghcr.io/paisans-software/admin-reconciler:0.2.0` was published by
tagging `admin-reconciler-v0.2.0`, resolved to its digest with an anonymous
pull token, pulled by that digest and run: it refused a members token set
without its domain and key, which only this release checks. The pocket-id
blueprint names it by that digest.

## 2026-10-13: Wiki.js is the core catalog's wiki, and Outline is removed

Founder decision, 2026-10-13. Spec: `docs/specs/2026-10-13-wikijs-blueprint.md`.

### What was decided

The core catalog's wiki is Wiki.js 2.5, as the `wikijs` blueprint, and the
`outline` blueprint is removed. Release `v0.0.1` of
`Paisans-Software/paisans-blueprints` carries the change, and the toolkit
reads that release. The blueprint's bootstrap, which finishes Wiki.js's setup
and keeps its sign-in, site settings and groups as the configuration says, is
a repository and image of its own, `Paisans-Software/wikijs-bootstrap`, first
released as 0.1.0, because no core blueprint is named in the toolkit's Go. A
deployment running Outline is redeployed, not converted.

### Why

A community's wiki has to follow the groups its members are in at Pocket ID.
Wiki.js's OAuth2 sign-in maps the groups claim onto wiki groups at every
login, and its page rules gate content by path per group. Uploads live in
Postgres, so the database's backup is the wiki's.

### What was weighed

Outline reads no groups claim. Its group sync exists in source but no
plugin registers it in 1.10.0, and 1.11.0 gates it behind a paid
entitlement. Keeping it meant a SCIM bridge from Pocket ID, a second client
to carry it, and an API key only a signed in admin can make. Wiki.js 3 is a
beta its own README calls not for production use.

Within Wiki.js 2.5, its OpenID Connect module fixes its scopes at `openid
profile email` (`server/modules/authentication/oidc/definition.yml:10-13` at
`v2.5.316`, applied at `server/models/users.js:313`), and Pocket ID sends the
`groups` claim only to a client that asks for the `groups` scope
(`oidc/claims_service.go:157` at `v2.14.0`). So members sign in through the
generic OAuth2 module, which takes its scope as configuration.

### What the spike changed

A spike against Wiki.js 2.5.316 and Pocket ID 2.14.0
(`docs/specs/2026-10-13-wikijs-blueprint-findings.md`) contradicted the spec in
three places, and the build follows what was observed. A clustered wiki's DSN
carries `ssl=no-verify`, since the image's connection string parser ignores
`sslmode` and connected without TLS. Pocket ID's consent screen is off by
default for every client the toolkit makes, with `sso_consent: true` to keep
it. A deep link takes one click on Wiki.js's refusal page, which the founder
chose to keep.

## 2026-10-13: Foundation images are shown in the grant review and not approved

Founder decision, 2026-10-13. It amends the grant review of
`docs/specs/2026-10-11-config-review.md`.

### What was decided

An image needs no `approve` in `deployment apply`'s grant review, when it is added or
changed, only when all of these hold:

1. The render itself took it from `foundation.yaml` for that service,
   decided where the render chose it, never by matching its repository, so
   another service of the same compose file a template points at the same
   reference is approved.
2. That `foundation.yaml` came from a release of the official core catalog: a
   release tag (`vMAJOR.MINOR.PATCH`) of
   `https://github.com/Paisans-Software/paisans-blueprints` names the commit
   read, as that repository itself says (checked against it, never against the
   configured URL, and recorded in the checkout so an offline run keeps it),
   and the toolkit read the commit from a git checkout whose origin is that URL
   and whose `HEAD` is that commit. The URL may differ only by a trailing
   slash, a `.git` suffix and the case of its host. A commit no release tag
   names (an unmerged branch, a pull request's head), a core pin with no tag, a
   core from `PAISANS_CORE_CATALOG`, a `catalogs.core.url`, any mirror, a
   cached commit another URL fetched, or an installed configuration store
   snapshot is not official.
3. The service is the toolkit's own: the infrastructure stack's, the gateway's
   Caddy unless `acme.image` declares one, a pinned app's own database unless
   the configuration declares one, a service the toolkit adds to a stack (the
   admins heartbeat), or a service written with `foundation:` in a reserved
   blueprint or a core catalog one.

The review lists such an image apart, under `foundation images, reviewed in the
core catalog:`, and the host's record holds it like every other image.

Everything else is approved as it was: a service a third party catalog's
blueprint writes with `foundation:`, every image when the core catalog was read
from any other URL, an image a blueprint names itself with `image:` (Wiki.js,
Mbin, WriteFreely, any third party catalog's), an image the configuration
declares, keys, the web server's directory and catalog blueprints. Whether an
image is the foundation's is decided by where the render took it, never by
matching its repository, so a catalog blueprint writing the foundation's own
reference under `image:` is still approved.

The founder narrowed the first version of this decision, which exempted a
`foundation:` service in any blueprint and did not ask where the core catalog
came from: "Approvals are only skipped if they are
https://github.com/Paisans-Software/paisans-blueprints/foundation.yaml. DO NOT
ALLOW 3p FOUNDATION BLUEPRINTS TO BYPASS APPROVALS."

### Why

The team reviews `foundation.yaml` in the official repository's pull
requests and releases it with a release tag there, so the images of a release
fetched from that URL at that commit are checked once, before any deployment
reads them. The repository also serves commits the team never merged or
released (GitHub serves any pull request's head), so the URL alone does not
say a commit was reviewed; a release tag the repository itself names does.
Asking every operator to approve the same tracks again at every apply repeats a
check already made. A third party catalog or a mirror of the core catalog is
not reviewed by that team: a mirror's `foundation.yaml` can say anything, and a
third party blueprint's choice to run a foundation image in its own stack, with
its own configuration around it, is that catalog's choice, which nobody on the
team reviewed. A snapshot is checked only against the sums its own publisher
wrote, and the catalog cache is keyed by commit alone, so neither says where
its bytes came from; only a checkout's origin and `HEAD` do.

### What was weighed

* **Every image of the core catalog**, the core blueprints' own images
  included (Wiki.js, Mbin, WriteFreely and the rest), since the same pull
  requests review them. Not chosen: the exemption covers the foundation only.
* **The foundation's images plus the images of the blueprints that fill a
  capability.** Not chosen, for the same boundary.
* **Requiring pull request approvals on the core catalog** before relying on
  its review: declined for now.
* **Exempting a `foundation:` service in any blueprint**, a third party
  catalog's included, since its image is then the reviewed reference. This was
  the first version; the founder narrowed it to the toolkit's own services.

## 2026-10-14: The invite service is the account service, on account. and invite.

Founder decision. The design is `docs/specs/2026-10-14-account-service.md`.

### What was decided

* The invite service becomes the account service: repository
  `Paisans-Software/paisans-account-service`, image
  `ghcr.io/paisans-software/paisans-account-service`, the reserved `account`
  blueprint and `foundation.yaml` key, the `ACCOUNT_` environment prefix. No
  name keeps an alias and nothing converts an older deployment: a deployment
  is redeployed.
* `settings.invites` is required and never defaulted.
* One app answers on two hostnames: `account.` for everything a signed in
  member or admin uses, and `invite.` for the pages an invitee opens, which
  exists only with `invites: true`. Each hostname serves only its own routes,
  in the service and again at the gateway.
* The `pocket-id` snippet closes its signup routes on `allow_user_signups`
  alone.
* First-party service repositories of the organisation end in `-service`. A
  helper that runs beside another application, such as `wikijs-bootstrap`, is
  named for the application it serves.

### Why

The service is the community's account management: invites and their
approval today, and promotion and banning next, so that none of them needs a
Pocket ID administrator. A name that says invite would be wrong for most of
what it is to carry. Invites are access policy, like `gated`, and they decide
whether a hostname exists, so they are declared rather than inferred. The
invitee's pages are on an origin of their own, which never holds a session: a
link opened by someone without an account never reaches the origin where
members are signed in. The signup condition reads one setting because an
account app with invites on already requires it disabled, and a snippet that
also read which apps name the identity app would keep a second answer to a
question the setting already answers.

### What was weighed

* **Two apps sharing one database**, one per hostname. Not chosen: one app
  that registers each route on its own host keeps the routes apart without a
  second deployment of the same service.
* **One hostname split by path.** Not chosen: the invitee's pages would share
  the origin that holds a member's session.
* **Deploying the app automatically from Pocket ID's settings**, when signups
  are disabled. Not chosen: the app exists because it is declared, and the
  toolkit never adds or drops an app because of another app's settings.

## 2026-10-14: Operators run the machines; management says who manages accounts

Founder decision. The design is
`docs/specs/2026-10-14-operators-and-management.md`.

### What was decided

* The people `paisans-stack.yaml` declares are **operators**: the section is
  `operators:`, the command `paisans operator`. Community admins are the
  members of the Pocket ID group `admins`. Every operator is a community
  admin; a community admin is not an operator.
* `validate` refuses fewer than two operators with a key on every site that
  runs Pocket ID (one is a warning from 2026-10-17, see *One operator is a
  warning, not a refusal*).
* The `pocket-id` app declares `management`, `paisans` or `pocket-id`,
  required and never defaulted. Under `paisans` the account service manages
  signup, bans and recovery, an account app and mail are required, the
  toolkit renders signup closed and the email code open, and nobody is a
  Pocket ID administrator. Under `pocket-id` Pocket ID's admin pages do, the
  operators are its administrators, and an account app is refused.
* The account app uses the deployment's one `pocket-id` app; `identity_app`
  is refused.
* The admin reconciler becomes the operator reconciler. It reads Pocket ID
  and writes nothing: every operator is an enabled member in the member group
  and in `operators`, and every Pocket ID administrator is an operator, none
  under `paisans`. DNS continuity checks the operators.
* Its image publishes only from a tag; a push to `develop` publishes nothing.
* Every name changes outright, with no alias and no conversion of an older
  deployment: deployments are redeployed.

### Why

Two kinds of people had one name. The people who hold SSH keys and the
secrets are few, declared in a file, and can break every machine; the people
who moderate the community are many, chosen by the community, and can break
nothing outside the apps. Naming them apart lets each be held to its own rule.
Two operators, because one lost workstation must not leave nobody able to run
the deployment.

Who manages accounts is a choice a community makes once, and the two answers
need different settings: the account service needs signup closed so new
people come through the code of conduct, and the email code open so a member
whose link expired can come back. Declaring it makes the toolkit render those
settings rather than check that an operator typed them, and makes the
deployment say who administers its identity provider.

Pocket ID is the record of who is a member. A reconciler that wrote groups
there was a second writer of that record, with an API key that can do
anything; one that reads and reports keeps the record with the people and
services that own it. An operator removed at Pocket ID fails the check until
they are taken out of the yaml, which is a manual step by design.

### What was weighed

* **Keeping the reconciler's writes** to keep `admins` equal to Pocket ID's
  administrators. Not chosen: under the account service nobody is a Pocket ID
  administrator, and a second writer of membership is the risk the
  read-only reconciler removes.
* **Defaulting `management`.** Not chosen: it decides who administers the
  identity provider, which is access policy, declared like `gated` and
  `invites`.
* **Letting the account app keep naming its identity app.** Not chosen: a
  deployment has one, and a setting naming it could only agree or be wrong.

## 2026-10-14: Signup is invite or open, every account app has join., and operators holds only operators

Founder decision. The design is
`docs/specs/2026-10-14-account-signup-bans-recovery.md`, and the service side
is paisans-account-service v0.2.0.

### What was decided

* `settings.signup`, `invite` or `open`, replaces `settings.invites`,
  required and never defaulted. `invite_quota` and `invite_lifetime_days` are
  refused under `open`; every other setting, the code of conduct among them,
  applies in both modes.
* The hostname role `invite` is `join`, derived `join.<domain>` unless
  `hostnames.join` is declared, and every account app has it. Under `invite`
  the gateway passes `/i/*` and `/static/*` there; under `open`, exactly `/`
  for `GET` and `POST` and `/static/*`. Everything else is a 404, and the
  primary refuses `/i/*` in both modes.
* `account-serves-nothing` and `account-invite-hostname-without-invites` are
  removed.
* The toolkit renders the operators' usernames as `ACCOUNT_OPERATORS`, which
  the service refuses to ban, recover or rename.
* The operator reconciler also checks the reverse of its first check: every
  enabled member of `operators` is an operator in the yaml, and `/healthz`
  names each one who is not. It reads only.

### Why

Every account app now takes signups in one mode or the other, so there is no
account app without people who have no account yet, and no reason for their
hostname to be conditional. Naming it for what it does (joining) rather than
for one way of joining keeps the name true in both modes. Open registration
lives at `/` on `join.`, so the gateway passes exactly that path and nothing
under it: the member's home page at `/` on `account.` is a different origin.

An operator is removed by editing the yaml, but the group `operators` at
Pocket ID stays until someone takes it away, and the uptime monitor and any
other app that reads it for operator access would keep granting it. The
reconciler already read every user with their groups, so the reverse check
costs no request and keeps the reconciler read-only.

## 2026-10-14: One tier of community admin, and the account service changes groups

Founder decision. The design is
`docs/specs/2026-10-14-account-promotion.md`.

### What was decided

* Any community admin changes any group of any user, themselves and other
  admins included, at `/admin/members/<id>/groups` on `account.`. There is
  one tier of community admin.
* `operators` is never changed there: `paisans-stack.yaml` decides who is in
  it. An operator's other groups can be changed, and the next `deployment apply` puts
  the operator back in every group it gives operators.
* The account service's writes at Pocket ID, all with the static API key, are
  creating a user at signup (Pocket ID puts them in the member group as its
  signup default), disabling and re-enabling a user, setting a user's email
  address (marked verified) and names, sending a one-time access email, and
  replacing a user's groups with `PUT /api/users/:id/user-groups`. The last
  sends the list it has just read with the admin's additions and removals
  applied, and is refused if it would change membership of `operators`.
* A change takes effect at the user's next sign in, and every change is an
  audit entry: who, whose groups, each group added and each removed, and
  when.
* `docs/deployment-agent-rules.md` approves the account service under
  *Services the deployment runs*, with these writes, and lets `deployment apply` create
  a missing group only where the configuration names it: `operators`, the
  member group, each app's admin and member groups, and each group in the
  `pocket-id` app's `signup_default_groups` (founder decision, the same day).

### Why

Under `management: paisans` nobody is a Pocket ID administrator, so the
account service is the only place a member's groups change, and a group is
what grants privilege. Adding a member to a group and taking them out of one
are the same act, so one page does both. Sending the freshly read list with
only the requested changes keeps two admins working at once from undoing each
other. An operator is declared in a file and holds the deployment's keys, so
the group that says who is one follows that file and nothing else.

## 2026-10-14: Plaintext SMTP, to a private network only

Founder decision.

### What was decided

* An `smtp:` block, the deployment's or an app's own, may declare
  `security: none`: plaintext, with no TLS at all.
* It is accepted only when the resolved host is private: an IP address in
  loopback, RFC 1918, RFC 6598 or IPv6 unique local space, or inside
  `mesh.subnet`; or a name with no dot that starts with a letter, a compose
  service name. Anything else is refused, `smtp-plaintext-public-host`.
  Names are not resolved, so a dotted name is refused even when it would
  resolve privately, and the refusal asks for the mesh address.
* Every accepted `none` is a warning, `smtp-plaintext`: the mail and the SMTP
  credentials cross that network unencrypted.
* Pocket ID renders `SMTP_TLS=none`, the account service
  `ACCOUNT_SMTP_TLS=none`, Mbin `smtp://...?auto_tls=false`, and the monitor
  `smtp_secure: false`. A missing port is 25.
* Pocket ID and the account service refuse to authenticate over plaintext to
  any host but localhost, so `none` with a username is refused for them,
  `smtp-plaintext-with-username`.

### Why

A deployment may run its own relay, on the mesh or as a compose service
beside the app, which takes mail without TLS and forwards it over TLS. TLS
between the app and that relay protects nothing the private network does not
already protect, and requiring it means issuing the relay a certificate the
apps can verify. The risk of plaintext is accepted on a private network,
where only the deployment's own hosts can read the traffic, and nowhere else.

Validate cannot know what a name will resolve to on another host at another
time, so it judges only what it can read: an address, or a name that cannot
leave the compose network. A service name must start with a letter because a
resolver may read digits, or `0x08080808`, as an IPv4 address.

The consumers were checked at the versions the toolkit runs. Pocket ID
accepts `none` (`dto/app_config_dto.go` at v2.18.0) and its mailer then skips
STARTTLS (`emailer/smtp/smtp.go` in `italypaleale/go-kit` v1.2.4). The account
service accepts `none` and sends with go-mail's `NoTLS` policy
(`internal/config` and `internal/mail` at v0.2.0). Mbin's Symfony Mailer never
sends STARTTLS with `auto_tls=false` (`EsmtpTransportFactory.php` at
v7.4.14). The monitor's nodemailer 8.0.7 sends in plaintext with
`secure: false` when the server offers no STARTTLS
(`lib/smtp-connection/index.js`). Both Go mailers refuse credentials without
TLS (Go's `net/smtp` `PlainAuth`; go-mail v0.8.1 `smtp/auth_plain.go`), which
is the username refusal.

### What was weighed

* **Resolving the name at validate time.** Not chosen: the answer on the
  workstation is not the answer the app gets, and a name that resolves
  privately today can be repointed.
* **Letting Pocket ID and the account service send without TLS and drop the
  username.** Not chosen: a field-by-field override cannot unset an inherited
  username, and a relay that wants one would refuse every mail.

## 2026-10-14: Pocket ID's API is reachable only over the mesh

Founder decision. The design is
`docs/specs/2026-10-14-pocket-id-api-private.md`.

### What was decided

* Every site that runs HAProxy, in a deployment with a pocket-id app, serves
  the active Pocket ID instance on its mesh address at port 1413, checked at
  `/healthz` for 204, with one server per site that runs the app.
* The account service calls Pocket ID's API there:
  `ACCOUNT_IDP_URL=http://<site mesh address>:1413`. Its OIDC issuer stays
  `https://<pocket-id hostname>`.
* The gateway refuses any request to Pocket ID's hostname that carries an
  `X-API-Key` header, with 403, members' own API keys included.
* `pocketid-go` refuses a `BaseURL` whose host is not private.

### Why

The static API key is a standing administrator credential, and the gateway
proxied Pocket ID's whole hostname, so the key worked from anywhere on the
internet. Every caller but the account service already reached Pocket ID on
the mesh. Moving that one caller onto a mesh route lets the gateway refuse the
header outright; the web interface signs in with a session cookie and never
sends it. The client's private address check guards against a configuration
mistake, and the gateway rule is what holds when the code is wrong. The
listener is on every HAProxy site, not only where the account service runs,
so a later caller finds it there.

## 2026-10-14: `secrets rotate` asks for a pasted secret's new value

Founder decision. It amends `docs/specs/2026-10-12-secrets-rotate.md`, whose
approved scope left reissuing a pasted secret out.

### What was decided

* `paisans secrets rotate` rotates the pasted secrets, `external.acme_dns_token`,
  `external.smtp_password` and `apps.<app>.smtp_password`, as well as the
  generated ones, and `--all` includes them.
* When every host using one runs the value the secrets file holds,
  `--execute` asks for the new value at a terminal, with echo off, naming the
  path and its issuer, for every such secret in the run, before any host is
  claimed. The answer is stage 1's next value under `rotations`, and stage 4
  promotes it, as a generated secret's is. A resumed run does not ask again.
* One already set anew with `paisans secrets set` is not asked for; the
  file's value is handed out.
* An empty answer refuses the run with nothing changed, naming `secrets set`
  and `--replaced`. An unchanged answer is refused. With no terminal, the run
  is refused with the steps: replace it at the issuer, `secrets set` it, run
  again.
* Stage 2 tries a DNS token against the community's zone at the provider,
  read only, through `internal/dns`; a pasted or set token that fails is
  refused before it is written. An SMTP password has no check.
* `--replaced <path>` keeps its meaning: a pasted secret already replaced
  and applied is listed, not rotated.

### Why

Before this, a pasted secret every host ran was refused, and `--all` refused
the whole run until each one was replaced at its issuer and set with `secrets
set` beforehand. That made the one command meant to replace every secret after
an operator leaves stop on exactly the secrets an outsider is most likely to
use, and sent the operator to a second command for each. Asking at the
terminal keeps the run whole while the value still comes from the only place
that can issue it. All the questions come before any host is claimed, with
the `sign out` confirmation, so a run never waits on a person part way through
with some hosts already changed.

The DNS token is checked before it is written, not only at stage 2, because a
value under `rotations` is what a resumed run hands out: a mistyped token
written there could only be cleared by hand. The zone lookup is the read only
call `dns init` already makes, so it adds no new credential use. Nothing in
the toolkit logs in to a mail provider, so there is no SMTP check to reuse,
and the plan says so rather than imply one.

### What was weighed

* **Refusing the pasted secrets until set by hand**, the approved spec's
  scope. Not kept: it split one rotation into two commands and made `--all`
  stop on them.
* **Asking for each value when its rotation is reached.** Not chosen: a run
  would pause part way with earlier secrets already changed, and an operator
  who walked away would come back to a half finished run.

## 2026-10-14: SMTP credentials and settings are tested

Founder decision. It replaces, for SMTP passwords, the line of *`secrets
rotate` asks for a pasted secret's new value* above that said an SMTP password
has no check.

### What was decided

* **A check** takes an app's effective SMTP settings (`config.SMTPFor`) and
  its password (`apps.<app>.smtp_password`, else `external.smtp_password`),
  connects, does TLS or STARTTLS as `security` declares with the certificate
  verified (`none` stays plaintext, which `validate` allows only toward a
  private or mesh relay), and logs in with the username and password.
* **It connects from a site running the app**, through the toolkit's own ssh
  (`ssh -W host:port`), so the site's real egress is tested and a mesh or
  private relay is reachable. The SMTP conversation runs on the workstation,
  in Go; nothing is installed on a host. Apps with the same settings share a
  check.
* **When:**
  1. `secrets rotate`, for an SMTP password: the next password logs in and
     sends one test message before any app is handed it; a pasted one is
     tried when pasted, one set with `secrets set` when the run is planned,
     so a bad value is never written under `rotations`.
  2. `secrets set`, for an SMTP password, keyed or in the owed walk: logs in
     and sends one test message before the value is written; a failure
     writes nothing.
  3. `deployment apply`: logs in only, sending nothing; a failure refuses the apply
     before any host is changed, naming the app, the setting and the
     server's reply, never the password.
  4. `validate`: logs in only; a failure warns.
* **The test message** is sent only by `secrets set` and `secrets rotate`,
  for a changed credential: to the operators' addresses the configuration
  declares, from the app's effective from address, short and plain, naming
  the deployment, the app and that it is a Paisans SMTP test. With no
  recipient, it is refused with an explanation.
* Passwords are registered with `ui.Secret` and never printed.

### How it was carried out

* **Recipients are every operator**, by email, in the order of their names,
  not the one running the command: a changed mail credential concerns every
  operator, each can confirm the message arrived, and nothing in the
  configuration says reliably which operator is at the keyboard.
* **One check per site and settings for `deployment apply` and `validate`; one per
  sender, from the first site running one of its apps, for `secrets set` and
  `secrets rotate`.** The first tests each host's own route, which is what
  `deployment apply` is about to depend on; the second tests a credential, and one
  message per sender is enough to prove the provider takes it.
* **A password `secrets set` wrote is only logged in with by a rotation.** It
  sent its test message when it was set, so the plan and stage 2 log in and
  send nothing, and a run's dry run sends no mail.
* **`validate` keeps working offline.** A site it cannot reach, or a secrets
  file it cannot read, skips the check with a warning; `--offline` skips it
  without trying. `deployment apply` is the gate that refuses.
* **`deployment apply` checks in its dry run too**, before the host check: the plan is
  what `--execute` would do, and a mail failure is where it would stop.
* **Settings the site's host cannot reach are warned about, not tried**: a
  compose service name or a loopback address is the app's own network.

### What was weighed

* **Dialling from the workstation.** Not chosen: it tests the operator's
  network rather than the host's, and cannot reach a relay on the mesh or a
  site's private network.
* **Running a mail client on the host** (`swaks`, `openssl s_client`, a
  container). Not chosen: it installs or runs something on a host for a
  check, and puts the password in a remote process.
* **Sending the test message on every `deployment apply`.** Not chosen: it mails the
  operators on every run against settings that already worked.
* **Mailing only the operator running the command.** Not chosen: nothing in
  the configuration names them reliably.

## 2026-10-14: What each Wiki.js group may do, and drift is reported

Founder decision. The design is
`docs/specs/2026-10-14-wikijs-groups-and-drift.md`; it amends the groups and
bootstrap sections of `docs/specs/2026-10-13-wikijs-blueprint.md`.

### What was decided

* **The admin group gets a page rule as well as its permissions**,
  `paisans-admin`, granting every page role everywhere, rewritten on every
  run. Wiki.js checks a page rule after the permission, and a group created
  through its API has a rule granting only reading and comments, so the admin
  group could not write a page.
* **The member group is seeded once**, in the run that creates it: members
  read and edit pages, their source and history, assets and comments. They do
  not move, rename or delete pages, nor write scripts or styles. After that
  the group is the admins'.
* **`admins/` is for admins only**, by a member rule whose regex excludes it.
  No seeded rule is a deny, because an operator is in both groups and a deny
  wins a tie.
* **A guest reads only what `guest_paths` names**, read only, and only on an
  ungated wiki. `Guests` is written on every run.
* **The bootstrap polls every fifteen minutes** and reports drift in what it
  writes on every run on its `/healthz` and to the monitor. It does not put
  the configuration back until its next start, and drift does not make its
  container unhealthy.
* **The bootstrap pushes a heartbeat to the monitor** every minute: success,
  or a failure carrying what drifted. A catalog blueprint may declare a
  heartbeat, and Paisans generates its token, seeds its push monitor and
  rotates it.
* **`paisans doctor` fails a running container whose healthcheck reports
  unhealthy**, for every blueprint.

### Why

A wiki members cannot write in is not a wiki, and an admin who cannot create
a page has nothing to fix it with short of the break glass account. Seeding
members once gives a new wiki a working default and leaves what members may do
to the community. A guest's access, unlike a member's, decides what the
internet reads, so it is the operator's, written down, and enforced. A change
undone silently at the next restart is worse than one refused, so the
bootstrap says when Wiki.js and the configuration disagree, and the operator
decides which one is right.

### What was weighed

* **Seeding members on every run.** Not chosen: every change an admin made to
  members in Wiki.js would be undone at the next restart.
* **Leaving members at Wiki.js's defaults.** Not chosen: every new wiki would
  need an admin to set up editing by hand before anyone could write.
* **Members moving and deleting pages.** Not chosen: neither is undone from
  history.
* **A deny rule on `members` for `admins/`.** Not chosen: it locks out every
  admin who is also a member, which is every operator.
* **An ungated wiki public throughout.** Not chosen: a wiki could not then be
  ungated and private.
* **Putting drifted settings back at once.** Not chosen: it hides that
  someone changed them, and fights an admin who has not yet been told why
  their change keeps disappearing.
* **Reporting drift in `deployment apply`.** Not chosen: `deployment apply` recreates the stack and
  the bootstrap converges, so there is nothing left to report.
* **The monitor checking the bootstrap's `/healthz` over the mesh.** Not
  chosen: it needs a published port on every apps site, a way to choose one
  where two wikis share a site, and a new kind of check in the blueprint
  schema, where a push needs only a token.
* **A curl sidecar pushing for the bootstrap**, as for the operator
  reconciler. Not chosen: one more container per wiki, on hosts that may be
  short of memory, for a process that already runs and holds the state.
* **Drift making the container unhealthy.** Not chosen: `deployment apply`'s health gate
  would then fail an `deployment apply` that left a drifted bootstrap running.

## 2026-10-15: A removal that keeps data keeps the registry entry

Founder decision. It amends `docs/specs/2026-10-10-remove-without-config.md`,
which left a deployment with no registry entry out of `--id`'s scope.

### What was decided

* A host stage of `site remove` that keeps data, files under
  `/srv/paisans/<token>` that apply did not write or named volumes, because
  `--delete-data` was not given, keeps the deployment's registry entry,
  marked `"kept": "data"`, instead of deleting it. A host with no entry gets
  one.
* A later run with `--delete-data`, by `--id` or with a configuration,
  deletes the data and then the entry. A later run without it leaves the
  entry as it is.
* `host deployments` shows the entry as `data kept`, with the command that
  deletes it. A claim by the same id writes the entry without the mark, as
  for a kept Caddy.

### Why

Every removal that kept data said `--delete-data deletes it`. With no
configuration that was false: `--id` finds the deployment by its registry
entry, and the first run had deleted it, so the second was refused with
`no deployment ... has id or token`, and the data could only be deleted by
hand. The entry is the proof the host was claimed by that id, and the data is
still the deployment's, so the claim is not over while the data is there.
Keeping it also stops another site of the same deployment being deployed
onto the data unannounced: the claim check names what the removal kept.

## 2026-10-15: `dns init` writes DMARC for send-only names and for hostnames below the registrable domain

Founder decision. It amends *`dns init` publishes the no-mail records* above,
where a domain with mail had its DMARC policy checked and never written, and
where the domain's `sp=reject` was taken to cover every hostname.

### What was decided

* Every mail name under `community.domain`, not only the domain itself, has
  its DMARC policy checked.
* A mail name with no MX record and no DMARC policy gets
  `v=DMARC1; p=reject; sp=reject;` written at its `_dmarc`. An existing
  policy, however weak, is still a note and never replaced; a missing one at
  a name with an MX record is still a note. `dns prune` never deletes it.
* When `community.domain` is below its registrable domain under the public
  suffix list, every hostname that gets an address record also gets the
  lockdown DMARC policy at its own `_dmarc`, except one that is itself a
  registrable domain. `dns prune` treats it like the hostname's SPF.
* The registrable domain above `community.domain` has its DMARC policy
  checked and warned about, never written, and not checked at all when the
  DNS provider does not hold it.

### Why

A name with no MX receives no mail, so its mail is a relay sending in its
name, and nobody runs a mail service there whose policy `dns init` would be
overriding. The note asked the operator to write by hand a record the toolkit
could write itself. Relaxed alignment is what keeps the deployment's own mail
delivered: a relay commonly signs with a subdomain it has verified, which
relaxed alignment accepts for any name under the same registrable domain.
Prune keeps it because the deployment's mail depends on it, and removing a
reject policy only weakens the name.

A receiver following RFC 7489 that finds no policy at a name's own `_dmarc`
looks at the registrable domain, never at the names between. A staging
deployment at `staging.example.org` therefore had its hostnames governed by
`example.org`'s policy, which the toolkit neither writes nor checked, and
its own `sp=reject` reached none of them. A record per hostname is within the
names the toolkit already writes SPF at. The registrable domain is not: it
holds names other services send from, and a missing MX there says nothing
about them, so a reject policy written there could refuse their mail.

### What was weighed

* **Writing the registrable domain's policy when it has no MX.** Not chosen:
  the toolkit cannot see which names under it send mail.
* **The zone apex as the registrable domain.** Not chosen: a delegated
  subzone's apex is not a registrable domain, and receivers use the public
  suffix list.
* **Strict alignment for a send-only name.** Not chosen: a relay signing as
  a sibling name would have the deployment's own mail rejected.

### What was run

`go build ./... && go vet ./... && go test ./...`; the new tests in
`internal/dns/mail_test.go` were checked to fail before the change.

## 2026-10-15: An operator overrides the infrastructure stack's images

Founder decision, 2026-10-15. Spec: `docs/specs/2026-10-15-infra-image-overrides.md`.

### What was decided

* `infra.images` (etcd, haproxy, garage, heartbeat) and
  `infra.postgres.<major>.spilo` override the core catalog foundation's
  images, with `apps.<name>.images`' semantics.
* Spilo's override is keyed by Postgres major, so a build of one major never
  follows the cluster onto another, and any provider works.
* An override is always reviewed at apply.
* `paisans images` lists each site's builds and what would move; an override
  behind the release it replaced is a warning.

### Why

A security fix outside a foundation track waited for a core catalog release,
and an operator had nothing to write that moved the infrastructure sooner.

### What was weighed

* **One key per subsystem** (`etcd.image`, `storage.garage.image`, ...): next
  to related settings, but scattered, with no one place to audit.
* **An operator-side overlay of `foundation.yaml`:** complete, but a second
  path beside `apps.<name>.images` and `acme.image` for the same images.
* **One flat `infra.images.spilo`, checked by reading the major from the
  repository name:** works for Zalando's naming only.
* **An override for a pinned app's own Postgres:** left out.
* **The stale-override check in validate:** validate reads the file alone and
  reaches no registry.

## 2026-10-16: `host admit` enables ufw when the owner says so

Founder decision, 2026-10-16. Spec: `docs/specs/2026-10-15-rootless-and-host-admit.md`, step 9.

### What was decided

* With `--enable-ufw`, or a yes at a terminal, admit enables an installed,
  inactive ufw: an allow for every TCP port sshd listens on, tagged
  `paisans: ssh`, after the deployment's own rules; a 10 minute
  `systemd-run` timer that runs `ufw disable`; `ufw --force enable`; and the
  timer stopped once a new SSH login gets through.
* Without it, admit refuses, naming `--enable-ufw` first, then the commands
  that enable ufw by hand, numbered, every allow before the enable.
* Admit does not enable ufw while it holds any deny or reject rule of the
  owner's, in any form, nor with `IPV6` other than `yes` in
  `/etc/default/ufw` while sshd listens on IPv6; the refusal says why, and
  no question is asked.
* Evict never disables ufw and never removes a `paisans: ssh` allow.

### Why

Admit used to refuse an inactive ufw and tell the owner to allow their SSH
port and run `sudo ufw enable`. An owner who ran only the enable was locked
out of SSH. Allowing the ports sshd actually listens on, with a fallback that
turns ufw off again, takes the lockout out of the owner's hands.

### What was weighed

* **Keep refusing, with better instructions:** the instructions stay, for
  the owner who wants to do it by hand, but a refusal is still a step to get
  wrong.
* **The ports from `sshd -T`:** under socket activation, Ubuntu 24.04's
  default, systemd holds the listening socket and sshd's Port lines say
  nothing of it, so the sockets are read with `ss` instead.
* **Parse the owner's deny rules, or insert admit's allows first (`ufw
  insert 1`):** parsing every rule form is open-ended, and inserting first
  overrides an owner's deliberate deny.
* **Enable without a fallback:** a cloud firewall or a rule admit cannot see
  would still lock the owner out, with nothing to undo it.

## 2026-10-17: `dns init` writes the registrable domain's DMARC policy when nothing under it has mail

Founder decision. It amends *`dns init` writes DMARC for send-only names and
for hostnames below the registrable domain* (2026-10-15), where the
registrable domain's policy was only checked and every hostname carried its
own.

### What was decided

* When `community.domain` is below its registrable domain, `dns init` creates
  the lockdown policy at `_dmarc.<registrable domain>` when the provider holds
  that zone, there is no DMARC policy there, every zone delegated from it is
  held by the provider, and no name in those zones has mail and no policy of
  its own. A name has mail when it holds an MX record other than the null MX,
  an SPF record naming a sender, or a DKIM key at a selector under it, or
  when the configuration names a sender there. The registrable domain never
  has a policy of its own, and neither does a hostname whose policy the new
  one would cover.
* A policy there covers only when it is the only one there and rejects. More
  than one, or a CNAME delegating the policy to a DMARC service, is a note
  and covers nothing.
* A policy there covers only with strict alignment, as the hostnames' own
  have: under relaxed alignment a forger passes with any sibling name's SPF,
  such as a relay's include on a name that sends mail. A name between that
  holds a policy not as strict, delegates it by a CNAME, or is delegated to
  nameservers the provider does not hold, covers nothing either.
* `dns prune` asks `dns init`'s plan which hostname policies are covered, so
  the two never disagree.
* A name that is a CNAME has its target's mail, which `dns init` resolves
  through public DNS; a lookup that fails other than by the name not
  existing counts as mail.
* `dns init --execute` creates that policy after every other record, since
  it rests on the policies the same run creates. A policy only covers once it
  is there: the run that creates it still gives each hostname its own, and
  `dns prune` removes them after.
* A hostname is not covered while a name between it and the registrable
  domain holds a weaker policy, which DMARCbis' tree walk reads first.
* Once written, the zone is not read again. The policy is a conflict when a
  sender the configuration names has no policy of its own, or the domain
  itself has an MX or an SPF record naming a sender; mail added elsewhere
  under the domain later is whoever adds it's to give a policy.
* `dns prune` judges a covering policy without any record of this
  deployment's the same prune deletes, and `--name` reaches the registrable
  domain's policy once the configuration no longer produces it.
* Where the registrable domain's policy rejects, whoever wrote it, no
  hostname gets `_dmarc.<hostname>` and `community.domain` gets no
  `_dmarc.<community.domain>`; `dns prune` removes the ones this deployment's
  `dns init` made. Where it does not, each still gets its own.
* `dns prune` never removes the registrable domain's policy that `dns init`
  wrote while the configuration still produces it.
* That policy is a note once a name under the domain has mail and no policy
  of its own.

### Why

A receiver applies the registrable domain's policy to every name under it
with none of its own, however deep, so one record there does what a record at
every hostname did. The objection on 2026-10-15 was that the toolkit could
not see which names send mail. It reads every record in the zone, and in each
delegated zone, so it can: an MX, an SPF record naming a sender and a DKIM key
are what a name with mail publishes, and a name with its own DMARC policy is
not governed by the registrable domain's at all. Where any of that is out of
sight, it writes nothing there and falls back to a policy per hostname.

### What was weighed

* **Only the registrable domain's MX.** Not chosen: a subdomain sending
  through a relay publishes no MX at the registrable domain, and its mail
  would be rejected.
* **Keeping the hostnames' policies beside it.** Not chosen: they say what
  the registrable domain's already says, and are records to drift.

### What was run

`go build ./... && go vet ./... && go test ./...`; the new tests in
`internal/dns/mail_test.go` were checked to fail before the change.

## 2026-10-17: `dns init` publishes SPF on the domain, not on every hostname

Founder decision. It amends *`dns init` publishes the no-mail records*, where
every name that gets an address record also got `v=spf1 -all`.

### What was decided

* `v=spf1 -all` is published on `community.domain`, and on a hostname only
  when the hostname is itself a registrable domain.
* Every other hostname relies on DMARC. One whose registrable domain's
  policy is the deployment's own lockdown policy gets nothing. One whose
  registrable domain's policy `dns init` only checks or may not write, above
  `community.domain` or at a `community.domain` with mail of its own, gets
  the lockdown policy at its own `_dmarc` while that policy does not reject,
  and none once it does.
* `dns prune` removes the hostnames' SPF records and covered DMARC policies
  this deployment's `dns init` made, as records nothing wants.
* `dns init` still reads each hostname's SPF record, and one that lets a
  server send is a conflict, as before: the hostname has mail nobody listed,
  and the DMARC policy covering it would reject that mail.

### Why

A forged message from a hostname is refused by DMARC alone at every receiver
that enforces it: with no SPF record the SPF result is none, not a pass, and
the forger has no aligned DKIM signature. SPF checks the envelope sender, not
the From a person reads, so a forger who wants past SPF uses another
envelope. A record per hostname, for every app and every media hostname,
bought protection only at receivers that check SPF and ignore DMARC, and
counted against the provider's per zone record quota. A hostname that is a
registrable domain is the exception: nothing the deployment may write gives
it a DMARC policy, and SPF reaches that name alone.

### What was weighed

* **A wildcard SPF record.** Not chosen: a wildcard reaches no name that
  holds any other record, so it covers no hostname with an address.
* **Hostnames as CNAMEs of one gateway name, inheriting its SPF.** Not
  chosen: gateway moves add and delete address records per hostname.

### What was run

`go build ./... && go vet ./... && go test ./...`.

## 2026-10-17: One operator is a warning, not a refusal

Founder direction, issue #365.

### What was decided

* `validate` refuses a deployment with no operator whose keys are on every
  site that runs Pocket ID (`operators-none`), and warns about one
  (`operators-fewer-than-two`).
* `paisans create` accepts one operator and offers a second after the first.
* The operator reconciler fails its pass while fewer than two operators are
  declared, so the operators heartbeat keeps the monitor alerting until a
  second is added.

### Why

A deployment being rolled out, or a small community, may have one operator
for a while, and refusing it left no way to start. One operator is a risk,
not an incoherence: a lost workstation would leave nobody able to run the
deployment, so it stays visible on every `validate` and on the health page
until there is a second. No operator at all is an incoherence, since nobody
could run the deployment, and stays a refusal.

### What was weighed

* **Keep refusing one:** blocks the first rollout and small communities
  outright, which is the problem the issue names.
* **Warn in `validate` only:** a warning shown once at `validate` is easy to
  forget; the heartbeat keeps saying it.

## 2026-10-17: `--yes` answers confirmations, except the digest and the fresh SSH login

Founder decision, 2026-10-17: every command that asks for a confirmation takes `--yes`, which answers each typed word and y/N question as typing it would, prints `approved by --yes: <what>` for each, and refuses unless `PAISANS_ALLOW_YES=1`; it never answers `host admit --request`'s digest, which proves the request's keys were not changed on their way (`--digest` gives it instead), nor the fresh SSH login after admit enables ufw, which proves SSH still works before the ufw fallback is stopped (`--yes` with `--request` refuses where admit would enable ufw, and `--site` proves it with its own login); a review `--yes` approves records `approved_by` as a typed approval does, so only the run's log shows it was `--yes`.

## 2026-10-17: An encrypted secrets file without `.sops.yaml` is refused, not re-encrypted

Issue #423.

### What was decided

* A write of a secrets file that was encrypted, by its sops metadata on disk
  or as it was read, with no recipient in the `.sops.yaml` beside it, is
  refused before anything changes. The refusal names the recipients the
  file's sops metadata records and, when no `.sops.yaml` exists, the command
  that writes one with them and refuses to overwrite a file already there.
* Nothing is written in plaintext except in the test suite's explicit mode
  (`docs/development.md`, *Fixtures and secrets*), which never writes an
  encrypted file back in plaintext.
* A plaintext `secrets.enc.yaml` is refused wherever a deployment reads it,
  outside that mode. Refusals never name the mode: they give the real fixes.

### Why

`.sops.yaml` says who may decrypt, and it is part of every published version,
so a deployment directory without it cannot be applied or published anyway.
Refusing changes nothing on disk and costs the operator one command, which
the refusal prints. Plaintext secrets under a name ending `.enc.yaml` are one
`git add` away from being committed, so no default may produce them.

### What was weighed

* **Re-encrypt to the recipients in the file's own sops metadata:** keeps the
  file encrypted, but writes to a recipient set nobody confirmed (one removed
  from `.sops.yaml` on purpose would be kept), and the run still stops at
  publishing for the missing `.sops.yaml`, after secrets were generated.

## 2026-10-17: Ubuntu 26.04 is supported, with the original sudo

Founder request, 2026-10-17: `site admit`, `site prepare` and the rootless commands accept Ubuntu 26.04 through the shared `ubuntu` profile, checked in an `ubuntu:26.04` container (systemd 259 delegates cpu, memory and pids as 24.04's does, apparmor ships the rootlesskit profile, Docker publishes a `resolute` suite); 26.04 makes sudo-rs `sudo`, which honours `Defaults` env_keep and setenv for a user without listing them under `sudo -l`, so admit keeps refusing it and its refusal names `update-alternatives --set sudo /usr/bin/sudo.ws` where the original is installed.

## 2026-10-17: Where sudo is sudo-rs, admit and doctor read sudo.ws's listing

Founder decision, 2026-10-17: where `sudo` is sudo-rs, `site admit` and `doctor` read the rules and Defaults that apply to the deployment's user from `/usr/bin/sudo.ws -n -l`, the original sudo, which Ubuntu 26.04's `sudo` package installs beside sudo-rs and which reads the same sudoers files; with no `sudo.ws`, admit refuses and doctor warns. It rests on `sudo.ws` listing every Defaults entry sudo-rs applies, observed in an `ubuntu:26.04` container (sudo-rs 0.2.13): sudo-rs applied a user's `setenv` and `env_keep`, which `sudo.ws` listed, did not apply a command-specific `env_keep` of `LD_PRELOAD`, which `sudo.ws` also listed, and with the stock sudoers passed none of the variables a root shell script trusts. A probe integration test checks this against the image's sudo-rs. Owners no longer switch `sudo` with `update-alternatives`.

## 2026-10-18: The uptime monitor's public status page is on, and shows only public checks

Founder decision, 2026-10-18.

### What was decided

* The uptime blueprint serves the fork's public status page by default:
  the seed sets `status_page_enabled` true and the edge passes `/status*`.
* Only each app's check at its public hostname is shown. Every seed entry
  names `status_page_excluded`, so a check that names a mesh address, a site
  or a token is hidden at every boot.
* A monitor made by hand starts hidden: the fork defaults
  `status_page_excluded` to 1 wherever the maker does not say otherwise.
* `settings.public_status_page: false` turns the page off in the monitor and
  at the edge.

### Why

A community's members are the people who notice an app is down, and the page
answers "is it just me" without an account or an operator. What made the
page unsafe was its content, every monitor with its URL, mesh addresses
included, not the page itself. Public checks request names already in public
DNS. A hand made monitor can point anywhere, so it is hidden until an admin
decides otherwise.

### What was weighed

* **Keep the page refused:** safe, but members learn of an outage only from
  an operator.
* **Show every monitor and leave hiding to the admins:** publishes the mesh
  layout the moment the page is on, before anyone has hidden anything.

## 2026-10-18: Apply keeps src_valid_mark at 0 in the rootless namespace

Founder direction, 2026-10-18.

### What was decided

* `deployment apply` writes 0 to every `src_valid_mark` under
  `/proc/sys/net/ipv4/conf` in RootlessKit's network namespace (`all`, each
  device's and `default`), as the deployment's user, when one reads 1, before
  any stack moves.
* It installs a user drop-in for `docker.service` whose `ExecStartPost` writes
  them again at every start of the daemon, prefixed `-` so it can never fail
  the start.
* `paisans doctor` fails a site whose namespace holds 1, with apply as the
  recovery.

### Why

Observed on two hosts running Tailscale, which sets the host's value to 1. The
namespace copies it, RootlessKit's transparent dial then loses every reply,
and each new connection to a published port waited one second before a plain
dial took over (traced with bpftrace: `ip_route_input_noref` returned
`-EINVAL` for the marked SYN-ACK). The plain dial also hid the client, so apps
saw the bridge's gateway and ignored `X-Forwarded-For`. Setting the value to 0
in the namespace alone fixed both, from 1.003 s to 0.003 s per connection,
and leaves the host's value, which Tailscale needs, as it is. The namespace is
the user's, so no root and no admit is needed, and the apply the operators
already run fixes a site.

### What was weighed

* **Raise the Docker floor to the release with RootlessKit 3.2:** tried on
  the affected host; RootlessKit 3.3.0 waited the same second, since the
  cause is the inherited value, not the firewall backend.
* **Turn RootlessKit's transparent mode off:** ends the wait but leaves every
  app seeing the bridge's address as every client.
* **Have site admit write the drop-in:** needs the host's owner for a change
  inside the deployment's own namespace.

## 2026-10-22: dns init updates the address records it made for this deployment

Founder direction, 2026-10-22 (issue #399).

### What was decided

* `dns init` updates an A or AAAA record to the address the configuration
  declares at its name when the record carries exactly this deployment's
  comment, is unproxied, is the only record of its type at the name, points
  at no former gateway, and leaves the name holding exactly what is wanted
  once changed. Every other record that disagrees is a conflict, and any
  conflict stops every write, updates included.
* The update writes the record as a create does: unproxied, with the
  provider's automatic TTL, and the deployment's comment. A TTL set by hand
  on such a record is reset.
* A hostname moving to a monitor site is updated in place, with no overlap:
  the record switches from the gateway's address to the monitor's in the
  apply that runs `dns init`.
* Records for a gateway role that moves to another site still wait for
  `gateway move`, which overlaps the two gateways, as long as the old site
  stays declared with its `public_address` until the move finishes. `dns
  init` knows a former gateway only by a declared site's address; once the
  old site's address, or the site, is gone from the configuration, a moved
  role reads exactly as a changed address, and `dns init` updates its records
  to the new gateway before that gateway is applied. The README says so where
  it describes the move, and a test pins the behaviour.

### Why

A gateway whose `public_address` changed left every record `dns init` had
made for it pointing at the old address. Each was a conflict, so the run
created nothing until the operator deleted the deployment's own records by
hand at the provider. The comment says whose a record is; requiring it
exactly, and requiring the name to end up holding only what is wanted, keeps
every record someone else made, or another deployment made, untouched. An
address change on one site has no second machine to overlap with, and a
monitor's hostname is served by the monitor from the apply that moves it, so
the switch is the change the operator asked for.

### What was weighed

* **Keep `dns init` create-only:** every address change became a manual
  deletion of the deployment's own records at the provider.
* **Keep the record's TTL:** Cloudflare's update requires a TTL, and its
  automatic setting reads back as 300 seconds, so the record's own setting
  cannot be told apart from automatic; `dns init` writes automatic
  everywhere else.

## 2026-10-25: A homeserver's delegation may share another app's hostname

A community's apex is the name its members want in their Matrix identifiers,
and it is usually already the community's own site, the account app or a blog.
Refusing the second claim made that combination impossible: the operator chose
between the identifiers they wanted and the site they had. Founder decision.

The delegation documents are paths under `/.well-known/matrix/` and nothing
else, so the gateway renders one host block for the name and routes those
paths to the homeserver's `wellknown` snippet inside it, with every other path
left to the app that holds the name. The toolkit owns the path, as
`blueprints.WellknownPath`, beside `WellknownRole`, and wraps the snippet in a
handle for it, so the snippet's own catch all 404 answers only for unknown
documents under `/.well-known/matrix/` and the core catalog needs no change.

The shared handle is written first in the block. Caddy sorts a handle with one
path ahead of a bare one, but treats a named matcher of several conditions as
having no path and keeps such handles in the order written
(`caddyconfig/httpcaddyfile/directives.go`, `sortRoutes`, read at v2.11.7). A
gated block's bypasses are such handles, so only being first reliably puts the
delegation ahead of them and of the gate. Rendered and served by Caddy 2.11.7
on 2026-10-25: the two documents answered from the homeserver's snippet, and
`/`, `/.well-known/openid-configuration` and `/i/<token>` reached the account
app's.

Only that claim is shared. Its owner must be another app's primary hostname,
because a media or join hostname's snippet answers every path itself and a
homeserver's own hostname already serves its delegation. Both apps must be
routed by one Caddy, because an app on a monitor is served there and the
delegation would be on the gateway under the same DNS name. And one name holds
one homeserver's delegation, because a user identifier names one homeserver.
