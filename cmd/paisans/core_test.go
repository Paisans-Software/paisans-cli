package main

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	_ "github.com/paisans-software/paisans-stack/internal/blueprints/coretest"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/configstore"
	"github.com/paisans-software/paisans-stack/internal/imagedigest"
)

// snapshotted is f with the snapshot of the blueprints its configuration's
// apps use, as a workstation's own files carry it, so a version a test
// writes by hand matches the files the commands read.
func snapshotted(f configstore.Files) configstore.Files {
	snap, err := blueprints.SnapshotOf(f.Paisans)
	if err != nil {
		panic(err)
	}
	if len(snap) > 0 {
		f.Blueprints = snap
	}
	return f
}

// firstTag is the tag every test registry puts a track on: the pattern
// with each `*` read as 0.
func firstTag(ref string) (string, error) { return strings.ReplaceAll(ref, "*", "0"), nil }

func (fixedDigest) Track(ref string) (string, error)  { return firstTag(ref) }
func (movingTag) Track(ref string) (string, error)    { return firstTag(ref) }
func (platformsAt) Track(ref string) (string, error)  { return firstTag(ref) }
func (publishedFor) Track(ref string) (string, error) { return firstTag(ref) }

func (unpublished) Track(ref string) (string, error) {
	if def, _ := blueprints.DefaultImage(config.Kind("mbin"), "app"); ref == def {
		return "", fmt.Errorf("ghcr.io answered that %s has no tag on its track: %w", ref, imagedigest.ErrNotFound)
	}
	return firstTag(ref)
}
