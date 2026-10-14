# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Each change gets one bullet, a single sentence of at most 35 words; related
changes share a bullet. Detailed notes are on the corresponding
[GitHub release](https://github.com/Paisans-Software/paisans-cli/releases).

## [Unreleased]

### Added

- The `pocket-id` app declares `management`, `paisans` or `pocket-id`: under `paisans` the account service manages signup, bans and recovery, and the toolkit renders Pocket ID's signup and email-code settings.
- `validate` refuses fewer than two operators with a key on every site that runs Pocket ID.
- An `smtp:` block may declare `security: none` for a relay at a private or mesh address, with a warning; a public address or a dotted name is refused.
- `paisans version` and `paisans --version` print the toolkit version and the commit the binary was built from, and blueprints' `requires_toolkit` is checked against that version.

### Changed

- The `invite` blueprint is the `account` blueprint, running paisans-account-service: `settings.signup` (`invite` or `open`) is required, and every account app serves signups on `join.<domain>` beside `account.<domain>`.
- The Pocket ID gateway closes its signup routes on `allow_user_signups` alone.
- The `admins:` section of `paisans-stack.yaml` and the secrets file is `operators:`, and `paisans admin` is `paisans operator`.
- apply makes each operator a member of `operators`, `members` and every app's groups, and a Pocket ID administrator only under `management: pocket-id`.
- The admin reconciler is the operator reconciler: it reads Pocket ID, writes nothing, and reports operators missing from Pocket ID, non-operators in `operators`, and administrators who are not operators.
- The admins heartbeat is the operators heartbeat, its token `operators_heartbeat_token`, its monitors `operator continuity`.
- The account app uses the deployment's one `pocket-id` app, and `identity_app` is refused.
- The uptime blueprint's admin group defaults to `operators`.
- `paisans create` asks who manages accounts, adds the account app under `paisans`, and asks for at least two operators.

[Unreleased]: https://github.com/Paisans-Software/paisans-cli/commits/develop

### Security

- The uptime monitor requires STARTTLS under `security: starttls`, so mail and SMTP credentials are never sent in plaintext when a relay does not offer it.
