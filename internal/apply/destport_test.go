package apply_test

import (
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

// A destination with a port goes to ssh as -p and user@host after --, the
// host unbracketed, IPv6 too; messages name it as user@host, with the port
// when it is not 22, never as a URL.
func TestADestinationPortGoesAsP(t *testing.T) {
	t.Cleanup(apply.SetMux(nil, new([][]string)))
	tr := apply.SSHTransport{Destination: "admin@2001:db8::1", DestinationPort: 2222, User: "admin", Host: "2001:db8::1"}
	if got, want := tr.SSHArgs(nil, "true"), []string{"-T", "-p", "2222", "--", "admin@2001:db8::1", apply.RemoteCommand("", "true")}; !reflect.DeepEqual(got, want) {
		t.Errorf("args %q, want %q", got, want)
	}
	if got := tr.Describe(); got != "admin@2001:db8::1 port 2222" {
		t.Errorf("Describe = %q", got)
	}
	if got := apply.ConnectCommand(tr); got != "ssh -p 2222 admin@2001:db8::1" {
		t.Errorf("connect command = %q", got)
	}
	if got := apply.KnownHostsName(tr); got != "[2001:db8::1]:2222" {
		t.Errorf("known_hosts name = %q", got)
	}
	at22 := apply.SSHTransport{Destination: "admin@home-a", DestinationPort: 22, User: "admin", Host: "home-a"}
	if at22.Describe() != "admin@home-a" || apply.ConnectCommand(at22) != "ssh admin@home-a" {
		t.Errorf("port 22: %q, %q", at22.Describe(), apply.ConnectCommand(at22))
	}
}

// A refused login names the transport's user and host as they are held,
// not as a split of the destination.
func TestADestinationPortsAuthProblemNamesItsHost(t *testing.T) {
	tr := apply.SSHTransport{Site: "home-a", Destination: "paisans-f2a9@2001:db8::1", DestinationPort: 2222, User: "paisans-f2a9", Host: "2001:db8::1", DefaultUser: true}
	err := apply.AuthProblem(tr, exitError(t), "paisans-f2a9@2001:db8::1: Permission denied (publickey).\n")
	if err == nil || !strings.Contains(err.Error(), "2001:db8::1") || strings.Contains(err.Error(), "2222@") {
		t.Errorf("auth problem: %v", err)
	}
}

func exitError(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 255").Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("no exit error: %v", err)
	}
	return err
}
