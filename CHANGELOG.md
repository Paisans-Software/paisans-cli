# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Each change gets one bullet, a single sentence of at most 35 words; related
changes share a bullet. Detailed notes are on the corresponding
[GitHub release](https://github.com/Paisans-Software/paisans-cli/releases).

## [Unreleased]

### Added

- The gateway's Caddy answers `/.well-known/paisans/<token>`, and `apply` ends a gateway's step by fetching it through the public address, failing when another server answers on 443 or 80.
- `infra.images` and `infra.postgres.<major>.spilo` override the infrastructure stack's images without a catalog release, and `paisans images` lists each site's builds, what would move, and overrides behind their track.
- `host request --site <s>` writes the request a host's owner admits that site from, naming the site and its roles, and prints its digest on stderr for the owner to compare over a trusted channel.
- Any run with `--execute` that logs in to a site leaving `ssh.user` out writes the deployment's user there; a run without `--execute` only says so, and a failed write is a warning.
- `host admit` and `host evict` let a host's owner admit a deployment as its own unprivileged user with rootless Docker, from a request on the host or over their own SSH, and undo it.
- `host prepare --all` prepares every site the configuration declares, one at a time in its order, each with its own review, stopping at the first failure.
- `host admit`, `host prepare` and the rootless deployment commands support Ubuntu 22.04 beside 24.04, with Docker's apt source naming the codename the host's `/etc/os-release` gives.
- Each release attaches static `paisans-linux-amd64` and `paisans-linux-arm64` binaries and a `SHA256SUMS` file, so a host owner can run the CLI on Linux without Go.

### Changed

- `paisans prune` reads each dangling volume's size from `docker system df -v`, which the deployment's user can read for a volume a container's user owns.
- The host check runs as the deployment's user: it reads listeners with `ss -Hltnue`, counts its own user's and containers' as this deployment's, and leaves the firewall to `host admit`.
- Every command that changes a host checks first that the host admitted the deployment as that site, with the grants its roles need, and names the `host request` and `host admit` lines when not.
- The mesh subnet check reads the deployment's own Docker pools from `~/.config/docker/daemon.json`.
- The configuration store lives in `/srv/paisans/<token>/.paisans/config/`, and its git daemon, sync and timer are the deployment user's own systemd units, the daemon retrying until the mesh is up.
- The host owner's Caddy site files beside a gateway go in `/srv/paisans/<token>/caddy.d`, which `apply` creates as the deployment's user, instead of `/srv/caddy.d`.
- The deployment record, the grant record and a gateway move's marker live in `/srv/paisans/<token>/.paisans/`, written by the deployment's own user under one lock of the deployment's.
- `sites.*.roles` lists each role once, so a repeated role is refused when the configuration loads, as `host request` requires.
- Preflight compares a new data site with the existing data sites on architecture alone, so data sites may run different Ubuntu releases, since Postgres uses its container's glibc.
- `watchdog: off` is refused, since a data site's Patroni needs a watchdog to fail over safely and no other site uses one.
- `apps.*.hostname` and `apps.*.hostnames.*` must be lowercase DNS names of two labels or more, so a capital, a single label, an empty or over-long label or an underscore is refused by `validate`.
- `sites.*.web_server.listen` must be an IPv4 loopback address or the site's own mesh address, its port written without leading zeros, as `host request` requires; `web-server-listen-not-this-host` refuses anything else.
- `mesh.subnet` and `sites.*.address` must be written in canonical form, Eg: `10.44.0.0/24` and not `10.44.0.0/024`, as `host request` carries them.
- `mesh.subnet` larger than a /24 or overlapping 0.0.0.0/8, 127.0.0.0/8, 169.254.0.0/16 or 224.0.0.0/3 is refused, as is a site address on its network or broadcast address, since hosts' firewalls trust the mesh.
- `mesh.subnet` outside 10.0.0.0/8, 172.16.0.0/12 and 192.168.0.0/16 is refused, replacing the `mesh-subnet-is-not-private` warning, so another deployment's user on a shared host cannot reach the mesh.
- `host prepare` installs `gnupg` and checks Docker's apt key against the fingerprint of Docker's release key before apt trusts it, refusing a download that holds any other key.
- Each site's mesh file renders inside the deployment's directory, as `/srv/paisans/<token>/psns-<token>.conf`, and carries no `PostUp`: a relaying site's forwarding is the host's sysctl.
- `apply` starts, syncs and restarts each site's mesh through its WireGuard helper, `sudo -n /usr/local/libexec/paisans/wg-psns-<token>`, which checks the mesh file before installing it in `/etc/wireguard`.
- `doctor` advises reading a down mesh interface's journal with its WireGuard helper's `log`, which the deployment's user can run, instead of `systemctl status` and `journalctl`.
- `site add`, `site remove` and `mesh rekey` read each site's handshakes, peers and key through the WireGuard helper, as the deployment's user, instead of running `wg show`.
- `dns init` creates `v=DMARC1; p=reject; sp=reject;` for any mail name under `community.domain` with no MX record and no DMARC policy, instead of only warning that the policy is missing.
- Below its registrable domain, `community.domain`'s hostnames each get their own reject DMARC policy, and the registrable domain's policy is checked and warned about, never written.
- Docker commands on a site reach the login user's rootless daemon at `/run/user/<uid>/docker.sock`, the uid read once a run, and a refused default user is reported as a site not admitted.
- `preflight`'s ssh check asks only that each site answers as its ssh user, and `doctor` has no sudo finding, since no command they check for runs through sudo.
- `operators.*.keys` must be ed25519, ECDSA, a security key's `sk-` type or RSA of at least 3072 bits, as `host request` requires, so a certificate or a shorter RSA key is refused.
- `sites.<site>.ssh.user` may not be `root` or another deployment's `paisans-<token>` user, as `host request` requires.
- `sites.<site>.ssh.user` is optional: a site that leaves it out is reached as the deployment's own user, `paisans-<token>`, which must exist on the host.
- `paisans create` asks each machine's SSH user as optional, offering the deployment's own user, `paisans-<token>`, when left blank.
- `init` ends with `Next: run paisans secrets set` when a secret only the operator can give is unset, shows secret counts only with `--verbose`, and the SMTP password's reason is its login and host.
- When sudo refuses the login outright, `host deployments` advises naming a login that has sudo with `--ssh`, since it reads no site's ssh section.

### Removed

- `host allow`, `host disallow` and the key allowlist in `/etc/paisans/ssh-allow.<user>` are removed, with `host prepare`'s refusal of keys it lacked and `doctor`'s allowlist check.
- `--sudo` is removed from every command but `host deployments`: each runs as the site's ssh user, and `apply`, `site remove`, `host prepare --all` and `config` ask no sudo password before they start.

### Fixed

- Every mesh overlap check reads every routing table, so a route another VPN keeps in its own table, such as Tailscale's table 52, is refused.
- Mesh overlap refusals in `init`, `apply`, `host prepare`, `preflight` and `doctor` show control characters in names a host printed as escapes, so none is drawn on the terminal.
- The configuration store's daemon starts and stops with the deployment's WireGuard unit, and `host prepare` starts it only where the mesh is up, so peers can fetch from it once `apply` starts the mesh.
- `apply --execute` starts no peer sync on a site `host prepare` has not given a store, which has nothing to sync, rather than warning on every bare host that its sync did not complete.
- `site remove` without `--delete-data` keeps the registry entry, marked `data` kept, while data is left, so a later `--force --id ... --delete-data` finds the deployment instead of refusing.
- `site remove` and `app remove` read every flag given after the name before they report, so `-v` there shows detail and `--execute` there records `ssh.user`.
- `apply` on a site whose mesh interface is up without its file's address, key or peers restarts or syncs it, so a run stopped between writing the file and handing it over completes.

### Security

- Files written to a host are staged in a `mktemp` file beside the target, created exclusively and renamed into place, so a symlink planted at a guessable name is never written through, even under sudo.
- Writing a file to a host refuses a target that is a symbolic link or a directory, rather than replacing the link or moving the file into the directory.

## [0.2.0] - 2026-10-15

### Added

- `paisans doctor` reads Caddy's certificate failures, checks the DNS token from each Caddy site and the workstation (`--secrets`), and reports deployment hostnames whose records point elsewhere or are missing, never feeding Cloudflare's lockout.
- `apply` checks the DNS token from every Caddy site before Caddy starts or reloads, and `dns init` and `dns prune` from the workstation, stopping on a refusal or no answer rather than feeding Cloudflare's lockout.
- `paisans secrets list` prints every secret path the configuration needs, whether each is set, whether it is generated, pasted or captured, and the path `secrets rotate` takes, never a value.
- A catalog blueprint in `paisans.blueprint/v2` may declare a `heartbeat`: each app gets a generated `heartbeat_token`, a push monitor in every seed, `.HeartbeatURLs` in its templates, and rotation by `secrets rotate`.
- SMTP settings are tried from a site over ssh: `secrets set` and `secrets rotate` log in and email the operators before writing a password, `apply` refuses settings that do not log in, `validate` warns.

### Changed

- Errors refer to `paisans doctor` when its checks name the cause: a health gate TLS internal error on a hostname Caddy serves, an unreachable database or site, and `gateway move`'s token refusals.
- `paisans doctor` runs its checks at once and prints each as it ends; a site that does not answer is one line naming what was not checked, and the failures are listed again, deepest first.
- `paisans secrets rotate` asks at a terminal for a pasted secret's new value, the DNS token or an SMTP password, and `--all` includes them; a new DNS token is tried against the zone first.
- The grant review lists each site's foundation images first, under `foundation images (required):`, then removals, then what needs approval under its count, so the count heads exactly the entries it counts.

### Fixed

- `paisans doctor` fails a running container whose healthcheck reports unhealthy, showing the last lines its healthchecks printed, and warns while a healthcheck is still starting.
- `host prepare` lets containers reach Pocket ID's API on their own site's mesh address, port 1413, wherever the account service runs; the default deny dropped its calls, so member pages timed out.
- A failed login link email says Pocket ID's cause without its wrapping and connection string format, and once, not on every operator's line, when every email failed the same way.
- `paisans secrets set <key>` at a terminal asks for that key's value with echo off instead of refusing, so a set credential can be replaced by hand.
- A first apply with several apps reading one new Pocket ID group no longer stops on `409 Name is already in use`: a group made since the plan was read is found and used.
- A host ssh cannot reach is named once, with ssh's own words once, instead of repeated through every layer that tried to read it, and `init` lists each unreachable site on its own line.

## [0.1.0] - 2026-10-14

### Added

- The `pocket-id` app declares `management`, `paisans` or `pocket-id`: under `paisans` the account service manages signup, bans and recovery, and the toolkit renders Pocket ID's signup and email-code settings.
- `validate` refuses fewer than two operators with a key on every site that runs Pocket ID.
- An `smtp:` block may declare `security: none` for a relay at a private or mesh address, with a warning; a public address or a dotted name is refused.
- `paisans version` and `paisans --version` print the toolkit version and the commit the binary was built from, and blueprints' `requires_toolkit` is checked against that version.

### Changed

- Every HAProxy site serves Pocket ID's API on its mesh address at port 1413, and the account service calls it there rather than at Pocket ID's public hostname.
- The `invite` blueprint is the `account` blueprint, running paisans-account-service: `settings.signup` (`invite` or `open`) is required, and every account app serves signups on `join.<domain>` beside `account.<domain>`.
- The Pocket ID gateway closes its signup routes on `allow_user_signups` alone.
- The `admins:` section of `paisans-stack.yaml` and the secrets file is `operators:`, and `paisans admin` is `paisans operator`.
- apply makes each operator a member of `operators`, `members` and every app's groups, and a Pocket ID administrator only under `management: pocket-id`.
- The admin reconciler is the operator reconciler: it reads Pocket ID, writes nothing, and reports operators missing from Pocket ID, non-operators in `operators`, and administrators who are not operators.
- The admins heartbeat is the operators heartbeat, its token `operators_heartbeat_token`, its monitors `operator continuity`.
- The account app uses the deployment's one `pocket-id` app, and `identity_app` is refused.
- The uptime blueprint's admin group defaults to `operators`.
- `paisans create` asks who manages accounts, adds the account app under `paisans`, and asks for at least two operators.

### Fixed

- `init` and `secrets set` ask for an SMTP password, and `secrets rotate --all` rotates one, only when an app's mail settings name a username, so a relay with no login needs none.

### Security

- The uptime monitor requires STARTTLS under `security: starttls`, so mail and SMTP credentials are never sent in plaintext when a relay does not offer it.
- The gateway refuses any request to Pocket ID that carries an `X-API-Key` header with 403, so Pocket ID's API keys work only on the mesh.

[Unreleased]: https://github.com/Paisans-Software/paisans-cli/compare/v0.2.0...develop
[0.2.0]: https://github.com/Paisans-Software/paisans-cli/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Paisans-Software/paisans-cli/tree/v0.1.0
