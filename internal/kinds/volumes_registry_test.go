//go:build registry_integration

package kinds_test

import (
	"path"
	"reflect"
	"sort"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/imagedigest"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// Every recorded image declares, on every platform it is published for,
// exactly the volumes kinds.ImageVolumes or kinds.RepositoryVolumes records
// for it, a repository's entry at its highest version tag. It reads the registry anonymously, with no Docker, so it answers
// for arm64 and amd64 alike from any workstation:
//
//	go test -tags registry_integration -run TestRecordedVolumesMatchEveryPlatform ./internal/kinds/
func TestRecordedVolumesMatchEveryPlatform(t *testing.T) {
	refs := map[string][]string{}
	for ref, paths := range kinds.ImageVolumes {
		refs[ref] = paths
	}
	// A repository's entry is checked at its highest version tag, a specific
	// release, rather than at a moving tag such as the gateway's Caddy `2` or
	// a fork's `stack-1` channel.
	registry := imagedigest.Registry{}
	for repository, paths := range kinds.RepositoryVolumes {
		newest, err := registry.NewestVersion(repository + ":latest")
		if err != nil {
			t.Fatalf("listing %s: %v", repository, err)
		}
		if newest == "" {
			t.Errorf("%s has no version tag to check", repository)
			continue
		}
		refs[repository+":"+newest] = paths
	}
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
