package version_test

import (
	"testing"

	"github.com/paisans-software/paisans-cli/internal/version"
)

// A build of develop counts as every pre-release of its own release, and
// as nothing newer.
func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		recorded, have string
		newer          bool
	}{
		{"0.3.1", "0.3.0", true},
		{"0.3.0", "0.3.0-rc.14", true},
		{"0.3.0-rc.14", "0.3.0-rc.13", true},
		{"0.3.0-rc.14", "0.3.0-dev", false},
		{"0.3.0", "0.3.0-dev", true},
		{"0.4.0-rc.1", "0.3.0-dev", true},
		{"0.3.0-dev", "0.3.0-dev", false},
		{"0.3.0-dev", "0.3.0-rc.14", false},
		{"0.2.0", "0.3.0", false},
	} {
		if got, err := version.Newer(tc.recorded, tc.have); err != nil || got != tc.newer {
			t.Errorf("Newer(%s, %s) = %v, %v, want %v", tc.recorded, tc.have, got, err, tc.newer)
		}
	}
}
