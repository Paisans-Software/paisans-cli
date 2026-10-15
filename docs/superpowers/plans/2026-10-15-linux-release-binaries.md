# Rootless Phase 8: Linux Release Binaries Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every GitHub Release of the toolkit carries static `linux/amd64` and `linux/arm64` binaries of `paisans` and a `SHA256SUMS` file, built by a `make release-binaries` target that a contributor can run locally.

**Architecture:** A new Makefile target cross-compiles `./cmd/paisans` for both Linux targets with `CGO_ENABLED=0` into `dist/` and writes `SHA256SUMS` there with bare file names. `.github/workflows/release.yml` sets up Go, runs that target before it tags anything, checks the amd64 binary reports this release's version and commit, then creates the release as a draft, attaches the three files by release id, and publishes the draft. Everything the workflow already does (tag, changelog check, operator notes, labels, generated notes) is kept unchanged.

**Tech Stack:** GNU Make, the Go toolchain's cross-compiler, `shasum -a 256`, GitHub Actions (`actions/setup-go`), the `gh api` REST calls the workflow already uses.

**Spec:** `docs/specs/2026-10-15-rootless-and-host-admit.md` (sections *Settled*, *Linux binaries*, and the acceptance line "The release workflow's artifacts include both Linux binaries and `SHA256SUMS`"), and the phase index `docs/superpowers/plans/2026-10-15-rootless-and-host-admit.md`, *Phase 8*. Both live on the `host-admit` branch, at `/Users/wash/Developer/paisans.community/src/paisans-stack/.worktrees/host-admit/docs/`.

## Global Constraints

* "An owner on a host needs the CLI and nothing else: not Go, not Docker, not Homebrew."
* "The release workflow attaches `linux/amd64` and `linux/arm64` binaries and a `SHA256SUMS` file to each release."
* Both binaries are built with `CGO_ENABLED=0`.
* Everything `release.yml` does today keeps working: the tag, the changelog check, the operator notes from the merged pull request body, the `release` label, and the generated notes since the previous `v*` tag.
* The Homebrew tap (`Paisans-Software/homebrew-paisans`, `Formula/paisans-cli.rb`) is HEAD-only and builds from source with `go build`; nothing in this plan may change what that build needs (`./cmd/paisans`, `go.mod`).
* No step of this plan pushes, tags, creates a release, or runs on a remote host. Those are listed under *Verification after merge* for the human.
* No em dashes in anything written: Makefile, workflow, docs, commit messages.
* Docs describe current state only: no "no longer", "used to" or history wording outside `CHANGELOG.md`; no rejected alternatives anywhere.
* Commits: `<type>: <lowercase subject>`, a body saying why, and the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
* `CHANGELOG.md`: one bullet under `[Unreleased]`, in its Keep a Changelog category, one sentence, at most 35 words.
* Never `git stash`; other sessions share the stash. Never `git push`.

## Review Focus

* A release binary built while `dist/` already holds the first binary: Go stamps `vcs.modified=true` when the tree has untracked, unignored files, so without `/dist/` in `.gitignore` the arm64 binary (built second) reports `-dirty`. Observed on this branch before planning. Expect `vcs.modified=false` on both (Task 1 Step 6) and the workflow to refuse a `-dirty` amd64 binary (Task 2 Step 3).
* A host owner who downloads one binary and `SHA256SUMS`, then runs `sha256sum -c`: the check must name bare files and pass with `--ignore-missing` for the one file present (Task 1 Step 5).
* A binary altered after download: `sha256sum -c` must report `FAILED` and exit non-zero (Task 1 Step 5).
* A compile failure during a release run: no tag and no release may be left behind, so the build runs before `Create and push the tag` (Task 2 Step 3, step order check in Task 2 Step 5).
* A run of `make release-binaries` over a `dist/` holding stale files from an earlier run: `SHA256SUMS` lists only the two binaries just built (Task 1 Step 5).

---

## File Structure

| File | Change | Responsibility after the change |
|---|---|---|
| `Makefile` | modify | adds `release-binaries`: builds `dist/paisans-linux-amd64`, `dist/paisans-linux-arm64`, `dist/SHA256SUMS` |
| `.gitignore` | modify | ignores `/dist/` |
| `.github/workflows/release.yml` | modify | sets up Go, builds and checks the binaries before tagging, creates a draft, attaches the files, publishes |
| `docs/development.md` | modify | *Build* names `make release-binaries` |
| `AGENTS.md` | modify | *Releases* step 3 says the release carries the Linux binaries and `SHA256SUMS` |
| `CHANGELOG.md` | modify | one `### Added` bullet |

No Go source changes. No README or guide install section describes downloads today; the host owners' guide that gives the download and the check is phase 9 (`docs/guides/host-owners.md`), so this plan adds none.

---

### Task 1: `make release-binaries` builds both Linux binaries and `SHA256SUMS`

**Files:**
- Modify: `Makefile` (the `.PHONY` line, and a new target after `bump-patch`)
- Modify: `.gitignore` (after the `/bin/` line at the end)
- Modify: `docs/development.md` (*Build*, the paragraph starting "Go 1.25.8 or newer")

**Interfaces:**
- Consumes: nothing.
- Produces: `make release-binaries`, which deletes and recreates `dist/` and writes exactly `dist/paisans-linux-amd64`, `dist/paisans-linux-arm64` and `dist/SHA256SUMS`. `SHA256SUMS` is `shasum -a 256` output (`<64 hex>  <name>` per line) with bare file names. Task 2's workflow runs this target and reads those three paths.

- [ ] **Step 1: Run the target to see it fail**

Run: `make release-binaries`
Expected: FAIL with `make: *** No rule to make target 'release-binaries'.  Stop.`

- [ ] **Step 2: Ignore `dist/`**

Append to `.gitignore`, after the `/bin/` line:

```gitignore

# make release-binaries writes the release's Linux binaries and SHA256SUMS
# here. Ignoring it is load-bearing: Go stamps a binary -dirty when the tree
# holds untracked files, so an unignored dist/ marks every binary after the
# first one built.
/dist/
```

- [ ] **Step 3: Add the target**

In `Makefile`, change the `.PHONY` line to:

```make
.PHONY: bump bump-major bump-minor bump-patch release-binaries
```

Append at the end of `Makefile`, after the `bump-patch` recipe:

```make

# The Linux binaries a release attaches, with a SHA256SUMS file beside them,
# in dist/. A host owner runs `paisans host admit` on the host itself and
# needs the CLI and nothing else, so each binary is static (CGO_ENABLED=0)
# and runs on any Linux of its architecture with no Go and no C library.
# .github/workflows/release.yml runs this target, so a local run builds what
# a release ships.
#
# dist/ is emptied first, so SHA256SUMS lists only what this run built.
# SHA256SUMS names the files without a directory, so `sha256sum -c` works in
# whatever directory they were downloaded to. -trimpath keeps the builder's
# paths out of the binary; -s -w drops the symbol table and DWARF, which a
# release binary has no use for.
RELEASE_PLATFORMS := linux/amd64 linux/arm64

release-binaries:
	rm -rf dist
	mkdir -p dist
	@for p in $(RELEASE_PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "building dist/paisans-$$os-$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="-s -w" \
			-o dist/paisans-$$os-$$arch ./cmd/paisans || exit 1; \
	done
	cd dist && shasum -a 256 paisans-* > SHA256SUMS
	@cat dist/SHA256SUMS
```

Recipe lines start with a tab, not spaces. `rm -rf dist` names a fixed relative path on purpose: no variable can turn it into another directory.

- [ ] **Step 4: Run the target and check the outputs**

Run: `make release-binaries && ls dist && file dist/paisans-linux-*`
Expected: `ls` prints exactly `SHA256SUMS`, `paisans-linux-amd64`, `paisans-linux-arm64`. `file` prints, for amd64, `ELF 64-bit LSB executable, x86-64, ... statically linked, ... stripped`, and for arm64, `ELF 64-bit LSB executable, ARM aarch64, ... statically linked, ... stripped`.

Run: `go version -m dist/paisans-linux-arm64 | grep -E 'CGO_ENABLED|GOOS|GOARCH'`
Expected: `CGO_ENABLED=0`, `GOOS=linux`, `GOARCH=arm64`. Repeat for `dist/paisans-linux-amd64`, expecting `GOARCH=amd64`.

Run: `git status --short`
Expected: `.gitignore`, `Makefile` modified; nothing under `dist/`.

- [ ] **Step 5: Check the checksums the way a host owner will**

Run: `(cd dist && shasum -a 256 -c SHA256SUMS)`
Expected: `paisans-linux-amd64: OK` and `paisans-linux-arm64: OK`, exit 0. Where `sha256sum` exists (Linux, recent macOS), `(cd dist && sha256sum -c SHA256SUMS)` prints the same.

One binary downloaded alone, as an owner on an arm64 host would:

```sh
S=$(mktemp -d)
cp dist/paisans-linux-arm64 dist/SHA256SUMS "$S"
(cd "$S" && shasum -a 256 -c --ignore-missing SHA256SUMS); echo "rc=$?"
```

Expected: `paisans-linux-arm64: OK`, `rc=0`.

An altered binary:

```sh
printf 'x' >> "$S/paisans-linux-arm64"
(cd "$S" && shasum -a 256 -c --ignore-missing SHA256SUMS); echo "rc=$?"
rm -rf "$S"
```

Expected: `paisans-linux-arm64: FAILED`, a warning that 1 computed checksum did NOT match, `rc=1`.

A stale file from an earlier run:

```sh
touch dist/paisans-linux-386
make release-binaries >/dev/null
ls dist; wc -l < dist/SHA256SUMS
```

Expected: `ls` shows only the three files (no `paisans-linux-386`), and `SHA256SUMS` has 2 lines.

- [ ] **Step 6: Update *Build* in `docs/development.md`**

Replace:

```markdown
Go 1.25.8 or newer, the `go` line in `go.mod`. There is no code generation step.
The `Makefile` holds only the release targets (`make bump` and its
`bump-patch`, `bump-minor` and `bump-major` forms), which set the version
constant in `internal/version` and cut `CHANGELOG.md`; *Releases* in
`AGENTS.md` describes the flow.
```

with:

```markdown
Go 1.25.8 or newer, the `go` line in `go.mod`. There is no code generation step.
The `Makefile` holds only the release targets. `make bump` and its
`bump-patch`, `bump-minor` and `bump-major` forms set the version constant in
`internal/version` and cut `CHANGELOG.md`; *Releases* in `AGENTS.md` describes
the flow. `make release-binaries` builds the Linux binaries a release
attaches, `paisans-linux-amd64` and `paisans-linux-arm64`, static with
`CGO_ENABLED=0`, into `dist/` with a `SHA256SUMS` file beside them. The
release workflow runs the same target, so a local run builds what a release
ships; `dist/` is ignored, because Go marks a binary `-dirty` when the tree
holds untracked files.
```

- [ ] **Step 7: Commit, then confirm a committed tree builds clean binaries**

```bash
git add Makefile .gitignore docs/development.md
git commit -F - <<'EOF'
feat: make release-binaries builds the linux binaries a release attaches

A host owner admits a deployment by running paisans on the host itself,
with no Go and no Homebrew there, so a release has to carry Linux binaries.
The build is a Makefile target so the release workflow and a contributor
run the same command, and a local run shows what a release ships. Both
targets are static (CGO_ENABLED=0), and SHA256SUMS names bare files so an
owner can check a download where it landed.

dist/ is ignored because Go stamps a binary -dirty when the tree holds
untracked files, which would mark every binary after the first.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

Then, with the tree clean:

Run: `make release-binaries >/dev/null && go version -m dist/paisans-linux-arm64 | grep vcs.modified && go version -m dist/paisans-linux-amd64 | grep vcs.modified`
Expected: `vcs.modified=false` twice. The arm64 binary is built second, with the amd64 one already in `dist/`, so it is the one that would read `true` if `/dist/` were not ignored.

Run: `go version -m dist/paisans-linux-amd64 | grep vcs.revision` and `git rev-parse HEAD`
Expected: the same commit.

---

### Task 2: `release.yml` attaches the binaries and `SHA256SUMS` to each release

**Files:**
- Modify: `.github/workflows/release.yml` (header comment; new steps after `Require the changelog section`; the `Publish the GitHub Release` step replaced by three steps)
- Modify: `AGENTS.md` (*Releases*, step 3)
- Modify: `CHANGELOG.md` (`[Unreleased]`, `### Added`)

**Interfaces:**
- Consumes: `make release-binaries` and its outputs `dist/paisans-linux-amd64`, `dist/paisans-linux-arm64`, `dist/SHA256SUMS` (Task 1). `steps.decide.outputs.release`, `.version`, `.tag`, `.previous` and `steps.pr.outputs.body`, which exist today.
- Produces: a new step output `steps.release.outputs.id`, the draft release's numeric id, read by the attach and publish steps.

- [ ] **Step 1: Confirm the workflow lints clean before the change**

Run: `actionlint .github/workflows/release.yml; echo "rc=$?"`
Expected: no findings, `rc=0`. If `actionlint` is absent, install it (`brew install actionlint`) or record in the hand-off that it was not run.

- [ ] **Step 2: Update the header comment**

In `.github/workflows/release.yml`, replace:

```yaml
# A push to main whose version has no tag is a release: this tags it vX.Y.Z
# and publishes the GitHub Release. Any other push to main is a no-op. The
```

with:

```yaml
# A push to main whose version has no tag is a release: this tags it vX.Y.Z
# and publishes the GitHub Release, with the static Linux binaries and their
# SHA256SUMS that `make release-binaries` builds attached to it. A host owner
# downloads one of those to run `paisans host admit` on the host itself.
# Any other push to main is a no-op. The
```

- [ ] **Step 3: Build and check the binaries before the tag**

Insert these three steps immediately after the `Require the changelog section` step and before `Create and push the tag`:

```yaml
      # The binaries are built before anything is tagged, so a build that
      # fails leaves no tag and no release behind. make release-binaries is
      # the command a contributor runs locally; the Makefile says why each
      # flag is there.
      - name: Set up Go
        if: steps.decide.outputs.release == 'true'
        uses: actions/setup-go@v6
        with:
          go-version-file: go.mod

      - name: Build the Linux binaries
        if: steps.decide.outputs.release == 'true'
        run: make release-binaries

      # The runner is linux/amd64, so the amd64 binary runs here. It has to
      # report this release's version and this exact commit, without -dirty:
      # anything else means it is not a build of what is being tagged.
      - name: Check the binaries
        if: steps.decide.outputs.release == 'true'
        env:
          VERSION: ${{ steps.decide.outputs.version }}
        run: |
          set -euo pipefail
          got=$(dist/paisans-linux-amd64 version)
          want="paisans ${VERSION} (${GITHUB_SHA:0:12})"
          if [ "$got" != "$want" ]; then
            echo "::error::dist/paisans-linux-amd64 version printed '${got}', expected '${want}'"
            exit 1
          fi
          (cd dist && sha256sum -c SHA256SUMS)
```

`paisans version` prints `paisans <Version> (<12-character commit>)`, with `-dirty` appended to the commit for a modified tree (*`paisans version`* in `docs/commands.md`). `actions/checkout` checks out `GITHUB_SHA`, so the expected commit is that one.

- [ ] **Step 4: Replace the publish step with draft, attach, publish**

Replace the whole `Publish the GitHub Release` step (from `- name: Publish the GitHub Release` to the end of the file) with:

```yaml
      # The release is created as a draft, given its binaries, and only then
      # published, so nobody sees a release that is missing them. The REST
      # API takes a hand-written body and the generated list at once,
      # appending the second under the first.
      - name: Create the draft release
        id: release
        if: steps.decide.outputs.release == 'true'
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          TAG: ${{ steps.decide.outputs.tag }}
          PREVIOUS: ${{ steps.decide.outputs.previous }}
          BODY: ${{ steps.pr.outputs.body }}
        run: |
          set -euo pipefail
          id=$(gh api "repos/${GITHUB_REPOSITORY}/releases" \
            -f "tag_name=${TAG}" \
            -f "name=${TAG}" \
            -f "body=${BODY}" \
            -F draft=true \
            -F generate_release_notes=true \
            ${PREVIOUS:+-f "previous_tag_name=${PREVIOUS}"} \
            --jq .id)
          echo "id=${id}" >> "$GITHUB_OUTPUT"

      # Uploaded by release id, because the API's lookup of a release by its
      # tag does not return a draft.
      - name: Attach the Linux binaries
        if: steps.decide.outputs.release == 'true'
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          ID: ${{ steps.release.outputs.id }}
        run: |
          set -euo pipefail
          for name in paisans-linux-amd64 paisans-linux-arm64 SHA256SUMS; do
            gh api --method POST \
              -H "Content-Type: application/octet-stream" \
              "https://uploads.github.com/repos/${GITHUB_REPOSITORY}/releases/${ID}/assets?name=${name}" \
              --input "dist/${name}" >/dev/null
          done

      - name: Publish the release
        if: steps.decide.outputs.release == 'true'
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          ID: ${{ steps.release.outputs.id }}
        run: |
          gh api --method PATCH "repos/${GITHUB_REPOSITORY}/releases/${ID}" \
            -F draft=false >/dev/null
```

The `${PREVIOUS:+...}` expansion stays unquoted, as it is today, so an empty `PREVIOUS` adds no argument. `gh api` accepts a full URL as its endpoint (checked with `gh api https://api.github.com/rate_limit` while planning) and sends the job's token to `uploads.github.com`.

- [ ] **Step 5: Lint and check the step order**

Run: `actionlint .github/workflows/release.yml; echo "rc=$?"`
Expected: no findings, `rc=0`. A shellcheck finding inside a `run:` block is fixed, not suppressed.

Run: `grep -n '      - name:' .github/workflows/release.yml`
Expected, in this order: `Checkout repository`, `Decide whether this is a release`, `Set the commit identity`, `Require the changelog section`, `Set up Go`, `Build the Linux binaries`, `Check the binaries`, `Create and push the tag`, `Read the merged pull request body`, `Label the release pull requests`, `Create the draft release`, `Attach the Linux binaries`, `Publish the release`.

Run: `grep -c "if: steps.decide.outputs.release == 'true'" .github/workflows/release.yml`
Expected: `11` (every step after `Decide whether this is a release` is gated: the six gated today, plus `Set up Go`, `Build the Linux binaries`, `Check the binaries`, and `Publish the GitHub Release` split into three).

Run: `make release-binaries >/dev/null && (cd dist && shasum -a 256 -c SHA256SUMS)`
Expected: both `OK`. This is the workflow's build and check, minus running the Linux binary, which a macOS workstation cannot.

- [ ] **Step 6: Update *Releases* step 3 in `AGENTS.md`**

Replace:

```markdown
   `.github/workflows/release.yml` sees a version on `main` with no tag, tags
   it and publishes the GitHub Release, with the changes since the previous
   release listed under the operator notes.
```

with:

```markdown
   `.github/workflows/release.yml` sees a version on `main` with no tag, tags
   it and publishes the GitHub Release, with the changes since the previous
   release listed under the operator notes and the Linux binaries and
   `SHA256SUMS` that `make release-binaries` builds attached.
```

- [ ] **Step 7: Add the changelog bullet**

In `CHANGELOG.md`, under `## [Unreleased]`, `### Added`, append after the existing `host prepare --all` bullet:

```markdown
- Each release attaches static `paisans-linux-amd64` and `paisans-linux-arm64` binaries and a `SHA256SUMS` file, so a host owner can run the CLI on Linux without Go.
```

That is one sentence of 24 words.

Run: `uvx --from python-kacl==0.7.0 kacl-cli verify`
Expected: `CHANGELOG.md` valid. If `uvx` is absent, record that the check was not run.

- [ ] **Step 8: Commit**

```bash
git add .github/workflows/release.yml AGENTS.md CHANGELOG.md
git commit -F - <<'EOF'
feat: releases attach the linux binaries and their sha256sums

The release workflow runs make release-binaries before it tags, so a
build that fails leaves no tag behind, and checks that the amd64 binary
reports this release's version and commit without -dirty. It creates the
release as a draft, attaches both binaries and SHA256SUMS by release id,
and then publishes it, so no one sees a release that is missing them. The
tag, the changelog check, the operator notes, the labels and the generated
notes are unchanged.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

---

### Task 3: Verify the branch

**Files:** none changed unless a check fails; a fix goes in its own commit in the house style.

- [ ] **Step 1: Build, vet and test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: every package `ok` or `[no test files]`. No Go source changed; this confirms the Homebrew tap's HEAD build (`go build ./cmd/paisans`) still has what it needs. The Docker tests behind build tags are not run: nothing here touches what they cover; say so in the hand-off.

- [ ] **Step 2: Lint the workflows**

Run: `actionlint; echo "rc=$?"`
Expected: no findings across `.github/workflows/`, `rc=0`.

- [ ] **Step 3: Rebuild from a clean tree**

Run: `git status --short && make release-binaries && file dist/paisans-linux-* && (cd dist && shasum -a 256 -c SHA256SUMS) && go version -m dist/paisans-linux-arm64 | grep vcs.modified`
Expected: `git status` prints nothing; both binaries `statically linked`; both checksums `OK`; `vcs.modified=false`.

- [ ] **Step 4: Check the diff for em dashes, history wording and addresses**

Run: `git diff origin/develop...HEAD | perl -CSD -ne 'print "$.: $_" if /\x{2014}/'`
Expected: no output.

Run: `git diff origin/develop...HEAD -- ':!CHANGELOG.md' ':!docs/superpowers' | grep '^+' | grep -in 'no longer\|used to\|was removed\|instead of'`
Expected: no output.

Run: `git diff origin/develop...HEAD | perl -ne 'print if /^\+/ && /\b(\d{1,3}\.){3}\d{1,3}\b/'`
Expected: no output.

- [ ] **Step 5: Read the whole diff**

Run: `git diff origin/develop...HEAD --stat` and then `git diff origin/develop...HEAD`
Expected: only `Makefile`, `.gitignore`, `.github/workflows/release.yml`, `docs/development.md`, `AGENTS.md`, `CHANGELOG.md` and this plan. Read every hunk.

---

## Verification after merge

These publish or reach a remote machine, so they are the human's, never part of executing this plan.

* **A test release in a fork.** In a personal fork of `Paisans-Software/paisans-cli`, run `make bump-patch` on a branch, merge it to the fork's `main` with a merge commit, and watch `release`. Expect the tag, a release with the operator notes and generated notes, and three assets: `paisans-linux-amd64`, `paisans-linux-arm64`, `SHA256SUMS`. Confirm the release was never visible as published without them. Delete the fork's tag and release afterwards.
* **The download check.** On any Linux machine, download one binary and `SHA256SUMS` from that release and run `sha256sum -c --ignore-missing SHA256SUMS`. Expect `OK`.
* **arm64.** On an arm64 Linux VM, `chmod +x paisans-linux-arm64 && ./paisans-linux-arm64 version`. Expect `paisans <version> (<12-character commit>)` with no `-dirty`.
* **A run that fails after the tag is pushed.** The decide step treats an existing tag as already released, so re-running is a no-op. Recovery is to delete the draft release and the tag, then re-run the job. The build runs before the tag, so a compile failure never reaches this.
* **The Homebrew tap.** `Formula/paisans-cli.rb` is HEAD-only and is unaffected. When it gains a stable block, the Linux assets are bare binaries, not archives, so it installs one with `bin.install "paisans-linux-arm64" => "paisans"` (and the amd64 name under `on_intel`) and takes each `sha256` from `SHA256SUMS`. The release carries no macOS binaries, so the tap's `on_macos` block keeps building from source until it does. Updating the tap's README "When the first release ships" to say so is a change in that repository.

---

## Self-Review

**Spec coverage.** *Linux binaries*: `linux/amd64` and `linux/arm64` with `CGO_ENABLED=0` (Task 1 Step 3), attached with `SHA256SUMS` to each release (Task 2 Step 4). *Settled*, "the owner needs the CLI and nothing else": static binaries, checked with `file` (Task 1 Step 4). The acceptance line "The release workflow's artifacts include both Linux binaries and `SHA256SUMS`" is checked by the fork release under *Verification after merge*, since no step here may publish. "The guide for host owners gives the download and the check" is phase 9; Task 1 Step 5 exercises the exact check that guide will give. Phase index: "verify on a test tag in a fork, and that the arm64 binary runs `paisans version` on an arm64 VM" are both under *Verification after merge*.

**Placeholders.** None: every edit carries its text, every run its command and expected result.

**Name consistency.** `release-binaries`, `dist/`, `paisans-linux-amd64`, `paisans-linux-arm64` and `SHA256SUMS` are spelled the same in the Makefile, `.gitignore`, the workflow's build, check and attach steps, `docs/development.md`, `AGENTS.md` and `CHANGELOG.md`. `steps.release.outputs.id` is written by `Create the draft release` (`id: release`) and read by the two steps after it.

**Review Focus.** Each line has its check: the `-dirty` stamp (Task 1 Step 7, Task 2 Step 3, Task 3 Step 3), one binary with `--ignore-missing` and an altered binary (Task 1 Step 5), a failed build leaving no tag (Task 2 Steps 3 and 5), stale files in `dist/` (Task 1 Step 5).
