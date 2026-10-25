package validate_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// withHomeserver is valid.yaml with a homeserver whose delegation is served on
// wellknown, and the account app moved to apex.
func withHomeserver(t *testing.T, apex, wellknown string) *config.Config {
	t.Helper()
	cfg := load(t, "valid")
	account := cfg.Apps["account"]
	account.Hostname = apex
	cfg.Apps["account"] = account
	cfg.Apps["chat"] = config.App{
		Blueprint: "synapse",
		Hostname:  "chat.example.org",
		Hostnames: map[string]string{"wellknown": wellknown},
		Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-a"},
	}
	return cfg
}

// The case the role exists for: the community's site holds the apex, and the
// homeserver's user identifiers end in it. Both are routed by the gateway's
// one host block for the name, so nothing is claimed twice.
func TestADelegationHostnameMaySitOnAnotherAppsHostname(t *testing.T) {
	cfg := withHomeserver(t, "example.org", "example.org")
	if f := findings(validate.Check(cfg), "duplicate-hostname"); len(f) != 0 {
		t.Errorf("the delegation on the account app's hostname was refused: %v", f)
	}
}

// Sharing is for the delegation documents on another app's primary hostname,
// and nothing else: every other collision is still two site blocks for one
// address.
func TestADelegationHostnameSharesOnlyAnotherAppsPrimary(t *testing.T) {
	// On the homeserver's own primary: the homeserver already serves the
	// documents there itself, so the role names nothing new.
	cfg := withHomeserver(t, "account.example.org", "chat.example.org")
	if f := findings(validate.Check(cfg), "duplicate-hostname"); len(f) != 1 || f[0] != "REFUSE apps.chat.hostnames.wellknown" {
		t.Errorf("the delegation on the homeserver's own hostname: %v", f)
	}

	// On another app's role hostname, here the account app's derived join
	// hostname, whose snippet answers every path itself.
	cfg = withHomeserver(t, "account.example.org", "join.example.org")
	if f := findings(validate.Check(cfg), "duplicate-hostname"); len(f) != 1 || f[0] != "REFUSE apps.chat.hostnames.wellknown" {
		t.Errorf("the delegation on a join hostname: %v", f)
	}

	// Two homeservers delegating on one name: one name in user identifiers
	// can belong to one homeserver only.
	cfg = withHomeserver(t, "example.org", "example.org")
	cfg.Apps["chat2"] = config.App{
		Blueprint: "synapse",
		Hostname:  "chat2.example.org",
		Hostnames: map[string]string{"wellknown": "example.org"},
		Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-b"},
	}
	if f := findings(validate.Check(cfg), "duplicate-hostname"); len(f) != 1 || f[0] != "REFUSE apps.chat2.hostnames.wellknown" {
		t.Errorf("two homeservers delegating on one name: %v", f)
	}
}

// A name shares a host block only when one Caddy routes both apps. An app a
// monitor serves is routed by the monitor's Caddy, so the delegation would be
// on the gateway and the app on the monitor, under one DNS name.
func TestADelegationHostnameIsNotSharedAcrossCaddies(t *testing.T) {
	cfg := withHomeserver(t, "account.example.org", "health.example.org")
	var said string
	for _, f := range validate.Check(cfg).Findings {
		if f.Rule == "duplicate-hostname" {
			said = f.Key + ": " + f.Message
		}
	}
	if !strings.HasPrefix(said, "apps.chat.hostnames.wellknown: ") || !strings.Contains(said, "monitor") {
		t.Errorf("the delegation on a monitor's app: %q", said)
	}
}
