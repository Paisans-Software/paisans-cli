package kinds

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// Catalogs are signed at their release tags, with git's SSH signatures
// (docs/specs/2026-10-12-blueprints-and-catalogs.md, "Signing"). A pin names
// the keys its tags must be signed by. Trust starts from the operator: the
// first key is one they accepted by its fingerprint, and a catalog moves to
// a new key only through a tag the old key signed, which lists the new one in
// keys/allowed_signers. Sites never check a catalog's signature: they trust
// the operator's signature on the configuration version that carries the
// snapshot.

// KeysFile is where a catalog lists the keys its release tags are signed by.
const KeysFile = "keys/allowed_signers"

// signedBy is the fingerprint git's verify-tag reports for a signed tag,
// whether or not the key is trusted.
var signedBy = regexp.MustCompile(`with [A-Z0-9-]+ key (SHA256:[A-Za-z0-9+/=]+)`)

// Signature is what a tag's signature says.
type Signature struct {
	// Fingerprint is the signing key's, empty for a tag with no signature.
	Fingerprint string
	// Trusted is whether the key is one of the signers checked against.
	Trusted bool
}

// VerifyTag reads the signature of tag in the git checkout at dir, against
// signers, each an SSH public key line.
func VerifyTag(dir, tag string, signers []string) (Signature, error) {
	allowed, err := os.CreateTemp("", "paisans-allowed-signers-")
	if err != nil {
		return Signature{}, err
	}
	defer os.Remove(allowed.Name())
	w := bufio.NewWriter(allowed)
	for _, s := range signers {
		key, _, err := config.ParseKeyLine(s)
		if err != nil {
			allowed.Close()
			return Signature{}, fmt.Errorf("signer %q: %w", s, err)
		}
		fmt.Fprintf(w, "catalog namespaces=\"git\" %s\n", key.Line)
	}
	if err := w.Flush(); err != nil {
		allowed.Close()
		return Signature{}, err
	}
	allowed.Close()
	out, err := git(dir, "-c", "gpg.format=ssh", "-c", "gpg.ssh.allowedSignersFile="+allowed.Name(), "verify-tag", "refs/tags/"+tag)
	m := signedBy.FindStringSubmatch(out)
	if m == nil {
		if strings.Contains(out, "no signature found") || strings.Contains(out, "cannot verify a non-tag object") {
			return Signature{}, nil
		}
		if err != nil {
			return Signature{}, fmt.Errorf("verifying %s: %s", tag, out)
		}
		return Signature{}, nil
	}
	return Signature{Fingerprint: m[1], Trusted: err == nil}, nil
}

// PublishedKeys is the keys a catalog lists in keys/allowed_signers at a tag,
// by fingerprint. A line may be a bare key or an allowed-signers line whose
// key follows its principal and options.
func PublishedKeys(dir, tag string) (map[string]string, error) {
	out, err := git(dir, "show", "refs/tags/"+tag+"^{commit}:"+KeysFile)
	if err != nil {
		return map[string]string{}, nil
	}
	keys := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		for i := range fields {
			key, _, err := config.ParseKeyLine(strings.Join(fields[i:], " "))
			if err == nil {
				keys[key.Fingerprint] = key.Line
				break
			}
		}
	}
	return keys, nil
}

// checkTrust refuses a fetched catalog whose pinned tag is not signed by one
// of its signers. An unsigned pin is checked for nothing, and says so
// wherever it is reviewed.
func checkTrust(dir string, cat config.Catalog, coreSigners bool) error {
	signers := cat.Signers
	if coreSigners {
		signers = CoreSigners()
		if len(signers) == 0 {
			return fmt.Errorf("this toolkit carries no core release key, so the core catalog can only be its own copy. Remove catalogs.core.commit")
		}
	}
	if cat.Unsigned && !coreSigners {
		return nil
	}
	if cat.Tag == "" {
		return fmt.Errorf("a signed pin names its tag, the object the signature is on")
	}
	sig, err := VerifyTag(dir, cat.Tag, signers)
	if err != nil {
		return err
	}
	switch {
	case sig.Fingerprint == "":
		return fmt.Errorf("%s's tag %s is not signed", cat.URL, cat.Tag)
	case !sig.Trusted:
		return fmt.Errorf("%s's tag %s is signed by %s, which is not one of the pin's signers", cat.URL, cat.Tag, sig.Fingerprint)
	}
	return nil
}

// CoreSigners is the keys the core catalog's release tags are signed by,
// built into the binary (internal/blueprints/signers). A core catalog pinned
// to another commit or a mirror must carry tags they signed.
func CoreSigners() []string {
	var out []string
	for _, line := range strings.Split(string(blueprints.CoreSigners), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

// TagsAfter is the catalog's tags after from, up to and including to, oldest
// first, in version order.
func TagsAfter(dir, from, to string) ([]string, error) {
	out, err := git(dir, "tag", "--sort=v:refname")
	if err != nil {
		return nil, fmt.Errorf("listing tags: %s", out)
	}
	var tags []string
	started := from == ""
	for _, t := range strings.Fields(out) {
		if started {
			tags = append(tags, t)
		}
		if t == from {
			started = true
		}
		if t == to {
			break
		}
	}
	if len(tags) == 0 || tags[len(tags)-1] != to {
		return nil, fmt.Errorf("%s does not come after %s", to, from)
	}
	return tags, nil
}

// Rotation is the outcome of walking a catalog's tags from a pin to a newer
// release: the signers the new pin carries, or the first tag whose key the
// operator has not accepted.
type Rotation struct {
	Signers []string
	// Stopped names the tag whose signature no trusted key made, and
	// Fingerprint its key, when the walk could not reach the release.
	Stopped, Fingerprint string
}

// Rotate walks the tags after a pin up to to, in version order. A tag signed
// by a trusted key is accepted, and the keys it lists in keys/allowed_signers
// become trusted for the tags after it: that is how a catalog moves to a new
// key. A tag signed by a key nothing trusted lists stops the walk, unless the
// operator accepted that key's fingerprint, in accept.
func Rotate(dir string, cat config.Catalog, to string, accept string) (Rotation, error) {
	tags, err := TagsAfter(dir, cat.Tag, to)
	if err != nil {
		return Rotation{}, err
	}
	trusted := map[string]string{}
	for _, s := range cat.Signers {
		key, _, err := config.ParseKeyLine(s)
		if err != nil {
			return Rotation{}, fmt.Errorf("signer %q: %w", s, err)
		}
		trusted[key.Fingerprint] = key.Line
	}
	for _, tag := range tags {
		sig, err := VerifyTag(dir, tag, values(trusted))
		if err != nil {
			return Rotation{}, err
		}
		published, err := PublishedKeys(dir, tag)
		if err != nil {
			return Rotation{}, err
		}
		switch {
		case sig.Fingerprint == "":
			return Rotation{Stopped: tag}, nil
		case !sig.Trusted && sig.Fingerprint != accept:
			return Rotation{Stopped: tag, Fingerprint: sig.Fingerprint}, nil
		case !sig.Trusted:
			line, ok := published[sig.Fingerprint]
			if !ok {
				return Rotation{}, fmt.Errorf("tag %s is signed by %s, which its %s does not list, so the key cannot be recorded", tag, sig.Fingerprint, KeysFile)
			}
			trusted[sig.Fingerprint] = line
		}
		for fp, line := range published {
			trusted[fp] = line
		}
	}
	return Rotation{Signers: values(trusted)}, nil
}

func values(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
