# An operator moves the infrastructure's images without a catalog release

Status: draft, awaiting founder approval. Design agreed in session,
2026-10-15. Builds on `docs/specs/2026-10-13-images-follow-tracks.md`.

## Why

Every image the infrastructure stack runs, etcd, Spilo, HAProxy, Garage and
the heartbeat, comes from the core catalog's `foundation.yaml`. A security fix
that lands inside the foundation's track reaches a deployment on its next
`apply`. One that does not, such as a fix HAProxy ships only in a newer minor
than `3.0.*-alpine` allows, or a patched Spilo from another provider, waits
for a core catalog release, and the operator has nothing to write in
`paisans-stack.yaml` that moves it sooner.

An app's images already have that key, `apps.<name>.images`, and the
gateway's Caddy has `acme.image`. The infrastructure stack is the one place
an operator cannot choose a build.

Nothing tells an operator that a newer build exists, either. Builds move only
when someone runs `apply`, and an override, once written, stops following its
track with nothing to say that the track has since moved past it.

## Settled

Agreed in session, 2026-10-15:

* **`infra.images` and `infra.postgres`** override the foundation's images
  for the infrastructure stack, with the semantics `apps.<name>.images` has.
* **Spilo's override is keyed by Postgres major**, as `foundation.yaml` keys
  it, so the operator states the major and nothing is read from an image's
  name. Any provider works.
* **An override is a declared image**, so apply's grant review shows it and
  asks for approval, whatever catalog the deployment reads. No review
  exemption widens.
* **`paisans images`** lists every image the deployment runs, the build each
  site runs, the build the next `apply` would run, and whether an override
  lags the reference it replaced.
* **No override for a pinned app's own Postgres** (`postgres.<major>.app`).

## The configuration

A new top level key, `infra`:

```yaml
infra:
  images:
    etcd: "gcr.io/etcd-development/etcd:v3.5.21"
    haproxy: "docker.io/library/haproxy:3.2.*-alpine"
    garage: "docker.io/dxflrs/garage:v1.1.0"
    heartbeat: "docker.io/curlimages/curl:8.11.1"
  postgres:
    "17":
      spilo: "example.org/pg-ha:17.4-1"
```

Every key is optional, and an unset one takes the foundation's image. Each
value is one full reference, exact or a track, for the reason
`apps.<name>.images` gives: switching to a fork changes registry, repository
and tag together.

`infra.images` takes `etcd`, `haproxy`, `garage` and `heartbeat`. The
gateway's Caddy is not among them; `acme.image` is its one key.

`infra.postgres.<major>.spilo` applies only while `cluster.postgres_version`
is that major. When the deployment moves to another major, the entry stops
applying and the new major takes the foundation's Spilo, or the entry the
operator writes for it, so a build of one major never follows the cluster
onto another.

The heartbeat override covers both places the toolkit runs the heartbeat: the
infrastructure stack's, and the operators heartbeat it adds to each Pocket ID
stack (`injectedImages`). `apps.<pocket-id app>.images` takes no heartbeat
key, so one image has one place to be set.

## What validate checks

`validate` reads the file alone, so every check here is offline.

Refused:

* a key under `infra.images` other than the four above, or under
  `infra.postgres` other than a major the toolkit runs (16, 17, 18);
* a key under `infra.postgres.<major>` other than `spilo`;
* an empty value, a reference that does not parse, or one with no tag or
  `latest` (the `floating-image-tag` rule `apps.<name>.images` has);
* an etcd, HAProxy or Garage reference whose tag can leave the prefixes the
  toolkit is written against (`v3.`, `3.`, `v1.`). This is the check
  `foundation.yaml` gets (`checkImage`), exported and called, not copied.
  A digest with no tag beside it is refused for the same images, since its
  version cannot be checked; `repo:tag@sha256:...` is accepted.

Warned:

* an `infra.postgres` entry for a major other than `cluster.postgres_version`:
  it applies to nothing, and should be removed;
* a Spilo override outside `ghcr.io/zalando/spilo-*`: the toolkit is written
  against Spilo's and Patroni's environment and data path and cannot check
  another image's. A Zalando Spilo reference is held to Spilo's supported
  prefixes (`3.`, `4.`) and refused outside them;
* a heartbeat override, once, saying it also replaces the operators
  heartbeat in every Pocket ID stack.

## Render and review

`infraImages()` in `internal/render/site.go`, and for the operators
heartbeat both `injectedImages()` in `internal/render/images.go` and
`appValues.HeartbeatImage` in `internal/render/appview.go`, take a declared
reference first and the foundation's otherwise, as `appImageOrigins` does for
an app. A track resolves
at apply time through the path every track takes, so the digest is pinned and
the dry run's move report names the build that moves.

`infraImageOrigins()` returns every overridden reference in `declared`, never
in `foundation`. The grant review therefore lists it for approval even when
the core catalog is the official one, and even when the override names the
same reference the foundation does (`fromElsewhere` already covers that).

## `paisans images`

Read only. It takes `--config`, `--secrets` and `--check`, and no
`--execute`.

```
site  stack  service  source             running                  newest                   note
a     infra  etcd     infra.images       v3.5.21 (sha256:1a2b3c)  v3.5.21 (sha256:1a2b3c)  foundation is at v3.5.23
a     infra  haproxy  foundation         3.0.11-alpine (4d5e6f)   3.0.12-alpine (7a8b9c)   moves on next apply
a     talk   app      blueprint          1.16.0-paisans (0a1b2c)  1.16.0-paisans (0a1b2c)
```

One row per site, stack and service:

* **source** is where the reference came from: `foundation`, `blueprint`,
  `infra.images`, `infra.postgres.<major>`, `apps.<name>.images` or
  `acme.image`.
* **running** is the build the site's compose file pins, read over ssh the
  way apply's dry run reads it.
* **newest** is the build the next `apply` would run: a track's newest tag
  and its digest, or an exact tag's digest today, so an image rebuilt under
  the same tag shows as a move.
* **note** says `moves on next apply` when running and newest differ, and,
  for an override, what the reference it replaced resolves to.

It builds the plan apply's dry run builds, with the same `imagedigest.Cache`,
so the two cannot disagree about which build is newest. It writes nothing to
any host.

A site that cannot be reached prints `unknown` under running and a warning,
and the other sites still print. A registry that cannot be reached prints
`unknown` under newest.

It exits 0 when it could read what it set out to, however much would move.
With `--check` it exits 3 when any build would move or any override lags, so
cron or CI can run it and tell that apart from an error, which exits 1 as
every command's does (2 is a usage error).

### When an override lags

For each override, of an infrastructure image or an app's, the reference it
replaced is resolved too: the foundation's for the infrastructure, the
blueprint's for an app.

* **Same repository:** the two tags' numbers are compared left to right, as
  `Pattern.Newest` orders them. When the replaced reference resolves higher,
  the note names it and the row warns that the override is behind.
* **Another repository:** the replaced reference's newest is shown, with no
  verdict, since two repositories' version schemes need not compare.

`apply`'s dry run prints the same warning. It never blocks.

## Tests

* `internal/config`: `infra` parses; unknown keys refused.
* `internal/validate`: each refusal and warning above, including a Spilo
  override for an unused major, a non-Zalando Spilo, and an etcd `v4.*`.
* `internal/render`: an override wins over the foundation; the heartbeat
  override reaches both the infrastructure stack and a Pocket ID stack; a
  Spilo override applies only for its major; an override is listed as
  declared and is not marked a foundation service with the official core.
* `internal/apply`: the lag comparison, same repository and another; the
  dry run's warning.
* `cmd/paisans`: `paisans images` against a stub registry and transport,
  including an unreachable site, an unreachable registry, and `--check`
  exiting 0 and 3.

## Docs

`README.md`'s configuration reference, `docs/commands.md` (`paisans images`,
and the new validate checks), `docs/development.md`, a record in
`docs/decisions.md`, and a `CHANGELOG.md` bullet.

## Work list

1. **Configuration and validate.** The `infra` key, its checks, and
   `checkImage` exported.
2. **Render and review.** Overrides in `infraImages` and `injectedImages`;
   overridden references declared in `infraImageOrigins`.
3. **The lag comparison**, and its warning in the dry run.
4. **`paisans images`.**
5. **Docs.**
