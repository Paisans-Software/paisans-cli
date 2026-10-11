package apply_test

import (
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/apply"
)

// A transport with a RetryLog of its own sends its retries there rather than
// to the package's, so commands run at once on several hosts can each keep
// their own.
func TestATransportsOwnRetryLogTakesItsRetries(t *testing.T) {
	apply.ForgetContacts()
	t.Cleanup(apply.ForgetContacts)
	var shared, own strings.Builder
	attempts := 0
	t.Cleanup(apply.FakeSSH(func([]string, string) (string, int) {
		attempts++
		if attempts == 1 {
			return "ssh: connect to host 192.0.2.10 port 22: Connection refused\n", 255
		}
		return "ok\n", 0
	}, []time.Duration{time.Millisecond}, func(time.Duration) {}, &shared))
	tr := apply.SSHTransport{Destination: "home-a", RetryLog: &own}
	if _, err := tr.Run("true"); err != nil {
		t.Fatal(err)
	}
	if shared.Len() != 0 || !strings.Contains(own.String(), "home-a: ssh could not connect") {
		t.Errorf("shared %q, own %q", shared.String(), own.String())
	}
}

// ssh asks on the terminal itself about a host key it does not know. Two
// such questions at once would share the terminal and garble each other, so
// first connections that may ask are made one at a time, whatever host each
// is to; a host whose key is known connects at once.
func TestHostKeyQuestionsAreAskedOneAtATime(t *testing.T) {
	apply.ForgetContacts()
	t.Cleanup(apply.ForgetContacts)
	var unused int
	t.Cleanup(apply.FakeHostKeys(func(t apply.SSHTransport) bool { return strings.HasPrefix(t.Destination, "known") }, &unused))
	var asking, most, known int32
	t.Cleanup(apply.FakeSSH(func(args []string, _ string) (string, int) {
		if strings.HasPrefix(args[len(args)-2], "known") {
			atomic.AddInt32(&known, 1)
			return "ok\n", 0
		}
		n := atomic.AddInt32(&asking, 1)
		for {
			m := atomic.LoadInt32(&most)
			if n <= m || atomic.CompareAndSwapInt32(&most, m, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&asking, -1)
		return "ok\n", 0
	}, nil, func(time.Duration) {}, io.Discard))
	var wg sync.WaitGroup
	for _, d := range []string{"new-a", "new-b", "new-c", "known-a", "known-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := (apply.SSHTransport{Destination: d}).Run("true"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if most != 1 {
		t.Errorf("%d host key questions at once, want 1", most)
	}
	if known != 2 {
		t.Errorf("%d known hosts connected, want 2", known)
	}
}
