package config_test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-cli/internal/config"
)

func TestListenPortIsTheEndpointsPort(t *testing.T) {
	for endpoint, want := range map[string]int{
		"vm.example.org:51820":  51820,
		"vm.example.org:51821":  51821,
		"[2001:db8::1]:4500":    4500,
		"":                      0,
		"vm.example.org":        0,
		"vm.example.org:99999":  0,
		"vm.example.org:wg-udp": 0,
	} {
		if got := (config.Site{Endpoint: endpoint}).ListenPort(); got != want {
			t.Errorf("ListenPort(%q) = %d, want %d", endpoint, got, want)
		}
	}
}

// The address other sites dial: public_address on the endpoint's port, or
// on 51820 when no endpoint is written. An endpoint is used as written only
// where there is no public_address to dial instead, or where it is an address
// itself. A hostname beside a public_address is not dialled, so the mesh
// never waits on DNS.
func TestDialEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		endpoint, public, public6 string
		want                      string
	}{
		{"public address alone", "", "203.0.113.10", "", "203.0.113.10:51820"},
		{"port only", ":51821", "203.0.113.10", "", "203.0.113.10:51821"},
		{"hostname beside a public address", "vm.example.org:51822", "203.0.113.10", "", "203.0.113.10:51822"},
		{"hostname alone", "home.example.org:51820", "", "", "home.example.org:51820"},
		{"address alone", "198.51.100.2:51820", "", "", "198.51.100.2:51820"},
		{"address beside a public address", "198.51.100.2:51820", "203.0.113.10", "", "198.51.100.2:51820"},
		{"IPv6 address", "[2001:db8::1]:4500", "", "", "[2001:db8::1]:4500"},
		{"neither", "", "", "", ""},
		{"public_address6 alone", "", "", "2001:db8::10", ""},
		{"port only without a public address", ":51820", "", "", ""},
		{"malformed", "vm.example.org", "203.0.113.10", "", ""},
	} {
		site := config.Site{Endpoint: tc.endpoint, PublicAddress: tc.public, PublicAddress6: tc.public6}
		if got := site.DialEndpoint(); got != tc.want {
			t.Errorf("%s: DialEndpoint() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The port a site listens on is the port it is dialled on, derived or not.
func TestListenPortFollowsTheDialEndpoint(t *testing.T) {
	for _, tc := range []struct {
		site config.Site
		want int
	}{
		{config.Site{PublicAddress: "203.0.113.10"}, 51820},
		{config.Site{PublicAddress: "203.0.113.10", Endpoint: ":51821"}, 51821},
		{config.Site{PublicAddress: "203.0.113.10", Endpoint: "vm.example.org:51822"}, 51822},
		{config.Site{PublicAddress6: "2001:db8::10"}, 0},
	} {
		if got := tc.site.ListenPort(); got != tc.want {
			t.Errorf("%+v: ListenPort() = %d, want %d", tc.site, got, tc.want)
		}
	}
}

// A site with only a public address is dialled, so it peers directly with a
// site that has neither.
func TestAPublicAddressPeersDirectly(t *testing.T) {
	public := config.Site{PublicAddress: "203.0.113.10"}
	home := config.Site{}
	if !home.PeersDirectly(public) || !public.PeersDirectly(home) {
		t.Error("a site with a public address does not peer directly with one that dials out")
	}
	if home.PeersDirectly(config.Site{PublicAddress6: "2001:db8::10"}) {
		t.Error("public_address6 alone made a site dialled")
	}
}

func TestLoadRefusesAPortOnlyEndpointWithoutAPublicAddress(t *testing.T) {
	body := strings.Replace(minimal, "    address: 10.44.0.1\n", "    address: 10.44.0.1\n    endpoint: \":51820\"\n", 1)
	_, err := config.Load(write(t, body))
	if err == nil || !strings.Contains(err.Error(), "sites.home-a.endpoint") || !strings.Contains(err.Error(), "public_address") {
		t.Fatalf("Load = %v, want a refusal of the endpoint naming public_address", err)
	}
	ok := strings.Replace(minimal, "    address: 10.44.0.1\n", "    address: 10.44.0.1\n    endpoint: \":51821\"\n    public_address: 203.0.113.10\n", 1)
	if _, err := config.Load(write(t, ok)); err != nil {
		t.Fatalf("Load refused a port-only endpoint beside a public address: %v", err)
	}
}

func TestLoadRefusesAPortOnlyEndpointWithABadPort(t *testing.T) {
	for _, port := range []string{":0", ":70000", ":wg"} {
		body := strings.Replace(minimal, "    address: 10.44.0.1\n", "    address: 10.44.0.1\n    endpoint: \""+port+"\"\n    public_address: 203.0.113.10\n", 1)
		_, err := config.Load(write(t, body))
		if err == nil || !strings.Contains(err.Error(), "sites.home-a.endpoint") {
			t.Errorf("endpoint %q: Load = %v, want a refusal of the endpoint", port, err)
		}
	}
}

func TestLoadRefusesAnEndpointWithoutAPort(t *testing.T) {
	body := strings.Replace(minimal, "    address: 10.44.0.1\n", "    address: 10.44.0.1\n    endpoint: home-a.example.org\n", 1)
	_, err := config.Load(write(t, body))
	if err == nil || !strings.Contains(err.Error(), "sites.home-a.endpoint") {
		t.Fatalf("Load = %v, want a refusal of the endpoint", err)
	}
}

func TestAMissingSubnetSendsTheOperatorToInit(t *testing.T) {
	body := strings.Replace(minimal, "mesh:\n  subnet: 10.44.0.0/24\n", "", 1)
	path := write(t, body)
	_, err := config.Load(path)
	if err == nil || !strings.Contains(err.Error(), "mesh.subnet: required. Run `paisans init`") {
		t.Fatalf("Load = %v, want a refusal sending the operator to init", err)
	}
	cfg, err := config.LoadForInit(path)
	if err != nil {
		t.Fatalf("LoadForInit refused a file whose only gap is the subnet: %v", err)
	}
	if cfg.Mesh.Subnet != "" {
		t.Errorf("subnet = %q", cfg.Mesh.Subnet)
	}
	bad := strings.Replace(minimal, "10.44.0.0/24", "10.44.0.0", 1)
	if _, err := config.LoadForInit(write(t, bad)); err == nil {
		t.Error("LoadForInit accepted a malformed subnet")
	}
}

const meshFile = `version: 1
id: f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01   # never changes

# the private network
mesh:
  # picked by init
  subnet: 10.44.0.0/24   # keep
sites:
  home-a:
    roles: [data]
    address: 10.44.0.1          # WireGuard address
  vm:
    address: "10.44.0.3"
    endpoint: vm.example.org:51820
`

func TestSetMeshKeepsEveryOtherByte(t *testing.T) {
	path := write(t, meshFile)
	if err := config.SetMesh(path, "10.212.37.0/24", map[string]string{"home-a": "10.212.37.1", "vm": "10.212.37.3"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := strings.NewReplacer(
		"subnet: 10.44.0.0/24", "subnet: 10.212.37.0/24",
		"address: 10.44.0.1 ", "address: 10.212.37.1 ",
		`address: "10.44.0.3"`, `address: "10.212.37.3"`,
	).Replace(meshFile)
	if string(got) != want {
		t.Errorf("SetMesh wrote:\n%s\nwant:\n%s", got, want)
	}
}

func TestSetMeshAddsAMissingSubnet(t *testing.T) {
	for name, tc := range map[string]struct{ from, to string }{
		"no mesh block": {
			from: "mesh:\n  # picked by init\n  subnet: 10.44.0.0/24   # keep\n",
			to:   "",
		},
		"an empty mesh key": {
			from: "  # picked by init\n  subnet: 10.44.0.0/24   # keep\n",
			to:   "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			body := strings.Replace(meshFile, tc.from, tc.to, 1)
			path := write(t, body)
			if err := config.SetMesh(path, "10.212.37.0/24", map[string]string{"home-a": "10.212.37.1"}); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(path)
			var cfg config.Config
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				t.Fatalf("the result does not parse: %v\n%s", err, data)
			}
			if cfg.Mesh.Subnet != "10.212.37.0/24" || cfg.Sites["home-a"].Address != "10.212.37.1" || cfg.Sites["vm"].Address != "10.44.0.3" {
				t.Errorf("loaded %+v", cfg)
			}
			if !strings.Contains(string(data), "# WireGuard address") || !strings.Contains(string(data), "# never changes") {
				t.Errorf("a comment was lost:\n%s", data)
			}
		})
	}
}

// CheckMeshSubnet is the one subnet rule the loader, a request and the
// WireGuard helper's renderer hold a mesh to: the network in its canonical
// form, no larger than a /24, and clear of every reserved range.
func TestCheckMeshSubnet(t *testing.T) {
	for subnet, want := range map[string][]string{
		"10.44.0.0/24":   nil,
		"10.44.0.0/28":   nil,
		"10.44.0.5/24":   {"is a host address inside a network, not the network itself, 10.44.0.0/24"},
		"10.44.0.0/024":  {"is not written in its canonical form, 10.44.0.0/24"},
		"fd00::/64":      {"is not IPv4"},
		"10.44.0.0":      {"is not a network in CIDR notation"},
		"10.44.0.0/16":   {"is larger than a /24"},
		"127.0.0.0/24":   {"overlaps 127.0.0.0/8"},
		"0.0.0.0/0":      {"is larger than a /24", "overlaps 0.0.0.0/8", "overlaps 127.0.0.0/8", "overlaps 169.254.0.0/16", "overlaps 224.0.0.0/3"},
		"169.254.1.0/24": {"overlaps 169.254.0.0/16"},
		"240.0.0.0/24":   {"overlaps 224.0.0.0/3"},
	} {
		network, problems := config.CheckMeshSubnet(subnet)
		if strings.Join(problems, "|") != strings.Join(want, "|") {
			t.Errorf("CheckMeshSubnet(%q) = %q, want %q", subnet, problems, want)
		}
		if want == nil && (network == nil || network.String() != subnet) {
			t.Errorf("CheckMeshSubnet(%q) network = %v", subnet, network)
		}
	}
}
