package apply_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

// Recording a removal twice keeps one line, creates the state directory,
// and the list reads back.
func TestRecordEtcdRemovedUnderAShell(t *testing.T) {
	base := t.TempDir()
	run := func(r apply.EtcdRemoved) {
		t.Helper()
		command := strings.ReplaceAll(apply.RecordEtcdRemovedCommand(rmDeployment, r), "'/srv/paisans/", "'"+base+"/srv/paisans/")
		if out, err := exec.Command("sh", "-c", command).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	a := apply.EtcdRemoved{Site: "vm", Cluster: 0xc1a5, Member: 0x3333}
	b := apply.EtcdRemoved{Site: "home-b", Cluster: 0xc1a5, Member: 0x2222}
	run(a)
	run(a)
	run(b)
	content, err := os.ReadFile(filepath.Join(base, strings.TrimPrefix(apply.EtcdRemovedPath(rmDeployment), "/")))
	if err != nil {
		t.Fatal(err)
	}
	got, err := apply.ParseEtcdRemoved(string(content))
	if err != nil || len(got) != 2 || got[0] != a || got[1] != b {
		t.Errorf("read %+v %v from:\n%s", got, err, content)
	}
	for _, bad := range []string{"vm c1a5\n", "vm zz 1\n", "vm 0 1\n", "vm c1a5 3333 extra\n"} {
		if _, err := apply.ParseEtcdRemoved(bad); err == nil {
			t.Errorf("%q was read", bad)
		}
	}
}
