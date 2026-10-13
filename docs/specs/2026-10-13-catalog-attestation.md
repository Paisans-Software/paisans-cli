# Catalogs are trusted by GitHub release attestations

Date: 2026-10-13. Status: approved by the founder in session, 2026-10-13. Not
implemented. Amends *Signing* in
`docs/specs/2026-10-12-blueprints-and-catalogs.md`, which this replaces.

A catalog pin used to trust the SSH keys its release tags were signed by. The
keys were the weak part: a catalog's author had to keep one safe and move to a
new one through a chain of signed releases, an operator was asked to check a
fingerprint by another channel before trusting it, trusted keys only ever
accumulated, and the core catalog needed a key the founder holds.

A catalog on GitHub is now trusted by an **attestation** its own release
workflow makes: a Sigstore signature, made with a short lived certificate that
GitHub Actions' OIDC token earns, naming the repository, the workflow file,
the tag and the commit it ran on, and recorded in a public transparency log. A
pin names the workflow it trusts instead of a key. Nobody keeps, rotates or
loses a key, and the core catalog needs none.

## Settled

Founder decisions, 2026-10-13, in session:

* **A catalog on GitHub is trusted by its release attestation.**
* **SSH-signed tags are dropped**: `signers`, `keys/allowed_signers`,
  `catalog add --trust`, key rotation and `internal/blueprints/signers/core`
  all go.
* **A catalog anywhere else is pinned `unsigned: true`**, as one on GitHub
  that does not attest may be.

## What does not change

Most of what made a pin trustworthy never depended on signatures, and stays:

* **The pin is a commit.** What a deployment renders is fixed by it.
* **The configuration store keeps a SHA-256 of every blueprint snapshot**, and
  the sites trust the admin's signature on the version that carries it. Sites
  never check a catalog's attestation, as they never checked its signature.
* **The grant review shows a moved pin and what it changes**, and asks for
  approval. That review is the gate on new code. An attestation answers one
  narrower question before it: did this release come from where the pin says.

## What a release attests

The subject is the catalog's digest at the release: the SHA-256 that
`blueprints.Digest` computes over every file under `blueprints/`, the one
`scripts/sync-core-catalog.sh` records for the core copy. The toolkit
recomputes it from the commit it fetched, so the attestation is about exactly
the files a deployment renders, and needs no artifact beyond the repository.

A catalog's release workflow, on a pushed `v*` tag:

```yaml
# .github/workflows/release.yml in the catalog
on:
  push:
    tags: ["v*"]
permissions:
  contents: write
  id-token: write
  attestations: write
jobs:
  attest:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@<pinned commit>
      - id: digest
        run: echo "sha256=$(scripts/catalog-digest.sh)" >> "$GITHUB_OUTPUT"
      - id: attest
        uses: actions/attest-build-provenance@<pinned commit>
        with:
          subject-name: paisans-catalog
          subject-digest: sha256:${{ steps.digest.outputs.sha256 }}
      - run: gh release create "$GITHUB_REF_NAME" "${{ steps.attest.outputs.bundle-path }}#catalog.sigstore.json" --verify-tag
        env:
          GH_TOKEN: ${{ github.token }}
```

`actions/attest-build-provenance` (v4.2.2, 2026-08-06) takes `subject-digest`
with `subject-name` in place of a file, and returns the bundle at its
`bundle-path` output; both read from its `action.yml`. The bundle is published
as the release's asset `catalog.sigstore.json`, so the toolkit reads it from
`https://github.com/<owner>/<repo>/releases/download/<tag>/catalog.sigstore.json`
with no GitHub token.

The toolkit ships `scripts/catalog-digest.sh` for catalog authors to copy, the
same computation as `scripts/sync-core-catalog.sh`, so the workflow and the
toolkit agree on the digest.

## The pin

```yaml
catalogs:
  example:
    url: https://github.com/example/paisans-blueprints
    tag: v1.3.0
    commit: <40 hex>
    attested_by: example/paisans-blueprints/.github/workflows/release.yml
```

`attested_by` is the signer workflow, `<owner>/<repo>/<path>`. A pin has
exactly one of `attested_by` and `unsigned: true`. `config` refuses
`attested_by` on a URL that is not `https://github.com/<owner>/<repo>`, and
one whose `<owner>/<repo>` is not the URL's: a workflow in another repository
attesting this one's releases is not this repository's word.

## What is checked

Every fetch of an attested pin, fresh or from the cache, checks the bundle
with `sigstore-go` (`github.com/sigstore/sigstore-go`, v1.3.0, 2026-07-30), the
library `gh attestation verify` is built on, against these, all of which must
hold:

| Check | Value |
|---|---|
| issuer | `https://token.actions.githubusercontent.com` |
| signer workflow | the pin's `attested_by`, at `refs/tags/<tag>` |
| source repository | the URL's `<owner>/<repo>` |
| source ref | `refs/tags/<tag>` |
| source commit | the pin's `commit` |
| runner | GitHub hosted; a self hosted runner is refused |
| subject | `sha256:<the digest recomputed from the fetched commit>` |
| predicate type | `https://slsa.dev/provenance/v1` |
| transparency log | the signature is in it |

These are the checks `gh attestation verify` makes with `--signer-workflow`,
`--source-ref`, `--source-digest` and `--deny-self-hosted-runners`, read from
its help in gh 2.91.0.

**The trusted root** is Sigstore's, fetched through its TUF repository with
the root shipped in the binary, and cached. A workstation with no network and
no cache cannot check an attestation, and does not need to: it renders from
the configuration store's snapshot, which the admin's signature covers.

## The commands

* **`catalog add <name> <url>`** reads the newest tag, fetches it and its
  `catalog.sigstore.json`. If the release attests, the command shows the
  signer workflow the certificate names and records it as `attested_by`; the
  workflow is a path in the repository the operator chose, so there is no
  fingerprint to check by another channel. A release with no bundle is
  refused unless `--unsigned` is given. A catalog not on GitHub needs
  `--unsigned`.
* **`catalog update`** checks the new release against the pin's `attested_by`.
  A release attested by another workflow file, a renamed one included, stops
  the update and names it; `--attested-by <owner>/<repo>/<path>` accepts it.
  There is no chain of keys to walk: every release is checked on its own.
* `--trust` is gone.

## The core catalog

The binary records the core catalog's identity in place of a key:
`Paisans-Software/blueprints/.github/workflows/release.yml`. A deployment that
pins `catalogs.core` to a commit or a mirror is checked against that identity,
whatever the pin says. A mirror on another host cannot carry GitHub's
attestation of the original's release, so a mirror is checked by fetching the
bundle from the original repository's release, with the commit the mirror
serves.

`Paisans-Software/blueprints` gains the release workflow above. Its releases
are tagged by a human, as every release here is.

## What this does not protect

* **An admin of the catalog's repository** can change its release workflow and
  attest anything. That release is in the public transparency log, and the
  grant review shows what it changes; the attestation does not stop it.
* **GitHub and Sigstore** are trusted for this: GitHub to issue the OIDC token
  only to the workflow it names, Sigstore to issue certificates only for it.
* **A private catalog** cannot be read anonymously. It is pinned unsigned until
  the toolkit reads release assets with the operator's GitHub credentials,
  which this amendment does not add.

## Open

* Whether GitHub's immutable releases, which lock a release's tag and assets,
  are worth requiring of a catalog. Not checked against GitHub's current
  documentation.

## Order of the work

1. `scripts/catalog-digest.sh`, and the release workflow in
   `Paisans-Software/blueprints`.
2. Verification with `sigstore-go` and `attested_by` in `config`, `kinds` and
   the commands; `signers`, `--trust`, `kinds.Rotate`, `kinds.VerifyTag`,
   `kinds.PublishedKeys` and `internal/blueprints/signers/core` removed.
3. The core identity in the binary, and a core pin checked against it.
4. README, `docs/commands.md`, the blueprints spec's status.
