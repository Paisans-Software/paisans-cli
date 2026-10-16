# Rootless Phase 9: Documentation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The documentation describes the toolkit as phases 1 to 7 left it: a guide for host owners, `docs/guides/host-owners.md`, from downloading and checking the binary to evicting; `docs/commands.md` for `host request`, `host admit`, `host evict` and every command whose behaviour the rootless work rewrote; `README.md`'s sections on what `host admit` and `host prepare` do, on the deployment's user's keys and on Docker being judged by its version, and every other passage that still gives `host prepare` a root step; `docs/deployment-agent-rules.md` saying admit and evict are owner actions; and, last, doctor's checks as phase 7 lands them.

**Architecture:** Documentation only; no code, no test, no `CHANGELOG.md` bullet (the index: "`CHANGELOG.md` bullets in the PRs that change behaviour (phases 1 to 8), not here"). Each task rewrites one document's passages in place, from the merged code: it starts by checking the facts it states against `develop` with the commands it lists, and where the code says otherwise the text follows the code. `README.md` keeps the reasoning, `docs/commands.md` the mechanics, the guide the owner's path; each links to the others by section name. The guide comes first because the others link to it; doctor's task comes last because it describes phase 7, which runs alongside this phase (founder decision).

**Tech Stack:** Markdown; `grep`, `perl` for the em dash check, `go run ./cmd/paisans <command> --help` for each command's flags, `make release-binaries` for the guide's checksum step.

**Spec:** `docs/specs/2026-10-15-rootless-and-host-admit.md` (all of it; *Documentation* lists this phase), the phase index `docs/superpowers/plans/2026-10-15-rootless-and-host-admit.md` (*Phase 9*, read beside *Phases 5, 6 and 7*), phase 5's plan (`docs/superpowers/plans/2026-10-15-rootless-phase-5-host-admit.md`: its *Rulings*, *Founder decisions on the plan's questions* and Task 15, which wrote the first `host admit` and `host evict` entries), and phase 7's plan (`docs/superpowers/plans/2026-10-15-rootless-phase-7-doctor.md`, *What this phase hands to phase 9*).

## Global Constraints

* "`docs/commands.md`: `host request`, `host admit`, `host evict`; `host prepare` and every former `--sudo` rewritten; `host allow`/`disallow` gone. `README.md`: the sections on `host prepare`, the login user's keys and Docker's version. `docs/guides/host-owners.md`: install and check the binary, compare the digest, what admit changes, each grant, evict. `docs/deployment-agent-rules.md`: admit and evict are owner actions." (index, *Phase 9*)
* "`host admit` and `host evict` are owner actions; `docs/deployment-agent-rules.md` says an agent never runs them unattended." (index, *Global Constraints*)
* "Depends on: phases 5 and 6; runs alongside phase 7 (founder decision), and documents doctor's checks as phase 7 lands them." (index) Doctor's task is last.
* Docs describe current state only: no "no longer", "used to", "previously", "now" contrasting with a past, no "was". No rejected alternatives outside `docs/decisions.md`. No em dashes in any line this phase writes; existing em dashes are swept only in lines a task already touches (`AGENTS.md`).
* Every flag a document names is one `cmd/paisans` defines; every internal link resolves; every fenced YAML block parses; no real hostname, address, key or secret (`AGENTS.md`, *Verifying before you claim it is done*). Examples use `example.org`, `home-a`, `home-b`, `vm`, and the documentation ranges `203.0.113.0/24`, `198.51.100.0/24`.
* "Deleting documentation requires explicit human approval. Rewriting a section in place is an edit; removing it is a deletion." (`AGENTS.md`) Every passage this phase changes is rewritten in place; see founder question 2 for the rows and sentences whose subject is gone.
* "A tier change is a human decision. Propose it; do not decide it." (`AGENTS.md`, on `docs/deployment-agent-rules.md`) Task 7 writes what the founder decided and proposes the rest (founder question 1).
* Commits: `<type>: <lowercase subject>`, a body saying why, `Founder decision.` where it was one, the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Every commit here starts `kacl-skip: docs:`, since nothing an operator runs changes.
* Never `git stash`; other sessions share the stash. Never push.
* There is no test suite for documentation; each task's checks are run, not asserted, and the full `go vet ./... && go test ./...` runs once at the end, to show nothing outside the docs moved.

## Review Focus

The index names no Review Focus line for this phase. The five below are what a reader of these documents would meet that no task's own text proves, most likely first; each is checked by a step in the task named.

* **An owner copies the guide's commands onto an arm64 host, or for a release the operators named.** Every command in the guide's install block must run as written on Ubuntu 24.04 with only `curl` and coreutils, and the checksum line must accept `SHA256SUMS` as the release workflow writes it (`shasum -a 256` format). Task 1, Step 4 runs the block against `make release-binaries`'s own `dist/`.
* **An owner compares the digest from the same email that carried the request.** That checks nothing: anyone who could change the file could change the digest beside it. The guide, the `host admit` entry and the agent rules must each say the digest is compared over a different channel the owner trusts (Task 1, Step 3; Task 2, Step 2; Task 7, Step 2).
* **A flag named in a document that the command does not take.** The guide and both commands entries name ten flags of `host admit` and `host evict` between them; each must be one `runHostAdmit` or `runHostEvict` defines, spelled as it does (Task 2, Step 1; Task 1, Step 4).
* **`README.md` saying an owner's command may run unattended while the agent rules say it never does.** *A sudo that wants a password is asked for once, on a terminal* tells the operator that "an unattended run of an owner's command needs a NOPASSWD sudoers rule"; with `host admit` and `host evict` among the owner's commands, that sentence must say they ask for a typed digest or name at a terminal and cannot run unattended (Task 5, Step 4; Task 7, Step 3).
* **A passage left saying `host prepare` installs Docker, opens a port, writes a root file or arms the watchdog.** Each sentence that gives `host prepare` a root step is wrong for every rootless deployment, and an operator who reads it will expect a prepare to fix what only the owner can. Task 6's sweep greps for each such phrase and expects none left (Task 6, Step 3).

## Rulings

1. **Phase 5 wrote the first `host admit` and `host evict` entries** (its Task 15) **and the agent rules' tier bullet.** This phase keeps them, checks them against the merged flags, and adds what they lack: the guide link, the digest's channel, and each grant's cost; it rewrites them only where the merged code differs.
2. **Each task checks its facts against `develop` before writing**, with the commands in its first step. Where phase 5's or phase 6's merged code differs from what this plan's text says, the text follows the code, and the commit body names the difference. The plan's text comes from the spec, the index and phase 5's rulings, since phase 6's plan did not exist when this was written.
3. **The guide is the owner's whole path; `README.md` is the reasoning; `docs/commands.md` is the mechanics.** The guide repeats no reasoning beyond a sentence per grant and links to `README.md` for the rest.
4. **`README.md`'s host sections are restructured, in place.** A new `### host admit gives a deployment a user of its own, once, as root` takes the subsections whose subject is now admit's (operating system profiles, Docker from Docker's repository, the firewall following roles, the ufw rules' ownership, the watchdog, the boot order), moved from under `host prepare` and rewritten; `### host prepare takes an admitted host to an apply-able state` keeps the configuration store and the keys. Each moved subsection keeps its heading's subject, so cross references by name still find it; Task 6 fixes every reference whose name changed.
5. **Doctor's documentation is the last task**, written from phase 7's merged code, and covers `README.md`'s doctor section, `docs/commands.md`'s `paisans doctor` entry and `docs/development.md`'s graph paragraph.
6. **Every commit is `kacl-skip: docs:`.** Documentation changes nothing an operator runs; the behaviour each describes carried its own `CHANGELOG.md` bullet in its own phase.
7. **The guide's examples name no real release.** It uses `v=0.3.0` with a comment saying to use the release the operators name; `0.3.0` is the next minor after `internal/version`'s `0.2.0`, which no example depends on.

## Founder questions

1. **The agent rules' new lines** (Task 7, Step 2). The founder decided admit and evict are owner actions an agent never runs unattended, and phase 5 put them in *Never without the human present*. This plan also proposes, as `AGENTS.md` says tier content is proposed: under *A human decides*, "whether a host takes a deployment, and what its owner grants it"; and three sentences under the tier: an agent never answers admit's digest or evict's name prompt, never writes a sudoers rule that would let either run unattended, and never sends a request's digest by the channel that carried the request. Approve, amend or drop each; Task 7 lands only what is approved.
2. **Rows and sentences whose subject is gone.** Rewriting in place leaves some text with nothing to describe: the firewall table's four `br-+` rows (phase 5: "The `br-+` rules go"), the `docker.service.d/paisans-<token>-after-wireguard.conf` drop-in (phase 6: `host prepare` installs nothing), the host-wide `flock /etc/paisans/authorized_keys.lock` and the root-owned sidecars under `/etc/paisans` (phase 6 moves the sidecar into the deployment's directory). Removing those is a deletion under `AGENTS.md`. This plan removes them as part of each rewrite (Tasks 5 and 6) and asks for approval here; a refusal keeps them, marked as describing nothing the toolkit does, which the current-state rule forbids, so the question is whether to delete.
3. **How an owner trusts the binary itself.** `SHA256SUMS` proves a download is whole and is the file the release lists; it comes from the same release page, so it proves nothing about who published the release. The guide says so (Task 1). Whether releases should carry a signature or a GitHub artifact attestation an owner can check is a release decision, outside this phase.
4. **Who closes an old gateway's 80 and 443 after `gateway move`.** `README.md`, *It is a config change*, has `host prepare` keep the old gateway's allows until the move is done and then remove them. Under phase 5 the allows are `host admit`'s, granted by `--public-ports`, and only the owner can take them away (a re-run without the grant). The spec has no rule for it. Task 6 describes what phase 6's merged `gateway move` does; if it leaves the old allows to the owner, the text says the owner re-runs `host admit` without `--public-ports` once DNS has moved.

## Interfaces this phase documents

From phase 5 (merged before this phase starts):

| Name | Where |
|---|---|
| `host admit` flags: `--request <file>`, `--site <name>`, `--ssh <login>`, `--config`, `--watchdog`, `--public-ports`, `--data-dir <path>`, `--bridge-netfilter`, `--mesh-lan`, `--execute` | `runHostAdmit`, `cmd/paisans/hostadmit.go` |
| `host evict` flags: `--request <file>`, `--deployment <id>`, `--user <name>`, `--site <name>`, `--ssh <login>`, `--config`, `--execute` | `runHostEvict`, same file |
| the Engine floor `29.5.0` | `hostadmit.MinRootlessEngine`, `internal/hostadmit/docker.go` |
| the privileged groups | `hostadmit.PrivilegedGroups`, `internal/hostadmit/user.go` |
| the record path and mode, `/etc/paisans/admitted.<token>.json`, 0644 | `admission.Path`, `admission.Mode` |
| the helper and its sudoers file, `/usr/local/libexec/paisans/wg-psns-<token>`, `/etc/sudoers.d/paisans-<token>` | `apply.WGHelper.Path`, `sudoersPath` |
| the egress table and unit, `inet paisans_<token>`, `paisans-<token>-egress.service` | `deployment.EgressTable`, `deployment.EgressUnit` |
| the sysctl file and its record, `/etc/sysctl.d/60-paisans.conf`, `/etc/paisans/sysctl.before` | phase 5 ruling 7 |

From phase 6 (merged before this phase starts; its plan did not exist when this was written, so each is checked in the task that names it): `host prepare` installs nothing and compares the record with the declaration; the deployment's state under `/srv/paisans/<token>/.paisans/` (the deployed record, the grant record, the configuration store, the keys' sidecar `authorized_keys.owned`); the configuration store's user units; `host deployments` listing admission records; `site remove` ending with the `host evict` line; `apply`'s `pending` section, its `Applied, degraded` ending and `--require-all`; the gateway's `/.well-known/paisans/<token>` check.

From phase 7 (Task 8 only; *What this phase hands to phase 9* in its plan): the nodes `admission`, `user`, `docker`, `docker engine`, `linger`, `sudo rights`, `gateway answer`; `doctor.UserCommand`, `DockerCommand`, `LingerCommand`, `SudoCommand`; pending and down as `WARN`, a gateway as `FAIL`; the boot advice.

---

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `docs/guides/host-owners.md` | create | the owner's path: install and check, read and compare, grant, admit, check, the web server hand-off, Caddy's admin API, change, evict |
| `docs/commands.md` | modify | *Hosts* (`host request`, `host admit`, `host evict`, `host prepare`, `host deployments`), the shared flags and their paragraphs, *Applying*, `site remove`; last, `paisans doctor` |
| `README.md` | modify | *What it does*'s command block; *Docker is judged by its version, not its source* and *The host check*'s shared-host list; a new `host admit` section with the moved subsections and *Egress*; `host prepare`'s section, its configuration store and keys; *A sudo that wants a password*; the sweep of `host prepare`'s root steps; last, doctor |
| `docs/deployment-agent-rules.md` | modify | admit and evict as owner actions |
| `docs/development.md` | modify | last, doctor's graph paragraph |

---

### Task 0: Bring the branch up to `develop`

**Files:** none.

**Interfaces:**
- Consumes: `develop` with phases 5 and 6 merged.
- Produces: a branch whose docs describe what is merged.

- [ ] **Step 1: Rebase**

The branch holds only this plan's commit.

Run: `git fetch origin && git rebase origin/develop`
Expected: the plan's commit replays cleanly.

- [ ] **Step 2: Confirm phases 5 and 6 are in**

Run: `ls cmd/paisans/hostadmit.go internal/hostadmit/ && grep -n "func runHostAdmit\|func runHostEvict" cmd/paisans/hostadmit.go`
Expected: both functions. Without them, stop: this phase documents merged code.

Run: `grep -rn "require-all" cmd/paisans/*.go | grep -v _test.go | head -3`
Expected: phase 6's flag. Without it, phase 6 is not merged; stop.

---

### Task 1: The host owners' guide

**Files:**
- Create: `docs/guides/host-owners.md`

**Interfaces:**
- Consumes: the release assets `paisans-linux-amd64`, `paisans-linux-arm64`, `SHA256SUMS` (`Makefile`, `release-binaries`; `.github/workflows/release.yml`); `host admit` and `host evict`'s flags (*Interfaces this phase documents*); phase 5's rulings 4, 7, 8, 14, 15, 18, 25, 30 and founder decision 2.
- Produces: `docs/guides/host-owners.md`, with the headings `## What you need`, `## 1. Install paisans and check it`, `## 2. Read the request and compare its digest`, `## 3. Decide what to grant`, `## 4. Admit`, `## What admitting changes on your machine`, `## Behind your own web server`, `## If you run Caddy`, `## Changing what you granted`, `## Evicting`, which Tasks 2, 5 and 7 link to.

- [ ] **Step 1: Check the facts the guide states**

Run: `go run ./cmd/paisans host admit --help 2>&1; go run ./cmd/paisans host evict --help 2>&1`
Expected: every flag in *Interfaces this phase documents*. A flag spelled otherwise is spelled the code's way in Step 3's text.

Run: `grep -n "ufw.*not installed\|apt-get install.*--no-upgrade\|MinRootlessEngine\|nfs\|cifs\|fuse\|20 GB\|5 GB\|sysctl.before\|caddy-admin\|2019" internal/hostadmit/*.go internal/hostprep/admit.go | grep -v _test.go`
Expected: admit stops without ufw (phase 5 founder decision 2); `--no-upgrade`; the floor; the refused filesystems; the free-space thresholds; the sysctl record; the admin API probe. Where one is absent or different, Step 3's sentence about it follows the code.

Run: `grep -n "ExecStart\|Before=\|RequiredBy=" internal/hostprep/snippets/paisans-egress.service.tmpl`
Expected: the unit loads the table from a file, before `user@<uid>.service`. Step 3's *Egress* sentence on rules the owner adds depends on it.

- [ ] **Step 2: Check what a request and an admit print**

Run: `ls internal/hostadmit/testdata/plans/ | head -40`
Expected: the golden plans (`docker-blank.txt` among them). Read `docker-blank.txt` and one `watchdog-*` plan: the guide describes in words what they show, so every step it names must appear there.

- [ ] **Step 3: Write the guide**

Create `docs/guides/host-owners.md`:

````markdown
# Admitting a deployment to your machine

This guide is for the owner of a machine, a homelab box or a VPS, that a
community has asked to run part of its paisans deployment. You do not need
the community's configuration, and you do not run its services. You run one
command as root, once, to admit the deployment, and again whenever you change
what you grant it. Everything the community's operators do after that runs as
a user of its own on your machine, with its own rootless Docker, and reaches
nothing of yours that user cannot.

The reasoning is in `README.md`, under *`host admit` gives a deployment a user
of its own, once, as root*; every flag is under *`paisans host admit`* and
*`paisans host evict`* in [docs/commands.md](../commands.md).

## What you need

* **Ubuntu 24.04, amd64 or arm64.** Admit refuses any other system, naming
  what it supports.
* **ufw installed.** Admit adds the deployment's rules to ufw and works with
  no other firewall. It stops before changing anything when ufw is not
  installed, since turning on a firewall you did not choose is not its
  decision; with ufw installed and inactive, it turns it on only when it
  finds no other firewall running.
* **Docker Engine 29.5 or later from Docker's repository, or no Docker.**
  With none, admit installs Docker from Docker's repository. It never
  upgrades or removes a Docker you already run: an older Engine, a snap, or
  Ubuntu's `docker.io` without Docker's rootless extras is refused, with the
  reason. Your own Docker keeps running beside the deployment's, untouched.
* **No Kubernetes.** The deployment marks its mesh traffic for the firewall
  with bits Kubernetes networking also uses.
* **The request file the operators sent you, and its digest**, which they
  tell you over a channel you trust (step 2).

## 1. Install paisans and check it

Each release attaches two static Linux binaries, `paisans-linux-amd64` and
`paisans-linux-arm64`, and a `SHA256SUMS` file listing their checksums. The
binary needs nothing else: not Go, not Docker, not Homebrew.

```sh
uname -m              # x86_64 means amd64, aarch64 means arm64
v=0.3.0               # the release your operators name
arch=amd64
base="https://github.com/Paisans-Software/paisans-cli/releases/download/v$v"
curl -fLO "$base/paisans-linux-$arch"
curl -fLO "$base/SHA256SUMS"
sha256sum --check --ignore-missing SHA256SUMS
sudo install -m 755 "paisans-linux-$arch" /usr/local/bin/paisans
paisans version
```

`sha256sum` prints `paisans-linux-amd64: OK`. Anything else, delete the file
and download it again. The check proves the file arrived whole and is the one
the release lists; both files come from the same release page, so it proves
nothing about the release itself, which the Paisans-Software organisation's
release workflow publishes.

Use the release the operators name. A request is refused by a paisans that
reads another request format, with the version it found, rather than read in
part.

## 2. Read the request and compare its digest

```sh
sudo paisans host admit --request home-a.admit
```

Without `--execute` it changes nothing. It shows the site and its roles, the
user it will make (`paisans-<token>`, unless the operators named another),
each operator whose keys will log in as that user, the grants the site's
roles need, every step it would take, and the request's **digest**: sixteen
characters in four groups, Eg: `7k2m-q9xd-4r8f-zt1c`.

The request carries the keys that will get a shell on your machine, so
anyone who could change it on its way could put their own key in it. The
digest is how you know it is the file the operators wrote: **ask an operator
for the digest over a different channel than the one that brought the file**,
a call or a message you know is theirs, and compare. A digest that travelled
beside the file in the same email proves nothing, since whoever changed the
file could change the digest too. When you admit with `--execute`, paisans
asks you to type the digest at the terminal, so the comparison cannot be
skipped by a script.

A request is read as strictly as a configuration: an unknown version, a field
missing or unknown, a user that is `root` or another deployment's, a key that
is not a plain public key, or any byte the operators' `host request` would
not have written, refuses it before anything else.

## 3. Decide what to grant

Admit gives nothing you did not pass a flag for. The plan lists the grants
the site's roles need. A site that needs the watchdog or the low ports stops
admit before it reads anything when you leave the flag out, and a grant the
site does not need is refused, since each weakens your machine for nothing.

| Flag | What it lets the deployment do | What it costs you |
|------|--------------------------------|-------------------|
| `--watchdog` | Patroni, the database, uses the machine's watchdog to fence itself: if it hangs while it leads, the machine reboots before another site's promotion leaves two primaries taking writes. A data site needs it to fail over safely. | **It can reboot your machine.** The device (`/dev/watchdog`) is given to the deployment's user. One deployment per machine can hold it; a second is refused. Where there is no hardware watchdog, admit loads `softdog`. |
| `--public-ports` | The deployment binds ports 80 and 443, which a gateway or a monitor running Paisans' own Caddy serves on. | **Every local user can then bind ports 80 to 1023**: admit lowers `net.ipv4.ip_unprivileged_port_start` to 80 for the whole machine, and opens 80 and 443 in ufw. Before granting it, admit checks that nothing else holds 80 or 443 (a listener, a container of your own Docker, or a forwarding rule), and refuses naming what does. |
| `--data-dir <path>` | The deployment's Docker data (images, containers, volumes: the database and its archive on a data site, the stored objects on a storage site) goes in `<path>/paisans-<token>` instead of the user's home. | Nothing, if the disk has room. `<path>` must exist, be a directory and not a link, and be root's with no group or other write bit. NFS, CIFS and SMB, FUSE filesystems, overlay, vfat, exFAT and NTFS are refused, since Docker's storage cannot use them; the plan states the free space, and warns below 20 GB for a data or storage site and 5 GB otherwise. Admit refuses to move the data root while the old one holds anything. |
| `--bridge-netfilter` | Admit loads `br_netfilter`, which the deployment's Docker needs, on a machine with bridges of its own (libvirt's, LXD's, yours). | **Traffic across your own bridges then passes the firewall's FORWARD chain**, which it skipped before, and whose policy is often to drop. The plan names each bridge and quotes the policy. Without the flag admit stops on such a machine; where the module is loaded already, or the only bridges are Docker's, it needs no flag. |
| `--mesh-lan` | The deployment's sites may reach each other on your LAN, at the private prefix of your machine's own network. | The deployment can send UDP to that LAN. Without it, the deployment's mesh traffic to your loopback, link-local and private ranges is dropped, and sites on one LAN reach each other through their public addresses, which needs a router that loops them back. |

**Forwarding has no flag.** A site with an endpoint relays mesh traffic
between sites that cannot dial each other, so admit turns on
`net.ipv4.ip_forward` for it and says so in the plan: routing between every
interface on your machine, which your firewall's forward policy then governs.

## 4. Admit

```sh
sudo paisans host admit --request home-a.admit --watchdog --execute
```

Pass the flags you decided on. Paisans asks for the digest, then makes each
change the plan showed, and ends by running `docker version` as the new user.
Every step can be run again: a re-run after a failure resumes, and one with
other flags changes only what they govern. The operators' next command is the
first to log in as the deployment's user.

An owner who is also one of the deployment's operators can admit from their
workstation instead, with the configuration and their own login to the
machine: `paisans host admit --site home-a --ssh you@home-a.example.org
--execute`. The steps are the same.

## What admitting changes on your machine

* **Docker**, where there was none: Docker Engine from Docker's repository,
  with its rootless extras, and `uidmap`, `nftables`, `wireguard-tools`,
  `ufw` and `git`. Where Docker is already there, only what is missing, and
  nothing is upgraded.
* **The user**, `paisans-<token>`, with a home, at least 65,536 subordinate
  IDs of its own, and linger, so its Docker runs with nobody logged in and
  starts at boot. A user that already exists and is root, or is in `sudo`,
  `wheel`, `admin`, `adm`, `docker`, `lxd` or `systemd-journal`, is refused:
  each reaches past the user.
* **cgroup delegation**, `/etc/systemd/system/user@.service.d/delegate.conf`,
  so the deployment's containers can be limited in CPU and memory. It is
  shared by every deployment admitted here.
* **The deployment's own Docker**, running as its user, configured in
  `~paisans-<token>/.config/docker/daemon.json`, and `br_netfilter`, loaded
  and listed in `/etc/modules-load.d/paisans.conf`.
* **`/srv/paisans/<token>`**, the user's, mode 750.
* **The mesh.** `/etc/wireguard`, root's, mode 700; a script,
  `/usr/local/libexec/paisans/wg-psns-<token>`, that installs and runs the
  deployment's WireGuard interface and checks every file it is given; and one
  line in `/etc/sudoers.d/paisans-<token>` that lets the user run that script
  as root, and nothing else.
* **ufw rules**, each commented `paisans-<token>: <why>`: the SSH port, the
  mesh's UDP port, traffic on the mesh interface, and 80 and 443 with
  `--public-ports`.
* **Egress**: an nftables table, `inet paisans_<token>`, loaded at boot by
  `paisans-<token>-egress.service` before the user's session starts, which
  drops the deployment's traffic to your loopback, link-local and private
  ranges, except the mesh and your DNS resolvers. If the deployment needs
  something on your network, its operators ask you; letting it through is a
  rule of yours, outside paisans, kept in a unit of your own ordered after
  the egress unit, since the table is loaded again at boot and whenever admit
  runs.
* **With `--watchdog`**, a udev rule giving the watchdog device to the user,
  and `softdog` where there is no hardware watchdog.
* **With `--public-ports`, or forwarding**, `/etc/sysctl.d/60-paisans.conf`,
  and the values it replaced recorded in `/etc/paisans/sysctl.before`.
* **The admission record**, `/etc/paisans/admitted.<token>.json`, root's and
  readable by all: the user, what you granted, the ports opened, the digest.
  It is how the deployment's own commands learn what they were given.
* **The registry**, `/var/lib/paisans/registry.json`, so a second deployment
  on this machine cannot take this one's names or ports.
* **The operators' keys**, in the user's `~/.ssh/authorized_keys`. From then
  on the operators maintain them themselves, as that user.

To see it afterwards:

```sh
sudo -l -U paisans-1a2b                       # the helper, and nothing else
sudo nft list table inet paisans_1a2b
sudo ufw status | grep paisans-1a2b
cat /etc/paisans/admitted.1a2b.json
```

**Anyone the operators trust gets a shell as that user.** Adding or removing a
co-operator is theirs to do without you. What protects you is that the user
is all that shell reaches.

## Behind your own web server

When your machine already serves 80 and 443 with its own web server, the
deployment's monitor can run behind it: the operators declare the site with
`web_server.managed_by: external`, and you admit it without
`--public-ports`. Admit detects your web server (Caddy, nginx or Apache, on
the host or in a container), and records how it is configured and how it
obtains certificates, which the deployment's user could not read for itself.
The deployment then renders its block for your server and never edits your
configuration: the operators run `paisans web-server show --app <app>` and
send you what it prints, you add it to your server and reload it, and their
`paisans web-server check` checks it from outside. See
[behind-your-own-web-server.md](behind-your-own-web-server.md).

## If you run Caddy

Caddy answers an admin API on `localhost:2019` unless told otherwise, and any
local user can replace its whole configuration through it, every admitted
deployment's user among them. When admit finds your Caddy doing so, its plan
warns. The fix is yours, in the global options block of your Caddyfile:

```caddyfile
{
	admin unix//run/caddy-admin.sock
}
```

or `admin off`, then reload Caddy. Admit only warns, since the server is
yours.

## Changing what you granted

Run admit again with the request for the site's roles as they are now and the
flags you grant now. It changes only what the flags govern. To add a grant
the site did not need before, ask the operators for a fresh request: a grant
the request does not need is refused.

A change of the site's SSH port, WireGuard port or user also needs you, since
each is a root change: the operators' `host prepare` refuses with the `host
admit` line to run.

## Evicting

```sh
sudo paisans host evict --request home-a.admit --execute
sudo paisans host evict --deployment f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01 --user paisans-f2a9 --execute
```

The second is for when you no longer have the request. Paisans asks you to
type the user's name, then stops the deployment's mesh and its user's
session, and removes the user with its home, its data directory,
`/srv/paisans/<token>`, the helper and its sudoers line, the watchdog rule,
the egress table and its unit, the deployment's ufw rules, the record and the
registry entry. It deletes a user only when it is the deployment's: the
record's, or, with no record, `paisans-<token>`.

What it leaves:

* **The SSH allow**, with a warning and the `ufw delete` line: it may be the
  rule your own login arrives by.
* **Files and settings other deployments share**: `delegate.conf`,
  `60-paisans.conf` and `modules-load.d/paisans.conf` stay while another
  deployment is admitted, and each sysctl is restored to the value it had
  before the first admit only once no admitted deployment needs it.
* **`br_netfilter` loaded**, until the next boot: your own Docker may use it.

The operators' `paisans site remove` does what the deployment's user can, and
ends by printing the `host evict` line for you.
````

- [ ] **Step 4: Check the guide**

Run: `make release-binaries && (cd dist && shasum -a 256 -c SHA256SUMS)`
Expected: each binary `OK`. On Linux, `sha256sum --check --ignore-missing SHA256SUMS` in `dist/` reads the same file and prints the same; `shasum` writes the two-space format `sha256sum` reads. Then `rm -rf dist`.

Run: `for f in --request --site --ssh --config --watchdog --public-ports --data-dir --bridge-netfilter --mesh-lan --execute --deployment --user; do grep -q -- "\"${f#--}\"" cmd/paisans/hostadmit.go || echo "missing $f"; done`
Expected: nothing.

Run: `ls docs/guides/behind-your-own-web-server.md docs/commands.md`
Expected: both exist.

Run: `perl -CSD -ne 'print "$.: $_" if /\x{2014}/' docs/guides/host-owners.md; grep -n "no longer\|used to\|previously" docs/guides/host-owners.md`
Expected: nothing.

Run: `grep -nE "[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}" docs/guides/host-owners.md`
Expected: nothing (the guide names no address).

Read the rendered file once, top to bottom, as an owner who has never seen paisans would: each command in order, each flag's cost before its flag is used.

- [ ] **Step 5: Commit**

```bash
git add docs/guides/host-owners.md
git commit -m "kacl-skip: docs: add a guide for host owners admitting a deployment

An owner who lends a machine needs the CLI and nothing else, and needs to
know what they are agreeing to: how to install the binary and check it,
how the digest proves the request is the operators', what each grant
costs them before they pass its flag, what admitting changes on the
machine, and how to evict. The guide is that path in order; the README
keeps the reasoning and the commands reference the flags.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: `docs/commands.md`, the owner's commands

**Files:**
- Modify: `docs/commands.md` (*Flags every command shares*, its paragraphs, *Hosts*: `host deployments`, `host request`, `host admit`, `host evict`)

**Interfaces:**
- Consumes: Task 1's guide headings; `host admit`'s and `host evict`'s flags; phase 6's `host deployments` (admission records).
- Produces: the entries Tasks 5 and 6 link to (*`paisans host admit`*, *`paisans host evict`*).

- [ ] **Step 1: Check the entries against the code**

Run: `go run ./cmd/paisans host deployments --help 2>&1; grep -n "admitted\|admission" cmd/paisans/hostdeployments.go | head`
Expected: `--ssh`, `--sudo`; the command reads admission records (phase 6). Step 2's `host deployments` text follows what it lists.

Run: `sed -n '/^### `paisans host admit`/,/^### `paisans prune`/p' docs/commands.md`
Expected: phase 5's `host admit` and `host evict` entries. Compare each flag with Task 1, Step 1's help output.

- [ ] **Step 2: The `host admit` and `host evict` entries**

In the `host admit` entry, replace its `* **Alone:**` bullet with:

```markdown
* **Alone:** always: run by the host's owner, never by an agent unattended.
  [guides/host-owners.md](guides/host-owners.md) is the owner's whole path.
```

Replace its `* **`--execute`:**` bullet with:

```markdown
* **`--execute`:** makes the changes the plan shows; with `--request` it
  first asks for the request's digest at a terminal, which the owner has
  from an operator over another channel than the one that carried the file.
```

After its `* **What it changes:**` bullet, add:

```markdown
* **Grants:** `--watchdog` gives the deployment the machine's watchdog,
  which can reboot it; `--public-ports` lowers
  `net.ipv4.ip_unprivileged_port_start` to 80 for every local user and opens
  80 and 443, refused while anything else holds either; `--data-dir <path>`
  puts the deployment's Docker data under `<path>/paisans-<token>`;
  `--bridge-netfilter` loads `br_netfilter` on a host with bridges of its
  own, whose traffic the FORWARD chain then filters; `--mesh-lan` lets the
  mesh reach the host's own LAN prefix. What each costs the owner is under
  *Decide what to grant* in the guide.
```

In the `host evict` entry, replace its `* **Alone:**` bullet with:

```markdown
* **Alone:** always, by the host's owner, never by an agent unattended;
  `site remove` ends by printing this command for them. See *Evicting* in
  [guides/host-owners.md](guides/host-owners.md).
```

- [ ] **Step 3: `host deployments`**

Replace the entry's first paragraph and its `* **Flags:**` bullet:

```markdown
Lists every deployment the host `--ssh` names holds: each admission record
under `/etc/paisans` (the user, the grants, the ports opened and when it was
admitted), and each registry entry (id, token, domain, site, roles and root,
and a Caddy kept for a host's own sites). Warns of any deployment directory
or labelled compose project neither names.
```

```markdown
* **Flags:** `--ssh <user@host[:port]>` (required), `--sudo`. With
  `--sudo=false` it lists the admission records alone, which are readable by
  every user, and not the registry or the directories, which are root's.
```

- [ ] **Step 4: The shared flags and their paragraphs**

In *Flags every command shares*, replace the `--sudo` row:

```markdown
| `--sudo` | on | `host deployments` | run its remote commands through sudo, since the registry and the deployments' directories are root's; `--sudo=false` skips it (see *A sudo that wants a password is asked for once, on a terminal* in `README.md`). `host admit` and `host evict` with `--site` reach the host through sudo always; with `--request` they run as root on the host itself. Every other command runs as the site's ssh user, without sudo |
```

Replace the paragraph beginning "With `--execute`, a command first claims each host it writes to":

```markdown
`paisans host admit` claims the host for the deployment in
`/var/lib/paisans/registry.json` when its owner admits it, and refuses if
another deployment there holds this one's token, WireGuard interface or
listen port, or an overlapping mesh subnet (see *A deployment has an id, and
a host knows every deployment on it* in `README.md`). Every other command
reads the claim through the admission record and never writes the registry.
```

- [ ] **Step 5: Check the entries**

Run: `grep -n "host admit\|host evict\|host-owners" docs/commands.md`
Expected: the `--sudo` row, the registry paragraph, the two entries, their guide links, and the `host request` entry's.

Run: `git diff docs/commands.md | perl -CSD -ne 'print if /^\+.*\x{2014}/'; git diff docs/commands.md | grep -n "^+.*\(no longer\|used to\|previously\)"`
Expected: nothing.

- [ ] **Step 6: Commit**

```bash
git add docs/commands.md
git commit -m "kacl-skip: docs: describe the owner's commands and their grants in the reference

host admit and host evict are the host owner's, never an agent's, and
their entries now say so with the guide beside them, and say what each
grant costs the owner. The digest is compared over another channel than
the one that carried the request, or it proves nothing. host deployments
lists admission records, which --sudo=false reads alone, and only admit
claims a host in the registry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: `docs/commands.md`, the commands the rootless work rewrote

**Files:**
- Modify: `docs/commands.md` (*Flags every command shares*'s review paragraph, `host prepare`, `apply` without `--site`, `apply --site`, `site remove`, `create`)

**Interfaces:**
- Consumes: phase 6's merged `host prepare`, `apply`, `site remove`, `create` (*Interfaces this phase documents*).
- Produces: the entries `README.md` names.

- [ ] **Step 1: Check each command's merged behaviour**

Run each, and read what it prints:

```bash
go run ./cmd/paisans host prepare --help 2>&1
go run ./cmd/paisans apply --help 2>&1
go run ./cmd/paisans site remove --help 2>&1
grep -n "granted\.\|\.paisans/" internal/grant/record.go internal/deployrecord/record.go internal/hostprep/configstore.go | head
grep -n "host evict" internal/siteremove/*.go | grep -v _test.go | head -3
grep -n "pending\|Applied, degraded" cmd/paisans/converge.go | head -5
grep -n "SSH user\|ssh user" internal/create/wizard.go | head -3
```

Expected: `host prepare` takes `--site`, `--all`, `--config`, `--ssh`, `--execute`; `apply` takes `--require-all`; the grant record, deployed record and configuration store under `/srv/paisans/<token>/.paisans/`; `site remove` prints the `host evict` line; the `pending` section and `Applied, degraded`; the wizard's user prompt. Every path and wording in Steps 2 to 5 is checked against these, and follows them where they differ.

- [ ] **Step 2: The review paragraph and `host prepare`**

Replace the whole `### `paisans host prepare`` entry, up to `### `paisans host deployments``, with:

````markdown
### `paisans host prepare`

Takes a host whose owner has admitted the deployment to the state `apply`
assumes, as the deployment's own user: the admission record checked against
`paisans-stack.yaml`, the user's authorized keys (every key of each operator
the site's `ssh.keys` names), the configuration store, and the user's Docker
configuration. It installs nothing and writes no file of root's; what needs
root is `host admit`'s.

```
paisans host prepare --site home-a            # shows what it would do
paisans host prepare --site home-a --execute  # does it
paisans host prepare --all --execute          # every site, one at a time
```

* **Alone:** the whole apply runs it for every site, so a single run is for one
  host on its own: preparing a newly admitted machine, or reaching a host
  through `--ssh`.
* **Flags:** `--site <name>` or `--all`, `--config`, `--ssh` (with `--site`
  only), `--execute`. It reads no secrets.
* **`--all`:** every site the configuration declares, in its order, each
  exactly as `--site` prepares it, with its own review. It stops at the first
  site that fails; re-running it resumes there, since a prepared host plans
  nothing.
* **Hosts:** the one site's, or with `--all` each site's in turn, as the
  deployment's user. It runs the host check first, against the user's own
  Docker.
* **The admission record:** read from `/etc/paisans/admitted.<token>.json`,
  which every user may read. A host with none, a record without a grant the
  site's roles need, or one whose user, SSH port, WireGuard port or
  forwarding differs from `paisans-stack.yaml` is refused, naming the
  `paisans host request` and `host admit` lines the owner runs, since each is
  a root change.
* **Review:** the keys, against what the host last recorded in the grant
  record under `/srv/paisans/<token>/.paisans/`: each key added or changed is
  a `+` or `~` line under `review what paisans-stack.yaml grants`, each
  removed one a `-` line, at every verbosity.
* **`--execute`:** asks for `approve` at a terminal when the review adds or
  changes anything; then writes what the plan lists, and records the keys
  once every step has succeeded. Without it, it reads and prints the plan and
  the review; a prepared host plans nothing.
* **Configuration store:** plans under *configuration store* what the host
  lacks of a site's store, under `/srv/paisans/<token>/.paisans/config/`: the
  bare repository, the peers file, the sync script, and the user's units for
  the daemon, the sync and its timer, which run under linger. The daemon runs
  while the deployment's mesh is up, so on a host whose mesh `apply` has not
  brought up it is enabled and not running. Every site's `host prepare` is
  also what makes the store `apply` publishes to, so `apply` refuses a
  version no site could take with this command named.

See *`host prepare` takes an admitted host to an apply-able state* and *`apply`
and `host prepare` review what `paisans-stack.yaml` grants* in `README.md`.
````

- [ ] **Step 3: `apply`**

In `### `paisans apply` without `--site``, add after its first paragraph:

```markdown
A site whose host has not admitted the deployment, or not with every grant
its roles need, is **pending**, and one that has joined and does not answer
is **down**. The run plans the deployment without its pending sites and
keeps its down sites as they are declared, and opens with a `pending`
section, a line per such site with its reason and, for a pending one, the
`paisans host request` line its owner admits it from. It refuses when the
gateway is pending or down, when no data site is ready while an app needs a
database, and when etcd or Patroni has no quorum among the members that
answer (see *A deployment with sites missing* in `README.md`).
```

In its flags bullet, add `--require-all` after `--keep-images`, and add a sentence to the bullet: "`--require-all` makes a run that ends degraded exit 1, for automation that must not pass on one."

In its `* **`--execute`:**` bullet, replace "It ends `Deployment complete.`, or," with:

```markdown
It ends `Deployment complete.`, or `Applied, degraded: <n> sites pending or
down (<names>)` with exit 0 when a site was pending or down, or,
```

In `### `paisans apply --site``, add to its `* **`--execute`:**` bullet, after its first sentence:

```markdown
On a gateway, the step ends by fetching `/.well-known/paisans/<token>`
through the site's public address on 443, and on 80 expecting the redirect;
any other answer fails the step, `443 on <site> answers, but not as this
deployment`, since something else on the host holds the port.
```

and a refusal to its *Hosts* bullet: "A `--site` naming a pending or down site is refused."

- [ ] **Step 4: `site remove` and `create`**

In `### `paisans site remove``'s `* **`--execute`:**` bullet, replace "Stage 3 deletes the record of what the deployment granted on the host, `/var/lib/paisans/granted.<token>.json`, with its registry entry. It deletes the host's configuration store too, the repository and the files beside it; the other sites still hold every version." with:

```markdown
Stage 3 does what the deployment's user can: it stops the site's stacks and
removes what is provably this deployment's under `/srv/paisans/<token>`,
the grant record and the configuration store among it; the other sites still
hold every version. It ends by printing the `sudo paisans host evict` line
for the host's owner, who removes the user, the mesh, the firewall rules,
the admission record and the registry entry.
```

In `### `paisans create``'s first paragraph, after "how many machines and what each does", the wizard asks each site's ssh user; add to the entry, after its first paragraph:

```markdown
Each site's ssh user may be left empty, which is the deployment's own user,
`paisans-<token>`, made by `host admit`.
```

- [ ] **Step 5: Check the entries**

Run: `grep -n "/var/lib/paisans/granted\|/var/lib/paisans/config\|include_dir" docs/commands.md`
Expected: no `granted` or `config` path under `/var/lib/paisans`; `include_dir` only where phase 6's merged web server entries still name it (`validate` refusing it).

Run: `for f in --require-all --all --site --ssh --execute; do go run ./cmd/paisans apply --help 2>&1 | grep -q -- "$f" || echo "apply lacks $f"; done`
Expected: `apply lacks --all` only (it is `host prepare`'s).

Run: `git diff docs/commands.md | perl -CSD -ne 'print if /^\+.*\x{2014}/'; git diff docs/commands.md | grep -n "^+.*\(no longer\|used to\|previously\)"`
Expected: nothing.

- [ ] **Step 6: Commit**

```bash
git add docs/commands.md
git commit -m "kacl-skip: docs: describe host prepare, apply and site remove as the deployment's user

host prepare installs nothing and checks the admission record; apply
runs on the sites it has and says which are pending or down, with
--require-all for automation that must not pass a degraded run, and a
gateway step that asks the gateway from outside; site remove does what
the deployment's user can and hands the owner the evict line. The
reference now says what each does.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: `README.md`, Docker judged by its version, and the shared host

**Files:**
- Modify: `README.md` (*The host check: what is already on a host decides how much is touched*, its *On a **shared** host* list; *Docker is judged by its version, not its source*)

**Interfaces:**
- Consumes: `hostadmit.MinRootlessEngine`; `hostcheck.MinEngine`, `MinCompose`; phase 5's ruling 18.
- Produces: the section Task 5's *Docker comes from Docker's repository when it is absent* links to.

- [ ] **Step 1: Check the floors**

Run: `grep -n "MinEngine *=\|MinCompose *=\|MinRootlessEngine *=" internal/hostcheck/docker.go internal/hostadmit/docker.go`
Expected: `MinEngine = "25.0.0"`, `MinCompose = "2.24.0"`, `MinRootlessEngine = "29.5.0"`. If phase 6 raised the host check's own floor to 29.5, Step 2's last paragraph says the host check holds the user's daemon to 29.5 as well.

Run: `grep -n "ufw\|firewall" internal/hostcheck/*.go | grep -v _test.go | head`
Expected: nothing (phase 6: "no ufw probe"). If a ufw probe remains, Step 3's first bullet follows it.

- [ ] **Step 2: Docker is judged by its version**

Replace the subsection `#### Docker is judged by its version, not its source`, from its heading up to `### `host prepare` takes a blank host to an apply-able state`, with:

```markdown
#### Docker is judged by its version, not its source

Each deployment runs its own rootless Docker daemon as its own user, beside
whatever Docker the host's owner runs, and `host admit` sets it up (see
*`host admit` gives a deployment a user of its own, once, as root*). Where
Docker came from matters in one way only: the rootless daemon is installed by
`dockerd-rootless-setuptool.sh`, which Docker's repository ships in
`docker-ce-rootless-extras`. Beyond that, what the toolkit depends on is what
the engine and compose can do, and that is a version. So admit refuses, on
any host and whatever its class:

* **An Engine older than 29.5.** From 29.5 a rootless container with host
  networking shares the host's network namespace; before it, host networking
  was namespaced inside RootlessKit
  ([Docker: rootless troubleshooting](https://docs.docker.com/engine/security/rootless/troubleshoot/)).
  Every infrastructure container is `network_mode: host`, bound to the
  site's mesh address, so on an older Engine no other site reaches it. The
  refusal names the version found and leaves the upgrade to the owner, since
  upgrading Docker restarts every container the owner runs. 29.5 is above
  every floor the rendered stacks set themselves, Engine 25.0.0 and compose
  2.24.0 for `healthcheck.start_interval`;
  [docs/specs/2026-10-11-docker-version-gate.md](docs/specs/2026-10-11-docker-version-gate.md)
  lists each feature the toolkit depends on with the release that brought
  it.
* **A Docker without the rootless extras**, such as Ubuntu's `docker.io`.
  Admit adds `docker-ce-rootless-extras` from Docker's repository beside a
  `docker-ce` from it, and never replaces packages the owner chose.
* **A Docker snap, however new.** snapd refreshes it, restarting the daemon
  and every container, on snapd's schedule rather than the owner's, and its
  confinement keeps Docker from bind mounting under `/srv`, where every stack
  keeps its data. A version number reveals neither.

Nothing is upgraded or removed: either stops every container the owner runs,
and that is a decision for whoever started them. The host check, which
`host prepare`, `apply` and the others run as the deployment's user, reads the
user's own daemon, which admit has already held to 29.5.
```

- [ ] **Step 3: The shared host**

In *The host check*, under "On a **shared** host:", replace the three bullets with:

```markdown
* **The firewall is the owner's.** `host admit` adds the deployment's rules,
  each commented `paisans-<token>:`, and turns ufw on only on a host where
  ufw is inactive and no other firewall runs; no command after it touches
  the firewall.
* **No image is removed**, as with `apply --keep-images`, and nothing is
  swept, because an image the toolkit renders (`caddy`, `postgres`) may be
  what a foreign project runs.
* **The owner's Docker is left alone**, as it is on every host: the
  deployment runs its own (see *Docker is judged by its version, not its
  source*).
```

- [ ] **Step 4: Check the section**

Run: `grep -n "let \`host prepare\`\|remove it and let\|Docker is left alone" README.md`
Expected: nothing.

Run: `git diff README.md | perl -CSD -ne 'print if /^\+.*\x{2014}/'; git diff README.md | grep -n "^+.*\(no longer\|used to\|previously\)"`
Expected: nothing. "was namespaced inside RootlessKit" describes Docker's own history, which the reason rests on, not the toolkit's.

- [ ] **Step 5: Commit**

```bash
git add README.md
git commit -m "kacl-skip: docs: judge docker by the version rootless host networking needs

Every infrastructure container binds the mesh address with host
networking, which a rootless daemon gives the host's own namespace only
from Engine 29.5, so that is the floor host admit holds a host's Docker
to; the rootless extras come from Docker's repository, and a snap is
refused as before. On a shared host the firewall is the owner's and the
owner's Docker is never touched, since the deployment runs its own.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: `README.md`, what `host admit` does

**Files:**
- Modify: `README.md` (*Bootstrapping a site*: a new section before `host prepare`'s, the subsections moved into it, *A sudo that wants a password is asked for once, on a terminal*)

**Interfaces:**
- Consumes: Task 1's guide; phase 5's step order (its ruling 1), rulings 7, 8, 10, 14, 22, 29, 30 and its founder decisions.
- Produces: the section `### `host admit` gives a deployment a user of its own, once, as root` and its subsections `#### Operating systems are profiles`, `#### Docker comes from Docker's repository when it is absent`, `#### The firewall follows roles`, `#### host admit owns its rules, and only its rules`, `#### Egress keeps the deployment off the owner's network`, `#### The watchdog is the owner's to grant`, `#### The deployment's Docker and its mesh at boot`, which Tasks 6 and 8 name.

- [ ] **Step 1: Check the order and the rules against the code**

Run: `grep -n '{"' internal/hostadmit/plan.go | head -40`
Expected: the `parts` table in its order (claim, docker, apparmor, user, delegation, egress, linger, bridge netfilter, daemon, deployment directory, mesh subnet, wireguard, firewall, watchdog, sysctl, web server, record, keys). Step 3's numbered list follows it.

Run: `grep -n "paisans-<token>: \|reason\|ssh, \|public ports, HTTP" internal/hostadmit/firewall.go internal/hostprep/admit.go | head`
Expected: the ufw rules admit adds and their comments (phase 5 ruling 23: `public ports, HTTP`, `public ports, HTTPS`). Step 4's table follows them.

- [ ] **Step 2: Make room for the section**

The new section goes immediately before `### `host prepare` takes a blank host to an apply-able state`. Cut these subsections, whole, from under `host prepare`'s section, to paste into the new one in Step 4: `#### Operating systems are profiles`, `#### Docker comes from Docker's repository when it is absent`, `#### Docker starts after the mesh interface`, `#### The firewall follows roles`, `#### host prepare owns its rules, and only its rules`, `#### The watchdog`. `#### host prepare gives each site a configuration store` and `#### host prepare manages the login user's authorized keys` stay; Task 6 rewrites them.

- [ ] **Step 3: The section's head**

Insert before `### `host prepare` takes a blank host to an apply-able state`:

```markdown
### `host admit` gives a deployment a user of its own, once, as root

`apply` assumes a host where the deployment has its own unprivileged user,
running its own rootless Docker, with the WireGuard tools, a firewall, a
narrow way to manage its mesh interface and, on a data site, a watchdog for
Patroni. Making that user, and everything it cannot do for itself, needs
root, and root on someone's machine is the owner's, not the operators'. So it
is one command the host's owner runs, `paisans host admit`, once per
deployment per host and again whenever they change what they grant, and
everything after it runs as the deployment's user, which is all an operator,
a bug in the CLI or a hostile blueprint can reach. How to run it, and its
flags, are under *`paisans host admit`* in [docs/commands.md](docs/commands.md);
[docs/guides/host-owners.md](docs/guides/host-owners.md) is the owner's whole
path, from installing the binary to evicting.

An owner without the deployment's configuration admits a site from a request
(see *A host's owner admits a site from a request*), on the host, as root;
an owner who is also an operator admits it from the configuration, with
`--site` and their own login in `--ssh`. Both plan the same steps. It probes
read only, prints the plan, and changes the host only with `--execute`. Every
step is idempotent, so a re-run after a failure resumes, and a re-run with
other flags changes only what they govern. In order:

1. **The registry claim**, first under `--execute`, so a conflict found under
   the lock changes nothing.
2. **Docker**, where there is none, from Docker's repository (below), with
   `docker-ce-rootless-extras`, `uidmap`, `nftables`, `wireguard-tools`, `ufw`
   and `git`; a Docker already there is judged by its version (see *Docker is
   judged by its version, not its source*), and only what is missing beside
   it is installed, with nothing upgraded.
3. **AppArmor.** Ubuntu 24.04 restricts unprivileged user namespaces unless a
   profile allows them; the Docker package's `rootlesskit` profile must be
   loaded, and admit refuses with the fix when it is not.
4. **The user**, `paisans-<token>` unless `ssh.user` names another, with at
   least 65,536 subordinate IDs no other user's overlap. A user that already
   exists and is root, or is in `sudo`, `wheel`, `admin`, `adm`, `docker`,
   `lxd` or `systemd-journal`, is refused: each reaches past the user, and
   `docker` is the owner's system daemon, which is root on the host.
   `paisans doctor` checks the same list on every run.
5. **cgroup delegation**, `Delegate=cpu cpuset io memory pids` in
   `/etc/systemd/system/user@.service.d/delegate.conf`, written before the
   user's manager first starts and never by restarting one already running,
   since that restarts the user's daemon and every container in it.
6. **Egress** (below), loaded before the user's manager can start, so no
   container ever runs unfiltered.
7. **Linger**, so the user's systemd instance, and with it its Docker and its
   timers, runs with nobody logged in and starts at boot.
8. **`br_netfilter`**, which the rootless daemon needs with its userland proxy
   off, listed in `/etc/modules-load.d/paisans.conf` even where it is loaded
   already, since a system Docker that loads it may start after the user's.
   It is host wide: once loaded, traffic across every bridge on the host
   passes the firewall's FORWARD chain, which often drops by default. So a
   host with bridges of its own (libvirt's, LXD's, the owner's) gets a plan
   naming each and quoting the chain's policy, and admit stops there unless
   the owner passes `--bridge-netfilter`.
9. **The rootless daemon**, installed as the user, with
   `~/.config/docker/daemon.json` written as the user: the userland proxy
   off, so a published port sees the client's address; the deployment's
   address pools, clear of the mesh subnet; and with `--data-dir`, the data
   root under the owner's chosen disk.
10. **`/srv/paisans/<token>`**, the user's, mode 750. `/srv/paisans` stays
    root's.
11. **The mesh**: `/etc/wireguard` root's and mode 700, and the WireGuard
    helper with its one sudoers line (see *`apply` brings the mesh interface
    up before anything binds to it*). A mesh subnet overlapping a route, an
    address or a Docker network on the host is refused.
12. **The firewall** (below).
13. **The watchdog**, with `--watchdog` (below).
14. **Low ports**, with `--public-ports`: `net.ipv4.ip_unprivileged_port_start`
    of 80, after checking as root that nothing else answers on 80 or 443: a
    listener, a port the owner's Docker publishes, or a DNAT rule for either
    port, which a Docker with its userland proxy off publishes with and no
    listener shows. Anything found refuses the grant, naming what holds the
    port, and names `web_server.managed_by: external` as the way to run
    behind it. **Forwarding**, for a site with an endpoint, which relays mesh
    traffic between sites that cannot dial each other: `net.ipv4.ip_forward`.
    Both go in `/etc/sysctl.d/60-paisans.conf`, with the values they replaced
    in `/etc/paisans/sysctl.before`, so evicting the last deployment that
    needs one restores it.
15. **The owner's web server**, for a site behind it (see *The monitor has a
    site of its own*): detected as root, recorded, and, for a Caddy answering
    its admin API on a TCP address, a warning that any local user can
    replace its configuration, with the fix.
16. **The admission record**, `/etc/paisans/admitted.<token>.json`, root's,
    mode 644: the user, the grants, whether forwarding is on, the ports
    opened, the request's digest, the data directory, the owner's web server
    and when. The deployment's own commands read it without sudo to learn
    what they were granted, and `host prepare` refuses a declaration it does
    not match.
17. **The keys**: each `ssh.keys` operator's keys appended to the user's
    `~/.ssh/authorized_keys`, written as the user, and recorded as `host
    prepare` records them; then `docker version` as the user.

**Grants are the owner's.** The watchdog can reboot the machine, and ports
below 1024, once lowered, are bindable by every local user, so neither is
given without its flag, `--watchdog` or `--public-ports`. A data site without
the watchdog, or a gateway or a monitor running Paisans' Caddy without the
low ports, stops admit before it reads anything, and a grant the site's roles
do not need is refused, since it weakens the host for nothing.

**Everything in the user's home is written as the user.** A root write
through a path the user controls follows any link the user planted there, so
admit writes `authorized_keys` and `daemon.json` with `runuser -u <user>`,
and nothing of root's ever reads or writes below the user's home or
`/srv/paisans/<token>`.
```

- [ ] **Step 4: The moved subsections, rewritten**

After the head, paste the six subsections cut in Step 2, in this order, and rewrite each as follows.

`#### Operating systems are profiles`: keep the text, replacing its first sentence's subject: "Everything that depends on the operating system lives in a **host profile**" stays; change "Adding Debian or Fedora is adding a directory and a registration, not editing the command." to "Adding Debian or Fedora is adding a directory and a registration, not editing `host admit`."

`#### Docker comes from Docker's repository when it is absent`: replace its body with:

```markdown
Where Docker is absent, admit installs Docker Engine and the compose plugin
from Docker's own apt repository, as
[Docker's install page](https://docs.docker.com/engine/install/ubuntu/)
describes, with `docker-ce-rootless-extras`. An install needs one source, and
that one gives every host the same engine from the same source, whenever it
was admitted, and carries the rootless extras.

Where Docker is there, admit installs only what is missing beside it, with
`apt-get install --no-upgrade`, which never upgrades a package already
installed: the owner's Docker, and every container it runs, stays as it was.
A Docker that is not from Docker's repository and has no
`dockerd-rootless-setuptool.sh` is refused, naming the packages, since the
rootless extras come from Docker's repository and mixing the two would
replace the owner's packages. Every `apt-get` runs non-interactively.
```

`#### Docker starts after the mesh interface`: rename it `#### The deployment's Docker and its mesh at boot`, and replace its body with:

```markdown
Every infrastructure container binds its site's mesh address, which exists
only once `wg-quick@psns-<token>` has brought the interface up, and the
user's Docker starts with the user's manager, under linger. Nothing orders
the two at boot. An infrastructure container that starts first fails to
bind, and Docker restarts it, since every one is `restart: unless-stopped`,
until the interface is up (observed: a container restarted on `Address not
available` and served six seconds after the interface came up). An app
container publishes its port on the mesh address instead, and Docker does
not retry a container whose network setup failed, so one that starts first
stays down; `paisans doctor` names it, with the `docker start` or `apply
--recreate` that brings it back.
```

`#### The firewall follows roles`: replace its table and the paragraph after it with:

```markdown
| Rule | Sites |
|------|-------|
| deny incoming, allow outgoing by default | a host whose ufw is inactive and which runs no other firewall; any other host keeps the policy its owner set |
| the site's `ssh.port`, 22 unless declared | every site |
| the port the site is dialled on, UDP, WireGuard | sites with a `public_address` or an `endpoint` (see *Where the mesh dials a site*) |
| everything arriving on `psns-<token>` | every site |
| 80/tcp, 443/tcp | with `--public-ports`: the gateway, and a monitor in `managed_by: paisans` |

A rootless container reaches the site's mesh address through its user's own
process, not through a host bridge, so no rule for bridge traffic is needed.
Nothing is listed per site: the rules follow the request's roles and the
owner's grants, and a site that gains the gateway role gains 80 and 443 when
its owner admits it again with `--public-ports`. A monitor behind the
owner's own web server opens neither: that server holds both, and its rules
are the owner's.
```

`#### host prepare owns its rules, and only its rules`: rename it `#### host admit owns its rules, and only its rules`; in its body replace each "`host prepare`" with "`host admit`", "A later prepare" with "A later admit", "A host prepared before rules carried a comment holds exactly the derived rules, uncommented." with "A host whose rules were added by hand before admit holds exactly the derived rules, uncommented.", and "every later prepare and apply arrives over it" with "every later command arrives over it". In its last paragraph, **Docker-published ports bypass the firewall.**, replace "That binding is the renderer's, not `host prepare`'s." with "That binding is the renderer's, not `host admit`'s." Keep everything else. Add, after the paragraph beginning "**SSH is never removed**":

```markdown
`host evict` keeps the deployment's SSH allow for the same reason, with a
warning naming the `ufw delete` line for the owner.
```

`#### The watchdog`: rename it `#### The watchdog is the owner's to grant`, and replace its first paragraph with:

```markdown
On a data site, with `--watchdog`, `host admit` gives Patroni the device:
the host's own watchdog where it has one, or `softdog`, with a warning, where
it has none; and a udev rule makes `/dev/watchdog` and `/dev/watchdog0` the
user's, owner and group both, mode 600. The group matters: a device whose
group is root's cannot be `chown`ed inside the user namespace, and the
infrastructure stack does that for Patroni (observed). One deployment per
host can hold it; a second is refused. Admit plans the `auto` mode of *A data
site's watchdog is declared, not assumed*, since a request carries no mode,
and `host prepare` and `paisans doctor` compare what it found with the
site's declared mode.
```

Keep its `softdog` paragraph, replacing "`softdog` is persisted with a small systemd unit that runs `modprobe softdog` before Docker starts" with "`softdog` is persisted with a small systemd unit that runs `modprobe softdog` at boot". Replace its last paragraph, **Provisional: little of this has run on a host yet.**, with:

```markdown
**Provisional: admit has been run on staging hosts only.** Each step's plan
is tested against a fake host, and the staging runs of phase 10 are the
first real ones; read their output rather than trust it.
```

- [ ] **Step 5: Egress**

After `#### host admit owns its rules, and only its rules`, add:

```markdown
#### Egress keeps the deployment off the owner's network

Every infrastructure container shares the host's network namespace, so
without a rule it could reach the owner's services bound to `127.0.0.1` and
the owner's LAN. Admit loads an nftables table, `inet paisans_<token>`, from
a unit, `paisans-<token>-egress.service`, ordered before the user's manager,
so no container starts unfiltered at boot. Its output chain matches the
user's UID and its whole subordinate range, since a container's users map
into that range and the UID alone would miss them, and:

1. accepts the mesh subnet;
2. accepts port 53 to the host's resolvers, read when admit runs;
3. drops `127.0.0.0/8`, `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`,
   `169.254.0.0/16`, `100.64.0.0/10`, `::1`, `fc00::/7` and `fe80::/10`.

**The mesh's own packets have a chain of their own.** WireGuard's UDP is sent
by the kernel, not by the user, so the UID chain never sees it, and the
user writes each peer's `Endpoint`. So the WireGuard helper adds a firewall
mark, one per deployment, to every mesh file it installs, and refuses a file
that sets one itself; a second chain, on that mark, drops loopback,
link-local and multicast, every address of the host itself and the ranges of
rule 3, after the endpoint's name is resolved, so nothing changes between
the check and the send. Everything else, the public endpoints a mesh dials,
passes. The mark is chosen for Docker hosts, and overlaps bits Kubernetes
networking uses, so a host running Kubernetes is outside what admit supports.

**`--mesh-lan`** lets a deployment's sites dial each other on the owner's
LAN: admit accepts the private prefix of the host's own LAN, the network of
its default route's interface, no wider than a /16, in the mark's chain, and
nothing wider. Without it, sites on one LAN dial each other's public
addresses, which needs a router that loops them back.

A deployment that needs something on the owner's network asks the owner,
who adds an accept rule of their own. That is outside the CLI on purpose.
```

- [ ] **Step 6: The owner's commands through sudo**

In `#### A sudo that wants a password is asked for once, on a terminal`, replace its first paragraph with:

```markdown
Every command that acts on a deployment logs in as the site's ssh user and
runs nothing through sudo but the WireGuard helper. Three commands are the
host owner's: `host admit` and `host evict`, which change the host as root,
and `host deployments`, which reads the registry and every deployment's
directories, root's. With `--request` the first two run as root on the host
itself, under the owner's own `sudo`; reached from a workstation, with
`--site` and the owner's login in `--ssh`, they and `host deployments` run
through sudo (`host deployments`' `--sudo`, on by default). Before the first
command, the toolkit asks the host `sudo -n true`. `-n` makes sudo fail at
once instead of waiting for a password, so the answer is immediate.
```

and replace its paragraph beginning "An unattended run of an owner's command needs a NOPASSWD sudoers rule" with:

```markdown
`host admit` and `host evict` never run unattended, whatever sudo allows:
with `--execute`, admit from a request asks for the request's digest and
evict for the deployment's user name, each typed at a terminal. An
unattended `host deployments` needs a NOPASSWD sudoers rule for the login
`--ssh` names. Whether to write one is a decision about the host, made by
whoever owns it, and the toolkit never makes it.
```

- [ ] **Step 7: Check the section**

Run: `grep -n "^### \`host admit\` gives\|^#### Operating systems\|^#### Docker comes from\|^#### The deployment's Docker and its mesh\|^#### The firewall follows\|^#### host admit owns\|^#### Egress keeps\|^#### The watchdog is the owner's\|^### \`host prepare\` takes" README.md`
Expected: the new section's heading, its seven subsections in that order, then `host prepare`'s heading.

Run: `grep -n "br-+\|after-wireguard" README.md`
Expected: nothing in the sections this task wrote. Task 6 sweeps the rest.

Run: `git diff README.md | perl -CSD -ne 'print if /^\+.*\x{2014}/'; git diff README.md | grep -n "^+.*\(no longer\|used to\|previously\)"`
Expected: nothing.

- [ ] **Step 8: Commit**

```bash
git add README.md
git commit -m "kacl-skip: docs: describe what host admit does, and why the owner runs it

Making the deployment's user and everything it cannot do for itself
needs root, which is the host owner's, so the steps that were host
prepare's root steps are admit's: Docker, the user, delegation, linger,
br_netfilter and the rootless daemon, the mesh helper, the firewall and
its owned rules, egress, the watchdog and the low ports. Each subsection
moved under admit's section and says what admit does and why; the egress
table, which nothing described, has its own. The owner's commands are the
only ones through sudo, and admit and evict never run unattended.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: `README.md`, what `host prepare` does, its keys, and every other root step

**Files:**
- Modify: `README.md` (*`host prepare` takes a blank host to an apply-able state* and its two remaining subsections; *What it does*'s command block; every passage the sweep in Step 3 finds)

**Interfaces:**
- Consumes: Task 5's section names; phase 6's merged `internal/hostprep` (keys, configuration store).
- Produces: `### `host prepare` takes an admitted host to an apply-able state`, `#### host prepare gives each site a configuration store`, `#### host prepare manages the deployment's user's authorized keys`, which Task 3's entry names.

- [ ] **Step 1: Check the keys and the store against the code**

Run: `grep -n "authorized_keys.owned\|\"added\"\|\"shared\"\|flock" internal/hostprep/authorized_keys.go | head`
Expected: the sidecar at `/srv/paisans/<token>/.paisans/authorized_keys.owned`, and whether the `shared` mark and the lock remain. Step 2's keys text is written for a sidecar of this deployment's alone; if `shared` remains, add the paragraph Step 2 gives for it.

Run: `grep -n "config\.git\|\.peers\|systemctl --user\|config-sync" internal/hostprep/configstore.go | head`
Expected: the store under `/srv/paisans/<token>/.paisans/config/`, user units.

Run: `grep -n "authorize key\|remove key\|not by host prepare" internal/hostprep/authorized_keys.go`
Expected: the plan lines' wording. Step 2's `-v` example quotes them, so it follows them exactly, whatever words they use.

- [ ] **Step 2: `host prepare`'s section**

Replace `### `host prepare` takes a blank host to an apply-able state` and its paragraphs, up to `#### host prepare gives each site a configuration store`, with:

```markdown
### `host prepare` takes an admitted host to an apply-able state

Once a host's owner has admitted the deployment, what is left before `apply`
is the deployment's own: the operators' keys, the configuration store, and
the user's Docker configuration. **None of that is done by hand**, for the
reason none of `apply` is: a step an operator performs from a wiki page is a
step done differently on every host, and the first time it matters is a
rebuild at the worst possible moment. So it is a command, `host prepare`. How
to run it, and its flags, are under *`paisans host prepare`* in
[docs/commands.md](docs/commands.md).

It reaches the host through the site's `ssh` section, as the deployment's
user, exactly as `apply` does, and needs no sudo: it installs nothing and
writes no file of root's. It first reads the admission record and compares it
with `paisans-stack.yaml`; a host with none, or one without a grant the
site's roles need, or whose user, SSH port, WireGuard port or forwarding
differs, is refused with the `host request` and `host admit` lines for the
owner, since each is a root change. It probes read only, plans only what is
missing, and changes nothing without `--execute`; a prepared host plans
nothing, so re-running it after a failure resumes rather than restarts. It is
not part of `apply --site` for the same reason `storage init` is not: the
store and the keys are host state that no rendered manifest describes, so
they are probed rather than compared. `apply` without `--site` runs it as a
command of its own (see *`paisans apply` with no `--site` converges the whole
deployment*).
```

Replace `#### host prepare gives each site a configuration store`'s body with:

```markdown
A site cannot keep signed copies of the configuration (see *Every site keeps
signed copies of the configuration*) until something exists on it to keep them
in, and `apply` publishes a version before it changes anything else, so the
store has to exist first. `host prepare` plans, under *configuration store* in
its plan, only what the host lacks, all under
`/srv/paisans/<token>/.paisans/config/` and the user's: the bare repository,
made only where its `HEAD` is absent so that a re-run never touches versions
already there; the read-only daemon and the sync timer, as the user's own
systemd units, which run under linger; and a file listing every other site's
mesh address, which the sync reads. `git` is there from `host admit`. A site
added later gets the peers it lacks by a `host prepare` on the existing sites,
which rewrites the list.
```

Rename `#### host prepare manages the login user's authorized keys` to `#### host prepare manages the deployment's user's authorized keys`, and replace its body, up to `### `apply` and `host prepare` review what `paisans-stack.yaml` grants`, with:

````markdown
`host prepare` makes every key of every operator in `ssh.keys` an authorized
key of the deployment's user (`ssh.user`, or `paisans-<token>` when it is left
out), in that user's own `~/.ssh/authorized_keys`, written as that user. Adding
or removing a co-operator is an operator's change and needs no owner. `host
admit` wrote the first keys, from the request, and recorded them as this
command records its own.

It adds only listed keys, and deletes a line only when **it or `host admit`
wrote that line**. A listed key that was already there is never recorded and
so never removed: it may be the key someone logs in with, and nothing on the
host says whether another way in exists.

The record is a sidecar, `/srv/paisans/<token>/.paisans/authorized_keys.owned`,
one line per key:

```
added <fingerprint> <name>
```

A line of any other shape is not one of this deployment's claims, and keeps
its key. Keys are compared by fingerprint, so a key whose comment was changed
is still the same key, and keeps its operator's name.

For a listed key:

| `authorized_keys` holds | The sidecar | The plan says | What happens |
|-------------------------|-------------|---------------|--------------|
| nothing for the key | | `○ authorize ssh key` | appended verbatim, recorded `added` |
| the key | lists it | `✓` | nothing; `○ rename ssh key` if the key now belongs to a different operator |
| the key | does not list it | `✓` | nothing; not recorded, so never removed |
| the key, only with options | | `✓ …: not paisans` | nothing; adding the plain key would undo the restriction |

For a key the sidecar lists that `ssh.keys` no longer does:

| `authorized_keys` holds | The plan says | What happens |
|-------------------------|---------------|--------------|
| nothing for the key | `○ forget ssh key` | dropped from the sidecar |
| the key | `○ remove ssh key` | that exact line deleted, last of all |

With `-v`, the keys' part of a plan reads:

```
  ○ authorize ssh key
      ssh: authorize key SHA256:RXm/ruZ0eTzRXKwi1AQEDynB0VgHQ2ac9KPSFdf/YnA (bob) for paisans-f2a9
  ○ remove ssh key
      ssh: remove key SHA256:baqJQcVDEweKmw1OiZxGooCG2MGxYtwsQQzzOstxmiA (carol) from /home/paisans-f2a9/.ssh/authorized_keys, which host prepare added and ssh.keys no longer lists
  ✓ ssh: key SHA256:kmYcvdi2GkPeWxB6XLjrZB8JHsy2Hm8luHMFp9GMvqk (alice) authorized for paisans-f2a9, not by host prepare, which never removes it
```

**Ownership lives in the sidecar, not in the key's comment.** The comment is
how an operator recognises a key (`alice@laptop`), and leaving it alone keeps
what they see in the one place they look. Each step rewrites the sidecar along
with its change, so a stopped run resumes: a key appended but not yet recorded
stays unrecorded, and so is never removed, and a sidecar entry whose key
someone already deleted is forgotten, so a copy they add by hand later is
never taken for host prepare's.

**Nothing is removed until everything is in place.** Removals run after every
addition, and a removal goes through a temporary file given the original's
mode and renamed over it, so sshd never reads a half written file. A plan that
would leave the user with none of the listed keys is refused. The key the
current connection used is not removed either, by construction: the transport
offers only listed keys, so the session's key is a listed one, unless `--ssh`
or the operator's own `IdentityFile` chose another.

**The review is the check on whose keys it adds.** `paisans-stack.yaml` is one
file every operator of a deployment edits, and whoever runs `host prepare` runs
it with their own access, so any operator can get a key of theirs made an
authorized key of the deployment's user through someone else's run. That user
reaches everything the deployment holds. The review (see *`apply` and `host
prepare` review what `paisans-stack.yaml` grants*) shows every key a run would
add, with its operator and fingerprint, and an `--execute` run asks for
`approve` at a terminal before anything changes. The host's owner is
protected by the user itself: a shell as it reaches nothing past it.
````

Only when Step 1 found the `shared` mark still in `internal/hostprep/authorized_keys.go`, add after the sidecar's format line:

```markdown
A sidecar line may also read `shared <fingerprint> <name>`: the line was
already there, and another deployment logging in as the same user had
claimed it when this one recorded it, so the line stays until the last
deployment claiming it releases it.
```

and the `shared` rows to both tables as phase 6's code plans them.

- [ ] **Step 3: The sweep**

Run: `grep -n "host prepare" README.md`
Expected: each hit either describes what `host prepare` does as the deployment's user (the review, the keys, the store, the record check, the host check, its place in `apply`), or is one of the passages below. Rewrite each listed passage in place:

* *What it does*, the command block: replace the two `host prepare` lines with

  ```
  sudo paisans host admit --request vm.admit --execute   # on the host, by its owner: the deployment's own user and its rootless Docker
  paisans host prepare --all --execute                    # every site's keys and configuration store, one at a time
  ```

  and add before them `paisans host request --site vm > vm.admit     # the request a host's owner admits a site from`.
* *The monitor has a site of its own*: "It claims tcp 80 and 443, and `host prepare` opens both." becomes "It claims tcp 80 and 443, which `host admit --public-ports` grants and opens."
* *A deployment has an id, and a host knows every deployment on it*, the names table: the row "a firewall rule `host prepare` adds" becomes "a firewall rule `host admit` adds"; the row "`host prepare`'s own files" becomes two rows, "`host admit`'s own files" with `/etc/paisans/admitted.<token>.json`, `/etc/sudoers.d/paisans-<token>`, `/usr/local/libexec/paisans/wg-psns-<token>`, `paisans-<token>-egress.service`, `paisans-<token>-watchdog.service`, and "the deployment's own state" with `/srv/paisans/<token>/.paisans/`: the deployed and grant records, the keys' sidecar and the configuration store.
* *Where the mesh dials a site*: "`host prepare` opens it on the host" becomes "`host admit` opens it on the host".
* *Two deployments on one host never share a mesh*: "the UDP port `host prepare` opens there" becomes "the UDP port `host admit` opens there".
* *A data site's watchdog is declared, not assumed*, the modes table's header "What `host prepare` does" becomes "What `host admit --watchdog` does, and `host prepare` checks"; "**Only one process can hold the device, and it has to be Patroni.** `host prepare` refuses" becomes "... `host admit` refuses".
* *Adding the gateway and the witness*, step 2: "`host prepare` on every site: the VM gets Docker, WireGuard, a firewall following its roles and the keys of the operators its `ssh.keys` names, and home-a, already prepared, plans nothing." becomes "`host prepare` on every site: the VM, which its owner has admitted with `--public-ports`, gets the keys of the operators its `ssh.keys` names and its configuration store, and home-a, already prepared, plans nothing."
* *What `paisans preflight` checks, and how*, the `Host` row: "or the host is shared and its firewall is not already up and denying by default, or its Docker is a snap or older than the toolkit needs" becomes "or its Docker is older than the toolkit needs"; the `Prepared` row: "`host prepare`'s own plan, without the firewall's defaults on a shared host" becomes "`host prepare`'s own plan, the admission record's check first"; the `Watchdog` row's check stays.
* *It is a config change*, the bullet **`host prepare` keeps the old gateway open.**: rewrite to what phase 6's merged `gateway move` does with the old gateway's 80 and 443 (founder question 4). Where it leaves them to the owner, the bullet reads: "**The old gateway's owner closes 80 and 443.** The allows are `host admit`'s, granted with `--public-ports`, so once DNS has moved and the old gateway's Caddy is gone, its owner admits the site again without `--public-ports`, from a fresh request; until then the ports stay open and serve nothing of this deployment's."

Run: `grep -n "br-+\|after-wireguard\|/etc/paisans/authorized_keys\|/var/lib/paisans/config\|/var/lib/paisans/granted\|host prepare.*\(installs Docker\|opens\|softdog\|ufw\)" README.md`
Expected: nothing. A hit is a passage still giving `host prepare` a root step or naming a path phase 6 moved; rewrite it to what is merged.

- [ ] **Step 4: Check the README**

Run: `grep -n "\*host prepare manages the login user's\|\*\`host prepare\` takes a blank host\|\*host prepare owns its rules\|\*Docker starts after the mesh interface\*\|\*The watchdog\*" README.md docs/*.md docs/guides/*.md`
Expected: nothing; every reference by section name uses the new names (*host prepare manages the deployment's user's authorized keys*, *`host prepare` takes an admitted host to an apply-able state*, *host admit owns its rules, and only its rules*, *The deployment's Docker and its mesh at boot*, *The watchdog is the owner's to grant*). Fix each hit.

Run: `git diff README.md | perl -CSD -ne 'print if /^\+.*\x{2014}/'; git diff README.md | grep -n "^+.*\(no longer\|used to\|previously\)"`
Expected: nothing, but for the quoted `-v` output of `host prepare`'s plan, whose words are the program's.

Run: `perl -ne 'print "$ARGV\t$.\t$1\n" while /\]\(([^)#]+)/g' README.md docs/commands.md docs/guides/host-owners.md | grep -v http | while IFS="$(printf '\t')" read f n p; do d=$(dirname "$f"); [ -e "$d/$p" ] || echo "broken $f:$n $p"; done`
Expected: nothing.

- [ ] **Step 5: Commit**

```bash
git add README.md
git commit -m "kacl-skip: docs: describe host prepare as the deployment's user's own step

host prepare installs nothing: it checks the admission record against
the declaration, then writes the operators' keys in the user's own home
and the configuration store as the user's units, with the keys' sidecar
in the deployment's directory. Every other passage that gave it a root
step (opening a port, installing Docker, arming the watchdog, a root
owned file) names host admit, and every reference to a renamed section
follows it.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: The deployment agent rules

**Files:**
- Modify: `docs/deployment-agent-rules.md`

**Interfaces:**
- Consumes: phase 5 Task 15's bullet under *Never without the human present*; founder question 1's answer.
- Produces: the rule Task 5's sudo paragraph and Task 2's entries agree with.

- [ ] **Step 1: Check what phase 5 wrote**

Run: `grep -n "host admit\|host evict" docs/deployment-agent-rules.md`
Expected: phase 5's bullet under *Never without the human present*: "Admitting a deployment to a host or evicting one (`paisans host admit`, `paisans host evict`): it is the host owner's root step on their own machine". Without it, add that bullet first.

- [ ] **Step 2: The owner's actions, explained**

Only with founder question 1 answered; land the parts approved.

Under *Decision authority*, in **A human decides:**, after "when a service is exposed to the public internet", add "; whether a host takes a deployment, and what its owner grants it".

Under *Blast radius*, after the paragraph beginning "The distinction between the last two tiers matters", add:

```markdown
**Admitting and evicting are the host owner's.** A host's owner is often not
one of the deployment's operators, and the machine is theirs, so these are
never an agent's to run, whoever asks. An agent never answers `host admit`'s
prompt for the request's digest or `host evict`'s for the user's name, and
never writes a sudoers rule or anything else that would let either run
without a person at the terminal. An agent may run `paisans host request`,
which reads the configuration and reaches no host, and hand the file to the
operator; the digest goes to the owner by another channel than the file, or
the comparison proves nothing. When a run reports a site pending or down, or
`site remove` prints the `host evict` line, the agent reports it to a human
with that line, and runs nothing on the owner's behalf.
```

- [ ] **Step 3: Check the rules**

Run: `grep -n "unattended\|NOPASSWD" README.md docs/deployment-agent-rules.md docs/commands.md`
Expected: README's sudo section saying `host admit` and `host evict` never run unattended (Task 5, Step 6), the commands entries' "never by an agent unattended", and the agent rules. No sentence saying an owner's command other than `host deployments` may run unattended.

Run: `git diff docs/deployment-agent-rules.md | perl -CSD -ne 'print if /^\+.*\x{2014}/'`
Expected: nothing.

The file stays generic: no host, product or community named in what this task adds (`AGENTS.md`).

- [ ] **Step 4: Commit**

```bash
git add docs/deployment-agent-rules.md
git commit -m "kacl-skip: docs: say why admitting and evicting are never an agent's

A host's owner is often not one of the deployment's operators, and the
machine is theirs, so admit and evict run only with a person at the
terminal: an agent never answers their prompts or writes a rule that
lets them run alone, and sends no digest by the channel that carried the
request. What an agent may do, a request and a report, is said beside
it.

Founder decision for the tier; the rest proposed and approved as founder
question 1 of the phase 9 plan records.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Adjust the body's last paragraph to what was approved.

---

### Task 8: Doctor's checks, as phase 7 lands them

Last, after phase 7 has merged into `develop` (founder decision: phase 9 runs alongside phase 7 and documents its checks as they land).

**Files:**
- Modify: `README.md` (*`paisans doctor` reports what is stuck and how to recover*)
- Modify: `docs/commands.md` (`### `paisans doctor``)
- Modify: `docs/development.md` (the doctor paragraphs under *Run*)

**Interfaces:**
- Consumes: phase 7's merged `internal/doctor/rootless.go`, `sudo.go`, `admission.go`, `answer.go`, `reach.go`, `containers.go`, `checks.go`.
- Produces: nothing other tasks read.

- [ ] **Step 1: Bring phase 7 in, and read its wording**

Run: `git fetch origin && git rebase origin/develop && ls internal/doctor/rootless.go internal/doctor/sudo.go internal/doctor/admission.go internal/doctor/answer.go`
Expected: phase 7 merged; the four files. Without them, stop and wait for phase 7.

Run: `grep -n "Section[A-Za-z]* *= \"" internal/doctor/*.go | grep -v _test.go; grep -n "Command *= " internal/doctor/rootless.go internal/doctor/sudo.go`
Expected: the sections `admission`, `user`, `docker`, `linger`, `sudo rights`, `gateway answer`, and the four commands. Each name and command in Steps 2 to 4 is checked against these; where phase 7 merged another, the text uses phase 7's.

Run: `grep -n "Level: Fail\|Level: Warn" internal/doctor/rootless.go internal/doctor/sudo.go internal/doctor/admission.go internal/doctor/answer.go internal/doctor/reach.go`
Expected: what fails and what warns, as Step 2's rows say.

- [ ] **Step 2: `README.md`'s doctor section**

In the paragraph beginning "A site that does not answer is itself a finding", replace its first sentence with:

```markdown
A site that does not answer is itself a finding, never a crash: every other
check runs with the sites that did. Whether it is pending, its host not yet
having admitted the deployment, or down, having joined and gone quiet, is
read from the deployment, never from the site, as `apply` reads it (see *A
deployment with sites missing*), and either is a `WARN`, since the deployment
runs on the sites it has; a gateway is a `FAIL`, since `apply` refuses to run
without one.
```

In the paragraph beginning "The checks run at once", replace "everything on a site needs its reach, etcd needs a member that answered" with "everything on a site needs its reach and then its admission record, everything that runs `docker` there needs the site's own Docker to answer, etcd needs a member that answered".

In the checks table, replace the `Reach` row and add five rows after it, and one after `Leftovers`:

```markdown
| Reach | `true` over ssh, on every site asked | the site does not answer and is neither pending nor down, or it is the gateway. A pending or down site is a `WARN`. Each says what the deployment is without while it is gone: the gateway, an etcd vote, a Patroni member, a Garage node, its copy of each clustered app, the apps pinned to it; a pending site with the `paisans host request` line, a down one with the `site remove --host-gone` line |
| Admission | the admission record, `/etc/paisans/admitted.<token>.json`, read as the site's ssh user | it cannot be read, or its user, SSH port, WireGuard port, forwarding or web server differs from `paisans-stack.yaml`. No record, or one without a grant the site's roles need, is pending: a `WARN` (a `FAIL` on the gateway) with the `host request` line, and nothing more is asked of the site |
| User | `id -u`, `id -un`, `id -nG` | the user has uid 0, or is in `root`, `sudo`, `wheel`, `admin`, `adm`, `docker`, `lxd` or `systemd-journal`, the groups `host admit` refuses |
| Docker | `docker version` and `docker info`'s security options, against the user's own socket | the daemon does not answer, is not rootless, or its Engine is older than 29.5. When it does not answer, nothing that runs `docker` on the site is checked |
| Linger | `loginctl show-user <user> --property=Linger` | linger is off: the user's Docker stops when the last login ends and does not start at boot |
| Sudo rights | `LC_ALL=C sudo -n -l` | sudo lets the user run anything but the WireGuard helper, as root, without a password, or does not let it run the helper. An answer it cannot read is a `WARN` |
```

```markdown
| Gateway answer | from this workstation, `/.well-known/paisans/<token>` through each gateway's public address, on 443 and on 80 expecting the redirect, with the check `apply` runs at the end of the gateway's step | any other answer: something else on the host holds the port, which the deployment's user cannot see, a DNAT rule least of all |
```

In the `Leftovers` row, replace any phrase phase 6 left saying it "reads as root" with what the merged `hostcheck.Inspect` does (run `grep -n "uid\|id -u" internal/hostcheck/inventory.go` to see).

Replace the paragraph beginning "A container that failed with `cannot assign requested address`" up to "Any other shows its last three log lines." with, keeping its sentences from "A container Docker keeps restarting" on:

```markdown
A container that failed with `cannot assign requested address` started before
the mesh interface at boot: it publishes its port on the mesh address, which
exists only once `wg-quick@psns-<token>` is up, and Docker does not retry a
container whose network setup failed (see *The deployment's Docker and its
mesh at boot*). The advice is `docker start <container>` on the host, as the
deployment's user, or `paisans apply --site <site> --recreate <stack>
--execute`.
```

- [ ] **Step 3: `docs/commands.md`'s doctor entry**

Replace its first paragraph with:

```markdown
Reports what is stuck and how to recover: sites pending admission, down or
unreachable, each site's admission record and rootless setup (the user's
groups, its own Docker and Engine, linger, what sudo allows), etcd quorum and
version, the Patroni leader and why a replica will not promote, containers
that are down or unhealthy, the settings a gated app must enforce, Pocket
ID's active instance, clocks, what is left over, each gateway's answer from
outside, and Caddy's certificates and the DNS token.
```

- [ ] **Step 4: `docs/development.md`**

In the paragraph beginning "`doctor` reaches every site", replace "sites that do not answer and what is lost while they are gone" with "sites that do not answer, pending or down, and what is lost while they are gone; each site's admission record, user, rootless Docker, linger and sudo rights". In the paragraph beginning "Its checks are a graph", add after its first sentence:

```markdown
After each site's reach comes its admission record, which every other node on
the site needs, so a site whose host has not admitted the deployment is one
warning; the site's `docker` node is needed by every node that runs `docker`
there. Pending and down come from the run's classification, asked once, the
first time a site does not answer (`doctorClassify`), and each gateway's
answer from outside from the same check `apply` runs (`doctorGatewayAnswer`).
```

- [ ] **Step 5: Check the docs**

Run: `grep -n "Admission\|Sudo rights\|Gateway answer\|Linger" README.md | head; grep -n "admission record" docs/commands.md docs/development.md`
Expected: the new rows and sentences.

Run: `grep -n "host prepare --site <site> --execute\`, which orders Docker" README.md`
Expected: nothing.

Run: `git diff README.md docs | perl -CSD -ne 'print if /^\+.*\x{2014}/'; git diff README.md docs | grep -n "^+.*\(no longer\|used to\|previously\)"`
Expected: nothing.

- [ ] **Step 6: Run the whole suite**

Run: `gofmt -l cmd internal && go vet ./... && go test ./...`
Expected: nothing listed; PASS. Nothing outside the docs changed.

Run: `git diff origin/develop --stat`
Expected: `README.md`, `docs/commands.md`, `docs/development.md`, `docs/deployment-agent-rules.md`, `docs/guides/host-owners.md`, and this plan.

- [ ] **Step 7: Commit**

```bash
git add README.md docs/commands.md docs/development.md
git commit -m "kacl-skip: docs: describe doctor's rootless checks, pending sites and the gateway's answer

Doctor checks each site's admission record, user, rootless Docker,
linger and sudo rights before anything else on it, reports a pending or
down site as a warning naming what the deployment is without, and asks
each gateway from outside whether it answers as itself. The README's
checks table, the commands reference and the development notes now say
so, in the words the checks print.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

- [ ] **Step 8: Review**

Request the final whole-branch review (superpowers:requesting-code-review). Every finding is fixed before merge, Minors batched into this step.

---

## Self-Review Notes

* **Spec coverage.** The spec's *Documentation*: `docs/commands.md`'s `host request`, `host admit` and `host evict`, with `host allow` and `host disallow` gone (phase 1 removed them; Task 2, Step 5's grep finds none) and `host prepare` and every `--sudo` rewritten (Tasks 2 and 3: the `--sudo` row, the registry paragraph, `host prepare`, `host deployments`); `README.md`'s sections on what `host prepare` does (Tasks 5 and 6), on the login user's authorized keys (Task 6, renamed to the deployment's user's) and on Docker judged by its version (Task 4); the host owners' guide, with installing and checking the binary, comparing the digest, what admitting changes, each flag, and evicting (Task 1), plus the index's `--watchdog`, `--public-ports`, `--data-dir`, `--bridge-netfilter`, `--mesh-lan`, the Caddy admin API warning and the web server hand-off (Task 1's table, *If you run Caddy*, *Behind your own web server*); `docs/deployment-agent-rules.md` (Task 7). The index's "documents doctor's checks as phase 7 lands them" (Task 8, last). The spec's *Egress*, which no document described, is Task 5, Step 5. *A deployment with sites missing*, as `apply` shows it, is Task 3, Step 3.
* **Review Focus.** The install block on arm64 and against the release's own `SHA256SUMS`: Task 1, Step 4. The digest over another channel: Task 1, Step 3's text, Task 2, Step 2, Task 7, Step 2. Every flag named exists: Task 1, Step 4's loop over `hostadmit.go`, Task 2, Step 1, Task 3, Step 5. README's unattended sentence against the agent rules: Task 5, Step 6, checked in Task 7, Step 3. No passage giving `host prepare` a root step: Task 6, Step 3's sweep and its grep.
* **Left to other phases, on purpose.** `CHANGELOG.md` bullets are each behaviour's own phase's. The web server guide and the web server entries are phase 6's rewrite; the host owners' guide links to it. The rekey with sites down is its own spec and plan.
* **Type and name consistency.** Section names Task 5 produces are the ones Tasks 6 and 8 cite: *`host admit` gives a deployment a user of its own, once, as root*, *The deployment's Docker and its mesh at boot*, *host admit owns its rules, and only its rules*, *Egress keeps the deployment off the owner's network*, *The watchdog is the owner's to grant*; Task 6 produces *`host prepare` takes an admitted host to an apply-able state* and *host prepare manages the deployment's user's authorized keys*, which Task 3's entry and Task 6, Step 4's grep cite. Flags are phase 5 Task 14's, listed once in *Interfaces this phase documents*. Doctor's node names are phase 7's *What this phase hands to phase 9*.
* **Placeholder scan.** Three steps are conditional on merged code this plan could not read, each with the text for each case: Task 6, Step 2's `shared` paragraph; Task 6, Step 3's gateway move bullet (founder question 4); Task 8, Step 2's `Leftovers` row. Every other step carries its full text.
