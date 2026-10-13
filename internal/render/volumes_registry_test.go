//go:build registry_integration

package render_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/imagedigest"
	"github.com/paisans-software/paisans-stack/internal/kinds"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// Every image the toolkit renders by default has its declared volumes mounted
// at exactly those paths, for every Postgres major it knows and with every app
// pinned as well as clustered, so each kind's own database is rendered too.
// Each track is resolved to the build apply would run today, and what it
// declares is read from the registry for every platform it is published for.
// `paisans apply` makes the same check against the host's copy of every
// image before it writes anything; this one says so before a release.
//
//	go test -tags registry_integration -run TestEveryDeclaredImageVolumeIsMounted ./internal/render/
func TestEveryDeclaredImageVolumeIsMounted(t *testing.T) {
	registry := imagedigest.Cached(imagedigest.Registry{})
	for _, major := range []string{"16", "17", "18"} {
		for _, pinned := range []bool{false, true} {
			cfg := fixture(t)
			cfg.Cluster.PostgresVersion = major
			for name, app := range cfg.Apps {
				app.Images = nil
				if pinned && app.Placement.Mode != config.PlacementPinned {
					app.Placement = config.Placement{Mode: config.PlacementPinned, Site: "home-a", Literal: "home-a"}
				}
				cfg.Apps[name] = app
			}
			plan, err := render.Build(cfg, fixtureSecrets(t))
			if err != nil {
				t.Fatalf("postgres %s, pinned %v: %v", major, pinned, err)
			}
			for path, content := range planFiles(plan) {
				if !strings.HasSuffix(path, "/compose.yaml") {
					continue
				}
				services, err := kinds.ComposeMounts(content)
				if err != nil {
					t.Fatalf("%s: %v", path, err)
				}
				for name, s := range services {
					for _, declared := range declaredVolumes(t, registry, s.Image) {
						if u := kinds.Uncovered(declared, s.Targets); len(u) > 0 {
							t.Errorf("postgres %s, pinned %v: %s service %s runs %s, which declares %v, and nothing is mounted at exactly that path, so every container would get an anonymous volume there", major, pinned, path, name, s.Image, u)
						}
					}
				}
			}
		}
	}
}

// read is what declaredVolumes has read, by reference, since every render
// names most images again and Docker Hub limits anonymous reads.
var read = map[string][][]string{}

// declaredVolumes is what each platform's build of ref declares, a track
// resolved to its newest tag first.
func declaredVolumes(t *testing.T, r *imagedigest.Cache, ref string) [][]string {
	t.Helper()
	if got, ok := read[ref]; ok {
		return got
	}
	orig := ref
	defer func() { read[orig] = read[ref] }()
	if imagedigest.IsTrack(ref) {
		tagged, err := r.Track(ref)
		if err != nil {
			t.Fatalf("resolving %s: %v", ref, err)
		}
		ref = tagged
	}
	builds, err := imagedigest.Registry{}.Builds(ref)
	if err != nil {
		t.Fatalf("reading %s: %v", ref, err)
	}
	var out [][]string
	for _, b := range builds {
		out = append(out, b.Volumes)
	}
	read[ref] = out
	return out
}
