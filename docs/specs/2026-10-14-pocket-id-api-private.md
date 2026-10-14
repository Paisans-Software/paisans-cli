# Pocket ID's API is reachable only over the mesh

Status: approved in session by the founder, 2026-10-14. Not implemented.
The gateway rule was observed against a running Pocket ID (*Observed*); the
private route for the account service is reasoned.

Pocket ID's static API key is a standing administrator credential: whoever
presents it can create administrators, read every user and mint sign-in
links. Every call the toolkit and its services make with it goes to a
Pocket ID on the mesh, except one, and nothing stops a call from the
internet: the gateway proxies Pocket ID's whole hostname, `/api` included,
so the key works from anywhere that can reach `https://<hostname>`.

This spec makes the mesh the only place the key works. The gateway refuses
any request that carries an API key, the one service that calls the API at the
public hostname moves to a route on the mesh, and the shared Pocket ID client
refuses to send the key anywhere else.

## The gateway refuses API keys

Pocket ID reads an API key from one header, `X-API-Key`
(`backend/internal/middleware/authenticator.go:20`, `:134`, `:138`).
Its web interface never sends that header: a browser signs in with the
`access_token` session cookie, and every page, every admin screen and passkey
registration use that cookie. The pocket-id blueprint's Caddy snippet
refuses the header before it proxies:

```caddy
# An API key is the toolkit's administrator credential, and it works only on
# the mesh. The web interface signs in with a session cookie and never sends
# this header, so refusing it here costs a member nothing.
@api_key header X-API-Key *
respond @api_key 403
```

The matcher is case insensitive in the header's name, as HTTP header names
are, and matches an empty value too. A request with the header is refused
whatever else it carries, a valid session included.

Members' own API keys, which Pocket ID lets a user create, are refused at the
gateway the same way. A member who wants to script against Pocket ID does it
from a machine on the mesh or not at all.

## The account service reaches Pocket ID over the mesh

The account service is the one caller at the public hostname today:
`internal/render/account.go` sets `ACCOUNT_IDP_URL` to
`https://<pocket-id hostname>`. It needs the instance that is active, on
whichever apps site that is, and its site can be any apps site.

Every site that runs the account service already runs HAProxy on its mesh
address, because the account service has cluster placement and reaches
Postgres through it. HAProxy gains a second listener, on the same pattern
as the first:

```
listen pocket-id
    bind {{ .Address }}:1413
    option httpchk GET /healthz
    http-check expect status 204
    default-server inter 3s fall 3 rise 2 on-marked-down shutdown-sessions
    server home-a 10.44.0.1:1411 check
    server home-b 10.44.0.2:1411 check
```

One `server` line for every apps site that runs the pocket-id app, at its
mesh address and Pocket ID's port. A standby refuses connections on that port
(`docs/specs/2026-10-07-pocket-id-standby.md`), so its check fails and only
the active instance is up. A takeover moves the route within three failed
checks, nine seconds, with no change on any site.

The listener is rendered on every site that runs HAProxy in a deployment
with a pocket-id app, so a service added later that needs the API finds it
already there. Port 1413 joins `render`'s port table beside the reconciler's
1412, so a collision on a site is refused like any other.

The account service's two Pocket ID addresses then differ:

| Variable | Value | Why |
|----------|-------|-----|
| `ACCOUNT_OIDC_ISSUER` | `https://<pocket-id hostname>` | An OIDC issuer is the URL Pocket ID names itself by, which is its `APP_URL`; a token whose issuer does not match is refused. It carries no key. |
| `ACCOUNT_IDP_URL` | `http://<site mesh address>:1413` | The API, with the key, over the mesh. WireGuard encrypts it between sites. |

## The client refuses to send the key off the mesh

`pocketid-go` (`docs/specs/2026-10-14-repository-split.md`) refuses a
`BaseURL` whose host is not private before it sends anything, from its first
release, `v0.1.0`. Founder decision. Private is a
loopback, RFC 1918, RFC 6598 or IPv6 unique local address, an address in a
network the caller names (the deployment's `mesh.subnet`), or a hostname with
no dot, such as `app` on a compose network. Anything else is refused with an
error that names the host and leaves out the key.

This is a guard against a mistake in configuration, such as a template that
renders the public hostname. The gateway rule is what holds when the code is
wrong.

## What does not change

* **The CLI.** Every call already goes over ssh to the site and runs `curl`
  against the mesh port.
* **The operator reconciler.** It calls `http://app:1411` on its own compose
  network.
* **The OIDC flows.** Discovery, the authorize, token and userinfo endpoints,
  and JWKS carry no API key, and every app reaches them at the public hostname
  as before.

## Documentation

`README.md`, in *The toolkit administers Pocket ID through its static API
key*, says the key "is reachable only over the mesh, because the port is
bound to the mesh address and nowhere else". It becomes true by this change;
the passage is rewritten to say the gateway refuses the header, which is why.

## Observed

2026-10-14, Pocket ID v2.14.0 and v2.18.0 (the newest on the `v2.*.*` track
that day), each behind Caddy 2.10.2 with the snippet above, SQLite, and a
static API key:

| Request | Through the gateway | Directly |
|---------|--------------------|----------|
| `GET /api/users` with the key | 403 | 200 |
| The header as `x-api-key`, `X-API-KEY` | 403 | |
| `POST /api/users` with the key | 403 | |
| A wrong key | 403 | |
| An empty `X-API-Key` | 403 | |
| A valid session cookie and the key | 403 | |
| `GET /api/users` with nothing | 401 | |
| `/`, `/login`, `/api/application-configuration` | 200 | |
| `/.well-known/openid-configuration` | 200 | |
| Sign in with a one-time code, then `/api/users/me` and `/api/users` as an administrator | 200 | |

By hand, in Chrome, through the gateway on v2.18.0: signing in with a
one-time code, registering a passkey, renaming it, and the users, groups and
OIDC clients admin pages all work.

Pocket ID's frontend source has no use of `X-API-Key` (a search of the
repository on 2026-10-14 finds it only under `backend/`).

Reasoned, not observed: the HAProxy listener and a takeover through it.

## Tests

* **Render.** The pocket-id snippet carries the matcher; the golden trees
  change accordingly. HAProxy's configuration carries `listen pocket-id` with
  one server per apps site running Pocket ID, on every site that runs HAProxy,
  and none in a deployment without Pocket ID. The account service's `.env`
  carries the mesh URL for `ACCOUNT_IDP_URL` and the public one for
  `ACCOUNT_OIDC_ISSUER`.
* **Docker, behind a build tag.** The table in *Observed*, against the
  pinned Pocket ID image and the Caddy image the foundation track resolves to.
* **`pocketid-go`.** The private address check, for each kind of address it
  accepts and a public address and hostname it refuses, with no request sent.
* **Staging.** A takeover with the account service mid use: its next call to
  Pocket ID succeeds on the new active site.
