//go:build registry_integration

package kinds_test

import (
	"path"
	"reflect"
	"sort"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/acme"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/imagedigest"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// Every recorded image declares, on every platform it is published for,
// exactly the volumes kinds.ImageVolumes or kinds.RepositoryVolumes records
// for it. It reads the registry anonymously, with no Docker, so it answers
// for arm64 and amd64 alike from any workstation:
//
//	go test -tags registry_integration -run TestRecordedVolumesMatchEveryPlatform ./internal/kinds/
func TestRecordedVolumesMatchEveryPlatform(t *testing.T) {
	refs := map[string][]string{}
	for ref, paths := range kinds.ImageVolumes {
		refs[ref] = paths
	}
	// A repository's entry is checked through the references the toolkit
	// ships for it: the gateway's Caddy tag, and each kind's channel tag.
	refs[acme.Published] = kinds.RepositoryVolumes[acme.Repository]
	for _, kind := range []config.Kind{config.KindMbin, config.KindOutline, config.KindPocketID, config.KindSynapse, config.KindElement, config.KindWriteFreely, config.KindOAuth2Proxy, config.KindUptime, config.KindInvite} {
		for _, service := range kinds.Services(kind) {
			if paths, ok := kinds.RepositoryVolumes[kinds.ParseReference(service.Image).Repository()]; ok && service.Image != "" {
				refs[service.Image] = paths
			}
		}
	}
	registry := imagedigest.Registry{}
	for ref, recorded := range refs {
		t.Run(ref, func(t *testing.T) {
			t.Parallel()
			builds, err := registry.Builds(ref)
			if err != nil {
				t.Fatal(err)
			}
			if len(builds) == 0 {
				t.Fatalf("%s lists no linux platform", ref)
			}
			want := cleaned(recorded)
			for _, b := range builds {
				if got := cleaned(b.Volumes); !reflect.DeepEqual(got, want) {
					t.Errorf("%s for %s declares %v; recorded %v", ref, b.Platform, got, want)
				}
			}
		})
	}
}

// cleaned is paths as Docker compares them: cleaned, sorted, and nil when
// there are none.
func cleaned(paths []string) []string {
	var out []string
	for _, p := range paths {
		out = append(out, path.Clean(p))
	}
	sort.Strings(out)
	return out
}
