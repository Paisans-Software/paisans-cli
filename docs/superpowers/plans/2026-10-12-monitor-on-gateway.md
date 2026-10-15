# Monitor on the gateway's machine: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the uptime monitor share a site with the gateway or the witness, with one warning, and refuse moving the gateway off such a site until the hand-off lands.

**Architecture:** validate turns two refusals into one per-site warning and adds `web-server-on-gateway`; render's Caddy route planning lets a gateway serve a monitor on its own site; doctor words a shared site's loss correctly; `gateway move` and `apply --site` refuse a shared site that still holds the gateway's record; the create wizard offers the choice.

**Tech Stack:** Go (the `paisans` CLI), Go's `testing`, YAML fixtures.

**Spec:** `docs/specs/2026-10-12-monitor-on-gateway.md`. Read it first. This plan covers everything in it except the second pull request under *Moving the gateway off a shared site: Second: hand it off*, which gets its own spec and plan.

## Global Constraints

- Work in `src/paisans-stack/.worktrees/monitor-on-gateway`, branch `feat/monitor-on-gateway`, which is stacked on `fix/wizard-site-prompt` (PR #199). Never `git stash`.
- Run `gofmt -l cmd internal` (expect no output), `go vet ./...` and `go test ./...` before every commit.
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- No migration code: nothing converts an older `paisans-stack.yaml`.
- Docs, comments and commits never describe rejected alternatives; the spec is the only record of them.
- Comments match the surrounding code: full sentences, the *why*, founder decisions named as such.
- Rule names, exactly: `monitor-shares-a-site` (warn), `web-server-on-gateway` (refuse). `monitor-on-gateway` and `monitor-on-witness` cease to exist as rules.
- The wizard's option 2 text, exactly: `on <site> (takes the monitor down with it, no alerts when the gateway fails)`.

## Review Focus

1. **A monitor beside the gateway on a deployment with several gateways during a move.** `routesFor` must serve the status page only from the gateway that is its monitor site, never from the other gateway. Task 2 pins it.
2. **Re-answering the machines section after choosing "on the gateway".** Renaming the gateway site must not lose the monitor role. Task 5 pins it.
3. **`apply --site` on a plain monitor-only site.** It must not be refused: only a site holding the record or the marker is. Task 4 pins it.
4. **A move from a plain old gateway** (not a monitor) must still run every stage. Task 4 runs the existing suite unchanged.
5. **The high availability shape with the monitor on the VPS** must still place chat beside the gateway, not on a data site. Task 5 pins it.

---

### Task 1: validate: one warning for a shared monitor, and `web-server-on-gateway`

**Files:**
- Modify: `internal/validate/monitor.go` (the `monitorRoles`, `nothingWatches` and `webServer` functions)
- Modify: `internal/validate/validate_test.go:116-123` (the `TestRulesFire` cases)
- Create: `internal/validate/testdata/web-server-on-gateway.yaml`
- Create: `internal/validate/monitor_test.go`
- Modify: header comments of `internal/validate/testdata/monitor-on-gateway.yaml` and `monitor-on-witness.yaml` (they now draw a warning)

**Interfaces:**
- Produces: rule `monitor-shares-a-site` (Warn) at key `sites.<name>.roles`; rule `web-server-on-gateway` (Refuse) at key `sites.<name>.web_server`. Tasks 2 and 5 expect these names.

- [ ] **Step 1: Write the failing tests**

In `internal/validate/validate_test.go`, replace the two refusal cases and add the new rule:

```go
		{"monitor-on-gateway", "monitor-shares-a-site", validate.Warn},
		{"monitor-on-witness", "monitor-shares-a-site", validate.Warn},
		{"monitor-shares-a-site", "monitor-shares-a-site", validate.Warn},
		{"web-server-on-gateway", "web-server-on-gateway", validate.Refuse},
```

Create `internal/validate/testdata/web-server-on-gateway.yaml` as a copy of `monitor-on-gateway.yaml` with the first line changed to `# monitor-on-gateway.yaml with a web_server block on the gateway.` and these lines added under the `vm:` site, after its `public_address`:

```yaml
    web_server:
      managed_by: paisans
```

Create `internal/validate/monitor_test.go`:

```go
package validate_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

// sharing returns the messages monitor-shares-a-site gave, by key.
func sharing(result validate.Result) map[string]string {
	out := map[string]string{}
	for _, f := range result.Findings {
		if f.Rule == "monitor-shares-a-site" {
			out[f.Key] = f.Message
		}
	}
	return out
}

// One warning per site, naming every role the monitor shares, gateway first,
// and saying what it cannot report.
func TestMonitorSharesASiteNamesWhatItShares(t *testing.T) {
	for _, tc := range []struct {
		fixture, key string
		want        []string
	}{
		{"monitor-on-gateway", "sites.vm.roles", []string{"beside gateway and witness", "It cannot report vm failing", "Nothing reports etcd losing its tiebreaker either", "roles: [monitor]"}},
		{"monitor-on-witness", "sites.watch.roles", []string{"beside witness.", "Nothing reports etcd losing its tiebreaker either"}},
		{"monitor-shares-a-site", "sites.home-a.roles", []string{"beside data and apps.", "It cannot report home-a failing"}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			got := sharing(validate.Check(load(t, tc.fixture)))
			if len(got) != 1 {
				t.Fatalf("want one warning, got %v", got)
			}
			msg, ok := got[tc.key]
			if !ok {
				t.Fatalf("no warning at %s: %v", tc.key, got)
			}
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("message lacks %q: %s", w, msg)
				}
			}
		})
	}
}

// A monitor alone, or beside storage alone, hides nothing and is not warned.
func TestAMonitorAloneOrBesideStorageIsNotWarned(t *testing.T) {
	cfg := load(t, "valid")
	if got := sharing(validate.Check(cfg)); len(got) != 0 {
		t.Fatalf("a monitor alone was warned: %v", got)
	}
	watch := cfg.Sites["watch"]
	watch.Roles = []config.Role{config.RoleMonitor, config.RoleStorage}
	cfg.Sites["watch"] = watch
	if got := sharing(validate.Check(cfg)); len(got) != 0 {
		t.Fatalf("a monitor beside storage was warned: %v", got)
	}
}

// web_server on a gateway is refused in either mode: the gateway's Caddy
// serves the monitor there.
func TestWebServerOnGatewayIsRefusedInEitherMode(t *testing.T) {
	for _, ws := range []*config.WebServer{
		{ManagedBy: config.ManagedByPaisans},
		{ManagedBy: config.ManagedByExternal, Listen: "127.0.0.1:8480"},
	} {
		cfg := load(t, "monitor-on-gateway")
		vm := cfg.Sites["vm"]
		vm.WebServer = ws
		cfg.Sites["vm"] = vm
		if !refusedFor(validate.Check(cfg), "web-server-on-gateway", "sites.vm.web_server") {
			t.Errorf("managed_by %s on the gateway was not refused", ws.ManagedBy)
		}
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/validate/ -run 'TestRulesFire|TestMonitorShares|TestAMonitorAlone|TestWebServerOnGateway'`
Expected: FAIL. `monitor-on-gateway` still fires as a refusal, and `web-server-on-gateway` does not exist.

- [ ] **Step 3: Implement**

In `internal/validate/monitor.go`, replace the doc comment of `monitorRoles` and its first loop's three role checks (the `RoleGateway`, `RoleWitness` and `RoleData || RoleApps` blocks) with:

```go
// monitorRoles checks where the uptime monitor may run. README.md: "The
// monitor has a site of its own".
//
// A monitor exists to report the failures of everything else, and it cannot
// report the machine it runs on. A machine of its own is the recommendation,
// but a deployment may have no machine to spare, so a monitor beside any other
// role is allowed with one warning that names what it shares and what then
// goes unreported. Founder decision, 2026-10-12, which made the gateway and
// the witness warnings rather than refusals. Storage is left out: a monitor's
// absence hides nothing about it.
//
// The monitor serves its own hostname from its own public address, so that
// address must be declared. Founder decision.
func (c *checker) monitorRoles() {
	for _, name := range c.cfg.SiteNames() {
		site := c.cfg.Sites[name]
		if !site.Has(config.RoleMonitor) {
			continue
		}
		key := fmt.Sprintf("sites.%s.roles", name)
		var shared []string
		for _, r := range []config.Role{config.RoleGateway, config.RoleWitness, config.RoleData, config.RoleApps} {
			if site.Has(r) {
				shared = append(shared, string(r))
			}
		}
		if len(shared) > 0 {
			witness := ""
			if site.Has(config.RoleWitness) {
				witness = " Nothing reports etcd losing its tiebreaker either."
			}
			c.warn("monitor-shares-a-site", key,
				"monitor shares a site",
				"holds monitor beside %s. It cannot report %s failing: when it goes down, the monitor goes with it and no alert is sent.%s A small machine of its own with roles: [monitor] reports it.", andList(shared), name, witness)
		}
```

Keep the rest of the loop (`monitor-without-uptime`, `monitor-without-public-address`) and the second loop unchanged. In the `uptime-needs-a-monitor-site` message, replace `The monitor runs on a site of its own role, never the gateway or the witness, and serves its own hostname rather than through the gateway.` with `The monitor runs on a site holding the monitor role, and serves its own hostname from it.`

Add below `hostsUptime`:

```go
// andList joins words as a sentence does: "a", "a and b", "a, b and c".
func andList(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}
```

In `nothingWatches`, replace the message's last sentence `Give a second machine roles: [monitor] with a public_address, pin an uptime app to it, and apply it (README.md, \"Monitoring\").` with `Declare an uptime app pinned to a site with the monitor role, ideally a small machine of its own with a public_address, and apply it (README.md, \"Monitoring\").`, and in its doc comment replace `since a first install may have one machine and the monitor needs a second, but it is` with `but it is`.

In `webServer`, directly after the `web-server-outside-monitor` block's closing brace (inside the loop, before `managedBy, listen := ...`), add:

```go
		if site.Has(config.RoleGateway) {
			c.refuse("web-server-on-gateway", key,
				"web_server is declared on a gateway",
				"is declared on %s, which also holds the gateway role. The gateway's Caddy serves the monitor there, so the block would be ignored, or would hand 80 and 443 to a second web server that Caddy already holds. Remove the block.", name)
			continue
		}
```

and add to its doc comment, after the first sentence about only a monitor carrying one: `A monitor beside the gateway is served by the gateway's Caddy, so it carries none either.`

Update the first line of `testdata/monitor-on-gateway.yaml` to `# valid.yaml with the monitor role on the gateway, which is also the witness: allowed, with a warning.` and of `testdata/monitor-on-witness.yaml` to `# valid.yaml with a monitor site that is also the witness, in place of vm in etcd: allowed, with a warning.`

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/validate/`
Expected: PASS. If another test pinned the old refusals, it names `monitor-on-gateway`; change it to expect the warning.

- [ ] **Step 5: Commit**

```bash
gofmt -l cmd internal; go vet ./... && go test ./...
git add internal/validate
git commit -m "feat(validate): a monitor may share the gateway or the witness, with a warning

monitor-on-gateway and monitor-on-witness were refusals. They and
monitor-shares-a-site become one warning per site naming every role the
monitor shares. web-server-on-gateway refuses a web_server block where
the gateway's Caddy serves the monitor.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

`go test ./...` may fail in `internal/create` (`TestNamedShapesValidate` expects `no-uptime-monitor` text only by rule name, so it should pass) or elsewhere on a test that pinned the old refusal by name: fix that test to the new rule in this commit.

---

### Task 2: render: the gateway serves a monitor on its own site

**Files:**
- Modify: `internal/render/site.go` (`routesFor`, the `switch` at about line 826)
- Modify: `internal/render/monitor_test.go` (add tests)

**Interfaces:**
- Consumes: Task 1's rules (the test config must pass validate with no refusals).

- [ ] **Step 1: Write the failing tests**

Append to `internal/render/monitor_test.go`:

```go
// monitorOnGateway is the fixture with the watch site gone and the monitor
// on vm, the gateway and witness, with the public address a monitor needs.
func monitorOnGateway(t *testing.T) *config.Config {
	t.Helper()
	cfg := fixture(t)
	delete(cfg.Sites, "watch")
	vm := cfg.Sites["vm"]
	vm.Roles = append(vm.Roles, config.RoleMonitor)
	vm.PublicAddress = "203.0.113.10"
	cfg.Sites["vm"] = vm
	status := cfg.Apps["status"]
	status.Placement = config.Placement{Mode: config.PlacementPinned, Site: "vm"}
	cfg.Apps["status"] = status
	if refusals := validate.Check(cfg).Refusals(); len(refusals) > 0 {
		t.Fatalf("the monitor on the gateway is refused: %v", refusals)
	}
	return cfg
}

// The monitor beside the gateway shares its Caddy: one Caddyfile carries the
// status page and every ordinary app, and one claim each on 80 and 443.
func TestAMonitorOnTheGatewayIsServedByTheGatewaysCaddy(t *testing.T) {
	cfg := monitorOnGateway(t)
	files := planFiles(mustBuild(t, cfg))
	gateway := files["vm/srv/paisans/f2a9/infra/caddy/Caddyfile"]
	for _, host := range []string{"status.example.org {", "talk.example.org {"} {
		if !strings.Contains(gateway, host) {
			t.Errorf("the gateway's Caddyfile lacks %q:\n%s", host, gateway)
		}
	}
	if _, ok := files["vm/srv/paisans/f2a9/infra/caddy/snippets/status.caddy"]; !ok {
		t.Error("the gateway carries no snippet for the monitor")
	}
	claims := map[int]int{}
	for _, l := range render.SiteListeners(cfg, "vm") {
		if l.Owner == "Caddy" && l.Proto == "tcp" && (l.Port == 80 || l.Port == 443) {
			claims[l.Port]++
		}
	}
	if claims[80] != 1 || claims[443] != 1 {
		t.Errorf("claims on 80 and 443: %v", claims)
	}
}

// Every other site is still pinged and still sends its heartbeat; the
// monitor's own site, the gateway, is not pinged.
func TestAMonitorOnTheGatewayPingsEveryOtherSite(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	delete(cfg.Sites, "watch")
	vm := cfg.Sites["vm"]
	vm.Roles = append(vm.Roles, config.RoleMonitor)
	vm.PublicAddress = "203.0.113.10"
	cfg.Sites["vm"] = vm
	status := cfg.Apps["status"]
	status.Placement = config.Placement{Mode: config.PlacementPinned, Site: "vm"}
	cfg.Apps["status"] = status
	monitors := byName(renderSeedAt(t, cfg, secrets, "vm/srv/paisans/f2a9/status/monitors.json"))
	for _, name := range []string{"home-a — ping", "home-b — ping"} {
		if monitors[name] == nil {
			t.Errorf("%s is missing", name)
		}
	}
	if monitors["vm — ping"] != nil {
		t.Error("the monitor pings its own site")
	}
	if monitors["talk — gate"] == nil {
		t.Error("an app on the monitor's machine lost its checks")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/render/ -run 'TestAMonitorOnTheGateway'`
Expected: FAIL in `TestAMonitorOnTheGatewayIsServedByTheGatewaysCaddy`: the gateway's Caddyfile lacks `status.example.org {`. The ping test may already pass; that is the spec's "already holds".

- [ ] **Step 3: Implement**

In `routesFor` in `internal/render/site.go`, change the doc comment's last sentence to `A gateway routes every app that no other site's monitor serves; a monitor that is not a gateway routes only the apps pinned to it.` and the first `case`:

```go
		case site.IsGateway && onMonitor && monitor != site.Name:
			continue
```

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/render/`
Expected: PASS, including `TestMonitorAppsLeaveTheGatewayCaddyfile` (a monitor on its own site still leaves the gateway).

- [ ] **Step 5: Commit**

```bash
gofmt -l cmd internal; go vet ./... && go test ./...
git add internal/render
git commit -m "fix(render): a gateway serves a monitor that shares its site

routesFor skipped every monitor app on a gateway, so a monitor beside the
gateway was routed by nothing. A gateway now skips only an app another
site's monitor serves.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: doctor: a shared site's loss is every hostname

**Files:**
- Modify: `internal/doctor/reach.go` (`losses`, the gateway `switch` at about line 77)
- Modify: `internal/doctor/doctor_test.go` (add a test)

- [ ] **Step 1: Write the failing test**

Append to `internal/doctor/doctor_test.go`:

```go
// A gateway that also runs the monitor takes every public hostname with it,
// the monitor's included.
func TestASharedGatewaysLossIsEveryHostname(t *testing.T) {
	cfg := fixture(t)
	delete(cfg.Sites, "watch")
	vm := cfg.Sites["vm"]
	vm.Roles = append(vm.Roles, config.RoleMonitor)
	cfg.Sites["vm"] = vm
	got := losses(cfg, "vm")
	for _, want := range []string{"its gateway, which every public hostname goes through", "the uptime monitor"} {
		if !strings.Contains(got, want) {
			t.Errorf("vm's loss lacks %q: %s", want, got)
		}
	}
	if strings.Contains(got, "but the monitor's") {
		t.Errorf("vm's loss excepts the monitor it runs: %s", got)
	}
}
```

If `config` is not yet imported in `doctor_test.go`, add `"github.com/paisans-software/paisans-stack/internal/config"`.

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/doctor/ -run TestASharedGatewaysLossIsEveryHostname`
Expected: FAIL: the loss says "every public hostname but the monitor's".

- [ ] **Step 3: Implement**

In `losses`, change the first `case`:

```go
		case len(cfg.GatewaySites()) == 1 && len(cfg.MonitorSites()) > 0 && !declared.Has(config.RoleMonitor):
```

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/doctor/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l cmd internal; go vet ./... && go test ./...
git add internal/doctor
git commit -m "fix(doctor): a gateway running the monitor loses every hostname

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: refuse moving the gateway off a site that keeps the monitor

**Files:**
- Modify: `internal/gatewaymove/gatewaymove.go` (add `HoldsRecordOrMarker`, `SharedRefusal`; a check in `Build` after `p.From` is set)
- Modify: `internal/gatewaymove/gatewaymove_test.go` (add a test)
- Create: `cmd/paisans/gateway_shared.go`
- Create: `cmd/paisans/gateway_shared_test.go`
- Modify: `cmd/paisans/main.go` (single-site `runApply`, just before `host, err := hostGate(r, cfg, *site, transport)` at about line 540)

**Interfaces:**
- Produces, in package `gatewaymove`:
  - `func HoldsRecordOrMarker(t apply.Transport, d deployment.Deployment) (bool, error)`
  - `func SharedRefusal(site string) error`
- Produces, in package `main` (`cmd/paisans`): `func refuseSharedGatewayMove(cfg *config.Config, site string, t apply.Transport) error`

- [ ] **Step 1: Write the failing tests**

Append to `internal/gatewaymove/gatewaymove_test.go`:

```go
// The old gateway kept the monitor: the move finds it through DNS but must
// not stop its Caddy, which still serves the monitor, so it is refused
// before anything runs.
func TestAMoveOffASiteThatKeepsTheMonitorIsRefused(t *testing.T) {
	w := newWorld(t, func(s string) string {
		s = strings.Replace(s, "  home-a:\n    roles: [data, apps]\n", "  home-a:\n    roles: [data, apps, monitor]\n", 1)
		return strings.Replace(s, "placement: { pinned: watch }", "placement: { pinned: home-a }", 1)
	})
	_, err := w.build()
	if err == nil || !strings.Contains(err.Error(), "keeps the monitor") {
		t.Fatalf("want the shared-site refusal, got %v", err)
	}
	if len(w.hosts["home-a"].caddy) != 1 {
		t.Error("home-a's Caddy was touched")
	}
	held, err := gatewaymove.HoldsRecordOrMarker(w.hosts["home-a"], w.cfg.Deployment())
	if err != nil || !held {
		t.Errorf("home-a holds the record: %v %v", held, err)
	}
	held, err = gatewaymove.HoldsRecordOrMarker(w.hosts["home-b"], w.cfg.Deployment())
	if err != nil || held {
		t.Errorf("home-b holds nothing: %v %v", held, err)
	}
}
```

Create `cmd/paisans/gateway_shared_test.go`:

```go
package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployrecord"
)

// fileHost is a host that answers file reads and nothing else.
type fileHost struct{ files map[string]string }

func (h fileHost) Describe() string { return "fake" }
func (h fileHost) Run(string) (string, error) {
	return "", errors.New("unexpected command")
}
func (h fileHost) RunInput(string, string) (string, error) {
	return "", errors.New("unexpected command")
}
func (h fileHost) ReadFile(p string) (string, bool, error) {
	c, ok := h.files[p]
	return c, ok, nil
}
func (h fileHost) WriteFile(string, string, uint32) error { return errors.New("unexpected write") }

func sharedFixture(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "..", "internal", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// apply --site on a monitor site still holding the gateway's record is
// refused; on a monitor site holding nothing, and on the gateway, it is not.
func TestApplyRefusesASiteThatKeepsTheMonitorAndTheGatewaysRecord(t *testing.T) {
	cfg := sharedFixture(t)
	record := fileHost{files: map[string]string{deployrecord.Path(cfg.Deployment()): "{}"}}
	err := refuseSharedGatewayMove(cfg, "watch", record)
	if err == nil || !strings.Contains(err.Error(), "keeps the monitor") {
		t.Fatalf("watch with the record: %v", err)
	}
	if err := refuseSharedGatewayMove(cfg, "watch", fileHost{files: map[string]string{}}); err != nil {
		t.Errorf("a plain monitor was refused: %v", err)
	}
	if err := refuseSharedGatewayMove(cfg, "vm", record); err != nil {
		t.Errorf("the gateway was refused: %v", err)
	}
	if err := refuseSharedGatewayMove(cfg, "home-a", record); err != nil {
		t.Errorf("a site running no Caddy was refused: %v", err)
	}
}
```

If `fileHost`'s method set does not satisfy `apply.Transport` (`internal/apply/transport.go:33`), add the missing methods returning `errors.New("unexpected")`.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/gatewaymove/ ./cmd/paisans/ -run 'TestAMoveOffASiteThatKeepsTheMonitor|TestApplyRefusesASiteThatKeepsTheMonitor'`
Expected: FAIL to build: `HoldsRecordOrMarker` and `refuseSharedGatewayMove` are undefined.

- [ ] **Step 3: Implement**

In `internal/gatewaymove/gatewaymove.go`, below `Holds`:

```go
// HoldsRecordOrMarker reports whether the host t reaches holds d's deployment
// record or a move's marker, and not its Caddy. Only a gateway is ever given
// the record, so on a site that runs Caddy for a monitor and no longer holds
// the gateway role, either one is a gateway being moved off a site that keeps
// the monitor.
func HoldsRecordOrMarker(t apply.Transport, d deployment.Deployment) (bool, error) {
	for _, path := range []string{deployrecord.Path(d), MarkerPath(d)} {
		_, found, err := t.ReadFile(path)
		if err != nil {
			return false, fmt.Errorf("%s: reading %s: %w", t.Describe(), path, err)
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

// SharedRefusal is the problem a move off a site that keeps the monitor is
// refused with, by the move and by apply alike. Handing such a site off,
// so that its Caddy keeps serving the gateway's hostnames until DNS has
// moved and then serves the monitor's alone, is not built yet; stopping its
// Caddy as a plain old gateway's would take the monitor down, and applying
// it first would stop the overlap the move exists for.
func SharedRefusal(site string) error {
	return refuse(site+" keeps the monitor and is still this deployment's gateway",
		"Moving the gateway off a site that keeps the monitor is not supported yet. Give "+site+" the gateway role back and apply it, or pin the status page to another monitor site first, then move the gateway.")
}
```

In `Build`, directly after `p.From = candidates[0]`:

```go
	if cfg.Sites[p.From].RunsCaddy() {
		return nil, SharedRefusal(p.From)
	}
```

Create `cmd/paisans/gateway_shared.go`:

```go
package main

import (
	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/gatewaymove"
)

// refuseSharedGatewayMove refuses to apply a site that paisans-stack.yaml no
// longer gives the gateway role, that runs Caddy for a monitor, and whose
// host still holds the gateway's record or a move's marker. Applying it
// would rewrite its Caddy to the monitor's routes alone while DNS still sends
// every hostname there. apply without --site applies each site through here,
// so it is covered too.
func refuseSharedGatewayMove(cfg *config.Config, site string, t apply.Transport) error {
	s := cfg.Sites[site]
	if s.Has(config.RoleGateway) || !s.RunsCaddy() {
		return nil
	}
	held, err := gatewaymove.HoldsRecordOrMarker(t, cfg.Deployment())
	if err != nil {
		return err
	}
	if held {
		return gatewaymove.SharedRefusal(site)
	}
	return nil
}
```

In `cmd/paisans/main.go`'s single-site `runApply`, directly before `host, err := hostGate(r, cfg, *site, transport)`:

```go
	if err := refuseSharedGatewayMove(cfg, *site, transport); err != nil {
		return err
	}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/gatewaymove/ ./cmd/paisans/`
Expected: PASS, every existing move test included (a move from a plain old gateway is unchanged). A `cmd/paisans` apply test whose fake transport fails on unexpected reads may now fail on the two `ReadFile` calls for a monitor site: give that fake an empty answer for them.

- [ ] **Step 5: Commit**

```bash
gofmt -l cmd internal; go vet ./... && go test ./...
git add internal/gatewaymove cmd/paisans
git commit -m "feat(gateway): refuse moving the gateway off a site that keeps the monitor

The move would stop that site's Caddy, taking the monitor down, and
apply would first rewrite it to the monitor's routes while DNS still
pointed every hostname there. Both refuse until the hand-off is built.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: create: offer the status page on the gateway

**Files:**
- Modify: `internal/create/answers.go` (`Answers`, `AllSites`)
- Modify: `internal/create/wizard.go` (`monitor`, and `apps` where it clears `a.Monitor`)
- Modify: `internal/create/render.go:125` (the chat placement rule)
- Modify: `internal/create/wizard_test.go` (replace `TestDecliningTheMonitorMachineDropsTheStatusPage`; add tests)
- Modify: `internal/create/render_test.go` (`TestNamedShapesValidate` gains monitor-on-gateway cases)

**Interfaces:**
- Consumes: Task 1's `monitor-shares-a-site`; Task 2's routing (the shapes must render).
- Produces: `Answers.MonitorOnGateway bool`.

- [ ] **Step 1: Write the failing tests**

In `internal/create/wizard_test.go`, replace `TestDecliningTheMonitorMachineDropsTheStatusPage` with:

```go
// Declining the status page leaves no status page and no machine for it.
func TestDecliningTheStatusPageDropsIt(t *testing.T) {
	var lines []string
	lines = append(lines, communityAnswers...)
	lines = append(lines, adminAnswers...)
	lines = append(lines, shapeAnswers[ShapeOneMachine]...)
	lines = append(lines, "", "n", "n", "n", "n")
	lines = append(lines, mailAnswers...)
	w, out, err := run(t, script(lines...))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, ok := w.Answers.App(statusApp); ok || w.Answers.Monitor != nil || w.Answers.MonitorOnGateway {
		t.Errorf("the status page or its machine is still declared: %+v", w.Answers)
	}
}

// Choosing the gateway puts the monitor beside it, offered by the gateway
// site's name with what it costs, and answering the machines again with the
// gateway renamed keeps it there.
func TestTheStatusPageOnTheGateway(t *testing.T) {
	var lines []string
	lines = append(lines, communityAnswers...)
	lines = append(lines, adminAnswers...)
	lines = append(lines, shapeAnswers[ShapeHomeAndVPS]...)
	lines = append(lines,
		"",         // sign in hostname
		"n", "n", "n", // no forums, wiki or blog
		"n",    // no chat
		"", "", // status page: yes, hostname
		"2",    // on the gateway
	)
	lines = append(lines, mailAnswers...)
	w, out, err := run(t, script(lines...))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := "2) on gateway (takes the monitor down with it, no alerts when the gateway fails)"; !strings.Contains(out, want) {
		t.Errorf("missing %q:\n%s", want, out)
	}
	if !w.Answers.MonitorOnGateway || w.Answers.Monitor != nil {
		t.Fatalf("not on the gateway: %+v", w.Answers)
	}
	w.Answers.Sites[1].Name = "edge"
	sites := w.Answers.AllSites()
	if len(sites) != 2 || !sites[1].Has(config.RoleMonitor) || sites[0].Has(config.RoleMonitor) {
		t.Fatalf("the monitor is not on the renamed gateway: %+v", sites)
	}
	if w.Answers.Sites[1].Has(config.RoleMonitor) {
		t.Error("AllSites changed the answered sites")
	}
	data, err := Render(w.Answers, testID, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	if got := rules(check(t, data).Warnings()); !slices.Contains(got, "monitor-shares-a-site") || slices.Contains(got, "no-uptime-monitor") {
		t.Errorf("warnings %v", got)
	}
}
```

Add `"slices"` and `"github.com/paisans-software/paisans-stack/internal/config"` to the test file's imports if missing. `check`, `rules`, `testID` and `testOrigin` are the helpers `render_test.go` already defines in this package.

In `internal/create/render_test.go`'s `TestNamedShapesValidate`, add a field `onGateway bool` to the case struct, add these cases to the table:

```go
		{ShapeOneMachine, true, []string{"backups-off", "monitor-shares-a-site"}, true},
		{ShapeHomeAndVPS, true, []string{"backups-off", "monitor-shares-a-site"}, true},
		{ShapeHighAvailability, true, []string{"backups-off", "garage-two-sites-stop-uploads", "monitor-shares-a-site", "pinned-app-on-witness"}, true},
```

(existing cases get `false` as the fourth value), include `onGateway` in the subtest name, and build the answers as:

```go
				a := answersFor(c.shape, c.all, gated)
				if c.onGateway {
					a.Monitor, a.MonitorOnGateway = nil, true
				}
				data, err := Render(a, testID, testOrigin)
```

`rules` returns sorted, de-duplicated rule names, so the expected lists above are sorted. If the high availability case shows `port-collision` or places chat on a data site, Step 3's render change is what fixes it.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/create/`
Expected: FAIL to build: `MonitorOnGateway` is undefined.

- [ ] **Step 3: Implement**

In `internal/create/answers.go`, below `Monitor *Site`:

```go
	// MonitorOnGateway puts the status page beside the gateway instead of on
	// a machine of its own: AllSites adds the monitor role to the site holding
	// the gateway role, so answering the sites again keeps it.
	MonitorOnGateway bool
```

and replace `AllSites`:

```go
// AllSites is every site in the order the file declares them: the shape's,
// the gateway's holding the monitor role too when the status page is beside
// it, then the monitor's own machine.
func (a Answers) AllSites() []Site {
	sites := append([]Site{}, a.Sites...)
	if a.MonitorOnGateway {
		for i, s := range sites {
			if s.Has(config.RoleGateway) {
				s.Roles = append(append([]config.Role{}, s.Roles...), config.RoleMonitor)
				sites[i] = s
				break
			}
		}
	}
	if a.Monitor != nil {
		sites = append(sites, *a.Monitor)
	}
	return sites
}
```

In `internal/create/wizard.go`, in `apps`, where the status page is not chosen (`} else { a.Monitor = nil }`), clear both: `a.Monitor, a.MonitorOnGateway = nil, false`. Replace `monitor` with:

```go
// monitor places the status page, on a machine of its own or beside the
// gateway, and reports whether the status page is kept.
func (w *Wizard) monitor() (bool, error) {
	a := &w.Answers
	for _, s := range a.Sites {
		if s.Has(config.RoleMonitor) {
			a.Monitor, a.MonitorOnGateway = nil, false
			return true, nil
		}
	}
	w.p.Say("\nThe status page reports the other machines failing, so it is best on a")
	w.p.Say("machine of its own.")
	if gateway := w.publicGateway(); gateway != "" {
		def := 0
		if a.MonitorOnGateway {
			def = 1
		}
		i, err := w.p.Choose("Where it runs", []string{
			"a machine of its own (recommended)",
			"on " + gateway + " (takes the monitor down with it, no alerts when the gateway fails)",
		}, def)
		if err != nil {
			return false, err
		}
		if i == 1 {
			a.Monitor, a.MonitorOnGateway = nil, true
			return true, nil
		}
	}
	a.MonitorOnGateway = false
	w.p.Say("It serves its own hostname, so it needs a public address.")
	d := Site{Name: monitorSite, SSHUser: "ubuntu"}
	if a.Monitor != nil {
		d = *a.Monitor
	}
	name, err := w.p.Text("Name", d.Name, notIn(w.siteNames(a.Sites), "machine", checkSiteName))
	if err != nil {
		return false, err
	}
	site := Site{Name: name, Roles: []config.Role{config.RoleMonitor}}
	if site.PublicAddress, err = w.p.Text("Public IPv4 address", d.PublicAddress, checkIPv4); err != nil {
		return false, err
	}
	if err := w.ssh(&site, d); err != nil {
		return false, err
	}
	a.Monitor = &site
	return true, nil
}

// publicGateway is the one site holding the gateway role with a public
// address, which the status page needs to sit beside it, or "" when there is
// not exactly one.
func (w *Wizard) publicGateway() string {
	var found []string
	for _, s := range w.Answers.Sites {
		if s.Has(config.RoleGateway) && s.PublicAddress != "" {
			found = append(found, s.Name)
		}
	}
	if len(found) != 1 {
		return ""
	}
	return found[0]
}
```

The shared `appAnswers` test helper keeps its line count: the `""` that answered "Add a machine for it?" now takes the default of "Where it runs". Correct its comment from `// its machine: add, name, address, ssh host, user` to `// its machine: where (its own), name, address, ssh host, user`.

In `internal/create/render.go`, change the chat rule and its comment:

```go
		// The chat server binds the ports Patroni and bg_mon do, so it goes
		// where no data is, and off a monitor that is not the gateway, which
		// serves the monitor alone; its web client binds 80, which the
		// gateway's Caddy holds, so it goes where no gateway is.
		if chat == "" && !s.Has(config.RoleData) && (!s.Has(config.RoleMonitor) || s.Has(config.RoleGateway)) {
```

- [ ] **Step 4: Run the package tests and refresh the golden files**

Run: `go test ./internal/create/`
Expected: PASS. If `TestWizardWritesEachNamedShape` reports a golden file differs, run `go test ./internal/create/ -update`, read `git diff internal/create/testdata`, and confirm only expected lines changed (none should: the golden shapes keep the monitor on its own machine). Then run `go test ./cmd/paisans/ -run TestCreate`.

- [ ] **Step 5: Commit**

```bash
gofmt -l cmd internal; go vet ./... && go test ./...
git add internal/create
git commit -m "feat(create): offer the status page beside the gateway

After choosing a status page, the wizard offers a machine of its own,
recommended, or the gateway, saying what that costs. Declining is
answering no to the status page; the separate yes/no for its machine
goes. Chat may sit beside a monitor that is the gateway.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: documentation

**Files:**
- Modify: `README.md`, section "### The monitor has a site of its own" (around line 631), and the `#### Moving the gateway` stage notes if they mention monitors
- Modify: `docs/specs/2026-10-08-monitor-role-and-host-check.md` (a note at the top of Part 1)

- [ ] **Step 1: Rewrite the README section's table and prose**

Replace the rules table with:

```markdown
| Combination | Result | Rule |
|---|---|---|
| `monitor` with `gateway`, `witness`, `data` or `apps` | warned, once per site | `monitor-shares-a-site` |
| `web_server` on a site holding `gateway` | refused | `web-server-on-gateway` |
| `monitor` with no uptime app pinned to it | refused | `monitor-without-uptime` |
| an uptime app pinned to a site without `monitor` | refused | `uptime-needs-a-monitor-site` |
| `monitor` without `public_address` | refused | `monitor-without-public-address` |
| no app of kind `uptime` at all | warned | `no-uptime-monitor` |
```

Replace the paragraph that begins "On the gateway the monitor goes dark" and ends "`roles: []` stays legal for a site hosting a pinned app of any other kind." with:

```markdown
A machine of its own is the recommendation: a small VM or box declared
`roles: [monitor]`, which needs no other role. A deployment with no machine to
spare may put the monitor beside the gateway, the witness, `data` or `apps`,
and validate warns once, naming what it shares (Founder decision,
2026-10-12). Such a monitor still checks every app and container on its
machine and every other site, but it cannot report its own machine failing:
when that goes down the monitor goes with it, and no alert is sent. Beside the
witness, nothing reports etcd losing its tiebreaker either. Beside the
gateway, the gateway's Caddy serves the monitor's hostname, so that site
carries no `web_server` block (`web-server-on-gateway`). `roles: []` stays
legal for a site hosting a pinned app of any other kind.

`paisans gateway move` does not yet move the gateway off a site that keeps
the monitor, and refuses, as does `paisans apply --site` on that site once
the gateway role has left it: stopping its Caddy would take the monitor down.
Pin the status page to another monitor site first, then move the gateway.
```

In the section's first paragraph and in "### Monitoring" intro, change any sentence saying the monitor is never the gateway to match. Search: `grep -n "never the gateway" README.md` and reword each hit about the monitor's placement to "on a site holding the monitor role" (leave hits about other subjects alone).

- [ ] **Step 2: Note the amendment in the 2026-10-08 spec**

At the top of `## Part 1: the \`monitor\` role` in `docs/specs/2026-10-08-monitor-role-and-host-check.md`, add:

```markdown
> **Amended 2026-10-12** by `2026-10-12-monitor-on-gateway.md`: a monitor
> beside the gateway or the witness is a warning, not a refusal.
```

- [ ] **Step 3: Check the docs read true**

Run: `grep -n "monitor-on-gateway\|monitor-on-witness" README.md docs/specs/2026-10-12-monitor-on-gateway.md`
Expected: no hit in `README.md`; the spec may still name them as the rules it removes.

- [ ] **Step 4: Commit**

```bash
git add README.md docs/specs/2026-10-08-monitor-role-and-host-check.md
git commit -m "docs: the monitor may share the gateway's machine

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7 (controller, not a subagent): Outline and the pull request

- [ ] Amend the Decisions record *Uptime runs on a monitor site, never the gateway; hosts are checked before the toolkit touches them* with the 2026-10-12 decision, and add one line to the Decision Log linking to it. Re-read both after writing.
- [ ] Push `feat/monitor-on-gateway` and open a pull request against `fix/wizard-site-prompt` (it retargets to `develop` when PR #199 merges). Body: what changed, the interim refusal, and that the hand-off is the next pull request. No Outline references in the PR.
- [ ] Update the claim in Current State with the PR number.
