package imagedigest_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/imagedigest"
)

// Builds names each platform with what its configuration declares, read by
// tag as well as by digest, so a table keyed by tag can be checked against
// every platform the tag names.
func TestBuildsReadEachPlatformsVolumes(t *testing.T) {
	srv, _ := published(t,
		build{arch: "arm64", volumes: `"/app/var/":{}`},
		build{arch: "amd64", volumes: `"/data":{},"/app/var/":{}`})
	for _, ref := range []string{mbinRef(srv), strings.TrimPrefix(srv.URL, "https://") + "/paisans-software/mbin:1.15.1-paisans"} {
		got, err := imagedigest.Registry{Client: srv.Client()}.Builds(ref)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		want := map[string][]string{"arm64": {"/app/var/"}, "amd64": {"/app/var/", "/data"}}
		seen := map[string][]string{}
		for _, b := range got {
			seen[b.Platform] = b.Volumes
		}
		if !reflect.DeepEqual(seen, want) {
			t.Errorf("%s: got %v, want %v", ref, seen, want)
		}
	}
}
