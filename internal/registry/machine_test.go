package registry

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// machineHost is a host with files: the registry and the WireGuard config.
// Run records each command and answers nothing.
type machineHost struct {
	files map[string]string
	ran   []string
}

func (h *machineHost) Run(command string) (string, error) {
	h.ran = append(h.ran, command)
	return "", nil
}

func (h *machineHost) ReadFile(path string) (string, bool, error) {
	content, ok := h.files[path]
	return content, ok, nil
}

func (h *machineHost) Describe() string { return "ubuntu@203.0.113.9" }

const conf = "/etc/wireguard/psns-f2a9.conf"

func machineConfig() *config.Config {
	return &config.Config{
		ID:   ours,
		Mesh: config.Mesh{Subnet: "10.44.0.0/24"},
		Sites: map[string]config.Site{
			"home-a": {Address: "10.44.0.1"},
			"home-b": {Address: "10.44.0.2"},
		},
	}
}

func wgConf(address string) string {
	return "[Interface]\nPrivateKey = x\nAddress = " + address + "/24\nListenPort = 51820\n"
}

func registryHolding(t *testing.T, e Entry) string {
	t.Helper()
	data, err := Encode(Registry{Version: Version, Deployments: map[string]Entry{ours: e}})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Check and Claim refuse a host that runs another site of this deployment,
// and pass one that runs none or this site, whatever its address is now.
func TestASiteIsOneMachine(t *testing.T) {
	addressed := func(site, address string) Entry {
		e := entry("f2a9", "example.org", site)
		e.Address = address
		return e
	}
	for _, tc := range []struct {
		name  string
		files map[string]string
		// want is each phrase the refusal holds, none for a pass.
		want []string
	}{
		{"a blank host", map[string]string{}, nil},
		{"this site's entry", map[string]string{Path: registryHolding(t, addressed("home-a", "10.44.0.1")), conf: wgConf("10.44.0.1")}, nil},
		// home-a's address changed in paisans-stack.yaml: the host's file still
		// has the old one, and the entry says the host is home-a.
		{"this site readdressed", map[string]string{Path: registryHolding(t, addressed("home-a", "10.44.0.9")), conf: wgConf("10.44.0.9")}, nil},
		{"this site's config, no entry", map[string]string{conf: wgConf("10.44.0.1")}, nil},
		{"another site's entry", map[string]string{Path: registryHolding(t, addressed("home-b", "10.44.0.2")), conf: wgConf("10.44.0.2")},
			[]string{"already runs site home-b (10.44.0.2) of this deployment", "A site is a machine", "give one site several roles", "a second environment is a second deployment", "sites.home-a.ssh"}},
		{"another site's config, no entry", map[string]string{conf: wgConf("10.44.0.2")},
			[]string{"already runs site home-b (10.44.0.2)", "A site is a machine"}},
		{"a site no longer declared", map[string]string{Path: registryHolding(t, addressed("vm", "10.44.0.3"))},
			[]string{"site vm (10.44.0.3) of this deployment, which paisans-stack.yaml no longer declares", "paisans site remove vm --force --ssh"}},
		{"an address no site has", map[string]string{conf: wgConf("10.44.0.7")},
			[]string{"address 10.44.0.7", "no site in paisans-stack.yaml has that address", "--id f2a9"}},
		{"a kept Caddy", map[string]string{Path: registryHolding(t, KeepCaddy(addressed("home-b", "10.44.0.2")))},
			[]string{"site home-b (10.44.0.2) of this deployment, or what its removal kept (caddy), so home-a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := machineConfig()
			for _, claim := range []struct {
				name string
				run  func(*machineHost) error
			}{
				{"check", func(h *machineHost) error { return Check(h, cfg, "home-a") }},
				{"claim", func(h *machineHost) error { return Claim(h, cfg, "home-a", time.Now()) }},
			} {
				h := &machineHost{files: tc.files}
				err := claim.run(h)
				if tc.want == nil {
					if err != nil {
						t.Errorf("%s refused: %v", claim.name, err)
					}
					continue
				}
				if err == nil {
					t.Errorf("%s passed, want a refusal", claim.name)
					continue
				}
				for _, phrase := range tc.want {
					if !strings.Contains(err.Error(), phrase) {
						t.Errorf("%s: the refusal does not say %q:\n%v", claim.name, phrase, err)
					}
				}
				if len(h.ran) > 0 {
					t.Errorf("%s ran %q after refusing", claim.name, h.ran)
				}
			}
		})
	}
}

// raceHost answers the claim as a merge that found this deployment's entry
// under another site, which is what a second claim racing the first meets.
type raceHost struct{ machineHost }

func (h *raceHost) Run(string) (string, error) {
	line, _ := entryLine(ours, entry("f2a9", "example.org", "home-b"))
	return conflictMarker + line, errors.New("exit status 3")
}

// A claim the merge refuses for another site of this deployment is refused in
// the same words as one found before it ran.
func TestClaimRaceNamesTheOtherSite(t *testing.T) {
	h := &raceHost{machineHost{files: map[string]string{}}}
	err := Claim(h, machineConfig(), "home-a", time.Now())
	if err == nil || !strings.Contains(err.Error(), "already runs site home-b of this deployment") || !strings.Contains(err.Error(), "A site is a machine") {
		t.Errorf("Claim = %v", err)
	}
}

func TestConfAddress(t *testing.T) {
	for in, want := range map[string]string{
		wgConf("10.44.0.1"):                  "10.44.0.1",
		"[Interface]\naddress=10.44.0.4\n":   "10.44.0.4",
		"Address = 10.44.0.5/24, fd00::5/64": "10.44.0.5",
		"[Interface]\nPrivateKey = x\n":      "",
	} {
		if got := ConfAddress(in); got != want {
			t.Errorf("ConfAddress(%q) = %q, want %q", in, got, want)
		}
	}
}
