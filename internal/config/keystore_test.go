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
	t.Chdir(filepath.Dir(path))
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
