package admission_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
)

// One golden request per role mix in the fixture: each is the file host
// request prints, byte for byte, and each reads back as itself.
func TestGoldenRequests(t *testing.T) {
	for _, site := range []string{"home-a", "home-b", "vm", "status", "watch", "store"} {
		t.Run(site, func(t *testing.T) {
			got := admission.Encode(build(t, site))
			path := filepath.Join("testdata", "requests", site+".admit")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Errorf("got\n%s\nwant\n%s", got, want)
			}
			back, err := admission.Parse(got)
			if err != nil {
				t.Fatalf("its own request is refused: %v", err)
			}
			if again := admission.Encode(back); string(again) != string(got) {
				t.Errorf("read back as\n%s", again)
			}
		})
	}
}

// golden is the home-a request as host request writes it.
func golden(t *testing.T) string {
	t.Helper()
	return string(admission.Encode(build(t, "home-a")))
}

// The digest is 80 bits of SHA-256 as sixteen Crockford base32 characters in
// four groups, the same on every run.
func TestDigestIsStable(t *testing.T) {
	got := admission.Digest([]byte(golden(t)))
	if !regexp.MustCompile(`^[0-9a-hjkmnp-tv-z]{4}(-[0-9a-hjkmnp-tv-z]{4}){3}$`).MatchString(got) {
		t.Fatalf("digest %q is not four groups of four Crockford base32 characters", got)
	}
	for range 3 {
		if again := admission.Digest(admission.Encode(build(t, "home-a"))); again != got {
			t.Fatalf("digest changed between runs: %s, then %s", got, again)
		}
	}
}

// Known vectors, computed independently of Digest: the first 10 bytes of the
// SHA-256 of the input, run through Python's base64.b32encode (RFC 4648, 80
// bits being exactly sixteen symbols) and re-mapped from its alphabet,
// A-Z2-7, to Crockford's with str.translate:
//
//	d = hashlib.sha256(data).digest()[:10]
//	base64.b32encode(d).decode().translate(str.maketrans(
//	    'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567',
//	    '0123456789abcdefghjkmnpqrstvwxyz'))
//
// then cut into groups of four. The second input is testdata/requests/home-a.admit.
func TestDigestKnownVectors(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("testdata", "requests", "home-a.admit"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		data []byte
		want string
	}{
		{"nothing", nil, "werc-8gmr-zge1-96qv"},
		{"home-a.admit", golden, "p7xh-w1a8-2q08-0a17"},
	} {
		if got := admission.Digest(c.data); got != c.want {
			t.Errorf("%s: digest %s, want %s", c.name, got, c.want)
		}
	}
}

// ParseDigest reads what an owner types: any case, with or without hyphens
// and spaces, with i and l for 1 and o for 0, and returns the form Digest
// writes, so the two compare with ==.
func TestParseDigestAccepts(t *testing.T) {
	const canonical = "0123-4567-89ab-cdef"
	for _, typed := range []string{
		"0123-4567-89ab-cdef",
		"0123456789abcdef",
		"0123 4567 89AB CDEF",
		"  0123-4567-89AB-CDEF\n",
		"oi23-4567-89ab-cdef",
		"OL23-4567-89AB-CDEF",
		"Ol23-4567-89aB-cDeF",
	} {
		got, err := admission.ParseDigest(typed)
		if err != nil {
			t.Errorf("%q: %v", typed, err)
		} else if got != canonical {
			t.Errorf("%q = %s, want %s", typed, got, canonical)
		}
	}
	d := admission.Digest([]byte(golden(t)))
	if got, err := admission.ParseDigest(strings.ToUpper(strings.ReplaceAll(d, "-", " "))); err != nil || got != d {
		t.Errorf("a digest typed in capitals = %q, %v; want %s", got, err, d)
	}
}

func TestParseDigestRefuses(t *testing.T) {
	for _, c := range []struct{ typed, want string }{
		{"", "sixteen"},
		{"0123-4567-89ab-cde", "sixteen"},
		{"0123-4567-89ab-cdef0", "sixteen"},
		{"0123-4567-89ab-cdeu", "u"},
		{"0123-4567-89ab-cde!", "!"},
		{"0123-4567-89ab-cdé0", "é"},
		{"0123_4567_89ab_cdef", "_"},
	} {
		_, err := admission.ParseDigest(c.typed)
		if err == nil {
			t.Errorf("%q: accepted", c.typed)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: got %v, want it to say %q", c.typed, err, c.want)
		}
	}
}

// mutations changes one field of a request each, keyed by its path. The
// test below requires one for every field Parse knows, so a field added
// later cannot be left out of the digest.
var mutations = map[string]func(*admission.Request){
	"version":             func(r *admission.Request) { r.Version = 2 },
	"deployment":          func(r *admission.Request) { r.Deployment = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e02" },
	"user":                func(r *admission.Request) { r.User = "community" },
	"ssh_port":            func(r *admission.Request) { r.SSHPort = 2222 },
	"listen_port":         func(r *admission.Request) { r.ListenPort = 51821 },
	"mesh_subnet":         func(r *admission.Request) { r.MeshSubnet = "10.44.0.0/16" },
	"address":             func(r *admission.Request) { r.Address = "10.44.0.9" },
	"endpoint":            func(r *admission.Request) { r.Endpoint = false },
	"grants":              func(r *admission.Request) { r.Grants = admission.Grants{} },
	"grants.watchdog":     func(r *admission.Request) { r.Grants.Watchdog = false },
	"grants.public_ports": func(r *admission.Request) { r.Grants.PublicPorts = false },
	"web_server": func(r *admission.Request) {
		r.WebServer = []admission.WebApp{{App: "x", Hostname: "x.example.org", Listen: "127.0.0.1:1"}}
	},
	"web_server.app": func(r *admission.Request) {
		r.WebServer = []admission.WebApp{{App: "y", Hostname: "x.example.org", Listen: "127.0.0.1:1"}}
	},
	"web_server.hostname": func(r *admission.Request) {
		r.WebServer = []admission.WebApp{{App: "x", Hostname: "y.example.org", Listen: "127.0.0.1:1"}}
	},
	"web_server.listen": func(r *admission.Request) {
		r.WebServer = []admission.WebApp{{App: "x", Hostname: "x.example.org", Listen: "127.0.0.1:2"}}
	},
	"operators":      func(r *admission.Request) { r.Operators = r.Operators[:1] },
	"operators.name": func(r *admission.Request) { r.Operators[0].Name = "carol" },
	"operators.keys": func(r *admission.Request) {
		r.Operators[0].Keys = []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"}
	},
}

// Changing any one field changes the digest.
func TestDigestChangesWithEveryField(t *testing.T) {
	base := admission.Digest([]byte(golden(t)))
	paths := admission.Fields()
	for _, path := range paths {
		mutate, ok := mutations[path]
		if !ok {
			t.Errorf("%s: no mutation; add one", path)
			continue
		}
		r := build(t, "home-a")
		mutate(&r)
		if admission.Digest(admission.Encode(r)) == base {
			t.Errorf("%s: changing it left the digest %s", path, base)
		}
	}
	for path := range mutations {
		if !slices.Contains(paths, path) {
			t.Errorf("%s: a mutation for a field Parse does not know", path)
		}
	}
}

// Each refusal names what is wrong. Every case starts from a request Parse
// accepts and changes one thing. What Check refuses is tested beside it;
// the cases here that Check also finds prove Parse runs it.
func TestParseRefuses(t *testing.T) {
	g := golden(t)
	alice := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for _, c := range []struct {
		name, data, want string
	}{
		{"not YAML", "version: [1\n", "it is not YAML"},
		{"a list, not a mapping", "- 1\n", "is one YAML mapping"},
		{"empty", "", "is one YAML mapping"},
		{"an unknown version", strings.Replace(g, "version: 1", "version: 2", 1), "version: 2, and this paisans reads version 1"},
		{"version 0", strings.Replace(g, "version: 1", "version: 0", 1), "version: 0, and this paisans reads version 1"},
		{"no version", strings.Replace(g, "version: 1\n", "", 1), "version: missing"},
		{"a missing field", strings.Replace(g, "ssh_port: 22\n", "", 1), "ssh_port: missing"},
		{"a missing grant", strings.Replace(g, "  public_ports: true\n", "", 1), "grants.public_ports: missing"},
		{"a missing operator field", strings.Replace(g, "  - name: \"alice\"\n    keys:", "  - keys:", 1), "operators.name: missing"},
		{"an unknown field", strings.Replace(g, "endpoint: true\n", "endpoint: true\nsudo: true\n", 1), "sudo: not a field of a request"},
		{"an unknown grant", strings.Replace(g, "  watchdog: true\n", "  watchdog: true\n  docker: true\n", 1), "grants.docker: not a field of a request"},
		{"an unknown operator field", strings.Replace(g, "  - name: \"alice\"\n", "  - name: \"alice\"\n    shell: \"/bin/sh\"\n", 1), "operators.shell: not a field of a request"},
		{"a port as text", strings.Replace(g, "ssh_port: 22", "ssh_port: \"22\"", 1), "the wrong kind of value"},
		{"a bad key", strings.Replace(g, alice, "ssh-ed25519 AAAAnotakey", 1), "operators[0].keys[0]: not an OpenSSH public key"},
		{"no operator", g[:strings.Index(g, "operators:")] + "operators: []\n", "operators: none"},
		{"an address outside the subnet", strings.Replace(g, `address: "10.44.0.1"`, `address: "10.45.0.1"`, 1), "address: \"10.45.0.1\" is not an IPv4 address inside mesh_subnet 10.44.0.0/24"},
		{"a subnet over loopback", strings.NewReplacer(`"10.44.0.0/24"`, `"127.0.0.0/24"`, `address: "10.44.0.1"`, `address: "127.0.0.1"`).Replace(g), "overlaps 127.0.0.0/8"},
		{"another deployment's user", strings.Replace(g, `user: "paisans-f2a9"`, `user: "paisans-0a0b"`, 1), "paisans-0a0b is another deployment's name"},
		{"a web server app with a bad listen", strings.Replace(g, "web_server: []", "web_server:\n  - app: \"status\"\n    hostname: \"status.example.org\"\n    listen: \"0.0.0.0\"", 1), "web_server[0].listen"},
		{"a comment added", g + "# fine\n", "not exactly as paisans host request wrote it"},
		{"CRLF line endings", strings.ReplaceAll(g, "\n", "\r\n"), "not exactly as paisans host request wrote it"},
		{"the header removed", g[strings.Index(g, "version:"):], "not exactly as paisans host request wrote it"},
		{"a value unquoted", strings.Replace(g, `user: "paisans-f2a9"`, `user: paisans-f2a9`, 1), "not exactly as paisans host request wrote it"},
		{"an anchor", strings.Replace(g, `user: "paisans-f2a9"`, `user: &u "paisans-f2a9"`, 1), "not exactly as paisans host request wrote it"},
		{"a second document", g + "---\nversion: 1\n", "not exactly as paisans host request wrote it"},
		{"a duplicate key", strings.Replace(g, "endpoint: true\n", "endpoint: true\nendpoint: false\n", 1), "already defined"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := admission.Parse([]byte(c.data))
			if err == nil {
				t.Fatalf("accepted:\n%s", c.data)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v\nwant it to say %q", err, c.want)
			}
		})
	}
}

// The attack the digest exists for: someone who can change the request on
// its way adds their own key. The result is a request Parse accepts, so
// only the digest can tell, and its digest is not the one the operator
// printed. Anything they could hide from Parse instead (a comment, a second
// document, an anchor) is refused above.
func TestAForgedKeyChangesTheDigest(t *testing.T) {
	sent := admission.Encode(build(t, "home-a"))
	forged := build(t, "home-a")
	forged.Operators = append(forged.Operators, admission.Operator{Name: "mallory", Keys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"}})
	data := admission.Encode(forged)
	if _, err := admission.Parse(data); err != nil {
		t.Fatalf("the forgery is refused for another reason, so this test proves nothing: %v", err)
	}
	if admission.Digest(data) == admission.Digest(sent) {
		t.Fatal("a request with a key added has the digest of the one sent")
	}
}
