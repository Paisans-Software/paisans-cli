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

// PinFile is the record, in core/, of which core catalog commit the binary
// fetches.
const PinFile = "pin.yaml"

// Pin is the core catalog commit this binary is pinned to
// (scripts/pin-core-catalog.sh writes it). A deployment that does not pin
// the core catalog itself uses this commit.
type Pin struct {
	URL    string `yaml:"url"`
	Commit string `yaml:"commit"`
	// Blueprints is the SHA-256 of each blueprint at the commit, as Digest
	// computes it over the blueprint's directory, so a checkout of one
	// blueprint can be checked alone.
	Blueprints map[string]string `yaml:"blueprints"`
}

// CorePin is the pin the binary was built with.
func CorePin() (Pin, error) {
	raw, err := fs.ReadFile(embedded, path.Join("core", PinFile))
	if err != nil {
		return Pin{}, err
	}
	var p Pin
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return Pin{}, fmt.Errorf("%s: %w", PinFile, err)
	}
	return p, nil
}

// Digest is the SHA-256 of the files under dir in fsys, as
// scripts/pin-core-catalog.sh computes it for each core blueprint: each
// file's SHA-256 and its path relative to dir, one per line in bytewise
// order, hashed. A pin.yaml directly under dir is left out.
func Digest(fsys fs.FS, dir string) (string, error) {
	files := map[string][]byte{}
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
		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		files[rel] = raw
		return nil
	})
	if err != nil {
		return "", err
	}
	return DigestFiles(files), nil
}

// DigestFiles is that SHA-256 for files held in memory, keyed by their path
// relative to the directory the digest is of.
func DigestFiles(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	list := sha256.New()
	for _, name := range names {
		sum := sha256.Sum256(files[name])
		fmt.Fprintf(list, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	return hex.EncodeToString(list.Sum(nil))
}
