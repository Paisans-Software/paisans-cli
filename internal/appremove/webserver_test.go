package appremove

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// webHost is a monitor that records every command it runs and keeps the
// manifest as a file of its own.
type webHost struct {
	ran      *[]string
	manifest map[string]string
}

func (h webHost) Run(command string) (string, error) {
	*h.ran = append(*h.ran, command)
	return "", nil
}
func (h webHost) RunInput(command, _ string) (string, error) { return h.Run(command) }
func (h webHost) Describe() string                           { return "watch" }
func (h webHost) ReadFile(path string) (string, bool, error) {
	c, ok := h.manifest[path]
	return c, ok, nil
}
func (h webHost) WriteFile(path, content string, _ uint32) error {
	h.manifest[path] = content
	return nil
}

const manifestPath = "/srv/paisans/f2a9/.paisans-manifest.json"

// app remove deletes the app's reference file, its own, and says which
// block the host's owner removes from their web server.
func TestAppRemoveNamesTheBlockTheOwnerRemoves(t *testing.T) {
	cfg := fixture(t)
	d := cfg.Deployment()
	health := render.ManifestWebServer{App: "health", Hostname: "health.example.org", Path: "/srv/paisans/f2a9/web-server/health.caddy", SHA256: "ab", Server: "caddy", Config: "/etc/caddy/Caddyfile"}
	other := render.ManifestWebServer{App: "other", Hostname: "other.example.org", Path: "/srv/paisans/f2a9/web-server/other.caddy", SHA256: "cd", Server: "caddy", Config: "/etc/caddy/Caddyfile"}
	m, _ := json.Marshal(render.Manifest{Version: 1, Build: version.Current(), Files: []render.ManifestFile{{Path: "srv/paisans/f2a9/health/compose.yaml", SHA256: "cd"}}, WebServer: []render.ManifestWebServer{other, health}})
	var ran []string
	h := webHost{&ran, map[string]string{manifestPath: string(m)}}

	sp, err := planSite(cfg, "health", SiteState{Site: "watch", Inventory: &hostcheck.Inventory{Manifest: true, ManifestFiles: []render.ManifestFile{}, ManifestWebServer: []render.ManifestWebServer{other, health}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(sp.WebServer) != 1 || sp.WebServer[0] != health || sp.Empty() {
		t.Fatalf("planned %+v", sp.WebServer)
	}
	rec := &ui.Recorder{}
	(&Plan{App: "health", Deployment: d, Sites: []SitePlan{sp}}).Show(rec)
	if !rec.Has("detail", "remove the reference file "+health.Path+"; the host's owner removes the block from caddy") {
		t.Errorf("plan:\n%s", rec.Lines())
	}

	e := &Executor{}
	if err := e.webServer(h, d, sp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(ran, "\n"), "rm -f -- '"+health.Path+"'") {
		t.Errorf("ran %v", ran)
	}
	for _, c := range ran {
		for _, server := range []string{"caddy", "nginx", "apachectl", "docker", "systemctl"} {
			if strings.Contains(c, server) && !strings.Contains(c, health.Path) {
				t.Errorf("ran against the web server: %s", c)
			}
		}
	}
	if !strings.Contains(strings.Join(e.Kept, "\n"), "the host's owner removes the block for health.example.org") {
		t.Errorf("kept %v", e.Kept)
	}
	var after render.Manifest
	json.Unmarshal([]byte(h.manifest[manifestPath]), &after)
	if len(after.WebServer) != 1 || after.WebServer[0] != other || len(after.Files) != 1 {
		t.Errorf("manifest %+v", after)
	}
}

// An entry that is not a reference file of this deployment's, by a path
// with .., outside the deployment's root, or elsewhere in it, is never
// deleted: it is kept with its entry and reported.
func TestAppRemoveKeepsAnEntryOutsideTheReferenceDirectory(t *testing.T) {
	cfg := fixture(t)
	d := cfg.Deployment()
	for _, p := range []string{"/srv/paisans/f2a9/web-server/../../../etc/passwd", "/etc/passwd", "/srv/paisans/f2a9/health/compose.yaml", "srv/paisans/f2a9/web-server/health.caddy"} {
		entry := render.ManifestWebServer{App: "health", Hostname: "health.example.org", Path: p, SHA256: "ab", Server: "caddy"}
		m, _ := json.Marshal(render.Manifest{Version: 1, Build: version.Current(), WebServer: []render.ManifestWebServer{entry}})
		var ran []string
		h := webHost{&ran, map[string]string{manifestPath: string(m)}}
		e := &Executor{}
		if err := e.webServer(h, d, SitePlan{Site: "watch", WebServer: []render.ManifestWebServer{entry}}); err != nil {
			t.Fatal(err)
		}
		if len(ran) != 0 {
			t.Errorf("%s: ran %v", p, ran)
		}
		if !strings.Contains(strings.Join(e.Kept, "\n"), "not a reference file of this deployment's. It names "+p+",") {
			t.Errorf("%s: kept %v", p, e.Kept)
		}
		var after render.Manifest
		json.Unmarshal([]byte(h.manifest[manifestPath]), &after)
		if len(after.WebServer) != 1 {
			t.Errorf("%s: manifest %+v", p, after)
		}
	}
}
