package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// storeHost answers the commands configstore.Pull runs with a fixed bundle,
// and logs in as login, holding the admission records in files that a
// recovery reads the deployment's id from. A host
// that is down fails everything. runs, when set, logs every command run on
// any host sharing it. A host with noPush answers but refuses a pushed
// bundle; one whose noStore says so has no store to push into.
type storeHost struct {
	name, answer, login string
	files               map[string]string
	down, noPush        bool
	noStore             func() bool
	runs                *[]string
}

func (h *storeHost) Run(c string) (string, error) {
	if h.runs != nil {
		*h.runs = append(*h.runs, h.name+": "+c)
	}
	if h.down {
		return "", errors.New("ssh: connect to host " + h.name + " port 22: Connection refused")
	}
	if strings.HasPrefix(c, `echo "user $(id -un)"`) {
		out := "user " + h.login + "\n"
		for _, p := range slices.Sorted(maps.Keys(h.files)) {
			out += p + "\n"
		}
		return out, nil
	}
	if strings.Contains(c, "bundle create") {
		return h.answer, nil
	}
	if h.noStore != nil && h.noStore() && strings.HasPrefix(c, "if [ -d ") {
		return "__PAISANS_NO_STORE__\n", nil
	}
	if h.noPush && strings.Contains(c, "bundle verify") {
		return "fatal: repository not found", errors.New("exit status 128")
	}
	return "", nil
}
func (h *storeHost) RunInput(c, _ string) (string, error) { return h.Run(c) }
func (h *storeHost) ReadFile(p string) (string, bool, error) {
	if h.down {
		return "", false, errors.New("ssh: connect to host " + h.name + " port 22: Connection refused")
	}
	c, ok := h.files[p]
	return c, ok, nil
}
func (h *storeHost) WriteFile(string, string, uint32) error { return nil }
func (h *storeHost) Describe() string                       { return h.name }

// store is a site's store for a test: a bare repository of versions of the
// fixture deployment, alice's key in it replaced with one the test holds.
type store struct {
	repo configstore.Repo
	yaml string
}

// testKey makes an ed25519 key and returns its private key path and public
// key line.
func testKey(t *testing.T) (string, string) {
	t.Helper()
	for _, tool := range []string{"git", "ssh-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	key := filepath.Join(t.TempDir(), "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	return key, strings.TrimSpace(string(pub))
}

// fixtureAs is the fixture's paisans-stack.yaml with alice's key replaced by pub.
func fixtureAs(t *testing.T, pub string) string {
	t.Helper()
	body, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadBytes(body)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Replace(string(body), cfg.Operators["alice"].KeyLines()[0], pub, 1)
}

func newStore(t *testing.T) store {
	t.Helper()
	repo, err := configstore.InitRepo(filepath.Join(t.TempDir(), "s.git"))
	if err != nil {
		t.Fatal(err)
	}
	return store{repo: repo}
}

// commit adds a version of yaml plus suffix on parents, signed with key.
func (s store) commit(t *testing.T, yaml, suffix string, parents []string, rev int, key string) (string, configstore.Files) {
	t.Helper()
	f := snapshotted(configstore.Files{Paisans: []byte(yaml + suffix), Secrets: []byte("secrets\n"), SOPS: []byte("creation_rules: []\n")})
	sha, err := s.repo.Commit(f, parents, rev, key, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	return sha, f
}

// answer is what Pull reads from a site holding this store.
func (s store) answer(t *testing.T) string {
	t.Helper()
	b, err := s.repo.Bundle()
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b) + "\n"
}

// storedVersion is a root version of the fixture deployment, signed by an
// admin it lists, as the sites' bundle answers it, with its sha and files.
func storedVersion(t *testing.T) (string, string, configstore.Files) {
	t.Helper()
	shas, answer, files := storedHistory(t, 1, 0)
	return shas[0], answer, files[0]
}

// storedHistory is n versions of the fixture deployment, each on the one
// before, each differing from the last by a comment in paisans-stack.yaml, as the
// sites' bundle answers them. With forged above 0, it also holds a chain of
// that many versions on the root signed by mallory, whose paisans-stack.yaml lists
// her key as alice's: what someone with root on a site can write.
func storedHistory(t *testing.T, n, forged int) ([]string, string, []configstore.Files) {
	t.Helper()
	shas, answer, files, _, _ := storedHistoryKey(t, n, forged)
	return shas, answer, files
}

// storedHistoryKey is storedHistory with the signing key's private key path
// and public key line.
func storedHistoryKey(t *testing.T, n, forged int) ([]string, string, []configstore.Files, string, string) {
	t.Helper()
	key, pub := testKey(t)
	s := newStore(t)
	yaml := fixtureAs(t, pub)
	var shas []string
	var files []configstore.Files
	for i := range n {
		var parents []string
		if i > 0 {
			parents = shas[i-1:]
		}
		sha, f := s.commit(t, yaml, strings.Repeat("# edited\n", i), parents, i+1, key)
		shas, files = append(shas, sha), append(files, f)
	}
	if forged > 0 {
		mkey, mpub := testKey(t)
		myaml := fixtureAs(t, mpub)
		parent := shas[0]
		for i := range forged {
			parent, _ = s.commit(t, myaml, "# forged\n", []string{parent}, i+2, mkey)
		}
	}
	return shas, s.answer(t), files, key, pub
}

// recoverHosts makes --from reach a host holding the version, and the
// fixture's sites reach home-a holding it and every other site down.
func recoverHosts(t *testing.T, answer string) {
	t.Helper()
	savedDest, savedSite := reachDestination, configSiteTransport
	t.Cleanup(func() { reachDestination, configSiteTransport = savedDest, savedSite })
	files := ownRecord(t)
	reachDestination = func(dest config.Destination) apply.Transport {
		return &storeHost{name: dest.String(), answer: answer, login: "paisans-f2a9", files: files}
	}
	configSiteTransport = func(name string, _ config.Site) apply.Transport {
		return &storeHost{name: name, answer: answer, down: name != "home-a"}
	}
	var b bytes.Buffer
	savedR := reporterOverride
	reporterOverride = ui.NewPlain(&b, false)
	t.Cleanup(func() { reporterOverride = savedR })
}

// ownRecord is a host's admission records: the fixture deployment's, for
// home-a reached as the deployment's own user, paisans-f2a9.
func ownRecord(t *testing.T) map[string]string {
	t.Helper()
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Sites["home-a"]
	s.SSH.User = ""
	cfg.Sites["home-a"] = s
	return map[string]string{admittedPath(cfg): admittedRecord(t, cfg, "home-a")}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// A recovery that reaches one of the fixture's four sites directly writes
// the files at no terminal, with nothing asked.
func TestConfigRecoverFromOneSiteAsksNothing(t *testing.T) {
	sha, answer, f := storedVersion(t)
	recoverHosts(t, answer)
	dir := t.TempDir()
	args := []string{"recover", "--from", "ubuntu@203.0.113.9", "--root", sha, "--config", filepath.Join(dir, "paisans-stack.yaml"), "--execute"}

	var out bytes.Buffer
	if err := runDeployment(args, noTerminalStdin(), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "partial") {
		t.Fatalf("output names partial: %q", out.String())
	}
	l := configstore.Local{Dir: dir}
	got, err := l.Files()
	if err != nil || !got.Equal(f) {
		t.Fatalf("files %v", err)
	}
	if h, _ := l.Head(); h != sha {
		t.Fatalf("head %q", h)
	}
	if r, _ := l.Root(); r != sha {
		t.Fatalf("root %q", r)
	}
}

// Without --execute a recovery writes nothing.
func TestConfigRecoverDryRunWritesNothing(t *testing.T) {
	sha, answer, _ := storedVersion(t)
	recoverHosts(t, answer)
	dir := t.TempDir()
	args := []string{"recover", "--from", "ubuntu@203.0.113.9", "--root", sha, "--config", filepath.Join(dir, "paisans-stack.yaml")}
	var out bytes.Buffer
	if err := runDeployment(args, noTerminalStdin(), &out); err != nil {
		t.Fatal(err)
	}
	if (configstore.Local{Dir: dir}).HasFiles() {
		t.Fatal("files written")
	}
}

// A recovery names the directory it writes into as a full path, even when
// --config is relative: "recover into ." does not say where the files went.
func TestConfigRecoverPrintsTheDirectoryInFull(t *testing.T) {
	sha, answer, _ := storedVersion(t)
	recoverHosts(t, answer)
	t.Chdir(t.TempDir())
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	out := plainReporter(t)
	if err := runDeployment([]string{"recover", "--from", "ubuntu@203.0.113.9", "--root", sha}, noTerminalStdin(), &bytes.Buffer{}); err != nil {
		t.Fatal(problemText(err))
	}
	if !strings.Contains(out.String(), "recover into "+dir+"\n") {
		t.Fatalf("want recover into %s:\n%s", dir, out.String())
	}
}

// Recovery is onto a workstation with nothing, and needs an anchor.
func TestConfigRecoverRefusals(t *testing.T) {
	sha, answer, _ := storedVersion(t)
	recoverHosts(t, answer)
	dir := t.TempDir()
	var out bytes.Buffer
	err := runDeployment([]string{"recover", "--from", "ubuntu@203.0.113.9", "--config", filepath.Join(dir, "paisans-stack.yaml")}, noTerminalStdin(), &out)
	if err == nil || !strings.Contains(problemText(err), "--root") || !strings.Contains(problemText(err), "--key") {
		t.Fatalf("no anchor: %v", problemText(err))
	}
	os.WriteFile(filepath.Join(dir, "paisans-stack.yaml"), []byte("x"), 0o644)
	err = runDeployment([]string{"recover", "--from", "ubuntu@203.0.113.9", "--root", sha, "--config", filepath.Join(dir, "paisans-stack.yaml")}, noTerminalStdin(), &out)
	if err == nil || !strings.Contains(problemText(err), "already") {
		t.Fatalf("files present: %v", problemText(err))
	}
	if err := runDeployment([]string{"recover", "--config", filepath.Join(t.TempDir(), "paisans-stack.yaml")}, noTerminalStdin(), &out); err == nil {
		t.Fatal("recover without --from ran")
	}
}

// pullWorkstation is a workstation holding files, based on base, with the
// root pinned, and every site of the fixture answering.
func pullWorkstation(t *testing.T, answer, root, base string, f configstore.Files) configstore.Local {
	t.Helper()
	recoverHosts(t, answer)
	configSiteTransport = func(name string, _ config.Site) apply.Transport {
		return &storeHost{name: name, answer: answer}
	}
	l := configstore.Local{Dir: t.TempDir()}
	if err := l.WriteFiles(f); err != nil {
		t.Fatal(err)
	}
	if err := l.SetRoot(root); err != nil {
		t.Fatal(err)
	}
	if err := l.SetHead(base); err != nil {
		t.Fatal(err)
	}
	return l
}

// With no local edits, pull moves the files to the sites' head; a dry run
// does not.
func TestConfigPullMovesUneditedFiles(t *testing.T) {
	shas, answer, files := storedHistory(t, 2, 0)
	l := pullWorkstation(t, answer, shas[0], shas[0], files[0])
	cfgPath := filepath.Join(l.Dir, "paisans-stack.yaml")
	var out bytes.Buffer
	if err := runDeployment([]string{"pull", "--config", cfgPath}, noTerminalStdin(), &out); err != nil {
		t.Fatal(err)
	}
	if h, _ := l.Head(); h != shas[0] {
		t.Fatalf("a dry run moved the head to %s", h)
	}
	if err := runDeployment([]string{"pull", "--config", cfgPath, "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(err)
	}
	got, _ := l.Files()
	if h, _ := l.Head(); h != shas[1] || !got.Equal(files[1]) {
		t.Fatalf("head %s, files moved %v", h, got.Equal(files[1]))
	}
}

// Over local edits, pull refuses, shows the difference, and with --execute
// leaves the head's files in .paisans/incoming/ and the local ones alone.
func TestConfigPullOverEditsRefusesAndShowsTheDifference(t *testing.T) {
	shas, answer, files := storedHistory(t, 2, 0)
	edited := files[0]
	edited.Paisans = append(append([]byte{}, edited.Paisans...), "# mine\n"...)
	edited.Secrets = []byte("a: 1\nb: 2\n")
	l := pullWorkstation(t, answer, shas[0], shas[0], edited)
	var out bytes.Buffer
	err := runDeployment([]string{"pull", "--config", filepath.Join(l.Dir, "paisans-stack.yaml"), "--execute"}, noTerminalStdin(), &out)
	if err == nil || !strings.Contains(problemText(err), "--merge") {
		t.Fatalf("pull over edits: %v", err)
	}
	for _, want := range []string{"-# mine", "+# edited", "secrets.enc.yaml differs"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "a: 1") {
		t.Error("a secret value was printed")
	}
	in := configstore.Local{Dir: l.Incoming()}
	if got, err := in.Files(); err != nil || !got.Equal(files[1]) {
		t.Fatalf("incoming %v", err)
	}
	if got, _ := l.Files(); !got.Equal(edited) {
		t.Fatal("the local files were overwritten")
	}
	if h, _ := l.Head(); h != shas[0] {
		t.Fatalf("head moved to %s", h)
	}
}

func TestSecretsKeyChangesNamesKeysOnly(t *testing.T) {
	got := secretsKeyChanges([]byte("a:\n  b: ENC[x]\n  c: ENC[y]\nsops:\n  mac: m1\n"), []byte("a:\n  b: ENC[x]\n  c: ENC[z]\n  d: ENC[w]\nsops:\n  mac: m2\n"))
	want := []string{"secrets: a.c differs", "secrets: a.d only in the head"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
}

// A publish signs with the recorded key, which the agent must hold, or else
// with the one agent key an admin of the base lists; none or several refuse.
func TestSigningKeyFor(t *testing.T) {
	cfg := fixture(t)
	alice := cfg.Operators["alice"].KeyLines()[0]
	other := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB bob@example.org"
	// The fixture declares bob under other; the first cases are about a
	// key no operator lists.
	delete(cfg.Operators, "bob")
	saved := agentKeys
	t.Cleanup(func() { agentKeys = saved })
	l := configstore.Local{Dir: t.TempDir()}
	for _, c := range []struct {
		held []string
		want string
	}{{[]string{other, alice}, alice}, {[]string{other}, ""}, {nil, ""}} {
		agentKeys = func() []string { return c.held }
		got, err := signingKeyFor(l, cfg)
		if got != c.want || (c.want == "") != (err != nil) {
			t.Errorf("held %d keys: %q, %v", len(c.held), got, err)
		}
	}
	cfg.Operators["bob"] = config.Operator{Name: "Bob Example", Email: "bob@example.org", Keys: other}
	agentKeys = func() []string { return []string{alice, other} }
	if _, err := signingKeyFor(l, cfg); err == nil || !strings.Contains(problemText(err), "more than one") {
		t.Fatalf("two operator keys: %v", err)
	}
	if err := l.SetSigningKey(other); err != nil {
		t.Fatal(err)
	}
	if got, err := signingKeyFor(l, cfg); err != nil || got != other {
		t.Fatalf("recorded: %q %v", got, err)
	}
	agentKeys = func() []string { return []string{alice} }
	if _, err := signingKeyFor(l, cfg); err == nil || !strings.Contains(problemText(err), "signing key") {
		t.Fatalf("recorded key not held: %v", err)
	}
}

// allSitesUp makes every fixture site answer with answer, logging each
// command to runs.
func allSitesUp(answer string, runs *[]string) {
	configSiteTransport = func(name string, _ config.Site) apply.Transport {
		return &storeHost{name: name, answer: answer, runs: runs}
	}
}

func synced(runs []string) bool {
	for _, r := range runs {
		if strings.Contains(r, "systemctl --user start") {
			return true
		}
	}
	return false
}

// A recovery learns the deployment from the hosts' admission records. A
// login with a record of its own names that record's deployment. Any other
// login, the owner's, names every deployment the host admits, and the host
// is reached again as the user the chosen deployment's record names, so
// recovering needs no name the lost workstation held. More than one
// deployment needs --id, and a host with no record names none.
func TestConfigRecoverReadsTheHostsAdmissionRecords(t *testing.T) {
	saved := reachDestination
	t.Cleanup(func() { reachDestination = saved })
	files := ownRecord(t)
	reachDestination = func(dest config.Destination) apply.Transport {
		return &storeHost{name: dest.String(), login: dest.User, files: files}
	}
	find := func(from, id string) (string, []string, string, error) {
		var b bytes.Buffer
		dest := must(config.ParseDestination(from))
		got, hosts, err := recoverID(ui.NewPlain(&b, false), []config.Destination{dest}, id)
		return got, slices.Sorted(maps.Keys(hosts)), b.String(), err
	}

	for _, from := range []string{"paisans-f2a9@203.0.113.9", "ubuntu@203.0.113.9"} {
		id, hosts, out, err := find(from, "")
		if err != nil || id != exampleID || !slices.Equal(hosts, []string{"paisans-f2a9@203.0.113.9:22"}) {
			t.Errorf("%s: recoverID = %q, %v, %v", from, id, hosts, err)
		}
		if moved := strings.Contains(out, "reached as paisans-f2a9"); moved != strings.HasPrefix(from, "ubuntu") {
			t.Errorf("%s: the note of the login it moved to: %q", from, out)
		}
	}

	const otherID = "0b0bc4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"
	for p, c := range ownRecord(t) {
		files[strings.Replace(p, "f2a9", "0b0b", 1)] = strings.ReplaceAll(strings.ReplaceAll(c, "f2a9c4e1", "0b0bc4e1"), "paisans-f2a9", "paisans-0b0b")
	}
	if id, _, _, err := find("paisans-f2a9@203.0.113.9", ""); err != nil || id != exampleID {
		t.Errorf("the login's own record beside another: %q, %v", id, err)
	}
	_, _, _, err := find("ubuntu@203.0.113.9", "")
	if text := problemText(err); err == nil || !strings.Contains(text, "more than one deployment") || !strings.Contains(text, exampleID) || !strings.Contains(text, otherID) {
		t.Errorf("an owner's login on a host of two deployments: %v\n%s", err, text)
	}
	if id, hosts, _, err := find("ubuntu@203.0.113.9", otherID); err != nil || id != otherID || !slices.Equal(hosts, []string{"paisans-0b0b@203.0.113.9:22"}) {
		t.Errorf("--id on a host of two deployments: %q, %v, %v", id, hosts, err)
	}

	clear(files)
	_, _, _, err = find("ubuntu@203.0.113.9", "")
	if text := problemText(err); err == nil || !strings.Contains(text, "no admission record for any deployment") || !strings.Contains(text, "ubuntu@203.0.113.9") {
		t.Errorf("a host with no record: %v\n%s", err, text)
	}
	if id, hosts, _, err := find("ubuntu@203.0.113.9", exampleID); err != nil || id != exampleID || !slices.Equal(hosts, []string{"ubuntu@203.0.113.9:22"}) {
		t.Errorf("--id on a host with no record keeps the login: %q, %v, %v", id, hosts, err)
	}
}

// A record whose deployment is not an id is never read as one, so a
// recovery that finds only such a record names nothing to recover, and no
// command but the listing reaches any host.
func TestConfigRecoverRefusesAnInjectedID(t *testing.T) {
	_, answer, _ := storedVersion(t)
	recoverHosts(t, answer)
	var runs []string
	files := ownRecord(t)
	for p, c := range files {
		files[p] = strings.Replace(c, exampleID, exampleID+";touch /pwned", 1)
	}
	reachDestination = func(dest config.Destination) apply.Transport {
		return &storeHost{name: dest.String(), answer: answer, login: "paisans-f2a9", files: files, runs: &runs}
	}
	allSitesUp(answer, &runs)
	dir := t.TempDir()
	var out bytes.Buffer
	err := runDeployment([]string{"recover", "--from", "ubuntu@203.0.113.9", "--root", strings.Repeat("a", 40), "--config", filepath.Join(dir, "paisans-stack.yaml"), "--execute"}, noTerminalStdin(), &out)
	if text := problemText(err); err == nil || !strings.Contains(text, "no admission record for any deployment") || strings.Contains(text, "touch /pwned") {
		t.Fatalf("got %v: %s", err, text)
	}
	for _, r := range runs {
		if !strings.Contains(r, `echo "user $(id -un)"`) {
			t.Fatalf("a command reached a host: %q", r)
		}
	}
}

// A dry run and log never ask a site to sync; pull --execute does.
func TestConfigDryRunsDoNotSync(t *testing.T) {
	shas, answer, files := storedHistory(t, 2, 0)
	l := pullWorkstation(t, answer, shas[0], shas[0], files[0])
	var runs []string
	allSitesUp(answer, &runs)
	cfgPath := filepath.Join(l.Dir, "paisans-stack.yaml")
	var out bytes.Buffer
	for _, sub := range []string{"pull", "log"} {
		if err := runDeployment([]string{sub, "--config", cfgPath}, noTerminalStdin(), &out); err != nil {
			t.Fatalf("%s: %v", sub, err)
		}
	}
	if synced(runs) || len(runs) == 0 {
		t.Fatalf("dry runs: %q", runs)
	}
	if err := runDeployment([]string{"pull", "--config", cfgPath, "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(err)
	}
	if !synced(runs) {
		t.Fatal("pull --execute did not sync")
	}
}

// A dry run says the sites were not asked to sync with their peers only when
// there are peers: a stack of one site has none for a sync to bring versions
// from.
func TestDryRunGatherNotesTheSkippedSyncOnlyWithPeers(t *testing.T) {
	shas, answer, files := storedHistory(t, 2, 0)
	l := pullWorkstation(t, answer, shas[0], shas[0], files[0])
	a, err := l.Anchor()
	if err != nil {
		t.Fatal(err)
	}
	id, _ := config.LoadBytes(must(os.ReadFile(fixtureConfig())))
	const note = "the sites were not asked to sync with their peers first"
	for _, tc := range []struct {
		sites []string
		want  bool
	}{
		{[]string{"home-a"}, false},
		{[]string{"home-a", "home-b"}, true},
	} {
		sites := map[string]apply.Transport{}
		for _, n := range tc.sites {
			sites[n] = &storeHost{name: n, answer: answer}
		}
		var b bytes.Buffer
		if _, err := gatherAt(ui.NewPlain(&b, false), l, sites, id.ID, a, gatherMode{}, noTerminalStdin(), io.Discard); err != nil {
			t.Fatalf("%d sites: %v", len(sites), problemText(err))
		}
		if got := strings.Contains(b.String(), note); got != tc.want {
			t.Errorf("%d sites: note shown %v, want %v:\n%s", len(sites), got, tc.want, b.String())
		}
	}
}

// A head older than one this workstation has seen asks for rollback, and
// without a terminal is refused with nothing written.
func TestConfigPullAsksRollback(t *testing.T) {
	shas, answer, files := storedHistory(t, 2, 0)
	l := pullWorkstation(t, answer, shas[0], shas[0], files[0])
	seen := "9 " + strings.Repeat("b", 40) + "\n"
	if err := os.WriteFile(filepath.Join(l.Dir, ".paisans", "config-seen"), []byte(seen), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runDeployment([]string{"pull", "--config", filepath.Join(l.Dir, "paisans-stack.yaml"), "--execute"}, noTerminalStdin(), &out)
	if err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("got %v", err)
	}
	if got, _ := l.Files(); !got.Equal(files[0]) {
		t.Fatal("files written")
	}
	if err := runDeployment([]string{"pull", "--config", filepath.Join(l.Dir, "paisans-stack.yaml"), "--execute"}, answering(t, "rollback\n"), &out); err != nil {
		t.Fatal(err)
	}
	if h, _ := l.Head(); h != shas[1] {
		t.Fatalf("head %s after rollback", h)
	}
	if b, _ := os.ReadFile(filepath.Join(l.Dir, ".paisans", "config-seen")); string(b) != "2 "+shas[1]+"\n" {
		t.Fatalf("seen after accepting %q", b)
	}
}

// Once rollback is accepted on a pull, publish goes on from that head, and
// the new version's revision follows it.
func TestConfigPublishAfterAnAcceptedRollback(t *testing.T) {
	shas, answer, files, key, pub := storedHistoryKey(t, 2, 0)
	l := pullWorkstation(t, answer, shas[0], shas[0], files[0])
	seenPath := filepath.Join(l.Dir, ".paisans", "config-seen")
	os.WriteFile(seenPath, []byte("9 "+strings.Repeat("b", 40)+"\n"), 0o644)
	cfgPath := filepath.Join(l.Dir, "paisans-stack.yaml")
	var out bytes.Buffer
	if err := runDeployment([]string{"pull", "--config", cfgPath, "--execute"}, answering(t, "rollback\n"), &out); err != nil {
		t.Fatal(err)
	}
	savedKeys, savedSign := agentKeys, configSignWith
	agentKeys = func() []string { return []string{pub} }
	configSignWith = func(string) string { return key }
	t.Cleanup(func() { agentKeys, configSignWith = savedKeys, savedSign })
	edited := files[1]
	edited.Paisans = append(append([]byte{}, edited.Paisans...), "# next\n"...)
	if err := l.WriteFiles(edited); err != nil {
		t.Fatal(err)
	}
	if err := runDeployment([]string{"publish", "--config", cfgPath, "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(err)
	}
	head, _ := l.Head()
	b, _ := os.ReadFile(seenPath)
	if head == shas[1] || string(b) != "3 "+head+"\n" {
		t.Fatalf("head %s, seen %q", head, b)
	}
}

// log and dry runs never ask for rollback: they report it, and a dry run
// says --execute will ask.
func TestConfigRollbackIsReportedWithoutAsking(t *testing.T) {
	shas, answer, files := storedHistory(t, 2, 0)
	l := pullWorkstation(t, answer, shas[0], shas[1], files[1])
	seen := "9 " + strings.Repeat("b", 40)
	os.WriteFile(filepath.Join(l.Dir, ".paisans", "config-seen"), []byte(seen+"\n"), 0o644)
	cfgPath := filepath.Join(l.Dir, "paisans-stack.yaml")
	var rep, out bytes.Buffer
	reporterOverride = ui.NewPlain(&rep, false)
	if err := runDeployment([]string{"log", "--config", cfgPath}, noTerminalStdin(), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"revision 9", strings.Repeat("b", 40), "revision 2", shas[1]} {
		if !strings.Contains(rep.String(), want) {
			t.Errorf("log's report lacks %q:\n%s", want, rep.String())
		}
	}
	if !strings.Contains(out.String(), shas[1][:12]) || !strings.Contains(out.String(), shas[0][:12]) {
		t.Errorf("log did not list the versions:\n%s", out.String())
	}
	if want := "  by paisans " + version.Current().String() + "  "; strings.Count(out.String(), want) != 2 {
		t.Errorf("log does not name the build that published each version, %q:\n%s", want, out.String())
	}
	rep.Reset()
	if err := runDeployment([]string{"pull", "--config", cfgPath}, noTerminalStdin(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.String(), "--execute will ask for rollback") {
		t.Errorf("dry run report:\n%s", rep.String())
	}
	if b, _ := os.ReadFile(filepath.Join(l.Dir, ".paisans", "config-seen")); string(b) != seen+"\n" {
		t.Fatalf("seen changed to %q", b)
	}
}

// Judging from a --root this run does not record leaves no newest revision
// behind.
func TestConfigATriedRootRaisesNothing(t *testing.T) {
	shas, answer, files := storedHistory(t, 2, 0)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	l := configstore.Local{Dir: t.TempDir()}
	l.WriteFiles(files[0])
	l.SetHead(shas[0])
	cfgPath := filepath.Join(l.Dir, "paisans-stack.yaml")
	var out bytes.Buffer
	for _, sub := range []string{"log", "pull"} {
		if err := runDeployment([]string{sub, "--root", shas[0], "--config", cfgPath}, noTerminalStdin(), &out); err != nil {
			t.Fatalf("%s: %v", sub, err)
		}
	}
	if rev, _, _ := l.Seen(); rev != 0 {
		t.Fatalf("seen raised to %d", rev)
	}
	if err := runDeployment([]string{"pull", "--root", shas[0], "--config", cfgPath, "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(err)
	}
	if rev, _, _ := l.Seen(); rev != 2 {
		t.Fatalf("a recorded root left seen at %d", rev)
	}
}

// A site holding mallory's versions, longer than the real history, moves
// nothing: pull keeps the files, and recover writes the real head's.
func TestConfigForgedVersionsChangeNoFiles(t *testing.T) {
	shas, answer, files := storedHistory(t, 2, 3)
	l := pullWorkstation(t, answer, shas[0], shas[1], files[1])
	var out bytes.Buffer
	if err := runDeployment([]string{"pull", "--config", filepath.Join(l.Dir, "paisans-stack.yaml"), "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := l.Files(); !got.Equal(files[1]) {
		t.Fatal("pull changed the files")
	}
	if h, _ := l.Head(); h != shas[1] {
		t.Fatalf("head %s", h)
	}
	dir := t.TempDir()
	err := runDeployment([]string{"recover", "--from", "ubuntu@203.0.113.9", "--root", shas[0], "--config", filepath.Join(dir, "paisans-stack.yaml"), "--execute"}, noTerminalStdin(), &out)
	if err != nil {
		t.Fatal(err)
	}
	r := configstore.Local{Dir: dir}
	if got, _ := r.Files(); !got.Equal(files[1]) {
		t.Fatalf("recover wrote %q", got.Paisans[len(got.Paisans)-20:])
	}
}

// A recovery that finds a fork writes each head's files to incoming/<sha>
// with --execute and chooses neither.
func TestConfigRecoverOnAForkWritesEachHead(t *testing.T) {
	key, pub := testKey(t)
	s := newStore(t)
	yaml := fixtureAs(t, pub)
	root, _ := s.commit(t, yaml, "", nil, 1, key)
	a, fa := s.commit(t, yaml, "# a\n", []string{root}, 2, key)
	b, fb := s.commit(t, yaml, "# b\n", []string{root}, 2, key)
	answer := s.answer(t)
	recoverHosts(t, answer)
	allSitesUp(answer, nil)
	dir := t.TempDir()
	var out bytes.Buffer
	err := runDeployment([]string{"recover", "--from", "ubuntu@203.0.113.9", "--root", root, "--config", filepath.Join(dir, "paisans-stack.yaml"), "--execute"}, noTerminalStdin(), &out)
	if err == nil || !strings.Contains(problemText(err), "publish --merge") {
		t.Fatalf("got %v", err)
	}
	l := configstore.Local{Dir: dir}
	if l.HasFiles() {
		t.Fatal("a fork's recovery wrote the files")
	}
	for sha, want := range map[string]configstore.Files{a: fa, b: fb} {
		got, err := (configstore.Local{Dir: l.IncomingFor(sha)}).Files()
		if err != nil || !got.Equal(want) {
			t.Fatalf("incoming %s: %v", sha[:12], err)
		}
		if fi, err := os.Stat(filepath.Join(l.IncomingFor(sha), "secrets.enc.yaml")); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("secrets mode %v %v", fi.Mode(), err)
		}
	}
}

// The publish dry run refuses a key that --execute would refuse: here the
// recorded key, which the agent holds, is no admin of the head.
func TestConfigPublishDryRunChecksTheSigner(t *testing.T) {
	shas, answer, files := storedHistory(t, 1, 0)
	l := pullWorkstation(t, answer, shas[0], shas[0], files[0])
	_, other := testKey(t)
	if err := l.SetSigningKey(other); err != nil {
		t.Fatal(err)
	}
	saved := agentKeys
	agentKeys = func() []string { return []string{other} }
	t.Cleanup(func() { agentKeys = saved })
	var out bytes.Buffer
	err := runDeployment([]string{"publish", "--config", filepath.Join(l.Dir, "paisans-stack.yaml")}, noTerminalStdin(), &out)
	if err == nil || !strings.Contains(err.Error(), "belongs to no operator") {
		t.Fatalf("got %v", err)
	}
}

// Typing rollback lowers the seen record only once the command commits: a
// pull that is given the word and then stops at local edits leaves it as it
// was.
func TestConfigRollbackThenRefusedPullLeavesSeen(t *testing.T) {
	shas, answer, files := storedHistory(t, 2, 0)
	edited := files[0]
	edited.Paisans = append(append([]byte{}, edited.Paisans...), "# local edit\n"...)
	l := pullWorkstation(t, answer, shas[0], shas[0], edited)
	seenPath := filepath.Join(l.Dir, ".paisans", "config-seen")
	seen := "9 " + strings.Repeat("b", 40) + "\n"
	if err := os.WriteFile(seenPath, []byte(seen), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runDeployment([]string{"pull", "--config", filepath.Join(l.Dir, "paisans-stack.yaml"), "--execute"}, answering(t, "rollback\n"), &out)
	if err == nil || !strings.Contains(problemText(err), "edits") || !strings.Contains(out.String(), "Type 'rollback'") {
		t.Fatalf("got %v\n%s", problemText(err), out.String())
	}
	if b, _ := os.ReadFile(seenPath); string(b) != seen {
		t.Fatalf("seen changed to %q by a pull that stopped", b)
	}
	if h, _ := l.Head(); h != shas[0] {
		t.Fatalf("head moved to %s", h)
	}
}

// A pull --execute --root that refuses records neither the root nor a newer
// revision seen.
func TestConfigRefusedPullWithRootRecordsNothing(t *testing.T) {
	shas, answer, files := storedHistory(t, 2, 0)
	recoverHosts(t, answer)
	l := configstore.Local{Dir: t.TempDir()}
	edited := files[0]
	edited.Paisans = append(append([]byte{}, edited.Paisans...), "# local edit\n"...)
	l.WriteFiles(edited)
	l.SetHead(shas[0])
	var out bytes.Buffer
	err := runDeployment([]string{"pull", "--root", shas[0], "--config", filepath.Join(l.Dir, "paisans-stack.yaml"), "--execute"}, noTerminalStdin(), &out)
	if err == nil || !strings.Contains(problemText(err), "edits") {
		t.Fatalf("got %v", problemText(err))
	}
	if rev, _, _ := l.Seen(); rev != 0 {
		t.Fatalf("seen raised to %d", rev)
	}
	if r, _ := l.Root(); r != "" {
		t.Fatalf("root %s recorded", r)
	}
}

// Dry runs of publish, pull and log leave the seen record alone.
func TestConfigDryRunsRecordNothingSeen(t *testing.T) {
	shas, answer, files, _, pub := storedHistoryKey(t, 2, 0)
	l := pullWorkstation(t, answer, shas[0], shas[1], files[1])
	saved := agentKeys
	agentKeys = func() []string { return []string{pub} }
	t.Cleanup(func() { agentKeys = saved })
	cfgPath := filepath.Join(l.Dir, "paisans-stack.yaml")
	var out bytes.Buffer
	for _, sub := range []string{"pull", "log", "publish"} {
		if err := runDeployment([]string{sub, "--config", cfgPath}, noTerminalStdin(), &out); err != nil {
			t.Fatalf("%s: %v", sub, err)
		}
		if rev, _, _ := l.Seen(); rev != 0 {
			t.Fatalf("a %s dry run raised seen to %d", sub, rev)
		}
	}
}

// A first version published from one site of four that answered, holding
// nothing, is signed and pushed at no terminal, with nothing asked.
func TestPublishFirstVersionFromOneSiteAsksNothing(t *testing.T) {
	key, pub := testKey(t)
	recoverHosts(t, "__PAISANS_EMPTY__\n") // home-a alone answers, holding nothing
	var runs []string
	configSiteTransport = func(name string, _ config.Site) apply.Transport {
		return &storeHost{name: name, answer: "__PAISANS_EMPTY__\n", down: name != "home-a", runs: &runs}
	}
	saved, savedSign := agentKeys, configSignWith
	agentKeys = func() []string { return []string{pub} }
	configSignWith = func(string) string { return key }
	t.Cleanup(func() { agentKeys, configSignWith = saved, savedSign })
	l := configstore.Local{Dir: t.TempDir()}
	f := snapshotted(configstore.Files{Paisans: []byte(fixtureAs(t, pub)), Secrets: []byte("secrets\n"), SOPS: []byte("creation_rules: []\n")})
	if err := l.WriteFiles(f); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(l.Dir, "paisans-stack.yaml")
	var rep, out bytes.Buffer
	reporterOverride = ui.NewPlain(&rep, false)
	if err := runDeployment([]string{"publish", "--config", cfgPath}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err))
	}
	if strings.Contains(rep.String(), "WARN only") {
		t.Errorf("dry run warned:\n%s", rep.String())
	}
	if err := runDeployment([]string{"publish", "--config", cfgPath, "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(problemText(err))
	}
	if h, _ := l.Head(); h == "" {
		t.Fatal("no head after publishing")
	}
	took := false
	for _, r := range runs {
		if strings.HasPrefix(r, "home-a: ") && strings.Contains(r, "bundle verify") {
			took = true
		}
	}
	if !took {
		t.Fatalf("the version did not reach home-a: %q", runs)
	}
}
