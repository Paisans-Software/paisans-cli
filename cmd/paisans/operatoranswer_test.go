package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/keystore"
	"github.com/paisans-software/paisans-cli/internal/operatorinvite"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// fixtureDeploymentID is the deployment id the fixture configuration holds.
const fixtureDeploymentID = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"

// answerFixture is a signed invite for carol, the inviter's key line, and
// carol's private key file; carol's key is held by the agent, and the
// keystore is empty.
type answerFixture struct {
	invite     []byte
	inviterKey string
	carolPriv  string
	carolPub   string
	keystore   *keystore.Memory
}

func newAnswerFixture(t *testing.T) answerFixture {
	t.Helper()
	withoutAgeKey(t)
	ks := keystore.NewMemory()
	withKeystore(t, ks)
	reporterOverride = ui.NewPlain(io.Discard, false)
	t.Cleanup(func() { reporterOverride = nil })

	l, _, inviterPub := inviteWorkstation(t)
	carolPriv, carolPub := testKey(t)
	var out bytes.Buffer
	if err := runOperatorInvite(inviteArgs(l, "carol", "--key", carolPub), &out); err != nil {
		t.Fatal(err)
	}
	signFromFile(t, carolPriv)
	saved := agentKeys
	agentKeys = func() []string { return []string{carolPub} }
	t.Cleanup(func() { agentKeys = saved })
	return answerFixture{invite: out.Bytes(), inviterKey: inviterPub, carolPriv: carolPriv, carolPub: carolPub, keystore: ks}
}

func (f answerFixture) answer(dir string, inviterKey string) (error, []byte) {
	var out bytes.Buffer
	err := answerInvite(ui.NewPlain(io.Discard, false), dir, f.invite, inviterKey, "Carol Jones", "carol@example.org", "", strings.NewReader(""), &out)
	return err, out.Bytes()
}

func TestAnswerWritesSignedRequest(t *testing.T) {
	f := newAnswerFixture(t)
	dir := t.TempDir()
	err, out := f.answer(dir, f.inviterKey)
	if err != nil {
		t.Fatal(err)
	}
	q, inv, err := operatorinvite.ParseRequest(out)
	if err != nil {
		t.Fatal(err)
	}
	if q.Name != "Carol Jones" || q.Email != "carol@example.org" || q.Invite != string(f.invite) || inv.Username != "carol" {
		t.Fatalf("got %+v", q)
	}
	want, err := operatorAgeKey(ui.NewPlain(io.Discard, false), "operator register", inv.Deployment, filepath.Join(dir, config.FileName), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(q.Age, "age1") || q.Age != want.Recipient {
		t.Fatalf("age %q, want %q", q.Age, want.Recipient)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".paisans", "invite"))
	if err != nil || !bytes.Equal(got, f.invite) {
		t.Fatalf("recorded invite %q, %v", got, err)
	}
	invite, key, ok, err := readJoin(dir)
	if err != nil || !ok || !bytes.Equal(invite, f.invite) || key != f.inviterKey {
		t.Fatalf("readJoin: %q %q %v %v", invite, key, ok, err)
	}
	for path, mode := range map[string]os.FileMode{".paisans": 0o700, ".paisans/invite": 0o600, ".paisans/inviter.pub": 0o600} {
		info, err := os.Stat(filepath.Join(dir, path))
		if err != nil || info.Mode().Perm() != mode {
			t.Errorf("%s: %v, %v, want %v", path, info, err, mode)
		}
	}
}

func TestAnswerTwiceSameAgeKey(t *testing.T) {
	f := newAnswerFixture(t)
	dir := t.TempDir()
	var ages []string
	for range 2 {
		err, out := f.answer(dir, f.inviterKey)
		if err != nil {
			t.Fatal(err)
		}
		q, _, err := operatorinvite.ParseRequest(out)
		if err != nil {
			t.Fatal(err)
		}
		ages = append(ages, q.Age)
	}
	if ages[0] != ages[1] {
		t.Errorf("ages differ: %v", ages)
	}
	if len(f.keystore.Items) != 1 {
		t.Errorf("the store holds %d items, want 1", len(f.keystore.Items))
	}
}

func TestAnswerRefusesWrongInviterKey(t *testing.T) {
	f := newAnswerFixture(t)
	dir := t.TempDir()
	_, other := testKey(t)
	err, out := f.answer(dir, other)
	if err == nil {
		t.Fatal("answered with the wrong inviter key")
	}
	f.nothingMade(t, dir, out)
}

func TestAnswerRefusesKeyNotInAgent(t *testing.T) {
	f := newAnswerFixture(t)
	_, other := testKey(t)
	agentKeys = func() []string { return []string{other} }
	dir := t.TempDir()
	err, out := f.answer(dir, f.inviterKey)
	if err == nil || !strings.Contains(problemText(err), "ssh-add") {
		t.Fatalf("got %q", problemText(err))
	}
	f.nothingMade(t, dir, out)
}

func TestAnswerRefusesOtherDeploymentHere(t *testing.T) {
	f := newAnswerFixture(t)
	dir := t.TempDir()
	body, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	other := strings.Replace(string(body), "id: "+fixtureDeploymentID, "id: 11111111-2222-4333-8444-555555555555", 1)
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	err, out := f.answer(dir, f.inviterKey)
	if err == nil || !strings.Contains(problemText(err), "--config") {
		t.Fatalf("got %q", problemText(err))
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".paisans")); statErr == nil {
		t.Error(".paisans was created")
	}
	if len(out) != 0 || len(f.keystore.Items) != 0 {
		t.Errorf("wrote %q, keystore %v", out, f.keystore.Items)
	}
}

func TestAnswerRefusesARecordedInviteForAnotherDeployment(t *testing.T) {
	f := newAnswerFixture(t)
	dir := t.TempDir()
	// An invite for another deployment, signed by carol's own key standing
	// as its inviter, recorded as an earlier answer would have.
	inv, _, err := operatorinvite.InviteSigner(f.invite)
	if err != nil {
		t.Fatal(err)
	}
	k, _, _ := config.ParseKeyLine(f.carolPub)
	inv.Deployment = "11111111-2222-4333-8444-555555555555"
	inv.Inviter.Fingerprint = k.Fingerprint
	body := operatorinvite.EncodeInvite(inv)
	sig, err := operatorinvite.Sign(f.carolPub, operatorinvite.InviteNamespace, body)
	if err != nil {
		t.Fatal(err)
	}
	if err := recordJoin(dir, operatorinvite.Signed(body, sig), f.carolPub); err != nil {
		t.Fatal(err)
	}
	err, out := f.answer(dir, f.inviterKey)
	if err == nil || !strings.Contains(problemText(err), "--config") {
		t.Fatalf("got %q", problemText(err))
	}
	if len(out) != 0 || len(f.keystore.Items) != 0 {
		t.Errorf("wrote %q, keystore %v", out, f.keystore.Items)
	}
}

func TestAnswerWithoutTerminalNeedsNameAndEmail(t *testing.T) {
	f := newAnswerFixture(t)
	for _, c := range []struct{ name, email, flag string }{
		{"", "carol@example.org", "--name"},
		{"Carol Jones", "", "--email"},
	} {
		dir := t.TempDir()
		var out bytes.Buffer
		err := answerInvite(ui.NewPlain(io.Discard, false), dir, f.invite, f.inviterKey, c.name, c.email, "", strings.NewReader(""), &out)
		if err == nil || !strings.Contains(problemText(err), c.flag) {
			t.Fatalf("%s: got %q", c.flag, problemText(err))
		}
		f.nothingMade(t, dir, out.Bytes())
	}
}

func TestAnswerRefusesABadNameWithoutTerminal(t *testing.T) {
	f := newAnswerFixture(t)
	dir := t.TempDir()
	var out bytes.Buffer
	err := answerInvite(ui.NewPlain(io.Discard, false), dir, f.invite, f.inviterKey, "Carol", "carol@example.org", "", strings.NewReader(""), &out)
	if err == nil || !strings.Contains(problemText(err), "--name") {
		t.Fatalf("got %q", problemText(err))
	}
	f.nothingMade(t, dir, out.Bytes())
}

func TestRegisterInviteWritesOnlyTheRequestToStdout(t *testing.T) {
	f := newAnswerFixture(t)
	dir := t.TempDir()
	file := filepath.Join(t.TempDir(), "carol.invite")
	if err := os.WriteFile(file, f.invite, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	args := []string{"--invite", file, "--inviter-key", f.inviterKey, "--name", "Carol Jones", "--email", "carol@example.org", "--config", filepath.Join(dir, config.FileName)}
	if err := runOperatorRegister(args, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if _, _, err := operatorinvite.ParseRequest(out.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterRefusesAnswerFlagsWithoutInvite(t *testing.T) {
	var out bytes.Buffer
	err := runOperatorRegister([]string{"--name", "Carol Jones"}, strings.NewReader(""), &out)
	if err == nil || !strings.Contains(problemText(err), "--invite") {
		t.Fatalf("got %q", problemText(err))
	}
}

// nothingMade fails the test when a refused answer wrote a request, made a
// keystore item or left anything in dir.
func (f answerFixture) nothingMade(t *testing.T, dir string, out []byte) {
	t.Helper()
	if len(out) != 0 {
		t.Errorf("wrote %q", out)
	}
	if len(f.keystore.Items) != 0 {
		t.Errorf("keystore holds %v", f.keystore.Items)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("%s holds %d entries", dir, len(entries))
	}
}

func TestAnswerRefusesADifferentInviteRecordedHere(t *testing.T) {
	f := newAnswerFixture(t)
	dir := t.TempDir()
	if err, _ := f.answer(dir, f.inviterKey); err != nil {
		t.Fatal(err)
	}
	// Same deployment, another invite: the bytes differ.
	inv, _, err := operatorinvite.InviteSigner(f.invite)
	if err != nil {
		t.Fatal(err)
	}
	// Another operator, dave, invites the same key to the same deployment.
	davePriv, davePub := testKey(t)
	dk, _, _ := config.ParseKeyLine(davePub)
	inv.Inviter = operatorinvite.Inviter{Name: "dave", Fingerprint: dk.Fingerprint}
	body := operatorinvite.EncodeInvite(inv)
	signFromFile(t, davePriv)
	sig, err := operatorinvite.Sign(davePub, operatorinvite.InviteNamespace, body)
	if err != nil {
		t.Fatal(err)
	}
	signFromFile(t, f.carolPriv)
	other := operatorinvite.Signed(body, sig)
	var out bytes.Buffer
	err = answerInvite(ui.NewPlain(io.Discard, false), dir, other, davePub, "Carol Jones", "carol@example.org", "", strings.NewReader(""), &out)
	if err == nil || !strings.Contains(problemText(err), "--config") || !strings.Contains(problemText(err), "remove") {
		t.Fatalf("got %q", problemText(err))
	}
	if out.Len() != 0 {
		t.Errorf("wrote %q", out.String())
	}
	got, _ := os.ReadFile(filepath.Join(dir, ".paisans", "invite"))
	if !bytes.Equal(got, f.invite) {
		t.Error("the recorded invite was replaced")
	}
}

func TestReadJoinWithoutInviteIsNotRecorded(t *testing.T) {
	f := newAnswerFixture(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".paisans"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".paisans", "inviter.pub"), []byte(f.inviterKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := readJoin(dir); ok || err != nil {
		t.Fatalf("ok %v, err %v", ok, err)
	}
	if err, out := f.answer(dir, f.inviterKey); err != nil || len(out) == 0 {
		t.Fatalf("answering failed: %v", err)
	}
}

func TestAnswerRefusesThisDeploymentsFilesHere(t *testing.T) {
	f := newAnswerFixture(t)
	dir := t.TempDir()
	body, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, config.FileName), body, 0o644); err != nil {
		t.Fatal(err)
	}
	err, out := f.answer(dir, f.inviterKey)
	if err == nil || !strings.Contains(problemText(err), "already holds") {
		t.Fatalf("got %q", problemText(err))
	}
	if len(out) != 0 || len(f.keystore.Items) != 0 {
		t.Errorf("wrote %q, keystore %v", out, f.keystore.Items)
	}
}

func TestRegisterInviteRefusesRegisteringFlags(t *testing.T) {
	for _, flag := range [][]string{{"--key", "k.pub"}, {"--root", "abc"}, {"--execute"}} {
		var out bytes.Buffer
		err := runOperatorRegister(append([]string{"--invite", "x"}, flag...), strings.NewReader(""), &out)
		if err == nil || !strings.Contains(problemText(err), "--invite answers an invite") {
			t.Errorf("%v: got %q", flag, problemText(err))
		}
	}
}
