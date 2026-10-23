package hostadmit

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/progname"
)

// The host's owner runs admit and evict on the host, as paisans; the request
// is the operator's, as the command was invoked.
func TestNoBuildAdviceNamesPaisansForTheOwner(t *testing.T) {
	progname.Set("/opt/bin/paisans-test")
	t.Cleanup(func() { progname.Set(progname.Default) })
	got := noBuildAdvice()
	for _, want := range []string{"`paisans-test site request`", "then `paisans site admit`", "with `paisans site evict`"} {
		if !strings.Contains(got, want) {
			t.Errorf("noBuildAdvice() = %q, want %q in it", got, want)
		}
	}
}
