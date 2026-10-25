package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"filippo.io/age/plugin"

	"github.com/paisans-software/paisans-cli/internal/keystore"
)

func TestReadVariableKey(t *testing.T) {
	a, b := identity(t), identity(t)
	pq, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	file := func(t *testing.T, body string) string {
		path := filepath.Join(t.TempDir(), "keys.txt")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, c := range []struct {
		name      string
		env       func(t *testing.T) map[string]string
		recipient string
		from      []string
		refusal   string
	}{
		{name: "keys on one line, repeated", env: func(*testing.T) map[string]string {
			return map[string]string{"SOPS_AGE_KEY": a.String() + " " + a.String()}
		}, recipient: a.Recipient().String(), from: []string{"SOPS_AGE_KEY"}},
		{name: "comments and blank lines", env: func(t *testing.T) map[string]string {
			return map[string]string{"SOPS_AGE_KEY_FILE": file(t, "# public key: x\n\n"+a.String()+"\n")}
		}, recipient: a.Recipient().String(), from: []string{"SOPS_AGE_KEY_FILE"}},
		{name: "two keys", env: func(*testing.T) map[string]string {
			return map[string]string{"SOPS_AGE_KEY": a.String() + "\n" + b.String()}
		}, refusal: "hold 2 age keys"},
		{name: "a file of two keys", env: func(t *testing.T) map[string]string {
			return map[string]string{"SOPS_AGE_KEY_FILE": file(t, a.String()+"\n"+b.String()+"\n")}
		}, refusal: "hold 2 age keys"},
		{name: "two variables, one key", env: func(t *testing.T) map[string]string {
			return map[string]string{"SOPS_AGE_KEY": a.String(), "SOPS_AGE_KEY_FILE": file(t, a.String()+"\n")}
		}, recipient: a.Recipient().String(), from: []string{"SOPS_AGE_KEY", "SOPS_AGE_KEY_FILE"}},
		{name: "a file of comments only", env: func(t *testing.T) map[string]string {
			return map[string]string{"SOPS_AGE_KEY_FILE": file(t, "# nothing\n")}
		}, refusal: "holds no age key"},
		{name: "an empty command", env: func(*testing.T) map[string]string {
			return map[string]string{"SOPS_AGE_KEY_CMD": "   "}
		}, refusal: "the command is empty"},
		{name: "a post-quantum key", env: func(*testing.T) map[string]string {
			return map[string]string{"SOPS_AGE_KEY": pq.String()}
		}, recipient: pq.Recipient().String(), from: []string{"SOPS_AGE_KEY"}},
		{name: "a plugin identity", env: func(*testing.T) map[string]string {
			return map[string]string{"SOPS_AGE_KEY": plugin.EncodeIdentity("yubikey", []byte("slot 1"))}
		}, refusal: "plugin identity"},
	} {
		t.Run(c.name, func(t *testing.T) {
			noAgeVariables(t)
			for k, v := range c.env(t) {
				t.Setenv(k, v)
			}
			key, err := ReadVariableKey()
			if c.refusal != "" {
				if err == nil || !strings.Contains(err.Error(), c.refusal) {
					t.Fatalf("ReadVariableKey = %+v, %v, want a refusal containing %q", key, err, c.refusal)
				}
				if strings.Contains(err.Error(), "AGE-SECRET-KEY") {
					t.Error("the refusal shows a private key")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if key.Recipient != c.recipient || strings.Join(key.Variables, ",") != strings.Join(c.from, ",") {
				t.Errorf("ReadVariableKey = %+v, want %s from %v", key, c.recipient, c.from)
			}
		})
	}
}

// lookupMemory is a keystore with a lookup command.
type lookupMemory struct{ *keystore.Memory }

func (lookupMemory) LookupCommand(id string) string { return "lookup " + id }

// failingGet is a keystore that cannot be read.
type failingGet struct{ *keystore.Memory }

func (failingGet) Get(string) (string, error) { return "", os.ErrPermission }

// A secrets file decrypted with a variable while the keystore holds the
// deployment's key is reported only when the variables do not hold it.
func TestKeystoreBypassed(t *testing.T) {
	saved, other := identity(t), identity(t)
	for _, c := range []struct {
		name   string
		store  func() keystore.Store
		env    map[string]string
		notice bool
	}{
		{name: "another key", env: map[string]string{"SOPS_AGE_KEY": other.String()}, notice: true},
		{name: "the saved key itself", env: map[string]string{"SOPS_AGE_KEY": saved.String()}},
		{name: "the saved key among others", env: map[string]string{"SOPS_AGE_KEY": other.String() + " " + saved.String()}},
		{name: "the keystore's lookup command", store: func() keystore.Store { return lookupMemory{keystore.NewMemory()} },
			env: map[string]string{"SOPS_AGE_KEY_CMD": "lookup " + testID, "SOPS_AGE_KEY": other.String()}},
		{name: "no item", store: func() keystore.Store { return nil }, env: map[string]string{"SOPS_AGE_KEY": other.String()}},
		{name: "a keystore that cannot be read", store: func() keystore.Store { return failingGet{keystore.NewMemory()} },
			env: map[string]string{"SOPS_AGE_KEY": other.String()}},
	} {
		t.Run(c.name, func(t *testing.T) {
			noAgeVariables(t)
			path := encryptedTo(t, saved, other)
			var s keystore.Store = keystore.NewMemory()
			if c.store != nil {
				s = c.store()
			}
			switch st := s.(type) {
			case nil:
				s = keystore.NewMemory()
			case *keystore.Memory:
				st.Put(testID, saved.String(), saved.Recipient().String())
			case lookupMemory:
				st.Put(testID, saved.String(), saved.Recipient().String())
			}
			withStore(t, s)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			var got []string
			KeystoreBypassed = func(variables []string, item string) { got = append(got, strings.Join(variables, ",")+" "+item) }
			t.Cleanup(func() { KeystoreBypassed = nil })
			if _, err := LoadDeploymentSecrets(path, testID); err != nil {
				t.Fatal(err)
			}
			if c.notice != (len(got) == 1) {
				t.Errorf("notices = %v, want one: %v", got, c.notice)
			}
			if c.notice && got[0] != "SOPS_AGE_KEY "+s.Describe(testID) {
				t.Errorf("notice = %q", got[0])
			}
		})
	}
}

// VariablesHolding names each set variable that holds the recipient's key,
// among others or alone, and passes over one that cannot be read.
func TestVariablesHolding(t *testing.T) {
	a, b := identity(t), identity(t)
	noAgeVariables(t)
	t.Setenv("SOPS_AGE_KEY", b.String()+" "+a.String())
	t.Setenv("SOPS_AGE_KEY_FILE", filepath.Join(t.TempDir(), "missing"))
	if got := VariablesHolding(a.Recipient().String()); len(got) != 1 || got[0] != "SOPS_AGE_KEY" {
		t.Fatalf("got %v", got)
	}
	if got := VariablesHolding(identity(t).Recipient().String()); len(got) != 0 {
		t.Fatalf("a key no variable holds: got %v", got)
	}
}
