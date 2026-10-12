package main

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/configstore"
	"github.com/paisans-software/paisans-stack/internal/grant"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// What follows is apply's part in the signed copies of the configuration
// every site keeps (docs/specs/2026-10-12-config-store.md). apply publishes
// the version it applies, so the store holds the declared state even when
// the apply stops part way, and each host records the version it was given,
// so a site serving an old version cannot put it on a host that has had a
// newer one without the operator noticing.
//
// The order is the point. Before the review: gather, and refuse a fork or
// local files that are not based on the sites' newest version, since
// applying them would undo whatever the newer version changed. After the
// approval and before the first change: check each host's record, then
// publish.

// appliedVersion is the version of the configuration an apply puts on its
// hosts, as each host's grant record holds it.
type appliedVersion struct {
	sha      string
	revision int
}

// storeVersion is the version an apply is about to put on its hosts, found
// by gathering before the review.
type storeVersion interface {
	// land checks each host's grant record, by site, against the version,
	// and with execute publishes it. It is called once the operator has
	// approved the review and before the first change. A dry run reports
	// what --execute would check and publish, and returns nil.
	land(r ui.Reporter, records map[string]grant.Record, execute bool, stdin io.Reader, stdout io.Writer) (*appliedVersion, error)
}

// applyStore gathers the versions every site holds for an apply, before its
// review, and refuses a fork and a stale base. Tests replace it.
var applyStore = gatherForApply

// gathered is a gather an apply goes on from: the version it will publish
// builds on parents, or is same when the local files are that version's
// already.
type gathered struct {
	l        configstore.Local
	g        configstore.Gathered
	sites    map[string]apply.Transport
	key      string
	files    configstore.Files
	parents  []string
	same     string
	revision int
}

// gatherForApply gathers as config publish does, without the side effects
// in a dry run: no site syncs and the newest revision seen is not raised.
// It refuses, before the review, what --execute could not publish: files
// that are not the three of a version, a fork, a base that is not the
// sites' newest version, a key no admin lists, and, with --execute, a run
// that reaches no site. No base at all is a deployment's first version.
func gatherForApply(r ui.Reporter, configPath, secretsPath string, cfg *config.Config, execute bool, stdin io.Reader, stdout io.Writer) (storeVersion, error) {
	l, err := workstationOf(configPath)
	if err != nil {
		return nil, err
	}
	beside := filepath.Join(l.Dir, "secrets.enc.yaml")
	if secretsPath != "" && !samePath(secretsPath, beside) {
		return nil, &ui.Problem{Hint: "--secrets names a file other than secrets.enc.yaml beside paisans.yaml", Explain: "apply publishes the version it applies, and a version holds the secrets.enc.yaml beside paisans.yaml. Applying " + secretsPath + " would put on the hosts secrets the store does not hold. Drop --secrets, or move the file beside paisans.yaml."}
	}
	files, err := l.Files()
	if err != nil {
		return nil, &ui.Problem{Hint: "the files of a version cannot be read", Explain: "apply publishes paisans.yaml, secrets.enc.yaml and .sops.yaml from " + l.Dir + " as the version it applies. " + errLine(err) + ".", Cause: err}
	}
	a, err := l.Anchor()
	if err != nil {
		return nil, err
	}
	r.Section("configuration store")
	sites := configTransports(cfg)
	g, err := gatherAt(r, l, sites, len(sites), cfg.ID, a, gatherMode{execute: execute, remember: execute}, stdin, stdout)
	if err != nil {
		return nil, err
	}
	if g.Verdict.Fork {
		return nil, forkProblem(g, "Nothing was applied.")
	}
	head, err := l.Head()
	if err != nil {
		return nil, err
	}
	parents, err := configstore.Parents(g, head, false)
	if err != nil {
		return nil, &ui.Problem{Hint: "the local files are not based on the sites' newest version", Explain: err.Error() + ". Applying them would undo whatever that version changed. Nothing was applied.", Cause: err}
	}
	// After the gather, which brings the base into the local store: the
	// key is checked against the admins the base lists.
	key, err := signingKeyFor(l, cfg)
	if err != nil {
		return nil, err
	}
	if err := configstore.CheckSigner(l, parents, cfg, key); err != nil {
		return nil, err
	}
	if len(g.Direct()) == 0 && execute {
		return nil, &ui.Problem{Hint: "no site answered", Explain: "apply publishes the version it applies before the first change, and no site can take it. Nothing was applied.", Cause: configstore.UnreachedError(g)}
	}
	if execute {
		if err := confirmPartial(stdin, stdout, g, len(sites)); err != nil {
			return nil, err
		}
	} else if !g.Majority {
		r.Warn(fmt.Sprintf("only %d of %d sites answered directly", len(g.Direct()), len(sites)), "--execute asks for the word "+partialWord+" at a terminal.")
	}
	v := &gathered{l: l, g: g, sites: sites, key: key, files: files, parents: parents, revision: 1}
	if len(parents) == 1 {
		cache, err := l.Cache()
		if err != nil {
			return nil, err
		}
		if pf, err := cache.Files(parents[0]); err == nil && pf.Equal(files) {
			v.same = parents[0]
		}
	}
	for _, p := range parents {
		v.revision = max(v.revision, g.Versions[p].Revision+1)
	}
	if v.same != "" {
		v.revision = g.Versions[v.same].Revision
	}
	return v, nil
}

// samePath reports whether a and b name one file, as absolute paths.
func samePath(a, b string) bool {
	x, errA := filepath.Abs(a)
	y, errB := filepath.Abs(b)
	return errA == nil && errB == nil && x == y
}

// what is the version in words: the sites' version the files equal, or a
// new one on its parents.
func (v *gathered) what() string {
	switch {
	case v.same != "":
		return describe(v.g, v.same)
	case len(v.parents) == 0:
		return fmt.Sprintf("revision %d, the first version", v.revision)
	}
	var on []string
	for _, p := range v.parents {
		on = append(on, p[:12])
	}
	return fmt.Sprintf("revision %d, a new version on %s", v.revision, strings.Join(on, " and "))
}

func (v *gathered) land(r ui.Reporter, records map[string]grant.Record, execute bool, stdin io.Reader, stdout io.Writer) (*appliedVersion, error) {
	if err := witness(r, records, v.revision, v.same, v.what(), execute, stdin, stdout); err != nil {
		return nil, err
	}
	k, _, _ := config.ParseKeyLine(v.key)
	if !execute {
		if v.same != "" {
			r.Step("publish "+v.what()).End(ui.OK, "the sites hold it already; --execute pushes it again to any that lack it")
		} else {
			r.Step("publish "+v.what()).End(ui.Pending, "signed with "+k.Fingerprint+"; --execute signs and pushes it before the first change")
		}
		return nil, nil
	}
	s := r.Step("publish " + v.what())
	// The files read before the review are published, not the files on
	// disk now: those are what this run applies, whatever was edited while
	// the operator was asked.
	sha, pushed, err := configstore.PublishFiles(v.l, v.g, v.files, v.sites, v.key, configSignWith(v.key), false, configNow())
	var pe *configstore.PushError
	switch {
	case errors.As(err, &pe) && len(pushed) == 0:
		// A version no site holds is not in the store the hosts are
		// witnessed against, so nothing is applied. It is in the
		// workstation's store and recorded as the head, so the next apply
		// or publish pushes it again rather than making another.
		s.Fail(err)
		return nil, &ui.Problem{Hint: "no site took the version to apply", Explain: errLine(err) + ". Nothing was applied. The version is kept here, and the next paisans apply or paisans config publish pushes it again.", Cause: err}
	case errors.As(err, &pe):
		// A site that missed it gets it from its peers' sync, or from the
		// next apply or publish, which pushes it again.
		s.End(ui.Warned, fmt.Sprintf("%s reached %s", sha[:12], strings.Join(pushed, ", ")))
		for _, site := range slices.Sorted(maps.Keys(pe.Failed)) {
			r.Warn(site+" did not take the version", fmt.Sprintf("%v. It gets the version from its peers' sync, or from the next apply or publish.", pe.Failed[site]))
		}
	case err != nil:
		s.Fail(err)
		return nil, &ui.Problem{Hint: "could not publish the version to apply", Explain: errLine(err) + ". Nothing was applied.", Cause: err}
	default:
		s.Done(fmt.Sprintf("%s reached %s", sha[:12], strings.Join(pushed, ", ")))
	}
	return &appliedVersion{sha: sha, revision: v.revision}, nil
}

// witness refuses to put revision on a host whose grant record holds a newer
// one, or another version at the same revision, unless the operator types
// rollback at a terminal: the host has had a version the sites no longer
// serve, which is what a site that deleted the newer versions looks like.
// hash is the version being applied, "" for one not yet published, which
// no record can hold; what names it. A dry run reports it, and says
// --execute asks.
func witness(r ui.Reporter, records map[string]grant.Record, revision int, hash, what string, execute bool, stdin io.Reader, stdout io.Writer) error {
	var other []string
	for _, site := range slices.Sorted(maps.Keys(records)) {
		c := records[site].Config
		if c == nil || c.Revision < revision || (c.Revision == revision && c.Hash == hash) {
			continue
		}
		line := fmt.Sprintf("%s holds revision %d (%s)", site, c.Revision, c.Hash)
		if !c.AppliedAt.IsZero() {
			line += ", applied " + c.AppliedAt.UTC().Format("2006-01-02 15:04 MST")
		}
		if c.Revision == revision {
			line += ", another version at the same revision"
		} else {
			line += ", newer than the one you are applying"
		}
		other = append(other, line)
	}
	if len(other) == 0 {
		return nil
	}
	hint := "a host holds another version than the one you are applying"
	why := "This apply puts " + what + " on hosts whose record holds another version:\n" + strings.Join(other, "\n") + "\nA site that deleted the newer versions can serve an old one, and applying it would put back old secrets."
	if !execute {
		r.Warn(hint, why+" --execute asks for "+rollbackWord+".")
		return nil
	}
	if err := confirmWord(stdin, stdout, rollbackWord, why+" Go on only if you know why the host's version is gone from the sites."); err != nil {
		return &ui.Problem{Hint: hint, Explain: why + "\n" + sentence(err.Error())}
	}
	return nil
}
