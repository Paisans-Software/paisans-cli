package validate_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// withHomeservers is the engagement fixture with a synapse app at each of the
// hostnames, pinned to home-a and home-b in turn.
func withHomeservers(t *testing.T, hostnames ...string) *config.Config {
	t.Helper()
	cfg := load(t, "engagement-valid")
	sites := []string{"home-a", "home-b"}
	for i, h := range hostnames {
		name := "chat" + string(rune('a'+i))
		cfg.Apps[name] = config.App{
			Blueprint: "synapse",
			Hostname:  h,
			Placement: config.Placement{Mode: config.PlacementPinned, Site: sites[i]},
		}
	}
	return cfg
}

func setDigestHomeserver(cfg *config.Config, hostname string) {
	app := cfg.Apps["digest"]
	app.Settings = map[string]any{"homeserver": hostname}
	cfg.Apps["digest"] = app
}

func linkFinding(result validate.Result, rule string) (validate.Finding, bool) {
	for _, f := range result.Findings {
		if f.Rule == rule {
			return f, true
		}
	}
	return validate.Finding{}, false
}

func TestSeveralHomeserversNeedTheSetting(t *testing.T) {
	cfg := withHomeservers(t, "home-a.example.org", "home-b.example.org")
	result := validate.Check(cfg)
	if !refusedFor(result, "link-provider-ambiguous", "apps.digest.settings.homeserver") {
		t.Fatalf("not refused: %v", result.Findings)
	}
	f, _ := linkFinding(result, "link-provider-ambiguous")
	for _, want := range []string{"matrix-homeserver", "home-a.example.org", "home-b.example.org"} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("message %q does not name %s", f.Message, want)
		}
	}
	if _, doubled := linkFinding(result, "link-shared-secret-outside"); doubled {
		t.Errorf("the same app is also reported as outside: %v", result.Findings)
	}

	setDigestHomeserver(cfg, "home-b.example.org")
	result = validate.Check(cfg)
	if refusedFor(result, "link-provider-ambiguous", "apps.digest.settings.homeserver") {
		t.Errorf("still refused with the setting naming a homeserver: %v", result.Findings)
	}
}

func TestAHomeserverSettingNamingNoHomeserver(t *testing.T) {
	cfg := withHomeservers(t, "home-a.example.org")
	setDigestHomeserver(cfg, "matrix.example.net")
	result := validate.Check(cfg)
	if !refusedFor(result, "link-shared-secret-outside", "apps.digest.settings.homeserver") {
		t.Fatalf("not refused: %v", result.Findings)
	}
	f, _ := linkFinding(result, "link-shared-secret-outside")
	for _, want := range []string{"matrix.example.net", "home-a.example.org"} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("message %q does not name %s", f.Message, want)
		}
	}
}

func TestASecretOutsideWithNoHomeserverSaysToRemoveTheSetting(t *testing.T) {
	cfg := withHomeservers(t)
	setDigestHomeserver(cfg, "matrix.example.net")
	result := validate.Check(cfg)
	f, ok := linkFinding(result, "link-shared-secret-outside")
	if !ok || f.Key != "apps.digest.settings.homeserver" {
		t.Fatalf("not refused at the setting: %v", result.Findings)
	}
	if !strings.Contains(f.Message, "Remove the setting") {
		t.Errorf("message %q does not say to remove the setting", f.Message)
	}
}

func TestOneHomeserverIsQuiet(t *testing.T) {
	result := validate.Check(withHomeservers(t, "home-a.example.org"))
	for _, rule := range []string{"link-provider-ambiguous", "link-shared-secret-outside"} {
		if _, fired := linkFinding(result, rule); fired {
			t.Errorf("%s fired: %v", rule, result.Findings)
		}
	}
}

func TestElementOutsideTheDeploymentIsStillAllowed(t *testing.T) {
	cfg := load(t, "engagement-valid")
	cfg.Apps["web"] = config.App{
		Blueprint: "element",
		Hostname:  "web.example.org",
		Placement: config.Placement{Mode: config.PlacementCluster},
		Settings:  map[string]any{"homeserver": "matrix.example.net"},
	}
	result := validate.Check(cfg)
	for _, rule := range []string{"link-provider-ambiguous", "link-shared-secret-outside"} {
		if _, fired := linkFinding(result, rule); fired {
			t.Errorf("%s fired: %v", rule, result.Findings)
		}
	}
}
