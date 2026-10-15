package render_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
)

const (
	haproxyA = "home-a/srv/paisans/f2a9/infra/haproxy/haproxy.cfg"
	haproxyB = "home-b/srv/paisans/f2a9/infra/haproxy/haproxy.cfg"
)

// pocketIDListener is the `listen pocket-id` section of a rendered
// haproxy.cfg, up to the next blank line, or "" when there is none.
func pocketIDListener(cfg string) string {
	i := strings.Index(cfg, "\nlisten pocket-id\n")
	if i < 0 {
		return ""
	}
	section := cfg[i+1:]
	if end := strings.Index(section, "\n\n"); end >= 0 {
		section = section[:end]
	}
	return section
}

// Every HAProxy site serves the active Pocket ID on its own mesh address at
// 1413, with one server per site that runs the pocket-id app, checked at
// /healthz for the 204 only the active instance answers.
func TestPocketIDAPIListenerOnEveryHAProxySite(t *testing.T) {
	files := buildAccount(t, nil)
	for path, address := range map[string]string{haproxyA: "10.44.0.1", haproxyB: "10.44.0.2"} {
		section := pocketIDListener(files[path])
		if section == "" {
			t.Fatalf("%s has no listen pocket-id:\n%s", path, files[path])
		}
		for _, want := range []string{
			"bind " + address + ":1413\n",
			"option httpchk GET /healthz\n",
			"http-check expect status 204\n",
			"default-server inter 3s fall 3 rise 2 on-marked-down shutdown-sessions\n",
			"server home-a 10.44.0.1:1411 check\n",
			"server home-b 10.44.0.2:1411 check",
		} {
			if !strings.Contains(section, want) {
				t.Errorf("%s: listen pocket-id lacks %q:\n%s", path, want, section)
			}
		}
		if strings.Count(section, "\n    server ") != 2 {
			t.Errorf("%s: want two servers:\n%s", path, section)
		}
	}
}

// A pocket-id app pinned to one site is one server, at that site, and every
// HAProxy site still routes to it.
func TestPocketIDAPIListenerFollowsAPinnedApp(t *testing.T) {
	files := buildAccount(t, func(cfg *config.Config, _ *config.Secrets) {
		auth := cfg.Apps["auth"]
		auth.Placement = config.Placement{Mode: config.PlacementPinned, Site: "home-b", Literal: "home-b"}
		cfg.Apps["auth"] = auth
	})
	for _, path := range []string{haproxyA, haproxyB} {
		section := pocketIDListener(files[path])
		if !strings.Contains(section, "server home-b 10.44.0.2:1411 check") || strings.Contains(section, "server home-a") {
			t.Errorf("%s: want home-b's server alone:\n%s", path, section)
		}
	}
}

// withoutPocketID removes the pocket-id app and the apps that need one.
func withoutPocketID(cfg *config.Config, _ *config.Secrets) {
	delete(cfg.Apps, "auth")
	delete(cfg.Apps, "account")
}

// With no pocket-id app there is nothing to route to: no listener, and no
// claim on the port.
func TestPocketIDAPIListenerNeedsAPocketIDApp(t *testing.T) {
	files := buildAccount(t, withoutPocketID)
	for _, path := range []string{haproxyA, haproxyB} {
		if files[path] == "" {
			t.Fatalf("%s was not rendered", path)
		}
		if strings.Contains(files[path], "pocket-id") || strings.Contains(files[path], ":1413") {
			t.Errorf("%s routes to a Pocket ID the deployment does not have:\n%s", path, files[path])
		}
	}
	cfg := fixture(t)
	withoutPocketID(cfg, nil)
	for _, l := range render.SiteListeners(cfg, "home-a") {
		if l.Port == render.PocketIDAPIPort {
			t.Errorf("home-a claims %s for %s without a pocket-id app", l, l.Owner)
		}
	}
}

// The listener is in the port table, on the mesh address only, claimed by the
// pocket-id app, so a collision on a site is refused like any other.
func TestPocketIDAPIPortIsClaimed(t *testing.T) {
	if render.PocketIDAPIPort != 1413 {
		t.Fatalf("PocketIDAPIPort is %d, want 1413", render.PocketIDAPIPort)
	}
	cfg := fixture(t)
	for site, address := range map[string]string{"home-a": "10.44.0.1", "home-b": "10.44.0.2"} {
		var found []render.Listener
		for _, l := range render.SiteListeners(cfg, site) {
			if l.Port == render.PocketIDAPIPort {
				found = append(found, l)
			}
		}
		if len(found) != 1 || found[0].Address != address || found[0].Proto != "tcp" || found[0].Key != "apps.auth" {
			t.Errorf("%s: 1413 claims %v, want one tcp listener on %s keyed apps.auth", site, found, address)
		}
	}
	// vm runs no HAProxy, so it binds no listener.
	for _, l := range render.SiteListeners(cfg, "vm") {
		if l.Port == render.PocketIDAPIPort {
			t.Errorf("vm runs no HAProxy but claims %s", l)
		}
	}
}
