package configstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// The three files of a version, by their names in the tree, sorted as git's
// mktree requires.
const (
	nameSOPS    = ".sops.yaml"
	namePaisans = config.FileName
	nameSecrets = "secrets.enc.yaml"
)

var shaRE = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

func checkSHA(sha string) error {
	if !shaRE.MatchString(sha) {
		return fmt.Errorf("%q is not a full lowercase hex commit id", sha)
	}
	return nil
}

// Repo is a bare git repository holding one commit per version, each named by
// refs/versions/<commit sha>.
type Repo struct{ Dir string }

// Version is one commit of the store and what git says about it.
type Version struct {
	SHA      string
	Parents  []string
	Revision int // from the Paisans-Revision trailer; 0 when absent or malformed
	// Signed is true only when git reports a good signature from a key in the
	// allowed-signers file (%G? is G). A valid signature by an unlisted key
	// (U) is not trusted.
	Signed    bool
	SignerFP  string    // %GF, the signing key's SHA256 fingerprint
	Committed time.Time // for display only
	TreeOK    bool      // the tree holds exactly the three files, and a sound snapshot
	// Build is the paisans that published the version, from its
	// Paisans-Toolkit-Version, Paisans-Toolkit-Commit and
	// Paisans-Toolkit-Modified trailers, which its signature covers.
	// BuildOK is whether the message holds each of them once, as Commit
	// writes them; a version without is refused as shape.
	// BuildNewer is whether they would, but for a version this paisans
	// cannot order: a newer paisans may have published it.
	Build      version.Build
	BuildOK    bool
	BuildNewer bool
}

// git runs git in the repository with the user's and the system's
// configuration shut out, and every GIT_ variable of the caller dropped (a
// stray GIT_DIR would point it at another repository), so that only what this
// package passes applies.
func (r Repo) git(stdin io.Reader, env []string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", r.Dir}, args...)...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=0", "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		sub := args
		for len(sub) > 1 && sub[0] == "-c" {
			sub = sub[2:]
		}
		return nil, fmt.Errorf("git %s: %w: %s", sub[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// InitRepo creates the bare repository at dir if it is not there, and makes it
// refuse deletions and non-fast-forward updates when pushed to.
func InitRepo(dir string) (Repo, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Repo{}, err
	}
	r := Repo{Dir: dir}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		if !os.IsNotExist(err) {
			return Repo{}, err
		}
		if _, err := r.git(nil, nil, "init", "--bare", "--quiet"); err != nil {
			return Repo{}, err
		}
	}
	for _, kv := range [][2]string{{"receive.denyDeletes", "true"}, {"receive.denyNonFastForwards", "true"}} {
		if _, err := r.git(nil, nil, "config", kv[0], kv[1]); err != nil {
			return Repo{}, err
		}
	}
	return r, nil
}

// Commit stores f as a signed commit on the given parents, records its
// revision and this build of paisans in trailers, which the signature
// covers, and names it with refs/versions/<sha>.
func (r Repo) Commit(f Files, parents []string, revision int, signingKey string, when time.Time) (string, error) {
	for _, p := range parents {
		if err := checkSHA(p); err != nil {
			return "", err
		}
	}
	tree, err := r.writeTree(f)
	if err != nil {
		return "", err
	}
	args := []string{
		"-c", "gpg.format=ssh", "-c", "user.signingKey=" + signingKey,
		"-c", "user.name=paisans", "-c", "user.email=paisans@invalid",
		"commit-tree", tree, "-S",
	}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	args = append(args, "-m", fmt.Sprintf("paisans configuration\n\nPaisans-Revision: %d\n%s", revision, buildTrailers(version.Current())))
	date := when.UTC().Format(time.RFC3339)
	out, err := r.git(nil, []string{"GIT_COMMITTER_DATE=" + date, "GIT_AUTHOR_DATE=" + date}, args...)
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(out))
	if _, err := r.git(nil, nil, "update-ref", "refs/versions/"+sha, sha); err != nil {
		return "", err
	}
	return sha, nil
}

// writeTree stores f's files and returns its tree: the three files at the
// top, and the blueprint snapshot under blueprints/ with blueprints/SHA256SUMS
// beside it. A version with no snapshot has exactly the three files, as every
// version before snapshots did.
func (r Repo) writeTree(f Files) (string, error) {
	entries := []struct {
		path string
		data []byte
	}{{nameSOPS, f.SOPS}, {namePaisans, f.Paisans}, {nameSecrets, f.Secrets}}
	if len(f.Blueprints) > 0 {
		for _, p := range sortedPaths(f.Blueprints) {
			if !snapshotPath.MatchString(p) {
				return "", fmt.Errorf("blueprints/%s is not a path a snapshot holds", p)
			}
			entries = append(entries, struct {
				path string
				data []byte
			}{blueprintsDir + "/" + p, f.Blueprints[p]})
		}
		entries = append(entries, struct {
			path string
			data []byte
		}{blueprintsDir + "/" + sumsFile, snapshotSums(f.Blueprints)})
	}
	index, err := os.CreateTemp("", "paisans-index-")
	if err != nil {
		return "", err
	}
	index.Close()
	os.Remove(index.Name())
	defer os.Remove(index.Name())
	env := []string{"GIT_INDEX_FILE=" + index.Name()}
	// One git for every blob and one for the index, since a snapshot holds
	// every file of every blueprint the apps use.
	staged, err := os.MkdirTemp("", "paisans-blobs-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staged)
	var paths bytes.Buffer
	for i, e := range entries {
		p := filepath.Join(staged, strconv.Itoa(i))
		if err := os.WriteFile(p, e.data, 0o600); err != nil {
			return "", err
		}
		paths.WriteString(p + "\n")
	}
	out, err := r.git(&paths, nil, "hash-object", "-w", "--stdin-paths")
	if err != nil {
		return "", err
	}
	oids := strings.Fields(string(out))
	if len(oids) != len(entries) {
		return "", fmt.Errorf("git hash-object stored %d of %d files", len(oids), len(entries))
	}
	var info bytes.Buffer
	for i, e := range entries {
		fmt.Fprintf(&info, "100644 %s\t%s\n", oids[i], e.path)
	}
	if _, err := r.git(&info, env, "update-index", "--add", "--index-info"); err != nil {
		return "", err
	}
	out, err = r.git(nil, env, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// blueprintsDir is where a version keeps its blueprint snapshot, and sumsFile
// the SHA-256 of each blueprint in it.
const (
	blueprintsDir = "blueprints"
	sumsFile      = "SHA256SUMS"
)

// snapshotPath is the one shape a snapshot path may have:
// <catalog commit>/<blueprint>/<file>, with no empty, dot or dot-dot part,
// or <core catalog commit>/foundation.yaml, the core catalog's own images.
var snapshotPath = regexp.MustCompile(`^[0-9a-f]{40}/([a-z0-9][a-z0-9-]{0,62}/([A-Za-z0-9_.+@-]+/)*[A-Za-z0-9_.+@-]+|foundation\.yaml)$`)

// snapshotSums is blueprints/SHA256SUMS: for each blueprint in the snapshot,
// in order, its SHA-256 as blueprints.DigestFiles computes it and its
// <commit>/<name>. Git names a version's files by SHA-1, so this is what a
// snapshot's integrity rests on beyond the signature.
func snapshotSums(files map[string][]byte) []byte {
	groups := map[string]map[string][]byte{}
	for p, data := range files {
		parts := strings.SplitN(p, "/", 3)
		// The foundation is a file of the commit, not of a blueprint, and
		// is summed as the commit's own.
		dir, file := parts[0], parts[1]
		if len(parts) == 3 {
			dir, file = parts[0]+"/"+parts[1], parts[2]
		}
		if groups[dir] == nil {
			groups[dir] = map[string][]byte{}
		}
		groups[dir][file] = data
	}
	var b strings.Builder
	for _, dir := range sortedPaths(groups) {
		fmt.Fprintf(&b, "%s  %s\n", blueprints.DigestFiles(groups[dir]), dir)
	}
	return []byte(b.String())
}

func sortedPaths[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// versionRefs returns the commits that refs/versions/ names correctly.
func (r Repo) versionRefs() ([]string, error) { return r.namedVersions("refs/versions/") }

// namedVersions returns the commits refs under prefix name correctly: a ref
// prefix<sha> is named for the commit sha it points to. One that is not, or
// that points at anything but a commit, is not a version and is skipped.
func (r Repo) namedVersions(prefix string) ([]string, error) {
	out, err := r.git(nil, nil, "for-each-ref", "--format=%(refname) %(objectname) %(objecttype)", prefix)
	if err != nil {
		return nil, err
	}
	var shas []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[2] != "commit" || f[0] != prefix+f[1] || checkSHA(f[1]) != nil {
			continue
		}
		shas = append(shas, f[1])
	}
	return shas, nil
}

// treeOK reports whether the tree of sha holds exactly the three files, each a
// regular non-executable blob, and at most a blueprint snapshot beside them
// whose every file has the one shape a snapshot path may have and whose
// SHA256SUMS matches what it holds. Entries are read NUL-delimited so that no
// name can pass for several.
func (r Repo) treeOK(sha string) (bool, error) {
	out, err := r.git(nil, nil, "ls-tree", "-r", "-z", sha)
	if err != nil {
		return false, err
	}
	want := map[string]bool{nameSOPS: true, namePaisans: true, nameSecrets: true}
	snapshot := map[string]string{}
	hasSums := false
	for _, e := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		meta, name, ok := strings.Cut(e, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 || f[0] != "100644" || f[1] != "blob" {
			return false, nil
		}
		switch {
		case want[name]:
			delete(want, name)
		case name == blueprintsDir+"/"+sumsFile:
			hasSums = true
		case strings.HasPrefix(name, blueprintsDir+"/") && snapshotPath.MatchString(strings.TrimPrefix(name, blueprintsDir+"/")):
			snapshot[strings.TrimPrefix(name, blueprintsDir+"/")] = f[2]
		default:
			return false, nil
		}
	}
	if len(want) != 0 || hasSums != (len(snapshot) > 0) {
		return false, nil
	}
	if len(snapshot) == 0 {
		return true, nil
	}
	files, err := r.snapshot(sha)
	if err != nil {
		return false, err
	}
	sums, err := r.git(nil, nil, "cat-file", "blob", sha+":"+blueprintsDir+"/"+sumsFile)
	if err != nil {
		return false, err
	}
	return bytes.Equal(sums, snapshotSums(files)), nil
}

// snapshot reads the blueprint snapshot of the version sha, empty when it has
// none.
func (r Repo) snapshot(sha string) (map[string][]byte, error) {
	out, err := r.git(nil, nil, "ls-tree", "-r", "-z", "--name-only", sha, blueprintsDir+"/")
	if err != nil {
		return nil, err
	}
	var names []string
	var want bytes.Buffer
	for _, name := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		rel := strings.TrimPrefix(name, blueprintsDir+"/")
		if name == "" || rel == sumsFile || !snapshotPath.MatchString(rel) {
			continue
		}
		names = append(names, rel)
		want.WriteString(sha + ":" + name + "\n")
	}
	files := map[string][]byte{}
	if len(names) == 0 {
		return files, nil
	}
	// One git for every file, since a snapshot holds every file of every
	// blueprint the apps use. snapshotPath keeps a newline out of a name, so
	// each is one request.
	out, err = r.git(&want, nil, "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	for _, rel := range names {
		header, rest, ok := bytes.Cut(out, []byte("\n"))
		f := strings.Fields(string(header))
		if !ok || len(f) != 3 || f[1] != "blob" {
			return nil, fmt.Errorf("reading blueprints/%s from version %s: %s", rel, sha, header)
		}
		size, err := strconv.Atoi(f[2])
		if err != nil || size < 0 || len(rest) < size+1 {
			return nil, fmt.Errorf("reading blueprints/%s from version %s: a short answer", rel, sha)
		}
		files[rel] = append([]byte(nil), rest[:size]...)
		out = rest[size+1:]
	}
	return files, nil
}

// revision reads the Paisans-Revision trailer of a commit on its own, so that
// a hostile message can spoil only its own version. Anything but one positive
// number is 0.
func (r Repo) revision(sha string) int {
	out, err := r.git(nil, nil, "show", "-s", "--format=%(trailers:key=Paisans-Revision,valueonly)", sha)
	if err != nil {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil && n > 0 {
		return n
	}
	return 0
}

// buildTrailers is b as the three trailers Commit writes, each on its own
// line.
func buildTrailers(b version.Build) string {
	return fmt.Sprintf("Paisans-Toolkit-Version: %s\nPaisans-Toolkit-Commit: %s\nPaisans-Toolkit-Modified: %t\n", b.Version, b.Commit, b.Modified)
}

// build reads the build trailers of a commit on its own, as revision reads
// its revision, and judges them with version.CheckBuild, the check every
// record's build passes. ok is whether each trailer is there once and the
// three make a build paisans records; newer is whether they would, but for
// a version this paisans cannot order.
func (r Repo) build(sha string) (b version.Build, ok, newer bool) {
	out, err := r.git(nil, nil, "show", "-s", "--format=%(trailers:key=Paisans-Toolkit-Version,valueonly)%x00%(trailers:key=Paisans-Toolkit-Commit,valueonly)%x00%(trailers:key=Paisans-Toolkit-Modified,valueonly)", sha)
	if err != nil {
		return version.Build{}, false, false
	}
	fields := strings.Split(strings.TrimSuffix(string(out), "\n"), "\x00")
	if len(fields) != 3 {
		return version.Build{}, false, false
	}
	for i, f := range fields {
		f, ok := strings.CutSuffix(f, "\n")
		if !ok || strings.Contains(f, "\n") {
			return version.Build{}, false, false
		}
		fields[i] = f
	}
	if fields[2] != "true" && fields[2] != "false" {
		return version.Build{}, false, false
	}
	obj := map[string]json.RawMessage{"toolkit_modified": json.RawMessage(fields[2])}
	for i, k := range []string{"toolkit_version", "toolkit_commit"} {
		raw, err := json.Marshal(fields[i])
		if err != nil {
			return version.Build{}, false, false
		}
		obj[k] = raw
	}
	b, err = version.CheckBuild(obj)
	switch {
	case err == nil:
		return b, true, false
	case version.IsNewer(err):
		return b, false, true
	}
	return version.Build{}, false, false
}

// Versions lists every version in the repository with git's verdict on its
// signature, checked against the allowed-signers file at allowedSigners.
func (r Repo) Versions(allowedSigners string) ([]Version, error) {
	shas, err := r.versionRefs()
	if err != nil || len(shas) == 0 {
		return nil, err
	}
	// Only fixed-shape fields come from the log; nothing an author wrote.
	const format = "%H%x00%P%x00%G?%x00%GF%x00%ct"
	args := append([]string{"-c", "gpg.ssh.allowedSignersFile=" + allowedSigners,
		"log", "--no-walk=unsorted", "--format=" + format}, shas...)
	out, err := r.git(nil, nil, args...)
	if err != nil {
		return nil, err
	}
	var vs []Version
	for _, rec := range strings.Split(string(out), "\n") {
		fields := strings.Split(rec, "\x00")
		if len(fields) != 5 || checkSHA(fields[0]) != nil {
			continue
		}
		v := Version{SHA: fields[0], Parents: strings.Fields(fields[1]), Signed: fields[2] == "G", SignerFP: fields[3]}
		if secs, err := strconv.ParseInt(fields[4], 10, 64); err == nil {
			v.Committed = time.Unix(secs, 0).UTC()
		}
		v.Revision = r.revision(v.SHA)
		v.Build, v.BuildOK, v.BuildNewer = r.build(v.SHA)
		if v.TreeOK, err = r.treeOK(v.SHA); err != nil {
			return nil, err
		}
		vs = append(vs, v)
	}
	return vs, nil
}

// Files reads the three files of the version sha.
func (r Repo) Files(sha string) (Files, error) {
	if err := checkSHA(sha); err != nil {
		return Files{}, err
	}
	var f Files
	for _, e := range []struct {
		name string
		dst  *[]byte
	}{{namePaisans, &f.Paisans}, {nameSecrets, &f.Secrets}, {nameSOPS, &f.SOPS}} {
		out, err := r.git(nil, nil, "cat-file", "blob", sha+":"+e.name)
		if err != nil {
			return Files{}, err
		}
		*e.dst = out
	}
	snap, err := r.snapshot(sha)
	if err != nil {
		return Files{}, err
	}
	if len(snap) > 0 {
		f.Blueprints = snap
	}
	return f, nil
}

// Bundle packs every version the repository holds, by the rule versionRefs
// lists them by; it is nil when there are none. A misnamed ref is never
// packed, so it cannot travel to another store.
func (r Repo) Bundle() ([]byte, error) {
	shas, err := r.versionRefs()
	if err != nil || len(shas) == 0 {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "paisans-bundle-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "b.bundle")
	var names strings.Builder
	for _, sha := range shas {
		names.WriteString("refs/versions/" + sha + "\n")
	}
	if _, err := r.git(strings.NewReader(names.String()), nil, "bundle", "create", "--quiet", path, "--stdin"); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// FetchBundle adds the versions in a bundle after git has verified it, by
// the receive rule hostprep.ConfigReceiveShell's paisans_receive follows on a site.
// A fetch into refs/versions/ would fast-forward a version's ref to a child
// of it, even without a plus in the refspec, so the bundle is fetched into a
// quarantine, refs/incoming/<random>/, and each version it names correctly
// is created where it is absent, against the null hash, so an existing ref
// never moves. Every other ref is ignored. Each version is created on its
// own, so one that fails leaves the rest; the quarantine is deleted after.
func (r Repo) FetchBundle(bundle []byte) error {
	dir, err := os.MkdirTemp("", "paisans-bundle-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "b.bundle")
	if err := os.WriteFile(path, bundle, 0o600); err != nil {
		return err
	}
	if _, err := r.git(nil, nil, "bundle", "verify", path); err != nil {
		return err
	}
	q := "refs/incoming/" + filepath.Base(dir) + "/"
	defer r.dropRefs(q)
	_, fetchErr := r.git(nil, nil, "fetch", "--no-prune", "--no-tags", "--quiet", path, "+refs/versions/*:"+q+"*")
	shas, err := r.namedVersions(q)
	if err != nil {
		return err
	}
	null, err := r.nullOID()
	if err != nil {
		return err
	}
	for _, sha := range shas {
		// An existing ref makes this fail, which is the point.
		r.git(nil, nil, "update-ref", "refs/versions/"+sha, sha, null)
	}
	return fetchErr
}

// nullOID is the all-zero hash in the repository's object format, which
// update-ref takes as "this ref must not exist".
func (r Repo) nullOID() (string, error) {
	out, err := r.git(nil, nil, "rev-parse", "--show-object-format")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(out)) == "sha256" {
		return strings.Repeat("0", 64), nil
	}
	return strings.Repeat("0", 40), nil
}

// dropRefs deletes every ref under prefix.
func (r Repo) dropRefs(prefix string) {
	out, err := r.git(nil, nil, "for-each-ref", "--format=delete %(refname)", prefix)
	if err != nil || len(out) == 0 {
		return
	}
	r.git(bytes.NewReader(out), nil, "update-ref", "--stdin")
}

// bundleVersions lists the versions a bundle names, by the same rule as
// versionRefs: a ref under refs/versions/ named for the commit it points to.
func (r Repo) bundleVersions(bundle []byte) ([]string, error) {
	dir, err := os.MkdirTemp("", "paisans-bundle-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "b.bundle")
	if err := os.WriteFile(path, bundle, 0o600); err != nil {
		return nil, err
	}
	out, err := r.git(nil, nil, "bundle", "list-heads", path)
	if err != nil {
		return nil, err
	}
	var shas []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == "refs/versions/"+f[0] && checkSHA(f[0]) == nil {
			shas = append(shas, f[0])
		}
	}
	return shas, nil
}
