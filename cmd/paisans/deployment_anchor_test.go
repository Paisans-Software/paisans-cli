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

// recoverArgs is a recovery from the fixture's host into dir, with more.
func recoverArgs(dir string, more ...string) []string {
	return append([]string{"recover", "--from", "ubuntu@203.0.113.9", "--config", filepath.Join(dir, "paisans-stack.yaml")}, more...)
}

// On an empty workstation, with no root pinned, a version signed by a key
// the ssh-agent holds anchors, and --execute records that key as the
// signing key, beside the files and the history's root.
func TestConfigRecoverAnchorsOnTheAgentsKey(t *testing.T) {
	shas, answer, files, _, pub := storedHistoryKey(t, 2, 1)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	_, unrelated := testKey(t)
	agentHolding(t, unrelated, pub)
	var rep bytes.Buffer
	reporterOverride = ui.NewPlain(&rep, false)
	dir := t.TempDir()
	var out bytes.Buffer
	if err := runDeployment(recoverArgs(dir), noTerminalStdin(), &out); err != nil {
		t.Fatalf("dry run: %v", problemText(err))
	}
	if !strings.Contains(rep.String(), "anchored on "+fingerprint(t, pub)) {
		t.Fatalf("the gather does not name the key that anchored it:\n%s", rep.String())
	}
	l := configstore.Local{Dir: dir}
	if l.HasFiles() {
		t.Fatal("a dry run wrote the files")
	}
	if k, _ := l.SigningKey(); k != "" {
		t.Fatalf("a dry run recorded the signing key %q", k)
	}
	if err := runDeployment(recoverArgs(dir, "--execute"), noTerminalStdin(), &out); err != nil {
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

// An agent holding no key that signed a version is no anchor, and the
// refusal says how to give one on a workstation that has no files.
func TestConfigRecoverWithUnrelatedAgentKeysRefuses(t *testing.T) {
	_, answer, _ := storedVersion(t)
	recoverHosts(t, answer)
	_, unrelated := testKey(t)
	agentHolding(t, unrelated)
	dir := t.TempDir()
	var out bytes.Buffer
	err := runDeployment(recoverArgs(dir, "--execute"), noTerminalStdin(), &out)
	text := problemText(err)
	for _, want := range []string{"no version to start trusting from", "ssh-add", "--key", "--root <sha>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("refusal %q does not say %q", text, want)
		}
	}
	if strings.Contains(text, "operator register") {
		t.Fatalf("refusal %q suggests operator register, which needs the files", text)
	}
	if (configstore.Local{Dir: dir}).HasFiles() {
		t.Fatal("a refused recovery wrote the files")
	}
}

// --key names a public key to anchor on alone. One the agent does not hold,
// as a colleague's, anchors and is never recorded; one it holds is.
func TestConfigRecoverAnchorsOnKeyFile(t *testing.T) {
	shas, answer, _, _, pub := storedHistoryKey(t, 2, 0)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	path := filepath.Join(t.TempDir(), "id.pub")
	if err := os.WriteFile(path, []byte(pub+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	agentHolding(t, mine, pub)
	dir = t.TempDir()
	if err := runDeployment(recoverArgs(dir, "--key", path, "--execute"), noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
	}
	if k, _ := (configstore.Local{Dir: dir}).SigningKey(); k != pub {
		t.Fatalf("signing key %q, want %q", k, pub)
	}

	// --key that differs from a recorded signing key is refused.
	minePath := filepath.Join(t.TempDir(), "mine.pub")
	if err := os.WriteFile(minePath, []byte(mine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runDeployment([]string{"log", "--config", filepath.Join(dir, "paisans-stack.yaml"), "--key", minePath}, noTerminalStdin(), &out)
	if !strings.Contains(problemText(err), "--key differs from the recorded signing key") {
		t.Fatalf("got %v", problemText(err))
	}

	// A private key, or a file that is no key, is refused.
	if err := os.WriteFile(path, []byte("not a key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = runDeployment(recoverArgs(t.TempDir(), "--key", path), noTerminalStdin(), &out)
	if !strings.Contains(problemText(err), "--key must name a public key file") {
		t.Fatalf("got %v", err)
	}
}

// A pinned root still applies: an agent key whose newest version does not
// descend from it is refused, not trusted beside it.
func TestConfigRecoverAgentKeyMustDescendFromTheRoot(t *testing.T) {
	key, pub := testKey(t)
	okey, opub := testKey(t)
	s := newStore(t)
	other, _ := s.commit(t, fixtureAs(t, opub), "# other\n", nil, 1, okey)
	root, _ := s.commit(t, fixtureAs(t, pub), "", nil, 1, key)
	s.commit(t, fixtureAs(t, pub), "# edited\n", []string{root}, 2, key)
	answer := s.answer(t)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	agentHolding(t, pub)
	dir := t.TempDir()
	var out bytes.Buffer
	err := runDeployment(recoverArgs(dir, "--root", other, "--execute"), noTerminalStdin(), &out)
	if !strings.Contains(problemText(err), "does not descend from the pinned root "+other) {
		t.Fatalf("got %v", problemText(err))
	}
	l := configstore.Local{Dir: dir}
	if l.HasFiles() {
		t.Fatal("a refused recovery wrote the files")
	}
	if k, _ := l.SigningKey(); k != "" {
		t.Fatalf("a refused recovery recorded the signing key %q", k)
	}
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

// Two keys the agent holds whose histories diverge, here from two roots, are a
// fork: neither is chosen, each head's files go to incoming/<sha>, and no key
// is recorded. Either key alone recovers its own history.
func TestConfigRecoverAgentKeysThatDivergeAreAFork(t *testing.T) {
	akey, apub := testKey(t)
	bkey, bpub := testKey(t)
	_, cpub := testKey(t)
	s := newStore(t)
	ra, _ := s.commit(t, operatorsAs(t, apub, cpub), "", nil, 1, akey)
	a, fa := s.commit(t, operatorsAs(t, apub, cpub), "# a\n", []string{ra}, 2, akey)
	rb, _ := s.commit(t, operatorsAs(t, bpub, cpub), "# b\n", nil, 1, bkey)
	b, fb := s.commit(t, operatorsAs(t, bpub, cpub), "# b2\n", []string{rb}, 2, bkey)
	answer := s.answer(t)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)

	agentHolding(t, apub)
	one := t.TempDir()
	var out bytes.Buffer
	if err := runDeployment(recoverArgs(one, "--execute"), noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
	}
	if h, _ := (configstore.Local{Dir: one}).Head(); h != a {
		t.Fatalf("one key: head %s, want %s", h, a)
	}

	agentHolding(t, apub, bpub)
	dir := t.TempDir()
	err := runDeployment(recoverArgs(dir, "--execute"), noTerminalStdin(), &out)
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

// A key the agent holds that a version removed anchors nothing on or beside
// the removal and is never recorded; the files are the removal's line.
func TestConfigRecoverRemovedAgentKey(t *testing.T) {
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

	for _, keys := range [][]string{{oldPub}, {oldPub, newPub}} {
		agentHolding(t, keys...)
		dir := t.TempDir()
		var out bytes.Buffer
		if err := runDeployment(recoverArgs(dir, "--root", v1, "--execute"), noTerminalStdin(), &out); err != nil {
			t.Fatal(problemText(err), err)
		}
		l := configstore.Local{Dir: dir}
		if got, _ := l.Files(); !got.Equal(f2) {
			t.Fatalf("%d keys: recover wrote another version's files", len(keys))
		}
		want := ""
		if len(keys) == 2 {
			want = newPub
		}
		if k, _ := l.SigningKey(); k != want {
			t.Fatalf("%d keys: signing key %q, want %q", len(keys), k, want)
		}
	}
}

// pull, on a workstation with files and no anchor recorded, anchors on the
// agent's key too, and records it with --execute.
func TestConfigPullAnchorsOnTheAgentsKey(t *testing.T) {
	shas, answer, files, _, pub := storedHistoryKey(t, 2, 0)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	l := configstore.Local{Dir: t.TempDir()}
	if err := l.WriteFiles(files[0]); err != nil {
		t.Fatal(err)
	}
	if err := l.SetHead(shas[0]); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(l.Dir, "paisans-stack.yaml")
	var out bytes.Buffer
	err := runDeployment([]string{"pull", "--config", cfgPath, "--execute"}, noTerminalStdin(), &out)
	if !strings.Contains(problemText(err), "ssh-add") {
		t.Fatalf("no key held: %v", problemText(err))
	}
	agentHolding(t, pub)
	if err := runDeployment([]string{"pull", "--config", cfgPath, "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
	}
	if h, _ := l.Head(); h != shas[1] {
		t.Fatalf("head %s, want %s", h, shas[1])
	}
	if k, _ := l.SigningKey(); k != pub {
		t.Fatalf("signing key %q, want %q", k, pub)
	}
}

// log, on a workstation with files and no anchor recorded, judges from the
// agent's key as pull does, and records nothing; unrelated keys refuse with
// the same advice.
func TestConfigLogAnchorsOnTheAgentsKey(t *testing.T) {
	shas, answer, files, _, pub := storedHistoryKey(t, 2, 0)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	l := configstore.Local{Dir: t.TempDir()}
	if err := l.WriteFiles(files[0]); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(l.Dir, "paisans-stack.yaml")
	_, unrelated := testKey(t)
	agentHolding(t, unrelated)
	var out bytes.Buffer
	err := runDeployment([]string{"log", "--config", cfgPath}, noTerminalStdin(), &out)
	text := problemText(err)
	for _, want := range []string{"no version to start trusting from", "ssh-add", "--key", "--root <sha>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("refusal %q does not say %q", text, want)
		}
	}
	agentHolding(t, unrelated, pub)
	var rep bytes.Buffer
	reporterOverride = ui.NewPlain(&rep, false)
	out.Reset()
	if err := runDeployment([]string{"log", "--config", cfgPath}, noTerminalStdin(), &out); err != nil {
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

	// --key names the key in place of the agent's.
	path := filepath.Join(t.TempDir(), "id.pub")
	if err := os.WriteFile(path, []byte(pub+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agentHolding(t)
	if err := runDeployment([]string{"log", "--config", cfgPath, "--key", path}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err), err)
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
		for _, want := range []string{"no version to start trusting from", "ssh-add", "paisans deployment pull --key <path.pub>", "--root <sha>", "paisans operator register"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%v: refusal %q does not say %q", args, text, want)
			}
		}
	}
}
