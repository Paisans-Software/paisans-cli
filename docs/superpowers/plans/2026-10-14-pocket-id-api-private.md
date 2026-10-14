# Pocket ID's API Private Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Pocket ID's static API key works only on the mesh: every HAProxy site serves Pocket ID's active instance on `<mesh address>:1413`, the account service calls the API there, and the gateway refuses any request carrying `X-API-Key`.

**Architecture:** Two pull requests into `develop`, in order. The first adds `listen pocket-id` to the rendered `haproxy.cfg`, claims port 1413 in `render.SiteListeners`, and points `ACCOUNT_IDP_URL` at the site's own listener. The second adds the `X-API-Key` refusal to the pocket-id blueprint's Caddy snippet. The second must not reach a release without the first, because before the first the account service calls the API through the gateway.

**Tech Stack:** Go 1.25, Go templates, golden-file render tests, HAProxy 3, Caddy 2.

**Spec:** `docs/specs/2026-10-14-pocket-id-api-private.md`. Read it with current names: the account service, the `account` blueprint, `ACCOUNT_IDP_URL`, `ACCOUNT_OIDC_ISSUER`, `internal/render/account.go`. The operator reconciler is in its own repository and out of scope; `pocketid-go` v0.1.0 already refuses a non-private `BaseURL`.

## Global Constraints

- The listener's port is `1413`, beside the reconciler's `1412`.
- The listener: `bind <site mesh address>:1413`, `option httpchk GET /healthz`, `http-check expect status 204`, `default-server inter 3s fall 3 rise 2 on-marked-down shutdown-sessions`, one `server <site> <address>:1411 check` per site running the pocket-id app.
- It is rendered on every site that runs HAProxy, in a deployment with a pocket-id app, and nowhere else.
- `ACCOUNT_IDP_URL=http://<its site's mesh address>:1413`; `ACCOUNT_OIDC_ISSUER=https://<pocket-id hostname>`.
- The gateway rule, verbatim: `@api_key header X-API-Key *` then `respond @api_key 403`, before any other matcher in the snippet.
- Say "blueprint", never "kind". Docs describe current state only; rejected alternatives only in `docs/decisions.md`. No em dashes in new text. No Outline references.
- One `CHANGELOG.md` `[Unreleased]` bullet per pull request, at most 35 words.
- Never `git stash` or force-push. Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

- A deployment without a pocket-id app: no `listen pocket-id`, and no 1413 claim. Pinned in Task 1.
- A pocket-id app pinned to one site: one `server` line, at that site, on every HAProxy site. Pinned in Task 1.
- Something else on a site binding 1413 on the mesh address: `validate` refuses `port-collision`. Pinned in Task 1.
- A site with apps but no clustered app runs no HAProxy, so it claims no 1413. Covered by reusing `RunsHAProxy`.
- The account service on two sites: each `.env` names its own site's address, not the first site's. Pinned in Task 2.

---

## Pull request 1: the mesh route (`feat/pocket-id-api-mesh`)

### Task 1: HAProxy's `listen pocket-id`, and port 1413 in the port table

**Files:**
- Modify: `internal/render/plan.go` (constant `pocketIDAPIPort = 1413`)
- Modify: `internal/render/ports.go` (claim in `SiteListeners`)
- Modify: `internal/render/site.go` (template data, `pocketIDServers`)
- Modify: `internal/render/defaults.go` (exported `PocketIDAPIPort`)
- Modify: `internal/render/templates/haproxy.cfg.tmpl`
- Test: `internal/render/pocketid_api_test.go`

**Interfaces:**
- Produces: `const pocketIDAPIPort = 1413` (package `render`), `render.PocketIDAPIPort`, `func (p *planner) pocketIDServers() []clusterMember` (nil without exactly one pocket-id app).

- [ ] **Step 1: Write the failing tests.** In `pocketid_api_test.go`: the fixture's `home-a` and `home-b` `haproxy.cfg` each carry `listen pocket-id`, `bind <own address>:1413`, `option httpchk GET /healthz`, `http-check expect status 204`, and `server home-a 10.44.0.1:1411 check` and `server home-b 10.44.0.2:1411 check`; with `auth` pinned to `home-b`, only the `home-b` server line, on both sites; with `auth` and `account` removed, no `listen pocket-id`; `render.SiteListeners(cfg, "home-a")` holds `10.44.0.1:1413/tcp` keyed `apps.auth`, and none without a pocket-id app; `validate` refuses `port-collision` when another listener takes 1413.
- [ ] **Step 2: Run** `go test ./internal/render -run PocketIDAPI` and see each fail.
- [ ] **Step 3: Implement.** The template block, after `listen postgres`:

```
{{- if .PocketIDServers }}

listen pocket-id
    bind {{ .Address }}:{{ .PocketIDAPIPort }}
    option httpchk GET /healthz
    http-check expect status 204
    default-server inter 3s fall 3 rise 2 on-marked-down shutdown-sessions
{{- range .PocketIDServers }}
    server {{ .Name }} {{ .Address }}:{{ $.PocketIDPort }} check
{{- end }}
{{- end }}
```

`pocketIDServers` reads `cfg.PocketIDApp()` and `AppSites(cfg)[idp]`, sorted, each at its site's mesh address. `SiteListeners` adds `HAProxy Pocket ID API`, key `apps.<idp>`, on the mesh address, inside the `RunsHAProxy` branch when `cfg.PocketIDApp()` answers.
- [ ] **Step 4: Run** the tests; they pass.
- [ ] **Step 5: Commit.**

### Task 2: the account service calls the API on its own site's listener

**Files:**
- Modify: `internal/render/account.go` (`IssuerURL`, `APIURL` replace `IdentityURL`; `accountValuesFor(name, site string)`)
- Modify: `internal/render/appview.go` (pass `planned.Site`)
- Modify: `internal/blueprints/reserved/account/.env.secret.tmpl`
- Test: `internal/render/account_test.go`

- [ ] **Step 1: Write the failing tests.** `ACCOUNT_IDP_URL` is `http://10.44.0.1:1413` on `home-a` and `http://10.44.0.2:1413` on `home-b`; `ACCOUNT_OIDC_ISSUER` stays `https://id.example.org` and follows a renamed identity hostname, while `ACCOUNT_IDP_URL` does not change with it.
- [ ] **Step 2: Run** `go test ./internal/render -run Account`; they fail.
- [ ] **Step 3: Implement** `APIURL: fmt.Sprintf("http://%s:%d", p.meshAddress(site), pocketIDAPIPort)` and render each variable from its own field, with a comment per variable saying why.
- [ ] **Step 4: Run** the tests; they pass.
- [ ] **Step 5: Commit.**

### Task 3: goldens, README, CHANGELOG, decision

- [ ] `go test ./internal/render -update`, and read every changed file: only `haproxy.cfg` on HAProxy sites and the account `.env` change.
- [ ] README: *The identity app* (under the account service) says where each address comes from; *Pocket ID runs on every apps site, and one of them is active* describes the listener and its takeover; the port list under the collision check names Pocket ID's API port; *What the gateway closes* says the account app reaches the API over the mesh.
- [ ] `CHANGELOG.md` `[Unreleased]`, `Changed`: one bullet.
- [ ] `docs/decisions.md`: the decision, with the spec as its design.
- [ ] `go vet ./... && go test ./...`; commit; fresh clone test; open the pull request.

## Pull request 2: the gateway refusal (`feat/pocket-id-api-gateway`), after the first merges

### Task 4: the snippet refuses `X-API-Key`

**Files:**
- Modify: `internal/blueprints/reserved/pocket-id/caddy.snippet.tmpl`
- Test: `internal/render/pocketid_snippet_test.go`

- [ ] **Step 1: Failing test.** The rendered snippet carries `@api_key header X-API-Key *` and `respond @api_key 403`, before `@setup` and before `reverse_proxy`, in every configuration of the snippet's conditional blocks.
- [ ] **Step 2: Run**; it fails.
- [ ] **Step 3: Implement** the spec's block and its comment; change the `/api/users/<id>/one-time-access-email` comment to say the account service calls it over the mesh.
- [ ] **Step 4: Run**; it passes. Regenerate goldens and read the diff.
- [ ] **Step 5: Real Caddy.** Run the gateway's Caddy image with the rendered snippet in front of a stub upstream: a request with `X-API-Key` (any case, empty value) is 403; `/`, `/login`, `/api/webauthn/...` and `/.well-known/openid-configuration` without it reach the upstream.
- [ ] **Step 6:** README's *The toolkit administers Pocket ID through its static API key* says the gateway refuses the header; CHANGELOG bullet; the spec's Status line is "implemented; not yet run end to end". Commit, fresh clone test, pull request.
