package apply_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// meshCommand matches a string that starts a wg, wg-quick, journalctl or
// systemctl command, at its start or after a shell separator, with or
// without sudo in front.
var meshCommand = regexp.MustCompile(`(^|[;&|]\s*)(sudo\s+(-n\s+)?)?(wg|wg-quick|journalctl|systemctl)\s`)

// Every command that reaches the mesh interface goes through the WireGuard
// helper, because it is the one thing the deployment's user may run as
// root: one written directly fails on a host, without sudo. This walks the
// packages that touch the mesh and refuses a string literal that starts
// such a command. A systemctl --user command is the user's own and allowed.
func TestTheMeshIsReachedOnlyThroughTheHelper(t *testing.T) {
	exempt := map[string]string{
		filepath.Join("..", "siteremove", "host.go"): "site remove's host step stops the removed site's own units and interface, which no helper verb does",
	}
	for _, dir := range []string{".", "../meshkey", "../meshrekey", "../siteadd", "../siteremove", "../doctor"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatalf("%s holds no Go file; the walk would pass on nothing", dir)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") || exempt[file] != "" {
				continue
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err == nil && meshCommand.MatchString(s) && !strings.Contains(s, "--user") {
					t.Errorf("%s: %q runs a mesh command directly; go through apply.WGHelper", fset.Position(lit.Pos()), s)
				}
				return true
			})
		}
	}
}
