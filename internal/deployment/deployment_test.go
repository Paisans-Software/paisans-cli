package deployment_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/deployment"
)

const fixtureID = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"

func TestNewIDIsAValidVersion4ID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id, err := deployment.NewID()
		if err != nil {
			t.Fatal(err)
		}
		if !deployment.ValidID(id) {
			t.Fatalf("NewID returned %q, which ValidID refuses", id)
		}
		if seen[id] {
			t.Fatalf("NewID returned %q twice", id)
		}
		seen[id] = true
	}
}

func TestValidID(t *testing.T) {
	for id, want := range map[string]bool{
		fixtureID:                              true,
		"F2A9C4E1-0B7D-4C3A-9E2F-5A6B7C8D9E01": false,
		"f2a9c4e1-0b7d-1c3a-9e2f-5a6b7c8d9e01": false,
		"f2a9c4e1-0b7d-4c3a-7e2f-5a6b7c8d9e01": false,
		"f2a9c4e10b7d4c3a9e2f5a6b7c8d9e01":     false,
		"":                                     false,
	} {
		if got := deployment.ValidID(id); got != want {
			t.Errorf("ValidID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestNamesAndPathsDeriveFromTheToken(t *testing.T) {
	d := deployment.Deployment{ID: fixtureID}
	for got, want := range map[string]string{
		d.Token():                 "f2a9",
		d.Prefix():                "paisans-f2a9",
		d.Project("talk"):         "paisans-f2a9-talk",
		d.Root():                  "/srv/paisans/f2a9",
		d.Rel():                   "srv/paisans/f2a9",
		d.Compose("infra"):        "/srv/paisans/f2a9/infra/compose.yaml",
		d.ComposeCmd("talk"):      "docker compose -f /srv/paisans/f2a9/talk/compose.yaml",
		d.Path("infra", "x"):      "/srv/paisans/f2a9/infra/x",
		d.RelPath("talk", ".env"): "srv/paisans/f2a9/talk/.env",
		d.LabelFilter():           "label=community.paisans.deployment=" + fixtureID,
		d.Interface():             "psns-f2a9",
		d.WireGuardConf():         "srv/paisans/f2a9/psns-f2a9.conf",
		d.WireGuardInstalled():    "/etc/wireguard/psns-f2a9.conf",
		d.WireGuardUnit():         "wg-quick@psns-f2a9",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestSplitRel(t *testing.T) {
	token, stack, rest, ok := deployment.SplitRel("srv/paisans/f2a9/talk/config/x.yaml")
	if !ok || token != "f2a9" || stack != "talk" || rest != "config/x.yaml" {
		t.Errorf("SplitRel = %q %q %q %v", token, stack, rest, ok)
	}
	for _, rel := range []string{"srv/paisans/f2a9/psns-f2a9.conf", "srv/talk/.env", "srv/paisans/f2a9/talk", "srv/paisans/f2a9/.paisans-manifest.json"} {
		if _, _, _, ok := deployment.SplitRel(rel); ok {
			t.Errorf("SplitRel(%q) reported a stack", rel)
		}
	}
}

// The interface name fits Linux's limit for any id, since the token has a
// fixed length.
func TestInterfaceFitsIFNAMSIZ(t *testing.T) {
	id, err := deployment.NewID()
	if err != nil {
		t.Fatal(err)
	}
	name := deployment.Deployment{ID: id}.Interface()
	if len(name) != 9 || len(name) > deployment.MaxInterfaceName {
		t.Errorf("Interface() = %q, %d characters, want 9 and at most %d", name, len(name), deployment.MaxInterfaceName)
	}
}

// The mark is the token in the low sixteen bits under a fixed "PS" in the
// high sixteen, so two deployments on a host, whose tokens the registry
// keeps apart, never share one, and the helper and the egress table read
// the same value.
func TestFwMark(t *testing.T) {
	for _, c := range []struct {
		id   string
		want uint32
	}{
		{"f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01", 0x5053f2a9},
		{"0000c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01", 0x50530000},
		{"ffffc4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01", 0x5053ffff},
	} {
		d := deployment.Deployment{ID: c.id}
		if got, err := d.FwMark(); err != nil || got != c.want {
			t.Errorf("%s: FwMark() = %#x, %v, want %#x", c.id, got, err, c.want)
		}
	}
	d := deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
	if d.EgressTable() != "paisans_f2a9" || d.EgressUnit() != "paisans-f2a9-egress.service" {
		t.Errorf("EgressTable() = %q, EgressUnit() = %q", d.EgressTable(), d.EgressUnit())
	}
}

// A token that is not exactly TokenLength lower-case hex digits has no mark,
// so it can never share token 0000's.
func TestFwMarkRefusesABadToken(t *testing.T) {
	for _, id := range []string{"", "f2a", "zzzz", "F2A9c4e1", "f2a 9", "+2a9c4e1", "0x2a9c4e1"} {
		d := deployment.Deployment{ID: id}
		if got, err := d.FwMark(); err == nil {
			t.Errorf("ID %q: FwMark() = %#x, want an error", id, got)
		}
	}
}

// The toolkit's own records on a host sit in the deployment's root, where
// its user writes them without sudo.
func TestStateDir(t *testing.T) {
	d := deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
	if d.StateDir() != "/srv/paisans/f2a9/.paisans" || d.StatePath("deployed.json") != "/srv/paisans/f2a9/.paisans/deployed.json" {
		t.Errorf("StateDir %s, StatePath %s", d.StateDir(), d.StatePath("deployed.json"))
	}
	lock := d.StateLockCommand("the deployment record")
	for _, want := range []string{"umask 077", "[ -L '/srv/paisans/f2a9/.paisans' ]", "install -d -m 700 '/srv/paisans/f2a9/.paisans'", "exec 9>>'/srv/paisans/f2a9/.paisans/lock'", "flock -w 60 9"} {
		if !strings.Contains(lock, want) {
			t.Errorf("the lock prelude has no %q:\n%s", want, lock)
		}
	}
	if strings.Contains(lock, "/var/lib/paisans") || strings.Contains(lock, "sudo") {
		t.Errorf("the prelude reaches root's directory: %s", lock)
	}
}
