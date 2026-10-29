package render_test

import (
	"strings"
	"testing"
)

// An app whose blueprint says hostname: none gets no route: no host block in
// the gateway's Caddyfile and no snippet beside it, since there is no name
// for either to serve.
func TestAHostlessAppGetsNoRoute(t *testing.T) {
	plan := build(t)
	for _, f := range plan.Files {
		if strings.HasSuffix(f.Path, "/snippets/notify.caddy") {
			t.Errorf("%s was rendered for the notifier, which takes no hostname", f.Path)
		}
		if strings.HasSuffix(f.Path, "/Caddyfile") && strings.Contains(string(f.Content), "snippets/notify.caddy") {
			t.Errorf("%s routes the notifier, which takes no hostname", f.Path)
		}
	}
}

// The monitor checks a hostless app over the mesh only: its health route on
// each site it runs on, with no public check and no Host header naming a
// hostname it has none of.
func TestAHostlessAppIsCheckedOverTheMesh(t *testing.T) {
	monitors := byName(renderSeed(t, fixture(t), fixtureSecrets(t)))
	if m, ok := monitors["notify — public"]; ok {
		t.Errorf("a public check of the notifier: %v", m)
	}
	direct, ok := monitors["notify — direct (vm)"]
	if !ok {
		t.Fatalf("no direct check of the notifier: %v", monitors)
	}
	if direct["url"] != "http://10.44.0.3:8085/healthz" {
		t.Errorf("direct check url %v", direct["url"])
	}
	if _, ok := direct["request_headers"]; ok {
		t.Errorf("direct check names a host: %v", direct["request_headers"])
	}
	for name, m := range monitors {
		if u, _ := m["url"].(string); strings.HasPrefix(u, "https:///") || strings.HasPrefix(u, "https://:") {
			t.Errorf("%s checks %s, a URL with no hostname", name, u)
		}
	}
}
