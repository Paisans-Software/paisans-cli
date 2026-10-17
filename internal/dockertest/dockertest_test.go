package dockertest_test

import (
	"testing"

	"github.com/paisans-software/paisans-cli/internal/dockertest"
)

// docker info lists rootless among the daemon's security options when it is
// one (Docker's rootless documentation).
func TestRootless(t *testing.T) {
	for in, want := range map[string]bool{
		`["name=seccomp,profile=builtin","name=rootless","name=cgroupns"]`: true,
		`["name=apparmor","name=seccomp,profile=builtin","name=cgroupns"]`: false,
		``: false,
	} {
		if got := dockertest.Rootless(in); got != want {
			t.Errorf("%s: %v", in, got)
		}
	}
}
