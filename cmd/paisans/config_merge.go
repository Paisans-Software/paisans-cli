package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/draft"
	"github.com/paisans-software/paisans-cli/internal/draftmerge"
	"github.com/paisans-software/paisans-cli/internal/localstore"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// What follows merges a draft forward (docs/specs/2026-10-24-system-copy.md,
// *Deploying a draft*, step 4): when the sites hold a version newer than
// the one a draft was drafted from, deployment pull and deployment apply
// merge that version into the draft with a three-way merge, base the
// version the draft was drafted from, ours the draft, theirs the sites'
// newest. paisans-stack.yaml and .sops.yaml merge path by path
// (internal/draftmerge), and the secrets by key once decrypted
// (configstore.MergeSecrets). A conflict keeps the draft's value and is
// listed, a secret by its key only. The merged draft's "drafted from" line
// names the sites' newest version, conflicts or not, so the next apply
// deploys the operator's resolution rather than merging again.

// merged is a draft merged forward onto the sites' newest version.
type merged struct {
	// files are the draft's files merged, as a version holds them, and
	// marker the version they are now drafted from.
	files  configstore.Files
	marker draft.Marker
	// conflicts is each path the draft and that version changed
	// differently, naming its file: "paisans-stack.yaml: sites.home-b",
	// "secrets: external.a", ".sops.yaml: creation_rules.0".
	conflicts []string
}

// mergeDraft merges d, drafted from base, forward onto head, the sites'
// newest version, both in the system copy's store. It writes nothing.
// Secrets that differ on all three sides are decrypted with this
// workstation's key, and a side it cannot decrypt is refused.
func mergeDraft(sys configstore.Local, d draft.Draft, id, base, head string) (merged, error) {
	cache, err := sys.Cache()
	if err != nil {
		return merged{}, err
	}
	b, err := cache.Files(base)
	if err != nil {
		return merged{}, err
	}
	m, t, err := headOf(cache, id, head)
	if err != nil {
		return merged{}, err
	}
	o, err := d.Files()
	if err != nil {
		return merged{}, &ui.Problem{Hint: "the draft cannot be read to merge it", Explain: "A merge reads the draft's " + config.FileName + ", secrets.enc.yaml and .sops.yaml from " + d.Dir + ". " + errLine(err) + ".", Cause: err}
	}
	o.Paisans = draft.StripMarker(o.Paisans)
	out := merged{marker: m, files: configstore.Files{Blueprints: t.Blueprints}}
	var conflicts []string
	if out.files.Paisans, conflicts, err = draftmerge.Config(b.Paisans, o.Paisans, t.Paisans); err != nil {
		return merged{}, &ui.Problem{Hint: "the draft's " + config.FileName + " cannot be merged", Explain: errLine(err) + ". Nothing was written.", Cause: err}
	}
	out.add(config.FileName, conflicts)
	if out.files.SOPS, conflicts, err = draftmerge.SOPS(b.SOPS, o.SOPS, t.SOPS); err != nil {
		return merged{}, &ui.Problem{Hint: "the draft's " + config.SOPSConfigName + " cannot be merged", Explain: errLine(err) + ". Nothing was written.", Cause: err}
	}
	out.add(config.SOPSConfigName, conflicts)
	if out.files.Secrets, conflicts, err = mergeSecretsFiles(d, id, b, o, t, out.files.SOPS); err != nil {
		return merged{}, err
	}
	out.add("secrets", conflicts)
	return out, nil
}

func (m *merged) add(file string, paths []string) {
	for _, p := range paths {
		m.conflicts = append(m.conflicts, file+": "+ui.Visible(p))
	}
}

// mergeSecretsFiles merges the secrets of b, the base, o, the draft's, and
// t, the sites' newest version, as files encrypted to sops, the merged
// .sops.yaml. A file only one side changed is taken as it is when it is
// encrypted for sops already. Otherwise the three are decrypted in memory,
// merged key by key, and the merge encrypted to the recipients sops names.
// conflicts are keys only: a value is never printed.
func mergeSecretsFiles(d draft.Draft, id string, b, o, t configstore.Files, sops []byte) ([]byte, []string, error) {
	switch {
	case bytesEqual(o.Secrets, t.Secrets):
		return o.Secrets, nil, nil
	case bytesEqual(o.Secrets, b.Secrets) && bytesEqual(t.SOPS, sops):
		return t.Secrets, nil, nil
	case bytesEqual(t.Secrets, b.Secrets) && bytesEqual(o.SOPS, sops):
		return o.Secrets, nil, nil
	}
	base, err := config.DecryptSecretsBytes(b.Secrets, id)
	if err != nil {
		return nil, nil, secretsMergeProblem("the base version's secrets")
	}
	theirs, err := config.DecryptSecretsBytes(t.Secrets, id)
	if err != nil {
		return nil, nil, secretsMergeProblem("the sites' newest version's secrets")
	}
	ours, err := config.LoadDeploymentSecrets(filepath.Join(d.Dir, configstore.FileSecrets), id)
	if errors.Is(err, config.ErrPastedControl) {
		// It names the key and `secrets set`, the repair, and never the
		// value, so it is said as it is.
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, secretsMergeProblem("the draft's secrets.enc.yaml")
	}
	m, conflicts, err := configstore.MergeSecrets(base, ours, theirs)
	if err != nil {
		return nil, nil, &ui.Problem{Hint: "the secrets cannot be merged", Explain: sentence(err.Error()) + " Nothing was written."}
	}
	// The recipients are the ones the merged .sops.yaml names, read from a
	// scratch copy, where the merge is encrypted before anything in the
	// draft changes.
	scratch, err := os.MkdirTemp("", "paisans-merge-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(scratch)
	if err := os.WriteFile(filepath.Join(scratch, config.SOPSConfigName), sops, 0o600); err != nil {
		return nil, nil, err
	}
	recipients, err := config.Recipients(scratch)
	if err != nil {
		return nil, nil, err
	}
	if len(recipients) == 0 {
		return nil, nil, &ui.Problem{Hint: "the merged .sops.yaml names no age recipient", Explain: "The merged secrets would be written unencrypted. Nothing was written."}
	}
	out, err := config.WriteSecretsBytes(filepath.Join(scratch, configstore.FileSecrets), m, recipients)
	if err != nil {
		return nil, nil, err
	}
	return out, conflicts, nil
}

// secretsMergeProblem refuses without the underlying error: a parse error can
// quote a fragment of the document, which is a value.
func secretsMergeProblem(what string) error {
	return &ui.Problem{Hint: "cannot read " + what + " to merge the secrets", Explain: "It could not be decrypted and parsed on this workstation, and the secrets are merged key by key only with all three sides readable. The reason is left out because it can quote a value. Nothing was written."}
}

// mergeForward merges the draft d, drafted from base, forward onto head,
// the sites' newest version g holds. With execute it writes the merged
// draft, its "drafted from" line naming head, then moves the system copy
// to head (moveSystemCopy). It shows the merged draft against head, and
// returns the merge.
func mergeForward(r ui.Reporter, sys configstore.Local, g configstore.Gathered, d draft.Draft, id, base, head, gatheredHead string, execute bool, record func(configstore.Local) error) (merged, error) {
	m, err := mergeDraft(sys, d, id, base, head)
	if err != nil {
		return m, err
	}
	what := fmt.Sprintf("merge revision %d into this draft", m.marker.Revision)
	cache, err := sys.Cache()
	if err != nil {
		return m, err
	}
	theirs, err := cache.Files(head)
	if err != nil {
		return m, err
	}
	lines, err := difference(theirs, m.files, sides{dirA: fmt.Sprintf("revision-%d/", m.marker.Revision), dirB: "draft/", inA: fmt.Sprintf("revision %d", m.marker.Revision), inB: "the draft"})
	if err != nil {
		return m, err
	}
	if !execute {
		r.Step(what).End(ui.Pending, "--execute will write it")
	} else {
		s := r.Step(what)
		if err := d.Write(m.files, m.marker); err != nil {
			s.Fail(err)
			return m, err
		}
		if err := moveSystemCopy(sys, id, gatheredHead, head, record); err != nil {
			s.Fail(err)
			return m, err
		}
		s.Done(describe(g, head))
	}
	if len(lines) > 0 {
		r.Step("what the merged draft changes").End(ui.OK, "the draft against "+describe(g, head))
		r.Verbatim(lines)
	}
	return m, nil
}

// moveSystemCopy moves the system copy to head, the sites' newest version,
// under its lock, and records with record what the gather saw. It is
// refused when the system copy moved from gathered, its head at the
// gather, since then another command wrote it.
func moveSystemCopy(sys configstore.Local, id, gathered, head string, record func(configstore.Local) error) error {
	st, err := openStore()
	if err != nil {
		return err
	}
	unlock, err := st.Lock(id)
	if err != nil {
		var locked *localstore.LockedError
		if errors.As(err, &locked) {
			return &ui.Problem{Hint: "another command is writing the system copy", Explain: sentence(locked.Error()) + " The merged draft is written; its \"drafted from\" line names the version merged. Once that command is done, run this again.", Cause: err}
		}
		return err
	}
	defer unlock()
	cur, err := sys.Head()
	if err != nil {
		return err
	}
	if cur != gathered {
		return &ui.Problem{Hint: "another command moved the system copy", Explain: fmt.Sprintf("The system copy of %s moved from %s to %s since this command gathered the sites' versions. The merged draft is written; its \"drafted from\" line names the version merged. Run this again.", id, headName(gathered), headName(cur))}
	}
	if cur != head {
		if err := sys.Checkout(head); err != nil {
			return err
		}
	}
	return record(sys)
}

// conflictsExplain lists the conflicts of m and says what settles them.
func conflictsExplain(m merged) string {
	return fmt.Sprintf("The draft and revision %d change these differently, and the draft keeps its own value at each:\n%s\nEdit the draft to the value it should have (%s secrets set for a secret), then run %s deployment apply --execute.", m.marker.Revision, strings.Join(m.conflicts, "\n"), progname.Cmd(), progname.Cmd())
}
