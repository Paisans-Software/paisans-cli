# Rootless Phase 9: Documentation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Revised against develop 6783478a** (phase 6 merged as PR #370; phase 7 in progress on `feat/rootless-doctor`). Phase 6's docs tasks and its final fix rounds rewrote `README.md`, `docs/commands.md`, `docs/development.md` and the guides for the rootless commands, so this plan shrank from nine tasks to seven:
>
> * **Dropped:** the old Task 0 (the branch is rebased on 6783478a, and phases 5 and 6 are in); the old Task 3 (`host prepare`, `apply`'s pending section and `--require-all`, the gateway's `/.well-known` check and `site remove`'s evict line are all in `docs/commands.md`); the restructuring of `README.md`'s host sections into a new `host admit` section (phase 6 kept them under *`host prepare` keeps what is the deployment's user's*, with admit's subsections, ufw ownership and `--enable-ufw` already written; moving them again is churn with nothing gained); the `br-+`, after-wireguard and authorized_keys lock deletions (phase 6 made them); the gateway move bullet (README already says the owner re-admits without `--public-ports`).
> * **Shrunk:** the commands reference to the guide links, the digest's channel and `create`'s ssh user prompt (Task 2); `README.md`'s Docker passage to what still contradicts `internal/hostadmit/docker.go`: a `docker.io` with `docker-compose-v2` is refused, not accepted (Task 4); the `README.md` sweep to the few sentences still wrong (Task 6).
> * **Kept, rewritten:** the host owners' guide (Task 1), now with Ubuntu 22.04 and 24.04 (founder decision, 2026-10-16), mixed releases across data sites with one architecture, `--enable-ufw` with its fallback timer, refusals and lockout recovery, the egress table and its limits (above all: bind admin services to `127.0.0.1` or the LAN address), and evict with what stays after it, each from merged code; the agent rules (Task 3), now saying `host admit --site` can run without a terminal under a NOPASSWD rule, so the rule, not a prompt, is what stops an agent.
> * **Added:** `README.md`'s egress subsection, which nothing describes in the README yet, and the stale ufw sentence in *A host's owner admits a site from a request* (Task 5); `docs/guides/how-a-deployment-protects-itself.md`'s sentence about the owner bounding the key list, whose allowlist phase 1 removed (Task 6).
> * **Deferred:** everything that documents doctor is Task 7, which runs only after phase 7 merges into `develop`. That includes `README.md`'s `cannot assign requested address` advice, which still names `host prepare` and is phase 7's Task 7 to change.

**Goal:** The documentation describes the toolkit as phases 1 to 7 left it: a guide for host owners, `docs/guides/host-owners.md`, from downloading and checking the binary to evicting and what stays after; `docs/commands.md` linking it and saying the digest travels on another channel; `README.md`'s Docker passage matching what `host admit` refuses, an egress subsection, the owner's commands and sudo said truthfully, and the last sentences that give `host prepare` a root step gone; `docs/deployment-agent-rules.md` saying why admit and evict are never an agent's; and, last, doctor's checks as phase 7 lands them.

**Architecture:** Documentation only; no code, no test, no `CHANGELOG.md` bullet. Each task rewrites one document's passages in place, from the merged code: it starts by checking the facts it states against `develop` with the commands it lists, and where the code says otherwise the text follows the code and the commit body names the difference. `README.md` keeps the reasoning, `docs/commands.md` the mechanics, the guide the owner's path; each links to the others by section name. Doctor's task is last because it describes phase 7, which runs alongside this phase (founder decision).

**Tech Stack:** Markdown; `grep`, `perl` for the em dash check, `go run ./cmd/paisans <command> --help` for each command's flags, `make release-binaries` for the guide's checksum step.

**Spec:** `docs/specs/2026-10-15-rootless-and-host-admit.md` (all of it; *Documentation* lists this phase), the phase index `docs/superpowers/plans/2026-10-15-rootless-and-host-admit.md` (*Phase 9*), and phase 7's plan on `feat/rootless-doctor` (`docs/superpowers/plans/2026-10-15-rootless-phase-7-doctor.md`, *What this phase hands to phase 9*).

## Global Constraints

* "`docs/commands.md`: `host request`, `host admit`, `host evict`; `host prepare` and every former `--sudo` rewritten; `host allow`/`disallow` gone. `README.md`: the sections on `host prepare`, the login user's keys and Docker's version. `docs/guides/host-owners.md`: install and check the binary, compare the digest, what admit changes, each grant, evict. `docs/deployment-agent-rules.md`: admit and evict are owner actions." (index, *Phase 9*) Phase 6 delivered most of the first two; this phase finishes them.
* "`host admit` and `host evict` are owner actions; `docs/deployment-agent-rules.md` says an agent never runs them unattended." (index, *Global Constraints*)
* "Ubuntu 22.04 and 24.04 are supported (founder decision, 2026-10-16). [...] Data sites may run different releases, since Postgres's glibc is its image's." (spec, *Settled*) Every data site has the same architecture (`README.md`, *Every data site has the same architecture, whichever it is*).
* "Depends on: phases 5 and 6; runs alongside phase 7 (founder decision), and documents doctor's checks as phase 7 lands them." (index) Doctor's task is last and waits for phase 7's merge.
* Docs describe current state only: no "no longer", "used to", "previously", "now" contrasting with a past, no "was". No rejected alternatives outside `docs/decisions.md`. No em dashes in any line this phase writes; existing em dashes are swept only in lines a task already touches (`AGENTS.md`).
* Every flag a document names is one `cmd/paisans` defines; every internal link resolves; every fenced YAML block parses; no real hostname, address, key or secret (`AGENTS.md`). Examples use `example.org`, `home-a`, `home-b`, `vm`, and the documentation ranges `203.0.113.0/24`, `198.51.100.0/24`.
* "Deleting documentation requires explicit human approval. Rewriting a section in place is an edit; removing it is a deletion." (`AGENTS.md`) Every passage this phase changes is rewritten in place.
* "A tier change is a human decision. Propose it; do not decide it." (`AGENTS.md`, on `docs/deployment-agent-rules.md`) Task 3 writes what the founder decided (founder decision 1).
* Commits: subject `kacl-skip: <lowercase subject>`, a body saying why, `Founder decision.` where it was one, the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
* Never `git stash`; other sessions share the stash. Never push.
* There is no test suite for documentation; each task's checks are run, not asserted, and the full `go vet ./... && go test ./...` runs once at the end, to show nothing outside the docs moved.

## Parallel groups

* **Group A, in parallel, each in its own worktree:** Task 1 (`docs/guides/host-owners.md`), Task 2 (`docs/commands.md`), Task 3 (`docs/deployment-agent-rules.md`), and the `README.md` chain Task 4, then Task 5, then Task 6, run in that order by one worker since all three edit `README.md` (Task 6 also edits `docs/guides/how-a-deployment-protects-itself.md` and `docs/guides/when-an-operator-leaves.md`, which no other Group A task touches). The guide's headings are fixed in Task 1's *Produces*, so Tasks 2, 3 and 5 link to them without waiting for it.
* **Group B, after phase 7 merges into `develop` and after Group A:** Task 7, doctor, with the final checks and review.

## Review Focus

* **An owner copies the guide's commands onto an arm64 host, or for a release the operators named.** Every command in the guide's install block must run as written on Ubuntu 22.04 and 24.04 with only `curl` and coreutils, and the checksum line must accept `SHA256SUMS` as the release workflow writes it. Task 1, Step 4 runs the block against `make release-binaries`'s own `dist/`.
* **An owner enables ufw and loses SSH.** The guide must say, before the owner runs admit, that the fallback timer turns ufw off within 10 minutes unless a new login gets through, that a reboot before it fires leaves ufw on, and what to do at a console then. Task 1, Steps 1 and 3.
* **An owner compares the digest from the same email that carried the request.** That checks nothing. The guide, the `host request` and `host admit` entries and the agent rules each say the digest is compared over another channel the owner trusts (Task 1, Step 3; Task 2, Step 2; Task 3, Step 2).
* **An owner's admin service bound to `0.0.0.0` is reachable from the deployment.** The guide must tell the owner, in a sentence they cannot miss, to bind admin services to `127.0.0.1` or the LAN address (Task 1, Step 3; Task 5, Step 2).
* **A flag named in a document that the command does not take.** Each must be one `runHostAdmit` or `runHostEvict` defines (Task 1, Step 4; Task 2, Step 3).
* **`README.md` saying admit and evict cannot run unattended when `host admit --site` can.** Admit from a request asks for the digest and evict always asks for the user's name, but admit with `--site`, every grant passed and ufw active, asks nothing; with a NOPASSWD rule it runs without a terminal. README and the agent rules must say the rule is what forbids it (Task 5, Step 3; Task 3, Step 2).

## Rulings

1. **Phase 6's text stands where it is right.** This phase edits only sentences that contradict merged code, are missing, or are the guide's; it does not restructure sections phase 6 laid out.
2. **Each task checks its facts against `develop` before writing**, with the commands in its first step. Where the code differs from this plan's text, the text follows the code, and the commit body names the difference.
3. **The guide is the owner's whole path; `README.md` is the reasoning; `docs/commands.md` is the mechanics.** The guide repeats no reasoning beyond a sentence per grant and links to `README.md` and `docs/commands.md` for the rest.
4. **Doctor's documentation is the last task**, written from phase 7's merged code, and covers `README.md`'s doctor section (and the `cannot assign requested address` paragraph in it), `docs/commands.md`'s `paisans doctor` entry and `docs/development.md`'s doctor paragraphs.
5. **The guide's examples name no real release.** It uses `v=0.3.0` with a comment saying to use the release the operators name.
6. **No `cmd/paisans/help.go` change.** Its `host admit`, `host evict` and `host request` texts already match the merged flags and say "Never run by an agent unattended"; this phase is docs only.

## Founder decisions

1. **The agent rules' new lines are approved** (Task 3), with one more sentence: the founder may authorise an agent to run admit and evict, prompts included, on named test servers, and that authorisation never extends to any other host.
2. **No release signatures or attestations for now.** The guide says plainly that `SHA256SUMS` proves a download is whole, not who published it.
3. **An old gateway's 80 and 443 are closed by its owner re-admitting the site** without `--public-ports` once DNS has moved. `README.md` already says so; the guide's *Changing what you granted* repeats it for the owner.
4. **Ubuntu 22.04 and 24.04 are both supported**, with one host profile (2026-10-16), and data sites may run different releases on one architecture.

## Interfaces this phase documents

From phases 5 and 6 (merged, at 6783478a):

| Name | Where |
|---|---|
| `host admit` flags: `--request <file>`, `--site <name>`, `--ssh <login>`, `--config`, `--watchdog`, `--public-ports`, `--data-dir <path>`, `--bridge-netfilter`, `--mesh-lan`, `--enable-ufw`, `--execute` | `runHostAdmit`, `cmd/paisans/hostadmit.go` |
| `host evict` flags: `--request <file>`, `--deployment <id>`, `--user <name>`, `--site <name>`, `--ssh <login>`, `--config`, `--data-dir <path>`, `--execute` | `runHostEvict`, same file |
| the Engine floor `29.5.0`; the refusal of a Docker that is neither `docker-ce` nor has `dockerd-rootless-setuptool.sh` | `hostadmit.MinRootlessEngine`, `planDocker`, `internal/hostadmit/docker.go` |
| the host check's floors `25.0.0` and `2.24.0`, against the user's own daemon | `hostcheck.MinEngine`, `MinCompose` |
| the ufw fallback: `paisans-ufw-fallback`, 600 seconds, disarmed by `systemctl stop paisans-ufw-fallback.timer` | `hostadmit.FallbackUnit`, `hostadmit.DisarmUFW`, `internal/hostadmit/ufw.go` |
| the egress table and unit, `inet paisans_<token>`, `paisans-<token>-egress.service`, ordered before the user's manager | `internal/hostadmit/egress.go` |
| the softdog unit, before `systemd-logind.service` | `internal/hostprep/profiles/ubuntu/watchdog-module.service.tmpl` |
| the record, `/etc/paisans/admitted.<token>.json`; what admit installed, `/etc/paisans/installed` | `admission.Path`; `docs/commands.md`, *`paisans host admit`* |
| the golden plans, `docker-blank.txt`, `docker-blank-jammy.txt`, `egress-*.txt`, `bridge-*.txt`, `datadir-*.txt`, `webserver-caddy-*.txt` | `internal/hostadmit/testdata/plans/` |
| the release assets `paisans-linux-amd64`, `paisans-linux-arm64`, `SHA256SUMS` | `Makefile` (`release-binaries`), `.github/workflows/release.yml` |

From phase 7 (Task 7 only; *What this phase hands to phase 9* in its plan): the nodes, in the graph's order, `reach <site>`, `admission <site>`, `user <site>`, `docker <site>`, `docker engine <site>`, `linger <site>`, `sudo rights <site>`, the existing ones, then `gateway answer <site>` after `leftovers <site>` and before the DNS nodes; `doctor.UserCommand`, `DockerCommand`, `LingerCommand`, `SudoCommand` (`LC_ALL=C sudo -n -l`); each of these failing as a `FAIL`; pending and down as `WARN`, a gateway as `FAIL`; pending skipping the rest of the site, and a daemon that does not answer skipping every check that runs `docker` there; the `cannot assign requested address` advice.

---

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `docs/guides/host-owners.md` | create | the owner's path: what you need, install and check, read and compare, grant, ufw, admit, SSH recovery, what admit changes, what the deployment can reach, the web server hand-off, Caddy's admin API, change, evict, what stays |
| `docs/commands.md` | modify | guide links in `host admit` and `host evict`; the digest's channel in `host request` and `host admit`; `create`'s ssh user; last, `paisans doctor` |
| `docs/deployment-agent-rules.md` | modify | why admit and evict are the owner's |
| `README.md` | modify | *What it does*'s command block; *Docker is judged by its version, not its source* and *Docker comes from Docker's repository when it is absent*; an egress subsection; *A host's owner admits a site from a request*'s ufw sentence; *A sudo that wants a password*; *The watchdog*'s softdog sentence; *Preflight*'s Docker sentence; last, doctor |
| `docs/guides/how-a-deployment-protects-itself.md` | modify | the owner's bound on `ssh.keys`, and a link to the host owners' guide |
| `docs/guides/when-an-operator-leaves.md` | modify | only if Task 6 finds a stale reference |
| `docs/development.md` | modify | last, doctor's graph paragraphs |

---

### Task 1: The host owners' guide

Group A.

**Files:**
- Create: `docs/guides/host-owners.md`

**Interfaces:**
- Consumes: the release assets; `host admit`'s and `host evict`'s flags; the egress table, the ufw fallback and evict's leftovers as *Interfaces this phase documents* names them; the spec's *What it does* (steps 9 to 12), *`paisans host evict`*, *Egress* and its *Known limits*.
- Produces: `docs/guides/host-owners.md`, with the headings `## What you need`, `## 1. Install paisans and check it`, `## 2. Read the request and compare its digest`, `## 3. Decide what to grant`, `## 4. ufw`, `## 5. Admit`, `## If SSH stops answering after admit enables ufw`, `## What admitting changes on your machine`, `## What the deployment can reach`, `## Behind your own web server`, `## If you run Caddy`, `## Changing what you granted`, `## Evicting`, `## What stays after evicting`, which Tasks 2, 3 and 5 link to.

- [ ] **Step 1: Check the facts the guide states**

Run: `go run ./cmd/paisans host admit --help 2>&1; go run ./cmd/paisans host evict --help 2>&1`
Expected: every flag in *Interfaces this phase documents*. A flag spelled otherwise is spelled the code's way in Step 3's text.

Run: `grep -n "FallbackUnit\|DisarmUFW\|on-active\|IPV6\|deny\|reject" internal/hostadmit/ufw.go | head -30`
Expected: the 600 second timer, the disarm command, the refusals for an owner's deny or reject rule and for `IPV6` not `yes` with sshd on IPv6. Step 3's *4. ufw* and the recovery section follow them.

Run: `grep -n "MinRootlessEngine\|docker-ce\|Setuptool\|Snap" internal/hostadmit/docker.go; grep -n "nfs\|cifs\|fuse\|GB" internal/hostadmit/datadir.go 2>/dev/null | head`
Expected: the floor; the refusal of a Docker that is neither `docker-ce` nor has the setup tool; the snap refusal; the refused filesystems and free-space warnings (find the file with `grep -ln "nfs" internal/hostadmit/*.go` if `datadir.go` is not it). Step 3's sentences follow the code.

Run: `grep -n "ID\|VERSION_ID\|22.04\|24.04" internal/hostprep/profile*.go internal/hostprep/profiles/*.go 2>/dev/null | head`
Expected: both releases registered. *What you need* names both.

Run: `grep -n "Before=\|RequiredBy=\|Requires=" internal/hostadmit/egress.go`
Expected: the egress unit before the user's manager. *What the deployment can reach* says an owner's own accept rule is kept in a unit of theirs ordered after it, since the table is loaded again at boot and at every admit; if the code loads the table differently, the sentence follows the code.

Run: `grep -n "2019\|admin" internal/hostadmit/webserver.go | head`
Expected: the Caddy admin API warning. *If you run Caddy* follows its wording.

- [ ] **Step 2: Read what a plan prints**

Read `internal/hostadmit/testdata/plans/docker-blank.txt`, `docker-blank-jammy.txt`, one `egress-*.txt`, one `bridge-*.txt` and one `datadir-*.txt`. The guide describes in words what they show, so every step *What admitting changes on your machine* names must appear in them, and what differs between 22.04 and 24.04 (AppArmor, cgroup delegation) is said where the guide mentions it.

- [ ] **Step 3: Write the guide**

Create `docs/guides/host-owners.md`:

````markdown
# Admitting a deployment to your machine

This guide is for the owner of a machine, a homelab box or a VPS, that a
community has asked to run part of its paisans deployment. You do not need
the community's configuration, and you do not run its services. You run one
command as root, once, to admit the deployment, and again whenever you change
what you grant it. Everything the community's operators do after that runs as
a user of its own on your machine, with its own rootless Docker, fenced off
your network.

The reasoning is in `README.md`, under *A host's owner admits a site from a
request* and *`host prepare` keeps what is the deployment's user's*; every
flag is under *`paisans host admit`* and *`paisans host evict`* in
[docs/commands.md](../commands.md).

## What you need

* **Ubuntu 22.04 or 24.04, amd64 or arm64.** Admit refuses any other system,
  naming what it supports. If your machine will hold the deployment's
  database (a data site), it must have the same architecture as the
  deployment's other data sites; the Ubuntu release may differ.
* **ufw installed.** Admit adds the deployment's rules to ufw and never
  installs it. If ufw is not active, see step 4.
* **Docker Engine 29.5 or later from Docker's repository, or no Docker.**
  With none, admit installs Docker's packages from Docker's repository and
  keeps their system daemon disabled, so only the deployment's own daemon
  runs. It never upgrades or removes a Docker you already run: an older
  Engine, a snap, or a Docker that is not Docker's `docker-ce` and has no
  `dockerd-rootless-setuptool.sh` (Ubuntu's `docker.io`, for one) is refused,
  with the reason. Your own Docker keeps running beside the deployment's,
  untouched.
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
nothing about who published the release, which the Paisans-Software
organisation's release workflow does.

Use the release the operators name. A paisans that reads another request
format refuses the request, naming the version it found, rather than read it
in part.

## 2. Read the request and compare its digest

```sh
sudo paisans host admit --request home-a.admit
```

Without `--execute` it changes nothing. It shows the site and its roles, the
user it will make (`paisans-<token>`, unless the operators named another),
the grants the site's roles need, every step it would take, and the
request's **digest**: sixteen characters in four groups, Eg:
`7k2m-q9xd-4r8f-zt1c`.

The request carries the keys that will get a shell on your machine, so
anyone who could change it on its way could put their own key in it. The
digest is how you know it is the file the operators wrote: **ask an operator
for the digest over a different channel than the one that brought the file**,
a call or a message you know is theirs, and compare. A digest that travelled
beside the file in the same email proves nothing, since whoever changed the
file could change the digest too. When you admit with `--execute`, paisans
asks you to type the digest at the terminal, so the comparison cannot be
skipped.

A request is read as strictly as a configuration: an unknown version, a field
missing or unknown, a user that is `root` or another deployment's, a key that
is not a plain public key, or any byte the operators' `host request` would
not have written, refuses it before anything else.

## 3. Decide what to grant

Admit gives nothing you did not grant. The plan lists the grants the site's
roles need; at a terminal, admit asks for each one your flags leave out,
saying what it gives, and elsewhere it stops. A grant the site does not need
is refused, since each weakens your machine for nothing.

| Flag | What it lets the deployment do | What it costs you |
|------|--------------------------------|-------------------|
| `--watchdog` | Patroni, the database, uses the machine's watchdog to fence itself: if it hangs while it leads, the machine reboots before another site's promotion leaves two primaries taking writes. A data site needs it. | **It can reboot your machine.** The device is given to the deployment's user. One deployment per machine can hold it; a second is refused. Where there is no hardware watchdog, admit loads `softdog`. |
| `--public-ports` | The deployment binds ports 80 and 443, which a gateway or a monitor running Paisans' own Caddy serves on. | **Every local user can then bind ports 80 to 1023**: admit lowers `net.ipv4.ip_unprivileged_port_start` to 80 for the whole machine, and opens 80/tcp, 443/tcp and 443/udp in ufw. Before granting it, admit checks that nothing else holds 80 or 443 (a listener, a container of your own Docker, or a forwarding rule), and refuses naming what does. |
| `--data-dir <path>` | The deployment's Docker data (images, containers, volumes: the database on a data site, the stored objects on a storage site) goes under `<path>` instead of the user's home. | Nothing, if the disk has room. `<path>` must exist, be a directory and not a link, and be root's with no group or other write bit. Network and FUSE filesystems are refused, since Docker's storage cannot use them; the plan states the free space, and warns when it is short. |
| `--bridge-netfilter` | Admit loads `br_netfilter`, which the deployment's Docker needs, on a machine with bridges of its own (libvirt's, LXD's, yours). | **Traffic across your own bridges then passes the firewall's FORWARD chain**, which it skipped before, and whose policy is often to drop. The plan names each bridge and quotes the policy. Without the flag admit stops on such a machine; where the module is loaded already, or the only bridges are Docker's, it needs no flag. |
| `--mesh-lan` | The deployment's sites may reach each other on your LAN, at the private prefix of your machine's own network. | The deployment can send its mesh traffic to that LAN. Without it, sites on one LAN reach each other through their public addresses, which needs a router that loops them back. |

**Forwarding has no flag.** A site with an endpoint relays mesh traffic
between sites that cannot dial each other, so admit turns on
`net.ipv4.ip_forward` for it and says so in the plan: routing between every
interface on your machine, which your firewall's forward policy then governs.

## 4. ufw

When ufw is active, admit adds the deployment's rules and changes nothing
else: it never sets ufw's default policy, and never removes a rule of yours.

When ufw is installed and not active, admit can turn it on, with
`--enable-ufw` or a yes at the terminal. It adds the deployment's rules, then
an allow for every TCP port sshd listens on, tagged `paisans: ssh`, then arms
a timer, `paisans-ufw-fallback`, that runs `ufw disable` in 10 minutes, and
only then enables ufw. ufw applies every rule as it is enabled, so SSH is
allowed from its first moment. Admit then asks you to open a **new** SSH
session to the machine and type yes; that stops the timer, and ufw stays on.

Admit refuses to enable ufw, with or without the flag, when:

* it cannot read a port sshd listens on, or ufw's own rules;
* ufw holds a deny or reject rule of yours, in any form, which it lists:
  ufw applies the first rule a connection matches, and admit does not
  override yours;
* sshd listens on IPv6 and `IPV6` in `/etc/default/ufw` is not `yes`, since
  ufw would then drop SSH over IPv6;
* the timer cannot be armed.

Admit allows only the TCP ports sshd listens on, and cannot see DROP rules
written by hand in `/etc/ufw/before.rules`, `before6.rules` or `user.rules`,
nor another way in such as sslh, dropbear or mosh. If you rely on any of
them, enable ufw yourself after checking them. Without `--enable-ufw` and off
a terminal, admit refuses before changing anything and prints the commands
that enable ufw by hand, each `sudo ufw allow <port>/tcp` before `sudo ufw
enable`.

## 5. Admit

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
machine: `paisans host admit --site home-a --ssh you --execute`. The steps
are the same.

## If SSH stops answering after admit enables ufw

**Do not reboot.** The timer lives only until the next boot: a reboot before
it fires leaves ufw enabled, and you locked out.

* **If you still have a session open**, find why the new one failed, then
  either allow it (`sudo ufw allow <port>/tcp`) and stop the timer with `sudo
  systemctl stop paisans-ufw-fallback.timer`, or let the timer turn ufw off.
* **If you have no session**, wait. Within 10 minutes of admit enabling ufw,
  the timer runs `ufw disable`, and SSH answers again. Admit printed the time.
  Then find what blocked SSH (a port sshd listens on that admit did not see,
  sslh, a rule in `before.rules`), and run admit again with `--enable-ufw`
  once it is fixed, or enable ufw yourself.
* **If the machine rebooted before the timer fired**, ufw is on and nothing
  will turn it off. Use the console your provider or the machine itself gives
  you, outside SSH, and run `sudo ufw disable`, or `sudo ufw allow
  <port>/tcp` for the port SSH listens on.

## What admitting changes on your machine

* **Docker**, where there was none: Docker Engine from Docker's repository,
  with its rootless extras, and the tools the deployment needs (`uidmap`,
  `nftables`, `wireguard-tools`, `git`), with Docker's system daemons kept
  disabled. Where Docker is there, only what is missing, and nothing is
  upgraded. What admit installed is listed in `/etc/paisans/installed`.
* **The user**, `paisans-<token>`, with a home, subordinate IDs of its own,
  and linger, so its Docker runs with nobody logged in and starts at boot. A
  user that already exists and is root, or is in `sudo`, `wheel`, `admin`,
  `adm`, `docker`, `lxd` or `systemd-journal`, is refused: each reaches past
  the user.
* **cgroup delegation**, a drop-in for every user's systemd manager, so the
  deployment's containers can be limited in CPU and memory; on Ubuntu 22.04
  it adds what 22.04 does not delegate by default. It is shared by every
  deployment admitted here.
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
* **Egress**: an nftables table, `inet paisans_<token>`, loaded at boot by
  `paisans-<token>-egress.service` before the user's manager starts (see
  *What the deployment can reach*).
* **With `--watchdog`**, a udev rule giving the watchdog device to the user,
  and `softdog`, loaded by a unit of admit's, where there is no hardware
  watchdog.
* **With `--public-ports`, or forwarding**, `/etc/sysctl.d/60-paisans.conf`,
  and the values it replaced recorded in `/etc/paisans/sysctl.before`.
* **The admission record**, `/etc/paisans/admitted.<token>.json`, root's and
  readable by all: the user, what you granted, the ports opened, the digest.
  It is how the deployment's own commands learn what they were given.
* **The registry**, `/var/lib/paisans/registry.json`, so a second deployment
  on this machine cannot take this one's names, ports or mesh subnet.
* **The operators' keys**, in the user's `~/.ssh/authorized_keys`. From then
  on the operators maintain them themselves, as that user.

To see it afterwards:

```sh
sudo -l -U paisans-1a2b                       # the helper, and nothing else
sudo nft list table inet paisans_1a2b
sudo ufw status | grep paisans-1a2b
cat /etc/paisans/admitted.1a2b.json
ls /etc/paisans/                             # one admitted.<token>.json per deployment
```

**Anyone the operators trust gets a shell as that user.** Adding or removing a
co-operator is theirs to do without you. What protects you is that the user
is all that shell reaches.

## What the deployment can reach

The egress table keeps the deployment's processes, its containers included,
off your machine and your network. It lets through the mesh and DNS to your
resolvers, and drops loopback, the private and link-local ranges, your
machine's own addresses, broadcast and multicast, and your on-link IPv6
prefixes, which admit reads when it runs: if your LAN's IPv6 prefix changes,
run admit again. On a gateway, or a site behind your web server, it allows
TCP 80 and 443 on your machine's own addresses. With `--mesh-lan` it allows
the mesh to dial your LAN.

What it cannot do, and what you do about it:

* **Bind your admin services to `127.0.0.1` or to your LAN address.** A
  service of yours bound to every interface (`0.0.0.0` or `::`) is also
  bound to the machine's mesh address, which the deployment must reach and
  so is not dropped: the deployment, and its other sites over the mesh, can
  reach it there.
* **The deployment's user can listen on any port no service of yours
  holds.** A client of yours that means a service which is down, or bound to
  the other address family (`127.0.0.1` while the client tries `[::1]`), can
  reach the deployment's listener instead.
* **Routes the table does not read.** An on-link public IPv4 subnet, or a
  VPN of yours that routes non-private IPv4 ranges, is reachable.

If the deployment needs something on your network, its operators ask you.
Letting it through is a rule of yours, outside paisans, kept in a unit of
your own ordered after `paisans-<token>-egress.service`, since the table is
loaded again at boot and whenever admit runs.

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

Run admit again with the request for the site as it is now and the flags you
grant now. It changes only what the flags govern. To add a grant the site
did not need before, ask the operators for a fresh request: a grant the
request does not need is refused.

A change of the site's SSH port, WireGuard port, user, endpoint or mesh
subnet also needs you, since each is a root change: the operators' commands
refuse with the `host request` and `host admit` lines to run. When the
operators move their gateway off your machine, they send a fresh request
once DNS has moved; admitting it without `--public-ports` closes 80 and 443
and restores the low-port setting, unless another deployment here holds it.

## Evicting

```sh
sudo paisans host evict --request home-a.admit --execute
sudo paisans host evict --deployment f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01 --user paisans-f2a9 --execute
```

The second is for when you no longer have the request. Paisans asks you to
type the user's name, then stops the deployment's processes and its mesh,
and removes the user with its home and Docker data, `/srv/paisans/<token>`,
the helper and its sudoers line, the watchdog rule, the egress table and its
unit, the deployment's ufw rules but one, the record and the registry entry.
It deletes a user only when it is the deployment's: the record's, or, with
no record, `paisans-<token>`. With no record, pass `--data-dir` if you gave
admit one, so evict finds the Docker data. On a data site the plan warns that
a Patroni that outlasts Docker's stop timeout is killed with the watchdog
armed, which reboots the machine.

The operators' `paisans site remove` does what the deployment's user can, and
ends by printing the `host evict` line for you.

## What stays after evicting

* **The deployment's SSH allow**, with a warning: it may be the rule your own
  login arrives by. Delete it yourself once nothing else needs it.
* **ufw, enabled**, if admit enabled it, with its `paisans: ssh` allows.
  Disabling it is yours.
* **Files and settings other deployments share**: the cgroup drop-in,
  `modules-load.d/paisans.conf` and `60-paisans.conf` stay while another
  deployment is admitted, and each sysctl is restored to the value it had
  before the first admit only once no admitted deployment needs it.
* **`br_netfilter` and the watchdog's module**, loaded until the next boot.
* **What admit installed**: the packages `/etc/paisans/installed` lists
  (Docker's, its rootless extras, `uidmap`, `wireguard-tools`), Docker's apt
  key and source, and the `docker` group, even when the last deployment goes.
  Something else may use them by then, so removing them is yours; the plan
  names each.
* A user you deleted by hand before evicting can leave an empty
  `/etc/systemd/system/user@<uid>.service.requires` behind.
````

- [ ] **Step 4: Check the guide**

Run: `make release-binaries && (cd dist && shasum -a 256 -c SHA256SUMS)`
Expected: each binary `OK`. Then `rm -rf dist`.

Run: `for f in --request --site --ssh --config --watchdog --public-ports --data-dir --bridge-netfilter --mesh-lan --enable-ufw --execute --deployment --user; do grep -q -- "\"${f#--}\"" cmd/paisans/hostadmit.go || echo "missing $f"; done`
Expected: nothing.

Run: `perl -CSD -ne 'print "$.: $_" if /\x{2014}/' docs/guides/host-owners.md; grep -n "no longer\|used to\|previously" docs/guides/host-owners.md`
Expected: nothing.

Run: `grep -nE "[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}" docs/guides/host-owners.md`
Expected: only `127.0.0.1` and `0.0.0.0`.

Read the rendered file once, top to bottom, as an owner who has never seen paisans would: each command in order, each flag's cost before its flag is used, ufw's recovery before the command that enables it.

- [ ] **Step 5: Commit**

```bash
git add docs/guides/host-owners.md
git commit -m "kacl-skip: add a guide for host owners admitting a deployment

An owner who lends a machine needs the CLI and nothing else, and needs to
know what they agree to before they run it as root: how to install and
check the binary, how the digest proves the request is the operators',
what each grant costs, how admit enables ufw and how to get back in if
SSH is blocked, what the deployment can still reach and why admin
services belong on 127.0.0.1 or the LAN address, how to evict, and what
stays after.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: `docs/commands.md`, the guide and the digest's channel

Group A.

**Files:**
- Modify: `docs/commands.md` (`host request`, `host admit`, `host evict`, `create`)

**Interfaces:**
- Consumes: Task 1's headings.
- Produces: the entries' links to the guide.

- [ ] **Step 1: Read the entries as merged**

Run: `sed -n '/^### `paisans host request`/,/^### `paisans prune`/p' docs/commands.md; sed -n '/^### `paisans create`/,/^### `paisans init`/p' docs/commands.md`
Expected: phase 6's entries. Each edit below is to a sentence quoted here; where phase 6's wording differs, edit the sentence it wrote that carries the same fact.

- [ ] **Step 2: The edits**

In `### `paisans host request``, replace "The owner compares that digest with the one the file shows them." with:

```markdown
The owner compares that digest with the one `host admit` shows them for the
file; tell it to them over another channel than the one that carries the
file, such as a call, or the comparison proves nothing.
```

In `### `paisans host admit``, replace its `* **Alone:**` bullet with:

```markdown
* **Alone:** always, by the host's owner, and never by an agent unattended:
  it is the owner's root step on their own machine.
  [guides/host-owners.md](guides/host-owners.md) is the owner's whole path,
  from installing the binary to evicting.
```

and in its `* **`--execute`:**` bullet, replace "With `--request` it first asks for the request's digest at a terminal." with "With `--request` it first asks for the request's digest at a terminal, which the owner has from an operator over another channel than the one that carried the file."

In its `* **ufw not active:**` bullet, append: "If SSH stops answering, see *If SSH stops answering after admit enables ufw* in [guides/host-owners.md](guides/host-owners.md)."

In `### `paisans host evict``, replace its `* **Alone:**` bullet with:

```markdown
* **Alone:** always, by the host's owner, and never by an agent unattended:
  it deletes the deployment's user and data as root. `site remove` ends by
  printing this command for them; see *Evicting* and *What stays after
  evicting* in [guides/host-owners.md](guides/host-owners.md).
```

In `### `paisans create``, after its first paragraph, add:

```markdown
Each machine's SSH user may be left empty, which is the deployment's own
user, `paisans-<token>`, that the host's owner's `host admit` makes.
```

- [ ] **Step 3: Check**

Run: `grep -n "host-owners" docs/commands.md`
Expected: four links, in `host admit` (two) and `host evict`, all to `guides/host-owners.md`.

Run: `git diff docs/commands.md | perl -CSD -ne 'print if /^\+.*\x{2014}/'; git diff docs/commands.md | grep -n "^+.*\(no longer\|used to\|previously\)"`
Expected: nothing.

- [ ] **Step 4: Commit**

```bash
git add docs/commands.md
git commit -m "kacl-skip: link the host owners' guide from the owner's commands

The owner's entries now point at the guide that walks the whole path,
and say the digest goes to the owner over another channel than the
request, since a digest beside the file it covers proves nothing.
create's entry says a blank SSH user is the deployment's own.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: The deployment agent rules

Group A.

**Files:**
- Modify: `docs/deployment-agent-rules.md`

**Interfaces:**
- Consumes: the bullet phase 5 wrote under *Never without the human present*; founder decision 1.
- Produces: the rule Task 5's sudo paragraph and the commands entries agree with.

- [ ] **Step 1: Check what is there**

Run: `grep -n "host admit\|host evict\|A human decides" docs/deployment-agent-rules.md`
Expected: phase 5's bullet "Admitting a deployment to a host or evicting one (`paisans host admit`, `paisans host evict`): it is the host owner's root step on their own machine", and the *A human decides* paragraph.

- [ ] **Step 2: The owner's actions, explained**

Under *Decision authority*, in **A human decides:**, after "when a service is exposed to the public internet", add "; whether a host takes a deployment, and what its owner grants it".

Under *Blast radius*, after the paragraph beginning "The distinction between the last two tiers matters", add:

```markdown
**Admitting and evicting are the host owner's.** A host's owner is often not
one of the deployment's operators, and the machine is theirs, so these are
never an agent's to run, whoever asks. Evicting always asks for the
deployment's user name at a terminal, and admitting from a request asks for
its digest; admitting from the configuration may ask nothing, and a
NOPASSWD sudoers rule would let it run without a person, so this rule, not a
prompt, is what stops an agent. An agent never answers either prompt, and
never writes a sudoers rule or anything else that would let either command
run without a person at the terminal. An agent may run `paisans host
request`, which reads the configuration and reaches no host, and hand the
file to an operator; the digest goes to the owner over another channel than
the file, or the comparison proves nothing. When a run reports a site
pending or down, or `site remove` prints the `host evict` line, the agent
reports it to a human with that line, and runs nothing on the owner's behalf.

The founder may authorise an agent to run admit and evict, prompts included,
on test servers named in that authorisation. It never extends to any other
host.
```

- [ ] **Step 3: Check the rules**

Run: `grep -n "unattended\|NOPASSWD" README.md docs/deployment-agent-rules.md docs/commands.md`
Expected: no sentence saying an agent may run admit or evict unattended. (README's sudo section is Task 5's; if Task 5 has not landed on this worktree, its old "An unattended run of an owner's command" sentence is expected here.)

Run: `git diff docs/deployment-agent-rules.md | perl -CSD -ne 'print if /^\+.*\x{2014}/'`
Expected: nothing.

The file stays generic: no host, product or community named in what this task adds (`AGENTS.md`).

- [ ] **Step 4: Commit**

```bash
git add docs/deployment-agent-rules.md
git commit -m "kacl-skip: say why admitting and evicting are never an agent's

A host's owner is often not one of the deployment's operators, and the
machine is theirs. Evict and admit from a request ask at a terminal, but
admit from the configuration can run without one under a NOPASSWD rule,
so the rule has to say an agent never runs them, answers their prompts,
or writes what would let them run alone. What an agent may do, a
request and a report, is said beside it, and so is the founder's
authorisation for named test servers.

Founder decision.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: `README.md`, what admit does with the Docker it finds

Group A, first of the `README.md` chain.

**Files:**
- Modify: `README.md` (*Docker is judged by its version, not its source*; *Docker comes from Docker's repository when it is absent*)

**Interfaces:**
- Consumes: `hostadmit.MinRootlessEngine`, `planDocker`; `hostcheck.MinEngine`, `MinCompose`; the profile's `AdmitPackages`.
- Produces: the section names Task 6 relies on, unchanged.

- [ ] **Step 1: Check the code**

Run: `sed -n '/^func planDocker/,/^}/p' internal/hostadmit/docker.go; grep -n "MinEngine *=\|MinCompose *=" internal/hostcheck/docker.go; grep -n "func.*AdmitPackages" -A30 internal/hostprep/*.go | grep -n "no-upgrade\|=\|pin" | head`
Expected: snap refused; Engine below 29.5 refused; a Docker that is neither `docker-ce` nor has `dockerd-rootless-setuptool.sh` refused; the host check's 25.0.0 and 2.24.0; how the rootless extras are pinned beside a present Docker. The text below follows each.

- [ ] **Step 2: Docker is judged by its version**

Replace the body of `#### Docker is judged by its version, not its source`, up to `### `host prepare` keeps what is the deployment's user's`, with:

```markdown
Each deployment runs its own rootless Docker daemon as its own user, beside
whatever Docker the host's owner runs, and `host admit` sets it up. What the
toolkit depends on is what that daemon and compose can do, and that is a
version, so admit refuses, on any host and whatever its class:

* **An Engine older than 29.5.** From 29.5 a rootless container with host
  networking shares the host's network namespace
  ([Docker: rootless troubleshooting](https://docs.docker.com/engine/security/rootless/troubleshoot/)).
  Every infrastructure container is `network_mode: host`, bound to the
  site's mesh address, so on an older Engine no other site reaches it. The
  refusal names the version found and leaves the upgrade to the owner, since
  upgrading Docker restarts every container the owner runs.
* **A Docker that cannot run rootless**: one that is not Docker's own
  `docker-ce` and has no `dockerd-rootless-setuptool.sh`, such as Ubuntu's
  `docker.io`. The rootless extras come from Docker's repository, which
  installs beside `docker-ce` only, and admit never replaces packages the
  owner chose.
* **A Docker snap, however new.** Its confinement keeps Docker from bind
  mounting under `/srv`, where every stack keeps its data, and snapd refreshes
  it, restarting the daemon and every container, on snapd's schedule rather
  than the owner's. A version number reveals neither.

The host check, which `host prepare`, `apply` and the others run as the
deployment's user, reads the user's own daemon and refuses one older than
engine 25.0.0 or compose 2.24.0, the first releases with
`healthcheck.start_interval`, which the rendered stacks use; admit has
already held it to 29.5.
[docs/specs/2026-10-11-docker-version-gate.md](docs/specs/2026-10-11-docker-version-gate.md)
lists every Docker feature the toolkit depends on, with the release that
introduced it. Nothing is upgraded or removed: either stops every container
the owner runs, and that is a decision for whoever started them.
```

- [ ] **Step 3: Docker comes from Docker's repository**

In `#### Docker comes from Docker's repository when it is absent`, replace its first paragraph ("Where `docker compose` already works, ...") with:

```markdown
Where Docker is there, admit leaves it as it is and adds only the rootless
extras beside it, pinned to the version installed so that apt has nothing to
upgrade; it refuses a Docker that cannot run rootless and an Engine older
than 29.5, rather than upgrading or replacing either (see *Docker is judged
by its version, not its source*).
```

and replace its third paragraph ("Where Docker is there and `docker compose` does not work, ...") with what Step 1 found `planDocker` and the profile do when Docker is absent and a conflicting package (`containerd`, `runc`) is installed (golden plan `docker-blank-with-ubuntu-containerd-refused.txt`): refused, not removed, naming the packages, with the reason that removing them stops the owner's containers, ending "Every `apt-get` runs non-interactively."

- [ ] **Step 4: Check**

Run: `grep -n "docker-compose-v2\|remove it and have the host's owner\|wherever it came from" README.md`
Expected: nothing in these two subsections.

Run: `git diff README.md | perl -CSD -ne 'print if /^\+.*\x{2014}/'; git diff README.md | grep -n "^+.*\(no longer\|used to\|previously\)"`
Expected: nothing.

- [ ] **Step 5: Commit**

```bash
git add README.md
git commit -m "kacl-skip: say which docker host admit accepts, and why

The README still said any Docker whose compose worked was accepted,
Ubuntu's docker.io among them, and offered docker-compose-v2 as a way
out. host admit refuses a Docker that is not docker-ce and has no
rootless setup tool, and any Engine below 29.5, since rootless host
networking needs it; the host check then holds the user's own daemon to
the rendered stacks' floor.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: `README.md`, egress, ufw and the owner's commands through sudo

Group A, second of the `README.md` chain, after Task 4.

**Files:**
- Modify: `README.md` (*A host's owner admits a site from a request*; a new `#### Egress keeps the deployment off the owner's network` after `#### host admit owns its rules, and only its rules`; *A sudo that wants a password is asked for once, on a terminal*)

**Interfaces:**
- Consumes: `internal/hostadmit/egress.go`; the spec's *Egress*; Task 1's guide headings; Task 3's rule.
- Produces: `#### Egress keeps the deployment off the owner's network`.

- [ ] **Step 1: Check the egress table against the code**

Run: `grep -n "accept\|drop\|fib daddr\|ct direction\|meta skuid\|mark" internal/hostadmit/egress*.go internal/hostadmit/*.nft* 2>/dev/null | grep -v _test | head -40; sed -n 682,842p docs/specs/2026-10-15-rootless-and-host-admit.md`
Expected: the output chain's rules in order, the reply accepts, and the mesh mark chain. Step 2's numbered list follows the code's order and ranges exactly; where the code and the spec differ, the code wins.

- [ ] **Step 2: The egress subsection and the ufw sentence**

After `#### host admit owns its rules, and only its rules` (before `#### host prepare manages the login user's authorized keys`), add `#### Egress keeps the deployment off the owner's network`, written from Step 1, covering, in this order:

* why: every infrastructure container shares the host's network namespace, so without a rule it reaches the owner's services on loopback and the LAN;
* the table, `inet paisans_<token>`, loaded by `paisans-<token>-egress.service` before the user's manager, so no container starts unfiltered at boot; the chain matching the user's UID and its subordinate range;
* the rules in the code's order, as a numbered list, with the reason each one's place is load-bearing;
* the mesh mark chain: WireGuard's UDP is the kernel's, so the helper marks each mesh conf and a second chain drops loopback, link-local, multicast and the host's own addresses after the endpoint's name is resolved; Kubernetes uses overlapping mark bits, so a Kubernetes host is outside what admit supports;
* `--mesh-lan`;
* **Known limits**, each as a bold-led paragraph: an owner service bound to `0.0.0.0` or `::` is reachable on the mesh address, so **owners should bind admin services to `127.0.0.1` or to their LAN address**; the user may listen on any port no owner's listener holds; routes the table does not read; flows opened while the table is absent;
* a deployment that needs something on the owner's network asks the owner, who adds an accept rule of their own, outside the CLI on purpose; and a link to *What the deployment can reach* in [docs/guides/host-owners.md](docs/guides/host-owners.md).

In `#### A host's owner admits a site from a request`, replace "Admitting needs ufw installed and active on the host: `host admit` never enables it, and refuses before making any change otherwise." with:

```markdown
Admitting needs ufw installed: `host admit` never installs it. When ufw is
not active, admit enables it only with `--enable-ufw` or a yes at a
terminal, behind a timer that turns it off again unless a new SSH login gets
through (see *SSH is never shut out* below, and *If SSH stops answering after
admit enables ufw* in
[docs/guides/host-owners.md](docs/guides/host-owners.md)).
```

and replace the sentences from "The egress table fences the deployment's user off the owner's machine" to the end of that paragraph, before "`paisans host admit` and `paisans host evict` are in `docs/commands.md`.", with "The egress table fences the deployment's user off the owner's machine, within the limits *Egress keeps the deployment off the owner's network* names." Keep "Ports 80 and 443 belong to one deployment per host."

Run `grep -n "SSH is never shut out" README.md` first; if that bold lead has another wording, name the one that is there.

- [ ] **Step 3: The owner's commands through sudo**

In `#### A sudo that wants a password is asked for once, on a terminal`, replace its first paragraph with:

```markdown
Every command that acts on a deployment logs in as the site's ssh user and
runs nothing through sudo but the WireGuard helper, which its one sudoers
line allows without a password. Three commands are the host owner's: `host
admit` and `host evict`, which change the host as root, and `host
deployments`, which reads the registry and every deployment's directories,
root's. With `--request`, admit and evict run as root on the host itself,
under the owner's own `sudo`; reached from a workstation, with `--site` and
the owner's login in `--ssh`, they and `host deployments` run through sudo
(`host deployments`' `--sudo`, on by default). Before the first such
command, the toolkit asks the host `sudo -n true`. `-n` makes sudo fail at
once instead of waiting for a password, so the answer is immediate.
```

Check the first sentence's claim with `grep -n "sudo -n\|NOPASSWD" internal/apply/wghelper*.go internal/hostadmit/*.go | grep -v _test | head`; if apply reaches the helper otherwise, say how.

Replace the paragraph beginning "An unattended run of an owner's command needs a NOPASSWD sudoers rule" with:

```markdown
`host evict` always asks for the deployment's user name at a terminal, and
`host admit --request` for the request's digest, so neither runs without a
person. `host admit --site` asks nothing once every grant is passed and ufw
is active, and `host deployments` asks nothing, so an unattended run of
either needs a NOPASSWD sudoers rule for the login `--ssh` names. Whether to
write one is a decision about the host, made by whoever owns it, and the
toolkit never makes it; an agent never runs admit or evict unattended,
whatever sudo allows (`docs/deployment-agent-rules.md`).
```

- [ ] **Step 4: Check**

Run: `grep -n "^#### Egress keeps\|never enables it\|An unattended run of an owner's command" README.md`
Expected: the new heading only.

Run: `git diff README.md | perl -CSD -ne 'print if /^\+.*\x{2014}/'; git diff README.md | grep -n "^+.*\(no longer\|used to\|previously\)"`
Expected: nothing.

- [ ] **Step 5: Commit**

```bash
git add README.md
git commit -m "kacl-skip: describe egress, enabling ufw and the owner's sudo truthfully

Nothing in the README described the egress table that fences each
deployment off its owner's network, or its limits, of which the one an
owner must act on is that a service bound to every interface is
reachable on the mesh address. The README also said admit never enables
ufw, which --enable-ufw does, and that the owner's commands could run
unattended alike, when only admit from the configuration and host
deployments can.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: The last stale sentences, in `README.md` and the guides

Group A, last of the `README.md` chain, after Task 5.

**Files:**
- Modify: `README.md` (*What it does*'s command block; *The watchdog*; *Preflight*)
- Modify: `docs/guides/how-a-deployment-protects-itself.md` (*Who is trusted with what*)
- Modify: `docs/guides/when-an-operator-leaves.md`, only if Step 3 finds a stale reference

**Interfaces:**
- Consumes: the merged code; Task 1's guide.
- Produces: nothing other tasks read.

- [ ] **Step 1: The command block**

In *What it does*, add before the two `host prepare` lines:

```
paisans host request --site vm > vm.admit  # the request the host's owner admits the site from
sudo paisans host admit --request vm.admit --execute   # on the host, by its owner: the deployment's own user and rootless Docker
```

Align the comments with the block's existing column where the lines allow.

- [ ] **Step 2: Two sentences**

Run: `grep -n "Before=" internal/hostprep/profiles/ubuntu/watchdog-module.service.tmpl`
Expected: `systemd-logind.service` for admit. In `#### The watchdog`, replace "`modprobe softdog` before Docker starts" with "`modprobe softdog` before `systemd-logind` starts the lingering users' managers, whose Docker starts Patroni".

In *Preflight* (the design-assumption list near `**Docker's presence is covered indirectly**`), replace "**Docker's presence is covered indirectly**: it is part of `host prepare`'s plan, so \"prepared\" covers it." with "**Docker's presence is covered indirectly**: `host admit` installs or refuses it, and the host check, which the `Host` row runs, reads the deployment's own daemon."

- [ ] **Step 3: The guides**

In `docs/guides/how-a-deployment-protects-itself.md`, *Who is trusted with what*, replace "The configuration names which operators may reach a site (its `ssh.keys`), and the host's owner can bound that list on the host itself." with:

```markdown
The configuration names which operators may reach a site (its `ssh.keys`),
and the host's owner decides whether the deployment runs there at all: they
admit it as a user of its own, fenced off their machine, and can evict it
(see [Admitting a deployment to your machine](host-owners.md)).
```

Run: `grep -n "allowlist\|host allow\|bound that list\|as root\|login user's authorized keys" docs/guides/*.md`
Expected: the `when-an-operator-leaves.md` reference to *host prepare manages the login user's authorized keys*, which is the section's current name and stays; nothing else. Rewrite in place any other hit that describes something phase 1 or phase 6 removed.

- [ ] **Step 4: The sweep**

Run: `grep -n "br-+\|after-wireguard\|/etc/paisans/authorized_keys\|/var/lib/paisans/config\|/var/lib/paisans/granted\|host allow\|host disallow" README.md docs/commands.md docs/development.md docs/guides/*.md`
Expected: only README's "No rule names a `br-+` bridge" sentence in *The firewall follows the admission*, which says why none exists.

Run: `grep -n "host prepare" README.md | grep -i "install\|opens\|enables\|arms\|ufw\|softdog"`
Expected: none that gives `host prepare` a root step. The doctor section's `cannot assign requested address` paragraph is Task 7's and stays until then.

Run: `perl -ne 'print "$ARGV\t$.\t$1\n" while /\]\(([^)#]+)/g' README.md docs/commands.md docs/guides/*.md | grep -v http | while IFS="$(printf '\t')" read f n p; do d=$(dirname "$f"); [ -e "$d/$p" ] || echo "broken $f:$n $p"; done`
Expected: nothing, once Task 1 has merged into this worker's branch; before then, only links to `host-owners.md`.

Run: `git diff README.md docs/guides | perl -CSD -ne 'print if /^\+.*\x{2014}/'; git diff README.md docs/guides | grep -n "^+.*\(no longer\|used to\|previously\)"`
Expected: nothing.

- [ ] **Step 5: Commit**

```bash
git add README.md docs/guides
git commit -m "kacl-skip: name host admit where the docs still gave its steps to others

The command block showed no step for the host's owner, the softdog unit
is ordered before logind and not Docker, preflight said host prepare
covered Docker's presence, and the operators' guide said an owner could
bound the key list on the host, which the removed allowlist did. Each
names what is merged.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Doctor's checks, as phase 7 lands them

Group B. **Runs only after phase 7 (`feat/rootless-doctor`) has merged into `develop`**, and after Group A (founder decision: phase 9 runs alongside phase 7 and documents its checks as they land). Nothing in this task may be written from phase 7's plan alone.

**Files:**
- Modify: `README.md` (*`paisans doctor` reports what is stuck and how to recover*)
- Modify: `docs/commands.md` (`### `paisans doctor``)
- Modify: `docs/development.md` (the doctor paragraphs under *Run*)

**Interfaces:**
- Consumes: phase 7's merged `internal/doctor/rootless.go`, `sudo.go`, `admission.go`, `answer.go`, `containers.go`, `checks.go`.
- Produces: nothing other tasks read.

- [ ] **Step 1: Bring phase 7 in, and read its wording**

Run: `git fetch origin && git rebase origin/develop && ls internal/doctor/rootless.go internal/doctor/sudo.go internal/doctor/admission.go internal/doctor/answer.go`
Expected: phase 7 merged; the four files. Without them, stop and wait for phase 7.

Run: `grep -n "Name: *\"\|Name: *fmt" internal/doctor/checks.go | head -40; grep -n "Command *= " internal/doctor/rootless.go internal/doctor/sudo.go`
Expected: the nodes in the graph's order (`reach`, `admission`, `user`, `docker`, `docker engine`, `linger`, `sudo rights`, ..., `gateway answer`) and the four commands. Each name and command in Steps 2 to 4 is checked against these; where phase 7 merged another, the text uses phase 7's.

Run: `grep -n "Level: Fail\|Level: Warn\|Fail(\|Warn(" internal/doctor/rootless.go internal/doctor/sudo.go internal/doctor/admission.go internal/doctor/answer.go internal/doctor/reach.go internal/doctor/containers.go`
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

In the paragraph beginning "The checks run at once", replace "everything on a site needs its reach" with "everything on a site needs its reach and then its admission record, everything that runs `docker` there needs the site's own Docker to answer".

In the checks table, replace the `Reach` row and add these after it, and the `Gateway answer` row after `Leftovers`, each judged against Step 1's output:

```markdown
| Reach | `true` over ssh, on every site asked | the site does not answer and is neither pending nor down, or it is the gateway. A pending or down site is a `WARN`, saying what the deployment is without while it is gone: the gateway, an etcd vote, a Patroni member, a Garage node, its copy of each clustered app, the apps pinned to it; a pending site with the `paisans host request` line, a down one with the `site remove --host-gone` line |
| Admission | the admission record, `/etc/paisans/admitted.<token>.json`, read as the site's ssh user | it cannot be read, or differs from `paisans-stack.yaml`. No record, or one without a grant the site's roles need, is pending: a `WARN` (a `FAIL` on the gateway) with the `host request` line, and nothing more is asked of the site |
| User | `id -u`, `id -un`, `id -nG` | the user has uid 0, or is in a group `host admit` refuses (`hostadmit.PrivilegedGroups`) |
| Docker | `docker version` and `docker info`'s security options, against the user's own socket | the daemon does not answer or is not rootless. When it does not answer, nothing that runs `docker` on the site is checked |
| Docker engine | the version `docker version` read | older than 29.5 |
| Linger | `loginctl show-user <user> --property=Linger` | linger is off: the user's Docker stops when the last login ends and does not start at boot |
| Sudo rights | `LC_ALL=C sudo -n -l` | sudo lets the user run anything but the WireGuard helper as root, or does not let it run the helper |
```

```markdown
| Gateway answer | from this workstation, `/.well-known/paisans/<token>` through each gateway's public address, on 443 and on 80 expecting the redirect, with the check `apply` runs at the end of the gateway's step | any other answer: something else on the host holds the port, which the deployment's user cannot see |
```

Replace the paragraph beginning "A container that failed with `cannot assign requested address`" up to its sentence naming `host prepare --site <site> --execute` with the advice phase 7's `internal/doctor/containers.go` prints, in words, and link *The user's Docker starts after the mesh interface*. Keep its sentences from "A container Docker keeps restarting" on.

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

Check its last clause against the DNS nodes in `internal/doctor/dnschecks.go`.

- [ ] **Step 4: `docs/development.md`**

In the paragraph beginning "`doctor` reaches every site", add the admission record, user, rootless Docker, linger and sudo rights to what it reports. In the paragraph beginning "Its checks are a graph", add after its first sentence:

```markdown
After each site's reach comes its admission record, which every other node on
the site needs, so a site whose host has not admitted the deployment is one
warning; the site's `docker` node is needed by every node that runs `docker`
there. Pending and down come from the run's classification, asked once, the
first time a site does not answer, and each gateway's answer from outside
from the same check `apply` runs.
```

Name the package variables phase 7 merged for the classification and the gateway answer, as `grep -n "^var doctor" cmd/paisans/*.go` shows them.

- [ ] **Step 5: Check the docs**

Run: `grep -n "Admission\|Sudo rights\|Gateway answer\|Linger" README.md | head; grep -n "admission record" docs/commands.md docs/development.md`
Expected: the new rows and sentences.

Run: `grep -n "host prepare --site <site> --execute\`, which orders Docker" README.md`
Expected: nothing.

Run: `git diff origin/develop -- README.md docs | perl -CSD -ne 'print if /^\+.*\x{2014}/'; git diff origin/develop -- README.md docs | grep -n "^+.*\(no longer\|used to\|previously\)"`
Expected: nothing.

- [ ] **Step 6: Run the whole suite**

Run: `gofmt -l cmd internal && go vet ./... && go test ./...`
Expected: nothing listed; PASS. Nothing outside the docs changed.

Run: `git diff origin/develop --stat`
Expected: `README.md`, `docs/commands.md`, `docs/development.md`, `docs/deployment-agent-rules.md`, `docs/guides/host-owners.md`, `docs/guides/how-a-deployment-protects-itself.md`, possibly `docs/guides/when-an-operator-leaves.md`, and this plan.

- [ ] **Step 7: Commit**

```bash
git add README.md docs/commands.md docs/development.md
git commit -m "kacl-skip: describe doctor's rootless checks, pending sites and the gateway's answer

Doctor checks each site's admission record, user, rootless Docker,
linger and sudo rights before anything else on it, reports a pending or
down site as a warning naming what the deployment is without, and asks
each gateway from outside whether it answers as itself. The README's
checks table, the commands reference and the development notes say so,
in the words the checks print.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

- [ ] **Step 8: Review**

Request the final whole-branch review (superpowers:requesting-code-review). Every finding is fixed before merge, Minors batched into this step.

---

## Self-Review Notes

* **Spec coverage.** The spec's *Documentation*: `docs/commands.md`'s `host request`, `host admit`, `host evict`, `host prepare` and `--sudo` (phase 6; Task 2 adds the guide links and the digest's channel); `README.md`'s sections on `host prepare` and the login user's keys (phase 6) and on Docker judged by its version (Task 4); the host owners' guide with install and check, the digest, what admitting changes, each flag, and evict (Task 1), plus `--enable-ufw` with its timer, refusals and lockout recovery, the egress limits with "bind admin services to `127.0.0.1` or the LAN address", Ubuntu 22.04 and 24.04, mixed releases on one architecture for data sites, and what stays after evict; `docs/deployment-agent-rules.md` (Task 3). The spec's *Egress*, which `README.md` did not describe, is Task 5. Doctor's checks are Task 7, after phase 7 merges.
* **Review Focus.** Install block: Task 1, Step 4. ufw lockout: Task 1, *4. ufw* and *If SSH stops answering*. Digest's channel: Task 1, Task 2, Task 3. Bind to `127.0.0.1`: Task 1, *What the deployment can reach*; Task 5. Flags: Task 1, Step 4's loop. Unattended: Task 5, Step 3 and Task 3, Step 2 say the same.
* **Left to other phases, on purpose.** `CHANGELOG.md` bullets are each behaviour's own phase's. `cmd/paisans/help.go` is already right (ruling 6). The README `cannot assign requested address` paragraph waits for phase 7's advice text (Task 7).
* **Name consistency.** Task 1's headings are the ones Tasks 2, 3 and 5 link to: *If SSH stops answering after admit enables ufw*, *What the deployment can reach*, *Evicting*, *What stays after evicting*. Task 5's *Egress keeps the deployment off the owner's network* is linked from the request section it edits. Phase 6's section names stay as they are; Task 7 links *The user's Docker starts after the mesh interface* by its current name.
* **Placeholder scan.** Steps whose text depends on code this plan quotes only in part say what to read and what the text must cover: Task 4, Step 3's conflicting-package paragraph; Task 5, Step 2's egress subsection (its rules come from `egress.go` in the code's order); Task 7, Step 2's container advice. Every other step carries its full text.
