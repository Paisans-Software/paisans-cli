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
writes the monitor's site into a file of its own, in a directory your server
includes with one line you add once (step 3).

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
  status:
    kind: uptime
    hostname: health.example.org
    placement: { pinned: health }
    settings:
      admin_group: admins
```

**Choose `listen` carefully.** Docker publishes a port with its own iptables
rules, which sit in front of ufw, so the firewall does not protect a published
port. Use `127.0.0.1` whenever the web server runs on the same machine: then
only that machine can reach the app. `paisans validate` refuses a public
address, and warns about a private LAN address (RFC 1918, or `100.64.0.0/10`
as Tailscale uses) or the site's mesh address, because anything that can
reach those reaches the app around your web server.
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

`host prepare` and `apply` start with the host check. A host already running a
web server is *shared*, which is fine here: nothing the monitor claims is
taken. On a shared host the toolkit leaves ufw's default policy alone, so ufw
must already be active and deny incoming by default, and it only adds its own
rules, each commented `paisans-<token>:` with the deployment's token. It does
not open 80 or 443 with `managed_by: external`;
your web server's rules are yours.

Once the monitor's stacks are up, `apply --site health --execute` goes on to
your web server: it writes the site file of step 3 and runs the check of step
6. Until your configuration includes the toolkit's directory it ends on a
warning, `!`, that says what to add, with exit status 0.

## 3. Include the toolkit's directory

`paisans apply --execute`, with or without `--site health`, writes the monitor's site,
filled in for the web server it finds, into a file of its own, `paisans-<token>-status.caddy` or
`.conf`, in a directory beside your server's main configuration:
`/etc/caddy/paisans.d`, `/etc/nginx/paisans.d` or `/etc/apache2/paisans.d` on
the machine itself. Your configuration includes that directory with one line,
which you add once:

| Server | Add | Where |
|---|---|---|
| Caddy | `import /etc/caddy/paisans.d/*` | at the top level of the Caddyfile, after the global options block if it has one, outside every site block and snippet |
| nginx | `include /etc/nginx/paisans.d/*.conf;` | inside the `http` block |
| Apache | `IncludeOptional /etc/apache2/paisans.d/*.conf` | at the top level, beside the server's other includes |

Caddy requires its global options block, the one that opens with `{` alone,
to come first in the Caddyfile. Put the import after it:

```
{
    email admin@example.org
}

import /etc/caddy/paisans.d/*

example.org {
    ...
}
```

An import above the global options block works while the directory is empty,
and once apply writes a site file, the site it imports comes ahead of the
options and Caddy refuses the whole configuration with `server block without
any key is global configuration, and if used, it must be first`. Apply reads
the Caddyfile first, and for an import above the global options block, or
one inside a site block or snippet, it writes nothing and warns with where to
move the line:

```
web server
  ! site file for status: caddy imports /etc/caddy/paisans.d above its global options block
       Nothing was written. /opt/caddy/Caddyfile has:
           import /etc/caddy/paisans.d/*
       above its global options block, the block that opens with { alone.
       Caddy requires that block to come first, so once a site file is in
       the directory the site it holds comes ahead of the options, and Caddy
       refuses the whole configuration: server block without any key is
       global configuration, and if used, it must be first. Move the import
       line to after the global options block:
           {
               email admin@example.org
           }

           import /etc/caddy/paisans.d/*

           example.org {
               ...
           }
       then run paisans apply --execute again.
```

The line must be a wildcard. If the directory is ever gone, a removed
deployment or a cleanup by hand, Caddy's `import` of a glob, nginx's `include`
of a glob and Apache's `IncludeOptional` simply include nothing, and the server
starts without the paisans sites. A line naming the file or the directory
itself, or Apache's plain `Include`, makes the next restart or reload fail,
which takes down every site on the machine, yours included. Apply reads your
configuration before it writes anything and refuses such a line, naming the
one to use instead.

You need not add it first: an apply that finds no line writes nothing, warns
with the exact line for your server, and ends without `Deployment complete.`.
The site file's line carries the instructions. The app's own check would
only fail for the same reason, so its line is left out; with `-v` it is
there, saying only that it fails until the file is in place. Add the line,
then apply again:

```
web server
  ! site file for status: caddy does not include /etc/caddy/paisans.d
       Nothing was written. Add this line to /opt/caddy/Caddyfile, at the top
       level of the Caddyfile, after the global options block if it has one,
       outside every site block and snippet, once:
           import /etc/caddy/paisans.d/*
       as here:
           {
               email admin@example.org
           }

           import /etc/caddy/paisans.d/*

           example.org {
               ...
           }
       then run paisans apply --execute again. It is a wildcard so that the
       server still starts, without the paisans sites, if the directory is ever
       gone.
       Or set sites.health.web_server.include_dir: false, and add what paisans
       web-server show --app status prints to your web server yourself.
```

With the line there, apply creates the directory if it is missing, writes the
file, validates your whole configuration, reloads a server in a container
(below), and records the directory in `paisans.yaml`:

```
web server
  ✓ site file for status   /opt/caddy/paisans.d/paisans-f2a9-status.caddy written, caddy reloaded
  ! health: include_dir set to /opt/caddy/paisans.d in paisans.yaml
  ✓ status                 health.example.org
```

The `include_dir` line is added under the site's `web_server`, every other line
and comment of `paisans.yaml` kept. Change it there to use another directory:
the next apply moves the file. When your configuration does not validate with
the file, apply puts back what was there and reloads nothing, so your server
keeps the configuration it ran, and warns with the server's own error; a
certificate certbot has not obtained yet is the usual cause, and the warning
names the `certbot certonly` command. An unchanged file is not written again
and nothing is reloaded, unless the app's check then fails: apply validates
again, in case you edited the configuration, reloads a server in a container
and checks the app again.

**A web server in a Docker container** is reloaded by apply once its
configuration validates: gracefully, through `docker exec` (`caddy reload`,
`nginx -s reload`, `apachectl graceful`), and with `docker restart` when that
fails, the step's line saying which. It reads the directory where the
container sees it. Apply takes the host path from the mount of the
configuration's directory, so with `/opt/caddy` mounted at `/etc/caddy` the
file goes in `/opt/caddy/paisans.d` and the line is `import
/etc/caddy/paisans.d/*`. A container that mounts its Caddyfile alone has no
such directory: apply records nothing and warns with the mount to add to the
web server's own service in its compose file,

```yaml
services:
  web:
    volumes:
      - /opt/caddy/Caddyfile:/etc/caddy/Caddyfile
      - /opt/caddy/paisans.d:/etc/caddy/paisans.d
```

then recreate the container, which is what makes Docker add a mount:

```
cd /opt/caddy && docker compose up -d web
```

`docker restart`, or a restart of the host, is not enough: a container keeps
the mounts it was created with. The next apply then finds the directory.

Apply reads the compose file that started the container, from the labels
Docker puts on it, so the warning names the file and the service, and the
command that recreates it. It only reads: it never edits the compose file and
never recreates the container. When the file already has a line for the
directory, the warning says which of the usual mistakes it is instead, each in
a few lines:

* the line is on the server's service, and the container was created before
  it: recreate it;
* the line is on another service, and the warning names both: move it;
* the line mounts the directory at a path the configuration does not import,
  such as `/etc/caddy/paisans` for `import /etc/caddy/paisans.d/*`: change the
  container path.

```
web server
  ! site file for status: no directory caddy reads to write in
       The compose file /opt/caddy/compose.yaml declares
       /opt/caddy/paisans.d:/etc/caddy/paisans.d for web, but the running
       container caddy-1 was created before it. Recreate it; a restart does not
       add a mount:
           cd /opt/caddy && docker compose up -d web
       then run paisans apply --execute again.
       Until the mount exists, caddy's import /etc/caddy/paisans.d/* matches
       nothing, which is harmless.
```

Relative paths in the compose file are read against the project's directory.
A volume written with a variable, `${DATA}/paisans.d`, is compared as written,
and the warning says so.

**A web server on the host itself** is yours to restart, so apply validates
its configuration with the file and does not reload it. The site file's line
is a warning with the command that reloads it, the systemd unit's when one is
active, and the run ends on that warning until you have:

```
web server
  ! site file for status: /etc/caddy/paisans.d/paisans-f2a9-status.caddy written, caddy not reloaded
       caddy's configuration is valid with it, and caddy runs on the host
       itself, so apply leaves its reload to you:
           sudo systemctl reload caddy
       then run paisans web-server check --app status.
```

Without an active unit the command is `caddy reload --config <your Caddyfile>
--adapter caddyfile`, `sudo nginx -s reload` or `sudo apachectl graceful`.

**The files are the toolkit's, and only they are.** Each is recorded in the
deployment's manifest. `paisans app remove --app status`, `paisans site
remove`, including `--force` on an abandoned host, and an apply where the
app's hostname left the site or `include_dir` changed delete that file and
nothing else, never the directory or another file in it, then validate the
same way, reloading a server in a container and naming the reload command for
one on the host. A file you edited is left where it is and reported. With
`site remove --host-gone` the host is not reached, so the file stays, and the
command says so.

To keep the configuration by hand instead, or for a server apply writes no
file for (anything but Caddy, nginx and Apache), set `include_dir: false`
under the site's `web_server`: apply writes nothing, and you copy the snippet
`web-server show` prints, below.

## 4. Get the hand-off sheet

```sh
paisans web-server show --app status
```

It reads the machine's web server over ssh, changing nothing, and prints:

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
    slash, because the app's routing ignores both: the status page lists
    every monitor, mesh addresses included, and `/metrics` is public whenever
    no API token exists;
* one line saying which web server it found, where, and how it comes by its
  certificates, for you to check, such as:

  ```
  web server: caddy (docker container caddy-1, config /opt/caddy/Caddyfile), obtains its own certificates (this site uses HTTP-01)
  ```

* the snippet for that web server, filled in;
* a warning when `listen` is not loopback, and one when Caddy's global options
  set `acme_dns` (below).

It looks for a container publishing 443 or 80 first, then for the process
listening on 443 or 80 on the machine itself, using sudo only where docker or
`ss` need it; it asks for the password once. What the snippet says about the
certificate follows what it found:

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
| none | HTTP-01; the line says `(this site uses HTTP-01)` |
| `cert_issuer acme { dns <provider> … }` | HTTP-01: its own issuer replaces the global one; the line says `(DNS-01 globally; this site uses HTTP-01)` |
| `acme_dns <provider> …` | DNS-01 with your global token: Caddy gives `acme_dns` to every ACME issuer without a DNS provider of its own, this one included |

With `acme_dns`, `show` and `apply` warn, and the site file is still written,
since it is valid and changes nothing there:

```
  ! site file for status: caddy's global acme_dns applies to health.example.org too
       /opt/caddy/Caddyfile sets, in its global options:

           acme_dns cloudflare {$CF_API_TOKEN}

       ... To have this site use HTTP-01, replace it with:

           cert_issuer acme {
               dns cloudflare {$CF_API_TOKEN}
           }
```

Either make sure the token covers the hostname's zone, or make that rewrite.
It gives the same DNS challenge to every site without an issuer of its own, but
drops Caddy's default ZeroSSL fallback issuer for them. A value in the line
that is not a placeholder is shown as `<value>`, so a token written into the
Caddyfile is never printed.

This snippet is what apply writes into the include directory. With
`include_dir: false`, copy it into your web server's configuration yourself,
obtain the certificate if the sheet says to, and reload the server.
`--sudo=false` reads as the ssh user alone.

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
paisans web-server check --app status
```

It changes nothing: it looks at the monitor as a visitor would, and marks
each check passed (`ok`, or ✓ at a terminal) or failed (`FAIL`, or ✗), with
what fixes a failure. When a check fails it also reads the web server over
ssh, as `web-server show` does, and names it.

| Check | Passes when | When it fails |
|---|---|---|
| `dns` | the hostname resolves to `public_address` (and `public_address6`), and to nothing else | create the records, step 5; delete any other record for the name by hand |
| `certificate` | `https://<hostname>/healthz` answers 200 with a certificate valid for the hostname; prints the days left | the certificate lines, step 4, or the proxy if the answer is not 200 |
| `sign in` | `/login/oidc` redirects to the identity provider with a callback under `https://<hostname>/` | a redirect back to `/login` means the app's client at Pocket ID is missing (`paisans oidc client create --app status`, then `paisans apply --site health`) or the identity provider is down; a wrong callback means apply has not reached the app, or the web server rewrites `Host` or `Location` |
| `upstream` | a connection to `public_address` (and `public_address6`) on the `listen` port is refused or not answered | the app is reachable around your web server: use a loopback `listen`, or drop the port in Docker's `DOCKER-USER` chain. Any other failure, such as no route from where you run it, says nothing: one conclusive answer still passes and names the family it could not reach, and with none the check is inconclusive, so run it from a machine that can reach the site |

Run it again after every change until no check fails and it ends with
`4 checks passed.`

`paisans apply --execute` without `--site` runs the same checks for every app
behind your own web server once every other step has run, and `paisans apply
--site health --execute` runs them for the monitor's apps once its stacks are
up, in a `web server` section:

```
web server
  ✓ status   health.example.org
```

When one fails the app's line is a warning, `!`, with the checks that failed
under it, an error several share said once, and the web server it finds in
front of the app, read with the sudo password the run asked for at its start.
When the app's site file step above already warned, the check's line says
only that it fails until that is fixed, so each problem comes with one set of
instructions. The run ends on a warning, `! Deployment complete, but 1 app
failed its web server check: status`, rather than `Deployment complete.`, or
`Applied <n> files to health.` for `apply --site health`, since a visitor cannot
use the app yet. Every step ran, so the exit status is
still 0. What apply could not do on the host, such as reaching it over ssh to
read the record of its site files, is a failure instead, `✗`, and the run
exits 1.

A web server that has just taken in a site file, or been reloaded, may still
be obtaining its certificate; Caddy refuses the name meanwhile, so
`certificate` and `sign in` fail. Those two are checked again after 2, 4, 8
and 16 seconds and then 30, a minute in all, and the line says what it waits
for:

```
web server
  ⠋ status: waiting for caddy's certificate: 14s
```

It passes as soon as every check does, `✓ status   health.example.org, after
14s`, and after a minute fails on the last check's result. Waiting cannot fix
a name that does not resolve or an app reachable around the web server, so
those fail at once. A site file already in place and unchanged is checked
once, and waited on only after apply reloaded a server in a container.
`paisans web-server check` checks once, when you are ready. Fix what it names with steps 3 to 5, then run `paisans web-server
check --app status` until it passes.

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

Changing your web server, its certificate or its address needs nothing from
the toolkit. Changing `listen` does: edit `paisans.yaml`, apply the site, and
update the upstream in your web server to match, then run `web-server check`
again.
