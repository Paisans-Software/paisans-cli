# `init` makes the age key a new deployment needs

Date: 2026-10-11. Status: approved by the founder in session.

A deployment's secrets file is encrypted with sops to the age recipients in the
`.sops.yaml` beside it. Today the operator makes that key by hand before the
first `init`: runs `age-keygen`, puts the private key somewhere sops will find
it, and writes the public recipient into `.sops.yaml`. Nothing in the toolkit
says so, and an operator who skips it gets a plaintext secrets file and a
warning. `init` now makes the key itself on a deployment built from scratch,
stores it in the workstation's own keystore under the deployment's id, and
every later command finds it there without any environment variable.

## Scope

In: key generation in `init`, a new `internal/keystore` package, the
`--age-private-key-file` flag, keystore lookup at decryption, the README
section on where the age key is found, and `docs/development.md`.

Out:

* **Any deployment that already has a key.** A `.sops.yaml` that names a
  recipient, or a secrets file that exists, means the key question is already
  answered. `init` behaves exactly as it does today.
* **Backing the key up.** `init` says the keystore holds the only copy and
  names the remedy (see *What `init` prints*). It makes no second copy itself.
* **Hosts.** `sops` and `age` stay on the workstation. Nothing here reaches a
  host, which `AGENTS.md` requires.

## When `init` makes a key

Only when both hold:

1. the secrets file (`--secrets`, default `secrets.enc.yaml` beside the config)
   does not exist, and
2. no `.sops.yaml` beside it names an age recipient.

That is a deployment built from scratch and nothing else. The private key is
never printed, because a secret printed to a terminal is in a scrollback buffer
and often in a multiplexer's log, which is why `init` already prints the names
of generated secrets and never their values. The key is worse to leak than any
of those: it opens all of them.

## Order inside `init`

1. Write the deployment id (`config.EnsureID`), unchanged. The id is a version
   4 UUID that never changes, so it is what the key is stored under.
2. Generate an X25519 identity with `filippo.io/age`, already a dependency.
3. Store it: in the file `--age-private-key-file` names when given, otherwise
   in the keystore (see *The keystore item*).
4. Read it back and compare it with what was generated. A key that was stored
   wrong, or not at all, must stop `init` here, because the next step encrypts
   every secret to it.
5. Write `.sops.yaml` with the public recipient:

   ```yaml
   creation_rules:
     - age: age1...
   ```

6. Fill the secrets file, unchanged: `fillSecrets` reads the recipient from the
   `.sops.yaml` just written and encrypts to it.

A failure at step 3 leaves nothing written but the deployment id, so a re-run
starts clean. A failure at step 4 also leaves the keystore item, which the
problem names; a re-run refuses until the operator has looked at it and
removed it (see *Problems*).

## The keystore item

| Field | Value |
|-------|-------|
| service | `paisans-age` |
| account | the deployment id |
| label | `paisans age key <deployment id>` |
| recipient | the public recipient, in the comment on macOS and in a `recipient` attribute in Secret Service |

The account is the deployment id because every command already reads secrets
through `config.LoadDeploymentSecrets(path, cfg.ID)`, and the secrets file
records the same id and refuses another deployment's file. The configuration,
the secrets file and the key are then tied by one value, and several
deployments on one workstation never share an item. The recipient is stored
beside the key so an operator looking at the keystore can match an item to a
`.sops.yaml` without decrypting anything.

## Backends

`internal/keystore` holds an interface with `Put` and `Get`, a backend per
platform, and nothing else. Tests replace the interface.

* **macOS: the `security` command.** `add-generic-password -w <key>` would put
  the key in `argv`, where `ps` shows it to every user on the machine, so the
  store runs `security -i` and writes the command to its stdin. Read back is
  `find-generic-password -s paisans-age -a <id> -w`.
* **Linux: `secret-tool`.** `secret-tool store` reads the secret from stdin;
  `secret-tool lookup service paisans-age account <id>` reads it back.

The key never appears in an argument list on either platform. A test asserts
it.

**No keystore means `init` refuses.** That is neither command on `PATH`, or a
store that fails, Eg: `secret-tool` with no D-Bus session on a headless
machine. It refuses rather than falling back to a file, because a private key
in a plaintext file is a choice the operator has to make knowingly, and a
fallback would make it for them. The refusal names both ways forward:

* re-run with `--age-private-key-file <path>`;
* bring a key: write its recipient to `.sops.yaml` and re-run `init`, which
  then generates nothing.

## `--age-private-key-file <path>`

The operator's explicit choice of a plaintext key file. `init` writes the key
there with mode 0600 and prints the `SOPS_AGE_KEY_FILE=<path>` line to set. It
refuses:

* **a path that exists**, because overwriting a key can orphan whatever was
  encrypted to it;
* **a path inside the directory that holds the configuration**, because that
  directory is usually a git repository and the key would be one `git add .`
  from being committed.

The keystore is not touched when the flag is given.

## Finding the key at decryption

`LoadDeploymentSecrets` passes the deployment id down to decryption. When no
`SOPS_AGE_*` variable is set, paisans reads the keystore item for that id and
hands the key to the file's age recipients directly, through sops'
`age.ParsedIdentities`. A variable the operator set always wins, because an
explicit choice outranks a convention, and decryption then runs exactly as it
does today.

The key goes from the keystore to the decryption in memory. No environment
variable is set, so `ssh` and the other children paisans starts never inherit
the key or a way to fetch it.

When the keystore has no item for the id, or cannot be read at all, decryption
runs as today, which still reads `sops/age/keys.txt`. When the item's key opens
none of the file's recipients, the same is tried before giving up. What the operator is told when
both fail:

* no item for this id: the existing "no usable age key" problem, with a line
  naming the item it looked for;
* an item whose key is not among the file's recipients: "the keystore key for
  deployment `<id>` is not a recipient of `secrets.enc.yaml`", which a stale
  item or a hand edited `.sops.yaml` produces, and which sops alone reports
  only as no usable key.

## What `init` prints

Names and the public key, never the private key:

* the public recipient and the file it was written to;
* where the key went: the keystore item (service and account), or the
  `--age-private-key-file` path;
* with `--verbose`, the `SOPS_AGE_KEY_CMD` line for running `sops` directly,
  Eg: `sops updatekeys`, since only `paisans` looks in the keystore on its own:
  `security find-generic-password -s paisans-age -a <id> -w` on macOS,
  `secret-tool lookup service paisans-age account <id>` on Linux;
* a warning that this is the only copy of the key. Losing the workstation
  loses every secret in the file. The remedy is a second recipient, another
  admin's key or an offline backup key, added to `.sops.yaml` followed by
  `sops updatekeys secrets.enc.yaml`.

## Problems

Each is a `ui.Problem` with a hint and an explanation, in the existing style.

| Situation | Result |
|-----------|--------|
| no keystore, or the store fails | refuse; name `--age-private-key-file` and bringing a key |
| read back does not match | refuse; `.sops.yaml` is not written; the item is named and left in place, since deleting a key is not `init`'s decision |
| an item for this id already exists | refuse; name the item. It is left from an earlier run that stopped, and replacing it could orphan a file encrypted to it |
| `--age-private-key-file` given, but the deployment already has a key | refuse; the flag only names where a new key goes |
| a `.sops.yaml` exists but names no recipient | refuse; it is the operator's file, so `init` does not rewrite it |
| `--age-private-key-file` path exists | refuse |
| `--age-private-key-file` path inside the configuration's directory | refuse |
| decryption: no item and no variable | existing problem plus the item it looked for |
| decryption: item's recipient not in the file | the recipient mismatch problem above |
| decryption: the keystore cannot be read, Eg: no D-Bus session or a locked keychain | sops' own search runs; if it finds nothing, a problem naming the item and the keystore's error |
| decryption fails while a `SOPS_AGE_*` variable is set | the no key problem names the variable and the item it kept paisans from reading |
| `init` from scratch with a `SOPS_AGE_*` variable set | no key is made or kept; `.sops.yaml` names the public key the variables hold, and the `age key` step names the variable and that key |
| the variables hold several keys, or a set one gives none | refuse; nothing is written. sops tries every key every set variable holds, so one key across all of them is required |
| `--age-private-key-file` with a `SOPS_AGE_*` variable set | refuse; the flag asks for a new key and the variable names one |
| decryption with a variable while the keystore holds the deployment's key, which the variable does not name | a note at every verbosity naming the variable and the item passed over |

## Testing

* **`internal/keystore`:** fake `security` and `secret-tool` scripts on `PATH`
  record their arguments and stdin. The key arrives on stdin and is absent from
  every recorded argument list.
* **`init` from scratch** with an in-memory keystore: the item is stored under
  the deployment id, `.sops.yaml` names the matching recipient, the secrets
  file decrypts with that key, and no output line contains `AGE-SECRET-KEY`.
* **`init` generates nothing** when a `.sops.yaml` names a recipient, when the
  secrets file exists, when an item for the id exists, and when there is no
  keystore. One test each.
* **Decryption:** a keystore item and no variable decrypts; a set variable wins
  over the item; an item with the wrong recipient gives the mismatch problem.
  The age test identities in `cmd/paisans/errors_test.go` are reused.
* **`--age-private-key-file`:** mode 0600, no overwrite, refused inside the
  configuration's directory.

## Documentation

The README section that says where the age key is found (under *Rendered
configuration is a build artifact*) gains how `init` makes and stores the key,
the keystore item, the backup warning and the second recipient remedy.
`docs/development.md` names `internal/keystore` and the lookup order at
decryption.
