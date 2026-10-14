# When an operator leaves

An operator of a paisans deployment holds three kinds of access: an ssh key on
the sites that name them; an account at Pocket ID in the group `operators` and
in every app's admin group, which makes them a community admin as well, and
under `management: pocket-id` a Pocket ID administrator; and, when their age
key is a recipient of `secrets.enc.yaml`, every secret of every site. Each is
closed a different way, and some cannot be closed at all, only made worthless
by rotating what they unlock. This guide is the order to close them in, for
two cases: an operator who leaves on good terms, and an operator whose site is
evicted because the others can no longer reach it.

Each step says what it closes and what is still open after it. Why each
protection works, and the scenarios behind them, are in
[How a deployment protects itself](how-a-deployment-protects-itself.md). The examples
use `alice` for the operator who leaves, `home-b` for a site, and `vm` for the
gateway. The reasoning is in `README.md`, under *Keys belong to operators, and
sites name operators*, *The operator reconciler checks Pocket ID against the
operators*, *Evicting a site: `--evict`*, *Rekeying the mesh: `mesh
rekey`* and *`secrets rotate` replaces a secret where it is checked and where
it is used*.

Both cases end the same way: the secrets file re-encrypted without them, then
the mesh rekeyed, then every other secret rotated with `paisans secrets
rotate --all`. The order is deliberate. Re-encrypting first keeps every new
value from them; rekeying next takes the mesh away from them, so the secrets
only the mesh reaches are out of their reach while they are replaced; and
`secrets rotate` refuses while a rekey is unfinished anyway.

## The `sops` commands here

Two `sops` commands appear below, checked against sops v3.13.3, the version
this toolkit embeds (`cmd/sops/main.go` in that release):

* `sops rotate --in-place --rm-age <recipient> secrets.enc.yaml` generates a
  new data key, re-encrypts every value with it, and takes the age recipient
  out of the file's own list of keys. `--in-place` writes the result back to
  the file instead of stdout.
* `sops updatekeys` is not used here. It wraps the same data key for a changed
  set of recipients, so anyone who could read the file before can still read
  every value written to it after.

`sops` needs the age key to decrypt. `init` prints the `SOPS_AGE_KEY_CMD`
line that gives it the key from the workstation's keystore; set it in the
shell first.

**`.sops.yaml` is edited as well as the file.** `--rm-age` changes the file's
own key list, but every paisans command that writes the secrets file
encrypts to the recipients `.sops.yaml` names. A recipient left there is
added back at the next `paisans secrets set`.

**Nothing re-encryption does reaches an old copy.** Every copy of
`secrets.enc.yaml` the operator could decrypt, including every version in the
repository's history, still opens with their key, and holds every value as it
was then. Re-encrypting closes the values written from now on; only rotating a
secret makes their copy of it worthless.

## Every version is signed, and a removal is final

`paisans apply --execute` publishes the files it applies to every site as a
signed version, and `paisans config publish` does the same without applying.
The version that drops the operator from `operators` is the one that removes them:
from then on a version signed by any key of theirs is not trusted, so they
cannot publish one that puts them back (README, *Trust starts from the
operator's own signature, never from the sites*). Until then their signature
still counts.

**Each remaining operator runs `paisans operator register` first**, on a workstation
that has never published or registered, before the first step of either case
below:

```sh
paisans operator register             # which key, and the version it would sign
paisans operator register --execute
```

A workstation with neither its own signature nor a pinned root trusts nothing
the sites hold, so `apply` and `config pull` refuse there. Registering signs the
version the operator already holds, so trust starts from their own key and not from
any history the leaving operator could have written. It signs only a version equal
to the local files, which is why it comes before the edit in step 1.

## An operator who leaves on good terms

The operator hands over, and the others trust that they keep nothing and use
nothing they kept. How much to rotate depends on that trust, and is the
community's decision.

1. **Take them out of `paisans-stack.yaml`.** Delete their entry under `operators`,
   and their name from every site's `ssh.keys`. Taking the operator out of
   `operators` changes nothing at Pocket ID, since `apply` never deletes,
   disables or demotes an account; steps 3 and 4 do the rest.
   Nothing is published yet. Closes nothing yet. Open: everything.

2. **If they were a recipient of the secrets file, re-encrypt it without
   them, before anything publishes.** Delete their age public key from
   `.sops.yaml`, then:

   ```sh
   sops rotate --in-place --rm-age age1... secrets.enc.yaml
   ```

   Commit both files. This comes before any `apply --execute` or `config
   publish`, since each publishes the files to every site as a version, and
   the one that drops them from `operators` would otherwise carry a secrets file
   still encrypted to them. Until this runs they hold the file's data key, so
   every value written to it, and every version published from it, opens with
   what they already have. Closes: every value written to the file from now
   on. Open: every value they could decrypt until now, in this file and in
   every older copy.

3. **Prepare every site that named them.**

   ```sh
   paisans host prepare --site home-a           # shows the keys it would delete
   paisans host prepare --site home-a --execute
   ```

   One site at a time, or `paisans apply`, which prepares every site and,
   with `--execute`, publishes the version that removes them: from then on
   their signature stops counting.
   Closes: ssh with every key `host prepare` added for them. Open: a key of
   theirs that was in `authorized_keys` before `host prepare` first ran is
   never recorded and never removed, because nothing on the host says
   whether it is the only way in (README, *host prepare manages the login
   user's authorized keys*). Read `~<user>/.ssh/authorized_keys` on each
   host, and delete such a line by hand. An account of their own on a host,
   or a key on another user, is outside the toolkit too.

4. **Take away what their Pocket ID account grants.** Their account is still
   in `operators`, which the health page reads for its administrators by
   default, and in
   every app's admin group, which makes them a community admin. Take them out
   of `operators`, and out of the admin groups unless the community keeps
   them as a community admin; disable the account if they should not sign in
   at all. The operator reconciler only reads, so nobody but you makes this
   change.
   Under `management: pocket-id` it is Pocket ID's admin pages, where the
   admin flag comes off their account as well: until it does, the operator
   reconciler's health check fails at every pass, naming them as a Pocket ID
   administrator who is not an operator. Under `management: paisans` nobody
   is a Pocket ID administrator, so no admin page reaches the account and
   nothing reports it: the change is a call to Pocket ID's API with the
   deployment's static API key, `PUT /api/users/<id>/user-groups` with every
   group they keep, and a call to it is a change to a user, approved as one.
   Closes: administration of Pocket ID where they held it, of the health
   page, and of every app whose admin group they left. Open: sessions they already hold in each app,
   until those expire or step 5 ends them.

5. **Rotate what the trust calls for.** On full trust, nothing more. Short of
   it, in this order:

   1. Replace the pasted secrets at their issuers, and set each new one. The
      DNS token first, since it can point the community's names anywhere:
      revoke it at the provider, issue a new one scoped to the zone, and pipe
      it in. Then each SMTP password the deployment sends mail with, at the
      mail provider. Every OIDC client secret is minted by `secrets rotate`
      itself, so none is replaced here:

      ```sh
      security find-generic-password -s acme -w | paisans secrets set external.acme_dns_token
      security find-generic-password -s smtp -w | paisans secrets set external.smtp_password
      ```

      `secrets rotate` hands each new value to the hosts in step 3. If an
      `apply` hands one out first, name it there with `--replaced <path>`,
      since rotate refuses a pasted secret every host already runs unless it
      is told the secret was replaced.

   2. Every site's WireGuard key: `paisans mesh rekey --all --execute`, one
      site at a time. `apply` refuses until it finishes, and a run that stops
      resumes when run again.

   3. Everything else:

      ```sh
      paisans secrets rotate --all             # every secret in order, its cost, and what is kept
      paisans secrets rotate --all --execute   # from a terminal
      ```

      One secret at a time, what works from the internet first: the pasted
      secrets set above, the OIDC client secrets, Mbin's OAuth2 keys and the
      apps' session secrets, the S3 keys, then the Postgres, RabbitMQ, Valkey,
      Mercure, Garage and heartbeat secrets. It asks for `sign out` at the
      terminal, since some of these end members' sessions; the plan says
      which. A run that stops resumes when run again, and `apply`, like
      every command that renders, refuses until it finishes. A plan that
      says it is resuming an earlier `--all` run that was not this one
      would leave the secrets that run finished as the departed operator knew
      them: run `paisans secrets rotate --all --restart` instead, which
      rotates every secret again. A rotation that cannot finish is ended by
      hand: README, *A rotation that cannot finish*.

   Closes: every secret they could read, except the ones the plan lists as
   kept. Open: Pocket ID's `encryption_key` and Matrix Authentication
   Service's `mas_encryption_secret`, which encrypt stored data and cannot be
   changed without losing it; each is useful only to someone who also holds
   the data it encrypts. Wiki.js's `local_admin_password`, which `secrets
   rotate` keeps because Wiki.js holds it hashed in its own database and the
   bootstrap signs in with it; on a gated wiki it opens nothing to someone
   Pocket ID no longer admits, since the gate stands in front of the sign-in
   page. And sessions in apps whose sessions no rotated
   secret signs, Pocket ID's own among them, and Mbin's open sessions, until
   they expire.

## An operator whose site is evicted

The operator stopped cooperating, and the others can no longer log in to their
host. It still runs and is still a member of the deployment: possibly a data
site, a Garage node, an apps site, or the gateway. `paisans site remove
--evict` takes it out without ever reaching it, and a replacement declared in
the same edit takes its roles over (README, *Evicting a site: `--evict`*).
`home-b` is the site here; where the gateway differs, the step says so.

The order matters: what can redirect the community's names closes first, the
secrets file is re-encrypted before any new site's secrets are generated, the
mesh is rekeyed after the eviction and before the replacement joins, and
everything else is rotated once the replacement runs: `secrets rotate` hands
each new value to every site the configuration declares, the replacement's
included, and refuses while one does not answer.

1. **When the site holds the gateway, revoke the DNS token at the provider,
   and issue a new one scoped to the zone.** Do not set it yet if step 2
   applies. The evicted host holds a working copy of the old token in its
   Caddy's environment, and can point the community's names back at itself
   until it is revoked. A monitor with `managed_by: paisans` holds the same
   token, so do this for one of those too. Closes: the evicted host's control
   of DNS. Open: the certificates it already holds (step 9).

2. **If they were a recipient of the secrets file, re-encrypt it without
   them, before anything else is written to it.** Delete their age public key
   from `.sops.yaml`, then:

   ```sh
   sops rotate --in-place --rm-age age1... secrets.enc.yaml
   ```

   This comes before the new token is set and before the replacement is
   declared, so that neither the new token nor any secret generated for the
   new site is ever encrypted to them. It comes before the `apply --execute`
   of step 7 for the same reason: that apply publishes the files to every
   site as the version that removes them, and until this runs they hold the
   file's data key, which would open it. Closes: every value written from now
   on. Open: every value they could decrypt until now, including every other
   site's WireGuard private key, until step 6, and every older copy of the
   file.

3. **Set the new DNS token**, for a gateway:

   ```sh
   security find-generic-password -s acme -w | paisans secrets set external.acme_dns_token
   ```

   The eviction checks this token against the provider before it changes
   anything, and refuses while it does not work: the revoked one still in the
   file is the usual reason. Closes nothing new; it is what the DNS stage
   deletes the evicted host's records with.

4. **In one edit of `paisans-stack.yaml`:** take them out of `operators` and every
   site's `ssh.keys`; declare the replacement as a new site, under a new
   name, with the roles `home-b` held; and pin to it any app pinned to
   `home-b`. The new name keeps the old host and the new one apart in the
   secrets, in etcd, Patroni and Garage, in DNS and in every log. Closes
   nothing yet.

5. **Evict the site.**

   ```sh
   paisans site remove home-b --evict             # the plan, what it will ask, what the host keeps
   paisans site remove home-b --evict --execute   # from a terminal
   ```

   It asks for `revoked` when `home-b` holds the gateway, then the site's
   name, then `lose writes` when `home-b` leads and no other member is a
   streaming `Sync Standby` (after showing how far behind the most current
   replica is), and `lose objects` when its Garage node held objects nothing
   else holds. It generates the replacement's secrets first when the file
   lacks them, and writes them encrypted to `.sops.yaml`'s recipients only
   after every answer. Closes: the host's place in the mesh, and with it every
   cluster service, which trusts the mesh and nothing else; its etcd and
   Patroni membership; its Garage node; its DNS records, even at names the
   replacement will answer. Open: everything it was rendered (below), and the
   community's names, which answer nothing until step 7 when `home-b` was the
   only gateway or the only apps site.

6. **Rekey the mesh.**

   ```sh
   paisans mesh rekey --all             # where each site's rekey stands
   paisans mesh rekey --all --execute
   ```

   One site at a time; the replacement is not brought up yet, so it is left
   out. Around the swap of a site others relay through, Eg: the one VM the
   homes reach each other through, Patroni is paused and then resumed, and the
   plan says so. A run that stops resumes when run again, and `apply` refuses
   until it finishes. Closes: every remaining site's
   WireGuard key, which an operator who could read the secrets file holds, and
   which WireGuard accepts from any address that proves it. Skip it only when
   they were never a recipient. Open: nothing on the mesh, once every site is
   done.

7. **Bring up the replacement.** Before it, the new host's owner can bound
   which keys `apply` may ever grant there:

   ```sh
   paisans host allow home-d carol dave           # on the new site, before its first apply
   paisans apply --execute
   ```

   `apply` prepares the new host, applies it and creates its DNS records, adds
   it as a peer on every site, and prepares every site, which deletes from
   each the keys of theirs `host prepare` added. It reviews every key it
   grants on the new host, which has no record yet, and asks for `approve` at
   the terminal (`docs/specs/2026-10-11-config-review.md`; `host allow`
   writes `/etc/paisans/ssh-allow.<user>` there). A replacement data site is
   joined by that apply's `site add`. It also publishes the version that
   takes them out of `operators`, so from then on a version signed by any key of
   theirs is not trusted. Closes: the outage of step 5, their ssh keys on
   every remaining site, and their signature on the configuration. Open: a
   key of theirs on a remaining host that `host prepare` never recorded (see
   step 3 of the first case).

8. **Take away what their Pocket ID account grants**, as in step 4 of the
   first case. Closes: their administration of Pocket ID where they held it,
   of the health page, and of every app whose admin group they left.

9. **Revoke the old gateway's certificates.** The evicted host keeps the
   certificates it obtained for the community's names, with their private
   keys, valid until they expire. Let's Encrypt lets an account other than
   the one that issued a certificate revoke it, once that account proves
   control of the names in it with the same DNS or HTTP challenge used to
   issue one; the certificates are found in the Certificate Transparency
   logs, Eg: at crt.sh, and revoked with `certbot revoke --cert-path
   <downloaded-cert.pem>` (https://letsencrypt.org/docs/revoking/). Caddy may
   obtain certificates from another CA when Let's Encrypt fails, so check
   which CA issued each one, and that CA's own revocation rules. Closes: the
   certificates, for every client that checks revocation. Open: clients that
   do not check.

10. **Rotate the rest.** First replace the pasted secrets at their issuers
    and set each, as in step 5.1 of the first case: each SMTP password.
    When `home-b` did not hold the gateway, the DNS token is the one
    they knew: replace it the same way. Then:

    ```sh
    paisans secrets rotate --all             # every secret in order, its cost, and what is kept
    paisans secrets rotate --all --execute   # from a terminal
    ```

    When `home-b` held the gateway, steps 3 and 7 already replaced the DNS
    token and put it on every host that uses it, and `--all` refuses it as
    unchanged, since it cannot tell that from a token never replaced. Say so:

    ```sh
    paisans secrets rotate --all --replaced external.acme_dns_token
    paisans secrets rotate --all --replaced external.acme_dns_token --execute
    ```

    The plan lists the token as replaced earlier and rotates everything else.

    It asks for `sign out`: after a gateway's eviction, signing members out
    is the point, since the evicted host saw every session cookie. A run
    that stops resumes when run again, and `apply`, like every command that
    renders, refuses until it finishes. When the plan says it is resuming an
    earlier `--all` run that was not this one, add `--restart`, so every
    secret is rotated again. A rotation that cannot finish is ended by hand:
    README, *A rotation that cannot finish*. The WireGuard keys were step 6.

11. **Remove the evicted site's secrets**, now that no site names it:

    ```sh
    paisans secrets prune             # lists them
    paisans secrets prune --execute
    ```

**What stays with the evicted host**, whatever is done here:

* a copy of the database and of the objects, as they were when it was cut;
* everything it saw while it terminated TLS, when it was the gateway:
  members' session cookies, and anything typed into a form;
* every secret it was rendered, until step 10 rotates it. The Postgres and
  Garage ones reach nothing once the mesh is closed; Mbin's keypair signs
  tokens that work from the internet;
* Pocket ID's `encryption_key` and Matrix Authentication Service's
  `mas_encryption_secret`, which `secrets rotate` keeps because they encrypt
  stored data, and which open only what the copy above already holds;
* Wiki.js's `local_admin_password`, which `secrets rotate` keeps because
  Wiki.js holds it hashed in its own database; on a gated wiki the gate
  stands in front of its sign-in page;
* members' sessions in every app, until they expire or step 10 ends them;
  sessions no rotated secret signs, Pocket ID's own among them, and Mbin's
  open sessions, last until they expire;
* when they were a recipient, every secret as it was until step 2, in every
  copy of the file they hold.
