package config

import (
	"os"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/keystore"
)

// TestMain keeps every test away from the workstation's real keystore. A
// test that needs one sets AgeKeystore itself.
func TestMain(m *testing.M) {
	AgeKeystore = keystore.Unavailable{}
	os.Exit(m.Run())
}
