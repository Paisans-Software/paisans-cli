package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	l := configstore.Local{Dir: dir}
	if l.HasFiles() {
		t.Fatal("a dry run wrote the files")
	}
	if err := runDeployment(recoverArgs(dir, "--key", key, "--execute"), noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
	}
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
}

// --key with a public key the agent does not hold, as a colleague's,
// anchors and is never recorded; --key that differs from a recorded key is
// refused, and a file that is no public key is refused.
func TestConfigRecoverAnchorsOnKeyFile(t *testing.T) {
	shas, answer, _, _, pub := storedHistoryKey(t, 2, 0)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	path := pubFile(t, pub)
	dir := t.TempDir()
	var out bytes.Buffer
	if err := runDeployment(recoverArgs(dir, "--key", path, "--execute"), noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
	}
	l := configstore.Local{Dir: dir}
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
// root neither blocks nor overrides it, and is not recorded.
func TestConfigRecoverPinnedRootWins(t *testing.T) {
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
	dir := t.TempDir()
	var out bytes.Buffer
	if err := runDeployment(recoverArgs(dir, "--root", other, "--key", pubFile(t, pub), "--execute"), noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
	}
	l := configstore.Local{Dir: dir}
	if got, _ := l.Files(); !got.Equal(f) {
		t.Fatal("recover did not write the pinned root's files")
	}
	if k, _ := l.SigningKey(); k != "" {
		t.Fatalf("the named key was recorded under another root's pin: %q", k)
	}
}

// A named key that signed two histories, here from two roots, anchors in
// each, and their heads differ: a fork, each head's files go to
// incoming/<sha>, and no key is recorded.
func TestConfigRecoverNamedKeyUnderTwoRootsIsAFork(t *testing.T) {
	akey, apub := testKey(t)
	_, bpub := testKey(t)
	_, cpub := testKey(t)
	s := newStore(t)
	ra, _ := s.commit(t, operatorsAs(t, apub, cpub), "", nil, 1, akey)
	a, fa := s.commit(t, operatorsAs(t, apub, cpub), "# a\n", []string{ra}, 2, akey)
	rb, _ := s.commit(t, operatorsAs(t, apub, bpub), "# b\n", nil, 1, akey)
	b, fb := s.commit(t, operatorsAs(t, apub, bpub), "# b2\n", []string{rb}, 2, akey)
	answer := s.answer(t)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	agentHolding(t, apub)
	dir := t.TempDir()
	var out bytes.Buffer
	err := runDeployment(recoverArgs(dir, "--key", pubFile(t, apub), "--execute"), noTerminalStdin(), &out)
	if !strings.Contains(problemText(err), "the sites hold a fork") {
		t.Fatalf("got %v", problemText(err))
	}
	l := configstore.Local{Dir: dir}
	if l.HasFiles() {
		t.Fatal("a fork's recovery wrote the files")
	}
	if k, _ := l.SigningKey(); k != "" {
		t.Fatalf("a fork's recovery recorded the signing key %q", k)
	}
	for sha, want := range map[string]configstore.Files{a: fa, b: fb} {
		got, err := (configstore.Local{Dir: l.IncomingFor(sha)}).Files()
		if err != nil || !got.Equal(want) {
			t.Fatalf("incoming %s: %v", sha[:12], err)
		}
	}
}

// --key naming a key a version removed is refused, even though the agent
// holds it and the removed key signed a version on the removal; the current
// key recovers the removal's line and is recorded.
func TestConfigRecoverNamedRemovedKeyIsRefused(t *testing.T) {
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
	if k, _ := l.SigningKey(); k != "" {
		t.Fatalf("a refused recovery recorded the signing key %q", k)
	}

	dir = t.TempDir()
	if err := runDeployment(recoverArgs(dir, "--key", pubFile(t, newPub), "--execute"), noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
	}
	l = configstore.Local{Dir: dir}
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
	l := configstore.Local{Dir: t.TempDir()}
	if err := l.WriteFiles(files[0]); err != nil {
		t.Fatal(err)
	}
	if err := l.SetHead(shas[0]); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(l.Dir, "paisans-stack.yaml")
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
	l := configstore.Local{Dir: t.TempDir()}
	if err := l.WriteFiles(files[0]); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(l.Dir, "paisans-stack.yaml")
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
}

// publish, with no anchor, gives the advice pull gives, through pull, since
// publish judges only from the recorded key.
func TestConfigPublishWithoutAnchorAdvises(t *testing.T) {
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
		for _, want := range []string{"no version to start trusting from", "paisans deployment pull --key <path.pub>", "ssh-add", "paisans deployment pull --root <sha>", "paisans operator register"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%v: refusal %q does not say %q", args, text, want)
			}
		}
	}
}
