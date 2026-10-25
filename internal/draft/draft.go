// Package draft reads and writes a folder's draft: the three files of a
// deployment and the "drafted from" line that names the version they were
// drafted from (docs/specs/2026-10-24-system-copy.md). A draft is plain files;
// nothing here reads trust from a folder.
package draft

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
)

// Marker names the version a draft was drafted from.
type Marker struct {
	Revision int
	SHA      string
}

var markerRE = regexp.MustCompile(`^# paisans: drafted from revision ([0-9]+) \(([0-9a-f]{40}|[0-9a-f]{64})\)$`)

func (m Marker) line() []byte {
	return []byte("# paisans: drafted from revision " + strconv.Itoa(m.Revision) + " (" + m.SHA + ")\n")
}

// firstLine splits paisans at its first line, without the line ending.
func firstLine(paisans []byte) (line, rest []byte) {
	line, rest, _ = bytes.Cut(paisans, []byte("\n"))
	return bytes.TrimSuffix(line, []byte("\r")), rest
}

// ReadMarker reads the marker on the first line of paisans-stack.yaml. It
// reports false when the line is absent or malformed.
func ReadMarker(paisans []byte) (Marker, bool) {
	line, _ := firstLine(paisans)
	m := markerRE.FindSubmatch(line)
	if m == nil {
		return Marker{}, false
	}
	n, err := strconv.Atoi(string(m[1]))
	if err != nil || n < 1 {
		return Marker{}, false
	}
	return Marker{Revision: n, SHA: string(m[2])}, true
}

// StripMarker removes the first line when it is a marker.
func StripMarker(paisans []byte) []byte {
	if _, ok := ReadMarker(paisans); !ok {
		return paisans
	}
	_, rest := firstLine(paisans)
	return rest
}

// WithMarker returns paisans with m as its first line, replacing a marker
// already there.
func WithMarker(paisans []byte, m Marker) []byte {
	body := StripMarker(paisans)
	return append(m.line(), body...)
}

// Draft is a folder holding a deployment's three files.
type Draft struct{ Dir string }

func (d Draft) path(name string) string { return filepath.Join(d.Dir, name) }

// Has reports whether the folder holds a paisans-stack.yaml.
func (d Draft) Has() bool {
	_, err := os.Lstat(d.path(config.FileName))
	return err == nil
}

// HasAny reports whether the folder holds any of the three files.
func (d Draft) HasAny() bool {
	for _, n := range []string{config.FileName, configstore.FileSecrets, configstore.FileSOPS} {
		if _, err := os.Lstat(d.path(n)); err == nil {
			return true
		}
	}
	return false
}

// Files reads the three files. Paisans keeps its marker line.
func (d Draft) Files() (configstore.Files, error) {
	var f configstore.Files
	for _, e := range []struct {
		name string
		dst  *[]byte
	}{{config.FileName, &f.Paisans}, {configstore.FileSecrets, &f.Secrets}, {configstore.FileSOPS, &f.SOPS}} {
		data, err := os.ReadFile(d.path(e.name))
		if err != nil {
			return configstore.Files{}, err
		}
		*e.dst = data
	}
	return f, nil
}

// Write writes the three files, each replaced whole and the secrets file
// readable only by its owner, with m as the first line of paisans-stack.yaml.
func (d Draft) Write(f configstore.Files, m Marker) error {
	f.Paisans = WithMarker(f.Paisans, m)
	return configstore.WriteFilesTo(d.Dir, f)
}

// Mark rewrites the first line of the folder's paisans-stack.yaml to m and
// leaves the rest of it, and the other two files, as they are: what the
// operator edited since the version m names stays a draft on it.
func (d Draft) Mark(m Marker) error {
	path := d.path(config.FileName)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(d.Dir, "."+config.FileName+".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(WithMarker(body, m)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), info.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
