package siteremove_test

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/siteremove"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// toolkitForms builds the removal of home-b in each form that cleans its
// host: from paisans-stack.yaml, with --force, and with --force --id.
func toolkitForms(w *world) map[string]func() (*siteremove.Plan, error) {
	dest, _ := config.ParseDestination("ubuntu@192.0.2.30")
	return map[string]func() (*siteremove.Plan, error){
		"config": func() (*siteremove.Plan, error) { return w.build("home-b", w.options()) },
		"force": func() (*siteremove.Plan, error) {
			return siteremove.BuildForced(w.cfg, w.secrets, "home-b", dest, w.hosts["home-b"], siteremove.Options{})
		},
		"force --id": func() (*siteremove.Plan, error) {
			return siteremove.BuildForcedByID(dest, w.hosts["home-b"], "f2a9", siteremove.Options{})
		},
	}
}

// A site a newer paisans applied is refused in every form before anything
// on the host is changed: this binary decides what is provably the
// deployment's, and the newer one may have put things there it does not
// know about.
func TestRefusesASiteANewerToolkitApplied(t *testing.T) {
	for _, recorded := range []string{"99.0.0", "99.0.0-dev", "99.0.0-rc.1", "next"} {
		w := setup(t)
		h := w.hosts["home-b"]
		h.setToolkitVersion(t, recorded)
		before := maps.Clone(h.files)
		for name, build := range toolkitForms(w) {
			_, err := build()
			if err == nil {
				t.Errorf("%s, %q: a site this paisans cannot vouch for was planned", name, recorded)
				continue
			}
			if !strings.Contains(err.Error(), recorded) || !strings.Contains(err.Error(), "Nothing was changed") || !strings.Contains(err.Error(), "Upgrade paisans") {
				t.Errorf("%s, %q: %v", name, recorded, err)
			}
		}
		if !maps.Equal(before, h.files) {
			t.Errorf("%q: the host changed", recorded)
		}
	}
	w := setup(t)
	w.hosts["home-b"].setToolkitVersion(t, "99.0.0")
	_, err := w.build("home-b", w.options())
	if want := "was last applied here by paisans 99.0.0, and this is paisans " + version.Version; err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "Upgrade paisans to 99.0.0 or later") {
		t.Errorf("the refusal does not say which releases: %v", err)
	}
}

// A site this release or an older one applied is removed as before.
func TestProceedsOnASiteThisOrAnOlderToolkitApplied(t *testing.T) {
	for _, recorded := range []string{version.Version, "0.0.1"} {
		w := setup(t)
		w.hosts["home-b"].setToolkitVersion(t, recorded)
		for name, build := range toolkitForms(w) {
			if _, err := build(); err != nil {
				t.Errorf("%s, %s: %v", name, recorded, err)
			}
		}
	}
}

// An apply by a newer paisans between the plan and its run is refused when
// the host stage starts, before it changes anything.
func TestRefusesANewerToolkitAppliedSinceThePlan(t *testing.T) {
	w := setup(t)
	p := byID(t, w, "home-b", "f2a9", siteremove.Options{})
	h := w.hosts["home-b"]
	h.setToolkitVersion(t, "99.0.0")
	before := maps.Clone(h.files)
	err := siteremove.Execute(p)
	if err == nil || !strings.Contains(err.Error(), "paisans 99.0.0") {
		t.Fatalf("the run went ahead: %v", err)
	}
	if !maps.Equal(before, h.files) {
		t.Error("the host changed")
	}
}

// A manifest that does not say which build of paisans wrote it is refused
// in every form, with apply named as what writes it anew, before anything
// on the host is changed.
func TestRefusesAManifestWithNoBuild(t *testing.T) {
	w := setup(t)
	h := w.hosts["home-b"]
	var m map[string]any
	if err := json.Unmarshal([]byte(h.files[dep.Manifest()]), &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "toolkit_commit")
	data, _ := json.Marshal(m)
	h.files[dep.Manifest()] = string(data)
	before := maps.Clone(h.files)
	for name, build := range toolkitForms(w) {
		_, err := build()
		if err == nil || !strings.Contains(err.Error(), "toolkit_commit missing") || !strings.Contains(err.Error(), "Apply the site again") || !strings.Contains(err.Error(), "Nothing was changed") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if !maps.Equal(before, h.files) {
		t.Error("the host changed")
	}
}

// A manifest whose version is no version at all does not say which build
// wrote it, and is refused in every form as one without a build is.
func TestRefusesAManifestWhoseVersionCannotBeOrdered(t *testing.T) {
	for _, recorded := range []string{"0.2.0 (519299d5cb00)", ""} {
		w := setup(t)
		h := w.hosts["home-b"]
		h.setToolkitVersion(t, recorded)
		before := maps.Clone(h.files)
		for name, build := range toolkitForms(w) {
			_, err := build()
			if err == nil || !strings.Contains(err.Error(), "is not a version") || !strings.Contains(err.Error(), "Apply the site again") || !strings.Contains(err.Error(), "Nothing was changed") {
				t.Errorf("%s, %q: %v", name, recorded, err)
			}
		}
		if !maps.Equal(before, h.files) {
			t.Errorf("%q: the host changed", recorded)
		}
	}
}

// keptCommit is the commit setToolkitVersion records beside a version.
const keptCommit = "519299d5cb009da4904cdf53c2025f1ad609f102"

// A kept Caddy's manifest keeps the build the manifest recorded, so a
// later run checks it as this one did.
func TestTheKeptCaddysManifestKeepsItsRelease(t *testing.T) {
	for _, recorded := range []string{"0.0.1", version.Version} {
		w := gatewayWorld(t, true)
		vm := w.hosts["vm"]
		vm.setToolkitVersion(t, recorded)
		p := forced(t, w, w.cfg, "vm", vmDest(), siteremove.Options{})
		if len(p.CaddyKept) == 0 {
			t.Fatalf("%q: Caddy is not kept:\n%s", recorded, printed(p))
		}
		if err := siteremove.Execute(p); err != nil {
			t.Fatal(err)
		}
		m, err := render.ParseManifest([]byte(vm.files[dep.Manifest()]))
		if err != nil {
			t.Fatal(err)
		}
		if want := (version.Build{Version: recorded, Commit: keptCommit, Modified: true}); m.Build != want {
			t.Errorf("the kept manifest records %+v, want %+v", m.Build, want)
		}
	}
}
