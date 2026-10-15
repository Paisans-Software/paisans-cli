package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// ownerSudo is every function allowed to give a transport sudo: the
// owner's own commands. Every command that acts on a deployment logs in as
// the deployment's user, which may run nothing through sudo but the
// WireGuard helper.
var ownerSudo = map[string]bool{
	"cmd/paisans/hostdeployments.go ownerTransport": true,
}

// Only the owner's transport runs through sudo. A Sudo or Auth field set on
// an SSHTransport anywhere else in the CLI's code is a deployment command
// reaching for root.
func TestOnlyTheOwnersTransportRunsThroughSudo(t *testing.T) {
	for _, root := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			rel := strings.TrimPrefix(path, "../../")
			for _, decl := range file.Decls {
				where := rel + " " + declName(decl)
				ast.Inspect(decl, func(n ast.Node) bool {
					switch n := n.(type) {
					case *ast.CompositeLit:
						if !isSSHTransport(n.Type) {
							return true
						}
						for _, e := range n.Elts {
							if kv, ok := e.(*ast.KeyValueExpr); ok {
								if key, ok := kv.Key.(*ast.Ident); ok && (key.Name == "Sudo" || key.Name == "Auth") && !ownerSudo[where] {
									t.Errorf("%s sets %s on an SSHTransport", where, key.Name)
								}
							}
						}
					case *ast.AssignStmt:
						for _, lhs := range n.Lhs {
							if sel, ok := lhs.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Sudo" || sel.Sel.Name == "Auth") && !ownerSudo[where] {
								t.Errorf("%s assigns .%s", where, sel.Sel.Name)
							}
						}
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// declName is a declaration's name: a function's, or the first name a var
// declares, which is how this package names a replaceable function.
func declName(decl ast.Decl) string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return d.Name.Name
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			if v, ok := spec.(*ast.ValueSpec); ok && len(v.Names) > 0 {
				return v.Names[0].Name
			}
		}
	}
	return ""
}

func isSSHTransport(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		return e.Sel.Name == "SSHTransport"
	case *ast.Ident:
		return e.Name == "SSHTransport"
	}
	return false
}
