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

// meshWords are the commands that reach the mesh interface or its unit.
const meshWords = `wg|wg-quick|journalctl|systemctl`

// optArg is an option's value, joined by = or as the next word.
const optArg = `(?:=\S*|\s+\S+)`

// meshWrapper is a command that runs the command after it, with its own
// options and arguments: sudo, env, timeout, nice, xargs, stdbuf, exec,
// command and nohup.
const meshWrapper = `(?:` +
	`sudo(?:\s+(?:--(?:user|group|host|prompt|role|type|other-user|chdir|chroot|close-from|command-timeout)` + optArg + `|-[ugCDhpRrTU]\s+\S+|-\S+))*` +
	`|env(?:\s+(?:-u\s+\S+|--unset` + optArg + `|-\S+|\S+=\S*))*` +
	`|timeout(?:\s+(?:-[sk]\s+\S+|--(?:signal|kill-after)` + optArg + `|-\S+))*\s+\d\S*` +
	`|nice(?:\s+(?:-n\s+\S+|--adjustment` + optArg + `|-\S+))*` +
	`|xargs(?:\s+(?:-[IiLlnPsdEa]\s+\S+|-\S+))*` +
	`|stdbuf(?:\s+(?:-[ioe]\s+\S+|-\S+))*` +
	`|exec|command|nohup)`

// meshCommand matches one of meshWords where a shell starts a command: at
// the start of the string or of a line, after ;, &, |, ( (which covers &&,
// || and $( ) or a backtick, or inside sh -c '. Before the word it allows
// any number of meshWrapper, in any order, and an absolute path. Group 1 is
// the word and group 2 the argument after it, if any.
var meshCommand = regexp.MustCompile(
	"(?:^|[\\n;&|(`]|\\b(?:ba)?sh\\s+-c\\s+['\"])\\s*" +
		`(?:` + meshWrapper + `\s+)*` +
		`(?:/\S*/)?(` + meshWords + ")" +
		"(?:$|[\\s'\"`;&|)]\\s*(\\S*))")

// meshWord matches a string that is nothing but one of meshWords, Eg: a
// constant joined to its arguments elsewhere.
var meshWord = regexp.MustCompile(`^\s*(?:/\S*/)?(?:` + meshWords + `)\s*$`)

// directMeshCommand reports whether s, a string in a package that touches
// the mesh, runs one of meshWords directly. systemctl --user is the
// deployment user's own systemd and does not reach the mesh, so that one
// command is allowed; any other in the same string is still caught.
func directMeshCommand(s string) bool {
	if meshWord.MatchString(s) {
		return true
	}
	for _, m := range meshCommand.FindAllStringSubmatch(s, -1) {
		if m[1] == "systemctl" && m[2] == "--user" {
			continue
		}
		return true
	}
	return false
}

// exemptLiterals are, by file, the exact strings that may run a mesh
// command directly, and why.
var exemptLiterals = map[string]map[string]string{}

// Every command that reaches the mesh interface goes through the WireGuard
// helper, because it is the one thing the deployment's user may run as
// root: one written directly fails on a host, without sudo. This walks the
// packages that touch the mesh and refuses a string literal that runs such
// a command, or that is one of their names on its own.
func TestTheMeshIsReachedOnlyThroughTheHelper(t *testing.T) {
	used := map[string]bool{}
	for _, dir := range []string{".", "../meshkey", "../meshrekey", "../siteadd", "../siteremove", "../doctor"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatalf("%s holds no Go file; the walk would pass on nothing", dir)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
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
				if err != nil || !directMeshCommand(s) {
					return true
				}
				if exemptLiterals[file][s] != "" {
					used[s] = true
					return true
				}
				t.Errorf("%s: %q runs a mesh command directly; go through apply.WGHelper", fset.Position(lit.Pos()), s)
				return true
			})
		}
	}
	// An exemption nothing matches any more is stale, and would silently
	// cover a new string with the same text.
	for file, literals := range exemptLiterals {
		for s := range literals {
			if !used[s] {
				t.Errorf("%s: the exempt string %q is not in the file any more; remove its exemption", file, s)
			}
		}
	}
}

// The guard's matcher catches each form a direct command can take, and
// lets prose and the user's own systemd through.
func TestTheMeshGuardCatchesEachForm(t *testing.T) {
	caught := []string{
		"wg show ",
		"wg",
		"wg-quick",
		" journalctl ",
		"systemctl",
		"/usr/bin/wg",
		"wg show psns-f2a9 peers",
		"wg-quick up psns-f2a9",
		"journalctl -u wg-quick@psns-f2a9",
		"systemctl restart wg-quick@psns-f2a9",
		"sudo wg show x",
		"sudo -n wg show x",
		"sudo -u root wg show x",
		"sudo -n -u root -E wg show x",
		"env wg show x",
		"env LC_ALL=C wg show x",
		"sudo env PATH=/usr/sbin wg show x",
		"exec wg-quick down x",
		"/usr/bin/wg show x",
		"sudo /usr/bin/wg show x",
		"true; wg show x",
		"true;wg show x",
		"true && wg show x",
		"false || wg show x",
		"echo x | wg setconf x /dev/stdin",
		"x\nwg show x",
		"echo $(wg show x)",
		"echo `wg show x`",
		"(wg show x)",
		"sh -c 'wg show x'",
		`sh -c "wg show x"`,
		"bash -c 'sudo wg show x'",
		"wg\tshow x",
		"systemctl --user status docker; systemctl stop x",
		"systemctl --user restart docker && sudo systemctl restart wg-quick@x",
		"x; systemctl",
		"sudo --user root wg show x",
		"sudo --user=root wg show x",
		"sudo -g grp wg show x",
		"sudo --group grp wg show x",
		"sudo --group=grp -n wg show x",
		"sudo -n --user root systemctl restart x",
		"timeout 5 wg show x",
		"timeout -s KILL 5s wg show x",
		"timeout --signal=KILL -k 1 5 wg show x",
		"sudo timeout 5 wg show x",
		"nice wg show x",
		"nice -n 10 wg show x",
		"nice -10 wg show x",
		"nice --adjustment=5 wg show x",
		"echo x | xargs wg show",
		"echo x | xargs -r wg show",
		"echo x | xargs -I {} wg show {}",
		"echo x | xargs -n 1 -P 2 wg show",
		"nohup wg-quick up x",
		"nohup sudo wg-quick up x",
		"stdbuf -oL wg show x",
		"stdbuf -o L -e 0 journalctl -u x",
		"stdbuf --output=L wg show x",
		"env -i wg show x",
		"env -u HOME wg show x",
		"timeout 5 nice -n 1 stdbuf -oL sudo -u root env A=1 wg show x",
	}
	for _, s := range caught {
		if !directMeshCommand(s) {
			t.Errorf("%q is not caught", s)
		}
	}
	allowed := []string{
		"systemctl --user status docker",
		"As that user, check `systemctl --user status docker`, and that DOCKER_HOST is set",
		"restart x, so it is up on its file's Address and MTU, which only wg-quick applies",
		"wg-quick: %s\n%s",
		" wg-quick[",
		"wgsomething show",
		"swg show",
		"sudo -n '/usr/local/libexec/paisans/wg-psns-f2a9' show peers",
		"the WireGuard helper's show latest-handshakes",
		"/etc/wireguard/psns-f2a9.conf",
	}
	for _, s := range allowed {
		if directMeshCommand(s) {
			t.Errorf("%q is caught, and runs no mesh command", s)
		}
	}
}
