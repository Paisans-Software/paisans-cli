package hostadmit

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/registry"
)

// PortTries is how many ports admit tries for a WireGuard listen port
// another deployment holds before it refuses the claim as it found it.
const PortTries = 10

// PortMoveError is a claim refused for the WireGuard listen port alone,
// with a port that is free on the host: Held is the port the request
// asked for, Free the first candidate no deployment's registry entry and
// nothing else on the host holds (see udpPorts). Its text is the
// refusal's, and it unwraps to the refusal, so a caller that does not move
// the port reads it as before.
// paisans init moves the site's endpoint to Free and admits it again.
type PortMoveError struct {
	Refusal    error
	Held, Free int
}

func (e *PortMoveError) Error() string { return e.Refusal.Error() }
func (e *PortMoveError) Unwrap() error { return e.Refusal }

// UDPPortsMarker is a string only the UDP listener probe contains.
const UDPPortsMarker = "paisans site admit: UDP listeners"

// udpPortsProbe reads, as root, everything that can take a UDP port
// with no deployment's registry entry: every bound socket (listen lines),
// and what receives traffic with no socket of its own, the ports the
// system Docker publishes and the DNAT and redirect rules (see natProbe).
// The registry knows only deployments, so a port some other service holds
// is found here.
var udpPortsProbe = "# " + UDPPortsMarker + "\n" + listenProbe("") + natProbe + "\necho end"

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
// one something else on the host holds (udpPorts) is a failed try. It returns 0
// when every try failed.
func freePort(p *planner, r registry.Registry, id string, e registry.Entry, random io.Reader) (int, error) {
	held, err := udpPorts(p)
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
		if candidate > 65535 || held(candidate) {
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

// udpPorts is what holds a UDP port on the host outside the registry, as
// one test of a port: a bound UDP socket, a UDP port a running container
// publishes, or one a stopped container with restart: always takes again,
// and any port a DNAT or redirect rule matches, of either protocol, since
// a rule's protocol is not read. A negated rule holds every port it does
// not name, and a rule with no port match holds every port. A host with
// neither nft nor iptables-save has a ruleset admit cannot read, so its
// port is never moved: no reader is no proof of no rules. Anything admit cannot read (a listener or ruleset read that
// failed, a rule's ports or a stopped container's bindings it cannot
// parse) is an error: a port chosen past it may be held.
func udpPorts(p *planner) (func(int) bool, error) {
	out, err := p.t.Run(udpPortsProbe)
	if err != nil {
		return nil, fmt.Errorf("reading what holds UDP ports on the host: %w", shown(err))
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if lines[len(lines)-1] != "end" {
		return nil, fmt.Errorf("reading what holds UDP ports on the host: the answer was cut short")
	}
	var spans []span
	var negated [][]span
	for _, line := range lines[:len(lines)-1] {
		kind, rest, _ := strings.Cut(line, " ")
		switch kind {
		case "unread":
			return nil, fmt.Errorf("reading what holds UDP ports on the host: admit could not read the %s", hostText(rest))
		case "listen":
			if sock, ok := readSocket(rest); ok && sock.Proto == "udp" {
				spans = append(spans, span{lo: sock.Port, hi: sock.Port})
			}
		case "container":
			if f := strings.Split(rest, "\t"); len(f) >= 2 {
				spans = append(spans, udpOnly(publishes(f[1]))...)
			}
		case "stopped":
			f := strings.Fields(rest)
			if len(f) < 3 || f[1] != "always" {
				continue
			}
			list, err := bindings(strings.Join(f[2:], " "))
			if err != nil {
				return nil, fmt.Errorf("reading what holds UDP ports on the host: admit could not read the ports your Docker's stopped container %s binds", hostText(strings.TrimPrefix(f[0], "/")))
			}
			spans = append(spans, udpOnly(list)...)
		case "dnat":
			matches := rulePorts(rest)
			if len(matches) == 0 {
				// No port match: the rule forwards every port.
				negated = append(negated, nil)
			}
			for _, m := range matches {
				switch {
				case m.unread:
					return nil, fmt.Errorf("reading what holds UDP ports on the host: admit could not read the ports of a NAT rule in %s", hostText(withoutCounters(rest)))
				case m.negated:
					negated = append(negated, m.spans)
				default:
					spans = append(spans, m.spans...)
				}
			}
		}
	}
	in := func(list []span, port int) bool {
		return slices.ContainsFunc(list, func(s span) bool { return s.has(port) })
	}
	return func(port int) bool {
		if in(spans, port) {
			return true
		}
		return slices.ContainsFunc(negated, func(list []span) bool { return !in(list, port) })
	}, nil
}

// udpOnly is the spans of list published for UDP.
func udpOnly(list []span) []span {
	var out []span
	for _, s := range list {
		if s.proto == "udp" {
			out = append(out, s)
		}
	}
	return out
}
