# Mbin's Instance, Meta and Federation settings belong to the configuration

Status: approved by the founder in session, 2026-10-18.

## Why

A deployment's Mbin shows its built-in title and meta text, not the names
`paisans-stack.yaml` gives it. `display_name` names the app's client at
Pocket ID and nothing else, and `community.name` reaches Mbin nowhere. An
admin can fix that in Mbin's settings page, and then the page, not the
configuration, is where the instance is described. The same is true of every
switch on that page that the blueprint does not already lock: an admin can
reopen local signup, turn federation off, or change how members are
admitted, and `apply` neither knows nor puts it back.

The blueprint already locks some settings. It renders each into Mbin's
`.env` and names it in `MBIN_LOCKED_SETTINGS`, and the paisans fork reads a
locked setting from the environment on every read, shows it disabled, and
refuses it through the settings API. This change extends that to every
setting in three sections of Mbin's admin settings: Meta, Instance and
Federation.

## Settled

Agreed in session, 2026-10-18:

* **The configuration owns every field in Mbin's Meta, Instance and
  Federation settings sections.** Each is locked whether or not the
  configuration sets it. A key the configuration leaves out takes a default
  chosen by the blueprint (below), never Mbin's database value.
* **Admins keep the federation allow-list's contents.** Which instances are
  allowed or defederated stays editable inside Mbin. Only the switches are
  locked.
* **Changing one is an edit to `paisans-stack.yaml` and `paisans deployment
  apply`.** Nothing else changes them.
* **The instance title (`KBIN_TITLE`) is the app's display name**: its
  `display_name`, else the app's name, exactly as `Config.ClientName`
  resolves it. It sits beside the logo in the header, and it matches what
  Pocket ID shows at sign in.
* **The meta title (`KBIN_META_TITLE`) is "`<display name>` · `<community.name>`"**,
  for example `Talk · Paisans`. It is the browser tab, the RSS feed title and
  nodeinfo's `nodeName`, which is what other servers and directories call
  the instance, where no logo or hostname gives it context. With no
  `community.name` it is the display name alone.
* **Meta description and keywords default to empty.** The description is
  community facing text, so the toolkit does not write one. Keywords are
  read by nothing that matters.
* **Every field can be set** under `apps.<app>.settings`, and the value set
  is locked the same as a default.
* **The settings the blueprint already forces stay forced**: private
  instance and use allow-list when the app is gated, SSO only when the app
  signs in through OIDC, and authorized fetch's existing rule. A
  configuration that sets one of them to the opposite of the forced value is
  refused by validate.
* **On an app that is not gated, private instance and use allow-list are
  settable**, `private_instance` and `federation_allow_list`, default false.
  Both are locked either way.
* **Federation can be turned off on any app, gated or not.** With
  `federation: false` the toolkit treats the app as one that does not
  federate: the blueprint's federation rules, the gate's signed-fetch
  setting and the monitor's signed-fetch probe, all of which hang on
  `app.federates`, no longer apply to it. That needs `app.federates` to
  follow the setting (see step 2).

### Defaults

| Mbin setting | Section | Setting key | Default |
|---|---|---|---|
| `KBIN_META_TITLE` | Meta | `meta_title` | display name · community name |
| `KBIN_META_DESCRIPTION` | Meta | `meta_description` | empty |
| `KBIN_META_KEYWORDS` | Meta | `meta_keywords` | empty |
| `KBIN_TITLE` | Instance | `title` | display name |
| `MBIN_DOWNVOTES_MODE` | Instance | `downvotes_mode` | `enabled` |
| `KBIN_HEADER_LOGO` | Instance | `header_logo` | false |
| `KBIN_REGISTRATIONS_ENABLED` | Instance | `registrations` | **false**: people join through the account service and Pocket ID |
| `MBIN_SSO_REGISTRATIONS_ENABLED` | Instance | `sso_registrations` | **true**: an invited member's first sign in creates their Mbin account |
| `MBIN_SSO_ONLY_MODE` | Instance | (forced) | true with OIDC, as today; false without, since there is no SSO to restrict to |
| `MBIN_SSO_SHOW_FIRST` | Instance | `sso_show_first` | false |
| `MBIN_SSO_AVATAR_OVERRIDE` | Instance | `avatar_override` | true |
| `KBIN_CAPTCHA_ENABLED` | Instance | `captcha` | false |
| `KBIN_MERCURE_ENABLED` | Instance | `mercure` | false |
| `KBIN_ADMIN_ONLY_OAUTH_CLIENTS` | Instance | `admin_only_oauth_clients` | false |
| `MBIN_PRIVATE_INSTANCE` | Instance | `private_instance` | forced true when gated; otherwise false |
| `KBIN_FEDERATED_SEARCH_ONLY_LOGGEDIN` | Instance | `federated_search_only_logged_in` | true |
| `MBIN_RESTRICT_MAGAZINE_CREATION` | Instance | `restrict_magazine_creation` | false |
| `MBIN_SIDEBAR_SECTIONS_RANDOM_LOCAL_ONLY` | Instance | `sidebar_random_local_only` | false |
| `MBIN_SIDEBAR_SECTIONS_USERS_LOCAL_ONLY` | Instance | `sidebar_users_local_only` | false |
| `MBIN_NEW_USERS_NEED_APPROVAL` | Instance | `new_users_need_approval` | false: the account service already vets them |
| `KBIN_FEDERATION_ENABLED` | Federation | `federation` | true |
| `KBIN_FEDERATION_PAGE_ENABLED` | Federation | `federation_page` | true |
| `MBIN_USE_FEDERATION_ALLOW_LIST` | Federation | `federation_allow_list` | forced true when gated; otherwise false |
| `MBIN_AUTHORIZED_FETCH` | Federation | `authorized_fetch` | as today |

The bold defaults differ from Mbin's own; the rest are Mbin's (the fork's
`SettingsManager::load()` fallbacks).

Two of these were the session's call rather than the founder's, corrected
after the founder approved "on" on the mistaken belief that it was Mbin's
default: Mercure stays off, because live updates through the in-image hub
have not been exercised across sites, and turning it on is its own
decision; SSO show first stays off, because with SSO only mode the SSO
button is the only one.

## The work, in order

### 1. The paisans fork of Mbin

Seven fields of the Instance section cannot be locked today, because
`SettingsManager::LOCKABLE_SETTINGS` does not list them and they have no
environment fallback:

`MBIN_SSO_REGISTRATIONS_ENABLED`, `MBIN_SSO_SHOW_FIRST`,
`MBIN_RESTRICT_MAGAZINE_CREATION`, `KBIN_MERCURE_ENABLED`,
`KBIN_FEDERATED_SEARCH_ONLY_LOGGEDIN`,
`MBIN_SIDEBAR_SECTIONS_RANDOM_LOCAL_ONLY`,
`MBIN_SIDEBAR_SECTIONS_USERS_LOCAL_ONLY`.

Each gains an environment variable of the same name, read with Mbin's
current default when unset so an instance that does not set it behaves as
before, and joins `LOCKABLE_SETTINGS`. The settings page does not yet render
`admin/_locked_setting.html.twig` beside any of the seven, so each gains the
include, and the settings API's refusal of a locked setting is checked for
each. A `fork/` branch from
`develop`, a pull request, then a minor release (a new capability), since
the blueprint's templates need it.

This is fork-only: locked settings are not upstream, so there is no upstream
branch.

### 2. The CLI (Paisans-Software/paisans-cli)

The template cannot reach either name today. `appValues` gains two fields:

* `DisplayName`, `Config.ClientName(app)`.
* `CommunityName`, `community.name`.

And `app.federates`, which the blueprint's `federation.federates` sets
unconditionally today, follows the app's configuration: a blueprint can
name a boolean setting that turns federation off (for mbin, `federation`),
and an app that sets it false does not federate. Everything that reads
`app.federates` (the blueprint `when:` rules, the gate's `signed_fetch`,
the monitor's probe) then follows without its own change. The builder
chooses the blueprint key's spelling.

### 3. The core catalog's mbin blueprint (paisans-blueprints)

* `.env.secret.tmpl` renders every setting in the table, from
  `.Setting`/`.SettingBool` with the default above, and the two titles from
  `DisplayName` and `CommunityName` when the configuration sets no title.
* `locked_settings` names every one of them.
* `setting_keys` maps each Mbin key to its setting, so validate's
  config-key-already-rendered check covers them.
* `checks` gains a type check for each boolean and for `downvotes_mode`
  (Mbin's `DownvotesMode` values), and a refusal when `private_instance` or
  `federation_allow_list` is false on a gated app.
* `federation` names the setting that turns federation off (step 2).
* The service image comment raises the floor to the fork release from step 1,
  since a template naming a setting the fork cannot lock makes Mbin answer
  every request with an error.

## Left to the builder

* Validation messages, in the voice of the existing `checks`.
* Test shape, following what the render and blueprint tests already do for
  the settings locked today.
* Whether `KBIN_JS_ENABLED`, `KBIN_DEFAULT_LANG` and `MBIN_DEFAULT_THEME`,
  which are lockable but sit in the General section, are in scope. They are
  not in the three sections, so the default is to leave them alone.
