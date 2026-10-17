package render_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// A manifest records the build of the paisans that wrote it, and reads it
// back.
func TestAManifestRoundTripsItsBuild(t *testing.T) {
	want := version.Build{Version: "0.3.0-dev", Commit: "519299d5cb009da4904cdf53c2025f1ad609f102", Modified: true}
	data, err := json.Marshal(render.Manifest{Version: 1, Build: want})
	if err != nil {
		t.Fatal(err)
	}
	m, err := render.ParseManifest(data)
	if err != nil || m.Build != want {
		t.Errorf("got %+v, %v", m.Build, err)
	}
}

// A manifest without every one of the build's keys is refused, and read
// all the same for apply, which writes it anew.
func TestAManifestWithNoBuildIsRefused(t *testing.T) {
	m, err := render.ParseManifest([]byte(`{"version":1,"files":[{"path":"a","sha256":"x","mode":"0644"}]}`))
	var noBuild *version.NoBuildError
	if !errors.As(err, &noBuild) || len(noBuild.Missing) != 3 {
		t.Fatalf("got %v", err)
	}
	if len(m.Files) != 1 {
		t.Errorf("files %v", m.Files)
	}
}

// A manifest whose build holds a value paisans never records is refused
// as one without a build is.
func TestAManifestWithABadBuildIsRefused(t *testing.T) {
	for _, bad := range []string{
		`{"version":1,"files":[],"toolkit_version":"","toolkit_commit":"v0.3.0","toolkit_modified":false}`,
		`{"version":1,"files":[],"toolkit_version":"0.3.0","toolkit_commit":"","toolkit_modified":false}`,
		`{"version":1,"files":[],"toolkit_version":"0.3.0","toolkit_commit":"v0.3.0","toolkit_modified":"no"}`,
	} {
		var noBuild *version.NoBuildError
		if _, err := render.ParseManifest([]byte(bad)); !errors.As(err, &noBuild) {
			t.Errorf("%s: got %v", bad, err)
		}
	}
}

// A manifest a release candidate built from its tarball wrote is read.
func TestAManifestOfAReleaseCandidateIsRead(t *testing.T) {
	m, err := render.ParseManifest([]byte(`{"version":1,"files":[],"toolkit_version":"0.3.0-rc.1","toolkit_commit":"v0.3.0-rc.1","toolkit_modified":false}`))
	if err != nil || m.Build != (version.Build{Version: "0.3.0-rc.1", Commit: "v0.3.0-rc.1"}) {
		t.Errorf("got %+v, %v", m.Build, err)
	}
}
