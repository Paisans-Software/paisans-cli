# AGENTS.md

Directives for an agent working in this repository. Read this before the first
edit, not after.

`AGENTS.md` and `CLAUDE.md` are the same instructions; if both exist, this file
is the source and the other is a pointer to it.

## What this repository is

A toolkit for installing and operating a paisans community stack, plus the
design that toolkit follows. **The design leads and the code follows it.**
`README.md` is the design; `cmd/` and `internal/` implement the parts of it that
have been built: the `paisans` command, from `validate` and `render` through
`site prepare`, `deployment apply`, `site add` and the rest. `docs/development.md` lists what exists, under *Run*,
and what does not, under *What is not here yet*. The commands reach hosts over
SSH and drive Docker there, and each one that changes a host does so only with
`--execute`. The toolkit's version is the constant in `internal/version`;
*Releases* says how a release is cut and what publishes one.

That matters for scope. Much of the work here is still *documentation of a
design*, and the design is explained rather than asserted: every rule in
`README.md` says why it holds. Match that, in code as well as in prose. A
change that adds a rule without the reasoning behind it is incomplete, no
matter how correct the rule is, and so is a refusal in the code that no rule in
`README.md` justifies.

**Do not write rejected alternatives.** Guides, documentation, code comments,
commit messages and pull request descriptions describe what is built and why
it works, not the options that were weighed and turned down. No "Rejected:"
paragraph, no "X was considered and rejected", no list of alternatives. The one
exception is `docs/decisions.md`, a decision record, which may keep them.
Founder decision.

This is also **not** the `paisans.community` deployment. Generic material is
canonical here; a specific community's hosts, decision log and governance stay
with that community. See *Where documentation lives* in `README.md`.

## `docs/deployment-agent-rules.md` is not your rulebook

This file is your rulebook. `docs/deployment-agent-rules.md` is a **shipped
artifact**, and it governs a different agent: one operating a *deployed* paisans
stack, with containers to stop, member data to touch, and a live identity
provider. Its blast-radius tiers describe actions that do not exist here.

You are editing a repository. Nothing you do here reaches a running deployment,
because this repository has no deployment attached to it. Do not read its
"requires a human present" tiers as constraints on your edits, and do not
announce that you are "operating from the agent rules template". That sentence
belongs to a recovery, not to a documentation change.

You do still **maintain** that file, and edits to it carry more weight than
ordinary documentation, because the wording becomes an operational rule on
someone else's machine at the worst moment they will ever have. Three
consequences:

* **A tier change is a human decision.** Moving an action between free,
  approved, and present alters what an operator's agent is permitted to do
  during an outage. Propose it; do not decide it.
* **It must stay generic.** No hosts, no products, no community's particulars.
  It is a floor that any community can adopt and edit.
* **It is a rulebook, not a Charter.** A community's Charter is its mission,
  values and code of conduct, it lives in that community's wiki, and it governs
  these rules. Do not let values, mission statements or governance drift into
  this file; they are not generic and they are not ours to write.

## What you decide, and what the human decides

Here, in this repository:

* **You decide alone:** file layout, document structure, wording, naming,
  which runbook to write, how to organise a section.
* **The human decides:** which software fills a capability; anything changing
  the identity model or federation policy; anything that locks in a migration
  path; whether a design rule is adopted at all; the content of the deployment
  agent rules' tiers; anything published or released.

Researching two candidate applications and recommending one is your work.
Choosing between them is not.

The one hard line that is genuinely dangerous here is **publishing**: a push to
`main` or `develop`, a tag, a release, or anything leaving this repository. That
is where a mistake becomes irreversible, and it is the human's action. An
`operator-reconciler-v*` tag publishes an image as well (see *Releases*).

## What must never enter this repository

The `.gitignore` encodes this and you should understand it rather than trust it.

* **No real hostnames, addresses, or a deployment's shape.** Examples use
  `example.org`, `home-a`, `home-b`, `vm`. Publishing a live deployment's
  topology here is the leak the repository boundary exists to prevent.
* **No secrets in any form.** Not a decrypted `secrets.yaml`, not a `.env`, not
  a key, not a token, not "just an example one that is already rotated". This is
  the one mistake with no undo once pushed.
* **No member data.** Ever, under any framing.

If a change requires naming a real host to be understandable, the change is
wrong; generalise it.

## Making a change

1. **Branch.** Never commit directly to `main` or `develop`; both take pull
   requests only. Name the branch for the change: `docs/backup-contract`,
   `feat/site-add`.
2. **Read the surrounding argument first.** `README.md` is one continuous line
   of reasoning. A new rule that contradicts rule 2 four sections later is the
   most common way to damage this document.
3. **Make the change small and self-contained.** One decision per commit.
4. **Verify** (see below).
5. **Commit** in the house style (see below).
6. **Open a pull request** against `develop`, or hand the branch to the human.
   Do not merge your own work without approval.

### Documentation changes specifically

* Keep tables as tables and prose as prose; the document uses each deliberately.
* Give the reason. "X, because Y" is the minimum. Do not add the alternatives
  that were turned down (see *What this repository is*).
* Cross-reference by section name, not by line number.
* **No em dashes in anything you write.** Use a comma, a semicolon, a colon, a
  full stop, or a real parenthetical instead. Where the aside is an
  illustration, make it an explicit one (Eg: `cluster`, `pinned` or a mix of
  both). This applies to every word you produce here: documentation, commit
  messages, pull request descriptions and code comments alike.
  Existing text predates the rule and is full of them. Do not sweep it. Fix
  em dashes only in lines your change already touches for another reason.
* British/American spelling is mixed in the existing text; do not sweep it.
  Spelling normalisation is not a change worth a commit here.
* **Deleting documentation requires explicit human approval.** Rewriting a
  section in place is an edit; removing it is a deletion.

### Code changes

You are *authoring* the tool that other agents and operators will run against
live deployments. You are not running it. The care goes into what the code
permits, because the blast radius lands on someone else's stack.

`deployment apply` has run against a real host; `site add` and `site remove` have not, and
*What is not here yet* in `docs/development.md` says what else is reasoned
rather than observed. All of it holds:

* **A pinned stack lays out under `/srv/paisans/<token>/<stack>/` with bind mounts**, not named
  volumes. `README.md` explains why (`app move` becomes one `tar`).
* **`.env` files are build artifacts.** Nothing hand-edits them; `deployment apply` renders
  them and refuses to clobber local modifications.
* **`sops`/`age` run on the workstation only.** No code path may put a
  decryption key on a host, and no code path may decrypt on a host, unless the
  human has explicitly chosen that mode.
* **Anything that mutates a live deployment is staged and gated**, and every
  gate stops on failure rather than warning and continuing. `site add` is the
  worked example: the stage-2 handshake gate exists because stage 3 can take a
  healthy cluster down.
* **Destructive and topology-changing operations are idempotent and
  resumable.** Re-running after a fixed network problem must resume.
* **Refuse rather than warn** where the configuration is incoherent (a witness
  sharing a failure domain with its only voter). **Warn rather than refuse**
  where it is merely risky (a pinned app on the witness host). Do not blur these.

## Verifying before you claim it is done

`go vet ./...` and `go test ./...` are the test suite, and *Tests* in
`docs/development.md` describes it, including the Docker tests behind build
tags. Documentation has no test, so its verification is manual and you must
actually do it, not assert it:

* `git diff` the full change and read it.
* Every internal link resolves: check `docs/` and `examples/` paths exist.
* Every fenced YAML block parses. Every command example uses flags the command
  accepts, which `cmd/paisans` defines.
* No real hostname, address, key or secret appears in the diff. Grep the diff,
  do not eyeball it.
* Claims about upstream software (S3 support, environment variable names,
  known bugs) are cited or verified. `README.md` makes specific factual claims
  about Mbin, Wiki.js, Synapse, Pocket ID and WriteFreely. Do not add one from
  memory.

For a code change, add: it builds; `go vet ./...` and `go test ./...` pass; the
test you wrote fails before your change and passes after; a change to what is
rendered regenerates the golden tree, and you read its diff; and the Docker
tests run when the change touches what they cover.

**Report what you actually ran.** If a check was skipped, say which and why.

## Commits

Format follows the existing log exactly. Read `git log` before writing one.

```
<type>: <subject, lowercase, imperative, no trailing period>

<body: what changed and, more importantly, why. Do not list alternatives that
were turned down. Wrap at 80 columns. Attribute the decision when a human made it
(Eg: "Founder decision." or "Founder direction."), so the log records who
chose.>

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
```

Types in use: `docs`, `feat`, `chore`. Add `fix` and `refactor` when there is
something to fix or refactor.

The bodies here are long on purpose. This log is a decision record; a subject
line alone throws away the part that will be needed in a year.

Never commit without the human asking, and never `push` or `git commit --amend`
on a shared branch unasked.

## Pull requests

* One decision per PR, same as commits.
* The description explains the reasoning; the diff shows the change.
* End the description with:

  ```
  🤖 Generated with [Claude Code](https://claude.com/claude-code)
  ```
* Do not merge your own PR without approval.

## Releases

**Versions are semantic, `MAJOR.MINOR.PATCH`, and written in one place**: the
`Version` constant in `internal/version/version.go`. `paisans version` prints
it, a blueprint's `requires_toolkit` is checked against it, and a release is
tagged `vMAJOR.MINOR.PATCH` from it. Between releases `develop` holds the next
release's `-dev` (Eg: `0.3.0-dev` after `v0.2.0`), which orders after the last
release and below the next one's pre-releases: a build of `develop` never
reports a release it is not. A pre-release, `make bump VERSION=0.3.0-rc.1`,
is released like a release, as a GitHub pre-release that never becomes the
latest; `0.3.0-dev < 0.3.0-rc.1 < 0.3.0-rc.2 < 0.3.0`, as semantic versioning
orders them. Every record the toolkit writes carries the build
that wrote it, `version.Build`: the version, the commit and whether the tree
was modified. `0.x` holds while command names remain
provisional. The operator reconciler, which the `pocket-id` blueprint runs,
is released from its own repository,
`Paisans-Software/paisans-operator-reconciler-service`; the core catalog's
`foundation.yaml` names the track the blueprint runs.

**Every pull request into `develop` records its change in `CHANGELOG.md`**, one
bullet under `[Unreleased]` in the Keep a Changelog category that fits, one
sentence of at most 35 words. `.github/workflows/changelog.yml` checks it. A
pull request with nothing an operator would notice opts out by starting its
head commit's message with `kacl-skip:`.

**A release is cut in four steps**; the first and the last are the only ones ever yours:

1. `make bump-patch`, `make bump-minor`, `make bump-major` or
   `make bump VERSION=x.y.z` (or `x.y.z-rc.N`) on a branch. The bump targets
   count from the newest final release tag. It sets the constant, moves the
   `[Unreleased]` bullets into a dated section for that version, and commits
   both as `kacl-skip: release x.y.z`. It refuses a dirty tree, a version
   already tagged and an empty `[Unreleased]`, except for the release of a
   pre-release cut with every change, whose section then says so. The branch goes to `develop`
   as a pull request titled `Release x.y.z`.
2. A pull request from `develop` into `main`, titled `Release x.y.z to main`.
   Its body is the operator notes at the top of the GitHub Release: anything
   an operator has to *do*, such as an `deployment apply`, a configuration edit, or a
   change to an existing deployment's data or topology. A release that changes
   an existing deployment says so first, with what to do about it, because
   this toolkit's design premise is that additions must not become migrations.
3. The founder merges that pull request **with a merge commit, never a squash
   or a rebase**, which keeps `main` an ancestor of `develop`.
   `.github/workflows/release.yml` sees a version on `main` with no tag, tags
   it and publishes the GitHub Release (a pre-release for an `-rc.N`), with the
   changes since the previous final release listed under the operator notes and the Linux binaries and
   `SHA256SUMS` that `make release-binaries` builds attached.
4. Once the release is tagged, `make dev` on a branch from `develop` moves the
   constant to the next minor's `-dev`, or, after a pre-release whose release
   is not cut yet, back to that release's `-dev` (`make dev VERSION=x.y.z` for
   another),
   committed as `kacl-skip: develop is x.y.z-dev`, as a pull request into
   `develop`. `release.yml` refuses a `-dev` version that reaches `main`.

**If a release step fails after the tag is pushed**, a re-run releases nothing,
because the tag exists. A human recovers it: delete the draft release if there
is one, then the tag (`git push origin :refs/tags/vX.Y.Z`), then re-run the
job. The comment above the tag step in `release.yml` says the same. A failure
before the tag step leaves nothing behind and needs only the re-run.

**Release is a human action.** Tagging and publishing leave this repository
and cannot be walked back. An agent may run `make bump` or `make dev` and open the
`Release x.y.z` pull request into `develop` when asked; it never merges into
`main`, pushes a tag, or publishes a release.

## When you are unsure

Ask, and keep working on the part that does not depend on the answer.

The failure mode to avoid here is not recklessness with a deployment, since you
have no deployment to be reckless with. It is **writing something into this
repository that reads as settled when it was your inference**: a design rule the
founder never adopted, a factual claim about upstream software taken from
memory, or a blast-radius tier you moved because it seemed reasonable. Those
travel.
Mark what is provisional as provisional.
