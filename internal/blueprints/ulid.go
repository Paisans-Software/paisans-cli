package blueprints

import (
	"crypto/sha256"
	"math/big"
)

// StableULID is a ULID derived from an app's name, the template helper
// stableULID, for a blueprint whose software wants a ULID it is told rather
// than one it makes: the homeserver's authentication service names the
// identity provider it signs in at with one, and that ULID is in the
// callback registered at the provider, so the operator has to know it.
// Deriving it rather than generating one keeps rendering deterministic, which
// is what stops a second `apply` from inventing a provider the registered
// callback no longer matches.
//
// A ULID is 26 Crockford base32 characters over 128 bits, which leaves two
// unused bits at the top, so the leading character of any value encoded this
// way is in 0 to 7 and the result always parses. It carries no timestamp, and
// nothing reads one back out of it. The derivation's prefix is fixed: every
// deployment's registered callback depends on it.
func StableULID(app string) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	sum := sha256.Sum256([]byte("paisans-stack upstream oauth2 provider:" + app))
	n := new(big.Int).SetBytes(sum[:16])
	mask := big.NewInt(31)
	digit := new(big.Int)
	out := make([]byte, 26)
	for i := 25; i >= 0; i-- {
		out[i] = alphabet[digit.And(n, mask).Int64()]
		n.Rsh(n, 5)
	}
	return string(out)
}
