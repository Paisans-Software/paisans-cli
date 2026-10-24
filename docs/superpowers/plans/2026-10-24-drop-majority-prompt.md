# Drop the Majority Prompt Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every command that gathers from the sites goes ahead on whichever sites answer, with no `partial` prompt and no warning that one is coming.

**Architecture:** `Gathered.Majority` is read only by `confirmPartial` and the dry-run warnings beside it. Removing the field removes the `configured` count it was judged against, which threads through `configstore.GatherAnchored`, `gather` and `gatherAt`. Unreached sites keep being reported per site, and "no site answered" refusals stay.

**Tech Stack:** Go, `internal/configstore`, `cmd/paisans`.

**Spec:** `docs/specs/2026-10-24-operator-invite.md`, section *No majority prompt*.

## Global Constraints

- Branch `fix/drop-majority-prompt`, cut from `origin/develop`, in `.worktrees/drop-majority-prompt`. Its own pull request, merged before the invite plan starts.
- Never use `git stash`.
- CI runs only the changelog check: run `go build ./... && go vet ./... && gofmt -l . && go test ./...` locally before the PR.
- Docs describe current state: no "no longer", no history of the prompt, no rejected alternatives.
- PR title and body never mention Outline pages or tickets.
- One CHANGELOG bullet, at most 35 words, under `## [Unreleased]` → `### Removed`.

## Review Focus

1. A run without a terminal and without `--yes` that reached one site of four: goes ahead, where it used to refuse. Pinned in Task 1 by the rewritten register test.
2. A first-version `publish` that reached one site of three: goes ahead with no prompt. Pinned in Task 1.
3. A command that reached no site: still refused with the message it has now. Existing tests cover it; Task 1 keeps them passing unchanged.
4. `--yes` output: no longer prints `approved by --yes: partial`. `yes_test.go` loses that word; Task 1.
5. Help and docs: no command's help still says it asks for `partial`. Task 2 greps for it.

---

### Task 1: Remove `Majority`, `configured` and `confirmPartial`

**Files:**
- Modify: `internal/configstore/local.go` (field at :501, computation at :606, `GatherAnchored` and `gather` signatures at :569 and :573, doc comment at :562)
- Modify: `cmd/paisans/deployment.go` (`partialWord` :35-39, `confirmPartial` :347-352, `gatherAt` :259, warnings at :508-509, :664-665, :1022-1023, calls at :537, :669, :1027, every `gatherAt` caller)
- Modify: `cmd/paisans/operatorregister.go` (:93, :133-134, :140-142, :173)
- Modify: `cmd/paisans/config_apply.go` (:121, :149-153)
- Modify: `cmd/paisans/config_merge.go` (:86-91, and the `configured` parameter of the function holding them, `pullMergingSecrets`)
- Modify: `cmd/paisans/deploymentlocal.go:106`
- Test: `cmd/paisans/operatorregister_test.go`, `cmd/paisans/yes_test.go`, `internal/configstore/local_test.go`, `cmd/paisans/deployment_test.go`, `cmd/paisans/deploymentlocal_test.go`, `cmd/paisans/leftover_test.go`

**Interfaces:**
- Produces: `configstore.GatherAnchored(l Local, sites map[string]apply.Transport, id string, a Anchor, o GatherOptions) (Gathered, error)`; `gatherAt(r ui.Reporter, l configstore.Local, sites map[string]apply.Transport, id string, a configstore.Anchor, m gatherMode, stdin io.Reader, stdout io.Writer) (configstore.Gathered, error)`. The invite plan calls both with these signatures.

- [ ] **Step 1: Rewrite the register test to expect no prompt**

Replace `TestOperatorRegisterWithoutAMajorityAsksPartial` with:

```go
// With one site of four reached directly, register --execute signs the
// attestation and records the key, at no terminal and with nothing asked.
func TestOperatorRegisterFromOneSiteAsksNothing(t *testing.T) {
	shas, answer, files, key, pub := storedHistoryKey(t, 1, 0)
	var runs []string
	l := registerWorkstation(t, answer, files[0], &runs, pub)
	recoverHosts(t, answer) // home-a alone answers: one of four sites
	savedSign := configSignWith
	configSignWith = func(string) string { return key }
	t.Cleanup(func() { configSignWith = savedSign })
	cfgPath := filepath.Join(l.Dir, "paisans-stack.yaml")
	var out bytes.Buffer
	if err := runOperator([]string{"register", "--root", shas[0], "--config", cfgPath, "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err))
	}
	if k, _ := l.SigningKey(); k != pub {
		t.Fatalf("key %q", k)
	}
	if strings.Contains(out.String(), "partial") {
		t.Fatalf("output names partial:\n%s", out.String())
	}
}
```

Find every other test that answers `partial\n` or asserts on `"partial"` or `Majority` (`grep -rn 'partial\|Majority' --include='*_test.go' cmd internal`, excluding the unrelated hits in `internal/apply`, `internal/hostadmit`, `internal/create`, `internal/validate`, `internal/dns`, `internal/smtpcheck`, `internal/garage`, `internal/siteremove`, `internal/ui`). Change each so it runs with no answer and expects success. In `internal/configstore/local_test.go` around :338-354 and :397, delete the `g.Majority` assertions and keep the others. In `yes_test.go`, drop `partialWord` from the word list at :31 and delete the `confirmPartial` case at :63-64.

Add one publish test to `cmd/paisans/deployment_test.go` beside the existing first-version publish tests: `TestPublishFirstVersionFromOneSiteAsksNothing`. One site of three answers, `--execute`, no terminal; assert `err == nil` and that the version reached the answering site, using the existing first-publish test's helpers.

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./cmd/paisans/ -run 'OperatorRegister|Publish|Yes' ./internal/configstore/`
Expected: FAIL. The new register and publish tests are refused with `it asks for the word partial at a terminal`.

- [ ] **Step 3: Remove the field, the count and the prompt**

Delete `Gathered.Majority` and its computation. Drop the `configured int` parameter from `GatherAnchored`, `gather` and `gatherAt`, and from every caller. Delete `partialWord`, `confirmPartial`, each `!g.Majority` warning block and each `confirmPartial` call. Delete the `configured` parameter from `pullMergingSecrets` if nothing else reads it. Rewrite the comment above the first-version publish block (deployment.go :529-531) to say only what the block still does. Leave each "no site answered" refusal as it is.

- [ ] **Step 4: Build, vet, run the package tests**

Run: `go build ./... && go vet ./... && go test ./cmd/paisans/ ./internal/configstore/`
Expected: PASS. Then `grep -rn 'partialWord\|confirmPartial\|Majority' --include='*.go' cmd internal` prints nothing.

- [ ] **Step 5: Commit**

```bash
git add -A internal/configstore cmd/paisans
git commit -m "fix: go ahead on whichever sites answer, with no partial prompt"
```

### Task 2: Docs, help and changelog

**Files:**
- Modify: `cmd/paisans/help.go` (:166, :433, :520-521)
- Modify: `docs/commands.md` (:81, :562, :630, :802, :817, :1371-1372, :1431)
- Modify: `README.md` (the *A majority of sites* row at :4518 and the prose at :4595 and :4603, plus the word list at :6124)
- Modify: `docs/specs/2026-10-12-config-store.md` (:204-207, :289, :354)
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Edit each listed line**

Remove `partial` from the lists of words typed at a terminal and from what `--yes` types. Delete the README row *A majority of sites* from the table under *An old version is made visible*, and rewrite the sentence after the table so it names only the two remaining memories, the workstation and the hosts. Rewrite each command's description so it says the command goes ahead on whichever sites answer. Leave the unrelated `partial` and `majority` hits alone: README :113, :1309, :1333, :1379, :4391, :6696, :9051, :9707, :9720, :9735 and :9805 are about Synapse, etcd, backups and cuts. In the config-store spec, remove the majority bullet and the two sentences that rely on it.

- [ ] **Step 2: Add the changelog bullet**

Under `## [Unreleased]`, add a `### Removed` heading if there is none:

```
- `deployment apply`, `publish`, `pull`, `recover` and `operator register` no longer ask for `partial` when few sites answer; each goes ahead on whichever sites do.
```

- [ ] **Step 3: Verify**

Run: `grep -n "word partial\|types partial\|\`partial\`\|for partial" README.md docs/commands.md cmd/paisans/help.go docs/specs/2026-10-12-config-store.md`
Expected: no output.
Run: `go test ./cmd/paisans/ -run Help`
Expected: PASS.

- [ ] **Step 4: Commit and open the PR**

```bash
git add README.md docs CHANGELOG.md cmd/paisans/help.go
git commit -m "docs: commands go ahead on whichever sites answer"
go test ./... && gofmt -l .
git push -u origin fix/drop-majority-prompt
gh pr create --base develop --title "Go ahead on whichever sites answer" --body "<summary of the two commits>

🤖 Generated with [Claude Code](https://claude.com/claude-code)"
```
