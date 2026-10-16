# Rootless Phase 6: Commands as the Deployment's User Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every command but `host admit`, `host evict` and `host deployments` runs on an admitted host as the deployment's own user, against its own rootless Docker, reading and writing only under `/srv/paisans/<token>/` and the user's home; and a deployment with sites pending or down runs on the sites it has.

**Architecture:** Phases 2, 3 and 5 left every deployment command logging in as `paisans-<token>` with `DOCKER_HOST` at its rootless socket, the mesh behind the WireGuard helper, and `host admit` doing every root step and leaving an admission record. This phase moves every path a deployment command reads or writes off root's directories and under `deployment.Root()` (a new `deployment.StateDir()` for the toolkit's own records), turns `host prepare` into a check of the admission record plus the user's own files and units, makes the host check, volume prune and the mesh probe work without root, replaces every registry claim and registry read in a deployment command with the admission record, hands a site behind the owner's web server a reference snippet instead of writing into the owner's configuration, adds the gateway's self-check from outside, and adds a classification of sites (`internal/topology`) that every run computes once and renders the effective topology from.

**Tech Stack:** Go 1.25, `text/template` with `embed`, POSIX sh, systemd user units under linger, `ss` from iproute2, `docker system df -v`, `rootlesskit` from `docker-ce-rootless-extras`, Caddy's Caddyfile, `net/http` with `crypto/tls`, the existing `internal/apply`, `internal/hostprep`, `internal/hostcheck`, `internal/admission`, `internal/render`, `internal/siteremove`, `internal/webserver` and `cmd/paisans` packages.

**Spec:** `docs/specs/2026-10-15-rootless-and-host-admit.md` (every section; *What runs as the deployment's user*, *Behind the owner's web server*, *A deployment with sites missing*, *Egress*, *Keys* and *Tests* govern this phase), the phase index `docs/superpowers/plans/2026-10-15-rootless-and-host-admit.md`, *Phase 6*, read beside *Phase 7* and *Phase 10*, and phase 5's plan `docs/superpowers/plans/2026-10-15-rootless-phase-5-host-admit.md`, *What this phase hands to phases 6, 7 and 10* and *Founder decisions on the plan's questions*.

## Global Constraints

* This phase starts once phase 5 has merged into `develop`, with `feat/rootless-user-commands` rebased onto it. Phase 5 is unmerged as this plan is written; every phase 5 interface it uses is named as phase 5's plan names it: `admission.Request.Site` and `Roles` (phase 5 Task 2b), `admission.NewRecord`, `EncodeRecord`, `ParseRecord`, `Read`, `Path`, `Dir`, `ErrNotAdmitted` (phase 4, extended by phase 5), `hostadmit.Build`, `Local`, `PrivilegedGroups`, `daemonJSON(p *planner) (string, error)` and its `daemonConfig` and `addressPool` in `internal/hostadmit/daemon.go` (phase 5 Task 7), the sidecar exports `hostprep.OwnedKey`, `ParseOwnedKeys`, `OwnedKeysFile` in `internal/hostprep/admit.go` (phase 5 Task 12), and whatever of `hostprep.AdmitPackages`, `PlanWatchdog`, `AdmitRules` phase 5's `internal/hostprep/admit.go` exports. Where phase 5 merged under another name, use the merged name and say so in the commit.
* "Rootless on every site; no code path keeps the sudo transport for a deployment command." (index) "Every command except `host admit`, `host evict` and `host deployments`." (spec, *What runs as the deployment's user*) `cmd/paisans/sudo_guard_test.go` keeps passing unchanged.
* "Default user `paisans-<token>` (`deployment.Prefix()`); `ssh.user` overrides." (index)
* "Docker Engine 29.5 or later." (index)
* "No migration code: existing deployments are redeployed." (index) No code reads, converts or cleans `/var/lib/paisans/deployed.*`, `/var/lib/paisans/config`, `/srv/caddy.d`, `/etc/paisans/authorized_keys.*.owned` or a system unit named `paisans-<token>-*`; a root-run deployment is redeployed.
* "`host prepare` installs nothing and touches no root file. It checks the admission record against the declaration, writes the user's `authorized_keys`, the config store and its user units, and the daemon's `daemon.json`." (spec)
* "`hostcheck.Inspect` stops requiring UID 0. It inventories the user's own daemon, and reads listening ports with `ss -Hltnu` (no `-p`), so a port held by another user is reported as taken without naming its process. The ufw probe goes; admit owns the firewall." (spec)
* "Volume prune stops reading the volume's mountpoint with `du` [...] It uses `docker system df -v`." (spec)
* The table in *What runs as the deployment's user*: records under `/srv/paisans/<token>/.paisans/`, the config store under `/srv/paisans/<token>/.paisans/config/`, its units the user's under linger, its script under `/srv/paisans/<token>/.paisans/bin/`, `/srv/caddy.d` to `/srv/paisans/<token>/caddy.d`, the mesh probe reading `~/.config/docker/daemon.json`. (spec)
* "The sidecar moves from `/etc/paisans/authorized_keys.<user>.paisans-<token>.owned` to `/srv/paisans/<token>/.paisans/authorized_keys.owned`." (spec, *Keys*)
* "The gateway's Caddy serves `/.well-known/paisans/<token>`, answering with the deployment's token, and `apply` ends the gateway's step by fetching it through the community's public address on 443 (and on 80, expecting the redirect). Any other answer is a conflict: the step fails with \"443 on <site> answers, but not as this deployment\", whatever the host check saw." (spec)
* *Behind the owner's web server*: "`include_dir` is refused by `validate`"; "`apply` runs the app on `listen`, writes the snippet, filled in from the admission record's detection, to `/srv/paisans/<token>/web-server/<app>.<ext>` for reference, and runs `web-server check` from outside. Until the check passes, the web server step ends on a warning naming `paisans web-server show --app <app>` for the owner; when the reference file changes, the warning says the owner's copy needs replacing"; "`web-server show` prints the sheet from the admission record"; "`app remove` and `site remove` say which block the owner removes." (spec)
* *A deployment with sites missing*, in full: the classification table, "Whether a site has joined is read from the deployment, never from the site's own host", "A down site is never removed by a run", the down line's exact wording, "Pending sites are left out as if not yet added", the four refusals, the `pending` section, "`Applied, degraded: <n> sites pending or down (<names>)`, exit 0. `--require-all` makes a degraded run exit 1", "The declaration stays the only record", "Each site's manifest records which sites were pending". (spec)
* "`site remove` does what the deployment's user can, then ends by printing the `host evict` line for the owner." (spec)
* "`host deployments` stays an owner's command with `--ssh` and sudo, and lists admission records beside what it lists today." "`host deployments --sudo=false` lists admission records without the registry or directories." (spec; index)
* "Docker-tagged tests run against a rootless daemon." (index)
* No em dashes anywhere: code, comments, docs, commit messages. Docs describe current state only; no "no longer" or "was" wording; no rejected alternatives outside `docs/decisions.md`. `docs/specs/` and `docs/superpowers/` are records and are not edited by these tasks, except this plan.
* Commits: `<type>: <lowercase subject>`, a body saying why, `Founder decision.` where it was one, the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. A behaviour-changing commit adds one `CHANGELOG.md` bullet under `[Unreleased]`, one sentence, at most 35 words; a commit with nothing an operator would notice adds none and says so in its body.
* Never `git stash`; other sessions and parallel worktrees share the stash. Never push.
* Every review finding is fixed before merge; Minor findings are batched to the final whole-branch review.
* `README.md`, `docs/commands.md` and the web server guide are edited in Tasks 21 and 22 only, after the code they describe, so tasks run in parallel never edit the same document; a code task edits help text in `cmd/paisans/help.go` where its flags or paths change.
* Implementers run the touched packages during TDD (`go test ./internal/<pkg>/ ./cmd/paisans/`) and `go vet ./... && go test ./...` once at the end of each task, before its last commit.

## Review Focus

The index's Review Focus lines all belong to phases 2 to 5. The five below are what this phase's spec implies and no line of it tests, most likely first; each is pinned by a named test in the task that owns the code.

* **A pending Garage site turns an S3 app into a local-storage app.** `blueprints.InObjectStorage` defaults an app to S3 only while `len(cfg.Storage.Garage.Sites) > 0`, so an effective topology that drops every Garage site would render that app's uploads onto local disk, silently, and the next apply with Garage back would leave them stranded. The effective topology must hold such an app back instead (Task 17: `TestEffectiveTopologyNeverMovesAnAppToLocalStorage`).
* **Evidence that could not be read makes a joined site pending.** No gateway answers, so the deployment record that lists the joined sites cannot be read: a site whose login fails must then never be dropped from the mesh and the cluster as pending; the run refuses instead, naming what it could not read, and a site etcd lists is down whatever the record says (Task 17: `TestUnreadableEvidenceNeverMakesAJoinedSitePending`).
* **The deployment's own listener read as another user's.** A rootless container's process runs as the user's UID or one in its subordinate range, so a re-apply of a running gateway finds 80 and 443 held; the host check must count those as this deployment's, and still count a listener of root's or another user's as taken (Task 5: `TestTheUsersOwnListenersAreOurs`).
* **The gateway's self-check passes against a server that is not this deployment's.** Another web server answering 200 with its own page, a 404, a redirect on 443 (which must never be followed to a page that happens to hold the token), port 80 redirecting to another host, or port 80 serving instead of redirecting: each must fail the step with the spec's line (Task 14: `TestSelfCheckRefusesAnotherServer`).
* **Site remove, run as the user, reaching outside the deployment's root.** A manifest written by an earlier root-run deployment can list a path outside `/srv/paisans/<token>/` (`etc/...`); the user cannot delete it and must not try, and a `--delete-data` run must empty the root through `rootlesskit` without trying to remove the root directory itself, which is root's to remove (Task 12: `TestSiteRemoveNeverReachesOutsideTheRoot`, `TestDeleteDataEmptiesTheRootInTheUsersNamespace`).

## Rulings

Where the spec, the index or the code leave a choice open, or disagree, this plan decides as follows. Each is restated where its task implements it.

1. **`deployment.StateDir()` is `/srv/paisans/<token>/.paisans`**, mode 700, the user's. Every record the toolkit keeps on a host for itself lives there, named without the token since the directory is the deployment's: `deployed.json`, `granted.json`, `gateway-move.json`, `authorized_keys.owned`, `lock`, `config/`, `bin/`. The manifest stays at `Root()/.paisans-manifest.json`, where apply already writes it.
2. **One lock per deployment.** The records' writers serialise on `flock` over `StateDir()/lock` instead of `/var/lib/paisans/registry.lock`: only this deployment's user writes them, so a host-wide lock buys nothing, and the user cannot open root's.
3. **The deployed record is the user's, mode 0644 inside a 700 directory**, and is read as the user with `ReadFile`; the "read through sudo instead" fallback (`deployrecord.ReadAsUser`) is removed, since there is no other reader.
4. **`render.HostSitesDir` becomes a function of the deployment**, `render.HostSitesDir(d)` = `Root()/caddy.d`, created by apply as the user, mode 755. The gateway's Caddy mounts it at `/etc/caddy.d` as today.
5. **The config store's git daemon runs as the deployment's user**, from the user unit `paisans-<token>-config-daemon.service` with `Restart=always`, `RestartSec=10` and `StartLimitIntervalSec=0`, `WantedBy=default.target`. A user unit cannot be ordered against the system unit `wg-quick@psns-<token>`, so the daemon retries until the mesh address exists instead of being started by it. Repositories keep their names (`<token>.git` under the base path), so peers fetch the same URL as before.
6. **User units are written as files in the user's home**, `<home>/.config/systemd/user/`, the home read once per run from `getent passwd "$(id -un)"`, and every `systemctl --user` runs with `XDG_RUNTIME_DIR=/run/user/$(id -u)`, which a non-interactive ssh session does not always set.
7. **One comparison of the record with the declaration, `admission.Record.Differences`**: the site's name, the user, the SSH port, the WireGuard port, forwarding (on exactly when the site has an endpoint), each grant its roles need, and, for a site with `managed_by: external`, the web server admit detected. `host prepare` refuses on any difference, naming each and the two lines that fix it: `paisans host request --site <s> > <s>.admit` for the operator and `sudo paisans host admit --request <s>.admit` for the owner.
8. **`host prepare`'s watchdog part only reads.** On a data site it checks that `/dev/watchdog` is owned by the user (`ls -ln`), that no other daemon holds it and that systemd's `RuntimeWatchdogUSec` is 0, as today, and with `watchdog: required` that the device is not softdog; anything else refuses with the `host admit` line. It loads no module and writes no unit.
9. **`host prepare` writes `daemon.json` from the function admit uses.** Phase 5's `daemonJSON` moves to `hostprep.DaemonJSON(meshSubnet, dataDir string) (string, error)`, and both commands call it, so they never disagree. `host prepare` writes it as the user only when it differs, restarts the user's daemon only when it runs no container, and otherwise warns, as admit does.
10. **The host check attributes a listener by UID.** It runs `ss -Hltnue`: the extended output carries `uid:<n>` for a socket owned by a user other than root, and none for root's (iproute2, `misc/ss.c`, `sock_details_print`: `if (s->uid) out(" uid:%u", s->uid)`). A socket owned by the user's UID or one in its subordinate range (`/etc/subuid`, world readable) is this deployment's; any other is taken, described by its UID. Phase 10 observes the `uid:` field on staging; R7 observed only that `ss -Hltnu` lists other users' sockets.
11. **The host check drops its ufw, firewalld and registry probes, and the cgroup probe.** Admit owns the firewall; the registry is root's; without `-p` there are no PIDs to map to cgroups. `Shared()` still reports a host where a listener of another user's sits beside this deployment, which is what keeps `apply` from removing an image a foreign project may run.
12. **Volume prune reads sizes from `docker system df -v --format '{{json .Volumes}}'`**, whose `Size` is Docker's human-readable decimal size (`48.38MB`, observed on Engine 29.7.2), parsed back to bytes; labels still come from `docker volume inspect`, since `df` joins them into one string that a comma in a value would make ambiguous.
13. **The mesh probe has two forms.** `mesh.Probe` keeps reading the system daemon's `/etc/docker/daemon.json`, which is what `host admit` checks as root; the new `mesh.ProbeUser` reads the user's `~/.config/docker/daemon.json` through the user's own shell, and every deployment command uses it.
14. **The admission record names its site and roles.** `admission.Record` gains `site` and `roles`, filled by `NewRecord` from the request, which phase 5's founder decision made carry both; `ParseRecord` requires them. Nothing is released, so `RecordVersion` stays 1. `site remove --force --id` and `host deployments` read them where they read the registry entry's.
15. **Every registry claim in a deployment command becomes a check of the admission record**, `admittedHosts`, at the same places `claimHosts` ran: a missing record, and any of ruling 7's differences, refuse before anything changes, naming the `host request` and `host admit` lines. Each difference is a root change the operator's command cannot finish, so finding it first beats failing halfway through an apply. The registry is admit's alone.
16. **`init` settles the mesh subnet against what the deployment's user can read.** A site whose login is refused because the host has not admitted the deployment is skipped with a line saying `host admit` checks the subnet against that host as root and refuses an overlap; a site that answers is probed for routes, addresses and the user's networks and pools; the subnet counts as deployed once any site holds the deployment's manifest. A site that does not answer still stops init, as today.
17. **`config recover` finds the deployment from the login's own admission record**: it lists `/etc/paisans/admitted.*.json` and takes the one whose `user` is the login's name (`admission.Mine`).
18. **`mesh rekey` and `site remove --evict` tell a joining site by its admission record**: a record found is a site claimed for the deployment; a login refused, or no record, is unclaimed, and the existing evidence check (a peer listing its key, the deployment record listing it) still refuses a site that ran.
19. **Site remove's user side** removes the deployment's containers, networks, images and (with `--delete-data`) volumes, the manifest-proven files, the user units, the config store, the records, and empties the root; it leaves the root directory itself, WireGuard, the firewall, the registry entry, the admission record and the user to `host evict`, and ends with `sudo paisans host evict --deployment <id> --user <user> --execute` for the owner. Files and directories a container's user owns are removed with `rootlesskit`, which runs the command as root of a new user namespace mapping the user's subordinate range (Docker's rootless documentation, *Uninstall*: `rootlesskit rm -rf ~/.local/share/docker`). A manifest path outside the root is kept and reported, never deleted.
20. **A kept Caddy keeps serving without a registry mark.** `site remove` on a gateway whose `caddy.d` holds the owner's site files still reduces its Caddy to them and leaves it running; nothing marks the registry, which is root's, and the `host evict` line says that evict stops that Caddy and deletes the owner's files in `caddy.d`. Data kept without `--delete-data` is likewise left in the root and the user's volumes, and the line says evict deletes it. See founder decision 1.
21. **The gateway's self-check** fetches `https://<host>/.well-known/paisans/<token>` through the gateway site's `public_address` on 443, where `<host>` is the first of `dns.GatewayHostnames(cfg)`, and `http://<host>/.well-known/paisans/<token>` on 80, expecting a 3xx whose `Location` is the HTTPS URL of the same host and path. The body is the token and nothing else, and it alone tells this deployment from another server: the certificate is not judged here, since an untrusted or staging certificate is the certificate check's to report. A connection refused or reset, a timeout, or a TLS alert (Caddy's answer while it has no certificate) is retried for up to two minutes; any HTTP answer with another status or body, and a redirect anywhere else, fail at once. A gateway with no hostname has nothing to check, and the step says so. IPv6 `public_address6` is not fetched, since the operator's machine often has no IPv6 route; doctor shares the code in phase 7.
22. **Every Caddy route serves the self-check first**, from a snippet `(paisans_self)` imported at the top of each host block: a named matcher, which Caddy keeps in written order, so it is matched before a gated host's `handle @gate_signin`.
23. **A site behind the owner's web server gets one reference file per app**, `Root()/web-server/<app>.caddy` or `<app>.conf` for the server the record names, and `<app>.caddy`, `<app>.nginx.conf` and `<app>.apache.conf` when the record names none. Each file is recorded in the manifest's `web_server` list with its checksum, so a changed file is told apart from an unchanged one.
24. **`web_server.include_dir` stays parseable so that `validate` can refuse it by name**, as `web-server-include-dir`, rather than failing the load on an unknown key.
25. **The grant loses its web server part.** Nothing is written outside the deployment's root any more, so there is nothing for the review to approve there: `grant.WebServer`, `WebServerOf`, `WebServerPart`, `Scope.WebServer` and the review's web server lines are removed.
26. **Classification lives in a new package, `internal/topology`**, with a pure `Classify` over gathered evidence and one `Gather` that reads it: each site's login (`true`) and admission record, the gateways' deployed records, etcd's membership and Patroni's from the first member of each that answers. A site's login that fails for any reason (refused, timed out, unreachable) is "does not answer", as the spec's table says.
27. **The deployment record decides "not joined", and a run that cannot read it does not decide.** Every gateway apply records the sites it deployed, so a site the record does not list has not joined, unless etcd's or Patroni's membership, read from the first member that answers, lists it. When a site does not answer, nothing read lists it, and the record could not be read (no gateway answered, or its record is unreadable), the run refuses, naming what it could not read: dropping a joined site from the mesh is the one mistake a run must not make. A gateway that answers with no record yet is a deployment never applied, which lists nothing.
28. **A site whose only route into the mesh is through pending sites is pending**: a site with no dial endpoint is pending when every site with one is pending, iterated until nothing changes.
29. **Garage short of its replication holds back storage.** When the ready and down Garage sites number fewer than `storage.garage.replication`, the effective topology drops the storage step and every app `blueprints.InObjectStorage` places in object storage under the declaration, with a warning naming them; an app the deployed record lists as deployed is never dropped, and the run refuses instead, since its data would be orphaned.
30. **The effective topology is `cfg.WithoutJoining(pending...)`**, less the held-back apps, and is handed to every step of one run through a package-level value in `cmd/paisans`, as `reviewedByRun` hands the review: a step loads the declaration from `--config` and, when the run has classified, takes the run's classification instead of reading again.
31. **`apply --site` alone also classifies every site** ("Every run, `--site` included, classifies every site"), refuses a `--site` that is pending or down, and ends `Applied, degraded: ...` the same way.
32. **The manifest gains `pending`**, the sorted names of the sites pending when it was written; down sites are rendered as declared and are not in it. Doctor's Leftovers and apply's leftover report classify a site against `cfg.WithoutJoining(manifest.Pending...)`.
33. **Docker-tagged tests fail on a daemon that is not rootless**, through `internal/dockertest.RequireRootless(t)`, which reads `docker info --format '{{json .SecurityOptions}}'` for `name=rootless` (Docker's rootless documentation: `docker info` lists `rootless` under Security Options), unless `PAISANS_DOCKER_ROOTFUL=1` is set, in which case the test logs that it ran against a rootful daemon. A tag is an explicit request to run them, and a pass against a rootful daemon proves nothing about the deployment's.
34. **`apply.ErrLoginRefused`** wraps the two authentication refusals `authProblem` recognises, so `topology`, `init` and `meshkey` tell "the host would not let us in" from a failed command; `apply.NoLogin(err)` is that or `ErrUnreachable`.

## Founder decisions

1. **A removed gateway's Caddy** (ruling 20): when its `caddy.d` holds only the deployment's own site files, `site remove` stops and removes it; when it serves anything else, `site remove` leaves it running, and the `host evict` line says evict stops it and deletes those files.
2. **`gateway move`** ends by printing the `host request` line for the old gateway's site; its owner re-admits it without `--public-ports` once DNS has moved, which closes 80 and 443 (founder decision; add it to the gateway move task).
3. **The user's manager is ordered after the mesh at boot by `host admit`** (phase 5). The config store's user unit keeps `Restart=always`, which still covers a mesh restart after boot.

## What this phase hands to phases 7 and 10

* **Phase 7** reads the record with `admission.Read`, compares it with `admission.Record.Differences(site, s) []string` (the same differences every command refuses on), checks the gateway with `selfcheck.Check`, and lists pending and down sites with `topology.Gather` and `topology.Classify`, wording each with `doctor.Losses(cfg, site)`, exported here.
* **Phase 10** runs `apply` as the deployment's user on admitted hosts: the self-check on 443 and 80, a container started after admission on 443 that `apply` reports, a data site not yet admitted (degraded run, then joined by the next apply), and a stopped joined host (degraded, kept as a member). It also observes the `uid:` field of `ss -Hltnue` (ruling 10).
* **The mesh rekey phase** (`docs/specs/2026-10-15-mesh-rekey-sites-down.md`) reads `topology.Classification` for which sites are down.

---

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `internal/admission/record.go`, `record_test.go`, `testdata/admitted.f2a9.json` | modify | the record's `site` and `roles`; `Mine` |
| `internal/deployment/deployment.go`, `deployment_test.go` | modify | `StateDir`, `StatePath`, `StateLockCommand` |
| `internal/deployrecord/record.go`, `internal/grant/record.go`, `internal/gatewaymove/gatewaymove.go`, `cmd/paisans/record.go` | modify | records under `StateDir`, one lock, no sudo fallback |
| `internal/render/hostsites.go`, `site.go`, `templates/Caddyfile.tmpl`, `testdata/golden*/` | modify | `HostSitesDir(d)`; the self-check snippet |
| `internal/apply/apply.go` | modify | the host sites directory created as the user; the manifest's `pending` |
| `internal/hostprep/configstore.go`, `profiles/ubuntu-24.04/config-*.tmpl`, `internal/configstore/remote.go` | modify | the config store under `StateDir`, as user units |
| `internal/hostcheck/inventory.go`, `parse.go`, `classify.go` | modify | the host check as the user |
| `internal/apply/volumeprune.go` | modify | sizes from `docker system df -v` |
| `internal/mesh/mesh.go`, `internal/preflight/preflight.go`, `cmd/paisans/mesh.go`, `cmd/paisans/doctor.go` | modify | `ProbeUser` |
| `internal/hostprep/authorized_keys.go`, `leave.go`, `admit.go` | modify | keys in the user's own home, the sidecar in `StateDir` |
| `internal/hostprep/hostprep.go`, `daemon.go` (create), `user.go` (create), `watchdog.go`, `internal/hostadmit/daemon.go` | modify | `host prepare` installs nothing |
| `internal/admission/record.go`, `cmd/paisans/claim.go` (renamed `admitted.go`) and every caller, `internal/preflight/preflight.go` | modify | `Record.Differences`, `ForSite`, `admittedHosts` |
| `internal/apply/transport.go`, `cmd/paisans/mesh.go`, `cmd/paisans/config.go`, `internal/meshkey/meshkey.go` | modify | `ErrLoginRefused`; registry reads out of init, recover and rekey |
| `internal/siteremove/host.go`, `force.go`, `siteremove.go`, `cmd/paisans/siteremove.go`, `cmd/paisans/hostdeployments.go` | modify | the user side and the evict line; admission records |
| `internal/selfcheck/selfcheck.go` (create), `cmd/paisans/gateway_selfcheck.go` (create) | create | the gateway answers as itself |
| `internal/validate/monitor.go`, `internal/config/include_dir.go`, `internal/grant/*` | modify | `include_dir` refused; the grant's web server part gone |
| `internal/webserver/*`, `cmd/paisans/converge_webserver*.go`, `cmd/paisans/webserver.go`, `internal/appremove/*` | modify | the reference snippet, `show` from the record |
| `internal/topology/topology.go`, `gather.go` (create) | create | the classification and the effective topology |
| `internal/render/write.go`, `internal/hostcheck/inventory.go`, `cmd/paisans/leftover.go`, `cmd/paisans/doctor.go` | modify | the manifest's `pending` |
| `cmd/paisans/sites.go` (create), `converge.go`, `main.go`, `internal/doctor/reach.go` | modify | the run on the sites it has |
| `internal/dockertest/dockertest.go` (create) and the tagged tests | create | Docker tests against a rootless daemon |
| `README.md`, `docs/commands.md`, `docs/guides/behind-your-own-web-server.md`, `docs/development.md`, `CHANGELOG.md` | modify | the passages this phase owns |

## Task order and parallel work

The founder approved parallel worktrees, integrated one at a time. Tasks in one group change different code. Each adds one bullet to `CHANGELOG.md`, and a conflict confined to it is resolved by keeping both bullets. Where two tasks of a group update the same shared test fake (`internal/siteremove/world_test.go`, `cmd/paisans/doctor_test.go`), each changes different answers in it: the integrator rebases the later task onto the earlier, resolves the fake by keeping both changes, and runs that package's tests again before merging. Everything not in a group runs alone, in order. No two tasks in a group regenerate the same golden tree or edit the same function.

| Task | Title | Runs |
|---|---|---|
| 1 | The admission record names its site and roles | group A |
| 2 | The deployment's records move under `StateDir` | group A |
| 3 | The owner's Caddy site files move under the deployment's root | alone, after group A |
| 4 | The configuration store as the user's own units | group B |
| 5 | The host check as the deployment's user | group B |
| 6 | Volume prune reads sizes from Docker | group B |
| 7 | The mesh probe reads the user's `daemon.json` | group B |
| 8 | Keys in the user's own `authorized_keys` | alone, after group B |
| 9 | The admission record replaces the registry claim | alone |
| 10 | `host prepare` installs nothing | alone |
| 11 | Registry reads leave init, recover and rekey | alone |
| 12 | Site remove cleans as the user and hands over to evict | alone |
| 13 | `site remove --force` and `host deployments` read admission records | alone |
| 14 | The gateway answers as itself | group D |
| 17 | The classification of sites | group D |
| 20 | Docker-tagged tests against a rootless daemon | group D |
| 15 | `include_dir` refused, the grant's web server part gone | alone, after group D |
| 16 | A reference snippet for the owner's web server | alone |
| 18 | Each manifest records the sites pending | alone |
| 19 | The run on the sites it has | alone |
| 21 | The owner's web server, documented | alone |
| 22 | The paths and permissions, documented | alone, last |

Tasks 11 and 12 run alone because both change `internal/siteremove`'s world fake. Task 15 runs after Task 14 because both edit the end of `applyCommand`. Task 16 follows 15 (it removes the code 15's grant changes stop calling) and 12 (site remove's web server step). Task 18 follows 17; 19 follows 18. Tasks 21 and 22 follow every code task they describe. In document order the tasks are numbered as above; their sections appear in the order of this table.

---
### Task 1: The admission record names its site and roles

Ruling 14. `site remove --force --id` (Task 13) and `host deployments` (Task 13) read the site and roles where they read a registry entry's today, and the registry is root's.

**Files:**
- Modify: `internal/admission/record.go`
- Modify: `internal/admission/testdata/admitted.f2a9.json` (regenerated)
- Test: `internal/admission/record_test.go`

**Interfaces:**
- Consumes: phase 5's `Request.Site`, `Request.Roles` (Task 2b of phase 5); `config.ValidSiteName`, `config.KnownRole`, `config.RoleRepeated`.
- Produces: `admission.Record.Site string` (`json:"site"`), `admission.Record.Roles []string` (`json:"roles"`, sorted, `[]` for none), filled by `NewRecord`; `func Mine(t Runner) (Record, error)` with `type Runner interface { Run(string) (string, error); ReadFile(string) (string, bool, error) }`, the record whose `user` is the login's own name; `ErrNotMine`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/admission/record_test.go`:

```go
// The record carries the request's site and roles, so a command that has
// no configuration (site remove --force --id, host deployments) learns
// what the host was admitted as.
func TestNewRecordCarriesTheSiteAndRoles(t *testing.T) {
	req := build(t, "home-a")
	rec := admission.NewRecord(req, admission.Grants{Watchdog: true}, "", at)
	if rec.Site != "home-a" || strings.Join(rec.Roles, ",") != strings.Join(req.Roles, ",") {
		t.Errorf("site %q roles %v, want home-a %v", rec.Site, rec.Roles, req.Roles)
	}
}

// A record without them, or with roles out of order or unknown, is not one
// admit writes.
func TestParseRecordRefusesABadSiteOrRoles(t *testing.T) {
	good := encode(t, admission.NewRecord(build(t, "home-a"), admission.Grants{Watchdog: true}, "", at))
	for name, edit := range map[string]func(string) string{
		"no site":       func(s string) string { return strings.Replace(s, "  \"site\": \"home-a\",\n", "", 1) },
		"bad site":      func(s string) string { return strings.Replace(s, `"site": "home-a"`, `"site": "Home A"`, 1) },
		"unknown role":  func(s string) string { return strings.Replace(s, `"data"`, `"database"`, 1) },
	} {
		if _, err := admission.ParseRecord([]byte(edit(string(good))), "f2a9"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Mine is the login's own record: the one under /etc/paisans whose user is
// the name `id -un` prints, whatever other deployments are admitted beside it.
func TestMineIsTheLoginsOwnRecord(t *testing.T) {
	mine := encode(t, admission.NewRecord(build(t, "home-a"), admission.Grants{Watchdog: true}, "", at))
	other := strings.ReplaceAll(strings.ReplaceAll(string(mine), "f2a9c4e1", "0b0bc4e1"), "paisans-f2a9", "paisans-0b0b")
	h := &listing{host: host{files: map[string]string{
		"/etc/paisans/admitted.f2a9.json": string(mine),
		"/etc/paisans/admitted.0b0b.json": other,
	}}, user: "paisans-f2a9"}
	rec, err := admission.Mine(h)
	if err != nil || rec.User != "paisans-f2a9" || rec.Site != "home-a" {
		t.Fatalf("Mine = %+v, %v", rec, err)
	}
	h.user = "ubuntu"
	if _, err := admission.Mine(h); !errors.Is(err, admission.ErrNotMine) {
		t.Errorf("a login with no record of its own: %v", err)
	}
}

// listing is a host that answers Mine's listing and the login's name.
type listing struct {
	host
	user string
}

func (l *listing) Run(command string) (string, error) {
	var names []string
	for p := range l.files {
		names = append(names, p)
	}
	slices.Sort(names)
	return "user " + l.user + "\n" + strings.Join(names, "\n") + "\n", nil
}
```

Add `"slices"` to the imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/admission/ -run 'TestNewRecordCarries|TestParseRecordRefusesABadSite|TestMineIs'`
Expected: FAIL, `rec.Site undefined` and `undefined: admission.Mine`.

- [ ] **Step 3: Implement**

In `internal/admission/record.go`, add the fields after `User`:

```go
	// Site is the site's name in the configuration and Roles its roles,
	// sorted, as the request named them: what a command with no
	// configuration learns the host was admitted as.
	Site  string   `json:"site"`
	Roles []string `json:"roles"`
```

In `NewRecord`, set `Site: req.Site, Roles: append([]string{}, req.Roles...)`. In `EncodeRecord`, before marshalling, `if r.Roles == nil { r.Roles = []string{} }`. Extend `recordFields` to `{"version", "deployment", "user", "site", "roles", "grants", ...}` in that order. In `ParseRecord`, after the user check:

```go
	if !config.ValidSiteName(r.Site) {
		problems = append(problems, fmt.Sprintf("site: %q is not a site name", r.Site))
	}
	roles := make([]config.Role, len(r.Roles))
	for i, role := range r.Roles {
		roles[i] = config.Role(role)
	}
	for i, role := range r.Roles {
		switch {
		case !config.KnownRole(roles[i]):
			problems = append(problems, fmt.Sprintf("roles[%d]: %q is not a site role", i, role))
		case config.RoleRepeated(roles, i):
			problems = append(problems, fmt.Sprintf("roles[%d]: %q is listed twice", i, role))
		case i > 0 && role < r.Roles[i-1]:
			problems = append(problems, fmt.Sprintf("roles[%d]: %q comes after %q, and roles are written sorted", i, role, r.Roles[i-1]))
		}
	}
```

Add, at the end of the file:

```go
// ErrNotMine is a login with no admission record of its own on the host.
var ErrNotMine = errors.New("no admission record names this login")

// Runner is what Mine needs of a host: a command, and a file read.
type Runner interface {
	Reader
	Run(command string) (string, error)
}

// mineCommand prints the login's name, then every record's path. /etc/paisans
// is 755 and every record 644, so the deployment's user lists and reads them.
const mineCommand = `echo "user $(id -un)"; for f in ` + Dir + `/admitted.*.json; do [ -f "$f" ] && echo "$f"; done; true`

// Mine is the admission record of the deployment the login belongs to: the
// one whose user is the login's own name. A deployment's commands that run
// without its configuration (config recover) learn the deployment from it.
// A record that does not parse is skipped, since it cannot name the login.
func Mine(t Runner) (Record, error) {
	out, err := t.Run(mineCommand)
	if err != nil {
		return Record{}, fmt.Errorf("listing %s: %w", Dir, err)
	}
	var user string
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(line, "user "); ok {
			user = name
		} else if line != "" {
			paths = append(paths, line)
		}
	}
	for _, p := range paths {
		token := strings.TrimSuffix(strings.TrimPrefix(p, Dir+"/admitted."), ".json")
		if !ValidToken(token) {
			continue
		}
		content, found, err := t.ReadFile(p)
		if err != nil || !found {
			continue
		}
		if rec, err := ParseRecord([]byte(content), token); err == nil && rec.User == user {
			return rec, nil
		}
	}
	return Record{}, fmt.Errorf("%s: %w (%s)", Dir, ErrNotMine, user)
}
```

- [ ] **Step 4: Regenerate the golden record and run the tests**

Run: `go test ./internal/admission/ -run TestRecordGolden -update && go test ./internal/admission/`
Expected: PASS. `git diff internal/admission/testdata/admitted.f2a9.json` shows `"site": "home-a"` and the roles after `"user"`, and nothing else.

- [ ] **Step 5: Check phase 5's writer still builds every record from `NewRecord`**

Run: `grep -rn 'admission.Record{' --include=*.go internal cmd | grep -v _test`
Expected: no output. A literal elsewhere would write a record without the site; build it from `NewRecord` instead.

- [ ] **Step 6: Commit**

```bash
git add internal/admission
git commit -m "feat: name the site and roles in the admission record" -m "A command with no configuration, site remove --force --id and host
deployments, learns what a host was admitted as from the record, since the
registry that held it is root's. Mine finds the login's own record, which
config recover reads to name the deployment.

Nothing is released, so the record's version stays 1.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

No CHANGELOG bullet: the record is unreleased; phase 5's bullet covers it.

---

### Task 2: The deployment's records move under `StateDir`

Rulings 1, 2, 3. The deployed record, the grant record and the gateway move's marker are written and read as the deployment's user, under `/srv/paisans/<token>/.paisans/`, serialised on one lock there.

**Files:**
- Modify: `internal/deployment/deployment.go`, `internal/deployment/deployment_test.go`
- Modify: `internal/deployrecord/record.go`, `internal/deployrecord/export_test.go`, `internal/deployrecord/record_test.go`, `internal/deployrecord/readable_test.go`
- Modify: `internal/grant/record.go`, `internal/grant/grant_test.go`
- Modify: `internal/gatewaymove/gatewaymove.go`, `internal/gatewaymove/gatewaymove_test.go`
- Modify: `cmd/paisans/record.go`, `cmd/paisans/converge_review_test.go`, `cmd/paisans/review_test.go`
- Modify: `internal/siteremove/world_test.go`, `internal/siteremove/resume_test.go` (paths in fakes)
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: `deployment.Deployment.Root()`.
- Produces: `func (d Deployment) StateDir() string` (`/srv/paisans/<token>/.paisans`); `func (d Deployment) StatePath(name string) string`; `func (d Deployment) StateLockCommand(what string) string`, the shell prelude every record writer runs: `umask 077`, the directory created 700 when absent and refused when a link, and `flock -w 60` on `StateDir()/lock`; `deployrecord.Path(d)` = `StatePath("deployed.json")`; `grant.Path(d)` = `StatePath("granted.json")`; `gatewaymove.MarkerPath(d)` = `StatePath("gateway-move.json")`. Removed: `deployrecord.ReadAsUser`, `userReader`, `userReadCommand`, `absentMarker`, `deniedMarker`, and the `UserReadCommand`/`DeniedMarker` exports.

- [ ] **Step 1: Write the failing tests**

Add to `internal/deployment/deployment_test.go`:

```go
// The toolkit's own records on a host sit in the deployment's root, where
// its user writes them without sudo.
func TestStateDir(t *testing.T) {
	d := deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
	if d.StateDir() != "/srv/paisans/f2a9/.paisans" || d.StatePath("deployed.json") != "/srv/paisans/f2a9/.paisans/deployed.json" {
		t.Errorf("StateDir %s, StatePath %s", d.StateDir(), d.StatePath("deployed.json"))
	}
	lock := d.StateLockCommand("the deployment record")
	for _, want := range []string{"umask 077", "[ -L '/srv/paisans/f2a9/.paisans' ]", "install -d -m 700 '/srv/paisans/f2a9/.paisans'", "exec 9>>'/srv/paisans/f2a9/.paisans/lock'", "flock -w 60 9"} {
		if !strings.Contains(lock, want) {
			t.Errorf("the lock prelude has no %q:\n%s", want, lock)
		}
	}
	if strings.Contains(lock, "/var/lib/paisans") || strings.Contains(lock, "sudo") {
		t.Errorf("the prelude reaches root's directory: %s", lock)
	}
}
```

In `internal/deployrecord/record_test.go`, replace `TestThePathCarriesTheToken` with:

```go
func TestTheRecordIsInTheDeploymentsStateDir(t *testing.T) {
	if got := deployrecord.Path(dep); got != "/srv/paisans/f2a9/.paisans/deployed.json" {
		t.Errorf("got %s", got)
	}
}
```

In `internal/deployrecord/readable_test.go`, delete `userHost`, `TestTheRecordIsReadWithoutSudo`, `TestAnAbsentRecordIsNotReadThroughSudo`, `TestADeniedRecordIsReadThroughSudo` and `TestADirectoryTheUserCannotEnterIsADenial`, and replace `TestAWriteLeavesTheRecordReadableWithoutSudo` with:

```go
// A write runs under the deployment's own lock, in its own directory, and
// leaves the record 644 by a rename.
func TestAWriteIsUnderTheDeploymentsLock(t *testing.T) {
	h := &fakeHost{files: map[string]string{}}
	var ran []string
	rec := recording{h, &ran}
	if _, err := deployrecord.Update(map[string]registry.Runner{"vm": rec}, dep, deployrecord.Adding(sites("vm")), now); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 {
		t.Fatalf("ran %d commands", len(ran))
	}
	command := ran[0]
	for _, want := range []string{dep.StateLockCommand("the deployment record"), `tmp=$(mktemp '/srv/paisans/f2a9/.paisans/.deployed.XXXXXX')`, `chmod 644 "$tmp";`, `mv "$tmp" "$f"`} {
		if !strings.Contains(command, want) {
			t.Errorf("the write has no %q:\n%s", want, command)
		}
	}
	if strings.Contains(command, "/var/lib/paisans") {
		t.Errorf("the write reaches root's directory:\n%s", command)
	}
}
```

Drop the now unused imports (`os`, `os/exec`, `path/filepath`) the deleted tests used. In `internal/deployrecord/export_test.go`, delete `UserReadCommand` and `DeniedMarker`.

In `internal/grant/grant_test.go`, in `TestWriteIsUnderTheLockAndKeepsTheOtherParts`, replace the `exec 9>>/var/lib/paisans/registry.lock` condition with `strings.Contains(cmd, testDeployment(t).StateLockCommand("the grant record"))`, and the message with `"not written under the deployment's lock by a rename"`. Add:

```go
func TestTheGrantRecordIsInTheDeploymentsStateDir(t *testing.T) {
	if got := grant.Path(testDeployment(t)); got != "/srv/paisans/f2a9/.paisans/granted.json" {
		t.Errorf("got %s", got)
	}
}
```

In `internal/gatewaymove/gatewaymove_test.go`, add:

```go
func TestTheMarkerIsInTheDeploymentsStateDir(t *testing.T) {
	d := deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
	if got := gatewaymove.MarkerPath(d); got != "/srv/paisans/f2a9/.paisans/gateway-move.json" {
		t.Errorf("got %s", got)
	}
}
```

(add the `deployment` import if the file lacks it).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/deployment/ ./internal/deployrecord/ ./internal/grant/ ./internal/gatewaymove/`
Expected: FAIL: `d.StateDir undefined`, and the path tests report `/var/lib/paisans/...`.

- [ ] **Step 3: Implement the deployment's state directory**

In `internal/deployment/deployment.go`, after `Manifest`:

```go
// StateDir is where the toolkit keeps its own records for this deployment on
// a host: /srv/paisans/<token>/.paisans, the deployment user's, mode 700.
// The deployed record, the grant record, a gateway move's marker, the
// authorized_keys sidecar, the configuration store and its sync script live
// here, so every command writes them as the deployment's user, without sudo.
func (d Deployment) StateDir() string { return d.Path(".paisans") }

// StatePath is a file in StateDir.
func (d Deployment) StatePath(name string) string { return d.StateDir() + "/" + name }

// StateLockCommand is the shell every writer of a record in StateDir runs
// first: a umask that keeps what it creates the user's, StateDir created 700
// when it is absent and refused when it is a link, and the deployment's one
// lock held for the rest of the command, so two commands never interleave
// their writes. what names the record, for the message when the lock is held.
func (d Deployment) StateLockCommand(what string) string {
	dir, lock := quote(d.StateDir()), quote(d.StatePath("lock"))
	return strings.Join([]string{
		"umask 077",
		fmt.Sprintf("if [ -L %[1]s ]; then echo 'paisans: %[1]s is a link, so %[2]s is not written'; exit 1; fi", dir, what),
		fmt.Sprintf("[ -d %[1]s ] || install -d -m 700 %[1]s", dir),
		"exec 9>>" + lock,
		fmt.Sprintf("flock -w 60 9 || { echo 'paisans: another command holds %s'; exit 1; }", lock),
	}, "; ")
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
```

- [ ] **Step 4: Move the three records**

In `internal/deployrecord/record.go`:

```go
// Path is the record on a gateway, in the deployment's state directory,
// which only the deployment's user writes.
func Path(d deployment.Deployment) string { return d.StatePath("deployed.json") }
```

and `writeCommand` becomes:

```go
func writeCommand(d deployment.Deployment, want, content string) string {
	return strings.Join([]string{
		"set -e",
		d.StateLockCommand("the deployment record"),
		"f=" + quote(Path(d)),
		"cur=none",
		`if [ -f "$f" ]; then cur=$(sha256sum "$f" | cut -d' ' -f1); fi`,
		`[ "$cur" = ` + quote(want) + ` ] || { echo ` + quote(ChangedMarker) + `; exit 1; }`,
		"tmp=$(mktemp " + quote(d.StatePath(".deployed.XXXXXX")) + ")",
		`trap 'rm -f "$tmp"' EXIT`,
		"printf %s " + quote(base64.StdEncoding.EncodeToString([]byte(content))) + ` | base64 -d > "$tmp"`,
		`chmod 644 "$tmp"`,
		`mv "$tmp" "$f"`,
	}, "; ")
}
```

Rewrite its doc comment: the record is the deployment user's, in its state directory, written under the deployment's lock. Delete `ReadAsUser`, `userReader`, its methods, `userReadCommand`, `absentMarker` and `deniedMarker`, and the `path` import if nothing else uses it.

In `internal/grant/record.go`, `Path` returns `d.StatePath("granted.json")` with the comment "the deployment user's, in its state directory", and `writeCommand` takes the same shape: `d.StateLockCommand("the grant record")` in place of the four `mkdir`/`chmod`/`exec`/`flock` lines, and `tmp=$(mktemp ` + `quote(d.StatePath(".granted.XXXXXX"))` + `)`. Replace the malformed-record advice at `Read`, `` `sudo rm %s` ``, with `` `rm %s` ``.

In `internal/gatewaymove/gatewaymove.go`, `MarkerPath` returns `d.StatePath("gateway-move.json")`, and its comment says it sits beside the deployment record in the deployment's state directory. The `WriteFile` at the marker's write is unchanged: the transport writes it as the user. If `registry` is no longer imported by any of these three files after the change, drop the import.

In `cmd/paisans/record.go`, `recordReaders` returns the hosts as they are:

```go
// recordReaders is every gateway paisans-stack.yaml declares, each read as
// the deployment's user, whose record it is.
func recordReaders(cfg *config.Config, hosts recordHosts) map[string]registry.Runner {
	out := map[string]registry.Runner{}
	for _, gw := range cfg.GatewaySites() {
		out[gw] = hosts(gw)
	}
	return out
}
```

Rewrite `deploymentRecord`'s comment to drop the sudo fallback, and in `recordApplied` replace `` `sudo rm %s` `` with `` `rm %s` ``.

- [ ] **Step 5: Update the fakes that name the old paths**

Run: `grep -rln '/var/lib/paisans/\(deployed\|granted\|gateway-move\)\|registry.lock' --include=*_test.go internal cmd`
For each file listed, replace `/var/lib/paisans/deployed.f2a9.json` with `/srv/paisans/f2a9/.paisans/deployed.json`, `/var/lib/paisans/granted.f2a9.json` with `/srv/paisans/f2a9/.paisans/granted.json`, `/var/lib/paisans/gateway-move.f2a9.json` with `/srv/paisans/f2a9/.paisans/gateway-move.json`, and an expectation of `registry.lock` with the deployment's `StateLockCommand`. Where a fake builds the path with `deployrecord.Path`, `grant.Path` or `gatewaymove.MarkerPath`, leave it.

- [ ] **Step 6: Run the touched packages**

Run: `go test ./internal/deployment/ ./internal/deployrecord/ ./internal/grant/ ./internal/gatewaymove/ ./internal/siteremove/ ./internal/meshkey/ ./cmd/paisans/`
Expected: PASS.

- [ ] **Step 7: CHANGELOG and the full suite**

Under `[Unreleased]`, `### Changed`:

```markdown
- The deployment record, the grant record and a gateway move's marker live in `/srv/paisans/<token>/.paisans/`, written by the deployment's own user under one lock of the deployment's.
```

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/deployment internal/deployrecord internal/grant internal/gatewaymove internal/siteremove internal/meshkey cmd/paisans CHANGELOG.md
git commit -m "feat: keep the deployment's records in its own state directory" -m "Every command but admit, evict and host deployments runs as the
deployment's user, which cannot write /var/lib/paisans. The deployment
record, the grant record and a gateway move's marker move to
/srv/paisans/<token>/.paisans/, the user's, and their writers lock the
deployment's own lock there. The sudo fallback for reading the deployment
record goes: the user that reads it is the user that wrote it.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: The owner's Caddy site files move under the deployment's root

Ruling 4. `/srv/caddy.d` is root's to create; `Root()/caddy.d` is the user's.

**Files:**
- Modify: `internal/render/hostsites.go`, `internal/render/site.go`, `internal/render/plan.go` (comment)
- Modify: `internal/render/testdata/golden/vm/srv/paisans/f2a9/infra/compose.yaml`, the three golden `Caddyfile`s that name `/srv/caddy.d` (regenerated)
- Modify: `internal/apply/apply.go`
- Modify: `internal/hostcheck/inventory.go`, `internal/hostcheck/classify.go`, `internal/hostcheck/probe_internal_test.go`
- Modify: `internal/gatewaymove/gatewaymove.go`
- Modify: `internal/siteremove/host.go`, `internal/siteremove/caddy.go`, `internal/siteremove/remains.go`, `internal/siteremove/force.go`, `internal/siteremove/siteremove.go` (comment)
- Modify: `internal/ownership/ownership.go` (comment), `internal/registry/registry.go` (comments and the one advice string)
- Modify: `cmd/paisans/hostdeployments.go`, `cmd/paisans/help.go`, `cmd/paisans/plan.go`
- Modify: every `_test.go` that names `/srv/caddy.d` (Step 5)
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: `deployment.Deployment.Path`.
- Produces: `func render.HostSitesDir(d deployment.Deployment) string` (`/srv/paisans/<token>/caddy.d`); `render.HostSitesMount` unchanged (`/etc/caddy.d`); `func apply.HostSitesCommand(d deployment.Deployment) string`, run as the user.

- [ ] **Step 1: Write the failing tests**

Create `internal/render/hostsites_test.go`:

```go
package render_test

import (
	"testing"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// The host owner's site files are in the deployment's own root, which its
// user creates and the gateway's Caddy mounts read only.
func TestHostSitesDirIsTheDeployments(t *testing.T) {
	d := deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
	if got := render.HostSitesDir(d); got != "/srv/paisans/f2a9/caddy.d" {
		t.Errorf("got %s", got)
	}
}
```

Add to `internal/apply/apply_test.go`:

```go
// The deployment's user creates the host sites directory, in its own root,
// with no chown it could not make.
func TestHostSitesCommandRunsAsTheUser(t *testing.T) {
	d := deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
	got := apply.HostSitesCommand(d)
	if got != "[ -d '/srv/paisans/f2a9/caddy.d' ] || install -d -m 755 '/srv/paisans/f2a9/caddy.d'" {
		t.Errorf("got %s", got)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/render/ -run TestHostSitesDirIsTheDeployments; go test ./internal/apply/ -run TestHostSitesCommandRunsAsTheUser`
Expected: FAIL, `render.HostSitesDir` is a constant and `HostSitesCommand` takes no argument.

- [ ] **Step 3: Implement**

`internal/render/hostsites.go` becomes:

```go
package render

import "github.com/paisans-software/paisans-cli/internal/deployment"

// HostSitesDir is the gateway's directory of Caddy site blocks the host's
// owner adds for things paisans does not run, Eg: a blog beside the
// community: /srv/paisans/<token>/caddy.d, in the deployment's own root,
// created by apply as the deployment's user. The owner writes into it as
// root; paisans never writes, records or removes a file there.
func HostSitesDir(d deployment.Deployment) string { return d.Path("caddy.d") }

// HostSitesMount is where the gateway's Caddy sees HostSitesDir.
const HostSitesMount = "/etc/caddy.d"
```

In `internal/render/site.go`, both places that set `HostSitesSource` to `HostSitesDir` set it to `HostSitesDir(p.dep())`.

In `internal/apply/apply.go`:

```go
// HostSitesCommand creates render.HostSitesDir, the deployment user's and
// 0755, when it is absent: the gateway's Caddy mounts it, and the host's
// owner writes their site files into it.
func HostSitesCommand(d deployment.Deployment) string {
	return fmt.Sprintf("[ -d %[1]s ] || install -d -m 755 %[1]s", shellQuote(render.HostSitesDir(d)))
}
```

and at its one caller, `t.Run(HostSitesCommand(plan.Deployment))`, with the message `"creating %s, so the gateway was not changed"` given `render.HostSitesDir(...)`. Rewrite the `HostSites` field's comment: "Execute creates the directory, the deployment user's and 0755".

In `internal/hostcheck/inventory.go`, `hostSitesProbe` becomes a function `hostSitesProbe(d deployment.Deployment) string` building the same script over `render.HostSitesDir(d)` (quote the path with the package's quoting), and `Inspect` calls it with its `d`. In `classify.go`, `keptCaddy`'s message names `render.HostSitesDir(d)` for that deployment `d`. In `probe_internal_test.go`, call `hostSitesProbe(fixtureDeployment)` (or the deployment the test file defines) and create the directory the test's shell reads under a temporary root by substituting the path, as the test does today with the constant.

In `internal/gatewaymove/gatewaymove.go`, the inspect command's glob and the note at the evidence check use `render.HostSitesDir(d)`; the comments say "the deployment's caddy.d".

In `internal/siteremove`, every use of `render.HostSitesDir` takes the plan's deployment (`p.dep()`); `containersCommand`'s guard and the caddy reduce's paths change with it.

In `cmd/paisans/hostdeployments.go`, `deploymentsProbe` drops `render.HostSitesDir` from its readability loop and lists sites with a glob over every root: `for f in ` + `deployment.Base` + `/*/caddy.d/*.caddy; do if [ -f "$f" ]; then echo "site ${f#` + `deployment.Base` + `/}"; fi; done`, so a site line names `<token>/caddy.d/<file>`. Task 13 rewrites this command; this step keeps it compiling and truthful.

In `cmd/paisans/help.go`, the two passages that say `/srv/caddy.d` say "its caddy.d" (`/srv/paisans/<token>/caddy.d`). In `cmd/paisans/plan.go`, the step that names the directory names `render.HostSitesDir(cfg.Deployment())`. In `internal/registry/registry.go`, the comments and the gateway advice string say "the Caddy that imports its deployment's caddy.d".

- [ ] **Step 4: Regenerate the golden render trees and read the diff**

Run: `go test ./internal/render/ -update && git diff --stat internal/render/testdata`
Expected: exactly the four files named under **Files** change, each by the one line naming `/srv/caddy.d`, now `/srv/paisans/f2a9/caddy.d`. Read `git diff internal/render/testdata`; any other change is a bug in Step 3.

- [ ] **Step 5: Update the tests that name the old directory**

Run: `grep -rln '/srv/caddy.d' --include=*_test.go internal cmd`
In each file, replace `/srv/caddy.d` with `/srv/paisans/f2a9/caddy.d` for this deployment's directory and `/srv/paisans/<that token>/caddy.d` where a test sets up another deployment's kept Caddy. Then `grep -rn '/srv/caddy.d' internal cmd` prints nothing.

- [ ] **Step 6: Run the touched packages, CHANGELOG, the full suite**

Run: `go test ./internal/render/ ./internal/apply/ ./internal/hostcheck/ ./internal/gatewaymove/ ./internal/siteremove/ ./internal/ownership/ ./internal/registry/ ./cmd/paisans/`
Expected: PASS.

Under `[Unreleased]`, `### Changed`:

```markdown
- The host owner's Caddy site files beside a gateway go in `/srv/paisans/<token>/caddy.d`, which `apply` creates as the deployment's user, instead of `/srv/caddy.d`.
```

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal cmd CHANGELOG.md
git commit -m "feat: keep the owner's caddy site files in the deployment's root" -m "The deployment's user cannot create /srv/caddy.d, which is root's. The
gateway's Caddy now imports the site files from its own deployment's
caddy.d, which apply creates as the user and mounts at /etc/caddy.d as
before. The golden render trees change by that one path.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 4: The configuration store as the user's own units

Rulings 5, 6. The store's repository, peers, sync log, incoming bundle and sync script move under `StateDir()`; its daemon, sync service and timer are the user's systemd units, under the linger admit turned on.

**Files:**
- Modify: `internal/hostprep/configstore.go`, `internal/hostprep/hostprep.go` (the `ConfigStore` call and the profile interface), `internal/hostprep/ubuntu.go` (`unitState`)
- Modify: `internal/hostprep/profiles/ubuntu-24.04/config-daemon.service.tmpl`, `config-sync.service.tmpl`, `config-sync.sh.tmpl`, `services-probe.sh.tmpl`
- Create: `internal/hostprep/user.go`
- Modify: `internal/configstore/remote.go`, `internal/configstore/remote_test.go`, `internal/configstore/receive_test.go`
- Test: `internal/hostprep/configstore_test.go`, `internal/hostprep/configsync_test.go`, `internal/hostprep/hostprep_test.go` (`configStored`)
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: Task 2's `deployment.Deployment.StateDir`, `StatePath`.
- Produces: `func hostprep.ConfigDir(d) string` (`StatePath("config")`); `ConfigRepo`, `ConfigPeers`, `ConfigSyncLog`, `ConfigIncoming` under it with today's names (`<token>.git`, `<token>.peers`, `<token>.sync.json`, `<token>.incoming.bundle`); `ConfigScriptDir(d)` = `StatePath("bin")`; `const hostprep.UserSystemctl = "XDG_RUNTIME_DIR=/run/user/$(id -u) systemctl --user"`; `func hostprep.Home(t Transport) (string, error)`; `func hostprep.UserUnitDir(home string) string` (`<home>/.config/systemd/user`); `Profile.ConfigStore(t Transport, d deployment.Deployment, self config.Site, peers []string, home string) (Section, error)`.

- [ ] **Step 1: Write the failing tests**

In `internal/hostprep/configstore_test.go`, replace the constants with the store's new places (the fixture's login user is `ubuntu`, whose passwd entry `keysPrepared` answers):

```go
const (
	storeRepo   = "/srv/paisans/f2a9/.paisans/config/f2a9.git"
	storePeers  = "/srv/paisans/f2a9/.paisans/config/f2a9.peers"
	storeScript = "/srv/paisans/f2a9/.paisans/bin/config-sync.sh"
	storeDaemon = "/home/ubuntu/.config/systemd/user/paisans-f2a9-config-daemon.service"
	storeSyncSv = "/home/ubuntu/.config/systemd/user/paisans-f2a9-config-sync.service"
	storeTimer  = "/home/ubuntu/.config/systemd/user/paisans-f2a9-config-sync.timer"

	probeDaemonUnit = "is-enabled paisans-f2a9-config-daemon.service"
	probeTimerUnit  = "is-enabled paisans-f2a9-config-sync.timer"
)
```

and `storePlan` calls `profile.ConfigStore(host, cfg.Deployment(), cfg.Sites["home-a"], peers, "/home/ubuntu")`. Delete `probeWireGuard` and every line that sets it, `TestTheUnitsOrderAfterWireGuardByItsFullName`, `TestTheDaemonIsStartedOnlyOnAMeshThatIsUp` and `TestAnInactiveDaemonIsStartedOnlyWhereTheMeshIsUp`, and add:

```go
// Every file of the store is the deployment's user's: under its state
// directory, or its own systemd user directory. Nothing goes to /etc,
// /usr/local or /var/lib.
func TestTheStoreIsTheUsersOwn(t *testing.T) {
	plan := storePlan(t, freshHost(), []string{"10.44.0.2"})
	for path := range fileSteps(plan) {
		if !strings.HasPrefix(path, "/srv/paisans/f2a9/.paisans/") && !strings.HasPrefix(path, "/home/ubuntu/.config/systemd/user/") {
			t.Errorf("the store writes %s, outside the user's own directories", path)
		}
	}
	for _, s := range plan.Steps {
		for _, root := range []string{"/etc/", "/usr/local/", "/var/lib/", "sudo ", "chown", " -o "} {
			if strings.Contains(s.Command, root) {
				t.Errorf("step %q reaches %q: %s", s.Title, root, s.Command)
			}
		}
		if strings.Contains(s.Command, "systemctl ") && !strings.Contains(s.Command, hostprep.UserSystemctl) {
			t.Errorf("step %q runs systemctl other than the user's: %s", s.Title, s.Command)
		}
	}
}

// A user unit cannot be ordered after the system's wg-quick, so the daemon
// retries until the mesh address exists, and never gives up.
func TestTheDaemonRetriesUntilTheMeshIsUp(t *testing.T) {
	unit := storeUnit(storePlan(t, freshHost(), nil), storeDaemon)
	for _, want := range []string{"Restart=always", "RestartSec=10", "StartLimitIntervalSec=0", "WantedBy=default.target"} {
		if !strings.Contains(unit, want) {
			t.Errorf("the daemon unit has no %q:\n%s", want, unit)
		}
	}
	for _, not := range []string{"User=", "BindsTo=", "wg-quick@", "safe.directory"} {
		if strings.Contains(unit, not) {
			t.Errorf("the daemon unit still has %q:\n%s", not, unit)
		}
	}
}

// Enabled and started on every prepare that finds it otherwise, whether or
// not the mesh is up: it binds the mesh address once it can.
func TestAStoppedDaemonIsStartedWhetherOrNotTheMeshIsUp(t *testing.T) {
	host := settled(t, nil)
	host.responses[probeDaemonUnit] = "enabled enabled\nactive inactive\n"
	plan := storePlan(t, host, nil)
	var start string
	for _, s := range plan.Steps {
		if s.Title == "start the configuration store" {
			start = s.Command
		}
	}
	want := hostprep.UserSystemctl + " daemon-reload && " + hostprep.UserSystemctl + " enable --now paisans-f2a9-config-sync.timer && " + hostprep.UserSystemctl + " enable paisans-f2a9-config-daemon.service && " + hostprep.UserSystemctl + " start paisans-f2a9-config-daemon.service"
	if start != want {
		t.Errorf("start is\n%s\nwant\n%s", start, want)
	}
}
```

Rename `TestTheRepositoryIsCreatedOnlyWhereHeadIsAbsentAndStaysReadableByNobody` to `TestTheRepositoryIsCreatedOnlyWhereHeadIsAbsent` and make its expectation the new `repoCommand` (Step 3). In `TestTheDaemonServesOnlyThisDeploymentsRepositoryOnTheMeshAddress`, expect `--base-path=/srv/paisans/f2a9/.paisans/config --export-all /srv/paisans/f2a9/.paisans/config/f2a9.git`. In `TestAChangedDaemonUnitRestartsTheDaemon`, expect `reenable` and `restart` through `hostprep.UserSystemctl`.

In `internal/hostprep/hostprep_test.go`, `configStored` calls `ConfigStore(..., peers, "/home/ubuntu")` and sets `h.files["/srv/paisans/f2a9/.paisans/config/f2a9.git/HEAD"]`.

In `internal/hostprep/configsync_test.go`, the script's paths are the new ones; read the test and replace `/var/lib/paisans/config/f2a9` with the temporary directory the test substitutes, as it does today.

Add to `internal/configstore/remote_test.go`:

```go
// A pull that syncs first starts the user's own sync unit.
func TestPullStartsTheUsersSyncUnit(t *testing.T) {
	h := &recordingHost{}
	_, _, _ = configstore.Pull(h, testDeployment, true)
	if len(h.commands) == 0 || !strings.Contains(h.commands[0], hostprep.UserSystemctl+" start 'paisans-f2a9-config-sync.service'") {
		t.Errorf("first command %q", h.commands)
	}
}
```

using the fake host and deployment the file already defines (read it; use their names in place of `recordingHost` and `testDeployment` where they differ).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/hostprep/ ./internal/configstore/`
Expected: FAIL: `ConfigStore` takes four arguments, `hostprep.UserSystemctl` is undefined, the paths are `/var/lib/paisans/config`.

- [ ] **Step 3: Implement**

Create `internal/hostprep/user.go`:

```go
package hostprep

import (
	"fmt"
	"strings"
)

// UserSystemctl is systemctl for the login user's own manager. A
// non-interactive ssh session does not always set XDG_RUNTIME_DIR, without
// which systemctl --user cannot find the manager admit's linger started.
const UserSystemctl = "XDG_RUNTIME_DIR=/run/user/$(id -u) systemctl --user"

// homeProbe prints the login user's passwd entry, whose sixth field is the
// home sshd and systemd use, whatever HOME the session was given.
const homeProbe = `getent passwd "$(id -un)"`

// Home is the login user's home directory on the host t reaches.
func Home(t Transport) (string, error) {
	out, err := t.Run(homeProbe)
	if err != nil {
		return "", fmt.Errorf("reading the login user's home on %s: %w", t.Describe(), err)
	}
	fields := strings.Split(strings.TrimSpace(out), ":")
	if len(fields) < 7 {
		return "", fmt.Errorf("the login user has no passwd entry on %s that paisans can read: %q", t.Describe(), strings.TrimSpace(out))
	}
	home := strings.TrimSuffix(fields[5], "/")
	if !strings.HasPrefix(home, "/") || strings.ContainsAny(home, " \t\n'\"") {
		return "", fmt.Errorf("the login user's home on %s is %q, not a plain absolute path", t.Describe(), home)
	}
	return home, nil
}

// UserUnitDir is where the login user's own systemd units live.
func UserUnitDir(home string) string { return home + "/.config/systemd/user" }
```

In `internal/hostprep/configstore.go`, replace `configDir` and the path functions:

```go
// ConfigDir holds the deployment's configuration store on a host, in its
// state directory: the repository <token>.git beside <token>.peers and
// <token>.sync.json. The git daemon serves it as its base path, so the token
// alone names the repository, and every peer fetches git://<address>/<token>.git.
func ConfigDir(d deployment.Deployment) string { return d.StatePath("config") }

// ConfigRepo is the deployment's bare repository of configuration versions.
func ConfigRepo(d deployment.Deployment) string { return ConfigDir(d) + "/" + d.Token() + ".git" }

// ConfigPeers lists the mesh addresses the sync fetches from, one per line.
func ConfigPeers(d deployment.Deployment) string { return ConfigDir(d) + "/" + d.Token() + ".peers" }

// ConfigSyncLog is what the last sync reached, written by the sync script.
func ConfigSyncLog(d deployment.Deployment) string { return ConfigDir(d) + "/" + d.Token() + ".sync.json" }

// ConfigIncoming is where a pushed bundle lands for the length of one push,
// beside the repository it is taken into. A push that is cut off can leave
// it behind, so site remove deletes it with the rest of the store.
func ConfigIncoming(d deployment.Deployment) string {
	return ConfigDir(d) + "/" + d.Token() + ".incoming.bundle"
}

// ConfigScriptDir holds the sync script, in the deployment's state directory.
func ConfigScriptDir(d deployment.Deployment) string { return d.StatePath("bin") }

// repoCommand creates the repository where it is absent, the user's and 700
// like the rest of the state directory: the daemon that serves it runs as
// the same user.
func repoCommand(d deployment.Deployment) string {
	return fmt.Sprintf("set -e; umask 077; install -d -m 700 %s %s; git init --bare --quiet %s",
		shellQuote(d.StateDir()), shellQuote(ConfigDir(d)), shellQuote(ConfigRepo(d)))
}
```

`ConfigStore` gains `home string` as its last parameter, the template data gains `"ConfigDir": ConfigDir(d), "Repo": ConfigRepo(d), "Peers": ConfigPeers(d), "Log": ConfigSyncLog(d), "Script": ConfigScriptDir(d) + "/config-sync.sh"` (and drops `WireGuardUnit`), the three unit files go to `UserUnitDir(home) + "/" + <unit>`, and the start becomes:

```go
	daemonEnabled, daemonActive, err := u.userUnitState(t, daemonUnit)
	if err != nil {
		return out, err
	}
	timerEnabled, timerActive, err := u.userUnitState(t, timerUnit)
	if err != nil {
		return out, err
	}
	// The daemon listens on the mesh address, which exists only while
	// WireGuard is up. A user unit cannot be ordered after the system's
	// wg-quick, so the daemon restarts every ten seconds until it can bind,
	// and is started here whether or not the mesh is up yet.
	if unitsChanged || !(daemonEnabled && daemonActive && timerEnabled && timerActive) {
		enable, start := "enable", "start"
		if daemonChanged {
			enable, start = "reenable", "restart"
		}
		command := fmt.Sprintf("%[1]s daemon-reload && %[1]s enable --now %[2]s && %[1]s %[3]s %[4]s && %[1]s %[5]s %[4]s", UserSystemctl, timerUnit, enable, daemonUnit, start)
		out.Steps = append(out.Steps, Step{
			Kinds:    kinds,
			Title:    "start the configuration store",
			Describe: fmt.Sprintf("configuration store: enable %s and %s as the deployment's user, and start them", daemonUnit, timerUnit),
			Command:  command,
		})
	} else {
		out.Present = append(out.Present, fmt.Sprintf("configuration store: %s and %s enabled and running", daemonUnit, timerUnit))
	}
```

Delete the `meshUp` read. In `internal/hostprep/ubuntu.go`, rename the unit probe's command through the template's new `Systemctl` value: `unitState` passes `"Systemctl": "systemctl"`, and add

```go
// userUnitState is unitState for the login user's own manager. activating
// counts as active: a daemon waiting to restart until the mesh is up is
// running as it should.
func (u ubuntu) userUnitState(t Transport, unit string) (enabled, active bool, err error) {
	script, err := snippet(u.tmpl("services-probe.sh.tmpl"), map[string]string{"Unit": unit, "Systemctl": UserSystemctl})
	if err != nil {
		return false, false, err
	}
	out, err := t.Run(script)
	if err != nil {
		return false, false, err
	}
	for _, line := range strings.Split(out, "\n") {
		key, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch key {
		case "enabled":
			enabled = rest == "enabled"
		case "active":
			active = rest == "active" || rest == "activating"
		}
	}
	return enabled, active, nil
}
```

`services-probe.sh.tmpl` becomes:

```sh
printf 'enabled %s\n' "$({{ .Systemctl }} is-enabled {{ .Unit }} 2>/dev/null || true)"
printf 'active %s\n' "$({{ .Systemctl }} is-active {{ .Unit }} 2>/dev/null || true)"
```

`config-daemon.service.tmpl` becomes:

```ini
[Unit]
Description=paisans {{.Token}}: serve configuration versions to mesh peers, read only
# The daemon listens on the mesh address, which exists only while WireGuard
# is up. A user unit cannot be ordered after the system's wg-quick, so it
# restarts every ten seconds until it can bind, and never stops trying.
StartLimitIntervalSec=0

[Service]
ExecStart=/usr/bin/git daemon --reuseaddr --listen={{.Address}} --port=9418 --base-path={{.ConfigDir}} --export-all {{.Repo}}
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
```

`config-sync.service.tmpl`:

```ini
[Unit]
Description=paisans {{.Token}}: fetch configuration versions from mesh peers

[Service]
Type=oneshot
ExecStart={{.Script}}
```

In `config-sync.sh.tmpl`, `repo={{.Repo}}`, `log={{.Log}}` and `done < {{.Peers}}`.

In `internal/hostprep/hostprep.go`, the `Profile` interface's `ConfigStore` gains `home string`, and `Build` reads it before calling:

```go
	home, err := Home(t)
	if err != nil {
		return nil, err
	}
	store, err := profile.ConfigStore(t, d, declared, configPeers(cfg, site), home)
```

In `internal/configstore/remote.go`, `Pull` starts the sync as the user:

```go
		if out, err := t.Run(fmt.Sprintf("if [ -d %s ]; then %s start %s; fi", quote(repo), hostprep.UserSystemctl, quote(d.Prefix()+"-config-sync.service"))); err != nil {
```

and every fake in `internal/configstore/*_test.go` that names `/var/lib/paisans/config` names `/srv/paisans/f2a9/.paisans/config`.

- [ ] **Step 4: Run the touched packages**

Run: `go test ./internal/hostprep/ ./internal/configstore/ ./internal/siteremove/ ./cmd/paisans/`
Expected: PASS. `internal/siteremove` reads the store's paths through these functions, so its fakes follow; where a fake there names `/var/lib/paisans/config` or `/usr/local/lib/paisans-f2a9` literally, change it to the new path.

- [ ] **Step 5: CHANGELOG, the full suite, commit**

Under `### Changed`:

```markdown
- The configuration store lives in `/srv/paisans/<token>/.paisans/config/`, and its git daemon, sync and timer are the deployment user's own systemd units, the daemon retrying until the mesh is up.
```

Run: `go vet ./... && go test ./...`
Expected: PASS.

```bash
git add internal/hostprep internal/configstore internal/siteremove cmd/paisans CHANGELOG.md
git commit -m "feat: run the configuration store as the deployment's user" -m "The store's repository and files were root's, its daemon ran as nobody
from a system unit, and host prepare wrote all of it through sudo. They move
into the deployment's state directory and the user's own systemd units,
under the linger host admit enables. A user unit cannot be ordered after the
system's wg-quick, so the daemon restarts until the mesh address exists.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: The host check as the deployment's user

Rulings 10, 11. `Inspect` stops requiring UID 0, reads listeners with `ss -Hltnue`, and attributes each by UID.

**Files:**
- Modify: `internal/hostcheck/inventory.go`, `internal/hostcheck/parse.go`, `internal/hostcheck/classify.go`
- Test: `internal/hostcheck/inventory_test.go`, `internal/hostcheck/hosts_test.go`, `internal/hostcheck/classify_test.go`, `internal/hostcheck/docker_internal_test.go` (only if it names a removed probe), `cmd/paisans/hostcheck_test.go`, `cmd/paisans/doctor_test.go` (the read allow list)
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: Task 3's `hostSitesProbe(d)`.
- Produces: `hostcheck.Socket{Proto, Address string; Port int; UID int}` (`UID` 0 for a socket `ss` printed no `uid:` for); `hostcheck.Self{UID int; SubStart, SubCount int}` with `func (s Self) Owns(uid int) bool`; `Inventory.Self Self`; removed: `Inventory.Cgroups`, `Inventory.Firewall`, `Inventory.Registered`, `Socket.Process`, `Socket.PID`, `Firewall`, `parseUFW`, `parseCgroups`, `cgroupProbe`, `CgroupPrefix`, `rootProbe`, `ufwProbe`, `firewalldProbe`, `firewallGap`, `firewallSummary`, `owners.keptCaddy`.

- [ ] **Step 1: Write the failing tests**

In `internal/hostcheck/hosts_test.go`, `cleanHost()` answers the self probe instead of `id -u`:

```go
		"uid ": "uid 1001\nsubuid paisans-f2a9:165536:65536\n",
```

keyed by the probe's own substring (`selfProbe` begins `echo "uid $(id -u)"`; key the fake on `echo "uid`), and every socket line in `baseSockets` and the other fixtures loses its `users:(("...",pid=N,fd=M))` and gains ` uid:N` where the listener is not root's: root's (sshd on 22, systemd-resolved on 127.0.0.53:53) print no uid; this deployment's Caddy prints `uid:1001` for 80 and `uid:165536` for 443 (a container's user, inside the range).

Add to `internal/hostcheck/inventory_test.go`, replacing `TestAProbeThatIsNotRootIsRefused`:

```go
// The inventory runs as the deployment's user: no probe asks for root, and
// none reads what only root may (ufw, firewalld, the registry, other users'
// process tables).
func TestTheInventoryRunsAsTheDeploymentsUser(t *testing.T) {
	h := cleanHost()
	inv, err := hostcheck.Inspect(h, fixtureDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Self != (hostcheck.Self{UID: 1001, SubStart: 165536, SubCount: 65536}) {
		t.Errorf("self %+v", inv.Self)
	}
	for _, c := range h.ran {
		for _, root := range []string{"ufw", "firewalld", "/proc/", "ss -Hltnup", "registry.json", "sudo"} {
			if strings.Contains(c, root) {
				t.Errorf("a probe reads %q, which needs root: %s", root, c)
			}
		}
	}
}

// ss -e prints uid:N for a socket of any user but root.
func TestSocketsCarryTheirOwner(t *testing.T) {
	h := cleanHost()
	h.answers["ss -Hltnue"] = "tcp LISTEN 0 4096 0.0.0.0:443 0.0.0.0:* uid:165536 ino:2 sk:1\ntcp LISTEN 0 128 0.0.0.0:22 0.0.0.0:* ino:3 sk:2\n"
	inv, err := hostcheck.Inspect(h, fixtureDeployment)
	if err != nil {
		t.Fatal(err)
	}
	want := []hostcheck.Socket{{Proto: "tcp", Port: 443, UID: 165536}, {Proto: "tcp", Port: 22, UID: 0}}
	if !reflect.DeepEqual(inv.Sockets, want) {
		t.Errorf("sockets %+v", inv.Sockets)
	}
}
```

(use the fake's map and `ran` field names as `hosts_test.go` defines them; add `reflect` to the imports).

Add to `internal/hostcheck/classify_test.go`:

```go
// The deployment's own listeners, its user's UID or one in its subordinate
// range, are ours on a re-apply; root's and another user's are taken.
func TestTheUsersOwnListenersAreOurs(t *testing.T) {
	inv := &hostcheck.Inventory{Host: "vm", Self: hostcheck.Self{UID: 1001, SubStart: 165536, SubCount: 65536}, Sockets: []hostcheck.Socket{
		{Proto: "tcp", Port: 80, UID: 1001},
		{Proto: "tcp", Port: 443, UID: 165536 + 70},
	}}
	if r := hostcheck.Classify(gatewayClaims(t), inv); r.Refusal() != nil {
		t.Errorf("the deployment's own Caddy refused: %v", r.Refusal())
	}
	for _, uid := range []int{0, 1002, 165536 + 65536} {
		inv.Sockets = []hostcheck.Socket{{Proto: "tcp", Port: 443, UID: uid}}
		r := hostcheck.Classify(gatewayClaims(t), inv)
		if r.Refusal() == nil || !strings.Contains(r.Refusal().Error(), "443") {
			t.Errorf("uid %d on 443 was not refused: %v", uid, r.Refusal())
		}
	}
}
```

where `gatewayClaims(t)` is the claims the file already builds for a gateway site (read `classify_test.go` and reuse its helper; if none builds a gateway's, build one with `hostcheck.ClaimsFor` over the fixture's gateway site).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/hostcheck/`
Expected: FAIL: `inv.Self` undefined, `Socket.UID` undefined.

- [ ] **Step 3: Implement the probes**

In `internal/hostcheck/inventory.go`, the probe comment says the probes run as the deployment's user, and the constants become:

```go
const (
	// selfProbe names the login user's UID and its subordinate range, which
	// a rootless container's users map into, so a listener can be told the
	// deployment's own by its owner. /etc/subuid is world readable.
	selfProbe      = `echo "uid $(id -u)"; echo "subuid $(grep "^$(id -un):" /etc/subuid | head -n 1)"`
	dockerProbe    = `command -v docker >/dev/null 2>&1 || { echo absent; exit 0; }; docker version --format '{{.Server.Version}} {{.Client.Version}}' || exit 1; echo "compose $(docker compose version --short 2>/dev/null || echo absent)"`
	packageProbe   = `dpkg-query -W -f='${Package} ${Status}\n' docker-ce docker.io 2>/dev/null; snap list docker 2>/dev/null; true`
	containerProbe = `...` // unchanged
	volumeProbe    = `...` // unchanged
	networkProbe   = `...` // unchanged
	// socketProbe lists listeners with their owner's UID (-e) and without
	// their process (-p), which only root can name for another user's.
	socketProbe = "ss -Hltnue"
	linkProbe   = "ip -o link"
	routeProbe  = "ip -j route"
	cLocale     = "export LC_ALL=C; "
)
```

(keep the three Docker probes' text exactly as it is). `ReadCommands` lists `selfProbe, dockerProbe, packageProbe, containerProbe, volumeProbe, networkProbe, socketProbe, linkProbe, routeProbe` and, last, `hostSitesProbe(d)`, so it takes `d deployment.Deployment`; `doctor_test.go` passes the fixture's deployment. Delete `CgroupPrefix`, `cgroupProbe` and `listeningPIDs`.

Add to `inventory.go`:

```go
// Self is the login user and the subordinate range its containers' users map
// into: everything a rootless Docker runs as is one of them.
type Self struct {
	UID                int
	SubStart, SubCount int
}

// Owns reports whether uid is the user's or one its containers run as.
func (s Self) Owns(uid int) bool {
	return uid == s.UID || (s.SubCount > 0 && uid >= s.SubStart && uid < s.SubStart+s.SubCount)
}

// parseSelf reads selfProbe's two lines.
func parseSelf(out string) (Self, error) {
	var s Self
	for _, line := range strings.Split(out, "\n") {
		key, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch key {
		case "uid":
			uid, err := strconv.Atoi(rest)
			if err != nil {
				return s, fmt.Errorf("unreadable uid %q", rest)
			}
			s.UID = uid
		case "subuid":
			f := strings.Split(rest, ":")
			if len(f) == 3 {
				s.SubStart, _ = strconv.Atoi(f[1])
				s.SubCount, _ = strconv.Atoi(f[2])
			}
		}
	}
	return s, nil
}
```

In `Inspect`, the first read is `run("asking who the probes run as", selfProbe)` into `inv.Self` through `parseSelf`, with no UID check; delete the cgroup read, the ufw read, the firewalld read and the registry read, and the `Cgroups`, `Firewall` and `Registered` fields and their comments.

In `internal/hostcheck/parse.go`, `Socket` becomes `{Proto, Address string; Port int; UID int}` and `parseSockets` reads the owner:

```go
var uidField = regexp.MustCompile(`\buid:(\d+)\b`)
```

```go
		s := Socket{Proto: f[0], Address: bindAddress(local[:i]), Port: port}
		if m := uidField.FindStringSubmatch(line); m != nil {
			s.UID, _ = strconv.Atoi(m[1])
		}
```

Delete `processField`, `parseUFW`, `Firewall` and `parseCgroups`.

- [ ] **Step 4: Attribute listeners by owner**

In `internal/hostcheck/classify.go`, `owners.socket` becomes:

```go
// socket decides who holds a listener, by its owner. Every process a
// rootless Docker runs, the daemon's port forwarder included, is the user's
// UID or one in its subordinate range, so that is this deployment's. The
// kernel's own WireGuard socket has no owner but root, and is the toolkit's
// when the toolkit wrote the interface it serves. A listener of root's is the
// base system's; one of another user's makes the host shared.
func (o owners) socket(s Socket) holder {
	if o.inv.Self.Owns(s.UID) {
		return holder{ours: true, desc: "this deployment's user"}
	}
	if s.UID == 0 && s.Proto == "udp" && o.port != 0 && s.Port == o.port && o.inv.ManifestWireGuard && contains(o.inv.Links, o.iface) {
		return holder{ours: true, desc: "WireGuard (" + o.iface + ")"}
	}
	if s.UID == 0 {
		return holder{base: true, desc: "a listener of root's"}
	}
	return holder{desc: fmt.Sprintf("a listener of another user's (uid %d)", s.UID)}
}
```

Delete `keptCaddy` and its call in `container`, `sshArg` if nothing else calls it, `baseProcesses` if nothing else reads it, the firewall gap check in `Classify` and the firewall line in `Show` with `firewallGap` and `firewallSummary`. Remove the `registry` import.

- [ ] **Step 5: Run the touched packages**

Run: `go test ./internal/hostcheck/ ./internal/siteremove/ ./internal/ownership/ ./cmd/paisans/`
Expected: PASS after the fakes in `cmd/paisans/hostcheck_test.go`, `doctor_test.go` and `internal/siteremove` answer `selfProbe` instead of `id -u` and give their socket lines `uid:` fields instead of `users:(...)`. A test that asserted a firewall gap refusal (`webHost(ufw)` in `cmd/paisans/hostcheck_test.go`) is deleted: admit owns the firewall.

- [ ] **Step 6: CHANGELOG, the full suite, commit**

Under `### Changed`:

```markdown
- The host check runs as the deployment's user: it reads listeners with `ss -Hltnue`, counts its own user's and containers' as this deployment's, and leaves the firewall to `host admit`.
```

Run: `go vet ./... && go test ./...`
Expected: PASS.

```bash
git add internal/hostcheck internal/siteremove internal/ownership cmd/paisans CHANGELOG.md
git commit -m "feat: run the host check as the deployment's user" -m "The inventory refused any login but root, read process names with ss -p
and the firewall through ufw, all of which need root. As the deployment's
user it reads listeners with their owner's uid instead: its own uid and its
subordinate range are this deployment's, root's are the base system, any
other user's is taken. The firewall is host admit's.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Volume prune reads sizes from Docker

Ruling 12. *Observed* R8: `du` on a volume was denied below a directory owned by a container user; `docker system df -v` reported its size.

**Files:**
- Modify: `internal/apply/volumeprune.go`, `internal/apply/disk.go` (or wherever `FormatSize` is defined: `grep -n 'func FormatSize' internal/apply`)
- Test: `internal/apply/volumeprune_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Produces: `DanglingVolume.Size int64`, -1 when Docker reported no readable size; `func apply.ParseDockerSize(s string) (int64, bool)`; `apply.FormatSize` prints `size unknown` for a negative size.

- [ ] **Step 1: Write the failing tests**

In `internal/apply/volumeprune_test.go`, the fake answers the new probe, keyed by its first command, and `listing` takes the sizes Docker reports:

```go
func (h *volumeHost) Run(command string) (string, error) {
	h.commands = append(h.commands, command)
	if strings.Contains(command, "docker system df -v --format") {
		return h.listing, nil
	}
	// ... the removal branch, unchanged
}

// listing is the probe's answer: Docker's volume sizes as JSON, then one
// line per dangling volume, then the end marker.
func listing(sizes string, lines ...string) string {
	return sizes + "\nvolumes\n" + strings.Join(lines, "\n") + "\nend\n"
}
```

Each existing call `listing(lines...)` gains a first argument, the sizes as `docker system df -v --format '{{json .Volumes}}'` prints them, Eg:

```go
	sizes := `[{"Name":"` + leaked1 + `","Size":"48.38MB","Links":"0"},{"Name":"` + leaked2 + `","Size":"0B","Links":"0"}]`
```

and each volume line drops its size field: `volume\t<name>\t<labels>\t<entries>`. Add:

```go
// Sizes are Docker's, read back from its human readable form, since du
// cannot read a volume a container's user owns.
func TestParseDockerSize(t *testing.T) {
	for in, want := range map[string]int64{"0B": 0, "512B": 512, "1.5kB": 1500, "48.38MB": 48380000, "2GB": 2000000000, "1.2TB": 1200000000000} {
		if got, ok := apply.ParseDockerSize(in); !ok || got != want {
			t.Errorf("%s: %d %v, want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "N/A", "12", "1.2XB", "-3e6B"} {
		if _, ok := apply.ParseDockerSize(bad); ok {
			t.Errorf("%q read as a size", bad)
		}
	}
}

// A volume Docker gives no readable size for is still decided, with its
// size unknown, never zero.
func TestAVolumeWithoutASizeIsUnknown(t *testing.T) {
	h := &volumeHost{listing: listing(`[{"Name":"`+leaked1+`","Size":"N/A"}]`, "volume\t"+leaked1+"\t"+ours+"\tcache")}
	p, err := apply.BuildVolumePrune(dep, "home-a", h)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Volumes) != 1 || p.Volumes[0].Size != -1 || apply.FormatSize(-1) != "size unknown" {
		t.Errorf("volumes %+v", p.Volumes)
	}
	if strings.Contains(h.commands[0], "du ") {
		t.Errorf("the probe still runs du: %s", h.commands[0])
	}
}
```

(`dep` is the deployment the file already uses; read it and use its name.)

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/apply/ -run 'Prune|DockerSize|WithoutASize'`
Expected: FAIL, `undefined: apply.ParseDockerSize`.

- [ ] **Step 3: Implement**

In `internal/apply/volumeprune.go`, `Size`'s comment says "what Docker's `system df -v` reports for it, in bytes, -1 when it reported none it could read", and the probe becomes:

```go
// danglingProbe prints Docker's size for every volume, then each dangling
// volume's labels and top level entries, one tab separated line each, and
// ends with a marker so a cut short answer is not read as a short list. The
// sizes come from Docker, which reads them as the daemon: the deployment's
// user cannot read a directory a container's user owns, so du on a volume's
// mountpoint can be denied. ls of the top level is allowed to fail the same
// way, and then lists nothing.
const danglingProbe = `set -e; docker system df -v --format '{{json .Volumes}}'; echo volumes; vs=$(docker volume ls -qf dangling=true); for v in $vs; do m=$(docker volume inspect --format '{{.Mountpoint}}' "$v") || continue; l=$(docker volume inspect --format '{{json .Labels}}' "$v") || continue; e=$(ls -A "$m" 2>/dev/null | head -n 10 | paste -sd ' ' -); printf 'volume\t%s\t%s\t%s\n' "$v" "$l" "$e"; done; echo end`
```

`BuildVolumePrune` reads the first line as the sizes:

```go
	head, rest, _ := strings.Cut(out, "\nvolumes\n")
	var listed []struct{ Name, Size string }
	if err := json.Unmarshal([]byte(strings.TrimSpace(head)), &listed); err != nil {
		return nil, fmt.Errorf("%s: reading Docker's volume sizes: %w", site, err)
	}
	sizes := map[string]int64{}
	for _, v := range listed {
		if n, ok := ParseDockerSize(v.Size); ok {
			sizes[v.Name] = n
		}
	}
```

then parses `rest` line by line with `fields[1]` the name, `fields[2]` the labels and `fields[3]` the entries, and sets `v.Size` from `sizes`, `-1` when absent. Add:

```go
// dockerSize is a size as Docker prints one (go-units HumanSize): a decimal
// number and a unit in powers of 1000.
var dockerSize = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(B|kB|KB|MB|GB|TB|PB)$`)

var dockerUnits = map[string]float64{"B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "PB": 1e15}

// ParseDockerSize reads a size Docker printed, Eg: 48.38MB. Anything else,
// N/A included, is not a size.
func ParseDockerSize(s string) (int64, bool) {
	m := dockerSize.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return int64(math.Round(n * dockerUnits[m[2]])), true
}
```

and in `FormatSize`, first: `if n < 0 { return "size unknown" }`. In `cmd/paisans/prune.go`, the total adds only sizes that are not negative.

- [ ] **Step 4: Run, CHANGELOG, full suite, commit**

Run: `go test ./internal/apply/ ./cmd/paisans/`
Expected: PASS.

Under `### Changed`:

```markdown
- `paisans prune` reads each dangling volume's size from `docker system df -v`, which the deployment's user can read for a volume a container's user owns.
```

Run: `go vet ./... && go test ./...`, then:

```bash
git add internal/apply cmd/paisans CHANGELOG.md
git commit -m "feat: read dangling volumes' sizes from docker system df" -m "du on a volume's mountpoint is denied to the deployment's user below a
directory a container's user owns (spike R8). Docker reads the size as the
daemon, so the prune asks it instead, and says size unknown for a volume it
gives none for rather than reading it as empty.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: The mesh probe reads the user's `daemon.json`

Ruling 13.

**Files:**
- Modify: `internal/mesh/mesh.go`, `internal/mesh/mesh_test.go`
- Modify: `internal/preflight/preflight.go`, `cmd/paisans/mesh.go` (`checkMeshLive`, `chooseMesh`), `internal/doctor/mesh.go` (the advice line), and the doctor read in `cmd/paisans/doctor.go`
- Modify: their tests' fakes (`internal/preflight/preflight_test.go`, `cmd/paisans/*_test.go`), `CHANGELOG.md`

**Interfaces:**
- Produces: `func mesh.ProbeUser(h Host) (Probed, error)`; `mesh.UserDaemonConfig = "~/.config/docker/daemon.json"` (a name for messages); `const mesh.UserDaemonCommand`; `Probed.DaemonPath string`, the file the pools came from, which `Taken` and `Pools` name; `func Pools(daemonJSON string, found bool, path string) ([]Taken, error)`.

- [ ] **Step 1: Write the failing test**

Add to `internal/mesh/mesh_test.go`:

```go
// The deployment's user reads its own daemon's pools, through its own shell
// so that ~ is its home; the system daemon's file is admit's to read.
func TestProbeUserReadsTheUsersDaemonJSON(t *testing.T) {
	h := &probeHost{answers: map[string]string{
		mesh.RouteCommand:      "[]",
		mesh.AddrCommand:       "[]",
		"docker network":       "[]",
		mesh.UserDaemonCommand: "present\n" + `{"default-address-pools":[{"base":"10.44.0.0/16","size":24}]}`,
	}}
	p, err := mesh.ProbeUser(h)
	if err != nil {
		t.Fatal(err)
	}
	taken, err := p.Taken("psns-f2a9", "home-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(taken) != 1 || !strings.Contains(taken[0].What, "~/.config/docker/daemon.json") {
		t.Errorf("taken %+v", taken)
	}
	if len(h.read) != 0 {
		t.Errorf("read %v directly; the user's file is read through its shell", h.read)
	}
	h.answers[mesh.UserDaemonCommand] = "absent\n"
	if p, err = mesh.ProbeUser(h); err != nil || p.DaemonFound {
		t.Errorf("absent read as found: %+v %v", p, err)
	}
}
```

with, if the file has no such fake:

```go
type probeHost struct {
	answers map[string]string
	read    []string
}

func (h *probeHost) Run(command string) (string, error) {
	for k, v := range h.answers {
		if strings.Contains(command, k) {
			return v, nil
		}
	}
	return "", fmt.Errorf("unexpected %q", command)
}
func (h *probeHost) ReadFile(p string) (string, bool, error) { h.read = append(h.read, p); return "", false, nil }
func (h *probeHost) Describe() string                        { return "fake" }
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/mesh/ -run TestProbeUser`
Expected: FAIL, `undefined: mesh.ProbeUser`.

- [ ] **Step 3: Implement**

In `internal/mesh/mesh.go`:

```go
const (
	// UserDaemonConfig names the deployment user's daemon configuration in
	// messages: its rootless Docker reads ~/.config/docker/daemon.json.
	UserDaemonConfig = "~/.config/docker/daemon.json"
	// UserDaemonCommand prints whether the user's daemon.json is there and
	// then its content, through the user's own shell, so $HOME is its home.
	UserDaemonCommand = `f="$HOME/.config/docker/daemon.json"; if [ -f "$f" ]; then echo present; cat -- "$f"; else echo absent; fi`
)
```

`Probed` gains `DaemonPath string`. `Probe` sets `p.DaemonPath = DaemonConfig` and its comment says it reads the system daemon's configuration, which `host admit` checks as root. Add:

```go
// ProbeUser is Probe for the deployment's user: its routes and addresses,
// which are the host's, its own daemon's networks (the transport points
// docker at it), and its own daemon's pools, from UserDaemonConfig.
func ProbeUser(h Host) (Probed, error) {
	var p Probed
	for _, step := range []struct {
		name, command string
		into          *string
	}{
		{RouteCommand, RouteCommand, &p.Routes},
		{AddrCommand, AddrCommand, &p.Addrs},
		{"docker network inspect", NetworksCommand, &p.Networks},
	} {
		out, err := h.Run(step.command)
		if err != nil {
			return p, fmt.Errorf("%s: `%s` failed: %v: %s", h.Describe(), step.name, err, strings.TrimSpace(out))
		}
		*step.into = out
	}
	out, err := h.Run(UserDaemonCommand)
	if err != nil {
		return p, fmt.Errorf("%s: reading %s: %v: %s", h.Describe(), UserDaemonConfig, err, strings.TrimSpace(out))
	}
	state, content, _ := strings.Cut(out, "\n")
	p.DaemonPath = UserDaemonConfig
	p.DaemonFound = strings.TrimSpace(state) == "present"
	if p.DaemonFound {
		p.Daemon = content
	}
	return p, nil
}
```

`Pools` takes the path it names: `func Pools(daemonJSON string, found bool, path string) ([]Taken, error)`, with every `DaemonConfig` in its messages replaced by `path`; `Taken` calls `Pools(p.Daemon, p.DaemonFound, p.DaemonPath)`, and an empty `DaemonPath` (a `Probed` built by hand in a test) names `DaemonConfig`.

Every deployment command's probe becomes `ProbeUser`: `internal/preflight/preflight.go` (`meshClear`), `cmd/paisans/mesh.go` (`checkMeshLive`, and the probe in `chooseMesh`), and doctor's mesh read in `cmd/paisans/doctor.go` (`grep -n 'mesh.Probe(' cmd internal`; leave `internal/hostadmit`'s, which runs as root). In `internal/doctor/mesh.go`, the advice names `mesh.UserDaemonConfig` and "a restart of the deployment's Docker" for the pools.

- [ ] **Step 4: Update the fakes, run, CHANGELOG, commit**

The fakes that answer the mesh probe in `internal/preflight` and `cmd/paisans` answer `mesh.UserDaemonCommand` (`"absent\n"` where they had no daemon.json, `"present\n<json>"` where they had one) instead of serving `/etc/docker/daemon.json` from their files.

Run: `go test ./internal/mesh/ ./internal/preflight/ ./internal/doctor/ ./cmd/paisans/`
Expected: PASS.

Under `### Changed`:

```markdown
- The mesh subnet check reads the deployment's own Docker pools from `~/.config/docker/daemon.json`.
```

Run: `go vet ./... && go test ./...`, then:

```bash
git add internal/mesh internal/preflight internal/doctor cmd/paisans CHANGELOG.md
git commit -m "feat: check the mesh against the user's own docker pools" -m "A deployment's networks live in its user's rootless daemon, whose pools
are in ~/.config/docker/daemon.json, not in the system daemon's file. The
deployment's commands read that through the user's own shell; host admit
keeps reading the system daemon's, as root.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Keys in the user's own `authorized_keys`

Spec, *Keys*. The deployment's user is its own, so no other deployment shares its file: the sidecar is `StatePath("authorized_keys.owned")`, the lock is the deployment's, and nothing is written as root.

**Files:**
- Modify: `internal/hostprep/authorized_keys.go`, `internal/hostprep/leave.go`, `internal/hostprep/admit.go` (phase 5's sidecar exports), `internal/hostadmit/record.go` (phase 5's sidecar path)
- Modify: `internal/siteremove/host.go` (`probeKeys`, the key records' removal, `hostGoneLeft`)
- Test: `internal/hostprep/hostprep_test.go` (the key tests)
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: Task 2's `StatePath`, `StateLockCommand`; phase 5's `hostprep.OwnedKey`, `ParseOwnedKeys`, `OwnedKeysFile`.
- Produces: `func hostprep.OwnedKeysPath(d deployment.Deployment) string` (`/srv/paisans/<token>/.paisans/authorized_keys.owned`); `func hostprep.Locked(d deployment.Deployment, command string) string` (the command under `d.StateLockCommand("the keys")`); `planAuthorizedKeys(t Transport, d deployment.Deployment, home string, s config.SSH) (Section, []Step, error)`. Removed: `ownedDir`, `keysLock`, `OwnedKeysGlob`, `OwnedKeysPrefixGlob`, `passwdProbe`, `readUserKeys`'s passwd read, the `share` step and `claimedBy`.

- [ ] **Step 1: Write the failing tests**

In `internal/hostprep/hostprep_test.go`, `ownedKeys` becomes `"/srv/paisans/f2a9/.paisans/authorized_keys.owned"`, and the special listing branch in `fakeHost.Run` for `/etc/paisans/authorized_keys.ubuntu.paisans-*.owned` is deleted. Delete `TestAKeyAnotherDeploymentClaimsIsShared`, `TestAKeyStillClaimedIsReleasedNotRemoved` and `TestTheRemovalChecksTheOtherSidecarsWhenItRuns`: a user is one deployment's. Add:

```go
// Every key step runs as the deployment's user, under the deployment's own
// lock, writing only its own home and its own state directory.
func TestKeyStepsAreTheUsersOwn(t *testing.T) {
	host := freshHost()
	delete(host.files, authorizedKeys)
	delete(host.files, ownedKeys)
	cfg := fixture(t)
	s := cfg.Sites["home-a"]
	s.SSH.Keys = append(s.SSH.Keys, config.NamedKey{Name: "bob", Line: keyBob})
	cfg.Sites["home-a"] = s
	plan, err := hostprep.Build("home-a", cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	lock := cfg.Deployment().StateLockCommand("the keys")
	var keySteps int
	for _, step := range plan.Steps {
		if !slices.Contains(step.Kinds, hostprep.KindSSHKeys) {
			continue
		}
		keySteps++
		for _, root := range []string{"/etc/paisans", "chown", " -o ", "sudo"} {
			if strings.Contains(step.Command, root) {
				t.Errorf("%q reaches %q: %s", step.Title, root, step.Command)
			}
		}
		if step.Title != "create authorized_keys" && !strings.Contains(step.Command, lock) {
			t.Errorf("%q is not under the deployment's lock: %s", step.Title, step.Command)
		}
	}
	if keySteps < 3 {
		t.Errorf("planned %d key steps, want create and two authorizations", keySteps)
	}
}
```

(add `slices` to the imports). Update every test that reads `host.files[ownedKeys]` or expects a `Locked(...)` text to the new path and lock, and `TestAMissingUserIsRefused` to expect the home read's error (Task 4's `Home`) when `getent` prints nothing.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/hostprep/ -run 'Key|Keys'`
Expected: FAIL: the plan's commands name `/etc/paisans` and `install -d -m 700 -o`.

- [ ] **Step 3: Implement**

In `internal/hostprep/authorized_keys.go`, rewrite the opening comment for a user of its own: the deployment's user's `authorized_keys`, which host admit first wrote and the operators maintain from `ssh.keys`; a key someone else put there (an operator by hand, with options or without) is never removed; the sidecar `/srv/paisans/<token>/.paisans/authorized_keys.owned` records what host prepare and host admit added; every write runs under the deployment's lock. Then:

```go
// OwnedKeysPath is the sidecar recording which keys of the deployment
// user's authorized_keys host admit and host prepare added.
func OwnedKeysPath(d deployment.Deployment) string { return d.StatePath("authorized_keys.owned") }

// Locked runs command under the deployment's lock, in its state directory.
func Locked(d deployment.Deployment, command string) string {
	return d.StateLockCommand("the keys") + "; " + command
}
```

`readUserKeys` becomes `readUserKeys(t Transport, home string) (userKeys, error)`, with `k.dir = home + "/.ssh"`, and no passwd read. `planAuthorizedKeys(t Transport, d deployment.Deployment, home string, s config.SSH)`:

- reads `OwnedKeysPath(d)` only, and no other sidecar;
- the create step is `fmt.Sprintf("set -e; umask 077; [ -d %[1]s ] || install -d -m 700 %[1]s; [ -f %[2]s ] || install -m 600 /dev/null %[2]s", shellQuote(dir), shellQuote(file))`;
- the `claimedBy` branch of the listed keys and of the unlisted keys is deleted (a `shared` line a sidecar holds, which phase 5's format still parses, is released as today: `markShared` lines are never removed from the file);
- every `Locked(...)` becomes `Locked(d, ...)`;
- `removeUnclaimed` is replaced by `removeLines(file, raws) + "; " + writeOwned(d, owned)` under `Locked(d, ...)`;
- `writeOwned(d, entries)` writes `OwnedKeysPath(d)` through a temporary file in the state directory, `chmod 600`, `mv`, and its header names "this deployment's user's authorized_keys";
- `removeLines` drops `chown --reference`, keeping `chmod --reference`: the user owns both files.

`appendKey`'s comment says it appends as the file's owner. In `internal/hostprep/leave.go`, delete `OwnedKeysPrefixGlob`. In `internal/hostprep/admit.go`, `OwnedKeysFile` writes the header `writeOwned` writes (read the two and make them one function's output). In `internal/hostadmit/record.go` (phase 5's keys part), the sidecar path is `hostprep.OwnedKeysPath(p.d)` wherever phase 5 spelled it.

In `internal/hostprep/hostprep.go`, `Build` passes `home` (read once in Task 4) to `planAuthorizedKeys(t, d, home, declared.SSH)`.

In `internal/siteremove/host.go`, `probeKeys` checks whether `hostprep.OwnedKeysPath(p.dep())` exists (`t.ReadFile`) and records it in `hp.keyRecords` when it does; the removal deletes it with `rm -f --` under `hostprep.Locked(d, ...)`; `hostGoneLeft` names `hostprep.OwnedKeysPath(d)`.

- [ ] **Step 4: Run, CHANGELOG, full suite, commit**

Run: `go test ./internal/hostprep/ ./internal/hostadmit/ ./internal/siteremove/ ./cmd/paisans/`
Expected: PASS.

Under `### Changed`:

```markdown
- `host prepare` maintains the deployment user's own `authorized_keys` as that user, recording what it added in `/srv/paisans/<token>/.paisans/authorized_keys.owned`.
```

Run: `go vet ./... && go test ./...`, then:

```bash
git add internal/hostprep internal/hostadmit internal/siteremove cmd/paisans CHANGELOG.md
git commit -m "feat: keep the deployment user's keys as that user" -m "Each deployment has a user of its own, so no other deployment shares its
authorized_keys and nothing needs root to write it. The sidecar moves into
the deployment's state directory, every write takes the deployment's lock,
and the sharing between deployments of one login user goes with the shared
user.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 9: The admission record replaces the registry claim

Ruling 15. Every command that wrote to a host claimed it in the registry first; the registry is root's and admit claims it. The same places now read the admission record as the deployment's user and refuse a host whose record does not cover the site.

**Files:**
- Modify: `internal/admission/record.go` (`Differences`, `ForSite`), `internal/admission/record_test.go`
- Rename: `cmd/paisans/claim.go` to `cmd/paisans/admitted.go`; `cmd/paisans/claim_test.go` to `cmd/paisans/admitted_test.go`
- Modify: every caller of `claimHosts` and `claimSites`: `cmd/paisans/appremove.go`, `failover.go`, `gateway.go`, `main.go` (three), `meshrekey.go`, `operatormagiclink.go`, `oidc.go`, `prune.go`, `rotatekey.go`, `secretsrotate.go`, `site.go`, `siteremove.go`, `storage.go`
- Modify: `internal/preflight/preflight.go`, `internal/preflight/preflight_test.go`
- Modify: the `cmd/paisans` test fakes that serve `registry.Path` for a claim (Step 5)
- Modify: `cmd/paisans/help.go` (the registry passage at the claim), `CHANGELOG.md`

**Interfaces:**
- Consumes: `admission.Read`, `admission.ErrNotAdmitted`, `admission.Needs`; Task 1's `Record.Site`.
- Produces: `func (r Record) Differences(site string, s config.Site) []string`, each a sentence naming what the record holds and what the declaration says, in a fixed order (site, user, SSH port, WireGuard port, forwarding, each missing grant, web server); `func ForSite(t Reader, deploymentID, site string, s config.Site) (Record, error)` returning the record, or an error wrapping `ErrNotAdmitted`, or `*SiteError{Site string; Problems []string}`; `func admittedHosts(r ui.Reporter, cfg *config.Config, transports map[string]registry.Runner) (map[string]admission.Record, error)`; `func admittedSites(r ui.Reporter, cfg *config.Config, sites ...string) (map[string]admission.Record, error)`; `func admissionProblem(site string, err error) error`, the operator's words for either refusal.

- [ ] **Step 1: Write the failing tests**

Add to `internal/admission/record_test.go`:

```go
// Every difference between what the owner admitted and what the
// declaration says is named; none when they agree.
func TestDifferences(t *testing.T) {
	cfg := fixture(t)
	rec := admission.NewRecord(build(t, "home-a"), admission.Grants{Watchdog: true, PublicPorts: true}, "", at)
	if d := rec.Differences("home-a", cfg.Sites["home-a"]); len(d) != 0 {
		t.Fatalf("an exact admission differs: %v", d)
	}
	rec.Grants.Watchdog = false
	rec.Ports.SSH = 2222
	d := rec.Differences("home-a", cfg.Sites["home-a"])
	if len(d) != 2 || !strings.Contains(d[0], "ssh port") || !strings.Contains(d[1], "--watchdog") {
		t.Errorf("differences %q", d)
	}
	if d := rec.Differences("home-b", cfg.Sites["home-b"]); len(d) == 0 || !strings.Contains(d[0], "admitted as site home-a") {
		t.Errorf("another site's record: %q", d)
	}
}

// ForSite reads the record as the deployment's user and judges it.
func TestForSite(t *testing.T) {
	cfg := fixture(t)
	h := &host{files: map[string]string{}}
	if _, err := admission.ForSite(h, cfg.ID, "home-a", cfg.Sites["home-a"]); !errors.Is(err, admission.ErrNotAdmitted) {
		t.Errorf("no record: %v", err)
	}
	h.files["/etc/paisans/admitted.f2a9.json"] = string(encode(t, admission.NewRecord(build(t, "home-a"), admission.Grants{}, "", at)))
	_, err := admission.ForSite(h, cfg.ID, "home-a", cfg.Sites["home-a"])
	var se *admission.SiteError
	if !errors.As(err, &se) || se.Site != "home-a" || len(se.Problems) != 2 {
		t.Errorf("grants missing: %v", err)
	}
}
```

Rename `cmd/paisans/claim_test.go` to `admitted_test.go` (`git mv`) and replace its tests with:

```go
// A host that has not admitted the deployment stops the command before
// anything changes, with the lines that admit it.
func TestAnUnadmittedHostIsRefused(t *testing.T) {
	cfg := fixtureConfig(t)
	h := &recordedHost{files: map[string]string{}}
	_, err := admittedHosts(&ui.Recorder{}, cfg, map[string]registry.Runner{"home-a": h})
	if err == nil {
		t.Fatal("an unadmitted host was accepted")
	}
	hint, explain := ui.Describe(err)
	for _, want := range []string{"home-a is not admitted", "paisans host request --site home-a > home-a.admit", "sudo paisans host admit --request home-a.admit"} {
		if !strings.Contains(hint+"\n"+explain, want) {
			t.Errorf("no %q in:\n%s\n%s", want, hint, explain)
		}
	}
	for _, c := range h.commands {
		if strings.Contains(c, "registry") {
			t.Errorf("the check reached the registry: %s", c)
		}
	}
}

// A record that covers the site lets the command through, and hands the
// caller the record.
func TestAnAdmittedHostIsAccepted(t *testing.T) {
	cfg := fixtureConfig(t)
	h := &recordedHost{files: map[string]string{admittedPath(cfg): admittedRecord(t, cfg, "home-a")}}
	recs, err := admittedHosts(&ui.Recorder{}, cfg, map[string]registry.Runner{"home-a": h})
	if err != nil || recs["home-a"].User != cfg.Sites["home-a"].SSHUser() {
		t.Fatalf("records %+v, %v", recs, err)
	}
}
```

with test helpers in the same file:

```go
// recordedHost serves files and keeps every command.
type recordedHost struct {
	files    map[string]string
	commands []string
}

func (h *recordedHost) Run(c string) (string, error) { h.commands = append(h.commands, c); return "", nil }
func (h *recordedHost) ReadFile(p string) (string, bool, error) {
	c, ok := h.files[p]
	return c, ok, nil
}
func (h *recordedHost) Describe() string { return "fake" }

func admittedPath(cfg *config.Config) string {
	p, _ := admission.Path(cfg.Deployment().Token())
	return p
}

// admittedRecord is the record admit writes for site with every grant its
// roles need.
func admittedRecord(t *testing.T, cfg *config.Config, site string) string {
	t.Helper()
	req, err := admission.Build(cfg, site)
	if err != nil {
		t.Fatal(err)
	}
	b, err := admission.EncodeRecord(admission.NewRecord(req, req.Grants, "", time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
```

In `internal/preflight/preflight_test.go`, the test at the registry check (around line 509) becomes one where `home-b` has no admission record, and expects the refusal named `admission` naming `paisans host request --site home-b`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/admission/ -run 'TestDifferences|TestForSite'; go test ./cmd/paisans/ -run 'TestAnUnadmitted|TestAnAdmitted'`
Expected: FAIL, `rec.Differences undefined`, `undefined: admittedHosts`.

- [ ] **Step 3: Implement the comparison**

Append to `internal/admission/record.go`:

```go
// Differences is every way r, what the owner admitted, differs from site as
// the declaration has it: each is a root change, so each needs the owner to
// run host admit again. None when r covers the site.
func (r Record) Differences(site string, s config.Site) []string {
	var out []string
	if r.Site != site {
		out = append(out, fmt.Sprintf("the host was admitted as site %s, and this is %s", r.Site, site))
	}
	if r.User != s.SSHUser() {
		out = append(out, fmt.Sprintf("the host admitted user %s, and sites.%s.ssh.user is %s", r.User, site, s.SSHUser()))
	}
	if r.Ports.SSH != s.SSH.PortOrDefault() {
		out = append(out, fmt.Sprintf("the host opened ssh port %d, and the site's is %d", r.Ports.SSH, s.SSH.PortOrDefault()))
	}
	if r.Ports.WireGuard != s.ListenPort() {
		out = append(out, fmt.Sprintf("the host opened WireGuard port %d, and the site's is %d", r.Ports.WireGuard, s.ListenPort()))
	}
	if want := s.Endpoint != ""; r.Forwarding != want {
		out = append(out, fmt.Sprintf("forwarding is %s on the host, and the site %s an endpoint, which relays the mesh", onOff(r.Forwarding), map[bool]string{true: "has", false: "has no"}[want]))
	}
	for _, g := range Needs(s).Missing(r.Grants) {
		out = append(out, fmt.Sprintf("the site's roles need --%s, which the host's owner did not grant", g))
	}
	if s.WebServerManagedBy() == config.ManagedByExternal && r.WebServer == nil {
		out = append(out, "the site is behind the owner's web server (managed_by: external), and the host was admitted without it")
	}
	return out
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// SiteError is a host whose admission does not cover its site.
type SiteError struct {
	Site     string
	Problems []string
}

func (e *SiteError) Error() string {
	return e.Site + "'s admission does not cover it: " + strings.Join(e.Problems, "; ")
}

// ForSite reads, as the deployment's user, the admission record of the
// deployment deploymentID on the host t reaches, and judges it against site.
// An error wrapping ErrNotAdmitted is a host with no record; a *SiteError is
// one whose record does not cover the site.
func ForSite(t Reader, deploymentID, site string, s config.Site) (Record, error) {
	rec, err := Read(t, deployment.Deployment{ID: deploymentID}.Token())
	if err != nil {
		return Record{}, err
	}
	if p := rec.Differences(site, s); len(p) > 0 {
		return rec, &SiteError{Site: site, Problems: p}
	}
	return rec, nil
}
```

- [ ] **Step 4: The command's check**

`git mv cmd/paisans/claim.go cmd/paisans/admitted.go` and replace `claimHosts` and `claimSites` with:

```go
// admittedHosts is what every command that writes to a host does before
// anything else there: it reads each named site's admission record, as the
// deployment's user, and refuses a host that has not admitted the
// deployment, or whose admission does not cover the site, with nothing on
// any host changed. host admit claimed the host in its registry, which is
// root's; the record is how the deployment learns it was admitted, and as
// what. Sites are read in name order, each a step, so a spinner shows while
// ssh answers.
func admittedHosts(r ui.Reporter, cfg *config.Config, transports map[string]registry.Runner) (map[string]admission.Record, error) {
	sites := slices.Sorted(maps.Keys(transports))
	out := map[string]admission.Record{}
	for _, site := range sites {
		name := "site"
		if len(sites) > 1 {
			name = site
		}
		rec, err := ui.Get(r, "check "+name+"'s admission", func() (admission.Record, error) {
			rec, err := admission.ForSite(transports[site], cfg.ID, site, cfg.Sites[site])
			if err != nil {
				return rec, admissionProblem(site, err)
			}
			return rec, nil
		})
		if err != nil {
			return nil, err
		}
		out[site] = rec
	}
	return out, nil
}

// admittedSites is admittedHosts over the named sites, each reached through
// its own ssh section.
func admittedSites(r ui.Reporter, cfg *config.Config, sites ...string) (map[string]admission.Record, error) {
	transports := map[string]registry.Runner{}
	for _, name := range sites {
		transports[name] = registryHost(name, cfg.Sites[name], "")
	}
	return admittedHosts(r, cfg, transports)
}

// admissionProblem is a refusal in the operator's words: what the record
// lacks, and the two lines that fix it, the operator's and the owner's.
func admissionProblem(site string, err error) error {
	lines := fmt.Sprintf("Send the host's owner a new request:\n\n    paisans host request --site %[1]s > %[1]s.admit\n\nand ask them to run, on the host:\n\n    sudo paisans host admit --request %[1]s.admit", site)
	var se *admission.SiteError
	switch {
	case errors.Is(err, admission.ErrNotAdmitted):
		return &ui.Problem{Hint: site + " is not admitted", Explain: "Its host has no admission record for this deployment: its owner has not admitted it, or has evicted it. " + lines, Cause: err}
	case errors.As(err, &se):
		return &ui.Problem{Hint: site + "'s admission does not match " + config.FileName, Explain: "- " + strings.Join(se.Problems, "\n- ") + "\n" + lines, Cause: err}
	}
	return err
}
```

Keep `registryHost`, `union` and their comments as they are. Then `grep -rn 'registry\.\(Claim\|Check\)(' --include=*.go internal cmd`: delete `registry.Claim` and `registry.Check` with their tests if nothing calls them (phase 5's admit calls `ClaimCommand`, `Conflicts` and `Read`, which stay).

At every caller, `claimHosts(r, cfg, X, transports)` becomes `admittedHosts(r, cfg, transports)` and `claimSites(r, cfg, X, sites...)` becomes `admittedSites(r, cfg, sites...)`, discarding the records (`if _, err := ...; err != nil`) except where a later task uses them. Read each call's comment and say "check the admission" where it says "claim".

In `internal/preflight/preflight.go`, `registry` becomes:

```go
// admission checks the new site's host has admitted this deployment, as
// that site, with the grants its roles need. It reads as the deployment's
// user; host admit claimed the host in its registry.
func (r *runner) admission(t apply.Transport) {
	if _, err := admission.ForSite(t, r.cfg.ID, r.newSite, r.cfg.Sites[r.newSite]); err != nil {
		r.refuse(r.newSite, "admission", "%v. Send the host's owner `paisans host request --site %s > %s.admit`", err, r.newSite, r.newSite)
		return
	}
	r.pass(r.newSite, "admission", "the host admitted this deployment as %s", r.newSite)
}
```

and its caller calls `r.admission(t)`.

- [ ] **Step 5: Update the fakes**

Run: `grep -rln 'registry.Path\|registry.json\|ClaimCommand' --include=*_test.go cmd internal/preflight`
A fake that served an empty registry so a claim would pass serves `admittedRecord(t, cfg, site)` at `admittedPath(cfg)` instead (`cmd/paisans`), or the record its own package builds the same way (`internal/preflight`). A test that asserted a claim was written asserts the step `check <site>'s admission` instead. `hostdeployments_test.go` keeps its registry: `host deployments` is the owner's.

- [ ] **Step 6: Run, CHANGELOG, full suite, commit**

Run: `go test ./internal/admission/ ./internal/preflight/ ./cmd/paisans/`
Expected: PASS.

Under `### Changed`:

```markdown
- Every command that changes a host checks first that the host admitted the deployment as that site, with the grants its roles need, and names the `host request` and `host admit` lines when not.
```

Run: `go vet ./... && go test ./...`, then:

```bash
git add internal/admission internal/preflight cmd/paisans CHANGELOG.md
git commit -m "feat: check the admission record where commands claimed the host" -m "The registry is root's and host admit claims it. Every command that wrote
to a host claimed it first; it now reads the admission record as the
deployment's user instead, and refuses a host with none, or one whose
admission no longer matches the site, before changing anything, with the
operator's and the owner's lines that fix it.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: `host prepare` installs nothing

Rulings 7, 8, 9. Spec: "`host prepare` installs nothing and touches no root file. It checks the admission record against the declaration, writes the user's `authorized_keys`, the config store and its user units, and the daemon's `daemon.json`."

**Files:**
- Modify: `internal/hostprep/hostprep.go` (`Build`, `Option`, `Shared`)
- Create: `internal/hostprep/daemon.go`
- Modify: `internal/hostprep/watchdog.go`, `internal/hostprep/snippets/watchdog-probe.sh`
- Modify: `internal/hostadmit/daemon.go` (phase 5's `daemonJSON` calls `hostprep.DaemonJSON`)
- Modify: `cmd/paisans/main.go` (`hostPrepare`)
- Test: `internal/hostprep/hostprep_test.go`, `internal/hostprep/daemon_test.go` (create), `cmd/paisans/hostprepare_all_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: Task 9's `admittedHosts` (its record), `admission.Record`; Task 4's `Home`, `UserSystemctl`; Task 8's `planAuthorizedKeys(t, d, home, s)`; phase 5's `mesh.DefaultPools`, `mesh.ParsePrefix`, `mesh.Overlap`.
- Produces: `func hostprep.Build(site string, cfg *config.Config, t Transport, rec admission.Record) (*Plan, error)`; `func hostprep.DaemonJSON(meshSubnet, dataDir string) (string, error)`; `func checkWatchdog(t Transport, site config.Site) (Section, error)`. Removed from `Build`'s path: `Profile.Packages`, `Services`, `WatchdogModule`, `Firewall`, `Rules`, `ContainerRules`, the gateway Caddy probe, `Option` and `Shared` (each kept only where phase 5's admit calls it: run `grep -rn 'hostprep\.' internal/hostadmit` and delete what neither calls).

- [ ] **Step 1: Write the failing tests**

Create `internal/hostprep/daemon_test.go`:

```go
package hostprep_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

// The user's daemon.json: the userland proxy off, Docker's pools less the
// one overlapping the mesh, and the data root when admit set one.
func TestDaemonJSON(t *testing.T) {
	got, err := hostprep.DaemonJSON("172.18.0.0/24", "/srv/data/paisans-f2a9")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		UserlandProxy *bool `json:"userland-proxy"`
		Pools         []struct {
			Base string `json:"base"`
			Size int    `json:"size"`
		} `json:"default-address-pools"`
		DataRoot string `json:"data-root"`
	}
	if err := json.Unmarshal([]byte(got), &d); err != nil {
		t.Fatal(err)
	}
	if d.UserlandProxy == nil || *d.UserlandProxy || d.DataRoot != "/srv/data/paisans-f2a9" {
		t.Errorf("got %s", got)
	}
	for _, p := range d.Pools {
		if p.Base == "172.18.0.0/16" || p.Size != 24 {
			t.Errorf("pool %+v", p)
		}
	}
	if !strings.HasSuffix(got, "}\n") {
		t.Errorf("no final newline: %q", got)
	}
}
```

In `internal/hostprep/hostprep_test.go`, every `hostprep.Build(site, cfg, host)` call gains a fourth argument, the record admit writes for that site: add

```go
// admitted is the record host admit leaves for site with every grant its
// roles need: what host prepare is checked against.
func admitted(t *testing.T, cfg *config.Config, site string) admission.Record {
	t.Helper()
	req, err := admission.Build(cfg, site)
	if err != nil {
		t.Fatal(err)
	}
	rec := admission.NewRecord(req, req.Grants, "", time.Unix(0, 0))
	if cfg.Sites[site].WebServerManagedBy() == config.ManagedByExternal {
		rec.WebServer = &admission.WebServer{Server: "caddy"}
	}
	return rec
}
```

Delete every test of packages, services, the Docker drop-in, the firewall, ufw rules, the softdog module and unit, and the former gateway's allows: `TestAFreshDataSitePlansEveryStep`, `TestAnArm64HostGetsDockersArm64Repository`, `TestSSHIsAllowedBeforeTheFirewallIsEnabled`, `TestFirewallRulesFollowRoles`, `TestADerivedEndpointOpensItsPort`, `TestAPaisansMonitorOpensEightyAndFourFortyThree`, `TestAFreshGatewaySiteDiffersFromADataSite`, `TestOnlyMissingFirewallRulesArePlanned`, `TestASoftdogHostReRunsClean`, `TestDistroDockerIsRefusedNotRemoved`, `TestDistroDockerWithComposeIsLeftAlone`, `TestARemovedPackageIsNotInstalled`, `TestContainerRulesReachOnlyWhatAppsUse`, `TestAFreshHostAddsCommentedRules`, `TestAStaleOwnedRuleIsRemovedLast`, `TestAnotherDeploymentsRuleIsForeign`, `TestAForeignRuleIsNeverTouched`, `TestAForeignCommentedMatchIsLeftAlone`, `TestAForeignActionIsWarnedOrRefused`, `TestALegacyRuleIsAdoptedWithoutAGap`, `TestSSHIsNeverRemoved`, `TestTheSSHRuleFollowsTheDeclaredPort`, `TestMovingTheSSHPortKeepsTheOldAllow`, `TestAnOldSSHAllowWithoutOnePortIsNamedByItsRule`, `TestADenyOnTheDeclaredSSHPortIsRefused`, `TestContainerRulesLetTheMonitorReachLocalApps`, `TestContainerRulesLetTheAccountServiceReachPocketIDsAPI`, `TestDockerIsOrderedAfterTheMeshInterface`, `TestASharedHostNeverSetsTheFirewallsDefaults`, `TestSnapDockerIsRefusedNotRemoved`, `TestInstallPackagesIsTheKindOfEachPackage`, `TestAFormerGatewayKeepsItsWebAllowsUntilItsCaddyIsGone`, `TestAFailedCaddyProbeIsAnError`, unless phase 5 moved one to test admit's use of the same code (then leave it; it tests admit). `TestParseAddedRulesTellsOursFromTheirs` stays: site remove's force path and admit read the rules. Then add:

```go
// host prepare as the deployment's user: nothing it plans installs a
// package, writes outside the user's home or the deployment's root, or runs
// a command only root can.
func TestHostPrepareInstallsNothing(t *testing.T) {
	cfg := fixture(t)
	for _, site := range cfg.SiteNames() {
		host := freshHost()
		plan, err := hostprep.Build(site, cfg, host, admitted(t, cfg, site))
		if err != nil {
			t.Fatalf("%s: %v", site, err)
		}
		for _, s := range plan.Steps {
			if s.File != nil && !strings.HasPrefix(s.File.Path, "/home/ubuntu/") && !strings.HasPrefix(s.File.Path, "/srv/paisans/f2a9/") {
				t.Errorf("%s: %q writes %s", site, s.Title, s.File.Path)
			}
			for _, root := range []string{"apt-get", "modprobe", "ufw ", "/etc/", "sudo ", "systemctl daemon-reload &&", "systemctl enable docker"} {
				if strings.Contains(s.Command, root) {
					t.Errorf("%s: %q runs %q: %s", site, s.Title, root, s.Command)
				}
			}
		}
		for _, c := range host.ran {
			if strings.Contains(c, "ufw") || strings.Contains(c, "dpkg") {
				t.Errorf("%s: probed %q", site, c)
			}
		}
	}
}

// A record that does not match the declaration refuses host prepare, naming
// each difference and the line the owner runs.
func TestARecordThatDiffersIsRefused(t *testing.T) {
	cfg := fixture(t)
	rec := admitted(t, cfg, "home-a")
	rec.Grants.Watchdog = false
	_, err := hostprep.Build("home-a", cfg, freshHost(), rec)
	if err == nil || !strings.Contains(err.Error(), "--watchdog") || !strings.Contains(err.Error(), "sudo paisans host admit --request home-a.admit") {
		t.Errorf("got %v", err)
	}
}

// The watchdog is admit's: host prepare reads that /dev/watchdog is the
// user's, and refuses with the admit line when it is not.
func TestTheWatchdogIsOnlyRead(t *testing.T) {
	cfg := fixture(t)
	host := freshHost()
	host.responses[probeWatchdog] = "device present\nidentity watchdog0 Software Watchdog\nsystemd 0\ndaemon absent\nowner 0\nme 1000\n"
	if _, err := hostprep.Build("home-a", cfg, host, admitted(t, cfg, "home-a")); err == nil || !strings.Contains(err.Error(), "host admit") {
		t.Errorf("a root-owned /dev/watchdog: %v", err)
	}
	host.responses[probeWatchdog] = "device present\nidentity watchdog0 Software Watchdog\nsystemd 0\ndaemon absent\nowner 1000\nme 1000\n"
	plan, err := hostprep.Build("home-a", cfg, host, admitted(t, cfg, "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if slices.Contains(s.Kinds, hostprep.KindWatchdog) {
			t.Errorf("planned a watchdog step: %+v", s)
		}
	}
}

// daemon.json is written as the user when it differs, and the daemon is
// restarted only when it runs no container.
func TestDaemonJSONIsWrittenAndTheDaemonRestartedOnlyWhenIdle(t *testing.T) {
	cfg := fixture(t)
	host := freshHost()
	host.responses["docker ps -q"] = "3\n"
	plan, err := hostprep.Build("vm", cfg, host, admitted(t, cfg, "vm"))
	if err != nil {
		t.Fatal(err)
	}
	var write string
	for _, s := range plan.Steps {
		if s.Title == "write daemon.json" {
			write = s.Command
		}
		if strings.Contains(s.Command, "restart docker") {
			t.Errorf("restarted a daemon running containers: %s", s.Command)
		}
	}
	if !strings.Contains(write, "/home/ubuntu/.config/docker/daemon.json") {
		t.Errorf("write %q", write)
	}
	if len(plan.Warnings) == 0 || !strings.Contains(plan.Warnings[0].Hint, "next restart") {
		t.Errorf("warnings %+v", plan.Warnings)
	}
}
```

`freshHost` gains `"docker ps -q": "0\n"` in its responses and its watchdog answer gains `owner 1000\nme 1000\n`; `preparedHost` gains the daemon.json `hostprep.DaemonJSON(cfg.Mesh.Subnet, "")` at `/home/ubuntu/.config/docker/daemon.json`.

In `cmd/paisans/hostprepare_all_test.go`, the fake hosts serve the admission record (Task 9's `admittedRecord`) for each site.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/hostprep/`
Expected: FAIL: `Build` takes three arguments, `undefined: hostprep.DaemonJSON`.

- [ ] **Step 3: `daemon.json` in one place**

Create `internal/hostprep/daemon.go` from phase 5's `daemonConfig`, `addressPool` and `daemonJSON` (`internal/hostadmit/daemon.go`), exported:

```go
package hostprep

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/mesh"
)

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

// DaemonJSON is the deployment user's daemon.json, which host admit writes
// and host prepare keeps: the userland proxy off, so a published port
// carries the client's own address (spec, Observed R6); Docker's default
// pools less any overlapping the mesh, each cut in /24s, so no network of the
// user's takes the mesh's range; and the data root host admit's --data-dir
// gave, recorded in the admission record.
func DaemonJSON(meshSubnet, dataDir string) (string, error) {
	subnet, err := mesh.ParsePrefix(meshSubnet)
	if err != nil {
		return "", err
	}
	cfg := daemonConfig{UserlandProxy: false, DataRoot: dataDir}
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

// containersProbe counts the containers the user's daemon runs.
const containersProbe = "docker ps -q | wc -l"

// planDaemonJSON keeps the user's daemon.json as DaemonJSON has it, written
// as the user through a temporary file. A daemon running a container is
// never restarted under it: the change applies at its next restart, and the
// plan warns.
func planDaemonJSON(t Transport, home, meshSubnet string, rec admission.Record) (Section, error) {
	var out Section
	want, err := DaemonJSON(meshSubnet, rec.DataDir)
	if err != nil {
		return out, err
	}
	path := home + "/.config/docker/daemon.json"
	have, found, err := t.ReadFile(path)
	if err != nil {
		return out, err
	}
	if found && have == want {
		out.Present = append(out.Present, "docker: "+path+" as admit wrote it")
		return out, nil
	}
	running, err := t.Run(containersProbe)
	if err != nil {
		return out, fmt.Errorf("asking the deployment's Docker what it runs: %w", err)
	}
	dir, tmp := home+"/.config/docker", path+".paisans-tmp"
	command := fmt.Sprintf("set -e; umask 077; mkdir -p %s; printf %%s %s | base64 -d > %s; mv -f %s %s",
		shellQuote(dir), shellQuote(base64.StdEncoding.EncodeToString([]byte(want))), shellQuote(tmp), shellQuote(tmp), shellQuote(path))
	idle := strings.TrimSpace(running) == "0"
	if idle {
		command += "; " + UserSystemctl + " restart docker"
	}
	out.Steps = append(out.Steps, Step{Kinds: []string{KindDocker}, Title: "write daemon.json", Describe: "docker: write " + path + ": userland proxy off, address pools clear of the mesh" + map[bool]string{true: ", and restart the daemon, which runs no container"}[idle], Command: command})
	if !idle {
		out.Warnings = append(out.Warnings, Warning{Hint: "docker: the deployment's Docker reads its new daemon.json at its next restart", Detail: "It runs containers, and host prepare never restarts a daemon under them. Restart it when the deployment can stop for a moment: " + UserSystemctl + " restart docker."})
	}
	return out, nil
}
```

In `internal/hostadmit/daemon.go`, delete `daemonConfig`, `addressPool` and the body of `daemonJSON`, which becomes `return hostprep.DaemonJSON(p.in.Request.MeshSubnet, p.record.DataDir)`; phase 5's golden plans must not change (`go test ./internal/hostadmit/` passes unchanged).

- [ ] **Step 4: The watchdog, read only**

Append to `internal/hostprep/snippets/watchdog-probe.sh`:

```sh
printf 'owner %s\n' "$(stat -c %u /dev/watchdog 2>/dev/null)"
printf 'me %s\n' "$(id -u)"
```

`watchdogFacts` gains `Owner, Me string`, read from the two lines. Add to `internal/hostprep/watchdog.go`:

```go
// admitWatchdog is what fixes the watchdog when host prepare finds it wrong:
// it is a root change, the host owner's.
const admitWatchdog = "The host's owner runs sudo paisans host admit --request <site>.admit --watchdog again, from paisans host request --site <site> > <site>.admit."

// checkWatchdog reads what a data site's watchdog is, as the deployment's
// user: host admit loaded it and gave /dev/watchdog to the user. It plans
// nothing; anything wrong refuses, naming what the owner runs.
func checkWatchdog(t Transport, site config.Site) (Section, error) {
	var out Section
	if !site.Has(config.RoleData) {
		out.Present = append(out.Present, "watchdog: not needed, this site holds no data role")
		return out, nil
	}
	facts, err := probeWatchdog(t)
	if err != nil {
		return out, err
	}
	switch {
	case systemdHoldsWatchdog(facts.RuntimeWatchdog):
		return out, fmt.Errorf("watchdog: systemd's RuntimeWatchdogUSec is %s on %s, so PID 1 holds /dev/watchdog and Patroni cannot open it. The host's owner sets RuntimeWatchdogSec=0 in /etc/systemd/system.conf and runs systemctl daemon-reexec", facts.RuntimeWatchdog, t.Describe())
	case facts.Daemon:
		return out, fmt.Errorf("watchdog: a watchdog daemon is running on %s and holds /dev/watchdog. The host's owner stops and disables it (systemctl disable --now watchdog)", t.Describe())
	case !facts.DeviceNode || facts.Owner != facts.Me:
		return out, fmt.Errorf("watchdog: /dev/watchdog on %s is not the deployment's user's, so Patroni cannot open it. %s", t.Describe(), admitWatchdog)
	}
	hardware := facts.hardware()
	switch site.WatchdogMode() {
	case config.WatchdogRequired:
		if len(hardware) == 0 {
			return out, fmt.Errorf("watchdog: required, and %s has only softdog. required accepts a hardware or hypervisor watchdog and nothing else: the host's owner enables one in the firmware or the VM's settings, or declare watchdog: auto to accept softdog", t.Describe())
		}
	case config.WatchdogSoftdog:
		if len(hardware) > 0 {
			out.Warnings = append(out.Warnings, Warning{Hint: "watchdog: softdog declared, but a hardware watchdog exists", Detail: fmt.Sprintf("%s provides /dev/watchdog and Patroni opens that one, not softdog. Declare auto to say so.", hardware[0].Identity)})
		}
	default:
		if len(hardware) == 0 {
			out.Warnings = append(out.Warnings, Warning{Hint: "watchdog: softdog", Detail: softdogWarning})
		}
	}
	out.Present = append(out.Present, "watchdog: "+facts.describe()+", the deployment's user's")
	return out, nil
}
```

`planWatchdog` stays for phase 5's admit (its `PlanWatchdog` export) if admit calls it; otherwise delete it with `WatchdogModule`.

- [ ] **Step 5: `Build`**

Replace `Build` and its doc comment in `internal/hostprep/hostprep.go`:

```go
// Build checks the site's host as the deployment's user and returns only
// what is missing of the user's own: the admission record compared with the
// declaration first, refusing on any difference; then the watchdog, read
// only; daemon.json; the configuration store; the authorized keys, and last
// the keys no longer listed. Nothing here is installed or written as root:
// host admit did that, and a difference is the owner's to fix by admitting
// again.
func Build(site string, cfg *config.Config, t Transport, rec admission.Record) (*Plan, error) {
	declared, ok := cfg.Sites[site]
	if !ok {
		return nil, fmt.Errorf("host prepare: no site %q is declared", site)
	}
	if diff := rec.Differences(site, declared); len(diff) > 0 {
		return nil, fmt.Errorf("host prepare: %s's admission does not match %s:\n- %s\nSend the host's owner a new request, paisans host request --site %s > %s.admit, and ask them to run sudo paisans host admit --request %s.admit", site, config.FileName, strings.Join(diff, "\n- "), site, site, site)
	}
	content, found, err := t.ReadFile("/etc/os-release")
	if err != nil {
		return nil, fmt.Errorf("reading /etc/os-release on %s: %w", t.Describe(), err)
	}
	if !found {
		return nil, fmt.Errorf("%s has no /etc/os-release, so no host profile can be chosen for it", t.Describe())
	}
	host := ParseOSRelease(content)
	profile, err := Select(host)
	if err != nil {
		return nil, err
	}
	plan := &Plan{Site: site, Profile: profile.ID() + " " + profile.Version()}
	d := cfg.Deployment()

	watchdog, err := checkWatchdog(t, declared)
	if err != nil {
		return nil, err
	}
	plan.add(watchdog)

	home, err := Home(t)
	if err != nil {
		return nil, err
	}
	daemon, err := planDaemonJSON(t, home, cfg.Mesh.Subnet, rec)
	if err != nil {
		return nil, err
	}
	plan.add(daemon)

	store, err := profile.ConfigStore(t, d, declared, configPeers(cfg, site), home)
	if err != nil {
		return nil, fmt.Errorf("configuration store on %s: %w", t.Describe(), err)
	}
	plan.add(store)

	keys, keyRemovals, err := planAuthorizedKeys(t, d, home, declared.SSH)
	if err != nil {
		return nil, fmt.Errorf("authorized keys on %s: %w", t.Describe(), err)
	}
	plan.add(keys)
	// Last of all: a key is taken away only once everything else, the listed
	// keys included, is in place.
	plan.Steps = append(plan.Steps, keyRemovals...)
	return plan, nil
}
```

Rewrite the package comment: hostprep checks an admitted host as the deployment's user and keeps the user's own files; the profile's packages, firewall and watchdog module are what `host admit` runs, as root. Delete `Option`, `options` and `Shared` if nothing else uses them. Run `grep -rn 'Profile)\|profile\.\(Packages\|Services\|Firewall\|WatchdogModule\)' internal` and remove from `Profile` each method only `Build` called; keep each one phase 5's admit calls.

- [ ] **Step 6: The command**

In `cmd/paisans/main.go`, `hostPrepare` takes the record from the admission check, which moves before the plan, and builds with it:

```go
	recs, err := admittedHosts(r, cfg, map[string]registry.Runner{o.site: transport})
	if err != nil {
		return err
	}
	plan, err := ui.Get(r, "read the host", func() (*hostprep.Plan, error) {
		return hostprep.Build(o.site, cfg, transport, recs[o.site])
	})
```

and the `options`/`host.Shared()` lines go (`host` stays for the gate's other uses; if it becomes unused, call `hostGate` for its error alone). The result line for an executed plan stays `Prepared %s.`.

- [ ] **Step 7: Run, CHANGELOG, full suite, commit**

Run: `go test ./internal/hostprep/ ./internal/hostadmit/ ./cmd/paisans/`
Expected: PASS, with `internal/hostadmit`'s golden plans unchanged.

Under `### Changed`:

```markdown
- `host prepare` installs nothing: it checks the host's admission record against the configuration, then keeps the deployment user's keys, configuration store, units and `daemon.json`.
```

Run: `go vet ./... && go test ./...`, then:

```bash
git add internal/hostprep internal/hostadmit cmd/paisans CHANGELOG.md
git commit -m "feat: make host prepare check the admission and install nothing" -m "Packages, the firewall, the watchdog module and the Docker drop-in are
root changes, which host admit makes for the owner. host prepare runs as
the deployment's user: it refuses a host whose admission no longer matches
the site, reads the watchdog admit gave it, and keeps what is the user's
own. daemon.json comes from the one function admit uses, so the two never
disagree.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: Registry reads leave init, recover and rekey

Rulings 16, 17, 18, 34.

**Files:**
- Modify: `internal/apply/transport.go` (`ErrLoginRefused`, `NoLogin`), `internal/apply/transport_test.go`
- Modify: `cmd/paisans/mesh.go` (`chooseMesh`, `holder`), `cmd/paisans/config.go` (`recoverID`), `internal/meshkey/meshkey.go` (`Joining`)
- Test: `cmd/paisans/init_test.go` (or wherever `chooseMesh` is tested: `grep -ln chooseMesh cmd/paisans/*_test.go`), `cmd/paisans/config_test.go`, `internal/meshkey/*_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: Task 1's `admission.Mine`; `admission.Read`, `ErrNotAdmitted`.
- Produces: `var apply.ErrLoginRefused`; `func apply.NoLogin(err error) bool` (`ErrLoginRefused` or `ErrUnreachable`); `meshkey.Joining` unchanged in signature.

- [ ] **Step 1: Write the failing tests**

Add to `internal/apply/transport_test.go`, beside the existing tests of the "not admitted" refusal (read them: they build a transport whose ssh exits 255 with `Permission denied (publickey)`):

```go
// A refused login is told apart from a failed command, whichever words ssh
// used, and so is a host that does not answer.
func TestNoLogin(t *testing.T) {
	refused := runRefused(t) // the existing helper that makes ssh refuse the key; name it as the file does
	if !errors.Is(refused, apply.ErrLoginRefused) || !apply.NoLogin(refused) {
		t.Errorf("a refused login: %v", refused)
	}
	if !apply.NoLogin(fmt.Errorf("x: %w", apply.ErrUnreachable)) || apply.NoLogin(errors.New("exit status 1")) {
		t.Error("NoLogin misreads an unreachable host or a failed command")
	}
}
```

For `chooseMesh`, add beside its tests:

```go
// A host that has not admitted the deployment is not read: admit checks the
// subnet against it as root. One that answers is, and a manifest on any of
// them means the subnet is deployed.
func TestInitSkipsAHostNotYetAdmitted(t *testing.T) {
	cfg := fixtureConfig(t)
	hosts := map[string]registry.Runner{}
	for _, name := range cfg.SiteNames() {
		hosts[name] = meshHost(t) // the fake chooseMesh's tests use for a clean host; name it as they do
	}
	hosts["home-b"] = refusingHost{err: fmt.Errorf("ssh: %w", apply.ErrLoginRefused)}
	r := &ui.Recorder{Verbose_: true}
	wrote, err := settleMesh(r, cfg, "unused.yaml", hosts, rand.New(rand.NewSource(1)))
	if err != nil || wrote {
		t.Fatalf("wrote %v, %v", wrote, err)
	}
	if !strings.Contains(r.Lines(), "home-b has not admitted this deployment yet; host admit checks the subnet against it") {
		t.Errorf("no line for home-b:\n%s", r.Lines())
	}
}

type refusingHost struct{ err error }

func (h refusingHost) Run(string) (string, error)              { return "", h.err }
func (h refusingHost) ReadFile(string) (string, bool, error)   { return "", false, h.err }
func (h refusingHost) Describe() string                        { return "refusing" }
```

In `cmd/paisans/config_test.go`, the `recoverID` tests serve admission records instead of a registry: a host whose login is `paisans-f2a9` and holds `/etc/paisans/admitted.f2a9.json` for the fixture's id is recovered as that id, and a host holding no record of the login's is refused with "no admission record names this login".

In `internal/meshkey`, the test of `Joining` that put a deployment entry in a registry puts an admission record at `/etc/paisans/admitted.f2a9.json` instead, and a new row: a site whose transport fails with `apply.ErrLoginRefused` and that no peer or record lists is joining.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/apply/ -run TestNoLogin; go test ./cmd/paisans/ -run 'TestInitSkips|Recover'; go test ./internal/meshkey/`
Expected: FAIL.

- [ ] **Step 3: Implement**

In `internal/apply/transport.go`, beside `ErrUnreachable`:

```go
// ErrLoginRefused is a host that answered and would not let the login in:
// no key it accepts, or no such user, which for the deployment's own user is
// a host that has not admitted it.
var ErrLoginRefused = errors.New("the host refused the login")

// NoLogin reports whether err is a host that could not be logged into at
// all, refused or not answering, rather than a command that failed there.
func NoLogin(err error) bool {
	return errors.Is(err, ErrLoginRefused) || errors.Is(err, ErrUnreachable)
}
```

In `authProblem`, both `Permission denied` cases set `Cause: fmt.Errorf("%w: %w", ErrLoginRefused, err)`.

In `cmd/paisans/mesh.go`, `chooseMesh` reads no registry:

- the first loop asks each site `s.Progress("read " + name)` whether it holds the deployment's manifest, `hosts[name].ReadFile(cfg.Deployment().Manifest())`; a site whose error is `apply.NoLogin` and wraps `apply.ErrLoginRefused` is recorded as not admitted (`s.Detail("%s has not admitted this deployment yet; host admit checks the subnet against it as root, and refuses one that overlaps anything there", name)`) and skipped from then on; any other error is a site that did not answer, which stops init as today;
- "deployed" is a site whose manifest was found (`claimedOn`), and the detail says "%s holds this deployment's manifest";
- the second loop probes only the admitted sites, with Task 7's `mesh.ProbeUser`;
- `registry.Meshes`, `registry.For` and `registry.Conflicts` go, with `registries` and the claim decision after `mesh.Choose`; the subnet check is admit's.

`holder` drops its registry lookup: it returns `" by paisans deployment " + token`.

In `cmd/paisans/config.go`, `recoverID` without `--id` reads each host's own record:

```go
	for _, name := range slices.Sorted(maps.Keys(hosts)) {
		rec, err := admission.Mine(hosts[name])
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		ids[rec.Deployment] = true
	}
```

and its comment says the deployment is the one each login's admission record names; the "not a deployment id" refusal goes, since `ParseRecord` has already refused such a record.

In `internal/meshkey/meshkey.go`, `Joining` reads each site's admission record instead of its registry:

```go
		_, err := admission.Read(t, cfg.Deployment().Token())
		switch {
		case err == nil:
			claimed = append(claimed, name)
		case errors.Is(err, admission.ErrNotAdmitted), errors.Is(err, apply.ErrLoginRefused):
			unclaimed = append(unclaimed, name)
		default:
			return nil, &NoAnswer{Site: name, Err: err}
		}
```

and its comment and its refusal say "has not admitted this deployment" where they say "holds no claim of this deployment in its registry", with the advice "Check that its ssh section reaches the host it runs on, and that the host's owner has not evicted this deployment."

- [ ] **Step 4: Run, CHANGELOG, full suite, commit**

Run: `go test ./internal/apply/ ./internal/meshkey/ ./internal/siteremove/ ./cmd/paisans/`
Expected: PASS.

Under `### Changed`:

```markdown
- `paisans init` settles the mesh subnet on the hosts that have admitted the deployment and leaves the rest to `host admit`'s own check; `config recover` finds the deployment from the login's admission record.
```

Run: `go vet ./... && go test ./...`, then:

```bash
git add internal/apply internal/meshkey internal/siteremove cmd/paisans CHANGELOG.md
git commit -m "feat: read admission records where commands read the registry" -m "The registry is root's, and the deployment's user cannot read it. init
settles the subnet on the hosts it can log into and leaves a host not yet
admitted to admit's check as root; config recover and mesh rekey learn the
deployment and its joined sites from admission records. A refused login is
now an error of its own, so each can tell it from a command that failed.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 12: Site remove cleans as the user and hands over to evict

Rulings 19, 20. Spec: "`site remove` does what the deployment's user can, then ends by printing the `host evict` line for the owner."

**Files:**
- Modify: `internal/siteremove/host.go`, `internal/siteremove/caddy.go`, `internal/siteremove/siteremove.go` (the plan's `Evict` line)
- Modify: `cmd/paisans/siteremove.go` (the result)
- Test: `internal/siteremove/host_test.go`, `internal/siteremove/world_test.go` (the fake's answers), the tests that assert registry marks, units under `/etc/systemd/system`, ufw deletions or a WireGuard stop (Step 5)
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: Task 4's `hostprep.Home`, `UserUnitDir`, `UserSystemctl`; Task 8's `OwnedKeysPath`, `Locked`; Task 3's `render.HostSitesDir`.
- Produces: `func siteremove.EvictLine(d deployment.Deployment, user string) string` = `sudo paisans host evict --deployment <id> --user <user> --execute`; `Plan.Evict string`, set whenever the plan reaches or skips a host; `func emptyRootCommand(d deployment.Deployment, deleteData bool) string`. Removed from the host stage: the WireGuard stop, ufw rule deletions, the registry entry's removal and both registry marks, system units and drop-ins.

- [ ] **Step 1: Write the failing tests**

Add to `internal/siteremove/host_test.go`:

```go
// The host stage reaches only what the deployment's user owns: no ufw, no
// registry, no system unit, no WireGuard unit, and no path outside its root
// or its user's home.
func TestTheHostStageIsTheUsersOwn(t *testing.T) {
	w := setup(t)
	p := w.mustBuild("vm", siteremove.Options{DeleteData: true})
	if err := siteremove.Execute(p); err != nil {
		t.Fatal(err)
	}
	for _, c := range w.hosts["vm"].commands {
		for _, root := range []string{"ufw ", "registry.json", "/etc/systemd/system", "wg-quick@", "sudo "} {
			if strings.Contains(c, root) {
				t.Errorf("site remove ran %q: %s", root, c)
			}
		}
	}
	if !strings.Contains(p.Evict, "sudo paisans host evict --deployment "+dep.ID+" --user ") {
		t.Errorf("evict line %q", p.Evict)
	}
}

// A manifest written outside the deployment's root is never deleted: the
// user cannot, and it is not provably this deployment's to the owner.
func TestSiteRemoveNeverReachesOutsideTheRoot(t *testing.T) {
	w := setup(t)
	vm := w.hosts["vm"]
	vm.addToManifest(t, "etc/hosts", "127.0.0.1 localhost\n") // see Step 5 for the helper
	p := w.mustBuild("vm", siteremove.Options{})
	if hasStepIn(stageNamed(p, "clean the host"), "vm", "delete", "/etc/hosts") {
		t.Errorf("planned deleting /etc/hosts:\n%s", printed(p))
	}
	if !strings.Contains(strings.Join(p.Kept, "\n"), "/etc/hosts: outside "+dep.Root()) {
		t.Errorf("kept %q", p.Kept)
	}
}

// --delete-data empties the root through rootlesskit, as root of the
// user's namespace, so files a container's user owns go too, and never
// removes the root itself, which is root's to remove.
func TestDeleteDataEmptiesTheRootInTheUsersNamespace(t *testing.T) {
	w := setup(t)
	p := w.mustBuild("vm", siteremove.Options{DeleteData: true})
	if err := siteremove.Execute(p); err != nil {
		t.Fatal(err)
	}
	vm := w.hosts["vm"]
	if vm.ran("rootlesskit find '"+dep.Root()+"' -mindepth 1 -delete") != 1 {
		t.Errorf("the root was not emptied through rootlesskit:\n%s", strings.Join(vm.commands, "\n"))
	}
	if vm.ran("rm -rf -- '"+dep.Root()+"'") != 0 {
		t.Error("tried to remove the root directory itself")
	}
}
```

Rewrite `TestCleaningAHostDeletesItsConfigurationStore` for the user's units: the unit files are `/home/ubuntu/.config/systemd/user/paisans-f2a9-config-daemon.service` and `.timer`, and the check after `Execute` looks for them there (the world's login is `ubuntu`; use the home the world answers for `getent passwd`).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/siteremove/ -run 'TheHostStage|NeverReaches|DeleteDataEmpties|ConfigurationStore'`
Expected: FAIL: `p.Evict` undefined; the stage runs `ufw delete` and the registry's awk.

- [ ] **Step 3: The host plan**

In `internal/siteremove/host.go`:

- `hostPlan` loses `wireguard`, `units`, `dropins`, `rules`, `registry`, `markData`, `dataEntry`; gains `userUnits []string` and `home string`. `caddyPlan` loses `entry` and `mark` (in `caddy.go`).
- `buildHost`, after `inspect`: `hp.home, err = hostprep.Home(t)`; the files proven by the manifest are split, keeping any whose path does not start with `d.Rel() + "/"`:

```go
		if !strings.HasPrefix(f.Entry.Path, d.Rel()+"/") {
			p.Kept = append(p.Kept, fmt.Sprintf("%s: /%s: outside %s, so site remove leaves it; the host's owner removes it if it is this deployment's", p.Site, f.Entry.Path, d.Root()))
			continue
		}
```

  (place it where `hp.files` is filled; read the loop and add it at its top). The units are the user's:

```go
// userUnitsCommand lists the deployment's units in its user's own systemd
// directory: the configuration store's, which host prepare writes.
func (p *Plan) userUnitsCommand(home string) string {
	return fmt.Sprintf(`for f in %s/%s-*; do [ -e "$f" ] && echo "unit $f"; done; true`, hostprep.UserUnitDir(home), p.dep().Prefix())
}

// removeUserUnitsCommand disables, stops and deletes each, then reloads the
// user's manager.
func (p *Plan) removeUserUnitsCommand(home string) string {
	return fmt.Sprintf(`for f in %[1]s/%[2]s-*; do [ -e "$f" ] || continue; %[3]s disable --now "$(basename "$f")" >/dev/null 2>&1 || true; rm -f -- "$f"; done; %[3]s daemon-reload`, hostprep.UserUnitDir(home), p.dep().Prefix(), hostprep.UserSystemctl)
}
```

  replacing `unitsCommand` and `removeUnitsCommand`; the ufw read, the registry read and every `registry.Keep`/`registry.For` decision go. `hp.keepData` stays (`p.DataKept` with it), set when data is left without `--delete-data`, with no registry entry. Delete `wireguardCommand`.
- `planRoot` and `planKeptRoot` plan emptying instead of removing the root:

```go
// emptyRootCommand empties the deployment's root, as root of a new user
// namespace that maps the user's subordinate range, which is what rootlesskit
// runs a command as (Docker's rootless documentation, Uninstall: rootlesskit
// rm -rf ~/.local/share/docker): a file a container's user owns is the
// user's to remove there. With deleteData everything goes; without, only
// the empty directories. The root itself stays: /srv/paisans is root's, and
// host evict removes it.
func emptyRootCommand(d deployment.Deployment, deleteData bool) string {
	root := quote(d.Root())
	if deleteData {
		return fmt.Sprintf("if [ -d %[1]s ]; then rootlesskit find %[1]s -mindepth 1 -delete; fi", root)
	}
	return fmt.Sprintf("if [ -d %[1]s ]; then rootlesskit find %[1]s -mindepth 1 -depth -type d -empty -delete; fi", root)
}
```

  `runHost`'s root case runs `emptyRootCommand(d, hp.deleteRoot)` in place of `appremove.DirCommandFor(d.Root(), hp.deleteRoot)`; the kept Caddy's two commands (`deleteBesideKept`, `emptyBesideKept` in `caddy.go`) run their `find` through `rootlesskit` the same way. The step texts say "empty <root>" for "delete <root>", and "the empty directories under <root>, which itself stays for host evict".
- `hostSteps` loses the steps "stop wg-quick@...", "remove units" for drop-ins, "delete ufw rules", "remove registry entry", both "mark the registry entry kept" steps; "remove units" lists `hp.userUnits`; and `runHost` loses the same blocks, with the units removed by `removeUserUnitsCommand(hp.home)`, and the key record removed by `hostprep.Locked(d, "rm -f -- "+quote(hostprep.OwnedKeysPath(d)))` when Task 8 found it.
- `verifyHost` drops the ufw and registry checks and lists `userUnitsCommand(p.host.home)`'s output in place of the system units'.
- The stage's gate text: "no container, network, unit or file of this deployment's left that its user can remove, and the root empty but for what is kept".

- [ ] **Step 4: The evict line**

In `internal/siteremove/siteremove.go`, `Plan` gains:

```go
	// Evict is the line the host's owner runs to finish: what is root's
	// (the user, its home, the root directory, WireGuard, the firewall, the
	// registry entry and the admission record) only host evict removes.
	// Empty when the plan reaches no host.
	Evict string
```

and:

```go
// EvictLine is how the host's owner removes what a site remove left for
// them, without the request file.
func EvictLine(d deployment.Deployment, user string) string {
	return fmt.Sprintf("sudo paisans host evict --deployment %s --user %s --execute", d.ID, user)
}
```

`buildHost` sets `p.Evict = EvictLine(d, <the site's login user>)` (the declared site's `SSHUser()`, or the user of the `--force` destination) on both its paths, `--host-gone` included. When the plan keeps a Caddy, it appends to `p.Kept`: `"<site>: its Caddy keeps serving the host owner's sites in <HostSitesDir>; host evict stops it and deletes that directory, so the owner moves those sites first"`; when data is kept: `"<site>: the data in <root> and the deployment's volumes stay; host evict deletes them"`.

`hostGoneLeft` becomes:

```go
func (p *Plan) hostGoneLeft() string {
	d := p.dep()
	return fmt.Sprintf("%s: host not reached (--host-gone). If it ever comes back it still holds the deployment's user %s with its containers, volumes and units, %s, and its admission record; its owner removes all of it with %s. Nothing it runs can reach the cluster: no remaining site has it as a WireGuard peer, and its etcd member is gone",
		p.Site, p.cfg.Sites[p.Site].SSHUser(), d.Root(), EvictLine(d, p.cfg.Sites[p.Site].SSHUser()))
}
```

In `cmd/paisans/siteremove.go`, after the result line, when `plan.Evict != ""`, print `r.Note("the host's owner finishes on "+site, "Send them this line, which removes the deployment's user and everything root's that admit made for it:\n\n    "+plan.Evict)`.

- [ ] **Step 5: Update the world and the tests**

In `internal/siteremove/world_test.go`, the fake's `Run` answers `getent passwd "$(id -un)"` with `ubuntu:x:1000:1000::/home/ubuntu:/bin/bash`, the user unit listing by globbing its files under `/home/ubuntu/.config/systemd/user/paisans-f2a9-`, the unit removal by deleting them, and `rootlesskit find '<dir>' -mindepth 1 -delete` by deleting every file under `<dir>` (and the `-type d -empty` form by doing nothing, as files hold the directories); add `addToManifest(t, path, content)`, which writes the file and adds its entry with its checksum to the host's manifest. Every test that set up `/etc/systemd/system/paisans-f2a9-*`, `ufw show added` rules or registry entries for the host stage, or asserted their removal, a mark, or `stop wg-quick@`, is changed to the user's units or deleted (`grep -ln 'registry\|ufw\|/etc/systemd/system\|wg-quick' internal/siteremove/*_test.go`). Tests of `site remove --force` by id keep their registry until Task 13.

- [ ] **Step 6: Run, CHANGELOG, full suite, commit**

Run: `go test ./internal/siteremove/ ./cmd/paisans/`
Expected: PASS.

Under `### Changed`:

```markdown
- `site remove` cleans a host as the deployment's user, emptying its root through `rootlesskit`, and ends with the `host evict` line the host's owner runs to remove the rest.
```

Run: `go vet ./... && go test ./...`, then:

```bash
git add internal/siteremove cmd/paisans CHANGELOG.md
git commit -m "feat: clean a removed site's host as the user and hand over to evict" -m "The deployment's user can remove its containers, files, units and records,
and nothing root made for it: the user itself, the root directory,
WireGuard, the firewall and the registry entry are host evict's. site
remove does its part, empties the root through rootlesskit so files a
container's user owns go too, never touches a path outside the root, and
prints the evict line for the owner.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 13: `site remove --force` and `host deployments` read admission records

Ruling 14. `site remove --force --ssh ... --id` reaches a host as whatever login `--ssh` names, which is the deployment's user, and read the registry to find the deployment; `host deployments` is the owner's, through sudo.

**Files:**
- Modify: `internal/siteremove/force.go`, `internal/siteremove/byid_test.go`, `internal/siteremove/force_test.go`
- Modify: `cmd/paisans/hostdeployments.go`, `cmd/paisans/hostdeployments_test.go`, `cmd/paisans/help.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: Task 1's `admission.Mine`, `Record.Site`, `Record.Roles`; `admission.Read`.
- Produces: `func siteremove.BuildForcedByID(dest config.Destination, t apply.Transport, ref string, o Options) (*Plan, error)` (same signature) reading `admission.Mine`; `const hostDeploymentsAdmitted`, the listing of records `host deployments` runs.

- [ ] **Step 1: Write the failing tests**

In `internal/siteremove/byid_test.go`, the host serves an admission record (built as Task 9's helper builds one, for the fixture's `vm`) instead of a registry entry, and the tests expect: `--id f2a9` finds it; `--id 0c1d` (another token) is refused with "this login's admission record is for deployment f2a9..."; a host whose login has no record is refused naming `admission.ErrNotMine`'s text. In `force_test.go`, the check that the entry names another site live in the configuration reads the record's `site`.

In `cmd/paisans/hostdeployments_test.go`, `listedHost` answers the records listing, and:

```go
// The owner's listing names every admission record beside the registry:
// the user, the site, the grants and when it was admitted.
func TestHostDeploymentsListsAdmissionRecords(t *testing.T) {
	out := runDeployments(t, listedHost(t, twoDeployments(), admittedRecords(t)), "--ssh", "owner@vm.example.org")
	for _, want := range []string{"admitted: paisans-f2a9 as vm (gateway), watchdog no, public ports yes, at 2026-10-15T00:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
}

// Without sudo, only the records are read: they are 644 in a 755 directory.
func TestHostDeploymentsWithoutSudoListsOnlyRecords(t *testing.T) {
	h := listedHost(t, twoDeployments(), admittedRecords(t))
	out := runDeployments(t, h, "--ssh", "owner@vm.example.org", "--sudo=false")
	if !strings.Contains(out, "admitted: paisans-f2a9") {
		t.Errorf("no record listed:\n%s", out)
	}
	for _, c := range h.commands {
		if strings.Contains(c, "registry") || strings.Contains(c, deployment.Base) {
			t.Errorf("read %q without sudo", c)
		}
	}
}
```

(`runDeployments`, `listedHost` and `admittedRecords` are the file's helpers, extended: read the file, which today calls `runHostDeployments` with `withHost`, and keep its pattern.)

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/siteremove/ -run 'ByID|Forced'; go test ./cmd/paisans/ -run HostDeployments`
Expected: FAIL.

- [ ] **Step 3: Implement**

In `internal/siteremove/force.go`:

- `BuildForced` reads `admission.Read(t, cfg.Deployment().Token())` in place of `registry.Read`; a record found names the site the host was admitted as, `rec.Site`, and its roles, `rec.Roles`, where the code used `e.Site` and `e.Roles`; `ErrNotAdmitted` is a host with nothing of this deployment's admitted, which proceeds as a missing entry did.
- `BuildForcedByID` reads `admission.Mine(t)`: the deployment is `rec.Deployment`, its token must match `ref` (a token or the full id), the site and roles are the record's. The checks of a registry entry's shape (`wrong`, `sharing`) go: `ParseRecord` has refused any record that names another token. The refusal when `ref` names another deployment: `"site remove --id %s: this login's admission record on %s is for deployment %s (%s). Log in as that deployment's user to clean it. Nothing was changed"`.

In `cmd/paisans/hostdeployments.go`:

```go
// hostDeploymentsAdmitted lists every admission record with its content,
// each preceded by its path, so one read lists them all. Records are 644 in a
// 755 directory: no sudo needed.
var hostDeploymentsAdmitted = `for f in ` + admission.Dir + `/admitted.*.json; do [ -f "$f" ] || continue; echo "record $f"; cat -- "$f"; echo; done; echo end`
```

`runHostDeployments` reads it first, parses each record with `admission.ParseRecord` (by the token in its name), and prints one line per record, `admitted: <user> as <site> (<roles>), watchdog <yes|no>, public ports <yes|no>, at <admitted_at>`, under the deployment's id, then, with `--sudo` (the default), the registry and roots as today. A record whose parse fails is listed as a warning naming the file and the problem. With `--sudo=false` the command runs nothing else. The help line and `--sudo`'s flag text say: "--sudo=false lists admission records only, which are readable without sudo". The system Docker container listing in `deploymentsProbe` goes: a deployment's containers are in its user's own daemon, which root's `docker` does not reach. No command writes a registry entry's `kept` any more (Task 12), so the listing's kept notes go, and `registry.Keep`, `KeepCaddy`, `KeptCaddy` and `KeptData` with them when nothing else calls them (`grep -rn 'Kept\|Keep(' --include=*.go internal cmd`), and the field `Entry.Kept` too when nothing reads it.

- [ ] **Step 4: Run, CHANGELOG, full suite, commit**

Run: `go test ./internal/siteremove/ ./cmd/paisans/`
Expected: PASS.

Under `### Changed`:

```markdown
- `host deployments` lists every admission record on the host, with the user, site, grants and time, and lists only those with `--sudo=false`.
```

Run: `go vet ./... && go test ./...`, then:

```bash
git add internal/siteremove cmd/paisans CHANGELOG.md
git commit -m "feat: read admission records in site remove by id and host deployments" -m "site remove --force --id logs in as the deployment's user, which cannot
read the registry; it finds the deployment, its site and its roles in the
login's own admission record. host deployments lists every record beside
the registry, and with --sudo=false lists the records alone, which any
user can read.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 14: The gateway answers as itself

Rulings 21, 22. Spec: "The deployment's user cannot read the host's ruleset or another user's sockets, so a port taken after admission, and above all one taken by a DNAT rule with no listener, is invisible from the host." The answer from outside is what decides.

**Files:**
- Create: `internal/selfcheck/selfcheck.go`, `internal/selfcheck/selfcheck_test.go`
- Modify: `internal/render/templates/Caddyfile.tmpl`, `internal/render/site.go` (the Caddyfile's data)
- Create: `internal/render/selfcheck_test.go`
- Modify: the golden Caddyfiles under `internal/render/testdata/golden*/` (regenerated)
- Create: `cmd/paisans/gateway_selfcheck.go`, `cmd/paisans/gateway_selfcheck_test.go`
- Modify: `cmd/paisans/main.go` (the call after `recordApplied`), the `cmd/paisans` test setup that stubs network probes (`grep -n 'webServerProbes =' cmd/paisans/*_test.go` and stub `gatewayProbe` the same way)
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: `dns.GatewayHostnames(cfg)`; `config.Site.PublicAddress`.
- Produces: `func selfcheck.Path(d deployment.Deployment) string` (`/.well-known/paisans/<token>`); `func selfcheck.Body(d) string` (the token); `type selfcheck.Answer struct { Status int; Body, Location string }`; `type selfcheck.Fetch func(ctx context.Context, scheme, address, host, path string) (Answer, error)`; `type selfcheck.Probe struct { Fetch Fetch; Now func() time.Time; Sleep func(time.Duration); Wait time.Duration }`; `func selfcheck.DefaultProbe() Probe`; `type selfcheck.Target struct { Deployment deployment.Deployment; Site, Address, Host string }`; `func selfcheck.TargetFor(cfg *config.Config, site string) (Target, bool)`; `func selfcheck.Check(ctx context.Context, p Probe, t Target) error`; `type selfcheck.Conflict struct { Port int; Site, Detail string }`; `var gatewayProbe = selfcheck.DefaultProbe` and `func checkGatewayAnswers(r ui.Reporter, cfg *config.Config, site string) error` in `cmd/paisans`. Phase 7's doctor calls `selfcheck.Check`.

- [ ] **Step 1: Write the failing tests**

Create `internal/selfcheck/selfcheck_test.go`:

```go
package selfcheck_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/selfcheck"
)

var d = deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}

var target = selfcheck.Target{Deployment: d, Site: "vm", Address: "203.0.113.20", Host: "talk.example.org"}

// answers is a fake world: what https and http each answer, in turn.
type answers struct {
	https, http []result
	asked       []string
}

type result struct {
	a   selfcheck.Answer
	err error
}

func (w *answers) fetch(_ context.Context, scheme, address, host, path string) (selfcheck.Answer, error) {
	w.asked = append(w.asked, scheme+"://"+address+"/"+host+path)
	list := &w.https
	if scheme == "http" {
		list = &w.http
	}
	r := (*list)[0]
	if len(*list) > 1 {
		*list = (*list)[1:]
	}
	return r.a, r.err
}

func probe(w *answers) selfcheck.Probe {
	now := time.Unix(0, 0)
	return selfcheck.Probe{Fetch: w.fetch, Now: func() time.Time { return now }, Sleep: func(d time.Duration) { now = now.Add(d) }, Wait: 2 * time.Minute}
}

var (
	ours     = result{a: selfcheck.Answer{Status: 200, Body: "f2a9"}}
	redirect = result{a: selfcheck.Answer{Status: 308, Location: "https://talk.example.org/.well-known/paisans/f2a9"}}
	refused  = result{err: fmt.Errorf("dial: %w", syscall.ECONNREFUSED)}
)

func TestTheDeploymentsOwnGatewayPasses(t *testing.T) {
	w := &answers{https: []result{ours}, http: []result{redirect}}
	if err := selfcheck.Check(context.Background(), probe(w), target); err != nil {
		t.Fatal(err)
	}
	if strings.Join(w.asked, "\n") != "https://203.0.113.20/talk.example.org/.well-known/paisans/f2a9\nhttp://203.0.113.20/talk.example.org/.well-known/paisans/f2a9" {
		t.Errorf("asked %q", w.asked)
	}
}

// Anything else that answers is not this deployment, and says so in the
// spec's words.
func TestSelfCheckRefusesAnotherServer(t *testing.T) {
	for name, w := range map[string]*answers{
		"another body":       {https: []result{{a: selfcheck.Answer{Status: 200, Body: "<html>welcome to nginx</html>"}}}, http: []result{redirect}},
		"a 404":              {https: []result{{a: selfcheck.Answer{Status: 404}}}, http: []result{redirect}},
		"a redirect on 443":  {https: []result{{a: selfcheck.Answer{Status: 302, Location: "https://elsewhere.example/f2a9"}}}, http: []result{redirect}},
		"80 elsewhere":       {https: []result{ours}, http: []result{{a: selfcheck.Answer{Status: 301, Location: "https://other.example.org/"}}}},
		"80 serves no redirect": {https: []result{ours}, http: []result{ours}},
	} {
		err := selfcheck.Check(context.Background(), probe(w), target)
		var c *selfcheck.Conflict
		if !errors.As(err, &c) || !strings.Contains(err.Error(), "on vm answers, but not as this deployment") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// While Caddy obtains its certificate nothing answers, or the handshake
// fails; that is waited for, up to Wait, and then named.
func TestSelfCheckWaitsForTheCertificateThenGivesUp(t *testing.T) {
	w := &answers{https: []result{refused, refused, ours}, http: []result{redirect}}
	if err := selfcheck.Check(context.Background(), probe(w), target); err != nil {
		t.Errorf("a gateway that answers after two tries: %v", err)
	}
	w = &answers{https: []result{refused}, http: []result{redirect}}
	err := selfcheck.Check(context.Background(), probe(w), target)
	if err == nil || !strings.Contains(err.Error(), "443 on vm does not answer") {
		t.Errorf("a gateway that never answers: %v", err)
	}
}
```

Create `internal/render/selfcheck_test.go`:

```go
package render_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every host block of the gateway's Caddyfile answers the self check first,
// with the deployment's token.
func TestEveryRouteAnswersAsTheDeployment(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "golden", "vm", "srv", "paisans", "f2a9", "infra", "caddy", "Caddyfile"))
	if err != nil {
		t.Fatal(err)
	}
	c := string(b)
	if !strings.Contains(c, "@paisans_self path /.well-known/paisans/f2a9") || !strings.Contains(c, `respond "f2a9" 200`) {
		t.Fatalf("no self check snippet:\n%s", c)
	}
	blocks := strings.Count(c, "\timport upstream_unavailable\n")
	if imports := strings.Count(c, "\timport paisans_self\n"); imports != blocks || blocks == 0 {
		t.Errorf("%d host blocks, %d import the self check", blocks, imports)
	}
}
```

Create `cmd/paisans/gateway_selfcheck_test.go`:

```go
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/selfcheck"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// apply's gateway step ends on the check; a conflict fails it in the
// spec's words.
func TestApplyFailsAGatewayThatAnswersAsAnotherServer(t *testing.T) {
	cfg := fixtureConfig(t)
	saved := gatewayProbe
	defer func() { gatewayProbe = saved }()
	gatewayProbe = func() selfcheck.Probe {
		return selfcheck.Probe{Fetch: func(context.Context, string, string, string, string) (selfcheck.Answer, error) {
			return selfcheck.Answer{Status: 200, Body: "hello"}, nil
		}, Now: time.Now, Sleep: func(time.Duration) {}, Wait: time.Second}
	}
	gw := cfg.GatewaySites()[0]
	err := checkGatewayAnswers(&ui.Recorder{}, cfg, gw)
	if err == nil || !strings.Contains(err.Error(), "443 on "+gw+" answers, but not as this deployment") {
		t.Errorf("got %v", err)
	}
	if err := checkGatewayAnswers(&ui.Recorder{}, cfg, "home-a"); err != nil {
		t.Errorf("a site that is not a gateway was checked: %v", err)
	}
}
```

(`home-a` stands for any fixture site without the gateway role; pick one from `fixtureConfig`.)

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/selfcheck/ ./internal/render/ -run 'Self|EveryRoute'; go test ./cmd/paisans/ -run TestApplyFailsAGateway`
Expected: FAIL: no package `selfcheck`; the golden Caddyfile has no snippet.

- [ ] **Step 3: The check**

Create `internal/selfcheck/selfcheck.go`:

```go
// Package selfcheck asks a gateway, from outside, whether it answers as this
// deployment. The deployment's user cannot read the host's ruleset or
// another user's sockets, so a port another server took after admission,
// and above all one a DNAT rule takes with no listener, is invisible from
// the host. The gateway's Caddy answers /.well-known/paisans/<token> with
// the token; anything else that answers there is another server.
package selfcheck

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/dns"
)

// Path is where the gateway's Caddy answers as the deployment.
func Path(d deployment.Deployment) string { return "/.well-known/paisans/" + d.Token() }

// Body is what it answers with: the token, and nothing else.
func Body(d deployment.Deployment) string { return d.Token() }

// Answer is what one request got: its status, at most 4 KiB of its body, and
// a redirect's Location.
type Answer struct {
	Status   int
	Body     string
	Location string
}

// Fetch requests scheme://host+path from this machine, connecting to
// address, on 443 for https and 80 for http, whatever host resolves to, and
// never following a redirect.
type Fetch func(ctx context.Context, scheme, address, host, path string) (Answer, error)

// Probe is how a check reaches the world, replaced in tests. Wait is how
// long a gateway that does not answer yet is waited for: Caddy obtains its
// certificate over DNS-01 after it starts, and answers nothing on 443 until
// then.
type Probe struct {
	Fetch Fetch
	Now   func() time.Time
	Sleep func(time.Duration)
	Wait  time.Duration
}

// DefaultProbe reaches the real world from the operator's machine.
func DefaultProbe() Probe {
	return Probe{Fetch: fetch, Now: time.Now, Sleep: time.Sleep, Wait: 2 * time.Minute}
}

// fetch does not judge the certificate: the body is what tells this
// deployment from another server, and whether the certificate is valid is
// the certificate check's to say, which a staging ACME directory would fail.
func fetch(ctx context.Context, scheme, address, host, path string) (Answer, error) {
	port := "443"
	if scheme == "http" {
		port = "80"
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, net.JoinHostPort(address, port))
			},
			TLSClientConfig: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+host+path, nil)
	if err != nil {
		return Answer{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Answer{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return Answer{Status: resp.StatusCode, Body: string(body), Location: resp.Header.Get("Location")}, nil
}

// Target is one gateway, asked through its public address for one of the
// hostnames it serves.
type Target struct {
	Deployment          deployment.Deployment
	Site, Address, Host string
}

// TargetFor is site's check: its public_address, and the first hostname the
// gateway serves. false for a site that is not a gateway, or a gateway with
// no hostname to ask for.
func TargetFor(cfg *config.Config, site string) (Target, bool) {
	s, ok := cfg.Sites[site]
	if !ok || !s.Has(config.RoleGateway) || s.PublicAddress == "" {
		return Target{}, false
	}
	names := dns.GatewayHostnames(cfg)
	if len(names) == 0 {
		return Target{}, false
	}
	return Target{Deployment: cfg.Deployment(), Site: site, Address: s.PublicAddress, Host: names[0].Name}, true
}

// Conflict is a port that answers, but not as this deployment.
type Conflict struct {
	Port         int
	Site, Detail string
}

func (c *Conflict) Error() string {
	return fmt.Sprintf("%d on %s answers, but not as this deployment: %s", c.Port, c.Site, c.Detail)
}

// notYet is an answer worth waiting for: nothing listening, nothing
// answering, or a handshake Caddy fails while it has no certificate.
func notYet(err error) bool {
	var ne net.Error
	var tlsAlert tls.AlertError
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.As(err, &ne) && ne.Timeout() || errors.As(err, &tlsAlert)
}

// Check asks t on 443 for the token, waiting up to p.Wait while nothing
// answers, then on 80 for the redirect to the same URL on HTTPS. Any other
// answer is a Conflict.
func Check(ctx context.Context, p Probe, t Target) error {
	path, want := Path(t.Deployment), Body(t.Deployment)
	deadline := p.Now().Add(p.Wait)
	var a Answer
	var err error
	for {
		a, err = p.Fetch(ctx, "https", t.Address, t.Host, path)
		if err == nil || !notYet(err) || !p.Now().Before(deadline) {
			break
		}
		p.Sleep(5 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("443 on %s does not answer https://%s%s through %s: %w", t.Site, t.Host, path, t.Address, err)
	}
	if a.Status != http.StatusOK || strings.TrimSpace(a.Body) != want {
		return &Conflict{Port: 443, Site: t.Site, Detail: describe(a)}
	}
	a, err = p.Fetch(ctx, "http", t.Address, t.Host, path)
	if err != nil {
		return fmt.Errorf("80 on %s does not answer http://%s%s through %s: %w", t.Site, t.Host, path, t.Address, err)
	}
	if a.Status < 300 || a.Status > 399 || a.Location != "https://"+t.Host+path {
		return &Conflict{Port: 80, Site: t.Site, Detail: describe(a) + ", where this deployment's Caddy redirects to https://" + t.Host + path}
	}
	return nil
}

// describe is an answer in a line: its status, and its body or redirect
// shortened and stripped of anything that would redraw a terminal.
func describe(a Answer) string {
	out := fmt.Sprintf("status %d", a.Status)
	if a.Location != "" {
		out += ", redirecting to " + printable(a.Location)
	}
	if b := strings.TrimSpace(a.Body); b != "" {
		if len(b) > 80 {
			b = b[:80] + "..."
		}
		out += ", body " + fmt.Sprintf("%q", printable(b))
	}
	return out
}

func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}
```

- [ ] **Step 4: The Caddyfile**

In `internal/render/templates/Caddyfile.tmpl`, after the `(upstream_single)` snippet:

```caddyfile
# The gateway answers as this deployment at a path only it serves, with its
# token, and apply asks for it from outside after every gateway apply: a
# server that took 80 or 443 after admission answers anything else. A named
# matcher, which Caddy keeps in the order written, so this is matched before
# a gated host's sign in.
(paisans_self) {
	@paisans_self path /.well-known/paisans/{{ .Token }}
	handle @paisans_self {
		respond "{{ .Token }}" 200
	}
}
```

and in each of the three host block forms (gated, auto login, plain), make `import paisans_self` the first line inside the block, before `import upstream_unavailable`. In `internal/render/site.go`, the Caddyfile's data gains `"Token": p.dep().Token()`.

- [ ] **Step 5: Regenerate the golden trees and read the diff**

Run: `go test ./internal/render/ -update && git diff --stat internal/render/testdata`
Expected: only `Caddyfile`s change, each by the snippet and one `import paisans_self` per host block. Read `git diff internal/render/testdata`; any other change is a bug.

- [ ] **Step 6: apply's gateway step**

Create `cmd/paisans/gateway_selfcheck.go`:

```go
package main

import (
	"context"
	"errors"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/selfcheck"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// gatewayProbe is how apply reaches the gateway from outside. Tests replace
// it, so that none reaches the network.
var gatewayProbe = selfcheck.DefaultProbe

// checkGatewayAnswers ends a gateway's apply: its Caddy must answer its own
// /.well-known/paisans/<token> through the public address, on 443 and with
// the redirect on 80. Another server there fails the step, whatever the host
// check saw, since a port taken by a DNAT rule shows no listener to it.
func checkGatewayAnswers(r ui.Reporter, cfg *config.Config, site string) error {
	if !cfg.Sites[site].Has(config.RoleGateway) {
		return nil
	}
	t, ok := selfcheck.TargetFor(cfg, site)
	if !ok {
		r.Step("check the gateway answers as this deployment").End(ui.OK, "no hostname to ask for")
		return nil
	}
	s := r.Step("check the gateway answers as this deployment")
	s.Detail("https://%s%s through %s, and http on 80 for the redirect", t.Host, selfcheck.Path(t.Deployment), t.Address)
	if err := selfcheck.Check(context.Background(), gatewayProbe(), t); err != nil {
		s.Fail(err)
		var c *selfcheck.Conflict
		if errors.As(err, &c) {
			return &ui.Problem{Hint: c.Error(), Explain: "Another server answers on that port of the gateway's public address: a container of the host owner's, a DNAT rule, or another web server started after the host was admitted. The host's owner frees the port, or runs the site behind their web server (web_server.managed_by: external on a monitor), and apply runs again.", Cause: err}
		}
		return err
	}
	s.Done("")
	return nil
}
```

In `cmd/paisans/main.go`, after `recordApplied` succeeds and before the grant is recorded, when `*execute`:

```go
	if *execute {
		if err := checkGatewayAnswers(r, cfg, *site); err != nil {
			return err
		}
	}
```

In the `cmd/paisans` test setup that stubs `webServerProbes` (or in `TestMain`, if the package has one), stub `gatewayProbe` to answer as the deployment (`Answer{Status: 200, Body: <token>}` on https and the redirect on http), so no test reaches the network.

- [ ] **Step 7: Run, CHANGELOG, full suite, commit**

Run: `go test ./internal/selfcheck/ ./internal/render/ ./cmd/paisans/`
Expected: PASS.

Under `### Added`:

```markdown
- The gateway's Caddy answers `/.well-known/paisans/<token>`, and `apply` ends a gateway's step by fetching it through the public address, failing when another server answers on 443 or 80.
```

Run: `go vet ./... && go test ./...`, then:

```bash
git add internal/selfcheck internal/render cmd/paisans CHANGELOG.md
git commit -m "feat: check from outside that the gateway answers as this deployment" -m "As the deployment's user, apply cannot see a port the owner's Docker or
a DNAT rule took after admission. The gateway's Caddy answers a path with
the deployment's token, and apply asks for it through the public address
on 443, waiting while Caddy obtains its certificate, and on 80 for the
redirect. Any other answer fails the step, naming the port.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 17: The classification of sites

Rulings 26 to 30. Spec, *A deployment with sites missing*. This task builds the package and its tests alone; Task 19 wires it into the run.

**Files:**
- Create: `internal/topology/topology.go`, `internal/topology/gather.go`
- Test: `internal/topology/topology_test.go`, `internal/topology/gather_test.go`, `internal/topology/testdata/paisans-stack.yaml`

**Interfaces:**
- Consumes: `admission.Read`, `admission.ErrNotAdmitted`, `Record.Differences` (Task 9); `apply.Transport`, `apply.NoLogin` (Task 11), `apply.ReadEtcdMembers`, `apply.EtcdMemberSite`; `deployrecord.Gather`, `deployrecord.ErrNoRecord`; `patroni.ClusterCommand`, `patroni.Parse`; `render.PatroniAPIPort`; `blueprints.InObjectStorage`, `blueprints.UsesPostgres`; `config.Config.WithoutJoining`.
- Produces:

```go
type Class int // Ready, Pending, Down
type Site struct { Name string; Class Class; Why string }
type RecordRead struct { Record admission.Record; Err error }
type Evidence struct {
	Login        map[string]error      // nil: the site answered
	Records      map[string]RecordRead // for sites that answered
	Joined       map[string][]string   // source -> the sites it lists
	Unread       []string              // sources that could not be read
	DeployedApps []string              // the deployment record's apps
}
type Classification struct { Sites map[string]Site; Held []string; Warnings []string }
func Classify(cfg *config.Config, ev Evidence) (*Classification, error)
func (c *Classification) Pending() []string
func (c *Classification) Down() []string
func (c *Classification) Degraded() bool
func (c *Classification) Ready(site string) bool
func (c *Classification) Effective(cfg *config.Config) *config.Config
func (c *Classification) StorageHeld() bool
func (c *Classification) Refuse(cfg *config.Config, site string) error
func Gather(cfg *config.Config, transports map[string]apply.Transport) Evidence
func DownLine(site string) string
```

- [ ] **Step 1: The fixture**

Create `internal/topology/testdata/paisans-stack.yaml`: a copy of `internal/admission/testdata/paisans-stack.yaml` with a third data site and Garage on two sites at replication 2, so that "a three-site deployment with one data site pending" is expressible: read the admission fixture, copy it, and add (keeping its id, operators and apps):

```yaml
  home-c:
    roles: [data, storage]
    address: 10.44.0.3
    public_address: 203.0.113.12
    ssh:
      keys: [alice]
```

with `storage.garage.sites: [home-b, home-c]`, `storage.garage.replication: 2`, and one app whose blueprint keeps uploads in S3 (Eg: `writefreely`, whichever the admission fixture already declares that `blueprints.UsesObjectStorage` answers true for). Run `go run ./cmd/paisans validate --config internal/topology/testdata/paisans-stack.yaml` and fix it until it passes with no refusal.

- [ ] **Step 2: Write the failing tests**

Create `internal/topology/topology_test.go`:

```go
package topology_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/topology"
)

func fixture(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join("testdata", "paisans-stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// everyoneAnswers is evidence of a deployment whose every site answered with
// a record that covers it, and that nothing lists as joined yet.
func everyoneAnswers(t *testing.T, cfg *config.Config) topology.Evidence {
	t.Helper()
	ev := topology.Evidence{Login: map[string]error{}, Records: map[string]topology.RecordRead{}, Joined: map[string][]string{}}
	for _, name := range cfg.SiteNames() {
		req, err := admission.Build(cfg, name)
		if err != nil {
			t.Fatal(err)
		}
		rec := admission.NewRecord(req, req.Grants, "", time.Unix(0, 0))
		ev.Login[name] = nil
		ev.Records[name] = topology.RecordRead{Record: rec}
	}
	return ev
}

var refused = errors.Join(apply.ErrLoginRefused, errors.New("Permission denied (publickey)"))
var timedOut = errors.Join(apply.ErrUnreachable, errors.New("Connection timed out"))

// The spec's Tests: a site whose login is refused and which neither the
// deployment record nor the cluster lists is pending; the same site listed
// is down; a timeout reads the same as a refusal, either way.
func TestClassifyPendingAndDown(t *testing.T) {
	cfg := fixture(t)
	for _, failed := range []error{refused, timedOut} {
		ev := everyoneAnswers(t, cfg)
		ev.Login["home-c"] = failed
		delete(ev.Records, "home-c")
		c, err := topology.Classify(cfg, ev)
		if err != nil {
			t.Fatal(err)
		}
		if got := c.Sites["home-c"]; got.Class != topology.Pending {
			t.Errorf("%v, not listed: %+v", failed, got)
		}
		ev.Joined["etcd's membership"] = []string{"home-a", "home-b", "home-c"}
		c, err = topology.Classify(cfg, ev)
		if err != nil {
			t.Fatal(err)
		}
		if got := c.Sites["home-c"]; got.Class != topology.Down || got.Why != topology.DownLine("home-c") {
			t.Errorf("%v, listed: %+v", failed, got)
		}
	}
}

// The down line is the spec's, word for word.
func TestDownLine(t *testing.T) {
	want := "home-b: its login is refused or it does not answer: the host is down, or its owner evicted this deployment. paisans site remove --site home-b --host-gone removes it from the deployment."
	if got := topology.DownLine("home-b"); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// A site that answers with no record, or with one lacking a grant its roles
// need, is pending while nothing lists it.
func TestAnsweringWithoutAdmissionIsPending(t *testing.T) {
	cfg := fixture(t)
	ev := everyoneAnswers(t, cfg)
	ev.Records["home-c"] = topology.RecordRead{Err: admission.ErrNotAdmitted}
	rec := ev.Records["home-b"].Record
	rec.Grants.Watchdog = false
	ev.Records["home-b"] = topology.RecordRead{Record: rec}
	c, err := topology.Classify(cfg, ev)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Pending(), []string{"home-b", "home-c"}) {
		t.Errorf("pending %v", c.Pending())
	}
	if !strings.Contains(c.Sites["home-b"].Why, "--watchdog") {
		t.Errorf("why %q", c.Sites["home-b"].Why)
	}
}

// Review Focus: a site that does not answer while a source of evidence
// could not be read is never taken for pending.
func TestUnreadableEvidenceNeverMakesAJoinedSitePending(t *testing.T) {
	cfg := fixture(t)
	ev := everyoneAnswers(t, cfg)
	ev.Login["home-c"] = timedOut
	delete(ev.Records, "home-c")
	ev.Unread = []string{"the deployment record (no gateway answered)"}
	if _, err := topology.Classify(cfg, ev); err == nil || !strings.Contains(err.Error(), "the deployment record") {
		t.Errorf("got %v", err)
	}
	ev.Joined["etcd's membership"] = []string{"home-c"}
	c, err := topology.Classify(cfg, ev)
	if err != nil || c.Sites["home-c"].Class != topology.Down {
		t.Errorf("listed by a source that was read: %+v %v", c, err)
	}
}

// The effective topology is the declaration less the pending sites: no
// peer, member, Garage node or pinned app of theirs. A down site stays.
func TestEffectiveTopology(t *testing.T) {
	cfg := fixture(t)
	ev := everyoneAnswers(t, cfg)
	ev.Login["home-c"] = refused
	delete(ev.Records, "home-c")
	c, err := topology.Classify(cfg, ev)
	if err != nil {
		t.Fatal(err)
	}
	eff := c.Effective(cfg)
	if _, ok := eff.Sites["home-c"]; ok || slices.Contains(eff.Etcd.Members, "home-c") || slices.Contains(eff.Cluster.Sites, "home-c") || slices.Contains(eff.Storage.Garage.Sites, "home-c") {
		t.Errorf("home-c is still in %+v", eff)
	}
	if _, ok := cfg.Sites["home-c"]; !ok {
		t.Error("the declaration was changed")
	}
	ev.Joined["etcd's membership"] = []string{"home-c"}
	c, _ = topology.Classify(cfg, ev)
	if eff := c.Effective(cfg); !slices.Contains(eff.Etcd.Members, "home-c") {
		t.Errorf("a down site was left out: %v", eff.Etcd.Members)
	}
}

// Review Focus: with Garage short of its replication, an app the
// declaration keeps in object storage is held back, never rendered with
// local storage; one the deployment record lists as deployed refuses.
func TestEffectiveTopologyNeverMovesAnAppToLocalStorage(t *testing.T) {
	cfg := fixture(t)
	var s3 []string
	for _, name := range cfg.AppNames() {
		if blueprints.InObjectStorage(cfg, cfg.Apps[name]) {
			s3 = append(s3, name)
		}
	}
	if len(s3) == 0 {
		t.Fatal("the fixture has no app in object storage")
	}
	ev := everyoneAnswers(t, cfg)
	ev.Login["home-c"] = refused
	delete(ev.Records, "home-c")
	c, err := topology.Classify(cfg, ev)
	if err != nil {
		t.Fatal(err)
	}
	if !c.StorageHeld() || !slices.Equal(c.Held, s3) {
		t.Fatalf("held %v, want %v", c.Held, s3)
	}
	eff := c.Effective(cfg)
	for _, name := range eff.AppNames() {
		if blueprints.InObjectStorage(cfg, cfg.Apps[name]) && !blueprints.InObjectStorage(eff, eff.Apps[name]) {
			t.Errorf("%s moved to local storage", name)
		}
	}
	ev.DeployedApps = s3[:1]
	if _, err := topology.Classify(cfg, ev); err == nil || !strings.Contains(err.Error(), s3[0]) {
		t.Errorf("a deployed app held back: %v", err)
	}
}

// A site that dials out only, whose every route into the mesh is through
// pending sites, is pending too.
func TestASiteReachableOnlyThroughAPendingRelayIsPending(t *testing.T) {
	cfg := fixture(t)
	ev := everyoneAnswers(t, cfg)
	for _, name := range cfg.SiteNames() {
		if cfg.Sites[name].DialEndpoint() != "" {
			ev.Login[name] = refused
			delete(ev.Records, name)
		}
	}
	c, err := topology.Classify(cfg, ev)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range cfg.SiteNames() {
		if c.Sites[name].Class != topology.Pending {
			t.Errorf("%s is %v", name, c.Sites[name].Class)
		}
	}
}

// The spec's refusals: the gateway pending, the gateway down, no ready data
// site while an app needs a database, and --site naming a pending site.
func TestRefusals(t *testing.T) {
	cfg := fixture(t)
	gw := cfg.GatewaySites()[0]
	for name, set := range map[string]func(*topology.Evidence){
		"gateway pending": func(ev *topology.Evidence) { ev.Login[gw] = refused; delete(ev.Records, gw) },
		"gateway down": func(ev *topology.Evidence) {
			ev.Login[gw] = refused
			delete(ev.Records, gw)
			ev.Joined["the deployment record"] = []string{gw}
		},
		"no ready data site": func(ev *topology.Evidence) {
			for _, s := range cfg.DataSites() {
				ev.Login[s] = refused
				delete(ev.Records, s)
			}
		},
	} {
		ev := everyoneAnswers(t, cfg)
		set(&ev)
		c, err := topology.Classify(cfg, ev)
		if err == nil {
			err = c.Refuse(cfg, "")
		}
		if err == nil {
			t.Errorf("%s: not refused", name)
		}
	}
	ev := everyoneAnswers(t, cfg)
	ev.Login["home-c"] = refused
	delete(ev.Records, "home-c")
	c, err := topology.Classify(cfg, ev)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Refuse(cfg, "home-c"); err == nil || !strings.Contains(err.Error(), "--site home-c") {
		t.Errorf("--site pending: %v", err)
	}
	if err := c.Refuse(cfg, ""); err != nil || !c.Degraded() {
		t.Errorf("one pending data site of three: %v, degraded %v", err, c.Degraded())
	}
}
```

Adjust the fixture's site names in these tests to the ones Step 1's file declares (the gateway, the data sites, the site with a dial endpoint) where they differ.

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/topology/`
Expected: FAIL, no package `topology`.

- [ ] **Step 4: Implement the classification**

Create `internal/topology/topology.go`:

```go
// Package topology classifies a deployment's declared sites at the start of
// every run, once: ready, pending or down, from what the deployment says has
// joined, never from the site's own host alone, since a host that evicted the
// deployment, one that is down and one that never admitted it look alike from
// outside. A run renders the effective topology, the declaration less its
// pending sites, and runs on the sites it has. See the spec's "A deployment
// with sites missing" (docs/specs/2026-10-15-rootless-and-host-admit.md).
package topology

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// Class is what a site is to this run.
type Class int

const (
	// Ready is a site that answers with an admission covering its roles.
	Ready Class = iota
	// Pending is a site the deployment has not joined that cannot be used
	// yet: it is left out of the topology, as if not yet added.
	Pending
	// Down is a joined site that does not let the run in: it is kept as
	// declared, a peer and a member, and only its own steps are skipped.
	Down
)

func (c Class) String() string { return [...]string{"ready", "pending", "down"}[c] }

// Site is one declared site's class, and for a pending or down one, why.
type Site struct {
	Name  string
	Class Class
	Why   string
}

// RecordRead is a site's admission record as read, or why it was not.
type RecordRead struct {
	Record admission.Record
	Err    error
}

// Evidence is what a run read about its sites. Gather reads it; Classify
// judges it.
type Evidence struct {
	// Login is each site's answer to its login, nil when it let the run in.
	Login map[string]error
	// Records is the admission record of each site that answered.
	Records map[string]RecordRead
	// Joined is each source of evidence that was read, by name, and the
	// sites it lists as part of the deployment.
	Joined map[string][]string
	// Unread names each source that could not be read, and why.
	Unread []string
	// DeployedApps is the apps the deployment record lists as deployed.
	DeployedApps []string
}

// Classification is the run's view of its sites.
type Classification struct {
	Sites map[string]Site
	// Held is the apps left out of the run because Garage has fewer nodes
	// than its replication, sorted.
	Held []string
	// Warnings are what the run says about what it holds back.
	Warnings []string
}

// DownLine is a down site's line. It cannot say whether the host is down or
// its owner evicted the deployment, so it says both.
func DownLine(site string) string {
	return fmt.Sprintf("%[1]s: its login is refused or it does not answer: the host is down, or its owner evicted this deployment. paisans site remove --site %[1]s --host-gone removes it from the deployment.", site)
}

// Classify judges ev. It refuses only where a site's class cannot be told
// safely: a site that does not answer while evidence could not be read, a
// joined site whose admission no longer covers it, and an app the
// deployment runs that Garage could not hold.
func Classify(cfg *config.Config, ev Evidence) (*Classification, error) {
	c := &Classification{Sites: map[string]Site{}}
	listed := func(site string) bool {
		for _, sites := range ev.Joined {
			if slices.Contains(sites, site) {
				return true
			}
		}
		return false
	}
	for _, name := range cfg.SiteNames() {
		s := cfg.Sites[name]
		joined := listed(name)
		if err := ev.Login[name]; err != nil {
			switch {
			case joined:
				c.Sites[name] = Site{Name: name, Class: Down, Why: DownLine(name)}
			case len(ev.Unread) > 0:
				return nil, fmt.Errorf("%s does not let this run in (%v), and whether it has joined the deployment cannot be told: %s could not be read. A joined site left out of the mesh and the cluster would be cut off from them, so the run stops. Run it again once one of them answers", name, err, strings.Join(ev.Unread, ", and "))
			default:
				c.Sites[name] = Site{Name: name, Class: Pending, Why: fmt.Sprintf("%s: its login is refused or it does not answer, and the deployment has not joined it: its host's owner has not admitted it yet", name)}
			}
			continue
		}
		rr := ev.Records[name]
		var why string
		switch {
		case errors.Is(rr.Err, admission.ErrNotAdmitted):
			why = "its host has not admitted this deployment"
		case rr.Err != nil:
			why = "its admission record cannot be read: " + rr.Err.Error()
		default:
			if diff := rr.Record.Differences(name, s); len(diff) > 0 {
				why = "its admission does not cover it: " + strings.Join(diff, "; ")
			}
		}
		switch {
		case why == "":
			c.Sites[name] = Site{Name: name, Class: Ready}
		case joined:
			return nil, fmt.Errorf("%s has joined this deployment, and %s. The host's owner admits it again (sudo paisans host admit --request %s.admit, from paisans host request --site %s > %s.admit) before the run goes on", name, why, name, name, name)
		default:
			c.Sites[name] = Site{Name: name, Class: Pending, Why: name + ": " + why}
		}
	}
	c.relays(cfg)
	if err := c.garage(cfg, ev.DeployedApps); err != nil {
		return nil, err
	}
	return c, nil
}

// relays makes pending each site that dials out only and whose every route
// into the mesh, a site with a dial endpoint, is pending, until nothing
// changes.
func (c *Classification) relays(cfg *config.Config) {
	for changed := true; changed; {
		changed = false
		var endpoints, live []string
		for _, name := range cfg.SiteNames() {
			if cfg.Sites[name].DialEndpoint() != "" {
				endpoints = append(endpoints, name)
				if c.Sites[name].Class != Pending {
					live = append(live, name)
				}
			}
		}
		if len(endpoints) == 0 {
			return
		}
		for _, name := range cfg.SiteNames() {
			s := c.Sites[name]
			if s.Class == Pending || cfg.Sites[name].DialEndpoint() != "" || slices.ContainsFunc(live, func(o string) bool { return o != name }) {
				continue
			}
			c.Sites[name] = Site{Name: name, Class: Pending, Why: fmt.Sprintf("%s: its only route into the mesh is through %s, which is pending", name, strings.Join(endpoints, ", "))}
			changed = true
		}
	}
}

// garage holds back storage when the Garage nodes the run keeps, ready and
// down, number fewer than its replication: every app the declaration keeps in
// object storage is held, so none is ever rendered with local storage, and
// one already deployed refuses the run, since its objects would be left
// behind.
func (c *Classification) garage(cfg *config.Config, deployed []string) error {
	g := cfg.Storage.Garage
	if len(g.Sites) == 0 {
		return nil
	}
	kept := 0
	for _, s := range g.Sites {
		if c.Sites[s].Class != Pending {
			kept++
		}
	}
	if kept >= g.Replication {
		return nil
	}
	for _, name := range cfg.AppNames() {
		if blueprints.InObjectStorage(cfg, cfg.Apps[name]) {
			if slices.Contains(deployed, name) {
				return fmt.Errorf("Garage has %d of its sites in the run and keeps %d copies, so it cannot take objects, and %s keeps its uploads there and is deployed. The run stops rather than run %s without its storage. Admit the pending Garage sites first", kept, g.Replication, name, name)
			}
			c.Held = append(c.Held, name)
		}
	}
	if len(c.Held) > 0 {
		c.Warnings = append(c.Warnings, fmt.Sprintf("storage held back: Garage has %d of its sites in the run and keeps %d copies, so %s wait until the pending Garage sites are admitted", kept, g.Replication, strings.Join(c.Held, ", ")))
	}
	return nil
}

func (c *Classification) of(class Class) []string {
	var out []string
	for name, s := range c.Sites {
		if s.Class == class {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// Pending is the pending sites, sorted.
func (c *Classification) Pending() []string { return c.of(Pending) }

// Down is the down sites, sorted.
func (c *Classification) Down() []string { return c.of(Down) }

// Degraded reports whether the run is without any declared site.
func (c *Classification) Degraded() bool { return len(c.Pending())+len(c.Down()) > 0 }

// Ready reports whether site is ready.
func (c *Classification) Ready(site string) bool { return c.Sites[site].Class == Ready }

// StorageHeld reports whether the run holds back storage.
func (c *Classification) StorageHeld() bool { return len(c.Held) > 0 }

// Effective is the topology the run renders: cfg less its pending sites and
// the apps pinned to them, and less the apps held back. The declaration is
// not changed.
func (c *Classification) Effective(cfg *config.Config) *config.Config {
	out := cfg.WithoutJoining(c.Pending()...)
	if len(c.Held) == 0 {
		return out
	}
	copied := *out
	copied.Apps = map[string]config.App{}
	for name, app := range out.Apps {
		if !slices.Contains(c.Held, name) {
			copied.Apps[name] = app
		}
	}
	return &copied
}

// Refuse is what stops the run: the gateway pending or down, no ready data
// site while an app needs a database, etcd without a quorum among its
// members that answer, a Patroni cluster none of whose members answer, and
// site naming a site that is pending or down.
func (c *Classification) Refuse(cfg *config.Config, site string) error {
	if site != "" && !c.Ready(site) {
		return fmt.Errorf("--site %s: %s", site, c.Sites[site].Why)
	}
	for _, gw := range cfg.GatewaySites() {
		if !c.Ready(gw) {
			return fmt.Errorf("the gateway %s is %s, and every public hostname goes through it: %s", gw, c.Sites[gw].Class, c.Sites[gw].Why)
		}
	}
	eff := c.Effective(cfg)
	needsDB := slices.ContainsFunc(eff.AppNames(), func(name string) bool { return blueprints.UsesPostgres(eff.Apps[name].Blueprint) })
	if needsDB && !slices.ContainsFunc(cfg.DataSites(), c.Ready) {
		return fmt.Errorf("no data site is ready, and %s need a database: %s", strings.Join(dbApps(eff), ", "), strings.Join(c.whys(cfg.DataSites()), "; "))
	}
	if members := eff.Etcd.Members; len(members) > 0 {
		ready := 0
		for _, m := range members {
			if c.Ready(m) {
				ready++
			}
		}
		if ready <= len(members)/2 {
			return fmt.Errorf("etcd has %d of %d members ready, and needs %d for a quorum: %s", ready, len(members), len(members)/2+1, strings.Join(c.whys(members), "; "))
		}
	}
	if sites := eff.Cluster.Sites; len(sites) > 0 && !slices.ContainsFunc(sites, c.Ready) {
		return fmt.Errorf("no member of the Patroni cluster is ready: %s", strings.Join(c.whys(sites), "; "))
	}
	return nil
}

func (c *Classification) whys(sites []string) []string {
	var out []string
	for _, s := range sites {
		if w := c.Sites[s].Why; w != "" {
			out = append(out, w)
		}
	}
	return out
}

func dbApps(cfg *config.Config) []string {
	var out []string
	for _, name := range cfg.AppNames() {
		if blueprints.UsesPostgres(cfg.Apps[name].Blueprint) {
			out = append(out, name)
		}
	}
	return out
}
```

- [ ] **Step 5: Gathering the evidence**

Create `internal/topology/gather.go`:

```go
package topology

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"github.com/paisans-software/paisans-cli/internal/patroni"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// Gather reads a run's evidence: each site's login and, where it answered,
// its admission record; then what the deployment says has joined, each from
// the first site of its kind that answers: the gateways' deployment record,
// etcd's membership and Patroni's. A source no site of its kind answered for
// is Unread.
func Gather(cfg *config.Config, transports map[string]apply.Transport) Evidence {
	d := cfg.Deployment()
	ev := Evidence{Login: map[string]error{}, Records: map[string]RecordRead{}, Joined: map[string][]string{}}
	for _, name := range cfg.SiteNames() {
		t, ok := transports[name]
		if !ok {
			ev.Login[name] = fmt.Errorf("no way to reach it")
			continue
		}
		if _, err := t.Run("true"); err != nil {
			ev.Login[name] = err
			continue
		}
		ev.Login[name] = nil
		rec, err := admission.Read(t, d.Token())
		ev.Records[name] = RecordRead{Record: rec, Err: err}
	}
	answered := func(sites []string) []string {
		var out []string
		for _, s := range sites {
			if err, ok := ev.Login[s]; ok && err == nil {
				out = append(out, s)
			}
		}
		return out
	}

	gateways := map[string]registry.Runner{}
	for _, gw := range answered(cfg.GatewaySites()) {
		gateways[gw] = transports[gw]
	}
	// The deployment record lists every site a gateway's apply has
	// deployed, so it is the one source whose silence about a site is
	// evidence: a run that cannot read it cannot tell a pending site from a
	// down one. A gateway that answered with no record yet is a deployment
	// never applied, which lists nothing.
	if len(cfg.GatewaySites()) > 0 {
		rec, found, missing := deployrecord.Gather(gateways, d)
		var unreadable []string
		for gw, err := range missing {
			if !errors.Is(err, deployrecord.ErrNoRecord) {
				unreadable = append(unreadable, gw+": "+err.Error())
			}
		}
		slices.Sort(unreadable)
		switch {
		case len(gateways) == 0:
			ev.Unread = append(ev.Unread, "the deployment record (no gateway answered)")
		case found > 0:
			ev.Joined["the deployment record"] = rec.List("sites")
			ev.DeployedApps = rec.List("apps")
		case len(unreadable) > 0:
			ev.Unread = append(ev.Unread, "the deployment record ("+strings.Join(unreadable, "; ")+")")
		}
	}

	// etcd's and Patroni's members are read from the first member that
	// answers; one that answers nowhere adds nothing.
	for _, m := range answered(cfg.Etcd.Members) {
		list, running, err := apply.ReadEtcdMembers(transports[m], d)
		if err != nil || !running {
			continue
		}
		var sites []string
		for _, em := range list {
			sites = append(sites, apply.EtcdMemberSite(cfg, em))
		}
		ev.Joined["etcd's membership"] = sites
		break
	}

	for _, s := range answered(cfg.Cluster.Sites) {
		api := fmt.Sprintf("%s:%d", cfg.Sites[s].Address, render.PatroniAPIPort)
		out, err := transports[s].Run(patroni.ClusterCommand(d, api))
		if err != nil {
			continue
		}
		c, err := patroni.Parse(out)
		if err != nil {
			continue
		}
		var sites []string
		for _, m := range c.Members {
			sites = append(sites, m.Name)
		}
		slices.Sort(sites)
		ev.Joined["Patroni's members"] = sites
		break
	}
	return ev
}
```

Patroni's members are named by site (`patroni.env`: "name is the site's name"); a cluster none of whose members answers is not counted as unread, since every data site is an etcd member and etcd's membership already covers it.

Create `internal/topology/gather_test.go` with a fake transport keyed by command substring (as `internal/hostcheck/hosts_test.go`'s), and:

```go
// Each source is read from the first site of its kind that answers; a source
// none answered for is unread; a site that did not answer is not asked for
// its record.
func TestGatherReadsEachSourceFromTheFirstThatAnswers(t *testing.T)
```

asserting, for the fixture with its first etcd member refusing the login: `ev.Login` holds that error, `ev.Records` has no entry for it, the second member's `etcdctl member list` was run and its sites are `ev.Joined["etcd's membership"]`; with every gateway refusing, `ev.Unread` names the deployment record; and with a gateway answering and no record on it, `ev.Unread` is empty and `ev.Joined` has no deployment record. Answer `etcdctl member list -w json` with the JSON shape `internal/apply/etcd_test.go` uses, and the deployment record with `deployrecord.Encode`.

- [ ] **Step 6: Run, full suite, commit**

Run: `go test ./internal/topology/`
Expected: PASS.

Run: `go vet ./... && go test ./...`, then:

```bash
git add internal/topology
git commit -m "feat: classify a deployment's sites as ready, pending or down" -m "A run must go on with the sites it has, and must never drop a joined site
from the mesh. Classification reads whether a site has joined from the
deployment, the gateways' record and etcd's and Patroni's membership, never
from the site's host alone, and refuses when that cannot be told. Pending
sites are left out of the effective topology; storage is held back, never
moved to local disk, when Garage is short of its replication.

Nothing calls it yet; the run is wired to it next.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

No CHANGELOG bullet: nothing an operator runs uses it until Task 19.

---

### Task 20: Docker-tagged tests against a rootless daemon

Ruling 33.

**Files:**
- Create: `internal/dockertest/dockertest.go`, `internal/dockertest/dockertest_test.go`
- Modify: `internal/garage/integration_test.go`, `internal/storageadd/integration_test.go`, `internal/storageadd/timing_real_test.go`, `internal/render/standby_integration_test.go`, `internal/render/writefreely_integration_test.go`
- Modify: `docs/development.md` (*Tests*)

**Interfaces:**
- Produces: `func dockertest.RequireRootless(t testing.TB)`; `func dockertest.Rootless(securityOptions string) bool`.

- [ ] **Step 1: Write the failing test**

Create `internal/dockertest/dockertest_test.go`:

```go
package dockertest_test

import (
	"testing"

	"github.com/paisans-software/paisans-cli/internal/dockertest"
)

// docker info lists rootless among the daemon's security options when it is
// one (Docker's rootless documentation).
func TestRootless(t *testing.T) {
	for in, want := range map[string]bool{
		`["name=seccomp,profile=builtin","name=rootless","name=cgroupns"]`: true,
		`["name=apparmor","name=seccomp,profile=builtin","name=cgroupns"]`: false,
		``: false,
	} {
		if got := dockertest.Rootless(in); got != want {
			t.Errorf("%s: %v", in, got)
		}
	}
}
```

Run: `go test ./internal/dockertest/`
Expected: FAIL, no package.

- [ ] **Step 2: Implement**

Create `internal/dockertest/dockertest.go`:

```go
// Package dockertest is what every Docker-backed test, behind its build tag,
// calls first: the deployment's daemon is a rootless one, so a test that
// passes against a rootful daemon proves nothing about it.
package dockertest

import (
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// Rootless reports whether docker info's security options, as JSON, name
// rootless.
func Rootless(securityOptions string) bool {
	var opts []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(securityOptions)), &opts); err != nil {
		return false
	}
	return slices.Contains(opts, "name=rootless")
}

// RequireRootless skips the test when Docker is absent or not running, and
// fails it when the daemon is not rootless, unless PAISANS_DOCKER_ROOTFUL=1
// says to run against a rootful one, which it then logs.
func RequireRootless(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not on PATH")
	}
	out, err := exec.Command("docker", "info", "--format", "{{json .SecurityOptions}}").Output()
	if err != nil {
		t.Skipf("docker is not running: %v", err)
	}
	if Rootless(string(out)) {
		return
	}
	if os.Getenv("PAISANS_DOCKER_ROOTFUL") == "1" {
		t.Logf("running against a rootful Docker (PAISANS_DOCKER_ROOTFUL=1): this proves nothing about a deployment's rootless daemon")
		return
	}
	t.Fatalf("the Docker daemon DOCKER_HOST reaches is not rootless (security options %s). Point DOCKER_HOST at a rootless daemon, Eg: unix:///run/user/$(id -u)/docker.sock, or set PAISANS_DOCKER_ROOTFUL=1 to run against this one", strings.TrimSpace(string(out)))
}
```

- [ ] **Step 3: Call it from every Docker-backed test**

In each file under **Files**, replace the `exec.LookPath("docker")` check (and `storageadd`'s `docker info` check after it) at the top of each test or setup helper with `dockertest.RequireRootless(t)`. `internal/render/volumes_registry_test.go` (`registry_integration`) reaches no Docker and is left alone.

Run: `go vet -tags 'garage_integration standby_integration writefreely_integration' ./internal/garage/ ./internal/storageadd/ ./internal/render/`
Expected: no output.

- [ ] **Step 4: Document and commit**

In `docs/development.md`, *Tests*, after the paragraph on Docker tests, add:

```markdown
They run against a rootless Docker, the daemon a deployment runs: each calls `dockertest.RequireRootless`, which fails a test whose daemon is not rootless. Point `DOCKER_HOST` at one (`unix:///run/user/$(id -u)/docker.sock` on a Linux machine with Docker's rootless extras), or set `PAISANS_DOCKER_ROOTFUL=1` to run them against a rootful daemon, which they then say proves nothing about a deployment's.
```

Run: `go vet ./... && go test ./...`; if a rootless daemon is at hand, also `go test -tags garage_integration ./internal/garage/ ./internal/storageadd/` and report whether it ran. Then:

```bash
git add internal/dockertest internal/garage internal/storageadd internal/render docs/development.md
git commit -m "test: run the docker-tagged tests against a rootless daemon" -m "Every deployment runs on its user's rootless Docker, so a Docker test that
passes against a rootful daemon says nothing about it. Each tagged test
checks the daemon first and fails on a rootful one, unless asked to run
there anyway, which it then logs.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

No CHANGELOG bullet: nothing an operator runs changes.

---
### Task 15: `include_dir` refused, the grant's web server part gone

Rulings 24, 25. Spec: "`include_dir` is refused by `validate`. Nothing is written into the owner's configuration and nothing of theirs is reloaded."

**Files:**
- Modify: `internal/validate/monitor.go`, `internal/validate/monitor_test.go` (or the file holding the web server refusals' tests: `grep -ln 'web-server-include-dir' internal/validate`)
- Delete: `internal/config/include_dir.go` and its test (`SetIncludeDir`)
- Modify: `internal/config/config.go` (the `IncludeDir` field's comment; `IncludeDirProblem` deleted)
- Modify: `internal/grant/grant.go`, `internal/grant/record.go`, `internal/grant/review.go`, `internal/grant/*_test.go`
- Modify: `cmd/paisans/main.go` (the web server grant scope at the end of `applyCommand`), `cmd/paisans/converge.go` (`convergeRecordWebServer`), `cmd/paisans/review.go`, their tests
- Modify: `CHANGELOG.md`

**Interfaces:**
- Produces: validate code `web-server-include-dir`; removed: `config.SetIncludeDir`, `config.IncludeDirProblem`, `grant.WebServer`, `grant.WebServerOf`, `Grant.WebServer`, `grant.WebServerPart`, `Record.WebServer`, `Scope.WebServer`, `compareWebServer`, `convergeRecordWebServer`. `config.WebServer.IncludeDir` stays, parsed only to be refused.

- [ ] **Step 1: Write the failing test**

Add beside the web server refusals' tests in `internal/validate`:

```go
// include_dir is refused by name, whatever it holds: nothing is written into
// the owner's web server, so there is nothing for it to name.
func TestIncludeDirIsRefused(t *testing.T) {
	for _, dir := range []string{"/etc/nginx/paisans.d", "false"} {
		cfg := externalMonitor(t) // the helper the file uses for a monitor with managed_by: external
		s := cfg.Sites["watch"]
		if dir == "false" {
			s.WebServer.IncludeDir = config.IncludeDir{Off: true}
		} else {
			s.WebServer.IncludeDir = config.IncludeDir{Path: dir}
		}
		cfg.Sites["watch"] = s
		r := validate.Check(cfg)
		if !hasRefusal(r, "web-server-include-dir") {
			t.Errorf("include_dir %s accepted: %+v", dir, r.Refusals())
		}
	}
}
```

(use the monitor site's name and the helpers the file defines). Delete the tests of `web-server-include-dir-managed-by` and `web-server-include-dir-path`, the tests of `config.SetIncludeDir`, and the grant tests of the web server part (`grep -ln 'WebServer' internal/grant/*_test.go`).

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/validate/ -run TestIncludeDirIsRefused`
Expected: FAIL: `include_dir: false` is accepted.

- [ ] **Step 3: Implement**

In `internal/validate/monitor.go`, replace the two include_dir refusals with one:

```go
		if !site.WebServer.IncludeDir.Unset() {
			c.refuse("web-server-include-dir", key+".include_dir",
				"web_server include_dir is not a setting",
				"is set. apply writes nothing into your web server's configuration: it writes a reference snippet for each app under %s/web-server/ on the site, and the host's owner installs it in their web server (paisans web-server show --app <app> prints it). Remove include_dir.", c.cfg.Deployment().Root())
		}
```

and rewrite the function's comment: `include_dir` is refused, since apply writes only into the deployment's own root. In `internal/config/config.go`, `IncludeDir`'s comment says it is parsed so that `validate` can refuse it by name. Delete `include_dir.go` and `IncludeDirProblem`.

In `internal/grant`: delete `WebServer`, `WebServerOf`, the `WebServer` field of `Grant`, `WebServerPart`, `Record.WebServer`, `Scope.WebServer` (and from `ApplyScope` and `AllScope`), `compareWebServer` and the record update's web server branch. A record that holds a `web_server` key from an earlier version is refused by `Parse` as today's unknown fields are; nothing is released, so nothing reads one.

In `cmd/paisans/main.go`, at the end of `applyCommand`, the grant is recorded once, with the review's scope as it is (`recordGrant(r, review, cfg.Deployment(), approvedBy)`), and the second, web-server-only record goes. In `cmd/paisans/converge.go`, `convergeRecordWebServer` and its call go. In `cmd/paisans/review.go`, any line about the web server's part goes.

- [ ] **Step 4: Run, CHANGELOG, full suite, commit**

Run: `go test ./internal/validate/ ./internal/config/ ./internal/grant/ ./cmd/paisans/`
Expected: PASS after removing what references the deleted names (`go vet ./...` lists them).

Under `### Removed`:

```markdown
- `web_server.include_dir`: `validate` refuses it, since `apply` writes nothing into the host owner's web server.
```

Run: `go vet ./... && go test ./...`, then:

```bash
git add internal/validate internal/config internal/grant cmd/paisans CHANGELOG.md
git commit -m "feat: refuse web_server.include_dir and drop it from the review" -m "The deployment's user cannot write the owner's web server configuration,
test it or reload it, and nothing grants it that. apply hands the owner a
reference snippet instead, so include_dir names nothing: validate refuses
it by name, and the grant loses the part that approved writing there.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 16: A reference snippet for the owner's web server

Ruling 23. Spec, *Behind the owner's web server*: the toolkit renders, the owner installs. `apply` writes the reference snippet in the deployment's root, filled from the admission record's detection, checks the app from outside, and warns until it passes; `web-server show` prints the sheet from the record; `app remove` and `site remove` say which block the owner removes.

**Files:**
- Create: `internal/webserver/reference.go`, `internal/webserver/reference_test.go`
- Delete: `internal/webserver/include.go`, `internal/webserver/include_test.go`, `internal/webserver/compose.go`, `internal/webserver/compose_test.go` (`FileName`'s callers move to `References`; `FileContent` moves to `reference.go`)
- Modify: `cmd/paisans/converge_webserver_files.go`, `cmd/paisans/converge_webserver.go`, `cmd/paisans/webserver.go`
- Modify: `cmd/paisans/converge_webserver_files_test.go`, `cmd/paisans/converge_webserver_test.go`, `cmd/paisans/site_webserver_test.go`, `cmd/paisans/webserver_test.go`
- Modify: `internal/appremove/execute.go`, `internal/appremove/print.go`, `internal/appremove/webserver_test.go`
- Modify: `internal/siteremove/host.go`, `internal/siteremove/webserver_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: `admission.Read`, `admission.WebServer`; `apply.ReadWebServerRecord`, `apply.WriteWebServerRecord`, `render.ManifestWebServer`; `webserver.Target`, `webserver.WebServer`, `Show`, `Check`, `Detect` (still admit's).
- Produces: `func webserver.ReferenceDir(d deployment.Deployment) string` (`Root()/web-server`); `type webserver.Reference struct { App, Path, Content string; Server Server }`; `func webserver.References(d deployment.Deployment, t Target, ws WebServer) []Reference`; `func webserver.FromRecord(r *admission.WebServer) WebServer`; `func webserver.FileContent(t Target, ws WebServer) string` (moved, header rewritten). In `cmd/paisans`: `siteFile{ until, path string; ws webserver.WebServer; changed bool }`.

- [ ] **Step 1: Write the failing tests**

Create `internal/webserver/reference_test.go`:

```go
package webserver_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/webserver"
)

var dep = deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}

var status = webserver.Target{App: "status", Site: "watch", Hostname: "status.example.org", Listen: "127.0.0.1:8480", ListenHost: "127.0.0.1", ListenPort: 8480, HealthPath: "/", HealthExpect: "200"}

// One reference file per app for the server admit found, in the
// deployment's own root; one per kind when admit found none.
func TestReferences(t *testing.T) {
	refs := webserver.References(dep, status, webserver.WebServer{Server: webserver.ServerNginx})
	if len(refs) != 1 || refs[0].Path != "/srv/paisans/f2a9/web-server/status.conf" || !strings.Contains(refs[0].Content, "proxy_pass http://127.0.0.1:8480") {
		t.Errorf("nginx: %+v", refs)
	}
	refs = webserver.References(dep, status, webserver.WebServer{})
	var paths []string
	for _, r := range refs {
		paths = append(paths, r.Path)
	}
	if strings.Join(paths, " ") != "/srv/paisans/f2a9/web-server/status.caddy /srv/paisans/f2a9/web-server/status.nginx.conf /srv/paisans/f2a9/web-server/status.apache.conf" {
		t.Errorf("unknown server: %v", paths)
	}
	if !strings.Contains(refs[0].Content, "for the host's owner") {
		t.Errorf("header: %s", refs[0].Content)
	}
}

// The record's detection, every field, back into the type Show reads.
func TestFromRecord(t *testing.T) {
	ws := webserver.FromRecord(&admission.WebServer{Server: "caddy", Process: "caddy", Config: "/etc/caddy/Caddyfile", Main: "/etc/caddy/Caddyfile", Certs: "automatic"})
	if ws.Server != webserver.ServerCaddy || ws.Certs != webserver.CertsAutomatic || ws.Main != "/etc/caddy/Caddyfile" {
		t.Errorf("got %+v", ws)
	}
	if ws := webserver.FromRecord(nil); ws.Server != "" {
		t.Errorf("nil record: %+v", ws)
	}
}
```

(the expected `proxy_pass` line is what `nginxSnippet` writes today for a listen address; read it and match it.)

In `cmd/paisans`, delete the tests of the include, reload, rollback and mount diagnosis (`TestApplyWritesTheSiteFileWhereTheServerIncludesIt`, `TestApplyWritesNothingWhereTheServerDoesNotIncludeIt`, `TestApplyRefusesALiteralInclude`, `TestApplyRefusesAnImportAboveTheGlobalOptions`, `TestApplyRefusesAnImportInsideABlock`, `TestApplyWithIncludeDirFalseWritesNothing`, `TestApplyWithIncludeDirFalseRemovesWhatItWrote`, `TestApplyAdvisesTheMountForASingleFileMount`, `TestApplyReportsTheServersRefusal`, `TestApplyRestartsAWebServerWhoseReloadFails`, `TestApplyLeavesTheReloadOfABareMetalServer`, `TestApplyGivesOneSetOfInstructionsPerProblem`, `TestApplyDiagnosesTheMountFromTheComposeFile`, `TestApplyReloadsAndChecksAgainWhenTheCheckFails`, `TestApplyWaitsForTheCertificateOfAReloadedServer`, `TestApplyRewritesTheSiteFileForARenamedHostname`, `TestApplySiteWritesTheSiteFileAndReloads`, `TestApplySiteWarnsWhereTheServerDoesNotIncludeTheDirectory`), and add to `converge_webserver_files_test.go`, built on the external monitor fixture and fake host the file already uses (read it; its host must now serve the admission record with a `web_server` detection):

```go
// apply writes the reference snippet from the admission record, in the
// deployment's root, and touches nothing of the owner's web server.
func TestApplyWritesTheReferenceSnippetFromTheRecord(t *testing.T)

// Until the owner's copy serves the app, the step ends on a warning naming
// web-server show for the owner.
func TestApplyWarnsUntilTheOwnersCopyServes(t *testing.T)

// When apply rewrites a reference file, the warning says the owner's copy
// needs replacing.
func TestAChangedReferenceSaysTheOwnersCopyNeedsReplacing(t *testing.T)
```

each asserting, in order: the host's written files are exactly `/srv/paisans/f2a9/web-server/<app>.caddy` (the record's server is Caddy) and the manifest, and no command contains `caddy reload`, `nginx -s`, `apachectl`, `docker exec`, `docker restart` or the owner's configuration path; with the check stubbed to fail, the run ends on a `ui.Warning` whose text contains `paisans web-server show --app <app>`; with the manifest's recorded checksum for that path differing from the new content, the warning contains `the host's owner replaces their copy`. In `webserver_test.go`, `TestWebServerShowPrintsTheDetectedServer` serves the record and asserts the sheet names the record's server; `TestWebServerHostReadsAsTheSSHUser` asserts no `docker ps` or `ss` ran.

In `internal/appremove/webserver_test.go`, replace the three tests with:

```go
// app remove deletes the app's reference file, its own, and says which
// block the host's owner removes from their web server.
func TestAppRemoveNamesTheBlockTheOwnerRemoves(t *testing.T)
```

asserting the `rm -f --` of the reference path ran, nothing ran against the web server, and the plan's kept lines contain `the host's owner removes the block for <hostname>`. Do the same in `internal/siteremove/webserver_test.go` (`TestSiteRemoveNamesTheBlockTheOwnerRemoves`), deleting its four tests.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/webserver/ -run 'TestReferences|TestFromRecord'`
Expected: FAIL, `undefined: webserver.References`.

- [ ] **Step 3: The reference files**

Create `internal/webserver/reference.go`:

```go
package webserver

import (
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// ReferenceDir is where apply writes, in the deployment's own root, the
// snippet the host's owner installs in their web server for each app it
// serves. The deployment's user cannot write the owner's configuration, test
// it or reload the server, and nothing grants it that: the toolkit renders,
// the owner installs.
func ReferenceDir(d deployment.Deployment) string { return d.Path("web-server") }

// Reference is one reference file: an app's snippet for one kind of server.
type Reference struct {
	App, Path, Content string
	Server             Server
}

// References is app's reference files: one for the server host admit found
// in front of the site, or, when it found none it knows, one for each of
// Caddy, nginx and Apache, so the owner has the right one either way.
func References(d deployment.Deployment, t Target, ws WebServer) []Reference {
	one := func(name string, s Server) Reference {
		w := ws
		w.Server = s
		return Reference{App: t.App, Path: ReferenceDir(d) + "/" + name, Content: FileContent(t, w), Server: s}
	}
	switch ws.Server {
	case ServerCaddy:
		return []Reference{one(t.App+".caddy", ServerCaddy)}
	case ServerNginx:
		return []Reference{one(t.App+".conf", ServerNginx)}
	case ServerApache:
		return []Reference{one(t.App+".conf", ServerApache)}
	}
	return []Reference{one(t.App+".caddy", ServerCaddy), one(t.App+".nginx.conf", ServerNginx), one(t.App+".apache.conf", ServerApache)}
}

// FileContent is a reference file for t: a header saying whose it is and
// what the owner does with it, then the snippet web-server show prints.
func FileContent(t Target, ws WebServer) string {
	header := fmt.Sprintf("# %s, the paisans app %s, written by paisans apply for the host's owner.\n# Copy it into %s's configuration. apply rewrites it when "+config.FileName+" changes, and says so: then replace your copy.\n", t.Hostname, t.App, ws.Server)
	switch ws.Server {
	case ServerCaddy:
		return header + caddySnippet(t, ws)
	case ServerNginx:
		return header + nginxSnippet(t, ws)
	case ServerApache:
		return header + apacheSnippet(t, ws)
	}
	return ""
}

// FromRecord is the web server host admit detected, as the admission record
// holds it, in the form Show and the check read. A record with none is an
// unknown server.
func FromRecord(r *admission.WebServer) WebServer {
	if r == nil {
		return WebServer{}
	}
	certs := map[string]Certs{"automatic": CertsAutomatic, "files": CertsFiles, "folder": CertsFolder}[r.Certs]
	return WebServer{
		Server: Server(r.Server), Container: r.Container, Process: r.Process,
		Config: r.Config, Main: r.Main, Certs: certs,
		CertFile: r.CertFile, KeyFile: r.KeyFile, Folder: r.Folder, Certbot: r.Certbot, Why: r.Why,
	}
}
```

Delete `include.go`, `compose.go` and their tests; run `go build ./...` and remove each remaining use of `Include`, `Sync`, `Remove`, `ValidateAndReload`, `CheckInclude`, `DeriveInclude`, `IncludeAt`, `IncludeLine`, `IncludeExample`, `RefusedWhy`, `DiagnoseMount`, `Outcome`, `Reload` and `FileName` in the steps below. `Detect` stays: `host admit` runs it as root. Rewrite the package comment: the package detects the owner's server (for admit), prints the sheet and the reference snippets, and checks an app from outside; it changes nothing of the owner's.

- [ ] **Step 4: apply's web server step**

In `cmd/paisans/converge_webserver_files.go`, `siteFile` becomes:

```go
// siteFile is what the reference step came to for one app.
type siteFile struct {
	// until is set when the step ended on a failure with instructions of
	// its own: what the app's check waits on.
	until string
	// ws is the owner's web server as admit recorded it; path the app's
	// reference file; changed whether this run rewrote it, so the owner's
	// copy is older than it.
	ws      webserver.WebServer
	path    string
	changed bool
}
```

and `writeWebServerFiles`, keeping its signature and its section heading, does per external monitor site in scope:

```go
		t := scope.recordHost(cfg, site)
		rec, err := admission.Read(t, d.Token())
		var recorded []render.ManifestWebServer
		if err == nil {
			recorded, err = apply.ReadWebServerRecord(t, d)
		}
		if err != nil {
			head()
			title := "reference files on " + site
			r.Step(title).Fail(err)
			r.Explain(errLine(err))
			out.failed = append(out.failed, title)
			for _, app := range apps {
				out.apps[app] = &siteFile{until: "its reference file is written"}
			}
			continue
		}
		ws := webserver.FromRecord(rec.WebServer)
		head()
		var keep []render.ManifestWebServer
		wanted := map[string]bool{}
		for _, app := range apps {
			target, err := webserver.For(cfg, app)
			if err != nil {
				out.add("reference file for "+app, ui.Failed)
				continue
			}
			sf := &siteFile{ws: ws}
			step := r.Step("reference file for " + app)
			if res, err := t.Run("install -d -m 755 " + shellQuote(webserver.ReferenceDir(d))); err != nil {
				step.Fail(fmt.Errorf("%w: %s", err, strings.TrimSpace(res)))
				out.failed = append(out.failed, "reference file for "+app)
				continue
			}
			for _, ref := range webserver.References(d, target, ws) {
				wanted[ref.Path] = true
				entry := render.ManifestWebServer{App: app, Path: ref.Path, SHA256: sha(ref.Content), Server: string(ref.Server), Container: ws.Container, Config: ws.Config}
				prev, had := recordedAt(recorded, ref.Path)
				if !had || prev.SHA256 != entry.SHA256 {
					if err := t.WriteFile(ref.Path, ref.Content, 0o644); err != nil {
						step.Fail(err)
						out.failed = append(out.failed, "reference file for "+app)
						break
					}
					sf.changed = had
				}
				sf.path = ref.Path
				keep = append(keep, entry)
			}
			step.Done(sf.path)
			out.apps[app] = sf
		}
		for _, e := range recorded {
			if !wanted[e.Path] {
				if _, err := t.Run("rm -f -- " + shellQuote(e.Path)); err != nil {
					out.failed = append(out.failed, "reference files on "+site)
				}
			}
		}
		if err := apply.WriteWebServerRecord(t, d, keep); err != nil {
			out.failed = append(out.failed, "reference files on "+site)
		}
```

with the helpers (in the same file):

```go
func recordedAt(list []render.ManifestWebServer, path string) (render.ManifestWebServer, bool) {
	for _, e := range list {
		if e.Path == path {
			return e, true
		}
	}
	return render.ManifestWebServer{}, false
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
```

Delete `writeWebServerFile`, `reportWebServerFiles`, `reloadAdvice`, `serverHost`, `webServerFilesHost` and `convergeWebServerFiles`'s comment's include wording. Rewrite `writeWebServerFiles`'s comment: it writes each external app's reference snippet into the deployment's root from the admission record, removes the ones no longer wanted, and records them in the manifest; nothing of the owner's is read or written.

In `cmd/paisans/converge_webserver.go`, `convergeWebServer` loses the `again` path and `reloadAgain` (delete the function), uses `sf.ws` where it called `findWebServer`, and its advice for a failed check is:

```go
		advice := fmt.Sprintf("The host's owner installs what paisans web-server show --app %s prints in their web server, then run paisans web-server check --app %s.", app, app)
		if sf != nil && sf.changed {
			advice = fmt.Sprintf("apply rewrote %s, so the host's owner replaces their copy with what paisans web-server show --app %s prints, then run paisans web-server check --app %s.", sf.path, app, app)
		}
```

`webServerResults` waits only when the check may still pass without the owner (`mayStillCome`); keep it for a passing app's certificate. In `webServerStep`, the warned titles are `"reference file for "`.

In `cmd/paisans/webserver.go`, `show` reads the record instead of detecting:

```go
		rec, err := admission.Read(siteTransport(target.Site, cfg.Sites[target.Site], ""), cfg.Deployment().Token())
		if err != nil {
			return admissionProblem(target.Site, err)
		}
		webserver.Show(ui.RedactWriter(os.Stdout), target, webserver.FromRecord(rec.WebServer))
```

and `check`'s failure note reads the same record. Delete `findWebServer`, `webServerHost` and `webServerHostVia` if nothing else uses them.

- [ ] **Step 5: app remove and site remove**

In `internal/appremove/execute.go`, `webServer` deletes each recorded reference file and names the owner's block:

```go
	for _, e := range s.WebServer {
		if out, err := h.Run("rm -f -- " + quote(e.Path)); err != nil {
			return fmt.Errorf("%s: removing %s: %w: %s", s.Site, e.Path, err, strings.TrimSpace(out))
		}
		ex.Kept = append(ex.Kept, fmt.Sprintf("%s: the host's owner removes the block for %s from %s (%s): apply wrote it as %s, which is gone", s.Site, hostnameOf(e.App), e.Server, e.Config, e.Path))
	}
```

(use the executor's field for kept lines and the app's hostname as the plan holds them; read the function and keep its record rewrite). `print.go`'s line says "remove the reference file %s; the host's owner removes the block from %s". In `internal/siteremove/host.go`, the step `remove site files` becomes `remove reference files` with the same deletion and the same kept line per entry, and the `webserver.Remove` loop in `runHost` goes.

- [ ] **Step 6: Run, CHANGELOG, full suite, commit**

Run: `go test ./internal/webserver/ ./internal/appremove/ ./internal/siteremove/ ./cmd/paisans/`
Expected: PASS.

Under `### Changed`:

```markdown
- Behind the owner's web server, `apply` writes each app's reference snippet to `/srv/paisans/<token>/web-server/` from what `host admit` detected, and warns until the owner's copy serves it; nothing of the owner's is written or reloaded.
```

Run: `go vet ./... && go test ./...`, then:

```bash
git add internal/webserver internal/appremove internal/siteremove cmd/paisans CHANGELOG.md
git commit -m "feat: hand the owner a reference snippet instead of including one" -m "The deployment's user cannot write, test or reload the owner's web server.
apply writes each app's snippet into the deployment's own root, filled from
the detection host admit recorded, checks the app from outside and warns
until the owner's copy serves it, saying when a rewrite means their copy
needs replacing. web-server show reads the same record, and app remove and
site remove name the block the owner removes.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 18: Each manifest records the sites pending

Ruling 32. Spec: "Each site's manifest records which sites were pending when its files were rendered, and the checks that compare a host with a render (an edited file, doctor's Leftovers) use that same view, so a file rendered without a pending site is not drift."

**Files:**
- Modify: `internal/render/write.go` (`Manifest.Pending`), `internal/apply/apply.go` (`Pending` option, `writeManifest`), `internal/hostcheck/inventory.go` (`ManifestPending`)
- Modify: `cmd/paisans/leftover.go`, `cmd/paisans/doctor.go` (`gatherLeftovers`)
- Test: `internal/apply/apply_test.go`, `internal/hostcheck/inventory_test.go`, `cmd/paisans/leftover_test.go`

**Interfaces:**
- Produces: `render.Manifest.Pending []string` (`json:"pending,omitempty"`); `func apply.Pending(sites ...string) Option`; `Inventory.ManifestPending []string`. Task 19 passes `apply.Pending(c.Pending()...)`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/apply/apply_test.go`, using the file's fake transport and plan helpers (read the test that asserts the manifest's written content, `grep -n 'Manifest()' internal/apply/apply_test.go`, and follow it):

```go
// The manifest records the sites pending when the files were rendered.
func TestTheManifestRecordsThePendingSites(t *testing.T)
```

asserting that a plan built with `apply.Pending("home-c", "home-b")` and executed writes a manifest whose JSON has `"pending": ["home-b", "home-c"]`, and one built without the option has no `pending` key.

Add to `internal/hostcheck/inventory_test.go`:

```go
func TestTheInventoryReadsTheManifestsPendingSites(t *testing.T) {
	h := cleanHost()
	h.files[fixtureDeployment.Manifest()] = `{"version":1,"files":[],"pending":["home-c"]}`
	inv, err := hostcheck.Inspect(h, fixtureDeployment)
	if err != nil || strings.Join(inv.ManifestPending, ",") != "home-c" {
		t.Errorf("pending %v, %v", inv.ManifestPending, err)
	}
}
```

(use the fake's file map's name). Add to `cmd/paisans/leftover_test.go`:

```go
// An app pinned to a site that was pending when this site's files were
// rendered is not reported as left over here.
func TestLeftoversUseTheManifestsView(t *testing.T)
```

built on the existing leftover test's setup, with `inv.ManifestPending` naming the site an app is pinned to, and asserting no leftover line names that app.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/apply/ ./internal/hostcheck/ ./cmd/paisans/ -run 'Pending|ManifestsView'`
Expected: FAIL.

- [ ] **Step 3: Implement**

In `internal/render/write.go`, `Manifest` gains:

```go
	// Pending is the sites that were pending when these files were
	// rendered, sorted: the effective topology left them out, so a check
	// comparing this site with a render uses the same view.
	Pending []string `json:"pending,omitempty"`
```

In `internal/apply/apply.go`, `options` gains `pending []string`, `Plan` gains `Pending []string` (set from it in `Build`), and:

```go
// Pending names the sites pending in this run, recorded in the manifest so
// that what compares this site with a render later renders the same view.
func Pending(sites ...string) Option {
	return func(o *options) { o.pending = slices.Sorted(slices.Values(sites)) }
}
```

`writeManifest` sets `Pending: plan.Pending`. In `internal/hostcheck/inventory.go`, `Inventory` gains `ManifestPending []string` and the manifest read sets it from `m.Pending`. In `cmd/paisans/leftover.go` and `gatherLeftovers` in `cmd/paisans/doctor.go`, classify against the manifest's view: `ownership.Classify(cfg.WithoutJoining(inv.ManifestPending...), site, inv, manifest)`.

- [ ] **Step 4: Run, full suite, commit**

Run: `go test ./internal/render/ ./internal/apply/ ./internal/hostcheck/ ./cmd/paisans/`, then `go vet ./... && go test ./...`.
Expected: PASS; the golden render trees do not change (the manifest is apply's, not render's output).

```bash
git add internal/render internal/apply internal/hostcheck cmd/paisans
git commit -m "feat: record in each manifest the sites pending at its render" -m "A run renders the declaration less its pending sites, and never publishes
that view. The manifest records which sites it left out, and doctor's and
apply's leftover reports classify a site against that same view, so a file
rendered without a pending site is not reported as drift.

Nothing passes pending sites yet; the run does next. No CHANGELOG bullet:
Task 19's covers the behaviour.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 19: The run on the sites it has

Rulings 30, 31. Spec, *A deployment with sites missing*: classification once per run, the effective topology in converge and rendering, the refusals, the `pending` plan section, the `Applied, degraded` ending and `--require-all`.

**Files:**
- Create: `cmd/paisans/sites.go`, `cmd/paisans/sites_test.go`
- Modify: `cmd/paisans/converge.go` (`runConverge`, `convergePlan`'s caller, `convergeOptions`), `cmd/paisans/main.go` (`applyCommand`, `siteApplied`, `dnsSetup`), `cmd/paisans/site.go` (`runSiteAdd`), `cmd/paisans/storage.go`, `cmd/paisans/converge_pull.go`, `cmd/paisans/help.go`
- Modify: `internal/doctor/reach.go` (`Losses` exported)
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: Task 17's `topology.Gather`, `Classify`, `Classification` and its methods, `DownLine`; Task 18's `apply.Pending`.
- Produces: `var runSites *topology.Classification`; `var classifyTransports = func(cfg *config.Config) map[string]apply.Transport`; `func classifySites(r ui.Reporter, cfg *config.Config) (*topology.Classification, error)`; `func effectiveConfig(r ui.Reporter, cfg *config.Config, site string) (*config.Config, *topology.Classification, error)`; `func showPending(r ui.Reporter, cfg *config.Config, c *topology.Classification)`; `func degradedResult(c *topology.Classification) string`; `func requireAll(c *topology.Classification, required bool) error`; `func doctor.Losses(cfg *config.Config, site string) string`; the flag `--require-all` on `apply` with and without `--site`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/paisans/sites_test.go`:

```go
package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/topology"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// classified runs f with the run's classification set to c, as a run that
// has classified does.
func classified(t *testing.T, c *topology.Classification) {
	t.Helper()
	saved := runSites
	runSites = c
	t.Cleanup(func() { runSites = saved })
}

func degraded(t *testing.T) *topology.Classification {
	t.Helper()
	cfg := fixtureConfig(t)
	c := &topology.Classification{Sites: map[string]topology.Site{}}
	for _, name := range cfg.SiteNames() {
		c.Sites[name] = topology.Site{Name: name, Class: topology.Ready}
	}
	c.Sites["home-b"] = topology.Site{Name: "home-b", Class: topology.Pending, Why: "home-b: its host has not admitted this deployment"}
	return c
}

// The plan opens with a pending section: a line per pending site with its
// reason and its request line, and what the deployment is without.
func TestThePendingSection(t *testing.T) {
	cfg := fixtureConfig(t)
	r := &ui.Recorder{Verbose_: true}
	showPending(r, cfg, degraded(t))
	out := r.Lines()
	for _, want := range []string{"pending", "home-b: its host has not admitted this deployment", "paisans host request --site home-b > home-b.admit", "without"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
}

// The degraded ending, and --require-all making it exit 1.
func TestTheDegradedEnding(t *testing.T) {
	c := degraded(t)
	if got := degradedResult(c); got != "Applied, degraded: 1 site pending or down (home-b)" {
		t.Errorf("got %q", got)
	}
	if err := requireAll(c, false); err != nil {
		t.Errorf("without --require-all: %v", err)
	}
	err := requireAll(c, true)
	var w *ui.Warning
	if err == nil || errors.As(err, &w) {
		t.Errorf("with --require-all a degraded run must fail, not warn: %v", err)
	}
}

// apply --site on a pending site is refused before anything is read.
func TestApplySiteRefusesAPendingSite(t *testing.T) {
	classified(t, degraded(t))
	err := runApply([]string{"--config", fixturePath(t), "--site", "home-b"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "--site home-b") {
		t.Errorf("got %v", err)
	}
}

// A run classifies once: a step reuses the run's classification instead of
// reading every site again.
func TestARunClassifiesOnce(t *testing.T) {
	cfg := fixtureConfig(t)
	reads := 0
	saved := classifyTransports
	classifyTransports = func(*config.Config) map[string]apply.Transport { reads++; return nil }
	t.Cleanup(func() { classifyTransports = saved })
	classified(t, degraded(t))
	if _, _, err := effectiveConfig(&ui.Recorder{}, cfg, ""); err != nil {
		t.Fatal(err)
	}
	if reads != 0 {
		t.Errorf("a step read the sites again (%d)", reads)
	}
}
```

(`fixtureConfig`, `fixturePath` are the package's test helpers; use their names as `cmd/paisans` defines them, and add the `config` import.)

Add to `cmd/paisans/converge_test.go`, built on its existing fakes for a whole run:

```go
// With one data site pending, the run plans no step for it, renders every
// other site without it, records it in each manifest, and ends degraded,
// exit 0; with --require-all, exit 1.
func TestConvergeRunsOnTheSitesItHas(t *testing.T)

// A down site keeps its place in the rendered mesh but none of its own
// steps run.
func TestConvergeSkipsADownSitesSteps(t *testing.T)
```

each setting `classifyTransports` to fakes that refuse the pending site's login (`apply.ErrLoginRefused`) and, for the down case, a deployment record on the gateway listing it, and asserting on the step titles the run executed (no `apply --site home-b`, no `host prepare --site home-b`, no `pull --site home-b`), on the written `wireguard` file of another site (no peer for the pending site; a peer for the down one), on the manifest's `pending`, and on the result line.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/paisans/ -run 'Pending|Degraded|RefusesAPending|ClassifiesOnce|SitesItHas|DownSites'`
Expected: FAIL, `undefined: showPending`.

- [ ] **Step 3: The run's classification**

Create `cmd/paisans/sites.go`:

```go
package main

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/doctor"
	"github.com/paisans-software/paisans-cli/internal/topology"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// runSites is the classification of the run in progress. The first command
// of a run that classifies sets it, and every step of the run after reads
// it: one view of who is in the mesh for the whole run, so no two sites'
// files disagree, and no step reads every site again.
var runSites *topology.Classification

// classifyTransports reaches every declared site to classify it. Tests
// replace it.
var classifyTransports = func(cfg *config.Config) map[string]apply.Transport {
	out := map[string]apply.Transport{}
	for _, name := range cfg.SiteNames() {
		out[name] = siteTransport(name, cfg.Sites[name], "")
	}
	return out
}

// classifySites is the run's classification, read once.
func classifySites(r ui.Reporter, cfg *config.Config) (*topology.Classification, error) {
	if runSites != nil {
		return runSites, nil
	}
	c, err := ui.Get(r, "classify the sites", func() (*topology.Classification, error) {
		return topology.Classify(cfg, topology.Gather(cfg, classifyTransports(cfg)))
	})
	if err != nil {
		return nil, err
	}
	runSites = c
	return c, nil
}

// effectiveConfig is what a step renders and plans from: the run's
// classification's effective topology, after its refusals, site being the
// one the step names, if any.
func effectiveConfig(r ui.Reporter, cfg *config.Config, site string) (*config.Config, *topology.Classification, error) {
	c, err := classifySites(r, cfg)
	if err != nil {
		return nil, nil, err
	}
	if err := c.Refuse(cfg, site); err != nil {
		return nil, nil, &ui.Problem{Hint: "the run cannot go on with the sites it has", Explain: err.Error(), Cause: err}
	}
	return c.Effective(cfg), c, nil
}

// showPending opens the plan: a line per pending or down site with its
// reason, a pending one's request line, and what the deployment is without,
// in doctor's words.
func showPending(r ui.Reporter, cfg *config.Config, c *topology.Classification) {
	if !c.Degraded() && len(c.Warnings) == 0 {
		return
	}
	r.Section("pending")
	for _, name := range c.Pending() {
		s := r.Step(name)
		s.Detail("%s", c.Sites[name].Why)
		s.Detail("paisans host request --site %s > %s.admit", name, name)
		if lost := doctor.Losses(cfg, name); lost != "" {
			s.Detail("While it is pending the deployment is without %s.", lost)
		}
		s.End(ui.Pending, "pending")
	}
	for _, name := range c.Down() {
		s := r.Step(name)
		s.Detail("%s", topology.DownLine(name))
		if lost := doctor.Losses(cfg, name); lost != "" {
			s.Detail("While it is down the deployment is without %s.", lost)
		}
		s.End(ui.Warned, "down")
	}
	for _, w := range c.Warnings {
		r.Warn(w, "")
	}
}

// degradedResult is how a degraded run ends.
func degradedResult(c *topology.Classification) string {
	names := append(c.Pending(), c.Down()...)
	n := len(names)
	noun := "sites"
	if n == 1 {
		noun = "site"
	}
	return fmt.Sprintf("Applied, degraded: %d %s pending or down (%s)", n, noun, strings.Join(names, ", "))
}

// requireAll is --require-all: a degraded run fails, for automation that
// must not pass on one.
func requireAll(c *topology.Classification, required bool) error {
	if !required || !c.Degraded() {
		return nil
	}
	return &ui.Problem{Hint: "the run is degraded and --require-all was given", Explain: degradedResult(c) + "."}
}
```

In `internal/doctor/reach.go`, rename `losses` to `Losses` with a doc comment saying apply's pending section words what a site's absence takes away the same way, and update its callers.

- [ ] **Step 4: Wire the whole run**

In `cmd/paisans/converge.go`:

- `convergeOptions` gains `RequireAll bool`, set from the new flag; `applyCommand` registers `requireAll := fs.Bool("require-all", false, "exit 1 when a site is pending or down")` and passes it on both paths.
- `runConverge`, right after the validate refusal and before the architecture section, saves and sets the run's classification and the effective topology, and opens the plan with the pending section:

```go
	savedSites := runSites
	defer func() { runSites = savedSites }()
	runSites = nil
	declared := cfg
	cfg, sites, err := effectiveConfig(r, declared, "")
	if err != nil {
		return err
	}
	showPending(r, declared, sites)
```

  Every read that iterates `cfg.SiteNames()` from there on reads only the sites the run steps on: the architecture read, the founded records, the gateway move, the review and the operators take `readySites(cfg, sites)`:

```go
// readySites is the sites of cfg, the effective topology, that the run
// steps on: every one but the down, whose own steps are skipped.
func readySites(cfg *config.Config, c *topology.Classification) []string {
	var out []string
	for _, name := range cfg.SiteNames() {
		if c.Ready(name) {
			out = append(out, name)
		}
	}
	return out
}
```

  (read each of `readArchitectures`, `convergeFounded`, `convergeGatewayMove`, `convergeReview` and `convergeOperators`, and pass the list where each takes `cfg.SiteNames()` or loops over `cfg.Sites`; a down etcd member's record is not read, and `convergeFounded` treats it as recorded, since only a joined site is down.)
- `convergePlan(cfg, st)` stays, and its result is filtered: `steps = slices.DeleteFunc(steps, func(s convergeStep) bool { site := stepSite(s); return site != "" && !sites.Ready(site) })`; when `sites.StorageHeld()`, the `storage init` or `storage add` step is removed with them.
- The run ends: where it prints `Deployment complete.`, print `degradedResult(sites) + "."` when `sites.Degraded()`, and return `requireAll(sites, o.RequireAll)` after printing; the dry run's `Nothing changed.` line is unchanged, and the pending section has said what will be left out.

- [ ] **Step 5: Wire the steps**

Each step command that renders or plans from the configuration takes the effective topology right after it has loaded and validated the declaration:

- `applyCommand` with `--site`: `cfg, sites, err = effectiveConfig(r, cfg, *site)`, so a pending or down `--site` is refused before any read, the render and the plan use the effective topology, and `apply.Pending(sites.Pending()...)` joins the apply options; `siteApplied`'s result, when `sites.Degraded()` and the command is not a step of the whole run, is `degradedResult(sites) + ". " + plural(written, "file") + " applied to " + site + "."`, and its error is `requireAll(sites, *requireAll)`.
- `runSiteAdd`, the `storage` commands, `pull` (`converge_pull.go`) and `dnsSetup` (`dns init`): `cfg, _, err = effectiveConfig(r, cfg, "")` after their validation, so a step of a run reuses the run's classification and a command run alone classifies on its own; `dns init` therefore publishes no record for a pending site.
- `host prepare` is left as it is: it prepares the one site it names, which must be admitted, and renders nothing.

In `cmd/paisans/help.go`, `apply`'s flags list `--require-all`: "exit 1 when the run is degraded: a site pending or down".

- [ ] **Step 6: Run, CHANGELOG, full suite, commit**

Run: `go test ./internal/doctor/ ./internal/topology/ ./cmd/paisans/`
Expected: PASS. Existing whole-run tests pass with every site ready: give their fakes the admission record each site needs (Task 9's `admittedRecord`) where `classifyTransports` reaches them.

Under `### Added`:

```markdown
- `apply` runs on the sites it has: a site not yet admitted is left out, a joined site that does not answer is kept, the plan opens with a `pending` section, and `--require-all` fails a degraded run.
```

Run: `go vet ./... && go test ./...`, then:

```bash
git add cmd/paisans internal/doctor CHANGELOG.md
git commit -m "feat: run apply on the sites it has" -m "Operators admit sites one host at a time, and a deployment that works on
fewer sites should not wait for all of them. Every run classifies every
site once and plans the effective topology: a pending site is left out as
if not yet added, a down one is kept as a peer and a member with its own
steps skipped. The plan opens with what is pending or down and what the
deployment is without, the run ends degraded with exit 0, and --require-all
makes that exit 1.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 21: The owner's web server, documented

Spec: "README, the guide `docs/guides/behind-your-own-web-server.md` and `docs/commands.md` rewritten to match." Documentation only; every passage describes what Tasks 15 and 16 built, in the present tense, with no em dash and no account of the include flow.

**Files:**
- Modify: `README.md` (*The monitor has a site of its own*: the `include_dir` line of the YAML example; the passages from "**The toolkit writes its own file, never the operator's configuration.**" through the `include_dir` derivation and its validate codes; the grant table's "web server" row; apply's web server section)
- Modify: `docs/guides/behind-your-own-web-server.md` (section 2's apply paragraph, section 3 in full, the acme_dns note's manual copying, section 6's waits, *Afterwards*)
- Modify: `docs/commands.md` (`apply` with and without `--site`, `site remove`, `app remove`, `web-server show and check`)

- [ ] **Step 1: README**

In *The monitor has a site of its own*, delete the `# include_dir:` line from the YAML example, and replace the passages on writing into the include directory, reloading, rolling back, the compose mount and `include_dir` with three paragraphs:

```markdown
**The toolkit renders, the owner installs.** On a site with `managed_by: external`, `apply` runs the app on `listen` and writes the snippet for the web server in front of it to `/srv/paisans/<token>/web-server/<app>.caddy` or `<app>.conf`, filled in from what `host admit` detected as root: the server, its configuration and how it obtains certificates. When admit found no server it knows, it writes one for each of Caddy, nginx and Apache. The deployment's user cannot write the owner's configuration, test it or reload the server, and nothing grants it that, so the owner copies the snippet in; `paisans web-server show --app <app>` prints it with the rest of the hand-off sheet.

**apply checks from outside and warns until the owner's copy serves.** After writing the snippet, `apply` runs `web-server check` for the app. Until the check passes, the web server step ends on a warning naming `paisans web-server show --app <app>` for the owner. When a change to `paisans-stack.yaml` rewrites the snippet, the warning says so, because the owner's copy is then older than it.

**Removing an app names the block to remove.** `app remove` and `site remove` delete the reference file, which is the deployment's, and say which block the owner removes from their web server. `validate` refuses `web_server.include_dir` (`web-server-include-dir`): nothing is written into the owner's configuration, so there is no directory to name.
```

Delete the grant table's row for the web server's include directory. In apply's web server section, say the step writes the reference snippets and checks each app.

- [ ] **Step 2: The guide**

In `docs/guides/behind-your-own-web-server.md`, rename section 3 to `## 3. Install the snippet` and replace its body with: where the reference file is (`/srv/paisans/<token>/web-server/`), that `paisans web-server show --app <app>` prints the same snippet, where it goes for Caddy (a site block, at the top level, after any global options block), nginx (a `server` block in a file under `conf.d` or `sites-enabled`) and Apache (a `VirtualHost`), that the owner validates and reloads their server themselves, and that apply's warning names a rewritten snippet. In section 2, the apply paragraph says apply writes the reference snippet and warns until the check passes. In the acme_dns note, drop "with `include_dir: false`". In *Afterwards*, a removed app's block is the owner's to remove, and apply names it. Keep every heading above and below section 3 as it is.

- [ ] **Step 3: commands.md**

In `docs/commands.md`: under `apply` without and with `--site`, the web server bullet says apply writes each external app's reference snippet under `/srv/paisans/<token>/web-server/` and checks it, warning until it serves; under `site remove` and `app remove`, the reference file is deleted and the owner's block is named; under `web-server show and check`, `show` prints the sheet from the admission record, run as the deployment's user.

- [ ] **Step 4: Verify and commit**

Run: `git diff README.md docs/guides/behind-your-own-web-server.md docs/commands.md | perl -CS -ne 'print if /\x{2014}/'` (expect no output), `grep -n 'include_dir\|reload' docs/guides/behind-your-own-web-server.md` (expect only the owner's own reload), and `git diff` the whole change and read it. Every command line in the diff uses flags `cmd/paisans` defines.

```bash
git add README.md docs/guides/behind-your-own-web-server.md docs/commands.md
git commit -m "docs: describe the reference snippet the owner installs" -m "Behind the owner's web server, apply writes a reference snippet into the
deployment's root and the owner installs it. The README, the guide and the
command reference describe that, and what apply's warning and the removals
say.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

No CHANGELOG bullet: Task 16's covers it.

---

### Task 22: The paths and permissions, documented

The index's last Phase 6 bullet: "README, each passage that names a root-owned path or sudo for a deployment command, rewritten to the paths under `deployment.Root()` and the user's own permissions." Documentation only.

**Files:**
- Modify: `README.md`, `docs/commands.md`, `cmd/paisans/help.go` (any help text still naming a moved path)

- [ ] **Step 1: Find every passage**

Run: `grep -n '/var/lib/paisans\|/etc/paisans\|/srv/caddy.d\|/usr/local/lib/paisans\|sudo\|as root\|registry' README.md docs/commands.md cmd/paisans/help.go`
Each hit about a deployment command is rewritten; hits about `host admit`, `host evict`, `host deployments` and the WireGuard helper's sudoers line stay.

- [ ] **Step 2: Rewrite the passages the index names**

In `README.md`:

- The uptime monitor's seed (*It starts as root and drops*): `apply` writes the seed as the deployment's user, 0600, because it carries the SMTP password; the data directory is a bind mount the user's rootless Docker creates; the fork's entrypoint, root of the container's user namespace, hands both to `node` and drops to it.
- The deployed record: it is `/srv/paisans/<token>/.paisans/deployed.json` on every gateway, the deployment user's, written under the deployment's own lock and read as that user; the "read through sudo instead" fallback is gone from the text.
- The admission record, beside it: `/etc/paisans/admitted.<token>.json`, root's and 0644, written by `host admit`, which the deployment's user reads without sudo to learn what it was granted, and which every command that changes a host checks first; `host deployments --sudo=false` lists the records without the registry or the directories.
- The configuration store: the repository and its files are in `/srv/paisans/<token>/.paisans/config/`, its daemon, sync and timer are the user's own units under linger, and gathering reaches every site as the deployment's user (both "with `sudo` because the store is root's" and gathering "with `sudo`" go).
- The host check's inventory: it runs as the deployment's user, reads listeners with `ss -Hltnue`, counts its own UID and subordinate range as this deployment's, root's as the base system and any other user's as taken; the firewall is `host admit`'s.
- doctor's Leftovers row: it reads the inventory as the deployment's user and classifies against the manifest's view, which names the sites pending at its render; the owner's site files are in `/srv/paisans/<token>/caddy.d`.
- `/srv/caddy.d` everywhere: `/srv/paisans/<token>/caddy.d`, the deployment's, created by `apply`.
- The registry passages (claim through sudo): the registry is root's and `host admit` claims it; a deployment's commands check its admission record instead.
- `host prepare`: it installs nothing; it checks the admission record against the configuration and keeps the user's keys, configuration store, units and `daemon.json`.
- Site remove's table and the `--host-gone` text: the user side, and the `host evict` line for the owner.
- *A deployment with sites missing*, a new subsection under `apply`: the classification table, the pending section, the degraded ending and `--require-all`, as Task 19 built them, with the reason each holds (from the spec's section).

In `docs/commands.md`: `host prepare` (installs nothing; its checks; no package or firewall bullet), `host deployments` (lists admission records; `--sudo=false` lists them alone), `apply` (`--require-all`; the `pending` section; the degraded ending; the self-check on the gateway), `site remove` (the evict line), `prune` (sizes from Docker).

- [ ] **Step 3: Verify and commit**

Run: `git diff README.md docs/commands.md cmd/paisans/help.go | perl -CS -ne 'print if /\x{2014}/'` (expect no output); `grep -n '/var/lib/paisans/deployed\|/var/lib/paisans/config\|/srv/caddy.d\|/usr/local/lib/paisans' README.md docs/commands.md cmd/paisans/help.go` (expect no output); `go test ./cmd/paisans/ -run Help`; read the whole diff.

```bash
git add README.md docs/commands.md cmd/paisans/help.go
git commit -m "docs: describe deployment commands as the deployment's user" -m "Every command but host admit, evict and deployments runs as the
deployment's own user, under /srv/paisans/<token>/ and its home. The
README and the command reference describe the paths and permissions it
uses, the admission record beside the deployment record, and a run with
sites pending or down.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

No CHANGELOG bullet: each code task added its own.

---

## Final review

- [ ] Run `go vet ./... && go test ./...` on the integrated branch and report the result.
- [ ] Run, where a rootless daemon is at hand, `go test -tags garage_integration ./internal/garage/ ./internal/storageadd/` and the other tags in `docs/development.md`, and report which ran.
- [ ] `git log --format=%B develop.. | perl -CS -ne 'print if /\x{2014}/'` prints nothing; every commit's subject is `<type>: <lowercase subject>` and every body ends with the trailer.
- [ ] `grep -rn '/var/lib/paisans\|/srv/caddy.d\|/usr/local/lib/paisans' --include=*.go internal cmd | grep -v '_test.go' | grep -v 'internal/hostadmit\|internal/registry'` prints nothing: no deployment command reaches a root path.
- [ ] A fresh reviewer reads the whole branch diff against the spec's *What runs as the deployment's user*, *Behind the owner's web server* and *A deployment with sites missing*, and the five Review Focus tests; every finding is fixed before merge, Minors batched here.
