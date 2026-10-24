# One system copy per deployment, drafts in folders

Status: approved by the founder on 2026-10-24; not yet implemented.

A workstation keeps one copy of each deployment it runs, in the Paisans data
directory: the **system copy**. It is the only state Paisans trusts on the
workstation. A folder the operator works in holds a **draft** of the
deployment's three files and nothing else. Every command that writes files
writes the draft; the system copy changes only when a draft is deployed or a
newer version is synced in from the sites.

## Intent

Today every folder holding a deployment's files is a workstation of its own,
with a `.paisans/` directory beside the files: the local store of signed
versions, the pinned root, the head, the newest revision seen, the recorded
signing key, and `incoming/` for merges. A second folder for the same
deployment is a second workstation with its own trust state, which can sit at
another version and judge the sites differently. An operator who is not
technical cannot tell which folder is the real one, and the invite flow had to
choose which folder an answer and a join belonged to.

One system copy per deployment removes that question. The operator edits where
they like; Paisans knows the deployment's state in one place.

## Goals

* One trusted copy of each deployment per workstation, found by deployment id.
* A folder is a draft: plain files with no state beside them. The draft's own
  `paisans-stack.yaml` carries one comment line naming the version it was
  drafted from, so every draft can be merged forward.
* Every command that writes the deployment's files writes the draft, and
  starts one from the system copy when the folder has none.
* The system copy changes only on deploy or on a sync from the sites.
* A draft behind the sites is merged forward before it is deployed, never
  deployed over a newer version.
* The review shows what a draft changes against the system copy, and flags
  changes that look like undoing a newer version.

## Non-goals

* Changing the three trust rules of the config store
  (`docs/specs/2026-10-12-config-store.md`), the signed version format, or
  what a site stores.
* Sharing a system copy between workstations or operators. Each workstation
  syncs its own from the sites.
* Converting existing folders or `.paisans/` directories. Nothing is released;
  deployments are redeployed.

## Anti-goals

* Any trust decision read from a folder. A draft is never an anchor, a root or
  a record of what was seen.
* A deploy that silently replaces a newer version on the sites.
* A command that changes the system copy without a deploy or a sync.
* Secret values printed in a review or a merge.

## The system copy

One directory per deployment id under the data directory
(`localstore.Base()`: Application Support/Paisans on macOS, the XDG data home
elsewhere, `PAISANS_DATA_DIR` over either), the saved copies `deployment list`
shows today. It holds:

* the three files of the version this workstation last deployed or synced;
* the store of every signed version gathered from the sites;
* the trust state: the pinned root, the recorded signing key, the newest
  revision seen and, for each revision, **when it reached this workstation**;
* beside it under the same base, as the invite flow keeps them: the
  operator's age-key binding (`age/<id>.recipient`) and any pending invite
  (`invites/<id>/`).

It is written only by:

* **a deploy**: `deployment apply --execute`, and `deployment publish
  --execute`, since after either the sites hold the version. The deployed
  files become the system copy's files, and the version joins its store;
* **a sync**: any command that gathers from the sites records what they hold
  in the system copy's store and when each version arrived. Only a deploy and
  `deployment pull` move the system copy's files to a newer trusted head;
  every other sync (`log`, `register`, a dry run) records versions and
  nothing else, so the files stay the base the next merge needs;
* **a recovery or a join**, which create the system copy from the sites.

The system copy has a lock, held only while a command writes it: two writers
at once is refused, the second naming the first. A deploy that another
deploy overtook between its sync and its publish is refused at publish by the
existing check that the version's parent is the sites' head, and says to run
apply again.

A deployment with no system copy on this workstation gets one only from a
recovery, a join, or the first deploy of a new deployment (`init` or `create`
made it, and no site holds a version yet). A draft of an existing deployment
whose system copy is missing is refused, naming `deployment recover` or the
join; no other command anchors trust on its own.

## Drafts

A draft is `paisans-stack.yaml`, `secrets.enc.yaml` and `.sops.yaml` in a
folder, always handled as a set. Nothing else in the folder belongs to
Paisans; no `.paisans/` directory is written there.

* **Which deployment.** A draft names its deployment by the `id` in its
  `paisans-stack.yaml`. In a folder with no draft, a command uses the only
  deployment on the workstation, or the one `--id` names when there are
  several.
* **Starting a draft.** A command that writes files, run in a folder with no
  draft, first copies the system copy's three files into the folder, then
  makes its change there. `deployment checkout` is that step alone.
* **Commands that write files** write the draft only: `init`, `create`,
  `operator add` (with or without `--request`), `secrets rotate`,
  `storage rotate-key`, `oidc client create`, `site remove`, `mesh rekey`,
  signup group edits, and every other writer of the three files. Hand edits
  are drafts too.
* **Read-only commands** (`validate`, `render`, `doctor`, `secrets list`,
  `deployment log`, `operator invite`) read the draft where there is one, and
  the system copy where there is none. Commands whose answer is about trust
  (`log`, `invite`'s root) always read the system copy.
* A multi-step change, such as a key rotation applied in turns, stays a draft
  between its steps; each step's apply deploys that step.

### The "drafted from" line

The first line of a draft's `paisans-stack.yaml` names the version it was
drafted from:

```yaml
# paisans: drafted from revision 10 (3f9c2a1e…)
```

with the version's full hash in the parentheses. Paisans writes or replaces
it whenever it puts files into a folder: when it starts a draft from the
system copy, when it merges a newer version into the draft, and when the
draft is deployed (the line then names the version just deployed). It is a
YAML comment, so the configuration does not read it.

It decides only the merge base. A deploy reads it and uses that version as
the base, provided the hash names a version in the system copy's trusted
history. A line that is missing, garbled, or names a version the history does
not hold is ignored, and the base is the system copy's files instead; the
review says so: `This draft has no "drafted from" line; it is taken as
drafted from revision 15, the system copy's.` A draft without the line is
never refused: a draft written by hand, or the first draft of a new
deployment, has none. Safety never rests on the line: the review diff, the
undo flags and the stale-draft warning cover a draft whatever its base.

## Deploying a draft

`deployment apply` (and `publish`) with a draft:

1. **Sync** the system copy from the sites.
2. **Find the draft's base**: the version its "drafted from" line names, or,
   without a usable line, the system copy's files before the sync.
3. **When the sites' newest version is the base**, the draft is the next
   version. The review shows it against the system copy (below).
4. **When the sites' newest version is newer than the base** (another
   operator deployed, from any workstation or another folder of this one),
   the system copy moves to it, and the draft is merged forward with a
   three-way merge: base is the base found in step 2, ours is the draft,
   theirs is the sites' newest version. The merge is new work: `paisans-stack.yaml`
   merges path by path on its YAML node tree (a list is one value; comments
   and key order come from the draft), and the secrets merge by key after
   decryption, extending the secrets-only merge `deployment pull` has today.
   What does not conflict merges. On a conflict the draft keeps its own value
   for each conflicting path, the conflicting paths are listed (secret keys by
   name only), and nothing is deployed; the operator edits the draft and runs
   apply again. Either way the apply stops after writing the merged draft;
   the operator reviews it and runs apply again.
5. **When the sites hold a fork**, apply refuses and names
   `deployment publish --merge`, which resolves it into one signed version as
   it does today; the draft is then merged forward from it on the next apply.
6. **A dry run** syncs the store and arrival record only: it writes no draft
   and moves no files, and says what an `--execute` run would merge.

A deployed draft's files become the system copy, and the draft's "drafted
from" line is rewritten to name the version just deployed. The folder is
otherwise left as it is: the operator keeps editing it.

## What the review shows

Before approval, `deployment apply`'s review shows:

* **The draft against the system copy.** A unified diff of
  `paisans-stack.yaml`, the secret keys added, removed or changed (never their
  values), and whether `.sops.yaml` differs. (This part ships first, ahead of
  this design, against the sites' newest version.)
* **Undo flags.** For each path the draft changes (a dotted path into
  `paisans-stack.yaml`, or a secret key), Paisans walks the system copy's
  trusted history back from the head to the most recent revision that changed
  that path. When the draft's value equals the value from before that
  revision, the line is flagged: `sites.home-b: added back; revision 15
  (signed by alice) removed it.` A deliberate undo and an accidental one look
  the same here, so the flag informs and never refuses.
* **A stale-draft warning.** When any draft file was last modified before the
  newest revision reached this workstation (both times from this machine's
  clock, the second from the system copy's arrival record), the review warns
  once: `This draft was last saved before revision 15 reached this
  workstation; it may not include revision 15's changes.` It is a warning, not
  a refusal: a file copy can reset a modification time, so the signal can miss
  a stale draft but never marks a fresh one stale.

The approval prompt that exists today decides.

## Commands

* `deployment pull` syncs the system copy, then, in a folder with a draft,
  merges the draft forward as step 4 above, from the base step 2 finds.
  Without a draft it starts one.
* `deployment checkout [--id]` starts a draft in the folder from the system
  copy. It refuses a folder that already holds a draft.
* `deployment recover` and the operator join create the system copy from the
  sites and start a draft in the folder.
* `deployment list` lists the system copies; `deployment fetch` fills one from
  a host as today.
* `operator register` attests from the system copy.
* `--config` keeps naming the draft's `paisans-stack.yaml` for every command
  that reads or writes a draft.

## What goes away

* `.paisans/` in every folder, with `incoming/`, `config-root`,
  `config-head`, `config-seen` and `signing-key` there. Their contents live
  only in the system copy.
* `configstore.Local` built from a folder (`workstationOf`). Every trust
  decision is made from the system copy found by deployment id.
* The per-folder refusals that exist only because two folders could disagree.

## Documentation

`README.md` (*Every site keeps signed copies of the configuration*, *This
workstation keeps every deployment it applies*), `docs/commands.md` and help
describe the system copy and drafts in current-state wording; the config-store
spec's *Workstation* wording follows.

## Testing

* Two draft folders for one deployment, both drafted from revision 10:
  deploying the second after the first merges the first's changes into it
  from base 10; a path both changed is a conflict and nothing is deployed.
* The "drafted from" line: written on starting, merging and deploying a
  draft; a missing, garbled or unknown line falls back to the system copy's
  files and says so; a hand-written draft without it is accepted.
* Two draft folders, the second's line deleted: deploying it after the first
  shows the first's changes being undone in the review, with undo flags.
* A draft behind the sites: apply merges forward, stops, and deploys on the
  next run; a conflicting path is shown and nothing is deployed.
* A command that writes files in an empty folder starts a draft from the
  system copy, then changes it; the system copy is unchanged.
* No command writes `.paisans/` in a folder.
* Undo flags: a removed site added back; a value changed twice; a path
  removed and re-added; a secret key put back (by name only).
* Stale warning: a draft saved before the newest revision's arrival warns; one
  saved after does not.
* The lock: two writers at once.

## Left to the builder

* The system copy's internal layout and the arrival record's format.
* How path flattening handles lists in `paisans-stack.yaml`.
