# The mesh dials a site's public address

Date: 2026-10-11. Status: the rule is approved by the founder in session; the decisions below are recommendations for review.

A site in `paisans-stack.yaml` carries up to four addresses: `address` (its mesh
address), `endpoint` (the `host:port` the other sites' WireGuard dials),
`public_address` and `public_address6` (what the internet reaches it on), and
`ssh.host`. On a cloud VM `endpoint` and `public_address` are the same address
written twice, and an operator who writes a name in `endpoint` that resolves
back to `public_address` makes the mesh depend on DNS: WireGuard resolves an
endpoint name once, as the interface starts, and a name whose record does not
exist yet fails the whole interface. This makes `public_address` what the mesh
dials wherever it is declared, and `endpoint` optional beside it.

## Scope

In: `config.Site`, the WireGuard render, `host prepare`'s firewall, the host
check's port claims and the registry's listen port, `validate`'s
`voters-share-a-relay`, `site add`'s scope check, `dns init`'s endpoint
records, `apply`'s `resolve mesh endpoints` step, the example, the guide and
the README.

Out:

* **`ssh.host`.** It is unchanged: an override, defaulting to
  `public_address`, then the endpoint's host.
* **Converting a file.** An existing `paisans-stack.yaml` with explicit endpoints is
  read under the rules below as it stands.

## The rule

One method, `config.Site.DialEndpoint()`, is the address every other site
dials, or nothing. Every consumer reads it rather than `endpoint`.

| `public_address` | `endpoint` | `DialEndpoint()` |
|---|---|---|
| set | absent | `public_address:51820` |
| set | `":<port>"` | `public_address:<port>` |
| set | `<name>:<port>` | `public_address:<port>` |
| set or absent | `<address>:<port>` | the endpoint as written |
| absent | `<name>:<port>` | the endpoint as written |
| absent | absent | nothing: the site only dials out |

51820 is `config.DefaultWireGuardPort`, WireGuard's conventional port.

`ListenPort()` is the port of `DialEndpoint()`, and `PeersDirectly()` is true
when either side has one. Both were already the one predicate the render, the
firewall, the host check, the registry and `validate` read, so routing them
through `DialEndpoint()` moves every consumer at once.

An endpoint written as an address is dialled as written even beside a
`public_address`: an operator who writes an address names exactly what to
dial, which may be a second address on the host.

## Consumers

| Consumer | Reads | Effect |
|---|---|---|
| WireGuard render (`render/plan.go`, `site.go`) | `DialEndpoint()` into the site view | peers' `Endpoint =`, which sites relay, `ListenPort =` |
| `host prepare` firewall (`hostprep/firewall.go`) | `ListenPort()` | a site with only a `public_address` opens 51820/udp |
| host check claims, registry | `ListenPort()` | the port claimed is the derived one |
| `validate` `voters-share-a-relay` | `DialEndpoint()` | a voter with a `public_address` counts as dialled |
| `site add` scope | `DialEndpoint()` | a site with a `public_address` and no endpoint is accepted |
| `dns init` | the endpoint as written | unchanged: a name beside a `public_address` still gets its A record; a port alone names nothing |
| `apply` `resolve mesh endpoints` | the rendered file | unchanged code: it reads the `Endpoint =` lines rendered, so a name beside a `public_address` is no longer waited on |

## Validation

| Refused | Message names |
|---|---|
| an endpoint that is neither `host:port` nor `:port` | `sites.<name>.endpoint`, with both forms as examples |
| a port outside 1 to 65535 | `sites.<name>.endpoint` |
| `":<port>"` on a site with no `public_address` | `sites.<name>.endpoint`, and that `public_address` is what a port alone means |

## Decisions

**IPv6.** `public_address6` alone derives no endpoint. The mesh dials from every
other site, and a peer with no IPv6 route, common at home, could never reach an
IPv6-only site; deriving one would make the mesh depend on every peer's IPv6. A
site reached only over IPv6 writes its endpoint as an address,
`"[2001:db8::10]:51820"`, which is dialled as written. A port alone needs
`public_address`, not `public_address6`.

**No opt-out.** A site with a `public_address` is always dialled; there is no
`endpoint: none`. Every site that declares one is reachable from the internet
by definition, `host prepare` opens the port on the host, and the only thing
that could still drop the packets is a provider's own firewall, which the
operator opens like 443. WireGuard also learns a peer's address from an
authenticated packet, so a site whose inbound UDP is dropped still peers with
every site that it can dial. An opt-out is added if a deployment turns up that
needs one.

**The mesh changes on the next apply.** A site that declares `public_address`
and no `endpoint` was not dialled before and is now. On the next `host
prepare` it opens 51820/udp, and on the next `apply` every other site's file
gains an `Endpoint =` and `PersistentKeepalive` for it, it gains a
`ListenPort = 51820` and the relay's `ip_forward`, and sites that relayed
through another site to reach it peer with it directly. A site whose endpoint
is a name beside a `public_address` is dialled at the address instead, which
changes only the `Endpoint =` line its peers render. Run `host prepare` before
`apply` on such a site, so the port is open when the peers start dialling it;
`paisans apply` with no `--site` does that in order.

## Tests

* `config`: `DialEndpoint` for every row of the table, `public_address6`
  alone, a port alone without `public_address`, a malformed endpoint;
  `ListenPort` derived; `PeersDirectly` with a `public_address`; `Load`
  refusing a port alone without `public_address` and a bad port.
* `render`: home-a's peer for vm under each row, and vm's `ListenPort`.
* `hostprep`: a site with only `public_address` opens 51820/udp, and a port
  alone opens its port.
* `validate`: three voters, two dialled only through `public_address`, pass
  `voters-share-a-relay`.
* `dns`: a name beside a `public_address` still gets its A record; a port
  alone or no endpoint gets none.
* `apply`: the resolve step does not wait on a name beside a
  `public_address`.
* `siteadd`: a site with a `public_address` and no endpoint passes the scope
  check.
* Golden tree: the fixture's `watch`, with `endpoint: watch.example.org:51820`
  and `public_address: 203.0.113.20`, is dialled at `203.0.113.20:51820` by
  home-a, home-b and vm.
