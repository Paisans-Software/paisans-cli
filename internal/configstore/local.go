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

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// Local is an operator's workstation: the directory holding paisans-stack.yaml,
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
	stateIgnore   = "store.git/\nincoming/\n" + signingFile + "\n" + headFile + "\n" + seenFile + "\n"
	stateFileMode = 0o644
)

func (l Local) state(name string) string { return filepath.Join(l.Dir, stateDir, name) }

// ensureState creates .paisans/ and its .gitignore, which keeps this
// operator's own state out of the repository the operators share: the cache,
// this workstation's memory of every version it has seen; the incoming
// files, a pull's scratch; the signing key, since a workstation that
// checked out another operator's would anchor its trust on their signature;
// and the head and the seen revision, which describe this workstation's
// files and gathers. config-root is left out, so the operators can commit the
// pin and share it. A .gitignore already there is the operator's, and is
// left as it is.
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

// acceptRollback records revision rev at sha as the newest seen, even when
// it is lower than what is recorded. It is the one way the record goes down,
// and only because the operator typed rollback for that head: they chose to
// go on from it, and every later gather is judged against that choice.
func (l Local) acceptRollback(rev int, sha string) error {
	if rev < 1 {
		return fmt.Errorf("revision %d is not one", rev)
	}
	if err := checkSHA(sha); err != nil {
		return err
	}
	return l.writeState(seenFile, fmt.Sprintf("%d %s", rev, sha))
}

// SeenChange is the change to .paisans/config-seen a gather leads to:
// raising the record to a newer trusted head, or, with Lower, setting it to
// an older head the operator accepted by typing rollback. A gather only
// carries it; RecordSeen writes it, which a command does once its last
// confirmation has passed and it commits its change, so a run that stops
// leaves the record as it was.
type SeenChange struct {
	Revision int
	SHA      string
	Lower    bool
}

// RecordSeen writes the change to the seen record g carries, if any.
func (l Local) RecordSeen(g Gathered) error {
	switch {
	case g.Seen == nil:
		return nil
	case g.Seen.Lower:
		return l.acceptRollback(g.Seen.Revision, g.Seen.SHA)
	}
	return l.raiseSeen(g.Seen.Revision, g.Seen.SHA)
}

// AcceptRollback makes g carry the older head rb names as the record's new
// value, once the operator has typed rollback for it.
func (g *Gathered) AcceptRollback(rb *RollbackError) {
	g.Seen = &SeenChange{Revision: rb.Revision, SHA: rb.SHA, Lower: true}
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

// Current is the version the files beside paisans-stack.yaml are, as this
// workstation alone knows it: its head and the head's revision, when the
// files on disk are that version's. It reaches no site, so a run can name
// what it applies before it asks for anything. An empty sha is files that
// are no version this workstation holds: none published yet, or edited
// since the last.
func (l Local) Current() (sha string, revision int, err error) {
	head, err := l.Head()
	if err != nil || head == "" {
		return "", 0, err
	}
	files, err := l.Files()
	if err != nil {
		return "", 0, err
	}
	cache, err := l.Cache()
	if err != nil {
		return "", 0, err
	}
	held, err := cache.Files(head)
	if err != nil || !held.Equal(files) {
		return "", 0, err
	}
	return head, cache.revision(head), nil
}

func (l Local) path(name string) string { return filepath.Join(l.Dir, name) }

// Files reads the three files beside paisans-stack.yaml.
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
	if SnapshotBlueprints != nil {
		snap, err := SnapshotBlueprints(f.Paisans)
		if err != nil {
			return Files{}, err
		}
		if len(snap) > 0 {
			f.Blueprints = snap
		}
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

// WriteFiles writes the three files beside paisans-stack.yaml, each replaced whole.
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
	if len(f.Blueprints) > 0 && InstallBlueprints != nil {
		return InstallBlueprints(f.Blueprints)
	}
	return nil
}

// Checkout writes the version sha's three files beside paisans-stack.yaml and
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

// Equal reports whether two sets of the three files, and their blueprint
// snapshots, are byte for byte the same.
func (f Files) Equal(o Files) bool {
	if !bytes.Equal(f.Paisans, o.Paisans) || !bytes.Equal(f.Secrets, o.Secrets) || !bytes.Equal(f.SOPS, o.SOPS) || len(f.Blueprints) != len(o.Blueprints) {
		return false
	}
	for p, data := range f.Blueprints {
		if other, ok := o.Blueprints[p]; !ok || !bytes.Equal(data, other) {
			return false
		}
	}
	return true
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
	Signers  map[string]string   // version -> the operator id its key belongs to, "" when none
	// Seen is the change to the seen record this gather leads to, nil for
	// none; Local.RecordSeen writes it.
	Seen *SeenChange
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

// GatherOptions say what a gather may change. Sync asks each site to sync
// with its peers first, which changes the site, so only a run with --execute
// sets it.
type GatherOptions struct {
	Sync bool
}

// GatherAnchored collects every version the sites hold into the cache and
// judges them from the anchor a. configured is how many sites the
// configuration declares, which the majority is counted against.
//
// A head older than the newest this workstation has seen is a
// *RollbackError, returned with the complete Gathered; otherwise a head
// newer than the record is carried in Gathered.Seen, for the command to
// record once it commits. A gather never writes the record itself.
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
	operators := operatorReader(configs)
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
		g.Signers[sha] = signerID(v, operators)
	}
	if len(g.Versions) == 0 {
		g.Verdict = Verdict{Trusted: map[string]bool{}, Reasons: map[string]string{}}
		return g, nil
	}
	g.Verdict, err = Judge(Graph{Versions: g.Versions, Operators: operators}, a)
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
	if rev > seenRev {
		g.Seen = &SeenChange{Revision: rev, SHA: top}
	}
	return g, nil
}

// versionConfigs reads a version's paisans-stack.yaml, decoded as strictly as
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

// operatorKeys maps each operator key a configuration lists, by fingerprint, to
// the operator it belongs to.
func operatorKeys(cfg *config.Config) (map[string]string, error) {
	m := map[string]string{}
	for id, operator := range cfg.Operators {
		for _, line := range operator.KeyLines() {
			k, _, err := config.ParseKeyLine(line)
			if err != nil {
				return nil, fmt.Errorf("operators.%s.keys: %w", id, err)
			}
			m[k.Fingerprint] = id
		}
	}
	return m, nil
}

// operatorReader is versionConfigs read as Graph.Operators reads it.
func operatorReader(configs func(string) (*config.Config, error)) func(sha string) (map[string]string, error) {
	return func(sha string) (map[string]string, error) {
		cfg, err := configs(sha)
		if err != nil {
			return nil, err
		}
		return operatorKeys(cfg)
	}
}

// signerID is the operator a version's signing key belongs to, by its first
// parent's operators, or its own for a root; "" when neither lists it.
func signerID(v Version, operators func(string) (map[string]string, error)) string {
	from := v.SHA
	if len(v.Parents) > 0 {
		from = v.Parents[0]
	}
	if m, _ := operators(from); m[v.SignerFP] != "" {
		return m[v.SignerFP]
	}
	m, _ := operators(v.SHA)
	return m[v.SignerFP]
}

// allowedSigners writes git's allowed-signers file: every operator key any held
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
		for _, operator := range cfg.Operators {
			for _, line := range operator.KeyLines() {
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

// PublishFrom commits the three local files as a version whose parent is
// the head, on a gather the caller has already made and judged (Eg: one
// whose rollback the operator accepted), signs it with keyLine (a public key
// line the ssh-agent holds), and pushes the cache to every site that
// answered. It refuses a base that is not the sites' head, a fork, a key
// that no operator of the parent lists (of the local paisans-stack.yaml for the first
// version), and a run that reaches no site. It records the change to the
// seen record the gather carries, the version as the head, the first as the
// root, and the key as the signing key when none is recorded. Unchanged
// files since the head make no new version: the head is pushed again.
// signWith is what git's user.signingKey is given, "key::" and the key line
// for the agent.
func PublishFrom(l Local, g Gathered, sites map[string]apply.Transport, keyLine, signWith string, merge bool, now time.Time) (string, []string, error) {
	return publishFrom(l, g, sites, publishOpts{keyLine: keyLine, signWith: signWith, merge: merge, now: now}, Push)
}

// PublishFiles is PublishFrom with the files to publish given, read by the
// caller, rather than read from disk now: a command that applies the files
// it read earlier publishes exactly those, whatever is on disk by the time
// it publishes.
func PublishFiles(l Local, g Gathered, f Files, sites map[string]apply.Transport, keyLine, signWith string, merge bool, now time.Time) (string, []string, error) {
	return publishFiles(l, g, f, sites, publishOpts{keyLine: keyLine, signWith: signWith, merge: merge, now: now}, Push)
}

func publishFrom(l Local, g Gathered, sites map[string]apply.Transport, o publishOpts, push PushFunc) (string, []string, error) {
	f, err := l.Files()
	if err != nil {
		return "", nil, fmt.Errorf("read the files to publish: %w", err)
	}
	return publishFiles(l, g, f, sites, o, push)
}

func publishFiles(l Local, g Gathered, f Files, sites map[string]apply.Transport, o publishOpts, push PushFunc) (string, []string, error) {
	// A version holds the secrets encrypted, and only encrypted.
	if err := config.CheckSecretsBytes(l.path(nameSecrets), f.Secrets); err != nil {
		return "", nil, err
	}
	if _, opts, err := config.ParseKeyLine(o.keyLine); err != nil || len(opts) > 0 {
		return "", nil, fmt.Errorf("the signing key is not a bare public key line: %q", o.keyLine)
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

	if err := l.RecordSeen(g); err != nil {
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

	pushed, err := pushVersion(cache, g, sites, local.ID, sha, push)
	return sha, pushed, err
}

// PublishNext commits f as the version after parent, at revision+1, where
// parent is the version at revision the caller published from g earlier in
// the same apply, signs it with keyLine, and pushes the cache to every site g
// reached directly. It is how apply publishes what it wrote to the files
// after publishing the version it applies: the sites hold parent as their
// head because the apply put it there, so nothing is gathered again. It refuses when the local head is not parent, and publishes nothing
// new for files that are parent's.
func PublishNext(l Local, g Gathered, parent string, revision int, f Files, sites map[string]apply.Transport, keyLine, signWith string, now time.Time) (string, []string, error) {
	return publishNext(l, g, parent, revision, f, sites, publishOpts{keyLine: keyLine, signWith: signWith, now: now}, Push)
}

func publishNext(l Local, g Gathered, parent string, revision int, f Files, sites map[string]apply.Transport, o publishOpts, push PushFunc) (string, []string, error) {
	// A version holds the secrets encrypted, and only encrypted.
	if err := config.CheckSecretsBytes(l.path(nameSecrets), f.Secrets); err != nil {
		return "", nil, err
	}
	if _, opts, err := config.ParseKeyLine(o.keyLine); err != nil || len(opts) > 0 {
		return "", nil, fmt.Errorf("the signing key is not a bare public key line: %q", o.keyLine)
	}
	local, err := config.LoadBytes(f.Paisans)
	if err != nil {
		return "", nil, err
	}
	head, err := l.Head()
	if err != nil {
		return "", nil, err
	}
	if head != parent {
		return "", nil, fmt.Errorf("the local head is %q, not %s, the version this run published, so nothing was published on it", head, parent)
	}
	cache, err := l.Cache()
	if err != nil {
		return "", nil, err
	}
	if err := CheckSigner(l, []string{parent}, local, o.keyLine); err != nil {
		return "", nil, err
	}
	sha := parent
	if pf, err := cache.Files(parent); err != nil || !pf.Equal(f) {
		rev := revision + 1
		if sha, err = cache.Commit(f, []string{parent}, rev, o.signWith, o.now); err != nil {
			return "", nil, fmt.Errorf("sign the version: %w", err)
		}
		if err := l.raiseSeen(rev, sha); err != nil {
			return "", nil, err
		}
		if err := l.SetHead(sha); err != nil {
			return "", nil, err
		}
	}
	pushed, err := pushVersion(cache, g, sites, local.ID, sha, push)
	return sha, pushed, err
}

// pushVersion pushes the cache, which holds sha, to every site g reached
// directly, and returns the sites that took it. Any that did not, or were
// not reached, make a *PushError.
func pushVersion(cache Repo, g Gathered, sites map[string]apply.Transport, id, sha string, push PushFunc) ([]string, error) {
	bundle, err := cache.Bundle()
	if err != nil {
		return nil, err
	}
	d := deployment.Deployment{ID: id}
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
		return pushed, pe
	}
	return pushed, nil
}

// CheckSigner refuses keyLine unless an operator of every parent lists it, or,
// for the first version (no parents), an operator of the local configuration:
// a version signed by anyone else would not be trusted.
func CheckSigner(l Local, parents []string, local *config.Config, keyLine string) error {
	key, opts, err := config.ParseKeyLine(keyLine)
	if err != nil || len(opts) > 0 {
		return fmt.Errorf("the signing key is not a bare public key line: %q", keyLine)
	}
	if len(parents) == 0 {
		m, err := operatorKeys(local)
		if err != nil {
			return err
		}
		if m[key.Fingerprint] == "" {
			return fmt.Errorf("the key %s belongs to no operator the local "+config.FileName+" lists; add it under your entry in operators", key.Fingerprint)
		}
		return nil
	}
	cache, err := l.Cache()
	if err != nil {
		return err
	}
	operators := operatorReader(versionConfigs(cache))
	for _, p := range parents {
		m, err := operators(p)
		if err != nil {
			return fmt.Errorf("read the operators of %s: %w", p, err)
		}
		if m[key.Fingerprint] == "" {
			return fmt.Errorf("the key %s belongs to no operator version %s lists, so a version signed with it would not be trusted; another operator publishes the change that adds it", key.Fingerprint, p)
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

// DiffersError is local files that are not the sites' newest version. An
// attestation says the operator holds that version's files, which they have
// checked; files that differ are not that check.
type DiffersError struct {
	Head          string
	Local, Theirs Files
}

func (e *DiffersError) Error() string {
	return fmt.Sprintf("the local files differ from the sites' newest version %s, and only a version equal to the files you hold is attested", e.Head)
}

// AttestTarget is the version an attestation signs: the single head of g,
// which must equal the local files byte for byte, since those are the copy
// the operator already trusts. With no local files at all it is the head the
// pinned root pin leads to, and write is true: the attestation writes the
// head's files, as a recovery does. It refuses a fork, no head, files that
// differ (a *DiffersError) and, with no local files, no pin.
func AttestTarget(l Local, g Gathered, pin string) (head string, write bool, err error) {
	heads := g.Verdict.Heads
	switch {
	case len(heads) == 0:
		return "", false, errors.New("the sites hold no version to attest; paisans config publish makes the first")
	case g.Verdict.Fork:
		return "", false, fmt.Errorf("the sites hold a fork, %s; merge the heads' files by hand and publish with paisans config publish --merge", strings.Join(heads, " and "))
	}
	head = heads[0]
	if !l.HasFiles() {
		if pin == "" || !slices.Contains(g.Roots(head), pin) {
			return "", false, errors.New("there are no local files to compare the sites' newest version with, and no pinned root it descends from; pin the first version you trust with --root <sha>")
		}
		return head, true, nil
	}
	local, err := l.Files()
	if err != nil {
		return "", false, fmt.Errorf("read the local files: %w", err)
	}
	cache, err := l.Cache()
	if err != nil {
		return "", false, err
	}
	theirs, err := cache.Files(head)
	if err != nil {
		return "", false, err
	}
	if !local.Equal(theirs) {
		return "", false, &DiffersError{Head: head, Local: local, Theirs: theirs}
	}
	return head, false, nil
}

// AttestFrom signs an attestation of the head of a gather the caller has
// already made and judged, from the anchor of the pinned root pin and the
// key: a version whose tree is exactly the head's and whose only parent is
// the head, one revision on. It pushes it to every site that answered and
// records the change to the seen record the gather carries, the key, the
// attestation as the head, and the root when none is pinned, so the
// attestation is the operator's anchor from then on. It changes nothing on
// a site but its store. signWith is what git's user.signingKey is given,
// "key::" and the key line for the agent. A head the key signed already is
// pushed again rather than attested twice, so running it again resumes a
// push some sites missed.
func AttestFrom(l Local, g Gathered, sites map[string]apply.Transport, pin, keyLine, signWith string, now time.Time) (string, []string, error) {
	return attestFrom(l, g, sites, pin, keyLine, signWith, now, Push)
}

func attestFrom(l Local, g Gathered, sites map[string]apply.Transport, pin, keyLine, signWith string, now time.Time, push PushFunc) (string, []string, error) {
	key, opts, err := config.ParseKeyLine(keyLine)
	if err != nil || len(opts) > 0 {
		return "", nil, fmt.Errorf("the signing key is not a bare public key line: %q", keyLine)
	}
	if len(g.Direct()) == 0 {
		return "", nil, fmt.Errorf("no site answered, so nothing was attested: %w", UnreachedError(g))
	}
	head, write, err := AttestTarget(l, g, pin)
	if err != nil {
		return "", nil, err
	}
	cache, err := l.Cache()
	if err != nil {
		return "", nil, err
	}
	theirs, err := cache.Files(head)
	if err != nil {
		return "", nil, err
	}
	cfg, err := config.LoadBytes(theirs.Paisans)
	if err != nil {
		return "", nil, fmt.Errorf("version %s: %w", head, err)
	}
	if err := CheckSigner(l, []string{head}, cfg, keyLine); err != nil {
		return "", nil, err
	}
	if err := l.RecordSeen(g); err != nil {
		return "", nil, err
	}
	sha := head
	if g.Versions[head].SignerFP != key.Fingerprint {
		rev := g.Versions[head].Revision + 1
		if sha, err = cache.Commit(theirs, []string{head}, rev, signWith, now); err != nil {
			return "", nil, fmt.Errorf("sign the attestation: %w", err)
		}
		if err := l.raiseSeen(rev, sha); err != nil {
			return "", nil, err
		}
	}
	if write {
		if err := l.WriteFiles(theirs); err != nil {
			return "", nil, err
		}
	}
	if err := l.SetSigningKey(key.Line); err != nil {
		return "", nil, err
	}
	if err := l.SetHead(sha); err != nil {
		return "", nil, err
	}
	if recorded, err := l.Root(); err != nil {
		return "", nil, err
	} else if recorded == "" {
		root := pin
		if roots := g.Roots(head); root == "" && len(roots) == 1 {
			root = roots[0]
		}
		if root != "" {
			if err := l.SetRoot(root); err != nil {
				return "", nil, err
			}
		}
	}
	pushed, err := pushVersion(cache, g, sites, cfg.ID, sha, push)
	return sha, pushed, err
}
