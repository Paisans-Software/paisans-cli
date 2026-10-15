# Infrastructure image overrides Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an operator override the infrastructure stack's images (`infra.images`, `infra.postgres.<major>.spilo`) without a core catalog release, see which builds would move with `paisans images`, and be warned when an override lags the reference it replaced.

**Architecture:** A new `infra` key in `internal/config` is checked for shape in config and for meaning in `internal/validate`. `internal/render` takes a declared reference before the foundation's for each infrastructure service and the operators heartbeat, records on every `render.ComposeImage` where its reference came from (`Origin`) and what it replaced (`Replaces`), and keeps declared images out of the foundation's review exemption. `internal/apply` copies those onto `ImagePin`, resolves `Replaces`, and adds a warning note when the override is behind. `cmd/paisans/images.go` plans every site the way a dry run does and prints the pins as a table.

**Tech Stack:** Go, `gopkg.in/yaml.v3`, `text/tabwriter`, the repo's own `internal/ui`, `internal/imagedigest`, `internal/blueprints`.

**Spec:** `docs/specs/2026-10-15-infra-image-overrides.md`

## Global Constraints

- `infra.images` keys: exactly `etcd`, `garage`, `haproxy`, `heartbeat`. Caddy is not one; `acme.image` stays its only key.
- `infra.postgres` keys: Postgres majors `16`, `17`, `18`; each takes only `spilo`.
- `cluster.postgres_version` left out means `"18"`.
- Supported tag prefixes, from `supportedTags` in `internal/blueprints/foundation.go`: etcd `v3.`, haproxy `3.`, garage `v1.`, Zalando Spilo `3.` or `4.`. Never copy this table; call the exported check.
- A Spilo override is held to Spilo's prefixes only when its repository starts `ghcr.io/zalando/spilo-`; any other gets a warning.
- An overridden image is never marked a foundation service (`ComposeImage.FoundationServices`), so apply's grant review always asks for it. Do not widen any review exemption.
- `paisans images` writes nothing to any host and never asks for sudo. It exits 0 normally, 3 with `--check` when a build would move or an override lags, and 1 on error.
- Prose rules for every comment, doc and message: describe current state only (no "no longer", "now", "used to"); never name rejected alternatives outside `docs/decisions.md`; say "blueprint", never "kind"; the project is "Paisans".
- Never use `git stash`. Commit on branch `docs/infra-image-overrides` in `.worktrees/infra-image-overrides`.
- Every commit ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

- An override that names exactly the foundation's own reference must still be reviewed (not marked a foundation service). Pinned by Task 3's `TestAnOverrideIsNeverTheFoundations`.
- A deployment moving `cluster.postgres_version` from 17 to 18 with only a `"17"` Spilo entry must render the foundation's 18 Spilo, not the 17 override. Pinned by Task 3's `TestASpiloOverrideAppliesOnlyToItsMajor`.
- A digest-only override (`repo@sha256:...`) for etcd, HAProxy, Garage or Zalando Spilo has no tag whose version can be checked; it is refused, naming the `repo:tag@sha256:...` form. Pinned by Task 2's `TestADigestOnlyInfraOverrideIsRefused`.
- An override in another repository (a fork) must not be called "behind" on version numbers that do not compare. Pinned by Task 4's `TestAnOverrideInAnotherRepositoryIsNotJudged`.
- One unreachable site must not hide the others in `paisans images`. Pinned by Task 5's `TestImagesKeepsGoingPastASiteItCannotReach`.

---

### Task 1: The `infra` configuration key

**Files:**
- Modify: `internal/config/config.go` (the `Config` struct at ~line 78; `structural` at ~line 1090, beside the `apps.%s.images` loop at ~line 1198)
- Modify: `internal/render/site.go:1061-1066` (`postgresVersion`)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces:
  - `type Infra struct { Images map[string]string; Postgres map[string]map[string]string }`, field `Config.Infra` (`yaml:"infra"`).
  - `var InfraImageKeys = []string{"etcd", "garage", "haproxy", "heartbeat"}`
  - `var InfraPostgresKeys = []string{"spilo"}`
  - `const DefaultPostgresVersion = "18"`
  - `func (c *Config) PostgresMajor() string`
  - `func (c *Config) SpiloImage() string` (the declared Spilo for `PostgresMajor()`, or `""`)

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/config_test.go`:

```go
// infra overrides the infrastructure stack's images. Its keys are the
// foundation's names for the images it replaces, so a key the toolkit does
// not run is refused rather than silently ignored.
func TestInfraImagesLoad(t *testing.T) {
	doc := validConfig + "infra:\n  images:\n    etcd: \"gcr.io/etcd-development/etcd:v3.5.21\"\n  postgres:\n    \"17\":\n      spilo: \"example.org/pg-ha:17.4-1\"\n"
	cfg, err := config.Load(write(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Infra.Images["etcd"]; got != "gcr.io/etcd-development/etcd:v3.5.21" {
		t.Errorf("infra.images.etcd is %q", got)
	}
	if got := cfg.Infra.Postgres["17"]["spilo"]; got != "example.org/pg-ha:17.4-1" {
		t.Errorf("infra.postgres.17.spilo is %q", got)
	}
}

func TestInfraImagesAreChecked(t *testing.T) {
	const payload = "ghcr.io/x/etcd:v3.5.1     privileged: true"
	cases := []struct{ body, want string }{
		{"infra:\n  images:\n    caddy: \"ghcr.io/x/caddy:2.1.0\"\n", "infra.images.caddy"},
		{"infra:\n  images:\n    etcd: \"\"\n", "infra.images.etcd: empty"},
		{"infra:\n  images:\n    etcd: \"" + payload + "\"\n", "infra.images.etcd"},
		{"infra:\n  images:\n    etcd: \"ghcr.io/x/etcd:v3 #\"\n", "infra.images.etcd"},
		{"infra:\n  postgres:\n    \"17\":\n      app: \"postgres:17.1\"\n", "infra.postgres.17.app"},
		{"infra:\n  postgres:\n    \"17\":\n      spilo: \"\"\n", "infra.postgres.17.spilo: empty"},
	}
	for _, c := range cases {
		msg := loadErr(t, validConfig+c.body)
		if !strings.Contains(msg, c.want) {
			t.Errorf("%q: loaded, or refused without naming %s: %q", c.body, c.want, msg)
		}
	}
}

// The major a deployment runs is cluster.postgres_version, 18 when unset,
// and the Spilo it declares is the entry for that major alone.
func TestSpiloImageIsTheRunningMajors(t *testing.T) {
	cfg := &config.Config{Infra: config.Infra{Postgres: map[string]map[string]string{"17": {"spilo": "a:17"}, "18": {"spilo": "a:18"}}}}
	if got := cfg.PostgresMajor(); got != config.DefaultPostgresVersion {
		t.Errorf("an unset postgres_version is %q", got)
	}
	if got := cfg.SpiloImage(); got != "a:18" {
		t.Errorf("SpiloImage is %q, want the 18 entry", got)
	}
	cfg.Cluster.PostgresVersion = "17"
	if got := cfg.SpiloImage(); got != "a:17" {
		t.Errorf("SpiloImage is %q, want the 17 entry", got)
	}
	cfg.Cluster.PostgresVersion = "16"
	if got := cfg.SpiloImage(); got != "" {
		t.Errorf("SpiloImage is %q for a major with no entry", got)
	}
}
```

Before running, check that `validConfig` ends with a newline at the top level (`tail -c 50` of its definition); if it ends inside a block, append the `infra:` text through a `strings.Replace` on a known top-level line instead, as `TestAnImageReferenceIsOne` does.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/config/ -run 'TestInfraImages|TestSpiloImageIsTheRunningMajors'`
Expected: FAIL to compile: `cfg.Infra undefined`.

- [ ] **Step 3: Implement**

In `internal/config/config.go`, add the field to `Config` after `ACME`:

```go
	// Infra overrides the images the infrastructure stack runs, each of
	// which is otherwise the core catalog foundation's.
	Infra Infra `yaml:"infra"`
```

Add the types beside `ACME`:

```go
// Infra overrides the infrastructure stack's images. Each key is optional,
// and an unset one takes the core catalog foundation's image. A value is one
// full reference, exact or a track, for the reason App.Images gives.
type Infra struct {
	// Images is keyed by InfraImageKeys, the names foundation.yaml gives
	// the images they replace. The heartbeat's replaces the infrastructure
	// stack's and every Pocket ID stack's operators heartbeat both.
	Images map[string]string `yaml:"images"`
	// Postgres is keyed by Postgres major, then by InfraPostgresKeys. An
	// entry applies only while cluster.postgres_version is its major, so a
	// build of one major never follows the cluster onto another.
	Postgres map[string]map[string]string `yaml:"postgres"`
}

// InfraImageKeys are the keys infra.images takes.
var InfraImageKeys = []string{"etcd", "garage", "haproxy", "heartbeat"}

// InfraPostgresKeys are the keys infra.postgres.<major> takes.
var InfraPostgresKeys = []string{"spilo"}

// DefaultPostgresVersion is the major a deployment runs when
// cluster.postgres_version is left out.
const DefaultPostgresVersion = "18"

// PostgresMajor is the Postgres major the deployment runs.
func (c *Config) PostgresMajor() string {
	if c.Cluster.PostgresVersion == "" {
		return DefaultPostgresVersion
	}
	return c.Cluster.PostgresVersion
}

// SpiloImage is the Spilo image infra.postgres declares for the major the
// deployment runs, empty when it declares none.
func (c *Config) SpiloImage() string {
	return c.Infra.Postgres[c.PostgresMajor()]["spilo"]
}
```

In `structural`, after the `for _, name := range c.AppNames()` loop, add:

```go
	problems = append(problems, c.Infra.problems()...)
```

If `structural` collects through `add` only and not a `problems` slice at that point, call `c.Infra.check(add)` instead and write the method to take `add func(string, ...any)`. Read the function's first twenty lines to see which; the method body is the same:

```go
// problems are infra's keys and references that cannot be rendered: a key
// the toolkit does not run, or a value that is not one image reference.
func (i Infra) problems() []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	known := map[string]bool{}
	for _, k := range InfraImageKeys {
		known[k] = true
	}
	for _, key := range sortedKeys(i.Images) {
		if !known[key] {
			add("infra.images.%s: not an image the infrastructure stack runs. infra.images takes %s; the gateway's Caddy is acme.image.", keyPath(key), strings.Join(InfraImageKeys, ", "))
			continue
		}
		imageProblem(add, "infra.images."+keyPath(key), i.Images[key])
	}
	for _, major := range sortedKeys(i.Postgres) {
		for _, key := range sortedKeys(i.Postgres[major]) {
			path := fmt.Sprintf("infra.postgres.%s.%s", keyPath(major), keyPath(key))
			if !slices.Contains(InfraPostgresKeys, key) {
				add("%s: infra.postgres.<major> takes %s. A pinned app's own database is apps.<name>.images.", path, strings.Join(InfraPostgresKeys, ", "))
				continue
			}
			imageProblem(add, path, i.Postgres[major][key])
		}
	}
	return out
}

// imageProblem adds what is wrong with ref as the image at path: a control
// character, nothing, or something no image reference holds.
func imageProblem(add func(string, ...any), path, ref string) {
	switch r, ok := ControlRune(ref); {
	case ok:
		add("%s %s", path, controlProblem(r))
	case strings.TrimSpace(ref) == "":
		add("%s: empty. Give a full image reference, or remove the key to take the core catalog's.", path)
	case !imageRef.MatchString(ref):
		add("%s: %s", path, imageRefProblem(ref))
	}
}
```

Import `slices` if `config.go` does not already. Then change the existing `apps.%s.images` loop to call `imageProblem(add, fmt.Sprintf("apps.%s.images.%s", name, keyPath(service)), app.Images[service])`, keeping its empty message's wording ("remove the key to take the default this blueprint ships") by passing it through: if the shared message differs, keep the apps loop as it is and use `imageProblem` for infra only. Do not change the apps messages; `TestAnImageReferenceIsOne` pins them.

In `internal/render/site.go`, replace the body of `postgresVersion`:

```go
func (p *planner) postgresVersion() string {
	return p.cfg.PostgresMajor()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ ./internal/render/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/render/site.go
git commit -m "feat(config): infra overrides the infrastructure stack's images

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: What validate checks about `infra`

**Files:**
- Modify: `internal/blueprints/foundation.go` (export `checkImage`, add `PostgresMajors`)
- Modify: `internal/validate/validate.go` (`floatingImages` at ~line 978; register new rules in `Check` at ~line 147)
- Test: `internal/validate/validate_test.go`

**Interfaces:**
- Consumes: `config.Infra`, `config.InfraImageKeys`, `(*config.Config).PostgresMajor()` from Task 1.
- Produces:
  - `func blueprints.CheckFoundationImage(name, ref string) error` (renamed `checkImage`; every caller in `foundation.go` updated)
  - `func blueprints.PostgresMajors() []string` (sorted keys of `postgresMajors`)
  - Validate rules: `floating-image-tag` (extended to infra keys), `infra-image-unsupported-version` (Refuse), `infra-postgres-unknown-major` (Refuse), `infra-postgres-unused-major` (Warn), `infra-spilo-unchecked` (Warn), `infra-heartbeat-covers-pocket-id` (Warn).

- [ ] **Step 1: Write the failing tests**

Append to `internal/validate/validate_test.go`:

```go
// warnedFor reports whether rule warned about exactly the given key.
func warnedFor(result validate.Result, rule, key string) bool {
	for _, f := range result.Warnings() {
		if f.Rule == rule && f.Key == key {
			return true
		}
	}
	return false
}

func withInfra(t *testing.T, infra config.Infra) *config.Config {
	t.Helper()
	cfg := load(t, "valid")
	cfg.Infra = infra
	return cfg
}

// An override of an image the toolkit's code speaks to stays inside the
// versions that code is written against, as the foundation's own track must.
func TestAnInfraOverrideStaysOnASupportedVersion(t *testing.T) {
	for key, ref := range map[string]string{
		"etcd":    "gcr.io/etcd-development/etcd:v3.6.*",
		"haproxy": "docker.io/library/haproxy:3.2.1-alpine",
		"garage":  "docker.io/dxflrs/garage:v1.1.0",
	} {
		cfg := withInfra(t, config.Infra{Images: map[string]string{key: ref}})
		if validate.Check(cfg).Has("infra-image-unsupported-version") {
			t.Errorf("%s: %s was refused", key, ref)
		}
	}
	for key, ref := range map[string]string{
		"etcd":    "gcr.io/etcd-development/etcd:v4.0.0",
		"haproxy": "docker.io/library/haproxy:*.0.1-alpine",
		"garage":  "docker.io/dxflrs/garage:v2.0.0",
	} {
		cfg := withInfra(t, config.Infra{Images: map[string]string{key: ref}})
		if !refusedFor(validate.Check(cfg), "infra-image-unsupported-version", "infra.images."+key) {
			t.Errorf("%s: %s was not refused", key, ref)
		}
	}
}

func TestADigestOnlyInfraOverrideIsRefused(t *testing.T) {
	cfg := withInfra(t, config.Infra{Images: map[string]string{"etcd": "gcr.io/etcd-development/etcd@sha256:" + strings.Repeat("a", 64)}})
	if !refusedFor(validate.Check(cfg), "infra-image-unsupported-version", "infra.images.etcd") {
		t.Error("a digest with no tag was accepted, and its version cannot be checked")
	}
	cfg = withInfra(t, config.Infra{Images: map[string]string{"etcd": "gcr.io/etcd-development/etcd:v3.5.21@sha256:" + strings.Repeat("a", 64)}})
	if validate.Check(cfg).Has("infra-image-unsupported-version") {
		t.Error("a tag with its digest beside it was refused")
	}
}

func TestAnInfraOverrideIsNotLatest(t *testing.T) {
	for _, ref := range []string{"gcr.io/etcd-development/etcd:latest", "gcr.io/etcd-development/etcd"} {
		cfg := withInfra(t, config.Infra{Images: map[string]string{"etcd": ref}})
		if !refusedFor(validate.Check(cfg), "floating-image-tag", "infra.images.etcd") {
			t.Errorf("%s was not refused", ref)
		}
	}
}

func TestSpiloOverridesAreCheckedByMajor(t *testing.T) {
	spilo := func(major, ref string) config.Infra {
		return config.Infra{Postgres: map[string]map[string]string{major: {"spilo": ref}}}
	}
	// valid.yaml runs Postgres 18.
	if !refusedFor(validate.Check(withInfra(t, spilo("15", "ghcr.io/zalando/spilo-15:3.2-p1"))), "infra-postgres-unknown-major", "infra.postgres.15") {
		t.Error("a major the toolkit does not run was accepted")
	}
	if !warnedFor(validate.Check(withInfra(t, spilo("17", "ghcr.io/zalando/spilo-17:4.0-p3"))), "infra-postgres-unused-major", "infra.postgres.17") {
		t.Error("an entry for a major the deployment does not run was not warned about")
	}
	if !refusedFor(validate.Check(withInfra(t, spilo("18", "ghcr.io/zalando/spilo-18:5.0-p1"))), "infra-image-unsupported-version", "infra.postgres.18.spilo") {
		t.Error("a Zalando Spilo outside the supported lines was accepted")
	}
	result := validate.Check(withInfra(t, spilo("18", "example.org/pg-ha:18.0-1")))
	if !warnedFor(result, "infra-spilo-unchecked", "infra.postgres.18.spilo") {
		t.Error("another provider's Spilo was not warned about")
	}
	if result.Has("infra-image-unsupported-version") {
		t.Error("another provider's tag was held to Spilo's versions")
	}
}

func TestAHeartbeatOverrideSaysItCoversPocketID(t *testing.T) {
	cfg := withInfra(t, config.Infra{Images: map[string]string{"heartbeat": "docker.io/curlimages/curl:8.11.1"}})
	if !warnedFor(validate.Check(cfg), "infra-heartbeat-covers-pocket-id", "infra.images.heartbeat") {
		t.Error("the heartbeat override did not say it replaces the operators heartbeat too")
	}
}
```

If `valid.yaml` does not set `postgres_version: "18"` (it does at line 64 today), adjust the "unused major" case to a major other than the one it sets.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/validate/ -run 'Infra|Spilo|Heartbeat|DigestOnly'`
Expected: FAIL: the rules never fire.

- [ ] **Step 3: Implement**

In `internal/blueprints/foundation.go`, rename `checkImage` to `CheckFoundationImage`, update its two callers in `ParseFoundation`, and widen its doc comment's first line to: `CheckFoundationImage checks one image the toolkit runs in place of the foundation's named name: a track whose pattern compiles, or a reference pinned to one build, and inside the tags the toolkit supports for it.` Add:

```go
// PostgresMajors are the Postgres majors the toolkit runs, sorted.
func PostgresMajors() []string { return sortedKeys(postgresMajors) }
```

In `internal/validate/validate.go`, extend `floatingImages` after its apps loop:

```go
	for _, key := range sortedKeys(c.cfg.Infra.Images) {
		c.floatingImage("infra.images."+key, c.cfg.Infra.Images[key])
	}
	for _, major := range sortedKeys(c.cfg.Infra.Postgres) {
		for _, key := range sortedKeys(c.cfg.Infra.Postgres[major]) {
			c.floatingImage(fmt.Sprintf("infra.postgres.%s.%s", major, key), c.cfg.Infra.Postgres[major][key])
		}
	}
```

and move the existing refusal into a helper both loops call:

```go
func (c *checker) floatingImage(key, ref string) {
	why := blueprints.ParseReference(ref).Floating()
	if why == "" {
		return
	}
	c.refuse("floating-image-tag", key,
		"image tag is floating",
		"is %q and %s. Two runs of `apply` would then deploy different builds from the same inputs, so the configuration and the secrets no longer reconstruct the stack. Name a version tag, or a digest, which is stronger.",
		ref, why)
}
```

Add a new rule method and register it in `Check` right after `c.floatingImages()`:

```go
// zalandoSpilo is the repository prefix of the Spilo the toolkit is written
// against, whose versions it can check.
const zalandoSpilo = "ghcr.io/zalando/spilo-"

// infraImages checks what infra overrides against what the toolkit's own
// code speaks to: the versions of etcd, HAProxy, Garage and Spilo it is
// written against, and the Postgres majors it runs.
func (c *checker) infraImages() {
	for _, key := range sortedKeys(c.cfg.Infra.Images) {
		c.supportedVersion("infra.images."+key, key, c.cfg.Infra.Images[key])
	}
	if c.cfg.Infra.Images["heartbeat"] != "" {
		c.warn("infra-heartbeat-covers-pocket-id", "infra.images.heartbeat",
			"the heartbeat override covers Pocket ID's too",
			"replaces the infrastructure stack's heartbeat and the operators heartbeat in every Pocket ID stack, which run the same image.")
	}
	majors := map[string]bool{}
	for _, m := range blueprints.PostgresMajors() {
		majors[m] = true
	}
	running := c.cfg.PostgresMajor()
	for _, major := range sortedKeys(c.cfg.Infra.Postgres) {
		key := "infra.postgres." + major
		switch {
		case !majors[major]:
			c.refuse("infra-postgres-unknown-major", key,
				"not a Postgres major the toolkit runs",
				"is Postgres %s, and the toolkit runs %s.", major, strings.Join(blueprints.PostgresMajors(), ", "))
			continue
		case major != running:
			c.warn("infra-postgres-unused-major", key,
				"applies to no Postgres the deployment runs",
				"is for Postgres %s, and cluster.postgres_version is %s, so nothing runs it. Remove it, or keep it only while a move to %s is planned.", major, running, major)
		}
		spilo := c.cfg.Infra.Postgres[major]["spilo"]
		if spilo == "" {
			continue
		}
		if strings.HasPrefix(spilo, zalandoSpilo) {
			c.supportedVersion(key+".spilo", "spilo", spilo)
			continue
		}
		c.warn("infra-spilo-unchecked", key+".spilo",
			"the toolkit cannot check this Spilo",
			"is %q, outside %s*. The toolkit is written against Spilo's and Patroni's environment and data path, and cannot check that this image follows them. apply's review asks for it.", spilo, zalandoSpilo)
	}
}

// supportedVersion refuses ref as the image of the foundation's name when
// its tag can leave the versions the toolkit's code is written against. A
// reference with no tag cannot be checked, and is refused naming the form
// that can.
func (c *checker) supportedVersion(key, name, ref string) {
	if blueprints.ParseReference(ref).Floating() != "" {
		return // floating-image-tag already refuses it
	}
	err := blueprints.CheckFoundationImage(name, ref)
	if err == nil {
		return
	}
	if r := blueprints.ParseReference(ref); r.Digest != "" && blueprints.ParseReference(r.Name).Tag == "" {
		err = fmt.Errorf("names a digest and no tag, so its version cannot be checked. Write the tag beside the digest (Eg: %s:<tag>@%s)", r.Name, r.Digest)
	}
	c.refuse("infra-image-unsupported-version", key,
		"outside the versions the toolkit is written against",
		"%v.", err)
}
```

`CheckFoundationImage` returns nil for names with no supported prefixes (heartbeat), so heartbeat needs no special case. Confirm `sortedKeys` in `internal/validate` is generic over the value type; if it takes only `map[string]string`, use `slices.Sorted(maps.Keys(...))` for `Infra.Postgres`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/blueprints/ ./internal/validate/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/blueprints/foundation.go internal/validate/validate.go internal/validate/validate_test.go
git commit -m "feat(validate): infra overrides stay on the versions the toolkit speaks to

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Render takes the override, says where each image came from, and keeps it out of the exemption

**Files:**
- Modify: `internal/render/site.go:107-135` (`infraImages`, new `infraOrigins`)
- Modify: `internal/render/images.go` (`ComposeImage`, `Setting`, `listImages` ~line 75-195, `infraImageOrigins` ~line 199, `injectedImages` ~line 225)
- Modify: `internal/render/plan.go:330-366` (`appImages`, `appImageOrigins`)
- Modify: `internal/render/appview.go:470` (`HeartbeatImage`)
- Modify: `internal/apply/images.go:553-580` (`unresolved`'s foundation advice)
- Test: `internal/render/infraoverride_test.go` (create)

**Interfaces:**
- Consumes: `config.Infra`, `(*config.Config).SpiloImage()`, `(*config.Config).PostgresMajor()` from Task 1.
- Produces:
  - `render.ComposeImage.Origin string`: the configuration key that declared the image (Eg: `infra.images.etcd`, `infra.postgres.18.spilo`, `apps.talk.images.app`, `acme.image`), or `render.OriginFoundation` / `render.OriginBlueprint`.
  - `render.ComposeImage.Replaces string`: the reference the render takes without that key; empty unless declared.
  - `const render.OriginFoundation = "foundation"`, `const render.OriginBlueprint = "blueprint"`.
  - `ComposeImage.Setting()` returns `infra.images.<key>` / `infra.postgres.<major>.spilo` for infrastructure services and the operators heartbeat.

- [ ] **Step 1: Write the failing tests**

Create `internal/render/infraoverride_test.go`:

```go
package render_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// infraImage is the ComposeImage of the infrastructure stack's compose
// service on site, failing the test when there is none.
func infraImage(t *testing.T, plan *render.Plan, site, service string) render.ComposeImage {
	t.Helper()
	for _, image := range plan.Images[site] {
		if image.Stack == "infra" && slices.Contains(image.Services, service) {
			return image
		}
	}
	t.Fatalf("%s's infrastructure stack runs no %s: %+v", site, service, plan.Images[site])
	return render.ComposeImage{}
}

func TestAnInfraOverrideWinsOverTheFoundation(t *testing.T) {
	cfg := fixture(t)
	const etcd = "gcr.io/etcd-development/etcd:v3.5.99"
	cfg.Infra.Images = map[string]string{"etcd": etcd}
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	foundation, _ := blueprints.FoundationImage(blueprints.FoundationEtcd)
	image := infraImage(t, plan, "home-a", "etcd")
	if image.Image != etcd || image.Origin != "infra.images.etcd" || image.Replaces != foundation {
		t.Errorf("etcd is %+v, want %s from infra.images.etcd replacing %s", image, etcd, foundation)
	}
	if got := image.Setting(); got != "infra.images.etcd" {
		t.Errorf("Setting is %q", got)
	}
	haproxy := infraImage(t, plan, "home-a", "haproxy")
	if haproxy.Origin != render.OriginFoundation || haproxy.Replaces != "" {
		t.Errorf("an image nothing overrides is %+v", haproxy)
	}
}

// An override is the operator's choice, which nobody reviewed, so it is
// never marked the foundation's: not even when it names the foundation's own
// reference.
func TestAnOverrideIsNeverTheFoundations(t *testing.T) {
	cfg := fixture(t)
	same, _ := blueprints.FoundationImage(blueprints.FoundationEtcd)
	cfg.Infra.Images = map[string]string{"etcd": same}
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	if image := infraImage(t, plan, "home-a", "etcd"); slices.Contains(image.FoundationServices, "etcd") {
		t.Errorf("an overridden etcd is marked the foundation's: %+v", image)
	}
}

// The fixture runs Postgres 18.
func TestASpiloOverrideAppliesOnlyToItsMajor(t *testing.T) {
	cfg := fixture(t)
	cfg.Infra.Postgres = map[string]map[string]string{"17": {"spilo": "example.org/pg-ha:17.4-1"}}
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := blueprints.SpiloImage("18")
	if image := infraImage(t, plan, "home-a", "patroni"); image.Image != want || image.Origin != render.OriginFoundation {
		t.Errorf("a 17 override reached Postgres 18: %+v", image)
	}
	cfg.Infra.Postgres = map[string]map[string]string{"18": {"spilo": "example.org/pg-ha:18.0-1"}}
	if plan, err = render.Build(cfg, fixtureSecrets(t)); err != nil {
		t.Fatal(err)
	}
	if image := infraImage(t, plan, "home-a", "patroni"); image.Image != "example.org/pg-ha:18.0-1" || image.Origin != "infra.postgres.18.spilo" {
		t.Errorf("the 18 override was not taken: %+v", image)
	}
}

// One heartbeat image, one key: the override reaches the operators
// heartbeat in every Pocket ID stack too.
func TestTheHeartbeatOverrideReachesPocketID(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	const heartbeat = "docker.io/curlimages/curl:8.99.0"
	cfg.Infra.Images = map[string]string{"heartbeat": heartbeat}
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, f := range plan.Files {
		if strings.HasSuffix(f.Path, "/auth/compose.yaml") {
			seen = true
			if !strings.Contains(f.Content, "image: \""+heartbeat+"\"\n") {
				t.Errorf("%s does not run the override:\n%s", f.Path, f.Content)
			}
		}
	}
	if !seen {
		t.Fatal("the fixture renders no Pocket ID stack")
	}
	for site, images := range plan.Images {
		for _, image := range images {
			if image.Image == heartbeat && len(image.FoundationServices) > 0 {
				t.Errorf("%s: the overridden heartbeat is marked the foundation's: %+v", site, image)
			}
			if image.Image == heartbeat && image.Setting() != "infra.images.heartbeat" {
				t.Errorf("%s: the heartbeat's Setting is %q", site, image.Setting())
			}
		}
	}
}

func TestAnAppOverrideSaysWhatItReplaces(t *testing.T) {
	cfg := fixture(t)
	app := cfg.Apps["talk"]
	app.Images = map[string]string{"app": "ghcr.io/example-org/mbin:1.0.0-fork"}
	cfg.Apps["talk"] = app
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, images := range plan.Images {
		for _, image := range images {
			if image.Stack == "talk" && image.Image == "ghcr.io/example-org/mbin:1.0.0-fork" {
				if image.Origin != "apps.talk.images.app" || image.Replaces == "" {
					t.Errorf("the talk override is %+v", image)
				}
				return
			}
		}
	}
	t.Fatal("no site runs the talk override")
}
```

Check before running: that `home-a` runs `etcd`, `haproxy` and `patroni` in `internal/render/testdata/deployment.yaml` (it has roles `[data, apps]`), and that the Pocket ID app is `auth`; adjust the names to the fixture if not.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/render/ -run 'Override|Spilo|HeartbeatOverride'`
Expected: FAIL to compile: `image.Origin undefined`.

- [ ] **Step 3: Implement**

In `internal/render/images.go`, add to `ComposeImage` after `FoundationServices`:

```go
	// Origin is where Image came from: the configuration key that declares
	// it (Eg: infra.images.etcd, apps.talk.images.app, acme.image), or
	// OriginFoundation or OriginBlueprint when the configuration declares
	// none.
	Origin string
	// Replaces is the reference the render takes when the configuration
	// declares none, empty when it declares none or there is none.
	Replaces string

	// setting is Setting's answer for an image the configuration key of
	// which the render knows directly: the infrastructure stack's and the
	// operators heartbeat.
	setting string
```

Add beside the type:

```go
// The origins of an image no configuration key declares.
const (
	// OriginFoundation is the core catalog's foundation.yaml.
	OriginFoundation = "foundation"
	// OriginBlueprint is the image a blueprint names itself.
	OriginBlueprint = "blueprint"
)

// imageOrigin is where the render took one service's image.
type imageOrigin struct {
	image string
	// declared reports that a configuration key named image.
	declared bool
	// replaces is the reference the render takes without that key.
	replaces string
	// foundation reports that the render took image from the core
	// catalog's foundation for one of the toolkit's own services, which is
	// what marks it for the review exemption (FoundationServices).
	foundation bool
	// blueprint reports that a blueprint named image itself.
	blueprint bool
}

// origin is o's ComposeImage.Origin, setting being the key that declares it.
func (o imageOrigin) origin(setting string) string {
	switch {
	case o.declared:
		return setting
	case o.blueprint:
		return OriginBlueprint
	}
	return OriginFoundation
}

// declaredOr is the reference the configuration declares, when it does, and
// otherwise the foundation's image of that name.
func declaredOr(declared, name string) (imageOrigin, error) {
	def, err := blueprints.FoundationImage(name)
	if err != nil {
		return imageOrigin{}, err
	}
	if declared != "" {
		return imageOrigin{image: declared, declared: true, replaces: def}, nil
	}
	return imageOrigin{image: def, foundation: true}, nil
}
```

Change `Setting()` to begin:

```go
	if c.setting != "" {
		return c.setting
	}
```

and update its doc comment's list to include `infra.images.<key>` and `infra.postgres.<major>.spilo`.

In `internal/render/site.go`, replace `infraImages` with:

```go
// infraOrigins is the image of each service the infrastructure template
// writes but the gateway's Caddy, keyed by compose service name: what infra
// declares, or else the core catalog foundation's. Spilo's is the
// deployment's Postgres major's, since Spilo publishes one repository per
// major, and infra.postgres declares it per major for the same reason.
func (p *planner) infraOrigins() (map[string]imageOrigin, error) {
	out := map[string]imageOrigin{}
	for service, name := range map[string]string{
		"etcd":      blueprints.FoundationEtcd,
		"haproxy":   blueprints.FoundationHAProxy,
		"garage":    blueprints.FoundationGarage,
		"heartbeat": blueprints.FoundationHeartbeat,
	} {
		o, err := declaredOr(p.cfg.Infra.Images[name], name)
		if err != nil {
			return nil, err
		}
		out[service] = o
	}
	spilo, err := blueprints.SpiloImage(p.postgresVersion())
	if err != nil {
		return nil, fmt.Errorf("cluster.postgres_version: %w", err)
	}
	out["patroni"] = imageOrigin{image: spilo, foundation: true}
	if declared := p.cfg.SpiloImage(); declared != "" {
		out["patroni"] = imageOrigin{image: declared, declared: true, replaces: spilo}
	}
	return out, nil
}

// infraSetting is the configuration key that decides an infrastructure
// service's image.
func (p *planner) infraSetting(service string) string {
	switch service {
	case "patroni":
		return "infra.postgres." + p.postgresVersion() + ".spilo"
	case "caddy":
		return "acme.image"
	}
	return "infra.images." + service
}

func (p *planner) infraImages() (infraImages, error) {
	o, err := p.infraOrigins()
	if err != nil {
		return infraImages{}, err
	}
	return infraImages{
		etcd: o["etcd"].image, spilo: o["patroni"].image, haproxy: o["haproxy"].image,
		garage: o["garage"].image, heartbeat: o["heartbeat"].image,
	}, nil
}
```

Keep the `infraImages` struct's doc comment, reworded to "each the configuration's (infra) or else the core catalog foundation's".

In `internal/render/images.go`, replace `infraImageOrigins` with one that adds Caddy to `infraOrigins`:

```go
// infraImageOrigins is infraOrigins and the gateway's Caddy: acme.image when
// it declares one, the foundation's otherwise.
func (p *planner) infraImageOrigins() (map[string]imageOrigin, error) {
	out, err := p.infraOrigins()
	if err != nil {
		return nil, err
	}
	caddy, err := blueprints.FoundationImage(blueprints.FoundationCaddy)
	if err != nil {
		return nil, err
	}
	if ref := p.cfg.ACME.Image; ref != "" {
		o := imageOrigin{image: ref, declared: true}
		if acme.Published(p.cfg.ACME.Provider) {
			o.replaces = caddy
		}
		out["caddy"] = o
		return out, nil
	}
	out["caddy"] = imageOrigin{image: caddy, foundation: true}
	return out, nil
}
```

Import `internal/acme` in `images.go` if it is not already.

Replace `injectedImages` with:

```go
// injectedImages are the services an app's stack runs that no service of
// its blueprint names, with their images: services the toolkit adds to a
// blueprint built into it. The pocket-id blueprint's operators heartbeat is
// the one, appValues.HeartbeatImage, and infra.images.heartbeat overrides it.
func (p *planner) injectedImages(app config.App) (map[string]imageOrigin, error) {
	if app.Blueprint != config.BlueprintPocketID {
		return nil, nil
	}
	o, err := p.heartbeatOrigin()
	if err != nil {
		return nil, err
	}
	return map[string]imageOrigin{operatorsHeartbeatService: o}, nil
}

// heartbeatOrigin is the heartbeat image: infra.images.heartbeat, or the
// foundation's.
func (p *planner) heartbeatOrigin() (imageOrigin, error) {
	return declaredOr(p.cfg.Infra.Images[blueprints.FoundationHeartbeat], blueprints.FoundationHeartbeat)
}
```

In `internal/render/appview.go:470`, replace the `FoundationImage(FoundationHeartbeat)` call:

```go
		heartbeat, err := p.heartbeatOrigin()
		if err != nil {
			return appValues{}, err
		}
		v.HeartbeatImage = heartbeat.image
```

In `internal/render/plan.go`, replace `appImages` and `appImageOrigins`:

```go
func (p *planner) appImages(app config.App) (map[string]string, error) {
	origins, err := p.appImageOrigins(app)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(origins))
	for service, o := range origins {
		out[service] = o.image
	}
	return out, nil
}

// appImageOrigins is where appImages took each service's image. A
// declaration wins and replaces the default. The foundation mark is for the
// image the core catalog's foundation chose for the toolkit: one a reserved
// or core blueprint names with foundation:, and the database's, which the
// toolkit chooses for every blueprint. A foundation: in a blueprint from any
// other catalog is that catalog's choice, which the team that reviews
// foundation.yaml did not review, so it is not marked.
func (p *planner) appImageOrigins(app config.App) (map[string]imageOrigin, error) {
	out := map[string]imageOrigin{}
	toolkits := blueprints.ToolkitBlueprint(app.Blueprint)
	for _, service := range blueprints.Services(app.Blueprint) {
		image, err := blueprints.ServiceImage(service)
		def := imageOrigin{image: image, foundation: service.Foundation != "" && toolkits, blueprint: service.Foundation == ""}
		if err == nil && image == "" && service.Name == blueprints.PostgresService {
			pg, pgErr := blueprints.PostgresImage(p.postgresVersion())
			if pgErr != nil {
				return nil, pgErr
			}
			def = imageOrigin{image: pg, foundation: true}
		}
		if declared := app.Images[service.Name]; declared != "" {
			out[service.Name] = imageOrigin{image: declared, declared: true, replaces: def.image}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("the image of %s's %s: %w", app.Blueprint, service.Name, err)
		}
		if def.image != "" {
			out[service.Name] = def
		}
	}
	return out, nil
}
```

Keep the existing long doc comment on `appImages` about the database's Debian release; it still holds.

In `listImages`, rewrite the infra branch and the app branch to use origins, and fill the new fields. The infra branch becomes:

```go
		origins := map[string]imageOrigin{}
		settings := map[string]string{}
		if stack == config.InfraStack {
			// The infrastructure template is the toolkit's own, so each of
			// its services running the foundation's image was chosen by the
			// render. Only its own services count, so nothing else that
			// reaches this file is marked.
			infra, err := p.infraImageOrigins()
			if err != nil {
				return nil, err
			}
			for name, service := range services {
				o, ok := infra[name]
				if !ok || !infraServices[name] || o.image != strings.TrimSpace(service.Image) {
					continue
				}
				origins[name], settings[name] = o, p.infraSetting(name)
			}
		} else {
			declared, isApp := p.cfg.Apps[stack]
			if !isApp {
				continue
			}
			app = stack
			appOrigins, err := p.appImageOrigins(declared)
			if err != nil {
				return nil, err
			}
			for service, o := range appOrigins {
				if lines[o.image] > 0 {
					named[o.image] = append(named[o.image], service)
				}
				origins[service], settings[service] = o, fmt.Sprintf("apps.%s.images.%s", stack, service)
			}
			injected, err := p.injectedImages(declared)
			if err != nil {
				return nil, err
			}
			for service, o := range injected {
				origins[service], settings[service] = o, "infra.images."+blueprints.FoundationHeartbeat
			}
		}
		for service, o := range origins {
			if o.foundation {
				chosen[service] = o.image
			} else {
				fromElsewhere[o.image] = true
			}
		}
```

Delete the old `chosen, fromElsewhere` assignments inside both branches; keep their declaration (`chosen, fromElsewhere := map[string]string{}, map[string]bool{}`) and the comment above it. `chosen` is keyed by the compose service for infra and by the blueprint service for apps, as before; the `marked` loop below compares `chosen[name] == image` by compose service name, which matches for infra, the injected heartbeat, and blueprint services whose compose name equals their configuration name, exactly as the old code did.

Then, where each `ComposeImage` is appended, fill the new fields from the first service (sorted) of `names` that has an origin:

```go
			var origin imageOrigin
			var setting string
			for _, name := range names {
				if o, ok := origins[name]; ok {
					origin, setting = o, settings[name]
					break
				}
			}
			ci := ComposeImage{Path: file.Path, Stack: stack, App: app, Image: image, Services: names, Lines: n, FoundationServices: marked[image], blueprint: p.cfg.Apps[app].Blueprint}
			if setting != "" {
				ci.Origin, ci.Replaces = origin.origin(setting), origin.replaces
				if stack == config.InfraStack || !blueprints.Has(ci.blueprint, names[0]) {
					ci.setting = setting
				}
			}
			out[site] = append(out[site], ci)
```

(`names` is sorted just above, so "first" is deterministic. The `setting` field is set only where `Setting()` has no answer of its own: infra services and the operators heartbeat.)

In `internal/apply/images.go` `unresolved`, the foundation case now has a setting too. Change its condition and message:

```go
	if image.Origin == render.OriginFoundation || image.Setting() == "" {
		// The core catalog's foundation image: the catalog's release fixes
		// it, and the setting, when there is one, holds a build until then.
		explain := fmt.Sprintf("Run apply again once this workstation reaches %s.", host)
		if errors.Is(failed, imagedigest.ErrNotFound) || errors.Is(failed, imagedigest.ErrDenied) {
			explain = "It is the core catalog's own image (foundation.yaml), and its track names nothing this registry publishes anonymously. A release of the core catalog fixes it; pinning core to an older release in `catalogs` works until then."
			if image.Setting() != "" {
				explain += fmt.Sprintf(" So does setting %s to a published tag.", setting)
			}
		}
```

(keep the rest of that block unchanged).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/render/ ./internal/apply/ ./internal/grant/`
Expected: PASS, including the existing `TestOnlyTheInfraTemplatesServicesAreMarked`, `TestInfraServicesAreTheTemplates`, golden render tests and the operators heartbeat tests. A golden file changing means the render changed for a configuration with no override: stop and find why; do not regenerate goldens.

- [ ] **Step 5: Commit**

```bash
git add internal/render internal/apply/images.go
git commit -m "feat(render): infra overrides win over the foundation and are always reviewed

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Apply says when an override lags what it replaced

**Files:**
- Modify: `internal/imagedigest/track.go` (add `OlderTag`)
- Modify: `internal/apply/images.go` (`ImagePin`, `pinImage`, `pinImages`, new `checkLag`, new `BuildLabel`)
- Test: `internal/imagedigest/track_test.go`, `internal/apply/images_test.go`

**Interfaces:**
- Consumes: `render.ComposeImage.Origin`, `.Replaces` from Task 3.
- Produces:
  - `func imagedigest.OlderTag(a, b string) bool`
  - `apply.ImagePin` fields `Origin string`, `Replaces string`, `Replaced string`, `Behind bool`
  - `func apply.BuildLabel(ref string) string` (Eg: `v3.5.21 (sha256:1a2b3c4d5e6f)`)

- [ ] **Step 1: Write the failing tests**

Append to `internal/imagedigest/track_test.go` (create it with `package imagedigest_test` and the import if it does not exist):

```go
func TestOlderTag(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v3.5.21", "v3.5.23", true},
		{"v3.5.23", "v3.5.21", false},
		{"v3.5.21", "v3.5.21", false},
		{"3.0.11-alpine", "3.2.0-alpine", true},
		{"4.0-p3", "4.0-p5", true},
		{"17.4-1", "17.10-1", true},
		{"v3.5", "v3.5.1", false},
		{"latest", "v1.0.0", false},
		{"", "v1.0.0", false},
		{"1.99999999999999999999", "2.0", true},
	}
	for _, c := range cases {
		if got := imagedigest.OlderTag(c.a, c.b); got != c.want {
			t.Errorf("OlderTag(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
```

Append to `internal/apply/images_test.go`:

```go
// An override behind the track it replaced is a warning, naming the key
// and the newer release; it never stops the plan.
func TestAnOverrideBehindItsTrackIsAWarning(t *testing.T) {
	const old = "gcr.io/etcd-development/etcd:v3.4.30"
	rendered := planWith(t, func(c *config.Config) { c.Infra.Images = map[string]string{"etcd": old} })
	p, err := apply.Build("home-a", rendered, acmeModule(t), newHost(), apply.ResolveImages(&registry{digest: buildA}))
	if err != nil {
		t.Fatal(err)
	}
	var pin *apply.ImagePin
	for i := range p.Pins {
		if p.Pins[i].Origin == "infra.images.etcd" {
			pin = &p.Pins[i]
		}
	}
	if pin == nil {
		t.Fatalf("no pin from infra.images.etcd: %+v", p.Pins)
	}
	// The fake registry puts a track on its pattern with each * as 0.
	if !pin.Behind || pin.Replaced != "gcr.io/etcd-development/etcd:v3.5.0" {
		t.Errorf("the pin is %+v, want behind v3.5.0", pin)
	}
	var warned bool
	for _, n := range p.Notes {
		if n.Hint != "" && strings.Contains(n.Hint, "infra.images.etcd") && strings.Contains(n.Text, "v3.5.0") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no warning names the key and the newer release: %+v", p.Notes)
	}
}

func TestAnOverrideAheadOfItsTrackIsNotBehind(t *testing.T) {
	rendered := planWith(t, func(c *config.Config) {
		c.Infra.Images = map[string]string{"etcd": "gcr.io/etcd-development/etcd:v3.5.21"}
	})
	p, err := apply.Build("home-a", rendered, acmeModule(t), newHost(), apply.ResolveImages(&registry{digest: buildA}))
	if err != nil {
		t.Fatal(err)
	}
	for _, pin := range p.Pins {
		if pin.Origin == "infra.images.etcd" && pin.Behind {
			t.Errorf("v3.5.21 is called behind v3.5.0: %+v", pin)
		}
	}
}

// Two repositories' version schemes need not compare, so a fork is shown
// beside what it replaced and never judged.
func TestAnOverrideInAnotherRepositoryIsNotJudged(t *testing.T) {
	rendered := planWith(t, func(c *config.Config) {
		c.Infra.Images = map[string]string{"etcd": "ghcr.io/example-org/etcd:v1.0.0"}
	})
	p, err := apply.Build("home-a", rendered, acmeModule(t), newHost(), apply.ResolveImages(&registry{digest: buildA}))
	if err != nil {
		t.Fatal(err)
	}
	for _, pin := range p.Pins {
		if pin.Origin != "infra.images.etcd" {
			continue
		}
		if pin.Behind || pin.Replaced == "" {
			t.Errorf("a fork was judged, or what it replaced not shown: %+v", pin)
		}
		return
	}
	t.Fatal("no pin from infra.images.etcd")
}

func TestBuildLabel(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1a", 32)
	cases := map[string]string{
		"gcr.io/etcd-development/etcd:v3.5.21@" + digest: "v3.5.21 (sha256:1a1a1a1a1a1a)",
		"gcr.io/etcd-development/etcd@" + digest:         "sha256:1a1a1a1a1a1a",
		"gcr.io/etcd-development/etcd:v3.5.21":           "v3.5.21",
	}
	for ref, want := range cases {
		if got := apply.BuildLabel(ref); got != want {
			t.Errorf("BuildLabel(%q) = %q, want %q", ref, got, want)
		}
	}
}
```

Check before running that `internal/apply/images_test.go` imports `config`; add `"github.com/paisans-software/paisans-cli/internal/config"` if not, and that `home-a` runs etcd in the fixture.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/imagedigest/ -run TestOlderTag; go test ./internal/apply/ -run 'Override|BuildLabel'`
Expected: FAIL to compile: `imagedigest.OlderTag undefined`, `pin.Origin undefined`.

- [ ] **Step 3: Implement**

In `internal/imagedigest/track.go`:

```go
// digitRun is one run of digits in a tag.
var digitRun = regexp.MustCompile(`[0-9]+`)

// OlderTag reports whether tag a is an older release than tag b: their runs
// of digits, read left to right as numbers, first differ lower in a. Tags
// whose numbers agree as far as both go are not older, and neither is a tag
// holding no number. It compares the versions of two builds of one
// repository; two repositories' tags need not compare.
func OlderTag(a, b string) bool {
	x, y := digitRun.FindAllString(a, -1), digitRun.FindAllString(b, -1)
	for i := 0; i < len(x) && i < len(y); i++ {
		if c := compareNumbers(x[i], y[i]); c != 0 {
			return c < 0
		}
	}
	return false
}

// compareNumbers compares two runs of digits as numbers of any length.
func compareNumbers(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}
```

In `internal/apply/images.go`, add to `ImagePin` after `Source`:

```go
	// Origin and Replaces are render.ComposeImage's: the configuration key
	// that declares the image, or where it came from when none does, and
	// the reference it replaced.
	Origin   string
	Replaces string
	// Replaced is the newest tag Replaces names now, as a reference, when
	// the resolver could say. Behind reports that it is a newer release of
	// the same repository than the build the pin runs.
	Replaced string
	Behind   bool
```

In `pinImage`, set them where the pin is made:

```go
	pin := &ImagePin{Stack: image.Stack, App: image.App, Services: image.Services, Image: want, Origin: image.Origin, Replaces: image.Replaces}
```

In `pinImages`, after `notes = append(notes, imageNotes...)` and the `pin == nil` check:

```go
		if pin.Replaces != "" && r != nil {
			notes = append(notes, checkLag(pin, r)...)
		}
```

Add:

```go
// checkLag resolves the reference an override replaced, and marks the pin
// Behind, with a warning, when that reference is a newer release of the same
// repository. It only advises: a registry that cannot say leaves the pin
// unmarked, and nothing is refused.
func checkLag(pin *ImagePin, r Resolver) []Note {
	replaced := pin.Replaces
	if imagedigest.IsTrack(replaced) {
		t, ok := r.(Tracker)
		if !ok {
			return nil
		}
		got, err := t.Track(replaced)
		if err != nil {
			return nil
		}
		replaced = got
	}
	pin.Replaced = replaced
	ours, theirs := blueprints.ParseReference(nameOf(pin.Reference)), blueprints.ParseReference(replaced)
	if ours.Repository() != theirs.Repository() || !imagedigest.OlderTag(ours.Tag, theirs.Tag) {
		return nil
	}
	pin.Behind = true
	return []Note{{
		Hint: fmt.Sprintf("%s is behind the image it replaces", pin.Origin),
		Text: fmt.Sprintf("%s names %s, and without it the %s stack would run %s, a newer release. Update %s, or remove it to follow %s again.", ui.Name(pin.Origin), ui.Name(nameOf(pin.Reference)), ui.Name(pin.Stack), ui.Name(replaced), ui.Name(pin.Origin), ui.Name(pin.Replaces)),
	}}
}

// BuildLabel names one build for a table: its tag and short digest, either
// alone when the reference names only one.
func BuildLabel(ref string) string {
	tag := blueprints.ParseReference(nameOf(ref)).Tag
	digest := digestOf(ref)
	if short, ok := strings.CutPrefix(digest, "sha256:"); ok && len(short) > 12 {
		digest = "sha256:" + short[:12]
	}
	switch {
	case digest == "":
		return tag
	case tag == "":
		return digest
	}
	return tag + " (" + digest + ")"
}
```

Note: the Text holds no site name, so `reportNotes`' `seen` map reports it once across every site.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/imagedigest/ ./internal/apply/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/imagedigest internal/apply
git commit -m "feat(apply): warn when an override is behind the image it replaced

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: `paisans images`

**Files:**
- Create: `cmd/paisans/images.go`
- Modify: `cmd/paisans/main.go` (dispatch switch ~line 58-136; `reportError` ~line 174)
- Modify: `cmd/paisans/help.go` (`helpCommands`, `helpSections` "Check")
- Test: `cmd/paisans/images_test.go` (create)

**Interfaces:**
- Consumes: `planSiteApply` (`main.go:1067`), `cacheDigests` (`main.go:1010`), `planEverySite` (`main.go:899`), `siteTransport` (`main.go:1625`), `reportNotes` (`plan.go`), `reportFindings`/`refused` (as `applyCommand` uses them), `apply.SkipSweepRead()`, `apply.BuildLabel`, `ImagePin.Origin/Replaced/Behind` from Task 4, `render.Build`, `render.ComposeImage.Origin` from Task 3.
- Produces: `func runImages(args []string, stdout io.Writer) error`, `func imageRows(r ui.Reporter, cfg *config.Config, secrets *config.Secrets) ([]imageRow, bool, error)`, `var imagesTransport`, `var errImagesMove`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/paisans/images_test.go`:

```go
package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// trackRegistry names one build for every tag and puts a track on its
// pattern with each * as 0.
type trackRegistry string

func (r trackRegistry) Digest(string) (string, error) { return string(r), nil }
func (r trackRegistry) Track(ref string) (string, error) {
	return strings.ReplaceAll(ref, "*", "0"), nil
}

// downHost is a site that does not answer.
type downHost struct{ emptyHost }

func (downHost) Run(string) (string, error) { return "", errors.New("ssh: connect to host: no route to host") }
func (downHost) ReadFile(string) (string, bool, error) {
	return "", false, errors.New("ssh: connect to host: no route to host")
}

func imagesFixture(t *testing.T, edit func(*config.Config)) (*config.Config, *config.Secrets) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(freshSite), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(cfg)
	}
	secrets := &config.Secrets{Version: 1}
	if _, err := secretsgen.Fill(cfg, secrets); err != nil {
		t.Fatal(err)
	}
	return cfg, secrets
}

func withImagesTransport(t *testing.T, f func(string, config.Site) apply.Transport) {
	t.Helper()
	saved, savedDigests := imagesTransport, imageDigests
	imagesTransport, imageDigests = f, trackRegistry(testCaddyDigest)
	t.Cleanup(func() { imagesTransport, imageDigests = saved, savedDigests })
}

func TestImagesListsEveryImageAndWhereItCameFrom(t *testing.T) {
	withImagesTransport(t, func(string, config.Site) apply.Transport { return emptyHost{} })
	cfg, secrets := imagesFixture(t, func(c *config.Config) {
		c.Infra.Images = map[string]string{"etcd": "gcr.io/etcd-development/etcd:v3.4.30"}
	})
	rows, lags, err := imageRows(&ui.Recorder{}, cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if !lags {
		t.Error("an override behind its track did not count")
	}
	var etcd, haproxy *imageRow
	for i := range rows {
		switch {
		case rows[i].stack == "infra" && rows[i].service == "etcd":
			etcd = &rows[i]
		case rows[i].stack == "infra" && rows[i].service == "haproxy":
			haproxy = &rows[i]
		}
	}
	if etcd == nil || haproxy == nil {
		t.Fatalf("rows: %+v", rows)
	}
	if etcd.origin != "infra.images.etcd" || !strings.Contains(etcd.note, "v3.5.0") || etcd.running != "-" {
		t.Errorf("etcd row: %+v", *etcd)
	}
	if haproxy.origin != "foundation" || !strings.HasPrefix(haproxy.newest, "3.0.0-alpine (") {
		t.Errorf("haproxy row: %+v", *haproxy)
	}
}

func TestImagesKeepsGoingPastASiteItCannotReach(t *testing.T) {
	cfg, secrets := imagesFixture(t, nil)
	if len(cfg.SiteNames()) < 1 {
		t.Fatal("the fixture declares no site")
	}
	withImagesTransport(t, func(string, config.Site) apply.Transport { return downHost{} })
	rec := &ui.Recorder{Verbose_: true}
	rows, _, err := imageRows(rec, cfg, secrets)
	if err != nil {
		t.Fatalf("one site down stopped the listing: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("a site that cannot be reached listed nothing")
	}
	for _, row := range rows {
		if row.running != "unknown" {
			t.Errorf("a site that cannot be reached has a running build: %+v", row)
		}
	}
	if !strings.Contains(rec.Lines(), "no route to host") {
		t.Errorf("the reason was not reported:\n%s", rec.Lines())
	}
}

func TestImagesCheckExitsThreeWhenSomethingWouldMove(t *testing.T) {
	if got := reportError(&strings.Builder{}, errImagesMove, false); got != 3 {
		t.Errorf("errImagesMove exits %d", got)
	}
}
```

`freshSite` declares one site, `home-a`, so the unreachable test covers the fallback rows; the multi-site case is the same loop. If `freshSite` declares more than one site, extend `downHost` use to one site by switching on the name in the transport function and assert the other site's rows have a real `running`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/paisans/ -run 'Images'`
Expected: FAIL to compile: `imageRows undefined`.

- [ ] **Step 3: Implement**

Create `cmd/paisans/images.go`:

```go
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// imagesTransport reaches a site for `paisans images`, which only reads.
// Tests replace it.
var imagesTransport = func(name string, site config.Site) apply.Transport {
	return siteTransport(name, site, "")
}

// errImagesMove is --check's answer when a build would move or an override
// lags. It is not a failure, and exits 3 so a schedule can tell it from one.
var errImagesMove = errors.New("a build would move, or an override lags")

// imageRow is one image one site runs, as `paisans images` prints it.
type imageRow struct {
	site, stack, service, origin, running, newest, note string
}

func runImages(args []string, stdout io.Writer) error {
	fs := newFlagSet("images", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	secretsPath := fs.String("secrets", "", "path to the secrets (default: secrets.enc.yaml beside the config)")
	check := fs.Bool("check", false, "exit 3 when any build would move or any override lags")
	if pos, err := parseAll(fs, args); err != nil {
		return err
	} else if len(pos) > 0 {
		return &ui.Problem{Hint: "images takes no arguments", Explain: fmt.Sprintf("It lists every site's images; %q is not a flag.", pos[0])}
	}
	r := errReporter(reporter())
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	result := validate.Check(cfg)
	reportFindings(r, *configPath, result)
	if result.Refused() {
		return refused(*configPath, len(result.Refusals()), "")
	}
	if *secretsPath == "" {
		*secretsPath = filepath.Join(filepath.Dir(*configPath), "secrets.enc.yaml")
	}
	secrets, err := config.LoadDeploymentSecrets(*secretsPath, cfg.ID)
	if err != nil {
		return err
	}
	if !secrets.Encrypted {
		warnUnencrypted(r, *secretsPath)
	}
	rows, lags, err := imageRows(r, cfg, secrets)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SITE\tSTACK\tSERVICE\tSOURCE\tRUNNING\tNEWEST\tNOTE")
	moves := lags
	for _, row := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", row.site, row.stack, row.service, row.origin, row.running, row.newest, row.note)
		if strings.HasPrefix(row.note, "moves") {
			moves = true
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if *check && moves {
		return errImagesMove
	}
	return nil
}

// imageRows plans every site as a dry run of apply does and lists each image
// it would run, and reports whether any override lags what it replaced. A
// site that cannot be planned is warned about and listed from the render
// alone, so one site down hides nothing about the others. It writes nothing
// to any host and asks for no sudo: a plan only reads (apply.Build).
func imageRows(r ui.Reporter, cfg *config.Config, secrets *config.Secrets) ([]imageRow, bool, error) {
	defer cacheDigests()()
	defer planEverySite()()
	var rendered *render.Plan
	var rows []imageRow
	lags := false
	seen := map[string]bool{}
	for _, site := range cfg.SiteNames() {
		plan, err := planSiteApply(cfg, secrets, site, imagesTransport(site, cfg.Sites[site]), apply.SkipSweepRead())
		if err != nil {
			r.Warn(fmt.Sprintf("could not plan %s; its running builds are unknown", site), err.Error())
			if rendered == nil {
				if rendered, err = render.Build(cfg, secrets); err != nil {
					return nil, false, err
				}
			}
			for _, image := range rendered.Images[site] {
				rows = append(rows, imageRow{site: site, stack: image.Stack, service: strings.Join(image.Services, ","), origin: image.Origin, running: "unknown", newest: "unknown"})
			}
			continue
		}
		reportNotes(r, plan, seen)
		for _, pin := range plan.Pins {
			rows = append(rows, pinRow(site, pin))
			lags = lags || pin.Behind
		}
	}
	return rows, lags, nil
}

// pinRow is one pin as a row: the build the site runs, the build the next
// apply runs, and what moves or lags.
func pinRow(site string, pin apply.ImagePin) imageRow {
	row := imageRow{site: site, stack: pin.Stack, service: strings.Join(pin.Services, ","), origin: pin.Origin, running: "-", newest: apply.BuildLabel(pin.Reference)}
	if pin.Deployed != "" {
		row.running = apply.BuildLabel(pin.Deployed)
	}
	if pin.Source == "deployed" {
		// The registry did not answer, and the plan keeps what runs.
		row.newest = "unknown"
	}
	var notes []string
	if pin.Moved() || pin.FirstPin() || (pin.Deployed == "" && pin.Reference != "") {
		notes = append(notes, "moves on next apply")
	}
	switch {
	case pin.Behind:
		notes = append(notes, "behind "+pin.Replaced)
	case pin.Replaced != "":
		notes = append(notes, "replaces "+pin.Replaced)
	}
	row.note = strings.Join(notes, "; ")
	if row.origin == "" {
		row.origin = "-"
	}
	return row
}
```

Check `config.LoadDeploymentSecrets` and `warnUnencrypted` names against `secretslist.go` and `main.go:552-558` and use what is there.

In `cmd/paisans/main.go`, add to the dispatch switch beside `doctor`:

```go
	case "images":
		err = runImages(os.Args[2:], os.Stdout)
```

and at the top of `reportError`, before `ui.PrintError`:

```go
	// Not a failure: --check found a build that would move or an override
	// that lags, and the table already says which. It exits 3 so a schedule
	// tells it from an error.
	if errors.Is(err, errImagesMove) {
		return 3
	}
```

In `cmd/paisans/help.go`, add `"images"` to the "Check" section after `"doctor"`, and an entry to `helpCommands` after `doctor`'s:

```go
	{
		name:    "images",
		summary: "List every image each site runs and what the next apply runs.",
		usage:   []string{"paisans images [--config " + config.FileName + "] [--secrets secrets.enc.yaml] [--check]"},
		detail: "List, for every site, each image its stacks run: where its reference comes from (the core catalog's foundation, the blueprint, or the configuration key that declares it), the build the site runs, and the build the next apply would run. " +
			"An override names what it replaced, and says when that is a newer release. " +
			"Reads every site over ssh and the registries; changes nothing. --check exits 3 when a build would move or an override lags.",
	},
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/paisans/`
Expected: PASS, including `TestTopHelpListsEveryCommandOnce`, `TestCommandHelpWraps`, `TestEveryFlagSetHasAPage`.

- [ ] **Step 5: Commit**

```bash
git add cmd/paisans/images.go cmd/paisans/images_test.go cmd/paisans/main.go cmd/paisans/help.go
git commit -m "feat: paisans images lists each site's builds and what would move

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Docs, decision record, changelog

**Files:**
- Modify: `README.md` (*Holding one image back* at ~line 2141; the review exemption list at ~line 5536-5560)
- Modify: `docs/commands.md` (`paisans validate` section; a new `paisans images` section under *Check*-like placement beside `paisans doctor` at ~line 952)
- Modify: `docs/development.md` (where `foundation.yaml` and `supportedTags` are described, ~line 801)
- Modify: `docs/decisions.md` (append a record)
- Modify: `CHANGELOG.md` (`[Unreleased]` → `### Added`)

**Interfaces:** none.

- [ ] **Step 1: README**

Replace the *Holding one image back* paragraph with:

```markdown
**Holding one image back, or moving it ahead of its track,** is a
configuration key: `apps.<name>.images` for an app's, `acme.image` for the
gateway's Caddy, and `infra` for the infrastructure stack's:

    infra:
      images:            # etcd, haproxy, garage, heartbeat
        haproxy: "docker.io/library/haproxy:3.2.*-alpine"
      postgres:
        "18":
          spilo: "ghcr.io/zalando/spilo-18:4.1-p3"

Each wins over the track, exact or a track of its own, and needs no catalog
release. `infra.postgres` is keyed by Postgres major and applies only while
`cluster.postgres_version` is that major. An etcd, HAProxy, Garage or Zalando
Spilo override stays inside the versions the toolkit is written against; a
Spilo from another provider is accepted with a warning. The heartbeat override
also replaces the operators heartbeat in every Pocket ID stack.
`paisans images` lists every image each site runs, the build the next apply
runs, and any override behind the release it replaced; `--check` exits 3 when
anything would move, for a schedule.
```

In the review exemption section, change item 3's "one of the infrastructure stack's" to "one of the infrastructure stack's unless `infra` declares it", and the later sentence "any image the configuration declares in `apps.<name>.images` or `acme.image`" to "any image the configuration declares in `apps.<name>.images`, `infra` or `acme.image`".

- [ ] **Step 2: docs/commands.md**

Add, after the `paisans doctor` section:

```markdown
### `paisans images`

Lists, for every site, each image its stacks run: where the reference comes
from (`foundation`, `blueprint`, or the configuration key that declares it),
the build the site runs, the build the next `apply` would run, and a note
when it moves or when an override is behind the release it replaced.

```sh
paisans images
paisans images --check   # exit 3 when a build would move or an override lags
```

* **Alone:** to see what the next apply moves, or on a schedule with
  `--check`.
* **Flags:** `--config`, `--secrets`, `--check` (off: exit 0 however much
  would move; on: exit 3 when anything would).
* **Hosts:** reads every site's compose files over ssh, as a dry run of
  `apply` does, and the registries. Changes none, and asks for no sudo. A site
  that cannot be reached is listed with `unknown` builds and a warning.
* **`--execute`:** not taken.
```

In the `paisans validate` section, add a bullet after **Alone:**

```markdown
* **Images:** an `infra` override is refused outside the versions the
  toolkit is written against (etcd v3, HAProxy 3, Garage v1, Zalando Spilo 3
  and 4), or with a digest and no tag; warned about for a Postgres major the
  deployment does not run, for a Spilo from another provider, and for the
  heartbeat, which it replaces in every Pocket ID stack too.
```

- [ ] **Step 3: docs/development.md**

Beside the paragraph naming `imagedigest.IsTrack` and `supportedTags` (~line 801), add:

```markdown
An operator's `infra` override is checked against the same table through
`blueprints.CheckFoundationImage`, from `internal/validate`. Render records on
each `render.ComposeImage` where its reference came from (`Origin`) and what
it replaced (`Replaces`); an image a configuration key declares is never
marked in `FoundationServices`, so the review exemption reaches only what the
render took from the official core catalog's foundation. Apply resolves
`Replaces` and warns, never refuses, when an override is an older release of
the same repository (`imagedigest.OlderTag`).
```

- [ ] **Step 4: docs/decisions.md**

Append:

```markdown
## 2026-10-15: An operator overrides the infrastructure stack's images

Founder decision, 2026-10-15. Spec: `docs/specs/2026-10-15-infra-image-overrides.md`.

### What was decided

* `infra.images` (etcd, haproxy, garage, heartbeat) and
  `infra.postgres.<major>.spilo` override the core catalog foundation's
  images, with `apps.<name>.images`' semantics.
* Spilo's override is keyed by Postgres major, so a build of one major never
  follows the cluster onto another, and any provider works.
* An override is always reviewed at apply.
* `paisans images` lists each site's builds and what would move; an override
  behind the release it replaced is a warning.

### Why

A security fix outside a foundation track waited for a core catalog release,
and an operator had nothing to write that moved the infrastructure sooner.

### What was weighed

* **One key per subsystem** (`etcd.image`, `storage.garage.image`, ...): next
  to related settings, but scattered, with no one place to audit.
* **An operator-side overlay of `foundation.yaml`:** complete, but a second
  path beside `apps.<name>.images` and `acme.image` for the same images.
* **One flat `infra.images.spilo`, checked by reading the major from the
  repository name:** works for Zalando's naming only.
* **An override for a pinned app's own Postgres:** left out.
* **The stale-override check in validate:** validate reads the file alone and
  reaches no registry.
```

- [ ] **Step 5: CHANGELOG.md**

Add under `[Unreleased]` → `### Added`:

```markdown
- `infra.images` and `infra.postgres.<major>.spilo` override the infrastructure stack's images without a catalog release, and `paisans images` lists each site's builds, what would move, and overrides behind their track.
```

Count it: at most 35 words.

- [ ] **Step 6: Verify and commit**

Run: `go test ./... && go vet ./...`
Expected: PASS.

```bash
git add README.md docs/commands.md docs/development.md docs/decisions.md CHANGELOG.md
git commit -m "docs: infra image overrides and paisans images

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
