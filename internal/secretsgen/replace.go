package secretsgen

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/progname"
)

// Replacement is a new value for the secret at a dotted path, in the shape
// that secret's consumer parses: Garage and the monitors take hex, and the
// cluster's and Garage's own passwords are generated passwords. A password in
// a hex position is a service that will not start, so the shape is chosen
// here, once, from the path. An app's secret is made as its blueprint says,
// which only ReplacementIn, given the deployment, can read.
//
// It refuses the secrets whose replacement would do harm, rather than
// generating a value for the caller to decide about. A WireGuard key is the
// mesh's, and `paisans mesh rekey` moves it without dropping a tunnel. It also refuses a path the file holds nothing at, because
// a replacement for a secret that does not exist is a rotation of nothing.
func Replacement(path string, s *config.Secrets) (string, error) {
	return ReplacementIn(nil, path, s)
}

// ReplacementIn is Replacement for a deployment, which lets an app's
// secret be made as its blueprint says: in its format, or refused when the
// blueprint says it is never rotated, as an encryption key protecting data
// already stored is, or is a keypair rotated only with its group.
func ReplacementIn(cfg *config.Config, path string, s *config.Secrets) (string, error) {
	parts := strings.Split(path, ".")
	last := parts[len(parts)-1]
	if cfg != nil && len(parts) == 3 && parts[0] == "apps" {
		if spec, ok := blueprints.SecretOf(cfg.Apps[parts[1]].Blueprint, last); ok {
			if _, held := s.Get(path); !held {
				return "", fmt.Errorf("%s: the secrets file holds no value to replace", path)
			}
			switch {
			case spec.NeverRotate != "":
				return "", fmt.Errorf("%s: %s, so nothing here replaces it", path, spec.NeverRotate)
			case spec.Format == blueprints.FormatRSAKeypair, spec.Format == blueprints.FormatRSAPublic,
				spec.Format == blueprints.FormatEd25519OpenSSH, spec.Format == blueprints.FormatEd25519Public:
				return "", fmt.Errorf("%s: a keypair is replaced with the rest of apps.%s.%s", path, parts[1], spec.Group)
			}
			return generate(spec.Format)
		}
	}
	switch {
	case last == "wireguard_private_key" || last == "wireguard_next_private_key":
		return "", fmt.Errorf("%s: a WireGuard key is moved by `%s mesh rekey`, which keeps every tunnel up", path, progname.Cmd())
	}

	var gen func() (string, error)
	switch {
	case len(parts) == 2 && parts[0] == "cluster":
		gen = password
	case path == "storage.garage.admin_token":
		gen = password
	case path == "storage.garage.rpc_secret":
		gen = func() (string, error) { return hexSecret(32) }
	case len(parts) == 3 && parts[0] == "sites" && last == "heartbeat_token":
		gen = func() (string, error) { return hexSecret(16) }
	case len(parts) == 3 && parts[0] == "apps" && last == "database_password":
		// Every Postgres backed app has one, whatever its blueprint says.
		gen = password
	}
	if gen == nil {
		if _, known := s.Get(path); known {
			return "", fmt.Errorf("%s: this secret has no replacement generator here", path)
		}
		return "", fmt.Errorf("%s: names no secret this file holds", path)
	}
	if _, ok := s.Get(path); !ok {
		return "", fmt.Errorf("%s: the secrets file holds no value to replace", path)
	}
	return gen()
}

// ReplacementKeypair is a new encrypted RSA keypair under passphrase. The
// private key is encrypted by the passphrase, so the pair is replaced together
// with it and not on its own.
func ReplacementKeypair(passphrase string) (privatePEM, publicPEM string, err error) {
	return oauthKeypairPEM(passphrase)
}

// ReplacementEd25519 is a new Ed25519 keypair, replaced together because the
// public half is the private key's and not a value of its own.
func ReplacementEd25519() (privatePEM, publicLine string, err error) {
	return ed25519Keypair()
}
