# Every deployment runs as its own unprivileged user, admitted once by the host's owner

Status: draft, awaiting founder approval. Docker's behaviour is cited from
its rootless documentation (*Sources*), and what a host did is under
*Observed*. No toolkit code has run rootless yet.

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
  unless `sites.<s>.ssh.user` names another. Once a command run with
  `--execute` has connected as that user, the CLI writes it into
  `sites.<s>.ssh.user`, whichever command it is. A run without `--execute`
  writes nothing and says the user will be recorded by `--execute`.
* **The new command is `paisans host admit`, undone by `paisans host evict`.**
* **Patroni's watchdog is the owner's to grant.** It reboots the whole
  machine. `host admit --watchdog` grants it; a data site on a host admitted
  without it is refused, whatever its `watchdog` mode. A Patroni member
  without a watchdog can keep acting as leader after it has lost its lock,
  so failover is not safe without one: `validate` refuses `watchdog: off` on
  a data site.
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
* **A system Docker admit installs never runs.** Where Docker is absent,
  admit installs Docker's packages for the rootless daemons and keeps the
  system daemon they bring disabled; the packages stay.
* **A deployment runs on the sites it has.** A site whose host has not
  admitted it yet, or cannot be used, does not hold up the rest, except
  where the deployment cannot run without it (*A deployment with sites
  missing*).

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
exists and is root, is a system account (below UID 1000), is in a group of
GID 0 under any name, or is in `sudo`, `wheel`, `admin`, `adm`, `docker`,
`lxd`, `systemd-journal`, `disk`, `shadow`, `kmem`, `libvirt` or
`incus-admin`. Any of those is root-equivalent or reads root's secrets, and
`docker` in particular undoes the whole design, because it is the owner's
system daemon. `paisans doctor` checks the same list on every run. Admit
also refuses, naming every problem it finds at once:

* a rule `sudo -l -U` shows for the user beyond admit's own helper line;
* a Defaults entry that would pass the user's environment, PATH, root or
  working directory to that helper (`setenv`, `env_keep`, `!env_reset` and
  their kin), or one set for a runas user or a command alone; `env_delete`,
  which only takes variables away, passes;
* sudo-rs, whose `sudo -l` lists no Defaults, so none can be read;
* a home that is a link, is not the user's, or lies below a link or a
  directory another user can write;
* for a user it is about to create, a home already present, a group of its
  name, which `useradd` would fail on, and a sudoers file other than
  admit's own that names it, whose rules would reach the new user;
* a subordinate range that starts below 100000 or holds 0, 65534 or the
  user's own ID.

## `paisans host admit`

The owner runs it once per deployment per host, and again whenever they
change what they grant. It is the only command that uses sudo. It runs in
one of two ways, and both do the same steps.

**On the host, from a request.** The usual case on a borrowed host: the
owner has the CLI but not the deployment's configuration.

```sh
sudo paisans host admit --request home-a.admit                                  # shows what it would do
sudo paisans host admit --request home-a.admit --watchdog --public-ports --data-dir /srv/data --execute
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

Grants are the owner's to give, and they must match the site's roles. The
plan lists the grants the deployment needs for its roles, and the owner
gives each by passing its flag. A grant the roles need and the owner did
not give, and one they do not need, each stop admit before it reaches the
host (below). The admission record holds the grants given.

Like `host prepare`, it probes read only, prints the plan, and changes the
host only with `--execute`. Every step is idempotent, so a re-run after a
failure resumes, and a re-run with different flags changes only what they
govern. A step that needs the user's UID is planned with a placeholder and
run once the user exists: `--execute` reads the host again then, and goes
on only when what is left is the approved steps, in order and unchanged,
less any now done, which it reports as done already.

**A data site without `--watchdog` stops admit before anything else.** The
request's `grants` say whether the site needs the watchdog, and the check
runs first, before any probe of the host, the plan or the digest prompt.
Without the flag, admit exits 1 having read and changed nothing, saying the
site is a data site, that its Patroni member needs the watchdog to fail over
safely and that the watchdog can reboot the machine, and that the owner
either admits it with `--watchdog` or does not admit it. A re-run that drops
`--watchdog` from a data site already admitted stops the same way, and
leaves the grant in place.

**A gateway or a monitor without `--public-ports` stops admit the same
way**, first, when the site's roles in the configuration include the
gateway, or a monitor whose web server is the toolkit's own Caddy, since
those bind 80 and 443. The message names the role and the ports. A site with
neither role is never stopped for the grant's absence, and a monitor behind
the owner's web server (`managed_by: external`) needs no low port.

**`--data-dir <path>` puts the deployment's Docker data on a disk the owner
chooses.** Rootless Docker keeps every image, layer and named volume under
the user's home by default (`~/.local/share/docker`), which for a data site
is the database and its WAL archive and for a Garage site the stored
objects. On many hosts `/home` is on a small root disk and the room is on a
second disk, which only the owner knows. Without the flag the data stays
under the home. `<path>` must exist, root's, mode 755 and writable by no one
else; one that does not exist is refused, naming the command that creates
it. With it, admit creates `<path>/paisans-<token>`, owned by
the user, mode 700, and sets `data-root` to it in the user's `daemon.json`;
the home itself stays where it is. Either way admit refuses a filesystem
Docker's overlay storage cannot use, NFS included, and its plan states the
free space where the data will live, warning below 20 GB for a data or
Garage site and 5 GB otherwise. The path is in the admission record.

### What it does

1. **Docker.** If Docker is absent, install it from Docker's apt repository
   as `host prepare` does today, plus `docker-ce-rootless-extras` and
   `uidmap`. If Docker is present, refuse an Engine older than 29.5 and say
   the owner must upgrade it; upgrading the owner's Docker is not this
   command's decision. The extras are installed pinned to the installed
   `docker-ce` version, and admit refuses when the repository has none, since
   apt would otherwise upgrade Docker to match them; a `dockerd` whose
   version cannot be read, or a Docker that is not dpkg's installed
   `docker-ce`, is refused too. Docker's apt key is trusted only when the
   download holds one primary key, public or secret, its subkeys aside, with
   the fingerprint of Docker's release key, since apt trusts every key in the
   file. 29.5 is the version from which a rootless container with host
   networking shares the real host's network namespace (*Sources*), and every
   infra container is `network_mode: host`, bound to the mesh address. A snap
   Docker is refused, as today.

   When admit installs Docker, the system daemons its packages bring never
   run. As it starts, `dockerd` turns on forwarding for every interface,
   creates `docker0`, adds its NAT and filter chains and sets the FORWARD
   policy to DROP; a daemon that never started changed none of it, where one
   stopped after would leave each change to be undone exactly. So the install
   runs under a `/usr/sbin/policy-rc.d` of admit's, which Debian's
   `invoke-rc.d` and `deb-systemd-invoke` ask before they start a service: it
   forbids (101) the units of what the install adds, `docker`,
   `docker.socket` and `docker.service` for `docker-ce`, `containerd` for
   `containerd.io` only when admit adds that too, and asks the owner's own
   `policy-rc.d` about every other. There is never a moment with no policy in
   place: admit's is written whole, the owner's is copied, through a
   temporary name so a copy cut short is never taken for it, to
   `policy-rc.d.paisans-owner`, and admit's is renamed over it. The owner's
   goes back when the install exits, however it exits. Saving the owner's
   policy fails closed: if the copy or its rename fails, the step stops
   before admit's policy is in place, with the owner's untouched. An install
   that fails disables the units of what it adds from its exit trap, while
   the hold is still in place, since their packages' scripts may have
   enabled them before it failed. The hold holds
   `/run/paisans-docker-hold.lock` throughout, so one run alone holds
   Docker's daemons. What a killed run leaves, admit's policy, the owner's
   copy beside it or beside nothing, or a temporary file of admit's, is put
   right by the next admit, or by evict, whether or not another deployment
   stays, under the same lock: while a running admit holds it, putting back
   refuses, saying so. A copy that differs from the policy in place is the
   owner's to choose between, and admit refuses, naming both. When the
   owner's `containerd.io` is installed, apt may upgrade it to the version
   `docker-ce` needs, which restarts it; the plan says so. The next step disables the same units, and fails if
   `docker0` exists anyway, naming a reboot, which clears what the daemon
   changed now that it is disabled.

   `/etc/paisans/installed`, root's and mode 644 (admit and evict refuse it
   otherwise), records what admit installs that the host did not have (each
   package, Docker's apt key and source) and the directories it creates
   (`user@.service.d`, below). Before the install each package is claimed
   as `installing`. The install takes the hold's lock first, then, from its
   exit trap, however it exits (a signal included) and before the hold is
   released, makes each claim whose files dpkg has (installed, or unpacked
   or half-configured by a failed install) a `package`, writing the list
   through a temporary file of its own. A claim still `installing` stands
   only while dpkg has the package's files and admit's own policy is still
   in place, which says that install was killed outright and apt may have
   run under it; a leftover from before the policy was in place keeps no
   claim. Any other claim, and any package whose files dpkg does not have,
   is dropped, so a Docker the owner installs after an admit that stopped
   early is never taken for admit's. Putting a hold back confirms the same
   way, under the lock, before it releases the hold. The admission record
   says admit disabled the daemon once; while a record says so, or another
   deployment's record cannot be read and so may, admit leaves the units as
   they are, so an owner who enables the daemon keeps it. A Docker the owner
   had before admit, and a `containerd.io` the host had, are never held,
   disabled or otherwise touched.

   The system Docker admit holds disabled, with no unit enabled and no
   network store (`/var/lib/docker/network/files/local-kv.db`), has never
   run, so the mesh's check reads it as having no networks. Any other Docker whose daemon does
   not answer still has its networks, which come back when it starts, so
   admit stops and says to start it.
2. **AppArmor.** On Ubuntu 24.04 and later, unprivileged user namespaces are
   restricted unless a profile allows them. Ubuntu's `apparmor` package ships
   the profile for `rootlesskit` at `/etc/apparmor.d/rootlesskit`, loaded at
   boot; admit checks it is loaded before it installs anything and refuses
   with the fix (install or update the `apparmor` package, load the profile)
   if not.
3. **The user.** `useradd --create-home --shell /bin/bash paisans-<token>`
   unless it exists, then the privilege check above. Subordinate ID ranges
   of at least 65,536 in `/etc/subuid` and `/etc/subgid`, which `useradd`
   allocates and admit verifies.
4. **Linger.** `loginctl enable-linger`, so the user's systemd instance, and
   so its Docker daemon and timers, run without a login session and start at
   boot.
5. **cgroup delegation.** `/etc/systemd/system/user@.service.d/delegate.conf`
   with `Delegate=cpu cpuset io memory pids`, then `daemon-reload`. Ubuntu
   24.04 already delegates `cpu`, `memory` and `pids`, so compose CPU and
   memory limits work without it; it adds `cpuset` and `io` (*Observed*).
   Admit writes it before the user's manager first starts and never
   restarts `user@<uid>.service` for a user that is already running,
   because that restarts the user's daemon and every container in it. The
   file is host wide and shared by every admitted deployment; it is written
   once and never removed by `evict` while another deployment is admitted.
   Beside it, for this user only,
   `/etc/systemd/system/user@<uid>.service.d/paisans-mesh.conf` with
   `Wants=` and `After=wg-quick@psns-<token>.service`, so at boot the user's
   manager, and with it the Docker daemon, starts after the mesh: a
   container that publishes on the mesh address fails when the address is
   not up yet, and Docker does not retry it. It takes effect at the next
   boot, like `delegate.conf`, and evict removes it. Until `apply` writes
   `/etc/wireguard/psns-<token>.conf`, that unit has nothing to bring up, so
   admit writes
   `/etc/systemd/system/wg-quick@psns-<token>.service.d/paisans-conf.conf`
   with `ConditionPathExists=` on the conf: the unit is skipped at boot,
   never failed, and stays out of `systemctl --failed`. Evict removes it.
   When `user@.service.d` is absent before admit writes `delegate.conf`,
   admit records it as a directory it created, and evict removes it with
   the file once it is empty.
6. **The rootless daemon.** First `br_netfilter`: loaded now and listed in
   `/etc/modules-load.d/paisans.conf`, because without it the daemon refuses
   to start with the userland proxy off (*Observed*). The module is host
   wide: once loaded, traffic crossing any of the host's bridges passes the
   firewall's FORWARD chain, which it skipped before, and that chain often
   drops by default (a system Docker sets it to DROP, ufw denies routed
   traffic). So admit reads first whether it is loaded and which bridges the
   host has (`ip -d link show type bridge`), and:
   * already loaded, as on most hosts with a system Docker: the plan says so,
     and nothing changes;
   * not loaded, with no bridge but Docker's: admit loads it, in one line of
     the plan;
   * not loaded, with other bridges (libvirt's, LXD's, the owner's own):
     the plan names each, quotes the FORWARD chain's policy, and says their
     traffic will be filtered by it from now on; admit stops, as it does
     for a missing grant, unless the owner passes `--bridge-netfilter`.
     The flag is in the admission record. Then, as the user,
   with its runtime directory: `~/.config/docker/daemon.json` with
   `"userland-proxy": false`, so a published port carries the client's
   source address (*Observed*), the deployment's `default-address-pools`,
   and, with `--data-dir`, `data-root`. Then `dockerd-rootless-setuptool.sh
   install --force`, so the daemon's first start reads that file.
7. **The deployment's directory.** `install -d -m 750 -o <user> -g <user>
   /srv/paisans/<token>`. `/srv/paisans` itself stays root's, mode 755. An
   existing directory of the user's in another mode or group is given mode
   750 and the user's group again (`chmod 00750`, which clears a setgid bit
   too), since group or other write would let another local user plant
   what the deployment runs; one with an ACL, which `chmod` cannot take
   away, stops admit.
8. **The WireGuard helper and its sudoers line** (below). Admit creates
   `/etc/wireguard` root's, mode 700, when it is absent, and stops when an
   existing one is not root's alone: another's, writable by its group or
   others, a link, not a directory, or carrying an ACL. A conf planted while
   another could write there would survive any repair, and `wg-quick` runs
   its `PostUp` as root, past the helper's checks.
9. **The firewall.** ufw rules tagged `paisans-<token>:` as today: the SSH
   port, the mesh's UDP `ListenPort`, `allow in on psns-<token>`, and 80/tcp,
   443/tcp and 443/udp, for HTTP/3, with `--public-ports`. The `br-+` rules go: a rootless container's traffic
   to the mesh address leaves through the user's own process, not a host
   bridge. ufw must be installed and active: admit adds its rules and never
   enables ufw or sets its defaults, and stops before changing anything on a
   host where ufw is absent or inactive.
10. **Egress.** An nftables table that keeps the deployment off the owner's
    network (below).
11. **The watchdog**, with `--watchdog`: softdog when the host has no
    hardware watchdog, loaded now and at boot by a unit of admit's own,
    ordered before `systemd-logind` starts the lingering users' managers,
    whose Docker starts Patroni; and a udev rule matching the `watchdog`
    and `misc` subsystems that gives `/dev/watchdog` and `/dev/watchdog0`
    to the user, owner and group both, mode 600. The group matters: a
    device whose group is root's cannot be `chown`ed inside the user
    namespace, and the infra compose does that for Patroni (*Observed*). A
    rule in place whose nodes are not yet the user's is applied again. One
    deployment per host can hold it; a second is refused.
12. **Low ports**, with `--public-ports`:
    `net.ipv4.ip_unprivileged_port_start=80` in
    `/etc/sysctl.d/60-paisans.conf`, applied with `sysctl --system`, which
    applies every sysctl file on the host again, then checked, since a file
    read after it could set the key back. The output says plainly that this
    lets every local user on the host bind ports 80 to 1023. Before granting
    it, admit checks, as root, that nothing else answers on 80 or 443: a TCP
    listener in `ss -Hltnp`, a bound UDP socket in `ss -Hlunp`, a port the
    system Docker publishes (`docker ps` on its socket, when one is running),
    a stopped container of that Docker with `restart: always`, which takes
    its ports again when the daemon restarts, or a DNAT or redirect rule for
    either port in `nft list ruleset`, or in `iptables-save` on a host
    without nft. A rule that only matches conntrack's dnat status is not one,
    a negated port holds 80 or 443 unless it names both, and a port operand
    that is anything but ports and ranges, a named set, a map, a
    concatenation or a service's name, which admit cannot read, is taken to
    hold them. A Docker daemon with its userland proxy off publishes a port
    with a DNAT rule alone and no listener, so `ss` cannot see it, and only
    root can read the ruleset. Anything found refuses the grant, naming what
    holds the port (process, container and compose project, or rule, quoted
    without its counters), and the refusal names `web_server.managed_by:
    external` as the way to run behind it; so does anything admit cannot read. Known limits: a DNAT rule with no
    destination port, a forward of every port of an address, is not counted,
    since rules of that shape are mostly a service's backends, reached after
    a port matched elsewhere; and an iptables rule that matches its ports
    through an ipset (`-m set --match-set`) names no `--dport`, so it is not
    counted either.
13. **Forwarding**, for a site with an endpoint: `net.ipv4.ip_forward=1` in
    the same file. Such a site relays mesh traffic between sites behind NAT,
    which cannot dial each other (`isRelay` in `internal/render/site.go`).
    Today that is a `PostUp` in the WireGuard conf, which runs as root; in
    this design no hook is accepted (*The WireGuard helper*). The plan says
    that this turns on routing between every interface on the host, which the
    firewall's forward policy then governs. The values evict restores for
    both keys are recorded in `/etc/paisans/sysctl.before` by a step ahead of
    Docker's install, since a `dockerd` that starts turns forwarding on, so a
    run that stops after one started, and the run after that, still record
    the host's own value; a key the file holds already keeps its value. A key is forgotten
    only after a re-run that no longer needs it has restored it.
14. **The admission record**, `/etc/paisans/admitted.<token>.json`, mode 644:
    the user, the grants (`watchdog`, `public_ports`), whether forwarding is
    on, the ports the firewall opened, the request's digest when there was
    one, the time, and whether the system Docker is one admit installed and
    disabled once (`system_docker_disabled`), which says what admit did, not
    how the daemon stands now. It is how the deployment's own commands, which cannot
    read root's files, learn what they were granted, and what `host
    deployments` lists. Admit keeps `/etc/paisans` root's, mode 755 (`chmod
    00755`, which clears a setgid bit too; an ACL stops admit), so each
    deployment's user reads its own record and none can write one.
15. **The registry claim**, as today, under `/var/lib/paisans`, so two
    deployments on one host cannot collide on a token or a port.
16. **The keys.** Write the `ssh.keys` operators' keys, from the
    configuration or the request, to the user's `~/.ssh/authorized_keys`,
    owned by the user, so the operators can log in, with group and other
    write taken off `~/.ssh`, without which `sshd` ignores the keys. From
    then on the operators maintain them (*Keys*).
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
| `site` | the site's name in the configuration, so the owner sees which site they admit |
| `roles` | the site's roles, sorted; the grants are what they need, and admit refuses grants that disagree |
| `user` | `sites.<s>.ssh.user`, or the default |
| `ssh_port`, `listen_port` | the site's SSH port and WireGuard `ListenPort` |
| `mesh_subnet`, `address` | the mesh subnet and the site's address in it |
| `endpoint` | whether the site has one, so forwarding is planned |
| `grants` | what the roles need: `watchdog` for a data site, `public_ports` for a gateway or a Caddy monitor |
| `web_server` | for a site with `managed_by: external`: each app's hostname and its `listen` address |
| `operators` | each `ssh.keys` operator's name and public keys |

**The digest is the owner's check that the request is the one the operator
sent.** The request carries the keys that will get a shell on the owner's
machine, so anyone who could change it on its way could put their own key
in it. Admit prints the same digest, and the owner compares the two over a
channel they trust, eg a call. `--execute` with `--request` asks the owner
to type the digest at a terminal, so it cannot be scripted past.

The digest is the first 80 bits of the SHA-256 of the request's bytes, in
Crockford's base32: sixteen characters in four groups of four, printed in
lower case, eg `7k2m-q9xd-4r8f-zt1c`. Eighty bits puts a forged request
with the same digest out of reach; at 48 bits a forger who intercepts a
request could vary a free field until a version carrying their own key
matched, in hours on one GPU. Crockford's alphabet leaves out `i`, `l`, `o`
and `u`, so what is read aloud is not misheard. The typed digest is read
without regard to case, hyphens or spaces, with `i` and `l` taken as `1`
and `o` as `0`.

Admit validates a request as strictly as a configuration: every field
present, the site's name and each role accepted by the configuration's own
rules, the user name accepted by the same pattern as `ssh.user`, every
key a public key the toolkit can parse, the address inside the subnet.
The subnet is held to the configuration's rule for `mesh.subnet`: an IPv4
network no larger than a /24, clear of `0.0.0.0/8`, loopback, link-local
and multicast, and inside the private ranges `10.0.0.0/8`, `172.16.0.0/12`
or `192.168.0.0/16`. Rule 3 of the egress table drops those ranges, and
of the rest only ranges a mesh may not use, so on a host two deployments
share, a mesh outside them is one the other deployment's user could send
to through this mesh's interface, as this mesh's address, and reach its
sites. The mesh is IPv4
alone, so IPv6's private range, `fc00::/7`, does not arise.

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
--delete-data` asks for the site's. It deletes a user only when it is the
deployment's: the record's, or, with no record, `paisans-<token>`, and
never root, a system account or a user in a privileged group.

It expires the user and stops its systemd instance, disables linger, then
kills every process left whose real, effective or saved uid is the user's
or in its subordinate range, and checks that none is, before anything that
fences the deployment goes. On a data site the plan warns that a Patroni
that outlasts Docker's stop timeout is killed with the watchdog armed,
which reboots the host. It stops `wg-quick@psns-<token>` and removes
`/etc/wireguard/psns-<token>.conf`, the drop-in that skipped the unit
without it, the user's mesh drop-in, found by the
deployment it names when the owner has deleted the user by hand, and the
egress table, with a `.new` table an interrupted admit left beside its
file, then removes the user's `--data-dir` directory,
`/srv/paisans/<token>`, and the user with its home, killing and checking
again first. Its home is checked as admit checks it: a link or another's
directory stops evict, and a home already gone deletes the user alone.
Then it removes the helper, the sudoers line, the udev rule, the
deployment's ufw rules but its SSH allow, the admission record and the
registry claim. The SSH allow may be the rule the owner's own login
arrives by, so it is kept, and the plan warns of it: the owner deletes it
once nothing else needs it. Host-wide files (`delegate.conf`,
`modules-load.d/paisans.conf`, `60-paisans.conf`) are removed only when
they are as admit wrote them and no other deployment is admitted or
claims the host, `delegate.conf` with its `user@.service.d` once empty
when admit created that, and `ip_unprivileged_port_start` and
`ip_forward` are each restored only when no other admitted deployment
holds `public_ports` or has forwarding on. A deployment the registry claims
with no admission record, an admit in flight or one that failed early,
keeps all of them, as a record evict cannot read does. Evict unloads no
module: `br_netfilter` and the watchdog's module stay loaded until the
next boot, and the plan warns of each as it removes its boot entry.

What admit installed stays when the last deployment goes: the packages
`/etc/paisans/installed` lists (Docker's, the rootless extras, `uidmap`,
`wireguard-tools`, and any other admit installed), Docker's apt key and
source, and the `docker` group `docker-ce` made, with the system daemon
as it is now. Something else on the host may use them by then, so they
are the owner's to remove: the plan names each, from the list, which it
then removes. Only what admit installed and dpkg still shows installed is
named. A hold on Docker's daemons a killed admit left is put back by every
evict.

With no admission record, evict cannot tell where admit put the Docker
data: `--data-dir` names it, and evict removes that directory only when
it is the user's or owned within its subordinate range. Without the flag,
while a subordinate range is left to free, the plan warns that such a
directory is not found. A user deleted by hand can leave an empty
`/etc/systemd/system/user@<uid>.service.requires` behind.

`site remove` does what the deployment's user can, then ends by printing the
`host evict` line for the owner. On a gateway whose Caddy serves only this
deployment's sites, it stops and removes that Caddy; when Caddy also serves
files the owner put in its `caddy.d`, it leaves Caddy running, and the evict
line says evict stops it and deletes those files.

After `paisans gateway move`, the old gateway's 80 and 443 stay open until
its owner re-admits the site without `--public-ports` once DNS has moved:
`gateway move` ends by printing the `host request` line for that site, and
the re-admission closes the ports and restores the low-port setting when no
other admitted deployment holds `public_ports`.

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
| `show peers`, `show latest-handshakes`, `show public-key`, `show allowed-ips`, `show persistent-keepalive`, `show listen-port` | `wg show psns-<token> <field>`: what the conf holds already or how the mesh is doing, never a private key or the address a peer last sent from |
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

`put` also refuses to install unless `/etc/wireguard` is a directory, not a
link, owned by root (uid 0) with no group or other write bit, read with `ls
-ldn` and refused when that cannot be read. It checks each time rather than
trusting step 8 once, because its validation is worth nothing if the user
can write a conf beside it. The refusal names the directory, and `apply`
answers it with "the host's owner runs `paisans host admit` again".

The script is embedded in the CLI beside `internal/hostprep/snippets` and has
its own tests (*Tests*).

## Egress

Every infra container shares the host's network namespace, so without a
rule it can reach the owner's services bound to `127.0.0.1` and the owner's
LAN. Admit loads an nftables table `inet paisans_<token>` from a unit
`paisans-<token>-egress.service`, ordered before `user@<uid>.service` so
no container starts unfiltered at boot, with an output
chain that matches the user's UID and its whole subordinate range (a
container's users map into that range, so the UID alone misses them).
The chain opens with the replies it accepts, each only to a flow someone
else opened (`ct direction reply ct mark and 0x08000000 == 0`), and only
where the deployment can be the server. The host side of each port the
deployment publishes runs as its user, so its answers to the owner's web
server on loopback, to a visitor on the LAN and to the host's own address
carry the user's UID.

* **TCP, on any port.** A TCP reply reaches only its own socket's peer, a
  client that connected to a socket of the user's, which the user can bind
  only at an address and port no owner's listener holds.
* **UDP from port 443, with the public ports grant only**, for HTTP/3. The
  grant is one deployment's per host, and admit refuses it while any other
  process holds TCP or UDP 80 or 443 (*What it does*, step 12), so no
  owner's UDP service on 443 receives these replies. A site behind the
  owner's web server has no such accept: that server may hold UDP 443
  itself, and no holder check runs without the grant. Every other UDP
  reply meets the UID rules: a user sharing an owner's UDP port through
  `SO_REUSEADDR` could otherwise answer the owner's own clients from it.
  The mesh's UDP passes by rule 1.

Next, every flow the user opens gets bit 27 of its ct mark, before any
accept or drop, so a flow the user opened under an accept never carries a
"reply": binding an owner's UDP port as the source, sending to the mesh,
then answering from the mesh is dropped. Bit 27 is clear of Tailscale's
connmark mask `0xff0000`, of the mesh's packet mark `0x5053<token>` and of
kube-proxy's and Docker's low bits. A host rule that copies a packet mark
over the whole ct mark (`CONNMARK --save-mark` with no mask) clears it. A
reply with no conntrack entry, on a port the owner exempts from
conntrack, meets the UID rules and is dropped. The deployment's own flows
take their replies through input. The UID rules follow:

1. Accept the mesh subnet.
2. Accept port 53 to the host's resolvers, read at admit time from
   `/etc/resolv.conf` (`127.0.0.53` under systemd-resolved, or the LAN
   resolver) and from `/run/systemd/resolve/resolv.conf`, the stub's
   upstream servers, which a rootless network's DNS may use instead.
3. Drop `127.0.0.0/8`, `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`,
   `169.254.0.0/16`, `100.64.0.0/10`, `::1`, `fc00::/7` and `fe80::/10`,
   and NAT64's translations of the same IPv4 ranges under the well-known
   prefix `64:ff9b::/96`, with the local-use prefix `64:ff9b:1::/48`
   whole. On a DNS64 host the user would otherwise reach the owner's IPv4
   LAN through the translator. The well-known prefix's public translations
   stay open, since an IPv6-only host reaches the IPv4 internet only
   through them.
4. On a gateway, accept TCP 80 and 443 to the host's own addresses
   (`fib daddr type local`), and the answers its own 80 and 443 send back
   to them, which the second rule marked as the user's. That second accept
   is TCP only: a TCP reply reaches only its own socket's peer, here the
   user's own client. A site is a gateway when its grant holds
   `public_ports`, where the deployment's own Caddy holds those ports, or
   when its `web_server` is external, where the owner's web server fronts
   it and the deployment reaches it by its public name. Either way the
   ports answer the internet already. The accept matches every local
   address past rule 3's drops, so 80 and 443 on a second public address,
   or on a global address of another interface, are open to the
   deployment as well. UDP 443 is not accepted.
5. Drop every address of the host itself, its broadcast and its multicast
   (`fib daddr type { local, broadcast, multicast }`). This keeps the
   deployment off the owner's services bound to `0.0.0.0` or `::`, which
   it would otherwise reach through the host's public addresses and its
   IPv6 global addresses, and off mDNS, SSDP and broadcast UDP on the LAN.
6. Drop the host's on-link IPv6 prefixes. A home LAN's IPv6 is global
   address space, so rule 3 does not cover it.

The order is load-bearing. Rule 4 follows rule 3, since loopback addresses
are `local` too, and precedes rule 6, since the host's own IPv6 address
lies in an on-link prefix. The mesh's own address stays reachable through
rule 1, which the deployment's services need.

**The on-link prefixes** are read at admit time from every routing table,
policy routing's among them: every IPv6 route with no gateway (`ip -6 -o
route show table all`, no `via`), kept when it is global unicast (inside
`2000::/3`) and on any interface but loopback and the mesh's own, and
when it does not overlap the mesh subnet. The system Docker's bridges are
kept: the deployment's own networks live in its user namespace and never
show in the host's table, so a prefix on one is the owner's containers.
Routes are read rather than addresses because a host holding a DHCPv6
`/128`, or an address added without a prefix route, still reaches its
LAN's `/64` through an on-link route. A prefix inside another kept one is
left out, and the rest are sorted. Each is parsed as a masked IPv6 prefix
before it is rendered, and the probe's text never reaches the table. The
plan names the prefixes dropped, and warns of one wider than a `/32`: a
full-tunnel VPN routes such prefixes on its interface, and the table then
drops most of the deployment's IPv6. Running admit again reads them
again: a changed set is a changed table, which admit writes and loads
again in place, and an unchanged set plans nothing. Until that run, a
prefix the ISP has rotated is not dropped.

**Loading the table.** Admit writes the table beside its file, checks it
with `nft -c`, and only then moves it over the file, so a table nft
refuses never becomes the one the next boot loads. The table is loaded in
one transaction. A loaded table that holds other rules than the file, one
flushed or edited by hand, which the probe sees by counting its rules, is
loaded again the same way. The unit requires the mount the file is on.

**Known limits.**

* **Flows opened while the table is absent.** A reload of
  `nftables.service` flushes the ruleset and loads the table again, and a
  flow the user opens in between carries no mark. Its replies stay
  acceptable for as long as the user keeps it alive. With the reply
  accepts limited as above, that matters only for UDP from 443 with the
  public ports grant, and for TCP not at all.
* **A service that is down or bound to one family.** The user may listen
  on any address and port no owner's listener holds, and answer whoever
  connects. An owner's client that means a service which is down, or
  bound to `127.0.0.1` while the client tries `[::1]`, can reach the
  deployment's listener instead. That is ordinary port squatting among a
  host's users, and no table can tell such a client from a visitor.
* **Routes the table does not read.** Only on-link IPv6 routes are read.
  An on-link public IPv4 subnet, and an owner's VPN that routes non-private
  IPv4 ranges, are outside rule 3, so the deployment reaches them.
* **The mesh accept names no interface.** Rule 1 accepts the mesh subnet
  wherever it is routed. Admit refuses a subnet that overlaps a route at
  admit time, in any table; a route added later on another interface
  would let the deployment reach that network through it.

A deployment that needs something on the owner's network asks the owner,
who adds an accept rule. That is deliberately outside the CLI.

**The mesh's own packets need a rule of their own.** WireGuard's UDP is sent
by the kernel, not by the user, so the UID chain never sees it, and the
deployment's user writes each peer's `Endpoint`. The helper refuses literal
loopback, link-local and multicast endpoints, but a name resolves again at
every up, restart and sync, as root, and the host's own addresses and the
LAN are ordinary addresses. Once a handshake is up, moving a peer's
endpoint keeps the session, so the user could send UDP whose payload they
mostly choose to a service on the owner's loopback or LAN. So:

* **The helper marks the socket.** `put` adds `FwMark = <mark>` to every
  conf it installs, the mark baked in when admit renders the helper, one
  per deployment. A conf that sets `FwMark` itself is refused, as today.
  The mark is `0x5053` above the deployment's token, chosen for Docker
  hosts: Kubernetes CNIs use overlapping bits (kube-proxy's `0x4000` and
  `0x8000`, Calico's `0xffff0000` mask), so a host running Kubernetes is
  outside what admit supports.
* **The egress table filters the mark**, in a second output chain matching
  `meta mark <mark>`, after the name was resolved, so nothing can change
  between the check and the send. It drops loopback, link-local and
  multicast, every address of the host itself (`fib daddr type local`),
  the private and shared ranges of rule 3 above, and the host's on-link
  IPv6 prefixes of rule 6. Everything else, the public endpoints a mesh
  normally dials, passes.
* **`--mesh-lan`** lets the deployment's sites dial each other on the
  owner's LAN. With it, admit adds an accept for the private prefix of the
  host's own LAN (the route of its default interface) in the mark chain,
  and nothing wider. The plan says plainly that the deployment can then
  send UDP to that LAN. It is off by default, and the flag is in the
  admission record. Without it, sites on one LAN dial each other's public
  address, which needs a router that loops it back.

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

## Behind the owner's web server

A site with `web_server.managed_by: external` is handed to the owner's web
server, and the owner installs it. The deployment's user cannot write the
owner's configuration, test it or reload the server, and nothing grants it
that: the toolkit renders, the owner installs.

* **Admit detects the server**, as root, with the detection `web-server show`
  runs today: Caddy, nginx or Apache, on the host or in a container, its
  configuration path, and how it obtains certificates. The deployment's user
  cannot repeat it: the system Docker socket, `ss -p`, `nginx -T` and
  `/etc/letsencrypt/live` are root's. Admit writes the result into the
  admission record.
* **Admit warns about Caddy's admin API.** When the detected Caddy answers
  its admin API on a TCP address (`localhost:2019` by default), any local
  user, every admitted deployment's user among them, can replace its
  configuration. Admit says so in its plan, with the fix: `admin
  unix//run/caddy-admin.sock` or `admin off` in the global options block. It
  is a warning, not a refusal: the server is the owner's.
* **`include_dir` is refused** by `validate`. Nothing is written into the
  owner's configuration and nothing of theirs is reloaded.
* **`apply`** runs the app on `listen`, writes the snippet, filled in from
  the admission record's detection, to
  `/srv/paisans/<token>/web-server/<app>.<ext>` for reference, and runs
  `web-server check` from outside. Until the check passes, the web server
  step ends on a warning naming `paisans web-server show --app <app>` for
  the owner; when the reference file changes, the warning says the owner's
  copy needs replacing.
* **`web-server show`** prints the sheet from the admission record, so it
  fills in the right server's snippet as the deployment's user.
* **`app remove` and `site remove`** say which block the owner removes.

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
* **The gateway answers as itself.** The deployment's user cannot read the
  host's ruleset or another user's sockets, so a port taken after
  admission, and above all one taken by a DNAT rule with no listener, is
  invisible from the host. The gateway's Caddy serves
  `/.well-known/paisans/<token>`, answering with the deployment's token,
  and `apply` ends the gateway's step by fetching it through the
  community's public address on 443 (and on 80, expecting the redirect).
  Any other answer is a conflict: the step fails with "443 on <site>
  answers, but not as this deployment", whatever the host check saw.
* **`paisans doctor`** loses the sudo finding and the allowlist check, and
  gains: the user is not privileged, the rootless socket answers, Engine is
  29.5 or later, linger is on, `sudo -n -l` lists the helper and nothing
  else, the admission record matches the declaration, and the gateway and
  every monitor running the toolkit's Caddy answer their own
  `/.well-known/paisans/<token>` from outside, as `apply` checks the
  gateway.
* **`host deployments`** stays an owner's command with `--ssh` and sudo, and
  lists admission records beside what it lists today.

## A deployment with sites missing

Operators admit sites one host at a time, and a deployment that works on
fewer sites should not wait for all of them. Each run classifies every
declared site, once, at its start, and every step of the run uses that
classification.

**Whether a site has joined is read from the deployment, never from the
site's own host.** A host that has evicted the deployment, one that is down
and one that never admitted it look alike from outside: the login is
refused or nothing answers. So the run reads the gateway's deployed record,
which lists the sites deployed, and etcd's and Patroni's membership from any
member that answers.

| The site's host | Joined | The site is | Its place in the topology | The run |
|---|---|---|---|---|
| answers, and its admission record covers its roles | either | ready | as declared | normal |
| refuses the login, does not answer, or is admitted without a grant its roles need | no | **pending** | left out: no mesh peer, no etcd or Patroni member, no Garage node, no DNS record, and its apps held back | goes on, degraded |
| refuses the login or does not answer | yes | **down** | kept as declared: still a peer and a member, its records kept | goes on, degraded, without that site's steps |

A down site is never removed by a run. The cluster keeps its member, as etcd
and Patroni keep any member that is down, and only `site remove` takes it
out. Its line cannot say whether the host is down or its owner evicted the
deployment, so it says both: `home-b: its login is refused or it does not
answer: the host is down, or its owner evicted this deployment. paisans site
remove --site home-b --host-gone removes it from the deployment.`

**Pending sites are left out as if not yet added.** The run plans the
**effective topology**, the declaration less its pending sites, and renders
every ready site from it. A pending data site joins through the path `site
add` takes once its host admits it; the next `apply` finds it ready, with no
other command. A site whose only route into the mesh is a pending site's
`endpoint` is pending too, with that reason. Garage with fewer ready nodes
than its replication holds back storage, and the apps that need it, with a
warning.

**What still refuses the run:** the gateway pending or down; no ready data
site while an app needs a database; etcd or Patroni without a quorum among
the members that answer; and `--site` naming a site that is pending or down.

**What it says.** The plan opens with a `pending` section, a line per
pending or down site with its reason and, for a pending one, its `paisans
host request --site <s> > <s>.admit` line, then what the deployment is
without (the wording of doctor's Reach row). The run ends `Applied,
degraded: <n> sites pending or down (<names>)`, exit 0. `--require-all`
makes a degraded run exit 1, for automation that must not pass on one.
`doctor` reports each pending or down site as a `WARN` with the same list,
and the gateway as a `FAIL`, since the run refuses without it.

**The declaration stays the only record.** The configuration store keeps
`paisans-stack.yaml` as written; the effective topology is derived on every
run and never published, so admitting a site creates no version. Each
site's manifest records which sites were pending when its files were
rendered, and the checks that compare a host with a render (an edited file,
doctor's Leftovers) use that same view, so a file rendered without a pending
site is not drift. Every run, `--site` included, classifies every site, so
one site's files never disagree with another's about who is in the mesh.

A rekey with sites down is in `docs/specs/2026-10-15-mesh-rekey-sites-down.md`.

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
* The egress table renders the user's UID and subordinate range, the
  gateway's accept only for a gateway, the UDP 443 reply accept only with
  the public ports grant, NAT64's private translations, and the on-link
  prefixes in both chains; a prefix that is not a masked IPv6 prefix is
  refused. A re-run with the same on-link prefixes plans nothing, one with
  changed prefixes writes and reloads the table, and one whose loaded
  table holds other rules reloads it.
* `host request`: a golden request per role mix; the digest is stable across
  runs and changes with any field. Admit refuses a request with a missing
  field, an unknown version, a bad key or a mistyped digest.
* Admit plans the same steps from `--request` as from `--site` for the same
  site.
* The local transport is constructed by no command other than admit and
  evict with `--request`.
* Classification: a site whose login is refused and which the gateway's
  record and the cluster do not list is pending; the same site listed there
  is down; a timeout is treated the same way in each case.
* The effective topology: golden renders of a three-site deployment with
  one data site pending (no peer, no member, no records for it), and the
  same with it down (rendered as declared).
* Refusals: the gateway pending, the gateway down, no ready data site, and
  `--site` naming a pending site.
* `--require-all` exits 1 on a degraded run; without it, 0.
* The release workflow's artifacts include both Linux binaries and
  `SHA256SUMS`.
* The Docker-tagged tests run against a rootless daemon.
* Admit with `--public-ports`: golden plans refusing a host where 80 is a
  process's listener, a system Docker container's published port, a
  stopped one with `restart: always`, and a DNAT rule with no listener,
  each naming what holds it.
* The gateway's step fails, naming the conflict, when its fetch of
  `/.well-known/paisans/<token>` returns another body or another server.
* Admit's `br_netfilter` plans: already loaded, not loaded with only
  Docker's bridges, and not loaded with a libvirt bridge (stopped without
  `--bridge-netfilter`, loaded with it).
* A first `--execute` on a blank host whose package step creates
  `/etc/wireguard` runs to the end: a step the rebuilt plan no longer has is
  done, while one added, moved or changed stops it.
* The Docker hold, run for real against a directory of the test's: the
  added packages' units forbidden, every other asked of the owner's
  `policy-rc.d`, the owner's back after, or none when there was none; a
  hold a killed run left, the owner's copy beside nothing or beside the
  same file, or cut short, put right; a copy that differs refused; the hold
  and putting it back refused while another run holds the lock; a copy
  that fails, under dash, stopping the hold with the owner's policy
  untouched; a failed install disabling what it adds.
* The install run for real with an apt that fails after dpkg shows
  `docker-ce` installed, or half-configured: the claim reads `package
  docker-ce` after, with and without a hold.
* A system Docker that does not answer: admit's held one read as having no
  networks, the owner's refused, readably, saying to start it.
* One end-to-end run on staging: admit a fresh host as its owner, apply as
  the deployment's user, then check that a container cannot connect to a
  service on the host's `127.0.0.1` or LAN, and that `sudo -n` as the user
  runs nothing but the helper. Then, on a second host, a system Docker with
  `"userland-proxy": false` publishing 443: admit `--public-ports` refuses,
  and with the container started after admission, `apply` and `doctor`
  report the conflict.

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

## Observed

On a blank Ubuntu 24.04.1 VPS (kernel 6.8, amd64), 2026-10-15, with a second
host as the mesh peer. Docker Engine 29.9.1 from Docker's repository,
RootlessKit 3.3.0. The spike plan is
`docs/superpowers/plans/2026-10-15-rootless-research-spike.md`; R-numbers are
its.

* **R1, host networking.** A rootless container with `network_mode: host`
  bound the WireGuard address in the host's namespace (`ss` shows the
  user's UID), and the peer read it over the mesh. Its socket survived a
  `wg-quick` restart. Caddy bound `*:80` once `ip_unprivileged_port_start`
  was 80; at 1024 it failed with `bind: permission denied`.
* **R2, beside a system daemon.** The setup tool installed with and without
  `--force`. The system daemon, its socket and its container were
  untouched. The storage driver reports `overlayfs`, the containerd image
  store, with the data directory under the user's home.
* **R3, egress.** With the table loaded, host-networked containers as
  container root and as UID 5432, and bridge containers, could not reach
  the host's `127.0.0.1` or `169.254.169.254`; the mesh, DNS through
  `127.0.0.53`, the internet and image pulls worked. Root on the host
  still reached its own service.
* **R4, the watchdog.** With owner and group both the user's, a container
  `chown`ed the device to its `postgres` user, which opened it and
  disarmed it. With the group left as root's, `chown` failed with
  `Operation not permitted`. Left armed, it rebooted the host in about 85
  seconds.
* **R5, delegation.** Ubuntu 24.04 delegated `cpu memory pids` by default
  and `--cpus 0.5 --memory 64m` worked; the drop-in added `cpuset io`.
  Restarting `user@` restarted the user's daemon: a container with a
  restart policy came back, one without did not.
* **R6, source address.** The daemon would not start with
  `"userland-proxy": false` until `br_netfilter` was loaded (`stat
  /proc/sys/net/bridge/bridge-nf-call-iptables: no such file or
  directory`). With it, a published port logged the peer's real address;
  with the default proxy, `172.17.0.1`.
* **R7, sockets.** `ss -Hltnu` as the user listed root's loopback listener
  and sshd, without process names.
* **R8, volumes.** `du` on a volume was denied below a directory owned by
  a container user; `docker system df -v` reported its size.
* **R9, the helper.** `sudo -n -l` listed only the helper; it ran with no
  password and no terminal, a bad verb exited 64, `sudo -n true` was
  refused.
* **R10, boot.** After a reboot: `wg-quick` up at 14.7 s, the user's
  manager at 17.0 s, its daemon at 25.4 s; the mesh-bound container was up
  with no restarts, as were the system daemon's container and a second
  user's daemon. Nothing orders them, so the reverse was forced: with the
  interface down the container restarted on `Errno 99 Address not
  available` and was serving 6 seconds after the interface came up. Every
  infra service has `restart: unless-stopped`, which is what makes boot
  order safe.
* **R14, R15, the users.** `paisans-0a0b` was accepted without
  `--badname`; `useradd` gave each of two users its own 65,536 subordinate
  IDs, without overlap, and each daemon listed only its own containers.
* **R18, R20.** The restriction on unprivileged user namespaces was on and
  the deb's `rootlesskit` profile was loaded. Engine 29.9.1.
