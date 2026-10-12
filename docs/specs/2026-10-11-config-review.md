# Reviewing what `paisans.yaml` grants before it is applied

Date: 2026-10-11. Status: approved by the founder in session, 2026-10-11.
Implementation holds until the eviction design
(docs/specs/2026-10-11-site-evict.md) is decided.

`paisans.yaml` is one file every admin of a deployment edits and commits, and
whoever runs `apply` runs it with their own access. So any admin can grant
themselves a host through someone else's run:

* **A key.** Adding their name to a site's `ssh.keys`, or replacing the key
  under an admin the site already names, makes `host prepare` install their
  key for `ssh.user`. That user has `sudo` and is in the `docker` group, so the
  key is root on the host.
* **An image.** Changing an entry of `apps.<name>.images` to an image they
  control makes `apply` pull and run it, on the mesh and with whatever the
  template mounts. The infrastructure services run with `network_mode: host`.

Today the plan says `○ authorize ssh key … (bob)`, and with `--execute` it is
not shown at all unless `-v` is given. Nothing on the host remembers what was
applied before, so nothing can say what changed.

This spec adds two controls:

1. **Review.** Each host records what it was last granted. `apply` and
   `host prepare` show what would change from that record and, when anything
   is added or changed, ask the operator to approve it at a terminal before any
   host is touched.
2. **The host's key allowlist.** A host owner may list, on the host, the key
   fingerprints `host prepare` is allowed to install there. A key not on the
   list is refused, whoever runs the command.

Review is for the operator running the command; the allowlist is for the
person who owns the host and may not be the one running it.

## Settled

Founder decisions, 2026-10-11, in session:

* **Build the review.** It covers keys and images, not keys alone.
* **No allowlist file means every key is allowed.** A host behaves as it does
  today until its owner runs `host allow`.

## Scope

In:

* A record on each host of what this deployment last granted there: the admin
  keys `host prepare` made authorized keys, and the images `apply` runs.
* A review step in `apply --site`, `apply` with no `--site`, and `host
  prepare`, between the plan and the first change, that needs a terminal when
  something is added or changed.
* `paisans host allow` and `paisans host disallow`, and the refusal in `host
  prepare` of a key the allowlist does not hold.
* `doctor` reporting each host's allowlist and any listed key it would refuse.

Out, each for a stated reason:

* **Settings, `config` and `smtp` of an app.** They become an app's own
  configuration and environment, read by an image the review already covers;
  they do not choose what code runs. A field added later that does choose code
  (a command, a hook, a mount) joins the review in the same change.
* **Who wrote a change.** The toolkit cannot know. The review shows *what*
  changed; finding who changed it is the repository's history.
* **Signing `paisans.yaml`.** Where the file is kept differs per deployment,
  and the toolkit does not assume a forge.

## What is reviewed

For each site, the **grant**: the part of the configuration that decides who
may log in to the host and what code runs on it. It is computed from the
configuration and the render, never from the host.

| Part | Holds | Comes from |
|---|---|---|
| `login` | `ssh.user` | the site's `ssh` section |
| `keys` | one entry per key: admin name, fingerprint (SHA256), the key's comment | every admin in the site's `ssh.keys`, each of their keys |
| `images` | one entry per running service: stack, service, image reference as the compose file writes it | every `compose.yaml` the render writes for the site, infrastructure and apps, including a Caddy reference pinned to a digest |
| `web_server` | the include directory | `web_server.include_dir`, on a site whose routes the operator's web server serves |

The include directory is where root writes files the operator's web server
reads. How that server is reloaded is found by apply from the server it
detects on the host, so paisans.yaml does not choose it, and it is not part of
a grant computed from the configuration.

A key is the same key when its fingerprint is; a change of comment or of the
admin it is listed under is a change of that key's entry. An image is the same
when its reference string is; a tag moving to a new digest without the
reference changing is not seen, which is why `validate` already refuses
floating tags.

## The record on the host

`/var/lib/paisans/granted.<token>.json`, owned by root, mode 0644: the
grant as last applied, plus when.

```json
{
  "version": 1,
  "keys":   {"applied_at": "2026-10-11T23:10:04Z", "login": "ubuntu", "entries": [ … ]},
  "images": {"applied_at": "2026-10-11T23:12:40Z", "entries": [ … ]},
  "web_server": {"applied_at": "…", "include_dir": "/srv/caddy.d"}
}
```

* **Readable by anyone, written by root.** It holds fingerprints and image
  references, nothing secret, so a plan reads it without `sudo`. Only root
  writes it, so the login user alone cannot rewrite the baseline.
* **Each part is written by the command that applies it**, after that
  command's steps for the site have all succeeded: `keys` and `login` by `host
  prepare`, `images` and `web_server` by `apply`. A run that fails part way
  writes nothing, and the next run reviews the same changes again.
* **Written under `/var/lib/paisans/registry.lock`**, the lock the registry and
  the deployment record already take, by a temporary file and a rename.
* **Removed with the deployment**: `site remove` deletes it in stage 3, with the
  registry entry; it is listed beside the deployment record in the table of
  what is removed.

A host with no record, which is every host of a deployment built before this
change, has an empty baseline: every entry of its grant is new and is
reviewed once. That is the record being made, not a migration.

## The review

Built for every site the command touches, after the plan and before the first
change. With `--execute` the first change is the registry claim, which comes
before the plan, so there the review is shown, and asked for, before the
claim. Each entry of the grant is compared with the record:

| Compared with the record | Shown as | Needs approval |
|---|---|---|
| in both, the same | not shown | no |
| in the grant only | `+ key SHA256:… (bob, bob@laptop) may log in as ubuntu` | **yes** |
| in both, different | `~ image talk/mbin: ghcr.io/…:1.14 → ghcr.io/…:1.15` | **yes** |
| in the record only | `- key SHA256:… (carol)` | no |

A removal is shown and not approved, because it only takes away. The review is
grouped by site, keys first, and printed in full at every verbosity, with
`--execute` as well as without.

**The dry run shows the review and asks nothing.** It ends with what
`--execute` will ask for.

**`--execute` asks once.** When anything needs approval, the command prints
the review and asks the operator to type `approve`, through the same
`confirmWord` that `site remove --delete-data` uses. Anything else, or end of
input, stops the command; nothing has changed. A run of `apply` with no
`--site` asks once for every site, at the point where it already settles
`sudo` passwords, before its first step.

**No terminal, no approval.** When something needs approval and standard input
is not a terminal, the command is refused before any host changes, saying to
run it at a terminal. No flag answers the question: a flag that could would be
in every script within a week, and approving is the control.

**Nothing to approve, nothing asked.** A run that changes no key and no image
behaves exactly as it does today, terminal or not. Routine applies stay
scriptable.

### Who approved

When exactly one admin's key in `paisans.yaml` is loaded in the operator's ssh
agent (`ssh-add -l`), the record also says `approved_by: <admin>`; otherwise
it says nothing. This is a label for the next reader, not a check: anyone can
load any key they hold.

## The host's key allowlist

`/etc/paisans/ssh-allow.<user>`, owned by root, mode 0644, one per login user
and shared by every deployment on the host. One key per line, `#` starting a
comment:

```
# keys host prepare may add to ubuntu's authorized_keys
SHA256:RXm/ruZ0eTzRXKwi1AQEDynB0VgHQ2ac9KPSFdf/YnA alice
SHA256:kmYcvdi2GkPeWxB6XLjrZB8JHsy2Hm8luHMFp9GMvqk bob
```

It holds fingerprints, not admin names, because a name is whatever
`paisans.yaml` says it is: replacing the key under `admins.alice.keys` must not
carry Alice's permission to the new key.

**No file, every key allowed.** Settled above.

**With a file, `host prepare` refuses to append a key it does not list.** The
refusal is a problem in the plan, before any step runs, naming the key, its
admin and the command that would allow it. Nothing else changes:

| Plan step | With the key not on the list |
|---|---|
| `authorize ssh key` (append) | **refused** |
| `share ssh key`, `rename ssh key` | allowed: the key is already in `authorized_keys`, so nothing new can log in |
| `release`, `forget`, `remove ssh key` | allowed: they only take away |

A key already in `authorized_keys` keeps working whatever the list says. The
list decides what `host prepare` may add, not who may log in now; taking a key
away is still removing it from `ssh.keys` and preparing again, or deleting it by
hand.

### `paisans host allow <site> <admin>...`

```
paisans host allow home-a bob             # dry run: the fingerprints, and the file as it would be
paisans host allow home-a bob --execute   # asks for "home-a" at a terminal
```

* Reads each admin's keys from `paisans.yaml` and shows every fingerprint with
  its comment. The operator checks them against the admin, out of band if they
  are not sure, because those are what the file will hold.
* **When the file does not exist yet, it is created holding also every key
  that some deployment's sidecar already claims for that user**, so that making
  a list does not refuse access the host already has. The plan lists those
  lines separately.
* Needs a terminal and the site's name typed, like `site remove
  --delete-data`. Writing the list is an owner granting access, so it is never
  scripted.
* Writes under `/etc/paisans/authorized_keys.lock`, the lock `host prepare`
  already takes, by a temporary file and a rename.

### `paisans host disallow <site> <admin|fingerprint>...`

Takes lines off the list. It does not remove a key from `authorized_keys`; the
plan says so and names the command that does. Deleting the last line leaves an
empty file, which allows nothing, rather than removing the file, which would
allow everything; to go back to no list, the owner deletes the file by hand.

### `doctor`

For each site: whether the login user has a list, how many keys it holds, and
each key `ssh.keys` names that it does not, as the refusal the next `host
prepare` would give.

## Commands

| Command | Change |
|---|---|
| `apply`, `apply --site` | review `images` and `web_server`; write them after success |
| `host prepare` | review `login` and `keys`; refuse keys the allowlist lacks; write them after success |
| `apply` with no `--site` | one review for every site, before the first step; each step writes its own part |
| `host allow`, `host disallow` | new |
| `site remove` | deletes `granted.<token>.json` in stage 3; never touches `ssh-allow.*`, which is the host's, not the deployment's |
| `doctor` | reports the allowlist |

`runApply` and `hostPrepare` take standard input and output as `runSiteRemove`
does, so tests drive the prompt through a pipe.

## Testing

* The grant: computed from a configuration, one test per part; a changed
  comment, a moved key, a pinned Caddy digest.
* The comparison: each row of the review table, and an empty baseline.
* The prompt: approved, refused, end of input, no terminal with something to
  approve (refused before any host is read for changes), no terminal with
  nothing to approve (runs).
* `apply` with no `--site`: one prompt for several sites; nothing runs when it
  is refused.
* The record: written only after success, under the lock; a failed step leaves
  the old one.
* The allowlist: no file; a file without the key (append refused, share,
  rename and removals allowed); a swapped key under a listed admin's name
  refused; `host allow` seeding a new file from the sidecars; `host disallow`
  leaving an empty file.
