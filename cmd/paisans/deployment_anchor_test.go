package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// agentHolding makes the ssh-agent hold lines for the rest of the test.
func agentHolding(t *testing.T, lines ...string) {
	t.Helper()
	saved := agentKeys
	agentKeys = func() []string { return lines }
	t.Cleanup(func() { agentKeys = saved })
}

// fingerprint is a public key line's fingerprint.
func fingerprint(t *testing.T, line string) string {
	t.Helper()
	k, _, err := config.ParseKeyLine(line)
	if err != nil {
		t.Fatal(err)
	}
	return k.Fingerprint
}

// pubFile writes a public key line to a .pub file and returns its path.
func pubFile(t *testing.T, line string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "id.pub")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// recoverArgs is a recovery from the fixture's host into dir, with more.
func recoverArgs(dir string, more ...string) []string {
	return append([]string{"recover", "--from", "ubuntu@203.0.113.9", "--config", filepath.Join(dir, "paisans-stack.yaml")}, more...)
}

// operatorsAs is the fixture's paisans-stack.yaml with alice's key replaced by
// alice and bob's by bob.
func operatorsAs(t *testing.T, alice, bob string) string {
	t.Helper()
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	return strings.Replace(fixtureAs(t, alice), cfg.Operators["bob"].KeyLines()[0], bob, 1)
}

// wantAdvice fails unless the refusal err is the missing anchor's, with the
// advice an empty workstation can follow.
func wantAdvice(t *testing.T, err error) {
	t.Helper()
	text := problemText(err)
	for _, want := range []string{"no version to start trusting from", "--key <path.pub>", "ssh-add", "--root <sha>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("refusal %q does not say %q", text, want)
		}
	}
}

// On an empty workstation, with no root pinned, the key --key names anchors
// on the newest version it signed, the gather names it, and --execute
// records it, since the agent holds it and an operator of the head lists it.
func TestConfigRecoverAnchorsOnTheNamedKey(t *testing.T) {
	withDataDir(t)
	shas, answer, files, _, pub := storedHistoryKey(t, 2, 1)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	_, unrelated := testKey(t)
	agentHolding(t, unrelated, pub)
	var rep bytes.Buffer
	reporterOverride = ui.NewPlain(&rep, false)
	dir := t.TempDir()
	key := pubFile(t, pub)
	var out bytes.Buffer
	if err := runDeployment(recoverArgs(dir, "--key", key), noTerminalStdin(), &out); err != nil {
		t.Fatalf("dry run: %v", problemText(err))
	}
	if !strings.Contains(rep.String(), "anchored on "+fingerprint(t, pub)) {
		t.Fatalf("the gather does not name the key that anchored it:\n%s", rep.String())
	}
	if !strings.Contains(rep.String(), "confirm the root "+shas[0]+" with another operator") {
		t.Fatalf("recover does not ask to confirm the full root hash:\n%s", rep.String())
	}
	l := configstore.Local{Dir: dir}
	if l.HasFiles() {
		t.Fatal("a dry run wrote the files")
	}
	if err := runDeployment(recoverArgs(dir, "--key", key, "--execute"), noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
	}
	l = heldCopy(t)
	if got, _ := l.Files(); !got.Equal(files[1]) {
		t.Fatal("recover did not write the newest version's files")
	}
	if h, _ := l.Head(); h != shas[1] {
		t.Fatalf("head %s, want %s", h, shas[1])
	}
	if r, _ := l.Root(); r != shas[0] {
		t.Fatalf("root %s, want %s", r, shas[0])
	}
	if k, _ := l.SigningKey(); k != pub {
		t.Fatalf("signing key %q, want %q", k, pub)
	}
}

// Without --key, no key in the agent is tried, even one that signed every
// version: a recovery needs --key or --root, and says so.
func TestConfigRecoverWithoutKeyIgnoresTheAgent(t *testing.T) {
	withDataDir(t)
	_, answer, _, _, pub := storedHistoryKey(t, 1, 0)
	recoverHosts(t, answer)
	_, attacker := testKey(t)
	agentHolding(t, attacker, pub)
	dir := t.TempDir()
	var out bytes.Buffer
	err := runDeployment(recoverArgs(dir, "--execute"), noTerminalStdin(), &out)
	wantAdvice(t, err)
	if strings.Contains(problemText(err), "operator register") {
		t.Fatalf("refusal %q suggests operator register, which needs the files", problemText(err))
	}
	if (configstore.Local{Dir: dir}).HasFiles() {
		t.Fatal("a refused recovery wrote the files")
	}
	noSystemCopy(t)
}

// --key with a public key the agent does not hold, as a colleague's,
// anchors and is never recorded; --key that differs from a recorded key is
// refused, and a file that is no public key is refused.
func TestConfigRecoverAnchorsOnKeyFile(t *testing.T) {
	withDataDir(t)
	shas, answer, _, _, pub := storedHistoryKey(t, 2, 0)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	path := pubFile(t, pub)
	dir := t.TempDir()
	var out bytes.Buffer
	if err := runDeployment(recoverArgs(dir, "--key", path, "--execute"), noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
	}
	l := heldCopy(t)
	if h, _ := l.Head(); h != shas[1] {
		t.Fatalf("head %s, want %s", h, shas[1])
	}
	if k, _ := l.SigningKey(); k != "" {
		t.Fatalf("a key the agent does not hold was recorded: %q", k)
	}

	_, mine := testKey(t)
	if err := l.SetSigningKey(mine); err != nil {
		t.Fatal(err)
	}
	if err := mirrorSystemCopy(t, l).SetSigningKey(mine); err != nil {
		t.Fatal(err)
	}
	err := runDeployment([]string{"log", "--config", filepath.Join(dir, "paisans-stack.yaml"), "--key", path}, noTerminalStdin(), &out)
	if !strings.Contains(problemText(err), "--key differs from the recorded signing key") {
		t.Fatalf("got %v", problemText(err))
	}

	if err := os.WriteFile(path, []byte("not a key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = runDeployment(recoverArgs(t.TempDir(), "--key", path), noTerminalStdin(), &out)
	if !strings.Contains(problemText(err), "--key must name a public key file") {
		t.Fatalf("got %v", err)
	}
}

// A pinned root anchors alone: a named key whose versions are under another
// root neither blocks nor overrides it, the run says it was ignored, and it
// is not recorded.
func TestConfigRecoverPinnedRootWins(t *testing.T) {
	withDataDir(t)
	key, pub := testKey(t)
	okey, opub := testKey(t)
	s := newStore(t)
	other, f := s.commit(t, fixtureAs(t, opub), "# other\n", nil, 1, okey)
	root, _ := s.commit(t, fixtureAs(t, pub), "", nil, 1, key)
	s.commit(t, fixtureAs(t, pub), "# edited\n", []string{root}, 2, key)
	answer := s.answer(t)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	agentHolding(t, pub)
	var rep bytes.Buffer
	reporterOverride = ui.NewPlain(&rep, false)
	dir := t.TempDir()
	var out bytes.Buffer
	if err := runDeployment(recoverArgs(dir, "--root", other, "--key", pubFile(t, pub), "--execute"), noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
	}
	if !strings.Contains(rep.String(), "--key ignored: a root is pinned") {
		t.Fatalf("no note that --key was ignored:\n%s", rep.String())
	}
	l := heldCopy(t)
	if got, _ := l.Files(); !got.Equal(f) {
		t.Fatal("recover did not write the pinned root's files")
	}
	if k, _ := l.SigningKey(); k != "" {
		t.Fatalf("the named key was recorded under another root's pin: %q", k)
	}
}

// A named key that signed versions under two roots is refused: which is this
// deployment's is for --root to say. Nothing is written or recorded.
func TestConfigRecoverNamedKeyUnderTwoRootsIsRefused(t *testing.T) {
	withDataDir(t)
	akey, apub := testKey(t)
	_, bpub := testKey(t)
	_, cpub := testKey(t)
	s := newStore(t)
	ra, _ := s.commit(t, operatorsAs(t, apub, cpub), "", nil, 1, akey)
	s.commit(t, operatorsAs(t, apub, cpub), "# a\n", []string{ra}, 2, akey)
	rb, _ := s.commit(t, operatorsAs(t, apub, bpub), "# b\n", nil, 1, akey)
	s.commit(t, operatorsAs(t, apub, bpub), "# b2\n", []string{rb}, 2, akey)
	answer := s.answer(t)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	agentHolding(t, apub)
	dir := t.TempDir()
	var out bytes.Buffer
	err := runDeployment(recoverArgs(dir, "--key", pubFile(t, apub), "--execute"), noTerminalStdin(), &out)
	if !strings.Contains(problemText(err), "more than one root") || !strings.Contains(problemText(err), "--root <sha>") {
		t.Fatalf("got %v", problemText(err))
	}
	l := configstore.Local{Dir: dir}
	if l.HasFiles() {
		t.Fatal("a refused recovery wrote the files")
	}
	noSystemCopy(t)
	if k, _ := l.SigningKey(); k != "" {
		t.Fatalf("a refused recovery recorded the signing key %q", k)
	}
}

// A host serving only an older incarnation of the deployment, where a
// co-operator since removed by a redeploy is still listed and signed on
// after the operator's last version, gets nothing trusted from --key alone:
// the key trusts only versions it signed, and the refusal asks for --root.
func TestConfigRecoverNamedKeyRefusesAnOldIncarnationsNewerVersion(t *testing.T) {
	withDataDir(t)
	akey, apub := testKey(t)
	mkey, mpub := testKey(t)
	s := newStore(t)
	yaml := operatorsAs(t, apub, mpub)
	o1, _ := s.commit(t, yaml, "", nil, 1, akey)
	o2, _ := s.commit(t, yaml, "# alice\n", []string{o1}, 2, akey)
	s.commit(t, yaml, "# mallory\n", []string{o2}, 3, mkey)
	answer := s.answer(t)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	agentHolding(t, apub)
	dir := t.TempDir()
	var out bytes.Buffer
	err := runDeployment(recoverArgs(dir, "--key", pubFile(t, apub), "--execute"), noTerminalStdin(), &out)
	if !strings.Contains(problemText(err), "not by the key --key names") || !strings.Contains(problemText(err), "--root <sha>") {
		t.Fatalf("got %v", problemText(err))
	}
	l := configstore.Local{Dir: dir}
	if l.HasFiles() {
		t.Fatal("a refused recovery wrote the files")
	}
	noSystemCopy(t)
	if k, _ := l.SigningKey(); k != "" {
		t.Fatalf("a refused recovery recorded the signing key %q", k)
	}
	if r, _ := l.Root(); r != "" {
		t.Fatalf("a refused recovery pinned %s", r)
	}
}

// Every version a named key leads to trusting must declare the deployment
// being recovered: a history that names another id is refused.
func TestConfigRecoverNamedKeyRefusesAnotherDeploymentsVersion(t *testing.T) {
	withDataDir(t)
	key, pub := testKey(t)
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	yaml := fixtureAs(t, pub)
	elsewhere := strings.Replace(yaml, "id: "+cfg.ID, "id: 0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d", 1)
	s := newStore(t)
	r, _ := s.commit(t, elsewhere, "", nil, 1, key)
	s.commit(t, yaml, "# here\n", []string{r}, 2, key)
	answer := s.answer(t)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	agentHolding(t, pub)
	dir := t.TempDir()
	var out bytes.Buffer
	err = runDeployment(recoverArgs(dir, "--key", pubFile(t, pub), "--execute"), noTerminalStdin(), &out)
	if !strings.Contains(problemText(err), "declares deployment 0a1b2c3d") {
		t.Fatalf("got %v", problemText(err))
	}
	if (configstore.Local{Dir: dir}).HasFiles() {
		t.Fatal("a refused recovery wrote the files")
	}
	noSystemCopy(t)
}

// --key naming a key a version removed is refused, even though the agent
// holds it and the removed key signed a version on the removal; the current
// key recovers the removal's line and is recorded.
func TestConfigRecoverNamedRemovedKeyIsRefused(t *testing.T) {
	withDataDir(t)
	oldKey, oldPub := testKey(t)
	newKey, newPub := testKey(t)
	_, cpub := testKey(t)
	s := newStore(t)
	both := operatorsAs(t, oldPub, newPub)
	v1, _ := s.commit(t, both, "", nil, 1, oldKey)
	v2, f2 := s.commit(t, operatorsAs(t, newPub, cpub), "# removes old\n", []string{v1}, 2, newKey)
	s.commit(t, both, "# by the removed key\n", []string{v2}, 3, oldKey)
	answer := s.answer(t)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	agentHolding(t, oldPub, newPub)

	dir := t.TempDir()
	var out bytes.Buffer
	err := runDeployment(recoverArgs(dir, "--key", pubFile(t, oldPub), "--execute"), noTerminalStdin(), &out)
	if !strings.Contains(problemText(err), "is not a key of an operator") {
		t.Fatalf("got %v", problemText(err))
	}
	l := configstore.Local{Dir: dir}
	if l.HasFiles() {
		t.Fatal("a refused recovery wrote the files")
	}
	noSystemCopy(t)
	if k, _ := l.SigningKey(); k != "" {
		t.Fatalf("a refused recovery recorded the signing key %q", k)
	}

	dir = t.TempDir()
	if err := runDeployment(recoverArgs(dir, "--key", pubFile(t, newPub), "--execute"), noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
	}
	l = heldCopy(t)
	if got, _ := l.Files(); !got.Equal(f2) {
		t.Fatal("recover wrote another version's files")
	}
	if k, _ := l.SigningKey(); k != newPub {
		t.Fatalf("signing key %q, want %q", k, newPub)
	}
}

// pull, on a workstation with files and no anchor recorded, refuses without
// --key or --root, whatever the agent holds, and with --key anchors on it
// and records it.
func TestConfigPullAnchorsOnTheNamedKey(t *testing.T) {
	shas, answer, files, _, pub := storedHistoryKey(t, 2, 0)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	agentHolding(t, pub)
	l := pullWorkstation(t, answer, "", shas[0], files[0])
	allSitesUp(answer, nil)
	cfgPath := l.cfg
	var out bytes.Buffer
	wantAdvice(t, runDeployment([]string{"pull", "--config", cfgPath, "--execute"}, noTerminalStdin(), &out))
	if err := runDeployment([]string{"pull", "--config", cfgPath, "--key", pubFile(t, pub), "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
	}
	if h, _ := l.Head(); h != shas[1] {
		t.Fatalf("head %s, want %s", h, shas[1])
	}
	if k, _ := l.SigningKey(); k != pub {
		t.Fatalf("signing key %q, want %q", k, pub)
	}
}

// log, on a workstation with files and no anchor recorded, refuses without
// --key or --root, and with --key judges from it, names it, and records
// nothing.
func TestConfigLogAnchorsOnTheNamedKey(t *testing.T) {
	shas, answer, files, _, pub := storedHistoryKey(t, 2, 0)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	agentHolding(t, pub)
	w := newWorkstation(t, files[0])
	l := w.sys
	cfgPath := w.config()
	var out bytes.Buffer
	wantAdvice(t, runDeployment([]string{"log", "--config", cfgPath}, noTerminalStdin(), &out))
	var rep bytes.Buffer
	reporterOverride = ui.NewPlain(&rep, false)
	out.Reset()
	if err := runDeployment([]string{"log", "--config", cfgPath, "--key", pubFile(t, pub)}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
	}
	if !strings.Contains(rep.String(), "anchored on "+fingerprint(t, pub)) {
		t.Fatalf("the gather does not name the key that anchored it:\n%s", rep.String())
	}
	if !strings.Contains(out.String(), shas[1][:12]+"  ") || !strings.Contains(out.String(), "valid, head") {
		t.Fatalf("log:\n%s", out.String())
	}
	if k, _ := l.SigningKey(); k != "" {
		t.Fatalf("log recorded the signing key %q", k)
	}
	noFolderState(t, w.dir)
}

// publish of a deployment the sites hold and this workstation has no
// system copy of refuses, naming the recovery and the join that make one:
// trust starts only from what those or a deploy recorded here.
func TestConfigPublishWithoutAnchorAdvises(t *testing.T) {
	withDataDir(t)
	key, pub := testKey(t)
	s := newStore(t)
	_, f := s.commit(t, fixtureAs(t, pub), "", nil, 1, key)
	answer := s.answer(t)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	l := configstore.Local{Dir: t.TempDir()}
	if err := l.WriteFiles(f); err != nil {
		t.Fatal(err)
	}
	agentHolding(t, pub)
	cfgPath := filepath.Join(l.Dir, "paisans-stack.yaml")
	for _, args := range [][]string{{"publish", "--config", cfgPath}, {"publish", "--config", cfgPath, "--execute"}} {
		var out bytes.Buffer
		text := problemText(runDeployment(args, noTerminalStdin(), &out))
		for _, want := range []string{"no version to start trusting from", "paisans deployment recover --from <user@host> --id " + fixtureDeploymentID, "paisans operator register --execute"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%v: refusal %q does not say %q", args, text, want)
			}
		}
	}
}

// log prints a root's full hash, which --root takes.
func TestConfigLogPrintsTheRootsFullHash(t *testing.T) {
	shas, answer, files := storedHistory(t, 2, 0)
	l := pullWorkstation(t, answer, shas[0], shas[0], files[0])
	var out bytes.Buffer
	if err := runDeployment([]string{"log", "--config", l.cfg}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
	}
	if !strings.Contains(out.String(), "root "+shas[0]) || strings.Contains(out.String(), "root "+shas[1]) {
		t.Fatalf("log:\n%s", out.String())
	}
}

// incarnations is an older incarnation of the fixture deployment, o1 to o3,
// and a newer one after a redeploy under the same id, n1 to n2, all signed by
// one key, each in a store of its own as a site holding only it answers.
type incarnations struct {
	pub                  string
	o1, o3, n1, n2       string
	f3, fn2              configstore.Files
	oldAnswer, newAnswer string
}

func newIncarnations(t *testing.T) incarnations {
	t.Helper()
	key, pub := testKey(t)
	yaml := fixtureAs(t, pub)
	old, cur := newStore(t), newStore(t)
	var in incarnations
	in.pub = pub
	in.o1, _ = old.commit(t, yaml, "# old\n", nil, 1, key)
	o2, _ := old.commit(t, yaml, "# old 2\n", []string{in.o1}, 2, key)
	in.o3, in.f3 = old.commit(t, yaml, "# old 3\n", []string{o2}, 3, key)
	in.n1, _ = cur.commit(t, yaml, "# new\n", nil, 1, key)
	in.n2, in.fn2 = cur.commit(t, yaml, "# new 2\n", []string{in.n1}, 2, key)
	in.oldAnswer, in.newAnswer = old.answer(t), cur.answer(t)
	return in
}

// sitesServe makes home-a answer with first and every other site with rest.
func sitesServe(first, rest string) {
	configSiteTransport = func(name string, _ config.Site) apply.Transport {
		if name == "home-a" {
			return &storeHost{name: name, answer: first}
		}
		return &storeHost{name: name, answer: rest}
	}
}

// onOldIncarnation is a workstation that recorded the key, pinned the older
// incarnation's root and pulled its head, so its cache, which never forgets,
// holds that line and its seen record is its revision 3.
func onOldIncarnation(t *testing.T, in incarnations) (configstore.Local, string) {
	t.Helper()
	l := pullWorkstation(t, in.oldAnswer, in.o1, in.o3, in.f3)
	if err := l.SetSigningKey(in.pub); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runDeployment([]string{"pull", "--config", l.cfg, "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
	}
	if rev, sha, _ := l.Seen(); rev != 3 || sha != in.o3 {
		t.Fatalf("seen %d %s, want 3 %s", rev, sha, in.o3)
	}
	return l.Local, l.cfg
}

// A workstation pinned to the older incarnation, whose sites now serve only
// the newer one after a redeploy, refuses rather than judge from its cache,
// names no root the sites served, and says to recover from the current root.
// A different --root is refused against the pin, and no refusal lowers the
// seen record. Following the advice recovers the current line.
func TestConfigPullWhenTheSitesServeNoneOfThePin(t *testing.T) {
	withDataDir(t)
	in := newIncarnations(t)
	l, cfg := onOldIncarnation(t, in)
	sitesServe(in.newAnswer, in.newAnswer)
	var out bytes.Buffer
	err := runDeployment([]string{"pull", "--config", cfg, "--execute"}, noTerminalStdin(), &out)
	text := problemText(err)
	for _, want := range []string{"serve no version of the pinned root " + in.o1, "another operator", "deployment recover", "--root <sha>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("refusal %q does not say %q", text, want)
		}
	}
	if strings.Contains(text, in.n1) || strings.Contains(text, in.n2) || strings.Contains(text, "--key") {
		t.Fatalf("refusal %q names what the sites served, or --key", text)
	}
	err = runDeployment([]string{"pull", "--config", cfg, "--root", in.n1, "--execute"}, noTerminalStdin(), &out)
	if !strings.Contains(problemText(err), "--root differs from the pinned root") {
		t.Fatalf("got %q", problemText(err))
	}
	if h, _ := l.Head(); h != in.o3 {
		t.Fatalf("a refused pull moved the head to %s", h)
	}
	if rev, sha, _ := l.Seen(); rev != 3 || sha != in.o3 {
		t.Fatalf("seen %d %s, want 3 %s", rev, sha, in.o3)
	}
	recoverHosts(t, in.newAnswer)
	sitesServe(in.newAnswer, in.newAnswer)
	dir := t.TempDir()
	if err := runDeployment(recoverArgs(dir, "--root", in.n1, "--execute"), noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
	}
	fresh := heldCopy(t)
	if got, _ := fresh.Files(); !got.Equal(in.fn2) {
		t.Fatal("recover did not write the current line's head")
	}
}

// A site serving only a root of its own, the only one answering, refuses a
// pinned pull without naming that root, and --root with it is refused
// against the pin.
func TestConfigPullRefusalNamesNoServedRoot(t *testing.T) {
	key, pub := testKey(t)
	mkey, mpub := testKey(t)
	cur, evil := newStore(t), newStore(t)
	n1, f1 := cur.commit(t, fixtureAs(t, pub), "# new\n", nil, 1, key)
	m1, _ := evil.commit(t, fixtureAs(t, mpub), "# evil\n", nil, 1, mkey)
	evil.commit(t, fixtureAs(t, mpub), "# evil 2\n", []string{m1}, 2, mkey)
	l := pullWorkstation(t, cur.answer(t), n1, n1, f1)
	if err := l.SetSigningKey(pub); err != nil {
		t.Fatal(err)
	}
	cfg := l.cfg
	var out bytes.Buffer
	if err := runDeployment([]string{"pull", "--config", cfg, "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
	}
	sitesServe(evil.answer(t), evil.answer(t))
	err := runDeployment([]string{"pull", "--config", cfg, "--execute"}, noTerminalStdin(), &out)
	if !strings.Contains(problemText(err), "serve no version of the pinned root") || strings.Contains(problemText(err), m1) {
		t.Fatalf("got %q", problemText(err))
	}
	err = runDeployment([]string{"pull", "--config", cfg, "--root", m1, "--execute"}, noTerminalStdin(), &out)
	if !strings.Contains(problemText(err), "--root differs from the pinned root") {
		t.Fatalf("got %q", problemText(err))
	}
	if r, _ := l.Root(); r != n1 {
		t.Fatalf("root %s, want %s", r, n1)
	}
}

// A site withholding the newest version while serving the pinned line and an
// older incarnation does not lower the seen record: the same --root as the
// pin judges as a plain pull does.
func TestConfigPullNeverLowersSeenSilently(t *testing.T) {
	key, pub := testKey(t)
	yaml := fixtureAs(t, pub)
	full, part, old := newStore(t), newStore(t), newStore(t)
	n1, _ := full.commit(t, yaml, "# new\n", nil, 1, key)
	n2, _ := full.commit(t, yaml, "# new 2\n", []string{n1}, 2, key)
	n3, f3 := full.commit(t, yaml, "# new 3\n", []string{n2}, 3, key)
	part.commit(t, yaml, "# new\n", nil, 1, key)
	part.commit(t, yaml, "# new 2\n", []string{n1}, 2, key)
	o1, _ := old.commit(t, yaml, "# old\n", nil, 1, key)
	old.commit(t, yaml, "# old 2\n", []string{o1}, 2, key)
	l := pullWorkstation(t, full.answer(t), n1, n3, f3)
	if err := l.SetSigningKey(pub); err != nil {
		t.Fatal(err)
	}
	cfg := l.cfg
	var out bytes.Buffer
	if err := runDeployment([]string{"pull", "--config", cfg, "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
	}
	sitesServe(old.answer(t), part.answer(t))
	for _, args := range [][]string{{"pull", "--config", cfg, "--execute"}, {"pull", "--config", cfg, "--root", n1, "--execute"}, {"pull", "--config", cfg, "--root", o1, "--execute"}} {
		_ = runDeployment(args, noTerminalStdin(), &out)
		if h, _ := l.Head(); h != n3 {
			t.Fatalf("%v: head %s, want %s", args, h, n3)
		}
		if rev, sha, _ := l.Seen(); rev != 3 || sha != n3 {
			t.Fatalf("%v: seen %d %s, want 3 %s", args, rev, sha, n3)
		}
		if r, _ := l.Root(); r != n1 {
			t.Fatalf("%v: root %s, want %s", args, r, n1)
		}
	}
}

// A --root that is not held, mistyped or an old one, is named as such, not
// answered with what the sites serve.
func TestConfigRecoverUnheldRootIsNamed(t *testing.T) {
	withDataDir(t)
	_, answer, _ := storedVersion(t)
	recoverHosts(t, answer)
	var out bytes.Buffer
	root := strings.Repeat("ab", 20)
	err := runDeployment(recoverArgs(t.TempDir(), "--root", root, "--execute"), noTerminalStdin(), &out)
	if !strings.Contains(problemText(err), "the pinned root "+root+" is not held") || strings.Contains(problemText(err), "serve none") {
		t.Fatalf("got %q", problemText(err))
	}
}
