# Rootless phase 10b: grow a production-shaped deployment on staging

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to run this plan task by task. Steps use checkbox (`- [ ]`) syntax for tracking. Nothing here changes code in this repository; every task runs commands against throwaway staging hosts and records an observation. `site admit` and `site evict` are owner actions: run them only on test hosts the operator owns, never on a host that serves anything.

**Goal:** A staging run that must pass before the founder treats the rootless work as fit for a first production release. Run A starts from the shape a first production deployment takes (a gateway VPS, a monitor on a VPS whose owner already runs a web server in Docker, and one data site at home behind NAT), grows it to two data sites and three etcd voters by following the operator documentation step by step, fails a data site hard, and replaces a lost one. Backups ship off and are documented as unsupported in the first release; Run B, backup and restore, is a future section.

**Architecture:** Four hosts. `g` (`[gateway]`) and `m` (`[monitor]`, behind its owner's web server) are VPSs with public addresses; `h` (`[data, apps]`) is undialled, as a home behind NAT is. Growth adds `h2` (`[data, apps]`, dialled) and gives `g` the `witness` role in the same change, through one `paisans apply`. Every command a deployment runs is as `paisans-<token>`, with that user's rootless Docker; every root step is `site admit` or `site evict`.

**Tech Stack:** Paisans built from `develop` with the prerequisites below merged, Ubuntu 24.04 (one host may be 22.04), rootless Docker Engine 29.5 or later, Docker CE as the owner's own on `m`, WireGuard, Patroni in Spilo, etcd, Pocket ID, Caddy as the owner's web server.

**Index:** `docs/superpowers/plans/2026-10-15-rootless-and-host-admit.md`, *Phase 10b*.

## Prerequisites

* **#393** (apply cannot join a pending `[data, apps]` site) merged. Its fix (branch `fix/join-data-apps-site`) makes the whole `apply` join a site declared `[data, apps]` as `[data]` without its apps, then apply that site, which starts its apps, then apply each gateway in `etcd.members`, whose routes gain it. `site add` typed alone still refuses `[data, apps]` and names that run. Run A uses only the whole `apply`.
* **#394** (re-admit after evict needs the system Docker started) merged, only for Task A8 Step 4, which re-admits a host after `site evict`. Every other step admits fresh hosts.
* **A decision on Run A's blocker** (see *Blockers*): today `site add` refuses any join while a declared site has neither an `endpoint` nor a `public_address`, and `h` has neither. Task A4 carries both ways the run can go.

## Global Constraints

* Every host is throwaway. Run A reboots a data host through its watchdog and powers one off, on purpose.
* Nothing from a staging host goes into this repository: no hostname, address, key or domain. Record shapes, versions, timings and outcomes. Names used throughout: sites `g`, `m`, `h`, `h2`, `h3`; hosts `<g>`, `<m>`, `<h>`, `<h2>`, `<h3>`; the domain `example.org`; mesh `10.44.0.0/24`; the deployment's token `<token>` and user `paisans-<token>`.
* `h` simulates a home behind NAT: no `public_address`, no `endpoint`, `ssh.host` set to the address the operator reaches it on, and inbound UDP dropped at the provider's firewall, so nothing can dial its WireGuard. Every other host has a `public_address`.
* Data sites share an architecture. Ubuntu releases may differ.
* Commands scripted with `--yes` run with `PAISANS_ALLOW_YES=1` exported in that shell only, never in a profile.
* `site admit` runs from the workstation with `--site <name> --ssh ubuntu`, where `ubuntu` is the cloud image's login with sudo. The request and digest path was proved in phase 10a and is not repeated here.
* **`doctor` clean** means exit 0, no `FAIL`, and every `WARN` it prints named in that step's expected list. A `WARN` not on the list fails the step. The expected list for a steady state is empty unless a step names one.
* **Hard failover** means the leader's Patroni killed or its host stopped, with the outage and the watchdog reboot accepted. Every app must answer again through the gateway with no manual restart.
* Every finding the docs do not settle is recorded against its *Open:* item below and settled in a pull request that changes the docs or the code, not in this plan.

## Review Focus

* Data written before growth that is missing after it: the run fails whatever else passes.
* An `apply` that claims `Deployment complete.` while `patronictl list` shows no `Sync Standby`, or while Pocket ID is not exactly one active instance.
* An app that answers 500 after a hard failover until someone restarts it.
* `site admit` on `m` touching the owner's Docker or their containers in any way.
* A re-run of `apply` that plans any step.

## Hosts

| Host | Roles at start | Roles at end | Minimum | Used |
|------|----------------|--------------|---------|------|
| `<g>` | `gateway` | `gateway, witness` | 1 vCPU, 1 GB RAM, 20 GB disk | 2 vCPU, 2 GB |
| `<m>` | `monitor`, behind the owner's web server | the same | 1 vCPU, 1 GB RAM, 20 GB disk, plus what the owner's containers use | 2 vCPU, 2 GB |
| `<h>` | `data, apps`, undialled | the same | 2 vCPU, 2 GB RAM, 30 GB disk | 2 vCPU, 4 GB |
| `<h2>` | not yet declared | `data, apps`, dialled | as `<h>` | as `<h>` |
| `<h3>` | not yet declared | `data, apps`, replacing a lost data site (A8) | as `<h>` | as `<h>` |

Phase 10 found 458 MB too small for a host running etcd beside Caddy; 1 GB is the floor for any host here. `site admit` warns below 20 GB free for a data site and below 5 GB for any other; apply refuses to pull with less than 3 GB free in Docker's data root.

Ports at each provider's firewall, matching what `site admit` opens in ufw: 22/tcp everywhere; 51820/udp on `<g>`, `<m>`, `<h2>` and `<h3>`; nothing else inbound on `<h>`; 80/tcp and 443/tcp on `<g>` and on `<m>` (the owner's web server, which the owner opens themselves).

## Workstation helpers

Defined once in the shell every task runs in:

```sh
export PAISANS_ALLOW_YES=1
TOKEN=<token>                         # paisans site list shows paisans-<token> under SSH after the first apply
R=/srv/paisans/$TOKEN
# on <host> '<command>': runs a command as the deployment's user, against its own Docker
on() { ssh "paisans-$TOKEN@$1" "export DOCKER_HOST=unix:///run/user/\$(id -u)/docker.sock; $2"; }
infra() { echo "docker compose -f $R/infra/compose.yaml exec -T $1"; }
psql_on() { on "$1" "$(infra patroni) psql -X -A -t -U postgres -d $2 -c \"$3\""; }
```

`psql_on` reaches Postgres over the Spilo container's own socket, as `internal/patroni` does for its size check. On a replica it reads; on the leader it also writes.

---

## Run A: grow a production-shaped deployment

The docs this run follows: README *Adding a site, and the constraint that shapes it* (the voters go from one to three in one change), *Adding the gateway and the witness* (a gateway VM already running gains `witness` in the change that adds the second data site), *`site add`: a second data site, and the one hard problem*, *`site add` is built for sites that dial each other* and *The staged gate, and the half-joined site*; *Where the mesh dials a site* and *The voters that survive one loss must still dial each other*; `docs/guides/behind-your-own-web-server.md` and `docs/guides/host-owners.md`; and `docs/commands.md` for `site admit`, `site add`, `preflight`, `apply`, `doctor` and `site remove`.

### Task A0: The owner's own web server on `<m>`

Before the deployment exists, `<m>`'s owner runs Docker CE and a web server of their own on 80 and 443, as a VPS already in use would.

- [ ] **Step 1: Install Docker CE from Docker's repository**, Engine 29.5 or later, with its rootless extras (admit refuses a Docker without `dockerd-rootless-setuptool.sh`):

```sh
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-ce-rootless-extras docker-compose-plugin
docker version --format '{{.Server.Version}}'
```

- [ ] **Step 2: Run the owner's containers.** A web server on the host's network, holding 80 and 443, so it can proxy to a loopback upstream as the guide advises, and an unrelated app behind it:

```sh
sudo mkdir -p /opt/owner && sudo tee /opt/owner/Caddyfile >/dev/null <<'EOF'
{
	admin unix//run/caddy-admin.sock
}

owner.example.org {
	reverse_proxy 127.0.0.1:9000
}
EOF
sudo docker run -d --name owner-app --restart unless-stopped -p 127.0.0.1:9000:80 nginx:1.27
sudo docker run -d --name owner-caddy --restart unless-stopped --network host \
  -v /opt/owner/Caddyfile:/etc/caddy/Caddyfile -v owner-caddy-data:/data caddy:2.8
curl -sS -o /dev/null -w '%{http_code}\n' https://owner.example.org/
```

Expected: 200. Record `sudo docker ps --format '{{.Names}} {{.Status}}'`, `sudo ss -Hltnp '( sport = :80 or sport = :443 )'` and `lsmod | grep br_netfilter` as the baseline Task A2 compares with.

### Task A1: Declare the starting shape

- [ ] **Step 1: Write `paisans-stack.yaml`:**

```yaml
version: 1
id: <a new uuid>
community:
  name: "Paisans staging, production shape"
  domain: example.org
mesh:
  subnet: 10.44.0.0/24
acme:
  provider: cloudflare
operators:
  alice: { name: Alice, email: alice@example.org, keys: "ssh-ed25519 AAAA... alice" }
  bob:   { name: Bob,   email: bob@example.org,   keys: "ssh-ed25519 AAAA... bob" }
sites:
  g:
    arch: amd64
    roles: [gateway]
    address: 10.44.0.3
    public_address: 203.0.113.30
    ssh: { keys: [alice, bob] }
  m:
    arch: amd64
    roles: [monitor]
    address: 10.44.0.4
    public_address: 203.0.113.40
    web_server:
      managed_by: external
      listen: 127.0.0.1:8480
    ssh: { keys: [alice, bob] }
  h:
    arch: amd64
    roles: [data, apps]
    address: 10.44.0.1
    ssh: { host: 198.51.100.10, keys: [alice, bob] }   # no public_address, no endpoint: it only dials out
cluster:
  port: 5000
  postgres_version: "18"
  synchronous: true
  synchronous_strict: false
apps:
  auth:    { blueprint: pocket-id, hostname: id.example.org,      placement: cluster, settings: { management: paisans, file_backend: database, signup_default_groups: [members] } }
  account: { blueprint: account,   hostname: account.example.org, placement: cluster, settings: { signup: invite, require_approval: true } }
  wiki:    { blueprint: wikijs,    hostname: wiki.example.org,    placement: cluster, gated: true, auto_login: true }
  health:  { blueprint: uptime,    hostname: health.example.org,  placement: { pinned: m }, settings: { admin_group: operators } }
smtp: { host: <a relay at a public address>, port: 587, security: starttls, from_address: noreply@example.org }
```

`etcd.members` and `cluster.sites` stay out: the one voter and the one cluster member are `h`. The mail relay is at a public address, since the egress table drops a relay on a private or loopback address (*What the deployment can reach*, host owners' guide).

- [ ] **Step 2: Validate.**

```sh
paisans validate
```

Expected: no refusal; the warning `backups-off`, and no other. Then, on a copy, give `g` the `witness` role and validate again: expected refusal `two-etcd-voters` ("etcd has exactly two voters"), which is why the witness waits for growth.

### Task A2: Admit the three hosts

- [ ] **Step 1: Admit.**

```sh
paisans site admit --site g --ssh ubuntu --public-ports --execute --yes
paisans site admit --site m --ssh ubuntu --execute --yes
paisans site admit --site h --ssh ubuntu --watchdog --execute --yes
```

Expected:
* `g`: granted public ports; ufw opens 22/tcp, 51820/udp, 80/tcp, 443/tcp, 443/udp; the record's `ports.wireguard` is 51820.
* `m`: no grant asked for (a monitor with `managed_by: external` runs no Caddy, so `admission.Needs` asks neither); 51820/udp opened, 80 and 443 not; the plan names the owner's web server as `caddy` in container `owner-caddy`, which `webserver.Detect` finds as a container on the host's network that publishes nothing; the plan says Caddy's admin API is not on TCP; `br_netfilter` already loaded (Docker loads it) or, if not, admit names the owner's bridges and stops unless `--bridge-netfilter`, which is then recorded. The record's `web_server` holds the server, container and config path.
* `h`: granted the watchdog; the record's `ports.wireguard` is 0 and no UDP port is opened, since nothing dials `h`.

- [ ] **Step 2: The owner's Docker is untouched on `<m>`.** Compare with A0's baseline: the same containers `Up` with unchanged uptimes, the same listeners on 80 and 443, `systemctl is-active docker` still `active`, `https://owner.example.org/` still 200. The admission record's `system_docker_disabled` is false. Failure signal: any owner container restarted, or the system daemon disabled.

### Task A3: Deploy the starting shape, and write member data

- [ ] **Step 1: Deploy.**

```sh
paisans init
paisans secrets set external.acme_dns_token
paisans apply --execute --yes
```

Expected: `Deployment complete.`, or the warning naming `paisans web-server show --app health` until Step 2, with exit 0.

- [ ] **Step 2: The owner installs the monitor's snippet.**

```sh
paisans web-server show --app health
```

Expected: the sheet names `caddy (docker container owner-caddy, config ...)`, the upstream `http://127.0.0.1:8480`, the four refused paths, and a Caddy site block for `health.example.org`. As `<m>`'s owner, add the block after the global options in `/opt/owner/Caddyfile`, then `sudo docker exec owner-caddy caddy validate --config /etc/caddy/Caddyfile` and `sudo docker restart owner-caddy` (the admin API is on a socket inside the container, so a restart rather than a reload). Then:

```sh
paisans web-server check --app health
curl -sS -o /dev/null -w '%{http_code}\n' https://health.example.org/
curl -sS -o /dev/null -w '%{http_code}\n' https://health.example.org/metrics
```

Expected: the check passes; the monitor answers under 500; `/metrics` is refused; `owner.example.org` still answers.

- [ ] **Step 3: `doctor` clean.** `paisans doctor`; expected list: empty.

- [ ] **Step 4: Write member data.** Through the browser, as a person would: each operator opens the one-time sign-in link `apply` printed and registers a passkey in Pocket ID; `alice` invites a member through `account.example.org`, the member registers with a passkey, and `bob` approves; the member creates a wiki page with a known title.

- [ ] **Step 5: Write canary rows.**

```sh
psql_on <h> postgres "CREATE DATABASE paisans_canary"
psql_on <h> paisans_canary "CREATE TABLE canary (id serial PRIMARY KEY, phase text, at timestamptz DEFAULT now())"
psql_on <h> paisans_canary "INSERT INTO canary (phase) SELECT 'before-growth' FROM generate_series(1, 10000)"
```

- [ ] **Step 6: Record the baseline** in the run's notes (not in the repository):

```sh
psql_on <h> paisans_canary "SELECT count(*), md5(string_agg(id::text || phase, ',' ORDER BY id)) FROM canary"
psql_on <h> auth "SELECT count(*) FROM users"
psql_on <h> auth "SELECT count(*) FROM webauthn_credentials"
psql_on <h> wiki "SELECT count(*) FROM pages"
```

Table names are Pocket ID v2's and Wiki.js 2's; confirm each with `\dt` first and record the ones used.

### Task A4: Declare the end state

- [ ] **Step 1: Edit `paisans-stack.yaml`** in one change, as README's growth sections say: `g` gains `witness`, and `h2` is added.

```yaml
  g:
    roles: [gateway, witness]          # was [gateway]
  h2:
    arch: amd64
    roles: [data, apps]
    address: 10.44.0.2
    public_address: 203.0.113.20
    ssh: { keys: [alice, bob] }
```

- [ ] **Step 2: Validate.**

```sh
paisans validate
```

Expected: no refusal, and `backups-off` alone. The voters derive to `g`, `h` and `h2`, two of them dialled (`g`, `h2`), so `voters-share-a-relay` passes; `h` and `h2` peer directly, since `h2` is dialled.

- [ ] **Step 3: The join's own rule.** `site add` (`internal/siteadd/siteadd.go`, `checkScope`, unchanged on #393's branch) refuses a join while any declared site has neither an `endpoint` nor a `public_address` ("site add joins sites that all dial each other directly; a site behind NAT needs the relay ..., which is not built yet"), and README *`site add` is built for sites that dial each other* says the same. `h` has neither, so `paisans apply` stops at `site add h2`. Check that this is still so:

```sh
paisans apply 2>&1 | grep -n 'nothing dials it'
```

If it refuses, the run takes one of these, as the founder decides (see *Blockers*):
* **A4a, make `h` dialled**, the way out README *The voters that survive one loss must still dial each other* gives for a home: a dynamic DNS name and one forwarded UDP port. Simulate it by declaring `endpoint: h.example.org:51820` on `h`, an A record for it to `<h>`'s address, and 51820/udp opened at its provider's firewall. This changes `h`'s WireGuard port and forwarding, which `admission.Record.Differences` compares, so `h`'s owner admits it again:

  ```sh
  paisans site prepare --site h                       # expected: refused, naming the WireGuard port (0 against 51820) and forwarding off
  paisans site request --site h > h.admit             # the digest is on stderr
  paisans site admit --site h --ssh ubuntu --watchdog --execute --yes
  ```

  Expected: the plan opens 51820/udp, turns forwarding on, re-renders the WireGuard helper with port 51820, and leaves the user, its Docker and its containers running; `site prepare --site h` then plans nothing.
* **A4b, wait for the relay**, which README calls a design that "the first deployment that needs it builds". Run A stops here until it exists.

### Task A5: Does `g` need its owner again for `witness`?

No, from the code. `admission.Record.Differences` (`internal/admission/record.go`) compares the site name, user, SSH port, `ports.wireguard` against `Site.ListenPort()`, the mesh subnet, forwarding against the presence of an `endpoint`, the grants the roles need, and the web server. It does not compare roles. For `g`, `ListenPort()` stays 51820 (from its `public_address`), no `endpoint` is added, and `admission.Needs` for `[gateway, witness]` is public ports alone, as for `[gateway]`: the witness needs no watchdog, since only the data role does. etcd's ports need no firewall change either: `hostprep.AdmitRules` lets the whole mesh interface in (`allow in on psns-<token>`, "the mesh: etcd, Patroni, Garage, HAProxy"), and opens per-port rules only for SSH, WireGuard and the public ports. So `site prepare`, preflight and `apply` do not refuse `g`, and no owner step comes before the apply.

- [ ] **Step 1: Confirm.**

```sh
paisans site prepare --site g
paisans preflight --site h2
```

Expected: `site prepare` plans nothing and names no `site request` line; preflight checks `h2` as a blank host and the witness joining beside it, every row `ok`, and nothing about `g`'s admission.

- [ ] **Step 2: Record what goes stale.** `ssh ubuntu@<g> cat /etc/paisans/admitted.$TOKEN.json` and `paisans site deployments --ssh ubuntu@<g>` after growth. Expected: both still list `g`'s roles as `[gateway]`, the roles it was admitted with. Nothing compares them (*Open: stale roles*).

### Task A6: Admit `h2`, and converge

- [ ] **Step 1: Admit.**

```sh
paisans site admit --site h2 --ssh ubuntu --watchdog --execute --yes
```

Expected: granted the watchdog and not public ports; 51820/udp opened; the record's `ports.wireguard` 51820.

- [ ] **Step 2: Dry run.**

```sh
paisans apply
```

Expected, in the order README's converge table gives on #393's branch: `init` with work (keys and secrets for `h2`); `site prepare` on every site; the pull on `h2`; `dns init`; `site add h2` with `g`'s etcd member joining first, as a learner, and `h2` after it, joined as `[data]` without its apps; then the other sites, `h2` first, which starts its apps, then `g`, the gateway in `etcd.members`, whose routes gain `h2`; `m` last; pass two. The review lists `h2`'s keys and images, its apps' included.

- [ ] **Step 3: Apply.**

```sh
paisans apply --execute --yes
```

Expected: `site add` passes all eight gates in order (the mesh's handshakes on both ends of every pair, etcd one to three, the replica streaming, `Sync Standby`, HAProxy, the replicas' `patroni.env`, the monitor's seed), and the run ends `Deployment complete.`. Failure signal: `site add h2: the site's roles must be exactly [data]` (#393 not merged), the `nothing dials it` refusal (A4 not settled), or any gate failing; record the stage and resume with the same command, which must start from the first unmet gate.

### Task A7: Checks after growth

- [ ] **A7.1 Data survived.** Re-run A3 Step 6 on `<h>` and on `<h2>` (a replica answers reads). Expected: identical counts and digest on both, equal to the baseline. Then sign in to `id.example.org` with each operator's and the member's existing passkey, and open the wiki page. Failure signal: any count or digest differs, or a passkey is refused.

- [ ] **A7.2 Three voters, and a replica in sync.**

```sh
on <h> "$(infra etcd) etcdctl --endpoints=http://10.44.0.1:2379 member list -w table"
on <h> "$(infra etcd) etcdctl --endpoints=http://10.44.0.1:2379,http://10.44.0.2:2379,http://10.44.0.3:2379 endpoint health -w table"
on <h> "$(infra patroni) patronictl -c /home/postgres/postgres.yml list"
psql_on <h> paisans_canary "INSERT INTO canary (phase) SELECT 'after-growth' FROM generate_series(1, 100)"
psql_on <h2> paisans_canary "SELECT count(*) FROM canary WHERE phase = 'after-growth'"
```

Expected: three members, none a learner, all healthy; `h` `Leader` `running`, `h2` `Sync Standby` `streaming`, lag 0; 100 on `h2` at once.

- [ ] **A7.3 The gateway routes to `h2`.** On `<g>`, `grep -n -A4 reverse_proxy $R/infra/caddy/Caddyfile` lists both data sites' mesh addresses for `auth`, `account` and `wiki`. Then stop each app on `h` and reach it through the gateway:

```sh
on <h> "docker compose -f $R/wiki/compose.yaml stop"
curl -sS -o /dev/null -w '%{http_code}\n' https://wiki.example.org/
on <h2> "docker compose -f $R/wiki/compose.yaml logs --since 1m | tail -5"
paisans apply --site h --only wiki --execute --yes
```

Expected: an answer under 500 (a redirect to sign-in counts) served from `h2`'s log; the apply starts it again on `h`. Repeat for `account`.

- [ ] **A7.4 Pocket ID: one active, one standby.** The apply's output carries `pocket-id auth: one active instance` with one site `active` and the other `standby`, and `doctor`'s Pocket ID row agrees. Then stop the active one cleanly:

```sh
on <active> "docker compose -f $R/auth/compose.yaml stop"
paisans doctor
paisans apply --site <active> --only auth --execute --yes
```

Expected: within about 15 seconds the other site is `active` (README: about two seconds at a 3 s retry; the default retry is 15 s), sign-in with a passkey works through the gateway, and the restarted instance comes back `standby`.

- [ ] **A7.5 The monitor knows the new sites.** Stage 8 reseeded `health` on `m`: in the uptime UI, `h2`'s ping and direct checks exist and are green, `g` keeps its own, and `https://health.example.org/` still answers through the owner's Caddy. `owner.example.org` still answers, and A2 Step 2's comparison still holds on `<m>`.

- [ ] **A7.6 `doctor` clean.** `paisans doctor`; expected list: empty. Exit 0 and no `FAIL` on the Reach, Admission, rootless, sudo, etcd, Patroni, containers, Pocket ID, clocks, web server and Leftovers rows of every site.

- [ ] **A7.7 Hard failover: kill the leader's Patroni.**

```sh
on <h2> "$(infra patroni) patronictl -c /home/postgres/postgres.yml list"    # confirm h leads
on <h> "docker compose -f $R/infra/compose.yaml kill patroni"
```

Expected: the killed Patroni never closes `/dev/watchdog`, so `<h>` reboots within the watchdog's timeout (Patroni's default `ttl` 30 less `safety_margin` 5, about 25 seconds); `h2` is promoted once the leader key expires; the gateway on `g` keeps answering and routes to `h2`'s apps; if `h`'s Pocket ID was active, `h2`'s takes over within about 90 seconds plus a retry. Write during the outage:

```sh
psql_on <h2> paisans_canary "INSERT INTO canary (phase) SELECT 'during-failover' FROM generate_series(1, 50)"
```

Then, with no command to any app: poll `id.example.org/.well-known/openid-configuration`, `account.example.org` and `wiki.example.org` through the gateway every 5 seconds, sign in with a passkey, and save a wiki edit. Expected: every app answers under 500 and the sign-in and the edit succeed without any restart. After `<h>` returns: `h` rejoins as `Sync Standby` and `streaming`, A7.1's counts and the `during-failover` rows are on both sites, and `doctor` is clean (expected list: empty). Record: time to promotion, time to the host's return, and time until each app answered. Failure signal: two leaders at any moment, `h` returning as a second leader, a lost row, or an app answering 500 until restarted.

- [ ] **A7.8 Hard failover: stop the leader's host.** Now `h2` leads. Power `<h2>` off at the provider, without a shutdown. Expected: `h` is promoted, the apps answer without a restart as in A7.7, and `paisans apply` runs degraded, ending `Applied, degraded: 1 sites pending or down (h2)`. `doctor` during the outage, expected list: the Reach row's `WARN` for `h2` as down, naming `paisans site remove --site h2 --host-gone`, the matching `WARN`s for its etcd member and its Patroni member, and, while the standby takes over, Pocket ID's row if `h2`'s instance was the active one; record each line exactly, and the run fails on any other. Power `<h2>` on: it rejoins as `Sync Standby`; `doctor` clean, expected list empty.

- [ ] **A7.9 A re-run changes nothing.**

```sh
paisans apply
paisans apply --execute --yes
```

Expected: the dry run's `install` section reads `up to date` for every site, the review lists nothing, the web server check passes, and the executed run touches no stack, ending `Deployment complete.`. Failure signal: any planned step.

### Task A8: Lose a data site's host for good, and replace it

The surviving data site holds every row, so this needs no backup.

- [ ] **Step 1: Lose it.** Power `<h2>` off and treat it as gone.

- [ ] **Step 2: Remove it.**

```sh
paisans site remove h2 --host-gone
paisans site remove h2 --host-gone --execute --yes
```

Expected, per *`site remove` takes a site out*: two data sites and a witness go to one voter, so `g`'s etcd member and its `witness` role leave in the same change, and `g`'s roles in `paisans-stack.yaml` are `[gateway]` again; `h2`'s block leaves the file; its DNS records go. `doctor` clean, expected list empty.

- [ ] **Step 3: Replace it.** Declare `h3` (`[data, apps]`, `public_address` set) and give `g` the `witness` role again, in one edit; admit `<h3>` as A6 Step 1 does; `paisans apply --execute --yes`. Expected: A7.1 to A7.6 pass with `h3` in `h2`'s place, and no owner step on `g` (A5's reasoning holds again).

- [ ] **Step 4 (needs #394): The same host again.** Instead of `<h3>`, bring `<h2>` back, have its owner run `sudo paisans site evict --deployment <id> --user paisans-<token> --execute`, then admit it as `h3`. Expected: admit runs without the owner starting the system Docker, and the host keeps no running system Docker afterwards.

---

## Run B, future: backup and restore under rootless

The first production release ships with backups off, documented as unsupported. Run B runs once phases 2 to 6 of `docs/specs/2026-10-11-backups.md` exist; today only phase 1 does (the `backup:` schema, its `validate` rules and its secrets), and README says "nothing runs a backup yet". The design it will test: WAL-G inside Spilo on the leader for the cluster, `postgres-walg` for a pinned Postgres, a `paisans-backup` runner on every site with restic for files and SQLite and an age encrypted bundle of the configuration store, all inside the deployment's containers and so as its subordinate UIDs, out through the egress table to S3 destinations; restore through `paisans backup restore` and `drill`, including point in time recovery through `CLONE_TARGET_TIME`. Its plan, written when those phases land, settles these first:

* **Open: backup commands.** The spec names `paisans backup status | run | verify | restore | drill` as provisional and gives no flags: how a restore chooses its scenario, the database or app, the destination and the target time.
* **Open: restoring everything.** The spec's *Everything is lost* gives no order between recovering the configuration from a destination, changing public addresses, the owners' `site admit`, and an `apply` that bootstraps from the backup rather than founding an empty cluster.
* **Open: stale cluster state in etcd.** Restoring a lost cluster through `CLONE_WITH_WALG` into an etcd that still holds the old scope's leader and member keys, or into a new etcd, is not covered; nor is who clears the old keys.
* **Open: private and loopback destinations.** The egress table drops a destination on a private, link-local or loopback address, or on the host's own address. Neither `validate` nor the spec says so.
* **Open: a home backup target.** The spec's home-hardware Garage is "one more S3 endpoint", but on a LAN shared with a site the egress table drops it, and a machine that is not a site has no mesh address.
* **Open: restores and subordinate UIDs.** The spec does not say where a restic restore of a pinned app's files or Uptime's SQLite runs; written from the host as the deployment's user, they would be owned by UID 0 inside the container.
* **Open: object storage.** README's backup table includes object storage, and the spec mirrors Garage buckets through `rclone`; Run A declares no `storage` site, so a Run B on its deployment would not test it.

## Open

* **Open: undialled data sites cannot grow.** The production shape puts the first data site at home with no endpoint, and `site add` refuses every join while it is so. Decide between A4a (the operator gives the home a dynamic DNS name and a forwarded port, and its owner re-admits) as the documented growth path, or building the relay before the first release.
* **Open: no owner step when a gateway gains `witness`.** The code needs none (Task A5), but README *Adding the gateway and the witness* and *Adding a site, and the constraint that shapes it* do not say so, nor that an `endpoint` added at growth does need the owner. Decide whether those sections say both.
* **Open: stale roles.** After growth, `g`'s admission record and its registry entry still list `[gateway]`, which `site deployments` shows its owner. Nothing compares roles, so nothing refuses or warns. Decide whether a role change is reported, or the owner's listing says the roles are those admitted.
* **Open: an owner web server in a bridged container.** The guide tells the operator to use a loopback `listen` when the web server runs on the same machine, but a web server in a container that publishes 80 and 443 on a bridge network cannot reach the host's loopback, and the mesh address it could reach is warned about (`web-server-listen-bypasses-firewall`) and sits behind ufw, which admits the mesh interface and not the owner's bridge. Run A uses a container on the host's network. Decide what the guide tells an owner whose web server is bridged.

## Blockers

* **Run A cannot grow the production shape today.** `checkScope` refuses `site add h2` because `h` has neither an `endpoint` nor a `public_address`, and #393's branch does not change that. A4a is the documented way out and runs today with #393; A4b waits for the relay.
* **#393 is not merged.** Without it the whole apply stops at `site add h2: the site's roles must be exactly [data]`.
