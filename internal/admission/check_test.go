package admission_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
)

// Check names the field of each problem. Every case starts from the home-a
// request, which it accepts, and changes one thing.
func TestCheckRefuses(t *testing.T) {
	if problems := build(t, "home-a").Check(); len(problems) > 0 {
		t.Fatalf("the home-a request has problems: %q", problems)
	}
	alice := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for _, c := range []struct {
		name   string
		mutate func(*admission.Request)
		want   string
	}{
		{"another version", func(r *admission.Request) { r.Version = 2 }, "version: 2, and this paisans reads version 1"},
		{"a bad deployment id", func(r *admission.Request) { r.Deployment = "F2A9" }, `deployment: "F2A9" is not a deployment id`},
		{"a user paisans refuses", func(r *admission.Request) { r.User = "a;b" }, `user: "a;b" is not a user name`},
		{"root", func(r *admission.Request) { r.User = "root" }, "user: root is never a deployment's user"},
		{"another deployment's user", func(r *admission.Request) { r.User = "paisans-0a0b" }, "paisans-0a0b is another deployment's name; this deployment's own user is paisans-f2a9"},
		{"an ssh port of 0", func(r *admission.Request) { r.SSHPort = 0 }, "ssh_port: 0 is not a port"},
		{"an ssh port out of range", func(r *admission.Request) { r.SSHPort = 70000 }, "ssh_port: 70000 is not a port"},
		{"a listen port out of range", func(r *admission.Request) { r.ListenPort = 70000 }, "listen_port: 70000 is not a port"},
		{"an endpoint with no port", func(r *admission.Request) { r.ListenPort = 0 }, "the site has an endpoint"},
		{"a subnet that is not one", func(r *admission.Request) { r.MeshSubnet = "10.44.0.0" }, `mesh_subnet: "10.44.0.0" is not an IPv4 network`},
		{"an IPv6 subnet", func(r *admission.Request) { r.MeshSubnet = "fd00::/64" }, `mesh_subnet: "fd00::/64" is not an IPv4 network`},
		{"a subnet written as a host", func(r *admission.Request) { r.MeshSubnet = "10.44.0.1/24" }, "is not written as its network, 10.44.0.0/24"},
		{"a subnet over loopback", func(r *admission.Request) { r.MeshSubnet, r.Address = "127.0.0.0/24", "127.0.0.1" }, "overlaps 127.0.0.0/8"},
		{"a subnet over everything", func(r *admission.Request) { r.MeshSubnet = "0.0.0.0/0" }, "overlaps 0.0.0.0/8"},
		{"a subnet over the metadata address", func(r *admission.Request) { r.MeshSubnet, r.Address = "169.254.0.0/24", "169.254.0.1" }, "overlaps 169.254.0.0/16"},
		{"a subnet over multicast", func(r *admission.Request) { r.MeshSubnet, r.Address = "239.1.0.0/24", "239.1.0.1" }, "overlaps 224.0.0.0/3"},
		{"an address outside the subnet", func(r *admission.Request) { r.Address = "10.45.0.1" }, `address: "10.45.0.1" is not an IPv4 address inside mesh_subnet 10.44.0.0/24`},
		{"an address that is not one", func(r *admission.Request) { r.Address = "home-a" }, `address: "home-a" is not an IPv4 address`},
		{"a web server app with a bad name", func(r *admission.Request) {
			r.WebServer = []admission.WebApp{{App: "Status", Hostname: "status.example.org", Listen: "127.0.0.1:8480"}}
		}, `web_server[0].app: "Status" is not an app name`},
		{"a web server app twice", func(r *admission.Request) {
			w := admission.WebApp{App: "status", Hostname: "status.example.org", Listen: "127.0.0.1:8480"}
			r.WebServer = []admission.WebApp{w, w}
		}, "web_server[1].app: status is listed twice"},
		{"a terminal escape in a hostname", func(r *admission.Request) {
			r.WebServer = []admission.WebApp{{App: "status", Hostname: "status\x1b[2J.example.org", Listen: "127.0.0.1:8480"}}
		}, "web_server[0].hostname"},
		{"a listen with no port", func(r *admission.Request) {
			r.WebServer = []admission.WebApp{{App: "status", Hostname: "status.example.org", Listen: "127.0.0.1"}}
		}, `web_server[0].listen: "127.0.0.1" is not an IPv4 address and a port`},
		{"no operator", func(r *admission.Request) { r.Operators = nil }, "operators: none, so nobody could log in as paisans-f2a9"},
		{"an operator with a bad name", func(r *admission.Request) { r.Operators[0].Name = "Alice" }, `operators[0].name: "Alice" is not an operator name`},
		{"an operator twice", func(r *admission.Request) { r.Operators[1].Name = "alice" }, "operators[1].name: alice is listed twice"},
		{"an operator with no key", func(r *admission.Request) { r.Operators[0].Keys = nil }, "operators[0].keys: none for alice"},
		{"a bad key", func(r *admission.Request) { r.Operators[0].Keys = []string{"ssh-ed25519 AAAAnotakey"} }, "operators[0].keys[0]: not an OpenSSH public key"},
		{"a key with options", func(r *admission.Request) { r.Operators[0].Keys = []string{`command="/bin/sh" ` + alice} }, "operators[0].keys[0]: carries options"},
		{"a key with a comment", func(r *admission.Request) { r.Operators[0].Keys = []string{alice + " mallory@example.org"} }, "not written as type and base64 alone, " + alice},
		{"two keys on a line", func(r *admission.Request) { r.Operators[0].Keys = []string{alice + "\n" + alice} }, "more than one key on the line"},
		{"a key under two operators", func(r *admission.Request) { r.Operators[1].Keys = []string{alice} }, "operators[1].keys[0]: the same key as alice's"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := build(t, "home-a")
			c.mutate(&r)
			problems := strings.Join(r.Check(), "\n")
			if !strings.Contains(problems, c.want) {
				t.Errorf("got %q\nwant it to say %q", problems, c.want)
			}
		})
	}
}
