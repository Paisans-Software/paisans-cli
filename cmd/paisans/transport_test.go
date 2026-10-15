package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// resetLogins starts the test with no login known, so a Login from another
// test is not shared into this one, and with no command run with --execute
// yet, so no login is written until one is.
func resetLogins(t *testing.T) {
	t.Helper()
	loginsMu.Lock()
	saved, savedRecord := logins, recordLogins
	logins = map[string]*apply.Login{}
	recordLogins = false
	loginsMu.Unlock()
	t.Cleanup(func() {
		loginsMu.Lock()
		logins, recordLogins = saved, savedRecord
		loginsMu.Unlock()
	})
}

// A site's section becomes the transport's user, host, port and keys; --ssh
// replaces all four rather than one of them. Either way the transport keeps
// the site's name, for messages, and a Login shared by every transport to
// the same destination, so the user's UID is asked once a run.
func TestSiteTransportUsesTheSectionOrTheOverride(t *testing.T) {
	resetLogins(t)
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	site := cfg.Sites["home-a"]
	got := siteTransport("home-a", site, "")
	if got.Login == nil || got.Login != siteTransport("home-a", site, "").Login {
		t.Fatalf("want one shared Login per site, got %p", got.Login)
	}
	got.Login = nil
	keys, _ := site.SSH.AuthorizedKeys()
	var lines []string
	for _, k := range keys {
		lines = append(lines, k.Line)
	}
	want := apply.SSHTransport{Site: "home-a", User: "ubuntu", Host: "home-a.local", Port: 22, PublicKeys: lines}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("section transport:\n got %+v\nwant %+v", got, want)
	}
	over := siteTransport("home-a", site, "admin@jump-alias")
	if over.Login == nil {
		t.Fatal("the override has no Login")
	}
	over.Login = nil
	if !reflect.DeepEqual(over, apply.SSHTransport{Site: "home-a", Destination: "admin@jump-alias"}) {
		t.Fatalf("override transport: %+v", over)
	}
}

// A site that leaves ssh.user out is reached as the deployment's own user,
// and its transport knows that user is the default, so a host that refuses
// it is reported as one that has not admitted the deployment. A declared
// user, and a destination given with --ssh, are the operator's choice.
func TestSiteTransportDefaultsToTheDeploymentsUser(t *testing.T) {
	resetLogins(t)
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	site := cfg.Sites["home-a"]
	if siteTransport("home-a", site, "").DefaultUser {
		t.Error("a declared user is taken for the default")
	}
	site.SSH.User = ""
	got := siteTransport("home-a", site, "")
	if got.User != "paisans-f2a9" || !got.DefaultUser {
		t.Errorf("user %q, default %v; want paisans-f2a9, the default", got.User, got.DefaultUser)
	}
	if siteTransport("home-a", site, "paisans-f2a9@home-a.local").DefaultUser {
		t.Error("an --ssh destination is taken for the default")
	}
	if siteTransport("", config.Site{}, "").DefaultUser {
		t.Error("a site with no deployment behind it is taken for the default")
	}
}

// rawSSH puts an ssh on PATH that logs each remote command it is given,
// decoded and unchanged, one per record ending in a line "--", to the file
// it returns. Only the login probe is answered before it.
func rawSSH(t *testing.T) string {
	t.Helper()
	log := filepath.Join(t.TempDir(), "ssh.log")
	sshAnswering(t, `#!/bin/sh
for last; do :; done
case $last in *"printf %s "*) ;; *) exit 0;; esac
b=${last#*printf %s }
b=${b%% |*}
{ printf %s "$b" | base64 -d; printf '\n--\n'; } >> '`+log+`'
exit 0
`)
	return log
}

// A deployment command reaches the site with DOCKER_HOST at the login
// user's rootless daemon, the UID its login probe was answered with, ahead
// of the command itself.
func TestASiteCommandRunsAgainstTheUsersDaemon(t *testing.T) {
	log := rawSSH(t)
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, override := range []string{"", "ubuntu@home-a.local"} {
		if _, err := siteTransport("home-a", cfg.Sites["home-a"], override).Run("docker ps"); err != nil {
			t.Fatalf("override %q: %v", override, err)
		}
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	sent := strings.Split(strings.TrimSuffix(string(data), "\n--\n"), "\n--\n")
	if len(sent) != 2 {
		t.Fatalf("want two commands, got %q", sent)
	}
	for _, c := range sent {
		if !strings.HasPrefix(c, "export DOCKER_HOST='unix:///run/user/1000/docker.sock'\n") || !strings.HasSuffix(c, "\ndocker ps") {
			t.Errorf("a site command was sent as %q", c)
		}
	}
}

// The owner's transport, which host deployments reads the host through,
// points docker at no user's daemon: it sends the command as given.
func TestTheOwnersCommandSendsNoDockerHost(t *testing.T) {
	log := rawSSH(t)
	dest, err := config.ParseDestination("ubuntu@192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ownerTransport(dest, false).Run("docker ps"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "docker ps\n--\n" {
		t.Errorf("the owner's command was sent as %q", got)
	}
}
