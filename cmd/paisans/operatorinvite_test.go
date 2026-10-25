package main

import (
	"bytes"
	"errors"
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

// unpinnedWorkstation is a registered workstation with no pinned root,
// whose store holds what build commits, signed with its key: build is given
// the store, the fixture's paisans-stack.yaml with the key in it, and the
// private key path, and returns the files the workstation holds.
func unpinnedWorkstation(t *testing.T, build func(s store, yaml, priv string) configstore.Files) (configstore.Local, string) {
	t.Helper()
	priv, pub := testKey(t)
	s := newStore(t)
	files := build(s, fixtureAs(t, pub), priv)
	l := registerWorkstation(t, s.answer(t), files, nil, pub)
	if err := l.SetSigningKey(pub); err != nil {
		t.Fatal(err)
	}
	bundle, err := s.repo.Bundle()
	if err != nil {
		t.Fatal(err)
	}
	cache, err := l.Cache()
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.FetchBundle(bundle); err != nil {
		t.Fatal(err)
	}
	signFromFile(t, priv)
	return l, pub
}

// With no pin, the invite names the one root of the newest version the
// workstation's store holds.
func TestInviteTakesTheRootFromTheStore(t *testing.T) {
	var root string
	l, pub := unpinnedWorkstation(t, func(s store, yaml, priv string) configstore.Files {
		var f configstore.Files
		root, f = s.commit(t, yaml, "", nil, 1, priv)
		_, f = s.commit(t, yaml, "# edited\n", []string{root}, 2, priv)
		return f
	})
	var out bytes.Buffer
	if err := runOperatorInvite(inviteArgs(l, "carol", "--key", newPub(t)), &out); err != nil {
		t.Fatal(problemText(err))
	}
	inv, err := operatorinvite.ParseInvite(out.Bytes(), pub)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Root != root {
		t.Fatalf("root %q, want %q", inv.Root, root)
	}
}

// A store holding a fork, or a newest version with two roots, names no one
// root, and the invite is refused with deployment pull named.
func TestInviteRefusesAStoreWithoutOneRoot(t *testing.T) {
	for name, build := range map[string]func(s store, yaml, priv string) configstore.Files{
		"fork": func(s store, yaml, priv string) configstore.Files {
			root, f := s.commit(t, yaml, "", nil, 1, priv)
			s.commit(t, yaml, "# one\n", []string{root}, 2, priv)
			s.commit(t, yaml, "# two\n", []string{root}, 2, priv)
			return f
		},
		"several roots": func(s store, yaml, priv string) configstore.Files {
			a, _ := s.commit(t, yaml, "# a\n", nil, 1, priv)
			b, _ := s.commit(t, yaml, "# b\n", nil, 1, priv)
			_, f := s.commit(t, yaml, "# merged\n", []string{a, b}, 2, priv)
			return f
		},
	} {
		t.Run(name, func(t *testing.T) {
			l, _ := unpinnedWorkstation(t, build)
			var out bytes.Buffer
			err := runOperatorInvite(inviteArgs(l, "carol", "--key", newPub(t)), &out)
			if err == nil || !strings.Contains(problemText(err), "deployment pull") {
				t.Fatalf("got %v", problemText(err))
			}
			if cause := errors.Unwrap(err); cause != nil {
				t.Fatalf("judging the store failed rather than finding no one root: %v", cause)
			}
			if out.Len() != 0 {
				t.Fatalf("wrote %q", out.String())
			}
		})
	}
}

// An invite changes no file: a workstation with no store has none after it,
// whether its root is pinned or not.
func TestInviteMakesNoStore(t *testing.T) {
	l, _, _ := inviteWorkstation(t)
	store := filepath.Join(l.Dir, ".paisans", "store.git")
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatalf("the fixture holds a store: %v", err)
	}
	if err := runOperatorInvite(inviteArgs(l, "carol", "--key", newPub(t)), &bytes.Buffer{}); err != nil {
		t.Fatal(problemText(err))
	}
	if err := os.Remove(filepath.Join(l.Dir, ".paisans", "config-root")); err != nil {
		t.Fatal(err)
	}
	if err := runOperatorInvite(inviteArgs(l, "carol", "--key", newPub(t)), &bytes.Buffer{}); err == nil || !strings.Contains(problemText(err), "deployment pull") {
		t.Fatalf("got %v", problemText(err))
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatalf("operator invite made %s: %v", store, err)
	}
}
