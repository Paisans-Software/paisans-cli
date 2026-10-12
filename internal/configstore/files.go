// Package configstore keeps signed versions of a deployment's configuration
// and decides which of them to trust (docs/specs/2026-10-12-config-store.md).
package configstore

// Files are the three files of a version, as stored.
type Files struct {
	Paisans, Secrets, SOPS []byte
}
