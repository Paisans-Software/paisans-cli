# Rootless phase 10b: grow and back up on staging

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to run this plan task by task. Steps use checkbox (`- [ ]`) syntax for tracking. Nothing here changes code in this repository; every task runs commands against throwaway staging hosts and records an observation. `site admit` and `site evict` are owner actions: run them only on test hosts the operator owns, never on a host that serves anything.

**Goal:** Two staging runs that must pass before the founder treats the rootless work as fit for a first production release. Run A grows a running single-site deployment to two data sites and a witness, following the operator documentation step by step. Run B declares backups on a running deployment, takes one, destroys data and restores it.

**Architecture:** Run A starts from one host holding every role, deployed and holding member data, then admits two more hosts and converges with one `paisans apply`. Run B runs on the deployment Run A leaves, plus an S3 compatible destination at a public address and one spare host. Every command a deployment runs is as `paisans-<token>`, with that user's rootless Docker; every root step is `site admit` or `site evict`.

**Tech Stack:** Paisans built from `develop` with the prerequisites below merged, Ubuntu 24.04 (one host may be 22.04), rootless Docker Engine 29.5 or later, WireGuard, Patroni in Spilo, etcd, Pocket ID, an S3 compatible destination.

**Index:** `docs/superpowers/plans/2026-10-15-rootless-and-host-admit.md`, *Phase 10b*.

## Prerequisites

* **#393** (apply cannot join a pending `[data, apps]` site) merged, for Run A and for Run B's replacement of a lost data site. Today `site add` refuses any joining site whose roles are not exactly `[data]` (`internal/siteadd/siteadd.go`, `checkScope`), whether `apply` reaches it from a pending site or from one admitted before the run. Run A declares the new site `[data, apps]` and expects one `apply` to join it.
* **#394** (re-admit after evict needs the system Docker started) merged, only for a step that re-admits a host after `site evict`. Run A has none. Run B's B9 has one; every other task in both runs admits fresh hosts.
* **Backups, phases 2 to 6 of `docs/specs/2026-10-11-backups.md`**, merged for Run B's tasks B3 to B8. Only phase 1 (the `backup:` schema, its `validate` rules and its secrets) exists today: nothing renders WAL-G's environment into `patroni.env`, no runner image or `backup-plan.json` exists, and there is no `paisans backup` command. README says so: "What exists so far is the section and its validation; nothing runs a backup yet." This is a blocker for Run B as a backup test; see *Blockers*.

## Global Constraints

* Every host is throwaway. Run A reboots a data host through its watchdog on purpose, and Run B destroys data on purpose.
* Nothing from a staging host goes into this repository: no hostname, address, key or domain. Record shapes, versions, timings and outcomes. Names used throughout: sites `site-a`, `site-b`, `witness`, `site-c`; hosts `<site-a>`, `<site-b>`, `<witness>`, `<site-c>`; the domain `example.org`; mesh `10.44.0.0/24`; the deployment's token `<token>` and user `paisans-<token>`.
* Every host has a `public_address`, so every site is dialled and `site add` accepts the shape (`checkScope` refuses a site with neither an endpoint nor a public address). No site declares an `endpoint`.
* Data sites share an architecture. Ubuntu releases may differ.
* Commands scripted with `--yes` run with `PAISANS_ALLOW_YES=1` exported in that shell only, never in a profile.
* `site admit` runs from the workstation with `--site <name> --ssh ubuntu`, where `ubuntu` is the cloud image's login with sudo. The request and digest path was proved in phase 10a and is not repeated here.
* Every finding the docs do not settle is recorded against its *Open:* item below and settled in a pull request that changes the docs or the code, not in this plan.

## Review Focus

* Data written before growth that is missing after it: the run is a failure whatever else passes.
* An `apply` that claims `Deployment complete.` while `patronictl list` shows no `Sync Standby`, or while one Pocket ID is not exactly one active instance.
* A re-run of `apply` that plans any step: growth left something unconverged.
* A restore that brings back rows but leaves Postgres unable to start because the data directory is owned outside the user's subordinate range.
* A backup run that reports success while its destination is unreachable through the egress table.

## Hosts

| Run | Host | Roles | Minimum | Used |
|-----|------|-------|---------|------|
| A | `<site-a>` | `gateway, data, apps, monitor` | 2 vCPU, 4 GB RAM, 30 GB disk | the same |
| A | `<site-b>` | `data, apps` | 2 vCPU, 2 GB RAM, 30 GB disk | 2 vCPU, 4 GB |
| A | `<witness>` | `witness` | 1 vCPU, 1 GB RAM, 20 GB disk | the same |
| B | the three above, as Run A leaves them | as above | as above | as above |
| B | `<site-c>` | `data, apps`, the replacement data site | as `<site-b>` | as `<site-b>` |
| B | `<s3>` | none: an S3 compatible bucket at a public HTTPS endpoint, a provider's or Garage behind TLS on its own VM | 1 GB RAM | the same |

Phase 10 found 458 MB too small for a host running etcd beside Caddy; 1 GB is the floor for any host here. `site admit` warns below 20 GB free for a data site and below 5 GB for any other; apply refuses to pull with less than 3 GB free in Docker's data root.

Ports, at each provider's firewall as well as on the host, where `site admit` opens them in ufw: 22/tcp on every host; 51820/udp on every deployment host; 80/tcp and 443/tcp on `<site-a>`.

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

## Run A: grow a single-site deployment to three sites

The README's path for growth is *Adding a site, and the constraint that shapes it*: the voters go from one to three in one change, the new data site and a witness in a third location together. *Adding the gateway and the witness* describes the case where a gateway VM already runs; here the gateway is on `site-a` itself, so the witness is a new host, which the README's stage table allows ("given to the gateway, or to a site in a third location, in the same change"). *`site add`: a second data site, and the one hard problem* and *`site add` is built for sites that dial each other* give the join's stages and gates, and `docs/commands.md` gives `site add`, `preflight` and `apply`.

### Task A0: Start: one site with every role, deployed, holding data

- [ ] **Step 1: Write `paisans-stack.yaml`** with one site and the apps the checks need:

```yaml
version: 1
id: <a new uuid>
community:
  name: "Paisans staging, grow and back up"
  domain: example.org
mesh:
  subnet: 10.44.0.0/24
acme:
  provider: cloudflare
operators:
  alice: { name: Alice, email: alice@example.org, keys: "ssh-ed25519 AAAA... alice" }
  bob:   { name: Bob,   email: bob@example.org,   keys: "ssh-ed25519 AAAA... bob" }
sites:
  site-a:
    arch: amd64
    roles: [gateway, data, apps, monitor]
    address: 10.44.0.1
    public_address: 203.0.113.11
    ssh: { keys: [alice, bob] }
cluster:
  port: 5000
  postgres_version: "18"
  synchronous: true
  synchronous_strict: false
apps:
  auth:    { blueprint: pocket-id, hostname: id.example.org,      placement: cluster, settings: { management: paisans, file_backend: database, signup_default_groups: [members] } }
  account: { blueprint: account,   hostname: account.example.org, placement: cluster, settings: { signup: invite, require_approval: true } }
  wiki:    { blueprint: wikijs,    hostname: wiki.example.org,    placement: cluster, gated: true, auto_login: true }
  health:  { blueprint: uptime,    hostname: health.example.org,  placement: { pinned: site-a }, settings: { admin_group: operators } }
smtp: { host: <a relay or sink the hosts reach>, port: 587, security: starttls, from_address: noreply@example.org }
```

No `endpoint` on `site-a`, and no `backup` section yet. The mail relay must be at a public address or on the mesh: the egress table drops a relay on a private or loopback address (*What the deployment can reach*, `docs/guides/host-owners.md`).

- [ ] **Step 2: Admit `site-a`**, as its owner would from the workstation:

```sh
paisans site admit --site site-a --ssh ubuntu --watchdog --public-ports --execute --yes
```

Expected: the plan shows `firewall` opening 22/tcp, 51820/udp, 80/tcp, 443/tcp and 443/udp; the admission record's `ports.wireguard` is 51820. Observe: `ssh ubuntu@<site-a> cat /etc/paisans/admitted.$TOKEN.json`.

- [ ] **Step 3: Deploy.**

```sh
paisans validate
paisans init
paisans secrets set external.acme_dns_token
paisans apply --execute --yes
```

Expected: `Deployment complete.`; `paisans doctor` exits 0.

- [ ] **Step 4: Write member data.** Through the browser, as a person would:
  * each operator opens the one-time sign-in link `apply` printed and registers a passkey in Pocket ID;
  * `alice` invites a member through `account.example.org`, the member registers with a passkey, and `bob` approves;
  * the member signs in to `wiki.example.org` and creates a page with a known title and body.

- [ ] **Step 5: Write canary rows** the run can count exactly:

```sh
psql_on <site-a> postgres "CREATE DATABASE paisans_canary"
psql_on <site-a> paisans_canary "CREATE TABLE canary (id serial PRIMARY KEY, phase text, at timestamptz DEFAULT now())"
psql_on <site-a> paisans_canary "INSERT INTO canary (phase) SELECT 'before-growth' FROM generate_series(1, 10000)"
```

- [ ] **Step 6: Record the baseline**, kept in the run's notes (not in the repository):

```sh
psql_on <site-a> paisans_canary "SELECT count(*), md5(string_agg(id::text || phase, ',' ORDER BY id)) FROM canary"
psql_on <site-a> auth "SELECT count(*) FROM users"
psql_on <site-a> auth "SELECT count(*) FROM webauthn_credentials"
psql_on <site-a> wiki "SELECT count(*) FROM pages"
```

Table names are Pocket ID v2's and Wiki.js 2's; confirm each with `\dt` first and record the ones used.

### Task A1: Declare the end state

- [ ] **Step 1: Edit `paisans-stack.yaml`**, adding both sites in one edit:

```yaml
  site-b:
    arch: amd64
    roles: [data, apps]
    address: 10.44.0.2
    public_address: 203.0.113.12
    ssh: { keys: [alice, bob] }
  witness:
    arch: amd64
    roles: [witness]
    address: 10.44.0.3
    public_address: 203.0.113.13
    ssh: { keys: [alice, bob] }
```

`site-a` is unchanged. `etcd.members` and `cluster.sites` stay out, so the voters derive to `site-a`, `site-b` and `witness`, and the cluster to `site-a` and `site-b`.

- [ ] **Step 2: Validate.**

```sh
paisans validate
```

Expected: no refusal. Warnings expected: `monitor-shares-a-site`, `backups-off`. Not expected: `two-etcd-voters`, `voters-share-a-relay`, `async-automatic-failover`.

### Task A2: Does `site-a` need its owner again?

The README's *Changing what you granted* (host owners' guide) says a change of the site's SSH port, WireGuard port, user, endpoint or mesh subnet needs a fresh request. Growth changes none of those for `site-a`. The code agrees: `admission.Record.Differences` (`internal/admission/record.go`) compares the site name, user, SSH port, `ports.wireguard` against `Site.ListenPort()`, the mesh subnet, forwarding against the presence of an `endpoint`, the grants the roles need, and the web server. It does not compare roles. `ListenPort()` follows from `DialEndpoint()`, which a `public_address` alone makes `<public_address>:51820` at one site as at three, and `site admit` opened that port at A0 (`hostprep.AdmitRules` with the request's `listen_port`). The mesh file rendered at one site already carries `ListenPort = 51820` and an empty peer list (`wireguard.conf.tmpl`).

- [ ] **Step 1: Confirm no re-admission is needed.**

```sh
paisans site prepare --site site-a
paisans preflight --site site-b
```

Expected: `site prepare` plans nothing and names no `site request` line; preflight's `Admission` row for the new sites is the only admission finding, and nothing about `site-a`'s record.

- [ ] **Step 2: Confirm the negative.** On a copy of the configuration, give `site-a` an `endpoint: ":51821"` and run `paisans site prepare --site site-a --config <copy>`. Expected: refused, naming the WireGuard port (51821 against 51820) and forwarding, with the `paisans site request --site site-a > site-a.admit` and `sudo paisans site admit --request site-a.admit` lines. Discard the copy.

### Task A3: Admit the new hosts

- [ ] **Step 1: Admit both.**

```sh
paisans site admit --site site-b --ssh ubuntu --watchdog --execute --yes
paisans site admit --site witness --ssh ubuntu --execute --yes
```

Expected: `site-b` granted the watchdog and not public ports (a `[data, apps]` site runs no Caddy); `witness` granted neither, and admit refuses `--watchdog` or `--public-ports` on it if given, since "a grant the site does not need stops it too". Each record has `ports.wireguard` 51820.

- [ ] **Step 2: Preflight.**

```sh
paisans preflight --site site-b
```

Expected: every row `ok` for both joining sites (RunAll checks the witness joining beside `site-b` as a blank host), exit 0. A `Prepared` refusal at this point is expected only if `site prepare` has not run; the whole apply runs it first, so record it and go on.

### Task A4: Converge

- [ ] **Step 1: Dry run.**

```sh
paisans apply
```

Expected, in order: `init` with work (WireGuard keys and secrets for `site-b` and `witness`); `site prepare` on all three; the pull on `site-b` and `witness`; `dns init` (record what it adds: the gateway on `site-a` still answers for every hostname); `site add site-b` with the witness joining in its etcd stage first; the other sites; pass two. The review lists the new sites' keys and images.

- [ ] **Step 2: Apply.**

```sh
paisans apply --execute --yes
```

Expected: `site add` passes all eight gates in order and the run ends `Deployment complete.`, with `site-b`'s apps started and the gateway's routes gaining `site-b`. Failure signal: `site add site-b: the site's roles must be exactly [data]` (#393 not merged), or any gate failing; record the stage and resume with the same command after a fix, which `site add` must do from the first unmet gate.

### Task A5: Checks

- [ ] **A5.1 Data survived.** Re-run Task A0 Step 6 against `<site-a>` and against `<site-b>` (a replica answers reads). Expected: identical counts and digest on both, equal to the baseline. Then sign in to `id.example.org` with each operator's and the member's existing passkey, and open the wiki page. Failure signal: any count or digest differs, or a passkey is refused.

- [ ] **A5.2 etcd has three voters.**

```sh
on <site-a> "$(infra etcd) etcdctl --endpoints=http://10.44.0.1:2379 member list -w table"
on <site-a> "$(infra etcd) etcdctl --endpoints=http://10.44.0.1:2379,http://10.44.0.2:2379,http://10.44.0.3:2379 endpoint health -w table"
```

Expected: three members, none `isLearner`, all healthy. Failure signal: a learner left, or two voters.

- [ ] **A5.3 Patroni has a synchronous replica.**

```sh
on <site-a> "$(infra patroni) patronictl -c /home/postgres/postgres.yml list"
```

Expected: `site-a` `Leader` `running`, `site-b` `Sync Standby` `streaming`, lag 0. Then write and read back across sites:

```sh
psql_on <site-a> paisans_canary "INSERT INTO canary (phase) SELECT 'after-growth' FROM generate_series(1, 100)"
psql_on <site-b> paisans_canary "SELECT count(*) FROM canary WHERE phase = 'after-growth'"
```

Expected: 100 on `site-b` at once.

- [ ] **A5.4 The gateway routes to `site-b`.** On `<site-a>`, `grep -n -A4 reverse_proxy $R/infra/caddy/Caddyfile` lists both sites' mesh addresses for `account` and `wiki` (and `auth`). Then stop each on `site-a` and reach it through the gateway:

```sh
on <site-a> "docker compose -f $R/wiki/compose.yaml stop"
curl -sS -o /dev/null -w '%{http_code}\n' https://wiki.example.org/
on <site-b> "docker compose -f $R/wiki/compose.yaml logs --since 1m | tail -5"
on <site-a> "docker compose -f $R/wiki/compose.yaml start"
```

Expected: an answer under 500 (a redirect to sign-in counts), and the request in `site-b`'s log. Repeat for `account`.

- [ ] **A5.5 Pocket ID: one active, one standby.** The apply's output carries `pocket-id auth: one active instance` with one site `active` and the other `standby`. Then hand over:

```sh
on <site-a> "docker compose -f $R/auth/compose.yaml stop"     # if site-a is the active one; else stop it on site-b
paisans doctor
```

Expected: within about 15 seconds of a clean stop the other site is `active` (README: about two seconds at a 3 s retry; the default retry is 15 s), sign-in with a passkey works through the gateway, and `doctor`'s Pocket ID row names the new active site. Start the stopped stack again with `paisans apply --site <that site> --only auth --execute --yes`; expected: it comes back `standby`.

- [ ] **A5.6 The monitor knows the new sites.** Stage 8 reseeded the `health` stack: in the uptime UI, `site-b` and `witness` have their ping and direct checks, and they are green.

- [ ] **A5.7 `doctor` is clean.**

```sh
paisans doctor
```

Expected: exit 0, no `FAIL`; the Reach, Admission, rootless, etcd, Patroni, containers, Pocket ID, clocks and Leftovers rows `ok` on all three sites. Any `WARN` is recorded with its text.

- [ ] **A5.8 Failover to the new site, planned.**

```sh
paisans failover test            # the checks and both switchover commands
paisans failover test --execute
```

Expected: both switches pass their gates; between them `site-b` leads, the apps answer through the gateway, and Pocket ID still has exactly one active instance.

- [ ] **A5.9 Failover to the new site, unplanned.** Kill the leader's Patroni without letting it release its lock:

```sh
on <site-b> "$(infra patroni) patronictl -c /home/postgres/postgres.yml list"   # confirm site-a leads
on <site-a> "docker compose -f $R/infra/compose.yaml kill patroni"
```

Expected: the watchdog, which the killed Patroni held open and never closed, reboots `<site-a>` within its timeout (Patroni's default `ttl` 30 less `safety_margin` 5, so about 25 seconds); `site-b` is promoted once the leader key expires; the public hostnames go dark with `<site-a>`, which holds the gateway, until it is back. After the reboot: `site-a` rejoins as a replica and `Sync Standby`, the gateway answers, and A5.1's counts plus every row written during the outage are present on both sites. Write during the outage directly on `site-b`:

```sh
psql_on <site-b> paisans_canary "INSERT INTO canary (phase) SELECT 'during-failover' FROM generate_series(1, 50)"
```

Record: time to promotion, time to the host's return, time until every app answers, and whether any app needed a restart to reach the new leader (`docker compose ... logs` for connection errors). Failure signal: two leaders at any moment in `patronictl list`, `site-a` coming back as a second leader, or lost rows. Finally `paisans failover test --execute` again, which leaves `site-a` leading.

- [ ] **A5.10 A re-run changes nothing.**

```sh
paisans apply
paisans apply --execute --yes
```

Expected: the dry run's `install` section reads `up to date` for every site, the review lists nothing, and the executed run touches no stack (no `recreate`, `restart` or `write` lines), ending `Deployment complete.`. Failure signal: any planned step.

---

## Run B: backup and restore under rootless

### What the toolkit backs up, and how, as designed

Nothing is built past the schema. README *Backup and restore* lists what `paisans backup` must produce (the declaration, the secrets, the databases with their write ahead log, object storage, and the age keys kept apart), and *Backups are declared in `paisans-stack.yaml`, and may be added later* the `backup:` section and its `validate` rules. `docs/commands.md` has no `backup` command. The design is `docs/specs/2026-10-11-backups.md`:

* **The cluster's Postgres:** WAL-G as Spilo bundles it, turned on by `WALG_S3_PREFIX` and the destination's credentials rendered into `patroni.env`. It runs inside the Spilo container on the leader only, archiving WAL continuously and taking delta base backups on the declared schedule; a promoted replica takes over.
* **A pinned app's Postgres:** WAL-G in `ghcr.io/paisans-software/postgres-walg:<major>`.
* **Everything else:** a runner, `paisans-backup`, in the infrastructure stack on every site, with restic (pinned apps' files, SQLite copies, a mirror of the Garage buckets) and the configuration store as an age encrypted `git bundle`.
* **Where:** each declared S3 destination, under `<prefix>/<deployment id>/pg/cluster/`, `pg/<app>/`, `restic/` and `config/`. The first destination takes the live stream; `wal-g copy` feeds the others.
* **As whom:** inside the deployment's containers, so as the deployment's user's subordinate UIDs on the host, and out through the egress table.
* **Restore:** `paisans backup restore` for four scenarios (one app's data bad, the cluster lost with the sites standing, a pinned app lost, everything lost), Spilo's `CLONE_WITH_WALG` with `CLONE_TARGET_TIME` for the cluster, so point in time recovery is in scope; and `paisans backup drill`, which restores into throwaway containers.

Tasks B1, B2 and B9 run today (B9 needs #393). B3 to B8 wait for the backup phases and are written against the spec, with its provisional command names.

### Task B1: Declare backups on the running deployment

- [ ] **Step 1: Make the destination.** On `<s3>`, a bucket `staging-backups` and a key with read, write, list and delete on it alone, at `https://s3.example.org`.

- [ ] **Step 2: Add the section.**

```yaml
backup:
  schedule: daily
  at: "03:00"
  destinations:
    - name: primary
      endpoint: https://s3.example.org
      region: garage
      bucket: staging-backups
      keep: { daily: 7, weekly: 4, monthly: 6, yearly: 1 }
```

- [ ] **Step 3: Validate, init, owe the credentials.**

```sh
paisans validate
paisans init
paisans secrets list
paisans secrets set backup.destinations.primary.access_key_id < key-id-file
paisans secrets set backup.destinations.primary.secret_access_key < secret-file
```

Expected: `validate` warns `backup-single-destination` and nothing else new; `backups-off` is gone. `init` generates `backup.restic_password` and `backup.walg_libsodium_key` and prints neither; `secrets list` shows the two destination keys owed until set.

### Task B2: Apply

- [ ] **Step 1:**

```sh
paisans apply
paisans apply --execute --yes
```

Expected today: nothing about backups changes on any host, since nothing renders it; the run ends `Deployment complete.` and A5.10's re-run still plans nothing. Expected once phases 2 and 3 land, per the spec's *Turning backups on later*: `patroni.env` gains the WAL-G variables, each Patroni is recreated one member at a time with the replica first and the leader's change following the rule for `patroni.env` on a leader (README, *A replica's `patroni.env` is applied; the leader's waits*), only `archive_command` changes, and the runner starts on every site. Observe:

```sh
on <site-a> "grep -c '^WALG_\|^AWS_' $R/infra/patroni.env; stat -c '%a %U' $R/infra/patroni.env"
psql_on <site-a> postgres "SHOW archive_command"
psql_on <site-a> postgres "SELECT last_archived_wal, last_failed_wal, last_failed_time FROM pg_stat_archiver"
```

Expected: the variables present, the file readable by the deployment's user alone, `archive_command` naming `wal-g`, and `last_archived_wal` advancing with no `last_failed_wal`. Failure signal: archiving failures, which on rootless first means the egress table (B8).

### Task B3: Write known data, then take a backup

- [ ] **Step 1:** Insert a marked batch and record the time:

```sh
psql_on <site-a> paisans_canary "INSERT INTO canary (phase) SELECT 'before-backup' FROM generate_series(1, 1000)"
date -u +%FT%TZ          # T0
```

Also create a second wiki page and a second member with a passkey, recorded by name.

- [ ] **Step 2:**

```sh
paisans backup run --execute
paisans backup status
```

Expected: a base backup of the cluster under `pg/cluster/`, a restic snapshot of `health`'s SQLite and the configuration bundle under `config/`, each with its time and size, and archive lag near zero.

### Task B4: Verify

- [ ] **Step 1:**

```sh
paisans backup verify --execute
```

Expected: `restic check --read-data-subset` and `wal-g wal-verify integrity` pass. Then list the destination directly (`<s3>`'s own client) and confirm the four prefixes and that every object is encrypted (no plaintext SQL or YAML in any object).

- [ ] **Step 2: Drill.**

```sh
paisans backup drill
```

Expected: the drill restores into throwaway containers, runs its per app queries, reports, and removes them, touching no live site.

### Task B5: Delete rows, restore to a point in time

- [ ] **Step 1:** Write more, record T1, then destroy:

```sh
psql_on <site-a> paisans_canary "INSERT INTO canary (phase) SELECT 'after-backup' FROM generate_series(1, 500)"
date -u +%FT%TZ          # T1
psql_on <site-a> paisans_canary "DELETE FROM canary WHERE phase IN ('before-backup', 'after-backup')"
psql_on <site-a> auth "DELETE FROM webauthn_credentials WHERE user_id = (SELECT id FROM users WHERE username = '<second member>')"
```

- [ ] **Step 2:** Restore the one database through a scratch instance, at T1 (the spec's *One app's data is bad*):

```sh
paisans backup restore ...        # Open: B-command, the flags for one database and a target time
```

Expected: `paisans_canary` holds the baseline, `before-backup` and `after-backup` rows, exactly, and the second member signs in with their passkey again. A restore with a target time between T0 and T1 brings back exactly the rows written before it: this is the point in time check, since T1 is after the base backup and needs the archived WAL. Then `patronictl list` as A5.3, and `paisans doctor` exit 0.

### Task B6: Lose the cluster, keep the sites

- [ ] **Step 1: Destroy.** On both data sites, stop Patroni and remove its volume, as the deployment's user:

```sh
for h in <site-b> <site-a>; do on $h "docker compose -f $R/infra/compose.yaml stop patroni"; done
for h in <site-a> <site-b>; do on $h "docker compose -f $R/infra/compose.yaml rm -f patroni && docker volume ls -q | grep postgres"; done
# then docker volume rm the listed volume on each
```

Expected: every app fails its database checks, `doctor` reports Patroni without a leader.

- [ ] **Step 2: Restore** the cluster from the backup (*The cluster is lost, the sites are not*):

```sh
paisans backup restore ...        # Open: B-command
```

Expected: one site bootstraps through `CLONE_WITH_WALG` to the newest point the archive holds, the other rejoins as `Sync Standby`, the apps reconnect (or are restarted by the restore, as `failover test` restarts them), and every row recorded at B5's end is present.

- [ ] **Step 3: Rootless ownership.** On the bootstrapped site, `on <site-a> "ls -ln \$(docker volume inspect -f '{{.Mountpoint}}' <volume>)"`. Expected: the data directory owned by a UID inside the user's subordinate range, the one Spilo's `postgres` maps to, not by the user's own UID and not by 0. Failure signal: Postgres refuses to start with `data directory has wrong ownership`.

### Task B7: Restore everything onto freshly admitted hosts

The spec's *Everything is lost*: the configuration recovered from the destination's `config/` bundle, then `apply` to new hosts with databases bootstrapped from the backup.

- [ ] **Step 1:** Make three fresh hosts with the shape of `site-a`, `site-b` and `witness`, on new public addresses. On an empty workstation directory with only the age key, fetch and decrypt the bundle and recover the configuration (`Open:` the command), then set the new public addresses in `paisans-stack.yaml`.

- [ ] **Step 2:** Admit each, as in A0 Step 2 and A3, and converge:

```sh
paisans site admit --site site-a --ssh ubuntu --watchdog --public-ports --execute --yes
paisans site admit --site site-b --ssh ubuntu --watchdog --execute --yes
paisans site admit --site witness --ssh ubuntu --execute --yes
paisans apply --execute --yes      # Open: B-everything, how apply is told to bootstrap from the backup
```

Expected: the deployment runs with the data of B6's end, every passkey signs in, `health`'s history is back from restic, and `doctor` exits 0. Then A5.10.

### Task B8: What rootless can break, each observed

- [ ] **B8.1 A destination the egress table drops.** Add a second destination whose endpoint is a private address (a Garage on the same LAN as a site, or `http://127.0.0.1:3900` on `<site-a>`), apply, and run a backup. Expected from the table (`internal/hostprep/snippets/egress.nft.tmpl`): every connection from the user and its subordinate range to `127.0.0.0/8`, `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `169.254.0.0/16`, `100.64.0.0/10` or the host's own addresses is dropped, so the run fails for that destination alone and reports it naming the destination, while the primary succeeds. Failure signal: a run reported `ok`, or the primary failing with it.

- [ ] **B8.2 A destination named through a resolver the host does not use.** The table accepts DNS only to the resolvers in `/etc/resolv.conf` and `/run/systemd/resolve/resolv.conf` at admit time. Expected: a name resolvable only elsewhere fails resolution in the container, and the report says so.

- [ ] **B8.3 A destination on the mesh.** The table accepts `mesh.subnet` for the user, so an S3 endpoint at another site's mesh address passes. Record whether any configuration makes that possible for a destination that is not a site.

- [ ] **B8.4 Credentials.** On each host, the rendered `.env` holding destination keys is readable by `paisans-<token>` and root alone (mode 600 or the directory's 750), and no command line on the host carries a key (`ps -eo args` as root during a run).

- [ ] **B8.5 Disk.** The runner refuses to stage with free space under twice the largest staged item. On `<witness>` (20 GB), record the free space the runner reports.

### Task B9: Lose a whole data site's host, and replace it

This path exists today and needs no backup: the surviving data site holds every row.

- [ ] **Step 1: Lose it.** Power `<site-b>` off at the provider. Expected: `paisans apply` runs degraded with `site-b` down, `doctor` warns naming `paisans site remove --site site-b --host-gone`, and writes continue on `site-a`.

- [ ] **Step 2: Remove it.**

```sh
paisans site remove site-b --host-gone
paisans site remove site-b --host-gone --execute --yes
```

Expected per *`site remove` takes a site out*: two data sites and a witness go to one voter, so the witness's member and its `witness` role leave in the same change; `site-b`'s block leaves `paisans-stack.yaml`; DNS records for `site-b`'s address are deleted.

- [ ] **Step 3: Replace it.** Declare `site-c` (`[data, apps]`, a new host) and give `witness` its role back in one edit, admit `<site-c>` as A3 does, then `paisans apply --execute --yes`. Expected: A5.1 to A5.7 pass with `site-c` in `site-b`'s place.

- [ ] **Step 4 (needs #394): The same host again.** Instead of a new host, bring `<site-b>` back, have its owner run `sudo paisans site evict --deployment <id> --user paisans-<token> --execute`, then admit it again as `site-c`. Expected: admit runs without the owner starting the system Docker, and the host keeps no running system Docker afterwards.

---

## Open

* **Open: #393 and the docs.** README *`site add` is built for sites that dial each other* and `docs/commands.md` *`paisans site add`* tell the operator to join a site as `[data]` and give it `apps` afterwards. Run A follows one `apply` with `[data, apps]`, which #393 makes work. Decide whether #393's pull request rewrites those passages, or whether `site add` alone keeps the two-step rule and only `apply` joins a `[data, apps]` site.
* **Open: growth from a gateway on a data site.** README *Adding the gateway and the witness* starts from a gateway VM already running, and the dual-site staging shape keeps the gateway off the data sites so a lost data site leaves the hostnames up. Starting with everything on one host, the docs do not say whether the production-shaped path also moves the gateway to the witness host (`gateway move`, which needs both hosts re-admitted for public ports). Decide whether Run A adds that move as a final task.
* **Open: no re-admission on growth is undocumented.** The code needs none for `site-a` (Task A2), but no passage in README's growth sections says the existing host's owner is not needed. Decide whether *Adding a site, and the constraint that shapes it* says so, and that an `endpoint` added at growth does need the owner.
* **Open: apps after an unplanned failover.** `failover test` restarts every database app after a switch because some never reconnect; an unplanned failover (A5.9) restarts nothing. Decide whether Pocket ID, the account service and Wiki.js must answer after one without a restart, and what the docs tell an operator to do if one does not.
* **Open: a watchdog reboot of the gateway.** Killing the leader's Patroni on `site-a` reboots the host that also holds the gateway. README's watchdog section says a leader that stops renewing is rebooted, and says nothing of the timing or of the gateway beside it. Decide whether A5.9 kills Patroni (and accepts the outage) or stops it cleanly.
* **Open: what `doctor` clean means.** `doctor` exits 0 with warnings. Decide whether any `WARN` fails A5.7, and which warnings this shape is expected to show.
* **Open: backups are not built.** Run B's B3 to B8 cannot run until the backup spec's phases 2 to 6 merge. Decide whether the first production release waits for them, or ships with backups off and Run B's backup half as a gate for a later release.
* **Open: B-command.** The spec names `paisans backup status | run | verify | restore | drill` as provisional and gives no flags: how a restore chooses its scenario, the database or app, the destination and the target time. Settle them in phase 6 and fill B5, B6 and B7.
* **Open: B-everything.** The spec's *Everything is lost* gives no order between recovering the configuration from a destination, changing public addresses, the owners' `site admit`, and an `apply` that bootstraps from the backup rather than founding an empty cluster. Decide how `apply` is told to bootstrap from a backup.
* **Open: stale cluster state in etcd.** Restoring a lost cluster through `CLONE_WITH_WALG` into a cluster whose etcd still holds the old scope's leader and member keys, or a new etcd, is not covered by the spec. Decide which, and who clears the old keys.
* **Open: private and loopback destinations.** The egress table drops a destination on a private, link-local or loopback address, or on the host's own address (B8.1). Neither `validate` nor the spec says so. Decide whether `validate` warns on an endpoint that resolves into those ranges, and document it in README's backup section and the host owners' guide.
* **Open: a home backup target.** The spec's home-hardware Garage is "one more S3 endpoint", but on a LAN shared with a site the egress table drops it, and a machine that is not a site has no mesh address. Decide how such a target is reached.
* **Open: restores and subordinate UIDs.** WAL-G inside Spilo writes as the container's `postgres`, which maps into the user's subordinate range. The spec does not say where a restic restore of a pinned app's files or Uptime's SQLite runs; written from the host as the deployment's user, they would be owned by UID 0 inside the container. Decide that every restore writes from inside the owning container's user namespace, and test it in B6 Step 3's form for files.
* **Open: object storage in Run B.** README's backup table includes object storage, and the spec mirrors Garage buckets through `rclone`. These runs declare no `storage` site, so nothing tests it. Decide whether Run B adds a Garage site and an app that writes objects.

## Blockers

* **Run B's backup tasks are impossible today.** No code renders WAL-G's environment, no runner exists, and there is no `paisans backup` command (`docs/commands.md` lists none; README: "nothing runs a backup yet"). B1, B2 and B9 can run; B3 to B8 wait for the backup spec's phases 2 to 6.
* **Run A is impossible until #393 merges**, as written with `site-b` declared `[data, apps]`. The documented two-step path (`[data]`, then `apps`) runs today and is the fallback if the founder wants Run A before #393.
