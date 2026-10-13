package main

import (
	"github.com/paisans-software/paisans-stack/internal/configstore"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// Every version the workstation publishes carries the catalog blueprints its
// apps use, and every version it checks out makes them resolvable without the
// catalog (docs/specs/2026-10-12-blueprints-and-catalogs.md, "The snapshot in
// the configuration store"). configstore cannot read a blueprint itself, so
// the command connects the two.
func init() {
	configstore.SnapshotBlueprints = kinds.SnapshotOf
	configstore.InstallBlueprints = kinds.InstallSnapshot
}
