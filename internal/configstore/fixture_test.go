package configstore

import (
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/keystore"
)

const fixtureID = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"

// fixtureSecrets encrypts a secrets file for fixtureID with an age key held in
// a memory keystore, and returns the bytes as stored.
func fixtureSecrets(t *testing.T, digest string) []byte {
	t.Helper()
	for _, name := range []string{"SOPS_AGE_KEY", "SOPS_AGE_KEY_FILE", "SOPS_AGE_KEY_CMD", "SOPS_AGE_SSH_PRIVATE_KEY_FILE", "SOPS_AGE_SSH_PRIVATE_KEY_CMD"} {
		t.Setenv(name, "")
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	mem := keystore.NewMemory()
	if err := mem.Put(fixtureID, id.String(), id.Recipient().String()); err != nil {
		t.Fatal(err)
	}
	prev := config.AgeKeystore
	config.AgeKeystore = mem
	t.Cleanup(func() { config.AgeKeystore = prev })

	s := config.Secrets{Version: 1, ID: fixtureID}
	if digest != "" {
		if err := s.Set("paisans.config_digest", digest); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "secrets.enc.yaml")
	if err := config.WriteSecrets(path, &s, []string{id.Recipient().String()}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// fixtureVersion is a version whose secrets record the digest of its files.
func fixtureVersion(t *testing.T, paisansYAML, sopsYAML string) Files {
	t.Helper()
	return Files{
		Paisans: []byte(paisansYAML),
		SOPS:    []byte(sopsYAML),
		Secrets: fixtureSecrets(t, Digest([]byte(paisansYAML), []byte(sopsYAML))),
	}
}

// fixtureVersionWithoutDigest is a version whose secrets record no digest.
func fixtureVersionWithoutDigest(t *testing.T) Files {
	t.Helper()
	return Files{
		Paisans: []byte("paisans: 1\n"),
		SOPS:    []byte("creation_rules: []\n"),
		Secrets: fixtureSecrets(t, ""),
	}
}
