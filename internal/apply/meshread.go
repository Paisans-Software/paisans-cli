package apply

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// wireguardProbe reads d's interface from the kernel, in one command:
// `ip -br addr` for its state and addresses, then `ip -o link` for its MTU.
// It prints nothing when the interface does not exist, and exits zero
// either way, so that an absent interface cannot be confused with ssh
// failing. It reads the kernel rather than the unit, because an interface
// somebody brought up with a bare `wg-quick up` is up for every service
// binding to it, and `systemctl enable --now` on top of it would fail on an
// interface that already exists. Neither read needs privilege.
func wireguardProbe(iface string) string {
	return "ip -br addr show dev " + iface + " 2>/dev/null; ip -o link show dev " + iface + " 2>/dev/null; true"
}

// meshLink is an interface as wireguardProbe read it.
type meshLink struct {
	// exists is whether the kernel has the interface at all.
	exists bool
	// state is its operational state as ip prints it: UNKNOWN for an up
	// WireGuard link, which has no carrier to report, and DOWN for one
	// that is down.
	state string
	// addresses are its addresses with their prefix (Eg: 10.44.0.1/24).
	addresses []string
	// mtu is its MTU as ip prints it, empty when ip printed none.
	mtu string
}

// readLink parses wireguardProbe's output for iface: the `ip -br addr`
// line starts with the name, the `ip -o link` line with its index and the
// name and a colon.
func readLink(iface, out string) meshLink {
	var link meshLink
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) >= 2 && fields[0] == iface:
			link.exists = true
			link.state = fields[1]
			link.addresses = fields[2:]
		case len(fields) >= 2 && fields[1] == iface+":":
			link.exists = true
			if i := slices.Index(fields, "mtu"); i >= 0 && i+1 < len(fields) {
				link.mtu = fields[i+1]
			}
		}
	}
	return link
}

// probeLink runs wireguardProbe for iface and parses it.
func probeLink(iface string, t Transport) (meshLink, error) {
	out, err := t.Run(wireguardProbe(iface))
	if err != nil {
		return meshLink{}, fmt.Errorf("reading %s: %w", iface, err)
	}
	return readLink(iface, out), nil
}

// runningFields are what runningStep reads through the helper.
var runningFields = []string{"public-key", "listen-port", "peers", "allowed-ips", "persistent-keepalive"}

// runningStep is what an up interface whose file did not change still
// needs. The helper's put and its sync or restart are separate commands
// from writing the file, so a run that stopped between them leaves the file
// as rendered and the interface on what it ran before. Read back, the
// interface is restarted when it lacks the file's Address or runs another
// MTU, since only wg-quick applies those; it is synced when its key, its
// ListenPort, its peers or a peer's AllowedIPs or PersistentKeepalive
// differ; otherwise it needs nothing. The link was read with ip already;
// the rest is one command through the helper's show.
//
// A peer's Endpoint is not read back. wg reports the address a peer last
// sent from, resolved and after any NAT, not the name or address in the
// file, so the two differ on a mesh that is working. A run stopped after a
// change to nothing but an Endpoint is therefore completed by the next
// change to the mesh file, not by the next apply.
func runningStep(site string, d deployment.Deployment, content string, link meshLink, t Transport) (WireGuardStep, error) {
	if address := wireguardAddress(content); address != "" && !slices.Contains(link.addresses, address) {
		return WireGuardRestart, nil
	}
	if mtu := fileValue(content, "MTU"); mtu != "" && mtu != link.mtu {
		return WireGuardRestart, nil
	}
	h := WGHelper{T: t, Iface: d.Interface()}
	shown, err := h.ShowFields(runningFields...)
	if err != nil {
		return WireGuardNone, helperReadProblem(site, h, err)
	}
	running, err := readRunning(site, shown)
	if err != nil {
		return WireGuardNone, err
	}
	file, err := readFile(content)
	if err != nil {
		return WireGuardNone, fmt.Errorf("%s's mesh file: %w", site, err)
	}
	if !file.equal(running) {
		return WireGuardSync, nil
	}
	return WireGuardNone, nil
}

// meshPeer is what a sync applies to one peer and runningStep compares.
type meshPeer struct {
	// allowed is its AllowedIPs as networks, sorted.
	allowed []string
	// keepalive is its PersistentKeepalive in seconds, or off.
	keepalive string
}

// meshRunning is what a sync applies to an interface: its key, its
// ListenPort (empty for a file with none, whose port the kernel picks),
// and its peers by public key.
type meshRunning struct {
	key   string
	port  string
	peers map[string]meshPeer
}

// equal reports whether running carries everything the file f sets.
func (f meshRunning) equal(running meshRunning) bool {
	if f.key != running.key || (f.port != "" && f.port != running.port) || len(f.peers) != len(running.peers) {
		return false
	}
	for key, peer := range f.peers {
		other, ok := running.peers[key]
		if !ok || peer.keepalive != other.keepalive || !slices.Equal(peer.allowed, other.allowed) {
			return false
		}
	}
	return true
}

// readFile is what a WireGuard file sets that a sync applies.
func readFile(content string) (meshRunning, error) {
	out := meshRunning{port: fileValue(content, "ListenPort"), peers: map[string]meshPeer{}}
	private := fileValue(content, "PrivateKey")
	if private == "" {
		return out, fmt.Errorf("it has no PrivateKey")
	}
	key, err := render.PublicKey(private)
	if err != nil {
		return out, err
	}
	out.key = key
	// Each [Peer] section is collected whole and assigned to its PublicKey
	// when the section ends, since its keys may come in any order.
	inPeer, public, peer := false, "", meshPeer{keepalive: "off"}
	end := func() {
		if inPeer && public != "" {
			slices.Sort(peer.allowed)
			out.peers[public] = peer
		}
		inPeer, public, peer = false, "", meshPeer{keepalive: "off"}
	}
	for _, line := range strings.Split(content, "\n") {
		if section := strings.TrimSpace(line); strings.HasPrefix(section, "[") {
			end()
			inPeer = section == "[Peer]"
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || !inPeer {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "PublicKey":
			public = v
		case "AllowedIPs":
			for _, cidr := range strings.Split(v, ",") {
				peer.allowed = append(peer.allowed, network(strings.TrimSpace(cidr)))
			}
		case "PersistentKeepalive":
			peer.keepalive = keepalive(v)
		}
	}
	end()
	return out, nil
}

// fileValue is the value of the first line of a WireGuard file that sets
// key, empty when none does.
func fileValue(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// network is a range as the kernel holds it, with the address's host bits
// cleared (Eg: 10.44.0.7/24 is 10.44.0.0/24), or s as it is when it is not
// a range.
func network(s string) string {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		return s
	}
	return n.String()
}

// keepalive is a PersistentKeepalive as wg prints it: off for none or 0.
func keepalive(s string) string {
	if s == "" || strings.TrimLeft(s, "0") == "" {
		return "off"
	}
	return strings.TrimLeft(s, "0")
}

// wgKey is a WireGuard key as wg prints one: 32 bytes in base64, the shape
// the helper's put checks a key for.
var wgKey = regexp.MustCompile(`^[A-Za-z0-9+/]{43}=$`)

// wgPort is a port as wg prints one.
var wgPort = regexp.MustCompile(`^[0-9]{1,5}$`)

// showLines is one field of the helper's output, line by line, without
// blank lines and without what sudo prints before running a command it
// will run anyway (Eg: "sudo: unable to resolve host vm"). sudo writes to
// stderr, which the transport merges into the output; a line that is a
// refusal never gets here, since sudo then exits non zero.
func showLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "sudo: ") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// badShow is a line of the helper's show that is not field's shape,
// quoted as the host printed it, made visible.
func badShow(site, field, line, shape string) error {
	return fmt.Errorf(`%s: the WireGuard helper's show %s printed "%s", not %s`, site, field, ui.Visible(line), shape)
}

// ReadMeshKey parses the helper's show public-key, strictly: the one key it
// printed, or empty for an interface with none ("(none)"). sudo's warnings
// are not read as the key, and anything else is an error that quotes it,
// never a key that matches nothing.
func ReadMeshKey(site, out string) (string, error) {
	keys := showLines(out)
	switch {
	case len(keys) == 1 && keys[0] == "(none)":
		return "", nil
	case len(keys) == 1 && wgKey.MatchString(keys[0]):
		return keys[0], nil
	case len(keys) == 0:
		return "", fmt.Errorf("%s: the WireGuard helper's show public-key printed no key", site)
	default:
		return "", badShow(site, "public-key", strings.Join(keys, "\n"), "one key")
	}
}

// ReadMeshPeers parses the helper's show peers, strictly: one key per line,
// without sudo's warnings, and anything else is an error that quotes it.
func ReadMeshPeers(site, out string) ([]string, error) {
	var peers []string
	for _, line := range showLines(out) {
		if !wgKey.MatchString(line) {
			return nil, badShow(site, "peers", line, "a key")
		}
		peers = append(peers, line)
	}
	return peers, nil
}

// readRunning parses what the helper showed of the running interface,
// strictly: every line must be its field's shape, and anything else is an
// error that quotes it rather than a difference that plans a sync.
func readRunning(site string, shown map[string]string) (meshRunning, error) {
	out := meshRunning{peers: map[string]meshPeer{}}

	key, err := ReadMeshKey(site, shown["public-key"])
	if err != nil {
		return out, err
	}
	out.key = key

	ports := showLines(shown["listen-port"])
	if len(ports) != 1 || !wgPort.MatchString(ports[0]) {
		return out, badShow(site, "listen-port", strings.Join(ports, "\n"), "one port")
	}
	out.port = ports[0]

	peers, err := ReadMeshPeers(site, shown["peers"])
	if err != nil {
		return out, err
	}
	for _, peer := range peers {
		out.peers[peer] = meshPeer{keepalive: "off"}
	}

	for _, line := range showLines(shown["allowed-ips"]) {
		fields := strings.Fields(line)
		if len(fields) < 2 || !wgKey.MatchString(fields[0]) {
			return out, badShow(site, "allowed-ips", line, "a key and its ranges")
		}
		peer, ok := out.peers[fields[0]]
		if !ok {
			return out, badShow(site, "allowed-ips", line, "a peer show peers listed")
		}
		if len(fields) == 2 && fields[1] == "(none)" {
			continue
		}
		for _, cidr := range fields[1:] {
			_, n, err := net.ParseCIDR(cidr)
			if err != nil {
				return out, badShow(site, "allowed-ips", line, "a key and its ranges")
			}
			peer.allowed = append(peer.allowed, n.String())
		}
		slices.Sort(peer.allowed)
		out.peers[fields[0]] = peer
	}

	for _, line := range showLines(shown["persistent-keepalive"]) {
		fields := strings.Fields(line)
		if len(fields) != 2 || !wgKey.MatchString(fields[0]) || (fields[1] != "off" && !wgPort.MatchString(fields[1])) {
			return out, badShow(site, "persistent-keepalive", line, "a key and off or seconds")
		}
		peer, ok := out.peers[fields[0]]
		if !ok {
			return out, badShow(site, "persistent-keepalive", line, "a peer show peers listed")
		}
		peer.keepalive = keepalive(fields[1])
		out.peers[fields[0]] = peer
	}
	return out, nil
}
