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

// --key names a public key the agent does not hold, to anchor on it alone,
// and --execute records it.
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
	if k, _ := l.SigningKey(); k != pub {
		t.Fatalf("signing key %q, want %q", k, pub)
	}

	// A private key, or a file that is no key, is refused.
	if err := os.WriteFile(path, []byte("not a key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runDeployment(recoverArgs(t.TempDir(), "--key", path), noTerminalStdin(), &out)
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

// Two keys the agent holds that signed divergent heads are a fork: neither
// is chosen, each head's files go to incoming/<sha>, and no key is recorded.
func TestConfigRecoverAgentKeysThatDivergeAreAFork(t *testing.T) {
	akey, apub := testKey(t)
	bkey, bpub := testKey(t)
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	yaml := strings.Replace(fixtureAs(t, apub), cfg.Operators["bob"].KeyLines()[0], bpub, 1)
	s := newStore(t)
	root, _ := s.commit(t, yaml, "", nil, 1, akey)
	a, fa := s.commit(t, yaml, "# a\n", []string{root}, 2, akey)
	b, _ := s.commit(t, yaml, "# b\n", []string{root}, 2, bkey)
	b3, fb := s.commit(t, yaml, "# b3\n", []string{b}, 3, bkey)
	answer := s.answer(t)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	agentHolding(t, apub, bpub)
	dir := t.TempDir()
	var out bytes.Buffer
	err = runDeployment(recoverArgs(dir, "--execute"), noTerminalStdin(), &out)
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
	for sha, want := range map[string]configstore.Files{a: fa, b3: fb} {
		got, err := (configstore.Local{Dir: l.IncomingFor(sha)}).Files()
		if err != nil || !got.Equal(want) {
			t.Fatalf("incoming %s: %v", sha[:12], err)
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
