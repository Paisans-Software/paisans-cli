package kinds_test

import (
	"slices"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
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
		{"default with garage", withGarage, config.App{Kind: config.KindMbin}, true},
		{"default without garage", without, config.App{Kind: config.KindMbin}, false},
		{"local with garage", withGarage, config.App{Kind: config.KindOutline, Storage: config.StorageLocal}, false},
		{"s3 says so", withGarage, config.App{Kind: config.KindWriteFreely, Storage: config.StorageS3}, true},
		// No S3 form at all, whatever the key says; validate refuses the s3.
		{"synapse", withGarage, config.App{Kind: config.KindSynapse, Storage: config.StorageS3}, false},
		{"uptime", withGarage, config.App{Kind: config.KindUptime}, false},
		{"pocket-id", withGarage, config.App{Kind: config.KindPocketID}, false},
	} {
		if got := kinds.InObjectStorage(tc.cfg, tc.app); got != tc.want {
			t.Errorf("%s: InObjectStorage = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A gated app keeping public files locally opens exactly the path they are
// served on, and nothing when they are in Garage or private.
func TestLocalMediaPaths(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.Garage.Sites = []string{"home-a"}
	if got := kinds.LocalMediaPaths(cfg, config.App{Kind: config.KindMbin, Storage: config.StorageLocal}); !slices.Equal(got, []string{"/media/*"}) {
		t.Errorf("mbin local: %v", got)
	}
	if got := kinds.LocalMediaPaths(cfg, config.App{Kind: config.KindWriteFreely, Storage: config.StorageLocal}); !slices.Equal(got, []string{"/uploads/*"}) {
		t.Errorf("writefreely local: %v", got)
	}
	if got := kinds.LocalMediaPaths(cfg, config.App{Kind: config.KindMbin}); got != nil {
		t.Errorf("mbin in garage: %v", got)
	}
	if got := kinds.LocalMediaPaths(cfg, config.App{Kind: config.KindOutline, Storage: config.StorageLocal}); got != nil {
		t.Errorf("outline local is private: %v", got)
	}
}
