package main

import (
	"reflect"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// A site's section becomes the transport's user, host, port and keys; --ssh
// replaces all four rather than one of them. Either way the transport keeps
// the site's name, for the sudo prompt.
func TestSiteTransportUsesTheSectionOrTheOverride(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	site := cfg.Sites["home-a"]
	got := siteTransport("home-a", site, "", true)
	if got.Auth == nil || got.Auth != siteTransport("home-a", site, "", true).Auth {
		t.Fatalf("want one shared SudoAuth per site, got %p", got.Auth)
	}
	got.Auth = nil
	keys, _ := site.SSH.AuthorizedKeys()
	var lines []string
	for _, k := range keys {
		lines = append(lines, k.Line)
	}
	want := apply.SSHTransport{Site: "home-a", User: "ubuntu", Host: "home-a.local", Port: 22, PublicKeys: lines, Sudo: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("section transport:\n got %+v\nwant %+v", got, want)
	}
	if got := siteTransport("home-a", site, "admin@jump-alias", false); !reflect.DeepEqual(got, apply.SSHTransport{Site: "home-a", Destination: "admin@jump-alias"}) {
		t.Fatalf("override transport: %+v", got)
	}
}

// A site that leaves ssh.user out is reached as the deployment's own user.
func TestSiteTransportDefaultsToTheDeploymentsUser(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	site := cfg.Sites["home-a"]
	site.SSH.User = ""
	if got := siteTransport("home-a", site, "", false); got.User != "paisans-f2a9" {
		t.Errorf("user is %q, want paisans-f2a9", got.User)
	}
}
