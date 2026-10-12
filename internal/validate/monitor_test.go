package validate_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

// sharing returns the messages monitor-shares-a-site gave, by key.
func sharing(result validate.Result) map[string]string {
	out := map[string]string{}
	for _, f := range result.Findings {
		if f.Rule == "monitor-shares-a-site" {
			out[f.Key] = f.Message
		}
	}
	return out
}

// One warning per site, naming every role the monitor shares, gateway first,
// and saying what it cannot report.
func TestMonitorSharesASiteNamesWhatItShares(t *testing.T) {
	for _, tc := range []struct {
		fixture, key string
		want         []string
	}{
		{"monitor-on-gateway", "sites.vm.roles", []string{"beside gateway and witness", "It cannot report vm failing", "Nothing reports etcd losing its tiebreaker either", "roles: [monitor]"}},
		{"monitor-on-witness", "sites.watch.roles", []string{"beside witness.", "Nothing reports etcd losing its tiebreaker either"}},
		{"monitor-shares-a-site", "sites.home-a.roles", []string{"beside data and apps.", "It cannot report home-a failing"}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			got := sharing(validate.Check(load(t, tc.fixture)))
			if len(got) != 1 {
				t.Fatalf("want one warning, got %v", got)
			}
			msg, ok := got[tc.key]
			if !ok {
				t.Fatalf("no warning at %s: %v", tc.key, got)
			}
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("message lacks %q: %s", w, msg)
				}
			}
		})
	}
}

// A monitor alone, or beside storage alone, hides nothing and is not warned.
func TestAMonitorAloneOrBesideStorageIsNotWarned(t *testing.T) {
	cfg := load(t, "valid")
	if got := sharing(validate.Check(cfg)); len(got) != 0 {
		t.Fatalf("a monitor alone was warned: %v", got)
	}
	watch := cfg.Sites["watch"]
	watch.Roles = []config.Role{config.RoleMonitor, config.RoleStorage}
	cfg.Sites["watch"] = watch
	if got := sharing(validate.Check(cfg)); len(got) != 0 {
		t.Fatalf("a monitor beside storage was warned: %v", got)
	}
}

// web_server on a gateway is refused in either mode: the gateway's Caddy
// serves the monitor there.
func TestWebServerOnGatewayIsRefusedInEitherMode(t *testing.T) {
	for _, ws := range []*config.WebServer{
		{ManagedBy: config.ManagedByPaisans},
		{ManagedBy: config.ManagedByExternal, Listen: "127.0.0.1:8480"},
	} {
		cfg := load(t, "monitor-on-gateway")
		vm := cfg.Sites["vm"]
		vm.WebServer = ws
		cfg.Sites["vm"] = vm
		if !refusedFor(validate.Check(cfg), "web-server-on-gateway", "sites.vm.web_server") {
			t.Errorf("managed_by %s on the gateway was not refused", ws.ManagedBy)
		}
	}
}
