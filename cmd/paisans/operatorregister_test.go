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

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/draft"
	"github.com/paisans-software/paisans-cli/internal/localstore"
	"github.com/paisans-software/paisans-cli/internal/ui"
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
// leads to, whose operators choose the key: here the draft lists the key
// under no operator. A dry run leaves none, and the draft is never touched.
func TestRegisterRootCreatesTheSystemCopy(t *testing.T) {
	shas, answer, files, key, pub := storedHistoryKey(t, 1, 0)
	var runs []string
	recoverHosts(t, answer)
	allSitesUp(answer, &runs)
	agentHolding(t, pub)
	withDataDir(t)
	dir := t.TempDir()
	_, other := testKey(t)
	edited := files[0]
	edited.Paisans = []byte(strings.Replace(string(edited.Paisans), pub, other, 1))
	if err := configstore.WriteFilesTo(dir, edited); err != nil {
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

// Making a system copy holds the lock from before it looks for one: while
// another writer holds it, building the copy, register --root is refused,
// dry run or not, and the other's directory is left as it is.
func TestRegisterRootRefusesWhileAnotherBuildsTheCopy(t *testing.T) {
	shas, answer, files, _, pub := storedHistoryKey(t, 1, 0)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	agentHolding(t, pub)
	withDataDir(t)
	dir := t.TempDir()
	if err := configstore.WriteFilesTo(dir, files[0]); err != nil {
		t.Fatal(err)
	}
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := st.Lock(fixtureDeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	theirs, err := st.Local(fixtureDeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(theirs.Dir, "in-progress"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := tree(t, theirs.Dir)
	for _, extra := range [][]string{nil, {"--execute"}} {
		args := append([]string{"register", "--root", shas[0], "--config", filepath.Join(dir, "paisans-stack.yaml")}, extra...)
		err := runOperator(args, noTerminalStdin(), &bytes.Buffer{})
		var locked *localstore.LockedError
		if !errors.As(err, &locked) {
			t.Fatalf("%v: got %v", extra, err)
		}
		if after := tree(t, theirs.Dir); len(after) != len(before) {
			t.Fatalf("%v: the other writer's directory changed: %v", extra, after)
		}
	}
}

// A deployment directory that holds only a record is no system copy: a dry
// run of register --root leaves it as it was, and makes no copy of it;
// --execute makes the copy there, keeping the record.
func TestRegisterRootKeepsADirectoryWithARecord(t *testing.T) {
	shas, answer, files, key, pub := storedHistoryKey(t, 1, 0)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	agentHolding(t, pub)
	withDataDir(t)
	dir := t.TempDir()
	if err := configstore.WriteFilesTo(dir, files[0]); err != nil {
		t.Fatal(err)
	}
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Record(localstore.Saved{ID: fixtureDeploymentID, Domain: "example.org", SHA: shas[0], Revision: 1, Status: localstore.Verified, Source: "test"}); err != nil {
		t.Fatal(err)
	}
	copyDir := filepath.Join(dataDir(t), "deployments", fixtureDeploymentID)
	before := tree(t, copyDir)
	cfgPath := filepath.Join(dir, "paisans-stack.yaml")
	if err := runOperator([]string{"register", "--root", shas[0], "--config", cfgPath}, noTerminalStdin(), &bytes.Buffer{}); err != nil {
		t.Fatal(problemText(err))
	}
	after := tree(t, copyDir)
	if len(after) != len(before) {
		t.Fatalf("a dry run changed %s: %v", copyDir, after)
	}
	for p, v := range before {
		if after[p] != v {
			t.Fatalf("a dry run changed %s", p)
		}
	}
	if _, ok, _ := st.Held(fixtureDeploymentID); ok {
		t.Fatal("a dry run made a system copy")
	}
	if entries, _ := os.ReadDir(dataDir(t)); len(entries) != 2 {
		t.Errorf("the data directory holds %v", entries)
	}
	savedSign := configSignWith
	configSignWith = func(string) string { return key }
	t.Cleanup(func() { configSignWith = savedSign })
	if err := runOperator([]string{"register", "--root", shas[0], "--config", cfgPath, "--execute"}, noTerminalStdin(), &bytes.Buffer{}); err != nil {
		t.Fatal(problemText(err))
	}
	sys, ok, err := st.Held(fixtureDeploymentID)
	if err != nil || !ok {
		t.Fatalf("no system copy: %v", err)
	}
	if k, _ := sys.SigningKey(); k != pub {
		t.Fatalf("key %q", k)
	}
	if saved, ok, err := st.Load(fixtureDeploymentID); err != nil || !ok || saved.ID != fixtureDeploymentID {
		t.Fatalf("the record is gone: %+v, %v", saved, err)
	}
}

// settle puts the made copy in place of whatever the directory held, every
// entry of it but those kept moved aside, outside deployments/, and none
// removed.
func TestSettleKeepsWhatWasThere(t *testing.T) {
	base := t.TempDir()
	staging, dest := t.TempDir(), filepath.Join(base, "deployments", fixtureDeploymentID)
	for path, body := range map[string]string{
		filepath.Join(staging, "paisans-stack.yaml"):      "ours\n",
		filepath.Join(staging, ".paisans", "config-head"): "head\n",
		filepath.Join(dest, "saved.json"):                 "{}\n",
		filepath.Join(dest, "paisans-stack.yaml"):         "theirs\n",
		filepath.Join(dest, ".paisans", "config-root"):    "theirs\n",
	} {
		os.MkdirAll(filepath.Dir(path), 0o700)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	aside, moved, err := settle(staging, dest)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(aside) != filepath.Join(base, "replaced") || !strings.HasPrefix(filepath.Base(aside), fixtureDeploymentID+"-") {
		t.Fatalf("aside %q", aside)
	}
	if strings.Join(moved, ",") != ".paisans,paisans-stack.yaml,saved.json" {
		t.Errorf("moved %q", moved)
	}
	for path, want := range map[string]string{
		filepath.Join(dest, "paisans-stack.yaml"):       "ours\n",
		filepath.Join(dest, ".paisans", "config-head"):  "head\n",
		filepath.Join(aside, "saved.json"):              "{}\n",
		filepath.Join(aside, "paisans-stack.yaml"):      "theirs\n",
		filepath.Join(aside, ".paisans", "config-root"): "theirs\n",
	} {
		if b, err := os.ReadFile(path); err != nil || string(b) != want {
			t.Errorf("%s: %q, %v", path, b, err)
		}
	}
	for _, gone := range []string{filepath.Join(dest, "saved.json"), filepath.Join(dest, ".paisans", "config-root")} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s is still in place: %v", gone, err)
		}
	}

	// An entry keep names stays in place, and the made copy's entries go in
	// beside it.
	staging, dest = t.TempDir(), filepath.Join(t.TempDir(), "deployments", fixtureDeploymentID)
	for path, body := range map[string]string{
		filepath.Join(staging, "paisans-stack.yaml"):      "ours\n",
		filepath.Join(staging, ".paisans", "config-head"): "head\n",
		filepath.Join(dest, "saved.json"):                 "{}\n",
		filepath.Join(dest, ".paisans", "config-root"):    "theirs\n",
	} {
		os.MkdirAll(filepath.Dir(path), 0o700)
		os.WriteFile(path, []byte(body), 0o600)
	}
	aside, moved, err = settle(staging, dest, "saved.json")
	if err != nil || strings.Join(moved, ",") != ".paisans" {
		t.Fatalf("moved %q, %v", moved, err)
	}
	for path, want := range map[string]string{
		filepath.Join(dest, "saved.json"):               "{}\n",
		filepath.Join(dest, "paisans-stack.yaml"):       "ours\n",
		filepath.Join(dest, ".paisans", "config-head"):  "head\n",
		filepath.Join(aside, ".paisans", "config-root"): "theirs\n",
	} {
		if b, err := os.ReadFile(path); err != nil || string(b) != want {
			t.Errorf("%s: %q, %v", path, b, err)
		}
	}

	// Where dest is not there, or empty, staging itself becomes it.
	for _, empty := range []bool{false, true} {
		staging, dest = t.TempDir(), filepath.Join(t.TempDir(), "deployments", fixtureDeploymentID)
		if empty {
			os.MkdirAll(dest, 0o700)
		}
		os.WriteFile(filepath.Join(staging, "paisans-stack.yaml"), []byte("ours\n"), 0o600)
		if aside, moved, err := settle(staging, dest); err != nil || aside != "" || len(moved) != 0 {
			t.Fatalf("empty %v: aside %q %q, %v", empty, aside, moved, err)
		}
		if b, _ := os.ReadFile(filepath.Join(dest, "paisans-stack.yaml")); string(b) != "ours\n" {
			t.Fatalf("got %q", b)
		}
	}
}

// A refusal raised while register works in its own directory names the
// system copy's place instead, since the other is gone when the run ends.
func TestRenamedNamesTheSystemCopy(t *testing.T) {
	p := renamed(&ui.Problem{Hint: "in /tmp/register-x", Explain: "see /tmp/register-x/.paisans"}, "/tmp/register-x", "/data/deployments/x")
	text := problemText(p)
	if strings.Contains(text, "register-x") || !strings.Contains(text, "/data/deployments/x/.paisans") {
		t.Fatalf("got %s", text)
	}
	plain := renamed(errors.New("open /tmp/register-x/secrets.enc.yaml"), "/tmp/register-x", "/data/deployments/x")
	if plain.Error() != "open /data/deployments/x/secrets.enc.yaml" {
		t.Fatalf("got %v", plain)
	}
	if renamed(nil, "a", "b") != nil {
		t.Fatal("nil became an error")
	}
	// A Problem wrapped in another error is the one shown, so it is
	// renamed, its Cause too; a longer name that starts with the directory
	// is another path, and is left.
	cause := errors.New("read /tmp/register-x/.paisans/config-head")
	wrapped := renamed(fmt.Errorf("register: %w", &ui.Problem{Hint: "in /tmp/register-x", Explain: "not /tmp/register-xy, but /tmp/register-x.", Cause: cause}), "/tmp/register-x", "/data/deployments/x")
	var q *ui.Problem
	if !errors.As(wrapped, &q) || q.Hint != "in /data/deployments/x" || q.Explain != "not /tmp/register-xy, but /data/deployments/x." {
		t.Fatalf("got %+v", q)
	}
	if q.Cause.Error() != "read /data/deployments/x/.paisans/config-head" || !errors.Is(q.Cause, cause) {
		t.Fatalf("cause %v", q.Cause)
	}
}

// What a run prints while it works in its own directory names the system
// copy's place: the reporter stageSystem returns renames it.
func TestStagedOutputNamesTheSystemCopy(t *testing.T) {
	var rec ui.Recorder
	r := renamingReporter{&rec, "/tmp/recover-x", "/data/deployments/x"}
	r.Warn("in /tmp/recover-x", "see /tmp/recover-x/.paisans/arrivals")
	r.Step("write /tmp/recover-x/paisans-stack.yaml").Done("/tmp/recover-x")
	r.Result("kept /tmp/recover-xy")
	text := rec.Lines()
	if strings.Contains(text, "/tmp/recover-x/") || strings.Contains(text, "/tmp/recover-x\n") || !strings.Contains(text, "/data/deployments/x/.paisans/arrivals") || !strings.Contains(text, "kept /tmp/recover-xy") {
		t.Fatalf("got\n%s", text)
	}
}

// A run that recorded its head but whose system copy cannot be put in
// place says so once, as its error, naming what failed, where the head is,
// the run's own error, and that what the place held may be partly moved
// aside.
func TestStageSettleFailureIsSaidOnce(t *testing.T) {
	st := localstore.Store{Base: t.TempDir()}
	dest := filepath.Join(st.Base, "deployments", fixtureDeploymentID)
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "left"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var rec ui.Recorder
	released := false
	sys, _, done, err := stageSystem(&rec, st, "recover", fixtureDeploymentID, "it held what a run left.", nil, func() { released = true }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sys.SetHead(strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	// replaced/ is a file, so nothing can be moved aside.
	if err := os.WriteFile(filepath.Join(st.Base, "replaced"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = done(errors.New("push refused"))
	text := problemText(err)
	for _, w := range []string{"could not be put in place", sys.Dir, "push refused", "they may not have it"} {
		if !strings.Contains(text, w) {
			t.Errorf("the error lacks %q:\n%s", w, text)
		}
	}
	if strings.Contains(rec.Lines(), "could not be put in place") {
		t.Errorf("the failure was also printed:\n%s", rec.Lines())
	}
	if !released {
		t.Error("the lock was not released")
	}
	if _, err := os.Stat(filepath.Join(sys.Dir, ".paisans", "config-head")); err != nil {
		t.Errorf("the directory holding the head is gone: %v", err)
	}

	// Once entries are moved aside and the rest fails, the error says what
	// may remain where.
	if err := os.Remove(filepath.Join(st.Base, "replaced")); err != nil {
		t.Fatal(err)
	}
	sys, _, done, err = stageSystem(&rec, st, "recover", fixtureDeploymentID, "it held what a run left.", nil, func() {}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sys.SetHead(strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dest, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dest, 0o700) })
	text = problemText(done(nil))
	if !strings.Contains(text, "may remain in "+dest) {
		t.Errorf("the error does not say what may remain:\n%s", text)
	}
}

// aliceAndBob is a history of two versions signed by alice, whose
// paisans-stack.yaml lists bob as an operator, the sites answering with it
// and the agent holding bob's key, which signs: a system copy and draft of
// the first version, nothing recorded.
func aliceAndBob(t *testing.T, runs *[]string) (s store, akey, yaml string, shas []string, l workstation) {
	t.Helper()
	akey, apub := testKey(t)
	bkey, bpub := testKey(t)
	s = newStore(t)
	yaml = withOperatorBob(t, apub, bpub)
	v1, f1 := s.commit(t, yaml, "", nil, 1, akey)
	l = registerWorkstation(t, s.answer(t), f1, runs, bpub)
	savedSign := configSignWith
	configSignWith = func(string) string { return bkey }
	t.Cleanup(func() { configSignWith = savedSign })
	return s, akey, yaml, []string{v1}, l
}

// An attestation moves the system copy's head, and records it over what
// the record said before: checkout then starts a draft of the
// attestation, and deployment list shows it.
func TestRegisterThenCheckout(t *testing.T) {
	_, _, _, shas, l := aliceAndBob(t, nil)
	st := must(openStore())
	if err := st.Record(localstore.Saved{ID: fixtureDeploymentID, Status: localstore.Unverified, Source: "fetched"}); err != nil {
		t.Fatal(err)
	}
	if err := runOperator([]string{"register", "--root", shas[0], "--config", l.config(), "--execute"}, noTerminalStdin(), &bytes.Buffer{}); err != nil {
		t.Fatal(problemText(err))
	}
	head, _ := l.sys.Head()
	if head == shas[0] {
		t.Fatal("no attestation was signed")
	}
	if m, ok := draft.ReadMarker(must(os.ReadFile(checkoutInto(t)))); !ok || m.SHA != head || m.Revision != 2 {
		t.Errorf("the draft is drafted from %+v, want the attestation %s", m, head)
	}
	if out := listed(t); !strings.Contains(out, shortSHA(head)) || !strings.Contains(out, "attested by operator register") || strings.Contains(out, "unverified") {
		t.Errorf("deployment list does not show the attestation:\n%s", out)
	}
}

// An attestation no site took is taken back whole: the system copy's head,
// records and files are as they were and its store names no version only
// this workstation holds. So once another operator deploys on the version
// it attested, there is no fork here: a pull brings that version, and
// register attests it.
func TestRegisterTakesBackAnAttestationNoSiteTook(t *testing.T) {
	var runs []string
	s, akey, yaml, shas, l := aliceAndBob(t, &runs)
	answer := s.answer(t)
	refuse := func(all bool) {
		configSiteTransport = func(name string, _ config.Site) apply.Transport {
			return &storeHost{name: name, answer: answer, runs: &runs, noPush: all}
		}
	}
	refuse(true)
	before := fileContents(t, l.sys.Dir)
	err := runOperator([]string{"register", "--root", shas[0], "--config", l.config(), "--execute"}, noTerminalStdin(), &bytes.Buffer{})
	if err == nil {
		t.Fatal("an attestation no site took was reported registered")
	}
	after := fileContents(t, l.sys.Dir)
	for _, name := range []string{"paisans-stack.yaml", "secrets.enc.yaml", ".sops.yaml", ".paisans/config-head", ".paisans/config-root", ".paisans/config-seen", ".paisans/signing-key", "saved.json"} {
		if after[name] != before[name] {
			t.Errorf("%s is not as it was: %q, was %q", name, after[name], before[name])
		}
	}
	// The gather brought the first version; the attestation is dropped.
	if now := must(must(l.sys.Cache()).Versions(os.DevNull)); len(now) != 1 || now[0].SHA != shas[0] {
		t.Fatalf("the store names %+v, want only the sites' %s", now, shas[0])
	}

	// alice deploys on the first version; this workstation pulls it.
	v2, _ := s.commit(t, yaml, "# alice's change\n", []string{shas[0]}, 2, akey)
	answer = s.answer(t)
	refuse(false)
	empty := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := runDeployment([]string{"pull", "--root", shas[0], "--config", empty, "--execute"}, noTerminalStdin(), &bytes.Buffer{}); err != nil {
		t.Fatal(problemText(err))
	}
	if h, _ := l.sys.Head(); h != v2 {
		t.Fatalf("head %s, want alice's %s", h, v2)
	}
	if err := runOperator([]string{"register", "--root", shas[0], "--config", l.config(), "--execute"}, noTerminalStdin(), &bytes.Buffer{}); err != nil {
		t.Fatal(problemText(err))
	}
	head, _ := l.sys.Head()
	vs := must(must(l.sys.Cache()).Versions(os.DevNull))
	i := slices.IndexFunc(vs, func(v configstore.Version) bool { return v.SHA == head })
	if i < 0 || !slices.Equal(vs[i].Parents, []string{v2}) || vs[i].Revision != 3 {
		t.Fatalf("the attestation is not on alice's version: %+v", vs)
	}
}

// A directory holding a record alone is no system copy: a recovery takes
// its place, and the warning that names what is moved aside says why.
func TestHeldAlreadyGivesAReasonForARecordAlone(t *testing.T) {
	withDataDir(t)
	st := must(openStore())
	if err := st.Record(localstore.Saved{ID: fixtureDeploymentID, Status: localstore.Unverified}); err != nil {
		t.Fatal(err)
	}
	why, err := heldAlready(st, fixtureDeploymentID, "")
	if err != nil || why == "" {
		t.Fatalf("why %q, %v", why, err)
	}
}
