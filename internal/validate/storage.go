package validate

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// storage checks each app's `storage` key against its kind and placement.
// README, Rule 5, "A pinned app may keep its files on its own disk", says why
// each combination is refused rather than warned about: every one of them
// either renders an app that cannot store a file, or silently means something
// other than what was written.
func (c *checker) storage() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		key := fmt.Sprintf("apps.%s.storage", name)
		switch {
		case app.Kind == config.KindPocketID:
			// Pocket ID has a storage setting of its own, which this key
			// would shadow without replacing.
			if app.Storage != "" {
				c.refuse("storage-on-pocket-id", key,
					"pocket-id takes settings.file_backend, not storage",
					"is %q, and pocket-id does not read it. Pocket ID chooses where it keeps its files with its own FILE_BACKEND, set here as apps.%s.settings.file_backend; database is the recommendation (README, Rule 3). Remove storage.",
					app.Storage, name)
			}
		case blueprints.KeepsFilesOnlyLocally(app.Kind):
			if app.Storage == config.StorageS3 {
				c.refuse("storage-s3-on-a-local-only-kind", key,
					"this kind keeps its files only on its own disk",
					"is s3, but %s has no object storage form here: %s. Remove the key, or set storage: local, which is what it does anyway.",
					app.Kind, localOnlyReason(app.Kind))
			}
		case !blueprints.UsesObjectStorage(app.Kind):
			if app.Storage != "" {
				c.refuse("storage-on-a-kind-without-files", key,
					"storage is declared for a kind that stores no files",
					"is %q, but %s stores no files, so this would be ignored without a word. Remove it.",
					app.Storage, app.Kind)
			}
		default:
			c.objectStorageApp(name, app)
		}
	}
}

// objectStorageApp checks an app of a kind that can keep its files in S3 or
// on its own disk.
func (c *checker) objectStorageApp(name string, app config.App) {
	key := fmt.Sprintf("apps.%s.storage", name)
	garage := len(c.cfg.Storage.Garage.Sites) > 0
	if app.Storage == config.StorageS3 && !garage {
		c.refuse("storage-s3-without-garage", key,
			"s3 storage needs a Garage site",
			"is s3, but storage.garage.sites names no site, so there is no object storage for %s to write to and every upload would fail. Add a Garage site, or set storage: local and pin the app.",
			name)
		return
	}
	if app.Placement.Mode == config.PlacementCluster && blueprints.KeepsFilesLocally(c.cfg, app) {
		// A clustered app runs on every apps site, and each would keep its
		// own copy of the files: an upload on one site is missing on the
		// others, and a failover moves readers onto the copy without it.
		if app.Storage == config.StorageLocal {
			c.refuse("storage-local-on-cluster", key,
				"local storage needs pinned placement",
				"is local, but apps.%s.placement is cluster. A clustered app runs on every apps site, and each would keep its own files, so an upload on one site would be missing from the others. Pin the app (placement: { pinned: <site> }), or remove storage to keep its files in Garage.",
				name)
		} else {
			c.refuse("storage-local-on-cluster", fmt.Sprintf("apps.%s.placement", name),
				"a clustered app's files need Garage",
				"is cluster, and storage.garage.sites names no site, so %s would keep its files on its own disk on every apps site, each a different copy. Add a Garage site, or pin the app (placement: { pinned: <site> }).",
				name)
		}
	}
	if declared := strings.TrimSpace(app.Hostnames[blueprints.MediaRole]); declared != "" && !blueprints.InObjectStorage(c.cfg, app) {
		c.refuse("media-hostname-without-object-storage", fmt.Sprintf("apps.%s.hostnames.%s", name, blueprints.MediaRole),
			"media hostname is declared for an app with no bucket",
			"is %q, but %s keeps its files on its own disk and serves them on its own hostname, so nothing would answer this name. Remove it, or keep the files in Garage.",
			declared, name)
	}
}

// localOnlyReason is why a kind that keeps its files only on its own disk
// has no S3 form, for the refusal of `storage: s3` on it.
func localOnlyReason(kind config.Kind) string {
	if why := blueprints.LocalOnlyReason(kind); why != "" {
		return why
	}
	return "it keeps its files on local disk"
}
