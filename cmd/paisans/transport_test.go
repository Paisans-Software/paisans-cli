package main

import (
	"reflect"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// resetLogins starts the test with no login known, so a Login from another
// test is not shared into this one.
func resetLogins(t *testing.T) {
	t.Helper()
	loginsMu.Lock()
	saved := logins
	logins = map[string]*apply.Login{}
	loginsMu.Unlock()
	t.Cleanup(func() {
		loginsMu.Lock()
		logins = saved
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
