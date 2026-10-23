package apply

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// HelperDir is where site admit installs each deployment's WireGuard helper.
const HelperDir = "/usr/local/libexec/paisans"

// WGHelper runs the WireGuard helper site admit installs for one deployment's
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

// Down stops the interface, disables it at boot and deletes its installed
// conf, which holds the site's private key: site remove's, for a site
// leaving the deployment.
func (h WGHelper) Down() error {
	return h.run("down")
}

// Sync applies the installed conf's peers to the running interface without
// restarting it.
func (h WGHelper) Sync() error {
	return h.run("sync")
}

// showFields are the fields the helper's show verb reads: each is held by
// the interface's own conf already or says how the mesh is doing.
var showFields = map[string]bool{
	"peers": true, "latest-handshakes": true, "public-key": true,
	"allowed-ips": true, "persistent-keepalive": true, "listen-port": true,
}

// showFieldList names showFields for a refusal.
const showFieldList = "peers, latest-handshakes, public-key, allowed-ips, persistent-keepalive or listen-port"

// Show reads one field of the running interface, as `wg show <iface>
// <field>` prints it.
func (h WGHelper) Show(field string) (string, error) {
	if !showFields[field] {
		return "", fmt.Errorf("the WireGuard helper shows %s, not %q", showFieldList, field)
	}
	cmd, err := h.command("show " + field)
	if err != nil {
		return "", err
	}
	out, err := h.T.Run(cmd)
	return out, withOutput(err, out)
}

// showMark starts each field's lines in ShowFields' output.
const showMark = "paisans-show:"

// ShowFields reads several fields of the running interface in one command,
// so one ssh round trip: the helper once per field, each behind a line
// that names the field. It returns each field's lines as the helper printed
// them; what came before the first such line, which can only be what sudo
// printed, goes with the first field. A field whose line is missing, such
// as after a failure, is an error.
func (h WGHelper) ShowFields(fields ...string) (map[string]string, error) {
	var parts []string
	for _, field := range fields {
		if !showFields[field] {
			return nil, fmt.Errorf("the WireGuard helper shows %s, not %q", showFieldList, field)
		}
		cmd, err := h.command("show " + field)
		if err != nil {
			return nil, err
		}
		parts = append(parts, "echo "+showMark+field, cmd)
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("no field to show")
	}
	out, err := h.T.Run(strings.Join(parts, " && "))
	if err != nil {
		return nil, withOutput(err, out)
	}
	got := map[string]string{}
	marked := map[string]bool{}
	field := fields[0]
	for _, line := range strings.SplitAfter(out, "\n") {
		if name, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), showMark); ok && slices.Contains(fields, name) {
			field, marked[name] = name, true
			continue
		}
		got[field] += line
	}
	for _, f := range fields {
		if !marked[f] {
			return nil, fmt.Errorf("the WireGuard helper's show %s printed nothing", f)
		}
	}
	return got, nil
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
