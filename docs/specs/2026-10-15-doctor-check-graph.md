# Doctor runs its checks as a graph, in parallel, and checks the DNS token

Status: approved by the founder, 2026-10-15; implemented in #320 (the token
check and the apply gate), #321 (errors that refer to doctor), #323 (the
check graph) and #325 (doctor's DNS checks). What Cloudflare and Caddy do
is cited or was observed on staging on 2026-10-15 (*Observed*).

## What happened

A staging apply of a single site stopped at `wait for gate`. oauth2-proxy
could not fetch `https://id.example.org/.well-known/openid-configuration` and
logged `remote error: tls: internal error`, which is Caddy answering a TLS
handshake for a hostname it holds no certificate for. Caddy's log showed the
DNS-01 challenge failing at Cloudflare for every hostname:

* `HTTP 403 ... Code:9109 Message:Cannot use the access token from location:
  203.0.113.7`. The DNS token, `external.acme_dns_token`, rendered to the
  caddy container as `ACME_DNS_TOKEN`, had Client IP Address Filtering that
  allowed the operator's workstation and not the gateway.
* Then `HTTP 429 ... Code:10502 Message:Too many authentication failures`.
  Caddy retrying six hostnames against two certificate authorities tripped
  Cloudflare's lockout, and the lockout kept going while Caddy kept retrying,
  so after the token was fixed the operator still had to stop Caddy and wait.

Nothing in `apply` or `doctor` said any of this. The operator read `docker
logs` by hand. One call to Cloudflare from the gateway, before Caddy started,
would have caught it.

The same token is used from the workstation too: `dns init`, `dns prune` and
`gateway move` call Cloudflare from there (`dnsSetup` in
`cmd/paisans/main.go`, through `dns.For` and `internal/dns/cloudflare.go`).
A token whose filter allows the gateway and not the workstation fails the
other way round. And the same staging zone held 18 records left from an
earlier deployment, among them A records for several of this deployment's
hostnames pointing somewhere else.

## Settled

Founder decisions, 2026-10-15:

* **`paisans doctor` owns these checks.** Doctor is the read-only answer to
  "why is this deployment unhealthy", so a check that explains an outage
  belongs there, beside the others an operator already runs.
* **Doctor runs its checks in parallel, as many at once as it can, and
  reports each as it finishes.** An operator in an outage reads the first
  answer while the slow ones still run, and a deployment of several sites is
  read in the time of its slowest site rather than the sum of them.
* **Checks depend on each other, and a failed prerequisite stops what
  depends on it.** A site whose ssh does not answer gets no further checks
  over that connection. A check skipped for that reason is reported as
  skipped once, with the failure that caused it, never as a cascade of
  errors, because a cascade buries the one line that matters.
* **An error from `apply` or `init` refers the operator to `paisans doctor`
  when the cause is something doctor diagnoses.** Eg: a stack failing health
  with `tls: internal error` on a hostname the gateway serves says that Caddy
  has no certificate for that hostname, and to run `paisans doctor`.
* **The DNS token is checked from every place that uses it**: from the
  workstation, where doctor runs and where `dns init` calls the provider, and
  from each site that runs Paisans' Caddy. The provider judges the address a
  call comes from, so a check from one place says nothing about the other.
* **A record at one of the deployment's hostnames that points somewhere
  other than its site is reported**, read only, as a check of its own that
  needs the token's zone read. A hostname with no record fails, because
  nothing can reach it.
* **`apply` checks the token on each Caddy site before it starts or reloads
  Caddy**, and refuses on a refusal, because one check costs two or three
  calls and a token Caddy cannot use costs the provider's lockout.
* **A provider API that does not answer stops the apply at that gate**, as
  a refusal does, because the gate cannot tell a token Caddy can use from
  one it cannot.
* **Doctor reads the workstation's copy of the token from the secrets
  file**, with the age key from the workstation's keystore as `apply` reads
  it. Calling the provider needs the token's value, and sops encrypts each
  value under a fresh nonce, so no comparison of encrypted values could
  stand in for the call.

## Doctor today

`runDoctor` (`cmd/paisans/doctor.go`) loads `paisans-stack.yaml`, builds a
transport per site, and runs `gatherDoctor` inside one `ui.Get` step, `read
every site`. `gatherDoctor` makes every read in sequence, one phase after
another: `true` on each site for reach, then the mesh on each site that
answered, then etcd from the first member that answers, Patroni, containers,
Pocket ID's standby, leftovers, the ssh allowlist, clocks and app settings.
A site that did not answer reach is left out of every later phase, through
a `reached` map. Nothing runs in parallel.

The answers fill one `doctor.Input`. `doctor.Diagnose`
(`internal/doctor/doctor.go`) turns it into findings with pure functions,
one per section (`Reach`, `Mesh`, `Etcd`, `Patroni`, `Containers`, ...), and
`Report.Show` prints them by section, in the order bottom of the stack up,
so the first `FAIL` is usually the cause of the ones after it. Nothing is
printed until every site has answered every read.

That split, gathering apart from judging, is what this design keeps. Every
judgement stays a pure function of what the hosts said, and keeps the tests
it has.

## The check graph

Doctor's checks become nodes of a graph, built from `paisans-stack.yaml`
before anything runs. A node is:

* a **name**, the line it is reported under: `<section> <subject>`, Eg:
  `mesh home-a`, `dns token workstation`;
* **where it runs**: one site, over that site's transport, or the
  workstation;
* **needs**: the nodes it depends on, each hard or soft;
* a **gather**, the reads it makes, which fill its probe in `doctor.Input`;
* a **judge**, the pure function that turns probes into findings: the
  section functions doctor has today, called with the probes of the nodes it
  covers.

A **hard need** must have passed for the node to run. A node whose hard need
failed or was skipped is skipped itself, and never touches the host or the
provider, because a read over a connection that already failed only fails
again, and a call with a token the provider already refused feeds its
lockout. A **soft need** must have finished, however it ended; the node runs
anyway and its judge uses what the need found, or words its advice without
it. Eg: a container's advice names the Patroni leader when there is one, and
says less when Patroni could not be read. A need can also be **any of**
several nodes, which passes when one of them passes: etcd is read from the
first member that answers, as today.

| Node | Runs on | Hard needs | Soft needs |
|------|---------|------------|------------|
| `reach <site>` | site | | |
| `mesh <site>` | site | `reach <site>` | |
| `etcd` | the first member that answers | any of `reach <member>` | |
| `etcd version` | the same member | `etcd` | |
| `patroni <site>` | each `cluster.sites` site | `reach <site>` | `etcd` (`/sync`) |
| `patroni` | none, it judges | any of `patroni <site>` | |
| `containers <site>` | site | `reach <site>` | `patroni` |
| `settings <site> <app>` | site | `reach <site>` | `containers <site>` |
| `pocket-id` | each site running it | any of `reach <site>` | `patroni` |
| `clocks <site>` | site | `reach <site>` | |
| `leftovers <site>` | site | `reach <site>` | |
| `ssh allowlist <site>` | site | `reach <site>` | |
| `caddy certificates <site>` | each Caddy site | `reach <site>` | |
| `secrets` | workstation | | |
| `dns token <site>` | each Caddy site | `reach <site>`, `caddy certificates <site>` without a lockout | |
| `dns token workstation` | workstation | `secrets`, no Caddy site's log showing a lockout | `caddy certificates <site>` of every Caddy site |
| `dns records` | workstation | `dns token workstation` | |

A Caddy site is one of `cfg.CaddySites()`: every gateway, and every monitor
whose Caddy Paisans manages. The DNS nodes exist only when `acme.provider`
is set and the deployment has a Caddy site. With `--site`, the site nodes
are built for the named sites alone, and the workstation's DNS nodes only
when a named site is a Caddy site, because a run limited to some sites asks
about those sites and nothing else.

`doctor.Input` stays the record of every probe, filled under a lock as each
node's gather ends, so `Diagnose` and its tests work as they do now: running
every node's judge over a complete `Input` gives the same findings the
graph reported.

## Running the graph

A scheduler starts every node whose needs are met, at once, and starts the
next ones as nodes end. It follows the pattern of `atOnce`
(`cmd/paisans/converge.go`), which already runs per site work in parallel
with a bound, with two bounds in place of one:

* **Per site, at most 8 commands at once.** Paisans shares one ssh
  connection per destination (`internal/apply/sshmux.go`), and sshd allows
  ten sessions on one connection by default (`MaxSessions`, sshd_config(5)).
  A command past that limit fails as if the host had refused it, which
  doctor would report as a failed check, so the bound stays under it.
* **Across sites, no bound.** Each site is a machine of its own, since
  `validate` refuses two sites at one ssh destination, so no two sites
  compete for anything but the workstation's network.

The workstation's calls to the provider are few (see *How few calls*) and
need no bound of their own.

Two things on a terminal cannot run in parallel, and are made to wait for
each other rather than stopping the rest. ssh asking to accept a host key
already holds the reporter's drawing and is asked one at a time (`askMu`
and `SetPromptHold` in `internal/apply/transport.go`). A sudo password
prompt takes the same lock and the same hold, so `reach <site>` settles
sudo on every site in parallel and only the prompts queue, rather than
`settleSudo`'s one host after another, where a host that does not answer
costs every host after it ten seconds.

With a bound of one, the scheduler runs nodes in the order of the table
above, every site's node before the next node kind, which is today's order.
Tests use that, as `prepareAtOnce` is one in converge's tests.

## Reporting each check as it ends

`ui.Group` (`internal/ui/group.go`) already draws lines that run at the
same time: each is printed whole, its details and advice under it, when it
ends, and on a terminal the lines still running are a block of spinners
below the ended ones. It needs every title up front, and doctor does not
want a spinner for a node that waits on another, so `ui` gains a group that
lines join as they start: `ui.Stream(r)`, whose `Add(title)` opens a line
and whose `Close` ends the group. Its drawing is `Group`'s. Off a terminal
each line prints whole when it ends, with no spinner, and a reporter that
draws no group reports each line as a step opened and ended at once.

A node's line is its name with `check` before it, as `Finding.Title` makes
today: `check mesh home-a`. Every line names its site, so lines from
several sites interleaving still read on their own, and the section
headings go, since a heading cannot sit above lines that arrive in any
order. The line ends with the worst of its node's findings: ✓ for ok and
info, ! for a warning, ✗ for a failure. Each finding is printed under it,
with the recovery of a `FAIL` shown at every verbosity, as `Report.Show`
does now.

A failed node's dependents are pruned, transitively, and get no lines of
their own. The failed line says once what was not checked because of it:

```
✗ check reach home-b   ssh to ubuntu@home-b did not answer (Connection timed out)
    roles: apps, data
    While it is down the deployment is without one of 3 etcd votes (quorum needs 2); a Patroni member.
    Start the host or fix the route to it.
    Not checked because of this: mesh, containers, settings mbin, clocks, leftovers, ssh allowlist on home-b.
```

The closing summary counts them, `12 checks passed, 6 skipped, 1 failed.`,
since a skipped check is not a pass. A node skipped for a reason of its own,
not a failed need, is a line of its own as today: Eg: `etcd version
skipped: the host has no curl`.

The order bottom of the stack up still holds, from the graph rather than
from the printing: a node cannot end before its hard needs end, so a cause
prints before what it causes. After the last line, when any check failed,
doctor prints the failures once more, in the table's order, deepest first,
under `failed, deepest first:`, because in a long interleaved run the first
failure on screen is the first to finish, not the first to fix. Then
`doctor changes nothing` and the summary, as now. The exit status is
unchanged: 1 when any finding is `FAIL`.

## The DNS checks

### Where the token comes from, and how it travels

The workstation's token is `external.acme_dns_token` from the secrets file,
which the `secrets` node opens as `dns init` does (`dnsSetup`). Doctor
gains `--secrets`, with `dns init`'s default beside the configuration. A
secrets file doctor cannot open, Eg: no age key on this workstation, skips
`secrets` with the reason, and the workstation's DNS checks with it, since
doctor's subject is the deployment and not the workstation's keys.

A site's token is the one its Caddy holds: `ACME_DNS_TOKEN` in the rendered
`/srv/paisans/<token>/infra/caddy/caddy.env` (the path's `<token>` is the
deployment's, not the DNS token). It is read there, on the host, and never
leaves it, because that is the token Caddy uses, which differs from the
secrets file when a `secrets set` has not been applied, and because doctor
sends no secret to a host. The command takes the token from the file into a
shell variable and hands it to curl through a configuration on stdin:

```sh
t=$(sed -n 's/^ACME_DNS_TOKEN=//p' /srv/paisans/<token>/infra/caddy/caddy.env)
printf 'header = "Authorization: Bearer %s"\n' "$t" |
  curl -sS --max-time 20 -w '\n%{http_code}\n' -K - \
    'https://api.cloudflare.com/client/v4/zones?name=example.org'
```

The token is in no process's argument list: the remote command runs under
`sh`, `printf` is a builtin of dash (dash(1), *Builtins*), and curl reads
its header from `-K -`, which is how Paisans already sends a secret to curl
(`garage.CurlStdin`, `internal/garage/s3.go`; README, *Rendered
configuration is a build artifact*). curl prints no request header without
`-v`. The workstation's calls go through `internal/dns/cloudflare.go`,
which sends the token only in the `Authorization` header and redacts it
from every error.

Every response is read for `success`, `errors[].code` and
`errors[].message`, `result.status`, and the zone's `id` and `name`.
Nothing else of the body reaches a finding, a detail or a trace, so a
provider that echoed a credential could not put it on the terminal.

### Token: active, and allowed from here

`dns token <place>` makes two calls, in order, and the second only when the
first passed:

1. The zone lookup `dns init` makes, `GET /client/v4/zones?name=<name>`,
   suffix by suffix as `dns.FindZone` asks (`internal/dns/zone.go`), until
   the zone holding `community.domain` is found. Client IP Address
   Filtering applies to it (observed), so it is the call that catches a
   token filtered away from this place, and it proves the token can read
   the zone, which DNS-01 needs.
2. Verify, for the token's status, `active`, `disabled` or `expired`.
   Anything but `active` fails, with the status. A token an account owns,
   whose value begins `cfat_`, is verified at
   `GET /client/v4/accounts/<account id>/tokens/verify`, with the account
   id the zone lookup returned; any other at
   `GET /client/v4/user/tokens/verify`
   ([Cloudflare: Verify Token](https://developers.cloudflare.com/api/resources/user/subresources/tokens/methods/verify/)).
   The user endpoint answers an account token `401`, `Code:1000 Invalid API
   Token`, although the token works (observed), so choosing the endpoint by
   the prefix is what keeps a working token from failing here.

Verify alone could not catch the incident: Cloudflare does not apply Client
IP Address Filtering to it
([Cloudflare: Restrict API tokens](https://developers.cloudflare.com/fundamentals/api/how-to/restrict-tokens/)),
so a token refused from the gateway verifies there as active. The zone
lookup's answers:

| Answer | Finding |
|--------|---------|
| the zone is found | ok: `cloudflare accepts the token from <place>; zone example.org` |
| `Code:9109`, `Cannot use the access token from location: <ip>` | FAIL: `cloudflare refuses the token from <place> (<ip>)`. Add the address to the token's Client IP Address Filtering, or remove the filter. The address is the one in Cloudflare's message, because that is what it judged, and it can differ from `public_address` behind NAT. |
| `Code:10502`, `Too many authentication failures` | FAIL: `cloudflare is locking the token out`. See *The lockout* |
| no zone the token can see holds the domain | FAIL: `dns.FindZone`'s error, which says the token needs zone read as well as record edit |
| `Code:1000`, `Invalid API Token` | FAIL: `cloudflare does not accept the token`; it was deleted, rolled or mistyped |
| no answer from the API | FAIL: `cloudflare did not answer from <place>`, with curl's or Go's error |

Edit permission on the zone's records cannot be read without writing one,
and doctor writes nothing at the provider, so the ok line says `record edit
not checked`. A token that can read and not edit fails Caddy's challenge,
and `caddy certificates` reports it from Caddy's log.

### Caddy's certificate failures

`caddy certificates <site>` reads the caddy container's log on each Caddy
site, `docker compose -f /srv/paisans/<token>/infra/compose.yaml logs
--no-color --since <started> caddy`, from the container's start or the last
hour, whichever is later. It keeps the error lines of Caddy's certificate
loggers, and for each hostname the gateway serves (`dns.GatewayHostnames`,
or the monitor's own on a monitor) reports the newest outcome:

| Newest in the log | Finding |
|-------------------|---------|
| a certificate obtained, or no attempt | ok |
| `Code:9109` | FAIL: `cloudflare refuses this server's token (<ip>)`, the same advice as the token check |
| `Code:10502` | FAIL: `cloudflare is locking the token out`; see *The lockout* |
| an ACME `rateLimited` problem (RFC 8555, section 6.7) | FAIL: `the certificate authority is rate limiting <hostname>`, with the time it gives to retry after ([Let's Encrypt: Rate Limits](https://letsencrypt.org/docs/rate-limits/)) |
| any other error | FAIL: the error's last clause, cut as `logCause` cuts one (`internal/apply/health.go`) |

Hostnames failing for one reason are one finding that names them all, since
six lines saying `9109` is the cascade the founder ruled out. The log is
where this server's own failures are, so this node needs no secret and no
call to the provider.

Caddy logs JSON to the container's output (observed). The summary reads
lines whose `logger` is `tls.obtain`, with `msg` either `could not get
certificate from issuer` or `certificate obtained successfully`, and their
`identifier` (the hostname), `issuer` and `error` fields. The provider's
code is not a field of its own: it is inside `error`, as
`{Code:9109 Message:Cannot use the access token from location: <ip> ...}`,
so the summary reads it from there. A staging log, with addresses and
hostnames replaced, is the test fixture.

### The lockout

Cloudflare answers `Code:10502`, `Too many authentication failures`, once
calls with a refused token arrive in a burst. Observed on staging: Caddy
started, its first `9109` came three seconds later and its first `10502` two
seconds after that. Over the next twenty minutes Caddy's log held 30 `9109`
answers and 42 `10502` answers, interleaved, so some calls still reached the
filter while others were turned away. Once the token's filter admitted the
server, the next attempt Caddy's schedule made obtained every certificate,
with no lockout left over. So `10502` is held while refused calls keep
arriving, and fixing the token is what ends it; how many failures trip it
and how long it outlasts the last one are not documented
([Cloudflare: API rate limits](https://developers.cloudflare.com/fundamentals/api/reference/limits/)
describes a different limit).

Doctor does not add to it. When any Caddy site's log shows `10502` in the
last five minutes, every `dns token` node is skipped, with `not asked, so as
not to feed Cloudflare's lockout`, and the finding says what does end it:
fix the token's filter, after which Caddy's next scheduled attempt succeeds
with nothing restarted.

### Records that point elsewhere

`dns records` builds the plan `dns init` builds, with `dns.Desired` and
`dns.Build` (`internal/dns/dns.go`), which reads and never writes, and
reports from its entries:

| Entry | Finding |
|-------|---------|
| `present` | ok, one line for them all |
| `conflict`: an address the deployment does not declare, a CNAME, or a proxied copy | FAIL per hostname, naming what the zone holds. A visitor is sent there. Delete it at the provider by hand, because `dns init` never updates a record and `dns prune` keeps one whose name is still wanted |
| `create`: nothing at the name | FAIL: `<hostname> has no record`, with `paisans dns init --execute` |
| `moving` | info, as `dns init` shows it |
| `warn` (an advisory record) | WARN, as `dns init` shows it |

It runs from the workstation only: what the zone holds does not depend on
where it is asked from, and the location check already ran on every Caddy
site. Records at names the deployment does not use are `dns prune`'s, and
doctor leaves them to it.

### How few calls

A refused call is what feeds the lockout, so the graph makes at most one
refused call per place in a run: the first call the filter applies to is the
zone lookup, and everything else that touches the provider needs it to have
passed.

| Place | Calls in one doctor run |
|-------|-------------------------|
| each Caddy site | verify, then the zone lookup: one call per label of the domain above the zone apex, usually two or three in all |
| the workstation | the same, then one records listing per hostname (`dns.Build`), each only after the zone lookup passed |

Nothing at the provider is called while a lockout shows in any Caddy log.

### A provider doctor has no check for

The calls above are Cloudflare's. For a provider `internal/dns` does not
implement (`dns.Implemented`), Eg: `desec`, the `dns token` and `dns
records` nodes are skipped with `doctor has no check for desec's tokens;
caddy certificates is what the log says`, which counts as skipped, not
passed. `caddy certificates` runs for every provider, since it reads Caddy's
log and not the provider, and an error it does not recognise is reported
with its own text.

## `apply` checks the token before it starts Caddy

On each Caddy site, `apply` runs `dns token <site>`, the same code as
doctor's node, before anything that makes Caddy ask the provider: after the
gateway's files are written and its modules checked, before `validate
gateway config`, `reload gateway`, `start gateway` or an `up -d` that
recreates the infrastructure stack's Caddy (`internal/apply/apply.go`). Its
step is `check DNS token`. It runs whenever one of those is in the plan,
since each is when Caddy begins asking, and not otherwise, so an apply that
leaves Caddy alone makes no call.

It reads the token from the rendered `caddy.env` that apply has just
written, so it checks the token Caddy is about to load. On a refusal
(`9109`, `10502`, a token not `active`, or no zone it can read), it stops
the site's apply with nothing of the gateway changed:

```
✗ vm: cloudflare refuses the DNS token from this server (203.0.113.7)
  Caddy could get no certificate with it, and its retries would lock the
  token out. Add the address to the token's Client IP Address Filtering,
  or remove the filter, then apply again. The gateway was not changed.
  `paisans doctor` checks the token from every place that uses it.
```

An API that does not answer stops the site's apply as a refusal does,
because the gate cannot tell a token Caddy can use from one it cannot, and
starting Caddy on an unchecked token is what the gate exists to prevent. The
gateway is left as it was, still serving the certificates it holds. A
provider with no check is not gated, and says so as a detail.

The workstation side is the same check, run first by `dns init` and `dns
prune` in `dnsSetup`, so a refusal there says `from this workstation` in
place of a bare `HTTP 403` from the first records call. Converge's `dns`
stage runs `dns init`, so `paisans apply` with no `--site` gets it there.

## Errors that refer to doctor

An error refers to `paisans doctor` only where a doctor check names its
cause, because a referral to a command that then reports everything ok is
worse than none. The causes live in one place, a small package both sides
import, `internal/diagnosis`: each entry is a matcher over an error or a log
line, the sentence an operator reads, and the doctor node that diagnoses
it. It is its own package because `internal/doctor` imports
`internal/apply` (`internal/doctor/standby.go`), so apply cannot import
doctor, and a second copy of the table would drift.

| Cause | Where it is seen | Sentence |
|-------|------------------|----------|
| `tls: internal error` fetching `https://<hostname>` that a Caddy site serves | `apply`'s health gate, from the failing service's log (`unhealthy`, `internal/apply/health.go`) | `Caddy on <site> has no certificate for <hostname>, so it refuses TLS for it. Run paisans doctor to see why it could not get one.` |
| `Code:9109` or `Code:10502` from the provider | `dns init`, `dns prune`, `gateway move`, the apply gate | as the token check's findings, with `Run paisans doctor` |
| a site whose ssh does not answer | `init`, which reads every site to settle the mesh subnet, and `apply` | `Run paisans doctor --site <site>`, which says what the deployment is without while it is gone |
| a container that cannot reach the database | `apply`'s health gate | `Run paisans doctor`, whose Patroni check says whether there is a primary |

`tls: internal error` is observed as Caddy's answer to a handshake for a
hostname it holds no certificate for (staging, 2026-10-15). The gate matches
the hostname in the failing service's log line against
`dns.GatewayHostnames`, so the plan carries those hostnames and the Caddy
site serving them; a `tls` error on a hostname Paisans does not serve gets
no referral. The sentence goes in the Problem's explanation, under the
cause line `unhealthy` already prints, so the default output carries it
without `--verbose`.

## Documentation

`README.md`, *`paisans doctor` reports what is stuck and how to recover*:
the checks table gains `caddy certificates`, `dns token` and `dns records`;
the paragraph on order says checks run at once and print as they end, a
cause before what it causes, with the closing list deepest first; and a
skipped check is described as folded into the line that caused it. The
apply gate is described under *DNS-01 is what makes step 1 possible*, and
the referral rule beside *`apply` checks each stack before the next one
moves*. `docs/development.md` gains the graph and the scheduler's bounds
under `apply`, and the gates in it.

## Tests

* **Graph.** A node never runs before its hard needs pass; a failed or
  skipped hard need skips its dependents transitively and makes no read; a
  soft need is waited for and its failure does not skip; an any-of need
  passes on one. A fake transport counts commands per site and never sees
  more than the bound at once.
* **Pruning is reported once.** `reach home-b` fails: one ✗ line, its `Not
  checked because of this` list naming every pruned node, no line for any of
  them, and the summary's skipped count equal to the list.
* **Streaming.** With a terminal reporter on a controlled clock
  (`NewTerminalForTest`), a node that ends first prints first, and lines
  from two sites interleave whole, never mid line. With a bound of one the
  plain output matches today's order, so the existing doctor tests keep
  their expectations apart from the section headings.
* **Same findings.** For every fixture `Input` in `internal/doctor`'s
  tests, the graph's findings equal `Diagnose`'s.
* **Token check.** Against an `httptest` server: `active` and a zone pass;
  `9109` names the address from the message and makes no verify call;
  `10502` fails; `1000` fails; no zone fails with `FindZone`'s error;
  `disabled` and `expired` fail after the zone is found; a `cfat_` token is
  verified at the account endpoint with the zone's account id and any other
  at the user endpoint. The remote command, given a fake host, carries no
  token in the command string, and its stdin is the only place the
  `Authorization` header appears.
* **No token in output.** The token, planted in a provider's error message
  and in the response body, appears in no finding, detail or trace.
* **Lockout.** A Caddy log fixture with `10502` in the window skips every
  `dns token` node and makes no call.
* **Caddy log.** The staging log, with addresses and hostnames replaced by
  `203.0.113.7` and `example.org` names, gives one `9109` finding naming
  six hostnames.
* **Records.** A fake provider holding an A record for `talk.example.org`
  at `198.51.100.9` gives one FAIL naming both; a missing record gives the
  `dns init` advice.
* **Apply gate.** A refusal stops the site's apply before `validate gateway
  config` with nothing of the gateway changed; an API that does not answer
  stops it the same way; an apply with no Caddy action makes no call.
* **Referrals.** `unhealthy` given oauth2-proxy's `tls: internal error`
  line for `id.example.org` says Caddy has no certificate for it and to run
  `paisans doctor`; the same line for a hostname Paisans does not serve
  gets no referral.

## Observed

On staging, 2026-10-15, with an account owned (`cfat_`) Cloudflare token:

| Call | From | Answer |
|------|------|--------|
| `GET /zones?name=<domain>`, token filtered to another address | a workstation | `403`, `Code:9109` |
| `GET /zones?name=<domain>`, token filtered to this server | the gateway | `200`, the zone |
| `GET /user/tokens/verify` | the gateway | `401`, `Code:1000 Invalid API Token` |
| `GET /accounts/<account id>/tokens/verify` | the gateway | `200`, `active` |
| Caddy's DNS-01 calls, token filtered away from the gateway | the gateway | `9109`, then interleaved `10502` |

## Open

* **The per site bound of 8** is reasoned from sshd's default and not
  measured.
* **How many refused calls trip `10502`, and how long it outlasts the
  last one**, are not documented and were not measured; the five minute
  window above is the conservative reading of the staging log.
