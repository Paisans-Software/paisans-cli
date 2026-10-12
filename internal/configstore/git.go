package configstore

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The three files of a version, by their names in the tree, sorted as git's
// mktree requires.
const (
	nameSOPS    = ".sops.yaml"
	namePaisans = "paisans.yaml"
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
	TreeOK    bool      // the tree holds exactly the three files
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
// revision in a trailer, and names it with refs/versions/<sha>.
func (r Repo) Commit(f Files, parents []string, revision int, signingKey string, when time.Time) (string, error) {
	for _, p := range parents {
		if err := checkSHA(p); err != nil {
			return "", err
		}
	}
	var tree strings.Builder
	for _, e := range []struct {
		name string
		data []byte
	}{{nameSOPS, f.SOPS}, {namePaisans, f.Paisans}, {nameSecrets, f.Secrets}} {
		out, err := r.git(bytes.NewReader(e.data), nil, "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&tree, "100644 blob %s\t%s\n", strings.TrimSpace(string(out)), e.name)
	}
	out, err := r.git(strings.NewReader(tree.String()), nil, "mktree")
	if err != nil {
		return "", err
	}
	args := []string{
		"-c", "gpg.format=ssh", "-c", "user.signingKey=" + signingKey,
		"-c", "user.name=paisans", "-c", "user.email=paisans@invalid",
		"commit-tree", strings.TrimSpace(string(out)), "-S",
	}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	args = append(args, "-m", fmt.Sprintf("paisans configuration\n\nPaisans-Revision: %d\n", revision))
	date := when.UTC().Format(time.RFC3339)
	out, err = r.git(nil, []string{"GIT_COMMITTER_DATE=" + date, "GIT_AUTHOR_DATE=" + date}, args...)
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(out))
	if _, err := r.git(nil, nil, "update-ref", "refs/versions/"+sha, sha); err != nil {
		return "", err
	}
	return sha, nil
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
// regular non-executable blob. Entries are read NUL-delimited so that no name
// can pass for several.
func (r Repo) treeOK(sha string) (bool, error) {
	out, err := r.git(nil, nil, "ls-tree", "-z", sha)
	if err != nil {
		return false, err
	}
	want := map[string]bool{nameSOPS: true, namePaisans: true, nameSecrets: true}
	entries := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if len(entries) != len(want) {
		return false, nil
	}
	for _, e := range entries {
		meta, name, ok := strings.Cut(e, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 || f[0] != "100644" || f[1] != "blob" || !want[name] {
			return false, nil
		}
		delete(want, name)
	}
	return len(want) == 0, nil
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
