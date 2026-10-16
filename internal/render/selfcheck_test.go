package render_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every host block of the gateway's Caddyfile answers the self check first,
// with the deployment's token.
func TestEveryRouteAnswersAsTheDeployment(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "golden", "vm", "srv", "paisans", "f2a9", "infra", "caddy", "Caddyfile"))
	if err != nil {
		t.Fatal(err)
	}
	c := string(b)
	if !strings.Contains(c, "@paisans_self path /.well-known/paisans/f2a9") || !strings.Contains(c, `respond "f2a9" 200`) {
		t.Fatalf("no self check snippet:\n%s", c)
	}
	blocks := strings.Count(c, "\timport upstream_unavailable\n")
	if imports := strings.Count(c, "\timport paisans_self\n"); imports != blocks || blocks == 0 {
		t.Errorf("%d host blocks, %d import the self check", blocks, imports)
	}
}
