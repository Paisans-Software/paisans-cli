package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// realSecrets turns the explicit test mode, which TestMain sets, off for one
// test: the rules a real deployment runs under apply.
func realSecrets(t *testing.T) {
	t.Helper()
	t.Setenv(config.PlaintextSecretsVariable, "")
}

// wantNoRecipient fails unless err refuses for a missing .sops.yaml, naming
// recipient and the command that restores .sops.yaml beside secretsPath.
func wantNoRecipient(t *testing.T, err error, secretsPath, recipient string) {
	t.Helper()
	if err == nil {
		t.Fatal("no refusal")
	}
	hint, explain := ui.Describe(err)
	if !strings.Contains(hint, ".sops.yaml") || !strings.Contains(explain, recipient) {
		t.Fatalf("err = %v, want a refusal naming .sops.yaml and %s", err, recipient)
	}
	if want := "printf 'creation_rules:\\n  - age: " + recipient + "\\n' > "; !strings.Contains(explain, want) {
		t.Errorf("no restore command %q in:\n%s", want, explain)
	}
}

// wantEncrypted fails unless the file at path still carries sops metadata
// and none of the plaintext values in values.
func wantEncrypted(t *testing.T, path string, values ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !config.IsEncrypted(data) {
		t.Fatalf("%s is plaintext", path)
	}
	for _, v := range values {
		if v != "" && bytes.Contains(data, []byte(v)) {
			t.Fatalf("%s holds a value in the clear", path)
		}
	}
}

// init on a deployment whose secrets are encrypted and whose .sops.yaml is
// missing refuses before it reads a host or writes anything: the
// configuration keeps its missing subnet, and the secrets stay encrypted,
// byte for byte, with nothing generated into them in plaintext.
func TestInitRefusesAnEncryptedFileWithoutSOPSConfig(t *testing.T) {
	for _, mode := range []string{"", "1"} {
		t.Run("test mode "+mode, func(t *testing.T) {
			t.Setenv(config.PlaintextSecretsVariable, mode)
			path, _ := initWorld(t, nil, fakeSites(), nil)
			identity, err := age.GenerateX25519Identity()
			if err != nil {
				t.Fatal(err)
			}
			keyFile := filepath.Join(t.TempDir(), "key.txt")
			if err := os.WriteFile(keyFile, []byte(identity.String()+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SOPS_AGE_KEY_FILE", keyFile)
			secretsPath := filepath.Join(filepath.Dir(path), "secrets.enc.yaml")
			const smtp = "kept-not-a-secret-smtp-0001"
			if err := config.WriteSecrets(secretsPath, &config.Secrets{Version: config.SecretsVersion, External: map[string]string{"smtp_password": smtp}}, []string{identity.Recipient().String()}); err != nil {
				t.Fatal(err)
			}
			files := snapshot(t, filepath.Dir(path))
			err = runInitQuietly(t, path)
			wantNoRecipient(t, err, secretsPath, identity.Recipient().String())
			if now := snapshot(t, filepath.Dir(path)); !reflect.DeepEqual(files, now) {
				t.Error("init changed a file although it refused")
			}
			wantEncrypted(t, secretsPath, smtp)
		})
	}
}

// apply on the same deployment refuses before init or any step, dry run and
// --execute alike, and leaves every file as it was.
func TestApplyRefusesAnEncryptedFileWithoutSOPSConfig(t *testing.T) {
	for _, execute := range []bool{false, true} {
		configPath, secretsPath := encryptedDeployment(t, dropGenerated)
		sopsPath := filepath.Join(filepath.Dir(secretsPath), config.SOPSConfigName)
		sops, err := os.ReadFile(sopsPath)
		if err != nil {
			t.Fatal(err)
		}
		recipient := strings.TrimSpace(strings.TrimPrefix(string(sops), "creation_rules:\n  - age: "))
		if err := os.Remove(sopsPath); err != nil {
			t.Fatal(err)
		}
		files := snapshot(t, filepath.Dir(configPath))
		ran := fakeConverge(t, nil)
		args := []string{"--config", configPath, "--secrets", secretsPath}
		if execute {
			args = append(args, "--execute")
		}
		captureStdout(t, func() { err = runApply(args, strings.NewReader(""), io.Discard) })
		wantNoRecipient(t, err, secretsPath, recipient)
		if len(*ran) != 0 {
			t.Errorf("execute %t: ran %v", execute, *ran)
		}
		if now := snapshot(t, filepath.Dir(configPath)); !reflect.DeepEqual(files, now) {
			t.Errorf("execute %t: a file changed", execute)
		}
		wantEncrypted(t, secretsPath)
	}
}

// A plaintext secrets.enc.yaml is refused wherever a deployment reads it,
// naming the problem and the fix, before anything is reached or changed.
func TestAPlaintextSecretsFileIsRefused(t *testing.T) {
	plain, err := os.ReadFile(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func(configPath, secretsPath string) error{
		"validate --offline": func(c, s string) error { return runValidate([]string{"--config", c, "--offline"}) },
		"validate":           func(c, s string) error { return runValidate([]string{"--config", c}) },
		"doctor":             func(c, s string) error { return runDoctor([]string{"--config", c}) },
		"apply": func(c, s string) error {
			fakeConverge(t, nil)
			return runApply([]string{"--config", c}, strings.NewReader(""), io.Discard)
		},
		"apply --site": func(c, s string) error {
			return runApply([]string{"--config", c, "--site", "home-a"}, strings.NewReader(""), io.Discard)
		},
		"config publish": func(c, s string) error {
			return runConfigPublish([]string{"--config", c, "--execute"}, strings.NewReader(""), io.Discard)
		},
		"secrets list": func(c, s string) error { return runSecretsList([]string{"--config", c}, io.Discard) },
		"init":         func(c, s string) error { return runInit([]string{"--config", c}, strings.NewReader(""), io.Discard) },
	} {
		t.Run(name, func(t *testing.T) {
			realSecrets(t)
			configPath, secretsPath := secretsDir(t, string(plain))
			files := snapshot(t, filepath.Dir(configPath))
			var err error
			captureStdout(t, func() { err = run(configPath, secretsPath) })
			if err == nil {
				t.Fatal("no refusal")
			}
			hint, explain := ui.Describe(err)
			if !strings.Contains(hint, "is not encrypted") || !strings.Contains(explain, "sops --encrypt --in-place") || strings.Contains(err.Error(), config.PlaintextSecretsVariable) {
				t.Fatalf("err = %v, want a refusal of plaintext naming the fix", err)
			}
			if now := snapshot(t, filepath.Dir(configPath)); !reflect.DeepEqual(files, now) {
				t.Error("a file changed")
			}
		})
	}
}

// The explicit test mode reads a plaintext file, and says so.
func TestTheTestModeReadsPlaintext(t *testing.T) {
	plain, err := os.ReadFile(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	configPath, _ := secretsDir(t, string(plain))
	rec := withRecorder(t, false)
	if err := runSecretsList([]string{"--config", configPath}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !rec.Has("warn", "is not encrypted") {
		t.Errorf("no warning:\n%s", rec.Lines())
	}
	if err := runValidate([]string{"--config", configPath, "--offline"}); err != nil {
		t.Errorf("validate: %v", err)
	}
}

// init on a fresh directory, outside the test mode, makes the age key and
// .sops.yaml and writes the secrets encrypted.
func TestInitOnAFreshDirectoryEncrypts(t *testing.T) {
	realSecrets(t)
	path, rec := initWorld(t, nil, fakeSites(), nil)
	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if r, err := config.Recipients(dir); err != nil || len(r) != 1 {
		t.Fatalf(".sops.yaml recipients %v, %v", r, err)
	}
	wantEncrypted(t, filepath.Join(dir, "secrets.enc.yaml"))
	if !rec.Has("done", "make age key") || rec.Has("warn", "was written in plaintext") {
		t.Errorf("unexpected report:\n%s", rec.Lines())
	}
}

// The test mode is announced once on every run, from main, whatever the
// command: validate --offline with the variable set warns on stderr, and
// without it a plaintext file is refused and nothing warns of the mode.
func TestTheTestModeWarnsOnEveryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "paisans")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	plain, err := os.ReadFile(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	configPath, _ := secretsDir(t, string(plain))
	run := func(mode string) (string, error) {
		cmd := exec.Command(bin, "validate", "--offline", "--config", configPath)
		cmd.Env = append(os.Environ(), config.PlaintextSecretsVariable+"="+mode)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		return stderr.String(), err
	}
	stderr, err := run("1")
	if err != nil {
		t.Fatalf("validate in the test mode: %v\n%s", err, stderr)
	}
	if strings.Count(stderr, config.PlaintextSecretsVariable+"=1 is set") != 1 {
		t.Errorf("stderr does not warn of the test mode once:\n%s", stderr)
	}
	stderr, err = run("")
	if err == nil || strings.Contains(stderr, "=1 is set") || !strings.Contains(stderr, "is not encrypted") {
		t.Errorf("without the test mode: %v\n%s", err, stderr)
	}
}

// The test mode is for the test suite, so no document an operator reads
// names it: a refusal points at the real fixes, and the variable is
// documented for contributors in docs/development.md alone.
func TestOperatorDocsDoNotNameTheTestMode(t *testing.T) {
	for _, name := range []string{"README.md", "CHANGELOG.md", filepath.Join("docs", "commands.md")} {
		data, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(config.PlaintextSecretsVariable)) {
			t.Errorf("%s names %s", name, config.PlaintextSecretsVariable)
		}
	}
}
