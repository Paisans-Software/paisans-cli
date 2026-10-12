package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"filippo.io/age"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// The next values and the paths a --all run has left are state the toolkit
// wrote about a rotation in progress, so a run that stops must find both again
// from the file alone.
func TestRotationsSurviveTheSecretsFile(t *testing.T) {
	dir := t.TempDir()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "keys.txt")
	if err := os.WriteFile(keyFile, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", keyFile)

	path := filepath.Join(dir, "secrets.enc.yaml")
	original := &config.Secrets{
		Version:         1,
		Rotations:       map[string]string{"cluster.admin_password": "next-not-a-secret-1"},
		RotateRemaining: []string{"storage.garage.admin_token", "storage.garage.rpc_secret"},
	}
	if err := config.WriteSecrets(path, original, []string{identity.Recipient().String()}); err != nil {
		t.Fatal(err)
	}
	back, err := config.LoadSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Rotations, original.Rotations) {
		t.Errorf("rotations = %v, want %v", back.Rotations, original.Rotations)
	}
	if !reflect.DeepEqual(back.RotateRemaining, original.RotateRemaining) {
		t.Errorf("rotate_remaining = %v, want %v", back.RotateRemaining, original.RotateRemaining)
	}
	if back.UnknownKeys {
		t.Error("the file's own rotation keys were reported as unknown")
	}
}

func TestRotationPendingIsSorted(t *testing.T) {
	s := &config.Secrets{Rotations: map[string]string{
		"storage.garage.rpc_secret": "b", "cluster.admin_password": "a", "apps.id.static_api_key": "c",
	}}
	want := []string{"apps.id.static_api_key", "cluster.admin_password", "storage.garage.rpc_secret"}
	if got := s.RotationPending(); !reflect.DeepEqual(got, want) {
		t.Errorf("RotationPending = %v, want %v", got, want)
	}
	if got := (&config.Secrets{}).RotationPending(); len(got) != 0 {
		t.Errorf("RotationPending on a file with no rotation = %v", got)
	}
}
