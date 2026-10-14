// The reserved blueprints, built into the binary: a blueprint.yaml and the
// templates it renders, one directory per blueprint under reserved/. They are
// the blueprints no catalog may provide: the identity provider, the gate, the
// account service and the monitor. The first party blueprints every deployment
// can use are the core catalog's, which the binary does not carry
// (docs/specs/2026-10-13-images-follow-tracks.md).

package blueprints

import (
	"embed"
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed all:reserved
var embedded embed.FS

// File is the name of a blueprint's own description, in its directory.
const File = "blueprint.yaml"

// reserved is the directory in the binary holding the reserved blueprints.
const reserved = "reserved"

// embeddedBlueprint is one blueprint directory in the binary.
type embeddedBlueprint struct {
	Name string
	// Spec is the content of its blueprint.yaml.
	Spec []byte
}

// embeddedBlueprints is every blueprint built into the binary, in name order.
// Each is a reserved one.
func embeddedBlueprints() ([]embeddedBlueprint, error) {
	entries, err := fs.ReadDir(embedded, reserved)
	if err != nil {
		return nil, err
	}
	var out []embeddedBlueprint
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		spec, err := fs.ReadFile(embedded, path.Join(reserved, e.Name(), File))
		if err != nil {
			return nil, err
		}
		out = append(out, embeddedBlueprint{Name: e.Name(), Spec: spec})
	}
	return out, nil
}

// embeddedTemplates is every built in blueprint's templates as one filesystem,
// with each blueprint's directory at <name>/ and its blueprint.yaml left out,
// so a template's path is the blueprint's name and then the file's path in the
// stack it renders.
var embeddedTemplates fs.FS = templates{}

type templates struct{}

// locateEmbedded reports whether the binary holds a blueprint.
func locateEmbedded(name string) bool {
	info, err := fs.Stat(embedded, path.Join(reserved, name))
	return err == nil && info.IsDir()
}

// resolveEmbedded maps a path in embeddedTemplates to one in the embedded
// filesystem.
func resolveEmbedded(name string) (string, error) {
	if !fs.ValidPath(name) || name == "." {
		return "", fs.ErrNotExist
	}
	first, rest, _ := strings.Cut(name, "/")
	if !locateEmbedded(first) {
		return "", fs.ErrNotExist
	}
	if rest == File {
		return "", fs.ErrNotExist
	}
	return path.Join(reserved, first, rest), nil
}

func (templates) Open(name string) (fs.File, error) {
	if name == "." {
		return nil, &fs.PathError{Op: "open", Path: name, Err: errors.New("the root of the template filesystem cannot be opened, only a blueprint's directory")}
	}
	real, err := resolveEmbedded(name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return embedded.Open(real)
}

func (t templates) ReadDir(name string) ([]fs.DirEntry, error) {
	real, err := resolveEmbedded(name)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	entries, err := fs.ReadDir(embedded, real)
	if err != nil {
		return nil, err
	}
	var out []fs.DirEntry
	for _, e := range entries {
		if !strings.Contains(name, "/") && e.Name() == File {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

func (templates) Stat(name string) (fs.FileInfo, error) {
	real, err := resolveEmbedded(name)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	return fs.Stat(embedded, real)
}
