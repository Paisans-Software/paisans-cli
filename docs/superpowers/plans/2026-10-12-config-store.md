# Config Store Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every site keeps every signed version of `paisans-stack.yaml`, `secrets.enc.yaml` and `.sops.yaml`, shares them with its mesh peers, and an admin can publish, pull, list and recover them, with `apply` refusing a stale base and each host refusing an older version than it last applied.

**Architecture:** A version is a git commit holding exactly the three files, signed with the admin's SSH key, its secrets carrying an encrypted digest of the other two. Sites hold a bare repository, served read-only over the mesh by `git daemon` and filled from peers by a timer; they never judge. The workstation keeps a bare cache at `.paisans/store.git`, gathers every copy it can reach, and decides trust with pure Go rules over the version graph.

**Tech Stack:** Go 1.x (module `github.com/paisans-software/paisans-stack`), system `git` (2.34 or later, for SSH signing) and `ssh-keygen` on the workstation, `git` on hosts, sops v3.13.3 in-process (already a dependency), systemd units written by `host prepare`.

**Spec:** `docs/specs/2026-10-12-config-store.md`. Read it before any task. It depends on `docs/specs/2026-10-11-config-review.md` (the grant record), so this branch is cut from `feat/config-review`.

## Global Constraints

- Follow `AGENTS.md` exactly: commit subject lowercase imperative, long "why" body wrapped at 80 columns, `Founder decision.` where the founder decided; NO em dashes anywhere; no rejected alternatives in code comments, docs or commits; refuse rather than warn where incoherent.
- Every commit message ends with exactly: `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`
- NEVER run `git stash`; use WIP commits. Never push.
- The three files of a version, exactly: `paisans-stack.yaml`, `secrets.enc.yaml`, `.sops.yaml`.
- Ref per version: `refs/versions/<commit sha>`.
- Commit message trailer: `Paisans-Revision: <n>`, n = 1 + highest parent revision; the root is revision 1.
- Secrets key: `paisans.config_digest`, value `sha256:<hex of paisans-stack.yaml> sha256:<hex of .sops.yaml>`.
- Host repository: `/var/lib/paisans/config/<token>.git`, owned by root, directories 0755, files 0644.
- Units: `paisans-<token>-config-daemon.service`, `paisans-<token>-config-sync.service`, `paisans-<token>-config-sync.timer` (every 15 minutes).
- Peers file: `/var/lib/paisans/config/<token>.peers`, one mesh address per line. Sync log: `/var/lib/paisans/config/<token>.sync.json`.
- `git daemon` binds the site's mesh address, port 9418, serves only that repository, runs as `nobody`.
- Workstation state: `.paisans/config-root`, `.paisans/config-head`, `.paisans/store.git/`, `.paisans/incoming/`, beside `paisans-stack.yaml`; `.paisans/.gitignore` ignores `store.git/` and `incoming/`.
- Typed words: `partial` (fewer than a majority of sites reached directly), `rollback` (an older revision than a host's record).
- Fetches never prune; nothing on a site is ever deleted except by `site remove`.

## Review Focus

1. A version the operator's age key cannot decrypt (published before they were a recipient): it must not invalidate the chain; only the head and fork tips need a verifying digest. Test in Task 4.
2. Two versions with identical content but different parents (a re-publish): distinct versions, no crash on duplicate trees. Test in Task 4.
3. A site whose repository is empty or missing (just prepared, or prepared before this change): gathering treats it as reached with nothing, not as an error. Test in Task 6.
4. Clock skew: a version whose commit date is older than its parent's must still order by revision, never by date. Test in Task 4.
5. `apply` from a workstation with no `.paisans/config-head` (every deployment before this change): it is the first publish, the root, and must not be refused as stale. Test in Task 8.

---

### Task 1: The digest lives in the secrets file

**Files:**
- Modify: `internal/config/secrets.go` (the `Secrets` struct, about line 32)
- Modify: `internal/config/secrets_set.go` (`Get` about line 16, `Set` about line 53)
- Create: `internal/config/secrets_bytes.go`
- Test: `internal/config/secrets_digest_test.go`

**Interfaces:**
- Produces: `Secrets.Paisans PaisansSecrets` with field `ConfigDigest string \`yaml:"config_digest,omitempty"\``; `Get("paisans.config_digest")` and `Set("paisans.config_digest", v)`; `func DecryptSecretsBytes(data []byte, id string) (*Secrets, error)` which decrypts in memory, verifies the sops MAC (returning the existing `errTampered` on mismatch) and decodes strictly.

- [ ] **Step 1: Write the failing tests**

```go
package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigDigestRoundTrips(t *testing.T) {
	s := &Secrets{Version: 1, ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
	if err := s.Set("paisans.config_digest", "sha256:aa sha256:bb"); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get("paisans.config_digest")
	if !ok || got != "sha256:aa sha256:bb" {
		t.Fatalf("Get = %q, %v", got, ok)
	}
}

func TestDecryptSecretsBytesRefusesTampering(t *testing.T) {
	dir := t.TempDir()
	path, id := writeEncryptedFixture(t, dir) // existing helper pattern in secrets_write_test.go; reuse or add
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptSecretsBytes(data, id); err != nil {
		t.Fatalf("untampered: %v", err)
	}
	tampered := flipOneCiphertextByte(t, data)
	if _, err := DecryptSecretsBytes(tampered, id); !errors.Is(err, errTampered) {
		t.Fatalf("tampered: got %v, want errTampered", err)
	}
	_ = filepath.Join
}
```

Use the encryption fixture already used by `secrets_write_test.go` (it writes a file with an in-memory keystore via `AgeKeystore = keystore.NewMemory()`); if there is no shared helper, add `writeEncryptedFixture` and `flipOneCiphertextByte` (replace one base64 character inside the first `ENC[AES256_GCM,data:` value) in the test file.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/config -run 'ConfigDigest|DecryptSecretsBytes' -v`
Expected: FAIL (unknown key, undefined `DecryptSecretsBytes`).

- [ ] **Step 3: Implement**

In `secrets.go` add to `Secrets`:

```go
	// Paisans holds what the toolkit records about the configuration itself.
	// config_digest binds a published version's paisans-stack.yaml and .sops.yaml
	// to this file: sops's MAC covers it, so only someone who can decrypt the
	// file can write a digest that verifies (docs/specs/2026-10-12-config-store.md).
	Paisans PaisansSecrets `yaml:"paisans,omitempty"`
```

```go
// PaisansSecrets is the toolkit's own section of the secrets file.
type PaisansSecrets struct {
	ConfigDigest string `yaml:"config_digest,omitempty"`
}
```

In `secrets_set.go` add a `case "paisans.config_digest":` to `Get` returning `s.Paisans.ConfigDigest, s.Paisans.ConfigDigest != ""` and to `Set` assigning it, following the existing cases' shape.

Create `secrets_bytes.go`:

```go
package config

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// DecryptSecretsBytes decrypts a secrets file held in memory, as a version in
// the configuration store holds it, and refuses one whose MAC does not verify.
func DecryptSecretsBytes(data []byte, id string) (*Secrets, error) {
	plain, err := decryptSOPS(data, id)
	if err != nil {
		return nil, err
	}
	var s Secrets
	dec := yaml.NewDecoder(bytes.NewReader(plain))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("secrets in a stored version: %w", err)
	}
	s.Encrypted = true
	return &s, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/config/... -v -run 'ConfigDigest|DecryptSecretsBytes'` then `go test ./internal/config/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat: the secrets file holds the digest of its configuration" -m "<why body per AGENTS.md>" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Computing and checking the digest

**Files:**
- Create: `internal/configstore/digest.go`
- Test: `internal/configstore/digest_test.go`

**Interfaces:**
- Produces: `func Digest(paisansYAML, sopsYAML []byte) string`; `func CheckDigest(files Files, id string) error` where `type Files struct{ Paisans, Secrets, SOPS []byte }`; errors `ErrNoDigest`, `ErrDigestMismatch`, and a wrapped decrypt error.

- [ ] **Step 1: Write the failing tests**

```go
package configstore

import (
	"errors"
	"testing"
)

func TestDigestIsStableAndOrderSensitive(t *testing.T) {
	a := Digest([]byte("x"), []byte("y"))
	if a != Digest([]byte("x"), []byte("y")) {
		t.Fatal("not stable")
	}
	if a == Digest([]byte("y"), []byte("x")) {
		t.Fatal("swapping the files must change the digest")
	}
	if want := "sha256:2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881 sha256:a1fce4363854ff888cff4b8e7875d600c2682390412a8cf79b37d0b11148b0fa"; a != want {
		t.Fatalf("Digest = %s", a)
	}
}

func TestCheckDigest(t *testing.T) {
	f := fixtureVersion(t, "paisans: 1\n", "creation_rules: []\n") // encrypts a secrets file whose digest matches
	if err := CheckDigest(f, fixtureID); err != nil {
		t.Fatalf("matching: %v", err)
	}
	f.Paisans = []byte("paisans: 2\n")
	if err := CheckDigest(f, fixtureID); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("edited paisans-stack.yaml: %v", err)
	}
	g := fixtureVersionWithoutDigest(t)
	if err := CheckDigest(g, fixtureID); !errors.Is(err, ErrNoDigest) {
		t.Fatalf("no digest: %v", err)
	}
}
```

Add `fixtureVersion`, `fixtureVersionWithoutDigest` and `fixtureID` in `internal/configstore/fixture_test.go`: generate an age identity with `filippo.io/age` (`age.GenerateX25519Identity`), point `config.AgeKeystore` at `keystore.NewMemory()` holding it, build a `config.Secrets{Version:1, ID:fixtureID}`, set `paisans.config_digest` to `Digest(...)`, and encrypt with `config.WriteSecrets` into a temp dir, reading the bytes back.

- [ ] **Step 2: Run to see it fail**

Run: `go test ./internal/configstore -run Digest -v`
Expected: FAIL (package does not exist).

- [ ] **Step 3: Implement**

```go
// Package configstore keeps signed versions of a deployment's configuration
// and decides which of them to trust (docs/specs/2026-10-12-config-store.md).
package configstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// Files are the three files of a version, as stored.
type Files struct {
	Paisans, Secrets, SOPS []byte
}

var (
	ErrNoDigest       = errors.New("the secrets file records no digest of its configuration")
	ErrDigestMismatch = errors.New("the secrets file's digest does not match paisans-stack.yaml and .sops.yaml")
)

// Digest is what a version's secrets file records about the other two files.
func Digest(paisansYAML, sopsYAML []byte) string {
	a := sha256.Sum256(paisansYAML)
	b := sha256.Sum256(sopsYAML)
	return "sha256:" + hex.EncodeToString(a[:]) + " sha256:" + hex.EncodeToString(b[:])
}

// CheckDigest decrypts the version's secrets with the operator's age key, which
// verifies sops's MAC, and compares the digest they record with the files.
func CheckDigest(f Files, id string) error {
	s, err := config.DecryptSecretsBytes(f.Secrets, id)
	if err != nil {
		return fmt.Errorf("decrypting the secrets: %w", err)
	}
	got, ok := s.Get("paisans.config_digest")
	if !ok {
		return ErrNoDigest
	}
	if got != Digest(f.Paisans, f.SOPS) {
		return ErrDigestMismatch
	}
	return nil
}
```

Recompute the expected constant in the first test by running it once and confirming it equals `sha256("x")` and `sha256("y")` hex (`printf x | shasum -a 256`); fix the literal if it differs.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/configstore -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/configstore
git commit -m "feat: a version's digest binds its configuration to its secrets" -m "<why>" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: The git layer

**Files:**
- Create: `internal/configstore/git.go`
- Test: `internal/configstore/git_test.go` (needs `git` and `ssh-keygen` on PATH; skip with `t.Skip` when absent)

**Interfaces:**
- Produces:

```go
type Repo struct{ Dir string } // a bare repository
func InitRepo(dir string) (Repo, error)                       // git init --bare, idempotent
func (r Repo) Commit(f Files, parents []string, revision int, signingKey string, when time.Time) (string, error)
func (r Repo) Versions(allowedSigners string) ([]Version, error)
func (r Repo) Files(sha string) (Files, error)
func (r Repo) Bundle() ([]byte, error)                        // every refs/versions/* ref
func (r Repo) FetchBundle(bundle []byte) error                // verify, then fetch refs/versions/*, no prune
type Version struct {
	SHA       string
	Parents   []string
	Revision  int       // from the Paisans-Revision trailer; 0 when absent or malformed
	Signed    bool      // git reports a good signature (%G? is G or U)
	SignerFP  string    // %GF, the signing key's SHA256 fingerprint
	Committed time.Time // for display only
	TreeOK    bool      // the tree holds exactly the three files
}
```

`signingKey` is passed to `git -c gpg.format=ssh -c user.signingKey=<value>`: in production a literal `key::<public key line>` so ssh-agent signs; in tests the path of a private key file made with `ssh-keygen -t ed25519 -N ""`. `allowedSigners` is the path of a file in OpenSSH allowed-signers format, one line per key: `<fingerprint> <key type> <base64>`, which `Versions` passes as `-c gpg.ssh.allowedSignersFile=`.

- [ ] **Step 1: Write the failing test**

```go
func TestCommitListBundleFetch(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	key, pub := testSSHKey(t) // ssh-keygen -t ed25519 -N "" -f <tmp>/k; returns path and .pub line
	a, _ := InitRepo(filepath.Join(t.TempDir(), "a.git"))
	f := Files{Paisans: []byte("p\n"), Secrets: []byte("s\n"), SOPS: []byte("o\n")}
	root, err := a.Commit(f, nil, 1, key, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	child, err := a.Commit(f, []string{root}, 2, key, time.Unix(1_700_000_000, 0)) // older date on purpose
	if err != nil {
		t.Fatal(err)
	}
	signers := allowedSignersFile(t, pub)
	vs, err := a.Versions(signers)
	if err != nil || len(vs) != 2 {
		t.Fatalf("Versions = %v, %v", vs, err)
	}
	byRev := map[int]Version{}
	for _, v := range vs {
		byRev[v.Revision] = v
		if !v.Signed || v.SignerFP == "" || !v.TreeOK {
			t.Fatalf("version %+v", v)
		}
	}
	if byRev[2].SHA != child || byRev[2].Parents[0] != root {
		t.Fatalf("graph %+v", byRev)
	}
	b, _ := a.Bundle()
	c, _ := InitRepo(filepath.Join(t.TempDir(), "c.git"))
	if err := c.FetchBundle(b); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Files(child)
	if string(got.Paisans) != "p\n" || string(got.SOPS) != "o\n" {
		t.Fatalf("files %+v", got)
	}
}
```

Add a second test committing a tree with a fourth file by hand (`git hash-object`, `git mktree`, `git commit-tree`, `git update-ref refs/versions/<sha>`) and asserting `TreeOK == false`.

- [ ] **Step 2: Run to see it fail**

Run: `go test ./internal/configstore -run CommitListBundleFetch -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

Run git with `exec.Command("git", append([]string{"-C", r.Dir}, args...)...)`, `cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")`, stdin from a `bytes.Reader` where needed, and errors that quote git's stderr.

- `InitRepo`: `os.MkdirAll`; if `HEAD` is missing, `git init --bare --quiet`; then `git config receive.denyDeletes true` and `git config receive.denyNonFastForwards true`.
- `Commit`: `git hash-object -w --stdin` for each file; `git mktree` with stdin lines `100644 blob <sha>\t.sops.yaml`, `100644 blob <sha>\tpaisans-stack.yaml`, `100644 blob <sha>\tsecrets.enc.yaml` (sorted by name, as mktree requires); `git -c gpg.format=ssh -c user.signingKey=<signingKey> -c user.name=paisans -c user.email=paisans@invalid commit-tree <tree> -S [-p <parent>]...` with message `"paisans configuration\n\nPaisans-Revision: <n>\n"` and env `GIT_COMMITTER_DATE`/`GIT_AUTHOR_DATE` = `when.UTC().Format(time.RFC3339)`; then `git update-ref refs/versions/<sha> <sha>`.
- `Versions`: `git -c gpg.ssh.allowedSignersFile=<path> log --no-walk=unsorted --format=%H%x00%P%x00%G?%x00%GF%x00%ct%x00%(trailers:key=Paisans-Revision,valueonly)%x1e --glob=refs/versions` ; split records on `\x1e`, fields on `\x00`; `Signed` is `G` or `U`; then for each SHA `git ls-tree --name-only <sha>` and set `TreeOK` when the names are exactly the three.
- `Files`: `git cat-file blob <sha>:<name>` for each name.
- `Bundle`: if there are no `refs/versions/*` refs return `nil, nil`; else `git bundle create <tmp> --glob=refs/versions` and read the file.
- `FetchBundle`: write to a temp file, `git bundle verify <tmp>`, then `git fetch --no-prune <tmp> 'refs/versions/*:refs/versions/*'`. Never pass `+` in the refspec: a ref named for its own hash never moves, and an update is a forgery.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/configstore -v`
Expected: PASS (or SKIP where tools are missing, which the commit body must not hide).

- [ ] **Step 5: Commit**

```bash
git add internal/configstore
git commit -m "feat: store signed configuration versions in a bare git repository" -m "<why>" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Which versions to trust, and the head

**Files:**
- Create: `internal/configstore/trust.go`
- Test: `internal/configstore/trust_test.go` (pure; no git)

**Interfaces:**
- Consumes: `Version` (Task 3), `CheckDigest` and `Files` (Task 2).
- Produces:

```go
type Graph struct {
	Versions map[string]Version
	Admins   func(sha string) (map[string]string, error) // fingerprint -> admin id, from that version's paisans-stack.yaml
	Digest   func(sha string) error                       // CheckDigest of that version
}
type Verdict struct {
	Valid   map[string]bool
	Reasons map[string]string // why a version is invalid
	Roots   []string          // valid roots
	Heads   []string          // valid tips, highest revision first
	Fork    bool              // more than one head
}
func Judge(g Graph, pinnedRoot string) (Verdict, error)
```

Rules, in this order, exactly as the spec's *Which versions the workstation trusts*:
1. `TreeOK`, `Signed`, and `Revision == 1 + max(parent revisions)` (1 for a root); otherwise invalid.
2. Signer: for a root, `SignerFP` is a key of an admin in its own `paisans-stack.yaml`; otherwise a key of an admin in every parent's `paisans-stack.yaml`. Every parent must itself be valid.
3. Removal is final: for each valid version R whose parent lists admin X and which does not, any version signed by X's key that is not an ancestor of R is invalid, reason `signed by <X> after a version removed them`.
4. Roots: when `pinnedRoot` is set, only that root and its descendants can be valid; otherwise more than one valid root returns an error listing each root's SHA, signer and date.
5. Digest: only versions that would be heads (valid, no valid child) must pass `Digest`; an ancestor whose secrets the operator cannot decrypt stays valid (Review Focus 1). A head failing the digest is invalid, and the computation repeats until stable.
6. Heads ordered by revision descending, then SHA for a stable order; never by date.

- [ ] **Step 1: Write the failing tests**

Build graphs in memory with a helper:

```go
type fake struct {
	vs      map[string]Version
	admins  map[string]map[string]string
	badDig  map[string]bool
}
func (f *fake) add(sha string, rev int, signer string, parents ...string) {
	f.vs[sha] = Version{SHA: sha, Parents: parents, Revision: rev, Signed: true, SignerFP: signer, TreeOK: true}
}
func (f *fake) graph() Graph {
	return Graph{
		Versions: f.vs,
		Admins: func(sha string) (map[string]string, error) { return f.admins[sha], nil },
		Digest: func(sha string) error {
			if f.badDig[sha] {
				return ErrDigestMismatch
			}
			return nil
		},
	}
}
```

Tests, one each, asserting `Valid`, `Heads`, `Fork` and the reason text:
- a linear chain of three, alice signing: head is the third.
- a forged root signed by mallory listing mallory, beside the real root: `Judge` errors naming both roots; with `pinnedRoot` set to the real one, the forged chain is invalid.
- a version signed by a key not in its parent's admins: invalid.
- removal is final: v2 removes bob; bob signs v3 on v1: v3 invalid with the removal reason, head is v2, `Fork` false.
- a real fork: alice and carol each sign a child of v2: `Fork` true, both heads.
- a merge with two parents resolves the fork: single head.
- a head whose digest fails is invalid and its parent becomes the head; an ancestor whose digest fails while a descendant passes stays valid (Review Focus 1).
- a child with `Revision` not equal to parent+1: invalid.
- dates: a child with an earlier `Committed` than its parent is still the head (Review Focus 4).
- two versions with identical trees and different parents: both judged independently (Review Focus 2).

- [ ] **Step 2: Run to see them fail**

Run: `go test ./internal/configstore -run Judge -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement `Judge`**

Process versions in topological order (parents before children; sort by revision then SHA, which is topological when rule 1 holds, and treat a version whose parent is missing from the map as invalid with reason `parent <sha> is not held`). Keep `valid map[string]bool`. Compute removals as a list of `(admin fingerprints, removal sha)` from each valid version whose parent's admin set contains fingerprints its own does not; ancestry by walking parents with a memoised `isAncestor(a, b)`. After rules 1 to 4, loop: compute heads (valid versions with no valid child), run `Digest` on each not yet checked, mark failures invalid, repeat until no head fails. Return heads sorted per rule 6.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/configstore -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/configstore
git commit -m "feat: decide which configuration versions to trust and which is newest" -m "<why>" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: The store on every site

**Files:**
- Modify: `internal/hostprep/ubuntu.go` (`basePackages` about line 38; add `git`, and update the probe strings in `internal/hostprep/hostprep_test.go` at about lines 128, 179 and 282)
- Create: `internal/hostprep/configstore.go`
- Create: `internal/hostprep/profiles/ubuntu-24.04/config-daemon.service.tmpl`, `config-sync.service.tmpl`, `config-sync.timer.tmpl`, `config-sync.sh.tmpl`
- Modify: `internal/hostprep/hostprep.go` (add the section to the plan the way the watchdog section is added; add a `KindConfigStore` to `kindOrder`)
- Modify: `internal/siteremove/host.go` (about lines 82-91: also remove `/var/lib/paisans/config/<token>.git`, `.peers` and `.sync.json`)
- Test: `internal/hostprep/configstore_test.go`, `internal/siteremove/host_test.go`

**Interfaces:**
- Produces: `func (u ubuntu) ConfigStore(t Transport, d deployment.Deployment, self config.Site, peers []string) (Section, error)` returning steps for: the repository (`git init --bare` when `HEAD` is absent, owner root, mode 0755), the peers file (content = peers joined by `\n`, 0644), the sync script at `/usr/local/lib/<prefix>/config-sync.sh` (0755), the three units (0644), and `systemctl daemon-reload && systemctl enable --now <prefix>-config-daemon.service <prefix>-config-sync.timer`. Each step is planned only when the host differs (read the file and compare, as the watchdog step does).

Templates (the executor fills `{{.Prefix}}`, `{{.Token}}`, `{{.Address}}`, `{{.WireGuardUnit}}`):

`config-daemon.service.tmpl`:
```ini
[Unit]
Description=paisans {{.Token}}: serve configuration versions to mesh peers, read only
After={{.WireGuardUnit}}
Requires={{.WireGuardUnit}}

[Service]
User=nobody
Group=nogroup
ExecStart=/usr/bin/git daemon --reuseaddr --listen={{.Address}} --port=9418 --base-path=/var/lib/paisans/config --export-all /var/lib/paisans/config/{{.Token}}.git
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

`config-sync.service.tmpl`:
```ini
[Unit]
Description=paisans {{.Token}}: fetch configuration versions from mesh peers
After={{.WireGuardUnit}}

[Service]
Type=oneshot
ExecStart=/usr/local/lib/{{.Prefix}}/config-sync.sh
```

`config-sync.timer.tmpl`:
```ini
[Unit]
Description=paisans {{.Token}}: fetch configuration versions every 15 minutes

[Timer]
OnBootSec=5min
OnUnitActiveSec=15min

[Install]
WantedBy=timers.target
```

`config-sync.sh.tmpl`:
```sh
#!/bin/sh
# Fetches every version a peer holds that this site lacks. Never prunes:
# a ref is named for its own hash, so nothing a peer holds can replace one here.
set -u
repo=/var/lib/paisans/config/{{.Token}}.git
log=/var/lib/paisans/config/{{.Token}}.sync.json
out=""
while IFS= read -r peer; do
	[ -n "$peer" ] || continue
	if timeout 60 git -C "$repo" fetch --no-prune --quiet "git://$peer/{{.Token}}.git" 'refs/versions/*:refs/versions/*' 2>/dev/null; then
		ok=true
	else
		ok=false
	fi
	out="$out{\"peer\":\"$peer\",\"ok\":$ok},"
done < /var/lib/paisans/config/{{.Token}}.peers
printf '{"at":"%s","peers":[%s]}\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${out%,}" > "$log.tmp" && mv "$log.tmp" "$log"
```

Peers come from the configuration at `host prepare` time: every other site's `Address`, in `cfg.SiteNames()` order. Pass them from the caller in `hostprep.Build`.

- [ ] **Step 1: Write the failing tests**

In `configstore_test.go`, with the existing fake host map used by `hostprep_test.go`:
- a fresh host plans: init the repository, write the peers file, the script and three units, enable; executing writes exactly those paths with the stated modes.
- a host already in that state plans nothing.
- a changed peer list plans only the peers file.
- the daemon unit's `ExecStart` names only this deployment's repository and binds the mesh address (assert substrings).

In `siteremove/host_test.go`: the removal command for a deployment deletes the repository and both files, and the existing unit glob still covers the new units.

- [ ] **Step 2: Run to see them fail**

Run: `go test ./internal/hostprep ./internal/siteremove -run 'ConfigStore|Config' -v`
Expected: FAIL.

- [ ] **Step 3: Implement** following `WatchdogModule` (about `ubuntu.go:302`) for templates, state reads and steps.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/hostprep/... ./internal/siteremove/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/hostprep internal/siteremove
git commit -m "feat: host prepare gives every site a configuration store and its sync" -m "<why>" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Talking to a site's store

**Files:**
- Create: `internal/configstore/remote.go`
- Test: `internal/configstore/remote_test.go`

**Interfaces:**
- Consumes: `apply.Transport` (`Run`, `ReadFile`, `WriteFile`, `RunInput`), `deployment.Deployment`.
- Produces:

```go
type SyncReport struct {
	At    time.Time
	Peers []struct{ Peer string; OK bool }
}
func Push(t apply.Transport, d deployment.Deployment, bundle []byte) error
func Pull(t apply.Transport, d deployment.Deployment, syncFirst bool) (bundle []byte, report *SyncReport, err error)
```

- `Push`: `WriteFile("/var/lib/paisans/config/<token>.incoming.bundle", string(bundle), 0o600)`, then `Run("git -C <repo> bundle verify <tmp> >/dev/null && git -C <repo> fetch --no-prune --quiet <tmp> 'refs/versions/*:refs/versions/*'; rc=$?; rm -f <tmp>; exit $rc")`. Quote every path with the existing single-quote helper pattern (`internal/grant/record.go` `quote`).
- `Pull`: when `syncFirst`, `Run("systemctl start <prefix>-config-sync.service")` (oneshot, so it returns when done; a failure is reported, not fatal); then `Run` a command that prints `__PAISANS_EMPTY__` when the repository has no `refs/versions/*` or is missing (Review Focus 3), else creates a bundle in a temp file and prints it base64 encoded on one line (`base64 -w0`), deleting the temp file; decode in Go. Read the sync log with `ReadFile` and parse it when present.

- [ ] **Step 1: Write the failing tests** with a fake transport that records commands and serves canned output (follow `apply_test.go`'s `fakeHost`): push writes the bundle then runs verify and fetch with no `+` in the refspec; pull with an empty repository returns `nil` bundle and no error; pull decodes a base64 bundle; pull with `syncFirst` starts the sync service first; a sync log is parsed into `SyncReport`.

- [ ] **Step 2: Run, see FAIL.** `go test ./internal/configstore -run 'Push|Pull' -v`

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run, see PASS.** `go test ./internal/configstore/...`

- [ ] **Step 5: Commit** `feat: push versions to a site's store and gather them back`

---

### Task 7: The workstation's store, gathering, and the `config` commands

**Files:**
- Create: `internal/configstore/local.go` (state files, cache, gathering, publishing)
- Create: `cmd/paisans/config.go` (`runConfig(args []string, stdin io.Reader, stdout io.Writer) error` with subcommands `publish`, `pull`, `log`, `recover`)
- Modify: `cmd/paisans/main.go` (dispatch `case "config":` and the `usage` text)
- Test: `internal/configstore/local_test.go`, `cmd/paisans/config_test.go`

**Interfaces:**
- Consumes: Tasks 2 to 6; `config.Load`, `config.Recipients`, `config.WriteSecrets`, `config.LoadDeploymentSecrets`; `siteTransport` (`cmd/paisans/main.go` about line 1554); `confirmWord`, `stdinIsTerminal`; `agentFingerprints` and `approver` (`cmd/paisans/review.go` about lines 46 and 60).
- Produces:

```go
type Local struct{ Dir string } // the directory holding paisans-stack.yaml
func (l Local) Root() (string, error); func (l Local) SetRoot(sha string) error
func (l Local) Head() (string, error); func (l Local) SetHead(sha string) error
func (l Local) Cache() (Repo, error) // .paisans/store.git, and .paisans/.gitignore
func (l Local) Files() (Files, error) // the three files on disk
type Reached struct{ Site string; Direct bool; Err error }
type Gathered struct {
	Verdict Verdict
	Reached []Reached
	Majority bool // direct reaches > half of the configured sites
}
func Gather(l Local, sites map[string]apply.Transport, configured int, id string) (Gathered, error)
func Publish(l Local, sites map[string]apply.Transport, signingKey string, now time.Time) (sha string, pushed []string, err error)
```

`Gather`: for each site, `Pull(t, d, true)` and `FetchBundle` into the cache; record `Reached{Direct: true}` or the error; then build allowed signers from every admin key in every held version's `paisans-stack.yaml` (write a temp file), `Versions`, and `Judge` with `Root()` as the pin, `Admins` reading `config` admin keys of that version via `config.ParseKeyLine(...).Fingerprint`, `Digest` via `CheckDigest(Files(sha), id)`. `Majority` counts only `Direct` successes.

`Publish`: refuse when `Head()` is set and is not the gathered single head (`stale: the sites hold <sha> (revision n, signed by x); run paisans config pull`), when the gathered result is a fork (name `publish --merge`), when no loaded agent key belongs to an admin listed in the parent (or, for the first version, in the local `paisans-stack.yaml`), or when no site is reached. Before committing, write the digest: load secrets, `Set("paisans.config_digest", Digest(paisans, sops))`, `WriteSecrets` with `Recipients`. Commit with `revision = parent+1` (1 for the first), push the cache's bundle to every reached site, `SetHead`, and `SetRoot` on the first. `--merge` passes every head as a parent.

Commands (all dry run unless `--execute`, as every command here):
- `paisans config publish [--merge] [--execute]`
- `paisans config pull [--execute]`: gather; with no local edits since `Head()` (compare files with `Files(Head())`), write the head's three files and `SetHead`; with edits, refuse, write the head's files to `.paisans/incoming/`, and print a unified diff of `paisans-stack.yaml` and the changed secrets keys by name only (Task 9 owns the secrets merge).
- `paisans config log`: gather without `--execute` effects; print revision, short SHA, signer admin id, date, `valid` or the reason, and which sites hold it.
- `paisans config recover --from user@host[:port]... [--root <sha>] [--execute]`: no configuration on disk. Build transports from the given destinations, gather, then from the first valid version's `paisans-stack.yaml` build transports for every site there and gather again (Review Focus 3 applies); without `Majority` ask for `partial` at a terminal (refuse without one); write the three files, `SetRoot`, `SetHead`.

Every prompt uses `confirmWord(stdin, stdout, word, what)`; without a terminal the command is refused before anything is written.

- [ ] **Step 1: Write failing tests** in `local_test.go` with real git and a fake transport whose "site" is another local bare repository (the fake runs `Push`/`Pull` commands against a temp dir by executing them with `sh -c` locally under a rewritten path; or fake `Pull`/`Push` at the function level by making `Gather` and `Publish` take `pullFunc`/`pushFunc` parameters, which is simpler: do that). Cases: first publish creates the root and state files; second publish from a stale head is refused; two admins publishing from the same head give a fork, and `--merge` resolves it; gather counts only direct reaches toward the majority; recover writes the files and state.

In `cmd/paisans/config_test.go`, through the fake terminal: `recover` with one of three sites reached asks `partial` and refuses without a terminal.

- [ ] **Step 2: Run, see FAIL.** `go test ./internal/configstore ./cmd/paisans -run 'Config|Publish|Gather|Recover' -v`

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run, see PASS.** `go test ./...`

- [ ] **Step 5: Commit** `feat: paisans config publish, pull, log and recover`

---

### Task 8: `apply` publishes what it applies, and hosts refuse to go backwards

**Files:**
- Modify: `internal/grant/record.go` (add `Config *ConfigPart`; `Version` 2; `Parse` accepts 1 and 2)
- Modify: `cmd/paisans/main.go` (`runApply` about lines 741-942), `cmd/paisans/converge.go` (about lines 769-807), `cmd/paisans/review.go` (`recordGrant` about line 160)
- Test: `internal/grant/record_test.go`, `cmd/paisans/config_apply_test.go`

**Interfaces:**
- Produces:

```go
type ConfigPart struct {
	Hash      string    `json:"hash"`
	Revision  int       `json:"revision"`
	AppliedAt time.Time `json:"applied_at"`
}
```

Behaviour:
1. Before the review in both `runApply` (with `--site`) and `runConverge`: `Gather`. A fork refuses (name `publish --merge`). A local `Head()` that is set and is not the gathered head refuses as stale (name `paisans config pull`). No `Head()` at all is a first publish and is not refused (Review Focus 5). Fewer than a majority reached asks `partial` at a terminal with `--execute`.
2. For every host the run touches, read its grant record; when `Config.Revision` is greater than the revision about to be applied, refuse unless the operator types `rollback` at a terminal; the message names the host, both revisions and both hashes.
3. After approval and before the first change: `Publish` (so the store holds the declared state even if the apply stops part way). When the local files equal the head's, publish nothing and apply the head.
4. After each host succeeds, `recordGrant` also writes `Config{Hash, Revision, AppliedAt}`.

- [ ] **Step 1: Write failing tests:** `Parse` accepts a version 1 record and a version 2 record with `config`; `apply --execute` with a stale head is refused before any host changes; with no head it publishes the root and proceeds; a host whose record has a higher revision refuses without `rollback` and proceeds after it (fake terminal); the record written after success carries the new revision.

- [ ] **Step 2: Run, see FAIL.** `go test ./internal/grant ./cmd/paisans -run 'Config|Rollback|Stale' -v`

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run, see PASS.** `go test ./...` and `go test -race ./cmd/paisans/...`

- [ ] **Step 5: Commit** `feat: apply publishes the version it applies and hosts refuse an older one`

---

### Task 9: Merging two edits of the secrets file

**Files:**
- Create: `internal/configstore/merge.go`
- Modify: `cmd/paisans/config.go` (`pull` uses it when both sides edited the secrets)
- Test: `internal/configstore/merge_test.go`

**Interfaces:**
- Produces: `func MergeSecrets(base, ours, theirs *config.Secrets) (merged *config.Secrets, conflicts []string, err error)`; conflicts are key paths only, never values.

Flatten each side to `map[path]string` using the same key paths `Get`/`Set` accept (iterate `Sites`, `Apps`, `External`, `Cluster`, `Storage`, `OIDCClients`, `PocketIDGroups`, `Paisans`; write a `flatten(*config.Secrets) map[string]string` beside it). For each path: unchanged on one side takes the other; changed identically takes it; changed differently is a conflict; removed on one side and unchanged on the other is removed. `paisans.config_digest` is never a conflict: it is recomputed at publish. Rebuild with `Set` on a copy of `theirs`.

- [ ] **Step 1: Write failing tests:** disjoint edits merge; the same key edited the same way merges; the same key edited differently is one conflict naming the path and no value appears in the error text; a removal merges; the digest never conflicts.

- [ ] **Step 2: Run, see FAIL.** `go test ./internal/configstore -run Merge -v`

- [ ] **Step 3: Implement**, and in `config pull`, when both the local and the head's secrets differ from the base's, merge: no conflicts writes the merged file (encrypted with `Recipients`) and moves `Head()`; conflicts refuse, listing the paths and naming `paisans secrets set`.

- [ ] **Step 4: Run, see PASS.** `go test ./...`

- [ ] **Step 5: Commit** `feat: config pull merges secrets edits key by key`

---

### Task 10: Documentation

**Files:**
- Modify: `README.md` (a section *Every site keeps signed copies of the configuration* after *Backup and restore*, with the reasoning from the spec; update *Backup and restore*'s table rows for `paisans-stack.yaml` and `secrets.enc.yaml` to say every site holds every version; add the store's files to the table in *site remove* of what is removed)
- Modify: `docs/commands.md` (`config publish`, `config pull`, `config log`, `config recover`, and the new `apply` refusals and words `partial` and `rollback`)
- Modify: `docs/development.md` (*Run*: the new commands; *What is not here yet*: the store has run against fakes and local repositories only)
- Modify: `docs/guides/how-a-deployment-protects-itself.md` (*Losing `paisans-stack.yaml` or the secrets file*: from "Designed, not built" to what is built; *Not built yet*: remove the item) and `docs/guides/when-an-admin-leaves.md` (re-encrypt before publishing the version that removes an admin) when those files exist on this branch; if they arrive with the eviction branch, note it in the pull request body instead.

- [ ] **Step 1:** Write the sections. Every rule gives its reason; no em dashes; cross-reference by section name.
- [ ] **Step 2:** Verify: every command example uses flags `cmd/paisans/config.go` defines (grep them); every internal link resolves; no real hostname, address or key in the diff (`git diff | grep -nE '([0-9]{1,3}\.){3}[0-9]{1,3}'` shows only documentation ranges and `10.44.` examples).
- [ ] **Step 3: Commit** `docs: every site keeps signed copies of the configuration`

---

## Self-review notes

- Spec coverage: version shape (Tasks 1 to 3), trust rules and head (4), rollback by memory, majority and witnesses (7, 8), site store, daemon, timer and cleanup (5), gathering with sync first and every site reached, majority counting direct reaches (6, 7), commands (7), secrets conflicts (9), failures (7, 8), testing (each task), docs (10).
- Interfaces used across tasks: `Files`, `Digest`, `CheckDigest` (2); `Repo`, `Version` (3); `Graph`, `Verdict`, `Judge` (4); `Push`, `Pull`, `SyncReport` (6); `Local`, `Gather`, `Publish`, `Gathered`, `Reached` (7); `ConfigPart` (8); `MergeSecrets` (9).

---

## Amendment, 2026-10-12: the anchor, and `admin register`

The spec's trust model changed (commit `0f7eff9`): trust starts from the
operator's own signature or a pinned root, and the digest is gone. Execution
order from here: Task 11, Task 12, Task 5, Task 6, Task 7, Task 13, Task 8,
Task 9, Task 10. Where Tasks 7 and 8 mention `Digest`, `CheckDigest`, a
digest written at publish, or `Graph.Digest`, those parts no longer apply:
publishing commits the three files as they are, and `Judge` takes the anchor
described below. Global Constraints: the `paisans.config_digest` line no longer
applies; add `.paisans/signing-key` (the operator's public key line) to the
workstation state.

### Task 11: Trust from an anchor

**Files:**
- Modify: `internal/configstore/trust.go`, `internal/configstore/trust_test.go`

**Interfaces:**
- Produces (replacing Task 4's `Graph`, `Judge`):

```go
type Graph struct {
	Versions map[string]Version
	Admins   func(sha string) (map[string]string, error) // fingerprint -> admin id, from that version's paisans-stack.yaml
}
type Anchor struct {
	Signer string // the operator's key fingerprint, from .paisans/signing-key; may be empty
	Root   string // a pinned root sha; may be empty
}
type Verdict struct {
	Trusted  map[string]bool
	Reasons  map[string]string
	AnchorAt string   // the version trust starts from
	Heads    []string // highest revision first, then sha
	Fork     bool
}
var ErrNoAnchor = errors.New("no version is signed by your key and no root is pinned")
type ConflictError struct{ A, B string } // two removals that bar each other outside the anchor's history
func Judge(g Graph, a Anchor) (Verdict, error)
```

Rules, exactly as the spec's *Which versions the workstation trusts*:
- The anchor: among versions passing shape (tree, `Signed`, revision) whose `SignerFP` equals `a.Signer`, the one with the highest revision, then lowest sha; else `a.Root` if it is held and is a root that passes shape and is signed by an admin it lists itself; else `ErrNoAnchor`. When both exist, the signer anchor wins only if it descends from the pinned root; otherwise `ErrNoAnchor` naming both. The anchor and all its ancestors are trusted (history), whatever their own shape.
- Extension, to a fixpoint in topological order: a version not in the anchor's history is trusted when it passes shape, every parent is trusted, its signer is a key of an admin listed in every parent, and it is not barred.
- Removal, key by key: for each trusted version R and each parent P of R, every fingerprint in `Admins(P)` that is absent from `Admins(R)` is removed by R. A version signed by a removed fingerprint that is not an ancestor of R is barred. Only trusted versions remove. Removals in the anchor's history always stand. Compute trusted set and bars together until nothing changes. If two versions outside the anchor's history would each bar the other (or a chain of such), return `*ConflictError` naming them; never pick by revision or sha.
- Heads: trusted versions with no trusted child, ordered by revision descending, then sha.

- [ ] **Step 1: Rewrite the tests**: the forged-root attack (attacker root signed by attacker key listing attacker and the real admin's key; operator anchored by their own signature on the real chain): the forged chain is not trusted; a history with no version signed by the operator and no pin gives `ErrNoAnchor`; a pin alone anchors; a signer anchor not descending from the pin is refused; the review's attack (alice's `a2` removes bob, alice signs `a3`, bob signs `b2` on `v1` removing alice, operator is alice): head `a3`, `b2` barred; the same with an operator who signed only `v1` (anchor `v1`): `*ConflictError`; key rotation: `R` replaces alice's old key with a new one, a version signed with the old key on an older parent is barred; an admin signing their own removal is trusted; carol's concurrent version on `v2` beside alice's `a3` (both admins) gives `Fork`; clock skew and duplicate trees as before; empty graph with a pin that is not held gives `ErrNoAnchor`; Judge terminates on a cycle.
- [ ] **Step 2: Run, see FAIL.** `go test ./internal/configstore -run Judge -v`
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run, see PASS.** `go test ./...`
- [ ] **Step 5: Commit** `feat: trust configuration versions from the operator's own anchor`

### Task 12: Remove the digest

**Files:**
- Delete: `internal/configstore/digest.go`, `internal/configstore/digest_test.go` (keep `fixture_test.go` only if a later test uses it; otherwise delete it too)
- Modify: `internal/config/secrets.go`, `internal/config/secrets_set.go` (remove `Paisans`/`PaisansSecrets` and the `paisans.config_digest` cases); keep `DecryptSecretsBytes` only if Task 9's secrets merge will use it (it will: keep it and its test)
- Test: existing suites

- [ ] **Step 1:** Remove the code; `go build ./... && go vet ./... && go test ./...` must pass.
- [ ] **Step 2: Commit** `refactor: drop the configuration digest from the secrets file`

### Task 13: `paisans admin register`

**Files:**
- Create: `cmd/paisans/adminregister.go`; Modify: `cmd/paisans/main.go` (dispatch `admin register` beside the existing `admin` handling, and usage)
- Modify: `internal/configstore/local.go` (`SigningKey() (string, error)`, `SetSigningKey(line string) error`, and `Attest(...)`)
- Test: `cmd/paisans/adminregister_test.go`, `internal/configstore/local_test.go`

**Interfaces:**
- Consumes: Task 7's `Local`, `Gather`, push; Task 11's `Judge`, `Anchor`; Task 3's `Repo.Commit`, `Files`.
- Produces: `func Attest(l Local, sites map[string]apply.Transport, signingKey string, now time.Time) (sha string, err error)` and the command `paisans admin register [--key <path.pub>] [--root <sha>] [--execute]`.

Behaviour, exactly as the spec's *`paisans admin register`*:
1. The key: `--key` names a `.pub` file; otherwise the ssh-agent's keys (`ssh-add -L`) that match a key under some `admins.<id>.keys` in the local `paisans-stack.yaml`. None: refuse, print the agent's public key lines, and say to add one under the operator's `admins` entry and have another admin publish it. More than one admin matched: refuse, naming them, and ask for `--key`.
2. Gather (Task 7). Anchor for this run: the pinned root if `--root` or `.paisans/config-root` is set; else the anchor rule with the chosen key; else refuse with `ErrNoAnchor`'s message.
3. The head must be single (a fork refuses, naming `config publish --merge`) and its three files must equal the local files, unless the head is the one the pinned root leads to and the local files are absent; otherwise refuse and print a diff of `paisans-stack.yaml` and the names of changed secrets keys.
4. With `--execute`: commit a version with the head's tree, parent the head, revision head+1, signed with the key (`key::<line>` so ssh-agent signs); push it to every reached site; write `.paisans/signing-key`, `.paisans/config-head`, and `.paisans/config-root` if unset. Without `--execute`: show what it would sign. Nothing on any host other than the store changes.
5. `config publish` and `apply` (Tasks 7 and 8) sign with `.paisans/signing-key` when it is set, and refuse when the agent does not hold that key.

- [ ] **Step 1: Write failing tests:** no matching key refuses and prints the agent's keys; two matching admins refuse; a head differing from local files refuses with the diff; an attestation has the head's tree, one parent, revision +1, and becomes the anchor in a later `Judge`; dry run writes nothing.
- [ ] Steps 2 to 4.
- [ ] **Step 5: Commit** `feat: paisans admin register signs an attestation of the head`
