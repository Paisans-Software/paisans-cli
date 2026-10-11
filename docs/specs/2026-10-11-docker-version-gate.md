# Docker is judged by its version, not its source

Date: 2026-10-11. Status: approved by the founder in session.

The host check stops caring where an existing Docker came from. It accepts
Docker from any source except a snap, provided `docker compose` works and the
engine and the compose plugin are at least the minimums below. Where Docker is
absent, `host prepare` still installs it from Docker's own apt repository.

## Rule

* **Any source but a snap.** Docker Engine from Docker's repository
  (`docker-ce`), from Ubuntu's archive (`docker.io` with `docker-compose-v2`),
  or built by hand is accepted when it meets the minimums. A snap is refused
  however new it is: its confinement keeps Docker from bind mounting under
  `/srv`, where every stack keeps its data, and snapd refreshes it, restarting
  the daemon and every container, on snapd's schedule. A version number does
  not reveal either.
* **Below a minimum is refused**, naming the version found and the version
  needed, with the fix: upgrade Docker from wherever it came from, or remove it
  and let `host prepare` install it from Docker's repository.
* **An install has one source.** When Docker is absent, `host prepare` installs
  it from Docker's apt repository, because an install needs one source and that
  one gives every prepared host the same engine.
* **A mixed or broken install** is still caught where it was: when
  `docker compose` does not work and a package from Ubuntu's archive is in the
  way, `host prepare` refuses and names the packages. On a `docker.io` without
  `docker-compose-v2`, it also offers installing that package, which is the
  compose plugin (`/usr/libexec/docker/cli-plugins/docker-compose` in its file
  list on packages.ubuntu.com, noble-updates, fetched 2026-10-11).

## Minimums

| | Minimum | Set by |
|---|---|---|
| Docker Engine, daemon and CLI | **25.0.0** | `healthcheck.start_interval` |
| Docker Compose | **2.24.0** | `healthcheck.start_interval` |

Each minimum is the newest version among the features the toolkit depends on.
The CLI is held to the engine's minimum as well as the daemon, because
`--format json` is the CLI's.

### Features that set or approach a minimum

| Feature | Where the code uses it | Version | Citation |
|---|---|---|---|
| `healthcheck.start_interval` | `internal/render/templates/pocket-id/compose.yaml.tmpl` | Engine 25.0.0, API 1.44 | Docker Engine 25.0 release notes, 25.0.0: "Add an additional interval to be used by healthchecks during the container start period." |
| `healthcheck.start_interval` | the same | Compose 2.24.0 | docker/compose v2.24.0 release notes: "Add support for start_interval" (docker/compose#10939). Compose refuses the key on an engine below API 1.44: "can't set healthcheck.start_interval as feature require Docker Engine v25 or later" (docker/compose#11360, v2.24.1) |
| `--format json` | `docker image ls --no-trunc --format json` in `internal/apply/prune.go` | Engine (CLI) 23.0.0 | Docker Engine 23.0 release notes, 23.0.0: "Add the `--format=json` shorthand variant of `--format="{{ json . }}"` to all commands supporting the `--format` flag." |
| `docker compose ps --format json` keys (`State`, `Health`, `ExitCode`) | `internal/apply/health.go`, `internal/apply/refresh.go` | Compose 2.21.0 | docker/compose v2.21.0 release notes: the format of `ps --format=json` "changed to better align with `docker ps` output" (docker/compose#10918). `parseContainers` also reads the JSON array older releases printed |

### Features older than both minimums

Each is present in the source at the minimum's tag: docker/cli `v25.0.0`, moby
`v25.0.0`, docker/compose `v2.24.0`.

| Feature | Where the code uses it | Present at |
|---|---|---|
| `docker ps --format '{{json .}}'` with `Names`, `State`, `Status`, `Labels` | `internal/doctor/containers.go` | docker/cli v25.0.0, `cli/command/formatter/container.go` |
| `docker system df --format`, `docker info --format` | `internal/apply/disk.go`, `internal/preflight` | docker/cli v25.0.0, `cli/command/system/df.go`, `info.go` |
| `docker pull --quiet` | `internal/apply/volumes.go` | docker/cli v25.0.0, `cli/command/image/pull.go` |
| `docker volume ls --filter dangling=true` | `internal/apply/volumeprune.go`, `anonvolumes.go` | docker/cli v25.0.0, `cli/command/volume/list.go` |
| `tmpfs` options passed through to the mount (`uid=`, `gid=`, `size=`, `mode=`) | `internal/render/templates/mbin/compose.yaml.tmpl` | moby v25.0.0, `daemon/oci_linux.go`, which splits the options and only merges duplicates (`mount.MergeTmpfsOptions`) |
| Docker's default address pools | `internal/mesh/mesh.go`, `DefaultPools` | moby v25.0.0, `libnetwork/ipamutils/utils.go`, the same list as v27.5.1 |
| `docker compose ps --all --status --quiet --format` | `internal/apply` | docker/compose v2.24.0, `cmd/compose/ps.go` |
| `docker compose logs --no-color --no-log-prefix --since --tail` | `internal/pocketid/emaillog.go`, `internal/doctor/patroni.go` | docker/compose v2.24.0, `cmd/compose/logs.go` |
| `docker compose exec -T`, `run --rm --no-deps --entrypoint` | `internal/apply`, `internal/patroni` | docker/compose v2.24.0, `cmd/compose/exec.go`, `run.go` |
| `docker compose up -d --force-recreate --no-deps` | `internal/apply` | docker/compose v2.24.0, `cmd/compose/up.go` |
| `docker compose version --short` | the host check's own probe | docker/compose v2.24.0, `cmd/compose/version.go` |
| `depends_on` with `condition: service_healthy` | the mbin template | docker/compose v2.24.0, `pkg/compose/convergence.go`, `ServiceConditionHealthy` |
| `deploy.replicas` without swarm | the mbin template | docker/compose v2.24.0, `pkg/compose/convergence.go` |

### Not established

**How compose reads a `.env` used as an `env_file`.** `configmerge.envValue`
quotes values by rules observed with `docker compose config` on Docker Compose
v5.4.0 (`docs/decisions.md` has the run). Whether Compose 2.24.0 reads those
quotes and escapes the same way is not established from a citation, so it did
not set the minimum. If it differs, a value with a quote, `$`, `#` or a
backslash in it could reach an application changed on an older compose.

## Where it runs

`hostcheck.Inspect`'s Docker probe now prints the daemon's and the CLI's
versions on one line and `compose <version>` or `compose absent` on the next.
`Report.Refusal` refuses a snap or an old Docker before it looks at conflicts
or a shared host's firewall. The host check runs first in `host prepare`,
`apply`, `site add`, `prune`, `storage rotate-key`, `storage add`,
`site remove` and `app remove` (their monitor sites), and in `preflight`, so
each refuses an old Docker before it changes anything. A Docker without a
working `docker compose` is not refused by the host check, because `host
prepare` is what installs the plugin or names what keeps it from working.

`--verbose` shows the compose plugin's version beside the engine's:

```
  ✓ docker: 27.5.1, compose 2.33.1 (docker.io)
```

## Tests

`internal/hostcheck`: a version parsing table (distribution suffixes
`+dfsg`, `-0ubuntu3~24.04.2`, `-1ubuntu1`, `+ds1`, a leading `v`, a
prerelease); the probe's shell run against a stand in `docker`; and
classification for `docker.io` current and old, `docker-ce` current, an old
compose plugin alone, an old CLI, a snap, and compose missing.
`internal/hostprep`: a `docker.io` whose compose works is left alone, and one
whose compose does not is refused, offering `docker-compose-v2` only when it is
not installed.
