# Rootless Phase 1: The Default User and the Allowlist's Removal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `sites.<s>.ssh.user` becomes optional, defaulting to the deployment's own user `paisans-<token>`; `config.SetSSHUser` can record a user in the file without overwriting one; and `host allow`, `host disallow`, the key allowlist, its guard in `host prepare` and doctor's allowlist check are gone.

**Architecture:** `config.Load` records the deployment's default user on every site's `SSH` section, the way it already fills `SSH.Keys`, so `(config.Site).SSHUser()` can answer without the `*Config`; every place that meant "the login user" calls it. `SetSSHUser` is a byte splice built from the helpers `SetArch` and `SetMesh` already use (`topMapping`, `lookup`, `lineStarts`, `replaceScalar`, `splice`, `replaceFile`). The allowlist is deleted outright: its files, its callers, its help, its doctor node and its documentation.

**Tech Stack:** Go, `gopkg.in/yaml.v3` node splicing, the existing `hostprep`, `doctor` and `cmd/paisans` packages.

**Spec:** `docs/specs/2026-10-15-rootless-and-host-admit.md` (sections *Settled*, *The deployment's user*, *Keys*), and the phase index `docs/superpowers/plans/2026-10-15-rootless-and-host-admit.md`, *Phase 1*. Both live on the `host-admit` branch, at `/Users/wash/Developer/paisans.community/src/paisans-stack/.worktrees/host-admit/docs/`.

## Global Constraints

* "The deployment's user is `paisans-<token>` (`Deployment.Prefix()`), unless `sites.<s>.ssh.user` names another."
* "`sites.<s>.ssh.user` becomes optional. Empty means the default."
* `SetSSHUser` "splices the value the way `SetArch` does and never overwrites one that is there."
* "`host allow`, `host disallow`, `/etc/paisans/ssh-allow.<user>` and the doctor allowlist check are removed." Founder decision, 2026-10-15; it is also the approval AGENTS.md requires for deleting documentation.
* `host prepare` keeps managing `authorized_keys` from `ssh.keys` exactly as today, minus the allowlist; the sidecar mechanism stays where it is in this phase.
* A declared `ssh.user` is still held to `unixUser` (`^[a-z_][a-z0-9_-]{0,31}$`) in `internal/config/ssh.go`.
* No migration code: nothing deletes an `/etc/paisans/ssh-allow.<user>` file already on a host.
* `SetSSHUser` is not wired to any command in this phase; phase 2 calls it.
* No em dashes in anything written: code, comments, docs, commit messages.
* Docs describe current state only: no "no longer", "removed", "used to" or history wording outside `CHANGELOG.md`; no rejected alternatives anywhere. `docs/decisions.md` and `docs/specs/` are records and are not edited.
* Commits: `<type>: <lowercase subject>`, a body saying why, `Founder decision.` where it was one, and the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
* `CHANGELOG.md`: one bullet per unrelated change under `[Unreleased]`, in its Keep a Changelog category, one sentence, at most 35 words.
* Never `git stash`; other sessions share the stash.

## Review Focus

* `ssh.user` written empty (`user: ""` or `user: ~`): Load treats it as the default, and `SetSSHUser` fills the name in where the empty value stands instead of refusing or adding a second `user:` key (Task 2 load test; Task 3 splice table).
* A user name YAML would read as another type (`null`, `true`): `SetSSHUser` writes it double quoted so the file loads back as the same string (Task 3).
* A site that leaves `ssh.user` out: `host prepare` reads `paisans-<token>`'s passwd entry and writes its `authorized_keys` and a sidecar named for it, never a path with an empty user in it (Task 2 hostprep test).
* A `config.Site` built by hand rather than loaded has no deployment behind it: `SSHUser()` returns only the declared name, never a bare `paisans-` (Task 2 destination test).
* A host that still carries `/etc/paisans/ssh-allow.<user>`, even an empty one that used to refuse every key: `host prepare` adds the listed keys and its commands never mention the file (Task 1 hostprep test).

---

## File Structure

| File | Change | Responsibility after the change |
|---|---|---|
| `cmd/paisans/hostallow.go`, `cmd/paisans/hostallow_test.go` | delete | |
| `internal/hostprep/allow.go`, `internal/hostprep/allow_test.go` | delete | |
| `internal/doctor/allowlist.go` | delete | |
| `internal/hostprep/authorized_keys.go` | modify | plans authorized keys for a user it is given |
| `internal/hostprep/hostprep.go` | modify | `Section` without `Refused`; `Show` and `Execute` without a refusal |
| `internal/doctor/{doctor,checks,graph}.go` | modify | no allowlist section, probe or node |
| `cmd/paisans/{main,help,doctor,catalog}.go` | modify | no `host allow`/`disallow`; doctor reads no allowlist |
| `internal/config/ssh.go` | modify | `SSH.defaultUser`, `(Site).SSHUser()`, `resolveUsers`, optional `user` |
| `internal/config/config.go` | modify | `parse` calls `resolveUsers` |
| `internal/config/ssh_user.go` | create | `SetSSHUser`, `setSSHUser`, `plainOrQuoted` |
| `internal/config/ssh_user_test.go` | create | splice tests |
| `internal/grant/grant.go`, `internal/siteremove/host.go` | modify | login from `SSHUser()` |
| `README.md`, `docs/commands.md`, `docs/development.md`, `docs/guides/*.md`, `examples/paisans-stack.example.yaml`, `CHANGELOG.md` | modify | current state |

---

### Task 1: Remove `host allow`, `host disallow`, the allowlist and its doctor check

**Files:**
- Delete: `cmd/paisans/hostallow.go`, `cmd/paisans/hostallow_test.go`, `internal/hostprep/allow.go`, `internal/hostprep/allow_test.go`, `internal/doctor/allowlist.go`
- Modify: `internal/hostprep/authorized_keys.go:109-113,151-198,309-312,325-341`, `internal/hostprep/hostprep.go:129-131,148,332,381-384,405-412`
- Modify: `internal/doctor/doctor.go:72,75,228,285`, `internal/doctor/checks.go:46-47,304-313,336`, `internal/doctor/graph.go:473`
- Modify: `cmd/paisans/main.go:76-81,1360-1364`, `cmd/paisans/help.go:44,178-191,423-429`, `cmd/paisans/doctor.go:23,435-452`, `cmd/paisans/catalog.go:457-458`
- Modify: `internal/config/ssh.go:452`, `internal/ui/visible.go:16-17`
- Modify tests: `internal/hostprep/hostprep_test.go` (new test), `internal/doctor/graph_test.go:290-293,392,433`, `internal/doctor/doctor_test.go:634-708`, `cmd/paisans/help_test.go:134` (and new test), `cmd/paisans/converge_test.go:854`, `cmd/paisans/doctor_test.go:18,89,203`, `cmd/paisans/hostprepare_all_test.go:53`, `cmd/paisans/review_test.go:292,330`, `cmd/paisans/operatorregister_test.go`, `internal/siteremove/record_test.go:84-109`, `internal/config/config_test.go:713`
- Modify docs: `README.md`, `docs/commands.md`, `docs/development.md`, `docs/guides/how-a-deployment-protects-itself.md`, `docs/guides/when-an-operator-leaves.md`, `CHANGELOG.md`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `func planAuthorizedKeys(t Transport, d deployment.Deployment, user string, s config.SSH) (out Section, removals []Step, err error)` in `internal/hostprep/authorized_keys.go` (the `site string` parameter becomes `user string`); `func readUserKeys(t Transport, user string) (userKeys, error)`. `hostprep.Build` calls `planAuthorizedKeys(t, d, declared.SSH.User, declared.SSH)`; Task 2 changes that argument to `declared.SSHUser()`. Removed exported names: `hostprep.AllowPath`, `AllowLine`, `ParseAllow`, `AllowFile`, `ParseAllowFile`, `Allows`, `EncodeAllow`, `AllowChangedMarker`, `WriteAllowCommand`, `KeyRefusal`, `Refusal`, `Reader`, `ClaimedKeys`, `AuthorizedFingerprints`, `Section.Refused`; `doctor.AllowlistProbe`, `doctor.Allowlist`, `doctor.SectionAllowlist`, `Input.Allowlists`, `Reads.Allowlist`.

- [ ] **Step 1: Write the failing hostprep test**

Append to `internal/hostprep/hostprep_test.go`, after `TestAUserWithoutAuthorizedKeysGetsThem`:

```go
// host prepare adds every key ssh.keys lists, whatever a file at
// /etc/paisans/ssh-allow.<user> on the host holds: it reads no such file,
// and no step it plans mentions one.
func TestKeysAreAddedWhateverAnSSHAllowFileHolds(t *testing.T) {
	host := preparedHost(false)
	host.files["/etc/paisans/ssh-allow.ubuntu"] = "# allows nothing\n"
	plan, err := hostprep.Build("home-a", withKeys(t, "home-a", keyAlice, keyBob), host)
	if err != nil {
		t.Fatal(err)
	}
	add := stepsLabelled(plan, "add")
	if len(add) != 1 || !strings.Contains(add[0].Describe, fingerprint(keyBob)+" (bob)") {
		t.Fatalf("want bob's key added, got:\n%s", printed(plan))
	}
	for _, s := range plan.Steps {
		if strings.Contains(s.Command, "ssh-allow") {
			t.Errorf("%s consults an allowlist: %s", s.Title, s.Command)
		}
	}
	if out := printed(plan); strings.Contains(out, "allowlist") {
		t.Errorf("the plan mentions an allowlist:\n%s", out)
	}
}
```

- [ ] **Step 2: Write the failing help test**

In `cmd/paisans/help_test.go`, change the hand-listed names in `TestEveryFlagSetHasAPage` (line 134) from

```go
	names := []string{"host allow", "host disallow", "web-server show", "web-server check", "dns init", "dns prune"}
```

to

```go
	names := []string{"web-server show", "web-server check", "dns init", "dns prune"}
```

and append this test to the same file:

```go
// The host group prepares a host and lists what is on it; nothing in the
// help offers a key allowlist.
func TestHostHasNoAllowlistCommands(t *testing.T) {
	for _, name := range []string{"host allow", "host disallow"} {
		if findCommand(name) != nil {
			t.Errorf("%s has a help page", name)
		}
	}
	for what, text := range map[string]string{"the host group's summary": groupSummary["host"], "help execute": executeTopic} {
		if strings.Contains(text, "allowlist") || strings.Contains(text, "host allow") {
			t.Errorf("%s still offers an allowlist:\n%s", what, text)
		}
	}
}
```

- [ ] **Step 3: Make doctor's read order expect no allowlist**

In `internal/doctor/graph_test.go`, `TestBoundOfOneRunsInOrder`, delete the line

```go
		"allowlist home-a", "allowlist home-b", "allowlist vm",
```

and in `TestPruningIsReportedOnce` change

```go
	want := "Not checked because of this: mesh, patroni, containers, settings talk, clocks, leftovers, ssh allowlist on home-b."
```

to

```go
	want := "Not checked because of this: mesh, patroni, containers, settings talk, clocks, leftovers on home-b."
```

In `cmd/paisans/doctor_test.go`, `TestDoctorDiagnosesTheStuckReplicaAndOnlyReads`, make the same change to the string at line 203:

```go
		"Not checked because of this: mesh, patroni, containers, settings talk, clocks, leftovers on home-b.",
```

- [ ] **Step 4: Run the new and changed tests to verify they fail**

Run: `go test ./internal/hostprep/ -run TestKeysAreAddedWhateverAnSSHAllowFileHolds`
Expected: FAIL, `want bob's key added, got:` followed by a plan that refuses bob's key (`a key not on ubuntu's allowlist on home-a`).

Run: `go test ./cmd/paisans/ -run 'TestHostHasNoAllowlistCommands|TestDoctorDiagnosesTheStuckReplicaAndOnlyReads'`
Expected: FAIL, `host allow has a help page`, `host disallow has a help page`, the host group's summary and help execute still offering an allowlist, and `output lacks "Not checked because of this: mesh, patroni, containers, settings talk, clocks, leftovers on home-b."`.

Run: `go test ./internal/doctor/ -run 'TestBoundOfOneRunsInOrder|TestPruningIsReportedOnce'`
Expected: FAIL, `reads in order:` listing `allowlist home-a` etc., and `reach home-b does not say what it kept from running`.

- [ ] **Step 5: Delete the allowlist's files**

```bash
git rm cmd/paisans/hostallow.go cmd/paisans/hostallow_test.go internal/hostprep/allow.go internal/hostprep/allow_test.go internal/doctor/allowlist.go
```

- [ ] **Step 6: Take the allowlist out of `internal/hostprep/authorized_keys.go`**

Replace the `keysHost` interface and `readUserKeys`'s signature (lines 109-117):

```go
// keysHost is what reading a user's authorized_keys needs.
type keysHost interface {
	Reader
	Describe() string
}

// readUserKeys reads user's authorized_keys on the host t reaches, finding
// it from the user's passwd entry.
func readUserKeys(t keysHost, user string) (userKeys, error) {
```

with

```go
// readUserKeys reads user's authorized_keys on the host t reaches, finding
// it from the user's passwd entry.
func readUserKeys(t Transport, user string) (userKeys, error) {
```

Delete `AuthorizedFingerprints` and its comment (lines 151-165, from `// AuthorizedFingerprints is the fingerprint of every key in user's` through its closing `}` and the blank line after it).

Replace the head of `planAuthorizedKeys` (lines 167-195):

```go
// planAuthorizedKeys compares the listed keys with the user's authorized_keys,
// this deployment's sidecar and every other deployment's sidecar for the same
// user. It returns the additions and the changes to the sidecar, which run
// with the rest of the plan, and the removals, which run last: a key is only
// taken away once every listed key is in place.
//
// A key to append that the host's allowlist does not hold is refused rather
// than planned (allow.go). A key already in authorized_keys is never refused:
// the list decides what host prepare may add, not who may log in now.
func planAuthorizedKeys(t Transport, d deployment.Deployment, site string, s config.SSH) (out Section, removals []Step, err error) {
	user := s.User
	listed, problems := s.AuthorizedKeys()
	if len(problems) > 0 {
		// Load refuses these; reaching here is a bug, not an operator error.
		return out, nil, fmt.Errorf("ssh.keys: %s", strings.Join(problems, "; "))
	}

	keys, err := readUserKeys(t, user)
	if err != nil {
		return out, nil, err
	}
	uid, gid, dir, file, found, lines := keys.uid, keys.gid, keys.dir, keys.file, keys.found, keys.lines

	allowContent, hasList, err := t.ReadFile(AllowPath(user))
	if err != nil {
		return out, nil, err
	}
	allow := ParseAllow(allowContent)
	allowed := func(fp string) bool { return !hasList || Allows(allow, fp) }

	own := OwnedKeysPath(d, user)
```

with

```go
// planAuthorizedKeys compares the keys s lists with user's authorized_keys,
// this deployment's sidecar and every other deployment's sidecar for the same
// user. It returns the additions and the changes to the sidecar, which run
// with the rest of the plan, and the removals, which run last: a key is only
// taken away once every listed key is in place.
func planAuthorizedKeys(t Transport, d deployment.Deployment, user string, s config.SSH) (out Section, removals []Step, err error) {
	listed, problems := s.AuthorizedKeys()
	if len(problems) > 0 {
		// Load refuses these; reaching here is a bug, not an operator error.
		return out, nil, fmt.Errorf("ssh.keys: %s", strings.Join(problems, "; "))
	}

	keys, err := readUserKeys(t, user)
	if err != nil {
		return out, nil, err
	}
	uid, gid, dir, file, found, lines := keys.uid, keys.gid, keys.dir, keys.file, keys.found, keys.lines

	own := OwnedKeysPath(d, user)
```

In the `share` step, replace

```go
				// The append, when the line has gone, is an addition like any
				// other, so the allowlist is checked first.
				Command: Locked(fmt.Sprintf("if grep -qxF %s %s; then %s; else %s; %s; %s; fi",
					strings.Join(raws, " "), shellQuote(file), writeOwned(d, user, owned), allowGuard(user, k.Fingerprint), appendKey(file, k.Line), writeOwned(d, user, added))),
```

with

```go
				Command: Locked(fmt.Sprintf("if grep -qxF %s %s; then %s; else %s; %s; fi",
					strings.Join(raws, " "), shellQuote(file), writeOwned(d, user, owned), appendKey(file, k.Line), writeOwned(d, user, added))),
```

Delete the refusal case:

```go
		case !allowed(k.Fingerprint):
			// Counted as remaining: a refusal stops the plan, so no removal
			// runs on the strength of it either way.
			remaining++
			out.Refused = append(out.Refused, KeyRefusal{Site: site, User: user, Admin: k.Name, Fingerprint: k.Fingerprint})
```

In the `add` step, replace

```go
				// The list is checked again under the lock, so one made
				// between this plan and its run still holds.
				Command: Locked(allowGuard(user, k.Fingerprint) + "; " + appendKey(file, k.Line) + "; " + writeOwned(d, user, owned)),
```

with

```go
				Command: Locked(appendKey(file, k.Line) + "; " + writeOwned(d, user, owned)),
```

- [ ] **Step 7: Take the refusal out of `internal/hostprep/hostprep.go`**

In `Section`, delete

```go
	// Refused are keys to add that the host's allowlist does not hold. Any
	// one refuses the whole plan, before a step runs (see Refusal).
	Refused []KeyRefusal
```

In `(*Section).add`, delete `	s.Refused = append(s.Refused, other.Refused...)`.

In `Build`, change

```go
	keys, keyRemovals, err := planAuthorizedKeys(t, d, site, declared.SSH)
```

to

```go
	keys, keyRemovals, err := planAuthorizedKeys(t, d, declared.SSH.User, declared.SSH)
```

In `(*Plan).Show`, delete

```go
	if err := Refusal(p); err != nil {
		hint, explain := ui.Describe(err)
		r.Refuse(hint, explain)
	}
```

Replace the head of `Execute`:

```go
// Execute refuses a plan Refusal refuses, before any step, and otherwise
// runs each step in order and stops at the first failure, naming the step, so an operator knows where a half finished run left the host. Every
// step is planned from a probe, so re-running Build after fixing the cause
// plans only what is still missing.
func Execute(plan *Plan, t Transport, r ui.Reporter) error {
	if err := Refusal(plan); err != nil {
		return err
	}
	for _, step := range plan.Steps {
```

with

```go
// Execute runs each step in order and stops at the first failure, naming the
// step, so an operator knows where a half finished run left the host. Every
// step is planned from a probe, so re-running Build after fixing the cause
// plans only what is still missing.
func Execute(plan *Plan, t Transport, r ui.Reporter) error {
	for _, step := range plan.Steps {
```

- [ ] **Step 8: Take the refusal out of host prepare's dry run in `cmd/paisans/main.go`**

Replace

```go
		refusal := hostprep.Refusal(plan)
		env.found(strings.Join(plan.Kinds(), ", "), refusal)
		switch {
		case refusal != nil:
			r.Result("Nothing changed. --execute would stop at the refusal above.")
		case len(plan.Steps) == 0 && pending == 0:
```

with

```go
		env.found(strings.Join(plan.Kinds(), ", "), nil)
		switch {
		case len(plan.Steps) == 0 && pending == 0:
```

Replace the end of the `host` dispatch (lines 76-80):

```go
		case len(os.Args) >= 3 && os.Args[2] == "allow":
			err = runHostAllow(os.Args[3:], os.Stdin, os.Stdout)
		case len(os.Args) >= 3 && os.Args[2] == "disallow":
			err = runHostDisallow(os.Args[3:], os.Stdin, os.Stdout)
		default:
			os.Exit(groupError(os.Stderr, "host", "host takes one subcommand, prepare, deployments, allow or disallow"))
```

with

```go
		default:
			os.Exit(groupError(os.Stderr, "host", "host takes one subcommand, prepare or deployments"))
```

- [ ] **Step 9: Take the commands out of `cmd/paisans/help.go`**

Change the `host` group summary:

```go
	"host":        "Prepare a host, list what is on it, manage its key allowlist.",
```

to

```go
	"host":        "Prepare a host, or list what is on it.",
```

Delete the two `helpCommand` entries whose `name` is `"host allow"` and `"host disallow"` (lines 178-191, from the `{` before `name:    "host allow",` through the `},` after the `host disallow` entry's `detail`).

Replace the first eight lines of `executeTopic`:

```go
const executeTopic = `host prepare, host deployments, host allow, host disallow, apply, prune, site
add, site remove, mesh rekey, storage init, storage add, storage rotate-key, app
admin create, app remove, oidc client create, secrets prune, secrets rotate,
preflight, failover test, doctor, init, config and operator register are the only
commands that reach a host. Each reads it to plan, and changes it only with
--execute; host allow and host disallow change only a host's key allowlist,
which is the host owner's, and claim nothing; config and operator register claim
nothing, and their --execute only starts each site's configuration sync and, for
```

with

```go
const executeTopic = `host prepare, host deployments, apply, prune, site add, site remove,
mesh rekey, storage init, storage add, storage rotate-key, app admin create, app
remove, oidc client create, secrets prune, secrets rotate, preflight, failover
test, doctor, init, config and operator register are the only commands that
reach a host. Each reads it to plan, and changes it only with --execute; config
and operator register claim nothing, and their --execute only starts each site's
configuration sync and, for
```

In `cmd/paisans/converge_test.go`, `TestUsageHostListNamesSecretsPrune`, change

```go
	start := strings.Index(executeTopic, "host prepare, host deployments, host allow, host disallow, apply")
```

to

```go
	start := strings.Index(executeTopic, "host prepare, host deployments, apply")
```

In `cmd/paisans/catalog.go`, change the `parseAll` comment

```go
// parseAll parses fs over args with positional arguments anywhere among the
// flags, as host allow does, and returns the positional ones in order.
```

to

```go
// parseAll parses fs over args with positional arguments anywhere among the
// flags, and returns the positional ones in order.
```

- [ ] **Step 10: Take the allowlist out of doctor**

`internal/doctor/doctor.go`: delete `	SectionAllowlist  = "ssh allowlist"`; remove `SectionAllowlist, ` from the `sections` slice so it reads

```go
var sections = []string{SectionReach, SectionMesh, SectionEtcd, SectionPatroni, SectionContainers, SectionSettings, SectionPocketID, SectionClocks, SectionLeftovers, SectionCaddy, SectionSecrets, SectionToken, SectionRecords}
```

delete `	Allowlists []AllowlistProbe` from `Input`, and delete `	findings = append(findings, Allowlist(cfg, in.Allowlists)...)` from `Diagnose`.

`internal/doctor/checks.go`: delete from `Reads`

```go
	// Allowlist reads the login user's key allowlist and authorized_keys.
	Allowlist(site string) AllowlistProbe
```

delete the node loop

```go
	for _, site := range sites {
		g.Add(Node{Name: SectionAllowlist + " " + site, Section: SectionAllowlist, Site: site, Subject: site,
			Hard: []Need{Hard(reach(site))},
			Gather: func(Input) func(*Input) {
				p := reads.Allowlist(site)
				return func(in *Input) { in.Allowlists = append(in.Allowlists, p) }
			},
			Judge: func(in Input) []Finding { return Allowlist(cfg, bySite(in.Allowlists, site, AllowlistProbe.site)) },
		})
	}
```

and delete `func (p AllowlistProbe) site() string { return p.Site }`.

`internal/doctor/graph.go`: delete `	out.Allowlists = slices.Clone(in.Allowlists)`.

`internal/doctor/graph_test.go`: delete the fixture method

```go
func (f *fixtureReads) Allowlist(site string) AllowlistProbe {
	f.asked("allowlist " + site)
	return AllowlistProbe{Site: site}
}
```

`internal/doctor/doctor_test.go`: delete `TestAllowlist`, `TestAllowlistShowsControlCharactersEscaped` and `TestAllowlistWarnsOnlyForKeysHostPrepareWouldAdd` with their comments, from the line `// The allowlist: no list is information, a list says how many keys it holds,` through the closing `}` of `TestAllowlistWarnsOnlyForKeysHostPrepareWouldAdd` and the blank line after it.

`cmd/paisans/doctor.go`: delete the method

```go
func (d doctorReads) Allowlist(site string) doctor.AllowlistProbe {
	t := d.transports[site]
	user := d.cfg.Sites[site].SSH.User
	p := doctor.AllowlistProbe{Site: site}
	content, found, err := t.ReadFile(hostprep.AllowPath(user))
	if err != nil {
		p.Err = err.Error()
	}
	p.Found, p.Content = found, content
	// Only a key host prepare would append is refused, so the warning
	// needs to know which keys authorized_keys holds already.
	if found {
		if fps, err := hostprep.AuthorizedFingerprints(t, user); err == nil {
			p.KeysRead, p.Authorized = true, fps
		}
	}
	return p
}
```

and the now unused import `"github.com/paisans-software/paisans-cli/internal/hostprep"`.

`cmd/paisans/doctor_test.go`: in `readOnly`, change

```go
	exact = append(exact, "read "+cfg.Deployment().Manifest(), "read "+registry.Path, "read "+hostprep.AllowPath("ubuntu"))
```

to

```go
	exact = append(exact, "read "+cfg.Deployment().Manifest(), "read "+registry.Path)
```

and delete the now unused `hostprep` import.

- [ ] **Step 11: Repoint the tests that leaned on the allowlist**

`cmd/paisans/operatorregister_test.go` uses `bobKey`, which `hostallow_test.go` declared. Add, above `withOperatorBob`:

```go
// bobKey is the key the fixture's second operator, bob, logs in with.
const bobKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB bob@example.org"
```

`cmd/paisans/hostprepare_all_test.go:53` and `cmd/paisans/review_test.go:292,330` fail the key step by matching its command on the guard's `awk -v fp=`, which no command sends now. Match the append instead, which only the key steps send (`appendKey` in `internal/hostprep/authorized_keys.go`). Change each `"awk -v fp="` to `"tail -c1"`:

```go
	loggingSSH(t, "tail -c1")
```

```go
	for name, fail := range map[string]string{"succeeds": "", "a step fails": "tail -c1"} {
```

`internal/siteremove/record_test.go`: replace the test from its comment through its closing brace:

```go
// Cleaning a host deletes the record of what this deployment granted there,
// with its registry entry, and leaves the host's key allowlist, which is the
// host owner's and shared by every deployment on it.
func TestCleaningAHostDeletesItsGrantRecordAndKeepsTheAllowlist(t *testing.T) {
	w := setup(t)
	vm := w.hosts["vm"]
	vm.files[grant.Path(dep)] = grant.Encode(grant.Record{})
	vm.files[hostprep.AllowPath("ubuntu")] = "# the owner's\n"
```

with

```go
// Cleaning a host deletes the record of what this deployment granted there,
// with its registry entry.
func TestCleaningAHostDeletesItsGrantRecord(t *testing.T) {
	w := setup(t)
	vm := w.hosts["vm"]
	vm.files[grant.Path(dep)] = grant.Encode(grant.Record{})
```

delete its last check

```go
	if _, ok := vm.files[hostprep.AllowPath("ubuntu")]; !ok {
		t.Error("the host's allowlist was deleted")
	}
```

and delete the file's now unused `hostprep` import.

- [ ] **Step 12: Fix the comments that name the commands**

`internal/config/ssh.go`, `controlProblem`: change `could redraw what the review of this file, host allow or doctor prints; remove it.` to `could redraw what the review of this file or doctor prints; remove it.`

`internal/ui/visible.go`: change

```go
// to read as something it is not. Everything the review, host allow, host
// disallow and doctor print from the configuration or from a host passes
// through it.
```

to

```go
// to read as something it is not. Everything the review and doctor print
// from the configuration or from a host passes through it.
```

(keep the sentence after it, `Validation refuses these characters...`, as it is; rewrap the comment so no line passes 80 columns).

`internal/config/config_test.go:713`: change `// A control character in what the review and host allow print is refused` to `// A control character in what the review prints is refused`.

- [ ] **Step 13: Build, format and run the affected packages**

Run: `gofmt -l cmd internal`
Expected: no output. If a file is listed, run `gofmt -w` on it and look at the diff.

Run: `go vet ./cmd/... ./internal/...`
Expected: no output.

Run: `go test ./internal/hostprep/ ./internal/doctor/ ./internal/siteremove/ ./internal/config/ ./cmd/paisans/`
Expected: `ok` for each package, including `TestKeysAreAddedWhateverAnSSHAllowFileHolds`, `TestHostHasNoAllowlistCommands`, `TestBoundOfOneRunsInOrder`, `TestPruningIsReportedOnce` and `TestHostPrepareRecordsTheKeysOnlyAfterSuccess`.

Run: `git grep -n 'AllowPath\|ParseAllow\|allowGuard\|KeyRefusal\|ClaimedKeys\|AuthorizedFingerprints\|runHostAllow\|runHostDisallow\|AllowlistProbe\|SectionAllowlist\|ssh-allow' -- '*.go'`
Expected: only the new test's `/etc/paisans/ssh-allow.ubuntu` in `internal/hostprep/hostprep_test.go`.

- [ ] **Step 14: Rewrite `README.md` to the current state**

In *Gathering, and why every site is asked*, change

```
   (Eg: a host whose allowlist leaves them out) arrives through one they can. A
```

to

```
   (Eg: a site whose `ssh.keys` does not name them) arrives through one they can. A
```

In *host prepare manages the login user's authorized keys*, delete from the line beginning `**The host's owner may bound which keys it adds.**` through the line `reports each host's list.` (inclusive; about 68 lines, ending just before `#### The watchdog`), and put in their place:

```
**The review is the check on whose keys it adds.** `paisans-stack.yaml` is one
file every operator of a deployment edits, and whoever runs `host prepare` runs
it with their own access, so any operator can get a key of theirs made an
authorized key of the login user through someone else's run. That user has
`sudo` and is in the `docker` group, so the key is root on the host. The review
(see *`apply` and `host prepare` review what `paisans-stack.yaml` grants*) shows
every key a run would add, with its operator and fingerprint, and an
`--execute` run asks for `approve` at a terminal before anything changes.
```

In *`apply` and `host prepare` review what `paisans-stack.yaml` grants*, change

```
line, everything the review, `host allow`, `host disallow` and `doctor` print
```

to

```
line, everything the review and `doctor` print
```

and replace

```
The review is the operator's check. The host's owner, who may not be the one
running the command, bounds which keys `host prepare` may add at all with an
allowlist on the host (see *host prepare manages the login user's authorized keys*).
```

with

```
The review is the operator's check, made at the terminal of whoever runs the
command.
```

Delete the table row beginning `| the host's key allowlist |` (in the table of what `site remove` cleans from a host) and the table row beginning `| SSH allowlist |` (in doctor's table of checks).

- [ ] **Step 15: Rewrite `docs/commands.md`**

In *`paisans host prepare`*, delete the bullet

```
* **Allowlist:** with `/etc/paisans/ssh-allow.<user>` on the host, a key to
  add that it does not list is a refusal in the plan, naming the key, its
  operator and the `host allow` that allows it; sharing, renaming and removing
  keys are never refused. Without the file every key is allowed.
```

Delete from the line `### \`paisans host allow\`` through the line `See *host prepare manages the login user's authorized keys* in \`README.md\`.` and the blank line after it, so `### \`paisans prune\`` follows the end of *`paisans host deployments`*.

In *`paisans site remove`*, change

```
  `/var/lib/paisans/granted.<token>.json`, with its registry entry, and never
  touches the host's key allowlist. It deletes the host's configuration store
```

to

```
  `/var/lib/paisans/granted.<token>.json`, with its registry entry. It deletes
  the host's configuration store
```

In *`paisans doctor`*, change

```
instance, clocks, what is left over, and each host's key allowlist, with each
key `ssh.keys` names that it does not hold and that the next `host prepare`
would add, since one already in `authorized_keys` is never added again.
```

to

```
instance, clocks, and what is left over.
```

- [ ] **Step 16: Rewrite `docs/development.md` and the guides**

`docs/development.md`, *Run*: change

```
host `--ssh` names, with no `paisans-stack.yaml`, and changes nothing. `host allow`
and `host disallow` change only a site's host's key allowlist,
`/etc/paisans/ssh-allow.<user>`, which is the host owner's, so they claim
nothing; with `--execute` they ask for the site's name at a terminal.
`internal/hostprep/allow.go` holds the list's format, the refusal `host
prepare` gives a key it lacks, and the locked write; `cmd/paisans/hostallow.go`
the commands. `secrets
```

to

```
host `--ssh` names, with no `paisans-stack.yaml`, and changes nothing. `secrets
```

and delete the two example lines

```
paisans host allow home-a bob             # bob's fingerprints, and home-a's allowlist as it would be
paisans host disallow home-a bob          # what it would take off home-a's allowlist
```

In the list of properties the tests hold, delete

```
* **host prepare never appends a key the host's allowlist lacks**, checked in
  the plan and again under the lock as the append runs. Tests in
  `internal/hostprep/allow_test.go` run the append against real files.
```

In *What is not here yet*, change

```
Nor have the review of what `paisans-stack.yaml` grants, its record on each host,
the allowlist's refusal in `host prepare`, and `host allow` and `host
disallow`: the shell they send is run against local files in the tests, not
observed on a host.
```

to

```
Nor have the review of what `paisans-stack.yaml` grants and its record on each
host: the shell they send is run against local files in the tests, not
observed on a host.
```

`docs/guides/how-a-deployment-protects-itself.md`, *Someone adds their key to a host they were not given*: delete the bullet beginning `* **The host's allowlist.**` (five lines, through `so a key swapped in under Alice's name is refused too.`), and change

```
**Does not protect.** A host with no allowlist file allows every key, by
founder decision, so until its owner runs `host allow` the review is the only
check. Neither control removes a key already in `authorized_keys`.

**You do.** Read the review before typing `approve`, as you would read a diff
before merging. If you own a host, run `host allow` on it.
```

to

```
**Does not protect.** The review is the only check, and it is made by whoever
runs the command. It removes no key already in `authorized_keys`.

**You do.** Read the review before typing `approve`, as you would read a diff
before merging.
```

`docs/guides/when-an-operator-leaves.md`, step 7: change

````
7. **Bring up the replacement.** Before it, the new host's owner can bound
   which keys `apply` may ever grant there:

   ```sh
   paisans host allow home-d carol dave           # on the new site, before its first apply
   paisans apply --execute
   ```
````

to

````
7. **Bring up the replacement.**

   ```sh
   paisans apply --execute
   ```
````

and change

```
   the terminal (`docs/specs/2026-10-11-config-review.md`; `host allow`
   writes `/etc/paisans/ssh-allow.<user>` there). A replacement data site is
```

to

```
   the terminal (`docs/specs/2026-10-11-config-review.md`). A replacement data site is
```

Run: `git grep -n 'host allow\|host disallow\|ssh-allow\|allowlist' -- README.md docs/commands.md docs/development.md docs/guides`
Expected: only lines about the blog's or Matrix's federation allowlist (Eg: `README.md` near *private mode* and `app.federation_allowlist`), none about SSH keys.

- [ ] **Step 17: Record the change in `CHANGELOG.md`**

Under `## [Unreleased]`, between `### Changed` and `### Fixed`, add:

```markdown
### Removed

- `host allow`, `host disallow` and the key allowlist in `/etc/paisans/ssh-allow.<user>` are removed, with `host prepare`'s refusal of keys it lacked and `doctor`'s allowlist check.
```

(28 words.)

- [ ] **Step 18: Commit**

```bash
git add -A cmd internal README.md docs/commands.md docs/development.md docs/guides CHANGELOG.md
git commit -F - <<'EOF'
feat: remove host allow, host disallow and the key allowlist

The allowlist bounded which keys host prepare could add, as root, to a
login user several deployments may share. Every deployment is moving to
its own unprivileged user, admitted once by the host's owner, and after
admission no command writes keys as root, so the list protects nothing.
host prepare keeps managing authorized_keys from ssh.keys, with the
sidecars, exactly as before; the review stays the check on whose keys a
run adds. doctor's allowlist section goes with it. A list already on a
host is left there; nothing reads it.

Founder decision.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

---

### Task 2: `ssh.user` is optional and defaults to the deployment's own user

**Files:**
- Modify: `internal/config/ssh.go:23-50` (`SSH`), `:226-244` (`sshProblems`), `:294-307` (after `resolveKeys`), `:425-428` (`Destination`)
- Modify: `internal/config/config.go:891` (`parse`)
- Modify: `cmd/paisans/main.go:1650` (`siteTransport`), `internal/grant/grant.go:94`, `internal/siteremove/host.go:933`, `internal/hostprep/hostprep.go:332`
- Test: `internal/config/config_test.go`, `internal/config/destination_test.go`, `cmd/paisans/transport_test.go`, `internal/grant/grant_test.go`, `internal/hostprep/hostprep_test.go`
- Modify docs: `README.md`, `examples/paisans-stack.example.yaml`, `CHANGELOG.md`

**Interfaces:**
- Consumes: `planAuthorizedKeys(t Transport, d deployment.Deployment, user string, s config.SSH)` from Task 1.
- Produces: `func (s Site) SSHUser() string` in `internal/config/ssh.go`: `s.SSH.User` when set, else the deployment's `Prefix()` recorded by `Load`, else `""` for a site not built by `Load`. `func (c *Config) resolveUsers()` (unexported), called by `parse` after `resolveKeys`. `(Site).Destination()` now carries `SSHUser()`. The unexported field `SSH.defaultUser string`.

- [ ] **Step 1: Write the failing config tests**

In `internal/config/config_test.go`, `TestSSHSectionRefusals`, delete the case

```go
		{"no user", "    ssh:\n      host: home-a.local\n" + key, "ssh.user: required"},
```

and add, after `TestSSHSectionLoads`:

```go
// ssh.user may be left out, or written empty: the site is then reached as
// the deployment's own user, paisans-<token>. The default is not written
// into ssh.user, so the file's own value stays apart from it.
func TestSSHUserDefaultsToTheDeploymentsOwnUser(t *testing.T) {
	for name, section := range map[string]string{
		"left out":      "    ssh:\n      host: home-a.local\n      keys: [alice]\n",
		"written empty": "    ssh:\n      host: home-a.local\n      user: \"\"\n      keys: [alice]\n",
		"written as ~":  "    ssh:\n      host: home-a.local\n      user: ~\n      keys: [alice]\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := config.Load(write(t, withSSH(section)))
			if err != nil {
				t.Fatal(err)
			}
			site := cfg.Sites["home-a"]
			if site.SSH.User != "" {
				t.Errorf("ssh.user read as %q, want it empty", site.SSH.User)
			}
			if got := site.SSHUser(); got != "paisans-f2a9" {
				t.Errorf("user is %q, want paisans-f2a9", got)
			}
			if got := site.Destination().String(); got != "paisans-f2a9@home-a.local:22" {
				t.Errorf("destination is %q, want paisans-f2a9@home-a.local:22", got)
			}
		})
	}
}

// A declared ssh.user is the user the site is reached as.
func TestSSHUserIsTheDeclaredName(t *testing.T) {
	cfg, err := config.Load(write(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Sites["home-a"].SSHUser(); got != "ubuntu" {
		t.Errorf("user is %q, want ubuntu", got)
	}
}
```

In `internal/config/destination_test.go`, append:

```go
// A site built by hand rather than loaded has no deployment to take a
// default from, so its user is the declared one or none, never a bare
// "paisans-".
func TestSSHUserOfASiteNotLoadedIsOnlyTheDeclaredOne(t *testing.T) {
	if got := (config.Site{}).SSHUser(); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := (config.Site{SSH: config.SSH{User: "admin"}}).SSHUser(); got != "admin" {
		t.Errorf("got %q, want admin", got)
	}
}
```

- [ ] **Step 2: Write the failing caller tests**

Append to `cmd/paisans/transport_test.go`:

```go
// A site that leaves ssh.user out is reached as the deployment's own user.
func TestSiteTransportDefaultsToTheDeploymentsUser(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	site := cfg.Sites["home-a"]
	site.SSH.User = ""
	if got := siteTransport("home-a", site, "", false); got.User != "paisans-f2a9" {
		t.Errorf("user is %q, want paisans-f2a9", got.User)
	}
}
```

Append to `internal/grant/grant_test.go`:

```go
// A site that leaves ssh.user out grants its keys to the user it is reached
// as, the deployment's own.
func TestTheLoginIsTheDefaultUserWhenSSHUserIsLeftOut(t *testing.T) {
	cfg := fixture(t)
	s := cfg.Sites["home-a"]
	s.SSH.User = ""
	cfg.Sites["home-a"] = s
	g, err := grant.For(cfg, nil, "home-a")
	if err != nil {
		t.Fatal(err)
	}
	if g.Login != "paisans-f2a9" {
		t.Errorf("login is %q, want paisans-f2a9", g.Login)
	}
}
```

Append to `internal/hostprep/hostprep_test.go`:

```go
// A site that leaves ssh.user out has its keys managed for the deployment's
// own user: its passwd entry, its authorized_keys and a sidecar named for it.
func TestKeysGoToTheDefaultUserWhenSSHUserIsLeftOut(t *testing.T) {
	cfg := withKeys(t, "home-a", keyAlice)
	s := cfg.Sites["home-a"]
	s.SSH.User = ""
	cfg.Sites["home-a"] = s
	host := preparedHost(false)
	host.responses[probePasswd] = "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/sh\n"
	delete(host.files, authorizedKeys)
	delete(host.files, ownedKeys)
	plan, err := hostprep.Build("home-a", cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	out := printed(plan)
	if !strings.Contains(out, "ssh: authorize key "+fingerprint(keyAlice)+" (alice) for paisans-f2a9") {
		t.Fatalf("alice's key is not authorized for paisans-f2a9:\n%s", out)
	}
	add := stepsLabelled(plan, "add")
	if len(add) != 1 {
		t.Fatalf("want one add, got:\n%s", out)
	}
	cmd := unlocked(t, add[0].Command)
	for _, want := range []string{"'/home/paisans-f2a9/.ssh/authorized_keys'", "/etc/paisans/authorized_keys.paisans-f2a9.paisans-f2a9.owned"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("the add lacks %s: %s", want, cmd)
		}
	}
	asked := false
	for _, c := range host.ran {
		asked = asked || strings.Contains(c, "getent passwd 'paisans-f2a9'")
	}
	if !asked {
		t.Errorf("paisans-f2a9's passwd entry was not read:\n%s", strings.Join(host.ran, "\n"))
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/config/ -run 'TestSSHUser|TestSSHSectionRefusals'`
Expected: FAIL to build, `site.SSHUser undefined (type config.Site has no field or method SSHUser)`.

Run: `go test ./cmd/paisans/ -run TestSiteTransportDefaultsToTheDeploymentsUser`
Expected: FAIL, `user is "", want paisans-f2a9`.

Run: `go test ./internal/grant/ -run TestTheLoginIsTheDefaultUserWhenSSHUserIsLeftOut`
Expected: FAIL, `login is "", want paisans-f2a9`.

Run: `go test ./internal/hostprep/ -run TestKeysGoToTheDefaultUserWhenSSHUserIsLeftOut`
Expected: FAIL, `alice's key is not authorized for paisans-f2a9:` (the plan says `for ` with an empty user).

- [ ] **Step 4: Record the default user and answer it in `internal/config/ssh.go`**

No new import is needed: `c.Deployment()` already returns a `deployment.Deployment`.

Change the `User` field's comment and add `defaultUser` after `declared`:

```go
	// User is the login user. Empty means the deployment's own user,
	// paisans-<token>, which SSHUser returns. Either way it must already
	// exist on the host: creating users is not something host prepare does.
	User string `yaml:"user"`
```

```go
	// declared records that the key was present at all, so a missing
	// section and an empty one get different messages.
	declared bool
	// defaultUser is the deployment's own user, paisans-<token>, which Load
	// records on every site so that a site can name its login without the
	// configuration it came from.
	defaultUser string
}
```

Add after `SSHHost`:

```go
// SSHUser is the user the toolkit logs in as: ssh.user, or failing that the
// deployment's own user, paisans-<token>. The default is the prefix every
// name the deployment owns on a host already carries, so a deployment that
// names no user logs in as one of its own. A site Load did not build has no
// deployment behind it, and so no default.
func (s Site) SSHUser() string {
	if s.SSH.User != "" {
		return s.SSH.User
	}
	return s.SSH.defaultUser
}
```

Add after `resolveKeys`:

```go
// resolveUsers records the deployment's own user on every site's ssh section,
// for SSHUser.
func (c *Config) resolveUsers() {
	user := c.Deployment().Prefix()
	for name, site := range c.Sites {
		site.SSH.defaultUser = user
		c.Sites[name] = site
	}
}
```

Change `Destination`:

```go
// Destination is where the site's ssh section reaches it.
func (s Site) Destination() Destination {
	return Destination{User: s.SSHUser(), Host: s.SSHHost(), Port: s.SSH.PortOrDefault()}
}
```

In `sshProblems`, replace

```go
	if !s.declared {
		add("sites.%s.ssh: required. Give the section with at least user and keys: it is how the toolkit reaches the site and who may log in to it.", name)
		return problems
	}
	switch {
	case s.User == "":
		add("sites.%s.ssh.user: required. Name the login user, which must already exist on the host.", name)
	case !unixUser.MatchString(s.User):
		add("sites.%s.ssh.user: %q is not a user name this toolkit accepts. Use a lowercase letter or underscore, then lowercase letters, digits, underscores or hyphens, at most 32 characters.", name, s.User)
	}
```

with

```go
	if !s.declared {
		add("sites.%s.ssh: required. Give the section with at least keys: it is how the toolkit reaches the site and who may log in to it.", name)
		return problems
	}
	if s.User != "" && !unixUser.MatchString(s.User) {
		add("sites.%s.ssh.user: %q is not a user name this toolkit accepts. Use a lowercase letter or underscore, then lowercase letters, digits, underscores or hyphens, at most 32 characters, or leave it out for the deployment's own user.", name, s.User)
	}
```

In `internal/config/config.go`, `parse`, change

```go
	cfg.resolveKeys()
```

to

```go
	cfg.resolveKeys()
	cfg.resolveUsers()
```

- [ ] **Step 5: Use `SSHUser()` wherever the login user is meant**

`cmd/paisans/main.go`, `siteTransport`:

```go
		t = apply.SSHTransport{Site: name, User: site.SSHUser(), Host: site.SSHHost(), Port: site.SSH.PortOrDefault(), PublicKeys: lines, Sudo: sudo}
```

`internal/grant/grant.go`, `For`:

```go
	g := Grant{Login: declared.SSHUser(), Keys: KeysOf(declared.SSH), WebServer: WebServerOf(declared)}
```

`internal/siteremove/host.go`, `hostGoneLeft`: change `hostprep.OwnedKeysPath(d, p.cfg.Sites[p.Site].SSH.User)` to `hostprep.OwnedKeysPath(d, p.cfg.Sites[p.Site].SSHUser())`.

`internal/hostprep/hostprep.go`, `Build`:

```go
	keys, keyRemovals, err := planAuthorizedKeys(t, d, declared.SSHUser(), declared.SSH)
```

Run: `git grep -n 'SSH\.User' -- '*.go' ':!*_test.go'`
Expected: only `internal/config/ssh.go` (`SSHUser` itself) and the writes in `internal/siteremove/force.go:43,165` and `cmd/paisans/siteremove.go:305`, which set a destination's user and stay as they are.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/config/ ./internal/grant/ ./internal/hostprep/ ./internal/siteremove/ ./cmd/paisans/`
Expected: `ok` for each, including `TestSSHUserDefaultsToTheDeploymentsOwnUser`, `TestSSHUserIsTheDeclaredName`, `TestSSHUserOfASiteNotLoadedIsOnlyTheDeclaredOne`, `TestSiteTransportDefaultsToTheDeploymentsUser`, `TestTheLoginIsTheDefaultUserWhenSSHUserIsLeftOut` and `TestKeysGoToTheDefaultUserWhenSSHUserIsLeftOut`.

- [ ] **Step 7: Document the default**

`README.md`, the `ssh` example under the operators and sites schema: change

```
      user: ubuntu         # required, and must already exist on the host
```

to

```
      user: ubuntu         # optional: defaults to paisans-<token>; must already exist on the host
```

the table row

```
| `user` | yes | not a lowercase letter or underscore followed by lowercase letters, digits, underscores or hyphens, 32 characters at most |
```

to

```
| `user` | no: empty means the deployment's own user, `paisans-<token>` | not a lowercase letter or underscore followed by lowercase letters, digits, underscores or hyphens, 32 characters at most |
```

and insert this paragraph before the one beginning `A single string in place of the section`:

```
`user` defaults to `paisans-<token>`, the prefix every name the deployment owns
on a host already carries, so a deployment that names no user logs in as one of
its own rather than as a user another deployment shares. The user must exist on
the host either way.
```

In *host prepare manages the login user's authorized keys*, change

```
`host prepare` makes every key of every operator in `ssh.keys` an authorized key of
`ssh.user`, in `~<user>/.ssh/authorized_keys`. When the directory or the file
```

to

```
`host prepare` makes every key of every operator in `ssh.keys` an authorized key of
the login user (`ssh.user`, or `paisans-<token>` when it is left out), in
`~<user>/.ssh/authorized_keys`. When the directory or the file
```

In the grant table, change `| login | \`ssh.user\` | the site's \`ssh\` section |` to `| login | \`ssh.user\`, or \`paisans-<token>\` when it is left out | the site's \`ssh\` section |`.

`examples/paisans-stack.example.yaml`: change

```yaml
      user: ubuntu              # must already exist on the host
```

to

```yaml
      user: ubuntu              # optional: defaults to paisans-<token>; must already exist on the host
```

Run: `go run ./cmd/paisans validate --offline --config examples/paisans-stack.example.yaml`
Expected: the same result as before the edit (the comment changes nothing the parser reads).

- [ ] **Step 8: Record the change in `CHANGELOG.md`**

Under `## [Unreleased]`, `### Changed`, add:

```markdown
- `sites.<site>.ssh.user` is optional: a site that leaves it out is reached as the deployment's own user, `paisans-<token>`, which must exist on the host.
```

(24 words.)

- [ ] **Step 9: Commit**

```bash
git add internal/config cmd/paisans internal/grant internal/siteremove internal/hostprep README.md examples/paisans-stack.example.yaml CHANGELOG.md
git commit -F - <<'EOF'
feat: ssh.user defaults to the deployment's own user

Every deployment is to run as its own unprivileged user, paisans-<token>,
admitted once by the host's owner, so the name is known from the
deployment's id and need not be written. Load records it on every site and
Site.SSHUser returns ssh.user or, when it is empty, that default; the
transport, the grant's login, host prepare's keys and site remove's note
all use it. A declared name is still held to the same pattern. The default
is not written into ssh.user, so a later command can tell a declared name
from a derived one.

Founder decision.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

---

### Task 3: `config.SetSSHUser` records a user without overwriting one

**Files:**
- Create: `internal/config/ssh_user.go`
- Create: `internal/config/ssh_user_test.go`
- Test: `internal/config/config_test.go` (file round trip)

**Interfaces:**
- Consumes: `unixUser` (`internal/config/ssh.go`), `topMapping` and `splice` (`internal/config/set_catalog.go`), `lookup`, `lineStarts`, `lineIndex`, `replaceScalar`, `edit`, `replaceFile` (`internal/config/mesh.go`), and `(Site).SSHUser()` from Task 2 (round-trip test only).
- Produces: `func SetSSHUser(path, site, user string) error`, exported for phase 2; unexported `func setSSHUser(data []byte, site, user string) ([]byte, error)` and `func plainOrQuoted(value string) string`. No command calls `SetSSHUser` in this phase.

- [ ] **Step 1: Write the failing splice tests**

Create `internal/config/ssh_user_test.go`:

```go
package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSetSSHUser(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			"absent, in a block, with comments",
			"sites:\n  # the home server\n  home-a:\n    roles: [data, apps]\n    ssh:\n      # reached on the LAN\n      host: home-a.local   # its mDNS name\n      keys: [alice]\n",
			"sites:\n  # the home server\n  home-a:\n    roles: [data, apps]\n    ssh:\n      # reached on the LAN\n      user: paisans-f2a9\n      host: home-a.local   # its mDNS name\n      keys: [alice]\n",
		},
		{
			"absent, in a flow mapping",
			"sites:\n  home-a:\n    ssh: {host: home-a.local, keys: [alice]}\n",
			"sites:\n  home-a:\n    ssh: {user: paisans-f2a9, host: home-a.local, keys: [alice]}\n",
		},
		{
			"present, the same name",
			"sites:\n  home-a:\n    ssh:\n      user: paisans-f2a9\n      keys: [alice]\n",
			"sites:\n  home-a:\n    ssh:\n      user: paisans-f2a9\n      keys: [alice]\n",
		},
		{
			"present, the same name quoted",
			"sites:\n  home-a:\n    ssh:\n      user: \"paisans-f2a9\"\n      keys: [alice]\n",
			"sites:\n  home-a:\n    ssh:\n      user: \"paisans-f2a9\"\n      keys: [alice]\n",
		},
		{
			"written empty, double quoted",
			"sites:\n  home-a:\n    ssh:\n      user: \"\"\n      keys: [alice]\n",
			"sites:\n  home-a:\n    ssh:\n      user: \"paisans-f2a9\"\n      keys: [alice]\n",
		},
		{
			"written empty, single quoted",
			"sites:\n  home-a:\n    ssh:\n      user: ''\n      keys: [alice]\n",
			"sites:\n  home-a:\n    ssh:\n      user: 'paisans-f2a9'\n      keys: [alice]\n",
		},
		{
			"written as ~, with a comment",
			"sites:\n  home-a:\n    ssh:\n      user: ~   # filled in on first contact\n      keys: [alice]\n",
			"sites:\n  home-a:\n    ssh:\n      user: paisans-f2a9   # filled in on first contact\n      keys: [alice]\n",
		},
		{
			"only the named site",
			"sites:\n  home-a:\n    ssh:\n      keys: [alice]\n  vm:\n    ssh:\n      keys: [alice]\n",
			"sites:\n  home-a:\n    ssh:\n      user: paisans-f2a9\n      keys: [alice]\n  vm:\n    ssh:\n      keys: [alice]\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := setSSHUser([]byte(c.in), "home-a", "paisans-f2a9")
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != c.want {
				t.Fatalf("got:\n%s\nwant:\n%s", out, c.want)
			}
		})
	}
}

func TestSetSSHUserRefuses(t *testing.T) {
	cases := []struct{ name, in, site, user, want string }{
		{"another name", "sites:\n  home-a:\n    ssh:\n      user: ubuntu\n      keys: [alice]\n", "home-a", "paisans-f2a9", "sites.home-a.ssh.user already names ubuntu"},
		{"a user written with no value", "sites:\n  home-a:\n    ssh:\n      user:\n      keys: [alice]\n", "home-a", "paisans-f2a9", "written with no value"},
		{"an undeclared site", "sites:\n  home-a:\n    ssh:\n      keys: [alice]\n", "home-z", "paisans-f2a9", "sites.home-z is not declared"},
		{"a site with no ssh section", "sites:\n  home-a:\n    roles: [data]\n", "home-a", "paisans-f2a9", "sites.home-a.ssh is not a section"},
		{"a name this toolkit refuses", "sites:\n  home-a:\n    ssh:\n      keys: [alice]\n", "home-a", "bob; rm", "is not a user name this toolkit accepts"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := setSSHUser([]byte(c.in), c.site, c.user); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error containing %q", err, c.want)
			}
		})
	}
}

// A name YAML would read as another type is written quoted, so the file
// loads back as the same string.
func TestSetSSHUserQuotesANameYAMLWouldReadAsAnotherType(t *testing.T) {
	for _, name := range []string{"null", "true"} {
		t.Run(name, func(t *testing.T) {
			out, err := setSSHUser([]byte("sites:\n  home-a:\n    ssh:\n      keys: [alice]\n"), "home-a", name)
			if err != nil {
				t.Fatal(err)
			}
			if want := "sites:\n  home-a:\n    ssh:\n      user: \"" + name + "\"\n      keys: [alice]\n"; string(out) != want {
				t.Fatalf("got:\n%s\nwant:\n%s", out, want)
			}
			var back struct {
				Sites map[string]struct {
					SSH struct {
						User string `yaml:"user"`
					} `yaml:"ssh"`
				} `yaml:"sites"`
			}
			if err := yaml.Unmarshal(out, &back); err != nil || back.Sites["home-a"].SSH.User != name {
				t.Fatalf("read back as %q, %v", back.Sites["home-a"].SSH.User, err)
			}
		})
	}
}
```

In `internal/config/config_test.go`, append (it needs a whole valid file, which only the external package's fixtures provide):

```go
// SetSSHUser writes the file in place, keeping its mode, and Load then reads
// the name as declared; a second call with the same name changes nothing.
func TestSetSSHUserWritesTheFile(t *testing.T) {
	path := write(t, withSSH("    ssh:\n      host: home-a.local\n      keys: [alice]\n"))
	if err := config.SetSSHUser(path, "home-a", "paisans-f2a9"); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Sites["home-a"].SSH.User; got != "paisans-f2a9" {
		t.Errorf("ssh.user read as %q, want paisans-f2a9", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", info.Mode().Perm())
	}
	if err := config.SetSSHUser(path, "home-a", "paisans-f2a9"); err != nil {
		t.Fatal(err)
	}
	if second, _ := os.ReadFile(path); string(second) != string(first) {
		t.Errorf("a second call changed the file:\n%s", second)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/ -run 'TestSetSSHUser'`
Expected: FAIL to build, `undefined: setSSHUser` and `undefined: config.SetSSHUser`.

- [ ] **Step 3: Write `internal/config/ssh_user.go`**

```go
package config

import (
	"bytes"
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// SetSSHUser writes sites.<site>.ssh.user into the declaration at path, and
// leaves every other byte as it was. It records the user a command has
// connected as, so that the name in the file is the name used from then on;
// a name already there is that record and is never overwritten, and the
// same name already there writes nothing.
//
// The line goes ahead of the ssh section's first key, indented as that key
// is, as SetArch places arch: the first key is the one place in a block
// mapping where a new line is valid whatever the keys after it hold. In a
// flow mapping it goes in front of the first key, followed by `, `. A user
// written empty (`user: ""`, `user: ''`, `user: ~`) names nobody, so the
// name is written where that value stands, in its quoting.
func SetSSHUser(path, site, user string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	out, err := setSSHUser(data, site, user)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if bytes.Equal(out, data) {
		return nil
	}
	return replaceFile(path, out)
}

func setSSHUser(data []byte, site, user string) ([]byte, error) {
	if !unixUser.MatchString(user) {
		return nil, fmt.Errorf("%q is not a user name this toolkit accepts", user)
	}
	top, err := topMapping(data)
	if err != nil {
		return nil, err
	}
	_, sites := lookup(top, "sites")
	_, s := lookup(sites, site)
	if s == nil || s.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("sites.%s is not declared as a section this command can add a user to", site)
	}
	_, ssh := lookup(s, "ssh")
	if ssh == nil || ssh.Kind != yaml.MappingNode || len(ssh.Content) == 0 {
		return nil, fmt.Errorf("sites.%s.ssh is not a section this command can add user to. Give the site its ssh section, with its keys, first", site)
	}
	lines := lineStarts(data)
	_, value := lookup(ssh, "user")
	switch {
	case value == nil:
	case value.Kind != yaml.ScalarNode:
		return nil, fmt.Errorf("sites.%s.ssh.user is not a single value", site)
	case value.Value == user:
		return data, nil
	case value.Tag == "!!null" && value.Value == "":
		return nil, fmt.Errorf("sites.%s.ssh.user is written with no value, in a form this command cannot edit in place. Write the name, Eg: user: %s, or remove the line", site, user)
	case value.Tag == "!!null" || value.Value == "":
		text := user
		if value.Style == 0 {
			text = plainOrQuoted(user)
		}
		e, err := replaceScalar(data, lines, value, text)
		if err != nil {
			return nil, fmt.Errorf("sites.%s.ssh.user: %w", site, err)
		}
		return splice(data, e), nil
	default:
		return nil, fmt.Errorf("sites.%s.ssh.user already names %s, which is never overwritten", site, value.Value)
	}
	first := ssh.Content[0]
	if ssh.Style&yaml.FlowStyle != 0 {
		runes := []rune(lines.text(data, first.Line))
		if first.Column-1 > len(runes) {
			return nil, fmt.Errorf("sites.%s.ssh could not be found on line %d", site, first.Line)
		}
		at := lines.start(first.Line) + len(string(runes[:first.Column-1]))
		return splice(data, edit{at, at, "user: " + plainOrQuoted(user) + ", "}), nil
	}
	at := lines.start(first.Line)
	return splice(data, edit{at, at, fmt.Sprintf("%*suser: %s\n", first.Column-1, "", plainOrQuoted(user))}), nil
}

// plainOrQuoted is value as YAML is to read it back: plain when the plain
// text is read as the same string, double quoted when it would be read as
// something else, Eg: null or true.
func plainOrQuoted(value string) string {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(value), &doc); err == nil && len(doc.Content) == 1 {
		if n := doc.Content[0]; n.Kind == yaml.ScalarNode && n.Tag == "!!str" && n.Value == value {
			return value
		}
	}
	return strconv.Quote(value)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/config/ -run 'TestSetSSHUser' -v`
Expected: PASS for `TestSetSSHUser` (8 subtests), `TestSetSSHUserRefuses` (5), `TestSetSSHUserQuotesANameYAMLWouldReadAsAnotherType` (2) and `TestSetSSHUserWritesTheFile`.

If `written as ~, with a comment` fails, print the node (`t.Logf("%+v", value)`) to confirm yaml.v3 reports `~` as `Tag "!!null"`, `Value "~"`, `Style 0`; `replaceScalar` replaces exactly the text `~` at its column.

Run: `go test ./internal/config/`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/config/ssh_user.go internal/config/ssh_user_test.go internal/config/config_test.go
git commit -F - <<'EOF'
feat: config.SetSSHUser records the user a command connected as

Once a command has connected as the deployment's user, the name goes into
sites.<site>.ssh.user so the file says who logs in. SetSSHUser splices the
line in as SetArch does, keeping every other byte, and never overwrites a
name already there; a value written empty names nobody and is filled in
where it stands. A name YAML would read as another type is quoted. Nothing
calls it yet: the transports in the next phase do.

Founder decision.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

---

### Task 4: Verify the branch

**Files:** none changed unless a check fails.

- [ ] **Step 1: Format and vet**

Run: `gofmt -l cmd internal`
Expected: no output.

Run: `go vet ./...`
Expected: no output.

- [ ] **Step 2: Run the whole suite**

Run: `go test ./...`
Expected: every package `ok` or `[no test files]`. The Docker tests behind build tags are not run: nothing here touches what they cover (Docker and compose on a host); say so in the hand-off.

- [ ] **Step 3: Check the diff for em dashes, leftovers and history wording**

Run: `git diff develop...HEAD | perl -CSD -ne 'print "$.: $_" if /\x{2014}/'`
Expected: no output.

Run: `git grep -n 'host allow\|host disallow\|ssh-allow\|AllowPath\|allowGuard\|AllowlistProbe' -- ':!docs/decisions.md' ':!docs/specs' ':!docs/superpowers' ':!CHANGELOG.md'`
Expected: only `internal/hostprep/hostprep_test.go` (the test proving the file is ignored).

Run: `git diff develop...HEAD -- '*.md' '*.go' ':!CHANGELOG.md' ':!docs/superpowers' | grep '^+' | grep -in 'no longer\|used to\|was removed\|were removed'`
Expected: no output.

Run: `git diff develop...HEAD | perl -ne 'print if /^\+/ && /\b(\d{1,3}\.){3}\d{1,3}\b/ && !/10\.44\.|203\.0\.113\.|192\.0\.2\./'`
Expected: no output (no real address enters the repository).

- [ ] **Step 4: Check the changelog**

Run: `sed -n '/## \[Unreleased\]/,/## \[0/p' CHANGELOG.md`
Expected: the `### Removed` bullet from Task 1 and the `### Changed` bullet from Task 2, each one sentence of at most 35 words, under `[Unreleased]`, with categories in Keep a Changelog order (Added, Changed, Removed, Fixed).

- [ ] **Step 5: Read the whole diff**

Run: `git diff develop...HEAD --stat` and then `git diff develop...HEAD`
Expected: only the files this plan names. Read every hunk. Nothing to commit unless a check above failed; a fix goes in its own commit in the house style.

---

## Self-Review

**Spec coverage.** *The deployment's user*: optional `ssh.user` and the `paisans-<token>` default (Task 2); `SetSSHUser`, splicing as `SetArch` and never overwriting (Task 3); the user written back by "the first command that connects" is phase 2. *Keys*: `host prepare` keeps managing `authorized_keys` from `ssh.keys` (Task 1 keeps it, Task 2 points it at the default user); the sidecar's move to the deployment's root is phase 2 or later, since it needs the deployment's user to own it. *Settled*: `host allow`, `host disallow` and the allowlist removed (Task 1). Privileged-group refusals and doctor's group check belong to `host admit` (phase 5) and are not here. The index's "validate accepts a site without `user`" is `TestSSHUserDefaultsToTheDeploymentsOwnUser`; its "golden files" for `SetSSHUser` are written as an inline table, the style `arch_test.go` uses for `setArch`.

**Placeholders.** None: every code step carries its code, every run its command and expected result.

**Type consistency.** `planAuthorizedKeys(t Transport, d deployment.Deployment, user string, s config.SSH)` in Task 1 Step 6, called in Task 1 Step 7 with `declared.SSH.User` and in Task 2 Step 5 with `declared.SSHUser()`. `(Site).SSHUser() string` defined in Task 2 Step 4, used in Task 2 Step 5 and Task 3's round trip. `setSSHUser(data []byte, site, user string) ([]byte, error)` and `SetSSHUser(path, site, user string) error` match between Task 3's tests and code. `edit{start, end, text}` and `splice(data, e)` match `mesh.go` and `set_catalog.go`.

**Review Focus.** Each of its five lines has its test: empty `user` (Task 2 Step 1, Task 3 Step 1), names YAML would retype (Task 3 Step 1), the default user's paths in `host prepare` (Task 2 Step 2), a hand-built `Site` (Task 2 Step 1), a leftover `ssh-allow` file (Task 1 Step 1).
