package validate

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
)

func TestChannelOf(t *testing.T) {
	for ref, want := range map[string]string{
		"ghcr.io/paisans-software/mbin:stack-1":                  "stack-1",
		"ghcr.io/paisans-software/mbin:stack-12@sha256:0123abcd": "stack-12",
		"ghcr.io/paisans-software/mbin:1.15.0-paisans":           "",
		"ghcr.io/paisans-software/mbin:stack-one":                "",
		"ghcr.io/paisans-software/mbin@sha256:0123abcd":          "",
		"localhost:5000/mbin":                                    "",
	} {
		if got := channelOf(ref); got != want {
			t.Errorf("channelOf(%s) = %q, want %q", ref, got, want)
		}
	}
}

// The rule works off whatever the kind's default is: it warns when an app's
// image names a channel other than the default's, and only then.
func TestImageChannelDiffers(t *testing.T) {
	saved := kindDefault
	t.Cleanup(func() { kindDefault = saved })
	cases := []struct {
		name, def, image string
		warn             bool
	}{
		{"another channel", "ghcr.io/paisans-software/mbin:stack-1", "ghcr.io/paisans-software/mbin:stack-2", true},
		{"another channel by digest", "ghcr.io/paisans-software/mbin:stack-1", "ghcr.io/paisans-software/mbin:stack-2@sha256:0c5a1ad0b7e4b2c6f0d3d0e5a2b9e8f7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1", true},
		{"the default's channel", "ghcr.io/paisans-software/mbin:stack-1", "ghcr.io/example-org/mbin:stack-1", false},
		{"a version tag", "ghcr.io/paisans-software/mbin:stack-1", "ghcr.io/paisans-software/mbin:1.15.0-paisans", false},
		{"a default on no channel", "ghcr.io/paisans-software/mbin:1.14.0-paisans", "ghcr.io/paisans-software/mbin:stack-2", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kindDefault = func(kind config.Kind, service string) string {
				if kind == config.KindMbin && service == "app" {
					return tc.def
				}
				return saved(kind, service)
			}
			cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			talk := cfg.Apps["talk"]
			talk.Images = map[string]string{"app": tc.image}
			cfg.Apps["talk"] = talk
			var found *Finding
			for _, f := range Check(cfg).Findings {
				if f.Rule == "image-channel-differs" {
					found = &f
				}
			}
			if (found != nil) != tc.warn {
				t.Fatalf("warned %+v, want a warning %t", found, tc.warn)
			}
			if found == nil {
				return
			}
			if found.Level != Warn || found.Key != "apps.talk.images.app" {
				t.Errorf("found %+v", found)
			}
			for _, want := range []string{"stack-1", "stack-2", tc.def} {
				if !strings.Contains(found.Message, want) {
					t.Errorf("the warning does not name %s: %s", want, found.Message)
				}
			}
		})
	}
}
