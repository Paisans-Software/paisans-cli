# `init` makes the age key: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `paisans init` on a deployment built from scratch generates its age key, keeps it in the workstation's keystore under the deployment id (or in a file the operator names), writes the recipient to `.sops.yaml`, and every later command decrypts with it without an environment variable.

**Architecture:**
- **`internal/keystore`** (new): a `Store` interface with `Get`/`Put`, backends for the macOS Keychain (`security`) and the Secret Service (`secret-tool`), an in-memory store for tests, and `Unavailable`.
- **`internal/config`:** a package variable `AgeKeystore` that decryption reads when no `SOPS_AGE_*` variable is set, handing the key to the file's age master keys through `sops/v3/age.ParsedIdentities`. Also `WriteSOPSConfig`.
- **`cmd/paisans`:** `ensureAgeKey`, called by `runInit` between resolving the secrets path and loading the secrets, and the `--age-private-key-file` flag.

**Tech Stack:** Go, `filippo.io/age` v1.3.1, `github.com/getsops/sops/v3` v3.13.3 (both already in `go.mod`), the `security` and `secret-tool` commands, `ui.Recorder` for test assertions.

**Spec:** `docs/specs/2026-10-11-init-age-key.md`.

## Global Constraints

- **The private key is never printed**, never in an argument list, never in an environment variable, and never in an error message.
- **Keystore item:** service `paisans-age`, account the deployment id, label `paisans age key <deployment id>`, recipient in the comment (`-j`) on macOS and a `recipient` attribute in Secret Service.
- **Generation only when** the secrets file does not exist **and** no `.sops.yaml` exists beside it. Every other case behaves as today, apart from the two refusals in the spec's *Problems* table.
- **Never replace:** an existing keystore item, an existing key file or an existing `.sops.yaml`.
- **A set `SOPS_AGE_*` variable wins** over the keystore at decryption.
- **No tests touch a real keystore.** `cmd/paisans`'s `TestMain` and a new `internal/config` `TestMain` replace `config.AgeKeystore`.
- **Public repo:** no real hostnames, addresses, keys or deployment facts. Use the example UUIDs in `cmd/paisans/init_test.go` (`exampleID`, `otherID`).
- **No em dashes** in anything written: docs, comments, commits, PR text.
- **No rejected alternatives** in docs, comments, commits or PRs.
- **No migration code.**
- **Never `git stash`.** Use a WIP commit to set work aside.
- **Commits** follow `AGENTS.md` *Commits* and end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Do not commit until the human has said to proceed with implementation.
- **After every task**, `gofmt -l .` prints nothing, and `go vet ./...` and `go test ./...` pass.

## Review Focus

1. **A re-run after a refused first run** (no keystore, then a re-run with `--age-private-key-file`). Expected: the first run left no `.sops.yaml` and no secrets file, so the second generates. Pinned in Task 3, `TestInitMakesNoKeyWhen/no keystore` asserts both files are absent.
2. **`secret-tool` present but no D-Bus session**, so `store` fails with a message and exit 1. Expected: `Put` returns an error that is not `ErrExists`, and `init` refuses naming `--age-private-key-file`. Pinned in Task 1, `TestSecretServiceStoreFailure`, and Task 3, `TestInitMakesNoKeyWhen/store fails`.
3. **`--age-private-key-file` given as a relative path, or through a symlinked directory** (macOS `/var` is `/private/var`), inside the configuration's directory. Expected: refused. Pinned in Task 3, `TestInitAgePrivateKeyFileRefusals/relative path inside`.
4. **A stale keystore item while `sops/age/keys.txt` holds the right key.** Expected: decryption still succeeds, by falling back to sops' own search. Pinned in Task 2, `TestDecryptFallsBackWhenItemIsNotARecipient`.
5. **A file encrypted to two admins' recipients, with the keystore holding the second.** Expected: decrypts. Pinned in Task 2, `TestDecryptWithSecondRecipient`.

---

### Task 1: `internal/keystore`

**Files:**
- Create: `internal/keystore/keystore.go`
- Create: `internal/keystore/memory.go`
- Create: `internal/keystore/keychain.go`
- Create: `internal/keystore/secretservice.go`
- Test: `internal/keystore/keystore_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `const Service = "paisans-age"`
  - `var ErrNotFound, ErrExists, ErrUnavailable error`
  - `type Store interface { Get(id string) (string, error); Put(id, key, recipient string) error; Describe(id string) string; LookupCommand(id string) string }`
  - `func Platform() Store`
  - `type Memory struct { Items map[string]MemoryItem }`, `type MemoryItem struct { Key, Recipient string }`, `func NewMemory() *Memory`
  - `type Unavailable struct{}`

- [ ] **Step 1: Write the failing tests**

`internal/keystore/keystore_test.go`:

```go
package keystore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testID        = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"
	testKey       = "AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ"
	testRecipient = "age1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"
)

// fakeSecurity is a `security` that keeps one item in dir/item, records
// every argument list in dir/argv and every stdin in dir/stdin.
const fakeSecurity = `#!/bin/sh
echo "$*" >> "$FAKE_DIR/argv"
case "$1" in
find-generic-password)
	[ -f "$FAKE_DIR/item" ] || exit 44
	cat "$FAKE_DIR/item"; exit 0;;
-i)
	cat > "$FAKE_DIR/stdin"
	[ -f "$FAKE_DIR/item" ] && exit 45
	sed -n 's/.* -w \([^ ]*\).*/\1/p' "$FAKE_DIR/stdin" > "$FAKE_DIR/item"; exit 0;;
esac
exit 2
`

// fakeSecretTool is the same for `secret-tool`. FAKE_STORE_FAILS makes
// store fail as it does with no D-Bus session.
const fakeSecretTool = `#!/bin/sh
echo "$*" >> "$FAKE_DIR/argv"
case "$1" in
lookup)
	[ -f "$FAKE_DIR/item" ] || exit 1
	cat "$FAKE_DIR/item"; exit 0;;
store)
	if [ -n "$FAKE_STORE_FAILS" ]; then echo "Cannot autolaunch D-Bus without X11 \$DISPLAY" >&2; exit 1; fi
	cat > "$FAKE_DIR/stdin"; cp "$FAKE_DIR/stdin" "$FAKE_DIR/item"; exit 0;;
esac
exit 2
`

func fake(t *testing.T, script string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "tool")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DIR", dir)
	return bin, dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

// Each backend stores, reads back, refuses a second item for the same id and
// reports a missing one, and the key reaches it on stdin and never in argv.
func TestBackends(t *testing.T) {
	for name, script := range map[string]string{"keychain": fakeSecurity, "secret service": fakeSecretTool} {
		t.Run(name, func(t *testing.T) {
			bin, dir := fake(t, script)
			var s Store = keychain{bin: bin}
			if name == "secret service" {
				s = secretService{bin: bin}
			}
			if _, err := s.Get(testID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Get before Put = %v, want ErrNotFound", err)
			}
			if err := s.Put(testID, testKey, testRecipient); err != nil {
				t.Fatal(err)
			}
			got, err := s.Get(testID)
			if err != nil || got != testKey {
				t.Fatalf("Get = %q, %v, want the stored key", got, err)
			}
			if err := s.Put(testID, testKey, testRecipient); !errors.Is(err, ErrExists) {
				t.Errorf("second Put = %v, want ErrExists", err)
			}
			if argv := read(t, filepath.Join(dir, "argv")); strings.Contains(argv, testKey) {
				t.Errorf("the key was in an argument list:\n%s", argv)
			}
			if stdin := read(t, filepath.Join(dir, "stdin")); !strings.Contains(stdin, testKey) {
				t.Errorf("the key did not arrive on stdin: %q", stdin)
			}
			if argv := read(t, filepath.Join(dir, "argv")); !strings.Contains(argv, Service) || !strings.Contains(argv+read(t, filepath.Join(dir, "stdin")), testRecipient) {
				t.Errorf("the item does not name the service and recipient:\n%s", argv)
			}
		})
	}
}

// A store that fails for want of a session is an error, not ErrExists, and
// its message never carries the key.
func TestSecretServiceStoreFailure(t *testing.T) {
	bin, _ := fake(t, fakeSecretTool)
	t.Setenv("FAKE_STORE_FAILS", "1")
	err := secretService{bin: bin}.Put(testID, testKey, testRecipient)
	if err == nil || errors.Is(err, ErrExists) {
		t.Fatalf("Put = %v, want a failure", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Error("the error carries the key")
	}
}

// Anything but a UUID, an age key and an age recipient is refused before a
// command runs, since the keychain's command line is built from them.
func TestPutRefusesMalformedValues(t *testing.T) {
	bin, dir := fake(t, fakeSecurity)
	s := keychain{bin: bin}
	for _, c := range [][3]string{
		{"not-a-uuid", testKey, testRecipient},
		{testID, "AGE-SECRET-KEY-1 -w x", testRecipient},
		{testID, testKey, `age1q" -l x`},
	} {
		if err := s.Put(c[0], c[1], c[2]); err == nil {
			t.Errorf("Put(%q, %q, %q) was accepted", c[0], c[1], c[2])
		}
	}
	if argv := read(t, filepath.Join(dir, "argv")); argv != "" {
		t.Errorf("a command ran:\n%s", argv)
	}
}

func TestMemoryAndUnavailable(t *testing.T) {
	m := NewMemory()
	if _, err := m.Get(testID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get = %v, want ErrNotFound", err)
	}
	if err := m.Put(testID, testKey, testRecipient); err != nil {
		t.Fatal(err)
	}
	if err := m.Put(testID, testKey, testRecipient); !errors.Is(err, ErrExists) {
		t.Errorf("second Put = %v, want ErrExists", err)
	}
	if m.Items[testID] != (MemoryItem{Key: testKey, Recipient: testRecipient}) {
		t.Errorf("Items = %v", m.Items)
	}
	var u Unavailable
	if _, err := u.Get(testID); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Unavailable.Get = %v", err)
	}
	if err := u.Put(testID, testKey, testRecipient); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Unavailable.Put = %v", err)
	}
}

func TestLookupCommands(t *testing.T) {
	if got, want := (keychain{bin: "/usr/bin/security"}).LookupCommand(testID), "security find-generic-password -s paisans-age -a "+testID+" -w"; got != want {
		t.Errorf("keychain = %q, want %q", got, want)
	}
	if got, want := (secretService{bin: "/usr/bin/secret-tool"}).LookupCommand(testID), "secret-tool lookup service paisans-age account "+testID; got != want {
		t.Errorf("secret service = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/keystore/`
Expected: FAIL, the package has no non-test files (`undefined: keychain`, and so on).

- [ ] **Step 3: Write `keystore.go`**

```go
// Package keystore keeps a deployment's age private key in the workstation's
// own secret store: the macOS Keychain through `security`, or the Secret
// Service through `secret-tool`. An item is named for the deployment id, so
// the configuration, the secrets file and the key are tied by one value and
// several deployments on one workstation never share an item.
//
// The key reaches each command on stdin and never in its arguments, where
// `ps` shows it to every user on the machine. README, "Rendered
// configuration is a build artifact".
package keystore

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
)

// Service is the service name every item is stored under.
const Service = "paisans-age"

var (
	// ErrNotFound is an id with no item.
	ErrNotFound = errors.New("no keystore item")
	// ErrExists is a Put for an id that already has an item. An item is
	// never replaced: something may be encrypted to the key in it.
	ErrExists = errors.New("a keystore item already exists")
	// ErrUnavailable is a workstation with no keystore paisans can use.
	ErrUnavailable = errors.New("no keystore is available")
)

// Store is a keystore. Tests replace it.
type Store interface {
	// Get returns the key stored for the deployment id, or ErrNotFound.
	Get(id string) (string, error)
	// Put stores key for id with its public recipient beside it, or returns
	// ErrExists and changes nothing.
	Put(id, key, recipient string) error
	// Describe names the item for id in an operator's words.
	Describe(id string) string
	// LookupCommand is a command line that prints the key for id, for
	// SOPS_AGE_KEY_CMD, or "" when there is none.
	LookupCommand(id string) string
}

// Platform is the keystore this workstation has, or Unavailable.
func Platform() Store {
	switch runtime.GOOS {
	case "darwin":
		if bin, err := exec.LookPath("security"); err == nil {
			return keychain{bin: bin}
		}
	case "linux":
		if bin, err := exec.LookPath("secret-tool"); err == nil {
			return secretService{bin: bin}
		}
	}
	return Unavailable{}
}

var (
	idPattern        = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	keyPattern       = regexp.MustCompile(`^AGE-SECRET-KEY-1[0-9A-Z]+$`)
	recipientPattern = regexp.MustCompile(`^age1[0-9a-z]+$`)
)

// checkID refuses anything but a deployment id, since it reaches a command.
func checkID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("keystore: %q is not a deployment id", id)
	}
	return nil
}

// checkItem refuses a malformed id, key or recipient before any command
// runs. The keychain's store is a command line written to stdin, so a value
// with a space or a quote in it would change what that line does.
func checkItem(id, key, recipient string) error {
	if err := checkID(id); err != nil {
		return err
	}
	if !keyPattern.MatchString(key) {
		return errors.New("keystore: the key is not an age private key")
	}
	if !recipientPattern.MatchString(recipient) {
		return fmt.Errorf("keystore: %q is not an age recipient", recipient)
	}
	return nil
}

func label(id string) string { return "paisans age key " + id }

// Unavailable is a workstation with no keystore.
type Unavailable struct{}

func (Unavailable) Get(string) (string, error)        { return "", ErrUnavailable }
func (Unavailable) Put(string, string, string) error  { return ErrUnavailable }
func (Unavailable) Describe(id string) string         { return "no keystore" }
func (Unavailable) LookupCommand(string) string       { return "" }
```

- [ ] **Step 4: Write `memory.go`**

```go
package keystore

import "sync"

// Memory is a keystore held in memory, for tests.
type Memory struct {
	mu    sync.Mutex
	Items map[string]MemoryItem
}

// MemoryItem is one stored key and its recipient.
type MemoryItem struct{ Key, Recipient string }

func NewMemory() *Memory { return &Memory{Items: map[string]MemoryItem{}} }

func (m *Memory) Get(id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.Items[id]
	if !ok {
		return "", ErrNotFound
	}
	return item.Key, nil
}

func (m *Memory) Put(id, key, recipient string) error {
	if err := checkItem(id, key, recipient); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.Items[id]; ok {
		return ErrExists
	}
	m.Items[id] = MemoryItem{Key: key, Recipient: recipient}
	return nil
}

func (m *Memory) Describe(id string) string   { return "keystore item " + Service + "/" + id }
func (m *Memory) LookupCommand(string) string { return "" }
```

- [ ] **Step 5: Write `keychain.go`**

```go
package keystore

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// keychain is the macOS Keychain, through the `security` command. Exit
// status 44 is errSecItemNotFound and 45 is errSecDuplicateItem.
type keychain struct{ bin string }

func (k keychain) Get(id string) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	var out, stderr bytes.Buffer
	cmd := exec.Command(k.bin, "find-generic-password", "-s", Service, "-a", id, "-w")
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	if exitCode(err) == 44 {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("security find-generic-password: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// Put writes add-generic-password to `security -i` on stdin, so the key is
// never in an argument list. The comment (-j) carries the recipient, so an
// item can be matched to a .sops.yaml without decrypting anything.
func (k keychain) Put(id, key, recipient string) error {
	if err := checkItem(id, key, recipient); err != nil {
		return err
	}
	if _, err := k.Get(id); err == nil {
		return ErrExists
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	line := fmt.Sprintf("add-generic-password -s %s -a %s -l \"%s\" -j %s -w %s\n", Service, id, label(id), recipient, key)
	var stderr bytes.Buffer
	cmd := exec.Command(k.bin, "-i")
	cmd.Stdin, cmd.Stderr = strings.NewReader(line), &stderr
	err := cmd.Run()
	if exitCode(err) == 45 {
		return ErrExists
	}
	if err != nil {
		return fmt.Errorf("security add-generic-password: %w: %s", err, redact(stderr.String(), key))
	}
	return nil
}

func (keychain) Describe(id string) string { return "Keychain item " + Service + "/" + id }

func (keychain) LookupCommand(id string) string {
	return "security find-generic-password -s " + Service + " -a " + id + " -w"
}

// exitCode is err's exit status, or -1 when it is not an exit.
func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// redact removes key from a command's own message before it becomes an
// error, in case the command echoes what it was given.
func redact(message, key string) string {
	return strings.TrimSpace(strings.ReplaceAll(message, key, "<key>"))
}
```

- [ ] **Step 6: Write `secretservice.go`**

```go
package keystore

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// secretService is the freedesktop Secret Service, through `secret-tool`.
// lookup exits 1 with nothing on stderr for a missing item; a failure, Eg:
// no D-Bus session, exits 1 with a message.
type secretService struct{ bin string }

func (s secretService) Get(id string) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	var out, stderr bytes.Buffer
	cmd := exec.Command(s.bin, "lookup", "service", Service, "account", id)
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	if exitCode(err) == 1 && strings.TrimSpace(stderr.String()) == "" {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("secret-tool lookup: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// Put reads the key from stdin, which `secret-tool store` takes as the
// secret. store replaces an item silently, so the item is looked for first.
func (s secretService) Put(id, key, recipient string) error {
	if err := checkItem(id, key, recipient); err != nil {
		return err
	}
	if _, err := s.Get(id); err == nil {
		return ErrExists
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	var stderr bytes.Buffer
	cmd := exec.Command(s.bin, "store", "--label="+label(id), "service", Service, "account", id, "recipient", recipient)
	cmd.Stdin, cmd.Stderr = strings.NewReader(key), &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("secret-tool store: %w: %s", err, redact(stderr.String(), key))
	}
	return nil
}

func (secretService) Describe(id string) string {
	return "Secret Service item service=" + Service + " account=" + id
}

func (secretService) LookupCommand(id string) string {
	return "secret-tool lookup service " + Service + " account " + id
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/keystore/ && go vet ./internal/keystore/`
Expected: PASS.

- [ ] **Step 8: Verify the real `security` against a throwaway keychain**

The fakes encode what `security` does; check it once for real without touching the login keychain. Run in a scratch directory:

```sh
K=$PWD/probe.keychain-db
security create-keychain -p probe "$K"
printf 'add-generic-password -s paisans-age -a f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01 -l "paisans age key f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01" -j age1probe -w AGE-SECRET-KEY-1PROBE %s\n' "$K" | security -i; echo "store exit $?"
security find-generic-password -s paisans-age -a f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01 -w "$K"
security find-generic-password -s paisans-age -a 0c1d2e3f-4a5b-4c6d-8e7f-8091a2b3c4d5 -w "$K"; echo "missing exit $?"
security delete-keychain "$K"
```

Expected: `store exit 0`, the key printed, `missing exit 44`. On Linux with a desktop session, confirm `secret-tool lookup service paisans-age account <unused id>` exits 1 with empty stderr; if it does not, adjust `secretService.Get` and the fake to match and say so in the commit body.

- [ ] **Step 9: Commit**

```bash
git add internal/keystore
git commit -F - <<'EOF'
feat: keystore keeps a deployment's age key under its id

A package for init and decryption to keep the age private key in the
workstation's own secret store: the macOS Keychain through security, the
Secret Service through secret-tool. The item is named for the deployment id,
and the key reaches each command on stdin, never in argv where ps shows it.
An item is never replaced, since something may be encrypted to its key.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

---

### Task 2: decryption finds the key in the keystore

**Files:**
- Modify: `internal/config/secrets.go` (`LoadSecrets`, `LoadDeploymentSecrets`, `decryptProblem`, `decryptSOPS`)
- Modify: `internal/config/secrets_write.go` (add `WriteSOPSConfig`; update the `WriteSecrets` comment)
- Create: `internal/config/main_test.go`
- Test: `internal/config/keystore_test.go`

**Interfaces:**
- Consumes: `keystore.Store`, `keystore.ErrNotFound`, `keystore.ErrUnavailable`, `keystore.NewMemory`, `keystore.Unavailable`, `keystore.Platform` (Task 1).
- Produces:
  - `var config.AgeKeystore keystore.Store`
  - `func config.WriteSOPSConfig(dir, recipient string) error`
  - `LoadSecrets(path)` keeps its signature and ignores the keystore; `LoadDeploymentSecrets(path, id)` keeps its signature and uses it.

- [ ] **Step 1: Write the failing tests**

`internal/config/main_test.go`:

```go
package config

import (
	"os"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/keystore"
)

// TestMain keeps every test away from the workstation's real keystore. A
// test that needs one sets AgeKeystore itself.
func TestMain(m *testing.M) {
	AgeKeystore = keystore.Unavailable{}
	os.Exit(m.Run())
}
```

`internal/config/keystore_test.go`:

```go
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/paisans-software/paisans-stack/internal/keystore"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

const testID = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"

// noAgeVariables leaves sops nothing but the keystore: no variable, and a
// home and config directory with no key in them.
func noAgeVariables(t *testing.T) string {
	t.Helper()
	empty := t.TempDir()
	t.Setenv("HOME", empty)
	t.Setenv("XDG_CONFIG_HOME", empty)
	for _, name := range ageVariables {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	return empty
}

func withStore(t *testing.T, s keystore.Store) {
	t.Helper()
	saved := AgeKeystore
	AgeKeystore = s
	t.Cleanup(func() { AgeKeystore = saved })
}

func identity(t *testing.T) *age.X25519Identity {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// encryptedTo writes a secrets file for testID encrypted to recipients.
func encryptedTo(t *testing.T, recipients ...*age.X25519Identity) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.enc.yaml")
	var names []string
	for _, r := range recipients {
		names = append(names, r.Recipient().String())
	}
	if err := WriteSecrets(path, &Secrets{Version: SecretsVersion, ID: testID}, names); err != nil {
		t.Fatal(err)
	}
	return path
}

func store(t *testing.T, key *age.X25519Identity) *keystore.Memory {
	t.Helper()
	m := keystore.NewMemory()
	if err := m.Put(testID, key.String(), key.Recipient().String()); err != nil {
		t.Fatal(err)
	}
	withStore(t, m)
	return m
}

func TestDecryptsWithKeystoreItem(t *testing.T) {
	noAgeVariables(t)
	key := identity(t)
	path := encryptedTo(t, key)
	store(t, key)
	s, err := LoadDeploymentSecrets(path, testID)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Encrypted || s.ID != testID {
		t.Errorf("loaded %+v", s)
	}
}

func TestDecryptWithSecondRecipient(t *testing.T) {
	noAgeVariables(t)
	first, second := identity(t), identity(t)
	path := encryptedTo(t, first, second)
	store(t, second)
	if _, err := LoadDeploymentSecrets(path, testID); err != nil {
		t.Fatal(err)
	}
}

// A variable the operator set is used alone: here it names a key that opens
// nothing, so the right key in the keystore must not rescue it.
func TestVariableWinsOverItem(t *testing.T) {
	noAgeVariables(t)
	key, other := identity(t), identity(t)
	path := encryptedTo(t, key)
	store(t, key)
	keyFile := filepath.Join(t.TempDir(), "key.txt")
	if err := os.WriteFile(keyFile, []byte(other.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", keyFile)
	if _, err := LoadDeploymentSecrets(path, testID); err == nil {
		t.Fatal("the keystore was read although SOPS_AGE_KEY_FILE was set")
	}
}

// LoadSecrets has no deployment id and never reads the keystore.
func TestLoadSecretsIgnoresKeystore(t *testing.T) {
	noAgeVariables(t)
	key := identity(t)
	path := encryptedTo(t, key)
	store(t, key)
	if _, err := LoadSecrets(path); err == nil {
		t.Fatal("LoadSecrets decrypted with the keystore")
	}
}

func TestNoItemNamesTheItemLookedFor(t *testing.T) {
	noAgeVariables(t)
	path := encryptedTo(t, identity(t))
	withStore(t, keystore.NewMemory())
	_, err := LoadDeploymentSecrets(path, testID)
	var p *ui.Problem
	if !errors.As(err, &p) || !strings.Contains(p.Hint, "no usable age key") || !strings.Contains(p.Explain, keystore.Service+"/"+testID) {
		t.Fatalf("error = %#v, want the no key problem naming the item", err)
	}
}

func TestItemThatIsNotARecipient(t *testing.T) {
	noAgeVariables(t)
	path := encryptedTo(t, identity(t))
	store(t, identity(t))
	_, err := LoadDeploymentSecrets(path, testID)
	var p *ui.Problem
	if !errors.As(err, &p) || !strings.Contains(p.Hint, "is not a recipient of secrets.enc.yaml") {
		t.Fatalf("error = %#v, want the recipient mismatch problem", err)
	}
	if len(p.Hint) > 80 {
		t.Errorf("hint is %d characters: %q", len(p.Hint), p.Hint)
	}
}

// A stale item does not stop a key sops finds on its own, here in
// sops/age/keys.txt under the config directory.
func TestDecryptFallsBackWhenItemIsNotARecipient(t *testing.T) {
	configDir := noAgeVariables(t)
	key := identity(t)
	path := encryptedTo(t, key)
	store(t, identity(t))
	keys := filepath.Join(configDir, "sops", "age", "keys.txt")
	if err := os.MkdirAll(filepath.Dir(keys), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keys, []byte(key.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDeploymentSecrets(path, testID); err != nil {
		t.Fatal(err)
	}
}

func TestWriteSOPSConfig(t *testing.T) {
	dir := t.TempDir()
	key := identity(t)
	if err := WriteSOPSConfig(dir, key.Recipient().String()); err != nil {
		t.Fatal(err)
	}
	got, err := Recipients(dir)
	if err != nil || len(got) != 1 || got[0] != key.Recipient().String() {
		t.Fatalf("Recipients = %v, %v", got, err)
	}
	if err := WriteSOPSConfig(dir, identity(t).Recipient().String()); err == nil {
		t.Error("an existing .sops.yaml was replaced")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/`
Expected: FAIL to compile: `undefined: AgeKeystore`, `undefined: ageVariables`, `undefined: WriteSOPSConfig`.

- [ ] **Step 3: Implement**

In `internal/config/secrets.go`, add imports `sopsage "github.com/getsops/sops/v3/age"` and `"github.com/paisans-software/paisans-stack/internal/keystore"`, then:

```go
// AgeKeystore is where decryption looks for a deployment's age key when no
// SOPS_AGE_* variable names one, and where init keeps the key it makes.
// Tests replace it.
var AgeKeystore keystore.Store = keystore.Platform()

// ageVariables are the variables through which an operator names an age key
// to sops. Any one of them set means the operator chose, and the keystore is
// not read.
var ageVariables = []string{"SOPS_AGE_KEY", "SOPS_AGE_KEY_FILE", "SOPS_AGE_KEY_CMD", "SOPS_AGE_SSH_PRIVATE_KEY_FILE", "SOPS_AGE_SSH_PRIVATE_KEY_CMD"}

func ageVariableSet() bool {
	for _, name := range ageVariables {
		if os.Getenv(name) != "" {
			return true
		}
	}
	return false
}
```

Replace `LoadSecrets` with a wrapper and move its body into `loadSecrets`:

```go
func LoadSecrets(path string) (*Secrets, error) { return loadSecrets(path, "") }

// loadSecrets is LoadSecrets for the deployment id, whose key decryption
// looks for in the keystore. "" looks nowhere but where sops does.
func loadSecrets(path, id string) (*Secrets, error) {
	// ... the existing body of LoadSecrets, with these two lines changed:
	//     plain, err = decryptSOPS(data, id)
	//     return nil, decryptProblem(path, err)
}
```

Keep `LoadSecrets`'s existing doc comment above the wrapper. In `LoadDeploymentSecrets`, change `s, err := LoadSecrets(path)` to `s, err := loadSecrets(path, id)`.

Replace the `GetDataKey` call in `decryptSOPS`, and give it the id:

```go
func decryptSOPS(data []byte, id string) ([]byte, error) {
	store := &sopsyaml.Store{}
	tree, err := store.LoadEncryptedFile(data)
	if err != nil {
		return nil, err
	}
	key, err := dataKey(&tree.Metadata, id)
	if err != nil {
		return nil, err
	}
	// ... unchanged from cipher := aes.NewCipher()
}

// dataKey opens the file's data key. With an id and no variable set, the
// key in the keystore item for id is tried first, in memory: nothing is set
// in the environment, so no child paisans starts can inherit it. sops'
// own search runs when there is no item, and when the item's key is not a
// recipient, so a stale item never hides a key sops would find.
func dataKey(m *sops.Metadata, id string) ([]byte, error) {
	if id == "" || ageVariableSet() {
		return sopsDataKey(m)
	}
	identity, err := AgeKeystore.Get(id)
	if errors.Is(err, keystore.ErrUnavailable) {
		return sopsDataKey(m)
	}
	if errors.Is(err, keystore.ErrNotFound) {
		key, err := sopsDataKey(m)
		if errors.Is(err, errNoKey) {
			return nil, &keystoreMiss{item: AgeKeystore.Describe(id), err: err}
		}
		return key, err
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", AgeKeystore.Describe(id), err)
	}
	if key, ok := keystoreDataKey(m, identity); ok {
		return key, nil
	}
	key, err := sopsDataKey(m)
	if errors.Is(err, errNoKey) {
		return nil, &notRecipient{id: id, item: AgeKeystore.Describe(id), err: err}
	}
	return key, err
}

func sopsDataKey(m *sops.Metadata) ([]byte, error) {
	key, err := m.GetDataKey()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNoKey, err)
	}
	return key, nil
}

// keystoreDataKey tries identity against each age recipient of a file with
// one key group, which is every file the toolkit writes. A file with more
// groups splits its data key between them, and is left to sops.
func keystoreDataKey(m *sops.Metadata, identity string) ([]byte, bool) {
	if len(m.KeyGroups) != 1 {
		return nil, false
	}
	var ids sopsage.ParsedIdentities
	if err := ids.Import(identity); err != nil {
		return nil, false
	}
	for _, k := range m.KeyGroups[0] {
		mk, ok := k.(*sopsage.MasterKey)
		if !ok {
			continue
		}
		c := *mk
		ids.ApplyToMasterKey(&c)
		if key, err := c.Decrypt(); err == nil {
			return key, true
		}
	}
	return nil, false
}

// keystoreMiss is no usable key, with no keystore item for the deployment.
type keystoreMiss struct {
	item string
	err  error
}

func (e *keystoreMiss) Error() string { return e.err.Error() }
func (e *keystoreMiss) Unwrap() error { return e.err }

// notRecipient is a keystore item whose key opens none of the file's
// recipients, and no other key sops could find.
type notRecipient struct {
	id, item string
	err      error
}

func (e *notRecipient) Error() string { return e.err.Error() }
func (e *notRecipient) Unwrap() error { return e.err }
```

In `decryptProblem`, add two cases **before** `case errors.Is(err, errNoKey):`, and give the no key explanation a name so the miss case can extend it:

```go
	const noKey = "Set SOPS_AGE_KEY_CMD to a command that prints your key, Eg: a Keychain lookup, so the key is never stored in a file or the environment. SOPS_AGE_KEY_FILE (its path), SOPS_AGE_KEY (the key itself) and sops/age/keys.txt in your config directory are read too. Your public key must be a recipient in .sops.yaml; if it was added recently, run sops updatekeys on the file."
	var miss *keystoreMiss
	var stale *notRecipient
	switch {
	case errors.As(err, &stale):
		return &ui.Problem{
			Hint:    fmt.Sprintf("the keystore key for deployment %s… is not a recipient of %s", deployment.Deployment{ID: stale.id}.Token(), name),
			Explain: fmt.Sprintf("%s holds a key that opens none of the recipients in %s, and no other key was found. Either the item is from another key, or .sops.yaml changed after the file was encrypted. Add the item's recipient to .sops.yaml and run sops updatekeys %s, or set SOPS_AGE_KEY_CMD to the right key.", stale.item, name, name),
			Cause:   cause,
		}
	case errors.As(err, &miss):
		return &ui.Problem{
			Hint:    name + " cannot be decrypted: no usable age key was found",
			Explain: noKey + " paisans also looked for " + miss.item + " and found none.",
			Cause:   cause,
		}
	case errors.Is(err, errNoKey):
		return &ui.Problem{Hint: name + " cannot be decrypted: no usable age key was found", Explain: noKey, Cause: cause}
	// ... errTampered case unchanged
	}
```

Extend `decryptProblem`'s doc comment with one sentence: "With a deployment id, paisans reads the keystore item for it first; see dataKey."

In `internal/config/secrets_write.go`, add:

```go
// WriteSOPSConfig writes a .sops.yaml into dir naming recipient, the one init
// writes for a key it made. An existing file is never replaced: once there,
// it is the operator's, and may name other admins.
func WriteSOPSConfig(dir, recipient string) error {
	path := filepath.Join(dir, SOPSConfigName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := f.WriteString("creation_rules:\n  - age: " + recipient + "\n"); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return f.Close()
}
```

Change the `WriteSecrets` comment's rationale paragraph to:

```go
// With no recipients the file is written in plaintext and the caller is
// expected to say so loudly. That is not a convenience for real deployments:
// init makes a key for a deployment built from scratch. It exists because
// fixtures must not require an age key, and because refusing to write
// anything at all would leave an operator with generated secrets that went
// nowhere.
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/config/ && go vet ./internal/config/`
Expected: PASS. If `TestDecryptFallsBackWhenItemIsNotARecipient` fails on macOS, the embedded sops is not reading `XDG_CONFIG_HOME` there: check `getUserConfigDir` in `$(go env GOMODCACHE)/github.com/getsops/sops/v3@v3.13.3/age/keysource.go` and point the test at the directory it does read.

- [ ] **Step 5: Run the whole suite**

Run: `go test ./...`
Expected: PASS. `cmd/paisans` has not changed yet, and `config.AgeKeystore` there is the real platform store, so a failure here means a test reached the keystore: stop and report it rather than continuing to Task 3.

- [ ] **Step 6: Commit**

```bash
git add internal/config
git commit -F - <<'EOF'
feat: decryption reads the deployment's age key from the keystore

LoadDeploymentSecrets now looks for the keystore item named for the
deployment id when no SOPS_AGE_* variable is set, and hands its key to the
file's age recipients in memory. Nothing is set in the environment, so no
child paisans starts inherits the key. A variable the operator set still
wins, and sops' own search still runs when there is no item or the item's
key is not a recipient, which now has a problem of its own.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

---

### Task 3: `init` makes the key

**Files:**
- Create: `cmd/paisans/agekey.go`
- Modify: `cmd/paisans/main.go` (`runInit`: the flag and the call)
- Modify: `cmd/paisans/nodns_test.go` (`TestMain`)
- Modify: `cmd/paisans/init_test.go` (`TestInitReportsStepsAndWhatIsOwed`, `TestInitWarnsAboutADroppedSite`)
- Test: `cmd/paisans/agekey_test.go`

**Interfaces:**
- Consumes: `config.AgeKeystore`, `config.WriteSOPSConfig`, `config.Recipients`, `config.SOPSConfigName` (Task 2); `keystore.ErrExists`, `keystore.NewMemory`, `keystore.Unavailable`, `keystore.Memory` (Task 1).
- Produces: `func ensureAgeKey(r ui.Reporter, id, configPath, secretsPath, keyFile string) error`; flag `--age-private-key-file` on `init`.

- [ ] **Step 1: Keep the package's tests away from the real keystore**

In `cmd/paisans/nodns_test.go`'s `TestMain`, before `os.Exit`, add:

```go
	// No test reaches the workstation's keystore: init makes and reads age
	// keys in memory. A test about the keystore sets its own.
	config.AgeKeystore = keystore.NewMemory()
```

and import `"github.com/paisans-software/paisans-stack/internal/keystore"`. Extend the comment above `TestMain` with "The same goes for the keystore init keeps age keys in."

- [ ] **Step 2: Write the failing tests**

`cmd/paisans/agekey_test.go`:

```go
package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/keystore"
)

func withKeystore(t *testing.T, s keystore.Store) {
	t.Helper()
	saved := config.AgeKeystore
	config.AgeKeystore = s
	t.Cleanup(func() { config.AgeKeystore = saved })
}

// runInitShowing runs init and returns its error and what reached stdout.
func runInitShowing(t *testing.T, args ...string) (error, string) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runInit(args) })
	return err, out
}

// lyingStore stores, then reads back a different key.
type lyingStore struct{ *keystore.Memory }

func (lyingStore) Get(string) (string, error) {
	return "AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ", nil
}

// failingStore fails to store, as secret-tool does with no D-Bus session.
type failingStore struct{ keystore.Unavailable }

func (failingStore) Put(string, string, string) error {
	return errors.New("secret-tool store: exit status 1: Cannot autolaunch D-Bus without X11 $DISPLAY")
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// From scratch, init makes a key, keeps it under the deployment id, names
// its recipient in .sops.yaml, encrypts to it, and never shows the key.
func TestInitMakesAgeKey(t *testing.T) {
	withoutAgeKey(t)
	ks := keystore.NewMemory()
	withKeystore(t, ks)
	path, rec := initWorld(t, nil, fakeSites(), nil)
	err, out := runInitShowing(t, "--config", path, "--sudo=false")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	item, ok := ks.Items[cfg.ID]
	if !ok {
		t.Fatalf("no item for %s: %v", cfg.ID, ks.Items)
	}
	identity, err := age.ParseX25519Identity(item.Key)
	if err != nil || identity.Recipient().String() != item.Recipient {
		t.Fatalf("item %v does not hold its recipient's key: %v", item, err)
	}
	dir := filepath.Dir(path)
	if got, _ := config.Recipients(dir); len(got) != 1 || got[0] != item.Recipient {
		t.Errorf(".sops.yaml names %v, want %s", got, item.Recipient)
	}
	secrets, err := config.LoadDeploymentSecrets(filepath.Join(dir, "secrets.enc.yaml"), cfg.ID)
	if err != nil || !secrets.Encrypted {
		t.Fatalf("secrets = %+v, %v, want encrypted and readable", secrets, err)
	}
	if !rec.Has("done", "make age key") || !rec.Has("warn", "only copy of the age key") || rec.Has("warn", "was written in plaintext") {
		t.Errorf("unexpected report:\n%s", rec.Lines())
	}
	if strings.Contains(rec.Lines()+out, "AGE-SECRET-KEY") {
		t.Error("the private key was shown")
	}
	if !strings.Contains(rec.Lines()+out, item.Recipient) {
		t.Error("the recipient was not shown")
	}
}

func TestInitMakesNoKeyWhen(t *testing.T) {
	other := func(t *testing.T) string {
		id, err := age.GenerateX25519Identity()
		if err != nil {
			t.Fatal(err)
		}
		return id.Recipient().String()
	}
	for _, c := range []struct {
		name  string
		store keystore.Store
		setup func(t *testing.T, dir, id string, ks keystore.Store)
		// refusal is part of the error init returns, or "" for success.
		refusal string
		// sopsAfter is whether .sops.yaml exists afterwards.
		sopsAfter, secretsAfter bool
	}{
		{name: "a recipient is named", setup: func(t *testing.T, dir, _ string, _ keystore.Store) {
			if err := config.WriteSOPSConfig(dir, other(t)); err != nil {
				t.Fatal(err)
			}
		}, sopsAfter: true, secretsAfter: true},
		{name: "the secrets file exists", setup: func(t *testing.T, dir, _ string, _ keystore.Store) {
			if err := os.WriteFile(filepath.Join(dir, "secrets.enc.yaml"), []byte("version: 1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, secretsAfter: true},
		{name: "an item exists", setup: func(t *testing.T, _, id string, ks keystore.Store) {
			key, _ := age.GenerateX25519Identity()
			if err := ks.Put(id, key.String(), key.Recipient().String()); err != nil {
				t.Fatal(err)
			}
		}, refusal: "already holds an age key"},
		{name: "no keystore", store: keystore.Unavailable{}, refusal: "no keystore"},
		{name: "store fails", store: failingStore{}, refusal: "no keystore"},
		{name: "read back differs", store: lyingStore{keystore.NewMemory()}, refusal: "read back"},
		{name: ".sops.yaml names no recipient", setup: func(t *testing.T, dir, _ string, _ keystore.Store) {
			if err := os.WriteFile(filepath.Join(dir, config.SOPSConfigName), []byte("creation_rules: []\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, refusal: "names no age recipient", sopsAfter: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			withoutAgeKey(t)
			store := c.store
			if store == nil {
				store = keystore.NewMemory()
			}
			withKeystore(t, store)
			path, _ := initWorld(t, nil, fakeSites(), nil)
			id, _, err := config.EnsureID(path)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Dir(path)
			if c.setup != nil {
				c.setup(t, dir, id, store)
			}
			before := map[string]keystore.MemoryItem{}
			if m, ok := store.(*keystore.Memory); ok {
				for k, v := range m.Items {
					before[k] = v
				}
			}
			err, _ = runInitShowing(t, "--config", path, "--sudo=false")
			switch {
			case c.refusal == "" && err != nil:
				t.Fatal(err)
			case c.refusal != "" && (err == nil || !strings.Contains(err.Error(), c.refusal)):
				t.Fatalf("init = %v, want a refusal containing %q", err, c.refusal)
			}
			if m, ok := store.(*keystore.Memory); ok && len(m.Items) != len(before) {
				t.Errorf("items changed from %v to %v", before, m.Items)
			}
			if got := exists(t, filepath.Join(dir, config.SOPSConfigName)); got != c.sopsAfter {
				t.Errorf(".sops.yaml exists = %v, want %v", got, c.sopsAfter)
			}
			if got := exists(t, filepath.Join(dir, "secrets.enc.yaml")); got != c.secretsAfter {
				t.Errorf("secrets.enc.yaml exists = %v, want %v", got, c.secretsAfter)
			}
		})
	}
}

// --age-private-key-file writes the key there, at 0600, and the keystore is
// not touched.
func TestInitAgePrivateKeyFile(t *testing.T) {
	withoutAgeKey(t)
	ks := keystore.NewMemory()
	withKeystore(t, ks)
	path, rec := initWorld(t, nil, fakeSites(), nil)
	keyFile := filepath.Join(t.TempDir(), "deployment.key")
	err, out := runInitShowing(t, "--config", path, "--sudo=false", "--age-private-key-file", keyFile)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(keyFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file %v, %v, want mode 0600", info, err)
	}
	data, _ := os.ReadFile(keyFile)
	identity, err := age.ParseX25519Identity(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := config.Recipients(filepath.Dir(path)); len(got) != 1 || got[0] != identity.Recipient().String() {
		t.Errorf(".sops.yaml names %v", got)
	}
	if len(ks.Items) != 0 {
		t.Errorf("the keystore was written: %v", ks.Items)
	}
	if !strings.Contains(rec.Lines()+out, "SOPS_AGE_KEY_FILE=") || strings.Contains(rec.Lines()+out, "AGE-SECRET-KEY") {
		t.Errorf("unexpected output:\n%s%s", rec.Lines(), out)
	}
}

func TestInitAgePrivateKeyFileRefusals(t *testing.T) {
	t.Run("the file exists", func(t *testing.T) {
		withoutAgeKey(t)
		path, _ := initWorld(t, nil, fakeSites(), nil)
		keyFile := filepath.Join(t.TempDir(), "deployment.key")
		if err := os.WriteFile(keyFile, []byte("kept\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err, _ := runInitShowing(t, "--config", path, "--sudo=false", "--age-private-key-file", keyFile)
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("init = %v, want a refusal", err)
		}
		if data, _ := os.ReadFile(keyFile); string(data) != "kept\n" {
			t.Error("the existing file was changed")
		}
	})
	t.Run("relative path inside", func(t *testing.T) {
		withoutAgeKey(t)
		path, _ := initWorld(t, nil, fakeSites(), nil)
		t.Chdir(filepath.Dir(path))
		err, _ := runInitShowing(t, "--config", "paisans.yaml", "--sudo=false", "--age-private-key-file", filepath.Join("keys", "..", "deployment.key"))
		if err == nil || !strings.Contains(err.Error(), "beside paisans.yaml") {
			t.Fatalf("init = %v, want a refusal", err)
		}
		if exists(t, "deployment.key") || exists(t, config.SOPSConfigName) {
			t.Error("something was written")
		}
	})
	t.Run("the deployment has a key", func(t *testing.T) {
		withoutAgeKey(t)
		path, _ := initWorld(t, nil, fakeSites(), nil)
		if err, _ := runInitShowing(t, "--config", path, "--sudo=false"); err != nil {
			t.Fatal(err)
		}
		keyFile := filepath.Join(t.TempDir(), "deployment.key")
		err, _ := runInitShowing(t, "--config", path, "--sudo=false", "--age-private-key-file", keyFile)
		if err == nil || !strings.Contains(err.Error(), "only for a new key") {
			t.Fatalf("init = %v, want a refusal", err)
		}
		if exists(t, keyFile) {
			t.Error("a key file was written")
		}
	})
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./cmd/paisans/ -run 'TestInitMakesAgeKey|TestInitMakesNoKeyWhen|TestInitAgePrivateKeyFile'`
Expected: FAIL. `TestInitMakesAgeKey` finds no item; the flag tests fail with `flag provided but not defined: -age-private-key-file`.

- [ ] **Step 4: Write `cmd/paisans/agekey.go`**

```go
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/keystore"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// ensureAgeKey makes the age key of a deployment built from scratch: no
// secrets file and no .sops.yaml beside it. It keeps the key in the keystore
// under the deployment id, or in keyFile when one is named, reads it back,
// and only then writes .sops.yaml, so nothing is ever encrypted to a key
// that was not kept. Any other deployment already answered where its key
// lives, and is left as it is. The private key is never shown: a secret
// printed to a terminal is in a scrollback buffer, and this one opens every
// other. README, "Rendered configuration is a build artifact".
func ensureAgeKey(r ui.Reporter, id, configPath, secretsPath, keyFile string) error {
	dir := filepath.Dir(secretsPath)
	sopsPath := filepath.Join(dir, config.SOPSConfigName)
	_, secretsErr := os.Stat(secretsPath)
	_, sopsErr := os.Stat(sopsPath)
	if !errors.Is(secretsErr, os.ErrNotExist) || !errors.Is(sopsErr, os.ErrNotExist) {
		recipients, err := config.Recipients(dir)
		if err != nil {
			return err
		}
		if errors.Is(secretsErr, os.ErrNotExist) && len(recipients) == 0 {
			return &ui.Problem{
				Hint:    config.SOPSConfigName + " names no age recipient",
				Explain: fmt.Sprintf("init makes a key only when there is no %s, since that file is yours once it exists. Add your public key under creation_rules, or move the file aside and re-run init to have a key made.", ui.ShortPath(sopsPath)),
			}
		}
		if keyFile != "" {
			return &ui.Problem{
				Hint:    "--age-private-key-file is only for a new key",
				Explain: "This deployment already has its age key, named in " + config.SOPSConfigName + " or already used for " + ui.ShortPath(secretsPath) + ", so there is no key to write. Drop the flag.",
			}
		}
		return nil
	}

	step := r.Step("make age key")
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		step.Fail(err)
		return err
	}
	key, recipient := identity.String(), identity.Recipient().String()
	var where, export string
	if keyFile != "" {
		where, err = writeKeyFile(id, configPath, keyFile, key)
		export = "SOPS_AGE_KEY_FILE=" + where
	} else {
		where, err = storeKey(id, key, recipient)
		if command := config.AgeKeystore.LookupCommand(id); command != "" {
			export = "SOPS_AGE_KEY_CMD='" + command + "'"
		}
	}
	if err == nil {
		err = config.WriteSOPSConfig(dir, recipient)
	}
	if err != nil {
		step.Fail(err)
		return err
	}
	step.Detail("public key %s, written to %s", recipient, ui.ShortPath(sopsPath))
	step.Detail("private key kept in %s", where)
	step.Done("")
	switch {
	case keyFile != "":
		r.Detail("Set this before running paisans or sops: export %s", export)
	case export != "":
		r.Detail("paisans finds the key on its own. To run sops by hand: export %s", export)
	}
	r.Warn("this is the only copy of the age key", "Losing it loses every secret in "+ui.ShortPath(secretsPath)+". Add a second recipient to "+config.SOPSConfigName+", another admin's key or an offline backup key, then run sops updatekeys "+ui.ShortPath(secretsPath)+".")
	return nil
}

// storeKey keeps key in the keystore under id and reads it back.
func storeKey(id, key, recipient string) (string, error) {
	ks := config.AgeKeystore
	item := ks.Describe(id)
	err := ks.Put(id, key, recipient)
	if errors.Is(err, keystore.ErrExists) {
		return "", &ui.Problem{
			Hint:    "the keystore already holds an age key for this deployment",
			Explain: item + " is there from an earlier init that stopped. It is not replaced, since something may be encrypted to its key. Look at it, remove it if nothing uses it, and re-run init.",
			Cause:   err,
		}
	}
	if err != nil {
		return "", &ui.Problem{
			Hint:    "no keystore to keep the age key in",
			Explain: "init keeps this deployment's age key in the macOS Keychain or the Secret Service (secret-tool), and neither could store it. Either:\n- re-run with --age-private-key-file <path> to keep it in a file you choose, outside this directory\n- bring your own key: write its public key to " + config.SOPSConfigName + " under creation_rules and re-run init",
			Cause:   err,
		}
	}
	got, err := ks.Get(id)
	if err != nil || got != key {
		return "", &ui.Problem{
			Hint:    "the age key read back from the keystore is not the one stored",
			Explain: "Nothing was encrypted to it and " + config.SOPSConfigName + " was not written. Look at " + item + ", remove it, and re-run init.",
			Cause:   err,
		}
	}
	return item, nil
}

// writeKeyFile writes key to path at 0600, never over an existing file and
// never inside the directory that holds the configuration, which is usually
// a git repository: there the key would be one `git add .` from being
// committed. It reads the key back, and returns the absolute path.
func writeKeyFile(id, configPath, path, key string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if inside(filepath.Dir(configPath), abs) {
		return "", &ui.Problem{
			Hint:    "the age key file would sit beside paisans.yaml",
			Explain: fmt.Sprintf("%s is inside %s, which is usually a git repository, so the key would be one git add from being committed. Name a path outside it, Eg: ~/.config/paisans/%s.key", ui.ShortPath(abs), ui.ShortPath(filepath.Dir(configPath)), deployment.Deployment{ID: id}.Token()),
		}
	}
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return "", &ui.Problem{
			Hint:    ui.ShortPath(abs) + " already exists",
			Explain: "init never replaces a key file, since something may be encrypted to the key in it. Name a new path.",
		}
	}
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(key + "\n"); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil || strings.TrimSpace(string(data)) != key {
		return "", &ui.Problem{
			Hint:    "the age key read back from " + ui.ShortPath(abs) + " is not the one written",
			Explain: "Nothing was encrypted to it and " + config.SOPSConfigName + " was not written. Remove the file and re-run init.",
			Cause:   err,
		}
	}
	return abs, nil
}

// inside reports whether path is dir or below it, after symlinks, so a
// relative path or a symlinked directory cannot slip past.
func inside(dir, path string) bool {
	resolve := func(p string) string {
		abs, err := filepath.Abs(p)
		if err != nil {
			return p
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			return real
		}
		// The file does not exist yet: resolve its directory.
		if real, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
			return filepath.Join(real, filepath.Base(abs))
		}
		return abs
	}
	rel, err := filepath.Rel(resolve(dir), resolve(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
```

- [ ] **Step 5: Wire it into `runInit`**

In `cmd/paisans/main.go`, `runInit`, add the flag after `secretsPath`:

```go
	ageKeyFile := fs.String("age-private-key-file", "", "keep a newly made age private key in this file instead of the keystore")
```

and after the block that defaults `*secretsPath`, before `config.LoadDeploymentSecrets`:

```go
	if err := ensureAgeKey(r, cfg.ID, *configPath, *secretsPath, *ageKeyFile); err != nil {
		return err
	}
```

- [ ] **Step 6: Run the new tests**

Run: `go test ./cmd/paisans/ -run 'TestInitMakesAgeKey|TestInitMakesNoKeyWhen|TestInitAgePrivateKeyFile'`
Expected: PASS.

- [ ] **Step 7: Update the existing init tests**

A fresh `init` now encrypts, so two tests change.

In `TestInitReportsStepsAndWhatIsOwed`, replace the plaintext assertion:

```go
	if !rec.Has("warn", "was written in plaintext") {
		t.Errorf("no plaintext warning, although no recipient is configured:\n%s", rec.Lines())
	}
```

with:

```go
	if !rec.Has("done", "make age key") || rec.Has("warn", "was written in plaintext") {
		t.Errorf("a fresh init did not make a key and encrypt:\n%s", rec.Lines())
	}
```

and update its doc comment's last clause to "and a fresh deployment's age key as a step of its own".

Add beside it, so a plaintext file keeps its warning:

```go
// A plaintext secrets file that already exists stays plaintext, and says so.
func TestInitWarnsAboutAPlaintextFile(t *testing.T) {
	path, rec := initWorld(t, nil, fakeSites(), nil)
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "secrets.enc.yaml"), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(err)
	}
	if !rec.Has("warn", "was written in plaintext") || rec.Has("done", "make age key") {
		t.Errorf("unexpected report:\n%s", rec.Lines())
	}
}
```

In `TestInitWarnsAboutADroppedSite`, change `config.LoadSecrets(filepath.Join(filepath.Dir(path), "secrets.enc.yaml"))` to `config.LoadDeploymentSecrets(filepath.Join(filepath.Dir(path), "secrets.enc.yaml"), cfg.ID)`.

- [ ] **Step 8: Run the whole suite and fix what a fresh encrypted init breaks**

Run: `go test ./cmd/paisans/`
Expected: PASS. A test still failing reads a fresh init's secrets with `config.LoadSecrets`; change it to `config.LoadDeploymentSecrets(path, cfg.ID)`, as in Step 7. A test that needs a plaintext file after a fresh init writes `version: 1` to the secrets file before running it, as `TestInitWarnsAboutAPlaintextFile` does. Do not change what a test asserts in any other way: report it instead. `TestOutput` (in `output_test.go`) checks every line `init` prints against the house rules; a hint over 80 characters fails there.

Then: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add cmd/paisans
git commit -F - <<'EOF'
feat: init makes the age key of a deployment built from scratch

With no secrets file and no .sops.yaml, init generates an age key, keeps it
in the keystore under the deployment id, reads it back, and only then writes
.sops.yaml, so nothing is encrypted to a key that was not kept. It prints the
recipient and where the key is, never the key, and warns that this is the
only copy. With no keystore it refuses, unless the operator names a file
with --age-private-key-file, which is never inside the configuration's
directory and never replaces a file. Founder decision.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

---

### Task 4: documentation

**Files:**
- Modify: `README.md` (section *Rendered configuration is a build artifact*, the paragraph beginning "The age key is found where sops looks for it"; and the `init` flag list if *`init`* documents flags)
- Modify: `docs/development.md` (the paragraph beginning "A decryption failure names every place")

**Interfaces:**
- Consumes: the flag and item names from Tasks 1 to 3.
- Produces: nothing code depends on.

- [ ] **Step 1: Replace the README paragraph**

Replace the paragraph that begins "The age key is found where sops looks for it." with:

```markdown
**`init` makes the age key of a deployment built from scratch**, one with no
secrets file and no `.sops.yaml` yet. It keeps the private key in the
workstation's own keystore, the macOS Keychain or the Secret Service
(`secret-tool`), as service `paisans-age` with the deployment id as the
account, and writes the public key to `.sops.yaml`. The key is never printed,
because a secret on a terminal is in a scrollback buffer and this one opens
every other. With no keystore, `init` refuses unless you name a file with
`--age-private-key-file <path>`, outside the directory that holds
`paisans.yaml`, since that directory is usually a git repository.

**The keystore then holds the only copy.** Losing the workstation loses every
secret in the file, so add a second recipient to `.sops.yaml`, another
admin's key or an offline backup key, and run `sops updatekeys
secrets.enc.yaml`.

Every later command finds the key on its own: with no `SOPS_AGE_*` variable
set, paisans reads the keystore item for the deployment id and decrypts with
it in memory. A variable you set always wins. `SOPS_AGE_KEY_CMD`, a command
that prints the key, keeps it out of files and the shell's environment, and
is what `sops` itself needs when you run it by hand; `init` prints the line.
`SOPS_AGE_KEY_FILE` (the key's path), `SOPS_AGE_KEY` (the key itself) and
`sops/age/keys.txt` in your config directory are read too.
```

Then `grep -n "age-key\|--secrets" README.md` within the README sections about `init`. If `init`'s flags are listed, add `--age-private-key-file <path>` with the one-line description from Task 3, Step 5.

- [ ] **Step 2: Update `docs/development.md`**

Replace the paragraph that begins "A decryption failure names every place" with:

```markdown
`init` makes the age key of a deployment built from scratch and keeps it in
`internal/keystore`: the macOS Keychain through `security`, the Secret
Service through `secret-tool`, an item per deployment id under service
`paisans-age`, the key on stdin and never in argv. Tests never reach a real
keystore: `cmd/paisans` and `internal/config` replace `config.AgeKeystore` in
`TestMain`.

`config.LoadDeploymentSecrets` reads that item when no `SOPS_AGE_*` variable
is set and hands its key to the file's age recipients in memory, through
`age.ParsedIdentities` in the embedded sops (v3.13.3). Otherwise, and when
there is no item or its key is not a recipient, sops' own search runs:
`SOPS_AGE_KEY_CMD` first, then `SOPS_AGE_KEY_FILE`, `SOPS_AGE_KEY` and
`sops/age/keys.txt` under the user's config directory. A failure names every
one of those places, and the keystore item when one was looked for.
```

- [ ] **Step 3: Verify the documentation**

Run each and read the output:

```sh
git diff develop -- README.md docs/development.md
git diff develop | grep -n '—'                      # expect nothing
git diff develop | grep -nE 'AGE-SECRET-KEY-1[0-9A-Z]{20,}'   # expect only the test fakes' Q-filled keys
go run ./cmd/paisans init --help 2>&1 | grep age-private-key-file
```

Expected: no em dashes; no real key; the flag listed.

- [ ] **Step 4: Commit**

```bash
git add README.md docs/development.md
git commit -F - <<'EOF'
docs: init makes the age key and keeps it in the keystore

The README section on where the age key is found now says init makes it for
a deployment built from scratch, where it is kept, that the keystore holds
the only copy and what to do about it. docs/development.md names the
keystore package and the order decryption looks for a key in.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

---

### Finish

- [ ] `go vet ./... && go test ./...` pass on the branch; report the commands actually run.
- [ ] Hand the branch to the human. Opening a pull request against `develop` is their call; its description ends with `🤖 Generated with [Claude Code](https://claude.com/claude-code)` and never names Outline pages or tickets.
