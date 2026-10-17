package hostcheck_test

import (
	"slices"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostcheck"
)

// One line of ss -Hltunpe gives the socket, its local address as printed
// and its process.
func TestParseSocketLine(t *testing.T) {
	s, ok, err := hostcheck.ParseSocketLine(`udp UNCONN 0 0 [::ffff:127.0.0.1]:5353 *:* users:(("avahi-daemon",pid=612,fd=12)) uid:107 ino:1 sk:1`)
	want := hostcheck.SocketLine{Socket: hostcheck.Socket{Proto: "udp", Address: "127.0.0.1", Port: 5353, UID: 107}, Local: "[::ffff:127.0.0.1]:5353", Process: "avahi-daemon", PID: 612}
	if err != nil || !ok || s != want {
		t.Errorf("%+v %v %v", s, ok, err)
	}
	if _, ok, err := hostcheck.ParseSocketLine("LISTEN 0 4096 *:80 *:*"); ok || err != nil {
		t.Errorf("a line with no Netid: %v %v", ok, err)
	}
}

// PortBindings give each host port or range with its protocol and address,
// and leave out a port Docker picks.
func TestParseBindings(t *testing.T) {
	got, err := hostcheck.ParseBindings(`{"443/tcp":[{"HostIp":"","HostPort":"8443"},{"HostIp":"::","HostPort":"8443"}],"53/udp":[{"HostIp":"127.0.0.1","HostPort":"5300-5301"}],"80/tcp":[{"HostIp":"","HostPort":""}]}`)
	want := []hostcheck.Binding{{Proto: "tcp", Lo: 8443, Hi: 8443}, {Proto: "tcp", Lo: 8443, Hi: 8443}, {Proto: "udp", Address: "127.0.0.1", Lo: 5300, Hi: 5301}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("%+v %v", got, err)
	}
}
