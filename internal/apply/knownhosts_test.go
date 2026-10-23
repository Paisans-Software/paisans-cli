package apply_test

import (
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

// A changed host key's advice names the host as known_hosts records it: the
// HostName an alias resolved to, given as KnownHost, and for an ssh:// URL
// the host alone, in brackets with its port when the port is not 22.
func TestKnownHostsName(t *testing.T) {
	for _, c := range []struct {
		tr   apply.SSHTransport
		want string
	}{
		{apply.SSHTransport{Destination: "ssh://admin@home-a:2222", KnownHost: "[203.0.113.9]:2222"}, "[203.0.113.9]:2222"},
		{apply.SSHTransport{Destination: "ssh://admin@home-a:22", KnownHost: "203.0.113.9"}, "203.0.113.9"},
		{apply.SSHTransport{Destination: "ssh://ubuntu@192.0.2.10:2222"}, "[192.0.2.10]:2222"},
		{apply.SSHTransport{Destination: "ssh://ubuntu@[2001:db8::1]:2222"}, "[2001:db8::1]:2222"},
		{apply.SSHTransport{Destination: "ubuntu@192.0.2.10"}, "192.0.2.10"},
		{apply.SSHTransport{Destination: "home-a"}, "home-a"},
		{apply.SSHTransport{User: "u", Host: "home-a", Port: 2222, KnownHost: "[203.0.113.9]:2222"}, "[203.0.113.9]:2222"},
		{apply.SSHTransport{User: "u", Host: "192.0.2.10", Port: 2222}, "[192.0.2.10]:2222"},
	} {
		if got := apply.KnownHostsName(c.tr); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.tr, got, c.want)
		}
	}
}
