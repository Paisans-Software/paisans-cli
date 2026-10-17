package create

import (
	"bufio"
	"io/fs"
	"path"
	"strings"

	"golang.org/x/crypto/ssh"
)

// LocalKey is an SSH public key found on the workstation create runs on.
type LocalKey struct {
	// Name is where it was found, as the operator is shown it.
	Name string
	// Line is the key as one line of a .pub file, comment and all.
	Line string
	// About is the key's type, fingerprint and comment, which tell keys apart
	// where the key itself is too long to read.
	About string
}

// FindLocalKeys reads the public keys in dir, a user's ~/.ssh: the first line
// of each *.pub file that is an SSH public key, in name order, each key once.
// A certificate is left out, since a site's ssh.keys takes a key. A file that
// cannot be read or holds no key is skipped: what is found is offered, never
// required. Each key's Name is its file's name under shown.
func FindLocalKeys(dir fs.FS, shown string) []LocalKey {
	names, err := fs.Glob(dir, "*.pub")
	if err != nil {
		return nil
	}
	var keys []LocalKey
	seen := map[string]bool{}
	for _, name := range names {
		line, pub, ok := readPublicKey(dir, name)
		if !ok || seen[string(pub.Marshal())] {
			continue
		}
		seen[string(pub.Marshal())] = true
		about := pub.Type() + " " + ssh.FingerprintSHA256(pub)
		if f := strings.Fields(line); len(f) > 2 {
			about += " " + strings.Join(f[2:], " ")
		}
		keys = append(keys, LocalKey{Name: path.Join(shown, name), Line: line, About: about})
	}
	return keys
}

// readPublicKey is the first line of name that is a public key, and the key.
func readPublicKey(dir fs.FS, name string) (string, ssh.PublicKey, bool) {
	f, err := dir.Open(name)
	if err != nil {
		return "", nil, false
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return "", nil, false
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			return "", nil, false
		}
		if _, cert := pub.(*ssh.Certificate); cert {
			return "", nil, false
		}
		return line, pub, true
	}
	return "", nil, false
}
