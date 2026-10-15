package hostadmit_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

func TestLocalRunsCommandsWithSh(t *testing.T) {
	l := hostadmit.Local()
	out, err := l.Run("printf '%s' \"$((1 + 2))\"")
	if err != nil || out != "3" {
		t.Fatalf("Run = %q, %v", out, err)
	}
	out, err = l.Run("echo partial; exit 3")
	if err == nil || out != "partial\n" || !strings.Contains(err.Error(), "echo partial; exit 3") || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("a failing command: %q, %v", out, err)
	}
	out, err = l.RunInput("cat", "from stdin")
	if err != nil || out != "from stdin" {
		t.Errorf("RunInput = %q, %v", out, err)
	}
	if d := l.Describe(); !strings.HasPrefix(d, "this machine") {
		t.Errorf("Describe() = %q", d)
	}
}

// A write creates the parent directories, sets the mode and replaces the
// file whole, leaving no temporary file beside it; a missing file, and a
// directory, read as absent.
func TestLocalReadsAndWritesFiles(t *testing.T) {
	l := hostadmit.Local()
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "conf")
	if err := l.WriteFile(path, "one\n", 0o640); err != nil {
		t.Fatal(err)
	}
	if err := l.WriteFile(path, "two\n", 0o600); err != nil {
		t.Fatal(err)
	}
	content, found, err := l.ReadFile(path)
	if err != nil || !found || content != "two\n" {
		t.Fatalf("ReadFile = %q, %v, %v", content, found, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, %v", info.Mode().Perm(), err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("left beside the file: %v", entries)
	}
	for _, absent := range []string{filepath.Join(dir, "missing"), filepath.Join(dir, "a")} {
		if _, found, err := l.ReadFile(absent); found || err != nil {
			t.Errorf("%s: found %v, %v", absent, found, err)
		}
	}
}

// A write goes through a fresh, randomly named temporary file opened
// exclusively in the target's directory, so a symlink planted at a
// predictable name by the directory's owner is never followed.
func TestLocalWriteDoesNotFollowAPlantedTemporaryName(t *testing.T) {
	l := hostadmit.Local()
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "conf")
	for _, name := range []string{"conf.paisans-tmp", ".conf.paisans-tmp"} {
		if err := os.Symlink(victim, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.WriteFile(path, "new\n", 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "precious" {
		t.Errorf("the planted link's target was written: %q", got)
	}
	if info, _ := os.Stat(victim); info.Mode().Perm() != 0o644 {
		t.Errorf("the planted link's target was chmoded: %v", info.Mode().Perm())
	}
	got, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(got) != "new\n" || info.Mode().Perm() != 0o600 {
		t.Errorf("target = %q, %v", got, info.Mode())
	}
}

// Setuid, setgid and sticky survive, as chmod over ssh keeps them.
func TestLocalWriteKeepsSpecialModeBits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bin")
	if err := hostadmit.Local().WriteFile(path, "x", 0o4755); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode()&os.ModeSetuid == 0 || info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", info.Mode())
	}
}

// A secret in the input that the command echoes never reaches the error.
func TestLocalErrorsAreRedacted(t *testing.T) {
	ui.Secret("hunter2-local-secret")
	_, err := hostadmit.Local().RunInput("cat; exit 1", "pw=hunter2-local-secret")
	if err == nil || strings.Contains(err.Error(), "hunter2-local-secret") {
		t.Errorf("error = %v", err)
	}
}

// Commands start in the running user's home, as they do over ssh.
func TestLocalCommandsStartInTheHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want, _ := filepath.EvalSymlinks(home)
	out, err := hostadmit.Local().Run("pwd -P")
	if err != nil || strings.TrimSpace(out) != want {
		t.Errorf("pwd = %q, %v; want %q", out, err, want)
	}
}

// A path through a regular file reads as absent, as `[ -f ]` does.
func TestLocalReadThroughAFileIsAbsent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	os.WriteFile(file, []byte("x"), 0o600)
	if _, found, err := hostadmit.Local().ReadFile(filepath.Join(file, "child")); found || err != nil {
		t.Errorf("found %v, %v", found, err)
	}
}

// The local transport runs a plan against the machine the CLI runs on, so
// it is reached only through host admit and host evict with --request,
// which run on the host itself. Neither exists yet, so no file outside this
// package imports it, tests included, under any name.
func TestOnlyHostAdmitReachesTheLocalTransport(t *testing.T) {
	const importPath = "github.com/paisans-software/paisans-cli/internal/hostadmit"
	allowed := map[string]bool{}
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("no go.mod above the test")
		}
		root = parent
	}
	own := filepath.Join(root, "internal", "hostadmit")
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".worktrees", ".superpowers", "vendor", "testdata":
				return filepath.SkipDir
			}
			if path == own {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, imp := range file.Imports {
			if strings.Trim(imp.Path.Value, "\"`") == importPath && !allowed[rel] {
				t.Errorf("%s imports the hostadmit package", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
