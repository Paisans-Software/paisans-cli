# Every deployment runs as its own unprivileged user, admitted once by the host's owner

Status: draft, awaiting founder approval. Docker's behaviour is cited from
its rootless documentation (*Sources*); nothing here has run on a host yet.

## Why

Some people with spare capacity, homelab owners and people with a VPS, will
host a community's stack but will not give that community's operators sudo on
their machine. Today every operator needs it: the CLI runs every remote
command as `sudo sh -c` (`internal/apply/sudo.go`), `/srv/paisans`,
`/var/lib/paisans` and `/etc/paisans` are root's, and the system Docker
daemon is root on the host to anyone who can reach its socket. The compose
policy in `internal/render/policy.go` cannot close that gap, because it runs
on the operator's workstation, where the operator can change it.

The boundary has to be a Linux user. Each deployment runs as its own user
with its own rootless Docker daemon, so the most an operator, a bug in the
CLI or a hostile blueprint can reach is that user. Root is used once, by the
host's owner, to admit the deployment.

## Settled

Founder decisions, 2026-10-15:

* **Rootless on every site.** The community's own hosts work the same way as
  a borrowed one: `host admit` is the root step everywhere, and everything
  after it runs as the deployment's user without general sudo. Existing
  deployments are redeployed; nothing converts them.
* **The deployment's user is `paisans-<token>`** (`Deployment.Prefix()`),
  unless `sites.<s>.ssh.user` names another. Once a command has connected as
  that user, the CLI writes it into `sites.<s>.ssh.user`.
* **The new command is `paisans host admit`, undone by `paisans host evict`.**
* **Patroni's watchdog is the owner's to grant.** It reboots the whole
  machine. `host admit --watchdog` grants it; a data site on a host admitted
  without it is refused.
* **Ports below 1024 are the owner's to grant.** `host admit --public-ports`
  lowers `net.ipv4.ip_unprivileged_port_start` host wide and opens 80 and
  443. A gateway or a Caddy monitor on a host admitted without it is refused.
* **The deployment's user keeps its own `authorized_keys`, maintained by the
  operators.** Only `host admit` and `host evict` need the owner's root
  login.
* **`host allow`, `host disallow` and the key allowlist are removed.** They
  limited which keys root could add to a shared login user, and after
  admission no command writes keys as root.
* **An owner without the deployment's configuration admits it from a
  request.** The operator runs `host request` and sends the file; the owner
  runs `host admit --request` on the host itself. The owner needs the CLI
  and nothing else, so releases carry Linux binaries.
* **Forwarding follows the site's endpoint.** A site with an endpoint relays
  mesh traffic between sites that have none, as today, so admit turns on
  `net.ipv4.ip_forward` for it and says so in the plan. There is no flag.
* **Hosts that will not run a second Docker daemon are not supported.**
  Rootless runs beside a system daemon the owner already has.

## The deployment's user

The default name is `paisans-<token>`, eg `paisans-1a2b`: four hex digits
behind a letter, which `useradd`'s default `NAME_REGEX` on Debian and Ubuntu
accepts, where a bare token starting with a digit is refused.

`sites.<s>.ssh.user` becomes optional. Empty means the default. The first
command that connects as the user, `host admit`'s own check or any later one,
writes the name into `sites.<s>.ssh.user` through a new `config.SetSSHUser`,
which splices the value the way `SetArch` does and never overwrites one that
is there. A name in the file is the name used, so a deployment whose owner
picked a different name declares it once.

The user must not be privileged. `host admit` refuses a user that already
exists and is root, or is in `sudo`, `wheel`, `admin`, `adm`, `docker`,
`lxd` or `systemd-journal`. Any of those reaches past the user, and `docker`
in particular undoes the whole design, because it is the owner's system
daemon. `paisans doctor` checks the same list on every run.

## `paisans host admit`

The owner runs it once per deployment per host, and again whenever they
change what they grant. It is the only command that uses sudo. It runs in
one of two ways, and both do the same steps.

**On the host, from a request.** The usual case on a borrowed host: the
owner has the CLI but not the deployment's configuration.

```sh
sudo paisans host admit --request home-a.admit                                  # shows what it would do
sudo paisans host admit --request home-a.admit --watchdog --public-ports --execute
```

**From a workstation, from the configuration.** For an operator who is also
the host's owner, eg on the community's own hosts.

```sh
paisans host admit --site home-a --ssh owner@home-a.example.org                # shows what it would do
paisans host admit --site home-a --ssh owner@home-a.example.org --watchdog --public-ports --execute
```

`--ssh` is required with `--site`: it is the owner's login, not the
deployment's. Sudo works as it does today (`SudoAuth`, asked once, at a
terminal). `--request` and `--site` are refused together, and `--request`
is refused unless the command runs as root on the host.

Grants are opt-in. The plan lists the grants the deployment needs for its
roles; the owner gives each by passing its flag. Admitting without one is
allowed, and the admission record says it was not given, so the operator's
next command refuses with what is missing rather than failing halfway
through an apply.

Like `host prepare`, it probes read only, prints the plan, and changes the
host only with `--execute`. Every step is idempotent, so a re-run after a
failure resumes, and a re-run with different flags changes only what they
govern.

### What it does

1. **Docker.** If Docker is absent, install it from Docker's apt repository
   as `host prepare` does today, plus `docker-ce-rootless-extras` and
   `uidmap`. If Docker is present, refuse an Engine older than 29.5 and say
   the owner must upgrade it; upgrading the owner's Docker is not this
   command's decision. 29.5 is the version from which a rootless container
   with host networking shares the real host's network namespace
   (*Sources*), and every infra container is `network_mode: host`, bound to
   the mesh address. A snap Docker is refused, as today.
2. **AppArmor.** On Ubuntu 24.04 and later, unprivileged user namespaces are
   restricted unless a profile allows them. The deb package bundles the
   profile for `rootlesskit`; admit checks it is loaded and refuses with the
   fix if not.
3. **The user.** `useradd --create-home --shell /bin/bash paisans-<token>`
   unless it exists, then the privilege check above. Subordinate ID ranges
   of at least 65,536 in `/etc/subuid` and `/etc/subgid`, which `useradd`
   allocates and admit verifies.
4. **Linger.** `loginctl enable-linger`, so the user's systemd instance, and
   so its Docker daemon and timers, run without a login session and start at
   boot.
5. **cgroup delegation.** `/etc/systemd/system/user@.service.d/delegate.conf`
   with `Delegate=cpu cpuset io memory pids`, then `daemon-reload`. Without it
   only `memory` and `pids` are delegated, so compose CPU limits fail. The
   file is host wide and shared by every admitted deployment; it is written
   once and never removed by `evict` while another deployment is admitted.
6. **The rootless daemon.** As the user, with its runtime directory:
   `dockerd-rootless-setuptool.sh install --force`. `--force` is what lets it
   install beside a running system daemon. Then `~/.config/docker/daemon.json`
   with `"userland-proxy": false`, so a published port carries the client's
   source address (*Sources*), and the deployment's `default-address-pools`.
7. **The deployment's directory.** `install -d -m 750 -o <user> -g <user>
   /srv/paisans/<token>`. `/srv/paisans` itself stays root's, mode 755.
8. **The WireGuard helper and its sudoers line** (below).
9. **The firewall.** ufw rules tagged `paisans-<token>:` as today: the SSH
   port, the mesh's UDP `ListenPort`, `allow in on psns-<token>`, and 80/443
   with `--public-ports`. The `br-+` rules go: a rootless container's traffic
   to the mesh address leaves through the user's own process, not a host
   bridge. Defaults are set only on a host with no active firewall, as today.
10. **Egress.** An nftables table that keeps the deployment off the owner's
    network (below).
11. **The watchdog**, with `--watchdog`: softdog as `host prepare` does today,
    and a udev rule giving `/dev/watchdog` to the user. One deployment per
    host can hold it; a second is refused.
12. **Low ports**, with `--public-ports`: `net.ipv4.ip_unprivileged_port_start=80`
    in `/etc/sysctl.d/60-paisans.conf`, applied with `sysctl --system`. The
    output says plainly that this lets every local user on the host bind
    ports 80 to 1023.
13. **Forwarding**, for a site with an endpoint: `net.ipv4.ip_forward=1` in
    the same file. Such a site relays mesh traffic between sites behind NAT,
    which cannot dial each other (`isRelay` in `internal/render/site.go`).
    Today that is a `PostUp` in the WireGuard conf, which runs as root; in
    this design no hook is accepted (*The WireGuard helper*). The plan says
    that this turns on routing between every interface on the host, which
    the firewall's forward policy then governs.
14. **The admission record**, `/etc/paisans/admitted.<token>.json`, mode 644:
    the user, the grants (`watchdog`, `public_ports`), whether forwarding is
    on, the ports the firewall opened, the request's digest when there was
    one, and the time. It is how the deployment's own commands, which cannot
    read root's files, learn what they were granted, and what `host
    deployments` lists.
15. **The registry claim**, as today, under `/var/lib/paisans`, so two
    deployments on one host cannot collide on a token or a port.
16. **The keys.** Write the `ssh.keys` operators' keys, from the
    configuration or the request, to the user's `~/.ssh/authorized_keys`,
    owned by the user, so the operators can log in. From then on the
    operators maintain them (*Keys*).
17. **The check.** With `--site`, connect as the user with an operator's
    key, run `docker version` against the user's socket, and write
    `ssh.user` back (*The deployment's user*). With `--request`, run
    `docker version` as the user locally; the write-back happens on the
    operator's next command, which is the first to connect.

## `paisans host request`

The operator's half of admission from a request:

```sh
paisans host request --site home-a > home-a.admit
```

It writes a short YAML document to stdout and prints a digest of it to
stderr. The request holds nothing secret, so it can travel by email or chat:

| Field | From |
|-------|------|
| `version` | the request format, so an older CLI refuses a newer request |
| `deployment` | the deployment's ID |
| `user` | `sites.<s>.ssh.user`, or the default |
| `ssh_port`, `listen_port` | the site's SSH port and WireGuard `ListenPort` |
| `mesh_subnet`, `address` | the mesh subnet and the site's address in it |
| `endpoint` | whether the site has one, so forwarding is planned |
| `grants` | what the roles need: `watchdog` for a data site, `public_ports` for a gateway or a Caddy monitor |
| `operators` | each `ssh.keys` operator's name and public keys |

**The digest is the owner's check that the request is the one the operator
sent.** The request carries the keys that will get a shell on the owner's
machine, so anyone who could change it on its way could put their own key
in it. Admit prints the same digest, as twelve hex digits in three groups,
and the owner compares the two over a channel they trust, eg a call.
`--execute` with `--request` asks the owner to type the digest at a
terminal, so it cannot be scripted past.

Admit validates a request as strictly as a configuration: every field
present, the user name accepted by the same pattern as `ssh.user`, every
key a public key the toolkit can parse, the address inside the subnet.

## Running on the host

`--request` runs with no SSH, through a new local implementation of
`apply.Transport` that runs each command with `sh -c` on the machine
itself. It is only constructed by `host admit --request` and `host evict
--request`, never by a command that changes a deployment, so a mistyped
flag on a workstation cannot run a host's plan against the workstation.

`host evict` takes the same `--request`, or `--deployment <id>` with
`--user`, so an owner who has lost the file can still evict.

## Linux binaries

An owner on a host needs the CLI and nothing else: not Go, not Docker, not
Homebrew. The release workflow attaches `linux/amd64` and `linux/arm64`
binaries and a `SHA256SUMS` file to each release, and the guide for host
owners gives the download and the check. Admit itself needs only the
binary, run with sudo.

A change of WireGuard port, SSH port or grant needs a re-run, because each
is a root change. `host prepare` compares the declaration with the admission
record and refuses with the `host admit` line to run.

## `paisans host evict`

The reverse, run by the owner the same two ways as admit, with `--execute`
and the deployment's user name typed at a terminal, as `site remove
--delete-data` asks for the site's. It stops the user's systemd instance, disables linger,
removes the user with its home, `/srv/paisans/<token>`, the helper, the
sudoers line, the udev rule, the egress table, the deployment's ufw rules,
the admission record and the registry claim. Host-wide files (`delegate.conf`,
`60-paisans.conf`) are removed only when no other deployment is admitted, and
`ip_unprivileged_port_start` and `ip_forward` are each restored only when
no other admitted deployment holds `public_ports` or has forwarding on.

`site remove` does what the deployment's user can, then ends by printing the
`host evict` line for the owner.

## The WireGuard helper

The mesh is the one thing the deployment needs root for after admission:
creating `psns-<token>`, writing its conf and syncing peers all need
`CAP_NET_ADMIN` in the host's namespace. Admit installs a root-owned script
per deployment with the interface name built in,
`/usr/local/libexec/paisans/wg-psns-<token>`, mode 755, and one sudoers line
in `/etc/sudoers.d/paisans-<token>`, mode 440, checked with `visudo -c`:

```
paisans-1a2b ALL=(root) NOPASSWD: /usr/local/libexec/paisans/wg-psns-1a2b
```

Its verbs, the only arguments it accepts:

| Verb | Does |
|------|------|
| `put` | reads a conf on stdin, validates it, installs it as `/etc/wireguard/psns-<token>.conf`, 600 |
| `up` | `systemctl enable --now wg-quick@psns-<token>` |
| `restart` | `systemctl restart wg-quick@psns-<token>` |
| `sync` | `wg-quick strip` the conf, then `wg syncconf` |
| `show peers`, `show latest-handshakes`, `show public-key` | `wg show psns-<token> <field>` |
| `log` | the unit's last 200 journal lines |

**Validation is the security of this helper.** `wg-quick` runs a conf's
`PreUp`, `PostUp`, `PreDown` and `PostDown` as root, so a helper that
installed any conf it was given would be root for its caller. `put` accepts
only the sections `[Interface]` and `[Peer]`, only the keys the renderer
writes (`Address`, `PrivateKey`, `ListenPort`, `MTU` in the first;
`PublicKey`, `PresharedKey`, `AllowedIPs`, `Endpoint`, `PersistentKeepalive`
in the second), each value matched by a pattern, `ListenPort` equal to the
admitted one and every `Address` inside the mesh subnet. Anything else
refuses the whole file. The renderer stops writing the relay `PostUp`
(*What it does*, step 13).

The script is embedded in the CLI beside `internal/hostprep/snippets` and has
its own tests (*Tests*).

## Egress

Every infra container shares the host's network namespace, so without a
rule it can reach the owner's services bound to `127.0.0.1` and the owner's
LAN. Admit loads an nftables table `inet paisans_<token>` from a unit
`paisans-<token>-egress.service`, so it survives a reboot, with an output
chain that matches the user's UID and its whole subordinate range (a
container's users map into that range, so the UID alone misses them):

1. Accept the mesh subnet.
2. Accept port 53 to the host's resolvers, read at admit time
   (`127.0.0.53` under systemd-resolved, or the LAN resolver).
3. Drop `127.0.0.0/8`, `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`,
   `169.254.0.0/16`, `100.64.0.0/10`, `::1`, `fc00::/7` and `fe80::/10`.

A deployment that needs something on the owner's network asks the owner,
who adds an accept rule. That is deliberately outside the CLI.

## Keys

`host prepare` keeps managing `authorized_keys` from `ssh.keys`, now as the
deployment's user and in its own home, so adding or removing a co-operator
is an operator's change and needs no owner. The sidecar moves from
`/etc/paisans/authorized_keys.<user>.paisans-<token>.owned` to
`/srv/paisans/<token>/.paisans/authorized_keys.owned`.

That makes the allowlist pointless: it limited which keys root could add to
a shared login user, and no command writes keys as root after admission.
`host allow`, `host disallow`, `/etc/paisans/ssh-allow.<user>` and the
doctor allowlist check are removed.

An operator can therefore give anyone a shell as the deployment's user. The
owner's protection is that the user is all that shell reaches.

## What runs as the deployment's user

Every command except `host admit`, `host evict` and `host deployments`.
`SSHTransport.Sudo` is false for all of them and every `--sudo` flag is
removed. `DOCKER_HOST` is set to `unix:///run/user/<uid>/docker.sock` in
`RemoteCommand`, so `docker` never falls back to the owner's system socket.

What moves:

| Today | Becomes |
|-------|---------|
| `/var/lib/paisans/deployed.<token>.json`, `granted.<token>.json`, `gateway-move.<token>.json` | `/srv/paisans/<token>/.paisans/` |
| `/var/lib/paisans/config/<token>.*` | `/srv/paisans/<token>/.paisans/config/` |
| `paisans-<token>-config-*` system units | the user's systemd units, under linger |
| `/usr/local/lib/paisans-<token>/config-sync.sh` | `/srv/paisans/<token>/.paisans/bin/` |
| `/srv/caddy.d` | `/srv/paisans/<token>/caddy.d` |
| `/etc/docker/daemon.json` (mesh probe) | `~/.config/docker/daemon.json` |
| WireGuard writes and `wg`, `systemctl`, `journalctl` on the mesh | the helper |

What changes in behaviour:

* **`host prepare`** installs nothing and touches no root file. It checks the
  admission record against the declaration, writes the user's
  `authorized_keys`, the config store and its user units, and the daemon's
  `daemon.json`.
* **`hostcheck.Inspect`** stops requiring UID 0. It inventories the user's
  own daemon, and reads listening ports with `ss -Hltnu` (no `-p`), so a
  port held by another user is reported as taken without naming its
  process. The ufw probe goes; admit owns the firewall.
* **Volume prune** stops reading the volume's mountpoint with `du`, which a
  container user's files under the subordinate range can deny. It uses
  `docker system df -v`.
* **The disk checks** read `DockerRootDir` as today, which is now the user's
  data directory. Admit refuses a data directory on NFS (*Sources*).
* **`paisans doctor`** loses the sudo finding and the allowlist check, and
  gains: the user is not privileged, the rootless socket answers, Engine is
  29.5 or later, linger is on, `sudo -n -l` lists the helper and nothing
  else, and the admission record matches the declaration.
* **`host deployments`** stays an owner's command with `--ssh` and sudo, and
  lists admission records beside what it lists today.

## Documentation

* `docs/commands.md`: `host request`, `host admit` and `host evict` replace
  `host allow` and `host disallow`; `host prepare` and every `--sudo` flag
  are rewritten.
* `README.md`: the sections on what `host prepare` does, on the login user's
  authorized keys, and on Docker being judged by its version.
* A guide for host owners: installing the Linux binary and checking it,
  comparing a request's digest, what admitting a deployment changes on
  their machine, what each flag grants, and how to evict.
* `docs/deployment-agent-rules.md`: `host admit` and `host evict` are owner
  actions, never run by an agent unattended.

## Tests

* `config`: `ssh.user` optional; `SetSSHUser` splices and never overwrites.
* `hostprep` admit plan: golden plans for a blank Ubuntu 24.04, a host with
  an old system Docker (refused), one with a current one, and each flag.
* The helper's `put`: a table of confs, every hook key refused, every
  unknown key refused, an `Address` outside the subnet refused, the
  renderer's own output accepted.
* The egress table renders the user's UID and subordinate range.
* `host request`: a golden request per role mix; the digest is stable across
  runs and changes with any field. Admit refuses a request with a missing
  field, an unknown version, a bad key or a mistyped digest.
* Admit plans the same steps from `--request` as from `--site` for the same
  site.
* The local transport is constructed by no command other than admit and
  evict with `--request`.
* The release workflow's artifacts include both Linux binaries and
  `SHA256SUMS`.
* The Docker-tagged tests run against a rootless daemon.
* One end-to-end run on staging: admit a fresh host as its owner, apply as
  the deployment's user, then check that a container cannot connect to a
  service on the host's `127.0.0.1` or LAN, and that `sudo -n` as the user
  runs nothing but the helper.

## Sources

Docker's rootless documentation, read 2026-10-15:

* <https://docs.docker.com/engine/security/rootless/>: `uidmap`; 65,536
  subordinate IDs; `loginctl enable-linger`; `--force` beside a running
  system daemon; `DOCKER_HOST=unix:///run/user/<uid>/docker.sock`.
* <https://docs.docker.com/engine/security/rootless/tips/>: ports below 1024
  via `ip_unprivileged_port_start`; `Delegate=cpu cpuset io memory pids`,
  with only `memory` and `pids` delegated by default; data directory
  `~/.local/share/docker`, not on NFS.
* <https://docs.docker.com/engine/security/rootless/troubleshoot/>: host
  networking was namespaced inside RootlessKit until Engine 29.5; source IP
  propagation with RootlessKit 3.0 needs `userland-proxy` disabled; Ubuntu
  24.04's user namespace restriction and the bundled `rootlesskit` profile;
  `--cap-add` applies only inside the user namespace.

## Open

* **Patroni's watchdog device in a rootless container.** The device is the
  user's, which is root inside the container, so the `chown postgres` in
  the infra compose should work. Unobserved.
* **`watchdog: off`.** A data site that declares it needs no device. Is it
  allowed on a host admitted without `--watchdog`, or refused as the
  decision above reads?
* **`web_server.managed_by: external`.** The include is written into the
  owner's web server and reloaded with sudo. Under this design the CLI
  cannot do either. Proposed: render the include under
  `/srv/paisans/<token>/web-server/`, and have admit add one include line
  to the owner's server, with reloads left to the owner.
* **The home directory.** `/home/paisans-<token>` holds the Docker data
  directory, so it has to be on a disk with room. Should admit take a
  `--home`?
