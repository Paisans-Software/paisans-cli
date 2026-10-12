package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// A command line that cannot be run reaches no host.
func TestMeshRekeyRefusesBeforeReachingAHost(t *testing.T) {
	saved := rekeyHost
	rekeyHost = func(string, config.Site, bool) apply.Transport {
		t.Fatal("a host was reached")
		return nil
	}
	t.Cleanup(func() { rekeyHost = saved })
	for args, want := range map[string]string{
		"":              "mesh rekey needs a site",
		"home-a --all":  "--all takes no site",
		"--all home-a":  "--all takes no site",
		"nowhere":       "declares no site nowhere",
		"home-a nobody": "declares no site nobody",
	} {
		err := runMeshRekey(append(strings.Fields(args), "--config", fixtureConfig()))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", args, err, want)
		}
	}
	if err := runMesh([]string{"spin"}); err == nil || !strings.Contains(err.Error(), "rekey") {
		t.Errorf("an unknown subcommand: %v", err)
	}
}

// apply refuses while a rekey is unfinished: a host may run the next key,
// and apply would render the old one back to its peers.
func TestApplyRefusesMidRekey(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "paisans.yaml")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(data), `home-a: { wireguard_private_key: "QEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEA=",`, `home-a: { wireguard_private_key: "QEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEA=", wireguard_next_private_key: "REREREREREREREREREREREREREREREREREREREREREQ=",`, 1)
	if text == string(data) {
		t.Fatal("the fixture's home-a line moved")
	}
	secretsPath := filepath.Join(dir, "secrets.enc.yaml")
	if err := os.WriteFile(secretsPath, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	log := failingSSH(t)
	for _, extra := range [][]string{{"--site", "home-a"}, nil} {
		var err error
		captureStdout(t, func() {
			err = runApply(append([]string{"--config", configPath, "--secrets", secretsPath, "--sudo=false"}, extra...))
		})
		if err == nil || !strings.Contains(err.Error(), "mesh rekey is unfinished") {
			t.Errorf("apply %v: err = %v", extra, err)
		}
	}
	if reached, _ := os.ReadFile(log); len(reached) > 0 {
		t.Errorf("a refused apply reached a host:\n%s", reached)
	}
}
