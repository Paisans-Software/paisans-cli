# Rootless Phase 5: `host admit` and `host evict` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `paisans host admit` takes a host, as its owner and as root, from whatever it is to a host where one deployment's own unprivileged user runs its own rootless Docker, with the WireGuard helper, the firewall, the egress table, the grants the owner gave and the admission record in place; `paisans host evict` undoes exactly that, leaving every other admitted deployment and the owner's own services as they were.

**Architecture:** A new planner in `internal/hostadmit` reads the host once (one read-only probe script, plus the existing probes for packages, ufw, the watchdog, the mesh, the registry and the owner's web server), builds a `Plan` of idempotent steps in a fixed order, shows it, and runs it with `--execute` through an `apply.Transport` that is root: `hostadmit.Local()` for `--request` on the host, `ownerTransport` (ssh with sudo) for `--site --ssh` from a workstation. Every root write is a step on that transport; every file in the deployment user's home, and every read there, runs as that user through `runuser -u <user> -- env -i ...`. `cmd/paisans/hostadmit.go` parses the flags, stops first on a missing grant before anything touches the host, asks for the typed digest (admit with `--request`) or the typed user name (evict), and hands the plan to `hostadmit.Execute`, which rebuilds once the user exists so the steps that need its UID run with the real one.

**Tech Stack:** Go 1.25, `text/template` with `embed`, POSIX sh and awk for the probe, nftables, ufw, systemd (system and user units), udev, sudo and `visudo`, the existing `internal/hostprep`, `internal/admission`, `internal/registry`, `internal/mesh`, `internal/webserver` and `internal/apply` packages.

**Spec:** `docs/specs/2026-10-15-rootless-and-host-admit.md` (every section; *What it does* steps 1 to 17, *Egress*, *The WireGuard helper*, *`paisans host evict`*, *Behind the owner's web server* and *Observed* govern this phase), and the phase index `docs/superpowers/plans/2026-10-15-rootless-and-host-admit.md`, *Phase 5*, read beside *Phase 6* and *Phase 10*, which consume what this phase leaves on a host.

## Global Constraints

* "Rootless on every site; no code path keeps the sudo transport for a deployment command." (index) Only `host admit`, `host evict` and `host deployments` reach a host as root.
* "Docker Engine 29.5 or later." (index) "If Docker is present, refuse an Engine older than 29.5 and say the owner must upgrade it; upgrading the owner's Docker is not this command's decision." "A snap Docker is refused, as today." (spec, step 1)
* "Grants are opt-in: `--watchdog`, `--public-ports`. Forwarding follows `endpoint`, with no flag." (index)
* "A data site without `--watchdog` stops admit first, before any probe, plan or digest prompt, exit 1, nothing read or changed; a re-run dropping it from an admitted data site stops the same way." "A site whose configured roles include the gateway or a Caddy monitor stops admit first without `--public-ports`, as the watchdog does; any other site never stops for it." (index)
* "Admit writes it before the user's manager first starts and never restarts `user@<uid>.service` for a user that is already running, because that restarts the user's daemon and every container in it." (spec, step 5; *Observed* R5)
* "Files in the deployment user's home (`~/.ssh/authorized_keys`, `~/.config/docker/daemon.json`) are written as that user (`runuser -u <user> --`), never by root, so a symlink the user plants cannot redirect a root write." (index)
* "The admission record is written mode 0644, owned by root, via temp and rename; [...] a re-run that finds it otherwise restores 0644." (index)
* The helper at `/usr/local/libexec/paisans/wg-<iface>`, root's, mode 755; "its sudoers line goes to `/etc/sudoers.d/paisans-<token>`, mode 440, written to a temporary file that `visudo -c` checks before it is moved into place, so a bad line never reaches sudo." (index)
* "`/etc/wireguard`: admit creates it root's, mode 700, when it is absent, makes an existing one root's with no group or other write bit, and refuses one it cannot fix (a link, or not a directory), naming it." (index)
* Egress: "an nftables table `inet paisans_<token>` from a unit `paisans-<token>-egress.service`, ordered before `user@<uid>.service` so no container starts unfiltered at boot", matching "the user's UID and its whole subordinate range"; the mark chain drops "loopback, link-local and multicast, every address of the host itself (`fib daddr type local`), and the private and shared ranges of rule 3"; `--mesh-lan` accepts "the private prefix of the host's own LAN (the route of its default interface) in the mark chain, and nothing wider." (spec, *Egress*)
* "`host evict` stops `wg-quick@psns-<token>` and removes `/etc/wireguard/psns-<token>.conf` and the `--data-dir` directory." "Host-wide files (`delegate.conf`, `60-paisans.conf`) are removed only when no other deployment is admitted, and `ip_unprivileged_port_start` and `ip_forward` are each restored only when no other admitted deployment holds `public_ports` or has forwarding on." (index; spec)
* `host admit` and `host evict` are owner actions: "an agent never runs them unattended" (index, *Global Constraints*; `docs/deployment-agent-rules.md`).
* No migration code: an existing deployment is redeployed, never converted. A path an earlier, root-run deployment left (`/srv/paisans/<token>` owned by root) is refused, not adopted.
* No em dashes anywhere: code, comments, docs, commit messages. Docs describe current state only; no rejected alternatives outside `docs/decisions.md`. `docs/specs/` and `docs/superpowers/` are records and are not edited by these tasks, except this plan.
* Commits: `<type>: <lowercase subject>`, a body saying why, `Founder decision.` where it was one, the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. A behaviour-changing commit adds one `CHANGELOG.md` bullet under `[Unreleased]`, one sentence, at most 35 words; a commit with nothing an operator would notice adds none and says so in its body.
* Never `git stash`; other sessions share the stash. Never push.

## Review Focus

The index's own two lines for this phase are pinned where they belong: "an owner admits with an existing user that is in `docker` or `sudo`: admit must refuse" is `TestAPrivilegedUserIsRefused` (Task 5), and "`host evict` of one of two admitted deployments: host-wide files and sysctls stay" is the golden plan `evict-one-of-two` (Task 13). The five below are what the spec implies and no line of it tests, most likely first.

* **An admitted gateway re-run.** The owner re-runs `host admit --public-ports` on a host whose 80 and 443 are held by this very deployment's Caddy (the deployment user's process, or a process in its subordinate range). The 80/443 holder check must see its own listener as its own and not refuse; another user's listener on the same port must still refuse (Task 9: `TestOwnListenerIsNotAHolder`, golden `ports-own-caddy`).
* **A path the deployment's user controls, read or written by root.** `~/.ssh` planted as a link to `/root/.ssh`, `~/.config/docker/daemon.json` linked to `/etc/shadow`, `/srv/paisans/<token>/.paisans` linked elsewhere, or the data directory swapped for a link: no root step reads or writes through any of them; every such step runs as the user (Task 3: `TestNoRootStepTouchesTheUsersPaths`; Task 12: `TestAPlantedSSHLinkLeavesItsTargetAlone`; Task 7: `TestTheDataDirectoryParentMustBeRoots`).
* **Evict naming an account that is not the deployment's.** `host evict --deployment <id> --user ubuntu` (the owner's own login) or `--user root`, or a record that names `community` while `--user paisans-<token>` is passed: evict must refuse and delete nothing (Task 13: `TestEvictRefusesAnotherUser`).
* **A blank host, where the user does not exist when the plan is drawn.** The egress table, its unit, the watchdog rule and the daemon steps need the user's UID and subordinate range; `--execute` must run them with the real values after the user is created, and must stop if the plan it rebuilds differs from the one the owner approved in anything but those values (Task 3: `TestExecuteRebuildsOnceTheUserExists`, `TestExecuteStopsWhenTheRebuiltPlanDiffers`).
* **An owner adding or changing `--data-dir` on a deployment that already has data.** Moving Docker's `data-root` under a running daemon hides every image, container and volume; admit must refuse while the old data root holds anything, and must never restart the user's daemon while it runs a container (Task 7: `TestChangingTheDataDirWithDataIsRefused`, `TestARunningDaemonIsNotRestarted`).

## Rulings

Where the spec or index leaves a choice open, or two of them disagree with the code, this plan decides as follows. Each is restated where its task implements it.

1. **Step order.** The spec's *What it does* is followed, with four moves the spec itself requires: the registry claim runs first under `--execute` (as every claim does today, so a conflict found under the lock changes nothing; the plan's read-only check refuses it earlier still); `delegate.conf` is written before `loginctl enable-linger`, because enabling linger starts the user's manager at once (*Observed* R14: "the user manager is active right after `enable-linger`") and step 5 says it is written before the manager first starts; the egress table and its unit are loaded before linger, so no container ever runs unfiltered; and the AppArmor check runs after the package step, which is what installs the `rootlesskit` profile on a blank host. The full order is in Task 3's `order` table.
2. **Steps that need the user's UID are provisional while the user does not exist.** `Build` renders them with `<uid>` and `<subordinate range>` in place of values and marks them `Provisional`; `Execute` runs every step before the first provisional one, rebuilds the plan from a fresh probe, and stops ("the host changed while admit ran") unless the rebuilt plan's remaining step titles equal the approved plan's. Nothing is guessed about which UID `useradd` will pick.
3. **The claim is built from the request alone, on both paths.** `registry.ForRequest(req, now)` fills token, root, interface, listen port, subnet and address from the request; `Roles` is `data` when the request needs the watchdog and `gateway` when it needs public ports, which are exactly the two exclusive roles and the two grants that make them exclusive; `Site` and `Domain` are empty. The merge's same-deployment check gains an address comparison, so a second site of one deployment on one machine is still refused. Both paths plan byte-identical steps. See founder question 1.
4. **A grant the request does not need is refused, before the host is read.** `--watchdog` on a site that needs no watchdog, or `--public-ports` on one that needs no low port, stops admit with exit 1 naming the flag, because each weakens the host for every local user and the owner gains nothing by it. See founder question 3.
5. **The mesh mark is derived from the token.** `deployment.Deployment.FwMark()` is `0x50530000 | token`, unique per token and therefore per host (the registry refuses a second claim of a token). `hostprep.WGHelperScript` reads it from the deployment rather than taking a parameter (the index's "gains the mark parameter"), so the helper and the egress table cannot disagree about it.
6. **The watchdog is planned as `auto`.** A request carries no watchdog mode, so both paths plan what `auto` plans: a hardware or hypervisor device when there is one, else softdog with the existing warning. `required` and `softdog` are compared with what admit found by `host prepare` and `doctor` in phases 6 and 7.
7. **Sysctl values are restored to what they were.** The first admit that changes `net.ipv4.ip_unprivileged_port_start` or `net.ipv4.ip_forward` records the value it found in `/etc/paisans/sysctl.before` (root's, 0644). `/etc/sysctl.d/60-paisans.conf` holds a key while any admitted deployment needs it; evict, once none does, drops the key, runs `sysctl -w` with the recorded value, and removes both files once both keys are gone. A system Docker that set `ip_forward=1` is therefore left with it.
8. **`br_netfilter` is never unloaded.** `/etc/modules-load.d/paisans.conf` is removed with the last admitted deployment; the module stays loaded until the next boot, since a system Docker may depend on it.
9. **The user's `default-address-pools`** are Docker's default pools (`mesh.DefaultPools`) less any that overlaps the mesh subnet, each with `size: 24`, so a bridge network inside the user's namespace never takes the mesh's range.
10. **A host with its own firewall stops admit.** ufw's default policy is set and ufw enabled only when ufw is inactive and no other firewall is found (firewalld active, or an nftables `hook input` chain in a table that is neither ufw's nor Docker's); otherwise, with ufw inactive, admit stops naming the firewall and the ports the deployment needs. See founder question 2.
11. **The user's daemon is never restarted under a container.** A changed `daemon.json` restarts the user's `docker.service` only when it runs no container; otherwise the step ends on a warning that the change applies at the daemon's next restart. A change of `data_dir` (added, changed or removed) while the old data root holds any image, container or volume is refused, naming both paths.
12. **Keys are added, never removed, and recorded as host prepare records them.** Admit appends each request key missing from `~/.ssh/authorized_keys` and writes `/srv/paisans/<token>/.paisans/authorized_keys.owned` (the spec's *Keys* path) as the user, one `added <fingerprint> <name>` line per key it appended, in the sidecar format of `internal/hostprep/authorized_keys.go`, so phase 6's `host prepare` can later remove a key an operator drops.
13. **Evict without a record deletes only a user named `paisans-<token>`.** With a record, the user must be the record's; without one, any other name is refused, so a lost request and a mistyped `--user` cannot delete the owner's login.
14. **`--mesh-lan`'s prefix** is the network of the first global IPv4 address on the default route's interface, and is refused unless it is inside `10.0.0.0/8`, `172.16.0.0/12` or `192.168.0.0/16`, and no wider than a /16.
15. **`--data-dir <path>`**: `<path>` must exist, be a directory and not a link, owned by root with no group or other write bit, so neither the deployment's user nor another can swap the child admit creates. Its filesystem is refused when it is NFS (any `nfs*`), CIFS or SMB, any FUSE filesystem, `overlay`, `vfat`, `exfat` or `ntfs`; the same check applies to the home when the flag is absent.
16. **Everything run as the user goes through one builder**, `asUser(u UserFacts, command string)`: `runuser -u <user> -- env -i HOME=<home> PATH=/usr/bin:/bin:/usr/sbin:/sbin XDG_RUNTIME_DIR=/run/user/<uid> DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/<uid>/bus DOCKER_HOST=unix:///run/user/<uid>/docker.sock sh -c '<command>'`, so nothing of root's environment, a `DOCKER_HOST` least of all, reaches it. Reads in the user's home use it too.
17. **Every system-Docker command admit runs names the system socket**: `docker -H unix:///var/run/docker.sock`, never what the environment says. The probes admit borrows (`webserver.Detect`, `mesh.Probe`) run plain `docker`, so `host admit` and `host evict` clear `DOCKER_HOST` and `DOCKER_CONTEXT` from their own environment before reaching the host; over ssh, sudo's `env_reset` does the same.
18. **Admit installs what phase 6's `host prepare` will no longer install**: with Docker absent, Docker's repository and packages with `docker-ce-rootless-extras`; with it present, `docker-ce-rootless-extras` with `apt-get install --no-upgrade`, which never upgrades a package already installed; in both, `uidmap`, `nftables`, `wireguard-tools`, `ufw` and `git`. A present Docker that is not `docker-ce` (Ubuntu's `docker.io`) is refused when it is 29.5 or later but has no `dockerd-rootless-setuptool.sh`, since the rootless extras come from Docker's repository.
19. **Subordinate IDs.** An existing user with no range, a range under 65,536, or a range overlapping another user's in `/etc/subuid` or `/etc/subgid` is refused with the `usermod --add-subuids` fix: the egress table trusts the range, so it must be the user's alone.
20. **`/srv/paisans/<token>` already present and not owned by the user is refused**, naming it: it is an earlier root-run deployment, which is redeployed, not converted.
21. **Free space.** Warned below 20 GB when the request needs the watchdog (a data site), 5 GB otherwise, since a request carries no roles; see founder question 1.
22. **No boot ordering beyond the egress unit.** *Observed* R10: nothing orders `wg-quick` and the user's daemon, and `restart: unless-stopped` on every infra service makes either order safe. The egress unit is `Before=` and `RequiredBy=` `user@<uid>.service`, so a table that fails to load keeps the user's manager, and every container, from starting.
23. **The public ports' ufw reasons** are `public ports, HTTP` and `public ports, HTTPS`, true of a gateway and a Caddy monitor alike.
24. **The sudoers temporary file is `/etc/sudoers.d/paisans-<token>.new`.** sudo's `#includedir` skips any name containing a `.`, so the unchecked file is never read by sudo even before `visudo -cf` has passed it.
25. **Caddy's admin API counts as TCP** when the detected Caddy answers `curl http://localhost:2019/config/` from the host, or, for a Caddy on the host itself, its Caddyfile's global options carry an `admin` address that is not `off` and does not start `unix/`; the record's `admin_tcp` is that address. A Caddy in a container whose admin endpoint the host cannot reach is no local user's to reach either.
26. **An ACL on `/etc/wireguard` is refused**, naming it: admit cannot assume `setfacl` is installed, and the helper refuses one too.
27. **`validate` refuses `watchdog: off` on a data site**; the mode stays valid on any other site, and the renderer's handling of it is left as it is, since it is reached only through a configuration `validate` has passed.
28. **Evict's order**: the typed user name, then `wg-quick@psns-<token>` stopped and disabled, its conf removed, the user's manager stopped (`systemctl stop user@<uid>.service`, `loginctl terminate-user`) and linger disabled, the user removed with its home (`userdel --remove`, then any line it left in `/etc/subuid` and `/etc/subgid`), the data directory and `/srv/paisans/<token>`, the helper, the sudoers line, the watchdog rule and unit, the egress unit, table and file, the deployment's ufw rules, the host-wide files and sysctls where no other deployment needs them, the record, and last the registry entry. Each is probed and planned only when present, so a re-run resumes.
29. **The `br_netfilter` boot entry is written even when the module is loaded already.** A system Docker loads it when it starts, which at boot may be after the user's daemon, which then fails to start; the spec's "nothing changes" holds of the running kernel.
30. **Evict keeps the deployment's SSH allow**, with a warning naming the `ufw delete` line: it may be the rule the owner's own login arrives by, and `host prepare` never removes one for the same reason.
31. **The check runs after `Execute`, on both paths, through `hostadmit.CheckUser`** (`docker version` as the user, on the host, through the root transport), so the two paths plan identical steps; with `--site` the command then logs in as the user over ssh, which records `ssh.user`.
32. **The mesh subnet is checked against the host as root** (phase 4's handoff): a route, an address, a Docker network or a Docker pool overlapping it refuses admit, through `mesh.Probe`, the check `host prepare` runs today.

## Founder decisions on the plan's questions

These override the interim rulings they name.

1. **The request carries the site's name and roles** (overrides rulings 3 and 21). A new task, Task 2b below, adds `site` (the site's name in the configuration) and `roles` (its roles, sorted, from the configuration) to phase 4's request: `Build` fills them, `Check` and `Parse` require them (`site` by the configuration's site-name rule, `roles` each a known role, no duplicates, sorted, not empty), `Encode` writes them in the request's field order, the golden requests and digest vectors are regenerated, and the spec's request table gains both rows. `RequestVersion` is unchanged, since nothing is released. `registry.ForRequest` takes `Site` and `Roles` from the request; the plan names the site to the owner; free space is judged at 20 GB for a data or Garage site. The grants are still computed from the roles (`admission.Needs`), and `Check` refuses a request whose grants disagree with its roles.
2. **ufw is the only firewall admit works with** (amends ruling 10). Admit exits 1 before changing anything when ufw is not installed, naming ufw and saying the owner installs it, since enabling a firewall the owner did not choose is not admit's decision. With ufw installed and inactive, it stops as ruling 10 says when another firewall is found, and otherwise sets ufw's defaults and enables it. Support for other firewalls is tracked outside this plan.
3. **A grant the request does not need is refused** (ruling 4 stands). An owner grants later by running `host admit` again with the request for the site's new roles: admit is idempotent and adds only what the new grant needs.

## What this phase hands to phases 6, 7 and 10

* **Phase 6** finds, on every admitted host: the user with linger on and its rootless daemon at `unix:///run/user/<uid>/docker.sock`; `~/.config/docker/daemon.json` written by admit (`userland-proxy: false`, `default-address-pools`, and `data-root` with `--data-dir`); `/srv/paisans/<token>` owned by the user, mode 750, with `.paisans/authorized_keys.owned`; the record at `admission.Path(token)` with `web_server`, `data_dir` and `bridge_netfilter` filled; the registry entry from `registry.ForRequest`; git, wireguard-tools, ufw, nftables and uidmap installed. Its `host prepare` installs nothing and compares the record with the declaration.
* **Phase 7**'s doctor checks what this phase guarantees: `sudo -n -l` lists the helper alone; the user is in none of `hostadmit.PrivilegedGroups`; linger is on.
* **Phase 10** runs `host admit --request` on fresh VMs and checks the egress table and the mark chain end to end, and whether loading `br_netfilter` changes the host's own bridge traffic at all.

---

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `internal/validate/validate.go`, `validate_test.go`, `README.md` | modify | `watchdog: off` on a data site refused |
| `internal/deployment/deployment.go`, `deployment_test.go` | modify | `FwMark()`, `EgressTable()`, `EgressUnit()` |
| `internal/hostprep/wghelper.go`, `snippets/wg-helper.sh.tmpl`, `wghelper_test.go` | modify | the helper adds the deployment's `FwMark` to every conf it installs |
| `internal/hostprep/admit.go` | create | what admit reuses from hostprep, exported: `AdmitPackages`, `PlanWatchdog`, `AdmitRules`, `EgressTable`, `EgressUnit` |
| `internal/hostprep/ubuntu.go`, `profiles/ubuntu-24.04/apt-install.sh.tmpl` | modify | `AdmitPackages` on the profile; `--no-upgrade` |
| `internal/hostprep/snippets/egress.nft.tmpl`, `paisans-egress.service.tmpl` | create | the egress table and its unit |
| `internal/hostadmit/plan.go` | create | `Flags`, `Input`, `Early`, `Step`, `Plan`, `Build`, `Text`, `Show`, `Execute`, the step order |
| `internal/hostadmit/facts.go`, `snippets/probe.sh.tmpl` | create | the one read-only probe and its parse |
| `internal/hostadmit/user.go` | create | `asUser`, the user, privilege check, subordinate IDs, delegation, linger |
| `internal/hostadmit/docker.go` | create | Docker's version gate, packages, AppArmor |
| `internal/hostadmit/egress.go` | create | the egress table, its unit, `--mesh-lan` |
| `internal/hostadmit/daemon.go` | create | `br_netfilter`, the rootless daemon, `daemon.json`, `--data-dir` |
| `internal/hostadmit/wireguard.go` | create | the deployment directory, `/etc/wireguard`, the helper, sudoers |
| `internal/hostadmit/firewall.go` | create | ufw through the profile, other firewalls, the 80/443 holders |
| `internal/hostadmit/watchdog.go`, `sysctl.go` | create | the watchdog grant; the host-wide sysctls |
| `internal/hostadmit/webserver.go` | create | the owner's web server into the record, the admin API warning |
| `internal/hostadmit/record.go` | create | the record, the claim, the keys, the check |
| `internal/hostadmit/evict.go` | create | `EvictInput`, `BuildEvict` |
| `internal/registry/registry.go`, `registry_test.go` | modify | `ForRequest`; the merge compares addresses for the same id |
| `internal/hostadmit/*_test.go`, `testdata/plans/*.txt` | create | the fake host, the golden harness, every golden plan |
| `cmd/paisans/hostadmit.go`, `hostadmit_test.go` | create | `host admit`, `host evict` |
| `cmd/paisans/main.go`, `help.go`, `sudo_guard_test.go`, `internal/hostadmit/local_test.go` | modify | dispatch, help, the two guard tests' allowed lists |
| `docs/commands.md`, `README.md`, `docs/deployment-agent-rules.md`, `CHANGELOG.md` | modify | the commands, the registry passage, the owner-action rule |

---

### Task 1: `validate` refuses `watchdog: off` on a data site

**Files:**
- Modify: `internal/validate/validate.go` (`watchdogOffOnDataSite`, around line 1791)
- Modify: `internal/validate/validate_test.go:84` and `TestWatchdogWarningNamesSpiloConfiguration` (around line 846)
- Modify: `README.md` (the paragraph "**`off` is a warning, not a refusal**", under the watchdog table)
- Modify: `internal/render/templates/patroni.env.tmpl:47` (its comment, if it says validate warns)
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: `(*checker).refuse(rule, key, hint, format string, args ...any)`.
- Produces: the rule `watchdog-off-on-data-site` at severity `validate.Refuse`. Nothing else in this plan depends on it; it is the configuration half of "a data site needs the `watchdog` grant whatever its `watchdog` mode" (index).

- [ ] **Step 1: Write the failing test**

In `internal/validate/validate_test.go`, change the table row at line 84 to:

```go
		{"watchdog-off-on-data-site", "watchdog-off-on-data-site", validate.Refuse},
```

and rename `TestWatchdogWarningNamesSpiloConfiguration` to `TestWatchdogOffRefusalSaysWhy`, replacing its body's checks with:

```go
// A data site's Patroni member without a watchdog can keep acting as leader
// after it has lost its lock, so failover is not safe without one; the
// refusal says so and names the grant the host's owner gives.
func TestWatchdogOffRefusalSaysWhy(t *testing.T) {
	result := validate.Check(load(t, "watchdog-off-on-data-site"))
	found := false
	for _, f := range result.Findings {
		if f.Rule != "watchdog-off-on-data-site" {
			continue
		}
		found = true
		if f.Severity != validate.Refuse {
			t.Errorf("watchdog: off on a data site is %v, want a refusal", f.Severity)
		}
		for _, want := range []string{"lost its lock", "--watchdog"} {
			if !strings.Contains(f.Message, want) {
				t.Errorf("the refusal does not say %q: %s", want, f.Message)
			}
		}
	}
	if !found {
		t.Fatal("watchdog: off on a data site was not reported")
	}
}
```

(Check the field names `Severity` and `Message` against `validate.Finding` before running; the existing test reads `f.Message`.)

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/validate/ -run 'TestRules|TestWatchdogOffRefusalSaysWhy'` (the table's test function is whichever holds line 84; run the package if unsure)
Expected: FAIL, the finding is `Warn`, and its message names neither "lost its lock" nor "--watchdog".

- [ ] **Step 3: Refuse it**

Replace `watchdogOffOnDataSite` in `internal/validate/validate.go`:

```go
// watchdogOffOnDataSite refuses a data site without a watchdog. A Patroni
// member without one can keep acting as leader after it has lost its lock,
// so failover is not safe without it, and host admit refuses to admit a
// data site without the owner's --watchdog grant whatever its mode: a
// configuration that turns it off could never be admitted.
func (c *checker) watchdogOffOnDataSite() {
	for _, name := range c.cfg.SiteNames() {
		site := c.cfg.Sites[name]
		if site.Has(config.RoleData) && site.WatchdogMode() == config.WatchdogOff {
			c.refuse("watchdog-off-on-data-site", fmt.Sprintf("sites.%s.watchdog", name),
				"watchdog is off on a data site",
				"is off on a site holding the data role. A Patroni member without a watchdog can keep acting as leader after it has lost its lock, so failover is not safe without one, and the host's owner admits a data site only with paisans host admit --watchdog. Use auto, required or softdog.")
		}
	}
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/validate/ ./internal/render/ ./internal/preflight/ ./cmd/paisans/`
Expected: PASS. If a render or cmd test loads a fixture with `watchdog: off` on a data site through `validate.Check`, it now fails; none does today (`grep -rn "watchdog: off" internal cmd examples` lists only `internal/validate/testdata/watchdog-off-on-data-site.yaml`), so a failure here is a new fixture to fix, not a reason to weaken the rule.

- [ ] **Step 5: The README and the changelog**

Replace the README paragraph that starts "**`off` is a warning, not a refusal**" with:

```markdown
**`off` is refused on a data site** (`watchdog-off-on-data-site`). A Patroni
member without a watchdog can keep acting as leader after it has lost its
lock, so failover is not safe without one, and the host's owner admits a data
site only with `paisans host admit --watchdog`. `off` remains on every other
site, where nothing maps the device.
```

If `internal/render/templates/patroni.env.tmpl` line 47's comment says validate warns about `off`, make it say validate refuses it on a data site.

In `CHANGELOG.md`, under `[Unreleased]`, `### Changed`, add:

```markdown
- `validate` refuses `watchdog: off` on a data site, since a Patroni member without a watchdog can lead after losing its lock and the host's owner admits a data site only with `--watchdog`.
```

Run: `git diff | perl -CSD -ne 'print if /^\+.*\x{2014}/'`
Expected: nothing.

- [ ] **Step 6: Commit**

```bash
git add internal/validate README.md internal/render/templates/patroni.env.tmpl CHANGELOG.md
git commit -m "feat: refuse watchdog off on a data site

A Patroni member without a watchdog can keep acting as leader after it has
lost its lock, so failover is not safe without one. host admit will admit a
data site only with the owner's --watchdog grant, whatever its watchdog
mode, so a configuration that turns the watchdog off on a data site could
never be admitted: validate says so first.

Founder decision.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: The mesh's mark, set by the helper on every conf it installs

**Files:**
- Modify: `internal/deployment/deployment.go`, `internal/deployment/deployment_test.go`
- Modify: `internal/hostprep/wghelper.go`, `internal/hostprep/snippets/wg-helper.sh.tmpl`, `internal/hostprep/wghelper_test.go`

**Interfaces:**
- Consumes: `deployment.Deployment.Token()`, `hostprep.wgHelperScript`.
- Produces: `func (d Deployment) FwMark() uint32` (`0x50530000 | token`), `func (d Deployment) EgressTable() string` (`paisans_<token>`), `func (d Deployment) EgressUnit() string` (`paisans-<token>-egress.service`). The helper's `put` installs the conf with `FwMark = 0x5053<token>` as the line after `[Interface]`, and refuses a conf that sets `FwMark` itself. `WGHelperScript(d, listenPort, subnet)` keeps its signature (ruling 5). Task 6's egress table matches `meta mark` on `d.FwMark()`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/deployment/deployment_test.go`:

```go
// The mark is the token in the low sixteen bits under a fixed "PS" in the
// high sixteen, so two deployments on a host, whose tokens the registry
// keeps apart, never share one, and the helper and the egress table read
// the same value.
func TestFwMark(t *testing.T) {
	for _, c := range []struct {
		id   string
		want uint32
	}{
		{"f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01", 0x5053f2a9},
		{"0000c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01", 0x50530000},
		{"ffffc4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01", 0x5053ffff},
	} {
		d := deployment.Deployment{ID: c.id}
		if got := d.FwMark(); got != c.want {
			t.Errorf("%s: FwMark() = %#x, want %#x", c.id, got, c.want)
		}
	}
	d := deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
	if d.EgressTable() != "paisans_f2a9" || d.EgressUnit() != "paisans-f2a9-egress.service" {
		t.Errorf("EgressTable() = %q, EgressUnit() = %q", d.EgressTable(), d.EgressUnit())
	}
}
```

In `internal/hostprep/wghelper_test.go`:

1. In `TestTheHelperInstallsWhatTheRendererWrites`, the installed file is no longer the input: replace the comparison `string(got) != f.Content` with `string(got) != marked(f.Content)`, and add the helper:

```go
// marked is conf as put installs it: the deployment's FwMark on the line
// after [Interface], which the egress table's mark chain matches.
func marked(conf string) string {
	return strings.Replace(conf, "[Interface]\n", "[Interface]\nFwMark = 0x5053f2a9\n", 1)
}
```

2. In `TestTheHelperRefusesAnythingElse`, change the `FwMark` row's expected text:

```go
		{"FwMark", replace("PrivateKey = ", "FwMark = 51820\nPrivateKey = "), "FwMark is set by this helper"},
		{"FwMark in a [Peer]", replace(keepalive, "FwMark = 0x5053f2a9\n"), "FwMark is set by this helper"},
```

3. Add:

```go
// The mark goes into every conf put installs, exactly once, after
// [Interface], whatever spacing the [Interface] line has.
func TestPutMarksEveryConf(t *testing.T) {
	for _, shell := range shells() {
		script, dir := helperIn(t, 51820, "10.44.0.0/24")
		conf := strings.Replace(goodConf, "[Interface]", "  [Interface]\t", 1)
		if out, code := runHelper(t, shell, script, conf, "put"); code != 0 {
			t.Fatalf("%s: refused (exit %d): %s", shell, code, out)
		}
		got, err := os.ReadFile(filepath.Join(dir, "psns-f2a9.conf"))
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(got), "FwMark"); n != 1 || !strings.Contains(string(got), "  [Interface]\t\nFwMark = 0x5053f2a9\n") {
			t.Errorf("%s: installed conf carries FwMark %d times, or not after [Interface]:\n%s", shell, n, got)
		}
		if names := entries(t, dir); len(names) != 1 {
			t.Errorf("%s: put left %v in the directory", shell, names)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/deployment/ ./internal/hostprep/ -run 'TestFwMark|TestTheHelper|TestPutMarks'`
Expected: FAIL to compile, `d.FwMark undefined`; then, once Step 3's deployment part is in, the hostprep tests fail with the conf installed unmarked and the refusal saying "is not a key paisans writes".

- [ ] **Step 3: Implement**

In `internal/deployment/deployment.go`, beside `WireGuardUnit` (add `"strconv"` to the imports):

```go
// FwMark is the mark the deployment's WireGuard socket carries: the helper
// host admit installs adds it to every conf it installs, and the egress
// table drops what a socket with it sends to the host itself, loopback,
// link-local, multicast and the private ranges. Kernel WireGuard sends from
// no user, so the table's UID match cannot see the mesh's own packets, and
// the mark is how it sees them. 0x5053 ("PS") above the token: the registry
// keeps tokens unique on a host, so marks are too.
func (d Deployment) FwMark() uint32 {
	n, _ := strconv.ParseUint(d.Token(), 16, 16)
	return 0x50530000 | uint32(n)
}

// EgressTable is the nftables table, in family inet, that keeps the
// deployment's user off the host's loopback and the owner's network.
func (d Deployment) EgressTable() string { return "paisans_" + d.Token() }

// EgressUnit is the system unit that loads EgressTable at boot, before the
// deployment user's manager starts.
func (d Deployment) EgressUnit() string { return d.Prefix() + "-egress.service" }
```

In `internal/hostprep/wghelper.go`, add `FwMark string` to `wgHelperData` and set it in `wgHelperScript`:

```go
		FwMark:     fmt.Sprintf("%#08x", d.FwMark()),
```

and extend `WGHelperScript`'s comment: "put adds d.FwMark to every conf it installs and refuses one that sets FwMark itself, so the egress table's mark chain sees the mesh's own packets."

In `internal/hostprep/snippets/wg-helper.sh.tmpl`:

1. After `owner='{{.Owner}}'`, add:

```sh
# The mark put adds to every conf it installs: the egress table drops what
# a socket with it sends to this host, loopback, link-local, multicast and
# the private ranges, after wg has resolved each Endpoint as root.
fwmark='{{.FwMark}}'
```

2. In `check`'s awk, before the `if (section == "") no(...)` line, add:

```awk
		if (lk == "fwmark") no(k " is set by this helper, never by the conf")
```

3. In `put`, replace the three lines from `chmod 600 "$tmp"` to `trap - EXIT` with:

```sh
	marked=$(mktemp "$dir/.$iface.conf.XXXXXX")
	trap 'rm -f "$tmp" "$marked"' EXIT
	# The mark on the line after [Interface], which check has seen exactly
	# once, before any [Peer].
	awk -v mark="$fwmark" '{ print } !done && /^[ \t]*\[Interface\][ \t]*$/ { print "FwMark = " mark; done = 1 }' "$tmp" > "$marked"
	chmod 600 "$marked"
	mv -f "$marked" "$conf"
	rm -f "$tmp"
	trap - EXIT
```

4. In the header comment's `put` line, add "with this deployment's FwMark added".

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./internal/deployment/ ./internal/hostprep/ ./internal/apply/...`
Expected: PASS, under `sh` and, where installed, `dash` and `bash`.

- [ ] **Step 5: Commit**

```bash
git add internal/deployment internal/hostprep/wghelper.go internal/hostprep/wghelper_test.go internal/hostprep/snippets/wg-helper.sh.tmpl
git commit -m "feat: mark the mesh's socket so the egress table can see it

Kernel WireGuard sends from no user, so the egress table's match on the
deployment's UID never sees the mesh's own packets, and the deployment's
user writes each peer's Endpoint. The helper now adds the deployment's
FwMark to every conf it installs and refuses a conf that sets one, so the
table can drop what that socket sends to the host itself and the owner's
network. The mark is the token under a fixed prefix, unique on a host
because the registry keeps tokens unique.

Nothing installs the helper until host admit, so no changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 2b: The request carries the site's name and roles

Founder decision 1 above. Files: `internal/admission/request.go`, `encode.go`, their tests and golden files under `internal/admission/testdata/requests/`, `internal/registry` (`ForRequest`), `docs/specs/2026-10-15-rootless-and-host-admit.md` (the request table), README and `docs/commands.md` where they list the request's fields.

- [ ] Write failing tests: `Build` fills `site` and `roles` for each golden role mix; `Parse` refuses a request missing either, a role that is not a known role, duplicate or unsorted roles, empty roles, and a `site` the configuration's site-name rule refuses, each message naming the field and quoting the value; `Check` refuses a request whose grants disagree with `admission.Needs` for its roles; the digest changes when `site` or `roles` changes (extend the field-sensitivity test through `Fields()`).
- [ ] Implement: add `Site string` and `Roles []string` to `Request` in the order the spec's table gives (after `deployment`), fill them in `Build` from the configuration, validate them in `Check` with the configuration's own site-name rule and role set (one rule, shared, as for every other field), write them in `Encode`, read them strictly in `Parse`.
- [ ] Regenerate the golden requests, read the diff (only the two new lines per file), and recompute the digest vectors independently with Python as Task 3 of phase 4 did; update the pinned vectors and the comment saying how they were computed.
- [ ] `registry.ForRequest` takes `Site` and `Roles` from the request; update its tests.
- [ ] Spec, README and `docs/commands.md`: the request's fields include `site` and `roles`.
- [ ] `go build ./...`, `go vet ./...`, `go test -count=1 ./...`; commit (`feat:` with a CHANGELOG bullet amending nothing released: since `host request` is unreleased, fold the fields into its existing Added bullet if it stays within 35 words, else no bullet and say why in the body). Body: `Founder decision.`

### Task 3: The plan model, the probe and the early stops

**Files:**
- Create: `internal/hostadmit/plan.go`, `internal/hostadmit/facts.go`, `internal/hostadmit/snippets/probe.sh.tmpl`
- Test: `internal/hostadmit/fake_test.go`, `internal/hostadmit/golden_test.go`, `internal/hostadmit/early_test.go`, `internal/hostadmit/facts_test.go`, `internal/hostadmit/execute_internal_test.go`
- Create: `internal/hostadmit/testdata/stops/*.txt` (generated)

**Interfaces:**
- Consumes: `admission.Request`, `(admission.Grants).Missing`, `admission.Path`, `admission.Dir`; `hostprep.Select`, `hostprep.ParseOSRelease`, `hostprep.Profile`, `hostprep.File`, `hostprep.Warning`; `apply.Transport`, `apply.WGHelper{Iface}.Path()`, `apply.ShellQuote`; `deployment.Deployment` and Task 2's `EgressTable`, `EgressUnit`; `ui.Reporter`, `ui.Problem`.
- Produces (every later task uses these exact names):
  - `type Flags struct { Watchdog, PublicPorts bool; DataDir string; BridgeNetfilter, MeshLAN bool }`, `func (f Flags) Grants() admission.Grants`.
  - `type Input struct { Request admission.Request; Digest string; Flags Flags; Now time.Time }`.
  - `func Early(in Input) error`: the grant stops and the flag checks, before anything reaches the host.
  - `type Step struct { Title, Describe string; File *hostprep.File; Command, Input string; Kinds []string; Provisional bool }`.
  - `type Plan struct { Deployment, User, Profile, Host string; Steps []Step; Present, Foreign []string; Warnings []hostprep.Warning }`, `func (p *Plan) Text() string`, `func (p *Plan) Show(r ui.Reporter)`, `func (p *Plan) Warn(r ui.Reporter)`.
  - `func Build(t apply.Transport, in Input) (*Plan, error)`, `func Execute(t apply.Transport, in Input, approved *Plan, r ui.Reporter) error`.
  - Inside the package: `type planner struct { t apply.Transport; in Input; d deployment.Deployment; f Facts; profile hostprep.Profile; host hostprep.OSRelease; plan *Plan; record admission.Record }` with `p.step(Step)`, `p.present(string)`, `p.warn(hint, detail string)`; `type part struct { name string; plan func(*planner) error }` and `var parts []part`, which each later task extends at the place its comment names; `type Facts` with the fields below; `uidOf(p) string`, which returns `<uid>` while the user does not exist (Task 5 adds `rangeOf`, its `<subordinate range>` counterpart).
  - Test helpers in `package hostadmit_test`: `fakeHost` (with `newBlank()`, `with`, `add`, `file`, `answer`, `withUser`), `request(t, site) admission.Request`, `input(t, site, flags) hostadmit.Input`, `goldenCase`, `addCases(...goldenCase) []goldenCase`, `runGolden(t, cases)`, `update` flag, `allCases`.

The order every later task fills in, which is the spec's *What it does* with ruling 1's four moves:

| # | part | task | spec step |
|---|---|---|---|
| 1 | claim | 12 | 15 (first, ruling 1) |
| 2 | docker | 4 | 1 |
| 3 | apparmor | 4 | 2 |
| 4 | user | 5 | 3 |
| 5 | delegation | 5 | 5 (before linger) |
| 6 | egress | 6 | 10 (before linger) |
| 7 | linger | 5 | 4 |
| 8 | bridge netfilter | 7 | 6 |
| 9 | daemon | 7 | 6 |
| 10 | deployment directory | 8 | 7 |
| 10a | mesh subnet | 8 | phase 4's handoff (ruling 32) |
| 11 | wireguard | 8 | 8 |
| 12 | firewall | 9 | 9 |
| 13 | watchdog | 10 | 11 |
| 14 | sysctls | 10 | 12, 13 |
| 14a | web server | 11 | *Behind the owner's web server* |
| 15 | record | 12 | 14 |
| 16 | keys | 12 | 16 |
| 17 | check | 12 | 17 |

- [ ] **Step 1: Write the fake host and the golden harness**

Create `internal/hostadmit/fake_test.go`:

```go
package hostadmit_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

// probeMarker is a string only admit's own probe contains.
const probeMarker = "paisans host admit: read only"

// fakeHost is a host as admit's probes see it: the probe's facts as key and
// value lines, files read with ReadFile, and an answer for each other probe,
// found by a substring of its command. Nothing runs a shell; what is under
// test is what admit plans from what a host says.
type fakeHost struct {
	facts   [][2]string
	files   map[string]string
	answers map[string]string
	ran     []string
	inputs  []string
	writes  map[string]uint32
	// onRun, when set, sees every command Execute runs, so a test can make
	// the host change as a real one would.
	onRun func(h *fakeHost, command string)
}

func (h *fakeHost) Describe() string { return "fake" }

func (h *fakeHost) Run(command string) (string, error) { return h.run(command, "") }

func (h *fakeHost) RunInput(command, stdin string) (string, error) { return h.run(command, stdin) }

func (h *fakeHost) run(command, stdin string) (string, error) {
	h.ran = append(h.ran, command)
	h.inputs = append(h.inputs, stdin)
	if h.onRun != nil {
		h.onRun(h, command)
	}
	if strings.Contains(command, probeMarker) {
		var b strings.Builder
		for _, kv := range h.facts {
			fmt.Fprintf(&b, "%s %s\n", kv[0], kv[1])
		}
		b.WriteString("end\n")
		return b.String(), nil
	}
	// The longest matching key wins, so a specific answer overrides a
	// general one whatever the map's order.
	keys := make([]string, 0, len(h.answers))
	for k := range h.answers {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) || len(keys[i]) == len(keys[j]) && keys[i] < keys[j] })
	for _, k := range keys {
		if strings.Contains(command, k) {
			return h.answers[k], nil
		}
	}
	return "", nil
}

func (h *fakeHost) ReadFile(path string) (string, bool, error) {
	c, ok := h.files[path]
	return c, ok, nil
}

func (h *fakeHost) WriteFile(path, content string, mode uint32) error {
	h.files[path] = content
	h.writes[path] = mode
	return nil
}

// with sets key to value, replacing every line the key had.
func (h *fakeHost) with(key, value string) *fakeHost {
	var kept [][2]string
	for _, kv := range h.facts {
		if kv[0] != key {
			kept = append(kept, kv)
		}
	}
	h.facts = append(kept, [2]string{key, value})
	return h
}

// add adds a line for a key that repeats.
func (h *fakeHost) add(key, value string) *fakeHost {
	h.facts = append(h.facts, [2]string{key, value})
	return h
}

// withFile sets a "file" fact: path with the checksum of content, a mode
// and an owner, as the probe prints a file admit writes.
func (h *fakeHost) withFile(path, content, mode string, uid int) *fakeHost {
	h.dropKeyed("file", path)
	return h.add("file", fmt.Sprintf("%s %s %s %d %d", path, sum(content), mode, uid, uid))
}

// withDir sets a "dir" fact as ls -ldn prints it: mode, uid, gid.
func (h *fakeHost) withDir(path, mode string, uid, gid int) *fakeHost {
	h.dropKeyed("dir", path)
	return h.add("dir", fmt.Sprintf("%s %s %d %d", path, mode, uid, gid))
}

func (h *fakeHost) dropKeyed(key, path string) {
	var kept [][2]string
	for _, kv := range h.facts {
		if kv[0] == key && strings.HasPrefix(kv[1], path+" ") {
			continue
		}
		kept = append(kept, kv)
	}
	h.facts = kept
}

func (h *fakeHost) file(path, content string) *fakeHost {
	h.files[path] = content
	return h
}

func (h *fakeHost) answer(substring, out string) *fakeHost {
	h.answers[substring] = out
	return h
}

func sum(content string) string {
	s := sha256.Sum256([]byte(content))
	return hex.EncodeToString(s[:])
}

const ubuntu2404 = "PRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\nNAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nVERSION_CODENAME=noble\nID=ubuntu\nID_LIKE=debian\nUBUNTU_CODENAME=noble\n"

// newBlank is a VPS fresh from Ubuntu 24.04: no Docker, AppArmor
// restricting unprivileged user namespaces with no rootlesskit profile, no
// deployment user, no file admit writes, ufw installed and inactive, one
// public interface, systemd-resolved, and nothing on 80 or 443.
func newBlank() *fakeHost {
	h := &fakeHost{files: map[string]string{
		"/etc/os-release":  ubuntu2404,
		"/etc/resolv.conf": "nameserver 127.0.0.53\noptions edns0 trust-ad\n",
	}, answers: map[string]string{}, writes: map[string]uint32{}}
	for _, kv := range [][2]string{
		{"engine", "absent"}, {"pkg", "docker-ce absent"}, {"pkg", "docker.io absent"},
		{"pkg", "docker-ce-rootless-extras absent"}, {"pkg", "uidmap absent"}, {"pkg", "nftables installed"},
		{"setuptool", "absent"}, {"snap", "absent"}, {"system-socket", "absent"},
		{"userns-restrict", "1"}, {"rootlesskit-profile", "absent"},
		{"passwd", "absent"}, {"subuid", "ubuntu:100000:65536"}, {"subgid", "ubuntu:100000:65536"},
		{"br_netfilter", "absent"}, {"forward-policy", "ACCEPT"},
		{"port-start", "1024"}, {"ip-forward", "0"},
		{"default-dev", "eth0"}, {"lan", "203.0.113.10/24"},
		{"firewalld", "inactive"}, {"input-policy", "ACCEPT"}, {"input-rules", "0"},
		{"egress-enabled", "no"}, {"egress-table", "absent"},
		{"data-path", "/home"}, {"data-fs", "ext2/ext3"}, {"data-avail", "42949672960"},
	} {
		h.add(kv[0], kv[1])
	}
	h.withDir("/etc/wireguard", "absent", 0, 0).withDir("/etc/paisans", "absent", 0, 0).
		withDir("/srv/paisans", "absent", 0, 0).withDir("/srv/paisans/f2a9", "absent", 0, 0)
	// hostprep's probes, as host prepare's tests answer them.
	h.answer("docker compose version", "compose absent\nkeyring absent\narch amd64\n").
		answer("RuntimeWatchdogUSec", "device absent\nsystemd 0\ndaemon absent\n").
		answer("ufw show added", "ufw present\nstatus inactive\n").
		answer("is-enabled", "enabled \nactive \n").
		answer("ip -j route", "[]").answer("ip -j addr", "[]").answer("docker network", "[]")
	return h
}

// withUser is h once useradd has made paisans-f2a9, uid 1001, with its own
// subordinate range after ubuntu's, its manager running under linger.
func (h *fakeHost) withUser() *fakeHost {
	return h.with("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash").
		with("groups", "paisans-f2a9").with("group", "paisans-f2a9").
		with("linger", "yes").with("manager", "active").with("daemon", "absent").
		add("subuid", "paisans-f2a9:165536:65536").add("subgid", "paisans-f2a9:165536:65536").
		with("data-path", "/home/paisans-f2a9")
}

// fixture is the admission package's own: one site per role mix, token
// f2a9. home-a is data, apps and the gateway with an endpoint; home-b data
// alone as user community on port 2222; vm the witness, dialled; status a
// monitor behind the owner's web server; watch a Caddy monitor with an
// endpoint; store storage alone.
func fixture(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "admission", "testdata", "paisans-stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// request is site's request as the owner reads it from the file.
func request(t *testing.T, site string) admission.Request {
	t.Helper()
	built, err := admission.Build(fixture(t), site)
	if err != nil {
		t.Fatal(err)
	}
	req, err := admission.Parse(admission.Encode(built))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// admitted is the time every golden plan is drawn at.
var admitted = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func input(t *testing.T, site string, flags hostadmit.Flags) hostadmit.Input {
	t.Helper()
	req := request(t, site)
	return hostadmit.Input{Request: req, Digest: admission.Digest(admission.Encode(req)), Flags: flags, Now: admitted}
}
```

Create `internal/hostadmit/golden_test.go`:

```go
package hostadmit_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

var update = flag.Bool("update", false, "rewrite the golden plans")

// goldenCase is one host, one site's request and the owner's flags, with
// the plan admit draws for them in testdata/plans/<name>.txt, or, when
// refused is set, the refusal in its place.
type goldenCase struct {
	name  string
	host  func() *fakeHost
	site  string
	flags hostadmit.Flags
}

// allCases is every golden case of every task's file, for the checks that
// hold of every plan admit draws (TestNoRootStepTouchesTheUsersPaths).
var allCases []goldenCase

func addCases(cs ...goldenCase) []goldenCase {
	allCases = append(allCases, cs...)
	return cs
}

// golden is the plan's text, or "refused: <error>" for a refusal: either
// is pinned, so a refusal's wording is reviewed like a plan's.
func golden(t *testing.T, c goldenCase) (string, *hostadmit.Plan) {
	t.Helper()
	plan, err := hostadmit.Build(c.host(), input(t, c.site, c.flags))
	if err != nil {
		return "refused: " + err.Error() + "\n", nil
	}
	return plan.Text(), plan
}

func runGolden(t *testing.T, cases []goldenCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := golden(t, c)
			path := filepath.Join("testdata", "plans", c.name+".txt")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/hostadmit/ -update, then read the file)", err)
			}
			if got != string(want) {
				t.Errorf("%s differs from what admit plans now:\n%s", path, diff(string(want), got))
			}
		})
	}
}

// diff is the first line where want and got part, with its neighbours.
func diff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return fmt.Sprintf("line %d:\n- %s\n+ %s", i+1, a, b)
		}
	}
	return "(equal)"
}

// userPaths are the paths the deployment's user owns or can replace: its
// home and its directory under /srv/paisans.
var userPaths = []string{"/home/paisans-f2a9", "/home/community", "/srv/paisans/f2a9/"}

// asUserPrefix starts every command admit runs as the deployment's user.
const asUserPrefix = "runuser -u "

// checkUserPaths names each step of plan that reaches a path the
// deployment's user controls as root: a file written by the transport,
// which is root, or a command that names the path and is not run as the
// user. /srv/paisans/<token> itself, which root creates and hands over, is
// not in userPaths; everything below it is.
func checkUserPaths(plan *hostadmit.Plan) []string {
	var bad []string
	for _, s := range plan.Steps {
		for _, p := range userPaths {
			if s.File != nil && strings.HasPrefix(s.File.Path, p) {
				bad = append(bad, s.Title+": writes "+s.File.Path+" as root")
			}
			if strings.Contains(s.Command, p) && !strings.HasPrefix(s.Command, asUserPrefix) {
				bad = append(bad, s.Title+": runs as root on "+p)
			}
		}
	}
	return bad
}

// Every plan of every case reaches the user's paths only as the user, so
// a link the user planted cannot redirect a root write or read.
func TestNoRootStepTouchesTheUsersPaths(t *testing.T) {
	bad := &hostadmit.Plan{Steps: []hostadmit.Step{
		{Title: "write keys", Command: "cat >> /home/paisans-f2a9/.ssh/authorized_keys"},
		{Title: "write daemon.json", File: &hostprep.File{Path: "/home/paisans-f2a9/.config/docker/daemon.json"}},
	}}
	if got := checkUserPaths(bad); len(got) != 2 {
		t.Fatalf("the check itself misses a root write: %q", got)
	}
	for _, c := range allCases {
		_, plan := golden(t, c)
		if plan == nil {
			continue
		}
		for _, b := range checkUserPaths(plan) {
			t.Errorf("%s: %s", c.name, b)
		}
	}
}
```

(Its imports are `flag`, `fmt`, `os`, `path/filepath`, `strings`, `testing`, `hostadmit` and `hostprep`. Evict's plans are not golden cases here: evict removes the user's paths as root by design, after the user and its processes are gone, and Task 13 tests it on its own.)

- [ ] **Step 2: Write the failing tests for the early stops and the probe**

Create `internal/hostadmit/early_test.go`:

```go
package hostadmit_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

// The grant a site's roles need is checked before anything reaches the
// host: each stop's text is pinned, and nothing about the host is needed to
// reach it. A grant the site does not need is refused the same way (ruling
// 4), as is a --data-dir that is not an absolute, clean path.
func TestEarlyStops(t *testing.T) {
	for _, c := range []struct {
		name  string
		site  string
		flags hostadmit.Flags
		stops bool
	}{
		{"data-without-watchdog", "home-b", hostadmit.Flags{}, true},
		{"gateway-without-public-ports", "home-a", hostadmit.Flags{Watchdog: true}, true},
		{"gateway-and-data-without-either", "home-a", hostadmit.Flags{}, true},
		{"caddy-monitor-without-public-ports", "watch", hostadmit.Flags{}, true},
		{"external-monitor-never-stops", "status", hostadmit.Flags{}, false},
		{"apps-only-never-stops", "store", hostadmit.Flags{}, false},
		{"witness-never-stops", "vm", hostadmit.Flags{}, false},
		{"watchdog-not-needed", "vm", hostadmit.Flags{Watchdog: true}, true},
		{"public-ports-not-needed", "status", hostadmit.Flags{PublicPorts: true}, true},
		{"data-dir-relative", "vm", hostadmit.Flags{DataDir: "srv/data"}, true},
		{"data-dir-unclean", "vm", hostadmit.Flags{DataDir: "/srv/../srv/data/"}, true},
		{"data-dir-root", "vm", hostadmit.Flags{DataDir: "/"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := hostadmit.Early(input(t, c.site, c.flags))
			if (err != nil) != c.stops {
				t.Fatalf("Early = %v, want a stop: %v", err, c.stops)
			}
			if err == nil {
				return
			}
			path := filepath.Join("testdata", "stops", c.name+".txt")
			if *update {
				os.MkdirAll(filepath.Dir(path), 0o755)
				os.WriteFile(path, []byte(err.Error()+"\n"), 0o644)
				return
			}
			want, rerr := os.ReadFile(path)
			if rerr != nil || string(want) != err.Error()+"\n" {
				t.Errorf("%s: got\n%s\nwant\n%s", path, err, want)
			}
		})
	}
}

// Build stops the same way, before its first read of the host, so a
// caller that skipped Early still changes and reads nothing.
func TestBuildStopsBeforeReadingTheHost(t *testing.T) {
	h := newBlank()
	if _, err := hostadmit.Build(h, input(t, "home-b", hostadmit.Flags{})); err == nil {
		t.Fatal("a data site without --watchdog was planned")
	}
	if len(h.ran) != 0 {
		t.Errorf("Build ran %q before stopping", h.ran)
	}
}
```

Create `internal/hostadmit/facts_test.go`, in `package hostadmit` (internal, since `Facts` is unexported):

```go
package hostadmit

import "testing"

// The probe's lines become facts; a repeated key is a list, and an answer
// cut short, without "end", is refused rather than read as a host with
// nothing on it.
func TestParseFacts(t *testing.T) {
	out := "engine 29.9.1\npkg docker-ce installed\npkg uidmap absent\n" +
		"passwd paisans-f2a9:x:1001:1002::/home/paisans-f2a9:/bin/bash\ngroups paisans-f2a9 users\ngroup paisans-f2a9\n" +
		"subuid ubuntu:100000:65536\nsubuid paisans-f2a9:165536:65536\n" +
		"file /usr/local/libexec/paisans/wg-psns-f2a9 " + "ab12" + " 755 0 0\n" +
		"file /etc/sudoers.d/paisans-f2a9 link\n" +
		"dir /etc/wireguard drwx------ 0 0\nbridge virbr0\nbridge docker0\nlan 192.168.1.20/24\nend\n"
	f, err := parseFacts(out, "paisans-f2a9")
	if err != nil {
		t.Fatal(err)
	}
	if f.Docker.Engine != "29.9.1" || !f.Packages["docker-ce"] || f.Packages["uidmap"] {
		t.Errorf("docker: %+v %v", f.Docker, f.Packages)
	}
	u := f.User
	if !u.Exists || u.UID != 1001 || u.GID != 1002 || u.Home != "/home/paisans-f2a9" || u.Group != "paisans-f2a9" || len(u.Groups) != 2 {
		t.Errorf("user: %+v", u)
	}
	if r := f.SubUID["paisans-f2a9"]; len(r) != 1 || r[0].Start != 165536 || r[0].Count != 65536 {
		t.Errorf("subuid: %+v", f.SubUID)
	}
	if h := f.Files["/usr/local/libexec/paisans/wg-psns-f2a9"]; !h.Present || h.Sum != "ab12" || h.Mode != "755" || h.UID != 0 {
		t.Errorf("helper: %+v", h)
	}
	if s := f.Files["/etc/sudoers.d/paisans-f2a9"]; !s.Link {
		t.Errorf("sudoers: %+v", s)
	}
	if w := f.Dirs["/etc/wireguard"]; !w.Present || w.Mode != "drwx------" {
		t.Errorf("wireguard: %+v", w)
	}
	if len(f.Bridges) != 2 || f.LAN != "192.168.1.20/24" {
		t.Errorf("bridges %v, lan %q", f.Bridges, f.LAN)
	}
	if _, err := parseFacts("engine 29.9.1\n", "paisans-f2a9"); err == nil {
		t.Error("an answer with no end line was read")
	}
}
```

Create `internal/hostadmit/execute_internal_test.go` (`package hostadmit`), which replaces `parts` for the test:

```go
package hostadmit

import (
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply/applytest"
	"github.com/paisans-software/paisans-cli/internal/ui"
)
```

(Use whatever fake transport `internal/apply/applytest` offers, or a small local one with `Run`, `RunInput`, `ReadFile`, `WriteFile`, `Describe` that answers the probe from a map the test changes; the listing below names it `host`.)

```go
// withParts runs the test with ps as admit's parts.
func withParts(t *testing.T, ps []part) {
	saved := parts
	parts = ps
	t.Cleanup(func() { parts = saved })
}

// A step that needs the user's UID is provisional until the user exists:
// Execute runs what comes before it, reads the host again, and runs the
// rest with the real UID.
func TestExecuteRebuildsOnceTheUserExists(t *testing.T) {
	h := newTestHost() // answers the probe with passwd absent
	h.onRun = func(command string) {
		if strings.HasPrefix(command, "useradd ") {
			h.set("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash")
		}
	}
	withParts(t, []part{
		{"user", func(p *planner) error {
			if !p.f.User.Exists {
				p.step(Step{Title: "create user paisans-f2a9", Command: "useradd paisans-f2a9"})
			}
			return nil
		}},
		{"egress", func(p *planner) error {
			p.step(Step{Title: "load the egress table", Command: "nft uid " + uidOf(p), Provisional: !p.f.User.Exists})
			return nil
		}},
	})
	in := testInput(t)
	plan, err := Build(h, in)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Steps[1].Provisional || !strings.Contains(plan.Text(), "nft uid <uid>") {
		t.Fatalf("the egress step is not provisional with <uid>:\n%s", plan.Text())
	}
	if err := Execute(h, in, plan, ui.Discard()); err != nil {
		t.Fatal(err)
	}
	if got := h.ranMatching("nft uid"); len(got) != 1 || got[0] != "nft uid 1001" {
		t.Errorf("the egress step ran as %q, want once with uid 1001", got)
	}
}

// The plan Execute rebuilds must be the approved one from the provisional
// step on, in everything but the values it could not know: a host that
// changed while admit ran is not acted on.
func TestExecuteStopsWhenTheRebuiltPlanDiffers(t *testing.T) {
	h := newTestHost()
	h.onRun = func(command string) {
		if strings.HasPrefix(command, "useradd ") {
			h.set("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash")
			h.set("surprise", "yes")
		}
	}
	withParts(t, []part{
		{"user", func(p *planner) error {
			if !p.f.User.Exists {
				p.step(Step{Title: "create user paisans-f2a9", Command: "useradd paisans-f2a9"})
			}
			return nil
		}},
		{"egress", func(p *planner) error {
			if p.f.raw["surprise"] != nil {
				p.step(Step{Title: "something new", Command: "rm -rf /"})
			}
			p.step(Step{Title: "load the egress table", Command: "nft uid " + uidOf(p), Provisional: !p.f.User.Exists})
			return nil
		}},
	})
	in := testInput(t)
	plan, _ := Build(h, in)
	err := Execute(h, in, plan, ui.Discard())
	if err == nil || !strings.Contains(err.Error(), "the host changed while admit ran") {
		t.Fatalf("Execute = %v", err)
	}
	if len(h.ranMatching("rm -rf")) != 0 || len(h.ranMatching("nft uid")) != 0 {
		t.Errorf("Execute ran past the change: %q", h.ran)
	}
}
```

(`newTestHost`, `testInput`, `ranMatching`, `set` and `raw` are written with the test; `raw` is a field of `Facts` holding every key's values, which `parseFacts` fills, so a test part can read a fact no named field carries. If `ui.Discard` does not exist, use the reporter `cmd/paisans` tests use, or `ui.New(io.Discard, false)`.)

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/hostadmit/`
Expected: FAIL to compile: `undefined: hostadmit.Flags`, `hostadmit.Early`, `hostadmit.Build`, `parseFacts`, `part`.

- [ ] **Step 4: Write the plan model**

Create `internal/hostadmit/plan.go`:

```go
package hostadmit

import (
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// Flags are the owner's choices: the two grants, where Docker's data goes,
// and the two host-wide permissions a host may need.
type Flags struct {
	Watchdog, PublicPorts bool
	// DataDir is --data-dir: admit creates DataDir/paisans-<token> for the
	// deployment's Docker data. "" leaves it under the user's home.
	DataDir string
	// BridgeNetfilter is --bridge-netfilter: load br_netfilter on a host
	// whose own bridges its FORWARD chain will then filter.
	BridgeNetfilter bool
	// MeshLAN is --mesh-lan: let the mesh dial the host's own LAN.
	MeshLAN bool
}

// Grants are the grants the flags give.
func (f Flags) Grants() admission.Grants {
	return admission.Grants{Watchdog: f.Watchdog, PublicPorts: f.PublicPorts}
}

// Input is everything admit is given: the request, from a file or built
// from the configuration, its digest ("" with --site), the flags, and the
// time the record will carry.
type Input struct {
	Request admission.Request
	Digest  string
	Flags   Flags
	Now     time.Time
}

// Early is admit's first check, before anything reaches the host: a grant
// the site's roles need and the owner did not give, a grant it does not
// need, and a --data-dir that is not an absolute, clean path. Each is the
// owner's to decide, so admit stops having read and changed nothing.
func Early(in Input) error {
	needed, given := in.Request.Grants, in.Flags.Grants()
	var hints, explains []string
	if needed.Watchdog && !given.Watchdog {
		hints = append(hints, "this is a data site, and admitting it needs --watchdog")
		explains = append(explains, "Its Patroni member needs the host's watchdog to fail over safely: without one, a member that has lost its lock can keep acting as leader. The watchdog reboots the whole machine when Patroni stops renewing it. Admit it with --watchdog, or do not admit it.")
	}
	if needed.PublicPorts && !given.PublicPorts {
		hints = append(hints, "this site runs the toolkit's Caddy, and admitting it needs --public-ports")
		explains = append(explains, "The gateway, or a monitor with web_server.managed_by: paisans, binds ports 80 and 443. --public-ports lowers net.ipv4.ip_unprivileged_port_start to 80 for the whole host, so every local user may then bind ports 80 to 1023, and opens 80 and 443 in the firewall. Admit it with --public-ports, or do not admit it; a monitor behind your own web server (web_server.managed_by: external) needs neither.")
	}
	if given.Watchdog && !needed.Watchdog {
		hints = append(hints, "--watchdog is not needed: this site holds no data role")
		explains = append(explains, "The watchdog can reboot the machine and is given only to a data site's Patroni. Drop --watchdog.")
	}
	if given.PublicPorts && !needed.PublicPorts {
		hints = append(hints, "--public-ports is not needed: this site binds no port below 1024")
		explains = append(explains, "--public-ports lets every local user bind ports 80 to 1023 and is given only to a site that runs the toolkit's Caddy. Drop --public-ports.")
	}
	if d := in.Flags.DataDir; d != "" && (!path.IsAbs(d) || path.Clean(d) != d || d == "/") {
		hints = append(hints, "--data-dir must be an absolute path in its plain form")
		explains = append(explains, fmt.Sprintf("%q is not: give the directory as /srv/data, with no trailing slash, no . or .., and not / itself.", d))
	}
	if len(hints) == 0 {
		return nil
	}
	return &ui.Problem{Hint: strings.Join(hints, "; "), Explain: strings.Join(explains, " ") + " Nothing was read or changed."}
}

// Step is one change admit still needs to make, in order.
type Step struct {
	// Title is the step's short name, Describe its sentence.
	Title, Describe string
	// File, when set, is written first, by the transport, as root.
	File *hostprep.File
	// Command, when set, runs after File, with Input on its stdin when
	// Input is not empty: content written as the deployment's user goes
	// there, never on a command line.
	Command, Input string
	Kinds          []string
	// Provisional marks a step rendered before the user existed, with
	// <uid> and <subordinate range> in place of the values useradd will
	// give it. Execute never runs one: it reads the host again once the
	// user exists and runs the step as rebuilt.
	Provisional bool
}

// Plan is what admit found on one host for one deployment.
type Plan struct {
	Deployment, User, Profile, Host string
	Steps                            []Step
	Present, Foreign                 []string
	Warnings                         []hostprep.Warning
}

// planner is one Build: the host, the input and what the probe found, and
// the plan as the parts fill it.
type planner struct {
	t       apply.Transport
	in      Input
	d       deployment.Deployment
	f       Facts
	profile hostprep.Profile
	host    hostprep.OSRelease
	plan    *Plan
	// record is the admission record the record part writes; parts before
	// it fill what they decide (data_dir, bridge_netfilter, web_server).
	record admission.Record
}

func (p *planner) step(s Step)                 { p.plan.Steps = append(p.plan.Steps, s) }
func (p *planner) present(what string)          { p.plan.Present = append(p.plan.Present, what) }
func (p *planner) warn(hint, detail string)     { p.plan.Warnings = append(p.plan.Warnings, hostprep.Warning{Hint: hint, Detail: detail}) }
func (p *planner) section(s hostprep.Section) {
	for _, st := range s.Steps {
		p.step(Step{Title: st.Title, Describe: st.Describe, File: st.File, Command: st.Command, Kinds: st.Kinds})
	}
	p.plan.Present = append(p.plan.Present, s.Present...)
	p.plan.Foreign = append(p.plan.Foreign, s.Foreign...)
	p.plan.Warnings = append(p.plan.Warnings, s.Warnings...)
}

// part is one stage of admit, in the order parts lists them.
type part struct {
	name string
	plan func(*planner) error
}

// parts are admit's stages in the order they run: the spec's What it does,
// with the claim first, delegation and the egress table before linger
// starts the user's manager, and AppArmor after the packages that bring its
// profile. Each is added by the task that writes it; the table in the
// phase 5 plan lists them all.
var parts = []part{}

// Build reads the host t reaches and plans admitting in.Request there. A
// refusal is an error; a plan with no steps is a host already admitted as
// asked.
func Build(t apply.Transport, in Input) (*Plan, error) {
	if err := Early(in); err != nil {
		return nil, err
	}
	content, found, err := t.ReadFile("/etc/os-release")
	if err != nil {
		return nil, fmt.Errorf("reading /etc/os-release on %s: %w", t.Describe(), err)
	}
	if !found {
		return nil, fmt.Errorf("%s has no /etc/os-release, so no host profile can be chosen for it", t.Describe())
	}
	host := hostprep.ParseOSRelease(content)
	profile, err := hostprep.Select(host)
	if err != nil {
		return nil, err
	}
	d := deployment.Deployment{ID: in.Request.Deployment}
	facts, err := probe(t, d, in)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", t.Describe(), err)
	}
	p := &planner{t: t, in: in, d: d, f: facts, profile: profile, host: host,
		plan:   &Plan{Deployment: in.Request.Deployment, User: in.Request.User, Profile: profile.ID() + " " + profile.Version(), Host: t.Describe()},
		record: admission.NewRecord(in.Request, in.Flags.Grants(), in.Digest, in.Now)}
	for _, pt := range parts {
		if err := pt.plan(p); err != nil {
			return nil, fmt.Errorf("%s: %w", pt.name, err)
		}
	}
	return p.plan, nil
}

// uidOf is the user's UID for a step's command or file, "<uid>" while the
// user does not exist; a step that uses it is Provisional then.
func uidOf(p *planner) string {
	if !p.f.User.Exists {
		return "<uid>"
	}
	return fmt.Sprint(p.f.User.UID)
}

// Text is the plan as a reviewer reads it in a golden file: every step with
// its command, its file and its stdin in full, then what was present, what
// was another's, and each warning.
func (p *Plan) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "admit %s as %s on %s (%s)\n", p.Deployment, p.User, p.Host, p.Profile)
	indent := func(prefix, text string) {
		for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			fmt.Fprintf(&b, "%s%s\n", prefix, line)
		}
	}
	for i, s := range p.Steps {
		fmt.Fprintf(&b, "\nstep %d: %s\n", i+1, s.Title)
		indent("  ", s.Describe)
		if s.Provisional {
			b.WriteString("  provisional: run once the user exists\n")
		}
		if s.File != nil {
			fmt.Fprintf(&b, "  write %s mode %04o:\n", s.File.Path, s.File.Mode)
			indent("    | ", s.File.Content)
		}
		if s.Command != "" {
			b.WriteString("  run:\n")
			indent("    $ ", s.Command)
		}
		if s.Input != "" {
			b.WriteString("  stdin:\n")
			indent("    | ", s.Input)
		}
	}
	for _, s := range p.Present {
		fmt.Fprintf(&b, "\npresent: %s", s)
	}
	for _, s := range p.Foreign {
		fmt.Fprintf(&b, "\nforeign: %s", s)
	}
	for _, w := range p.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s", w.Hint)
		if w.Detail != "" {
			fmt.Fprintf(&b, "\n  %s", w.Detail)
		}
	}
	b.WriteString("\n")
	return b.String()
}

// Show reports the plan as host prepare's does: each step pending under
// its title, its sentence as a detail, then under --verbose what was
// present and another's, then the warnings.
func (p *Plan) Show(r ui.Reporter) {
	r.Detail("host profile %s", p.Profile)
	for _, s := range p.Steps {
		st := r.Step(s.Title)
		st.Detail("%s", s.Describe)
		st.End(ui.Pending, "")
	}
	if r.Verbose() {
		for _, s := range p.Present {
			r.Step(s).End(ui.OK, "")
		}
		for _, s := range p.Foreign {
			r.Step(s).End(ui.OK, "not paisans")
		}
	}
	p.Warn(r)
}

// Warn reports the warnings alone, as --execute does.
func (p *Plan) Warn(r ui.Reporter) {
	for _, w := range p.Warnings {
		r.Warn(w.Hint, w.Detail)
	}
}

// Execute runs approved's steps in order and stops at the first failure,
// naming the step. At the first provisional step it reads the host again
// and plans again, now that the user exists, and carries on with the plan
// it rebuilt, provided that plan's steps from there on carry the titles
// the owner approved: anything else means the host changed while admit ran,
// and nothing more is done.
func Execute(t apply.Transport, in Input, approved *Plan, r ui.Reporter) error {
	steps := approved.Steps
	for i := 0; i < len(steps); i++ {
		if steps[i].Provisional {
			rebuilt, err := Build(t, in)
			if err != nil {
				return fmt.Errorf("reading the host again once %s existed: %w", approved.User, err)
			}
			if err := sameTitles(steps[i:], rebuilt.Steps); err != nil {
				return err
			}
			steps = append(steps[:i:i], rebuilt.Steps...)
		}
		if err := run(t, steps[i], r); err != nil {
			return err
		}
	}
	return nil
}

// sameTitles is nil when rebuilt holds exactly the approved steps' titles,
// none of them provisional.
func sameTitles(approved, rebuilt []Step) error {
	var a, b []string
	for _, s := range approved {
		a = append(a, s.Title)
	}
	for _, s := range rebuilt {
		if s.Provisional {
			return fmt.Errorf("the host changed while admit ran: %q still needs the user, which was created. Run host admit again to see the plan", s.Title)
		}
		b = append(b, s.Title)
	}
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		return fmt.Errorf("the host changed while admit ran: the steps left were\n- %s\nand are now\n- %s\nNothing more was done. Run host admit again to see the plan", strings.Join(a, "\n- "), strings.Join(b, "\n- "))
	}
	return nil
}

// run runs one step, as host prepare's Execute does.
func run(t apply.Transport, s Step, r ui.Reporter) error {
	st := r.Step(s.Title)
	st.Detail("%s", s.Describe)
	if s.File != nil {
		if err := t.WriteFile(s.File.Path, s.File.Content, s.File.Mode); err != nil {
			st.Fail(err)
			return fmt.Errorf("%s: %w", s.Title, err)
		}
	}
	if s.Command != "" {
		var out string
		var err error
		if s.Input != "" {
			out, err = t.RunInput(s.Command, s.Input)
		} else {
			out, err = t.Run(s.Command)
		}
		if err != nil {
			st.Fail(err)
			return fmt.Errorf("%s: %w: %s", s.Title, err, strings.TrimSpace(out))
		}
	}
	st.Done("")
	return nil
}
```

- [ ] **Step 5: Write the probe**

Create `internal/hostadmit/snippets/probe.sh.tmpl`. It changes nothing, reads nothing inside the deployment user's home (Task 7 and Task 12 read there as the user), and ends with `end` so a cut answer is refused:

```sh
{{- /* paisans host admit: read only. One "key value" line per fact; a key
        that repeats is a list. Nothing here changes the host, and nothing
        reads inside the deployment user's home, which is read as the user. */ -}}
export LC_ALL=C PATH=/usr/sbin:/usr/bin:/sbin:/bin
u={{.User}}
say() { printf '%s %s\n' "$1" "$2"; }
# Docker, judged by its binary so a stopped daemon is judged too.
if command -v dockerd >/dev/null 2>&1; then say engine "$(dockerd --version 2>/dev/null | sed -n 's/^Docker version \([^,]*\),.*/\1/p')"; else say engine absent; fi
for p in docker-ce docker.io docker-ce-rootless-extras uidmap nftables; do
  if dpkg-query -W -f='${Status}' "$p" 2>/dev/null | grep -q 'ok installed$'; then say pkg "$p installed"; else say pkg "$p absent"; fi
done
if command -v dockerd-rootless-setuptool.sh >/dev/null 2>&1; then say setuptool present; else say setuptool absent; fi
if command -v snap >/dev/null 2>&1 && snap list docker >/dev/null 2>&1; then say snap docker; else say snap absent; fi
if [ -S /var/run/docker.sock ]; then say system-socket present; else say system-socket absent; fi
# AppArmor's restriction on unprivileged user namespaces, and the profile
# Docker's rootless extras bring.
say userns-restrict "$(sysctl -n kernel.apparmor_restrict_unprivileged_userns 2>/dev/null || echo 0)"
if grep -q rootlesskit /sys/kernel/security/apparmor/profiles 2>/dev/null; then say rootlesskit-profile loaded; else say rootlesskit-profile absent; fi
# The user, and every user's subordinate ranges.
home=
if line=$(getent passwd "$u"); then
  say passwd "$line"
  say groups "$(id -Gn "$u")"
  say group "$(id -gn "$u")"
  uid=$(id -u "$u")
  home=$(printf '%s' "$line" | cut -d: -f6)
  if [ -e "/var/lib/systemd/linger/$u" ]; then say linger yes; else say linger no; fi
  say manager "$(systemctl is-active "user@$uid.service" 2>/dev/null || true)"
else
  say passwd absent
fi
awk -F: 'NF >= 3 { print "subuid " $1 ":" $2 ":" $3 }' /etc/subuid 2>/dev/null
awk -F: 'NF >= 3 { print "subgid " $1 ":" $2 ":" $3 }' /etc/subgid 2>/dev/null
# Each file admit writes as root: its checksum, mode and owner, so admit
# compares with what it would write without printing what is there.
for f in{{ range .Files }} {{ . }}{{ end }}; do
  if [ -L "$f" ]; then say file "$f link"
  elif [ -f "$f" ]; then say file "$f $(sha256sum < "$f" | cut -c1-64) $(stat -c '%a %u %g' "$f")"
  else say file "$f absent"; fi
done
if systemctl is-enabled {{.EgressUnit}} >/dev/null 2>&1; then say egress-enabled yes; else say egress-enabled no; fi
if nft list table inet {{.EgressTable}} >/dev/null 2>&1; then say egress-table present; else say egress-table absent; fi
# Each directory admit creates or checks, as ls -ldn prints it, so a link,
# a non-directory and an ACL (a + after the mode) are all seen.
for d in{{ range .Dirs }} {{ . }}{{ end }}; do
  if [ -e "$d" ] || [ -L "$d" ]; then say dir "$d $(ls -ldn "$d" | awk '{ print $1, $3, $4 }')"; else say dir "$d absent 0 0"; fi
done
# The kernel and the network.
if [ -d /sys/module/br_netfilter ]; then say br_netfilter loaded; else say br_netfilter absent; fi
ip -d -o link show type bridge 2>/dev/null | awk -F': ' '{ sub(/@.*/, "", $2); print "bridge " $2 }'
say forward-policy "$(iptables -S FORWARD 2>/dev/null | awk '$1 == "-P" { print $3; exit }')"
say input-policy "$(iptables -S INPUT 2>/dev/null | awk '$1 == "-P" { print $3; exit }')"
say input-rules "$(iptables -S INPUT 2>/dev/null | grep -c '^-A INPUT' || true)"
say port-start "$(sysctl -n net.ipv4.ip_unprivileged_port_start)"
say ip-forward "$(sysctl -n net.ipv4.ip_forward)"
dev=$(ip -4 -o route show default 2>/dev/null | awk '{ for (i = 1; i < NF; i++) if ($i == "dev") { print $(i + 1); exit } }')
say default-dev "${dev:-absent}"
if [ -n "$dev" ]; then ip -4 -o addr show dev "$dev" scope global | awk '{ print "lan " $4; exit }'; fi
if systemctl is-active firewalld >/dev/null 2>&1; then say firewalld active; else say firewalld inactive; fi
nft list chains 2>/dev/null | awk '/^table / { t = $2 " " $3 } /^[ \t]*chain / { c = $2 } /hook input/ { print "input-chain " t " " c }'
for f in /etc/udev/rules.d/60-paisans-*-watchdog.rules; do [ -e "$f" ] && say watchdog-rule "$f"; done
for f in /etc/paisans/admitted.*.json; do [ -e "$f" ] && say record "$f"; done
# Where Docker's data will live: --data-dir, or the user's home, or /home
# for a user not made yet; the nearest directory that exists.
target={{ if .DataParent }}{{ .DataParent }}{{ else }}${home:-/home}{{ end }}
while [ ! -e "$target" ]; do target=$(dirname "$target"); done
say data-path "$target"
say data-fs "$(stat -f -c %T "$target")"
say data-avail "$(df -P -B1 "$target" | awk 'NR == 2 { print $4 }')"
echo end
```

The template's first line is the comment that carries `probeMarker`'s text, "paisans host admit: read only"; `text/template` drops the comment, so also start the script body with `# paisans host admit: read only` on its own line after the comment, which is what the fake host matches.

Create `internal/hostadmit/facts.go`:

```go
package hostadmit

import (
	"embed"
	"fmt"
	"strconv"
	"strings"
	"text/template"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

//go:embed snippets
var snippets embed.FS

// Facts are what the probe found, read once per Build.
type Facts struct {
	Docker   DockerFacts
	Packages map[string]bool
	AppArmor struct{ Restricted, Profile bool }
	User     UserFacts
	// SubUID and SubGID are every user's ranges, by name.
	SubUID, SubGID map[string][]IDRange
	Files          map[string]FileState
	Dirs           map[string]DirState
	EgressEnabled, EgressTable bool
	BrNetfilter    bool
	Bridges        []string
	ForwardPolicy  string
	InputPolicy    string
	InputRules     int
	InputChains    []string
	Firewalld      bool
	PortStart, IPForward string
	DefaultDev, LAN      string
	WatchdogRules  []string
	Records        []string
	Data           struct {
		Path, FS string
		Avail    int64
	}
	// raw is every line, by key, for what no field above carries.
	raw map[string][]string
}

type DockerFacts struct {
	// Engine is dockerd's version, "" when Docker is absent.
	Engine     string
	Setuptool  bool
	Snap       bool
	SystemSock bool
}

type UserFacts struct {
	Exists        bool
	Name          string
	UID, GID      int
	Home, Group   string
	Groups        []string
	Linger        bool
	ManagerActive bool
}

type IDRange struct{ Start, Count int64 }

// FileState is one file admit writes, as the probe saw it.
type FileState struct {
	Present, Link bool
	Sum, Mode     string
	UID, GID      int
}

// DirState is one directory as ls -ldn printed it.
type DirState struct {
	Present  bool
	Mode     string
	UID, GID int
}

// probeData is what probe.sh.tmpl is rendered with.
type probeData struct {
	User, Token, Prefix, EgressUnit, EgressTable, DataParent string
	Files, Dirs                                              []string
}

// probedFiles are the files admit writes as root, whose state the probe
// reads; each task that writes one adds its path here.
func probedFiles(d deployment.Deployment, in Input) []string {
	helper, _ := apply.WGHelper{Iface: d.Interface()}.Path()
	record, _ := admission.Path(d.Token())
	return []string{
		delegatePath, modulesLoadPath, sysctlPath, sysctlBeforePath,
		helper, sudoersPath(d), egressFilePath(d), "/etc/systemd/system/" + d.EgressUnit(),
		watchdogRulePath(d), "/etc/systemd/system/" + d.Prefix() + "-watchdog.service",
		record,
	}
}

// probedDirs are the directories admit creates or checks.
func probedDirs(d deployment.Deployment, in Input) []string {
	dirs := []string{"/etc/wireguard", admission.Dir, deployment.Base, d.Root(), apply.HelperDir}
	if in.Flags.DataDir != "" {
		dirs = append(dirs, in.Flags.DataDir, in.Flags.DataDir+"/"+d.Prefix())
	}
	return dirs
}

func probe(t apply.Transport, d deployment.Deployment, in Input) (Facts, error) {
	tmpl, err := template.ParseFS(snippets, "snippets/probe.sh.tmpl")
	if err != nil {
		return Facts{}, err
	}
	var script strings.Builder
	if err := tmpl.Execute(&script, probeData{
		User: in.Request.User, Token: d.Token(), Prefix: d.Prefix(),
		EgressUnit: d.EgressUnit(), EgressTable: d.EgressTable(), DataParent: in.Flags.DataDir,
		Files: probedFiles(d, in), Dirs: probedDirs(d, in),
	}); err != nil {
		return Facts{}, err
	}
	out, err := t.Run(script.String())
	if err != nil {
		return Facts{}, err
	}
	return parseFacts(out, in.Request.User)
}

// parseFacts reads the probe's lines. Values the probe prints are paths,
// numbers and names; a line it cannot read is refused rather than skipped.
func parseFacts(out, user string) (Facts, error) {
	f := Facts{Packages: map[string]bool{}, SubUID: map[string][]IDRange{}, SubGID: map[string][]IDRange{},
		Files: map[string]FileState{}, Dirs: map[string]DirState{}, raw: map[string][]string{}}
	f.User.Name = user
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || lines[len(lines)-1] != "end" {
		return f, fmt.Errorf("the probe's answer was cut short")
	}
	for _, line := range lines[:len(lines)-1] {
		key, v, _ := strings.Cut(line, " ")
		f.raw[key] = append(f.raw[key], v)
		switch key {
		case "engine":
			if v != "absent" {
				f.Docker.Engine = v
			}
		case "pkg":
			name, state, _ := strings.Cut(v, " ")
			f.Packages[name] = state == "installed"
		case "setuptool":
			f.Docker.Setuptool = v == "present"
		case "snap":
			f.Docker.Snap = v == "docker"
		case "system-socket":
			f.Docker.SystemSock = v == "present"
		case "userns-restrict":
			f.AppArmor.Restricted = v == "1"
		case "rootlesskit-profile":
			f.AppArmor.Profile = v == "loaded"
		case "passwd":
			if v == "absent" {
				continue
			}
			p := strings.Split(v, ":")
			if len(p) != 7 || p[0] != user {
				return f, fmt.Errorf("unreadable passwd entry %q", v)
			}
			uid, err1 := strconv.Atoi(p[2])
			gid, err2 := strconv.Atoi(p[3])
			if err1 != nil || err2 != nil {
				return f, fmt.Errorf("unreadable passwd entry %q", v)
			}
			f.User.Exists, f.User.UID, f.User.GID, f.User.Home = true, uid, gid, p[5]
		case "groups":
			f.User.Groups = strings.Fields(v)
		case "group":
			f.User.Group = v
		case "linger":
			f.User.Linger = v == "yes"
		case "manager":
			f.User.ManagerActive = v == "active"
		case "subuid", "subgid":
			p := strings.Split(v, ":")
			start, err1 := strconv.ParseInt(p[len(p)-2], 10, 64)
			count, err2 := strconv.ParseInt(p[len(p)-1], 10, 64)
			if len(p) != 3 || err1 != nil || err2 != nil {
				return f, fmt.Errorf("unreadable %s line %q", key, v)
			}
			m := f.SubUID
			if key == "subgid" {
				m = f.SubGID
			}
			m[p[0]] = append(m[p[0]], IDRange{start, count})
		case "file":
			fields := strings.Fields(v)
			switch {
			case len(fields) == 2 && fields[1] == "link":
				f.Files[fields[0]] = FileState{Present: true, Link: true}
			case len(fields) == 2 && fields[1] == "absent":
				f.Files[fields[0]] = FileState{}
			case len(fields) == 5:
				uid, _ := strconv.Atoi(fields[3])
				gid, _ := strconv.Atoi(fields[4])
				f.Files[fields[0]] = FileState{Present: true, Sum: fields[1], Mode: fields[2], UID: uid, GID: gid}
			default:
				return f, fmt.Errorf("unreadable file line %q", v)
			}
		case "dir":
			fields := strings.Fields(v)
			if len(fields) != 4 {
				return f, fmt.Errorf("unreadable dir line %q", v)
			}
			uid, _ := strconv.Atoi(fields[2])
			gid, _ := strconv.Atoi(fields[3])
			f.Dirs[fields[0]] = DirState{Present: fields[1] != "absent", Mode: fields[1], UID: uid, GID: gid}
		case "egress-enabled":
			f.EgressEnabled = v == "yes"
		case "egress-table":
			f.EgressTable = v == "present"
		case "br_netfilter":
			f.BrNetfilter = v == "loaded"
		case "bridge":
			f.Bridges = append(f.Bridges, v)
		case "forward-policy":
			f.ForwardPolicy = v
		case "input-policy":
			f.InputPolicy = v
		case "input-rules":
			f.InputRules, _ = strconv.Atoi(v)
		case "input-chain":
			f.InputChains = append(f.InputChains, v)
		case "firewalld":
			f.Firewalld = v == "active"
		case "port-start":
			f.PortStart = v
		case "ip-forward":
			f.IPForward = v
		case "default-dev":
			f.DefaultDev = v
		case "lan":
			f.LAN = v
		case "watchdog-rule":
			f.WatchdogRules = append(f.WatchdogRules, v)
		case "record":
			f.Records = append(f.Records, v)
		case "data-path":
			f.Data.Path = v
		case "data-fs":
			f.Data.FS = v
		case "data-avail":
			f.Data.Avail, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if f.User.Exists && f.User.Group == "" {
		return f, fmt.Errorf("no primary group read for %s", user)
	}
	return f, nil
}
```

The path constants and functions `delegatePath`, `modulesLoadPath`, `sysctlPath`, `sysctlBeforePath`, `sudoersPath`, `egressFilePath` and `watchdogRulePath` are each defined by the task that writes the file (Tasks 5, 7, 10, 8, 6, 10). Until then, define them in `facts.go` as:

```go
const (
	delegatePath     = "/etc/systemd/system/user@.service.d/delegate.conf"
	modulesLoadPath  = "/etc/modules-load.d/paisans.conf"
	sysctlPath       = "/etc/sysctl.d/60-paisans.conf"
	sysctlBeforePath = admission.Dir + "/sysctl.before"
)

func sudoersPath(d deployment.Deployment) string      { return "/etc/sudoers.d/" + d.Prefix() }
func egressFilePath(d deployment.Deployment) string   { return admission.Dir + "/egress." + d.Token() + ".nft" }
func watchdogRulePath(d deployment.Deployment) string { return "/etc/udev/rules.d/60-" + d.Prefix() + "-watchdog.rules" }
```

and leave them here: they are what the probe reads, so they live beside it.

- [ ] **Step 6: Run the tests to verify they pass, and generate the stops**

Run: `go test ./internal/hostadmit/ -run 'TestEarlyStops' -update && go test ./internal/hostadmit/`
Expected: PASS. Read every file under `internal/hostadmit/testdata/stops/`: each names the flag, says what it grants or why it is not needed, and ends "Nothing was read or changed."

Run: `gofmt -l internal/hostadmit && go vet ./internal/hostadmit/`
Expected: nothing listed; no finding.

- [ ] **Step 7: Commit**

```bash
git add internal/hostadmit
git commit -m "feat: plan host admit from one read of the host

host admit is the one root step on someone else's machine, so it reads
the host once, read only, plans each change as an idempotent step, and
changes nothing without --execute. A grant the site's roles need and the
owner did not give, or one it does not need, stops it before anything
reaches the host. Steps that need the user's UID are drawn with a
placeholder until the user exists, and --execute reads the host again
then and refuses to go on if the plan it rebuilds is not the one the
owner approved.

Nothing runs it yet, so no changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
**How every later task treats the golden plans.** Tasks 4 to 12 each add a part, and a part changes the text of plans drawn by earlier tasks' cases (a blank host's plan gains the user's steps in Task 5, the egress table in Task 6, and so on). Each task's last test step is therefore `go test ./internal/hostadmit/ -update`, then `git diff internal/hostadmit/testdata` read in full: only the task's own part may have changed any plan, and every new step in it is read for its command, its file and its mode. A reviewer rejects a task whose regeneration changed another part's steps.

---

### Task 4: Docker and AppArmor

**Files:**
- Create: `internal/hostprep/admit.go`
- Modify: `internal/hostprep/hostprep.go` (the `Profile` interface), `internal/hostprep/ubuntu.go` (`dockerRepo`, `AdmitPackages`, `probePackages`'s list), `internal/hostprep/profiles/ubuntu-24.04/apt-install.sh.tmpl`
- Modify: `internal/hostcheck/docker.go` (export `AtLeast`), its callers in `internal/hostcheck`
- Create: `internal/hostadmit/docker.go`
- Test: `internal/hostprep/admit_test.go`, `internal/hostadmit/docker_test.go`, `internal/hostadmit/testdata/plans/docker-*.txt`

**Interfaces:**
- Consumes: Task 3's `planner`, `Facts.Docker`, `Facts.Packages`, `Facts.AppArmor`, `p.section`, `parts`.
- Produces: `hostprep.Profile.AdmitPackages(t Transport, host OSRelease, dockerPresent bool) (Section, error)`; `hostcheck.AtLeast(version, min string) bool`; `hostadmit.MinRootlessEngine = "29.5.0"`; `const systemDocker = "docker -H unix:///var/run/docker.sock"` in `hostadmit`, which every later task's system-Docker command starts with; parts `{"docker", planDocker}` and `{"apparmor", planAppArmor}`, appended to `parts` in that order.

- [ ] **Step 1: Write the failing tests**

Create `internal/hostprep/admit_test.go`:

```go
package hostprep_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

// With Docker absent, admit installs Docker from Docker's repository with
// the rootless extras; with it present, it installs the extras and the
// rest without upgrading anything already installed, so the owner's
// Docker is never upgraded by admit.
func TestAdmitPackages(t *testing.T) {
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		t.Fatal(err)
	}
	blank := freshHost()
	s, err := profile.AdmitPackages(blank, hostprep.ParseOSRelease(ubuntu2404), false)
	if err != nil {
		t.Fatal(err)
	}
	all := ""
	for _, st := range s.Steps {
		all += st.Title + "\n" + st.Command + "\n"
	}
	for _, want := range []string{"add docker apt key", "add docker apt repo", "docker-ce ", "docker-ce-rootless-extras", "uidmap", "nftables", "wireguard-tools", "ufw", "git"} {
		if !strings.Contains(all, want) {
			t.Errorf("a blank host's admit packages lack %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, "--no-upgrade") {
		t.Errorf("a blank host's install is --no-upgrade:\n%s", all)
	}

	present := freshHost()
	present.responses[probePackages] = "compose present\npkg docker-ce install ok installed\npkg wireguard-tools absent\npkg ufw install ok installed\npkg git install ok installed\nkeyring present\narch amd64\n"
	s, err = profile.AdmitPackages(present, hostprep.ParseOSRelease(ubuntu2404), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Steps) != 1 || !strings.Contains(s.Steps[0].Command, "--no-upgrade") || strings.Contains(s.Steps[0].Command, " docker-ce ") || !strings.Contains(s.Steps[0].Command, "docker-ce-rootless-extras") {
		t.Errorf("with Docker present, admit plans %+v", s.Steps)
	}
}
```

Create `internal/hostadmit/docker_test.go`:

```go
package hostadmit_test

import (
	"strings"
	"testing"
)

var dockerCases = addCases(
	goldenCase{name: "docker-blank", host: newBlank, site: "vm"},
	goldenCase{name: "docker-old-refused", site: "vm", host: func() *fakeHost {
		return newBlank().with("engine", "27.5.1").with("pkg", "docker-ce installed").with("system-socket", "present")
	}},
	goldenCase{name: "docker-current", site: "vm", host: func() *fakeHost {
		return newBlank().with("engine", "29.9.1").with("system-socket", "present").
			answer("docker compose version", "compose present\npkg docker-ce install ok installed\npkg ufw install ok installed\npkg git install ok installed\npkg wireguard-tools install ok installed\nkeyring present\narch amd64\n")
	}},
	goldenCase{name: "docker-snap-refused", site: "vm", host: func() *fakeHost {
		return newBlank().with("snap", "docker").with("engine", "29.9.1")
	}},
	goldenCase{name: "docker-distro-without-extras-refused", site: "vm", host: func() *fakeHost {
		return newBlank().with("engine", "29.6.0").with("pkg", "docker.io installed")
	}},
	goldenCase{name: "apparmor-extras-without-profile-refused", site: "vm", host: func() *fakeHost {
		return newBlank().with("engine", "29.9.1").with("setuptool", "present").
			with("pkg", "docker-ce-rootless-extras installed")
	}},
)

func TestDockerPlans(t *testing.T) { runGolden(t, dockerCases) }

// Every system-Docker command admit runs names the system socket, so an
// owner's DOCKER_HOST or context never points it elsewhere.
func TestSystemDockerIsNamedBySocket(t *testing.T) {
	for _, c := range allCases {
		_, plan := golden(t, c)
		if plan == nil {
			continue
		}
		for _, s := range plan.Steps {
			for _, line := range strings.Split(s.Command, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "docker ") && !strings.Contains(line, "-H unix:///var/run/docker.sock") {
					t.Errorf("%s: %s runs %q", c.name, s.Title, line)
				}
			}
		}
	}
}
```

The `pkg` key repeats; `with("pkg", ...)` replaces every `pkg` line, which these cases want: `newBlank` lists `pkg` lines only so a case can replace them. Where a case needs several packages, use `add`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/hostprep/ -run TestAdmitPackages; go test ./internal/hostadmit/ -run 'TestDockerPlans|TestSystemDocker'`
Expected: FAIL to compile, `profile.AdmitPackages undefined`; the hostadmit test fails with "no such file" for each golden plan, and `docker-old-refused` is planned rather than refused.

- [ ] **Step 3: The packages in hostprep**

In `internal/hostprep/hostprep.go`, add to `Profile`:

```go
	// AdmitPackages plans what host admit installs for a deployment that
	// runs rootless: with Docker absent, Docker's repository and packages
	// with its rootless extras; with Docker present, the extras alone,
	// never upgrading a package already installed; in both, uidmap,
	// nftables and the base packages. dockerPresent is admit's own
	// judgement, made from the engine's version.
	AdmitPackages(t Transport, host OSRelease, dockerPresent bool) (Section, error)
```

In `internal/hostprep/ubuntu.go`:

1. Add after `basePackages`:

```go
// rootlessPackages are what a deployment's own rootless daemon needs
// beside Docker: the setup tool and rootlesskit, newuidmap and newgidmap,
// and nft for the egress table. Docker's rootless page names the first two
// (https://docs.docker.com/engine/security/rootless/, read 2026-10-15).
var rootlessPackages = []string{"docker-ce-rootless-extras", "uidmap", "nftables"}
```

and add `rootlessPackages` to `all` in `probePackages`, and give `packageKinds` the kind `KindDocker` for each of them.

2. Move the keyring and sources half of `Packages` (from `if facts.keyring {` to the sources step) into:

```go
// dockerRepo plans Docker's apt key and repository, each only when it is
// not already as written.
func (u ubuntu) dockerRepo(t Transport, host OSRelease, facts packageFacts) (Section, error) {
	// ... the existing code, unchanged, appending to a Section it returns.
}
```

and call it from `Packages` where that code was, so host prepare's tests pass unchanged.

3. Add:

```go
// AdmitPackages: see Profile.
func (u ubuntu) AdmitPackages(t Transport, host OSRelease, dockerPresent bool) (Section, error) {
	var out Section
	facts, err := u.probePackages(t)
	if err != nil {
		return out, err
	}
	if facts.snapDocker {
		return out, fmt.Errorf("docker: Docker is installed as a snap, whose confinement keeps it from bind mounting under /srv and whose refreshes restart every container on snapd's schedule. Remove it yourself (snap remove docker), after checking nothing running depends on it, and admit again")
	}
	var install []string
	if !dockerPresent {
		repo, err := u.dockerRepo(t, host, facts)
		if err != nil {
			return out, err
		}
		out.add(repo)
		for _, p := range dockerPackages {
			if !facts.installed[p] {
				install = append(install, p)
			}
		}
	}
	for _, p := range append(append([]string{}, rootlessPackages...), basePackages...) {
		if facts.installed[p] {
			out.Present = append(out.Present, "package: "+p+" installed")
		} else {
			install = append(install, p)
		}
	}
	if len(install) == 0 {
		return out, nil
	}
	script, err := snippet(u.tmpl("apt-install.sh.tmpl"), map[string]any{"Packages": install, "NoUpgrade": dockerPresent})
	if err != nil {
		return out, err
	}
	describe := "packages: install " + strings.Join(install, " ")
	if dockerPresent {
		describe += ", upgrading nothing already installed"
	}
	out.Steps = append(out.Steps, Step{Kinds: packageKinds(install), Title: "install packages: " + strings.Join(install, ", "), Describe: describe, Command: script})
	return out, nil
}
```

In `internal/hostprep/profiles/ubuntu-24.04/apt-install.sh.tmpl`, make the install line:

```
apt-get -o DPkg::Lock::Timeout=300 -o Dpkg::Options::=--force-confold install -y{{ if .NoUpgrade }} --no-upgrade{{ end }}{{ range .Packages }} {{ . }}{{ end }}
```

and give its existing callers' data a `NoUpgrade` key (false), or switch the data to a struct with both fields, whichever keeps `text/template` from failing on a missing map key (a missing map key renders as `<no value>` in a map with `missingkey=default`, which is false-y for `if`; check it).

Create `internal/hostprep/admit.go` with the package comment line `// What host admit reuses from host prepare, exported.` and nothing else yet; Tasks 6, 9 and 10 add to it.

In `internal/hostcheck/docker.go`, rename `atLeast` to `AtLeast` (exported, comment unchanged but for the name), and update its callers in the package.

- [ ] **Step 4: Docker and AppArmor in hostadmit**

Create `internal/hostadmit/docker.go`:

```go
package hostadmit

import (
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/hostcheck"
)

// MinRootlessEngine is the oldest Docker Engine admit accepts: from 29.5 a
// rootless container with host networking shares the real host's network
// namespace, which every infrastructure container needs to bind the mesh
// address (Docker's rootless troubleshooting page, read 2026-10-15; spec,
// Observed R1).
const MinRootlessEngine = "29.5.0"

// systemDocker starts every command admit runs against the owner's system
// daemon, so an owner's DOCKER_HOST or docker context never redirects it.
const systemDocker = "docker -H unix:///var/run/docker.sock"

// planDocker refuses a snap Docker and an Engine older than
// MinRootlessEngine, since upgrading the owner's Docker is not admit's
// decision, and a present Docker that cannot run rootless; then plans the
// packages.
func planDocker(p *planner) error {
	dk := p.f.Docker
	if dk.Snap {
		return fmt.Errorf("Docker is installed as a snap, whose confinement keeps it from bind mounting under /srv and whose refreshes restart every container on snapd's schedule. Remove it yourself (snap remove docker), after checking nothing running depends on it, and admit again. Nothing was changed")
	}
	present := dk.Engine != ""
	if present {
		if !hostcheck.AtLeast(dk.Engine, MinRootlessEngine) {
			return fmt.Errorf("Docker Engine %s is installed, and admitting a deployment needs %s or later: from 29.5 a rootless container with host networking shares the host's network namespace, which every infrastructure container needs. Upgrade Docker yourself and admit again; upgrading your Docker is not admit's decision. Nothing was changed", dk.Engine, MinRootlessEngine)
		}
		if !p.f.Packages["docker-ce"] && !dk.Setuptool {
			return fmt.Errorf("Docker %s here is not Docker's own docker-ce and has no dockerd-rootless-setuptool.sh. The rootless extras come from Docker's apt repository, which installs beside docker-ce only. Install Docker from Docker's repository yourself, or admit on another host. Nothing was changed", dk.Engine)
		}
		p.present("docker: Engine " + dk.Engine)
	}
	s, err := p.profile.AdmitPackages(p.t, p.host, present)
	if err != nil {
		return err
	}
	p.section(s)
	return nil
}

// planAppArmor checks that Ubuntu's restriction on unprivileged user
// namespaces lets rootlesskit run: the profile docker-ce-rootless-extras
// installs must be loaded. When this run installs the extras, the check is
// a step after the install; when they are installed already and the
// profile is not loaded, admit refuses with the fix.
func planAppArmor(p *planner) error {
	a := p.f.AppArmor
	switch {
	case !a.Restricted:
		p.present("apparmor: unprivileged user namespaces are not restricted")
	case a.Profile:
		p.present("apparmor: rootlesskit's profile is loaded")
	case p.f.Packages["docker-ce-rootless-extras"]:
		return fmt.Errorf("AppArmor restricts unprivileged user namespaces here and no rootlesskit profile is loaded, though docker-ce-rootless-extras is installed. Load the profile it installed with systemctl reload apparmor, check aa-status lists rootlesskit, and admit again. Nothing was changed")
	default:
		p.step(Step{
			Title:    "check rootlesskit's AppArmor profile",
			Describe: "apparmor: Ubuntu restricts unprivileged user namespaces, so the rootlesskit profile docker-ce-rootless-extras installs must be loaded before the user's daemon can start",
			Command:  `grep -q rootlesskit /sys/kernel/security/apparmor/profiles || { echo "AppArmor restricts unprivileged user namespaces and no rootlesskit profile is loaded after installing docker-ce-rootless-extras. Load it with systemctl reload apparmor, check aa-status lists rootlesskit, and run host admit again"; exit 1; }`,
			Kinds:    []string{"docker"},
		})
	}
	return nil
}
```

In `internal/hostadmit/plan.go`, set:

```go
var parts = []part{
	{"docker", planDocker},
	{"apparmor", planAppArmor},
}
```

- [ ] **Step 5: Run the tests, generate the golden plans and read them**

Run: `go test ./internal/hostprep/ ./internal/hostcheck/ && go test ./internal/hostadmit/ -update && go test ./internal/hostadmit/`
Expected: PASS.

Read every new `internal/hostadmit/testdata/plans/docker-*.txt`: `docker-blank` adds Docker's key and repository and installs `docker-ce ... docker-ce-rootless-extras uidmap wireguard-tools ufw git` (nftables is present on the blank host) then checks the profile; `docker-current` installs with `--no-upgrade` and no Docker package; each `-refused` file is one line naming what holds and what the owner does.

- [ ] **Step 6: Commit**

```bash
git add internal/hostprep internal/hostcheck internal/hostadmit
git commit -m "feat: plan admit's docker and apparmor steps

A deployment's rootless daemon needs Docker Engine 29.5 or later, from
which a host-networked rootless container shares the host's network
namespace, and the rootlesskit profile Ubuntu's user namespace
restriction asks for. Admit installs Docker from Docker's repository
where it is absent; where it is present it never upgrades it, refusing
an old engine, a snap, or a Docker without the rootless extras, and
installs the extras without upgrading anything already installed.

Nothing runs admit yet, so no changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: The user, its subordinate IDs, delegation and linger

**Files:**
- Create: `internal/hostadmit/user.go`
- Modify: `internal/hostadmit/plan.go` (`parts`)
- Test: `internal/hostadmit/user_test.go`, `internal/hostadmit/testdata/plans/user-*.txt`

**Interfaces:**
- Consumes: Task 3's `Facts.User`, `Facts.SubUID`, `Facts.SubGID`, `Facts.Files[delegatePath]`, `uidOf`; `config.DeploymentUserProblem`.
- Produces: `var PrivilegedGroups = []string{"root", "sudo", "wheel", "admin", "adm", "docker", "lxd", "systemd-journal"}` (phase 7's doctor reads the same list); `func asUser(p *planner, command string) string`; `func rangeOf(p *planner) string` (`<subordinate range>` or `165536-231071`); `delegateConf` (the drop-in's content); parts `{"user", planUser}`, `{"delegation", planDelegation}` after `apparmor`, and `{"linger", planLinger}` after the `egress` part Task 6 adds (until Task 6, directly after `delegation`).

- [ ] **Step 1: Write the failing tests**

Create `internal/hostadmit/user_test.go`:

```go
package hostadmit_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

// current is a host with Docker 29.9.1 and its extras installed and the
// profile loaded, so the user's tests see only the user's steps change.
func current() *fakeHost {
	return newBlank().with("engine", "29.9.1").with("setuptool", "present").with("rootlesskit-profile", "loaded").
		with("pkg", "docker-ce installed").add("pkg", "docker-ce-rootless-extras installed").add("pkg", "uidmap installed").add("pkg", "nftables installed").
		with("system-socket", "present").
		answer("docker compose version", "compose present\npkg docker-ce install ok installed\npkg docker-ce-rootless-extras install ok installed\npkg uidmap install ok installed\npkg nftables install ok installed\npkg wireguard-tools install ok installed\npkg ufw install ok installed\npkg git install ok installed\nkeyring present\narch amd64\n")
}

var userCases = addCases(
	goldenCase{name: "user-fresh", host: current, site: "vm"},
	goldenCase{name: "user-existing-running", site: "vm", host: func() *fakeHost { return current().withUser() }},
	goldenCase{name: "user-not-paisans", site: "home-b", flags: hostadmit.Flags{Watchdog: true}, host: func() *fakeHost {
		return current().with("passwd", "community:x:1002:1002::/home/community:/bin/bash").with("groups", "community").
			with("group", "community").with("linger", "no").with("manager", "inactive").
			add("subuid", "community:231072:65536").add("subgid", "community:231072:65536")
	}},
	goldenCase{name: "user-no-subids-refused", site: "vm", host: func() *fakeHost {
		return current().with("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash").with("groups", "paisans-f2a9").with("group", "paisans-f2a9")
	}},
	goldenCase{name: "user-overlapping-subids-refused", site: "vm", host: func() *fakeHost {
		return current().withUser().add("subuid", "someone:170000:65536")
	}},
)

func TestUserPlans(t *testing.T) { runGolden(t, userCases) }

// A user that is root, or in any group that reaches past it, is refused
// whatever else it is: docker is the owner's system daemon, sudo and the
// rest reach root or every user's logs.
func TestAPrivilegedUserIsRefused(t *testing.T) {
	for _, group := range hostadmit.PrivilegedGroups {
		h := current().withUser().with("groups", "paisans-f2a9 "+group)
		_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
		if err == nil || !strings.Contains(err.Error(), group) {
			t.Errorf("a user in %s: %v", group, err)
		}
	}
	h := current().with("passwd", "paisans-f2a9:x:0:0::/root:/bin/bash").with("groups", "root").with("group", "root")
	if _, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{})); err == nil || !strings.Contains(err.Error(), "uid 0") {
		t.Errorf("a user with uid 0: %v", err)
	}
}

// The manager of a user that is already running is never restarted: not
// for the delegation drop-in, not for linger.
func TestARunningManagerIsNeverRestarted(t *testing.T) {
	plan, err := hostadmit.Build(current().withUser(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if strings.Contains(s.Command, "restart user@") || strings.Contains(s.Command, "try-restart user@") {
			t.Errorf("%s restarts the user's manager: %s", s.Title, s.Command)
		}
	}
	found := false
	for _, w := range plan.Warnings {
		found = found || strings.Contains(w.Hint, "next start")
	}
	if !found {
		t.Errorf("no warning that delegation reaches the running manager at its next start: %+v", plan.Warnings)
	}
}

// The drop-in is written before linger is enabled, since enabling linger
// starts the user's manager at once.
func TestDelegationComesBeforeLinger(t *testing.T) {
	plan, err := hostadmit.Build(current(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	delegate, linger := -1, -1
	for i, s := range plan.Steps {
		if s.File != nil && strings.HasSuffix(s.File.Path, "delegate.conf") {
			delegate = i
		}
		if strings.Contains(s.Command, "enable-linger") {
			linger = i
		}
	}
	if delegate < 0 || linger < 0 || delegate > linger {
		t.Errorf("delegate.conf at step %d, linger at step %d", delegate, linger)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/hostadmit/ -run 'TestUserPlans|TestAPrivileged|TestARunningManager|TestDelegation'`
Expected: FAIL, `undefined: hostadmit.PrivilegedGroups`, then no user steps planned.

- [ ] **Step 3: Implement**

Create `internal/hostadmit/user.go`:

```go
package hostadmit

import (
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// PrivilegedGroups are the groups a deployment's user must not be in: each
// reaches past the user, to root (sudo, wheel, admin, lxd), to every user's
// logs (adm, systemd-journal), or to the owner's system Docker daemon,
// which is root on the host (docker).
var PrivilegedGroups = []string{"root", "sudo", "wheel", "admin", "adm", "docker", "lxd", "systemd-journal"}

// minSubIDs is the subordinate range a rootless daemon needs (Docker's
// rootless page: "at least 65,536").
const minSubIDs = 65536

// asUser is command run as the deployment's user, with an environment of
// its own and nothing of root's: its home, its runtime directory and bus,
// and its own Docker socket, so a DOCKER_HOST root has set never reaches
// it. Everything admit reads or writes in the user's home goes through it,
// so a link the user planted cannot redirect root.
func asUser(p *planner, command string) string {
	u := p.f.User
	home := u.Home
	if home == "" {
		home = "/home/" + p.in.Request.User
	}
	uid := uidOf(p)
	return "runuser -u " + p.in.Request.User + " -- env -i HOME=" + apply.ShellQuote(home) +
		" PATH=/usr/bin:/bin:/usr/sbin:/sbin XDG_RUNTIME_DIR=/run/user/" + uid +
		" DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/" + uid + "/bus" +
		" DOCKER_HOST=unix:///run/user/" + uid + "/docker.sock sh -c " + apply.ShellQuote(command)
}

// rangeOf is the user's subordinate UID range as nft writes one,
// first-last, or a placeholder while the user does not exist.
func rangeOf(p *planner) string {
	r := p.f.SubUID[p.in.Request.User]
	if !p.f.User.Exists || len(r) != 1 {
		return "<subordinate range>"
	}
	return fmt.Sprintf("%d-%d", r[0].Start, r[0].Start+r[0].Count-1)
}

// planUser creates the user, or checks the one that exists: never root,
// in no privileged group, with one subordinate range of each kind, at least
// 65,536 long and overlapping no other user's, since the egress table
// trusts the whole range as the user's.
func planUser(p *planner) error {
	req := p.in.Request
	if prob := config.DeploymentUserProblem(req.User, req.Deployment); prob != "" {
		return fmt.Errorf("user %s", prob)
	}
	u := p.f.User
	if !u.Exists {
		p.step(Step{
			Title:    "create user " + req.User,
			Describe: "user: create " + req.User + " with a home, whose subordinate ID ranges useradd allocates",
			Command:  "useradd --create-home --shell /bin/bash " + req.User,
			Kinds:    []string{"user"},
		})
		return nil
	}
	if u.UID == 0 {
		return fmt.Errorf("%s has uid 0, which is root, and a deployment's user is never root. Nothing was changed", req.User)
	}
	var privileged []string
	for _, g := range append([]string{u.Group}, u.Groups...) {
		if slices.Contains(PrivilegedGroups, g) && !slices.Contains(privileged, g) {
			privileged = append(privileged, g)
		}
	}
	if len(privileged) > 0 {
		return fmt.Errorf("%s is in %s, which reaches past the user (docker is your system daemon, which is root; sudo, wheel, admin and lxd reach root; adm and systemd-journal read every user's logs). Remove it from them (gpasswd -d %s <group>), or name another user in the request, and admit again. Nothing was changed", req.User, strings.Join(privileged, ", "), req.User)
	}
	p.present(fmt.Sprintf("user: %s exists, uid %d, in no privileged group", req.User, u.UID))
	if req.User != p.d.Prefix() {
		p.warn("user: "+req.User+" already exists and is not "+p.d.Prefix(),
			"admit adds the operators' keys to "+req.User+"'s authorized_keys and runs the deployment as "+req.User+": check that this account is meant for the deployment and nothing else.")
	}
	for _, kind := range []struct {
		name   string
		ranges map[string][]IDRange
		flag   string
	}{{"subuid", p.f.SubUID, "--add-subuids"}, {"subgid", p.f.SubGID, "--add-subgids"}} {
		mine := kind.ranges[req.User]
		free := freeRange(kind.ranges)
		fix := fmt.Sprintf("usermod %s %d-%d %s", kind.flag, free, free+minSubIDs-1, req.User)
		switch {
		case len(mine) == 0:
			return fmt.Errorf("%s has no range in /etc/%s, and its rootless daemon needs one of at least %d. Give it one (%s, a range no other user holds) and admit again. Nothing was changed", req.User, kind.name, minSubIDs, fix)
		case len(mine) > 1 || mine[0].Count < minSubIDs:
			return fmt.Errorf("%s needs one range of at least %d in /etc/%s, and has %v. Remove its lines there and give it one (%s), then admit again. Nothing was changed", req.User, minSubIDs, kind.name, mine, fix)
		}
		for name, rs := range kind.ranges {
			for _, r := range rs {
				if name != req.User && r.Start < mine[0].Start+mine[0].Count && mine[0].Start < r.Start+r.Count {
					return fmt.Errorf("%s's range in /etc/%s overlaps %s's (%d-%d), so the egress table, which trusts the whole range as %s's, would trust %s's processes too. Give %s a range of its own (%s) and admit again. Nothing was changed", req.User, kind.name, name, r.Start, r.Start+r.Count-1, req.User, name, req.User, fix)
				}
			}
		}
		p.present(fmt.Sprintf("user: %s holds %d-%d in /etc/%s", req.User, mine[0].Start, mine[0].Start+mine[0].Count-1, kind.name))
	}
	return nil
}

// freeRange is the first ID above every range listed, for a fix to name.
func freeRange(ranges map[string][]IDRange) int64 {
	next := int64(100000)
	for _, rs := range ranges {
		for _, r := range rs {
			if end := r.Start + r.Count; end > next {
				next = end
			}
		}
	}
	return next
}

// delegateConf is the drop-in that lets every user's manager hand cpuset
// and io, besides what Ubuntu delegates already, to the containers below
// it (spec, Observed R5). It is host wide, shared by every admitted
// deployment.
const delegateConf = "# Written by paisans host admit. Shared by every admitted deployment;\n" +
	"# paisans host evict removes it with the last of them.\n" +
	"[Service]\nDelegate=cpu cpuset io memory pids\n"

// planDelegation writes the drop-in before the user's manager first starts.
// A manager already running gets it at its next start: restarting it would
// restart the user's daemon and every container in it, so admit never does.
func planDelegation(p *planner) error {
	f := p.f.Files[delegatePath]
	switch {
	case f.Link:
		return fmt.Errorf("%s is a symbolic link, and admit writes it as root. Remove the link and admit again. Nothing was changed", delegatePath)
	case f.Present && f.Sum == sha(delegateConf):
		p.present("delegation: " + delegatePath + " delegates cpu cpuset io memory pids")
	case f.Present:
		p.plan.Foreign = append(p.plan.Foreign, "delegation: "+delegatePath)
		p.warn("delegation: "+delegatePath+" is not paisans', and is left as it is",
			"It is the host's own drop-in for every user's manager. Compose cpuset and io limits work only if it delegates cpuset and io.")
		return nil
	default:
		p.step(Step{
			Title:    "delegate cgroups to users",
			Describe: "delegation: let every user's manager delegate cpu cpuset io memory pids, for compose's limits; written before " + p.in.Request.User + "'s manager first starts",
			File:     &hostprep.File{Path: delegatePath, Content: delegateConf, Mode: 0o644},
			Command:  "systemctl daemon-reload",
			Kinds:    []string{"user"},
		})
	}
	if p.f.User.ManagerActive && !(f.Present && f.Sum == sha(delegateConf)) {
		p.warn("delegation reaches "+p.in.Request.User+"'s manager at its next start",
			"Its manager is running, and restarting it would restart its Docker daemon and every container in it, so admit does not. cpuset and io limits apply after the host's next reboot.")
	}
	return nil
}

// planLinger enables linger, so the user's manager, its daemon and its
// timers run with no login and start at boot, then starts the manager if
// it is not running. start never restarts a running unit.
func planLinger(p *planner) error {
	u, user := p.f.User, p.in.Request.User
	if u.Exists && u.Linger {
		p.present("linger: on for " + user)
	} else {
		p.step(Step{Title: "enable linger for " + user, Describe: "linger: run " + user + "'s manager, its Docker daemon and its timers without a login, and at boot", Command: "loginctl enable-linger " + user, Kinds: []string{"user"}})
	}
	if u.Exists && u.ManagerActive {
		p.present("linger: " + user + "'s manager is running")
		return nil
	}
	p.step(Step{
		Title:       "start " + user + "'s manager",
		Describe:    "linger: start user@" + uidOf(p) + ".service and wait for it, which does nothing to a manager already running",
		Command:     "systemctl start user@" + uidOf(p) + ".service",
		Kinds:       []string{"user"},
		Provisional: !u.Exists,
	})
	return nil
}
```

Add `sha(content string) string` (hex SHA-256) to `facts.go` beside the probe, and import `hostprep` in `user.go`.

In `parts`, after `apparmor`: `{"user", planUser}, {"delegation", planDelegation}, {"linger", planLinger}`.

- [ ] **Step 4: Run, regenerate and read**

Run: `go test ./internal/hostadmit/ -update && go test ./internal/hostadmit/`
Expected: PASS. `git diff internal/hostadmit/testdata`: `docker-blank` and `docker-current` gain `create user paisans-f2a9`, `delegate cgroups to users`, `enable linger` and a provisional `start paisans-f2a9's manager` with `user@<uid>.service`; nothing else changed. `user-existing-running` has no user step and the delegation warning; `user-not-paisans` warns that `community` is not `paisans-f2a9`; the two `-refused` files name the fix with a free range.

- [ ] **Step 5: Commit**

```bash
git add internal/hostadmit
git commit -m "feat: plan the deployment's user, its delegation and linger

The user is the whole boundary, so admit refuses one that is root or in
any group that reaches past it, docker above all, and one whose
subordinate range is missing, short, or shared with another user, since
the egress table trusts that range as the user's. The cgroup drop-in goes
in before linger starts the user's manager; a manager already running is
never restarted, because that restarts every container in it.

Nothing runs admit yet, so no changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: The egress table, its unit and `--mesh-lan`

**Files:**
- Create: `internal/hostprep/snippets/egress.nft.tmpl`, `internal/hostprep/snippets/paisans-egress.service.tmpl`
- Modify: `internal/hostprep/admit.go`
- Create: `internal/hostadmit/egress.go`
- Modify: `internal/hostadmit/plan.go` (`parts`), `internal/admission/record.go`, `internal/admission/record_test.go`, `internal/admission/testdata/admitted.f2a9.json`
- Test: `internal/hostprep/egress_test.go`, `internal/hostadmit/egress_test.go`, `internal/hostadmit/testdata/plans/egress-*.txt`

**Interfaces:**
- Consumes: Task 2's `FwMark`, `EgressTable`, `EgressUnit`; Task 3's `egressFilePath`, `Facts.Files`, `Facts.EgressEnabled`, `Facts.EgressTable`, `Facts.LAN`; Task 5's `uidOf`, `rangeOf`; `/etc/resolv.conf` through `p.t.ReadFile`.
- Produces: `hostprep.EgressData{ Token, Table, Mark, Users, Mesh, LAN string; Resolvers4, Resolvers6 []string }`, `hostprep.EgressTable(EgressData) (string, error)`, `hostprep.EgressUnitData{ Token, User, Table, File, UID string }`, `hostprep.EgressUnit(EgressUnitData) (string, error)`; part `{"egress", planEgress}` between `delegation` and `linger`; the record's new field `MeshLAN string` (`json:"mesh_lan"`), which phase 4's `Record` lacks and Step 3 adds, set to the prefix `--mesh-lan` accepts.

- [ ] **Step 1: Write the failing tests**

Create `internal/hostprep/egress_test.go`:

```go
package hostprep_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

var egress = hostprep.EgressData{
	Token: "f2a9", Table: "paisans_f2a9", Mark: "0x5053f2a9",
	Users: "1001, 165536-231071", Mesh: "10.44.0.0/24",
	Resolvers4: []string{"127.0.0.53"},
}

// The table matches the user's UID and its whole subordinate range in its
// UID chain, since a container's users map into that range, and the mesh's
// mark in its mark chain, which drops the host's own addresses before any
// --mesh-lan accept.
func TestEgressTableRendersTheUsersRangeAndTheMark(t *testing.T) {
	text, err := hostprep.EgressTable(egress)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"table inet paisans_f2a9 {",
		"meta skuid { 1001, 165536-231071 } ip daddr 10.44.0.0/24 accept",
		"meta skuid { 1001, 165536-231071 } ip daddr { 127.0.0.53 } meta l4proto { tcp, udp } th dport 53 accept",
		"meta skuid { 1001, 165536-231071 } ip daddr { 127.0.0.0/8, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 100.64.0.0/10 } drop",
		"meta skuid { 1001, 165536-231071 } ip6 daddr { ::1, fc00::/7, fe80::/10 } drop",
		"meta mark 0x5053f2a9 fib daddr type { local, broadcast, multicast } drop",
		"meta mark 0x5053f2a9 ip6 daddr { ::1, fe80::/10, ff00::/8, fc00::/7 } drop",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the table lacks %q:\n%s", want, text)
		}
	}
	lan := egress
	lan.LAN = "192.168.1.0/24"
	text, _ = hostprep.EgressTable(lan)
	local := strings.Index(text, "fib daddr type")
	accept := strings.Index(text, "ip daddr 192.168.1.0/24 accept")
	private := strings.Index(text, "meta mark 0x5053f2a9 ip daddr {")
	if local < 0 || accept < local || private < accept {
		t.Errorf("with --mesh-lan, the host's own addresses are not dropped first, then the LAN accepted, then the private ranges dropped:\n%s", text)
	}
}

// The unit loads the table before the user's manager, and is required by
// it, so a table that fails to load keeps every container from starting.
func TestEgressUnitIsRequiredByTheUsersManager(t *testing.T) {
	text, err := hostprep.EgressUnit(hostprep.EgressUnitData{Token: "f2a9", User: "paisans-f2a9", Table: "paisans_f2a9", File: "/etc/paisans/egress.f2a9.nft", UID: "1001"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Before=network-pre.target user@1001.service", "After=nftables.service", "RequiredBy=user@1001.service", "ExecStart=/usr/sbin/nft -f /etc/paisans/egress.f2a9.nft", "ExecReload=/usr/sbin/nft -f /etc/paisans/egress.f2a9.nft"} {
		if !strings.Contains(text, want) {
			t.Errorf("the unit lacks %q:\n%s", want, text)
		}
	}
}

// Where nft is installed and the test runs as root, the table parses.
func TestEgressTableParses(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("nft -c needs root")
	}
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("no nft")
	}
	text, _ := hostprep.EgressTable(egress)
	path := filepath.Join(t.TempDir(), "egress.nft")
	os.WriteFile(path, []byte(text), 0o644)
	if out, err := exec.Command("nft", "-c", "-f", path).CombinedOutput(); err != nil {
		t.Fatalf("nft -c: %v\n%s", err, out)
	}
}
```

Create `internal/hostadmit/egress_test.go`:

```go
package hostadmit_test

import "github.com/paisans-software/paisans-cli/internal/hostadmit"

// home is a host on a home LAN, as one admitted with --mesh-lan is.
func home() *fakeHost { return current().withUser().with("lan", "192.168.1.20/24") }

var egressCases = addCases(
	goldenCase{name: "egress-fresh-user", host: current, site: "vm"},
	goldenCase{name: "egress-without-mesh-lan", host: home, site: "vm"},
	goldenCase{name: "egress-with-mesh-lan", host: home, site: "vm", flags: hostadmit.Flags{MeshLAN: true}},
	goldenCase{name: "egress-mesh-lan-public-refused", site: "vm", flags: hostadmit.Flags{MeshLAN: true}, host: func() *fakeHost { return current().withUser() }},
	goldenCase{name: "egress-lan-resolver", site: "vm", host: func() *fakeHost {
		return home().file("/etc/resolv.conf", "nameserver 192.168.1.1\nnameserver fd00::1\n")
	}},
)

func TestEgressPlans(t *testing.T) { runGolden(t, egressCases) }
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/hostprep/ -run Egress; go test ./internal/hostadmit/ -run TestEgressPlans`
Expected: FAIL to compile, `undefined: hostprep.EgressData`.

- [ ] **Step 3: The templates and the record's field**

Create `internal/hostprep/snippets/egress.nft.tmpl`:

```
{{- /* The egress table host admit loads for one deployment (spec, Egress).
        The first two lines make the load atomic: the table is created if
        absent, deleted, and defined again in one transaction, so a reload
        never leaves the user unfiltered. */ -}}
# Written by paisans host admit for paisans-{{ .Token }}. Keeps the
# deployment's user, its containers and its mesh off this host's loopback
# and the owner's network. paisans host evict removes it.
table inet {{ .Table }}
delete table inet {{ .Table }}
table inet {{ .Table }} {
	# What the user and its containers send: a container's users map into
	# the user's subordinate range, so the UID alone would miss them.
	chain output {
		type filter hook output priority 0; policy accept;
		meta skuid { {{ .Users }} } ip daddr {{ .Mesh }} accept
{{- if .Resolvers4 }}
		meta skuid { {{ .Users }} } ip daddr { {{ join .Resolvers4 ", " }} } meta l4proto { tcp, udp } th dport 53 accept
{{- end }}
{{- if .Resolvers6 }}
		meta skuid { {{ .Users }} } ip6 daddr { {{ join .Resolvers6 ", " }} } meta l4proto { tcp, udp } th dport 53 accept
{{- end }}
		meta skuid { {{ .Users }} } ip daddr { 127.0.0.0/8, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 100.64.0.0/10 } drop
		meta skuid { {{ .Users }} } ip6 daddr { ::1, fc00::/7, fe80::/10 } drop
	}
	# What the mesh's own socket sends, which no UID matches: the kernel
	# sends it, to each Endpoint the user wrote, resolved as root.
	chain mesh {
		type filter hook output priority 0; policy accept;
		meta mark {{ .Mark }} fib daddr type { local, broadcast, multicast } drop
{{- if .LAN }}
		meta mark {{ .Mark }} ip daddr {{ .LAN }} accept
{{- end }}
		meta mark {{ .Mark }} ip daddr { 127.0.0.0/8, 169.254.0.0/16, 224.0.0.0/4, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10 } drop
		meta mark {{ .Mark }} ip6 daddr { ::1, fe80::/10, ff00::/8, fc00::/7 } drop
	}
}
```

(`join` is `strings.Join`, given to the template with `Funcs`; `snippet` in `hostprep.go` parses without functions, so add `snippetFuncs(path string, funcs template.FuncMap, data any)` beside it, or a `funcs` map every snippet gets.)

Create `internal/hostprep/snippets/paisans-egress.service.tmpl`:

```
# Written by paisans host admit. Loads the table that keeps {{ .User }} off
# this host's loopback and the owner's network, before {{ .User }}'s
# manager, and so its Docker daemon and containers, can start: the manager
# requires this unit, so a table that fails to load keeps them stopped.
[Unit]
Description=paisans {{ .Token }}: egress table for {{ .User }}
DefaultDependencies=no
After=nftables.service
Wants=network-pre.target
Before=network-pre.target user@{{ .UID }}.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/nft -f {{ .File }}
ExecReload=/usr/sbin/nft -f {{ .File }}
ExecStop=/usr/sbin/nft delete table inet {{ .Table }}

[Install]
WantedBy=multi-user.target
RequiredBy=user@{{ .UID }}.service
```

`After=nftables.service`: that unit, where enabled, starts with `flush ruleset`, which would remove the table were it loaded first.

In `internal/hostprep/admit.go`:

```go
// EgressData is what the egress table is rendered with: the deployment's
// token and table, the mesh's mark as nft writes it, the user's UID and
// subordinate range as an nft set's elements ("1001, 165536-231071"), the
// mesh subnet, the host's resolvers by family, and with --mesh-lan the
// host's own LAN prefix.
type EgressData struct {
	Token, Table, Mark, Users, Mesh, LAN string
	Resolvers4, Resolvers6               []string
}

// EgressTable is the nftables file host admit loads for a deployment.
func EgressTable(d EgressData) (string, error) {
	return snippetFuncs("snippets/egress.nft.tmpl", template.FuncMap{"join": strings.Join}, d)
}

// EgressUnitData is what the egress unit is rendered with.
type EgressUnitData struct{ Token, User, Table, File, UID string }

// EgressUnit is the system unit that loads the egress table at boot.
func EgressUnit(d EgressUnitData) (string, error) {
	return snippet("snippets/paisans-egress.service.tmpl", d)
}
```

The record needs `--mesh-lan` (index: "recorded in the admission record"), which phase 4's `Record` lacks. In `internal/admission/record.go`, add after `BridgeNetfilter`:

```go
	// MeshLAN is the host's LAN prefix the owner let the mesh dial with
	// --mesh-lan, "" without it.
	MeshLAN string `json:"mesh_lan"`
```

add `"mesh_lan"` to `recordFields` after `"bridge_netfilter"`, refuse in `ParseRecord` a `mesh_lan` that is not empty or a canonical IPv4 prefix inside 10/8, 172.16/12 or 192.168/16, regenerate `internal/admission/testdata/admitted.f2a9.json` with `go test ./internal/admission/ -update`, and add a `TestParseRecordRefuses` row for `"mesh_lan": "8.8.8.0/24"`. The record format has not been released, so `RecordVersion` stays 1.

- [ ] **Step 4: The part**

Create `internal/hostadmit/egress.go`:

```go
package hostadmit

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

// planEgress writes the egress table and its unit and loads it, before
// linger starts the user's manager, so no container of the user's ever
// runs unfiltered. A changed table is reloaded in place, atomically, never
// by restarting the unit, whose stop deletes the table.
func planEgress(p *planner) error {
	lan := ""
	if p.in.Flags.MeshLAN {
		var err error
		if lan, err = meshLAN(p.f.DefaultDev, p.f.LAN); err != nil {
			return err
		}
		p.warn("--mesh-lan: the deployment can send UDP to "+lan, "Its sites may then dial each other on your LAN, and the deployment's user, which writes each peer's endpoint, can send WireGuard packets to any address in "+lan+".")
	}
	p.record.MeshLAN = lan
	resolv, _, err := p.t.ReadFile("/etc/resolv.conf")
	if err != nil {
		return err
	}
	var v4, v6 []string
	for _, line := range strings.Split(resolv, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "nameserver" {
			continue
		}
		a, err := netip.ParseAddr(f[1])
		switch {
		case err != nil:
			continue
		case a.Is4():
			v4 = append(v4, a.String())
		default:
			v6 = append(v6, a.String())
		}
	}
	if len(v4)+len(v6) == 0 {
		p.warn("egress: /etc/resolv.conf names no resolver", "The deployment's containers will not resolve names until one is set and admit is run again.")
	}
	provisional := !p.f.User.Exists
	table, err := hostprep.EgressTable(hostprep.EgressData{
		Token: p.d.Token(), Table: p.d.EgressTable(), Mark: fmt.Sprintf("%#08x", p.d.FwMark()),
		Users: uidOf(p) + ", " + rangeOf(p), Mesh: p.in.Request.MeshSubnet, LAN: lan, Resolvers4: v4, Resolvers6: v6,
	})
	if err != nil {
		return err
	}
	file, unitPath := egressFilePath(p.d), "/etc/systemd/system/"+p.d.EgressUnit()
	unit, err := hostprep.EgressUnit(hostprep.EgressUnitData{Token: p.d.Token(), User: p.in.Request.User, Table: p.d.EgressTable(), File: file, UID: uidOf(p)})
	if err != nil {
		return err
	}
	for _, path := range []string{file, unitPath} {
		if p.f.Files[path].Link {
			return fmt.Errorf("%s is a symbolic link, and admit writes it as root. Remove the link and admit again. Nothing was changed", path)
		}
	}
	tableChanged := p.f.Files[file].Sum != sha(table) || p.f.Files[file].Mode != "644"
	unitChanged := p.f.Files[unitPath].Sum != sha(unit) || p.f.Files[unitPath].Mode != "644"
	if tableChanged {
		p.step(Step{Title: "write the egress table", Describe: "egress: keep " + p.in.Request.User + ", its containers and its mesh off this host's loopback and your network (" + file + ")",
			File: &hostprep.File{Path: file, Content: table, Mode: 0o644}, Command: "nft -c -f " + file, Kinds: []string{"egress"}, Provisional: provisional})
	} else {
		p.present("egress: " + file + " as written")
	}
	if unitChanged || !p.f.EgressEnabled {
		p.step(Step{Title: "install the egress unit", Describe: "egress: load the table at boot, before " + p.in.Request.User + "'s manager, which requires it (" + p.d.EgressUnit() + ")",
			File: &hostprep.File{Path: unitPath, Content: unit, Mode: 0o644}, Command: "systemctl daemon-reload && systemctl enable " + p.d.EgressUnit(), Kinds: []string{"egress"}, Provisional: provisional})
	} else {
		p.present("egress: " + p.d.EgressUnit() + " enabled")
	}
	switch {
	case !p.f.EgressTable:
		p.step(Step{Title: "load the egress table", Describe: "egress: load " + p.d.EgressTable() + " now", Command: "systemctl start " + p.d.EgressUnit(), Kinds: []string{"egress"}, Provisional: provisional})
	case tableChanged:
		p.step(Step{Title: "reload the egress table", Describe: "egress: replace " + p.d.EgressTable() + " in one transaction", Command: "systemctl reload " + p.d.EgressUnit(), Kinds: []string{"egress"}, Provisional: provisional})
	default:
		p.present("egress: " + p.d.EgressTable() + " loaded")
	}
	return nil
}

// meshLAN is the private prefix of the host's own LAN: the network of the
// first global IPv4 address on the default route's interface, refused
// unless it is private and no wider than a /16, so --mesh-lan never opens
// more than the owner's own network.
func meshLAN(dev, addr string) (string, error) {
	if dev == "" || dev == "absent" || addr == "" {
		return "", fmt.Errorf("--mesh-lan: this host has no default route with an IPv4 address, so there is no LAN to let the mesh dial. Drop --mesh-lan. Nothing was changed")
	}
	pfx, err := netip.ParsePrefix(addr)
	if err != nil {
		return "", fmt.Errorf("--mesh-lan: %s's address %q is not a prefix", dev, addr)
	}
	pfx = pfx.Masked()
	private := false
	for _, r := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		if netip.MustParsePrefix(r).Contains(pfx.Addr()) && pfx.Bits() >= netip.MustParsePrefix(r).Bits() {
			private = true
		}
	}
	if !private || pfx.Bits() < 16 {
		return "", fmt.Errorf("--mesh-lan: %s is on %s, which is not a private network of a /16 or narrower, so there is no LAN of yours for the mesh to dial. Drop --mesh-lan. Nothing was changed", dev, pfx)
	}
	return pfx.String(), nil
}
```

In `parts`, insert `{"egress", planEgress}` between `delegation` and `linger`. In `plan.go`'s `Build`, after `NewRecord`, nothing else: `p.record.MeshLAN` is set here.

- [ ] **Step 5: Run, regenerate and read**

Run: `go test ./internal/admission/ -update && go test ./internal/hostprep/ ./internal/admission/ && go test ./internal/hostadmit/ -update && go test ./internal/hostadmit/`
Expected: PASS. In the diff, every earlier plan gains the three egress steps after delegation, provisional with `<uid>, <subordinate range>` on `current()` and `newBlank()` hosts, with real values after `withUser()`. `egress-with-mesh-lan` has `ip daddr 192.168.1.0/24 accept` after the `fib` drop; `egress-mesh-lan-public-refused` names `203.0.113.0/24`.

- [ ] **Step 6: Commit**

```bash
git add internal/hostprep internal/hostadmit internal/admission
git commit -m "feat: plan the egress table that keeps a deployment off the host

Every infrastructure container shares the host's network namespace, so
without a rule it reaches the owner's loopback and LAN. Admit loads an
nftables table matching the user's UID and its whole subordinate range,
and the mesh's mark, which catches what the kernel sends for WireGuard to
an endpoint the user chose. Its unit is required by the user's manager,
so a table that fails to load keeps every container stopped. --mesh-lan
accepts the host's own private LAN in the mark chain and nothing wider,
and is recorded.

Nothing runs admit yet, so no changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 7: `br_netfilter`, the rootless daemon and `--data-dir`

**Files:**
- Create: `internal/hostadmit/daemon.go`
- Modify: `internal/hostadmit/plan.go` (`parts`, `planner.previous`)
- Test: `internal/hostadmit/daemon_test.go`, `internal/hostadmit/testdata/plans/bridge-*.txt`, `daemon-*.txt`, `datadir-*.txt`

**Interfaces:**
- Consumes: Task 3's `Facts.BrNetfilter`, `Bridges`, `ForwardPolicy`, `Files[modulesLoadPath]`, `Dirs`, `Data`; Task 5's `asUser`, `uidOf`; `mesh.DefaultPools`, `mesh.ParsePrefix`, `mesh.Overlap`; `admission.Read`, `admission.ErrNotAdmitted`.
- Produces: `func (p *planner) previous() (*admission.Record, error)`, this deployment's record as the host holds it, nil when there is none, read once per Build (Tasks 10, 12 and 13 use it); `func userState(p *planner) (UserState, error)`, the user's own facts read as the user (`DaemonActive`, `UnitPresent bool`, `DaemonJSON string` as its checksum, `Containers int`, `Images`, `Volumes int`), zero while the user does not exist; `daemonJSON(p *planner) string`; parts `{"bridge netfilter", planBrNetfilter}` and `{"daemon", planDaemon}` after `linger`; `p.record.DataDir` and `p.record.BridgeNetfilter` set.

- [ ] **Step 1: Write the failing tests**

Create `internal/hostadmit/daemon_test.go`:

```go
package hostadmit_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

// userState answers the probe admit runs as the user.
const userStateMarker = "paisans host admit: as the user"

func running(h *fakeHost, containers int) *fakeHost {
	return h.answer(userStateMarker, "daemon active\nunit present\ndaemon-json "+sum("{}")+"\ncontainers "+itoa(containers)+"\nimages 0\nvolumes 0\nend\n")
}

var daemonCases = addCases(
	goldenCase{name: "bridge-loaded", site: "vm", host: func() *fakeHost { return home().with("br_netfilter", "loaded").add("bridge", "docker0") }},
	goldenCase{name: "bridge-docker-only", site: "vm", host: func() *fakeHost { return home().add("bridge", "docker0").add("bridge", "br-3f2a1c") }},
	goldenCase{name: "bridge-libvirt-stopped", site: "vm", host: func() *fakeHost { return home().add("bridge", "virbr0").with("forward-policy", "DROP") }},
	goldenCase{name: "bridge-libvirt-with-flag", site: "vm", flags: hostadmit.Flags{BridgeNetfilter: true}, host: func() *fakeHost {
		return home().add("bridge", "virbr0").with("forward-policy", "DROP")
	}},
	goldenCase{name: "datadir-none", site: "vm", host: home},
	goldenCase{name: "datadir-given", site: "home-b", flags: hostadmit.Flags{Watchdog: true, DataDir: "/srv/data"}, host: func() *fakeHost {
		return home().withDir("/srv/data", "drwxr-xr-x", 0, 0).withDir("/srv/data/paisans-f2a9", "absent", 0, 0).
			with("data-path", "/srv/data").with("data-fs", "xfs").with("data-avail", "500000000000")
	}},
	goldenCase{name: "datadir-nfs-refused", site: "vm", flags: hostadmit.Flags{DataDir: "/mnt/nas"}, host: func() *fakeHost {
		return home().withDir("/mnt/nas", "drwxr-xr-x", 0, 0).with("data-path", "/mnt/nas").with("data-fs", "nfs")
	}},
	goldenCase{name: "datadir-home-on-nfs-refused", site: "vm", host: func() *fakeHost { return home().with("data-fs", "nfs4") }},
	goldenCase{name: "datadir-short-of-space", site: "home-b", flags: hostadmit.Flags{Watchdog: true}, host: func() *fakeHost { return home().with("data-avail", "8000000000") }},
	goldenCase{name: "datadir-parent-writable-refused", site: "vm", flags: hostadmit.Flags{DataDir: "/srv/data"}, host: func() *fakeHost {
		return home().withDir("/srv/data", "drwxrwxrwt", 0, 0).with("data-path", "/srv/data")
	}},
	goldenCase{name: "daemon-running-with-containers", site: "vm", host: func() *fakeHost { return running(home(), 3) }},
	goldenCase{name: "daemon-running-idle", site: "vm", host: func() *fakeHost { return running(home(), 0) }},
)

func TestDaemonPlans(t *testing.T) { runGolden(t, daemonCases) }

// The parent --data-dir names is the owner's and must stay so: one the
// deployment's user, or anyone but root, could write would let them swap
// the directory admit hands over for a link.
func TestTheDataDirectoryParentMustBeRoots(t *testing.T) {
	for _, dir := range []struct{ mode string; uid int }{
		{"drwxrwxr-x", 0}, {"drwxr-xrwx", 0}, {"drwxr-xr-x", 1001}, {"lrwxrwxrwx", 0}, {"-rw-r--r--", 0}, {"drwxr-xr-x+", 0}, {"absent", 0},
	} {
		h := home().withDir("/srv/data", dir.mode, dir.uid, dir.uid).with("data-path", "/srv/data")
		if _, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{DataDir: "/srv/data"})); err == nil {
			t.Errorf("--data-dir under a parent %s owned by %d was planned", dir.mode, dir.uid)
		}
	}
}

// A daemon running containers is never restarted for a changed
// daemon.json; one running none is.
func TestARunningDaemonIsNotRestarted(t *testing.T) {
	for n, wantRestart := range map[int]bool{0: true, 3: false} {
		plan, err := hostadmit.Build(running(home(), n), input(t, "vm", hostadmit.Flags{}))
		if err != nil {
			t.Fatal(err)
		}
		restarted := false
		for _, s := range plan.Steps {
			restarted = restarted || strings.Contains(s.Command, "restart docker")
		}
		if restarted != wantRestart {
			t.Errorf("%d containers: restart planned %v", n, restarted)
		}
	}
}

// Moving Docker's data root under a deployment that already has data
// would hide every image, container and volume: admit refuses, naming
// both paths.
func TestChangingTheDataDirWithDataIsRefused(t *testing.T) {
	h := home().withDir("/srv/data", "drwxr-xr-x", 0, 0).with("data-path", "/srv/data").
		answer(userStateMarker, "daemon active\nunit present\ndaemon-json x\ncontainers 0\nimages 4\nvolumes 2\nend\n")
	h.file("/etc/paisans/admitted.f2a9.json", previousRecord(t, "vm", "")).withFile("/etc/paisans/admitted.f2a9.json", previousRecord(t, "vm", ""), "644", 0)
	_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{DataDir: "/srv/data"}))
	if err == nil || !strings.Contains(err.Error(), "/srv/data/paisans-f2a9") || !strings.Contains(err.Error(), "/home/paisans-f2a9") {
		t.Fatalf("Build = %v", err)
	}
}
```

with, in `fake_test.go`:

```go
func itoa(n int) string { return strconv.Itoa(n) }

// previousRecord is the record an earlier admit of site wrote, with
// dataDir, encoded as admit writes it.
func previousRecord(t *testing.T, site, dataDir string) string {
	t.Helper()
	req := request(t, site)
	rec := admission.NewRecord(req, req.Grants, "", admitted)
	rec.DataDir = dataDir
	data, err := admission.EncodeRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
```

(`strconv` joins the file's imports).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/hostadmit/ -run 'TestDaemonPlans|TestTheDataDirectory|TestARunningDaemon|TestChangingTheDataDir'`
Expected: FAIL: no golden files; every `--data-dir` parent is accepted; no restart is planned for either.

- [ ] **Step 3: Implement**

Create `internal/hostadmit/daemon.go`:

```go
package hostadmit

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/mesh"
)

// modulesLoadConf loads br_netfilter at every boot: without it the user's
// daemon will not start with its userland proxy off (spec, Observed R6).
const modulesLoadConf = "# Written by paisans host admit. The rootless Docker daemons of admitted\n" +
	"# deployments need it to start with their userland proxy off.\nbr_netfilter\n"

// dockerBridge is a bridge a Docker daemon made: docker0, or a compose
// project's br-<id>.
func dockerBridge(name string) bool { return name == "docker0" || strings.HasPrefix(name, "br-") }

// planBrNetfilter loads br_netfilter, which is host wide: once it is
// loaded, traffic crossing any of the host's bridges passes the FORWARD
// chain, which it skipped before. Already loaded, nothing changes but the
// boot entry; not loaded with only Docker's bridges, it is loaded; not
// loaded with other bridges, admit names them and the chain's policy and
// stops unless the owner passed --bridge-netfilter.
func planBrNetfilter(p *planner) error {
	p.record.BridgeNetfilter = p.in.Flags.BridgeNetfilter
	var others []string
	for _, b := range p.f.Bridges {
		if !dockerBridge(b) {
			others = append(others, b)
		}
	}
	switch {
	case p.f.BrNetfilter:
		p.present("br_netfilter: loaded already")
	case len(others) > 0 && !p.in.Flags.BridgeNetfilter:
		return fmt.Errorf("br_netfilter is not loaded, and this host has bridges of its own (%s). The deployment's daemon needs it, and once it is loaded their traffic passes the FORWARD chain, whose policy is %s, which it skips today. Check that the chain lets their traffic through, then admit again with --bridge-netfilter. Nothing was changed", strings.Join(others, ", "), policyOr(p.f.ForwardPolicy))
	default:
		if len(others) > 0 {
			p.warn("br_netfilter: "+strings.Join(others, ", ")+" pass the FORWARD chain from now on", "Its policy is "+policyOr(p.f.ForwardPolicy)+". --bridge-netfilter was given, and is recorded.")
		}
		p.step(Step{Title: "load br_netfilter", Describe: "daemon: load br_netfilter now, which the user's daemon needs with its userland proxy off", Command: "modprobe br_netfilter", Kinds: []string{"docker"}})
	}
	f := p.f.Files[modulesLoadPath]
	switch {
	case f.Link:
		return fmt.Errorf("%s is a symbolic link, and admit writes it as root. Remove the link and admit again. Nothing was changed", modulesLoadPath)
	case f.Sum == sha(modulesLoadConf) && f.Mode == "644":
		p.present("br_netfilter: loaded at boot by " + modulesLoadPath)
	default:
		p.step(Step{Title: "load br_netfilter at boot", Describe: "daemon: list br_netfilter in " + modulesLoadPath + ", shared by every admitted deployment",
			File: &hostprep.File{Path: modulesLoadPath, Content: modulesLoadConf, Mode: 0o644}, Kinds: []string{"docker"}})
	}
	return nil
}

func policyOr(p string) string {
	if p == "" {
		return "not readable"
	}
	return p
}

// UserState is what admit reads as the deployment's user, in its own home.
type UserState struct {
	DaemonActive, UnitPresent bool
	// DaemonJSON is the checksum of ~/.config/docker/daemon.json, "" when
	// there is none.
	DaemonJSON                  string
	Containers, Images, Volumes int
}

// userStateProbe runs as the user, so a link in its home is followed with
// the user's rights alone. Its first line carries the marker a test's
// fake host answers it by.
const userStateProbe = `# paisans host admit: as the user
if systemctl --user is-active -q docker.service 2>/dev/null; then echo "daemon active"; else echo "daemon inactive"; fi
if [ -f "$HOME/.config/systemd/user/docker.service" ]; then echo "unit present"; else echo "unit absent"; fi
if [ -f "$HOME/.config/docker/daemon.json" ]; then echo "daemon-json $(sha256sum < "$HOME/.config/docker/daemon.json" | cut -c1-64)"; else echo "daemon-json absent"; fi
if docker info >/dev/null 2>&1; then
  echo "containers $(docker ps -aq | wc -l)"
  echo "images $(docker images -aq | wc -l)"
  echo "volumes $(docker volume ls -q | wc -l)"
else
  echo "containers 0"; echo "images 0"; echo "volumes 0"
fi
echo end`

// userState reads UserState as the user; a user not made yet has none.
func userState(p *planner) (UserState, error) {
	var s UserState
	if !p.f.User.Exists {
		return s, nil
	}
	out, err := p.t.Run(asUser(p, userStateProbe))
	if err != nil {
		return s, fmt.Errorf("reading %s's daemon as %s: %w", p.in.Request.User, p.in.Request.User, err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || lines[len(lines)-1] != "end" {
		return s, fmt.Errorf("reading %s's daemon: the answer was cut short", p.in.Request.User)
	}
	for _, line := range lines {
		k, v, _ := strings.Cut(line, " ")
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		switch k {
		case "daemon":
			s.DaemonActive = v == "active"
		case "unit":
			s.UnitPresent = v == "present"
		case "daemon-json":
			if v != "absent" {
				s.DaemonJSON = v
			}
		case "containers":
			s.Containers = n
		case "images":
			s.Images = n
		case "volumes":
			s.Volumes = n
		}
	}
	return s, nil
}

// previous is this deployment's admission record as the host holds it,
// nil when it has none, read once per Build.
func (p *planner) previous() (*admission.Record, error) {
	if p.prev.read {
		return p.prev.rec, p.prev.err
	}
	p.prev.read = true
	rec, err := admission.Read(p.t, p.d.Token())
	switch {
	case errors.Is(err, admission.ErrNotAdmitted):
	case err != nil:
		p.prev.err = fmt.Errorf("this deployment's admission record: %w. Move it aside and admit again", err)
	default:
		p.prev.rec = &rec
	}
	return p.prev.rec, p.prev.err
}

// daemonConfig is the user's daemon.json, in a fixed field order.
type daemonConfig struct {
	UserlandProxy bool          `json:"userland-proxy"`
	Pools         []addressPool `json:"default-address-pools"`
	DataRoot      string        `json:"data-root,omitempty"`
}

type addressPool struct {
	Base string `json:"base"`
	Size int    `json:"size"`
}

// daemonJSON is the user's daemon.json: the userland proxy off, so a
// published port carries the client's own address (spec, Observed R6);
// Docker's default pools less any overlapping the mesh, each cut in /24s,
// so no network of the user's takes the mesh's range; and the data root
// --data-dir gives.
func daemonJSON(p *planner) (string, error) {
	subnet, err := mesh.ParsePrefix(p.in.Request.MeshSubnet)
	if err != nil {
		return "", err
	}
	cfg := daemonConfig{UserlandProxy: false, DataRoot: p.record.DataDir}
	for _, b := range mesh.DefaultPools {
		pool, err := mesh.ParsePrefix(b)
		if err != nil || mesh.Overlap(pool, subnet) {
			continue
		}
		cfg.Pools = append(cfg.Pools, addressPool{Base: b, Size: 24})
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out) + "\n", nil
}

// refusedFS are filesystems Docker's overlay storage cannot keep its data
// on: network filesystems (Docker's rootless tips: not on NFS), FUSE,
// another overlay, and those without the file types overlay needs.
func refusedFS(fs string) bool {
	return strings.HasPrefix(fs, "nfs") || strings.HasPrefix(fs, "fuse") ||
		slices.Contains([]string{"cifs", "smb2", "smb3", "overlayfs", "overlay", "msdos", "vfat", "exfat", "ntfs", "ntfs3"}, fs)
}

// rootsAlone reports whether a directory's ls -ldn mode and owner say it is
// a directory, not a link, owned by root, with no group or other write bit
// and no ACL.
func rootsAlone(d DirState) bool {
	m := d.Mode
	return d.Present && len(m) == 10 && m[0] == 'd' && m[5] != 'w' && m[8] != 'w' && d.UID == 0
}

// planDaemon puts the user's rootless daemon in place: the data
// directory with --data-dir, daemon.json, written as the user, then the
// setup tool, run as the user, which starts the daemon reading it.
func planDaemon(p *planner) error {
	user, flags := p.in.Request.User, p.in.Flags
	state, err := userState(p)
	if err != nil {
		return err
	}
	if flags.DataDir != "" {
		parent := p.f.Dirs[flags.DataDir]
		if !rootsAlone(parent) {
			return fmt.Errorf("--data-dir %s must be a directory, not a link, owned by root and writable by nobody else (its listing is %q, owner %d), so that no one but you can replace what admit creates in it. Fix it and admit again. Nothing was changed", flags.DataDir, parent.Mode, parent.UID)
		}
		p.record.DataDir = flags.DataDir + "/" + p.d.Prefix()
	}
	prev, err := p.previous()
	if err != nil {
		return err
	}
	if prev != nil && prev.DataDir != p.record.DataDir && state.Images+state.Containers+state.Volumes > 0 {
		was, now := prev.DataDir, p.record.DataDir
		if was == "" {
			was = p.f.User.Home + "/.local/share/docker"
		}
		if now == "" {
			now = p.f.User.Home + "/.local/share/docker"
		}
		return fmt.Errorf("%s's Docker keeps its data in %s, which holds %d images, %d containers and %d volumes, and this admit would move its data root to %s, hiding all of them. Keep the --data-dir it was admitted with, or have the operators remove the deployment's stacks first. Nothing was changed", user, was, state.Images, state.Containers, state.Volumes, now)
	}
	if refusedFS(p.f.Data.FS) {
		return fmt.Errorf("%s is on %s, which Docker's overlay storage cannot keep its data on. Give --data-dir a local filesystem (ext4, xfs or btrfs) and admit again. Nothing was changed", p.f.Data.Path, p.f.Data.FS)
	}
	floor, why := int64(5e9), "5 GB"
	if p.in.Request.Grants.Watchdog {
		floor, why = 20e9, "20 GB for a data site"
	}
	free := fmt.Sprintf("%.1f GB free", float64(p.f.Data.Avail)/1e9)
	if p.f.Data.Avail < floor {
		p.warn("daemon: "+p.f.Data.Path+" has "+free+", under "+why, "Images, layers and volumes, and on a data site the database and its WAL archive, live there. Give --data-dir a larger disk, or free space before the deployment fills it.")
	} else {
		p.present("daemon: " + p.f.Data.Path + " has " + free)
	}
	if dir := p.record.DataDir; dir != "" {
		child := p.f.Dirs[dir]
		switch {
		case !child.Present:
			p.step(Step{Title: "create " + dir, Describe: "daemon: " + user + "'s Docker data, on the disk --data-dir names",
				Command: "install -d -m 700 -o " + user + " -g " + groupOf(p) + " " + dir, Kinds: []string{"docker"}, Provisional: !p.f.User.Exists})
		case child.Mode[0] != 'd' || child.UID != p.f.User.UID:
			return fmt.Errorf("%s exists and is not a directory of %s's (%q, owner %d). Remove it, or give another --data-dir, and admit again. Nothing was changed", dir, user, child.Mode, child.UID)
		default:
			p.present("daemon: " + dir + " is " + user + "'s")
		}
	}
	content, err := daemonJSON(p)
	if err != nil {
		return err
	}
	changed := state.DaemonJSON != sha(content)
	if changed {
		p.step(Step{Title: "write " + user + "'s daemon.json", Describe: "daemon: userland proxy off, address pools clear of the mesh" + map[bool]string{true: ", data root " + p.record.DataDir}[p.record.DataDir != ""] + "; written as " + user,
			Command: asUser(p, `umask 077 && mkdir -p "$HOME/.config/docker" && cat > "$HOME/.config/docker/.daemon.json.paisans-tmp" && mv -f "$HOME/.config/docker/.daemon.json.paisans-tmp" "$HOME/.config/docker/daemon.json"`),
			Input:   content, Kinds: []string{"docker"}, Provisional: !p.f.User.Exists})
	} else {
		p.present("daemon: " + user + "'s daemon.json as written")
	}
	switch {
	case !state.UnitPresent:
		p.step(Step{Title: "install " + user + "'s rootless Docker", Describe: "daemon: dockerd-rootless-setuptool.sh install --force, as " + user + "; --force because a system daemon may run beside it",
			Command: asUser(p, "dockerd-rootless-setuptool.sh install --force"), Kinds: []string{"docker"}, Provisional: !p.f.User.Exists})
	case changed && state.DaemonActive && state.Containers > 0:
		p.warn("daemon: "+user+"'s daemon picks up its new daemon.json at its next restart", fmt.Sprintf("It runs %d containers, and admit never restarts a daemon under them. The operators restart it, or the host's next reboot does.", state.Containers))
	case changed:
		p.step(Step{Title: "restart " + user + "'s Docker", Describe: "daemon: restart the daemon, which runs no container, to read its new daemon.json",
			Command: asUser(p, "systemctl --user restart docker.service"), Kinds: []string{"docker"}})
	default:
		p.present("daemon: " + user + "'s rootless Docker is installed")
	}
	return nil
}

// groupOf is the user's primary group, its own name for a user not made
// yet (useradd's default on Debian and Ubuntu); a step using it then is
// provisional, and rebuilt with the group read.
func groupOf(p *planner) string {
	if p.f.User.Exists {
		return p.f.User.Group
	}
	return p.in.Request.User
}
```

Add to `planner` in `plan.go`:

```go
	// prev caches previous(): this deployment's record as the host holds it.
	prev struct {
		read bool
		rec  *admission.Record
		err  error
	}
```

and to `parts`, after `linger`: `{"bridge netfilter", planBrNetfilter}, {"daemon", planDaemon}`.

`map[bool]string{...}[cond]` above is shorthand for the listing; write it as an `if` when you implement it.

- [ ] **Step 4: Run, regenerate and read**

Run: `go test ./internal/hostadmit/ -update && go test ./internal/hostadmit/`
Expected: PASS. Every plan gains `load br_netfilter` (unless loaded), `load br_netfilter at boot`, `write paisans-f2a9's daemon.json` (stdin: the JSON with `"userland-proxy": false` and pools without the one overlapping `10.44.0.0/24`, none here) and `install paisans-f2a9's rootless Docker`, all through `runuser -u paisans-f2a9 -- env -i ...`; `bridge-libvirt-stopped` refuses naming `virbr0` and `DROP`; `datadir-given` creates `/srv/data/paisans-f2a9` mode 700 and its daemon.json carries `"data-root"`; `datadir-short-of-space` warns at 8.0 GB under 20 GB.

- [ ] **Step 5: Commit**

```bash
git add internal/hostadmit
git commit -m "feat: plan the user's rootless daemon and where its data lives

The daemon will not start with its userland proxy off until br_netfilter
is loaded, and loading it makes every bridge on the host pass the FORWARD
chain, so admit stops on a host with bridges of its own until the owner
passes --bridge-netfilter. daemon.json and the setup tool run as the
user. --data-dir puts the data on a disk the owner chooses, under a
parent only root can write; a filesystem overlay cannot use is refused,
low space is warned about, and the data root never moves under data that
exists. A daemon running containers is never restarted.

Nothing runs admit yet, so no changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: The deployment's directory, `/etc/wireguard`, the helper and its sudoers line

**Files:**
- Create: `internal/hostadmit/wireguard.go`
- Modify: `internal/hostadmit/plan.go` (`parts`)
- Test: `internal/hostadmit/wireguard_test.go`, `internal/hostadmit/testdata/plans/srv-*.txt`, `wgdir-*.txt`, `helper-*.txt`

**Interfaces:**
- Consumes: Task 2's helper (`hostprep.WGHelperScript(d, listenPort, subnet)`), `apply.WGHelper{Iface}.Path()`, `apply.HelperDir`; Task 3's `sudoersPath`, `Facts.Dirs`, `Facts.Files`; Task 7's `rootsAlone`, `groupOf`.
- Produces: `sudoersLine(d, user string) string`; parts `{"deployment directory", planDeploymentDir}`, `{"mesh subnet", planMeshSubnet}` and `{"wireguard", planWireGuard}` after `daemon`.

- [ ] **Step 1: Write the failing tests**

Create `internal/hostadmit/wireguard_test.go`:

```go
package hostadmit_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

var wireguardCases = addCases(
	goldenCase{name: "wgdir-absent", site: "vm", host: home},
	goldenCase{name: "wgdir-correct", site: "vm", host: func() *fakeHost { return home().withDir("/etc/wireguard", "drwx------", 0, 0) }},
	goldenCase{name: "wgdir-group-writable", site: "vm", host: func() *fakeHost { return home().withDir("/etc/wireguard", "drwxrwxr-x", 0, 1001) }},
	goldenCase{name: "wgdir-link-refused", site: "vm", host: func() *fakeHost { return home().withDir("/etc/wireguard", "lrwxrwxrwx", 0, 0) }},
	goldenCase{name: "wgdir-file-refused", site: "vm", host: func() *fakeHost { return home().withDir("/etc/wireguard", "-rw-------", 0, 0) }},
	goldenCase{name: "wgdir-acl-refused", site: "vm", host: func() *fakeHost { return home().withDir("/etc/wireguard", "drwx------+", 0, 0) }},
	goldenCase{name: "helper-dialled", site: "home-a", flags: hostadmit.Flags{Watchdog: true, PublicPorts: true}, host: home},
	goldenCase{name: "helper-no-listen-port", site: "store", host: home},
	goldenCase{name: "helper-changed", site: "vm", host: func() *fakeHost { return helperInstalled(home(), "vm").withFile(helperPath, "#!/bin/sh\nexec sh\n", "755", 0) }},
	goldenCase{name: "sudoers-changed", site: "vm", host: func() *fakeHost {
		return helperInstalled(home(), "vm").withFile("/etc/sudoers.d/paisans-f2a9", "paisans-f2a9 ALL=(ALL) NOPASSWD: ALL\n", "440", 0)
	}},
	goldenCase{name: "srv-root-owned-refused", site: "vm", host: func() *fakeHost {
		return home().withDir("/srv/paisans", "drwxr-xr-x", 0, 0).withDir("/srv/paisans/f2a9", "drwxr-xr-x", 0, 0)
	}},
	goldenCase{name: "mesh-subnet-taken-refused", site: "vm", host: func() *fakeHost {
		return home().answer("ip -j route", `[{"dst":"10.44.0.0/16","dev":"tailscale0"}]`)
	}},
)

const helperPath = "/usr/local/libexec/paisans/wg-psns-f2a9"

// helperInstalled is h with the helper and sudoers line for site as admit
// writes them, so a case can change one.
func helperInstalled(h *fakeHost, site string) *fakeHost {
	req := requestFor(site)
	d := deployment.Deployment{ID: req.Deployment}
	script, _ := hostprep.WGHelperScript(d, req.ListenPort, req.MeshSubnet)
	return h.withDir(apply.HelperDir, "drwxr-xr-x", 0, 0).withFile(helperPath, script, "755", 0).
		withFile("/etc/sudoers.d/paisans-f2a9", "paisans-f2a9 ALL=(root) NOPASSWD: "+helperPath+"\n", "440", 0).
		withDir("/etc/wireguard", "drwx------", 0, 0)
}

func TestWireGuardPlans(t *testing.T) { runGolden(t, wireguardCases) }

// The sudoers line reaches sudo only once visudo has checked it, under a
// name sudo's includedir skips until then.
func TestTheSudoersLineIsCheckedBeforeItIsInPlace(t *testing.T) {
	plan, err := hostadmit.Build(home(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if s.File == nil || !strings.HasPrefix(s.File.Path, "/etc/sudoers.d/") {
			continue
		}
		if s.File.Path != "/etc/sudoers.d/paisans-f2a9.new" || s.File.Mode != 0o440 {
			t.Errorf("the sudoers line is written to %s mode %o", s.File.Path, s.File.Mode)
		}
		if !strings.HasPrefix(s.Command, "visudo -cqf /etc/sudoers.d/paisans-f2a9.new && ") || !strings.Contains(s.Command, "mv -f /etc/sudoers.d/paisans-f2a9.new /etc/sudoers.d/paisans-f2a9") {
			t.Errorf("the line is not checked before it is moved: %s", s.Command)
		}
		if s.File.Content != "paisans-f2a9 ALL=(root) NOPASSWD: "+helperPath+"\n" {
			t.Errorf("the line is %q", s.File.Content)
		}
		return
	}
	t.Error("no sudoers step")
}
```

`requestFor(site)` is `request` without a `*testing.T`, for builders: add it to `fake_test.go`, panicking on an error, since the fixture always builds.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/hostadmit/ -run 'TestWireGuardPlans|TestTheSudoersLine'`
Expected: FAIL: no golden files; no sudoers step.

- [ ] **Step 3: Implement**

Create `internal/hostadmit/wireguard.go`:

```go
package hostadmit

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/mesh"
)

// planDeploymentDir creates /srv/paisans, root's and 755, and the
// deployment's directory in it, the user's and 750. A deployment directory
// that exists and is not the user's is an earlier deployment run as root,
// which is redeployed, not converted.
func planDeploymentDir(p *planner) error {
	base, root, user := p.f.Dirs[deployment.Base], p.f.Dirs[p.d.Root()], p.in.Request.User
	switch {
	case !base.Present:
		p.step(Step{Title: "create " + deployment.Base, Describe: "directory: every deployment's directory lives in it, root's, mode 755", Command: "install -d -m 755 -o root -g root " + deployment.Base, Kinds: []string{"directory"}})
	case !rootsAlone(base):
		return fmt.Errorf("%s is %q, owner %d: it must be a directory owned by root that only root may write. Fix it and admit again. Nothing was changed", deployment.Base, base.Mode, base.UID)
	}
	switch {
	case !root.Present:
		p.step(Step{Title: "create " + p.d.Root(), Describe: "directory: the deployment's own, " + user + "'s, mode 750",
			Command: "install -d -m 750 -o " + user + " -g " + groupOf(p) + " " + p.d.Root(), Kinds: []string{"directory"}, Provisional: !p.f.User.Exists})
	case root.Mode[0] != 'd' || !p.f.User.Exists || root.UID != p.f.User.UID:
		return fmt.Errorf("%s exists and is not a directory of %s's (%q, owner %d). It is an earlier deployment run as root, which is redeployed rather than taken over: remove it with paisans site remove, or by hand once nothing in it is wanted, and admit again. Nothing was changed", p.d.Root(), user, root.Mode, root.UID)
	default:
		p.present("directory: " + p.d.Root() + " is " + user + "'s")
	}
	return nil
}

// planMeshSubnet refuses a mesh subnet that overlaps a route, an address,
// a Docker network or a Docker pool on the host, read as root: a request
// cannot know them, and two networks on overlapping ranges route each
// other's traffic. It is the check host prepare runs (cmd/paisans
// checkMeshLive), on the same probe.
func planMeshSubnet(p *planner) error {
	subnet, err := mesh.ParsePrefix(p.in.Request.MeshSubnet)
	if err != nil {
		return err
	}
	probed, err := mesh.Probe(p.t)
	if err != nil {
		return err
	}
	taken, err := probed.Taken(p.d.Interface(), "")
	if err != nil {
		return err
	}
	if clash := mesh.Clashes(subnet, taken); len(clash) > 0 {
		var what []string
		for _, c := range clash {
			what = append(what, c.What)
		}
		return fmt.Errorf("the deployment's mesh subnet %s overlaps %s on this host, so its mesh would route that network's traffic. The operators pick another subnet (paisans init picks one clear of every host) and send a new request. Nothing was changed", p.in.Request.MeshSubnet, strings.Join(what, ", "))
	}
	p.present("mesh: nothing on this host overlaps " + p.in.Request.MeshSubnet)
	return nil
}

// sudoersLine is the one line that lets the deployment's user run its
// WireGuard helper, and nothing else, as root, with no password.
func sudoersLine(d deployment.Deployment, user string) (string, error) {
	helper, err := apply.WGHelper{Iface: d.Interface()}.Path()
	if err != nil {
		return "", err
	}
	return user + " ALL=(root) NOPASSWD: " + helper + "\n", nil
}

// planWireGuard makes /etc/wireguard root's alone, installs the helper and
// its sudoers line, and restores either when it finds it changed.
func planWireGuard(p *planner) error {
	const wg = "/etc/wireguard"
	dir := p.f.Dirs[wg]
	switch {
	case !dir.Present:
		p.step(Step{Title: "create " + wg, Describe: "wireguard: root's, mode 700, so the deployment's user cannot plant a conf the helper has not checked", Command: "install -d -m 700 -o root -g root " + wg, Kinds: []string{"wireguard"}})
	case len(dir.Mode) != 10 || dir.Mode[0] != 'd':
		what := "is not a directory"
		if dir.Mode[0] == 'l' {
			what = "is a symbolic link"
		} else if len(dir.Mode) > 10 {
			what = "carries an ACL (" + dir.Mode + ")"
		}
		return fmt.Errorf("%s %s, and admit cannot make it a directory only root may write. A conf the deployment's user could plant there would run its PostUp as root at the next up. Fix it yourself (remove the link, or the ACL with setfacl -b) and admit again. Nothing was changed", wg, what)
	case !rootsAlone(dir):
		p.step(Step{Title: "make " + wg + " root's alone", Describe: fmt.Sprintf("wireguard: it is %s, owner %d, group %d; make it root's with no group or other write bit", dir.Mode, dir.UID, dir.GID),
			Command: "chown root:root " + wg + " && chmod go-w " + wg, Kinds: []string{"wireguard"}})
	default:
		p.present("wireguard: " + wg + " is root's alone")
	}

	libexec := p.f.Dirs[apply.HelperDir]
	switch {
	case !libexec.Present:
		p.step(Step{Title: "create " + apply.HelperDir, Describe: "wireguard: where each deployment's helper lives, root's, mode 755", Command: "install -d -m 755 -o root -g root " + apply.HelperDir, Kinds: []string{"wireguard"}})
	case !rootsAlone(libexec):
		return fmt.Errorf("%s is %q, owner %d: it must be a directory owned by root that only root may write. Fix it and admit again. Nothing was changed", apply.HelperDir, libexec.Mode, libexec.UID)
	}
	helper, err := apply.WGHelper{Iface: p.d.Interface()}.Path()
	if err != nil {
		return err
	}
	script, err := hostprep.WGHelperScript(p.d, p.in.Request.ListenPort, p.in.Request.MeshSubnet)
	if err != nil {
		return err
	}
	if err := ensureRootFile(p, helper, script, 0o755, "install the WireGuard helper", "wireguard: the one command "+p.in.Request.User+" may run as root: it checks each conf before wg-quick sees it, and adds the mesh's mark"); err != nil {
		return err
	}
	line, err := sudoersLine(p.d, p.in.Request.User)
	if err != nil {
		return err
	}
	final := sudoersPath(p.d)
	if f := p.f.Files[final]; f.Present && !f.Link && f.Sum == sha(line) && f.Mode == "440" && f.UID == 0 {
		p.present("wireguard: " + final + " lets " + p.in.Request.User + " run the helper alone")
		return nil
	}
	if p.f.Files[final].Link {
		return fmt.Errorf("%s is a symbolic link. Remove it and admit again. Nothing was changed", final)
	}
	tmp := final + ".new"
	title := "install the helper's sudoers line"
	if p.f.Files[final].Present {
		title = "restore the helper's sudoers line"
	}
	p.step(Step{Title: title, Describe: "wireguard: " + final + ", mode 440, checked by visudo under a name sudo skips before it is moved into place",
		File:    &hostprep.File{Path: tmp, Content: line, Mode: 0o440},
		Command: "visudo -cqf " + tmp + " && chown root:root " + tmp + " && mv -f " + tmp + " " + final + " || { rm -f " + tmp + "; echo 'visudo refused the line; nothing reached sudo'; exit 1; }",
		Kinds:   []string{"wireguard"}})
	return nil
}

// ensureRootFile plans writing content at path, root's, with mode, unless
// it is there as written; a link is refused, and a changed file restored.
func ensureRootFile(p *planner, path, content string, mode uint32, title, describe string) error {
	f := p.f.Files[path]
	switch {
	case f.Link:
		return fmt.Errorf("%s is a symbolic link, and admit writes it as root. Remove the link and admit again. Nothing was changed", path)
	case f.Present && f.Sum == sha(content) && f.Mode == fmt.Sprintf("%o", mode) && f.UID == 0:
		p.present(describe)
		return nil
	case f.Present:
		title = "restore: " + title
		describe += fmt.Sprintf(" (found changed: mode %s, owner %d)", f.Mode, f.UID)
	}
	p.step(Step{Title: title, Describe: describe, File: &hostprep.File{Path: path, Content: content, Mode: mode}, Kinds: []string{"wireguard"}})
	return nil
}
```

In `parts`, after `daemon`: `{"deployment directory", planDeploymentDir}, {"mesh subnet", planMeshSubnet}, {"wireguard", planWireGuard}`.

Both transports' `WriteFile` write a temporary file in the target's directory and rename it, and run as root, so the helper is root's; `ensureRootFile` compares the owner on the next run and restores it otherwise.

- [ ] **Step 4: Run, regenerate and read**

Run: `go test ./internal/hostadmit/ -update && go test ./internal/hostadmit/`
Expected: PASS. `helper-dialled`'s helper carries `port='51820'`, `helper-no-listen-port`'s `port='0'`, both `fwmark='0x5053f2a9'`; `helper-changed` and `sudoers-changed` plan `restore: ...`; `wgdir-group-writable` plans `chown root:root /etc/wireguard && chmod go-w /etc/wireguard`; each `-refused` names the path and what is wrong with it; `mesh-subnet-taken-refused` names the route through `tailscale0`.

- [ ] **Step 5: Commit**

```bash
git add internal/hostadmit
git commit -m "feat: plan the helper, its sudoers line and /etc/wireguard

The helper is the one thing the deployment's user may run as root, so
admit installs it root's and mode 755 and restores it whenever it finds
it changed. Its sudoers line is written under a name sudo skips and moved
into place only after visudo has checked it, so a bad line never reaches
sudo. /etc/wireguard is made root's alone, or refused when it is a link,
not a directory or carries an ACL, since a conf planted there would run
its PostUp as root past every check. The mesh subnet is checked against
the host's routes, addresses and Docker networks, which a request cannot
know.

Nothing runs admit yet, so no changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 9: The firewall, and who holds 80 and 443

**Files:**
- Modify: `internal/hostprep/admit.go` (`AdmitRules`)
- Modify: `internal/hostadmit/snippets/probe.sh.tmpl`, `internal/hostadmit/facts.go` (`UFWActive`)
- Create: `internal/hostadmit/firewall.go`
- Modify: `internal/hostadmit/plan.go` (`parts`)
- Test: `internal/hostprep/admit_test.go`, `internal/hostadmit/firewall_test.go`, `internal/hostadmit/testdata/plans/firewall-*.txt`, `ports-*.txt`

**Interfaces:**
- Consumes: `hostprep.Profile.Firewall(t, d, rules, hostWide)`, `hostprep.Rule`, `sshWhy`; Task 3's `Facts`; Task 4's `systemDocker`; Task 5's `rangeOf` values through `p.f.SubUID`.
- Produces: `hostprep.AdmitRules(d deployment.Deployment, sshPort, listenPort int, public bool) []Rule`; `Facts.UFWActive bool`; `type holder struct{ What string }` and `func holders(p *planner) ([]holder, error)`; part `{"firewall", planFirewall}` after `wireguard`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/hostprep/admit_test.go`:

```go
// Admit opens the SSH port, the mesh's port when one is admitted, the
// mesh interface, and 80 and 443 with the public ports; no br-+ rule, since
// a rootless container reaches the mesh address through the user's own
// process, not a host bridge.
func TestAdmitRules(t *testing.T) {
	d := deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
	var got []string
	for _, r := range hostprep.AdmitRules(d, 2222, 51820, true) {
		got = append(got, r.String()+" ("+r.Why+")")
	}
	want := []string{"2222/tcp (ssh, the bootstrap route)", "51820/udp (WireGuard, the mesh)", "all inbound on psns-f2a9 (the mesh: etcd, Patroni, Garage, HAProxy)", "80/tcp (public ports, HTTP)", "443/tcp (public ports, HTTPS)"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("AdmitRules =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if rules := hostprep.AdmitRules(d, 22, 0, false); len(rules) != 2 || !rules[0].SSH {
		t.Errorf("no port, no public ports: %+v", rules)
	}
}
```

Create `internal/hostadmit/firewall_test.go`:

```go
package hostadmit_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

const portsMarker = "paisans host admit: who holds 80 and 443"

// gateway is the fixture's gateway site's flags.
var gateway = hostadmit.Flags{Watchdog: true, PublicPorts: true}

var firewallCases = addCases(
	goldenCase{name: "firewall-inactive-ufw", site: "vm", host: home},
	goldenCase{name: "firewall-active-ufw", site: "vm", host: func() *fakeHost {
		return home().with("ufw", "active").answer("ufw show added", "ufw present\nstatus active\nrule allow 22/tcp\n")
	}},
	goldenCase{name: "firewall-firewalld-refused", site: "vm", host: func() *fakeHost { return home().with("firewalld", "active") }},
	goldenCase{name: "firewall-own-nft-refused", site: "vm", host: func() *fakeHost { return home().add("input-chain", "inet filter input") }},
	goldenCase{name: "ports-free", site: "home-a", flags: gateway, host: home},
	goldenCase{name: "ports-process-refused", site: "home-a", flags: gateway, host: func() *fakeHost {
		return home().answer(portsMarker, `listen LISTEN 0 511 0.0.0.0:443 0.0.0.0:* users:(("nginx",pid=901,fd=6)) uid:0 ino:1 sk:1`+"\nend\n")
	}},
	goldenCase{name: "ports-container-refused", site: "home-a", flags: gateway, host: func() *fakeHost {
		return home().with("system-socket", "present").answer(portsMarker, "container shop-web-1\t0.0.0.0:443->443/tcp, [::]:443->443/tcp\tshop\nend\n")
	}},
	goldenCase{name: "ports-dnat-refused", site: "home-a", flags: gateway, host: func() *fakeHost {
		return home().answer(portsMarker, "dnat ip nat DOCKER: iifname != \"docker0\" tcp dport 443 counter packets 0 bytes 0 dnat to 172.17.0.2:443\nend\n")
	}},
	goldenCase{name: "ports-own-caddy", site: "home-a", flags: gateway, host: ownCaddy},
)

// ownCaddy is an admitted gateway whose Caddy, the deployment's own, runs
// as its user (uid 1001) on 80 and 443, as admit finds it on a re-run.
func ownCaddy() *fakeHost {
	return home().answer(portsMarker, `listen LISTEN 0 4096 *:443 *:* users:(("caddy",pid=2201,fd=9)) uid:1001 ino:2 sk:2`+"\n"+
		`listen LISTEN 0 4096 *:80 *:* users:(("caddy",pid=2201,fd=8)) uid:165536 ino:3 sk:3`+"\nend\n")
}

func TestFirewallPlans(t *testing.T) { runGolden(t, firewallCases) }

// The deployment's own Caddy, run as its user or in its subordinate range,
// is not a holder; any other user's listener on the same ports is.
func TestOwnListenerIsNotAHolder(t *testing.T) {
	if _, err := hostadmit.Build(ownCaddy(), input(t, "home-a", gateway)); err != nil {
		t.Fatalf("the deployment's own Caddy refused the grant: %v", err)
	}
	other := home().answer(portsMarker, `listen LISTEN 0 4096 *:443 *:* users:(("caddy",pid=2301,fd=9)) uid:1002 ino:2 sk:2`+"\nend\n")
	_, err := hostadmit.Build(other, input(t, "home-a", gateway))
	if err == nil || !strings.Contains(err.Error(), "caddy") || !strings.Contains(err.Error(), "managed_by: external") {
		t.Fatalf("another user's Caddy on 443: %v", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/hostprep/ -run TestAdmitRules; go test ./internal/hostadmit/ -run 'TestFirewallPlans|TestOwnListener'`
Expected: FAIL: `undefined: hostprep.AdmitRules`; no firewall steps, no refusal.

- [ ] **Step 3: Implement**

In `internal/hostprep/admit.go`:

```go
// AdmitRules are the inbound rules host admit gives a deployment: the SSH
// port first, since the firewall is enabled after it; the mesh's UDP port
// when one is admitted; everything on the mesh interface; and 80 and 443
// with the public ports grant. No br-+ rule: a rootless container's
// traffic to the mesh address leaves through the user's own process, not
// a host bridge.
func AdmitRules(d deployment.Deployment, sshPort, listenPort int, public bool) []Rule {
	rules := []Rule{{Port: sshPort, Proto: "tcp", Why: sshWhy, SSH: true}}
	if listenPort != 0 {
		rules = append(rules, Rule{Port: listenPort, Proto: "udp", Why: "WireGuard, the mesh"})
	}
	rules = append(rules, Rule{Interface: d.Interface(), Why: "the mesh: etcd, Patroni, Garage, HAProxy"})
	if public {
		rules = append(rules, Rule{Port: 80, Proto: "tcp", Why: "public ports, HTTP"}, Rule{Port: 443, Proto: "tcp", Why: "public ports, HTTPS"})
	}
	return rules
}
```

In `probe.sh.tmpl`, after the firewalld line:

```sh
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q '^Status: active'; then say ufw active; else say ufw inactive; fi
```

and in `parseFacts`: `case "ufw": f.UFWActive = v == "active"`, with `UFWActive bool` in `Facts`; `newBlank` adds `{"ufw", "inactive"}`.

Create `internal/hostadmit/firewall.go`:

```go
package hostadmit

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

// planFirewall checks 80 and 443 before the public ports are granted, then
// plans ufw through the host profile: the deployment's tagged rules, and
// the default policy and enabling only on a host with no active firewall.
// A host whose own firewall is not ufw stops admit, naming it and the
// ports the deployment needs, since admit enabling ufw there would put a
// second firewall over the owner's.
func planFirewall(p *planner) error {
	req := p.in.Request
	if p.in.Flags.PublicPorts {
		found, err := holders(p)
		if err != nil {
			return err
		}
		if len(found) > 0 {
			var what []string
			for _, h := range found {
				what = append(what, "- "+h.What)
			}
			return fmt.Errorf("--public-ports cannot be granted: something else answers on 80 or 443, so the deployment's Caddy could never take them:\n%s\nStop it, or run this site behind it with web_server.managed_by: external in the deployment's configuration, which needs no low port. Nothing was changed", strings.Join(what, "\n"))
		}
		p.present("public ports: nothing else answers on 80 or 443")
	}
	if !p.f.UFWActive {
		var other []string
		if p.f.Firewalld {
			other = append(other, "firewalld is active")
		}
		for _, c := range p.f.InputChains {
			if !strings.HasPrefix(c, "ip filter ") && !strings.HasPrefix(c, "ip6 filter ") {
				other = append(other, "nftables chain "+c+" filters incoming traffic")
			}
		}
		if p.f.InputPolicy != "" && p.f.InputPolicy != "ACCEPT" || p.f.InputRules > 0 {
			other = append(other, fmt.Sprintf("iptables INPUT has policy %s and %d rules", policyOr(p.f.InputPolicy), p.f.InputRules))
		}
		if len(other) > 0 {
			ports := fmt.Sprintf("%d/tcp (SSH), the mesh interface %s", req.SSHPort, p.d.Interface())
			if req.ListenPort != 0 {
				ports += fmt.Sprintf(", %d/udp (WireGuard)", req.ListenPort)
			}
			if p.in.Flags.PublicPorts {
				ports += ", 80/tcp and 443/tcp"
			}
			return fmt.Errorf("this host has a firewall of its own (%s), and ufw is not active. Admit would enable ufw, a second firewall over yours, so it stops: open %s in your firewall and enable ufw, or admit on a host whose firewall is ufw. Nothing was changed", strings.Join(other, "; "), ports)
		}
	}
	s, err := p.profile.Firewall(p.t, p.d, hostprep.AdmitRules(p.d, req.SSHPort, req.ListenPort, p.in.Flags.PublicPorts), !p.f.UFWActive)
	if err != nil {
		return err
	}
	p.section(s)
	return nil
}

// holder is one thing answering on 80 or 443 that is not the deployment.
type holder struct{ What string }

// portsProbe reads, as root, everything that can answer on 80 or 443: a
// listener with its process and uid, a port the system Docker publishes,
// and a DNAT rule, which a daemon with its userland proxy off publishes
// with no listener at all, so only the ruleset shows it.
const portsProbe = `# paisans host admit: who holds 80 and 443
ss -Hltnpe '( sport = :80 or sport = :443 )' 2>/dev/null | sed 's/^/listen /'
if [ -S /var/run/docker.sock ]; then ` + systemDocker + ` ps --format '{{.Names}}\t{{.Ports}}\t{{.Label "com.docker.compose.project"}}' | sed 's/^/container /'; fi
nft list ruleset 2>/dev/null | awk '/^table / { t = $2 " " $3 } /^[ \t]*chain / { c = $2 } /dnat/ && /dport/ { sub(/^[ \t]+/, ""); print "dnat " t " " c ": " $0 }'
echo end`

var (
	ssUID      = regexp.MustCompile(`\buid:(\d+)`)
	ssProc     = regexp.MustCompile(`users:\(\("([^"]+)",pid=(\d+)`)
	published  = regexp.MustCompile(`:(80|443)->`)
	dnatPort   = regexp.MustCompile(`dport (\{[^}]*\b(80|443)\b[^}]*\}|(80|443)\b)`)
	listenPort = regexp.MustCompile(`:(80|443)$`)
)

// holders is everything portsProbe found that is not the deployment's own:
// a listener run as the user or in its subordinate range is its own Caddy,
// on a re-run of an admitted gateway.
func holders(p *planner) ([]holder, error) {
	out, err := p.t.Run(portsProbe)
	if err != nil {
		return nil, fmt.Errorf("reading what answers on 80 and 443: %w", err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || lines[len(lines)-1] != "end" {
		return nil, fmt.Errorf("reading what answers on 80 and 443: the answer was cut short")
	}
	var found []holder
	for _, line := range lines[:len(lines)-1] {
		kind, rest, _ := strings.Cut(line, " ")
		switch kind {
		case "listen":
			f := strings.Fields(rest)
			if len(f) < 4 || !listenPort.MatchString(f[3]) {
				continue
			}
			uid := -1
			if m := ssUID.FindStringSubmatch(rest); m != nil {
				uid, _ = strconv.Atoi(m[1])
			}
			if p.ownUID(uid) {
				continue
			}
			what := "a process listens on " + f[3]
			if m := ssProc.FindStringSubmatch(rest); m != nil {
				what = fmt.Sprintf("%s (pid %s, uid %d) listens on %s", m[1], m[2], uid, f[3])
			}
			found = append(found, holder{what})
		case "container":
			f := strings.Split(rest, "\t")
			if len(f) < 2 || !published.MatchString(f[1]) {
				continue
			}
			what := "your Docker's container " + f[0] + " publishes " + f[1]
			if len(f) > 2 && f[2] != "" {
				what += ", in compose project " + f[2]
			}
			found = append(found, holder{what})
		case "dnat":
			if dnatPort.MatchString(rest) {
				found = append(found, holder{"a DNAT rule in table " + rest})
			}
		}
	}
	return found, nil
}

// ownUID reports whether uid is the deployment's user or in its
// subordinate range.
func (p *planner) ownUID(uid int) bool {
	if uid < 0 || !p.f.User.Exists {
		return false
	}
	if uid == p.f.User.UID {
		return true
	}
	for _, r := range p.f.SubUID[p.in.Request.User] {
		if int64(uid) >= r.Start && int64(uid) < r.Start+r.Count {
			return true
		}
	}
	return false
}
```

The fake host answers `portsProbe` by `portsMarker`; `newBlank` answers it with `end\n` (nothing holds the ports): add `answer(portsMarker, "end\n")` there. The golden check `TestSystemDockerIsNamedBySocket` reads plan steps, not probes; `portsProbe`'s `docker` line carries `-H` by construction.

In `parts`, after `wireguard`: `{"firewall", planFirewall}`.

- [ ] **Step 4: Run, regenerate and read**

Run: `go test ./internal/hostprep/ && go test ./internal/hostadmit/ -update && go test ./internal/hostadmit/`
Expected: PASS. Every plan gains `allow 22/tcp`, `allow 51820/udp` where a port is admitted, `allow all inbound on psns-f2a9`, then `deny incoming`, `allow outgoing`, `enable ufw` on an inactive ufw; `firewall-active-ufw` adds the tagged rules only; `ports-free` adds 80 and 443; the three `ports-*-refused` name nginx with its pid and uid, `shop-web-1` with project `shop`, and the DNAT rule, each ending with `web_server.managed_by: external`; `ports-own-caddy` is planned.

- [ ] **Step 5: Commit**

```bash
git add internal/hostprep internal/hostadmit
git commit -m "feat: plan admit's firewall and check who holds 80 and 443

Admit opens the deployment's ports in ufw with its own tagged rules, and
sets ufw's defaults only where no firewall is active; a host with a
firewall of its own stops admit rather than gaining a second one. Before
granting the public ports it reads, as root, every listener, every port
the owner's Docker publishes and every DNAT rule on 80 and 443, since a
daemon without its userland proxy holds a port with a rule alone, and
refuses naming what holds it. The deployment's own Caddy, on a re-run, is
its own.

Nothing runs admit yet, so no changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: The watchdog grant and the host-wide sysctls

**Files:**
- Modify: `internal/hostprep/admit.go` (`PlanWatchdog`)
- Create: `internal/hostadmit/watchdog.go`, `internal/hostadmit/sysctl.go`
- Modify: `internal/hostadmit/plan.go` (`parts`)
- Test: `internal/hostadmit/watchdog_test.go`, `internal/hostadmit/sysctl_test.go`, `internal/hostadmit/testdata/plans/watchdog-*.txt`, `sysctl-*.txt`

**Interfaces:**
- Consumes: `hostprep.planWatchdog` (through `PlanWatchdog`), `config.Site`, `config.WatchdogAuto`; Task 3's `watchdogRulePath`, `sysctlPath`, `sysctlBeforePath`, `Facts.WatchdogRules`, `Facts.Records`, `Facts.PortStart`, `Facts.IPForward`; Task 5's `groupOf`; Task 7's `previous`; `admission.ParseRecord`.
- Produces: `hostprep.PlanWatchdog(t Transport, d deployment.Deployment, profile Profile) (Section, error)`; `func others(p *planner) ([]admission.Record, error)` (every other deployment's record on the host; Task 13 reuses it); `func sysctlFile(low, forward bool) string`; `type before map[string]string` with `parseBefore`, `(before) text()`; parts `{"watchdog", planWatchdogGrant}` and `{"sysctls", planSysctls}` after `firewall`.

- [ ] **Step 1: Write the failing tests**

Create `internal/hostadmit/watchdog_test.go`:

```go
package hostadmit_test

import "github.com/paisans-software/paisans-cli/internal/hostadmit"

var dataSite = hostadmit.Flags{Watchdog: true}

var watchdogCases = addCases(
	goldenCase{name: "watchdog-softdog", site: "home-b", flags: dataSite, host: homeB},
	goldenCase{name: "watchdog-hardware", site: "home-b", flags: dataSite, host: func() *fakeHost {
		return homeB().answer("RuntimeWatchdogUSec", "device present\nidentity watchdog0 iTCO_wdt\nsystemd 0\ndaemon absent\n")
	}},
	goldenCase{name: "watchdog-second-holder-refused", site: "home-b", flags: dataSite, host: func() *fakeHost {
		return homeB().add("watchdog-rule", "/etc/udev/rules.d/60-paisans-0c0d-watchdog.rules")
	}},
	goldenCase{name: "watchdog-systemd-holds-refused", site: "home-b", flags: dataSite, host: func() *fakeHost {
		return homeB().answer("RuntimeWatchdogUSec", "device present\nidentity watchdog0 iTCO_wdt\nsystemd 30s\ndaemon absent\n")
	}},
)

// homeB is a home host whose existing user is home-b's, community.
func homeB() *fakeHost {
	return home().with("passwd", "community:x:1002:1002::/home/community:/bin/bash").with("groups", "community").
		with("group", "community").add("subuid", "community:231072:65536").add("subgid", "community:231072:65536")
}

func TestWatchdogPlans(t *testing.T) { runGolden(t, watchdogCases) }
```

(Add the `testing` import.)

Create `internal/hostadmit/sysctl_test.go`:

```go
package hostadmit_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

// other is the record of a second deployment on the host, 0c0d, with the
// grants and forwarding given. The fixture always encodes, so an error is
// a broken test and panics.
func other(public, forwarding bool) string {
	rec := admission.Record{Version: admission.RecordVersion, Deployment: "0c0dc4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01", User: "paisans-0c0d",
		Grants: admission.Grants{PublicPorts: public}, Forwarding: forwarding,
		Ports: admission.Ports{SSH: 22, WireGuard: 51821, Public: []int{}}, AdmittedAt: "2026-10-01T09:00:00Z"}
	if public {
		rec.Ports.Public = []int{80, 443}
	}
	data, err := admission.EncodeRecord(rec)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// withOther is h with deployment 0c0d admitted too.
func withOther(h *fakeHost, public, forwarding bool) *fakeHost {
	return h.add("record", "/etc/paisans/admitted.0c0d.json").file("/etc/paisans/admitted.0c0d.json", other(public, forwarding))
}

var sysctlCases = addCases(
	goldenCase{name: "sysctl-endpoint-and-low-ports", site: "watch", flags: hostadmit.Flags{PublicPorts: true}, host: home},
	goldenCase{name: "sysctl-no-endpoint", site: "vm", host: home},
	goldenCase{name: "sysctl-forwarding-on-already", site: "watch", flags: hostadmit.Flags{PublicPorts: true}, host: func() *fakeHost { return home().with("ip-forward", "1") }},
	goldenCase{name: "sysctl-shared-with-another", site: "vm", host: func() *fakeHost {
		return withOther(home(), true, true).with("port-start", "80").with("ip-forward", "1").
			withFile("/etc/sysctl.d/60-paisans.conf", hostadmit.SysctlFile(true, true), "644", 0)
	}},
)

func TestSysctlPlans(t *testing.T) { runGolden(t, sysctlCases) }

// The low ports are announced as what they are: every local user may bind
// them; forwarding as routing between every interface.
func TestSysctlWarningsSayWhatTheyGrant(t *testing.T) {
	plan, err := hostadmit.Build(home(), input(t, "watch", hostadmit.Flags{PublicPorts: true}))
	if err != nil {
		t.Fatal(err)
	}
	text := plan.Text()
	for _, want := range []string{"every local user", "ports 80 to 1023", "routing between every interface"} {
		if !strings.Contains(text, want) {
			t.Errorf("the plan does not say %q", want)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/hostadmit/ -run 'TestWatchdogPlans|TestSysctl'`
Expected: FAIL: `undefined: hostadmit.SysctlFile`; no watchdog steps; the second holder is admitted.

- [ ] **Step 3: Implement**

In `internal/hostprep/admit.go`:

```go
// PlanWatchdog is what host prepare plans for a data site's watchdog in
// mode auto: the device the host has, or softdog with its warning, after
// refusing a host where systemd or a watchdog daemon already holds it.
// Admit plans it whatever the site's mode, which a request does not carry;
// host prepare and doctor compare the mode with what admit found.
func PlanWatchdog(t Transport, d deployment.Deployment, profile Profile) (Section, error) {
	return planWatchdog(t, d, profile, config.Site{Roles: []config.Role{config.RoleData}, Watchdog: config.WatchdogAuto})
}
```

Create `internal/hostadmit/watchdog.go`:

```go
package hostadmit

import (
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

// udevTrigger applies the rules to the devices that exist now (spec,
// Observed R4).
const udevTrigger = "udevadm control --reload && udevadm trigger --subsystem-match=watchdog --subsystem-match=misc"

// watchdogRule gives the device to the user, owner and group both: a
// device whose group is root's cannot be chowned inside the user
// namespace, and the infra compose chowns it for Patroni (Observed R4).
func watchdogRule(user, group string) string {
	return "# Written by paisans host admit: the watchdog is " + user + "'s, for its\n" +
		"# Patroni. One deployment per host holds it; paisans host evict removes this.\n" +
		`KERNEL=="watchdog*", OWNER="` + user + `", GROUP="` + group + `", MODE="0600"` + "\n"
}

// planWatchdogGrant gives the watchdog with --watchdog: the module as host
// prepare loads it, then the device to the user. One deployment per host
// holds it, so another deployment's rule refuses. Without the grant, a
// rule an earlier admit wrote is removed, with the module's unit.
func planWatchdogGrant(p *planner) error {
	own := watchdogRulePath(p.d)
	unit := "/etc/systemd/system/" + p.d.Prefix() + "-watchdog.service"
	if !p.in.Flags.Watchdog {
		if p.f.Files[own].Present {
			p.step(Step{Title: "take the watchdog back", Describe: "watchdog: this site is no longer a data site; remove " + own + " and give the device back to root",
				Command: "rm -f " + own + " && " + udevTrigger, Kinds: []string{hostprep.KindWatchdog}})
		}
		if p.f.Files[unit].Present {
			p.step(Step{Title: "stop loading the watchdog at boot", Describe: "watchdog: remove " + unit,
				Command: "systemctl disable " + p.d.Prefix() + "-watchdog.service; rm -f " + unit + " && systemctl daemon-reload", Kinds: []string{hostprep.KindWatchdog}})
		}
		return nil
	}
	for _, r := range p.f.WatchdogRules {
		if r != own {
			return fmt.Errorf("another deployment holds this host's watchdog (%s), and only one Patroni can open the device. Admit this data site on another host, or evict that deployment first. Nothing was changed", r)
		}
	}
	s, err := hostprep.PlanWatchdog(p.t, p.d, p.profile)
	if err != nil {
		return err
	}
	p.section(s)
	rule := watchdogRule(p.in.Request.User, groupOf(p))
	f := p.f.Files[own]
	switch {
	case f.Link:
		return fmt.Errorf("%s is a symbolic link. Remove it and admit again. Nothing was changed", own)
	case f.Sum == sha(rule) && f.Mode == "644" && f.UID == 0:
		p.present("watchdog: the device is " + p.in.Request.User + "'s")
	default:
		p.step(Step{Title: "give the watchdog to " + p.in.Request.User, Describe: "watchdog: " + own + " makes /dev/watchdog and /dev/watchdog0 " + p.in.Request.User + "'s, owner and group, mode 600",
			File: &hostprep.File{Path: own, Content: rule, Mode: 0o644}, Command: udevTrigger, Kinds: []string{hostprep.KindWatchdog}, Provisional: !p.f.User.Exists})
	}
	p.warn("watchdog: "+p.in.Request.User+"'s Patroni can reboot this machine", "Patroni arms the watchdog and reboots the whole host when it stops renewing it, which is what makes its failover safe.")
	return nil
}
```

Create `internal/hostadmit/sysctl.go`:

```go
package hostadmit

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

const (
	keyLowPorts = "net.ipv4.ip_unprivileged_port_start"
	keyForward  = "net.ipv4.ip_forward"
)

// SysctlFile is /etc/sysctl.d/60-paisans.conf as admit writes it: a line
// for each key some admitted deployment needs. It is shared by every
// admitted deployment.
func SysctlFile(low, forward bool) string {
	out := "# Written by paisans host admit, for every admitted deployment that needs\n" +
		"# it; paisans host evict restores each value once none does.\n"
	if low {
		out += keyLowPorts + " = 80\n"
	}
	if forward {
		out += keyForward + " = 1\n"
	}
	return out
}

// before is /etc/paisans/sysctl.before: each key's value as admit found it
// before it first changed it, which evict restores.
type before map[string]string

func parseBefore(content string) before {
	b := before{}
	for _, line := range strings.Split(content, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			b[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return b
}

func (b before) text() string {
	keys := make([]string, 0, len(b))
	for k := range b {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := "# Written by paisans host admit: each value as it was before admit\n# first changed it. paisans host evict restores them.\n"
	for _, k := range keys {
		out += k + " = " + b[k] + "\n"
	}
	return out
}

// others is every other admitted deployment's record on the host. One
// that cannot be read is taken to need everything, so nothing is restored
// under a deployment admit cannot see into, and is warned about.
func others(p *planner) ([]admission.Record, error) {
	var out []admission.Record
	for _, file := range p.f.Records {
		token := strings.TrimSuffix(strings.TrimPrefix(path.Base(file), "admitted."), ".json")
		if token == p.d.Token() {
			continue
		}
		content, _, err := p.t.ReadFile(file)
		if err != nil {
			return nil, err
		}
		rec, err := admission.ParseRecord([]byte(content), token)
		if err != nil {
			p.warn("sysctls: "+file+" cannot be read, so its deployment is taken to need low ports and forwarding", err.Error())
			rec = admission.Record{Deployment: token, Grants: admission.Grants{PublicPorts: true}, Forwarding: true}
		}
		out = append(out, rec)
	}
	return out, nil
}

// planSysctls keeps 60-paisans.conf holding a key while any admitted
// deployment needs it: the low ports for one granted them, forwarding for
// one with an endpoint, which relays mesh traffic between sites behind
// NAT. The value each key had before admit first changed it is recorded,
// for evict to restore.
func planSysctls(p *planner) error {
	low, fwd := p.in.Flags.PublicPorts, p.in.Request.Endpoint
	var lowFor, fwdFor []string
	recs, err := others(p)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if r.Grants.PublicPorts {
			lowFor = append(lowFor, r.Deployment)
		}
		if r.Forwarding {
			fwdFor = append(fwdFor, r.Deployment)
		}
	}
	wantLow, wantFwd := low || len(lowFor) > 0, fwd || len(fwdFor) > 0
	if low {
		p.warn("public ports: every local user on this host may bind ports 80 to 1023", keyLowPorts+" = 80 is host wide: no user's process needs root for a port from 80 up, the deployment's Caddy among them.")
	}
	if fwd {
		p.warn("forwarding: this turns on routing between every interface on this host", "The site has an endpoint, so it relays mesh traffic between sites behind NAT, which cannot dial each other. The firewall's forward policy governs what is routed.")
	} else {
		p.present("forwarding: not needed, this site has no endpoint")
	}
	beforeContent, _, err := p.t.ReadFile(sysctlBeforePath)
	if err != nil {
		return err
	}
	was := parseBefore(beforeContent)
	b := before{}
	for k, v := range was {
		b[k] = v
	}
	if wantLow {
		if _, ok := b[keyLowPorts]; !ok {
			b[keyLowPorts] = p.f.PortStart
		}
	}
	if wantFwd {
		if _, ok := b[keyForward]; !ok {
			b[keyForward] = p.f.IPForward
		}
	}
	var restore []string
	if !wantLow {
		if v, ok := b[keyLowPorts]; ok {
			restore = append(restore, keyLowPorts+"="+v)
			delete(b, keyLowPorts)
		}
	}
	if !wantFwd {
		if v, ok := b[keyForward]; ok {
			restore = append(restore, keyForward+"="+v)
			delete(b, keyForward)
		}
	}
	for _, f := range []string{sysctlPath, sysctlBeforePath} {
		if p.f.Files[f].Link {
			return fmt.Errorf("%s is a symbolic link. Remove it and admit again. Nothing was changed", f)
		}
	}
	if b.text() != before(was).text() && len(b) > 0 {
		p.step(Step{Title: "record the sysctls' values before", Describe: "sysctls: " + sysctlBeforePath + ", for host evict to restore", File: &hostprep.File{Path: sysctlBeforePath, Content: b.text(), Mode: 0o644}, Kinds: []string{"sysctl"}})
	}
	file := SysctlFile(wantLow, wantFwd)
	live := (!wantLow || p.f.PortStart == "80") && (!wantFwd || p.f.IPForward == "1")
	switch {
	case !wantLow && !wantFwd:
		if p.f.Files[sysctlPath].Present {
			p.step(Step{Title: "remove " + sysctlPath, Describe: "sysctls: no admitted deployment needs it", Command: "rm -f " + sysctlPath, Kinds: []string{"sysctl"}})
		}
	case p.f.Files[sysctlPath].Sum != sha(file) || !live:
		p.step(Step{Title: "set the host's sysctls", Describe: "sysctls: " + sysctlPath + ", applied with sysctl --system",
			File: &hostprep.File{Path: sysctlPath, Content: file, Mode: 0o644}, Command: "sysctl --system >/dev/null", Kinds: []string{"sysctl"}})
	default:
		p.present("sysctls: " + sysctlPath + " as written and applied")
	}
	if len(restore) > 0 {
		p.step(Step{Title: "restore " + strings.Join(restore, ", "), Describe: "sysctls: no admitted deployment needs them any more; set each back to what it was before admit",
			Command: "sysctl -w " + strings.Join(restore, " ") + " >/dev/null", Kinds: []string{"sysctl"}})
		if len(b) == 0 && p.f.Files[sysctlBeforePath].Present {
			p.step(Step{Title: "remove " + sysctlBeforePath, Describe: "sysctls: nothing left to restore", Command: "rm -f " + sysctlBeforePath, Kinds: []string{"sysctl"}})
		}
	}
	for _, d := range lowFor {
		p.present("sysctls: low ports kept for deployment " + d)
	}
	for _, d := range fwdFor {
		p.present("sysctls: forwarding kept for deployment " + d)
	}
	return nil
}
```

In `parts`, after `firewall`: `{"watchdog", planWatchdogGrant}, {"sysctls", planSysctls}`.

- [ ] **Step 4: Run, regenerate and read**

Run: `go test ./internal/hostprep/ && go test ./internal/hostadmit/ -update && go test ./internal/hostadmit/`
Expected: PASS. `watchdog-softdog` loads softdog, enables its unit and writes the rule with `OWNER="community", GROUP="community"`; `watchdog-hardware` writes only the rule; the two `-refused` name `60-paisans-0c0d-watchdog.rules` and `RuntimeWatchdogUSec`. `sysctl-endpoint-and-low-ports` records `net.ipv4.ip_forward = 0` and `net.ipv4.ip_unprivileged_port_start = 1024` before, then writes both keys; `sysctl-forwarding-on-already` records `ip_forward = 1`, so evict leaves it on; `sysctl-no-endpoint` changes nothing; `sysctl-shared-with-another` keeps the file and lists the other deployment.

- [ ] **Step 5: Commit**

```bash
git add internal/hostprep internal/hostadmit
git commit -m "feat: plan the watchdog grant and the host's shared sysctls

The watchdog reboots the machine, so it is the owner's to grant, and only
one Patroni can hold it: admit gives the device to the user, owner and
group both so its container can chown it, and refuses when another
deployment holds it. The low ports and forwarding are host wide and
shared, so admit keeps one file holding each key while any admitted
deployment needs it, and records the value it found before, which evict
restores once none does.

Nothing runs admit yet, so no changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: The owner's web server, and Caddy's admin API

**Files:**
- Create: `internal/hostadmit/webserver.go`
- Modify: `internal/hostadmit/plan.go` (`parts`)
- Test: `internal/hostadmit/webserver_test.go`, `internal/hostadmit/testdata/plans/webserver-*.txt`

**Interfaces:**
- Consumes: `webserver.Detect(webserver.Host{Plain: t}, webserver.Target)`, `webserver.WebServer`, `webserver.ServerCaddy`, the `Certs` constants; `admission.WebServer`; Task 4's `systemDocker`.
- Produces: `func recordWebServer(ws webserver.WebServer, adminTCP string) *admission.WebServer`; `func caddyAdmin(caddyfile string) (string, bool)`; part `{"web server", planWebServer}` after `sysctls`, which sets `p.record.WebServer` for a request with `web_server` entries and leaves it nil otherwise.

- [ ] **Step 1: Write the failing tests**

Create `internal/hostadmit/webserver_test.go`. The answers are the commands `internal/webserver/detect.go` runs; read it for each command's exact text (`dockerPS`, `ss -ltnp`, the admin API's `curl`, `ps -o args= -C caddy`, `nginx -T 2>&1`, Apache's `grep -rhiE`, `docker inspect` for mounts) and key the fake's answers by a substring only that command has:

```go
package hostadmit_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

const caddyAPI = `{"apps":{"http":{"servers":{"srv0":{"listen":[":443"]}}},"tls":{}}}`

// onHost is a web server running as a process on the host, listening on 443.
func onHost(process string) func() *fakeHost {
	return func() *fakeHost {
		return home().answer("docker ps --format", "").
			answer("ss -ltnp", `LISTEN 0 511 0.0.0.0:443 0.0.0.0:* users:(("`+process+`",pid=700,fd=6))`+"\n")
	}
}

// inContainer is one in the owner's Docker, publishing 443.
func inContainer(image string) func() *fakeHost {
	return func() *fakeHost {
		return home().with("system-socket", "present").
			answer("docker ps --format", "edge\t"+image+"\t0.0.0.0:443->443/tcp\n").
			answer("docker inspect", "[]")
	}
}

var webserverCases = addCases(
	goldenCase{name: "webserver-caddy-host-tcp-admin", site: "status", host: func() *fakeHost {
		return onHost("caddy")().answer("localhost:2019/config/", caddyAPI)
	}},
	goldenCase{name: "webserver-caddy-host-unix-admin", site: "status", host: func() *fakeHost {
		return onHost("caddy")().answer("ps -o args= -C caddy", "caddy run --config /etc/caddy/Caddyfile\n").
			answer("cat '/etc/caddy/Caddyfile'", "{\n\tadmin unix//run/caddy-admin.sock\n}\nexample.org {\n}\n")
	}},
	goldenCase{name: "webserver-caddy-container-tcp-admin", site: "status", host: func() *fakeHost {
		return inContainer("caddy:2")().answer("localhost:2019/config/", caddyAPI)
	}},
	goldenCase{name: "webserver-caddy-container-internal-admin", site: "status", host: inContainer("caddy:2")},
	goldenCase{name: "webserver-nginx-host", site: "status", host: func() *fakeHost {
		return onHost("nginx")().answer("nginx -T", "# configuration file /etc/nginx/nginx.conf:\nserver { server_name status.example.org; ssl_certificate /etc/letsencrypt/live/status.example.org/fullchain.pem; ssl_certificate_key /etc/letsencrypt/live/status.example.org/privkey.pem; }\n")
	}},
	goldenCase{name: "webserver-nginx-container", site: "status", host: func() *fakeHost {
		return inContainer("nginx:1.27")().answer("nginx -T", "# configuration file /etc/nginx/nginx.conf:\n")
	}},
	goldenCase{name: "webserver-apache-host", site: "status", host: func() *fakeHost {
		return onHost("apache2")().answer("grep -rhiE", "SSLCertificateFile /etc/ssl/certs/status.pem\nSSLCertificateKeyFile /etc/ssl/private/status.key\nServerName status.example.org\n")
	}},
	goldenCase{name: "webserver-apache-container", site: "status", host: inContainer("httpd:2.4")},
)

func TestWebServerPlans(t *testing.T) { runGolden(t, webserverCases) }

// A Caddy whose admin API answers on TCP is warned about with the fix; one
// on a unix socket is not.
func TestCaddyAdminAPIWarning(t *testing.T) {
	for name, want := range map[string]bool{"webserver-caddy-host-tcp-admin": true, "webserver-caddy-host-unix-admin": false, "webserver-caddy-container-tcp-admin": true, "webserver-caddy-container-internal-admin": false} {
		for _, c := range webserverCases {
			if c.name != name {
				continue
			}
			plan, err := hostadmit.Build(c.host(), input(t, c.site, c.flags))
			if err != nil {
				t.Fatal(err)
			}
			warned := strings.Contains(plan.Text(), "admin unix//run/caddy-admin.sock")
			if warned != want {
				t.Errorf("%s: warned %v, want %v", name, warned, want)
			}
		}
	}
}

// The Caddyfile's global options name the admin endpoint; a site block's
// admin-looking text does not.
func TestCaddyAdmin(t *testing.T) {
	for _, c := range []struct {
		conf, addr string
		set        bool
	}{
		{"{\n\tadmin :2020\n}\n", ":2020", true},
		{"{\n\tadmin off\n}\n", "", true},
		{"{\n\tadmin unix//run/caddy.sock\n}\n", "", true},
		{"{\n\temail a@example.org\n}\nexample.org {\n\trespond admin\n}\n", "", false},
		{"example.org {\n}\n", "", false},
	} {
		addr, set := hostadmit.CaddyAdmin(c.conf)
		if addr != c.addr || set != c.set {
			t.Errorf("%q: %q %v, want %q %v", c.conf, addr, set, c.addr, c.set)
		}
	}
}
```

Export `caddyAdmin` for the test through `internal/hostadmit/export_test.go` (`var CaddyAdmin = caddyAdmin`), the pattern `internal/hostprep/export_test.go` uses.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/hostadmit/ -run 'TestWebServerPlans|TestCaddyAdmin'`
Expected: FAIL: `undefined: caddyAdmin`.

- [ ] **Step 3: Implement**

Create `internal/hostadmit/webserver.go`:

```go
package hostadmit

import (
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/webserver"
)

// caddyAdminURL is Caddy's default admin endpoint.
const caddyAdminURL = "http://localhost:2019/config/"

// planWebServer detects, as root, the owner's web server in front of a
// site with web_server.managed_by: external, with the detection web-server
// show runs, since the deployment's user cannot: the system Docker socket,
// ss -p, nginx -T and /etc/letsencrypt are root's. It is recorded for
// web-server show to fill its snippet from. A Caddy whose admin API any
// local user can reach on TCP is warned about with the fix; the server is
// the owner's, so it is never refused.
func planWebServer(p *planner) error {
	apps := p.in.Request.WebServer
	if len(apps) == 0 {
		return nil
	}
	target := webserver.Target{App: apps[0].App, Hostname: apps[0].Hostname, Mode: config.ManagedByExternal, Listen: apps[0].Listen}
	ws := webserver.Detect(webserver.Host{Plain: p.t}, target)
	admin := ""
	if ws.Server == webserver.ServerCaddy {
		admin = caddyTCPAdmin(p, ws)
	}
	p.record.WebServer = recordWebServer(ws, admin)
	if ws.Why != "" {
		p.warn("web server: "+ws.Why, "Admit records what it could tell; paisans web-server show asks you for the rest when the operators run it.")
	} else {
		p.present("web server: " + ws.Summary())
	}
	if admin != "" {
		where := ws.Config
		if where == "" {
			where = "its Caddyfile"
		}
		p.warn("web server: Caddy's admin API answers on TCP at "+admin,
			"Any local user, every admitted deployment's user among them, can replace that Caddy's configuration through it. Put admin unix//run/caddy-admin.sock, or admin off, in the global options block of "+where+", and reload Caddy.")
	}
	return nil
}

// caddyTCPAdmin is the TCP address the owner's Caddy answers its admin
// API on, "" for none: the default endpoint when it answers from the host,
// or, for a Caddy on the host itself, the admin address its Caddyfile's
// global options name. A Caddy in a container whose endpoint the host
// cannot reach is no local user's to reach either.
func caddyTCPAdmin(p *planner, ws webserver.WebServer) string {
	if out, err := p.t.Run("curl -s --max-time 3 " + caddyAdminURL); err == nil && strings.HasPrefix(strings.TrimSpace(out), "{") {
		return "localhost:2019"
	}
	if ws.Container != "" || ws.Config == "" {
		return ""
	}
	conf, err := p.t.Run("cat " + shellQuote(ws.Config))
	if err != nil {
		return ""
	}
	if addr, set := caddyAdmin(conf); set && addr != "" {
		return addr
	}
	return ""
}

// caddyAdmin reads the admin directive of a Caddyfile's global options
// block, the block a Caddyfile opens with: its TCP address, "" for off or
// a unix socket, and whether the block sets one at all.
func caddyAdmin(conf string) (string, bool) {
	lines := strings.Split(conf, "\n")
	i := 0
	for i < len(lines) && (strings.TrimSpace(lines[i]) == "" || strings.HasPrefix(strings.TrimSpace(lines[i]), "#")) {
		i++
	}
	if i == len(lines) || strings.TrimSpace(lines[i]) != "{" {
		return "", false
	}
	for depth := 1; i+1 < len(lines) && depth > 0; {
		i++
		f := strings.Fields(lines[i])
		switch {
		case len(f) == 0:
		case f[0] == "}":
			depth--
		case strings.HasSuffix(lines[i], "{"):
			depth++
		case depth == 1 && f[0] == "admin" && len(f) >= 2:
			if f[1] == "off" || strings.HasPrefix(f[1], "unix/") {
				return "", true
			}
			return f[1], true
		}
	}
	return "", false
}

// recordWebServer is what the record keeps of a detection: strings only,
// so the record reads the same to any toolkit of its version.
func recordWebServer(ws webserver.WebServer, adminTCP string) *admission.WebServer {
	certs := map[webserver.Certs]string{webserver.CertsAutomatic: "automatic", webserver.CertsFiles: "files", webserver.CertsFolder: "folder"}[ws.Certs]
	return &admission.WebServer{
		Server: string(ws.Server), Container: ws.Container, Process: ws.Process,
		Config: ws.Config, Main: ws.Main, Certs: certs,
		CertFile: ws.CertFile, KeyFile: ws.KeyFile, Folder: ws.Folder, Certbot: ws.Certbot,
		AdminTCP: adminTCP, Why: ws.Why,
	}
}
```

`shellQuote` is `apply.ShellQuote`. `webserver.Detect` runs `docker ps` without `-H`; `host admit` clears `DOCKER_HOST` and `DOCKER_CONTEXT` from its own environment before it reaches the host (Task 14), and sudo's `env_reset` does the same over ssh, so it reaches the system daemon (ruling 17).

In `parts`, after `sysctls`: `{"web server", planWebServer}`.

- [ ] **Step 4: Run, regenerate and read**

Run: `go test ./internal/hostadmit/ -update && go test ./internal/hostadmit/`
Expected: PASS. Only the `webserver-*` plans gain lines (no site but `status` has `web_server` entries): a `present: web server: ...` line naming the server and its configuration, and the admin API warning in the two `-tcp-admin` plans.

- [ ] **Step 5: Commit**

```bash
git add internal/hostadmit
git commit -m "feat: detect the owner's web server as root, for the record

Behind the owner's web server, the deployment's user can neither read
nor run what tells which server it is, so admit detects it as root with
the detection web-server show runs and keeps the result for the record.
A Caddy whose admin API answers on TCP lets every local user replace its
configuration, every admitted deployment's user among them, so admit
says so with the fix; the server is the owner's, so it never refuses.

Nothing runs admit yet, so no changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 12: The claim, the record, the keys and the check

**Files:**
- Modify: `internal/registry/registry.go`, `internal/registry/registry_test.go` (`ForRequest`, `Refused`, the merge's address check)
- Modify: `internal/hostprep/admit.go` (`OwnedKey`, `ParseOwnedKeys`, `OwnedKeysFile`)
- Create: `internal/hostadmit/record.go`
- Modify: `internal/hostadmit/plan.go` (`parts`)
- Test: `internal/hostadmit/record_test.go`, `internal/hostadmit/rerun_test.go`, `internal/hostadmit/root_linux_test.go`, `internal/hostadmit/testdata/plans/record-*.txt`, `rerun-*.txt`, `claim-*.txt`

**Interfaces:**
- Consumes: `registry.Read`, `registry.Conflicts`, `registry.ClaimCommand`, `registry.JoinRoles`; `admission.EncodeRecord`, `admission.Path`, `admission.Dir`; `config.ParseKeyLine`; Task 5's `asUser`, `uidOf`; Task 7's `previous`, `rootsAlone`; every earlier task's `p.record` fields.
- Produces: `registry.ForRequest(req admission.Request, now time.Time) (string, Entry)`; `registry.Refused(t Runner, e Entry, why error) error`; `hostprep.OwnedKey{Mark, Fingerprint, Name string}`, `hostprep.ParseOwnedKeys(string) []OwnedKey`, `hostprep.OwnedKeysFile([]OwnedKey) string`; `hostadmit.CheckUser(t apply.Transport, req admission.Request) (string, error)` (the user's Docker answering, run after `Execute` by the command for both paths); parts `{"claim", planClaim}` first in `parts`, and `{"record", planRecord}`, `{"keys", planKeys}` after `web server`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/registry/registry_test.go`:

```go
// A claim from a request carries what the request does: token, root,
// interface, port, subnet and address, and the exclusive roles its grants
// imply. Two sites of one deployment on one machine are told apart by
// address, since a request names no site.
func TestForRequest(t *testing.T) {
	req := admission.Request{Version: 1, Deployment: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01", User: "paisans-f2a9", SSHPort: 22, ListenPort: 51820,
		MeshSubnet: "10.44.0.0/24", Address: "10.44.0.1", Grants: admission.Grants{Watchdog: true, PublicPorts: true}}
	id, e := registry.ForRequest(req, time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC))
	want := registry.Entry{Token: "f2a9", Root: "/srv/paisans/f2a9", ClaimedAt: "2026-10-15T12:00:00Z", Interface: "psns-f2a9", ListenPort: 51820, Subnet: "10.44.0.0/24", Address: "10.44.0.1", Roles: "data,gateway"}
	if id != req.Deployment || e != want {
		t.Errorf("ForRequest = %s %+v", id, e)
	}
}
```

and, beside the existing merge tests that run the awk program against `Encode`'s output, a row: an existing entry for the same id with `"address":"10.44.0.2"` and a claim with address `10.44.0.1` exits 3 with the conflict marker; the same id and address with `site` empty in the claim and `home-a` in the entry merges.

Create `internal/hostadmit/record_test.go`:

```go
package hostadmit_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

var recordCases = addCases(
	goldenCase{name: "record-fresh", site: "vm", host: home},
	goldenCase{name: "record-mode-restored", site: "vm", host: func() *fakeHost {
		h := admittedHost(requestFor("vm"), hostadmit.Flags{})
		rec := h.files["/etc/paisans/admitted.f2a9.json"]
		return h.withFile("/etc/paisans/admitted.f2a9.json", rec, "600", 0)
	}},
	goldenCase{name: "record-etc-paisans-link-refused", site: "vm", host: func() *fakeHost { return home().withDir("/etc/paisans", "lrwxrwxrwx", 0, 0) }},
	goldenCase{name: "claim-another-site-of-this-deployment-refused", site: "vm", host: func() *fakeHost {
		return home().file("/var/lib/paisans/registry.json", registryWith("f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01", "10.44.0.1"))
	}},
	goldenCase{name: "keys-some-present", site: "home-a", flags: gateway, host: func() *fakeHost {
		return home().answer(keysMarker, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA owner@laptop\npaisans-sidecar\n")
	}},
)

const keysMarker = "paisans host admit: the user's keys"

func TestRecordPlans(t *testing.T) { runGolden(t, recordCases) }

// Every plan that writes the record writes it root's, mode 0644, at its
// path, so the deployment's user reads it without sudo; and a record found
// with another mode is rewritten.
func TestRecordIsWrittenMode0644(t *testing.T) {
	path, _ := admission.Path("f2a9")
	for _, c := range allCases {
		_, plan := golden(t, c)
		if plan == nil {
			continue
		}
		for _, s := range plan.Steps {
			if s.File != nil && s.File.Path == path && s.File.Mode != 0o644 {
				t.Errorf("%s: the record is written mode %o", c.name, s.File.Mode)
			}
		}
	}
	plan, err := hostadmit.Build(recordCases[1].host(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Text(), "write "+path+" mode 0644") {
		t.Errorf("a record found mode 600 is not rewritten 0644:\n%s", plan.Text())
	}
}

// The same site planned from its request and from the configuration is
// the same plan, but for the digest the record keeps.
func TestRequestAndSitePlanTheSame(t *testing.T) {
	for _, site := range []string{"home-a", "home-b", "vm", "status", "watch", "store"} {
		flags := hostadmit.Flags{Watchdog: requestFor(site).Grants.Watchdog, PublicPorts: requestFor(site).Grants.PublicPorts}
		fromFile := input(t, site, flags)
		built, err := admission.Build(fixture(t), site)
		if err != nil {
			t.Fatal(err)
		}
		fromConfig := hostadmit.Input{Request: built, Flags: flags, Now: admitted}
		a, errA := hostadmit.Build(home(), fromFile)
		b, errB := hostadmit.Build(home(), fromConfig)
		if errA != nil || errB != nil {
			t.Fatalf("%s: %v, %v", site, errA, errB)
		}
		if strings.ReplaceAll(a.Text(), fromFile.Digest, "") != b.Text() {
			t.Errorf("%s: the plan from the request differs from the plan from the configuration:\n%s", site, diff(b.Text(), a.Text()))
		}
	}
}
```

`registryWith(id, address)` encodes a registry holding one entry for `id` at `address` with `registry.Encode`; add it to `fake_test.go`.

Create `internal/hostadmit/rerun_test.go`:

```go
package hostadmit_test

import (
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

// A host admitted exactly as asked plans nothing: every step is idempotent,
// so the second run of the same admit finds everything present.
func TestASecondRunPlansNothing(t *testing.T) {
	for _, c := range []struct {
		site  string
		flags hostadmit.Flags
	}{{"vm", hostadmit.Flags{}}, {"home-a", gateway}, {"home-b", dataSite}, {"status", hostadmit.Flags{}}, {"store", hostadmit.Flags{}}, {"watch", hostadmit.Flags{PublicPorts: true}}} {
		h := admittedHost(requestFor(c.site), c.flags)
		plan, err := hostadmit.Build(h, input(t, c.site, c.flags))
		if err != nil {
			t.Fatalf("%s: %v", c.site, err)
		}
		if len(plan.Steps) != 0 {
			t.Errorf("%s: a second run plans:\n%s", c.site, plan.Text())
		}
	}
}

// The site no longer needs the public ports: the re-run takes 80 and 443
// out of the firewall, drops the low ports from the shared file, restores
// the value recorded before, and writes the record without the grant.
var rerunCases = addCases(goldenCase{name: "rerun-public-ports-removed", site: "watch", host: func() *fakeHost {
	return admittedHost(requestFor("watch"), hostadmit.Flags{PublicPorts: true})
}})

func TestRerunPlans(t *testing.T) {
	c := rerunCases[0]
	req := requestFor("watch")
	req.Grants.PublicPorts = false
	in := hostadmit.Input{Request: req, Digest: admission.Digest(admission.Encode(req)), Now: admitted}
	plan, err := hostadmit.Build(c.host(), in)
	if err != nil {
		t.Fatal(err)
	}
	checkGoldenText(t, c.name, plan.Text())
}
```

`checkGoldenText(t, name, text)` is `runGolden`'s comparison for a text built by hand; split it out of `runGolden` in `golden_test.go`. `rerunCases` is in `allCases` with the default input, which plans keeping the grant; that plan is checked by the cross-cutting tests only.

`admittedHost(req, flags)` is the fake host after a real admit of `req` with `flags`: add it to `fake_test.go`. It builds the plan on `home()`, then applies each step to the fake host as a real host would change:

```go
// admittedHost is home() once admit of req with flags has run: each file
// step's file present as written, root's, with its mode; each directory
// `install -d` made; the user's daemon installed and running with the
// daemon.json written; the keys and sidecar in place; ufw holding each
// rule added; the sysctls applied; the egress table loaded; the registry
// claimed; the record read back. TestASecondRunPlansNothing is what keeps
// it honest: a fact it forgets is a step the second run plans.
func admittedHost(req admission.Request, flags hostadmit.Flags) *fakeHost {
	h := home()
	if req.User == "community" {
		h = homeB()
	}
	if flags.Watchdog {
		h.answer("RuntimeWatchdogUSec", "device present\nidentity watchdog0 Software Watchdog\nsystemd 0\ndaemon absent\n")
	}
	in := hostadmit.Input{Request: req, Digest: admission.Digest(admission.Encode(req)), Flags: flags, Now: admitted}
	plan, err := hostadmit.Build(h, in)
	if err != nil {
		panic(err)
	}
	var rules []string
	daemonJSON, keys := "", ""
	for _, s := range plan.Steps {
		if s.File != nil {
			h.withFile(s.File.Path, s.File.Content, fmt.Sprintf("%o", s.File.Mode), 0)
			h.files[s.File.Path] = s.File.Content
		}
		for _, f := range strings.Split(s.Command, " && ") {
			switch {
			case strings.HasPrefix(f, "install -d -m "):
				w := strings.Fields(f) // install -d -m MODE [-o U -g G] PATH
				uid := 0
				if len(w) > 5 && w[5] != "root" {
					uid = 1001
				}
				h.withDir(w[len(w)-1], modeString(w[3]), uid, uid)
			case strings.HasPrefix(f, "ufw allow "):
				rules = append(rules, "rule "+strings.TrimPrefix(f, "ufw "))
			case strings.HasPrefix(f, "mv -f /etc/sudoers.d/"):
				p := strings.Fields(f)
				h.withFile(p[3], h.files[p[2]], "440", 0)
			}
		}
		if strings.Contains(s.Command, "daemon.json") {
			daemonJSON = s.Input
		}
		if strings.Contains(s.Command, "authorized_keys") {
			keys = s.Input
		}
	}
	firewall := "ufw present\nstatus active\n" + strings.Join(rules, "\n") + "\n"
	h.answer("ufw show added", firewall).with("ufw", "active").
		with("br_netfilter", "loaded").with("egress-enabled", "yes").with("egress-table", "present").
		answer(userStateMarker, "daemon active\nunit present\ndaemon-json "+sum(daemonJSON)+"\ncontainers 0\nimages 0\nvolumes 0\nend\n").
		answer(keysMarker, keysAfter(keys)).
		with("rootlesskit-profile", "loaded")
	if flags.PublicPorts {
		h.with("port-start", "80")
	}
	if req.Endpoint {
		h.with("ip-forward", "1")
	}
	h.add("record", "/etc/paisans/admitted.f2a9.json")
	h.file("/var/lib/paisans/registry.json", registryWith(req.Deployment, req.Address))
	return h
}
```

(`modeString("750")` is `drwxr-x---`; `keysAfter` turns the keys step's stdin into the `authorized_keys` lines and sidecar the fake answers the keys probe with. Each is a few lines; write them with the helper.)

Create `internal/hostadmit/root_linux_test.go`, run only as root with `PAISANS_ROOT_TESTS=1`, on a throwaway machine or container, since it makes and removes a real user:

```go
//go:build linux

package hostadmit_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

// The keys are written as the user, so a ~/.ssh the user planted as a link
// to a directory of root's leaves that directory as it was.
func TestAPlantedSSHLinkLeavesItsTargetAlone(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("PAISANS_ROOT_TESTS") != "1" {
		t.Skip("run as root with PAISANS_ROOT_TESTS=1: it creates and removes the user paisans-0e0e")
	}
	user := "paisans-0e0e"
	if out, err := exec.Command("useradd", "--create-home", "--shell", "/bin/bash", user).CombinedOutput(); err != nil {
		t.Fatalf("useradd: %v %s", err, out)
	}
	t.Cleanup(func() { exec.Command("userdel", "--remove", user).Run() })
	target := t.TempDir() // root's, 700
	os.Chmod(target, 0o700)
	if err := os.Symlink(target, "/home/"+user+"/.ssh"); err != nil {
		t.Fatal(err)
	}
	command := hostadmit.KeysCommand(user, "/home/"+user, "0e0e")
	_, err := hostadmit.Local().RunInput(command, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice\n--- sidecar ---\n")
	if err == nil {
		t.Error("writing keys through a link to root's directory succeeded")
	}
	entries, _ := os.ReadDir(target)
	if len(entries) != 0 {
		t.Errorf("root's directory gained %v", entries)
	}
	if _, err := os.Stat(filepath.Join(target, "authorized_keys")); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Errorf("authorized_keys appeared in root's directory: %v", err)
	}
}
```

`hostadmit.KeysCommand(user, home, token)` is the keys step's command, `asUser` and the script below, exported through `export_test.go` as `KeysCommand` from an unexported `keysCommand`; it takes the uid from `id -u` at run time when given no facts, so it can run without a `planner`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/registry/ -run TestForRequest; go test ./internal/hostadmit/ -run 'TestRecord|TestRequestAndSite|TestASecondRun|TestRerun'`
Expected: FAIL: `undefined: registry.ForRequest`; no claim, record or keys steps; `TestASecondRunPlansNothing` lists the steps each part still plans on an admitted host, which is the list of facts `admittedHost` must set.

- [ ] **Step 3: The registry**

In `internal/registry/registry.go` (add `admission` to its imports; `admission` imports only `config` and `deployment`, so there is no cycle):

```go
// ForRequest is the entry host admit claims a host with, from the request
// alone, so a plan from a request and one from the configuration claim
// alike. A request names no site and no roles: Site and Domain stay empty,
// and Roles holds the two exclusive roles its grants imply, data for the
// watchdog and gateway for the public ports, which are what one host gives
// to one deployment.
func ForRequest(req admission.Request, now time.Time) (string, Entry) {
	d := deployment.Deployment{ID: req.Deployment}
	var roles []config.Role
	if req.Grants.Watchdog {
		roles = append(roles, config.RoleData)
	}
	if req.Grants.PublicPorts {
		roles = append(roles, config.RoleGateway)
	}
	return req.Deployment, Entry{
		Token: d.Token(), Root: d.Root(), ClaimedAt: now.UTC().Format(time.RFC3339),
		Interface: d.Interface(), ListenPort: req.ListenPort, Subnet: req.MeshSubnet, Address: req.Address,
		Roles: JoinRoles(roles),
	}
}

// Refused is the refusal a claim of e meets for why, in the words Claim
// uses, for host admit's read-only check.
func Refused(t Runner, e Entry, why error) error { return refused(t, e, why) }
```

In `awkArgs`, add `"address": e.Address` to `vars` and `"address"` to the name list after `"site"`; in `mergeProgram`, replace the same-id block with:

```awk
	if (key == id) {
		s = str(line, "site"); a = str(line, "address")
		if ((s != "" && site != "" && s != site) || (a != "" && address != "" && a != address)) {
			print marker line > "/dev/stderr"
			conflict = 1
		}
		next
	}
```

and update the program's comment: "id's own line naming another site, or another mesh address, is a conflict too".

- [ ] **Step 4: The sidecar in hostprep**

In `internal/hostprep/admit.go`, export the sidecar's format, reusing `parseOwned`, `ownedEntry`, `markAdded` and the header `writeOwned` writes in `authorized_keys.go` (read them; do not write a second format):

```go
// OwnedKey is one line of a deployment's authorized_keys sidecar: added
// for a key the deployment appended, shared for one another deployment
// appended.
type OwnedKey struct{ Mark, Fingerprint, Name string }

// ParseOwnedKeys reads a sidecar.
func ParseOwnedKeys(content string) []OwnedKey

// OwnedKeysFile is a sidecar holding keys, as host prepare writes one.
func OwnedKeysFile(keys []OwnedKey) string
```

with a test that `OwnedKeysFile(ParseOwnedKeys(x)) == x` for a sidecar host prepare's own tests write.

- [ ] **Step 5: The parts**

Create `internal/hostadmit/record.go`:

```go
package hostadmit

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/registry"
)

// planClaim claims the host in its registry first, so a conflict found
// under the claim's lock stops admit with nothing changed. The read-only
// check here refuses it before the plan is shown: a token, root,
// interface, port, subnet or exclusive role another deployment holds, or
// another site of this deployment on this machine.
func planClaim(p *planner) error {
	id, e := registry.ForRequest(p.in.Request, p.in.Now)
	r, err := registry.Read(p.t)
	if err != nil {
		return err
	}
	if mine, ok := r.Deployments[id]; ok && mine.Address != "" && mine.Address != e.Address {
		return fmt.Errorf("this machine already runs this deployment's site at %s, and this request is for %s. A site is a machine: its files, containers and WireGuard interface are named for the deployment, so a second site of it here would overwrite the first. Admit it on another machine. Nothing was changed", mine.Address, e.Address)
	}
	if c := registry.Conflicts(r, id, e); len(c) > 0 {
		return registry.Refused(p.t, e, c[0])
	}
	if mine, ok := r.Deployments[id]; ok {
		mine.ClaimedAt = e.ClaimedAt
		if mine == e {
			p.present("registry: this host is claimed for deployment " + p.d.Token())
			return nil
		}
	}
	command, err := registry.ClaimCommand(id, e)
	if err != nil {
		return err
	}
	p.step(Step{Title: "claim the host", Describe: "registry: record deployment " + p.d.Token() + " in " + registry.Path + ", so no other deployment here takes its token, directory, interface, port, subnet or role",
		Command: command, Kinds: []string{"registry"}})
	return nil
}

// planRecord writes the admission record, root's and mode 0644 so the
// deployment's own user reads it without sudo, through the transport's
// write, which writes a temporary file beside it and renames it. A record
// that says the same but for its time is left with its time; one found
// with another mode or owner is rewritten.
func planRecord(p *planner) error {
	dir := p.f.Dirs[admission.Dir]
	switch {
	case !dir.Present:
		p.step(Step{Title: "create " + admission.Dir, Describe: "record: root's, mode 755: every deployment's user reads its own record, none can write one", Command: "install -d -m 755 -o root -g root " + admission.Dir, Kinds: []string{"record"}})
	case len(dir.Mode) != 10 || dir.Mode[0] != 'd':
		return fmt.Errorf("%s is %q, not a directory: it must be root's, so no deployment can write another's record. Fix it and admit again. Nothing was changed", admission.Dir, dir.Mode)
	case !rootsAlone(dir):
		p.step(Step{Title: "make " + admission.Dir + " root's alone", Describe: fmt.Sprintf("record: it is %s, owner %d", dir.Mode, dir.UID), Command: "chown root:root " + admission.Dir + " && chmod 755 " + admission.Dir, Kinds: []string{"record"}})
	}
	path, err := admission.Path(p.d.Token())
	if err != nil {
		return err
	}
	f := p.f.Files[path]
	if f.Link {
		return fmt.Errorf("%s is a symbolic link. Remove it and admit again. Nothing was changed", path)
	}
	prev, err := p.previous()
	if err != nil {
		return err
	}
	if prev != nil && f.Mode == "644" && f.UID == 0 {
		same := *prev
		same.AdmittedAt = p.record.AdmittedAt
		if reflect.DeepEqual(same, p.record) {
			p.present("record: " + path + " as admitted at " + prev.AdmittedAt)
			return nil
		}
	}
	content, err := admission.EncodeRecord(p.record)
	if err != nil {
		return err
	}
	describe := "record: who the deployment runs as and what you granted, which its own commands read"
	if f.Present && (f.Mode != "644" || f.UID != 0) {
		describe += fmt.Sprintf("; found mode %s, owner %d, restored to root's and 0644", f.Mode, f.UID)
	}
	p.step(Step{Title: "write the admission record", Describe: describe, File: &hostprep.File{Path: path, Content: string(content), Mode: admission.Mode}, Kinds: []string{"record"}})
	return nil
}

// keysProbe reads, as the user, its authorized_keys and the sidecar admit
// and host prepare keep of the keys they appended.
func keysProbe(token string) string {
	return "# paisans host admit: the user's keys\ncat \"$HOME/.ssh/authorized_keys\" 2>/dev/null || true\necho paisans-sidecar\ncat " + sidecarPath(token) + " 2>/dev/null || true"
}

func sidecarPath(token string) string { return "/srv/paisans/" + token + "/.paisans/authorized_keys.owned" }

// keysScript appends the keys on its stdin to authorized_keys and writes
// the sidecar after the separator, as the user, each through a temporary
// file and a rename. It runs as the user, so a link the user planted is
// followed with the user's rights alone.
func keysScript(token string) string {
	return `set -e; umask 077; mkdir -p "$HOME/.ssh" /srv/paisans/` + token + `/.paisans
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
awk -v k="$tmp/keys" -v s="$tmp/sidecar" '$0 == "--- sidecar ---" { out = 1; next } { if (out) print > s; else print > k }'
touch "$tmp/keys" "$tmp/sidecar"
{ [ -f "$HOME/.ssh/authorized_keys" ] && awk 1 "$HOME/.ssh/authorized_keys"; cat "$tmp/keys"; } > "$HOME/.ssh/.authorized_keys.paisans-tmp"
mv -f "$HOME/.ssh/.authorized_keys.paisans-tmp" "$HOME/.ssh/authorized_keys"
cat "$tmp/sidecar" > ` + sidecarPath(token) + `.paisans-tmp && mv -f ` + sidecarPath(token) + `.paisans-tmp ` + sidecarPath(token)
}

// planKeys adds each operator's key the user's authorized_keys lacks, and
// records it in the sidecar as host prepare does, so a later host prepare,
// run by the operators as the user, can take a key away. Admit never
// removes a key: the owner may have put one there.
func planKeys(p *planner) error {
	have := map[string]bool{}
	var owned []hostprep.OwnedKey
	if p.f.User.Exists {
		out, err := p.t.Run(asUser(p, keysProbe(p.d.Token())))
		if err != nil {
			return fmt.Errorf("reading %s's keys as %s: %w", p.in.Request.User, p.in.Request.User, err)
		}
		keys, sidecar, _ := strings.Cut(out, "paisans-sidecar\n")
		for _, line := range strings.Split(keys, "\n") {
			if k, _, err := config.ParseKeyLine(line); err == nil {
				have[k.Fingerprint] = true
			}
		}
		owned = hostprep.ParseOwnedKeys(sidecar)
	}
	var add []string
	for _, o := range p.in.Request.Operators {
		for _, key := range o.Keys {
			k, _, err := config.ParseKeyLine(key + " " + o.Name)
			if err != nil {
				return fmt.Errorf("operator %s's key: %w", o.Name, err)
			}
			if have[k.Fingerprint] {
				continue
			}
			have[k.Fingerprint] = true
			add = append(add, key+" "+o.Name)
			owned = append(owned, hostprep.OwnedKey{Mark: "added", Fingerprint: k.Fingerprint, Name: o.Name})
		}
	}
	if len(add) == 0 {
		p.present("keys: every operator's key is in " + p.in.Request.User + "'s authorized_keys")
		return nil
	}
	p.step(Step{Title: "add the operators' keys", Describe: fmt.Sprintf("keys: %d operator keys into %s's authorized_keys, written as %s, and recorded for host prepare", len(add), p.in.Request.User, p.in.Request.User),
		Command: asUser(p, keysScript(p.d.Token())), Input: strings.Join(add, "\n") + "\n--- sidecar ---\n" + hostprep.OwnedKeysFile(owned),
		Kinds: []string{hostprep.KindSSHKeys}, Provisional: !p.f.User.Exists})
	return nil
}

// CheckUser runs docker version as the deployment's user on the host t
// reaches as root, which is admit's last step on both paths: the user's
// own daemon answers on its own socket. It returns the Engine's version.
func CheckUser(t apply.Transport, req admission.Request) (string, error) {
	out, err := t.Run("runuser -u " + req.User + ` -- sh -c 'uid=$(id -u); env -i HOME="$HOME" PATH=/usr/bin:/bin XDG_RUNTIME_DIR=/run/user/$uid DOCKER_HOST=unix:///run/user/$uid/docker.sock docker version --format "{{.Server.Version}}"'`)
	if err != nil {
		return "", fmt.Errorf("%s's Docker does not answer on its own socket: %w", req.User, err)
	}
	return strings.TrimSpace(out), nil
}
```

The keys step's `Input` carries keys and the sidecar on stdin, never on a command line. Export `keysCommand(user, home, token)` for the root test as described in Step 1: it is `asUser`'s command line with the uid read by `id -u` at run time.

In `parts`, put `{"claim", planClaim}` first and add `{"record", planRecord}, {"keys", planKeys}` after `web server`.

- [ ] **Step 6: Run, regenerate and read**

Run: `go test ./internal/registry/ ./internal/hostprep/ && go test ./internal/hostadmit/ -update && go test ./internal/hostadmit/ && go test ./internal/...`
Expected: PASS, `TestASecondRunPlansNothing` included. Every plan now opens with `claim the host` and ends with `write the admission record` (its JSON in full, `"admitted_at": "2026-10-15T12:00:00Z"`, `"digest"` the request's) and `add the operators' keys` (stdin: each key with its operator's name, then `--- sidecar ---` and the sidecar). `record-mode-restored` says "found mode 600, owner 0, restored". `rerun-public-ports-removed` deletes the two tagged ufw rules, drops the low ports from `60-paisans.conf`, runs `sysctl -w net.ipv4.ip_unprivileged_port_start=1024` and writes the record with `"public_ports": false`.

- [ ] **Step 7: Commit**

```bash
git add internal/registry internal/hostprep internal/hostadmit
git commit -m "feat: plan admit's claim, its record and the operators' keys

The claim comes first, so a conflict changes nothing, and is built from
the request on both paths, so a plan from a request and one from the
configuration are the same plan. The record is written root's and 0644
through a temporary file and a rename, so the deployment's own user reads
it without sudo, and a record found otherwise is restored. The operators'
keys are added as the user, recorded the way host prepare records them,
and never taken away by admit.

Nothing runs admit yet, so no changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 13: `host evict`'s plan

**Files:**
- Create: `internal/hostadmit/evict.go`
- Modify: `internal/hostadmit/snippets/probe.sh.tmpl`, `internal/hostadmit/facts.go` (the mesh unit's state, the installed conf)
- Test: `internal/hostadmit/evict_test.go`, `internal/hostadmit/testdata/evict/*.txt`

**Interfaces:**
- Consumes: Task 3's `probe`, `Facts`, `Plan`, `Step`, `run`; Task 5's `PrivilegedGroups`; Task 7's `previous`; Task 9's `hostprep.AdmitRules` and the profile's `Firewall`; Task 10's `others`, `parseBefore`, `SysctlFile`, `keyLowPorts`, `keyForward`; `registry.Read`, `registry.RemoveCommand`; `config.DeploymentUserProblem`.
- Produces: `type EvictInput struct { Deployment, User string }`; `func BuildEvict(t apply.Transport, in EvictInput) (*Plan, error)`; `func RunSteps(t apply.Transport, plan *Plan, r ui.Reporter) error` (every step in order, no rebuild, through Task 3's `run`); `Facts.MeshUnit struct{ Enabled, Active bool }`.

- [ ] **Step 1: Write the failing tests**

Create `internal/hostadmit/evict_test.go`:

```go
package hostadmit_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

const id = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"

// evicted is a host where admit of site ran with flags, with the mesh up.
func evicted(site string, flags hostadmit.Flags) func() *fakeHost {
	return func() *fakeHost {
		return admittedHost(requestFor(site), flags).with("mesh-unit", "enabled active").
			withFile("/etc/wireguard/psns-f2a9.conf", "[Interface]\n", "600", 0)
	}
}

var evictCases = []struct {
	name string
	user string
	host func() *fakeHost
}{
	{"evict-only-deployment", "paisans-f2a9", evicted("watch", hostadmit.Flags{PublicPorts: true})},
	{"evict-one-of-two", "paisans-f2a9", func() *fakeHost {
		return withOther(evicted("watch", hostadmit.Flags{PublicPorts: true})(), true, true)
	}},
	{"evict-with-data-dir", "paisans-f2a9", func() *fakeHost {
		h := admittedWithDataDir("vm", "/srv/data")
		return h.with("mesh-unit", "enabled active")
	}},
	{"evict-without-record", "paisans-f2a9", func() *fakeHost {
		h := evicted("vm", hostadmit.Flags{})()
		delete(h.files, "/etc/paisans/admitted.f2a9.json")
		h.dropKeyed("file", "/etc/paisans/admitted.f2a9.json")
		return h
	}},
	{"evict-nothing-left", "paisans-f2a9", func() *fakeHost { return newBlank() }},
}

func TestEvictPlans(t *testing.T) {
	for _, c := range evictCases {
		t.Run(c.name, func(t *testing.T) {
			plan, err := hostadmit.BuildEvict(c.host(), hostadmit.EvictInput{Deployment: id, User: c.user})
			got := ""
			if err != nil {
				got = "refused: " + err.Error() + "\n"
			} else {
				got = plan.Text()
			}
			checkGoldenAt(t, filepath.Join("testdata", "evict", c.name+".txt"), got)
		})
	}
}

// Evict deletes a user only when it is the deployment's: the record's, or,
// with no record, paisans-<token>. The owner's login, root, another
// deployment's user and a privileged account are refused, with nothing
// planned.
func TestEvictRefusesAnotherUser(t *testing.T) {
	withRecord := evicted("vm", hostadmit.Flags{})
	noRecord := func() *fakeHost {
		h := withRecord()
		delete(h.files, "/etc/paisans/admitted.f2a9.json")
		return h
	}
	for _, c := range []struct {
		name, user string
		host       func() *fakeHost
	}{
		{"root", "root", withRecord},
		{"another deployment's", "paisans-0c0d", withRecord},
		{"not the record's", "community", withRecord},
		{"the owner's login, no record", "ubuntu", noRecord},
		{"privileged", "paisans-f2a9", func() *fakeHost { return withRecord().with("groups", "paisans-f2a9 sudo") }},
	} {
		h := c.host()
		_, err := hostadmit.BuildEvict(h, hostadmit.EvictInput{Deployment: id, User: c.user})
		if err == nil {
			t.Errorf("%s: evict of %s was planned", c.name, c.user)
		}
	}
}

// With another deployment admitted, evict leaves the host-wide files and
// every sysctl another deployment needs; it takes only its own.
func TestEvictOfOneOfTwoKeepsTheHostWide(t *testing.T) {
	h := withOther(evicted("watch", hostadmit.Flags{PublicPorts: true})(), true, true)
	plan, err := hostadmit.BuildEvict(h, hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	text := plan.Text()
	for _, kept := range []string{"delegate.conf", "modules-load.d/paisans.conf", "60-paisans.conf", "sysctl -w"} {
		for _, s := range plan.Steps {
			if strings.Contains(s.Command, kept) {
				t.Errorf("evict of one of two touches %s: %s", kept, s.Command)
			}
		}
	}
	if !strings.Contains(text, "kept for deployment 0c0dc4e1") {
		t.Errorf("the plan does not say what it keeps, and for whom:\n%s", text)
	}
}
```

`withOther` is Task 10's. `admittedWithDataDir(site, dir)` is `admittedHost` with `Flags{DataDir: dir}` on a host whose `/srv/data` is root's 755; `checkGoldenAt(t, path, got)` is `checkGoldenText` with a path. Add the three to `fake_test.go`/`golden_test.go`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/hostadmit/ -run 'TestEvict'`
Expected: FAIL: `undefined: hostadmit.BuildEvict`.

- [ ] **Step 3: The probe's two facts**

In `probe.sh.tmpl`, after the egress lines:

```sh
say mesh-unit "$(systemctl is-enabled {{.MeshUnit}} 2>/dev/null || echo disabled) $(systemctl is-active {{.MeshUnit}} 2>/dev/null || echo inactive)"
```

with `MeshUnit: d.WireGuardUnit()` in `probeData`; add `d.WireGuardInstalled()` (with its leading `/`, as `registry.sameMachine` builds it) to `probedFiles`; parse `mesh-unit` into `Facts.MeshUnit{Enabled: first == "enabled", Active: second == "active"}`.

- [ ] **Step 4: Implement**

Create `internal/hostadmit/evict.go`:

```go
package hostadmit

import (
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// EvictInput is what evict is given: the deployment, and the user the
// owner names, from a request or typed.
type EvictInput struct{ Deployment, User string }

// BuildEvict plans undoing admit for one deployment, leaving every other
// admitted deployment and the owner's own services as they were: what is
// the deployment's goes, and a host-wide file or sysctl goes only when no
// other admitted deployment needs it. Each step is planned from a probe,
// so a re-run after a failure plans only what is left.
//
// The user is deleted only when it is the deployment's: the record's user,
// or, with no record, paisans-<token>. Anything else is refused, so a
// mistyped or lost name never deletes the owner's own account.
func BuildEvict(t apply.Transport, in EvictInput) (*Plan, error) {
	if !deployment.ValidID(in.Deployment) {
		return nil, fmt.Errorf("%q is not a deployment id", in.Deployment)
	}
	d := deployment.Deployment{ID: in.Deployment}
	if prob := config.DeploymentUserProblem(in.User, in.Deployment); prob != "" {
		return nil, fmt.Errorf("--user: %s. Nothing was changed", prob)
	}
	ain := Input{Request: admission.Request{Deployment: in.Deployment, User: in.User}}
	f, err := probe(t, d, ain)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", t.Describe(), err)
	}
	p := &planner{t: t, in: ain, d: d, f: f, plan: &Plan{Deployment: in.Deployment, User: in.User, Profile: "evict", Host: t.Describe()}}
	rec, err := p.previous()
	if err != nil {
		return nil, err
	}
	switch {
	case rec != nil && rec.User != in.User:
		return nil, fmt.Errorf("this host's admission record for %s names the user %s, not %s. Evict names the deployment's own user. Nothing was changed", d.Token(), rec.User, in.User)
	case rec == nil && in.User != d.Prefix():
		return nil, fmt.Errorf("this host holds no admission record for %s, so evict deletes only %s, the deployment's own default user, and not %s, which may be someone else's. Nothing was changed", d.Token(), d.Prefix(), in.User)
	}
	if f.User.Exists {
		for _, g := range append([]string{f.User.Group}, f.User.Groups...) {
			if slices.Contains(PrivilegedGroups, g) || f.User.UID == 0 {
				return nil, fmt.Errorf("%s is in %s or is root, which admit never makes a deployment's user, so it is not this deployment's to delete. Nothing was changed", in.User, g)
			}
		}
	}
	host, _, err := t.ReadFile("/etc/os-release")
	if err != nil {
		return nil, err
	}
	if p.profile, err = hostprep.Select(hostprep.ParseOSRelease(host)); err != nil {
		return nil, err
	}
	for _, step := range []func(*planner, *admission.Record) error{evictMesh, evictUser, evictFiles, evictFirewall, evictHostWide, evictRecord} {
		if err := step(p, rec); err != nil {
			return nil, err
		}
	}
	return p.plan, nil
}

func (p *planner) remove(title, describe, command string) {
	p.step(Step{Title: "remove " + title, Describe: describe, Command: command, Kinds: []string{"evict"}})
}

// evictMesh stops the mesh first, so nothing of the deployment is routed
// while the rest goes, and removes its conf.
func evictMesh(p *planner, _ *admission.Record) error {
	unit, conf := p.d.WireGuardUnit(), "/"+strings.TrimPrefix(p.d.WireGuardInstalled(), "/")
	if p.f.MeshUnit.Enabled || p.f.MeshUnit.Active {
		p.remove("the mesh interface", "evict: stop and disable "+unit, "systemctl disable --now "+unit)
	}
	if p.f.Files[conf].Present {
		p.remove(conf, "evict: the mesh's conf, which holds the site's private key", "rm -f "+conf)
	}
	return nil
}

// evictUser stops the user's manager, which stops its Docker daemon and
// every container, disables linger and deletes the user with its home,
// then any line useradd gave it in /etc/subuid and /etc/subgid that
// userdel left.
func evictUser(p *planner, _ *admission.Record) error {
	u, user := p.f.User, p.in.Request.User
	if !u.Exists {
		return nil
	}
	p.remove("user "+user, "evict: stop "+user+"'s manager, its daemon and every container, disable linger, and delete "+user+" with its home",
		fmt.Sprintf("loginctl disable-linger %s; systemctl stop user@%d.service; loginctl terminate-user %s 2>/dev/null; userdel --remove %s && sed -i '/^%s:/d' /etc/subuid /etc/subgid", user, u.UID, user, user, user))
	return nil
}

// evictFiles removes what admit put on the host for the deployment alone.
// A directory is removed only when it is not a link, and without crossing
// into another filesystem.
func evictFiles(p *planner, rec *admission.Record) error {
	rmDir := func(dir, why string) {
		if s := p.f.Dirs[dir]; s.Present {
			p.remove(dir, why, "[ ! -L "+dir+" ] && rm -rf --one-file-system -- "+dir)
		}
	}
	if rec != nil && rec.DataDir != "" {
		rmDir(rec.DataDir, "evict: the deployment's Docker data, on the disk --data-dir named")
	}
	rmDir(p.d.Root(), "evict: the deployment's directory")
	helper, _ := apply.WGHelper{Iface: p.d.Interface()}.Path()
	for _, f := range []struct{ path, why string }{
		{helper, "evict: the WireGuard helper"},
		{sudoersPath(p.d), "evict: the helper's sudoers line"},
	} {
		if p.f.Files[f.path].Present {
			p.remove(f.path, f.why, "rm -f "+f.path+" "+f.path+".new")
		}
	}
	if p.f.Files[watchdogRulePath(p.d)].Present {
		p.remove("the watchdog rule", "evict: give the watchdog back to root", "rm -f "+watchdogRulePath(p.d)+" && "+udevTrigger)
	}
	wdUnit := "/etc/systemd/system/" + p.d.Prefix() + "-watchdog.service"
	if p.f.Files[wdUnit].Present {
		p.remove("the watchdog unit", "evict: stop loading the watchdog at boot", "systemctl disable "+p.d.Prefix()+"-watchdog.service; rm -f "+wdUnit+" && systemctl daemon-reload")
	}
	unit := "/etc/systemd/system/" + p.d.EgressUnit()
	if p.f.Files[unit].Present || p.f.EgressTable || p.f.Files[egressFilePath(p.d)].Present {
		p.remove("the egress table", "evict: unload "+p.d.EgressTable()+" and remove its unit and file",
			"systemctl disable --now "+p.d.EgressUnit()+" 2>/dev/null; nft delete table inet "+p.d.EgressTable()+" 2>/dev/null; rm -f "+unit+" "+egressFilePath(p.d)+" && systemctl daemon-reload")
	}
	return nil
}

// evictFirewall removes the deployment's tagged ufw rules but its SSH
// allow, which may be what the owner's own login arrives by: that one is
// left, with a warning, for the owner to delete. The default policy and
// whether ufw is on are never changed.
func evictFirewall(p *planner, rec *admission.Record) error {
	ssh := 22
	if rec != nil {
		ssh = rec.Ports.SSH
	}
	keep := hostprep.AdmitRules(p.d, ssh, 0, false)[:1]
	s, err := p.profile.Firewall(p.t, p.d, keep, false)
	if err != nil {
		return err
	}
	for _, st := range s.Steps {
		if st.Label == "remove" {
			p.step(Step{Title: st.Title, Describe: st.Describe, Command: st.Command, Kinds: st.Kinds})
		}
	}
	p.warn(fmt.Sprintf("firewall: the deployment's SSH allow on %d/tcp is kept", ssh), "It may be the rule your own login arrives by. Delete it yourself (ufw delete allow "+fmt.Sprint(ssh)+"/tcp) once nothing else needs it.")
	return nil
}

// evictHostWide takes the host-wide files and sysctls away only when no
// other admitted deployment needs them, restoring each sysctl to the value
// admit found before it first changed it.
func evictHostWide(p *planner, _ *admission.Record) error {
	recs, err := others(p)
	if err != nil {
		return err
	}
	var lowFor, fwdFor []string
	for _, r := range recs {
		if r.Grants.PublicPorts {
			lowFor = append(lowFor, r.Deployment)
		}
		if r.Forwarding {
			fwdFor = append(fwdFor, r.Deployment)
		}
	}
	if len(recs) == 0 {
		if p.f.Files[delegatePath].Sum == sha(delegateConf) {
			p.remove(delegatePath, "evict: no other deployment is admitted", "rm -f "+delegatePath+" && systemctl daemon-reload")
		}
		if p.f.Files[modulesLoadPath].Sum == sha(modulesLoadConf) {
			p.remove(modulesLoadPath, "evict: no other deployment is admitted; br_netfilter stays loaded until the next boot", "rm -f "+modulesLoadPath)
		}
	} else {
		for _, r := range recs {
			p.present("host wide: delegate.conf and br_netfilter kept for deployment " + r.Deployment)
		}
	}
	content, _, err := p.t.ReadFile(sysctlBeforePath)
	if err != nil {
		return err
	}
	b := parseBefore(content)
	var restore []string
	if v, ok := b[keyLowPorts]; ok && len(lowFor) == 0 {
		restore = append(restore, keyLowPorts+"="+v)
		delete(b, keyLowPorts)
	}
	if v, ok := b[keyForward]; ok && len(fwdFor) == 0 {
		restore = append(restore, keyForward+"="+v)
		delete(b, keyForward)
	}
	for _, d := range lowFor {
		p.present("sysctls: low ports kept for deployment " + d)
	}
	for _, d := range fwdFor {
		p.present("sysctls: forwarding kept for deployment " + d)
	}
	if len(restore) == 0 {
		return nil
	}
	wantLow, wantFwd := len(lowFor) > 0, len(fwdFor) > 0
	command := "sysctl -w " + strings.Join(restore, " ") + " >/dev/null"
	if !wantLow && !wantFwd {
		command = "rm -f " + sysctlPath + " " + sysctlBeforePath + " && " + command
		p.remove("the host's paisans sysctls", "evict: no admitted deployment needs them; set each back to what it was before admit", command)
		return nil
	}
	p.step(Step{Title: "restore " + strings.Join(restore, ", "), Describe: "evict: no other admitted deployment needs them",
		File: &hostprep.File{Path: sysctlPath, Content: SysctlFile(wantLow, wantFwd), Mode: 0o644}, Command: command, Kinds: []string{"evict"}})
	p.step(Step{Title: "record the sysctls' values before", Describe: "evict: what is left to restore", File: &hostprep.File{Path: sysctlBeforePath, Content: b.text(), Mode: 0o644}, Kinds: []string{"evict"}})
	return nil
}

// evictRecord removes the record and, last, the registry entry: until then
// the host still says the deployment is here, so a failed evict is found
// and resumed.
func evictRecord(p *planner, rec *admission.Record) error {
	path, _ := admission.Path(p.d.Token())
	if p.f.Files[path].Present {
		p.remove("the admission record", "evict: "+path, "rm -f "+path)
	}
	r, err := registry.Read(p.t)
	if err != nil {
		return err
	}
	if _, ok := r.Deployments[p.in.Request.Deployment]; ok {
		p.remove("the registry entry", "evict: deployment "+p.d.Token()+" from "+registry.Path, registry.RemoveCommand(p.in.Request.Deployment))
	}
	return nil
}

// RunSteps runs plan's steps in order and stops at the first failure,
// naming it. Evict's steps never need a rebuild; each step runs through
// the same run admit's Execute uses.
func RunSteps(t apply.Transport, plan *Plan, r ui.Reporter) error {
	for _, s := range plan.Steps {
		if err := run(t, s, r); err != nil {
			return err
		}
	}
	return nil
}
```

(`hostprep.Step` has a `Label`; Task 3's `section` drops it, so read `s.Steps` directly here as written. `udevTrigger` is Task 10's. The privileged check's message names `g` from the loop; write it to collect every group as `planUser` does.)

- [ ] **Step 5: Run, regenerate and read**

Run: `go test ./internal/hostadmit/ -run TestEvict -update && go test ./internal/hostadmit/`
Expected: PASS. Read each file under `testdata/evict/`:
* `evict-only-deployment`: disables `wg-quick@psns-f2a9` and removes its conf first, then the user, `/srv/paisans/f2a9`, the helper, the sudoers line, the egress table, the 80 and 443 rules (and not the SSH allow, with the warning), `delegate.conf`, `paisans.conf`, both sysctl files with `sysctl -w net.ipv4.ip_unprivileged_port_start=1024 net.ipv4.ip_forward=0`, the record, and the registry entry last.
* `evict-one-of-two`: the same but no `delegate.conf`, no `modules-load.d`, no sysctl step, and `present` lines naming deployment `0c0dc4e1-...` for each.
* `evict-with-data-dir`: `[ ! -L /srv/data/paisans-f2a9 ] && rm -rf --one-file-system -- /srv/data/paisans-f2a9`.
* `evict-without-record`: deletes `paisans-f2a9`.
* `evict-nothing-left`: no step.

- [ ] **Step 6: Commit**

```bash
git add internal/hostadmit
git commit -m "feat: plan host evict

Evict undoes admit for one deployment and nothing else: it stops the
mesh and removes its conf first, deletes the user with its home and its
data directory, and removes the helper, the sudoers line, the watchdog
rule, the egress table and the deployment's ufw rules. Host-wide files
and sysctls go only when no other admitted deployment needs them, each
sysctl restored to the value admit found. It deletes a user only when it
is the deployment's, so a lost request cannot delete the owner's login,
and it keeps the SSH allow, which may be the owner's own way in.

Nothing runs evict yet, so no changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 14: The commands

**Files:**
- Create: `cmd/paisans/hostadmit.go`, `cmd/paisans/hostadmit_test.go`
- Modify: `cmd/paisans/main.go` (dispatch, the `host` group's error), `cmd/paisans/help.go` (the two commands, the `host` group's line), `internal/hostadmit/local_test.go` (`allowed`)
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: `hostadmit.Early`, `Build`, `Execute`, `CheckUser`, `BuildEvict`, `RunSteps`, `Local`; `admission.Parse`, `Digest`, `ParseDigest`, `Build`, `MaxRequestSize`; `ownerTransport(dest, true)`, `siteTransport`, `config.ParseDestination`, `validate.Check`, `confirmWord`, `stdinIsTerminal`, `commonFlags`, `newFlagSet`, `parseFlags`.
- Produces: `runHostAdmit(args []string, stdin io.Reader, stdout io.Writer) error`, `runHostEvict(args []string, stdin io.Reader, stdout io.Writer) error`; test seams `localTransport func() apply.Transport`, `reachOwnerAdmit func(config.Destination) apply.Transport`, `runningAsRootOnLinux func() bool`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/paisans/hostadmit_test.go`. Use the fake transport the other `cmd/paisans` tests use (or a small one recording every command, answering nothing), and these seams:

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// noHost fails the test if anything builds a transport: the early stops
// come before admit reaches any host.
func noHost(t *testing.T) {
	t.Helper()
	savedLocal, savedOwner, savedRoot := localTransport, reachOwnerAdmit, runningAsRootOnLinux
	localTransport = func() apply.Transport { t.Fatal("the local transport was built"); return nil }
	reachOwnerAdmit = func(config.Destination) apply.Transport { t.Fatal("the owner's transport was built"); return nil }
	runningAsRootOnLinux = func() bool { return true }
	t.Cleanup(func() { localTransport, reachOwnerAdmit, runningAsRootOnLinux = savedLocal, savedOwner, savedRoot })
}

// requestFile writes site's request from the fixture to a file, as the
// operator sends it.
func requestFile(t *testing.T, site string) (path, digest string) {
	t.Helper()
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	req, err := admission.Build(cfg, site)
	if err != nil {
		t.Fatal(err)
	}
	data := admission.Encode(req)
	path = filepath.Join(t.TempDir(), site+".admit")
	os.WriteFile(path, data, 0o644)
	return path, admission.Digest(data)
}

// A data site without --watchdog, and a gateway without --public-ports,
// stop admit before any transport exists, from a request and from the
// configuration alike: exit 1, nothing read or changed. A re-run that
// drops --watchdog from an admitted data site stops the same way, since
// the stop never reads the host.
func TestAdmitStopsBeforeReachingTheHost(t *testing.T) {
	data, _ := requestFile(t, dataSiteOf(t))
	gw, _ := requestFile(t, gatewayOf(t))
	for _, args := range [][]string{
		{"--request", data},
		{"--request", data, "--execute"},
		{"--request", gw, "--watchdog"},
		{"--site", dataSiteOf(t), "--ssh", "owner@host.example.org", "--config", fixtureConfig()},
	} {
		noHost(t)
		err := runHostAdmit(args, strings.NewReader(""), new(strings.Builder))
		if err == nil || !strings.Contains(err.Error(), "Nothing was read or changed") {
			t.Errorf("%q: %v", args, err)
		}
	}
}

func TestAdmitFlagCombinations(t *testing.T) {
	path, _ := requestFile(t, "vm")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{}, "--request <file> on the host, or --site <name> --ssh"},
		{[]string{"--request", path, "--site", "vm"}, "not both"},
		{[]string{"--site", "vm", "--config", fixtureConfig()}, "--ssh"},
		{[]string{"--request", path, "--ssh", "owner@h"}, "--ssh"},
	} {
		noHost(t)
		if err := runHostAdmit(c.args, strings.NewReader(""), new(strings.Builder)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.args, err, c.want)
		}
	}
	noHost(t)
	runningAsRootOnLinux = func() bool { return false }
	if err := runHostAdmit([]string{"--request", path}, strings.NewReader(""), new(strings.Builder)); err == nil || !strings.Contains(err.Error(), "as root on the host") {
		t.Errorf("--request not as root: %v", err)
	}
}

// --execute with --request asks for the digest at a terminal, so it cannot
// be scripted past; a mistyped one changes nothing; the digest read aloud,
// in another case, spaced, with i, l and o for 1 and 0, is accepted.
func TestAdmitAsksForTheDigest(t *testing.T) {
	path, digest := requestFile(t, "vm")
	for _, c := range []struct {
		typed    string
		terminal bool
		executes bool
	}{
		{digest + "\n", false, false},
		{"0000-0000-0000-0000\n", true, false},
		{strings.ToUpper(strings.ReplaceAll(digest, "-", " ")) + "\n", true, true},
	} {
		h := withFakeLocalHost(t) // a fake that answers admit's probes as a blank host would, and records every command
		withTerminal(t, c.terminal)
		err := runHostAdmit([]string{"--request", path, "--execute"}, strings.NewReader(c.typed), new(strings.Builder))
		if executed := h.ranAny("useradd "); executed != c.executes {
			t.Errorf("typed %q at a terminal %v: executed %v (%v)", c.typed, c.terminal, executed, err)
		}
	}
}

// Evict asks for the user's name at a terminal, and refuses anything else.
func TestEvictAsksForTheUserName(t *testing.T) {
	for _, c := range []struct {
		typed    string
		executes bool
	}{{"paisans-f2a9\n", true}, {"yes\n", false}, {"\n", false}} {
		h := withFakeLocalHost(t)
		withTerminal(t, true)
		runHostEvict([]string{"--deployment", fixtureID(t), "--user", "paisans-f2a9", "--execute"}, strings.NewReader(c.typed), new(strings.Builder))
		if executed := h.ranAny("rm -f "); executed != c.executes {
			t.Errorf("typed %q: executed %v", c.typed, executed)
		}
	}
}
```

`dataSiteOf`, `gatewayOf` and `fixtureID` read the `cmd/paisans` fixture (`internal/render/testdata/deployment.yaml`) for a data site, a gateway and the id, so the tests follow the fixture rather than name its sites; `withFakeLocalHost` sets `localTransport` and `runningAsRootOnLinux`; `withTerminal` replaces the seam `stdinIsTerminal` reads through (read `cmd/paisans/terminal.go` for it; add one if it has none, as `openTTY` is replaced in `sudo_test.go`).

In `internal/hostadmit/local_test.go`, `TestOnlyHostAdmitReachesTheLocalTransport`'s `allowed` becomes:

```go
	allowed := map[string]bool{
		filepath.Join("cmd", "paisans", "hostadmit.go"):      true,
		filepath.Join("cmd", "paisans", "hostadmit_test.go"): true,
	}
```

and its comment: "so it is reached only through host admit and host evict with --request, which run on the host itself."

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/paisans/ -run 'TestAdmit|TestEvict'`
Expected: FAIL to compile, `undefined: runHostAdmit`.

- [ ] **Step 3: Implement**

Create `cmd/paisans/hostadmit.go`:

```go
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// localTransport is the transport for --request, on the host itself.
// Tests replace it.
var localTransport = hostadmit.Local

// reachOwnerAdmit is the owner's own ssh, through sudo, for --site. Tests
// replace it.
var reachOwnerAdmit = func(dest config.Destination) apply.Transport { return ownerTransport(dest, true) }

// runningAsRootOnLinux is whether this process may run a host's plan on
// the machine it runs on: --request runs only there, as root.
var runningAsRootOnLinux = func() bool { return runtime.GOOS == "linux" && os.Geteuid() == 0 }

type admitOptions struct {
	request, site, ssh, configPath string
	flags                          hostadmit.Flags
	execute                        bool
}

func runHostAdmit(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("host admit", flag.ContinueOnError)
	reporter := commonFlags(fs)
	var o admitOptions
	fs.StringVar(&o.request, "request", "", "the request file the operator sent, on this host, as root")
	fs.StringVar(&o.site, "site", "", "the site to admit, from the configuration, with --ssh")
	fs.StringVar(&o.ssh, "ssh", "", "your own login on the host, user@host[:port], with --site")
	fs.StringVar(&o.configPath, "config", config.FileName, "path to the deployment declaration, with --site")
	fs.BoolVar(&o.flags.Watchdog, "watchdog", false, "grant the host's watchdog, which can reboot it, to a data site's Patroni")
	fs.BoolVar(&o.flags.PublicPorts, "public-ports", false, "let every local user bind ports 80 to 1023, and open 80 and 443, for the toolkit's Caddy")
	fs.StringVar(&o.flags.DataDir, "data-dir", "", "keep the deployment's Docker data under this directory instead of the user's home")
	fs.BoolVar(&o.flags.BridgeNetfilter, "bridge-netfilter", false, "load br_netfilter although this host has bridges of its own")
	fs.BoolVar(&o.flags.MeshLAN, "mesh-lan", false, "let the mesh dial this host's own LAN")
	fs.BoolVar(&o.execute, "execute", false, "make the changes the plan shows")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("host admit takes no argument. Got: %s", quoteAll(fs.Args()))
	}
	switch {
	case o.request == "" && o.site == "":
		return fmt.Errorf("host admit: give --request <file> on the host, or --site <name> --ssh <your login> from a workstation")
	case o.request != "" && o.site != "":
		return fmt.Errorf("host admit: --request or --site, not both")
	case o.site != "" && o.ssh == "":
		return fmt.Errorf("host admit: --site needs --ssh, your own login on the host (user@host[:port]), which runs admit through sudo")
	case o.request != "" && o.ssh != "":
		return fmt.Errorf("host admit: --request runs on the host itself and takes no --ssh")
	case o.request != "" && !runningAsRootOnLinux():
		return fmt.Errorf("host admit: --request runs as root on the host it admits to: copy the request there and run sudo paisans host admit --request <file>")
	}
	r := reporter()
	in := hostadmit.Input{Flags: o.flags, Now: time.Now()}
	var declared config.Site
	var cfg *config.Config
	if o.request != "" {
		data, err := readRequest(o.request)
		if err != nil {
			return err
		}
		if in.Request, err = admission.Parse(data); err != nil {
			return fmt.Errorf("host admit: %s: %w", o.request, err)
		}
		in.Digest = admission.Digest(data)
	} else {
		var err error
		if cfg, err = config.Load(o.configPath); err != nil {
			return err
		}
		if result := validate.Check(cfg); result.Refused() {
			reportFindings(r, o.configPath, result)
			return refused(o.configPath, len(result.Refusals()), "")
		}
		if in.Request, err = admission.Build(cfg, o.site); err != nil {
			return fmt.Errorf("host admit: %w", err)
		}
		declared = cfg.Sites[o.site]
	}
	// The grants first, before any transport exists: the owner's decision
	// needs nothing from the host.
	if err := hostadmit.Early(in); err != nil {
		return err
	}
	var t apply.Transport
	if o.request != "" {
		// The owner's environment never points admit's Docker commands
		// at another daemon; over ssh, sudo's env_reset does the same.
		os.Unsetenv("DOCKER_HOST")
		os.Unsetenv("DOCKER_CONTEXT")
		t = localTransport()
		r.Result("request digest %s: compare it with the one the operator gave you, over a channel you both trust, such as a call", in.Digest)
	} else {
		dest, err := config.ParseDestination(o.ssh)
		if err != nil {
			return sshFlagProblem(o.ssh, err)
		}
		t = reachOwnerAdmit(dest)
	}
	r.Section(fmt.Sprintf("admit %s on %s", in.Request.User, t.Describe()))
	plan, err := ui.Get(r, "read the host", func() (*hostadmit.Plan, error) { return hostadmit.Build(t, in) })
	if err != nil {
		return err
	}
	if !o.execute || r.Verbose() {
		plan.Show(r)
	} else {
		plan.Warn(r)
	}
	if !o.execute {
		r.Result("Nothing was changed. Run it again with --execute to make these changes.")
		return nil
	}
	if o.request != "" {
		if err := confirmDigest(stdin, stdout, in.Digest); err != nil {
			return err
		}
	}
	if err := hostadmit.Execute(t, in, plan, r); err != nil {
		return err
	}
	version, err := ui.Get(r, "check "+in.Request.User+"'s Docker", func() (string, error) { return hostadmit.CheckUser(t, in.Request) })
	if err != nil {
		return err
	}
	if o.site != "" {
		// The operators' first login as the user, which records ssh.user.
		login := siteTransport(o.site, declared, "")
		if err := ui.Run(r, "log in as "+in.Request.User, func() error {
			_, err := login.Run("docker version --format '{{.Server.Version}}'")
			return err
		}); err != nil {
			return err
		}
	}
	r.Result("Admitted: %s runs Docker %s. The operators' next command logs in as %s.", in.Request.User, version, in.Request.User)
	return nil
}

// readRequest reads a request file, refusing one larger than any request.
func readRequest(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("host admit: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, admission.MaxRequestSize+1))
	if err != nil {
		return nil, fmt.Errorf("host admit: reading %s: %w", path, err)
	}
	if len(data) > admission.MaxRequestSize {
		return nil, fmt.Errorf("host admit: %s is larger than any request (%d bytes)", path, admission.MaxRequestSize)
	}
	return data, nil
}

// confirmDigest asks for the request's digest at a terminal and refuses
// anything that is not it, read as ParseDigest reads it. There is no flag
// to answer it, so it cannot be scripted past.
func confirmDigest(stdin io.Reader, stdout io.Writer, digest string) error {
	if !stdinIsTerminal(stdin) {
		return fmt.Errorf("host admit: --execute with --request asks you to type the request's digest at a terminal, and stdin is not one. Run it from an interactive shell. Nothing was changed")
	}
	fmt.Fprintf(stdout, "\nThe request carries the keys that will log in to this machine. Type the digest the operator gave you, over a channel you trust: ")
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && line == "" {
		return fmt.Errorf("host admit: no digest read. Nothing was changed")
	}
	typed, err := admission.ParseDigest(strings.TrimSpace(line))
	if err != nil || typed != digest {
		return fmt.Errorf("host admit: that is not this request's digest, %s. The request may have been changed on its way: ask the operator for it again. Nothing was changed", digest)
	}
	return nil
}

func runHostEvict(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("host evict", flag.ContinueOnError)
	reporter := commonFlags(fs)
	request := fs.String("request", "", "the request the deployment was admitted from, on this host, as root")
	id := fs.String("deployment", "", "the deployment's id, with --user, on this host, as root")
	user := fs.String("user", "", "the deployment's user, with --deployment")
	site := fs.String("site", "", "the site to evict, from the configuration, with --ssh")
	sshFlag := fs.String("ssh", "", "your own login on the host, user@host[:port], with --site")
	configPath := fs.String("config", config.FileName, "path to the deployment declaration, with --site")
	execute := fs.Bool("execute", false, "make the changes the plan shows")
	if err := fs.Parse(args); err != nil {
		return err
	}
	modes := 0
	for _, set := range []bool{*request != "", *id != "" || *user != "", *site != ""} {
		if set {
			modes++
		}
	}
	switch {
	case modes != 1:
		return fmt.Errorf("host evict: give one of --request <file>, --deployment <id> --user <name> (both on the host), or --site <name> --ssh <your login>")
	case (*id == "") != (*user == ""):
		return fmt.Errorf("host evict: --deployment and --user go together")
	case *site != "" && *sshFlag == "":
		return fmt.Errorf("host evict: --site needs --ssh, your own login on the host")
	case *site == "" && *sshFlag != "":
		return fmt.Errorf("host evict: --ssh goes with --site")
	case *site == "" && !runningAsRootOnLinux():
		return fmt.Errorf("host evict: --request and --deployment run as root on the host itself: run sudo paisans host evict there")
	}
	r := reporter()
	var in hostadmit.EvictInput
	var t apply.Transport
	switch {
	case *request != "":
		data, err := readRequest(*request)
		if err != nil {
			return err
		}
		req, err := admission.Parse(data)
		if err != nil {
			return fmt.Errorf("host evict: %s: %w", *request, err)
		}
		in = hostadmit.EvictInput{Deployment: req.Deployment, User: req.User}
	case *id != "":
		in = hostadmit.EvictInput{Deployment: *id, User: *user}
	default:
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		s, ok := cfg.Sites[*site]
		if !ok {
			return fmt.Errorf("host evict: %s declares no site %q", *configPath, *site)
		}
		in = hostadmit.EvictInput{Deployment: cfg.ID, User: s.SSHUser()}
	}
	if *site != "" {
		dest, err := config.ParseDestination(*sshFlag)
		if err != nil {
			return sshFlagProblem(*sshFlag, err)
		}
		t = reachOwnerAdmit(dest)
	} else {
		os.Unsetenv("DOCKER_HOST")
		os.Unsetenv("DOCKER_CONTEXT")
		t = localTransport()
	}
	r.Section(fmt.Sprintf("evict %s from %s", in.User, t.Describe()))
	plan, err := ui.Get(r, "read the host", func() (*hostadmit.Plan, error) { return hostadmit.BuildEvict(t, in) })
	if err != nil {
		return err
	}
	plan.Show(r)
	if !*execute {
		r.Result("Nothing was changed. Run it again with --execute to evict.")
		return nil
	}
	if len(plan.Steps) == 0 {
		r.Result("Nothing of this deployment is left on %s.", t.Describe())
		return nil
	}
	what := ui.Danger(stdout, fmt.Sprintf("This deletes %s with its home, its Docker data and everything of deployment %s on %s. Nothing brings it back.", in.User, in.Deployment, t.Describe()))
	if err := confirmWord(stdin, stdout, in.User, what); err != nil {
		return fmt.Errorf("host evict: %w", err)
	}
	if err := hostadmit.RunSteps(t, plan, r); err != nil {
		return err
	}
	r.Result("Evicted: nothing of deployment %s is left on %s but what the plan said it kept.", in.Deployment, t.Describe())
	return nil
}
```

In `cmd/paisans/main.go`'s `host` switch, add:

```go
		case len(os.Args) >= 3 && os.Args[2] == "admit":
			err = runHostAdmit(os.Args[3:], os.Stdin, os.Stdout)
		case len(os.Args) >= 3 && os.Args[2] == "evict":
			err = runHostEvict(os.Args[3:], os.Stdin, os.Stdout)
```

and make the group's error "host takes one subcommand: prepare, deployments, request, admit or evict".

In `cmd/paisans/help.go`, the `host` group's line becomes "Prepare a host, list what is on it, request admission, or admit and evict a deployment.", and add after `host request`:

```go
	{
		name:    "host admit",
		summary: "Admit a deployment to this host, as its owner.",
		usage: []string{
			"sudo paisans host admit --request <file> [--watchdog] [--public-ports] [--data-dir <path>]\n                        [--bridge-netfilter] [--mesh-lan] [--execute]",
			"paisans host admit --site <name> --ssh <user@host[:port]> [--config " + config.FileName + "] [the same grants]\n                   [--execute]",
		},
		detail: "The one root step for a deployment on a host, run by the host's owner: the deployment's own unprivileged user with its own rootless Docker, the WireGuard helper and its sudoers line, the firewall, the egress table, the grants given and the admission record. " +
			"--watchdog and --public-ports are the owner's to give; a data site without --watchdog, or a gateway or Caddy monitor without --public-ports, stops before anything is read. " +
			"--execute with --request asks for the request's digest at a terminal. Never run by an agent unattended.",
	},
	{
		name:    "host evict",
		summary: "Remove a deployment from this host, as its owner.",
		usage: []string{
			"sudo paisans host evict --request <file> [--execute]",
			"sudo paisans host evict --deployment <id> --user <name> [--execute]",
			"paisans host evict --site <name> --ssh <user@host[:port]> [--config " + config.FileName + "] [--execute]",
		},
		detail: "Undo host admit for one deployment: its mesh, its user with its home and data, the helper, the egress table, its firewall rules, the record and the registry entry. Host-wide files and sysctls go only when no other deployment is admitted. " +
			"--execute asks for the user's name at a terminal. Never run by an agent unattended.",
	},
```

(Match the struct's field names and the help tests' expectations, which `cmd/paisans/help_test.go` checks for every command the dispatcher knows.)

In `CHANGELOG.md`, under `[Unreleased]`, `### Added`:

```markdown
- `host admit` and `host evict` let a host's owner admit a deployment as its own unprivileged user with rootless Docker, from a request on the host or over their own SSH, and undo it.
```

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/paisans/ ./internal/hostadmit/ && go vet ./...`
Expected: PASS, including `TestOnlyHostAdmitReachesTheLocalTransport` and `TestOnlyTheOwnersTransportRunsThroughSudo` (admit reuses `ownerTransport`, already allowed).

- [ ] **Step 5: Commit**

```bash
git add cmd/paisans internal/hostadmit/local_test.go CHANGELOG.md
git commit -m "feat: add host admit and host evict

The host's owner admits a deployment once, as root, and everything after
runs as the deployment's own user. Admit runs on the host from a request,
through the local transport, or from a workstation through the owner's
own ssh and sudo; the grants the site needs stop it before any host is
reached, and --execute with a request asks for the digest at a terminal,
so a request changed on its way cannot be scripted past. Evict undoes it
for one deployment and asks for the user's name.

Founder decision.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 15: The documentation

**Files:**
- Modify: `docs/commands.md` (after `paisans host request`), `README.md` (the registry passage, under the registry section), `docs/deployment-agent-rules.md` (*Blast radius*, *Never without the human present*)

**Interfaces:**
- Consumes: Task 14's flags, exactly as `runHostAdmit` and `runHostEvict` define them.
- Produces: nothing code reads.

- [ ] **Step 1: `docs/commands.md`**

After the `paisans host request` entry, in its style (*Alone*, *Flags*, *Hosts*, *`--execute`*), add entries for `paisans host admit` and `paisans host evict`:

````markdown
### `paisans host admit`

The host's owner admits one site of a deployment to their machine: the one
step that runs as root. It makes the deployment's own unprivileged user,
with its own rootless Docker, and everything that user cannot do itself.

```sh
sudo paisans host admit --request home-a.admit                                  # shows what it would do
sudo paisans host admit --request home-a.admit --watchdog --public-ports --execute
paisans host admit --site home-a --ssh owner@home-a.example.org --execute       # from a workstation
```

* **Alone:** always: run by the host's owner, never by an agent unattended.
* **Flags:** `--request <file>` (on the host, as root) or `--site <name>`
  with `--ssh <your login>`; `--config`; the grants `--watchdog` and
  `--public-ports`; `--data-dir <path>`, `--bridge-netfilter`, `--mesh-lan`;
  `--execute`.
* **Hosts:** the one it runs on, or the one `--ssh` names, through sudo.
  A data site without `--watchdog`, or a gateway or Caddy monitor without
  `--public-ports`, stops it before anything is read; so does a grant the
  site does not need.
* **`--execute`:** makes the changes the plan shows; with `--request` it
  first asks for the request's digest at a terminal.
* **What it changes:** Docker from Docker's repository where absent (an
  Engine older than 29.5 is refused, never upgraded); the user, with linger
  and its rootless daemon; the cgroup delegation drop-in; the egress table;
  `/srv/paisans/<token>`; `/etc/wireguard`; the WireGuard helper and its
  sudoers line; ufw rules tagged `paisans-<token>:`; with `--watchdog`, the
  device for the user; with `--public-ports`, `ip_unprivileged_port_start`
  of 80; for a site with an endpoint, forwarding; the admission record; the
  registry claim; the operators' keys in the user's `authorized_keys`.

### `paisans host evict`

Undoes `host admit` for one deployment, leaving every other deployment and
the owner's own services as they were.

```sh
sudo paisans host evict --request home-a.admit --execute
sudo paisans host evict --deployment <id> --user paisans-1a2b --execute   # without the request
```

* **Alone:** always, by the host's owner, never by an agent unattended.
* **Flags:** `--request <file>`, or `--deployment <id> --user <name>` (both
  on the host, as root), or `--site <name> --ssh <your login>`; `--config`;
  `--execute`.
* **Hosts:** the one it runs on, or the one `--ssh` names, through sudo.
  Deletes a user only when it is the deployment's: the record's, or with no
  record, `paisans-<token>`.
* **`--execute`:** asks for the user's name at a terminal, then removes the
  mesh, the user with its home and Docker data, the deployment's directory,
  the helper and sudoers line, the egress table and its firewall rules (the
  SSH allow is kept, with a warning), the record and the registry entry;
  host-wide files and sysctls only when no other deployment is admitted,
  each sysctl restored to what it was.
````

- [ ] **Step 2: `README.md`**

Replace "`--execute` they claim through sudo, since the registry is root's." at the end of the registry passage with:

```markdown
`--execute` they claim it, which only root can: `paisans host admit` claims
the host for the deployment when its owner admits it, from the request alone,
so a host admitted from a request and one admitted from the configuration
hold the same entry.
```

and read the passage around it, editing any other sentence that says a deployment command claims through sudo so it describes what `host admit` does. Describe what is, not what was.

- [ ] **Step 3: `docs/deployment-agent-rules.md`**

Under **Never without the human present**, add:

```markdown
* Admitting a deployment to a host or evicting one (`paisans host admit`,
  `paisans host evict`): it is the host owner's root step on their own
  machine
```

This places two actions in a tier, which the index's *Global Constraints* record as already decided ("`docs/deployment-agent-rules.md` says an agent never runs them unattended"); say so in the commit body.

- [ ] **Step 4: Check the docs**

Run: `git diff docs README.md | perl -CSD -ne 'print if /^\+.*\x{2014}/'`
Expected: nothing.

Run: `grep -n "host admit\|host evict" docs/commands.md README.md docs/deployment-agent-rules.md`
Expected: the two entries, the README passage, the rule. Every flag named is one Task 14 defines (`--request`, `--site`, `--ssh`, `--config`, `--watchdog`, `--public-ports`, `--data-dir`, `--bridge-netfilter`, `--mesh-lan`, `--execute`, `--deployment`, `--user`).

- [ ] **Step 5: Run the whole suite**

Run: `gofmt -l cmd internal && go vet ./... && go test ./...`
Expected: nothing listed; PASS.

Run: `git diff origin/develop --stat && git diff origin/develop | perl -CSD -ne 'print if /^\+.*\x{2014}/'`
Expected: this plan's files; no em dash.

- [ ] **Step 6: Commit**

```bash
git add docs/commands.md README.md docs/deployment-agent-rules.md
git commit -m "kacl-skip: docs: describe host admit and host evict

The commands reference names both commands, their flags and what each
changes on a host; the README says the registry claim is admit's, from
the request alone; and the deployment agent rules put admitting and
evicting in the tier a human is present for, since they are the host
owner's root step on their own machine.

Founder decision, recorded in the rootless phase index's constraints.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Self-Review Notes

* **Spec coverage.** *What it does*: Docker, step 1 (Task 4); AppArmor, step 2 (Task 4); the user and its subordinate IDs, step 3 (Task 5); linger, step 4 (Task 5); delegation, step 5, written before the manager starts and never restarting a running one (Task 5); `br_netfilter` and the rootless daemon with `daemon.json`, step 6 (Task 7); the deployment's directory, step 7 (Task 8); `/etc/wireguard`, the helper and its sudoers line, step 8 (Task 8); the firewall without `br-+`, step 9 (Task 9); egress, step 10 (Task 6); the watchdog with the udev `GROUP`, step 11 (Task 10); the low ports with the 80/443 holders, step 12 (Tasks 9 and 10); forwarding, step 13 (Task 10); the record, step 14 (Task 12); the claim, step 15 (Task 12); the keys, step 16 (Task 12); the check, step 17 (Task 12's `CheckUser` and Task 14's login). *`paisans host admit`*: the two ways, `--ssh` with `--site`, `--request` only as root on the host (Task 14); grants opt-in and the early stops (Task 3); `--data-dir` (Task 7). *`paisans host request`*: the typed digest (Task 14). *Running on the host*: the local transport reached only by admit and evict (Task 14's guard update). *`paisans host evict`*: every item of its list, plus stopping `wg-quick` and removing the conf (index), with the host-wide rule (Task 13). *Egress*: both chains, `--mesh-lan` (Tasks 2 and 6). *Behind the owner's web server*: detection and the admin API warning (Task 11). *Tests*: golden plans for a blank Ubuntu 24.04, an old and a current system Docker and each flag (Tasks 4 to 12); the egress table renders the UID and range (Task 6); admit refuses a mistyped digest (Task 14); the same steps from `--request` as from `--site` (Task 12); the three 80/443 holders (Task 9); the three `br_netfilter` plans (Task 7). *Observed*: R1 and R20 (Task 4's floor), R4 (Task 10's `GROUP`), R5 (Task 5), R6 (Task 7), R10 (ruling 22), R14 and R15 (Task 5), R18 (Task 4). *Documentation*: the commands and the agent rule (Task 15); the host owners' guide is phase 9's. Phase 4's handoff: the typed digest (Task 14), the existing user not named `paisans-<token>` shown plainly (Task 5's warning), and the mesh subnet against the host's routes (Task 8, ruling 32).
* **Review Focus.** Each of the five lines has its test in the owning task: the admitted gateway's own Caddy (Task 9, `TestOwnListenerIsNotAHolder`, golden `ports-own-caddy`); paths the user controls (Task 3, `TestNoRootStepTouchesTheUsersPaths`, which every later task's cases feed; Task 12, `TestAPlantedSSHLinkLeavesItsTargetAlone`; Task 7, `TestTheDataDirectoryParentMustBeRoots`); evict naming another account (Task 13, `TestEvictRefusesAnotherUser`); the blank host's provisional steps (Task 3, `TestExecuteRebuildsOnceTheUserExists`, `TestExecuteStopsWhenTheRebuiltPlanDiffers`); the data root moving under data (Task 7, `TestChangingTheDataDirWithDataIsRefused`, `TestARunningDaemonIsNotRestarted`). Each is written before its code and seen failing in the task's Step 2.
* **Index coverage.** Every phase 5 bullet: the package's files (Tasks 3 to 13; `sysctl.go` and `watchdog.go` are Task 10, `evict.go` Task 13), the two snippets in `internal/hostprep/snippets` (Task 6), the command with `--request`, `--site --ssh`, the typed digest and typed user name (Task 14), every golden plan the index lists (blank: `docker-blank`; old Docker: `docker-old-refused`; current: `docker-current`; snap: `docker-snap-refused`; each grant: `watchdog-*`, `ports-free`, `sysctl-*`; a re-run with a grant removed: `rerun-public-ports-removed`; a privileged user: `TestAPrivilegedUserIsRefused`; a second watchdog holder: `watchdog-second-holder-refused`; evict of one of two: `evict-one-of-two`; evict's `wg-quick`, conf and data dir: `evict-only-deployment`, `evict-with-data-dir`), files in the home as the user with the planted `~/.ssh` test (Tasks 3 and 12), the record's mode (Task 12), the 80/443 holders (Task 9), the web server (Task 11), the watchdog and public-ports early stops with their golden tests (Task 3), `validate` refusing `watchdog: off` (Task 1), `--data-dir` (Task 7), `br_netfilter` (Task 7), the mark (Tasks 2 and 6), the helper's install (Task 8), `/etc/wireguard` (Task 8), `ip_forward` (Task 10), R10 (ruling 22), the README registry passage (Task 15).
* **Left to later phases, on purpose.** `host prepare` comparing the record with the declaration, commands logging in as the user, doctor's checks, the gateway's `/.well-known/paisans/<token>`, `web-server show` from the record, and pending sites are phases 6 and 7. The host owners' guide and the rest of `docs/commands.md` are phase 9. End to end on VMs, and whether `--bridge-netfilter` is needed at all, is phase 10.
* **Type consistency.** `Flags`, `Input`, `Step`, `Plan`, `planner`, `part`, `parts` (Task 3) are what every part uses; `asUser`, `uidOf`, `rangeOf`, `groupOf` (Tasks 3, 5, 7); `previous` (Task 7) is what Tasks 10, 12 and 13 read; `others` (Task 10) is what Task 13 reads; `sha` (Task 5, in `facts.go`) and `sum` (tests) are the same hex SHA-256; path helpers live in `facts.go` beside the probe that reads them; `systemDocker` (Task 4) prefixes every system-Docker command; `udevTrigger` (Task 10) is evict's too.
* **Placeholder scan.** No step says "add error handling" or "similar to". Three listings say "check this against the code" where the plan cannot see a value (the `validate.Finding` field names in Task 1; `webserver.Detect`'s command strings in Task 11; `hostprep`'s sidecar header in Task 12); each names the file to read.
