package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// loopbackLiteral matches a string that names the host's loopback: any 127/8
// address, localhost, or ::1, but not an IPv6 address that merely ends in ::1.
var loopbackLiteral = regexp.MustCompile(`\b127\.\d{1,3}\.\d{1,3}\.\d{1,3}\b|(?i:\blocalhost\b)|(^|[^0-9A-Fa-f:])::1($|[^0-9A-Fa-f:])`)

// loopbackAllowed is every declaration whose code may name loopback, and
// why. None of them binds or dials loopback as the deployment.
var loopbackAllowed = map[string]string{
	"internal/config/config.go structural":                "an example web_server.listen, which the owner's web server dials",
	"internal/config/mesh.go reservedMesh":                "ranges the mesh subnet may not overlap",
	"internal/hostadmit/ufw.go readUFWBefore":             "reads the host's own listeners, as root",
	"internal/hostadmit/ufw.go loopbackRule":              "reads the host's own NAT rules, as root",
	"internal/hostadmit/webserver.go caddyAdminURL":       "the owner's Caddy, which site admit probes as root",
	"internal/hostadmit/webserver.go caddyTCPAdmin":       "the owner's Caddy, which site admit probes as root",
	"internal/hostprep/snippets/egress.nft.tmpl":          "the egress table, which drops the deployment's loopback dials",
	"internal/hostprep/snippets/wg-helper.sh.tmpl":        "the WireGuard helper, which refuses a loopback endpoint",
	"internal/imagedigest/imagedigest.go locate":          "an image reference's registry name",
	"internal/smtpcheck/smtpcheck.go skipReason":          "a relay the workstation's check cannot reach",
	"internal/smtpcheck/smtpcheck.go converse":            "the name the check greets a relay with",
	"internal/smtpcheck/smtptest/smtptest.go Dial":        "a test server on the machine running the tests",
	"internal/validate/monitor.go webServer":              "web_server.listen, which the owner's web server dials",
	"internal/validate/smtp.go smtpPlaintextWithUsername": "what an app's mail library allows",
	"internal/webserver/check.go checkUpstream":           "web_server.listen, which the owner's web server dials",
	"internal/webserver/detect.go caddyAdmin":             "the owner's Caddy, which site admit probes as root",
	"internal/webserver/detect.go caddy":                  "the owner's Caddy, which site admit probes as root",
	"internal/webserver/show.go ListenWarning":            "web_server.listen, which the owner's web server dials",
}

// The egress table site admit loads drops whatever the deployment's user and
// its containers send to loopback, so every command the CLI runs as that
// user, and every file it renders for a host-networked container, binds and
// dials a mesh address. A string literal naming loopback anywhere else in
// the CLI's code, or in a template it renders, is a dial that never connects,
// and is refused here until it is reviewed into loopbackAllowed.
func TestNothingTheDeploymentRunsDialsLoopback(t *testing.T) {
	seen := map[string]bool{}
	for _, root := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			rel := strings.TrimPrefix(path, "../../")
			switch {
			case strings.HasSuffix(path, ".tmpl"):
				content, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				for i, line := range strings.Split(string(content), "\n") {
					if loopbackLiteral.MatchString(line) {
						where := rel
						seen[where] = true
						if loopbackAllowed[where] == "" {
							t.Errorf("%s:%d names loopback: %s", rel, i+1, strings.TrimSpace(line))
						}
					}
				}
			case strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go"):
				file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
				if err != nil {
					return err
				}
				for _, decl := range file.Decls {
					where := rel + " " + declName(decl)
					ast.Inspect(decl, func(n ast.Node) bool {
						lit, ok := n.(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							return true
						}
						s, err := strconv.Unquote(lit.Value)
						if err != nil || !loopbackLiteral.MatchString(s) {
							return true
						}
						seen[where] = true
						if loopbackAllowed[where] == "" {
							t.Errorf("%s names loopback: %.120s", where, s)
						}
						return true
					})
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var stale []string
	for where := range loopbackAllowed {
		if !seen[where] {
			stale = append(stale, where)
		}
	}
	sort.Strings(stale)
	for _, where := range stale {
		t.Errorf("%s is allowed to name loopback and no longer does: remove it from loopbackAllowed", where)
	}
}

// etcdctlCommand matches an etcdctl command line the CLI runs or tells an
// operator to run: one inside a container exec, or quoted as advice.
var etcdctlCommand = regexp.MustCompile("(exec -T etcd |`)etcdctl")

// etcdctl with no --endpoints dials 127.0.0.1:2379, which etcd does not
// listen on, so every etcdctl command the CLI builds or advises names one.
func TestEtcdctlAlwaysNamesItsEndpoints(t *testing.T) {
	for _, root := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err == nil && etcdctlCommand.MatchString(s) && !strings.Contains(s, "--endpoints") {
					t.Errorf("%s: etcdctl without --endpoints: %.120s", strings.TrimPrefix(path, "../../"), s)
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
