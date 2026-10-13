# Blueprints and catalogs

Date: 2026-10-12. Status: approved by the founder in session, 2026-10-12.
Implemented 2026-10-13, in pull requests #209, #211, #212, #213, #214, #215,
#217, #218 and #221; the core catalog is
[`Paisans-Software/paisans-blueprints`](https://github.com/Paisans-Software/paisans-blueprints).

**Where the implementation differs from the design below**, each because the
simpler form covered every blueprint there is:

* The format is the keys `kinds.Parse` reads, which group as this design does
  but are not all spelled as its examples are: `config.setting_keys` and
  `config.omitted_keys`, `storage.s3` and `storage.local_only`,
  `secrets.generate`, `secrets.required` and `secrets.self_rotated`,
  `federation.federates` and `federation.setting`, and `oidc.launch_path` and
  `oidc.no_launch`. A blueprint with no `oidc` signs nobody in; there is no
  `oidc: none`.
* A blueprint's templates are every file in its directory but
  `blueprint.yaml`; there is no `templates` list.
* A service has no `when`: a compose template renders a service or not itself.
* A secret rotates by re-rendering its stack (`secrets.self_rotated`); a
  blueprint cannot yet declare rotation steps run in its containers.
* A setting's `default` is filled in only for a blueprint read from a
  catalog, and only into what that blueprint's templates and CEL expressions
  see, never into the configuration: the built in blueprints' Go code and
  validate's rules give a setting that is absent a meaning of its own (an
  absent `authorized_fetch` is on).
* An expression sees no `app.name`, `app.oidc` or `deployment.sites`; the
  variables are the ones `internal/kinds/cel.go` declares.
* Mbin's lock list is not a key: `MBIN_LOCKED_SETTINGS` is rendered from Go
  (`MbinLockedSettings` in `internal/render/mbin.go`), so `blueprint lint` has
  no lock list to check it against.
* `placement` is not a key: an engine other than postgres is pinned only, as
  the cluster rules already decide.
* **Catalogs are unsigned.** Signing, as *Signing* below describes it, was
  built and then removed on 2026-10-13 (`docs/decisions.md`, *Catalogs are
  unsigned, trusted by their pin*). A pin has no `signers` or `unsigned` key,
  and the core catalog may be pinned to a commit of its own or a mirror like
  any other catalog.

Every application the toolkit can run is compiled into the binary today. What
the toolkit knows about a kind is spread over about thirty files: the service
and image list in `internal/kinds/kinds.go`, its configuration file, its OIDC
client spec, its health route, its gate settings, its passthrough keys in
`internal/render/config.go`, its secrets in `internal/secretsgen` and
`internal/secretsrotate`, its checks in `internal/validate`, and its templates
under `internal/render/templates/<kind>/`. Adding an application means a
toolkit release, and only the toolkit's maintainers can make one.

This spec makes an application a **blueprint**: a directory holding a
`blueprint.yaml` and the templates it renders, read from a **catalog**, which
is a git repository of blueprints. Anyone can publish a catalog; a deployment
names the catalogs it uses in `paisans-stack.yaml`, pins each to a signed tag,
and keeps a copy of every blueprint it runs in its signed configuration store.
The first party applications move to a **core catalog** that every deployment
uses unless it says otherwise.

Four blueprints stay in the binary and cannot be provided by any catalog:
`pocket-id`, the gate, `invite` and `uptime`. They are the identity provider,
the services that write to it or are derived from the whole deployment, and
the one monitor every other blueprint is watched by.

The deployment file is named `paisans-stack.yaml` here, following the rename in
progress on `feat/rename-paisans-stack-yaml`.

## Settled

Founder decisions, 2026-10-12, in session:

* **Applications are declarative blueprints, and the logic that cannot be
  declared stays in the binary** as named capabilities and helpers that any
  blueprint may use, third party ones included.
* **Catalogs are git repositories.** A deployment declares the catalogs it uses
  by URL. With none declared it uses the core catalog at a URL compiled into
  the binary.
* **The nouns are blueprint and catalog.** The `kind:` key of an app becomes
  `blueprint:`.
* **Predicates and checks in a blueprint are CEL expressions.**
* **`pocket-id`, the gate, `invite` and `uptime` are reserved**, built into
  the binary and never read from a catalog.
* **Uptime is the only monitor.** There is no monitor role a blueprint can
  provide; the seed stays in Go, written for the uptime fork.
* **A blueprint with no health route is still monitored**, by a weaker
  fallback check, with a warning.
* **A deployment with no monitor stays a warning** (`no-uptime-monitor`), as
  today.
* **Declaring an app approves its OIDC client, whatever catalog its blueprint
  comes from.** `paisans apply` creating the client of an app declared in
  `paisans-stack.yaml` already needs no approval of its own; a blueprint from a
  third party catalog is covered the same way, because declaring the app and
  pinning its catalog are the operator's choices of what to run.

## Scope

In:

* the blueprint format, `blueprint.yaml` and its templates;
* catalogs: declaring, pinning, fetching, verifying and updating them;
* name resolution and the reserved names;
* capabilities and helpers, and the contract they offer blueprints;
* signing catalogs;
* the snapshot of every blueprint in use in the configuration store;
* what `validate`, the grant review, `doctor` and the uptime seed read from a
  blueprint;
* the order of the work.

Out:

* arbitrary code in a blueprint. A blueprint runs commands only inside its own
  containers (see *In-app actions*); nothing from a catalog runs on the
  operator's workstation.
* a second monitor. See *Uptime watches every blueprint*.
* creating the core catalog repository, which is publishing and the founder's
  action.

## A blueprint

A blueprint is a directory in a catalog:

```
<catalog>/
  catalog.yaml
  blueprints/
    <name>/
      blueprint.yaml
      compose.yaml.tmpl
      caddy.snippet.tmpl
      <config file>.tmpl
```

`blueprint.yaml` holds everything the toolkit now keeps per kind in Go maps and
switches. The templates are what `internal/render/templates/<kind>/` holds
today, with the same conventions: `.tmpl` is dropped when rendered, and
`.secret` marks a file that carries a secret.

### The fields

| Field | What it says | Replaces |
|---|---|---|
| `apiVersion` | `paisans.blueprint/v1` | |
| `name` | the blueprint's name within its catalog | the `config.Kind` constant |
| `requires_toolkit` | the oldest toolkit version that can render it | |
| `port` | the port the app serves on | `appPort` in `internal/render/plan.go` |
| `services` | each compose service, its default image, an optional `when`, and a `port` for one the gateway routes to directly | `catalogue` in `internal/kinds/kinds.go` |
| `database` | `engine` (`postgres`, `mysql`, `sqlite`, `none`), for a container of its own `service`, and `extra`, databases kept beside the app's own in the same Postgres | `UsesPostgres`, `PostgresService` |
| `placement` | the placements it allows | `cluster-placement-without-a-cluster` and the pinned only kinds |
| `storage` | `local`, `s3`, `public_reads`, and for `local_only` its `local_only_reason` | `UsesObjectStorage`, `ServesObjectsPublicly` |
| `settings` | each `settings:` key it reads: type, default, allowed values | per kind constants such as `MbinQueueSetting` |
| `config` | the passthrough file's name and format, and its lock list, `locked_settings` | `configFiles`, `mbinLockedSettings` |
| `provides`, `consumes` | the roles linking it to other apps, `matrix-homeserver` the first | `homeserverServerName` |
| `oidc` | `none`, or the client spec: callback, launch path, PKCE, group settings, whether it reads groups | `OIDCClient` |
| `gate` | what the gate leaves open, the settings that close it, the probe and the live read | `registerGateSettings`, `SignedFetchFor`, `MbinPrivateAPIProbe` |
| `health` | the route and the status codes that mean healthy, or `tcp: true` | `HealthFor` |
| `mail` | whether it reads the deployment's SMTP settings | `SendsMail` |
| `secrets` | each secret: how it is generated and how it is rotated | `internal/secretsgen`, `internal/secretsrotate` |
| `backup` | for an engine other than Postgres, the dump and restore commands | |
| `checks` | CEL rules `validate` runs, each a refusal or a warning | per kind checks in `internal/validate` |
| `templates` | the files it renders | `internal/render/templates/<kind>/` |

### Example: Mbin, as the core catalog would ship it

The values below are the ones the toolkit renders, from
`internal/blueprints/core/mbin/blueprint.yaml`, `internal/kinds/mbin.go` and
`internal/render/mbin.go`, shortened: the gate's open paths and the comments
that cite the fork's source are left out. The `settings` and `checks` are the
ones Go keeps for Mbin today (`MbinQueueSetting`, `MbinContactEmailSetting`
and the rules in `internal/validate`), written as a catalog blueprint would
declare them.

```yaml
apiVersion: paisans.blueprint/v1
name: mbin
requires_toolkit: ">= 0.1"
port: 8080
database: { engine: postgres }
storage: { s3: true, public_reads: true }
hostname_roles: [media]
federation: { federates: true }
health: { path: /healthz, status: [200] }
mail: reads

settings:
  queue:         { type: enum, values: [postgres, rabbitmq], default: postgres }
  contact_email: { type: email }
  admin_group:   { type: string }
  member_group:  { type: string }

# app comes first; postgres is the app's own database when it is pinned. The
# compose template renders rabbitmq and amqproxy only when settings.queue is
# rabbitmq.
services:
  - { name: app,      image: "ghcr.io/paisans-software/mbin:1.15.1-paisans" }
  - { name: amqproxy, image: "docker.io/cloudamqp/amqproxy:3.2.0" }
  - { name: rabbitmq, image: "docker.io/library/rabbitmq:3.13.7-management-alpine" }
  - { name: valkey,   image: "docker.io/valkey/valkey:9.1.2-trixie" }
  - { name: postgres, purpose: "its own database, when the app is pinned" }

config:
  file: .env
  format: env
  setting_keys:
    - { key: MBIN_AUTHORIZED_FETCH,   file: .env, setting: authorized_fetch }
    - { key: OAUTH_OIDC_ADMIN_GROUP,  file: .env, setting: admin_group }
    - { key: OAUTH_OIDC_MEMBER_GROUP, file: .env, setting: member_group }

oidc:
  callback: /oauth/oidc/verify
  launch_path: /oauth/oidc/connect
  pkce: true
  reads_groups: true
  admin_group_setting: admin_group
  member_group_setting: member_group
  default_admin_group: admins
  default_member_group: members

gate:
  gateable: true
  leaves_open: [activitypub, api_tokens]
  inbox_paths: ["/i/inbox", "/f/inbox", "/u/*/inbox", "/m/*/inbox"]
  token_paths: ["/api/*"]
  sign_in_path: /login
  private_probe: { path: /api/entries, status: [401] }
  settings:
    - name: MBIN_PRIVATE_INSTANCE
      want: "true"
      when: "app.gated"
      why: "the gate lets a request with a bearer token reach /api/*"
      config_key: MBIN_PRIVATE_INSTANCE
    - name: MBIN_USE_FEDERATION_ALLOW_LIST
      want: "true"
      when: "app.gated"
      why: "without the allow list Mbin answers a signed read from any instance"
      config_key: MBIN_USE_FEDERATION_ALLOW_LIST
    - name: MBIN_AUTHORIZED_FETCH
      want: "true"
      when: "app.gated && app.federates"
      why: "without authorized fetch Mbin answers an unsigned read"
      config_key: MBIN_AUTHORIZED_FETCH
      setting: authorized_fetch
  live: { action: env-lock-list, service: app, lock_variable: MBIN_LOCKED_SETTINGS }

checks:
  - id: mbin-smtp-from-not-an-email
    level: refuse
    key: smtp.from_address
    rule: "!has(app.smtp.from_address) || isBareEmail(app.smtp.from_address)"
    message: "Mbin fails to send any mail unless the sender is an email address alone, Eg: hello@example.org."
  - id: mbin-without-smtp
    level: warn
    key: smtp
    rule: "has(app.smtp.host)"
    message: "no smtp host, so MAILER_DSN=null://null and every mail Mbin sends is discarded."

secrets:
  generate: [mercure_jwt_secret, rabbitmq_password, valkey_password, app_secret,
             oauth_passphrase, oauth_encryption_key, oauth_private_key, oauth_public_key]
  self_rotated: [app_secret, mercure_jwt_secret, valkey_password]
```

Its `.env` template quotes each value with `envquote`, a helper any blueprint
may call:

```
MAILER_DSN={{ envquote .MailerDSN }}
KBIN_CONTACT_EMAIL={{ envquote .MbinContactEmail }}
MBIN_LOCKED_SETTINGS={{ .MbinLockedSettings }}
```

`.MailerDSN`, `.MbinContactEmail` and `.MbinLockedSettings` are Mbin only
values, worked out in Go in `internal/render/mbin.go`: the mailer DSN is in
Symfony's syntax, which only Mbin reads, and the other two follow Mbin's own
rules. They are not general helpers and not part of the contract below. A
blueprint of its own reads a setting with `.Setting`, Eg:
`{{ envquote (.Setting "contact_email" "") }}`.

### Example: a third party blueprint with its own database and no OIDC

The motivating case is a blog engine that keeps its data in MySQL and has its
own sign in. The example is a made up `journal` app. Its image, port and routes
are placeholders, not a claim about any real program.

```yaml
apiVersion: paisans.blueprint/v1
name: journal
requires_toolkit: ">= 0.1"
port: 8000
services:
  - { name: app, image: "ghcr.io/example/journal:2.4.1@sha256:<digest>" }
  - { name: db,  image: "docker.io/library/mysql:8.4@sha256:<digest>" }
database: { engine: mysql }
config: { format: env, file: .env }
gate: { gateable: true }
storage: { local_only: true }
mail: reads
health: { path: /status, status: [200] }
backup:
  dump:    { service: db, run: [mysqldump, --single-transaction, journal] }
  restore: { service: db, run: [mysql, journal] }
secrets:
  generate: [db_password]
```

```yaml
# paisans-stack.yaml
catalogs:
  example:
    url: https://github.com/example/paisans-blueprints
    tag: v1.0.0
    commit: <40 hex>
    signers: ["ssh-ed25519 AAAA<key> example release key"]
apps:
  journal:
    blueprint: example/journal
    hostname: journal.example.org
    placement: { pinned: vm }
    gated: true
```

`engine: mysql` makes the blueprint pinned only, the same way the uptime kind
is today: the cluster is Patroni, and a database it does not run cannot fail
over with it. With no `oidc` section its users sign in with the app's own
accounts; `gated: true` still puts the member gate in front of it, which needs
no OIDC support from the app at all. With no `leaves_open`, the app exposes
nothing the gate passes through, so the gate alone is the whole control.

## Predicates and checks are CEL

Every `when`, every `checks[].rule` and every gate setting's condition is a
[CEL](https://cel.dev) expression. CEL is typed, cannot loop, has a cost limit
the toolkit sets, and evaluates without I/O, so a catalog can add a condition
without adding code. `cel.dev/expr` is already in the module graph as an
indirect dependency; the evaluator is `github.com/google/cel-go`.

An expression sees:

| Variable | Holds |
|---|---|
| `app.name`, `app.hostname` | the app's key and hostname |
| `app.gated`, `app.auto_login` | the two gate booleans |
| `app.federates` | what `kinds.Federates` answers today |
| `app.oidc` | whether the app has a client |
| `app.placement` | `cluster`, or the site it is pinned to |
| `app.settings` | its `settings:` map, with the blueprint's defaults filled in for a blueprint from a catalog |
| `app.smtp` | its resolved SMTP settings, without the password |
| `app.sites` | the sites it runs on |
| `app.storage` | `mode` (`object` or `local`), `bucket` and `media_hostname` |
| `deployment.sites` | site names and their roles |

A gate setting's condition sees the app alone, so `app.name`, `app.smtp`,
`app.sites`, `app.storage` and `deployment.sites` are empty in one.

The toolkit adds functions where CEL has none. The first are `isBareEmail`
(what `isAddressAlone` in `internal/validate/mbin_mail.go` checks) and
`isHostname`, beside CEL's string extension. A function is part of the helper
contract below.

A check gives its `message` as text, or as `message_expr`, a string
expression over the same variables, so the message can name the value that is
wrong. Its key, relative to `apps.<name>`, is `key` or `key_expr` the same
way, and `hint` is its one line summary.

## Capabilities, helpers and in-app actions

The Go code that a blueprint cannot replace falls into three groups. Each is
named for its mechanism, never for the app that first needed it, because any
blueprint may use any of them.

**Capabilities** are writes the toolkit makes to a system outside the app on
the app's behalf. A blueprint asks for one and gives it parameters; the toolkit
makes the write, so the write is the same code whoever asked.

| Capability | What the toolkit does |
|---|---|
| `oidc-client` | creates and records the app's client at Pocket ID, restricted to its groups |
| `postgres-role` | creates the app's role and database on the cluster, or its own Postgres when pinned |
| `s3-bucket` | creates the app's bucket in Garage, public for reads only with `public_reads` |
| `gate` | puts the member gate in front of the app when it sets `gated` or `auto_login` |
| `smtp` | gives the app the deployment's resolved mail settings |

**Helpers** are template functions with no side effects, the ones
`templateFuncs` in `internal/render/site.go` lists: `quote`, `indent`,
`regexquote`, `envquote` and `yesno`, which take values and return a string,
and `gateSessionCookie`, `gateMarker` and `configFile`, which return the
toolkit's own names so a template cannot disagree with them. A template reads
an app's settings with `.Setting "key" "fallback"` and `.SettingBool "key"
fallback`, which return what the app declares, else the blueprint's default,
else the fallback. A service with a `port` of its own is reached at
`index .ServiceUpstreams "<service>"`, on `index .ServicePorts "<service>"`,
an extra database is named `index .Databases "<name>"`, `.SMTPDSN` and
`.SMTPSender` are the resolved mail settings as a Symfony DSN and a From
address, `.LockedSettings "<separator>"` joins the blueprint's
`config.locked_settings` that apply, `stableULID` derives a ULID from an
app's name, and `.Linked "<role>" "<value>"` reads what the provider of a
consumed role offers. Values the toolkit
works out in Go for one blueprint, such as Mbin's mailer DSN (`.MailerDSN`),
are that blueprint's, not helpers.

**In-app actions** run a command inside one of the app's own containers: a
gate setting's live read (`env-lock-list`, which is today's `mbinLive`), a
secret's rotation steps, and a backup's dump and restore. The command comes
from the blueprint, which adds no trust the app does not already have: its
image already runs whatever its author put in it. What the toolkit enforces is
the boundary. A live read returns only the named settings, never the rest of an
environment that also holds secrets, which is what `mbinLive` enforces today. A
command runs in a service the blueprint itself declares, never in another
app's stack.

### These are a contract

Third party blueprints use capabilities, helpers and actions, so changing one
breaks blueprints nobody here has read. Each is versioned with the toolkit: a
blueprint names the oldest toolkit it needs in `requires_toolkit`; a name is
deprecated for at least one release before it is removed; and a blueprint that
names a capability, helper, action or CEL function the toolkit does not have is
refused by name, rather than rendered without it.

### Reserved capabilities

Some writes belong only to the reserved blueprints: calls to Pocket ID's admin
API, admin reconciliation, the derived member groups, the uptime seed and the
invite service's account writes. No catalog blueprint can ask for one. A
blueprint that names one is refused.

## Reserved blueprints

| Blueprint | Why it stays in the binary |
|---|---|
| `pocket-id` | It is the identity provider. Whoever controls its blueprint controls its image, and its image holds every sign in, group and admin flag. The toolkit calls its API with the operator's key, and it runs the admin reconciler built from `cmd/admin-reconciler`. |
| the gate (`oauth2-proxy`) | No file declares it: the toolkit derives it from every app that sets `gated` or `auto_login`, and every such app depends on it. |
| `invite` | It creates Pocket ID users on its own credential. A community approves it once, for its design; a replacement under the same name would inherit an approval given to something else. |
| `uptime` | Its seed is built from every other app, and it carries the admin continuity heartbeat that the admin reconciler's design depends on. |

They are written in the blueprint format, so the format has no feature a
reserved blueprint needs and a catalog cannot use, and are embedded in the
binary with the Go that drives them. Their image and that Go are released
together, which is what keeps the code that calls Pocket ID's API matched to
the Pocket ID it calls.

An operator can still patch a reserved blueprint's image without a release,
through the existing per service `images:` key. The grant review shows that
override as a change to a reserved service, since it is the one way left to
change what runs the identity provider.

Replacing a reserved blueprint with other software, another identity provider
or another monitor, is a change to the toolkit, not to configuration. That
matches who decides: changing the identity model is a human decision, and
making it a code change puts a review in front of it.

## Catalogs

A catalog is a git repository with a `catalog.yaml` at its root and a
`blueprints/` directory:

```yaml
# catalog.yaml
apiVersion: paisans.catalog/v1
name: example
requires_toolkit: ">= 0.1"
```

### Declaring them

```yaml
# paisans-stack.yaml
catalogs:
  example:
    url: https://github.com/example/paisans-blueprints
    tag: v1.3.0
    commit: 4f2a<...40 hex>
    signers:
      - "ssh-ed25519 AAAA<key> example release key"
```

* **`url`** is anything `git clone` accepts. The toolkit fetches with the
  `git` binary, as `internal/configstore` already does, so a private catalog
  works with the operator's own git credentials and the toolkit stores none.
* **`tag` and `commit`** are the pin. Rendering uses the commit; the tag is
  what `catalog update` moves and what the operator reads. A tag that now names
  a different commit is refused.
* **`signers`** are the keys a tag must be signed by (see *Signing*).
* **`unsigned: true`** accepts a catalog with no signed tags, and the grant
  review says so every time the pin moves.

The pin is recorded in the deployment file itself rather than in a lock file
of its own, so it is signed with the rest of the configuration and reviewed
with it.

### The core catalog

The core catalog holds every first party blueprint that is not reserved:
`element`, `mbin`, `outline`, `synapse` and `writefreely`. Its URL is compiled
into the binary: `https://github.com/Paisans-Software/paisans-blueprints`.

* **It is always there.** A deployment with no `catalogs:` key uses it, and so
  does one that declares other catalogs. Declaring a third party catalog never
  drops the core one.
* **The binary pins it.** Each toolkit release records the core commit it was
  tested against, and a deployment that does not pin core uses that commit. A
  given binary therefore renders the same core blueprints every time, with or
  without network access.
* **The binary carries a copy.** That commit's blueprints are embedded at build
  time, so `paisans create`, the test suite and a recovery on a workstation that
  cannot reach the forge all work.
* **It can be mirrored, not modified.** `catalogs.core.url` points core at a
  mirror. A mirror's tags must verify against the core release keys embedded in
  the binary, and `catalogs.core.signers` cannot be set. A changed copy of a
  core blueprint is a third party catalog, with qualified names.

### Names

| Written | Resolves to |
|---|---|
| `blueprint: pocket-id` (or `invite`, `uptime`) | the reserved blueprint in the binary, always |
| `blueprint: mbin` | the core catalog |
| `blueprint: example/journal` | the `journal` blueprint in the catalog declared as `example` |

An unqualified name never resolves to a third party catalog, so no catalog can
stand in for `mbin`, let alone `pocket-id`. A catalog blueprint whose name is a
reserved name is refused, so `example/pocket-id` cannot exist to be confused
with the real one.

### Commands

| Command | What it does |
|---|---|
| `paisans catalog add <name> <url>` | fetches the newest signed tag, shows its signer's key for the operator to trust, and writes the pin into `paisans-stack.yaml` |
| `paisans catalog update [<name>]` | moves a pin to the newest signed tag and shows, per blueprint in use, what its grants change: images, ports, volumes and mounts, capabilities, in-app actions |
| `paisans catalog remove <name>` | removes a catalog; refused while an app uses one of its blueprints |
| `paisans catalog list` | the declared catalogs, their pins and whether each is signed |
| `paisans blueprint list` | every blueprint the declared catalogs offer, and the reserved ones |
| `paisans blueprint show <name>` | one blueprint's fields and templates |
| `paisans blueprint lint <dir>` | for catalog authors: the checks `validate` makes of a blueprint, run against a directory |

`blueprint lint` is how the cross checks the toolkit's own tests make today
reach catalog authors. `TestMbinLockedSettingsAreRendered` checks Mbin's
template against its lock list in both directions; the lint makes the same
check of any blueprint with a lock list. It also fails a template whose
literal `.Setting` or `.SettingBool` fallback differs from the default the
blueprint declares for that setting, so a default is stated once.

## Signing

Removed on 2026-10-13: catalogs are unsigned (see the status above). This
section is the design as approved.

Catalogs are signed at their release tags, with git's SSH signatures. A
commit is not required to be signed. `catalog update` moves to the newest
signed tag, never to a branch head, so catalogs have real versions.

**Trust starts from the operator.** `catalog add` shows the key that signed the
newest tag, with its fingerprint, and records it in `signers` only when the
operator accepts it. This is the configuration store's rule, that trust starts
from the operator's own decision and never from what a remote offers.

**Every fetch is verified.** The toolkit writes `signers` to a temporary
allowed signers file and runs `git verify-tag` with `gpg.format=ssh` and
`gpg.ssh.allowedSignersFile` set to it. A tag that is unsigned, signed by a key
not in `signers`, or pointing at a commit other than the one pinned is refused.

**A signing key changes through the old key.** A catalog lists its keys in
`keys/allowed_signers`. A new key is accepted without asking only when it
appears there in a tag signed by a key already trusted. Any other change of key
stops the update and shows the new fingerprint for the operator to accept.

**The core catalog's keys are in the binary.** Core release tags are signed in
the release workflow, and core cannot be marked `unsigned`.

**Sites trust the operator, not the catalogs.** The operator's workstation
verifies a catalog; the blueprints it verified go into the configuration store
version the operator signs (see *The snapshot*); the sites check the
operator's signature, as they already do. A site never fetches from a catalog
and never holds a catalog's key.

What signing adds to the commit pin is the update path. The pin already fixes
what a deployment renders. A compromised forge account, a transferred
repository, or a deleted one whose name is registered again can each publish a
new tag; none of them can publish one signed by the trusted key.

## The snapshot in the configuration store

Every blueprint an app uses is copied, as resolved and verified, into the
configuration store version that `apply --execute` or `config publish`
publishes, under a `blueprints/<catalog>/<name>/` tree beside the three files.
Reserved blueprints are not copied; they are in the binary.

* **Removal never depends on a catalog.** `app remove`, `site remove` and
  recovery need the blueprint of what they remove, and a catalog's author
  deleting their repository must not be able to stop that.
* **Every admin and every site renders the same bytes.** The snapshot is what
  was rendered, signed by the admin who published it.
* **Review covers blueprints.** The grant review shows what a blueprint
  changed since a host's last apply, the way it shows what the deployment file
  changed.

Git's object names are SHA-1, so the snapshot also records a SHA-256 of each
blueprint's files, and a version whose files do not match is refused on the
same terms as one with a bad signature.

This amends *Signed copies of the configuration on every site*
(`docs/specs/2026-10-12-config-store.md`): a version's tree holds the three
files and the `blueprints/` tree, and its shape check accepts exactly that.

The workstation also caches fetched catalogs, by commit, under the user cache
directory. The cache is only for speed: losing it costs a fetch.

## What reads a blueprint

### validate

* `apps.<name>.blueprint` must resolve. An unknown catalog, an unknown
  blueprint, a blueprint whose `requires_toolkit` this binary does not meet,
  and a blueprint naming an unknown capability, helper, action or function are
  each refused by name.
* An engine other than `postgres` allows only pinned placement, which is
  today's `cluster-placement-without-a-cluster` read from the blueprint.
* `gated: true` on a blueprint with no `gate` section is refused: the gate is
  safe only when the app closes whatever the gate leaves open, and a blueprint
  that does not say what that is cannot be checked.
* A `leaves_open` entry that no gate setting closes is a warning, naming the
  entry.
* When the deployment declares backup destinations, an app whose engine is
  neither `postgres` nor `none` and whose blueprint has no `backup` is refused,
  because its data would not be in the backups the operator believes exist.
* Each blueprint's `checks` run with the rule's `id`, `level` and `message`.

### The grant review

`apply` and `host prepare` already show what the deployment file grants. They
also show, per blueprint, its images, published ports, volumes and mounts, the
capabilities it asks for and the in-app actions it declares, and they mark a
blueprint from a catalog marked `unsigned`.

Every rendered compose file is checked against a policy before it is written:
`privileged`, `network_mode: host`, `pid: host`, `cap_add`, `devices`, the
Docker socket, and any bind mount outside the stack's own directory are
refused. Templates run with only the toolkit's helpers; nothing in a template
can read a file outside its blueprint's directory.

### doctor

`doctor` reads each blueprint's gate settings through its `live` action and
compares them with what the blueprint says they must be, as it does for Mbin
and WriteFreely today.

### Uptime watches every blueprint

The seed (`internal/render/uptime.go`) builds its checks from the blueprint
instead of from per kind code:

* `health` gives the public check and the direct check on each site, as
  `HealthFor` does today.
* `gate.probe` gives the extra direct check of a gated app, which is how Mbin's
  private API check (`MbinPrivateAPIProbe`) is made today. The `KindMbin`
  branch in `uptimeSeed` goes.
* The reserved blueprints keep their own checks in Go: Pocket ID's reconciler
  check and admin continuity heartbeat, and the monitors' checks of each other.

**A blueprint with no `health` is still watched.** The fork has a TCP monitor
type (`VALID_MONITOR_TYPES` in `src/lib/sitePayload.js:18` at uptime fork
`b730e9b`, with `tcp_host` and `tcp_port` at :127-134). Without a health route
the seed gives the app:

| Check | Named like | What it is | What it notices |
|---|---|---|---|
| reachable, one per site | `directCheckName`, with `reachable` for `direct` | a TCP connect to the app's port on the site's mesh address | the container gone, crashed or restarting, the port closed |
| public, no health route | `PublicCheckName`, with `no health route` | `GET /` at the public hostname, expecting any of `200,301,302,303,307,308,401,403,404` | the edge answering 502, 503 or 504 because the app is down, and DNS, certificate and gateway faults |
| gate | `GateCheckName` | unchanged, when the app is gated | the gate in the path |

The status list is spelled out because the fork's `expected_status` is a
comma separated list of exact codes with no ranges (see
`docs/specs/2026-10-07-uptime-monitoring.md`). What the fallback cannot see is
an app that is up and broken, answering `/` while its database is down; the
check's name says so on the monitor's own page.

`validate` warns `blueprint-without-health` for such an app, naming the
blueprint. `health: { tcp: true }` declares an app that serves no HTTP, and
gets the TCP checks only. A blueprint with no `port` gets no checks, and a
stronger warning, `blueprint-without-checks`.

A deployment with no monitor stays the `no-uptime-monitor` warning.

## Config keys

| Key | Change |
|---|---|
| `apps.<name>.kind` | becomes `apps.<name>.blueprint` |
| `catalogs` | new, optional; absent means the core catalog alone |
| `apps.<name>.settings` | each key is checked against the blueprint's `settings`, so an unknown key is refused by name |

The toolkit has had no release, so an existing deployment file with `kind:` is
edited, not converted.

## Order of the work

1. **One record per kind, in Go.** Every `config.Kind` switch and map reads
   from one struct instead. The rendered golden tree does not change; that is
   the test. This is most of the work and worth doing on its own.
2. **The blueprint format, embedded.** The struct is read from
   `blueprint.yaml` files embedded in the binary, CEL is wired in, and the
   templates move beside their blueprints. The golden tree still does not
   change.
3. **The core catalog.** The non reserved blueprints move to the core catalog
   repository, the binary pins and embeds its commit, and `kind:` becomes
   `blueprint:`.
4. **Third party catalogs.** `catalogs:`, fetching, the snapshot in the
   configuration store, and the `catalog` and `blueprint` commands.
5. **Signing and the review.** Tag verification, key rotation, the compose
   policy and the grant review's blueprint section.

A step does not start until the one before it has merged.

## Open

* **The core catalog's repository name** and its release key are created when
  step 3 starts.
