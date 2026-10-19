package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// apply plans the gateway with Caddy's tag resolved to the build it names,
// which is what a real `paisans deployment apply` hands its plan, and the dry run says
// which build that is.
func TestApplyPlansCaddyAtTheResolvedBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	gateway := strings.Replace(freshSite, "roles: [data, apps]", "roles: [data, apps, gateway]", 1)
	if err := os.WriteFile(path, []byte(gateway), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	secrets := &config.Secrets{Version: 1}
	if _, err := secretsgen.Fill(cfg, secrets); err != nil {
		t.Fatal(err)
	}
	plan, err := planSiteApply(cfg, secrets, "home-a", emptyHost{})
	if err != nil {
		t.Fatal(err)
	}
	want := "ghcr.io/paisans-software/caddy:2.0.0@" + testCaddyDigest
	if c := plan.Caddy(); c == nil || c.Reference != want {
		t.Fatalf("planned %+v, want %s", c, want)
	}
	rec := &ui.Recorder{Verbose_: true}
	reportNotes(rec, plan, map[string]bool{})
	if !strings.Contains(rec.Lines(), testCaddyDigest) {
		t.Errorf("the plan does not say which build Caddy runs:\n%s", rec.Lines())
	}
}
