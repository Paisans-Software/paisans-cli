# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Each change gets one bullet, a single sentence of at most 35 words; related
changes share a bullet. Detailed notes are on the corresponding
[GitHub release](https://github.com/Paisans-Software/paisans-cli/releases).

## [Unreleased]

### Added

- `paisans doctor` reads Caddy's certificate failures, checks the DNS token from each Caddy site and the workstation (`--secrets`), and reports deployment hostnames whose records point elsewhere or are missing, never feeding Cloudflare's lockout.
- `apply` checks the DNS token from every Caddy site before Caddy starts or reloads, and `dns init` and `dns prune` from the workstation, stopping on a refusal or no answer rather than feeding Cloudflare's lockout.
- `paisans secrets list` prints every secret path the configuration needs, whether each is set, whether it is generated, pasted or captured, and the path `secrets rotate` takes, never a value.
- SMTP settings are tried from a site over ssh: `secrets set` and `secrets rotate` log in and email the operators before writing a password, `apply` refuses settings that do not log in, `validate` warns.

### Changed

- Errors refer to `paisans doctor` when its checks name the cause: a health gate TLS internal error on a hostname Caddy serves, an unreachable database or site, and `gateway move`'s token refusals.
- `paisans doctor` runs its checks at once and prints each as it ends; a site that does not answer is one line naming what was not checked, and the failures are listed again, deepest first.
- `paisans secrets rotate` asks at a terminal for a pasted secret's new value, the DNS token or an SMTP password, and `--all` includes them; a new DNS token is tried against the zone first.
- The grant review lists each site's foundation images first, under `foundation images (required):`, then removals, then what needs approval under its count, so the count heads exactly the entries it counts.

### Fixed

- `host prepare` lets containers reach Pocket ID's API on their own site's mesh address, port 1413, wherever the account service runs; the default deny dropped its calls, so member pages timed out.
- A failed login link email says Pocket ID's cause without its wrapping and connection string format, and once, not on every operator's line, when every email failed the same way.
- `paisans secrets set <key>` at a terminal asks for that key's value with echo off instead of refusing, so a set credential can be replaced by hand.
- A first apply with several apps reading one new Pocket ID group no longer stops on `409 Name is already in use`: a group made since the plan was read is found and used.
- A host ssh cannot reach is named once, with ssh's own words once, instead of repeated through every layer that tried to read it, and `init` lists each unreachable site on its own line.

## [0.1.0] - 2026-10-14

### Added

- The `pocket-id` app declares `management`, `paisans` or `pocket-id`: under `paisans` the account service manages signup, bans and recovery, and the toolkit renders Pocket ID's signup and email-code settings.
- `validate` refuses fewer than two operators with a key on every site that runs Pocket ID.
- An `smtp:` block may declare `security: none` for a relay at a private or mesh address, with a warning; a public address or a dotted name is refused.
- `paisans version` and `paisans --version` print the toolkit version and the commit the binary was built from, and blueprints' `requires_toolkit` is checked against that version.

### Changed

- Every HAProxy site serves Pocket ID's API on its mesh address at port 1413, and the account service calls it there rather than at Pocket ID's public hostname.
- The `invite` blueprint is the `account` blueprint, running paisans-account-service: `settings.signup` (`invite` or `open`) is required, and every account app serves signups on `join.<domain>` beside `account.<domain>`.
- The Pocket ID gateway closes its signup routes on `allow_user_signups` alone.
- The `admins:` section of `paisans-stack.yaml` and the secrets file is `operators:`, and `paisans admin` is `paisans operator`.
- apply makes each operator a member of `operators`, `members` and every app's groups, and a Pocket ID administrator only under `management: pocket-id`.
- The admin reconciler is the operator reconciler: it reads Pocket ID, writes nothing, and reports operators missing from Pocket ID, non-operators in `operators`, and administrators who are not operators.
- The admins heartbeat is the operators heartbeat, its token `operators_heartbeat_token`, its monitors `operator continuity`.
- The account app uses the deployment's one `pocket-id` app, and `identity_app` is refused.
- The uptime blueprint's admin group defaults to `operators`.
- `paisans create` asks who manages accounts, adds the account app under `paisans`, and asks for at least two operators.

### Fixed

- `init` and `secrets set` ask for an SMTP password, and `secrets rotate --all` rotates one, only when an app's mail settings name a username, so a relay with no login needs none.

### Security

- The uptime monitor requires STARTTLS under `security: starttls`, so mail and SMTP credentials are never sent in plaintext when a relay does not offer it.
- The gateway refuses any request to Pocket ID that carries an `X-API-Key` header with 403, so Pocket ID's API keys work only on the mesh.

[Unreleased]: https://github.com/Paisans-Software/paisans-cli/compare/v0.1.0...develop
[0.1.0]: https://github.com/Paisans-Software/paisans-cli/tree/v0.1.0
