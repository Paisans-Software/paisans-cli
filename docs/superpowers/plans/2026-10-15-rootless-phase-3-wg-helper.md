# Rootless Phase 3: The WireGuard Helper Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every command that touches a deployment's mesh interface does so through one root-owned helper per deployment, `sudo -n /usr/local/libexec/paisans/wg-psns-<token> <verb>`, whose `put` refuses any conf that could run a command as root or reach outside the admitted port and mesh subnet.

**Architecture:** `internal/hostprep` renders the helper, a POSIX sh script with the interface, the admitted listen port and the mesh subnet built in, from `snippets/wg-helper.sh.tmpl`; its `put` checks a conf with awk and installs it in `/etc/wireguard` through a temp file and a rename. The deployment's user can neither read nor write `/etc/wireguard`, so the rendered mesh file moves into the deployment's own directory (`/srv/paisans/<token>/psns-<token>.conf`), where `apply`'s ordinary read, compare, write and manifest machinery keeps working, and every mesh step hands that file to the helper's `put` before `up`, `sync` or `restart`. `meshkey`, `meshrekey`, `siteadd`, `siteremove` and `doctor` read through `apply.WGHelper`, and a source walk refuses any `wg`, `wg-quick`, `journalctl` or `systemctl` command written directly in those packages.

**Tech Stack:** Go 1.25, `text/template` and the existing `hostprep` embed, POSIX sh and awk (tested under `sh`, and `dash` and `bash` when present; checked by hand against Ubuntu 24.04's dash and mawk 1.3.4 and macOS's sh and awk), `go/parser` for the source walk, the existing fakes in each package's tests.

**Spec:** `docs/specs/2026-10-15-rootless-and-host-admit.md` (sections *The WireGuard helper*, *What it does* step 13, *What runs as the deployment's user*, *Tests*, *Observed* R9), and the phase index `docs/superpowers/plans/2026-10-15-rootless-and-host-admit.md`, *Phase 3: The WireGuard helper*.

## Global Constraints

* "Admit installs a root-owned script per deployment with the interface name built in, `/usr/local/libexec/paisans/wg-psns-<token>`, mode 755, and one sudoers line" (spec, *The WireGuard helper*). Installing it is phase 5's; this phase renders it and makes every caller use it.
* Verbs, "the only arguments it accepts": `put`, `up`, `restart`, `sync`, `show peers`, `show latest-handshakes`, `show public-key`, `log`. `log` is "the unit's last 200 journal lines". A bad verb exits 64 (*Observed*, R9).
* "`put` accepts only the sections `[Interface]` and `[Peer]`, only the keys the renderer writes (`Address`, `PrivateKey`, `ListenPort`, `MTU` in the first; `PublicKey`, `PresharedKey`, `AllowedIPs`, `Endpoint`, `PersistentKeepalive` in the second), each value matched by a pattern, `ListenPort` equal to the admitted one and every `Address` inside the mesh subnet. Anything else refuses the whole file."
* "The helper's `put` refuses any key outside the renderer's set, any `PreUp`/`PostUp`/`PreDown`/`PostDown`, a `ListenPort` other than the admitted one, and an `Address` outside the mesh subnet." (index, *Global Constraints*). Hooks are refused in any section, any case, with any surrounding whitespace.
* `put` writes `/etc/wireguard/psns-<token>.conf` mode 600 through a temp file and a rename in that directory, never through a link.
* "The renderer stops writing the relay `PostUp`" (spec). Forwarding on a site with an endpoint is `net.ipv4.ip_forward=1` in `/etc/sysctl.d/60-paisans.conf`, set by `host admit` in phase 5 (spec, *What it does*, step 13). Until phase 5 lands, a relaying site forwards nothing; nothing releases from `develop` until phase 6.
* "WireGuard writes and `wg`, `systemctl`, `journalctl` on the mesh | the helper" (spec, *What runs as the deployment's user*).
* The helper's `put` validator is tested by running the rendered script under `sh` against a table of confs, including the renderer's own output.
* A change to what is rendered regenerates the golden tree, and its diff is read.
* No migration code: existing deployments are redeployed, so nothing reads the old `etc/wireguard` rendered path or a manifest entry for it.
* No em dashes anywhere: code, comments, docs, commit messages. Docs describe current state only; no rejected alternatives anywhere. `docs/specs/`, `docs/superpowers/` and `docs/decisions.md` are records and are not edited.
* Commits: `<type>: <lowercase subject>`, a body saying why, and the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
* `CHANGELOG.md`: a behaviour-changing commit adds one bullet under `[Unreleased]`, in its Keep a Changelog category, one sentence, at most 35 words. A commit that changes nothing an operator would notice adds none and says so in its body.
* Never `git stash`; other sessions share the stash. Never push.

## Review Focus

* A conf with `PostUp` in a `[Peer]` section, or a hook key written `postup`, `POSTUP`, `  PostUp`, `\tPostUp` or `PostUp=`: `put` refuses it with exit 65 and installs nothing (Task 2 refusal table).
* A conf whose `AllowedIPs` is `0.0.0.0/0`, a range wider than the mesh, or the owner's LAN: wg-quick would add those routes to the owner's host, so `put` refuses them as it refuses an `Address` outside the subnet (Task 2 refusal table).
* `/etc/wireguard/psns-<token>.conf` planted as a link, to a file or to a directory: `put` replaces a link to a file with the new conf and leaves its target untouched, and refuses a link to a directory (Task 2 install tests).
* An `apply` that stopped after writing the mesh file and before the helper's `sync` or `restart`: the next `apply` finds the file unchanged and the interface up but running other peers, a different key, or without its address, and syncs or restarts it rather than reporting nothing to do (Task 4 tests).
* A host where sudo will not run the helper (not yet admitted, or its sudoers line removed): `apply` stops naming `paisans host admit` and the line sudo printed, not a raw `exit status 1` (Task 3 test).

---

## Call sites

Every `wg`, `wg-quick`, `systemctl ... wg-quick@` and `journalctl -u wg-quick@` run, and every write or read of `/etc/wireguard`, in the packages this phase covers, at `origin/develop` (`fd9b2c77`):

| File:line | Today | Becomes | Task |
|---|---|---|---|
| `internal/apply/apply.go:799` with `:1192-1197` | the rendered `etc/wireguard/psns-<token>.conf` read by `Build` and written by `execute` as files | the rendered file is `srv/paisans/<token>/psns-<token>.conf`, the deployment's own | 1 |
| `internal/apply/apply.go:304` | `systemctl enable --now wg-quick@psns-<token>` | `WGHelper.Put`, then `WGHelper.Up` | 3 |
| `internal/apply/apply.go:310` | `wg-quick strip` into a temp file, then `wg syncconf` | `WGHelper.Put`, then `WGHelper.Sync` | 3 |
| `internal/apply/apply.go:312` | `systemctl restart wg-quick@psns-<token>` | `WGHelper.Put`, then `WGHelper.Restart` | 3 |
| `internal/apply/mesh.go:35` | `t.Run(command)` from `WireGuardStep.Command` | `handMesh` | 3 |
| `internal/apply/mesh.go:115` | `journalctl -u wg-quick@psns-<token> -n 20 --no-pager` | `WGHelper.Log`, its last 20 lines kept | 3 |
| `internal/apply/scope.go:48-49` | `Rollback` runs `WireGuardStep.Command` | `handMesh` with the restored file | 3 |
| `internal/apply/apply.go:1066-1087` | an unchanged file on an up interface is nothing to do | read back with `ip -br addr` (unprivileged), `WGHelper.Show("public-key")` and `Show("peers")` | 4 |
| `internal/meshkey/meshkey.go:29` | `wg show <iface> public-key` | `WGHelper.Show("public-key")` | 5 |
| `internal/meshkey/meshkey.go:40` | `wg show <iface> peers` | `WGHelper.Show("peers")` | 5 |
| `internal/meshrekey/meshrekey.go:566` | `wg show <iface> peers` | `WGHelper.Show("peers")` | 5 |
| `internal/meshrekey/meshrekey.go:575` | `apply.WireGuardSync.Command` | `apply.SyncMesh` | 5 |
| `internal/meshrekey/meshrekey.go:585` | `wg show <iface> latest-handshakes` | `WGHelper.Show("latest-handshakes")` | 5 |
| `internal/siteadd/mesh.go:20` (run at `:125`) | `wg show <iface> latest-handshakes; echo "now $(date +%s)"` | `WGHelper.Show("latest-handshakes")`, then `date +%s` | 5 |
| `internal/siteremove/cluster.go:571` | `wg show <iface> peers` | `WGHelper.Show("peers")` | 5 |
| `internal/siteremove/evict.go:532` | `wg show <iface> latest-handshakes` | `WGHelper.Show("latest-handshakes")` | 5 |
| `internal/doctor/mesh.go:48` | advice: `systemctl status` and `journalctl -u` | advice: `sudo /usr/local/libexec/paisans/wg-psns-<token> log` | 6 |
| `internal/siteadd/siteadd.go:368`, `:398` | `ReadFile("/" + WireGuardConf())` | unchanged code; reads the deployment's own copy after Task 1 | 1 |
| `internal/registry/machine.go:44` | `ReadFile("/" + WireGuardConf())` | unchanged code; reads the copy after Task 1 | 1 |
| `internal/siteremove/host.go:133` | `systemctl disable --now wg-quick@psns-<token>` | **not changed here**: the helper has no verb that stops the interface; phase 6 rewrites `site remove`'s host step and phase 5's `host evict` stops the unit (see *Rulings*) | none |

Text only, no command run: `internal/siteadd/mesh.go:31`, `:131`, `:163`; `internal/apply/apply.go:293`, `:343`, `:407-411`, `:1090-1092`; `internal/meshrekey/meshrekey.go:17`, `:326`. The ones that tell an operator what to run are changed in Tasks 3, 5 and 6; the ones that describe what happens on the host (`wg syncconf` hands peers over in place) stay true, since the helper runs exactly that.

## Rulings

Where the index or spec was silent or the code contradicted it:

1. **The rendered mesh file moves into the deployment's directory.** The index lists the helper and the call sites but not where `apply` keeps the file. The deployment's user can neither write `/etc/wireguard` nor read the 600 file in it, and `apply` decides `Create`, `Unchanged`, `Conflict`, `sync` or `restart` by reading the current file. So `deployment.WireGuardConf()` becomes `srv/paisans/<token>/psns-<token>.conf` (directly under the root, so `deployment.SplitRel` names no stack), and a new `deployment.WireGuardInstalled()` is `/etc/wireguard/psns-<token>.conf`, where the helper installs it. The render's escape check then has no exception: nothing leaves the deployment's directory.
2. **A listen port of 0 means none.** `config.Site.ListenPort()` is 0 for a site nothing dials, and the renderer writes no `ListenPort` there (golden `home-a`). The helper rendered with port 0 refuses any `ListenPort`; rendered with a port, it requires `ListenPort` present and equal, since the firewall opens exactly that port.
3. **`AllowedIPs` must lie inside the mesh subnet.** The spec says "each value matched by a pattern"; for `AllowedIPs` the pattern is the subnet, because wg-quick adds a route for each entry, and `0.0.0.0/0` sets up policy routing that takes the host's whole traffic.
4. **Allowed keys and section headers are matched exactly; hook keys in any case.** wg-quick's own parser is case sensitive (`linux.bash`, `parse_options`), `wg` is not, so a key in another case is refused as unknown, and a hook in any case is refused as a hook.
5. **`MTU` is 1280 to 9000.** The renderer writes none; the spec allows the key, and the range is the IPv6 minimum to a jumbo frame.
6. **`siteremove/cluster.go` and `siteremove/evict.go` are included** though the index does not list `siteremove`: they read `wg show` on remaining sites as the deployment's user. `siteremove/host.go:133` stays: it stops the removed site's interface, which the helper cannot do. The spec's `host evict` list omits stopping `wg-quick@psns-<token>` and deleting `/etc/wireguard/psns-<token>.conf`; phase 5's plan must add both.
7. **`site add`'s one probe becomes two calls.** `wg show ... ; echo "now $(date +%s)"` cannot pass through a helper that takes only its verbs, so the gate reads `show latest-handshakes` and then `date +%s` on the same host.
8. **An unchanged file on an up interface is read back** (Task 4, not in the index). The helper splits writing the file from installing and syncing it, so a run that stops between them leaves a file `Build` would take as done. Reading the running key, peers and address closes that.
9. **Only the main golden tree changes.** `testdata/golden-local-storage` and `testdata/golden-open-signup` render no mesh file.
10. **`registry/machine.go` reads the copy.** Its fallback for a host with no registry entry reads the mesh file's `Address`; after Task 1 that is `/srv/paisans/<token>/psns-<token>.conf`, which root (in admit) and the deployment's user can both read.

## File Structure

| File | Change | Responsibility after the change |
|---|---|---|
| `internal/deployment/deployment.go` | modify | `WireGuardConf()` under the root; `WireGuardInstalled()` |
| `internal/render/templates/wireguard.conf.tmpl`, `internal/render/site.go`, `internal/render/plan.go` | modify | no relay `PostUp`; no file outside the root |
| `internal/render/testdata/golden/*/` | regenerate | the mesh file at `srv/paisans/f2a9/psns-f2a9.conf`, without `PostUp` |
| `internal/hostprep/snippets/wg-helper.sh.tmpl` | create | the helper |
| `internal/hostprep/wghelper.go` | create | `WGHelperScript` |
| `internal/hostprep/export_test.go` | create | `WGHelperScriptIn`, rendering with a test's own conf directory |
| `internal/hostprep/wghelper_test.go` | create | the helper run under `sh` against confs |
| `internal/apply/helper.go` | modify | errors carry the helper's output; `CommandLine` |
| `internal/apply/mesh.go` | modify | `handMesh`, `SyncMesh`, `helperProblem`, `runningStep`; the journal through `Log` |
| `internal/apply/apply.go` | modify | `Plan.meshConf`; `WireGuardStep.Command` removed; `execute` and `wireguardStep` through the helper |
| `internal/apply/scope.go` | modify | `Rollback` through the helper |
| `internal/meshkey/meshkey.go`, `internal/meshrekey/meshrekey.go`, `internal/siteadd/mesh.go`, `internal/siteremove/cluster.go`, `internal/siteremove/evict.go` | modify | reads and syncs through the helper |
| `internal/apply/wgcalls_test.go` | create | the source walk |
| `internal/doctor/mesh.go` | modify | the advice names the helper's `log` |
| `README.md`, `docs/development.md`, `CHANGELOG.md` | modify | current state |
| test fakes in `internal/apply`, `internal/meshrekey`, `internal/siteadd`, `internal/siteremove` | modify | answer the helper's verbs |

---

### Task 1: The mesh file is rendered inside the deployment's directory, without the relay `PostUp`

**Files:**
- Modify: `internal/deployment/deployment.go:119-124` (`WireGuardConf`), add `WireGuardInstalled` after it, `:138-142` (`SplitRel` comment)
- Modify: `internal/render/templates/wireguard.conf.tmpl:13-18`
- Modify: `internal/render/site.go:769-775` (comment), `:827-834` (template data)
- Modify: `internal/render/plan.go:181-190`
- Modify: `internal/render/stacks_test.go:94-96` (comment)
- Modify: `internal/apply/apply.go:544-546`, `:1638-1640` (comments)
- Modify: `internal/registry/machine.go:10-14` (comment)
- Modify: `internal/siteremove/host.go:933` (`hostGoneLeft`)
- Regenerate: `internal/render/testdata/golden/`
- Test: `internal/deployment/deployment_test.go:57`, `internal/render/render_test.go` (new test, and `:2619`), every `*_test.go` naming the old path (Step 6)
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: `deployment.Deployment.RelPath`, `Interface`.
- Produces: `func (d Deployment) WireGuardConf() string` returning `srv/paisans/<token>/psns-<token>.conf`; `func (d Deployment) WireGuardInstalled() string` returning `/etc/wireguard/psns-<token>.conf`. Task 2 renders the helper's conf path from `WireGuardInstalled`; Tasks 3 to 5 hand `WireGuardConf`'s content to the helper.

- [ ] **Step 1: Write the failing tests**

In `internal/deployment/deployment_test.go`, in the map at line 57, replace

```go
		d.WireGuardConf():         "etc/wireguard/psns-f2a9.conf",
```

with

```go
		d.WireGuardConf():         "srv/paisans/f2a9/psns-f2a9.conf",
		d.WireGuardInstalled():    "/etc/wireguard/psns-f2a9.conf",
```

In `internal/render/render_test.go`, after `TestGoldenTree`, add:

```go
// The mesh file is the deployment's own, under its root, where apply writes
// it as the deployment's user; the WireGuard helper installs it in
// /etc/wireguard. It carries no hook: the helper refuses PreUp, PostUp,
// PreDown and PostDown, since wg-quick runs them as root, and a relaying
// site's forwarding is the host's sysctl.
func TestTheMeshFileIsTheDeploymentsOwnAndRunsNothing(t *testing.T) {
	plan := build(t)
	d := fixture(t).Deployment()
	seen := 0
	for _, f := range plan.Files {
		site, rel, _ := strings.Cut(f.Path, "/")
		if !strings.HasSuffix(rel, ".conf") || !strings.Contains(rel, d.Interface()) {
			continue
		}
		seen++
		if rel != "srv/paisans/f2a9/psns-f2a9.conf" {
			t.Errorf("%s renders its mesh file at %s", site, rel)
		}
		for _, line := range strings.Split(f.Content, "\n") {
			key, _, _ := strings.Cut(strings.TrimSpace(line), "=")
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "preup", "postup", "predown", "postdown":
				t.Errorf("%s's mesh file runs a command: %q", site, line)
			}
		}
	}
	if seen != 4 {
		t.Errorf("%d mesh files rendered, want one for each of the fixture's four sites", seen)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/deployment/ -run TestNamesAndPathsDeriveFromTheToken`
Expected: FAIL to compile: `d.WireGuardInstalled undefined`.

Run: `go test ./internal/render/ -run TestTheMeshFileIsTheDeploymentsOwnAndRunsNothing`
Expected: FAIL with `home-a renders its mesh file at etc/wireguard/psns-f2a9.conf` for each of the four sites, and `home-b's mesh file runs a command: "PostUp = sysctl -w net.ipv4.ip_forward=1"` for `home-b`, `vm` and `watch`.

- [ ] **Step 3: Move the file and add the installed path**

In `internal/deployment/deployment.go`, replace lines 119-124:

```go
// WireGuardConf is the interface's wg-quick file, as a rendered path
// relative to the host's /: etc/wireguard/psns-<token>.conf. wg-quick names
// the interface after the file.
func (d Deployment) WireGuardConf() string {
	return "etc/wireguard/" + d.Interface() + ".conf"
}
```

with

```go
// WireGuardConf is the interface's wg-quick file as apply renders and writes
// it, relative to the host's /: srv/paisans/<token>/psns-<token>.conf. It is
// the deployment's own copy, in its own directory, because the deployment's
// user can neither write /etc/wireguard nor read the file there; apply hands
// it to the WireGuard helper, which checks it and installs it as
// WireGuardInstalled. It sits directly under the root, so it is no stack's.
func (d Deployment) WireGuardConf() string {
	return d.RelPath(d.Interface() + ".conf")
}

// WireGuardInstalled is where the WireGuard helper installs the interface's
// file for wg-quick, which names the interface after it:
// /etc/wireguard/psns-<token>.conf, root's, mode 600.
func (d Deployment) WireGuardInstalled() string {
	return "/etc/wireguard/" + d.Interface() + ".conf"
}
```

In the `SplitRel` comment (lines 138-141), replace `A path
// outside every root, such as etc/wireguard/psns-f2a9.conf, is not.` with `A path
// with no stack, such as srv/paisans/f2a9/psns-f2a9.conf, or outside every
// root, such as etc/hosts, is not.`

- [ ] **Step 4: Drop the relay `PostUp` and the render's exception**

In `internal/render/templates/wireguard.conf.tmpl`, delete lines 13-18:

```
{{- if .IsRelay }}
# This site has a stable endpoint, so it relays traffic between sites that
# have none: two sites behind NAT with changing addresses cannot dial each
# other.
PostUp = sysctl -w net.ipv4.ip_forward=1
{{- end }}
```

In `internal/render/site.go`, delete line 831 (`"IsRelay":    isRelay,`) and gofmt the map. Replace the doc comment's last sentences (lines 774-775), `Relaying keeps this to static configuration with no daemon and
// no coordination service, which is why plain WireGuard was chosen.`, with:

```go
// Relaying keeps this to static configuration with no daemon and no
// coordination service. The relay forwards with the host's
// net.ipv4.ip_forward, which host admit turns on for a site with an
// endpoint: the file carries no PostUp, because wg-quick runs one as root
// and the WireGuard helper refuses any.
```

In `internal/render/plan.go`, replace lines 181-190:

```go
	// Nothing leaves the deployment's own directory but the WireGuard file
	// wg-quick reads from /etc/wireguard: apply writes each file as root at
	// its path, so a path that walks out would be written anywhere.
	root, wg := cfg.Deployment().RelPath()+"/", cfg.Deployment().WireGuardConf()
	for _, f := range files {
		_, rel := splitSite(f.Path)
		if rel != path.Clean(rel) || path.IsAbs(rel) || (!strings.HasPrefix(rel, root) && rel != wg) {
```

with

```go
	// Nothing leaves the deployment's own directory, the mesh file included:
	// apply writes each file at its path as the deployment's user, and the
	// mesh file reaches /etc/wireguard only through the WireGuard helper,
	// which checks it first. A path that walks out is refused here rather
	// than written wherever it points.
	root := cfg.Deployment().RelPath() + "/"
	for _, f := range files {
		_, rel := splitSite(f.Path)
		if rel != path.Clean(rel) || path.IsAbs(rel) || !strings.HasPrefix(rel, root) {
```

In `internal/render/stacks_test.go`, replace the comment at lines 94-96 with:

```go
// Nothing the render writes leaves the deployment's root, the mesh file
// included; an app whose name walks out of the root is refused rather than
// written there.
```

- [ ] **Step 5: Comments and the one message that named the old path**

`internal/apply/apply.go:545`: `(Eg: etc/wireguard/psns-f2a9.conf)` becomes `(Eg: srv/paisans/f2a9/psns-f2a9.conf)`.
`internal/apply/apply.go:1640`: `` `etc/wireguard/psns-f2a9.conf` is not a stack at all. `` becomes `` `srv/paisans/f2a9/psns-f2a9.conf`, directly under the root, is not a stack at all. ``
`internal/registry/machine.go:13`: `interface psns-<token> and /etc/wireguard/psns-<token>.conf, and its one` becomes `interface psns-<token> and its mesh file /srv/paisans/<token>/psns-<token>.conf, and its one`.

In `internal/siteremove/host.go:933`, `hostGoneLeft` lists `%s and its manifest, %s and /%s` with `d.Root(), d.WireGuardUnit(), d.WireGuardConf()`. The mesh file is now under the root, already named; the host still holds the installed one. Change `%s and /%s` to `%s and %s` and `d.WireGuardConf()` to `d.WireGuardInstalled()` in that argument list.

- [ ] **Step 6: Every test that named the old path**

Every test fake treats the old path as "the file apply wrote", which is now the deployment's copy, so the rename is mechanical:

```bash
grep -rl 'etc/wireguard/psns-f2a9\.conf' --include='*_test.go' internal cmd \
  | xargs sed -i '' -e 's|/etc/wireguard/psns-f2a9\.conf|/srv/paisans/f2a9/psns-f2a9.conf|g' \
                    -e 's|etc/wireguard/psns-f2a9\.conf|srv/paisans/f2a9/psns-f2a9.conf|g'
```

(`sed -i ''` is BSD sed; on Linux use `sed -i`.) Then fix the two places the rename alone does not:

* `internal/deployment/deployment_test.go:58`: Step 1's `d.WireGuardInstalled(): "/etc/wireguard/psns-f2a9.conf"` was renamed by the sed; set it back to `"/etc/wireguard/psns-f2a9.conf"`.
* `internal/render/render_test.go:2619`: the list is sorted, and the mesh file now sorts after `infra/`. Set it to:

```go
	want := "srv/paisans/f2a9/infra/compose.yaml,srv/paisans/f2a9/infra/garage/garage.toml,srv/paisans/f2a9/infra/heartbeat/heartbeat.env,srv/paisans/f2a9/infra/heartbeat/push.sh,srv/paisans/f2a9/psns-f2a9.conf"
```

Check nothing names the old path any more: `grep -rn 'etc/wireguard' --include='*.go' internal cmd` prints only `deployment.go`'s `WireGuardInstalled` and `deployment_test.go:58`.

- [ ] **Step 7: Regenerate the golden tree and read its diff**

Run: `go test ./internal/render/ -run TestGoldenTree -update && git add -A internal/render/testdata/golden && git diff --cached -M --stat internal/render/testdata/golden && git diff --cached -M internal/render/testdata/golden`
Expected: four renames, `golden/<site>/etc/wireguard/psns-f2a9.conf` to `golden/<site>/srv/paisans/f2a9/psns-f2a9.conf` for `home-a`, `home-b`, `vm` and `watch`, and nothing else. Read the diff: `home-a`'s is a pure rename; `home-b`'s, `vm`'s and `watch`'s each remove exactly four lines, the three comment lines about relaying and `PostUp = sysctl -w net.ipv4.ip_forward=1`. Anything more is a render change this task did not intend; stop and find it.

- [ ] **Step 8: Run the whole suite**

Run: `go vet ./... && go test ./...`
Expected: PASS. A failure here is a test that indexed `Changes` or a sorted list by position with the mesh file first; fix it to the new order, as in Step 6, and name each such fix in the commit body.

- [ ] **Step 9: Changelog**

`CHANGELOG.md`, `[Unreleased]`, `### Changed`, add:

```markdown
- Each site's mesh file renders inside the deployment's directory, as `/srv/paisans/<token>/psns-<token>.conf`, and carries no `PostUp`: a relaying site's forwarding is the host's sysctl.
```

- [ ] **Step 10: Commit**

```bash
git add internal/deployment internal/render internal/apply internal/registry internal/siteremove internal/hostcheck internal/meshrekey internal/siteadd cmd CHANGELOG.md
git commit -m "feat: render the mesh file inside the deployment's directory

The deployment's user can neither write /etc/wireguard nor read the 600
file in it, and apply decides between create, update, sync and restart by
reading the file it wrote last. The rendered file therefore becomes the
deployment's own copy, /srv/paisans/<token>/psns-<token>.conf, which apply
reads and writes as that user, and the WireGuard helper installs it in
/etc/wireguard, where WireGuardInstalled names it. With that, nothing the
render writes leaves the deployment's directory, and the escape check has
no exception.

The relay's PostUp goes: wg-quick runs it as root, and the helper refuses
any hook. A site with an endpoint forwards with the host's ip_forward,
which host admit sets.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: The helper script and its renderer

**Files:**
- Create: `internal/hostprep/snippets/wg-helper.sh.tmpl`
- Create: `internal/hostprep/wghelper.go`
- Create: `internal/hostprep/export_test.go`
- Test: `internal/hostprep/wghelper_test.go`

**Interfaces:**
- Consumes: `apply.WGHelper{Iface}.Path() (string, error)` and `apply.HelperDir` (phase 2, `internal/apply/helper.go`); `deployment.Deployment.Interface`, `WireGuardUnit`, `WireGuardInstalled` (Task 1); `snippet(path string, data any) (string, error)` in `internal/hostprep/hostprep.go:422`.
- Produces: `func WGHelperScript(d deployment.Deployment, listenPort int, subnet string) (string, error)` in `internal/hostprep/wghelper.go`, for phase 5's `host admit` to install at `apply.WGHelper{Iface: d.Interface()}.Path()`, mode 755. `listenPort` is `config.Site.ListenPort()` (0 for a site nothing dials); `subnet` is `mesh.subnet`. Test seam: `hostprep.WGHelperScriptIn(dir string, d deployment.Deployment, listenPort int, subnet string) (string, error)`.

- [ ] **Step 1: Write the seam and the failing tests**

Create `internal/hostprep/export_test.go`:

```go
package hostprep

import "github.com/paisans-software/paisans-cli/internal/deployment"

// WGHelperScriptIn renders the WireGuard helper with its conf directory at
// dir in place of /etc/wireguard, so a test can run its put against a
// directory of its own.
func WGHelperScriptIn(dir string, d deployment.Deployment, listenPort int, subnet string) (string, error) {
	return wgHelperScript(dir, d, listenPort, subnet)
}
```

Create `internal/hostprep/wghelper_test.go`:

```go
package hostprep_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/render"
)

var meshDeployment = deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}

// goodConf is a mesh file as the renderer writes it for a dialled site on
// port 51820 in 10.44.0.0/24, with one peer it dials and one relayed range.
const goodConf = `# Rendered by paisans. Do not edit: ` + "`paisans apply`" + ` overwrites this file.
[Interface]
Address = 10.44.0.4/24
# The port of this site's endpoint, which every other site dials.
ListenPort = 51820
PrivateKey = QEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEA=

[Peer]
# home-b
PublicKey = ehpOcJvwhaxJSroEabmx7aCrH3ixaqu3n/7akGI+hSI=
AllowedIPs = 10.44.0.0/24
Endpoint = 198.51.100.2:51820
# Keeps the NAT mapping open from the side that dials out.
PersistentKeepalive = 25

[Peer]
# vm
PublicKey = EyxEK+AQ+9V+cmAzKKp25x/MwVA6riGTJ9FNnJmT9HI=
AllowedIPs = 10.44.0.3/32
Endpoint = vm.example.org:51820
PersistentKeepalive = 25
`

// shells are the shells the helper is run under: sh always, and dash and
// bash where this machine has them, since a host's /bin/sh is dash on
// Debian and Ubuntu.
func shells() []string {
	out := []string{"sh"}
	for _, s := range []string{"dash", "bash"} {
		if _, err := exec.LookPath(s); err == nil {
			out = append(out, s)
		}
	}
	return out
}

// helperIn renders the helper for the fixture deployment with port and
// subnet built in and a fresh conf directory of the test's, and returns the
// script's path and that directory.
func helperIn(t *testing.T, port int, subnet string) (script, dir string) {
	t.Helper()
	dir = t.TempDir()
	text, err := hostprep.WGHelperScriptIn(dir, meshDeployment, port, subnet)
	if err != nil {
		t.Fatal(err)
	}
	script = filepath.Join(t.TempDir(), "wg-psns-f2a9")
	if err := os.WriteFile(script, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, dir
}

// runHelper runs script under shell with args, stdin on its standard input,
// and returns what it printed and its exit status.
func runHelper(t *testing.T, shell, script, stdin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(shell, append([]string{script}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exit):
		return string(out), exit.ExitCode()
	}
	t.Fatalf("running %s %s: %v", shell, script, err)
	return "", -1
}

// entries lists dir, so a test can see that a refused put left nothing,
// not even its temporary file.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range list {
		out = append(out, e.Name())
	}
	return out
}

// The renderer's own output is what the helper exists to install: every
// site of the fixture, dialled or not, relaying or relayed, is accepted
// with its own listen port, and installed as written, mode 600.
func TestTheHelperInstallsWhatTheRendererWrites(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets(filepath.Join("..", "render", "testdata", "secrets.fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Deployment()
	seen := 0
	for _, f := range plan.Files {
		site, rel, _ := strings.Cut(f.Path, "/")
		if rel != d.WireGuardConf() {
			continue
		}
		seen++
		for _, shell := range shells() {
			script, dir := helperIn(t, cfg.Sites[site].ListenPort(), cfg.Mesh.Subnet)
			if out, code := runHelper(t, shell, script, f.Content, "put"); code != 0 {
				t.Fatalf("%s: %s refused %s's mesh file (exit %d):\n%s\n%s", shell, script, site, code, out, f.Content)
			}
			conf := filepath.Join(dir, "psns-f2a9.conf")
			got, err := os.ReadFile(conf)
			if err != nil || string(got) != f.Content {
				t.Errorf("%s: %s's mesh file was not installed as written: %v", shell, site, err)
			}
			if info, err := os.Lstat(conf); err != nil || info.Mode() != 0o600 {
				t.Errorf("%s: %s's installed file has mode %v, want -rw-------", shell, site, info.Mode())
			}
			if names := entries(t, dir); len(names) != 1 {
				t.Errorf("%s: put left %v in the directory", shell, names)
			}
		}
	}
	if seen != 4 {
		t.Fatalf("%d mesh files rendered, want four", seen)
	}
}

// Keys and values the renderer can write but the fixture does not are
// accepted too.
func TestTheHelperAcceptsEveryKeyTheRendererMayWrite(t *testing.T) {
	conf := strings.Replace(goodConf, "PrivateKey = ", "MTU = 1420\nPrivateKey = ", 1)
	conf = strings.Replace(conf, "Endpoint = vm.example.org:51820", "Endpoint = [2001:db8::3]:51820\nPresharedKey = ze/YeDqRtEZkDi4flVmds15ISgBxvSGCs7YNCBLBDHA=", 1)
	conf = strings.Replace(conf, "AllowedIPs = 10.44.0.3/32", "AllowedIPs = 10.44.0.3/32, 10.44.0.128/25", 1)
	conf = "\n  # an indented comment\n\t\n" + conf
	for _, shell := range shells() {
		script, _ := helperIn(t, 51820, "10.44.0.0/24")
		if out, code := runHelper(t, shell, script, conf, "put"); code != 0 {
			t.Errorf("%s: refused (exit %d): %s", shell, code, out)
		}
	}
}

// Every way a conf could run a command as root, or reach past the port and
// subnet the owner admitted, refuses the whole file: exit 65, a line saying
// why, and nothing in the directory.
func TestTheHelperRefusesAnythingElse(t *testing.T) {
	replace := func(old, new string) string { return strings.Replace(goodConf, old, new, 1) }
	keepalive := "PersistentKeepalive = 25\n"
	for _, c := range []struct {
		name, conf, want string
	}{
		{"PostUp in [Interface]", replace("PrivateKey = ", "PostUp = id\nPrivateKey = "), "runs a command as root"},
		{"PostUp in a [Peer]", replace(keepalive, "PostUp = id\n"), "runs a command as root"},
		{"PreUp", replace(keepalive, "PreUp = id\n"), "runs a command as root"},
		{"PreDown", replace(keepalive, "PreDown = id\n"), "runs a command as root"},
		{"PostDown", replace(keepalive, "PostDown = id\n"), "runs a command as root"},
		{"a hook in lower case", replace(keepalive, "postup = id\n"), "runs a command as root"},
		{"a hook in upper case", replace(keepalive, "POSTUP = id\n"), "runs a command as root"},
		{"a hook behind spaces", replace(keepalive, "    PostUp = id\n"), "runs a command as root"},
		{"a hook behind a tab", replace(keepalive, "\tPostUp = id\n"), "runs a command as root"},
		{"a hook with no spaces", replace(keepalive, "PostUp=id\n"), "runs a command as root"},
		{"a hook with spaces before =", replace(keepalive, "PostUp \t = id\n"), "runs a command as root"},
		{"a hook before any section", "PostUp = id\n" + goodConf, "runs a command as root"},
		{"a line with no =", replace(keepalive, "PostUp\n"), "is not key = value"},
		{"DNS", replace("PrivateKey = ", "DNS = 1.1.1.1\nPrivateKey = "), "is not a key paisans writes in [Interface]"},
		{"Table", replace("PrivateKey = ", "Table = off\nPrivateKey = "), "is not a key paisans writes"},
		{"SaveConfig", replace("PrivateKey = ", "SaveConfig = true\nPrivateKey = "), "is not a key paisans writes"},
		{"FwMark", replace("PrivateKey = ", "FwMark = 51820\nPrivateKey = "), "is not a key paisans writes"},
		{"an [Interface] key in a [Peer]", replace(keepalive, "MTU = 1420\n"), "is not a key paisans writes in [Peer]"},
		{"a known key in another case", replace("Address = ", "address = "), "is not a key paisans writes"},
		{"a section in another case", replace("[Peer]", "[peer]"), "is not [Interface] or [Peer]"},
		{"another section", replace("[Peer]", "[Match]"), "is not [Interface] or [Peer]"},
		{"a second [Interface]", goodConf + "\n[Interface]\n", "a second [Interface]"},
		{"a key before any section", "Address = 10.44.0.4/24\n" + goodConf, "before any section"},
		{"a second Address", replace("PrivateKey = ", "Address = 10.44.0.5/24\nPrivateKey = "), "a second Address"},
		{"a ListenPort other than the admitted one", replace("ListenPort = 51820", "ListenPort = 51821"), "admitted on 51820"},
		{"no ListenPort on a host admitted with one", replace("ListenPort = 51820\n", ""), "no ListenPort"},
		{"an Address outside the subnet", replace("Address = 10.44.0.4/24", "Address = 10.45.0.4/24"), "is not one address in 10.44.0.0/24"},
		{"an Address with another prefix", replace("Address = 10.44.0.4/24", "Address = 10.44.0.4/16"), "is not one address in 10.44.0.0/24"},
		{"two Addresses on one line", replace("Address = 10.44.0.4/24", "Address = 10.44.0.4/24, 10.44.0.5/24"), "is not one address"},
		{"an IPv6 Address", replace("Address = 10.44.0.4/24", "Address = fd00::4/64"), "is not one address"},
		{"an octet over 255", replace("Address = 10.44.0.4/24", "Address = 10.44.0.256/24"), "is not one address"},
		{"an octet with a leading zero", replace("Address = 10.44.0.4/24", "Address = 10.44.0.04/24"), "is not one address"},
		{"AllowedIPs everywhere", replace("AllowedIPs = 10.44.0.3/32", "AllowedIPs = 0.0.0.0/0"), "is not inside 10.44.0.0/24"},
		{"AllowedIPs wider than the mesh", replace("AllowedIPs = 10.44.0.3/32", "AllowedIPs = 10.44.0.0/16"), "is not inside 10.44.0.0/24"},
		{"AllowedIPs on the owner's LAN", replace("AllowedIPs = 10.44.0.3/32", "AllowedIPs = 10.44.0.3/32, 192.168.1.0/24"), "192.168.1.0/24 is not inside"},
		{"an Endpoint carrying a command", replace("Endpoint = vm.example.org:51820", "Endpoint = $(id):51820"), "is not host:port"},
		{"an Endpoint with no port", replace("Endpoint = vm.example.org:51820", "Endpoint = vm.example.org"), "is not host:port"},
		{"an Endpoint on port 0", replace("Endpoint = vm.example.org:51820", "Endpoint = vm.example.org:0"), "is not host:port"},
		{"a PrivateKey that is not a key", replace("PrivateKey = QEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEA=", "PrivateKey = x"), "PrivateKey is not a WireGuard key"},
		{"a PublicKey that is not a key", replace("PublicKey = EyxEK+AQ+9V+cmAzKKp25x/MwVA6riGTJ9FNnJmT9HI=", "PublicKey = EyxEK+AQ+9V+cmAzKKp25x/MwVA6riGTJ9FNnJmT9HI"), "PublicKey is not a WireGuard key"},
		{"a value with a trailing comment", replace(keepalive, "PersistentKeepalive = 25 # keep\n"), "PersistentKeepalive"},
		{"an MTU out of range", replace("PrivateKey = ", "MTU = 100\nPrivateKey = "), "MTU 100 is not from 1280 to 9000"},
		{"a [Peer] without PublicKey", strings.Replace(goodConf, "PublicKey = EyxEK+AQ+9V+cmAzKKp25x/MwVA6riGTJ9FNnJmT9HI=\n", "", 1), "the last [Peer] has no PublicKey"},
		{"CRLF line endings", strings.ReplaceAll(goodConf, "\n", "\r\n"), "carriage return"},
		{"a NUL byte", replace("[Peer]", "[Peer]\x00"), "NUL byte"},
		{"a conf over 64 KiB", goodConf + strings.Repeat("# padding\n", 7000), "larger than 65536 bytes"},
		{"nothing", "", "no [Interface]"},
	} {
		for _, shell := range shells() {
			script, dir := helperIn(t, 51820, "10.44.0.0/24")
			out, code := runHelper(t, shell, script, c.conf, "put")
			if code != 65 || !strings.Contains(out, c.want) || !strings.Contains(out, "nothing was installed") {
				t.Errorf("%s, %s: exit %d, %q; want 65 and %q", c.name, shell, code, out, c.want)
			}
			if names := entries(t, dir); len(names) != 0 {
				t.Errorf("%s, %s: a refused put left %v", c.name, shell, names)
			}
		}
	}
}

// A site nothing dials has no fixed port: rendered with port 0, the helper
// refuses any ListenPort and accepts a conf without one.
func TestAHostAdmittedWithNoPortTakesNoListenPort(t *testing.T) {
	for _, shell := range shells() {
		script, _ := helperIn(t, 0, "10.44.0.0/24")
		if out, code := runHelper(t, shell, script, goodConf, "put"); code != 65 || !strings.Contains(out, "admitted with none") {
			t.Errorf("%s: a ListenPort on a host admitted with none: exit %d, %q", shell, code, out)
		}
		without := strings.Replace(goodConf, "ListenPort = 51820\n", "", 1)
		if out, code := runHelper(t, shell, script, without, "put"); code != 0 {
			t.Errorf("%s: a conf with no ListenPort was refused: %d, %q", shell, code, out)
		}
	}
}

// The conf's path is replaced by the rename, never written through: a link
// planted there to a file leaves that file as it was, and a link to a
// directory, or a directory, is refused rather than moved into.
func TestPutNeverWritesThroughALink(t *testing.T) {
	for _, shell := range shells() {
		script, dir := helperIn(t, 51820, "10.44.0.0/24")
		conf := filepath.Join(dir, "psns-f2a9.conf")
		target := filepath.Join(t.TempDir(), "shadow")
		if err := os.WriteFile(target, []byte("root's\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, conf); err != nil {
			t.Fatal(err)
		}
		if out, code := runHelper(t, shell, script, goodConf, "put"); code != 0 {
			t.Fatalf("%s: exit %d: %s", shell, code, out)
		}
		if got, _ := os.ReadFile(target); string(got) != "root's\n" {
			t.Errorf("%s: put wrote through the link: %q", shell, got)
		}
		if info, err := os.Lstat(conf); err != nil || !info.Mode().IsRegular() {
			t.Errorf("%s: the link was not replaced by a file: %v", shell, err)
		}

		script, dir = helperIn(t, 51820, "10.44.0.0/24")
		elsewhere := t.TempDir()
		if err := os.Symlink(elsewhere, filepath.Join(dir, "psns-f2a9.conf")); err != nil {
			t.Fatal(err)
		}
		if out, code := runHelper(t, shell, script, goodConf, "put"); code != 65 || !strings.Contains(out, "is a directory") {
			t.Errorf("%s: a link to a directory: exit %d, %q", shell, code, out)
		}
		if names := entries(t, elsewhere); len(names) != 0 {
			t.Errorf("%s: put wrote %v into the linked directory", shell, names)
		}
	}
}

// A refused put leaves the conf already installed exactly as it was.
func TestARefusedPutLeavesTheInstalledFile(t *testing.T) {
	for _, shell := range shells() {
		script, dir := helperIn(t, 51820, "10.44.0.0/24")
		if _, code := runHelper(t, shell, script, goodConf, "put"); code != 0 {
			t.Fatal("the good conf was refused")
		}
		bad := strings.Replace(goodConf, "PersistentKeepalive = 25\n", "PostUp = id\n", 1)
		if _, code := runHelper(t, shell, script, bad, "put"); code != 65 {
			t.Fatalf("%s: the bad conf was not refused", shell)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "psns-f2a9.conf")); string(got) != goodConf {
			t.Errorf("%s: a refused put changed the installed conf", shell)
		}
	}
}

// The conf directory must be a directory of its own, not a link to one.
func TestPutRefusesALinkedConfDirectory(t *testing.T) {
	real := t.TempDir()
	linked := filepath.Join(t.TempDir(), "wireguard")
	if err := os.Symlink(real, linked); err != nil {
		t.Fatal(err)
	}
	text, err := hostprep.WGHelperScriptIn(linked, meshDeployment, 51820, "10.44.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "wg-psns-f2a9")
	if err := os.WriteFile(script, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, code := runHelper(t, "sh", script, goodConf, "put"); code != 65 || !strings.Contains(out, "is not a directory") {
		t.Errorf("exit %d, %q", code, out)
	}
	if names := entries(t, real); len(names) != 0 {
		t.Errorf("put wrote %v through the linked directory", names)
	}
}

// The verbs are the only arguments the helper takes: anything else exits
// 64 before running a command or touching the directory.
func TestTheHelperTakesOnlyItsVerbs(t *testing.T) {
	for _, args := range [][]string{
		{}, {"bogus"}, {"put", "extra"}, {"up", "now"}, {"restart", "-f"}, {"sync", "x"},
		{"show"}, {"show", "dump"}, {"show", "private-key"}, {"show", "peers", "extra"}, {"log", "-n", "1000"},
	} {
		for _, shell := range shells() {
			script, dir := helperIn(t, 51820, "10.44.0.0/24")
			out, code := runHelper(t, shell, script, goodConf, args...)
			if code != 64 || !strings.Contains(out, "usage:") {
				t.Errorf("%s %v: exit %d, %q; want 64 and the usage", shell, args, code, out)
			}
			if names := entries(t, dir); len(names) != 0 {
				t.Errorf("%s %v: left %v", shell, args, names)
			}
		}
	}
}

// The values the helper enforces are built in when it is rendered, from
// the deployment and what the owner admits, and nothing the caller passes
// can change them.
func TestTheHelperIsRenderedWithItsValuesBuiltIn(t *testing.T) {
	text, err := hostprep.WGHelperScript(meshDeployment, 51820, "10.44.0.7/24")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"iface='psns-f2a9'\n", "dir='/etc/wireguard'\n", "unit='wg-quick@psns-f2a9'\n",
		"port='51820'\n", "net='10.44.0.0'\n", "prefix='24'\n", "/usr/local/libexec/paisans/wg-psns-f2a9",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the rendered helper lacks %q", want)
		}
	}
	if !strings.HasPrefix(text, "#!/bin/sh\n") {
		t.Error("the helper does not start with #!/bin/sh")
	}
}

// What the helper cannot be rendered for is refused at render time.
func TestTheHelperRefusesBadValues(t *testing.T) {
	for name, build := range map[string]func() (string, error){
		"an interface that is no deployment's": func() (string, error) {
			return hostprep.WGHelperScript(deployment.Deployment{ID: "zzzz-not-hex"}, 51820, "10.44.0.0/24")
		},
		"an IPv6 subnet":      func() (string, error) { return hostprep.WGHelperScript(meshDeployment, 51820, "fd00::/64") },
		"a subnet with no prefix": func() (string, error) { return hostprep.WGHelperScript(meshDeployment, 51820, "10.44.0.0") },
		"the whole IPv4 space": func() (string, error) { return hostprep.WGHelperScript(meshDeployment, 51820, "0.0.0.0/0") },
		"a negative port":     func() (string, error) { return hostprep.WGHelperScript(meshDeployment, -1, "10.44.0.0/24") },
		"a port over 65535":   func() (string, error) { return hostprep.WGHelperScript(meshDeployment, 65536, "10.44.0.0/24") },
		"a relative conf directory": func() (string, error) {
			return hostprep.WGHelperScriptIn("etc/wireguard", meshDeployment, 51820, "10.44.0.0/24")
		},
		"a conf directory with a quote": func() (string, error) {
			return hostprep.WGHelperScriptIn("/tmp/it's", meshDeployment, 51820, "10.44.0.0/24")
		},
	} {
		if _, err := build(); err == nil {
			t.Errorf("%s was rendered", name)
		}
	}
}
```

Run `gofmt -w internal/hostprep/wghelper_test.go` after pasting; the map literal in the last test aligns on gofmt.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/hostprep/ -run 'Helper|Put|AHostAdmitted'`
Expected: FAIL to compile: `undefined: wgHelperScript` in `export_test.go` and `undefined: hostprep.WGHelperScript`.

- [ ] **Step 3: Write the helper**

Create `internal/hostprep/snippets/wg-helper.sh.tmpl`:

```sh
#!/bin/sh
# The WireGuard helper for {{.Interface}}, rendered by paisans with this
# deployment's interface, listen port and mesh subnet built in. paisans host
# admit installs it as {{.Path}}, root's and mode 755, with one sudoers line
# that lets the deployment's user run it, and nothing else, as root.
#
# Creating {{.Interface}}, installing its conf and syncing its peers need
# CAP_NET_ADMIN in the host's network namespace, which the deployment's user
# does not have. wg-quick runs a conf's PreUp, PostUp, PreDown and PostDown as
# root, so a helper that installed any conf it was given would hand root to
# its caller. put therefore installs only a conf made of the keys paisans
# renders, each value matching its pattern, the ListenPort the owner admitted
# and addresses inside the mesh subnet; anything else refuses the whole file
# and installs nothing.
#
# The verbs are the only arguments it takes:
#   put           read a conf on stdin, check it, install it as {{.Conf}}, 600
#   up            systemctl enable --now {{.Unit}}
#   restart       systemctl restart {{.Unit}}
#   sync          wg-quick strip the installed conf, then wg syncconf
#   show FIELD    wg show {{.Interface}} FIELD: peers, latest-handshakes or public-key
#   log           the unit's last 200 journal lines
# A wrong verb or argument count exits 64; a refused conf exits 65.
set -eu
PATH=/usr/sbin:/usr/bin:/sbin:/bin
LC_ALL=C
export PATH LC_ALL
umask 077

iface='{{.Interface}}'
dir='{{.Dir}}'
conf="$dir/$iface.conf"
unit='{{.Unit}}'
# The admitted ListenPort, 0 for a site nothing dials, whose conf has none.
port='{{.ListenPort}}'
# The mesh subnet, as its network address and prefix length.
net='{{.Network}}'
prefix='{{.Prefix}}'
# The largest conf put reads. A mesh of a hundred sites is under 20 KiB.
max=65536

usage() {
	echo "usage: $0 put | up | restart | sync | show peers|latest-handshakes|public-key | log" >&2
	exit 64
}

refuse() {
	echo "wg-$iface: $1; nothing was installed" >&2
	exit 65
}

# check reads a conf on stdin and prints why it is refused, exiting 1, or
# prints nothing and exits 0. Keys and section names are matched exactly, as
# the renderer writes them; a hook key is refused in any section, in any case
# and with any whitespace around it. It is POSIX awk only, with no regular
# expression intervals, for mawk, gawk and BSD awk alike.
check() {
	awk -v port="$port" -v net="$net" -v prefix="$prefix" '
	function why(msg) { if (!failed) reason = msg; failed = 1 }
	function no(msg) { why("line " NR ": " msg); exit 1 }
	function ip4(s,    a, n, i, v) {
		n = split(s, a, ".")
		if (n != 4) return -1
		v = 0
		for (i = 1; i <= 4; i++) {
			if (a[i] !~ /^(0|[1-9][0-9]?[0-9]?)$/ || a[i] + 0 > 255) return -1
			v = v * 256 + a[i]
		}
		return v
	}
	# cidr parses address/prefix into cv and cp, false when it is not one.
	function cidr(s,    a) {
		if (split(s, a, "/") != 2 || a[2] !~ /^(0|[1-9][0-9]?)$/ || a[2] + 0 > 32) return 0
		cp = a[2] + 0
		cv = ip4(a[1])
		return cv >= 0
	}
	# inside is whether the parsed cv/cp lies inside the mesh subnet.
	function inside(    d) {
		if (cp < prefix) return 0
		d = 2 ^ (32 - prefix)
		return int(cv / d) == int(netv / d)
	}
	function key(s) { return length(s) == 44 && s ~ /^[A-Za-z0-9+\/]+=$/ }
	function num(s, lo, hi) { return s ~ /^(0|[1-9][0-9]*)$/ && length(s) <= 5 && s + 0 >= lo && s + 0 <= hi }
	BEGIN {
		netv = ip4(net)
		if (netv < 0 || !num(prefix, 1, 32) || !num(port, 0, 65535)) { why("the helper was rendered with a bad subnet or port"); exit 1 }
	}
	/\r/ { no("a carriage return; write the conf with Unix line endings") }
	/^[ \t]*$/ { next }
	/^[ \t]*#/ { next }
	{
		line = $0
		sub(/^[ \t]+/, "", line)
		sub(/[ \t]+$/, "", line)
		if (substr(line, 1, 1) == "[") {
			if (line == "[Interface]") {
				if (interfaces || peers) no("a second [Interface], or one after a [Peer]")
				interfaces = 1; section = "Interface"
			} else if (line == "[Peer]") {
				if (!interfaces) no("[Peer] before [Interface]")
				if (section == "Peer" && !pub) no("the [Peer] before this one has no PublicKey")
				peers++; pub = 0; section = "Peer"
			} else {
				no("section " line " is not [Interface] or [Peer]")
			}
			next
		}
		eq = index(line, "=")
		if (eq == 0) no("\"" line "\" is not key = value")
		k = substr(line, 1, eq - 1)
		v = substr(line, eq + 1)
		sub(/[ \t]+$/, "", k)
		sub(/^[ \t]+/, "", v)
		lk = tolower(k)
		if (lk == "preup" || lk == "postup" || lk == "predown" || lk == "postdown") no(k " runs a command as root, and no conf may carry one")
		if (section == "") no(k " before any section")
		if (seen[peers, k]++) no("a second " k " in one section")
		if (section == "Interface") {
			if (k == "Address") {
				if (!cidr(v) || cp != prefix || !inside()) no("Address " v " is not one address in " net "/" prefix)
				address = 1
			} else if (k == "PrivateKey") {
				if (!key(v)) no("PrivateKey is not a WireGuard key")
				private = 1
			} else if (k == "ListenPort") {
				if (port == 0) no("ListenPort " v ", but this deployment was admitted with none on this host")
				if (v != port) no("ListenPort " v ", but this deployment was admitted on " port)
				listen = 1
			} else if (k == "MTU") {
				if (!num(v, 1280, 9000)) no("MTU " v " is not from 1280 to 9000")
			} else {
				no(k " is not a key paisans writes in [Interface]")
			}
		} else {
			if (k == "PublicKey") {
				if (!key(v)) no("PublicKey is not a WireGuard key")
				pub = 1
			} else if (k == "PresharedKey") {
				if (!key(v)) no("PresharedKey is not a WireGuard key")
			} else if (k == "AllowedIPs") {
				n = split(v, ips, ",")
				for (i = 1; i <= n; i++) {
					s = ips[i]
					sub(/^[ \t]+/, "", s)
					sub(/[ \t]+$/, "", s)
					if (!cidr(s) || !inside()) no("AllowedIPs " s " is not inside " net "/" prefix)
				}
			} else if (k == "Endpoint") {
				p = v
				sub(/^.*:/, "", p)
				if (v !~ /^([A-Za-z0-9][A-Za-z0-9.-]*|\[[0-9A-Fa-f:.]+\]):[0-9]+$/ || !num(p, 1, 65535)) no("Endpoint " v " is not host:port")
			} else if (k == "PersistentKeepalive") {
				if (!num(v, 0, 65535)) no("PersistentKeepalive " v " is not from 0 to 65535")
			} else {
				no(k " is not a key paisans writes in [Peer]")
			}
		}
	}
	END {
		if (!failed) {
			if (!interfaces) why("no [Interface]")
			else if (!address) why("no Address")
			else if (!private) why("no PrivateKey")
			else if (port != 0 && !listen) why("no ListenPort, but this deployment was admitted on " port)
			else if (section == "Peer" && !pub) why("the last [Peer] has no PublicKey")
		}
		if (failed) { print reason; exit 1 }
	}'
}

put() {
	if [ ! -d "$dir" ] || [ -L "$dir" ]; then
		refuse "$dir is not a directory"
	fi
	# -d follows a link, so a link to a directory is refused here too. A link
	# to anything else is replaced by the rename below, never written through.
	if [ -d "$conf" ]; then
		refuse "$conf is a directory"
	fi
	tmp=$(mktemp "$dir/.$iface.conf.XXXXXX")
	trap 'rm -f "$tmp"' EXIT
	head -c $((max + 1)) > "$tmp"
	size=$(($(wc -c < "$tmp")))
	if [ "$size" -gt "$max" ]; then
		refuse "the conf is larger than $max bytes"
	fi
	if [ "$(($(tr -d '\000' < "$tmp" | wc -c)))" -ne "$size" ]; then
		refuse "the conf has a NUL byte"
	fi
	if ! reason=$(check < "$tmp"); then
		refuse "$reason"
	fi
	chmod 600 "$tmp"
	mv -f "$tmp" "$conf"
	trap - EXIT
}

case "${1-}" in
put)
	[ $# -eq 1 ] || usage
	put
	;;
up)
	[ $# -eq 1 ] || usage
	exec systemctl enable --now "$unit"
	;;
restart)
	[ $# -eq 1 ] || usage
	exec systemctl restart "$unit"
	;;
sync)
	[ $# -eq 1 ] || usage
	# Through a file rather than a pipe: sh has no pipefail, so a strip that
	# failed would hand syncconf an empty conf, and syncconf removes every
	# peer it is not given.
	stripped=$(mktemp)
	trap 'rm -f "$stripped"' EXIT
	wg-quick strip "$conf" > "$stripped"
	wg syncconf "$iface" "$stripped"
	;;
show)
	[ $# -eq 2 ] || usage
	case "$2" in
	peers | latest-handshakes | public-key) exec wg show "$iface" "$2" ;;
	*) usage ;;
	esac
	;;
log)
	[ $# -eq 1 ] || usage
	exec journalctl -u "$unit" -n 200 --no-pager
	;;
*)
	usage
	;;
esac
```

Create `internal/hostprep/wghelper.go`:

```go
package hostprep

import (
	"fmt"
	"net"
	"path"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// wgHelperData is what snippets/wg-helper.sh.tmpl is rendered with.
type wgHelperData struct {
	Interface, Path, Conf, Unit, Dir, Network string
	ListenPort, Prefix                         int
}

// WGHelperScript is the WireGuard helper host admit installs for d at
// apply.WGHelper's Path, mode 755: a POSIX sh script whose put checks a conf
// and installs it as d.WireGuardInstalled, and whose other verbs start,
// restart, sync, show and read the journal of d's interface. The interface,
// the listen port the owner admits (0 for a site nothing dials) and the mesh
// subnet are built in, so nothing the deployment's user passes it can widen
// them.
func WGHelperScript(d deployment.Deployment, listenPort int, subnet string) (string, error) {
	return wgHelperScript(path.Dir(d.WireGuardInstalled()), d, listenPort, subnet)
}

func wgHelperScript(dir string, d deployment.Deployment, listenPort int, subnet string) (string, error) {
	iface := d.Interface()
	helper, err := apply.WGHelper{Iface: iface}.Path()
	if err != nil {
		return "", err
	}
	if listenPort < 0 || listenPort > 65535 {
		return "", fmt.Errorf("the WireGuard helper for %s: listen port %d is not from 0 to 65535", iface, listenPort)
	}
	_, network, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", fmt.Errorf("the WireGuard helper for %s: mesh subnet %q is not a network: %w", iface, subnet, err)
	}
	prefix, bits := network.Mask.Size()
	if bits != 32 || prefix < 1 {
		return "", fmt.Errorf("the WireGuard helper for %s: mesh subnet %q is not an IPv4 network with a prefix", iface, subnet)
	}
	if !path.IsAbs(dir) || strings.ContainsAny(dir, "'\n") {
		return "", fmt.Errorf("the WireGuard helper for %s: %q cannot be its conf directory", iface, dir)
	}
	return snippet("snippets/wg-helper.sh.tmpl", wgHelperData{
		Interface:  iface,
		Path:       helper,
		Conf:       dir + "/" + iface + ".conf",
		Unit:       d.WireGuardUnit(),
		Dir:        dir,
		Network:    network.IP.String(),
		ListenPort: listenPort,
		Prefix:     prefix,
	})
}
```

`hostprep` importing `apply` adds no cycle: `go list -deps ./internal/apply` does not list `hostprep`.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./internal/hostprep/ -run 'Helper|Put|AHostAdmitted' -v 2>&1 | tail -20`
Expected: PASS for all ten tests. If `dash` is on the machine, each table runs under it too.

- [ ] **Step 5: Run the whole suite**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

No `CHANGELOG.md` bullet: nothing installs or runs the helper until `host admit` (phase 5), so an operator sees no change.

```bash
git add internal/hostprep
git commit -m "feat: render the wireguard helper for a deployment's interface

The mesh is the one thing a deployment needs root for after admission:
creating its interface, installing its conf and syncing its peers need
CAP_NET_ADMIN in the host's namespace. host admit will install this script
for each deployment, with the interface, the admitted listen port and the
mesh subnet built in, and one sudoers line for it. wg-quick runs a conf's
hooks as root, so put checks every line with awk and refuses the whole file
on a hook in any section or case, an unknown key, another port, or an
address or route outside the subnet, then installs it 600 through a temp
file and a rename, never through a link. The tests run the rendered script
under sh, and dash and bash where present, against a table of confs and the
renderer's own output.

No changelog bullet: nothing installs or runs the helper yet.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: `apply` reaches the mesh through the helper

**Files:**
- Modify: `internal/apply/helper.go` (errors carry output; `CommandLine`)
- Modify: `internal/apply/apply.go:186-188` (add `meshConf`), `:280-316` (`WireGuardStep` comment and `Command` removed), `:799-805` (record `meshConf`), `:1209-1226` (`execute`)
- Modify: `internal/apply/mesh.go:31-44` (`bringUpMesh`), `:103-148` (`meshProblem`), add `handMesh`, `SyncMesh`, `helperProblem`, `sudoRefusal`, `refusedConf`, `journalTail`
- Modify: `internal/apply/scope.go:17-54` (`Rollback`)
- Test: `internal/apply/helper_test.go`, `internal/apply/apply_test.go`, `internal/apply/scope_test.go`, `internal/apply/meshresolve_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: `apply.WGHelper` (phase 2); `deployment.Deployment.WireGuardConf` (Task 1).
- Produces: `func (h WGHelper) CommandLine(verb string) string` (eg `sudo /usr/local/libexec/paisans/wg-psns-f2a9 log`, for a message; Tasks 5 and 6 use it); `func SyncMesh(plan *Plan, t Transport) error` (Task 5's `meshrekey`); `func handMesh(plan *Plan, t Transport, step WireGuardStep) error`, `func helperProblem(site string, h WGHelper, err error) error` (Task 4). `WireGuardStep.Command` is removed; `Title` and `Describe` stay.

- [ ] **Step 1: Write the failing helper tests**

Append to `internal/apply/helper_test.go`:

```go
// failingHost answers every command with out and a bare exit status, as a
// transport that does not quote the output in its error would.
type failingHost struct{ helperHost }

func (h *failingHost) Run(command string) (string, error) {
	h.ran = append(h.ran, command)
	return h.out, errors.New("exit status 1")
}

func (h *failingHost) RunInput(command, stdin string) (string, error) { return h.Run(command) }

// What the helper or sudo printed is why it failed, so every verb's error
// carries it, whether or not the transport quoted it.
func TestWGHelperErrorsCarryWhatTheHelperPrinted(t *testing.T) {
	h := &failingHost{helperHost{out: "sudo: a password is required\n"}}
	w := apply.WGHelper{T: h, Iface: "psns-f2a9"}
	for name, call := range map[string]func() error{
		"Put":     func() error { return w.Put([]byte("x")) },
		"Up":      w.Up,
		"Restart": w.Restart,
		"Sync":    w.Sync,
		"Show":    func() error { _, err := w.Show("peers"); return err },
		"Log":     func() error { _, err := w.Log(); return err },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "sudo: a password is required") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// An error that already quotes the output does not quote it twice.
func TestWGHelperErrorsQuoteTheOutputOnce(t *testing.T) {
	h := &quotingHost{out: "wg-psns-f2a9: line 3: no; nothing was installed\n"}
	err := apply.WGHelper{T: h, Iface: "psns-f2a9"}.Up()
	if err == nil || strings.Count(err.Error(), "nothing was installed") != 1 {
		t.Errorf("%v", err)
	}
}

// quotingHost fails with its output in the error, as SSHTransport does.
type quotingHost struct{ helperHost }

func (h *quotingHost) Run(command string) (string, error) {
	return h.out, fmt.Errorf("home-a: %s: exit status 65\n%s", command, h.out)
}

// CommandLine is what an operator types as the deployment's user, for a
// message: sudo, the helper's path, unquoted, and the verb.
func TestWGHelperCommandLine(t *testing.T) {
	if got := (apply.WGHelper{Iface: "psns-f2a9"}).CommandLine("log"); got != "sudo /usr/local/libexec/paisans/wg-psns-f2a9 log" {
		t.Errorf("CommandLine = %q", got)
	}
	if got := (apply.WGHelper{Iface: "wg0"}).CommandLine("log"); got != "the WireGuard helper's log" {
		t.Errorf("CommandLine for no deployment's interface = %q", got)
	}
}
```

Add `"errors"` and `"fmt"` to the file's imports.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/apply/ -run 'TestWGHelper'`
Expected: FAIL to compile: `CommandLine undefined`. Remove `TestWGHelperCommandLine` temporarily to see `TestWGHelperErrorsCarryWhatTheHelperPrinted` fail for `Up`, `Restart`, `Sync`, `Put` (their errors are the bare `exit status 1`), then restore it.

- [ ] **Step 3: Carry the output, and add `CommandLine`**

In `internal/apply/helper.go`, add `"strings"` to the imports, and replace `Put`, `Show`, `Log` and `run` with:

```go
// Put sends conf on stdin, for the helper to check and install as the
// interface's wg-quick file.
func (h WGHelper) Put(conf []byte) error {
	cmd, err := h.command("put")
	if err != nil {
		return err
	}
	out, err := h.T.RunInput(cmd, string(conf))
	return withOutput(err, out)
}
```

```go
	out, err := h.T.Run(cmd)
	return out, withOutput(err, out)
}
```

as the last two lines of `Show` and of `Log` (in place of `return h.T.Run(cmd)`), and

```go
func (h WGHelper) run(verb string) error {
	cmd, err := h.command(verb)
	if err != nil {
		return err
	}
	out, err := h.T.Run(cmd)
	return withOutput(err, out)
}

// withOutput is err with what the helper or sudo printed, which is why it
// failed: SSHTransport's error quotes the output already, and a transport
// that does not loses nothing. Nil stays nil.
func withOutput(err error, out string) error {
	out = strings.TrimRight(out, "\n")
	if err == nil || out == "" || strings.Contains(err.Error(), out) {
		return err
	}
	return fmt.Errorf("%w\n%s", err, out)
}

// CommandLine is the helper's command for verb as an operator types it,
// logged in as the deployment's user, for a message: sudo, the path
// unquoted, and the verb. An Iface that is no deployment's has no helper,
// so it is named in words instead.
func (h WGHelper) CommandLine(verb string) string {
	path, err := h.Path()
	if err != nil {
		return "the WireGuard helper's " + verb
	}
	return "sudo " + path + " " + verb
}
```

Run: `go test ./internal/apply/ -run 'TestWGHelper'`
Expected: PASS.

- [ ] **Step 4: Make the fake host answer the helper, and write the failing apply tests**

In `internal/apply/apply_test.go`:

Add after the imports:

```go
// wgHelper is the start of every command apply sends the WireGuard helper
// for the fixture's interface, and wgInstalled is where the helper's put
// installs the mesh file on a real host.
const (
	wgHelper    = "sudo -n '/usr/local/libexec/paisans/wg-psns-f2a9' "
	wgInstalled = "/etc/wireguard/psns-f2a9.conf"
)
```

In `fakeHost`, replace the `wgUp`, `wgStays` and `journal` comments' wording "Starting or restarting the unit" with "The helper's up or restart", "`journalctl -u wg-quick@psns-f2a9`" with "the helper's log", and add two fields:

```go
	// wgRun is the mesh file psns-f2a9 runs: what the helper had installed
	// when it was last brought up, restarted or synced.
	wgRun string
	// failOut, when set, is what a command matching fail prints in place of
	// the fake's own refusal.
	failOut string
```

In `Run`, change the `h.fail` branch to:

```go
	if h.fail != "" && strings.Contains(command, h.fail) {
		if h.failOut != "" {
			return h.failOut, fmt.Errorf("exit status 1")
		}
		return "refused by the fake host", fmt.Errorf("exit status 1")
	}
	if verb, ok := strings.CutPrefix(command, wgHelper); ok {
		return h.helper(verb)
	}
```

Delete the two branches `strings.Contains(command, "enable --now wg-quick@psns-f2a9") || ...` and `strings.Contains(command, "journalctl -u wg-quick@psns-f2a9")`. In the `ip -br addr show dev psns-f2a9` branch, read the address from `h.wgRun` instead of `h.files[...]`:

```go
		if address == "" {
			for _, line := range strings.Split(h.wgRun, "\n") {
				if v, ok := strings.CutPrefix(line, "Address = "); ok {
					address = v
				}
			}
		}
```

Add after `Run`:

```go
// helper answers the WireGuard helper's verbs the way the script does on a
// host: put installs what it was sent, up and restart bring the interface
// up from the installed file, sync hands it the installed file in place,
// and show reads what it runs.
func (h *fakeHost) helper(verb string) (string, error) {
	switch verb {
	case "put":
		h.files[wgInstalled] = h.inputs[len(h.inputs)-1]
	case "up", "restart":
		h.wgUp = !h.wgStays
		h.wgRun = h.files[wgInstalled]
	case "sync":
		h.wgRun = h.files[wgInstalled]
	case "show public-key":
		m := regexp.MustCompile(`(?m)^PrivateKey = (\S+)`).FindStringSubmatch(h.wgRun)
		if m == nil {
			return "", nil
		}
		key, err := render.PublicKey(m[1])
		if err != nil {
			return "", err
		}
		return key + "\n", nil
	case "show peers":
		var b strings.Builder
		for _, m := range regexp.MustCompile(`(?m)^PublicKey = (\S+)`).FindAllStringSubmatch(h.wgRun, -1) {
			b.WriteString(m[1] + "\n")
		}
		return b.String(), nil
	case "log":
		return h.journal, nil
	default:
		return "usage: wg-psns-f2a9 ...", fmt.Errorf("exit status 64")
	}
	return "", nil
}
```

Add `"regexp"` to the imports.

Update the existing mesh tests' expectations, each a one-line change:

| Test | Old | New |
|---|---|---|
| `TestAFirstApplyBringsUpTheMeshFirst` | `host.indexOf("systemctl enable --now wg-quick@psns-f2a9")` | `host.indexOf(wgHelper + "up")` |
| `TestAPeerChangeIsSyncedNotRestarted` | `host.ran("wg syncconf psns-f2a9")`, `host.ran("restart wg-quick@psns-f2a9")` | `host.ran(wgHelper + "sync")`, `host.ran(wgHelper + "restart")` |
| `TestAMeshThatWillNotStartStopsTheApply` | `host.fail = "wg-quick@psns-f2a9"` | `host.fail = wgHelper + "up"` |
| `TestAMeshThatIsNotUpAfterItsStartStopsTheApply` | `host.ran("journalctl -u wg-quick@psns-f2a9 -n 20 --no-pager")` | `host.ran(wgHelper + "log")` |
| `TestAMeshWhoseStartFailsIsExplainedFromItsJournal` | `host.fail = "enable --now wg-quick@psns-f2a9"` | `host.fail = wgHelper + "up"` |
| `TestAMeshFailureWithoutAJournalStillNamesTheMesh` | `host.fail = "journalctl"`; `"journalctl -u wg-quick@psns-f2a9"` | `host.fail = wgHelper + "log"`; `"sudo /usr/local/libexec/paisans/wg-psns-f2a9 log"` |
| `TestARestartedMeshIsReadBack` | `host.indexOf("restart wg-quick@psns-f2a9")` | `host.indexOf(wgHelper + "restart")` |

and the comment above `wgQuickJournal` to "what the helper's log prints after a start that failed".

In `internal/apply/meshresolve_test.go`: `"systemctl enable --now wg-quick@psns-f2a9"` (lines 73, 124, 199) becomes `wgHelper + "up"`; `"wg syncconf psns-f2a9"` (line 94) becomes `wgHelper + "sync"`; line 146's `host.ran("wg-quick@psns-f2a9") || host.ran("wg syncconf")` becomes `host.ran(wgHelper)`.

In `internal/apply/scope_test.go`: both `host.ran("wg syncconf")` become `host.ran(wgHelper + "sync")`. In `TestAScopedApplyRollsBack`, after the `host.files["/"+wgFile] != before` check, add:

```go
	if host.files[wgInstalled] != before {
		t.Error("the helper was not handed the restored file")
	}
	if put, sync := host.indexOf(wgHelper+"put"), host.indexOf(wgHelper+"sync"); put < 0 || sync < put {
		t.Errorf("the restored file was not put before the sync: %v", host.commands)
	}
```

Append the new tests to `internal/apply/apply_test.go`, after `TestARestartedMeshIsReadBack`:

```go
// The helper is handed exactly the file this apply wrote, before the
// interface is started from it, and nothing reaches the mesh except
// through the helper: the deployment's user has no other way to it.
func TestTheMeshFileIsPutBeforeTheInterfaceStarts(t *testing.T) {
	host := newHost()
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	put, up := host.indexOf(wgHelper+"put"), host.indexOf(wgHelper+"up")
	if put < 0 || up < put {
		t.Fatalf("put at %d, up at %d: %v", put, up, host.commands)
	}
	if host.files[wgInstalled] == "" || host.files[wgInstalled] != host.files["/srv/paisans/f2a9/psns-f2a9.conf"] {
		t.Error("the helper was handed something other than the file apply wrote")
	}
	for _, c := range host.commands {
		if strings.HasPrefix(c, "wg ") || strings.Contains(c, "wg-quick") || strings.Contains(c, "journalctl") || strings.Contains(c, "systemctl") {
			t.Errorf("%q reached the mesh without the helper", c)
		}
	}
}

// A file the helper refuses stops the apply before the interface or any
// stack moves, quoting the line it refused and saying what changes it: the
// owner admitted another port or subnet.
func TestAMeshFileTheHelperRefusesStopsTheApply(t *testing.T) {
	host := newHost()
	host.fail = wgHelper + "put"
	host.failOut = "wg-psns-f2a9: line 5: ListenPort 51999, but this deployment was admitted on 51820; nothing was installed\n"
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	err = apply.Execute(p, host)
	var problem *ui.Problem
	if !errors.As(err, &problem) {
		t.Fatalf("a refused mesh file is not a problem for the operator: %v", err)
	}
	if problem.Hint != "mesh: the WireGuard helper refused home-a's mesh file" {
		t.Errorf("hint = %q", problem.Hint)
	}
	for _, want := range []string{"line 5: ListenPort 51999, but this deployment was admitted on 51820", "admits the deployment again"} {
		if !strings.Contains(problem.Explain, want) {
			t.Errorf("the explanation lacks %q:\n%s", want, problem.Explain)
		}
	}
	if host.ran(wgHelper+"up") || host.firstCompose() >= 0 {
		t.Errorf("the apply carried on past a refused mesh file: %v", host.commands)
	}
}

// A host where sudo will not run the helper has not admitted the
// deployment, or has lost its sudoers line: the apply says so and names
// host admit, rather than quoting a bare exit status.
func TestAHostWithoutTheHelperNamesHostAdmit(t *testing.T) {
	host := newHost()
	host.fail = wgHelper
	host.failOut = "sudo: a password is required\n"
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	err = apply.Execute(p, host)
	var problem *ui.Problem
	if !errors.As(err, &problem) {
		t.Fatalf("a refused sudo is not a problem for the operator: %v", err)
	}
	if problem.Hint != "mesh: the WireGuard helper did not run on home-a" {
		t.Errorf("hint = %q", problem.Hint)
	}
	for _, want := range []string{"sudo: a password is required", "paisans host admit", "/usr/local/libexec/paisans/wg-psns-f2a9"} {
		if !strings.Contains(problem.Explain, want) {
			t.Errorf("the explanation lacks %q:\n%s", want, problem.Explain)
		}
	}
	if host.firstCompose() >= 0 {
		t.Errorf("a stack moved without the mesh: %v", host.commands)
	}
}

// SyncMesh puts the plan's file and syncs, for a caller resuming a run that
// wrote the file and stopped before the interface had it.
func TestSyncMeshPutsThenSyncs(t *testing.T) {
	host := applied(t, "home-a")
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.Scope("srv/paisans/f2a9/psns-f2a9.conf"))
	if err != nil {
		t.Fatal(err)
	}
	host.commands = nil
	if err := apply.SyncMesh(p, host); err != nil {
		t.Fatal(err)
	}
	want := []string{wgHelper + "put", wgHelper + "sync"}
	if strings.Join(host.commands, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran %v, want %v", host.commands, want)
	}
}
```

- [ ] **Step 5: Run them to verify they fail**

Run: `go test ./internal/apply/`
Expected: FAIL to compile: `undefined: apply.SyncMesh`. Comment out `TestSyncMeshPutsThenSyncs` to see the rest fail: `TestAFirstApplyBringsUpTheMeshFirst` (`psns-f2a9 was written and never started`), `TestTheMeshFileIsPutBeforeTheInterfaceStarts` (`put at -1`), `TestAMeshFileTheHelperRefusesStopsTheApply` and `TestAHostWithoutTheHelperNamesHostAdmit` (no problem), the scope and resolve tests (no helper `sync` or `up`); then restore it.

- [ ] **Step 6: Hand the mesh to the helper**

In `internal/apply/apply.go`, after `meshAddress string` (line 188), add:

```go
	// meshConf is the site's rendered WireGuard file, as written to
	// deployment.WireGuardConf. Every mesh step hands it to the WireGuard
	// helper's put before it starts, syncs or restarts the interface, so the
	// interface runs what this apply wrote.
	meshConf string
```

In the `if rel == d.WireGuardConf() {` block (line 799), add `out.meshConf = content` beside `out.meshAddress = ...`.

Replace the `WireGuardStep` comment and constants' wording at lines 280-296 so that `WireGuardStart` "puts the file and enables and starts the unit through the WireGuard helper", `WireGuardSync` "puts the file and hands the running interface its peers without taking it down", `WireGuardRestart` "puts the file and restarts the interface"; keep the reason sentences. Delete the `Command` method (lines 298-316) whole.

In `execute`, replace lines 1209-1226:

```go
	// The mesh comes up before anything that binds to it. It runs before the
	// gateway gates as well, which need no mesh themselves, so that the one
	// rule is simple: no container is started or checked on a site whose
	// interface is down.
	if plan.WireGuard != WireGuardNone {
		if err := resolveMeshEndpoints(plan, t); err != nil {
			return err
		}
		done := plan.step(plan.WireGuard.Title(plan.Deployment))
		done.Detail("%s", plan.WireGuard.Describe(plan.Deployment))
		err := bringUpMesh(plan, t)
		finish(done, err)
		if err != nil {
			return err
		}
	}
```

In `internal/apply/mesh.go`, replace `bringUpMesh` (lines 31-44) with:

```go
// bringUpMesh hands the interface the plan's file through the WireGuard
// helper and runs the plan's step, then reads the interface back. A mesh
// that did not come up is the operator's problem, explained from the unit's
// journal; the helper's own refusals are already one; a read that failed is
// returned as it is.
func bringUpMesh(plan *Plan, t Transport) error {
	if err := handMesh(plan, t, plan.WireGuard); err != nil {
		if errors.As(err, new(*ui.Problem)) {
			return err
		}
		return meshProblem(plan, t, fmt.Errorf("%s: %w", plan.WireGuard.Title(plan.Deployment), err))
	}
	err := meshUp(plan, t)
	if errors.As(err, new(*meshDownError)) {
		return meshProblem(plan, t, err)
	}
	return err
}

// handMesh puts the plan's mesh file through the WireGuard helper, then
// runs step's verb: up, sync or restart. The put comes first every time, so
// the interface is always started, synced or restarted from the file this
// apply wrote, whatever /etc/wireguard held before.
func handMesh(plan *Plan, t Transport, step WireGuardStep) error {
	h := WGHelper{T: t, Iface: plan.Deployment.Interface()}
	if plan.meshConf == "" {
		return fmt.Errorf("%s: this plan holds no mesh file to hand %s", plan.Site, h.Iface)
	}
	if err := h.Put([]byte(plan.meshConf)); err != nil {
		return helperProblem(plan.Site, h, err)
	}
	var err error
	switch step {
	case WireGuardStart:
		err = h.Up()
	case WireGuardSync:
		err = h.Sync()
	case WireGuardRestart:
		err = h.Restart()
	}
	if err != nil {
		return helperProblem(plan.Site, h, err)
	}
	return nil
}

// SyncMesh hands the interface the plan's mesh file and syncs its peers in
// place, for a caller resuming a run that wrote the file and stopped before
// the interface had it.
func SyncMesh(plan *Plan, t Transport) error {
	return handMesh(plan, t, WireGuardSync)
}

// helperProblem turns the WireGuard helper's two refusals into the
// operator's problem and returns any other error as it is. sudo refusing it
// is a host that has not admitted this deployment, or has lost the sudoers
// line; put refusing the file names the line, and the usual cause is an
// endpoint port or mesh subnet changed since the owner admitted it.
func helperProblem(site string, h WGHelper, err error) error {
	const stopped = "Nothing that binds the mesh address was started."
	text := err.Error()
	path, _ := h.Path()
	if line := sudoRefusal(text); line != "" {
		return &ui.Problem{
			Hint:    "mesh: the WireGuard helper did not run on " + site,
			Explain: fmt.Sprintf("sudo would not run %s (%s). `paisans host admit` installs the helper and the one sudoers line that lets this deployment's user run it: the host's owner admits the deployment, then apply again.\n%s", path, line, stopped),
			Cause:   err,
		}
	}
	if line := refusedConf(text); line != "" {
		return &ui.Problem{
			Hint:    "mesh: the WireGuard helper refused " + site + "'s mesh file",
			Explain: fmt.Sprintf("%s. The helper installs only what paisans renders, on the ListenPort and inside the mesh subnet the host's owner admitted. When the site's endpoint port or mesh.subnet has changed since, the owner admits the deployment again from a new request.\n%s", line, stopped),
			Cause:   err,
		}
	}
	return err
}

// sudoRefusal is the line sudo printed when it would not run the helper,
// such as "sudo: a password is required" or "Sorry, user ... is not allowed
// to execute ...", empty when there is none.
func sudoRefusal(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "sudo: ") || strings.Contains(line, " is not allowed to execute ") {
			return line
		}
	}
	return ""
}

// refusedConf is why the helper's put refused a file, from the line it
// prints ("wg-psns-f2a9: line 5: ...; nothing was installed"), without its
// name and ending, empty when there is none.
func refusedConf(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		why, ok := strings.CutSuffix(line, "; nothing was installed")
		if !ok {
			continue
		}
		if _, rest, ok := strings.Cut(why, ": "); ok {
			return rest
		}
		return why
	}
	return ""
}

// journalTail is the last n lines of a journal, newlines kept.
func journalTail(journal string, n int) string {
	lines := strings.Split(strings.TrimRight(journal, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
```

Replace `journalLines`' comment and `meshProblem` (lines 103-148) with:

```go
// journalLines is how much of the unit's journal a failure keeps: enough for
// one attempt of wg-quick, which logs a line per command it runs. The
// helper's log prints the last 200; the rest is older attempts.
const journalLines = 20

// meshProblem turns a mesh that did not come up into the operator's problem,
// with wg-quick's own error read from the unit's journal, which the
// deployment's user reads through the helper's log. Only the line that says
// what went wrong is in the explanation; the journal is the cause, shown
// with -v. A journal that cannot be read leaves the explanation without it.
func meshProblem(plan *Plan, t Transport, cause error) error {
	d := plan.Deployment
	unit := d.WireGuardUnit()
	h := WGHelper{T: t, Iface: d.Interface()}
	logCommand := h.CommandLine("log")
	problem := &ui.Problem{
		Hint:  "mesh: " + unit + " did not come up",
		Cause: cause,
	}
	stopped := "Nothing that binds the mesh address was started."

	// An interface that is up with the wrong address is not wg-quick
	// failing, so its journal has nothing to add.
	var down *meshDownError
	if errors.As(cause, &down) && down.address() {
		problem.Explain = fmt.Sprintf("%s, which every service binds. Run `%s` on %s and apply again.\n%s",
			down.Error(), h.CommandLine("restart"), plan.Site, stopped)
		return problem
	}

	journal, err := h.Log()
	if err != nil {
		problem.Explain = fmt.Sprintf("Read why on %s with `%s`, fix it and apply again.\n%s", plan.Site, logCommand, stopped)
		return problem
	}
	journal = journalTail(journal, journalLines)
	problem.Cause = fmt.Errorf("%w\n%s:\n%s", cause, logCommand, journal)
	line := wgQuickError(journal)
	if name := unresolvedName(line); name != "" {
		problem.Explain = fmt.Sprintf("%s does not resolve from %s; create its DNS record (paisans dns init) and apply again.\n%s", name, plan.Site, stopped)
		return problem
	}
	if line != "" {
		problem.Explain = fmt.Sprintf("wg-quick: %s\n%s", line, stopped)
		return problem
	}
	problem.Explain = fmt.Sprintf("Read why on %s with `%s`, fix it and apply again.\n%s", plan.Site, logCommand, stopped)
	return problem
}
```

In `internal/apply/scope.go`, replace the loop body and the tail of `Rollback` (lines 34-53):

```go
	restored := false
	conf := remoteRoot + plan.Deployment.WireGuardConf()
	for i, change := range plan.Changes {
		if change.Kind != Update {
			continue
		}
		if err := t.WriteFile(change.Path, change.before, change.Mode); err != nil {
			return fmt.Errorf("%s: restoring %s: %w", plan.Site, change.Path, err)
		}
		plan.Changes[i].content = change.before
		if change.Path == conf {
			plan.meshConf = change.before
		}
		restored = true
	}
	if !restored {
		return nil
	}
	if err := writeManifest(plan, t); err != nil {
		return err
	}
	if plan.WireGuard != WireGuardNone && plan.WireGuard != WireGuardStart {
		if err := handMesh(plan, t, plan.WireGuard); err != nil {
			return fmt.Errorf("%s: handing %s its restored configuration: %w", plan.Site, plan.Deployment.Interface(), err)
		}
	}
	return nil
```

and in its comment, "a peer added with `wg syncconf` is removed with `wg syncconf`" becomes "a peer added by the helper's sync is removed by its sync".

- [ ] **Step 7: Run the package to verify it passes**

Run: `go vet ./internal/apply/ && go test ./internal/apply/`
Expected: PASS. `go build ./...` fails in `internal/meshrekey` (`apply.WireGuardSync.Command undefined`); Task 5 changes that call, so for this task replace `internal/meshrekey/meshrekey.go:575-577`:

```go
	if out, err := t.Run(apply.WireGuardSync.Command(p.dep())); err != nil {
		return fmt.Errorf("%s: %s: %w: %s", name, apply.WireGuardSync.Title(p.dep()), err, strings.TrimSpace(out))
	}
```

with

```go
	if err := apply.SyncMesh(sp, t); err != nil {
		return fmt.Errorf("%s: %s: %w", name, apply.WireGuardSync.Title(p.dep()), err)
	}
```

and in `internal/meshrekey/meshrekey_test.go` add the helper's verbs to the fake as Task 5 Step 1 lists for `meshrekey` (the `wgHelper` and `wgInstalled` constants, `put` in `RunInput`, and the `put`, `up`, `restart` and `sync` cases), leaving the `wg show` cases and the bare-`wg` refusal for Task 5. Do the same for the `internal/siteadd/world_test.go` and `internal/siteremove/world_test.go` fakes: their `put`, `up`, `restart` and `sync` cases from Task 5 Step 1, and the `"wg syncconf"` strings in `internal/siteremove/evict_test.go`, `refusal_test.go`, `siteremove_test.go` and `internal/siteadd/siteadd_test.go` as listed there. These fakes are driven through `apply.Execute`, so they must answer the helper from this task on.

- [ ] **Step 8: Run the whole suite**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 9: Changelog**

`CHANGELOG.md`, `[Unreleased]`, `### Changed`, add:

```markdown
- `apply` starts, syncs and restarts each site's mesh through its WireGuard helper, `sudo -n /usr/local/libexec/paisans/wg-psns-<token>`, which checks the mesh file before installing it in `/etc/wireguard`.
```

- [ ] **Step 10: Commit**

```bash
git add internal/apply internal/meshrekey internal/siteadd internal/siteremove CHANGELOG.md
git commit -m "feat: reach the mesh through the wireguard helper in apply

apply runs as the deployment's user, who cannot write /etc/wireguard, start
a system unit or read its journal. Every mesh step now hands the helper the
file apply wrote with put, then runs up, sync or restart, and a failed mesh
is explained from the helper's log. The put comes first every time, so the
interface always runs what this apply wrote. A helper sudo will not run, or
a file its put refuses, stops the apply as a problem naming host admit or
the refused line, since both mean the host's admission and the
configuration disagree. Rollback hands back the restored file the same way.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: An up interface that runs another file is synced or restarted

**Files:**
- Modify: `internal/apply/apply.go:879`, `:1054` (callers), `:1064-1087` (`wireguardStep`), `:318-346` (`Describe`)
- Modify: `internal/apply/mesh.go` (add `runningStep`, `fileKey`, `filePeers`)
- Test: `internal/apply/apply_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: `handMesh`, `helperProblem`, `WGHelper.Show` (Task 3); `meshAddressProbe`, `wireguardAddress` (`internal/apply/mesh.go`); `render.PublicKey(private string) (string, error)`.
- Produces: `wireguardStep(site string, d deployment.Deployment, change Change, before string, t Transport) (WireGuardStep, error)`; nothing exported.

- [ ] **Step 1: Write the failing tests**

Append to `internal/apply/apply_test.go`, after `TestAnUpMeshWithAnUnchangedFileIsLeftAlone`:

```go
// A run that stopped after writing the mesh file and before the helper's
// sync leaves the file as rendered and the interface on its old peers. The
// next apply reads what the interface runs and syncs it, rather than taking
// the unchanged file as done.
func TestAnUpMeshRunningOtherPeersIsSynced(t *testing.T) {
	host := applied(t, "home-a")
	cut := strings.Index(host.wgRun, "[Peer]\n# watch")
	if cut < 0 {
		t.Fatalf("the fixture's home-a has no peer watch:\n%s", host.wgRun)
	}
	host.wgRun = host.wgRun[:cut]
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if p.WireGuard != apply.WireGuardSync {
		t.Fatalf("an interface missing a peer of its unchanged file plans %v, want a sync", p.WireGuard)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(host.wgRun, "# watch") {
		t.Error("the sync did not hand the interface its file")
	}
}

// The same for a key: a rekey's swap stopped between the write and the sync.
func TestAnUpMeshRunningAnotherKeyIsSynced(t *testing.T) {
	host := applied(t, "home-a")
	host.wgRun = regexp.MustCompile(`(?m)^PrivateKey = \S+`).ReplaceAllString(host.wgRun, "PrivateKey = ze/YeDqRtEZkDi4flVmds15ISgBxvSGCs7YNCBLBDHA=")
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if p.WireGuard != apply.WireGuardSync {
		t.Errorf("an interface on another key plans %v, want a sync", p.WireGuard)
	}
}

// An address is wg-quick's, which a sync never applies: an up interface
// without its unchanged file's address is restarted.
func TestAnUpMeshWithoutItsAddressIsRestarted(t *testing.T) {
	host := applied(t, "home-a")
	host.wgAddress = "10.44.0.9/24"
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if p.WireGuard != apply.WireGuardRestart {
		t.Errorf("an interface without its file's address plans %v, want a restart", p.WireGuard)
	}
}

// Reading what an up interface runs is still a probe: a plan changes
// nothing on the mesh.
func TestPlanningAnUpMeshOnlyReadsIt(t *testing.T) {
	host := applied(t, "home-a")
	if _, err := apply.Build("home-a", plan(t), acmeModule(t), host); err != nil {
		t.Fatal(err)
	}
	for _, command := range host.commands {
		switch {
		case strings.Contains(command, "ip link show psns-f2a9"),
			strings.Contains(command, "ip -br addr show dev psns-f2a9"),
			command == wgHelper+"show public-key",
			command == wgHelper+"show peers",
			strings.Contains(command, "echo paisans-sweep-end"):
		default:
			if strings.Contains(command, "psns-f2a9") {
				t.Errorf("planning ran %q on the mesh", command)
			}
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/apply/ -run 'TestAnUpMesh|TestPlanningAnUpMesh'`
Expected: FAIL: `an interface missing a peer of its unchanged file plans 0, want a sync`, the same for the key, and `plans 0, want a restart`. `TestPlanningAnUpMeshOnlyReadsIt` and `TestAnUpMeshWithAnUnchangedFileIsLeftAlone` pass already.

- [ ] **Step 3: Read what the interface runs**

In `internal/apply/apply.go`, change both calls (lines 879 and 1054) to `wireguardStep(site, d, *wireguard, wireguardBefore, t)`, and replace `wireguardStep` (lines 1064-1087) with:

```go
// wireguardStep decides what d's interface needs. Reading the host here is a
// probe and changes nothing, so a dry run can show it.
func wireguardStep(site string, d deployment.Deployment, change Change, before string, t Transport) (WireGuardStep, error) {
	switch change.Kind {
	case Create:
		return WireGuardStart, nil
	case Conflict:
		// Execute refuses before any step runs.
		return WireGuardNone, nil
	}
	out, err := t.Run(wireguardProbe(d))
	if err != nil {
		return WireGuardNone, fmt.Errorf("asking whether %s is up: %w", d.Interface(), err)
	}
	if strings.TrimSpace(out) != "up" {
		return WireGuardStart, nil
	}
	if change.Kind == Unchanged {
		return runningStep(site, d, change.content, t)
	}
	if wgQuickLines(before) != wgQuickLines(change.content) {
		return WireGuardRestart, nil
	}
	return WireGuardSync, nil
}
```

In `internal/apply/mesh.go`, add `"slices"` and `"github.com/paisans-software/paisans-cli/internal/render"` to the imports and:

```go
// runningStep is what an up interface whose file did not change still
// needs. The helper's put and its sync or restart are separate commands
// from writing the file, so a run that stopped between them leaves the file
// as rendered and the interface on what it ran before. Read back: without
// the file's Address it is restarted, since only wg-quick applies an
// address; on another key or other peers it is synced; otherwise it needs
// nothing.
func runningStep(site string, d deployment.Deployment, content string, t Transport) (WireGuardStep, error) {
	iface := d.Interface()
	out, err := t.Run(meshAddressProbe(iface))
	if err != nil {
		return WireGuardNone, fmt.Errorf("reading %s: %w", iface, err)
	}
	fields := strings.Fields(out)
	if address := wireguardAddress(content); address != "" && (len(fields) < 3 || !slices.Contains(fields[2:], address)) {
		return WireGuardRestart, nil
	}
	h := WGHelper{T: t, Iface: iface}
	key, err := h.Show("public-key")
	if err != nil {
		return WireGuardNone, helperProblem(site, h, err)
	}
	peers, err := h.Show("peers")
	if err != nil {
		return WireGuardNone, helperProblem(site, h, err)
	}
	want, err := fileKey(content)
	if err != nil {
		return WireGuardNone, fmt.Errorf("%s's mesh file: %w", site, err)
	}
	running := strings.Fields(peers)
	listed := filePeers(content)
	slices.Sort(running)
	slices.Sort(listed)
	if strings.TrimSpace(key) != want || !slices.Equal(running, listed) {
		return WireGuardSync, nil
	}
	return WireGuardNone, nil
}

// fileKey is the public key of a WireGuard file's PrivateKey.
func fileKey(content string) (string, error) {
	for _, line := range strings.Split(content, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(key) == "PrivateKey" {
			return render.PublicKey(strings.TrimSpace(value))
		}
	}
	return "", fmt.Errorf("it has no PrivateKey")
}

// filePeers is every PublicKey a WireGuard file lists, in its order.
func filePeers(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(key) == "PublicKey" {
			out = append(out, strings.TrimSpace(value))
		}
	}
	return out
}
```

Add `"github.com/paisans-software/paisans-cli/internal/deployment"` to `mesh.go`'s imports. In `Describe` (lines 333-346), the two texts now also cover an unchanged file: `WireGuardSync` becomes `"give " + iface + " its file's key and peers in place, without taking the mesh down"`, and `WireGuardRestart` becomes `"restart " + iface + ", so it runs its file's Address and MTU, which only wg-quick applies"`.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./internal/apply/`
Expected: PASS.

- [ ] **Step 5: Run the whole suite**

Run: `go vet ./... && go test ./...`
Expected: PASS. A fake that answers `ip -br addr` or the helper's `show` from something other than what its interface runs now plans a sync on an unchanged up site: the `meshrekey`, `siteadd` and `siteremove` fakes answer `show` in Task 5; if their tests fail here on an unexpected `sync`, add Task 5 Step 1's `show public-key` and `show peers` cases to that fake in this task, and say so in the commit body. A test whose `Describe` text changed (a `siteadd` or `siteremove` step text) takes the new text.

- [ ] **Step 6: Changelog**

`CHANGELOG.md`, `[Unreleased]`, `### Fixed`, add:

```markdown
- `apply` on a site whose mesh interface is up without its file's address, key or peers restarts or syncs it, so a run stopped between writing the file and handing it over completes.
```

- [ ] **Step 7: Commit**

```bash
git add internal/apply internal/meshrekey internal/siteadd internal/siteremove CHANGELOG.md
git commit -m "fix: sync or restart an up mesh that runs another file

Writing the mesh file and handing it to the interface are separate
commands, now more so with the helper's put between them. A run that
stopped after the write left the file as rendered, so the next apply
found it unchanged and the interface up and did nothing, while the
interface ran the old peers, the old key or the old address. An unchanged
file on an up interface is now read back: its address with ip, which needs
no privilege, and its key and peers through the helper; a missing address
restarts it and a different key or peer list syncs it.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: `site add`, `site remove`, `mesh rekey` and `meshkey` read through the helper

**Files:**
- Modify: `internal/meshkey/meshkey.go:26-50`
- Modify: `internal/meshrekey/meshrekey.go:556-597`
- Modify: `internal/siteadd/mesh.go:14-21`, `:31`, `:123-133`, `:163`
- Modify: `internal/siteremove/cluster.go:566-576`, `internal/siteremove/evict.go:526-537`
- Create: `internal/apply/wgcalls_test.go`
- Test: `internal/meshrekey/meshrekey_test.go`, `internal/siteadd/world_test.go`, `internal/siteadd/siteadd_test.go`, `internal/siteremove/world_test.go`, `internal/siteremove/evict_test.go`, `internal/siteremove/refusal_test.go`, `internal/siteremove/siteremove_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: `apply.WGHelper{T, Iface}.Show(field string) (string, error)`, `CommandLine(verb string) string`, `apply.SyncMesh(plan *apply.Plan, t apply.Transport) error` (Task 3).
- Produces: no new API. `meshkey.RunningKey` and `meshkey.Peers` keep their signatures.

- [ ] **Step 1: Write the failing guard, and make the fakes answer only the helper**

Create `internal/apply/wgcalls_test.go`:

```go
package apply_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// meshCommand matches a string that starts a wg, wg-quick, journalctl or
// systemctl command, at its start or after a shell separator, with or
// without sudo in front.
var meshCommand = regexp.MustCompile(`(^|[;&|]\s*)(sudo\s+(-n\s+)?)?(wg|wg-quick|journalctl|systemctl)\s`)

// Every command that reaches the mesh interface goes through the WireGuard
// helper, because it is the one thing the deployment's user may run as
// root: one written directly fails on a host, without sudo. This walks the
// packages that touch the mesh and refuses a string literal that starts
// such a command. A systemctl --user command is the user's own and allowed.
func TestTheMeshIsReachedOnlyThroughTheHelper(t *testing.T) {
	exempt := map[string]string{
		filepath.Join("..", "siteremove", "host.go"): "site remove's host step stops the removed site's own units and interface, which no helper verb does",
	}
	for _, dir := range []string{".", "../meshkey", "../meshrekey", "../siteadd", "../siteremove", "../doctor"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") || exempt[file] != "" {
				continue
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err == nil && meshCommand.MatchString(s) && !strings.Contains(s, "--user") {
					t.Errorf("%s: %q runs a mesh command directly; go through apply.WGHelper", fset.Position(lit.Pos()), s)
				}
				return true
			})
		}
	}
}
```

Make each fake answer the helper and refuse a bare `wg`. In each file below, add the constants

```go
const (
	wgHelper    = "sudo -n '/usr/local/libexec/paisans/wg-psns-f2a9' "
	wgInstalled = "/etc/wireguard/psns-f2a9.conf"
)
```

(`internal/siteremove/world_test.go` has a `const (...)` block at line 43 with `wgConf`; add them there. If Task 3 Step 7 added them already, keep those.)

`internal/meshrekey/meshrekey_test.go`:
* `RunInput` (line 109), before `return h.Run(command)`:

```go
	if command == wgHelper+"put" {
		out, err := h.Run(command)
		if err == nil {
			h.files[wgInstalled] = stdin
		}
		return out, err
	}
```

* In `Run`, replace the four cases at lines 159-176 (`wg syncconf`, and the three `wg show psns-f2a9 ...`) with:

```go
	case command == wgHelper+"put":
		return "", nil
	case command == wgHelper+"sync", command == wgHelper+"up", command == wgHelper+"restart":
		h.run = h.files[wgInstalled]
		return "", nil
	case command == wgHelper+"show public-key":
		return h.runsKey() + "\n", nil
	case command == wgHelper+"show peers":
		return strings.Join(h.peers(), "\n") + "\n", nil
	case command == wgHelper+"show latest-handshakes":
```

keeping the latest-handshakes body as it was, and add, before the switch's `default` (or at the top of the switch):

```go
	case strings.HasPrefix(command, "wg "), strings.HasPrefix(command, "wg-quick "), strings.Contains(command, "wg syncconf"):
		return "", fmt.Errorf("%s: %q reached the mesh without the WireGuard helper", h.name, command)
```

* Line 297, `h.run = h.files[wgConf]`, becomes `h.files[wgInstalled], h.run = h.files[wgConf], h.files[wgConf]`; line 334, `h.files[wgConf], h.run = content, content`, becomes `h.files[wgConf], h.files[wgInstalled], h.run = content, content, content`.
* `"wg syncconf"` at lines 448, 573, 594, 686 becomes `"' sync"` (the end of the helper's command, which `strings.Contains` and `failOnce` match).

`internal/siteadd/world_test.go`:
* `RunInput` (line 88) becomes:

```go
func (h *host) RunInput(command, stdin string) (string, error) {
	out, err := h.Run(command)
	if err == nil && command == wgHelper+"put" {
		h.files[wgInstalled] = stdin
	}
	return out, err
}
```

* In `Run`, the case at line 185 (`enable --now wg-quick@psns-f2a9`, `restart wg-quick@psns-f2a9`) becomes `case command == wgHelper+"up", command == wgHelper+"restart":`; add `case command == wgHelper+"put", command == wgHelper+"sync": return "", nil`; the case `strings.Contains(command, "latest-handshakes")` becomes `command == wgHelper+"show latest-handshakes"`; add

```go
	case command == wgHelper+"show public-key":
		m := regexp.MustCompile(`(?m)^PrivateKey = (\S+)`).FindStringSubmatch(h.files[wgInstalled])
		if m == nil {
			return "", nil
		}
		key, err := render.PublicKey(m[1])
		if err != nil {
			return "", err
		}
		return key + "\n", nil
	case command == wgHelper+"show peers":
		var b strings.Builder
		for _, m := range regexp.MustCompile(`(?m)^PublicKey = (\S+)`).FindAllStringSubmatch(h.files[wgInstalled], -1) {
			b.WriteString(m[1] + "\n")
		}
		return b.String(), nil
	case command == "date +%s":
		return "1000\n", nil
	case strings.HasPrefix(command, "wg "), strings.Contains(command, "wg-quick"):
		return "", fmt.Errorf("%s: %q reached the mesh without the WireGuard helper", h.name, command)
```

* In `ip -br addr show dev psns-f2a9` (line 174) and `handshakes` (lines 345, 359), read `h.files[wgInstalled]` / `other.files[wgInstalled]`, what the interface was handed, and delete `b.WriteString("now 1000\n")` from `handshakes`.
* `internal/siteadd/siteadd_test.go:80`: in the verbs list, `"syncconf"` becomes `"' sync"`, and add `"' put"`.

`internal/siteremove/world_test.go`:
* `RunInput` (line 195), before `return h.Run(command)`:

```go
	if command == wgHelper+"put" {
		h.commands = append(h.commands, command)
		h.w.log = append(h.w.log, h.name+": "+command)
		h.files[wgInstalled] = stdin
		return "", nil
	}
```

* In `Run`, the case `strings.Contains(command, "wg syncconf")` (line 624) becomes:

```go
	case command == wgHelper+"sync", command == wgHelper+"up", command == wgHelper+"restart":
		running := h.files[wgInstalled]
		h.wgRun = &running
		if command != wgHelper+"sync" {
			h.wgUp = true
		}
		return "", nil
```

and the three `wg show psns-f2a9 ...` cases (631, 641, 647) become `command == wgHelper+"show public-key"`, `wgHelper+"show peers"` and `wgHelper+"show latest-handshakes"`, bodies unchanged. A bare `wg` command falls to the fake's existing `unexpected command` error.
* Where a seeded host's running file is set (line 1387, `if running, ok := h.files[wgConf]; ok { h.wgRun = &running }`), also set `h.files[wgInstalled] = running`.
* `"wg syncconf"` becomes `"' sync"` in `internal/siteremove/evict_test.go` (lines 201, 339, 435, 484, 552, 597, 608, 688); in `refusal_test.go:114` and `siteremove_test.go:100` the verbs list's `"syncconf"` becomes `"' sync"`, and add `"' put"`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/apply/ -run TestTheMeshIsReachedOnlyThroughTheHelper; go test ./internal/meshkey/ ./internal/meshrekey/ ./internal/siteadd/ ./internal/siteremove/`
Expected: FAIL. The guard names `meshkey.go:29` and `:40`, `meshrekey.go:566` and `:585`, `siteadd/mesh.go:20`, `siteremove/cluster.go:571` and `siteremove/evict.go:532`, and nothing in `apply` or `doctor`. The `meshrekey` and `siteadd` tests fail with `reached the mesh without the WireGuard helper`, and the `siteremove` tests with `unexpected command "wg show psns-f2a9 ..."` (or, if Task 4 Step 5 already added the `show` cases there, on the `siteremove` gate only through the guard).

- [ ] **Step 3: Read through the helper**

`internal/meshkey/meshkey.go`, replace lines 26-50 with:

```go
// RunningKey is the public key site's interface runs, reached through t and
// the WireGuard helper's show public-key, which prints it as `wg show
// <interface> public-key` does (wireguard-tools, wg(8)).
func RunningKey(site string, t apply.Transport, d deployment.Deployment) (string, error) {
	out, err := apply.WGHelper{T: t, Iface: d.Interface()}.Show("public-key")
	if err != nil {
		return "", fmt.Errorf("%s: reading %s's key through the WireGuard helper: %w", site, d.Interface(), err)
	}
	return strings.TrimSpace(out), nil
}

// Peers is the public keys site's interface lists as peers, reached through
// t and the helper's show peers, one per line (wireguard-tools, wg(8)).
func Peers(site string, t apply.Transport, d deployment.Deployment) ([]string, error) {
	out, err := apply.WGHelper{T: t, Iface: d.Interface()}.Show("peers")
	if err != nil {
		return nil, fmt.Errorf("%s: reading %s's peers through the WireGuard helper: %w", site, d.Interface(), err)
	}
	var keys []string
	for _, line := range strings.Split(out, "\n") {
		if key := strings.TrimSpace(line); key != "" {
			keys = append(keys, key)
		}
	}
	return keys, nil
}
```

and in the package comment (line 8), `read with \`wg show\`` becomes `read through the WireGuard helper's show`.

`internal/meshrekey/meshrekey.go`, in `handTo` replace lines 565-570:

```go
		out, err := t.Run("wg show " + p.dep().Interface() + " peers")
		if err != nil {
			return fmt.Errorf("%s: `wg show %s peers`: %w: %s", name, p.dep().Interface(), err, strings.TrimSpace(out))
		}
		runs = strings.Contains(out, newPublic)
```

with

```go
		peers, err := meshkey.Peers(name, t, p.dep())
		if err != nil {
			return err
		}
		runs = slices.Contains(peers, newPublic)
```

(add `"slices"` to the imports), and in `handshake` replace lines 585-588:

```go
	out, err := apply.WGHelper{T: p.transports[site], Iface: p.dep().Interface()}.Show("latest-handshakes")
	if err != nil {
		return false, fmt.Errorf("%s: reading %s's handshakes through the WireGuard helper: %w", site, p.dep().Interface(), err)
	}
```

and its comment's "`wg show <interface> latest-handshakes` prints" becomes "The helper's show latest-handshakes prints, as `wg show <interface> latest-handshakes` does,". The package comment's "read with `wg show`" (line 17) becomes "read through the WireGuard helper".

`internal/siteadd/mesh.go`, replace `handshakeProbe` (lines 14-21) with:

```go
// handshakes reads each peer's latest handshake on t's interface through the
// WireGuard helper, then the host's own clock, so an age is computed against
// the clock that wrote the timestamp. The helper prints one line per peer,
// its public key and a Unix time, 0 for a peer that has never completed one
// (wireguard-tools, wg(8), "latest-handshakes"); the clock is appended as
// "now <time>", which handshakeAges reads.
func handshakes(t apply.Transport, d deployment.Deployment) (string, error) {
	out, err := apply.WGHelper{T: t, Iface: d.Interface()}.Show("latest-handshakes")
	if err != nil {
		return out, err
	}
	now, err := t.Run("date +%s")
	if err != nil {
		return now, err
	}
	return out + "now " + strings.TrimSpace(now) + "\n", nil
}
```

In `meshGate`, line 125 becomes `out, err := handshakes(p.transports[name], p.cfg.Deployment())`; line 131's message becomes `fmt.Sprintf("%s: reading %s's handshakes failed: %s", name, p.cfg.Deployment().Interface(), lastLines(out, 2))`; the closing error (line 163) ends `"... and read %s on the sites named", ..., apply.WGHelper{Iface: p.cfg.Deployment().Interface()}.CommandLine("show latest-handshakes"))` in place of `` read `wg show %s` on the sites named `` and its interface argument. The gate text (line 31) "read from `wg show` on both ends" becomes "read through the WireGuard helper on both ends".

`internal/siteremove/cluster.go`, replace lines 571-574:

```go
		peers, err := meshkey.Peers(name, p.transports[name], p.dep())
		if err != nil {
			return err
		}
		for _, line := range peers {
```

(the loop body's `strings.TrimSpace(line)` stays harmless; `meshkey` is already imported in `siteremove` per *development.md* *`mesh rekey`*; add the import if the compiler says otherwise). Its comment: "`wg show <interface> peers` prints one public key per line" becomes "The WireGuard helper's show peers prints one public key per line".

`internal/siteremove/evict.go`, replace lines 532-535:

```go
		out, err := apply.WGHelper{T: p.transports[name], Iface: p.dep().Interface()}.Show("latest-handshakes")
		if err != nil {
			return fmt.Errorf("%s: reading %s's handshakes through the WireGuard helper: %w", name, p.dep().Interface(), err)
		}
```

and its comment's "`wg show <interface> latest-handshakes` prints" becomes "The WireGuard helper's show latest-handshakes prints". The stage gate text at line 500, "`wg show` on each has no handshake from it", stays: it says what is read, not how to run it.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./internal/apply/ ./internal/meshkey/ ./internal/meshrekey/ ./internal/siteadd/ ./internal/siteremove/`
Expected: PASS, the guard included.

- [ ] **Step 5: See the guard catch a direct command**

Temporarily add `_ = "wg show psns-f2a9 peers"` inside any function in `internal/siteadd/mesh.go`, run `go test ./internal/apply/ -run TestTheMeshIsReachedOnlyThroughTheHelper`, and expect it to FAIL naming that line; remove it and run again to PASS.

- [ ] **Step 6: Run the whole suite**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Changelog**

`CHANGELOG.md`, `[Unreleased]`, `### Changed`, add:

```markdown
- `site add`, `site remove` and `mesh rekey` read each site's handshakes, peers and key through the WireGuard helper, as the deployment's user, instead of running `wg show`.
```

- [ ] **Step 8: Commit**

```bash
git add internal/apply/wgcalls_test.go internal/meshkey internal/meshrekey internal/siteadd internal/siteremove CHANGELOG.md
git commit -m "feat: read the mesh through the wireguard helper everywhere

site add's gate, site remove's cut and handshake gates, mesh rekey's swap
and meshkey's reads of the running key ran wg show directly, which needs
CAP_NET_ADMIN the deployment's user does not have. They read through the
helper's show, and site add reads the host's clock with its own date
command, since the helper takes only its verbs. A test walks the packages
that touch the mesh and refuses any string that starts a wg, wg-quick,
journalctl or systemctl command outside the helper, so a new one fails in
review rather than on a host. site remove's host step, which stops the
removed site's own interface, is the one exemption.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: `doctor`'s advice and the documentation name the helper

**Files:**
- Modify: `internal/doctor/mesh.go:41-50`
- Test: `internal/doctor/mesh_test.go`
- Modify: `README.md` (lines cited below, by section), `docs/development.md:84-89`, `:609-616`, `:1050-1053`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: `apply.WGHelper{Iface}.CommandLine(verb string) string` (Task 3); `deployment.WireGuardConf`, `WireGuardInstalled` (Task 1).
- Produces: nothing.

- [ ] **Step 1: Write the failing test**

Append to `internal/doctor/mesh_test.go`:

```go
// A down interface's journal is root's to read, so the advice reads it the
// one way the deployment's user can: the WireGuard helper's log.
func TestADownInterfaceIsReadThroughTheHelper(t *testing.T) {
	got := Mesh(fixture(t), []MeshProbe{{Site: "home-b", Link: "absent\n", Probed: mesh.Probed{Routes: "[]", Addrs: "[]", Networks: "[]"}}})
	if len(got) == 0 || got[0].Level != Fail {
		t.Fatalf("Mesh = %+v, want a FAIL first", got)
	}
	more := strings.Join(got[0].More, "\n")
	if !strings.Contains(more, "`sudo /usr/local/libexec/paisans/wg-psns-f2a9 log` on home-b") {
		t.Errorf("the advice does not read the journal through the helper:\n%s", more)
	}
	if strings.Contains(more, "journalctl") || strings.Contains(more, "systemctl") {
		t.Errorf("the advice names a command the deployment's user cannot run:\n%s", more)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/doctor/ -run TestADownInterfaceIsReadThroughTheHelper`
Expected: FAIL: `the advice does not read the journal through the helper`.

- [ ] **Step 3: Change the advice**

In `internal/doctor/mesh.go`, add `"github.com/paisans-software/paisans-cli/internal/apply"` to the imports and replace line 48:

```go
						fmt.Sprintf("  1. `systemctl status %s` and `journalctl -u %s` on %s say why it did not come up.", d.WireGuardUnit(), d.WireGuardUnit(), p.Site),
```

with

```go
						fmt.Sprintf("  1. `%s` on %s, as the deployment's user, prints %s's journal, which says why it did not come up.", apply.WGHelper{Iface: iface}.CommandLine("log"), p.Site, d.WireGuardUnit()),
```

Run: `go test ./internal/doctor/`
Expected: PASS.

- [ ] **Step 4: README**

Each edit replaces exactly the text quoted. Read the section around each before editing; keep tables as tables.

*Names a deployment owns on a host* table (line 1756), the row

```
| the WireGuard interface | `psns-<token>`, Eg: `psns-f2a9`, from `/etc/wireguard/psns-f2a9.conf` by `wg-quick@psns-f2a9` (see *Two deployments on one host never share a mesh*) |
```

becomes

```
| the WireGuard interface | `psns-<token>`, Eg: `psns-f2a9`, from `/etc/wireguard/psns-f2a9.conf` by `wg-quick@psns-f2a9`; `apply` renders that file as `/srv/paisans/f2a9/psns-f2a9.conf` and the WireGuard helper `/usr/local/libexec/paisans/wg-psns-f2a9` installs it (see *Two deployments on one host never share a mesh*) |
```

The registry passage (lines 1830-1831), `names it; on a host with no entry, from the \`Address\` in
\`/etc/wireguard/psns-<token>.conf\`, matched` becomes `names it; on a host with no entry, from the \`Address\` in
the mesh file \`/srv/paisans/<token>/psns-<token>.conf\`, matched`.

*Two deployments on one host never share a mesh* (lines 1912-1914), `everything that touches the interface uses it: \`/etc/wireguard/psns-f2a9.conf\`,
\`wg-quick@psns-f2a9\`, \`wg syncconf psns-f2a9\`, the firewall's` becomes `everything that touches the interface uses it: \`/etc/wireguard/psns-f2a9.conf\`,
\`wg-quick@psns-f2a9\`, its helper \`/usr/local/libexec/paisans/wg-psns-f2a9\`, the firewall's`.

*`apply` brings the mesh interface up before anything binds to it*: after its first paragraph (ending "where nothing listens.", line 5666), add:

```markdown
**Every step goes through the WireGuard helper.** The deployment's user
cannot create an interface, write `/etc/wireguard` or start a system unit,
so `host admit` installs one root-owned script per deployment,
`/usr/local/libexec/paisans/wg-psns-<token>`, and one sudoers line that
lets that user run it and nothing else as root. `apply` writes the mesh
file as `/srv/paisans/<token>/psns-<token>.conf`, its own copy, which it
can read back and compare like any other file it renders, and hands it to
the helper's `put` before every start, sync or restart; `put` installs it
as `/etc/wireguard/psns-<token>.conf`, mode 600. `put` accepts only the
keys `apply` renders, each value matching its pattern, the `ListenPort` the
owner admitted, and an `Address` and `AllowedIPs` inside the mesh subnet,
and refuses the whole file otherwise, because wg-quick runs a file's
`PreUp`, `PostUp`, `PreDown` and `PostDown` as root and adds a route for
each `AllowedIPs`: a helper that installed any file would hand root, or the
host's routing, to whoever could run it. A file it refuses stops the apply
with the line it refused, and a host where sudo will not run it stops the
apply naming `paisans host admit`.
```

The table (lines 5670-5676) becomes:

```markdown
| `psns-<token>.conf` | Interface | Action, after the helper's `put` |
|---------------------|-----------|--------|
| new | any | the helper's `up`: `systemctl enable --now wg-quick@psns-<token>` |
| unchanged | down | the same, so a rebooted or hand stopped site recovers on the next apply |
| unchanged | up, running the file | nothing, and no `put` |
| unchanged | up, with another key or other peers | the helper's `sync` |
| unchanged | up, without the file's `Address` | the helper's `restart` |
| changed peers | up | the helper's `sync`: `wg syncconf` from `wg-quick strip`, in place |
| changed `Address` or `MTU` | up | the helper's `restart`: `systemctl restart wg-quick@psns-<token>` |
```

After the paragraph ending "(wireguard-tools, `src/wg-quick/linux.bash`)." (line 5687), add:

```markdown
**An up interface is read back even when its file did not change.**
Writing the file and handing it over are separate commands, so a run that
stopped between them leaves the file as rendered and the interface on what
it ran before. `apply` reads the interface's address with `ip`, and its key
and peers with the helper's `show`, and restarts or syncs it when they are
not the file's.
```

Lines 5700-5702, `\`mesh: wg-quick@psns-<token> did not come up\` and explains it from the unit's
journal (\`journalctl -u wg-quick@psns-<token> -n 20\`, through the site's sudo):
wg-quick's own error line by default, the whole journal with \`-v\`.` become `\`mesh: wg-quick@psns-<token> did not come up\` and explains it from the unit's
journal, which the deployment's user reads with the helper's \`log\`
(\`sudo /usr/local/libexec/paisans/wg-psns-<token> log\`, the unit's last 200
lines, of which \`apply\` keeps 20): wg-quick's own error line by default,
the last 20 lines with \`-v\`.`

*Rekeying the mesh: `mesh rekey`* (lines 8858-8860), `and which public key its host runs, read with \`wg show <interface>
public-key\`, says whether stage 2 is done.` becomes `and which public key its host runs, read with the WireGuard helper's
\`show public-key\`, says whether stage 2 is done.`

- [ ] **Step 5: `docs/development.md`**

Line 88, `the \`Address\` line of \`/etc/wireguard/psns-<token>.conf\`.` becomes `the \`Address\` line of the mesh file, \`/srv/paisans/<token>/psns-<token>.conf\`.`

Lines 609-616, the paragraph beginning **The mesh interface, `psns-<token>`, is up before any container moves.**, becomes:

```markdown
**The mesh interface, `psns-<token>`, is up before any container moves.** Every service binds the site's
mesh address, so the mesh comes up right after the files are written and before
the gateway checks and stack actions below. Every mesh step goes through the
WireGuard helper (`apply.WGHelper`, `internal/apply/helper.go`), whose script
`internal/hostprep/wghelper.go` renders from
`internal/hostprep/snippets/wg-helper.sh.tmpl`: `handMesh` puts the rendered
file, then a first apply runs `up`; a peer change runs `sync` so the mesh
stays up; a change to a line only wg-quick applies runs `restart`; and an
unchanged file on a host whose interface is down runs `up`. An unchanged file
on an up interface is read back (`runningStep`) and synced or restarted when
the interface runs another key, other peers or no address. A failure stops
the apply there. `README.md` has the table under "`apply` brings the mesh
interface up before anything binds to it". `internal/hostprep/wghelper_test.go`
runs the rendered script under `sh`, and `dash` and `bash` when present,
against a table of confs and the renderer's own output, and
`internal/apply/wgcalls_test.go` refuses any `wg`, `wg-quick`, `journalctl` or
`systemctl` command written directly in the packages that touch the mesh.
```

Line 1052, `from the secrets file and \`wg show <interface> public-key\`, and nothing else.` becomes `from the secrets file and the WireGuard helper's \`show public-key\`, and nothing else.`

- [ ] **Step 6: Check the docs**

Run: `git diff README.md docs/development.md | grep '^+' | grep -n "$(printf '\342\200\224')"` and expect nothing: no added line carries an em dash. Run `grep -n 'through the site.s sudo\|journalctl -u wg-quick' README.md docs/development.md` and expect nothing. Read the whole of *`apply` brings the mesh interface up before anything binds to it* once through.

- [ ] **Step 7: Changelog**

`CHANGELOG.md`, `[Unreleased]`, `### Changed`, add:

```markdown
- `doctor` advises reading a down mesh interface's journal with its WireGuard helper's `log`, which the deployment's user can run, instead of `systemctl status` and `journalctl`.
```

- [ ] **Step 8: Run the whole suite and commit**

Run: `go vet ./... && go test ./...`
Expected: PASS.

```bash
git add internal/doctor README.md docs/development.md CHANGELOG.md
git commit -m "docs: name the wireguard helper wherever the mesh is reached

doctor told the operator to run systemctl status and journalctl on a site
whose mesh is down, neither of which the deployment's user can do for a
system unit; it now names the helper's log. README describes the helper
where apply brings the mesh up: why put checks every line, the copy apply
keeps in the deployment's directory, the new read-back of an up interface,
and the journal read through log. development.md says where the helper and
its tests live.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Self-Review Notes

* **Spec coverage.** The helper's verbs, built-in interface, port and subnet, `put`'s sections, keys, patterns, port and subnet rules, the 600 install by temp and rename (Task 2); "the renderer stops writing the relay `PostUp`" (Task 1); "WireGuard writes and `wg`, `systemctl`, `journalctl` on the mesh | the helper" (Tasks 1, 3, 4, 5, 6, and the inventory above); "The helper's `put`: a table of confs, every hook key refused, every unknown key refused, an `Address` outside the subnet refused, the renderer's own output accepted" (Task 2); R9's exit 64 (Task 2); the golden tree regenerated and read (Task 1, Step 7); the README passages the index names (Task 6).
* **Left to later phases, on purpose.** Installing the helper and its sudoers line, `visudo -c`, and the forwarding sysctl are `host admit`'s (phase 5), which calls `hostprep.WGHelperScript(d, site.ListenPort(), cfg.Mesh.Subnet)` or the request's equivalents. `host evict` stopping `wg-quick@psns-<token>` and deleting `/etc/wireguard/psns-<token>.conf` is phase 5's, though the spec's evict list omits it (*Rulings* 6). `site remove`'s host step (`internal/siteremove/host.go`) is phase 6's.
* **Type consistency.** `WGHelper.CommandLine(verb string) string` (Task 3) is used in Tasks 3, 5 and 6; `SyncMesh(plan *Plan, t Transport) error` (Task 3) in Task 5; `handMesh(plan *Plan, t Transport, step WireGuardStep) error` and `helperProblem(site string, h WGHelper, err error) error` (Task 3) in Task 4; `wireguardStep(site, d, change, before, t)` (Task 4); `deployment.WireGuardInstalled() string` (Task 1) in Task 2; `hostprep.WGHelperScript(d deployment.Deployment, listenPort int, subnet string) (string, error)` (Task 2).
* **Placeholders.** None: every code step carries its code, and the mechanical test edits name file, line and replacement.
