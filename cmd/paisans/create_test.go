package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

const createKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice@example.org"

// answersFile is a terminal's worth of answers, one per line, as a file
// create reads from with isTerminal saying it is a terminal.
func answersFile(t *testing.T, lines ...string) *os.File {
	t.Helper()
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
	createAdmin     = []string{"alice", "Alice Example", "alice@example.org", createKey, "", "n"}
	// sign in, then no forums, wiki, blog or status page.
	createNoApps = []string{"", "n", "n", "n", "n"}
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
		[]string{"1", "", "203.0.113.10", "", ""}, createNoApps, createMail, []string{""})...)
	var out bytes.Buffer
	if err := runCreate([]string{"--config", path}, in, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
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
	for _, want := range []string{"paisans secrets set external.smtp_password", "paisans init", "paisans apply"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("next steps lack %q", want)
		}
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
		[]string{""}, // write it
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
	if _, ok := cfg.Sites["server"]; !ok {
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

func TestCreateWritesNothingOnNo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	plainReporter(t)
	in := answersFile(t, createLines(createCommunity, createAdmin,
		[]string{"1", "", "203.0.113.10", "", ""}, createNoApps, createMail, []string{"n"})...)
	err := runCreate([]string{"--config", path}, in, &bytes.Buffer{})
	if hint, _ := ui.Describe(err); !strings.Contains(hint, "nothing written") {
		t.Errorf("err = %v", err)
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
