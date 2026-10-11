# `paisans apply` makes the declared admins and hands each a login link

Date: 2026-10-11. Status: approved by the founder in session.

A new deployment comes up from `paisans apply` with every app running and no
one able to sign in: Pocket ID has no administrator, and its users sign in
with passkeys, so there is no password to hand anyone. Today the run ends
with a note naming `paisans app admin create`, to be run once per admin with
a username and an email the configuration already holds in its `admins`
section.

`paisans apply` does it instead. Once Pocket ID is running, each admin the
`admins` section declares is made a Pocket ID administrator, and each one who has
not yet registered a passkey gets a fresh **login link, valid for 24 hours**.
Every link is printed at the end of the run, so it can be passed on by any
means. When the deployment can send mail, Pocket ID also emails each admin a link
of their own, and the run reports whether that send failed.

## Scope

In: the admins in `paisans.yaml`'s `admins` section, on every Pocket ID app the
deployment runs, on a whole `apply --execute`.

Out:

* **Taking an admin away.** An admin removed from the list is left as they
  are in Pocket ID. Demoting or deleting an account is a separate, deliberate
  act, and the admin reconciler already keeps the admin group in step with
  Pocket ID's own administrator flag.
* **`apply --site <name>`**, which is one site's install, not the deployment's.
* **An admin outside the list.** `paisans app admin create` is unchanged and
  still makes any other administrator.

## Who is made

Each entry under `admins` is one Pocket ID user:

| Pocket ID field | From |
|---|---|
| `username` | the entry's key, Eg: `joe` |
| `email` | `email`, marked verified |
| `firstName`, `lastName` | `name` split at its first space; without `name`, the key |
| `displayName` | `name`, or the key |
| `isAdmin` | true |
| groups | every app's admin group, as `app admin create` puts them |

The email is marked verified for the reason `app admin create` gives: the
operator who wrote the address into the configuration vouches for it, and the
paisans Mbin fork refuses an OIDC sign in that matches an existing local
account by an unverified address.

**The key must be a valid Pocket ID username.** Pocket ID takes letters,
digits, `_`, `.`, `-` and `@`, starting and ending with a letter or digit
(`dto/validations.go:21-25` at v2.14.0). An admin name is already limited to
lowercase letters, digits, `.`, `_` and `-`, so the one extra rule is the
first and last character. `validate` refuses a name that breaks it when the
deployment runs a Pocket ID app (`pocket-id-admin-username`), so the refusal comes before any host is
reached rather than as a 400 halfway through a run.

**An existing account is found by username, then by email.** Pocket ID lets a
user change their own username and email by default
(`ALLOW_OWN_ACCOUNT_EDIT`), so an admin who renamed themselves after signing
in is still the account with their address. Only an account found by neither
is created. An account that is found is brought up to the table as
`app admin create` does: verified if it is not, made an administrator if it
is not, and put in the admin groups it is missing. A disabled account is
refused, with the admin named, as `app admin create` refuses it.

## Who gets a link

**An admin who has registered a passkey gets nothing.** That is the sign the
account was taken up: the link exists only so a person can sign in once and
add one. Pocket ID records it as a `PASSKEY_ADDED` audit event for the user
(`webauthn/service.go:204`), and the toolkit reads it with
`GET /api/audit-logs/all?filters[userID]=<id>&filters[event]=PASSKEY_ADDED`
(`controller/audit_log_controller.go:24`; both fields are `filterable` in
`model/audit_log.go:10-22`). No admin API lists another user's passkeys, so
the audit log is the one read that answers it. The read is read only.

**Every other admin gets a new link on every run.** It is a one-time access
token, `POST /api/users/:id/one-time-access-token` with `ttl` `24h`, opened at
`<Pocket ID URL>/lc/<token>`. The 24 hour token is 12 characters, which the
login page accepts whether or not email login is on (see `loginTTL` in
`internal/appadmin`). Pocket ID keeps no way to read a token back once
issued, so a run cannot reuse an earlier link; it issues a new one. **An
earlier link stays valid** until it is used once or its 24 hours run out,
whichever comes first (`onetimeaccess/service.go`, `StoreToken`): issuing a
token does not revoke the others.

## Email

**Pocket ID sends it, when the app can send mail**: its smtp settings resolve
a host and a sender, the two Pocket ID's admin page asks for before it turns
any email on. Then `email_one_time_access_as_admin` renders `true` unless the app's
settings set it, which lets the API send a login link to a user
(`onetimeaccess/service.go:56-61`; the setting is off by default,
`appconfig/model.go:145`). An operator who sets it `false` gets printed links
only, and the run says why no email was sent, as it does when the app cannot
send mail.

For each admin who gets a link, apply then calls
`POST /api/users/:id/one-time-access-email` with `ttl` `24h`. Pocket ID makes
a token of its own for the email and never returns it, so **the emailed link
and the printed link are two different links**, each valid for 24 hours and
one sign in.

**Pocket ID answers before it sends.** The call returns 204 and the email goes
out in the background (`onetimeaccess/service.go:105-133`). A failed send is
logged as `Failed to send one-time access token email` with the address and
the error; a successful one logs nothing. So apply reads the Pocket ID
container's log from just before the call, over the same ssh as every step,
for up to 30 seconds, and reports:

* `failed: <error>` when that line appears for the admin's address, with
  Pocket ID's own error;
* `sent` when no such line has appeared after 30 seconds.

`sent` therefore means Pocket ID did not report a failure. It cannot mean the
mail reached the inbox, and the output does not claim that. The admin without
an email address gets no email; the configuration requires one, so this is
an account Pocket ID already held.

## Output

A `pocket id` section, one line per admin per Pocket ID app, replacing the
current "has no admin yet" note, then one line per emailed admin once the log
has been read:

```
pocket id
  ✓ joe on auth                 registered
  ✓ fern on auth                created, link issued
  ✓ sam on auth                 link issued
  ✓ email fern a login link     sent
  ✗ email sam a login link      failed: dial tcp: i/o timeout
```

A deployment that declares no admins keeps the note.

After the run's result, whatever it is, the links, one per admin, under a
line saying each is valid for 24 hours and once:

```
Login links, each valid for 24 hours and for one sign in. Treat each as a password:
  fern  https://id.example.org/lc/ABCDEFGHJKLM
  sam   https://id.example.org/lc/NPQRSTUVWXYZ
```

The links are credentials. They go to standard output once and nowhere else:
not into an error, not into the deployment record, not into a log file. They
are printed whether output is a terminal or a pipe, because a pipe is how an
operator copies them.

A failed email does not fail the run: the printed link is there. A Pocket ID
that cannot be reached, or a create that fails, does fail it, naming the
admin, since the deployment cannot be signed into; every other admin is tried
first, and the links already issued are printed.

**The dry run** probes read only and shows the plan, one `○` line per admin
with its steps: `create administrator fern`, `issue login link for fern`,
`email login link to fern`. It issues and sends nothing.

## Authority

These are Pocket ID user writes, which Agent Instructions puts behind per-use
approval. The founder decided on 2026-10-11 that **declaring an admin in
`paisans.yaml` is the approval** for apply to make that admin's account, bring
it up to the table above, issue them a login link and have Pocket ID email it,
in the way the 2026-10-08 standing exception treats a declared app's OIDC
client and signup groups. Every other user write, and every one of these done
by hand, stays in its tier. `docs/deployment-agent-rules.md` carries the same
exception in generic words.

## Tests

* An admin with no account: create, groups, link, email; the create body is
  the table above.
* An admin with an account found by email under another username: no create,
  no second account.
* An admin with a `PASSKEY_ADDED` event: no token call, no email call.
* An admin without one, on a second run: a second token call.
* No SMTP host or sender, or the setting `false`: no email call, and a note
  says why.
* The log scan: a matching failure line gives `failed: <error>`; another
  address's failure line does not; none gives `sent`.
* A token never appears in an error, a note or the recorder's non-link output.
* `validate` refuses `joe-` with a Pocket ID app and accepts it without one.
* `render`: `EMAIL_ONE_TIME_ACCESS_AS_ADMIN_ENABLED=true` with a host, a
  sender and the setting unset; as set when it is set; absent without a host
  or a sender.
