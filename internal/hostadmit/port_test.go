package hostadmit_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// portHolders is a registry of one other deployment per port, each
// holding that WireGuard listen port and nothing else vm's claim needs.
func portHolders(roles string, ports ...int) string {
	r := registry.Registry{Version: registry.Version, Deployments: map[string]registry.Entry{}}
	for i, port := range ports {
		token := fmt.Sprintf("%04x", 0xa000+i)
		r.Deployments[fmt.Sprintf("%s0000-0000-4000-8000-000000000000", token)] = registry.Entry{
			Token: token, Root: "/srv/paisans/" + token, Domain: "example.net", Site: "edge",
			ClaimedAt: "2026-10-01T09:00:00Z", Interface: "psns-" + token, ListenPort: port,
			Subnet: fmt.Sprintf("10.%d.0.0/24", 100+i), Address: fmt.Sprintf("10.%d.0.1", 100+i), Roles: roles,
			Build: version.Build{Version: "0.3.0-dev", Commit: "v0.3.0"},
		}
	}
	data, err := registry.Encode(r)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// udpListeners is the UDP probe's answer: one socket per port, held by a
// process that is no deployment's.
func udpListeners(ports ...int) string {
	var b strings.Builder
	for _, port := range ports {
		fmt.Fprintf(&b, "listen udp UNCONN 0 0 0.0.0.0:%d 0.0.0.0:* users:((\"openvpn\",pid=812,fd=6)) uid:0 ino:1 sk:1\n", port)
	}
	b.WriteString("end\n")
	return b.String()
}

// buildMoving builds vm's admit with a random source for the listen port
// on a host whose registry is reg and whose UDP listeners are udp.
func buildMoving(t *testing.T, reg, udp string, port int, random []byte) (error, *bytes.Reader, *fakeHost) {
	t.Helper()
	h := home().file(registry.Path, reg).answer(hostadmit.UDPPortsMarker, udp)
	in := input(t, "vm", hostadmit.Flags{})
	if port != 0 {
		in.Request.ListenPort = port
	}
	r := bytes.NewReader(random)
	in.MovePort = r
	_, err := hostadmit.Build(h, in)
	return err, r, h
}

// A claim refused for the listen port alone, with the ports above it
// free, finds the first free candidate: the held port plus a random step
// of 2 to 10.
func TestAListenPortHeldFindsAFreeOne(t *testing.T) {
	err, _, _ := buildMoving(t, portHolders("apps", 51820), udpListeners(), 0, []byte{3})
	var moved *hostadmit.PortMoveError
	if !errors.As(err, &moved) {
		t.Fatalf("Build = %v, want a PortMoveError", err)
	}
	if moved.Held != 51820 || moved.Free != 51825 {
		t.Errorf("moved %d to %d, want 51820 to 51825", moved.Held, moved.Free)
	}
	var c registry.Conflict
	if !errors.As(err, &c) || !strings.Contains(err.Error(), "WireGuard listen port 51820/udp") {
		t.Errorf("the error %v does not carry the refusal", err)
	}
}

// A candidate something other than a deployment listens on is a failed
// try: the registry knows only deployments.
func TestAListenPortSkipsAPortAnotherProcessHolds(t *testing.T) {
	err, _, _ := buildMoving(t, portHolders("apps", 51820), udpListeners(51822), 0, []byte{0, 0})
	var moved *hostadmit.PortMoveError
	if !errors.As(err, &moved) || moved.Free != 51824 {
		t.Fatalf("Build = %v, want a move to 51824 past the listener on 51822", err)
	}
}

// Ten held candidates in a row give up with the refusal as it was.
func TestAListenPortGivesUpAfterTenTries(t *testing.T) {
	ports := []int{51820}
	for i := 1; i <= 10; i++ {
		ports = append(ports, 51820+2*i)
	}
	err, _, _ := buildMoving(t, portHolders("apps", ports...), udpListeners(), 0, make([]byte, 10))
	if err == nil || errors.As(err, new(*hostadmit.PortMoveError)) || !strings.Contains(err.Error(), "WireGuard listen port 51820/udp") {
		t.Errorf("Build = %v, want the refusal for 51820", err)
	}
}

// A candidate above 65535 is a failed try.
func TestAListenPortStaysInRange(t *testing.T) {
	err, _, _ := buildMoving(t, portHolders("apps", 65534), udpListeners(), 65534, make([]byte, 10))
	if err == nil || errors.As(err, new(*hostadmit.PortMoveError)) || !strings.Contains(err.Error(), "WireGuard listen port 65534/udp") {
		t.Errorf("Build = %v, want the refusal for 65534", err)
	}
}

// A listen port held beside a role is refused as it was, with no try: no
// port frees the host while the role is held.
func TestAListenPortHeldBesideARoleIsNotMoved(t *testing.T) {
	h := home().file(registry.Path, portHolders("gateway", 51820)).answer(hostadmit.UDPPortsMarker, udpListeners())
	in := input(t, "home-a", gateway)
	r := bytes.NewReader([]byte{3})
	in.MovePort = r
	_, err := hostadmit.Build(h, in)
	if err == nil || errors.As(err, new(*hostadmit.PortMoveError)) || !strings.Contains(err.Error(), "ports 80 and 443 to one deployment") || strings.Contains(err.Error(), "another port") {
		t.Errorf("Build = %v, want the gateway refusal alone", err)
	}
	if r.Len() != 1 {
		t.Errorf("a random byte was read for a refusal no port frees")
	}
	for _, c := range h.ran {
		if strings.Contains(c, hostadmit.UDPPortsMarker) {
			t.Errorf("the UDP listeners were read for a refusal no port frees")
		}
	}
}

// Without a random source, admit never moves the port.
func TestAListenPortIsMovedOnlyWhenAsked(t *testing.T) {
	h := home().file(registry.Path, portHolders("apps", 51820))
	_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err == nil || errors.As(err, new(*hostadmit.PortMoveError)) || !strings.Contains(err.Error(), "Give this site's endpoint another port") {
		t.Errorf("Build = %v, want the refusal", err)
	}
}
