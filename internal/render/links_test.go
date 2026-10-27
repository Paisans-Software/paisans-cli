package render

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// linksFixture is the render fixture's configuration and secrets.
func linksFixture(t *testing.T) (*config.Config, *config.Secrets) {
	t.Helper()
	cfg, err := config.Load(filepath.Join("testdata", "deployment.yaml"))
	if err != nil {
		t.Fatalf("loading the fixture configuration: %v", err)
	}
	secrets, err := config.LoadSecrets(filepath.Join("testdata", "secrets.fixture.yaml"))
	if err != nil {
		t.Fatalf("loading the fixture secrets: %v", err)
	}
	return cfg, secrets
}

// valuesOf is what the templates of the app called app are given.
func valuesOf(t *testing.T, cfg *config.Config, secrets *config.Secrets, app string) appValues {
	t.Helper()
	p := newPlanner(cfg, secrets)
	if err := p.placeApps(); err != nil {
		t.Fatalf("placing the apps: %v", err)
	}
	planned, err := p.plannedFor(app, cfg.Apps[app])
	if err != nil {
		t.Fatalf("planning %s: %v", app, err)
	}
	v, err := p.values(planned, cfg.Apps[app])
	if err != nil {
		t.Fatalf("the values of %s: %v", app, err)
	}
	return v
}

func TestHasLinkedAndLinkedApp(t *testing.T) {
	cfg, secrets := linksFixture(t)
	v := valuesOf(t, cfg, secrets, "web")
	if !v.HasLinked("matrix-homeserver") {
		t.Error(`HasLinked("matrix-homeserver") is false, want true`)
	}
	got, err := v.LinkedApp("matrix-homeserver")
	if err != nil || got != "chat" {
		t.Errorf(`LinkedApp("matrix-homeserver") = %q, %v; want "chat"`, got, err)
	}
	if v.HasLinked("nothing") {
		t.Error(`HasLinked("nothing") is true, want false`)
	}
}

func TestLinkedAppOutsideTheDeploymentFails(t *testing.T) {
	cfg, secrets := linksFixture(t)
	web := cfg.Apps["web"]
	web.Settings = map[string]any{"homeserver": "matrix.example.net"}
	cfg.Apps["web"] = web
	v := valuesOf(t, cfg, secrets, "web")
	if !v.HasLinked("matrix-homeserver") {
		t.Error(`HasLinked("matrix-homeserver") is false, want true`)
	}
	_, err := v.LinkedApp("matrix-homeserver")
	if err == nil || !strings.Contains(err.Error(), "outside the deployment") {
		t.Errorf(`LinkedApp("matrix-homeserver") error = %v, want one saying "outside the deployment"`, err)
	}
}

func TestConsumersOfAProvider(t *testing.T) {
	cfg, secrets := linksFixture(t)
	v := valuesOf(t, cfg, secrets, "chat")
	got, err := v.Consumers("matrix-homeserver")
	if err != nil {
		t.Fatalf(`Consumers("matrix-homeserver"): %v`, err)
	}
	want := []Consumer{{Name: "web", Secrets: map[string]string{}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf(`Consumers("matrix-homeserver") = %#v, want %#v`, got, want)
	}
}

func TestConsumersOfARoleNotProvidedFails(t *testing.T) {
	cfg, secrets := linksFixture(t)
	v := valuesOf(t, cfg, secrets, "web")
	_, err := v.Consumers("matrix-homeserver")
	if err == nil || !strings.Contains(err.Error(), "a role it does not provide") {
		t.Errorf(`Consumers("matrix-homeserver") error = %v, want one saying "a role it does not provide"`, err)
	}
}

func TestLinkedOffers(t *testing.T) {
	cfg, secrets := linksFixture(t)
	v := valuesOf(t, cfg, secrets, "web")
	for _, c := range []struct {
		role, value string
		want        bool
	}{
		{"matrix-homeserver", "server_name", true},
		{"matrix-homeserver", "nothing", false},
		{"nothing", "x", false},
	} {
		if got := v.LinkedOffers(c.role, c.value); got != c.want {
			t.Errorf("LinkedOffers(%q, %q) = %v, want %v", c.role, c.value, got, c.want)
		}
	}
}

func TestLinkedStillFailsForAnUndeclaredValue(t *testing.T) {
	cfg, secrets := linksFixture(t)
	v := valuesOf(t, cfg, secrets, "web")
	_, err := v.Linked("matrix-homeserver", "nothing")
	if err == nil || !strings.Contains(err.Error(), "offers no value") {
		t.Errorf(`Linked("matrix-homeserver", "nothing") error = %v, want one saying "offers no value"`, err)
	}
}
