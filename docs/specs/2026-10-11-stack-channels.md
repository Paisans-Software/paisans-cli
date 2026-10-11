# Fork images follow a toolkit channel

Date: 2026-10-11. Status: approved by the founder in session.

The two forks the toolkit runs, Mbin (`ghcr.io/paisans-software/mbin`) and
WriteFreely (`ghcr.io/josephquigley/writefreely-wisp`), each publish a moving
image tag that means "vetted for the toolkit": `stack-1`. A maintainer moves
it to a released version once that version is known to work with what the
toolkit renders. The `mbin` and `writefreely` kinds default to it, and `paisans
apply` resolves it to a digest while it plans, the way it already resolves the
gateway's Caddy image. A promotion therefore reaches every deployment on its
next apply, with no toolkit release, as a planned and health gated change.

## Scope

In:

* a `promote` workflow in each fork that moves `stack-<N>`;
* the `mbin` and `writefreely` kinds defaulting to `stack-1`;
* `apply` resolving every application image named by a tag to a digest at
  plan time, recording it, and keeping it consistent across the sites an app
  runs on;
* the dry run naming what moved;
* the README, the example, `docs/decisions.md`.

Out:

* other images. Caddy already follows its major version. Third party images
  (Outline, Pocket ID, Synapse and the rest) keep exact tags, since they are
  not ours to promote. They do gain digest resolution, below, which is what
  lets a rebuild of a version tag reach a host.
* any unattended pull. A promotion reaches a host only when someone runs
  `apply` there.

## The channel

`stack-<N>` names the latest promoted release for the toolkit's template
contract `N`.

**`N` is a template contract, not an application version.** It changes only
when a fork release needs something rendered that an older toolkit does not
render, or stops reading something an older toolkit renders. A release like
that is promoted to `stack-<N+1>`, and only a toolkit whose kind names
`stack-<N+1>` follows it. Every other release, major versions included, is
promoted within the channel it already has. Mbin 2.0 that reads the same
`.env` is a `stack-1` promotion.

A kind records its channel in one place, its default image in
`internal/kinds/kinds.go`:

```go
{Name: "app", Image: "ghcr.io/paisans-software/mbin:stack-1", ...}
{Name: "app", Image: "ghcr.io/josephquigley/writefreely-wisp:stack-1", ...}
```

The first promotions are Mbin `1.15.0+paisans` and WriteFreely `0.21.1+wisp`,
the first releases carrying locked settings, so `stack-1` is the contract in
which the toolkit renders `MBIN_LOCKED_SETTINGS`, WriteFreely's
`locked_settings`, authorized fetch on both, and `/healthz` checks on both.

## Promotion

Each fork gets `.github/workflows/promote.yml`, run by hand
(`workflow_dispatch`) from `main`, with two inputs: `version` (Eg:
`1.15.0+paisans`) and `channel` (Eg: `1`).

It:

1. refuses unless the tag `v<version>` exists and has a published GitHub
   Release, and the image `<version>` with `+` written `-` exists in the
   registry;
2. refuses to move the channel to a version older than the one it names,
   unless the input `rollback` is true;
3. retags with `docker buildx imagetools create -t <image>:stack-<channel>
   <image>:<version>`, which copies the manifest and rebuilds nothing;
4. appends a line to that GitHub Release's body, `Promoted to stack-<channel>
   on <date>.`, so the release page is the record of what each channel has
   named and when;
5. writes the old and new digests to the job summary.

It uses the workflow's own `GITHUB_TOKEN`, which can write to the fork's own
package, so neither fork needs a new secret.

**Rolling a channel back moves the tag, not the data.** A release that ran a
one way migration (WriteFreely 0.21's settings move is one) leaves its schema
behind when the channel moves back, and the older build may not run on it.
The release notes say when a release is one way; a rollback past one needs the
release's own recovery steps first.

## Resolution at plan time

Today only the gateway's Caddy image is resolved (`apply.ResolveImages`,
`render.Plan.CaddyImages`, `internal/imagedigest`). Application images are
rendered as written, so a tag that moves never changes a compose file and
never reaches a host. This extends the same mechanism to every application
image:

* `render` stays offline. It renders each image as configured and lists, per
  site and per stack, the images a compose file names (`Plan.Images`, which
  absorbs `CaddyImages`).
* `apply.Build`, given a resolver, resolves each reference that has a tag and
  no digest to `<reference>@sha256:...`, once per plan, so every site and every
  service in one plan gets the same build of one tag. It rewrites each
  `image:` line naming that reference; the number of lines it rewrites must
  equal the number of services the template rendered with it, or the plan
  stops, as `PinCaddyImage` does for one line today. Mbin's app and its
  messenger consumers name one image, and are pinned together.
* A reference that already carries a digest is left as it is. An operator who
  writes `apps.<name>.images.app` with a digest gets exactly that build.
* When the registry cannot be reached, the plan keeps the build the host
  already runs, with a warning, and refuses a stack that runs none, as for
  Caddy.
* Commands that rewrite a compose file for a narrower reason (`site remove`,
  `site add`, `storage add`) pass no resolver and keep the deployed build.
* The site manifest records, per service, the reference as configured, the
  digest pinned, and when it was resolved, replacing the `caddy` record with a
  general `images` record.

The floating tag refusal (`floating-image-tag`) is unchanged: `latest` and an
untagged reference stay refused for an application, since they cross every
version the publisher ships. `stack-<N>` and any version tag pass, as they do
today.

### An app on several sites

A clustered app runs one database from several sites, and two builds against
one database is how a migration runs twice or not at all. So when an app runs
on more than one site:

* an apply that plans every one of its sites resolves once and moves them
  together;
* an apply with `--site` that leaves some of them out pins the build those
  other sites already run, read from their manifests, and says that the app
  stays on it; moving the app to a new build takes an apply that covers all
  its sites;
* if the sites already disagree, the plan refuses and names each site's
  build, and the operator chooses with an apply that covers all of them.

## What the operator sees

The dry run lists each image whose build changes, by app and service, with
the old and new digests and, when the image carries the
`org.opencontainers.image.version` label (both forks' builds set it through
`docker/metadata-action`), the old and new versions:

```
talk  app  ghcr.io/paisans-software/mbin:stack-1  1.15.0-paisans (sha256:1a2b...) -> 1.16.0-paisans (sha256:3c4d...)
```

The apply then pulls, checks, validates and health gates the stack exactly as
for any image change.

## Overriding

`apps.<name>.images.app` still wins:

* a digest, or a version tag such as `1.15.0-paisans`, holds the app on one
  build until the operator changes it;
* another channel, such as `stack-2` ahead of a toolkit release, is accepted,
  and validate warns (`image-channel-differs`) that the toolkit renders
  `stack-1`'s contract, naming both.

## Validate

| Rule | Level | When |
|------|-------|------|
| `image-channel-differs` | warn | an app's image names a `stack-<N>` other than its kind's default |

## Tests

* kinds: both forks default to `stack-1`, and no other kind names a channel.
* render: `Plan.Images` lists each compose file's images, Mbin's shared image
  once with its service count.
* apply: with a fake resolver, a tag is pinned on every line naming it, a
  digest is left alone, an unreachable registry keeps the deployed build and
  refuses a new stack, a `--site` apply holds a multi site app on its deployed
  build, disagreeing sites refuse, and the manifest records each pin.
* The Caddy tests keep passing against the general path.
* The promote workflow is exercised once per fork against the first
  promotion, and its job summary quoted in the pull request.

## Known limits

* **Resolution needs the registry from the workstation.** Anonymous reads of
  ghcr.io and Docker Hub manifests; a registry that requires credentials for
  reads is not supported, as for Caddy today.
* **A version tag the publisher rebuilds now moves.** That is the purpose for
  rebuilds that fix a CVE, and it shows in the dry run like any other change.
* **The channel is only as good as the promotion.** Nothing checks that a
  promoted release works with the templates; the maintainer promoting it is
  the check.
