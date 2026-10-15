package admission_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// What each role mix needs, and the rest of what Build reads from the site.
func TestBuildReadsTheSite(t *testing.T) {
	for _, c := range []struct {
		site       string
		grants     admission.Grants
		user       string
		sshPort    int
		listenPort int
		endpoint   bool
		webServer  int
		operators  []string
	}{
		{"home-a", admission.Grants{Watchdog: true, PublicPorts: true}, "paisans-f2a9", 22, 51820, true, 0, []string{"alice", "bob"}},
		{"home-b", admission.Grants{Watchdog: true}, "community", 2222, 0, false, 0, []string{"bob"}},
		{"vm", admission.Grants{}, "paisans-f2a9", 22, 51820, false, 0, []string{"alice"}},
		{"status", admission.Grants{}, "paisans-f2a9", 22, 51820, false, 1, []string{"alice"}},
		{"watch", admission.Grants{PublicPorts: true}, "paisans-f2a9", 22, 51821, true, 0, []string{"alice"}},
		{"store", admission.Grants{}, "paisans-f2a9", 22, 0, false, 0, []string{"alice"}},
	} {
		r := build(t, c.site)
		var names []string
		for _, o := range r.Operators {
			names = append(names, o.Name)
		}
		if r.Grants != c.grants || r.User != c.user || r.SSHPort != c.sshPort || r.ListenPort != c.listenPort || r.Endpoint != c.endpoint || len(r.WebServer) != c.webServer || strings.Join(names, ",") != strings.Join(c.operators, ",") {
			t.Errorf("%s: got %+v", c.site, r)
		}
	}
	status := build(t, "status")
	if w := status.WebServer[0]; w != (admission.WebApp{App: "status", Hostname: "status.example.org", Listen: "127.0.0.1:8480"}) {
		t.Errorf("status: web_server %+v", w)
	}
	if bob := build(t, "home-b").Operators[0]; len(bob.Keys) != 2 || strings.Contains(strings.Join(bob.Keys, " "), "bob@") {
		t.Errorf("bob's keys are not both there, without their comments: %q", bob.Keys)
	}
}

// A site the configuration does not declare is refused, naming the ones it
// does.
func TestBuildRefusesAnUndeclaredSite(t *testing.T) {
	_, err := admission.Build(fixture(t), "home-z")
	if err == nil || !strings.Contains(err.Error(), `declares no site "home-z"`) || !strings.Contains(err.Error(), "home-a, home-b") {
		t.Errorf("got %v", err)
	}
}

// Build checks what it builds, so host request never prints a request admit
// would refuse: a mesh over loopback is refused here as in Parse.
func TestBuildRefusesWhatParseWould(t *testing.T) {
	cfg := fixture(t)
	cfg.Mesh.Subnet = "127.0.0.0/8"
	home := cfg.Sites["home-a"]
	home.Address = "127.0.0.1"
	cfg.Sites["home-a"] = home
	_, err := admission.Build(cfg, "home-a")
	var refused *admission.Error
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "overlaps 127.0.0.0/8") {
		t.Errorf("got %v", err)
	}
}

// A refusal Build can still reach names the configuration key the operator
// edits, beside the request's field.
func TestBuildNamesTheConfigurationKey(t *testing.T) {
	for _, c := range []struct {
		site   string
		mutate func(*config.Config)
		want   string
	}{
		{"home-a", func(cfg *config.Config) {
			s := cfg.Sites["home-a"]
			s.SSH.User = "root"
			cfg.Sites["home-a"] = s
		}, "sites.home-a.ssh.user (user): root is never a deployment's user"},
		{"status", func(cfg *config.Config) {
			s := cfg.Sites["status"]
			s.WebServer.Listen = "192.168.1.5:8480"
			cfg.Sites["status"] = s
		}, `sites.status.web_server.listen (web_server[0].listen): "192.168.1.5:8480" is not`},
		{"home-a", func(cfg *config.Config) {
			cfg.Mesh.Subnet = "127.0.0.0/24"
			s := cfg.Sites["home-a"]
			s.Address = "127.0.0.1"
			cfg.Sites["home-a"] = s
		}, "mesh.subnet (mesh_subnet): 127.0.0.0/24 overlaps 127.0.0.0/8"},
		{"home-a", func(cfg *config.Config) {
			s := cfg.Sites["home-a"]
			s.Address = "10.44.0.255"
			cfg.Sites["home-a"] = s
		}, "sites.home-a.address (address): 10.44.0.255 is the subnet's network or broadcast address"},
	} {
		cfg := fixture(t)
		c.mutate(cfg)
		_, err := admission.Build(cfg, c.site)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("got %v\nwant it to say %q", err, c.want)
		}
	}
}
