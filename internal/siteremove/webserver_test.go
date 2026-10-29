package siteremove_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/siteremove"
)

const referenceFile = "/srv/paisans/f2a9/web-server/health.caddy"

// referenceOn records in the site's manifest the reference file apply wrote
// in the deployment's root for the host owner's web server.
func referenceOn(t *testing.T, w *world, site string) *host {
	t.Helper()
	h := w.hosts[site]
	content := "health.example.org {\n\treverse_proxy 127.0.0.1:8480\n}\n"
	h.files[referenceFile] = content
	var m render.Manifest
	if err := json.Unmarshal([]byte(h.files[dep.Manifest()]), &m); err != nil {
		t.Fatal(err)
	}
	m.WebServer = []render.ManifestWebServer{{App: "health", Hostname: "health.example.org", Path: referenceFile, SHA256: sum(content), Server: "caddy", Config: "/etc/caddy/Caddyfile"}}
	data, _ := json.Marshal(m)
	h.files[dep.Manifest()] = string(data)
	return h
}

// site remove deletes the reference file, its own, and says which block
// the host's owner removes from their web server, which it never touches.
func TestSiteRemoveNamesTheBlockTheOwnerRemoves(t *testing.T) {
	w := setup(t)
	h := referenceOn(t, w, "watch")
	p := forced(t, w, w.cfg, "watch", w.cfg.Sites["watch"].Destination(), siteremove.Options{})
	if !hasStepIn(stageNamed(p, "clean the host"), "watch", "remove", referenceFile) {
		t.Fatalf("the plan does not remove the reference file:\n%s", printed(p))
	}
	if err := siteremove.Execute(p); err != nil {
		t.Fatal(err)
	}
	ran := strings.Join(h.commands, "\n")
	if !strings.Contains(ran, "rm -f -- '"+referenceFile+"'") {
		t.Errorf("the reference file was not removed:\n%s", ran)
	}
	if !strings.Contains(ran, "-maxdepth 0 -type d -empty -delete") || !strings.Contains(ran, "'/srv/paisans/f2a9/web-server'") {
		t.Errorf("the reference directory was not removed once empty:\n%s", ran)
	}
	for _, c := range h.commands {
		for _, server := range []string{"caddy reload", "caddy validate", "nginx -", "apachectl", "docker exec", "systemctl reload", "/etc/caddy"} {
			if strings.Contains(c, server) {
				t.Errorf("ran against the web server: %s", c)
			}
		}
	}
	if !strings.Contains(strings.Join(p.Kept, "\n"), "the host's owner removes the block for health.example.org") {
		t.Errorf("kept %q", p.Kept)
	}
}

// A web server entry that is not a reference file of this deployment's, by
// a path with .., or a clean one outside the reference directory, is never
// read or deleted: the manifest is the deployment user's own file. It is
// kept and reported, and the reference file beside it still goes.
func TestAForeignReferenceEntryIsKept(t *testing.T) {
	for _, bad := range []string{"/srv/paisans/f2a9/web-server/../../../etc/passwd", "/etc/passwd", "/srv/paisans/f2a9/health/compose.yaml"} {
		w := setup(t)
		h := referenceOn(t, w, "watch")
		var m render.Manifest
		if err := json.Unmarshal([]byte(h.files[dep.Manifest()]), &m); err != nil {
			t.Fatal(err)
		}
		m.WebServer = append(m.WebServer, render.ManifestWebServer{App: "health", Path: bad, SHA256: sum("x"), Server: "caddy", Config: "/etc/caddy/Caddyfile"})
		data, _ := json.Marshal(m)
		h.files[dep.Manifest()] = string(data)
		p := forced(t, w, w.cfg, "watch", w.cfg.Sites["watch"].Destination(), siteremove.Options{})
		if hasStepIn(stageNamed(p, "clean the host"), "watch", "remove", bad) {
			t.Errorf("the plan removes %s:\n%s", bad, printed(p))
		}
		if !strings.Contains(strings.Join(p.Kept, "\n"), bad+": not a reference file of this deployment's") {
			t.Errorf("%s: kept %q", bad, p.Kept)
		}
		if err := siteremove.Execute(p); err != nil {
			t.Fatal(err)
		}
		for _, c := range h.commands {
			if strings.Contains(c, "rm -f -- '"+bad+"'") || strings.Contains(c, "../") {
				t.Errorf("ran %s", c)
			}
		}
		if _, ok := h.files[referenceFile]; ok {
			t.Error("the reference file stayed")
		}
	}
}

// --host-gone reaches nothing, and says the owner's web server may still
// carry the blocks of this deployment's apps.
func TestHostGoneSaysWhatTheOwnersWebServerStillCarries(t *testing.T) {
	w := setup(t)
	watch := w.cfg.Sites["watch"]
	watch.WebServer = &config.WebServer{ManagedBy: config.ManagedByExternal, Listen: "127.0.0.1:8480"}
	w.cfg.Sites["watch"] = watch
	// Its apps were removed first, as app remove does, while the host was
	// still there.
	delete(w.cfg.Apps, "health")
	delete(w.cfg.Apps, "stats")
	delete(w.cfg.Apps, "notify") // it serves the hub
	p, err := w.build("watch", siteremove.Options{HostGone: true})
	if err != nil {
		t.Fatal(err)
	}
	left := strings.Join(p.Remains(), "\n")
	if !strings.Contains(left, "the host's owner removes the blocks apply's reference files in /srv/paisans/f2a9/web-server held") {
		t.Errorf("remains:\n%s", left)
	}
}
