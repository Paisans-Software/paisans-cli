# Rootless and host admit: phase plan

> **For agentic workers:** This is the index of the work, not a task list to execute. Each phase below gets its own detailed plan (`docs/superpowers/plans/2026-10-15-rootless-<phase>.md`), written when the phase starts, from the spec as amended by the research spike. REQUIRED SUB-SKILL for those: superpowers:subagent-driven-development or superpowers:executing-plans.

**Goal:** Every deployment runs as its own unprivileged user with rootless Docker, admitted once by the host's owner with `paisans host admit`.

**Architecture:** One root step (`host admit` / `host evict`, local from a request or over the owner's SSH), one narrow root helper for WireGuard, and every other command running as `paisans-<token>` with `DOCKER_HOST` pointed at that user's daemon. Phases land as separate pull requests against `develop`, each passing `go test ./...` on its own; the toolkit is not deployable rootless until phase 6 lands, and nothing is released from `develop` between phase 1 and phase 6: from phase 1 a site without `ssh.user` logs in as a user only `host admit` creates.

**Tech Stack:** Go, the existing `apply.Transport`, `yaml.v3` splicing in `internal/config`, POSIX sh for the helper, nftables, ufw, systemd user units, GitHub Actions.

**Spec:** `docs/specs/2026-10-15-rootless-and-host-admit.md`

## Global Constraints

* Rootless on every site; no code path keeps the sudo transport for a deployment command.
* Default user `paisans-<token>` (`deployment.Prefix()`); `ssh.user` overrides; written back with a splice that never overwrites.
* Docker Engine 29.5 or later.
* The helper's `put` refuses any key outside the renderer's set, any `PreUp`/`PostUp`/`PreDown`/`PostDown`, a `ListenPort` other than the admitted one, and an `Address` outside the mesh subnet.
* Grants are opt-in: `--watchdog`, `--public-ports`. Forwarding follows `endpoint`, with no flag.
* No migration code: existing deployments are redeployed.
* No em dashes in docs, comments, commits or PR bodies. Commits: `<type>: <lowercase subject>`, body says why, `Founder decision.` where it was one.
* `host admit` and `host evict` are owner actions; `docs/deployment-agent-rules.md` says an agent never runs them unattended.

## Review Focus

* An owner admits with an existing user that is in `docker` or `sudo`: admit must refuse (phase 5 test).
* A request edited after its digest was read: admit must refuse at the typed digest (phase 4 test).
* A conf with `PostUp` smuggled in a `[Peer]` section, or a key in odd case or with leading spaces: the helper must refuse (phase 3 test table).
* A command run with `ssh.user` absent before admission: it must say the host is not admitted and print the `host request` line, not fail on ssh auth (phase 2 test).
* `host evict` of one of two admitted deployments: host-wide files and sysctls stay (phase 5 test).

---

## Phase 0: Research spike

`docs/superpowers/plans/2026-10-15-rootless-research-spike.md`. Two throwaway VMs; results become the spec's *Observed* section. **Blocks every phase that depends on an R-number below.** Phases 1 and 8 do not depend on it and may start alongside.

## Phase 1: Configuration and the allowlist's removal

Depends on: nothing.

* Modify `internal/config/ssh.go`: `ssh.user` optional; `(Site).SSHUser(d deployment.Deployment) string` returns the declared name or `d.Prefix()`. Validation keeps the pattern for a declared name.
* Create `internal/config/ssh_user.go`: `SetSSHUser(path, site, user string) error`, splicing as `SetArch` does in `internal/config/arch.go`, never overwriting.
* Delete `cmd/paisans/hostallow.go`, `internal/hostprep/allow.go` and its test, `internal/doctor/allowlist.go`, the `allow`/`disallow` cases in `cmd/paisans/main.go`, and the allowlist guard in `internal/hostprep/authorized_keys.go`.
* Tests: `SetSSHUser` golden files (absent, present, quoted, with comments); `SSHUser` default; validate accepts a site without `user`.
* Founder decision already given for the deletion.

## Phase 2: Transports

Depends on: phase 1.

* Modify `internal/apply/transport.go`: `SSHTransport` gains `DockerHost string`; `RemoteCommand` exports `DOCKER_HOST` when set. `Sudo` stays only for the owner's transport.
* Create `internal/apply/local.go`: `LocalTransport` implementing `Transport` with `sh -c`, unexported constructor reached only through `hostadmit.Local()`.
* Modify `cmd/paisans/main.go` `siteTransport`: user from `SSHUser`, `DockerHost` from the user's UID (read once per run with `id -u`), no sudo. Remove every `--sudo` flag listed in the inventory (`cmd/paisans/*.go`) and `settleSudo` calls outside admit and evict.
* Create `internal/apply/helper.go`: `WGHelper{T Transport; Iface string}` with `Put(conf []byte)`, `Up()`, `Restart()`, `Sync()`, `Show(field string)`, `Log()`, each `sudo -n /usr/local/libexec/paisans/wg-<iface> <verb>`.
* An unadmitted host (ssh auth fails for the default user and no `ssh.user` is declared) reports "not admitted" with the `host request` line.
* Write-back: the first command that connects as the default user calls `config.SetSSHUser`; a write-back failure (eg a bare `user:` with no value, which `SetSSHUser` refuses) is a warning, never fatal to the command that connected. Add `SetSSHUser` tests for a flow `ssh` with an empty user, a quoted value with a trailing comment, CRLF, and `ssh` last with no final newline.
* `paisans create` (`internal/create/wizard.go`): the SSH user prompt offers the deployment's default and may be left empty, instead of "SSH user (must already exist)".
* Tests: `RemoteCommand` with `DockerHost`; no transport built with `Sudo` outside admit/evict (a test that walks `siteTransport` callers); `WGHelper` command lines.

## Phase 3: The WireGuard helper

Depends on: phase 2; R9.

* Create `internal/hostprep/snippets/wg-helper.sh.tmpl`: verbs `put`, `up`, `restart`, `sync`, `show <field>`, `log`; interface, listen port and subnet baked in at render; `put` validates with awk against the allowed keys and value patterns, writes 600 via temp and rename.
* Create `internal/hostprep/wghelper_test.go`: runs the rendered `put` validator under `sh` on a table of confs (renderer output accepted; each hook key, unknown key, case and spacing variants, wrong port, address outside subnet refused).
* Modify `internal/render/templates/wireguard.conf.tmpl`: drop the relay `PostUp`.
* Modify `internal/apply/apply.go` (mesh steps), `internal/apply/mesh.go`, `internal/meshrekey/meshrekey.go`, `internal/meshkey/meshkey.go`, `internal/siteadd/mesh.go`, `internal/doctor/mesh.go`: every `wg`, `wg-quick`, `systemctl ... wg-quick@` and `journalctl -u` goes through `WGHelper`.
* Golden render tree regenerated and its diff read.
* README: the mesh's `journalctl -u wg-quick@psns-<token>` "through the site's sudo" (*apply*, mesh failure) and any other `wg`/`wg-quick` passage name the helper instead.

## Phase 4: Requests and admission records

Depends on: phase 1.

* Create `internal/admission/request.go`: `Request` (fields per the spec's table), `Build(cfg *config.Config, site string) (Request, error)`, `Encode(Request) []byte`, `Parse([]byte) (Request, error)` with strict validation, `Digest([]byte) string` (12 hex digits in three groups of SHA-256 over the encoded bytes).
* Create `internal/admission/record.go`: `Record` (user, grants, forwarding, ports, digest, time), path `/etc/paisans/admitted.<token>.json`, mode 0644 and owned by root so the deployment's user can read it without sudo, `Read(t Transport, token string) (Record, error)`.
* Create `cmd/paisans/hostrequest.go`: `host request --site <s>`; request on stdout, digest on stderr.
* Tests: golden requests per role mix; digest stable and field-sensitive; `Parse` refusals (missing field, unknown version, bad key, address outside subnet).

## Phase 5: `host admit` and `host evict`

Depends on: phases 2, 4; R1, R2, R3, R4, R5, R10, R14, R15, R18, R20.

* Create `internal/hostadmit/` (`plan.go`, `docker.go`, `user.go`, `daemon.go`, `firewall.go`, `egress.go`, `watchdog.go`, `sysctl.go`, `record.go`, `evict.go`), reusing `internal/hostprep` steps for apt, ufw and softdog where they already exist (`ubuntu.go`, `ufw.go`, `firewall.go`, `watchdog.go`), with the step order of the spec's *What it does*.
* Create `internal/hostprep/snippets/egress.nft.tmpl` and `paisans-egress.service.tmpl`.
* Create `cmd/paisans/hostadmit.go`: `host admit` (`--request` | `--site --ssh`, `--watchdog`, `--public-ports`, `--execute`), the typed digest, and `host evict` (`--request` | `--deployment --user` | `--site --ssh`, typed user name).
* Golden plans: blank Ubuntu 24.04, old system Docker (refused), current system Docker, snap Docker (refused), each grant, re-run with a grant removed, a privileged existing user (refused), second watchdog holder (refused), evict of one of two deployments.
* Files in the deployment user's home (`~/.ssh/authorized_keys`, `~/.config/docker/daemon.json`) are written as that user (`runuser -u <user> --`), never by root, so a symlink the user plants cannot redirect a root write. A test plants `~/.ssh` as a link to a root-owned directory and admit leaves that directory untouched.
* The admission record is written mode 0644, owned by root, via temp and rename; a golden plan and a test assert the mode, and a re-run that finds it otherwise restores 0644.
* `--public-ports` checks 80 and 443 before granting, as root: `ss -Hltnp`, the system Docker's published ports when its socket exists, and DNAT rules for either port in `nft list ruleset`. Anything found refuses the grant, naming the process, the container and compose project, or the rule, and naming `web_server.managed_by: external`. Golden plans for each of the three holders.
* For a site with `managed_by: external`: detect the owner's web server as root, record it in the admission record, and warn in the plan when Caddy's admin API listens on TCP. Golden plans for Caddy with and without a TCP admin API, nginx and Apache, on the host and in a container.
* Any boot-ordering mechanism R10 calls for lands here.
* README: the registry claim passages ("claim through sudo, since the registry is root's", under the registry section) say the claim happens in `host admit`.

## Phase 6: Commands as the deployment's user

Depends on: phases 2, 3, 5; R7, R8.

* `internal/hostprep`: `host prepare` installs nothing; checks the admission record against the declaration; writes `authorized_keys` and its sidecar in the user's home and `/srv/paisans/<token>/.paisans/`; config store as user units (`configstore.go`, `profiles/ubuntu-24.04/*.tmpl`); `~/.config/docker/daemon.json`.
* Paths: `internal/deployrecord/record.go`, `internal/grant/record.go`, `internal/gatewaymove/gatewaymove.go`, `internal/hostprep/configstore.go`, `internal/render/hostsites.go` move under `deployment.Root()`; `deployment.go` gains `StateDir()`.
* `internal/hostcheck/inventory.go`: no UID 0; own daemon only; `ss -Hltnu`; no ufw probe.
* `internal/apply/volumeprune.go`: `docker system df -v`. `internal/mesh/mesh.go`: the user's `daemon.json`.
* `internal/registry`: claims happen in admit; deployment commands read the admission record (mode 0644) as the deployment's user, with no sudo, instead.
* `internal/siteremove/host.go`: user-side removal, then the `host evict` line. `cmd/paisans/hostdeployments.go`: lists admission records.
* The gateway's Caddy serves `/.well-known/paisans/<token>` with the deployment's token (render templates, golden tree regenerated). The gateway's step in `apply` ends by fetching it through the public address on 443, and 80 expecting the redirect; any other answer fails the step: "443 on <site> answers, but not as this deployment". Tests with a fake fetcher for a matching body, another body, another server and no answer.
* `web_server.managed_by: external` per the spec's *Behind the owner's web server*: admit detects the owner's server as root and records it (phase 5 adds the record fields and the request's `web_server` field); admit warns when Caddy's admin API listens on TCP; `validate` refuses `include_dir`; `apply` writes the snippet under `/srv/paisans/<token>/web-server/` and warns until `web-server check` passes; `show` fills the snippet from the record; the include, rollback and reload code in `internal/webserver` is removed. README, the guide `docs/guides/behind-your-own-web-server.md` and `docs/commands.md` rewritten to match.
* Docker-tagged tests run against a rootless daemon.
* README, each passage that names a root-owned path or sudo for a deployment command, rewritten to the paths under `deployment.Root()` and the user's own permissions: the seed written through `sudo` (account seed), the deployed record's "read through sudo instead" fallback, the config store "with `sudo` because the store is root's", gathering "with `sudo`", the host check's inventory "as root ... refuses unless it is 0", and doctor's Leftovers row. The admission record is described beside the deployed record: 0644, readable by the deployment's user without sudo, and `host deployments --sudo=false` lists admission records without the registry or directories.

## Phase 7: Doctor

Depends on: phase 6.

* `internal/doctor/reach.go` and new `internal/doctor/rootless.go`: the user is not privileged, the rootless socket answers, Engine 29.5 or later, linger on, `sudo -n -l` lists only the helper, the admission record matches the declaration. Each in the check graph from `docs/specs/2026-10-15-doctor-check-graph.md`, with the sudo finding removed, and a gateway check that fetches `/.well-known/paisans/<token>` from outside as `apply` does, sharing its code.

## Phase 8: Linux release binaries

Depends on: nothing.

* `.github/workflows/release.yml`: build `linux/amd64` and `linux/arm64` with `CGO_ENABLED=0`, attach them and `SHA256SUMS` to the release.
* Verify on a test tag in a fork or with `act`, and that the arm64 binary runs `paisans version` on an arm64 VM.

## Phase 9: Documentation

Depends on: phases 5 to 7.

* `docs/commands.md`: `host request`, `host admit`, `host evict`; `host prepare` and every former `--sudo` rewritten; `host allow`/`disallow` gone.
* `README.md`: the sections on `host prepare`, the login user's keys and Docker's version.
* `docs/guides/host-owners.md`: install and check the binary, compare the digest, what admit changes, each grant, evict.
* `docs/deployment-agent-rules.md`: admit and evict are owner actions.
* `CHANGELOG.md` bullets in the PRs that change behaviour (phases 1 to 8), not here.

## Phase 10: Staging end to end

Depends on: everything.

* Fresh VMs from `test-deployments/` (in the community repo), admitted with `--request` as an owner would, then `apply` as the deployment's user. Checks: a container cannot reach the host's `127.0.0.1` or LAN; `sudo -n` runs only the helper; reboot recovers; `host evict` leaves the host as the owner had it. On a second host, a system Docker with `"userland-proxy": false` publishing 443: admit `--public-ports` refuses; with the container started after admission, `apply` and `doctor` report the conflict.

## Founder questions blocking phases

| Question (spec *Open*) | Blocks |
|---|---|
| `watchdog: off` on a host admitted without `--watchdog` | phase 5 refusal rule |
| `--home` for admit | phase 5 |
