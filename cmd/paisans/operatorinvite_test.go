package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/operatorinvite"
)

// signFromFile replaces operatorinvite.Sign, for the rest of the test, with
// signing from the private key file at priv.
func signFromFile(t *testing.T, priv string) {
	t.Helper()
	old := operatorinvite.Sign
	t.Cleanup(func() { operatorinvite.Sign = old })
	operatorinvite.Sign = func(_, namespace string, data []byte) ([]byte, error) {
		cmd := exec.Command("ssh-keygen", "-Y", "sign", "-n", namespace, "-f", priv)
		cmd.Stdin = bytes.NewReader(data)
		var out, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("ssh-keygen -Y sign: %v: %s", err, stderr.String())
		}
		return out.Bytes(), nil
	}
}

// inviteWorkstation is a registered workstation: the fixture's files with
// alice's key replaced by the signing key, that key recorded and held by the
// agent, and the root pinned.
func inviteWorkstation(t *testing.T) (l configstore.Local, shas []string, pub string) {
	t.Helper()
	shas, answer, files, priv, pub := storedHistoryKey(t, 1, 0)
	l = registerWorkstation(t, answer, files[0], nil, pub)
	if err := l.SetSigningKey(pub); err != nil {
		t.Fatal(err)
	}
	if err := l.SetRoot(shas[0]); err != nil {
		t.Fatal(err)
	}
	signFromFile(t, priv)
	return l, shas, pub
}

// newPub is a public key line that carries a comment.
func newPub(t *testing.T) string {
	t.Helper()
	_, pub := testKey(t)
	return pub + " carol@laptop"
}

// bare is a key line as type and base64 alone.
func bare(line string) string {
	f := strings.Fields(line)
	return f[0] + " " + f[1]
}

func inviteArgs(l configstore.Local, rest ...string) []string {
	return append(rest, "--config", filepath.Join(l.Dir, config.FileName))
}

func TestInviteWritesSignedInvite(t *testing.T) {
	l, shas, pub := inviteWorkstation(t)
	newKey := newPub(t)
	want := bare(newKey)
	var out bytes.Buffer
	if err := runOperatorInvite(inviteArgs(l, "carol", "--key", newKey), &out); err != nil {
		t.Fatal(err)
	}
	inv, err := operatorinvite.ParseInvite(out.Bytes(), pub)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(l.Dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if inv.Deployment != cfg.ID || inv.Domain != cfg.Community.Domain || inv.Root != shas[0] || inv.Username != "carol" {
		t.Fatalf("got %+v", inv)
	}
	if inv.Key != want {
		t.Fatalf("key %q, want %q without its comment", inv.Key, want)
	}
	if inv.Inviter.Name != "alice" {
		t.Fatalf("inviter %+v", inv.Inviter)
	}
	k, _, _ := config.ParseKeyLine(pub)
	if inv.Inviter.Fingerprint != k.Fingerprint {
		t.Fatalf("fingerprint %q, want %q", inv.Inviter.Fingerprint, k.Fingerprint)
	}
	names := cfg.SiteNames()
	if len(inv.Sites) != len(names) {
		t.Fatalf("got %d sites, want %d", len(inv.Sites), len(names))
	}
	for i, s := range inv.Sites {
		site := cfg.Sites[names[i]]
		if s.Name != names[i] || s.User != site.SSHUser() || s.Port != site.SSH.PortOrDefault() || s.Host != site.SSHHost() {
			t.Fatalf("site %d: %+v", i, s)
		}
	}
}

func TestInviteRefusesDeclaredUsername(t *testing.T) {
	l, _, _ := inviteWorkstation(t)
	var out bytes.Buffer
	err := runOperatorInvite(inviteArgs(l, "alice", "--key", newPub(t)), &out)
	if err == nil || !strings.Contains(problemText(err), "alice") {
		t.Fatalf("got %v", problemText(err))
	}
	if out.Len() != 0 {
		t.Fatalf("wrote %q", out.String())
	}
}

func TestInviteRefusesTakenKey(t *testing.T) {
	l, _, pub := inviteWorkstation(t)
	var out bytes.Buffer
	err := runOperatorInvite(inviteArgs(l, "carol", "--key", pub), &out)
	if err == nil || !strings.Contains(problemText(err), "alice") {
		t.Fatalf("got %v", problemText(err))
	}
	if out.Len() != 0 {
		t.Fatalf("wrote %q", out.String())
	}
}

func TestInviteReadsKeyFile(t *testing.T) {
	l, _, pub := inviteWorkstation(t)
	newKey := newPub(t)
	want := bare(newKey)
	file := filepath.Join(t.TempDir(), "carol.pub")
	if err := os.WriteFile(file, []byte(newKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runOperatorInvite(inviteArgs(l, "carol", "--key", file), &out); err != nil {
		t.Fatal(err)
	}
	inv, err := operatorinvite.ParseInvite(out.Bytes(), pub)
	if err != nil || inv.Key != want {
		t.Fatalf("got %+v, %v", inv, err)
	}
}

func TestInviteRefusesNoSigningKey(t *testing.T) {
	_, answer, files, _, _ := storedHistoryKey(t, 1, 0)
	l := registerWorkstation(t, answer, files[0], nil)
	var out bytes.Buffer
	err := runOperatorInvite(inviteArgs(l, "carol", "--key", newPub(t)), &out)
	if err == nil || !strings.Contains(problemText(err), "operator register") {
		t.Fatalf("got %v", problemText(err))
	}
	if out.Len() != 0 {
		t.Fatalf("wrote %q", out.String())
	}
}

func TestInviteRefusesAgentWithoutSigningKey(t *testing.T) {
	l, _, _ := inviteWorkstation(t)
	saved := agentKeys
	agentKeys = func() []string { return nil }
	t.Cleanup(func() { agentKeys = saved })
	err := runOperatorInvite(inviteArgs(l, "carol", "--key", newPub(t)), &bytes.Buffer{})
	if err == nil || !strings.Contains(problemText(err), "ssh-agent") {
		t.Fatalf("got %v", problemText(err))
	}
}

func TestInviteRefusesUnknownRoot(t *testing.T) {
	l, _, _ := inviteWorkstation(t)
	if err := os.Remove(filepath.Join(l.Dir, ".paisans", "config-root")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runOperatorInvite(inviteArgs(l, "carol", "--key", newPub(t)), &out)
	if err == nil || !strings.Contains(problemText(err), "deployment pull") {
		t.Fatalf("got %v", problemText(err))
	}
	if out.Len() != 0 {
		t.Fatalf("wrote %q", out.String())
	}
}

func TestInviteNeedsOneUsernameAndAKey(t *testing.T) {
	l, _, _ := inviteWorkstation(t)
	if err := runOperatorInvite(inviteArgs(l, "--key", newPub(t)), &bytes.Buffer{}); err == nil {
		t.Fatal("no username was accepted")
	}
	if err := runOperatorInvite(inviteArgs(l, "carol"), &bytes.Buffer{}); err == nil {
		t.Fatal("no key was accepted")
	}
	if err := runOperatorInvite(inviteArgs(l, "carol", "dave", "--key", newPub(t)), &bytes.Buffer{}); err == nil {
		t.Fatal("two usernames were accepted")
	}
}
