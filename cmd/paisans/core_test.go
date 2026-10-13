package main

import (
	_ "github.com/paisans-software/paisans-stack/internal/blueprints/coretest"
	"github.com/paisans-software/paisans-stack/internal/configstore"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// snapshotted is f with the snapshot of the blueprints its configuration's
// apps use, as a workstation's own files carry it, so a version a test
// writes by hand matches the files the commands read.
func snapshotted(f configstore.Files) configstore.Files {
	snap, err := kinds.SnapshotOf(f.Paisans)
	if err != nil {
		panic(err)
	}
	if len(snap) > 0 {
		f.Blueprints = snap
	}
	return f
}
