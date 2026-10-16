package admission_test

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"math/big"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

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
		{"a subnet that is not one", func(r *admission.Request) { r.MeshSubnet = "10.44.0.0" }, `mesh_subnet: "10.44.0.0" is not a network in CIDR notation`},
		{"an IPv6 subnet", func(r *admission.Request) { r.MeshSubnet = "fd00::/64" }, `mesh_subnet: "fd00::/64" is not IPv4`},
		{"a subnet with a leading zero", func(r *admission.Request) { r.MeshSubnet = "10.44.0.0/024" }, `mesh_subnet: "10.44.0.0/024" is not written in its canonical form, 10.44.0.0/24`},
		{"a subnet written as a host", func(r *admission.Request) { r.MeshSubnet = "10.44.0.1/24" }, "is a host address inside a network, not the network itself, 10.44.0.0/24"},
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
		}, `web_server[0].listen: "127.0.0.1" is not a loopback address or this site's mesh address, with a port`},
		{"no site", func(r *admission.Request) { r.Site = "" }, `site: "" is not a site name`},
		{"a site with a terminal escape", func(r *admission.Request) { r.Site = "home\x1b[2J" }, `site: "home\x1b[2J" is not a site name`},
		{"no roles, with grants", func(r *admission.Request) { r.Roles = []string{} }, `grants: watchdog true and public_ports true, and roles [] need watchdog false and public_ports false`},
		{"an unknown role", func(r *admission.Request) { r.Roles = []string{"apps", "data", "root"} }, `roles[2]: "root" is not a site role`},
		{"a role twice", func(r *admission.Request) { r.Roles = []string{"apps", "apps", "data", "gateway"} }, `roles[1]: "apps" is listed twice`},
		{"roles unsorted", func(r *admission.Request) { r.Roles = []string{"data", "apps", "gateway"} }, `roles[1]: "apps" comes after "data"`},
		{"a watchdog the roles do not need", func(r *admission.Request) { r.Roles = []string{"apps", "gateway"} }, `grants: watchdog true and public_ports true, and roles ["apps" "gateway"] need watchdog false and public_ports true`},
		{"a watchdog the roles need, not asked", func(r *admission.Request) { r.Grants.Watchdog = false }, `grants: watchdog false and public_ports true, and roles ["apps" "data" "gateway"] need watchdog true and public_ports true`},
		{"low ports a Caddy monitor needs, not asked", func(r *admission.Request) { r.Roles, r.Grants = []string{"monitor"}, admission.Grants{} }, `need watchdog false and public_ports true`},
		{"no operator", func(r *admission.Request) { r.Operators = nil }, "operators: none, so nobody could log in as \"paisans-f2a9\""},
		{"an operator with a bad name", func(r *admission.Request) { r.Operators[0].Name = "Alice" }, `operators[0].name: "Alice" is not an operator name`},
		{"an operator twice", func(r *admission.Request) { r.Operators[1].Name = "alice" }, "operators[1].name: alice is listed twice"},
		{"an operator with no key", func(r *admission.Request) { r.Operators[0].Keys = nil }, `operators[0].keys: none for "alice"`},
		{"a bad key", func(r *admission.Request) { r.Operators[0].Keys = []string{"ssh-ed25519 AAAAnotakey"} }, "operators[0].keys[0]: not an OpenSSH public key"},
		{"a key with options", func(r *admission.Request) { r.Operators[0].Keys = []string{`command="/bin/sh" ` + alice} }, "operators[0].keys[0]: carries options"},
		{"a key with a comment", func(r *admission.Request) { r.Operators[0].Keys = []string{alice + " mallory@example.org"} }, "not written as type and base64 alone, " + alice},
		{"two keys on a line", func(r *admission.Request) { r.Operators[0].Keys = []string{alice + "\n" + alice} }, "more than one key on the line"},
		{"a key under two operators", func(r *admission.Request) { r.Operators[1].Keys = []string{alice} }, `operators[1].keys[0]: the same key as "alice"'s`},
		{"a mesh larger than a /24", func(r *admission.Request) { r.MeshSubnet = "10.44.0.0/16" }, `mesh_subnet: "10.44.0.0/16" is larger than a /24`},
		{"a mesh outside the private ranges", func(r *admission.Request) { r.MeshSubnet, r.Address = "44.1.2.0/24", "44.1.2.1" }, `mesh_subnet: "44.1.2.0/24" is outside the private ranges`},
		{"a mapped IPv6 subnet", func(r *admission.Request) { r.MeshSubnet, r.Address = "::ffff:10.44.0.0/120", "10.44.0.1" }, `mesh_subnet: "::ffff:10.44.0.0/120" is not written in its canonical form, 10.44.0.0/24`},
		{"a mapped IPv6 address", func(r *admission.Request) { r.Address = "::ffff:10.44.0.1" }, `address: "::ffff:10.44.0.1" is not written as plain dotted IPv4`},
		{"the network address", func(r *admission.Request) { r.Address = "10.44.0.0" }, "is the subnet's network or broadcast address"},
		{"the broadcast address", func(r *admission.Request) { r.Address = "10.44.0.255" }, "is the subnet's network or broadcast address"},
		{"a listen on the metadata address", func(r *admission.Request) {
			r.WebServer = []admission.WebApp{{App: "status", Hostname: "status.example.org", Listen: "169.254.169.254:80"}}
		}, `web_server[0].listen: "169.254.169.254:80" is not`},
		{"a listen on another mesh address", func(r *admission.Request) {
			r.WebServer = []admission.WebApp{{App: "status", Hostname: "status.example.org", Listen: "10.44.0.2:80"}}
		}, `web_server[0].listen: "10.44.0.2:80" is not`},
		{"a listen on port 0", func(r *admission.Request) {
			r.WebServer = []admission.WebApp{{App: "status", Hostname: "status.example.org", Listen: "127.0.0.1:0"}}
		}, `web_server[0].listen: "127.0.0.1:0" is not`},
		{"an IPv6 loopback listen", func(r *admission.Request) {
			r.WebServer = []admission.WebApp{{App: "status", Hostname: "status.example.org", Listen: "[::1]:8480"}}
		}, `web_server[0].listen: "[::1]:8480" is not`},
		{"a hostname twice", func(r *admission.Request) {
			r.WebServer = []admission.WebApp{
				{App: "status", Hostname: "status.example.org", Listen: "127.0.0.1:8480"},
				{App: "watch", Hostname: "status.example.org", Listen: "127.0.0.1:8481"},
			}
		}, `web_server[1].hostname: "status.example.org" is listed twice`},
		{"a hostname with an empty label", func(r *admission.Request) {
			r.WebServer = []admission.WebApp{{App: "status", Hostname: "a..example.org", Listen: "127.0.0.1:8480"}}
		}, "web_server[0].hostname"},
		{"a hostname with capitals", func(r *admission.Request) {
			r.WebServer = []admission.WebApp{{App: "status", Hostname: "Status.example.org", Listen: "127.0.0.1:8480"}}
		}, `web_server[0].hostname: "Status.example.org" is not a hostname`},
		{"an rsa key under 3072 bits", func(r *admission.Request) { r.Operators[0].Keys = []string{keyLine(t, rsaKey(t, 2048))} }, "ssh-rsa key of 2048 bits, under 3072"},
		{"an rsa key 6 bits short of 3072", func(r *admission.Request) { r.Operators[0].Keys = []string{keyLine(t, rsaModulus(3066))} }, "ssh-rsa key of 3066 bits, under 3072"},
		{"a certificate", func(r *admission.Request) { r.Operators[0].Keys = []string{certLine(t)} }, `"ssh-ed25519-cert-v01@openssh.com", not a key type`},
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

// The grants Check holds a request to are Needs for its roles, with a
// monitor behind the owner's web server when the request hands that server
// an app: each golden role mix passes, and the monitor that hands one over
// needs no low ports.
func TestCheckGrantsFollowTheRoles(t *testing.T) {
	for _, site := range []string{"home-a", "home-b", "vm", "status", "watch", "store", "host"} {
		if problems := build(t, site).Check(); len(problems) > 0 {
			t.Errorf("%s: %q", site, problems)
		}
	}
	r := build(t, "status")
	if len(r.WebServer) == 0 || r.Grants.PublicPorts {
		t.Errorf("a monitor with managed_by: external and its uptime app pinned: web_server %+v, grants %+v; want its app handed over and no public_ports", r.WebServer, r.Grants)
	}
	r.Grants.PublicPorts = true
	if problems := strings.Join(r.Check(), "\n"); !strings.Contains(problems, `roles ["monitor"] need watchdog false and public_ports false`) {
		t.Errorf("a monitor handing its app to the owner's web server was let ask for low ports: %q", problems)
	}
}

// A site with no roles exists to host an app pinned to it, which the
// configuration allows, so its request carries roles: [] and no grant.
func TestCheckAcceptsNoRoles(t *testing.T) {
	r := build(t, "host")
	if len(r.Roles) != 0 || r.Grants != (admission.Grants{}) {
		t.Fatalf("roles %q, grants %+v", r.Roles, r.Grants)
	}
	if problems := r.Check(); len(problems) > 0 {
		t.Errorf("%q", problems)
	}
}

func keyLine(t *testing.T, pub any) string {
	t.Helper()
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// rsaModulus is an RSA public key whose modulus is exactly bits long. It is
// no one's key, which a size check does not need.
func rsaModulus(bits int) *rsa.PublicKey {
	n := new(big.Int).Lsh(big.NewInt(1), uint(bits-1))
	n.Add(n, big.NewInt(0x1234567))
	return &rsa.PublicKey{N: n, E: 65537}
}

func rsaKey(t *testing.T, bits int) *rsa.PublicKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	return &k.PublicKey
}

func certLine(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	cert := &ssh.Certificate{Key: k, CertType: ssh.UserCert, SignatureKey: k, Signature: &ssh.Signature{Format: "ssh-ed25519", Blob: make([]byte, 64)}}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(cert)))
}

// The key types a request accepts: the ones a current OpenSSH generates by
// default, and RSA only at a size that is still sound.
func TestCheckAcceptsTheKeyTypesItShould(t *testing.T) {
	ec := func(c elliptic.Curve) string {
		k, err := ecdsa.GenerateKey(c, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return keyLine(t, &k.PublicKey)
	}
	for name, line := range map[string]string{
		"ecdsa-p256": ec(elliptic.P256()),
		"ecdsa-p384": ec(elliptic.P384()),
		"ecdsa-p521": ec(elliptic.P521()),
		"rsa-3072":   keyLine(t, rsaKey(t, 3072)),
	} {
		r := build(t, "home-a")
		r.Operators[0].Keys = []string{line}
		if problems := r.Check(); len(problems) > 0 {
			t.Errorf("%s: %q", name, problems)
		}
	}
}

// Whatever a request echoes of its own text is quoted, so a forged request
// cannot repaint the owner's terminal around the prompt that asks them to
// trust it.
func TestCheckQuotesWhatItEchoes(t *testing.T) {
	esc := "\x1b[2J"
	alice := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for name, mutate := range map[string]func(*admission.Request){
		"user with no operators": func(r *admission.Request) { r.User, r.Operators = esc, nil },
		"operator without keys":  func(r *admission.Request) { r.Operators[0].Name, r.Operators[0].Keys = esc, nil },
		"key options":            func(r *admission.Request) { r.Operators[0].Keys = []string{"from=\"" + esc + "\" " + alice} },
		"duplicate key":          func(r *admission.Request) { r.Operators[0].Name, r.Operators[1].Keys = esc, []string{alice} },
		"key type":               func(r *admission.Request) { r.Operators[0].Keys = []string{esc + " AAAA"} },
		"site":                   func(r *admission.Request) { r.Site = esc },
		"role":                   func(r *admission.Request) { r.Roles = []string{"apps", esc} },
		"roles with grants":      func(r *admission.Request) { r.Roles = []string{esc} },
	} {
		r := build(t, "home-a")
		mutate(&r)
		for _, p := range r.Check() {
			if strings.ContainsAny(p, "\x1b\n") {
				t.Errorf("%s: a problem holds a raw control character: %q", name, p)
			}
		}
	}
}
