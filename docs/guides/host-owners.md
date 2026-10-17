# Admitting a deployment to your machine

This guide is for the owner of a machine, a homelab box or a VPS, that a
community has asked to run part of its Paisans deployment. You do not need
the community's configuration, and you do not run its services. You run one
command as root, once, to admit the deployment, and again whenever you change
what you grant it. Everything the community's operators do after that runs as
a user of its own on your machine, with its own rootless Docker, fenced off
your network.

The reasoning is in `README.md`, under *A host's owner admits a site from a
request* and *`site prepare` keeps what is the deployment's user's*; every
flag is under *`paisans site admit`* and *`paisans site evict`* in
[docs/commands.md](../commands.md).

**Read *4. ufw* and *If SSH stops answering after admit enables ufw* before
you run anything with `--execute`.** Enabling a firewall on a machine you
reach only over SSH is the one step here that can lock you out.

## What you need

* **Ubuntu 22.04 or 24.04, amd64 or arm64.** Admit refuses any other
  release, naming the ones it supports; the binary exists for amd64 and
  arm64 only. If your machine will hold the deployment's database (a data
  site), it must have the same architecture as the deployment's other data
  sites, which the operators' `site add` checks; the Ubuntu release may
  differ.
* **ufw installed.** Admit adds the deployment's rules to ufw and never
  installs it. If ufw is installed and not active, see step 4.
* **Docker Engine 29.5 or later from Docker's repository, or no Docker.**
  With none, admit installs Docker's packages from Docker's repository and
  keeps their system daemon disabled, so only the deployment's own daemon
  runs. It never upgrades or removes a Docker you already run: an older
  Engine, a snap, or a Docker that is not Docker's `docker-ce` and has no
  `dockerd-rootless-setuptool.sh` (Ubuntu's `docker.io`, for one) is refused,
  with the reason. Your own Docker keeps running beside the deployment's,
  untouched.
* **User namespaces that rootless Docker may use.** Ubuntu 24.04 restricts
  unprivileged user namespaces and ships the AppArmor profile,
  `/etc/apparmor.d/rootlesskit`, that lets rootless Docker past the
  restriction; admit checks that it is loaded. Ubuntu 22.04 has neither the
  restriction nor the profile, unless a newer kernel brought the restriction
  and you turned it on. Where the restriction is on and no profile is
  loaded, admit refuses, saying how to load the profile, or, where your
  release has none, what lifting the restriction for the whole machine
  costs, and leaves that choice to you.
* **No Kubernetes.** The deployment marks its mesh traffic for the firewall
  with bits Kubernetes networking also uses. Admit does not check for it: a
  machine running Kubernetes is outside what it supports.
* **The request file the operators sent you, and its digest**, which they
  tell you over a channel you trust (step 2).

## 1. Install paisans and check it

Each release attaches two static Linux binaries, `paisans-linux-amd64` and
`paisans-linux-arm64`, and a `SHA256SUMS` file listing their checksums. The
binary needs nothing else: not Go, not Docker, not Homebrew.

```sh
v=0.3.0                                # the release your operators name
arch=$(dpkg --print-architecture)      # amd64 or arm64
echo "$arch"
base="https://github.com/Paisans-Software/paisans-cli/releases/download/v$v"
curl -fLO "$base/paisans-linux-$arch" &&
  curl -fLO "$base/SHA256SUMS" &&
  sha256sum --check --ignore-missing SHA256SUMS &&
  sudo install -m 755 "paisans-linux-$arch" /usr/local/bin/paisans &&
  paisans version
```

`dpkg --print-architecture` names your machine's architecture the way the
release names its binaries, so the block downloads the one for your machine;
`echo` shows which. Each step after it runs only when the one before
succeeded, so nothing is installed unless the download is whole and
`sha256sum` prints `paisans-linux-<arch>: OK`. Anything else, delete the
file and download it again. The check proves the file arrived whole and is
the one the release lists. Both files come from the same release page, so it proves
nothing about who published the release.

Use the release the operators name. A paisans that reads another request
format refuses the request, naming the version it found and the one it
reads, rather than read it in part.

## 2. Read the request and compare its digest

```sh
sudo paisans site admit --request home-a.admit
```

Without `--execute` it changes nothing. It shows the site and its roles, the
user it will make (`paisans-<token>`, unless the operators named another),
the grants the site's roles need, every step it would take, and the
request's **digest**: sixteen characters in four groups, for example
`7k2m-q9xd-4r8f-zt1c`.

The request carries the keys that will get a shell on your machine, so
anyone who could change it on its way could put their own key in it. The
digest is how you know it is the file the operators wrote: **ask an operator
for the digest over a different channel than the one that brought the file**,
a call or a message you know is theirs, and compare. A digest that travelled
beside the file in the same email proves nothing, since whoever changed the
file could change the digest too. When you admit with `--execute`, paisans
asks you to type the digest at the terminal, so the comparison cannot be
skipped; from anything but a terminal it refuses to run with `--execute`
unless you give the digest as `--digest <digest>`, which it compares the
same way. `--yes` never answers it.

A request is read as strictly as a configuration: an unknown version, a field
missing or unknown, a user that is `root` or another deployment's, a key that
is not a plain public key, or any byte the operators' `site request` would
not have written, refuses it before anything else.

## 3. Decide what to grant

Admit gives nothing you did not grant. Two grants follow from the site's
roles: `--watchdog` for a data site, and `--public-ports` for a gateway or a
monitor that runs Paisans' own Caddy. The plan lists the ones the site
needs. At a terminal, admit asks for each one your flags leave out, saying
what it gives; elsewhere it stops. Either one given to a site that does not
need it is refused, since each weakens your machine for nothing. The other
three are yours to decide by your machine, not by the site.

| Flag | What it lets the deployment do | What it costs you |
|------|--------------------------------|-------------------|
| `--watchdog` | Patroni, the database, uses the machine's watchdog to fence itself: if it hangs while it leads, the machine reboots before another site's promotion leaves two primaries taking writes. A data site needs it. | **It can reboot your machine.** The device is given to the deployment's user. One deployment per machine can hold it; a second is refused, and so is a machine whose systemd (`RuntimeWatchdogSec`) or a watchdog daemon holds the watchdog itself. Where there is no hardware watchdog, admit loads `softdog`. |
| `--public-ports` | The deployment binds ports 80 and 443, which a gateway or a monitor running Paisans' own Caddy serves on. | **Every local user can then bind ports 80 to 1023**: admit lowers `net.ipv4.ip_unprivileged_port_start` to 80 for the whole machine, and opens 80/tcp, 443/tcp and 443/udp in ufw. Before granting it, admit checks that nothing else answers on 80 or 443 (a listener, a container of your own Docker, or a forwarding rule), and refuses naming what does. |
| `--data-dir <path>` | The deployment's Docker data (images, containers, volumes: the database on a data site, the stored objects on a storage site) goes under `<path>/paisans-<token>` instead of the user's home. | Nothing, if the disk has room. `<path>` must exist, be a directory and not a link, and be root's with no group or other write bit. With or without the flag, admit refuses to put the data on a filesystem Docker's storage cannot use (NFS, CIFS and SMB, FUSE, overlay, FAT, exFAT and NTFS), and the plan states the free space where it goes, warning below 20 GB for a data or storage site and 5 GB otherwise. |
| `--bridge-netfilter` | Admit loads `br_netfilter`, which the deployment's Docker needs, on a machine with bridges of its own (libvirt's, LXD's, yours). | **Traffic across your own bridges then passes the firewall's FORWARD chain**, which it skipped before, and whose policy is often to drop. The refusal names each bridge and quotes the policy. Without the flag admit stops on such a machine; where the module is loaded already, or the only bridges are Docker's, it needs no flag. |
| `--mesh-lan` | The deployment's sites may reach each other on your LAN, at the private prefix of your machine's default interface. | The deployment can send its mesh traffic to that LAN, and the plan says so. Admit refuses it where that interface is not on a private network of a /16 or narrower. Without it, sites on one LAN reach each other through their public addresses, which needs a router that loops them back. |

**Forwarding has no flag.** A site with an endpoint relays mesh traffic
between sites that cannot dial each other, so admit turns on
`net.ipv4.ip_forward` for it and says so in the plan: routing between every
interface on your machine, which your firewall's forward policy then governs.

## 4. ufw

When ufw is active, admit adds the deployment's rules and changes nothing
else: it never sets ufw's default policy, and never removes a rule of yours.

When ufw is installed and not active, admit can turn it on, with
`--enable-ufw` or a yes at the terminal, where it asks before showing the
plan. In order, it:

1. adds the deployment's rules;
2. adds an allow for every TCP port sshd listens on, tagged `paisans: ssh`,
   unless an allow of yours already covers it;
3. arms a timer, `paisans-ufw-fallback`, that runs `ufw disable` 10 minutes
   later;
4. enables ufw.

Before any of that, admit checks what enabling ufw would block. It reads
every listener on an address other than loopback, TCP and UDP, IPv4 and
IPv6, and every port your Docker publishes or a NAT rule receives. A port
stays reachable when it is sshd's, the deployment's own, or one a rule of
yours allows; every other port ufw would block. If there is one, admit
refuses and changes nothing, listing each port with what serves it, and the
lines that keep each reachable:

```text
ufw is installed but not active, and enabling it would block these, which answer from off this host now:
  80/tcp: caddy (pid 900, uid 0) listens on *:80
  443/tcp: caddy (pid 900, uid 0) listens on *:443
For each port whose service must stay reachable, run its line before ufw is enabled, then run admit again:
  sudo ufw allow 80/tcp
  sudo ufw allow 443/tcp
```

Run the lines for the services that must stay reachable, such as your own
web server, then run admit again: ufw keeps a rule added while it is
inactive. A service meant to stay unreachable from outside, such as a
database listening on a public address, needs nothing. Once you have checked
every port listed, `--enable-ufw-block-listed` with `--enable-ufw` enables
ufw over them, and the plan names each one it blocks; at a terminal, a
second question asks the same, and `--yes` never answers it. The plan's
`enable ufw` step names what stays reachable, and why.

A port that reaches a container or a virtual machine through NAT is routed
traffic, which ufw decides by its route rules, after translation, and never
by `ufw allow`. Docker on its default iptables backend puts its own rules
ahead of ufw's, so its published ports stay reachable and admit says so.
For any other, admit prints `sudo ufw route allow proto <proto> to
<address> port <port>`, the address and port the traffic is translated to;
check it from outside once ufw is on.

ufw applies every rule as it is enabled, so SSH is allowed from its first
moment. Admit then prints the time the timer fires and asks you to open a
**new** SSH session to the machine and type `yes` once it logs in. That
stops the timer, and ufw stays on. Anything else leaves the timer to turn
ufw off at the time printed; the deployment is admitted either way.

Admit reads the ports from the sockets sshd and systemd listen on, not from
`sshd_config`, so it allows what a new login connects to now. It allows
only those TCP ports, and cannot see DROP rules written by hand in
`/etc/ufw/before.rules`, `before6.rules` or `user.rules`, nor a way in that
is not listening as it reads the machine, such as sslh, dropbear or mosh
started later, nor a firewall at your provider. If you rely on any of them,
enable ufw yourself after checking them.

Admit refuses to enable ufw, with or without the flag and without asking,
and changes nothing, when:

* it cannot read the ports sshd listens on, ufw's own rules, or what else
  answers from outside (the UDP sockets, your Docker's containers or the NAT
  rules);
* it finds no port sshd listens on;
* ufw holds a deny or reject rule of yours, in any form, which it lists:
  ufw applies the first rule a connection matches, and admit neither reads
  every form a rule takes nor overrides yours. Check that none blocks SSH,
  then enable ufw yourself with the commands it prints;
* sshd listens on IPv6 and `IPV6`, as ufw reads it from `/etc/default/ufw`
  and then `/etc/ufw/ufw.conf`, is not `yes`, since ufw would then drop SSH
  over IPv6. Set `IPV6=yes` and run admit again;
* `systemd-run` is missing, so the timer cannot be armed;
* the timer is already loaded, most likely from an earlier admit that did
  not finish. The refusal gives the commands that clear it.

Without `--enable-ufw` and off a terminal, admit refuses before changing
anything and prints the commands that enable ufw by hand, numbered, each
`sudo ufw allow <port>/tcp` before `sudo ufw enable`, after the ports
enabling ufw would block, if any, with their allow lines.

**Never run `sudo ufw enable` by hand without the allow lines first.**
Ubuntu's ufw denies incoming connections by default, so enabling it with no
allow for your SSH port cuts off SSH at once, your own session's next
connection included, and nothing turns it off again. The same holds for
every other service of yours that answers from outside: allow it first.

## 5. Admit

```sh
sudo paisans site admit --request home-a.admit --watchdog --execute
```

Pass the flags you decided on. Paisans shows the plan, asks for the digest,
then makes each change the plan showed, and checks that the new user's
Docker answers. Every step can be run again: a re-run after a failure
resumes, and one with other flags changes only what they govern. The
operators' next command is the first to log in as the deployment's user.

An owner who is also one of the deployment's operators can admit from their
workstation instead, with the configuration and their own login to the
machine: `paisans site admit --site home-a --ssh you --execute`. The steps
are the same. There, admit ends by logging in as the deployment's user, a
new SSH connection, and stops the ufw timer itself when that login gets
through.

## If SSH stops answering after admit enables ufw

**Do not reboot.** The timer lives only until the next boot: a reboot before
it fires leaves ufw enabled, and you locked out.

* **If you still have a session open**, find why the new one failed, then
  either allow it (`sudo ufw allow <port>/tcp`) and stop the timer with `sudo
  systemctl stop paisans-ufw-fallback.timer`, or let the timer turn ufw off.
* **If you have no session**, wait. At the time admit printed, at most 10
  minutes after it armed the timer, the timer runs `ufw disable`, and SSH
  answers again. Then find what blocked SSH (a port sshd listens on that
  admit did not see, sslh, a rule in `before.rules`, a firewall at your
  provider), and run admit again with `--enable-ufw` once it is fixed, or
  enable ufw yourself.
* **If the machine rebooted before the timer fired**, ufw is on and nothing
  will turn it off. Use the console your provider or the machine itself gives
  you, outside SSH, and run `sudo ufw disable`, or `sudo ufw allow
  <port>/tcp` for the port SSH listens on.

## What admitting changes on your machine

* **Docker**, where there was none: Docker Engine from Docker's repository,
  with its rootless extras, and the tools the deployment needs (`uidmap`,
  `nftables`, `wireguard-tools`, `git`), with Docker's system daemons kept
  from starting while they install and disabled after. Where Docker is
  there, only what is missing, and nothing is upgraded. What admit
  installed is listed in `/etc/paisans/installed`.
* **The user**, `paisans-<token>`, with a home, subordinate IDs of its own,
  and linger, so its Docker runs with nobody logged in and starts at boot. A
  user the request names that already exists is warned of when it is not
  `paisans-<token>`, and refused when it is root, a system account, or in
  `sudo`, `wheel`, `admin`, `adm`, `docker`, `lxd`, `systemd-journal`,
  `disk`, `shadow`, `kmem`, `libvirt` or `incus-admin`: each reaches past
  the user.
* **cgroup delegation**, `/etc/systemd/system/user@.service.d/delegate.conf`,
  a drop-in for every user's systemd manager that delegates cpu, cpuset,
  io, memory and pids, so the deployment's containers can be limited. Ubuntu
  24.04 delegates cpu, memory and pids without it, 22.04 only memory and
  pids. It is shared by every deployment admitted here. A manager already
  running takes it at its next start, which the plan warns of, since admit
  never restarts it.
* **The deployment's own Docker**, running as its user, and `br_netfilter`,
  loaded and listed in `/etc/modules-load.d/paisans.conf`.
* **`/srv/paisans/<token>`**, the user's.
* **The mesh.** `/etc/wireguard`, root's, mode 700; a script,
  `/usr/local/libexec/paisans/wg-psns-<token>`, that installs and runs the
  deployment's WireGuard interface and checks every file it is given; one
  line in `/etc/sudoers.d/paisans-<token>` that lets the user run that script
  as root, and nothing else; and drop-ins that start the user's manager after
  the mesh at boot.
* **ufw rules**, each commented `paisans-<token>: <why>`: the SSH port, the
  mesh's UDP port, traffic on the mesh interface, and with `--public-ports`
  80/tcp, 443/tcp and 443/udp.
* **Egress**: an nftables table, `inet paisans_<token>`, in
  `/etc/paisans/egress.<token>.nft`, loaded at boot by
  `paisans-<token>-egress.service` before the user's manager starts, which
  requires it (see *What the deployment can reach*).
* **With `--watchdog`**, a udev rule giving the watchdog device to the user,
  and, where there is no hardware watchdog, `softdog`, loaded now and at
  every boot by `paisans-<token>-watchdog.service`.
* **With `--public-ports`, or forwarding**, `/etc/sysctl.d/60-paisans.conf`,
  and the values it replaced recorded in `/etc/paisans/sysctl.before`.
* **The admission record**, `/etc/paisans/admitted.<token>.json`, root's and
  readable by all: the user, what you granted, the ports opened, the digest.
  It is how the deployment's own commands learn what they were given.
* **The registry**, `/var/lib/paisans/registry.json`, so a second deployment
  on this machine cannot take this one's names, ports or mesh subnet.
* **The operators' keys**, in the user's `~/.ssh/authorized_keys`. From then
  on the operators maintain them themselves, as that user.

To see it afterwards, with your deployment's token in place of `1a2b`:

```sh
sudo -l -U paisans-1a2b                       # the helper, and nothing else
sudo nft list table inet paisans_1a2b
sudo ufw status | grep paisans-1a2b
cat /etc/paisans/admitted.1a2b.json
ls /etc/paisans/                              # the records, admitted.<token>.json, beside admit's own files
```

**Anyone the operators trust gets a shell as that user.** Adding or removing a
co-operator is theirs to do without you. What protects you is that the user
is all that shell reaches.

## What the deployment can reach

The egress table keeps the deployment's processes, its containers included,
off your machine and your network. It lets through the mesh and DNS to your
resolvers, and drops loopback, the private, shared and link-local ranges,
your machine's own addresses, broadcast and multicast, and your on-link IPv6
prefixes, which admit reads when it runs: if your LAN's IPv6 prefix changes,
run admit again. On a gateway, or a site behind your web server, it allows
TCP 80 and 443 on your machine's own addresses. The mesh's own WireGuard
packets, which the kernel sends, are filtered the same way; with
`--mesh-lan` they may reach your LAN.

What it cannot do, and what you do about it:

* **Bind your admin services to `127.0.0.1` (or `::1`) or to your LAN
  address.** A service of yours bound to every interface (`0.0.0.0` or `::`) is
  also bound to the machine's mesh address, which the deployment must reach and
  so is not dropped: the deployment, and its other sites over the mesh, can
  reach it there.
* **The deployment's user can listen on any port no service of yours
  holds.** A client of yours that means a service which is down, or bound to
  the other address family (`127.0.0.1` while the client tries `[::1]`), can
  reach the deployment's listener instead.
* **Routes the table does not read.** An on-link public IPv4 subnet, or a
  VPN of yours that routes non-private IPv4 ranges, is reachable.

**Paisans has no supported way to open the egress table to one of your
services**, and none that lasts. An accept in a table of your own does not
override the deployment's drop. A rule added to `inet paisans_<token>`
itself is removed whenever the table is loaded again from its file: at boot,
on every reload or restart of nftables, and by any admit whose plan shows
*reload the egress table*, which an added rule alone makes it do. Never
weaken the table, or stop its unit, to make room for one service: it is what
keeps the deployment off your whole machine and network.

If the deployment needs something of yours, its operators ask you, and what
works is one of these:

* **Serve it on the mesh address.** The table accepts the mesh subnet, so a
  service of yours bound to this machine's mesh address (`ip -4 addr show
  psns-<token>`) is reachable by the deployment, and by every one of its
  sites and its operators over the mesh, and by nothing else outside your
  machine. That address exists only while the mesh is up, so the service
  starts after `wg-quick@psns-<token>.service`.
* **Serve it at a public address on another machine.** The table drops only
  private, shared and link-local ranges, this machine's own addresses and
  your on-link IPv6 prefixes, so a public address elsewhere is reachable.

`--mesh-lan` is not one of them: it lets the mesh's own WireGuard packets
reach your LAN, so the deployment's sites can dial each other there, and
lets nothing else of the deployment's reach it.

## Your own sites on a gateway

When your machine is the deployment's gateway (`--public-ports`), the
deployment's Caddy holds 80 and 443, so a site of your own on this machine,
a blog for one, goes through that Caddy too. Its site block goes in
`/srv/paisans/<token>/caddy.d`, a file per site ending `.caddy`, written as
the deployment's user and never as root, since the user owns the
directory: `sudo -u paisans-<token> tee
/srv/paisans/<token>/caddy.d/blog.caddy`. Paisans never writes, changes or
removes a file there, but **evict deletes the directory with the rest of
`/srv/paisans/<token>`** (see *Evicting*). The README has the details,
under *The gateway host's own sites live in the deployment's `caddy.d`*.

## Behind your own web server

When your machine already serves 80 and 443 with its own web server, the
deployment's monitor can run behind it: the operators declare the site with
`web_server.managed_by: external`, and you admit it without
`--public-ports`. Admit detects your web server (Caddy, nginx or Apache, on
the host or in a container), and records how it is configured and how it
obtains certificates, which the deployment's user could not read for itself.
The deployment never edits your configuration: the operators run `paisans
web-server show --app <app>` and send you what it prints, you add it to your
server and reload it, and their `paisans web-server check` checks it from
outside. See [behind-your-own-web-server.md](behind-your-own-web-server.md).

## If you run Caddy

Caddy answers an admin API on `localhost:2019` unless told otherwise, and any
local user can replace its whole configuration through it, every admitted
deployment's user among them. When admit, detecting your web server, finds
your Caddy's admin API on TCP, its plan warns. The fix is yours, in the
global options block of your Caddyfile:

```caddyfile
{
	admin unix//run/caddy-admin.sock
}
```

or `admin off`. A Caddy configured in JSON takes `"admin": {"listen":
"unix//run/caddy-admin.sock"}` or `"admin": {"disabled": true}`. Then
**restart** Caddy: a reload sends the new configuration to the new admin
address, which is not listening yet. Admit only warns, since the server is
yours.

## Changing what you granted

Run admit again with the request for the site as it is now and the flags you
grant now. It changes only what the flags govern. To add a grant the site
did not need before, ask the operators for a fresh request: a grant the
request does not need is refused.

A change of the site's SSH port, WireGuard port, user, endpoint or mesh
subnet also needs you, since each is a root change: the operators' commands
refuse with the `site request` and `site admit` lines to run. When the
operators move their gateway off your machine, they send a fresh request
once DNS has moved; admitting it without `--public-ports` closes 80 and 443
and restores the low-port setting, unless another deployment here holds it.

## Evicting

```sh
sudo paisans site evict --request home-a.admit --execute
sudo paisans site evict --deployment f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01 --user paisans-f2a9 --execute
```

The second is for when the request is lost. Without `--execute`,
evict shows its plan and changes nothing. With it, paisans asks you to type
the user's name, then stops the deployment's processes and its mesh, and
removes the user with its home and Docker data, `/srv/paisans/<token>`, the
helper and its sudoers line, the watchdog rule, the egress table and its
unit, the deployment's ufw rules but one, the record and the registry entry.
It deletes a user only when it is the deployment's: the record's, or, with
no record, `paisans-<token>`. With no record, pass `--data-dir` if you gave
admit one, so evict finds the Docker data. On a data site the plan warns that
a Patroni that outlasts Docker's stop timeout is killed with the watchdog
armed, which reboots the machine: have the operators stop the deployment
first, or expect a reboot.

**On a gateway, copy your own site blocks out of
`/srv/paisans/<token>/caddy.d` before you evict.** Evict stops the
deployment's Caddy, which serves them, with the rest of the deployment, and
deletes `/srv/paisans/<token>` with everything in it, those files included;
its plan does not warn of them. The operators' `site remove` keeps that
Caddy serving them and says so, but evict does not.

The operators' `paisans site remove` does what the deployment's user can, and
ends by printing the `site evict` line for you.

## What stays after evicting

* **The deployment's SSH allow**, with a warning: it may be the rule your own
  login arrives by. Delete it yourself once nothing else needs it.
* **ufw, enabled**, if admit enabled it, with its `paisans: ssh` allows.
  Disabling it is yours (`sudo ufw disable`).
* **Files and settings other deployments share**: the cgroup drop-in,
  `modules-load.d/paisans.conf` and `60-paisans.conf` stay while another
  deployment is admitted, and each sysctl is restored to the value it had
  before admit only once no admitted deployment needs it.
* **`br_netfilter` and the watchdog's module**, loaded until the next boot.
* **What admit installed**, even when the last deployment goes: the packages
  `/etc/paisans/installed` listed (Docker's, its rootless extras, `uidmap`,
  `wireguard-tools`, and any other admit installed), Docker's apt key and
  source, the `docker` group `docker-ce` made, and Docker's system daemon,
  disabled as admit left it. Something else may use them by then, so
  removing them is yours; the plan names each.
* **`/etc/paisans/installed`**, the list of what admit installed, root's and
  mode 644, with `/etc/paisans` holding it, while the list names anything
  still installed. A later admit on this host, of any deployment, reads it,
  knows Docker's system daemon as admit's and leaves it disabled. If you
  start that daemon after evicting, or containerd, a later admit refuses
  before changing anything, since disabling it would stop your containers.
  To keep it as yours, remove the `package docker-ce` line (and `package
  containerd.io`, if listed) from `/etc/paisans/installed`. To give it back
  to admit, stop your containers, run `systemctl disable --now` on its
  units, remove Docker's network store
  (`/var/lib/docker/network/files/local-kv.db`, or under the `data-root`
  `/etc/docker/daemon.json` sets), and reboot. Then admit again. Admit reads
  `data-root` written as `"data-root": "/absolute/path"` on one line; one
  it cannot read may hold a network store, so it is taken to have run.
* A user you deleted by hand before evicting can leave an empty
  `/etc/systemd/system/user@<uid>.service.requires` behind.
