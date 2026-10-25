package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// A file the toolkit wrote sets none of the metadata HandSetMetadata names;
// a hand-set field is named, and an empty or false one is not.
func TestHandSetMetadata(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "secrets.enc.yaml")
	if err := config.WriteSecrets(path, &config.Secrets{Version: 1}, []string{id.Recipient().String()}); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := config.HandSetMetadata(written); len(got) != 0 {
		t.Fatalf("a file the toolkit wrote sets %v", got)
	}
	hand := "a: ENC[x]\nsops:\n    age:\n        - recipient: " + id.Recipient().String() + "\n    kms: []\n    unencrypted_suffix: \"\"\n    mac_only_encrypted: true\n    encrypted_regex: ^a$\n    mac: ENC[x]\n"
	if got := config.HandSetMetadata([]byte(hand)); !slices.Equal(got, []string{"encrypted_regex", "mac_only_encrypted"}) {
		t.Fatalf("got %v", got)
	}
}

// ReencryptSecretsBytes refuses a file with hand-set metadata before
// decrypting it, naming the field and sops updatekeys.
func TestReencryptRefusesHandSetMetadata(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	hand := []byte("a: ENC[x]\nsops:\n    age:\n        - recipient: " + id.Recipient().String() + "\n    unencrypted_regex: ^a$\n    mac: ENC[x]\n")
	_, err = config.ReencryptSecretsBytes("secrets.enc.yaml", hand, "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01", []string{id.Recipient().String()})
	hint, explain := ui.Describe(err)
	if err == nil || !strings.Contains(hint, "unencrypted_regex") || !strings.Contains(explain, "sops updatekeys") {
		t.Fatalf("got %v: %s", err, explain)
	}
}
