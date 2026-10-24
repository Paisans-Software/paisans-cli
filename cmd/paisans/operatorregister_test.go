package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/localstore"
)

// registerWorkstation is a workstation whose system copy and draft hold
// files, nothing recorded, the fixture's sites all answering with answer and
// logging to runs, and the agent holding held.
func registerWorkstation(t *testing.T, answer string, f configstore.Files, runs *[]string, held ...string) workstation {
	t.Helper()
	recoverHosts(t, answer)
	allSitesUp(answer, runs)
	saved := agentKeys
	agentKeys = func() []string { return held }
	t.Cleanup(func() { agentKeys = saved })
	return newWorkstation(t, f)
}

// bobKey is the key the fixture's second operator, bob, logs in with.
const bobKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB bob@example.org"

// withOperatorBob is the fixture's paisans-stack.yaml with alice's key
// replaced by alice and its second operator bob's by bob.
func withOperatorBob(t *testing.T, alice, bob string) string {
	t.Helper()
	return strings.Replace(fixtureAs(t, alice), bobKey, bob, 1)
}

func pushed(runs []string) bool {
	for _, r := range runs {
		if strings.Contains(r, "bundle verify") {
			return true
		}
	}
	return false
}

// With no agent key any operator lists, register refuses, prints the agent's
// keys, and says where to add one.
func TestOperatorRegisterWithoutAnAdminKeyPrintsTheAgentsKeys(t *testing.T) {
	shas, answer, files := storedHistory(t, 1, 0)
	_, other := testKey(t)
	l := registerWorkstation(t, answer, files[0], nil, other)
	var out bytes.Buffer
	err := runOperator([]string{"register", "--root", shas[0], "--config", l.config()}, noTerminalStdin(), &out)
	if err == nil || !strings.Contains(problemText(err), "operators") || !strings.Contains(problemText(err), "another operator") {
		t.Fatalf("got %v", problemText(err))
	}
	if !strings.Contains(out.String(), other) {
		t.Fatalf("the agent's key was not printed:\n%s", out.String())
	}
}

// Keys of two operators in the agent refuse, naming both and asking for --key.
func TestOperatorRegisterWithTwoAdminsKeysAsksForKey(t *testing.T) {
	_, alice := testKey(t)
	_, bob := testKey(t)
	yaml := withOperatorBob(t, alice, bob)
	f := snapshotted(configstore.Files{Paisans: []byte(yaml), Secrets: []byte("secrets\n"), SOPS: []byte("creation_rules: []\n")})
	var runs []string
	l := registerWorkstation(t, "", f, &runs, alice, bob)
	var out bytes.Buffer
	err := runOperator([]string{"register", "--config", l.config()}, noTerminalStdin(), &out)
	text := problemText(err)
	if err == nil || !strings.Contains(text, "alice") || !strings.Contains(text, "bob") || !strings.Contains(text, "--key") {
		t.Fatalf("got %v", text)
	}
	if len(runs) != 0 {
		t.Fatalf("sites reached before a key was chosen: %q", runs)
	}
}

// Local files that differ from the head refuse, with the difference shown
// and nothing recorded.
func TestOperatorRegisterRefusesFilesThatDifferFromTheHead(t *testing.T) {
	shas, answer, files, _, pub := storedHistoryKey(t, 2, 0)
	var runs []string
	l := registerWorkstation(t, answer, files[0], &runs, pub)
	var out bytes.Buffer
	err := runOperator([]string{"register", "--root", shas[0], "--config", l.config(), "--execute"}, noTerminalStdin(), &out)
	if err == nil || !strings.Contains(problemText(err), "differ") {
		t.Fatalf("got %v", problemText(err))
	}
	if !strings.Contains(out.String(), "+# edited") {
		t.Fatalf("no difference shown:\n%s", out.String())
	}
	if k, _ := l.sys.SigningKey(); k != "" || pushed(runs) {
		t.Fatalf("key %q recorded, pushed %v", k, pushed(runs))
	}
}

// A dry run shows what it would sign and writes no state and nothing to any
// site; --execute signs the attestation on the head, pushes it, and records
// the key, the head and the root.
func TestOperatorRegisterDryRunThenExecute(t *testing.T) {
	akey, apub := testKey(t)
	bkey, bpub := testKey(t)
	s := newStore(t)
	yaml := withOperatorBob(t, apub, bpub)
	v1, f1 := s.commit(t, yaml, "", nil, 1, akey)
	v2, f2 := s.commit(t, yaml, "# edited\n", []string{v1}, 2, akey)
	shas, files := []string{v1, v2}, []configstore.Files{f1, f2}
	var runs []string
	l := registerWorkstation(t, s.answer(t), files[1], &runs, bpub)
	savedSign := configSignWith
	configSignWith = func(string) string { return bkey }
	t.Cleanup(func() { configSignWith = savedSign })
	cfgPath := l.config()
	var out bytes.Buffer
	if err := runOperator([]string{"register", "--root", shas[0], "--config", cfgPath}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err))
	}
	for _, name := range []string{"signing-key", "config-head", "config-root", "config-seen"} {
		if _, err := os.Stat(filepath.Join(l.sys.Dir, ".paisans", name)); !os.IsNotExist(err) {
			t.Errorf("a dry run wrote %s", name)
		}
	}
	if synced(runs) || pushed(runs) {
		t.Fatalf("a dry run changed a site: %q", runs)
	}
	if err := runOperator([]string{"register", "--root", shas[0], "--config", cfgPath, "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err))
	}
	head, _ := l.sys.Head()
	rec, _ := l.sys.SigningKey()
	root, _ := l.sys.Root()
	if head == "" || head == shas[1] || rec != bpub || root != shas[0] {
		t.Fatalf("head %q key %q root %q", head, rec, root)
	}
	cache, _ := l.sys.Cache()
	vs, err := cache.Versions(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(vs, func(v configstore.Version) bool { return v.SHA == head })
	if i < 0 || !slices.Equal(vs[i].Parents, []string{shas[1]}) || vs[i].Revision != 3 {
		t.Fatalf("attestation %+v", vs)
	}
	if got, _ := cache.Files(head); !got.Equal(files[1]) {
		t.Fatal("the attestation's tree is not the head's")
	}
	if !synced(runs) || !pushed(runs) {
		t.Fatalf("--execute did not sync and push: %q", runs)
	}
	noFolderState(t, l.dir)
}

// With one site of four reached directly, register --execute signs the
// attestation and records the key, at no terminal and with nothing asked.
func TestOperatorRegisterFromOneSiteAsksNothing(t *testing.T) {
	shas, answer, files, key, pub := storedHistoryKey(t, 1, 0)
	var runs []string
	l := registerWorkstation(t, answer, files[0], &runs, pub)
	recoverHosts(t, answer) // home-a alone answers: one of four sites
	savedSign := configSignWith
	configSignWith = func(string) string { return key }
	t.Cleanup(func() { configSignWith = savedSign })
	cfgPath := l.config()
	var out bytes.Buffer
	if err := runOperator([]string{"register", "--root", shas[0], "--config", cfgPath, "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err))
	}
	if k, _ := l.sys.SigningKey(); k != pub {
		t.Fatalf("key %q", k)
	}
	if strings.Contains(out.String(), "partial") {
		t.Fatalf("output names partial:\n%s", out.String())
	}
}

// register attests the system copy's files: a draft beside it that differs
// from the sites' newest version is neither compared nor touched, and
// nothing is written into its folder.
func TestRegisterAttestsFromSystemCopy(t *testing.T) {
	shas, answer, files, key, pub := storedHistoryKey(t, 2, 0)
	var runs []string
	l := registerWorkstation(t, answer, files[1], &runs, pub)
	edited := files[0]
	edited.Paisans = append(append([]byte{}, edited.Paisans...), "# a draft's edit\n"...)
	if err := configstore.WriteFilesTo(l.dir, edited); err != nil {
		t.Fatal(err)
	}
	before := tree(t, l.dir)
	savedSign := configSignWith
	configSignWith = func(string) string { return key }
	t.Cleanup(func() { configSignWith = savedSign })
	var out bytes.Buffer
	if err := runOperator([]string{"register", "--root", shas[0], "--config", l.config(), "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err))
	}
	if k, _ := l.sys.SigningKey(); k != pub {
		t.Fatalf("key %q", k)
	}
	if r, _ := l.sys.Root(); r != shas[0] {
		t.Fatalf("root %q", r)
	}
	if !pushed(runs) {
		t.Fatal("nothing pushed")
	}
	if after := tree(t, l.dir); len(after) != len(before) {
		t.Fatalf("the folder changed: %v", after)
	}
	for p, v := range before {
		if tree(t, l.dir)[p] != v {
			t.Fatalf("%s changed", p)
		}
	}
	noFolderState(t, l.dir)
}

// A draft of a deployment with no system copy here is refused, naming the
// recovery, unless --root pins the root: a pinned root anchors alone, so
// register --root makes the system copy, from the sites' version the pin
// leads to. A dry run leaves none, and the draft is never touched.
func TestRegisterRootCreatesTheSystemCopy(t *testing.T) {
	shas, answer, files, key, pub := storedHistoryKey(t, 1, 0)
	var runs []string
	recoverHosts(t, answer)
	allSitesUp(answer, &runs)
	agentHolding(t, pub)
	withDataDir(t)
	dir := t.TempDir()
	if err := configstore.WriteFilesTo(dir, files[0]); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "paisans-stack.yaml")
	before := tree(t, dir)
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	held := func() bool {
		_, ok, err := st.Held(fixtureDeploymentID)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	var out bytes.Buffer
	err = runOperator([]string{"register", "--config", cfgPath}, noTerminalStdin(), &out)
	if text := problemText(err); err == nil || !strings.Contains(text, "deployment recover") {
		t.Fatalf("without --root: %v\n%s", err, text)
	}
	if err := runOperator([]string{"register", "--root", shas[0], "--config", cfgPath}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err))
	}
	if held() {
		t.Fatal("a dry run left a system copy")
	}
	if entries, _ := os.ReadDir(filepath.Join(dataDir(t), "deployments")); len(entries) != 0 {
		t.Fatalf("a dry run left %v", entries)
	}
	savedSign := configSignWith
	configSignWith = func(string) string { return key }
	t.Cleanup(func() { configSignWith = savedSign })
	if err := runOperator([]string{"register", "--root", shas[0], "--config", cfgPath, "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err))
	}
	sys, ok, err := st.Held(fixtureDeploymentID)
	if err != nil || !ok {
		t.Fatalf("no system copy: %v", err)
	}
	if r, _ := sys.Root(); r != shas[0] {
		t.Fatalf("root %q", r)
	}
	if k, _ := sys.SigningKey(); k != pub {
		t.Fatalf("key %q", k)
	}
	if h, _ := sys.Head(); h == "" {
		t.Fatal("no head")
	}
	if got, _ := sys.Files(); !got.Equal(files[0]) {
		t.Fatal("the system copy does not hold the attested files")
	}
	if !pushed(runs) {
		t.Fatal("nothing pushed")
	}
	after := tree(t, dir)
	if len(after) != len(before) {
		t.Fatalf("the folder changed: %v", after)
	}
	for p, v := range before {
		if after[p] != v {
			t.Fatalf("%s changed", p)
		}
	}
}

// register --execute writes the system copy, so it holds the lock: while
// another command holds it, register is refused and nothing is recorded.
func TestRegisterRefusesALockedSystemCopy(t *testing.T) {
	shas, answer, files, _, pub := storedHistoryKey(t, 1, 0)
	var runs []string
	l := registerWorkstation(t, answer, files[0], &runs, pub)
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := st.Lock(fixtureDeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	err = runOperator([]string{"register", "--root", shas[0], "--config", l.config(), "--execute"}, noTerminalStdin(), &bytes.Buffer{})
	var locked *localstore.LockedError
	if !errors.As(err, &locked) {
		t.Fatalf("got %v", err)
	}
	if k, _ := l.sys.SigningKey(); k != "" || pushed(runs) {
		t.Fatalf("key %q recorded, pushed %v", k, pushed(runs))
	}
}
