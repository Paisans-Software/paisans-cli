package blueprints

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// PinFile is the record, in core/, of which core catalog commit the copy
// there came from.
const PinFile = "pin.yaml"

// Pin is the core catalog commit this binary carries a copy of
// (scripts/sync-core-catalog.sh writes it). A deployment that does not pin
// the core catalog itself uses this commit.
type Pin struct {
	URL    string `yaml:"url"`
	Commit string `yaml:"commit"`
	SHA256 string `yaml:"sha256"`
}

// CorePin is the pin the binary was built with.
func CorePin() (Pin, error) {
	raw, err := fs.ReadFile(embedded, path.Join(string(Core), PinFile))
	if err != nil {
		return Pin{}, err
	}
	var p Pin
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return Pin{}, fmt.Errorf("%s: %w", PinFile, err)
	}
	return p, nil
}

// CoreDigest is the SHA-256 of the copy of the core catalog in the binary,
// computed as scripts/sync-core-catalog.sh computes it: each file's SHA-256
// and its path relative to core/, one per line in bytewise order, hashed.
// pin.yaml itself is left out.
func CoreDigest() (string, error) {
	return Digest(embedded, string(Core))
}

// Digest is that SHA-256 for the blueprints under dir in fsys, the same
// whether they came from the binary or from a catalog's checkout.
func Digest(fsys fs.FS, dir string) (string, error) {
	var files []string
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(p, dir+"/")
		if rel == PinFile {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	list := sha256.New()
	for _, f := range files {
		raw, err := fs.ReadFile(fsys, path.Join(dir, f))
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(raw)
		fmt.Fprintf(list, "%s  %s\n", hex.EncodeToString(sum[:]), f)
	}
	return hex.EncodeToString(list.Sum(nil)), nil
}
