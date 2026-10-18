package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// A run of apply names what it applies before anything else: the build of
// paisans running it, the deployment id and the configuration version,
// revision and hash. They open the output,
// so a log of any run says which deployment and
// which version the rest of it is about, and two runs' logs can be told
// apart.
//
// Both are read from the workstation alone, since nothing has reached a host
// yet: the id from paisans-stack.yaml, the version from the head this
// workstation last published or pulled, when the files on disk are still
// that version. What the run cannot know yet shows as pending: an id init
// has still to write, or files no version holds until the run publishes
// them. A run that opened with either pending names both again once it ends,
// finished or stopped, with what it learned on the way.

// runIdentity is the deployment id and the version a run applies, "" and 0
// while not known.
type runIdentity struct {
	id       string
	sha      string
	revision int
	// pending is whether the run opened with either one not known, which
	// is what makes it name them again at the end.
	pending bool
}

// openIdentity reads the id and the version the files at configPath are,
// and reports them as the run's first section.
func openIdentity(r ui.Reporter, configPath string) *runIdentity {
	v := &runIdentity{id: declaredID(configPath)}
	// A file that is not where a version is kept, or a store that cannot
	// be read, leaves the version pending: the gather says why.
	if l, err := workstationOf(configPath); err == nil {
		v.sha, v.revision, _ = l.Current()
	}
	v.pending = v.id == "" || v.sha == ""
	v.report(r)
	return v
}

// closeIdentity names the id and the version again, as the run ends, when it
// opened with either pending. It names them once, whichever call is first.
func (v *runIdentity) closeIdentity(r ui.Reporter) {
	if v.pending {
		v.pending = false
		v.report(r)
	}
}

// applied records the version the run applies, once it is known.
func (v *runIdentity) applied(sha string, revision int) {
	if sha != "" {
		v.sha, v.revision = sha, revision
	}
}

func (v *runIdentity) report(r ui.Reporter) {
	r.Section("deployment")
	r.Step("paisans").End(ui.OK, version.Current().String())
	id := r.Step("id")
	if v.id == "" {
		id.End(ui.Pending, "pending: init will write it")
	} else {
		id.End(ui.OK, v.id)
	}
	version := r.Step("configuration")
	if v.sha == "" {
		version.End(ui.Pending, "pending: deployment apply --execute will publish it")
	} else {
		version.End(ui.OK, fmt.Sprintf("revision %d, %s", v.revision, v.sha[:12]))
	}
	reportCore(r)
}

// reportCore names the core catalog release the run reads, the blueprints
// and the foundation's tracks, and each newer release it skipped with why.
// Nothing when the core catalog has not been read.
func reportCore(r ui.Reporter) {
	release, skipped := blueprints.CoreRelease()
	if release.Commit == "" {
		return
	}
	core := r.Step("core catalog")
	core.End(ui.OK, fmt.Sprintf("%s, %s", release.Tag, release.Commit[:12]))
	for _, why := range skipped {
		r.Warn("a core catalog release was not used", why)
	}
}

// declaredID is the deployment id the file at path declares, "" for none or
// a file that cannot be read.
func declaredID(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		ID string `yaml:"id"`
	}
	if yaml.Unmarshal(data, &doc) != nil {
		return ""
	}
	return doc.ID
}
