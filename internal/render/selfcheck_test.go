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

// Caddy orders directives such as redir, rewrite, basic_auth and
// forward_auth before handle, so one a blueprint's snippet put at the top of
// a host block would answer before the self check. At the top of every host
// block there is therefore only the self check, the error handling, header
// stripping, matchers and handle blocks: a blueprint's routing is always
// inside a handle, which the self check's single path outranks.
func TestNoRoutingSitsBesideTheSelfCheck(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "golden*", "*", "srv", "paisans", "f2a9", "infra", "caddy", "Caddyfile"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no golden Caddyfiles: %v", err)
	}
	allowed := []string{"import paisans_self", "import upstream_unavailable", "request_header ", "@", "handle ", "handle {", "}"}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		depth, host := 0, false
		for n, line := range strings.Split(string(b), "\n") {
			code := strings.TrimSpace(line)
			if code == "" || strings.HasPrefix(code, "#") {
				continue
			}
			if depth == 0 {
				host = strings.HasSuffix(code, "{") && code != "{" && !strings.HasPrefix(code, "(")
			} else if depth == 1 && host {
				ok := false
				for _, a := range allowed {
					ok = ok || strings.HasPrefix(code, a)
				}
				if !ok {
					t.Errorf("%s:%d: %q at the top of a host block", f, n+1, code)
				}
			}
			depth += strings.Count(code, "{") - strings.Count(code, "}")
		}
	}
}
