package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/draft"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// mergeCase is a store of two versions of the fixture whose secrets differ,
// a system copy at the first, and a draft of it whose own secrets differ
// again.
type mergeCase struct {
	l     pullCase
	cfg   string
	shas  []string
	sops  string
	files configstore.Files
}

// newMergeCase encrypts base, ours and theirs to one test key. edit may change
// the yaml of the head only.
func newMergeCase(t *testing.T, base, ours, theirs []string, headYAML string) mergeCase {
	t.Helper()
	return newMergeCaseSOPS(t, base, ours, theirs, headYAML, "")
}

// newMergeCaseSOPS is newMergeCase with the head's .sops.yaml replaced by
// headSOPS when that is not empty.
func newMergeCaseSOPS(t *testing.T, base, ours, theirs []string, headYAML, headSOPS string) mergeCase {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "key.txt")
	if err := os.WriteFile(keyFile, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", keyFile)
	sops := "creation_rules:\n  - age: " + identity.Recipient().String() + "\n"
	key, pub := testKey(t)
	yaml := fixtureAs(t, pub)
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	enc := func(kv []string) []byte {
		s := &config.Secrets{Version: config.SecretsVersion, ID: cfg.ID}
		for i := 0; i < len(kv); i += 2 {
			// Set does not take a next key, which only a rekey writes.
			if site, ok := strings.CutSuffix(strings.TrimPrefix(kv[i], "sites."), ".wireguard_next_private_key"); ok {
				if s.Sites == nil {
					s.Sites = map[string]config.SiteSecrets{}
				}
				v := s.Sites[site]
				v.WireGuardNextPrivateKey = kv[i+1]
				s.Sites[site] = v
				continue
			}
			if err := s.Set(kv[i], kv[i+1]); err != nil {
				t.Fatal(err)
			}
		}
		p := filepath.Join(t.TempDir(), "s.enc.yaml")
		if err := config.WriteSecrets(p, s, []string{identity.Recipient().String()}); err != nil {
			t.Fatal(err)
		}
		return must(os.ReadFile(p))
	}
	s := newStore(t)
	b := snapshotted(configstore.Files{Paisans: []byte(yaml), Secrets: enc(base), SOPS: []byte(sops)})
	h := snapshotted(configstore.Files{Paisans: []byte(yaml + headYAML), Secrets: enc(theirs), SOPS: []byte(sops)})
	if headSOPS != "" {
		h.SOPS = []byte(headSOPS)
	}
	var shas []string
	when := time.Unix(1_800_000_000, 0)
	sha0, err := s.repo.Commit(b, nil, 1, key, when)
	if err != nil {
		t.Fatal(err)
	}
	sha1, err := s.repo.Commit(h, []string{sha0}, 2, key, when)
	if err != nil {
		t.Fatal(err)
	}
	shas = []string{sha0, sha1}
	local := snapshotted(configstore.Files{Paisans: b.Paisans, Secrets: enc(ours), SOPS: b.SOPS})
	l := pullWorkstation(t, s.answer(t), sha0, sha0, local)
	return mergeCase{l: l, cfg: l.cfg, shas: shas, sops: sops, files: local}
}

func (m mergeCase) pull(t *testing.T, execute bool) (string, error) {
	t.Helper()
	args := []string{"pull", "--config", m.cfg}
	if execute {
		args = append(args, "--execute")
	}
	rep := plainReporter(t)
	var out bytes.Buffer
	err := runDeployment(args, noTerminalStdin(), &out)
	return rep.String() + out.String(), err
}

func (m mergeCase) secrets(t *testing.T) *config.Secrets {
	t.Helper()
	id := must(config.LoadBytes(must(os.ReadFile(m.cfg)))).ID
	return must(config.LoadDeploymentSecrets(filepath.Join(m.l.dir, "secrets.enc.yaml"), id))
}

// unchanged fails the test when the draft or the system copy changed: the
// draft's files are still m.files and the system copy is at the first
// version.
func (m mergeCase) unchanged(t *testing.T) {
	t.Helper()
	if got := m.l.draft(t); !got.Equal(m.files) {
		t.Fatal("the draft changed")
	}
	if h, _ := m.l.Head(); h != m.shas[0] {
		t.Fatalf("the system copy moved to %s", h)
	}
}

// mergedInto fails the test unless the draft is drafted from the head and
// the system copy is at it.
func (m mergeCase) mergedInto(t *testing.T) {
	t.Helper()
	if mk, ok := draft.ReadMarker(must(os.ReadFile(m.cfg))); !ok || mk.SHA != m.shas[1] || mk.Revision != 2 {
		t.Errorf("marker %+v %v", mk, ok)
	}
	if h, _ := m.l.Head(); h != m.shas[1] {
		t.Fatalf("head %s, want %s", h, m.shas[1])
	}
}

var (
	mergeBase   = []string{"external.a", "base-a", "external.b", "base-b"}
	mergeOurs   = []string{"external.a", "ours-a", "external.b", "base-b"}
	mergeTheirs = []string{"external.a", "base-a", "external.b", "theirs-b"}
)

func noValues(t *testing.T, shown string) {
	t.Helper()
	for _, v := range []string{"base-a", "base-b", "ours-a", "theirs-b", "ours-b", "theirs-a"} {
		if strings.Contains(shown, v) {
			t.Errorf("the value %q appears in:\n%s", v, shown)
		}
	}
}

func TestConfigPullMergesSecretsEditedOnBothSides(t *testing.T) {
	m := newMergeCase(t, mergeBase, mergeOurs, mergeTheirs, "")
	out, err := m.pull(t, false)
	if err != nil {
		t.Fatal(problemText(err))
	}
	if !strings.Contains(out, "--execute will write it") {
		t.Errorf("the dry run does not say what --execute does:\n%s", out)
	}
	for _, want := range []string{"secrets: external.a", "secrets: external.b"} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run does not name %s:\n%s", want, out)
		}
	}
	noValues(t, out)
	m.unchanged(t)

	out, err = m.pull(t, true)
	if err != nil {
		t.Fatal(problemText(err))
	}
	noValues(t, out)
	s := m.secrets(t)
	if a, _ := s.Get("external.a"); a != "ours-a" {
		t.Errorf("external.a %q", a)
	}
	if b, _ := s.Get("external.b"); b != "theirs-b" {
		t.Errorf("external.b %q", b)
	}
	m.mergedInto(t)
	info, err := os.Stat(filepath.Join(m.l.dir, "secrets.enc.yaml"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("secrets file %v %v", info, err)
	}
	noFolderState(t, m.l.dir)
}

func TestConfigPullMergeTakesTheHeadsOtherFiles(t *testing.T) {
	m := newMergeCase(t, mergeBase, mergeOurs, mergeTheirs, "# edited at the head\n")
	if _, err := m.pull(t, true); err != nil {
		t.Fatal(problemText(err))
	}
	if got := string(must(os.ReadFile(m.cfg))); !strings.HasSuffix(got, "# edited at the head\n") {
		t.Error("paisans-stack.yaml is not the head's")
	}
}

// A key both sides changed differently is a conflict named by its key
// only: the draft keeps its value there, takes the rest of the merge, and
// is drafted from the head; a dry run writes nothing.
func TestPullSecretsConflictNamesKeysOnly(t *testing.T) {
	ours := []string{"external.a", "ours-a", "external.b", "ours-b"}
	m := newMergeCase(t, mergeBase, ours, mergeTheirs, "")
	for _, execute := range []bool{false, true} {
		out, err := m.pull(t, execute)
		if err == nil {
			t.Fatal("a conflict was not reported")
		}
		text := problemText(err)
		shown := out + err.Error() + text
		if !strings.Contains(text, "secrets: external.b") || !strings.Contains(text, "paisans secrets set") {
			t.Errorf("the refusal does not name the key and the command:\n%s", shown)
		}
		if strings.Contains(text, "external.a") {
			t.Errorf("a key that merges is named as a conflict:\n%s", text)
		}
		noValues(t, shown)
		if !execute {
			m.unchanged(t)
		}
	}
	s := m.secrets(t)
	if a, _ := s.Get("external.a"); a != "ours-a" {
		t.Errorf("external.a %q", a)
	}
	if b, _ := s.Get("external.b"); b != "ours-b" {
		t.Errorf("the conflict did not keep the draft's external.b: %q", b)
	}
	m.mergedInto(t)
}

// Both sides edited paisans-stack.yaml and the secrets: both merge.
func TestConfigPullMergesYAMLAndSecretsEditedOnBothSides(t *testing.T) {
	m := newMergeCase(t, mergeBase, mergeOurs, mergeTheirs, "# head\n")
	local := m.files
	local.Paisans = []byte(strings.Replace(string(local.Paisans), "name: Bob Example", "name: Bob Draft", 1))
	if err := configstore.WriteFilesTo(m.l.dir, local); err != nil {
		t.Fatal(err)
	}
	if _, err := m.pull(t, true); err != nil {
		t.Fatal(problemText(err))
	}
	if got := string(must(os.ReadFile(m.cfg))); !strings.Contains(got, "name: Bob Draft") {
		t.Errorf("the draft's edit is gone:\n%s", got)
	}
	if b, _ := m.secrets(t).Get("external.b"); b != "theirs-b" {
		t.Errorf("external.b %q", b)
	}
	m.mergedInto(t)
}

// A head whose .sops.yaml names no age key would have the merge written in
// plaintext: refused before anything is written.
func TestConfigPullMergeRefusesNoRecipients(t *testing.T) {
	m := newMergeCaseSOPS(t, mergeBase, mergeOurs, mergeTheirs, "", "creation_rules: []\n")
	out, err := m.pull(t, true)
	if err == nil || !strings.Contains(problemText(err), ".sops.yaml") {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	m.unchanged(t)
}

// An unreadable secrets document is refused without the reason, which can
// quote a value.
func TestConfigPullUnreadableSecretsNeverQuotesThem(t *testing.T) {
	m := newMergeCase(t, mergeBase, mergeOurs, mergeTheirs, "")
	bad := m.files
	bad.Secrets = []byte("version: 1\nexternal: {a: SENTINELVALUE\n  b: [SENTINELVALUE\n")
	if err := configstore.WriteFilesTo(m.l.dir, bad); err != nil {
		t.Fatal(err)
	}
	out, err := m.pull(t, true)
	if err == nil {
		t.Fatal("pulled")
	}
	var p *ui.Problem
	if errors.As(err, &p) && p.Cause != nil {
		t.Errorf("the problem carries a cause: %v", p.Cause)
	}
	if shown := out + err.Error() + problemText(err); strings.Contains(shown, "SENTINELVALUE") {
		t.Errorf("a value appears in:\n%s", shown)
	}
}

// A rekey under way on either side refuses the merge before anything is
// written, and says what finishes it.
func TestConfigPullMergeRefusesWhileARekeyIsUnderWay(t *testing.T) {
	ours := append([]string{"sites.home-a.wireguard_next_private_key", "next-sentinel"}, mergeOurs...)
	m := newMergeCase(t, mergeBase, ours, mergeTheirs, "")
	for _, execute := range []bool{false, true} {
		out, err := m.pull(t, execute)
		if err == nil {
			t.Fatal("pulled")
		}
		shown := out + err.Error() + problemText(err)
		if !strings.Contains(shown, "paisans mesh rekey") || !strings.Contains(shown, "home-a") {
			t.Errorf("the refusal does not name the rekey:\n%s", shown)
		}
		noValues(t, shown)
		if strings.Contains(shown, "next-sentinel") {
			t.Errorf("the next key appears in:\n%s", shown)
		}
		m.unchanged(t)
	}
}

// A draft's file holding a pasted value with a line break in it is refused
// by that refusal, naming the key and the command that repairs it, not as a
// file that could not be decrypted.
func TestConfigPullMergeSaysWhyTheLocalSecretsAreRefused(t *testing.T) {
	ours := append(append([]string{}, mergeOurs...), "external.c", "bad\nc")
	m := newMergeCase(t, mergeBase, ours, mergeTheirs, "")
	_, err := m.pull(t, true)
	text := problemText(err)
	if err == nil || strings.Contains(text, "could not be decrypted") || !strings.Contains(text, "external.c") || !strings.Contains(text, "paisans secrets set external.c") {
		t.Errorf("got %q", text)
	}
	if strings.Contains(text, "bad") {
		t.Errorf("the refusal shows the value: %q", text)
	}
}

// A published version holding a pasted value with a line break in it is
// still read as the merge's base or head: the check that refuses it belongs
// to the draft's file, which the next command loads, and a version in the
// store cannot be repaired, so refusing it there would fail every pull.
func TestConfigPullMergeReadsAVersionWithABadPastedValue(t *testing.T) {
	// The repair: the value went bad in a published version, and the draft
	// set it again with `secrets set`.
	t.Run("base and head", func(t *testing.T) {
		bad := []string{"external.c", "bad\nc"}
		base := append(append([]string{}, mergeBase...), bad...)
		theirs := append(append([]string{}, mergeTheirs...), bad...)
		ours := append(append([]string{}, mergeOurs...), "external.c", "fixed-c")
		m := newMergeCase(t, base, ours, theirs, "")
		if _, err := m.pull(t, true); err != nil {
			t.Fatalf("the merge refused versions with a bad pasted value: %v", problemText(err))
		}
		s := m.secrets(t)
		for key, want := range map[string]string{"external.a": "ours-a", "external.b": "theirs-b", "external.c": "fixed-c"} {
			if got, _ := s.Get(key); got != want {
				t.Errorf("%s %q, want %q", key, got, want)
			}
		}
	})
	t.Run("head", func(t *testing.T) {
		theirs := []string{"external.a", "base-a", "external.b", "theirs\nb"}
		m := newMergeCase(t, mergeBase, mergeOurs, theirs, "")
		_, err := m.pull(t, true)
		if err != nil && strings.Contains(err.Error()+problemText(err), "could not be decrypted") {
			t.Fatalf("a head with a bad pasted value reads as undecryptable: %v", err)
		}
		id := must(config.LoadBytes(must(os.ReadFile(m.cfg)))).ID
		_, err = config.LoadDeploymentSecrets(filepath.Join(m.l.dir, "secrets.enc.yaml"), id)
		var p *ui.Problem
		if !errors.As(err, &p) || !strings.Contains(p.Hint, "external.b") {
			t.Errorf("the merged file's bad value is not refused by key on the next load: %v", err)
		}
	})
}
