package hostadmit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeWatchdog stands in for /dev/watchdog and its sysfs directory, and
// stops the timer when the magic close is written, as the kernel does
// unless nowayout is set.
type fakeWatchdog struct {
	dev, sys string
}

func newFakeWatchdog(t *testing.T, state string, nowayout bool) fakeWatchdog {
	t.Helper()
	dir := t.TempDir()
	w := fakeWatchdog{dev: filepath.Join(dir, "watchdog"), sys: filepath.Join(dir, "sys")}
	if err := os.MkdirAll(w.sys, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.dev, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.sys, "state"), []byte(state+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	no := "0"
	if nowayout {
		no = "1"
	}
	if err := os.WriteFile(filepath.Join(w.sys, "nowayout"), []byte(no+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	if !nowayout {
		go func() {
			for {
				select {
				case <-done:
					return
				case <-time.After(20 * time.Millisecond):
				}
				if b, _ := os.ReadFile(w.dev); strings.Contains(string(b), "V") {
					os.WriteFile(filepath.Join(w.sys, "state"), []byte("inactive\n"), 0o644)
				}
			}
		}()
	}
	return w
}

func (w fakeWatchdog) run(stop string) (string, error) {
	cmd := disarmAround(stop)
	cmd = strings.ReplaceAll(cmd, "/sys/class/watchdog/watchdog0", w.sys)
	cmd = strings.ReplaceAll(cmd, "/dev/watchdog", w.dev)
	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	return string(out), err
}

func (w fakeWatchdog) written() string {
	b, _ := os.ReadFile(w.dev)
	return string(b)
}

// A timer left running, as by a Patroni killed while it led, is disarmed,
// even while the stop beside it still runs.
func TestDisarmAroundStopsARunningTimer(t *testing.T) {
	w := newFakeWatchdog(t, "active", false)
	start := time.Now()
	if out, err := w.run("sleep 2"); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !strings.Contains(w.written(), "V") {
		t.Errorf("the magic close was not written")
	}
	if b, _ := os.ReadFile(filepath.Join(w.sys, "state")); strings.TrimSpace(string(b)) != "inactive" {
		t.Errorf("the timer still runs")
	}
	if time.Since(start) > 4*time.Second {
		t.Errorf("took %s", time.Since(start))
	}
}

// A stopped timer is never opened, since opening one starts it.
func TestDisarmAroundLeavesAStoppedTimerAlone(t *testing.T) {
	w := newFakeWatchdog(t, "inactive", false)
	if out, err := w.run("true"); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if w.written() != "" {
		t.Errorf("the device was written: %q", w.written())
	}
}

// A host without the sysfs state is left alone.
func TestDisarmAroundWithoutTheDevice(t *testing.T) {
	w := fakeWatchdog{dev: filepath.Join(t.TempDir(), "absent"), sys: filepath.Join(t.TempDir(), "absent")}
	if out, err := w.run("true"); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

// A stop that fails fails the command with its status.
func TestDisarmAroundKeepsTheStopsFailure(t *testing.T) {
	w := newFakeWatchdog(t, "inactive", false)
	out, err := w.run("false")
	if err == nil {
		t.Fatalf("a failed stop passed: %s", out)
	}
}

// A driver with nowayout set cannot be stopped, and the command says so.
func TestDisarmAroundNamesNowayout(t *testing.T) {
	if testing.Short() {
		t.Skip("waits the 10 s the command allows")
	}
	w := newFakeWatchdog(t, "active", true)
	out, err := w.run("true")
	if err == nil || !strings.Contains(out, "nowayout") {
		t.Errorf("%v: %s", err, out)
	}
}
