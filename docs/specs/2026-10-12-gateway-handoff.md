# Handing the gateway off a site that keeps the monitor

Status: design approved in session on 2026-10-12, implemented (pull request #208).

This is the second step of `2026-10-12-monitor-on-gateway.md`, *Moving the
gateway off a shared site*. The first step made `paisans gateway move` and
every per-site apply refuse a site that keeps the monitor while it still
holds the gateway's deployment record. This one makes the move work.

## The situation

Site S holds `gateway` and `monitor`. The operator gives `gateway` to site T
in `paisans-stack.yaml`, and S keeps `monitor`. S's Caddy must go on serving
every gateway hostname until DNS has moved and the old records' TTL has
passed, exactly as a plain old gateway does, and must then serve the
monitor's hostnames alone. It is never stopped: the monitor needs it.

Two things in the toolkit work against that today. The move does not look
for an old gateway among sites whose roles run Caddy, because a monitor's
Caddy is legitimately there. And `apply` renders S from the configuration,
which no longer gives it `gateway`, so applying S before the move rewrites
its Caddyfile to the monitor's routes while DNS still sends every hostname
there.

## A departing gateway is rendered as a gateway

`render.WithDepartingGateway(site)` is a render option, like
`WithEtcdInitial`: a fact read from the live deployment and handed to a
render that otherwise reads only the configuration. It marks a site that
runs Caddy as a gateway for that render. Being a gateway decides exactly
what a departing site must keep:

* its routes: every app no other site's monitor serves, beside its own
  monitor's;
* the gate's snippets, which gated routes import;
* the host owner's site blocks from `/srv/caddy.d`, which only a gateway's
  Caddy mounts.

Nothing else about the site changes: its ports, firewall and stacks are a
monitor's, which already run Caddy on 80 and 443.

`planSiteApply` passes the option for a site that does not hold `gateway`,
runs Caddy, and whose host holds this deployment's record or the move's
marker. Only a gateway is ever given the record (`cmd/paisans/record.go`
writes to gateway sites alone), so a plain monitor is never rendered as one.
Every path that applies a site goes through `planSiteApply`: `apply` with and
without `--site`, `secrets rotate` and `rotate-key`. The refusal the first
step added there goes.

## The move finds a departing site

`Build` inspects every site but the new gateway. For a site whose roles run
Caddy, its Caddy containers are not evidence; three things are:

* the deployment record;
* the move's marker;
* its Caddyfile on the host names a hostname the gateway serves
  (`dns.GatewayHostnames`) as a site block.

The third is what makes the hand-off resumable: once stage 6 has removed the
record and the marker, a run that stopped before the hand-off finished still
finds the site by the routes its Caddy still carries. A plain monitor's
Caddyfile names only monitor hostnames, so it is never found.

The refusal the first step added in `Build` goes. The refusal over a
monitor's record still pointing at the old site stays: a monitor that moved
with the gateway keeps its record there until it is changed by hand.

## The stages

A plain move keeps its six stages unchanged. A move from a site that runs
Caddy for a monitor changes the sixth and adds a seventh.

| # | Stage | Does | Gate |
|---|---|---|---|
| 6 | stop | checks the new gateway again, then removes the deployment record and the marker; Caddy keeps running | the old site holds neither the record nor the marker |
| 7 | hand off | applies the old site's `infra` stack, so its Caddy reloads with the monitor's routes alone | the old site's Caddyfile names no hostname the gateway serves |

Stage 6 keeps its refusal over the host owner's sites in `/srv/caddy.d`: a
monitor's Caddy does not mount them, so the hand-off would take them dark.
Move them to the new gateway's `/srv/caddy.d` first, as for a plain move.

Stage 7 runs `paisans apply --site <old> --only infra --execute` in the same
process, with the move's own `--config`, `--secrets` and `--sudo`, through an
`Options.ApplySite` hook the command supplies. With the record and the marker
gone, that apply renders the site as the monitor it now is. A dry run never
reaches it. The apply claims the host and reviews changes exactly as typed by
hand.

The move's notes that the old site keeps 80 and 443 open, and that the old
Caddy's files stay on it, are left out for a shared site: its Caddy runs on,
and the monitor needs both ports.

## apply without --site

`convergeGatewayMove` decides whether `apply` ends with a move step. It skips
sites whose roles run Caddy; it now asks them too, with the same evidence as
`Build`, so a deployment whose gateway left a shared site gets its move step,
and the move its hand-off.

## Testing

* render: a monitor site rendered departing carries every gateway hostname,
  its own monitor hostname and the host owner's sites mount; the gateway
  still omits that monitor's hostname; without the option the site is a
  plain monitor.
* gatewaymove: a full move off a shared site runs seven stages, leaves its
  Caddy running and its Caddyfile without a gateway hostname, and calls the
  hook once; a run stopped after stage 6 resumes at the hand-off; a plain move
  runs six stages unchanged; a plain monitor is never found; a dry run never
  calls the hook.
* cmd: `planSiteApply` renders a shared site holding the record as departing
  and a plain monitor as a monitor; `apply` without `--site` appends the move
  step for a departing shared site.
