package appremove

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/webserver/webservertest"
)

// webHost is a monitor whose web server includes the toolkit's directory:
// commands go to the web server, and the manifest is a file of its own.
type webHost struct {
	*webservertest.Server
	manifest map[string]string
}

func (h webHost) Run(command string) (string, error)         { return h.Plain().Run(command) }
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

// app remove deletes the app's own file from the web server's include
// directory, and nothing else there, validates and reloads the server, and
// drops the file's entry from the manifest, keeping every other.
func TestAppRemoveDeletesItsSiteFileOnly(t *testing.T) {
	cfg := fixture(t)
	d := cfg.Deployment()
	server := webservertest.New("")
	server.Files["/opt/caddy/paisans.d/paisans-f2a9-health.caddy"] = "health.example.org {\n}\n"
	server.Files["/opt/caddy/paisans.d/blog.caddy"] = "blog.example.org {\n}\n"
	health := render.ManifestWebServer{App: "health", Path: "/opt/caddy/paisans.d/paisans-f2a9-health.caddy", SHA256: sum("health.example.org {\n}\n"), Server: "caddy", Container: "caddy-1", Config: "/etc/caddy/Caddyfile"}
	other := render.ManifestWebServer{App: "other", Path: "/opt/caddy/paisans.d/paisans-f2a9-other.caddy", SHA256: "ab", Server: "caddy", Container: "caddy-1", Config: "/etc/caddy/Caddyfile"}
	m, _ := json.Marshal(render.Manifest{Version: 1, Files: []render.ManifestFile{{Path: "srv/paisans/f2a9/health/compose.yaml", SHA256: "cd"}}, WebServer: []render.ManifestWebServer{other, health}})
	h := webHost{server, map[string]string{manifestPath: string(m)}}

	sp, err := planSite(cfg, "health", SiteState{Site: "watch", Inventory: &hostcheck.Inventory{Manifest: true, ManifestFiles: []render.ManifestFile{}, ManifestWebServer: []render.ManifestWebServer{other, health}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(sp.WebServer) != 1 || sp.WebServer[0] != health || sp.Empty() {
		t.Fatalf("planned %+v", sp.WebServer)
	}
	rec := &ui.Recorder{}
	(&Plan{App: "health", Sites: []SitePlan{sp}}).Show(rec)
	if !rec.Has("detail", health.Path+", from the include directory of caddy") {
		t.Errorf("plan:\n%s", rec.Lines())
	}

	e := &Executor{}
	if err := e.webServer(h, d, sp); err != nil {
		t.Fatal(err)
	}
	if _, ok := server.Files[health.Path]; ok {
		t.Error("the site file stayed")
	}
	if _, ok := server.Files["/opt/caddy/paisans.d/blog.caddy"]; !ok {
		t.Error("somebody else's file went")
	}
	if !server.Ran("caddy validate") || !server.Ran("caddy reload") {
		t.Errorf("asked %v", server.Asked)
	}
	var after render.Manifest
	json.Unmarshal([]byte(h.manifest[manifestPath]), &after)
	if len(after.WebServer) != 1 || after.WebServer[0] != other || len(after.Files) != 1 {
		t.Errorf("manifest %+v", after)
	}

	// Run again, the file is already gone: nothing to do, nothing reloaded.
	server.Asked = nil
	if err := e.webServer(h, d, sp); err != nil || server.Ran("reload") {
		t.Errorf("again: %v %v", err, server.Asked)
	}
}

// A removal the web server refuses puts the file back, keeps its entry,
// and stops the run with the server's error.
func TestAppRemoveStopsWhenTheServerRefuses(t *testing.T) {
	cfg := fixture(t)
	server := webservertest.New("")
	content := "health.example.org {\n}\n"
	path := "/etc/nginx/paisans.d/paisans-f2a9-health.conf"
	server.Files[path] = content
	server.Validate = "nginx: [emerg] unknown directive in /etc/nginx/nginx.conf:12"
	entry := render.ManifestWebServer{App: "health", Path: path, SHA256: sum(content), Server: "nginx", Config: "/etc/nginx/nginx.conf"}
	m, _ := json.Marshal(render.Manifest{Version: 1, WebServer: []render.ManifestWebServer{entry}})
	h := webHost{server, map[string]string{manifestPath: string(m)}}
	err := (&Executor{}).webServer(h, cfg.Deployment(), SitePlan{Site: "watch", WebServer: []render.ManifestWebServer{entry}})
	if err == nil || !strings.Contains(err.Error(), "unknown directive") {
		t.Fatalf("err %v", err)
	}
	if server.Files[path] != content || server.Ran("reload") || h.manifest[manifestPath] != string(m) {
		t.Errorf("not put back: %v %v", server.Files, server.Asked)
	}
}

// A web server on the host itself is validated after the removal and not
// reloaded: what is left says the command that reloads it.
func TestAppRemoveLeavesTheReloadOfABareMetalServer(t *testing.T) {
	cfg := fixture(t)
	server := webservertest.New("")
	server.Units = []string{"nginx"}
	content := "health.example.org {\n}\n"
	path := "/etc/nginx/paisans.d/paisans-f2a9-health.conf"
	server.Files[path] = content
	entry := render.ManifestWebServer{App: "health", Path: path, SHA256: sum(content), Server: "nginx", Config: "/etc/nginx/nginx.conf"}
	m, _ := json.Marshal(render.Manifest{Version: 1, WebServer: []render.ManifestWebServer{entry}})
	h := webHost{server, map[string]string{manifestPath: string(m)}}
	e := &Executor{}
	if err := e.webServer(h, cfg.Deployment(), SitePlan{Site: "watch", WebServer: []render.ManifestWebServer{entry}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := server.Files[path]; ok || !server.Ran("nginx -t") || server.Ran("nginx -s reload") || server.Ran("systemctl reload") {
		t.Errorf("files %v, asked %v", server.Files, server.Asked)
	}
	if !strings.Contains(strings.Join(e.Kept, "\n"), "sudo systemctl reload nginx") {
		t.Errorf("kept %v", e.Kept)
	}
}
