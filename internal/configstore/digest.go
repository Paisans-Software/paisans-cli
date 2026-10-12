// Package configstore keeps signed versions of a deployment's configuration
// and decides which of them to trust (docs/specs/2026-10-12-config-store.md).
package configstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// Files are the three files of a version, as stored.
type Files struct {
	Paisans, Secrets, SOPS []byte
}

var (
	ErrNoDigest       = errors.New("the secrets file records no digest of its configuration")
	ErrDigestMismatch = errors.New("the secrets file's digest does not match paisans.yaml and .sops.yaml")
)

// Digest is what a version's secrets file records about the other two files.
func Digest(paisansYAML, sopsYAML []byte) string {
	a := sha256.Sum256(paisansYAML)
	b := sha256.Sum256(sopsYAML)
	return "sha256:" + hex.EncodeToString(a[:]) + " sha256:" + hex.EncodeToString(b[:])
}

// CheckDigest decrypts the version's secrets with the operator's age key, which
// verifies sops's MAC, and compares the digest they record with the files.
func CheckDigest(f Files, id string) error {
	s, err := config.DecryptSecretsBytes(f.Secrets, id)
	if err != nil {
		return fmt.Errorf("decrypting the secrets: %w", err)
	}
	got, ok := s.Get("paisans.config_digest")
	if !ok {
		return ErrNoDigest
	}
	if got != Digest(f.Paisans, f.SOPS) {
		return ErrDigestMismatch
	}
	return nil
}
