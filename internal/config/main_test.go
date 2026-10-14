package config

import (
	"os"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/keystore"
)

// TestMain keeps every test away from the workstation's real keystore, and
// from any SOPS_AGE_* variable the shell running the tests has set, which
// would otherwise be the key sops uses. A test that needs either sets it
// itself.
func TestMain(m *testing.M) {
	AgeKeystore = keystore.Unavailable{}
	for _, name := range AgeVariables() {
		os.Unsetenv(name)
	}
	os.Exit(m.Run())
}
