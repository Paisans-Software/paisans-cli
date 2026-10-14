package render_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// openSignupFiles is the part of the render signup: open changes: the
// account app's own directory on every site, and the gateway's Caddyfile and
// the account app's snippets.
func openSignupFiles(t *testing.T) map[string]string {
	t.Helper()
	cfg, secrets := fixture(t), fixtureSecrets(t)
	openSignup(cfg, secrets)
	if refusals := validate.Check(cfg).Refusals(); len(refusals) > 0 {
		t.Fatalf("the open signup fixture is refused: %v", refusals)
	}
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatalf("building the plan: %v", err)
	}
	out := map[string]string{}
	for _, f := range plan.Files {
		if strings.HasPrefix(f.Path, "vm/") && (strings.HasSuffix(f.Path, "/infra/caddy/Caddyfile") || strings.Contains(f.Path, "/infra/caddy/snippets/account")) ||
			strings.Contains(f.Path, "/srv/paisans/f2a9/account/") {
			out[f.Path] = f.Content
		}
	}
	return out
}

// The golden tree for an account app with signup: open, beside the main tree,
// which has signup: invite. Regenerated with -update, like the main tree.
func TestGoldenOpenSignup(t *testing.T) {
	files := openSignupFiles(t)
	golden := filepath.Join("testdata", "golden-open-signup")
	if *update {
		if err := os.RemoveAll(golden); err != nil {
			t.Fatal(err)
		}
		for path, content := range files {
			full := filepath.Join(golden, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		t.Log("open signup golden tree rewritten")
		return
	}
	dir := t.TempDir()
	for path, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	compareTrees(t, golden, dir)
}
