package storageadd

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// An app keeping its files on its own disk has no bucket, so storage add
// neither counts one across the reset nor puts its probe through one.
func TestALocalStorageAppHasNoBucketToCountOrProbe(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets(filepath.Join("..", "render", "testdata", "secrets.fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"talk", "blog"} {
		app := cfg.Apps[name]
		app.Placement = config.Placement{Mode: config.PlacementPinned, Site: "home-a"}
		app.Storage = config.StorageLocal
		cfg.Apps[name] = app
	}
	p := &Plan{cfg: cfg, secrets: secrets}

	if got := p.buckets(); !slices.Equal(got, []string{"docs-uploads"}) {
		t.Errorf("buckets = %v, want only docs-uploads", got)
	}
	// talk and blog are the two apps whose objects are public; with both
	// local there is nothing public to probe through, and Outline's private
	// bucket is never used for it.
	if pr := p.publicProbe(); pr != nil {
		t.Errorf("probed %s's bucket %s, which keeps its files locally or is private", pr.app, pr.bucket)
	}
}
