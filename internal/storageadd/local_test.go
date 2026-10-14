package storageadd

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// localPlan is a plan over the render fixture with the named apps pinned to
// home-a and keeping their files there.
func localPlan(t *testing.T, local ...string) *Plan {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets(filepath.Join("..", "render", "testdata", "secrets.fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range local {
		app := cfg.Apps[name]
		app.Placement = config.Placement{Mode: config.PlacementPinned, Site: "home-a"}
		app.Storage = config.StorageLocal
		cfg.Apps[name] = app
	}
	return &Plan{cfg: cfg, secrets: secrets}
}

// An app keeping its files on its own disk has no bucket, so storage add
// neither counts one across the reset nor puts its probe through one. The
// blog, still in Garage, is counted and probed through in its place.
func TestALocalStorageAppHasNoBucketToCountOrProbe(t *testing.T) {
	p := localPlan(t, "talk")
	if got := p.buckets(); !slices.Equal(got, []string{"blog-uploads"}) {
		t.Errorf("buckets = %v, want only blog-uploads", got)
	}
	if pr := p.publicProbe(); pr == nil || pr.app != "blog" || pr.bucket != "blog-uploads" {
		t.Errorf("probe = %+v, want one through the blog's bucket", pr)
	}
}

// With talk and blog both local, no app in the fixture keeps objects in
// Garage, since the wiki stores none, so there is nothing to count and
// nothing public to probe through.
func TestNoBucketMeansNothingToCountOrProbe(t *testing.T) {
	p := localPlan(t, "talk", "blog")
	if got := p.buckets(); len(got) != 0 {
		t.Errorf("buckets = %v, want none", got)
	}
	if pr := p.publicProbe(); pr != nil {
		t.Errorf("probed %s's bucket %s, which keeps its files locally", pr.app, pr.bucket)
	}
}
