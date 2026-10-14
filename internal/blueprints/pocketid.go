package blueprints

// PocketIDStandbyMarker is the text a Pocket ID v2 log carries
// when it refuses to start because another instance already holds the
// database. The standby wrapper the pocket-id kind renders (README, "Pocket ID
// runs on every apps site, and one of them is active") stands by on an exit
// whose last lines contain it, and treats every other exit as a failure.
//
// v2.14.0: bootstrap.Bootstrap turns francis's ErrClusterFull into "it
// appears that there's already one instance of Pocket ID running - running
// multiple replicas is not (yet) supported"
// (backend/internal/bootstrap/bootstrap.go:126-128), and the root command logs
// it under "error" and exits 1 (backend/internal/cmds/root.go:21-24). The part
// kept here has no apostrophe, so neither slog's quoting nor the wrapper's own
// single quotes can change it.
//
// One text serves every image on the foundation's track, which stays on v2
// (supportedTags). A release that changes it fails safe, not silently: the
// wrapper exits, Docker's restart policy retries the container more slowly,
// and apply's gate reports it as restarting. Moving the track to another
// major is a toolkit change, which is when this text is read again.
const PocketIDStandbyMarker = "already one instance of Pocket ID running"

// StandbyMarker is the refusal text for a Pocket ID image, and whether the
// image is the foundation's own repository, which is the one the text was
// read from. Any other, such as an operator's own `images.app`, gets the same
// text: the most likely to match, and harmless when it does not.
func StandbyMarker(image string) (string, bool) {
	def, err := FoundationImage(FoundationPocketID)
	known := err == nil && ParseReference(image).Repository() == ParseReference(def).Repository()
	return PocketIDStandbyMarker, known
}
