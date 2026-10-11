package kinds

import "github.com/paisans-software/paisans-stack/internal/config"

// KeepsFilesOnlyLocally reports whether a kind stores files that can live
// only on its own site's disk, with no object storage form at all.
//
// Synapse's S3 storage provider supplements its media store rather than
// replacing it, so a local media directory is always required. The uptime
// fork keeps everything, its SQLite database included, under /data. Both are
// pinned only for that reason, and an app's `storage` key has nothing to
// choose between for either: local is accepted, because it is what the kind
// does anyway, and s3 is refused.
func KeepsFilesOnlyLocally(kind config.Kind) bool {
	return kind == config.KindSynapse || kind == config.KindUptime
}

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
	if !UsesObjectStorage(app.Kind) {
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
	return UsesObjectStorage(app.Kind) && !InObjectStorage(cfg, app)
}

// localMediaPaths is where each kind serves its stored files on its own
// hostname when it keeps them locally, for a kind whose files must be
// readable with no credential (ServesObjectsPublicly). In S3 mode the media
// hostname carries these and is never gated; in local mode the app's own
// hostname carries them, so a gated app opens exactly these paths to keep
// the same contract.
//
// Mbin's are under /media, served as static files by the web server in its
// own container (docs/02-admin/01-installation/02-docker.md, "Uploaded media
// files", at the paisans fork's tag v1.14.0+paisans), and the federated copy
// of a post points a remote instance there. The writefreely-wisp fork's are
// under /uploads/, served by uploadsHandler from the local image store
// (imagestore.go:364-386 at ff9dceb, the commit the pinned image is built
// from).
//
// Outline is absent on purpose. Its local store answers through its own API
// with the reader's session or a signature it issued, so there is nothing to
// open.
var localMediaPaths = map[config.Kind][]string{
	config.KindMbin:        {"/media/*"},
	config.KindWriteFreely: {"/uploads/*"},
}

// LocalMediaPaths is the paths an app's locally kept files are served on,
// on its own hostname, that must stay readable without a gate session. Empty
// for an app in object storage, and for a kind whose files are private.
func LocalMediaPaths(cfg *config.Config, app config.App) []string {
	if !KeepsFilesLocally(cfg, app) {
		return nil
	}
	return append([]string(nil), localMediaPaths[app.Kind]...)
}
