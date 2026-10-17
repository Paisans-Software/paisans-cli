package doctor

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/version"
)

// The leftovers check names the build of paisans that last applied the
// site, and fails on a manifest that does not say, with the refusal.
func TestLeftoversNameTheManifestsBuild(t *testing.T) {
	b := version.Build{Version: "0.3.0-dev", Commit: "519299d5cb009da4904cdf53c2025f1ad609f102", Modified: true}
	fs := Leftovers([]LeftoverProbe{{Site: "home-a", Manifest: true, ManifestBuild: b}})
	if len(fs) == 0 || fs[0].Level != Info || fs[0].Line != "home-a: last applied by paisans 0.3.0-dev (519299d5cb00-dirty)" {
		t.Errorf("got:\n%s", dump(fs))
	}
	refusal := "/srv/paisans/f2a9/.paisans-manifest.json: it does not say which build of paisans wrote it (toolkit_commit missing). Apply the site again, which writes it anew"
	fs = Leftovers([]LeftoverProbe{{Site: "home-a", Manifest: true, ManifestErr: refusal}})
	if len(fs) == 0 || fs[0].Level != Fail || !strings.Contains(strings.Join(fs[0].More, "\n"), "Apply the site again") {
		t.Errorf("got:\n%s", dump(fs))
	}
	if fs := Leftovers([]LeftoverProbe{{Site: "home-a"}}); len(fs) != 1 || fs[0].Level != OK {
		t.Errorf("a site with no manifest:\n%s", dump(fs))
	}
}
