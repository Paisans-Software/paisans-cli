package apply_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// The commands that empty a directory through rootlesskit, run by a real
// shell against a directory tree under a temporary root, so what they delete
// is what sh does rather than what a fake says it does.

// runRootless runs command under sh with base in front of every quoted
// /srv/paisans path. rootlesskit, which a host has and this machine may not,
// stands in as a script that runs its arguments: the namespace changes whose
// files may be removed, not which.
func runRootless(t *testing.T, base, command string) (string, error) {
	t.Helper()
	command = strings.ReplaceAll(command, "'/srv/paisans/", "'"+base+"/srv/paisans/")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "rootlesskit"), []byte("#!/bin/sh\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeTree(t *testing.T, base string, files ...string) {
	t.Helper()
	for _, rel := range files {
		p := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

var rmDeployment = deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}

// Emptying etcd's data directory deletes every entry in it, dotfiles and
// names starting with two dots included, keeps the directory itself, which
// the etcd container mounts, and touches nothing beside it.
func TestEmptyEtcdDataDeletesEverythingInItAndKeepsIt(t *testing.T) {
	base := t.TempDir()
	dir := strings.TrimPrefix(apply.EtcdDataDir(rmDeployment), "/")
	if dir != "srv/paisans/f2a9/infra/etcd" {
		t.Fatalf("etcd's data directory is %s", dir)
	}
	writeTree(t, base,
		dir+"/member/snap/db",
		dir+"/member/wal/0000000000000000-0000000000000000.wal",
		dir+"/.lock",
		dir+"/..odd",
		"srv/paisans/f2a9/infra/etcd-initial",
		"srv/paisans/f2a9/infra/compose.yaml",
	)
	if err := os.Symlink(filepath.Join(base, "srv/paisans/f2a9/infra/compose.yaml"), filepath.Join(base, dir, "link")); err != nil {
		t.Fatal(err)
	}
	if out, err := runRootless(t, base, apply.EmptyEtcdData(rmDeployment)); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	entries, err := os.ReadDir(filepath.Join(base, dir))
	if err != nil {
		t.Fatalf("the directory itself is gone: %v", err)
	}
	for _, e := range entries {
		t.Errorf("%s is left in the data directory", e.Name())
	}
	for _, rel := range []string{"srv/paisans/f2a9/infra/etcd-initial", "srv/paisans/f2a9/infra/compose.yaml"} {
		if _, err := os.Stat(filepath.Join(base, rel)); err != nil {
			t.Errorf("%s, beside the data directory, was deleted", rel)
		}
	}
}

// A site that never ran etcd has no data directory, or an empty one, and
// emptying it is nothing.
func TestEmptyEtcdDataOnAFreshSiteIsNothing(t *testing.T) {
	base := t.TempDir()
	if out, err := runRootless(t, base, apply.EmptyEtcdData(rmDeployment)); err != nil {
		t.Fatalf("with no directory: %v: %s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(base, "srv/paisans/f2a9/infra/etcd"), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := runRootless(t, base, apply.EmptyEtcdData(rmDeployment)); err != nil {
		t.Fatalf("with an empty directory: %v: %s", err, out)
	}
}

// A data directory that is a symbolic link would take the delete outside
// the deployment: it is refused and nothing is deleted.
func TestEmptyEtcdDataRefusesASymlinkedDirectory(t *testing.T) {
	base := t.TempDir()
	writeTree(t, base, "outside/keep.txt", "srv/paisans/f2a9/infra/compose.yaml")
	if err := os.Symlink(filepath.Join(base, "outside"), filepath.Join(base, "srv/paisans/f2a9/infra/etcd")); err != nil {
		t.Fatal(err)
	}
	out, err := runRootless(t, base, apply.EmptyEtcdData(rmDeployment))
	if err == nil || !strings.Contains(out, "symbolic link") {
		t.Errorf("a symlinked data directory was emptied: %v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(base, "outside/keep.txt")); err != nil {
		t.Error("a file was deleted through the link")
	}
}

// An entry rootlesskit cannot delete stops the command with a failure,
// rather than the loop going on and the command exiting 0.
func TestEmptyEtcdDataFailsWhenAnEntryCannotGo(t *testing.T) {
	base := t.TempDir()
	dir := "srv/paisans/f2a9/infra/etcd"
	writeTree(t, base, dir+"/a", dir+"/z")
	bin := t.TempDir()
	// A rootlesskit that refuses the first entry only.
	script := "#!/bin/sh\ncase \"$4\" in */a) echo \"rm: cannot remove '$4': Permission denied\"; exit 1;; esac\nexec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "rootlesskit"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	command := strings.ReplaceAll(apply.EmptyEtcdData(rmDeployment), "'/srv/paisans/", "'"+base+"/srv/paisans/")
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("a refused delete exited 0: %s", out)
	}
}
