package kinds

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
)

const minimal = `apiVersion: paisans.blueprint/v1
name: example
services:
  - {name: app, image: "example/app:1"}
database: {engine: none}
port: 8000
config: {format: env, file: .env}
health: {path: /status, status: [200]}
`

func always(string) bool { return true }

// The smallest blueprint that says what the toolkit needs is accepted, and
// reads back as the record it describes.
func TestAMinimalBlueprintParses(t *testing.T) {
	d, err := Parse("example", []byte(minimal), always)
	if err != nil {
		t.Fatal(err)
	}
	if d.Port != 8000 || d.Health.Expect != "200" || d.ConfigFormat != ConfigEnv || d.Services[0].Image != "example/app:1" {
		t.Errorf("%+v", d)
	}
}

// Each mistake is refused by name when the blueprint is read, rather than
// rendering an app without what its author meant.
func TestABlueprintMistakeIsRefused(t *testing.T) {
	cases := []struct{ name, change, want string }{
		{"unknown key", minimal + "colour: blue\n", "colour"},
		{"wrong format version", strings.Replace(minimal, "paisans.blueprint/v1", "paisans.blueprint/v9", 1), "apiVersion"},
		{"first service not app", strings.Replace(minimal, "name: app", "name: web", 1), "first service must be app"},
		{"unknown engine", strings.Replace(minimal, "engine: none", "engine: oracle", 1), "database.engine"},
		{"postgres with no postgres service", strings.Replace(minimal, "engine: none", "engine: postgres", 1), "database.engine"},
		{"no port", strings.Replace(minimal, "port: 8000\n", "", 1), "port"},
		{"bad config format", strings.Replace(minimal, "format: env", "format: toml", 1), "config.format"},
		{"health without status", strings.Replace(minimal, "status: [200]", "status: []", 1), "health"},
		{"unknown hostname role", minimal + "hostname_roles: [admin]\n", "hostname_roles"},
		{"bad mail", minimal + "mail: sends\n", "mail"},
		{"oidc callback not a path", minimal + "oidc: {callback: \"https://evil.example\"}\n", "oidc.callback"},
		{"setting with no reader", minimal + "gate:\n  settings:\n    - {name: X, want: \"true\", when: app.gated, why: w}\n", "settings and live"},
		{"condition naming nothing", minimal + "gate:\n  settings:\n    - {name: X, want: \"true\", when: app.gatd, why: w}\n  live: {action: env-lock-list, service: app, lock_variable: L}\n", "when"},
		{"condition that is not a boolean", minimal + "gate:\n  settings:\n    - {name: X, want: \"true\", when: app.hostname, why: w}\n  live: {action: env-lock-list, service: app, lock_variable: L}\n", "not a boolean"},
		{"unknown action", minimal + "gate:\n  settings:\n    - {name: X, want: \"true\", when: app.gated, why: w}\n  live: {action: run-anything, service: app}\n", "gate.live.action"},
	}
	for _, c := range cases {
		_, err := Parse("example", []byte(c.change), always)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error naming %q", c.name, err, c.want)
		}
	}
	if _, err := Parse("other", []byte(minimal), always); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("a name that is not its directory: %v", err)
	}
	if _, err := Parse("example", []byte(minimal+"hostname_roles: [media]\n"), func(string) bool { return false }); err == nil || !strings.Contains(err.Error(), "caddy.snippet.media.tmpl") {
		t.Errorf("a role without its snippet: %v", err)
	}
}

// A blueprint's condition is CEL over what the app's declaration decides.
func TestAConditionReadsTheApp(t *testing.T) {
	when, err := compileCondition(`app.gated && app.settings["private"] == true`)
	if err != nil {
		t.Fatal(err)
	}
	if when(config.App{Gated: false, Settings: map[string]any{"private": true}}) {
		t.Error("held for an app that is not gated")
	}
	if !when(config.App{Gated: true, Settings: map[string]any{"private": true}}) {
		t.Error("did not hold for a gated private app")
	}
	// A setting the app does not declare cannot be read, and a condition that
	// cannot be evaluated holds, so a setting it decides is required.
	if !when(config.App{Gated: true}) {
		t.Error("a condition that could not be evaluated did not hold")
	}
}

// Every reserved blueprint is marked reserved, and no core one is.
func TestTheReservedBlueprintsAreTheFour(t *testing.T) {
	want := map[config.Kind]bool{config.KindPocketID: true, config.KindOAuth2Proxy: true, config.KindInvite: true, config.KindUptime: true}
	for _, kind := range known() {
		if defs[kind].Reserved != want[kind] {
			t.Errorf("%s reserved is %v", kind, defs[kind].Reserved)
		}
	}
}
