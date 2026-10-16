# Running the monitor behind your own web server

The uptime monitor runs on a site of its own, with the `monitor` role, and
serves its own hostname rather than going through the gateway. By default the
toolkit runs its own Caddy there (`managed_by: paisans`), which needs ports
80 and 443 to itself. This guide is for the other case: the machine you want
the monitor on already runs a web server on 80 and 443, and that web server is
going to stay in front of it.

With `managed_by: external` the toolkit runs the monitor's container and nothing in front of
it, published on `listen` alone. Your web server terminates TLS and proxies to
the container. No DNS token goes on this machine, since the toolkit obtains
no certificate here; with `managed_by: paisans` the monitor's Caddy holds the same
zone token the gateway does, which a challenge only zone can narrow (see the
header of the rendered `caddy.env`). The
certificate, and renewing it, are yours: the toolkit cannot know how your
server obtains one, and it does not edit a configuration it does not own. It
writes the monitor's site as a snippet in a reference file of its own, and you
install it in your server (step 3).

`README.md` has the reasoning, under *The monitor has a site of its own*.

## 1. Declare the site

```yaml
sites:
  health:
    roles: [monitor]
    address: 10.44.0.4                # its mesh address
    public_address: 203.0.113.20      # what the internet and the mesh reach it on
    web_server:
      managed_by: external
      listen: 127.0.0.1:8480          # where your web server proxies to

apps:
  health:
    blueprint: uptime
    hostname: health.example.org
    placement: { pinned: health }
    settings:
      admin_group: operators
```

**Choose `listen` carefully.** Rootless Docker publishes a port through
rootlesskit, a listener on the host's own network stack, so the firewall's
rules apply to it, but they admit the whole mesh. Use `127.0.0.1` whenever the
web server runs on the same machine: then only that machine can reach the app.
`paisans validate` refuses any address but loopback and the site's own mesh
address, a public or private LAN address among them (RFC 1918, or `100.64.0.0/10`
as Tailscale uses), and warns about the mesh address, because anything that can
reach the mesh reaches the app around your web server.
Pick a port nothing else on the machine uses; `validate` refuses one the
toolkit already binds there.

The app's compose network is pinned at `10.255.255.0/29`, so that the address
your web server's connections arrive from through Docker's publish is known
and is all the app trusts to report a client's address. The host check
refuses a machine where another network or a route already uses that range,
and `validate` refuses a mesh over it.

The site needs no other role, and should have none of `gateway` or `witness`,
which are refused: the monitor exists to report their failures. Pin no other
app there either: `validate` refuses one, because with `managed_by: external` your web server
is handed the monitor alone and nothing would route the other.

## 2. Check the configuration and apply it

```sh
paisans validate
paisans host prepare --site health           # shows what it would do
paisans host prepare --site health --execute
paisans apply --site health                  # shows what would change
paisans apply --site health --execute
```

`host prepare` and `apply` start with the host check. A web server on the host
is fine here, whatever class the host check gives its listeners: the monitor
claims neither 80 nor 443. The firewall is the host owner's: `host admit` leaves ufw's default
policy alone and only adds the deployment's own rules, each commented
`paisans-<token>:` with the deployment's token. It does
not open 80 or 443 with `managed_by: external`;
your web server's rules are yours.

Once the monitor's stacks are up, `apply --site health --execute` goes on to
your web server: it writes the reference snippet of step 3 and runs the check
of step 6. Until the check passes it ends on a warning, `!`, that names
`paisans web-server show --app health`, with exit status 0.

## 3. Install the snippet

`paisans apply --execute`, with or without `--site health`, writes the monitor's site,
filled in for the web server `host admit` found, into
`/srv/paisans/<token>/web-server/health.caddy` or `health.conf`. When admit
found no server it knows, it writes `health.caddy`, `health.nginx.conf` and
`health.apache.conf`. The file is the deployment's, and a later apply puts it
back if it is missing or edited. The toolkit's user cannot write your web
server's configuration, test it or reload it, and nothing grants it that, so
nothing there changes until you do it. `paisans web-server show --app health`
prints the same snippet, with the rest of the sheet of step 4, from the
admission record, as the site's ssh user.

Put the snippet where your server reads its sites:

| Server | Where the snippet goes |
|---|---|
| Caddy | a site block, at the top level of the Caddyfile, after the global options block if it has one |
| nginx | a `server` block in a file under `conf.d` or `sites-enabled` |
| Apache | a `VirtualHost` |

Caddy requires its global options block, the one that opens with `{` alone,
to come first in the Caddyfile, so the site block goes after it:

```
{
    email admin@example.org
}

health.example.org {
    ...
}

example.org {
    ...
}
```

Validate your configuration and reload your server yourself (`caddy validate`
and `caddy reload`, `nginx -t` and `nginx -s reload`, `apachectl configtest`
and `apachectl graceful`, or your service manager's equivalent). Nothing apply
does touches your configuration or reloads it.

Apply checks the app from outside right after writing the snippet, once and
without waiting, and warns until your copy serves it:

```
web server
  ! health   health.example.org
       ...
       The host's owner installs what paisans web-server show --app health
       prints in their web server, then run paisans web-server check --app health.
```

When the check fails after a change to `paisans-stack.yaml` rewrote the
snippet, the warning says that apply rewrote the reference file and that your
copy needs replacing with what `web-server show` prints now.

## 4. Get the hand-off sheet

```sh
paisans web-server show --app health
```

It reads the machine's admission record over ssh, changing nothing, and prints:

* the hostname, the upstream (`http://127.0.0.1:8480`) and the health path,
  `/healthz`;
* what your web server must do:
  * terminate TLS for the hostname, with a certificate you obtain and renew;
  * proxy every path to the upstream, but for the four below;
  * pass the `Host` header through unchanged;
  * set `X-Forwarded-For` to the client's address and `X-Forwarded-Proto` to
    `https`, because the monitor's sign in rate limit is keyed on the client's
    address;
  * refuse `/status*`, `/badge/*`, `/metrics` and `/api/v1/*`, as the
    toolkit's own edge does, in any case and with or without a trailing
    slash, because the app's routing ignores both: the health page lists
    every monitor, mesh addresses included, and `/metrics` is public whenever
    no API token exists;
* one line saying which web server it found, where, and how it comes by its
  certificates, for you to check, such as:

  ```
  web server: caddy (docker container caddy-1, config /opt/caddy/Caddyfile), obtains its own certificates
  ```

* the snippet for that web server, filled in;
* a warning when `listen` is not loopback.

The record holds the web server `host admit` detected as root: a container
publishing 443 or 80 first, then the process listening on 443 or 80 on the
machine itself. What the snippet says about the certificate follows what it
found:

| Found | Certificate in the snippet |
|---|---|
| Caddy whose admin API shows it obtains its own certificates | `tls { issuer acme }`: Caddy obtains this one too, over HTTP-01 |
| Caddy loading certificate files for its other sites | a `tls` line naming files for this hostname by the same pattern |
| nginx or Apache with certificates under `/etc/letsencrypt/live/` | the files certbot will write for this hostname, after the `certbot certonly` command that obtains them the way your other certificates were |
| nginx or Apache with other certificate files | files for this hostname by the same pattern, where the paths name a host |
| nothing it could read | the Caddy, nginx and Apache snippets, Caddy's `tls` line commented out and each marked `YOUR CERTIFICATE`, with the line above saying what could not be determined |

When Caddy also serves a listener bound to a private address only, such as a
Tailscale one, the snippet says which listener the site belongs on.

### A Caddy that obtains its own certificates

The site gets an issuer of its own, so its certificate never depends on a DNS
challenge you set for your other sites, whose token may not cover the
monitor's zone:

```
health.example.org {
	tls {
		issuer acme
	}
	...
}
```

With no DNS provider, Caddy obtains the certificate over HTTP-01, or
TLS-ALPN-01. Both need the hostname's A record (and AAAA, with
`public_address6`) pointing at this machine, which step 5 publishes, and port
80, or 443 for TLS-ALPN-01, reachable from the internet. Your global `email`
still applies.

How your global options set a DNS challenge decides whether the site keeps out
of it:

| Global options | This site |
|---|---|
| none | HTTP-01 |
| `cert_issuer acme { dns <provider> … }` | HTTP-01: its own issuer replaces the global one |
| `acme_dns <provider> …` | DNS-01 with your global token: Caddy gives `acme_dns` to every ACME issuer without a DNS provider of its own, this one included |

With `acme_dns`, either make sure the token covers the hostname's zone, or
replace it with:

```
cert_issuer acme {
    dns cloudflare {$CF_API_TOKEN}
}
```

That gives the same DNS challenge to every site without an issuer of its own,
but drops Caddy's default ZeroSSL fallback issuer for them. Admit does not
record your global options, so neither `show` nor `apply` can tell you which
case you are in.

This snippet is what apply writes to the reference file. Copy it into your web
server's configuration yourself, obtain the certificate if the sheet says to,
and reload the server.

## 5. Publish the name

The hostname must resolve to the monitor's `public_address`, not the
gateway's.

```sh
paisans dns init              # shows the records it would create
paisans dns init --execute
```

`dns init` points every hostname of an app pinned to a monitor site at that
site's `public_address`, and at `public_address6` too when it is set. If you
manage DNS by hand, create the same records.

If the monitor's hostname already has a record pointing at the gateway, from
before the monitor had a site of its own, `dns init` stops with a conflict:
it never updates a record, and `dns prune` keeps one whose name is still
wanted. Change that record by hand at the provider, then run `dns init`
again.

## 6. Check it from your machine

```sh
paisans web-server check --app health
```

It changes nothing: it looks at the monitor as a visitor would, and marks
each check passed (`ok`, or ✓ at a terminal) or failed (`FAIL`, or ✗), with
what fixes a failure. When a check fails it also reads the admission record
over ssh, as `web-server show` does, and names the web server.

| Check | Passes when | When it fails |
|---|---|---|
| `dns` | the hostname resolves to `public_address` (and `public_address6`), and to nothing else | create the records, step 5; delete any other record for the name by hand |
| `certificate` | `https://<hostname>/healthz` answers 200 with a certificate valid for the hostname; prints the days left | the certificate lines, step 4, or the proxy if the answer is not 200 |
| `sign in` | `/login/oidc` redirects to the identity provider with a callback under `https://<hostname>/` | a redirect back to `/login` means the app's client at Pocket ID is missing (`paisans oidc client create --app health`, then `paisans apply --site health`) or the identity provider is down; a wrong callback means apply has not reached the app, or the web server rewrites `Host` or `Location` |
| `upstream` | a connection to `public_address` (and `public_address6`) on the `listen` port is refused or not answered | the app is reachable around your web server: use a loopback `listen`, or drop the port in Docker's `DOCKER-USER` chain. Any other failure, such as no route from where you run it, says nothing: one conclusive answer still passes and names the family it could not reach, and with none the check is inconclusive, so run it from a machine that can reach the site |

Run it again after every change until no check fails and it ends with
`4 checks passed.`

`paisans apply --execute` without `--site` runs the same checks for every app
behind your own web server once every other step has run, and `paisans apply
--site health --execute` runs them for the monitor's apps once its stacks are
up, each app once and without waiting, in a `web server` section:

```
web server
  ✓ health   health.example.org
```

When one fails the app's line is a warning, `!`, with the checks that failed
under it, an error several share said once, the web server it found, and the
`paisans web-server show --app health` and `paisans web-server check --app
health` commands that finish the hand-off. The run ends on a warning, `!
Deployment complete, but 1 app failed its web server check: health`, rather
than `Deployment complete.`, or `Applied <n> files to health.` for `apply
--site health`, since a visitor cannot use the app yet. Every step ran, so the
exit status is still 0. What apply could not do on the host, such as reaching
it over ssh or writing the reference file, is a failure instead, `✗`, and the
run exits 1.

A web server that has just taken in your snippet may still be obtaining its
certificate, and Caddy refuses the name meanwhile, so `certificate` and `sign
in` fail. Install the snippet and reload, give the server a moment, then run
`paisans web-server check --app health` until it passes. It checks once, when
you are ready. Fix what it names with steps 3 to 5.

## Afterwards

The monitor does not check its own public URL: a monitor watching itself
reports nothing when it is down. Every other site pushes its heartbeat to
`https://<hostname>/ping/<token>` once a minute, so a certificate there that
has expired, or a web server that stopped answering, leaves every heartbeat
stale at once, and the monitor reports it. Where the deployment has a second
monitor on a second `monitor` site, that one checks this URL every minute,
warns 14 days before the certificate there expires, and reports a monitor
whose host has died. With one monitor, watch the certificate's renewal in
your web server yourself.

A removed app's block is yours to remove from your web server. `paisans app
remove` and `paisans site remove` delete the reference file, which is the
deployment's, and name the block to remove; until you do, your server keeps
routing the hostname to an app that no longer runs.

Changing your web server, its certificate or its address needs nothing from
the toolkit. Changing `listen` does: edit `paisans-stack.yaml`, apply the site, and
update the upstream in your web server to match, then run `web-server check`
again.
