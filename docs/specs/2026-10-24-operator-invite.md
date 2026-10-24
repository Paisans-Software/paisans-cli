# Operator invites

Status: approved by the founder on 2026-10-24; not yet implemented.

Adding an operator today takes the new operator's keys arriving by hand, an age
key made with `age-keygen` and stored by hand under a deployment id they do not
know yet, `.sops.yaml` edited and `sops updatekeys` run by the existing
operator, and a root hash sent back so the new operator can recover. Every one
of those hand steps is a place to get it wrong, and none of them is checked.

This spec replaces them with two signed files that make one round trip: an
**invite** the existing operator writes, and a **request** the new operator's
`register` writes in answer. Both travel over any channel, since each proves
who wrote it. The one thing exchanged outside the toolkit is each person's SSH
public key, once, before the invite.

## Intent

* The new operator runs one command to answer an invite and one to join, and
  never touches `age-keygen`, `sops`, the keystore or a hash.
* The existing operator runs one command to accept the answer, and never edits
  `.sops.yaml` or runs `sops` by hand.
* Neither party has to be online at the same time. Nothing is read aloud.
* Nothing a site serves is trusted to start the new operator's history: the
  root they recover from comes from the invite, which the inviter signed.

## Non-goals

* How the two SSH public keys are exchanged. Assume each party holds the
  other's real public key before the invite is written.
* Delivering the Pocket ID sign-in link. `deployment apply` emails it or prints
  it, as now, and `operator magic-link` issues another.
* Removing an operator, or any change to the three trust rules of the config
  store (`docs/specs/2026-10-12-config-store.md`): which versions are trusted
  does not change.
* Tracking invites the inviter has sent. Nothing about an invite is kept on the
  inviter's workstation.

## Anti-goals

* A private key printed, written beside `paisans-stack.yaml`, or put in an
  argument list. The age key follows `init`'s rules exactly.
* An age key in the keystore replaced. A key may already have something
  encrypted to it.
* A request accepted whose invite the accepting workstation cannot verify as
  signed by an operator of this deployment.
* A recovery that trusts a root the invite did not carry.

## The flow

Before it: the inviter (an existing operator) and the new operator each hold
the other's SSH public key.

1. **Inviter:** `paisans operator invite justin --key justin.pub > justin.invite`.
2. **New operator:** `paisans operator register --invite justin.invite
   --inviter-key inviter.pub > justin.request`. It checks the invite, makes
   their age key under the deployment's id, and writes the signed request.
3. **Inviter:** `paisans operator add --request justin.request`. It checks
   the request, declares the operator as `operator add` does, and makes the
   secrets readable to their age key.
4. **Inviter:** `paisans deployment apply --execute`, as now.
5. **New operator:** `paisans operator register --execute`, in an empty
   directory, any one. It recovers the deployment from the invite's root and sites, then signs
   the attestation `register` signs today.

## The invite

`paisans operator invite <username> --key <file.pub | key line>` writes the
invite to stdout. It runs beside the deployment's files and reaches no host.

It holds nothing secret:

| Field | Is |
|-------|----|
| `version` | `1`. Any other is refused, as a site request's is. |
| `deployment` | the deployment's id |
| `domain` | `community.domain`, so the new operator can see what they are joining |
| `root` | the full hash of the history's root version: the parentless version `deployment log` prints on a root's line, the one `--root` takes |
| `inviter` | the inviter's operator name and the fingerprint of the key that signed |
| `username` | the operator id the new operator will have |
| `key` | the new operator's SSH public key, as type and base64 with no comment, like a site request's keys |
| `sites` | each site the deployment declares: its name, ssh host, port and user, enough for `deployment recover --from` to reach it |

It is signed with the inviter's SSH key through their ssh-agent, the key
`.paisans/signing-key` records, as an SSH signature (`ssh-keygen -Y sign`
format) under the namespace `paisans-operator-invite`. The signature covers
the exact bytes of the YAML above it.

It refuses:

* a username already declared, or one `operator add` would refuse;
* a key another operator lists, or one `operator add` would refuse;
* a workstation with no recorded signing key, or an agent that does not hold
  it, naming `operator register`;
* a workstation that does not know its root (no `.paisans/config-root` and no
  single root among the versions it has gathered), naming `deployment pull`.

The root is taken from what this workstation trusts, never from what a site
serves when the invite is written.

## `operator register --invite`: answering it

`paisans operator register --invite <file> --inviter-key <file.pub | key line>`
runs on the new operator's workstation, in any directory: it reads and writes
nothing there. It reaches no host.

1. **Checks the invite**: its shape, its version, and its signature against
   `--inviter-key`, whose fingerprint must be the invite's `inviter`
   fingerprint. A mismatch is refused with nothing written.
2. **Checks the key it names**: the ssh-agent must hold the invite's `key`,
   since that key signs the request now and the attestation later.
3. **Refuses another pending invite**: when an invite to the same deployment
   is pending (step 5) and differs from this one, it is refused, naming the
   pending invite file, since the request that answered it may still be on its
   way. Removing that file abandons it. The identical invite is answered
   again.
4. **Makes the age key** under the invite's deployment id, or finds the one
   already there. The sources are taken in this order, the first present
   wins, and the step says which it used:
   1. a `SOPS_AGE_*` variable, as `init` takes one: it must hold exactly one
      key, nothing is kept, and a keystore item for the id beside it is
      warned about, since every later command decrypts with the variable;
   2. the keystore item for the deployment id (`paisans-age`), used as it is;
   3. with `--age-private-key-file`, that file: read and used when it exists
      and holds one age key, made by `init`'s rules when it does not;
   4. otherwise a new key in the keystore, by `init`'s rules.

   The first answer for a deployment binds the key it names to the
   deployment. Every later answer for it, from any directory, searches every
   one of the sources above for the bound key and uses the one that holds it,
   even where an earlier source holds another key. When none holds it, the
   answer is refused with no key made, naming the bound key and the binding
   file; removing that file is how an operator starts over with another key.
5. **Asks for the full name and email** at a terminal, with `create`'s
   checks, or takes `--name` and `--email`.
6. **Writes the request** to stdout and records what the join needs in the
   workstation's data directory (where `deployment list`'s saved deployments
   live: `$PAISANS_DATA_DIR`, else `~/Library/Application Support/Paisans` on
   macOS and `$XDG_DATA_HOME/paisans` elsewhere), keyed by the deployment id:
   * `invites/<deployment-id>/invite`, the invite verbatim, and
     `invites/<deployment-id>/inviter.pub`, the inviter's key: the pending
     invite. The invite is written last, and marks the record whole.
   * `age/<deployment-id>.recipient`, the bound age public key and where the
     answer found it, written by the first answer and kept after the join.

   The directory is 0700 and each file 0600, written through a synced
   temporary file and a rename.

The request holds nothing secret:

| Field | Is |
|-------|----|
| `version` | `1` |
| `invite` | the invite, whole and as signed |
| `name`, `email` | as typed |
| `age` | the age public key, `age1…` |

It is signed with the invite's `key`, under the namespace
`paisans-operator-request`.

## `operator add --request`: accepting it

`paisans operator add --request <file>` runs beside the deployment's files, on
any operator's workstation, not only the inviter's.

1. **Checks the request**: its shape, its version, and its signature against
   the key its invite names.
2. **Checks the invite inside it**: its signature is from a key that an
   operator of this `paisans-stack.yaml` lists, its `deployment` is this
   deployment's id, and its `root` is this workstation's root.
3. **Declares the operator** with the invite's `username` and `key` and the
   request's `name` and `email`. Sites are asked for, or given with `--site`
   or `--all-sites`, exactly as `operator add` asks now. Every refusal
   `operator add` makes still applies.
4. **Adds the age key** to `.sops.yaml`'s recipients and re-encrypts
   `secrets.enc.yaml` to the new set with the sops library the toolkit already
   writes secrets with, as `sops updatekeys` would. It decrypts with this
   operator's own key, so a workstation that cannot open the secrets refuses
   before anything is written.
5. **Writes** `paisans-stack.yaml`, `.sops.yaml` and `secrets.enc.yaml`, only
   once `validate` refuses nothing the addition introduced. A failure after the
   first write leaves the files as they were before the command; the builder
   chooses how.

The other flags of `operator add` (`--name`, `--email`, `--key`, the username
argument) are refused beside `--request`, since the request supplies them.

**Accepting the same request again is not refused.** When the invite's
`username` is already declared with exactly the invite's `key`, step 3 is
skipped, and step 4 adds the age key if `.sops.yaml` lacks it and re-encrypts
if the secrets file is not encrypted to it. With both already done it changes
nothing and says so. A declared username with any other key is refused as
`operator add` refuses it.

## `operator register`, again: joining

Run in a directory with no deployment files, `register` takes the pending
invite: the only one when one is pending, the one for `--id <deployment-id>`
when given. Several pending without `--id` are refused, listing their ids.
With none pending, `register` registers as it does without an invite. It
then:

1. **Recovers** as `deployment recover --root <invite root> --from <each
   invite site>` would, with the invite's deployment id. Reaching the sites
   needs the apply of step 4: until it ran, the new operator's key is on no
   site, and the refusal says to ask the inviter whether they have applied.
2. **Checks the newest version lists them**: the invite's `key` under the
   invite's `username` in `operators`. A newest version that does not is
   refused before any file is written, with the same advice.
3. **Writes the files** and records the root and the signing key, as recovery
   does. The invite is then no longer pending, and its directory is removed;
   the age binding is kept. A fork among the sites is refused before this
   step, saying that an operator who holds the files resolves it with
   `deployment publish --merge`, and that the join runs again once they have.
4. **Signs the attestation** as `register` does today.
5. **Says** whether the recovered secrets open with their age key, as a
   failed check rather than a refusal, since the files are theirs either way.
   Secrets encrypted to the bound age key that do not open were accepted, and
   what stops them is on this workstation: the check names the key, where the
   answer found it, and how sops is pointed at a key. Secrets not encrypted to
   it mean the inviter's step 3 did not re-encrypt, and the remedy is for any operator to run `operator add
   --request` with the same request again, then `deployment apply
   --execute`, and for the new operator to `deployment pull`.

`--execute` governs steps 3 and 4 as it governs recovery and registration now.
Without it, the run reads and says what it would write.

Once the files are there, `register` behaves as it does today, and takes no
`--id`.

## No majority prompt

Every command that gathers from the sites goes ahead on whichever sites
answer, once at least one does. Nothing asks the operator to type a word for
having reached few of them, and no dry run warns that one will. This applies
to `deployment apply`, `publish`, `pull` (with or without a secrets merge),
`recover` and `operator register` alike, and to the join above, which is a
recovery and a registration.

What stays: each site that did not answer is still reported by name with its
error, as the gather reports it now, and a command that reached no site is
still refused.

The word `partial`, the `Majority` field of a gather and the count of sites
it was judged against go with the prompt, since nothing else reads them.
`README.md` loses *A majority of sites* from *An old version is made visible*,
and the config-store spec's wording follows. Founder decision, 2026-10-24:
the prompt added confusion and no value, and operators who reach more sites
reconcile a stale view a single site could show.

This lands first, as its own change, since the invite flow relies on it and it
stands without the rest.

## `paisans operator age-key`

Shows the operator's age public key for a deployment, making the key first if
there is none. The id comes from `--id`, or from `paisans-stack.yaml` beside
it. It is the step `register --invite` uses, as a command of its own, for an
operator who wants their key without an invite, Eg: to send it for a hand
edit, or to check which key they hold.

* Keystore item present: prints its public key, changes nothing.
* Absent: makes one, by the order `register --invite` takes its sources in,
  then prints its public key.
* Never prints the private key.

`init` and this command share one implementation of making and keeping the
key.

## Documentation

* `README.md`, *Operators and community admins*: the flow above replaces the
  paragraph on `operator add` that sends the age key by hand.
* `docs/commands.md`: `operator invite` and `operator age-key` get sections;
  `operator register` and `operator add` describe `--invite` and `--request`.
* `help.go` and `CHANGELOG.md` follow.

## Testing

* Unit tests for the invite and request formats: round trip, every refusal,
  a signature from the wrong key, a tampered byte after signing, a request
  whose invite was signed by a non-operator.
* `operator add --request` against a fixture deployment: the operator is
  declared, `.sops.yaml` names both recipients, and the secrets open with each
  key.
* `register` joining against the config-store test harness: refused before
  the apply, succeeds after it, never trusts a root other than the invite's.
* Each command that asked for `partial`: goes ahead with one site of three
  answering, at a terminal and without one, and still refuses with none.
* On staging, one end-to-end run of the five steps with two workstations.

## Left to the builder

* The SSH signature implementation (a library or `ssh-keygen -Y`), so long as
  the signatures verify with `ssh-keygen -Y verify`.
* The file layout of the signed files, so long as the signed bytes are exactly
  the YAML and the files are plain text that survives being pasted.
* Names of internal packages and functions.
