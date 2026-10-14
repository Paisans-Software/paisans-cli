# Operators, and who manages accounts

Status: approved by the founder on 2026-10-14, not implemented.

A deployment has two kinds of people with power over it, and this spec names
them apart. **Operators** run the machines: they are declared in
`paisans-stack.yaml`, hold SSH keys and the secrets, and run `apply`.
**Community admins** run the community: they are the members of the Pocket ID
group `admins`, which every app reads for its administrators. Every operator
is also a community admin, so a new deployment has someone to run it; a
community admin is not an operator.

Who manages accounts (signup, bans, recovery) is a declared choice on the
`pocket-id` app: the account service, or Pocket ID's own administration. Under
the account service, nobody is a Pocket ID administrator.

The admin reconciler becomes the operator reconciler. It reads Pocket ID and
writes nothing: Pocket ID is the record of who is a member of the community,
and the reconciler reports when an operator in the yaml is not one.

Nothing runs in production. Every name changes outright, with no alias and no
conversion of an older deployment.

## Operators

The `admins:` section of `paisans-stack.yaml` is `operators:`, with the same
fields (`name`, `email`, `keys`). A site's `ssh.keys` names operators. Every
error, help text and document that said admin for this section says operator.

* **At least two.** `validate` refuses fewer than two operators whose keys are
  on the sites that run the `pocket-id` app (`operators-fewer-than-two`). One
  lost workstation must not leave nobody able to run the deployment.
* **Each operator is a Pocket ID user.** `apply` creates them as it creates
  declared admins today (username is the operator's key, email verified, name
  split at the first space), puts them in the group `operators` and in every
  app's member group, and gives a login link to one who has no passkey yet.
* **`isAdmin` only under `management: pocket-id`** (below). Under
  `management: paisans`, `apply` never sets it, and never clears it: a flag
  set by hand is the reconciler's to report.
* **A community admin as well.** `apply` puts every operator in every app's
  admin group, as it puts declared admins today. A new deployment's first
  community admins are its operators; anyone else joins `admins` by promotion,
  which a later spec gives the account service.
* **Removing an operator from the yaml** is how an operator is removed,
  including a ban. `apply` never deletes or disables a Pocket ID user; the
  person stays a member until the community removes them.
* **`paisans admin` is `paisans operator`**: `paisans operator register` and
  `paisans operator magic-link`, unchanged in behaviour.

The infrastructure apps that read an admin group for operator access read
`operators`: the uptime blueprint's default `admin_group` is `operators`.

## `management`

```yaml
apps:
  auth:
    blueprint: pocket-id
    hostname: id.example.org
    settings:
      management: paisans       # or pocket-id; required
```

`management` is required, never defaulted (`pocket-id-management-unset`).

| | `paisans` | `pocket-id` |
|---|---|---|
| An `account` app | required (`pocket-id-without-account`) | refused (`account-under-pocket-id-management`) |
| `allow_user_signups` | refused if set; rendered `disabled` | operator's choice |
| `email_one_time_access_as_unauthenticated` | refused if set; rendered `true` | operator's choice |
| `email_one_time_access_as_admin` | refused if set to `false`, as today | operator's choice |
| SMTP host and sender | required (`pocket-id-paisans-without-smtp`) | optional |
| Operators' `isAdmin` | never set by `apply` | set by `apply` |
| Signup, bans, recovery | the account service | Pocket ID's admin pages |

The refusal of a set value under `paisans` is `pocket-id-setting-managed`: the
toolkit renders these settings, so a declared one could only disagree.

The gateway's Pocket ID routes follow the rendered settings, as they do today
(`internal/blueprints/reserved/pocket-id/caddy.snippet.tmpl`). Under `paisans`
the rendered values close the signup routes and open the email-code routes
with no change to the snippet.

### The account app finds its identity provider

`identity_app` is removed from the `account` blueprint's settings, and refused
if set (`account-setting-unknown`). A deployment has at most one `pocket-id`
app; the account app uses it. An `account` app with no `pocket-id` app is
refused (`account-without-pocket-id`), as is more than one `pocket-id` app in
a deployment that has an `account` app (`account-one-identity-app`, kept).

## The operator reconciler

The admin reconciler is renamed throughout: `cmd/operator-reconciler`,
`internal/operatorreconciler`, image `ghcr.io/paisans-software/operator-reconciler`,
the core catalog's `foundation.yaml` key `operator-reconciler`, release tags
`operator-reconciler-v*`, the heartbeat `operators-heartbeat` and its monitor
`operator continuity`. Its image workflow publishes only from those tags; a
push to `develop` publishes nothing.

It runs beside Pocket ID on every site that runs it, as now, active or standby
as now, a pass every two hours and once at start. A pass **reads and never
writes**:

1. **Every operator in the yaml is a member.** For each operator: a Pocket ID
   user with that username exists, is not disabled, and is in the community's
   member group and in `operators`. A failure names the operator and what is
   missing.
2. **Every Pocket ID administrator is an operator.** Every enabled user with
   `isAdmin`, the static API key's user (`00000000-0000-0000-0000-000000000000`)
   excluded, is an operator. Under `management: paisans` there are none.
3. **DNS continuity**, where the deployment checks it
   (`docs/specs/2026-10-13-dns-admin-continuity.md`), checks the operators: the
   addresses compared with the DNS account's Super Administrators are the
   operators' Pocket ID addresses, found by username, not those of Pocket ID's
   administrators.

`/healthz` answers 503 when any check fails, before the first pass, when
Pocket ID does not answer, and once the last pass is more than two and a half
hours old, and 200 otherwise; the body names each failure. `/livez` is
unchanged. A standby answers `standby` with a 200, as now.

It reads from the rendered `.env`, beside what it reads today:

| Variable | Value |
|---|---|
| `PAISANS_OPERATORS` | the operators' usernames, comma separated, sorted |
| `PAISANS_MEMBER_GROUP` | the community's member group, `members` |
| `PAISANS_MANAGEMENT` | `paisans` or `pocket-id` |

The reconciler's group writes, its floor and its `PUT /api/users/:id/user-groups`
are removed with the rest of its write path. Its tests fail if any request it
makes is not a `GET`.

An operator banned or removed at Pocket ID fails check 1 until they are taken
out of the yaml. That is a manual step by design.

## `paisans create`

The wizard asks for `management`. With `paisans` it adds an `account` app and
asks what the account app's settings need; with `pocket-id` it asks for
`allow_user_signups` and the email-access settings, and SMTP becomes optional.
It writes an `operators:` section.

## Documentation

The README's sections on admins, the reconciler, Pocket ID's configuration,
invites and the gateway describe operators, community admins, `management`
and the operator reconciler as they are. `docs/decisions.md` gains one entry
for this spec. `docs/specs/2026-10-08-admin-reconciler.md` and
`docs/specs/2026-10-13-dns-admin-continuity.md` stay as the record of what was
approved then; the README is where current behaviour is described.
`docs/specs/2026-10-14-repository-split.md` and
`docs/specs/2026-10-14-pocket-id-api-private.md` are updated to the new names
only.

## Rollout

1. `paisans-stack`: all of the above, one pull request into `develop`.
2. `paisans-blueprints`: `foundation.yaml` replaces `admin-reconciler` with
   `operator-reconciler: "ghcr.io/paisans-software/operator-reconciler:0.3.*"`.
   One pull request into `main`; no release.
3. Founder: tag `operator-reconciler-v0.3.0`, release the core catalog and the
   toolkit, and delete the ghcr package `admin-reconciler` once nothing points
   at it.

## Testing

* `validate`: a fixture per new rule; `management` unset, `paisans` with each
  managed setting set, `paisans` without an account app or SMTP, `pocket-id`
  with an account app; fewer than two operators.
* Render: goldens for one deployment in each mode. Under `paisans` the
  Pocket ID `.env` has `ALLOW_USER_SIGNUPS=disabled` and
  `EMAIL_ONE_TIME_ACCESS_AS_UNAUTHENTICATED_ENABLED=true` and the gateway
  closes signup; the reconciler's `.env` carries the three new variables.
* `apply`: an operator is created without `isAdmin` under `paisans` and with it
  under `pocket-id`, in `operators`, every member group and every admin group.
* The reconciler: a table over operator present, missing, disabled, outside
  the member group, outside `operators`; an administrator who is an operator,
  one who is not, under each mode; DNS continuity against operators; no
  request other than `GET`.
* Every suite passes in a fresh clone before merge.
