package validate_test

import (
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

// pinBlog pins the valid fixture's WriteFreely app to home-a, with the given
// storage key, so a storage rule can be tested apart from placement.
func pinBlog(t *testing.T, storage string) *config.Config {
	t.Helper()
	cfg := load(t, "valid")
	blog := cfg.Apps["blog"]
	blog.Placement = config.Placement{Mode: config.PlacementPinned, Site: "home-a"}
	blog.Storage = storage
	cfg.Apps["blog"] = blog
	return cfg
}

// A pinned app may keep its files on its own disk, and may keep them in a
// Garage on another site: neither is refused or warned about. Founder
// decision.
func TestPinnedStorageIsQuiet(t *testing.T) {
	for _, storage := range []string{"", config.StorageLocal, config.StorageS3} {
		cfg := pinBlog(t, storage)
		// Garage on home-b, the app on home-a.
		cfg.Storage.Garage.Sites = []string{"home-b"}
		if result := validate.Check(cfg); len(result.Findings) != 0 {
			t.Errorf("storage %q: %v", storage, result.Findings)
		}
	}
}

func TestStorageRefusals(t *testing.T) {
	cases := []struct {
		name string
		edit func(*config.Config)
		rule string
		key  string
	}{
		{"s3 without garage", func(c *config.Config) {
			c.Storage.Garage = config.Garage{}
			blog := c.Apps["blog"]
			blog.Placement = config.Placement{Mode: config.PlacementPinned, Site: "home-a"}
			blog.Storage = config.StorageS3
			c.Apps["blog"] = blog
		}, "storage-s3-without-garage", "apps.blog.storage"},
		// With no Garage the default is local, which a clustered app cannot
		// have either, so the refusal names the placement.
		{"cluster without garage", func(c *config.Config) {
			c.Storage.Garage = config.Garage{}
		}, "storage-local-on-cluster", "apps.blog.placement"},
		{"media hostname on a local app", func(c *config.Config) {
			blog := c.Apps["blog"]
			blog.Placement = config.Placement{Mode: config.PlacementPinned, Site: "home-a"}
			blog.Storage = config.StorageLocal
			blog.Hostnames = map[string]string{"media": "files.example.org"}
			c.Apps["blog"] = blog
		}, "media-hostname-without-object-storage", "apps.blog.hostnames.media"},
		{"a blueprint with no files", func(c *config.Config) {
			c.Apps["web"] = config.App{Blueprint: config.Blueprint("element"), Hostname: "web.example.org",
				Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-a"},
				Storage:   config.StorageLocal, Settings: map[string]any{"homeserver": "chat.example.org"}}
		}, "storage-on-a-blueprint-without-files", "apps.web.storage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := load(t, "valid")
			tc.edit(cfg)
			if result := validate.Check(cfg); !refusedFor(result, tc.rule, tc.key) {
				t.Fatalf("%s did not refuse %s: %v", tc.rule, tc.key, result.Findings)
			}
		})
	}
}

// storage: local on a blueprint that keeps its files only locally is what it
// does anyway, so it is accepted rather than refused as redundant.
func TestLocalStorageOnALocalOnlyBlueprintIsAccepted(t *testing.T) {
	cfg := load(t, "valid")
	health := cfg.Apps["health"]
	health.Storage = config.StorageLocal
	cfg.Apps["health"] = health
	if result := validate.Check(cfg); len(result.Findings) != 0 {
		t.Errorf("%v", result.Findings)
	}
}

// An unknown value is refused when the file is loaded, before any rule.
func TestAnUnknownStorageValueIsRefusedAtLoad(t *testing.T) {
	if _, err := config.Load("testdata/storage-unknown-value.yaml"); err == nil {
		t.Fatal("storage: disk loaded")
	}
}
