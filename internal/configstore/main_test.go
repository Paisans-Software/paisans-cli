package configstore

import (
	"os"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// TestMain runs every test in the explicit test mode: the files these
// tests publish hold plaintext secrets, so no age key is needed. A test
// about the refusal of plaintext unsets it.
func TestMain(m *testing.M) {
	os.Setenv(config.PlaintextSecretsVariable, "1")
	os.Exit(m.Run())
}
