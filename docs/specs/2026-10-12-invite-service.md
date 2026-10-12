# Invites: members bring people in, the code of conduct comes first

Status: approved in session on 2026-10-12, not implemented.

A deployment admits new people through invitations from its own members. A new
service, `paisans-invite`, lets a signed in member make an invite link. The
person who opens it reads the community's code of conduct, agrees to it, and
chooses a username, a display name and an email address. If the deployment
requires approval, an admin then approves or declines the application. Only
once it is approved does an account exist at the identity provider, and the new
member is emailed a one-time link to register a passkey.

A new `invite` kind deploys it, and the toolkit wires it to the deployment's
Pocket ID, its Postgres cluster and its mail.

## What it must do

* **Members invite, within a quota.** Every member may hold a configured
  number of live invites at once. Admins have no quota.
* **Nobody gets an account without agreeing to the code of conduct.** The
  agreement is recorded against the exact text agreed to, before any account
  is created.
* **Approval is a deployment's choice.** `require_approval` is set in
  `paisans.yaml`. With it off, agreeing creates the account at once.
* **An invite survives the loss of a site.** Its state is in the deployment's
  Postgres cluster, and the service runs on every cluster site.
* **A declined address is remembered, not refused.** Admins see how many times
  an address has been declined and when last. A member may invite it again.
* **No data is held on people who are not members**, beyond a keyed hash of a
  declined address, for a configured period.

## The service

`Paisans-Software/paisans-invite` is its own repository: one Go binary, with
its HTML templates and stylesheet embedded, published as
`ghcr.io/paisans-software/paisans-invite:<version>`. It keeps a
`CHANGELOG.md` in Keep a Changelog format. All of its state is in Postgres; the
container has no volume.

It has three parts with one purpose each:

* **The web application**: the member, invitee and admin pages and their
  handlers.
* **The store**: every read and write of its Postgres tables.
* **The identity provider adapter**: every call to the identity provider. It is
  the only package that sends HTTP to the provider.

### The identity provider adapter

The application never names Pocket ID. It speaks to an interface:

| Call | Does |
|------|------|
| `UsernameTaken(username)` | whether an account already has this username, compared case-insensitively |
| `EmailTaken(email)` | whether an account already has this email address |
| `FindUser(username)` | the provider's user, if one exists, so a retry never creates a second |
| `CreateUser(username, email, displayName)` | creates the account and returns its ID |
| `AdminGroupEmails(group)` | the email addresses of the members of the service's admin group |
| `SendLoginLink(userID)` | has the provider email the user a one-time sign in link that lasts 7 days |

The service keeps its own record of invites, applications and who invited whom.
Some of that duplicates what the provider holds, and that is accepted: it is
what lets a deployment change provider by writing a second adapter.

**The Pocket ID adapter**, against Pocket ID v2.18.0:

* `UsernameTaken`, `EmailTaken`, `FindUser`: `GET /api/users`, every page, matched
  case-insensitively on the field in question. Pocket ID's `search` is a
  case-sensitive `LIKE` on Postgres, so the service never relies on it.
* `AdminGroupEmails`: reads of the admin group and its members, so the people
  who can approve are the ones told an application is waiting.
* `CreateUser`: `POST /api/users` with `username`, `email`, `emailVerified:
  false`, `displayName`, and nothing else. **No `userGroupIds` and no
  `isAdmin`.** Pocket ID puts a user created without groups into the instance's
  signup default groups (`backend/internal/service/user_service.go:355-361`,
  `applyDefaultGroups` at `:382`), so group membership is the deployment's
  `signup_default_groups`, never the service's.
* `SendLoginLink`: `POST /api/users/:id/one-time-access-email` with the body
  `{"ttl":"168h"}` and nothing else (`backend/internal/onetimeaccess/module.go:69`).
  Without a `ttl` Pocket ID makes the link last 15 minutes, which an approved
  invitee would usually miss. This link is not tied to a device: it works on
  whichever phone or computer opens it. Pocket ID sends the mail
  itself, through its own SMTP settings, and refuses unless
  `EmailOneTimeAccessAsAdminEnabled` is on (`onetimeaccess/service.go:56-59`).

Usernames are checked against Pocket ID's rule before anything is stored:
`^[a-zA-Z0-9]([a-zA-Z0-9_.@-]*[a-zA-Z0-9])?$`, at most 50 characters
(`backend/internal/dto/validations.go:25`, `dto/user_dto.go:27`). A display
name is at most 100 characters.

**These two writes are the service's whole write surface at the identity
provider.** A test parses the adapter's source and fails on any HTTP method
other than `GET` outside those two calls, and on any request body field other
than those named above. Every write is logged and recorded in the audit table.

The adapter authenticates with the deployment's Pocket ID static API key, the
same `apps.<auth>.static_api_key` the toolkit itself uses. Pocket ID API keys
carry no scopes, so the key is all of Pocket ID, and a leak is answered by
rotating it across the deployment: `paisans secrets set
apps.<auth>.static_api_key`, an apply of every site, then a check of Pocket ID
for admins, clients and API keys nobody made. Per-route scopes are on Pocket
ID's main branch (pull request #1823) but in no release; once a release lets an
API key carry them, the service moves to a key limited to `users:read` and
`users:write`.

### Sign in

Members and admins sign in with OIDC, through an OIDC client of the service's
own. The client admits the deployment's member and admin groups. A member is
identified by the OIDC `sub` claim, never by a provider-internal ID. Admin pages
and actions check the admin group claim on every request.

### Tables

* **`invites`**: the SHA-256 of the invite token, the inviter's `sub` and their
  username at the time, created and expiry times, and a state: `open`,
  `applied`, `used`, `expired`, `revoked` or `declined`.
* **`applications`**: the invite, the chosen username, email and display name,
  the code of conduct version agreed to and when, a state (`pending`,
  `approving` or `link_unsent`), when an approval claimed it, and who decided
  and when. An approval claims the row (`pending` → `approving`) before it
  creates the account, so a decline, a second approval or the sweep cannot
  remove it mid-way; a claim older than ten minutes may be taken over. An application is deleted once its
  account exists and the link has gone, or once it is declined.
* **`declines`**: the keyed hash of a declined email address and when it was
  declined.
* **`audit`**: append only. Every write the service makes, to its own tables
  or to the provider, with IDs, the actor's `sub` and a timestamp. It never
  holds an email address, a name, an invite token or a credential.
* **`introductions`**: after approval, the new member's username and their inviter's
  `sub`. This is the record of who brought whom in, and it is all that is kept
  of an approved application.

### Flows

**A member makes an invite.** A signed in member asks for one. In a single
transaction the service counts the member's `open` and `applied` invites,
refuses if that count has reached `invite_quota` (admins skip the count), and
inserts the invite. The token is 32 random bytes; only its hash is stored. The
member is shown `https://<hostname>/i/<token>` to copy and send however they
like. Their own invites are listed with their state, and an `open` one can be
revoked. Doing the count and the insert in one transaction, with the member's
rows locked, is what stops two sites admitting two invites past the quota.

**An invitee opens the link.** A token that is unknown, used, expired, revoked
or declined shows one neutral page, the same in every case. A good token shows
the code of conduct, an agreement checkbox, and fields for username, display
name and email. On submission the service checks the username's form, that no
account and no pending application holds it, and that no account holds the
email address. It then stores the application with the code of conduct's
version and the time of agreement, and marks the invite `applied`.

* With `require_approval` off, it goes straight on to create the account.
* With it on, every member of the admin group is emailed that an application
  is waiting.

What the invitee is shown next, exactly:

* Approval off: **Check your email.** "We've sent you a sign-in link. It
  expires in 7 days." Below it, smaller: "Link expired? Go to
  `<identity host>`, choose sign in with an email code, and enter the code from
  the new email on that same screen."
* Approval on: **Thanks for applying.** "An admin will review your
  application. Once you're approved, we'll email you a sign-in link." Nothing
  about expiry: it may be days before the admin acts.

The recovery line relies on Pocket ID's own "sign in with an email code", whose
codes last 15 minutes and work only in the browser that asked for them.

**An admin approves or declines.** The queue shows each pending application:
the inviter, username, display name, email address, when it was made, and the
address's decline history, as a count and the date of the latest.

**Creating the account.** The service checks the username and email again,
calls `CreateUser`, then `SendLoginLink`, marks the invite `used`, writes the
`introductions` row, and deletes the application. If the account was created but the
link was not sent, the application stays as `link_unsent` and the admin sees a
retry. A retry calls `FindUser` first, and never creates a second account. A
username or email taken between application and approval fails the approval
with that reason, and the admin declines the application.

**Declining.** The service stores the keyed hash of the email address with the
time, deletes the application, marks the invite `declined`, which returns the
inviter's slot, emails the invitee a short neutral note, and emails the inviter
that it was declined, with no reason.

**The hourly sweep.** One site at a time, under a Postgres advisory lock:

* `open` invites past `invite_lifetime_days` become `expired`.
* `pending` applications older than `application_wait_days` are deleted, and
  their invites become `expired`. No decline is recorded.
* `declines` rows older than `decline_retention_days` are deleted.

### Declined addresses

The hash is HMAC-SHA256, keyed by a secret the toolkit generates, over the
address trimmed and lower-cased. Because it is keyed, a copy of the database
alone cannot be used to test a guessed address against it. Rotating the key ends the decline history, because no stored
hash matches any more.

### The code of conduct

`code_of_conduct` names a Markdown file beside `paisans.yaml`. It is mounted
read-only and rendered on the invitee's page. Its version is the SHA-256 of its
contents, recorded with every agreement, so an edit to the file is a new
version without anyone having to number it.

### Look and feel

The pages look like Pocket ID's. The service's stylesheet carries Pocket ID's
design tokens, copied from `frontend/src/app.css` at v2.18.0 under its
BSD-2-Clause licence with the notice kept: the colour variables for light and
dark, the radius and the font. Its card, button, input and alert styles follow
Pocket ID's components. It is plain CSS with no build step, served by the
binary. The file names the Pocket ID version it was copied from.

* The accent colour is the identity app's `accent_color`, rendered into the
  service's environment, so the two sites change together.
* The logo is Pocket ID's own, from `/api/application-images/logo`.
* Dark mode follows the browser's preference, as Pocket ID does.

### Security

* **Pages strangers use** (`/i/*`) are rate limited per client address, and
  every form carries a CSRF token. A 256-bit token cannot be guessed. The
  username check tells whoever holds a valid invite whether a name exists.
* **Logs** never hold an invite token, the API key, an email address or a name.
* **Mail** the service sends itself (the waiting notice to admins, the
  invitee's acknowledgement, a decline) goes through the deployment's `smtp:`
  settings. The login link is Pocket ID's own mail.

### Failures

* **The provider is down when someone applies**: the application is stored, and
  the availability checks are made again at approval.
* **The provider is down at approval**: the admin is shown the error and
  nothing changes. They retry.
* **Postgres is unreachable**: the service answers 503. Patroni's failover is
  what brings it back.

## The `invite` kind

```yaml
apps:
  invite:
    kind: invite
    hostname: invite.example.org
    placement: cluster
    settings:
      identity_app: auth
      invite_quota: 3
      require_approval: true
      invite_lifetime_days: 14
      application_wait_days: 14
      decline_retention_days: 183
      code_of_conduct: code-of-conduct.md
```

| Setting | Takes | Default |
|---------|-------|---------|
| `identity_app` | the name of a `pocket-id` app | required |
| `invite_quota` | 1 to 100 | `3` |
| `require_approval` | `true` or `false` | `true` |
| `invite_lifetime_days` | 1 to 365 | `14` |
| `application_wait_days` | 1 to 365 | `14` |
| `decline_retention_days` | 1 to 3650 | `183` |
| `code_of_conduct` | a path relative to `paisans.yaml` | required |

**`validate` refuses:**

* an `identity_app` that is missing or is not a `pocket-id` app;
* that app's `allow_user_signups` set to anything but `disabled`, because
  Pocket ID's own signup links would admit people past the code of conduct;
* that app's `email_one_time_access_as_admin` set to `false`, because the login
  link could not be sent;
* that app's `email_one_time_access_as_unauthenticated` not set to `true`,
  because a member whose sign-in link expired recovers through it;
* no `smtp:` settings;
* a `code_of_conduct` file that does not exist;
* any `placement` other than `cluster`;
* a setting outside the table's range.

**`init` generates** `apps.<invite>.email_hash_key` and `apps.<invite>.session_key`, 32 random bytes each. The session key signs the service's sign-in cookie; rotating it signs everyone out of the invite service and nothing else.

**`render` writes** the service's `.env`: its database connection through the
site's HAProxy, the identity app's URL and static API key, its OIDC client ID
and secret, the email hash key, the session key, the SMTP settings, the identity app's accent
colour, and every setting above. The code of conduct file is mounted
read-only.

**`apply`** creates its Postgres role and database as for any clustered app,
and creates its OIDC client as for any app with a client shape, admitting the
member and admin groups.

The image is pinned in the kind, as every other kind's is.

The example configuration's `signup_default_groups` becomes `[members]`: with
the code of conduct agreed before the account exists, a new account is a
member from its first sign in.

## Testing

**In `paisans-invite`:**

* Unit tests for each package.
* Flow tests against a real Postgres, run as a CI service container, with a
  fake identity provider: every flow above, every refusal, every state change.
* A concurrency test: two requests at once cannot take a member past the
  quota, and cannot use one invite twice.
* The write surface test described under the adapter.
* A contract test against a real Pocket ID v2.18.0 container, with Mailpit
  catching its mail: a created user lands in the signup default groups, and the
  login link is sent.

**In `paisans-stack`:**

* A test for each `validate` refusal.
* The render golden tree with an `invite` app.
* The example configuration validates with no refusals.

**On staging**, once deployment is approved: make an invite, apply, approve,
receive the login link, register a passkey, and sign in to another app as the
new member.

## Order of work

1. This spec.
2. The `paisans-invite` repository, through its first image.
3. The `invite` kind in `paisans-stack`.
4. Staging.

Each repository has its own plan and its own branches.
