package blueprints_test

import (
	"slices"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// The storage key decides per app, and an app that leaves it out takes S3
// only where there is a Garage to hold it. Founder decision.
func TestInObjectStorageDefaultsToGarageWhereThereIsOne(t *testing.T) {
	withGarage := &config.Config{}
	withGarage.Storage.Garage.Sites = []string{"home-a"}
	without := &config.Config{}
	for _, tc := range []struct {
		name string
		cfg  *config.Config
		app  config.App
		want bool
	}{
		{"default with garage", withGarage, config.App{Kind: config.Kind("mbin")}, true},
		{"default without garage", without, config.App{Kind: config.Kind("mbin")}, false},
		{"local with garage", withGarage, config.App{Kind: config.Kind("writefreely"), Storage: config.StorageLocal}, false},
		{"s3 says so", withGarage, config.App{Kind: config.Kind("writefreely"), Storage: config.StorageS3}, true},
		// No S3 form at all, whatever the key says; validate refuses the s3.
		{"synapse", withGarage, config.App{Kind: config.Kind("synapse"), Storage: config.StorageS3}, false},
		{"uptime", withGarage, config.App{Kind: config.KindUptime}, false},
		{"pocket-id", withGarage, config.App{Kind: config.KindPocketID}, false},
	} {
		if got := blueprints.InObjectStorage(tc.cfg, tc.app); got != tc.want {
			t.Errorf("%s: InObjectStorage = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A gated app keeping public files locally opens exactly the path they are
// served on, and nothing when they are in Garage or private.
func TestLocalMediaPaths(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.Garage.Sites = []string{"home-a"}
	if got := blueprints.LocalMediaPaths(cfg, config.App{Kind: config.Kind("mbin"), Storage: config.StorageLocal}); !slices.Equal(got, []string{"/media/*"}) {
		t.Errorf("mbin local: %v", got)
	}
	if got := blueprints.LocalMediaPaths(cfg, config.App{Kind: config.Kind("writefreely"), Storage: config.StorageLocal}); !slices.Equal(got, []string{"/uploads/*"}) {
		t.Errorf("writefreely local: %v", got)
	}
	if got := blueprints.LocalMediaPaths(cfg, config.App{Kind: config.Kind("mbin")}); got != nil {
		t.Errorf("mbin in garage: %v", got)
	}
	// The paths are only what a blueprint declares under local_media_paths,
	// so a kind declaring none opens none. Wiki.js declares none because it
	// keeps no files at all; this case proves a kind without declared paths
	// gets an empty list, not anything about privacy.
	if got := blueprints.LocalMediaPaths(cfg, config.App{Kind: config.Kind("wikijs"), Storage: config.StorageLocal}); len(got) != 0 {
		t.Errorf("wikijs keeps no files: %v", got)
	}
}
