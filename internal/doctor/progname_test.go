package doctor

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/progname"
)

// The swap advice is the host's owner's, run on the host, so it names
// paisans whatever doctor was invoked as.
func TestSwapAdviceNamesPaisansForTheOwner(t *testing.T) {
	progname.Set("/opt/bin/paisans-test")
	t.Cleanup(func() { progname.Set(progname.Default) })
	got := Swap([]MemoryProbe{{Site: "vm", Out: memOut(mib458, "0", false)}})
	if len(got) != 1 || !strings.Contains(strings.Join(got[0].More, " "), "runs `paisans site admit` again") {
		t.Errorf("swap advice: %+v, want the owner's paisans site admit", got)
	}
}
