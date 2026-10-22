package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/grant"
	"github.com/paisans-software/paisans-cli/internal/keystore"
	"github.com/paisans-software/paisans-cli/internal/localstore"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/selfcheck"
	"github.com/paisans-software/paisans-cli/internal/smtpcheck"
	"github.com/paisans-software/paisans-cli/internal/smtpcheck/smtptest"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// TestMain stands a refusal in for the DNS provider site remove builds, so
// that no test in this package can reach a real one, whatever provider its
// fixture names. A test that needs one replaces it with a fake. The same goes
// for the registry apply resolves Caddy's tag against, for the mail server
// the SMTP checks log in to, and for the keystore init keeps age keys in: every test sees the tag name one fixed build,
// testCaddyDigest, and keeps its age keys in memory.
func TestMain(m *testing.M) {
	imageDigests = fixedDigest(testCaddyDigest)
	dnsProviderFor = func(name, _ string) (dns.Provider, error) {
		return nil, fmt.Errorf("test: no DNS provider may be built in tests, asked for %s", name)
	}
	// No test reaches a host to ask whether it was the gateway; a test
	// about the move says otherwise.
	convergeHoldsGateway = func(*config.Config, string) (bool, error) { return false, nil }
	// No test's apply reaches a configuration store; a test about the
	// store sets gatherForApply back.
	applyStore = func(ui.Reporter, string, string, *configstore.Files, *config.Config, bool, answerer, io.Reader, io.Writer) (storeVersion, error) {
		return noStore{}, nil
	}
	// No test reaches the workstation's keystore: init makes and reads age
	// keys in memory. A test about the keystore sets its own. Nor does a
	// SOPS_AGE_* variable from the shell running the tests decide which key
	// sops uses; a test that wants one sets it.
	config.AgeKeystore = keystore.NewMemory()
	// Most tests keep their secrets in plaintext, so they need no age key:
	// the explicit test mode. A test about the refusal of plaintext unsets
	// it (realSecrets).
	os.Setenv(config.PlaintextSecretsVariable, "1")
	// Every host has admitted the deployment as the site it is asked about,
	// with every grant its roles need.
	// The record is still read, so a test sees which hosts were checked and
	// when; a test about the check reads and judges it (realAdmission).
	admissionForSite = func(t admission.Reader, cfg *config.Config, site string) (admission.Record, error) {
		if p, err := admission.Path(deployment.Deployment{ID: cfg.ID}.Token()); err == nil {
			_, _, _ = t.ReadFile(p)
		}
		s := cfg.Sites[site]
		rec := admission.Record{Site: site, User: s.SSHUser(), Grants: admission.Needs(s), Forwarding: s.Endpoint != "",
			Ports: admission.Ports{SSH: s.SSH.PortOrDefault(), WireGuard: s.ListenPort()}, MeshSubnet: cfg.Mesh.Subnet}
		if s.WebServerManagedBy() == config.ManagedByExternal {
			rec.WebServer = &admission.WebServer{Server: "caddy"}
		}
		return rec, nil
	}
	// No test reads the workstation's ssh config: a destination resolves
	// through a stub ssh -G, which knows no alias, so every host is itself.
	// A test about aliases sets its own (withSSHAliases).
	operatorSSHConfig = func() config.SSHConfig { return stubSSHConfig(nil) }
	// Every run classifies every site: here each host lets the run in and
	// has admitted the deployment as its site, so every site is ready. A
	// test about a site missing sets its own (classifyWith).
	classifyTransports = classifyHosts(nil)
	// No test spends most of a second on each OAuth2 key it fills: what
	// they test is that a key is filled, opens with its passphrase and
	// stays put, and secretsgen's tests hold the key to 4096 bits.
	secretsgen.SetOAuthKeyBits(1024)
	// No test reaches a gateway from outside: every gateway answers as the
	// deployment it is asked about. A test about the check sets its own.
	gatewayProbe = func() selfcheck.Probe {
		return selfcheck.Probe{Fetch: func(_ context.Context, scheme, _, host, path string) (selfcheck.Answer, error) {
			if scheme == "http" {
				return selfcheck.Answer{Status: 308, Location: "https://" + host + path}, nil
			}
			return selfcheck.Answer{Status: 200, Body: path[strings.LastIndex(path, "/")+1:]}, nil
		}, Now: time.Now, Sleep: func(time.Duration) {}}
	}
	// No test reaches a site or a mail server to try mail settings: every
	// check is answered by an in-process server that takes any login. A
	// test about the check sets its own (fakeSMTP).
	acceptAll := smtptest.New()
	smtpEnv = func(*config.Config, map[string]string) smtpcheck.Env { return acceptAll.Env() }
	localSMTPEnv = func() smtpcheck.Env { return acceptAll.Env() }
	for _, name := range config.AgeVariables() {
		os.Unsetenv(name)
	}
	// No test writes the checked-in secrets fixture: apply and init may
	// fill in a generated secret the file they are given lacks, so every
	// test reads a copy. TestSecretsFixtureIsComplete checks the original.
	dir, err := os.MkdirTemp("", "paisans-cmd-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	data, err := os.ReadFile(checkedInSecrets)
	if err == nil {
		secretsCopy = filepath.Join(dir, "secrets.fixture.yaml")
		err = os.WriteFile(secretsCopy, data, 0o600)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	gitDirect(dir)
	// No test saves to the operator's own deployments: every saved copy
	// lands in a directory of the run's own.
	dataDir, err := os.MkdirTemp("", "paisans-data-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv(localstore.DirVariable, dataDir)
	code := m.Run()
	os.RemoveAll(dataDir)
	os.RemoveAll(dir)
	os.Exit(code)
}

// gitDirect puts the git that /usr/bin/git stands for first on PATH, linked
// from a directory of its own in dir, when /usr/bin/git is the git on PATH
// on macOS. That one is Xcode's stub, which asks xcrun where git is on every
// call, and that doubles what each of the thousands of git commands these
// tests run costs. The git that runs is the same one.
func gitDirect(dir string) {
	if runtime.GOOS != "darwin" {
		return
	}
	if found, err := exec.LookPath("git"); err != nil || found != "/usr/bin/git" {
		return
	}
	out, err := exec.Command("xcrun", "--find", "git").Output()
	real := strings.TrimSpace(string(out))
	if err != nil || !filepath.IsAbs(real) {
		return
	}
	bin := filepath.Join(dir, "git-bin")
	if os.Mkdir(bin, 0o700) != nil || os.Symlink(real, filepath.Join(bin, "git")) != nil {
		return
	}
	os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// noStore is a configuration store that checks nothing and publishes
// nothing.
type noStore struct{}

func (noStore) check(ui.Reporter, map[string]grant.Record, bool, io.Reader, io.Writer) error {
	return nil
}

func (noStore) publish(ui.Reporter, bool) (*appliedVersion, error) { return nil, nil }

func (noStore) next(ui.Reporter, *appliedVersion, configstore.Files) (*appliedVersion, error) {
	return nil, nil
}

func (noStore) held() (string, int) { return "", 0 }

// testCaddyDigest is the build every test's registry says Caddy's tag names.
const testCaddyDigest = "sha256:b401d1cb18074026a535a1facc5f6d9d35bc62fdff480f3b1fd91042c8e48186"

// fixedDigest is a registry that names one build for every tag.
type fixedDigest string

func (f fixedDigest) Digest(string) (string, error) { return string(f), nil }
