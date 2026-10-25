package operatorinvite_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/paisans-software/paisans-cli/internal/operatorinvite"
)

// testKey is an unencrypted ed25519 key: its private key file, and its
// public key line as type and base64 alone.
type testKey struct {
	priv, pub, fingerprint string
}

func needSSHKeygen(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not on PATH")
	}
}

func newKey(t *testing.T) testKey {
	t.Helper()
	needSSHKeygen(t)
	path := filepath.Join(t.TempDir(), "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "someone@example", "-f", path).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	data, err := os.ReadFile(path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{priv: path, pub: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))), fingerprint: ssh.FingerprintSHA256(pub)}
}

// sign is an SSH signature of data by k under namespace, from its private
// key file.
func sign(t *testing.T, k testKey, namespace string, data []byte) []byte {
	t.Helper()
	cmd := exec.Command("ssh-keygen", "-Y", "sign", "-n", namespace, "-f", k.priv)
	cmd.Stdin = bytes.NewReader(data)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ssh-keygen -Y sign: %v: %s", err, stderr.String())
	}
	return out.Bytes()
}

// useKeys replaces Sign, for the rest of the test, with signing from the
// private key files of keys, found by their public key lines.
func useKeys(t *testing.T, keys ...testKey) {
	t.Helper()
	old := operatorinvite.Sign
	t.Cleanup(func() { operatorinvite.Sign = old })
	operatorinvite.Sign = func(keyLine, namespace string, data []byte) ([]byte, error) {
		for _, k := range keys {
			if k.pub == keyLine {
				return sign(t, k, namespace, data), nil
			}
		}
		t.Fatalf("no test key for %s", keyLine)
		return nil, nil
	}
}

func TestSignedVerifies(t *testing.T) {
	k := newKey(t)
	body := []byte("version: 1\n")
	sig := sign(t, k, operatorinvite.InviteNamespace, body)
	if err := operatorinvite.Verify(k.pub, operatorinvite.InviteNamespace, body, sig); err != nil {
		t.Fatal(err)
	}
	signed := operatorinvite.Signed(body, sig)
	if !bytes.HasPrefix(signed, body) || !bytes.Contains(signed, []byte("\n-----BEGIN SSH SIGNATURE-----\n")) {
		t.Fatalf("Signed is not the body then the block:\n%s", signed)
	}
}

func TestVerifyRefusesOtherKey(t *testing.T) {
	k, other := newKey(t), newKey(t)
	body := []byte("version: 1\n")
	sig := sign(t, other, operatorinvite.InviteNamespace, body)
	if err := operatorinvite.Verify(k.pub, operatorinvite.InviteNamespace, body, sig); err == nil {
		t.Fatal("a signature by another key verified")
	}
}

func TestVerifyRefusesOtherNamespace(t *testing.T) {
	k := newKey(t)
	body := []byte("version: 1\n")
	sig := sign(t, k, operatorinvite.RequestNamespace, body)
	if err := operatorinvite.Verify(k.pub, operatorinvite.InviteNamespace, body, sig); err == nil {
		t.Fatal("a signature under the request namespace verified as an invite's")
	}
}

func TestVerifyRefusesChangedBody(t *testing.T) {
	k := newKey(t)
	body := []byte("version: 1\n")
	sig := sign(t, k, operatorinvite.InviteNamespace, body)
	if err := operatorinvite.Verify(k.pub, operatorinvite.InviteNamespace, []byte("version: 2\n"), sig); err == nil {
		t.Fatal("a signature verified over other bytes")
	}
}

// The real Sign signs through the ssh-agent with only the public key named,
// and what it writes verifies with ssh-keygen -Y verify itself.
func TestVerifyMatchesSSHKeygen(t *testing.T) {
	needSSHKeygen(t)
	for _, tool := range []string{"ssh-agent", "ssh-add"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	k := newKey(t)
	// A socket's path is limited to about a hundred bytes, which a test's
	// temporary directory can exceed.
	short, err := os.MkdirTemp("/tmp", "pi")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	sock := filepath.Join(short, "s")
	agent := exec.Command("ssh-agent", "-D", "-a", sock)
	if err := agent.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { agent.Process.Kill(); agent.Wait() })
	t.Setenv("SSH_AUTH_SOCK", sock)
	for i := 0; i < 50; i++ {
		if err = exec.Command("ssh-add", k.priv).Run(); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Skipf("no agent socket: ssh-add: %v", err)
	}
	body := []byte("version: 1\n")
	sig, err := operatorinvite.Sign(k.pub, operatorinvite.InviteNamespace, body)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	allowed, sigFile := filepath.Join(dir, "allowed"), filepath.Join(dir, "sig")
	if err := os.WriteFile(allowed, []byte("paisans "+k.pub+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sigFile, sig, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ssh-keygen", "-Y", "verify", "-f", allowed, "-I", "paisans", "-n", operatorinvite.InviteNamespace, "-s", sigFile)
	cmd.Stdin = bytes.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen -Y verify: %v: %s", err, out)
	}
	if err := operatorinvite.Verify(k.pub, operatorinvite.InviteNamespace, body, sig); err != nil {
		t.Fatal(err)
	}
}

func TestSignedEndsTheBodyWithANewline(t *testing.T) {
	k := newKey(t)
	sig := sign(t, k, operatorinvite.InviteNamespace, []byte("version: 1\n"))
	if got := operatorinvite.Signed([]byte("version: 1"), sig); !bytes.HasPrefix(got, []byte("version: 1\n-----BEGIN SSH SIGNATURE-----\n")) {
		t.Fatalf("Signed does not start the block on a line of its own:\n%s", got)
	}
}

// Sign returns only what is one armored signature block, whatever the
// ssh-keygen on PATH printed.
func TestSignRefusesOutputThatIsNotOneBlock(t *testing.T) {
	k := newKey(t)
	block := string(sign(t, k, operatorinvite.InviteNamespace, []byte("x\n")))
	for _, out := range []string{"", "Signing data\n" + block, block + "more\n", strings.TrimSuffix(block, "\n"), "-----BEGIN SSH SIGNATURE-----\n"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "out"), []byte(out), 0o600); err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\ncat >/dev/null\ncat " + filepath.Join(dir, "out") + "\n"
		if err := os.WriteFile(filepath.Join(dir, "ssh-keygen"), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		if sig, err := operatorinvite.Sign(k.pub, operatorinvite.InviteNamespace, []byte("x\n")); err == nil {
			t.Errorf("ssh-keygen printed %q and Sign returned %q", out, sig)
		}
	}
}
