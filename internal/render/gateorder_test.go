package render_test

import (
	"strings"
	"testing"
)

// Caddy sorts the directives of a block by its directive order, which puts
// handle ahead of route, and the gate is a route. An app snippet that routes
// with handle, as engagement's does, would match before the gate ran and
// reach the app unauthenticated. So a gated hostname's catch-all wraps the
// gate and the snippet in a route of their own, which runs them as written:
// the gate first.
func TestTheGateRunsBeforeTheAppsHandles(t *testing.T) {
	caddyfile := planFiles(build(t))["vm/srv/paisans/f2a9/infra/caddy/etc/Caddyfile"]
	start := strings.Index(caddyfile, "\ndigest.example.org {")
	if start < 0 {
		t.Fatal("no digest.example.org block")
	}
	block := caddyfile[start:]
	block = block[:strings.Index(block, "\n}\n")]
	want := "\thandle {\n\t\troute {\n\t\t\timport gate\n\t\t\timport /etc/caddy/snippets/digest.caddy\n\t\t}\n\t}"
	if !strings.Contains(block, want) {
		t.Errorf("the gated catch-all does not run the gate first in a route:\n%s", block)
	}
}
