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

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/configstore"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// mergeCase is a store of two versions of the fixture whose secrets differ,
// and a workstation based on the first whose own secrets differ again.
type mergeCase struct {
	l     configstore.Local
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
	b := configstore.Files{Paisans: []byte(yaml), Secrets: enc(base), SOPS: []byte(sops)}
	h := configstore.Files{Paisans: []byte(yaml + headYAML), Secrets: enc(theirs), SOPS: []byte(sops)}
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
	local := configstore.Files{Paisans: b.Paisans, Secrets: enc(ours), SOPS: b.SOPS}
	l := pullWorkstation(t, s.answer(t), sha0, sha0, local)
	return mergeCase{l: l, cfg: filepath.Join(l.Dir, "paisans.yaml"), shas: shas, sops: sops, files: local}
}

func (m mergeCase) pull(t *testing.T, execute bool) (string, error) {
	t.Helper()
	args := []string{"pull", "--config", m.cfg}
	if execute {
		args = append(args, "--execute")
	}
	var out bytes.Buffer
	err := runConfig(args, noTerminalStdin(), &out)
	return out.String(), err
}

func (m mergeCase) secrets(t *testing.T) *config.Secrets {
	t.Helper()
	id := must(config.LoadBytes(must(os.ReadFile(m.cfg)))).ID
	return must(config.LoadDeploymentSecrets(filepath.Join(m.l.Dir, "secrets.enc.yaml"), id))
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
		t.Fatal(err)
	}
	for _, want := range []string{"external.a", "external.b"} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run does not name %s:\n%s", want, out)
		}
	}
	noValues(t, out)
	if got, _ := m.l.Files(); !got.Equal(m.files) {
		t.Fatal("a dry run wrote files")
	}
	if h, _ := m.l.Head(); h != m.shas[0] {
		t.Fatalf("a dry run moved the head to %s", h)
	}

	out, err = m.pull(t, true)
	if err != nil {
		t.Fatal(err)
	}
	noValues(t, out)
	s := m.secrets(t)
	if a, _ := s.Get("external.a"); a != "ours-a" {
		t.Errorf("external.a %q", a)
	}
	if b, _ := s.Get("external.b"); b != "theirs-b" {
		t.Errorf("external.b %q", b)
	}
	if h, _ := m.l.Head(); h != m.shas[1] {
		t.Fatalf("head %s, want %s", h, m.shas[1])
	}
	info, err := os.Stat(filepath.Join(m.l.Dir, "secrets.enc.yaml"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("secrets file %v %v", info, err)
	}
	if _, err := os.Stat(m.l.Incoming()); err == nil {
		t.Error("a merge wrote the incoming directory")
	}
}

func TestConfigPullMergeTakesTheHeadsOtherFiles(t *testing.T) {
	m := newMergeCase(t, mergeBase, mergeOurs, mergeTheirs, "# edited at the head\n")
	if _, err := m.pull(t, true); err != nil {
		t.Fatal(err)
	}
	if got := string(must(os.ReadFile(m.cfg))); !strings.HasSuffix(got, "# edited at the head\n") {
		t.Error("paisans.yaml is not the head's")
	}
}

func TestConfigPullSecretsConflictRefusesNamingPathsOnly(t *testing.T) {
	ours := []string{"external.a", "ours-a", "external.b", "ours-b"}
	m := newMergeCase(t, mergeBase, ours, mergeTheirs, "")
	for _, execute := range []bool{false, true} {
		out, err := m.pull(t, execute)
		if err == nil {
			t.Fatal("a conflict pulled")
		}
		shown := out + err.Error() + problemText(err)
		if !strings.Contains(shown, "external.b") || !strings.Contains(shown, "paisans secrets set") {
			t.Errorf("the refusal does not name the key and the command:\n%s", shown)
		}
		if strings.Contains(shown, "pocket_id_groups") {
			t.Errorf("advice for a section no conflict is in:\n%s", shown)
		}
		if strings.Contains(shown, "external.a") {
			t.Errorf("a key that merges is named as a conflict:\n%s", shown)
		}
		noValues(t, shown)
		if got, _ := m.l.Files(); !got.Equal(m.files) {
			t.Fatal("a conflict changed the files")
		}
		if h, _ := m.l.Head(); h != m.shas[0] {
			t.Fatalf("a conflict moved the head to %s", h)
		}
	}
}

// Both sides edited paisans.yaml: pull does not try to merge the secrets.
func TestConfigPullEditedYAMLOnBothSidesStillRefuses(t *testing.T) {
	m := newMergeCase(t, mergeBase, mergeOurs, mergeTheirs, "# head\n")
	local := m.files
	local.Paisans = append(append([]byte{}, local.Paisans...), "# mine\n"...)
	if err := m.l.WriteFiles(local); err != nil {
		t.Fatal(err)
	}
	_, err := m.pull(t, true)
	if err == nil || !strings.Contains(problemText(err), "--merge") {
		t.Fatalf("pull: %v", err)
	}
	if got, _ := m.l.Files(); !got.Equal(local) {
		t.Fatal("the local files were overwritten")
	}
}

// A head whose .sops.yaml names no age key would have the merge written in
// plaintext: refused before anything is written.
func TestConfigPullMergeRefusesNoRecipients(t *testing.T) {
	m := newMergeCaseSOPS(t, mergeBase, mergeOurs, mergeTheirs, "", "creation_rules: []\n")
	out, err := m.pull(t, true)
	if err == nil || !strings.Contains(problemText(err), ".sops.yaml") {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	if got, _ := m.l.Files(); !got.Equal(m.files) {
		t.Fatal("files changed")
	}
	if h, _ := m.l.Head(); h != m.shas[0] {
		t.Fatalf("head moved to %s", h)
	}
}

// An unreadable secrets document is refused without the reason, which can
// quote a value.
func TestConfigPullUnreadableSecretsNeverQuotesThem(t *testing.T) {
	m := newMergeCase(t, mergeBase, mergeOurs, mergeTheirs, "")
	bad := m.files
	bad.Secrets = []byte("version: 1\nexternal: {a: SENTINELVALUE\n  b: [SENTINELVALUE\n")
	if err := m.l.WriteFiles(bad); err != nil {
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
