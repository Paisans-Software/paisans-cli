# Development

The toolkit is a single Go binary, `cmd/paisans`. A second, `cmd/admin-reconciler`,
is built into the image that runs beside Pocket ID, so no operator builds it.
Contributors need a Go toolchain; operators need nothing.

## Build

```
go build ./cmd/paisans
```

Go 1.25.8 or newer, the `go` line in `go.mod`. There is no code generation step
and no Makefile.

## Run

[`docs/commands.md`](commands.md) is the operator's reference for every
command: what it does, when it is run on its own rather than through the whole
`apply`, its flags and defaults, and what it changes. This section is how the
code behind each one is laid out.

validate and render touch nothing outside the working directory, and create
writes one new file there. init writes only local files, and reads every site over ssh to settle the mesh subnet.
`host prepare`, `apply`, `prune`, `site add`, `site remove`, `mesh rekey`,
`secrets rotate`, `storage init`, `storage add`, `storage rotate-key`, `app
admin create`, `app remove` and `oidc client create` reach a machine, and each
changes it only with `--execute`. `preflight` and `doctor`
reach every site and never change one, and `host deployments` reads the one
host `--ssh` names, with no `paisans-stack.yaml`, and changes nothing. `host allow`
and `host disallow` change only a site's host's key allowlist,
`/etc/paisans/ssh-allow.<user>`, which is the host owner's, so they claim
nothing; with `--execute` they ask for the site's name at a terminal.
`internal/hostprep/allow.go` holds the list's format, the refusal `host
prepare` gives a key it lacks, and the locked write; `cmd/paisans/hostallow.go`
the commands. `secrets
prune` reads each gateway's deployment record and rewrites only the local
secrets file, only with `--execute`. `failover test` changes which site
is primary, only with `--execute`. `dns init` and `dns prune` reach no
machine, only the DNS provider's API, and change it only with `--execute`;
`site remove` also deletes there the A and AAAA records `dns init` made for the
removed site. `site remove --evict`, `mesh rekey` and `secrets rotate` also
write the local secrets file: the replacement's generated secrets, a site's
next and promoted WireGuard key, the sites a `mesh rekey --all` run has left,
and a secret's next and promoted value with the secrets a `secrets rotate
--all` run has left.
`web-server show` and `web-server check` change nothing: `check` looks at a
monitor's public hostname as a visitor would, and in `managed_by` `external`
both read the monitor's web server over ssh, `show` always and `check` when a
check fails.

`config publish`, `config pull`, `config log`, `config recover` and `admin
register` read every site's configuration store over ssh and change a host only
with `--execute`, and then only by starting each site's sync and, for `publish`
and `register`, adding a version to its store; they claim nothing. `apply`
gathers and publishes through the same code. The commands are in
`cmd/paisans/config.go` (`publish`, `pull`, `log`, `recover`),
`cmd/paisans/config_merge.go` (the secrets merge on a pull),
`cmd/paisans/config_apply.go` (`apply`'s gather, publish and witness check) and
`cmd/paisans/adminregister.go`. The rules that decide which version to trust are
`internal/configstore/trust.go`, and are written against built histories, not
against a host, so they are tested without one. `internal/grant` holds the
version a host last applied in `config` of its record.

Each command that changes a host first claims it in the host registry,
`/var/lib/paisans/registry.json`, and its dry run only reads that registry;
`cmd/paisans/claim.go` is the one place that happens, and `internal/registry`
holds the decision and the locked shell command. Every name and path those
commands use comes from `internal/deployment`, derived from the `id` in
`paisans-stack.yaml`. README.md "A deployment has an id, and a host knows every
deployment on it" has the reasoning.

The registry entry also carries the deployment's WireGuard interface
(`psns-<token>`), its listen port (the site's endpoint port) and its mesh
subnet, and a claim refuses another deployment's same interface, same port or
overlapping subnet. It carries the site's roles too (`roles`, sorted and
comma joined so the awk matches one with `index()`), and a claim refuses
`gateway` or `data` when another deployment on the host holds the same role.
A site is one machine: `registry.Check` and `registry.Claim` refuse a host
that runs another site of this deployment (`internal/registry/machine.go`),
read from this deployment's entry, which names the site, or with no entry from
the `Address` line of `/etc/wireguard/psns-<token>.conf`. The awk merge
refuses the same thing under the lock, for two claims racing, and `Merge`
mirrors it. `validate` refuses two sites with one mesh address
(`duplicate-site-address`) and two at one ssh host and port or one dial
endpoint (`sites-share-a-machine`), so `apply` without `--site` runs every
site's prepare and pull at once with no host shared between them.
`apply` and `host prepare` also read the host's routes,
addresses, Docker networks and Docker's address pools before they claim it,
and refuse an overlap with the mesh subnet; `internal/mesh` holds the parsing
and the overlap test as pure functions, and the probe (`mesh.Probe`, over any
`mesh.Host`); `cmd/paisans/mesh.go` turns a clash into the refusal
(`checkMeshLive`). README.md "Two deployments on one host never share a mesh"
has the reasoning.

`apply`, `apply --site`, `host prepare` and `site add` review what
`paisans-stack.yaml` grants before their first change. `internal/grant` computes a site's grant from the
configuration and the render (the login user, every key of the admins
`ssh.keys` names, every rendered service's image, and the web server's
`include_dir`), compares it with the host's record,
`/var/lib/paisans/granted.<token>.json`, and writes that record under the
registry's lock. `cmd/paisans/review.go` shows the review and asks for
`approve` through `confirmWord`, refusing without a terminal;
`cmd/paisans/converge_review.go` is `apply` with no `--site`'s one review for
every site, which its steps check against rather than asking again. README.md
"`apply` and `host prepare` review what `paisans-stack.yaml` grants" has the
reasoning.

```
paisans validate --config examples/paisans-stack.example.yaml
paisans init     --config examples/paisans-stack.example.yaml
paisans render   --config examples/paisans-stack.example.yaml \
                 --secrets secrets.enc.yaml --out ./out
paisans host prepare --site home-a        # the host check, then what a blank host lacks
paisans host allow home-a bob             # bob's fingerprints, and home-a's allowlist as it would be
paisans host disallow home-a bob          # what it would take off home-a's allowlist
paisans apply    --site home-a            # shows what would change
paisans apply    --site home-a --execute  # does it
paisans prune    --site home-a            # lists dangling volumes, and which go
paisans preflight --site home-b           # site add's read only checks
paisans storage add                       # every Garage stage, from live state
paisans storage rotate-key --app talk     # replaces one app's S3 key, staged
paisans app remove docs                   # an app out of paisans-stack.yaml, off every host
paisans site remove home-b                # a site out of the deployment, staged
paisans site remove home-b --evict        # a site nobody else can reach, cut out first
paisans mesh rekey home-a                 # one site's WireGuard key replaced, staged
paisans secrets rotate --all              # every secret's rotation, in order, with its cost
paisans failover test                     # checks, and prints the plan
paisans doctor                            # what is stuck, and how to recover
paisans dns init                          # shows which records it would create
paisans dns prune                         # shows which records it would delete
paisans web-server show  --app health        # hand-off sheet for a monitor's web server
paisans web-server check --app health        # DNS, certificate, sign in, closed upstream
security find-generic-password -s acme -w \
  | paisans secrets set external.acme_dns_token
```

`validate` loads a declaration and prints every problem it finds, rather than
stopping at the first, because fixing a file one error per run is miserable. It
exits non zero when anything was refused.

`create` asks a new deployment's questions at a terminal and writes its
`paisans-stack.yaml`. `internal/create` holds the questions (`wizard.go`), the three
named shapes and the capabilities they are asked in (`shapes.go`: the core
catalog's `wizard` menu, read with `kinds.Wizard`, and the health page, which
is the toolkit's own; an app's `on` and `avoid` roles decide its site), the checks
each answer meets when it is typed (`checks.go`), and the template the file is
rendered from (`paisans-stack.yaml.tmpl`, through `render.go`, which also decides
placement, object storage and etcd timings from the answers). The rendered
bytes go through `config.LoadBytes` and `validate.Check` before they are
shown, and a refusal leads back to the section its key names
(`create.SectionFor`). `cmd/paisans/create.go` refuses an existing file and a
stdin that is not a terminal, and writes with `O_EXCL`. The tests in
`internal/create` render every named shape, with no apps and with every app,
gated and not, and require no refusal and exactly the warnings each is known
to leave; `testdata/` holds one golden file per shape, rewritten with `go test
./internal/create -update`.

`init` first gives the declaration an `id` if it has none, written as one line
after `version:` with every other byte left as it was, and never changes one
that is there. Then it settles `mesh.subnet`: until any site's registry
records this deployment, it reads every site's registry and networks, keeps a
declared subnet that overlaps none of them, and otherwise rolls a random
`10.<a>.<b>.0/24` that does, moving each site's address into it with its host
number kept and every comment left in place. A site it cannot reach makes it
refuse with nothing written. Once a registry records the deployment it leaves
the subnet alone. Then it generates the secrets a declaration needs and writes
them, encrypted to the age recipients named in a `.sops.yaml` beside the file.
It prints names and never values, because a secret printed to a terminal is
in a scrollback buffer and often in a multiplexer's log as well. Three things
about it are load bearing:

* **It never replaces a value that exists.** Regenerating a WireGuard key breaks
  every peer that trusted the old one; regenerating a database password locks an
  application out of a role that still holds the old one. Re-running `init` is
  therefore the intended way to fill in a site or an app added to the
  configuration later, and the second run of an unchanged deployment writes
  nothing at all. Replacing one on purpose is `secrets rotate`'s, which
  changes it where it is checked before any file presents it.
* **It generates only what it can.** A DNS token is issued by a provider, a
  mail password by the mail provider (`external.smtp_password`, when an app
  sends mail through a host and has no password of its own), and an OIDC
  client lives at a running identity provider. None is invented or left
  silent, and `secretsgen.Result` sorts them by who acts. `Owed` is what only
  the operator can provide, each a warning with its reason. `Pending` is what
  a running service of the deployment issues and `apply --execute` records
  before the app that needs it starts: today the client of every kind with a
  client shape (`kinds.OIDCClient`), since declaring the app is the approval
  for its client. Pending clients are one line, not warnings, and `--verbose`
  names each one's redirect URI and PKCE. A pending secret is a key absent
  from the file, never a placeholder, so `render` reads the file as it is.
* **Without an age recipient it writes plaintext and says so loudly.** Refusing
  would leave an operator holding generated secrets that went nowhere, and a
  first look at the tool must not require a key.

`secrets set <dotted.key>` writes one value read from stdin into the secrets
file, re-encrypted to the recipients in `.sops.yaml`, and prints only `set
<key>`. It strips one trailing newline, refuses an empty value, refuses a
terminal on stdin, and accepts a key only if it is already set, reported by
`init` as owed or pending (`external.acme_dns_token`, `oidc_clients.<app>.*`,
through `secretsgen.ExpectedNames`), or under `external`. It exists so a credential issued elsewhere never touches an echoing terminal or an
editor: an argument is in shell history and `ps`, an echoed prompt is in
scrollback, and `sops` opens the whole decrypted file in an editor.

`secrets set` with no key, at a terminal, walks `secretsgen.OwedSecrets`, which
holds no pending client, and reads each answer on the
controlling terminal with echo off through `readOwedSecret`, which tests
replace. Each answer is written before the next prompt, so an interruption
keeps what was typed; an empty answer skips the key.

A whole `apply --execute` does the same, once Pocket ID runs, for every admin
the `admins` section declares (`cmd/paisans/converge_admins.go`), through
`appadmin` with `Request.Declared`: the account is also found by its email, a
24 hour link is issued to each admin with no `PASSKEY_ADDED` audit event, and
with mail Pocket ID emails one too (`Request.EmailLink`); its log is read for
a failed send. Every link is printed after the run's result. See *`apply`
makes the declared admins and hands each a login link* in `README.md`.

`admin magic-link <admin>` issues one declared admin a one-time sign-in link
and nothing else (`appadmin.MagicLink`): it finds the account as apply does,
by username then email, refuses one apply has not made or that is disabled,
and prints the link once on `--execute`, emailed too when apply's are. It
reads `apps.<app>.static_api_key` from the secrets file, so it takes
`--secrets`. Its calls to Pocket ID never use sudo, because curl needs no root;
only the claim `--execute` makes in the host registry, which is root's, does.
See *`admin magic-link` gives a declared admin a fresh sign-in link* in
`README.md`.

`oidc client create --app <name>` creates an app's client at the deployment's
Pocket ID, restricted to the member and admin groups the app's `settings`
name (`member_group` and `admin_group`, defaulting to `members` and
`admins`; uptime's one group is `admin_group`, used as both, with no default
because `validate` requires it; the gate's is `members` and `admins`, the
groups its compose file admits, with no setting), with a launch URL so Pocket ID's dashboard lists it, by default
for every kind but Synapse and the gate, which have no page for a member
(`sso_dashboard_link` still gives either one), and records its ID and
secret under `oidc_clients.<app>`. Every step is printed with what it sends and
nothing changes without `--execute`. The secret is generated on the
workstation and written to the secrets file before Pocket ID is sent it, and is
never printed. `--rotate-secret` adds a new secret, leaving the old one valid.
`apply` runs the same step for every app it starts (see *`apply` creates each
app's OIDC client* below), so the command is for running it alone and for
rotating. A client is made for every app whose blueprint declares `oidc`
(`kinds.OIDCClient`), at the callback the blueprint gives, each cited there
from the pinned source. The launch URL is the app's
hostname plus the kind's dashboard path (`kinds.DashboardPath`, Mbin's
`/oauth/oidc/connect` so the tile signs the member in, otherwise `/`), or
`apps.<app>.settings.sso_dashboard_link`, which `validate` refuses unless it is
a path. An existing client's launch URL is moved only when it is empty or a
toolkit default (`kinds.ToolkitLaunchURLs`); any other value is left, with a
warning if `sso_dashboard_link` asks for something else. See *`oidc client
create` makes an app's client at Pocket ID* in `README.md`.

`app remove <app>` takes an app that has left `paisans-stack.yaml` off every host.
`internal/appremove` holds it: `Refusal` and `Build` are pure functions of the
configuration and what the probes read (`ProbeSite`, which runs
`hostcheck.Inspect`, hashes the app's manifest files and reads its stack
directory; `ProbeClient`; `ProbeDatabase`; `ProbeStorage`), and `Executor`
runs the plan, each step from live state so a re-run resumes. What belongs to
the app is `AppFile`: its stack directory, and in the infrastructure stack's
snippets a file named by `ownership.RouteOf`, unless a declared app's route
could carry the same name. The gate is the left over mark on every one of
those entries. `cmd/paisans/appremove.go` wires the hosts, Pocket ID through
`clientAPI` at the site `pocketIDSite` picks, and the claims, and asks for
the app's name at a terminal before `--delete-data` deletes anything. Tests
run a fake world in maps (`world_test.go`): files, containers, networks and
volumes per host, a Postgres and a Garage with S3 objects. See *`app remove`
takes an app off its hosts* in `README.md`.

`preflight --site <new>` makes every check `site add` makes before it changes
anything, on the site being added and on every site already running, and
prints each as `ok`, `WARNING` or `REFUSED`. It exits non zero on a refusal.
It reaches every site through its ssh section and takes no `--ssh`, since one
override cannot name several hosts. The table of checks and the reasoning for
each are in *Preflight* in `README.md`.

`failover test` switches the Patroni primary to another data site and back,
and only with `--execute`; without it, it checks the cluster and every app and
prints both switchover commands and the expected write interruption. Each
switch is gated on the new leader, the old one streaming, and every app stack
healthy and answering through the gateway, polled for three minutes. See
*`failover test`: a switchover on purpose* in `README.md`.

`doctor` reaches every site, or the ones `--site` names, and reports what is
stuck with how to recover: sites that do not answer and what is lost while
they are gone, etcd quorum and cluster version, the Patroni leader or why no
replica promotes, containers that are down, Pocket ID's active instance,
clock drift, and what is left over or foreign (below). It has no `--execute` and sends only reads, which its command
level test checks against an allow list. The judgements are pure functions in
`internal/doctor`, tested with what live hosts answered; `cmd/paisans` runs the
commands. It exits 1 on any `FAIL`. See *`paisans doctor` reports what is
stuck and how to recover* in `README.md`.

**Leftovers and foreign users of Caddy** are `internal/ownership`: `Classify`
is a pure function of the configuration, a `hostcheck.Inventory` and the
manifest's entries. Something is this deployment's only when provable: a
container with this id's deployment label and a `paisans-<token>-<stack>`
project, or a manifest entry. A stack that `render.SiteStacks` no longer names
for the site, and a manifest entry marked left over, is a leftover; on the
gateway, a `*.caddy` in `render.HostSitesDir` is foreign: the Caddyfile is
this deployment's and Caddy runs in the host's network namespace, so that
directory is the only way something foreign is served by it. `apply` prints the leftovers after
its plan (`printLeftovers` in `cmd/paisans/leftover.go`); `doctor` runs
`hostcheck.Inspect` on each site that answered and reports them as `WARN`,
and the foreign users as `info`. Both name `paisans app remove <app>` for
each app the leftovers belong to (`Report.Apps`). Its allow list takes the inventory's probes
from `hostcheck.ReadCommands`.

`render`, `apply`, `site add` and `site remove` refuse a site running the
toolkit's Caddy (a gateway, or a monitor in `managed_by` `paisans`) when
`external.acme_dns_token` is empty. `init` lists it as owed, but rendering without it produced a Caddy that
starts and then fails every DNS-01 challenge, which a visitor finds rather than
the operator.

`init` makes the age key of a deployment built from scratch and keeps it in
`internal/keystore`: the macOS Keychain through `security`, the Secret
Service through `secret-tool`, an item per deployment id under service
`paisans-age`, the key on stdin and never in argv. Tests never reach a real
keystore: `cmd/paisans` and `internal/config` replace `config.AgeKeystore` in
`TestMain`. With a `SOPS_AGE_*` variable set it makes no key
(`useVariableKey`): `config.ReadVariableKey` reads every set variable the way
the embedded sops (v3.13.3, `age/keysource.go`) does, parsing age identities
with sops' own `age.ParsedIdentities` and SSH keys with `x/crypto/ssh`, and
returns the one public key they hold together, or refuses. Tests set the
variables with `t.Setenv`, and a command variable runs `cat` on a key file.

`config.LoadDeploymentSecrets` reads that item when no `SOPS_AGE_*` variable
is set and hands its key to the file's age recipients in memory, through
`age.ParsedIdentities` in the embedded sops (v3.13.3). Otherwise, and when
there is no item or its key is not a recipient, sops' own search runs, which
reads `SOPS_AGE_KEY_CMD`, `SOPS_AGE_KEY_FILE`, `SOPS_AGE_KEY`, the SSH pair and
`sops/age/keys.txt` under the user's config directory, and tries every key it
finds. A failure names every one of those places, and the keystore item when
one was looked for. When a variable decrypted the file and the keystore holds
the deployment's key, which the variables do not name, `dataKey` calls
`config.KeystoreBypassed`; `routeKeyNotices` points it at the command's
reporter, which shows it once as a note.

`web-server show --app <name>` and `web-server check --app <name>` are for an app
pinned to a monitor site (`render.ServedBy`); every other app is the
gateway's and is refused. `internal/webserver` holds both: `For` builds the
target from the configuration, `Detect` finds the web server in front of it
over ssh, reading only (`internal/webserver/detect.go`), `Show` prints what that
web server must do and the snippet for it, or for Caddy, nginx and Apache when
`Detect` found none, and `Check` runs four
read only checks through `Probes`, a resolver, an HTTPS client that never
follows a redirect, a dialer and a clock. Behind the toolkit's own Caddy
(`managed_by: paisans`) there is nothing to hand off, so `Show` says so and
`Check` makes only the DNS and certificate checks (`Target.External`). Tests replace all four and run
against `httptest` TLS servers with certificates generated in the test, so no
test touches real DNS or the network. README.md *The monitor has a site of
its own* has the reasoning; `docs/guides/behind-your-own-web-server.md` is
the walk-through.

`apply` without `--site` uses the same package once every step has run:
`writeWebServerFiles` (`cmd/paisans/converge_webserver_files.go`) writes each
app's snippet into the web server's `include_dir` through `webserver.Sync`
(`internal/webserver/include.go`), recording each file in the manifest
(`internal/apply/webserverrecord.go`) so `app remove` and `site remove` delete
it through `webserver.Remove`, and `convergeWebServer`
(`cmd/paisans/converge_webserver.go`) runs `Check` once for each app, and a
failure alone ends the run on a `ui.Warning`, which exits 0.

`render` validates, then writes per site artifacts under `--out`. It writes
files and stops: pushing them to a host is `apply`'s work.

The example validates with exactly two warnings, and both are deliberate. Its
Synapse stack is pinned to the site that also holds the witness role, which the
design warns about rather than refuses, and its Garage keeps replication 2 on
two sites, so uploads stop while either is down, a trade its comment explains.

## The host check, before anything changes

`host prepare`, `apply`, `site add` (on the site being added) and `prune`
each run `internal/hostcheck` first, and so do `storage rotate-key` (on every
site the app runs on), `storage add` (every Garage site, the gateway and every
monitor site), and `site add`, `site remove` and `app remove` on every monitor
site they reseed, in a dry run as well as with `--execute`, and
print its report above their own plan. `cmd/paisans/hostcheck.go` holds the
one gate they share, `hostGate`, and `gateSites`, which runs it on several
sites before any of them changes. README.md *The host check: what is already on
a host decides how much is touched* has the reasoning; what the code does:

* **Claims** (`hostcheck.ClaimsFor`) are `render.SiteListeners`, each
  listener carrying the `paisans-stack.yaml` key behind it (`render.Listener.Key`),
  plus the `psns-<token>` interface, the endpoint's port (none without an
  endpoint), the mesh subnet, and each subnet the site's stacks pin
  (`render.SiteNetworks`, with its key; only an app on a monitor with `managed_by:
  external` pins one), over which a foreign Docker network or route is a
  conflict, as one over the mesh is. A new listener is added to
  `render.SiteListeners` with its key, and the host check, preflight's ports
  check and validate's collision check all see it.
* **Inventory** (`hostcheck.Inspect`) runs one probe per fact and changes
  nothing. Every probe is prefixed `export LC_ALL=C; `, and the first is
  `id -u`, refused unless it answers 0. A failed probe is an error, and Docker
  installed but not answering is an error rather than an empty list; the three
  `docker ... inspect` probes discard stderr and their exit status, because a
  container removed between the listing and the inspect is gone, not a
  failure. Each probe is told apart in tests by a substring only it contains
  (`id -u`, `docker version`, `dpkg-query`,
  `docker inspect`, `docker volume inspect`, `docker network inspect`,
  `ss -Hltnup`, `/proc/`, `ip -o link`, `ip -j route`, `ufw status verbose`,
  `is-active firewalld`, `/srv/caddy.d/`); a fake transport elsewhere that reaches one of
  these commands has to answer it. The `docker version` probe prints the
  daemon's and the CLI's versions on its first line and `compose <version>`
  or `compose absent` on the next; a fake that answers the first line alone
  reads as a Docker whose compose was not reported, which is not refused.
* **Docker** (`hostcheck.MinEngine`, `hostcheck.MinCompose`, in
  `internal/hostcheck/docker.go`) is refused by `Report.Refusal` before the
  class is: a snap, or an engine, CLI or compose plugin below the minimum.
  The source package is shown and otherwise ignored. A new dependency on a
  Docker or Compose feature goes in the table in
  `docs/specs/2026-10-11-docker-version-gate.md` with the release that
  introduced it, and raises the constant when it is newer.
* **Classification** (`hostcheck.Classify`) is a pure function of the
  claims and the inventory, tested with recorded output: a clean host with
  an empty Docker, the toolkit's own running stack, a foreign host network
  Caddy against a site that claims 80 and 443 and one that does not, a
  foreign Postgres, a published port with no listener, a foreign `psns-<token>`, a
  network over the mesh, the three ways a shared host's firewall is
  refused, and Docker from each source at old and current versions.

What "shared" changes, and where:

| Command | On a shared host |
|---|---|
| `host prepare` | `hostprep.Shared()`: the profile's `Firewall` gets `hostWide` false and plans only its own `paisans-<token>:` rules, never `ufw default` or `ufw --force enable` |
| `apply` | `apply.KeepImages()`, as `--keep-images`, which also skips the sweep |
| `site add` | `siteadd.Plan.KeepImages`, passed to the replica stage's whole apply; `siteadd.Plan.SharedSites`, the monitor sites, for the reseed's apply |
| `site remove`, `app remove` | the monitor sites are host checked, and the reseed's apply there keeps nothing back: `apply.MonitorReseed.KeepImages` is there for a caller to set |
| `storage rotate-key` | `apply.KeepImages()` in the switch's apply on that site |
| `storage add` | `storageadd.Options.SharedSites`: `apply.KeepImages()` in every scoped apply on that site, and in the monitor's reseed |
| `preflight` | a `host` check line; the `prepared` check asks for the shared plan |

A conflict, and a shared host whose ufw is not active with incoming denied or
rejected by default (or whose firewalld is active), is refused by `Report.Refusal` before
anything else is asked of the host. There is no flag to skip the check.

## `apply`, and the gates in it

`apply --site` is one site at a time, and a dry run unless `--execute` is given. Both
are deliberate: a staged change that half succeeds across three machines is
worse than one that failed on one, and the difference between "show me" and "do
it" should be a flag an operator typed rather than a habit they formed.
`apply` without `--site` keeps both: it runs `apply --site` for each site in
turn, as one step among the others it runs (`cmd/paisans/converge.go`), and
is a dry run unless `--execute` is given. Ahead of those steps it pulls every
site's images on every host at once (`cmd/paisans/converge_pull.go`), from the
plan `planSiteApply` makes for the site, with `Plan.Disk.Pulls` as the list
and `apply.DiskRefusal` as the stop for a short disk; it reports straight to
each site's line and never through `reporterOverride`, which is one command's
at a time. Every plan of one `apply` resolves each image's tag through one
`imagedigest.Cache` (`cacheDigests`), so the pull and the steps after it name
the same build, and every site of a clustered app moves to one build. That dry run changes nothing on the
servers; the one file it may write is the local secrets file, with the
generated secrets it lacks, through `fillSecrets`, the path `init` writes
them with.

The order inside it is the design, not an implementation detail. Everything is
compared first, so a conflict is found before a single byte is written; an apply
that wrote files as it discovered them could leave a stack half updated and then
refuse.

**Every command reports through one reporter.** Each command writes through a
`ui.Reporter` from `internal/ui` and formats nothing itself, so what an
operator reads can change in one place and no package decides its own voice.
There are two levels. By default a step is one short line
(`ok   disk space              13.6 GiB free`) with no rationale, values or
request bodies, because an operator watching an install needs to know where it is, and a screenful of
explanation on every step buries the one line that matters. With `-v` or
`--verbose` the detail behind each step appears under it: reasons,
configuration values, request bodies, ssh retries, and the raw output of the
two commands whose output explains a step: what each site's Pocket ID said
while `apply` waits for one active instance, and the `patronictl switchover`
of `failover test`. Compose and
pull output is not streamed at either level; a command that fails carries its
output in the error.

A step is reported before its command runs. A pull or a health wait takes
minutes on a real site, and a step reported only once finished looked hung for
all of them; a step announced first also names where an apply stopped. Every
command reads hosts the same way, whether to plan or to check: `ui.Run` and
`ui.Get` wrap work that reports nothing of its own, Eg: `siteremove.Build`, in
a step, so it is the one path to a spinner. On a
terminal the open step shows a spinner with the elapsed time. When output is
piped, or `NO_COLOR` is set, or `TERM` is `dumb`, there is no colour, no cursor
movement and no spinner, and the words `ok`, `FAIL` and `WARN` stand in for the
glyphs, so a log from cron or CI reads cleanly. A step that works through
several things, Eg: the install line of `apply` without `--site`, names the one
it has reached with `Step.Progress`: the text follows the title on the spinner
line, is not part of the finished line, and prints nothing when output is
piped, so a log gets one line for the step rather than one per change.

Anything that asks on the terminal holds the spinner first (`ui.Hold`), since a
frame clears the line it draws on and would erase the question. The sudo
password prompt is read inside a hold and erased once answered (`askPassword`,
over every row it wrapped to), and `apply.SetPromptHold` holds it
around every ssh attempt to a host until that host has answered once, which is
where ssh asks to accept an unknown host key. `routeHolds` in `cmd/paisans`
points both at the reporter that draws, beside `routeRetries`. A key
passphrase ssh asks for on every connection is not held after the first one;
the README's sudo section tells the operator to keep keys in an agent.

`apply.Report` hands the reporter to `apply.Build`, which keeps it on the
plan for `apply.Execute`, so planning and executing speak in one voice.

A note said while a step is open is that step's detail (`Detail`), and raw
command output is a `Trace`; both show only with `--verbose`. A warning shows
its one-line hint and a refusal its hint and explanation, because an operator
must see that there is something to fix. A dry run ends with `Nothing changed.
Re-run with --execute to apply.`, and `--execute` shows progress only. The
converge dry run that added generated secrets says `Nothing changed on the
servers.` and names them instead.

**The error that ends a command reads like a refusal.** `main` prints it
through `ui.PrintError`: `✗` (`FAIL` off a terminal) and a hint, then the
explanation indented and wrapped, and with `-v` the cause chain under it. An
error an operator is likely to meet is a `ui.Problem`, a hint of at most 80
characters, an explanation that says what to do, and the underlying error as
its cause. Everything needed to act goes in the hint or the explanation, never
only in the cause, since having to re-run a failed apply with `-v` to learn
why it failed is the thing this avoids. `Unwrap` returns the cause, so
`errors.Is` sees through a `Problem` to `apply.ErrUnreachable` or
`storageadd.ErrWaiting`, and exit 75, a gate waiting on Garage, is decided
that way. A typed error with
its own wording (`storageadd.Waiting`, `config.LoadError`) unwraps to its
`Problem`. Any other error prints whole in the same form, its first line as
the hint. Name a file in a hint with `ui.ShortPath`, never in a command meant
to be copied. Refusal explanations, notes and the final error's explanation
are wrapped by the reporter at the terminal's width, breaking only at spaces,
so a wrapped line keeps its indent; off a terminal they are not wrapped at
all. Write them as prose and a list as lines starting `- `, without wrapping
them by hand. A type-to-confirm question is printed unwrapped, and the
sentence saying what it deletes permanently goes through `ui.Danger`.

Tests assert on `ui.Recorder` events, not on rendered text, so a change to the
wording of a line or to the plain rendering does not break a test about what a
command did. `cmd/paisans/output_test.go` drives the dry runs and executions
against their fakes and holds piped default output to plain text, without
escapes.

**A file edited on the host is a conflict, and a conflict stops the whole
apply.** Rendered files are build artifacts and nothing edits them in place, so
a file that differs from what the last apply recorded is a change somebody made
on the machine. The record is a manifest at `/srv/paisans/<token>/.paisans-manifest.json`,
written after every successful apply; without it, every apply would be a blind
overwrite. A file present on the host that no apply ever wrote is somebody
else's too, and is a conflict rather than something to adopt, which is the case
on any host that was set up by hand before the toolkit existed.

**The narrower action wins.** A changed bind mounted configuration file needs a
restart at most, and the container keeps its identity. Only a changed `.env`, any
other `*.env` handed over as an `env_file` (`patroni.env`, `caddy/caddy.env`),
or `compose.yaml` needs `up -d`, because Compose passes environment at start and
a running container cannot be told about a new value. `caddy.env` is not a
routing file even though it sits beside the Caddyfile: a reload rereads the
Caddyfile and never the environment, so a rotated DNS token arrives only by
recreating the gateway, behind the same gates as an image change. A recreate is an outage,
however brief, so it is not the default action for every change.

The gateway's routing files make a reload and no stack action, so Caddy gets
one action per apply (README, *A routing change gets exactly one action on
Caddy*). When the infrastructure stack is also an `up -d`, `runAction` in
`internal/apply/refresh.go` reads the container IDs before and after, reloads
a Caddy `up -d` left in place, and restarts any other service whose bind
mounted file changed and whose container was left in place (`Action.Refresh`).
`gatewayaction_test.go` and `infraservices_test.go` pin both with a fake whose
`up -d` replaces only the containers a test names.

**The mesh interface, `psns-<token>`, is up before any container moves.** Every service binds the site's
mesh address, so the mesh comes up right after the files are written and before
the gateway checks and stack actions below. A first apply enables and starts
`wg-quick@psns-<token>`; a peer change is handed over with `wg syncconf` so the mesh
stays up; a change to a line only wg-quick applies restarts it; and an
unchanged file on a host whose interface is down starts it. A failure stops the
apply there. `README.md` has the table under "`apply` brings the mesh
interface up before anything binds to it".

**The infrastructure stack moves first, and app stacks only after their
databases exist.** Sorted order alone started `blog` and `docs` before
`infra`. Between the infrastructure stack and the first app stack, a site in
`cluster.sites` waits for a Patroni primary and creates clustered apps' roles
and databases; see "Every app has its own database credential" below.

**A stopped apply resumes.** Files written before a gate stopped the apply
already match the render, so the next apply would otherwise see nothing to do.
`/srv/paisans/<token>/.paisans-pending.json` records the owed stack actions and gateway checks
before the first write, drops each stack as its action finishes (the last one
stays until the end, so a bootstrap failing after it is still owed), and is
removed on success; `Build` folds it into the next plan.

**An owed stack is force-recreated.** On a real host an `up -d` failed part way
("failed to bind host port ... address already in use") and left Mbin's `app`
container created with no network attached. The resumed apply ran a plain
`up -d`, Compose saw an unchanged configuration and only started that
container, which came up with no networks and an empty route table. So a stack
the pending record owes runs `up -d --force-recreate`, and the plan shows it as
`recreate <stack> (forced)`. `--recreate <stack>` does the same for a stack
named by the operator when no record exists; it is repeatable, one stack per
flag, a stack this site does not render is refused, and a named stack is
planned even when nothing changed. Only those stacks are forced, because a
forced recreate replaces every container of a stack, an outage each time.

**Each stack must come up healthy before the next one moves.** An apply
reported success while Mbin's app could not reach its database, because `up -d`
and `restart` return as soon as containers start. After each stack's action,
`apply` polls `docker compose ps --all --format json` every 5 seconds for up to
5 minutes until every container of the project is running and every one with a
healthcheck reports `healthy`; a container without one counts once running.
A container exited, restarting or `unhealthy` stops the apply at once, a
timeout stops it too, and either error names the stack and services with one
line of cause for each, read from its last 30 log lines, which follow with `-v`. The stack stays in the pending record, so the
next apply resumes there and force-recreates it. The infrastructure stack is
checked like any other, which covers the gateway's Caddy; Patroni's own wait
still gates the database bootstrap. `ps` output is read in both shapes Compose
has printed: one JSON object per line (current, `cmd/formatter/container.go`)
and one JSON array (older v2 releases, `cmd/formatter/formatter.go`).

**A pull onto a nearly full disk is refused before anything is written.** On
the first real host (10 GB root disk) the deployed stacks left 1.8 GB free,
and an Mbin upgrade pulls a 1.4 GB image beside the old one. `Build` probes
every rendered image with `docker image inspect` in one loop that prints
`present <id> <ref>` or `absent <ref>` per image; an output missing any image
is an error rather than "present", so a garbled probe cannot skip the check.
When a recreated stack names an absent image, `Build` reads Docker's data root
and its free space, and `Plan.Disk` carries the numbers; `Build` reports them
on its `disk space` step, so a dry run shows them, and `Execute` refuses first,
before the pending record.
The default is 3 GiB, `apply --min-free <size>` overrides it for one run, and
the refusal quotes `docker system df`. README.md "`apply` checks free space
before it pulls" has the reasoning.

**A healthy stack's superseded images are pruned.** After the health gate,
`Execute` lists images (`docker image ls --no-trunc --format json`), reprobes
the rendered images' IDs and what every container uses (`docker ps -a` names
plus `docker container inspect` IDs), and runs `docker image rm <id>` for each
image from that stack's repositories that no stack of the site renders and no
container uses. A failed removal is a warning (`<stack>: old images left in
place`), since the stack is already healthy. `Build` lists the same
candidates, without the container filter, as `Plan.Prunes`, which a dry run
shows as a `prune old images` step under each stack. `apply --keep-images`
skips both, and so does a host the host
check found shared. IDs are compared by
prefix with `sha256:` stripped, since Docker prints them full or 12
characters short. The keep set is every image the site renders, held back or
not, and every image any other site of the deployment renders
(`Plan.deploymentImages`, read from the render's compose files, where another
site's Caddy is its configured tag).

**A whole apply ends with the sweep** (`internal/apply/sweep.go`). The
manifest's `images` field (`render.ManifestImage`: a repository and every
reference rendered from it) is the site's history: `writeManifest` writes the
union of what `Build` read and what the plan renders, so it only grows, and a
manifest without it reads as an empty history. When `Plan.Whole()` and not
`KeepImages`, `Build` reads the host once (`sweepRead`: other deployments' roots
under `/srv/paisans/`, `docker image ls`, the IDs of the keep set, and every
container's image, ending in a marker line so a cut answer is refused) and
fills `Plan.Sweep` with what `superseded` picks over the recorded and rendered
repositories, less what `Plan.Prunes` already lists. `Execute` reads again
after the pending record is removed and runs `docker image rm <id>` for each.
Another deployment's root on the host makes both read nothing to remove, with a
note in the plan. Every failure is a warning or a note. The pull stage plans
with `apply.SkipSweepRead()`, since it executes none of its plans. A no-op
whole apply costs two extra round trips, one in `Build` and one in `Execute`,
plus one per removed image. README.md "`apply` prunes the images it
superseded" has the reasoning.

**An image's declared volume must be mounted at exactly its path.** Docker
gives every new container an anonymous volume for a `VOLUME` nothing mounts,
and Compose abandons it on the next recreate; a real apps site collected 18
under Mbin's `/app/var/`. Three pieces, each easy to undo by accident:

* `TestEveryDeclaredImageVolumeIsMounted` in `internal/render`, behind the
  `registry_integration` tag (below), renders every app pinned and clustered
  at Postgres 16, 17 and 18, resolves every track to the build it names
  today, reads that build's configuration from the registry for every
  platform, and fails on an unmounted path. Run it when a core catalog
  release moves a track. Do not read a build's volumes with `docker image
  inspect`: that answers for one platform, and on an arm64 workstation the
  containerd image store printed null for images that declare volumes.
* `Build` inspects every image of every acted on stack (`VolumeCheck`, in
  `internal/apply/volumes.go`) with `kinds.ComposeMounts` and
  `kinds.Uncovered`. Only an exact path counts, because a bind at a parent was
  observed to leave Docker's volume beneath it. An absent image is owed and
  `Execute` pulls and checks it after the disk check and before the pending
  record; any uncovered path refuses there. The fakes in `internal/apply`,
  `internal/siteadd` and `cmd/paisans` answer the probe, which is a `for r in`
  loop like the presence probe and is told apart by `.Config.Volumes`.
* Before a recreate, `Execute` records the stack's containers' anonymous
  volumes, and after the health gate removes those now dangling
  (`internal/apply/anonvolumes.go`). Warnings only, and nothing on a failed
  gate.

`prune --site <s>` (`internal/apply/volumeprune.go`) lists every dangling
volume with size and top level entries and removes, with `--execute`, only
those labelled `community.paisans.deployment` with this deployment's id;
another id's, an unlabelled one, an anonymous one and a named one are kept. The listing ends with an `end` marker, so a cut
short answer is an error rather than a shorter list. README.md "Every volume
an image declares is mounted, and `apply` checks it" has the audit table and
the reasoning.

**A file no longer rendered stays recorded, marked left over.** A whole plan
(`Plan.Whole`) lists the manifest's entries for files the site no longer
renders as `Plan.Leftovers`, each with `leftover` set and `leftover_since`
kept from the manifest or stamped now, and `writeManifest` keeps them. Nothing
deletes the file. Dropping the entry would make the deployment's own file
unprovable, and the next apply would read it as somebody else's. A file
rendered again is written with a fresh entry, which clears the mark. Scoped
and partial plans mark nothing and keep every entry outside what they write.
`leftover_test.go` covers all three.

**The gateway creates `/srv/caddy.d` and never touches what is in it.** The
gateway's Caddy mounts the host's `render.HostSitesDir` read only, and the
Caddyfile imports `*.caddy` from it last, at the top level. `Plan.HostSites`
is set on a gateway whose Caddy is changing, and `Execute` runs
`HostSitesCommand` before the Caddy validation, the first container that
mounts it: it creates the directory root's and 0755 when it is missing and
leaves an existing one alone. Nothing under it is written or recorded.

**Files are recorded as soon as they land.** The manifest is written right
after the files, and again at the end, not only on success. A manifest written
only on success made every file of a failed first apply look like somebody
else's: on the first real host, the next apply carried a fix to `patroni.env`
and refused it as a host edit.

**`--overwrite <path>` replaces one named conflict.** A conflict stops the
apply, and the way out used to be deleting the file on the host by hand.
Naming the path is the same decision made through the toolkit, and it is
repeatable, one path per flag, so a single choice never covers files the
operator did not look at. A path that is not a conflict is refused rather than
ignored, so a typo cannot pass for consent.

**The assembled gateway configuration is validated before any reload, and a
failure stops the reload.** It is built from per app snippets, so a wrong
snippet is a wrong configuration for every hostname at once. The cost of that
mistake should be an error message on the workstation, not the public address of
every application.

**Before any of that, the gateway's Caddy is asked which DNS modules it has.**
A provider is a module compiled into the binary, so a wrong image and a provider
no module answers to both produce a gateway that cannot load its own
configuration, and neither is visible in an image reference. This check runs
before the configuration check because its error says what to fix.

**Every image is resolved to a build while apply plans.** No image is named
in Go: the core catalog names each as a track (`imagedigest.IsTrack`, a tag
pattern whose `*` each match one run of digits), an app's in its blueprint and
the toolkit's own services' in `foundation.yaml` (`kinds.FoundationImage`,
`kinds.SpiloImage`, `kinds.PostgresImage`; a reserved blueprint's service
names its image with `foundation:`). An operator's `images` entry or
`acme.image` may name a tag that moves too. `render` renders each as
configured, listing every image of every compose file per site in
`Plan.Images`, with the configuration's names for each and the number of
lines naming it, since render reaches no network.
`planSiteApply` passes `apply.ResolveImages` with `imageDigests`, an
`imagedigest.Registry`, which asks the registry's HTTP API, anonymously and
from the workstation, for the newest tag on each track (`Registry.Track`,
every page of `tags/list`) and the digest each tag names; `apply.Build` pins it
into every line naming the tag with `render.PinImages` before the file is
compared, and stops when the lines it rewrote differ from the count render
reported. A new build is therefore a changed file, and goes through every gate
above. A digest the configuration names is left alone. A registry that cannot
be reached keeps the build the stack runs, read from its compose file, with a
warning; a stack running none is refused. Execute records each service's build
in the manifest's `pins`. `Plan.Moved` is what the dry run lists, with the
version labels `imagedigest.Registry.Version` reads. `apply --site` passes
`apply.OtherSites`, the compose files of each clustered app on its other sites
(`readPeers`), so the app stays on the build they run, and sites on different
builds are refused; a step of apply without `--site` (`planningEverySite`)
passes none. Without a resolver, or for a stack outside `--only` or held back,
Build keeps whatever the host runs; a track that runs nowhere on the host is
resolved through `apply.FreshImages`, which `paisans` sets to `imageDigests`,
and Build refuses to write a compose file still naming a track. Tests of the
packages built on apply set it with a blank import of
`internal/apply/applytest`, whose `Pinned` also seeds a fake host with files
as an apply leaves them. `provenOn` skips the platform check for a build the
host runs, by digest, on the architecture the manifest's `arch` recorded,
because each check is a manifest GET Docker Hub counts against an anonymous
allowance and a running build already proves its architecture; `CheckPlatforms`,
which reads no host, relies on `imagedigest.Cache.Store` instead, the
per-digest answers `cacheDigests` keeps under the catalog cache for the real
registry only. `Registry.Platforms` reads the index alone. A 429 is
`imagedigest.ErrRateLimited`, which `imagedigest.Unreachable` counts as a
host down for the run. `ImagePin.FirstPin` is a bare tag pinned
for the first time, which the dry run lists apart from `Plan.Moved`. Tests in
`cmd/paisans` stand a fixed digest in for the registry (`TestMain`).

A DNS provider's Caddyfile syntax is provider specific rather than uniform: the
cloudflare module takes the API token as a bare argument, while the deSEC module
requires a block with a `token` subdirective and rejects a bare argument. That
is why `internal/acme` holds the exact lines for each provider rather than
building one shared form (github.com/caddy-dns/cloudflare, README "Caddyfile"
section; github.com/caddy-dns/desec, README "Caddyfile" section).

**`apply` creates each app's OIDC client.** Declaring an app whose kind has a
client shape (`kinds.OIDCClient`) is the approval for its client at the
deployment's Pocket ID (founder decision, 2026-10-08), so `apply` ensures it
before the app renders. `clientStep` in `cmd/paisans/clients.go` holds the
step; `oidc client create` calls the same helpers there, and
`internal/oidcclient` does the planning and sending, unchanged. The step plans
read only before the site plan, so a dry run shows it. On `--execute`, a site
running Pocket ID applies in two passes (`executeWithClients`): the first
holds every other app stack back with `apply.Except`, and with it each held
app's Caddy route on that site (`render.File.App` names the app a file
outside its stack directory belongs to; `heldRoute` holds it unless it is
not on the host yet, since the Caddyfile imports it by path; a held file
edited on the host stays in the plan as a conflict, so the pass refuses on
it), then `CheckOneActive`
waits for Pocket ID's `/healthz`, then the clients are ensured, then the site
is planned again with the new credentials and executed. Any other site
ensures first and applies once. An app with no recorded client is held back
when Pocket ID cannot be asked, which exits 0; a refused client holds its app
back and exits 1 once the rest is applied, and so does an app whose recorded
credentials apply will not replace (`keepsRecorded`): partly recorded, or a
client with the client's name (`config.Config.ClientName`) under another ID. Fully recorded credentials for a
client Pocket ID does not have are replaced, with `oidcclient.Plan.Replaces`
naming the recorded ID. The second pass gets `apply.After(first)`, so an
`--overwrite` or `--recreate` the first pass carried out is not repeated, and
a held app's database path restart is owed in the pending record
(`DatabasePath`) and runs once when the app is released. `planWithClients`
calls `apply.Refusal`, the checks `Execute` makes before its first write, on
the whole site before Pocket ID is asked anything. The pending record is kept
whole across partial plans: an `--only` run carries what is owed outside it,
a `--recreate` of a held stack is owed, and an owed stack the site no longer
renders is dropped with a note (`Plan.Notes`). `apply.Except` keeps a held stack
owed in the pending record, so the apply that releases it force-recreates it.
`clients_test.go` drives the passes with a fake `sitePass` that renders and
records, and the Pocket ID fake from `oidc_test.go`.

**After an apply that acted on a Pocket ID stack running on more than one site,
every one of those sites is asked whether its instance is active.** Each
stack passed its own gate whether it is active or on standby, so this is the
only check that can see two active or none. It lives in `cmd/paisans`
(`checkStandby`) rather than in `apply.Execute`, because it reaches other
sites and `Execute` is one site's. The rule is `apply.OneActive`, shared with
`failover test` and with the admin commands, which use it to find the site
to call. README: "Pocket ID runs on every apps site, and one of them is
active".

## `site add`, and why it is not a loop of applies

```
paisans site add home-b             # every stage, its steps and its gate
paisans site add home-b --execute
```

`internal/siteadd` builds the join from live state and runs it stage by stage;
README.md's *`site add` is built for sites that dial each other* has the
design. What the code relies on, and what is easy to undo:

* **Existing sites are changed by scoped applies** (`apply.Scope`), never whole
  ones. A whole apply of the primary's site would recreate Patroni for its
  new `ETCD3_HOSTS`, a failover in the middle of a join. A scoped plan records
  its files in the same manifest, keeping every other entry, so the next whole
  apply sees them as its own, and `apply.Rollback` restores what one updated.
* **Every etcd host has `/srv/paisans/<token>/infra/etcd-initial`**, the flags its member was
  born with. `apply` and `site add` read it (`apply.ReadEtcdInitial`, falling
  back to the compose file's own flags) and hand it to `render.Build` with
  `render.WithEtcdInitial`; without it a member renders as a founder of
  `etcd.members`. `isRecord` in `internal/apply` keeps its write from acting on
  the stack.
* **`apply` refuses a half grown etcd** (`apply.EtcdRefusal`), probing the
  site and then the other configured members.
* **Every wait is a count of polls**, the wait over the interval, so the tests
  replace `sleep` with nothing (`SetFast` in `export_test.go`) and still end.
* **Stage 1 is `internal/preflight`**, through `runPreflight`, a variable so
  that the tests here, which drive the later stages against fake hosts, stand
  in a passing report; preflight's own checks are tested in its package. A
  re-run of a join that has started skips stage 1, because the new site's own
  mesh interface now holds the port preflight's ports check refuses.

The tests in `internal/siteadd` run a three host world in maps: etcd's
membership, Patroni's members, each HAProxy's served configuration and the
handshakes implied by each `psns-<token>.conf`. They cover the dry run changing nothing,
a whole join leaving nothing to do, the mesh rollback, the promotion retry and
its limit, resuming after a stopped learner and after a failed replica gate,
the stage 5 and 6 gates failing, each replica's `patroni.env` brought up to
date, and the monitor reseeded last.

## `site remove`, and what each removal is proven by

```
paisans site remove home-b             # refusals, then every stage, its steps and its gate
paisans site remove home-b --execute
```

`internal/siteremove` builds the removal from live state and runs it stage by
stage, the way `internal/siteadd` builds a join; README.md's *`site remove`
takes a site out* has the design, and `docs/specs/2026-10-08-site-remove.md`
the approved specification. What the code relies on, and what is easy to
undo:

* **The end state is `siteremove.EndState`**: `config.WithoutSite`, and, when
  that would leave one data site and a witness as two etcd voters, the
  witness out of `etcd.members` and its `witness` role too. Stage 5 writes
  exactly that with `config.RemoveSite`, or `config.RemoveSiteAndWitness` in
  the witness case, both editing `paisans-stack.yaml` in place the way
  `config.SetMesh` does: the site's block and the comment lines directly
  above it, one item in each list, one capacity line, every other byte kept.
  `Refusal` validates the end state, so a removal never writes a file
  `validate` would refuse.
* **Remaining sites move only what names the removed site.** Stage 2 renders
  the configuration with and without the site (each etcd member with the
  flags its host records) and diffs the two per site. The mesh file,
  `haproxy.cfg` and Caddy's routing files go through a scoped `apply` and the
  one command each needs. A replica's `patroni.env` goes through
  `apply.ReplicaEnv`, which `site add`'s stage 7 shares: a scoped `apply`
  built at the moment it runs, so the manifest keeps what earlier scoped
  writes on the same host recorded, then Patroni recreated alone and gated on
  streaming. The leader's `patroni.env`, and every other changed file, is
  reported for a later `apply`, except the monitor's own stack, which the
  last stage applies whole.
* **The monitor is reseeded last, by `apply.MonitorReseed`**, which
  `site add`, `storage add` and `app remove` share: every monitor site's
  `uptime` stack as an `apply.Only` plan, built at the moment it runs for the
  same reason `ReplicaEnv` is, then gated on the seed read back and the
  stack healthy. It comes after the configuration edit, so its failure names
  `paisans apply --site <monitor> --only <app> --execute` rather than a
  re-run. README.md's *Topology commands reseed the monitor* is the argument.
* **Every host removal is selected by its proof, at the moment it runs.**
  Docker objects by the label filter, files by `appremove.FilesCommand`
  (hash checked on the host), units by the `paisans-<token>-` glob, ufw rules
  by `hostprep.ParseAddedRules` read again before deleting, the registry by
  `registry.RemoveCommand` under the claim's lock. A re-run therefore does
  only what is left. `verifyHost`, the stage's gate, runs last.
* **The SSH allow is never removed, and not reported.** It is how the host
  is reached, so it is always kept: `buildHost` leaves it out of the plan and
  `runHost` skips it when it reads the rules again. The full removal,
  `--force` and `--id` all go through those two, and
  `TestNoRemovalDeletesTheSSHAllow` runs each, with and without
  `--delete-data`.
* **Authorized keys are never removed.** A key may be the only way into the
  host, and nothing on it says whether another exists. The deployment's
  records of the keys `host prepare` added are its own bookkeeping, so they
  are deleted under host prepare's key lock, with no step of their own in the plan.
* **The registry removal has a Go counterpart**, `registry.Remove`, and the
  tests run the awk program against `Encode` of its result.
* **`inspect` is a variable** standing in for `hostcheck.Inspect`, so the tests
  hand in a fake host's Docker objects and manifest.

The tests in `internal/siteremove` run a world in maps (`world_test.go`):
etcd's membership and health, Patroni's members, Garage's layout and resync,
each HAProxy's served configuration, and per host its files, Docker objects,
units, ufw rules, keys and registry. They cover the dry run changing nothing,
a whole removal keeping every foreign container, file, unit and rule and an
edited file, every refusal, resuming from each stage, the leader switched
over first, synchronous mode turned off, `--host-gone`, `--delete-data`, the
key rules, and the gateway hand over: done, not needed, undone on a failed
gate, stopped by an invalid configuration, and refused into a `/srv/caddy`
that is somebody else's.

### `--evict`, and why its order differs

`siteremove.Build` with `Options.Evict` builds the eviction from the same
probes and the same pieces as a removal, in another order: `buildEvict` in
`internal/siteremove/evict.go`. README.md's *Evicting a site: `--evict`* has
the design, and `docs/specs/2026-10-11-site-evict.md` the approved
specification. What the code relies on:

* **Joining sites are read, then left alone.** `JoiningSites` reads each
  remaining host's registry; one without this deployment's entry is a
  replacement declared in the same edit, and is taken out of the transports,
  so no stage can reach it. `live` filters it out of every list a probe or
  gate walks (etcd members, cluster sites, Garage sites, gateways), and the
  command neither host checks nor claims it.
* **The cut is rendered without the joining sites** (`liveEnd`, which also
  drops apps pinned to them), so it never waits on a host or a DNS record
  that does not exist yet. Stage 6 uses the render with them, for HAProxy.
* **`planSiteChanges` and `planForget` are shared** with stage 2 of a plain
  removal, with a filter for which files a stage moves: the cut takes the
  mesh file and Caddy's routing, the rest stage everything else.
* **Patroni's health is not refused once the cut is done.** A failover is
  under way, so a resumed run reads the members as they move; stage 3's gate
  is what has to hold.
* **Which replica leads is read at stage 6, not at Build**, when the evicted
  site led: its successor is Patroni's choice in stage 3.

The tests in `evict_test.go` run the same world, with a failover that
promotes the `Sync Standby`, or the first streaming replica, once no
remaining host's running interface lists the evicted key, and a running mesh
file per host that only `wg syncconf` changes. They cover the order, the host
never reached, a leader with and without a `Sync Standby`, objects lost at
replication 1, a gateway replaced in the same edit, the token check, every
refusal, a partial cut, and resuming from each stage.

## `mesh rekey`, one site at a time

`internal/meshrekey` rekeys each site in four stages, reading the secrets
through a `Store` the command implements over `config.LoadDeploymentSecrets`
and `config.WriteSecrets`. README.md's *Rekeying the mesh: `mesh rekey`* has
the design. The swap renders the configuration with the site's next key in
place of its current one, and every other site with the key its host runs
(`internal/meshkey`, which `site remove` uses too, so a peer part way through
its own rekey keeps its new key), and moves only the mesh file, by scoped
`apply`, on every peer and then the site; render never reads
`wireguard_next_private_key`, so `apply` and `apply --site` refuse while the
secrets hold any next key (`refuseMidRekey` in `cmd/paisans/meshrekey.go`):
they would put the old key back. Where a rekey stands is read
from the secrets file and `wg show <interface> public-key`, and nothing else.
A site other sites relay through is found in the rendered mesh files, as the
peer each of them routes the whole subnet through while it lacks a peer of its
own for some other site, and Patroni is paused around its swap. The tests run
a mesh in maps in which a handshake is up when both ends run a peer entry for
the key the other runs, two undialled sites reach each other through their
relay, etcd is healthy while every voter's tunnels are up, and Patroni's pause
is a flag that `show-config` reports.

## `secrets rotate`, one secret at a time

`internal/secretsrotate` is the engine: `Plan`, `PlanAll` and `PlanAllRestart`
read where each rotation stands and change nothing, and `Run.Execute` takes each
secret through its four stages. Each secret is a `Rotator` (`rotator.go`) saying
where its next value comes from, how the checking side learns it, which sites
and stacks present it, and how the value is promoted, grouped into files by what
checks the secret (`postgres.go`, `rabbitmq.go`, `garage.go`, `uptime.go`,
`appsecret.go`), and `wrapped.go` for the pasted secrets (the DNS token and the
SMTP passwords) and for the OIDC and S3 rotations, which run `oidc client create --rotate-secret` and `storage
rotate-key` in their place in the order. `cmd/paisans/secretsrotate.go` maps a
path to its rotator, builds `--all`'s order, and hands the engine a scoped
`apply` rendered from the secrets with the next value in place, never from the
file, so stage 3 presents what stage 2 taught. What is easy to undo by accident:

* **The next value is written before any host is handed it**, and the file
  is read back and compared after every write. A re-run takes the pending
  value from `rotations` and never generates another while one is there.
* **Every command that renders refuses while a rotation is pending, and
  `secrets rotate` refuses while a `mesh rekey` is** (`refuseMidRotation`
  and `refuseRotateMidRekey`, both in `cmd/paisans/secretsrotate.go`).
  Either would render a value some host no longer runs. A new command that
  renders from the secrets file and reaches a host calls `refuseMidRotation`
  too.
* **A kept secret is a `KeptError`, not a refusal.** A precheck that finds
  nothing checks the secret (an admin role that cannot log in, a queue with no
  broker) returns one, and the engine lists it in `Run.Kept` and plans the
  rest. A pending rotation's precheck is run too, except a pasted secret's,
  and there any refusal, a `KeptError` included, means it cannot finish; its
  refusal ends with `secretsrotate.CannotFinish`, as every such refusal does.
* **Stage 3 asks the rotator for its users again.** The plan's `Item.Uses`
  are only what it shows: a cluster role's switchover moves the leader, and
  the next cluster role in the same run restarts the new one last.
* **A pasted secret is compared line by line.** `carriers` finds the lines a
  render with a probe value changes, and the host prints the hash of each
  line of its copy (`lineHashCommand`), so a file that differs elsewhere does
  not pass for a replaced secret.
* **Passwords never reach `argv` or a log, except where upstream leaves no
  other way in.** SQL goes on stdin with statement logging off, as `apply`'s
  bootstrap does; `rabbitmqctl` takes a password only as an argument, which
  `rabbitmq.go` says and accepts for the length of the call.

## `storage add`, and what lives where

`internal/storageadd` is shaped like `internal/siteadd`: Build reads every
Garage site and the gateway and changes nothing, each stage carries its steps
and a gate, and Execute runs the steps only when there are some and the gate
always. `docs/specs/2026-10-07-multisite-garage.md` is the approved design and
cites the Garage v1.0.1 source behind every gate. What is easy to undo by
accident:

* **Two gates wait on Garage, not on the toolkit** (`Stage.Waits`: settle and
  sync). They return a `*Waiting`, which unwraps to `ErrWaiting`, and
  `cmd/paisans` exits 75 on it rather than 1. A failure that is really a wait
  makes an operator chase a problem that is not there; a wait that is really a
  failure makes them wait for ever. Pick deliberately.
* **Every piece of resume state is on a host.** The deployed factor in
  `garage.toml`, `meta/cluster_layout.rf<N>` beside a set aside layout, and
  the object counts in `/srv/paisans/<token>/infra/garage/replication-change.counts` on the
  anchor's host. Nothing is kept on the operator's machine, so a run resumed
  from another machine sees the same state.
* **The reset stops every node at the old factor before it changes any.**
  Garage exits a node that meets a peer at a higher factor
  (`src/rpc/system.rs:583-587`), so stopping and rewriting one node at a time
  takes the others down mid-run. A unit test asserts the order.
* **`garage.toml` is rewritten through `apply`, scoped, with
  `apply.ReplicationChange()`**, which is the only way past `apply`'s refusal
  of another factor. Writing it any other way leaves the manifest stale and
  makes the next `apply` call it somebody's edit.
* **The probe's key goes to curl on stdin** (`curl -K -`), never in a command
  line, and a failure is redacted. A unit test makes the fake host echo stdin
  into the error, which is the worst case.
* **The media routes are rendered after the join, not before.** A node with no
  role answers every bucket as missing, and the routes prefer the first
  listed node. The stage writes every `<app>-media.caddy` the gateway renders,
  one per app that stores objects.

* **The stop test always starts the node again**, in a deferred call, even
  when a read failed, and only ever stops the last listed site. A refactor that
  returns early from `stopTest` without the deferred start leaves a site down.
* **Capacities are compared in bytes, within 2%**, because `layout show`
  rounds. Comparing the strings would reassign, and rebalance, on every run.

The tests in `internal/storageadd` run a Garage cluster in maps: each host's
files, its node, its copy of the layout and the peers it has met, with output
shaped like `dxflrs/garage:v1.0.1`'s. They cover the refusal without
`--change-replication`, a reset that waits at the sync and resumes, `--wait`,
a reset interrupted between rewriting and starting, the order rule, a site
with no `garage.toml`, a join without a reset, the probe's secret, and a
shortfall in object counts.

## `storage rotate-key`

`internal/rotatekey` has the same shape as `internal/storageadd`: Build reads
the secrets and Garage and changes nothing, each stage carries its steps and a
gate, Execute runs the steps only when there are some and the gate always. The
rules are in `README.md` under *Replacing an app's key*. What is easy to undo
by accident:

* **The resume state is the secrets file.** A previous pair beside the current
  one means a rotation is in progress. Nothing generates a pair while one
  exists, and half a pair, or a previous key equal to the current one, is
  refused before Garage is asked anything.
* **The deletion is last and is never skipped to.** A failure at any stage
  before it must leave the old key in Garage and granted; a test fails each
  stage in turn and asserts that, then resumes.
* **Keys are matched by ID only.** `garage key info`, `key delete` and
  `bucket allow` take a pattern that matches an ID prefix or an exact name
  (`src/model/key_table.rs`, `KeyFilter::MatchesAndNotDeleted`, at v1.0.1),
  so the code passes a full ID every time, and the grant check reads only the
  ID column of `bucket info`.
* **The switch is `apply` itself.** `cmd/paisans` implements
  `rotatekey.Switch` with `planSiteApply`, the function `runApply` uses, and
  `apply.Only`. A second way of writing an app's `.env` would leave the
  manifest stale.
* **Secrets stay out of output.** `key import` takes the secret positionally,
  so it goes through `garage.ImportStep`, whose failures are redacted; the
  probe sends the key on curl's stdin through `garage.S3Object`. A test
  asserts that no other command carries either secret.

The tests run Garage's keys and grants, the secrets file and each site's
running key in maps: a whole rotation's order, a dry run that changes nothing,
a resume after a failure at every stage and after the deletion, and the
refusals.

## `dns init`, `dns prune`, and why init can only create

`dns init` is the one command that talks to something other than a host or the
local disk: the DNS provider's API, from the workstation. It is a dry run unless
`--execute` is given, like `apply` and `storage init`, and it takes no `--site`
because it reaches no site.

```
paisans dns init                 # shows present, create and conflict per record
paisans dns init --execute       # creates what is missing, then reads each back
paisans dns prune                # shows remove and keep per toolkit record
paisans dns prune --execute      # deletes the removes, then lists the zone again
```

The rules are in `README.md` under *`dns init` creates the records a deployment
needs*. Four things about the code are easy to undo by accident:

* **`internal/dns.Provider` has no update.** A method that does not exist
  cannot be called by mistake, and a conflict is reported for a human to
  resolve rather than handed to an overwrite. Adding one is a design change,
  not a convenience.
* **`Delete` exists for prune alone.** Its one caller is `ExecutePrune`, which
  deletes only what a prune plan marked remove after every rule in
  `internal/dns/prune.go`. Two builders make that plan, `BuildPrune` for `dns
  prune` and `BuildSiteRemoval` for `site remove`'s DNS stage, and both fill
  the one `pruneRules` the one planner applies: they differ only in the
  address set of rule 4, the configuration rule 5 compares against, and the
  shared-address reason. Loosening a rule, and above all deciding by the
  comment alone, reopens a founder decision; the README section says why.
* **The provider's base URL is a struct field, not a flag.** Tests point it at
  an `httptest` server; an operator has no reason to send the zone token
  anywhere but the provider. No test calls a real provider, and none may.
* **The token never leaves the request header.** Every error from the
  Cloudflare client passes through a redaction of the token, and a test
  asserts it, including against a fake provider that echoes the header back.

Cloudflare's request and response shapes are cited in
`internal/dns/cloudflare.go` against its API reference. Nothing in this package
has been run against the real API yet.

A deployment that uses the challenge only zone arrangement described under
*Certificates use DNS-01, everywhere* holds a token that cannot see the main
zone. `dns init` then reports that no zone it can see holds the name, which is
correct: that arrangement trades this command for a narrower credential on the
gateway, and the records are created by hand.

### Why ssh is shelled out to and sops is not

The opposite choice in each case, for the same reason: what the operator already
has.

`sops` and `age` are embedded so that an operator need not
install two binaries before they can render anything, and because no code path
here may put a decryption key on a host.

`ssh` is shelled out to because every operator already has one, and theirs
already knows things this toolkit should never learn: their agent, their keys,
their `~/.ssh/config` with its jump hosts and per host users, their
`known_hosts`. An embedded client would have to reimplement that or, far worse,
invite a toolkit specific way to hand it a private key.

What the toolkit adds to the operator's ssh comes from the site's `ssh`
section, after the connection sharing options (`internal/apply/sshmux.go`,
README's *One connection per host, shared*), and is pinned by a test of the
exact argument list (`internal/apply/transport_test.go`):

```
ssh -o ControlMaster=auto -o ControlPath=/tmp/psns-XXXX/%C -o ControlPersist=60 -p 22 -o IdentitiesOnly=yes -o ConnectTimeout=10 -i /tmp/paisans-ssh-XXXX/key-1.pub -i /tmp/paisans-ssh-XXXX/key-2.pub ubuntu@203.0.113.10 "sh -c 'eval \"\$(printf %s <command, base64> | base64 -d)\"'"
```

The `-i` files are the listed **public** keys, written per invocation into a
0700 directory and removed after. With `IdentitiesOnly`, ssh offers exactly
those and signs with whichever one's private half is in the operator's agent;
README's *How the operator's ssh uses them* cites ssh(1) and ssh_config(5) for
it. `paisans-stack.yaml` never names a private key, because it is shared by every
admin and a private key is one admin's. `--ssh <destination>` replaces the
section whole and is passed verbatim, as it was before the section existed.

File contents go to the host over stdin rather than in a command line, because a
rendered file carries credentials and a command line is visible in `ps` to every
user on the host. Each write lands in a temporary file that is then moved, so a
failed transfer leaves the previous file intact rather than a truncated one an
application would happily read.

A connection that never opened is retried, twice, inside `SSHTransport` itself
(README, *A connection that never opened is tried again*), so every caller gets
it without asking. Running the ssh binary is a package variable, `runSSH`, and
the tests in `transport_test.go` replace it with a fake that chooses the output
and exit status, which is how the "255 and a connection error, nothing else"
rule is pinned. When every attempt fails the error wraps `apply.ErrUnreachable`,
which is what a caller tests with `errors.Is` before it reads a failed probe as
anything at all.

The fakes in `internal/storageadd` and `internal/siteadd` have an
`unreachable` knob that fails matching commands with that error, and the tests
using it pin README's *Never infer host state from a failed probe*: each probe
stops with "could not read host state", and none is read as an answer. A new
probe that ignores an error, or reads `err != nil` as "absent", is the pattern
to avoid; check `apply.ErrUnreachable` first, or make the remote script print
a marker for every answer it can give, as the set aside script does.

## `gateway move`, and the one other path that deletes DNS records

`paisans gateway move` moves the gateway role to the site `paisans-stack.yaml` gives
it, from the site that still serves as the gateway, as the overlap README's
*Moving the gateway* describes. It is a dry run unless `--execute` is given,
takes no site, and is built the way `site remove` is: `internal/gatewaymove`
plans six stages from live state, each ending at a gate, and every gate runs
on every run, so a re-run after a fix resumes. `apply` without `--site` runs it
last when a site without the gateway role still holds a gateway.
`docs/specs/2026-10-11-gateway-move.md` is the specification.

```
paisans gateway move              # the old gateway it found, then every stage
paisans gateway move --execute    # record, serve, dns add, dns remove, wait, stop
```

Things about the code that are easy to undo by accident:

* **`ExecutePrune` deletes a third builder's plan.** `internal/dns/move.go`'s
  `BuildMove` makes the plan, under its own rules (stated at the top of that
  file), and `ExecuteMoveRemove` deletes through `ExecutePrune`. It is a
  founder decision (docs/decisions.md, 2026-10-11) that this deletion is
  automated at all, and it is narrow: this deployment's comment, an unproxied
  address record, a hostname the gateway serves, the old gateway's address and
  not the new one's, and every record the new gateway needs present. Widening
  any of these, or deleting before `verify` passed on the same run, reopens
  that decision. `Delete` still has `ExecutePrune` as its only caller.
* **The new gateway is checked through its own address.** `Probes.Get` dials
  the address under test and verifies the certificate against the hostname,
  whatever the name resolves to; resolving the name would check the old
  gateway for as long as DNS points there. The check runs at stage 2's gate and
  again at the start of stage 4's and stage 6's runs.
* **The old gateway is found, never declared.** A site without the role and
  not running Caddy for a monitor is the old gateway when its host has this
  deployment's gateway Caddy, its deployment record or the move's marker, or
  this deployment's records at a gateway hostname point at it. A probe that
  fails is an error, never "no gateway". Stage 6 removes the marker last, so a
  run stopped part way finds the site again.
* **`host prepare` keeps a former gateway's 80 and 443** while its Caddy is
  there (`hostprep.GatewayCaddyProbe`), with the gateway's own comments, so
  `apply`'s first step does not cut the overlap short.
* **`dns init`'s `moving` is not a conflict and not a create.** A wanted
  record at a gateway hostname that still holds the record `dns init` made for
  another declared site is left to the move (`Want.ViaGateway`,
  `Want.Former`). Turning it into a create would publish the new gateway
  before it is verified; turning it into a conflict stops `apply` before the
  new gateway is applied.
* **No test reaches a host or a provider.** `cmd/paisans`'s `TestMain` makes
  every site answer that it holds no gateway, and the move's own tests use
  fake hosts, `dnstest` and fake probes.

Nothing here has been run against a real host or a real provider.

## Refuse and warn

The distinction is the point of the tool and it is not a matter of severity
taste.

**Refuse** means the configuration is incoherent: it describes something that
cannot work, so rendering it would produce artifacts that damage a deployment.
A witness sharing a failure domain with its only voter is the worked example.

**Warn** means the configuration is legitimate but risky, and the risk is fine
when it is chosen rather than stumbled into. Pinning an app onto the witness
host is the worked example.

Every rule traces to a rule in `README.md`. If you believe one is missing, say
so in the pull request rather than adding it: policy that is not in the README
is policy nobody agreed to.

## Tests

```
go vet ./...
go test ./...
```

Three kinds of test carry most of the weight.

**Rule tests** live in `internal/validate`. Each fixture in
`internal/validate/testdata` is the valid one with exactly one thing broken and
is named for the rule it breaks, so a rule that fires on the wrong fixture is a
failure rather than a puzzle.

**Golden tree tests** live in `internal/render`. The tree under
`internal/render/testdata/golden` is checked in, because it is the
specification of what an operator receives: a diff there is a change in what
lands on a host. Regenerate it deliberately, and read the diff:

```
go test ./internal/render -update
```

**A determinism test** renders twice and compares byte for byte. Nothing
rendered may carry a timestamp or depend on map iteration order, or a diff
between two renders stops meaning anything.

**Docker tests** run real `dxflrs/garage:v1.0.1` containers, and are behind a
build tag because they need Docker and take seconds rather than milliseconds:

```
go test -tags garage_integration ./internal/garage/ ./internal/storageadd/
```

`internal/garage`'s starts the rendered `garage.toml`, provisions a node, and
routes media through the rendered snippet in the real gateway image,
including a read with the first listed node stopped. `internal/storageadd`'s
drives `storage add` from one node at replication 1 to two at replication 2,
`dangerous`, through the reset, and reads an object written before it back
through both nodes. A second grows to three nodes at replication 3, one a
`storage` host with a smaller capacity, and runs `--stop-test`. Under the tag
the Docker tests run alone and at real timing: the fake-cluster tests, whose
`TestMain` makes every wait instant, are left out of that build. It runs its probe in `curlimages/curl`, which it pulls.

**Registry tests** read published images over the network, anonymously and
without Docker, behind their own tag:

```
go test -tags registry_integration ./internal/render/
```

`TestEveryDeclaredImageVolumeIsMounted` resolves each track the test copy of
the core catalog names and checks what every linux platform of its build
declares against the rendered mounts. Docker Hub limits anonymous reads, so a
run soon after another may answer 429; wait and run it again.

Pocket ID's standby wrapper has Docker tests of its own, behind their own tag:

```
go test -tags standby_integration -timeout 10m -run TestStandbyWrapper ./internal/render/
```

Four run the rendered wrapper in the pinned Pocket ID image, through the
image's entrypoint and `su-exec`, with a fake `/app/pocket-id` bind mounted
over the binary: standing by and then serving, a prompt stop that reaches the
child, any other exit passed on, and the 50 line window. The fifth runs two
real instances against a `postgres:17-trixie` and stops the active one. Rerun
it whenever the Pocket ID image is bumped: it is what proves the marker
against the binary. It runs the image for the workstation's own platform;
on 2026-10-13 all seven passed on arm64 as well as amd64. Docker reuses
whatever build it already holds under a tag, so a workstation that once
pulled the image with `--platform linux/amd64` keeps running that one under
emulation: `docker pull` the tag first to get the native build.

The admin reconciler has a Docker test of its own, behind its own tag:

```
go test -tags pocketid_integration ./internal/adminreconciler/
```

It starts the pinned Pocket ID image with a static API key, makes one
administrator in `admins` and one outside it, runs one pass, and reads the
groups back; then demotes the second, runs another pass, and reads them
back again. Rerun it whenever the Pocket ID image is bumped: it proves what
the reconciler assumes about the API, and the static key's user, against the real
thing.

The passthrough ini merge and the locked settings have a Docker test of
their own, behind their own tag:

```
go test -tags writefreely_integration -timeout 30m ./internal/render/
```

It boots the pinned WriteFreely fork against the rendered `config.ini`, with a
Postgres beside it, and asks the application for the passthrough key's value;
then does the same with that one line removed, as a control. Then it boots it
once more and checks that every name in `locked_settings` is locked at the
file's value, that `/healthz` answers 200 and that an unsigned read of a blog
outbox answers 401. It boots the application four times and may pull images,
hence the longer timeout. `PAISANS_WF_IMAGE=<reference>` runs it against
another build, such as a local one of a release newer than the one the kind
names.

The one workflow in `.github/workflows` builds the admin reconciler's image,
and runs only the tests of the packages that image is built from. Nothing runs
`go test ./...` or any tagged test for you, so run them before a pull request.

## Fixtures and secrets

`internal/render/testdata/secrets.fixture.yaml` is plaintext on purpose. Every
value in it is a fixed placeholder, and its WireGuard private keys are repeated
bytes chosen so the derived public keys are stable across runs. Nothing in this
repository is a credential, and nothing in it may become one.

The encrypted path is exercised without an encrypted file ever being committed:
`internal/config/secrets_test.go` generates an age identity, encrypts a fixture
in memory, writes it to a temporary directory, and reads it back. That test is
also the proof that sops and age are genuinely embedded, because it starts no
process.

`sops` and `age` are Go libraries here, not binaries. Neither has to be
installed, on a workstation or anywhere else.

## Layout

| Path | Holds |
|------|-------|
| `cmd/paisans` | the command, flag parsing, and how findings are printed |
| `cmd/admin-reconciler` | the admin reconciler's binary and its image's Dockerfile: the pass loop, `/healthz` and the DNS check's two paths, and `healthcheck` |
| `internal/adminreconciler` | one admin reconciler pass: who to add to `admins` and who to remove, the two writes, the floor under removals, and what `/healthz` says |
| `internal/config` | loading `paisans-stack.yaml`, and decrypting `secrets.enc.yaml` |
| `internal/configmerge` | placing an app's passthrough `config` keys into a file a template has already rendered, in that file's own syntax |
| `internal/ui` | how every command reports to the operator: one line per step by default, and every detail with `--verbose` |
| `internal/deployment` | a deployment's id and token, and every name, path and label derived from them |
| `internal/registry` | a host's record of the deployments on it, and the locked claim every writing command makes |
| `internal/deployrecord` | the gateway's record of the sites, apps and Pocket ID groups the deployment has deployed, which `apply` adds to and only a removal takes from |
| `internal/mesh` | reading a host's routes, addresses and Docker networks and pools, the overlap test, and `init`'s subnet roll |
| `internal/validate` | the rules, and nothing else |
| `internal/secretsgen` | what a deployment's secrets are, and which of them the toolkit may invent |
| `internal/kinds` | what an application kind is: its compose services, and the image each runs by default |
| `internal/acme` | what each ACME DNS provider needs: its Caddy module, the image that carries it, and its Caddyfile directive |
| `internal/render` | placement, templates, and the writer |
| `internal/dnscontinuity` | the reconciler's DNS check: reading the Cloudflare account's members (two GETs, nothing else), the keyed hash every address is compared under, and the counts the two DNS continuity monitors carry |
| `internal/dns` | which public records a deployment needs, creating the missing ones at the DNS provider, and pruning the ones it created that nothing wants |
| `internal/apply` | what to push to a host, what to restart, and the gates before either |
| `internal/siteadd` | joining a new data site: eight staged, gated, resumable stages |
| `internal/siteremove` | taking a site out: its refusals, six staged, gated, resumable stages, the host's cleaning with each removal proven, and the gateway's Caddy hand over |
| `internal/garage` | `storage init`: one site's Garage layout, and each app's S3 key and bucket; and the Garage commands and S3 requests other commands send, each with its secret kept off the command line |
| `internal/storageadd` | `storage add`: joining every Garage site into one cluster, and a change of replication factor, in staged, gated, resumable stages |
| `internal/rotatekey` | `storage rotate-key`: replacing one app's S3 key in staged, gated stages, resumed from the secrets file |
| `internal/appadmin` | an app's first administrator: probe, plan, and the per kind API calls |
| `internal/pocketid` | Pocket ID's REST API, called through curl on the host with everything variable on stdin, or over HTTP from beside it |
| `internal/oidcclient` | an app's client at Pocket ID: probe, plan, and record its credentials before sending its secret |
| `internal/webserver` | a monitor's web server: the hand-off sheet for an operator's own web server, and the read only checks run from the workstation |
| `internal/hostarch` | a host's architecture (`dpkg --print-architecture`): which are supported, the refusal when a host contradicts the one recorded for its site, and whether an image's platforms include it |
| `internal/hostcheck` | what a site claims on its host, what the host already runs, and whether that is clean, shared or a conflict |
| `internal/ownership` | what of this deployment is left over on a host, and what foreign relies on its Caddy, from the host check's inventory |
| `internal/appremove` | `app remove`: which of a host's things are a removed app's, the gate, the plan, and each idempotent step |
| `internal/preflight` | `site add`'s first stage: read only checks on the new site and every running one, as a report |
| `internal/failover` | `failover test`: its checks, the switchover and its gates |
| `internal/doctor` | `doctor`: the read commands it sends, and the findings and recovery advice made from their answers |
| `internal/patroni` | reading a Patroni cluster through the Spilo container: `/cluster`, the leader, lag, database size |
| `internal/configstore` | the signed copies of the configuration: a version as a git commit and the bare repository holding it, the rules that choose which versions to trust and which is newest, gathering from every site, pushing to them, and the key by key merge of the secrets |
| `internal/hostprep` | taking a blank host to what `apply` assumes; one profile per operating system, its shell under `profiles/<id>-<version>/` |
| `internal/render/templates` | the infrastructure templates, plus one directory per kind |

Templating is `text/template` from the standard library. No template engine is
inherited, per the language decision in `docs/decisions.md`.

### A kind ships a blueprint directory

The first party blueprints, and the images of the toolkit's own services in
`foundation.yaml`, are the core catalog's,
[`Paisans-Software/paisans-blueprints`](https://github.com/Paisans-Software/paisans-blueprints),
fetched at its newest release this binary reads (`kinds.LoadCore`,
`kinds.CoreRelease`); the binary carries only its URL, `kinds.CoreURL`, which
`PAISANS_CORE_CATALOG` overrides. Change one there, in a pull request to that
repository, and tag a release; deployments take it on their next run. Run
`scripts/fetch-core-testdata.sh <tag>` here to replace the test copy in
`internal/blueprints/testdata/core/` and its tag in `core.release`; with
`CORE_CATALOG_DIR` it copies a local checkout instead, for a change made to
both repositories before the release exists.

Links between apps are the blueprints' own: a provider lists each role it
provides with a CEL expression per value (`provides: [{role, values}]`, each a
string over the app, `app.hostnames` included), and a consumer's templates
read them with `.Linked "<role>" "<value>"`, which fails the render for a
value the provider does not declare. No role is named in Go; a consumer
pointed at a provider outside the deployment gets the values any blueprint
providing the role says of that hostname (`kinds.LinkValuesAt`).

A change to the catalog that an older binary cannot read changes the
`apiVersion` of what it changes, a blueprint's or the foundation's: an older
binary then skips that release for the newest it reads. A new key counts,
since both are decoded strictly.

Tests never fetch. A test package that reads a core blueprint or the
foundation carries a `core_test.go` importing `internal/blueprints/coretest`
blank, which makes the test copy a local repository tagged with the release
`core.release` names, points `PAISANS_CORE_CATALOG` at it, installs its commit
in a catalog cache, and allows git only local repositories. It also sets
`PAISANS_TEST_OFFICIAL_CORE` to the copy, so `blueprints.CoreIsOfficial` counts
it as the official core catalog and its foundation's images skip approval as
the real one's do; `internal/blueprints` reads that variable only in a binary
`go test` built (`testing.Testing`), so no production run can count another core
as official. A test that wants a mirror clears it with `t.Setenv`. A test that needs a cache of its own calls
`coretest.Cache(t)`, which holds the same core copy. The reserved blueprints in `internal/blueprints/reserved/` are this
repository's own and are edited in place.

A blueprint's directory, `blueprints/<kind>/` in a catalog or
`internal/blueprints/reserved/<kind>/` here, is
a set, and every template in it is rendered; its `blueprint.yaml` is read
rather than rendered. Two conventions make a set need no code of its own.

**A template's path is its destination.** The synapse blueprint's
`initdb.d/01-mas-database.sql.tmpl` lands at
`/srv/paisans/<token>/<stack>/initdb.d/01-mas-database.sql`, so where a file goes is
read off the tree rather than held in a mapping somewhere else. Adding a file
to a set is adding a file.

**A `.secret.tmpl` suffix means the rendered file is 0600.** Everything else is
0644. The mode travels with the template for the same reason the destination
does, and 0644 is the default because a rendered file the container's own user
cannot read is a stack that does not start.

The set is given one value, `appValues` in `internal/render/appview.go`, which
holds what needs the whole deployment to know: the database host for this app's
placement, the object storage endpoint, this app's own secrets and its client at
the identity provider. **The template decides what goes into which file, under
what names, in what format; Go decides nothing about an application's
configuration.** That is why three of the eight kinds, element, synapse and
writefreely, configure their application by a file rather than environment at
all.

One file in a set is not rendered beside the app: `caddy.snippet.tmpl` lands on
every gateway, at `/srv/paisans/<token>/infra/caddy/snippets/<app>.caddy`, because that is where
it is read. An app pinned to a monitor site is the exception: its snippet
lands on that monitor instead, with `managed_by: paisans`, and on no site at all
with managed_by: external (`routesFor` in `internal/render/site.go`). The gateway's own `Caddyfile` keeps only what is cross cutting,
certificates and the trusted proxy range, and gives each app a host block that
imports its snippet. A snippet never hardcodes where its application runs: it
receives the upstreams from the inventory, which is what keeps a pinned app and
a clustered app the same shape.

An app can answer on more than one hostname, and a gate can sit in front of
it, and both are properties of the app declared in `paisans-stack.yaml` rather than
anything encoded in a template's filename alone. `internal/kinds/hostnames.go`
is the closed set of extra hostname roles a kind understands beyond the
primary; `SnippetFor` maps a role to `caddy.snippet.<role>.tmpl`, so a role a
kind's template directory ships no file for cannot be declared, and a role
outside that set is refused at validate rather than discovered at render, the
same way an unknown `images` key is. `synapse` has a second role,
`wellknown`, for a homeserver's delegation documents. Every kind that stores
objects has `media`, and it is the one role that exists without being
declared: `kinds.MediaHostname` derives `<label>-media.<domain>` from the
app's own hostname, and `hostnames.media` only overrides it. It is also the one
route the app's gate does not follow, because a media hostname is never gated.
`gated` and `auto_login` work the other way. `config.Load` derives the gate
itself, an `oauth2-proxy` app named `gate` that no file declares
(`deriveGate`), whenever an app sets either, and the app's host block in the
gateway `Caddyfile` imports the gate's named snippets: `gate` ahead of the
app's own snippet when gated, `gate_autologin` on the auto-login paths, and
`gate_signin` on a public app's sign-in page. `internal/render/site.go`'s
`renderGateSnippets` renders those named snippets onto the gateway, and the
`Caddyfile` imports them at its top level, however many host blocks go on to
import them by name. Both are read in
`internal/render/appview.go`'s `values`, the same function that assembles
everything else a template set is given, so a snippet template sees them as
ordinary fields rather than as a special case.

Embedding uses `//go:embed all:templates`. Without `all:` embed skips files
beginning with a dot, and every environment configured kind ships a `.env`
template.

### Where a claim about upstream software comes from

Every environment variable name, configuration key and image tag in a template
set is checked against upstream before it is written, and the check is recorded
in a comment where it is not obvious. Some of it is counter intuitive and does
not survive being recalled: WriteFreely's `[oauth.generic]` endpoints are paths
appended to `host` rather than URLs, upstream Mbin ships named OAuth providers
and no generic OIDC one (which is why the mbin kind's default image is the
paisans fork, which adds one, and its callback is `/oauth/oidc/verify`), and
Spilo publishes a separate image repository per Postgres major version whose
tags do not run in step.

## Certificates use DNS-01, everywhere

With HTTP-01 a server can only obtain a certificate for a name that already
points at it. A new gateway could therefore not hold valid certificates until
DNS moved, and DNS should not be moved to a server without them. DNS-01 proves
control through the provider's API and needs no inbound reachability, so a new
gateway can be fully ready before a single record changes. That is what makes
moving the gateway an overlap rather than a cutover, and reversible at every
step until the old one is stopped.

It is also the only option for a hostname served behind a VPN, where nothing on
the internet can reach the host to answer a challenge.

The cost is a token with DNS edit rights on the zone, sitting on the gateway,
which is the most internet exposed machine in the deployment. Two things bound
it. The token is scoped to one zone, never an account wide credential. And an
operator who wants it narrower can CNAME every `_acme-challenge` record into a
challenge only zone and scope the token to that zone, leaving a credential on
the gateway that can write challenges and nothing else. The toolkit does not
require that arrangement, and it does not prevent it.

The token is acceptable on the gateway because anyone with root there already
terminates TLS for every hostname and holds every private key, so they can
already read and alter all traffic. The token adds reach past that machine,
which is why it is scoped to one zone.

## Every app has its own database credential

There is no fallback and no shared password. One cluster holds every app's
database, so a credential shared between apps is a credential that reads every
other app's data, and the admin password is worse again: it creates and drops
roles, and an operator uses it.

`secrets.enc.yaml` therefore carries `apps.<name>.database_password` for every
app that uses Postgres, clustered or pinned, and rendering fails by name when
one is missing. The kinds with no Postgres need none: Element is a static
client with no server side state, oauth2-proxy keeps its session in a cookie,
and uptime keeps its own SQLite file under `/data`. WriteFreely is not among
them, because the `writefreely` kind is the writefreely-wisp fork, which
connects to Postgres.

`apply` creates the roles and databases for clustered apps, on a site in
`cluster.sites`, after the infrastructure stack and before any app stack. It
waits up to three minutes for Patroni's `/cluster` to name a running leader,
then sends one psql script on stdin to the Spilo container: create the role if
missing, set its password every time (so rotation is editing the secret and
applying), create the database owned by it if missing. A replica creates
nothing and reads its own catalogue: it goes on to the app stacks only when
every app database and role is there, and otherwise stops with
`apply.ErrDatabasesWait`, naming the leader's site to apply first, which
`paisans apply` treats as a pause. A leader that is not one of
`cluster.sites` is not a replica and stops the apply. A timeout or a psql
failure stops it too, before any app starts, and the next apply resumes there. `README.md` has
the reasoning under "`apply` creates each clustered app's role and database".
A pinned app's own Postgres creates its role from the image's environment, as
before.

## Things the code enforces that are easy to undo by accident

* **Trusted proxies are the mesh subnet**, never a host address. This is
  expensive to retrofit and fails quietly, so it is not configurable and a test
  asserts it.
* **The mesh subnet is declared in `mesh.subnet`**, not derived from the site
  addresses. Deriving the tightest network that fits them would widen it the
  moment a site was added, silently changing `TRUSTED_PROXIES` in every app.
  Declaring it also lets an operator avoid a range their hosts already route.
  Every site address must sit inside it, and that is a refusal.
* **`etcd.members` and `cluster.sites` are resolved when the file loads.**
  `config.Load` fills each list the file leaves out from the roles, into the
  same field a written key fills, and records it in `Etcd.MembersDerived` and
  `Cluster.SitesDerived`. Every command reads the field, so there is one list.
  A `config.Config` built in a test leaves both flags false, which reads as
  written. Whether a key is written comes from the yaml node, not the decoded
  slice, since `members: []` and a key left out decode alike; a decoded list
  the walk cannot see, behind a merge key, also counts as written. The flags
  word messages (`EtcdMembersKey`, `ClusterSitesKey`, `site remove`'s stage 5
  and its two voter refusal, `validate`'s voter advice), hold `validate`'s two
  `cluster.sites` comparisons and its `undeclared-site` check to a written
  list, and make `WithoutSite` derive a derived list again from the remaining
  roles.
  The yaml writers (`RemoveSite`, `RemoveSiteAndWitness`) read the file
  itself and edit only a key it has. See
  `docs/specs/2026-10-10-derived-members.md`.
* **Every port a site binds is listed once, in `render.SiteListeners`**, from
  the constants the templates render. `validate`'s `port-collision` reads it,
  so a new listener added to a template without a constant, or a new kind's
  port added to its record (`kinds.Def.Port`) without a look at the others, escapes the check.
  `TestPublishedPortsAreDistinctPerSite` in `internal/render` catches the
  second against the fixture; nothing catches the first.
* **A pinned stack uses bind mounts under `/srv/paisans/<token>/<stack>/`**, never named
  volumes, so relocating it is one `tar`. A test walks every rendered compose
  file to confirm it.
* **A clustered app has no Postgres service of its own** and connects to the
  HAProxy on its own site, at that site's mesh address and the cluster port
  (Eg: `10.44.0.1:5000`), never `127.0.0.1`, which inside the app's container
  is the container itself. A pinned app gets its own container: an app that is
  pinned must be pinned all the way down.
* **Failover and the 503 live in the gateway file**, as the named snippets
  `upstream_failover` and `upstream_unavailable`, not in each kind. A kind's
  snippet imports `upstream_failover` when it has more than one upstream and
  never sets `lb_policy` itself; every host block imports
  `upstream_unavailable`. `TestMultiSiteAppsShareOneFailoverSetting` and
  `TestEveryHostBlockAnswers503WhenNoSiteCan` hold both. README's "Failing
  over, and saying so when nothing can serve" has the reasoning.
* **Mbin's `LOCK_DSN` stays `flock`**, and only `CLUSTER_LOCK_DSN` points at
  the database. Every Symfony rate limiter takes the default lock store in
  FrankenPHP's long lived web workers, where a database store's private
  connection is never reopened once HAProxy closes it; a first version of this
  change made exactly that mistake. `CLUSTER_LOCK_DSN` is `DATABASE_URL`
  unchanged, for the fork's planned named `cluster` lock resource, and is inert
  until a fork release reads it. `TestMbinSplitsItsLocks` asserts both for
  clustered and pinned placement. `README.md` has the reasoning under "Mbin's
  locks are split: flock by default, Postgres for the few that cross sites".
* **Mbin's queues default to Postgres** (`settings.queue`, read by the mbin
  blueprint's templates as `.Setting "queue" "postgres"`),
  with no broker in the stack; `queue: rabbitmq` renders the broker stack as
  before. `TestMbinQueuesInPostgresByDefault` and `TestMbinRabbitMQIsAnOptIn`
  hold both, and the blueprint's checks refuse an unknown value and warn for
  RabbitMQ on more than one apps site. The default needs a paisans fork image with the
  Doctrine transport decorator. README: "Mbin's queues are in Postgres by
  default".
* **Pocket ID's standby is keyed on its log text**, pinned per image in
  `kinds.PocketIDStandbyMarker`. `TestDefaultPocketIDImageHasItsStandbyMarker`
  fails on an image bump until the new text is recorded; record it from the
  release's `bootstrap.go`, then run the `standby_integration` tests above.
  The instance check asks for the wrapper's state file **before** `/healthz`:
  a standby's port is closed, so the other order reads every standby as
  down. `TestTheInstanceQuestionAsksTheStateFileThenTheMeshPort` holds the
  order. And the compose file names the image's entrypoint and command as the
  wrapper's arguments because setting `entrypoint` clears the image's
  command; dropping them leaves the wrapper running nothing.
* **A published port binds the site's mesh address**, never every interface,
  because Docker's iptables rules bypass a host firewall. A test walks every
  rendered compose file to confirm it.
* **Nothing rendered carries a floating image tag.** `latest` is refused in an
  operator's configuration, so a default that floated would be the toolkit
  refusing what it writes itself. A test walks every rendered compose file.
* **A file carrying a credential is 0600.** A bind mounted file at 0600 is not
  readable through `docker inspect` or `/proc/<pid>/environ`, which is the whole
  reason a file configured application is the better case. A test asserts it
  against the fixture's placeholder credentials.
* **Output is deterministic.** Sort before you iterate a map.
* **host prepare removes only authorized keys it added**, as recorded in
  `/etc/paisans/authorized_keys.<user>.paisans-<token>.owned`, and never rewrites a key's
  comment to mark it. Removals run last, and a plan that would leave the user
  with none of the listed keys is refused. Tests in
  `internal/hostprep/hostprep_test.go` cover add, adopt, forget, remove, a
  restricted key and that refusal.
* **No flag answers the review.** `apply`, `host prepare` and `site add` ask for
  `approve` at a terminal when what `paisans-stack.yaml` grants adds or changes
  something, and refuse without one, before the claim. A flag that answered
  it would end up in every script, and approving is the control. Tests in
  `cmd/paisans/review_test.go` and `converge_review_test.go` drive the prompt
  through a pipe.
* **host prepare never appends a key the host's allowlist lacks**, checked in
  the plan and again under the lock as the append runs. Tests in
  `internal/hostprep/allow_test.go` run the append against real files.
* **The SSH allow follows `ssh.port` and is never removed**, including the
  allow for a port the site used before. A test moves the port and asserts the
  old allow is kept and noted.

## What is not here yet

No `backup`. `site add` exists for sites that all declare an endpoint; the
relay for sites behind NAT is still design. `storage add` joins Garage nodes,
changes the replication factor, sizes each node and can stop one to prove
failover, and has run against containers; replacing a Garage node is not
built, and removing one goes with its site, in `site remove`, where an evicted
site's node is skipped as dead. `site add`'s first stage is `preflight`, and
`failover test` exists; both are described above. `apply`
pushes files, brings up the mesh interface, creates clustered apps' roles and databases, and
takes the narrowest action that makes the rest live. `site add` has never been
run against a real host, so every command it sends is reasoned from upstream
source and documentation, not observed. Nor has `site remove`, whose commands are reasoned
the same way.
Nor have the review of what `paisans-stack.yaml` grants, its record on each host,
the allowlist's refusal in `host prepare`, and `host allow` and `host
disallow`: the shell they send is run against local files in the tests, not
observed on a host. Nor `site remove --evict` and `mesh rekey`: Patroni's failover
once its leader is cut off, `garage layout skip-dead-nodes` on a node that
is down, a WireGuard key swapped by `wg syncconf`, and `patronictl pause
--wait` and `resume --wait` around a relay's swap, with the pause read back as
`pause: true` in `patronictl show-config`, are read from upstream source
(Patroni v4.1.0, `patroni/ctl.py`) and documentation, and have run only
against fakes.

`secrets rotate` has never been run against a real host either. Its `ALTER
ROLE` on the leader and the Patroni restarts and switchover around a cluster
password, `rabbitmqctl change_password` in Mbin's broker, every Garage node
restarted together for `rpc_secret`, and whether each app ends the sessions
the plan says it does are read from upstream source and documentation, cited
in `internal/secretsrotate`, and have run only against fakes.

The signed copies of the configuration have run only against fakes and local
bare repositories standing in for sites: the trust rules against built
histories, publishing and gathering through a fake transport, and the host
prepare steps against a fake host. The shell that pushes to a store, bundles it
for a pull, and the rendered sync script have run with a local `sh` against
local bare repositories, with the host's paths moved into a temporary directory
and each peer's `git://` address replaced by its directory. Nothing has been run
against a real host, so the shell that creates the store, the daemon's reading
of it as `nobody` (with `safe.directory` set for its one repository by
environment) and the sync's fetch between real mesh addresses are reasoned from
git's documentation, not observed; `git ls-remote` against a prepared host's
daemon is the check to make first. Nor has `apply`'s publishing after the
`host prepare` steps, or the witness refusal, run on a real deployment.

Pocket ID on more than one apps site has run only in containers on one
machine: two real instances against one Postgres, through the rendered
wrapper and compose file. It has not run across the mesh, so the gateway's
handling of a standby's closed port is reasoned, not observed.

Mbin's media reverse proxy is rendered, as one of the per app media
hostnames: each kind that stores objects ships a `caddy.snippet.media.tmpl`,
and the gateway gives each such app a site block for its own
`<label>-media.<domain>`. See rule 3 in `README.md`.

Mbin's own OAuth2 server keypair, which API clients and mobile apps need, is
generated by `init` and rendered to `/srv/paisans/<token>/<app>/oauth/` on every site running
Mbin; README's *Three kinds of secret* says why it is 0644. Nothing chowns any
of the stack's bind mounted directories; the header of
the core catalog's `blueprints/mbin/compose.yaml.tmpl` says which containers cope with a root
owned directory and which do not.

The transport layer is `apply.SSHTransport`, described above under *Why ssh is
shelled out to and sops is not*. If you find yourself writing a second one,
that wants its own review, because every command reaches a host through the
first.

Two claims used to live in this section and are corrected here rather than
left to go on being read. **Secret generation exists**: `internal/secretsgen`
and `paisans init` are implemented, described above under `init`. **The
gateway validation gate did not land with `apply`, because it did not need
to.** It is enforced today, described above under "`apply`, and the gates in
it": the assembled gateway configuration is validated before any reload, and
a failure stops the reload rather than taking every hostname down with it.
