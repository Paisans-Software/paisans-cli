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
