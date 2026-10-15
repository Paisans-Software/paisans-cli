# Rootless and host admit: research spike

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to run this spike task by task. Steps use checkbox (`- [ ]`) syntax for tracking. Nothing here changes code in this repository; every task records an observation.

**Goal:** Observe, on real Ubuntu 24.04 hosts, every assumption the spec rests on that Docker's documentation does not settle, before any code is written.

**Architecture:** Two throwaway VMs, `a` and `b`, joined by a WireGuard interface named as a deployment's would be. Each experiment is a shell block run as root on `a` unless it says otherwise, with a pass criterion. Results go into a new *Observed* section of the spec, and a failed assumption changes the spec before the phase plans are written.

**Tech Stack:** Ubuntu 24.04, Docker CE from Docker's apt repository (Engine 29.5 or later), rootless extras, WireGuard, nftables, ufw, systemd.

**Spec:** `docs/specs/2026-10-15-rootless-and-host-admit.md`

## Global Constraints

* Both VMs are throwaway: R4b reboots one on purpose. Never run this on a host that serves anything.
* Ubuntu 24.04, the only host profile the toolkit ships. amd64 or arm64.
* `a` and `b` reach each other on UDP 51820 and TCP 22; `a` is reachable from `b` on TCP 80.
* Names used throughout: user `paisans-0a0b`, interface `psns-0a0b`, mesh `10.99.0.0/24`, `a` is `10.99.0.1`, `b` is `10.99.0.2`.
* No real hostname, address or key from a VM goes into the repository. Record shapes, versions and outcomes, not values.

## Review Focus

* A rootless container on host networking that cannot bind the WireGuard address breaks every infra stack: R1 decides the whole design.
* A container user's UID outside the matched range leaks past the egress table: R3 tests a non-root container user explicitly.
* Docker starting before `wg-quick@` at boot leaves infra down after every reboot: R10 reboots and looks.
* `chown` of `/dev/watchdog` inside the user namespace failing leaves data sites unadmittable: R4 tests it as Patroni does.
* `--force` beside a system daemon touching the owner's daemon: R2 checks the system daemon is untouched.

---

### Task 0: Hosts and shared helpers

- [ ] **Step 1: Create two Ubuntu 24.04 VMs**, 2 vCPU, 4 GB, 30 GB disk, with the ports in *Global Constraints* open at the provider's firewall.

- [ ] **Step 2: On both, as root, define the helpers** used by every later block (put them in `/root/spike.sh` and `. /root/spike.sh` in each shell):

```sh
U=paisans-0a0b
IF=psns-0a0b
uid_() { id -u "$U"; }
runas() { sudo -u "$U" XDG_RUNTIME_DIR=/run/user/$(uid_) DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$(uid_)/bus DOCKER_HOST=unix:///run/user/$(uid_)/docker.sock "$@"; }
subrange() { awk -F: -v u="$U" '$1==u {print $2"-"$2+$3-1}' /etc/subuid; }
```

- [ ] **Step 3: Record** `uname -r`, `lsb_release -ds`, `uname -m`.

### Task 1 (R2, R14, R18, R20): Install beside a running system daemon

- [ ] **Step 1: Install Docker from Docker's repository, with the system daemon running**

```sh
apt-get update && apt-get install -y ca-certificates curl
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
. /etc/os-release
cat > /etc/apt/sources.list.d/docker.sources <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: ${VERSION_CODENAME}
Components: stable
Signed-By: /etc/apt/keyrings/docker.asc
EOF
apt-get update
apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin docker-ce-rootless-extras uidmap wireguard-tools nftables ufw
systemctl enable --now docker
docker run -d --name owners-own --restart unless-stopped alpine sleep infinity
docker version --format '{{.Server.Version}}'
```

Pass (R20): the version is 29.5 or later.

- [ ] **Step 2: AppArmor (R18)**

```sh
sysctl kernel.apparmor_restrict_unprivileged_userns
ls /etc/apparmor.d | grep -i rootlesskit
aa-status | grep -i rootlesskit
```

Pass: the restriction is 1 and a `rootlesskit` profile is loaded from the deb.

- [ ] **Step 3: The user (R14)**

```sh
useradd --create-home --shell /bin/bash "$U" && id "$U"
grep "^$U:" /etc/subuid /etc/subgid
loginctl enable-linger "$U"
systemctl is-active user@$(uid_).service
```

Pass: the name is accepted with no `--badname`; each subordinate range is at least 65536; the user manager is active right after `enable-linger`.

- [ ] **Step 4: Setup tool, first without `--force` (R2)**

```sh
runas dockerd-rootless-setuptool.sh install; echo "exit $?"
runas dockerd-rootless-setuptool.sh install --force; echo "exit $?"
runas docker info --format '{{.ServerVersion}} {{.Driver}} {{.DockerRootDir}} {{.SecurityOptions}}'
runas rootlesskit --version
docker ps --format '{{.Names}}'; ls -l /var/run/docker.sock; systemctl is-active docker
```

Pass: without `--force` it refuses and names the system daemon; with it, it installs; the driver is `overlay2`; the root dir is under the user's home; RootlessKit is 3.0 or later; `owners-own` still runs on the system daemon, whose socket and service are unchanged.

### Task 2 (R1): Host networking binds the mesh address

- [ ] **Step 1: WireGuard between the hosts, as root on each** (swap addresses and keys on `b`)

```sh
umask 077; wg genkey | tee /root/wg.key | wg pubkey > /root/wg.pub
cat /root/wg.pub   # exchange with the other host
cat > /etc/wireguard/$IF.conf <<EOF
[Interface]
Address = 10.99.0.1/24
ListenPort = 51820
PrivateKey = $(cat /root/wg.key)

[Peer]
PublicKey = <b's public key>
AllowedIPs = 10.99.0.2/32
Endpoint = <b's public address>:51820
EOF
systemctl enable --now wg-quick@$IF && wg show $IF latest-handshakes
```

- [ ] **Step 2: A rootless host-networked container binds the mesh address**

```sh
runas docker run -d --name meshbind --network host --restart unless-stopped python:3-alpine python -m http.server 8080 --bind 10.99.0.1
ss -Hltnp | grep 10.99.0.1:8080
```

On `b`: `curl -s -o /dev/null -w '%{http_code}\n' http://10.99.0.1:8080/`

Pass: `b` gets 200. This is the design's load-bearing assumption.

- [ ] **Step 3: Ports below 1024 on all interfaces**

```sh
runas docker run --rm --network host caddy:2 caddy respond --listen :80 hi & sleep 3; kill %1
sysctl -w net.ipv4.ip_unprivileged_port_start=80
runas docker run -d --name low --network host caddy:2 caddy respond --listen :80 hi
```

On `b`: `curl -s http://<a's public address>/`

Pass: the first run fails with a permission error; after the sysctl, `b` reads `hi`.

### Task 3 (R3): Egress table keeps the user off loopback and the LAN

- [ ] **Step 1: Something of the owner's on loopback, and the table**

```sh
python3 -m http.server 9999 --bind 127.0.0.1 >/dev/null 2>&1 &
R=$(subrange); N=$(uid_)
nft -f - <<EOF
table inet paisans_0a0b {
  chain output {
    type filter hook output priority 0; policy accept;
    meta skuid { $N, $R } ip daddr 10.99.0.0/24 accept
    meta skuid { $N, $R } ip daddr 127.0.0.53 meta l4proto { tcp, udp } th dport 53 accept
    meta skuid { $N, $R } ip daddr { 127.0.0.0/8, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 100.64.0.0/10 } counter drop
    meta skuid { $N, $R } ip6 daddr { ::1, fc00::/7, fe80::/10 } counter drop
  }
}
EOF
```

- [ ] **Step 2: Probe as container root and as a container user**

```sh
for u in 0 999; do
  runas docker run --rm --network host --user $u alpine sh -c '
    wget -q -T 3 -O- http://127.0.0.1:9999/ >/dev/null && echo LOOPBACK-OPEN || echo loopback-blocked
    wget -q -T 3 -O- http://10.99.0.2:8080/ >/dev/null 2>&1; echo "mesh exit $?"
    nslookup example.com >/dev/null && echo dns-ok || echo DNS-BROKEN
    wget -q -T 5 -O- https://example.com/ >/dev/null && echo internet-ok || echo INTERNET-BROKEN'
done
runas docker run --rm alpine sh -c 'nslookup example.com >/dev/null && echo bridge-dns-ok; wget -q -T 3 -O- http://10.0.2.2:9999/ >/dev/null && echo SLIRP-HOST-OPEN || echo slirp-host-blocked'
runas docker pull hello-world >/dev/null && echo pull-ok
nft list table inet paisans_0a0b | grep counter
```

Pass: loopback blocked for both UIDs; mesh reachable (run `meshbind` on `b` first, or expect a connection refusal rather than a timeout); DNS, internet, a bridge container's DNS and an image pull all work; the drop counter is non-zero.

### Task 4 (R4): The watchdog device in a rootless container

- [ ] **Step 1: Grant the device**

```sh
modprobe softdog
echo 'KERNEL=="watchdog*", OWNER="paisans-0a0b", MODE="0600"' > /etc/udev/rules.d/60-paisans-0a0b-watchdog.rules
udevadm control --reload && udevadm trigger --subsystem-match=watchdog --subsystem-match=misc
ls -l /dev/watchdog /dev/watchdog0
```

- [ ] **Step 2: Open it the way the infra compose does**, `chown postgres` then open as that user, closing with the magic `V` so it disarms

```sh
runas docker run --rm --device /dev/watchdog alpine sh -c '
  adduser -D -u 999 postgres && chown postgres /dev/watchdog && ls -ln /dev/watchdog &&
  su postgres -s /bin/sh -c "exec 3>/dev/watchdog; printf V >&3" && echo watchdog-ok'
```

Pass: `watchdog-ok`.

- [ ] **Step 3 (R4b, destructive, `a` only): it fires.** Open it as above without writing `V` and exit. Pass: the VM reboots within softdog's default 60 seconds. Record whether the rootless daemon and `meshbind` are back afterwards (feeds R10).

### Task 5 (R5): cgroup delegation

- [ ] **Step 1: Without the drop-in**

```sh
cat /sys/fs/cgroup/user.slice/user-$(uid_).slice/user@$(uid_).service/cgroup.controllers
runas docker run --rm --cpus 0.5 --memory 64m alpine true; echo "exit $?"
```

- [ ] **Step 2: With it**

```sh
mkdir -p /etc/systemd/system/user@.service.d
printf '[Service]\nDelegate=cpu cpuset io memory pids\n' > /etc/systemd/system/user@.service.d/delegate.conf
systemctl daemon-reload && systemctl restart user@$(uid_).service
cat /sys/fs/cgroup/user.slice/user-$(uid_).slice/user@$(uid_).service/cgroup.controllers
runas docker run --rm --cpus 0.5 --memory 64m alpine true; echo "exit $?"
```

Pass: step 1 lists only `memory pids` and `--cpus` fails; step 2 lists `cpu` and succeeds. Record whether restarting `user@` stopped the user's containers.

### Task 6 (R6): Source address on a published port

- [ ] **Step 1**

```sh
runas sh -c 'mkdir -p ~/.config/docker && printf "{\"userland-proxy\": false}\n" > ~/.config/docker/daemon.json && systemctl --user restart docker'
runas docker run -d --name pub -p 8081:8080 python:3-alpine python -m http.server 8080
```

On `b`: `curl -s http://<a's public address>:8081/ >/dev/null`, then on `a`: `runas docker logs pub | tail -1`.

Pass: the log line starts with `b`'s address, not a 10.0.2.x address.

### Task 7 (R7, R8): Unprivileged reads

- [ ] **Step 1: Listening sockets of other users**

```sh
runas ss -Hltnu | grep -E ':9999|:22 '
```

Pass: root's `127.0.0.1:9999` and sshd are listed, without process names.

- [ ] **Step 2: Volume sizes**

```sh
runas docker volume create v1
runas docker run --rm -v v1:/d --user 999 alpine sh -c 'head -c 1048576 /dev/urandom > /d/f'
runas sh -c 'du -sb "$(docker volume inspect -f "{{.Mountpoint}}" v1)"'; echo "du exit $?"
runas docker system df -v | sed -n '/VOLUME NAME/,/^$/p' | grep v1
```

Pass: record whether `du` is denied; `docker system df -v` reports about 1 MB for `v1`.

### Task 8 (R9): The helper's sudoers line

- [ ] **Step 1**

```sh
install -d -m 755 /usr/local/libexec/paisans
cat > /usr/local/libexec/paisans/wg-$IF <<'EOF'
#!/bin/sh
set -eu
case "${1:-}" in
  show) [ "${2:-}" = public-key ] && exec wg show psns-0a0b public-key ;;
esac
echo "refused" >&2; exit 64
EOF
chmod 755 /usr/local/libexec/paisans/wg-$IF
echo "$U ALL=(root) NOPASSWD: /usr/local/libexec/paisans/wg-$IF" > /etc/sudoers.d/paisans-0a0b
chmod 440 /etc/sudoers.d/paisans-0a0b && visudo -c
runas sudo -n -l
runas sudo -n /usr/local/libexec/paisans/wg-$IF show public-key; echo "exit $?"
runas sudo -n /usr/local/libexec/paisans/wg-$IF rm -rf /; echo "exit $?"
runas sudo -n true; echo "exit $?"
```

Pass: `sudo -n -l` lists only the helper; `show public-key` prints a key with no password prompt and no tty; the bad verb exits 64; `sudo -n true` is refused.

### Task 9 (R10, R11): Boot order

- [ ] **Step 1: Reboot `a` and look**

```sh
reboot
# after it is back:
systemctl show -p ActiveEnterTimestampMonotonic wg-quick@$IF
runas systemctl --user show -p ActiveEnterTimestampMonotonic docker
runas docker ps --format '{{.Names}} {{.Status}}'
runas docker inspect -f '{{.RestartCount}}' meshbind
```

Pass: record which started first. `meshbind` must be `Up` with no manual action. If the user's Docker starts first and `meshbind` failed to bind, record the restart count and how long it took to come up; that decides whether admit needs an ordering mechanism (eg `net.ipv4.ip_nonlocal_bind`, or a user unit that waits for the address).

### Task 10 (R15): A second deployment on the same host

- [ ] **Step 1**

```sh
U2=paisans-0c0d
useradd --create-home --shell /bin/bash $U2 && loginctl enable-linger $U2
grep -E "^($U|$U2):" /etc/subuid
U=$U2 runas dockerd-rootless-setuptool.sh install --force
U=$U2 runas docker ps; runas docker ps --format '{{.Names}}'
```

Pass: the ranges do not overlap; each daemon lists only its own containers.

### Task 11: Record

- [ ] **Step 1: Add an *Observed* section to the spec** with one line per R-number: what was run, on which versions, and the outcome. A failed pass criterion gets a line under *Open* naming the spec section it breaks.

- [ ] **Step 2: Commit**

```bash
git add docs/specs/2026-10-15-rootless-and-host-admit.md
git commit -m "kacl-skip: docs: what the rootless spike observed on Ubuntu 24.04"
```

- [ ] **Step 3: Destroy both VMs.**
