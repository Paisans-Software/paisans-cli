package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"filippo.io/age"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/registry"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/secretsgen"
	"github.com/paisans-software/paisans-stack/internal/secretsrotate"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// rotateDeployment copies the fixture deployment into a directory of its
// own, its secrets passed through edit, and returns the two paths.
func rotateDeployment(t *testing.T, edit func(string) string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	data, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "paisans-stack.yaml")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if edit != nil {
		text = edit(text)
	}
	secretsPath := filepath.Join(dir, "secrets.enc.yaml")
	if err := os.WriteFile(secretsPath, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, secretsPath
}

// withRotation adds a pending rotation of path to the fixture's secrets.
func withRotation(path string) func(string) string {
	return func(text string) string {
		return text + fmt.Sprintf("\nrotations:\n  %s: \"next-not-a-secret-0001\"\n", path)
	}
}

// rotateFake is a site as secrets rotate reaches it: it answers ssh, reads
// etcd and Patroni as healthy unless told otherwise, holds no registry, and
// refuses to read any other file, so an apply it is handed fails at once.
type rotateFake struct {
	name string
	cfg  *config.Config
	w    *rotateWorld
}

type rotateWorld struct {
	mu       sync.Mutex
	commands []string
	// silent is the sites that do not answer ssh.
	silent map[string]bool
	// sickEtcd is an etcd that reports an unhealthy endpoint.
	sickEtcd bool
	// files is each site's rendered files, by path, as its last apply left
	// them.
	files map[string]map[string]string
}

func (f rotateFake) Describe() string { return "ubuntu@" + f.name + ".local" }

func (f rotateFake) Run(command string) (string, error) {
	f.w.mu.Lock()
	f.w.commands = append(f.w.commands, f.name+": "+command)
	f.w.mu.Unlock()
	if f.w.silent[f.name] {
		return "", fmt.Errorf("ssh: connect to host %s.local port 22: Connection timed out", f.name)
	}
	f.w.mu.Lock()
	files := f.w.files[f.name]
	f.w.mu.Unlock()
	if out, ok := answerLineHashes(command, files); ok {
		return out, nil
	}
	switch {
	case strings.Contains(command, "endpoint health"):
		var list []string
		for _, m := range f.cfg.Etcd.Members {
			list = append(list, fmt.Sprintf(`{"endpoint":"http://%s:2379","health":%t}`, f.cfg.Sites[m].Address, !f.w.sickEtcd))
		}
		return "[" + strings.Join(list, ",") + "]", nil
	case strings.Contains(command, "patronictl") && strings.Contains(command, " list"):
		var list []string
		for i, m := range f.cfg.Cluster.Sites {
			role, state := "Sync Standby", "streaming"
			if i == 0 {
				role, state = "Leader", "running"
			}
			list = append(list, fmt.Sprintf(`{"Member":%q,"Role":%q,"State":%q}`, m, role, state))
		}
		return "[" + strings.Join(list, ",") + "]", nil
	}
	return "", nil
}

func (f rotateFake) RunInput(command, stdin string) (string, error) { return f.Run(command) }

func (f rotateFake) ReadFile(path string) (string, bool, error) {
	if path == registry.Path || strings.Contains(path, "wireguard") {
		return "", false, nil
	}
	return "", false, errors.New("fake: no file here")
}

func (f rotateFake) WriteFile(path, content string, mode uint32) error {
	return errors.New("fake: nothing is written here")
}

// fakeRotateHosts puts every site behind a rotateFake and records what was
// said, so a test can assert on it.
func fakeRotateHosts(t *testing.T) (*rotateWorld, *ui.Recorder) {
	t.Helper()
	w := &rotateWorld{silent: map[string]bool{}}
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	savedHost, savedReporter := rotateHost, reporterOverride
	rotateHost = func(name string, site config.Site, sudo bool) apply.Transport {
		return rotateFake{name: name, cfg: cfg, w: w}
	}
	rec := &ui.Recorder{}
	reporterOverride = rec
	t.Cleanup(func() { rotateHost, reporterOverride = savedHost, savedReporter })
	return w, rec
}

// noRotateHost fails the test if a host is reached.
func noRotateHost(t *testing.T) {
	t.Helper()
	saved := rotateHost
	rotateHost = func(string, config.Site, bool) apply.Transport {
		t.Fatal("a host was reached")
		return nil
	}
	t.Cleanup(func() { rotateHost = saved })
}

// terminalStdin is stdin holding answer, read as a terminal when terminal is
// true.
func terminalStdin(t *testing.T, answer string, terminal bool) *os.File {
	t.Helper()
	saved := isTerminal
	isTerminal = func(*os.File) bool { return terminal }
	t.Cleanup(func() { isTerminal = saved })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString(answer)
	w.Close()
	t.Cleanup(func() { r.Close() })
	return r
}

func rotateArgs(configPath, secretsPath string, extra ...string) []string {
	return append(extra, "--config", configPath, "--secrets", secretsPath, "--sudo=false")
}

func paths(rs []secretsrotate.Rotator) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Path())
	}
	return out
}

// --all is every secret in scope that the deployment has, in the order the
// spec gives: what works from the internet first, then what only the mesh
// reaches.
func TestSecretsRotateAllOrder(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	s, err := config.LoadDeploymentSecrets(fixtureSecretsPath(), cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	rs, kept, err := rotateTargets(cfg, s, nil, true, rotateWiring{})
	if err != nil {
		t.Fatal(err)
	}
	// Every OIDC client secret is minted by `oidc client create`, the
	// homeserver's and the gate's among them: only the DNS token and the
	// SMTP password are pasted.
	for _, r := range rs {
		if strings.HasPrefix(r.Path(), "oidc_clients.") && secretsrotate.Pasted(r) {
			t.Errorf("%s is rotated as a pasted secret", r.Path())
		}
	}
	want := []string{
		"external.acme_dns_token",
		"external.smtp_password",
		"oidc_clients.blog.client_secret",
		"oidc_clients.chat.client_secret",
		"oidc_clients.docs.client_secret",
		"oidc_clients.gate.client_secret",
		"oidc_clients.invite.client_secret",
		"oidc_clients.status.client_secret",
		"oidc_clients.talk.client_secret",
		"apps.talk.oauth",
		"apps.talk.app_secret",
		"apps.gate.cookie_secret",
		"apps.status.session_secret",
		"apps.invite.session_key",
		"apps.chat.mas_signing_key",
		"apps.chat.mas_matrix_secret",
		"apps.blog.s3_secret_access_key",
		"apps.docs.s3_secret_access_key",
		"apps.talk.s3_secret_access_key",
		"apps.auth.static_api_key",
		"apps.status.admin_password",
		"cluster.superuser_password",
		"cluster.admin_password",
		"cluster.standby_password",
		"apps.auth.database_password",
		"apps.blog.database_password",
		"apps.chat.database_password",
		"apps.docs.database_password",
		"apps.invite.database_password",
		"apps.talk.database_password",
		"apps.talk.rabbitmq_password",
		"apps.talk.valkey_password",
		"apps.talk.mercure_jwt_secret",
		"apps.docs.redis_password",
		"apps.docs.utils_secret",
		"storage.garage.admin_token",
		"storage.garage.rpc_secret",
		"sites.home-a.heartbeat_token",
		"sites.home-b.heartbeat_token",
		"sites.vm.heartbeat_token",
		"sites.watch.heartbeat_token",
		"apps.auth.admins_heartbeat_token",
	}
	if got := paths(rs); !slices.Equal(got, want) {
		t.Errorf("--all order:\n got  %s\n want %s", strings.Join(got, "\n      "), strings.Join(want, "\n      "))
	}
	var keptPaths []string
	for _, k := range kept {
		keptPaths = append(keptPaths, k.Path)
		if k.Reason == "" {
			t.Errorf("%s is kept with no reason", k.Path)
		}
	}
	for _, p := range []string{
		"apps.auth.encryption_key",
		"apps.chat.mas_encryption_secret",
		"apps.docs.outline_secret_key",
		"apps.invite.email_hash_key",
		"sites.*.wireguard_private_key",
	} {
		if !slices.Contains(keptPaths, p) {
			t.Errorf("%s is not listed as kept: %v", p, keptPaths)
		}
	}
}

// A run builds a rotator for every rotation an earlier run left pending,
// named or not, since the engine finishes those first.
func TestSecretsRotateResumesEveryPendingPath(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	s, err := config.LoadDeploymentSecrets(fixtureSecretsPath(), cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.Rotations = map[string]string{"cluster.standby_password": "next-not-a-secret-0001"}
	rs, _, err := rotateTargets(cfg, s, []string{"apps.gate.cookie_secret"}, false, rotateWiring{})
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(rs); !slices.Equal(got, []string{"apps.gate.cookie_secret", "cluster.standby_password"}) {
		t.Errorf("rotators = %v", got)
	}
}

// Paths are refused, each with why, before any host is reached: out of
// scope, someone else's to rotate, or not a secret at all.
func TestSecretsRotateRefusesBeforeReachingAHost(t *testing.T) {
	noRotateHost(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	for args, want := range map[string]string{
		"":                                    "names the secrets to rotate",
		"--all apps.gate.cookie_secret":       "--all takes no path",
		"apps.auth.encryption_key":            "unreadable",
		"apps.chat.mas_encryption_secret":     "unreadable",
		"sites.home-a.wireguard_private_key":  "paisans mesh rekey",
		"apps.talk.oauth_passphrase":          "apps.talk.oauth",
		"external.nothing":                    "no rotation",
		"apps.talk.nonsense":                  "no rotation",
		"cluster.standby_password nonsense.x": "no rotation",
	} {
		err := runSecretsRotate(rotateArgs(configPath, secretsPath, strings.Fields(args)...), strings.NewReader(""), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", args, err, want)
		}
	}
}

// Review Focus 5: a rotation does not start while a mesh rekey is
// unfinished, and says which command finishes it.
func TestSecretsRotateRefusedMidRekey(t *testing.T) {
	noRotateHost(t)
	configPath, secretsPath := rotateDeployment(t, func(text string) string {
		return strings.Replace(text, `home-a: { wireguard_private_key: "QEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEA=",`, `home-a: { wireguard_private_key: "QEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEA=", wireguard_next_private_key: "REREREREREREREREREREREREREREREREREREREREREQ=",`, 1)
	})
	for _, args := range [][]string{{"--all"}, {"apps.gate.cookie_secret", "--execute"}} {
		err := runSecretsRotate(rotateArgs(configPath, secretsPath, args...), strings.NewReader(""), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "paisans mesh rekey") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
}

// Review Focus 5: apply refuses while a rotation is pending, with and
// without --site, and names the command that finishes it.
func TestApplyRefusesMidRotation(t *testing.T) {
	configPath, secretsPath := rotateDeployment(t, withRotation("apps.gate.cookie_secret"))
	log := failingSSH(t)
	for _, extra := range [][]string{{"--site", "home-a"}, nil} {
		var err error
		captureStdout(t, func() {
			err = runApply(append([]string{"--config", configPath, "--secrets", secretsPath, "--sudo=false"}, extra...), strings.NewReader(""), io.Discard)
		})
		if err == nil || !strings.Contains(err.Error(), "paisans secrets rotate") || !strings.Contains(err.Error(), "apps.gate.cookie_secret") {
			t.Errorf("apply %v: err = %v", extra, err)
		}
	}
	if reached, _ := os.ReadFile(log); len(reached) > 0 {
		t.Errorf("a refused apply reached a host:\n%s", reached)
	}
}

// A secret with nothing to check it is listed as kept with its reason, in
// the dry run, beside the rotations that do run; and the dry run asks
// nothing.
func TestSecretsRotateListsKeptSecrets(t *testing.T) {
	_, rec := fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	before, _ := os.ReadFile(secretsPath)
	var out bytes.Buffer
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "apps.talk.rabbitmq_password", "apps.gate.cookie_secret"), terminalStdin(t, "", true), &out)
	if err != nil {
		t.Fatalf("err = %v\n%s", err, rec.Lines())
	}
	k := rec.Index("done", "apps.talk.rabbitmq_password")
	if k < 0 || rec.Index("section", "kept") > k {
		t.Errorf("the rabbitmq password is not listed as kept:\n%s", rec.Lines())
	}
	if rec.Index("explain", "starts no RabbitMQ") < k {
		t.Errorf("the kept secret's reason is not given:\n%s", rec.Lines())
	}
	if rec.Index("section", "rotate apps.gate.cookie_secret") < 0 {
		t.Errorf("the cookie secret's rotation is not planned:\n%s", rec.Lines())
	}
	if rec.Index("section", "rotate apps.talk.rabbitmq_password") >= 0 {
		t.Errorf("a kept secret is planned for rotation:\n%s", rec.Lines())
	}
	if strings.Contains(out.String(), "Type 'sign out'") {
		t.Error("a dry run asked for sign out")
	}
	if after, _ := os.ReadFile(secretsPath); !bytes.Equal(before, after) {
		t.Error("a dry run changed the secrets file")
	}
}

// A run that signs members out asks for `sign out` at a terminal; without
// one it is refused, and so is any other answer, with nothing changed.
func TestSecretsRotateAsksSignOut(t *testing.T) {
	for _, tc := range []struct {
		name     string
		answer   string
		terminal bool
		want     string
	}{
		{"no terminal", "sign out\n", false, "stdin is not one"},
		{"wrong word", "yes\n", true, `"yes" is not sign out`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, rec := fakeRotateHosts(t)
			configPath, secretsPath := rotateDeployment(t, nil)
			before, _ := os.ReadFile(secretsPath)
			err := runSecretsRotate(rotateArgs(configPath, secretsPath, "apps.gate.cookie_secret", "--execute"), terminalStdin(t, tc.answer, tc.terminal), &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Nothing was changed") {
				t.Errorf("err = %v\n%s", err, rec.Lines())
			}
			if strings.Count(err.Error(), "Nothing was changed") != 1 {
				t.Errorf("err = %v", err)
			}
			for _, c := range w.commands {
				if strings.Contains(c, registry.Path) {
					t.Errorf("a host was claimed before sign out was answered: %s", c)
				}
			}
			if after, _ := os.ReadFile(secretsPath); !bytes.Equal(before, after) {
				t.Error("a refused run changed the secrets file")
			}
		})
	}

	// Typed at a terminal, the run goes on: stage 1 writes the next value,
	// and the fake host refuses the apply after it.
	w, rec := fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	var out bytes.Buffer
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "apps.gate.cookie_secret", "--execute"), terminalStdin(t, "sign out\n", true), &out)
	if err == nil || strings.Contains(err.Error(), "sign out") {
		t.Errorf("err = %v\n%s", err, rec.Lines())
	}
	if !strings.Contains(out.String(), "Type 'sign out'") {
		t.Errorf("sign out was not asked:\n%s", out.String())
	}
	s, lerr := config.LoadDeploymentSecrets(secretsPath, "")
	if lerr != nil {
		t.Fatal(lerr)
	}
	if next := s.Rotations["apps.gate.cookie_secret"]; next == "" || next == "fixture-not-a-secret-gate-cookie" {
		t.Errorf("stage 1 did not write a next value: %v", s.Rotations)
	}
	claimed := false
	for _, c := range w.commands {
		claimed = claimed || strings.Contains(c, registry.Path)
	}
	if !claimed {
		t.Errorf("no host was claimed before it was written to:\n%s", strings.Join(w.commands, "\n"))
	}
}

// A run that signs nobody out asks nothing.
func TestSecretsRotateAsksNothingWithoutSignOut(t *testing.T) {
	_, rec := fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	var out bytes.Buffer
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "storage.garage.admin_token", "--execute"), terminalStdin(t, "", false), &out)
	if strings.Contains(out.String(), "sign out") {
		t.Errorf("asked:\n%s\n%s", out.String(), rec.Lines())
	}
	// Past the confirmation: stage 1 wrote the next value, and the fake host
	// refused the write after it.
	if err == nil || strings.Contains(err.Error(), "sign out") || strings.Contains(err.Error(), "terminal") {
		t.Errorf("err = %v", err)
	}
	s, lerr := config.LoadDeploymentSecrets(secretsPath, "")
	if lerr != nil {
		t.Fatal(lerr)
	}
	if s.Rotations["storage.garage.admin_token"] == "" {
		t.Errorf("the run stopped before stage 1: %v\n%s", err, rec.Lines())
	}
}

// A site that does not answer over ssh, and an unhealthy cluster, refuse
// the run before anything changes.
func TestSecretsRotateRefusesAnUnreadyDeployment(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*rotateWorld)
		want  string
	}{
		{"silent site", func(w *rotateWorld) { w.silent["watch"] = true }, "watch does not answer over ssh"},
		{"sick etcd", func(w *rotateWorld) { w.sickEtcd = true }, "etcd is unhealthy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, rec := fakeRotateHosts(t)
			tc.setup(w)
			configPath, secretsPath := rotateDeployment(t, nil)
			before, _ := os.ReadFile(secretsPath)
			err := runSecretsRotate(rotateArgs(configPath, secretsPath, "storage.garage.admin_token", "--execute"), strings.NewReader(""), &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Nothing was changed") {
				t.Errorf("err = %v\n%s", err, rec.Lines())
			}
			if after, _ := os.ReadFile(secretsPath); !bytes.Equal(before, after) {
				t.Error("a refused run changed the secrets file")
			}
		})
	}
}

// The homeserver's client secret and the gate's are rotated by `oidc client
// create --rotate-secret`, as every other client's is, never as a pasted
// secret a person mints at Pocket ID first.
func TestSecretsRotateMintsTheHomeserversAndTheGatesClientSecrets(t *testing.T) {
	for _, path := range []string{"oidc_clients.chat.client_secret", "oidc_clients.gate.client_secret"} {
		r, err := rotatorFor(path, rotateWiring{})
		if err != nil {
			t.Fatal(err)
		}
		if secretsrotate.Pasted(r) {
			t.Errorf("%s is rotated as a pasted secret", path)
		}
		if _, ok := r.(secretsrotate.Whole); !ok {
			t.Errorf("%s is not rotated by oidc client create", path)
		}
	}
}

// The plan says what each rotation's cost alone does not: who deletes the old
// OIDC and S3 secrets and when, a one-member cluster's restart in place, and
// every node a Garage rpc_secret step covers.
func TestSecretsRotateNotes(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"oidc_clients.docs.client_secret": "deleted at Pocket ID",
		"apps.docs.s3_secret_access_key":  "deleted from Garage",
		"storage.garage.rpc_secret":       "home-a, home-b",
		"external.acme_dns_token":         "paisans secrets set external.acme_dns_token",
		"external.smtp_password":          "paisans secrets set external.smtp_password",
		"oidc_clients.chat.client_secret": "oidc client create --app chat --rotate-secret",
		"oidc_clients.gate.client_secret": "oidc client create --app gate --rotate-secret",
	} {
		if got := strings.Join(rotationNotes(cfg, path), " "); !strings.Contains(got, want) {
			t.Errorf("%s: notes %q, want %q", path, got, want)
		}
	}
	if got := rotationNotes(cfg, "cluster.standby_password"); len(got) != 0 {
		t.Errorf("a two-member cluster's role notes %q", got)
	}
	cfg.Cluster.Sites = cfg.Cluster.Sites[:1]
	if got := strings.Join(rotationNotes(cfg, "cluster.standby_password"), " "); !strings.Contains(got, "in place") {
		t.Errorf("a one-member cluster's role notes %q", got)
	}
}

// The secrets file takes the OAuth unit's path, which names no key of its
// own, under rotations, encrypted and back.
func TestRotationsHoldTheOAuthUnitPath(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(dir, config.SOPSConfigName), []byte("creation_rules:\n  - age: "+identity.Recipient().String()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := config.LoadDeploymentSecrets(fixtureSecretsPath(), "")
	if err != nil {
		t.Fatal(err)
	}
	s.Encrypted = true
	unit := "oauth_passphrase: p\noauth_encryption_key: k\n"
	s.Rotations = map[string]string{"apps.talk.oauth": unit}
	store := secretsStore{path: filepath.Join(dir, "secrets.enc.yaml"), id: s.ID}
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}
	back, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if back.Rotations["apps.talk.oauth"] != unit || !back.Encrypted || back.UnknownKeys {
		t.Errorf("rotations = %q, encrypted %v, unknown keys %v", back.Rotations, back.Encrypted, back.UnknownKeys)
	}
}

// --all on a file that lacks a secret init generates is refused, naming
// init, before any host is reached: --all would leave that secret out.
func TestSecretsRotateAllRefusesAnUnfilledFile(t *testing.T) {
	noRotateHost(t)
	configPath, secretsPath := rotateDeployment(t, func(text string) string {
		out := strings.Replace(text, "    cookie_secret: \"fixture-not-a-secret-gate-cookie\"\n", "    {}\n", 1)
		if out == text {
			t.Fatal("the fixture's gate cookie line moved")
		}
		return out
	})
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "--all"), strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "paisans init") || !strings.Contains(err.Error(), "apps.gate.cookie_secret") {
		t.Errorf("err = %v", err)
	}
}

// applied writes to every site the files rendered with the secrets at
// secretsPath, edited by edit when it is not nil, as an apply leaves them.
func (w *rotateWorld) applied(t *testing.T, configPath, secretsPath string, edit func(*config.Secrets)) {
	t.Helper()
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := config.LoadDeploymentSecrets(secretsPath, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(s)
	}
	plan, err := render.Build(cfg, s)
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.files = map[string]map[string]string{}
	for _, site := range cfg.SiteNames() {
		w.files[site] = map[string]string{}
		for _, f := range plan.Files {
			if rel, ok := strings.CutPrefix(f.Path, site+"/"); ok {
				w.files[site]["/"+rel] = f.Content
			}
		}
	}
}

// answerLineHashes answers the command a pasted secret's check runs, which
// prints the SHA-256 of each line of one file, from files, as the shell
// would; ok is false for any other command.
func answerLineHashes(command string, files map[string]string) (string, bool) {
	rest, ok := strings.CutPrefix(command, "f='")
	if !ok || !strings.Contains(command, "sha256sum") {
		return "", false
	}
	path, _, _ := strings.Cut(rest, "'")
	content, ok := files[path]
	if !ok {
		return "absent\n", true
	}
	var out strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
		digest := sha256.Sum256([]byte(line))
		out.WriteString(hex.EncodeToString(digest[:]) + "  -\n")
	}
	return out.String(), true
}

// A pasted secret replaced and applied before, which every host using it
// runs, is listed with --replaced and not rotated.
func TestSecretsRotateReplacedIsListed(t *testing.T) {
	w, rec := fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	w.applied(t, configPath, secretsPath, nil)
	before, _ := os.ReadFile(secretsPath)
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "external.acme_dns_token", "--replaced", "external.acme_dns_token"), terminalStdin(t, "", true), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("err = %v\n%s", err, rec.Lines())
	}
	k := rec.Index("done", "external.acme_dns_token")
	if k < 0 || rec.Index("section", "kept") > k {
		t.Errorf("the DNS token is not listed as kept:\n%s", rec.Lines())
	}
	if rec.Index("explain", "replaced earlier, hosts already run it") < k {
		t.Errorf("the reason is not given:\n%s", rec.Lines())
	}
	if rec.Index("section", "rotate external.acme_dns_token") >= 0 {
		t.Errorf("a replaced secret is planned for rotation:\n%s", rec.Lines())
	}
	if after, _ := os.ReadFile(secretsPath); !bytes.Equal(before, after) {
		t.Error("the secrets file changed")
	}
}

// --replaced takes only a pasted secret, and is refused before any host is
// reached for a generated one.
func TestSecretsRotateReplacedRefusesAGeneratedSecret(t *testing.T) {
	noRotateHost(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "--all", "--replaced", "cluster.standby_password"), terminalStdin(t, "", true), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "pasted secret") || !strings.Contains(err.Error(), "cluster.standby_password") {
		t.Errorf("err = %v", err)
	}
}

// --replaced is refused for a pasted secret some host does not run yet: it
// still has a value to hand out.
func TestSecretsRotateReplacedRefusesWhatHostsDoNotRun(t *testing.T) {
	w, rec := fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	w.applied(t, configPath, secretsPath, func(s *config.Secrets) {
		if err := s.Set("external.acme_dns_token", "the-token-before-it-was-replaced"); err != nil {
			t.Fatal(err)
		}
	})
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "external.acme_dns_token", "--replaced", "external.acme_dns_token"), terminalStdin(t, "", true), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "Rotate it without --replaced") {
		t.Errorf("err = %v\n%s", err, rec.Lines())
	}
}

// --all refuses a pasted secret every host already runs, and passes it with
// --replaced.
func TestSecretsRotateAllPassesWithReplaced(t *testing.T) {
	w, rec := fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	w.applied(t, configPath, secretsPath, nil)
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "--all"), terminalStdin(t, "", true), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--replaced") {
		t.Fatalf("--all without --replaced: err = %v", err)
	}
	pasted := []string{"external.acme_dns_token", "external.smtp_password"}
	args := []string{"--all"}
	for _, p := range pasted {
		args = append(args, "--replaced", p)
	}
	// The fake hosts hold no Patroni cluster document, so the plan stops at
	// the cluster's admin role, after every pasted secret: what matters
	// here is that none of those refuses it.
	err = runSecretsRotate(rotateArgs(configPath, secretsPath, args...), terminalStdin(t, "", true), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cluster.admin_password") {
		t.Fatalf("--all with --replaced: err = %v\n%s", err, rec.Lines())
	}
	for _, p := range pasted {
		if strings.Contains(err.Error(), p) {
			t.Errorf("--all with --replaced is refused on %s: %v", p, err)
		}
	}
}

// withRemaining records paths as an earlier --all run's rotate_remaining.
func withRemaining(paths ...string) func(string) string {
	return func(text string) string {
		text += "\nrotate_remaining:\n"
		for _, p := range paths {
			text += "  - " + p + "\n"
		}
		return text
	}
}

// --restart starts an --all run again, and means nothing without one.
func TestSecretsRotateRestartTakesAll(t *testing.T) {
	noRotateHost(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "cluster.standby_password", "--restart"), strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--restart takes --all") {
		t.Errorf("err = %v", err)
	}
}

// A resumed --all says so on --execute at the default verbosity, where the
// plan itself is not shown: an operator who ran --all expects every secret.
func TestSecretsRotateResumeIsNotedOnExecute(t *testing.T) {
	_, rec := fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, withRemaining("storage.garage.admin_token"))
	// The fake host refuses the apply after stage 1; the note comes first.
	_ = runSecretsRotate(rotateArgs(configPath, secretsPath, "--all", "--execute"), terminalStdin(t, "", false), &bytes.Buffer{})
	if !rec.Has("note", "resuming the --all run") {
		t.Errorf("the resume is not noted:\n%s", rec.Lines())
	}
}

// A path an earlier --all run left that this deployment no longer has a
// rotation of is refused, naming it and --all --restart.
func TestSecretsRotateResumeRefusesAPathWithNoRotation(t *testing.T) {
	fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, withRemaining("apps.gone.database_password"))
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "--all"), strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "apps.gone.database_password") || !strings.Contains(err.Error(), "--all --restart") {
		t.Errorf("err = %v", err)
	}
}

// A resumed --all whose every path left is now kept clears what the file
// records on --execute, so the next --all rotates everything.
func TestSecretsRotateResumeOfOnlyKeptClearsTheRecord(t *testing.T) {
	_, rec := fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, withRemaining("apps.talk.rabbitmq_password"))
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "--all", "--execute"), strings.NewReader(""), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("err = %v\n%s", err, rec.Lines())
	}
	s, err := config.LoadDeploymentSecrets(secretsPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.RotateRemaining) != 0 {
		t.Errorf("rotate_remaining %v", s.RotateRemaining)
	}
	if !rec.Has("result", "not rotated again") {
		t.Errorf("the result does not say what was not rotated again:\n%s", rec.Lines())
	}
}

// --all --restart is refused while a rotation is pending.
func TestSecretsRotateRestartRefusedWhilePending(t *testing.T) {
	fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, withRotation("apps.gate.cookie_secret"))
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "--all", "--restart"), strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--restart") || !strings.Contains(err.Error(), "apps.gate.cookie_secret") {
		t.Errorf("err = %v", err)
	}
}

// Every command that renders from the secrets file and reaches a host
// refuses while a rotation is pending, before any host is reached, naming
// the command that finishes it: each would render the old value and hand it
// back to hosts that may already run the next one. site remove refuses the
// same way, after its own refusals, which the fixture trips first.
func TestRenderingCommandsRefuseMidRotation(t *testing.T) {
	const pending = "cluster.standby_password"
	for name, run := range map[string]func(configPath, secretsPath string) error{
		"site add": func(c, s string) error {
			return runSiteAdd([]string{"home-b", "--config", c, "--secrets", s, "--sudo=false"}, strings.NewReader(""), io.Discard)
		},
		"storage add": func(c, s string) error {
			return runStorageAdd([]string{"--config", c, "--secrets", s, "--sudo=false"})
		},
		"storage rotate-key": func(c, s string) error {
			return runStorageRotateKey([]string{"--app", "talk", "--config", c, "--secrets", s, "--sudo=false"})
		},
		"app remove": func(c, s string) error {
			return runAppRemove([]string{"retired", "--config", c, "--secrets", s, "--sudo=false"}, strings.NewReader(""), &bytes.Buffer{})
		},
		"mesh rekey": func(c, s string) error {
			return runMeshRekey([]string{"home-a", "--config", c, "--secrets", s, "--sudo=false"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			configPath, secretsPath := rotateDeployment(t, withRotation(pending))
			log := failingSSH(t)
			var err error
			captureStdout(t, func() { err = run(configPath, secretsPath) })
			if err == nil || !strings.Contains(err.Error(), "paisans secrets rotate "+pending) || !strings.Contains(err.Error(), name) {
				t.Errorf("err = %v", err)
			}
			if reached, _ := os.ReadFile(log); len(reached) > 0 {
				t.Errorf("a refused %s reached a host:\n%s", name, reached)
			}
		})
	}
}

// --replaced reads the hosts per secret: a caddy.env that differs from the
// render only in a line that does not carry the token, Eg: a comment an
// upgrade changed, still runs the token the file holds.
func TestSecretsRotateReplacedIgnoresUnrelatedLines(t *testing.T) {
	w, rec := fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	w.applied(t, configPath, secretsPath, nil)
	for _, files := range w.files {
		for path, content := range files {
			if strings.HasSuffix(path, "/caddy.env") {
				files[path] = "# written by an older toolkit\n" + content
			}
		}
	}
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "external.acme_dns_token", "--replaced", "external.acme_dns_token"), terminalStdin(t, "", true), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("err = %v\n%s", err, rec.Lines())
	}
}

// A pending rotation no rotation can be built for is refused, naming the way
// to end it by hand; and so is apply while it is pending.
func TestSecretsRotatePendingWithoutRotationNamesTheWayOut(t *testing.T) {
	noRotateHost(t)
	configPath, secretsPath := rotateDeployment(t, withRotation("apps.auth.encryption_key"))
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "apps.gate.cookie_secret"), strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `"A rotation that cannot finish"`) {
		t.Errorf("secrets rotate: err = %v", err)
	}
	var applyErr error
	captureStdout(t, func() {
		applyErr = runApply([]string{"--config", configPath, "--secrets", secretsPath, "--sudo=false", "--site", "home-a"}, strings.NewReader(""), io.Discard)
	})
	if applyErr == nil || !strings.Contains(applyErr.Error(), `"A rotation that cannot finish"`) {
		t.Errorf("apply: err = %v", applyErr)
	}
}

// Every secret init generates, for every kind the fixture declares, is
// either rotated by --all or listed as kept with a reason. A generated key
// that is neither is one an evicted admin keeps after a rotation that
// claimed to be everything, with nothing said.
func TestSecretsRotateAllCoversEveryGeneratedSecret(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	s, err := config.LoadDeploymentSecrets(fixtureSecretsPath(), cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	filled, err := secretsgen.Fill(cfg, s)
	if err != nil {
		t.Fatal(err)
	}
	generated := append(append([]string{}, filled.Generated...), filled.Kept...)
	if !slices.Contains(generated, "apps.invite.session_key") {
		t.Fatalf("init's list lacks the invite keys, so this test reads the wrong list: %v", generated)
	}
	rs, kept, err := rotateTargets(cfg, s, nil, true, rotateWiring{})
	if err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{}
	for _, r := range rs {
		covered[r.Path()] = true
	}
	for _, k := range kept {
		covered[k.Path] = true
	}
	for _, path := range generated {
		parts := strings.Split(path, ".")
		alias := path
		switch {
		case len(parts) == 3 && parts[0] == "apps" && strings.HasPrefix(parts[2], "oauth_"):
			// Mbin's OAuth2 unit rotates as one.
			alias = "apps." + parts[1] + ".oauth"
		case len(parts) == 3 && parts[0] == "apps" && parts[2] == "s3_access_key_id":
			// Rotated with its secret, as one Garage key.
			alias = "apps." + parts[1] + ".s3_secret_access_key"
		case len(parts) == 3 && parts[0] == "sites" && parts[2] == "wireguard_private_key":
			alias = "sites.*.wireguard_private_key"
		}
		if !covered[path] && !covered[alias] {
			t.Errorf("%s is generated by init and neither rotated nor kept by --all", path)
		}
	}
}

// secrets rotate keeps every backup secret, with why: the two keys encrypt
// the backups already taken, and a destination's credentials are replaced at
// the provider that issued them.
func TestSecretsRotateKeepsTheBackupSecrets(t *testing.T) {
	for path, want := range map[string]string{
		"backup.restic_password":                        "backup already taken",
		"backup.walg_libsodium_key":                     "backup already taken",
		"backup.destinations.offsite.access_key_id":     "provider",
		"backup.destinations.offsite.secret_access_key": "provider",
	} {
		if _, err := rotatorFor(path, rotateWiring{}); err == nil || !strings.Contains(err.Error(), "is kept") || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", path, err)
		}
	}

	cfg, err := config.Load(filepath.Join("..", "..", "internal", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := &config.Secrets{Backup: config.BackupSecrets{
		ResticPassword:   "r",
		WALGLibsodiumKey: "w",
		Destinations:     map[string]config.DestinationSecrets{"offsite": {AccessKeyID: "a", SecretAccessKey: "b"}},
	}}
	rs, kept, err := rotateAll(cfg, s, rotateWiring{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if strings.HasPrefix(r.Path(), "backup.") {
			t.Errorf("--all rotates %s", r.Path())
		}
	}
	var paths []string
	for _, k := range kept {
		paths = append(paths, k.Path)
	}
	for _, want := range []string{"backup.restic_password", "backup.walg_libsodium_key", "backup.destinations.*"} {
		if !slices.Contains(paths, want) {
			t.Errorf("--all does not list %s as kept: %v", want, paths)
		}
	}
}

// A destination's credentials that paisans-stack.yaml no longer declares are an
// orphan for secrets prune, not a secret --all keeps, so --all lists the
// destinations as kept only for one the configuration still names.
func TestSecretsRotateAllKeepsNoOrphanedDestination(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "internal", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := &config.Secrets{Backup: config.BackupSecrets{
		Destinations: map[string]config.DestinationSecrets{"gone": {AccessKeyID: "a", SecretAccessKey: "b"}},
	}}
	_, kept, err := rotateAll(cfg, s, rotateWiring{})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range kept {
		if k.Path == "backup.destinations.*" {
			t.Errorf("--all lists backup.destinations.* as kept with only an orphaned destination's credentials")
		}
	}
}

// A Pocket ID app's admins heartbeat token is rotated by name, at the cost of
// the admins heartbeat's pushes refused while the monitors and pushers take
// it, and signs nobody out.
func TestSecretsRotateNamesTheAdminsHeartbeatToken(t *testing.T) {
	r, err := rotatorFor("apps.auth.admins_heartbeat_token", rotateWiring{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Path() != "apps.auth.admins_heartbeat_token" {
		t.Errorf("path %q", r.Path())
	}
	if c := r.Cost(); c.SignsOut || !strings.Contains(c.Downtime, "admins heartbeat") {
		t.Errorf("cost %+v", c)
	}
}
