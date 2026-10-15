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

// No command reads a web server through sudo: webserver.Host's Sudo runner
// is set only by the webserver package's own tests, which exercise the
// branches that use it. A Host literal or assignment setting Sudo anywhere
// else, tests included, is a command reaching for root.
func TestNoCommandReadsTheWebServerThroughSudo(t *testing.T) {
	own := filepath.Join("internal", "webserver") + string(filepath.Separator)
	for _, root := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel := strings.TrimPrefix(path, "../../")
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			test := strings.HasSuffix(path, "_test.go")
			if test && strings.HasPrefix(rel, own) {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			inWebserver := file.Name.Name == "webserver"
			importsWebserver := false
			for _, imp := range file.Imports {
				if strings.HasSuffix(strings.Trim(imp.Path.Value, `"`), "/internal/webserver") {
					importsWebserver = true
				}
			}
			ast.Inspect(file, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CompositeLit:
					if !isWebServerHost(n.Type, inWebserver) {
						return true
					}
					for _, e := range n.Elts {
						if kv, ok := e.(*ast.KeyValueExpr); ok {
							if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Sudo" {
								t.Errorf("%s sets Sudo on a webserver.Host", rel)
							}
						}
					}
				case *ast.AssignStmt:
					// Without type information an assignment's receiver is
					// not known, so .Sudo is refused wherever the file can
					// hold a webserver.Host: its package, or one importing it.
					if !inWebserver && !importsWebserver {
						return true
					}
					for _, lhs := range n.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sudo" {
							t.Errorf("%s assigns .Sudo where a webserver.Host can be", rel)
						}
					}
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

func isWebServerHost(expr ast.Expr, inWebserver bool) bool {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		x, ok := e.X.(*ast.Ident)
		return ok && x.Name == "webserver" && e.Sel.Name == "Host"
	case *ast.Ident:
		return inWebserver && e.Name == "Host"
	}
	return false
}
