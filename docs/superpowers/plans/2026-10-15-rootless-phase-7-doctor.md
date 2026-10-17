# Rootless Phase 7: Doctor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

## Revised against develop 6783478a

This plan was first written before phase 6 existed and named phase 6's interfaces as phase 6's plan predicted them. It is revised against `develop` at `6783478a` (phase 6 merged as PR #370). What changed, and why:

* **Task 0 (reconcile) is gone.** This revision is that reconciliation; every name below exists on `develop` or is produced by a task here.
* **The classification is `internal/topology`**, not an assumed `sitestate`: `topology.Gather(cfg, transports) Evidence` and `topology.Classify(cfg, ev) (*Classification, error)`, with `Classification.Sites map[string]topology.Site` (`Name`, `Class`, `Why`, `Request`), the classes `topology.Ready`, `topology.Pending`, `topology.Down` (zero is unclassified), `topology.DownLine(site)`, and a refusal (an error) where a site's class cannot be told safely: unread evidence (`Source{Name, Sites}`), or a joined site whose admission no longer covers it. doctor keeps topology's types instead of mapping them to its own. `Classification.Wait` holds a join while a joined site is down; that is `apply`'s, and doctor reports the down site that causes it.
* **The record comparison exists.** `admission.Record.Differences(cfg *config.Config, site string) []string` and `admission.ForSite(t, cfg, site)` landed in phase 6, and compare the site name, user, SSH and WireGuard ports, `mesh_subnet`, forwarding, the grants, and the owner's web server. The old Task 3 steps that wrote `Differences(config.Site)` and moved `host prepare`'s comparison are dropped; the admission judge calls the merged one.
* **A site whose admission does not cover it is pending only when the deployment has not joined it** (topology's table). The old ruling 6 made a differing record a `FAIL` and a missing one a `WARN`; the admission judge now asks the classification whenever the record does not cover the site, so doctor and `apply` agree: pending is a `WARN` (`FAIL` on a gateway), and a joined site the record no longer covers is a `FAIL` with the classification's own refusal.
* **The gateway's own answer is `internal/selfcheck`**: `selfcheck.Check(ctx, Probe, Target) error`, `selfcheck.TargetFor(cfg, site) (Target, bool)`, `*selfcheck.Conflict{Port, Site, Detail}`, `selfcheck.Path`, `selfcheck.DefaultProbe`. It already compares the trimmed body with the token exactly, so the echo case is a pinning test row, not a fix. `TargetFor` knows only gateways; founder decision 2 also asks each monitor running the toolkit's Caddy, so Task 5 extends it. The node is `self-check <site>`, since it is no longer only the gateway's.
* **`doctor.Losses`** is exported (phase 6), and `apply`'s pending section words absence with it.
* **host admit's sudo reading is reused, not copied.** Phase 5/6 merged `readSudo` and `sudoRules` in `internal/hostadmit`, which also refuse a Defaults entry that hands the helper an environment (`setenv`, `env_keep`, ...) and any runas or command-specific Defaults line. The old Task 2 wrote a second parser that knew none of that; Task 1 now exports admit's (`ReadSudoList`, `SudoList.BeyondHelper`, `HelperRule`) and Task 3 judges with it, so admit and doctor refuse the same things.
* **Boot ordering is checked, per founder decision 3.** host admit writes `/etc/systemd/system/user@<uid>.service.d/paisans-mesh.conf` (mode 644) to start the user's manager after the mesh. The linger check reads whether it is there (Task 1 exports its path as `hostadmit.MeshDropInPath`), and the container advice for `cannot assign requested address` names the container only, pointing at the linger check for the ordering.
* **`hostadmit.PrivilegedGroups` grew** (disk, shadow, kmem, libvirt, incus-admin); doctor reads the list, so nothing here changes for it.
* **Already delivered by phase 6, so not in this plan:** doctor reads the user's own `daemon.json` (`mesh.ProbeUser` in `doctorReads.Mesh`), the host check runs as the user (`ss -Hltnue`, UIDs), Leftovers classifies against the manifest's pending list, every command carries the user's `DOCKER_HOST` (an `id -u` login probe that refuses uid 0), `apply.NoLogin` / `apply.ErrLoginRefused` tell a refused login from an unreachable host, `host admit --enable-ufw` (`ufw_enabled` in the record), the egress table, the WireGuard helper, and SMTP set and rotate from the workstation. None of them needs a doctor change here.
* **Test harness names that exist:** `cmd/paisans` already has `admittedRecord(t, cfg, site) string` and `admittedPath(cfg)` (`admitted_test.go`), so the command tests reuse them; `readOnly` no longer lists the registry read.
* **Tasks were split for parallel worktrees** (see *Task order and parallel groups*).

---

**Goal:** `paisans doctor` checks that each site is still what its owner's `host admit` made it (the admission record covers the site, the deployment's user holds no privileged group, its own rootless Docker answers at Engine 29.5 or later, linger is on and its manager starts after the mesh, and sudo lets it run the WireGuard helper and nothing else), reports a site pending admission or down as a `WARN` naming what the deployment is without (a `FAIL` for a gateway), and fetches `/.well-known/paisans/<token>` from outside from the gateway and every monitor running the toolkit's Caddy, with the code `apply` runs; every one a node of the check graph.

**Architecture:** Each new check is a pure judge in `internal/doctor` (`rootless.go`, `sudo.go`, `admission.go`, `selfcheck.go`) over a probe a new `Reads` method fills, and a node in `doctor.Checks`. The admission node runs first on every site that answered and is a hard need of everything else asked of that site, so a pending site is one `WARN` line and nothing more; the `docker <site>` node is a hard need of every check that runs `docker` there, so a daemon that does not answer is one `FAIL` and no cascade. Pending and down come from `topology`'s classification, asked once per run and only when a site does not answer or its record does not cover it, through one package variable in `cmd/paisans`. The self-check is `internal/selfcheck`, called from the workstation through another.

**Tech Stack:** Go 1.25, the `internal/doctor` check graph (`Graph`, `Node`, `Need`, `Unless`), `internal/admission`, `internal/hostadmit`, `internal/hostcheck`, `internal/topology`, `internal/selfcheck`, `apply.WGHelper`, the `cmd/paisans` doctor test harness (`doctorHost`, `withDoctorHosts`, `readOnly`, `stuckHosts`, `admittedRecord`, `admittedPath`).

**Spec:** `docs/specs/2026-10-15-rootless-and-host-admit.md` (*The deployment's user*, *The WireGuard helper*, *What runs as the deployment's user*: "`paisans doctor` loses the sudo finding and the allowlist check, and gains [...] the gateway and every monitor running the toolkit's Caddy answer their own `/.well-known/paisans/<token>` from outside", *The gateway answers as itself*, *A deployment with sites missing*), `docs/specs/2026-10-15-doctor-check-graph.md` (*The check graph*, *Running the graph*, *Reporting each check as it ends*), and the phase index `docs/superpowers/plans/2026-10-15-rootless-and-host-admit.md`, *Phase 7: Doctor*.

## Global Constraints

* "the user is not privileged, the rootless socket answers, Engine 29.5 or later, linger on, `sudo -n -l` lists only the helper, the admission record matches the declaration. Each in the check graph [...], with the sudo finding removed, and a gateway check that fetches `/.well-known/paisans/<token>` from outside as `apply` does, sharing its code, and a `WARN` for each pending or down site listing what the deployment is without." (index, *Phase 7*)
* "`paisans doctor` checks the same list on every run." (spec, *The deployment's user*) The list is `hostadmit.PrivilegedGroups`, the Engine floor `hostadmit.MinRootlessEngine`, the sudo reading `hostadmit.ReadSudoList` and `SudoList.BeyondHelper`, the drop-in path `hostadmit.MeshDropInPath`; doctor never keeps a copy of any.
* "`doctor` reports each pending or down site as a `WARN` with the same list, and the gateway as a `FAIL`, since the run refuses without it." (spec, *A deployment with sites missing*) "Whether a site has joined is read from the deployment, never from the site's own host." (same) doctor asks `topology`, never its own reading.
* "A failed node's dependents are pruned, transitively, and get no lines of their own. The failed line says once what was not checked because of it." (doctor spec) "A **hard need** must have passed for the node to run." (same)
* Doctor only reads: the command-level test's allow list (`readOnly` in `cmd/paisans/doctor_test.go`) gains each new command and the record read, and refuses any other.
* "`doctor.Input` stays the record of every probe [...] running every node's judge over a complete `Input` gives the same findings the graph reported." (doctor spec) `Diagnose` calls every new judge, in the graph's order.
* No em dashes anywhere: code, comments, commit messages. Code comments describe what is, never what was.
* Commits: `<type>: <lowercase subject>`, a body saying why, `Founder decision.` where it was one, the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. A behaviour-changing commit adds one `CHANGELOG.md` bullet under `[Unreleased]`, one sentence, at most 35 words; a commit with nothing an operator would notice adds none and says so in its body.
* User documentation is phase 9's (*Rulings*, 9): this phase changes `CHANGELOG.md` and code only.
* Never `git stash`; other sessions share the stash. Never push.
* Implementers run the touched packages during each task and the full suite once, at the end (*Final checks*).

## Review Focus

The index names no Review Focus line for this phase. The five below are what the spec implies and no line of it tests, most likely first; each is pinned by a test in the task named.

* **sudo's answer in another language, or in a shape the parse does not know.** On a host whose `LANG` is not English, `sudo -l` translates its headers; a parse that found no header could read "nothing listed" as "nothing but the helper". The command runs in the C locale, and any answer the parse does not recognise is a `WARN` that it could not be read, never an `ok` (Task 3: `TestSudoRights`, rows `unrecognised` and `translated`; Task 1: `TestReadSudoList`).
* **sudo allowing the helper and something more, on one line, through a group, or through a Defaults entry.** `(root) NOPASSWD: <helper>, /bin/sh`, a `%group` rule's `(ALL : ALL) ALL`, the helper with `(ALL)` as the run-as user, another deployment's helper, `setenv` in the matching Defaults, or a `Defaults!<helper>` line: each must `FAIL` naming exactly what reaches past the helper (Task 3: the rows of `TestSudoRights`).
* **A site that answers ssh but has not admitted the deployment.** `ssh.user` names an existing account the owner never ran `host admit` for: the login works, there is no record. It must be one `WARN` with the `host request` line, and nothing else asked of that host, rather than a `FAIL` per check (Task 6: `TestAPendingSiteIsCheckedNoFurther`).
* **A gateway that is pending or down.** The deployment cannot serve without it and `apply` refuses to run (`Classification.Refuse`), so the line must stay a `FAIL` and doctor must exit 1, while a pending or down data or apps site is a `WARN` and doctor exits 0 for it (Task 4: `TestAdmission`, row `gateway without the ports`; Task 7: `TestAbsentSites`, rows `gateway down` and `gateway pending`).
* **A server on 443 that echoes the request.** A service that answers any path with the path itself puts the token in its body; a check that looks for the token anywhere in the body would pass it. The check must accept the token as the whole body and nothing else (Task 5: the `an echo of the path` row of `TestSelfCheckRefusesAnotherServer`).

## Rulings

Where the spec, the index or the code leaves a choice open, this plan decides as follows. Each is restated where its task implements it.

1. **The sudo finding is gone already.** Phase 2 removed it, and phase 1 removed the allowlist check. This phase removes nothing; *Final checks* confirms with a grep, and the read-only allow list keeps any sudo command but `SudoCommand` out.
2. **The new checks are part of `doctor.Reads`, not an optional interface** as `DNSReads` is. Every deployment is rootless, so every doctor run asks them.
3. **Node order and needs.** After `reach <site>`: `admission <site>` (hard: reach); then `user <site>`, `docker <site>`, `docker engine <site>` (hard: `docker <site>`, no read of its own), `linger <site>` and `sudo rights <site>` (hard: admission). Every existing node that reads one site needs `admission <site>` in place of `reach <site>` (`mesh`, `clocks`, `dns token <site>`); `patroni <site>`, `containers`, `settings`, `leftovers` and `caddy certificates` need `docker <site>`, since each runs `docker` there. `etcd`, `etcd version`, the `patroni` aggregate and `pocket-id` keep their any-of needs on `reach`, since their gathers try each member in turn and record why one failed. `self-check <site>` has no needs and runs after `leftovers`, before the DNS nodes.
4. **Shared, never copied:** the Engine floor is `hostadmit.MinRootlessEngine`, compared with `hostcheck.AtLeast`; the privileged groups are `hostadmit.PrivilegedGroups`; what sudo may allow is `hostadmit.SudoList.BeyondHelper` over `hostadmit.ReadSudoList`; the boot-order drop-in is `hostadmit.MeshDropInPath`. `internal/doctor` imports `internal/hostadmit` for these names only; `hostadmit` does not import `doctor`, so there is no cycle.
5. **What sudo may allow is exactly what host admit accepts**: the one rule `hostadmit.HelperRule(helper)`, `(root) NOPASSWD: <helper>`, no Defaults entry `sudoDefaultProblem` names, and no runas or command-specific Defaults line. Anything else is a `FAIL` naming it. `sudo -n -l` runs as `LC_ALL=C sudo -n -l`, and an answer the parse does not know is a `WARN`.
6. **The record covers the site when `admission.Read` found it and `Record.Differences(cfg, site)` is empty.** When it does not (no record, one that cannot be read, or any difference, grants included), the read asks the classification. A site `topology` classed pending is a `WARN` (a `FAIL` on a gateway, ruling 8) whose node's outcome is `Failed`, so nothing more is asked of that site. Any other uncovered site (joined, or a classification that refused) is a `FAIL` whose outcome is `Passed`, so the rest of the site is still checked, and its finding carries the classification's refusal.
7. **Grants are among `Differences`.** The merged `Differences` names a missing grant ("the site's roles need --watchdog, which the host's owner did not grant"); the judge adds what each grant gives, from `admission.Needs(site).Missing(record.Grants)`.
8. **A gateway pending or down is a `FAIL`.** The spec makes each pending or down site a `WARN`, and has the run refuse while the gateway is pending or down; doctor's exit status follows the run. Every other site is a `WARN`. Founder decision.
9. **User documentation is phase 9's.** Phase 9 runs alongside this phase and documents doctor's checks as they land; this phase changes no `README.md`, `docs/commands.md` or `docs/development.md` text, and *What this phase hands to phase 9* lists every name and wording.
10. **The classification is asked once per run, and only when a site does not answer or its record does not cover it.** A healthy run never pays for it. It gets a transport for every declared site, not only those `--site` names, since it reads the gateways' deployment record and the cluster's members. A refusal is kept as text (`Unclassified`) and reported, never a crash.
11. **The self-check node runs on the workstation, one per asked site that runs the toolkit's Caddy (`config.Site.RunsCaddy`), with no needs**, so a site whose ssh does not answer is still asked. It asks once (`Probe.Wait` 0): doctor does not wait for a certificate as `apply` does after starting Caddy. A site with no `public_address` or no hostname is a `WARN` "not asked from outside", as `apply`'s step warns.
12. **A container that failed with `cannot assign requested address`** is advised to be started again or its stack recreated, as the deployment's user. Whether its manager is ordered after the mesh at boot is the linger node's to say (founder decision 3), and the advice points there.

## Founder decisions

1. **A pending or down gateway is a `FAIL`**; every other pending or down site is a `WARN` (ruling 8 stands).
2. **Doctor fetches `/.well-known/paisans/<token>` from every site that runs the toolkit's Caddy**: the gateway and each monitor with `web_server.managed_by: paisans`. Task 5 extends `selfcheck.TargetFor` to the monitor; a monitor behind the owner's web server is not fetched.
3. **`host admit` orders the user's manager after the mesh** (its drop-in, `hostadmit.MeshDropInPath`). The advice names a reboot ordering problem only when the drop-in is missing: the linger node fails then (Task 2), and the container advice (Task 9) names the container.

---

## What this phase hands to phase 9

Phase 9's last task documents doctor's checks from this phase's merged code. It describes, in `README.md`'s checks table and `docs/commands.md`'s `paisans doctor` entry:

* The nodes, in the graph's order: `reach <site>`, `admission <site>`, `user <site>`, `docker <site>`, `docker engine <site>`, `linger <site>`, `sudo rights <site>`, then the existing ones, then `self-check <site>` after `leftovers <site>` and before the DNS nodes.
* The commands: `doctor.UserCommand`, `doctor.DockerCommand`, `doctor.LingerCommand` (linger, and the drop-in at `/etc/systemd/system/user@<uid>.service.d/paisans-mesh.conf`), `doctor.SudoCommand` (`LC_ALL=C sudo -n -l`), and the record read from `admission.Path(token)` as the login user.
* What fails and what warns: a privileged group, a group of GID 0 (read with `id -G`, so a GID-0 group under another name counts) or uid 0; a daemon that does not answer or is not rootless; an Engine below 29.5; linger off; the manager not ordered after the mesh; sudo allowing anything past the helper, or not the helper; a record that does not cover a joined site (each a `FAIL`). An answer doctor cannot read (the user's groups, the Docker daemon's, linger's or the mesh-order line, or sudo's, such as a translated one) is a `WARN` naming what it could not read. A site not admitted, admitted without a grant or with a record that differs, while the deployment has not joined it, or down: a `WARN`, a `FAIL` on a gateway, with what the deployment is without and the `host request` or `site remove --host-gone` line. A Caddy site answering as something else, or not answering on 443 or 80, from the workstation: a `FAIL`, naming the port and URL that got no answer; one with no `public_address` or no hostname of its own: a `WARN` "not asked from outside".
* That pending skips the rest of that site's checks, and a daemon that does not answer skips every check that runs `docker` there. A pending Caddy site's self-check is a `WARN` that it was not asked, since apply has not deployed its Caddy; a down site's self-check is still asked, and its failure is a `WARN` (a `FAIL` on a gateway) pointing at reach.
* Checks that read across sites (etcd, Patroni's `/sync` and logs, Pocket ID's instances) ask only sites whose admission passed and that the run's classification placed neither pending nor down. A site they did not ask is listed with "not asked: its admission did not pass (see admission)" or "not asked: the run's classification found it pending or down, as apply would". Patroni says nothing more of a pending data site, and names reach, admission or docker when one of those is why a member is missing.
* A site the classification places pending through a relay (it answers, and its record covers it, but its every route into the mesh is through a pending site) keeps its own single-site checks; only the checks across sites leave it out.
* Exit status: `FAIL` alone makes doctor exit 1; a run with only warnings, a pending or down data site among them, exits 0.
* Two passages of `README.md` describe the behaviour before this phase and are phase 9's to correct: the line in *A deployment with sites missing* that says "`doctor` reports a site that does not answer as a `FAIL` in its Reach row", and the Reach row of doctor's checks table, whose *FAIL when* column says the same. Both become: a down or pending site is a `WARN`, a `FAIL` on a gateway.
* The container advice for `cannot assign requested address`.

---

## File Structure

| File | Change | Responsibility | Task |
|---|---|---|---|
| `internal/hostadmit/sudolist.go`, `sudolist_test.go` | create | `SudoList`, `ReadSudoList`, `HelperRule`, `(SudoList).BeyondHelper`, `splitUnescaped` (moved) | 1 |
| `internal/hostadmit/facts.go`, `user.go`, `evict.go`, `user_internal_test.go`, `probe_integration_test.go` | modify | `readSudo` gone, `sudoRules` over `BeyondHelper`, `meshDropInPath` renamed `MeshDropInPath` | 1 |
| `internal/doctor/rootless.go`, `rootless_test.go` | create | `UserCommand`, `DockerCommand`, `LingerCommand`; the probes; `User`, `DockerSocket`, `DockerEngine`, `Linger` | 2 |
| `internal/doctor/sudo.go`, `sudo_test.go` | create | `SudoCommand`, `SudoProbe`, `SudoRights` | 3 |
| `internal/doctor/admission.go`, `admission_test.go` | create | `SectionAdmission`, `AdmissionProbe`, `Admission`, `pending`, `absentLevel`, `requestAdvice` | 4 |
| `internal/selfcheck/selfcheck.go`, `selfcheck_test.go`, `target_test.go` | modify, modify, create | `TargetFor` for a monitor running Caddy; the echo row | 5 |
| `internal/doctor/selfcheck.go`, `selfcheck_test.go` | create | `SectionSelfCheck`, `SelfCheckProbe`, `SelfChecks` | 5 |
| `internal/doctor/checks.go`, `doctor.go`, `graph.go`, `dnschecks.go`, `graph_test.go` | modify | the `Reads` methods; nodes and needs; `Input`, `sections`, `Diagnose`, `clone` | 6, 7, 8 |
| `cmd/paisans/doctor.go`, `doctor_test.go`, `doctor_dns_test.go`, `output_test.go` | modify | the reads, the classification, the self-check; the harness | 6, 7, 8 |
| `internal/doctor/reach.go`, `reach_test.go` | modify, create | pending and down in `Reach` | 7 |
| `internal/doctor/containers.go`, `doctor_test.go` | modify | the boot advice | 9 |
| `CHANGELOG.md` | modify | four bullets | 6, 7, 8, 9 |

## Task order and parallel groups

Each group's tasks touch no file in common and may run in parallel worktrees, integrated one at a time; a later group starts once every task of the one before has merged into the phase branch.

| Group | Tasks | Why together |
|---|---|---|
| A | 1, 4, 5, 9 | Task 1 is `internal/hostadmit` alone; 4 and 5 create new files in `internal/doctor` and `internal/selfcheck`; 9 is `containers.go` and `internal/doctor/doctor_test.go`. Only 9 adds a changelog bullet. |
| B | 2, 3 | Both need Task 1's exports; each creates its own two files in `internal/doctor`. |
| then, in order | 6, 7, 8 | Each changes `checks.go`, `doctor.go`, `cmd/paisans/doctor.go` and the command test harness. |

Test helpers are named apart so the parallel tasks' test files compile together: `rootlessDocker` (Task 2), `sudoList`, `helperEntry` (Task 3), `admittedRecord` (Task 4), `ourCaddy` (Task 5).

---

### Task 1: Share host admit's sudo reading and its drop-in path

**Files:**
- Create: `internal/hostadmit/sudolist.go`, `internal/hostadmit/sudolist_test.go`
- Modify: `internal/hostadmit/facts.go`, `internal/hostadmit/user.go`, `internal/hostadmit/evict.go`
- Modify: `internal/hostadmit/user_internal_test.go`, `internal/hostadmit/probe_integration_test.go`

**Interfaces:**
- Consumes: `readSudo`, `splitUnescaped`, `meshDropInPath` (`facts.go`), `sudoRules`, `sudoDefaultProblem` (`user.go`), the fixture `testdata/sudo/setenv-helper.txt`.
- Produces: `type SudoList struct{ Rules, Defaults, Specific []string }`; `func ReadSudoList(lines []string) (SudoList, bool)`; `func HelperRule(helper string) string`; `func (l SudoList) BeyondHelper(helper string) (rules, defaults []string)`; `func MeshDropInPath(uid string) string`.

- [ ] **Step 1: Write the failing test**

Create `internal/hostadmit/sudolist_test.go`:

```go
package hostadmit_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

const helper = "/usr/local/libexec/paisans/wg-psns-f2a9"

// ReadSudoList reads sudo -l as Ubuntu 24.04 prints it, as root for a user
// or as the user itself: its Defaults split into entries, each rule, each
// specific line; a refusal is a list of nothing; anything else is unread.
func TestReadSudoList(t *testing.T) {
	b, err := os.ReadFile("testdata/sudo/setenv-helper.txt")
	if err != nil {
		t.Fatal(err)
	}
	l, ok := hostadmit.ReadSudoList(strings.Split(strings.TrimRight(string(b), "\n"), "\n"))
	if !ok ||
		!slices.Equal(l.Defaults, []string{"env_reset", "mail_badpass", `secure_path=/usr/local/sbin\:/usr/local/bin\:/usr/sbin\:/usr/bin\:/sbin\:/bin\:/snap/bin`, "use_pty"}) ||
		!slices.Equal(l.Rules, []string{hostadmit.HelperRule(helper)}) ||
		!slices.Equal(l.Specific, []string{"Defaults!" + helper + " setenv"}) {
		t.Errorf("%+v, %v", l, ok)
	}
	l, ok = hostadmit.ReadSudoList([]string{"Matching Defaults entries for paisans-f2a9 on host:", "    env_reset, mail_badpass,", `    secure_path=/usr/bin\:/bin,`, "    use_pty", "",
		"User paisans-f2a9 may run the following commands on host:", "    " + hostadmit.HelperRule(helper)})
	if !ok || !slices.Equal(l.Defaults, []string{"env_reset", "mail_badpass", `secure_path=/usr/bin\:/bin`, "use_pty"}) {
		t.Errorf("wrapped Defaults: %+v", l)
	}
	for _, refused := range []string{"User paisans-f2a9 is not allowed to run sudo on host.", "Sorry, user paisans-f2a9 may not run sudo on host."} {
		if l, ok := hostadmit.ReadSudoList([]string{refused}); !ok || len(l.Rules) != 0 {
			t.Errorf("%q: %+v, %v", refused, l, ok)
		}
	}
	for _, lines := range [][]string{
		{""},
		{"something else"},
		{"Matching Defaults entries for paisans-f2a9 on host:", "    env_reset", "Something sudo never printed"},
		{"Benutzer paisans-f2a9 darf die folgenden Befehle auf host ausführen:", "    " + hostadmit.HelperRule(helper)},
	} {
		if l, ok := hostadmit.ReadSudoList(lines); ok {
			t.Errorf("%q read as %+v", lines, l)
		}
	}
}

// BeyondHelper is every rule but the helper's, every Defaults entry that
// hands the helper the user's environment or path, and every specific line.
func TestBeyondHelper(t *testing.T) {
	l := hostadmit.SudoList{
		Rules:    []string{hostadmit.HelperRule(helper), "(ALL : ALL) ALL", "(root) NOPASSWD: " + helper + ", /bin/sh"},
		Defaults: []string{"env_reset", "use_pty", "setenv", "!env_reset"},
		Specific: []string{"Defaults!" + helper + " setenv"},
	}
	rules, defaults := l.BeyondHelper(helper)
	if !slices.Equal(rules, []string{"(ALL : ALL) ALL", "(root) NOPASSWD: " + helper + ", /bin/sh"}) {
		t.Errorf("rules %q", rules)
	}
	if !slices.Equal(defaults, []string{
		"setenv: the user's variables reach the helper",
		"!env_reset: the user's whole environment reaches the helper",
		"Defaults!" + helper + " setenv: set for one runas user or command alone",
	}) {
		t.Errorf("defaults %q", defaults)
	}
	if r, d := (hostadmit.SudoList{Rules: []string{hostadmit.HelperRule(helper)}, Defaults: []string{"env_reset", "use_pty"}}).BeyondHelper(helper); len(r)+len(d) != 0 {
		t.Errorf("admit's own line reaches past it: %q %q", r, d)
	}
}
```

Run: `go test ./internal/hostadmit/ -run 'TestReadSudoList|TestBeyondHelper'`
Expected: FAIL to compile, `undefined: hostadmit.ReadSudoList`.

- [ ] **Step 2: Move the reading out of the facts, and export it**

Create `internal/hostadmit/sudolist.go`:

```go
package hostadmit

import "strings"

// SudoList is what sudo -l printed for one user: its rules, each entry of
// the Defaults that apply to it, and each runas or command-specific
// Defaults line, as sudo printed them.
type SudoList struct {
	Rules, Defaults, Specific []string
}

// ReadSudoList reads what sudo -n -l printed under LC_ALL=C, as root for a
// user (-U) or as the user itself: a header line per section, each entry
// indented below it, long lines wrapped onto more indented lines. A user
// sudo refuses outright reads as a list with nothing in it. ok is false
// for a line it does not know, or a listing with neither rules nor a
// refusal, so no caller takes an answer it cannot read for one granting
// nothing.
func ReadSudoList(lines []string) (list SudoList, ok bool) {
	section, understood := "", false
	var defaults []string
	for _, l := range lines {
		l = strings.TrimRight(l, "\r")
		indented := strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")
		entry := strings.TrimSpace(l)
		switch {
		case entry == "":
			section = ""
		case !indented && strings.HasPrefix(l, "Matching Defaults entries for "):
			section = "defaults"
		case !indented && strings.HasPrefix(l, "Runas and Command-specific defaults for "):
			section = "specific"
		case !indented && strings.Contains(l, " may run the following commands on "):
			section, understood = "rules", true
		case !indented && strings.Contains(l, " is not allowed to run sudo on "),
			!indented && strings.HasPrefix(l, "Sorry, user ") && strings.Contains(l, " may not run sudo on "):
			section, understood = "", true
		case indented && section == "defaults":
			defaults = append(defaults, entry)
		case indented && section == "specific":
			list.Specific = append(list.Specific, entry)
		case indented && section == "rules":
			list.Rules = append(list.Rules, entry)
		default:
			return SudoList{}, false
		}
	}
	// Defaults wrap after a comma, and a comma inside a value is escaped.
	for _, e := range splitUnescaped(strings.Join(defaults, " "), ',') {
		if e = strings.TrimSpace(e); e != "" {
			list.Defaults = append(list.Defaults, e)
		}
	}
	return list, understood
}

// HelperRule is the one rule host admit grants a deployment's user, as
// sudo -l lists it: the WireGuard helper, as root, with no password.
func HelperRule(helper string) string { return "(root) NOPASSWD: " + helper }

// BeyondHelper is what in l reaches past the helper: every rule but
// HelperRule(helper); every Defaults entry sudoDefaultProblem names, with
// why; and every runas or command-specific Defaults line, which sudo -l
// lists apart from the rule it changes. host admit refuses a user with any
// of them, and doctor fails one.
func (l SudoList) BeyondHelper(helper string) (rules, defaults []string) {
	for _, r := range l.Rules {
		if r != HelperRule(helper) {
			rules = append(rules, r)
		}
	}
	for _, d := range l.Defaults {
		if why := sudoDefaultProblem(d); why != "" {
			defaults = append(defaults, d+": "+why)
		}
	}
	for _, d := range l.Specific {
		defaults = append(defaults, d+": set for one runas user or command alone")
	}
	return rules, defaults
}

// splitUnescaped splits s at each sep that no backslash escapes.
func splitUnescaped(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case sep:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
```

In `internal/hostadmit/facts.go`, delete `readSudo` and `splitUnescaped`, and replace the call in `parseFacts`:

```go
	if f.raw["sudo"] != nil && f.raw["sudo"][len(f.raw["sudo"])-1] == "listed" {
		if list, ok := ReadSudoList(sudoL); ok {
			f.User.SudoRules, f.User.SudoDefaults, f.User.SudoSpecific, f.User.SudoRead = list.Rules, list.Defaults, list.Specific, true
		}
	}
```

In `internal/hostadmit/user.go`, `sudoRules`, replace the three loops that build `rules` and `defaults` with:

```go
	extra, reach := SudoList{Rules: u.SudoRules, Defaults: u.SudoDefaults, Specific: u.SudoSpecific}.BeyondHelper(helper)
	var rules, defaults []string
	for _, r := range extra {
		rules = append(rules, "- "+ui.Visible(r))
	}
	for _, d := range reach {
		defaults = append(defaults, "- "+ui.Visible(d))
	}
```

The message is unchanged: `ui.Visible` leaves text that is visible already as it is.

- [ ] **Step 3: Export the drop-in path**

Rename `meshDropInPath` to `MeshDropInPath` everywhere in the package, and say why it is exported:

Run: `grep -rln 'meshDropInPath' internal/hostadmit | xargs sed -i '' 's/meshDropInPath/MeshDropInPath/g'` (GNU sed: `sed -i`)

In `facts.go`, its comment becomes:

```go
// MeshDropInPath is the user's drop-in that orders its manager after the
// mesh. Its path holds the user's UID, which the probe learns, so the probe
// reads it from its own list (userfiles) rather than from probedFiles.
// doctor asks whether it is there, as the user: it is mode 644.
```

In `user_internal_test.go`, replace `TestReadSudoReadsUbuntusListing` (now `TestReadSudoList` in `sudolist_test.go`) by deleting it, and drop the `os` and `slices` imports if nothing else there uses them.

Run: `gofmt -l internal/hostadmit && go vet ./internal/hostadmit/ && go test ./internal/hostadmit/`
Expected: nothing listed; PASS, the golden plans and every refusal unchanged.

- [ ] **Step 4: Commit**

```bash
git add internal/hostadmit
git commit -m "refactor: share host admit's reading of sudo -l and its drop-in path

doctor checks on every run what admit checks once: that sudo lets the
deployment's user reach no further than the WireGuard helper, and that
its manager starts after the mesh at boot. Exporting admit's own reading,
its judgement of what reaches past the helper and the drop-in's path
keeps the two from drifting. The reading also takes sudo's refusal as
the user sees it, which doctor gets and admit, asking as root, does not.

Nothing an operator would notice, so no changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: The user, its Docker daemon and linger, judged

**Files:**
- Create: `internal/doctor/rootless.go`
- Test: `internal/doctor/rootless_test.go`

**Interfaces:**
- Consumes: `hostadmit.PrivilegedGroups`, `hostadmit.MinRootlessEngine` (`"29.5.0"`), `hostadmit.MeshDropInPath` (Task 1), `hostcheck.AtLeast(version, min string) bool`; `firstLine`, `lastLines` (`doctor.go`); the test helpers `fixture`, `find`, `dump` (`doctor_test.go`).
- Produces: `const SectionUser = "user"`, `SectionDocker = "docker"`, `SectionLinger = "linger"`; `const UserCommand`, `DockerCommand string`; `var LingerCommand string`; `type UserProbe`, `DockerProbe`, `LingerProbe struct{ Site, Out, Err string }`, each with `site()`; `func (p DockerProbe) Answered() bool`; `func User(probes []UserProbe) []Finding`, `func DockerSocket(probes []DockerProbe) []Finding`, `func DockerEngine(probes []DockerProbe) []Finding`, `func Linger(d deployment.Deployment, probes []LingerProbe) []Finding`; `func keyed(out string) map[string]string`.

- [ ] **Step 1: Write the failing tests**

Create `internal/doctor/rootless_test.go`:

```go
package doctor

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

// rootlessDocker is what DockerCommand prints for a rootless daemon.
const rootlessDocker = "host unix:///run/user/1001/docker.sock\nengine 29.9.1\nsecurity [\"name=seccomp,profile=builtin\",\"name=rootless\",\"name=cgroupns\"]\n"

// A user in any group that reaches past it fails, naming each group; uid 0
// fails on its own; a user in none passes; an answer that cannot be read
// is a warning, never a pass.
func TestUser(t *testing.T) {
	findings := User([]UserProbe{
		{Site: "home-a", Out: "uid 1001\nuser paisans-f2a9\ngroups paisans-f2a9\n"},
		{Site: "home-b", Out: "uid 1002\nuser paisans-f2a9\ngroups paisans-f2a9 docker sudo docker\n"},
		{Site: "vm", Out: "uid 0\nuser root\ngroups root\n"},
		{Site: "watch", Out: "", Err: "id: cannot find name for group ID 1003"},
	})
	if f := find(t, findings, SectionUser, "home-a: paisans-f2a9 is in no privileged group"); f.Level != OK {
		t.Errorf("home-a: level %v", f.Level)
	}
	b := find(t, findings, SectionUser, "home-b: paisans-f2a9 is in docker, sudo")
	if b.Level != Fail || !strings.Contains(strings.Join(b.More, "\n"), "`sudo gpasswd -d paisans-f2a9 <group>` on home-b") {
		t.Errorf("home-b: %+v", b)
	}
	if f := find(t, findings, SectionUser, "vm: the login user root has uid 0"); f.Level != Fail {
		t.Errorf("vm: level %v", f.Level)
	}
	if f := find(t, findings, SectionUser, "watch: could not read the login user's groups"); f.Level != Warn {
		t.Errorf("watch: level %v", f.Level)
	}
}

// Every group host admit refuses fails here too: the two read one list.
func TestEveryPrivilegedGroupFails(t *testing.T) {
	for _, g := range hostadmit.PrivilegedGroups {
		fs := User([]UserProbe{{Site: "home-a", Out: "uid 1001\nuser paisans-f2a9\ngroups paisans-f2a9 " + g + "\n"}})
		if len(fs) != 1 || fs[0].Level != Fail {
			t.Errorf("%s: %+v", g, fs)
		}
	}
}

// The daemon answers as rootless, does not answer, answers as root, or
// says something unreadable.
func TestDockerSocket(t *testing.T) {
	probes := []DockerProbe{
		{Site: "home-a", Out: rootlessDocker},
		{Site: "home-b", Out: "host unix:///run/user/1001/docker.sock\n", Err: "Cannot connect to the Docker daemon at unix:///run/user/1001/docker.sock. Is the docker daemon running?"},
		{Site: "vm", Out: "host unix:///run/user/1001/docker.sock\nengine 29.9.1\nsecurity [\"name=apparmor\",\"name=seccomp,profile=builtin\",\"name=cgroupns\"]\n"},
		{Site: "watch", Out: "host unix:///run/user/1001/docker.sock\nengine 29.9.1\nsecurity not json\n"},
	}
	findings := DockerSocket(probes)
	if f := find(t, findings, SectionDocker, "home-a: its rootless Docker answers at unix:///run/user/1001/docker.sock"); f.Level != OK {
		t.Errorf("home-a: level %v", f.Level)
	}
	b := find(t, findings, SectionDocker, "home-b: its rootless Docker does not answer at unix:///run/user/1001/docker.sock")
	if b.Level != Fail || !strings.Contains(strings.Join(b.More, "\n"), "`systemctl --user status docker`") {
		t.Errorf("home-b: %+v", b)
	}
	if f := find(t, findings, SectionDocker, "vm: the Docker daemon at unix:///run/user/1001/docker.sock is not rootless"); f.Level != Fail {
		t.Errorf("vm: level %v", f.Level)
	}
	if f := find(t, findings, SectionDocker, "watch: could not read its Docker daemon's answer"); f.Level != Warn {
		t.Errorf("watch: level %v", f.Level)
	}
	for i, want := range []bool{true, false, false, false} {
		if got := probes[i].Answered(); got != want {
			t.Errorf("%s: Answered %v, want %v", probes[i].Site, got, want)
		}
	}
}

// 29.5.0 is the floor, inclusive; a daemon that did not answer is judged
// by DockerSocket alone.
func TestDockerEngine(t *testing.T) {
	findings := DockerEngine([]DockerProbe{
		{Site: "home-a", Out: rootlessDocker},
		{Site: "home-b", Out: strings.Replace(rootlessDocker, "29.9.1", "29.4.2", 1)},
		{Site: "vm", Out: strings.Replace(rootlessDocker, "29.9.1", "29.5.0", 1)},
		{Site: "watch", Err: "Cannot connect to the Docker daemon"},
	})
	if len(findings) != 3 {
		t.Fatalf("want 3 findings, watch's left out:\n%s", dump(findings))
	}
	if f := find(t, findings, SectionDocker, "home-a: Docker Engine 29.9.1"); f.Level != OK {
		t.Errorf("home-a: level %v", f.Level)
	}
	b := find(t, findings, SectionDocker, "home-b: Docker Engine 29.4.2, and 29.5.0 or later is needed")
	if b.Level != Fail || !strings.Contains(strings.Join(b.More, "\n"), "host networking shares the host's network namespace") {
		t.Errorf("home-b: %+v", b)
	}
	if f := find(t, findings, SectionDocker, "vm: Docker Engine 29.5.0"); f.Level != OK {
		t.Errorf("vm: level %v", f.Level)
	}
}

// Linger on and the manager ordered after the mesh pass; either missing
// fails; an answer that cannot be read is a warning.
func TestLinger(t *testing.T) {
	d := fixture(t).Deployment()
	findings := Linger(d, []LingerProbe{
		{Site: "home-a", Out: "linger yes\nmesh-order yes\n"},
		{Site: "home-b", Out: "linger no\nmesh-order no\n"},
		{Site: "vm", Out: "linger \nmesh-order yes\n"},
	})
	if f := find(t, findings, SectionLinger, "home-a: linger is on"); f.Level != OK {
		t.Errorf("home-a: level %v", f.Level)
	}
	if f := find(t, findings, SectionLinger, "home-a: its manager starts after the mesh at boot"); f.Level != OK {
		t.Errorf("home-a order: level %v", f.Level)
	}
	b := find(t, findings, SectionLinger, "home-b: linger is off")
	if b.Level != Fail || !strings.Contains(strings.Join(b.More, "\n"), "`paisans host admit`") {
		t.Errorf("home-b: %+v", b)
	}
	o := find(t, findings, SectionLinger, "home-b: its manager is not ordered after the mesh at boot")
	if o.Level != Fail || !strings.Contains(strings.Join(o.More, "\n"), "wg-quick@psns-f2a9") {
		t.Errorf("home-b order: %+v", o)
	}
	if f := find(t, findings, SectionLinger, "vm: could not read whether linger is on"); f.Level != Warn {
		t.Errorf("vm: level %v", f.Level)
	}
}

// LingerCommand looks for the drop-in at host admit's own path.
func TestLingerCommandReadsAdmitsDropIn(t *testing.T) {
	if !strings.Contains(LingerCommand, `"`+hostadmit.MeshDropInPath("$(id -u)")+`"`) {
		t.Errorf("LingerCommand: %s", LingerCommand)
	}
}
```

Run: `go test ./internal/doctor/ -run 'TestUser|TestEveryPrivilegedGroupFails|TestDocker|TestLinger'`
Expected: FAIL to compile, `undefined: User` (and the other judges and probes).

- [ ] **Step 2: Write the judges**

Create `internal/doctor/rootless.go`:

```go
package doctor

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
)

// The rootless checks ask whether a site is still what its owner's host
// admit made it: the admission record covers the site (admission.go), the
// deployment's user holds no group that reaches past it, its own rootless
// Docker daemon answers and is new enough, linger keeps that daemon running
// with nobody logged in and its manager starts after the mesh, and sudo
// lets the user run the WireGuard helper and nothing else (sudo.go). Each
// is read as the login user, with nothing that user may not read. See
// docs/specs/2026-10-15-rootless-and-host-admit.md, The deployment's user.

// Sections of the rootless checks, read right after admission.
const (
	SectionUser   = "user"
	SectionDocker = "docker"
	SectionLinger = "linger"
)

// UserCommand prints the login user's UID, name and groups, a line each.
const UserCommand = `printf 'uid %s\nuser %s\ngroups %s\n' "$(id -u)" "$(id -un)" "$(id -nG)"`

// DockerCommand prints the DOCKER_HOST the command ran with, then asks the
// daemon there for its Engine version and its security options, which name
// rootless for a daemon running as its user. The transport exports
// DOCKER_HOST as the login user's own socket ahead of every command.
const DockerCommand = `printf 'host %s\n' "${DOCKER_HOST:-unset}" && docker version --format 'engine {{.Server.Version}}' && docker info --format 'security {{json .SecurityOptions}}'`

// LingerCommand prints whether systemd keeps the login user's instance, and
// with it the user's Docker daemon, running with no session open, and
// whether host admit's drop-in starts that instance after the mesh at boot.
// The drop-in is root's, mode 644, so the user reads it.
var LingerCommand = `printf 'linger %s\n' "$(loginctl show-user "$(id -un)" --property=Linger --value)"; ` +
	`if [ -f "` + hostadmit.MeshDropInPath("$(id -u)") + `" ]; then echo 'mesh-order yes'; else echo 'mesh-order no'; fi`

// UserProbe is UserCommand's answer on one site.
type UserProbe struct{ Site, Out, Err string }

// DockerProbe is DockerCommand's answer on one site. Out is kept when the
// command failed, since its first line still says which socket was asked.
type DockerProbe struct{ Site, Out, Err string }

// LingerProbe is LingerCommand's answer on one site.
type LingerProbe struct{ Site, Out, Err string }

func (p UserProbe) site() string   { return p.Site }
func (p DockerProbe) site() string { return p.Site }
func (p LingerProbe) site() string { return p.Site }

// keyed reads lines of the form "<key> <value>" into a map, the value being
// the rest of the line. A line with no space is left out.
func keyed(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), " "); ok {
			m[k] = strings.TrimSpace(v)
		}
	}
	return m
}

// User fails a login user that holds any group host admit refuses
// (hostadmit.PrivilegedGroups), or uid 0. Admit checks the same list once;
// this checks it on every run, because a group can be added to the user
// after admission and nothing else would notice.
func User(probes []UserProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		f := keyed(p.Out)
		name, uid := f["user"], f["uid"]
		if p.Err != "" || name == "" || uid == "" {
			why := p.Err
			if why == "" {
				why = "unreadable answer: " + firstLine(p.Out)
			}
			out = append(out, Finding{Section: SectionUser, Level: Warn, Line: fmt.Sprintf("%s: could not read the login user's groups", p.Site), More: []string{why}})
			continue
		}
		if uid == "0" {
			out = append(out, Finding{Section: SectionUser, Level: Fail,
				Line: fmt.Sprintf("%s: the login user %s has uid 0", p.Site, name),
				More: []string{
					"A deployment's user is never root: everything the operators run on the site would run as root.",
					"recover:",
					fmt.Sprintf("  1. Remove `ssh.user` from sites.%s, so the site logs in as the deployment's own user, and have the host's owner admit it.", p.Site),
				}})
			continue
		}
		var held []string
		for _, g := range strings.Fields(f["groups"]) {
			if slices.Contains(hostadmit.PrivilegedGroups, g) && !slices.Contains(held, g) {
				held = append(held, g)
			}
		}
		if len(held) == 0 {
			out = append(out, Finding{Section: SectionUser, Level: OK, Line: fmt.Sprintf("%s: %s is in no privileged group", p.Site, name)})
			continue
		}
		out = append(out, Finding{Section: SectionUser, Level: Fail,
			Line: fmt.Sprintf("%s: %s is in %s", p.Site, name, strings.Join(held, ", ")),
			More: []string{
				"The deployment's user is the boundary between this deployment and the rest of the host, and each of these groups reaches past it, to root, to every user's logs, to the owner's system Docker daemon, or to the host's disks and secrets.",
				"recover:",
				fmt.Sprintf("  1. The host's owner takes %s out of each, with `sudo gpasswd -d %s <group>` on %s.", name, name, p.Site),
				fmt.Sprintf("  2. `paisans doctor --site %s` again.", p.Site),
			}})
	}
	return out
}

// rootlessOption is the security option docker info lists for a daemon
// that runs as its user.
const rootlessOption = "name=rootless"

func (p DockerProbe) parsed() (host, engine string, options []string, read bool) {
	f := keyed(p.Out)
	host, engine = f["host"], f["engine"]
	if s, ok := f["security"]; ok {
		read = json.Unmarshal([]byte(s), &options) == nil
	}
	return host, engine, options, read
}

// Answered reports whether the site's own rootless daemon answered, which
// every check that runs docker on the site needs.
func (p DockerProbe) Answered() bool {
	_, engine, options, read := p.parsed()
	return p.Err == "" && read && engine != "" && slices.Contains(options, rootlessOption)
}

// DockerSocket checks that the login user's own rootless daemon answers.
// Every container of the deployment runs in it, and a daemon running as root
// at that socket would be root on the host to anyone who can reach it.
func DockerSocket(probes []DockerProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		host, engine, options, read := p.parsed()
		at := host
		if at == "" || at == "unset" {
			at = "the login user's socket"
		}
		switch {
		case p.Err != "" && engine == "":
			out = append(out, Finding{Section: SectionDocker, Level: Fail,
				Line: fmt.Sprintf("%s: its rootless Docker does not answer at %s", p.Site, at),
				More: []string{
					firstLine(p.Err),
					"Every container of this deployment on the site runs in the deployment's user's own daemon, so none of them runs while it does not answer.",
					"recover:",
					fmt.Sprintf("  1. `systemctl --user status docker`, as the deployment's user on %s, says why.", p.Site),
					"  2. `systemctl --user start docker` starts it.",
				}})
		case !read || engine == "":
			out = append(out, Finding{Section: SectionDocker, Level: Warn, Line: fmt.Sprintf("%s: could not read its Docker daemon's answer", p.Site), More: lastLines(p.Out+"\n"+p.Err, 3)})
		case !slices.Contains(options, rootlessOption):
			out = append(out, Finding{Section: SectionDocker, Level: Fail,
				Line: fmt.Sprintf("%s: the Docker daemon at %s is not rootless", p.Site, at),
				More: []string{
					"It runs as root, and a daemon running as root is root on the host to anyone who can reach its socket, which undoes what admitting the deployment to a user of its own draws.",
					"recover:",
					fmt.Sprintf("  1. The host's owner finds what runs a root daemon at that socket and stops it, then runs `paisans host admit` for %s again, which sets up the user's own rootless daemon.", p.Site),
				}})
		default:
			out = append(out, Finding{Section: SectionDocker, Level: OK, Line: fmt.Sprintf("%s: its rootless Docker answers at %s", p.Site, at)})
		}
	}
	return out
}

// DockerEngine checks the rootless daemon's Engine against the floor host
// admit holds it to. A daemon that did not answer is DockerSocket's.
func DockerEngine(probes []DockerProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		if !p.Answered() {
			continue
		}
		_, engine, _, _ := p.parsed()
		if hostcheck.AtLeast(engine, hostadmit.MinRootlessEngine) {
			out = append(out, Finding{Section: SectionDocker, Level: OK, Line: fmt.Sprintf("%s: Docker Engine %s", p.Site, engine)})
			continue
		}
		out = append(out, Finding{Section: SectionDocker, Level: Fail,
			Line: fmt.Sprintf("%s: Docker Engine %s, and %s or later is needed", p.Site, engine, hostadmit.MinRootlessEngine),
			More: []string{
				"From 29.5 a rootless container with host networking shares the host's network namespace. Every infrastructure container is network_mode: host and binds the mesh address, so on an older Engine they bind inside RootlessKit's own namespace, where no other site reaches them.",
				"recover:",
				"  1. The host's owner upgrades Docker; upgrading it is the owner's decision, which host admit leaves to them.",
				fmt.Sprintf("  2. `systemctl --user restart docker`, as the deployment's user on %s, which restarts every container in it.", p.Site),
			}})
	}
	return out
}

// Linger checks that systemd keeps the login user's instance running with
// nobody logged in, and that host admit's drop-in starts it after the mesh
// at boot. Without linger the user's Docker daemon, and every container in
// it, stops when the last login ends and does not start at boot; without
// the drop-in it may start the containers before the mesh address exists.
func Linger(d deployment.Deployment, probes []LingerProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		f := keyed(p.Out)
		switch linger := f["linger"]; {
		case p.Err == "" && linger == "yes":
			out = append(out, Finding{Section: SectionLinger, Level: OK, Line: fmt.Sprintf("%s: linger is on: its Docker daemon runs with nobody logged in, and starts at boot", p.Site)})
		case p.Err == "" && linger == "no":
			out = append(out, Finding{Section: SectionLinger, Level: Fail,
				Line: fmt.Sprintf("%s: linger is off", p.Site),
				More: []string{
					"Without it the user's systemd instance, and with it its Docker daemon and every container, stops when the last login to the user ends, and nothing of the deployment starts at boot.",
					"recover:",
					fmt.Sprintf("  1. The host's owner runs `paisans host admit` for %s again, which turns it on.", p.Site),
				}})
		default:
			why := p.Err
			if why == "" {
				why = "unreadable answer: " + firstLine(p.Out)
			}
			out = append(out, Finding{Section: SectionLinger, Level: Warn, Line: fmt.Sprintf("%s: could not read whether linger is on", p.Site), More: []string{why}})
			continue
		}
		switch f["mesh-order"] {
		case "yes":
			out = append(out, Finding{Section: SectionLinger, Level: OK, Line: fmt.Sprintf("%s: its manager starts after the mesh at boot", p.Site)})
		case "no":
			out = append(out, Finding{Section: SectionLinger, Level: Fail,
				Line: fmt.Sprintf("%s: its manager is not ordered after the mesh at boot", p.Site),
				More: []string{
					fmt.Sprintf("host admit's drop-in, which starts the user's manager after %s, is missing. At boot its Docker daemon can then start a container before %s is up, and one that publishes on the mesh address fails with cannot assign requested address, which Docker does not retry.", d.WireGuardUnit(), d.Interface()),
					"recover:",
					fmt.Sprintf("  1. The host's owner runs `paisans host admit` for %s again, which writes it; it takes effect at the next boot.", p.Site),
				}})
		}
	}
	return out
}
```

- [ ] **Step 3: Run them to verify they pass**

Run: `go test ./internal/doctor/ -run 'TestUser|TestEveryPrivilegedGroupFails|TestDocker|TestLinger' && go vet ./internal/doctor/`
Expected: PASS. `find` matches the start of a line, so `home-a: linger is on` finds the longer OK line.

- [ ] **Step 4: Commit**

```bash
git add internal/doctor/rootless.go internal/doctor/rootless_test.go
git commit -m "feat: judge the deployment's user, its rootless docker and linger

Admission draws the boundary once; a group added to the user afterwards,
a daemon that stopped answering or was replaced by a root one, an Engine
too old for host networking, linger turned off or the drop-in that
starts the user's manager after the mesh gone each undoes part of it
with nothing else to notice. These are the judges doctor runs over what
each site says; the privileged groups, the Engine floor and the
drop-in's path are host admit's own, so the two cannot drift.

Nothing an operator would notice yet, since no check runs them, so no
changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: What sudo lets the deployment's user run

**Files:**
- Create: `internal/doctor/sudo.go`
- Test: `internal/doctor/sudo_test.go`

**Interfaces:**
- Consumes: Task 1's `hostadmit.ReadSudoList`, `hostadmit.HelperRule`, `SudoList.BeyondHelper`; `apply.WGHelper{Iface string}.Path() (string, error)`; `deployment.Deployment.Interface()`, `.Prefix()`; `config.Site.SSHUser()`; `ui.Visible`; `lastLines`.
- Produces: `const SectionSudo = "sudo rights"`; `const SudoCommand = "LC_ALL=C sudo -n -l"`; `type SudoProbe struct{ Site, Out, Err string }` with `site()`; `func SudoRights(cfg *config.Config, probes []SudoProbe) []Finding`.

- [ ] **Step 1: Write the failing tests**

Create `internal/doctor/sudo_test.go`:

```go
package doctor

import (
	"strings"
	"testing"
)

// sudoDefaults is the start of sudo -l's answer on Ubuntu 24.04, before
// the list itself.
const sudoDefaults = "Matching Defaults entries for ubuntu on home-a:\n" +
	"    env_reset, mail_badpass,\n" +
	"    secure_path=/usr/local/sbin\\:/usr/local/bin\\:/usr/sbin\\:/usr/bin\\:/sbin\\:/bin\\:/snap/bin,\n" +
	"    use_pty\n\n"

const helperEntry = "(root) NOPASSWD: /usr/local/libexec/paisans/wg-psns-f2a9"

func sudoList(entries ...string) string {
	return sudoDefaults + "User ubuntu may run the following commands on home-a:\n    " + strings.Join(entries, "\n    ") + "\n"
}

// What sudo allows is exactly what host admit accepts, or the check fails
// naming what reaches past the helper, or that the helper is missing; an
// answer it cannot read is a warning.
func TestSudoRights(t *testing.T) {
	cfg := fixture(t)
	for _, c := range []struct {
		name, out, err string
		level          Level
		line           string
		more           string
	}{
		{"helper alone", sudoList(helperEntry), "", OK, "home-a: sudo lets ubuntu run the WireGuard helper and nothing else", ""},
		{"helper and ALL", sudoList(helperEntry, "(ALL : ALL) ALL"), "", Fail, "home-a: sudo lets ubuntu reach past the WireGuard helper: (ALL : ALL) ALL", "`sudo -l -U ubuntu`"},
		{"helper and a shell on one line", sudoList(helperEntry + ", /bin/sh"), "", Fail, "reach past the WireGuard helper: " + helperEntry + ", /bin/sh", ""},
		{"helper as any user", sudoList("(ALL) NOPASSWD: /usr/local/libexec/paisans/wg-psns-f2a9"), "", Fail, "reach past the WireGuard helper: (ALL) NOPASSWD: /usr/local/libexec/paisans/wg-psns-f2a9", ""},
		{"helper asking a password", sudoList("(root) /usr/local/libexec/paisans/wg-psns-f2a9"), "", Fail, "reach past the WireGuard helper: (root) /usr/local/libexec/paisans/wg-psns-f2a9", ""},
		{"another deployment's helper", sudoList(helperEntry, "(root) NOPASSWD: /usr/local/libexec/paisans/wg-psns-0a0b"), "", Fail, "reach past the WireGuard helper: (root) NOPASSWD: /usr/local/libexec/paisans/wg-psns-0a0b", ""},
		{"setenv in the Defaults", strings.Replace(sudoList(helperEntry), "use_pty", "use_pty, setenv", 1), "", Fail, "reach past the WireGuard helper: setenv: the user's variables reach the helper", ""},
		{"a specific Defaults line", sudoDefaults + "Runas and Command-specific defaults for ubuntu:\n    Defaults!/usr/local/libexec/paisans/wg-psns-f2a9 setenv\n\nUser ubuntu may run the following commands on home-a:\n    " + helperEntry + "\n", "", Fail, "set for one runas user or command alone", ""},
		{"not allowed", "Sorry, user ubuntu may not run sudo on home-a.\n", "exit status 1", Fail, "home-a: sudo does not let ubuntu run the WireGuard helper", "`paisans host admit`"},
		{"password required", "sudo: a password is required\n", "exit status 1", Fail, "home-a: sudo does not let ubuntu run the WireGuard helper", ""},
		{"unrecognised", "something else\n", "", Warn, "home-a: could not read what sudo lets ubuntu run", ""},
		{"translated", "Benutzer ubuntu darf die folgenden Befehle auf home-a ausführen:\n    " + helperEntry + "\n", "", Warn, "home-a: could not read what sudo lets ubuntu run", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			fs := SudoRights(cfg, []SudoProbe{{Site: "home-a", Out: c.out, Err: c.err}})
			if len(fs) != 1 {
				t.Fatalf("want one finding:\n%s", dump(fs))
			}
			f := fs[0]
			if f.Level != c.level || !strings.Contains(f.Line, c.line) || !strings.Contains(strings.Join(f.More, "\n"), c.more) {
				t.Errorf("got %v %q\n%s", f.Level, f.Line, strings.Join(f.More, "\n"))
			}
		})
	}
}
```

Run: `go test ./internal/doctor/ -run TestSudoRights`
Expected: FAIL to compile, `undefined: SudoRights`.

- [ ] **Step 2: Write the judge**

Create `internal/doctor/sudo.go`:

```go
package doctor

import (
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// SectionSudo is what sudo lets the deployment's user run.
const SectionSudo = "sudo rights"

// SudoCommand lists what sudo lets the login user run, without asking for
// a password, in the C locale: sudo translates its answer, and the reading
// knows its English words. The transport returns stderr with stdout, so a
// refusal is in the answer too.
const SudoCommand = "LC_ALL=C sudo -n -l"

// SudoProbe is SudoCommand's answer on one site.
type SudoProbe struct{ Site, Out, Err string }

func (p SudoProbe) site() string { return p.Site }

// SudoRights checks that sudo lets the deployment's user run the WireGuard
// helper and nothing else, as host admit accepts it: its one rule, no
// Defaults entry that hands the helper the user's environment or path, and
// no runas or command-specific Defaults line (hostadmit.SudoList.
// BeyondHelper). Every operator of the deployment can log in as the user,
// so anything more is theirs too, and root on the host for anything past
// the helper.
func SudoRights(cfg *config.Config, probes []SudoProbe) []Finding {
	d := cfg.Deployment()
	helper, helperErr := apply.WGHelper{Iface: d.Interface()}.Path()
	var out []Finding
	for _, p := range probes {
		user := cfg.Sites[p.Site].SSHUser()
		missing := Finding{Section: SectionSudo, Level: Fail,
			Line: fmt.Sprintf("%s: sudo does not let %s run the WireGuard helper", p.Site, user),
			More: []string{
				"Every change to the mesh (its file, bringing it up, syncing its peers) runs the helper with sudo -n, so apply cannot manage the mesh on this site.",
				"recover:",
				fmt.Sprintf("  1. The host's owner runs `paisans host admit` for %s again, which installs %s and its line in /etc/sudoers.d/%s.", p.Site, helper, d.Prefix()),
			}}
		if helperErr != nil || strings.Contains(p.Out, "a password is required") {
			out = append(out, missing)
			continue
		}
		list, ok := hostadmit.ReadSudoList(strings.Split(strings.TrimRight(p.Out, "\n"), "\n"))
		if !ok {
			out = append(out, Finding{Section: SectionSudo, Level: Warn, Line: fmt.Sprintf("%s: could not read what sudo lets %s run", p.Site, user), More: lastLines(p.Out+"\n"+p.Err, 3)})
			continue
		}
		rules, defaults := list.BeyondHelper(helper)
		if extra := append(rules, defaults...); len(extra) > 0 {
			shown := make([]string, len(extra))
			for i, e := range extra {
				shown[i] = ui.Visible(e)
			}
			out = append(out, Finding{Section: SectionSudo, Level: Fail,
				Line: fmt.Sprintf("%s: sudo lets %s reach past the WireGuard helper: %s", p.Site, user, strings.Join(shown, "; ")),
				More: []string{
					fmt.Sprintf("Every operator of this deployment can log in as %s, so whatever sudo allows it is theirs too, and root on the host for anything past the helper. host admit writes one line, %s, in /etc/sudoers.d/%s; the rest comes from elsewhere in the host's sudoers.", user, hostadmit.HelperRule(helper), d.Prefix()),
					"recover:",
					fmt.Sprintf("  1. The host's owner lists every rule and Defaults entry for the user with `sudo -l -U %s` and removes all but host admit's line with `visudo`, looking in /etc/sudoers and /etc/sudoers.d.", user),
					fmt.Sprintf("  2. `paisans doctor --site %s` again.", p.Site),
				}})
			continue
		}
		if slices.Contains(list.Rules, hostadmit.HelperRule(helper)) {
			out = append(out, Finding{Section: SectionSudo, Level: OK, Line: fmt.Sprintf("%s: sudo lets %s run the WireGuard helper and nothing else", p.Site, user)})
			continue
		}
		out = append(out, missing)
	}
	return out
}
```

- [ ] **Step 3: Run it to verify it passes**

Run: `go test ./internal/doctor/ -run TestSudoRights && go vet ./internal/doctor/`
Expected: PASS. The fixture's sites log in as `ubuntu`, so the lines name `ubuntu`.

- [ ] **Step 4: Commit**

```bash
git add internal/doctor/sudo.go internal/doctor/sudo_test.go
git commit -m "feat: judge what sudo lets the deployment's user run

The WireGuard helper is the one command the deployment's user may run as
root, through the one sudoers line host admit writes. Every operator can
log in as that user, so a second rule, a group's ALL, the helper allowed
as any user, a Defaults entry that hands the helper the user's
environment, or the helper's line gone are each worth a failure naming
it. The reading and the judgement are host admit's own, sudo -l is read
in the C locale, and an answer it does not know is a warning, never a
pass.

Nothing an operator would notice yet, since no check runs it, so no
changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: The admission record against the declaration

**Files:**
- Create: `internal/doctor/admission.go`
- Test: `internal/doctor/admission_test.go`

**Interfaces:**
- Consumes: `admission.Record`, `(Record).Differences(cfg *config.Config, site string) []string`, `admission.Needs(config.Site) Grants`, `(Grants).Missing(Grants) []string`, `admission.Build`, `admission.NewRecord`; `topology.Site`, `topology.Pending`; `config.Site.SSHUser()`, `.Has(config.RoleGateway)`, `.WebServerManagedBy()`; `roleLine`, `Losses` (`internal/doctor/reach.go`).
- Produces: `const SectionAdmission = "admission"`; `type AdmissionProbe struct { Site string; Found bool; Record admission.Record; Err string; More []string; Class topology.Site; Unclassified string }` with `site()`, `func (p AdmissionProbe) Covered(cfg *config.Config) bool`, `func (p AdmissionProbe) Pending() bool`; `func Admission(cfg *config.Config, probes []AdmissionProbe) []Finding`; `func pending(cfg *config.Config, section, site, line string, why ...string) Finding`; `func absentLevel(cfg *config.Config, site string) Level`; `func requestAdvice(site string) string`. In tests: `func admittedRecord(cfg *config.Config, site string) admission.Record`.

- [ ] **Step 1: Write the failing test**

Create `internal/doctor/admission_test.go`:

```go
package doctor

import (
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/topology"
)

// admittedRecord is the record host admit leaves for a site of cfg as
// declared, with every grant its roles need. It panics rather than take a
// *testing.T, since the graph tests' reads call it.
func admittedRecord(cfg *config.Config, site string) admission.Record {
	req, err := admission.Build(cfg, site)
	if err != nil {
		panic(err)
	}
	rec := admission.NewRecord(req, req.Grants, "", time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC))
	if cfg.Sites[site].WebServerManagedBy() == config.ManagedByExternal {
		rec.WebServer = &admission.WebServer{Server: "nginx", Process: "nginx"}
	}
	return rec
}

func pendingClass(site string) topology.Site {
	return topology.Site{Name: site, Class: topology.Pending, Why: site + ": its host has not admitted this deployment", Request: true}
}

func TestAdmission(t *testing.T) {
	cfg := fixture(t)
	noWatchdog := admittedRecord(cfg, "home-b")
	noWatchdog.Grants.Watchdog = false
	moved := admittedRecord(cfg, "home-a")
	moved.Ports.SSH = 2222
	noPorts := admittedRecord(cfg, "vm")
	noPorts.Grants.PublicPorts = false
	noPorts.Ports.Public = []int{}
	joined := "home-a has joined this deployment, and its admission does not cover it: the host opened ssh port 2222, and the site's is 22."

	for _, c := range []struct {
		name    string
		probe   AdmissionProbe
		level   Level
		line    string
		more    []string
		pending bool
	}{
		{"admitted", AdmissionProbe{Site: "home-a", Found: true, Record: admittedRecord(cfg, "home-a")}, OK,
			"home-a: admitted as ubuntu, covering its roles", nil, false},
		{"not admitted, pending", AdmissionProbe{Site: "home-b", Class: pendingClass("home-b")}, Warn,
			"home-b: pending: this deployment is not admitted on its host",
			[]string{"roles: data, apps", "Until it joins, the deployment is without one of 3 etcd votes", "`paisans host request --site home-b > home-b.admit`"}, true},
		{"without the watchdog, pending", AdmissionProbe{Site: "home-b", Found: true, Record: noWatchdog, Class: pendingClass("home-b")}, Warn,
			"home-b: pending: its admission does not cover it",
			[]string{"the site's roles need --watchdog, which the host's owner did not grant", "--watchdog grants Patroni the machine's watchdog"}, true},
		{"gateway without the ports", AdmissionProbe{Site: "vm", Found: true, Record: noPorts, Class: pendingClass("vm")}, Fail,
			"vm: pending: its admission does not cover it",
			[]string{"--public-ports lets the deployment bind ports 80 and 443", "its gateway"}, true},
		{"differs, joined", AdmissionProbe{Site: "home-a", Found: true, Record: moved, Unclassified: joined}, Fail,
			"home-a: its admission does not cover it",
			[]string{"the host opened ssh port 2222, and the site's is 22", "The run's classification of the sites refuses: home-a has joined", "`paisans host request --site home-a > home-a.admit`"}, false},
		{"unreadable", AdmissionProbe{Site: "home-a", Err: `the admission record for "f2a9" is refused:`, More: []string{"- version: \"2\""}}, Fail,
			"home-a: its admission record cannot be read",
			[]string{`- version: "2"`, "`paisans host request --site home-a > home-a.admit`"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fs := Admission(cfg, []AdmissionProbe{c.probe})
			if len(fs) != 1 {
				t.Fatalf("want one finding:\n%s", dump(fs))
			}
			f := fs[0]
			text := strings.Join(f.More, "\n")
			if f.Level != c.level || f.Line != c.line {
				t.Errorf("got %v %q", f.Level, f.Line)
			}
			for _, want := range c.more {
				if !strings.Contains(text, want) {
					t.Errorf("lacks %q:\n%s", want, text)
				}
			}
			if got := c.probe.Pending(); got != c.pending {
				t.Errorf("Pending %v, want %v", got, c.pending)
			}
		})
	}
}
```

Run: `go test ./internal/doctor/ -run TestAdmission`
Expected: FAIL to compile, `undefined: AdmissionProbe`, `undefined: Admission`.

- [ ] **Step 2: Write the judge**

Create `internal/doctor/admission.go`:

```go
package doctor

import (
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/topology"
)

// SectionAdmission is each site's admission record against its
// declaration, read right after reach: every other read of the site needs
// it.
const SectionAdmission = "admission"

// AdmissionProbe is the site's admission record, read as the login user:
// it is mode 644, so the deployment's user reads it without sudo.
type AdmissionProbe struct {
	Site string
	// Found is whether the host holds a record for this deployment.
	Found  bool
	Record admission.Record
	// Err is why a record could not be read or was refused, one line, and
	// More the rest of it.
	Err  string
	More []string
	// Class is the run's classification of the site, asked only when the
	// record does not cover it; Unclassified is why the classification
	// refused, when it did.
	Class        topology.Site
	Unclassified string
}

func (p AdmissionProbe) site() string { return p.Site }

// Covered reports whether the record was read and covers the site as cfg
// declares it: the same test every command that writes to a host makes
// (admission.ForSite).
func (p AdmissionProbe) Covered(cfg *config.Config) bool {
	return p.Err == "" && p.Found && len(p.Record.Differences(cfg, p.Site)) == 0
}

// Pending reports whether the run's classification left the site out as
// pending: nothing more is asked of it.
func (p AdmissionProbe) Pending() bool { return p.Class.Class == topology.Pending }

// grantWhy is what each grant gives, by the flag that gives it.
var grantWhy = map[string]string{
	"watchdog":     "--watchdog grants Patroni the machine's watchdog, which a data site needs to fail over safely, and which can reboot the machine.",
	"public-ports": "--public-ports lets the deployment bind ports 80 and 443, which the gateway and a monitor running Paisans' Caddy serve on; every local user can then bind ports below 1024.",
}

// Admission checks each site's admission record against its declaration. A
// site the record does not cover (none, unreadable, or differing, a
// missing grant included) is pending when the run's classification says
// so: the deployment runs without it, so it is a warning naming what it is
// without and how its owner admits it (a failure on a gateway,
// absentLevel). Any other site the record does not cover is a failure,
// with the classification's refusal: the deployment has joined it, or
// cannot tell, and apply stops until its owner admits it again.
func Admission(cfg *config.Config, probes []AdmissionProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		if p.Covered(cfg) {
			out = append(out, Finding{Section: SectionAdmission, Level: OK, Line: fmt.Sprintf("%s: admitted as %s, covering its roles", p.Site, p.Record.User)})
			continue
		}
		var problem string
		var more []string
		switch {
		case p.Err != "":
			problem = "its admission record cannot be read"
			more = append([]string{p.Err}, p.More...)
		case !p.Found:
			problem = "this deployment is not admitted on its host"
			more = []string{fmt.Sprintf("Its login as %s works, and the host holds no admission record for this deployment: its owner has not admitted it, or has evicted it.", cfg.Sites[p.Site].SSHUser())}
		default:
			problem = "its admission does not cover it"
			more = p.Record.Differences(cfg, p.Site)
			for _, g := range admission.Needs(cfg.Sites[p.Site]).Missing(p.Record.Grants) {
				more = append(more, grantWhy[g])
			}
		}
		if p.Pending() {
			out = append(out, pending(cfg, SectionAdmission, p.Site, fmt.Sprintf("%s: pending: %s", p.Site, problem), more...))
			continue
		}
		if p.Unclassified != "" {
			more = append(more, "The run's classification of the sites refuses: "+p.Unclassified)
		}
		more = append(more, "recover:", requestAdvice(p.Site))
		out = append(out, Finding{Section: SectionAdmission, Level: Fail, Line: fmt.Sprintf("%s: %s", p.Site, problem), More: more})
	}
	return out
}

// pending is a site the deployment runs without because its host has not
// admitted it, or not as declared: its roles, why, what the deployment is
// without until it joins, and how its owner admits it.
func pending(cfg *config.Config, section, site, line string, why ...string) Finding {
	more := append([]string{roleLine(cfg, site)}, why...)
	if lost := Losses(cfg, site); lost != "" {
		more = append(more, "Until it joins, the deployment is without "+lost+".")
	}
	more = append(more, "recover:", requestAdvice(site))
	return Finding{Section: section, Level: absentLevel(cfg, site), Line: line, More: more}
}

// absentLevel is how bad a site's absence is: a warning, since the
// deployment runs on the sites it has, and a failure for a gateway, since
// apply refuses to run without one.
func absentLevel(cfg *config.Config, site string) Level {
	if cfg.Sites[site].Has(config.RoleGateway) {
		return Fail
	}
	return Warn
}

// requestAdvice is how a site's owner admits it, or admits it again, in
// the words apply's refusal uses.
func requestAdvice(site string) string {
	return fmt.Sprintf("  1. `paisans host request --site %[1]s > %[1]s.admit` writes the request; its owner runs `sudo paisans host admit --request %[1]s.admit` on the host, granting what it asks for, and the next `paisans apply` joins it.", site)
}
```

- [ ] **Step 3: Run it to verify it passes**

Run: `go test ./internal/doctor/ -run TestAdmission && go vet ./internal/doctor/`
Expected: PASS. Should the `admitted` row show a difference, read `admission.Build` and `NewRecord` for that site: a difference there is a bug in the helper, never in `Differences`.

- [ ] **Step 4: Commit**

```bash
git add internal/doctor/admission.go internal/doctor/admission_test.go
git commit -m "feat: judge a site's admission record against its declaration

The record is what the host's owner granted and opened; the declaration
is what the deployment now needs. A site the record does not cover is
pending when the deployment has not joined it, which the run's
classification decides, never the site's own host: doctor will say so
as a warning with what the deployment is without and the host request
line, and a failure for a gateway, which apply cannot run without. A
joined site the record no longer covers is a failure, since apply stops
on it. The comparison is the one every command that writes to a host
makes.

Nothing an operator would notice yet, since no check runs it, so no
changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: The self-check, for the gateway and a monitor running Caddy

**Files:**
- Modify: `internal/selfcheck/selfcheck.go` (`TargetFor`, the package comment)
- Modify: `internal/selfcheck/selfcheck_test.go` (the echo row)
- Create: `internal/selfcheck/target_test.go`
- Create: `internal/doctor/selfcheck.go`, `internal/doctor/selfcheck_test.go`

**Interfaces:**
- Consumes: `selfcheck.TargetFor`, `selfcheck.Path`, `selfcheck.Conflict`; `dns.GatewayHostnames`, `dns.ServedHostnames(cfg, site)`; `config.Site.RunsCaddy()`.
- Produces: `selfcheck.TargetFor` answering for a monitor that runs the toolkit's Caddy; in `internal/doctor`: `const SectionSelfCheck = "self-check"`; `type SelfCheckProbe struct { Site, Host, Address, Unchecked string; Conflict int; Err string }` with `site()`; `func SelfChecks(cfg *config.Config, probes []SelfCheckProbe) []Finding`.

- [ ] **Step 1: Pin the echo, and the monitor's target**

In `internal/selfcheck/selfcheck_test.go`, `TestSelfCheckRefusesAnotherServer`, add a row to the map:

```go
		"an echo of the path": {https: []result{{a: selfcheck.Answer{Status: 200, Body: "/.well-known/paisans/f2a9\n"}}}, http: []result{redirect}},
```

Create `internal/selfcheck/target_test.go`:

```go
package selfcheck_test

import (
	"path/filepath"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/selfcheck"
)

// A gateway is asked for a hostname it serves for the deployment, a
// monitor running the toolkit's Caddy for one of its own; a site with no
// Caddy of the toolkit's, or no public_address, has no target.
func TestTargetForTheGatewayAndAMonitor(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range []string{"home-a", "vm"} {
		if got, ok := selfcheck.TargetFor(cfg, site); ok {
			t.Errorf("%s: %+v", site, got)
		}
	}
	got, ok := selfcheck.TargetFor(cfg, "watch")
	want := selfcheck.Target{Deployment: cfg.Deployment(), Site: "watch", Address: "203.0.113.20", Host: "health.example.org"}
	if !ok || got != want {
		t.Errorf("watch: %+v, %v", got, ok)
	}

	vm := cfg.Sites["vm"]
	vm.PublicAddress = "203.0.113.30"
	cfg.Sites["vm"] = vm
	got, ok = selfcheck.TargetFor(cfg, "vm")
	if !ok || got.Address != "203.0.113.30" || got.Host != dns.GatewayHostnames(cfg)[0].Name {
		t.Errorf("vm: %+v, %v", got, ok)
	}

	watch := cfg.Sites["watch"]
	watch.WebServer = &config.WebServer{ManagedBy: config.ManagedByExternal}
	cfg.Sites["watch"] = watch
	if got, ok := selfcheck.TargetFor(cfg, "watch"); ok {
		t.Errorf("a monitor behind the owner's web server: %+v", got)
	}
}
```

Run: `go test ./internal/selfcheck/`
Expected: the echo row PASSES (`Check` compares the trimmed body with the token exactly); `TestTargetForTheGatewayAndAMonitor` FAILS on `watch`. Should the echo row fail, the check accepts a body holding the token among other text: make it compare `strings.TrimSpace(a.Body)` with `Body(t.Deployment)` exactly, which fixes `apply`'s step as well.

- [ ] **Step 2: A target for a monitor running Caddy**

In `internal/selfcheck/selfcheck.go`, replace the package comment's first sentence with "Package selfcheck asks a site that runs the toolkit's Caddy, from outside, whether it answers as this deployment." and its last sentence with "That Caddy answers /.well-known/paisans/<token> with the token; anything else that answers there is another server." Replace `TargetFor`:

```go
// TargetFor is site's check: its public_address, and the first hostname its
// Caddy serves, a gateway's for the deployment and a monitor's its own.
// false for a site that runs no Caddy of the toolkit's, one with no
// public_address to ask through, or one with no hostname to ask for.
func TargetFor(cfg *config.Config, site string) (Target, bool) {
	s, ok := cfg.Sites[site]
	if !ok || !s.RunsCaddy() || s.PublicAddress == "" {
		return Target{}, false
	}
	names := dns.GatewayHostnames(cfg)
	if !s.Has(config.RoleGateway) {
		names = dns.ServedHostnames(cfg, site)
	}
	if len(names) == 0 {
		return Target{}, false
	}
	return Target{Deployment: cfg.Deployment(), Site: site, Address: s.PublicAddress, Host: names[0].Name}, true
}
```

`apply`'s step (`checkGatewayAnswers`) asks only a gateway, so nothing it does changes.

Run: `go test ./internal/selfcheck/ ./cmd/paisans/ -run 'SelfCheck|TargetFor|Gateway'`
Expected: PASS.

- [ ] **Step 3: Write the failing doctor test**

Create `internal/doctor/selfcheck_test.go`:

```go
package doctor

import (
	"strings"
	"testing"
)

// ourCaddy is a site that answered as this deployment.
var ourCaddy = SelfCheckProbe{Site: "watch", Host: "health.example.org", Address: "203.0.113.20"}

func TestSelfChecks(t *testing.T) {
	cfg := fixture(t)
	conflict := ourCaddy
	conflict.Conflict, conflict.Err = 443, `443 on watch answers, but not as this deployment: status 200, body "hello"`
	silent := ourCaddy
	silent.Err = "443 on watch does not answer https://health.example.org/.well-known/paisans/f2a9 through 203.0.113.20: connection refused"
	fs := SelfChecks(cfg, []SelfCheckProbe{ourCaddy, {Site: "vm", Unchecked: "no public_address to ask through"}, conflict, silent})
	if len(fs) != 4 {
		t.Fatalf("want four findings:\n%s", dump(fs))
	}
	for i, c := range []struct {
		level Level
		line  string
		more  []string
	}{
		{OK, "watch: answers as this deployment on 443 and 80, asked for health.example.org through 203.0.113.20", nil},
		{Warn, "vm: not asked from outside: no public_address to ask through", []string{"/.well-known/paisans/f2a9"}},
		{Fail, `watch: 443 on watch answers, but not as this deployment: status 200, body "hello"`, []string{"/.well-known/paisans/f2a9", "`sudo ss -Hltnp`", "web_server.managed_by: external", "`paisans gateway move`"}},
		{Fail, "watch: 443 on watch does not answer", []string{"hairpin NAT"}},
	} {
		f := fs[i]
		if f.Level != c.level || !strings.HasPrefix(f.Line, c.line) {
			t.Errorf("%d: got %v %q", i, f.Level, f.Line)
		}
		text := strings.Join(f.More, "\n")
		for _, want := range c.more {
			if !strings.Contains(text, want) {
				t.Errorf("%d lacks %q:\n%s", i, want, text)
			}
		}
	}
}
```

Run: `go test ./internal/doctor/ -run TestSelfChecks`
Expected: FAIL to compile, `undefined: SelfChecks`.

- [ ] **Step 4: Write the judge**

Create `internal/doctor/selfcheck.go`:

```go
package doctor

import (
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/selfcheck"
)

// SectionSelfCheck is each Caddy site's own answer, asked from outside,
// after everything read on the sites.
const SectionSelfCheck = "self-check"

// SelfCheckProbe is whether a site running the toolkit's Caddy answered
// /.well-known/paisans/<token> as this deployment, fetched from the
// workstation with the check apply runs at the end of the gateway's step.
// Host and Address are what was asked; Unchecked is why nothing was. Err
// is the check's error, and Conflict the port that answered as something
// else, 0 when nothing answered.
type SelfCheckProbe struct {
	Site, Host, Address, Unchecked string
	Conflict                       int
	Err                            string
}

func (p SelfCheckProbe) site() string { return p.Site }

// SelfChecks judges each site's answer. The deployment's user cannot read
// the host's ruleset or another user's sockets, so a port taken after
// admission, and above all one taken by a DNAT rule with no listener, is
// invisible from the host; asking from outside is the one way to see it.
func SelfChecks(cfg *config.Config, probes []SelfCheckProbe) []Finding {
	path := selfcheck.Path(cfg.Deployment())
	var out []Finding
	for _, p := range probes {
		switch {
		case p.Unchecked != "":
			out = append(out, Finding{Section: SectionSelfCheck, Level: Warn,
				Line: fmt.Sprintf("%s: not asked from outside: %s", p.Site, p.Unchecked),
				More: []string{fmt.Sprintf("doctor asks each site that runs the toolkit's Caddy for %s through its public_address, as apply asks the gateway; without it, another server holding 443 or 80 there goes unseen.", path)}})
		case p.Err == "":
			out = append(out, Finding{Section: SectionSelfCheck, Level: OK,
				Line: fmt.Sprintf("%s: answers as this deployment on 443 and 80, asked for %s through %s", p.Site, p.Host, p.Address)})
		case p.Conflict != 0:
			out = append(out, Finding{Section: SectionSelfCheck, Level: Fail,
				Line: fmt.Sprintf("%s: %s", p.Site, firstLine(p.Err)),
				More: []string{
					fmt.Sprintf("doctor asked %s for https://%s%s from this workstation, as apply does at the end of the gateway's step, and another server answered on %d: a listener of the host owner's, a container of their system Docker, or a DNAT rule, which only root on the host can see.", p.Address, p.Host, path, p.Conflict),
					"recover:",
					fmt.Sprintf("  1. The host's owner finds what holds %d on %s: `sudo ss -Hltnp`, `sudo docker ps` for the system daemon, and `sudo nft list ruleset` for a DNAT rule.", p.Conflict, p.Site),
					"  2. They free the port; or the site runs behind their web server (web_server.managed_by: external, on a monitor), or the gateway moves to another site with `paisans gateway move`.",
					fmt.Sprintf("  3. `paisans doctor --site %s` again.", p.Site),
				}})
		default:
			out = append(out, Finding{Section: SectionSelfCheck, Level: Fail,
				Line: fmt.Sprintf("%s: %s", p.Site, firstLine(p.Err)),
				More: []string{
					fmt.Sprintf("doctor asked %s for https://%s%s from this workstation, once, and nothing answered.", p.Address, p.Host, path),
					"When the site is at home behind NAT and doctor runs from the same network, the router has to send its own public address back inside (hairpin NAT, or NAT loopback); many do not, and then doctor is run from outside that network.",
					fmt.Sprintf("Otherwise check that the router or the provider's firewall forwards 80 and 443 to %s, that public_address is the address they arrive on, and that its Caddy runs (containers %s).", p.Site, p.Site),
				}})
		}
	}
	return out
}
```

Run: `go test ./internal/doctor/ -run TestSelfChecks && go vet ./internal/doctor/ ./internal/selfcheck/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/selfcheck internal/doctor/selfcheck.go internal/doctor/selfcheck_test.go
git commit -m "feat: judge the self-check of the gateway and a monitor running caddy

A monitor that serves its own hostname runs the toolkit's Caddy on 80
and 443 as the gateway does, so a port another server took there after
admission is as invisible from the host. The self-check now has a target
for it, its own first hostname, and doctor's judge words each answer:
this deployment, another server, nothing, or a site with no public
address to ask through. A server that echoes the path is pinned as
another server.

Nothing an operator would notice yet: apply asks only the gateway, and
no doctor check runs the judge. No changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: The rootless checks in the graph

**Files:**
- Modify: `internal/doctor/checks.go`, `internal/doctor/doctor.go`, `internal/doctor/graph.go`, `internal/doctor/dnschecks.go` (`addDNS`'s needs)
- Modify: `internal/doctor/graph_test.go`
- Modify: `cmd/paisans/doctor.go`
- Modify: `cmd/paisans/doctor_test.go`, `cmd/paisans/doctor_dns_test.go`, `cmd/paisans/output_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: Tasks 2 to 4's probes and judges; `admission.Read(Reader, token string) (Record, error)`, `admission.ErrNotAdmitted`; `doctorReads.read`; the graph's `Node`, `Hard`, `bySite`; in tests, `admittedRecord(t, cfg, site) string` and `admittedPath(cfg) string` (`cmd/paisans/admitted_test.go`).
- Produces: `doctor.Reads` gains `Admission(site string) AdmissionProbe`, `User(site string) UserProbe`, `Docker(site string) DockerProbe`, `Linger(site string) LingerProbe`, `Sudo(site string) SudoProbe`; `doctor.Input` gains `Admission []AdmissionProbe`, `Users []UserProbe`, `Docker []DockerProbe`, `Linger []LingerProbe`, `Sudo []SudoProbe`; nodes `admission <site>`, `user <site>`, `docker <site>`, `docker engine <site>`, `linger <site>`, `sudo rights <site>`. In `cmd/paisans` tests: `doctorHost.files map[string]string`, `func admitHosts(t *testing.T, cfg *config.Config, hosts map[string]doctorHost)`, `func withDoctorHosts(t *testing.T, cfg *config.Config, hosts map[string]doctorHost)`, `func sudoHelperOnly(user, host, helper string) string`.

- [ ] **Step 1: Teach the test reads the new questions**

In `internal/doctor/graph_test.go`, add to `fixtureReads`, below `Leftovers`. Each answers from the fixture's `Input` when the site is there, and otherwise as a sound admitted host:

```go
// fixtureConfig is the fixture the reads answer from, loaded once.
var fixtureConfig = sync.OnceValue(func() *config.Config {
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		panic(err)
	}
	return cfg
})

func (f *fixtureReads) Admission(site string) AdmissionProbe {
	f.asked("admission " + site)
	for _, p := range f.in.Admission {
		if p.Site == site {
			return p
		}
	}
	return AdmissionProbe{Site: site, Found: true, Record: admittedRecord(fixtureConfig(), site)}
}

func (f *fixtureReads) User(site string) UserProbe {
	f.asked("user " + site)
	for _, p := range f.in.Users {
		if p.Site == site {
			return p
		}
	}
	return UserProbe{Site: site, Out: "uid 1000\nuser ubuntu\ngroups ubuntu\n"}
}

func (f *fixtureReads) Docker(site string) DockerProbe {
	f.asked("docker " + site)
	for _, p := range f.in.Docker {
		if p.Site == site {
			return p
		}
	}
	return DockerProbe{Site: site, Out: rootlessDocker}
}

func (f *fixtureReads) Linger(site string) LingerProbe {
	f.asked("linger " + site)
	for _, p := range f.in.Linger {
		if p.Site == site {
			return p
		}
	}
	return LingerProbe{Site: site, Out: "linger yes\nmesh-order yes\n"}
}

func (f *fixtureReads) Sudo(site string) SudoProbe {
	f.asked("sudo " + site)
	for _, p := range f.in.Sudo {
		if p.Site == site {
			return p
		}
	}
	return SudoProbe{Site: site, Out: sudoList(helperEntry)}
}
```

Add `"path/filepath"`, `"github.com/paisans-software/paisans-cli/internal/config"` and `"github.com/paisans-software/paisans-cli/internal/topology"` to its imports (`sync` is there already).

- [ ] **Step 2: Write the failing graph tests**

In `TestBoundOfOneRunsInOrder`, replace `want` with:

```go
	want := []string{
		"reach home-a", "reach home-b", "reach vm",
		"admission home-a", "admission home-b", "admission vm",
		"user home-a", "user home-b", "user vm",
		"docker home-a", "docker home-b", "docker vm",
		"linger home-a", "linger home-b", "linger vm",
		"sudo home-a", "sudo home-b", "sudo vm",
		"mesh home-a", "mesh home-b", "mesh vm",
		"etcd health home-a", "etcd version home-a",
		"patroni home-a", "patroni home-b", "sync home-a",
		"containers home-a", "containers home-b", "containers vm",
		"settings home-a talk", "settings home-b talk",
		"standby auth",
		"clock home-a", "clock home-b", "clock vm",
		"leftovers home-a", "leftovers home-b", "leftovers vm",
	}
```

In `TestPruningIsReportedOnce`, replace the `want` sentence and the count check:

```go
	want := "Not checked because of this: admission, user, docker, docker engine, linger, sudo rights, mesh, patroni, containers, settings talk, clocks, leftovers on home-b."
```

```go
	if report.Pruned != 13 || listed != report.Pruned || report.Count(Skip) != 1 || !strings.Contains(report.Summary(), "14 skipped") {
```

Add, below `TestPruningIsReportedOnce`:

```go
// A site that answers but is pending is one warning, with the host request
// line, and nothing more is read from it.
func TestAPendingSiteIsCheckedNoFurther(t *testing.T) {
	cfg := fixture(t)
	fix := healthyInput()
	fix.Admission = []AdmissionProbe{{Site: "home-b", Class: topology.Site{Name: "home-b", Class: topology.Pending, Why: "home-b: its host has not admitted this deployment", Request: true}}}
	reads := &fixtureReads{in: fix, log: &events{}}
	rec := &ui.Recorder{}
	report, _ := Checks(cfg, fix.Sites, reads).Run(rec, 0)

	f := find(t, report.Findings, SectionAdmission, "home-b: pending: this deployment is not admitted on its host")
	if f.Level != Warn || !strings.Contains(strings.Join(f.More, "\n"), "`paisans host request --site home-b > home-b.admit`") {
		t.Errorf("home-b: %+v", f)
	}
	want := "Not checked because of this: user, docker, docker engine, linger, sudo rights, mesh, patroni, containers, settings talk, clocks, leftovers on home-b."
	if !rec.Has("explain", want) {
		t.Errorf("admission home-b does not say what it kept from running, want %q:\n%s", want, rec.Lines())
	}
	for _, read := range reads.log.list {
		if strings.HasSuffix(read, " home-b") && read != "reach home-b" && read != "admission home-b" {
			t.Errorf("read home-b after it was found pending: %s", read)
		}
	}
	for _, f := range report.Findings {
		if strings.HasPrefix(f.Line, "home-b:") && f.Level == Fail {
			t.Errorf("a pending data site failed a check: %+v", f)
		}
	}
}

// A daemon that does not answer stops what runs docker on its site, and
// nothing else there.
func TestDockerDownStopsWhatRunsDocker(t *testing.T) {
	cfg := fixture(t)
	fix := healthyInput()
	fix.Docker = []DockerProbe{{Site: "home-a", Out: "host unix:///run/user/1000/docker.sock\n", Err: "Cannot connect to the Docker daemon"}}
	reads := &fixtureReads{in: fix, log: &events{}}
	rec := &ui.Recorder{}
	Checks(cfg, fix.Sites, reads).Run(rec, 0)

	for _, read := range []string{"containers home-a", "settings home-a talk", "leftovers home-a", "patroni home-a"} {
		if reads.log.has(read) {
			t.Errorf("%s ran although home-a's Docker did not answer", read)
		}
	}
	for _, read := range []string{"linger home-a", "sudo home-a", "mesh home-a", "clock home-a", "containers home-b"} {
		if !reads.log.has(read) {
			t.Errorf("%s did not run", read)
		}
	}
	if !rec.Has("explain", "Not checked because of this: docker engine, patroni, containers, settings talk, leftovers on home-a.") {
		t.Errorf("docker home-a does not say what it kept from running:\n%s", rec.Lines())
	}
}
```

Run: `go test ./internal/doctor/ -run 'TestBoundOfOne|TestPruningIsReportedOnce|TestAPendingSite|TestDockerDown|TestGraph'`
Expected: FAIL to compile, `f.in.Admission undefined`, then, once Step 3's fields exist, FAIL on the read order and the pruning lists.

- [ ] **Step 3: The probes in the Input, cloned and judged**

In `internal/doctor/doctor.go`, add to `Input`, after `Reach`:

```go
	// Admission, Users, Docker, Linger and Sudo are each site's rootless
	// checks (admission.go, rootless.go, sudo.go).
	Admission []AdmissionProbe
	Users     []UserProbe
	Docker    []DockerProbe
	Linger    []LingerProbe
	Sudo      []SudoProbe
```

Replace `sections` with:

```go
var sections = []string{SectionReach, SectionAdmission, SectionUser, SectionDocker, SectionLinger, SectionSudo, SectionMesh, SectionEtcd, SectionPatroni, SectionContainers, SectionSettings, SectionPocketID, SectionClocks, SectionLeftovers, SectionCaddy, SectionSecrets, SectionToken, SectionRecords}
```

In `Diagnose`, after the `Reach` line:

```go
	findings = append(findings, Admission(cfg, in.Admission)...)
	findings = append(findings, User(in.Users)...)
	findings = append(findings, DockerSocket(in.Docker)...)
	findings = append(findings, DockerEngine(in.Docker)...)
	findings = append(findings, Linger(cfg.Deployment(), in.Linger)...)
	findings = append(findings, SudoRights(cfg, in.Sudo)...)
```

In `internal/doctor/graph.go`, `clone`, after `out.Reach = ...`:

```go
	out.Admission = slices.Clone(in.Admission)
	out.Users = slices.Clone(in.Users)
	out.Docker = slices.Clone(in.Docker)
	out.Linger = slices.Clone(in.Linger)
	out.Sudo = slices.Clone(in.Sudo)
```

- [ ] **Step 4: The reads and the nodes**

In `internal/doctor/checks.go`, add to `Reads`, after `Reach`:

```go
	// Admission reads the site's admission record, as the login user.
	Admission(site string) AdmissionProbe
	// User runs UserCommand on the site.
	User(site string) UserProbe
	// Docker runs DockerCommand on the site.
	Docker(site string) DockerProbe
	// Linger runs LingerCommand on the site.
	Linger(site string) LingerProbe
	// Sudo runs SudoCommand on the site.
	Sudo(site string) SudoProbe
```

Rewrite `Checks`' comment's first paragraph:

```go
// Checks is every check doctor makes on sites, the ones it asks, as a
// graph: each site's reach first, then its admission record, which every
// other read of the site needs, since nothing is asked of a pending site;
// then the deployment's user, its rootless Docker, which every check that
// runs docker there needs, linger and sudo; then from the bottom of the
// stack up, the order Diagnose reports in. Each check's judge is the
// section function Diagnose calls, given the probes of the check's own
// subject, so the graph's findings for a complete Input are Diagnose's.
```

After the `reach` loop, add:

```go
	admitted := func(site string) string { return SectionAdmission + " " + site }
	docker := func(site string) string { return SectionDocker + " " + site }
	for _, site := range sites {
		g.Add(Node{Name: admitted(site), Section: SectionAdmission, Site: site, Subject: site,
			Hard: []Need{Hard(reach(site))},
			Gather: func(Input) func(*Input) {
				p := reads.Admission(site)
				return func(in *Input) { in.Admission = append(in.Admission, p) }
			},
			Outcome: func(in Input) Outcome {
				for _, p := range bySite(in.Admission, site, AdmissionProbe.site) {
					if p.Pending() {
						return Failed
					}
				}
				return Passed
			},
			Judge: func(in Input) []Finding { return Admission(cfg, bySite(in.Admission, site, AdmissionProbe.site)) },
		})
	}
	for _, site := range sites {
		g.Add(Node{Name: SectionUser + " " + site, Section: SectionUser, Site: site, Subject: site,
			Hard: []Need{Hard(admitted(site))},
			Gather: func(Input) func(*Input) {
				p := reads.User(site)
				return func(in *Input) { in.Users = append(in.Users, p) }
			},
			Judge: func(in Input) []Finding { return User(bySite(in.Users, site, UserProbe.site)) },
		})
	}
	for _, site := range sites {
		g.Add(Node{Name: docker(site), Section: SectionDocker, Site: site, Subject: site,
			Hard: []Need{Hard(admitted(site))},
			Gather: func(Input) func(*Input) {
				p := reads.Docker(site)
				return func(in *Input) { in.Docker = append(in.Docker, p) }
			},
			Outcome: func(in Input) Outcome {
				for _, p := range bySite(in.Docker, site, DockerProbe.site) {
					if p.Answered() {
						return Passed
					}
				}
				return Failed
			},
			Judge: func(in Input) []Finding { return DockerSocket(bySite(in.Docker, site, DockerProbe.site)) },
		})
	}
	for _, site := range sites {
		g.Add(Node{Name: SectionDocker + " engine " + site, Section: SectionDocker, Site: site, Subject: site,
			Hard:  []Need{Hard(docker(site))},
			Judge: func(in Input) []Finding { return DockerEngine(bySite(in.Docker, site, DockerProbe.site)) },
		})
	}
	for _, site := range sites {
		g.Add(Node{Name: SectionLinger + " " + site, Section: SectionLinger, Site: site, Subject: site,
			Hard: []Need{Hard(admitted(site))},
			Gather: func(Input) func(*Input) {
				p := reads.Linger(site)
				return func(in *Input) { in.Linger = append(in.Linger, p) }
			},
			Judge: func(in Input) []Finding { return Linger(d, bySite(in.Linger, site, LingerProbe.site)) },
		})
	}
	for _, site := range sites {
		g.Add(Node{Name: SectionSudo + " " + site, Section: SectionSudo, Site: site, Subject: site,
			Hard: []Need{Hard(admitted(site))},
			Gather: func(Input) func(*Input) {
				p := reads.Sudo(site)
				return func(in *Input) { in.Sudo = append(in.Sudo, p) }
			},
			Judge: func(in Input) []Finding { return SudoRights(cfg, bySite(in.Sudo, site, SudoProbe.site)) },
		})
	}
```

Then change the existing nodes' hard needs:

* `mesh <site>`, `clocks <site>`: `Hard: []Need{Hard(reach(site))}` becomes `Hard: []Need{Hard(admitted(site))}`.
* `patroni <site>`, `containers <site>`, `settings <site> <app>`, `leftovers <site>`: `Hard: []Need{Hard(reach(site))}` becomes `Hard: []Need{Hard(docker(site))}`.
* `etcd`, `etcd version`, the `patroni` aggregate and `pocket-id <app>` keep theirs.

In `internal/doctor/dnschecks.go`, `addDNS`: the `caddy certificates <site>` node's `Hard(SectionReach + " " + site)` becomes `Hard(SectionDocker + " " + site)`, since it runs `docker compose`; the `dns token <site>` node's becomes `Hard(SectionAdmission + " " + site)`.

Run: `go test ./internal/doctor/`
Expected: PASS, including `TestGraphFindsWhatDiagnoseFinds` (its inputs hold no rootless probes, so neither side reports any), `TestGraphRunReportsItsInput` and `TestStreamingPrintsAsChecksEnd`. Where a "Not checked" list differs from what the run prints, compare it with the node order above: the test is right when it names every node a failed need cut, in the graph's order. In `dnschecks_test.go`, a list naming `caddy certificates` or `dns token` under a failed reach now names them after `admission` and `docker`; correct it the same way.

- [ ] **Step 5: The reads over each site's transport**

In `cmd/paisans/doctor.go`, add below `Reach`:

```go
// Admission reads the site's admission record as the login user, who may:
// it is root's, mode 644, in a directory every user lists.
func (d doctorReads) Admission(site string) doctor.AdmissionProbe {
	p := doctor.AdmissionProbe{Site: site}
	rec, err := admission.Read(d.transports[site], d.cfg.Deployment().Token())
	switch {
	case errors.Is(err, admission.ErrNotAdmitted):
	case err != nil:
		lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
		p.Err, p.More = lines[0], lines[1:]
	default:
		p.Found, p.Record = true, rec
	}
	return p
}

func (d doctorReads) User(site string) doctor.UserProbe {
	out, failure := d.read(site, doctor.UserCommand)
	return doctor.UserProbe{Site: site, Out: out, Err: failure}
}

func (d doctorReads) Docker(site string) doctor.DockerProbe {
	out, failure := d.read(site, doctor.DockerCommand)
	return doctor.DockerProbe{Site: site, Out: out, Err: failure}
}

func (d doctorReads) Linger(site string) doctor.LingerProbe {
	out, failure := d.read(site, doctor.LingerCommand)
	return doctor.LingerProbe{Site: site, Out: out, Err: failure}
}

func (d doctorReads) Sudo(site string) doctor.SudoProbe {
	out, failure := d.read(site, doctor.SudoCommand)
	return doctor.SudoProbe{Site: site, Out: out, Err: failure}
}
```

Add `"github.com/paisans-software/paisans-cli/internal/admission"` to its imports. Update `doctorReads`' comment: "Every command here is one of internal/doctor's read commands, patroni's ClusterCommand, apply.LookAtInstances' question, or the host check's inventory (hostcheck.Inspect), and it reads the admission record; none writes."

- [ ] **Step 6: Admit the fake hosts in the command tests**

In `cmd/paisans/doctor_test.go`, give `doctorHost` a `files map[string]string` field, after `answers`, and make `ReadFile` answer from it:

```go
func (h doctorHost) ReadFile(path string) (string, bool, error) {
	*h.sent = append(*h.sent, h.name+": read "+path)
	if h.down {
		return "", false, fmt.Errorf("%w after 3 attempts: exit status 255", apply.ErrUnreachable)
	}
	content, ok := h.files[path]
	return content, ok, nil
}
```

Add, below `readOnly`:

```go
// sudoHelperOnly is sudo -l's answer for a user host admit gave the helper
// and nothing else.
func sudoHelperOnly(user, host, helper string) string {
	return "Matching Defaults entries for " + user + " on " + host + ":\n" +
		"    env_reset, mail_badpass, use_pty\n\n" +
		"User " + user + " may run the following commands on " + host + ":\n" +
		"    " + hostadmit.HelperRule(helper) + "\n"
}

// admitHosts makes each fake host one whose owner admitted its site as the
// configuration declares it, with a sound rootless setup, unless the test
// gave it another answer already: the record, a user in no privileged
// group, its own rootless Docker 29.9.1, linger on with the manager after
// the mesh, and sudo for the WireGuard helper alone.
func admitHosts(t *testing.T, cfg *config.Config, hosts map[string]doctorHost) {
	t.Helper()
	helper, err := apply.WGHelper{Iface: cfg.Deployment().Interface()}.Path()
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range cfg.SiteNames() {
		s := cfg.Sites[site]
		h, ok := hosts[s.SSHHost()]
		if !ok {
			continue
		}
		if h.files == nil {
			h.files = map[string]string{}
		}
		if _, given := h.files[admittedPath(cfg)]; !given {
			h.files[admittedPath(cfg)] = admittedRecord(t, cfg, site)
		}
		if h.answers == nil {
			h.answers = map[string]string{}
		}
		for command, answer := range map[string]string{
			doctor.UserCommand:   "uid 1000\nuser " + s.SSHUser() + "\ngroups " + s.SSHUser() + "\n",
			doctor.DockerCommand: "host unix:///run/user/1000/docker.sock\nengine 29.9.1\nsecurity [\"name=seccomp,profile=builtin\",\"name=rootless\",\"name=cgroupns\"]\n",
			doctor.LingerCommand: "linger yes\nmesh-order yes\n",
			doctor.SudoCommand:   sudoHelperOnly(s.SSHUser(), site, helper),
		} {
			if _, given := h.answers[command]; !given {
				h.answers[command] = answer
			}
		}
		hosts[s.SSHHost()] = h
	}
}
```

Change `withDoctorHosts` to take the configuration and admit the hosts first:

```go
func withDoctorHosts(t *testing.T, cfg *config.Config, hosts map[string]doctorHost) {
	t.Helper()
	admitHosts(t, cfg, hosts)
	saved, savedNow, savedAtOnce := doctorTransport, doctorNow, doctorAtOnce
```

(the rest of its body unchanged). Change every call, `withDoctorHosts(t, X)` to `withDoctorHosts(t, cfg, X)`:

Run: `grep -n "withDoctorHosts(t, " cmd/paisans/*_test.go`
Expected: the calls in `doctor_test.go`, `doctor_dns_test.go` and `output_test.go`. In each, pass the `*config.Config` the test already loaded. In `output_test.go`, where the call is `withDoctorHosts(t, stuckHosts(mustLoad(t, cfg), &sent, &writes))`, bind it first: `loaded := mustLoad(t, cfg)`, then `withDoctorHosts(t, loaded, stuckHosts(loaded, &sent, &writes))`.

In `readOnly`, add to `exact`:

```go
		doctor.UserCommand,
		doctor.DockerCommand,
		doctor.LingerCommand,
		doctor.SudoCommand,
```

and change `exact = append(exact, "read "+cfg.Deployment().Manifest())` to:

```go
	exact = append(exact, "read "+cfg.Deployment().Manifest(), "read "+admittedPath(cfg))
```

Add `"github.com/paisans-software/paisans-cli/internal/hostadmit"` to the test file's imports.

- [ ] **Step 7: The command tests' expectations**

In `TestDoctorDiagnosesTheStuckReplicaAndOnlyReads`, replace the `Not checked` expectation:

```go
		"Not checked because of this: admission, user, docker, docker engine, linger, sudo rights, mesh, patroni, containers, settings talk, clocks, leftovers on home-b.",
```

and add to its `want` list:

```go
		"done: check admission home-a admitted as ubuntu, covering its roles",
		"done: check user home-a ubuntu is in no privileged group",
		"done: check docker home-a its rootless Docker answers at unix:///run/user/1000/docker.sock",
		"done: check docker engine home-a Docker Engine 29.9.1",
		"done: check linger home-a its manager starts after the mesh at boot",
		"done: check sudo rights home-a sudo lets ubuntu run the WireGuard helper and nothing else",
```

Run: `go test ./cmd/paisans/ -run 'Doctor|DefaultOutput'`
Expected: PASS. Where another doctor test asserts a summary count ("N checks passed", "N skipped"), recount it: each site that answered adds six passed checks (admission, user, docker, docker engine, linger, sudo rights), and each site that did not adds six skipped. Change the number, never a check's wording. Where a test's host answers a command by prefix, check that no new command matches one of its prefixes: `UserCommand`, `DockerCommand` and `LingerCommand` start `printf 'uid`, `printf 'host` and `printf 'linger`, `SudoCommand` `LC_ALL=C sudo`.

- [ ] **Step 8: The changelog**

In `CHANGELOG.md`, under `[Unreleased]`, `### Added`, add:

```markdown
- `doctor` checks each site as its deployment's user: the admission record, no privileged group, its own rootless Docker at Engine 29.5 or later, linger with the mesh first at boot, and sudo for the WireGuard helper alone.
```

- [ ] **Step 9: Run the touched packages**

Run: `gofmt -l internal/doctor cmd/paisans && go vet ./internal/doctor/ ./cmd/paisans/ && go test ./internal/doctor/ ./cmd/paisans/`
Expected: nothing listed; PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/doctor cmd/paisans CHANGELOG.md
git commit -m "feat: doctor checks each site's admission and rootless setup

Every command runs as the deployment's own user against its own rootless
daemon, so doctor asks first whether that holds: the admission record
against the declaration, the user's groups, its daemon and Engine,
linger and the mesh's place at boot, and what sudo allows. The record
comes first and every other read of a site needs it, so a pending site
is one warning rather than a failure per check; the daemon comes before
every check that runs docker, so a stopped daemon is one failure.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Pending and down sites, from the classification

**Files:**
- Modify: `internal/doctor/reach.go`
- Create: `internal/doctor/reach_test.go`
- Modify: `cmd/paisans/doctor.go`, `cmd/paisans/doctor_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: Task 4's `pending`, `absentLevel`, `requestAdvice`, `AdmissionProbe.Covered`; `roleLine`, `Losses`, `sshReason`; `topology.Gather(cfg, transports) Evidence`, `topology.Classify(cfg, ev) (*Classification, error)`, `topology.Site`, `topology.Pending`, `topology.Down`, `topology.DownLine(site)`.
- Produces: `SiteReach.Class topology.Site`, `SiteReach.Unclassified string`; in `cmd/paisans`: `var doctorClassify func(cfg *config.Config, all map[string]apply.Transport) (*topology.Classification, error)`, `type siteClasses struct`, `func (d doctorReads) class(site string) (topology.Site, string)`, the fields `doctorReads.all func() map[string]apply.Transport` and `doctorReads.classes *siteClasses`.

- [ ] **Step 1: Write the failing tests**

Create `internal/doctor/reach_test.go`:

```go
package doctor

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/topology"
)

// A site that did not answer is a warning once the classification says it
// is pending or down, saying what the deployment is without and what to
// run; a gateway is a failure either way, since apply refuses to run
// without it; a site the classification did not place is a failure, as
// before, with the classification's refusal when there was one. A refused
// login and a timeout read the same.
func TestAbsentSites(t *testing.T) {
	cfg := fixture(t)
	timeout := "ssh: connect to host home-b.local port 22: Operation timed out"
	refused := "ubuntu@home-b.local: Permission denied (publickey)."
	pendingB := topology.Site{Name: "home-b", Class: topology.Pending, Request: true}
	for _, c := range []struct {
		name  string
		reach SiteReach
		level Level
		line  string
		more  []string
	}{
		{"pending", SiteReach{Site: "home-b", Destination: "ubuntu@home-b.local", Err: refused, Class: pendingB}, Warn,
			"home-b: pending: its login is refused or it does not answer, and the deployment has not joined it",
			[]string{"roles: data, apps", "ssh to ubuntu@home-b.local: ubuntu@home-b.local: Permission denied (publickey).", "Until it joins, the deployment is without one of 3 etcd votes", "`paisans host request --site home-b > home-b.admit`"}},
		{"down, timed out", SiteReach{Site: "home-b", Destination: "ubuntu@home-b.local", Err: timeout, Class: topology.Site{Name: "home-b", Class: topology.Down}}, Warn,
			topology.DownLine("home-b"),
			[]string{"While it is down the deployment is without one of 3 etcd votes", "ssh to ubuntu@home-b.local: Operation timed out", "Start the host or fix the route to it."}},
		{"down, refused", SiteReach{Site: "home-b", Destination: "ubuntu@home-b.local", Err: refused, Class: topology.Site{Name: "home-b", Class: topology.Down}}, Warn,
			topology.DownLine("home-b"), nil},
		{"gateway down", SiteReach{Site: "vm", Destination: "ubuntu@vm.example.org", Err: timeout, Class: topology.Site{Name: "vm", Class: topology.Down}}, Fail,
			topology.DownLine("vm"), []string{"its gateway"}},
		{"gateway pending", SiteReach{Site: "vm", Destination: "ubuntu@vm.example.org", Err: refused, Class: topology.Site{Name: "vm", Class: topology.Pending, Request: true}}, Fail,
			"vm: pending: its login is refused or it does not answer, and the deployment has not joined it",
			[]string{"`paisans host request --site vm > vm.admit`"}},
		{"unclassified", SiteReach{Site: "home-b", Destination: "ubuntu@home-b.local", Err: timeout, Unclassified: "home-b: its login is refused or it does not answer, and whether it has joined the deployment cannot be told"}, Fail,
			"home-b: ssh to ubuntu@home-b.local did not answer (Operation timed out)",
			[]string{"Whether it has joined the deployment could not be told: home-b: its login", "Start the host or fix the route to it."}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fs := Reach(cfg, []SiteReach{c.reach})
			if len(fs) != 1 {
				t.Fatalf("want one finding:\n%s", dump(fs))
			}
			f := fs[0]
			if f.Level != c.level || f.Line != c.line {
				t.Errorf("got %v %q", f.Level, f.Line)
			}
			text := strings.Join(f.More, "\n")
			for _, want := range c.more {
				if !strings.Contains(text, want) {
					t.Errorf("lacks %q:\n%s", want, text)
				}
			}
		})
	}
}
```

In `cmd/paisans/doctor_test.go`, add:

```go
// A site that does not answer is classified once, and only then: a run
// where every site answers and is admitted never asks.
func TestDoctorClassifiesOnlyWhenASiteIsNotCovered(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	var sent, writes []string
	withDoctorHosts(t, cfg, stuckHosts(cfg, &sent, &writes))
	// After withDoctorHosts, whose own replacement this one overrides; its
	// cleanup restores the original.
	asked := 0
	doctorClassify = func(c *config.Config, all map[string]apply.Transport) (*topology.Classification, error) {
		asked++
		if len(all) != len(c.Sites) {
			t.Errorf("classification got %d transports, want one per declared site", len(all))
		}
		return &topology.Classification{Sites: map[string]topology.Site{"home-b": {Name: "home-b", Class: topology.Down, Why: topology.DownLine("home-b")}}}, nil
	}
	rec := withRecorder(t, true)
	_ = runDoctor([]string{"--config", fixtureConfig(), "--site", "home-a", "--site", "home-b"})
	if asked != 1 {
		t.Errorf("classified %d times, want once", asked)
	}
	if out := rec.Lines(); !strings.Contains(out, "warned: check reach home-b its login is refused or it does not answer") {
		t.Errorf("home-b is not reported down:\n%s", out)
	}

	asked = 0
	_ = runDoctor([]string{"--config", fixtureConfig(), "--site", "home-a"})
	if asked != 0 {
		t.Errorf("a run where every site is covered classified %d times", asked)
	}
}
```

Add `"github.com/paisans-software/paisans-cli/internal/topology"` to the test file's imports.

Run: `go test ./internal/doctor/ -run TestAbsentSites; go test ./cmd/paisans/ -run TestDoctorClassifies`
Expected: FAIL to compile, `unknown field Class in struct literal of type SiteReach`, `undefined: doctorClassify`.

- [ ] **Step 2: Pending and down in `Reach`**

In `internal/doctor/reach.go`, add to `SiteReach`, after `Err`:

```go
	// Class is the run's classification of a site that did not answer,
	// filled only then (cmd/paisans, doctorReads.Reach); Unclassified is
	// why the classification refused, when it did.
	Class        topology.Site
	Unclassified string
```

Add `"github.com/paisans-software/paisans-cli/internal/topology"` to its imports, and rewrite `Reach` and its comment:

```go
// Reach is one line per site. A site that did not answer is never a crash:
// the rest of the report is made from the sites that did, and this says
// what the deployment is without while it is gone, so an operator can tell
// an inconvenience from an outage at a glance. A site the run's
// classification found pending or down is a warning, since the deployment
// runs on the sites it has; a gateway is a failure, since apply refuses to
// run without it, and so is a site the classification did not place.
func Reach(cfg *config.Config, list []SiteReach) []Finding {
	var out []Finding
	for _, r := range list {
		ssh := "ssh to " + r.Destination + ": " + sshReason(firstLine(r.Err))
		switch {
		case r.Answered():
			out = append(out, Finding{Section: SectionReach, Level: OK, Line: fmt.Sprintf("%s: %s answers", r.Site, r.Destination)})
		case r.Class.Class == topology.Pending:
			out = append(out, pending(cfg, SectionReach, r.Site,
				fmt.Sprintf("%s: pending: its login is refused or it does not answer, and the deployment has not joined it", r.Site), ssh))
		case r.Class.Class == topology.Down:
			more := []string{roleLine(cfg, r.Site)}
			if lost := Losses(cfg, r.Site); lost != "" {
				more = append(more, "While it is down the deployment is without "+lost+".")
			}
			more = append(more, ssh, "recover:", "  1. Start the host or fix the route to it.")
			out = append(out, Finding{Section: SectionReach, Level: absentLevel(cfg, r.Site), Line: topology.DownLine(r.Site), More: more})
		default:
			more := []string{roleLine(cfg, r.Site)}
			if lost := Losses(cfg, r.Site); lost != "" {
				more = append(more, "While it is down the deployment is without "+lost+".")
			}
			if r.Unclassified != "" {
				more = append(more, "Whether it has joined the deployment could not be told: "+r.Unclassified)
			}
			more = append(more, "Start the host or fix the route to it. Every other check ran with the sites that answered.")
			out = append(out, Finding{Section: SectionReach, Level: Fail,
				Line: fmt.Sprintf("%s: ssh to %s did not answer (%s)", r.Site, r.Destination, sshReason(firstLine(r.Err))),
				More: more})
		}
	}
	return out
}
```

`topology.DownLine` names `paisans site remove --site <s> --host-gone` in the line itself, as `apply`'s pending section prints it.

Run: `go test ./internal/doctor/`
Expected: PASS, `TestUnreachableSiteSaysWhatIsLost` among them (its sites carry no class).

- [ ] **Step 3: The classification, once per run, in the command**

In `cmd/paisans/doctor.go`, add to `doctorReads`:

```go
	// all makes a transport for every declared site, which the
	// classification reads beyond the sites --site names; it is called only
	// when the classification runs, so a site outside --site is never
	// contacted otherwise. classes is the classification's answer.
	all     func() map[string]apply.Transport
	classes *siteClasses
```

and below it:

```go
// siteClasses is the run's classification of its sites, made the first
// time a site does not answer or its record does not cover it, and never
// otherwise, so a healthy run never pays for it.
type siteClasses struct {
	once sync.Once
	c    *topology.Classification
	err  error
}

// class is how the run's classification placed site, or why it refused;
// nothing, for reads made without one (a test's).
func (d doctorReads) class(site string) (topology.Site, string) {
	if d.classes == nil {
		return topology.Site{}, ""
	}
	d.classes.once.Do(func() { d.classes.c, d.classes.err = doctorClassify(d.cfg, d.all()) })
	if d.classes.err != nil {
		return topology.Site{}, d.classes.err.Error()
	}
	return d.classes.c.Sites[site], ""
}

// doctorClassify is the run's classification of every declared site, as
// apply makes it at its start: topology reads each site's login and
// record, the gateways' deployment record and the cluster's members, all
// reads. Tests replace it.
var doctorClassify = func(cfg *config.Config, all map[string]apply.Transport) (*topology.Classification, error) {
	return topology.Classify(cfg, topology.Gather(cfg, all))
}
```

Add `"github.com/paisans-software/paisans-cli/internal/topology"` to the imports. Make `Reach` fill the class when the site did not answer, and `Admission` when the record does not cover the site:

```go
func (d doctorReads) Reach(site string) doctor.SiteReach {
	t := d.transports[site]
	r := doctor.SiteReach{Site: site, Destination: t.Describe()}
	if out, err := t.Run(doctor.ReachCommand); err != nil {
		r.Err = errText(out, err)
		r.Class, r.Unclassified = d.class(site)
	}
	return r
}
```

and at the end of `Admission`, before `return p`:

```go
	if !p.Covered(d.cfg) {
		p.Class, p.Unclassified = d.class(site)
	}
```

In `runDoctor`, after the `transports` loop, say how to make a transport for every declared site, reusing the ones already made, and pass it:

```go
	all := func() map[string]apply.Transport {
		out := map[string]apply.Transport{}
		for _, name := range cfg.SiteNames() {
			if t, ok := transports[name]; ok {
				out[name] = t
				continue
			}
			out[name] = newBoundedTransport(doctorTransport(siteTransport(name, cfg.Sites[name], "")), doctorSiteCommands)
		}
		return out
	}
```

```go
	graph := doctor.Checks(cfg, sites, doctorReads{cfg: cfg, transports: transports, all: all, classes: &siteClasses{}, token: &workstationToken{path: *secretsPath}})
```

In `withDoctorHosts` (`cmd/paisans/doctor_test.go`), make the classification place nothing unless a test says otherwise, so the existing expectations hold, before its own `t.Cleanup`:

```go
	savedClassify := doctorClassify
	doctorClassify = func(*config.Config, map[string]apply.Transport) (*topology.Classification, error) {
		return &topology.Classification{Sites: map[string]topology.Site{}}, nil
	}
	t.Cleanup(func() { doctorClassify = savedClassify })
```

A transport to a site outside `--site` is made only when the classification runs, so `TestDoctorSiteNarrowsTheHostsReached`, whose one host answers and is admitted, still contacts nothing else; a test whose classification runs gives every declared site's host, as `stuckHosts` does.

Run: `go test ./internal/doctor/ ./cmd/paisans/ -run 'Reach|Absent|Doctor'`
Expected: PASS.

- [ ] **Step 4: The changelog**

In `CHANGELOG.md`, under `[Unreleased]`, `### Changed`, add:

```markdown
- `doctor` reports a site pending admission, or down after joining, as a warning naming what the deployment is without and how to admit or remove it; a gateway stays a failure.
```

- [ ] **Step 5: Commit**

```bash
git add internal/doctor/reach.go internal/doctor/reach_test.go cmd/paisans/doctor.go cmd/paisans/doctor_test.go CHANGELOG.md
git commit -m "feat: doctor warns of a site pending admission or down

A deployment runs on the sites it has, so a site whose host has not
admitted it yet, or a joined site that does not answer, is a risk to
know about rather than an outage. Whether a site has joined is read from
the deployment, through the classification apply makes, which doctor
asks once and only when a site does not answer or its record does not
cover it. Each such site is a warning with what the deployment is
without and the host request or site remove line; a gateway stays a
failure, since apply refuses to run without one.

Founder decision.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: The self-check in the graph

**Files:**
- Modify: `internal/doctor/checks.go`, `internal/doctor/doctor.go`, `internal/doctor/graph.go`, `internal/doctor/graph_test.go`
- Modify: `cmd/paisans/doctor.go`, `cmd/paisans/doctor_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: Task 5's `SelfCheckProbe`, `SelfChecks`, `SectionSelfCheck`, `selfcheck.TargetFor`; `selfcheck.Check`, `selfcheck.DefaultProbe`, `*selfcheck.Conflict`; `config.Site.RunsCaddy()`.
- Produces: `Reads.SelfCheck(site string) SelfCheckProbe`; `Input.SelfChecks []SelfCheckProbe`; nodes `self-check <site>`; in `cmd/paisans`: `var doctorSelfCheck func(ctx context.Context, t selfcheck.Target) error`.

- [ ] **Step 1: Write the failing tests**

In `internal/doctor/graph_test.go`, add to `fixtureReads`:

```go
func (f *fixtureReads) SelfCheck(site string) SelfCheckProbe {
	f.asked("self-check " + site)
	for _, p := range f.in.SelfChecks {
		if p.Site == site {
			return p
		}
	}
	return SelfCheckProbe{Site: site, Host: "talk.example.org", Address: "203.0.113.30"}
}
```

and to `TestBoundOfOneRunsInOrder`'s `want`, after `"leftovers vm"`:

```go
		"self-check vm",
```

Add, below `TestDockerDownStopsWhatRunsDocker`:

```go
// The gateway is asked from outside even when its ssh does not answer.
func TestSelfCheckNeedsNoSsh(t *testing.T) {
	cfg := fixture(t)
	fix := healthyInput()
	fix.Reach = []SiteReach{
		{Site: "home-a", Destination: "ubuntu@home-a.local"},
		{Site: "home-b", Destination: "ubuntu@home-b.local"},
		{Site: "vm", Destination: "ubuntu@vm.example.org", Err: "ssh: connect to host vm.example.org port 22: Operation timed out"},
	}
	reads := &fixtureReads{in: fix, log: &events{}}
	Checks(cfg, fix.Sites, reads).Run(ui.Discard, 0)
	if !reads.log.has("self-check vm") {
		t.Errorf("the gateway was not asked from outside: %v", reads.log.list)
	}
	if reads.log.has("self-check home-a") {
		t.Errorf("a site with no Caddy of the toolkit's was asked: %v", reads.log.list)
	}
}
```

Run: `go test ./internal/doctor/ -run 'TestSelfCheckNeedsNoSsh|TestBoundOfOne'`
Expected: FAIL to compile, `f.in.SelfChecks undefined`.

- [ ] **Step 2: The probe in the Input, the read and the node**

In `internal/doctor/doctor.go`: add `SelfChecks []SelfCheckProbe` to `Input` after `Settings`, with the comment `// SelfChecks is each Caddy site's own answer, asked from outside (selfcheck.go).`; insert `SectionSelfCheck` in `sections` after `SectionLeftovers`; in `Diagnose`, after the `Leftovers` line, `findings = append(findings, SelfChecks(cfg, in.SelfChecks)...)`. In `internal/doctor/graph.go`, `clone`, add `out.SelfChecks = slices.Clone(in.SelfChecks)`.

In `internal/doctor/checks.go`, add to `Reads`:

```go
	// SelfCheck asks a site running the toolkit's Caddy, from the
	// workstation, through its public address, whether it answers as this
	// deployment, as apply asks the gateway.
	SelfCheck(site string) SelfCheckProbe
```

and in `Checks`, after the `leftovers` loop and before `if dr, ok := reads.(DNSReads)`:

```go
	// Each site running the toolkit's Caddy, asked from outside: it needs
	// no ssh, so one whose ssh does not answer is still asked.
	for _, site := range sites {
		if !cfg.Sites[site].RunsCaddy() {
			continue
		}
		g.Add(Node{Name: SectionSelfCheck + " " + site, Section: SectionSelfCheck, Site: site, Subject: site,
			Gather: func(Input) func(*Input) {
				p := reads.SelfCheck(site)
				return func(in *Input) { in.SelfChecks = append(in.SelfChecks, p) }
			},
			Judge: func(in Input) []Finding { return SelfChecks(cfg, bySite(in.SelfChecks, site, SelfCheckProbe.site)) },
		})
	}
```

Run: `go test ./internal/doctor/`
Expected: PASS.

- [ ] **Step 3: The read, through the self-check apply runs**

In `cmd/paisans/doctor.go`, add:

```go
// doctorSelfCheck asks t from this workstation with the check apply runs at
// the end of the gateway's step, so the two cannot disagree about what
// answers as this deployment. It asks once: doctor does not wait for a
// certificate. Tests replace it, since no test may reach a real address.
var doctorSelfCheck = func(ctx context.Context, t selfcheck.Target) error {
	p := selfcheck.DefaultProbe()
	p.Wait = 0
	return selfcheck.Check(ctx, p, t)
}

// SelfCheck asks the site from outside, with a minute for both fetches.
func (d doctorReads) SelfCheck(site string) doctor.SelfCheckProbe {
	p := doctor.SelfCheckProbe{Site: site}
	t, ok := selfcheck.TargetFor(d.cfg, site)
	if !ok {
		p.Unchecked = "no hostname to ask for"
		if d.cfg.Sites[site].PublicAddress == "" {
			p.Unchecked = "no public_address to ask through"
		}
		return p
	}
	p.Host, p.Address = t.Host, t.Address
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := doctorSelfCheck(ctx, t)
	var c *selfcheck.Conflict
	if errors.As(err, &c) {
		p.Conflict = c.Port
	}
	if err != nil {
		p.Err = err.Error()
	}
	return p
}
```

Add `"github.com/paisans-software/paisans-cli/internal/selfcheck"` to the imports.

In `withDoctorHosts`, make every site answer as itself unless a test says otherwise, before its own `t.Cleanup`:

```go
	savedSelfCheck := doctorSelfCheck
	doctorSelfCheck = func(context.Context, selfcheck.Target) error { return nil }
	t.Cleanup(func() { doctorSelfCheck = savedSelfCheck })
```

Add `"context"` and `"github.com/paisans-software/paisans-cli/internal/selfcheck"` to the test file's imports. Add a command test:

```go
// A monitor running Caddy that answers as something else fails doctor,
// with apply's own words; a gateway with no public_address warns.
func TestDoctorSelfChecksEachCaddySite(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	var sent, writes []string
	withDoctorHosts(t, cfg, stuckHosts(cfg, &sent, &writes))
	doctorSelfCheck = func(_ context.Context, target selfcheck.Target) error {
		return &selfcheck.Conflict{Port: 443, Site: target.Site, Detail: `status 200, body "hello"`}
	}
	rec := withRecorder(t, true)
	_ = runDoctor([]string{"--config", fixtureConfig(), "--site", "vm", "--site", "watch"})
	out := rec.Lines()
	if !strings.Contains(out, "fail: check self-check watch 443 on watch answers, but not as this deployment") {
		t.Errorf("the monitor's answer is not a failure:\n%s", out)
	}
	if !strings.Contains(out, "warned: check self-check vm not asked from outside: no public_address to ask through") {
		t.Errorf("the gateway with no public_address does not say it was not asked:\n%s", out)
	}
}
```

In `TestDoctorDiagnosesTheStuckReplicaAndOnlyReads`, add to `want`:

```go
		"done: check self-check watch answers as this deployment on 443 and 80, asked for health.example.org through 203.0.113.20",
		"warned: check self-check vm not asked from outside: no public_address to ask through",
```

Run: `go test ./cmd/paisans/ -run 'Doctor|DefaultOutput'`
Expected: PASS. A summary count in another doctor test grows by one passed check for `watch` and one warning for `vm`, where each was asked; recount it as Task 6, Step 7 says.

- [ ] **Step 4: The changelog**

In `CHANGELOG.md`, under `[Unreleased]`, `### Added`, add:

```markdown
- `doctor` fetches `/.well-known/paisans/<token>` from the gateway and each monitor running Paisans' Caddy, from the workstation as `apply` does, and fails when 443 or 80 answers as anything else.
```

- [ ] **Step 5: Run the touched packages**

Run: `gofmt -l internal/doctor internal/selfcheck cmd/paisans && go vet ./internal/doctor/ ./internal/selfcheck/ ./cmd/paisans/ && go test ./internal/doctor/ ./internal/selfcheck/ ./cmd/paisans/`
Expected: nothing listed; PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/doctor cmd/paisans CHANGELOG.md
git commit -m "feat: doctor asks each caddy site from outside whether it answers as itself

The deployment's user cannot read the host's ruleset or another user's
sockets, so a port taken after admission, and above all one a DNAT rule
takes with no listener, is invisible from the host. doctor fetches the
gateway's and each Caddy monitor's own well-known path from the
workstation with the check apply runs, so the two cannot disagree, and
asks even a site whose ssh does not answer.

Founder decision.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: A container whose mesh address was missing at boot

**Files:**
- Modify: `internal/doctor/containers.go` (the `cannot assign requested address` case)
- Modify: `internal/doctor/doctor_test.go` (`TestContainerThatStartedBeforeWg0`)
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: `deployment.Deployment.Interface()`, `.WireGuardUnit()`; `PSEntry.Stack`.
- Produces: the advice text phase 9 documents.

- [ ] **Step 1: Change the test to the advice that holds**

In `TestContainerThatStartedBeforeWg0`, replace the `want` loop:

```go
	for _, want := range []string{"Its Docker daemon started it before psns-f2a9 was up", "`docker start paisans-f2a9-talk-app-1` on home-b, as the deployment's user", "`paisans apply --site home-b --recreate talk --execute`", "`paisans doctor --site home-b`"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "host prepare") {
		t.Errorf("the advice names host prepare, which orders nothing at boot:\n%s", text)
	}
```

Run: `go test ./internal/doctor/ -run TestContainerThatStartedBeforeWg0`
Expected: FAIL, `missing "Its Docker daemon started it before psns-f2a9 was up"` and the `host prepare` error.

- [ ] **Step 2: The advice**

In `internal/doctor/containers.go`, replace the `cannot assign requested address` case's `f.More`:

```go
		f.More = []string{
			fmt.Sprintf("Its Docker daemon started it before %s was up. The container publishes its port on the site's mesh address, which exists only once %s has brought %s up, and Docker does not retry a container whose network setup failed.", dep.Interface(), dep.WireGuardUnit(), dep.Interface()),
			"recover:",
			fmt.Sprintf("  1. `docker start %s` on %s, as the deployment's user, or %s.", name, site, recreate),
			fmt.Sprintf("  2. If it happens at every boot, `paisans doctor --site %s` says under linger whether the user's manager is ordered after the mesh.", site),
		}
```

Run: `go test ./internal/doctor/`
Expected: PASS.

- [ ] **Step 3: The changelog**

In `CHANGELOG.md`, under `[Unreleased]`, `### Changed`, add:

```markdown
- `doctor`'s advice for a container whose mesh address was missing at boot is to start it again, as the deployment's user, or recreate its stack.
```

- [ ] **Step 4: Commit**

```bash
git add internal/doctor/containers.go internal/doctor/doctor_test.go CHANGELOG.md
git commit -m "fix: doctor's boot advice names what the deployment's user can run

A container that failed with cannot assign requested address was told to
run host prepare, which ordered the system Docker after the mesh. Each
deployment's containers run in its own user's daemon, host prepare
installs nothing, and host admit's drop-in orders that user's manager
after the mesh, which the linger check reads. So the advice is what the
user can do: start the container again, or recreate its stack.

Founder decision.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Final checks

- [ ] **Step 1: The sudo finding stays gone**

Run: `grep -rn "sudo" internal/doctor/*.go cmd/paisans/doctor.go | grep -v _test.go`
Expected: `SudoCommand`, `SectionSudo` and their comments in `internal/doctor/sudo.go`, `SudoRights`' advice, the user check's advice (`sudo gpasswd`), the self-check's advice (`sudo ss`, `sudo docker ps`, `sudo nft`), and `journalAdvice`'s helper command in `mesh.go`. Nothing that runs a command through sudo, and no finding about a sudo the transport could not use.

- [ ] **Step 2: The whole suite**

Run: `gofmt -l cmd internal && go vet ./... && go test ./...`
Expected: nothing listed; PASS.

- [ ] **Step 3: The diff**

Run: `git diff origin/develop --stat && git diff origin/develop | perl -CSD -ne 'print if /^\+.*\x{2014}/'`
Expected: this plan's files only; no em dash.

Run: `git diff origin/develop -- CHANGELOG.md`
Expected: four bullets, each one sentence of at most 35 words.

- [ ] **Step 4: Review**

Request the final whole-branch review (superpowers:requesting-code-review). Every finding is fixed before merge, Minors batched into this step.

---

## Self-Review Notes

* **Spec coverage.** The index's phase 7 list: the user is not privileged (Task 2, `User`, reading `hostadmit.PrivilegedGroups`); the rootless socket answers (Task 2, `DockerSocket`); Engine 29.5 or later (Task 2, `DockerEngine`, `hostadmit.MinRootlessEngine`); linger on (Task 2, `Linger`, with the boot order of founder decision 3); `sudo -n -l` lists only the helper (Tasks 1 and 3, admit's own judgement); the admission record matches the declaration (Task 4, the merged `Differences`); each in the check graph (Tasks 6 and 8); the sudo finding removed (ruling 1, *Final checks* Step 1); the self-check from outside sharing `apply`'s code (Tasks 5 and 8, the gateway and each Caddy monitor per the spec's *What runs as the deployment's user*); a `WARN` per pending or down site listing what the deployment is without (Task 7 for a site that does not answer, Task 4 for one that answers uncovered, both from `topology`'s classification). *A deployment with sites missing*: "`doctor` reports each pending or down site as a `WARN` with the same list, and the gateway as a `FAIL`" (Tasks 4 and 7, `Losses`, `absentLevel`); "Whether a site has joined is read from the deployment" (ruling 10, `topology.Gather`). The doctor graph spec: hard needs prune and report once (Task 6, `TestAPendingSiteIsCheckedNoFurther`, `TestDockerDownStopsWhatRunsDocker`, `TestPruningIsReportedOnce`); same findings as `Diagnose` (Tasks 6 and 8, Step 3 and Step 2); bound of one in the table's order (`TestBoundOfOneRunsInOrder`).
* **Review Focus.** sudo in another language or an unknown shape: Task 3 rows `translated`, `unrecognised`, Task 1's `TestReadSudoList`. Reaching past the helper: Task 3's rows and Task 1's `TestBeyondHelper`. A site that answers but is not admitted: Task 6, `TestAPendingSiteIsCheckedNoFurther`. A gateway pending or down: Task 4 row `gateway without the ports`, Task 7 rows `gateway down`, `gateway pending`. An echoing server: Task 5, the `an echo of the path` row.
* **Left to other phases, on purpose.** User documentation of every check here is phase 9's last task (ruling 9). The classification, the self-check's fetch and `apply`'s step are phase 6's; this phase calls them. Staging confirmation of `sudo -n -l`'s exact output as the user is phase 10's.
* **Type consistency.** `UserProbe`, `DockerProbe`, `LingerProbe` (Task 2), `SudoProbe` (Task 3), `AdmissionProbe` (Task 4), `SelfCheckProbe` (Task 5) are each an `Input` field's element (`Users`, `Docker`, `Linger`, `Sudo`, `Admission`, `SelfChecks`) and a `Reads` method's return (`User`, `Docker`, `Linger`, `Sudo`, `Admission`, `SelfCheck`). `pending`, `absentLevel` and `requestAdvice` are Task 4's, and Task 7's `Reach` calls them. Each section constant lives in its judge's file. `admittedRecord` exists once per test package: `internal/doctor/admission_test.go` (Task 4, no `*testing.T`) and `cmd/paisans/admitted_test.go` (already on `develop`, with one). `rootlessDocker` (Task 2), `sudoList` and `helperEntry` (Task 3) are the defaults `fixtureReads` answers with (Task 6). Node names: `admission <site>`, `user <site>`, `docker <site>`, `docker engine <site>`, `linger <site>`, `sudo rights <site>`, `self-check <site>`; the pruned labels in the tests drop the site word from each.
* **Placeholder scan.** None: every function this plan calls is on `develop` at `6783478a` or produced by a task above.
