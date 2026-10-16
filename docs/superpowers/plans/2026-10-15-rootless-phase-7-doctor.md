# Rootless Phase 7: Doctor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `paisans doctor` checks that each site is still what its owner's `host admit` made it (the admission record matches the declaration, the deployment's user holds no privileged group, its own rootless Docker answers at Engine 29.5 or later, linger is on, and sudo lets it run the WireGuard helper and nothing else), reports a site pending admission or down as a `WARN` naming what the deployment is without, and fetches each gateway's `/.well-known/paisans/<token>` from outside with the code `apply` runs; every one a node of the check graph.

**Architecture:** Each new check is a pure judge in `internal/doctor` (`rootless.go`, `sudo.go`, `admission.go`, `answer.go`) over a probe a new `Reads` method fills, and a node in `doctor.Checks`. The admission node runs first on every site that answered and is a hard need of everything else asked of that site, so a site that has not admitted the deployment is one `WARN` line and nothing more; the `docker <site>` node is a hard need of every check that runs `docker` there, so a daemon that does not answer is one `FAIL` and no cascade. Pending and down come from phase 6's classification, asked once per run and only when a site does not answer, through one package variable in `cmd/paisans`. The gateway's answer is phase 6's own check, called from the workstation through another.

**Tech Stack:** Go 1.25, the existing `internal/doctor` check graph (`Graph`, `Node`, `Need`), `internal/admission` (phase 4), `internal/hostadmit` and `internal/hostcheck` (phase 5), `apply.WGHelper` (phase 3), phase 6's classification and gateway answer check, the `cmd/paisans` doctor test harness (`doctorHost`, `withDoctorHosts`, `readOnly`).

**Spec:** `docs/specs/2026-10-15-rootless-and-host-admit.md` (*The deployment's user*, *The WireGuard helper*, *What runs as the deployment's user*: "`paisans doctor` loses the sudo finding and the allowlist check, and gains [...]", *The gateway answers as itself*, *A deployment with sites missing*), `docs/specs/2026-10-15-doctor-check-graph.md` (*The check graph*, *Running the graph*, *Reporting each check as it ends*), and the phase index `docs/superpowers/plans/2026-10-15-rootless-and-host-admit.md`, *Phase 7: Doctor*, read beside *Phase 5*, *Phase 6* and *Phase 9*. Phase 5's plan, *What this phase hands to phases 6, 7 and 10*, names what this phase checks.

## Global Constraints

* "the user is not privileged, the rootless socket answers, Engine 29.5 or later, linger on, `sudo -n -l` lists only the helper, the admission record matches the declaration. Each in the check graph [...], with the sudo finding removed, and a gateway check that fetches `/.well-known/paisans/<token>` from outside as `apply` does, sharing its code, and a `WARN` for each pending or down site listing what the deployment is without." (index, *Phase 7*)
* "`host admit` refuses a user that already exists and is root, or is in `sudo`, `wheel`, `admin`, `adm`, `docker`, `lxd` or `systemd-journal`. [...] `paisans doctor` checks the same list on every run." (spec, *The deployment's user*) The list is `hostadmit.PrivilegedGroups`; doctor never keeps a copy.
* "Docker Engine 29.5 or later." (index) The floor is `hostadmit.MinRootlessEngine`; doctor never keeps a copy.
* "`doctor` reports each pending or down site as a `WARN` with the same list." (spec, *A deployment with sites missing*) "Whether a site has joined is read from the deployment, never from the site's own host." (same)
* "A failed node's dependents are pruned, transitively, and get no lines of their own. The failed line says once what was not checked because of it." (doctor spec, *Reporting each check as it ends*) "A **hard need** must have passed for the node to run." (doctor spec, *The check graph*)
* "Doctor is the read-only answer [...]": every command doctor sends is a read; the command-level test's allow list (`readOnly` in `cmd/paisans/doctor_test.go`) gains each new command and refuses any other.
* "`doctor.Input` stays the record of every probe [...] running every node's judge over a complete `Input` gives the same findings the graph reported." (doctor spec) `Diagnose` calls every new judge, in the graph's order.
* No em dashes anywhere: code, comments, commit messages. Code comments describe what is, never what was.
* Commits: `<type>: <lowercase subject>`, a body saying why, `Founder decision.` where it was one, the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. A behaviour-changing commit adds one `CHANGELOG.md` bullet under `[Unreleased]`, one sentence, at most 35 words; a commit with nothing an operator would notice adds none and says so in its body.
* User documentation is phase 9's (*Rulings*, 9): this phase changes `CHANGELOG.md` and code only.
* Never `git stash`; other sessions share the stash. Never push.
* Implementers run the touched packages during each task (`go test ./internal/doctor/ ./cmd/paisans/ ...`) and the full suite once, at the end (*Final checks*).

## Review Focus

The index names no Review Focus line for this phase. The five below are what the spec implies and no line of it tests, most likely first; each is pinned by a test in the task named.

* **sudo's answer in another language, or in a shape the parse does not know.** On a host whose `LANG` is not English, `sudo -l` translates "may run the following commands"; a parse that found no header could read "nothing listed" as "nothing but the helper". The command runs in the C locale, and any answer the parse does not recognise is a `WARN` that it could not be read, never an `ok` (Task 2: `TestSudoRights`, rows `unrecognised` and `translated`).
* **sudo allowing the helper and something more, on one line or through a group.** `(root) NOPASSWD: <helper>, /bin/sh`, a second line `(ALL : ALL) ALL` from a `%group` rule, the helper with `(ALL)` as the run-as user, or another deployment's helper: each must `FAIL` naming exactly what is extra (Task 2: rows `helper and a shell on one line`, `helper and ALL`, `helper as any user`, `another deployment's helper`).
* **A site that answers ssh but has not admitted the deployment.** `ssh.user` names an existing account the owner never ran `host admit` for: the login works, there is no record. It must be one `WARN` with the `host request` line, and nothing else asked of that host, rather than a `FAIL` per check (Task 4: `TestAPendingSiteIsCheckedNoFurther`).
* **A gateway that is pending or down.** The deployment cannot serve without it and `apply` refuses to run (spec, *What still refuses the run*), so the line must stay a `FAIL` and doctor must exit 1, while a pending or down data or apps site is a `WARN` and doctor exits 0 for it (Task 5: `TestAbsentSites`, rows `gateway down` and `gateway pending`).
* **A server on 443 that echoes the request.** A service that answers any path with the path itself puts the token in its body; a check that looks for the token anywhere in the body would pass it. The gateway check must accept the token as the whole body and nothing else (Task 6: `TestGatewayAnswerRefusesAnEcho`, written against phase 6's shared check).

## Rulings

Where the spec, the index or the code leaves a choice open, this plan decides as follows. Each is restated where its task implements it.

1. **The sudo finding is gone already.** Phase 2 removed it (`16b7bbcb`, "feat: deployment commands run without sudo": "doctor's finding for a sudo it could not use goes too, since none of its transports has one"), and phase 1 removed the allowlist check. This phase removes nothing; *Final checks* confirms with a grep, and the read-only allow list keeps any sudo command but `SudoCommand` out.
2. **The new checks are part of `doctor.Reads`, not an optional interface** as `DNSReads` is. Every deployment is rootless, so every doctor run asks them; the DNS checks are optional only because they need the secrets.
3. **Node order and needs.** After `reach <site>`: `admission <site>` (hard: reach); then `user <site>`, `docker <site>`, `docker engine <site>` (hard: `docker <site>`, no read of its own), `linger <site>` and `sudo rights <site>` (hard: admission). Every existing node that reads one site needs `admission <site>` in place of `reach <site>`; `containers`, `settings`, `leftovers`, `patroni <site>` and `caddy certificates` need `docker <site>`, since each runs `docker` there. `etcd`, `etcd version` and `pocket-id` keep their any-of needs on `reach`, since their gathers already try each member in turn and record why one failed.
4. **The Engine floor is `hostadmit.MinRootlessEngine`, compared with `hostcheck.AtLeast`** (both phase 5, Task 4), and the privileged groups are `hostadmit.PrivilegedGroups` (phase 5, Task 5). `internal/doctor` imports `internal/hostadmit` for those two names only; `hostadmit` does not import `doctor`, so there is no cycle.
5. **What sudo may allow is exactly the line `host admit` writes**: one entry, run as `root`, tagged `NOPASSWD` and nothing else, whose one command is `apply.WGHelper{Iface}.Path()`. Anything else is a `FAIL` naming it. `sudo -n -l` runs as `LC_ALL=C sudo -n -l`, and an answer the parse does not know is a `WARN`.
6. **A site with no record, or without a grant its roles need, is pending**: a `WARN` (a `FAIL` on a gateway, ruling 8) whose node's outcome is `Failed`, so nothing more is asked of that site. A record that cannot be read, or that differs from the declaration, is a `FAIL` whose outcome is `Passed`, so the rest of the site is still checked.
7. **The record is compared with the declaration by `(admission.Record).Differences(config.Site) []string`**: user, SSH port, WireGuard port, forwarding, and the owner's web server for a monitor behind it. The grants are not among them; `admission.Needs(site).Missing(record.Grants)` names those, and makes a site pending rather than wrong. If phase 6 already compares the record with the declaration in `host prepare`, Task 3 moves that comparison here and has `host prepare` call it, so the two can never disagree.
8. **A gateway pending or down is a `FAIL`.** The spec makes each pending or down site a `WARN`, and also has the run refuse while the gateway is pending or down; doctor's exit status follows the run, so a deployment `apply` refuses is one doctor fails. Every other site is a `WARN`. Founder decision.
9. **User documentation is phase 9's.** The founder decided phase 9 runs alongside this phase and documents doctor's checks as they land; this phase changes no `README.md`, `docs/commands.md` or `docs/development.md` text, and *What this phase hands to phase 9* lists every name and wording phase 9's last task describes.
10. **Pending and down are asked of phase 6's classification once per run, and only when a site does not answer.** A healthy run never pays for it. The classification gets a transport for every declared site, not only those `--site` names, since it reads the gateway's deployed record and the cluster's members.
11. **The gateway answer node runs on the workstation, one per gateway asked, with no needs.** It asks from outside, so a site whose ssh does not answer is still asked.
12. **A container that failed with `cannot assign requested address`** is advised to be started again or its stack recreated, as the deployment's user: nothing installs a drop-in ordering Docker after `wg-quick` (phase 6: `host prepare` installs nothing), and each deployment runs its own daemon. See founder question 3.

## Founder questions

These are calls this plan does not make. Each has an interim ruling above so the work can proceed; an answer that differs changes the task named.

1. **A gateway pending or down: `FAIL` or `WARN`?** The spec says doctor reports each pending or down site as a `WARN`, and that `apply` refuses while the gateway is pending or down. This plan makes the gateway a `FAIL` so that doctor exits 1 exactly when `apply` would refuse (ruling 8, Task 5). A `WARN` would change one function, `absentLevel`.
2. **Should doctor fetch `/.well-known/paisans/<token>` from Caddy monitors too?** The spec names the gateway; a monitor running Paisans' Caddy also binds 80 and 443 under `--public-ports` and is as exposed to a port taken after admission. This plan checks gateways only, as phase 6's `apply` step does (ruling 11, Task 6); doctor would follow whatever `apply` checks.
3. **What orders the deployment's Docker daemon after its mesh interface at boot?** The spec's *Observed* R10 found the user's manager starting after `wg-quick`, unordered, and relies on `restart: unless-stopped` for host-networked infrastructure containers. An app container publishes its port on the mesh address, and Docker does not retry a container whose network setup failed, so on a boot where the user's daemon wins the race those containers stay down. Phase 5's ruling 22 adds no ordering beyond the egress unit. Doctor's advice (Task 7) is to start the container again; whether `host admit` should order `user@<uid>.service` after `wg-quick@psns-<token>` is a design call for phase 5 or a follow-up.

## Phase 6 interfaces this plan assumes

Phase 6's plan did not exist when this plan was written. The names below are what this plan calls; each is used in exactly one place, so a different name is a one-line change. **Before Task 1**, reconcile them (Task 0).

| What | Assumed name and shape | Called from |
|---|---|---|
| The classification of every declared site, once per run | `sitestate.Classify(cfg *config.Config, transports map[string]apply.Transport) (map[string]sitestate.Class, error)`, with `sitestate.Class{State sitestate.State; Reason string}` and states `sitestate.Ready`, `sitestate.Pending`, `sitestate.Down` | `doctorClassify` in `cmd/paisans/doctor.go` (Task 5) |
| The gateway's own answer, fetched through its public address on 443 and on 80 | `apply.CheckGatewayAnswer(ctx context.Context, fetch apply.Fetcher, cfg *config.Config, site string) error`, `apply.HTTPFetcher{}` the real fetcher, an error whose first line is "443 on <site> answers, but not as this deployment" for another answer | `doctorGatewayAnswer` in `cmd/paisans/doctor.go` (Task 6) |
| Where `host prepare` compares the admission record with the declaration | whatever phase 6 wrote; Task 3 makes it `(admission.Record).Differences` | Task 3 |
| What a site's absence costs | `losses(cfg, site)` in `internal/doctor/reach.go`, unless phase 6 moved it so `apply`'s plan can print the same words | Tasks 3 and 5 |

---

## What this phase hands to phase 9

Phase 9's last task documents doctor's checks from this phase's merged code. It describes, in `README.md`'s checks table and `docs/commands.md`'s `paisans doctor` entry:

* The nodes, in the graph's order: `reach <site>`, `admission <site>`, `user <site>`, `docker <site>`, `docker engine <site>`, `linger <site>`, `sudo rights <site>`, then the existing ones, then `gateway answer <site>` after `leftovers <site>` and before the DNS nodes.
* The commands: `doctor.UserCommand`, `doctor.DockerCommand`, `doctor.LingerCommand`, `doctor.SudoCommand` (`LC_ALL=C sudo -n -l`), and the record read from `admission.Path(token)` as the login user.
* What fails and what warns: a privileged group or uid 0; a daemon that does not answer or is not rootless; an Engine below 29.5; linger off; sudo allowing anything but the helper, or not the helper; a record unreadable or differing (each a `FAIL`). A site not admitted, admitted without a grant, or down: a `WARN`, a `FAIL` on a gateway, with what the deployment is without and the `host request` or `site remove --host-gone` line. The gateway answering as something else: a `FAIL` with the phase 6 message.
* That pending skips the rest of that site's checks, and a daemon that does not answer skips every check that runs `docker` there.
* The container advice for `cannot assign requested address`.

---

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `internal/doctor/rootless.go`, `rootless_test.go` | create | `UserCommand`, `DockerCommand`, `LingerCommand`; `UserProbe`, `DockerProbe`, `LingerProbe`; `User`, `DockerSocket`, `DockerEngine`, `Linger`; the section names |
| `internal/doctor/sudo.go`, `sudo_test.go` | create | `SudoCommand`, `SudoProbe`, `SudoEntry`, `ParseSudoList`, `SudoRights` |
| `internal/admission/record.go`, `differences_test.go` | modify, create | `(Record).Differences(config.Site) []string` |
| `internal/doctor/admission.go`, `admission_test.go` | create | `AdmissionProbe`, `Admission`, `pending`, `absentLevel`, `requestAdvice` |
| `internal/doctor/checks.go` | modify | the `Reads` methods; the new nodes and needs |
| `internal/doctor/doctor.go` | modify | `Input` fields, `sections`, `Diagnose` |
| `internal/doctor/graph.go` | modify | `Input.clone` |
| `internal/doctor/reach.go`, `reach_test.go` | modify, create | `SiteState`, `SiteClass`, `SiteReach.Class`; pending and down in `Reach` |
| `internal/doctor/answer.go`, `answer_test.go` | create | `SectionAnswer`, `AnswerProbe`, `GatewayAnswers` |
| `internal/doctor/containers.go`, `doctor_test.go` | modify | the boot advice; the fixtures |
| `internal/doctor/graph_test.go` | modify | `fixtureReads`' new reads; the order and pruning tests |
| `cmd/paisans/doctor.go` | modify | `doctorReads`' new reads, `doctorClassify`, `doctorGatewayAnswer`, the classification transports |
| `cmd/paisans/doctor_test.go`, `doctor_dns_test.go`, `output_test.go` | modify | `doctorHost.files`, `admitHosts`, `withDoctorHosts(t, cfg, hosts)`, `readOnly`, expectations |
| `internal/apply/<phase 6's gateway answer test file>` | modify | `TestGatewayAnswerRefusesAnEcho` |
| `CHANGELOG.md` | modify | four bullets |

---

### Task 0: Reconcile with phases 5 and 6

Not a code task, and no commit unless a name changes. This branch was cut from phase 5 in progress; phases 5 and 6 merge into `develop` before this phase starts (index: "Depends on: phase 6").

**Files:**
- Modify (only if a name differs): this plan

**Interfaces:**
- Consumes: phase 5's `hostadmit.PrivilegedGroups`, `hostadmit.MinRootlessEngine`, `hostcheck.AtLeast`, `admission.Record.MeshLAN`; phase 6's names in *Phase 6 interfaces this plan assumes*.
- Produces: a plan whose every name exists on `develop`.

- [ ] **Step 1: Bring the branch up to `develop`**

The branch holds only this plan's commit.

Run: `git fetch origin && git rebase origin/develop`
Expected: the plan's commit replays cleanly onto `develop`.

- [ ] **Step 2: Check phase 5's names**

Run: `grep -n "var PrivilegedGroups\|MinRootlessEngine =\|func AtLeast" internal/hostadmit/*.go internal/hostcheck/*.go`
Expected: three hits, `PrivilegedGroups` in `internal/hostadmit/user.go`, `MinRootlessEngine` in `internal/hostadmit/docker.go`, `AtLeast` in `internal/hostcheck/docker.go`.

- [ ] **Step 3: Check phase 6's names**

Run: `grep -rn "func Classify\|func CheckGatewayAnswer\|type Fetcher\|HTTPFetcher" internal/ cmd/ | grep -v _test.go`
Expected: the classification and the gateway answer check. Where a name or a signature differs from *Phase 6 interfaces this plan assumes*, edit this plan's table and the two functions that call them (`doctorClassify` in Task 5, `doctorGatewayAnswer` in Task 6) to phase 6's, and read phase 6's error text for another answer.

Run: `grep -rn "admission.Read\|Ports.SSH\|Ports.WireGuard" internal/hostprep/*.go | grep -v _test.go`
Expected: where phase 6's `host prepare` compares the record with the declaration. Task 3, Step 5 moves it.

Run: `grep -rn "func losses" internal/`
Expected: `internal/doctor/reach.go`, or wherever phase 6 moved it; Tasks 3 and 5 call it there.

- [ ] **Step 4: Commit, only if the plan changed**

```bash
git add docs/superpowers/plans/2026-10-15-rootless-phase-7-doctor.md
git commit -m "kacl-skip: docs: name phase 6's interfaces in the doctor plan

The plan was written before phase 6's, and named the classification and
the gateway answer check as it expected them; this names them as phase 6
merged them, so each task calls what exists.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 1: The user, its Docker daemon and linger, judged

**Files:**
- Create: `internal/doctor/rootless.go`
- Test: `internal/doctor/rootless_test.go`

**Interfaces:**
- Consumes: `hostadmit.PrivilegedGroups []string`, `hostadmit.MinRootlessEngine string` (`"29.5.0"`), `hostcheck.AtLeast(version, min string) bool`; `firstLine`, `lastLines` (`internal/doctor/doctor.go`); the test helper `find(t, findings, section, contains)` (`doctor_test.go`).
- Produces: `const SectionAdmission = "admission"`, `SectionUser = "user"`, `SectionDocker = "docker"`, `SectionLinger = "linger"`, `SectionSudo = "sudo rights"`; `const UserCommand`, `DockerCommand`, `LingerCommand string`; `type UserProbe struct{ Site, Out, Err string }`, `type DockerProbe struct{ Site, Out, Err string }`, `type LingerProbe struct{ Site, Out, Err string }`, each with an unexported `site() string`; `func (p DockerProbe) Answered() bool`; `func User(probes []UserProbe) []Finding`, `func DockerSocket(probes []DockerProbe) []Finding`, `func DockerEngine(probes []DockerProbe) []Finding`, `func Linger(probes []LingerProbe) []Finding`; `func keyed(out string) map[string]string`.

- [ ] **Step 1: Write the failing tests**

Create `internal/doctor/rootless_test.go`:

```go
package doctor

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

// rootlessDocker is what DockerCommand printed for a rootless daemon on a
// staging host, with the UID and the version kept and nothing else.
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

// Every group hostadmit refuses at admission fails here too: the two read
// one list.
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

func TestLinger(t *testing.T) {
	findings := Linger([]LingerProbe{
		{Site: "home-a", Out: "yes\n"},
		{Site: "home-b", Out: "no\n"},
		{Site: "vm", Err: "Failed to get user: No such process"},
	})
	if f := find(t, findings, SectionLinger, "home-a: linger is on"); f.Level != OK {
		t.Errorf("home-a: level %v", f.Level)
	}
	b := find(t, findings, SectionLinger, "home-b: linger is off")
	if b.Level != Fail || !strings.Contains(strings.Join(b.More, "\n"), "`paisans host admit`") {
		t.Errorf("home-b: %+v", b)
	}
	if f := find(t, findings, SectionLinger, "vm: could not read whether linger is on"); f.Level != Warn {
		t.Errorf("vm: level %v", f.Level)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/doctor/ -run 'TestUser|TestEveryPrivilegedGroupFails|TestDocker|TestLinger'`
Expected: FAIL to compile, `undefined: User` (and the other judges and probes).

- [ ] **Step 3: Write the judges**

Create `internal/doctor/rootless.go`:

```go
package doctor

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
)

// The rootless checks ask whether a site is still what its owner's host
// admit made it: the admission record matches the declaration
// (admission.go), the deployment's user holds no group that reaches past
// it, its own rootless Docker daemon answers and is new enough, linger keeps
// that daemon running with nobody logged in, and sudo lets the user run the
// WireGuard helper and nothing else (sudo.go). Each is read as the login
// user, with nothing that user may not read. See
// docs/specs/2026-10-15-rootless-and-host-admit.md, The deployment's user.

// Sections of the rootless checks, read right after reach: each says
// whether the rest of a site's checks can be believed.
const (
	SectionAdmission = "admission"
	SectionUser      = "user"
	SectionDocker    = "docker"
	SectionLinger    = "linger"
	SectionSudo      = "sudo rights"
)

// UserCommand prints the login user's UID, name and groups, a line each.
const UserCommand = `printf 'uid %s\nuser %s\ngroups %s\n' "$(id -u)" "$(id -un)" "$(id -nG)"`

// DockerCommand prints the DOCKER_HOST the command ran with, then asks the
// daemon there for its Engine version and its security options, which name
// rootless for a daemon running as its user. The transport sets
// DOCKER_HOST to the login user's own socket on every command.
const DockerCommand = `printf 'host %s\n' "${DOCKER_HOST:-unset}" && docker version --format 'engine {{.Server.Version}}' && docker info --format 'security {{json .SecurityOptions}}'`

// LingerCommand prints yes when systemd keeps the login user's instance,
// and with it the user's Docker daemon, running with no session open.
const LingerCommand = `loginctl show-user "$(id -un)" --property=Linger --value`

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
				"The deployment's user is the boundary between this deployment and the rest of the host, and each of these reaches past it: root, sudo, wheel and admin give root; docker is the owner's system daemon, which is root on the host; adm and systemd-journal read every log on it; lxd starts privileged containers.",
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
// nobody logged in: without it the user's Docker daemon, and every
// container in it, stops when the last login ends and does not start at
// boot.
func Linger(probes []LingerProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		switch answer := strings.TrimSpace(p.Out); {
		case p.Err == "" && answer == "yes":
			out = append(out, Finding{Section: SectionLinger, Level: OK, Line: fmt.Sprintf("%s: linger is on: its Docker daemon runs with nobody logged in, and starts at boot", p.Site)})
		case p.Err == "" && answer == "no":
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
		}
	}
	return out
}
```

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./internal/doctor/ -run 'TestUser|TestEveryPrivilegedGroupFails|TestDocker|TestLinger' && go vet ./internal/doctor/`
Expected: PASS. `TestLinger`'s `home-a` find matches the start of the OK line.

- [ ] **Step 5: Commit**

```bash
git add internal/doctor/rootless.go internal/doctor/rootless_test.go
git commit -m "feat: judge the deployment's user, its rootless docker and linger

Admission draws the boundary once; a group added to the user afterwards,
a daemon that stopped answering or was replaced by a root one, an Engine
too old for host networking, or linger turned off each undoes part of it
with nothing else to notice. These are the judges doctor runs over what
each site says; the privileged groups and the Engine floor are host
admit's own, so the two cannot drift.

Nothing an operator would notice yet, since no check runs them, so no
changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: What sudo lets the deployment's user run

**Files:**
- Create: `internal/doctor/sudo.go`
- Test: `internal/doctor/sudo_test.go`

**Interfaces:**
- Consumes: Task 1's `SectionSudo`, `keyed`; `apply.WGHelper{Iface string}.Path() (string, error)`; `deployment.Deployment.Interface()`, `.Prefix()`; `config.Config.Sites[site].SSHUser()`; `lastLines`.
- Produces: `const SudoCommand = "LC_ALL=C sudo -n -l"`; `type SudoProbe struct{ Site, Out, Err string }` with `site()`; `type SudoEntry struct { RunAs string; Tags []string; Commands []string }`; `func ParseSudoList(out string) (entries []SudoEntry, ok bool)`; `func SudoRights(cfg *config.Config, probes []SudoProbe) []Finding`.

- [ ] **Step 1: Write the failing tests**

Create `internal/doctor/sudo_test.go`:

```go
package doctor

import (
	"reflect"
	"strings"
	"testing"
)

// sudoDefaults is the start of sudo -l's answer on Ubuntu 24.04, sudo
// 1.9.15, before the list itself.
const sudoDefaults = "Matching Defaults entries for paisans-f2a9 on home-a:\n" +
	"    env_reset, mail_badpass,\n" +
	"    secure_path=/usr/local/sbin\\:/usr/local/bin\\:/usr/sbin\\:/usr/bin\\:/sbin\\:/bin\\:/snap/bin,\n" +
	"    use_pty\n\n"

const helperEntry = "(root) NOPASSWD: /usr/local/libexec/paisans/wg-psns-f2a9"

func sudoList(entries ...string) string {
	return sudoDefaults + "User paisans-f2a9 may run the following commands on home-a:\n    " + strings.Join(entries, "\n    ") + "\n"
}

func TestParseSudoList(t *testing.T) {
	got, ok := ParseSudoList(sudoList(helperEntry, "(ALL : ALL) ALL", "(root) NOPASSWD: SETENV: /bin/a, /bin/b"))
	want := []SudoEntry{
		{RunAs: "root", Tags: []string{"NOPASSWD"}, Commands: []string{"/usr/local/libexec/paisans/wg-psns-f2a9"}},
		{RunAs: "ALL : ALL", Commands: []string{"ALL"}},
		{RunAs: "root", Tags: []string{"NOPASSWD", "SETENV"}, Commands: []string{"/bin/a", "/bin/b"}},
	}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, %v\nwant %+v", got, ok, want)
	}
	if got, ok := ParseSudoList("Sorry, user paisans-f2a9 may not run sudo on home-a.\n"); !ok || len(got) != 0 {
		t.Errorf("not allowed: %+v, %v", got, ok)
	}
	for _, out := range []string{"", "something else\n", sudoDefaults, "Einträge für paisans-f2a9 auf home-a:\n    (root) NOPASSWD: /bin/sh\n"} {
		if _, ok := ParseSudoList(out); ok {
			t.Errorf("%q read as a list", out)
		}
	}
}

// What sudo allows is exactly the line host admit writes, or the check
// fails naming what is extra or what is missing; an answer it cannot read
// is a warning.
func TestSudoRights(t *testing.T) {
	cfg := fixture(t)
	for _, c := range []struct {
		name, out, err string
		level          Level
		line           string
		more           string
	}{
		{"helper alone", sudoList(helperEntry), "", OK, "home-a: sudo lets ubuntu run the WireGuard helper and nothing else", ""},
		{"helper and ALL", sudoList(helperEntry, "(ALL : ALL) ALL"), "", Fail, "home-a: sudo lets ubuntu run more than the WireGuard helper: (ALL : ALL) ALL", "`sudo -l -U ubuntu`"},
		{"helper and a shell on one line", sudoList(helperEntry + ", /bin/sh"), "", Fail, "more than the WireGuard helper: (root) NOPASSWD: /bin/sh", ""},
		{"helper as any user", sudoList("(ALL) NOPASSWD: /usr/local/libexec/paisans/wg-psns-f2a9"), "", Fail, "more than the WireGuard helper: (ALL) NOPASSWD: /usr/local/libexec/paisans/wg-psns-f2a9", "The helper's own line is missing too"},
		{"helper asking a password", sudoList("(root) /usr/local/libexec/paisans/wg-psns-f2a9"), "", Fail, "more than the WireGuard helper: (root) /usr/local/libexec/paisans/wg-psns-f2a9", ""},
		{"another deployment's helper", sudoList(helperEntry, "(root) NOPASSWD: /usr/local/libexec/paisans/wg-psns-0a0b"), "", Fail, "more than the WireGuard helper: (root) NOPASSWD: /usr/local/libexec/paisans/wg-psns-0a0b", ""},
		{"not allowed", "Sorry, user ubuntu may not run sudo on home-a.\n", "exit status 1", Fail, "home-a: sudo does not let ubuntu run the WireGuard helper", "`paisans host admit`"},
		{"password required", "", "sudo: a password is required", Fail, "home-a: sudo does not let ubuntu run the WireGuard helper", ""},
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

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/doctor/ -run 'TestParseSudoList|TestSudoRights'`
Expected: FAIL to compile, `undefined: ParseSudoList`, `undefined: SudoRights`.

- [ ] **Step 3: Write the parse and the judge**

Create `internal/doctor/sudo.go`:

```go
package doctor

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// SudoCommand lists what sudo lets the login user run, without asking for
// a password, in the C locale: sudo translates its answer, and the parse
// reads its English words.
const SudoCommand = "LC_ALL=C sudo -n -l"

// SudoProbe is SudoCommand's answer on one site.
type SudoProbe struct{ Site, Out, Err string }

func (p SudoProbe) site() string { return p.Site }

// SudoEntry is one line of what sudo -l lists under "may run the following
// commands": the user the commands run as, their tags, and the commands.
type SudoEntry struct {
	RunAs    string
	Tags     []string
	Commands []string
}

var (
	sudoHeader     = regexp.MustCompile(`^User \S+ may run the following commands on \S+:$`)
	sudoNotAllowed = regexp.MustCompile(`^Sorry, user \S+ may not run sudo on \S+\.$`)
	sudoRunAs      = regexp.MustCompile(`^\(([^)]*)\)\s*`)
	sudoTag        = regexp.MustCompile(`^([A-Z_]+):\s*`)
)

// ParseSudoList reads sudo -l's answer: the entries under its "may run"
// line, or none for a user sudo refuses outright. ok is false for anything
// else, so a check never takes an answer it does not know for one listing
// nothing.
func ParseSudoList(out string) (entries []SudoEntry, ok bool) {
	in := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		t := strings.TrimSpace(line)
		switch {
		case sudoNotAllowed.MatchString(t):
			return nil, true
		case sudoHeader.MatchString(t):
			in = true
			continue
		case !in || t == "":
			continue
		case !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t"):
			return nil, false
		}
		var e SudoEntry
		if m := sudoRunAs.FindStringSubmatch(t); m != nil {
			e.RunAs = strings.TrimSpace(m[1])
			t = t[len(m[0]):]
		}
		for m := sudoTag.FindStringSubmatch(t); m != nil; m = sudoTag.FindStringSubmatch(t) {
			e.Tags = append(e.Tags, m[1])
			t = t[len(m[0]):]
		}
		for _, c := range strings.Split(t, ",") {
			if c = strings.TrimSpace(c); c != "" {
				e.Commands = append(e.Commands, c)
			}
		}
		if len(e.Commands) == 0 {
			return nil, false
		}
		entries = append(entries, e)
	}
	return entries, in
}

// line is one command of e as sudo -l prints it.
func (e SudoEntry) line(command string) string {
	s := ""
	if e.RunAs != "" {
		s = "(" + e.RunAs + ") "
	}
	for _, t := range e.Tags {
		s += t + ": "
	}
	return s + command
}

// SudoRights checks that sudo lets the deployment's user run the WireGuard
// helper and nothing else, by comparing what sudo -l lists with the one
// line host admit writes: run as root, NOPASSWD, the helper's path. Every
// operator of the deployment can log in as the user, so anything more is
// theirs too, and root on the host for anything past the helper.
func SudoRights(cfg *config.Config, probes []SudoProbe) []Finding {
	d := cfg.Deployment()
	helper, err := apply.WGHelper{Iface: d.Interface()}.Path()
	var out []Finding
	for _, p := range probes {
		user := cfg.Sites[p.Site].SSHUser()
		text := p.Out
		if strings.TrimSpace(text) == "" {
			text = p.Err
		}
		missing := Finding{Section: SectionSudo, Level: Fail,
			Line: fmt.Sprintf("%s: sudo does not let %s run the WireGuard helper", p.Site, user),
			More: []string{
				"Every change to the mesh (its file, bringing it up, syncing its peers) runs the helper with sudo -n, so apply cannot manage the mesh on this site.",
				"recover:",
				fmt.Sprintf("  1. The host's owner runs `paisans host admit` for %s again, which installs %s and its line in /etc/sudoers.d/%s.", p.Site, helper, d.Prefix()),
			}}
		if err != nil || strings.Contains(text, "a password is required") {
			out = append(out, missing)
			continue
		}
		entries, ok := ParseSudoList(text)
		if !ok {
			out = append(out, Finding{Section: SectionSudo, Level: Warn, Line: fmt.Sprintf("%s: could not read what sudo lets %s run", p.Site, user), More: lastLines(text, 3)})
			continue
		}
		var extra []string
		seen := false
		for _, e := range entries {
			for _, c := range e.Commands {
				if e.RunAs == "root" && slices.Equal(e.Tags, []string{"NOPASSWD"}) && c == helper {
					seen = true
					continue
				}
				extra = append(extra, e.line(c))
			}
		}
		switch {
		case len(extra) > 0:
			more := []string{
				fmt.Sprintf("Every operator of this deployment can log in as %s, so whatever sudo allows it is theirs too, and root on the host for anything past the helper. host admit writes one line, in /etc/sudoers.d/%s; the rest comes from elsewhere in the host's sudoers.", user, d.Prefix()),
			}
			if !seen {
				more = append(more, "The helper's own line is missing too, so apply cannot manage the mesh on this site.")
			}
			more = append(more, "recover:",
				fmt.Sprintf("  1. The host's owner lists every rule for the user with `sudo -l -U %s` and removes all but the helper's with `visudo`.", user),
				fmt.Sprintf("  2. `paisans doctor --site %s` again.", p.Site))
			out = append(out, Finding{Section: SectionSudo, Level: Fail,
				Line: fmt.Sprintf("%s: sudo lets %s run more than the WireGuard helper: %s", p.Site, user, strings.Join(extra, "; ")),
				More: more})
		case seen:
			out = append(out, Finding{Section: SectionSudo, Level: OK, Line: fmt.Sprintf("%s: sudo lets %s run the WireGuard helper and nothing else", p.Site, user)})
		default:
			out = append(out, missing)
		}
	}
	return out
}
```

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./internal/doctor/ -run 'TestParseSudoList|TestSudoRights' && go vet ./internal/doctor/`
Expected: PASS. The fixture's sites log in as `ubuntu` (`internal/render/testdata/deployment.yaml`), so the lines name `ubuntu`.

- [ ] **Step 5: Commit**

```bash
git add internal/doctor/sudo.go internal/doctor/sudo_test.go
git commit -m "feat: judge what sudo lets the deployment's user run

The WireGuard helper is the one command the deployment's user may run as
root, through the one sudoers line host admit writes. Every operator can
log in as that user, so a second rule, a group's ALL, the helper allowed
as any user, or the helper's line gone are each worth a failure naming
it. sudo -l is read in the C locale and an answer the parse does not
know is a warning, never a pass.

Nothing an operator would notice yet, since no check runs it, so no
changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: The admission record against the declaration

**Files:**
- Modify: `internal/admission/record.go`
- Create: `internal/admission/differences_test.go`
- Create: `internal/doctor/admission.go`
- Test: `internal/doctor/admission_test.go`
- Modify (only when phase 6 compares the record itself): the `internal/hostprep` file Task 0, Step 3 found

**Interfaces:**
- Consumes: `admission.Record`, `admission.Ports`, `admission.Needs(config.Site) Grants`, `(Grants).Missing(Grants) []string`; `config.Site.SSHUser()`, `.SSH.PortOrDefault()`, `.ListenPort()`, `.Endpoint`, `.Has(config.Role)`, `.WebServerManagedBy()`, `config.ManagedByExternal`, `config.RoleMonitor`, `config.RoleGateway`, `config.FileName`; `roleLine`, `losses` (`internal/doctor/reach.go`); Task 1's `SectionAdmission`.
- Produces: `func (r Record) Differences(site config.Site) []string`; in `internal/doctor`: `type AdmissionProbe struct { Site string; Found bool; Record admission.Record; Err string; More []string }` with `site()` and `func (p AdmissionProbe) Pending(site config.Site) bool`; `func Admission(cfg *config.Config, probes []AdmissionProbe) []Finding`; `func pending(cfg *config.Config, section, site, line string, why ...string) Finding`; `func absentLevel(cfg *config.Config, site string) Level`; `func requestAdvice(site string) string`.

- [ ] **Step 1: Write the failing test for the comparison**

Create `internal/admission/differences_test.go`:

```go
package admission_test

import (
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
)

// A record made from a site's own request differs from it in nothing; each
// field the declaration changes afterwards is one line naming both values.
func TestDifferences(t *testing.T) {
	cfg := fixture(t)
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for _, site := range []string{"home-a", "home-b", "vm", "status", "watch", "store"} {
		req := build(t, site)
		rec := admission.NewRecord(req, req.Grants, "", at)
		if site == "status" {
			rec.WebServer = &admission.WebServer{Server: "nginx", Process: "nginx"}
		}
		if got := rec.Differences(cfg.Sites[site]); len(got) != 0 {
			t.Errorf("%s: %q", site, got)
		}
	}

	req := build(t, "home-a")
	rec := admission.NewRecord(req, req.Grants, "", at)
	s := cfg.Sites["home-a"]
	s.SSH.User = "community"
	s.SSH.Port = 2200
	s.Endpoint = ":51821"
	got := strings.Join(rec.Differences(s), "\n")
	for _, want := range []string{
		"user: admitted as " + req.User + ", and the site logs in as community",
		"ssh port: the firewall opened 22, and the site declares 2200",
		"wireguard port: the firewall opened 51820, and the site is dialled on 51821",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}

	s = cfg.Sites["home-a"]
	s.Endpoint = ""
	if got := strings.Join(rec.Differences(s), "\n"); !strings.Contains(got, "forwarding: on, and the site has no endpoint") {
		t.Errorf("forwarding:\n%s", got)
	}

	status := admission.NewRecord(build(t, "status"), admission.Grants{}, "", at)
	if got := strings.Join(status.Differences(cfg.Sites["status"]), "\n"); !strings.Contains(got, "web server: the site runs behind the owner's web server") {
		t.Errorf("web server:\n%s", got)
	}

	// The grants are not a difference: Needs and Missing name them.
	bare := admission.NewRecord(build(t, "home-a"), admission.Grants{}, "", at)
	if got := bare.Differences(cfg.Sites["home-a"]); len(got) != 0 {
		t.Errorf("grants compared: %q", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/admission/ -run TestDifferences`
Expected: FAIL to compile, `rec.Differences undefined`.

- [ ] **Step 3: Write the comparison**

In `internal/admission/record.go`, after `NewRecord`, add:

```go
// Differences names each thing the record says that the site's declaration
// does not: the user, the SSH and WireGuard ports the firewall opened,
// whether forwarding is on, and, for a monitor behind the owner's web
// server, whether that server was detected. Each is a root change, so only
// the host's owner brings the host back in line, by admitting the site
// again. The grants are not compared: Needs(site).Missing(r.Grants) names
// what is lacking, which leaves a site pending rather than wrong.
func (r Record) Differences(site config.Site) []string {
	var out []string
	if u := site.SSHUser(); r.User != u {
		out = append(out, fmt.Sprintf("user: admitted as %s, and the site logs in as %s", r.User, u))
	}
	if p := site.SSH.PortOrDefault(); r.Ports.SSH != p {
		out = append(out, fmt.Sprintf("ssh port: the firewall opened %d, and the site declares %d", r.Ports.SSH, p))
	}
	if p := site.ListenPort(); r.Ports.WireGuard != p {
		out = append(out, fmt.Sprintf("wireguard port: the firewall opened %s, and the site is dialled on %s", portOrNone(r.Ports.WireGuard), portOrNone(p)))
	}
	switch endpoint := site.Endpoint != ""; {
	case endpoint && !r.Forwarding:
		out = append(out, "forwarding: off, and the site has an endpoint, so it relays mesh traffic between sites that have none")
	case !endpoint && r.Forwarding:
		out = append(out, "forwarding: on, and the site has no endpoint, so nothing relays through it")
	}
	if site.Has(config.RoleMonitor) && site.WebServerManagedBy() == config.ManagedByExternal && r.WebServer == nil {
		out = append(out, "web server: the site runs behind the owner's web server, and was admitted when it did not, so nothing detected that server")
	}
	return out
}

// portOrNone is a port as the record and the declaration hold it, 0 being
// none.
func portOrNone(p int) string {
	if p == 0 {
		return "none"
	}
	return strconv.Itoa(p)
}
```

Run: `go test ./internal/admission/ && go vet ./internal/admission/`
Expected: PASS. Should `vm` or another fixture site show a difference, read `NewRecord` and the site's declaration: a difference there is a bug in `Differences`, never in the fixture.

- [ ] **Step 4: Write the failing test for the judge**

Create `internal/doctor/admission_test.go`:

```go
package doctor

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// admittedRecord is the record host admit leaves for a site of cfg as
// declared, with every grant its roles need.
func admittedRecord(cfg *config.Config, site string) admission.Record {
	s := cfg.Sites[site]
	g := admission.Needs(s)
	public := []int{}
	if g.PublicPorts {
		public = []int{80, 443}
	}
	rec := admission.Record{
		Version: admission.RecordVersion, Deployment: cfg.ID, User: s.SSHUser(), Grants: g,
		Forwarding: s.Endpoint != "",
		Ports:      admission.Ports{SSH: s.SSH.PortOrDefault(), WireGuard: s.ListenPort(), Public: public},
		AdmittedAt: "2026-10-15T12:00:00Z",
	}
	if s.Has(config.RoleMonitor) && s.WebServerManagedBy() == config.ManagedByExternal {
		rec.WebServer = &admission.WebServer{Server: "nginx", Process: "nginx"}
	}
	return rec
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

	for _, c := range []struct {
		name    string
		probe   AdmissionProbe
		level   Level
		line    string
		more    []string
		pending bool
	}{
		{"admitted", AdmissionProbe{Site: "home-a", Found: true, Record: admittedRecord(cfg, "home-a")}, OK, "home-a: admitted as ubuntu, with every grant its roles need", nil, false},
		{"not admitted", AdmissionProbe{Site: "home-b"}, Warn, "home-b: this deployment is not admitted on its host", []string{"roles: data, apps", "Until it joins, the deployment is without one of 3 etcd votes", "`paisans host request --site home-b > home-b.admit`"}, true},
		{"without the watchdog", AdmissionProbe{Site: "home-b", Found: true, Record: noWatchdog}, Warn, "home-b: admitted without --watchdog, which its roles need", []string{"--watchdog grants Patroni the machine's watchdog"}, true},
		{"gateway without the ports", AdmissionProbe{Site: "vm", Found: true, Record: noPorts}, Fail, "vm: admitted without --public-ports, which its roles need", []string{"--public-ports lets the deployment bind ports 80 and 443"}, true},
		{"differs", AdmissionProbe{Site: "home-a", Found: true, Record: moved}, Fail, "home-a: the admission record differs from paisans-stack.yaml", []string{"ssh port: the firewall opened 2222, and the site declares 22", "host prepare refuses"}, false},
		{"unreadable", AdmissionProbe{Site: "home-a", Err: `the admission record for "f2a9" is refused:`, More: []string{"- version: \"2\""}}, Fail, "home-a: its admission record cannot be read", []string{`- version: "2"`, "`paisans host admit`"}, false},
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
			if got := c.probe.Pending(cfg.Sites[c.probe.Site]); got != c.pending {
				t.Errorf("Pending %v, want %v", got, c.pending)
			}
		})
	}
}
```

- [ ] **Step 5: Run it to verify it fails**

Run: `go test ./internal/doctor/ -run TestAdmission`
Expected: FAIL to compile, `undefined: AdmissionProbe`, `undefined: Admission`.

- [ ] **Step 6: Write the judge**

Create `internal/doctor/admission.go`:

```go
package doctor

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
)

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
}

func (p AdmissionProbe) site() string { return p.Site }

// Pending reports whether the site has not joined because of its host: no
// record, or one without a grant its roles need. Nothing more is asked of a
// pending site. A record that could not be read is not pending: the rest
// of the site is still checked.
func (p AdmissionProbe) Pending(site config.Site) bool {
	if p.Err != "" {
		return false
	}
	return !p.Found || len(admission.Needs(site).Missing(p.Record.Grants)) > 0
}

// grantWhy is what each grant gives, by the flag that gives it.
var grantWhy = map[string]string{
	"watchdog":     "--watchdog grants Patroni the machine's watchdog, which a data site needs to fail over safely, and which can reboot the machine.",
	"public-ports": "--public-ports lets the deployment bind ports 80 and 443, which the gateway and a monitor running Paisans' Caddy serve on; every local user can then bind ports below 1024.",
}

// Admission checks the site's admission record against its declaration.
// A site whose host has not admitted the deployment, or not with every
// grant its roles need, is pending: the deployment runs without it, so it
// is a warning naming what it is without and how its owner admits it (a
// failure on a gateway, absentLevel). A record that cannot be read, or that
// says something the declaration does not, is a failure: each difference
// is a root change only the owner makes, and host prepare refuses until
// the record matches.
func Admission(cfg *config.Config, probes []AdmissionProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		s := cfg.Sites[p.Site]
		switch {
		case p.Err != "":
			more := append([]string{}, p.More...)
			more = append(more, "recover:", fmt.Sprintf("  1. The host's owner runs `paisans host admit` for %s again, which rewrites it.", p.Site))
			out = append(out, Finding{Section: SectionAdmission, Level: Fail, Line: fmt.Sprintf("%s: its admission record cannot be read", p.Site), More: append([]string{p.Err}, more...)})
		case !p.Found:
			out = append(out, pending(cfg, SectionAdmission, p.Site,
				fmt.Sprintf("%s: this deployment is not admitted on its host", p.Site),
				fmt.Sprintf("Its login as %s works, but the host holds no admission record for this deployment, so its owner has not run host admit for it.", s.SSHUser())))
		default:
			if missing := admission.Needs(s).Missing(p.Record.Grants); len(missing) > 0 {
				flags := make([]string, len(missing))
				var why []string
				for i, m := range missing {
					flags[i] = "--" + m
					why = append(why, grantWhy[m])
				}
				out = append(out, pending(cfg, SectionAdmission, p.Site,
					fmt.Sprintf("%s: admitted without %s, which its roles need", p.Site, strings.Join(flags, " and ")), why...))
				continue
			}
			if diffs := p.Record.Differences(s); len(diffs) > 0 {
				more := append([]string{}, diffs...)
				more = append(more,
					"Each is a root change only the host's owner makes, and host prepare refuses until the record matches.",
					"recover:", requestAdvice(p.Site))
				out = append(out, Finding{Section: SectionAdmission, Level: Fail, Line: fmt.Sprintf("%s: the admission record differs from %s", p.Site, config.FileName), More: more})
				continue
			}
			out = append(out, Finding{Section: SectionAdmission, Level: OK, Line: fmt.Sprintf("%s: admitted as %s, with every grant its roles need", p.Site, p.Record.User)})
		}
	}
	return out
}

// pending is a site the deployment runs without because its host has not
// admitted it, or not with every grant its roles need: its roles, why, what
// the deployment is without until it joins, and how its owner admits it.
func pending(cfg *config.Config, section, site, line string, why ...string) Finding {
	more := append([]string{roleLine(cfg, site)}, why...)
	if lost := losses(cfg, site); lost != "" {
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

// requestAdvice is how a site's owner admits it, or admits it again.
func requestAdvice(site string) string {
	return fmt.Sprintf("  1. `paisans host request --site %s > %s.admit` writes the request its owner admits it from; they run `sudo paisans host admit --request %s.admit --execute` on the host, with the grants it asks for, and the next `paisans apply` joins it.", site, site, site)
}
```

- [ ] **Step 7: Run it to verify it passes**

Run: `go test ./internal/doctor/ -run TestAdmission && go vet ./internal/doctor/`
Expected: PASS.

- [ ] **Step 8: One comparison, used by `host prepare` too**

Only when Task 0, Step 3 found phase 6's `host prepare` comparing the record's user, ports or forwarding with the declaration itself: replace that comparison with `rec.Differences(site)`, keeping `host prepare`'s refusal and its `host admit` line around it, and keep its tests passing; where a test there asserts the old wording, change it to the wording `Differences` gives.

Run: `go test ./internal/hostprep/ ./internal/admission/ ./internal/doctor/`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/admission/record.go internal/admission/differences_test.go internal/doctor/admission.go internal/doctor/admission_test.go
git add internal/hostprep   # only when Step 8 changed it
git commit -m "feat: compare a site's admission record with its declaration

The record is what the host's owner granted and opened; the declaration
is what the deployment now needs. A site whose host holds no record, or
one without a grant its roles need, is pending: the deployment runs
without it, so doctor will say so as a warning with what it lacks and
the host request line, and a failure for a gateway, which apply cannot
run without. A record that says another user, other ports or other
forwarding is a failure, since only the owner can change those.
Differences is the one comparison, so host prepare and doctor cannot
disagree about it.

Nothing an operator would notice yet, since no check runs it, so no
changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: The rootless checks in the graph

**Files:**
- Modify: `internal/doctor/checks.go`, `internal/doctor/doctor.go`, `internal/doctor/graph.go`, `internal/doctor/dnschecks.go` (`addDNS`'s need)
- Modify: `internal/doctor/graph_test.go`
- Modify: `cmd/paisans/doctor.go`
- Modify: `cmd/paisans/doctor_test.go`, `cmd/paisans/doctor_dns_test.go`, `cmd/paisans/output_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: Tasks 1 to 3's probes and judges; `admission.Read(Reader, token string) (Record, error)`, `admission.ErrNotAdmitted`, `admission.Path`, `admission.EncodeRecord`; `doctorReads.read` (`cmd/paisans/doctor.go`); the graph's `Node`, `Hard`, `bySite`.
- Produces: `doctor.Reads` gains `Admission(site string) AdmissionProbe`, `User(site string) UserProbe`, `Docker(site string) DockerProbe`, `Linger(site string) LingerProbe`, `Sudo(site string) SudoProbe`; `doctor.Input` gains `Admission []AdmissionProbe`, `Users []UserProbe`, `Docker []DockerProbe`, `Linger []LingerProbe`, `Sudo []SudoProbe`; nodes `admission <site>`, `user <site>`, `docker <site>`, `docker engine <site>`, `linger <site>`, `sudo rights <site>`. In `cmd/paisans` tests: `doctorHost.files map[string]string`, `func admitHosts(t *testing.T, cfg *config.Config, hosts map[string]doctorHost)`, `func withDoctorHosts(t *testing.T, cfg *config.Config, hosts map[string]doctorHost)`, `func admittedRecord(cfg *config.Config, site string) admission.Record`, `func sudoHelperOnly(user, host, helper string) string`.

- [ ] **Step 1: Teach the test reads the new questions**

In `internal/doctor/graph_test.go`, add to `fixtureReads`, below `Leftovers`. Each answers from the fixture's `Input` when the site is there, and otherwise as a sound admitted host:

```go
// fixtureConfig is the fixture the tests load, once, for the reads that
// answer from the declaration.
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
	return LingerProbe{Site: site, Out: "yes\n"}
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

Add `"path/filepath"`, `"sync"` and `"github.com/paisans-software/paisans-cli/internal/config"` to its imports.

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
// A site that answers but has not admitted the deployment is one warning,
// with the host request line, and nothing more is read from it.
func TestAPendingSiteIsCheckedNoFurther(t *testing.T) {
	cfg := fixture(t)
	fix := healthyInput()
	fix.Admission = []AdmissionProbe{{Site: "home-b"}}
	reads := &fixtureReads{in: fix, log: &events{}}
	rec := &ui.Recorder{}
	report, _ := Checks(cfg, fix.Sites, reads).Run(rec, 0)

	f := find(t, report.Findings, SectionAdmission, "home-b: this deployment is not admitted on its host")
	if f.Level != Warn || !strings.Contains(strings.Join(f.More, "\n"), "`paisans host request --site home-b > home-b.admit`") {
		t.Errorf("home-b: %+v", f)
	}
	want := "Not checked because of this: user, docker, docker engine, linger, sudo rights, mesh, patroni, containers, settings talk, clocks, leftovers on home-b."
	if !rec.Has("explain", want) && !rec.Has("detail", want) {
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
Expected: FAIL to compile, `f.in.Admission undefined` (no such field in `Input`), then, once Step 3's fields exist, FAIL on the read order and the pruning lists.

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
	findings = append(findings, Linger(in.Linger)...)
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

Rewrite `Checks`' comment's first paragraph to say what it does now:

```go
// Checks is every check doctor makes on sites, the ones it asks, as a
// graph: each site's reach first, then its admission record, which every
// other read of the site needs, since nothing is asked of a host that has
// not admitted the deployment; then the deployment's user, its rootless
// Docker, which every check that runs docker there needs, linger and sudo;
// then from the bottom of the stack up, the order Diagnose reports in. Each
// check's judge is the section function Diagnose calls, given the probes of
// the check's own subject, so the graph's findings for a complete Input are
// Diagnose's.
```

After the `for _, site := range sites { g.Add(Node{Name: reach(site) ...}) }` loop, add:

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
					if p.Pending(cfg.Sites[site]) {
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
			Judge: func(in Input) []Finding { return Linger(bySite(in.Linger, site, LingerProbe.site)) },
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
* `etcd`, `etcd version`, `patroni` and `pocket-id <app>` keep theirs.

In `internal/doctor/dnschecks.go`, `addDNS`, the `caddy certificates <site>` node's `Hard: []Need{Hard(SectionReach + " " + site)}` becomes `Hard: []Need{Hard(SectionDocker + " " + site)}`, since it reads Caddy's log with `docker compose logs`.

Run: `go test ./internal/doctor/`
Expected: PASS, including `TestGraphFindsWhatDiagnoseFinds` (its fixture inputs hold no rootless probes, so neither side reports any) and `TestStreamingPrintsAsChecksEnd`. Where `TestPruningIsReportedOnce`'s "Not checked" list or `TestDockerDownStopsWhatRunsDocker`'s differs from what the run prints, compare the run's list with the node order above: the test is right when it names every node a failed need cut, in the graph's order.

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

Add `"github.com/paisans-software/paisans-cli/internal/admission"` to its imports. Update `doctorReads`' comment's list of what it sends: "Every command here is one of internal/doctor's read commands, patroni's ClusterCommand, apply.LookAtInstances' question, or the host check's inventory (hostcheck.Inspect), and it reads the admission record; none writes."

- [ ] **Step 6: Admit the fake hosts in the command tests**

In `cmd/paisans/doctor_test.go`:

Give `doctorHost` a `files map[string]string` field, after `answers`, and make `ReadFile` answer from it:

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
// admittedRecord is the record host admit leaves for a site of cfg as
// declared, with every grant its roles need.
func admittedRecord(cfg *config.Config, site string) admission.Record {
	s := cfg.Sites[site]
	g := admission.Needs(s)
	public := []int{}
	if g.PublicPorts {
		public = []int{80, 443}
	}
	rec := admission.Record{
		Version: admission.RecordVersion, Deployment: cfg.ID, User: s.SSHUser(), Grants: g,
		Forwarding: s.Endpoint != "",
		Ports:      admission.Ports{SSH: s.SSH.PortOrDefault(), WireGuard: s.ListenPort(), Public: public},
		AdmittedAt: "2026-10-15T12:00:00Z",
	}
	if s.Has(config.RoleMonitor) && s.WebServerManagedBy() == config.ManagedByExternal {
		rec.WebServer = &admission.WebServer{Server: "nginx", Process: "nginx"}
	}
	return rec
}

// sudoHelperOnly is sudo -l's answer for a user host admit gave the helper
// and nothing else.
func sudoHelperOnly(user, host, helper string) string {
	return "Matching Defaults entries for " + user + " on " + host + ":\n" +
		"    env_reset, mail_badpass, use_pty\n\n" +
		"User " + user + " may run the following commands on " + host + ":\n" +
		"    (root) NOPASSWD: " + helper + "\n"
}

// admitHosts makes each fake host one whose owner admitted its site as the
// configuration declares it, with a sound rootless setup, unless the test
// gave it another answer already: the record, a user in no privileged
// group, its own rootless Docker 29.9.1, linger on, and sudo for the
// WireGuard helper alone.
func admitHosts(t *testing.T, cfg *config.Config, hosts map[string]doctorHost) {
	t.Helper()
	d := cfg.Deployment()
	path, err := admission.Path(d.Token())
	if err != nil {
		t.Fatal(err)
	}
	helper, err := apply.WGHelper{Iface: d.Interface()}.Path()
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
		if _, given := h.files[path]; !given {
			data, err := admission.EncodeRecord(admittedRecord(cfg, site))
			if err != nil {
				t.Fatal(err)
			}
			h.files[path] = string(data)
		}
		if h.answers == nil {
			h.answers = map[string]string{}
		}
		for command, answer := range map[string]string{
			doctor.UserCommand:   "uid 1000\nuser " + s.SSHUser() + "\ngroups " + s.SSHUser() + "\n",
			doctor.DockerCommand: "host unix:///run/user/1000/docker.sock\nengine 29.9.1\nsecurity [\"name=seccomp,profile=builtin\",\"name=rootless\",\"name=cgroupns\"]\n",
			doctor.LingerCommand: "yes\n",
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

and after the `exact = append(exact, "read "+cfg.Deployment().Manifest(), "read "+registry.Path)` line:

```go
	recordPath, _ := admission.Path(cfg.Deployment().Token())
	exact = append(exact, "read "+recordPath)
```

Add `"github.com/paisans-software/paisans-cli/internal/admission"` to the test file's imports.

- [ ] **Step 7: The command tests' expectations**

In `TestDoctorDiagnosesTheStuckReplicaAndOnlyReads`, replace the `Not checked` expectation:

```go
		"Not checked because of this: admission, user, docker, docker engine, linger, sudo rights, mesh, patroni, containers, settings talk, clocks, leftovers on home-b.",
```

and add to its `want` list:

```go
		"done: check admission home-a admitted as ubuntu, with every grant its roles need",
		"done: check user home-a ubuntu is in no privileged group",
		"done: check docker home-a its rootless Docker answers at unix:///run/user/1000/docker.sock",
		"done: check docker engine home-a Docker Engine 29.9.1",
		"done: check sudo rights home-a sudo lets ubuntu run the WireGuard helper and nothing else",
```

Run: `go test ./cmd/paisans/ -run 'Doctor|DefaultOutput'`
Expected: PASS. Where another doctor test asserts a summary count ("N checks passed", "N skipped"), recount it: each site that answered adds six passed checks (admission, user, docker, docker engine, linger, sudo rights), and each site that did not adds six skipped. Change the number, never the check's wording. Where a test's host answers a command by prefix, check that no new command matches one of its prefixes: `UserCommand` and `DockerCommand` start `printf 'uid` and `printf 'host`, `LingerCommand` `loginctl`, `SudoCommand` `LC_ALL=C sudo`.

- [ ] **Step 8: The changelog**

In `CHANGELOG.md`, under `[Unreleased]`, `### Added`, add:

```markdown
- `doctor` checks each site as its deployment's user: the admission record against the declaration, no privileged group, its own rootless Docker at Engine 29.5 or later, linger on, and sudo for the WireGuard helper alone.
```

- [ ] **Step 9: Run the touched packages**

Run: `gofmt -l internal/doctor cmd/paisans internal/admission && go vet ./internal/doctor/ ./cmd/paisans/ ./internal/admission/ && go test ./internal/doctor/ ./cmd/paisans/ ./internal/admission/`
Expected: nothing listed; PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/doctor/checks.go internal/doctor/doctor.go internal/doctor/graph.go internal/doctor/dnschecks.go internal/doctor/graph_test.go cmd/paisans/doctor.go cmd/paisans/doctor_test.go cmd/paisans/doctor_dns_test.go cmd/paisans/output_test.go CHANGELOG.md
git commit -m "feat: doctor checks each site's admission and rootless setup

Every command now runs as the deployment's own user against its own
rootless daemon, so doctor asks first whether that holds: the admission
record against the declaration, the user's groups, its daemon and Engine,
linger, and what sudo allows. The record comes first and every other read
of a site needs it, so a host that has not admitted the deployment is one
warning rather than a failure per check; the daemon comes before every
check that runs docker, so a stopped daemon is one failure.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Pending and down sites, as warnings

**Files:**
- Modify: `internal/doctor/reach.go`
- Create: `internal/doctor/reach_test.go`
- Modify: `cmd/paisans/doctor.go`, `cmd/paisans/doctor_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: Task 3's `pending`, `absentLevel`, `roleLine`, `losses`, `sshReason`; phase 6's classification (*Phase 6 interfaces this plan assumes*: `sitestate.Classify`, `sitestate.Class`, `sitestate.Pending`, `sitestate.Down`).
- Produces: `type SiteState int` with `Unclassified`, `Pending`, `Down`; `type SiteClass struct { State SiteState; Reason string }`; `SiteReach.Class SiteClass`; in `cmd/paisans`: `var doctorClassify func(cfg *config.Config, transports map[string]apply.Transport) map[string]doctor.SiteClass`, `type siteClasses struct`, `func (d doctorReads) class(site string) doctor.SiteClass`, the field `doctorReads.classes *siteClasses` and `doctorReads.all map[string]apply.Transport`.

- [ ] **Step 1: Write the failing tests**

Create `internal/doctor/reach_test.go`:

```go
package doctor

import (
	"strings"
	"testing"
)

// A site that did not answer is a warning once the classification says it
// is pending or down, saying what the deployment is without and what to
// run; a gateway is a failure either way, since apply refuses to run
// without it; a site the classification did not place is a failure, as
// before. A refused login and a timeout read the same.
func TestAbsentSites(t *testing.T) {
	cfg := fixture(t)
	timeout := "ssh: connect to host home-b.local port 22: Operation timed out"
	refused := "paisans-f2a9@home-b.local: Permission denied (publickey)."
	for _, c := range []struct {
		name  string
		reach SiteReach
		level Level
		line  string
		more  []string
	}{
		{"pending", SiteReach{Site: "home-b", Destination: "ubuntu@home-b.local", Err: refused, Class: SiteClass{State: Pending, Reason: "its owner has not admitted this deployment"}}, Warn,
			"home-b: pending: its owner has not admitted this deployment",
			[]string{"roles: data, apps", "Until it joins, the deployment is without one of 3 etcd votes", "`paisans host request --site home-b > home-b.admit`"}},
		{"down, timed out", SiteReach{Site: "home-b", Destination: "ubuntu@home-b.local", Err: timeout, Class: SiteClass{State: Down}}, Warn,
			"home-b: its login is refused or it does not answer: the host is down, or its owner evicted this deployment",
			[]string{"While it is down the deployment is without one of 3 etcd votes", "ssh to ubuntu@home-b.local: Operation timed out", "`paisans site remove --site home-b --host-gone` removes it from the deployment"}},
		{"down, refused", SiteReach{Site: "home-b", Destination: "ubuntu@home-b.local", Err: refused, Class: SiteClass{State: Down}}, Warn,
			"home-b: its login is refused or it does not answer: the host is down, or its owner evicted this deployment",
			[]string{"`paisans site remove --site home-b --host-gone`"}},
		{"gateway down", SiteReach{Site: "vm", Destination: "ubuntu@vm.example.org", Err: timeout, Class: SiteClass{State: Down}}, Fail,
			"vm: its login is refused or it does not answer: the host is down, or its owner evicted this deployment",
			[]string{"its gateway"}},
		{"gateway pending", SiteReach{Site: "vm", Destination: "ubuntu@vm.example.org", Err: refused, Class: SiteClass{State: Pending, Reason: "its owner has not admitted this deployment"}}, Fail,
			"vm: pending: its owner has not admitted this deployment",
			[]string{"`paisans host request --site vm > vm.admit`"}},
		{"unclassified", SiteReach{Site: "home-b", Destination: "ubuntu@home-b.local", Err: timeout}, Fail,
			"home-b: ssh to ubuntu@home-b.local did not answer (Operation timed out)",
			[]string{"Start the host or fix the route to it."}},
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
// where every site answers never asks.
func TestDoctorClassifiesOnlyWhenASiteDoesNotAnswer(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	var sent, writes []string
	withDoctorHosts(t, cfg, stuckHosts(cfg, &sent, &writes))
	// After withDoctorHosts, which places nothing, so this one is in force;
	// its cleanup restores the original.
	asked := 0
	doctorClassify = func(c *config.Config, all map[string]apply.Transport) map[string]doctor.SiteClass {
		asked++
		if len(all) != len(c.Sites) {
			t.Errorf("classification got %d transports, want one per declared site", len(all))
		}
		return map[string]doctor.SiteClass{"home-b": {State: doctor.Down}}
	}
	rec := withRecorder(t, true)
	_ = runDoctor([]string{"--config", fixtureConfig(), "--site", "home-a", "--site", "home-b"})
	if asked != 1 {
		t.Errorf("classified %d times, want once", asked)
	}
	if out := rec.Lines(); !strings.Contains(out, "check reach home-b its login is refused or it does not answer") {
		t.Errorf("home-b is not reported down:\n%s", out)
	}
}
```

Run: `go test ./internal/doctor/ -run TestAbsentSites; go test ./cmd/paisans/ -run TestDoctorClassifies`
Expected: FAIL to compile, `unknown field Class in struct literal of type SiteReach`, `undefined: doctorClassify`.

- [ ] **Step 2: Pending and down in `Reach`**

In `internal/doctor/reach.go`, add after `SiteReach`:

```go
// SiteState is what the run's classification made of a site that did not
// answer: whether it has joined the deployment, read from the deployment
// (the gateway's deployed record, etcd's and Patroni's members) and never
// from the site's own host, since a host that is down, one whose owner
// evicted the deployment and one that never admitted it look alike from
// outside.
type SiteState int

const (
	// Unclassified is a site the classification said nothing of, or a run
	// whose classification could not be made.
	Unclassified SiteState = iota
	// Pending is a site that has not joined: its host has not admitted the
	// deployment, or not with every grant its roles need.
	Pending
	// Down is a site that has joined and does not answer.
	Down
)

// SiteClass is a site's state and the reason the classification gives.
type SiteClass struct {
	State  SiteState
	Reason string
}
```

Add to `SiteReach`, after `Err`:

```go
	// Class is the classification of a site that did not answer, filled
	// only then (see cmd/paisans, doctorReads.Reach).
	Class SiteClass
```

Rewrite `Reach`'s comment and its `default:` branch:

```go
// Reach is one line per site. A site that did not answer is never a crash:
// the rest of the report is made from the sites that did, and this says
// what the deployment is without while it is gone, so an operator can tell
// an inconvenience from an outage at a glance. A site the classification
// found pending or down is a warning, since the deployment runs on the sites
// it has; a gateway is a failure, since apply refuses to run without it,
// and so is a site the classification did not place.
func Reach(cfg *config.Config, list []SiteReach) []Finding {
	var out []Finding
	for _, r := range list {
		ssh := "ssh to " + r.Destination + ": " + sshReason(firstLine(r.Err))
		switch {
		case r.Answered():
			out = append(out, Finding{Section: SectionReach, Level: OK, Line: fmt.Sprintf("%s: %s answers", r.Site, r.Destination)})
		case r.Class.State == Pending:
			out = append(out, pending(cfg, SectionReach, r.Site, fmt.Sprintf("%s: pending: %s", r.Site, r.Class.Reason), ssh))
		case r.Class.State == Down:
			more := []string{roleLine(cfg, r.Site)}
			if lost := losses(cfg, r.Site); lost != "" {
				more = append(more, "While it is down the deployment is without "+lost+".")
			}
			more = append(more, ssh,
				"recover:",
				"  1. Start the host or fix the route to it.",
				fmt.Sprintf("  2. If its owner evicted this deployment, `paisans site remove --site %s --host-gone` removes it from the deployment.", r.Site))
			out = append(out, Finding{Section: SectionReach, Level: absentLevel(cfg, r.Site),
				Line: fmt.Sprintf("%s: its login is refused or it does not answer: the host is down, or its owner evicted this deployment", r.Site),
				More: more})
		default:
			more := []string{roleLine(cfg, r.Site)}
			if lost := losses(cfg, r.Site); lost != "" {
				more = append(more, "While it is down the deployment is without "+lost+".")
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
// time a site does not answer and never otherwise, so a healthy run never
// pays for it.
type siteClasses struct {
	once   sync.Once
	byName map[string]doctor.SiteClass
}

// class is how the run's classification placed site; nothing, for reads
// made without one (a test's).
func (d doctorReads) class(site string) doctor.SiteClass {
	if d.classes == nil {
		return doctor.SiteClass{}
	}
	d.classes.once.Do(func() { d.classes.byName = doctorClassify(d.cfg, d.all()) })
	return d.classes.byName[site]
}

// doctorClassify is phase 6's classification of every declared site,
// mapped to doctor's: pending, down, or nothing for a site it found ready
// or a run it could not classify, which doctor then reports as a site that
// did not answer. Tests replace it.
var doctorClassify = func(cfg *config.Config, all map[string]apply.Transport) map[string]doctor.SiteClass {
	classes, err := sitestate.Classify(cfg, all)
	if err != nil {
		return nil
	}
	out := map[string]doctor.SiteClass{}
	for site, c := range classes {
		switch c.State {
		case sitestate.Pending:
			out[site] = doctor.SiteClass{State: doctor.Pending, Reason: c.Reason}
		case sitestate.Down:
			out[site] = doctor.SiteClass{State: doctor.Down, Reason: c.Reason}
		}
	}
	return out
}
```

Add phase 6's package to the imports (`internal/sitestate` as assumed; Task 0 reconciled the name). Make `Reach` fill the class when the site did not answer:

```go
func (d doctorReads) Reach(site string) doctor.SiteReach {
	t := d.transports[site]
	r := doctor.SiteReach{Site: site, Destination: t.Describe()}
	if out, err := t.Run(doctor.ReachCommand); err != nil {
		r.Err = errText(out, err)
		r.Class = d.class(site)
	}
	return r
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

In `withDoctorHosts` (`cmd/paisans/doctor_test.go`), make the classification place nothing unless a test says otherwise, so the existing expectations hold:

```go
	savedClassify := doctorClassify
	doctorClassify = func(*config.Config, map[string]apply.Transport) map[string]doctor.SiteClass { return nil }
	t.Cleanup(func() { doctorClassify = savedClassify })
```

placed before `withDoctorHosts`' own `t.Cleanup`. `TestDoctorClassifiesOnlyWhenASiteDoesNotAnswer` sets its own after calling `withDoctorHosts`, so its replacement is the one in force.

A transport to a site outside `--site` is made only when the classification runs, so `TestDoctorSiteNarrowsTheHostsReached`, whose one host answers, still contacts nothing else; a test whose classification runs must give every declared site's host, as `stuckHosts` does.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/doctor/ ./cmd/paisans/ -run 'Reach|Absent|Doctor'`
Expected: PASS.

- [ ] **Step 5: The changelog**

In `CHANGELOG.md`, under `[Unreleased]`, `### Changed`, add:

```markdown
- `doctor` reports a site pending admission, or down after joining, as a warning naming what the deployment is without and how to admit or remove it; a gateway stays a failure.
```

- [ ] **Step 6: Commit**

```bash
git add internal/doctor/reach.go internal/doctor/reach_test.go cmd/paisans/doctor.go cmd/paisans/doctor_test.go CHANGELOG.md
git commit -m "feat: doctor warns of a site pending admission or down

A deployment runs on the sites it has, so a site whose host has not
admitted it yet, or a joined site that does not answer, is a risk to
know about rather than an outage. Whether a site has joined is read from
the deployment, through the run's classification, which doctor asks once
and only when a site does not answer. Each such site is a warning with
what the deployment is without and the host request or site remove line;
a gateway stays a failure, since apply refuses to run without one.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: The gateway answers as itself, from outside

**Files:**
- Create: `internal/doctor/answer.go`, `internal/doctor/answer_test.go`
- Modify: `internal/doctor/checks.go`, `internal/doctor/doctor.go`, `internal/doctor/graph.go`, `internal/doctor/graph_test.go`
- Modify: `cmd/paisans/doctor.go`, `cmd/paisans/doctor_test.go`
- Modify: phase 6's test file for its gateway answer check, in `internal/apply`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: phase 6's `apply.CheckGatewayAnswer`, `apply.HTTPFetcher` (*Phase 6 interfaces this plan assumes*); `config.Config.GatewaySites()`; `deployment.Deployment.Token()`.
- Produces: `const SectionAnswer = "gateway answer"`; `type AnswerProbe struct { Site, Err string; More []string }` with `site()`; `func GatewayAnswers(cfg *config.Config, probes []AnswerProbe) []Finding`; `Reads.GatewayAnswer(site string) AnswerProbe`; `Input.Answers []AnswerProbe`; nodes `gateway answer <site>`; in `cmd/paisans`: `var doctorGatewayAnswer func(ctx context.Context, cfg *config.Config, site string) error`.

- [ ] **Step 1: Pin the shared check against an echo**

Phase 6's tests for its gateway check use a fake fetcher, with cases for a matching body, another body, another server and no answer. Add, beside them and in their form:

```go
// A server that answers any path with the path itself carries the token in
// its body; only the token as the whole body is this deployment.
func TestGatewayAnswerRefusesAnEcho(t *testing.T) {
	// The fake returns, for the 443 fetch, status 200 and the body
	// "/.well-known/paisans/f2a9\n", which contains the token f2a9; the 80
	// fetch answers as the matching case does. Build the fake as phase 6's
	// "another body" case builds its own.
	err := /* phase 6's check, called as its "another body" case calls it, with that fake */
	if err == nil || !strings.Contains(err.Error(), "443 on vm answers, but not as this deployment") {
		t.Errorf("an echo of the path passed: %v", err)
	}
}
```

Write the two marked lines in the form phase 6's own "another body" case takes; the fake's body and the expected error are the test.

Run: `go test ./internal/apply/ -run TestGatewayAnswer`
Expected: PASS when phase 6 compares the whole body with the token. If it FAILS, the shared check accepts a body that holds the token among other text: make it compare the body, with surrounding white space trimmed, with the token exactly, which fixes `apply`'s step as well, and run again.

- [ ] **Step 2: Write the failing doctor tests**

Create `internal/doctor/answer_test.go`:

```go
package doctor

import (
	"strings"
	"testing"
)

func TestGatewayAnswers(t *testing.T) {
	cfg := fixture(t)
	fs := GatewayAnswers(cfg, []AnswerProbe{
		{Site: "vm"},
		{Site: "vm", Err: "443 on vm answers, but not as this deployment", More: []string{"the body was \"hello\""}},
	})
	if len(fs) != 2 {
		t.Fatalf("want two findings:\n%s", dump(fs))
	}
	if fs[0].Level != OK || fs[0].Line != "vm: 443 and 80 answer as this deployment" {
		t.Errorf("ok: %v %q", fs[0].Level, fs[0].Line)
	}
	text := strings.Join(fs[1].More, "\n")
	if fs[1].Level != Fail || fs[1].Line != "vm: 443 on vm answers, but not as this deployment" {
		t.Errorf("fail: %v %q", fs[1].Level, fs[1].Line)
	}
	for _, want := range []string{"the body was \"hello\"", "/.well-known/paisans/f2a9", "`sudo ss -Hltnp`", "`paisans gateway move`"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
}
```

In `internal/doctor/graph_test.go`, add to `fixtureReads`:

```go
func (f *fixtureReads) GatewayAnswer(site string) AnswerProbe {
	f.asked("answer " + site)
	for _, p := range f.in.Answers {
		if p.Site == site {
			return p
		}
	}
	return AnswerProbe{Site: site}
}
```

and to `TestBoundOfOneRunsInOrder`'s `want`, after `"leftovers vm"`:

```go
		"answer vm",
```

Add, below `TestDockerDownStopsWhatRunsDocker`:

```go
// The gateway is asked from outside even when its ssh does not answer.
func TestGatewayAnswerNeedsNoSsh(t *testing.T) {
	cfg := fixture(t)
	fix := healthyInput()
	fix.Reach = []SiteReach{
		{Site: "home-a", Destination: "ubuntu@home-a.local"},
		{Site: "home-b", Destination: "ubuntu@home-b.local"},
		{Site: "vm", Destination: "ubuntu@vm.example.org", Err: "ssh: connect to host vm.example.org port 22: Operation timed out"},
	}
	reads := &fixtureReads{in: fix, log: &events{}}
	Checks(cfg, fix.Sites, reads).Run(ui.Discard, 0)
	if !reads.log.has("answer vm") {
		t.Errorf("the gateway was not asked from outside: %v", reads.log.list)
	}
}
```

Run: `go test ./internal/doctor/ -run 'TestGatewayAnswer|TestBoundOfOne'`
Expected: FAIL to compile, `undefined: GatewayAnswers`, `undefined: AnswerProbe`.

- [ ] **Step 3: The judge, the probe and the node**

Create `internal/doctor/answer.go`:

```go
package doctor

import (
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// SectionAnswer is the gateway's own answer, asked from outside, after
// everything read on the sites.
const SectionAnswer = "gateway answer"

// AnswerProbe is whether the gateway answered /.well-known/paisans/<token>
// as this deployment, fetched from the workstation with the check apply
// runs at the end of the gateway's step. Err is that check's error, its
// first line, and More the rest.
type AnswerProbe struct {
	Site string
	Err  string
	More []string
}

func (p AnswerProbe) site() string { return p.Site }

// GatewayAnswers judges each gateway's answer. The deployment's user cannot
// read the host's ruleset or another user's sockets, so a port taken after
// admission, and above all one taken by a DNAT rule with no listener, is
// invisible from the host; asking from outside is the one way to see it.
func GatewayAnswers(cfg *config.Config, probes []AnswerProbe) []Finding {
	path := "/.well-known/paisans/" + cfg.Deployment().Token()
	var out []Finding
	for _, p := range probes {
		if p.Err == "" {
			out = append(out, Finding{Section: SectionAnswer, Level: OK, Line: fmt.Sprintf("%s: 443 and 80 answer as this deployment", p.Site)})
			continue
		}
		more := append([]string{}, p.More...)
		more = append(more,
			fmt.Sprintf("doctor fetched %s through %s's public address from this workstation, as apply does at the end of the gateway's step, and something other than this deployment's Caddy answered, or nothing did. A listener, a container of the owner's system Docker or a DNAT rule may hold the port, and only root on the host can see which.", path, p.Site),
			"recover:",
			fmt.Sprintf("  1. The host's owner finds what holds 443 and 80 on %s: `sudo ss -Hltnp`, `sudo docker ps` for the system daemon, and `sudo nft list ruleset` for a DNAT rule.", p.Site),
			"  2. They free the ports, or the operators move the gateway to another site with `paisans gateway move`.",
			fmt.Sprintf("  3. `paisans doctor --site %s` again.", p.Site))
		out = append(out, Finding{Section: SectionAnswer, Level: Fail, Line: fmt.Sprintf("%s: %s", p.Site, p.Err), More: more})
	}
	return out
}
```

In `internal/doctor/checks.go`, add to `Reads`:

```go
	// GatewayAnswer fetches the gateway's /.well-known/paisans/<token> from
	// the workstation, through its public address, as apply does.
	GatewayAnswer(site string) AnswerProbe
```

and in `Checks`, after the `leftovers` loop and before `if dr, ok := reads.(DNSReads)`:

```go
	// Each gateway asked, from outside: it needs no ssh, so a gateway whose
	// ssh does not answer is still asked.
	for _, site := range cfg.GatewaySites() {
		if !asked[site] {
			continue
		}
		g.Add(Node{Name: SectionAnswer + " " + site, Section: SectionAnswer, Site: site, Subject: site,
			Gather: func(Input) func(*Input) {
				p := reads.GatewayAnswer(site)
				return func(in *Input) { in.Answers = append(in.Answers, p) }
			},
			Judge: func(in Input) []Finding { return GatewayAnswers(cfg, bySite(in.Answers, site, AnswerProbe.site)) },
		})
	}
```

In `internal/doctor/doctor.go`: add `Answers []AnswerProbe` to `Input` after `Settings`, with the comment `// Answers is each gateway's own answer, asked from outside (answer.go).`; insert `SectionAnswer` in `sections` after `SectionLeftovers`; in `Diagnose`, after the `Leftovers` line, `findings = append(findings, GatewayAnswers(cfg, in.Answers)...)`. In `internal/doctor/graph.go`, `clone`, add `out.Answers = slices.Clone(in.Answers)`.

Run: `go test ./internal/doctor/`
Expected: PASS.

- [ ] **Step 4: The read, through phase 6's check**

In `cmd/paisans/doctor.go`, add:

```go
// doctorGatewayAnswer fetches the gateway's /.well-known/paisans/<token>
// from this workstation with the check apply runs at the end of the
// gateway's step, so the two cannot disagree about what answers as this
// deployment. Tests replace it, since no test may reach a real address.
var doctorGatewayAnswer = func(ctx context.Context, cfg *config.Config, site string) error {
	return apply.CheckGatewayAnswer(ctx, apply.HTTPFetcher{}, cfg, site)
}

// GatewayAnswer asks the gateway from outside, with a minute for both
// fetches.
func (d doctorReads) GatewayAnswer(site string) doctor.AnswerProbe {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	p := doctor.AnswerProbe{Site: site}
	if err := doctorGatewayAnswer(ctx, d.cfg, site); err != nil {
		lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
		p.Err, p.More = lines[0], lines[1:]
	}
	return p
}
```

In `withDoctorHosts`, make the gateway answer as itself unless a test says otherwise, before its own `t.Cleanup`:

```go
	savedAnswer := doctorGatewayAnswer
	doctorGatewayAnswer = func(context.Context, *config.Config, string) error { return nil }
	t.Cleanup(func() { doctorGatewayAnswer = savedAnswer })
```

Add `"context"` to the test file's imports. Add a command test:

```go
// A gateway that answers as something else fails doctor, with apply's own
// words.
func TestDoctorFailsAGatewayAnsweringAsSomethingElse(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	var sent, writes []string
	withDoctorHosts(t, cfg, stuckHosts(cfg, &sent, &writes))
	doctorGatewayAnswer = func(_ context.Context, _ *config.Config, site string) error {
		return fmt.Errorf("443 on %s answers, but not as this deployment\nit answered with another body", site)
	}
	rec := withRecorder(t, true)
	_ = runDoctor([]string{"--config", fixtureConfig(), "--site", "vm"})
	if out := rec.Lines(); !strings.Contains(out, "fail: check gateway answer vm 443 on vm answers, but not as this deployment") {
		t.Errorf("the gateway's answer is not a failure:\n%s", out)
	}
}
```

Run: `go test ./cmd/paisans/ -run Doctor`
Expected: PASS. A summary count in another doctor test grows by one passed check for the gateway's answer, where the gateway was asked; recount it as Task 4, Step 7 says.

- [ ] **Step 5: The changelog**

In `CHANGELOG.md`, under `[Unreleased]`, `### Added`, add:

```markdown
- `doctor` fetches each gateway's `/.well-known/paisans/<token>` from the workstation, as `apply` does, and fails when 443 or 80 answers as anything but this deployment.
```

- [ ] **Step 6: Run the touched packages**

Run: `gofmt -l internal/doctor internal/apply cmd/paisans && go vet ./internal/doctor/ ./internal/apply/ ./cmd/paisans/ && go test ./internal/doctor/ ./internal/apply/ ./cmd/paisans/`
Expected: nothing listed; PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/doctor/answer.go internal/doctor/answer_test.go internal/doctor/checks.go internal/doctor/doctor.go internal/doctor/graph.go internal/doctor/graph_test.go cmd/paisans/doctor.go cmd/paisans/doctor_test.go internal/apply CHANGELOG.md
git commit -m "feat: doctor asks each gateway from outside whether it answers as itself

The deployment's user cannot read the host's ruleset or another user's
sockets, so a port taken after admission, and above all one a DNAT rule
takes with no listener, is invisible from the host. doctor fetches the
gateway's own well-known path from the workstation with the check apply
runs, so the two cannot disagree, and the check accepts only the token as
the whole body, so a server that echoes the path does not pass.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: A container whose mesh address was missing at boot

**Files:**
- Modify: `internal/doctor/containers.go` (the `cannot assign requested address` case)
- Modify: `internal/doctor/doctor_test.go` (`TestContainerThatStartedBeforeWg0`)
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: `deployment.Deployment.Interface()`, `.WireGuardUnit()`; `PSEntry.Stack`.
- Produces: the advice text phase 9 documents.

- [ ] **Step 1: Change the test to the advice that holds**

In `TestContainerThatStartedBeforeWg0`, replace the `want` list:

```go
	for _, want := range []string{"Its Docker daemon started it before psns-f2a9 was up", "`docker start paisans-f2a9-talk-app-1` on home-b, as the deployment's user", "`paisans apply --site home-b --recreate talk --execute`"} {
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
deployment's containers run in its own user's daemon, and host prepare
installs nothing, so the advice is what that user can do: start the
container again, or recreate its stack.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Final checks

- [ ] **Step 1: The sudo finding stays gone**

Run: `grep -rn "sudo" internal/doctor/*.go cmd/paisans/doctor.go | grep -v _test.go`
Expected: `SudoCommand` and its comments in `internal/doctor/sudo.go`, `SudoRights`' advice, the user check's advice (`sudo gpasswd`), the gateway answer's advice (`sudo ss`, `sudo docker ps`, `sudo nft`), and `journalAdvice`'s helper command in `mesh.go`. Nothing that runs a command through sudo, and no finding about a sudo the transport could not use.

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

* **Spec coverage.** The index's phase 7 list: the user is not privileged (Task 1, `User`, reading `hostadmit.PrivilegedGroups`); the rootless socket answers (Task 1, `DockerSocket`); Engine 29.5 or later (Task 1, `DockerEngine`, `hostadmit.MinRootlessEngine`); linger on (Task 1, `Linger`); `sudo -n -l` lists only the helper (Task 2); the admission record matches the declaration (Task 3); each in the check graph (Task 4); the sudo finding removed (ruling 1, *Final checks* Step 1); the gateway's `/.well-known/paisans/<token>` from outside sharing `apply`'s code (Task 6); a `WARN` per pending or down site listing what the deployment is without (Task 5 for a site that does not answer, Task 3 for one that answers without a record or a grant). The spec's *What runs as the deployment's user* list for doctor is the same list. *A deployment with sites missing*: "`doctor` reports each pending or down site as a `WARN` with the same list" (Tasks 3 and 5, `losses`); "a timeout is treated the same way" (Task 5, rows `down, timed out` and `down, refused`). The doctor graph spec: hard needs prune and report once (Task 4, `TestAPendingSiteIsCheckedNoFurther`, `TestDockerDownStopsWhatRunsDocker`, `TestPruningIsReportedOnce`); same findings as `Diagnose` (Task 4, Step 3); bound of one in the table's order (Task 4, `TestBoundOfOneRunsInOrder`).
* **Review Focus.** sudo in another language or an unknown shape: Task 2, `TestSudoRights` rows `translated`, `unrecognised`, and `TestParseSudoList`'s refusals. Extra rules: Task 2 rows `helper and a shell on one line`, `helper and ALL`, `helper as any user`, `another deployment's helper`. A site that answers but is not admitted: Task 4, `TestAPendingSiteIsCheckedNoFurther`. A gateway pending or down: Task 5, `TestAbsentSites` rows `gateway down`, `gateway pending`. An echoing server: Task 6, `TestGatewayAnswerRefusesAnEcho`.
* **Left to other phases, on purpose.** User documentation of every check here is phase 9's last task (ruling 9). The classification itself and the gateway's well-known route and fetch are phase 6's; this phase calls them. Whether `host admit` orders the user's daemon after the mesh at boot is founder question 3. Staging confirmation of `sudo -n -l`'s exact output (R9 recorded that it "listed only the helper", not its text) is phase 10's.
* **Type consistency.** `UserProbe`, `DockerProbe`, `LingerProbe` (Task 1), `SudoProbe`, `SudoEntry` (Task 2), `AdmissionProbe` (Task 3), `AnswerProbe` (Task 6) are each an `Input` field's element (`Users`, `Docker`, `Linger`, `Sudo`, `Admission`, `Answers`) and a `Reads` method's return (`User`, `Docker`, `Linger`, `Sudo`, `Admission`, `GatewayAnswer`). `pending`, `absentLevel` and `requestAdvice` are Task 3's and Task 5's `Reach` calls them. `admittedRecord` is written once per test package (`internal/doctor/admission_test.go`, `cmd/paisans/doctor_test.go`), since test helpers do not cross packages. `rootlessDocker` (Task 1), `sudoList` and `helperEntry` (Task 2) are the defaults `fixtureReads` answers with (Task 4). Node names: `admission <site>`, `user <site>`, `docker <site>`, `docker engine <site>`, `linger <site>`, `sudo rights <site>`, `gateway answer <site>`; the pruned labels in the tests drop the site word from each.
* **Placeholder scan.** Two places name code this plan cannot see, each saying where to read it: Task 6, Step 1's fake, written in the form of phase 6's own "another body" case; and Task 3, Step 8, which runs only when phase 6 compares the record in `host prepare`. Phase 6's two other names are in one table and two functions, reconciled by Task 0.
