# Signed copies of the configuration on every site

Date: 2026-10-12. Status: approved by the founder in session, 2026-10-12.

`paisans.yaml`, `secrets.enc.yaml` and `.sops.yaml` live on admins'
workstations, usually in a git repository. Losing them loses the deployment's
topology and every credential (README, *Backup and restore*), and nothing in the
toolkit keeps another copy. This spec makes every site keep every version of the
three files, signed by the admin who published it, shared among the sites over
the mesh, and recoverable onto an empty workstation.

The copies start as recovery copies and are built to become the deployment's
shared source of truth: every version records its parent, two versions from the
same parent are a fork, and `apply` refuses to run from a copy older than the
sites' newest.

The one loss this cannot protect is every admin losing their age key. That is
total loss, which is why README tells every deployment to add a second
recipient.

## Settled

Founder decisions, 2026-10-12, in session:

* **Sites hold recovery copies now and are the shared source of truth later**;
  the version history and fork detection are built from the start.
* **The anchor is the operator's own signature, or a pinned root.** The
  operator's own SSH key, recorded by `paisans admin register`, or a root hash
  they pin, is where trust starts; a history found only on the sites is never
  trusted on its own. Founder decision, 2026-10-12.
* **One signature, from any current admin.** Every admin is trusted with the
  whole deployment already (they hold the configuration and can decrypt the
  secrets); signatures are for telling an admin from someone who is not one, and
  a current admin from a former one.
* **Push plus peer sync.** Publishing pushes to every site it reaches; a timer
  on each site pulls what it lacks from its peers.
* **`apply --execute` publishes the version it applies**, and `paisans config
  publish` publishes an edit that is not applied yet.
* **A stale copy is refused.** `apply` refuses to run from a base older than the
  sites' newest version, and names `paisans config pull`.
* **Git, with the sites as plain replicas.** The sites store; the workstation
  decides what to trust.

## Scope

In: the store on every site, publishing, peer sync, pulling, recovery onto an
empty workstation, forks and their merge, and each host's record of the version
last applied to it.

Out, each for a stated reason:

* **The age keys.** They must never be on a host (AGENTS.md), and backing them
  up is each admin's.
* **A copy outside the sites.** The backups design stores to object storage; a
  copy of every version there is a source a host attacker cannot touch, and is
  decided with that design.

## A version

A version is a git commit whose tree holds exactly `paisans.yaml`,
`secrets.enc.yaml` and `.sops.yaml`. A commit holding anything else is invalid.

* **Signed** with the publishing admin's SSH key, by git's `gpg.format=ssh`, so
  signing goes through the operator's ssh-agent and needs no new key.
* **Its message** carries `Paisans-Revision: <n>`, one more than its highest
  parent. The commit date is for people; nothing decides by it, because any
  host's clock can be wrong and anyone writing a copy can set it.

The signature covers the commit, and so the three files in its tree. Nothing in
the files themselves can prove who wrote them: encrypting to an age recipient
needs only the recipient's public key, which every stored `.sops.yaml` names, so
anyone can write a secrets file that decrypts for every admin.

Every version is stored under a ref named for its own hash,
`refs/versions/<sha>`, so two versions never compete for a name, and nothing is
ever deleted.

## Which versions the workstation trusts

The sites keep whatever they are sent. Every decision is made on the
workstation, and it starts from something the operator holds that nobody else
can forge.

**The anchor** is the newest version signed by the operator's own SSH key, the
one `paisans admin register` recorded (below), or a root hash the operator pins
with `--root <hash>`. A workstation that has pulled before keeps both in
`.paisans/`. With neither, a pull or recovery is refused and asks for `--root`:
a history found only on the sites, however consistent, is one someone with root
on those sites could have written, signed with their own key and listing
themselves as an admin.

**Trusted** versions are the anchor, every ancestor of it, and every version
that passes all of these:

1. **Shape.** Its tree holds exactly the three files, its signature is good, and
   its revision is one more than its highest parent's.
2. **The signer.** Every parent is trusted, and the signing key belongs to an
   admin listed in every parent's `paisans.yaml`. Someone who is not an admin
   cannot extend the history, however many sites they control.
3. **Removal is final, key by key.** When a trusted version drops a key that a
   parent listed (an admin removed, or one of their keys rotated out), every
   version signed by that key that is not an ancestor of the removal is not
   trusted. A version that is not trusted removes nothing. Removals in the
   anchor's own history always stand, because the operator signed after them.
   Two removals that each would bar the other, neither in the anchor's history,
   are refused and shown to the operator; nothing decides between them by
   revision or hash, since the one who signs chooses both.

**The head** is the trusted version with the highest revision that no other
trusted version builds on. Two such versions are a fork.

**Removing an admin who could decrypt the file.** Their age key comes out of
`.sops.yaml` and the file is re-encrypted under a new data key (`sops rotate
--rm-age`) before the version that removes them is published, so nothing written
afterwards is readable to them.

## `paisans admin register`

An admin who has never applied has signed nothing, so their workstation has no
anchor of its own. `admin register` gives them one without changing anything on
a host:

* It finds the operator's key: the key in their ssh-agent that matches one
  listed under their entry in `admins`, or the one named with `--key`. It
  refuses when none matches and says which `admins` entry to add the key to,
  which another admin publishes.
* It gathers, then signs an **attestation**: a version whose tree is exactly the
  head's and whose only parent is the head, one revision on. It records the key
  in `.paisans/signing-key` and the attestation as the anchor.
* It attests only a head whose three files equal the operator's local files,
  which are the copy they already trust; otherwise it refuses and shows the
  difference. On a workstation with a pinned root it may attest the head the pin
  leads to.

`config publish` and `apply` sign with the recorded key, so after `admin
register` every later version is the operator's own anchor as well.

## Rollback

A version that was really published verifies forever, so someone who controls a
site can delete the newer ones there and serve an old one. Applying it would put
back old secrets, undoing a `secrets rotate` or a `mesh rekey`. No signature can
prove a version is the newest; these make an old one visible:

* **The workstation remembers.** Nothing older than its anchor is ever a head,
  and it records the newest revision it has seen in `.paisans/config-seen` and
  refuses a head with a lower one unless the operator types `rollback`. The
  record is a file of its own, not part of the cache (`store.git`), because the
  cache can be deleted and gathered again and the memory must survive that. A
  record nothing could lower would be a trap for an operator restoring from an
  old backup, so an accepted `rollback` re-bases it to the head chosen. Only
  `--execute` asks; `log` warns and lists, and a dry run reports the rollback
  and what `--execute` will ask. A root pinned for one run with `--root` and not
  recorded does not raise the record, so a mistaken pin cannot poison it.
* **A majority of sites.** A pull, a recovery, `admin register` and `apply
  --execute` report how many sites they reached directly, and with fewer than a
  majority the operator types `partial` to go on.
* **Hosts are witnesses.** Each host's grant record
  (docs/specs/2026-10-11-config-review.md) also holds the hash and revision of
  the version last applied to it, and `apply` refuses to apply an older revision
  to a host whose record is newer, or holds the same revision under another
  hash, unless the operator types `rollback`. Another version at the same
  revision is a version another admin applied there that this one would replace,
  which is what the witness exists to surface.

So a host attacker can roll back only the hosts they hold. What stays is a fresh
workstation that reaches only sites the attacker controls: it can be served an
old, genuine version, and applying it reaches only those sites.

## The store on a site

* A bare repository at `/var/lib/paisans/config/<token>.git`, owned by root,
  created by `host prepare`, which also installs `git`.
* A read-only `git daemon`, unit `paisans-<token>-config-daemon`, bound to the
  site's mesh address and serving only that directory. The mesh interface is
  already let in whole by the firewall. A mesh peer reads `paisans.yaml` and the
  encrypted secrets, both of which a peer is already trusted with.
* A timer, `paisans-<token>-config-sync`, every 15 minutes: `git fetch` of
  `refs/versions/*` from each peer's mesh address, listed in a rendered file,
  without `--prune`, so nothing is ever deleted.
* An evicted site is no longer a peer, so it neither fetches nor serves.
* Each version is tens of kilobytes; thousands take a few megabytes, and nothing
  is pruned.

The units follow the `paisans-<token>-*` naming, so `site remove` cleans them
with the rest.

## Commands

Local state is four files beside `paisans.yaml`, in `.paisans/`: `config-root`,
the pinned root; `config-head`, the version the local files started from;
`config-seen`, the newest revision this workstation has seen (see *Rollback*);
and `signing-key`, the operator's public key that `admin register` recorded. The
anchor is the newest version that key signed. A cache of the versions gathered
(`store.git`) and the files a refused pull sets aside (`incoming/`) sit beside
them, and a `.gitignore` there keeps both out of the operator's own repository.

The three files of a version have fixed names, so `--config` must name a file
called `paisans.yaml`, with `secrets.enc.yaml` and `.sops.yaml` beside it.

**Gathering.** `pull`, `recover` and the stale-copy check in `apply` all gather
the same way before deciding anything:

1. **With `--execute`, every site reached starts its sync now** and the command
   waits for it, so versions from sites the operator cannot log in to (Eg: a
   host whose allowlist leaves them out) arrive through one they can. A dry run
   and `config log` start no sync: a command changes a host only with
   `--execute`, so they read what each site holds now, which can be staler.
2. **Every site is reached.** The sites named in the configuration, or, for a
   recovery, in the first version that verifies, are each reached over ssh, not
   only the ones given on the command line. The store is root's, so each is
   reached with `sudo`, and a sudo password is asked for once, before the first
   site, as for the other commands.
3. **Union, then verify.** Every copy is combined before the rules run, and only
   then is the head chosen.

The majority in *Rollback* counts only sites reached directly over ssh. A
version that arrived through another site's sync counts toward finding the
newest, but "these peers answered" is that site's word, and a site that lies
could inflate it.

| Command | Does |
|---|---|
| `paisans admin register` | finds the operator's key, gathers, and signs an attestation of the head when it equals the local files; records the key and the anchor (above) |
| `paisans config publish` | commits the three files with the base as parent, signs with an admin key from the ssh-agent, and pushes a `git bundle` to every site it reaches over the existing ssh transport, where `sudo git fetch` stores it. Refused: the base is not the sites' head, including when the workstation has no base and the sites hold versions (a second root would result; run `config pull` first); no loaded key belongs to an admin listed in the parent; no site answers |
| `paisans apply`, with or without `--execute` | gathers before the review, and refuses a fork and a stale base; it needs the standard file names beside `paisans.yaml` and an admin key in the ssh-agent even for a dry run, since it checks what `--execute` would sign with |
| `paisans apply --execute` | after approval, publishes, then applies, so the store shows the declared state even when the apply stops part way; the version published is the very bytes apply loaded and applies, never a second read of the files; writes the version into each host's grant record; refuses an older revision than a host's record, or another version at the same revision, unless `rollback` is typed |
| `paisans config pull` | gathers and verifies; with no local edits, moves the files to the head; with local edits on an older base, refuses, writes the head's files to `.paisans/incoming/` and shows the difference |
| `paisans config publish --merge` | a version whose parents are every head, after the operator has merged by hand; it resolves a fork and an incoming change alike |
| `paisans config log` | every version: revision, signer, date, and which sites hold it |
| `paisans config recover --from <user@host>...` | onto a workstation with nothing: gathers from the named hosts and every site the first verified version names, verifies, reports how many sites answered, and writes the three files and the state files. A host's registry may name more than one deployment; `--id` names the one to recover, and a recovery that finds several without it refuses and lists them |

**Publishing in `apply`.** `apply` publishes the version it applies and refuses
when the version reaches no site, naming `host prepare` for a site that has no
store yet: a version no site holds is not in the store the hosts are witnessed
against. `apply --site` publishes before its first change. An `apply` of the
whole deployment publishes after its `host prepare` steps, which create the
stores, and before any other step changes a host: the stores cannot receive a
version before they exist, and prepare's own changes are reviewed already. The
witness check still precedes everything. A run that stops between prepare and
publish leaves keys granted without the version published, and the next run
publishes it. `apply --site` without a store refuses naming `host prepare`.

**A fork on recovery.** `recover` never picks between two heads by order. With a
fork it writes each head's files to `.paisans/incoming/<sha>/` and refuses, and
the operator merges by hand and publishes with `--merge`.

**Secrets conflicts.** Two edits of the encrypted file cannot be merged as text.
`pull` decrypts both sides and compares them key by key: changes to different
keys merge; two changes to one key are refused, naming the key and no value. The
pull takes neither side's value by itself: to take the head's value the operator
sets that key locally to it (`paisans secrets set` does it for a key it accepts)
and pulls again. To keep the local value, the operator takes the head's value
first so the pull can merge the rest, pulls, then sets the key back to their
value and publishes; a `pocket_id_groups` key, which `paisans secrets set` does
not accept, is edited with `sops`.

## Failures

* **A site unreachable at publish** gets the version from its peers; the report
  names it.
* **A fork** stops `apply` until `publish --merge` resolves it.
* **A version that does not verify** is listed with the rule it fails and never
  counts toward the head.

## Testing

* The trust rules against built histories: a forged root, a version signed by an
  admin removed earlier, a key rotated out, two removals that bar each other, a
  history with no anchor, a replayed old version, a fork, a merge, removal is
  final, more than one root.
* Publishing to a local bare repository standing in for a site, and a resumed
  publish after some sites failed.
* The daemon and timer units in the golden render tree.
* Gathering: a site reached directly, a version arriving only through another
  site's sync, the majority counting only direct reaches.
* The witness refusal and `rollback`, through the fake terminal.
* Each secrets merge case.
