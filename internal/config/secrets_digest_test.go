package config

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

func TestConfigDigestRoundTrips(t *testing.T) {
	s := &Secrets{Version: 1, ID: testID}
	if err := s.Set("paisans.config_digest", "sha256:aa sha256:bb"); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get("paisans.config_digest")
	if !ok || got != "sha256:aa sha256:bb" {
		t.Fatalf("Get = %q, %v", got, ok)
	}
}

func TestDecryptSecretsBytesRefusesTampering(t *testing.T) {
	key := identity(t)
	store(t, key)
	path := encryptedTo(t, key)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptSecretsBytes(data, testID); err != nil {
		t.Fatalf("untampered: %v", err)
	}
	tampered := dropOneEncryptedValue(t, data)
	if _, err := DecryptSecretsBytes(tampered, testID); !errors.Is(err, errTampered) {
		t.Fatalf("tampered: got %v, want errTampered", err)
	}
}

// dropOneEncryptedValue removes the line of the first encrypted value in the
// file. Every remaining value still decrypts, so the only thing that can
// notice is the MAC, which covers the whole tree.
func dropOneEncryptedValue(t *testing.T, data []byte) []byte {
	t.Helper()
	lines := bytes.Split(data, []byte("\n"))
	for i, line := range lines {
		if bytes.Contains(line, []byte("ENC[AES256_GCM,data:")) {
			return bytes.Join(append(lines[:i:i], lines[i+1:]...), []byte("\n"))
		}
	}
	t.Fatal("the fixture holds no encrypted value")
	return nil
}
