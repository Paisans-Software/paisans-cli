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
