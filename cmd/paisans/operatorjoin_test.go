package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/keystore"
)

// joinFixture is carol's workstation after she answered alice's invite: the
// invite and alice's key recorded in dir, carol's ssh key in the agent, and
// her age key in the keystore. The sites serve a history whose root, v1, is
// the invite's, and whose head lists carol when the fixture was made so.
type joinFixture struct {
	dir        string
	root, head string
	carolPub   string
	head2      configstore.Files // the head's files
	runs       *[]string
	report     *bytes.Buffer
}

// carolEntry is an operators entry for carol with key, carrying a comment.
func carolEntry(key string) string {
	return "  carol:\n    name: Carol Jones\n    email: carol@example.org\n    keys: |\n      " + key + " carol@laptop\nsites:\n"
}

// encryptedFixtureSecrets is the fixture's secrets encrypted to recipient,
// as a secrets.enc.yaml holds them.
func encryptedFixtureSecrets(t *testing.T, recipient string) []byte {
	t.Helper()
	secrets, err := config.LoadSecrets(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "secrets.enc.yaml")
	if err := config.WriteSecrets(path, secrets, []string{recipient}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// newJoinFixture makes the history, alice's invite for carol from it, and
// carol's workstation. With listed the head, v2, declares carol; otherwise
// the history is v1 alone. The secrets are encrypted to carol's age key
// when opens is set, else to a key nobody here holds.
func newJoinFixture(t *testing.T, listed, opens bool) joinFixture {
	t.Helper()
	withoutAgeKey(t)
	alicePriv, alicePub := testKey(t)
	carolPriv, carolPub := testKey(t)
	carol, _ := age.GenerateX25519Identity()
	other, _ := age.GenerateX25519Identity()
	recipient := other.Recipient().String()
	if opens {
		recipient = carol.Recipient().String()
	}
	secrets := encryptedFixtureSecrets(t, recipient)
	sops := []byte("creation_rules:\n  - age: " + recipient + "\n")

	s := newStore(t)
	yaml := fixtureAs(t, alicePub)
	f1 := snapshotted(configstore.Files{Paisans: []byte(yaml), Secrets: secrets, SOPS: sops})
	v1, err := s.repo.Commit(f1, nil, 1, alicePriv, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	head, files := v1, f1
	if listed {
		withCarol := strings.Replace(yaml, "\nsites:\n", "\n"+carolEntry(carolPub), 1)
		files = snapshotted(configstore.Files{Paisans: []byte(withCarol), Secrets: secrets, SOPS: sops})
		if head, err = s.repo.Commit(files, []string{v1}, 2, alicePriv, time.Unix(1_800_000_100, 0)); err != nil {
			t.Fatal(err)
		}
	}
	answer := s.answer(t)

	// alice's workstation writes the invite.
	alice := registerWorkstation(t, answer, f1, nil, alicePub)
	if err := alice.SetSigningKey(alicePub); err != nil {
		t.Fatal(err)
	}
	if err := alice.SetRoot(v1); err != nil {
		t.Fatal(err)
	}
	signFromFile(t, alicePriv)
	var invite bytes.Buffer
	if err := runOperatorInvite(inviteArgs(alice, "carol", "--key", carolPub+" carol@laptop"), &invite); err != nil {
		t.Fatal(problemText(err))
	}

	// carol's workstation holds the recorded answer, and nothing else.
	dir := t.TempDir()
	if err := recordJoin(dir, invite.Bytes(), alicePub); err != nil {
		t.Fatal(err)
	}
	var runs []string
	recoverHosts(t, answer)
	allSitesUp(answer, &runs)
	report := plainReporter(t)
	realSecrets(t)
	ks := keystore.NewMemory()
	withKeystore(t, ks)
	if err := ks.Put(fixtureDeploymentID, carol.String(), carol.Recipient().String()); err != nil {
		t.Fatal(err)
	}
	saved, savedSign := agentKeys, configSignWith
	agentKeys = func() []string { return []string{carolPub} }
	configSignWith = func(string) string { return carolPriv }
	t.Cleanup(func() { agentKeys, configSignWith = saved, savedSign })
	return joinFixture{dir: dir, root: v1, head: head, carolPub: carolPub, head2: files, runs: &runs, report: report}
}

func (f joinFixture) register(t *testing.T, rest ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runOperator(append([]string{"register", "--config", filepath.Join(f.dir, config.FileName)}, rest...), noTerminalStdin(), &out)
	return f.report.String() + out.String(), err
}

// wroteNothing fails the test when the join wrote a file or a record into
// the directory, or changed a site.
func (f joinFixture) wroteNothing(t *testing.T) {
	t.Helper()
	if (configstore.Local{Dir: f.dir}).HasFiles() {
		t.Error("files written")
	}
	for _, name := range []string{config.FileName, config.SOPSConfigName, "secrets.enc.yaml"} {
		if _, err := os.Stat(filepath.Join(f.dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s written", name)
		}
	}
	for _, name := range []string{"signing-key", "config-head", "config-root", "config-seen"} {
		if _, err := os.Stat(filepath.Join(f.dir, ".paisans", name)); !os.IsNotExist(err) {
			t.Errorf(".paisans/%s written", name)
		}
	}
	if pushed(*f.runs) {
		t.Errorf("pushed to a site: %q", *f.runs)
	}
}

func wantAskInviter(t *testing.T, err error) {
	t.Helper()
	text := problemText(err)
	if err == nil || !strings.Contains(text, "inviter") || !strings.Contains(text, "deployment apply --execute") {
		t.Fatalf("want the advice to ask the inviter whether they applied: %v (%s)", err, text)
	}
}

// A head that does not list carol is refused before anything is written,
// with the advice to ask the inviter whether they have applied.
func TestJoinBeforeApplyRefused(t *testing.T) {
	f := newJoinFixture(t, false, true)
	_, err := f.register(t, "--execute")
	wantAskInviter(t, err)
	f.wroteNothing(t)
}

// loginRefusedHost is a host that answers and refuses the login.
type loginRefusedHost struct{ storeHost }

func (h *loginRefusedHost) Run(string) (string, error) {
	return "", fmt.Errorf("%s: %w", h.name, apply.ErrLoginRefused)
}
func (h *loginRefusedHost) RunInput(c, _ string) (string, error) { return h.Run(c) }
func (h *loginRefusedHost) ReadFile(string) (string, bool, error) {
	return "", false, fmt.Errorf("%s: %w", h.name, apply.ErrLoginRefused)
}

// Every site refusing carol's login is the same advice: her key reaches the
// sites only once the inviter applied.
func TestJoinEverySiteRefusedTheLogin(t *testing.T) {
	f := newJoinFixture(t, true, true)
	reachDestination = func(dest config.Destination) apply.Transport {
		return &loginRefusedHost{storeHost{name: dest.String()}}
	}
	_, err := f.register(t, "--execute")
	wantAskInviter(t, err)
	f.wroteNothing(t)
}

// With the head listing carol, --execute writes the three files, records
// the invite's root and carol's key, and pushes an attestation; the secrets
// open with her age key, so no remedy is named.
func TestJoinRecoversAndAttests(t *testing.T) {
	f := newJoinFixture(t, true, true)
	out, err := f.register(t, "--execute")
	if err != nil {
		t.Fatal(problemText(err), err)
	}
	l := configstore.Local{Dir: f.dir}
	got, err := l.Files()
	if err != nil || !got.Equal(f.head2) {
		t.Fatalf("files %v", err)
	}
	if r, _ := l.Root(); r != f.root {
		t.Fatalf("root %q, want the invite's %q", r, f.root)
	}
	if k, _ := l.SigningKey(); k != bare(f.carolPub) {
		t.Fatalf("signing key %q", k)
	}
	if h, _ := l.Head(); h == "" || h == f.head {
		t.Fatalf("head %q: no attestation recorded", h)
	}
	if !pushed(*f.runs) {
		t.Fatalf("no attestation pushed: %q", *f.runs)
	}
	if strings.Contains(out, "operator add --request") || !strings.Contains(out, "open the secrets with your age key") {
		t.Fatalf("secrets that open named a remedy, or were not checked:\n%s", out)
	}
	// Once the files are there, register is register.
	if _, err := f.register(t); err != nil {
		t.Fatal(problemText(err))
	}
}

// Sites serving another history, though signed by the same inviter, are
// refused: the invite's root is the one trust starts from.
func TestJoinNeverTrustsOtherRoot(t *testing.T) {
	f := newJoinFixture(t, true, true)
	_, answer, _ := storedHistory(t, 2, 0)
	recoverHosts(t, answer)
	allSitesUp(answer, f.runs)
	f.report.Reset()
	_, err := f.register(t, "--execute")
	if err == nil || !strings.Contains(problemText(err), f.root) || strings.Contains(problemText(err), "--key") {
		t.Fatalf("another root: %v (%s)", err, problemText(err))
	}
	f.wroteNothing(t)
}

// Secrets not encrypted to carol's age key do not refuse the join, since
// the files are hers either way: a failed check names the remedy.
func TestJoinSaysWhenSecretsDoNotOpen(t *testing.T) {
	f := newJoinFixture(t, true, false)
	out, err := f.register(t, "--execute")
	if err != nil {
		t.Fatal(problemText(err), err)
	}
	if !(configstore.Local{Dir: f.dir}).HasFiles() || !pushed(*f.runs) {
		t.Fatal("the join did not complete")
	}
	if !strings.Contains(out, "operator add --request") || !strings.Contains(out, "deployment pull") {
		t.Fatalf("no failed check naming the remedy:\n%s", out)
	}
}

// Without --execute the join reads and says what it would write, and
// writes nothing.
func TestJoinDryRunWritesNothing(t *testing.T) {
	f := newJoinFixture(t, true, true)
	out, err := f.register(t)
	if err != nil {
		t.Fatal(problemText(err), err)
	}
	f.wroteNothing(t)
	if synced(*f.runs) {
		t.Fatalf("a dry run changed a site: %q", *f.runs)
	}
	if !strings.Contains(out, "--execute") {
		t.Fatalf("the dry run does not say what --execute does:\n%s", out)
	}
}

// The invite names the root and the key, so a join takes neither flag.
func TestJoinRefusesRootAndKey(t *testing.T) {
	f := newJoinFixture(t, true, true)
	for _, flag := range []string{"--root", "--key"} {
		_, err := f.register(t, flag, "x")
		if err == nil || !strings.Contains(problemText(err), "join") {
			t.Fatalf("%s: %v", flag, problemText(err))
		}
	}
	f.wroteNothing(t)
}
