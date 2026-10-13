// Package blueprints holds the blueprints built into the binary: a
// blueprint.yaml and the templates it renders, one directory per blueprint.
// See docs/specs/2026-10-12-blueprints-and-catalogs.md.
//
// reserved/ holds the blueprints no catalog may provide: the identity
// provider, the gate, the invite service and the monitor. core/ holds the
// first party blueprints every deployment can use.
//
// This package only carries the files. internal/kinds reads them.
package blueprints

import (
	"embed"
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed all:core all:reserved
var embedded embed.FS

// CoreSigners is the allowed-signers list of the keys the core catalog's
// release tags are signed by, one SSH public key per line. A deployment that
// pins the core catalog to a commit of its own, or to a mirror, is checked
// against it; the toolkit's own copy needs no check, being in the binary.
//
//go:embed signers/core
var CoreSigners []byte

// File is the name of a blueprint's own description, in its directory.
const File = "blueprint.yaml"

// Origin is where a built in blueprint comes from.
type Origin string

const (
	Core     Origin = "core"
	Reserved Origin = "reserved"
)

// Builtin is one blueprint directory in the binary.
type Builtin struct {
	Name   string
	Origin Origin
	// Spec is the content of its blueprint.yaml.
	Spec []byte
}

// Builtins is every blueprint built into the binary, reserved ones first,
// each in name order.
func Builtins() ([]Builtin, error) {
	var out []Builtin
	for _, origin := range []Origin{Reserved, Core} {
		entries, err := fs.ReadDir(embedded, string(origin))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			spec, err := fs.ReadFile(embedded, path.Join(string(origin), e.Name(), File))
			if err != nil {
				return nil, err
			}
			out = append(out, Builtin{Name: e.Name(), Origin: origin, Spec: spec})
		}
	}
	return out, nil
}

// Templates is every built in blueprint's templates as one filesystem, with
// each blueprint's directory at <name>/ and its blueprint.yaml left out, so a
// template's path is the blueprint's name and then the file's path in the
// stack it renders.
var Templates fs.FS = templates{}

type templates struct{}

// locate finds which origin holds a blueprint.
func locate(name string) (string, bool) {
	for _, origin := range []Origin{Reserved, Core} {
		if info, err := fs.Stat(embedded, path.Join(string(origin), name)); err == nil && info.IsDir() {
			return string(origin), true
		}
	}
	return "", false
}

// resolve maps a path in Templates to one in the embedded filesystem.
func resolve(name string) (string, error) {
	if !fs.ValidPath(name) || name == "." {
		return "", fs.ErrNotExist
	}
	first, rest, _ := strings.Cut(name, "/")
	origin, ok := locate(first)
	if !ok {
		return "", fs.ErrNotExist
	}
	if rest == File {
		return "", fs.ErrNotExist
	}
	return path.Join(origin, first, rest), nil
}

func (templates) Open(name string) (fs.File, error) {
	if name == "." {
		return nil, &fs.PathError{Op: "open", Path: name, Err: errors.New("the root of the template filesystem cannot be opened, only a blueprint's directory")}
	}
	real, err := resolve(name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return embedded.Open(real)
}

func (t templates) ReadDir(name string) ([]fs.DirEntry, error) {
	real, err := resolve(name)
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
	real, err := resolve(name)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	return fs.Stat(embedded, real)
}
