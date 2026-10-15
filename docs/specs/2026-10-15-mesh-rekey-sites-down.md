# A mesh rekey finishes with sites down

Status: draft, awaiting founder approval. It changes `paisans mesh rekey`
(`internal/meshrekey`) and the mesh's rendering; it depends on the
classification of sites in `docs/specs/2026-10-15-rootless-and-host-admit.md`,
*A deployment with sites missing*.

## Why

`mesh rekey` refuses a site that does not answer over ssh, "since every site
is a peer of some site rekeyed", and its health gate wants etcd healthy at
every voter. So one host that is down, or whose owner has evicted the
deployment, blocks every rekey, including the one that matters most: after
an operator leaves or a key is exposed, every remaining key must be replaced
whether or not every host is up. And `apply` refuses while any site has a
next key in the secrets file, so a rekey left waiting on a down site would
hold back every other change to the deployment.

## Settled

Founder decisions, 2026-10-15:

* **A rekey succeeds with sites down or evicted.** The sites that answer are
  rekeyed; the rest are recorded as behind and caught up when they return.
* **Each site's key has a generation**, a number that goes up by one each
  time the site's key changes. Generations are per site, not one counter for
  the deployment, so a rekey of one site marks no other site behind.
* **The public key and its generation live in `paisans-stack.yaml`, not in
  `secrets.enc.yaml`.** Neither is secret. Both files are published together
  in every signed version and kept on every site, but sops encrypts the
  secrets file's values, so only an operator with an age key can read them.
  In `paisans-stack.yaml` a host, `doctor` and an operator who has decrypted
  nothing can read which key each site runs and how new it is, and a peer's
  key can be checked against what an operator signed. The private keys and
  the rekey's progress stay in `secrets.enc.yaml`.

## The declaration

```yaml
sites:
  home-a:
    wireguard:
      public_key: 3xY...=   # written by init and mesh rekey
      generation: 4
```

`init` writes generation 1 with the first key. `mesh rekey` writes each
change; nothing else does, and `validate` refuses a `public_key` that is not
a WireGuard key or a `generation` below 1. Both are written by splicing, as
`ssh.user` is, keeping every other line and comment. A secrets file whose
private key does not derive the declared public key is refused by every
command that renders, naming the site.

Each rendered mesh file records, in the site's manifest, the generation of
its own key and of every peer key it holds.

## A rekey with sites down

Each site still goes through the four stages, one at a time: new key, swap,
health, promote. What changes is which sites each stage needs.

**A site that answers** is rekeyed as today. At promote, its `public_key`
and `generation` in `paisans-stack.yaml` change with the private key in the
secrets file, in one published version.

**A site that is down**, named or under `--all`:

1. **New key.** Generated on the workstation as today, written as its next
   private key.
2. **Swap, peers only.** Every peer that answers gets the new public key; the
   site's own file cannot be written. Its old key is accepted nowhere that
   answered, which is what a rekey after an exposure needs.
3. **Health.** As for any site (below).
4. **Promote.** The new public key and its generation are written to
   `paisans-stack.yaml`. The private key stays the site's next key in the
   secrets file, and the site is listed as behind.

**The gates count only what answers.**

* The handshake gate skips peers that are down.
* The health gate needs etcd healthy at a quorum of its voters and Patroni
  with one leader and every data site that answers streaming. A quorum it
  cannot meet still refuses, before a site's rekey starts, as today.
* A relay that is down cannot be swapped, and the sites that reach the mesh
  only through it are down with it; the plan says so.

The run ends listing the sites behind, each with what is pending on it.

**`apply` goes on.** It no longer refuses because a site that is down has a
next key: peers render that site's declared public key, which is the new one.
It still refuses while a site that answers is mid rekey, as today, since
rendering would hand the old key back to its peers.

## When a site comes back

A site that was down during a rekey cannot join the mesh: its file holds
peer keys that no peer runs any more, and its own key, if it was rekeyed, is
one no peer accepts. WireGuard refuses its handshakes, so its etcd and
Patroni cannot reach the cluster and take no part in it. Nothing on the site
has to refuse.

The workstation sees it. The next `apply` or `doctor` compares the
generations in the site's manifest with `paisans-stack.yaml`:

```
home-b is behind the mesh: home-a's key is at 4, home-b holds 3; its own
key is pending. apply --site home-b brings it up to date first.
```

**The catch up runs first.** `apply` brings a site that is behind up to date
before any other step on it: its mesh file with every peer's current key and,
when one is pending, its own next private key, then `wg syncconf` through the
helper. Its next key is then promoted in the secrets file. Its handshakes
gate the rest of the apply. Until the catch up has run, every step that would
put the site in the cluster refuses, naming it.

A site that is not coming back is removed with `site remove --host-gone`,
which drops its key, its generation and its next key.

## Tests

* `validate`: a malformed `public_key`, a `generation` of 0, and a secrets
  key that does not derive the declared public key, each refused.
* `init` writes generation 1 and the public key, spliced.
* Rekey plans: one site down under `--all` (peers swapped, its next key
  kept, generation raised, listed behind); a relay down (its dependants
  listed); a quorum lost (refused before any swap).
* `apply` renders a down site's new public key on its peers and does not
  refuse for its pending key; it still refuses for a site that answers mid
  rekey.
* Catch up: a manifest behind the declaration plans the catch up first and
  refuses cluster steps before it.
* `site remove --host-gone` drops the site's key, generation and next key.

## Open

* **Existing deployments.** A deployment from before this change has no
  `wireguard` in its sites. Under the no-migration rule it is redeployed;
  is that acceptable for a mesh change, or should `init` fill them in from
  the secrets file once?
