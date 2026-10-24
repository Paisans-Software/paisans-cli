package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/keystore"
	"github.com/paisans-software/paisans-cli/internal/localstore"
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
	withDataDir(t)
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
	base := dataDir(t)
	got, err := os.ReadFile(filepath.Join(base, "invites", inv.Deployment, "invite"))
	if err != nil || !bytes.Equal(got, f.invite) {
		t.Fatalf("pending invite %q, %v", got, err)
	}
	invite, key, ok, err := readPending(inv.Deployment)
	if err != nil || !ok || !bytes.Equal(invite, f.invite) || key != f.inviterKey {
		t.Fatalf("readPending: %q %q %v %v", invite, key, ok, err)
	}
	if recipient, _ := recordedAge(t, inv.Deployment); recipient != q.Age {
		t.Errorf("bound %q, want %q", recipient, q.Age)
	}
	for path, mode := range map[string]os.FileMode{
		"invites/" + inv.Deployment:                  0o700,
		"invites/" + inv.Deployment + "/invite":      0o600,
		"invites/" + inv.Deployment + "/inviter.pub": 0o600,
		"age":                                  0o700,
		"age/" + inv.Deployment + ".recipient": 0o600,
	} {
		info, err := os.Stat(filepath.Join(base, path))
		if err != nil || info.Mode().Perm() != mode {
			t.Errorf("%s: %v, %v, want %v", path, info, err, mode)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the answer wrote %d entries into the folder", len(entries))
	}
}

func TestAnswerTwiceSameAgeKey(t *testing.T) {
	f := newAnswerFixture(t)
	var ages []string
	for range 2 {
		err, out := f.answer(t.TempDir(), f.inviterKey)
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
	for _, sub := range []string{"invites", "age"} {
		if entries, _ := os.ReadDir(filepath.Join(dataDir(t), sub)); len(entries) != 0 {
			t.Errorf("the data directory's %s holds %d entries", sub, len(entries))
		}
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

// answerShowing answers f's invite in dir with keyFile and stdin, and
// returns the error, the request and what the reporter printed.
func (f answerFixture) answerShowing(t *testing.T, dir, name, email, keyFile string, stdin io.Reader) (error, []byte, string) {
	t.Helper()
	var out, report bytes.Buffer
	err := answerInvite(ui.NewPlain(&report, false), dir, f.invite, f.inviterKey, name, email, keyFile, stdin, &out)
	return err, out.Bytes(), report.String()
}

// recordedAge is the age key and source bound to deployment id.
func recordedAge(t *testing.T, id string) (string, string) {
	t.Helper()
	recipient, source, _, err := readAgeBinding(id)
	if err != nil {
		t.Fatal(err)
	}
	return recipient, source
}

// An existing --age-private-key-file is read, not replaced, and the answer
// says to export SOPS_AGE_KEY_FILE, since nothing finds a key file on its
// own. The record names the file in full.
func TestAnswerReadsAnExistingKeyFile(t *testing.T) {
	f := newAnswerFixture(t)
	identity, _ := age.GenerateX25519Identity()
	keyFile := filepath.Join(t.TempDir(), "carol.key")
	if err := os.WriteFile(keyFile, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	err, out, report := f.answerShowing(t, dir, "Carol Jones", "carol@example.org", keyFile, strings.NewReader(""))
	if err != nil {
		t.Fatal(problemText(err))
	}
	q, _, err := operatorinvite.ParseRequest(out)
	if err != nil {
		t.Fatal(err)
	}
	if q.Age != identity.Recipient().String() {
		t.Fatalf("age %q, want the file's %q", q.Age, identity.Recipient())
	}
	if !strings.Contains(report, "export SOPS_AGE_KEY_FILE=") || !strings.Contains(report, keyFile) {
		t.Errorf("no export line for the key file:\n%s", report)
	}
	if strings.Contains(report, identity.String()) {
		t.Error("the private key was printed")
	}
	if got, _ := os.ReadFile(keyFile); string(got) != identity.String()+"\n" {
		t.Error("the key file changed")
	}
	if len(f.keystore.Items) != 0 {
		t.Errorf("keystore holds %v", f.keystore.Items)
	}
	if recipient, source := recordedAge(t, fixtureDeploymentID); recipient != q.Age || source != keyFile {
		t.Errorf("recorded %q from %q, want %q from %q", recipient, source, q.Age, keyFile)
	}
}

// A directory named as --age-private-key-file is refused as one, with
// nothing made.
func TestAnswerRefusesADirectoryAsKeyFile(t *testing.T) {
	f := newAnswerFixture(t)
	dir := t.TempDir()
	err, out, _ := f.answerShowing(t, dir, "Carol Jones", "carol@example.org", t.TempDir(), strings.NewReader(""))
	if err == nil || !strings.Contains(problemText(err), "is a directory") {
		t.Fatalf("got %q", problemText(err))
	}
	f.nothingMade(t, dir, out)
}

// A SOPS_AGE_* variable's key answers the invite, and nothing is kept for
// it: the keystore stays empty and the record names the variable.
func TestAnswerTakesTheVariablesKey(t *testing.T) {
	f := newAnswerFixture(t)
	identity, _ := age.GenerateX25519Identity()
	t.Setenv("SOPS_AGE_KEY", identity.String())
	dir := t.TempDir()
	err, out, report := f.answerShowing(t, dir, "Carol Jones", "carol@example.org", "", strings.NewReader(""))
	if err != nil {
		t.Fatal(problemText(err))
	}
	q, _, err := operatorinvite.ParseRequest(out)
	if err != nil {
		t.Fatal(err)
	}
	if q.Age != identity.Recipient().String() {
		t.Fatalf("age %q, want the variable's %q", q.Age, identity.Recipient())
	}
	if len(f.keystore.Items) != 0 {
		t.Errorf("keystore holds %v", f.keystore.Items)
	}
	if !strings.Contains(report, "SOPS_AGE_KEY") || strings.Contains(report, identity.String()) {
		t.Errorf("the source is not named, or the key was printed:\n%s", report)
	}
	if recipient, source := recordedAge(t, fixtureDeploymentID); recipient != q.Age || !strings.Contains(source, "SOPS_AGE_KEY") {
		t.Errorf("recorded %q from %q", recipient, source)
	}
}

// At a terminal the name and email are asked for, with create's checks: a
// refused answer is asked again.
func TestAnswerAsksAtATerminal(t *testing.T) {
	f := newAnswerFixture(t)
	dir := t.TempDir()
	stdin := terminalStdin(t, "Carol\nCarol Jones\ncarol@example.org\n", true)
	err, out, _ := f.answerShowing(t, dir, "", "", "", stdin)
	if err != nil {
		t.Fatal(problemText(err))
	}
	q, _, err := operatorinvite.ParseRequest(out)
	if err != nil {
		t.Fatal(err)
	}
	if q.Name != "Carol Jones" || q.Email != "carol@example.org" {
		t.Fatalf("got %q <%s>", q.Name, q.Email)
	}
}

// Once the request is written, the answer says what comes next: send it,
// and join with operator register --execute once it is accepted and
// applied.
func TestAnswerSaysWhatComesNext(t *testing.T) {
	f := newAnswerFixture(t)
	dir := t.TempDir()
	err, _, report := f.answerShowing(t, dir, "Carol Jones", "carol@example.org", "", strings.NewReader(""))
	if err != nil {
		t.Fatal(problemText(err))
	}
	for _, want := range []string{"send the request to alice", "operator add --request", "deployment apply --execute", "operator register --execute"} {
		if !strings.Contains(report, want) {
			t.Errorf("lacks %q:\n%s", want, report)
		}
	}
}

// withDataDir points the workstation's data directory at a directory of the
// test's own.
func withDataDir(t *testing.T) {
	t.Helper()
	t.Setenv(localstore.DirVariable, t.TempDir())
}

// dataDir is the workstation's data directory.
func dataDir(t *testing.T) string {
	t.Helper()
	base, err := localstore.Base()
	if err != nil {
		t.Fatal(err)
	}
	return base
}

// The answer reads and writes nothing in the folder: one holding another
// deployment's files answers as an empty one does, and is left as it was.
func TestAnswerLeavesTheFolderAlone(t *testing.T) {
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
	if err, out := f.answer(dir, f.inviterKey); err != nil || len(out) == 0 {
		t.Fatalf("got %q", problemText(err))
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("the folder holds %d entries, want its one file", len(entries))
	}
}

// otherInvite is an invite to f's deployment for carol's key from another
// operator, dave, and dave's key line.
func (f answerFixture) otherInvite(t *testing.T) ([]byte, string) {
	t.Helper()
	inv, _, err := operatorinvite.InviteSigner(f.invite)
	if err != nil {
		t.Fatal(err)
	}
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
	return operatorinvite.Signed(body, sig), davePub
}

// A second, different invite to a deployment whose invite is pending is
// refused, from any folder, naming the pending invite to remove; the
// pending one is kept. The identical invite answers again.
func TestAnswerRefusesASecondInviteWhilePending(t *testing.T) {
	f := newAnswerFixture(t)
	if err, _ := f.answer(t.TempDir(), f.inviterKey); err != nil {
		t.Fatal(err)
	}
	other, davePub := f.otherInvite(t)
	var out bytes.Buffer
	err := answerInvite(ui.NewPlain(io.Discard, false), t.TempDir(), other, davePub, "Carol Jones", "carol@example.org", "", strings.NewReader(""), &out)
	pending := filepath.Join(dataDir(t), "invites", fixtureDeploymentID, "invite")
	if err == nil || !strings.Contains(problemText(err), ui.ShortPath(pending)) {
		t.Fatalf("got %q", problemText(err))
	}
	if out.Len() != 0 {
		t.Errorf("wrote %q", out.String())
	}
	if got, _ := os.ReadFile(pending); !bytes.Equal(got, f.invite) {
		t.Error("the pending invite was replaced")
	}
	if err, out := f.answer(t.TempDir(), f.inviterKey); err != nil || len(out) == 0 {
		t.Fatalf("the identical invite: %q", problemText(err))
	}
}

// A pending invite is one whose invite file, written last, is there.
func TestReadPendingWithoutInviteIsNotPending(t *testing.T) {
	f := newAnswerFixture(t)
	dir := filepath.Join(dataDir(t), "invites", fixtureDeploymentID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inviter.pub"), []byte(f.inviterKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := readPending(fixtureDeploymentID); ok || err != nil {
		t.Fatalf("ok %v, err %v", ok, err)
	}
	if ids, err := pendingInvites(); err != nil || len(ids) != 0 {
		t.Fatalf("pending %v, %v", ids, err)
	}
	if err, out := f.answer(t.TempDir(), f.inviterKey); err != nil || len(out) == 0 {
		t.Fatalf("answering failed: %v", err)
	}
}

// requestAge is the age key the request in out names.
func requestAge(t *testing.T, out []byte) string {
	t.Helper()
	q, _, err := operatorinvite.ParseRequest(out)
	if err != nil {
		t.Fatal(err)
	}
	return q.Age
}

// A later answer, in another folder, with a SOPS_AGE_KEY set to another key,
// names the key the first answer bound, found in the keystore, and says the
// variable is set beside it.
func TestReanswerUsesTheBoundKeyOverAVariable(t *testing.T) {
	f := newAnswerFixture(t)
	err, out := f.answer(t.TempDir(), f.inviterKey)
	if err != nil {
		t.Fatal(problemText(err))
	}
	first := requestAge(t, out)
	other, _ := age.GenerateX25519Identity()
	t.Setenv("SOPS_AGE_KEY", other.String())
	err, out, report := f.answerShowing(t, t.TempDir(), "Carol Jones", "carol@example.org", "", strings.NewReader(""))
	if err != nil {
		t.Fatal(problemText(err))
	}
	if got := requestAge(t, out); got != first {
		t.Fatalf("age %q, want the bound %q", got, first)
	}
	if !strings.Contains(report, "SOPS_AGE_KEY") {
		t.Errorf("the variable beside the bound key is not said:\n%s", report)
	}
	if len(f.keystore.Items) != 1 {
		t.Errorf("the store holds %d items, want 1", len(f.keystore.Items))
	}
}

// A later answer without the --age-private-key-file the first one read finds
// the bound key in the keystore.
func TestReanswerFindsTheBoundKeyInTheKeystore(t *testing.T) {
	f := newAnswerFixture(t)
	identity, _ := age.GenerateX25519Identity()
	keyFile := filepath.Join(t.TempDir(), "carol.key")
	if err := os.WriteFile(keyFile, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err, _, _ := f.answerShowing(t, t.TempDir(), "Carol Jones", "carol@example.org", keyFile, strings.NewReader("")); err != nil {
		t.Fatal(problemText(err))
	}
	if err := f.keystore.Put(fixtureDeploymentID, identity.String(), identity.Recipient().String()); err != nil {
		t.Fatal(err)
	}
	err, out := f.answer(t.TempDir(), f.inviterKey)
	if err != nil {
		t.Fatal(problemText(err))
	}
	if got := requestAge(t, out); got != identity.Recipient().String() {
		t.Fatalf("age %q, want the bound %q", got, identity.Recipient())
	}
}

// A later answer where no source holds the bound key is refused, naming the
// key and the binding file, and makes no key and writes nothing.
func TestReanswerRefusesWithoutTheBoundKey(t *testing.T) {
	f := newAnswerFixture(t)
	identity, _ := age.GenerateX25519Identity()
	t.Setenv("SOPS_AGE_KEY", identity.String())
	if err, _ := f.answer(t.TempDir(), f.inviterKey); err != nil {
		t.Fatal(problemText(err))
	}
	t.Setenv("SOPS_AGE_KEY", "")
	os.Unsetenv("SOPS_AGE_KEY")
	binding := filepath.Join(dataDir(t), "age", fixtureDeploymentID+".recipient")
	before, _ := os.ReadFile(binding)
	err, out := f.answer(t.TempDir(), f.inviterKey)
	text := problemText(err)
	if err == nil || !strings.Contains(text, identity.Recipient().String()) || !strings.Contains(text, ui.ShortPath(binding)) {
		t.Fatalf("got %q", text)
	}
	if len(out) != 0 || len(f.keystore.Items) != 0 {
		t.Errorf("wrote %q, keystore %v", out, f.keystore.Items)
	}
	if after, _ := os.ReadFile(binding); !bytes.Equal(before, after) {
		t.Error("the binding changed")
	}
}
