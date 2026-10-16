package hostprep_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/render"
)

var meshDeployment = deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}

// goodConf is a mesh file as the renderer writes it for a dialled site on
// port 51820 in 10.44.0.0/24, with one peer it dials and one relayed range.
const goodConf = `# Rendered by paisans. Do not edit: ` + "`paisans apply`" + ` overwrites this file.
[Interface]
Address = 10.44.0.4/24
# The port of this site's endpoint, which every other site dials.
ListenPort = 51820
PrivateKey = QEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEA=

[Peer]
# home-b
PublicKey = ehpOcJvwhaxJSroEabmx7aCrH3ixaqu3n/7akGI+hSI=
AllowedIPs = 10.44.0.0/24
Endpoint = 198.51.100.2:51820
# Keeps the NAT mapping open from the side that dials out.
PersistentKeepalive = 25

[Peer]
# vm
PublicKey = EyxEK+AQ+9V+cmAzKKp25x/MwVA6riGTJ9FNnJmT9HI=
AllowedIPs = 10.44.0.3/32
Endpoint = vm.example.org:51820
PersistentKeepalive = 25
`

// shells are the shells the helper is run under: sh always, and dash and
// bash where this machine has them, since a host's /bin/sh is dash on
// Debian and Ubuntu.
func shells() []string {
	out := []string{"sh"}
	for _, s := range []string{"dash", "bash"} {
		if _, err := exec.LookPath(s); err == nil {
			out = append(out, s)
		}
	}
	return out
}

// helperIn renders the helper for the fixture deployment with port and
// subnet built in and a fresh conf directory of the test's, and returns the
// script's path and that directory.
func helperIn(t *testing.T, port int, subnet string) (script, dir string) {
	t.Helper()
	dir = t.TempDir()
	text, err := hostprep.WGHelperScriptIn(dir, meshDeployment, port, subnet)
	if err != nil {
		t.Fatal(err)
	}
	script = filepath.Join(t.TempDir(), "wg-psns-f2a9")
	if err := os.WriteFile(script, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, dir
}

// runHelper runs script under shell with args, stdin on its standard input,
// and returns what it printed and its exit status.
func runHelper(t *testing.T, shell, script, stdin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(shell, append([]string{script}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exit):
		return string(out), exit.ExitCode()
	}
	t.Fatalf("running %s %s: %v", shell, script, err)
	return "", -1
}

// entries lists dir, so a test can see that a refused put left nothing,
// not even its temporary file.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range list {
		out = append(out, e.Name())
	}
	return out
}

// The renderer's own output is what the helper exists to install: every
// site of the fixture, dialled or not, relaying or relayed, is accepted
// with its own listen port, and installed as written, mode 600.
func TestTheHelperInstallsWhatTheRendererWrites(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets(filepath.Join("..", "render", "testdata", "secrets.fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Deployment()
	seen := 0
	for _, f := range plan.Files {
		site, rel, _ := strings.Cut(f.Path, "/")
		if rel != d.WireGuardConf() {
			continue
		}
		seen++
		for _, shell := range shells() {
			script, dir := helperIn(t, cfg.Sites[site].ListenPort(), cfg.Mesh.Subnet)
			if out, code := runHelper(t, shell, script, f.Content, "put"); code != 0 {
				t.Fatalf("%s: %s refused %s's mesh file (exit %d):\n%s\n%s", shell, script, site, code, out, f.Content)
			}
			conf := filepath.Join(dir, "psns-f2a9.conf")
			got, err := os.ReadFile(conf)
			if err != nil || string(got) != marked(f.Content) {
				t.Errorf("%s: %s's mesh file was not installed as written: %v", shell, site, err)
			}
			if info, err := os.Lstat(conf); err != nil || info.Mode() != 0o600 {
				t.Errorf("%s: %s's installed file has mode %v, want -rw-------", shell, site, info.Mode())
			}
			if names := entries(t, dir); len(names) != 1 {
				t.Errorf("%s: put left %v in the directory", shell, names)
			}
		}
	}
	if seen != 4 {
		t.Fatalf("%d mesh files rendered, want four", seen)
	}
}

// Keys and values the renderer can write but the fixture does not are
// accepted too.
func TestTheHelperAcceptsEveryKeyTheRendererMayWrite(t *testing.T) {
	conf := strings.Replace(goodConf, "PrivateKey = ", "MTU = 1420\nPrivateKey = ", 1)
	conf = strings.Replace(conf, "Endpoint = vm.example.org:51820", "Endpoint = [2001:db8::3]:51820\nPresharedKey = ze/YeDqRtEZkDi4flVmds15ISgBxvSGCs7YNCBLBDHA=", 1)
	conf = strings.Replace(conf, "AllowedIPs = 10.44.0.3/32", "AllowedIPs = 10.44.0.3/32, 10.44.0.128/25", 1)
	conf = "\n  # an indented comment\n\t\n" + conf
	for _, shell := range shells() {
		script, _ := helperIn(t, 51820, "10.44.0.0/24")
		if out, code := runHelper(t, shell, script, conf, "put"); code != 0 {
			t.Errorf("%s: refused (exit %d): %s", shell, code, out)
		}
	}
}

// Every way a conf could run a command as root, or reach past the port and
// subnet the owner admitted, refuses the whole file: exit 65, a line saying
// why, and nothing in the directory.
func TestTheHelperRefusesAnythingElse(t *testing.T) {
	replace := func(old, new string) string { return strings.Replace(goodConf, old, new, 1) }
	keepalive := "PersistentKeepalive = 25\n"
	for _, c := range []struct {
		name, conf, want string
	}{
		{"PostUp in [Interface]", replace("PrivateKey = ", "PostUp = id\nPrivateKey = "), "runs a command as root"},
		{"PostUp in a [Peer]", replace(keepalive, "PostUp = id\n"), "runs a command as root"},
		{"PreUp", replace(keepalive, "PreUp = id\n"), "runs a command as root"},
		{"PreDown", replace(keepalive, "PreDown = id\n"), "runs a command as root"},
		{"PostDown", replace(keepalive, "PostDown = id\n"), "runs a command as root"},
		{"a hook in lower case", replace(keepalive, "postup = id\n"), "runs a command as root"},
		{"a hook in upper case", replace(keepalive, "POSTUP = id\n"), "runs a command as root"},
		{"a hook behind spaces", replace(keepalive, "    PostUp = id\n"), "runs a command as root"},
		{"a hook behind a tab", replace(keepalive, "\tPostUp = id\n"), "runs a command as root"},
		{"a hook with no spaces", replace(keepalive, "PostUp=id\n"), "runs a command as root"},
		{"a hook with spaces before =", replace(keepalive, "PostUp \t = id\n"), "runs a command as root"},
		{"a hook before any section", "PostUp = id\n" + goodConf, "runs a command as root"},
		{"a line with no =", replace(keepalive, "PostUp\n"), "is not key = value"},
		{"DNS", replace("PrivateKey = ", "DNS = 1.1.1.1\nPrivateKey = "), "is not a key paisans writes in [Interface]"},
		{"Table", replace("PrivateKey = ", "Table = off\nPrivateKey = "), "is not a key paisans writes"},
		{"SaveConfig", replace("PrivateKey = ", "SaveConfig = true\nPrivateKey = "), "is not a key paisans writes"},
		{"FwMark", replace("PrivateKey = ", "FwMark = 51820\nPrivateKey = "), "FwMark is set by this helper"},
		{"FwMark in a [Peer]", replace(keepalive, "FwMark = 0x5053f2a9\n"), "FwMark is set by this helper"},
		{"an [Interface] key in a [Peer]", replace(keepalive, "MTU = 1420\n"), "is not a key paisans writes in [Peer]"},
		{"a known key in another case", replace("Address = ", "address = "), "is not a key paisans writes"},
		{"a section in another case", replace("[Peer]", "[peer]"), "is not [Interface] or [Peer]"},
		{"another section", replace("[Peer]", "[Match]"), "is not [Interface] or [Peer]"},
		{"a second [Interface]", goodConf + "\n[Interface]\n", "a second [Interface]"},
		{"a key before any section", "Address = 10.44.0.4/24\n" + goodConf, "before any section"},
		{"a second Address", replace("PrivateKey = ", "Address = 10.44.0.5/24\nPrivateKey = "), "a second Address"},
		{"a ListenPort other than the admitted one", replace("ListenPort = 51820", "ListenPort = 51821"), "admitted on 51820"},
		{"no ListenPort on a host admitted with one", replace("ListenPort = 51820\n", ""), "no ListenPort"},
		{"an Address outside the subnet", replace("Address = 10.44.0.4/24", "Address = 10.45.0.4/24"), "is not one address in 10.44.0.0/24"},
		{"an Address that is the subnet's network", replace("Address = 10.44.0.4/24", "Address = 10.44.0.0/24"), "is the network or broadcast address of 10.44.0.0/24"},
		{"an Address that is the subnet's broadcast", replace("Address = 10.44.0.4/24", "Address = 10.44.0.255/24"), "is the network or broadcast address of 10.44.0.0/24"},
		{"an Address with another prefix", replace("Address = 10.44.0.4/24", "Address = 10.44.0.4/16"), "is not one address in 10.44.0.0/24"},
		{"two Addresses on one line", replace("Address = 10.44.0.4/24", "Address = 10.44.0.4/24, 10.44.0.5/24"), "is not one address"},
		{"an IPv6 Address", replace("Address = 10.44.0.4/24", "Address = fd00::4/64"), "is not one address"},
		{"an octet over 255", replace("Address = 10.44.0.4/24", "Address = 10.44.0.256/24"), "is not one address"},
		{"an octet with a leading zero", replace("Address = 10.44.0.4/24", "Address = 10.44.0.04/24"), "is not one address"},
		{"AllowedIPs everywhere", replace("AllowedIPs = 10.44.0.3/32", "AllowedIPs = 0.0.0.0/0"), "is not inside 10.44.0.0/24"},
		{"AllowedIPs wider than the mesh", replace("AllowedIPs = 10.44.0.3/32", "AllowedIPs = 10.44.0.0/16"), "is not inside 10.44.0.0/24"},
		{"AllowedIPs on the owner's LAN", replace("AllowedIPs = 10.44.0.3/32", "AllowedIPs = 10.44.0.3/32, 192.168.1.0/24"), "192.168.1.0/24 is not inside"},
		{"an Endpoint carrying a command", replace("Endpoint = vm.example.org:51820", "Endpoint = $(id):51820"), "is not host:port"},
		{"an Endpoint with no port", replace("Endpoint = vm.example.org:51820", "Endpoint = vm.example.org"), "is not host:port"},
		{"an Endpoint on port 0", replace("Endpoint = vm.example.org:51820", "Endpoint = vm.example.org:0"), "is not host:port"},
		{"a PrivateKey that is not a key", replace("PrivateKey = QEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEA=", "PrivateKey = x"), "PrivateKey is not a WireGuard key"},
		{"a PublicKey that is not a key", replace("PublicKey = EyxEK+AQ+9V+cmAzKKp25x/MwVA6riGTJ9FNnJmT9HI=", "PublicKey = EyxEK+AQ+9V+cmAzKKp25x/MwVA6riGTJ9FNnJmT9HI"), "PublicKey is not a WireGuard key"},
		{"a value with a trailing comment", replace(keepalive, "PersistentKeepalive = 25 # keep\n"), "PersistentKeepalive"},
		{"an MTU out of range", replace("PrivateKey = ", "MTU = 100\nPrivateKey = "), "MTU 100 is not from 1280 to 9000"},
		{"a [Peer] without PublicKey", strings.Replace(goodConf, "PublicKey = EyxEK+AQ+9V+cmAzKKp25x/MwVA6riGTJ9FNnJmT9HI=\n", "", 1), "the last [Peer] has no PublicKey"},
		{"CRLF line endings", strings.ReplaceAll(goodConf, "\n", "\r\n"), "carriage return"},
		{"a NUL byte", replace("[Peer]", "[Peer]\x00"), "NUL byte"},
		{"a conf over 64 KiB", goodConf + strings.Repeat("# padding\n", 7000), "larger than 65536 bytes"},
		{"a hook behind a vertical tab", replace(keepalive, "\vPostUp = id\n"), "is not a key paisans writes"},
		{"a hook behind a form feed", replace(keepalive, "\fPostUp = id\n"), "is not a key paisans writes"},
		{"a hook followed by a form feed", replace(keepalive, "PostUp\f= id\n"), "is not a key paisans writes"},
		{"a hook in fullwidth letters", replace(keepalive, "\uff30ostUp = id\n"), "is not a key paisans writes"},
		{"a hook with a Cyrillic o", replace(keepalive, "P\u043estUp = id\n"), "is not a key paisans writes"},
		{"a header behind a vertical tab", replace("[Peer]", "\v[Peer]"), "is not key = value"},
		{"a hex octet in an Address", replace("Address = 10.44.0.4/24", "Address = 0x0a.44.0.4/24"), "is not one address"},
		{"a plus octet in an Address", replace("Address = 10.44.0.4/24", "Address = +10.44.0.4/24"), "is not one address"},
		{"a hex octet in AllowedIPs", replace("AllowedIPs = 10.44.0.3/32", "AllowedIPs = 0x0a.44.0.3/32"), "is not inside"},
		{"a plus octet in AllowedIPs", replace("AllowedIPs = 10.44.0.3/32", "AllowedIPs = +10.44.0.3/32"), "is not inside"},
		{"an exponent prefix in AllowedIPs", replace("AllowedIPs = 10.44.0.3/32", "AllowedIPs = 10.44.0.3/2e1"), "is not inside"},
		{"an empty AllowedIPs", replace("AllowedIPs = 10.44.0.3/32", "AllowedIPs ="), "AllowedIPs is empty"},
		{"a ListenPort with a plus", replace("ListenPort = 51820", "ListenPort = +51820"), "admitted on 51820"},
		{"a ListenPort with a leading zero", replace("ListenPort = 51820", "ListenPort = 051820"), "admitted on 51820"},
		{"a ListenPort in hex", replace("ListenPort = 51820", "ListenPort = 0xca6c"), "admitted on 51820"},
		{"a ListenPort in exponent form", replace("ListenPort = 51820", "ListenPort = 5182e1"), "admitted on 51820"},
		{"an Endpoint on 127.0.0.1", replace("Endpoint = vm.example.org:51820", "Endpoint = 127.0.0.1:53"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on 0.0.0.0", replace("Endpoint = vm.example.org:51820", "Endpoint = 0.0.0.0:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on 0.1.2.3", replace("Endpoint = vm.example.org:51820", "Endpoint = 0.1.2.3:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on link-local", replace("Endpoint = vm.example.org:51820", "Endpoint = 169.254.169.254:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on multicast", replace("Endpoint = vm.example.org:51820", "Endpoint = 224.0.0.1:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on broadcast", replace("Endpoint = vm.example.org:51820", "Endpoint = 255.255.255.255:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on IPv6 multicast", replace("Endpoint = vm.example.org:51820", "Endpoint = [ff02::1]:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on IPv6 multicast in upper case", replace("Endpoint = vm.example.org:51820", "Endpoint = [FF05::2]:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on ::1", replace("Endpoint = vm.example.org:51820", "Endpoint = [::1]:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on ::", replace("Endpoint = vm.example.org:51820", "Endpoint = [::]:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on ::1 written out", replace("Endpoint = vm.example.org:51820", "Endpoint = [0:0:0:0:0:0:0:1]:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on fe80::", replace("Endpoint = vm.example.org:51820", "Endpoint = [fe80::1]:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on febf::", replace("Endpoint = vm.example.org:51820", "Endpoint = [FEBF::1]:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on lower case febf::", replace("Endpoint = vm.example.org:51820", "Endpoint = [febf::1]:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on a mapped loopback", replace("Endpoint = vm.example.org:51820", "Endpoint = [::ffff:127.0.0.1]:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on a short loopback", replace("Endpoint = vm.example.org:51820", "Endpoint = 127.1:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on a decimal loopback", replace("Endpoint = vm.example.org:51820", "Endpoint = 2130706433:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on an octal loopback", replace("Endpoint = vm.example.org:51820", "Endpoint = 0177.0.0.1:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on a hex loopback", replace("Endpoint = vm.example.org:51820", "Endpoint = 0x7f.0.0.1:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint on localhost", replace("Endpoint = vm.example.org:51820", "Endpoint = localhost:51820"), "loopback, link-local, multicast or unspecified"},
		{"an Endpoint under .localhost", replace("Endpoint = vm.example.org:51820", "Endpoint = a.LOCALHOST:51820"), "loopback, link-local, multicast or unspecified"},
		{"nothing", "", "no [Interface]"},
	} {
		for _, shell := range shells() {
			script, dir := helperIn(t, 51820, "10.44.0.0/24")
			out, code := runHelper(t, shell, script, c.conf, "put")
			if code != 65 || !strings.Contains(out, c.want) || !strings.Contains(out, "nothing was installed") {
				t.Errorf("%s, %s: exit %d, %q; want 65 and %q", c.name, shell, code, out, c.want)
			}
			if names := entries(t, dir); len(names) != 0 {
				t.Errorf("%s, %s: a refused put left %v", c.name, shell, names)
			}
		}
	}
}

// A site nothing dials has no fixed port: rendered with port 0, the helper
// refuses any ListenPort and accepts a conf without one.
func TestAHostAdmittedWithNoPortTakesNoListenPort(t *testing.T) {
	for _, shell := range shells() {
		script, _ := helperIn(t, 0, "10.44.0.0/24")
		if out, code := runHelper(t, shell, script, goodConf, "put"); code != 65 || !strings.Contains(out, "admitted with none") {
			t.Errorf("%s: a ListenPort on a host admitted with none: exit %d, %q", shell, code, out)
		}
		without := strings.Replace(goodConf, "ListenPort = 51820\n", "", 1)
		if out, code := runHelper(t, shell, script, without, "put"); code != 0 {
			t.Errorf("%s: a conf with no ListenPort was refused: %d, %q", shell, code, out)
		}
	}
}

// The conf's path is replaced by the rename, never written through: a link
// planted there to a file leaves that file as it was, and a link to a
// directory, or a directory, is refused rather than moved into.
func TestPutNeverWritesThroughALink(t *testing.T) {
	for _, shell := range shells() {
		script, dir := helperIn(t, 51820, "10.44.0.0/24")
		conf := filepath.Join(dir, "psns-f2a9.conf")
		target := filepath.Join(t.TempDir(), "shadow")
		if err := os.WriteFile(target, []byte("root's\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, conf); err != nil {
			t.Fatal(err)
		}
		if out, code := runHelper(t, shell, script, goodConf, "put"); code != 0 {
			t.Fatalf("%s: exit %d: %s", shell, code, out)
		}
		if got, _ := os.ReadFile(target); string(got) != "root's\n" {
			t.Errorf("%s: put wrote through the link: %q", shell, got)
		}
		if info, err := os.Lstat(conf); err != nil || !info.Mode().IsRegular() {
			t.Errorf("%s: the link was not replaced by a file: %v", shell, err)
		}

		script, dir = helperIn(t, 51820, "10.44.0.0/24")
		elsewhere := t.TempDir()
		if err := os.Symlink(elsewhere, filepath.Join(dir, "psns-f2a9.conf")); err != nil {
			t.Fatal(err)
		}
		if out, code := runHelper(t, shell, script, goodConf, "put"); code != 65 || !strings.Contains(out, "is a directory") {
			t.Errorf("%s: a link to a directory: exit %d, %q", shell, code, out)
		}
		if names := entries(t, elsewhere); len(names) != 0 {
			t.Errorf("%s: put wrote %v into the linked directory", shell, names)
		}
	}
}

// A refused put leaves the conf already installed exactly as it was.
func TestARefusedPutLeavesTheInstalledFile(t *testing.T) {
	for _, shell := range shells() {
		script, dir := helperIn(t, 51820, "10.44.0.0/24")
		if _, code := runHelper(t, shell, script, goodConf, "put"); code != 0 {
			t.Fatal("the good conf was refused")
		}
		bad := strings.Replace(goodConf, "PersistentKeepalive = 25\n", "PostUp = id\n", 1)
		if _, code := runHelper(t, shell, script, bad, "put"); code != 65 {
			t.Fatalf("%s: the bad conf was not refused", shell)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "psns-f2a9.conf")); string(got) != marked(goodConf) {
			t.Errorf("%s: a refused put changed the installed conf", shell)
		}
	}
}

// The conf directory must be a directory of its own, not a link to one.
func TestPutRefusesALinkedConfDirectory(t *testing.T) {
	real := t.TempDir()
	linked := filepath.Join(t.TempDir(), "wireguard")
	if err := os.Symlink(real, linked); err != nil {
		t.Fatal(err)
	}
	text, err := hostprep.WGHelperScriptIn(linked, meshDeployment, 51820, "10.44.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "wg-psns-f2a9")
	if err := os.WriteFile(script, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, code := runHelper(t, "sh", script, goodConf, "put"); code != 65 || !strings.Contains(out, "is not a directory") {
		t.Errorf("exit %d, %q", code, out)
	}
	if names := entries(t, real); len(names) != 0 {
		t.Errorf("put wrote %v through the linked directory", names)
	}
}

// put installs only into a conf directory root holds alone: one the
// deployment's user could write would let it plant a conf with PostUp
// that wg-quick runs as root at the next up, past every check put makes.
// The scratch directory stands in for /etc/wireguard with the test's own
// uid as the owner put expects, and is refused when owned by anyone else
// or writable by its group or others.
func TestPutRefusesAConfDirectoryRootDoesNotHoldAlone(t *testing.T) {
	render := func(t *testing.T, dir string, owner int) string {
		t.Helper()
		text, err := hostprep.WGHelperScriptOwnedBy(dir, owner, meshDeployment, 51820, "10.44.0.0/24")
		if err != nil {
			t.Fatal(err)
		}
		script := filepath.Join(t.TempDir(), "wg-psns-f2a9")
		if err := os.WriteFile(script, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
		return script
	}
	for _, c := range []struct {
		name  string
		mode  os.FileMode
		owner int
		ok    bool
	}{
		{"the owner's alone", 0o700, os.Getuid(), true},
		{"readable by all", 0o755, os.Getuid(), true},
		{"another owner's", 0o700, os.Getuid() + 1, false},
		{"group writable", 0o770, os.Getuid(), false},
		{"writable by others", 0o703, os.Getuid(), false},
	} {
		for _, shell := range shells() {
			dir := t.TempDir()
			if err := os.Chmod(dir, c.mode); err != nil {
				t.Fatal(err)
			}
			out, code := runHelper(t, shell, render(t, dir, c.owner), goodConf, "put")
			switch {
			case c.ok && code != 0:
				t.Errorf("%s: %s: refused (exit %d): %s", shell, c.name, code, out)
			case !c.ok && (code != 65 || !strings.Contains(out, dir+" is not a directory only root may write")):
				t.Errorf("%s: %s: exit %d, %q", shell, c.name, code, out)
			case !c.ok && len(entries(t, dir)) != 0:
				t.Errorf("%s: %s: put left %v", shell, c.name, entries(t, dir))
			}
		}
	}
}

// The verbs are the only arguments the helper takes: anything else exits
// 64 before running a command or touching the directory.
func TestTheHelperTakesOnlyItsVerbs(t *testing.T) {
	for _, args := range [][]string{
		{}, {"bogus"}, {"put", "extra"}, {"up", "now"}, {"restart", "-f"}, {"sync", "x"},
		{"show"}, {"show", "dump"}, {"show", "private-key"}, {"show", "endpoints"}, {"show", "preshared-keys"}, {"show", "peers", "extra"}, {"log", "-n", "1000"},
	} {
		for _, shell := range shells() {
			script, dir := helperIn(t, 51820, "10.44.0.0/24")
			out, code := runHelper(t, shell, script, goodConf, args...)
			if code != 64 || !strings.Contains(out, "usage:") {
				t.Errorf("%s %v: exit %d, %q; want 64 and the usage", shell, args, code, out)
			}
			if names := entries(t, dir); len(names) != 0 {
				t.Errorf("%s %v: left %v", shell, args, names)
			}
		}
	}
}

// show reads only fields the interface's own conf already holds, or that
// say how it is doing: never a key it keeps secret, and never where its
// peers last sent from.
func TestTheHelperShowsItsReadOnlyFields(t *testing.T) {
	for _, field := range []string{"peers", "latest-handshakes", "public-key", "allowed-ips", "persistent-keepalive", "listen-port"} {
		for _, shell := range shells() {
			script, _ := helperIn(t, 51820, "10.44.0.0/24")
			// wg is absent or finds no such interface here: either way the
			// helper ran it rather than refusing the field.
			out, code := runHelper(t, shell, script, "", "show", field)
			if code == 64 || strings.Contains(out, "usage:") {
				t.Errorf("%s show %s: exit %d, %q; the field was refused", shell, field, code, out)
			}
		}
	}
}

// The values the helper enforces are built in when it is rendered, from
// the deployment and what the owner admits, and nothing the caller passes
// can change them.
func TestTheHelperIsRenderedWithItsValuesBuiltIn(t *testing.T) {
	text, err := hostprep.WGHelperScript(meshDeployment, 51820, "10.44.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"iface='psns-f2a9'\n", "dir='/etc/wireguard'\n", "unit='wg-quick@psns-f2a9'\n",
		"port='51820'\n", "owner='0'\n", "net='10.44.0.0'\n", "prefix='24'\n", "/usr/local/libexec/paisans/wg-psns-f2a9",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the rendered helper lacks %q", want)
		}
	}
	if !strings.HasPrefix(text, "#!/bin/sh\n") {
		t.Error("the helper does not start with #!/bin/sh")
	}
}

// What the helper cannot be rendered for is refused at render time.
func TestTheHelperRefusesBadValues(t *testing.T) {
	for name, build := range map[string]func() (string, error){
		"an interface that is no deployment's": func() (string, error) {
			return hostprep.WGHelperScript(deployment.Deployment{ID: "zzzz-not-hex"}, 51820, "10.44.0.0/24")
		},
		"an IPv6 subnet":          func() (string, error) { return hostprep.WGHelperScript(meshDeployment, 51820, "fd00::/64") },
		"a subnet with no prefix": func() (string, error) { return hostprep.WGHelperScript(meshDeployment, 51820, "10.44.0.0") },
		"the whole IPv4 space":    func() (string, error) { return hostprep.WGHelperScript(meshDeployment, 51820, "0.0.0.0/0") },
		"a /23 subnet":            func() (string, error) { return hostprep.WGHelperScript(meshDeployment, 51820, "10.44.0.0/23") },
		"a /16 subnet":            func() (string, error) { return hostprep.WGHelperScript(meshDeployment, 51820, "192.168.0.0/16") },
		"a loopback subnet":       func() (string, error) { return hostprep.WGHelperScript(meshDeployment, 51820, "127.0.0.0/24") },
		"a zero-net subnet":       func() (string, error) { return hostprep.WGHelperScript(meshDeployment, 51820, "0.1.0.0/24") },
		"a link-local subnet":     func() (string, error) { return hostprep.WGHelperScript(meshDeployment, 51820, "169.254.1.0/24") },
		"a multicast subnet":      func() (string, error) { return hostprep.WGHelperScript(meshDeployment, 51820, "224.0.0.0/24") },
		"a reserved subnet":       func() (string, error) { return hostprep.WGHelperScript(meshDeployment, 51820, "240.0.0.0/24") },
		"a host address":          func() (string, error) { return hostprep.WGHelperScript(meshDeployment, 51820, "10.44.0.5/24") },
		"a non-canonical subnet":  func() (string, error) { return hostprep.WGHelperScript(meshDeployment, 51820, "10.44.0.0/024") },
		"a negative port":         func() (string, error) { return hostprep.WGHelperScript(meshDeployment, -1, "10.44.0.0/24") },
		"a port over 65535":       func() (string, error) { return hostprep.WGHelperScript(meshDeployment, 65536, "10.44.0.0/24") },
		"a relative conf directory": func() (string, error) {
			return hostprep.WGHelperScriptIn("etc/wireguard", meshDeployment, 51820, "10.44.0.0/24")
		},
		"a conf directory with a quote": func() (string, error) {
			return hostprep.WGHelperScriptIn("/tmp/it's", meshDeployment, 51820, "10.44.0.0/24")
		},
	} {
		if _, err := build(); err == nil {
			t.Errorf("%s was rendered", name)
		}
	}
}

// Endpoints on a private LAN, a documentation range or a name are the
// ordinary case: two sites may share a LAN.
func TestTheHelperAcceptsEndpointsOnALan(t *testing.T) {
	for _, ep := range []string{"[fe8::1]:51820", "192.168.1.20:51820", "10.0.0.5:51820", "172.16.3.4:51820", "198.51.100.9:1", "[fd00::5]:51820", "[2001:db8::3]:51820", "[fec0::1]:51820", "vm.example.org:51820", "localhostel.example.org:51820", "0xfoo.example.org:51820"} {
		conf := strings.Replace(goodConf, "Endpoint = vm.example.org:51820", "Endpoint = "+ep, 1)
		for _, shell := range shells() {
			script, _ := helperIn(t, 51820, "10.44.0.0/24")
			if out, code := runHelper(t, shell, script, conf, "put"); code != 0 {
				t.Errorf("%s: Endpoint %s refused (exit %d): %s", shell, ep, code, out)
			}
		}
	}
}

// sync holds the conf's private key in a temporary file, so the file lives
// beside the conf in the root-owned directory, never under a TMPDIR.
func TestSyncKeepsItsTemporaryFileBesideTheConf(t *testing.T) {
	text, err := hostprep.WGHelperScript(meshDeployment, 51820, "10.44.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `stripped=$(mktemp "$dir/.$iface.strip.XXXXXX")`) {
		t.Error("sync does not make its temporary file in the conf directory")
	}
	if strings.Contains(text, "mktemp)") {
		t.Error("the helper makes a temporary file under TMPDIR")
	}
}

// marked is conf as put installs it: the deployment's FwMark on the line
// after [Interface], which the egress table's mark chain matches.
func marked(conf string) string {
	return strings.Replace(conf, "[Interface]\n", "[Interface]\nFwMark = 0x5053f2a9\n", 1)
}

// The mark goes into every conf put installs, exactly once, after
// [Interface], whatever spacing the [Interface] line has.
func TestPutMarksEveryConf(t *testing.T) {
	for _, shell := range shells() {
		script, dir := helperIn(t, 51820, "10.44.0.0/24")
		conf := strings.Replace(goodConf, "[Interface]", "  [Interface]\t", 1)
		if out, code := runHelper(t, shell, script, conf, "put"); code != 0 {
			t.Fatalf("%s: refused (exit %d): %s", shell, code, out)
		}
		got, err := os.ReadFile(filepath.Join(dir, "psns-f2a9.conf"))
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(got), "FwMark"); n != 1 || !strings.Contains(string(got), "  [Interface]\t\nFwMark = 0x5053f2a9\n") {
			t.Errorf("%s: installed conf carries FwMark %d times, or not after [Interface]:\n%s", shell, n, got)
		}
		if names := entries(t, dir); len(names) != 1 {
			t.Errorf("%s: put left %v in the directory", shell, names)
		}
	}
}
