package hostadmit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// disarmWatchdog writes the magic close to the device, and skips a host
// without one.
func TestDisarmWatchdogCommand(t *testing.T) {
	dir := t.TempDir()
	dev := filepath.Join(dir, "watchdog")
	if err := os.WriteFile(dev, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(path string) ([]byte, error) {
		return exec.Command("sh", "-c", strings.ReplaceAll(disarmWatchdog, "/dev/watchdog", path)).CombinedOutput()
	}
	if out, err := run(dev); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if b, _ := os.ReadFile(dev); string(b) != "V" {
		t.Errorf("the device holds %q, not the magic close", b)
	}
	if out, err := run(filepath.Join(dir, "absent")); err != nil {
		t.Errorf("a host without the device: %v: %s", err, out)
	}
}
