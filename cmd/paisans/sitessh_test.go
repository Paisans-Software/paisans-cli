package main

import (
	"errors"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// sessionFor runs site ssh with args against a stub session and returns the
// transport and command it was given.
func sessionFor(t *testing.T, args []string, status error) (apply.SSHTransport, []string, error) {
	t.Helper()
	saved := shellSite
	t.Cleanup(func() { shellSite = saved })
	var got apply.SSHTransport
	var command []string
	called := false
	shellSite = func(tr apply.SSHTransport, c []string) error {
		got, command, called = tr, c, true
		return status
	}
	err := runSiteSSH(args)
	if err == nil && !called {
		t.Fatal("site ssh returned without opening a session")
	}
	return got, command, err
}

// site ssh logs in as the site's ssh.user, at its host and port, offering
// its keys, as every command does.
func TestSiteSSHLogsInAsTheSitesUser(t *testing.T) {
	tr, command, err := sessionFor(t, []string{"home-a", "--config", fixtureConfig()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tr.User != "ubuntu" || tr.Host != "home-a.local" || tr.Port != 22 || tr.DefaultUser {
		t.Errorf("transport %+v, want ubuntu@home-a.local:22 as the declared user", tr)
	}
	if len(tr.PublicKeys) != 2 {
		t.Errorf("offers %d keys, want alice's and bob's", len(tr.PublicKeys))
	}
	if len(command) != 0 {
		t.Errorf("command %q, want a shell", command)
	}
}

// A site that names no ssh user is reached as the deployment's own user, not
// the operator's local login a plain ssh would use.
func TestSiteSSHLogsInAsTheDeploymentsUser(t *testing.T) {
	path := fixtureWithoutUsers(t)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := cfg.Deployment().Prefix()
	tr, _, err := sessionFor(t, []string{"--config", path, "home-a"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want == "" || tr.User != want || !tr.DefaultUser {
		t.Errorf("user %q (default %v), want the deployment's %q", tr.User, tr.DefaultUser, want)
	}
	args := strings.Join(tr.ShellArgs([]string{"k1"}, nil), " ")
	if !strings.Contains(args, "PreferredAuthentications=publickey") || !strings.HasSuffix(args, "-- "+want+"@home-a.local") {
		t.Errorf("ssh args %q", args)
	}
}

// Words after -- are the command, flags and all, and are never read as site
// ssh's own; a remote status that is not zero is the command's exit status.
func TestSiteSSHRunsTheCommandAfterTheSeparator(t *testing.T) {
	ssh := exitStatus(t, 3)
	_, command, err := sessionFor(t, []string{"home-a", "--config", fixtureConfig(), "--", "docker", "ps", "--all", "--execute"}, ssh)
	if want := []string{"docker", "ps", "--all", "--execute"}; !slices.Equal(command, want) {
		t.Errorf("command %q, want %q", command, want)
	}
	var exit remoteExit
	if !errors.As(err, &exit) || int(exit) != 3 {
		t.Fatalf("err %v, want the remote status 3", err)
	}
	if code := reportError(&strings.Builder{}, err, false); code != 3 {
		t.Errorf("exits %d, want 3", code)
	}
}

// A missing or unknown site, or a word outside --, is refused before any
// session opens.
func TestSiteSSHRefusesWithoutOneDeclaredSite(t *testing.T) {
	saved := shellSite
	t.Cleanup(func() { shellSite = saved })
	shellSite = func(apply.SSHTransport, []string) error {
		t.Fatal("a session opened")
		return nil
	}
	for _, args := range [][]string{
		{"--config", fixtureConfig()},
		{"nowhere", "--config", fixtureConfig()},
		{"home-a", "uptime", "--config", fixtureConfig()},
	} {
		if err := runSiteSSH(args); err == nil {
			t.Errorf("%q: no error", args)
		}
	}
}

// exitStatus is the error of a real process that exited with code.
func exitStatus(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("sh exit %d: %v", code, err)
	}
	return err
}
