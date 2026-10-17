package siteremove_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/acme"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/siteremove"
)

// meshRel is the fixture's mesh file, relative to the host's root.
var meshRel = strings.TrimPrefix(wgConf, "/")

// A resumed sync hands the helper the mesh file of the site's own scoped
// apply when that apply holds it, without building another plan.
func TestAResumedSyncReusesThePlanThatHoldsTheMeshFile(t *testing.T) {
	w, zone := cutoffWorld(t, "home-b")
	p := w.mustBuild("home-b", cutoffOptions(zone))
	held, err := apply.Build("home-a", p.Rendered(), acme.Module("cloudflare"), w.hosts["home-a"], apply.Scope(meshRel))
	if err != nil {
		t.Fatal(err)
	}
	got, err := siteremove.MeshPlan("home-b", "home-a", held, []string{meshRel}, meshRel, func() (*apply.Plan, error) {
		t.Error("a plan was built although the site's own plan holds the mesh file")
		return nil, nil
	})
	if err != nil || got != held {
		t.Errorf("got %p, %v; want the held plan %p", got, err, held)
	}
}

// A plan built for the mesh file alone is refused as the site's own plan
// is when somebody edited the file on the host: the helper would install
// the render while the edit stayed in /srv.
func TestAResumedSyncRefusesAnEditedMeshFile(t *testing.T) {
	w, zone := cutoffWorld(t, "home-b")
	p := w.mustBuild("home-b", cutoffOptions(zone))
	build := func() (*apply.Plan, error) {
		return apply.Build("home-a", p.Rendered(), acme.Module("cloudflare"), w.hosts["home-a"], apply.Scope(meshRel))
	}
	clean, err := siteremove.MeshPlan("home-b", "home-a", nil, nil, meshRel, build)
	if err != nil || clean == nil {
		t.Fatalf("an unedited mesh file: %v", err)
	}
	w.hosts["home-a"].files[wgConf] += "# edited by hand\n"
	_, err = siteremove.MeshPlan("home-b", "home-a", nil, nil, meshRel, build)
	if err == nil || !strings.Contains(err.Error(), "somebody edited it on the host") || !strings.Contains(err.Error(), wgConf) {
		t.Errorf("an edited mesh file: %v", err)
	}
}
