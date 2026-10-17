package siteremove_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/siteremove"
)

// drift is every file h's manifest records with a checksum other than the
// file's on the host: what the next apply takes for a hand edit.
func (h *host) drift(t *testing.T) []string {
	t.Helper()
	var m render.Manifest
	if err := json.Unmarshal([]byte(h.files[dep.Manifest()]), &m); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range m.Files {
		content, ok := h.files["/"+f.Path]
		if ok && hexSum(content) != f.SHA256 {
			out = append(out, f.Path)
		}
	}
	return out
}

// remaining is every site the configuration declares once the removal is
// done.
func (w *world) remaining(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(w.configPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// meshCheck is the check site add makes on every existing site before it
// writes the mesh file with a new peer: the file scoped on its own, against
// a render that differs from it, refused when the host's copy is not what
// the manifest recorded.
func (w *world) meshCheck(t *testing.T, cfg *config.Config) map[string][]string {
	t.Helper()
	rendered, err := render.Build(cfg, w.secrets)
	if err != nil {
		t.Fatal(err)
	}
	for i := range rendered.Files {
		if strings.HasSuffix(rendered.Files[i].Path, "/"+dep.WireGuardConf()) {
			rendered.Files[i].Content += "# a new peer\n"
		}
	}
	out := map[string][]string{}
	for _, name := range cfg.SiteNames() {
		mp, err := apply.Build(name, rendered, "", w.hosts[name], apply.Scope(dep.WireGuardConf()))
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range mp.Conflicts() {
			out[name] = append(out[name], c.Path)
		}
	}
	return out
}

// Every file a removal with --host-gone writes on a remaining site is in
// that site's manifest as written, the mesh file its cut writes among them,
// so the site add that follows finds no edit on any host.
func TestACutoffRecordsWhatItWrites(t *testing.T) {
	w, zone := cutoffWorld(t, "home-b")
	execute(t, w.mustBuild("home-b", cutoffOptions(zone)))
	cfg := w.remaining(t)
	for _, name := range cfg.SiteNames() {
		if d := w.hosts[name].drift(t); len(d) > 0 {
			t.Errorf("%s's manifest does not record what is on the host for %s", name, strings.Join(d, ", "))
		}
	}
	if c := w.meshCheck(t, cfg); len(c) > 0 {
		t.Errorf("site add would refuse files site remove wrote as hand edits: %v", c)
	}
}

// A removal without --host-gone records what it writes on a remaining site
// as well.
func TestARemovalRecordsWhatItWrites(t *testing.T) {
	w := setup(t)
	execute(t, w.mustBuild("home-b", siteremove.Options{}))
	cfg := w.remaining(t)
	for _, name := range cfg.SiteNames() {
		if d := w.hosts[name].drift(t); len(d) > 0 {
			t.Errorf("%s's manifest does not record what is on the host for %s", name, strings.Join(d, ", "))
		}
	}
	if c := w.meshCheck(t, cfg); len(c) > 0 {
		t.Errorf("site add would refuse files site remove wrote as hand edits: %v", c)
	}
}

// A hand edit of the mesh file made after the removal is still one.
func TestAHandEditAfterACutoffIsStillRefused(t *testing.T) {
	w, zone := cutoffWorld(t, "home-b")
	execute(t, w.mustBuild("home-b", cutoffOptions(zone)))
	w.hosts["vm"].files[wgConf] += "# a hand edit\n"
	c := w.meshCheck(t, w.remaining(t))
	if len(c["vm"]) != 1 || c["vm"][0] != wgConf {
		t.Errorf("site add does not refuse vm's hand edited mesh file: %v", c)
	}
	for name, paths := range c {
		if name != "vm" {
			t.Errorf("site add refuses %s's %v, which nobody edited", name, paths)
		}
	}
}
