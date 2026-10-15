package apply_test

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
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
