# `paisans secrets rotate`

Date: 2026-10-12. Status: approved by the founder in session, 2026-10-12, with
the decisions under *Settled*. The third piece of the work
that begins with docs/specs/2026-10-11-config-review.md and
docs/specs/2026-10-11-site-evict.md: after an admin is evicted, or leaves
having been able to read `secrets.enc.yaml`, every secret they could read is
replaced, where it is checked and where it is used, without a person editing a
file on a host.

Today nothing rotates a generated secret. `init` creates each one once and
never replaces it, because replacing a value in the file alone breaks every
service that still checks the old one (README, *Three kinds of secret*). Two
rotations exist, each for one secret: `oidc client create --rotate-secret` and
`storage rotate-key --app`. WireGuard keys are `mesh rekey`'s
(docs/specs/2026-10-11-site-evict.md).

## Scope

In: every generated secret that can be replaced without losing data, the
pasted secrets once a human has replaced them at their issuer, and signing
members out of the apps whose sessions a secret signs.

Out, each for a stated reason:

* **WireGuard keys.** `mesh rekey`, which needs the mesh's own staging.
* **Secrets that encrypt stored data**: Pocket ID's `encryption_key` and
  Matrix Authentication Service's `mas_encryption_secret`. Changing either
  makes what they encrypted unreadable (the comments in
  `internal/secretsgen/secretsgen.go` cite each project), and neither project
  offers a re-encryption the toolkit could drive. An evicted admin who held one
  also holds a copy of the data it protects, so rotating it would not take
  anything back. The plan lists them as kept, with this reason.
* **Reissuing a pasted secret.** The DNS token and SMTP passwords are issued by
  a provider. A human replaces them there and with `secrets set`; `secrets
  rotate` then makes sure every host uses the new value.

## The operator's flow

```
paisans secrets rotate --all                    # dry run: every secret, in order, with what each costs
paisans secrets rotate --all --execute
paisans secrets rotate cluster.standby_password --execute
```

A secret is named by its path in the secrets file. `--all` is every secret in
scope, in the order below. A run that stops part way resumes: re-running plans
only what is left.

## How one secret is rotated

| Stage | Work | Gate |
|---|---|---|
| 1. Next value | a new value generated on the workstation, written to the secrets file as `<path>.next` beside the current one | the file decrypts and holds it |
| 2. Where it is checked | the side that verifies the secret learns the new value: `ALTER ROLE` for a Postgres role, Garage's configuration for its secrets, the app's own environment for a secret it checks itself | a check made with the new value succeeds |
| 3. Where it is used | every stack that presents the secret, by scoped `apply`, one site at a time | each stack healthy before the next site |
| 4. Promote | `.next` becomes the value; the old value is deleted | the file decrypts and holds only the new value |

**The next value lives in the file from stage 1**, so a run stopped between
stage 2 and stage 3, after Postgres already holds the new password, still has
it. A resumed run reads which value the checking side accepts and continues
from there. The file is re-encrypted under its current recipients at every
write; after an eviction that is the new data key, so a next value is never
readable by the evicted admin.

**Some services read a secret only when they first initialise.** The
Postgres roles Spilo creates at bootstrap, a pinned app's own Postgres, and
RabbitMQ's default user keep the old value however often the file is rendered
again. For those, stage 2 changes the secret inside the service (`ALTER ROLE`,
`rabbitmqctl change_password`) before any file is rendered.

**A secret checked in one place and used in one place** has a short window
between stages 2 and 3 when the user still presents the old value: a reconnect
fails and is retried. **A secret every node must share**, Garage's
`rpc_secret`, cannot be held in two values at once, so every Garage node is
restarted with the new one together, and storage is unavailable for that
restart. The plan says which kind each secret is.

## What each secret costs

| Secret | Checked by | Used by | Cost to members |
|---|---|---|---|
| `external.acme_dns_token` (after `secrets set`) | the provider | the gateway's Caddy | none |
| `apps.<app>` OIDC client secret | Pocket ID | the app | none; `oidc client create --rotate-secret`, run in order |
| Mbin `oauth_passphrase`, `oauth_encryption_key` and keypair | Mbin | Mbin | every API client and app signs in again |
| Mbin `app_secret`, oauth2-proxy `cookie_secret`, uptime `session_secret` | the app | the app | signed in browser sessions of that app end (to verify per app in the implementation) |
| MAS `mas_signing_key`, `mas_matrix_secret` | MAS, the homeserver | MAS, the homeserver | Matrix sessions end (to verify) |
| `apps.<app>.s3_*` | Garage | the app | none; `storage rotate-key --app`, run in order |
| Pocket ID `static_api_key`, uptime `admin_password` | the app | the toolkit, the operator | none |
| `cluster.*_password`, `apps.<app>.database_password` | Postgres | Patroni, HAProxy, the app | reconnects; the leader's `patroni.env` changes with a switchover, as `site add` does |
| Mbin `rabbitmq_password`, `valkey_password`, `mercure_jwt_secret` | the broker, the cache, the hub | Mbin | a restart of the Mbin stack |
| `storage.garage.admin_token` | each Garage node | nothing yet: the toolkit drives Garage through its command line inside the container | none |
| `storage.garage.rpc_secret` | every Garage node | every Garage node | storage unavailable for one restart |
| `sites.<site>.heartbeat_token` | the monitor | the site's watchdog | none |

`--all` runs them in this order: what works from the internet first, the DNS
token, client secrets and signing keys, then what only the mesh reaches.

**A rotation that signs members out asks first.** When the plan includes one,
`--execute` asks the operator to type `sign out` at a terminal, the same as the
other typed confirmations; without a terminal it is refused. After an eviction
of a gateway, which saw every session cookie, signing everyone out is the
point.

## Refusals

| Refused | Because |
|---|---|
| etcd or Patroni unhealthy | a password change on a limping cluster can leave a member unable to rejoin |
| a site that does not answer over ssh | it would keep the old value, and break when the old one is retired |
| a pasted secret whose value in the file has not changed since the hosts were last applied | rotating it would restart services for nothing; replace it at the issuer and with `secrets set` first |
| a `.next` value whose stage cannot be read from the hosts | a person has to look before anything else changes |

## Settled

Founder decisions, 2026-10-12, in session:

1. **Scope.** Every generated secret that can be replaced without losing data,
   and the pasted secrets once replaced at their issuer. Pocket ID's
   `encryption_key` and Matrix Authentication Service's `mas_encryption_secret`
   are kept, and the plan says why.
2. **Signing members out.** `--all` includes the rotations that sign members
   out, and a run that includes one asks the operator to type `sign out` at a
   terminal first.
3. **Garage's `rpc_secret`.** `--all` includes it: every Garage node restarts
   with the new value together, and the plan says storage is unavailable for
   that restart.
