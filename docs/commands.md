# Commands

One entry per `paisans` command: what it does, when an operator runs it on its
own, its flags with their defaults, whether it reaches a host, and what it
does without and with `--execute`. `paisans help` lists the commands in a line
each, and `paisans help <command>` or `paisans <command> --help` prints one
command's usage, what it does and its flags, from `cmd/paisans/help.go`.

This file holds the mechanics. Why each command behaves as it does is in
`README.md`, and each entry names the section there that carries the
reasoning.

## Most of the time, one command

`paisans apply` without `--site` takes `paisans-stack.yaml` to a complete, running
stack. It runs `init`, `host prepare`, `dns init`, `apply --site`, `site add`,
`storage init`, `storage add` and, when the gateway role has moved, `gateway
move` itself, in the order the deployment needs them, each exactly as it runs when typed, pulls every site's images on every
host at once before the first of them installs anything, and ends by running
`web-server check`'s checks for each app behind the operator's own web server
(see *`paisans apply` with no `--site` converges the whole deployment* in
`README.md`). So the usual way to make any change is to edit `paisans-stack.yaml`
and run:

```sh
paisans apply             # every step it would run, and what each would change
paisans apply --execute   # runs them, stopping at the first failure
```

Running one of those commands on its own is for a narrower job: looking at one
step, working on one host, or reaching for a flag the whole apply does not
pass on. Every other command is never run by the whole apply. Each is a
decision an operator makes (removing a site or an app, replacing a key,
deleting DNS records, making an administrator) or a read (`doctor`,
`preflight`, `web-server show`).

## Flags every command shares

| Flag | Default | Taken by | Meaning |
|------|---------|----------|---------|
| `-v`, `--verbose` | off | every command | the reasons, values, request bodies and command output behind each step; with it, a failure also shows the underlying error |
| `--config <path>` | `paisans-stack.yaml` | every command except `host deployments` | the deployment declaration |
| `--secrets <path>` | `secrets.enc.yaml` beside the config | every command that reads secrets | the sops encrypted secrets file |
| `--execute` | off | every command that changes something, as each entry says | without it, the command plans and prints and changes nothing |
| `--sudo` | on | `host deployments` | run its remote commands through sudo, since the registry is root's; `--sudo=false` skips it (see *A sudo that wants a password is asked for once, on a terminal* in `README.md`). Every other command runs as the site's ssh user, without sudo |
| `--ssh <destination>` | the site's `ssh` section | commands about one site's host | used verbatim in place of the site's `ssh` section, whose user, host, port and keys are then ignored (see *`--ssh` replaces the whole section* in `README.md`) |

With `--execute`, a command first claims each host it writes to in
`/var/lib/paisans/registry.json`, and refuses if another deployment there
holds this one's token, WireGuard interface or listen port, or an overlapping
mesh subnet (see *A deployment has an id, and a host knows every deployment on
it* in `README.md`).

With `--execute`, `apply`, `apply --site`, `host prepare` and `site add` first show what
`paisans-stack.yaml` grants on each host they reach that the host has not had (a key
that may log in, an image that runs, the directory the operator's web server
reads), and, when anything is added or changed, ask for the word `approve` at
a terminal before that claim. Without a terminal they refuse, before any host
changes; no flag answers the question. A run that adds or changes nothing asks
nothing. Their dry runs show the same review and ask nothing (see *`apply` and
`host prepare` review what `paisans-stack.yaml` grants* in `README.md`).

A site that leaves `ssh.user` out is reached as the deployment's own user,
`paisans-<token>`. Any run with `--execute` that logs in to it writes that
name into the site's `ssh.user`, whatever the command, so the file names who
logs in; a name already there is never changed, and a write that fails is a
warning, since the login it records has worked. A run without `--execute`
writes nothing and notes what `--execute` records.

A command exits 0 when it did what it was asked, including when it ends on a
warning (`!`, `WARN` off a terminal), 1 when it stopped on a failure or a
refusal, and 2 when its command line cannot run. `storage add`
also exits 75 when a stage is waiting on Garage. `doctor` and `web-server check`
exit 1 on any `FAIL`.

## Configuration and secrets

### `paisans validate`

Loads the declaration and prints every problem found, refusals and warnings
both, rather than stopping at the first. Exits non zero when anything is
refused.

Once the file is refused nothing, it tries every app's mail settings: each
logs in to its mail server from a site running the app, and nothing is sent
(see *Mail settings are tried before they are used* in `README.md`). A
failure is a warning, never a refusal, naming the app, where its settings are
declared and the server's reply.

```sh
paisans validate
paisans validate --offline   # the file alone; no site is reached
```

* **Alone:** while editing `paisans-stack.yaml`, before anything else reads it.
  Every command that loads the declaration runs the same checks, so this is
  the one that does nothing else.
* **Flags:** `--config`, `--secrets` (the SMTP passwords the mail check logs
  in with; default `secrets.enc.yaml` beside the config), `--offline` (off:
  check the file alone and reach no site).
* **Hosts:** reaches each site running an app that sends mail, over ssh, and
  through it the app's mail server. Changes none. A site that cannot be
  reached, or a secrets file that cannot be read, skips the mail check with a
  warning, so it works away from the hosts.
* **`--execute`:** not taken.

### `paisans create`

Asks about a new deployment at a terminal (the community and its domain, the
DNS provider, the operators, at least two of them, how many machines and what
each does, the apps, who manages accounts, and the mail server) and writes a commented `paisans-stack.yaml` from the answers. The
file is checked as `validate` checks it before it is shown; a refusal offers
the questions it came from again, and nothing is written until the file is
refused nothing and the operator says yes. It then names `secrets set`,
`init` and `apply`.

* **Alone:** once, before anything else, for a deployment that has no
  `paisans-stack.yaml` yet. The whole apply never runs it.
* **Flags:** `--config` (the file to write; default `paisans-stack.yaml`).
* **Hosts:** none. It asks for no secret either: `init` generates them and
  names the ones owed from elsewhere.
* **`--execute`:** not taken. It writes one local file, mode 0644, and refuses
  a path that already holds one, both before the first question and at the
  write. Exits 1 when nothing was written, including a stdin that is not a
  terminal.

See *`paisans create` writes a new deployment's file, and only that* in
`README.md`.

### `paisans init`

Gives the declaration an `id` if it has none, settles `mesh.subnet` against
every site while the deployment is on none of them, generates every secret the
declaration needs into the secrets file (filling only what is missing), and
says what is still owed from elsewhere. Each owed secret is a warning, since
only the operator can provide it. The sign in clients `apply` creates are
pending rather than owed, and are one line, Eg: `4 sign in clients (blog,
docs, gate, health) are created by apply`, with what apply makes for each
under `--verbose`.

* **Alone:** the whole apply runs it whenever it has work, so it is typed on
  its own to see what is owed before any host is changed, or to generate the
  secrets for a site or app just declared without applying anything.
* **Flags:** `--config`, `--secrets`, `--age-private-key-file <path>` (keep a
  newly made age key in this file, outside the configuration's directory,
  instead of the keystore).
* **Age key:** for a deployment with no secrets file and no `.sops.yaml`, it
  makes the age key and keeps it in the keystore, or in
  `--age-private-key-file`. With a `SOPS_AGE_*` variable set it makes none:
  it writes the variable's public key to `.sops.yaml` and names the variable
  on the `age key` step. Variables that hold no key or several are refused,
  and so is `--age-private-key-file` beside one.
* **Hosts:** reads every site, to choose the mesh subnet. Changes none.
* **`--execute`:** not taken. It writes local files only: the `id` and the
  subnet into `paisans-stack.yaml`, the secrets into the secrets file, and
  `.sops.yaml` (and the key file, when named) for a new deployment.

See *Three kinds of secret*, *Rendered configuration is a build artifact* and
*A deployment has an id, and a host knows every deployment on it* in
`README.md`.

### `paisans render`

Validates, then writes every site's artifacts under a local directory.

* **Alone:** to read what `apply` would push, file by file. `apply` renders
  for itself and never runs this as a step.
* **Flags:** `--config`, `--secrets`, `--out <dir>` (required).
* **Hosts:** none.
* **`--execute`:** not taken. It writes under `--out` and stops.

### `paisans secrets list`

Prints every secret `paisans-stack.yaml` needs, and every other one the secrets
file holds for something it still declares, one row per path, with no value:

```
PATH                                STATE  SOURCE     ROTATE
apps.talk.oauth_private_key         set    generated  apps.talk.oauth
backup.restic_password              set    generated  kept
cluster.standby_password            set    generated  cluster.standby_password
external.acme_dns_token             unset  pasted     -
oidc_clients.talk.client_secret     unset  captured   -
sites.home-a.wireguard_private_key  set    generated  mesh rekey
```

* **`STATE`:** `set` when the file holds a value there, `unset` when it does
  not.
* **`SOURCE`:** `generated` by `init`, `pasted` with `secrets set`, or
  `captured` by `apply --execute` from a running service (see *Three kinds of
  secret* in `README.md`).
* **`ROTATE`:** the path `secrets rotate` takes for a set secret: its own, or
  its unit's for one an app's blueprint rotates as a unit; `kept` for one
  `secrets rotate` keeps; `mesh rekey` for a WireGuard key; `-` for an unset
  secret or one nothing rotates.

```sh
paisans secrets list
paisans secrets list | grep unset   # what is still to fill
```

* **Alone:** always. It is how to find the path `secrets set` and
  `secrets rotate` take without opening the decrypted file.
* **Flags:** `--config`, `--secrets`.
* **Hosts:** none. It decrypts the local secrets file and reads it.
* **`--execute`:** not taken. It changes nothing.

A path naming something `paisans-stack.yaml` no longer declares is counted, not
listed; `secrets prune` lists it. The table is on stdout and the report on
stderr, so the table pipes cleanly.

### `paisans secrets set`

Reads one value from stdin and writes it into the encrypted secrets file under
a dotted key, printing only the key's name. For credentials issued elsewhere,
such as the DNS provider's token.

An SMTP password (`external.smtp_password`, `apps.<app>.smtp_password`) is
tried before it is written: for each sender of the apps that log in with it,
from a site running one of them, it logs in to the mail server and sends one
test message to every operator's email. A failure writes nothing, and says
the server's reply, never the password (see *Mail settings are tried before
they are used* in `README.md`).

```
security find-generic-password -s acme -w | paisans secrets set external.acme_dns_token
```

* **Alone:** always. The whole apply names it, in a note, for each credential
  still owed.

Given a key at a terminal, it asks for that key's value with echo off, saying
whether the key is already set. An empty answer writes nothing.

```
paisans secrets set external.acme_dns_token
```

Run with no key at a terminal, it asks for each secret still owed in turn,
with echo off, writing each answer before asking for the next. An empty answer
skips a key.

```
paisans secrets set
```

* **Flags:** the key, as the first argument, or none to be asked for each owed
  secret; `--config`, `--secrets`.
* **Hosts:** none, except for an SMTP password, which is tried from a site
  running an app that uses it, over ssh, and changes nothing there.
* **`--execute`:** not taken. It writes the local file at once. Given no key
  it refuses anything but a terminal on stdin.

See *Three kinds of secret* in `README.md`.

### `paisans secrets prune`

Lists the secrets that name something `paisans-stack.yaml` no longer declares, by key
and never by value: a removed site's WireGuard key, a removed app's passwords
and sign in client, a Pocket ID group nothing names, a removed backup
destination's credentials.

```sh
paisans secrets prune             # lists them
paisans secrets prune --execute   # removes them
paisans secrets prune --without-record --execute   # no gateway to read; asks for the word prune
```

* **Alone:** always, after a site or an app has been removed.
* **Flags:** `--config`, `--secrets`, `--execute`, `--without-record` (off:
  trust `paisans-stack.yaml` alone when no gateway's deployment record can be read;
  with `--execute` it asks for the word `prune` at a terminal).
* **Hosts:** reads each gateway's deployment record, as the ssh user, since it
  holds only names and is world readable. Changes none.
* **`--execute`:** removes them and writes the local secrets file, encrypted to
  the same recipients.

### `paisans secrets rotate`

Replaces secrets where they are checked and where they are used, one secret at
a time: a next value written to the secrets file, the side that checks it
taught it, every site that presents it applied in turn and healthy before the
next, then the next value promoted. Lists the secrets it keeps, each with why.
A stopped run resumes where it stopped; `apply`, and every other command that
renders from the secrets file and reaches a host, refuses until it finishes,
since each renders the old value.

```sh
paisans secrets rotate --all                               # dry run: every secret in order, its cost, and what is kept
paisans secrets rotate --all --execute
paisans secrets rotate cluster.standby_password --execute
paisans secrets rotate apps.talk.oauth --execute           # Mbin's OAuth2 keys, as one unit
paisans secrets rotate external.acme_dns_token --execute   # asks for the new token, then hands it to every site using it
security find-generic-password -s acme -w | paisans secrets set external.acme_dns_token
paisans secrets rotate external.acme_dns_token --execute   # without a terminal: hands on the token set above
paisans secrets rotate --all --replaced external.acme_dns_token --execute   # the token an eviction already applied
paisans secrets rotate --all --restart --execute           # the full list again, whatever a stopped --all left
```

* **Alone:** always; after an operator who could read the secrets file leaves,
  or a site is evicted, once the file is re-encrypted and the mesh rekeyed.
* **Flags:** the secrets' paths in the secrets file as arguments, or `--all`
  (every secret in scope, in the order *`secrets rotate` replaces a secret
  where it is checked and where it is used* gives); `--restart` (with
  `--all` only: discard what a stopped `--all` run left and rotate every
  secret in scope again; refused while a rotation is pending, which finishes
  first); `--replaced <path>` (repeatable: a pasted secret already replaced
  and applied, which every host using it runs; listed, not rotated, and
  refused for a generated secret or one some host does not run yet);
  `--config`, `--secrets`, `--execute`.
* **A stopped `--all`:** the secrets file records what it left
  (`rotate_remaining`), and the next `--all` does only that, saying so at
  every verbosity and in its result: the secrets the earlier run finished
  are not rotated again. A recorded path the deployment no longer has a
  rotation of is refused, naming it and `--all --restart`.
* **Hosts:** every site, since each must answer before anything changes, and
  each is claimed. The secrets file is read and written on this machine only.
* **`--execute`:** rotates each secret in four gated stages, and stops at the
  first gate that fails. Asks for `sign out` at a terminal first when a rotation
  in the run signs members out, and is refused without one. Refuses before
  anything changes when a `mesh rekey` is unfinished, a site does not answer
  over ssh, or etcd or Patroni is unhealthy. `--all` also refuses a secrets
  file that lacks a secret `init` generates, naming `paisans init`.
* **A pasted secret** (the DNS token or an SMTP password) **that every host
  using it runs:** `--execute` asks for its new value at a terminal, with echo
  off and before any host is claimed, naming where to replace it (the DNS
  provider, or the mail provider). The dry run lists it as asked for at
  `--execute`. An empty answer stops the run with nothing changed, naming
  `secrets set` and `--replaced`; an answer equal to the value the hosts run is
  refused as unchanged; and without a terminal the run is refused, with the
  steps to replace it, `secrets set` it, and run again. A resumed rotation is
  not asked for again. One already set with `secrets set`, which some host does
  not run yet, is not asked for: the file's value is handed out. Which value a
  host runs is read per secret: the host prints the hash of each line of the
  file that carries it, and the lines that carry it are compared, so another
  change in that file does not count.
* **The DNS token's check:** the next token, pasted or set, looks up the
  community domain's zone at the provider, read only, and one that cannot see
  it is refused before it is written or handed to any Caddy. A provider whose
  record management is not implemented is not checked; the plan says so.
* **An SMTP password's check:** a pasted one logs in to the mail server from
  a site running each app that uses it and sends every operator one test
  message as soon as it is given; one set with `secrets set` logs in when the
  run is planned. One that fails is refused before it is written under
  `rotations`. Stage 2 logs in again, and sends nothing.

A pending rotation that cannot finish, Eg: of a secret whose app was taken
out of `paisans-stack.yaml`, is ended by hand: *A rotation that cannot finish* in
`README.md`. See *`secrets rotate` replaces a secret where it is checked and
where it is used* there too, and
[guides/when-an-operator-leaves.md](guides/when-an-operator-leaves.md) for the order
around it.

### `paisans config`

The signed copies of `paisans-stack.yaml`, `secrets.enc.yaml` and `.sops.yaml` that
every site keeps. Four subcommands, each gathering every site's versions first
and trusting them only from the operator's own signature or a root pinned with
`--root`.

```sh
paisans config publish                # what would be signed, with which key
paisans config publish --execute      # sign the local files, push to every site
paisans config publish --merge --execute   # one version on every head of a fork
paisans config pull                   # what would be written
paisans config pull --execute         # move the files to the sites' newest
paisans config log                    # every version, signer, holding sites
paisans config recover --from admin@203.0.113.9 --root <sha> --execute   # empty directory
```

* **Alone:** always, except as `apply` runs its part (below). Each is an
  operator's decision: what to publish, when to take the sites' version, and
  where to recover.
* **Flags:**

  | Flag | Taken by | Meaning |
  |------|----------|---------|
  | `--config <path>` | all | the deployment declaration, which must be named `paisans-stack.yaml` with `secrets.enc.yaml` and `.sops.yaml` beside it; `.paisans/` beside it holds this workstation's state |
  | `--merge` | `publish` | the version's parents are every head, after the operator has merged a fork or an incoming change by hand |
  | `--root <sha>` | `pull`, `log`, `recover` | trust starts from this version; `pull` and `recover` record it, `log` judges from it for the run only |
  | `--from <user@host[:port]>` | `recover` (required, repeatable) | a host of the deployment to recover from; every site the trusted heads name is reached after it |
  | `--id <id>` | `recover` | the deployment, when a host's registry lists more than one; it must be a deployment id |
  | `--execute` | `publish`, `pull`, `recover` | do it; `log` takes none |

  And the flags every command shares.
* **Hosts:** every site, over ssh as the site's ssh user, and each is
  reached, not only the ones named on the command line. `recover` has no
  `paisans-stack.yaml` to name them and so starts from `--from`.
* **Without `--execute`:** no site is changed and no site is asked to sync with
  its peers; it reads what each site holds now, which can be staler. Only the
  cache in `.paisans/` beside the configuration is written.
* **Per-operator state:** `.paisans/.gitignore` keeps `store.git/`,
  `incoming/`, `signing-key`, `config-head` and `config-seen` out of a
  repository the operators share, since each describes one workstation, and a
  shared signing key would start one operator's trust from another's signature.
  `config-root` may be committed so the operators share the pin.
* **`--execute`:** each site first syncs with its peers, so a version only a
  site the operator cannot log in to holds arrives through one they can.
  `publish` signs with the key in `.paisans/signing-key`, or the one ssh-agent
  key that belongs to an operator the base lists, and pushes to every site it
  reaches. It refuses when its base is not the sites' newest version (including
  a workstation with no base while the sites hold versions: run `config pull`
  first), when the sites hold a fork and `--merge` is not given, and when no
  site answers. `pull` moves files with no local edits to the head; over local
  edits on an older base it refuses, shows how they differ, and writes the
  head's files to `.paisans/incoming/` to merge by hand. When only
  `secrets.enc.yaml` differs on both sides it merges key by key: two changes to
  one key refuse, naming the key and never a value, and the pull takes neither
  side's value by itself (set the key locally to the head's value and pull
  again; to keep the local value, take the head's first, pull, then set the key
  back with `paisans secrets set` and publish, or edit a `pocket_id_groups` key
  with `sops`). It refuses while either side has a `paisans mesh rekey` under
  way, naming the sites, until `mesh rekey` finishes it, and while either side
  has a `paisans secrets rotate` under way (`rotations` or `rotate_remaining`),
  naming the paths, until `secrets rotate` finishes it. `recover` refuses to
  write over files, and with a fork writes each head to
  `.paisans/incoming/<sha>/` and refuses to choose.
* **Words typed at a terminal:** `partial` when fewer than a majority of sites
  answered directly (no more than half), for `apply --execute`, `config pull`,
  `config recover`, `config publish` of a first version and `operator register`;
  `rollback` when the newest version is older than one this workstation has
  seen. An accepted `rollback` re-bases `.paisans/config-seen` to the head
  chosen once the command commits its change; a run that stops leaves it as it
  was. `log` asks for neither: it warns and lists. A dry run says what
  `--execute` will ask.
* **In `apply`:** `apply` gathers before its review and refuses a fork or a base
  older than the sites' newest version, naming `config pull`. With `--execute`
  it publishes the version it applies and each host records it; a host whose
  record holds a newer revision, or another version at the same revision, makes
  `apply` ask for `rollback`. It needs the standard file names beside
  `paisans-stack.yaml` (a `--secrets` naming another file is refused), an operator's key in
  the ssh-agent even for a dry run.

See *Every site keeps signed copies of the configuration* in `README.md` and
`docs/specs/2026-10-12-config-store.md`.

### `paisans operator register`

Gives an operator an anchor of their own: the point on their workstation from which
the sites' versions are trusted. An operator who has never published has signed
nothing, so `config pull` has nothing of theirs to start from.

```sh
paisans operator register                # the key and the head it would attest
paisans operator register --execute      # sign the attestation, record the key
paisans operator register --key ~/.ssh/id_ed25519.pub --execute
```

* **Alone:** always, once per operator per workstation, and again if the key
  changes.
* **Flags:** `--config`, `--key <path.pub>` (the key to sign with; the ssh-agent
  must hold its private half), `--root <sha>` (pin the first version you trust),
  `--execute`.
* **Hosts:** every site, as `config` does.
* **The key:** `--key`, else the one `.paisans/signing-key` records, else the
  one key in the ssh-agent that is listed under an operator of `paisans-stack.yaml`. It
  refuses when none matches, saying the key goes under your own entry in
  `operators` and another operator publishes that; when the agent holds more than one
  of your keys; and when it holds keys of more than one operator.
* **What it signs:** an attestation, a version with the head's three files and
  the head as its only parent. It signs only a head whose files equal the local
  files, since it says "I hold this version"; otherwise it shows the difference
  and writes nothing. With no files here and a root pinned, it attests the head
  the pin leads to and writes its files. A head your key signed already is
  pushed again, not attested again.
* **`--execute`:** signs, pushes the attestation to every site it reaches, and
  writes the key to `.paisans/signing-key`; `config publish` and `apply` sign
  with it from then on. It asks for `partial` when fewer than a majority of
  sites answered directly. Without it, nothing is signed or recorded, and no
  site is changed.

See *Every site keeps signed copies of the configuration* in `README.md`.

### `paisans operator magic-link`

Gives an operator declared under `operators` a one-time link that signs them in to
Pocket ID once, to register a passkey with: after a first link expired
unused, or a passkey was lost. It changes nothing else.

```sh
paisans operator magic-link alice              # finds alice's account, changes nothing
paisans operator magic-link alice --execute    # issues the link, prints it once
```

* **Alone:** always. apply gives a declared operator with no passkey a link on
  every run already; this is for one with no apply due, or who lost a passkey.
* **Flags:** `--app <pocket-id app>` (default: the only one), `--site`
  (default: the pinned site, or the site whose instance is active), `--ssh`,
  `--config`, `--secrets`, `--execute`.
* **Hosts:** the one site whose Pocket ID it calls.
* **`--execute`:** issues the link, valid a day and for one sign in, and has
  Pocket ID email one when it emails apply's. A name not under `operators`, an
  operator apply has not made yet, and a disabled account are refused.

See *`operator magic-link` gives a declared operator a fresh sign-in link* in
`README.md`.

### `paisans catalog`

Pins the catalogs of blueprints the deployment's apps may name, in
`paisans-stack.yaml`. None of its subcommands reaches a host.

```sh
paisans catalog add example/blueprints     # github.com/example/paisans-blueprints
paisans catalog add https://git.example.org/team/paisans-tools.git   # named team/tools
paisans catalog add notes https://github.com/example/paisans-blueprints   # a name of your own
paisans catalog update            # every catalog; or name one, with --tag
paisans catalog remove example/blueprints
paisans catalog list
```

* **`add [<name>] <url>` or `add <owner>/<name>`:** a catalog's repository is
  named `paisans-<name>`, and one that is not is refused. With a URL alone the
  catalog is named `<owner>/<name>`, the prefix left out; `<owner>/<name>` alone
  is `https://github.com/<owner>/paisans-<name>`. The command reads the newest
  release (or `--tag`) and the commit
  it names with `git ls-remote`. A release is a tag that is
  `vMAJOR.MINOR.PATCH` (or the same without the `v`), compared number by
  number; a pre-release such as `v2.0.0-rc1` is pinned only by naming it with
  `--tag`, and a tag that is not a version is ignored. It then fetches that
  commit's trees into the catalog cache, with no file contents but
  `catalog.yaml`, refuses a repository with no `catalog.yaml`,
  and writes the pin as `catalogs.<name>`. An app then names a blueprint as
  `<name>/<blueprint>`, Eg: `example/blueprints/journal`.
* **`update [<name>]`:** for each declared catalog, or the one named, moves the
  pin to the newest release, by the same rule (or `--tag`), when it names
  another commit, saying for each app using one of its blueprints what changes: images, port, sign-in, the
  gate's openings, storage, secrets. A release that drops a blueprint an app
  uses is refused, and nothing changes.
* **`remove <name>`:** refused while an app names one of its blueprints.
* **`list`:** the core catalog with the release and commit it reads, its
  newest one this toolkit can read or the one pinned under `catalogs.core`,
  then every declared catalog with its URL, tag and commit.

### `paisans blueprint`

```sh
paisans blueprint list                      # every name an app can use
paisans blueprint show example/blueprints/journal   # its blueprint.yaml at the pin
paisans blueprint lint ./blueprints/journal # for a catalog author; needs no paisans-stack.yaml
```

`lint` checks that `blueprint.yaml` is one this toolkit reads, that every other
file is a template, and that `compose.yaml.tmpl` and `caddy.snippet.tmpl` are
there. See *A blueprint can come from a catalog* in `README.md`.

## Hosts

### `paisans host prepare`

Takes a blank host to the state `apply` assumes: Docker with the compose
plugin, the WireGuard tools, a firewall following the site's roles, the login
user's authorized keys (every key of each operator the site's `ssh.keys` names),
and a watchdog on a data site. Installs only what is missing.

```
paisans host prepare --site home-a            # shows what it would do
paisans host prepare --site home-a --execute  # does it
paisans host prepare --all --execute          # every site, one at a time
```

* **Alone:** the whole apply runs it for every site, so a single run is for one
  host on its own: preparing a new machine before declaring the rest of a
  change, or reaching a host through `--ssh`.
* **Flags:** `--site <name>` or `--all`, `--config`, `--ssh` (with `--site`
  only), `--execute`. It reads no secrets.
* **`--all`:** every site the configuration declares, in its order, each
  exactly as `--site` prepares it, with its own review. It stops at the first
  site that fails; re-running it resumes there, since a prepared host plans
  nothing.
* **Hosts:** the one site's, or with `--all` each site's in turn. It runs the host check first, which refuses a
  Docker snap and a Docker older than engine 25.0.0 or compose 2.24.0,
  whatever its source.
* **Docker:** left as it is wherever `docker compose` works, from any source.
  Installed from Docker's own apt repository where it is absent. Where it is
  there and `docker compose` does not work, the packages in the way are named
  and refused, never removed.
* **Review:** the login user and the keys, against what the host last
  recorded in `/var/lib/paisans/granted.<token>.json`: each key added or
  changed is a `+` or `~` line under `review what paisans-stack.yaml grants`, each
  removed one a `-` line, at every verbosity.
* **`--execute`:** asks for `approve` at a terminal when the review adds or
  changes anything, before the claim; then installs and configures what the
  plan lists, and records the login and the keys once every step has
  succeeded. Without it, it probes and prints the plan and the review; a
  prepared host plans nothing.
* **Configuration store:** installs `git`, and plans under *configuration store*
  what the host lacks of a site's store: the bare repository at
  `/var/lib/paisans/config/<token>.git`, the peers file, the sync script, and
  the units `paisans-<token>-config-daemon`, `-config-sync` and
  `-config-sync.timer`. The daemon runs while the deployment's WireGuard unit
  does, so on a host whose mesh `apply` has not brought up it is enabled and
  not running. Every site's `host prepare` is also what makes the store
  `apply` publishes to, so `apply` refuses a version no site could take with
  this command named.

See *`host prepare` takes a blank host to an apply-able state*, *Docker is
judged by its version, not its source* and *`apply` and `host prepare` review
what `paisans-stack.yaml` grants* in `README.md`.

### `paisans host deployments`

Lists every deployment in the registry of the host `--ssh` names: id, token,
domain, site, roles and root, and a Caddy kept for `/srv/caddy.d`. Warns of any
deployment directory or labelled compose project no registry entry names.

```sh
paisans host deployments --ssh admin@203.0.113.9   # read only
```

* **Alone:** always, and without `paisans-stack.yaml`: to see what a shared host
  holds, or to find the id `site remove --id` takes.
* **Flags:** `--ssh <user@host[:port]>` (required), `--sudo`.
* **Hosts:** reads the one host named. Changes nothing.
* **`--execute`:** not taken.

See *`--id` cleans a host when `paisans-stack.yaml` is lost* in `README.md`.

### `paisans prune`

Lists one site's dangling Docker volumes, with size and top level entries, and
says which carry this deployment's label.

* **Alone:** always, for volumes that accumulated before `apply` removed its
  own.
* **Flags:** `--site <name>` (required), `--config`, `--ssh`, `--execute`.
* **Hosts:** the one site's.
* **`--execute`:** removes the volumes labelled with this deployment's id;
  every other volume is kept.

See *Every volume an image declares is mounted, and `apply` checks it* in
`README.md`.

## Applying

### `paisans apply` without `--site`

Every site, from `paisans-stack.yaml` to a running stack: the commands in this file
that it runs, in the order and under the conditions *`paisans apply` with no
`--site` converges the whole deployment* in `README.md` gives. In order: `init`
when it has work; `host prepare` on every host at once; a pull of every site's
images on every host at once, so no install waits on a download; `dns init`; the
founding applies, `site add`, the other sites, storage and pass two; `gateway
move`, when a site without the gateway role still serves as the gateway; the
`pocket id` step, which makes each operator a user of every running Pocket ID
app and issues a login link to one with no passkey; and, for each app behind the operator's own web
server, its site file written into the web server's `include_dir` and
`web-server check`'s checks. The version of the configuration it applies is
published once the `host prepare` steps have made every site's store and before
any other step changes a host (see *Every site keeps signed copies of the
configuration* in `README.md`). Each `apply --site` it runs that covers the
whole site, rather than holding apps back until their sign in clients exist,
ends by sweeping the old images its site's stacks used (see *`apply` prunes the
images it superseded* in `README.md`).

* **Alone:** this is the command an operator types; everything else is the
  exception.
* **Flags:** `--config`, `--secrets`, `--execute`, `--min-free`
  and `--keep-images` (handed to each `apply` step, below; the pull checks
  free space against `--min-free` too). `--ssh`,
  `--overwrite`, `--only` and `--recreate` are about one site and are refused
  without `--site`.
* **Hosts:** every site, and each app behind the operator's own web server
  from this machine, as a visitor would.
* **Review:** before the first step that
  changes a host (an `init` with work runs first: it writes only local
  files), one review of what `paisans-stack.yaml` grants on every site, under `review`, a line per site
  with each change under it: keys, images and the web server's directory.
  With `--execute`, when anything is added or changed, it asks once for
  `approve` at a terminal, for every site, and refuses without one; each step
  then applies what was approved, and refuses anything that changed since.
  The dry run shows it, asks nothing, and ends saying what `--execute` will
  ask for. Each step records its part once it has succeeded, and the run
  records each web server's directory after its web server step.
* **Configuration store:** before the review, every site's versions are gathered
  (under `configuration store`) and a fork, a base older than the sites' newest
  version, or a key no operator lists is refused, naming `config pull` or `config
  publish --merge`. With `--execute`, it asks for `partial` when fewer than a
  majority of sites answered directly. A dry run changes no site and starts no
  sync. After the approval, a host whose record holds a newer revision, or
  another version at the same revision, asks for `rollback`. `--execute` then
  publishes the version, signed with the recorded key, after the `host prepare`
  steps; a version that reaches no site refuses, naming `host prepare`. See
  `paisans config`.
* **Secrets:** each gateway's deployment record is read once, and each
  secret or recorded name `paisans-stack.yaml` no longer uses is
  warned about once, by key, under `secrets`, dry run or not. The `apply`
  steps it runs do not read the record or warn again. An `init` it runs warns
  in its place.
* **Mail:** before the first step, every app that sends mail logs in to its
  mail server from each site it runs on, under `mail`, and nothing is sent;
  a dry run whose `init` still has work tries them once `init` has run. A failure refuses the run with every host as it was,
  naming the app, where its settings are declared and the server's reply. The
  `apply` steps it runs do not try again. See *Mail settings are tried before
  they are used* in `README.md`.
* **`--execute`:** runs the steps, stopping at the first failure; run again,
  it reads live state again and carries on. It ends `Deployment complete.`, or,
  when an app failed its web server check or its site file step warned, ends
  on a warning naming each such app and exits 0, since every step ran and the
  web server is the operator's; with another failure as well, or a web server
  step that could not reach or write what it needs on the host, it exits 1.
  Each problem with the operator's web server is one `!` line with one set of
  instructions; an app whose site file step warned or failed has no check line
  of its own but with `-v`. Caddy's `import` of the include directory goes at
  the top level of the Caddyfile, after the global options block if it has
  one; apply refuses one above that block or inside a block, writing nothing,
  and says where to move it. A web server container without the include
  directory is diagnosed from its compose file, read only, with the command
  that recreates it, since a restart does not add a mount. A web server in a Docker container is validated and reloaded
  after its site file changes, restarted when the reload fails; one on the
  host itself is validated, and the warning gives the command to reload it. An `include_dir` it
  derives is written into `paisans-stack.yaml`. Without it, every step is checked
  as its own dry run, the web server checks are listed as `checked with
  --execute`, and the generated secrets the file lacks may be added to the
  local secrets file, as `init` would add them, which it says when it does.

### `paisans apply --site`

Compares one site's rendered artifacts with what is on its host, shows what
would change, and with `--execute` writes the files, brings the mesh up and
starts, restarts or recreates what changed.

* **Alone:** for one site's change on its own: an app only change, a file
  conflict to resolve, a stack to recreate, or a host reached through `--ssh`.
  The whole apply runs it for every site, in order.
* **Flags:**

  | Flag | Default | Meaning |
  |------|---------|---------|
  | `--site <name>` | required | the site to apply |
  | `--ssh <destination>` | the site's `ssh` section | as above |
  | `--only <stack>` | every stack | apply only this stack's files and actions (repeatable); an apply narrowed by it does not sweep |
  | `--overwrite <path>` | none | replace this conflicting file although it differs from the last apply's record (repeatable) |
  | `--recreate <stack>` | none | `up -d --force-recreate` every container of this stack, even if nothing changed (repeatable) |
  | `--min-free <size>` | `3G` | free space Docker's data root must have before a stack pulls an image |
  | `--keep-images` | off | leave the images this apply supersedes on the host, and skip the sweep |

  And `--config`, `--secrets`, `--execute`.
* **Hosts:** the site's own; an etcd member's apply also reads the other
  members, to compare the live membership. It reads every gateway's
  deployment record as the ssh user, since the record holds only names and
  is world readable.
* **Mail:** before the host check, dry run or not, every app the apply
  starts on the site (`--only` narrows it) logs in to its mail server from
  the site, through `--ssh` when given, under `mail`; nothing is sent. A
  failure refuses the apply before anything changes, naming the app, where
  its settings are declared and the server's reply.
* **Review:** the images every rendered `compose.yaml` of the site runs, and
  the web server's `include_dir`, against what the host last recorded, under
  `review what paisans-stack.yaml grants`; `--only` narrows the images to its
  stacks. With `--execute` it is asked for, as `approve` at a terminal, before
  the claim, when anything is added or changed; the images are recorded once
  the apply has succeeded, and the directory once the web server step has.
* **Configuration store:** gathered and checked before the review as the whole
  apply does, with `--secrets` refused when it names a file other than the
  `secrets.enc.yaml` beside `paisans-stack.yaml`. `--execute` publishes the version it
  loaded before its first change, and refuses when the site has no store, naming
  `host prepare`; the host's record of the version is written with its other
  parts. A step of the whole apply leaves this to the run, and refuses files
  edited since the run published.
* **`--execute`:** writes and acts, and an apply of the whole site ends by
  sweeping the old images of repositories its stacks render or have rendered,
  which no site of the deployment renders and no container uses (`sweep old
  images` in the plan). Without it, it reads and prints the plan. On a monitor
  with `managed_by: external`, `--execute` then runs the web server step for
  the apps that monitor serves, as the whole apply does at its end: each
  app's site file written into `include_dir` (an `include_dir` it derives
  written into `paisans-stack.yaml`), the configuration validated, a server in a
  container reloaded and the reload command given for one on the host, then
  `web-server check`'s checks, a line per app in a `web server` section. A
  certificate or sign in failure, which a web server still obtaining its
  certificate gives, is checked again after 2, 4, 8 and 16 seconds and then
  30, up to a minute in all, the line saying what it waits for; any other
  failure, such as DNS, is reported at once. It
  reaches the monitor through the same ssh as the rest of the apply,
  `--ssh` included. A problem with the operator's web server ends it on the
  whole apply's warning with exit status 0, in place of `Applied <n> files to
  <site>.`; one apply could not do on the host, such as reading or writing the
  record of its site files, ends it failed with exit status 1. Its dry run
  lists the check as `checked with --execute`. The applies the whole apply
  runs as its steps leave the step to it, so it runs once.

See *Rendered configuration is a build artifact* and the `apply` sections under
*Bootstrapping a site* in `README.md`.

## Sites

### `paisans site add`

Joins a data site to the running cluster in eight gated stages: preflight,
mesh, etcd (learners, then promoted), the Patroni replica, synchronous mode,
HAProxy, each existing replica's `patroni.env`, and each monitor site's uptime
stack. A witness joining in the same change, whether an existing site gaining the
role or a new host holding it, joins etcd first, as a learner, in the same
stage, and preflight checks a new witness host as it checks the new site.
Refuses a witness given on its own: it joins through the `site add` of the
data site joining with it.

```
paisans site add home-b             # every stage, its steps and its gate
paisans site add home-b --execute   # runs them, stopping at the first failed gate
```

* **Alone:** the whole apply runs it for each data site the running cluster
  has not admitted, and a witness joining beside one is joined by that run.
  Typed alone, it shows a join's stages and gates, or resumes a join.
* **Flags:** the site, as an argument before or after the flags; `--config`,
  `--secrets`, `--execute`. No `--ssh`, since it reaches several
  hosts.
* **Hosts:** every site.
* **Review:** stage 4 applies the new site's whole render, so `site add`
  reviews the new site's images as `apply --site` does: shown under `review
  what paisans-stack.yaml grants` before the stages, asked for as `approve` at a
  terminal with `--execute`, refused without one, and recorded once stage 4's
  gate has passed. As a step of the whole apply it asks nothing and refuses
  an image the run's review did not approve.
* **`--execute`:** runs the stages, stopping at the first gate that fails; a
  re-run plans only what differs and resumes.

See *`site add` is built for sites that dial each other* in `README.md`.

### `paisans preflight`

The read only checks `site add` makes first, on the site being added and on
every site already running, each printed as `ok`, `WARNING` or `REFUSED`. Exits
non zero on a refusal.

* **Alone:** always, before a join, to see what would stop it.
* **Flags:** `--site <new site>` (required), `--config`. No `--ssh`.
* **Hosts:** every site, read only.
* **`--execute`:** not taken.

See *Preflight* in `README.md`.

### `paisans site remove`

Takes a site out of the running deployment in gated stages: its data out, out
of the cluster, its host cleaned of what is provably this deployment's (on a
monitor behind the operator's own web server, the site files apply wrote into
its `include_dir` included, after which a web server in a Docker container is
validated and reloaded, and one on the host itself validated and left for the
operator to reload with the command it names), its DNS records deleted, and
its entry out of `paisans-stack.yaml`.

```sh
paisans site remove home-b                             # refusals, then every stage and its gate
paisans site remove home-b --execute                   # runs them, stopping at the first failed gate
paisans site remove home-b --delete-data --execute     # and deletes its data, after asking
paisans site remove home-b --host-gone --execute       # the host is never coming back
```

`--evict` takes out a site whose host still runs and that nobody else can
reach. It never reaches that host: the mesh is cut on every remaining site
first, then its DNS records go, Patroni fails over by itself, its etcd member
is removed and its Garage node is skipped as dead. A replacement declared in
the same edit of `paisans-stack.yaml` is left alone and brought up by `paisans
apply` afterwards; its secrets are generated first if the file lacks them.

```sh
paisans site remove home-b --evict             # refusals, then every stage, what it would ask, and what the host keeps
paisans site remove home-b --evict --execute   # asks for the site's name, and revoked, lose writes or lose objects when they apply
paisans apply                                  # brings up the replacement the configuration declares
```

`--force` cleans one host and nothing else, and `--id` does it with no
`paisans-stack.yaml`:

```sh
paisans site remove monitor-a --force --ssh admin@203.0.113.9                          # dry run
paisans site remove monitor-a --force --ssh admin@203.0.113.9 --execute --delete-data  # asks for the name
paisans site remove home-b --force --execute                                           # its declared host, asks for the name
paisans site remove home-b --force --ssh admin@198.51.100.4 --execute                  # a host it ran on before
paisans site remove --force --ssh admin@203.0.113.9 --id f2a9                          # dry run
paisans site remove --force --ssh admin@203.0.113.9 --id f2a9 --execute                # asks for the name
paisans site remove --force --ssh admin@203.0.113.9 --id f2a9 --execute --delete-data
```

* **Alone:** always. The whole apply never takes a site out; it warns of one
  that is deployed and no longer declared, with this command.
* **Flags:**

  | Flag | Default | Meaning |
  |------|---------|---------|
  | `--host-gone` | off | run every stage but cleaning the host, and reach it not at all |
  | `--evict` | off | the host still runs and nobody else can reach it: cut it out of the mesh first and take it out of the cluster without reaching it; implies `--host-gone`, and refuses `--force`, `--ssh`, `--id` and `--delete-data` |
  | `--delete-data` | off | also delete this deployment's data on the host; asks for the site's name at a terminal |
  | `--force` | off | clean this deployment off one host and nothing else; no cluster stage, and neither file edited |
  | `--ssh <user@host[:port]>` | the site's `ssh` section | with `--force`: the host to clean |
  | `--id <id or token>` | none | with `--force` and `--ssh`, and no `paisans-stack.yaml`: the deployment to clean, as `host deployments` lists it |

  And the site as an argument, `--config`, `--secrets`, `--execute`.
* **Hosts:** every site, and the DNS provider for the removed site's records;
  `--force` reaches the one host only and leaves DNS alone. `--host-gone`
  reaches the removed site's host not at all, so it says the site files apply
  wrote there may still be in the web server's `include_dir`.
* **`--execute`:** runs the stages, stopping at the first gate that fails. Stage
  3 deletes the record of what the deployment granted on the host,
  `/var/lib/paisans/granted.<token>.json`, with its registry entry. It deletes
  the host's configuration store
  too, the repository and the files beside it; the other sites still hold every
  version.
  With `--evict` it needs a terminal, and asks, in order: `revoked` for a
  gateway, once its DNS token is revoked at the provider and a new one set
  with `secrets set`; the site's name; `lose writes` when the leader has no
  streaming `Sync Standby`; `lose objects` when its Garage node's objects have
  no other copy. No flag answers them.

See *`site remove` takes a site out* and *Evicting a site: `--evict`* in
`README.md`, and [guides/when-an-operator-leaves.md](guides/when-an-operator-leaves.md)
for the order around an eviction.

### `paisans mesh rekey`

Replaces sites' WireGuard keys, one site at a time: a new key written to the
secrets file as the site's next key, every peer's mesh file and then the
site's own synced with it, a handshake under it from every peer, etcd and
Patroni healthy, then the next key promoted. A stopped run resumes where it
stopped; `apply` refuses until it finishes, since an apply renders the old key.

```sh
paisans mesh rekey home-a              # dry run: where each site's rekey stands, and every stage
paisans mesh rekey home-a vm --execute # home-a, then vm
paisans mesh rekey --all --execute     # every site, one at a time
```

* **Alone:** always. Nothing else rotates a WireGuard key.
* **Flags:** `--all` (every site the configuration declares, in its order),
  and the sites as arguments, `--config`, `--secrets`, `--execute`.
* **Hosts:** every site, since each is a peer of some site rekeyed. The
  secrets file is read and written on this machine only.
* **`--execute`:** rekeys each site in four gated stages, and stops at the first
  gate that fails. Refuses before anything changes when etcd or Patroni is
  unhealthy, Patroni is paused by something else, a site does not answer over
  ssh, or a `secrets rotate` is unfinished, since its applies would render the
  old value of the secret being rotated. Around the swap of a site other sites
  relay through, Patroni is paused and then resumed once the cluster is healthy
  again.

See *Rekeying the mesh: `mesh rekey`* in `README.md`.

### `paisans failover test`

Switches the Patroni primary to another data site and back, checking the
cluster and every app before and after each switch. Interrupts writes briefly,
twice.

* **Alone:** always, to prove failover works before an outage does.
* **Flags:** `--config`, `--execute`.
* **Hosts:** every site, and every app's hostname from this machine.
* **`--execute`:** switches. Without it, it checks and prints both switchover
  commands.

See *`failover test`: a switchover on purpose* in `README.md`.

### `paisans doctor`

Reports what is stuck and how to recover: unreachable sites, etcd quorum and
version, the Patroni leader and why a replica will not promote, containers
that are down or unhealthy, the settings a gated app must enforce, Pocket ID's active
instance, clocks, and what is left over.

* **Alone:** always, in an outage or after one.
* **Flags:** `--site <name>` (repeatable; default every site), `--config`,
  `--execute`.
* **Hosts:** every site, or the named ones, read only.
* **`--execute`:** removes from `paisans-stack.yaml` each `config:` key that only
  repeats a value gating renders, before loading it, and records the `ssh.user`
  of a site it logs in to as the deployment's own user. It changes no host; the
  recoveries it prints are for a human to run.

See *`paisans doctor` reports what is stuck and how to recover* in `README.md`.

### `paisans version`

Prints the toolkit version the binary was built from and, in parentheses, its
commit, with `-dirty` when the tree had uncommitted changes:
`paisans 0.1.0 (519299d5cb00)`. A build with no commit recorded prints the
version alone. The version is the constant in `internal/version`, so a build
between releases prints the last release cut on its branch. A blueprint's
`requires_toolkit` is checked against it.

* **Alone:** always.
* **Flags:** none of its own. `paisans --version` is the same command.
* **Hosts:** none.

## Object storage

### `paisans storage init`

Provisions object storage on one site: each app's key and bucket, and the
layout when it is the only Garage site. An app whose `storage` is `local`
keeps its files on its own disk and is skipped: no key, no bucket.

* **Alone:** the whole apply runs it after every site is up when
  `storage.garage.sites` lists one site. Typed alone, it provisions an app's
  bucket after an `apply --site`.
* **Flags:** `--site <name>` (required), `--config`, `--secrets`, `--ssh`,
  `--execute`.
* **Hosts:** the one site's Garage.
* **`--execute`:** creates what is missing.

See *`storage init` provisions object storage* in `README.md`.

### `paisans storage add`

Joins every site in `storage.garage.sites` into one Garage cluster in gated
stages: connect, layout, sync, provision, media routes, a smoke test. An app
whose `storage` is `local` is skipped: the provision and media route stages
give it no key, no bucket and no media route.

* **Alone:** the whole apply runs it in place of `storage init` when there are
  several Garage sites. Typed alone, it is for its own flags, which the whole
  apply does not pass on, and for resuming a stage waiting on Garage.
* **Flags:**

  | Flag | Default | Meaning |
  |------|---------|---------|
  | `--change-replication` | off | allow Garage's reset for another replication factor: every node stopped, the cluster laid out again; media is unavailable for about a minute |
  | `--wait <duration>` | `0` | how long a stage waiting on Garage polls before exiting; by default it reads once and exits with status 75 |
  | `--stop-test` | off | in the smoke stage, stop Garage on the last listed site and prove reads and an upload survive; refused unless uploads survive one node down |

  And `--config`, `--secrets`, `--execute`. It takes no site.
* **Hosts:** every Garage site.
* **`--execute`:** runs the stages, stopping at a gate that fails or waits.

See *More than one Garage site: `storage add`* in `README.md`.

### `paisans storage rotate-key`

Replaces one app's S3 key in gated stages: the new pair into the secrets, the
new key imported and granted, the app applied alone on every site running it,
a probe with the new key, and only then the old key deleted.

* **Alone:** always, when a key may have leaked.
* **Flags:** `--app <name>` (required), `--config`, `--secrets`, `--execute`.
* **Hosts:** the Garage sites and every site running the app.
* **`--execute`:** runs the stages; a re-run resumes from the secrets file.

See *Replacing an app's key: `storage rotate-key`* in `README.md`.

## DNS

### `paisans dns init`

Creates the public DNS records the configuration implies, at the provider
`acme.provider` names. Creates only what is missing, never updates or deletes,
and refuses if a record conflicts. An app whose `storage` is `local` has no
media hostname, so it gets no media record.

```sh
paisans dns init                # shows what it would create
paisans dns init --execute      # creates it
```

* **Alone:** the whole apply runs it before any site is applied. Typed alone,
  it is for a change that only adds names.
* **Flags:** `--config`, `--secrets`, `--execute`.
* **Hosts:** none; the DNS provider's API only.
* **`--execute`:** creates the records listed.

See *`dns init` creates the records a deployment needs* in `README.md`.

### `paisans dns prune`

Deletes the records `dns init` created that the configuration no longer
implies, and lists every other record it marked, with why it is kept.

```sh
paisans dns prune               # lists remove and keep, with reasons
paisans dns prune --execute     # deletes the removes, then confirms each is gone
```

* **Alone:** always, after a name has left the configuration.
* **Flags:** `--name <fqdn>` (repeatable: vouch that this name, outside
  `community.domain` and no longer configured, was this deployment's),
  `--config`, `--secrets`, `--execute`.
* **Hosts:** none; the DNS provider's API only.
* **`--execute`:** deletes, then lists the zone again to confirm each delete.

## Gateway

### `paisans gateway move`

Moves the gateway role to the site `paisans-stack.yaml` gives it, from the site that
still serves as the gateway, as an overlap: both gateways serve while the new
one is checked, its address records are added beside the old one's, the old
one's are deleted, their TTL is waited out, and only then is the old Caddy
stopped. It takes no site: the new gateway is the configuration's, and the old
one is found from what it still runs, holds and is pointed at. With no old
gateway found, it says there is nothing to move.

```sh
paisans gateway move              # the old gateway it found, then every stage
paisans gateway move --execute    # record, serve, dns add, dns remove, wait, stop
```

* **Alone:** when the new gateway has already been applied. The whole apply
  runs it last, after pass two, whenever a site without the gateway role still
  holds a gateway.
* **Flags:** `--config`, `--secrets`, `--execute`.
* **Hosts:** reads every site without the gateway role, to find the old
  gateway; writes only to the old and new gateways' hosts. Also the DNS
  provider's API, and each hostname the gateway serves over HTTPS from this
  machine, through the new gateway's own public address.
* **`--execute`:** runs the six stages in order, each ending at a gate that
  stops the run with its evidence; a re-run resumes at the first stage with
  anything left to do. It deletes only the old gateway's address records
  that `dns init` made, once the new gateway passed its check on the same run,
  and does not stop the old Caddy while the host's `/srv/caddy.d` holds a site
  file. Without it, it shows the old gateway it found and every stage.

See *Moving the gateway* in `README.md`.

## Apps

### `paisans oidc client create`

Creates an app's client at the deployment's Pocket ID, with the groups the app
reads, and records its ID and secret in the secrets file. The secret is never
printed.

```sh
paisans oidc client create --app talk             # shows what it would do
paisans oidc client create --app talk --execute   # does it
paisans oidc client create --app talk --rotate-secret --execute
```

* **Alone:** `apply` runs the same step for every app it starts, so it is typed
  alone to rotate a secret, or to create or re-record a client `apply`
  refused to replace.
* **Flags:** `--app <name>` (required), `--rotate-secret` (off: add a new secret
  even when the recorded one is live), `--site` (default: the pinned site, or
  the apps site whose instance is active), `--ssh`, `--config`, `--secrets`,
  `--execute`.
* **Hosts:** the one site whose Pocket ID it calls.
* **`--execute`:** creates and records.

See *`oidc client create` makes an app's client at Pocket ID* in `README.md`.

### `paisans app remove`

Takes an app that has left `paisans-stack.yaml` off every host, once a whole apply
on each site has marked its files left over. For an app behind the operator's
own web server it also deletes the site file apply wrote into the web
server's `include_dir`, then validates that web server and reloads it when it
runs in a Docker container. One on the host itself is the operator's to
reload: what is left at the end gives the command.

```sh
paisans app remove docs                              # shows what it would do
paisans app remove docs --execute                    # does it, and keeps its data
paisans app remove docs --delete-data --execute      # and deletes its data, after asking
```

* **Alone:** always. The whole apply never takes an app out; it lists the
  leftovers and names this command.
* **Flags:** the app, as an argument before or after the flags;
  `--delete-data` (off: also delete its database, bucket and keys, volumes and
  data directories, after the app's name is typed at a terminal), `--config`,
  `--secrets`, `--execute`.
* **Hosts:** every site that holds the app, and its Pocket ID.
* **`--execute`:** stops, removes and deletes.

See *`app remove` takes an app off its hosts* in `README.md`.

### `paisans web-server show` and `paisans web-server check`

For an app pinned to a monitor site behind the operator's own web server.
`show` prints what that web server must do, with the snippet for the web
server it finds in front of the app, or for Caddy, nginx and Apache when it
finds none. `check` checks, from this machine as a visitor would, that the
hostname resolves to the monitor, that `/healthz` answers with a valid
certificate, that sign in redirects with an https callback, and that the
published port is closed from outside; when a check fails, it names the web
server it finds there.

```sh
paisans web-server show  --app health   # the hand-off sheet, for the web server it finds
paisans web-server check --app health   # from your machine, as a visitor would
```

* **Alone:** `show` always, to hand the snippet to whoever runs the web
  server. The whole apply writes that snippet into the web server's
  `include_dir` and runs `check`'s checks after every step, waiting up to a
  minute on a certificate or sign in failure and reporting a DNS or routing
  failure at once, so `check` is typed alone to check again once the web
  server is fixed. Typed alone it checks once.
* **Flags:** `--app <name>` (required), `--config`. It reads as the ssh user.
* **Hosts:** in `managed_by` `external`, the monitor over ssh, reading only:
  `show` always, `check` only when a check fails. `check` otherwise looks at
  the public hostname only.
* **`--execute`:** not taken. Neither changes anything. `check` exits 1 on any
  `FAIL`.

See *The monitor has a site of its own* in `README.md`, and
[`docs/guides/behind-your-own-web-server.md`](guides/behind-your-own-web-server.md).
