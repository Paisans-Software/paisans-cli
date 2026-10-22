package hostadmit

import (
	"fmt"
	"io"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/registry"
)

// PortTries is how many ports admit tries for a WireGuard listen port
// another deployment holds before it refuses the claim as it found it.
const PortTries = 10

// PortMoveError is a claim refused for the WireGuard listen port alone,
// with a port that is free on the host: Held is the port the request
// asked for, Free the first candidate no deployment's registry entry and
// no UDP socket holds. Its text is the refusal's, and it unwraps to the
// refusal, so a caller that does not move the port reads it as before.
// paisans init moves the site's endpoint to Free and admits it again.
type PortMoveError struct {
	Refusal    error
	Held, Free int
}

func (e *PortMoveError) Error() string { return e.Refusal.Error() }
func (e *PortMoveError) Unwrap() error { return e.Refusal }

// UDPPortsMarker is a string only the UDP listener probe contains.
const UDPPortsMarker = "paisans site admit: UDP listeners"

// udpPortsProbe reads, as root, every bound UDP socket, as listen lines.
// The registry knows only deployments, so a port some other service
// holds is found here.
var udpPortsProbe = "# " + UDPPortsMarker + "\n" + listenProbe("") + "\necho end"

// portAlone reports whether every conflict is over the WireGuard listen
// port and nothing else, which another port frees.
func portAlone(conflicts []registry.Conflict) bool {
	for _, c := range conflicts {
		for _, clash := range c.Clashes {
			if !strings.HasPrefix(clash, "WireGuard listen port") {
				return false
			}
		}
	}
	return len(conflicts) > 0
}

// freePort looks for a listen port for e's claim on a host whose
// registry is r: the held port plus a random step of 2 to 10, from
// random, then each later try a new step up from the last candidate, up
// to PortTries. A candidate above 65535, one another deployment holds, or
// one a UDP socket on the host is bound to is a failed try. It returns 0
// when every try failed.
func freePort(p *planner, r registry.Registry, id string, e registry.Entry, random io.Reader) (int, error) {
	bound, err := udpPorts(p)
	if err != nil {
		return 0, err
	}
	candidate := e.ListenPort
	for range PortTries {
		var b [1]byte
		if _, err := io.ReadFull(random, b[:]); err != nil {
			return 0, fmt.Errorf("reading a random byte for a listen port: %w", err)
		}
		candidate += 2 + int(b[0])%9
		if candidate > 65535 || bound[candidate] {
			continue
		}
		try := e
		try.ListenPort = candidate
		if len(registry.Conflicts(r, id, try)) == 0 {
			return candidate, nil
		}
	}
	return 0, nil
}

// udpPorts is every port a UDP socket on the host is bound to.
func udpPorts(p *planner) (map[int]bool, error) {
	out, err := p.t.Run(udpPortsProbe)
	if err != nil {
		return nil, fmt.Errorf("reading the host's UDP listeners: %w", shown(err))
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if lines[len(lines)-1] != "end" {
		return nil, fmt.Errorf("reading the host's UDP listeners: the answer was cut short")
	}
	bound := map[int]bool{}
	for _, line := range lines[:len(lines)-1] {
		kind, rest, _ := strings.Cut(line, " ")
		if kind == "unread" {
			return nil, fmt.Errorf("reading the host's UDP listeners: admit could not read the %s", hostText(rest))
		}
		if kind != "listen" {
			continue
		}
		if sock, ok := readSocket(rest); ok && sock.Proto == "udp" {
			bound[sock.Port] = true
		}
	}
	return bound, nil
}
