// Package operatorinvite is the invite an operator writes for someone they
// invite, and the request that someone answers it with: their file formats,
// their checks and their SSH signatures. Both files are plain text, the YAML
// that is signed and then the armored SSH signature over exactly its bytes,
// as ssh-keygen -Y sign writes one.
package operatorinvite

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// The namespaces each signature is made under, so a signature over an
// invite never verifies as a request's, nor the other way around, nor as
// any other SSH signature the same key makes.
const (
	InviteNamespace  = "paisans-operator-invite"
	RequestNamespace = "paisans-operator-request"
)

const (
	beginSignature = "-----BEGIN SSH SIGNATURE-----"
	endSignature   = "-----END SSH SIGNATURE-----"
	// principal is the one identity the allowed-signers file Verify writes
	// names: the file holds the one key a signature is checked against.
	principal = "paisans"
)

// MaxSize is the largest invite or request the parsers read. Each is a few
// KiB, and each comes from someone else, so a larger file is refused before
// it is decoded.
const MaxSize = 64 * 1024

// Sign is the armored SSH signature of data by the key whose public key line
// is keyLine, under namespace. ssh-keygen -Y sign makes it with -U, so the
// ssh-agent holding the key signs and the private key never leaves it.
var Sign = func(keyLine, namespace string, data []byte) ([]byte, error) {
	key, err := canonicalKey(keyLine)
	if err != nil {
		return nil, fmt.Errorf("the signing key is %v", err)
	}
	dir, err := os.MkdirTemp("", "paisans-sign")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	pub := filepath.Join(dir, "key.pub")
	if err := os.WriteFile(pub, []byte(key+"\n"), 0o600); err != nil {
		return nil, err
	}
	cmd := exec.Command("ssh-keygen", "-Y", "sign", "-n", namespace, "-f", pub, "-U")
	cmd.Stdin = bytes.NewReader(data)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ssh-keygen -Y sign with %s from the ssh-agent: %v: %s", fingerprint(key), err, strings.TrimSpace(stderr.String()))
	}
	sig := out.Bytes()
	if body, _, err := split(sig); err != nil || len(body) > 0 || !bytes.HasSuffix(sig, []byte("\n")) {
		return nil, fmt.Errorf("ssh-keygen -Y sign with %s printed something other than one SSH signature block", fingerprint(key))
	}
	return sig, nil
}

// Signed is the signed file: body, then the armored signature block on a
// line of its own. body ends with a newline, as EncodeInvite and
// EncodeRequest write it; one is added to a body that does not.
func Signed(body, sig []byte) []byte {
	out := make([]byte, 0, len(body)+1+len(sig))
	out = append(out, body...)
	if len(body) > 0 && body[len(body)-1] != '\n' {
		out = append(out, '\n')
	}
	return append(out, sig...)
}

// Verify checks that sig is a signature of body by the key keyLine under
// namespace, with ssh-keygen -Y verify and an allowed-signers file holding
// that key alone.
func Verify(keyLine, namespace string, body, sig []byte) error {
	key, err := canonicalKey(keyLine)
	if err != nil {
		return fmt.Errorf("the key to verify with is %v", err)
	}
	dir, err := os.MkdirTemp("", "paisans-verify")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	allowed, sigFile := filepath.Join(dir, "allowed_signers"), filepath.Join(dir, "sig")
	line := fmt.Sprintf("%s namespaces=%q %s\n", principal, namespace, key)
	if err := os.WriteFile(allowed, []byte(line), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(sigFile, sig, 0o600); err != nil {
		return err
	}
	cmd := exec.Command("ssh-keygen", "-Y", "verify", "-f", allowed, "-I", principal, "-n", namespace, "-s", sigFile)
	cmd.Stdin = bytes.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("not a signature by %s under %s (%s)", fingerprint(key), namespace, strings.TrimSpace(string(out)))
	}
	return nil
}

// split is a signed file's body and its signature block. The body ends at
// the first line that is the block's opening line, and nothing may follow
// the block's closing line but the newline that ends it.
func split(signed []byte) (body, sig []byte, err error) {
	at := -1
	if bytes.HasPrefix(signed, []byte(beginSignature+"\n")) {
		at = 0
	} else if i := bytes.Index(signed, []byte("\n"+beginSignature+"\n")); i >= 0 {
		at = i + 1
	}
	if at < 0 {
		return nil, nil, errors.New("signature: none, and a signed file ends with a " + beginSignature + " block")
	}
	body, sig = signed[:at], signed[at:]
	end := bytes.Index(sig, []byte("\n"+endSignature))
	if end < 0 {
		return nil, nil, errors.New("signature: the block has no " + endSignature + " line")
	}
	end += len("\n" + endSignature)
	if rest := sig[end:]; len(rest) > 0 && string(rest) != "\n" {
		return nil, nil, fmt.Errorf("signature: %d bytes after the signature block, and nothing follows it", len(rest)-1)
	}
	return body, sig, nil
}

// sshsig is an SSH signature's blob after its SSHSIG magic, as OpenSSH's
// PROTOCOL.sshsig lays it out.
type sshsig struct {
	Version   uint32
	PublicKey []byte
	Namespace string
	Reserved  string
	HashAlg   string
	Signature []byte
}

// signer is the public key line an armored signature names as its signer,
// as type and base64 alone. It is what the signature claims, not yet checked:
// Verify with it is what checks it.
func signer(sig []byte) (string, error) {
	text := strings.TrimSpace(string(sig))
	text = strings.TrimPrefix(text, beginSignature)
	text = strings.TrimSuffix(text, endSignature)
	blob, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(text), ""))
	if err != nil || !bytes.HasPrefix(blob, []byte("SSHSIG")) {
		return "", errors.New("signature: not an SSH signature")
	}
	var s sshsig
	if err := ssh.Unmarshal(blob[len("SSHSIG"):], &s); err != nil {
		return "", fmt.Errorf("signature: not an SSH signature (%v)", err)
	}
	pub, err := ssh.ParsePublicKey(s.PublicKey)
	if err != nil {
		return "", fmt.Errorf("signature: its key is not one OpenSSH reads (%v)", err)
	}
	return canonicalKey(string(ssh.MarshalAuthorizedKey(pub)))
}

// canonicalKey is a public key line as type and base64 alone: its comment
// dropped, since an invite carries nothing the inviter does not need and
// free text is what a forger varies to search for a matching digest.
func canonicalKey(line string) (string, error) {
	pub, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	switch {
	case err != nil:
		return "", fmt.Errorf("not an OpenSSH public key (%v)", err)
	case len(options) > 0:
		return "", fmt.Errorf("carries options (%q)", strings.Join(options, ","))
	case len(strings.TrimSpace(string(rest))) > 0:
		return "", fmt.Errorf("more than one key on the line")
	}
	if p := config.KeyTypeProblem(pub); p != "" {
		return "", errors.New(p)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))), nil
}

// fingerprint is the SHA256 fingerprint of a canonical key line.
func fingerprint(key string) string {
	k, _, err := config.ParseKeyLine(key)
	if err != nil {
		return key
	}
	return k.Fingerprint
}

// Error is an invite or a request refused, with every problem found.
type Error struct {
	What     string
	Problems []string
}

func (e *Error) Error() string {
	return e.What + " is refused:\n- " + strings.Join(e.Problems, "\n- ")
}
