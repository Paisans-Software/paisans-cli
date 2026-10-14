// Package configstore keeps signed versions of a deployment's configuration
// and decides which of them to trust (docs/specs/2026-10-12-config-store.md).
package configstore

// Files are the three files of a version, as stored, and the snapshot of the
// catalog blueprints its apps use.
type Files struct {
	Paisans, Secrets, SOPS []byte

	// Blueprints is every catalog blueprint the configuration's apps use, as
	// it was rendered, keyed by its path under the version's blueprints/
	// directory: <catalog commit>/<blueprint>/<file>. It is what lets a
	// removal or a recovery read a blueprint whose catalog has gone, and
	// what makes every operator and every site render the same bytes
	// (docs/specs/2026-10-12-blueprints-and-catalogs.md, "The snapshot in the
	// configuration store"). Empty for a configuration that uses no catalog.
	Blueprints map[string][]byte
}

// SnapshotBlueprints, when set, is the snapshot a configuration's apps need,
// read from its paisans-stack.yaml. Local.Files calls it, so every version a
// workstation publishes carries its snapshot. The command sets it, because
// only internal/blueprints can resolve a blueprint.
var SnapshotBlueprints func(paisans []byte) (map[string][]byte, error)

// InstallBlueprints, when set, makes a snapshot readable where blueprints are
// resolved from, so a checked out or recovered version renders without its
// catalogs. writeFiles calls it.
var InstallBlueprints func(map[string][]byte) error
