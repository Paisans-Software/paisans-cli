package apply

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// HelperDir is where host admit installs each deployment's WireGuard helper.
const HelperDir = "/usr/local/libexec/paisans"

// WGHelper runs the WireGuard helper host admit installs for one deployment's
// interface. Creating the interface, writing its conf and syncing its peers
// need CAP_NET_ADMIN in the host's namespace, which the deployment's user
// does not have, so the helper is the one command that user may run as root,
// through one sudoers line, and it takes only these verbs.
//
// Every verb runs with sudo -n, so a host where the sudoers line is missing
// fails at once instead of waiting for a password nobody is there to type.
type WGHelper struct {
	T     Transport
	Iface string
}

// ifacePattern is what deployment.Interface produces: the prefix and a token
// of hex digits, which is also within the 15 bytes Linux allows a name.
var ifacePattern = regexp.MustCompile("^" + regexp.QuoteMeta(deployment.InterfacePrefix) + "[0-9a-f]{" + fmt.Sprint(deployment.TokenLength) + "}$")

// Path is the helper for this interface: HelperDir/wg-<iface>. An Iface that
// is not a deployment's interface name has no helper, so it is an error here
// and in every method, before a command is built from it.
func (h WGHelper) Path() (string, error) {
	if !ifacePattern.MatchString(h.Iface) || len(h.Iface) > deployment.MaxInterfaceName {
		return "", fmt.Errorf("%q is not a deployment's WireGuard interface name (%s followed by %d hex digits)", h.Iface, deployment.InterfacePrefix, deployment.TokenLength)
	}
	return HelperDir + "/wg-" + h.Iface, nil
}

func (h WGHelper) command(verb string) (string, error) {
	path, err := h.Path()
	if err != nil {
		return "", err
	}
	return "sudo -n " + shellQuote(path) + " " + verb, nil
}

// Put sends conf on stdin, for the helper to check and install as the
// interface's wg-quick file.
func (h WGHelper) Put(conf []byte) error {
	cmd, err := h.command("put")
	if err != nil {
		return err
	}
	out, err := h.T.RunInput(cmd, string(conf))
	return withOutput(err, out)
}

// Up enables and starts the interface.
func (h WGHelper) Up() error {
	return h.run("up")
}

// Restart restarts the interface.
func (h WGHelper) Restart() error {
	return h.run("restart")
}

// Sync applies the installed conf's peers to the running interface without
// restarting it.
func (h WGHelper) Sync() error {
	return h.run("sync")
}

// showFields are the fields the helper's show verb reads.
var showFields = map[string]bool{"peers": true, "latest-handshakes": true, "public-key": true}

// Show reads one field of the running interface, as `wg show <iface>
// <field>` prints it.
func (h WGHelper) Show(field string) (string, error) {
	if !showFields[field] {
		return "", fmt.Errorf("the WireGuard helper shows peers, latest-handshakes or public-key, not %q", field)
	}
	cmd, err := h.command("show " + field)
	if err != nil {
		return "", err
	}
	out, err := h.T.Run(cmd)
	return out, withOutput(err, out)
}

// Log is the interface unit's last journal lines.
func (h WGHelper) Log() (string, error) {
	cmd, err := h.command("log")
	if err != nil {
		return "", err
	}
	out, err := h.T.Run(cmd)
	return out, withOutput(err, out)
}

func (h WGHelper) run(verb string) error {
	cmd, err := h.command(verb)
	if err != nil {
		return err
	}
	out, err := h.T.Run(cmd)
	return withOutput(err, out)
}

// withOutput is err with what the helper or sudo printed, which is why it
// failed: SSHTransport's error quotes the output already, and a transport
// that does not loses nothing. The output is compared and appended
// redacted only, as SSHTransport quotes it, so a secret registered for
// display never reaches the error raw. Nil stays nil.
func withOutput(err error, out string) error {
	out = ui.Redact(strings.TrimRight(out, "\n"))
	if err == nil || out == "" || strings.Contains(err.Error(), out) {
		return err
	}
	return fmt.Errorf("%w\n%s", err, out)
}

// CommandLine is the helper's command for verb as an operator types it,
// logged in as the deployment's user, for a message: sudo, the path
// unquoted, and the verb. An Iface that is no deployment's has no helper,
// so it is named in words instead.
func (h WGHelper) CommandLine(verb string) string {
	path, err := h.Path()
	if err != nil {
		return "the WireGuard helper's " + verb
	}
	return "sudo " + path + " " + verb
}
