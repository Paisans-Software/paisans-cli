package apply

import "fmt"

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

// Path is the helper for this interface: HelperDir/wg-<iface>.
func (h WGHelper) Path() string { return HelperDir + "/wg-" + h.Iface }

func (h WGHelper) command(verb string) string {
	return "sudo -n " + shellQuote(h.Path()) + " " + verb
}

// Put sends conf on stdin, for the helper to check and install as the
// interface's wg-quick file.
func (h WGHelper) Put(conf []byte) error {
	_, err := h.T.RunInput(h.command("put"), string(conf))
	return err
}

// Up enables and starts the interface.
func (h WGHelper) Up() error {
	_, err := h.T.Run(h.command("up"))
	return err
}

// Restart restarts the interface.
func (h WGHelper) Restart() error {
	_, err := h.T.Run(h.command("restart"))
	return err
}

// Sync applies the installed conf's peers to the running interface without
// restarting it.
func (h WGHelper) Sync() error {
	_, err := h.T.Run(h.command("sync"))
	return err
}

// showFields are the fields the helper's show verb reads.
var showFields = map[string]bool{"peers": true, "latest-handshakes": true, "public-key": true}

// Show reads one field of the running interface, as `wg show <iface>
// <field>` prints it.
func (h WGHelper) Show(field string) (string, error) {
	if !showFields[field] {
		return "", fmt.Errorf("the WireGuard helper shows peers, latest-handshakes or public-key, not %q", field)
	}
	return h.T.Run(h.command("show " + field))
}

// Log is the interface unit's last journal lines.
func (h WGHelper) Log() (string, error) { return h.T.Run(h.command("log")) }
