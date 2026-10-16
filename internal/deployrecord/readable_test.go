package deployrecord_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"github.com/paisans-software/paisans-cli/internal/registry"
)

// A write runs under the deployment's own lock, in its own directory, and
// leaves the record 644 by a rename.
func TestAWriteIsUnderTheDeploymentsLock(t *testing.T) {
	h := &fakeHost{files: map[string]string{}}
	var ran []string
	rec := recording{h, &ran}
	if _, err := deployrecord.Update(map[string]registry.Runner{"vm": rec}, dep, deployrecord.Adding(sites("vm")), now); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 {
		t.Fatalf("ran %d commands", len(ran))
	}
	command := ran[0]
	for _, want := range []string{dep.StateLockCommand("the deployment record"), `tmp=$(mktemp '/srv/paisans/f2a9/.paisans/.deployed.XXXXXX')`, `chmod 644 "$tmp";`, `mv "$tmp" "$f"`} {
		if !strings.Contains(command, want) {
			t.Errorf("the write has no %q:\n%s", want, command)
		}
	}
	if strings.Contains(command, "/var/lib/paisans") {
		t.Errorf("the write reaches root's directory:\n%s", command)
	}
}

// recording is a fakeHost that keeps every command it runs.
type recording struct {
	*fakeHost
	ran *[]string
}

func (r recording) Run(command string) (string, error) {
	*r.ran = append(*r.ran, command)
	return r.fakeHost.Run(command)
}
