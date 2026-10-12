package configstore

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
// configuration shut out, so that only what this package passes applies.
func (r Repo) git(stdin io.Reader, env []string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", r.Dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
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

// hasVersions reports whether any refs/versions/* ref exists.
func (r Repo) hasVersions() (bool, error) {
	out, err := r.git(nil, nil, "for-each-ref", "--count=1", "--format=%(refname)", "refs/versions/")
	return len(bytes.TrimSpace(out)) > 0, err
}

// Versions lists every version in the repository with git's verdict on its
// signature, checked against the allowed-signers file at allowedSigners.
func (r Repo) Versions(allowedSigners string) ([]Version, error) {
	have, err := r.hasVersions()
	if err != nil || !have {
		return nil, err
	}
	const format = "%H%x00%P%x00%G?%x00%GF%x00%ct%x00%(trailers:key=Paisans-Revision,valueonly)%x1e"
	out, err := r.git(nil, nil, "-c", "gpg.ssh.allowedSignersFile="+allowedSigners,
		"log", "--no-walk=unsorted", "--format="+format, "--glob=refs/versions")
	if err != nil {
		return nil, err
	}
	var vs []Version
	for _, rec := range strings.Split(string(out), "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		fields := strings.Split(rec, "\x00")
		if len(fields) != 6 {
			return nil, fmt.Errorf("git log: unexpected record %q", rec)
		}
		v := Version{SHA: fields[0], Parents: strings.Fields(fields[1]), Signed: fields[2] == "G", SignerFP: fields[3]}
		if secs, err := strconv.ParseInt(fields[4], 10, 64); err == nil {
			v.Committed = time.Unix(secs, 0).UTC()
		}
		if n, err := strconv.Atoi(strings.TrimSpace(fields[5])); err == nil && n > 0 {
			v.Revision = n
		}
		names, err := r.git(nil, nil, "ls-tree", "--name-only", v.SHA)
		if err != nil {
			return nil, err
		}
		v.TreeOK = strings.Join(strings.Fields(string(names)), " ") == nameSOPS+" "+namePaisans+" "+nameSecrets
		vs = append(vs, v)
	}
	return vs, nil
}

// Files reads the three files of the version sha.
func (r Repo) Files(sha string) (Files, error) {
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

// Bundle packs every refs/versions/* ref; it is nil when there are none.
func (r Repo) Bundle() ([]byte, error) {
	have, err := r.hasVersions()
	if err != nil || !have {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "paisans-bundle-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "b.bundle")
	if _, err := r.git(nil, nil, "bundle", "create", "--quiet", path, "--glob=refs/versions"); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// FetchBundle adds the versions in a bundle after git has verified it. The
// refspec has no force marker: a ref is named for its own commit, so it never
// legitimately moves, and an update to one is a forgery that must fail.
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
	_, err = r.git(nil, nil, "fetch", "--no-prune", "--quiet", path, "refs/versions/*:refs/versions/*")
	return err
}
