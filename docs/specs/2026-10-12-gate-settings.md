# The settings gating needs, checked on both sides

Status: approved in session on 2026-10-12. Founder direction: move the
passthrough collision check into `validate`; describe, per kind, the settings
`gated` and `auto_login` need; have `doctor` check the live apps against that
description and `paisans.yaml` against it, and fix what it can with
`--execute`. No central per-kind branching in `doctor`. Founder decision: only
settings written and locked on disk are in scope; nothing writes an app's
database.

## The problem

Two things go wrong today, and neither is caught early.

1. **A `config:` key the template already writes is refused only at render.**
   Gating an Mbin makes its template write `MBIN_USE_FEDERATION_ALLOW_LIST`,
   so a deployment that already carried that key in `apps.talk.config` passes
   `validate` and fails `config-key-already-rendered` at render. A whole
   `apply` runs `validate` first and renders only in its pull stage, so
   `init` and `host prepare` run before the refusal.
2. **Nothing reads what a running app actually enforces.** The gate leaves
   ActivityPub to the app and, for Mbin, its API with a token, so the app's
   own settings are part of the gate: private instance mode, the allow list,
   authorized fetch, WriteFreely's private mode. The toolkit renders and locks
   them, but an image that does not honour the lock, a stack not recreated
   after an apply, or a database row written before the lock can leave a
   running app with one of them off. The monitor's signed fetch and private
   API checks see two of these from outside; nothing sees the rest.

## Part 1: `validate` renders to find passthrough collisions

`validate` gains `config-key-already-rendered`, the same rule name and message
render uses, as a refusal. It renders the deployment with throwaway secrets:
`secretsgen.Fill` on an empty `config.Secrets`, then `render.Build`, and turns
the render's refusal into a finding. The throwaway secrets never leave memory.

* Rendering needs no real secret to find a collision: which keys a template
  writes depends on `paisans.yaml`, not on secret values. Checked on
  2026-10-12: the example renders cleanly this way, and staging's file with
  `MBIN_USE_FEDERATION_ALLOW_LIST` back in `talk.config` is refused with the
  same message render gives.
* Render keeps its own check. It is the last line before files reach a host,
  and a caller that skips `validate` still meets it.
* Cost: generating the secrets is nearly all of it (an Mbin's OAuth key pair
  takes a few hundred milliseconds; rendering each key, milliseconds), so the
  throwaway set is kept for the life of the process and only topped up.
* Only this rule is taken from render. Any other render error is left to
  render, so `validate` does not grow a second copy of render's failures.

## Part 2: each kind describes the settings gating needs

A new record in `internal/kinds`, one entry per kind that has any, in the same
shape as `GateSpec`: a map from kind to its record, read through one function,
with a test that every gateable kind has a record or says it needs none.

```go
// GateSetting is one setting the app must enforce while it uses the gate,
// written and locked by a file the toolkit renders.
type GateSetting struct {
	Name string                // the app's own name: MBIN_PRIVATE_INSTANCE, app.private
	Want string                // the value it must have: "true"
	When func(config.App) bool // Eg: gated, or gated and federating
	Why  string                // what the gate leaves open without it
	// ConfigKey is the passthrough key that could contradict it, in the
	// kind's config syntax; SettingKey the settings: key, when one owns it.
	ConfigKey, SettingKey string
}

// LiveSettings reads those settings in a running app. Each kind that has
// GateSettings implements it in its own file.
type LiveSettings interface {
	Service() string                   // the compose service to exec in
	ReadCommand(names []string) []string // argv: what the app holds, and what it locks
	Parse(out string, names []string) (map[string]Live, error)
}

type Live struct {
	Value  string
	Locked bool // locked by the file the toolkit renders
}
```

`doctor` walks the apps, asks `kinds.GateSettingsFor(app)` and
`kinds.LiveSettingsFor(kind)`, and does the same thing for every kind. A kind
without a record has nothing to check. Adding a kind adds a file in
`internal/kinds`, never a branch in `doctor`.

**Only settings the toolkit writes and locks on disk.** Every setting here is
rendered into a file the app reads (Mbin's `.env`, WriteFreely's
`config.ini`) and named in that file's lock list (`MBIN_LOCKED_SETTINGS`,
`locked_settings`), so the app reads it from the file on every read and
neither its admin page nor its own command line can change it. A setting that
is wrong in a running app is therefore a file that is wrong, or an app that
was not restarted after the file changed, and the fix is `apply`, which
renders the file and recreates the stack. Nothing here writes an app's
database. A running app that does not lock a setting the file names (an image
older than the lock) is a Fail whose fix is the image, not a database edit.

### Mbin

| Setting | When | Without it |
|---|---|---|
| `MBIN_PRIVATE_INSTANCE` = `true` | gated | `/api/*` with any token would rest on the firewall alone; anonymous API reads reach Mbin |
| `MBIN_USE_FEDERATION_ALLOW_LIST` = `true` | gated | a signed read from any instance is answered |
| `MBIN_AUTHORIZED_FETCH` = `true` | gated and federating | an unsigned ActivityPub read is answered |

Read: `docker exec` in the `app` service prints the variables and
`MBIN_LOCKED_SETTINGS` from the running container's environment. A setting
named there is read from the environment on every read
(`SettingsManager.php:104` at `v1.15.1+paisans`), so the environment's value
is what Mbin enforces. Reasoned from source, not yet run against a live Mbin.

### WriteFreely

| Setting | When | Without it |
|---|---|---|
| `app.private` = `true` | gated | reads are not limited to the allowlist |
| `app.authorized_fetch` = `true` | gated and federating | an unsigned read or delivery is accepted |

Read: `writefreely -c /data/config.ini settings list` in the `app` service,
which prints each setting's effective value and marks the ones `config.ini`
locks (`cmd/writefreely/settings.go:38-58` at `v0.21.1+wisp`).

### Outline, Element

No record: neither has a setting the gate leaves to it. Outline's API is a
token path because Outline issues tokens only from a signed in session, which
is a property of the software, not a setting.

## Part 3: `doctor` reads both sides

**`paisans.yaml`.** `doctor` loads the file through `validate`, as every
command that reaches a host does, so a `config:` or `settings:` key that
contradicts gating stops it with `validate`'s refusal
(`config-key-already-rendered`, `gated-without-signed-fetch`). When the
refused key only repeats the value gating requires, the refusal says that
`paisans doctor --execute` removes it.

**The running apps.** A new section, `settings`, after `containers`. For each
app using the gate, on each site it runs on that `doctor` reached, the kind's
read command runs in the app's running container, found by its compose labels
and this deployment's. Every `GateSetting` whose value differs from `Want`, or
that the file does not lock, is a Fail naming `paisans apply --site <site>`,
or the image when the app does not lock it after an apply. An app not running
there, or a read that failed, is a Skip, not a pass. An app that uses the gate
only for auto-login has no setting left to it, so nothing to check.

`doctor` needs no secrets for either: the wanted values come from the kind's
record, not from a render.

## Part 4: `doctor --execute`

`doctor` stays read only without `--execute`, as every other command that
changes something. With it, before it loads the file, it applies the one fix it can make
safely, and no other:

| Drift | `--execute` does |
|---|---|
| a `config:` key that repeats the value gating requires (Eg: `MBIN_USE_FEDERATION_ALLOW_LIST: true` on a gated Mbin) | removes the key from `paisans.yaml`, keeping every other line |
| a `config:` or `settings:` key that contradicts what gating needs (Eg: `authorized_fetch: false`) | nothing: that is a decision in the file, and changing it changes policy. The finding says which line to edit |
| a setting wrong, or not locked, in the running app | nothing: names `paisans apply --site <site>`, or the image when the app does not lock it |

The edit prints what it removed and the file is loaded again afterwards; a
file that no longer loads is restored and the run fails.

**Not migration code.** Founder decision: the `paisans.yaml` edit removes
only a key whose value the toolkit now renders identically, so the file says
the same thing with one fewer place to say it. It converts no older schema
(`visibility_gate` and a declared `oauth2-proxy` stay refused, never
rewritten), and it never changes a value.

**Tier.** `doctor --execute` changes no host: it edits `paisans.yaml` alone.

## Not in scope

* Reading or fixing any setting that is not one gating needs.
* Fixing a rendered file on a host that differs from the render: `apply`
  already refuses to clobber a locally edited file, and says so.
