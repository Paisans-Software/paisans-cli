# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Each change gets one bullet, a single sentence of at most 35 words; related
changes share a bullet. Detailed notes are on the corresponding
[GitHub release](https://github.com/Paisans-Software/paisans-cli/releases).

## [Unreleased]

### Added

- `--ssh` and `--from` accept ssh config aliases, `[user@]alias[:port]`, and connect through them; a destination given without a user logs in as its ssh config `User`, or your local login.
- `doctor` checks each host's swap, and `doctor` and `site admit` warn when a host's RAM, with swap counting up to a quarter, is under the floor its roles need.
- `deployment checkout <id> --execute` copies a saved deployment, with its signed store and trust state, into an empty directory, where `deployment log`, `pull` and `publish` work without pinning a root.

### Changed

- `deployment apply` and `oidc client create` clear a toolkit-set launch URL from the client of any `no_launch` blueprint, now `account` and the gate's `oauth2-proxy`, and leave one an operator set.
- `init` moves a never-applied site's WireGuard listen port, when another deployment on the host holds it and the endpoint is unset or a port alone, to a free one and says which port to open.

### Fixed

- `doctor`'s self-check and `deployment apply`'s gateway step report a 443 answering in anything but TLS as another server holding the port, and doctor advises a refused connection apart from a timeout.
- `dns init` updates an A or AAAA record it made for this deployment to the address now declared, so a gateway whose public address changed needs no records deleted by hand.
- `site add` deletes a rejoining site's old etcd data before adding its learner, so a site an earlier removal took out of etcd rejoins instead of crash-looping; `site remove` empties a leaving member's too.
- `deployment apply` keeps Patroni's leader through a join by writing a `patroni.env` whose only change is `ETCD3_HOSTS` without recreating Patroni, and warns whenever a recreate may move, or has moved, the leader.
- `deployment recover --from` takes any login that reaches the host, the owner's too, and reaches it again as the deployment's user its admission record names, so a recovery needs no name the lost workstation held.
- `deployment recover`, `deployment pull`, `init --age-private-key-file` and the saved version's `applied from` note of `deployment apply` name the configuration's directory as a full path, not `.`.
- `deployment recover`, `pull` and `log` take `--key <path.pub>` to anchor an unpinned workstation on versions you signed, `log` prints full root hashes, and a pin the sites serve none of refuses.

### Security

- Pasted secrets holding a control character or line separator are refused when set or loaded, and the DNS tokens and OIDC client credentials are quoted in env files, so none can add a variable.

## [0.3.0-rc.5] - 2026-10-19

### Added

- `paisans operator grants --ssh <alias or user@host>` lists each site's grants and flags on a host, yes or no, and the `site admit` command that admitted it, read without sudo.
- The mbin blueprint renders and locks every Meta, Instance and Federation setting from `apps.<app>.settings`, titling the instance from `display_name` and `community.name`; it needs Mbin `1.16.0+paisans`.
- Blueprint format `paisans.blueprint/v3` gives templates `.DisplayName` and `.CommunityName`, so a catalog release using them is skipped by older binaries.
- The uptime monitor's public status page is on by default, showing only each app's check at its public hostname; `settings.public_status_page: false` turns it off.
- `site admit` and `site prepare` warn about a host with no swap, and `site admit --swapfile`, or a yes at a terminal with `--execute`, makes a swapfile of twice its RAM, up to 16 GiB.
- `deployment apply --execute` and `deployment recover --execute` save the version to this workstation's data directory, `deployment list` lists saved deployments, and `deployment fetch --ssh` saves every deployment a host's registry holds.

### Changed

- Rendered files and the WireGuard boot drop-in name `paisans deployment apply` in their comments; the next apply rewrites each once, restarting containers that mount them as any change to those files does.
- `apply` and the `config` subcommands moved under `deployment`: `paisans deployment apply`, `deployment publish`, `deployment pull`, `deployment log` and `deployment recover`; `paisans apply` and `paisans config` are gone.
- Where sudo is sudo-rs, `site admit` and `doctor` read the sudoers Defaults from the original sudo installed beside it as `sudo.ws`, so a stock Ubuntu 26.04 host is admitted without switching sudo.
- A dry run says what `--execute` will do in the future tense, so a note such as `ssh.user paisans-c111, --execute will record it` reads as yet to happen.

### Fixed

- `deployment apply` names, under each site's pull line in a dry run and with `--execute`, every image whose build the apply moves, with the versions and digests it goes between.
- A first ssh connection to a host whose key is known runs in batch mode, so its spinner and timer show; only a login that needs a password or passphrase pauses them.
- `deployment apply` sets `src_valid_mark` to 0 in the rootless Docker namespace, which copies Tailscale's 1, ending a one second wait per connection and the apps' wrong client addresses; `doctor` checks it.
- Caddy mounts its Caddyfile and snippets as one directory, `infra/caddy/etc`, so a reload after `deployment apply` reads the new routes; the old files are left over, and the next apply replaces Caddy once.
- Under `management: paisans`, `validate` refuses `signup_default_groups` without the account app's member group and `create` writes `[members]`, so an accepted invite makes a member rather than an account in no group.
- `init` moves a never-applied mesh subnet clear of one `site admit` refused, admitting each site again on it instead of requiring evictions, and admit advises on a clashing role before the subnet.
- `site admit` says each clash with another deployment on a host in one short sentence naming its id and domain or site, and `init`'s skip of that host omits admit commands.
- `init` moves the mesh subnet clear of one `site admit` refused beside a role, skipping that site, instead of keeping the subnet until the role clash is fixed.
- A command without `--execute`, such as `init` or `validate`, says nothing of what `--execute` records about a login user, since that flag belongs to other commands.
- `create` writes `cluster.synchronous: true` for any layout with two or more data sites, a custom one included, instead of only the high availability shape, so a failover loses no acknowledged write.

## [0.3.0-rc.4] - 2026-10-17

### Added

- `site admit`, `site prepare` and the rootless deployment commands support Ubuntu 26.04, and admit's sudo-rs refusal says how to make the original sudo, installed as `sudo.ws`, `sudo` instead.

### Fixed

- `init`'s skip keeps the lines of why `site admit` stopped, so a refused release lists every supported release on its own line instead of joining the first to the sentence.

## [0.3.0-rc.3] - 2026-10-17

### Fixed

- A site that does not answer is classified down on one connection attempt instead of three, and no later command of the run waits for its host again.
- `site admit --enable-ufw` refuses while enabling ufw would block a port that answers from off the host now, listing each with the `ufw` line that keeps it reachable, unless `--enable-ufw-block-listed` accepts them.

## [0.3.0-rc.2] - 2026-10-17

### Added

- `init` lists every site not admitted, with why, then at a terminal offers to admit each with `site admit --site`'s own flow; otherwise, and with `--yes`, it prints the commands that admit it.

### Fixed

- A site whose login sshd ends with "Too many authentication failures" and then a disconnect notice reads as a refused login, so `init` and `apply` skip a site not yet admitted instead of failing.
- An encrypted `secrets.enc.yaml` without `.sops.yaml` is never rewritten in plaintext: writes refuse, naming its recipients and the restore command, and a plaintext secrets file is refused wherever a deployment reads it.

## [0.3.0-rc.1] - 2026-10-17

### Added

- `apply` runs on the sites it has: a site not yet admitted is left out, a joined site that does not answer is kept, the plan lists them, and `--require-all` fails a degraded run.
- The gateway's Caddy answers `/.well-known/paisans/<token>`, and `apply` ends a gateway's step by fetching it through the public address, failing when another server answers on 443 or 80.
- `infra.images` and `infra.postgres.<major>.spilo` override the infrastructure stack's images without a catalog release, and `paisans images` lists each site's builds, what would move, and overrides behind their track.
- `site request --site <s>` writes the request a host's owner admits that site from, naming the site and its roles, and prints its digest on stderr for the owner to compare over a trusted channel.
- Any run with `--execute` that logs in to a site leaving `ssh.user` out writes the deployment's user there; a run without `--execute` only says so, and a failed write is a warning.
- `site admit` and `site evict` let a host's owner admit a deployment as its own unprivileged user with rootless Docker, from a request on the host or over their own SSH, and undo it.
- `site list` prints every site the configuration declares, with its roles, mesh address, SSH destination and public addresses, reading `--config` or the working directory's file.
- `site prepare --all` prepares every site the configuration declares, one at a time in its order, each with its own review, stopping at the first failure.
- `site admit`, `site prepare` and the rootless deployment commands support Ubuntu 22.04 beside 24.04.
- Each release attaches static `paisans-linux-amd64` and `paisans-linux-arm64` binaries and a `SHA256SUMS` file, so a host owner can run the CLI on Linux without Go.
- `doctor` checks each site as its deployment's user: its admission record, no privileged group, rootless Docker at Engine 29.5 or later, linger after the mesh, and sudo for the WireGuard helper alone.
- `doctor` fetches `/.well-known/paisans/<token>` from the gateway and each monitor running Paisans' Caddy, from the workstation as `apply` does, and fails when 443 or 80 does not answer as this deployment.
- `create` offers the first operator each SSH public key in `~/.ssh/*.pub` on the workstation, so a key already there is taken with Enter rather than pasted.
- `--yes` answers every confirmation a command asks for, printing each answer, when `PAISANS_ALLOW_YES=1` is set; `site admit --request` takes its digest as `--digest`, which `--yes` never supplies.
- Every record paisans writes names the version, commit and modified flag of its build, refusing one without; develop builds report `X.Y.Z-dev`, and `X.Y.Z-rc.N` cuts publish as GitHub pre-releases.

### Changed

- `site remove --host-gone` cuts the site out of the mesh first, waits for Patroni's own failover and skips its Garage node as dead, for a dead host or one no longer the deployment's to reach.
- Every `host` command is under `site`: `site request`, `site admit`, `site prepare`, `site evict` and `site deployments`, beside `site list`, `site add` and `site remove`.
- `dns init` writes the registrable domain's DMARC policy when nothing under it has mail, publishes SPF on `community.domain` alone, and gives a hostname DMARC of its own only where no strict policy above covers it.
- `site evict` refuses, with nothing changed, a host any deployment of which a newer release of paisans claimed, naming the highest release to upgrade to.
- `validate` warns about one operator instead of refusing it, and refuses a deployment with no operator (`operators-none`); `paisans create` accepts one, and the operator reconciler fails the operators heartbeat until a second is declared.
- `paisans create` writes the file to `--config`, or `paisans-stack.yaml` in the current directory, once validate refuses nothing in it, without printing it or asking first.
- `site remove` refuses, in every form and with nothing changed, a site a newer release of paisans applied; `apply` records its release in the manifest, and `site admit` in the registry entry.
- `doctor` reports a site pending admission, or down after joining, as a warning naming what the deployment is without and how to admit or remove it; a gateway stays a failure.
- `doctor`'s advice for a container whose mesh address was missing at boot is to start it again, as the deployment's user, or recreate its stack.
- `secrets set` and `secrets rotate` try an SMTP password from the machine they run on, warning when the server cannot be reached there, and rotate tries it from every site before handing it to any.
- Behind the owner's web server, `apply` writes each app's reference snippet to `/srv/paisans/<token>/web-server/` from what `site admit` detected, and warns until the owner's copy serves it; nothing of the owner's is written or reloaded.
- `site remove` cleans a host only as the deployment's user its admission record names, empties its root through `rootlesskit`, and ends with the `site evict` line the host's owner runs to remove the rest.
- `site deployments` lists every admission record on the host, with the user, site, grants and time, and lists only those with `--sudo=false`.
- `paisans init` settles the mesh subnet on the hosts that have admitted the deployment and leaves the rest to `site admit`'s own check; `config recover` finds the deployment from the login's admission record.
- `paisans prune` reads each dangling volume's size from `docker system df -v`, which the deployment's user can read for a volume a container's user owns.
- The host check runs as the deployment's user: it reads listeners with `ss -Hltnue`, counts its own user's and containers' as this deployment's, and leaves the firewall to `site admit`.
- Every command that changes a host checks first that the host admitted the deployment as that site, with the grants its roles need, and names the `site request` and `site admit` lines when not.
- `site prepare` installs nothing: it checks the admission record, then keeps the deployment user's `authorized_keys`, configuration store in `.paisans/config/`, user systemd units and `daemon.json`.
- The mesh subnet check reads the deployment's own Docker pools from `~/.config/docker/daemon.json`.
- The host owner's Caddy site files beside a gateway go in `/srv/paisans/<token>/caddy.d`, which `apply` creates as the deployment's user, instead of `/srv/caddy.d`.
- The deployment record, the grant record and a gateway move's marker live in `/srv/paisans/<token>/.paisans/`, written by the deployment's own user under one lock of the deployment's.
- `sites.*.roles` lists each role once, so a repeated role is refused when the configuration loads, as `site request` requires.
- Preflight compares a new data site with the existing data sites on architecture alone, so data sites may run different Ubuntu releases, since Postgres uses its container's glibc.
- `watchdog: off` is refused, since a data site's Patroni needs a watchdog to fail over safely and no other site uses one.
- `apps.*.hostname` and `apps.*.hostnames.*` must be lowercase DNS names of two labels or more, so a capital, a single label, an empty or over-long label or an underscore is refused by `validate`.
- `sites.*.web_server.listen` must be an IPv4 loopback address or the site's own mesh address, its port written without leading zeros, as `site request` requires; `web-server-listen-not-this-host` refuses anything else.
- `mesh.subnet` and `sites.*.address` must be written in canonical form, Eg: `10.44.0.0/24` and not `10.44.0.0/024`, as `site request` carries them.
- `mesh.subnet` larger than a /24 or overlapping 0.0.0.0/8, 127.0.0.0/8, 169.254.0.0/16 or 224.0.0.0/3 is refused, as is a site address on its network or broadcast address, since hosts' firewalls trust the mesh.
- `mesh.subnet` outside 10.0.0.0/8, 172.16.0.0/12 and 192.168.0.0/16 is refused, replacing the `mesh-subnet-is-not-private` warning, so another deployment's user on a shared host cannot reach the mesh.
- Each site's mesh file renders inside the deployment's directory, as `/srv/paisans/<token>/psns-<token>.conf`, and carries no `PostUp`: a relaying site's forwarding is the host's sysctl.
- `apply` starts, syncs and restarts each site's mesh through its WireGuard helper, `sudo -n /usr/local/libexec/paisans/wg-psns-<token>`, which checks the mesh file before installing it in `/etc/wireguard`.
- `doctor` advises reading a down mesh interface's journal with its WireGuard helper's `log`, which the deployment's user can run, instead of `systemctl status` and `journalctl`.
- `site add`, `site remove` and `mesh rekey` read each site's handshakes, peers and key through the WireGuard helper, as the deployment's user, instead of running `wg show`.
- `dns init` creates `v=DMARC1; p=reject; sp=reject;` for any mail name under `community.domain` with no MX record and no DMARC policy, instead of only warning that the policy is missing.
- Below its registrable domain, `community.domain`'s hostnames each get their own reject DMARC policy, and the registrable domain's policy is checked and warned about, never written.
- Docker commands on a site reach the login user's rootless daemon at `/run/user/<uid>/docker.sock`, the uid read once a run, and a refused default user is reported as a site not admitted.
- `preflight`'s ssh check asks only that each site answers as its ssh user, and `doctor` has no sudo finding, since no command they check for runs through sudo.
- `operators.*.keys` must be ed25519, ECDSA, a security key's `sk-` type or RSA of at least 3072 bits, as `site request` requires, so a certificate or a shorter RSA key is refused.
- `sites.<site>.ssh.user` may not be `root` or another deployment's `paisans-<token>` user, as `site request` requires.
- `sites.<site>.ssh.user` is optional: a site that leaves it out is reached as the deployment's own user, `paisans-<token>`, which must exist on the host.
- `paisans create` asks each machine's SSH user as optional, offering the deployment's own user, `paisans-<token>`, when left blank.
- `init` ends with `Next: run paisans secrets set` when a secret only the operator can give is unset, shows secret counts only with `--verbose`, and the SMTP password's reason is its login and host.
- When sudo refuses the login outright, `site deployments` advises naming a login that has sudo with `--ssh`, since it reads no site's ssh section.

### Removed

- `host allow`, `host disallow` and the key allowlist in `/etc/paisans/ssh-allow.<user>` are removed, with `site prepare`'s refusal of keys it lacked and `doctor`'s allowlist check.
- `--sudo` is removed from every command but `site deployments`: each runs as the site's ssh user, and `apply`, `site remove`, `site prepare --all` and `config` ask no sudo password before they start.
- `web_server.include_dir`: `validate` refuses it, since `apply` writes nothing into the host owner's web server.

### Fixed

- `--help` for `apply --recreate` and `site remove --id` names the flag's value type, not a phrase from its description.
- Catalog blueprints can enable `no-new-privileges` without failing the container policy check; options that disable confinement and malformed entries remain refused.
- `init` before any `site admit` no longer stops at an SSH password prompt: it marks each site admitted, skipped or unreachable, and settles the subnet from the admitted ones.
- Every mesh overlap check reads every routing table, so a route another VPN keeps in its own table, such as Tailscale's table 52, is refused.
- Mesh overlap refusals in `init`, `apply`, `site prepare`, `preflight` and `doctor` show control characters in names a host printed as escapes, so none is drawn on the terminal.
- `apply --execute` starts no peer sync on a site `site prepare` has not given a store, which has nothing to sync, rather than warning on every bare host that its sync did not complete.
- `site remove` and `app remove` read every flag given after the name before they report, so `-v` there shows detail and `--execute` there records `ssh.user`.
- `apply` on a site whose mesh interface is up without its file's address, key or peers restarts or syncs it, so a run stopped between writing the file and handing it over completes.
- A deployment binds and dials only each site's mesh address, so the egress table `site admit` loads, which drops its loopback dials, stalls neither etcd's own client gateway nor Patroni's leader election.
- `apply` starts a stack whose containers were removed or stopped though its files match the render, and applies each Garage site before storage, so a site whose containers were removed converges again.
- `create` refuses, as it is pasted, an operator's key another operator gave, one listed twice, or one the loader refuses, rather than rendering a file that does not load.
- `create` offers to write a file validate refuses as it stands, to fix by hand, so the answers are not lost.
- `apply` that records a Pocket ID client's credentials or a signup group's ID publishes the bytes it wrote as the next revision, which its host records, so a first apply completes.
- `apply` ends `start pocket-id` done when its site stops for etcd or its databases, lists a failed install step's error under it, and names the member a later founding stop waits on.
- `apply` joins a pending site declared `[data, apps]` in one run: its `site add` joins the data role, then the site is applied to start its apps, and the gateway after it.
- `apply` brings up the Caddy of each gateway outside `etcd.members` before the data members are founded, so their apps reach Pocket ID's public URL as they start; `apply --hold-apps` starts no app stack.
- `site remove --host-gone` proceeds when only the gone site's etcd member is unhealthy; it, `site add`, `mesh rekey` and `secrets rotate` read patronictl past its log lines and name each unhealthy or missing etcd member.
- `site remove` that takes a witness out of etcd removes its etcd container, running or stopped, and keeps its volumes, so the witness has none for its restart policy to start.
- `site add` and `storage add` record every file they write in each site's manifest, so no later `apply` takes one for a hand edit; `apply --overwrite <path>` per refused file repairs an earlier record.

### Security

- Files written to a host are staged in a `mktemp` file beside the target, created exclusively and renamed into place, so a symlink planted at a guessable name is never written through.
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

[Unreleased]: https://github.com/Paisans-Software/paisans-cli/compare/v0.3.0-rc.5...develop
[0.3.0-rc.5]: https://github.com/Paisans-Software/paisans-cli/compare/v0.3.0-rc.4...v0.3.0-rc.5
[0.3.0-rc.4]: https://github.com/Paisans-Software/paisans-cli/compare/v0.3.0-rc.3...v0.3.0-rc.4
[0.3.0-rc.3]: https://github.com/Paisans-Software/paisans-cli/compare/v0.3.0-rc.2...v0.3.0-rc.3
[0.3.0-rc.2]: https://github.com/Paisans-Software/paisans-cli/compare/v0.3.0-rc.1...v0.3.0-rc.2
[0.3.0-rc.1]: https://github.com/Paisans-Software/paisans-cli/compare/v0.2.0...v0.3.0-rc.1
[0.2.0]: https://github.com/Paisans-Software/paisans-cli/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Paisans-Software/paisans-cli/tree/v0.1.0
