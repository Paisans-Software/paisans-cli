package apply_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

// helperHost records each command and its stdin, and answers with out.
type helperHost struct {
	ran, stdin []string
	out        string
}

func (h *helperHost) Run(command string) (string, error) {
	h.ran = append(h.ran, command)
	return h.out, nil
}

func (h *helperHost) RunInput(command, stdin string) (string, error) {
	h.ran = append(h.ran, command)
	h.stdin = append(h.stdin, stdin)
	return h.out, nil
}

func (h *helperHost) ReadFile(string) (string, bool, error)  { return "", false, nil }
func (h *helperHost) WriteFile(string, string, uint32) error { return nil }
func (h *helperHost) Describe() string                       { return "home-a" }

// Every verb is the helper host admit installs for the interface, run with
// sudo -n: the one command the deployment's user may run as root, which
// fails at once on a host without the sudoers line rather than waiting for
// a password.
func TestWGHelperCommandLines(t *testing.T) {
	h := &helperHost{out: "peer\n"}
	w := apply.WGHelper{T: h, Iface: apply.Fixture.Interface()}
	if got, err := w.Path(); err != nil || got != "/usr/local/libexec/paisans/wg-psns-f2a9" {
		t.Errorf("Path() = %q, %v", got, err)
	}
	if err := w.Put([]byte("[Interface]\nListenPort = 51820\n")); err != nil {
		t.Fatal(err)
	}
	for _, f := range []func() error{w.Up, w.Restart, w.Sync} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"peers", "latest-handshakes", "public-key"} {
		if out, err := w.Show(field); err != nil || out != "peer\n" {
			t.Errorf("Show(%s) = %q, %v", field, out, err)
		}
	}
	if _, err := w.Log(); err != nil {
		t.Fatal(err)
	}
	const helper = "sudo -n '/usr/local/libexec/paisans/wg-psns-f2a9' "
	want := []string{helper + "put", helper + "up", helper + "restart", helper + "sync", helper + "show peers", helper + "show latest-handshakes", helper + "show public-key", helper + "log"}
	if strings.Join(h.ran, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran:\n%s\nwant:\n%s", strings.Join(h.ran, "\n"), strings.Join(want, "\n"))
	}
	if len(h.stdin) != 1 || h.stdin[0] != "[Interface]\nListenPort = 51820\n" {
		t.Errorf("put's stdin: %q", h.stdin)
	}
}

// show takes only the fields the helper reads; anything else is refused
// here, before it reaches the host.
func TestWGHelperShowRefusesAnotherField(t *testing.T) {
	h := &helperHost{}
	for _, field := range []string{"", "dump", "peers; id", "private-key"} {
		if _, err := (apply.WGHelper{T: h, Iface: "psns-f2a9"}).Show(field); err == nil {
			t.Errorf("Show(%q) was not refused", field)
		}
	}
	if len(h.ran) != 0 {
		t.Errorf("ran %q", h.ran)
	}
}

// Iface is the name the deployment package produces, psns- and its token. A
// name that is not that is refused by every method before any command is
// built, so the helper path cannot leave HelperDir or carry a quote.
func TestWGHelperRefusesABadInterface(t *testing.T) {
	for _, iface := range []string{"", "../../../tmp/x", "psns-a/b", "psns-x'y", "wg0", "psns-f2a9-0123456", "psns-F2A9", "psns-f2a"} {
		h := &helperHost{}
		w := apply.WGHelper{T: h, Iface: iface}
		calls := map[string]func() error{
			"Path":    func() error { _, err := w.Path(); return err },
			"Put":     func() error { return w.Put([]byte("x")) },
			"Up":      w.Up,
			"Restart": w.Restart,
			"Sync":    w.Sync,
			"Show":    func() error { _, err := w.Show("peers"); return err },
			"Log":     func() error { _, err := w.Log(); return err },
		}
		for name, call := range calls {
			if call() == nil {
				t.Errorf("%s accepted interface %q", name, iface)
			}
		}
		if len(h.ran) != 0 {
			t.Errorf("interface %q ran %q", iface, h.ran)
		}
	}
}
