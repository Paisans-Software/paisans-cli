package render_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/render"
)

// Every Caddy's Caddyfile and snippets are rendered into one directory, and
// its compose file mounts that directory, never a file in it: apply replaces
// a file by renaming a new one over it, and a single file bind mount keeps
// the file it was created with, so a reload after an apply read the old
// Caddyfile and a new hostname never got a site block.
func TestCaddyMountsItsConfigurationAsOneDirectory(t *testing.T) {
	plan := build(t)
	dir := render.CaddyConfigDir(plan.Deployment)
	if dir != "srv/paisans/f2a9/infra/caddy/etc" {
		t.Fatalf("CaddyConfigDir = %q", dir)
	}
	if got := render.SnippetsDir(plan.Deployment); got != dir+"/snippets" {
		t.Errorf("SnippetsDir = %q, not under %s", got, dir)
	}
	content := map[string]string{}
	for _, f := range plan.Files {
		content[f.Path] = f.Content
	}
	for _, site := range []string{"vm", "watch"} {
		if _, ok := content[site+"/"+dir+"/Caddyfile"]; !ok {
			t.Errorf("%s: no Caddyfile rendered in %s", site, dir)
		}
		compose := content[site+"/srv/paisans/f2a9/infra/compose.yaml"]
		if !strings.Contains(compose, "- /"+dir+":"+render.CaddyConfigMount+":ro\n") {
			t.Errorf("%s: Caddy does not mount /%s at %s read only:\n%s", site, dir, render.CaddyConfigMount, compose)
		}
		if strings.Contains(compose, ":/etc/caddy/Caddyfile") || strings.Contains(compose, ":/etc/caddy/snippets") {
			t.Errorf("%s: Caddy still mounts a file or directory inside %s on its own", site, render.CaddyConfigMount)
		}
	}
}
