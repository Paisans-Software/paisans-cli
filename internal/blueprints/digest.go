package blueprints

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Digest is the SHA-256 of the files under dir in fsys: each file's SHA-256
// and its path relative to dir, one per line in bytewise order, hashed.
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
