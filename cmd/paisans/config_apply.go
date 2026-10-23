package main

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/grant"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
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
// approval and before the first change: check each host's record, which
// only reads. Then publish: deployment apply --site before its first change, apply
// without --site once its site prepare steps have made every site's store
// and before any other step changes a host (see runConverge). What is
// published is the bytes apply loaded and applies, never a second read.

// appliedVersion is the version of the configuration an apply puts on its
// hosts, as each host's grant record holds it.
type appliedVersion struct {
	sha      string
	revision int
	// files are the bytes published, which every step of apply without
	// --site must have loaded too.
	files configstore.Files
}

// storeVersion is the version an apply is about to put on its hosts, found
// by gathering before the review.
type storeVersion interface {
	// check checks each host's grant record, by site, against the version,
	// once the operator has approved the review and before the first
	// change; a dry run reports what --execute would ask.
	check(r ui.Reporter, records map[string]grant.Record, execute bool, stdin io.Reader, stdout io.Writer) error
	// publish signs and pushes the version with execute; a dry run says
	// it would, and returns nil.
	publish(r ui.Reporter, execute bool) (*appliedVersion, error)
	// next signs and pushes files as the version after prev, which publish
	// returned earlier in the same run: what a step of the run wrote.
	next(r ui.Reporter, prev *appliedVersion, files configstore.Files) (*appliedVersion, error)
	// held is the version the sites hold already when the files are it,
	// "" when the run has still to publish them.
	held() (sha string, revision int)
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
	// answers is how check's rollback is answered: at the terminal, or by
	// --yes.
	answers answerer
}

// gatherForApply gathers as deployment publish does, without the side effects
// in a dry run: no site syncs. The seen record is written only when the
// version is published, after the review's approval (configstore.PublishFiles).
// loaded is the paisans-stack.yaml and secrets.enc.yaml the caller loaded and
// applies, which are what is published; nil reads them here, for apply
// without --site, whose steps each check that they load the same.
// It refuses, before the review, what --execute could not publish: files
// that are not the three of a version, a fork, a base that is not the
// sites' newest version, a key no operator lists, and, with --execute, a run
// that reaches no site. No base at all is a deployment's first version.
func gatherForApply(r ui.Reporter, configPath, secretsPath string, loaded *configstore.Files, cfg *config.Config, execute bool, ans answerer, stdin io.Reader, stdout io.Writer) (storeVersion, error) {
	l, err := workstationOf(configPath)
	if err != nil {
		return nil, err
	}
	beside := filepath.Join(l.Dir, "secrets.enc.yaml")
	if secretsPath != "" && !samePath(secretsPath, beside) {
		return nil, &ui.Problem{Hint: "--secrets names a file other than secrets.enc.yaml beside " + config.FileName, Explain: "apply publishes the version it applies, and a version holds the secrets.enc.yaml beside " + config.FileName + ". Applying " + secretsPath + " would put on the hosts secrets the store does not hold. Drop --secrets, or move the file beside " + config.FileName + "."}
	}
	files, err := l.Files()
	if err == nil && loaded != nil {
		files.Paisans, files.Secrets = loaded.Paisans, loaded.Secrets
	}
	if err != nil {
		return nil, &ui.Problem{Hint: "the files of a version cannot be read", Explain: "apply publishes " + config.FileName + ", secrets.enc.yaml and .sops.yaml from " + l.Dir + " as the version it applies. " + errLine(err) + ".", Cause: err}
	}
	// A version holds the secrets encrypted, and only encrypted.
	if err := config.CheckSecretsBytes(beside, files.Secrets); err != nil {
		return nil, err
	}
	a, err := l.Anchor()
	if err != nil {
		return nil, err
	}
	r.Section("configuration store")
	sites := configTransports(cfg)
	g, err := gatherAt(r, l, sites, len(sites), cfg.ID, a, gatherMode{execute: execute, answers: ans}, stdin, stdout)
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
	// key is checked against the operators the base lists.
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
		if err := confirmPartial(ans, stdin, stdout, g, len(sites)); err != nil {
			return nil, err
		}
	} else if !g.Majority {
		r.Warn(fmt.Sprintf("only %d of %d sites answered directly", len(g.Direct()), len(sites)), "--execute will ask for the word "+partialWord+" at a terminal.")
	}
	v := &gathered{l: l, g: g, sites: sites, key: key, files: files, parents: parents, revision: 1, answers: ans}
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

// what is the version in words: the sites' version the files equal, or the
// revision of a new one.
func (v *gathered) what() string {
	if v.same != "" {
		return describe(v.g, v.same)
	}
	return fmt.Sprintf("revision %d", v.revision)
}

func (v *gathered) held() (string, int) {
	if v.same == "" {
		return "", 0
	}
	return v.same, v.revision
}

func (v *gathered) check(r ui.Reporter, records map[string]grant.Record, execute bool, stdin io.Reader, stdout io.Writer) error {
	return witness(r, records, v.revision, v.same, v.what(), execute, v.answers, stdin, stdout)
}

func (v *gathered) publish(r ui.Reporter, execute bool) (*appliedVersion, error) {
	k, _, _ := config.ParseKeyLine(v.key)
	if !execute {
		if v.same != "" {
			r.Step("publish "+v.what()).End(ui.OK, "the sites hold it already")
		} else {
			r.Step("publish "+v.what()).End(ui.Pending, "signed with "+k.Fingerprint)
		}
		return nil, nil
	}
	s := r.Step("publish " + v.what())
	sha, pushed, err := configstore.PublishFiles(v.l, v.g, v.files, v.sites, v.key, configSignWith(v.key), false, configNow())
	return v.saved(r)(published(r, s, sha, pushed, err, v.revision, v.files))
}

func (v *gathered) next(r ui.Reporter, prev *appliedVersion, files configstore.Files) (*appliedVersion, error) {
	s := r.Step(fmt.Sprintf("publish revision %d", prev.revision+1))
	s.Detail("what this run recorded in secrets.enc.yaml, on %s", prev.sha[:12])
	sha, pushed, err := configstore.PublishNext(v.l, v.g, prev.sha, prev.revision, files, v.sites, v.key, configSignWith(v.key), configNow())
	return v.saved(r)(published(r, s, sha, pushed, err, prev.revision+1, files))
}

// saved saves a version the run published to this workstation's deployments,
// so every deployment applied from here is found again by deployment list
// wherever its files were.
func (v *gathered) saved(r ui.Reporter) func(*appliedVersion, error) (*appliedVersion, error) {
	return func(a *appliedVersion, err error) (*appliedVersion, error) {
		if err == nil && a != nil {
			saveVersion(r, v.l, a.sha, a.revision, a.files, v.key, "applied from "+v.l.Dir)
		}
		return a, err
	}
}

// published ends s, the step that published files as revision, from what
// the publish returned: a site that missed the version is warned about, and
// a version no site took, or none at all, stops the apply.
func published(r ui.Reporter, s ui.Step, sha string, pushed []string, err error, revision int, files configstore.Files) (*appliedVersion, error) {
	var pe *configstore.PushError
	switch {
	case errors.As(err, &pe) && len(pushed) == 0:
		// A version no site holds is not in the store the hosts are
		// witnessed against, so nothing is applied.
		s.Fail(err)
		return nil, noSiteTook(pe)
	case errors.As(err, &pe):
		// A site that missed it gets it from its peers' sync.
		s.End(ui.Warned, fmt.Sprintf("%s reached %s", sha[:12], strings.Join(pushed, ", ")))
		for _, site := range slices.Sorted(maps.Keys(pe.Failed)) {
			r.Warn(site+" did not take the version", pushFailure(site, pe.Failed[site])+" It gets the version from its peers' sync.")
		}
	case err != nil:
		s.Fail(err)
		return nil, &ui.Problem{Hint: "could not publish the version to apply", Explain: errLine(err) + ". Nothing was applied.", Cause: err}
	default:
		s.Done(sha[:12])
	}
	return &appliedVersion{sha: sha, revision: revision, files: files}, nil
}

// pushFailure is why site did not take a version, as a sentence: a site
// with no store is named with the command that makes one.
func pushFailure(site string, err error) string {
	if errors.Is(err, configstore.ErrNoStore) {
		return site + " has no configuration store yet; " + progname.Cmd() + " site prepare --site " + site + " --execute will make it."
	}
	if err == nil {
		return site + " did not answer."
	}
	return sentence(errLine(err))
}

// noSiteTook is the refusal when no site took the version: each site with
// why, a site with no store named with the site prepare that makes it.
func noSiteTook(pe *configstore.PushError) error {
	var lines []string
	for _, site := range slices.Sorted(maps.Keys(pe.Failed)) {
		lines = append(lines, pushFailure(site, pe.Failed[site]))
	}
	return &ui.Problem{
		Hint:    "no site took the version to apply",
		Explain: strings.Join(lines, "\n") + "\nA version no site holds is not one the hosts can be checked against, so nothing was applied. Once a site can take it, run " + progname.Cmd() + " deployment apply --execute again.",
		Cause:   pe,
	}
}

// witness refuses to put revision on a host whose grant record holds a newer
// one, or another version at the same revision, unless the operator types
// rollback at a terminal: the host has had a version the sites no longer
// serve, which is what a site that deleted the newer versions looks like.
// hash is the version being applied, "" for one not yet published, which
// no record can hold; what names it. A dry run reports it, and says
// --execute asks.
func witness(r ui.Reporter, records map[string]grant.Record, revision int, hash, what string, execute bool, ans answerer, stdin io.Reader, stdout io.Writer) error {
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
		r.Warn(hint, why+" --execute will ask for "+rollbackWord+".")
		return nil
	}
	if err := confirmWord(ans, stdin, stdout, rollbackWord, why+" Proceed only if you know why the host's version is gone from the sites."); err != nil {
		return &ui.Problem{Hint: hint, Explain: why + "\n" + sentence(err.Error())}
	}
	return nil
}

// publishWritten publishes, as the version after the one s records, the
// secrets.enc.yaml this apply wrote when it recorded what it made at Pocket
// ID: written, the bytes it wrote, never the file read again, beside the
// rest of s's version. The new version is the one s records, so the host
// that runs what was recorded holds the revision that has it; as a step of
// apply without --site it is also the run's, which every later step applies.
// identity names it. Nothing written, or no version to build on, publishes
// nothing.
func (s *siteReview) publishWritten(r ui.Reporter, written []byte, store storeVersion, identity *runIdentity) error {
	if s == nil || s.version == nil || written == nil || store == nil {
		return nil
	}
	files := s.version.files
	files.Secrets = written
	v, err := store.next(r, s.version, files)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	s.version = v
	if run := reviewedByRun; run != nil {
		run.version = v
	}
	if identity != nil {
		identity.applied(v.sha, v.revision)
	}
	return nil
}
