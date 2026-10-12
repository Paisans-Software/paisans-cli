# Commands

One entry per `paisans` command: what it does, when an operator runs it on its
own, its flags with their defaults, whether it reaches a host, and what it
does without and with `--execute`. `paisans help` prints the same list in
short, from `cmd/paisans/main.go`.

This file holds the mechanics. Why each command behaves as it does is in
`README.md`, and each entry names the section there that carries the
reasoning.

## Most of the time, one command

`paisans apply` without `--site` takes `paisans.yaml` to a complete, running
stack. It runs `init`, `host prepare`, `dns init`, `apply --site`, `site add`,
`storage init`, `storage add` and, when the gateway role has moved, `gateway
move` itself, in the order the deployment needs them, each exactly as it runs when typed, pulls every site's images on every
host at once before the first of them installs anything, and ends by running
`web-server check`'s checks for each app behind the operator's own web server
(see *`paisans apply` with no `--site` converges the whole deployment* in
`README.md`). So the usual way to make any change is to edit `paisans.yaml`
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
| `--config <path>` | `paisans.yaml` | every command except `host deployments` | the deployment declaration |
| `--secrets <path>` | `secrets.enc.yaml` beside the config | every command that reads secrets | the sops encrypted secrets file |
| `--execute` | off | every command that changes something, as each entry says | without it, the command plans and prints and changes nothing |
| `--sudo` | on | every command that reaches a host over ssh, except `app admin create` and `oidc client create`, whose calls need no root | run remote commands through sudo; `--sudo=false` skips it (see *A sudo that wants a password is asked for once, on a terminal* in `README.md`) |
| `--ssh <destination>` | the site's `ssh` section | commands about one site's host | used verbatim in place of the site's `ssh` section, whose user, host, port and keys are then ignored (see *`--ssh` replaces the whole section* in `README.md`) |

With `--execute`, a command first claims each host it writes to in
`/var/lib/paisans/registry.json`, and refuses if another deployment there
holds this one's token, WireGuard interface or listen port, or an overlapping
mesh subnet (see *A deployment has an id, and a host knows every deployment on
it* in `README.md`).

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

* **Alone:** while editing `paisans.yaml`, before anything else reads it.
  Every command that loads the declaration runs the same checks, so this is
  the one that does nothing else.
* **Flags:** `--config`.
* **Hosts:** none. It reads the file and nothing else.
* **`--execute`:** not taken.

### `paisans init`

Gives the declaration an `id` if it has none, settles `mesh.subnet` against
every site while the deployment is on none of them, generates every secret the
declaration needs into the secrets file (filling only what is missing), and
says what is still owed from elsewhere.

* **Alone:** the whole apply runs it whenever it has work, so it is typed on
  its own to see what is owed before any host is changed, or to generate the
  secrets for a site or app just declared without applying anything.
* **Flags:** `--config`, `--secrets`, `--sudo` (on: each site's registry is
  root's).
* **Hosts:** reads every site, to choose the mesh subnet. Changes none.
* **`--execute`:** not taken. It writes local files only: the `id` and the
  subnet into `paisans.yaml`, the secrets into the secrets file.

See *Three kinds of secret* and *A deployment has an id, and a host knows every
deployment on it* in `README.md`.

### `paisans render`

Validates, then writes every site's artifacts under a local directory.

* **Alone:** to read what `apply` would push, file by file. `apply` renders
  for itself and never runs this as a step.
* **Flags:** `--config`, `--secrets`, `--out <dir>` (required).
* **Hosts:** none.
* **`--execute`:** not taken. It writes under `--out` and stops.

### `paisans secrets set`

Reads one value from stdin and writes it into the encrypted secrets file under
a dotted key, printing only the key's name. For credentials issued elsewhere,
such as the DNS provider's token.

```
security find-generic-password -s acme -w | paisans secrets set external.acme_dns_token
```

* **Alone:** always. The whole apply names it, in a note, for each credential
  still owed.
* **Flags:** the key, as the first argument; `--config`, `--secrets`.
* **Hosts:** none.
* **`--execute`:** not taken. It writes the local file at once, and refuses a
  terminal on stdin.

See *Three kinds of secret* in `README.md`.

### `paisans secrets prune`

Lists the secrets that name something `paisans.yaml` no longer declares, by key
and never by value: a removed site's WireGuard key, a removed app's passwords
and sign in client, a Pocket ID group nothing names.

```sh
paisans secrets prune             # lists them
paisans secrets prune --execute   # removes them
paisans secrets prune --without-record --execute   # no gateway to read; asks for the word prune
```

* **Alone:** always, after a site or an app has been removed.
* **Flags:** `--config`, `--secrets`, `--execute`, `--without-record` (off:
  trust `paisans.yaml` alone when no gateway's deployment record can be read;
  with `--execute` it asks for the word `prune` at a terminal), `--sudo`.
* **Hosts:** reads each gateway's deployment record, as the ssh user since it
  holds only names and is world readable, and through sudo only for one
  written with other modes. Changes none.
* **`--execute`:** removes them and writes the local secrets file, encrypted to
  the same recipients.

## Hosts

### `paisans host prepare`

Takes a blank host to the state `apply` assumes: Docker with the compose
plugin, the WireGuard tools, a firewall following the site's roles, the login
user's authorized keys (every key of each admin the site's `ssh.keys` names),
and a watchdog on a data site. Installs only what is missing.

```
paisans host prepare --site home-a            # shows what it would do
paisans host prepare --site home-a --execute  # does it
```

* **Alone:** the whole apply runs it for every site, so a single run is for one
  host on its own: preparing a new machine before declaring the rest of a
  change, or reaching a host through `--ssh`.
* **Flags:** `--site <name>` (required), `--config`, `--ssh`, `--execute`,
  `--sudo`. It reads no secrets.
* **Hosts:** the one site's. It runs the host check first, which refuses a
  Docker snap and a Docker older than engine 25.0.0 or compose 2.24.0,
  whatever its source.
* **Docker:** left as it is wherever `docker compose` works, from any source.
  Installed from Docker's own apt repository where it is absent. Where it is
  there and `docker compose` does not work, the packages in the way are named
  and refused, never removed.
* **`--execute`:** installs and configures what the plan lists. Without it, it
  probes and prints the plan; a prepared host plans nothing.

See *`host prepare` takes a blank host to an apply-able state* and *Docker is
judged by its version, not its source* in `README.md`.

### `paisans host deployments`

Lists every deployment in the registry of the host `--ssh` names: id, token,
domain, site, roles and root, and a Caddy kept for `/srv/caddy.d`. Warns of any
deployment directory or labelled compose project no registry entry names.

```sh
paisans host deployments --ssh admin@203.0.113.9   # read only
```

* **Alone:** always, and without `paisans.yaml`: to see what a shared host
  holds, or to find the id `site remove --id` takes.
* **Flags:** `--ssh <user@host[:port]>` (required), `--sudo`.
* **Hosts:** reads the one host named. Changes nothing.
* **`--execute`:** not taken.

See *`--id` cleans a host when `paisans.yaml` is lost* in `README.md`.

### `paisans prune`

Lists one site's dangling Docker volumes, with size and top level entries, and
says which carry this deployment's label.

* **Alone:** always, for volumes that accumulated before `apply` removed its
  own.
* **Flags:** `--site <name>` (required), `--config`, `--ssh`, `--execute`,
  `--sudo`.
* **Hosts:** the one site's.
* **`--execute`:** removes the volumes labelled with this deployment's id;
  every other volume is kept.

See *Every volume an image declares is mounted, and `apply` checks it* in
`README.md`.

## Applying

### `paisans apply` without `--site`

Every site, from `paisans.yaml` to a running stack: the commands in this file
that it runs, in the order and under the conditions *`paisans apply` with no
`--site` converges the whole deployment* in `README.md` gives. In order:
`init` when it has work; `host prepare` on every host at once; a pull of every
site's images on every host at once, so no install waits on a download; `dns
init`; the founding applies, `site add`, the other sites, storage and pass
two; `gateway move`, when a site without the gateway role still serves as
the gateway; a `pocket id` check for an admin; and, for each app behind the
operator's own web server, its site file written into the web server's
`include_dir` and `web-server check`'s checks. Each `apply --site` it runs that
covers the whole site, rather than holding apps back until their sign in
clients exist, ends by sweeping the old images its site's stacks used (see
*`apply` prunes the images it superseded* in `README.md`).

* **Alone:** this is the command an operator types; everything else is the
  exception.
* **Flags:** `--config`, `--secrets`, `--execute`, `--sudo`, `--min-free`
  and `--keep-images` (handed to each `apply` step, below; the pull checks
  free space against `--min-free` too). `--ssh`,
  `--overwrite`, `--only` and `--recreate` are about one site and are refused
  without `--site`.
* **Hosts:** every site, and each app behind the operator's own web server
  from this machine, as a visitor would. Every host's sudo password is asked
  for before the first step.
* **Secrets:** each gateway's deployment record is read once, after the sudo
  passwords, and each secret or recorded name `paisans.yaml` no longer uses is
  warned about once, by key, under `secrets`, dry run or not. The `apply`
  steps it runs do not read the record or warn again. An `init` it runs warns
  in its place.
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
  derives is written into `paisans.yaml`. Without it, every step is checked
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

  And `--config`, `--secrets`, `--execute`, `--sudo`.
* **Hosts:** the site's own; an etcd member's apply also reads the other
  members, to compare the live membership. It reads every gateway's
  deployment record as the ssh user, without sudo, since the record holds
  only names and is world readable, so applying a site that is not a gateway
  asks for no gateway's sudo password.
* **`--execute`:** writes and acts, and an apply of the whole site ends by
  sweeping the old images of repositories its stacks render or have rendered,
  which no site of the deployment renders and no container uses (`sweep old
  images` in the plan). Without it, it reads and prints the plan. On a monitor
  with `managed_by: external`, `--execute` then runs the web server step for
  the apps that monitor serves, as the whole apply does at its end: each
  app's site file written into `include_dir` (an `include_dir` it derives
  written into `paisans.yaml`), the configuration validated, a server in a
  container reloaded and the reload command given for one on the host, then
  `web-server check`'s checks, a line per app in a `web server` section. A
  certificate or sign in failure, which a web server still obtaining its
  certificate gives, is checked again after 2, 4, 8 and 16 seconds and then
  30, up to a minute in all, the line saying what it waits for; any other
  failure, such as DNS, is reported at once. It
  reaches the monitor through the same ssh and sudo as the rest of the apply,
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
  `--secrets`, `--execute`, `--sudo`. No `--ssh`, since it reaches several
  hosts.
* **Hosts:** every site.
* **`--execute`:** runs the stages, stopping at the first gate that fails; a
  re-run plans only what differs and resumes.

See *`site add` is built for sites that dial each other* in `README.md`.

### `paisans preflight`

The read only checks `site add` makes first, on the site being added and on
every site already running, each printed as `ok`, `WARNING` or `REFUSED`. Exits
non zero on a refusal.

* **Alone:** always, before a join, to see what would stop it.
* **Flags:** `--site <new site>` (required), `--config`, `--sudo`. No `--ssh`.
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
its entry out of `paisans.yaml`.

```sh
paisans site remove home-b                             # refusals, then every stage and its gate
paisans site remove home-b --execute                   # runs them, stopping at the first failed gate
paisans site remove home-b --delete-data --execute     # and deletes its data, after asking
paisans site remove home-b --host-gone --execute       # the host is never coming back
```

`--force` cleans one host and nothing else, and `--id` does it with no
`paisans.yaml`:

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
  | `--delete-data` | off | also delete this deployment's data on the host; asks for the site's name at a terminal |
  | `--force` | off | clean this deployment off one host and nothing else; no cluster stage, and neither file edited |
  | `--ssh <user@host[:port]>` | the site's `ssh` section | with `--force`: the host to clean |
  | `--id <id or token>` | none | with `--force` and `--ssh`, and no `paisans.yaml`: the deployment to clean, as `host deployments` lists it |

  And the site as an argument, `--config`, `--secrets`, `--execute`, `--sudo`.
* **Hosts:** every site, and the DNS provider for the removed site's records;
  `--force` reaches the one host only and leaves DNS alone. `--host-gone`
  reaches the removed site's host not at all, so it says the site files apply
  wrote there may still be in the web server's `include_dir`.
* **`--execute`:** runs the stages, stopping at the first gate that fails.

See *`site remove` takes a site out* in `README.md`.

### `paisans failover test`

Switches the Patroni primary to another data site and back, checking the
cluster and every app before and after each switch. Interrupts writes briefly,
twice.

* **Alone:** always, to prove failover works before an outage does.
* **Flags:** `--config`, `--execute`, `--sudo`.
* **Hosts:** every site, and every app's hostname from this machine.
* **`--execute`:** switches. Without it, it checks and prints both switchover
  commands.

See *`failover test`: a switchover on purpose* in `README.md`.

### `paisans doctor`

Reports what is stuck and how to recover: unreachable sites, etcd quorum and
version, the Patroni leader and why a replica will not promote, containers
that are down, the settings a gated app must enforce, Pocket ID's active
instance, clocks, and what is left over.

* **Alone:** always, in an outage or after one.
* **Flags:** `--site <name>` (repeatable; default every site), `--config`,
  `--sudo`, `--execute`.
* **Hosts:** every site, or the named ones, read only.
* **`--execute`:** removes from `paisans.yaml` each `config:` key that only
  repeats a value gating renders, before loading it. It changes no host; the
  recoveries it prints are for a human to run.

See *`paisans doctor` reports what is stuck and how to recover* in `README.md`.

## Object storage

### `paisans storage init`

Provisions object storage on one site: each app's key and bucket, and the
layout when it is the only Garage site. An app whose `storage` is `local`
keeps its files on its own disk and is skipped: no key, no bucket.

* **Alone:** the whole apply runs it after every site is up when
  `storage.garage.sites` lists one site. Typed alone, it provisions an app's
  bucket after an `apply --site`.
* **Flags:** `--site <name>` (required), `--config`, `--secrets`, `--ssh`,
  `--execute`, `--sudo`.
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

  And `--config`, `--secrets`, `--execute`, `--sudo`. It takes no site.
* **Hosts:** every Garage site.
* **`--execute`:** runs the stages, stopping at a gate that fails or waits.

See *More than one Garage site: `storage add`* in `README.md`.

### `paisans storage rotate-key`

Replaces one app's S3 key in gated stages: the new pair into the secrets, the
new key imported and granted, the app applied alone on every site running it,
a probe with the new key, and only then the old key deleted.

* **Alone:** always, when a key may have leaked.
* **Flags:** `--app <name>` (required), `--config`, `--secrets`, `--execute`,
  `--sudo`.
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

Moves the gateway role to the site `paisans.yaml` gives it, from the site that
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
* **Flags:** `--config`, `--secrets`, `--execute`, `--sudo` (on: run remote
  commands through sudo).
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

### `paisans app admin create`

Makes sure a user exists, is verified, is an administrator of Pocket ID, and
is in every admin group the apps signing in through it read. A created account
gets a one-time login link, printed once.

```sh
paisans app admin create --app auth --username founder \
    --email founder@example.org --first-name Fern     # shows what it would do
paisans app admin create --app auth --username founder \
    --email founder@example.org --first-name Fern --execute
```

* **Alone:** always. The whole apply names it for a Pocket ID app that has no
  administrator yet.
* **Flags:** `--app <pocket-id app>`, `--username`, `--email` (needed to create
  the account), `--first-name` (default: the username), `--last-name`,
  `--login-link` (off: issue a fresh link for an account that exists),
  `--site` (default: the pinned site, or the site whose instance is active),
  `--ssh`, `--config`, `--secrets`, `--execute`.
* **Hosts:** the one site whose Pocket ID it calls.
* **`--execute`:** creates, verifies and grants. It refuses anything piped to
  it, since there is no password.

See *`app admin create` makes an app's first administrator* in `README.md`.

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

Takes an app that has left `paisans.yaml` off every host, once a whole apply
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
  `--secrets`, `--execute`, `--sudo`.
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
paisans web-server show  --app status   # the hand-off sheet, for the web server it finds
paisans web-server check --app status   # from your machine, as a visitor would
```

* **Alone:** `show` always, to hand the snippet to whoever runs the web
  server. The whole apply writes that snippet into the web server's
  `include_dir` and runs `check`'s checks after every step, waiting up to a
  minute on a certificate or sign in failure and reporting a DNS or routing
  failure at once, so `check` is typed alone to check again once the web
  server is fixed. Typed alone it checks once.
* **Flags:** `--app <name>` (required), `--config`, `--sudo` (on: read the web
  server through sudo where the ssh user cannot; `--sudo=false` reads as the
  ssh user alone).
* **Hosts:** in `managed_by` `external`, the monitor over ssh, reading only:
  `show` always, `check` only when a check fails. `check` otherwise looks at
  the public hostname only.
* **`--execute`:** not taken. Neither changes anything. `check` exits 1 on any
  `FAIL`.

See *The monitor has a site of its own* in `README.md`, and
[`docs/guides/behind-your-own-web-server.md`](guides/behind-your-own-web-server.md).
