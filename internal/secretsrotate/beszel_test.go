package secretsrotate

import (
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// A beszel hub's admin_password is handed to the hub's own stack on its
// site, and to nothing else; an app that is not a hub is refused.
func TestBeszelAdminRotatesOnTheHubsSite(t *testing.T) {
	cfg := &config.Config{
		Sites: map[string]config.Site{
			"watch":  {Roles: []config.Role{config.RoleMonitor}, Address: "10.0.0.4"},
			"home-a": {Roles: []config.Role{config.RoleData, config.RoleApps}, Address: "10.0.0.1"},
		},
		Apps: map[string]config.App{
			"stats":  {Blueprint: config.BlueprintBeszel, Hostname: "status.health.example.org", Placement: config.Placement{Mode: config.PlacementPinned, Site: "watch"}},
			"health": {Blueprint: config.BlueprintUptime, Hostname: "health.example.org", Placement: config.Placement{Mode: config.PlacementPinned, Site: "watch"}},
		},
	}
	e := Env{Cfg: cfg}
	r := BeszelAdmin("stats")
	if r.Path() != "apps.stats.admin_password" {
		t.Fatalf("path %q", r.Path())
	}
	if c := r.Cost(); c != (Cost{}) {
		t.Fatalf("cost %+v, want none to members", c)
	}
	if err := r.(Prechecker).Precheck(e); err != nil {
		t.Fatal(err)
	}
	if got := usesOf(e, r); !slices.Equal(got, []string{"watch:stats"}) {
		t.Fatalf("users %v, want the hub's own stack on watch", got)
	}
	if ok, err := r.Accepts(e, "anything"); !ok || err != nil {
		t.Fatalf("Accepts %v %v", ok, err)
	}
	for _, tc := range []struct{ app, want string }{{"nowhere", "declares no app"}, {"health", "beszel"}} {
		err := BeszelAdmin(tc.app).(Prechecker).Precheck(e)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want a refusal naming %q", tc.app, err, tc.want)
		}
	}
}

// A site's beszel_token is handed to the hub's stack first and then to the
// site's infra stack; a site the configuration lacks and a deployment with no
// hub are refused.
func TestBeszelTokenLearnedByTheHubBeforeTheAgent(t *testing.T) {
	cfg := &config.Config{
		Sites: map[string]config.Site{
			"watch":  {Roles: []config.Role{config.RoleMonitor}, Address: "10.0.0.4"},
			"home-a": {Roles: []config.Role{config.RoleData, config.RoleApps}, Address: "10.0.0.1"},
		},
		Apps: map[string]config.App{
			"stats": {Blueprint: config.BlueprintBeszel, Hostname: "status.health.example.org", Placement: config.Placement{Mode: config.PlacementPinned, Site: "watch"}},
		},
	}
	e := Env{Cfg: cfg}
	r := BeszelToken("home-a")
	if r.Path() != "sites.home-a.beszel_token" {
		t.Fatalf("path %q", r.Path())
	}
	if err := r.(Prechecker).Precheck(e); err != nil {
		t.Fatal(err)
	}
	if got := usesOf(e, r); !slices.Equal(got, []string{"watch:stats", "home-a:infra"}) {
		t.Fatalf("users %v", got)
	}
	if err := BeszelToken("nowhere").(Prechecker).Precheck(e); err == nil || !strings.Contains(err.Error(), "declares no site") {
		t.Errorf("unknown site: %v", err)
	}
	delete(cfg.Apps, "stats")
	if err := r.(Prechecker).Precheck(e); err == nil || !strings.Contains(err.Error(), "beszel") {
		t.Errorf("no hub: %v", err)
	}
}

// A hub's key pair is handed to the hub's stack and then to every site's
// infra stack, whose agent checks the hub by its public half.
func TestBeszelKeyReachesEverySitesAgent(t *testing.T) {
	cfg := &config.Config{
		Sites: map[string]config.Site{
			"watch":  {Roles: []config.Role{config.RoleMonitor}, Address: "10.0.0.4"},
			"home-a": {Roles: []config.Role{config.RoleData, config.RoleApps}, Address: "10.0.0.1"},
			"home-b": {Roles: []config.Role{config.RoleData, config.RoleApps}, Address: "10.0.0.2"},
		},
		Apps: map[string]config.App{
			"stats": {Blueprint: config.BlueprintBeszel, Hostname: "status.health.example.org", Placement: config.Placement{Mode: config.PlacementPinned, Site: "watch"}},
		},
	}
	e := Env{Cfg: cfg}
	if !IsBeszelKey(cfg, "stats", "hub") || IsBeszelKey(cfg, "stats", "other") {
		t.Fatal("IsBeszelKey misreads the hub's key group")
	}
	r := BeszelKey(cfg, "stats")
	if r.Path() != "apps.stats.hub" {
		t.Fatalf("path %q", r.Path())
	}
	if err := r.(Prechecker).Precheck(e); err != nil {
		t.Fatal(err)
	}
	want := []string{"watch:stats", "home-a:infra", "home-b:infra", "watch:infra"}
	if got := usesOf(e, r); !slices.Equal(got, want) {
		t.Fatalf("users %v, want %v", got, want)
	}
}
