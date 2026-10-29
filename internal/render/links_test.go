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
	if err == nil || !strings.Contains(err.Error(), "at matrix.example.net is outside the deployment") {
		t.Errorf(`LinkedApp("matrix-homeserver") error = %v, want one saying "at matrix.example.net is outside the deployment"`, err)
	}
}

// chat's consumers are digest, carrying the secret it shares, and web, which
// shares none, in that order.
func TestConsumersCarryTheSharedSecret(t *testing.T) {
	cfg, secrets := linksFixture(t)
	v := valuesOf(t, cfg, secrets, "chat")
	got, err := v.Consumers("matrix-homeserver")
	if err != nil {
		t.Fatalf(`Consumers("matrix-homeserver"): %v`, err)
	}
	want := []Consumer{
		{Name: "digest", Secrets: map[string]string{"mas_admin_client_secret": "fixture-not-a-secret-digest-mas-admin"}, Values: digestValues},
		{Name: "notify", Secrets: map[string]string{"mas_admin_client_secret": "fixture-not-a-secret-notify-mas-admin"}, Values: map[string]string{}},
		{Name: "web", Secrets: map[string]string{}, Values: map[string]string{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf(`Consumers("matrix-homeserver") = %#v, want %#v`, got, want)
	}
}

// digestValues is what the fixture's engagement app shows its providers.
var digestValues = map[string]string{
	"community_room_alias": "fixture-community",
	"mbin_agent_client_id": "engagement-agent",
	"mbin_agent_username":  "community-bot",
}

// The forum sees the engagement app with the Mbin agent client's secret,
// which the mbin app declares the client with.
func TestForumConsumersCarryTheMbinSecret(t *testing.T) {
	cfg, secrets := linksFixture(t)
	v := valuesOf(t, cfg, secrets, "talk")
	got, err := v.Consumers("forum")
	if err != nil {
		t.Fatalf(`Consumers("forum"): %v`, err)
	}
	want := []Consumer{
		{Name: "digest", Secrets: map[string]string{"mbin_agent_client_secret": "fixture-not-a-secret-digest-mbin-agent"}, Values: digestValues},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf(`Consumers("forum") = %#v, want %#v`, got, want)
	}
}

// A shared secret not yet generated leaves the consumer listed without it.
func TestConsumersLeaveOutAnUnfilledSecret(t *testing.T) {
	cfg, secrets := linksFixture(t)
	delete(secrets.Apps["digest"], "mas_admin_client_secret")
	v := valuesOf(t, cfg, secrets, "chat")
	got, err := v.Consumers("matrix-homeserver")
	if err != nil {
		t.Fatalf(`Consumers("matrix-homeserver"): %v`, err)
	}
	if len(got) == 0 || got[0].Name != "digest" {
		t.Fatalf(`Consumers("matrix-homeserver") = %#v, want digest first`, got)
	}
	if _, ok := got[0].Secrets["mas_admin_client_secret"]; ok {
		t.Errorf("digest's Secrets %v hold a secret that is not set", got[0].Secrets)
	}
}

// With no homeserver in the deployment, digest's optional consume is simply
// unlinked, and every read of the role says so.
func TestAnOptionalConsumeWithNoProvider(t *testing.T) {
	cfg, secrets := linksFixture(t)
	delete(cfg.Apps, "chat")
	delete(cfg.Apps, "web")
	v := valuesOf(t, cfg, secrets, "digest")
	if v.HasLinked("matrix-homeserver") {
		t.Error(`HasLinked("matrix-homeserver") is true with no homeserver`)
	}
	if _, err := v.LinkedApp("matrix-homeserver"); err == nil || !strings.Contains(err.Error(), "is not linked") {
		t.Errorf(`LinkedApp("matrix-homeserver") error = %v, want one saying "is not linked"`, err)
	}
	if _, err := v.Linked("matrix-homeserver", "base_url"); err == nil || !strings.Contains(err.Error(), "guard .Linked with .HasLinked") {
		t.Errorf(`Linked("matrix-homeserver", "base_url") error = %v, want one saying "guard .Linked with .HasLinked"`, err)
	}
}

// A synapse blueprint that does not offer mas_admin_url, one released before
// it, renders the digest with the bot off rather than failing.
func TestEngagementWithAnOlderHomeserverRendersNoMatrix(t *testing.T) {
	cfg, secrets := linksFixture(t)
	p := newPlanner(cfg, secrets)
	v := valuesOf(t, cfg, secrets, "digest")
	delete(v.links["matrix-homeserver"], "mas_admin_url")
	env, err := p.renderFile("engagement/.env.secret.tmpl", v)
	if err != nil {
		t.Fatalf("rendering the digest's .env: %v", err)
	}
	if strings.Contains(env, "ENGAGEMENT_MAS_") || strings.Contains(env, "ENGAGEMENT_MATRIX_") {
		t.Errorf("the digest's .env renders Matrix with no mas_admin_url:\n%s", env)
	}
}

// An Mbin blueprint that provides no forum, or one that does not offer
// api_port, renders the digest with the Mbin welcomes off rather than
// failing.
func TestEngagementWithAnOlderForumRendersNoAgent(t *testing.T) {
	for name, older := range map[string]func(appValues){
		"no forum":    func(v appValues) { delete(v.links, "forum") },
		"no api_port": func(v appValues) { delete(v.links["forum"], "api_port") },
	} {
		cfg, secrets := linksFixture(t)
		p := newPlanner(cfg, secrets)
		v := valuesOf(t, cfg, secrets, "digest")
		older(v)
		env, err := p.renderFile("engagement/.env.secret.tmpl", v)
		if err != nil {
			t.Fatalf("%s: rendering the digest's .env: %v", name, err)
		}
		if strings.Contains(env, "ENGAGEMENT_MBIN_API_URL") || strings.Contains(env, "ENGAGEMENT_MBIN_AGENT_") {
			t.Errorf("%s: the digest's .env renders the agent client:\n%s", name, env)
		}
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
