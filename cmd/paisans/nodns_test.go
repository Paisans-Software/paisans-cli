package main

import (
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/dns"
	"github.com/paisans-software/paisans-stack/internal/grant"
	"github.com/paisans-software/paisans-stack/internal/keystore"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// TestMain stands a refusal in for the DNS provider site remove builds, so
// that no test in this package can reach a real one, whatever provider its
// fixture names. A test that needs one replaces it with a fake. The same goes
// for the registry apply resolves Caddy's tag against, and for the keystore
// init keeps age keys in: every test sees the tag name one fixed build,
// testCaddyDigest, and keeps its age keys in memory.
func TestMain(m *testing.M) {
	imageDigests = fixedDigest(testCaddyDigest)
	dnsProviderFor = func(name, _ string) (dns.Provider, error) {
		return nil, fmt.Errorf("test: no DNS provider may be built in tests, asked for %s", name)
	}
	// No test reaches a host to ask whether it was the gateway; a test
	// about the move says otherwise.
	convergeHoldsGateway = func(*config.Config, string, bool) (bool, error) { return false, nil }
	// No test's apply reaches a configuration store; a test about the
	// store sets gatherForApply back.
	applyStore = func(ui.Reporter, string, string, *config.Config, bool, io.Reader, io.Writer) (storeVersion, error) {
		return noStore{}, nil
	}
	// No test reaches the workstation's keystore: init makes and reads age
	// keys in memory. A test about the keystore sets its own. Nor does a
	// SOPS_AGE_* variable from the shell running the tests decide which key
	// sops uses; a test that wants one sets it.
	config.AgeKeystore = keystore.NewMemory()
	// No test waits between two web server checks.
	fakeWebServerClock()
	for _, name := range config.AgeVariables() {
		os.Unsetenv(name)
	}
	os.Exit(m.Run())
}

// noStore is a configuration store that checks nothing and publishes
// nothing.
type noStore struct{}

func (noStore) land(ui.Reporter, map[string]grant.Record, bool, io.Reader, io.Writer) (*appliedVersion, error) {
	return nil, nil
}

// testCaddyDigest is the build every test's registry says Caddy's tag names.
const testCaddyDigest = "sha256:b401d1cb18074026a535a1facc5f6d9d35bc62fdff480f3b1fd91042c8e48186"

// fixedDigest is a registry that names one build for every tag.
type fixedDigest string

func (f fixedDigest) Digest(string) (string, error) { return string(f), nil }
