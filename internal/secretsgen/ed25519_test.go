package secretsgen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/paisans-software/paisans-cli/internal/blueprints/coretest"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
)

// withEd25519App is the fixture with an app of a catalog blueprint that
// declares an Ed25519 pair.
func withEd25519App(t *testing.T) *config.Config {
	t.Helper()
	coretest.Cache(t)
	repo := filepath.Join(t.TempDir(), "paisans-example")
	for name, content := range map[string]string{
		"catalog.yaml":                       "apiVersion: paisans.catalog/v1\nname: example\n",
		"blueprints/hubby/blueprint.yaml":    "apiVersion: paisans.blueprint/v2\nname: hubby\nservices:\n  - {name: app, image: \"ghcr.io/example/hubby:1.0.0\"}\ndatabase: {engine: none}\nport: 8300\nconfig: {format: env, file: .env}\nsecrets:\n  generate:\n    - {name: hub_key, format: ed25519-openssh, group: hub}\n    - {name: hub_key_public, format: ed25519-public, of: hub_key, group: hub}\n",
		"blueprints/hubby/compose.yaml.tmpl": "services: {}\n",
	} {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "catalog")
	cfg := load(t)
	cfg.Catalogs = map[string]config.Catalog{"ex": {URL: repo, Commit: git("rev-parse", "HEAD")}}
	cfg.Apps["stats"] = config.App{Blueprint: "ex/hubby", Hostname: "stats.example.org",
		Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-a"}}
	if problems := config.ResolveBlueprints(cfg); len(problems) > 0 {
		t.Fatal(problems)
	}
	return cfg
}

// Fill makes an Ed25519 pair without a passphrase, derives a missing public
// half from the private key, and keeps both once set.
func TestFillEd25519Pair(t *testing.T) {
	cfg := withEd25519App(t)
	secrets := &config.Secrets{}
	if _, err := secretsgen.Fill(cfg, secrets); err != nil {
		t.Fatal(err)
	}
	private, _ := secrets.Apps["stats"]["hub_key"].(string)
	public, _ := secrets.Apps["stats"]["hub_key_public"].(string)
	if !strings.HasPrefix(private, "-----BEGIN OPENSSH PRIVATE KEY-----") {
		t.Fatalf("hub_key is %.40q", private)
	}
	if !strings.HasPrefix(public, "ssh-ed25519 ") || strings.HasSuffix(public, "\n") {
		t.Fatalf("hub_key_public is %q", public)
	}
	raw, err := ssh.ParseRawPrivateKey([]byte(private))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))); public != want {
		t.Errorf("public half %q, want %q", public, want)
	}
	if _, has := secrets.Apps["stats"]["passphrase"]; has {
		t.Error("a passphrase was made for a key that has none")
	}
	if again, err := secretsgen.Fill(cfg, secrets); err != nil || again.Changed() {
		t.Fatalf("a second pass: %v %v", again.Generated, err)
	}
	delete(secrets.Apps["stats"], "hub_key_public")
	if _, err := secretsgen.Fill(cfg, secrets); err != nil {
		t.Fatal(err)
	}
	if secrets.Apps["stats"]["hub_key"] != private || secrets.Apps["stats"]["hub_key_public"] != public {
		t.Error("a missing public half was not derived from the kept private key")
	}
	if _, err := secretsgen.ReplacementIn(cfg, "apps.stats.hub_key", secrets); err == nil || !strings.Contains(err.Error(), "apps.stats.hub") {
		t.Errorf("one key of the pair replaced alone: %v", err)
	}
}
