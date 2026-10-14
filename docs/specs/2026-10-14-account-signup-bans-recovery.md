# The account service: signup, bans and recovery

Status: approved by the founder on 2026-10-14, not implemented. Builds on
`docs/specs/2026-10-14-account-service.md` and
`docs/specs/2026-10-14-operators-and-management.md`, which lands first.

Under `management: paisans`, the account service is how people join, how they
are banned and how they recover an account. Pocket ID's own signup is off and
nobody is a Pocket ID administrator, so these exist nowhere else. Promotion to
community admin is a later spec.

Nothing runs in production. Names change outright, with no alias.

## Signup

`settings.invites` is replaced by `settings.signup`, required, with two values:

| `signup` | Who can join |
|---|---|
| `invite` | someone holding a member's invite link, as today |
| `open` | anyone, through a registration page |

Both modes: the person reads and agrees to the code of conduct, chooses a
username, a display name and an email address, and is checked against the ban
list. With `require_approval` true an admin approves the application first;
otherwise the account is created at once. Either way the service creates the
account at Pocket ID through its API and Pocket ID emails a one-time sign-in
link, as the invite flow does today. `require_approval` and `code_of_conduct`
apply in both modes; `invite_quota` and `invite_lifetime_days` only with
`signup: invite` (`account-invite-settings-without-invites` becomes
`account-invite-settings-under-open-signup`).

### `join.` replaces `invite.`

The hostname role `invite` is `join`, derived `join.<domain>` unless
`hostnames.join` is declared. It exists for every account app, since every
account app takes signups. It serves the pages for people who have no account:

| `signup` | `join.` routes |
|---|---|
| `invite` | `GET`, `POST /i/{token}`, `GET /static/` |
| `open` | `GET`, `POST /` (the registration page), `GET /static/` |

`account.` answers 404 to both, and `join.` to everything else, in the service
(host patterns) and at the gateway (`caddy.snippet.join.tmpl`), as for
`invite.` today. Every invite-mode route of `account.` (`POST /invites`,
revoke) exists only with `signup: invite`.

### Environment

| Variable | Replaces |
|---|---|
| `ACCOUNT_SIGNUP` (`invite` or `open`) | `ACCOUNT_INVITES` |
| `ACCOUNT_JOIN_PUBLIC_URL` (always required) | `ACCOUNT_INVITE_PUBLIC_URL` |
| `ACCOUNT_OPERATORS` (usernames, comma separated) | new |

### Open registration

`POST /` on `join.` is rate limited per client address as `/i/{token}` is
today. An application under open signup has no invite and no inviter; the
approval queue shows it as "registered" instead of "invited by". Everything
after the form (the declined-address count, approval, account creation, the
sign-in link) is the invite flow's.

## Bans

Admins ban from `/admin/members` on `account.`: a search by username, display
name or email through Pocket ID's user list, and a ban button on each result.
`/admin/bans` lists every ban, current and lifted.

* **A ban** disables the user at Pocket ID (`PUT /api/users/:id`, every other
  field sent back as read, `disabled: true`), then records it: the user's ID
  and username, the keyed hash of their email under the service's
  `email_hash_key`, the reason the admin gives (required), who banned and
  when. The record is written in the same transaction as the audit entry.
* **Banned addresses cannot sign up.** Every signup, in both modes, is refused
  when the hash of its address matches a current ban. The person is told the
  application cannot be accepted, and nothing more.
* **Lifting a ban** re-enables the user and marks the record lifted, with who
  and when. Groups were never touched, so everything the member had returns.
* **Operators cannot be banned here.** A user whose username is in
  `ACCOUNT_OPERATORS` has no ban button and a ban request for one is refused:
  an operator is removed by editing `paisans-stack.yaml`. An admin cannot ban
  themselves. Admins are otherwise banned like any member.
* **A ban does not end sessions apps already hold.** Pocket ID refuses the
  next sign-in, but an app that keeps its own session keeps the member signed
  in until it expires. The ban confirmation page says so. Accepted for now;
  Paisans-Software/paisans-invite#4 holds the follow-up.

## Recovery

Pocket ID's email code covers a lost passkey with the address still in hand:
`email_one_time_access_as_unauthenticated` is rendered `true` under
`management: paisans`.

### Changing one's own email or name

Members cannot edit their own Pocket ID account (`allow_own_account_edit` is
rendered `false`), so `account.` offers it:

* **Email:** `/account/email` takes a new address and emails a six-digit code
  to it, valid for 15 minutes, stored only as a hash. Entering the code sets
  the address at Pocket ID, marked verified, and emails the old address that
  it changed. An address already used by another user, or matching a current
  ban, is refused.
* **Name:** `/account/name` sets the display, first and last name directly.
  The username never changes.
* **Operators** change their address and name in `paisans-stack.yaml`, so the
  service refuses both for a user in `ACCOUNT_OPERATORS`.

### Recovery two admins vouch for

For a member who has lost both their passkey and their email, at
`/admin/recovery`:

1. An admin opens a request for the user: the new address and how the member's
   identity was confirmed (required text).
2. A **different** admin approves it within 48 hours. On approval the service
   sets the new address at Pocket ID, marked verified, and asks Pocket ID to
   email a one-time sign-in link to it
   (`POST /api/users/:id/one-time-access-email`).
3. Either admin may cancel it before approval. An unapproved request expires.

Each step is an audit entry. Operators are refused here too; they recover with
`paisans operator magic-link`.

## Storage

New tables in the service's Postgres database: `bans`, `email_changes` (user,
new address, code hash, expiry, attempts) and `recovery_requests` (user, new
address, reason, opened by, approved by, state, times). `applications.invite_id`
becomes nullable for open registrations. Migrations follow the service's
existing scheme.

## The toolkit

* The `account` blueprint: `signup` replaces `invites`, required
  (`account-signup-unset`, `account-setting-invalid` for any other value);
  the hostname role `invite` becomes `join` with `InviteHostname` becoming
  `JoinHostname`, present for every account app; renders `ACCOUNT_SIGNUP`,
  `ACCOUNT_JOIN_PUBLIC_URL` and `ACCOUNT_OPERATORS`.
* `account-serves-nothing` and `account-invite-hostname-without-invites` are
  removed: every account app takes signups and has `join.`.
* README and `docs/decisions.md` as for the previous spec.

## Testing

* **The service:** routes by host for each signup mode; open registration end
  to end against the fake identity provider; a banned address refused in both
  modes; ban and lift, the operator and self refusals, the confirmation page's
  session notice; email change (wrong code, expired code, address in use,
  banned address, old address notified); name change; recovery (same admin
  cannot approve, expiry, cancel, operator refused); contract tests against
  Pocket ID for disabling and re-enabling a user and for setting an email.
* **The toolkit:** goldens for each signup mode; a fixture per changed rule;
  `join.` in DNS and the gateway for every account app.
* Every suite passes in a fresh clone before merge.

## Rollout

1. `paisans-invite` (the account service): one pull request into `develop`.
2. `paisans-stack`: one pull request into `develop`.
3. Founder: the ⚠ steps of `docs/specs/2026-10-14-account-service.md`, with the
   service released as `v0.2.0` carrying this spec.
