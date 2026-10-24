package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/keystore"
	"github.com/paisans-software/paisans-cli/internal/operatorinvite"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// joinFixture is carol's workstation after she answered alice's invite: the
// invite and alice's key pending in the data directory, her age key bound
// there and kept in the keystore, carol's ssh key in the agent, and dir an
// empty folder to join in. The sites serve a history whose root, v1, is
// the invite's, and whose head lists carol when the fixture was made so.
type joinFixture struct {
	dir        string
	root, head string
	carolPub   string
	head2      configstore.Files // the head's files
	answer     string            // what the sites serve
	store      store             // the sites' store
	alicePriv  string
	carolYAML  string // the root's paisans-stack.yaml with carol declared
	runs       *[]string
	report     *bytes.Buffer
}

// joinAgeSource is where the fixture's answer says it found carol's age key.
const joinAgeSource = "the keystore item paisans-age for the fixture"

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
	withDataDir(t)
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
	withCarol := strings.Replace(yaml, "\nsites:\n", "\n"+carolEntry(carolPub), 1)
	if listed {
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

	// carol's workstation holds what her answer recorded, and an empty
	// folder.
	dir := t.TempDir()
	if err := recordAgeBinding(fixtureDeploymentID, ageKey{Recipient: carol.Recipient().String(), Source: joinAgeSource}); err != nil {
		t.Fatal(err)
	}
	if err := recordPending(fixtureDeploymentID, invite.Bytes(), alicePub); err != nil {
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
	return joinFixture{dir: dir, root: v1, head: head, carolPub: carolPub, head2: files, answer: answer, store: s, alicePriv: alicePriv, carolYAML: withCarol, runs: &runs, report: report}
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
	if _, err := os.Stat(filepath.Join(dataDir(t), "invites", fixtureDeploymentID)); !os.IsNotExist(err) {
		t.Errorf("the pending invite was kept after the join: %v", err)
	}
	if recipient, _ := recordedAge(t, fixtureDeploymentID); recipient == "" {
		t.Error("the age binding was removed")
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
	if strings.Contains(out, joinAgeSource) {
		t.Fatalf("secrets not encrypted to carol were blamed on her workstation:\n%s", out)
	}
}

// Secrets encrypted to the age key the answer recorded, that do not open
// here, are this workstation's to fix: the check names the key, where the
// answer found it and how sops is pointed at a key, and does not send the
// inviter to accept again.
func TestJoinSaysWhenYourKeyIsNotReached(t *testing.T) {
	f := newJoinFixture(t, true, true)
	withKeystore(t, keystore.NewMemory())
	out, err := f.register(t, "--execute")
	if err != nil {
		t.Fatal(problemText(err), err)
	}
	if !(configstore.Local{Dir: f.dir}).HasFiles() || !pushed(*f.runs) {
		t.Fatal("the join did not complete")
	}
	recipient, _, _, err := readAgeBinding(fixtureDeploymentID)
	if err != nil || recipient == "" {
		t.Fatalf("no recorded age key: %v", err)
	}
	for _, want := range []string{"they do not open", recipient, joinAgeSource, "what stops them opening is on this workstation", "SOPS_AGE_KEY_FILE", "SOPS_AGE_KEY_CMD"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "operator add --request") || strings.Contains(out, "does not reach that key") {
		t.Errorf("a local failure sent the inviter to accept again, or guessed its cause:\n%s", out)
	}
}

// A join refused once the gather ran says no configuration file was
// written, rather than nothing, since the gather filled the store.
func TestJoinRefusalSaysNoConfigurationFileWritten(t *testing.T) {
	f := newJoinFixture(t, false, true)
	_, err := f.register(t, "--execute")
	if !strings.Contains(problemText(err), "No configuration file was written") {
		t.Fatalf("got %s", problemText(err))
	}
}

// A refused login can also be a host that is not the deployment's site, so
// the advice names the invite's hosts beside asking the inviter.
func TestJoinRefusedLoginNamesTheInvitesHosts(t *testing.T) {
	f := newJoinFixture(t, true, true)
	reachDestination = func(dest config.Destination) apply.Transport {
		if dest.Host == "home-a.example.org" {
			return &storeHost{name: dest.String(), down: true}
		}
		return &loginRefusedHost{storeHost{name: dest.String()}}
	}
	_, err := f.register(t, "--execute")
	wantAskInviter(t, err)
	inv, _, _ := pendingInvite(fixtureDeploymentID)
	text := problemText(err)
	for _, s := range inv.Sites {
		host := config.Destination{User: s.User, Host: s.Host, Port: s.Port}.String()
		if !strings.Contains(text, host) {
			t.Errorf("lacks the invite's host %s: %s", host, text)
		}
	}
	if !strings.Contains(text, "deployment's sites") {
		t.Errorf("does not say to check the hosts are the sites: %s", text)
	}
	f.wroteNothing(t)
}

// A fork among the sites is said as one, before either head is judged, so a
// head that does not list carol does not read as an acceptance not applied.
func TestJoinSaysTheSitesHoldAFork(t *testing.T) {
	f := newJoinFixture(t, false, true)
	root, err := f.store.repo.Files(f.root)
	if err != nil {
		t.Fatal(err)
	}
	listing := snapshotted(configstore.Files{Paisans: []byte(f.carolYAML), Secrets: root.Secrets, SOPS: root.SOPS})
	other := snapshotted(configstore.Files{Paisans: append(append([]byte{}, root.Paisans...), "# other\n"...), Secrets: root.Secrets, SOPS: root.SOPS})
	for i, files := range []configstore.Files{listing, other} {
		if _, err := f.store.repo.Commit(files, []string{f.root}, 2, f.alicePriv, time.Unix(1_800_000_100+int64(i), 0)); err != nil {
			t.Fatal(err)
		}
	}
	answer := f.store.answer(t)
	recoverHosts(t, answer)
	allSitesUp(answer, f.runs)
	_, err = f.register(t, "--execute")
	text := problemText(err)
	if err == nil || !strings.Contains(text, "the sites hold a fork") || strings.Contains(text, "do not know you") {
		t.Fatalf("got %s", text)
	}
	for _, want := range []string{"deployment publish --merge", "operator register --execute here again", "No configuration file was written"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q: %s", want, text)
		}
	}
	if strings.Contains(text, "Merge their files by hand") {
		t.Errorf("the join says to merge by hand: %s", text)
	}
	f.wroteNothing(t)
	if _, statErr := os.Stat(configstore.Local{Dir: f.dir}.Incoming()); !os.IsNotExist(statErr) {
		t.Error("a head's files were written")
	}
}

// A join whose attestation push failed has written the files, so running
// register again is plain register, which signs and pushes.
func TestJoinRerunAfterFailedPush(t *testing.T) {
	f := newJoinFixture(t, true, true)
	answer := f.answer
	configSiteTransport = func(name string, _ config.Site) apply.Transport {
		return &storeHost{name: name, answer: answer, runs: f.runs, noPush: true}
	}
	if _, err := f.register(t, "--execute"); err == nil {
		t.Fatal("the push did not fail")
	}
	l := configstore.Local{Dir: f.dir}
	if !l.HasFiles() {
		t.Fatal("the files were not written before the push")
	}
	*f.runs = nil
	allSitesUp(answer, f.runs)
	if out, err := f.register(t, "--execute"); err != nil {
		t.Fatalf("%s\n%s", problemText(err), out)
	}
	if !pushed(*f.runs) {
		t.Fatalf("the re-run pushed nothing: %q", *f.runs)
	}
	if h, _ := l.Head(); h == "" || h == f.head {
		t.Fatalf("head %q: no attestation recorded", h)
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
		_, err := f.register(t, "--id", fixtureDeploymentID, flag, "x")
		if err == nil || !strings.Contains(problemText(err), "join") {
			t.Fatalf("%s: %v", flag, problemText(err))
		}
	}
	f.wroteNothing(t)
}

// The five steps on two workstations, through the commands themselves: alice
// invites carol, carol answers, alice accepts, alice's files are published
// as the sites' newest version, and carol joins. The age key carol's request
// named opens the joined secrets, and her key signs from then on.
func TestInviteAnswerAcceptJoin(t *testing.T) {
	withoutAgeKey(t)
	withDataDir(t)
	alicePriv, alicePub := testKey(t)
	carolPriv, carolPub := testKey(t)
	aliceAge, _ := age.GenerateX25519Identity()
	aliceKeys, carolKeys := keystore.NewMemory(), keystore.NewMemory()
	if err := aliceKeys.Put(fixtureDeploymentID, aliceAge.String(), aliceAge.Recipient().String()); err != nil {
		t.Fatal(err)
	}

	s := newStore(t)
	f1 := snapshotted(configstore.Files{
		Paisans: []byte(fixtureAs(t, alicePub)),
		Secrets: encryptedFixtureSecrets(t, aliceAge.Recipient().String()),
		SOPS:    []byte("creation_rules:\n  - age: " + aliceAge.Recipient().String() + "\n"),
	})
	v1, err := s.repo.Commit(f1, nil, 1, alicePriv, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	alice := registerWorkstation(t, s.answer(t), f1, nil, alicePub)
	if err := alice.SetSigningKey(alicePub); err != nil {
		t.Fatal(err)
	}
	if err := alice.SetRoot(v1); err != nil {
		t.Fatal(err)
	}
	realSecrets(t)
	saved, savedSign := agentKeys, configSignWith
	t.Cleanup(func() { agentKeys, configSignWith = saved, savedSign })
	as := func(keys *keystore.Memory, priv, pub string) {
		config.AgeKeystore = keys
		agentKeys = func() []string { return []string{pub} }
		configSignWith = func(string) string { return priv }
		signFromFile(t, priv)
	}
	withKeystore(t, aliceKeys)

	// 1. alice invites carol.
	as(aliceKeys, alicePriv, alicePub)
	var invite bytes.Buffer
	if err := runOperatorInvite(inviteArgs(alice, "carol", "--key", carolPub), &invite); err != nil {
		t.Fatal(problemText(err))
	}
	inviteFile := filepath.Join(t.TempDir(), "carol.invite")
	if err := os.WriteFile(inviteFile, invite.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	// 2. carol answers it on her own workstation, in one folder.
	as(carolKeys, carolPriv, carolPub)
	var request bytes.Buffer
	if err := runOperatorRegister([]string{"--invite", inviteFile, "--inviter-key", alicePub, "--name", "Carol Jones", "--email", "carol@example.org", "--config", filepath.Join(t.TempDir(), config.FileName)}, noTerminalStdin(), &request); err != nil {
		t.Fatal(problemText(err))
	}
	q, _, err := operatorinvite.ParseRequest(request.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	requestFile := filepath.Join(t.TempDir(), "carol.request")
	if err := os.WriteFile(requestFile, request.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	// 3. alice accepts it.
	as(aliceKeys, alicePriv, alicePub)
	if err := runOperatorAdd([]string{"--request", requestFile, "--config", filepath.Join(alice.Dir, config.FileName), "--all-sites"}, notATerminal(t), &bytes.Buffer{}); err != nil {
		t.Fatal(problemText(err))
	}

	// 4. alice's files become the sites' newest version, as apply publishes
	// them.
	accepted, err := alice.Files()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.repo.Commit(snapshotted(accepted), []string{v1}, 2, alicePriv, time.Unix(1_800_000_100, 0)); err != nil {
		t.Fatal(err)
	}
	answer := s.answer(t)
	var runs []string
	recoverHosts(t, answer)
	allSitesUp(answer, &runs)
	report := plainReporter(t)

	// 5. carol joins, in another folder.
	as(carolKeys, carolPriv, carolPub)
	dir := t.TempDir()
	carolConfig := filepath.Join(dir, config.FileName)
	if err := runOperator([]string{"register", "--config", carolConfig, "--execute"}, noTerminalStdin(), &bytes.Buffer{}); err != nil {
		t.Fatalf("%s\n%s", problemText(err), report.String())
	}
	l := configstore.Local{Dir: dir}
	if k, _ := l.SigningKey(); k != bare(carolPub) {
		t.Fatalf("signing key %q, want carol's", k)
	}
	if r, _ := l.Root(); r != v1 {
		t.Fatalf("root %q, want the invite's %q", r, v1)
	}
	if !pushed(runs) {
		t.Fatal("no attestation pushed")
	}
	joined, err := os.ReadFile(filepath.Join(dir, "secrets.enc.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(config.FileRecipients(joined), q.Age) {
		t.Fatalf("the joined secrets are not encrypted to the request's age key %s", q.Age)
	}
	if _, err := config.LoadDeploymentSecrets(filepath.Join(dir, "secrets.enc.yaml"), fixtureDeploymentID); err != nil {
		t.Fatalf("carol's age key does not open the joined secrets: %v", err)
	}
	if !strings.Contains(report.String(), "open the secrets with your age key") || strings.Contains(report.String(), "they do not open") {
		t.Fatalf("the join's check failed:\n%s", report.String())
	}
}

// With invites to two deployments pending, an empty folder's join is refused
// naming both, and --id selects one; an --id with no pending invite is
// refused.
func TestJoinSelectsAPendingInvite(t *testing.T) {
	f := newJoinFixture(t, true, true)
	const otherID = "11111111-2222-4333-8444-555555555555"
	if err := recordPending(otherID, []byte("an invite\n"), f.carolPub); err != nil {
		t.Fatal(err)
	}
	_, err := f.register(t)
	text := problemText(err)
	if err == nil || !strings.Contains(text, fixtureDeploymentID) || !strings.Contains(text, otherID) || !strings.Contains(text, "--id") {
		t.Fatalf("got %s", text)
	}
	invites := filepath.Join(dataDir(t), "invites")
	for _, id := range []string{fixtureDeploymentID, otherID} {
		if !strings.Contains(text, ui.ShortPath(filepath.Join(invites, id))) {
			t.Errorf("does not name the pending directory of %s: %s", id, text)
		}
	}
	if !strings.Contains(text, "abandons") {
		t.Errorf("does not say removing one abandons it: %s", text)
	}
	f.wroteNothing(t)
	_, err = f.register(t, "--id", "22222222-2222-4333-8444-555555555555")
	text = problemText(err)
	if err == nil || !strings.Contains(text, "22222222-2222-4333-8444-555555555555") || !strings.Contains(text, ui.ShortPath(filepath.Join(invites, otherID))) || !strings.Contains(text, "abandons") {
		t.Fatalf("an id with no pending invite: %v", text)
	}
	out, err := f.register(t, "--id", fixtureDeploymentID)
	if err != nil {
		t.Fatal(problemText(err))
	}
	if !strings.Contains(out, "join ") {
		t.Fatalf("--id did not join:\n%s", out)
	}
}

// --id selects a pending invite for a folder without files; one with files
// registers, and takes none.
func TestRegisterRefusesIDBesideFiles(t *testing.T) {
	f := newJoinFixture(t, true, true)
	if _, err := f.register(t, "--execute"); err != nil {
		t.Fatal(problemText(err))
	}
	if _, err := f.register(t, "--id", fixtureDeploymentID); err == nil || !strings.Contains(problemText(err), "--id") {
		t.Fatalf("got %v", problemText(err))
	}
}

// --key or --root without --id registers plainly, whatever is pending: a
// pinned root in an empty folder reads the sites from this workstation's
// store, which holds nothing here.
func TestRegisterWithRootSkipsPendingInvites(t *testing.T) {
	f := newJoinFixture(t, true, true)
	plain := func(t *testing.T) {
		t.Helper()
		_, err := f.register(t, "--root", f.root)
		text := problemText(err)
		if err == nil || !strings.Contains(text, "pinned root is not in this workstation's store") || strings.Contains(text, "join") || strings.Contains(text, "pending") {
			t.Fatalf("got %s", text)
		}
	}
	plain(t)
	if err := recordPending("11111111-2222-4333-8444-555555555555", []byte("an invite\n"), f.carolPub); err != nil {
		t.Fatal(err)
	}
	plain(t)
	f.wroteNothing(t)
}

// A pending invite is used only for the deployment whose directory holds
// it.
func TestJoinRefusesAnInviteInAnotherDeploymentsDirectory(t *testing.T) {
	f := newJoinFixture(t, true, true)
	invite, inviter, ok, err := readPending(fixtureDeploymentID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	const otherID = "11111111-2222-4333-8444-555555555555"
	if err := recordPending(otherID, invite, inviter); err != nil {
		t.Fatal(err)
	}
	_, err = f.register(t, "--id", otherID)
	if text := problemText(err); err == nil || !strings.Contains(text, otherID) || !strings.Contains(text, fixtureDeploymentID) {
		t.Fatalf("got %s", text)
	}
	f.wroteNothing(t)
}

// An --id that is not a deployment id is refused as one.
func TestJoinRefusesAnIDThatIsNotOne(t *testing.T) {
	f := newJoinFixture(t, true, true)
	_, err := f.register(t, "--id", "../x")
	var p *ui.Problem
	if !errors.As(err, &p) || !strings.Contains(p.Hint, "--id") {
		t.Fatalf("got %v", err)
	}
	f.wroteNothing(t)
}

// A pending invite that cannot be removed once the files are written is
// warned about, and the join goes on to sign.
func TestJoinWarnsWhenThePendingInviteStays(t *testing.T) {
	f := newJoinFixture(t, true, true)
	saved := removePendingDir
	removePendingDir = func(string) error { return os.ErrPermission }
	t.Cleanup(func() { removePendingDir = saved })
	out, err := f.register(t, "--execute")
	if err != nil {
		t.Fatal(problemText(err))
	}
	if !pushed(*f.runs) {
		t.Fatal("no attestation pushed")
	}
	if !strings.Contains(out, ui.ShortPath(filepath.Join(dataDir(t), "invites", fixtureDeploymentID))) {
		t.Errorf("the warning does not name the pending directory:\n%s", out)
	}
}
