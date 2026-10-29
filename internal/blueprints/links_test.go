package blueprints

import (
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// A locked setting with a condition is locked only while it holds, and its
// order is the blueprint's.
func TestLockedSettingsFollowTheirConditions(t *testing.T) {
	d, err := Parse("example", []byte(strings.Replace(minimal, "config: {format: env, file: .env}", `config:
  format: env
  file: .env
  locked_settings:
    - SITE_NAME
    - {name: SSO_ONLY, when: app.oidc}
    - {name: PRIVATE, when: app.gated}`, 1)), always)
	if err != nil {
		t.Fatal(err)
	}
	register("test/locked", d, t.TempDir())
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		delete(defs, "test/locked")
		delete(sources, "test/locked")
	})
	for _, c := range []struct {
		gated, oidc bool
		want        string
	}{
		{false, false, "SITE_NAME"},
		{true, false, "SITE_NAME,PRIVATE"},
		{true, true, "SITE_NAME,SSO_ONLY,PRIVATE"},
	} {
		got := strings.Join(LockedSettingsFor(config.App{Blueprint: "test/locked", Gated: c.gated}, c.oidc), ",")
		if got != c.want {
			t.Errorf("gated %v, oidc %v: %s, want %s", c.gated, c.oidc, got, c.want)
		}
	}
}

// A provider declares what it offers, each value a string over the app, and
// a consumer says which setting names its provider.
func TestALinkMistakeIsRefused(t *testing.T) {
	for change, want := range map[string]string{
		"provides:\n  - {role: Mail Server, values: {x: \"'a'\"}}\n":                                    "provides[0].role",
		"provides:\n  - {role: matrix-homeserver}\n":                                                    "provides[0].values",
		"provides:\n  - {role: matrix-homeserver, values: {base_url: \"1 + 1\"}}\n":                     "provides[0].values.base_url",
		"consumes:\n  - {role: matrix-homeserver}\n":                                                    "consumes[0].setting",
		"consumes:\n  - {role: Nothing, setting: x}\n":                                                  "consumes[0].role",
		"provides:\n  - {role: matrix-homeserver, values: {base_url: \"'https://' + app.hostname\"}}\n": "",
		"consumes:\n  - {role: matrix-homeserver, setting: homeserver}\n":                               "",
	} {
		_, err := Parse("example", []byte(minimal+change), always)
		switch {
		case want == "" && err != nil:
			t.Errorf("%q: %v", change, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%q: got %v, want %q", change, err, want)
		}
	}
}

// colocate is a v4 key: a blueprint that declares it names the format an
// older toolkit skips, and an older format declaring it is refused by name.
func TestColocateNeedsV4(t *testing.T) {
	consume := "consumes:\n  - {role: matrix-homeserver, setting: homeserver, optional: true, colocate: true}\n"
	d, err := Parse("example", []byte(minimalV4+consume), always)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Consumes) != 1 || !d.Consumes[0].Colocate {
		t.Fatalf("consumes %+v, want one colocated", d.Consumes)
	}
	for _, doc := range []string{minimal, minimalV2, minimalV3} {
		_, err := Parse("example", []byte(doc+consume), always)
		if err == nil || !strings.Contains(err.Error(), "consumes[0].colocate") || !strings.Contains(err.Error(), APIVersion) {
			t.Errorf("%v, want a refusal naming consumes[0].colocate and %s", err, APIVersion)
		}
	}
}

// A provider's values are its blueprint's expressions over the app, and one
// outside the deployment offers what any provider of the role says of its
// hostname alone.
func TestLinkValuesAreTheProvidersExpressions(t *testing.T) {
	cfg := &config.Config{Apps: map[string]config.App{"chat": {Blueprint: "synapse", Hostname: "chat.example.org", Hostnames: map[string]string{WellknownRole: "example.org"}}}}
	got, err := LinkValues(cfg, "chat", "matrix-homeserver")
	if err != nil || got["base_url"] != "https://chat.example.org" || got["server_name"] != "example.org" {
		t.Errorf("got %v, %v", got, err)
	}
	away, err := LinkValuesAt("matrix-homeserver", "matrix.example.net")
	if err != nil || away["base_url"] != "https://matrix.example.net" || away["server_name"] != "matrix.example.net" {
		t.Errorf("outside the deployment: %v, %v", away, err)
	}
	if _, err := LinkValuesAt("nothing-provides-this", "x.example.org"); err == nil {
		t.Error("a role nobody provides was answered")
	}
}

// A provider's values may name the mesh address of the site a pinned app
// runs on; a clustered app has none, and neither has one outside the
// deployment.
func TestMeshAddressIsThePinnedSites(t *testing.T) {
	cfg := &config.Config{
		Sites: map[string]config.Site{"vm": {Address: "10.44.0.3"}},
		Apps: map[string]config.App{
			"chat": {Blueprint: "synapse", Hostname: "chat.example.org", Placement: config.Placement{Mode: config.PlacementPinned, Site: "vm"}},
		},
	}
	got, err := LinkValues(cfg, "chat", "matrix-homeserver")
	if err != nil || got["mas_admin_url"] != "http://10.44.0.3:18010" {
		t.Errorf("pinned: %v, %v", got, err)
	}
	cfg.Apps["chat"] = config.App{Blueprint: "synapse", Hostname: "chat.example.org", Placement: config.Placement{Mode: config.PlacementCluster}}
	if got, _ := LinkValues(cfg, "chat", "matrix-homeserver"); got["mas_admin_url"] != "http://:18010" {
		t.Errorf("clustered: %v", got["mas_admin_url"])
	}
	if v := celApp(config.App{Placement: config.Placement{Mode: config.PlacementPinned, Site: "vm"}})["app.mesh_address"]; v != "" {
		t.Errorf("with no deployment around it: %q", v)
	}
}

// A consumer shares only its own self rotated secrets, each once, and reads
// one link per role.
func TestASharedSecretMistakeIsRefused(t *testing.T) {
	secrets := "secrets:\n  generate:\n    - {name: s, format: password}\n  self_rotated: [s]\n"
	for change, want := range map[string]string{
		"consumes:\n  - {role: matrix-homeserver, setting: h, optional: true, shared_secrets: [s]}\n" + secrets:  "",
		"consumes:\n  - {role: matrix-homeserver, setting: h, shared_secrets: [nope]}\n" + secrets:               "consumes[0].shared_secrets[0]: nope is not one of secrets.generate",
		"consumes:\n  - {role: matrix-homeserver, setting: h, shared_secrets: [s]}\nsecrets:\n  generate: [s]\n": "is not in secrets.self_rotated",
		"consumes:\n  - {role: matrix-homeserver, setting: h, shared_secrets: [s, s]}\n" + secrets:               "is shared twice",
		"consumes:\n  - {role: matrix-homeserver, setting: h}\n  - {role: matrix-homeserver, setting: i}\n":      "consumes[1].role: matrix-homeserver is consumed twice",
	} {
		_, err := Parse("example", []byte(minimal+change), always)
		switch {
		case want == "" && err != nil:
			t.Errorf("%q: %v", change, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%q: got %v, want %q", change, err, want)
		}
	}
}

// registerConsumer registers a blueprint that consumes matrix-homeserver
// optionally and shares its secret s, for the length of the test.
func registerConsumer(t *testing.T) {
	t.Helper()
	d, err := Parse("example", []byte(minimal+"consumes:\n  - {role: matrix-homeserver, setting: homeserver, optional: true, shared_secrets: [s]}\nsecrets:\n  generate:\n    - {name: s, format: password}\n  self_rotated: [s]\n"), always)
	if err != nil {
		t.Fatal(err)
	}
	register("test/consumer", d, t.TempDir())
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		delete(defs, "test/consumer")
		delete(sources, "test/consumer")
	})
}

// An optional consume left unset links to the deployment's one provider of
// its role, or to none; a set one links to the provider at that hostname.
func TestLinkOfAnOptionalConsume(t *testing.T) {
	registerConsumer(t)
	c := Consume{Role: "matrix-homeserver", Setting: "homeserver", Optional: true}
	digest := config.App{Blueprint: "test/consumer", Hostname: "digest.example.org"}
	chat := config.App{Blueprint: "synapse", Hostname: "chat.example.org"}
	chat2 := config.App{Blueprint: "synapse", Hostname: "chat2.example.org"}

	cfg := &config.Config{Apps: map[string]config.App{"digest": digest}}
	if got, err := LinkOf(cfg, "digest", c); err != nil || got != (Link{Role: "matrix-homeserver"}) {
		t.Errorf("no provider: %+v, %v", got, err)
	}
	cfg.Apps["chat"] = chat
	if got, err := LinkOf(cfg, "digest", c); err != nil || got != (Link{Role: "matrix-homeserver", Linked: true, Provider: "chat", Hostname: "chat.example.org"}) {
		t.Errorf("one provider: %+v, %v", got, err)
	}
	cfg.Apps["chat2"] = chat2
	if _, err := LinkOf(cfg, "digest", c); err == nil || err.Error() != "apps.digest.settings.homeserver: unset, and 2 apps provide matrix-homeserver (chat, chat2); name one by its hostname" || !errors.Is(err, ErrLinkAmbiguous) {
		t.Errorf("two providers: %v", err)
	}
	digest.Settings = map[string]any{"homeserver": "chat2.example.org"}
	cfg.Apps["digest"] = digest
	if got, err := LinkOf(cfg, "digest", c); err != nil || got != (Link{Role: "matrix-homeserver", Linked: true, Provider: "chat2", Hostname: "chat2.example.org"}) {
		t.Errorf("named: %+v, %v", got, err)
	}
	digest.Settings = map[string]any{"homeserver": "matrix.example.net"}
	cfg.Apps["digest"] = digest
	if got, err := LinkOf(cfg, "digest", c); err != nil || got != (Link{Role: "matrix-homeserver", Linked: true, Hostname: "matrix.example.net"}) {
		t.Errorf("outside the deployment: %+v, %v", got, err)
	}
}

// A consume without optional left unset means a provider at the consumer's
// own hostname, whatever else the deployment runs.
func TestLinkOfWithoutOptionalIsUnchanged(t *testing.T) {
	cfg := &config.Config{Apps: map[string]config.App{
		"web":  {Blueprint: "element", Hostname: "web.example.org"},
		"chat": {Blueprint: "synapse", Hostname: "chat.example.org"},
	}}
	c := Consumes("element")[0]
	got, err := LinkOf(cfg, "web", c)
	if err != nil || got != (Link{Role: "matrix-homeserver", Linked: true, Hostname: "web.example.org"}) {
		t.Errorf("got %+v, %v", got, err)
	}
	cfg.Apps["web"] = config.App{Blueprint: "element", Hostname: "web.example.org", Settings: map[string]any{"homeserver": "chat.example.org"}}
	got, err = LinkOf(cfg, "web", c)
	if err != nil || got != (Link{Role: "matrix-homeserver", Linked: true, Provider: "chat", Hostname: "chat.example.org"}) {
		t.Errorf("named: %+v, %v", got, err)
	}
}

// A provider's consumers are the apps whose consume links to it, sorted, and
// a consumer's shared secret is shared with the provider it links to.
func TestConsumersOfAndSharedSecretProvider(t *testing.T) {
	registerConsumer(t)
	cfg := &config.Config{Apps: map[string]config.App{
		"b":    {Blueprint: "test/consumer", Hostname: "b.example.org"},
		"a":    {Blueprint: "test/consumer", Hostname: "a.example.org"},
		"chat": {Blueprint: "synapse", Hostname: "chat.example.org"},
	}}
	if got := ConsumersOf(cfg, "chat", "matrix-homeserver"); strings.Join(got, ",") != "a,b" {
		t.Errorf("consumers: %v", got)
	}
	if got := ConsumersOf(cfg, "", "matrix-homeserver"); got != nil {
		t.Errorf("consumers of no provider: %v", got)
	}
	if got, ok := SharedSecretProvider(cfg, "a", "s"); got != "chat" || !ok {
		t.Errorf("shared: %q, %v", got, ok)
	}
	if _, ok := SharedSecretProvider(cfg, "a", "other"); ok {
		t.Error("a secret the consumer does not share has a provider")
	}
	delete(cfg.Apps, "chat")
	if _, ok := SharedSecretProvider(cfg, "a", "s"); ok {
		t.Error("a secret is shared with no provider")
	}
	// With no provider the optional consumes link nowhere, and they are
	// nobody's consumers, not the consumers of provider "".
	if got := ConsumersOf(cfg, "", "matrix-homeserver"); got != nil {
		t.Errorf("unlinked consumers: %v", got)
	}
}
