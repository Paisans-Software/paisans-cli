# Gateway hand-off: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `paisans gateway move` hands the gateway off a site that keeps the monitor, keeping its Caddy serving every gateway hostname until DNS has moved, then reloading it with the monitor's routes alone.

**Architecture:** a render option marks a departing site as a gateway; `planSiteApply` passes it when the site's host still holds the record or marker; `gatewaymove.Build` finds a departing site by record, marker or Caddyfile routes; a shared move's stage 6 keeps Caddy and a new stage 7 re-applies the site through a hook.

**Tech Stack:** Go, Go's `testing`.

**Spec:** `docs/specs/2026-10-12-gateway-handoff.md`.

## Global Constraints

- Branch `feat/gateway-handoff`, worktree `.worktrees/gateway-handoff`. Never `git stash`.
- `gofmt -l cmd internal` empty, `go vet ./...` and `go test ./...` green before every commit. Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- A plain move (old site does not run Caddy) keeps six stages, byte for byte the same steps, gates and notes.
- No migration code. No rejected alternatives in docs, comments or commits.

## Review Focus

1. A plain monitor site (never a gateway) must never be found as an old gateway, nor rendered departing.
2. A run stopped after stage 6 (record and marker gone) must still find the site and finish the hand-off.
3. A finished shared move must find nothing: its Caddyfile names no gateway hostname.
4. A dry run must never apply anything.
5. A plain old gateway's leftover Caddyfile (its Caddy stopped, files kept) must not be evidence: Caddyfile routes count only for sites whose roles run Caddy.

---

### Task 1: render: `WithDepartingGateway`

**Files:** Modify `internal/render/etcd.go` (beside `WithEtcdInitial`) or a new `internal/render/departing.go`; test in `internal/render/monitor_test.go`.

**Produces:** `func WithDepartingGateway(site string) Option`.

- [ ] Test: fixture `monitorConfig(t, nil)` (watch is a paisans monitor). Build with `render.WithDepartingGateway("watch")`: watch's Caddyfile has `talk.example.org {` and `status.example.org {` and `render.HostSitesMount`; vm's Caddyfile has no `status.example.org`. Without the option, watch's Caddyfile has no `talk.example.org`. With the option on `home-a` (runs no Caddy), nothing changes for home-a.
- [ ] Run it: fails, `WithDepartingGateway` undefined.
- [ ] Implement:

```go
// WithDepartingGateway renders site, which runs Caddy for a monitor, as the
// gateway it still is on its host: paisans-stack.yaml has given the role
// to another site, but until gateway move hands it off, DNS still sends
// every hostname there. Its Caddy keeps every gateway route beside its
// monitor's, the gate's snippets and the host owner's sites.
func WithDepartingGateway(site string) Option {
	return func(p *planner) {
		if s, ok := p.sites[site]; ok && s.RunsCaddy {
			s.IsGateway = true
		}
	}
}
```

- [ ] `go test ./internal/render/` green; full suite; commit `feat(render): render a departing gateway that keeps the monitor`.

### Task 2: gatewaymove: find a departing site and hand it off

**Files:** `internal/gatewaymove/gatewaymove.go`, `internal/gatewaymove/gatewaymove_test.go`.

**Produces:** `Options.ApplySite func(site string) error`; `Evidence.Routes bool`; `func Departing(t apply.Transport, cfg *config.Config) (bool, error)` (record, marker or routes); stage constant `stageHandOff`. Removes `SharedRefusal`.

- [ ] Tests (world harness, home-a given `monitor` and `status` pinned to it; home-a's host serves a Caddyfile naming every gateway hostname; `ApplySite` fake records its call and rewrites home-a's Caddyfile to the status host block alone):
  - `TestAMoveOffASiteThatKeepsTheMonitorHandsItOff`: run → 7 stages, ApplySite called once with home-a, home-a's Caddy still running, record and marker gone, Caddyfile names no gateway hostname, DNS moved.
  - `TestAHandOffStoppedAfterStageSixResumes`: ApplySite fails once; second run finds home-a by its routes, stages 1–6 do nothing, hand-off applies, done.
  - `TestAFinishedHandOffFindsNothing`: after a full run, `build` gives `From == ""`.
  - `TestAPlainMonitorIsNeverAnOldGateway`: watch's host holds a Caddyfile with only status; home-a is the plain old gateway; build finds home-a, six stages.
  - `TestADryRunAppliesNothing`: build + Show, ApplySite never called.
  - Replace `TestAMoveOffASiteThatKeepsTheMonitorIsRefused`.
- [ ] Run: fail for the right reasons (refusal, missing hook).
- [ ] Implement: in `Build`'s `held` loop stop skipping `RunsCaddy` sites; for them use `inspectDeparting` (inspect, drop Caddy, set Routes from the Caddyfile). `any()` includes Routes. Remove the `SharedRefusal` check. When `cfg.Sites[p.From].RunsCaddy()`: stage 6 is `releaseStage` (no Caddy stop, keeps host-sites refusal, removes record and marker, gate: neither held), append `handOffStage` (step "apply <from>'s infra stack so its Caddy serves the monitor alone" when Routes; run calls `ApplySite`, error if nil; gate: no gateway hostname in the Caddyfile), and the notes for a shared site.
- [ ] Green; full suite; commit `feat(gateway): hand the gateway off a site that keeps the monitor`.

### Task 3: cmd: render departing, converge the move, wire the hook

**Files:** `cmd/paisans/main.go` (`planSiteApply`), `cmd/paisans/converge.go` (`convergeGatewayMove`, `convergeHoldsGateway`), `cmd/paisans/gateway.go`, delete `cmd/paisans/gateway_shared.go` and its test, new test file `cmd/paisans/gateway_handoff_test.go`.

- [ ] Tests: a planSiteApply-level test that a shared site whose host holds the record renders departing (its planned Caddyfile names `talk.example.org`) and a plain monitor does not; `convergeGatewayMove` returns true when a `RunsCaddy` site is departing (replace `convergeHoldsGateway` to answer per site).
- [ ] Implement: in `planSiteApply`, for a non-gateway `RunsCaddy` site with `gatewaymove.HoldsRecordOrMarker`, append `render.WithDepartingGateway(site)`. In `convergeGatewayMove` stop skipping `RunsCaddy` sites; `convergeHoldsGateway` uses `gatewaymove.Departing` for them. In `runGatewayMove`, set `ApplySite` when the options leave it nil: `runApply([]string{"--site", site, "--only", "infra", "--execute", "--config", cfgPath, "--secrets", secretsPath, "--sudo=" + strconv.FormatBool(sudo)}, os.Stdin, os.Stdout)`.
- [ ] Green; full suite; commit `feat(apply): a departing gateway keeps its routes until handed off`.

### Task 4: README

- [ ] "Moving the gateway": the stage table notes stage 6's shared variant and adds row 7; a paragraph on what a departing shared site keeps. The monitor section's refusal paragraph becomes how the hand-off works. Keep *Moving an existing monitor*.
- [ ] Commit `docs: the gateway hand-off`.
