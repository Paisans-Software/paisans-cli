package admission_test

import (
	"slices"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// A data site needs the watchdog whatever its watchdog mode; a site that
// runs the toolkit's Caddy needs low ports; a monitor behind the owner's
// web server, and every other site, needs neither.
func TestNeeds(t *testing.T) {
	external := &config.WebServer{ManagedBy: config.ManagedByExternal, Listen: "127.0.0.1:8480"}
	for _, c := range []struct {
		name string
		site config.Site
		want admission.Grants
	}{
		{"data", config.Site{Roles: []config.Role{config.RoleData}}, admission.Grants{Watchdog: true}},
		{"gateway", config.Site{Roles: []config.Role{config.RoleGateway}}, admission.Grants{PublicPorts: true}},
		{"Caddy monitor", config.Site{Roles: []config.Role{config.RoleMonitor}}, admission.Grants{PublicPorts: true}},
		{"external monitor", config.Site{Roles: []config.Role{config.RoleMonitor}, WebServer: external}, admission.Grants{}},
		{"apps", config.Site{Roles: []config.Role{config.RoleApps}}, admission.Grants{}},
		{"witness", config.Site{Roles: []config.Role{config.RoleWitness}}, admission.Grants{}},
		{"storage", config.Site{Roles: []config.Role{config.RoleStorage}}, admission.Grants{}},
		{"gateway and external monitor", config.Site{Roles: []config.Role{config.RoleGateway, config.RoleMonitor}, WebServer: external}, admission.Grants{PublicPorts: true}},
		{"data and Caddy monitor", config.Site{Roles: []config.Role{config.RoleData, config.RoleMonitor}}, admission.Grants{Watchdog: true, PublicPorts: true}},
		{"data and external monitor", config.Site{Roles: []config.Role{config.RoleData, config.RoleMonitor}, WebServer: external}, admission.Grants{Watchdog: true}},
		{"no roles", config.Site{}, admission.Grants{}},
		{"data and gateway", config.Site{Roles: []config.Role{config.RoleData, config.RoleGateway}}, admission.Grants{Watchdog: true, PublicPorts: true}},
	} {
		if got := admission.Needs(c.site); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// Missing names each grant needed and not given, by its flag's name.
func TestMissing(t *testing.T) {
	both := admission.Grants{Watchdog: true, PublicPorts: true}
	for _, c := range []struct {
		needed, granted admission.Grants
		want            []string
	}{
		{both, admission.Grants{}, []string{"watchdog", "public-ports"}},
		{both, admission.Grants{Watchdog: true}, []string{"public-ports"}},
		{both, both, nil},
		{admission.Grants{}, both, nil},
	} {
		if got := c.needed.Missing(c.granted); !slices.Equal(got, c.want) {
			t.Errorf("%+v given %+v: %q, want %q", c.needed, c.granted, got, c.want)
		}
	}
}
