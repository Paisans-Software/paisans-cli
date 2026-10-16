//go:build linux

package hostadmit_test

import (
	"os"
	"os/exec"
	osuser "os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

// rootUser makes the user paisans-0e0e with its home, and its deployment
// directory as admit leaves it, the user's, and removes both when the test
// ends. It skips unless run as root with PAISANS_ROOT_TESTS=1, on a
// throwaway machine or container.
func rootUser(t *testing.T) (string, int) {
	t.Helper()
	if os.Geteuid() != 0 || os.Getenv("PAISANS_ROOT_TESTS") != "1" {
		t.Skip("run as root with PAISANS_ROOT_TESTS=1: it creates and removes the user paisans-0e0e")
	}
	user := "paisans-0e0e"
	if out, err := exec.Command("useradd", "--create-home", "--shell", "/bin/bash", user).CombinedOutput(); err != nil {
		t.Fatalf("useradd: %v %s", err, out)
	}
	t.Cleanup(func() {
		exec.Command("userdel", "--remove", user).Run()
		os.RemoveAll("/srv/paisans/0e0e")
	})
	u, err := osuser.Lookup(user)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if err := os.MkdirAll("/srv/paisans", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir("/srv/paisans/0e0e", 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown("/srv/paisans/0e0e", uid, gid); err != nil {
		t.Fatal(err)
	}
	return user, uid
}

const rootTestKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice"

// The keys step appends to authorized_keys, keeping what was there, and
// writes the sidecar, both the user's and mode 600.
func TestTheKeysAreWrittenAsTheUser(t *testing.T) {
	user, uid := rootUser(t)
	home := "/home/" + user
	if err := os.MkdirAll(home+"/.ssh", 0o700); err != nil {
		t.Fatal(err)
	}
	// The owner's key, with no final newline.
	if err := os.WriteFile(home+"/.ssh/authorized_keys", []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB owner"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chown(home+"/.ssh", uid, uid)
	os.Chown(home+"/.ssh/authorized_keys", uid, uid)
	if out, err := hostadmit.Local().RunInput(hostadmit.KeysCommand(user, home, "0e0e"), rootTestKey+"\n--- sidecar ---\nadded SHA256:x alice\n"); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	keys, _ := os.ReadFile(home + "/.ssh/authorized_keys")
	if want := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB owner\n" + rootTestKey + "\n"; string(keys) != want {
		t.Errorf("authorized_keys is %q, want %q", keys, want)
	}
	sidecar, _ := os.ReadFile("/srv/paisans/0e0e/.paisans/authorized_keys.owned")
	if string(sidecar) != "added SHA256:x alice\n" {
		t.Errorf("the sidecar is %q", sidecar)
	}
	for _, path := range []string{home + "/.ssh/authorized_keys", "/srv/paisans/0e0e/.paisans/authorized_keys.owned"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st := info.Sys().(*syscall.Stat_t); int(st.Uid) != uid || info.Mode().Perm() != 0o600 {
			t.Errorf("%s is owner %d mode %o", path, st.Uid, info.Mode().Perm())
		}
	}
}

// The keys are written as the user, so a ~/.ssh the user planted as a link
// to a directory of root's leaves that directory as it was.
func TestAPlantedSSHLinkLeavesItsTargetAlone(t *testing.T) {
	user, _ := rootUser(t)
	target := t.TempDir() // root's
	if err := os.Chmod(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, "/home/"+user+"/.ssh"); err != nil {
		t.Fatal(err)
	}
	command := hostadmit.KeysCommand(user, "/home/"+user, "0e0e")
	out, err := hostadmit.Local().RunInput(command, rootTestKey+"\n--- sidecar ---\n")
	if err == nil {
		t.Error("writing keys through a link to root's directory succeeded")
	}
	t.Logf("refused: %v %s", err, out)
	entries, _ := os.ReadDir(target)
	if len(entries) != 0 {
		t.Errorf("root's directory gained %v", entries)
	}
	if _, err := os.Lstat(filepath.Join(target, "authorized_keys")); !os.IsNotExist(err) {
		t.Errorf("authorized_keys appeared in root's directory: %v", err)
	}
}
