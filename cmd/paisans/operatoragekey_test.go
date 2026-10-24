package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/paisans-software/paisans-cli/internal/keystore"
)

// ageKeyTestID is a deployment id the keystore accepts.
const ageKeyTestID = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"

// runAgeKey runs operator age-key and returns its error, what it wrote to
// the stdout it was given, and what the reporter printed.
func runAgeKey(t *testing.T, args ...string) (error, string, string) {
	t.Helper()
	var out strings.Builder
	var err error
	printed := captureStdout(t, func() { err = runOperatorAgeKey(args, &out) })
	return err, out.String(), printed
}

func TestAgeKeyMakesThenShows(t *testing.T) {
	withoutAgeKey(t)
	ks := keystore.NewMemory()
	withKeystore(t, ks)
	err, first, _ := runAgeKey(t, "--id", ageKeyTestID)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^age1[0-9a-z]+$`).MatchString(lastLine(first)) {
		t.Fatalf("last line of %q is not a recipient", first)
	}
	err, second, _ := runAgeKey(t, "--id", ageKeyTestID)
	if err != nil {
		t.Fatal(err)
	}
	if lastLine(second) != lastLine(first) {
		t.Errorf("second run printed %q, first %q", lastLine(second), lastLine(first))
	}
	if len(ks.Items) != 1 {
		t.Errorf("the store holds %d items, want 1: %v", len(ks.Items), ks.Items)
	}
}

func TestAgeKeyVariableWins(t *testing.T) {
	withoutAgeKey(t)
	ks := keystore.NewMemory()
	withKeystore(t, ks)
	stored, _ := age.GenerateX25519Identity()
	if err := ks.Put(ageKeyTestID, stored.String(), stored.Recipient().String()); err != nil {
		t.Fatal(err)
	}
	brought, _ := age.GenerateX25519Identity()
	t.Setenv("SOPS_AGE_KEY", brought.String())
	err, out, printed := runAgeKey(t, "--id", ageKeyTestID)
	if err != nil {
		t.Fatal(err)
	}
	if lastLine(out) != brought.Recipient().String() {
		t.Errorf("printed %q, want the variable's %s", lastLine(out), brought.Recipient())
	}
	if !strings.Contains(printed, "from SOPS_AGE_KEY") {
		t.Errorf("the step does not say where the key came from:\n%s", printed)
	}
	if !strings.Contains(printed, "WARN the keystore holds an age key for this deployment") {
		t.Errorf("no warning about the keystore item beside the variable:\n%s", printed)
	}
}

func TestAgeKeyReadsExistingFile(t *testing.T) {
	withoutAgeKey(t)
	ks := keystore.NewMemory()
	withKeystore(t, ks)
	identity, _ := age.GenerateX25519Identity()
	keyFile := filepath.Join(t.TempDir(), "mine.key")
	content := "# created by hand\n" + identity.String() + "\n"
	if err := os.WriteFile(keyFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	err, out, printed := runAgeKey(t, "--id", ageKeyTestID, "--age-private-key-file", keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if lastLine(out) != identity.Recipient().String() {
		t.Errorf("printed %q, want %s", lastLine(out), identity.Recipient())
	}
	if data, _ := os.ReadFile(keyFile); string(data) != content {
		t.Error("the key file was changed")
	}
	if len(ks.Items) != 0 {
		t.Errorf("the keystore was written: %v", ks.Items)
	}
	if strings.Contains(out+printed, "AGE-SECRET-KEY") {
		t.Error("the private key was shown")
	}
}

func TestAgeKeyKeystoreBeatsFile(t *testing.T) {
	withoutAgeKey(t)
	ks := keystore.NewMemory()
	withKeystore(t, ks)
	stored, _ := age.GenerateX25519Identity()
	if err := ks.Put(ageKeyTestID, stored.String(), stored.Recipient().String()); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "new.key")
	err, out, _ := runAgeKey(t, "--id", ageKeyTestID, "--age-private-key-file", keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if lastLine(out) != stored.Recipient().String() {
		t.Errorf("printed %q, want the keystore item's %s", lastLine(out), stored.Recipient())
	}
	if exists(t, keyFile) {
		t.Error("a key file was written beside an existing keystore item")
	}
}

func TestAgeKeyMakesNamedFile(t *testing.T) {
	withoutAgeKey(t)
	withKeystore(t, keystore.NewMemory())
	keyFile := filepath.Join(t.TempDir(), "new.key")
	err, first, _ := runAgeKey(t, "--id", ageKeyTestID, "--age-private-key-file", keyFile)
	if err != nil {
		t.Fatal(err)
	}
	err, second, _ := runAgeKey(t, "--id", ageKeyTestID, "--age-private-key-file", keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if lastLine(first) != lastLine(second) || !strings.HasPrefix(lastLine(first), "age1") {
		t.Errorf("first %q, second %q, want the same recipient", lastLine(first), lastLine(second))
	}
}

func TestAgeKeyIDFromConfig(t *testing.T) {
	withoutAgeKey(t)
	ks := keystore.NewMemory()
	withKeystore(t, ks)
	path, _ := initWorld(t, nil, fakeSites(), nil)
	err, out, _ := runAgeKey(t, "--config", path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(lastLine(out), "age1") || len(ks.Items) != 1 {
		t.Errorf("printed %q, items %v", out, ks.Items)
	}
	t.Chdir(t.TempDir())
	err, _, _ = runAgeKey(t)
	if err == nil || !strings.Contains(problemText(err)+err.Error(), "--id") {
		t.Fatalf("age-key = %v, want a refusal naming --id", err)
	}
}

func TestAgeKeyNeverPrintsPrivate(t *testing.T) {
	withoutAgeKey(t)
	withKeystore(t, keystore.NewMemory())
	for range 2 {
		err, out, printed := runAgeKey(t, "--id", ageKeyTestID, "-v")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out+printed, "AGE-SECRET-KEY") {
			t.Errorf("the private key was shown:\n%s%s", out, printed)
		}
	}
}
