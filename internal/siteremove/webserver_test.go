package siteremove_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/siteremove"
	"github.com/paisans-software/paisans-stack/internal/webserver/webservertest"
)

const siteFile = webDir + "paisans.d/paisans-f2a9-status.caddy"

// webOn gives the site's host a Caddy in a container whose include
// directory holds the toolkit's site file, recorded in the manifest, and a
// file of the host owner's beside it.
func webOn(t *testing.T, w *world, site string) *webservertest.Server {
	t.Helper()
	h := w.hosts[site]
	h.web = webservertest.New("import /etc/caddy/paisans.d/*\n")
	content := "status.example.org {\n\treverse_proxy 127.0.0.1:8480\n}\n"
	h.web.Files[siteFile] = content
	h.web.Files[webDir+"paisans.d/blog.caddy"] = "blog.example.org {\n}\n"
	var m render.Manifest
	if err := json.Unmarshal([]byte(h.files[dep.Manifest()]), &m); err != nil {
		t.Fatal(err)
	}
	m.WebServer = []render.ManifestWebServer{{App: "status", Path: siteFile, SHA256: sum(content), Server: "caddy", Container: "caddy-1", Config: "/etc/caddy/Caddyfile"}}
	data, _ := json.Marshal(m)
	h.files[dep.Manifest()] = string(data)
	return h.web
}

// Removing a site, here an abandoned one with --force, removes the site
// file apply wrote for the web server on its host, and only that file:
// the directory and the host owner's file stay, and the server is
// validated and reloaded inside its container, before the containers go.
func TestSiteRemoveDeletesItsSiteFileOnly(t *testing.T) {
	w := setup(t)
	web := webOn(t, w, "watch")
	p := forced(t, w, w.cfg, "watch", w.cfg.Sites["watch"].Destination(), siteremove.Options{})
	if !hasStepIn(stageNamed(p, "clean the host"), "watch", "remove", siteFile) {
		t.Fatalf("the plan does not remove the site file:\n%s", printed(p))
	}
	if err := siteremove.Execute(p); err != nil {
		t.Fatal(err)
	}
	if _, ok := web.Files[siteFile]; ok {
		t.Error("the site file stayed")
	}
	if _, ok := web.Files[webDir+"paisans.d/blog.caddy"]; !ok {
		t.Error("the host owner's file went")
	}
	if !web.Ran("caddy validate") || !web.Ran("caddy reload") {
		t.Errorf("asked %v", web.Asked)
	}
	for _, c := range web.Asked {
		if strings.Contains(c, "rmdir") || strings.Contains(c, "rm -r") {
			t.Errorf("removed the directory: %s", c)
		}
	}
	order := strings.Join(w.hosts["watch"].commands, "\n")
	if i, j := strings.Index(order, "rm -f -- '"+siteFile), strings.Index(order, "docker stop"); i < 0 || (j >= 0 && j < i) {
		t.Errorf("the site file did not go first:\n%s", order)
	}
}

// A removal the web server refuses puts the file back and stops the run,
// with the manifest still proving the file this deployment's, so a run
// again resumes.
func TestSiteRemoveStopsWhenTheWebServerRefuses(t *testing.T) {
	w := setup(t)
	web := webOn(t, w, "watch")
	web.Validate = "Error: adapting config using caddyfile: File to import not found: /etc/caddy/paisans.d/paisans-f2a9-status.caddy"
	err := siteremove.Execute(forced(t, w, w.cfg, "watch", w.cfg.Sites["watch"].Destination(), siteremove.Options{}))
	if err == nil || !strings.Contains(err.Error(), "File to import not found") {
		t.Fatalf("err %v", err)
	}
	if _, ok := web.Files[siteFile]; !ok {
		t.Error("the refused removal was not put back")
	}
	if _, ok := w.hosts["watch"].files[dep.Manifest()]; !ok {
		t.Error("the manifest went")
	}
	web.Validate = ""
	if err := siteremove.Execute(forced(t, w, w.cfg, "watch", w.cfg.Sites["watch"].Destination(), siteremove.Options{})); err != nil {
		t.Fatal(err)
	}
	if _, ok := web.Files[siteFile]; ok {
		t.Error("the run again did not remove it")
	}
}

// --host-gone reaches nothing, and says the web server there still
// includes the site files.
func TestHostGoneSaysWhatTheWebServerStillIncludes(t *testing.T) {
	w := setup(t)
	watch := w.cfg.Sites["watch"]
	watch.WebServer = &config.WebServer{ManagedBy: config.ManagedByExternal, Listen: "127.0.0.1:8480", IncludeDir: config.IncludeDir{Path: "/opt/caddy/paisans.d"}}
	w.cfg.Sites["watch"] = watch
	// Its app was removed first, as app remove does, while the host was
	// still there.
	delete(w.cfg.Apps, "status")
	p, err := w.build("watch", siteremove.Options{HostGone: true})
	if err != nil {
		t.Fatal(err)
	}
	left := strings.Join(p.Remains(), "\n")
	if !strings.Contains(left, "site files named paisans-f2a9-* in /opt/caddy/paisans.d, which apply wrote") {
		t.Errorf("remains:\n%s", left)
	}
}
