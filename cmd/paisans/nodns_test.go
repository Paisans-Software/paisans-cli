package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/grant"
	"github.com/paisans-software/paisans-cli/internal/keystore"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
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
	applyStore = func(ui.Reporter, string, string, *configstore.Files, *config.Config, bool, io.Reader, io.Writer) (storeVersion, error) {
		return noStore{}, nil
	}
	// No test reaches the workstation's keystore: init makes and reads age
	// keys in memory. A test about the keystore sets its own. Nor does a
	// SOPS_AGE_* variable from the shell running the tests decide which key
	// sops uses; a test that wants one sets it.
	config.AgeKeystore = keystore.NewMemory()
	// No test waits between two web server checks.
	fakeWebServerClock()
	// No test spends most of a second on each OAuth2 key it fills: what
	// they test is that a key is filled, opens with its passphrase and
	// stays put, and secretsgen's tests hold the key to 4096 bits.
	secretsgen.SetOAuthKeyBits(1024)
	// No test reaches a site or a mail server to try mail settings: every
	// check is answered by an in-process server that takes any login. A
	// test about the check sets its own (fakeSMTP).
	acceptAll := smtptest.New()
	smtpEnv = func(*config.Config, map[string]string) smtpcheck.Env { return acceptAll.Env() }
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
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// noStore is a configuration store that checks nothing and publishes
// nothing.
type noStore struct{}

func (noStore) check(ui.Reporter, map[string]grant.Record, bool, io.Reader, io.Writer) error {
	return nil
}

func (noStore) publish(ui.Reporter, bool) (*appliedVersion, error) { return nil, nil }

func (noStore) held() (string, int) { return "", 0 }

// testCaddyDigest is the build every test's registry says Caddy's tag names.
const testCaddyDigest = "sha256:b401d1cb18074026a535a1facc5f6d9d35bc62fdff480f3b1fd91042c8e48186"

// fixedDigest is a registry that names one build for every tag.
type fixedDigest string

func (f fixedDigest) Digest(string) (string, error) { return string(f), nil }
