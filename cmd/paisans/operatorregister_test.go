package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/configstore"
)

// registerWorkstation is a workstation holding files, nothing recorded, the
// fixture's sites all answering with answer and logging to runs, and the
// agent holding held.
func registerWorkstation(t *testing.T, answer string, f configstore.Files, runs *[]string, held ...string) configstore.Local {
	t.Helper()
	recoverHosts(t, answer)
	allSitesUp(answer, runs)
	saved := agentKeys
	agentKeys = func() []string { return held }
	t.Cleanup(func() { agentKeys = saved })
	l := configstore.Local{Dir: t.TempDir()}
	if err := l.WriteFiles(f); err != nil {
		t.Fatal(err)
	}
	return l
}

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
	err := runOperator([]string{"register", "--root", shas[0], "--config", filepath.Join(l.Dir, "paisans-stack.yaml")}, noTerminalStdin(), &out)
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
	err := runOperator([]string{"register", "--config", filepath.Join(l.Dir, "paisans-stack.yaml")}, noTerminalStdin(), &out)
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
	err := runOperator([]string{"register", "--root", shas[0], "--config", filepath.Join(l.Dir, "paisans-stack.yaml"), "--execute"}, noTerminalStdin(), &out)
	if err == nil || !strings.Contains(problemText(err), "differ") {
		t.Fatalf("got %v", problemText(err))
	}
	if !strings.Contains(out.String(), "+# edited") {
		t.Fatalf("no difference shown:\n%s", out.String())
	}
	if k, _ := l.SigningKey(); k != "" || pushed(runs) {
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
	cfgPath := filepath.Join(l.Dir, "paisans-stack.yaml")
	var out bytes.Buffer
	if err := runOperator([]string{"register", "--root", shas[0], "--config", cfgPath}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err))
	}
	for _, name := range []string{"signing-key", "config-head", "config-root", "config-seen"} {
		if _, err := os.Stat(filepath.Join(l.Dir, ".paisans", name)); !os.IsNotExist(err) {
			t.Errorf("a dry run wrote %s", name)
		}
	}
	if synced(runs) || pushed(runs) {
		t.Fatalf("a dry run changed a site: %q", runs)
	}
	if err := runOperator([]string{"register", "--root", shas[0], "--config", cfgPath, "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err))
	}
	head, _ := l.Head()
	rec, _ := l.SigningKey()
	root, _ := l.Root()
	if head == "" || head == shas[1] || rec != bpub || root != shas[0] {
		t.Fatalf("head %q key %q root %q", head, rec, root)
	}
	cache, _ := l.Cache()
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
}

// With fewer than a majority of sites reached directly, register --execute
// asks for partial even when it writes no files, and without a terminal
// refuses with nothing signed, pushed or recorded.
func TestOperatorRegisterWithoutAMajorityAsksPartial(t *testing.T) {
	shas, answer, files, key, pub := storedHistoryKey(t, 1, 0)
	var runs []string
	l := registerWorkstation(t, answer, files[0], &runs, pub)
	recoverHosts(t, answer) // home-a alone answers: one of four sites
	savedSign := configSignWith
	configSignWith = func(string) string { return key }
	t.Cleanup(func() { configSignWith = savedSign })
	cfgPath := filepath.Join(l.Dir, "paisans-stack.yaml")
	var out bytes.Buffer
	err := runOperator([]string{"register", "--root", shas[0], "--config", cfgPath, "--execute"}, noTerminalStdin(), &out)
	if err == nil || !strings.Contains(err.Error(), "partial") {
		t.Fatalf("got %v", problemText(err))
	}
	for _, name := range []string{"signing-key", "config-head", "config-root", "config-seen"} {
		if _, err := os.Stat(filepath.Join(l.Dir, ".paisans", name)); !os.IsNotExist(err) {
			t.Errorf("a refused register wrote %s", name)
		}
	}
	if err := runOperator([]string{"register", "--root", shas[0], "--config", cfgPath, "--execute"}, answering(t, "partial\n"), &out); err != nil {
		t.Fatal(problemText(err))
	}
	if k, _ := l.SigningKey(); k != pub {
		t.Fatalf("key %q after partial", k)
	}
}
