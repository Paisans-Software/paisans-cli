package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/create"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

const (
	createKey    = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice@example.org"
	createBobKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB bob@example.org"
)

// answersFile is a terminal's worth of answers, one per line, as a file
// create reads from with isTerminal saying it is a terminal. HOME is an empty
// directory, so no key of the machine running the tests is offered.
func answersFile(t *testing.T, lines ...string) *os.File {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "answers")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	saved := isTerminal
	isTerminal = func(*os.File) bool { return true }
	t.Cleanup(func() { isTerminal = saved })
	return f
}

func plainReporter(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	reporterOverride = ui.NewPlain(&b, false)
	t.Cleanup(func() { reporterOverride = nil })
	return &b
}

var (
	createCommunity = []string{"Example Community", "example.org", ""}
	createAdmin     = []string{"alice", "Alice Example", "alice@example.org", createKey, "", "",
		"bob", "Bob Example", "bob@example.org", createBobKey, "", "n"}
	// sign in; the account service manages accounts, on its default
	// hostname, with open signup and the code of conduct the wizard's own
	// tests read; then no forums, wiki, blog or health page.
	createNoApps = []string{"", "", "", "2", "../../internal/create/testdata/code-of-conduct.md", "n", "n", "n", "n"}
	createMail   = []string{"smtp.example.org", "", "", "community@example.org", "", ""}
)

func createLines(groups ...[]string) []string {
	var out []string
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

func TestCreateWritesAFileThatValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	plainReporter(t)
	in := answersFile(t, createLines(createCommunity, createAdmin,
		[]string{"1", "", "203.0.113.10", "", ""}, createNoApps, createMail)...)
	var out bytes.Buffer
	if err := runCreate([]string{"--config", path}, in, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	// The file goes to disk only: the terminal shows neither its contents
	// nor a question about writing it.
	for _, unwanted := range []string{"version:", "Write "} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("output holds %q:\n%s", unwanted, out.String())
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if validate.Check(cfg).Refused() {
		t.Error("the written file is refused")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
		t.Errorf("mode %v, want 0644: the file holds no secret and is meant to be committed", info.Mode().Perm())
	}
	// init creates secrets.enc.yaml and the id a site request carries, so it
	// comes first; each host is admitted before apply reaches it; secrets set
	// with no key asks for each secret only the operator can give.
	next := out.String()[strings.Index(out.String(), "Next:"):]
	at := -1
	for _, want := range []string{"  paisans init ", "  paisans site request ", "  paisans secrets set ", "  paisans deployment apply "} {
		i := strings.Index(next, want)
		if i < 0 {
			t.Errorf("next steps lack %q:\n%s", want, next)
			continue
		}
		if i < at {
			t.Errorf("%q is out of order:\n%s", want, next)
		}
		at = i
	}
	if strings.Contains(next, "external.") {
		t.Errorf("next steps name a secret's key rather than asking for each:\n%s", next)
	}
}

func TestCreateOffersTheKeysInHome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	plainReporter(t)
	// alice takes the key found rather than pasting one.
	in := answersFile(t, createLines(createCommunity,
		[]string{"alice", "Alice Example", "alice@example.org", "",
			"", "bob", "Bob Example", "bob@example.org", createBobKey, "", "n"},
		[]string{"1", "", "203.0.113.10", "", ""}, createNoApps, createMail)...)
	ssh := filepath.Join(os.Getenv("HOME"), ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ssh, "id_ed25519.pub"), []byte(createKey+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runCreate([]string{"--config", path}, in, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Use ~/.ssh/id_ed25519.pub (ssh-ed25519 SHA256:") {
		t.Errorf("the key in ~/.ssh is not offered:\n%s", out.String())
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(cfg.Operators["alice"].Keys); got != createKey {
		t.Errorf("alice's keys = %q, want the one in ~/.ssh", got)
	}
}

// A custom shape is the one place validate judges the roles: data and a
// witness on one machine is refused, the machines are asked again, and the
// corrected answers write the file.
func TestCreateAsksAgainAfterARefusal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	rec := plainReporter(t)
	in := answersFile(t, createLines(createCommunity, createAdmin,
		[]string{"4", "1", "", "data, apps, gateway, witness", "203.0.113.10", "", "", ""},
		createNoApps, createMail,
		[]string{""}, // answer the machines again
		[]string{"1", "", "203.0.113.10", "", ""},
	)...)
	var out bytes.Buffer
	if err := runCreate([]string{"--config", path}, in, &out); err != nil {
		t.Fatalf("%v\n%s\n%s", err, rec.String(), out.String())
	}
	if !strings.Contains(rec.String(), "witness") || !strings.Contains(out.String(), "answer the machines again") {
		t.Errorf("the refusal or the way back to the machines was not shown:\n%s\n%s", rec.String(), out.String())
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Sites["example-community"]; !ok {
		t.Errorf("the corrected machines were not written: %v", cfg.SiteNames())
	}
}

func TestCreateRefusesAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	os.WriteFile(path, []byte("version: 1\n"), 0o644)
	in := answersFile(t, "x")
	err := runCreate([]string{"--config", path}, in, &bytes.Buffer{})
	if hint, _ := ui.Describe(err); !strings.Contains(hint, "already exists") {
		t.Errorf("err = %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "version: 1\n" {
		t.Error("the existing file was changed")
	}
}

func TestCreateRefusesStdinThatIsNotATerminal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	in := answersFile(t, "x")
	isTerminal = func(*os.File) bool { return false }
	err := runCreate([]string{"--config", path}, in, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Errorf("err = %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a file was written")
	}
}

// Quitting after a refusal is the operator's choice, not a failure, and
// writes nothing.
func TestCreateWritesNothingOnQuit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	rec := plainReporter(t)
	in := answersFile(t, createLines(createCommunity, createAdmin,
		[]string{"4", "1", "", "data, apps, gateway, witness", "203.0.113.10", "", "", ""},
		createNoApps, createMail,
		[]string{"4"}, // quit without writing anything
	)...)
	var out bytes.Buffer
	if err := runCreate([]string{"--config", path}, in, &out); err != nil {
		t.Errorf("err = %v", err)
	}
	if want := "paisans-stack.yaml not written. Run paisans create again to start over."; !strings.Contains(rec.String(), want) {
		t.Errorf("missing %q:\n%s", want, rec.String())
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a file was written")
	}
}

func TestCreateWritesNothingWhenInputEnds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	in := answersFile(t, createCommunity...)
	err := runCreate([]string{"--config", path}, in, &bytes.Buffer{})
	if hint, _ := ui.Describe(err); !strings.Contains(hint, "nothing written") {
		t.Errorf("err = %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a file was written")
	}
}

func TestWriteNewRefusesAFileThatAppeared(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	os.WriteFile(path, []byte("theirs\n"), 0o644)
	if err := writeNew(path, []byte("ours\n")); err == nil {
		t.Error("a file that appeared meanwhile was replaced")
	}
	if data, _ := os.ReadFile(path); string(data) != "theirs\n" {
		t.Error("the file that appeared was changed")
	}
}

// A refused file is still written when the operator asks for it as it
// stands, so that their answers are not lost.
func TestCreateWritesARefusedFileOnRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	rec := plainReporter(t)
	in := answersFile(t, createLines(createCommunity, createAdmin,
		[]string{"4", "1", "", "data, apps, gateway, witness", "203.0.113.10", "", "", ""},
		createNoApps, createMail,
		[]string{"3"}, // write the file as it stands
	)...)
	var out bytes.Buffer
	if err := runCreate([]string{"--config", path}, in, &out); err != nil {
		t.Fatalf("%v\n%s\n%s", err, rec.String(), out.String())
	}
	if !strings.Contains(rec.String(), "which validate refuses") {
		t.Errorf("the write did not say the file is refused:\n%s", rec.String())
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !validate.Check(cfg).Refused() {
		t.Error("the file written as it stands is not the refused one")
	}
}

// A file the loader refuses is shown as refusals, each under its key, rather
// than ending create with nothing written.
func TestCheckRenderedTurnsALoadErrorIntoRefusals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	plainReporter(t)
	in := answersFile(t, createLines(createCommunity, createAdmin,
		[]string{"1", "", "203.0.113.10", "", ""}, createNoApps, createMail)...)
	if err := runCreate([]string{"--config", path}, in, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	shared := strings.Replace(string(data), strings.Fields(createBobKey)[1], strings.Fields(createKey)[1], 1)
	result, err := checkRendered([]byte(shared))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Refused() || create.SectionFor(result.Refusals()[0].Key) != create.SectionOperators {
		t.Errorf("the shared key is not a refusal under the operators: %+v", result.Findings)
	}
}
