# Server and container stats with Beszel

Status: drafted 2026-10-29 at the founder's request, not implemented. The
founder chose Beszel for the capability and the default hostname
`status.health.<domain>`, and skipped the design questions; every line marked
*provisional* is the author's call and open to correction.

## Why

The uptime monitor says whether an app answers and whether a site is up. It
does not say why. When a site slows down or a container restarts in a loop,
the first questions are about the machine: is it out of memory, is a disk
full, which container is eating the CPU. Answering them today means an ssh
session and `docker stats` on each host in turn, at the moment the operator
least wants to be doing that.

Every site runs Docker, so one agent per site can read every container's CPU,
memory and network figures beside the host's own, and a hub can keep their
history. Beszel is that pair: a small Go agent and a PocketBase hub with a
web UI, both one container each.

## What gets built

All of it in `paisans-cli`, plus two image tracks in the core catalog:

1. **A reserved blueprint, `beszel`**: the hub. Built into the binary beside
   `uptime`, because the toolkit renders what no catalog blueprint can: the
   list of sites and a token for each.
2. **A per site agent** in every site's `infra` stack, beside the heartbeat,
   rendered whenever the deployment declares a `beszel` app.
3. **An Ed25519 secret format**, for the hub's key.
4. **A wizard question** after the health page.
5. **Two tracks in `foundation.yaml`** in `paisans-blueprints`: `beszel` and
   `beszel-agent`.

## Goals

* Every site's host stats (CPU, memory, swap, disk, network, load) and every
  container's CPU, memory and network, in one page, with history.
* No step by hand: `deployment apply` brings up the hub and every agent, and
  `site add` and `site remove` add and drop a site's system with no visit to
  the hub's UI.
* Operators sign in through Pocket ID, and only operators can.
* The agents open no port: each dials the hub over the mesh.

## Non goals

* **No alert seeding.** Beszel keeps alert rules and notification URLs per
  user, in its UI. Paging already belongs to the uptime monitor; an operator
  who wants a memory alert sets one in Beszel by hand. The hub's SMTP
  settings are written from the deployment's `smtp` block, so an address an
  operator adds in Beszel receives the alerts they set (amended 2026-10-29;
  README.md, *Server and container stats*).
* **No GPU, SMART, sensors or systemd monitoring.** Each needs a device or
  a privilege the rootless agent does not have.
* **No member access.** The hub is an operator tool, like the monitor.

## Anti goals

* An agent that listens on any address, mesh or public.
* A token or the hub's private key in a file another user on the host can
  read, or in a rendered file that is 0644.
* A site's history silently kept after `site remove`, or a removed site still
  listed as down.
* A hub reachable only through the gateway: like the monitor, it must answer
  when the gateway does not.

## The software

Beszel, `henrygd/beszel`, checked at `v0.21.1` (released 2026-10-22). Citations
are to that tag unless marked.

| Fact | Where |
|---|---|
| Images `ghcr.io/henrygd/beszel/beszel` and `ghcr.io/henrygd/beszel/beszel-agent`, linux amd64, arm64, arm/v6, arm/v7; tags have no `v` (`0.21.1`, `0.21`) | `.github/workflows/docker-images.yml` |
| Hub listens on 8090, data in `/beszel_data`, image `FROM scratch` with no `USER` | `internal/dockerfile_hub`, `internal/cmd/hub/hub.go:42` |
| `GET /api/health` answers 200 with no auth (PocketBase v0.40.5) | pocketbase `apis/health.go` |
| The hub loads `<data>/id_ed25519` if present with `ssh.ParsePrivateKey`, and generates an Ed25519 key there if not | `internal/hub/hub.go:152-201` |
| `<data>/config.yml` lists systems (`name`, `host`, `port`, `token`, `users`); at every start the hub creates the missing ones, updates the rest and **deletes every system not listed** | `internal/hub/config/config.go`, `SyncSystems` |
| Agent: `KEY` or `KEY_FILE` (the hub's public key) is required; `HUB_URL` with `TOKEN` or `TOKEN_FILE` makes it dial the hub over WebSocket; `SYSTEM_NAME` names it | `internal/cmd/agent/agent.go:122-145`, `agent/client.go:78-122` |
| Agent reads Docker at `DOCKER_HOST`; only `/var/run/docker.sock` and Podman's socket are found unset | `agent/docker.go:870` |
| Host network stats need `network_mode: host` | beszel-docs `agent-installation.md` |
| OIDC is a PocketBase OAuth2 provider, `oidc`, callback `/api/oauth2-redirect`; configured in the UI or the PocketBase collections API, with no env var | beszel-docs `oauth.md`; pocket-id.org `client-examples/beszel` |
| `USER_EMAIL` and `USER_PASSWORD` create the first superuser and admin user on a new install only | `internal/migrations/initial-settings.go` |
| `USER_CREATION=true` lets an OAuth2 sign in create its user; such a user's role is `OAUTH_DEFAULT_USER_ROLE`, `user` or `readonly`, never `admin` | `internal/hub/collections.go:28-37`, `internal/users/users.go:28-49` |
| `DISABLE_SSH`, `SHARE_ALL_SYSTEMS`, `APP_URL`, `TRUSTED_PROXY_IPS`, `CHECK_UPDATES`; every hub variable also reads with a `BESZEL_HUB_` prefix | beszel-docs `environment-variables.md` |

Not verified, and the builder checks each on a host before relying on it:
that a token in `config.yml` may be any string rather than a UUID; that the
agent honours `DISABLE_SSH`; the memory either one holds at rest.

## The hub: the `beszel` blueprint

A reserved blueprint like `uptime`, and **pinned only to a site holding the
`monitor` role** (*provisional*), for the monitor's reason: it is what an
operator opens when the rest is failing, so it lives where the rest is not. It
keeps SQLite under `/beszel_data`, so `cluster` is refused by the rule that
refuses the monitor. Its rules, beside the monitor's table in `README.md`,
*Monitoring*:

| Combination | Result | Rule |
|---|---|---|
| a `beszel` app pinned to a site without `monitor` | refused | `beszel-needs-a-monitor-site` |
| more than one `beszel` app | refused | `one-beszel` |

A monitor with `web_server.managed_by: external` hosts the monitor alone, and
`web-server-external-serves-one-app` already refuses a second app there, the
hub included. Lifting that is a change to `listen`, not to this blueprint.

**Its hostname is served like the monitor's.** An app pinned to a monitor site
already has its hostnames pointed at the monitor's `public_address` by
`dns init` and served by the monitor's Caddy, or the gateway's when the
monitor is beside it. Nothing new is needed for `status.health.<domain>`
except that it is a name two labels under the domain, which the certificate
covers through DNS-01 like any other. A deployment whose DNS provider proxies
its records must leave this one unproxied, since the provider's own edge
certificate covers one label only; `dns init` already writes the monitor's
records unproxied.

**Not gateable**, for the monitor's reason in the uptime blueprint's comments:
the gate signs in at the identity provider on sites that may be the ones
down.

**Sign in.** An OIDC client restricted to `settings.admin_group`, default
`operators`, callback `/api/oauth2-redirect`, PKCE on, so Pocket ID refuses
everyone else before Beszel sees them. The hub runs with:

* `APP_URL` the app's `https://` hostname.
* `USER_CREATION=true` and `OAUTH_DEFAULT_USER_ROLE=user`, so an operator's
  first sign in creates their user.
* `SHARE_ALL_SYSTEMS=true`, so every user sees every system and none has to
  be added to one.
* `DISABLE_SSH=true`: the hub never dials an agent.
* `USER_EMAIL` and `USER_PASSWORD` from generated secrets, the break glass
  superuser for when Pocket ID is what failed. Its email is
  `beszel-admin@<domain>` (*provisional*); nothing is sent to it.
* `TRUSTED_PROXY_IPS` the address the Caddy in front of it connects from.

Two more containers in the hub's stack make the rest of it converge
(*provisional*):

* **`admin`**, a one shot from the hub's own image, runs `/beszel superuser
  upsert <email> <password>` (beszel-docs `user-accounts.md`) once the hub is
  healthy, since a superuser made before the hub's first start collides with
  the one its migration makes. `USER_PASSWORD` is read on a
  new install only, so this is what makes a rotated `admin_password` take
  effect on the next apply.
* **`setup`**, on the foundation's `heartbeat` image (curl and a shell), runs
  once the hub answers `/api/health`: it signs in as the superuser and writes
  the `oidc` provider into the `users` collection's OAuth2 settings through
  the PocketBase API, with Pocket ID's authorize, token and userinfo URLs and
  the client's id and secret. It writes the same settings on every start, so a
  rotated client secret reaches the hub on the next apply. Its script carries
  the superuser password and is rendered 0600.

**Its port.** The hub listens on 8090 in its container and publishes 8084 on
the monitor's mesh address, which is how the agents reach it: the core
catalog's Element publishes 8090.

## The systems list: `config.yml`

The toolkit renders `/beszel_data/config.yml` for the hub, one system per site
of the deployment, in site name order:

```yaml
systems:
  - name: site-a
    host: 10.44.0.1
    token: <sites.site-a.beszel_token>
    users: []
```

`host` is the site's mesh address, which only names the system, since the hub
never dials it. An empty `users` gives the system to the first user, which is
the break glass admin, and `SHARE_ALL_SYSTEMS` shows it to everyone else.

Because the hub deletes what the file does not list, the file is the whole
truth: `site add` renders a new line, `site remove` drops one, and the hub's
next start makes it so, history included. `deployment apply` restarts the hub
when the file changes, which it already does for a changed rendered file. The
file is rendered 0600 into the hub's data directory, bind mounted with it, and
a hub that has written its own changes to it has them overwritten, which is
right: the toolkit owns it.

## The agent: one per site

A `beszel-agent` service in each site's `infra` stack whenever the deployment
declares a `beszel` app, beside the heartbeat:

* `network_mode: host`, like every infra container, for the host's network
  figures. On an Engine before 29.5 a rootless container with host networking
  sits in RootlessKit's namespace and reports that namespace's interfaces;
  `doctor` already warns of that Engine for the infra stack.
* The deployment user's rootless Docker socket, `/run/user/<uid>/docker.sock`
  (`apply.RootlessDockerHost`), mounted at `/var/run/docker.sock`, read only.
  The UID is learnt over ssh, after rendering, so the rendered file names
  `${PAISANS_DOCKER_SOCKET:?...}` and the transport exports
  `PAISANS_DOCKER_SOCKET` beside the `DOCKER_HOST` it already exports ahead
  of every command (*provisional*). The rendered file is the same on every
  site, and a compose run outside the toolkit fails by name rather than
  mounting nothing.
* `HUB_URL` the hub's site's mesh address and published port, over plain
  HTTP inside the mesh, as every service talks across sites (*Rule 2*).
* `TOKEN`, which is `sites.<site>.beszel_token`, and `KEY`, the hub's
  public key, from `beszel-agent.env`, 0600, as `heartbeat.env` is.
* `DATA_DIR=/var/lib/beszel-agent`, bound from the infra stack's
  `beszel-agent/data`, where the toolkit renders `fingerprint`, 0600, the
  lowercase hex sha256 of `beszel-agent:<deployment id>:<site>`, so the
  fingerprint the hub pins on first connect survives a restart and a host
  rebuilt under the same site name.
* `SYSTEM_NAME` the site's name. `DISABLE_SSH=true`, so it listens for
  nothing.

**The socket is the deployment user's Docker.** Read only on the mount does not
restrict the API behind it: a process that can talk to that socket can start a
container as the deployment user, which already owns every container on the
site. The agent only reads, and it runs from the foundation's pinned digest
like every infra image, but this is a real widening, and it is stated here and
in `README.md` so nobody meets it later.

**Per container CPU, memory and I/O need their controllers delegated** to the
deployment user's systemd, and `site admit` already does it: it writes
`Delegate=cpu cpuset io memory pids` for `user@.service` on every host
(`internal/hostadmit/user.go:352`), and a staging host shows all five
controllers. So the figures work on Ubuntu 22.04 and 24.04 alike, with no new
drop-in and no `doctor` check.

**Memory.** `hostmem` counts the agent in `FloorBase` and the hub in a new
`FloorBeszel` on a site a `beszel` app is pinned to. `FloorBeszel` is
provisional, set from what the hub runs, until it is measured at rest on a
host.

**Its env file restarts only the agent.** `deployment apply` treats a changed
infra env file as a change to the gateway's compose, except the heartbeat's;
the agent's gets the same exemption, so rotating a site's token does not
reload the gateway.

## Secrets

| Key | Format | Read by | Rotation |
|---|---|---|---|
| `sites.<site>.beszel_token` | `hex32` | that site's agent, the hub's `config.yml` | the site's agent and the hub restart |
| `apps.<app>.hub_key` | `ed25519-openssh` (new) | the hub, as `/beszel_data/id_ed25519`, 0600 | every agent and the hub restart |
| `apps.<app>.hub_key_public` | `ed25519-public` (new), the half of `hub_key` | every agent's `KEY` | with `hub_key` |
| `apps.<app>.admin_password` | `password` | `USER_PASSWORD`, the setup container | the setup container resets it |

The two new formats follow `rsa-keypair` and `rsa-public`: `of` names the
private half. Generated with `crypto/ed25519` and written with
`golang.org/x/crypto/ssh`'s `MarshalPrivateKey` (unencrypted) and
`MarshalAuthorizedKey`. The site token is generated with the site's other
secrets, only while the deployment declares a `beszel` app, and listed by
`secretsgen`'s declared set like `heartbeat_token`. A site's token rotates
with `paisans secrets rotate sites.<site>.beszel_token`, which applies that
site's `infra` and the hub. `site add` and `site remove` re-render the hub
with the monitor (`apply.PlanMonitorReseeds`).

## The wizard

After the health page, and only when the health page is yes, `paisans create`
asks *"Server and container stats (Beszel)?"*, default yes (*provisional*),
and declares an app keyed `stats`, blueprint `beszel`, hostname
`status.health.<domain>`, pinned to the monitor site.

## Images

Two tracks in the core catalog's `foundation.yaml`, with the toolkit's
`supportedTags` naming them:

```yaml
  # 0.21 only: Beszel is 0.x, and a minor may change the agent's protocol,
  # so a new minor is a catalog release.
  beszel: "ghcr.io/henrygd/beszel/beszel:0.21.*"
  beszel-agent: "ghcr.io/henrygd/beszel/beszel-agent:0.21.*"
```

The core catalog is released first, then the toolkit, as for every foundation
image.

## Documentation

* `README.md`, *Monitoring*: a section for the hub, its rules table, the agent,
  the socket's reach and the delegation requirement.
* `docs/development.md`: the new parts under *Run*.
* `CHANGELOG.md`: one bullet under *Added*.

## Testing

Unit tests for the parse, the secrets, `config.yml` rendering (a site added and
removed), the agent's infra service, and each validate rule; golden trees
regenerated and read.

On staging, three hosts of 1 GB: `site-a` and `site-b` with `[data, apps]`,
`edge` with `[gateway, witness, monitor]`. Pocket ID and WriteFreely clustered,
the uptime monitor and Beszel on `edge`, and no account service or digest.
Pass when Beszel shows three systems up, each listing its containers with CPU
and memory (Patroni, Garage, Pocket ID and WriteFreely among them); an operator
signs in through Pocket ID and a member cannot; and a `site remove` of a site
followed by apply drops its system.
