# Repository split: the CLI, the Pocket ID client and the operator reconciler

Status: approved in session by the founder, 2026-10-14. Not implemented.

This repository holds two programs. `cmd/paisans` is the toolkit an operator
runs on a workstation. `cmd/operator-reconciler` is a service every deployment
runs beside Pocket ID, published as
`ghcr.io/paisans-software/operator-reconciler`: the read-only operator
reconciler of `docs/specs/2026-10-14-operators-and-management.md`. They share
one Go module, one tag namespace and one set of workflows, so the reconciler's
image workflow answers to `operator-reconciler-v*` tags beside the toolkit's
own `v*` releases.

The split gives each program its own repository, and gives the code both of
them need, the Pocket ID API client, a repository of its own:

| Repository | Holds | Published as |
|------------|-------|--------------|
| `Paisans-Software/paisans-cli` | `cmd/paisans` and everything only it uses. This repository, renamed. | the `paisans` binary |
| `Paisans-Software/pocketid-go` | the Pocket ID API client and its types | Go module `github.com/paisans-software/pocketid-go` |
| `Paisans-Software/paisans-operator-reconciler-service` | the reconciler and its DNS continuity check | `ghcr.io/paisans-software/operator-reconciler` |

`paisans-account-service` moves onto `pocketid-go` as well, so the toolkit, the
reconciler and the account service speak to Pocket ID through one client.

## Settled

Founder decisions, 2026-10-14, in session:

* **The operator reconciler gets its own repository.** It is a service with its
  own image, its own release cadence and its own approval as a service the
  deployment runs, so it ships like the other services
  (`paisans-account-service`, `wikijs-bootstrap`).
* **A service's repository is named `paisans-<what it does>-service`.** A
  service is a container a deployment runs, beside an app or as an app. A
  library, a catalog, a tap and the CLI are not services and keep names of
  their own. The reconciler's repository is
  `paisans-operator-reconciler-service`.
* **The Pocket ID client is shared through a module of its own,** not copied
  into each program. Its types mirror Pocket ID's API, so a change on Pocket
  ID's side is fixed once, and one contract test covers every program that
  calls it.
* **The shared module holds the Pocket ID client and nothing else.** It is
  named for that one job. Code two programs happen to share does not go into
  it on that ground alone; it earns a module of its own the same way this one
  did.
* **The operator reconciler's first release comes from its own
  repository.** It is a plain `v0.3.0` there, after steps 1 and 2 of *Order*;
  no `operator-reconciler-v*` tag is made in this repository. The core catalog
  and toolkit releases that carry the operators change wait for it. Outside
  this repository a release needs no prefix, since nothing else shares its
  tags.
* **This repository is renamed `paisans-cli`,** with its Go module path. The
  product is the paisans CLI, `paisans` on the command line, and the
  documentation calls it that.
* **The deployment's file stays `paisans-stack.yaml`.** It describes the
  stack being deployed, not the tool deploying it.

## `pocketid-go`

The module is `internal/pocketid` as it stands, made public: `Client`, its
`Transport` and `HTTP` paths, `Group`, `User`, `NewUser`, `OIDCClient`,
`NewOIDCClient`, `ClientSecret`, `APIError`, `Redact`, `ValidGroupName` and the
methods on `Client`. One package, `pocketid`, at the module root.

`emaillog.go` stays in the CLI. It reads a deployed Pocket ID's container log
through `docker compose logs`, which is how the toolkit operates a host, not
part of Pocket ID's API. It moves to `internal/pocketidlog`.

The module imports nothing from the CLI today and must not start: `Transport`
is declared in the client so that it depends on a shape, and the CLI's
`apply.SSHTransport` satisfies it from outside.

**Every method takes a `context.Context` first.** Founder decision. A caller
can then cancel a call or bound it: the account service passes the request
it is serving, so a Pocket ID call ends when the visitor leaves or the server
shuts down, and the reconciler passes its pass's deadline. The `HTTP` path
hands the context to `http.NewRequestWithContext`. The `Transport` path
checks it before each call; `Transport.RunInput` takes no context, so a curl
already running on a host runs to its end. The context goes in at
extraction, in `v0.1.0`, so each caller is edited once.

**The CLI makes every Pocket ID API call through `pocketid-go`.** No command
builds its own request to Pocket ID's API. *CLI calls to Pocket ID* below
lists what the CLI reaches Pocket ID with today and what each becomes.

**The client sends the key only to a private address,** from `v0.1.0`.
Founder decision. `docs/specs/2026-10-14-pocket-id-api-private.md` has the
rule: a `BaseURL` whose host is not private is refused before anything is
sent. Every caller in this repository already uses a mesh address, and the
reconciler a compose hostname, so step 1 changes no behaviour. The CLI gives
each client the deployment's `mesh.subnet` as a private network, so a mesh
outside the private ranges still reaches Pocket ID.

What the module carries:

* **Pocket ID's major version.** The module is written against Pocket ID v2,
  and its README says so. A Pocket ID major release is a `pocketid-go` major
  release.
* **The integration test,** behind the `pocketid_integration` build tag,
  against the Pocket ID image the `pocket-id` blueprint's track resolves to
  today. It moves from `internal/operatorreconciler/integration_test.go`,
  keeping only what exercises the client; the reconciler keeps its own.
* **Its own CI:** `go vet` and `go test` on every pull request, and the
  integration test on `develop`.

Releases are tags `vX.Y.Z`, `0.x` while the API settles. Each program depends
on a tagged release, never a pseudo version, so `go.mod` records what was
tested together.

## CLI calls to Pocket ID

Every authenticated call the CLI makes to Pocket ID's API already goes through
`pocketid.Client`, over the `Transport` path: ssh to the site whose instance is
active (or the one `--site` names), and `curl` there against
`http://<mesh address>:1411`, with the key from the workstation's decrypted
`secrets.enc.yaml`. Three places build a client: `clientAPI` in
`cmd/paisans/clients.go` (apply's identity step, `oidc client create`,
`app remove`, `secrets rotate`), `internal/appadmin` (declared operators,
`operator magic-link`), and the reconciler (the `HTTP` path, on the compose
network).

Three things reach Pocket ID without the client:

| Where | What | In `pocketid-go` |
|-------|------|------------------|
| `internal/apply/standby.go`, `instanceCommand` | One shell command per site: is the stack there, is its container a standby (a marker file read with `docker exec`), does `/healthz` answer on the mesh port | No. The standby marker is container state, not API, and reading it in the same command as the health check keeps the probe to one ssh round trip. |
| `internal/pocketid/emaillog.go` | Reads Pocket ID's container log for a failed login link email | No. Pocket ID reports the failure only in its log. It moves to `internal/pocketidlog` (*`pocketid-go`*). |
| `internal/failover/failover.go` | Anonymous HTTPS from the workstation to `/.well-known/openid-configuration` at the public hostname | No. It is failover's gateway probe, which asks every app's hostname the same question and only picks Pocket ID's path; it carries no key. |

`internal/gatewaymove/verify.go` also requests Pocket ID's `/healthz` at its
public hostname, pinned to a new gateway's address. That is a probe of the
gateway, and it stays there.

### Which calls could go over HTTPS

Pocket ID's whole API is reachable from the workstation at
`https://<hostname>/api/...`: its Caddy snippet is a plain `reverse_proxy`,
the gate never covers it, and the gateway's failover sends a request to the
active instance. The account service already calls the API that way with the
same key. A command that only needs the active instance could therefore use
the `HTTP` path from the workstation and skip ssh for its Pocket ID calls.

| Command | Pocket ID calls could use HTTPS | Why |
|---------|-------------------------------|-----|
| `app remove` | Yes | It deletes clients on the active instance; nothing needs a particular site. |
| `secrets rotate` (retiring secrets) | Yes | Same: plain API work on the active instance, after apply. |
| apply's declared operators | Yes, after the converge | They run after DNS and the gateway are in place. A dry run before the first converge has no public hostname to call. |
| `oidc client create` | Only without `--site` or `--ssh` | Those flags name one instance, which the public hostname cannot. Its `--execute` claims the host registry over ssh either way. |
| apply's identity step (clients, signup groups) | No | It runs as soon as Pocket ID starts, before the first converge has given it DNS or a certificate. |
| `operator magic-link` | No | It is the way back in when something is broken, the gateway included, and it reads the email failure from the log over ssh. |

Moving any command to HTTPS is a separate decision and not part of this split.
`pocketid-go` keeps both paths, so either is available to every command.

## `paisans-operator-reconciler-service`

The repository receives `cmd/operator-reconciler` as the module root's
`main.go`, `internal/operatorreconciler` and `internal/dnscontinuity`, the
Dockerfile, and the image workflow, with its history (`git filter-repo` over
those paths), so `git log` on the reconciler still explains it.

It depends on `pocketid-go` for the client. It writes nothing: the tests that
fail on any request other than a `GET` move with it, and they are what keeps
it a reader.

Its tests import the toolkit in two places, and neither import can cross a
module boundary. `operatorreconciler_test.go` checks that the reconciler's
`OperatorsGroup`, `ManagementPaisans` and `ManagementPocketID` equal the
toolkit's in `internal/blueprints`; each side's tests instead pin the values
the operators spec states, so either side changing one fails its own tests.
`integration_test.go` takes the Pocket ID image from
`blueprints.DefaultImage`; in the new repository it takes the image from an
environment variable, defaulting to the Pocket ID release the `pocket-id`
blueprint's track resolves to when the move is made.

Releases are tags `vX.Y.Z` on its own repository, and only a tag publishes:
the workflow publishes `X.Y.Z` from a tag and nothing from a push to a
branch. **The first release is `v0.3.0`:** it publishes
`ghcr.io/paisans-software/operator-reconciler:0.3.0`, which the core
catalog's track `ghcr.io/paisans-software/operator-reconciler:0.3.*` matches.

The GHCR package `operator-reconciler` belongs to the organisation. If it
exists before the first release, an organisation owner grants the new
repository write access to it (package settings, *Manage Actions access*),
or the workflow's `GITHUB_TOKEN` cannot push.

## `paisans-cli`

After the two moves, this repository is the CLI:

* `cmd/operator-reconciler`, `internal/operatorreconciler`,
  `internal/dnscontinuity` and
  `.github/workflows/operator-reconciler-image.yml` are removed.
* `internal/pocketid` is removed, and every import of it names
  `github.com/paisans-software/pocketid-go`.
* The group `operators` is named by `blueprints.OperatorsGroup` here and by
  the reconciler's own constant there. The group name is a contract between
  the `pocket-id` blueprint, apply and the reconciler, and the operators spec
  states it; neither program imports it from the other.
* The `pocket-id` blueprint and its compose template are unchanged. They name
  the reconciler through the foundation track, not through the code.

The rename:

1. GitHub repository `Paisans-Software/paisans-stack` becomes
   `Paisans-Software/paisans-cli`. GitHub redirects the old path for clones and
   the web; it does not redirect Go module resolution, which fetches the path
   in `go.mod`.
2. `go.mod` becomes `module github.com/paisans-software/paisans-cli`, and every
   import follows, in one commit.
3. The documentation calls the product the paisans CLI, or `paisans` where it
   means the command. `paisans-stack.yaml` keeps its name everywhere.
4. References in the sibling repositories follow: the core catalog's README
   and comments, `paisans-account-service`'s, and the Homebrew tap's (step 4 of
   *Order*).

The `admin-reconciler-v0.1.0` and `admin-reconciler-v0.2.0` tags are deleted
from this repository. Nothing consumes them: a deployment reaches the image
through its track, and the published `0.1.0` and `0.2.0` images stay in GHCR,
where deleting a git tag does not reach. Founder decision.

## Order

Each step leaves every repository building and every deployment running the
same images.

1. **`pocketid-go`.** Create the repository from `internal/pocketid` with its
   history. Tag `v0.1.0`. Switch this repository's imports to it, reconciler
   included, and remove `internal/pocketid`.
2. **`paisans-operator-reconciler-service`.** Create the repository with its
   history. If the GHCR package `operator-reconciler` already exists, grant
   the repository write access to it. Tag `v0.3.0` and confirm the image
   resolves on the `0.3.*` track; the operators change's core catalog and
   toolkit releases follow. Remove the reconciler from this repository and
   delete its two `admin-reconciler-v*` tags here.
3. **The rename.** Rename the repository, change the module path, update the
   documentation, the core catalog's references and `paisans-account-service`'s.
4. **The Homebrew tap.** In `Paisans-Software/homebrew-paisans`, point
   `Formula/paisans-cli.rb`'s `homepage` and `head` at
   `Paisans-Software/paisans-cli`, and update the README: its links, the
   `head` example, and the release `url` template the stable block will use
   (`.../paisans-cli/releases/download/...`). `brew install --HEAD
   paisans-cli` must build from the renamed repository before this step is
   done. The formula's build names `./cmd/paisans`, not the module path, so
   the install line is unchanged.
5. **`paisans-account-service` onto `pocketid-go`.** Replace
   `internal/idp/pocketid` with the shared client behind the account
   service's `idp` interface, keeping its contract and write surface tests. The
   account service must already reach Pocket ID over the mesh
   (`docs/specs/2026-10-14-pocket-id-api-private.md`), since the shared
   client refuses its public hostname.

## Open

* **Who creates the repositories and the rename.** Each is a publishing
  action, so a human performs or approves it at its step.
