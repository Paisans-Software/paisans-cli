package apply_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// healthNetwork is the uptime app's compose default network in the fixture,
// <project>_default, the name Compose gives it.
const healthNetwork = "paisans-f2a9-health_default"

const (
	pinnedPool   = `[{"Subnet":"10.255.255.0/29","Gateway":"10.255.255.1"}]`
	unpinnedPool = `[{"Subnet":"172.18.0.0/16","Gateway":"172.18.0.1"}]`
)

// external renders the fixture with the monitor site with managed_by:
// external, which pins the uptime app's compose network.
func external(t *testing.T) *render.Plan {
	t.Helper()
	return planWith(t, func(cfg *config.Config) {
		watch := cfg.Sites["watch"]
		watch.WebServer = &config.WebServer{ManagedBy: config.ManagedByExternal, Listen: "127.0.0.1:8480"}
		cfg.Sites["watch"] = watch
	})
}

// appliedFrom runs a first apply of rendered on site, so that a test about a
// later apply starts from a host that has everything and records it.
func appliedFrom(t *testing.T, site string, rendered *render.Plan) *fakeHost {
	t.Helper()
	host := newHost()
	first, err := apply.Build(site, rendered, acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(first, host); err != nil {
		t.Fatal(err)
	}
	host.commands = nil
	return host
}

func healthAction(t *testing.T, p *apply.Plan) apply.Action {
	t.Helper()
	for _, a := range p.Actions {
		if a.Stack == "health" {
			return a
		}
	}
	t.Fatalf("no action on health in %+v", p.Actions)
	return apply.Action{}
}

// Switching to managed_by: external pins the network, and the network the
// host already has keeps the pool Docker gave it. The plan takes the stack
// down first, so that `up` creates the network the compose file declares,
// and building the plan changes nothing on the host.
func TestPinningANetworkTakesTheStackDown(t *testing.T) {
	host := appliedFrom(t, "watch", plan(t))
	host.networks = map[string]string{healthNetwork: unpinnedPool}

	p, err := apply.Build("watch", external(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	action := healthAction(t, p)
	if !action.Down || !action.Recreate {
		t.Fatalf("a newly pinned network is not taken down and recreated: %+v", action)
	}
	if !strings.Contains(action.Reason, "10.255.255.0/29") {
		t.Errorf("the reason does not say which network: %q", action.Reason)
	}
	if host.ran("compose.yaml down") {
		t.Fatalf("building the plan took a stack down: %v", host.commands)
	}

	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	down := host.indexOf("health/compose.yaml down")
	up := host.indexOf("health/compose.yaml up -d")
	if down < 0 || up < 0 || down > up {
		t.Fatalf("want down then up for health, got %v", host.commands)
	}
}

// Leaving managed_by: external drops the pin, and a network still on the
// pinned pool would keep its old gateway, which TRUST_PROXY no longer names.
func TestDroppingAPinTakesTheStackDown(t *testing.T) {
	host := appliedFrom(t, "watch", external(t))
	host.networks = map[string]string{healthNetwork: pinnedPool}

	p, err := apply.Build("watch", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if action := healthAction(t, p); !action.Down {
		t.Fatalf("a network left on the old pin is not taken down: %+v", action)
	}
}

// A network that already matches what the compose file declares, pinned or
// not, plans nothing beyond the ordinary recreate.
func TestAMatchingNetworkIsLeftUp(t *testing.T) {
	for name, tc := range map[string]struct {
		rendered *render.Plan
		pool     string
	}{
		"pinned":   {external(t), pinnedPool},
		"unpinned": {plan(t), unpinnedPool},
		// An IPv6 pool Docker added beside the IPv4 one is not a difference.
		"pinned with ipv6":   {external(t), `[{"Subnet":"10.255.255.0/29","Gateway":"10.255.255.1"},{"Subnet":"fd00:1::/64","Gateway":"fd00:1::1"}]`},
		"unpinned with ipv6": {plan(t), `[{"Subnet":"172.18.0.0/16","Gateway":"172.18.0.1"},{"Subnet":"fd00:1::/64"}]`},
	} {
		t.Run(name, func(t *testing.T) {
			host := appliedFrom(t, "watch", tc.rendered)
			host.networks = map[string]string{healthNetwork: tc.pool}
			p, err := apply.Build("watch", tc.rendered, acmeModule(t), host, apply.Recreate("health"))
			if err != nil {
				t.Fatal(err)
			}
			if action := healthAction(t, p); action.Down {
				t.Fatalf("a matching network is taken down: %+v", action)
			}
			if err := apply.Execute(p, host); err != nil {
				t.Fatal(err)
			}
			if host.ran("compose.yaml down") {
				t.Fatalf("a matching network was taken down: %v", host.commands)
			}
		})
	}
}

// A stack taken down starts on fresh anonymous volumes, so the old ones are
// dangling afterwards, and they may hold the stack's data. They are kept.
func TestATakenDownStackKeepsItsAnonymousVolumes(t *testing.T) {
	const old = "3a37a98261c4f658850d43b3d0ddc746ae25d9ec6bb58e83132662b7ea646191"
	host := appliedFrom(t, "watch", plan(t))
	host.networks = map[string]string{healthNetwork: unpinnedPool}
	host.stackVolumes = map[string]string{"health": old + "\n"}
	host.dangling = old + "\n"

	p, err := apply.Build("watch", external(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if !healthAction(t, p).Down {
		t.Fatal("the test needs a stack taken down")
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if len(host.removedVolumes) != 0 {
		t.Errorf("a taken down stack's old anonymous volumes were removed: %v", host.removedVolumes)
	}
}

// The infrastructure stack is never taken down: that stops the database and
// the gateway together. A mismatch is a note telling the operator what to do.
func TestTheInfrastructureStackIsNeverTakenDown(t *testing.T) {
	host := newHost()
	host.networks = map[string]string{"paisans-f2a9-infra_default": pinnedPool}
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range p.Actions {
		if a.Down {
			t.Errorf("%s is taken down", a.Stack)
		}
	}
	var noted bool
	for _, note := range p.Notes {
		// The operator has to act on it, so it carries a hint, and the hint
		// alone says what to do.
		if strings.Contains(note.Text, "paisans-f2a9-infra_default") && strings.Contains(note.Hint, "--recreate infra") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("no note tells the operator about the infrastructure network: %v", p.Notes)
	}
}

// A Docker that cannot answer is not a host without the network: the plan
// stops with Docker's error rather than missing a stale network.
func TestAFailedNetworkProbeStopsThePlan(t *testing.T) {
	host := appliedFrom(t, "watch", plan(t))
	host.fail = "docker network ls"
	_, err := apply.Build("watch", external(t), acmeModule(t), host)
	if err == nil || !strings.Contains(err.Error(), "compose networks") {
		t.Fatalf("got %v, want the probe's failure", err)
	}
}

// The infrastructure stack is never taken down by apply; the plan names the
// command to run at a time the site can be out, with Patroni's stop timeout,
// so a leading Patroni it stops disarms the watchdog.
func TestAStaleInfraNetworkNamesItsDownWithPatronisTimeout(t *testing.T) {
	host := appliedFrom(t, "home-a", plan(t))
	host.networks = map[string]string{"paisans-f2a9-infra_default": pinnedPool}
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.Recreate("infra"))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range p.Notes {
		if strings.HasPrefix(n.Hint, "infra network differs") {
			if !strings.Contains(n.Text, "/infra/compose.yaml down --timeout 60`") {
				t.Errorf("the down it names has no stop timeout: %s", n.Text)
			}
			return
		}
	}
	t.Fatalf("no note for the stale infra network: %+v", p.Notes)
}
