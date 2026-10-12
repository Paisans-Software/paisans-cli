# When an admin leaves

An admin of a paisans deployment holds three kinds of access: an ssh key on
the sites that name them, an administrator's account at Pocket ID, and, when
their age key is a recipient of `secrets.enc.yaml`, every secret of every site.
Each is closed a different way, and some cannot be closed at all, only made
worthless by rotating what they unlock. This guide is the order to close them
in, for two cases: an admin who leaves on good terms, and an admin whose site
is evicted because the others can no longer reach it.

Each step says what it closes and what is still open after it. The examples
use `alice` for the admin who leaves, `home-b` for a site, and `vm` for the
gateway. The reasoning is in `README.md`, under *Keys belong to admins, and
sites name admins*, *The admin reconciler keeps `admins` equal to Pocket ID's
administrators*, *Evicting a site: `--evict`* and *Rekeying the mesh: `mesh
rekey`*.

**`secrets rotate` is not built yet.** It will rotate every shared secret where
it is checked and where it is used together, including signing every member
out, and has its own pull request. Until it lands, the steps below rotate what
existing commands can: the DNS token with `paisans secrets set`, OIDC client
secrets with `paisans oidc client create --rotate-secret`, apps' S3 keys with
`paisans storage rotate-key --app`, and WireGuard keys with `paisans mesh
rekey`. The Postgres, Garage and heartbeat secrets stay as they are, and each
case says what that leaves open.

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
`secrets.enc.yaml` the admin could decrypt, including every version in the
repository's history, still opens with their key, and holds every value as it
was then. Re-encrypting closes the values written from now on; only rotating a
secret makes their copy of it worthless.

## An admin who leaves on good terms

The admin hands over, and the others trust that they keep nothing and use
nothing they kept. How much to rotate depends on that trust, and is the
community's decision.

1. **Take them out of `paisans.yaml`.** Delete their entry under `admins`,
   and their name from every site's `ssh.keys`. Taking the admin out of
   `admins` changes nothing at Pocket ID; steps 2 and 3 do the rest.
   Closes nothing yet. Open: everything.

2. **Prepare every site that named them.**

   ```sh
   paisans host prepare --site home-a           # shows the keys it would delete
   paisans host prepare --site home-a --execute
   ```

   One site at a time, or `paisans apply`, which prepares every site.
   Closes: ssh with every key `host prepare` added for them. Open: a key of
   theirs that was in `authorized_keys` before `host prepare` first ran is
   never recorded and never removed, because nothing on the host says
   whether it is the only way in (README, *host prepare manages the login
   user's authorized keys*). Read `~<user>/.ssh/authorized_keys` on each
   host, and delete such a line by hand. An account of their own on a host,
   or a key on another user, is outside the toolkit too.

3. **Take away their Pocket ID administrator.** In Pocket ID's admin page,
   remove the admin flag from their account, or disable the account if they
   should not sign in at all. The admin reconciler takes them out of the
   `admins` group at its next pass, within two hours, or at once when its
   container restarts, and every app that reads that group stops treating
   them as an administrator. Closes: administration of Pocket ID and of every
   app. Open: sessions they already hold in each app, until those expire or
   `secrets rotate` signs every member out.

4. **If they were a recipient of the secrets file, re-encrypt it without
   them.** Delete their age public key from `.sops.yaml`, then:

   ```sh
   sops rotate --in-place --rm-age age1... secrets.enc.yaml
   ```

   Commit both files. Closes: every value written to the file from now on.
   Open: every value they could decrypt until now, in this file and in
   every older copy.

5. **Rotate what the trust calls for.** On full trust, nothing more. Short of
   it, in this order, credentials that work from the internet first:

   1. The DNS token: revoke it at the provider, issue a new one scoped to the
      zone, and pipe it in, then apply the sites that render it (the gateway,
      and a monitor with `managed_by: paisans`):

      ```sh
      security find-generic-password -s acme -w | paisans secrets set external.acme_dns_token
      paisans apply --execute
      ```

   2. Each app's OIDC client secret, then an apply to render the new one:

      ```sh
      paisans oidc client create --app talk --rotate-secret --execute
      paisans apply --execute
      ```

      A rotation adds a secret and leaves the old one valid, so the app keeps
      signing people in until the apply. The old one stays valid at Pocket ID
      until it is deleted there.

   3. Each app's S3 key: `paisans storage rotate-key --app talk --execute`.
   4. Every site's WireGuard key: `paisans mesh rekey --all --execute`, one
      site at a time. Run no `apply` until it finishes.

   Open: the Postgres superuser, standby and admin passwords, the apps'
   database passwords, Garage's `rpc_secret` and `admin_token`, the heartbeat
   tokens and Mbin's OAuth2 keypair, until `secrets rotate`. These are reached
   only over the mesh, except Mbin's keypair, so they matter less once step
   4 of this list is done.

## An admin whose site is evicted

The admin stopped cooperating, and the others can no longer log in to their
host. It still runs and is still a member of the deployment: possibly a data
site, a Garage node, an apps site, or the gateway. `paisans site remove
--evict` takes it out without ever reaching it, and a replacement declared in
the same edit takes its roles over (README, *Evicting a site: `--evict`*).
`home-b` is the site here; where the gateway differs, the step says so.

The order matters: what can redirect the community's names closes first, the
secrets file is re-encrypted before any new site's secrets are generated, and
the mesh is rekeyed after the eviction and before the replacement joins.

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
   new site is ever encrypted to them. Closes: every value written from now
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

4. **In one edit of `paisans.yaml`:** take them out of `admins` and every
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
   out. Run no `apply` until it finishes. Closes: every remaining site's
   WireGuard key, which an admin who could read the secrets file holds, and
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
   joined by that apply's `site add`. Closes: the outage of step 5, and their
   ssh keys on every remaining site. Open: a key of theirs on a remaining
   host that `host prepare` never recorded (see step 2 of the first case).

8. **Take away their Pocket ID administrator**, as in step 3 of the first
   case. Closes: their administration of Pocket ID and every app.

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

10. **Rotate the rest**, in the order of step 5 of the first case: the OIDC
    client secrets, then the apps' S3 keys. The DNS token was step 1 and the
    WireGuard keys step 6.

11. **Remove the evicted site's secrets**, now that no site names it:

    ```sh
    paisans secrets prune             # lists them
    paisans secrets prune --execute
    ```

**What stays with the evicted host**, whatever is done here:

* a copy of the database and of the objects, as they were when it was cut;
* everything it saw while it terminated TLS, when it was the gateway:
  members' session cookies, and anything typed into a form;
* the Postgres, Garage and heartbeat secrets and Mbin's OAuth2 keypair, until
  `secrets rotate`. The Postgres and Garage ones reach nothing once the mesh is
  closed; Mbin's keypair signs tokens that work from the internet;
* members' sessions in every app, until they expire or `secrets rotate` signs
  every member out;
* when they were a recipient, every secret as it was until step 2, in every
  copy of the file they hold.
