package configstore

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const snapCommit = "4f2a0c0e9b1d7a3f5e6c8b2d4a6f8e0c1b3d5f7a"

func snapshotFiles() Files {
	f := testFiles()
	f.Blueprints = map[string][]byte{
		snapCommit + "/journal/blueprint.yaml":    []byte("name: journal\n"),
		snapCommit + "/journal/compose.yaml.tmpl": []byte("services: {}\n"),
		snapCommit + "/journal/oauth/key.tmpl":    []byte("k\n"),
	}
	return f
}

// A version with a blueprint snapshot keeps it under blueprints/, with the
// SHA-256 of each blueprint beside it, and reads it back exactly.
func TestASnapshotIsStoredAndReadBack(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	key, pub := testSSHKey(t)
	r, err := InitRepo(filepath.Join(t.TempDir(), "s.git"))
	if err != nil {
		t.Fatal(err)
	}
	f := snapshotFiles()
	sha, err := r.Commit(f, nil, 1, key, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	vs, err := r.Versions(allowedSignersFile(t, pub))
	if err != nil || len(vs) != 1 || !vs[0].TreeOK || !vs[0].Signed {
		t.Fatalf("versions %+v, %v", vs, err)
	}
	got, err := r.Files(sha)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(f) {
		t.Errorf("read back %+v", got)
	}
	sums, err := r.git(nil, nil, "cat-file", "blob", sha+":blueprints/SHA256SUMS")
	if err != nil || !strings.HasSuffix(strings.TrimSpace(string(sums)), "  "+snapCommit+"/journal") {
		t.Errorf("SHA256SUMS %q, %v", sums, err)
	}
	// A version without a snapshot is the three files alone, as before.
	plain, err := r.Commit(testFiles(), nil, 2, key, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := r.git(nil, nil, "ls-tree", "--name-only", plain)
	if strings.Fields(string(out))[0] != ".sops.yaml" || len(strings.Fields(string(out))) != 3 {
		t.Errorf("plain tree %q", out)
	}
}

// A snapshot whose files do not match its SHA256SUMS, or with a path a
// snapshot never holds, is not a sound tree, and its version is not
// trusted.
func TestATamperedSnapshotIsRefused(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	key, _ := testSSHKey(t)
	r, err := InitRepo(filepath.Join(t.TempDir(), "t.git"))
	if err != nil {
		t.Fatal(err)
	}
	sha, err := r.Commit(snapshotFiles(), nil, 1, key, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite one blueprint file in the tree, keeping the old SHA256SUMS.
	tree := func(replace, with string) string {
		listing, err := r.git(nil, nil, "ls-tree", "-r", sha)
		if err != nil {
			t.Fatal(err)
		}
		blob, _ := r.git(bytes.NewReader([]byte("tampered\n")), nil, "hash-object", "-w", "--stdin")
		index := filepath.Join(t.TempDir(), "index")
		env := []string{"GIT_INDEX_FILE=" + index}
		for _, line := range strings.Split(strings.TrimSpace(string(listing)), "\n") {
			meta, path, _ := strings.Cut(line, "\t")
			f := strings.Fields(meta)
			obj := f[2]
			if path == replace {
				obj = strings.TrimSpace(string(blob))
				if with != "" {
					path = with
				}
			}
			if _, err := r.git(nil, env, "update-index", "--add", "--cacheinfo", "100644,"+obj+","+path); err != nil {
				t.Fatal(err)
			}
		}
		out, err := r.git(nil, env, "write-tree")
		if err != nil {
			t.Fatal(err)
		}
		c, err := r.git(nil, nil, "commit-tree", strings.TrimSpace(string(out)), "-m", "x")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(c))
	}
	for _, c := range []struct{ replace, with string }{
		{"blueprints/" + snapCommit + "/journal/compose.yaml.tmpl", ""},
		{"blueprints/" + snapCommit + "/journal/compose.yaml.tmpl", "blueprints/notacommit/journal/compose.yaml.tmpl"},
		{"blueprints/" + snapCommit + "/journal/compose.yaml.tmpl", "extra.yaml"},
	} {
		bad := tree(c.replace, c.with)
		ok, err := r.treeOK(bad)
		if err != nil || ok {
			t.Errorf("%+v: treeOK %v, %v", c, ok, err)
		}
	}
	if ok, err := r.treeOK(sha); err != nil || !ok {
		t.Errorf("the untouched version: %v, %v", ok, err)
	}
}

// Writing a version's files out hands its snapshot to whatever installs
// one, so a checked out version renders without its catalogs.
func TestWritingFilesInstallsTheSnapshot(t *testing.T) {
	var got map[string][]byte
	InstallBlueprints = func(m map[string][]byte) error { got = m; return nil }
	defer func() { InstallBlueprints = nil }()
	f := snapshotFiles()
	if err := writeFiles(t.TempDir(), f); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("installed %v", got)
	}
}
