package hostadmit_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
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

// The local transport runs a plan against the machine the CLI runs on, so
// it is reached only through host admit and host evict with --request,
// which run on the host itself. Neither exists yet, so nothing outside this
// package calls Local.
func TestOnlyHostAdmitReachesTheLocalTransport(t *testing.T) {
	allowed := map[string]bool{}
	for _, root := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "/internal/hostadmit/") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Local" {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "hostadmit" && !allowed[strings.TrimPrefix(path, "../../")] {
					t.Errorf("%s calls hostadmit.Local", strings.TrimPrefix(path, "../../"))
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
