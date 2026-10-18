package hostcheck

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// composeProject is the label Compose puts on every container, volume and
// network it creates.
const composeProject = "com.docker.compose.project"

// composeService is the label naming a container's compose service.
const composeService = "com.docker.compose.service"

// anonymousLabel is the label Docker puts on an anonymous volume, and
// anonymousName the name an engine that sets no label gives one.
const anonymousLabel = "com.docker.volume.anonymous"

var anonymousName = regexp.MustCompile(`^[0-9a-f]{64}$`)

// uidField is a socket's owner as `ss -e` prints it, for any user but root.
var uidField = regexp.MustCompile(`\buid:(\d+)\b`)

// Socket is one listener `ss -Hltnue` printed.
type Socket struct {
	Proto string
	// Address is the bound address, "" for every address.
	Address string
	Port    int
	// UID is the socket's owner: 0 for root's, which ss prints no uid for,
	// and for one the kernel holds itself, as WireGuard's is.
	UID int
}

// Listener is the socket as render describes a bind, so the two compare
// with render.Listener.Overlaps.
func (s Socket) Listener() render.Listener {
	return render.Listener{Proto: s.Proto, Address: s.Address, Port: s.Port}
}

// parseSockets reads `ss -Hltnue`, a SocketLine each (ParseSocketLine).
func parseSockets(out string) ([]Socket, error) {
	var list []Socket
	for _, line := range strings.Split(out, "\n") {
		s, ok, err := ParseSocketLine(line)
		if err != nil {
			return nil, err
		}
		if ok {
			list = append(list, s.Socket)
		}
	}
	return list, nil
}

// SocketLine is one line of `ss -Hl` with both -t and -u, so its first
// field is the Netid: the socket, its local address as ss printed it, and
// its process, when -p named one.
type SocketLine struct {
	Socket
	// Local is the local address and port as ss printed them: *:80,
	// [::1]:6379, 127.0.0.53%lo:53.
	Local string
	// Process and PID are the first process ss -p names, "" and 0 for none.
	Process string
	PID     int
}

// ssProcess is the first process `ss -p` names.
var ssProcess = regexp.MustCompile(`users:\(\("([^"]+)",pid=(\d+)`)

// ParseSocketLine reads one line of `ss -Hl` with -t and -u: Netid, State,
// Recv-Q, Send-Q, Local Address:Port, Peer Address:Port, then the details,
// uid:N and users:((...)) among them. The port is after the last colon,
// which holds for "*:22", "[::]:22" and "127.0.0.53%lo:53". ok is false
// for a line that is no TCP or UDP socket.
func ParseSocketLine(line string) (s SocketLine, ok bool, err error) {
	f := strings.Fields(line)
	if len(f) < 5 || (f[0] != "tcp" && f[0] != "udp") {
		return s, false, nil
	}
	local := f[4]
	i := strings.LastIndex(local, ":")
	if i < 0 {
		return s, false, fmt.Errorf("unreadable ss address %q", local)
	}
	port, err := strconv.Atoi(local[i+1:])
	if err != nil {
		return s, false, fmt.Errorf("unreadable ss port in %q", local)
	}
	s = SocketLine{Socket: Socket{Proto: f[0], Address: BindAddress(local[:i]), Port: port}, Local: local}
	if m := uidField.FindStringSubmatch(line); m != nil {
		s.UID, _ = strconv.Atoi(m[1])
	}
	if m := ssProcess.FindStringSubmatch(line); m != nil {
		s.Process = m[1]
		s.PID, _ = strconv.Atoi(m[2])
	}
	return s, true, nil
}

// BindAddress normalises an address as ss or Docker prints it: the interface
// scope and brackets dropped, an IPv4-mapped IPv6 address read as the IPv4
// address it is ([::ffff:127.0.0.1] is 127.0.0.1), and every spelling of
// "any address" made "", as render.Listener has it.
func BindAddress(a string) string {
	if i := strings.Index(a, "%"); i >= 0 {
		a = a[:i]
	}
	a = strings.TrimSuffix(strings.TrimPrefix(a, "["), "]")
	if ip := net.ParseIP(a); ip != nil && strings.Contains(a, ":") && ip.To4() != nil {
		a = ip.To4().String()
	}
	switch a {
	case "*", "0.0.0.0", "::":
		return ""
	}
	return a
}

// Container is one container, running or stopped.
type Container struct {
	ID, Name string
	// Project is its compose project label, "" for none.
	Project string
	// Service is its compose service label, "" for none.
	Service string
	// Deployment is the id in its deployment label, "" for none.
	Deployment string
	// Image is the ID of the image it was created from, sha256:..., which
	// cleaning a host removes once no container on it uses the image.
	Image string
	// PID is its main process, 0 when it is not running.
	PID int
	// Networks are the Docker networks it is attached to, by name and
	// sorted: "host" for one in the host's network namespace.
	Networks []string
	// Bindings are the host ports it publishes. They come from its
	// configuration rather than from what is listening, because Docker
	// without its userland proxy publishes a port with no listener at all,
	// and a stopped container binds them again when it starts, at boot
	// under a restart policy.
	Bindings []render.Listener
}

type inspectedContainer struct {
	ID       string                     `json:"id"`
	Name     string                     `json:"name"`
	Image    string                     `json:"image"`
	PID      int                        `json:"pid"`
	Labels   map[string]string          `json:"labels"`
	Networks map[string]json.RawMessage `json:"networks"`
	Ports    portBindings               `json:"ports"`
}

func parseContainers(out string) ([]Container, error) {
	var list []Container
	err := eachJSONLine(out, func(line []byte) error {
		var in inspectedContainer
		if err := json.Unmarshal(line, &in); err != nil {
			return err
		}
		c := Container{ID: in.ID, Name: strings.TrimPrefix(in.Name, "/"), Project: in.Labels[composeProject], Service: in.Labels[composeService], Deployment: in.Labels[deployment.Label], Image: in.Image, PID: in.PID}
		for name := range in.Networks {
			c.Networks = append(c.Networks, name)
		}
		sort.Strings(c.Networks)
		for _, b := range bindingsOf(in.Ports) {
			// A range no claim names, as an ephemeral port is not.
			if b.Lo == b.Hi {
				c.Bindings = append(c.Bindings, render.Listener{Owner: c.Name, Proto: b.Proto, Address: b.Address, Port: b.Lo})
			}
		}
		list = append(list, c)
		return nil
	})
	return list, err
}

// Volume is one Docker volume.
type Volume struct {
	Name, Project, Deployment string
	// Anonymous is a volume Docker made for a container, not one somebody
	// named.
	Anonymous bool
}

func parseVolumes(out string) ([]Volume, error) {
	var list []Volume
	err := eachJSONLine(out, func(line []byte) error {
		var in struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		}
		if err := json.Unmarshal(line, &in); err != nil {
			return err
		}
		_, labelled := in.Labels[anonymousLabel]
		list = append(list, Volume{Name: in.Name, Project: in.Labels[composeProject], Deployment: in.Labels[deployment.Label], Anonymous: labelled || anonymousName.MatchString(in.Name)})
		return nil
	})
	return list, err
}

// Network is one Docker network and the subnets it hands out.
type Network struct {
	ID, Name, Project, Deployment string
	Subnets                       []string
}

func parseNetworks(out string) ([]Network, error) {
	var list []Network
	err := eachJSONLine(out, func(line []byte) error {
		var in struct {
			ID     string            `json:"id"`
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
			IPAM   []struct {
				Subnet string `json:"Subnet"`
			} `json:"ipam"`
		}
		if err := json.Unmarshal(line, &in); err != nil {
			return err
		}
		n := Network{ID: in.ID, Name: in.Name, Project: in.Labels[composeProject], Deployment: in.Labels[deployment.Label]}
		for _, c := range in.IPAM {
			if c.Subnet != "" {
				n.Subnets = append(n.Subnets, c.Subnet)
			}
		}
		list = append(list, n)
		return nil
	})
	return list, err
}

// eachJSONLine calls fn for every line that holds a JSON object.
func eachJSONLine(out string, fn func([]byte) error) error {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		if err := fn([]byte(line)); err != nil {
			return fmt.Errorf("unreadable line %q: %w", line, err)
		}
	}
	return nil
}

// Route is one entry of `ip -j route show table all`: every table, since
// another VPN may keep its routes in one of its own (Tailscale's 52).
type Route struct {
	Type  string `json:"type"`
	Dst   string `json:"dst"`
	Dev   string `json:"dev"`
	Table string `json:"table"`
}

func parseRoutes(out string) ([]Route, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	var list []Route
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("unreadable `ip -j route` output: %w", err)
	}
	return list, nil
}

// parseLinks reads interface names from `ip -o link`: "3: wg0: <...>", or
// "7: veth1@if6: <...>" for one end of a pair.
func parseLinks(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimSuffix(f[1], ":"), "@")
		names = append(names, name)
	}
	return names
}

// parsePackages names where Docker came from, from dpkg-query's
// "<package> <status>" lines and `snap list docker`'s table.
func parsePackages(out string) []string {
	var found []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 0:
		case (f[0] == "docker-ce" || f[0] == "docker.io") && strings.HasSuffix(strings.TrimSpace(line), "ok installed"):
			found = append(found, f[0])
		case f[0] == "docker" && len(f) > 1:
			found = append(found, "snap")
		}
	}
	return found
}

// portBindings is a container's HostConfig.PortBindings as docker inspect
// prints it: {"443/tcp":[{"HostIp":"","HostPort":"8443"}]}.
type portBindings map[string][]struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

// Binding is one host port, or range of ports from Lo to Hi, a container
// publishes, with its protocol and its bound address ("" for every
// address).
type Binding struct {
	Proto, Address string
	Lo, Hi         int
}

// ParseBindings reads a container's PortBindings as `docker inspect
// --format '{{json .HostConfig.PortBindings}}'` prints them.
func ParseBindings(raw string) ([]Binding, error) {
	var in portBindings
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return nil, err
	}
	return bindingsOf(in), nil
}

// bindingsOf is in's host ports, in the order of their container ports. A
// binding with no host port, which Docker picks when the container
// starts, is an ephemeral port none names, and is left out.
func bindingsOf(in portBindings) []Binding {
	ports := make([]string, 0, len(in))
	for p := range in {
		ports = append(ports, p)
	}
	sort.Strings(ports)
	var out []Binding
	for _, p := range ports {
		proto := "tcp"
		if _, after, ok := strings.Cut(p, "/"); ok {
			proto = after
		}
		for _, b := range in[p] {
			from, to, ranged := strings.Cut(b.HostPort, "-")
			lo, err := strconv.Atoi(from)
			if err != nil || lo <= 0 {
				continue
			}
			hi := lo
			if ranged {
				if hi, err = strconv.Atoi(to); err != nil || hi < lo {
					continue
				}
			}
			out = append(out, Binding{Proto: proto, Address: BindAddress(b.HostIP), Lo: lo, Hi: hi})
		}
	}
	return out
}
