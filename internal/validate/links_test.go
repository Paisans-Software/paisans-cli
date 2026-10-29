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

// sharingBlueprint consumes matrix-homeserver without optional and shares
// its secret s, so an unset homeserver means the app's own hostname.
const sharingBlueprint = `apiVersion: paisans.blueprint/v1
name: notes
services:
  - {name: app, image: "ghcr.io/example/notes:1"}
database: {engine: none}
port: 8400
config: {format: env, file: .env}
settings:
  homeserver: {type: string}
consumes:
  - {role: matrix-homeserver, setting: homeserver, shared_secrets: [s]}
secrets:
  generate:
    - {name: s, format: password}
  self_rotated: [s]
`

// A shared secret's link left unset leads to the app's own hostname, and the
// remedy is to name a provider, not to remove a setting that is not there.
func TestASecretOutsideWithTheSettingUnsetSaysToSetIt(t *testing.T) {
	cfg := loadWithBlueprint(t, sharingBlueprint, "")
	homeservers := map[string]config.App{}
	for name, app := range cfg.Apps {
		if app.Blueprint == "synapse" {
			homeservers[name] = app
			delete(cfg.Apps, name)
		}
	}
	result := validate.Check(cfg)
	f, ok := linkFinding(result, "link-shared-secret-outside")
	if !ok || f.Key != "apps.notes" {
		t.Fatalf("not refused at the app: %v", result.Findings)
	}
	if strings.Contains(f.Message, "Remove the setting") || !strings.Contains(f.Message, "Set settings.homeserver to a hostname that provides matrix-homeserver, or add an app that provides it.") {
		t.Errorf("message %q", f.Message)
	}

	if len(homeservers) != 1 {
		t.Fatalf("the fixture has %d homeservers, want one", len(homeservers))
	}
	var hostname string
	for name, app := range homeservers {
		cfg.Apps[name], hostname = app, app.Hostname
	}
	f, _ = linkFinding(validate.Check(cfg), "link-shared-secret-outside")
	if !strings.Contains(f.Message, "Set settings.homeserver to one of "+hostname+".") {
		t.Errorf("with a homeserver, message %q", f.Message)
	}
}

// colocatingBlueprint consumes matrix-homeserver with colocate, optional so
// that a deployment with no homeserver links to none.
const colocatingBlueprint = `apiVersion: paisans.blueprint/v4
name: notes
services:
  - {name: app, image: "ghcr.io/example/notes:1"}
database: {engine: none}
port: 8400
config: {format: env, file: .env}
settings:
  homeserver: {type: string}
consumes:
  - {role: matrix-homeserver, setting: homeserver, optional: true, colocate: true}
`

// placeApps sets the placement of the app called name, and of the fixture's
// one homeserver.
func placeApps(t *testing.T, cfg *config.Config, notes, homeserver config.Placement) string {
	t.Helper()
	app := cfg.Apps["notes"]
	app.Placement = notes
	cfg.Apps["notes"] = app
	var name string
	for n, a := range cfg.Apps {
		if a.Blueprint == "synapse" {
			if name != "" {
				t.Fatalf("the fixture has two homeservers, %s and %s", name, n)
			}
			name = n
			a.Placement = homeserver
			cfg.Apps[n] = a
		}
	}
	if name == "" {
		t.Fatal("the fixture has no homeserver")
	}
	return name
}

// A colocated consume holds the app to its provider's placement: pinned to
// the same site, or clustered beside a clustered provider.
func TestAColocatedAppIsPlacedAsItsProvider(t *testing.T) {
	pinned := func(site string) config.Placement {
		return config.Placement{Mode: config.PlacementPinned, Site: site}
	}
	cluster := config.Placement{Mode: config.PlacementCluster}
	for _, c := range []struct {
		name            string
		notes, provider config.Placement
		refused         bool
		mentions        []string
	}{
		{"pinned to the provider's site", pinned("vm"), pinned("vm"), false, nil},
		{"pinned elsewhere", pinned("home-a"), pinned("vm"), true, []string{"home-a", "vm", "pinned: vm"}},
		{"clustered beside a clustered provider", cluster, cluster, false, nil},
		{"clustered beside a pinned provider", cluster, pinned("vm"), true, []string{"pinned: vm"}},
		{"pinned beside a clustered provider", pinned("vm"), cluster, true, []string{"placement: cluster"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := loadWithBlueprint(t, colocatingBlueprint, "")
			provider := placeApps(t, cfg, c.notes, c.provider)
			result := validate.Check(cfg)
			f, fired := linkFinding(result, "colocated-app-elsewhere")
			if fired != c.refused {
				t.Fatalf("refused %v, want %v: %v", fired, c.refused, result.Findings)
			}
			if !fired {
				return
			}
			if f.Level != validate.Refuse || f.Key != "apps.notes.placement" {
				t.Errorf("finding %s at %s, want a refusal at apps.notes.placement", f.Level, f.Key)
			}
			for _, want := range append([]string{provider}, c.mentions...) {
				if !strings.Contains(f.Message, want) {
					t.Errorf("message %q does not name %s", f.Message, want)
				}
			}
		})
	}
}

// A colocated consume with nothing to be placed beside, an optional one with
// no provider in the deployment, holds the app to nothing.
func TestAColocatedAppWithNoProviderIsQuiet(t *testing.T) {
	cfg := loadWithBlueprint(t, colocatingBlueprint, "")
	for name, app := range cfg.Apps {
		if app.Blueprint == "synapse" {
			delete(cfg.Apps, name)
		}
	}
	if _, fired := linkFinding(validate.Check(cfg), "colocated-app-elsewhere"); fired {
		t.Errorf("refused with no homeserver: %v", validate.Check(cfg).Findings)
	}
}
