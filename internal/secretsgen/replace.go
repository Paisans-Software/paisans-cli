package secretsgen

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// Replacement is a new value for the secret at a dotted path, in the shape
// that secret's consumer parses: Garage and the monitors take hex, MAS takes a
// PEM key, and everything else is a generated password. A password in a hex
// or PEM position is a service that will not start, so the shape is chosen
// here, once, from the path.
//
// It refuses the secrets whose replacement would do harm, rather than
// generating a value for the caller to decide about. An encryption key
// protects data already stored, so a new one leaves that data unreadable. A
// WireGuard key is the mesh's, and `paisans mesh rekey` moves it without
// dropping a tunnel. It also refuses a path the file holds nothing at, because
// a replacement for a secret that does not exist is a rotation of nothing.
func Replacement(path string, s *config.Secrets) (string, error) {
	parts := strings.Split(path, ".")
	last := parts[len(parts)-1]
	switch {
	case last == "encryption_key" && len(parts) == 3 && parts[0] == "apps",
		last == masEncryptionSecret && len(parts) == 3 && parts[0] == "apps":
		return "", fmt.Errorf("%s: rotating it makes the data it encrypts unreadable, so nothing here replaces it", path)
	case last == "wireguard_private_key" || last == "wireguard_next_private_key":
		return "", fmt.Errorf("%s: a WireGuard key is moved by `paisans mesh rekey`, which keeps every tunnel up", path)
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
	case len(parts) == 3 && parts[0] == "apps":
		switch last {
		case "database_password", "app_secret", "cookie_secret", "session_secret",
			"admin_password", "static_api_key", "rabbitmq_password", "valkey_password",
			"mercure_jwt_secret", "oauth_passphrase", "oauth_encryption_key", masMatrixSecret,
			"email_hash_key", "session_key":
			gen = password
		case masSigningKey:
			gen = rsaPrivateKeyPEM
		}
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

// ReplacementOAuthKeypair is a new Mbin OAuth2 keypair under passphrase. The
// private key is encrypted by the passphrase, so the pair is replaced together
// with oauth_passphrase and not on its own.
func ReplacementOAuthKeypair(passphrase string) (privatePEM, publicPEM string, err error) {
	return oauthKeypairPEM(passphrase)
}
