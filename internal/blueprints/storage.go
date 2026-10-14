package blueprints

import "github.com/paisans-software/paisans-stack/internal/config"

// KeepsFilesOnlyLocally reports whether a kind stores files that can live
// only on its own site's disk, with no object storage form at all.
//
// A kind like that is pinned only for that reason, and an app's `storage`
// key has nothing to choose between for it: local is accepted, because it is
// what the kind does anyway, and s3 is refused.
func KeepsFilesOnlyLocally(blueprint config.Blueprint) bool { return of(blueprint).LocalFilesOnly }

// InObjectStorage reports whether an app keeps its files in the deployment's
// Garage, which is what decides whether it has a bucket, a key, a media
// hostname, and a route and DNS record for it.
//
// Only a kind that can use S3 (UsesObjectStorage) ever does. For one of
// those, `storage: s3` and `storage: local` say so outright, and an app that
// leaves the key out takes S3 when storage.garage.sites names a site and its
// own disk when it names none. The default is S3 wherever Garage exists
// because that is what Rule 3 ships, and local where it does not because
// there is no S3 to use: rendering S3 settings with no endpoint would only
// produce an app whose every upload fails. Founder decision.
//
// validate refuses the combinations this cannot answer coherently: local
// under cluster placement, whether written or the default, and s3 with no
// Garage site.
func InObjectStorage(cfg *config.Config, app config.App) bool {
	if !UsesObjectStorage(app.Blueprint) {
		return false
	}
	switch app.Storage {
	case config.StorageS3:
		return true
	case config.StorageLocal:
		return false
	}
	return len(cfg.Storage.Garage.Sites) > 0
}

// KeepsFilesLocally reports whether an app that can use S3 keeps its files on
// its own site's disk instead, by `storage: local` or by the default in a
// deployment with no Garage.
func KeepsFilesLocally(cfg *config.Config, app config.App) bool {
	return UsesObjectStorage(app.Blueprint) && !InObjectStorage(cfg, app)
}

// LocalMediaPaths is the paths an app's locally kept files are served on,
// on its own hostname, that must stay readable without a gate session. Empty
// for an app in object storage, and for a kind whose files are private.
//
// In S3 mode the media hostname carries these and is never gated; in local
// mode the app's own hostname carries them, so a gated app opens exactly
// these paths to keep the same contract.
func LocalMediaPaths(cfg *config.Config, app config.App) []string {
	if !KeepsFilesLocally(cfg, app) {
		return nil
	}
	return append([]string(nil), of(app.Blueprint).LocalMediaPaths...)
}
