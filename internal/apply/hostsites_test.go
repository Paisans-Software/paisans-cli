package apply_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// A gateway's apply creates the deployment's Caddy site directory before the
// first container that mounts it, with a command that leaves an existing one
// alone, and never writes into it or records it. A site without the gateway
// role never asks.
func TestTheGatewayCreatesTheHostsSiteDirectoryAndNothingInIt(t *testing.T) {
	host := newHost()
	p, err := apply.Build("vm", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if !p.HostSites {
		t.Fatal("the gateway's plan does not create the host's Caddy site directory")
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	ensure, validate := -1, -1
	for i, c := range host.commands {
		if c == apply.HostSitesCommand(p.Deployment) && ensure < 0 {
			ensure = i
		}
		if strings.Contains(c, "caddy validate") && validate < 0 {
			validate = i
		}
	}
	if ensure < 0 || validate < 0 || ensure > validate {
		t.Fatalf("the directory is not created before the validation mounts it: %q", host.commands)
	}
	sitesDir := render.HostSitesDir(p.Deployment)
	for path := range host.files {
		if strings.HasPrefix(path, sitesDir+"/") {
			t.Errorf("apply wrote %s into the host's own directory", path)
		}
	}
	if strings.Contains(host.files["/srv/paisans/f2a9/.paisans-manifest.json"], sitesDir) {
		t.Error("the manifest records the host's own directory")
	}

	other := newHost()
	q, err := apply.Build("home-a", plan(t), acmeModule(t), other)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(q, other); err != nil {
		t.Fatal(err)
	}
	if q.HostSites || other.ran(sitesDir) {
		t.Error("a site without the gateway role touched the host's Caddy site directory")
	}
}

// The deployment's user creates the host sites directory, in its own root,
// with no chown it could not make.
func TestHostSitesCommandRunsAsTheUser(t *testing.T) {
	d := deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
	got := apply.HostSitesCommand(d)
	if got != "[ -d '/srv/paisans/f2a9/caddy.d' ] || install -d -m 755 '/srv/paisans/f2a9/caddy.d'" {
		t.Errorf("got %s", got)
	}
}
