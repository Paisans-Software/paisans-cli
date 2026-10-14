# The account service: changing a member's groups

Status: approved by the founder on 2026-10-14, not implemented. Builds on
`docs/specs/2026-10-14-account-signup-bans-recovery.md`.

Under `management: paisans` nobody is a Pocket ID administrator, so a
member's groups change only through the account service. A group is what
grants privilege: `admins` makes a community admin of every app that reads
it, an app's member group admits a member to it. Promotion and demotion are
the same act: adding a member to a group, or taking them out of one.

## What it must do

* **Any admin changes any group of any user**, themselves and other admins
  included, except `operators` (below). Founder decision: there is one tier of
  admin for now.
* **`operators` is the yaml's.** It is shown on the page and never changed
  here: an operator is added or removed by editing `paisans-stack.yaml`, and
  the operator reconciler fails on any member of `operators` the yaml does not
  name.
* **An operator's other groups can be changed**, with a note on the page that
  the next `apply` puts an operator back in `members`, `operators` and every
  app's admin and member group.
* **A change takes effect at the next sign-in.** An app session already open
  keeps the privileges it was given until it expires, as for a ban
  (Paisans-Software/paisans-account-service#4). The confirmation page says so.
* **Every change is in the audit log**: who, whose groups, each group added
  and each removed, and when.

## The page

`/admin/members` (from the bans spec) gains a groups page per user,
`/admin/members/{id}/groups`: every group Pocket ID has, by name and display
name, with the user's current ones marked, and `operators` shown but not
editable. The admin changes the marks and saves; the save names the groups to
add and the groups to remove, not the whole set.

## The write

Pocket ID replaces a user's whole group list in one call,
`PUT /api/users/:id/user-groups` with `{"userGroupIds": [...]}`. The service:

1. reads the user again, `GET /api/users/:id`, immediately before writing;
2. computes the new list as the groups that read found, plus those the admin
   added, minus those the admin removed;
3. refuses the write if that list differs from what was read in `operators`
   membership;
4. sends it, then reads the user once more and records the audit entry with
   the groups actually added and removed.

A group the admin marked that someone else changed in between is applied as
the admin asked; every other group is kept as the fresh read found it, so two
admins working at once never undo each other's unrelated changes.

The service's write-surface test gains this route. It fails if the body is not
the freshly read list with exactly the requested additions and removals, or if
`operators` membership changes.

## Testing

* Add, remove, both at once; self; another admin; an operator's `admins`
  removed, with the note shown.
* `operators` cannot be added or removed, by form tampering included.
* Concurrent change: a group changed between page load and save by someone
  else is kept.
* The audit entry lists what was actually added and removed.
* A contract test against real Pocket ID: the write changes the groups named
  and no other field of the user.

## Documentation

The toolkit's README section on the account service describes group changes,
and `docs/deployment-agent-rules.md` names the account service among the
services the deployment runs, with this write among its approved writes.
