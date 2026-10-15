package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
)

// secretsDir is a deployment directory with the fixture configuration and a
// secrets file holding only what is given.
func secretsDir(t *testing.T, secrets string) (configPath, secretsPath string) {
	t.Helper()
	dir := t.TempDir()
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	configPath = filepath.Join(dir, "paisans-stack.yaml")
	secretsPath = filepath.Join(dir, "secrets.enc.yaml")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretsPath, []byte(secrets), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, secretsPath
}

func set(t *testing.T, configPath, secretsPath, key, stdin string) (string, error) {
	t.Helper()
	var err error
	printed := captureStdout(t, func() {
		err = runSecretsSet([]string{key, "--config", configPath, "--secrets", secretsPath}, strings.NewReader(stdin))
	})
	return printed, err
}

// A pasted credential goes in through a pipe and comes out only as its name.
func TestSecretsSetWritesAnOwedKeyAndPrintsOnlyItsName(t *testing.T) {
	configPath, secretsPath := secretsDir(t, "version: 1\n")
	const token = "not-a-real-token-0001"

	printed, err := set(t, configPath, secretsPath, "external.acme_dns_token", token+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(printed, "Set external.acme_dns_token.\n") || strings.Contains(printed, token) {
		t.Errorf("printed %q, want the key and never the value", printed)
	}
	secrets, err := config.LoadSecrets(secretsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := secrets.External["acme_dns_token"]; got != token {
		t.Errorf("stored %q, want the value less its one trailing newline", got)
	}

	// An OIDC client is pending for every app with a sign in, which apply
	// creates, and one made by hand may be recorded here instead.
	if _, err := set(t, configPath, secretsPath, "oidc_clients.talk.client_secret", "s3cret"); err != nil {
		t.Errorf("a pending OIDC client secret was refused: %v", err)
	}
}

func TestSecretsSetRefuses(t *testing.T) {
	configPath, secretsPath := secretsDir(t, "version: 1\napps:\n  talk:\n    valkey_password: kept\n")
	for _, tc := range []struct {
		name, key, stdin, want string
	}{
		{"an empty value", "external.smtp_password", "\n", "nothing on stdin"},
		{"an unknown section", "nope.thing", "x", `no "nope" section`},
		{"a generated key nobody created", "apps.talk.database_password", "x", "paisans init"},
		{"a key that names no place", "cluster.root_password", "x", "neither set nor owed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			printed, err := set(t, configPath, secretsPath, tc.key, tc.stdin)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want an error containing %q", err, tc.want)
			}
			if printed != "" {
				t.Errorf("a refusal printed %q", printed)
			}
		})
	}

	// Replacing a key that exists is a rotation, and allowed.
	if _, err := set(t, configPath, secretsPath, "apps.talk.valkey_password", "rotated"); err != nil {
		t.Errorf("rotating an existing key was refused: %v", err)
	}

	// The value never comes from an argument.
	err := runSecretsSet([]string{"external.smtp_password", "--secrets", secretsPath, "hunter2"}, strings.NewReader(""))
	if err == nil {
		t.Error("a value given as an argument was accepted")
	}
}

// An encrypted file stays encrypted, to the recipients beside it.
func TestSecretsSetReencrypts(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	configPath, secretsPath := secretsDir(t, "")
	dir := filepath.Dir(secretsPath)
	keyFile := filepath.Join(dir, "key.txt")
	if err := os.WriteFile(keyFile, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", keyFile)
	sops := "creation_rules:\n  - age: " + identity.Recipient().String() + "\n"
	if err := os.WriteFile(filepath.Join(dir, config.SOPSConfigName), []byte(sops), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteSecrets(secretsPath, &config.Secrets{Version: 1}, []string{identity.Recipient().String()}); err != nil {
		t.Fatal(err)
	}

	const token = "not-a-real-token-0002"
	if _, err := set(t, configPath, secretsPath, "external.acme_dns_token", token); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(secretsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatal("the value was written in plaintext")
	}
	secrets, err := config.LoadSecrets(secretsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !secrets.Encrypted || secrets.External["acme_dns_token"] != token {
		t.Errorf("encrypted %v, token round tripped %v", secrets.Encrypted, secrets.External["acme_dns_token"] == token)
	}
}

// A gateway rendered without the DNS token is a Caddy that can obtain no
// certificate, which only a visitor would discover. It is refused, and the
// refusal says how to supply it.
func TestRenderRefusesAGatewayWithoutItsACMEToken(t *testing.T) {
	fixture, err := os.ReadFile(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	without := strings.Replace(string(fixture), "acme_dns_token:", "unused_token:", 1)
	configPath, secretsPath := secretsDir(t, without)

	out := t.TempDir()
	err = runRender([]string{"--config", configPath, "--secrets", secretsPath, "--out", out})
	if err == nil {
		t.Fatal("a gateway was rendered with no DNS token")
	}
	if !strings.Contains(err.Error(), "paisans secrets set external.acme_dns_token") {
		t.Errorf("the refusal does not say how to fix it:\n%v", err)
	}

	// apply of a site without the gateway role does not need the token, and
	// is stopped later by ssh rather than by this check.
	if err := requireACMEToken(mustLoad(t, configPath), mustSecrets(t, secretsPath), []string{"home-a"}); err != nil {
		t.Errorf("a site with no gateway was refused for the gateway's token: %v", err)
	}
	if err := requireACMEToken(mustLoad(t, configPath), mustSecrets(t, secretsPath), []string{"vm"}); err == nil {
		t.Error("apply of the gateway site was not refused")
	}
	// The monitor runs the same Caddy for its own hostname.
	if err := requireACMEToken(mustLoad(t, configPath), mustSecrets(t, secretsPath), []string{"watch"}); err == nil || !strings.Contains(err.Error(), "watch runs the toolkit's Caddy") {
		t.Errorf("apply of a monitor with managed_by: paisans was not refused: %v", err)
	}
}

func mustLoad(t *testing.T, path string) *config.Config {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func mustSecrets(t *testing.T, path string) *config.Secrets {
	t.Helper()
	s, err := config.LoadSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// writeFixtureSecrets is a deployment directory with the fixture
// configuration and the fixture secrets, changed by edit.
func writeFixtureSecrets(t *testing.T, edit func(*config.Secrets)) (configPath, secretsPath string) {
	t.Helper()
	configPath, secretsPath = secretsDir(t, "")
	secrets, err := config.LoadSecrets(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	edit(secrets)
	if err := config.WriteSecrets(secretsPath, secrets, nil); err != nil {
		t.Fatal(err)
	}
	return configPath, secretsPath
}

func TestSecretsPruneListsThenRemovesOnlyOrphans(t *testing.T) {
	configPath, secretsPath := writeFixtureSecrets(t, func(s *config.Secrets) {
		if s.Sites == nil {
			s.Sites = map[string]config.SiteSecrets{}
		}
		s.Sites["monitor-a"] = config.SiteSecrets{WireGuardPrivateKey: "x"}
		if s.OIDCClients == nil {
			s.OIDCClients = map[string]config.OIDCClient{}
		}
		s.OIDCClients["uptime"] = config.OIDCClient{ClientID: "abc", ClientSecret: "do-not-print"}
	})
	withRecords(t, map[string]string{"vm": fixtureRecord()})
	out := captureStdout(t, func() {
		if err := runSecretsPrune([]string{"--config", configPath, "--secrets", secretsPath}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"sites.monitor-a", "oidc_clients.uptime", "still exists at Pocket ID"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run does not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "do-not-print") {
		t.Fatal("a secret value was printed")
	}
	if s, _ := config.LoadSecrets(secretsPath); s.Sites["monitor-a"].WireGuardPrivateKey == "" {
		t.Fatal("the dry run changed the file")
	}
	captureStdout(t, func() {
		if err := runSecretsPrune([]string{"--config", configPath, "--secrets", secretsPath, "--execute"}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	})
	s, err := config.LoadSecrets(secretsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Sites["monitor-a"]; ok {
		t.Error("sites.monitor-a is still there")
	}
	if _, ok := s.OIDCClients["uptime"]; ok {
		t.Error("oidc_clients.uptime is still there")
	}
	if _, ok := s.Sites["home-a"]; !ok {
		t.Error("a declared site's secrets were pruned")
	}
}

// An encrypted file with no recipient beside it is refused rather than
// written back in plaintext.
func TestSecretsPruneNeverDecryptsTheFile(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	configPath, secretsPath := secretsDir(t, "")
	keyFile := filepath.Join(filepath.Dir(secretsPath), "key.txt")
	if err := os.WriteFile(keyFile, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", keyFile)
	withRecords(t, map[string]string{"vm": fixtureRecord()})
	secrets := &config.Secrets{Version: 1, Sites: map[string]config.SiteSecrets{"monitor-a": {WireGuardPrivateKey: "x"}}}
	if err := config.WriteSecrets(secretsPath, secrets, []string{identity.Recipient().String()}); err != nil {
		t.Fatal(err)
	}
	var runErr error
	captureStdout(t, func() {
		runErr = runSecretsPrune([]string{"--config", configPath, "--secrets", secretsPath, "--execute"}, strings.NewReader(""), &bytes.Buffer{})
	})
	if runErr == nil || !strings.Contains(runErr.Error(), "plaintext") {
		t.Fatalf("err = %v", runErr)
	}
	if s, _ := config.LoadSecrets(secretsPath); !s.Encrypted || s.Sites["monitor-a"].WireGuardPrivateKey == "" {
		t.Error("the file was changed")
	}
}

// withRecords makes every gateway answer with the record given for it; a
// gateway absent from records has none, and one named in down does not
// answer.
func withRecords(t *testing.T, records map[string]string, down ...string) {
	t.Helper()
	saved := registryHost
	registryHost = func(name string, site config.Site, destination string) registry.Runner {
		return &initFake{name: name, down: slices.Contains(down, name), files: map[string]string{deployrecord.Path(fixtureDeployment()): records[name]}}
	}
	t.Cleanup(func() { registryHost = saved })
}

func fixtureDeployment() deployment.Deployment {
	cfg, _ := config.Load(fixtureConfig())
	return cfg.Deployment()
}

func TestPruneRefusesWithoutARecord(t *testing.T) {
	configPath, secretsPath := writeFixtureSecrets(t, func(s *config.Secrets) { s.Sites["monitor-a"] = config.SiteSecrets{WireGuardPrivateKey: "x"} })
	withRecords(t, map[string]string{})
	var err error
	captureStdout(t, func() {
		err = runSecretsPrune([]string{"--config", configPath, "--secrets", secretsPath}, strings.NewReader(""), &bytes.Buffer{})
	})
	if err == nil || !strings.Contains(err.Error(), "--without-record") || !strings.Contains(err.Error(), "vm") {
		t.Errorf("err = %v", err)
	}
}

func TestPruneKeepsWhatTheRecordLists(t *testing.T) {
	configPath, secretsPath := writeFixtureSecrets(t, func(s *config.Secrets) {
		s.Sites["monitor-a"] = config.SiteSecrets{WireGuardPrivateKey: "x"}
		s.Sites["monitor-b"] = config.SiteSecrets{WireGuardPrivateKey: "y"}
	})
	withRecords(t, map[string]string{"vm": deployrecord.Encode(deployrecord.Record{Sites: []string{"home-a", "home-b", "vm", "watch", "monitor-a"}})})
	out := captureStdout(t, func() {
		if err := runSecretsPrune([]string{"--config", configPath, "--secrets", secretsPath, "--execute"}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	})
	s, _ := config.LoadSecrets(secretsPath)
	if _, ok := s.Sites["monitor-a"]; !ok {
		t.Error("a site the record lists lost its secrets")
	}
	if _, ok := s.Sites["monitor-b"]; ok {
		t.Error("sites.monitor-b, in no record, was kept")
	}
	if !strings.Contains(out, "sites.monitor-a is deployed") {
		t.Errorf("prune does not say why it kept monitor-a:\n%s", out)
	}
}

func TestPruneWithoutRecordAsksAtATerminal(t *testing.T) {
	configPath, secretsPath := writeFixtureSecrets(t, func(s *config.Secrets) { s.Sites["monitor-a"] = config.SiteSecrets{WireGuardPrivateKey: "x"} })
	withRecords(t, map[string]string{}, "vm")
	var err error
	captureStdout(t, func() {
		err = runSecretsPrune([]string{"--config", configPath, "--secrets", secretsPath, "--without-record", "--execute"}, strings.NewReader("prune\n"), &bytes.Buffer{})
	})
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Errorf("err = %v", err)
	}
	if s, _ := config.LoadSecrets(secretsPath); s.Sites["monitor-a"].WireGuardPrivateKey == "" {
		t.Error("the file was changed")
	}
}

func fixtureRecord() string {
	cfg, _ := config.Load(fixtureConfig())
	return deployrecord.Encode(deployrecord.FromConfig(cfg))
}

// prune says secrets are kept only for a dropped name that has some.
func TestPruneSaysKeptOnlyWhenThereAreSecrets(t *testing.T) {
	configPath, secretsPath := writeFixtureSecrets(t, func(s *config.Secrets) { s.Sites["monitor-a"] = config.SiteSecrets{WireGuardPrivateKey: "x"} })
	withRecords(t, map[string]string{"vm": deployrecord.Encode(deployrecord.Record{Sites: []string{"home-a", "home-b", "vm", "watch", "monitor-a", "monitor-b"}})})
	out := captureStdout(t, func() {
		if err := runSecretsPrune([]string{"--config", configPath, "--secrets", secretsPath}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "sites.monitor-a is deployed but paisans-stack.yaml no longer declares it; its secrets are kept") {
		t.Errorf("monitor-a:\n%s", out)
	}
	if strings.Contains(out, "monitor-b is deployed but paisans-stack.yaml no longer declares it; its secrets are kept") {
		t.Errorf("monitor-b has no secrets:\n%s", out)
	}
}

// withTwoGateways adds a second gateway site, vm2, to the configuration at
// configPath.
func withTwoGateways(t *testing.T, configPath string) {
	t.Helper()
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	// vm's block, renamed and readdressed, so it carries vm's ssh keys.
	text := string(data)
	start := strings.Index(text, "\n  vm:\n") + 1
	end := start + len("  vm:\n")
	for end < len(text) && (strings.HasPrefix(text[end:], "    ") || strings.HasPrefix(text[end:], "\n")) {
		end += strings.Index(text[end:], "\n") + 1
	}
	block := strings.NewReplacer("  vm:\n", "  vm2:\n", "[gateway, witness]", "[gateway]", "address: 10.44.0.3", "address: 10.44.0.9", "vm.example.org", "vm2.example.org").Replace(text[start:end])
	out := text[:end] + block + text[end:]
	if err := os.WriteFile(configPath, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil || len(cfg.GatewaySites()) != 2 {
		t.Fatalf("two gateways: %v, %v", cfg, err)
	}
}

// A gateway that missed a removal still holds the site's add; the other
// gateway's removal cancels that add, and prune removes the site's secrets.
func TestPruneMergesTheGatewaysRecords(t *testing.T) {
	configPath, secretsPath := writeFixtureSecrets(t, func(s *config.Secrets) { s.Sites["monitor-a"] = config.SiteSecrets{WireGuardPrivateKey: "x"} })
	withTwoGateways(t, configPath)
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Deployment()
	vm, vm2 := &recordFake{files: map[string]string{}}, &recordFake{files: map[string]string{}}
	both := map[string]registry.Runner{"vm": vm, "vm2": vm2}
	all := deployrecord.FromConfig(cfg)
	all.Sites = append(all.Sites, "monitor-a")
	if _, err := deployrecord.Update(both, d, deployrecord.Adding(all), time.Now()); err != nil {
		t.Fatal(err)
	}
	vm2.down = true
	if _, err := deployrecord.Update(both, d, deployrecord.Forgetting(deployrecord.Record{Sites: []string{"monitor-a"}}), time.Now()); err != nil {
		t.Fatal(err)
	}
	withRecords(t, map[string]string{"vm": vm.files[deployrecord.Path(d)], "vm2": vm2.files[deployrecord.Path(d)]})
	captureStdout(t, func() {
		if err := runSecretsPrune([]string{"--config", configPath, "--secrets", secretsPath, "--execute"}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	})
	if s, _ := config.LoadSecrets(secretsPath); s.Sites["monitor-a"].WireGuardPrivateKey != "" {
		t.Error("the gateway that missed the removal kept monitor-a")
	}
}

// A backup destination's credentials are owed for each destination declared,
// so they go in here; a destination nobody declared has none.
func TestSecretsSetTakesADeclaredDestinationsCredentials(t *testing.T) {
	configPath, secretsPath := secretsDir(t, "version: 1\n")
	if _, err := set(t, configPath, secretsPath, "backup.destinations.offsite.access_key_id", "AKIAEXAMPLE\n"); err != nil {
		t.Fatalf("a declared destination's key was refused: %v", err)
	}
	secrets, err := config.LoadSecrets(secretsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := secrets.Backup.Destinations["offsite"].AccessKeyID; got != "AKIAEXAMPLE" {
		t.Errorf("stored %q", got)
	}
	if _, err := set(t, configPath, secretsPath, "backup.destinations.nowhere.access_key_id", "x"); err == nil || !strings.Contains(err.Error(), "neither set nor owed") {
		t.Errorf("an undeclared destination's key: %v", err)
	}
	if _, err := set(t, configPath, secretsPath, "backup.restic_password", "x"); err == nil || !strings.Contains(err.Error(), "paisans init") {
		t.Errorf("a generated backup key nobody created: %v", err)
	}
}

// atTerminal makes stdin read as a terminal and answers each prompt from
// answers by key, recording the keys asked in order. A key with no answer
// fails the read, as an interrupted prompt does.
func atTerminal(t *testing.T, answers map[string]string) (stdin *os.File, asked *[]string) {
	t.Helper()
	savedTerminal, savedRead := isTerminal, readOwedSecret
	t.Cleanup(func() { isTerminal, readOwedSecret = savedTerminal, savedRead })
	isTerminal = func(*os.File) bool { return true }
	asked = &[]string{}
	readOwedSecret = func(o secretsgen.Owed) (string, error) {
		*asked = append(*asked, o.Name)
		if o.Why == "" {
			t.Errorf("%s was asked for without saying why it is owed", o.Name)
		}
		answer, ok := answers[o.Name]
		if !ok {
			return "", errors.New("interrupted")
		}
		return answer, nil
	}
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, asked
}

// owedLeaves is every owed key the walk asks for.
func owedLeaves(t *testing.T, configPath, secretsPath string) []string {
	t.Helper()
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets(secretsPath)
	if err != nil {
		t.Fatal(err)
	}
	return secretsgen.OwedNames(cfg, secrets)
}

// With no key at a terminal, it asks for each owed key in turn, writes each
// answer, skips a blank one, and prints names and never values.
func TestSecretsSetWithNoKeyAsksForEachOwedKey(t *testing.T) {
	configPath, secretsPath := secretsDir(t, "version: 1\n")
	want := owedLeaves(t, configPath, secretsPath)
	if !slices.Contains(want, "external.acme_dns_token") || !slices.Contains(want, "backup.destinations.offsite.access_key_id") {
		t.Fatalf("the fixture owes %v; the test needs the DNS token and a backup key", want)
	}
	answers := map[string]string{}
	for i, key := range want {
		answers[key] = fmt.Sprintf("not-a-real-value-%04d", i)
	}
	answers["external.acme_dns_token"] = ""
	stdin, asked := atTerminal(t, answers)

	var err error
	printed := captureStdout(t, func() {
		err = runSecretsSet([]string{"--config", configPath, "--secrets", secretsPath}, stdin)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*asked, want) {
		t.Errorf("asked for %v, want %v", *asked, want)
	}
	secrets, err := config.LoadSecrets(secretsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range want {
		got, _ := secrets.Get(key)
		if key == "external.acme_dns_token" {
			if got != "" {
				t.Errorf("a blank answer stored %q", got)
			}
			continue
		}
		if got != answers[key] {
			t.Errorf("%s stored %q, want %q", key, got, answers[key])
		}
		if strings.Contains(printed, answers[key]) {
			t.Errorf("printed the value of %s", key)
		}
	}
	if !strings.Contains(printed, "external.acme_dns_token") {
		t.Errorf("printed %q, want the skipped key named as still owed", printed)
	}
}

// An answer already given is written before the next is asked, so an
// interrupted walk keeps what was typed before it.
func TestSecretsSetWithNoKeyKeepsAnswersBeforeAnInterruption(t *testing.T) {
	configPath, secretsPath := secretsDir(t, "version: 1\n")
	want := owedLeaves(t, configPath, secretsPath)
	stdin, _ := atTerminal(t, map[string]string{want[0]: "not-a-real-value-first"})

	var err error
	captureStdout(t, func() {
		err = runSecretsSet([]string{"--config", configPath, "--secrets", secretsPath}, stdin)
	})
	if err == nil {
		t.Fatal("an interrupted prompt was not an error")
	}
	secrets, err := config.LoadSecrets(secretsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := secrets.Get(want[0]); got != "not-a-real-value-first" {
		t.Errorf("%s stored %q, want the answer given before the interruption", want[0], got)
	}
}

// With nothing owed it asks nothing and says so.
func TestSecretsSetWithNoKeyAndNothingOwed(t *testing.T) {
	configPath, secretsPath := secretsDir(t, "version: 1\n")
	stdin, _ := atTerminal(t, map[string]string{})
	saved := readOwedSecret
	for i, key := range owedLeaves(t, configPath, secretsPath) {
		if _, err := set(t, configPath, secretsPath, key, fmt.Sprintf("not-a-real-value-%04d", i)); err != nil {
			t.Fatal(err)
		}
	}
	readOwedSecret = func(o secretsgen.Owed) (string, error) {
		t.Errorf("asked for %s with nothing owed", o.Name)
		return saved(o)
	}

	var err error
	printed := captureStdout(t, func() {
		err = runSecretsSet([]string{"--config", configPath, "--secrets", secretsPath}, stdin)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(printed, "Nothing is owed") {
		t.Errorf("printed %q, want it to say nothing is owed", printed)
	}
}

// With no key and stdin a pipe, it cannot tell which key one value is for.
func TestSecretsSetWithNoKeyRefusesAPipe(t *testing.T) {
	configPath, secretsPath := secretsDir(t, "version: 1\n")
	err := runSecretsSet([]string{"--config", configPath, "--secrets", secretsPath}, strings.NewReader("not-a-real-value\n"))
	if err == nil || !strings.Contains(err.Error(), "name the key") {
		t.Errorf("got %v, want a refusal asking for the key", err)
	}
	if s, _ := config.LoadSecrets(secretsPath); s.External["acme_dns_token"] != "" {
		t.Error("a piped value was written with no key")
	}
}

// keyAtTerminal makes stdin read as a terminal and answers the prompt for a
// named key with answer, recording the key asked for and whether it was
// already set. A nil answer fails the read, as an interrupted prompt does.
func keyAtTerminal(t *testing.T, answer *string) (stdin *os.File, asked *[]string, wasSet *[]bool) {
	t.Helper()
	savedTerminal, savedRead := isTerminal, readKeySecret
	t.Cleanup(func() { isTerminal, readKeySecret = savedTerminal, savedRead })
	isTerminal = func(*os.File) bool { return true }
	asked, wasSet = &[]string{}, &[]bool{}
	readKeySecret = func(key string, set bool) (string, error) {
		*asked = append(*asked, key)
		*wasSet = append(*wasSet, set)
		if answer == nil {
			return "", errors.New("interrupted")
		}
		return *answer, nil
	}
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, asked, wasSet
}

func setAtTerminal(t *testing.T, configPath, secretsPath, key string, stdin *os.File) (string, error) {
	t.Helper()
	var err error
	printed := captureStdout(t, func() {
		err = runSecretsSet([]string{key, "--config", configPath, "--secrets", secretsPath}, stdin)
	})
	return printed, err
}

// Given a key at a terminal, it asks for that key with echo off, says it
// replaces a value already set, writes the answer, and prints only the name.
func TestSecretsSetWithAKeyAtATerminalReplacesASetValue(t *testing.T) {
	configPath, secretsPath := secretsDir(t, "version: 1\n")
	if _, err := set(t, configPath, secretsPath, "external.acme_dns_token", "not-a-real-token-old\n"); err != nil {
		t.Fatal(err)
	}
	answer := "not-a-real-token-new"
	stdin, asked, wasSet := keyAtTerminal(t, &answer)

	printed, err := setAtTerminal(t, configPath, secretsPath, "external.acme_dns_token", stdin)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*asked, []string{"external.acme_dns_token"}) || !slices.Equal(*wasSet, []bool{true}) {
		t.Errorf("asked for %v (set %v), want external.acme_dns_token, set", *asked, *wasSet)
	}
	if !strings.Contains(printed, "Set external.acme_dns_token.") || strings.Contains(printed, answer) || strings.Contains(printed, "not-a-real-token-old") {
		t.Errorf("printed %q, want the key and never a value", printed)
	}
	if s, _ := config.LoadSecrets(secretsPath); s.External["acme_dns_token"] != answer {
		t.Errorf("stored %q, want the answer", s.External["acme_dns_token"])
	}
}

// An owed key not yet set is asked for too, and says it is not set.
func TestSecretsSetWithAKeyAtATerminalSetsAnOwedKey(t *testing.T) {
	configPath, secretsPath := secretsDir(t, "version: 1\n")
	answer := "AKIAEXAMPLE"
	stdin, _, wasSet := keyAtTerminal(t, &answer)

	if _, err := setAtTerminal(t, configPath, secretsPath, "backup.destinations.offsite.access_key_id", stdin); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*wasSet, []bool{false}) {
		t.Errorf("set reported as %v, want not set", *wasSet)
	}
	if s, _ := config.LoadSecrets(secretsPath); s.Backup.Destinations["offsite"].AccessKeyID != answer {
		t.Errorf("stored %q", s.Backup.Destinations["offsite"].AccessKeyID)
	}
}

// An empty answer writes nothing and says so; an interrupted one is an error
// that writes nothing.
func TestSecretsSetWithAKeyAtATerminalWritesNothingOnAnEmptyAnswer(t *testing.T) {
	configPath, secretsPath := secretsDir(t, "version: 1\n")
	if _, err := set(t, configPath, secretsPath, "external.acme_dns_token", "not-a-real-token-old\n"); err != nil {
		t.Fatal(err)
	}
	empty := ""
	stdin, _, _ := keyAtTerminal(t, &empty)
	printed, err := setAtTerminal(t, configPath, secretsPath, "external.acme_dns_token", stdin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(printed, "Nothing was written") {
		t.Errorf("printed %q, want it to say nothing was written", printed)
	}
	if s, _ := config.LoadSecrets(secretsPath); s.External["acme_dns_token"] != "not-a-real-token-old" {
		t.Errorf("an empty answer changed the value to %q", s.External["acme_dns_token"])
	}

	stdin, _, _ = keyAtTerminal(t, nil)
	if _, err := setAtTerminal(t, configPath, secretsPath, "external.acme_dns_token", stdin); err == nil || !strings.Contains(err.Error(), "Nothing was written") {
		t.Errorf("an interrupted prompt: %v", err)
	}
	if s, _ := config.LoadSecrets(secretsPath); s.External["acme_dns_token"] != "not-a-real-token-old" {
		t.Errorf("an interrupted prompt changed the value to %q", s.External["acme_dns_token"])
	}
}

// A key the pipe would refuse is refused at a terminal before anything is
// asked, so nothing is pasted that cannot be written.
func TestSecretsSetWithAKeyAtATerminalRefusesBeforeAsking(t *testing.T) {
	configPath, secretsPath := secretsDir(t, "version: 1\n")
	answer := "not-a-real-value"
	stdin, asked, _ := keyAtTerminal(t, &answer)
	for _, key := range []string{"backup.restic_password", "nonsense.key"} {
		if _, err := setAtTerminal(t, configPath, secretsPath, key, stdin); err == nil {
			t.Errorf("%s was accepted", key)
		}
	}
	if len(*asked) != 0 {
		t.Errorf("asked for %v before refusing", *asked)
	}
}
