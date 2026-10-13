# The monitor may share the gateway's machine

Status: design approved in session on 2026-10-12, implemented (pull requests #207 and #210; the hand-off in #208).

This amends `2026-10-08-monitor-role-and-host-check.md`, Part 1, where a
monitor is never the gateway and never the witness. Founder decision,
2026-10-12: both become warnings, so that a deployment with one machine, or
with no machine to spare, can still watch its apps and containers. The
warning says what is lost and recommends a machine that runs the monitor and
nothing else.

## What a monitor on the gateway can and cannot do

It can report every app and container failing while the machine stays up:
each app's public, direct, gate, signed fetch and private API checks are
unchanged. With more than one site, every other site still gets its ping and
heartbeat.

It cannot report its own machine failing. When the gateway's machine goes
down the monitor goes with it, and no alert is sent. On the witness, nothing
reports etcd losing its tiebreaker. Uptime already seeds no ping and no
heartbeat for the monitor's own site (`internal/render/uptime.go`), so a
one-machine deployment has no site checks at all, which is correct: a
monitor cannot watch the host it runs on.

## validate

`monitor-on-gateway` and `monitor-on-witness` are removed as refusals.
`monitor-shares-a-site` becomes the one warning for a monitor beside any other
role, once per site, naming every role it shares, so a one-machine deployment
reads one line rather than three. The message, for `sites.<name>.roles`:

> holds monitor beside gateway, data and apps. It cannot report <name>
> failing: when it goes down, the monitor goes with it and no alert is sent.
> A small machine of its own with roles: [monitor] reports it.

The roles are listed gateway, witness, data, apps: what is lost, most first. Holding the witness
adds one sentence: "Nothing reports etcd losing its tiebreaker either." A
site holding `monitor` alone, or `monitor` with `storage`, is not warned:
storage is not something the monitor's absence would hide.

New refusal, `web-server-on-gateway`: a `web_server` block on a site that
also holds `gateway`. A gateway always runs the toolkit's Caddy, which then
serves the monitor's hostname too, so the block would be ignored, and
`managed_by: external` would hand ports 80 and 443 to a second web server
that the gateway's Caddy already holds. Message, for
`sites.<name>.web_server`: "is declared on <name>, which also holds the
gateway role. The gateway's Caddy serves the monitor there. Remove the
block."

Unchanged: `monitor-without-uptime`, `uptime-needs-a-monitor-site`,
`monitor-without-public-address` (a gateway has one already),
`web-server-outside-monitor`.

`no-uptime-monitor` stops telling the operator a second machine is needed.
Its new remedy: "Declare an uptime app pinned to a site with the monitor
role, ideally a small machine of its own with a public_address, and apply it
(README.md, \"Monitoring\")."

## render

`routesFor` (`internal/render/site.go`) assumes the monitor and the gateway
are always different sites: a gateway skips every app a monitor serves, and a
non-gateway routes only its own. On a site holding both, the status page
would be routed by nothing. The fix: a gateway skips an app only when a
*different* site serves it.

```go
case site.IsGateway && onMonitor && monitor != site.Name:
	continue
```

Everything else already holds:

* Ports: `internal/render/ports.go` adds the monitor's own Caddy ports only
  when the site is not also a gateway, so there is one Caddy and one claim on
  80 and 443.
* Firewall and host check: both key on `RunsCaddy`, true once for the site.
* DNS: `dns init` points the status hostname at the monitor's
  `public_address`, which here is the gateway's.
* `UpstreamElsewhere` is false for a monitor's app, which stays true: its
  Caddy is on its own machine.
* Uptime seeding, as above.

`doctor` (`internal/doctor/reach.go`) describes a lost gateway as carrying
"every public hostname but the monitor's" whenever a monitor exists. When the
monitor shares the gateway it carries every public hostname, and the
monitor with it; the description says so.


## The wizard

After "A status page (uptime monitor)?" is answered yes, `monitor()` offers a
choice instead of requiring a machine:

```
The status page reports the other machines failing, so it is best on a
machine of its own.
  1) a machine of its own (recommended)
  2) on <gateway> (takes the monitor down with it, no alerts when the gateway fails)
```

`<gateway>` is the name of the site holding the gateway role in the chosen
shape: the one machine, `gateway`, or what the operator renamed it to. It is
the only site in a named shape with a public address, which the monitor
needs. A custom shape offers the same choice when exactly one of its sites
holds `gateway`, and only the first option otherwise.

Option 1 asks the machine's name, address and SSH questions it asks today.
"Add a machine for it?" goes: declining the status page is answering no to
"A status page?", one question earlier. Option 2 sets
`Answers.MonitorOnGateway`, and `AllSites` adds `monitor` to the roles of the
site holding `gateway`, so answering the machines again, or renaming that
site, keeps the choice; `Answers.Monitor` stays nil. Answering again keeps
the choice as the default.

`create`'s render keeps the chat server off monitor sites. With the monitor
on the high availability shape's VPS, that would push chat onto a data site,
where it collides with Patroni's ports. The rule becomes "off a monitor site
that is not the gateway": a monitor beside the gateway shares the gateway's
Caddy, so chat there is routed as before.

Chat stays unavailable on one machine. That is a port collision between
Synapse and the database, and Element and the gateway, unrelated to this.

## Moving the gateway off a shared site

`paisans gateway move` finds the old gateway from what its host still holds,
and skips any site whose roles run Caddy, which a monitor's do. It still
finds a shared site through DNS, since the gateway's hostnames point there,
and its last stage would then stop that site's Caddy, taking the monitor down
with it. Worse, `apply` runs on every site before the move, and on a shared
site it rewrites the Caddyfile to the monitor's routes alone while DNS still
sends every hostname there: the outage "an overlap, not a cutover" exists to
prevent.

This ships in two pull requests. Founder decision, 2026-10-12.

### First: refuse it

> **Superseded 2026-10-12** by `2026-10-12-gateway-handoff.md`, which
> replaces this refusal with the hand-off.

With the rest of this spec. Both `paisans gateway move` and
`paisans apply --site` (which `apply` runs for every site) refuse when the
site concerned does not hold `gateway`, runs Caddy for the monitor, and its
host holds this deployment's record or the move's marker. Only a gateway
ever holds the record (`cmd/paisans/record.go` writes it to gateway sites
alone), so a monitor-only site is never mistaken for an old one. The
message: "<site> keeps the monitor and is still this deployment's gateway.
Moving the gateway off a site that keeps the monitor is not supported yet.
Give <site> the gateway role back and apply it, or pin the status page to
another monitor site first, then move the gateway."

### Second: hand it off

Its own spec and plan, after the first merges. The design approved in
session:

* **Finding it.** A site that runs Caddy for a monitor is read too, with its
  Caddy not counted as evidence: the record and the marker are.
* **Keeping the overlap.** While such a site still holds the record or the
  marker and paisans-stack.yaml no longer gives it `gateway`, `apply` renders it as
  a departing gateway: its Caddyfile keeps every gateway route beside the
  monitor's. `apply` reads this from the host, as it already reads whether a
  move is pending, and passes it to render.
* **Stage 6 on a shared site** removes the record and the marker and leaves
  Caddy running. A last stage, *hand off*, applies that site again, so its
  Caddy reloads with the monitor's routes alone. Its gate: the site's
  Caddyfile names no gateway hostname.
* The refusal over the host owner's sites in `/srv/caddy.d` stands: a
  monitor's Caddy does not serve them.
* The note that the old site keeps 80 and 443 open is dropped for a shared
  site, whose monitor still needs them.

## Documentation

* README, "The monitor has a site of its own": the rules table loses its two
  refusals and gains `web-server-on-gateway`; the prose says a monitor
  beside the gateway or the witness is allowed with a warning, what it
  cannot report, and that a machine of its own is still the recommendation.
  "The smallest deployment with one is two machines" goes.
* `docs/specs/2026-10-08-monitor-role-and-host-check.md`: a dated note at
  the top of Part 1 pointing here.
* Outline: the Decisions record *Uptime runs on a monitor site, never the
  gateway; hosts are checked before the toolkit touches them* is amended
  with this decision, and the Decision Log gets a line.

## Testing

* validate: one warning, naming each shared role, for monitor with
  gateway, with gateway and witness, and with data and apps; none for
  monitor alone or with storage; `web-server-on-gateway` refused with either
  `managed_by`.
* render: a one-machine deployment with a monitor renders one Caddy whose
  Caddyfile carries the status hostname, one claim each on 80 and 443, and a
  `monitors.json` with app checks and no ping or heartbeat. A two-site
  deployment with the monitor on the gateway pings and receives a heartbeat
  from the other site.
* gatewaymove: a move from a site that runs the monitor is refused, and
  its Caddy is not stopped; a move from a plain old gateway is unchanged.
* apply: `apply --site` on a monitor site holding the record or the marker
  is refused; on a monitor site holding neither it proceeds.
* doctor: the lost-gateway description for a shared monitor.
* create: both choices, on each named shape, validate with only the shared
  site warning added (and, on high availability, chat still beside the
  gateway); answering again keeps the choice; the golden files gain a
  one-machine-with-monitor case.
