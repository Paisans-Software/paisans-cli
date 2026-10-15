package config

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ParseMeshSubnet is subnet as a network, or what is wrong with how it is
// written: not CIDR, not IPv4, a host address rather than the network, or
// any spelling but the canonical one net.IPNet prints, so a leading zero or
// an IPv4-mapped IPv6 form never reaches a host as a second spelling of one
// mesh. The loader holds mesh.subnet to it and a request its mesh_subnet.
func ParseMeshSubnet(subnet string) (*net.IPNet, string) {
	ip, network, err := net.ParseCIDR(subnet)
	switch {
	case err != nil:
		return nil, "is not a network in CIDR notation"
	case ip.To4() == nil:
		return nil, "is not IPv4"
	case !ip.Equal(network.IP):
		return nil, "is a host address inside a network, not the network itself, " + network.String()
	case network.String() != subnet:
		return nil, "is not written in its canonical form, " + network.String()
	}
	return network, ""
}

// MeshMinPrefix is the shortest prefix mesh.subnet may have: no larger than a
// /24, which is what init picks. The hosts' firewalls trust the whole mesh,
// so a larger one could cover an owner's LAN.
const MeshMinPrefix = 24

// reservedMesh is what a mesh subnet may not overlap. An admitted host's
// egress table accepts the mesh subnet before it drops loopback and
// link-local, so a subnet over either would let the deployment reach the
// owner's local services and the cloud metadata address; this network,
// multicast and the reserved range route nothing a mesh could use.
var reservedMesh = []string{"0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/3"}

// MeshReservedOverlaps is each range in reservedMesh that mesh overlaps, in
// that order, empty when it overlaps none. The loader refuses mesh.subnet
// and a request refuses mesh_subnet by it.
func MeshReservedOverlaps(mesh *net.IPNet) []string {
	var out []string
	for _, r := range reservedMesh {
		_, n, _ := net.ParseCIDR(r)
		if n.Contains(mesh.IP) || mesh.Contains(n.IP) {
			out = append(out, r)
		}
	}
	return out
}

// CheckMeshSubnet is the one rule a mesh subnet is held to, by the loader
// for mesh.subnet, by a request for mesh_subnet and by the WireGuard
// helper's renderer: subnet as a network, and what is wrong with it. A
// subnet ParseMeshSubnet refuses gives its phrase alone and no network;
// otherwise each problem is one phrase, "is larger than a /24, ..." for a
// prefix shorter than MeshMinPrefix and "overlaps 127.0.0.0/8, ..." for each
// reserved range it overlaps, each saying why. No problems means the subnet may be a mesh.
func CheckMeshSubnet(subnet string) (*net.IPNet, []string) {
	network, p := ParseMeshSubnet(subnet)
	if p != "" {
		return nil, []string{p}
	}
	var problems []string
	if ones, _ := network.Mask.Size(); ones < MeshMinPrefix {
		problems = append(problems, fmt.Sprintf("is larger than a /%d, the largest a mesh may be, so it cannot take in an owner's LAN", MeshMinPrefix))
	}
	for _, r := range MeshReservedOverlaps(network) {
		problems = append(problems, "overlaps "+r+", which the deployment must never reach")
	}
	return network, problems
}

// IsNetworkOrBroadcast reports whether a is mesh's network or broadcast
// address, which no site may hold: the first names the subnet and the last
// reaches every host on it.
func IsNetworkOrBroadcast(a net.IP, mesh *net.IPNet) bool {
	a, network := a.To4(), mesh.IP.To4()
	if a == nil || network == nil {
		return false
	}
	mask := mesh.Mask[len(mesh.Mask)-4:]
	last := make(net.IP, 4)
	for i := range last {
		last[i] = network[i] | ^mask[i]
	}
	return a.Equal(network) || a.Equal(last)
}

// ParseEndpointPort is the port of an endpoint written host:port.
func ParseEndpointPort(endpoint string) (int, error) {
	_, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return 0, fmt.Errorf("is not host:port (%v)", err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("has port %q, which is not a number from 1 to 65535", port)
	}
	return n, nil
}

// DefaultWireGuardPort is the port a site is dialled on when it declares a
// public_address and no endpoint port: WireGuard's own conventional port.
const DefaultWireGuardPort = 51820

// DialEndpoint is the host:port every other site's WireGuard dials to reach
// this one, or "" when nothing dials it. It is the one place the endpoint is
// read, so the render, the firewall, the port checks and validate cannot
// disagree about who is dialled where.
//
//   - public_address declared: that address, on the endpoint's port, or on
//     DefaultWireGuardPort when no endpoint is written. An endpoint naming a
//     host is not dialled: its name still gets a DNS record (`paisans dns
//     init`), but the mesh never waits on DNS for a site whose address is
//     declared. An endpoint written as an address is dialled as written.
//   - no public_address: the endpoint as written, a dynamic DNS name or a
//     router's address; nothing when there is none, and the site only dials
//     out. public_address6 alone derives nothing: a peer with no IPv6 route
//     could never reach it.
//
// It assumes the endpoint was checked; a malformed one dials nothing.
func (s Site) DialEndpoint() string {
	if s.Endpoint == "" {
		if s.PublicAddress == "" {
			return ""
		}
		return net.JoinHostPort(s.PublicAddress, strconv.Itoa(DefaultWireGuardPort))
	}
	port, err := ParseEndpointPort(s.Endpoint)
	if err != nil {
		return ""
	}
	host, _, _ := net.SplitHostPort(s.Endpoint)
	switch {
	case net.ParseIP(host) != nil:
		return s.Endpoint
	case s.PublicAddress != "":
		return net.JoinHostPort(s.PublicAddress, strconv.Itoa(port))
	case host == "":
		return ""
	default:
		return s.Endpoint
	}
}

// ListenPort is the UDP port this site's WireGuard interface listens on: the
// port of its DialEndpoint, since that is the port every other site dials. A
// site that is never dialled has no fixed port: its interface is rendered
// without ListenPort and WireGuard picks one ("Optional; if not specified,
// chosen randomly", wireguard-tools v1.0.20210914, src/man/wg.8). That is
// also what lets two deployments share a host behind NAT without choosing
// ports. Zero means none.
func (s Site) ListenPort() int {
	endpoint := s.DialEndpoint()
	if endpoint == "" {
		return 0
	}
	port, err := ParseEndpointPort(endpoint)
	if err != nil {
		return 0
	}
	return port
}

// PeersDirectly reports whether this site and another exchange WireGuard
// traffic without a relay: one of them is dialled (DialEndpoint), so the
// other dials it. Two sites that are never dialled never peer at all; their
// traffic goes through the first dialled site, in sorted order, which is the
// one path between them and is gone when that site is. The render lays out
// exactly this, and validate's quorum rule reads the same predicate, so the
// two cannot disagree about which sites reach each other.
func (s Site) PeersDirectly(other Site) bool {
	return s.DialEndpoint() != "" || other.DialEndpoint() != ""
}

// SetMesh writes mesh.subnet and the named sites' addresses into the
// declaration at path, and leaves every other byte as it was: each value is
// replaced where it stands, in the quoting it was written in, so comments,
// order and spacing survive. A missing mesh.subnet is added: inside an
// existing `mesh:` block, or as a new block after the top level `id:` line.
// It is `paisans init`'s write, and refuses a layout it cannot edit in place
// rather than reformatting the file.
func SetMesh(path, subnet string, addresses map[string]string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	out, err := setMesh(data, subnet, addresses)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return replaceFile(path, out)
}

// edit replaces data[start:end] with text.
type edit struct {
	start, end int
	text       string
}

func setMesh(data []byte, subnet string, addresses map[string]string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the top level is not a mapping")
	}
	top := doc.Content[0]
	lines := lineStarts(data)
	var edits []edit

	meshKey, mesh := lookup(top, "mesh")
	switch {
	case mesh == nil:
		_, id := lookup(top, "id")
		if id == nil {
			return nil, fmt.Errorf("no top level `id:` line to write mesh.subnet after. Run `paisans init` on a file with `version: 1` as its first key")
		}
		at := lines.next(id.Line)
		edits = append(edits, edit{at, at, "mesh:\n  subnet: " + subnet + "\n"})
	case mesh.Kind == yaml.ScalarNode && mesh.Tag == "!!null":
		at := lines.next(meshKey.Line)
		if mesh.Line != meshKey.Line {
			return nil, fmt.Errorf("mesh: is written in a form this command cannot edit in place. Write `mesh:` on a line of its own")
		}
		// `mesh:` with nothing after it: the subnet goes on the next line.
		end := lines.start(meshKey.Line) + len(strings.TrimRight(lines.text(data, meshKey.Line), "\n"))
		edits = append(edits, edit{end, at, "\n  subnet: " + subnet + "\n"})
	case mesh.Kind != yaml.MappingNode || mesh.Style&yaml.FlowStyle != 0:
		return nil, fmt.Errorf("mesh: is written in a form this command cannot edit in place. Write it as a block, `mesh:` with `subnet:` indented under it")
	default:
		if _, value := lookup(mesh, "subnet"); value != nil {
			e, err := replaceScalar(data, lines, value, subnet)
			if err != nil {
				return nil, fmt.Errorf("mesh.subnet: %w", err)
			}
			edits = append(edits, e)
		} else if len(mesh.Content) > 0 {
			first := mesh.Content[0]
			at := lines.start(first.Line)
			edits = append(edits, edit{at, at, strings.Repeat(" ", first.Column-1) + "subnet: " + subnet + "\n"})
		} else {
			return nil, fmt.Errorf("mesh: is empty in a form this command cannot edit in place")
		}
	}

	_, sites := lookup(top, "sites")
	names := make([]string, 0, len(addresses))
	for name := range addresses {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var value *yaml.Node
		if sites != nil && sites.Kind == yaml.MappingNode {
			if _, site := lookup(sites, name); site != nil && site.Kind == yaml.MappingNode {
				_, value = lookup(site, "address")
			}
		}
		if value == nil {
			return nil, fmt.Errorf("sites.%s.address: not found as a value this command can replace", name)
		}
		e, err := replaceScalar(data, lines, value, addresses[name])
		if err != nil {
			return nil, fmt.Errorf("sites.%s.address: %w", name, err)
		}
		edits = append(edits, e)
	}

	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := append([]byte(nil), data...)
	for _, e := range edits {
		var b bytes.Buffer
		b.Write(out[:e.start])
		b.WriteString(e.text)
		b.Write(out[e.end:])
		out = b.Bytes()
	}
	return out, nil
}

// lookup finds key in a mapping node: the key node and its value.
func lookup(m *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

// replaceScalar replaces a scalar's text where it stands, keeping its
// quoting. The text found there must be the value as the parser read it, so
// an escape or a multi-line scalar is refused rather than half replaced.
func replaceScalar(data []byte, lines lineIndex, n *yaml.Node, value string) (edit, error) {
	if n.Kind != yaml.ScalarNode {
		return edit{}, fmt.Errorf("is not a single value")
	}
	var text string
	switch n.Style {
	case 0:
		text = n.Value
	case yaml.DoubleQuotedStyle:
		text, value = `"`+n.Value+`"`, `"`+value+`"`
	case yaml.SingleQuotedStyle:
		text, value = "'"+n.Value+"'", "'"+value+"'"
	default:
		return edit{}, fmt.Errorf("is written in a style this command cannot edit in place. Write it plain, on one line")
	}
	line := lines.text(data, n.Line)
	runes := []rune(line)
	if n.Column-1 > len(runes) {
		return edit{}, fmt.Errorf("could not be found on line %d", n.Line)
	}
	start := lines.start(n.Line) + len(string(runes[:n.Column-1]))
	if !bytes.HasPrefix(data[start:], []byte(text)) {
		return edit{}, fmt.Errorf("on line %d is not written as %s, so it was not replaced", n.Line, text)
	}
	return edit{start, start + len(text), value}, nil
}

// lineIndex is the byte offset each line of a file starts at.
type lineIndex []int

func lineStarts(data []byte) lineIndex {
	out := lineIndex{0}
	for i, b := range data {
		if b == '\n' {
			out = append(out, i+1)
		}
	}
	return out
}

// start is where 1-based line n begins.
func (l lineIndex) start(n int) int { return l[n-1] }

// next is where the line after line n begins.
func (l lineIndex) next(n int) int {
	if n < len(l) {
		return l[n]
	}
	return l[len(l)-1]
}

// text is line n with its newline.
func (l lineIndex) text(data []byte, n int) string {
	end := len(data)
	if n < len(l) {
		end = l[n]
	}
	return string(data[l[n-1]:end])
}

// replaceFile writes data over path through a temporary file beside it, with
// path's permissions, so a failed write never leaves half a declaration.
func replaceFile(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
