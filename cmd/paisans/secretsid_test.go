package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// Every command reads the secrets through config.LoadDeploymentSecrets, which
// refuses a file another deployment wrote. A direct config.LoadSecrets would
// read it without asking, so none is allowed outside a test.
func TestEveryCommandChecksTheSecretsDeployment(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("config.LoadSecrets(")) {
			t.Errorf("%s reads the secrets with config.LoadSecrets, which does not check they belong to this deployment. Use config.LoadDeploymentSecrets", file)
		}
	}
}

// foreignSecretsWorld is the render fixture's declaration beside its secrets,
// which carry another deployment's id.
func foreignSecretsWorld(t *testing.T) (configPath, secretsPath string) {
	t.Helper()
	dir := t.TempDir()
	declaration, err := os.ReadFile(filepath.Join("..", "..", "internal", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	configPath = filepath.Join(dir, "paisans-stack.yaml")
	if err := os.WriteFile(configPath, declaration, 0o644); err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	secrets.ID = otherID
	secretsPath = filepath.Join(dir, "secrets.enc.yaml")
	if err := config.WriteSecrets(secretsPath, secrets, nil); err != nil {
		t.Fatal(err)
	}
	return configPath, secretsPath
}

// The commands that read the secrets refuse another deployment's before
// anything is rendered, written or dialled.
func TestCommandsRefuseAnotherDeploymentsSecrets(t *testing.T) {
	commands := map[string]func(configPath, secretsPath string) error{
		"render": func(c, s string) error {
			return runRender([]string{"--config", c, "--secrets", s, "--out", t.TempDir()})
		},
		"apply": func(c, s string) error {
			return runApply([]string{"--config", c, "--secrets", s}, strings.NewReader(""), io.Discard)
		},
		"apply --site": func(c, s string) error {
			return runApply([]string{"--config", c, "--secrets", s, "--site", "home-a", "--ssh", "nonexistent.invalid"}, strings.NewReader(""), io.Discard)
		},
		"storage init": func(c, s string) error {
			return runStorageInit([]string{"--config", c, "--secrets", s, "--site", "home-a", "--ssh", "nonexistent.invalid"})
		},
		"secrets set": func(c, s string) error {
			return runSecretsSet([]string{"external.cloudflare_api_token", "--config", c, "--secrets", s}, strings.NewReader("not-a-real-token-0001"))
		},
		"secrets prune": func(c, s string) error {
			return runSecretsPrune([]string{"--config", c, "--secrets", s}, strings.NewReader(""), &bytes.Buffer{})
		},
	}
	for name, run := range commands {
		t.Run(name, func(t *testing.T) {
			configPath, secretsPath := foreignSecretsWorld(t)
			before, err := os.ReadFile(secretsPath)
			if err != nil {
				t.Fatal(err)
			}
			withRecorder(t, false)
			var runErr error
			captureStdout(t, func() { runErr = run(configPath, secretsPath) })
			if runErr == nil || !strings.Contains(runErr.Error(), "secrets.enc.yaml belongs to deployment 0c1d…, not f2a9…") {
				t.Fatalf("%s did not refuse another deployment's secrets: %v", name, runErr)
			}
			after, err := os.ReadFile(secretsPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Error("the other deployment's secrets file was changed")
			}
		})
	}
}

// init refuses another deployment's secrets file rather than filling it.
func TestInitRefusesAnotherDeploymentsSecrets(t *testing.T) {
	path, _ := initWorld(t, nil, fakeSites(), nil)
	secretsPath := filepath.Join(filepath.Dir(path), "secrets.enc.yaml")
	if err := config.WriteSecrets(secretsPath, &config.Secrets{Version: config.SecretsVersion, ID: otherID}, nil); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(secretsPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runInitQuietly(t, path)
	if err == nil || !strings.Contains(err.Error(), "belongs to deployment 0c1d…, not f2a9…") {
		t.Fatalf("init did not refuse another deployment's secrets: %v", err)
	}
	if after, _ := os.ReadFile(secretsPath); !bytes.Equal(before, after) {
		t.Error("init changed the other deployment's secrets file")
	}
}

// init writes paisans-stack.yaml's id into the secrets file it creates, and into
// one that had none when it next writes it.
func TestInitWritesTheDeploymentID(t *testing.T) {
	for name, existing := range map[string]bool{"new file": false, "file without an id": true} {
		t.Run(name, func(t *testing.T) {
			path, _ := initWorld(t, nil, fakeSites(), nil)
			secretsPath := filepath.Join(filepath.Dir(path), "secrets.enc.yaml")
			if existing {
				if err := os.WriteFile(secretsPath, []byte("cluster:\n  admin_password: kept-value-0001\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := runInitQuietly(t, path); err != nil {
				t.Fatal(err)
			}
			secrets, err := config.LoadDeploymentSecrets(secretsPath, exampleID)
			if err != nil {
				t.Fatal(err)
			}
			if secrets.ID != exampleID || secrets.Version != config.SecretsVersion {
				t.Errorf("written id %q, version %d", secrets.ID, secrets.Version)
			}
			if existing && secrets.Cluster.AdminPassword != "kept-value-0001" {
				t.Error("init changed a secret that was set")
			}
		})
	}
}
