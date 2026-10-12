package configstore

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployment"
)

// Local is an operator's workstation: the directory holding paisans.yaml,
// secrets.enc.yaml and .sops.yaml, and beside them .paisans/ with what the
// workstation remembers about the store (docs/specs/2026-10-12-config-store.md,
// *Commands*).
type Local struct{ Dir string }

const (
	stateDir      = ".paisans"
	rootFile      = "config-root"
	headFile      = "config-head"
	seenFile      = "config-seen"
	signingFile   = "signing-key"
	cacheDir      = "store.git"
	incomingDir   = "incoming"
	stateIgnore   = "store.git/\nincoming/\n"
	stateFileMode = 0o644
)

func (l Local) state(name string) string { return filepath.Join(l.Dir, stateDir, name) }

// ensureState creates .paisans/ and its .gitignore, which keeps the cache and
// the incoming files out of the operator's own repository: the cache is
// this workstation's own memory of every version it has seen, and the
// incoming files are a pull's scratch.
func (l Local) ensureState() error {
	if err := os.MkdirAll(filepath.Join(l.Dir, stateDir), 0o755); err != nil {
		return err
	}
	path := l.state(".gitignore")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, []byte(stateIgnore), stateFileMode)
}

// readState is one state file's single value, "" when the file is absent.
func (l Local) readState(name string) (string, error) {
	data, err := os.ReadFile(l.state(name))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// writeState replaces a state file whole, through a rename, so a reader never
// sees half of one.
func (l Local) writeState(name, value string) error {
	if err := l.ensureState(); err != nil {
		return err
	}
	return writeAtomic(l.state(name), []byte(value+"\n"), stateFileMode)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (l Local) readSHA(name string) (string, error) {
	v, err := l.readState(name)
	if err != nil || v == "" {
		return v, err
	}
	if err := checkSHA(v); err != nil {
		return "", fmt.Errorf("%s: %w", l.state(name), err)
	}
	return v, nil
}

func (l Local) writeSHA(name, sha string) error {
	if err := checkSHA(sha); err != nil {
		return err
	}
	return l.writeState(name, sha)
}

// Root is the pinned root, "" when none is.
func (l Local) Root() (string, error) { return l.readSHA(rootFile) }

// SetRoot pins sha as the root.
func (l Local) SetRoot(sha string) error { return l.writeSHA(rootFile, sha) }

// Head is the version the local files started from, "" when none did.
func (l Local) Head() (string, error) { return l.readSHA(headFile) }

// SetHead records sha as the version the local files started from.
func (l Local) SetHead(sha string) error { return l.writeSHA(headFile, sha) }

// Seen is the highest revision of a trusted head any gather here has seen,
// and its hash; 0 and "" when none has.
func (l Local) Seen() (int, string, error) {
	v, err := l.readState(seenFile)
	if err != nil || v == "" {
		return 0, "", err
	}
	f := strings.Fields(v)
	rev := 0
	if len(f) == 2 {
		rev, _ = strconv.Atoi(f[0])
	}
	if rev < 1 || checkSHA(f[len(f)-1]) != nil {
		return 0, "", fmt.Errorf("%s: %q is not a revision and a commit id", l.state(seenFile), v)
	}
	return rev, f[1], nil
}

// raiseSeen records revision rev at sha as seen, when it is higher than what
// is recorded. It never lowers it: that is the memory a rollback is told by.
func (l Local) raiseSeen(rev int, sha string) error {
	old, _, err := l.Seen()
	if err != nil || rev <= old {
		return err
	}
	if err := checkSHA(sha); err != nil {
		return err
	}
	return l.writeState(seenFile, fmt.Sprintf("%d %s", rev, sha))
}

// AcceptRollback records revision rev at sha as the newest seen, even when
// it is lower than what is recorded. It is the one way the record goes down,
// and only because the operator typed rollback for that head: they chose to
// go on from it, and every later gather is judged against that choice.
func (l Local) AcceptRollback(rev int, sha string) error {
	if rev < 1 {
		return fmt.Errorf("revision %d is not one", rev)
	}
	if err := checkSHA(sha); err != nil {
		return err
	}
	return l.writeState(seenFile, fmt.Sprintf("%d %s", rev, sha))
}

// RollbackError is a gather whose newest trusted version is older than one
// this workstation has seen before: what a site serving an old, genuine
// version after deleting the newer ones looks like. The Gathered returned
// beside it is complete, for a command that the operator lets go on.
type RollbackError struct {
	SeenRevision int
	SeenSHA      string
	Revision     int
	SHA          string
}

func (e *RollbackError) Error() string {
	return fmt.Sprintf("the newest version the sites hold is revision %d (%s), older than revision %d (%s), which this workstation has seen before", e.Revision, e.SHA, e.SeenRevision, e.SeenSHA)
}

// SigningKey is the operator's public key line, "" when none is recorded.
func (l Local) SigningKey() (string, error) {
	line, err := l.readState(signingFile)
	if err != nil || line == "" {
		return line, err
	}
	if _, _, err := config.ParseKeyLine(line); err != nil {
		return "", fmt.Errorf("%s: %w", l.state(signingFile), err)
	}
	return line, nil
}

// SetSigningKey records the operator's public key line.
func (l Local) SetSigningKey(line string) error {
	k, opts, err := config.ParseKeyLine(line)
	if err != nil {
		return err
	}
	if len(opts) > 0 {
		return fmt.Errorf("a signing key is a bare public key line, without options")
	}
	return l.writeState(signingFile, k.Line)
}

// Anchor is what this workstation starts trust from: the fingerprint of its
// signing key and its pinned root, either of which may be empty.
func (l Local) Anchor() (Anchor, error) {
	var a Anchor
	line, err := l.SigningKey()
	if err != nil {
		return a, err
	}
	if line != "" {
		k, _, _ := config.ParseKeyLine(line)
		a.Signer = k.Fingerprint
	}
	a.Root, err = l.Root()
	return a, err
}

// Cache is the workstation's own store, .paisans/store.git, which holds every
// version any gather has seen. Nothing is ever removed from it, so a site
// that drops a version cannot make the workstation forget it.
func (l Local) Cache() (Repo, error) {
	if err := l.ensureState(); err != nil {
		return Repo{}, err
	}
	return InitRepo(l.state(cacheDir))
}

func (l Local) path(name string) string { return filepath.Join(l.Dir, name) }

// Files reads the three files beside paisans.yaml.
func (l Local) Files() (Files, error) {
	var f Files
	for _, e := range []struct {
		name string
		dst  *[]byte
	}{{namePaisans, &f.Paisans}, {nameSecrets, &f.Secrets}, {nameSOPS, &f.SOPS}} {
		data, err := os.ReadFile(l.path(e.name))
		if err != nil {
			return Files{}, err
		}
		*e.dst = data
	}
	return f, nil
}

// HasFiles reports whether any of the three files is on disk.
func (l Local) HasFiles() bool {
	for _, n := range []string{namePaisans, nameSecrets, nameSOPS} {
		if _, err := os.Lstat(l.path(n)); err == nil {
			return true
		}
	}
	return false
}

// WriteFiles writes the three files beside paisans.yaml, each replaced whole.
// The secrets file stays readable only by its owner, as init writes it.
func (l Local) WriteFiles(f Files) error { return writeFiles(l.Dir, f) }

func writeFiles(dir string, f Files) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, e := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{{namePaisans, f.Paisans, 0o644}, {nameSecrets, f.Secrets, 0o600}, {nameSOPS, f.SOPS, 0o644}} {
		if err := writeAtomic(filepath.Join(dir, e.name), e.data, e.mode); err != nil {
			return err
		}
	}
	return nil
}

// Checkout writes the version sha's three files beside paisans.yaml and
// records it as the head.
func (l Local) Checkout(sha string) error {
	cache, err := l.Cache()
	if err != nil {
		return err
	}
	f, err := cache.Files(sha)
	if err != nil {
		return err
	}
	if err := l.WriteFiles(f); err != nil {
		return err
	}
	return l.SetHead(sha)
}

// Incoming is where a pull that meets local edits puts the head's files.
func (l Local) Incoming() string { return l.state(incomingDir) }

// WriteIncoming writes the version sha's three files to Incoming.
func (l Local) WriteIncoming(sha string) error {
	cache, err := l.Cache()
	if err != nil {
		return err
	}
	f, err := cache.Files(sha)
	if err != nil {
		return err
	}
	return writeFiles(l.Incoming(), f)
}

// IncomingFor is where one head of a fork's files go: incoming/<sha>.
func (l Local) IncomingFor(sha string) string { return filepath.Join(l.Incoming(), sha) }

// WriteIncomingFor writes the version sha's three files to IncomingFor(sha).
func (l Local) WriteIncomingFor(sha string) error {
	if err := checkSHA(sha); err != nil {
		return err
	}
	cache, err := l.Cache()
	if err != nil {
		return err
	}
	f, err := cache.Files(sha)
	if err != nil {
		return err
	}
	return writeFiles(l.IncomingFor(sha), f)
}

// Equal reports whether two sets of the three files are byte for byte the
// same.
func (f Files) Equal(o Files) bool {
	return bytes.Equal(f.Paisans, o.Paisans) && bytes.Equal(f.Secrets, o.Secrets) && bytes.Equal(f.SOPS, o.SOPS)
}

// Reached is one site a gather tried. Direct is a site that answered over
// ssh; Err is why one did not. SyncErr is a site that answered but whose own
// sync with its peers did not complete; its versions still count.
type Reached struct {
	Site    string
	Direct  bool
	Err     error
	SyncErr string
}

// Gathered is every version the sites and the cache hold, and what the trust
// rules make of them.
type Gathered struct {
	Verdict  Verdict
	Reached  []Reached
	Majority bool // sites reached directly are more than half of those configured
	Synced   bool // each site was asked to sync with its peers first
	Versions map[string]Version
	Holders  map[string][]string // version -> the sites whose answer held it
	Signers  map[string]string   // version -> the admin id its key belongs to, "" when none
}

// Direct is the sites reached directly, in name order.
func (g Gathered) Direct() []string {
	var out []string
	for _, r := range g.Reached {
		if r.Direct {
			out = append(out, r.Site)
		}
	}
	return out
}

// Roots is every root among sha and its ancestors.
func (g Gathered) Roots(sha string) []string {
	seen := map[string]bool{}
	var roots []string
	stack := []string{sha}
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[s] {
			continue
		}
		seen[s] = true
		v, ok := g.Versions[s]
		if !ok {
			continue
		}
		if len(v.Parents) == 0 {
			roots = append(roots, s)
		}
		stack = append(stack, v.Parents...)
	}
	slices.Sort(roots)
	return roots
}

// PullFunc and PushFunc are how a gather and a publish reach a site's store:
// Pull and Push, which tests replace.
type (
	PullFunc func(apply.Transport, deployment.Deployment, bool) ([]byte, *SyncReport, error)
	PushFunc func(apply.Transport, deployment.Deployment, []byte) error
)

// Gather collects every version the sites hold into the cache and judges
// them from the workstation's own anchor (Local.Anchor). Each site first
// syncs with its peers (Pull with syncFirst), which changes the site, so
// Gather is for a command run with --execute. configured is how many sites
// the configuration declares, which the majority is counted against.
//
// A head older than the newest this workstation has seen is a
// *RollbackError, returned with the complete Gathered; otherwise the head's
// revision is recorded as seen.
func Gather(l Local, sites map[string]apply.Transport, configured int, id string) (Gathered, error) {
	a, err := l.Anchor()
	if err != nil {
		return Gathered{}, err
	}
	return gather(l, sites, configured, id, a, GatherOptions{Sync: true, Remember: true}, Pull)
}

// GatherOptions say what a gather may change. Sync asks each site to sync
// with its peers first, which changes the site, so only a run with --execute
// sets it. Remember raises the newest revision seen; it is set only when the
// anchor is the workstation's recorded one, or a root this run records, so
// that judging from a root the operator only tried out leaves no trace.
type GatherOptions struct {
	Sync, Remember bool
}

// GatherAnchored is Gather from the anchor a, with what it may change set
// by o.
func GatherAnchored(l Local, sites map[string]apply.Transport, configured int, id string, a Anchor, o GatherOptions) (Gathered, error) {
	return gather(l, sites, configured, id, a, o, Pull)
}

func gather(l Local, sites map[string]apply.Transport, configured int, id string, a Anchor, o GatherOptions, pull PullFunc) (Gathered, error) {
	sync := o.Sync
	cache, err := l.Cache()
	if err != nil {
		return Gathered{}, err
	}
	d := deployment.Deployment{ID: id}
	g := Gathered{Holders: map[string][]string{}, Signers: map[string]string{}, Synced: sync}
	direct := 0
	for _, name := range slices.Sorted(maps.Keys(sites)) {
		bundle, report, err := pull(sites[name], d, sync)
		if err == nil && bundle != nil {
			var heads []string
			if heads, err = cache.bundleVersions(bundle); err == nil {
				err = cache.FetchBundle(bundle)
			}
			if err == nil {
				for _, sha := range heads {
					g.Holders[sha] = append(g.Holders[sha], name)
				}
			}
		}
		if err != nil {
			g.Reached = append(g.Reached, Reached{Site: name, Err: err})
			continue
		}
		r := Reached{Site: name, Direct: true}
		if report != nil {
			r.SyncErr = report.SyncErr
		}
		g.Reached = append(g.Reached, r)
		direct++
	}
	g.Majority = configured > 0 && 2*direct > configured

	shas, err := cache.versionRefs()
	if err != nil {
		return g, err
	}
	configs := versionConfigs(cache)
	admins := adminReader(configs)
	own, err := l.SigningKey()
	if err != nil {
		return g, err
	}
	signers, err := allowedSigners(shas, configs, own)
	if err != nil {
		return g, err
	}
	defer os.RemoveAll(filepath.Dir(signers))
	vs, err := cache.Versions(signers)
	if err != nil {
		return g, err
	}
	g.Versions = map[string]Version{}
	for _, v := range vs {
		g.Versions[v.SHA] = v
	}
	for sha, v := range g.Versions {
		g.Signers[sha] = signerID(v, admins)
	}
	if len(g.Versions) == 0 {
		g.Verdict = Verdict{Trusted: map[string]bool{}, Reasons: map[string]string{}}
		return g, nil
	}
	g.Verdict, err = Judge(Graph{Versions: g.Versions, Admins: admins}, a)
	if err != nil || len(g.Verdict.Heads) == 0 {
		return g, err
	}
	top := g.Verdict.Heads[0]
	rev := g.Versions[top].Revision
	seenRev, seenSHA, err := l.Seen()
	if err != nil {
		return g, err
	}
	if rev < seenRev {
		return g, &RollbackError{SeenRevision: seenRev, SeenSHA: seenSHA, Revision: rev, SHA: top}
	}
	if !o.Remember {
		return g, nil
	}
	return g, l.raiseSeen(rev, top)
}

// versionConfigs reads a version's paisans.yaml, decoded as strictly as
// config.Load decodes the local one, and remembers each answer.
func versionConfigs(cache Repo) func(sha string) (*config.Config, error) {
	type result struct {
		cfg *config.Config
		err error
	}
	memo := map[string]result{}
	return func(sha string) (*config.Config, error) {
		if r, ok := memo[sha]; ok {
			return r.cfg, r.err
		}
		var r result
		f, err := cache.Files(sha)
		if err == nil {
			r.cfg, err = config.LoadBytes(f.Paisans)
		}
		r.err = err
		memo[sha] = r
		return r.cfg, r.err
	}
}

// adminKeys maps each admin key a configuration lists, by fingerprint, to
// the admin it belongs to.
func adminKeys(cfg *config.Config) (map[string]string, error) {
	m := map[string]string{}
	for id, admin := range cfg.Admins {
		for _, line := range admin.KeyLines() {
			k, _, err := config.ParseKeyLine(line)
			if err != nil {
				return nil, fmt.Errorf("admins.%s.keys: %w", id, err)
			}
			m[k.Fingerprint] = id
		}
	}
	return m, nil
}

// adminReader is versionConfigs read as Graph.Admins reads it.
func adminReader(configs func(string) (*config.Config, error)) func(sha string) (map[string]string, error) {
	return func(sha string) (map[string]string, error) {
		cfg, err := configs(sha)
		if err != nil {
			return nil, err
		}
		return adminKeys(cfg)
	}
}

// signerID is the admin a version's signing key belongs to, by its first
// parent's admins, or its own for a root; "" when neither lists it.
func signerID(v Version, admins func(string) (map[string]string, error)) string {
	from := v.SHA
	if len(v.Parents) > 0 {
		from = v.Parents[0]
	}
	if m, _ := admins(from); m[v.SignerFP] != "" {
		return m[v.SignerFP]
	}
	m, _ := admins(v.SHA)
	return m[v.SignerFP]
}

// allowedSigners writes git's allowed-signers file: every admin key any held
// version lists, and the operator's own. Being listed only lets git report a
// signature as good; whose signature counts is the trust rules' decision.
func allowedSigners(shas []string, configs func(string) (*config.Config, error), own string) (string, error) {
	lines := map[string]bool{}
	add := func(line string) {
		k, opts, err := config.ParseKeyLine(line)
		if err != nil || len(opts) > 0 {
			return
		}
		f := strings.Fields(k.Line)
		lines[k.Fingerprint+" "+f[0]+" "+f[1]] = true
	}
	for _, sha := range shas {
		cfg, err := configs(sha)
		if err != nil {
			continue
		}
		for _, admin := range cfg.Admins {
			for _, line := range admin.KeyLines() {
				add(line)
			}
		}
	}
	if own != "" {
		add(own)
	}
	dir, err := os.MkdirTemp("", "paisans-signers-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "allowed_signers")
	body := strings.Join(slices.Sorted(maps.Keys(lines)), "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return path, nil
}

// PushError is a publish that some sites did not take. The version is in the
// cache and recorded as the head; a site that missed it gets it from its
// peers' sync, or from publishing again, which pushes without a new version.
type PushError struct {
	SHA    string
	Failed map[string]error
}

func (e *PushError) Error() string {
	var parts []string
	for _, site := range slices.Sorted(maps.Keys(e.Failed)) {
		parts = append(parts, fmt.Sprintf("%s: %v", site, e.Failed[site]))
	}
	return fmt.Sprintf("version %s did not reach %s", e.SHA, strings.Join(parts, "; "))
}

type publishOpts struct {
	keyLine  string // the operator's public key line
	signWith string // what git's user.signingKey is given
	merge    bool
	now      time.Time
}

// Publish commits the three local files as a version whose parent is the
// head, signs it with signingKey (a public key line the ssh-agent holds), and
// pushes the cache to every site that answered. It refuses a base that is
// not the sites' head, a fork, a key that no admin of the parent lists (of
// the local paisans.yaml for the first version), and a run that reaches no
// site. It records the version as the head, the first as the root, and the
// key as the signing key when none is recorded. Unchanged files since the
// head make no new version: the head is pushed again. A rollback refuses
// here; a caller that lets the operator accept one gathers itself and calls
// PublishFrom.
func Publish(l Local, sites map[string]apply.Transport, signingKey string, now time.Time) (string, []string, error) {
	return publish(l, sites, publishOpts{keyLine: signingKey, signWith: "key::" + signingKey, now: now}, Pull, Push)
}

// PublishMerge is Publish with every head as a parent, after the operator
// has merged them by hand. It resolves a fork, and an incoming change alike.
func PublishMerge(l Local, sites map[string]apply.Transport, signingKey string, now time.Time) (string, []string, error) {
	return publish(l, sites, publishOpts{keyLine: signingKey, signWith: "key::" + signingKey, merge: true, now: now}, Pull, Push)
}

func publish(l Local, sites map[string]apply.Transport, o publishOpts, pull PullFunc, push PushFunc) (string, []string, error) {
	f, err := l.Files()
	if err != nil {
		return "", nil, fmt.Errorf("read the files to publish: %w", err)
	}
	local, err := config.LoadBytes(f.Paisans)
	if err != nil {
		return "", nil, err
	}
	a, err := l.Anchor()
	if err != nil {
		return "", nil, err
	}
	g, err := gather(l, sites, len(sites), local.ID, a, GatherOptions{Sync: true, Remember: true}, pull)
	if err != nil {
		return "", nil, err
	}
	return publishFrom(l, g, sites, o, push)
}

// PublishFrom is Publish on a gather the caller has already made, and
// judged: Eg: one whose rollback the operator accepted. signWith is what
// git's user.signingKey is given, "key::" and the key line for the agent.
func PublishFrom(l Local, g Gathered, sites map[string]apply.Transport, keyLine, signWith string, merge bool, now time.Time) (string, []string, error) {
	return publishFrom(l, g, sites, publishOpts{keyLine: keyLine, signWith: signWith, merge: merge, now: now}, Push)
}

func publishFrom(l Local, g Gathered, sites map[string]apply.Transport, o publishOpts, push PushFunc) (string, []string, error) {
	if _, opts, err := config.ParseKeyLine(o.keyLine); err != nil || len(opts) > 0 {
		return "", nil, fmt.Errorf("the signing key is not a bare public key line: %q", o.keyLine)
	}
	f, err := l.Files()
	if err != nil {
		return "", nil, fmt.Errorf("read the files to publish: %w", err)
	}
	local, err := config.LoadBytes(f.Paisans)
	if err != nil {
		return "", nil, err
	}
	head, err := l.Head()
	if err != nil {
		return "", nil, err
	}
	if len(g.Direct()) == 0 {
		return "", nil, fmt.Errorf("no site answered, so nothing was published: %w", UnreachedError(g))
	}
	parents, err := Parents(g, head, o.merge)
	if err != nil {
		return "", nil, err
	}
	cache, err := l.Cache()
	if err != nil {
		return "", nil, err
	}
	if err := CheckSigner(l, parents, local, o.keyLine); err != nil {
		return "", nil, err
	}

	sha := ""
	if !o.merge && head != "" && len(parents) == 1 && parents[0] == head {
		if hf, err := cache.Files(head); err == nil && hf.Equal(f) {
			sha = head
		}
	}
	if sha == "" {
		rev := 1
		for _, p := range parents {
			rev = max(rev, g.Versions[p].Revision+1)
		}
		if sha, err = cache.Commit(f, parents, rev, o.signWith, o.now); err != nil {
			return "", nil, fmt.Errorf("sign the version: %w", err)
		}
		if err := l.raiseSeen(rev, sha); err != nil {
			return "", nil, err
		}
	}
	if err := l.SetHead(sha); err != nil {
		return "", nil, err
	}
	if len(parents) == 0 {
		if err := l.SetRoot(sha); err != nil {
			return "", nil, err
		}
	}
	if own, err := l.SigningKey(); err != nil {
		return "", nil, err
	} else if own == "" {
		if err := l.SetSigningKey(o.keyLine); err != nil {
			return "", nil, err
		}
	}

	bundle, err := cache.Bundle()
	if err != nil {
		return sha, nil, err
	}
	d := deployment.Deployment{ID: local.ID}
	pe := &PushError{SHA: sha, Failed: map[string]error{}}
	var pushed []string
	for _, r := range g.Reached {
		if !r.Direct {
			pe.Failed[r.Site] = r.Err
			continue
		}
		if err := push(sites[r.Site], d, bundle); err != nil {
			pe.Failed[r.Site] = err
			continue
		}
		pushed = append(pushed, r.Site)
	}
	if len(pe.Failed) > 0 {
		return sha, pushed, pe
	}
	return sha, pushed, nil
}

// CheckSigner refuses keyLine unless an admin of every parent lists it, or,
// for the first version (no parents), an admin of the local configuration:
// a version signed by anyone else would not be trusted.
func CheckSigner(l Local, parents []string, local *config.Config, keyLine string) error {
	key, opts, err := config.ParseKeyLine(keyLine)
	if err != nil || len(opts) > 0 {
		return fmt.Errorf("the signing key is not a bare public key line: %q", keyLine)
	}
	if len(parents) == 0 {
		m, err := adminKeys(local)
		if err != nil {
			return err
		}
		if m[key.Fingerprint] == "" {
			return fmt.Errorf("the key %s belongs to no admin the local paisans.yaml lists; add it under your entry in admins", key.Fingerprint)
		}
		return nil
	}
	cache, err := l.Cache()
	if err != nil {
		return err
	}
	admins := adminReader(versionConfigs(cache))
	for _, p := range parents {
		m, err := admins(p)
		if err != nil {
			return fmt.Errorf("read the admins of %s: %w", p, err)
		}
		if m[key.Fingerprint] == "" {
			return fmt.Errorf("the key %s belongs to no admin version %s lists, so a version signed with it would not be trusted; another admin publishes the change that adds it", key.Fingerprint, p)
		}
	}
	return nil
}

// Parents is what a version published from the base head builds on: the
// sites' single head, every head with merge, or none for the first version.
// It refuses a base that is not the sites' head, and a fork without merge.
func Parents(g Gathered, head string, merge bool) ([]string, error) {
	heads := g.Verdict.Heads
	switch {
	case len(heads) == 0 && head != "":
		return nil, fmt.Errorf("your base %s is held by no site that answered and is not in the local store; run paisans config pull", head)
	case len(heads) == 0 && merge:
		return nil, errors.New("there is nothing to merge: no site holds a version")
	case len(heads) == 0:
		return nil, nil
	case merge && (head == "" || !g.Verdict.Trusted[head]):
		return nil, fmt.Errorf("a merge needs a trusted base, and your base %q is not one; run paisans config pull", head)
	case merge:
		return heads, nil
	case g.Verdict.Fork:
		return nil, fmt.Errorf("the sites hold a fork, %s; merge the heads' files by hand and publish with paisans config publish --merge", strings.Join(heads, " and "))
	case head != heads[0]:
		v := g.Versions[heads[0]]
		signer := g.Signers[heads[0]]
		if signer == "" {
			signer = v.SignerFP
		}
		return nil, fmt.Errorf("stale: the sites hold %s (revision %d, signed by %s); run paisans config pull", heads[0], v.Revision, signer)
	}
	return heads[:1], nil
}

// UnreachedError is why each site a gather tried did not answer.
func UnreachedError(g Gathered) error {
	var errs []error
	for _, r := range g.Reached {
		if r.Err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Site, r.Err))
		}
	}
	if len(errs) == 0 {
		return errors.New("no site was given")
	}
	return errors.Join(errs...)
}
