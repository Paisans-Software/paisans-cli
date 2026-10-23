package hostadmit

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/progname"
)

// Another deployment's operator and the host's owner run these commands, so
// each names paisans whatever this command was invoked as.
func TestNoBuildAdviceNamesPaisansForTheOwner(t *testing.T) {
	progname.Set("/opt/bin/paisans-test")
	t.Cleanup(func() { progname.Set(progname.Default) })
	got := noBuildAdvice()
	for _, want := range []string{"(`paisans site request`", "then `paisans site admit`", "with `paisans site evict`"} {
		if !strings.Contains(got, want) {
			t.Errorf("noBuildAdvice() = %q, want %q in it", got, want)
		}
	}
}
