# The account service: `account.` for members and admins, `invite.` for invitees

Status: approved in session on 2026-10-14, implemented in paisans-stack and paisans-account-service; not yet run end to end.

The invite service (`2026-10-12-invite-service.md`) becomes the account service.
It is the community's account management tool: today it carries invites and
their approval, and it is where promotion and banning will be built, so that
neither needs a Pocket ID administrator. This spec renames it, gives it two
hostnames, and makes invites a switch on it. It adds no new member or admin
feature.

Nothing runs in production. Every name changes outright, with no alias for the
old one and no conversion of an older deployment: a deployment is redeployed.

## What it must do

* **One app, two hostnames.** `account.<domain>` serves everything a signed in
  member or admin uses. `invite.<domain>` serves only the pages an invitee
  opens before they have an account.
* **Invites are a declared switch.** `settings.invites` is required. With it
  off, the `invite.` hostname does not exist anywhere: no DNS record, no Caddy
  block, no certificate, no route in the app.
* **The app exists because it is declared.** The toolkit never adds or drops
  the account app because of another app's settings. A public, self-service
  community leaves it out.
* **Each hostname serves only its own routes**, enforced in the app and again
  at the gateway.

## Names

| Thing | Name |
|---|---|
| GitHub repository | `Paisans-Software/paisans-account-service` |
| Go module | `github.com/paisans-software/paisans-account-service` |
| Command | `cmd/paisans-account-service` |
| Image | `ghcr.io/paisans-software/paisans-account-service` |
| Reserved blueprint | `account` |
| `foundation.yaml` key | `account` |
| Code of conduct in the container | `/etc/paisans-account-service/code-of-conduct.md` |
| Environment variable prefix | `ACCOUNT_` |
| First release | `v0.2.0`, continuing the repository's version series |

Service repositories of the organisation end in `-service`: a first-party
application people use. A helper that runs beside another application, such
as `wikijs-bootstrap`, is named for the application it serves.

## Configuration

```yaml
apps:
  account:
    blueprint: account
    hostname: account.example.org
    settings:
      identity_app: auth
      invites: true
      invite_quota: 3
      require_approval: true
      code_of_conduct: |
        ...
    hostnames:
      invite: invite.example.org   # optional; invite.<domain> when unset
```

`invites` is access policy, like `gated`, so it is declared and never
defaulted. The `invite_*` settings, `require_approval` and `code_of_conduct`
belong to invites and are valid only with `invites: true`.

### What `validate` refuses

| Rule | Refused or warned |
|---|---|
| `account-invites-unset` | `settings.invites` missing. Refused |
| `account-invite-settings-without-invites` | an `invite_*` setting, `require_approval` or `code_of_conduct` with `invites: false`. Refused |
| `account-invite-hostname-without-invites` | `hostnames.invite` with `invites: false`. Refused |
| `account-serves-nothing` | `invites: false`. Warning: the app has no feature without invites |

Every rule `validate` holds an `invite` app to today keeps its meaning under an
`account-` id and applies only with `invites: true`: the identity app's
`allow_user_signups` disabled or unset, `email_one_time_access_as_admin` not
`false`, `email_one_time_access_as_unauthenticated` `true`, one `pocket-id`
app, SMTP resolved, the code of conduct present, and the settings' ranges.

## The service

### Environment

| Variable | When | Used for |
|---|---|---|
| `ACCOUNT_PUBLIC_URL` | always | the OIDC redirect, links to `/admin`, the `Secure` cookie flag |
| `ACCOUNT_INVITES` | always, `true` or `false` | registers the invite routes and the invite UI |
| `ACCOUNT_INVITE_PUBLIC_URL` | required with `ACCOUNT_INVITES=true`, refused otherwise | invite links |

Every `INVITE_*` variable is renamed `ACCOUNT_*` (`INVITE_QUOTA` becomes
`ACCOUNT_INVITE_QUOTA`, `INVITE_SMTP_HOST` becomes `ACCOUNT_SMTP_HOST`, and so
on). Neither public URL may end in `/`.

### Routes by host

| Host | Routes |
|---|---|
| `account.` | `GET /`, `GET /auth/login`, `GET /auth/callback`, `POST /auth/logout`, `POST /invites`, `POST /invites/{id}/revoke`, `GET /admin`, `POST /admin/applications/{id}/approve`, `POST /admin/applications/{id}/decline`, `POST /admin/applications/{id}/dismiss`, `GET /static/` |
| `invite.` | `GET /i/{token}`, `POST /i/{token}`, `GET /static/` |
| any | `GET /healthz` |

The app registers each route with the host of its public URL in the
`ServeMux` pattern (`account.example.org/admin`), so a request for a route on
the other hostname does not match and gets a 404. Caddy passes the original
`Host` to the upstream, so this holds behind the gateway and equally for a
request that reaches the app directly on the mesh. `/healthz` has no host,
because health checks reach the app by its mesh address.

With `ACCOUNT_INVITES=false`, `/i/{token}`, `/invites` and the application
queue are not registered. The home page shows the signed in member, and
`/admin` shows the admin page with nothing to act on.

### Links

* Invite link: `ACCOUNT_INVITE_PUBLIC_URL + "/i/" + token`.
* The admins' notice that an application is waiting:
  `ACCOUNT_PUBLIC_URL + "/admin"`.
* OIDC redirect: `ACCOUNT_PUBLIC_URL + "/auth/callback"`.

An invitee who accepts is emailed Pocket ID's one-time link (`/lc/<token>`) and
never needs `account.` to finish.

### Cookies

The session cookie carries no `Domain` attribute, so the browser sends it to
`account.` only. The pages an invitee opens are on an origin that never holds a
session.

### Reaching Pocket ID

The service has two Pocket ID addresses, and they differ:

| Variable | Value |
|---|---|
| `ACCOUNT_OIDC_ISSUER` | `https://<pocket-id hostname>`, the issuer Pocket ID names itself by |
| `ACCOUNT_IDP_URL` | the API, called with the static API key |

`docs/specs/2026-10-14-pocket-id-api-private.md` makes the mesh the only
place the key works: the gateway refuses `X-API-Key`, each site's HAProxy
gains `listen pocket-id` on port 1413, and `ACCOUNT_IDP_URL` is
`http://<site mesh address>:1413`. Until that spec is implemented,
`ACCOUNT_IDP_URL` is the public hostname, as `INVITE_IDP_URL` is today. This
spec renames the variable and the files that spec names
(`internal/render/invite.go` becomes `internal/render/account.go`), and
updates that spec's text to the new names in the same change, so whichever
is implemented second reads current names. Step 5 of
`docs/specs/2026-10-14-repository-split.md`, moving the service onto
`pocketid-go`, needs the mesh route first, because that client refuses to
send the key to an address that is not private.

The `pocket-id` blueprint's Caddy snippet says which caller reaches
`/api/users/<id>/one-time-access-email` through the gateway. That caller is
the account service, and the comment names it so.

## The `account` blueprint

The reserved `invite` blueprint's directory becomes `account`. Its
`blueprint.yaml` declares `hostname_roles: [invite]` and the blueprint ships
`caddy.snippet.invite.tmpl`. It keeps one OIDC client with its callback on the
primary hostname, and `gate: {gateable: false}`.

### The `invite` hostname role

`InviteRole` joins `media` and `wellknown` in `knownRoles`. Like `media`, it
exists only under a condition and has a derived name:

```go
// InviteHostname is where an account app's invitees are served, and empty
// unless settings.invites is true. A declared hostnames.invite wins;
// otherwise invite.<domain>.
func InviteHostname(cfg *config.Config, app config.App) string
```

The derived name is the fixed label `invite` under the community's domain. One
account app exists per identity app, so it cannot collide with a second one;
`validate`'s hostname collision check catches a clash with another app.

Every consumer of an app's hostnames (the gateway render, `dns init`, the
certificate list, `validate`'s collision and character checks) reads a role's
hostname through one resolver that returns the active hostname for each role,
or none. A consumer that reads `app.Hostnames` directly is changed to use it,
so an `invite` hostname with invites off reaches none of them.

### Gateway snippets

* `caddy.snippet.invite.tmpl` proxies `/i/*` and `/static/*` and answers 404
  to every other path.
* `caddy.snippet.tmpl`, the primary, answers 404 to `/i/*`.

### Pocket ID's route exposure

The signup routes in the `pocket-id` snippet close on the rendered
`allow_user_signups` alone: closed when it is `disabled` or unset, open for
`withToken` and `open`. An account app with `invites: true` already requires
`disabled`, so an invite community gets the closed routes; with invites off,
or with no account app, Pocket ID's own setting decides. The snippet does not
read which apps name the identity app, and `IdentityClients` leaves the app
view unless another template reads it.

## Rollout

Each step is a pull request against the repository's usual branch, merged
before the next. Steps marked ⚠ are visible outside a repository or cannot be
undone, and each is confirmed with the founder at the time it runs.

1. ⚠ Rename `Paisans-Software/paisans-invite` to `paisans-account-service`.
   Point the local remote at the new name, move `src/paisans-invite` to
   `src/paisans-account-service` in the operator workspace, run
   `git worktree repair` there, and update that workspace's `.gitignore`
   entry and `src/CLAUDE.md`, whose account of `src/` names the service and
   the toolkit's `account` blueprint.
2. The account service: module path, command, environment, routes by host,
   links, cookies, `release.yml`'s image name, README and changelog under
   `0.2.0`. Its unit tests and its contract tests against Pocket ID pass.
3. ⚠ Tag `v0.2.0` on the account service, which publishes the image.
4. `paisans-stack`: the `account` blueprint, the `invite` role and its
   resolver, the `validate` rules, the `pocket-id` snippet's condition, the
   README section on the account app, and a `docs/decisions.md` entry.
5. `paisans-blueprints`: `foundation.yaml` gains
   `account: ghcr.io/paisans-software/paisans-account-service:0.2.*` and loses
   `invite`. ⚠ Releasing it breaks the current toolkit release's `invite`
   image, since core follows its newest release; accepted, as nothing runs in
   production.
6. ⚠ Release the toolkit, by the founder, under its release process.
7. ⚠ Delete the ghcr package `paisans-invite`:
   `gh api -X DELETE /orgs/Paisans-Software/packages/container/paisans-invite`,
   which needs `delete:packages` on the token. Only after step 5.

## Testing

* **The account service:** a request for every route on each host, asserting
  200 or the route's own status on its host and 404 on the other, and
  `/healthz` on a bare address. With `ACCOUNT_INVITES=false`, every invite
  route is 404 on every host. Config tests for `ACCOUNT_INVITE_PUBLIC_URL` required,
  refused and trailing slash. The invite link, the admin notice and the OIDC
  redirect each use the right base URL. The cookie has no `Domain`.
* **The toolkit:** render goldens for an account app with invites on and off,
  showing the `invite` snippet, DNS record and gateway block present and
  absent. A test per new `validate` rule. The `pocket-id` snippet table test
  keyed on `allow_user_signups` alone.
* Every repository's suite passes in a fresh clone of its branch before merge.
* **End to end, by the founder on a test deployment:** sign in on `account.`,
  make an invite, open it on `invite.`, accept it, sign in with the emailed
  link; `account.`'s paths answer 404 on `invite.`, and `/i/*` answers 404 on
  `account.`. Until that runs, nothing here has run end to end.

## Out of scope

* Who an administrator is: operators, community admins and Pocket ID
  administrators, and the admin reconciler that follows from them. Its own
  spec.
* Promotion and banning in the account service.
