# arm64 and Mixed Architecture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the toolkit deploy to amd64 and arm64 hosts. It records each site's architecture in `paisans-stack.yaml`, refuses data sites of different architectures, and refuses an image that has no build for its site.

**Architecture:** A small `internal/hostarch` package reads the host's architecture with `dpkg --print-architecture` and holds the rules. `config.Site` gains `Arch`, written back by a byte-level edit beside `SetIncludeDir`. `validate` gains the data-site rule. `cmd/paisans/arch.go` probes and records the key for apply, `host prepare` and `site add`. `preflight.osMatch` compares the architecture as well. `apply.Build` refuses a pinned image that has no platform for the site.

**Tech Stack:** Go 1.x standard library, `gopkg.in/yaml.v3` (already a dependency), the existing `imagedigest` registry client.

**Spec:** `docs/specs/2026-10-13-arm64-and-mixed-architecture.md`

## Global Constraints

- Supported values: `amd64` and `arm64`, exactly. Anything else is refused, naming the site and the reported value.
- The key is `sites.<name>.arch`. The schema change is additive. An older binary rejecting the key is accepted.
- The toolkit writes the key. Each write changes only the added text, and every other byte of the file stays as it was.
- A whole `apply` records missing keys **before** host prepare and **before** `applyStore`/publish. A later `apply --site` step refuses a file whose bytes changed after the publish (`cmd/paisans/main.go:556-558`).
- Messages:
  - A mismatch: `<site>: paisans-stack.yaml records arch <recorded>, and the host reports <reported>. If this is new hardware, delete the arch line from <site> and run again.`
  - Unsupported: `<site>: the host reports <value>. The toolkit supports amd64 and arm64.`
  - Recorded: `<site>: arch <value> recorded in paisans-stack.yaml`
  - Image: `<site> (<arch>): <image> is published for <platforms> only.`
- The key is not added to config review grants or the deployment record.
- Comments and docs explain why. No rejected alternatives anywhere except `docs/decisions.md` (AGENTS.md).
- Verification for every task: `go build ./... && go vet ./... && go test ./...` and `gofmt -l .` empty.

## Review Focus

1. **Existing fake hosts in `cmd/paisans` tests do not answer `dpkg --print-architecture`.** The new probe makes them fail with "no rule". Each fake must answer `amd64`; failing tests are not a reason to skip the probe. Task 5 owns this.
2. **A site mapping written in flow style** (`home-a: {roles: [data], address: 10.44.0.1}`) must gain `arch: amd64, ` inside the braces rather than corrupting the file. Task 2 pins it.
3. **A dry run must not write the file**, yet its plan must still apply the image check using the probed architecture. Task 4 pins it.
4. **An image platform with a variant** (`arm64/v8`) is an arm64 build, and `windows/amd64` is not an amd64 Linux build. Task 1 pins it.
5. **A registry that cannot be read for platforms** must not stop the apply. It becomes a note, and the pull on the host still tells. Task 6 pins it.

---

### Task 1: `internal/hostarch`, reading and judging an architecture

**Files:**
- Create: `internal/hostarch/hostarch.go`
- Test: `internal/hostarch/hostarch_test.go`

**Interfaces:**
- Produces:
  - `const Command = "dpkg --print-architecture"`
  - `type Runner interface { Run(command string) (string, error) }`
  - `func Parse(site, out string) (string, error)`
  - `func Read(site string, t Runner) (string, error)`
  - `func Mismatch(site, recorded, reported string) error` (nil when they agree or `recorded == ""`)
  - `func Supported(arch string) bool`
  - `func Runs(platforms []string, arch string) bool`

- [ ] **Step 1: Write the failing tests**

```go
package hostarch_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/hostarch"
)

type host struct {
	out string
	err error
}

func (h host) Run(command string) (string, error) {
	if command != hostarch.Command {
		return "", errors.New("unexpected " + command)
	}
	return h.out, h.err
}

func TestReadAcceptsEachSupportedArchitecture(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		got, err := hostarch.Read("home-a", host{out: arch + "\n"})
		if err != nil || got != arch {
			t.Errorf("%s: got %q, %v", arch, got, err)
		}
	}
}

func TestReadRefusesAnotherArchitectureByName(t *testing.T) {
	_, err := hostarch.Read("home-c", host{out: "armhf\n"})
	if err == nil || !strings.Contains(err.Error(), "home-c: the host reports armhf. The toolkit supports amd64 and arm64.") {
		t.Fatalf("got %v", err)
	}
}

func TestReadNamesTheSiteWhenTheProbeFails(t *testing.T) {
	_, err := hostarch.Read("home-a", host{out: "dpkg: not found", err: errors.New("exit status 127")})
	if err == nil || !strings.Contains(err.Error(), "home-a") {
		t.Fatalf("got %v", err)
	}
}

func TestMismatchNamesBothAndTheLineToDelete(t *testing.T) {
	err := hostarch.Mismatch("home-b", "amd64", "arm64")
	want := "home-b: paisans-stack.yaml records arch amd64, and the host reports arm64. If this is new hardware, delete the arch line from home-b and run again."
	if err == nil || err.Error() != want {
		t.Fatalf("got %v", err)
	}
	if hostarch.Mismatch("home-b", "arm64", "arm64") != nil || hostarch.Mismatch("home-b", "", "arm64") != nil {
		t.Fatal("agreeing or unrecorded is not a mismatch")
	}
}

func TestRunsMatchesOnTheArchitectureAlone(t *testing.T) {
	cases := []struct {
		platforms []string
		arch      string
		want      bool
	}{
		{[]string{"amd64", "arm64/v8"}, "arm64", true},
		{[]string{"amd64"}, "arm64", false},
		{[]string{"windows/amd64"}, "amd64", false},
		{[]string{"arm64"}, "arm64", true},
		{nil, "amd64", false},
	}
	for _, c := range cases {
		if got := hostarch.Runs(c.platforms, c.arch); got != c.want {
			t.Errorf("Runs(%v, %s) = %v", c.platforms, c.arch, got)
		}
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/hostarch/`
Expected: FAIL, because the package does not exist.

- [ ] **Step 3: Implement**

```go
// Package hostarch reads a host's architecture and holds the rules the
// toolkit applies to it. amd64 and arm64 are supported because the stack's
// images are published for those two (docs/specs/2026-10-13-arm64-and-mixed-architecture.md).
package hostarch

import (
	"fmt"
	"strings"
)

// Command prints the architecture in Debian's names (amd64, arm64), the
// same names Docker's apt source and image platforms use.
const Command = "dpkg --print-architecture"

// Runner runs a command on a host: apply.Transport, hostprep.Transport.
type Runner interface {
	Run(command string) (string, error)
}

// Supported reports whether the toolkit deploys to arch.
func Supported(arch string) bool { return arch == "amd64" || arch == "arm64" }

// Parse is what Command printed on site, refused when it is not supported.
func Parse(site, out string) (string, error) {
	arch := strings.TrimSpace(out)
	if !Supported(arch) {
		return "", fmt.Errorf("%s: the host reports %s. The toolkit supports amd64 and arm64.", site, arch)
	}
	return arch, nil
}

// Read asks site's host for its architecture.
func Read(site string, t Runner) (string, error) {
	out, err := t.Run(Command)
	if err != nil {
		return "", fmt.Errorf("%s: reading the architecture (%s): %s: %w", site, Command, strings.TrimSpace(out), err)
	}
	return Parse(site, out)
}

// Mismatch refuses a host that reports another architecture than the one
// recorded for its site. A host replaced under the same name by one of
// another architecture would otherwise be prepared, then fail inside
// replication or be handed a restore it cannot read. Deleting the line is the
// operator saying the change is meant.
func Mismatch(site, recorded, reported string) error {
	if recorded == "" || recorded == reported {
		return nil
	}
	return fmt.Errorf("%s: paisans-stack.yaml records arch %s, and the host reports %s. If this is new hardware, delete the arch line from %s and run again.", site, recorded, reported, site)
}

// Runs reports whether an image published for platforms (as
// imagedigest.Build.Platform names them: amd64, arm64/v8, windows/amd64)
// has a Linux build for arch. A variant does not matter: every arm64 host
// runs arm64/v8.
func Runs(platforms []string, arch string) bool {
	for _, p := range platforms {
		if p == arch || strings.HasPrefix(p, arch+"/") {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/hostarch/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/hostarch
git commit -m "feat: hostarch reads a host's architecture and judges it"
```

---

### Task 2: `sites.<name>.arch` in the configuration, and its write-back

**Files:**
- Modify: `internal/config/config.go` (the `Site` struct, at the end of the `Site` struct near line 222; the per-site checks near line 1067)
- Create: `internal/config/arch.go`
- Test: `internal/config/arch_test.go`, `internal/config/config_test.go`

**Interfaces:**
- Consumes: `hostarch.Supported` (Task 1).
- Produces:
  - `Site.Arch string` with the tag `yaml:"arch"`
  - `func SetArch(path string, archs map[string]string) error`

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/config_test.go`:

```go
// The toolkit writes arch itself, so a value it would never write is a
// hand edit gone wrong, refused at load rather than compared with a host.
func TestUnknownArchIsRefused(t *testing.T) {
	body := strings.Replace(minimal, "    address: 10.44.0.1\n", "    address: 10.44.0.1\n    arch: armhf\n", 1)
	_, err := config.Load(write(t, body))
	if err == nil || !strings.Contains(err.Error(), "sites.home-a.arch") {
		t.Fatalf("an unknown arch loaded: %v", err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		body := strings.Replace(minimal, "    address: 10.44.0.1\n", "    address: 10.44.0.1\n    arch: "+arch+"\n", 1)
		cfg, err := config.Load(write(t, body))
		if err != nil {
			t.Fatalf("arch %s was refused: %v", arch, err)
		}
		if cfg.Sites["home-a"].Arch != arch {
			t.Errorf("arch %s read as %q", arch, cfg.Sites["home-a"].Arch)
		}
	}
}
```

Create `internal/config/arch_test.go`:

```go
package config

import (
	"strings"
	"testing"
)

func TestSetArchAddsOneLineAheadOfTheSitesFirstKey(t *testing.T) {
	in := "sites:\n  # the home server\n  home-a:\n    roles: [data, apps]\n    address: 10.44.0.1\n  vm:\n    roles:\n      - gateway\n    address: 10.44.0.2\n"
	out, err := setArch([]byte(in), map[string]string{"home-a": "amd64", "vm": "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	want := "sites:\n  # the home server\n  home-a:\n    arch: amd64\n    roles: [data, apps]\n    address: 10.44.0.1\n  vm:\n    arch: arm64\n    roles:\n      - gateway\n    address: 10.44.0.2\n"
	if string(out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestSetArchWritesInsideAFlowMapping(t *testing.T) {
	in := "sites:\n  home-a: {roles: [data], address: 10.44.0.1}\n"
	out, err := setArch([]byte(in), map[string]string{"home-a": "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "sites:\n  home-a: {arch: arm64, roles: [data], address: 10.44.0.1}\n"; string(out) != want {
		t.Fatalf("got %q", out)
	}
}

func TestSetArchRefusesASiteThatRecordsOne(t *testing.T) {
	in := "sites:\n  home-a:\n    arch: amd64\n    address: 10.44.0.1\n"
	if _, err := setArch([]byte(in), map[string]string{"home-a": "arm64"}); err == nil || !strings.Contains(err.Error(), "already records") {
		t.Fatalf("got %v", err)
	}
}

func TestSetArchRefusesAnUndeclaredSite(t *testing.T) {
	in := "sites:\n  home-a:\n    address: 10.44.0.1\n"
	if _, err := setArch([]byte(in), map[string]string{"home-z": "arm64"}); err == nil || !strings.Contains(err.Error(), "home-z") {
		t.Fatalf("got %v", err)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/config/ -run 'Arch'`
Expected: FAIL to compile (`setArch` is undefined, `Site.Arch` is unknown).

- [ ] **Step 3: Implement**

In `internal/config/config.go`, add the field at the end of `Site`:

```go
	// Arch is the host's architecture, amd64 or arm64, as `dpkg
	// --print-architecture` reported it the first time the toolkit reached
	// the site. The toolkit writes it (SetArch), so validate can check which
	// sites must share one without reaching a host, and a host replaced by one
	// of another architecture is refused rather than joined. Deleting it is
	// how an operator accepts new hardware.
	Arch string `yaml:"arch"`
```

Beside the watchdog check (near line 1067):

```go
		if site.Arch != "" && !hostarch.Supported(site.Arch) {
			add("sites.%s.arch: unknown value %q. The toolkit supports amd64 and arm64, and writes this line itself: delete it, and the next apply records what the host reports.", name, site.Arch)
		}
```

Add the `internal/hostarch` import. `hostarch` imports nothing from `config`, so there is no cycle.

Create `internal/config/arch.go`:

```go
package config

import (
	"bytes"
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// SetArch writes sites.<site>.arch for each site in archs into the
// declaration at path, and leaves every other byte as it was. The line goes
// ahead of the site's first key, indented as that key is, because the first
// key is the one place in a block mapping where a new line is valid whatever
// the keys after it hold. In a flow mapping it goes in front of the first key,
// followed by `, `. It is apply's record of what each host reported, and it
// refuses a site that already records one: a recorded value is compared with
// the host, never overwritten.
func SetArch(path string, archs map[string]string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	out, err := setArch(data, archs)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return replaceFile(path, out)
}

func setArch(data []byte, archs map[string]string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the top level is not a mapping")
	}
	_, sites := lookup(doc.Content[0], "sites")
	lines := lineStarts(data)
	names := make([]string, 0, len(archs))
	for name := range archs {
		names = append(names, name)
	}
	sort.Strings(names)
	var edits []edit
	for _, name := range names {
		_, site := lookup(sites, name)
		if site == nil || site.Kind != yaml.MappingNode || len(site.Content) == 0 {
			return nil, fmt.Errorf("sites.%s is not a mapping this command can add arch to", name)
		}
		if _, recorded := lookup(site, "arch"); recorded != nil {
			return nil, fmt.Errorf("sites.%s already records arch %s", name, recorded.Value)
		}
		first := site.Content[0]
		if site.Style&yaml.FlowStyle != 0 {
			runes := []rune(lines.text(data, first.Line))
			if first.Column-1 > len(runes) {
				return nil, fmt.Errorf("sites.%s could not be found on line %d", name, first.Line)
			}
			at := lines.start(first.Line) + len(string(runes[:first.Column-1]))
			edits = append(edits, edit{at, at, "arch: " + archs[name] + ", "})
			continue
		}
		at := lines.start(first.Line)
		edits = append(edits, edit{at, at, fmt.Sprintf("%*sarch: %s\n", first.Column-1, "", archs[name])})
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := append([]byte(nil), data...)
	for _, e := range edits {
		var b bytes.Buffer
		b.Write(out[:e.start])
		b.WriteString(e.text)
		b.Write(out[e.end:])
		out = b.Bytes()
	}
	return out, nil
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/config/`
Expected: PASS. Then run `go test ./...`. A golden or schema test that lists `Site`'s keys may need `arch` added. Add it where such a test enumerates the keys, and change nothing else.

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat: sites.<name>.arch records a site's architecture"
```

---

### Task 3: `validate` refuses data sites of different architectures

**Files:**
- Create: `internal/validate/arch.go`
- Modify: `internal/validate/validate.go:123-183` (register the check in `Check`)
- Test: `internal/validate/arch_test.go`

**Interfaces:**
- Consumes: `Site.Arch` (Task 2).
- Produces: rule id `data-sites-differ-in-arch`, at key `cluster.sites`. Task 4 looks it up by this id.

- [ ] **Step 1: Write the failing test**

```go
package validate_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/validate"
)

func TestDataSitesMustShareAnArchitecture(t *testing.T) {
	cfg := load(t, "valid")
	data := cfg.Cluster.Sites
	if len(data) < 2 {
		t.Skip("the valid fixture needs two data sites for this test")
	}
	set := func(name, arch string) {
		s := cfg.Sites[name]
		s.Arch = arch
		cfg.Sites[name] = s
	}

	set(data[0], "amd64")
	if validate.Check(cfg).Has("data-sites-differ-in-arch") {
		t.Fatal("one recorded and one unrecorded is not a difference")
	}

	set(data[1], "arm64")
	result := validate.Check(cfg)
	if !result.Has("data-sites-differ-in-arch") {
		t.Fatalf("did not fire: %v", result.Findings)
	}
	for _, f := range result.Findings {
		if f.Rule == "data-sites-differ-in-arch" && (!strings.Contains(f.Message, data[0]+" is amd64") || !strings.Contains(f.Message, data[1]+" is arm64")) {
			t.Errorf("message names neither site with its value: %s", f.Message)
		}
	}

	set(data[1], "amd64")
	if validate.Check(cfg).Has("data-sites-differ-in-arch") {
		t.Fatal("the same architecture fired")
	}
}

// Sites without the data role copy no database between them, so they mix.
func TestOtherSitesMayDifferInArchitecture(t *testing.T) {
	cfg := load(t, "valid")
	gw := cfg.GatewaySites()[0]
	s := cfg.Sites[gw]
	s.Arch = "arm64"
	cfg.Sites[gw] = s
	for _, name := range cfg.Cluster.Sites {
		if name == gw {
			t.Skip("the gateway holds the data role in this fixture")
		}
		d := cfg.Sites[name]
		d.Arch = "amd64"
		cfg.Sites[name] = d
	}
	if validate.Check(cfg).Has("data-sites-differ-in-arch") {
		t.Fatal("a gateway of another architecture was refused")
	}
}
```

`internal/validate/testdata/valid.yaml` declares two data sites and a gateway-and-witness site, so neither skip fires today. The skips guard against a later fixture edit.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/validate/ -run Architecture`
Expected: FAIL with "did not fire".

- [ ] **Step 3: Implement**

`internal/validate/arch.go`:

```go
package validate

import (
	"fmt"
	"sort"
	"strings"
)

// dataSitesShareAnArch refuses data sites that record different
// architectures. Patroni's replication, failover and site add's
// pg_basebackup copy the data directory and its WAL byte for byte, and both
// are written in the host's byte order and alignment, so a replica on
// another architecture cannot read what it receives. Which architecture does
// not matter. A site with no arch recorded yet is skipped: apply records it
// on the first run and checks again.
func (c *checker) dataSitesShareAnArch() {
	byArch := map[string][]string{}
	for _, name := range c.cfg.Cluster.Sites {
		if arch := c.cfg.Sites[name].Arch; arch != "" {
			byArch[arch] = append(byArch[arch], name)
		}
	}
	if len(byArch) < 2 {
		return
	}
	var parts []string
	for arch, names := range byArch {
		sort.Strings(names)
		for _, name := range names {
			parts = append(parts, fmt.Sprintf("%s is %s", name, arch))
		}
	}
	sort.Strings(parts)
	c.refuse("data-sites-differ-in-arch", "cluster.sites",
		"data sites have different architectures",
		"%s. Patroni replicates the data directory byte for byte, so every data site needs the same architecture.",
		strings.Join(parts, " and "))
}
```

Register it in `Check` beside `c.clusterSiteWithoutData()`:

```go
	c.dataSitesShareAnArch()
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/validate/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/validate
git commit -m "feat: validate refuses data sites of different architectures"
```

---

### Task 4: Probing and recording architectures from the command line

**Files:**
- Create: `cmd/paisans/arch.go`
- Test: `cmd/paisans/arch_test.go`

**Interfaces:**
- Consumes: `hostarch.Read`, `hostarch.Mismatch` (Task 1); `config.SetArch`, `Site.Arch` (Task 2); rule `data-sites-differ-in-arch` (Task 3).
- Produces:
  - `func readArchitectures(r ui.Reporter, cfg *config.Config, sites []string, host func(name string) hostarch.Runner) (map[string]string, error)`
  - `func recordArchitectures(r ui.Reporter, path string, cfg *config.Config, read map[string]string, execute bool) (*config.Config, error)`

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/hostarch"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

type archHost string

func (a archHost) Run(command string) (string, error) {
	if command != hostarch.Command {
		return "", errors.New("unexpected " + command)
	}
	return string(a) + "\n", nil
}

const twoDataSites = `version: 1
id: f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01
community:
  name: "Fixture"
  domain: example.org
mesh:
  subnet: 10.44.0.0/24
admins:
  alice:
    name: Alice Example
    email: alice@example.org
    keys: |
      ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice@example.org
sites:
  home-a:
    roles: [data, apps]
    address: 10.44.0.1
    ssh: {host: home-a.local, user: ubuntu, keys: [alice]}
  home-b:
    roles: [data, apps]
    address: 10.44.0.2
    ssh: {host: home-b.local, user: ubuntu, keys: [alice]}
etcd:
  members: [home-a, home-b]
`

func archFixture(t *testing.T, body string) (string, *config.Config) {
	t.Helper()
	path := filepath.Join(t.TempDir(), config.FileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, cfg
}

func hostsAt(archs map[string]string) func(string) hostarch.Runner {
	return func(name string) hostarch.Runner { return archHost(archs[name]) }
}

func TestAnExecutedRunRecordsEveryMissingArchAndRereads(t *testing.T) {
	path, cfg := archFixture(t, twoDataSites)
	r := &ui.Recorder{}
	read, err := readArchitectures(r, cfg, cfg.SiteNames(), hostsAt(map[string]string{"home-a": "arm64", "home-b": "arm64"}))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = recordArchitectures(r, path, cfg, read, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sites["home-a"].Arch != "arm64" || cfg.Sites["home-b"].Arch != "arm64" {
		t.Fatalf("not reread: %+v", cfg.Sites)
	}
	data, _ := os.ReadFile(path)
	if strings.Count(string(data), "arch: arm64") != 2 {
		t.Fatalf("file:\n%s", data)
	}
	if r.Index("note", "home-a: arch arm64 recorded in paisans-stack.yaml") < 0 {
		t.Errorf("no note: %+v", r.Events)
	}
}

func TestADryRunRecordsInMemoryAndWritesNothing(t *testing.T) {
	path, cfg := archFixture(t, twoDataSites)
	before, _ := os.ReadFile(path)
	cfg, err := recordArchitectures(&ui.Recorder{}, path, cfg, map[string]string{"home-a": "amd64", "home-b": "amd64"}, false)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("a dry run wrote the file")
	}
	if cfg.Sites["home-a"].Arch != "amd64" {
		t.Fatal("the plan does not see the probed architecture")
	}
}

func TestARecordedArchTheHostContradictsIsRefused(t *testing.T) {
	_, cfg := archFixture(t, strings.Replace(twoDataSites, "    address: 10.44.0.2\n", "    address: 10.44.0.2\n    arch: amd64\n", 1))
	_, err := readArchitectures(&ui.Recorder{}, cfg, []string{"home-b"}, hostsAt(map[string]string{"home-b": "arm64"}))
	if err == nil || !strings.Contains(err.Error(), "delete the arch line from home-b") {
		t.Fatalf("got %v", err)
	}
}

func TestDataSitesFoundToDifferAreRefusedAfterRecording(t *testing.T) {
	path, cfg := archFixture(t, twoDataSites)
	_, err := recordArchitectures(&ui.Recorder{}, path, cfg, map[string]string{"home-a": "amd64", "home-b": "arm64"}, false)
	if err == nil || !strings.Contains(err.Error(), "home-a is amd64 and home-b is arm64") {
		t.Fatalf("got %v", err)
	}
}

func TestEveryKeyRecordedWritesNothing(t *testing.T) {
	body := strings.Replace(strings.Replace(twoDataSites, "    address: 10.44.0.1\n", "    address: 10.44.0.1\n    arch: amd64\n", 1), "    address: 10.44.0.2\n", "    address: 10.44.0.2\n    arch: amd64\n", 1)
	path, cfg := archFixture(t, body)
	info, _ := os.Stat(path)
	if _, err := recordArchitectures(&ui.Recorder{}, path, cfg, map[string]string{"home-a": "amd64", "home-b": "amd64"}, true); err != nil {
		t.Fatal(err)
	}
	again, _ := os.Stat(path)
	if !again.ModTime().Equal(info.ModTime()) {
		t.Fatal("rewrote a file with nothing to add")
	}
}
```

If `ui.Recorder` has no `Index(kind, text string) int`, use the helper next to `Event` in `internal/ui/recorder.go` ("Index is the position of the first event of kind whose Text contains text"). Read its exact signature before using it. If the fixture fails `config.Load` for a reason unrelated to arch, copy the smallest valid two-data-site fixture from `cmd/paisans/testdata` instead.

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./cmd/paisans/ -run 'Arch|DryRunRecords|DataSitesFound|EveryKeyRecorded'`
Expected: FAIL to compile (`readArchitectures` is undefined).

- [ ] **Step 3: Implement**

`cmd/paisans/arch.go`:

```go
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/hostarch"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

// readArchitectures asks each of sites for its architecture, one after
// another, and refuses a host that contradicts the arch its site records
// before anything on it changes. See hostarch.Mismatch.
func readArchitectures(r ui.Reporter, cfg *config.Config, sites []string, host func(name string) hostarch.Runner) (map[string]string, error) {
	line := r.Step("read each site's architecture")
	read := map[string]string{}
	var shown []string
	for _, name := range sites {
		arch, err := hostarch.Read(name, host(name))
		if err == nil {
			err = hostarch.Mismatch(name, cfg.Sites[name].Arch, arch)
		}
		if err != nil {
			line.Fail(err)
			return nil, err
		}
		read[name] = arch
		shown = append(shown, name+" "+arch)
	}
	line.Done(strings.Join(shown, ", "))
	return read, nil
}

// recordArchitectures puts every architecture in read that its site does not
// record yet into the configuration. With execute it writes them into the
// file at path in one edit, says so for each, and returns the configuration
// read again, as init's write is. A whole apply does this before it publishes
// the file, because each of its steps refuses bytes changed after the
// publish. A dry run writes nothing and sets them in memory, so its plan is
// checked against what the hosts reported.
//
// Either way, validate's data site rule is applied again afterwards: a site
// whose key was missing when validate ran could otherwise carry a mixed
// cluster past it.
func recordArchitectures(r ui.Reporter, path string, cfg *config.Config, read map[string]string, execute bool) (*config.Config, error) {
	missing := map[string]string{}
	for name, arch := range read {
		if cfg.Sites[name].Arch == "" {
			missing[name] = arch
		}
	}
	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for name := range missing {
			names = append(names, name)
		}
		sort.Strings(names)
		if execute {
			if err := config.SetArch(path, missing); err != nil {
				return nil, err
			}
			for _, name := range names {
				r.Note(fmt.Sprintf("%s: arch %s recorded in %s", name, missing[name], config.FileName), "")
			}
			reread, err := config.Load(path)
			if err != nil {
				return nil, err
			}
			cfg = reread
		} else {
			for _, name := range names {
				site := cfg.Sites[name]
				site.Arch = missing[name]
				cfg.Sites[name] = site
				r.Note(fmt.Sprintf("%s: arch %s, recorded in %s by --execute", name, missing[name], config.FileName), "")
			}
		}
	}
	for _, f := range validate.Check(cfg).Findings {
		if f.Rule == "data-sites-differ-in-arch" {
			return nil, errors.New(f.Key + ": " + f.Message)
		}
	}
	return cfg, nil
}
```

`apply.Transport` and `hostprep.Transport` both have `Run(string) (string, error)`, so either satisfies `hostarch.Runner`.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./cmd/paisans/ -run 'Arch|DryRunRecords|DataSitesFound|EveryKeyRecorded'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/paisans/arch.go cmd/paisans/arch_test.go
git commit -m "feat: apply's helpers read each site's architecture and record it"
```

---

### Task 5: Wiring the probe into apply, host prepare and site add

**Files:**
- Modify: `cmd/paisans/converge.go`, in `runConverge` after `settle(cfg)` (near line 776) and before `convergeFounded`/`applyStore`
- Modify: `cmd/paisans/main.go`
  - `runApply`, after `declared` is found (near line 516) and before the secrets are loaded
  - `hostPrepare`, after `transport` is built (near line 1196)
- Modify: `cmd/paisans/site.go`, in `runSiteAdd` after `transports` is built (near line 103)
- Modify: every fake host in the `cmd/paisans` tests that a run now probes (see Review Focus 1)
- Test: `cmd/paisans/arch_test.go` (extend)

**Interfaces:**
- Consumes: `readArchitectures`, `recordArchitectures` (Task 4); `convergeHost` (`converge.go:1841`); `siteTransport` (`main.go:1459`).

- [ ] **Step 1: Write the failing test for the whole apply**

The whole apply is the path whose ordering matters. Test it through `convergeHost`, which the tests already replace (see `sudo_upfront_test.go:62`). Read `sudo_upfront_test.go:40-120` first, then use the same setup it uses to call `runConverge` as a dry run, with hosts answering `dpkg --print-architecture`:

```go
// A whole apply reads every site's architecture before it plans, and its dry
// run says what --execute would record without writing it.
func TestADryRunApplyReadsEverySitesArchitecture(t *testing.T) {
	// Setup copied from the nearest runConverge dry-run test in
	// sudo_upfront_test.go: a fixture deployment with secrets, and
	// convergeHost replaced by fakes. Each fake answers
	// hostarch.Command with "arm64\n".
	// ... (that test's setup, verbatim) ...
	r := &ui.Recorder{}
	_ = runConverge(r, convergeOptions{Config: path, Secrets: secrets, Execute: false, Sudo: false})
	if r.Index("done", "read each site's architecture") < 0 {
		t.Fatalf("no architecture read: %+v", r.Events)
	}
	before, _ := os.ReadFile(path)
	if strings.Contains(string(before), "arch:") {
		t.Fatal("a dry run wrote arch")
	}
}
```

Replace the `// ...` line with the setup of the chosen test, copied in full. The test must compile and run without reaching a network.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./cmd/paisans/ -run TestADryRunApplyReadsEverySitesArchitecture`
Expected: FAIL with "no architecture read".

- [ ] **Step 3: Wire `runConverge`**

Directly after `if err := settle(cfg); err != nil { return err }`:

```go
	// Every site's architecture is read before anything is planned, and a
	// missing one is recorded before the file is gathered and published:
	// each step of the run refuses bytes changed after the publish. See
	// recordArchitectures.
	read, err := readArchitectures(r, cfg, cfg.SiteNames(), func(name string) hostarch.Runner {
		return convergeHost(name, cfg.Sites[name], sudo)
	})
	if err != nil {
		return err
	}
	if cfg, err = recordArchitectures(r, configPath, cfg, read, execute); err != nil {
		return err
	}
```

- [ ] **Step 4: Wire `runApply`, `hostPrepare` and `runSiteAdd`**

In `runApply`, directly after the `declared, ok := cfg.Sites[*site]` block. A step of a whole apply has the run's recorded file, so the step only compares; a standalone `apply --site` records its own site:

```go
	// The site's architecture is read before anything else reaches it. As a
	// step of apply without --site the run recorded it already, and the
	// read only compares; on its own, apply --site records it, and the next
	// apply without --site publishes it. The bytes applied are read again
	// after a write.
	archHost := siteTransport(*site, declared, *destination, *sudo)
	read, err := readArchitectures(r, cfg, []string{*site}, func(string) hostarch.Runner { return archHost })
	if err != nil {
		return err
	}
	if cfg, err = recordArchitectures(r, *configPath, cfg, read, *execute && reviewedByRun == nil); err != nil {
		return err
	}
	if *execute && reviewedByRun == nil {
		if _, paisansBytes, err = config.LoadWithBytes(*configPath); err != nil {
			return err
		}
	}
	declared = cfg.Sites[*site]
```

In `hostPrepare`, directly after `transport` is built and its `RetryLog` set:

```go
	// See runApply: host prepare records its own site's architecture, which
	// the next apply publishes.
	read, err := readArchitectures(r, cfg, []string{o.site}, func(string) hostarch.Runner { return transport })
	if err != nil {
		return err
	}
	if cfg, err = recordArchitectures(r, o.config, cfg, read, o.execute); err != nil {
		return err
	}
```

`hostPrepare` also runs as a step of a whole apply (`convergePrepareAll`, every host at once). There the run has recorded every key already, so `recordArchitectures` finds nothing missing and writes nothing. Concurrent steps therefore never edit the file.

In `runSiteAdd`, directly after the `transports` loop and the declared-site check:

```go
	// The new site's architecture is recorded as host prepare records it;
	// preflight compares it with the data sites'.
	read, err := readArchitectures(r, cfg, []string{site}, func(name string) hostarch.Runner { return transports[name] })
	if err != nil {
		return err
	}
	if cfg, err = recordArchitectures(r, *configPath, cfg, read, *execute); err != nil {
		return err
	}
```

Add the `internal/hostarch` import to each file.

- [ ] **Step 5: Run the whole suite and fix the fakes**

Run: `go test ./cmd/paisans/`
Expected: the new test passes. Existing tests whose fake hosts lack a `dpkg --print-architecture` answer fail with that command in the message. Add an answer of `amd64\n` to each such fake's `Run`, beside its other command matches. Do not make the probe tolerate a failure.

Run: `go build ./... && go vet ./... && go test ./... && gofmt -l .`
Expected: PASS, and `gofmt` prints nothing.

- [ ] **Step 6: Commit**

```bash
git add cmd/paisans
git commit -m "feat: apply, host prepare and site add record each site's architecture"
```

---

### Task 6: Preflight compares the architecture, and apply checks image platforms

**Files:**
- Modify: `internal/preflight/preflight.go:323-401` (`platform`, `readPlatform`, the `osMatch` message)
- Modify: `internal/imagedigest/version.go` (add `Registry.Platforms`)
- Modify: `internal/imagedigest/cache.go` (add `Cache.Platforms`)
- Modify: `internal/apply/images.go` (the `Platformer` interface)
- Modify: `internal/apply/apply.go` (an `Arch` option, and the check after `pinImages` near line 696)
- Modify: `cmd/paisans/main.go:1004` (`planSiteApply` passes `apply.Arch(cfg.Sites[site].Arch)`)
- Test: `internal/preflight/preflight_test.go`, `internal/imagedigest/version_test.go`, `internal/apply/images_test.go`

**Interfaces:**
- Consumes: `hostarch.Read`, `hostarch.Runs` (Task 1).
- Produces:
  - `func (r Registry) Platforms(ref string) ([]string, error)`
  - `func (c *Cache) Platforms(ref string) ([]string, error)`
  - `apply.Platformer interface { Platforms(ref string) ([]string, error) }`
  - `func apply.Arch(arch string) Option`

- [ ] **Step 1: Write the failing tests**

In `internal/preflight/preflight_test.go`:
- Add `{match: "dpkg --print-architecture", out: "amd64\n"},` to `healthy`'s rules.
- Add a case to the refusal table beside "glibc differs":

```go
		{name: "architecture differs", site: "home-b", check: "platform", want: "arm64, but home-a runs amd64",
			setup: func(h map[string]*fakeHost) {
				h["home-b"].override(rule{match: "dpkg --print-architecture", out: "arm64\n"})
			}},
```

In `internal/imagedigest/version_test.go`:

```go
func TestPlatformsNamesEveryLinuxBuild(t *testing.T) {
	srv, _ := published(t, build{arch: "amd64"}, build{arch: "arm64"})
	got, err := imagedigest.Registry{Client: srv.Client()}.Platforms(mbinRef(srv))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "amd64,arm64" {
		t.Errorf("got %v", got)
	}
}
```

In `internal/apply/images_test.go`, make the fake a `Platformer`, then add the tests:

```go
// platforms makes the fake an apply.Platformer: each reference whose name
// (before @) is a key is published for those platforms, and any other for
// amd64 and arm64.
func (r *registry) Platforms(ref string) ([]string, error) {
	if r.platformErr != nil {
		return nil, r.platformErr
	}
	name, _, _ := strings.Cut(ref, "@")
	if p, ok := r.platforms[name]; ok {
		return p, nil
	}
	return []string{"amd64", "arm64"}, nil
}
```

Add these fields to `registry`: `platforms map[string][]string` and `platformErr error`.

```go
func TestAnImageWithoutTheSitesPlatformIsRefusedInThePlan(t *testing.T) {
	host := newHost()
	r := &registry{digest: buildA, platforms: map[string][]string{mbinTag: {"amd64"}}}
	_, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.ResolveImages(r), apply.Arch("arm64"))
	if err == nil || !strings.Contains(err.Error(), "home-a (arm64): "+mbinTag) || !strings.Contains(err.Error(), "linux/amd64 only") {
		t.Fatalf("got %v", err)
	}
	for _, c := range host.commands {
		if strings.Contains(c, "docker pull") {
			t.Fatalf("pulled before refusing: %s", c)
		}
	}
}

func TestAVariantCountsAsItsArchitecture(t *testing.T) {
	r := &registry{digest: buildA, platforms: map[string][]string{mbinTag: {"amd64", "arm64/v8"}}}
	if _, err := apply.Build("home-a", plan(t), acmeModule(t), newHost(), apply.ResolveImages(r), apply.Arch("arm64")); err != nil {
		t.Fatal(err)
	}
}

func TestAnUnreadablePlatformListIsANoteNotARefusal(t *testing.T) {
	r := &registry{digest: buildA, platformErr: errors.New("the registry answered 500")}
	p, err := apply.Build("home-a", plan(t), acmeModule(t), newHost(), apply.ResolveImages(r), apply.Arch("arm64"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range p.Notes {
		found = found || strings.Contains(n.Hint, "platforms")
	}
	if !found {
		t.Fatalf("no note: %+v", p.Notes)
	}
}

func TestWithoutAnArchNoPlatformIsAsked(t *testing.T) {
	r := &registry{digest: buildA, platformErr: errors.New("must not be asked")}
	p, err := apply.Build("home-a", plan(t), acmeModule(t), newHost(), apply.ResolveImages(r))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range p.Notes {
		if strings.Contains(n.Hint, "platforms") {
			t.Fatal("asked without an arch")
		}
	}
}
```

The fixture's `home-a` runs the `talk` stack, which holds `mbinTag`; `images_test.go:367` already applies it. If `host.commands` is named differently on `fakeHost`, read `apply_test.go` for its field.

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/preflight/ ./internal/imagedigest/ ./internal/apply/`
Expected: FAIL to compile (`Platforms` and `apply.Arch` are undefined), and the preflight case does not fire.

- [ ] **Step 3: Implement preflight**

In `internal/preflight/preflight.go`:

```go
// platform is what osMatch compares.
type platform struct {
	id, version, glibc, arch string
}

func (p platform) String() string {
	return fmt.Sprintf("%s, %s %s, glibc %s", p.arch, p.id, p.version, p.glibc)
}
```

At the end of `readPlatform`, before the return:

```go
	arch, err := hostarch.Read("this site", t)
	if err != nil {
		return platform{}, err
	}
	return platform{id: host.ID, version: host.VersionID, glibc: glibcRelease(fields[len(fields)-1]), arch: arch}, nil
```

In `osMatch`, give the architecture its own sentence, ahead of the generic mismatch case:

```go
		case err != nil:
			r.refuse(r.newSite, "platform", "could not read %s's: %v", name, err)
		case mine.arch != theirs.arch:
			r.refuse(r.newSite, "platform", "%s, but %s runs %s. A replica must match its primary's architecture, or Postgres cannot read the data directory it copies", mine.arch, name, theirs.arch)
		case mine != theirs:
```

Update the `osMatch` doc comment so it names the architecture too: a replica's data directory is the primary's bytes, in its byte order. Check the existing "glibc differs" and "os differs" cases still pass, since `String()` now leads with the architecture.

- [ ] **Step 4: Implement `Platforms`**

In `internal/imagedigest/version.go`, after `Builds`:

```go
// Platforms names the platforms the build ref names was published for, as
// Build.Platform does (amd64, arm64/v8), sorted. Apply refuses an image with
// none for a site's architecture before anything is pulled.
func (r Registry) Platforms(ref string) ([]string, error) {
	builds, err := r.Builds(ref)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(builds))
	for _, b := range builds {
		out = append(out, b.Platform)
	}
	sort.Strings(out)
	return out, nil
}
```

In `internal/imagedigest/cache.go`, add the field `platforms map[string][]string` to `Cache`, initialise it in `Cached`, and add:

```go
// Platformer names the platforms of a build, as Registry.Platforms does.
type Platformer interface {
	Platforms(ref string) ([]string, error)
}

// Platforms is the wrapped resolver's Platforms for ref, asked once per run.
// A build named by its digest never changes, so the answer is kept like a
// digest is. A failure is returned and not kept.
func (c *Cache) Platforms(ref string) ([]string, error) {
	p, ok := c.r.(Platformer)
	if !ok {
		return nil, nil
	}
	c.mu.Lock()
	if got, ok := c.platforms[ref]; ok {
		c.mu.Unlock()
		return got, nil
	}
	c.mu.Unlock()
	got, err := p.Platforms(ref)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.platforms[ref] = got
	c.mu.Unlock()
	return got, nil
}
```

A nil list with no error means "not known", and apply skips the check for it (see Step 5).

- [ ] **Step 5: Implement the apply check**

In `internal/apply/images.go`, after `Versioner`:

```go
// Platformer is a Resolver that can also name the platforms a build was
// published for. imagedigest.Registry and imagedigest.Cache are both.
type Platformer interface {
	Platforms(ref string) ([]string, error)
}

// platformCheck refuses a pin whose build has no Linux platform for arch,
// before anything is pulled, so a dry run names it. A list that cannot be
// read is a note rather than a refusal: the pull on the host still says so,
// and a registry's bad moment should not stop an apply that would succeed.
func platformCheck(site, arch string, image ImagePin, r Resolver) (*Note, error) {
	p, ok := r.(Platformer)
	if !ok || arch == "" {
		return nil, nil
	}
	platforms, err := p.Platforms(image.Reference)
	if err != nil {
		return &Note{
			Hint: fmt.Sprintf("could not read the platforms of %s", ui.Name(image.Image)),
			Text: fmt.Sprintf("Reading which platforms %s was published for failed (%v), so the plan cannot say whether it runs on %s. The pull on %s says so if it does not.", ui.Name(image.Image), err, arch, site),
		}, nil
	}
	if platforms == nil || hostarch.Runs(platforms, arch) {
		return nil, nil
	}
	// Build.Platform names the OS only when it is not linux (windows/amd64),
	// so linux is put back where the first segment is an architecture.
	named := make([]string, len(platforms))
	for i, pl := range platforms {
		first, _, _ := strings.Cut(pl, "/")
		named[i] = pl
		if goArchs[first] {
			named[i] = "linux/" + pl
		}
	}
	return nil, fmt.Errorf("%s (%s): %s is published for %s only.", site, arch, ui.Name(image.Image), strings.Join(named, ", "))
}
```

Beside it, add:

```go
// goArchs are the architecture names an image index uses (Go's GOARCH),
// for telling an architecture from an OS in Build.Platform.
var goArchs = map[string]bool{"amd64": true, "arm64": true, "arm": true, "386": true, "ppc64le": true, "s390x": true, "riscv64": true, "mips64le": true}
```

Add this test beside the others in `images_test.go`:

```go
func TestANonLinuxPlatformIsNamedAsItIs(t *testing.T) {
	r := &registry{digest: buildA, platforms: map[string][]string{mbinTag: {"windows/amd64"}}}
	_, err := apply.Build("home-a", plan(t), acmeModule(t), newHost(), apply.ResolveImages(r), apply.Arch("amd64"))
	if err == nil || !strings.Contains(err.Error(), "published for windows/amd64 only") {
		t.Fatalf("got %v", err)
	}
}
```

Add the `internal/hostarch` import to `images.go`.

In `internal/apply/apply.go`, add the field to `options`:

```go
	// arch is the site's architecture; see Arch.
	arch string
```

and the option:

```go
// Arch has Build refuse an image with no build for the site's architecture
// (amd64, arm64), before anything is pulled. Empty, or not given, checks
// nothing, which is a site whose architecture is not known yet.
func Arch(arch string) Option {
	return func(o *options) { o.arch = arch }
}
```

After `pinImages` returns, in the `if !leftAlone` branch near line 702:

```go
			if !leftAlone {
				for _, pin := range pins {
					note, err := platformCheck(site, o.arch, pin, r)
					if err != nil {
						finish(done, err)
						return nil, err
					}
					if note != nil {
						notes = append(notes, *note)
					}
				}
				out.Pins = append(out.Pins, pins...)
				out.Notes = append(out.Notes, notes...)
			}
```

In `cmd/paisans/main.go:1004`, inside `planSiteApply`:

```go
	options = append([]apply.Option{apply.DatabaseApps(apply.ClusterDatabaseApps(cfg)...), apply.ResolveImages(imageDigests), apply.Arch(cfg.Sites[site].Arch)}, options...)
```

A clustered app is planned once per apps site, and each plan carries its own site's architecture. A clustered app missing one architecture is therefore refused on each site it cannot run on, named by that site.

- [ ] **Step 6: Run the tests and watch them pass**

Run: `go test ./internal/preflight/ ./internal/imagedigest/ ./internal/apply/ ./cmd/paisans/`
Then run: `go build ./... && go vet ./... && go test ./... && gofmt -l .`
Expected: PASS, and `gofmt` prints nothing.

- [ ] **Step 7: Commit**

```bash
git add internal/preflight internal/imagedigest internal/apply cmd/paisans/main.go
git commit -m "feat: preflight compares architecture, and apply refuses an image without the site's platform"
```

---

### Task 7: Documentation

**Files:**
- Modify: `README.md`
  - `### Moving a pinned app` (line ~390), and the four-things list after it
  - `## Adding a site, and the constraint that shapes it` (line ~1191)
  - `## Configuration` (line ~1355): a new `### Architectures` subsection
- Modify: `docs/specs/2026-10-11-backups.md` (*Pinned Postgres*, near line 242; *Restore*, near line 462)
- Modify: `docs/development.md`: list the new `hostarch` package wherever it lists packages, if it does

No code and no tests. Verify by reading each changed section in place.

- [ ] **Step 1: README, `### Architectures`**

Add under `## Configuration`, after `### The config declares intent; etcd holds truth`:

```markdown
### Architectures

A site may be amd64 or arm64. The toolkit reads each host's architecture
(`dpkg --print-architecture`) the first time apply, `host prepare` or `site
add` reaches it, and records it as `sites.<name>.arch`. An operator never
writes the line. It exists so that `validate` can check the rule below
without reaching a host, and so that a host replaced under the same name by
one of another architecture is refused rather than joined. To accept new
hardware, delete the line, and the next run records what the host reports.

**Every data site has the same architecture, whichever it is.** Postgres's
data directory and its WAL are written in the host's byte order and
alignment, and Patroni's replication, a failover and `site add`'s
`pg_basebackup` copy them byte for byte. Everything else may differ: apps
sites, an app on SQLite or pinned to its own site, Pocket ID, Garage, etcd,
the gateway, the monitor and the witness. Each of them talks over the
network and copies no database between machines.

**Every image must have a build for its site.** Apply reads which platforms
each image was published for when it plans, and refuses one with no build
for the site's architecture before anything is pulled. A clustered app needs
a build for every apps site's architecture.
```

- [ ] **Step 2: README, *Moving a pinned app***

Add this as a paragraph directly before the code block:

```markdown
**Both hosts need the same architecture** (`sites.<name>.arch`). The tar
carries the app's Postgres data directory, which only a host of the same
architecture can read. Between amd64 and arm64, move the database with
`pg_dump` on the old site and a restore on the new one, and move the files as
below.
```

- [ ] **Step 3: README, *Adding a site***

Add one sentence where that section lists what `site add` checks: a new data site must have the data sites' architecture, which preflight compares along with the OS and glibc release.

- [ ] **Step 4: The backups spec amendment**

Under *Pinned Postgres: `postgres-walg`* and in *Restore*, add:

```markdown
**A base backup records the architecture it was taken on**
(`sites.<name>.arch` of the site that took it), beside WAL-G's own metadata.
A restore compares it with the target site's and refuses a difference
before fetching anything, naming the backup, its architecture, the site and
the site's. A physical backup restores only onto the architecture it was
taken on; to cross architectures, restore onto a matching site and copy the
data with `pg_dump`. A backup with no architecture recorded restores with a
warning that its architecture is unknown. Restoring one clustered app through
a scratch instance needs only the scratch instance to match: the dump that
follows crosses architectures freely. See
docs/specs/2026-10-13-arm64-and-mixed-architecture.md.
```

- [ ] **Step 5: Commit**

```bash
git add README.md docs/specs/2026-10-11-backups.md docs/development.md
git commit -m "docs: architectures, the pinned app move, and backups' architecture record"
```

---

## Not in this plan

The WAL-G architecture record and restore check (spec phase 5) are code for `postgres-walg` and the backup runner, neither of which exists yet. Task 7 writes the requirement into the backups spec, and the backups implementation builds it.
