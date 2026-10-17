package selfcheck_test

import (
	"path/filepath"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/selfcheck"
)

// A gateway is asked for a hostname it serves for the deployment, a
// monitor running the toolkit's Caddy for one of its own; a site with no
// Caddy of the toolkit's, or no public_address, has no target.
func TestTargetForTheGatewayAndAMonitor(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range []string{"home-a", "vm"} {
		if got, ok := selfcheck.TargetFor(cfg, site); ok {
			t.Errorf("%s: %+v", site, got)
		}
	}
	got, ok := selfcheck.TargetFor(cfg, "watch")
	want := selfcheck.Target{Deployment: cfg.Deployment(), Site: "watch", Address: "203.0.113.20", Host: "health.example.org"}
	if !ok || got != want {
		t.Errorf("watch: %+v, %v", got, ok)
	}

	vm := cfg.Sites["vm"]
	vm.PublicAddress = "203.0.113.30"
	cfg.Sites["vm"] = vm
	got, ok = selfcheck.TargetFor(cfg, "vm")
	if !ok || got.Address != "203.0.113.30" || got.Host != dns.GatewayHostnames(cfg)[0].Name {
		t.Errorf("vm: %+v, %v", got, ok)
	}

	watch := cfg.Sites["watch"]
	watch.WebServer = &config.WebServer{ManagedBy: config.ManagedByExternal}
	cfg.Sites["watch"] = watch
	if got, ok := selfcheck.TargetFor(cfg, "watch"); ok {
		t.Errorf("a monitor behind the owner's web server: %+v", got)
	}
}
