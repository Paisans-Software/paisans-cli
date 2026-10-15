# Rootless Phase 4: Requests and Admission Records Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `paisans host request --site <s>` writes the request a host's owner admits that site from, on stdout, with its digest on stderr; and a new package, `internal/admission`, holds the request (built from the configuration, encoded in one byte-exact form, parsed strictly, digested), the grants a site's roles need, and the admission record's shape, path and mode, with a reader the deployment's own user runs without sudo.

**Architecture:** `internal/admission` depends on `internal/config` and `internal/deployment` only, so any later package (`hostadmit`, `hostprep`, `doctor`, `apply`) can import it without a cycle; the record reader takes a one-method `Reader` interface that `apply.Transport` already satisfies. A request is written by a hand-rolled encoder (fixed field order, every string double quoted, a fixed header comment), and `Parse` refuses any file that is not exactly the bytes `Encode` writes for what it holds, so the digest of the file is the digest of the request the owner is shown and nothing in the file goes unread. `Build` and `Parse` share one `Check`, so `host request` never prints a request `host admit` would refuse. Phase 4 writes nothing on a host: phase 5 writes the record with `NewRecord`, `EncodeRecord`, `Path` and `Mode`, and phase 6 reads it with `Read`.

**Tech Stack:** Go 1.25, `gopkg.in/yaml.v3` (its node API, for the shape check), `golang.org/x/crypto/ssh` (public keys), `crypto/sha256`, `encoding/json`, the `cmd/paisans` test helpers `captureOutput` and `fixtureConfig`.

**Spec:** `docs/specs/2026-10-15-rootless-and-host-admit.md` (sections *`paisans host admit`* steps 6, 12, 13, 14 and 16, *`paisans host request`*, *Running on the host*, *Behind the owner's web server*, *A deployment with sites missing*, *Tests*), and the phase index `docs/superpowers/plans/2026-10-15-rootless-and-host-admit.md`, *Phase 4: Requests and admission records*, read beside its phases 5 and 6, which consume what this phase produces.

## Global Constraints

* "Grants are opt-in: `--watchdog`, `--public-ports`. Forwarding follows `endpoint`, with no flag." (index)
* "`grants` | what the roles need: `watchdog` for a data site, `public_ports` for a gateway or a Caddy monitor" (spec, request table). A data site needs the watchdog "whatever its `watchdog` mode".
* "`web_server` | for a site with `managed_by: external`: each app's hostname and its `listen` address" (spec, request table).
* "The digest is the owner's check that the request is the one the operator sent. [...] twelve hex digits in three groups" of SHA-256 over the encoded bytes (spec; index).
* "Admit validates a request as strictly as a configuration: every field present, the user name accepted by the same pattern as `ssh.user`, every key a public key the toolkit can parse, the address inside the subnet." (spec)
* "The request holds nothing secret, so it can travel by email or chat." (spec)
* The admission record: "`/etc/paisans/admitted.<token>.json`, mode 644: the user, the grants (`watchdog`, `public_ports`), whether forwarding is on, the ports the firewall opened, the request's digest when there was one, and the time", plus the web server detection, the `--data-dir` path and the `--bridge-netfilter` flag (spec, steps 6 and 14, *`--data-dir`*, *Behind the owner's web server*). "mode 0644 and owned by root so the deployment's user can read it without sudo" (index).
* `(config.Site).SSHUser()` takes no argument: phase 1 records the default on each site at `config.Load`.
* No migration code. Nothing is released from `develop` until phase 6; help and docs may name `host admit`, which phase 5 adds.
* No em dashes anywhere: code, comments, docs, commit messages.
* Docs describe current state only, and no rejected alternatives anywhere. `docs/decisions.md`, `docs/specs/` and `docs/superpowers/` are records and are not edited by these tasks.
* Commits: `<type>: <lowercase subject>`, a body saying why, and the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. A behaviour-changing commit adds one `CHANGELOG.md` bullet under `[Unreleased]`, one sentence, at most 35 words; a commit with nothing an operator would notice adds none and says so in its body.
* Never `git stash`; other sessions share the stash. Never push.

## Review Focus

The index's own line for this phase, "A request edited after its digest was read: admit must refuse at the typed digest (phase 4 test)", splits in two: phase 4 proves that any edit Parse accepts changes the digest, and that any edit that would not change what Parse returns is refused outright; phase 5 owns the typed comparison.

* **Someone who can change the request on its way adds their own key.** The forged file must parse (so only the digest can tell) and its digest must differ from the one the operator printed; and anything they might hide from the parsed view instead (a comment, an anchor, a second document, a duplicate key, CRLF, an unquoted value, the header removed) must be refused, so the owner is never shown less than admit acts on (Task 3: `TestAForgedKeyChangesTheDigest`, `TestParseRefuses`, `TestDigestChangesWithEveryField`).
* **A request that names someone else's user or reaches past the egress table.** `user: root`, `user: paisans-<another token>` (whose `authorized_keys` admit would otherwise write into), and a `mesh_subnet` over loopback, link-local, `0.0.0.0/8` or multicast (which admit's egress table accepts before it drops those ranges) must each be refused, from a file and from the configuration alike (Task 2: `TestCheckRefuses`, `TestBuildRefusesWhatParseWould`; Task 3: the loopback and other-deployment rows of `TestParseRefuses`).
* **A value that redraws the owner's terminal.** A request is read by a person on a host as root; a hostname, name or key carrying ESC or another control character must be refused, never printed into admit's plan (Task 2: the terminal-escape row of `TestCheckRefuses`).
* **`paisans host request --site home-a > home-a.admit`.** Stdout must be the request and nothing else, byte for byte what the digest on stderr was computed over, and two runs must print the same bytes (Task 5: `TestHostRequestWritesTheRequestAndItsDigest`, `TestHostRequestIsStable`).
* **An owner whose paisans is older or newer than the operator's.** A request or record of another version must be refused naming the version, never read in part (Task 3: the version rows of `TestParseRefuses`; Task 4: the version rows of `TestParseRecordRefuses`).

## Founder question, not blocking

The digest is 48 bits, as the spec fixes it. A forger who intercepts a request needs a second request with the same 48-bit digest and their own key in it. The request's free fields (an operator's name, up to 32 characters; the key itself) leave them room to vary it, and 2^48 SHA-256 evaluations is hours on one current GPU, so within a window like "the owner reads their email tomorrow" the digest does not hold against a determined forger, only against a casual edit. This plan implements the spec as written: the length is one constant, `admission.DigestLength`, and `DigestGroup` the group size, so a longer digest is a one-line change with its tests. Dropping key comments (Task 2) removes the cheapest field to vary, which is all this phase can do within the spec.

## Rulings

Where the index, the spec and the code disagreed or left a choice open, this plan decides as follows.

1. **`Read` takes `admission.Reader`, not `apply.Transport`.** The index's `Read(t Transport, token string)` would make `internal/admission` import `internal/apply`; phases 5 and 6 read the record from `hostprep`, `doctor` and `apply` itself, so that import would become a cycle. `Reader` is `ReadFile(path) (string, bool, error)`, which every `apply.Transport` already has, so callers pass their transport unchanged.
2. **`listen_port: 0` means no fixed port.** `config.Site.ListenPort()` is 0 for a site nothing dials, which WireGuard then picks; the spec wants every field present, so the field is written as 0 and the firewall opens no UDP port for it.
3. **`endpoint` is `site.Endpoint != ""`**, the same test as `isRelay` in `internal/render/site.go`, so forwarding follows exactly what makes a site a relay. A site dialled on its `public_address` alone (`vm` in the fixture) has a `listen_port` and no forwarding.
4. **`web_server` is always present**, `[]` for a site that is not a monitor with `managed_by: external`; for one that is, an entry per app pinned there (`validate` allows one), each with the site's `web_server.listen`.
5. **`grants` is a mapping of two booleans**, both always written, so "every field present" covers the grants too. `Missing` names a grant by its flag's name (`watchdog`, `public-ports`), which is what phase 5's refusal prints.
6. **Keys travel as type and base64 alone**, comments dropped, in the order `ssh.keys` names the operators and each operator lists their keys. A comment is free text a forger could vary (see the founder question), and the owner needs only the key.
7. **`Parse` requires the exact bytes `Encode` writes, header comment included.** The spec says the digest is over "the encoded bytes"; this makes the digest of the file the owner holds equal to the digest of the request they are shown, and refuses everything YAML could carry outside it. Email clients that rewrite line endings are refused with the instruction to resend as an attachment.
8. **Refusals beyond the spec's list**, each applied by `Check`, so by `host request` too: `user: root`; a user named `paisans-<token>` for any token other than the deployment's own; an `endpoint` with `listen_port: 0`; and a `mesh_subnet` overlapping `0.0.0.0/8`, `127.0.0.0/8`, `169.254.0.0/16` or `224.0.0.0/3`. The last follows from the spec's *Egress*: its table accepts the mesh subnet first, so a subnet over loopback or the metadata address would reopen what the table drops. A configuration whose mesh subnet overlaps them gets no request, with that reason; `config` itself is unchanged. This is a new refusal and is called out in the pull request for review.
9. **The record carries `version` and `deployment` beyond the spec's list**, so `Read` refuses a record of another layout and one that names another deployment. Its ports are `{ssh, wireguard, public}` rather than a flat list, so `host prepare` (phase 6) compares each with the declaration by purpose. The time is `admitted_at`, RFC 3339 in UTC.
10. **The record's web server detection is `admission.WebServer`, strings only**, not `webserver.WebServer`, whose `Certs` and `DNS` are Go enums and whose package imports `render`. `admission` stays importable from anywhere; phase 6 adds the mapping in `internal/webserver`.
11. **`host request` runs `validate.Check`** and refuses a configuration it refuses, as `host prepare` does, printing the findings on stderr only then: stdout is the request.
12. **The request has no `site` field**, as the spec's table has none: the owner learns the site from the file's name and the operator. If the founder wants the plan to name it, it is a seventh scalar and a golden regeneration.

## What this phase hands to phases 5 and 6

* **Phase 5 (`host admit`, `host evict`)** gets a request from `admission.Parse(file)` with `admission.Digest(file)`, or from `admission.Build(cfg, site)` with an empty digest, so both paths plan from the same `Request` (the spec's "Admit plans the same steps from `--request` as from `--site`"). It stops first on `req.Grants.Missing(granted)`, before any probe. It builds the record with `admission.NewRecord(req, granted, digest, time.Now())`, sets `DataDir`, `BridgeNetfilter` and `WebServer` itself, and writes `admission.EncodeRecord(rec)` to `admission.Path(req.Token())` with `admission.Mode` through its root transport (whose `WriteFile` writes a temporary file and renames it, so the file is root's), creating `admission.Dir` mode 755 first. Phase 5 also owns comparing the typed digest (case, spaces and hyphens normalised), showing plainly when `req.User` names an account that already exists and is not `paisans-<token>` (whose `authorized_keys` admit would then write), and refusing a `mesh_subnet` that overlaps a route or address on the host, none of which `Parse` can know.
* **Phase 6** reads the record as the deployment's user with `admission.Read(transport, cfg.Deployment().Token())`; `errors.Is(err, admission.ErrNotAdmitted)` is a site not admitted, and `admission.Needs(site).Missing(rec.Grants)` non-empty is a site whose grants do not cover its roles, both *pending* in *A deployment with sites missing*. `host prepare` compares `rec.User`, `rec.Ports.SSH`, `rec.Ports.WireGuard` and `rec.Forwarding` with the declaration. `web-server show` maps `rec.WebServer` into `webserver.WebServer`.

---

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `internal/admission/grants.go` | create | package doc; `Grants`, `Needs`, `(Grants).Missing` |
| `internal/config/names.go` | create | `ValidUserName`, `ValidOperatorName`, `ValidAppName`, `ValidHostname`: the loader's own rules, exported |
| `internal/admission/request.go` | create | `Request`, `WebApp`, `Operator`, `RequestVersion`, `Build`, `(Request).Check`, `(Request).Token`, `Error` |
| `internal/admission/encode.go` | create | `Encode`, `Parse`, `Digest`, `DigestLength`, `DigestGroup`, `Fields` |
| `internal/admission/record.go` | create | `Record`, `Ports`, `WebServer`, `RecordVersion`, `Dir`, `Mode`, `Path`, `ErrNotAdmitted`, `NewRecord`, `EncodeRecord`, `ParseRecord`, `Reader`, `Read` |
| `internal/admission/testdata/paisans-stack.yaml` | create | one site per role mix |
| `internal/admission/testdata/requests/*.admit`, `internal/admission/testdata/admitted.f2a9.json` | generated | golden request per site; golden record |
| `cmd/paisans/hostrequest.go` | create | `runHostRequest` |
| `cmd/paisans/main.go`, `cmd/paisans/help.go` | modify | dispatch `host request`; its help page and the host group's line |
| `docs/commands.md`, `README.md`, `CHANGELOG.md` | modify | the command, its reasoning, one bullet |

---

### Task 1: The grants a site's roles need

**Files:**
- Create: `internal/admission/grants.go`
- Test: `internal/admission/grants_test.go`

**Interfaces:**
- Consumes: `(config.Site).Has(config.Role) bool`, `(config.Site).RunsCaddy() bool` (true on a gateway and on a monitor with `managed_by: paisans`).
- Produces: `type Grants struct { Watchdog bool; PublicPorts bool }` with tags `yaml:"watchdog" json:"watchdog"` and `yaml:"public_ports" json:"public_ports"`; `func Needs(site config.Site) Grants`; `func (needed Grants) Missing(granted Grants) []string`, returning `"watchdog"` then `"public-ports"` for each needed and not granted, nil when none.

- [ ] **Step 1: Write the failing test**

Create `internal/admission/grants_test.go`:

```go
package admission_test

import (
	"slices"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// A data site needs the watchdog whatever its watchdog mode; a site that
// runs the toolkit's Caddy needs low ports; a monitor behind the owner's
// web server, and every other site, needs neither.
func TestNeeds(t *testing.T) {
	external := &config.WebServer{ManagedBy: config.ManagedByExternal, Listen: "127.0.0.1:8480"}
	for _, c := range []struct {
		name string
		site config.Site
		want admission.Grants
	}{
		{"data", config.Site{Roles: []config.Role{config.RoleData}}, admission.Grants{Watchdog: true}},
		{"data, watchdog off", config.Site{Roles: []config.Role{config.RoleData}, Watchdog: config.WatchdogOff}, admission.Grants{Watchdog: true}},
		{"gateway", config.Site{Roles: []config.Role{config.RoleGateway}}, admission.Grants{PublicPorts: true}},
		{"Caddy monitor", config.Site{Roles: []config.Role{config.RoleMonitor}}, admission.Grants{PublicPorts: true}},
		{"external monitor", config.Site{Roles: []config.Role{config.RoleMonitor}, WebServer: external}, admission.Grants{}},
		{"apps", config.Site{Roles: []config.Role{config.RoleApps}}, admission.Grants{}},
		{"witness", config.Site{Roles: []config.Role{config.RoleWitness}}, admission.Grants{}},
		{"storage", config.Site{Roles: []config.Role{config.RoleStorage}}, admission.Grants{}},
		{"data and gateway", config.Site{Roles: []config.Role{config.RoleData, config.RoleGateway}}, admission.Grants{Watchdog: true, PublicPorts: true}},
	} {
		if got := admission.Needs(c.site); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// Missing names each grant needed and not given, by its flag's name.
func TestMissing(t *testing.T) {
	both := admission.Grants{Watchdog: true, PublicPorts: true}
	for _, c := range []struct {
		needed, granted admission.Grants
		want            []string
	}{
		{both, admission.Grants{}, []string{"watchdog", "public-ports"}},
		{both, admission.Grants{Watchdog: true}, []string{"public-ports"}},
		{both, both, nil},
		{admission.Grants{}, both, nil},
	} {
		if got := c.needed.Missing(c.granted); !slices.Equal(got, c.want) {
			t.Errorf("%+v given %+v: %q, want %q", c.needed, c.granted, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/admission/`
Expected: FAIL, the package has no non-test Go files (`no non-test Go files` or `undefined: admission.Needs`).

- [ ] **Step 3: Write the grants**

Create `internal/admission/grants.go`:

```go
// Package admission is what passes between a deployment's operators and a
// host's owner: the request an operator sends (Build, Encode), the owner's
// strict reading of it (Parse) and the digest both compare (Digest), and the
// admission record host admit leaves on the host, which the deployment's own
// user reads without sudo (Read).
package admission

import "github.com/paisans-software/paisans-cli/internal/config"

// Grants are the owner's opt-in grants: Patroni's watchdog, which can
// reboot the machine, and ports below 1024, which every local user can then
// bind. In a request they are what the site's roles need; in a record they
// are what the owner gave.
type Grants struct {
	Watchdog    bool `yaml:"watchdog" json:"watchdog"`
	PublicPorts bool `yaml:"public_ports" json:"public_ports"`
}

// Needs is what a site's roles need granted: the watchdog for a data site,
// whatever its watchdog mode, since a Patroni member without one can keep
// acting as leader after losing its lock; low ports for a site that runs the
// toolkit's Caddy, a gateway or a monitor with managed_by: paisans, which
// binds 80 and 443.
func Needs(site config.Site) Grants {
	return Grants{Watchdog: site.Has(config.RoleData), PublicPorts: site.RunsCaddy()}
}

// Missing names each grant needed that granted lacks, in a fixed order,
// by the name the flag that gives it carries: watchdog, public-ports.
func (needed Grants) Missing(granted Grants) []string {
	var out []string
	if needed.Watchdog && !granted.Watchdog {
		out = append(out, "watchdog")
	}
	if needed.PublicPorts && !granted.PublicPorts {
		out = append(out, "public-ports")
	}
	return out
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/admission/ && go vet ./internal/admission/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/admission/grants.go internal/admission/grants_test.go
git commit -m "feat: name the grants a site's roles need from its host's owner

The watchdog and ports below 1024 are the owner's to grant, and a
request, admit's early stop and the classification of a pending site all
ask the same question of a site's roles: a data site needs the watchdog
whatever its watchdog mode, and a site running the toolkit's Caddy needs
the low ports. Needs answers it once, and Missing names what a record
lacks by the flag that grants it.

Nothing an operator would notice yet, so no changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: A site's request, built from the configuration and checked

**Files:**
- Create: `internal/config/names.go`, `internal/config/names_test.go`
- Create: `internal/admission/request.go`
- Create: `internal/admission/testdata/paisans-stack.yaml`
- Test: `internal/admission/helpers_test.go`, `internal/admission/request_test.go`, `internal/admission/check_test.go`

**Interfaces:**
- Consumes: Task 1's `Grants`, `Needs`. From `internal/config`: `(Site).SSHUser() string`, `(SSH).PortOrDefault() int`, `(Site).ListenPort() int`, `(Site).WebServerManagedBy() ManagedBy`, `(*Config).PinnedTo(site string) []string`, `(WebServer).ListenHostPort() (string, int, bool)`, `Site.SSH.Keys` (a `KeyList` of `NamedKey{Name, Line}` in `ssh.keys` order). From `internal/deployment`: `ValidID(string) bool`, `Deployment{ID}.Prefix()`, `.Token()`.
- Produces: in `internal/config`: `func ValidUserName(s string) bool`, `func ValidOperatorName(s string) bool`, `func ValidAppName(s string) bool`, `func ValidHostname(s string) bool`. In `internal/admission`: `const RequestVersion = 1`; `type Request struct { Version int; Deployment, User string; SSHPort, ListenPort int; MeshSubnet, Address string; Endpoint bool; Grants Grants; WebServer []WebApp; Operators []Operator }` (YAML tags `version`, `deployment`, `user`, `ssh_port`, `listen_port`, `mesh_subnet`, `address`, `endpoint`, `grants`, `web_server`, `operators`); `type WebApp struct { App, Hostname, Listen string }`; `type Operator struct { Name string; Keys []string }`; `func (r Request) Token() string`; `func Build(cfg *config.Config, site string) (Request, error)`; `func (r Request) Check() []string`; `type Error struct { What string; Problems []string }` implementing `error`. Test helpers `update`, `fixture(t)`, `build(t, site)` in `helpers_test.go`, used by Tasks 3 and 4.

- [ ] **Step 1: Write the failing test for the exported name rules**

Create `internal/config/names_test.go`:

```go
package config_test

import (
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// Each exported check is the rule the loader holds its field to, so a
// request read without a configuration is held to the same.
func TestNameChecks(t *testing.T) {
	for _, c := range []struct {
		check func(string) bool
		name  string
		good  []string
		bad   []string
	}{
		{config.ValidUserName, "user", []string{"paisans-f2a9", "ubuntu", "_svc"}, []string{"", "Root", "1abc", "a b", "a;b", "abcdefghijklmnopqrstuvwxyzabcdefg"}},
		{config.ValidOperatorName, "operator", []string{"alice", "0day", "a.b_c-d"}, []string{"", "Alice", "-a", "a b", "a/b"}},
		{config.ValidAppName, "app", []string{"talk", "status-2"}, []string{"", "Talk", "a.b", "a_b", "-a"}},
		{config.ValidHostname, "hostname", []string{"status.example.org"}, []string{"", "a b.example.org", "a;b", "-a.example.org"}},
	} {
		for _, s := range c.good {
			if !c.check(s) {
				t.Errorf("%s %q refused", c.name, s)
			}
		}
		for _, s := range c.bad {
			if c.check(s) {
				t.Errorf("%s %q accepted", c.name, s)
			}
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/config/ -run TestNameChecks`
Expected: FAIL to compile, `undefined: config.ValidUserName` (and the three others).

- [ ] **Step 3: Export the rules**

Create `internal/config/names.go`. Each wraps the pattern the loader already holds its field to (`unixUser` and `keyName` in `ssh.go`, `blueprintName` in `catalog.go`, `nameChars` in `config.go`), so a request read without a configuration is held to the same rule and the two cannot drift:

```go
package config

// ValidUserName reports whether s is a login name this toolkit accepts, the
// rule sites.<s>.ssh.user is held to: a lowercase letter or underscore, then
// lowercase letters, digits, underscores or hyphens, at most 32 characters.
func ValidUserName(s string) bool { return unixUser.MatchString(s) }

// ValidOperatorName reports whether s is a name an operator may have under
// operators, the name recorded on a host beside each of their keys.
func ValidOperatorName(s string) bool { return keyName.MatchString(s) }

// ValidAppName reports whether s is a name an app may have under apps.
func ValidAppName(s string) bool { return blueprintName.MatchString(s) }

// ValidHostname reports whether s holds only what a DNS name holds, the rule
// an app's hostname is held to.
func ValidHostname(s string) bool { return nameChars(s) }
```

Run: `go test ./internal/config/ -run TestNameChecks`
Expected: PASS.

- [ ] **Step 4: Write the fixture and the failing tests**

Create `internal/admission/testdata/paisans-stack.yaml`, one site per role mix. It loads with `config.Load` and `validate.Check` refuses nothing in it (warnings only):

```yaml
# One site per role mix a request is built for. Loaded with config.Load;
# the keys are placeholders that parse, not anyone's.
version: 1
id: f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01
community:
  name: "Fixture"
  domain: example.org
mesh:
  subnet: 10.44.0.0/24
acme:
  provider: desec
operators:
  alice:
    name: Alice Example
    email: alice@example.org
    keys: |
      ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice@laptop
  bob:
    name: Bob Example
    email: bob@example.org
    keys: |
      ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB bob@desk
      ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC bob@phone
sites:
  # data, apps and the gateway, with an endpoint: both grants, forwarding.
  home-a:
    roles: [data, apps, gateway]
    address: 10.44.0.1
    public_address: 203.0.113.10
    endpoint: ":51820"
    ssh:
      keys: [alice, bob]
  # data alone behind NAT, with its own user and SSH port: the watchdog only.
  home-b:
    roles: [data]
    address: 10.44.0.2
    ssh:
      host: home-b.local
      user: community
      port: 2222
      keys: [bob]
  # the witness, dialled on its public address with no endpoint: a
  # WireGuard port, no forwarding, no grant.
  vm:
    roles: [witness]
    address: 10.44.0.3
    public_address: 203.0.113.30
    ssh:
      keys: [alice]
  # a monitor behind the owner's web server: no low port, its app handed over.
  status:
    roles: [monitor]
    address: 10.44.0.4
    public_address: 203.0.113.40
    web_server:
      managed_by: external
      listen: 127.0.0.1:8480
    ssh:
      keys: [alice]
  # a monitor running the toolkit's Caddy, with an endpoint naming a host.
  watch:
    roles: [monitor]
    address: 10.44.0.5
    public_address: 203.0.113.50
    endpoint: watch.example.org:51821
    ssh:
      keys: [alice]
  # storage alone, never dialled: nothing to grant, no port.
  store:
    roles: [storage]
    address: 10.44.0.6
    ssh:
      host: store.local
      keys: [alice]
etcd:
  members: [home-a, home-b, vm]
storage:
  garage:
    sites: [store]
apps:
  talk:
    blueprint: mbin
    hostname: talk.example.org
    placement: cluster
  status:
    blueprint: uptime
    hostname: status.example.org
    placement: { pinned: status }
  watch:
    blueprint: uptime
    hostname: watch.example.org
    placement: { pinned: watch }
```

Create `internal/admission/helpers_test.go`:

```go
package admission_test

import (
	"flag"
	"path/filepath"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
)

var update = flag.Bool("update", false, "rewrite the golden requests")

func fixture(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join("testdata", "paisans-stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func build(t *testing.T, site string) admission.Request {
	t.Helper()
	r, err := admission.Build(fixture(t), site)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
```

Create `internal/admission/request_test.go`:

```go
package admission_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
)

// What each role mix needs, and the rest of what Build reads from the site.
func TestBuildReadsTheSite(t *testing.T) {
	for _, c := range []struct {
		site       string
		grants     admission.Grants
		user       string
		sshPort    int
		listenPort int
		endpoint   bool
		webServer  int
		operators  []string
	}{
		{"home-a", admission.Grants{Watchdog: true, PublicPorts: true}, "paisans-f2a9", 22, 51820, true, 0, []string{"alice", "bob"}},
		{"home-b", admission.Grants{Watchdog: true}, "community", 2222, 0, false, 0, []string{"bob"}},
		{"vm", admission.Grants{}, "paisans-f2a9", 22, 51820, false, 0, []string{"alice"}},
		{"status", admission.Grants{}, "paisans-f2a9", 22, 51820, false, 1, []string{"alice"}},
		{"watch", admission.Grants{PublicPorts: true}, "paisans-f2a9", 22, 51821, true, 0, []string{"alice"}},
		{"store", admission.Grants{}, "paisans-f2a9", 22, 0, false, 0, []string{"alice"}},
	} {
		r := build(t, c.site)
		var names []string
		for _, o := range r.Operators {
			names = append(names, o.Name)
		}
		if r.Grants != c.grants || r.User != c.user || r.SSHPort != c.sshPort || r.ListenPort != c.listenPort || r.Endpoint != c.endpoint || len(r.WebServer) != c.webServer || strings.Join(names, ",") != strings.Join(c.operators, ",") {
			t.Errorf("%s: got %+v", c.site, r)
		}
	}
	status := build(t, "status")
	if w := status.WebServer[0]; w != (admission.WebApp{App: "status", Hostname: "status.example.org", Listen: "127.0.0.1:8480"}) {
		t.Errorf("status: web_server %+v", w)
	}
	if bob := build(t, "home-b").Operators[0]; len(bob.Keys) != 2 || strings.Contains(strings.Join(bob.Keys, " "), "bob@") {
		t.Errorf("bob's keys are not both there, without their comments: %q", bob.Keys)
	}
}

// A site the configuration does not declare is refused, naming the ones it
// does.
func TestBuildRefusesAnUndeclaredSite(t *testing.T) {
	_, err := admission.Build(fixture(t), "home-z")
	if err == nil || !strings.Contains(err.Error(), `declares no site "home-z"`) || !strings.Contains(err.Error(), "home-a, home-b") {
		t.Errorf("got %v", err)
	}
}

// Build checks what it builds, so host request never prints a request admit
// would refuse: a mesh over loopback is refused here as in Parse.
func TestBuildRefusesWhatParseWould(t *testing.T) {
	cfg := fixture(t)
	cfg.Mesh.Subnet = "127.0.0.0/8"
	home := cfg.Sites["home-a"]
	home.Address = "127.0.0.1"
	cfg.Sites["home-a"] = home
	_, err := admission.Build(cfg, "home-a")
	var refused *admission.Error
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "overlaps 127.0.0.0/8") {
		t.Errorf("got %v", err)
	}
}
```

Create `internal/admission/check_test.go`:

```go
package admission_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
)

// Check names the field of each problem. Every case starts from the home-a
// request, which it accepts, and changes one thing.
func TestCheckRefuses(t *testing.T) {
	if problems := build(t, "home-a").Check(); len(problems) > 0 {
		t.Fatalf("the home-a request has problems: %q", problems)
	}
	alice := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for _, c := range []struct {
		name   string
		mutate func(*admission.Request)
		want   string
	}{
		{"another version", func(r *admission.Request) { r.Version = 2 }, "version: 2, and this paisans reads version 1"},
		{"a bad deployment id", func(r *admission.Request) { r.Deployment = "F2A9" }, `deployment: "F2A9" is not a deployment id`},
		{"a user paisans refuses", func(r *admission.Request) { r.User = "a;b" }, `user: "a;b" is not a user name`},
		{"root", func(r *admission.Request) { r.User = "root" }, "user: root is never a deployment's user"},
		{"another deployment's user", func(r *admission.Request) { r.User = "paisans-0a0b" }, "paisans-0a0b is another deployment's name; this deployment's own user is paisans-f2a9"},
		{"an ssh port of 0", func(r *admission.Request) { r.SSHPort = 0 }, "ssh_port: 0 is not a port"},
		{"an ssh port out of range", func(r *admission.Request) { r.SSHPort = 70000 }, "ssh_port: 70000 is not a port"},
		{"a listen port out of range", func(r *admission.Request) { r.ListenPort = 70000 }, "listen_port: 70000 is not a port"},
		{"an endpoint with no port", func(r *admission.Request) { r.ListenPort = 0 }, "the site has an endpoint"},
		{"a subnet that is not one", func(r *admission.Request) { r.MeshSubnet = "10.44.0.0" }, `mesh_subnet: "10.44.0.0" is not an IPv4 network`},
		{"an IPv6 subnet", func(r *admission.Request) { r.MeshSubnet = "fd00::/64" }, `mesh_subnet: "fd00::/64" is not an IPv4 network`},
		{"a subnet written as a host", func(r *admission.Request) { r.MeshSubnet = "10.44.0.1/24" }, "is not written as its network, 10.44.0.0/24"},
		{"a subnet over loopback", func(r *admission.Request) { r.MeshSubnet, r.Address = "127.0.0.0/24", "127.0.0.1" }, "overlaps 127.0.0.0/8"},
		{"a subnet over everything", func(r *admission.Request) { r.MeshSubnet = "0.0.0.0/0" }, "overlaps 0.0.0.0/8"},
		{"a subnet over the metadata address", func(r *admission.Request) { r.MeshSubnet, r.Address = "169.254.0.0/24", "169.254.0.1" }, "overlaps 169.254.0.0/16"},
		{"a subnet over multicast", func(r *admission.Request) { r.MeshSubnet, r.Address = "239.1.0.0/24", "239.1.0.1" }, "overlaps 224.0.0.0/3"},
		{"an address outside the subnet", func(r *admission.Request) { r.Address = "10.45.0.1" }, `address: "10.45.0.1" is not an IPv4 address inside mesh_subnet 10.44.0.0/24`},
		{"an address that is not one", func(r *admission.Request) { r.Address = "home-a" }, `address: "home-a" is not an IPv4 address`},
		{"a web server app with a bad name", func(r *admission.Request) {
			r.WebServer = []admission.WebApp{{App: "Status", Hostname: "status.example.org", Listen: "127.0.0.1:8480"}}
		}, `web_server[0].app: "Status" is not an app name`},
		{"a web server app twice", func(r *admission.Request) {
			w := admission.WebApp{App: "status", Hostname: "status.example.org", Listen: "127.0.0.1:8480"}
			r.WebServer = []admission.WebApp{w, w}
		}, "web_server[1].app: status is listed twice"},
		{"a terminal escape in a hostname", func(r *admission.Request) {
			r.WebServer = []admission.WebApp{{App: "status", Hostname: "status\x1b[2J.example.org", Listen: "127.0.0.1:8480"}}
		}, "web_server[0].hostname"},
		{"a listen with no port", func(r *admission.Request) {
			r.WebServer = []admission.WebApp{{App: "status", Hostname: "status.example.org", Listen: "127.0.0.1"}}
		}, `web_server[0].listen: "127.0.0.1" is not an IPv4 address and a port`},
		{"no operator", func(r *admission.Request) { r.Operators = nil }, "operators: none, so nobody could log in as paisans-f2a9"},
		{"an operator with a bad name", func(r *admission.Request) { r.Operators[0].Name = "Alice" }, `operators[0].name: "Alice" is not an operator name`},
		{"an operator twice", func(r *admission.Request) { r.Operators[1].Name = "alice" }, "operators[1].name: alice is listed twice"},
		{"an operator with no key", func(r *admission.Request) { r.Operators[0].Keys = nil }, "operators[0].keys: none for alice"},
		{"a bad key", func(r *admission.Request) { r.Operators[0].Keys = []string{"ssh-ed25519 AAAAnotakey"} }, "operators[0].keys[0]: not an OpenSSH public key"},
		{"a key with options", func(r *admission.Request) { r.Operators[0].Keys = []string{`command="/bin/sh" ` + alice} }, "operators[0].keys[0]: carries options"},
		{"a key with a comment", func(r *admission.Request) { r.Operators[0].Keys = []string{alice + " mallory@example.org"} }, "not written as type and base64 alone, " + alice},
		{"two keys on a line", func(r *admission.Request) { r.Operators[0].Keys = []string{alice + "\n" + alice} }, "more than one key on the line"},
		{"a key under two operators", func(r *admission.Request) { r.Operators[1].Keys = []string{alice} }, "operators[1].keys[0]: the same key as alice's"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := build(t, "home-a")
			c.mutate(&r)
			problems := strings.Join(r.Check(), "\n")
			if !strings.Contains(problems, c.want) {
				t.Errorf("got %q\nwant it to say %q", problems, c.want)
			}
		})
	}
}
```

- [ ] **Step 5: Run them to verify they fail**

Run: `go test ./internal/admission/`
Expected: FAIL to compile, `undefined: admission.Request`, `undefined: admission.Build`, `undefined: admission.Error`.

- [ ] **Step 6: Write the request, `Build` and `Check`**

Create `internal/admission/request.go`:

```go
package admission

import (
	"fmt"
	"net"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// RequestVersion is the request format this toolkit writes and reads. A
// request of any other version is refused, so an older CLI never admits a
// newer request on a partial reading of it.
const RequestVersion = 1

// Request is what host admit needs to admit one site of a deployment,
// without its configuration. It holds nothing secret.
type Request struct {
	Version    int    `yaml:"version"`
	Deployment string `yaml:"deployment"`
	User       string `yaml:"user"`
	SSHPort    int    `yaml:"ssh_port"`
	// ListenPort is the site's WireGuard port, 0 when no site dials it and
	// WireGuard picks one, so the firewall opens none.
	ListenPort int    `yaml:"listen_port"`
	MeshSubnet string `yaml:"mesh_subnet"`
	Address    string `yaml:"address"`
	// Endpoint is whether the site has one, so admit plans forwarding: such
	// a site relays mesh traffic between sites that have none.
	Endpoint bool   `yaml:"endpoint"`
	Grants   Grants `yaml:"grants"`
	// WebServer is each app the owner's web server is handed, for a site
	// with web_server.managed_by: external; empty for every other site.
	WebServer []WebApp   `yaml:"web_server"`
	Operators []Operator `yaml:"operators"`
}

// WebApp is one app behind the owner's web server: its public hostname and
// the address the web server proxies to.
type WebApp struct {
	App      string `yaml:"app"`
	Hostname string `yaml:"hostname"`
	Listen   string `yaml:"listen"`
}

// Operator is one operator the site's ssh.keys names, by their name under
// operators, and their public keys, each as type and base64 with no comment.
type Operator struct {
	Name string   `yaml:"name"`
	Keys []string `yaml:"keys"`
}

// Token is the deployment's token, which names everything admit creates.
func (r Request) Token() string { return deployment.Deployment{ID: r.Deployment}.Token() }

// Build is the request for one site of cfg. It refuses a site cfg does not
// declare, and a request Check refuses, so host request never prints one
// that host admit would refuse.
func Build(cfg *config.Config, site string) (Request, error) {
	s, ok := cfg.Sites[site]
	if !ok {
		return Request{}, fmt.Errorf("%s declares no site %q. Declared sites are %s", cfg.Path, site, strings.Join(cfg.SiteNames(), ", "))
	}
	r := Request{
		Version:    RequestVersion,
		Deployment: cfg.ID,
		User:       s.SSHUser(),
		SSHPort:    s.SSH.PortOrDefault(),
		ListenPort: s.ListenPort(),
		MeshSubnet: cfg.Mesh.Subnet,
		Address:    s.Address,
		Endpoint:   s.Endpoint != "",
		Grants:     Needs(s),
		WebServer:  []WebApp{},
		Operators:  []Operator{},
	}
	if s.Has(config.RoleMonitor) && s.WebServerManagedBy() == config.ManagedByExternal && s.WebServer != nil {
		for _, app := range cfg.PinnedTo(site) {
			r.WebServer = append(r.WebServer, WebApp{App: app, Hostname: cfg.Apps[app].Hostname, Listen: s.WebServer.Listen})
		}
	}
	for _, nk := range s.SSH.Keys {
		key, err := canonicalKey(nk.Line)
		if err != nil {
			return Request{}, fmt.Errorf("operators.%s.keys: %w", nk.Name, err)
		}
		if n := len(r.Operators); n == 0 || r.Operators[n-1].Name != nk.Name {
			r.Operators = append(r.Operators, Operator{Name: nk.Name})
		}
		last := &r.Operators[len(r.Operators)-1]
		last.Keys = append(last.Keys, key)
	}
	if problems := r.Check(); len(problems) > 0 {
		return Request{}, &Error{What: "the request for " + site, Problems: problems}
	}
	return r, nil
}

// canonicalKey is a public key line as type and base64 alone: its comment
// dropped, since a request carries nothing the owner does not need and free
// text is what a forger varies to search for a matching digest.
func canonicalKey(line string) (string, error) {
	pub, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	switch {
	case err != nil:
		return "", fmt.Errorf("not an OpenSSH public key (%v)", err)
	case len(options) > 0:
		return "", fmt.Errorf("carries options (%s)", strings.Join(options, ","))
	case len(strings.TrimSpace(string(rest))) > 0:
		return "", fmt.Errorf("more than one key on the line")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))), nil
}

// forbiddenMesh is what a mesh subnet may not overlap. Admit's egress table
// accepts the mesh subnet before it drops loopback and link-local, so a
// subnet over either would let the deployment reach the owner's local
// services and the cloud metadata address; this network, multicast and the
// reserved range route nothing a mesh could use.
var forbiddenMesh = []string{"0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/3"}

// Check is every problem with r, each naming its field. Parse refuses a
// request with any, and Build never returns one.
func (r Request) Check() []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if r.Version != RequestVersion {
		add("version: %d, and this paisans reads version %d requests", r.Version, RequestVersion)
	}
	if !deployment.ValidID(r.Deployment) {
		add("deployment: %q is not a deployment id", r.Deployment)
	}
	own := deployment.Deployment{ID: r.Deployment}.Prefix()
	switch {
	case !config.ValidUserName(r.User):
		add("user: %q is not a user name paisans accepts", r.User)
	case r.User == "root":
		add("user: root is never a deployment's user")
	case strings.HasPrefix(r.User, "paisans-") && deployment.ValidID(r.Deployment) && r.User != own:
		add("user: %s is another deployment's name; this deployment's own user is %s", r.User, own)
	}
	if r.SSHPort < 1 || r.SSHPort > 65535 {
		add("ssh_port: %d is not a port", r.SSHPort)
	}
	if r.ListenPort < 0 || r.ListenPort > 65535 {
		add("listen_port: %d is not a port, or 0 for none", r.ListenPort)
	}
	if r.Endpoint && r.ListenPort == 0 {
		add("listen_port: 0, but the site has an endpoint, which other sites dial on a fixed port")
	}
	ip, mesh, err := net.ParseCIDR(r.MeshSubnet)
	switch {
	case err != nil || ip.To4() == nil:
		add("mesh_subnet: %q is not an IPv4 network", r.MeshSubnet)
	case !ip.Equal(mesh.IP):
		add("mesh_subnet: %q is not written as its network, %s", r.MeshSubnet, mesh)
	default:
		for _, f := range forbiddenMesh {
			_, n, _ := net.ParseCIDR(f)
			if n.Contains(mesh.IP) || mesh.Contains(n.IP) {
				add("mesh_subnet: %s overlaps %s, which the deployment must never reach", r.MeshSubnet, f)
			}
		}
		if a := net.ParseIP(r.Address); a == nil || a.To4() == nil || !mesh.Contains(a) {
			add("address: %q is not an IPv4 address inside mesh_subnet %s", r.Address, r.MeshSubnet)
		}
	}
	apps := map[string]bool{}
	for i, w := range r.WebServer {
		switch {
		case !config.ValidAppName(w.App):
			add("web_server[%d].app: %q is not an app name", i, w.App)
		case apps[w.App]:
			add("web_server[%d].app: %s is listed twice", i, w.App)
		}
		apps[w.App] = true
		if !config.ValidHostname(w.Hostname) {
			add("web_server[%d].hostname: %q is not a hostname", i, w.Hostname)
		}
		if _, _, ok := (config.WebServer{Listen: w.Listen}).ListenHostPort(); !ok {
			add("web_server[%d].listen: %q is not an IPv4 address and a port", i, w.Listen)
		}
	}
	if len(r.Operators) == 0 {
		add("operators: none, so nobody could log in as %s", r.User)
	}
	names, keys := map[string]bool{}, map[string]string{}
	for i, o := range r.Operators {
		switch {
		case !config.ValidOperatorName(o.Name):
			add("operators[%d].name: %q is not an operator name", i, o.Name)
		case names[o.Name]:
			add("operators[%d].name: %s is listed twice", i, o.Name)
		}
		names[o.Name] = true
		if len(o.Keys) == 0 {
			add("operators[%d].keys: none for %s", i, o.Name)
		}
		for j, k := range o.Keys {
			canonical, err := canonicalKey(k)
			switch {
			case err != nil:
				add("operators[%d].keys[%d]: %v", i, j, err)
				continue
			case canonical != k:
				add("operators[%d].keys[%d]: not written as type and base64 alone, %s", i, j, canonical)
				continue
			}
			if first, dup := keys[k]; dup {
				add("operators[%d].keys[%d]: the same key as %s's", i, j, first)
			}
			keys[k] = o.Name
		}
	}
	return problems
}

// Error is a request, or a record, refused, with every problem found.
type Error struct {
	What     string
	Problems []string
}

func (e *Error) Error() string {
	return e.What + " is refused:\n- " + strings.Join(e.Problems, "\n- ")
}
```

- [ ] **Step 7: Run them to verify they pass**

Run: `go test ./internal/admission/ ./internal/config/ && go vet ./internal/admission/ ./internal/config/`
Expected: PASS. If a `TestCheckRefuses` row fails, read its message before touching the code: each `want` is a substring of the problem `Check` writes, and the rows are the contract.

- [ ] **Step 8: Commit**

```bash
git add internal/config/names.go internal/config/names_test.go internal/admission/request.go internal/admission/helpers_test.go internal/admission/request_test.go internal/admission/check_test.go internal/admission/testdata/paisans-stack.yaml
git commit -m "feat: build a site's admission request from the configuration

An owner without the deployment's configuration admits a site from a
request, so the request carries what admit needs and nothing secret: the
user, ports, mesh address and subnet, whether forwarding is planned, the
grants the roles need, the apps handed to the owner's web server, and
each operator's keys, without their comments.

Check holds every field to the rule the loader holds it to, through the
patterns config now exports, and refuses root, another deployment's
paisans- user and a mesh subnet over loopback or link-local, which admit's
egress table would otherwise let through. Build runs it too, so host
request never writes a request admit refuses.

Nothing an operator would notice yet, so no changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: One byte-exact form, a strict parse and the digest

**Files:**
- Create: `internal/admission/encode.go`
- Test: `internal/admission/parse_test.go`
- Generated: `internal/admission/testdata/requests/{home-a,home-b,vm,status,watch,store}.admit`

**Interfaces:**
- Consumes: Task 2's `Request`, `WebApp`, `Operator`, `RequestVersion`, `(Request).Check`, `Error`; test helpers `update`, `build`.
- Produces: `func Encode(r Request) []byte`; `func Parse(data []byte) (Request, error)` (errors are `*Error` with `What: "the request"`); `func Digest(data []byte) string` (`xxxx-xxxx-xxxx`, lowercase hex); `const DigestLength = 12`, `const DigestGroup = 4`; `func Fields() []string` (every field path: the eleven top-level keys, then `grants.watchdog`, `grants.public_ports`, `web_server.app`, `web_server.hostname`, `web_server.listen`, `operators.name`, `operators.keys`).

- [ ] **Step 1: Write the failing tests**

Create `internal/admission/parse_test.go`:

```go
package admission_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
)

// One golden request per role mix in the fixture: each is the file host
// request prints, byte for byte, and each reads back as itself.
func TestGoldenRequests(t *testing.T) {
	for _, site := range []string{"home-a", "home-b", "vm", "status", "watch", "store"} {
		t.Run(site, func(t *testing.T) {
			got := admission.Encode(build(t, site))
			path := filepath.Join("testdata", "requests", site+".admit")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Errorf("got\n%s\nwant\n%s", got, want)
			}
			back, err := admission.Parse(got)
			if err != nil {
				t.Fatalf("its own request is refused: %v", err)
			}
			if again := admission.Encode(back); string(again) != string(got) {
				t.Errorf("read back as\n%s", again)
			}
		})
	}
}

// golden is the home-a request as host request writes it.
func golden(t *testing.T) string {
	t.Helper()
	return string(admission.Encode(build(t, "home-a")))
}

// The digest is twelve hex digits in three groups, the same on every run,
// and it is SHA-256's: the digest of no bytes is SHA-256's own, cut short.
func TestDigestIsStable(t *testing.T) {
	got := admission.Digest([]byte(golden(t)))
	if !regexp.MustCompile(`^[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}$`).MatchString(got) {
		t.Fatalf("digest %q is not three groups of four hex digits", got)
	}
	for range 3 {
		if again := admission.Digest(admission.Encode(build(t, "home-a"))); again != got {
			t.Fatalf("digest changed between runs: %s, then %s", got, again)
		}
	}
	if want := admission.Digest([]byte("")); want != "e3b0-c442-98fc" {
		t.Errorf("digest of nothing = %s, want SHA-256's e3b0-c442-98fc", want)
	}
}

// mutations changes one field of a request each, keyed by its path. The
// test below requires one for every field Parse knows, so a field added
// later cannot be left out of the digest.
var mutations = map[string]func(*admission.Request){
	"version":             func(r *admission.Request) { r.Version = 2 },
	"deployment":          func(r *admission.Request) { r.Deployment = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e02" },
	"user":                func(r *admission.Request) { r.User = "community" },
	"ssh_port":            func(r *admission.Request) { r.SSHPort = 2222 },
	"listen_port":         func(r *admission.Request) { r.ListenPort = 51821 },
	"mesh_subnet":         func(r *admission.Request) { r.MeshSubnet = "10.44.0.0/16" },
	"address":             func(r *admission.Request) { r.Address = "10.44.0.9" },
	"endpoint":            func(r *admission.Request) { r.Endpoint = false },
	"grants":              func(r *admission.Request) { r.Grants = admission.Grants{} },
	"grants.watchdog":     func(r *admission.Request) { r.Grants.Watchdog = false },
	"grants.public_ports": func(r *admission.Request) { r.Grants.PublicPorts = false },
	"web_server": func(r *admission.Request) {
		r.WebServer = []admission.WebApp{{App: "x", Hostname: "x.example.org", Listen: "127.0.0.1:1"}}
	},
	"web_server.app": func(r *admission.Request) {
		r.WebServer = []admission.WebApp{{App: "y", Hostname: "x.example.org", Listen: "127.0.0.1:1"}}
	},
	"web_server.hostname": func(r *admission.Request) {
		r.WebServer = []admission.WebApp{{App: "x", Hostname: "y.example.org", Listen: "127.0.0.1:1"}}
	},
	"web_server.listen": func(r *admission.Request) {
		r.WebServer = []admission.WebApp{{App: "x", Hostname: "x.example.org", Listen: "127.0.0.1:2"}}
	},
	"operators":      func(r *admission.Request) { r.Operators = r.Operators[:1] },
	"operators.name": func(r *admission.Request) { r.Operators[0].Name = "carol" },
	"operators.keys": func(r *admission.Request) {
		r.Operators[0].Keys = []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"}
	},
}

// Changing any one field changes the digest.
func TestDigestChangesWithEveryField(t *testing.T) {
	base := admission.Digest([]byte(golden(t)))
	paths := admission.Fields()
	for _, path := range paths {
		mutate, ok := mutations[path]
		if !ok {
			t.Errorf("%s: no mutation; add one", path)
			continue
		}
		r := build(t, "home-a")
		mutate(&r)
		if admission.Digest(admission.Encode(r)) == base {
			t.Errorf("%s: changing it left the digest %s", path, base)
		}
	}
	for path := range mutations {
		if !slices.Contains(paths, path) {
			t.Errorf("%s: a mutation for a field Parse does not know", path)
		}
	}
}

// Each refusal names what is wrong. Every case starts from a request Parse
// accepts and changes one thing. What Check refuses is tested beside it;
// the cases here that Check also finds prove Parse runs it.
func TestParseRefuses(t *testing.T) {
	g := golden(t)
	alice := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for _, c := range []struct {
		name, data, want string
	}{
		{"not YAML", "version: [1\n", "it is not YAML"},
		{"a list, not a mapping", "- 1\n", "is one YAML mapping"},
		{"empty", "", "is one YAML mapping"},
		{"an unknown version", strings.Replace(g, "version: 1", "version: 2", 1), "version: 2, and this paisans reads version 1"},
		{"version 0", strings.Replace(g, "version: 1", "version: 0", 1), "version: 0, and this paisans reads version 1"},
		{"no version", strings.Replace(g, "version: 1\n", "", 1), "version: missing"},
		{"a missing field", strings.Replace(g, "ssh_port: 22\n", "", 1), "ssh_port: missing"},
		{"a missing grant", strings.Replace(g, "  public_ports: true\n", "", 1), "grants.public_ports: missing"},
		{"a missing operator field", strings.Replace(g, "  - name: \"alice\"\n    keys:", "  - keys:", 1), "operators.name: missing"},
		{"an unknown field", strings.Replace(g, "endpoint: true\n", "endpoint: true\nsudo: true\n", 1), "sudo: not a field of a request"},
		{"an unknown grant", strings.Replace(g, "  watchdog: true\n", "  watchdog: true\n  docker: true\n", 1), "grants.docker: not a field of a request"},
		{"an unknown operator field", strings.Replace(g, "  - name: \"alice\"\n", "  - name: \"alice\"\n    shell: \"/bin/sh\"\n", 1), "operators.shell: not a field of a request"},
		{"a port as text", strings.Replace(g, "ssh_port: 22", "ssh_port: \"22\"", 1), "the wrong kind of value"},
		{"a bad key", strings.Replace(g, alice, "ssh-ed25519 AAAAnotakey", 1), "operators[0].keys[0]: not an OpenSSH public key"},
		{"no operator", g[:strings.Index(g, "operators:")] + "operators: []\n", "operators: none"},
		{"an address outside the subnet", strings.Replace(g, `address: "10.44.0.1"`, `address: "10.45.0.1"`, 1), "address: \"10.45.0.1\" is not an IPv4 address inside mesh_subnet 10.44.0.0/24"},
		{"a subnet over loopback", strings.NewReplacer(`"10.44.0.0/24"`, `"127.0.0.0/24"`, `address: "10.44.0.1"`, `address: "127.0.0.1"`).Replace(g), "overlaps 127.0.0.0/8"},
		{"another deployment's user", strings.Replace(g, `user: "paisans-f2a9"`, `user: "paisans-0a0b"`, 1), "paisans-0a0b is another deployment's name"},
		{"a web server app with a bad listen", strings.Replace(g, "web_server: []", "web_server:\n  - app: \"status\"\n    hostname: \"status.example.org\"\n    listen: \"0.0.0.0\"", 1), "web_server[0].listen"},
		{"a comment added", g + "# fine\n", "not exactly as paisans host request wrote it"},
		{"CRLF line endings", strings.ReplaceAll(g, "\n", "\r\n"), "not exactly as paisans host request wrote it"},
		{"the header removed", g[strings.Index(g, "version:"):], "not exactly as paisans host request wrote it"},
		{"a value unquoted", strings.Replace(g, `user: "paisans-f2a9"`, `user: paisans-f2a9`, 1), "not exactly as paisans host request wrote it"},
		{"an anchor", strings.Replace(g, `user: "paisans-f2a9"`, `user: &u "paisans-f2a9"`, 1), "not exactly as paisans host request wrote it"},
		{"a second document", g + "---\nversion: 1\n", "not exactly as paisans host request wrote it"},
		{"a duplicate key", strings.Replace(g, "endpoint: true\n", "endpoint: true\nendpoint: false\n", 1), "already defined"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := admission.Parse([]byte(c.data))
			if err == nil {
				t.Fatalf("accepted:\n%s", c.data)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v\nwant it to say %q", err, c.want)
			}
		})
	}
}

// The attack the digest exists for: someone who can change the request on
// its way adds their own key. The result is a request Parse accepts, so
// only the digest can tell, and its digest is not the one the operator
// printed. Anything they could hide from Parse instead (a comment, a second
// document, an anchor) is refused above.
func TestAForgedKeyChangesTheDigest(t *testing.T) {
	sent := admission.Encode(build(t, "home-a"))
	forged := build(t, "home-a")
	forged.Operators = append(forged.Operators, admission.Operator{Name: "mallory", Keys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"}})
	data := admission.Encode(forged)
	if _, err := admission.Parse(data); err != nil {
		t.Fatalf("the forgery is refused for another reason, so this test proves nothing: %v", err)
	}
	if admission.Digest(data) == admission.Digest(sent) {
		t.Fatal("a request with a key added has the digest of the one sent")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/admission/`
Expected: FAIL to compile, `undefined: admission.Encode`, `undefined: admission.Parse`, `undefined: admission.Digest`, `undefined: admission.Fields`.

- [ ] **Step 3: Write the encoder, the parser and the digest**

Create `internal/admission/encode.go`:

```go
package admission

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// header opens every request, for the owner who opens the file first.
const header = "# A request to admit one site of a paisans deployment to this host.\n" +
	"# Compare its digest with the operator who sent it before you admit it:\n" +
	"#   sudo paisans host admit --request <this file>\n"

// Encode is r as the request's file: one form for each request, written by
// hand rather than by a YAML library, so the bytes, and so the digest,
// depend on the request alone. Every string is double quoted, so no value
// reads as another type. Encode is for a request Check accepts.
func Encode(r Request) []byte {
	var b bytes.Buffer
	b.WriteString(header)
	fmt.Fprintf(&b, "version: %d\n", r.Version)
	fmt.Fprintf(&b, "deployment: %s\n", quote(r.Deployment))
	fmt.Fprintf(&b, "user: %s\n", quote(r.User))
	fmt.Fprintf(&b, "ssh_port: %d\n", r.SSHPort)
	fmt.Fprintf(&b, "listen_port: %d\n", r.ListenPort)
	fmt.Fprintf(&b, "mesh_subnet: %s\n", quote(r.MeshSubnet))
	fmt.Fprintf(&b, "address: %s\n", quote(r.Address))
	fmt.Fprintf(&b, "endpoint: %t\n", r.Endpoint)
	b.WriteString("grants:\n")
	fmt.Fprintf(&b, "  watchdog: %t\n", r.Grants.Watchdog)
	fmt.Fprintf(&b, "  public_ports: %t\n", r.Grants.PublicPorts)
	if len(r.WebServer) == 0 {
		b.WriteString("web_server: []\n")
	} else {
		b.WriteString("web_server:\n")
		for _, w := range r.WebServer {
			fmt.Fprintf(&b, "  - app: %s\n", quote(w.App))
			fmt.Fprintf(&b, "    hostname: %s\n", quote(w.Hostname))
			fmt.Fprintf(&b, "    listen: %s\n", quote(w.Listen))
		}
	}
	if len(r.Operators) == 0 {
		b.WriteString("operators: []\n")
	} else {
		b.WriteString("operators:\n")
		for _, o := range r.Operators {
			fmt.Fprintf(&b, "  - name: %s\n", quote(o.Name))
			if len(o.Keys) == 0 {
				b.WriteString("    keys: []\n")
				continue
			}
			b.WriteString("    keys:\n")
			for _, k := range o.Keys {
				fmt.Fprintf(&b, "      - %s\n", quote(k))
			}
		}
	}
	return b.Bytes()
}

// quote is s as a YAML double-quoted scalar. JSON's string form is one:
// every escape it writes, \u00XX included, means the same in YAML.
func quote(s string) string {
	out, _ := json.Marshal(s)
	return string(out)
}

// fields is every key a request's mappings hold, by where the mapping is.
var fields = map[string][]string{
	"":           {"version", "deployment", "user", "ssh_port", "listen_port", "mesh_subnet", "address", "endpoint", "grants", "web_server", "operators"},
	"grants":     {"watchdog", "public_ports"},
	"web_server": {"app", "hostname", "listen"},
	"operators":  {"name", "keys"},
}

// Parse reads a request as host admit does, refusing anything it would
// have to guess at: a version other than RequestVersion, a field missing
// or unknown, any problem Check finds, and a file that is not exactly the
// bytes Encode writes for what it holds. That last rule means nothing in
// the file goes unread (no comment, anchor, tag, duplicate key or second
// document), and that the digest of the file is the digest of the request
// the owner is shown.
func Parse(data []byte) (Request, error) {
	refuse := func(problems ...string) (Request, error) {
		return Request{}, &Error{What: "the request", Problems: problems}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return refuse(fmt.Sprintf("it is not YAML (%v)", err))
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return refuse("it is not a request: a request is one YAML mapping, as paisans host request writes it")
	}
	root := doc.Content[0]
	if v := value(root, "version"); v == nil {
		return refuse("version: missing")
	} else if n, err := strconv.Atoi(v.Value); err != nil || v.Kind != yaml.ScalarNode || n != RequestVersion {
		return refuse(fmt.Sprintf("version: %s, and this paisans reads version %d requests. Install the paisans release the operator runs, or ask them for a version %d request", v.Value, RequestVersion, RequestVersion))
	}
	if problems := shape(root, ""); len(problems) > 0 {
		return refuse(problems...)
	}
	var r Request
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return refuse(fmt.Sprintf("a field holds the wrong kind of value (%v)", err))
	}
	if problems := r.Check(); len(problems) > 0 {
		return refuse(problems...)
	}
	if !bytes.Equal(Encode(r), data) {
		return refuse("it is not exactly as paisans host request wrote it: something changed it after it was written (a comment, spacing, quoting or line endings). Ask the operator to send the file again, as an attachment, and compare the digest again")
	}
	return r, nil
}

// value is the value of key in mapping m, nil when m has no such key.
func value(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// shape is each field missing from or unknown to the mapping m found at
// path, and to the mappings inside it.
func shape(m *yaml.Node, path string) []string {
	var problems []string
	known := map[string]bool{}
	for _, k := range fields[path] {
		known[k] = true
	}
	at := func(k string) string {
		if path == "" {
			return k
		}
		return path + "." + k
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := m.Content[i].Value; !known[k] {
			problems = append(problems, at(k)+": not a field of a request")
		}
	}
	for _, k := range fields[path] {
		v := value(m, k)
		switch {
		case v == nil:
			problems = append(problems, at(k)+": missing")
		case path == "" && k == "grants" && v.Kind == yaml.MappingNode:
			problems = append(problems, shape(v, "grants")...)
		case path == "" && (k == "web_server" || k == "operators") && v.Kind == yaml.SequenceNode:
			for _, item := range v.Content {
				if item.Kind == yaml.MappingNode {
					problems = append(problems, shape(item, k)...)
				}
			}
		}
	}
	return problems
}

// DigestLength is how many hex digits of the SHA-256 a digest keeps, and
// DigestGroup how many each group holds.
const (
	DigestLength = 12
	DigestGroup  = 4
)

// Digest is the owner's check that a request is the one its operator sent:
// the first twelve hex digits of the SHA-256 of the file, in three groups,
// Eg: 1a2b-3c4d-5e6f. host request prints it, host admit prints it again,
// and the two are compared over a channel both trust.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	hexed := hex.EncodeToString(sum[:])[:DigestLength]
	var groups []string
	for i := 0; i < DigestLength; i += DigestGroup {
		groups = append(groups, hexed[i:i+DigestGroup])
	}
	return strings.Join(groups, "-")
}

// Fields is every field a request holds, by its path: each top level key,
// and each key of grants, of a web_server entry and of an operator, as
// <parent>.<key>.
func Fields() []string {
	out := slices.Clone(fields[""])
	for _, parent := range []string{"grants", "web_server", "operators"} {
		for _, k := range fields[parent] {
			out = append(out, parent+"."+k)
		}
	}
	return out
}
```

- [ ] **Step 4: Generate the golden requests and read them**

Run: `go test ./internal/admission/ -run TestGoldenRequests -update && git status --short internal/admission/testdata/requests/`
Expected: six new `.admit` files. Read each against the fixture: `home-a` has both grants, `endpoint: true`, `listen_port: 51820`, user `paisans-f2a9`, alice then bob with bob's two keys; `home-b` has user `community`, `ssh_port: 2222`, `listen_port: 0`, the watchdog only; `vm` has `listen_port: 51820` and `endpoint: false`; `status` has no grant and one `web_server` entry; `watch` has `public_ports` and `listen_port: 51821`; `store` has nothing granted and `listen_port: 0`. No key carries a comment. `home-a.admit` must read exactly:

```yaml
# A request to admit one site of a paisans deployment to this host.
# Compare its digest with the operator who sent it before you admit it:
#   sudo paisans host admit --request <this file>
version: 1
deployment: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"
user: "paisans-f2a9"
ssh_port: 22
listen_port: 51820
mesh_subnet: "10.44.0.0/24"
address: "10.44.0.1"
endpoint: true
grants:
  watchdog: true
  public_ports: true
web_server: []
operators:
  - name: "alice"
    keys:
      - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
  - name: "bob"
    keys:
      - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
      - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
```

and `status.admit`:

```yaml
# A request to admit one site of a paisans deployment to this host.
# Compare its digest with the operator who sent it before you admit it:
#   sudo paisans host admit --request <this file>
version: 1
deployment: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"
user: "paisans-f2a9"
ssh_port: 22
listen_port: 51820
mesh_subnet: "10.44.0.0/24"
address: "10.44.0.4"
endpoint: false
grants:
  watchdog: false
  public_ports: false
web_server:
  - app: "status"
    hostname: "status.example.org"
    listen: "127.0.0.1:8480"
operators:
  - name: "alice"
    keys:
      - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/admission/ && go vet ./internal/admission/`
Expected: PASS, with `TestParseRefuses` running each row as its own subtest.

- [ ] **Step 6: Commit**

```bash
git add internal/admission/encode.go internal/admission/parse_test.go internal/admission/testdata/requests
git commit -m "feat: encode, parse and digest an admission request

The request carries the keys that will get a shell on the owner's
machine, and the owner's check that it is the one the operator sent is a
digest compared over a channel both trust. That check is only as good as
the guarantee that the digest covers what admit acts on, so a request
has one form, written by hand with every string quoted, and Parse
refuses any file that is not exactly that form: a comment, an anchor, a
second document or rewritten line endings cannot carry something the
owner is not shown. Parse also refuses a version it does not read and a
field missing or unknown, and runs Check.

Nothing an operator would notice yet, so no changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: The admission record, and reading it as the deployment's user

**Files:**
- Create: `internal/admission/record.go`
- Test: `internal/admission/record_test.go`
- Generated: `internal/admission/testdata/admitted.f2a9.json`

**Interfaces:**
- Consumes: Task 1's `Grants`; Task 2's `Request`, `Error`, `config.ValidUserName`; Task 3's `Encode`, `Digest`; test helpers `update`, `build`.
- Produces: `const RecordVersion = 1`; `const Dir = "/etc/paisans"`; `const Mode = 0o644`; `func Path(token string) string` (`/etc/paisans/admitted.<token>.json`); `var ErrNotAdmitted error`; `type Record struct { Version int; Deployment, User string; Grants Grants; Forwarding bool; Ports Ports; Digest, AdmittedAt, DataDir string; BridgeNetfilter bool; WebServer *WebServer }` (JSON keys `version`, `deployment`, `user`, `grants`, `forwarding`, `ports`, `digest`, `admitted_at`, `data_dir`, `bridge_netfilter`, `web_server`); `type Ports struct { SSH, WireGuard int; Public []int }` (`ssh`, `wireguard`, `public`); `type WebServer struct { Server, Container, Process, Config, Main, Certs, CertFile, KeyFile, Folder, Certbot, AdminTCP, Why string }`; `func NewRecord(req Request, granted Grants, digest string, at time.Time) Record`; `func EncodeRecord(r Record) []byte`; `func ParseRecord(data []byte, token string) (Record, error)`; `type Reader interface { ReadFile(path string) (string, bool, error) }`; `func Read(t Reader, token string) (Record, error)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/admission/record_test.go`:

```go
package admission_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
)

// host is a host's files, read as the deployment's user reads them.
type host struct {
	files map[string]string
	err   error
	read  []string
}

func (h *host) ReadFile(path string) (string, bool, error) {
	h.read = append(h.read, path)
	if h.err != nil {
		return "", false, h.err
	}
	c, ok := h.files[path]
	return c, ok, nil
}

var at = time.Date(2026, 10, 15, 12, 0, 0, 0, time.FixedZone("CEST", 2*3600))

// The record admit writes for the home-a request, with both grants given:
// its golden file pins every field and its order.
func TestRecordGolden(t *testing.T) {
	req := build(t, "home-a")
	rec := admission.NewRecord(req, admission.Grants{Watchdog: true, PublicPorts: true}, admission.Digest(admission.Encode(req)), at)
	rec.DataDir = "/srv/data/paisans-f2a9"
	rec.WebServer = &admission.WebServer{Server: "caddy", Process: "caddy", Config: "/etc/caddy/Caddyfile", Main: "/etc/caddy/Caddyfile", Certs: "automatic", AdminTCP: "localhost:2019"}
	got := admission.EncodeRecord(rec)
	path := filepath.Join("testdata", "admitted.f2a9.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	back, err := admission.ParseRecord(got, "f2a9")
	if err != nil {
		t.Fatal(err)
	}
	if string(admission.EncodeRecord(back)) != string(got) {
		t.Errorf("read back differently:\n%s", admission.EncodeRecord(back))
	}
}

// What NewRecord derives: ports from the request and the grants,
// forwarding from the endpoint, the time in UTC, nothing given that the
// owner did not give.
func TestNewRecord(t *testing.T) {
	rec := admission.NewRecord(build(t, "home-a"), admission.Grants{Watchdog: true}, "", at)
	if rec.Grants != (admission.Grants{Watchdog: true}) || !rec.Forwarding || rec.User != "paisans-f2a9" {
		t.Errorf("got %+v", rec)
	}
	if rec.Ports.SSH != 22 || rec.Ports.WireGuard != 51820 || len(rec.Ports.Public) != 0 {
		t.Errorf("ports %+v: 80 and 443 opened without public_ports", rec.Ports)
	}
	if rec.AdmittedAt != "2026-10-15T10:00:00Z" {
		t.Errorf("admitted_at %q, want UTC", rec.AdmittedAt)
	}
	open := admission.NewRecord(build(t, "watch"), admission.Grants{PublicPorts: true}, "", at)
	if len(open.Ports.Public) != 2 || open.Ports.Public[0] != 80 || open.Ports.Public[1] != 443 {
		t.Errorf("public ports %v", open.Ports.Public)
	}
	if store := admission.NewRecord(build(t, "store"), admission.Grants{}, "", at); store.Forwarding || store.Ports.WireGuard != 0 {
		t.Errorf("store: %+v", store)
	}
}

// Read reads /etc/paisans/admitted.<token>.json through the transport it is
// given, with no sudo, and says when there is none.
func TestRead(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "admitted.f2a9.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := &host{files: map[string]string{"/etc/paisans/admitted.f2a9.json": string(data)}}
	rec, err := admission.Read(h, "f2a9")
	if err != nil {
		t.Fatal(err)
	}
	if rec.User != "paisans-f2a9" || !rec.Grants.Watchdog || rec.WebServer == nil || rec.WebServer.AdminTCP != "localhost:2019" {
		t.Errorf("got %+v", rec)
	}
	if len(h.read) != 1 || h.read[0] != "/etc/paisans/admitted.f2a9.json" {
		t.Errorf("read %q", h.read)
	}
	if _, err := admission.Read(&host{files: map[string]string{}}, "f2a9"); !errors.Is(err, admission.ErrNotAdmitted) {
		t.Errorf("no record: %v", err)
	}
	down := errors.New("ssh: connect to host home-a.example.org port 22: Operation timed out")
	if _, err := admission.Read(&host{err: down}, "f2a9"); !errors.Is(err, down) || errors.Is(err, admission.ErrNotAdmitted) {
		t.Errorf("a failed read: %v", err)
	}
}

// A record is read as strictly as a request: another version, a field
// missing or unknown, another deployment's, or a user paisans refuses.
func TestParseRecordRefuses(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "admitted.f2a9.json"))
	if err != nil {
		t.Fatal(err)
	}
	g := string(data)
	for _, c := range []struct {
		name, data, token, want string
	}{
		{"not JSON", "{", "f2a9", "it is not JSON"},
		{"another version", strings.Replace(g, `"version": 1`, `"version": 2`, 1), "f2a9", "version: 2, and this paisans reads version 1"},
		{"no version", strings.Replace(g, `"version": 1,`, ``, 1), "f2a9", "version: missing"},
		{"a missing field", strings.Replace(g, `"forwarding": true,`, ``, 1), "f2a9", "forwarding: missing"},
		{"an unknown field", strings.Replace(g, `"forwarding": true,`, `"forwarding": true, "sudo": true,`, 1), "f2a9", "unknown field"},
		{"another token", g, "0a0b", "is not a deployment whose token is 0a0b"},
		{"a user paisans refuses", strings.Replace(g, `"user": "paisans-f2a9"`, `"user": "a;b"`, 1), "f2a9", "user: \"a;b\""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := admission.ParseRecord([]byte(c.data), c.token); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v\nwant it to say %q", err, c.want)
			}
		})
	}
}

// The path, the mode and the directory phase 5 writes to.
func TestRecordPlace(t *testing.T) {
	if got := admission.Path("f2a9"); got != "/etc/paisans/admitted.f2a9.json" {
		t.Errorf("Path = %s", got)
	}
	if admission.Mode != 0o644 {
		t.Errorf("Mode = %o", admission.Mode)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/admission/`
Expected: FAIL to compile, `undefined: admission.NewRecord`, `undefined: admission.Read`, `undefined: admission.ParseRecord`.

- [ ] **Step 3: Write the record**

Create `internal/admission/record.go`:

```go
package admission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// RecordVersion is the record's layout. A record of another version is
// refused rather than read in part.
const RecordVersion = 1

// Dir is where admission records live on a host: root's, mode 755, so
// every deployment's user reads its own record and none can write one.
const Dir = "/etc/paisans"

// Mode is the record's mode. It is world readable because the deployment's
// own user reads it without sudo, and it holds nothing secret.
const Mode = 0o644

// Path is the record of the deployment with token on a host.
func Path(token string) string { return Dir + "/admitted." + token + ".json" }

// ErrNotAdmitted is a host with no record for the deployment: its owner has
// not admitted it, or has evicted it.
var ErrNotAdmitted = errors.New("no admission record")

// Record is what host admit leaves on the host for one deployment: who it
// runs as and what the owner granted, so the deployment's commands, which
// cannot read root's files, learn it, and host deployments lists it.
type Record struct {
	Version    int    `json:"version"`
	Deployment string `json:"deployment"`
	User       string `json:"user"`
	// Grants are what the owner gave, which may be less than the site's
	// roles need: Needs(site).Missing(record.Grants) is what is lacking.
	Grants Grants `json:"grants"`
	// Forwarding is whether admit turned on net.ipv4.ip_forward for the
	// site, which it does for a site with an endpoint.
	Forwarding bool `json:"forwarding"`
	// Ports are what the firewall opened for the deployment.
	Ports Ports `json:"ports"`
	// Digest is the request's digest when admit ran from one, "" when it
	// ran from the configuration with --site.
	Digest string `json:"digest"`
	// AdmittedAt is when admit last ran with --execute, RFC 3339 in UTC.
	AdmittedAt string `json:"admitted_at"`
	// DataDir is the deployment's Docker data-root, <path>/paisans-<token>,
	// when the owner gave --data-dir; "" when it is under the user's home.
	DataDir string `json:"data_dir"`
	// BridgeNetfilter is whether the owner passed --bridge-netfilter, which
	// lets admit load br_netfilter on a host with bridges of its own.
	BridgeNetfilter bool `json:"bridge_netfilter"`
	// WebServer is what admit found in front of a site with
	// web_server.managed_by: external, nil for every other site.
	WebServer *WebServer `json:"web_server"`
}

// Ports are the ports the firewall opened: the SSH port, the WireGuard port
// (0 when it opened none), and 80 and 443 with public_ports.
type Ports struct {
	SSH       int   `json:"ssh"`
	WireGuard int   `json:"wireguard"`
	Public    []int `json:"public"`
}

// WebServer is the owner's web server as admit detected it, as root, since
// the deployment's user cannot: the system Docker socket, ss -p, nginx -T
// and /etc/letsencrypt are root's. web-server show fills its snippet from
// it. Every field is a string or a list of strings, so the record reads the
// same to any toolkit of its version.
type WebServer struct {
	// Server is caddy, nginx or apache, "" when not determined (Why says).
	Server string `json:"server"`
	// Container is the Docker container it runs in, Process the process
	// listening on 443 or 80 on the host; one is set.
	Container string `json:"container"`
	Process   string `json:"process"`
	// Config is its configuration's path on the host, Main the same as the
	// server reads it (inside the container, for one in a container).
	Config string `json:"config"`
	Main   string `json:"main"`
	// Certs is automatic, files or folder, "" when not determined.
	Certs    string `json:"certs"`
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
	Folder   string `json:"folder"`
	Certbot  string `json:"certbot"`
	// AdminTCP is the TCP address a Caddy answers its admin API on, "" for
	// a unix socket, admin off, or a server that is not Caddy. Admit warns
	// when it is set: any local user can replace that Caddy's configuration.
	AdminTCP string `json:"admin_tcp"`
	Why      string `json:"why"`
}

// NewRecord is the record admit writes for req, with the grants the owner
// gave, at at. The ports follow from req and the grants; forwarding follows
// the endpoint. Admit sets DataDir, BridgeNetfilter and WebServer itself.
func NewRecord(req Request, granted Grants, digest string, at time.Time) Record {
	public := []int{}
	if granted.PublicPorts {
		public = []int{80, 443}
	}
	return Record{
		Version:    RecordVersion,
		Deployment: req.Deployment,
		User:       req.User,
		Grants:     granted,
		Forwarding: req.Endpoint,
		Ports:      Ports{SSH: req.SSHPort, WireGuard: req.ListenPort, Public: public},
		Digest:     digest,
		AdmittedAt: at.UTC().Format(time.RFC3339),
	}
}

// EncodeRecord is the record's file: indented JSON with a final newline.
func EncodeRecord(r Record) []byte {
	if r.Ports.Public == nil {
		r.Ports.Public = []int{}
	}
	out, _ := json.MarshalIndent(r, "", "  ")
	return append(out, '\n')
}

// recordFields is every key a record holds; a record missing one was not
// written by a toolkit of its version.
var recordFields = []string{"version", "deployment", "user", "grants", "forwarding", "ports", "digest", "admitted_at", "data_dir", "bridge_netfilter", "web_server"}

// ParseRecord reads a record strictly: its version, every field present and
// none unknown, a deployment whose token is token, and a user paisans
// accepts.
func ParseRecord(data []byte, token string) (Record, error) {
	refuse := func(problems ...string) (Record, error) {
		return Record{}, &Error{What: "the admission record " + Path(token), Problems: problems}
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return refuse(fmt.Sprintf("it is not JSON (%v)", err))
	}
	raw, ok := keys["version"]
	if !ok {
		return refuse("version: missing")
	}
	var version int
	if err := json.Unmarshal(raw, &version); err != nil || version != RecordVersion {
		return refuse(fmt.Sprintf("version: %s, and this paisans reads version %d records. Run the paisans release the host's owner admitted it with", raw, RecordVersion))
	}
	var problems []string
	for _, k := range recordFields {
		if _, ok := keys[k]; !ok {
			problems = append(problems, k+": missing")
		}
	}
	if len(problems) > 0 {
		return refuse(problems...)
	}
	var r Record
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return refuse(fmt.Sprintf("a field is unknown or holds the wrong kind of value (%v)", err))
	}
	if !deployment.ValidID(r.Deployment) || (deployment.Deployment{ID: r.Deployment}).Token() != token {
		problems = append(problems, fmt.Sprintf("deployment: %q is not a deployment whose token is %s", r.Deployment, token))
	}
	if !config.ValidUserName(r.User) {
		problems = append(problems, fmt.Sprintf("user: %q is not a user name paisans accepts", r.User))
	}
	if len(problems) > 0 {
		return refuse(problems...)
	}
	return r, nil
}

// Reader reads a file on a host, as apply.Transport does.
type Reader interface {
	ReadFile(path string) (content string, found bool, err error)
}

// Read is the deployment's admission record on the host t reaches, read as
// whatever user t logs in as: the record is mode 644, so the deployment's
// own user reads it without sudo. ErrNotAdmitted when there is none.
func Read(t Reader, token string) (Record, error) {
	content, found, err := t.ReadFile(Path(token))
	if err != nil {
		return Record{}, fmt.Errorf("reading the admission record %s: %w", Path(token), err)
	}
	if !found {
		return Record{}, fmt.Errorf("%s: %w", Path(token), ErrNotAdmitted)
	}
	return ParseRecord([]byte(content), token)
}
```

- [ ] **Step 4: Generate the golden record and read it**

Run: `go test ./internal/admission/ -run TestRecordGolden -update && cat internal/admission/testdata/admitted.f2a9.json`
Expected, exactly (the digest is that of `home-a.admit`):

```json
{
  "version": 1,
  "deployment": "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01",
  "user": "paisans-f2a9",
  "grants": {
    "watchdog": true,
    "public_ports": true
  },
  "forwarding": true,
  "ports": {
    "ssh": 22,
    "wireguard": 51820,
    "public": [
      80,
      443
    ]
  },
  "digest": "b1fb-1e05-4815",
  "admitted_at": "2026-10-15T10:00:00Z",
  "data_dir": "/srv/data/paisans-f2a9",
  "bridge_netfilter": false,
  "web_server": {
    "server": "caddy",
    "container": "",
    "process": "caddy",
    "config": "/etc/caddy/Caddyfile",
    "main": "/etc/caddy/Caddyfile",
    "certs": "automatic",
    "cert_file": "",
    "key_file": "",
    "folder": "",
    "certbot": "",
    "admin_tcp": "localhost:2019",
    "why": ""
  }
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/admission/ && go vet ./internal/admission/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/admission/record.go internal/admission/record_test.go internal/admission/testdata/admitted.f2a9.json
git commit -m "feat: define the admission record and read it as the deployment's user

The deployment's own commands cannot read root's files, so host admit
leaves what it granted in /etc/paisans/admitted.<token>.json, mode 644:
the user, the grants, forwarding, the ports the firewall opened, the
request's digest, the time, the data directory, the bridge netfilter
flag and the owner's web server as admit detected it as root. Read takes
any transport's ReadFile and needs no sudo, and refuses a record of
another version, another deployment's, or with a field missing or
unknown, rather than read it in part. Phase 5 writes it with NewRecord,
EncodeRecord, Path and Mode.

Nothing an operator would notice yet, so no changelog entry.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: `paisans host request`

**Files:**
- Create: `cmd/paisans/hostrequest.go`
- Modify: `cmd/paisans/main.go` (the `case "host":` switch), `cmd/paisans/help.go` (`groupSummary["host"]`, a `helpCommands` entry after `host deployments`)
- Modify: `docs/commands.md` (after `### paisans host deployments`), `README.md` (before `#### Keys belong to operators, and sites name operators`), `CHANGELOG.md`
- Test: `cmd/paisans/hostrequest_test.go`

**Interfaces:**
- Consumes: `admission.Build`, `admission.Encode`, `admission.Digest`; in `cmd/paisans`: `newFlagSet`, `commonFlags`, `errReporter`, `reportFindings`, `refused`; test helpers `captureOutput(t, fn) (stdout, stderr string)` (`clients_test.go`) and `fixtureConfig()` (`app_test.go`, `internal/render/testdata/deployment.yaml`, whose `vm` is `[gateway, witness]` with user `ubuntu`, address `10.44.0.3` and an endpoint).
- Produces: `func runHostRequest(args []string) error`, dispatched for `paisans host request`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/paisans/hostrequest_test.go`:

```go
package main

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
)

// The request is stdout and nothing else, so it can be redirected to the
// file the owner admits; the digest is on stderr, and it is the digest of
// exactly what stdout carried.
func TestHostRequestWritesTheRequestAndItsDigest(t *testing.T) {
	var err error
	stdout, stderr := captureOutput(t, func() {
		err = runHostRequest([]string{"--site", "vm", "--config", fixtureConfig()})
	})
	if err != nil {
		t.Fatal(err)
	}
	req, perr := admission.Parse([]byte(stdout))
	if perr != nil {
		t.Fatalf("stdout is not a request admit reads: %v\n%s", perr, stdout)
	}
	if req.User != "ubuntu" || req.Address != "10.44.0.3" || !req.Grants.PublicPorts || req.Grants.Watchdog {
		t.Errorf("not vm's request: %+v", req)
	}
	digest := admission.Digest([]byte(stdout))
	if !strings.Contains(stderr, "digest "+digest) {
		t.Errorf("stderr does not carry the digest %s:\n%s", digest, stderr)
	}
	if strings.Contains(stdout, digest) {
		t.Errorf("the digest is on stdout, inside the request:\n%s", stdout)
	}
}

// Two runs print the same bytes, so the same digest.
func TestHostRequestIsStable(t *testing.T) {
	first, _ := captureOutput(t, func() { _ = runHostRequest([]string{"--site", "home-a", "--config", fixtureConfig()}) })
	second, _ := captureOutput(t, func() { _ = runHostRequest([]string{"--site", "home-a", "--config", fixtureConfig()}) })
	if first == "" || first != second {
		t.Errorf("two runs differ:\n%s\n%s", first, second)
	}
}

// A site the configuration does not declare, no site at all, and a stray
// argument are refused, and nothing is written to stdout.
func TestHostRequestRefuses(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"an undeclared site", []string{"--site", "home-z", "--config", fixtureConfig()}, `declares no site "home-z"`},
		{"no site", []string{"--config", fixtureConfig()}, "name the site with --site"},
		{"a stray argument", []string{"--site", "vm", "--config", fixtureConfig(), "extra"}, "takes no argument"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var err error
			stdout, _ := captureOutput(t, func() { err = runHostRequest(c.args) })
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want %q", err, c.want)
			}
			if stdout != "" {
				t.Errorf("wrote to stdout:\n%s", stdout)
			}
		})
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/paisans/ -run TestHostRequest`
Expected: FAIL to compile, `undefined: runHostRequest`.

- [ ] **Step 3: Write the command**

Create `cmd/paisans/hostrequest.go`:

```go
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// runHostRequest is `paisans host request`: the request a host's owner
// admits one site from, on stdout, and its digest on stderr, so the request
// can be redirected to a file and the digest still read. It reaches no host.
func runHostRequest(args []string) error {
	fs := newFlagSet("host request", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	site := fs.String("site", "", "the site to request admission for, by the name it has in the configuration")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("host request takes no argument: paisans host request --site <name> > <name>.admit. Got extra argument(s): %s", strings.Join(fs.Args(), " "))
	}
	if *site == "" {
		return &ui.Problem{Hint: "host request: name the site with --site", Explain: "paisans host request --site <name> > <name>.admit writes the request the host's owner admits that site from."}
	}
	// The request goes to stdout and nothing else does.
	r := errReporter(reporter())
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	result := validate.Check(cfg)
	if result.Refused() {
		reportFindings(r, *configPath, result)
		return refused(*configPath, len(result.Refusals()), "No request is written for a configuration validate refuses.")
	}
	req, err := admission.Build(cfg, *site)
	if err != nil {
		return fmt.Errorf("host request: %w", err)
	}
	data := admission.Encode(req)
	if _, err := os.Stdout.Write(data); err != nil {
		return fmt.Errorf("host request: writing the request: %w", err)
	}
	r.Result("digest %s: tell it to the host's owner over a channel you both trust, such as a call; host admit shows the same", admission.Digest(data))
	return nil
}
```

In `cmd/paisans/main.go`, in the `case "host":` switch, replace

```go
		case len(os.Args) >= 3 && os.Args[2] == "deployments":
			err = runHostDeployments(os.Args[3:])
		default:
			os.Exit(groupError(os.Stderr, "host", "host takes one subcommand, prepare or deployments"))
```

with

```go
		case len(os.Args) >= 3 && os.Args[2] == "deployments":
			err = runHostDeployments(os.Args[3:])
		case len(os.Args) >= 3 && os.Args[2] == "request":
			err = runHostRequest(os.Args[3:])
		default:
			os.Exit(groupError(os.Stderr, "host", "host takes one subcommand, prepare, deployments or request"))
```

- [ ] **Step 4: Run the help tests to verify they fail**

Run: `go test ./cmd/paisans/ -run 'TestHostRequest|TestEveryFlagSetHasAPage'`
Expected: the `TestHostRequest` tests PASS; `TestEveryFlagSetHasAPage` FAILS with `host request has a flag set and no help page`.

- [ ] **Step 5: Give it a help page**

In `cmd/paisans/help.go`, replace

```go
	"host":        "Prepare a host, or list what is on it.",
```

with

```go
	"host":        "Prepare a host, list what is on it, or request admission.",
```

(the first page's line must stay inside 80 columns, which `TestTopHelpListsEveryCommandOnce` checks), and insert after the `host deployments` entry of `helpCommands`, before `site add`:

```go
	{
		name:    "host request",
		summary: "Write the request a host's owner admits a site from.",
		usage:   []string{"paisans host request --site <name> [--config " + config.FileName + "] > <name>.admit"},
		detail: "Write to stdout the request the host's owner admits the site from with sudo paisans host admit --request <file>: the deployment's id, the site's user, SSH and WireGuard ports, mesh address and subnet, whether it has an endpoint, the grants its roles need, the apps handed to the owner's web server, and each operator's public keys. " +
			"Print its digest to stderr, for the owner to compare over a channel you both trust, since the request carries the keys that will log in to their machine. " +
			"Reaches no host; holds nothing secret.",
	},
```

Run: `go test ./cmd/paisans/ -run 'TestHostRequest|TestEveryFlagSetHasAPage|TestEveryCommandAcceptsVerbose|TestTopHelpListsEveryCommandOnce|TestCommandHelpWraps|TestResolveHelp|TestHostHasNoAllowlistCommands'`
Expected: PASS.

Run: `go run ./cmd/paisans help host && go run ./cmd/paisans host request --help`
Expected: the host page lists `request  Write the request a host's owner admits a site from.`; the command's page shows the usage line and the detail wrapped inside 80 columns.

Run, from `internal/render/testdata`: `go run ../../../cmd/paisans host request --site vm --config deployment.yaml > /dev/null`
Expected: stderr ends `digest xxxx-xxxx-xxxx: tell it to the host's owner over a channel you both trust, such as a call; host admit shows the same`, exit 0.

- [ ] **Step 6: Document the command**

In `docs/commands.md`, insert before `### \`paisans prune\``:

````markdown
### `paisans host request`

Writes the request a host's owner admits one site from, to stdout, and its
digest to stderr. The owner, who needs the CLI and nothing else, runs `sudo
paisans host admit --request <file>` on the host and compares the digest it
shows with the one this printed.

```sh
paisans host request --site home-a > home-a.admit   # the digest is on stderr
```

* **Alone:** always: it is the operator's half of admission when the host's
  owner does not have the configuration, and every command that finds a
  site not admitted names it.
* **Flags:** `--site <name>` (required), `--config`.
* **Hosts:** reaches none. Refuses a site the configuration does not
  declare, a configuration `validate` refuses, and a request `host admit`
  would refuse.
* **`--execute`:** not taken. It writes nothing but stdout.
* **The request:** YAML holding nothing secret: the deployment's id, the
  site's user, its SSH and WireGuard ports (`listen_port: 0` when no site
  dials it), its mesh address and the subnet, whether it has an endpoint,
  the grants its roles need (`watchdog` for a data site, `public_ports` for
  a gateway or a monitor running the toolkit's Caddy), the apps a monitor
  behind the owner's web server hands it, and each operator's public keys,
  without their comments. Two runs print the same bytes, so the same digest.

See *A host's owner admits a site from a request* in `README.md`.

````

In `README.md`, insert before `#### Keys belong to operators, and sites name operators`:

```markdown
#### A host's owner admits a site from a request

An owner who does not have the deployment's configuration admits a site
from a request: `paisans host request --site <site> > <site>.admit` writes
it, and the owner runs `sudo paisans host admit --request <site>.admit` on
the host. The request holds what admit needs and nothing secret: the
deployment's id, the site's user, its SSH and WireGuard ports, its mesh
address and the mesh subnet, whether it has an endpoint, the grants its
roles need (the watchdog for a data site, ports 80 and 443 for a gateway or
a monitor running the toolkit's Caddy), the apps a monitor behind the
owner's web server hands it, and each operator's public keys.

Those keys get a shell on the owner's machine, so the request is only as
trustworthy as the way it travelled. `host request` prints a digest of it
on stderr, the first twelve hex digits of its SHA-256 in three groups, and
`host admit` prints the same digest of the file it was given; the owner
compares the two over a channel they trust, such as a call. A digest covers
what admit acts on only if admit reads every byte it covers, so a request
is refused unless it is exactly the bytes `host request` writes for what it
holds: a comment, an anchor, a second document or rewritten line endings
cannot carry something the owner was not shown. Keys travel without their
comments, which are free text a forger could vary while searching for a
request with a matching digest.

A request is read as strictly as a configuration. An unknown version, a
field missing or unknown, a user name `ssh.user` would refuse, `root`, a
`paisans-` name other than the deployment's own, a key that is not a plain
public key, and an address outside the subnet are each refused, and so is a
mesh subnet overlapping `0.0.0.0/8`, loopback, link-local or multicast:
admit's egress table accepts the mesh subnet before it drops the owner's
loopback and link-local ranges, so a subnet over them would let the
deployment reach what the table keeps it from. `host request` applies the
same checks, so it never writes a request admit would refuse.

```

In `CHANGELOG.md`, under `[Unreleased]`, `### Added`, add:

```markdown
- `host request --site <s>` writes the request a host's owner admits that site from, and prints its digest on stderr for the owner to compare over a channel both trust.
```

Check the docs: `grep -n "A host's owner admits a site from a request" README.md docs/commands.md` (one heading in `README.md`, one reference in `docs/commands.md`); `git diff README.md docs/commands.md CHANGELOG.md | perl -CSD -ne 'print if /^\+.*\x{2014}/'` prints nothing; every flag the entry names (`--site`, `--config`) is one `runHostRequest` defines.

- [ ] **Step 7: Run the whole suite**

Run: `gofmt -l internal/admission internal/config cmd/paisans && go vet ./... && go test ./...`
Expected: no file listed by `gofmt`; PASS.

Run: `git diff origin/develop --stat && git diff origin/develop | perl -CSD -ne 'print if /^\+.*\x{2014}/'`
Expected: the files of this plan only; no em dash.

- [ ] **Step 8: Commit**

```bash
git add cmd/paisans/hostrequest.go cmd/paisans/hostrequest_test.go cmd/paisans/main.go cmd/paisans/help.go docs/commands.md README.md CHANGELOG.md
git commit -m "feat: add host request, which writes a site's request and its digest

A host's owner who will not take the deployment's configuration admits a
site from a request the operator sends, so the operator needs a command
that writes it. host request prints the request on stdout, so it can be
redirected to the file that travels, and its digest on stderr, so the
operator can tell the owner over a channel they trust. It reaches no host
and refuses a site the configuration does not declare.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Self-Review Notes

* **Spec coverage.** The request's eleven fields (Task 2), `grants` from roles (Task 1) and `web_server` from `managed_by: external` (Task 2); the digest, twelve hex digits in three groups of SHA-256 over the encoded bytes (Task 3); `Parse`'s refusals of an unknown version, an unknown or missing field, a bad key and an address outside the subnet (Task 3, with `Check`'s in Task 2); the record's user, grants, forwarding, ports, digest, time, web server detection, data directory and bridge netfilter flag, its path and mode, and `Read` as the deployment's user (Task 4); `host request` with stdout and stderr split, refusing an undeclared site, golden requests per role mix, a digest stable across runs and changed by every field (Tasks 3 and 5); `host request` in `docs/commands.md` and help (Task 5).
* **Left to later phases, on purpose.** Writing the record, its mode on a re-run, the typed digest, the early stops on a missing grant, the web server detection itself and every probe of the host are phase 5's (see *What this phase hands to phases 5 and 6*). Reading the record in `host prepare`, `host deployments`, `doctor` and the classification of pending sites, and mapping `admission.WebServer` into `webserver.WebServer`, are phase 6's.
* **Type consistency.** `Grants` (Task 1) is the request's `grants`, the record's `grants`, `NewRecord`'s `granted` and `Missing`'s receiver and argument. `Request.Token()` (Task 2) is the token `Path` (Task 4) takes. `Error` (Task 2) is what `Build`, `Parse` and `ParseRecord` return. The test helpers `update`, `fixture` and `build` are defined once, in Task 2's `helpers_test.go`.
