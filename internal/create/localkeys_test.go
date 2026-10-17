package create

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"golang.org/x/crypto/ssh"
)

func TestFindLocalKeys(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cert := &ssh.Certificate{Key: signer.PublicKey(), CertType: ssh.UserCert}
	if err := cert.SignCert(rand.Reader, signer); err != nil {
		t.Fatal(err)
	}
	dir := fstest.MapFS{
		"id_ed25519.pub":      {Data: []byte(aliceKey + "\n")},
		"id_ed25519":          {Data: []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n")},
		"work.pub":            {Data: []byte("# the work laptop\n\n" + bobKey + "\n")},
		"copy.pub":            {Data: []byte(strings.Replace(aliceKey, "alice@example.org", "a copy", 1) + "\n")},
		"id_ed25519-cert.pub": {Data: ssh.MarshalAuthorizedKey(cert)},
		"broken.pub":          {Data: []byte("not a key\n")},
		"empty.pub":           {},
		"dir.pub":             {Mode: 0o755 | 0o20000000000},
	}
	keys := FindLocalKeys(dir, "~/.ssh")
	var names, lines []string
	for _, k := range keys {
		names = append(names, k.Name)
		lines = append(lines, k.Line)
	}
	// copy.pub sorts first, so it is the one of the two copies kept.
	if want := []string{"~/.ssh/copy.pub", "~/.ssh/work.pub"}; !slices.Equal(names, want) {
		t.Errorf("names = %q, want %q", names, want)
	}
	if len(lines) == 2 && lines[1] != bobKey {
		t.Errorf("work.pub's line = %q, want %q", lines[1], bobKey)
	}
	if len(keys) == 2 && (!strings.HasPrefix(keys[1].About, "ssh-ed25519 SHA256:") || !strings.HasSuffix(keys[1].About, " bob@example.org")) {
		t.Errorf("About = %q, want its type, fingerprint and comment", keys[1].About)
	}
}

// carolKey is a third operator's key, for when alice takes both of the others.
const carolKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgIC carol@example.org"

func TestTheFirstOperatorIsOfferedTheKeysOnThisMachine(t *testing.T) {
	local := []LocalKey{
		{Name: "~/.ssh/id_ed25519.pub", Line: aliceKey, About: "ssh-ed25519 SHA256:a alice@example.org"},
		{Name: "~/.ssh/work.pub", Line: bobKey, About: "ssh-ed25519 SHA256:b bob@example.org"},
	}
	for _, tc := range []struct {
		name  string
		lines []string
		want  []string
	}{
		{"both taken", []string{"alice", "Alice Example", "alice@example.org", "", "",
			"bob", "Bob Example", "bob@example.org", carolKey, "", "n"}, []string{aliceKey, bobKey}},
		{"one taken", []string{"alice", "Alice Example", "alice@example.org", "y", "n",
			"bob", "Bob Example", "bob@example.org", bobKey, "", "n"}, []string{aliceKey}},
		{"none taken, so pasted", []string{"alice", "Alice Example", "alice@example.org", "n", "n", bobKey, "",
			"bob", "Bob Example", "bob@example.org", aliceKey, "", "n"}, []string{bobKey}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			w := NewWizard(NewPrompter(strings.NewReader(script(tc.lines...)), &out))
			w.LocalKeys = local
			if err := w.Ask(SectionOperators); err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			ops := w.Answers.Operators
			if len(ops) != 2 {
				t.Fatalf("%d operators, want 2\n%s", len(ops), out.String())
			}
			if !slices.Equal(ops[0].Keys, tc.want) {
				t.Errorf("first operator's keys = %q, want %q", ops[0].Keys, tc.want)
			}
			if !strings.Contains(out.String(), "Use ~/.ssh/work.pub (ssh-ed25519 SHA256:b bob@example.org)? ") {
				t.Errorf("the key is not offered by file and fingerprint:\n%s", out.String())
			}
			if strings.Count(out.String(), "Use ~/.ssh/") != 2 {
				t.Errorf("the keys are offered to more than the first operator:\n%s", out.String())
			}
		})
	}
}

func TestKeysGivenAreKeptOverTheKeysOnThisMachine(t *testing.T) {
	var out strings.Builder
	w := NewWizard(NewPrompter(strings.NewReader(strings.Repeat("\n", 20)), &out))
	w.LocalKeys = []LocalKey{{Name: "~/.ssh/work.pub", Line: bobKey}}
	w.Answers.Operators = []Operator{
		{ID: "alice", Name: "Alice Example", Email: "alice@example.org", Keys: []string{aliceKey}},
		{ID: "bob", Name: "Bob Example", Email: "bob@example.org", Keys: []string{bobKey}},
	}
	if err := w.Ask(SectionOperators); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if got := w.Answers.Operators[0].Keys; !slices.Equal(got, []string{aliceKey}) {
		t.Errorf("first operator's keys = %q, want the ones given", got)
	}
	if strings.Contains(out.String(), "Use ~/.ssh/") {
		t.Errorf("keys already given were offered again:\n%s", out.String())
	}
}

// A key on this machine the loader would refuse is not offered.
func TestARefusedKeyOnThisMachineIsNotOffered(t *testing.T) {
	small, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(&small.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	weak := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	var out strings.Builder
	w := NewWizard(NewPrompter(strings.NewReader(script(
		"alice", "Alice Example", "alice@example.org", "",
		"bob", "Bob Example", "bob@example.org", bobKey, "", "n",
	)), &out))
	w.LocalKeys = []LocalKey{{Name: "~/.ssh/old.pub", Line: weak}, {Name: "~/.ssh/work.pub", Line: aliceKey}}
	if err := w.Ask(SectionOperators); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "old.pub") {
		t.Errorf("a key the loader refuses was offered:\n%s", out.String())
	}
	if got := w.Answers.Operators[0].Keys; !slices.Equal(got, []string{aliceKey}) {
		t.Errorf("first operator's keys = %q, want the one offered", got)
	}
}
