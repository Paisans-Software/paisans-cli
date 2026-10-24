# System Copy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One trusted system copy per deployment in the data directory; folders are drafts of the three files with a "drafted from" line; every trust decision reads the system copy.

**Architecture:** The system copy is the saved copy `localstore.Store.Local(id)` already keeps under `<Base>/deployments/<id>/` — a full `configstore.Local` whose `.paisans/` lives in the data directory. Every command that today builds a `Local` from the `--config` folder (`workstationOf`) gets the system copy by deployment id instead; the folder's three files are read and written as a draft through a small `internal/draft` package. Deploying copies the draft's files (marker stripped) into the system copy and publishes from there with the existing configstore functions.

**Tech Stack:** Go; `internal/configstore`, `internal/localstore`, `gopkg.in/yaml.v3`, sops via `internal/config`.

**Spec:** `docs/specs/2026-10-24-system-copy.md`. Code map: `.superpowers/system-copy-map.md` (file:line for every Local use, writer and test helper).

## Global Constraints

- Branch `feat/system-copy` in `.worktrees/system-copy`, stacked on `feat/operator-invite` (PR #514). The PR targets `feat/operator-invite`. Never `git stash`.
- No conversion of existing folders or `.paisans/` directories; nothing is released.
- The host's `/srv/paisans/<token>/.paisans/` is a different thing and is not touched.
- The draft marker, verbatim: first line of `paisans-stack.yaml` = `# paisans: drafted from revision <N> (<full sha>)`. It is stripped from the bytes before any commit, and rewritten after start, merge and deploy.
- No folder ever gets a `.paisans/` directory from any command.
- Secret values never printed; merges and reviews name secret keys only.
- Commands go ahead on whichever sites answer (no majority prompt); `rollback` stays.
- CI checks only the changelog: run `go build ./... && go vet ./... && test -z "$(gofmt -l .)" && go test ./...` before each push, and once from a fresh clone before the PR.
- Docs and messages describe current state: no "no longer", history or rejected alternatives; "blueprint", never "kind"; no Outline or ticket references.
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **A folder with a draft for a deployment whose system copy is missing** (a second workstation, a wiped data dir): refused before anything is written, naming `deployment recover`/the join. Test in Task 2 (`TestSystemForRefusesMissingCopy`).
2. **A deploy that fails after the draft's files were copied into the system copy** (no site takes the version, a host apply fails before publish): the system copy's files and records are back to what they were. Test in Task 4 (`TestDeployRestoresSystemCopyOnPublishFailure`).
3. **A draft whose marker names a sha the system copy's history does not hold** (copied from another deployment, hand-edited): falls back to the system copy's files as base and says so; never used as a base. Test in Task 6 (`TestMergeBaseIgnoresUnknownMarker`).
4. **Two commands at once on one deployment** (two terminals): the second writer is refused naming the first; a reader is not blocked. Test in Task 2 (`TestLockRefusesSecondWriter`).
5. **The marker in a committed version:** a version published from a draft never contains the `# paisans: drafted from` line, so the sites and other operators' system copies never see it. Test in Task 4 (`TestPublishedVersionHasNoMarker`).

---

### Task 1: `internal/draft`: the marker and the draft's files

**Files:**
- Create: `internal/draft/draft.go`, `internal/draft/draft_test.go`

**Interfaces:**
- Produces:
  ```go
  type Marker struct{ Revision int; SHA string }
  func ReadMarker(paisans []byte) (Marker, bool)          // first line only; false when absent or malformed
  func WithMarker(paisans []byte, m Marker) []byte        // replaces an existing marker line or prepends one
  func StripMarker(paisans []byte) []byte                 // removes the first line when it is a marker
  type Draft struct{ Dir string }                          // a folder's three files
  func (d Draft) Files() (configstore.Files, error)        // reads the three files; Paisans with the marker kept
  func (d Draft) Has() bool                                // paisans-stack.yaml exists in Dir
  func (d Draft) Write(f configstore.Files, m Marker) error // writes the three files (atomic, secrets 0600), marker set
  ```
- [ ] **Step 1: Tests:** `TestMarkerRoundTrip` (WithMarker then ReadMarker gives the same Revision/SHA; the line is exactly `# paisans: drafted from revision 10 (<sha>)`); `TestWithMarkerReplaces` (one marker line after two calls); `TestStripMarker` (bytes equal the original without the line; a file without a marker is unchanged); `TestReadMarkerRejects` (marker not on line 1, short sha, non-hex, revision 0 → false); `TestDraftWriteFiles` (three files written, secrets mode 0600, no `.paisans` created).
- [ ] **Step 2:** `go test ./internal/draft/` → FAIL (package missing).
- [ ] **Step 3:** Implement. The sha is 40 or 64 lowercase hex; Write uses the same atomic write as `configstore.writeFiles` (export a helper from configstore if needed rather than copying it).
- [ ] **Step 4:** `go test ./internal/draft/` → PASS.
- [ ] **Step 5:** Commit `feat: internal/draft keeps a draft's files and its drafted-from line`.

### Task 2: Finding the system copy, the lock, starting a draft, and per-test data dirs

**Files:**
- Create: `cmd/paisans/systemcopy.go`, `cmd/paisans/systemcopy_test.go`
- Modify: `internal/localstore/localstore.go` (lock), `cmd/paisans/nodns_test.go` (TestMain), `cmd/paisans/deploymentcheckout.go` (rewritten onto startDraft), its tests

**Interfaces:**
- Consumes: Task 1.
- Produces:
  ```go
  // systemFor finds the deployment of the draft at configPath (its id), or, with no draft there,
  // the one id names, or the only system copy on the workstation. It refuses a missing system copy.
  func systemFor(configPath, id string) (sys configstore.Local, d draft.Draft, deploymentID string, err error)
  // startDraft writes the system copy's head files into d with the marker naming that head.
  func startDraft(sys configstore.Local, d draft.Draft) error
  // ensureDraft is startDraft when d has no files, otherwise nothing; writers call it first.
  func ensureDraft(configPath, id string) (draft.Draft, error)
  func (s localstore.Store) Lock(id string) (unlock func(), err error) // exclusive, non-blocking; refusal names the holder's pid and command
  ```
- `deployment checkout [--id <id>] [--head <sha>]` becomes `startDraft` into the folder: refuses a folder that holds a draft; `--head` writes a trusted head's files from the system copy's store (the hand-merge source for `publish --merge`, replacing `incoming/` in folders).
- Test isolation: `TestMain` no longer sets one shared `PAISANS_DATA_DIR` for every test that touches the system copy; add `withSystemCopy(t, id) configstore.Local` (sets a per-test `PAISANS_DATA_DIR` with `t.Setenv`, returns `Store.Local(id)`), and move the existing `withDataDir` helper here.
- [ ] **Step 1: Tests:** `TestSystemForByDraftID`, `TestSystemForOnlyCopy`, `TestSystemForNeedsIDWhenSeveral` (refusal lists ids), `TestSystemForRefusesMissingCopy` (Review Focus 1: draft with id X, no `deployments/X` → refused naming `deployment recover`, nothing written), `TestStartDraftWritesMarker`, `TestEnsureDraftLeavesExistingDraft`, `TestLockRefusesSecondWriter` (Review Focus 4), `TestCheckoutStartsDraft`, `TestCheckoutHeadWritesThatHead`, `TestCheckoutRefusesExistingDraft`, `TestNoFolderStateFromCheckout` (no `.paisans` in the folder).
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement; rewrite `deployment checkout` on `startDraft` and drop its folder-state copying (`checkoutState`, `heldState`, `copyCheckout`, `sameTrust`), keeping `judgeSaved`'s trust check of the system copy before writing.
- [ ] **Step 4:** `go test ./cmd/paisans/ -run 'SystemFor|StartDraft|EnsureDraft|Lock|Checkout' ./internal/localstore/ ./internal/draft/` → PASS.
- [ ] **Step 5:** Commit `feat: find the system copy by deployment id and start drafts from it`.

### Task 3: Read-only and trust readers use the system copy

**Files:**
- Modify: `cmd/paisans/deployment.go` (log), `operatorinvite.go`, `appremove.go`, `apply_identity.go`, `operatorregister.go` (plain register, attest), `operatoradd.go` (`--request` root check), their tests

**Interfaces:**
- Consumes: `systemFor` (Task 2).
- `deployment log`, `operator invite`, `app remove`'s `Declaring`, `openIdentity`, `operator register`'s anchor/attest and `add --request`'s `trustedRoot` take `sys` from `systemFor`, never a folder Local. `register --root` with no system copy is the one path that may create it (a pinned root anchors alone), as `recover` does.
- [ ] **Step 1: Tests:** for each command, a test that runs it beside a draft with a system copy holding the trust state and asserts it reads the system copy's root/head/key (e.g. `TestLogReadsSystemCopy`, `TestInviteRootFromSystemCopy`, `TestRegisterAttestsFromSystemCopy`, `TestAddRequestRootFromSystemCopy`, `TestAppRemoveDeclaringFromSystemCopy`), and that no `.paisans` appears in the folder.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Swap each `workstationOf` caller listed; migrate the affected test fixtures (`inviteWorkstation`, `unpinnedWorkstation`, `registerWorkstation`, `newRequestDeployment`) to `withSystemCopy`.
- [ ] **Step 4:** `go test ./cmd/paisans/ -run 'Log|Invite|Register|OperatorAdd|AppRemove|Identity'` → PASS.
- [ ] **Step 5:** Commit `refactor: log, invite, register, add --request and app remove read the system copy`.

### Task 4: Deploying a draft (apply and publish)

**Files:**
- Modify: `cmd/paisans/config_apply.go` (`gatherForApply`, `publish`, `next`, `saved`, `showChange`), `cmd/paisans/deployment.go` (publish), `internal/localstore/localstore.go` (Save becomes "set the system copy's files"), tests (`config_apply_test.go`, `deployment_test.go`)

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces: `func deployDraft(sys configstore.Local, d draft.Draft, publish func() (sha string, rev int, err error)) error` — reads the system copy's three files, writes the draft's files with the marker stripped into the system copy, runs publish, and on error writes the system files back byte for byte; on success rewrites the draft's marker to the published version.
- Flow per spec *Deploying a draft*: sync (gather into `sys`); base = `ReadMarker` when its sha is in `sys`'s trusted history, else `sys`'s head; sites' newest == base → deploy; newer → **refuse for now**, naming `deployment pull` (Task 6 replaces this with the merge); fork → refuse naming `deployment publish --merge`; dry run records versions only. The review's diff (`showChange`) compares the draft (marker stripped) with the system copy's head. `publishWritten` (secrets written mid-apply) writes the draft and deploys it the same way.
- [ ] **Step 1: Tests:** `TestApplyDeploysDraftIntoSystemCopy`, `TestPublishedVersionHasNoMarker` (Review Focus 5), `TestDeployRewritesMarker`, `TestDeployRestoresSystemCopyOnPublishFailure` (Review Focus 2), `TestApplyRefusesNewerUntilPulled`, `TestApplyRefusesFork`, `TestApplyDryRunWritesNothing` (no draft or system file changes, versions recorded), `TestPublishDeploysDraft`, `TestApplyNoFolderState`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement; migrate `newApplyStore` and the apply/publish tests to a draft folder + `withSystemCopy`.
- [ ] **Step 4:** `go test ./cmd/paisans/ -run 'Apply|Publish|Converge'` → PASS.
- [ ] **Step 5:** Commit `feat: apply and publish deploy a draft into the system copy`.

### Task 5: Recovery, the join and fetch create the system copy

**Files:**
- Modify: `cmd/paisans/deployment.go` (recover, `recoverFiles`), `operatorjoin.go`, `deploymentlocal.go` (fetch), tests

**Interfaces:**
- Consumes: Tasks 1–4.
- `recover` and the join gather into the system copy (`Store.Local(id)`, created here), record root/seen/key there, then `startDraft` into the folder. The folder must have no draft. A fork on recover refuses, naming `deployment checkout --head` and `publish --merge`, with the heads kept in the system copy's store. `fetch` keeps writing only the system copy (its `incoming/` stays inside the system copy's `.paisans/`).
- [ ] **Step 1: Tests:** `TestRecoverCreatesSystemCopyAndDraft`, `TestRecoverRefusesExistingDraft`, `TestRecoverForkRefuses`, `TestJoinCreatesSystemCopyAndDraft`, `TestFetchWritesOnlySystemCopy`, and `TestInviteAnswerAcceptJoin` (the end-to-end test) updated to assert the system copy and the draft marker.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement; migrate `recoverHosts`, `newJoinFixture`, the recover/join/fetch tests.
- [ ] **Step 4:** `go test ./cmd/paisans/ -run 'Recover|Join|Fetch|InviteAnswerAcceptJoin'` → PASS.
- [ ] **Step 5:** Commit `feat: recovery, the join and fetch create the system copy and start a draft`.

### Task 6: The three-way merge, pull, and apply's merge-forward

**Files:**
- Create: `internal/draftmerge/draftmerge.go`, `internal/draftmerge/draftmerge_test.go`
- Modify: `cmd/paisans/deployment.go` (pull), `cmd/paisans/config_merge.go` (replaced), `cmd/paisans/config_apply.go` (newer-version branch), tests

**Interfaces:**
- Consumes: Tasks 1–5; `configstore.MergeSecrets`.
- Produces:
  ```go
  // Config merges paisans-stack.yaml path by path on the yaml.v3 node tree. A mapping key is a path;
  // a sequence or scalar is one value. Comments and key order come from ours.
  func Config(base, ours, theirs []byte) (merged []byte, conflicts []string, err error)
  // SOPS merges .sops.yaml the same way (each creation rule is one value).
  func SOPS(base, ours, theirs []byte) (merged []byte, conflicts []string, err error)
  ```
  A conflict keeps ours at that path and is listed as its dotted path.
- `deployment pull`: sync; move the system copy to the trusted head (`sys.Checkout` + `recordPull`); with a draft, merge it forward from the base (marker or system files before the sync) with `draftmerge.Config`, `draftmerge.SOPS` and `MergeSecrets` (secrets re-encrypted to the merged `.sops.yaml`'s recipients), write the draft with the marker naming the new head, and list conflicts (secret keys by name); without a draft, `startDraft`. Remove `pullOverEdits`, `pullMergingSecrets`, `secretsOnlyEdits` and every folder `incoming/` write.
- `apply` when the sites are newer than the base: the same merge into the draft, then stop: "merged revision N into this draft; review it and run apply again" (conflicts listed; nothing deployed).
- [ ] **Step 1: Tests (draftmerge):** `TestConfigMergesDisjointPaths`, `TestConfigConflictKeepsOurs` (path listed), `TestConfigBothSameChangeNoConflict`, `TestConfigRemovedPath` (ours removed, theirs unchanged → removed; theirs changed → conflict), `TestConfigSequenceIsOneValue`, `TestConfigKeepsOurComments`, `TestSOPSRuleIsOneValue`.
- [ ] **Step 2: Tests (cmd):** `TestPullMergesDraftForward`, `TestPullConflictListsPaths`, `TestPullSecretsConflictNamesKeysOnly`, `TestPullStartsDraftWhenNone`, `TestMergeBaseFromMarker` (two folders drafted from revision 10; the second is merged from 10 after the first deploys), `TestMergeBaseIgnoresUnknownMarker` (Review Focus 3: notice printed, system files used), `TestApplyMergesNewerThenStops`.
- [ ] **Step 3:** Run → FAIL.
- [ ] **Step 4:** Implement.
- [ ] **Step 5:** `go test ./internal/draftmerge/ ./cmd/paisans/ -run 'Pull|Merge|Apply'` → PASS.
- [ ] **Step 6:** Commit `feat: drafts merge forward from the version they were drafted from`.

### Task 7: Every file-writing command writes the draft

**Files:**
- Modify: the writers in the map's *Writers* section: `main.go` (init's writers, `siteTransport`'s `recordSSHUser`), `create.go`, `operatoradd.go`, `secrets.go`, `secretsrotate.go`, `meshrekey.go`, `rotatekey.go`, `oidc.go`, `clients.go`, `signupgroups.go`, `converge.go` (`convergeGenerate`), `arch.go` callers, `siteremove.go`, `doctor.go`, `catalog.go`, and tests

**Interfaces:**
- Consumes: `ensureDraft` (Task 2).
- Each writer command calls `ensureDraft(configPath, idFlag)` before its first write, so a folder with no draft is started from the system copy. `init` and `create` on a deployment with no system copy (a new deployment) write the draft without one; the first deploy creates the system copy (Task 4's deploy path does this when `sys` has no head).
- [ ] **Step 1: Tests:** a table test `TestWritersStartADraft` running each writer (operator add, secrets set, secrets rotate (dry and --execute as each supports), storage rotate-key, oidc client create, site remove, catalog add, doctor --fix) in an empty folder with a system copy: the folder gains a draft with the marker, the change is in the draft, the system copy's files are unchanged. `TestInitNewDeploymentHasNoSystemCopyUntilDeploy`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `go test ./cmd/paisans/` → PASS.
- [ ] **Step 5:** Commit `feat: every command that writes the deployment's files writes the draft`.

### Task 8: Undo flags in the review

**Files:**
- Create: `cmd/paisans/undoflags.go`, `cmd/paisans/undoflags_test.go`
- Modify: `cmd/paisans/config_apply.go` (`showChange`)

**Interfaces:**
- Produces: `func undoFlags(sys configstore.Local, head string, draft configstore.Files) ([]string, error)` — for each path the draft changes (dotted paths of `paisans-stack.yaml` via the same flattening `draftmerge` uses; secret keys by name via `flatKeys`), walk the trusted history from `head` back to the most recent revision that changed that path; when the draft's value equals the value before that revision, return `<path>: put back; revision N (signed by <operator>) changed it.` (added back / removed / changed wording per case).
- [ ] **Step 1: Tests:** `TestUndoFlagSiteAddedBack` (exact line `sites.home-b: added back; revision 15 (signed by alice) removed it.`), `TestUndoFlagValueChangedTwice`, `TestUndoFlagPathRemovedAndReAdded`, `TestUndoFlagSecretKeyByNameOnly` (no value in output), `TestNoUndoFlagForNewChange`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement; print the flags through `r.Verbatim` under the review's difference.
- [ ] **Step 4:** `go test ./cmd/paisans/ -run 'UndoFlag|Apply'` → PASS.
- [ ] **Step 5:** Commit `feat: apply's review flags changes that put back an older version's values`.

### Task 9: Arrival record and the stale-draft warning

**Files:**
- Modify: `internal/configstore/local.go` (arrival record), `cmd/paisans/config_apply.go`, tests

**Interfaces:**
- Produces: `func (l Local) RecordArrival(g Gathered, now time.Time) error` (records, for each trusted version not yet recorded, when it first reached this workstation, in the system copy's `.paisans/arrivals` as `<sha> <RFC3339>` lines) and `func (l Local) ArrivedAt(sha string) (time.Time, bool)`. Every sync calls `RecordArrival`, dry runs included.
- Apply's review: when any draft file's modification time is before `ArrivedAt(newest trusted head)`, print once: `This draft was last saved before revision N reached this workstation; it may not include revision N's changes.`
- [ ] **Step 1: Tests:** `TestRecordArrivalOnce` (second sync keeps the first time), `TestStaleDraftWarns`, `TestFreshDraftDoesNotWarn`, `TestDryRunRecordsArrival`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `go test ./internal/configstore/ ./cmd/paisans/ -run 'Arrival|StaleDraft|Apply'` → PASS.
- [ ] **Step 5:** Commit `feat: apply warns about a draft saved before the newest revision arrived`.

### Task 10: No per-folder state anywhere; docs

**Files:**
- Modify: `cmd/paisans/deployment.go` (delete `workstationOf`; messages), `internal/configstore/local.go` (folder `.gitignore` text only applies to the system copy; comments), `help.go`, `README.md`, `docs/commands.md`, `docs/guides/how-a-deployment-protects-itself.md`, `docs/specs/2026-10-12-config-store.md`, `docs/specs/2026-10-24-operator-invite.md`, `CHANGELOG.md`

- [ ] **Step 1: Test:** `TestNoCommandWritesFolderState` — run, in one draft folder with a system copy and fake sites: checkout, validate, operator add, secrets set, pull, apply (dry and --execute), publish, log, register, invite; assert the folder never contains `.paisans`.
- [ ] **Step 2:** Run → FAIL if any path still writes it; fix.
- [ ] **Step 3:** Delete `workstationOf` and any remaining folder-Local construction; `grep -rn 'workstationOf\|Local{Dir' cmd internal --include='*.go' | grep -v _test` prints only `localstore`.
- [ ] **Step 4: Docs:** describe the system copy, drafts, the drafted-from line, merge-forward, the review's flags and warning, `checkout --head`, in current-state wording; remove every workstation `.paisans/`-in-a-folder instruction (README *Every site keeps signed copies…* and *This workstation keeps every deployment it applies*, commands.md publish/pull/log/recover/list/checkout/fetch/register/invite, the guide, both specs). CHANGELOG: one `### Changed` bullet and one `### Added` bullet, each ≤35 words.
- [ ] **Step 5:** `go build ./... && go vet ./... && test -z "$(gofmt -l .)" && go test ./...`; fresh clone: `go test ./cmd/paisans/` → PASS.
- [ ] **Step 6:** Commit `refactor: no folder keeps Paisans state; docs describe the system copy`.
