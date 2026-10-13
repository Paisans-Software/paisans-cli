# DNS admin continuity

Status: design approved in session on 2026-10-13, not implemented. The
Cloudflare response shapes below were read from Cloudflare's API reference and
observed against a real free account on 2026-10-13 (*Observed*).

The admin reconciler (`2026-10-08-admin-reconciler.md`) reports when a
community is one lost administrator away from nobody being able to run its
identity provider. The DNS zone has the same failure, one level further out.
Whoever controls the zone controls every hostname the deployment serves, and an
account whose last member with the power to add members has gone can only be
recovered at the registrar, by moving the domain to a new account. This spec
makes the reconciler check the DNS provider's account against Pocket ID's
administrators, and report the gap to the health page before it is a loss.

Cloudflare is the one provider covered, because it is the one whose account
has members an API can list. A deployment on another provider renders none of
this.

## The rule

**Every enabled Pocket ID administrator is an accepted Super Administrator of
the Cloudflare account that holds the zone, and there are at least two.**

* **Super Administrator, not Administrator.** Cloudflare's Administrator role
  "Can access the full account and edit subscriptions. Cannot manage members
  nor billing profile." Super Administrator "Can edit any Cloudflare setting,
  make purchases, update billing, manage members"
  ([Cloudflare: Roles](https://developers.cloudflare.com/fundamentals/manage-members/roles/),
  read 2026-10-13). An account whose Super Administrators have all gone keeps
  working, and its Administrators keep editing records, but nobody can add the
  next administrator, so every departure shrinks the set for good. An
  administrator holding any role but Super Administrator fails the rule, even
  with access to the zone. Founder decision.
* **Every Pocket ID administrator, declared or not.** `paisans-stack.yaml`'s
  `admins` are made Pocket ID administrators by `apply`; anyone else who holds
  that flag holds the same power over the community and is held to the same
  rule.
* **At least two.** The reconciler's own threshold, for the same reason: one
  is a single loss away from none. It is restated here so that the DNS check is
  correct on its own, though the `admins` group check fails first in practice.
* **Extra members are allowed.** A Cloudflare member who matches no Pocket ID
  administrator is not reported, whatever their role. Founder decision.
* **Pending is not accepted.** An invitation not yet accepted grants nothing,
  so it fails the rule. The body counts it separately, so "invited, not yet
  accepted" reads differently from "never invited".
* **Two-factor authentication is a separate check.** A Super Administrator
  without it is one phished password from losing the zone, which is a
  continuity risk of its own, but a different repair, so a different monitor.

## Matching an administrator to a member

An administrator's **expected address** is their mapped address when the
secrets file maps one, and their Pocket ID email otherwise. A member matches
when their `user.email` equals it, compared lowercased with surrounding space
trimmed and nothing else normalised: folding `+tag` or dots would match two
different people. A Pocket ID administrator with no email and no mapping has
no expected address, and fails the rule.

### The mapping

Some admins use a different address at Cloudflare. The secrets file maps them,
by the admin's key in `paisans-stack.yaml`:

```yaml
admins:
  alice:
    dns_provider_email: alice.work@example.org
```

Unmapped admins need nothing. Matching is one to one: `render` refuses two
admins mapped to the same address, or a mapped address equal to another
admin's `email`, because either lets one Cloudflare membership satisfy two
administrators. It also refuses a key that is not an admin `paisans-stack.yaml`
declares, naming admins by key and never the address. Founder decision.
`validate` reads no secrets, so the refusal is `render`'s, where a malformed
heartbeat token's already is.

**Why the secrets file, and not `paisans-stack.yaml`.** The configuration is
copied in plaintext to every site (`2026-10-12-config-store.md`); the secrets
file is copied too, but encrypted to the admins' age keys, which no host ever
holds. A second address is personal data the hosts have no need to read, so it
stays where only a workstation can.

**Why not a Pocket ID custom claim.** A claim is carried to the apps that sign
in through Pocket ID, which would hand every app an address only the
reconciler needs. Founder decision.

### Keyed hashes on the host

The reconciler needs to test membership, not to know an address, so no
Cloudflare address is rendered to a host. `init` generates
`apps.<app>.dns_email_hash_key`, 32 random bytes as 64 lowercase hex
characters, and `apply` renders each mapping as
`HMAC-SHA256(key, pocket id email) = HMAC-SHA256(key, mapped address)`, both
sides lowercased and trimmed first, into the Pocket ID stack's `.env` beside
the key. The reconciler hashes each Pocket ID administrator's email and each
Cloudflare member's email with the same key and compares hashes.

What this protects, and what it does not:

* **Protected:** every copy at rest. The rendered `.env`, a host backup and the
  config store's copies hold no Cloudflare address, and a hash cannot be
  confirmed against a guessed address without the key.
* **Not protected:** a host taken over while the reconciler runs. The key is in
  the same `.env`, and the token below can list the account's members, so root
  on an apps site can read every member's address from Cloudflare directly.
  That is the token's reach, and the reason it is read only.

## The token

A new pasted secret, `external.dns_members_token`: a Cloudflare API token
with **Account Settings: Read** on the account and **Zone: Read** on the zone,
and nothing else. Cloudflare lists Account Settings Read as sufficient for the
members list
([Cloudflare API: List Members](https://developers.cloudflare.com/api/resources/accounts/subresources/members/methods/list/),
read 2026-10-13); Zone Read is for finding the account from the zone.

It is not `external.acme_dns_token`. That token edits records, and lives in
Caddy on the gateway and on the operator's workstation. The reconciler runs
in the Pocket ID stack on every apps site, and a token that can edit the zone
there would turn a leak from any apps site into control of every hostname.
Founder decision.

**Optional.** With the token unset, or `acme.provider` other than
`cloudflare`, nothing in this spec is rendered: no key, no mapping, no
monitors, and the reconciler makes no Cloudflare call. A deployment that adds
the token later gets the monitors on its next `apply`.

## What the reconciler does

The DNS check runs inside the reconciler's existing pass, after the `admins`
group step and on its schedule: every two hours, once at start, on the active
instance only. A standby does nothing, as now.

1. **Account.** `GET /zones?name=<suffix>` with the members token, for each
   suffix of `community.domain`, longest first, until a zone the token can
   see answers: the way `internal/dns` finds a zone, because the domain need
   not be a zone apex. The domain is rendered as `PAISANS_DNS_DOMAIN`, and the
   zone's `account.id` is the account. Found every pass, never configured, so
   a zone moved between accounts is followed.
2. **Members.** `GET /accounts/{account_id}/members`, `per_page=50` (the
   maximum), paged with `page` up to `result_info.total_pages`, as the DNS
   client pages records. The reference lists `count`, `page`, `per_page` and
   `total_count` only; a real response carries `total_pages` too
   (*Observed*). A read that fails on any page fails the check, which then
   reports `cloudflare unreadable`.
3. **Classify.** For each member: `status` (`accepted` or `pending`),
   `user.email`, `user.two_factor_authentication_enabled`, and whether it is a
   Super Administrator. A member is a Super Administrator when a role in
   `roles` is named `Super Administrator - All Privileges`, or a `policies`
   entry with `access: allow` grants a permission group of that name over a
   resource group scoped to the account (`com.cloudflare.api.account.<id>`).
   A real member carries both (*Observed*); either alone is accepted, so an
   account that drops one shape keeps passing.
4. **Count**, over the enabled Pocket ID administrators the `admins` step
   already read, the synthetic API key user excluded:
   * `super admins`: how many have an accepted Super Administrator membership
     at their expected address, and of those failing, how many are pending.
   * `2fa`: of those matched, how many have two-factor authentication off.

The reconciler makes no write at Cloudflare. Its only requests there are the
two `GET`s above, and a test fails on any other.

### What it reports

Two more paths beside `/healthz`, same contract: the last pass's result, plain
text, one line, 200 or 503, `standby` on a standby, `starting` before the first
pass, `stale` after two and a half hours.

| Path | 200 when | 503 body |
|---|---|---|
| `/healthz/dns-super-admins` | every administrator matched, and at least two | `1 of 3 admins not cloudflare super admin (1 pending)` |
| `/healthz/dns-2fa` | every matched member has two-factor on | `1 of 3 admins have 2fa off` |

When Cloudflare cannot be read, both answer 503 with
`cloudflare unreadable: <status>`, the HTTP status or `no answer`, never
the response body or the token. Not seeing the account is itself a risk, and
two red monitors with the same reason say where to look.

**Bodies are counts.** They name nobody, by username, email or mask. Founder
decision: the monitors exist so that admins and members with the health page
know the community is at risk; who is missing is worked out afterwards, from
the container's log, which names each failing administrator by Pocket ID
username. The log never prints a Cloudflare address.

## Monitors

Two heartbeat monitors per Pocket ID app, seeded beside the admins heartbeat
with its fields (`interval_seconds: 60`, `heartbeat_grace_seconds: 120`,
`failure_threshold: 2`):

| Monitor | Token | Fed from |
|---|---|---|
| `admin continuity: dns super admins` | `apps.<app>.dns_super_admins_heartbeat_token` | `/healthz/dns-super-admins` |
| `admin continuity: dns 2fa` | `apps.<app>.dns_2fa_heartbeat_token` | `/healthz/dns-2fa` |

**The continuity monitors are named for the community, not the app.** The
admins heartbeat is renamed from `<app> — admin continuity` to
`admin continuity`, and the two above sit beside it under the same prefix.
Every instance of a Pocket ID app already pushes to one token, so a deployment
has one of each whatever its sites. A deployment that declares more than one
`pocket-id` app prefixes each name with the app key (`auth: admin
continuity`), because the seed tells monitors apart by name. Founder
direction.

The rename is a new monitor to the seed: on the next `apply` the old managed
monitor is deleted with its history and channel links, and the new one is
attached to every channel flagged to attach to new managed monitors.

The tokens are generated at `init`, and by `apply`'s dry run, in the shape the
seed accepts, as `admins_heartbeat_token` is.

The `admins-heartbeat` pusher already asks the reconciler once a minute and
pushes to every monitor. It asks the two new paths the same way, with the same
table (`standby` pushes nothing, other 200 pushes success, 503 pushes a failure
with the body, no answer pushes nothing), to the URLs in two more variables in
its 0600 `heartbeat.env`. One pusher, three paths: no new service, image or
port.

## Approval

The reconciler is approved once, as a whole, for two writes to Pocket ID
(`docs/deployment-agent-rules.md`, *Services the deployment runs*). This adds
no write anywhere; it adds reads at Cloudflare and a credential that can read
the account's members. Adding a credential to a service on every apps site is
a human decision, approved with this design on 2026-10-13. The reconciler gives
an agent no authority to change Cloudflare membership, and the monitors are
the whole of what it does with what it reads.

## Tests

* **Classify** against a fake Cloudflare (`httptest`): an accepted Super
  Administrator by role and by policy matches; an Administrator, a pending
  Super Administrator and a policy with `access: deny` do not; a mapped
  administrator matches at the mapped address and not at the Pocket ID one;
  an administrator with no email and no mapping fails; an extra member is not
  counted; a single matched Super Administrator fails the floor; two-factor off
  is counted on matched members only.
* **Paging**: 51 members across two pages are all read; a second page that
  fails gives `cloudflare unreadable` on both paths.
* **Requests**: every request the check makes is recorded, and the test fails
  on any method but `GET` or any path but the two above.
* **Privacy**: no body and no log line contains a member's address, the
  mapped address or the token, asserted over every case above.
* **Hashing**: case and surrounding space are folded on both sides; `+tag` is
  not.
* **Mapping refusals**, at render: two admins mapped to one address, a mapping equal to another
  admin's `email`, and a key that is no declared admin are each refused.
* **Render**: without the token, the golden tree is unchanged. With it, the
  Pocket ID `.env` gains the token, the domain, the key and the hashed map; the seed gains
  the two monitors; the pusher's env gains the two URL variables; and the
  pusher's script runs its new branches under `sh` against a fake curl.

## Observed

`GET /accounts/{id}/members?per_page=50` against a free account with two
members, 2026-10-13, values withheld:

* `result_info`: `page`, `per_page`, `total_pages`, `count`, `total_count`.
* Each member: `api_access_enabled`, `email`, `id`, `policies`, `roles`,
  `status`, `user`; `user` holds `email`, `first_name`, `id`, `last_name`,
  `two_factor_authentication_enabled`.
* Both members `accepted`, each with role `Super Administrator - All
  Privileges` and one policy, `access: allow`, permission group of the same
  name, resource scope key beginning `com.cloudflare.api.account`.

## Open

* **The registrar.** Whoever controls the domain at the registrar is the last
  recovery for the zone and is not checked. Registrar APIs differ by
  registrar; it is out of scope here.
