# Backups, phase 1: `metadata:`, the revision, and distributing the pair

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move `version` and `id` into a `metadata:` block with a `revision` and `updated`, bump the revision on every toolkit write of `paisans.yaml` or `secrets.enc.yaml`, and have every `apply` copy both files to every site at `/srv/paisans/<token>/config/`.

**Architecture:** `config.Metadata` is embedded in `config.Config` under the YAML key `metadata`, so Go callers keep `cfg.ID` and `cfg.Version` through field promotion. Every write of the declaration already funnels through `replaceFile` in `internal/config`; it becomes `writeDeclaration`, which bumps the revision in the bytes it writes, editing lines in place as `SetMesh` does. `WriteSecrets` gains the declaration's path and bumps it after writing the secrets. A new `internal/apply/configcopy.go` reads every site's copy, decides whether a hand edit needs a bump and which sites are behind, and writes them; `apply --site` runs it after its own work.

**Tech Stack:** Go, `gopkg.in/yaml.v3` node API, the existing `apply.Transport`.

**Spec:** `docs/specs/2026-10-11-backups.md`, sections *`metadata:`* and *Every `apply` distributes the pair*, and phase 1 of *Phases*.

## Global Constraints

- `metadata.version` stays `1`. No schema bump, no migration code: a file with top level `version` or `id` is refused with a message naming the move.
- `revision` versions the pair. Every toolkit write of either file bumps it and sets `updated` to UTC, RFC 3339, seconds precision (Eg: `2026-10-11T18:04:00Z`).
- A write edits `paisans.yaml` in place, byte for byte outside the lines it changes. Never round trip the file through a YAML encoder.
- Remote copies: `/srv/paisans/<token>/config/paisans.yaml` and `/srv/paisans/<token>/config/secrets.enc.yaml`, owned by root, mode 0600. `secrets.enc.yaml` is copied as it is on disk; nothing is decrypted for it.
- Every `apply`, including `apply --site <name>`, distributes to every site. An unreachable site is a warning, never a failure.
- Repository rules (`AGENTS.md`): no em dashes, no rejected alternatives outside `docs/decisions.md`, cite upstream claims, commit subjects `<type>: lowercase subject` with a long body, trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never `git stash`.

## Review Focus

1. **A site holds a newer revision than the workstation** (a second admin applied from another checkout). Expected: `apply` refuses before writing anything, naming the site and both revisions, rather than overwriting the newer copy. Pinned in Task 6.
2. **`metadata:` written in flow style** (`metadata: { version: 1, id: ... }`) or with a comment on the revision line. Expected: a flow mapping is refused with a message saying to write it as a block; a trailing comment survives the bump. Pinned in Task 3.
3. **A command that writes both files** (init: id, mesh, secrets). Expected: the revision only ever increases and the file still loads after every write; several bumps in one command are fine. Pinned in Task 4.
4. **A hand edit to `secrets.enc.yaml` through `sops`** with `paisans.yaml` untouched. Expected: the next `apply` sees the secrets differ from the newest copy and bumps. Pinned in Task 6.
5. **Dry run.** Expected: `apply --site x` without `--execute` writes nothing locally or remotely and reports which sites it would copy to and whether it would bump. Pinned in Task 7.

---

## File structure

| File | Responsibility |
|------|----------------|
| `internal/config/metadata.go` (new) | `Metadata` type, the top level move check, `ReadRevision`, `Strip` |
| `internal/config/revision.go` (new) | `bumpRevision` (pure, on bytes), `writeDeclaration`, `BumpRevision` |
| `internal/config/config.go` | embed `Metadata`; call the move check in `load` |
| `internal/config/id.go` | `EnsureID` writes `id` inside `metadata:` |
| `internal/config/mesh.go`, `include_dir.go`, `remove_site.go`, `remove_witness.go` | call `writeDeclaration` instead of `replaceFile` |
| `internal/config/secrets_write.go` | `WriteSecrets(declaration, path, ...)` bumps the declaration |
| `internal/config/writepath_test.go` (new) | the single write path guard |
| `internal/deployment/deployment.go` | `ConfigCopy(name string) string` |
| `internal/apply/configcopy.go` (new) | `PlanConfigCopy`, `ExecuteConfigCopy` |
| `cmd/paisans/configcopy.go` (new) | `distributeConfig`, the reporting |
| `cmd/paisans/main.go`, `converge.go` | wiring; `missingID` reads `metadata.id` |
| every fixture and example | the mechanical move |
| `README.md`, `docs/commands.md`, `examples/*.yaml` | the new block, and where the copies live |

---

### Task 1: `metadata:` block, and the refusal of the old layout

**Files:**
- Create: `internal/config/metadata.go`
- Modify: `internal/config/config.go:84-92` (the `Version` and `ID` fields), `internal/config/config.go:727-738` (`load`)
- Modify: `cmd/paisans/converge.go:630-640` (`missingID`)
- Modify: every fixture holding a declaration (script below)
- Test: `internal/config/metadata_test.go`

**Interfaces:**
- Produces: `type Metadata struct { Version int; ID string; Revision int; Updated string }` embedded in `Config` with tag `yaml:"metadata"`. `cfg.ID`, `cfg.Version`, `cfg.Revision`, `cfg.Updated` all work by promotion.

- [ ] **Step 1: Write the failing tests**

`internal/config/metadata_test.go`:

```go
package config_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
)

func TestLoadReadsMetadata(t *testing.T) {
	body := strings.Replace(minimal, "  id: "+fixtureID+"\n", "  id: "+fixtureID+"\n  revision: 42\n  updated: 2026-10-11T18:04:00Z\n", 1)
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 1 || cfg.ID != fixtureID || cfg.Revision != 42 || cfg.Updated != "2026-10-11T18:04:00Z" {
		t.Fatalf("metadata = %+v", cfg.Metadata)
	}
}

func TestLoadRefusesTopLevelVersionOrID(t *testing.T) {
	for _, tc := range []struct{ name, body, key string }{
		{"version", "version: 1\n" + minimal, "version"},
		{"id", "id: " + fixtureID + "\n" + strings.Replace(minimal, "  id: "+fixtureID+"\n", "", 1), "id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(write(t, tc.body))
			if err == nil {
				t.Fatal("Load accepted a top level " + tc.key)
			}
			if want := "metadata." + tc.key; !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not name %s: %v", want, err)
			}
		})
	}
}
```

Change `minimal` in `internal/config/config_test.go:21` to open with:

```go
const minimal = `metadata:
  version: 1
  id: f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01
community:
```

and change `withID` in `id_test.go` to match the `  id: ` line and to insert after `  version: 1\n`.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/config/ -run 'Metadata|TopLevel' -v`
Expected: FAIL to compile: `cfg.Revision undefined`.

- [ ] **Step 3: Implement**

`internal/config/metadata.go`:

```go
package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Metadata is what the declaration says about itself rather than about the
// deployment: the format version, the deployment's identity, and the revision
// of the pair paisans.yaml and secrets.enc.yaml. It is embedded in Config, so
// cfg.ID and the rest read as they always have.
type Metadata struct {
	Version int `yaml:"version"`
	// ID is the deployment's identity, a version 4 UUID that `paisans init`
	// writes once and nothing changes afterwards. Every name and path on a host
	// is derived from it (see internal/deployment), and the host registry is
	// keyed by it, so changing it would orphan everything already deployed.
	ID string `yaml:"id"`
	// Revision counts the toolkit's writes of paisans.yaml and
	// secrets.enc.yaml together. It only increases. Updated is when it last
	// did, in UTC.
	Revision int    `yaml:"revision"`
	Updated  string `yaml:"updated"`
}

// movedKeys are the top level keys that moved into metadata.
var movedKeys = []string{"version", "id"}

// refuseMovedKeys names a top level version or id, which now belong in
// metadata, before the decode would refuse them as unknown fields with no
// word about where they went.
func refuseMovedKeys(path string, data []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	m := doc.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		for _, k := range movedKeys {
			if m.Content[i].Value == k {
				return fmt.Errorf("%s:%d: %s is now metadata.%s. Move it under a top level `metadata:` block, Eg:\n\nmetadata:\n  version: 1\n  id: <the deployment id>", path, m.Content[i].Line, k, k)
			}
		}
	}
	return nil
}
```

In `config.go`, replace the `Version` and `ID` fields (and the `ID` comment, now on `Metadata.ID`) with:

```go
	Metadata `yaml:"metadata"`
```

yaml.v3 nests an embedded struct under its tag unless the tag says `,inline`, so this is the `metadata:` block, and Go promotes its fields.

In `load`, after `os.ReadFile` succeeds and before the decoder:

```go
	if err := refuseMovedKeys(path, data); err != nil {
		return nil, err
	}
```

If the error type used for config problems elsewhere in `load` is a `Problem` (see `problem.go`), wrap it the same way `parseProblem` does so the hint and explanation split as other refusals do.

In `cmd/paisans/converge.go`, `missingID`:

```go
	var doc struct {
		Metadata struct {
			ID string `yaml:"id"`
		} `yaml:"metadata"`
	}
	return yaml.Unmarshal(data, &doc) == nil && doc.Metadata.ID == ""
```

- [ ] **Step 4: Move every fixture**

Write this script to the scratchpad, not the repository, and run it from the worktree root:

```sh
cat > "$SCRATCH/move-metadata.py" <<'EOF'
import pathlib, re, subprocess, sys
files = subprocess.run(["git", "grep", "-l", "-E", "^version: 1$", "--", "*.yaml", "*.yml", "README.md", "docs"], capture_output=True, text=True).stdout.split()
for name in files:
    p = pathlib.Path(name)
    s = p.read_text()
    # A secrets file has the same first line and keeps it: only declarations move.
    if not re.search(r"(?m)^(sites|community|apps):", s):
        continue
    s2 = re.sub(r"(?m)^version: 1\nid: (\S+)\n", r"metadata:\n  version: 1\n  id: \1\n", s, count=1)
    if s2 == s:
        s2 = re.sub(r"(?m)^version: 1\n", "metadata:\n  version: 1\n", s, count=1)
    if s2 != s:
        p.write_text(s2)
        print(name)
EOF
python3 -I "$SCRATCH/move-metadata.py"
```

Then the Go string fixtures (15 files hold `version: 1` in a string; `git grep -n 'version: 1' -- '*.go'`). Change each declaration string the same way by hand. Leave the ones that are secrets (`agekey_test.go:116`, `init_test.go:322,400,411`): those write `secrets.enc.yaml`.

Markdown code blocks in `README.md` and `docs/` showing a declaration move too; the script's `README.md` and `docs` entries cover files whose fences start a line with `version: 1`. Check each hit by eye.

- [ ] **Step 5: Run the whole suite**

Run: `go test ./...`
Expected: PASS. A failure saying `version is now metadata.version` names a fixture the script missed: move it and run again. Golden files under `internal/render/testdata` do not change, since no rendered file carries the declaration.

- [ ] **Step 6: Commit**

```bash
git add -A internal cmd examples README.md docs
git commit -m "feat: version and id move into a metadata block" -m "..."
```

Body: what moved, that `cfg.ID` still reads the same through embedding, that the old layout is refused with the move named, no migration. "Founder decision." and the trailer.

---

### Task 2: `EnsureID` writes into `metadata:`

**Files:**
- Modify: `internal/config/id.go` (all of `EnsureID`, `topLevelID`, `versionLine`)
- Test: `internal/config/id_test.go`

**Interfaces:**
- Consumes: `Metadata` (Task 1).
- Produces: `EnsureID(path string) (id string, added bool, err error)`, unchanged signature.

- [ ] **Step 1: Update the tests**

In `TestEnsureIDWritesAVersion4IDAndKeepsEveryOtherByte`, the expected file is `body` with `"  id: " + id + "\n"` inserted after `"  version: 1\n"`. Add:

```go
func TestEnsureIDRefusesADeclarationWithoutMetadata(t *testing.T) {
	path := write(t, strings.Replace(withID(""), "metadata:\n  version: 1\n", "", 1))
	_, _, err := config.EnsureID(path)
	if err == nil || !strings.Contains(err.Error(), "metadata:") {
		t.Fatalf("EnsureID: %v, want a refusal naming the metadata block", err)
	}
}
```

- [ ] **Step 2: Run to see it fail**

Run: `go test ./internal/config/ -run EnsureID -v`
Expected: FAIL: the id lands at the top level.

- [ ] **Step 3: Implement**

Replace `versionLine` with `metadataVersionLine = regexp.MustCompile(`(?m)^metadata:[ \t]*\n([ \t]+)version:[^\n]*\n`)`. Write `<indent>id: <id>\n` after the match, with the captured indent. `topLevelID` becomes `metadataID`: find the top level `metadata` key, and within its mapping the `id` key. The refusal when there is no match:

```go
return "", false, fmt.Errorf("%s: no `metadata:` block with `version:` as its first key to write the deployment id into. Start the file with:\n\nmetadata:\n  version: 1", path)
```

`EnsureID` writes through `writeDeclaration` once Task 3 exists; until then `replaceFile`.

- [ ] **Step 4: Run**

Run: `go test ./internal/config/ ./cmd/paisans/ -run 'EnsureID|Init' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

`feat: init writes the deployment id into metadata`

---

### Task 3: the revision bump, on bytes

**Files:**
- Create: `internal/config/revision.go`
- Modify: `internal/config/mesh.go:286-310` (`replaceFile` stays as the plain atomic write; nothing outside `revision.go` calls it after this task)
- Modify: `internal/config/id.go`, `mesh.go:119`, `include_dir.go:26`, `remove_site.go:79`, `remove_witness.go:30`: `replaceFile(path, out)` becomes `writeDeclaration(path, out)`
- Test: `internal/config/revision_test.go`

**Interfaces:**
- Produces:
  - `func bumpRevision(data []byte, now time.Time) ([]byte, error)`, unexported, pure.
  - `func writeDeclaration(path string, data []byte) error`, unexported: bump, then `replaceFile`.
  - `func BumpRevision(path string) error`, exported: read, bump, write. Used by `WriteSecrets` (Task 4) and `apply` (Task 6).
  - `var now = time.Now`, replaced in tests.

- [ ] **Step 1: Write the failing tests**

```go
package config

import (
	"strings"
	"testing"
	"time"
)

var at = time.Date(2026, 10, 11, 18, 4, 0, 0, time.UTC)

func TestBumpRevision(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{
			"adds both after the last key",
			"metadata:\n  version: 1\n  id: x\nsites: {}\n",
			"metadata:\n  version: 1\n  id: x\n  revision: 1\n  updated: 2026-10-11T18:04:00Z\nsites: {}\n",
		},
		{
			"increments in place",
			"# top\nmetadata:\n  version: 1\n  id: x\n  revision: 41\n  updated: 2020-01-01T00:00:00Z\nsites: {}\n",
			"# top\nmetadata:\n  version: 1\n  id: x\n  revision: 42\n  updated: 2026-10-11T18:04:00Z\nsites: {}\n",
		},
		{
			"keeps a trailing comment and the indent",
			"metadata:\n    version: 1\n    revision: 3   # do not edit\n",
			"metadata:\n    version: 1\n    revision: 4   # do not edit\n    updated: 2026-10-11T18:04:00Z\n",
		},
		{
			"metadata not first",
			"sites: {}\nmetadata:\n  version: 1\n",
			"sites: {}\nmetadata:\n  version: 1\n  revision: 1\n  updated: 2026-10-11T18:04:00Z\n",
		},
		{
			"no trailing newline",
			"metadata:\n  version: 1",
			"metadata:\n  version: 1\n  revision: 1\n  updated: 2026-10-11T18:04:00Z\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bumpRevision([]byte(tc.in), at)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestBumpRevisionRefuses(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"flow style", "metadata: { version: 1 }\n", "block"},
		{"no metadata", "sites: {}\n", "metadata:"},
		{"not a number", "metadata:\n  version: 1\n  revision: two\n", "revision"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := bumpRevision([]byte(tc.in), at)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run to see them fail**

Run: `go test ./internal/config/ -run BumpRevision -v`
Expected: FAIL: `undefined: bumpRevision`.

- [ ] **Step 3: Implement**

`internal/config/revision.go`:

```go
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// now is the clock updated is read from. Tests replace it.
var now = time.Now

// BumpRevision bumps the revision of the declaration at path. It is the write
// for a change to the other file of the pair, secrets.enc.yaml, and for a hand
// edit apply has found.
func BumpRevision(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	return writeDeclaration(path, data)
}

// writeDeclaration is the one way the toolkit writes paisans.yaml: data, with
// its revision bumped, replacing path atomically. Every write bumps, so a
// command that writes twice bumps twice; the revision only has to increase.
func writeDeclaration(path string, data []byte) error {
	out, err := bumpRevision(data, now())
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return replaceFile(path, out)
}

// bumpRevision adds one to metadata.revision and sets metadata.updated, each
// replaced where it stands or added after the block's last line, indented as
// its first key. Every other byte is kept, as SetMesh keeps them.
func bumpRevision(data []byte, at time.Time) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("no `metadata:` block")
	}
	top := doc.Content[0]
	var meta *yaml.Node
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == "metadata" {
			meta = top.Content[i+1]
		}
	}
	if meta == nil {
		return nil, errors.New("no `metadata:` block")
	}
	if meta.Kind != yaml.MappingNode || meta.Style&yaml.FlowStyle != 0 || len(meta.Content) == 0 {
		return nil, errors.New("metadata is not a block mapping. Write it one key per line, as `metadata:` followed by indented keys")
	}

	revision := 0
	var revNode, updNode *yaml.Node
	last := 0
	for i := 0; i+1 < len(meta.Content); i += 2 {
		k, v := meta.Content[i], meta.Content[i+1]
		if v.Line > last {
			last = v.Line
		}
		switch k.Value {
		case "revision":
			n, err := strconv.Atoi(v.Value)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("metadata.revision %q is not a whole number", v.Value)
			}
			revision, revNode = n, v
		case "updated":
			updNode = v
		}
	}
	indent := meta.Content[0].Column - 1
	stamp := at.UTC().Format(time.RFC3339)

	lines := bytes.SplitAfter(data, []byte("\n"))
	if len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if n := len(lines); n > 0 && !bytes.HasSuffix(lines[n-1], []byte("\n")) {
		lines[n-1] = append(lines[n-1], '\n')
	}
	var add [][]byte
	set := func(node *yaml.Node, key, value string) {
		if node == nil {
			add = append(add, []byte(fmt.Sprintf("%*s%s: %s\n", indent, "", key, value)))
			return
		}
		lines[node.Line-1] = replaceScalar(lines[node.Line-1], node, value)
	}
	set(revNode, "revision", strconv.Itoa(revision+1))
	set(updNode, "updated", stamp)

	var out bytes.Buffer
	for i, l := range lines {
		out.Write(l)
		if i == last-1 {
			for _, a := range add {
				out.Write(a)
			}
		}
	}
	return out.Bytes(), nil
}

// replaceScalar replaces the plain scalar node starts on line with value,
// leaving what precedes and follows it, a comment included.
func replaceScalar(line []byte, node *yaml.Node, value string) []byte {
	start := node.Column - 1
	end := start + len(node.Value)
	if node.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) != 0 {
		end += 2
	}
	out := append([]byte{}, line[:start]...)
	out = append(out, value...)
	return append(out, line[end:]...)
}
```

Note the `last` line rule: the new keys go after the line holding the block's last value. A multi-line value as the last key of `metadata:` is not a layout the toolkit writes; if the test author finds one in a fixture, add a refusal with the same message as flow style.

Then replace the five `replaceFile(path, out)` call sites listed under **Files** with `writeDeclaration(path, out)`.

- [ ] **Step 4: Run**

Run: `go test ./internal/config/ -v -run 'BumpRevision|SetMesh|IncludeDir|RemoveSite|RemoveWitness|EnsureID'`
Expected: PASS. Tests that compare a whole written file against an expected string now see `revision:` and `updated:` added: fix them by setting `config.SetNow` (add `func SetNow(f func() time.Time) func()` to `export_test.go`, returning the restore) to `at` and adding the two expected lines.

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

`feat: every write of paisans.yaml bumps metadata.revision`

---

### Task 4: writing the secrets bumps the declaration

**Files:**
- Modify: `internal/config/secrets_write.go:53` (`WriteSecrets`)
- Modify callers: `cmd/paisans/main.go:524`, `oidc.go:183`, `rotatekey.go:102`, `secrets.go:134`, `secrets.go:233`, `signupgroups.go:113`, and any test calling `WriteSecrets`
- Test: `internal/config/secrets_test.go`, `cmd/paisans/init_test.go`

**Interfaces:**
- Produces: `func WriteSecrets(declaration, path string, s *Secrets, recipients []string) error`. `declaration` is the path of the `paisans.yaml` this secrets file belongs to; it is bumped after the secrets are written.

- [ ] **Step 1: Write the failing tests**

In `internal/config/secrets_test.go`:

```go
func TestWriteSecretsBumpsTheDeclaration(t *testing.T) {
	decl := write(t, minimal)
	secrets := filepath.Join(filepath.Dir(decl), "secrets.enc.yaml")
	if err := config.WriteSecrets(decl, secrets, &config.Secrets{Version: 1}, nil); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(decl)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Revision != 1 || cfg.Updated == "" {
		t.Fatalf("metadata after a secrets write = %+v", cfg.Metadata)
	}
}
```

In `cmd/paisans/init_test.go`, add to the test that runs a full `init` on a fresh declaration (find it with `grep -n "func TestInit" cmd/paisans/init_test.go`):

```go
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Revision < 1 {
		t.Fatalf("init wrote the pair and left revision %d", cfg.Revision)
	}
```

- [ ] **Step 2: Run to see them fail**

Run: `go test ./internal/config/ -run WriteSecretsBumps -v`
Expected: FAIL to compile: too many arguments.

- [ ] **Step 3: Implement**

```go
// WriteSecrets serialises secrets and writes them to path, encrypted to the
// given age recipients, then bumps the revision of declaration, the
// paisans.yaml the secrets belong to: the revision versions the pair. A bump
// that fails after the secrets were written leaves them written, and the next
// apply finds the change and bumps then.
func WriteSecrets(declaration, path string, s *Secrets, recipients []string) error {
	// ... the existing body, unchanged, up to the writeSecretsFile call ...
	if err := writeSecretsFile(path, out); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return BumpRevision(declaration)
}
```

Keep the existing doc comment paragraphs about recipients below the new first paragraph. Each caller passes its configuration path: `cfg.Path` where a loaded `*config.Config` is in scope, otherwise the `--config` flag's value (`*configPath`). `convergeGenerate` writes secrets during a dry run; that is a real write and bumps, which is correct.

- [ ] **Step 4: Run**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

`feat: writing the secrets bumps the declaration's revision`

---

### Task 5: one write path, enforced

**Files:**
- Create: `internal/config/writepath_test.go`

**Interfaces:**
- Consumes: nothing. It reads the repository's Go sources.

- [ ] **Step 1: Write the test**

```go
package config_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writers are the functions that can put bytes in a file on the
// workstation. Only the files below may call them. A new caller is either a
// write of paisans.yaml or secrets.enc.yaml that skips the revision, which
// the spec forbids, or a write of some other file, which belongs on this
// list with its reason.
var writers = map[string]bool{
	"os.WriteFile": true, "os.Create": true, "os.CreateTemp": true,
	"os.OpenFile": true, "os.Rename": true,
}

var allowed = map[string]string{
	"internal/config/mesh.go":          "replaceFile, the atomic write under writeDeclaration",
	"internal/config/secrets_write.go": "writeSecretsFile, under WriteSecrets",
	"internal/render/write.go":         "render writes its output directory, never the pair",
	"internal/apply/transport.go":      "a host key file for ssh, never the pair",
}

func TestOnlyTheConfigPackageWritesThePair(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(here), "..", "..")
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || !writers[pkg.Name+"."+sel.Sel.Name] {
				return true
			}
			if _, ok := allowed[filepath.ToSlash(rel)]; !ok {
				t.Errorf("%s: %s.%s writes a file outside the allowed writers. A write of paisans.yaml or secrets.enc.yaml goes through internal/config so the revision is bumped; any other file is added to `allowed` with its reason", fset.Position(call.Pos()), pkg.Name, sel.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/config/ -run OnlyTheConfigPackage -v`
Expected: PASS, or failures naming files not in `allowed`. For each, read the call: if it writes the pair, route it through `internal/config`; if not, add it to `allowed` with a one line reason. Do not add a file whose write could reach the pair.

- [ ] **Step 3: Prove it bites**

Temporarily add `_ = os.WriteFile("x", nil, 0)` to `cmd/paisans/secrets.go`, run the test, see it FAIL naming that line, remove it.

- [ ] **Step 4: Commit**

`test: only internal/config writes paisans.yaml and secrets.enc.yaml`

---

### Task 6: deciding what to copy

**Files:**
- Modify: `internal/deployment/deployment.go` (add `ConfigCopy`)
- Create: `internal/config/copy.go`: `Strip`, `ReadRevision`
- Create: `internal/apply/configcopy.go`
- Test: `internal/config/copy_test.go`, `internal/apply/configcopy_test.go`

**Interfaces:**
- Produces:
  - `func (d Deployment) ConfigCopy(name string) string` returns `d.Path("config", name)`.
  - `func ReadRevision(data []byte) int`: `metadata.revision`, 0 when absent or unreadable.
  - `func Strip(data []byte) []byte`: `data` without the lines holding `metadata.revision` and `metadata.updated`, for comparing content.
  - `type ConfigCopies struct { Bump bool; Behind []string; Unreachable map[string]error }`
  - `func PlanConfigCopy(declaration, secrets []byte, sites map[string]Transport, d deployment.Deployment) (ConfigCopies, error)`
  - `func ExecuteConfigCopy(declaration, secrets []byte, sites []string, transports map[string]Transport, d deployment.Deployment) map[string]error`

- [ ] **Step 1: Write the failing tests**

`internal/config/copy_test.go`:

```go
func TestStripIgnoresOnlyRevisionAndUpdated(t *testing.T) {
	a := "metadata:\n  version: 1\n  revision: 3\n  updated: 2026-01-01T00:00:00Z\nsites: {}\n"
	b := "metadata:\n  version: 1\n  revision: 9\n  updated: 2026-10-01T00:00:00Z\nsites: {}\n"
	c := "metadata:\n  version: 1\n  revision: 3\n  updated: 2026-01-01T00:00:00Z\nsites: { x: {} }\n"
	if string(config.Strip([]byte(a))) != string(config.Strip([]byte(b))) {
		t.Error("two revisions of the same content compare different")
	}
	if string(config.Strip([]byte(a))) == string(config.Strip([]byte(c))) {
		t.Error("an edit compares equal")
	}
	if config.ReadRevision([]byte(b)) != 9 || config.ReadRevision([]byte("sites: {}\n")) != 0 {
		t.Error("ReadRevision")
	}
}
```

`internal/apply/configcopy_test.go`, with `newHost()` from `apply_test.go` (a `*fakeHost` whose `files` map backs `ReadFile` and `WriteFile`; check its constructor name with `grep -n "func newHost" internal/apply/*_test.go`):

```go
func decl(rev int, body string) []byte {
	return []byte(fmt.Sprintf("metadata:\n  version: 1\n  id: x\n  revision: %d\n  updated: 2026-10-11T00:00:00Z\n%s", rev, body))
}

func TestPlanConfigCopy(t *testing.T) {
	d := deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
	put := func(h *fakeHost, decl, secrets []byte) {
		h.files[d.ConfigCopy("paisans.yaml")] = string(decl)
		h.files[d.ConfigCopy("secrets.enc.yaml")] = string(secrets)
	}
	sec := []byte("sops: a\n")

	t.Run("a site with no copy is behind, nothing to bump", func(t *testing.T) {
		a, b := newHost(), newHost()
		put(a, decl(4, "sites: {}\n"), sec)
		got, err := PlanConfigCopy(decl(4, "sites: {}\n"), sec, map[string]Transport{"a": a, "b": b}, d)
		if err != nil || got.Bump || !reflect.DeepEqual(got.Behind, []string{"b"}) {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("a hand edit at the same revision bumps", func(t *testing.T) {
		a := newHost()
		put(a, decl(4, "sites: {}\n"), sec)
		got, err := PlanConfigCopy(decl(4, "sites: { x: {} }\n"), sec, map[string]Transport{"a": a}, d)
		if err != nil || !got.Bump {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("a hand edit of the secrets alone bumps", func(t *testing.T) {
		a := newHost()
		put(a, decl(4, "sites: {}\n"), sec)
		got, err := PlanConfigCopy(decl(4, "sites: {}\n"), []byte("sops: b\n"), map[string]Transport{"a": a}, d)
		if err != nil || !got.Bump {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("a newer copy on a site refuses", func(t *testing.T) {
		a := newHost()
		put(a, decl(7, "sites: {}\n"), sec)
		_, err := PlanConfigCopy(decl(4, "sites: {}\n"), sec, map[string]Transport{"a": a}, d)
		if err == nil || !strings.Contains(err.Error(), "revision 7") || !strings.Contains(err.Error(), "revision 4") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an unreachable site is recorded, not an error", func(t *testing.T) {
		a := newHost()
		a.fail = "cat"
		got, err := PlanConfigCopy(decl(4, "sites: {}\n"), sec, map[string]Transport{"a": a}, d)
		if err != nil || got.Unreachable["a"] == nil {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("a revision 0 declaration bumps", func(t *testing.T) {
		got, err := PlanConfigCopy([]byte("metadata:\n  version: 1\n  id: x\n"), sec, map[string]Transport{}, d)
		if err != nil || !got.Bump {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
}

func TestExecuteConfigCopyWritesBothFilesRootOnly(t *testing.T) {
	d := deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
	a := newHost()
	errs := ExecuteConfigCopy(decl(5, ""), []byte("sops: a\n"), []string{"a"}, map[string]Transport{"a": a}, d)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if a.files[d.ConfigCopy("paisans.yaml")] != string(decl(5, "")) || a.files[d.ConfigCopy("secrets.enc.yaml")] != "sops: a\n" {
		t.Fatal("the copies differ from what was sent")
	}
	// fakeHost records modes beside files; assert 0600 the way other
	// WriteFile tests in this package do.
}
```

Before writing the last assertion, read how `fakeHost.WriteFile` records a mode (`grep -n "func (h \*fakeHost) WriteFile" -A10 internal/apply/apply_test.go`) and assert `0o600` through it. If it records none, add a `modes map[string]uint32` field set in `WriteFile`.

How `fail` reaches `ReadFile` on the fake also needs reading: if `ReadFile` does not honour `fail`, make the unreachable case use whatever the fake does offer for a failed read, or add a `readFail string` field.

- [ ] **Step 2: Run to see them fail**

Run: `go test ./internal/config/ ./internal/apply/ -run 'Strip|ConfigCopy' -v`
Expected: FAIL: undefined.

- [ ] **Step 3: Implement**

`internal/deployment/deployment.go`:

```go
// ConfigCopy is where apply keeps a site's copy of one file of the pair,
// /srv/paisans/<token>/config/<name>, so every site can name the declaration
// it runs by revision, and the backup runner can upload it.
func (d Deployment) ConfigCopy(name string) string { return d.Path("config", name) }
```

`internal/config/copy.go`:

```go
package config

import (
	"bytes"

	"gopkg.in/yaml.v3"
)

// ReadRevision is data's metadata.revision, 0 when it has none or cannot be
// read: a copy that cannot be read is older than any revision.
func ReadRevision(data []byte) int {
	var doc struct {
		Metadata Metadata `yaml:"metadata"`
	}
	if yaml.Unmarshal(data, &doc) != nil {
		return 0
	}
	return doc.Metadata.Revision
}

// Strip is data without the lines holding metadata.revision and
// metadata.updated, so two files compare equal exactly when they declare the
// same thing.
func Strip(data []byte) []byte {
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return data
	}
	drop := map[int]bool{}
	top := doc.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value != "metadata" || top.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		m := top.Content[i+1]
		for j := 0; j+1 < len(m.Content); j += 2 {
			if k := m.Content[j].Value; k == "revision" || k == "updated" {
				drop[m.Content[j].Line] = true
			}
		}
	}
	var out bytes.Buffer
	for n, line := range bytes.SplitAfter(data, []byte("\n")) {
		if !drop[n+1] {
			out.Write(line)
		}
	}
	return out.Bytes()
}
```

`internal/apply/configcopy.go`:

```go
package apply

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployment"
)

// ConfigCopies is what distributing the pair would do: bump the revision
// first, because the declaration changed by hand since the newest copy any
// site holds, then write the sites that are behind. A site that could not be
// read is in Unreachable, and is caught up by a later apply.
type ConfigCopies struct {
	Bump        bool
	Behind      []string
	Unreachable map[string]error
}

// PlanConfigCopy reads every site's copy of the pair and compares it with
// the workstation's. A site holding a newer revision than the workstation is
// a refusal: another checkout applied since this one was last updated, and
// copying would replace what it declared.
func PlanConfigCopy(declaration, secrets []byte, sites map[string]Transport, d deployment.Deployment) (ConfigCopies, error) {
	out := ConfigCopies{Unreachable: map[string]error{}}
	local := config.ReadRevision(declaration)
	newest, newestSite := -1, ""
	var newestDecl, newestSecrets []byte
	names := make([]string, 0, len(sites))
	for name := range sites {
		names = append(names, name)
	}
	sort.Strings(names)
	held := map[string][2][]byte{}
	for _, name := range names {
		t := sites[name]
		decl, found, err := t.ReadFile(d.ConfigCopy("paisans.yaml"))
		if err != nil {
			out.Unreachable[name] = err
			continue
		}
		sec, _, err := t.ReadFile(d.ConfigCopy("secrets.enc.yaml"))
		if err != nil {
			out.Unreachable[name] = err
			continue
		}
		if !found {
			out.Behind = append(out.Behind, name)
			continue
		}
		held[name] = [2][]byte{[]byte(decl), []byte(sec)}
		if r := config.ReadRevision([]byte(decl)); r > newest {
			newest, newestSite, newestDecl, newestSecrets = r, name, []byte(decl), []byte(sec)
		}
	}
	if newest > local {
		return out, fmt.Errorf("%s holds revision %d of paisans.yaml, newer than this one's revision %d: another checkout has applied since. Bring this checkout up to date, Eg: copy %s from %s, before applying", newestSite, newest, local, d.ConfigCopy("paisans.yaml"), newestSite)
	}
	out.Bump = local == 0 || (newest == local &&
		(!bytes.Equal(config.Strip(declaration), config.Strip(newestDecl)) || !bytes.Equal(secrets, newestSecrets)))
	for name, pair := range held {
		if out.Bump || !bytes.Equal(pair[0], declaration) || !bytes.Equal(pair[1], secrets) {
			out.Behind = append(out.Behind, name)
		}
	}
	sort.Strings(out.Behind)
	return out, nil
}

// ExecuteConfigCopy writes both files to each named site, root owned and
// 0600, and returns the sites it could not write. Nothing is decrypted:
// secrets.enc.yaml reaches the host as it is on the workstation.
func ExecuteConfigCopy(declaration, secrets []byte, sites []string, transports map[string]Transport, d deployment.Deployment) map[string]error {
	failed := map[string]error{}
	for _, name := range sites {
		t := transports[name]
		if err := t.WriteFile(d.ConfigCopy("paisans.yaml"), string(declaration), 0o600); err != nil {
			failed[name] = err
			continue
		}
		if err := t.WriteFile(d.ConfigCopy("secrets.enc.yaml"), string(secrets), 0o600); err != nil {
			failed[name] = err
		}
	}
	return failed
}
```

`WriteFile` runs through sudo on a real site (`apply --sudo`, the default), so the file is root's. If `SSHTransport.WriteFile` creates parent directories with a mode wider than 0700, add `install -d -m 0700 -o root -g root <dir>` as a `Run` before the first write and test it on the fake.

- [ ] **Step 4: Run**

Run: `go test ./internal/config/ ./internal/apply/ ./internal/deployment/ -v -run 'Strip|ConfigCopy|Deployment'`
Expected: PASS.

- [ ] **Step 5: Commit**

`feat: apply plans copying the declaration and secrets to every site`

Body names the refusal on a newer copy as part of the design, because it is the only behaviour here the spec does not spell out.

---

### Task 7: every `apply` distributes the pair

**Files:**
- Create: `cmd/paisans/configcopy.go`
- Modify: `cmd/paisans/main.go:610-890` (`runApply`): call `distributeConfig` in the dry run branch and after `recordApplied`
- Test: `cmd/paisans/configcopy_test.go`

**Interfaces:**
- Consumes: `apply.PlanConfigCopy`, `apply.ExecuteConfigCopy`, `config.BumpRevision`, `siteTransport` (`main.go:1460`).
- Produces: `func distributeConfig(r ui.Reporter, cfg *config.Config, configPath, secretsPath string, execute bool, transport func(site string) apply.Transport) error`

- [ ] **Step 1: Write the failing test**

Find how `cmd/paisans` tests drive `runApply` against fakes: `grep -n "transportFor\|siteTransport =\|var siteTransport" cmd/paisans/*.go`. If `siteTransport` is a plain function, make the transport an argument of `distributeConfig` (as in the signature above) and test `distributeConfig` directly with a recording reporter (`grep -n "type recorder\|func newRecorder" cmd/paisans/*_test.go` for the one other tests use) and `apply` fakes exported for tests, or a small fake in `configcopy_test.go` implementing `apply.Transport` over a map.

```go
func TestDistributeConfig(t *testing.T) {
	dir := t.TempDir()
	declPath := filepath.Join(dir, "paisans.yaml")
	secPath := filepath.Join(dir, "secrets.enc.yaml")
	// minimalDeclaration: a two site declaration from this package's fixtures,
	// with metadata.revision: 3.
	writeFile(t, declPath, minimalDeclaration)
	writeFile(t, secPath, "sops: a\n")
	cfg, err := config.Load(declPath)
	if err != nil {
		t.Fatal(err)
	}
	hosts := map[string]*mapHost{"home-a": newMapHost(), "home-b": newMapHost()}
	hosts["home-b"].unreachable = true
	transport := func(s string) apply.Transport { return hosts[s] }

	t.Run("dry run writes nothing", func(t *testing.T) {
		rec := newRecorder()
		if err := distributeConfig(rec, cfg, declPath, secPath, false, transport); err != nil {
			t.Fatal(err)
		}
		if len(hosts["home-a"].files) != 0 {
			t.Fatal("a dry run wrote a copy")
		}
		if got := readFile(t, declPath); got != minimalDeclaration {
			t.Fatal("a dry run changed paisans.yaml")
		}
		rec.wantLine(t, "copy the configuration", "home-a")
	})
	t.Run("execute copies and warns about the unreachable site", func(t *testing.T) {
		rec := newRecorder()
		if err := distributeConfig(rec, cfg, declPath, secPath, true, transport); err != nil {
			t.Fatal(err)
		}
		d := cfg.Deployment()
		if hosts["home-a"].files[d.ConfigCopy("paisans.yaml")] != readFile(t, declPath) {
			t.Fatal("home-a's copy is not the declaration")
		}
		rec.wantWarn(t, "home-b")
	})
}
```

Replace `newRecorder`, `wantLine`, `wantWarn`, `writeFile`, `readFile` and `mapHost` with the helpers this package already has; read `cmd/paisans/record_test.go` first, which tests `recordApplied`, a step with the same shape (reaches other sites, warns on the ones it missed).

- [ ] **Step 2: Run to see it fail**

Run: `go test ./cmd/paisans/ -run DistributeConfig -v`
Expected: FAIL: undefined.

- [ ] **Step 3: Implement**

`cmd/paisans/configcopy.go`:

```go
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// distributeConfig is every apply's last write: paisans.yaml and
// secrets.enc.yaml, copied to every site, so each holds the declaration it
// runs and can name it by revision. A declaration edited by hand since the
// newest copy is bumped first. A site that does not answer is warned about,
// never a failure: the next apply catches it up.
func distributeConfig(r ui.Reporter, cfg *config.Config, configPath, secretsPath string, execute bool, transport func(string) apply.Transport) error {
	s := r.Step("copy the configuration")
	read := func() ([]byte, []byte, error) {
		decl, err := os.ReadFile(configPath)
		if err != nil {
			return nil, nil, err
		}
		sec, err := os.ReadFile(secretsPath)
		return decl, sec, err
	}
	decl, sec, err := read()
	if err != nil {
		s.Fail(err)
		return err
	}
	sites := map[string]apply.Transport{}
	for _, name := range cfg.SiteNames() {
		sites[name] = transport(name)
	}
	plan, err := apply.PlanConfigCopy(decl, sec, sites, cfg.Deployment())
	if err != nil {
		s.Fail(err)
		return err
	}
	if !execute {
		what := "up to date on every site"
		if len(plan.Behind) > 0 {
			what = "to " + strings.Join(plan.Behind, ", ")
			if plan.Bump {
				what += ", as a new revision: paisans.yaml changed since it was last copied"
			}
		}
		s.End(ui.Pending, what)
		warnUnreachable(r, plan.Unreachable)
		return nil
	}
	if plan.Bump {
		if err := config.BumpRevision(configPath); err != nil {
			s.Fail(err)
			return err
		}
		if decl, sec, err = read(); err != nil {
			s.Fail(err)
			return err
		}
	}
	failed := apply.ExecuteConfigCopy(decl, sec, plan.Behind, sites, cfg.Deployment())
	wrote := len(plan.Behind) - len(failed)
	for name, err := range plan.Unreachable {
		failed[name] = err
	}
	s.Done(fmt.Sprintf("revision %d, %s", config.ReadRevision(decl), plural(wrote, "site")+" written"))
	warnUnreachable(r, failed)
	return nil
}

// warnUnreachable names each site the copy did not reach.
func warnUnreachable(r ui.Reporter, missed map[string]error) {
	names := make([]string, 0, len(missed))
	for name := range missed {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		r.Warn(fmt.Sprintf("%s did not get this configuration; the next apply copies it", name), missed[name].Error())
	}
}
```

In `runApply`, the secrets path: `convergeSecretsPath(*configPath, *secretsPath)`. The transport: `func(name string) apply.Transport { if name == *site { return transport }; return siteTransport(name, cfg.Sites[name], "", *sudo) }`, the same shape `checkStandby` uses at `main.go:866`.

Wire it in two places:

1. Dry run branch (`if !*execute {`, `main.go:803`), before `dryRunFound`: `_ = distributeConfig(r, cfg, *configPath, secrets, false, transportFor)`. A dry run's copy problem is reported on its line and stops nothing, as `convergeAdmins` does.
2. After `recordApplied` and before `siteApplied`: `if err := distributeConfig(r, cfg, *configPath, secrets, true, transportFor); err != nil { return err }`.

`apply` without `--site` runs `apply --site` for every site, so each step distributes; after the first, every site is up to date and the others read and write nothing.

- [ ] **Step 4: Run**

Run: `go test ./cmd/paisans/ -v -run 'DistributeConfig|Apply|Converge'`
Expected: PASS. Converge tests that assert an exact sequence of report lines now see `copy the configuration`: add it where they list a step's lines, after `record the deployment`.

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

`feat: every apply copies the declaration and secrets to every site`

---

### Task 8: documentation

**Files:**
- Modify: `README.md` (the configuration section that shows the top of `paisans.yaml`, and "Backup and restore")
- Modify: `docs/commands.md` (`apply`: the copy step; `init`: writes `metadata.id`)
- Modify: `examples/paisans.example.yaml`, `examples/home-sites.yaml` (already moved in Task 1; add a comment above `metadata:`)
- Modify: `docs/specs/2026-10-11-backups.md`, *`metadata:`*: say each write bumps, so one command may bump more than once, and add the refusal of a site's newer copy

- [ ] **Step 1: Write the docs**

The example comment:

```yaml
# metadata is what this file says about itself. version is the format, id the
# deployment's identity, written once by `paisans init`. revision counts the
# toolkit's writes of this file and secrets.enc.yaml together, and updated is
# when it last did. Do not edit revision or updated: apply bumps them itself
# when it finds a hand edit.
metadata:
  version: 1
```

`docs/commands.md`, under `apply`, one paragraph: every apply, with or without `--site`, ends by copying `paisans.yaml` and `secrets.enc.yaml` to `/srv/paisans/<token>/config/` on every site, root only, 0600, still encrypted; a site that does not answer is warned about and caught up next time; a site holding a newer revision stops the apply.

`README.md`, "Backup and restore": each site now holds the declaration and the encrypted secrets; the recovery kit section of the spec is phase 4's, so say only what exists.

- [ ] **Step 2: Check the rules**

Run: `grep -rn "—" README.md docs/commands.md examples/ docs/specs/2026-10-11-backups.md`
Expected: no output.

- [ ] **Step 3: Commit**

`docs: metadata, the revision, and where apply copies the pair`

---

## Done when

- `go test ./...` and `go vet ./...` pass.
- `paisans validate examples/paisans.example.yaml` passes, and the same file with `version: 1` at the top level is refused naming `metadata.version`.
- One pull request against `develop`, titled `feat: metadata block, revision, and apply copying the pair to every site`.

## Later phases

Each later phase gets its own plan, written when the phase before it has merged, against the code as it then is: phase 2 (the `backup:` schema and `validate`), phase 3 (`postgres-walg` and Spilo's WAL-G environment), phase 4 (the runner, `backup-plan.json`, scheduling, reporting), phase 5 (retention and copies), phase 6 (`status`, `run`, `verify`), phase 7 (`restore`, `drill`).
