# `paisans create` writes a new deployment's `paisans.yaml` from questions

Date: 2026-10-12. Status: approved by the founder in session.

Every command in the toolkit starts from a `paisans.yaml`, and today the only
way to get one is to copy `examples/paisans.example.yaml` and edit it. That
example declares four sites, eight apps and object storage, and a newcomer has
to work out which of it they need and which role combinations validate refuses.
`paisans create` asks the questions instead, writes a commented file that
validates, and hands over to `paisans init`.

## Scope

In: a new `paisans create` command, a new `internal/create` package, the entry
in `docs/commands.md`, and the `README.md` and `docs/development.md` lines that
list commands.

Out:

* **Renaming the example's monitor site.** `examples/paisans.example.yaml`
  names it `watch`, with its app at `status.example.org`, where the wizard's
  defaults are `health` and `health.example.org`. The example is the fixture
  most of the test suite and the render golden tree load, so the rename is its
  own change, made once on its own branch.

* **Editing an existing file.** `create` writes a file that does not exist and
  refuses one that does. Adding a site, an app or an admin later is an edit to
  `paisans.yaml`, as it is today.
* **Secrets.** `create` asks for none and writes none. `init` generates every
  secret the declaration needs and names the ones owed from elsewhere, the DNS
  token and the SMTP password among them, which keeps one command responsible
  for the secrets file.
* **Hosts.** `create` reaches no host. Settling the mesh subnet against every
  site is `init`'s, and it runs once the sites exist.

## Who it is for

An organizer building a deployment from nothing. The questions use the words
of what a community needs (forums, a wiki, a blog, chat, a status page) rather
than the toolkit's kind names, and every answer has a default where one is
sensible. Done is a `paisans.yaml` that `paisans validate` accepts with no
refusal, commented so that the person who answered the questions can read what
each section means, followed by the two commands to run next.

## The command

```
paisans create [--config paisans.yaml]
```

* Refuses when the file already exists, before asking anything, and again just
  before writing, since one may have appeared while the questions were asked.
  The file is opened with `O_EXCL`, so the second check cannot race.
* Refuses when stdin is not a terminal. The command is a conversation, and a
  pipe of answers that drifts out of step with the questions writes the wrong
  file without a word.
* Takes no `--execute`: it writes one local file, and only after the operator
  has read it and said yes.
* Exits 0 when the file is written, 1 when nothing was written (a refusal, a
  "no" at review, end of input or Ctrl-C), and 2 when its command line cannot
  run.

## The questions

Each is a plain line prompt. A default is shown in brackets and taken on Enter.
A menu is numbered. An answer that does not parse is asked again with the
reason, and so is an empty answer to a required question.

1. **Community.** Its name, then its domain. The domain prompt says that
   federation identity is the hostname, so changing the domain after federating
   is a rebuild rather than a rename.
2. **DNS provider.** `cloudflare` or `desec`. Certificates are always issued
   over DNS-01 (see *DNS-01 is what makes step 1 possible* in `README.md`), so the gateway's Caddy has
   to be told where the domain's DNS lives.
3. **Admins.** At least one, and "add another?" after each: a username, a full
   name (first and last), an email address and one or more SSH public keys,
   pasted one per line and ended with a blank line. The username is checked
   against the same pattern `admins` keys are, and against Pocket ID's refusal
   of a trailing `.`, `_` or `-`, when it is typed rather than at the end.
4. **Shape.** One of:

   | Shape | Sites and roles |
   |-------|-----------------|
   | one machine | `server`: data, apps, gateway |
   | one home and one VPS | `home`: data, apps; `vps`: gateway |
   | two homes and a VPS (high availability) | `home-a`, `home-b`: data, apps; `vps`: gateway, witness |
   | custom | as many sites as asked for, each with the roles chosen for it |

   The three named shapes are the role layouts validate accepts with no
   refusal. The VPS in the second shape carries no witness, because a witness
   there would make two etcd voters, and two voters survive nothing while
   requiring both (see `two-etcd-voters`). In the third, one home must have an
   endpoint of its own: with only the VPS dialled, the two homes would reach
   each other only through it, and losing it would leave each voter alone with
   no majority (see `voters-share-a-relay`).
5. **Each site.** Its name (the shape's default), its public address where the
   internet reaches it, its SSH host and its SSH user (default `ubuntu`). The
   public address is required on a gateway in the named shapes, because the
   other sites dial it and `dns init` points every hostname at it; elsewhere it
   is optional. The SSH host is asked only when there is no public address,
   since `ssh.host` defaults to it. In the third shape the first home is asked
   for its endpoint, a dynamic DNS name and a forwarded UDP port. Every site's
   `ssh.keys` names every admin.
6. **Custom.** The number of sites, then for each a checklist of the six roles.
   Validate judges the combination at review; the wizard does not repeat its
   rules.
7. **Apps.** A yes or no for each capability:

   | Capability | Kind | App | Hostname |
   |------------|------|-----|----------|
   | forums and social | `mbin` | `talk` | `talk.<domain>` |
   | wiki | `outline` | `docs` | `docs.<domain>` |
   | blog | `writefreely` | `blog` | `blog.<domain>` |
   | chat | `synapse` and `element` | `chat`, `web` | `chat.<domain>`, `web.<domain>` |
   | status page | `uptime` | `status` | `health.<domain>` |

   Sign in (`pocket-id`, app `auth`, at `id.<domain>`) is always declared,
   because every other app signs in through it. Each chosen app's hostname is
   asked, with the table's default. Each app of a kind the gate can stand in
   front of (`mbin`, `outline`, `writefreely`, `element`) is asked "members
   only?", default yes, and any yes declares the gate (`oauth2-proxy`, app
   `gate`, at `gate.<domain>`), since `visibility_gate` without a gate app is
   refused.
8. **The status page's machine.** The uptime monitor runs on a site of its own
   with the monitor role, never the gateway or the witness, because its job is
   to report their failures (see *The monitor has a site of its own* in `README.md`). When the
   status page is chosen and no site holds the monitor role, the wizard adds
   one, `health` by default, and asks its public address (required: it serves
   its own hostname) and its SSH user. Declining it drops the status page.
9. **Mail.** Always asked, and required: SMTP host, security (`starttls` or
   `tls`), port (587 for starttls, 465 for tls), username (default: the from
   address), from address and from name (default: the community's name).
   Pocket ID mails each admin's login link and every member's login codes, the
   status page alerts by mail, and Mbin mails account notices. A deployment
   with no mail has admins who sign in only through a printed link and outages
   nobody is told of. Validate still treats a missing `smtp` block as a warning,
   so a file written by hand may leave it out; the wizard is stricter than the
   schema on purpose. The password is a secret and is not asked: the wizard
   names `paisans secrets set external.smtp_password` at the end.
10. **Review.** The rendered file, then validate's findings exactly as `paisans
    validate` prints them, then "Write paisans.yaml? [Y/n]".

## What the wizard decides

These follow from the answers and are not asked, so that a newcomer is never
asked a question whose right answer the toolkit already knows.

* **Placement.** Every kind that can join the cluster (`pocket-id`, `mbin`,
  `outline`, `writefreely`) is `cluster` when any site holds data, so that
  adding a data site later is an addition rather than a move. The kinds that
  must be pinned are pinned: `oauth2-proxy`, `synapse` and `element` to the
  first site with the apps role, `uptime` to the monitor site.
* **Object storage.** When a clustered app stores objects (`mbin`, `outline`,
  `writefreely`), Garage runs on every data site, with a replication factor of
  the number of data sites capped at 2. A clustered app may not keep its files
  on one disk (`storage-local-on-cluster`).
* **Synchronous replication** is on in the high availability shape, as
  `cluster.synchronous: true` with `synchronous_strict: false`, which is what
  makes automatic failover lose no committed write.
* **etcd timings** of 200 ms and 2000 ms on every shape with more than one
  voter, since the defaults assume one datacenter and a home connection's
  hiccup would otherwise start an election.
* **Mesh.** `mesh.subnet: 10.44.0.0/24` and site addresses `10.44.0.1`
  onward. `init` checks the subnet against every site before the first apply
  and moves the subnet and the addresses together when it overlaps anything
  (see *A deployment has an id, and a host knows every deployment on it* in
  `README.md`), so the wizard need not guess.
* **The deployment id**, generated as `init` would generate it.
* **Pocket ID** keeps its uploads in the database (`file_backend: database`)
  and is named after the community (`app_name`). **The status page** admits
  the `admins` group.

## The file it writes

The file is rendered from a text template, not marshalled, because the
newcomer reads it: each section carries a comment saying what it means and
what is permanent, worded as `examples/paisans.example.yaml` words it, so the
two never explain the same key two ways. Every value the operator typed passes
through a YAML quoting function, so a name holding `:` or `#` is a string and
not a syntax error.

Before anything is shown, the rendered bytes are parsed with
`config.LoadBytes` and checked with `validate.Check`, the same checks every
command runs. The review shows exactly what `paisans validate` would say of
the file.

## Refusals and warnings

* **A refusal** writes nothing. The wizard prints it as validate does, then
  offers to answer the section it names again (sites, apps or mail, read from
  the finding's key) or to quit. Every earlier answer is the default the second
  time, so answering again is mostly Enter. This is the custom shape's normal
  path, since that is the one place the wizard lets validate judge the roles.
* **A warning** is shown and does not stop the write, as `AGENTS.md` separates
  the incoherent from the merely risky. The named shapes leave a known few: one
  data site keeps a single copy of each object, and a deployment without the
  status page has no uptime monitor.
* **End of input or Ctrl-C** at any question writes nothing and says so.

## After it writes

It prints the path written, then:

```
paisans secrets set external.smtp_password   # the SMTP password, read from stdin
paisans init                                 # the id is set; checks the subnet, generates secrets
paisans apply                                # shows every step; --execute runs them
```

## Layout

* `internal/create`: `Prompter` (line prompts over an `io.Reader` and an
  `io.Writer`), `Answers` (what was asked, with no YAML in it), the shapes and
  the decisions above, `Run` (the questions, in order) and `Render` (answers
  and id to bytes, from an embedded template). It does no I/O but what it is
  handed, so its tests script stdin.
* `cmd/paisans/create.go`: the flag, the two refusals, the loop of questions,
  render, validate and review, and the write.

## Tests

* The prompter: defaults, re-asking, menu bounds, a key paste ended by a blank
  line, and end of input.
* Every named shape with no apps, with every app, and with members only on and
  off: scripted answers, rendered, loaded with `config.LoadBytes`, checked with
  `validate.Check`, asserting no refusal and naming each warning expected. This
  is what holds the claim that a named shape always validates. The same files
  also render with `render`, so a refusal only render makes is caught too.
* A golden file per named shape under `internal/create/testdata/`, regenerated
  with `-update`.
* A custom shape that puts data and witness on one site: the refusal is shown,
  the sites are asked again, and the corrected answers write the file.
* Quoting: a name and an email holding `:`, `#`, quotes and a leading `-` load
  back unchanged.
* The command: an existing file is refused, stdin that is not a terminal is
  refused, and "no" at review writes nothing.
