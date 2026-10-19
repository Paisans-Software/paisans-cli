package apply_test

import (
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// loginScript stands in for ssh: it answers id -u with uid and every other
// command with nothing, and keeps each command as the remote sh runs it.
func loginScript(t *testing.T, uid string, exit int) *[]string {
	t.Helper()
	apply.ForgetContacts()
	t.Cleanup(apply.ForgetContacts)
	blanks := 0
	t.Cleanup(apply.FakeHostKeys(func(apply.SSHTransport) bool { return true }, &blanks))
	var ran []string
	t.Cleanup(apply.FakeSSH(func(args []string, _ string) (string, int) {
		command := args[len(args)-1]
		ran = append(ran, command)
		if command == "id -u" {
			return uid, exit
		}
		return "", 0
	}, nil, func(time.Duration) {}, io.Discard))
	return &ran
}

const rootlessSocket = "unix:///run/user/1000/docker.sock"

// The login user's UID is asked once, on the first command, and every
// command runs with DOCKER_HOST at that user's rootless socket, so docker
// never reaches the host's system daemon. A copy of the transport shares the
// answer, as a second transport built for the same site does.
func TestALoginAsksItsUIDOnceAndPointsDockerAtIt(t *testing.T) {
	ran := loginScript(t, "1000\n", 0)
	connected := 0
	tr := apply.SSHTransport{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, Login: apply.NewLogin(func() { connected++ })}
	if _, err := tr.Run("docker ps"); err != nil {
		t.Fatal(err)
	}
	copied := tr
	if _, err := copied.Run("docker version"); err != nil {
		t.Fatal(err)
	}
	export := "export DOCKER_HOST='" + rootlessSocket + "'\n"
	want := []string{"id -u", export + "docker ps", export + "docker version"}
	if strings.Join(*ran, "|") != strings.Join(want, "|") {
		t.Errorf("ran %q, want %q", *ran, want)
	}
	if connected != 1 {
		t.Errorf("Connected called %d times, want once", connected)
	}
}

// What id -u printed is a UID or nothing is built from it: a login that
// prints a banner first, or nothing, fails naming the probe, and runs no
// command without its DOCKER_HOST. The failure is not kept, so the next
// command asks again.
func TestALoginWhoseUIDCannotBeReadRunsNothing(t *testing.T) {
	for _, out := range []string{"Welcome to home-a\n1000\n", "\n"} {
		ran := loginScript(t, out, 0)
		connected := false
		tr := apply.SSHTransport{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, Login: apply.NewLogin(func() { connected = true })}
		_, err := tr.Run("docker ps")
		if err == nil || !strings.Contains(err.Error(), "id -u printed") {
			t.Errorf("%q: err = %v", out, err)
		}
		if _, err := tr.Run("docker ps"); err == nil {
			t.Errorf("%q: a second command ran", out)
		}
		if strings.Join(*ran, "|") != "id -u|id -u" || connected {
			t.Errorf("%q: ran %q, connected %v", out, *ran, connected)
		}
	}
}

// A login ssh never made is ErrUnreachable to the caller, as any command's
// is, and counts as no login.
func TestALoginThatNeverConnectedIsUnreachable(t *testing.T) {
	loginScript(t, "ssh: connect to host 203.0.113.10 port 22: Connection refused\n", 255)
	connected := false
	tr := apply.SSHTransport{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, Login: apply.NewLogin(func() { connected = true })}
	if _, err := tr.Run("docker ps"); !errors.Is(err, apply.ErrUnreachable) || connected {
		t.Errorf("err = %v, connected %v", err, connected)
	}
}

// A DockerHost given outright is used as given, with no id -u.
func TestAGivenDockerHostAsksNoUID(t *testing.T) {
	ran := loginScript(t, "1000\n", 0)
	tr := apply.SSHTransport{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, DockerHost: "unix:///run/user/1001/docker.sock", Login: apply.NewLogin(nil)}
	if _, err := tr.Run("docker ps"); err != nil {
		t.Fatal(err)
	}
	if want := "export DOCKER_HOST='unix:///run/user/1001/docker.sock'\ndocker ps"; strings.Join(*ran, "|") != want {
		t.Errorf("ran %q, want %q", *ran, want)
	}
}

func TestRootlessDockerHost(t *testing.T) {
	if got := apply.RootlessDockerHost("1000"); got != rootlessSocket {
		t.Errorf("RootlessDockerHost(1000) = %q", got)
	}
}

// A probe that fails names the destination once, not once for the probe and
// again for the command that asked.
func TestALoginProbeFailureNamesTheDestinationOnce(t *testing.T) {
	loginScript(t, "Welcome to home-a\n1000\n", 0)
	tr := apply.SSHTransport{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, Login: apply.NewLogin(nil)}
	_, err := tr.Run("docker ps")
	if err == nil || strings.Count(err.Error(), "203.0.113.10") != 1 {
		t.Errorf("err = %v", err)
	}
}

// The deployment's user is never root: UID 0 is refused, naming the probe.
func TestALoginAsRootIsRefused(t *testing.T) {
	ran := loginScript(t, "0\n", 0)
	tr := apply.SSHTransport{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, Login: apply.NewLogin(nil)}
	_, err := tr.Run("docker ps")
	if err == nil || !strings.Contains(err.Error(), "id -u printed") || !strings.Contains(err.Error(), "root") {
		t.Errorf("err = %v", err)
	}
	if strings.Join(*ran, "|") != "id -u" {
		t.Errorf("ran %q", *ran)
	}
}

// sudo resets the environment, so DOCKER_HOST would not reach docker: the
// combination is refused before anything runs.
func TestSudoWithADockerHostIsRefused(t *testing.T) {
	ran := loginScript(t, "1000\n", 0)
	for _, tr := range []apply.SSHTransport{
		{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, Sudo: true, DockerHost: rootlessSocket},
		{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, Sudo: true, Login: apply.NewLogin(nil)},
	} {
		if _, err := tr.Run("docker ps"); err == nil || !strings.Contains(err.Error(), "sudo") || strings.Count(err.Error(), "paisans-f2a9@203.0.113.10") != 1 {
			t.Errorf("err = %v", err)
		}
	}
	if len(*ran) != 0 {
		t.Errorf("ran %q", *ran)
	}
}

// Many first commands at once, on copies of one transport, ask the UID once
// and report the connection once.
func TestALoginUsedConcurrentlyAsksOnceAndConnectsOnce(t *testing.T) {
	apply.ForgetContacts()
	t.Cleanup(apply.ForgetContacts)
	blanks := 0
	t.Cleanup(apply.FakeHostKeys(func(apply.SSHTransport) bool { return true }, &blanks))
	var mu sync.Mutex
	probes := 0
	t.Cleanup(apply.FakeSSH(func(args []string, _ string) (string, int) {
		if args[len(args)-1] == "id -u" {
			mu.Lock()
			probes++
			mu.Unlock()
			return "1000\n", 0
		}
		return "", 0
	}, nil, func(time.Duration) {}, io.Discard))
	var connected atomic.Int32
	tr := apply.SSHTransport{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, Login: apply.NewLogin(func() { connected.Add(1) })}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := tr
			if _, err := c.Run("docker ps"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if probes != 1 || connected.Load() != 1 {
		t.Errorf("probes %d, connected %d", probes, connected.Load())
	}
}

// A probe that fails to run at all (ssh cannot connect, or the command exits
// non-zero) names the destination once in the error Run returns.
func TestALoginProbeThatFailsNamesTheDestinationOnce(t *testing.T) {
	loginScript(t, "id: command not found\n", 127)
	tr := apply.SSHTransport{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, Login: apply.NewLogin(nil)}
	_, err := tr.Run("docker ps")
	if err == nil || !strings.Contains(err.Error(), "id -u") || strings.Count(err.Error(), "203.0.113.10") != 1 {
		t.Errorf("err = %v", err)
	}
}

// Dial reaches run without a wrapper that names the destination, so the
// refusal names it itself.
func TestDialRefusesSudoWithADockerHostNamingTheDestination(t *testing.T) {
	loginScript(t, "1000\n", 0)
	tr := apply.SSHTransport{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, Sudo: true, DockerHost: rootlessSocket}
	_, err := tr.Dial("127.0.0.1:80")
	if err == nil || !strings.Contains(err.Error(), "paisans-f2a9@203.0.113.10") {
		t.Errorf("err = %v", err)
	}
}

// downHost stands in for ssh to a host that is switched off: every attempt
// times out. It counts the attempts, and lets a retry wait nothing.
func downHost(t *testing.T) *int {
	t.Helper()
	apply.ForgetContacts()
	t.Cleanup(apply.ForgetContacts)
	blanks := 0
	t.Cleanup(apply.FakeHostKeys(func(apply.SSHTransport) bool { return true }, &blanks))
	tries := 0
	t.Cleanup(apply.FakeSSH(func([]string, string) (string, int) {
		tries++
		return "ssh: connect to host 203.0.113.10 port 22: Operation timed out\n", 255
	}, []time.Duration{time.Millisecond, time.Millisecond}, func(time.Duration) {}, io.Discard))
	return &tries
}

// Reach tries a host once, where a command retries a connection that never
// opened: each retry would wait out ssh's connect timeout again.
func TestReachTriesAHostOnce(t *testing.T) {
	tries := downHost(t)
	tr := apply.SSHTransport{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, Login: apply.NewLogin(nil)}
	err := apply.Reach(tr)
	if !errors.Is(err, apply.ErrUnreachable) || *tries != 1 {
		t.Errorf("err = %v after %d tries, want ErrUnreachable after 1", err, *tries)
	}
	if _, explain := ui.Describe(err); !strings.Contains(explain, "in 1 attempt:") {
		t.Errorf("explained %q, want it to say 1 attempt", explain)
	}
}

// A destination whose first login never connected is down for the run:
// every later command through its Login fails as unreachable at once,
// without trying ssh again, whether or not a copy of the transport retries.
func TestALoginThatNeverConnectedTriesNoMore(t *testing.T) {
	tries := downHost(t)
	login := apply.NewLogin(nil)
	tr := apply.SSHTransport{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, Login: login}
	first := apply.Reach(tr)
	if !errors.Is(first, apply.ErrUnreachable) {
		t.Fatalf("Reach: %v", first)
	}
	again := apply.SSHTransport{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, Login: login}
	if _, err := again.Run("docker ps"); !errors.Is(err, apply.ErrUnreachable) {
		t.Errorf("err = %v, want ErrUnreachable", err)
	}
	if _, _, err := again.ReadFile("/etc/hostname"); !errors.Is(err, apply.ErrUnreachable) {
		t.Errorf("ReadFile: err = %v, want ErrUnreachable", err)
	}
	if *tries != 1 {
		t.Errorf("ssh tried %d times, want once", *tries)
	}
	_, err := again.Run("docker ps")
	if _, explain := ui.Describe(err); !strings.HasPrefix(explain, "It did not answer the run's first login, so this run does not try it again.") {
		t.Errorf("explained %q, want it to say the command was not tried", explain)
	}
}

// A login that refused is not kept: a refusal is the host's answer to this
// key, and a later command may offer one it accepts. Its first contact is
// tried in batch mode and not again where ssh may ask: declared keys have no
// passphrase to ask for.
func TestALoginThatWasRefusedIsAskedAgain(t *testing.T) {
	loginScript(t, "paisans-f2a9@203.0.113.10: Permission denied (publickey).\n", 255)
	var blanks int
	t.Cleanup(apply.FakeHostKeys(func(apply.SSHTransport) bool { return true }, &blanks))
	t.Cleanup(apply.FakeQuietLogin(func(apply.SSHTransport) bool { return true }))
	ran := 0
	t.Cleanup(apply.FakeSSH(func([]string, string) (string, int) {
		ran++
		return "paisans-f2a9@203.0.113.10: Permission denied (publickey).\n", 255
	}, nil, func(time.Duration) {}, io.Discard))
	tr := apply.SSHTransport{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, Login: apply.NewLogin(nil)}
	for range 2 {
		if _, err := tr.Run("docker ps"); !errors.Is(err, apply.ErrLoginRefused) {
			t.Fatalf("err = %v, want ErrLoginRefused", err)
		}
	}
	if ran != 2 {
		t.Errorf("ssh ran %d times, want twice", ran)
	}
}

// A command's first contact that never connected is not kept: only Reach
// decides a host is down for the run, and the next command, a wait loop's
// among them, tries the host again with its own retries.
func TestACommandThatNeverConnectedLeavesTheNextToTryAgain(t *testing.T) {
	tries := downHost(t)
	tr := apply.SSHTransport{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, Login: apply.NewLogin(nil)}
	for range 2 {
		if _, err := tr.Run("docker ps"); !errors.Is(err, apply.ErrUnreachable) {
			t.Fatalf("err = %v, want ErrUnreachable", err)
		}
	}
	if *tries != 6 {
		t.Errorf("ssh tried %d times, want three for each command", *tries)
	}
}
