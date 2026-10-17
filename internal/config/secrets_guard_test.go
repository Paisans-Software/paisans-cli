package config_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// realDeployment turns the explicit test mode off for one test, so the
// rules a real deployment runs under apply.
func realDeployment(t *testing.T) {
	t.Helper()
	t.Setenv(config.PlaintextSecretsVariable, "")
}

// encryptedFile writes a secrets file at dir/secrets.enc.yaml encrypted to
// two fresh age keys, with no .sops.yaml beside it, and sets the first key
// for decryption. It returns the path and the two recipients.
func encryptedFile(t *testing.T) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	var recipients []string
	var first *age.X25519Identity
	for range 2 {
		id, err := age.GenerateX25519Identity()
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = id
		}
		recipients = append(recipients, id.Recipient().String())
	}
	keyFile := filepath.Join(t.TempDir(), "keys.txt")
	if err := os.WriteFile(keyFile, []byte(first.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", keyFile)
	path := filepath.Join(dir, "secrets.enc.yaml")
	s := &config.Secrets{Version: config.SecretsVersion, External: map[string]string{"smtp_password": "kept-not-a-secret-0001"}}
	if err := config.WriteSecrets(path, s, recipients); err != nil {
		t.Fatal(err)
	}
	return path, recipients
}

// An encrypted file with no .sops.yaml beside it is never written back in
// plaintext: the write is refused, with the file as it was, and the refusal
// names .sops.yaml, the recipients the file records, and the command that
// restores .sops.yaml with them. The explicit test mode does not change
// that, and neither does a caller that hands it secrets of its own.
func TestWriteSecretsNeverTurnsAnEncryptedFileIntoPlaintext(t *testing.T) {
	for _, mode := range []string{"", "1"} {
		t.Run("test mode "+mode, func(t *testing.T) {
			t.Setenv(config.PlaintextSecretsVariable, mode)
			path, recipients := encryptedFile(t)
			before, _ := os.ReadFile(path)
			read, err := config.LoadDeploymentSecrets(path, "")
			if err != nil {
				t.Fatal(err)
			}
			for name, s := range map[string]*config.Secrets{
				"as read":   read,
				"a new one": {Version: config.SecretsVersion, External: map[string]string{"smtp_password": "new-not-a-secret-0002"}},
			} {
				read.External["dns_token"] = "new-not-a-secret-0003"
				err := config.WriteSecrets(path, s, nil)
				if err == nil {
					t.Fatalf("%s: written with no recipient", name)
				}
				after, _ := os.ReadFile(path)
				if !bytes.Equal(before, after) {
					t.Fatalf("%s: the file changed", name)
				}
				hint, explain := ui.Describe(err)
				if !strings.Contains(hint, ".sops.yaml") {
					t.Errorf("%s: hint %q does not name .sops.yaml", name, hint)
				}
				for _, r := range recipients {
					if !strings.Contains(explain, r) {
						t.Errorf("%s: the refusal does not name recipient %s:\n%s", name, r, explain)
					}
				}
				want := "printf 'creation_rules:\\n  - age: " + strings.Join(recipients, ",") + "\\n' > " + filepath.Join(filepath.Dir(path), ".sops.yaml")
				if !strings.Contains(explain, want) {
					t.Errorf("%s: no restore command %q in:\n%s", name, want, explain)
				}
			}
			entries, _ := os.ReadDir(filepath.Dir(path))
			if len(entries) != 1 {
				t.Errorf("files beside the secrets: %v", entries)
			}
		})
	}
}

// The recipients a sops file records are read from its metadata, without
// decrypting it.
func TestFileRecipients(t *testing.T) {
	path, recipients := encryptedFile(t)
	t.Setenv("SOPS_AGE_KEY_FILE", "")
	data, _ := os.ReadFile(path)
	if got := config.FileRecipients(data); !slices.Equal(got, recipients) {
		t.Errorf("FileRecipients = %v, want %v", got, recipients)
	}
	if got := config.FileRecipients([]byte("version: 2\n")); got != nil {
		t.Errorf("a plaintext file's recipients: %v", got)
	}
}

// Outside the explicit test mode nothing is written in plaintext, not even
// a file that does not exist yet.
func TestWriteSecretsRefusesPlaintextOutsideTestMode(t *testing.T) {
	realDeployment(t)
	path := filepath.Join(t.TempDir(), "secrets.enc.yaml")
	err := config.WriteSecrets(path, &config.Secrets{Version: config.SecretsVersion}, nil)
	if err == nil {
		t.Fatal("written in plaintext")
	}
	if hint, _ := ui.Describe(err); !strings.Contains(hint, ".sops.yaml") || strings.Contains(err.Error(), config.PlaintextSecretsVariable) {
		t.Fatalf("WriteSecrets = %v, want a refusal naming .sops.yaml and not the test mode", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file was written: %v", err)
	}
}

// A deployment's plaintext secrets file is refused where it is read, naming
// the problem and the sops command that fixes it; the explicit test mode
// reads it.
func TestLoadDeploymentSecretsRefusesPlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.enc.yaml")
	if err := os.WriteFile(path, []byte(plaintextSecrets), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadDeploymentSecrets(path, ""); err != nil {
		t.Fatalf("the test mode: %v", err)
	}
	realDeployment(t)
	_, err := config.LoadDeploymentSecrets(path, "")
	if err == nil {
		t.Fatal("a plaintext file was read")
	}
	hint, explain := ui.Describe(err)
	if !strings.Contains(hint, "is not encrypted") || !strings.Contains(explain, "sops --encrypt --in-place "+path) || strings.Contains(err.Error(), config.PlaintextSecretsVariable) {
		t.Fatalf("LoadDeploymentSecrets = %v, want a refusal naming the fix", err)
	}
}

// CheckSecretsFile passes an encrypted file with a recipient beside it and
// a missing file, and refuses an encrypted one without and a plaintext one
// outside the test mode.
func TestCheckSecretsFile(t *testing.T) {
	path, recipients := encryptedFile(t)
	if err := config.CheckSecretsFile(path); err == nil || !strings.Contains(err.Error(), recipients[0]) {
		t.Errorf("no .sops.yaml: %v", err)
	}
	if err := config.WriteSOPSConfig(filepath.Dir(path), recipients[0]); err != nil {
		t.Fatal(err)
	}
	if err := config.CheckSecretsFile(path); err != nil {
		t.Errorf("with .sops.yaml: %v", err)
	}
	if err := config.CheckSecretsFile(filepath.Join(t.TempDir(), "secrets.enc.yaml")); err != nil {
		t.Errorf("no file: %v", err)
	}
	plain := filepath.Join(t.TempDir(), "secrets.enc.yaml")
	if err := os.WriteFile(plain, []byte(plaintextSecrets), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.CheckSecretsFile(plain); err != nil {
		t.Errorf("plaintext in the test mode: %v", err)
	}
	realDeployment(t)
	if err := config.CheckSecretsFile(plain); err == nil || !strings.Contains(err.Error(), "is not encrypted") {
		t.Errorf("plaintext: %v", err)
	}
	if err := config.CheckSecretsBytes(plain, []byte(plaintextSecrets)); err == nil {
		t.Error("plaintext bytes passed")
	}
}

// The command a refusal prints for a missing .sops.yaml writes it, and run
// again refuses rather than replace the file it wrote. A .sops.yaml that is
// there but names no recipient Paisans reads (here, key_groups) gets no
// command at all, so pasting nothing can replace the operator's rules.
func TestTheRestoreCommandNeverOverwrites(t *testing.T) {
	path, recipients := encryptedFile(t)
	dir := filepath.Dir(path)
	_, explain := ui.Describe(config.CheckSecretsFile(path))
	var command string
	for _, line := range strings.Split(explain, "\n") {
		if strings.HasPrefix(line, "  sh -c ") {
			command = strings.TrimSpace(line)
		}
	}
	if command == "" {
		t.Fatalf("no command in:\n%s", explain)
	}
	if out, err := exec.Command("sh", "-c", command).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", command, err, out)
	}
	if got, err := config.Recipients(dir); err != nil || !slices.Equal(got, recipients) {
		t.Fatalf("restored .sops.yaml names %v, %v; want %v", got, err, recipients)
	}
	sopsPath := filepath.Join(dir, config.SOPSConfigName)
	keyGroups := "creation_rules:\n  - key_groups:\n      - age:\n          - " + recipients[0] + "\n"
	if err := os.WriteFile(sopsPath, []byte(keyGroups), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-c", command).CombinedOutput(); err == nil {
		t.Errorf("the command ran again over an existing .sops.yaml: %s", out)
	}
	if data, _ := os.ReadFile(sopsPath); string(data) != keyGroups {
		t.Fatalf("the command replaced .sops.yaml: %q", data)
	}
	err := config.CheckSecretsFile(path)
	if err == nil {
		t.Fatal("key_groups alone passed")
	}
	hint, explain := ui.Describe(err)
	if !strings.Contains(hint, "names no age recipient") || strings.Contains(explain, "printf") || strings.Contains(explain, "sh -c") || !strings.Contains(explain, recipients[0]) {
		t.Errorf("an existing .sops.yaml: %s\n%s", hint, explain)
	}
}

// Only age public keys from a file's metadata are printed, and a file whose
// metadata names anything else gets no command: a recipient there could
// break out of the command's quoting.
func TestNoRecipientProblemPrintsOnlyAgeRecipients(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	good := id.Recipient().String()
	const bad = "age1x' ; touch pwned ; echo '"
	path := filepath.Join(t.TempDir(), "secrets.enc.yaml")
	file := "version: 2\nsops:\n    age:\n        - recipient: " + good + "\n        - recipient: \"" + bad + "\"\n    mac: ENC[x]\n"
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	_, explain := ui.Describe(config.NoRecipientProblem(path))
	if strings.Contains(explain, "touch") || strings.Contains(explain, "printf") || !strings.Contains(explain, good) || !strings.Contains(explain, "1 recipient(s) that are not age public keys") {
		t.Errorf("explain:\n%s", explain)
	}
}
