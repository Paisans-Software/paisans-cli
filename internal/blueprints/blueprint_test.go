package blueprints

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
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
		{"tcp with a path", strings.Replace(minimal, "health: {path: /status, status: [200]}", "health: {tcp: true, path: /x}", 1), "tcp is for an app"},
		{"backup for a file database", minimal + "backup: {dump: {service: app, run: [x]}, restore: {service: app, run: [y]}}\n", "backup: only"},
		{"backup without its commands", strings.Replace(minimal, "engine: none", "engine: mysql", 1) + "backup: {dump: {service: db}}\n", "dump and restore each need"},
		{"a check with a bad level", minimal + "checks:\n  - {id: x, level: maybe, rule: \"true\", message: m}\n", "level"},
		{"a check with a bad id", minimal + "checks:\n  - {id: Not An Id, level: warn, rule: \"true\", message: m}\n", "needs an id"},
		{"a check naming nothing", minimal + "checks:\n  - {id: x, level: warn, rule: app.colour, message: m}\n", "rule"},
		{"a check with two messages", minimal + "checks:\n  - {id: x, level: warn, rule: \"true\", message: m, message_expr: \"'m'\"}\n", "one of message and message_expr"},
		{"a message that is not text", minimal + "checks:\n  - {id: x, level: warn, rule: \"true\", message_expr: \"size(app.sites)\"}\n", "message_expr"},
		{"a second service on the app's port", strings.Replace(minimal, "  - {name: app, image: \"example/app:1\"}\n", "  - {name: app, image: \"example/app:1\"}\n  - {name: side, image: \"example/side:1\", port: 8000}\n", 1), "is app's port too"},
		{"an app port rootless Docker cannot publish", strings.Replace(minimal, "port: 8000", "port: 80", 1), "port: is 80, and a site's rootless Docker binds a port below 1024"},
		{"a service port rootless Docker cannot publish", strings.Replace(minimal, "  - {name: app, image: \"example/app:1\"}\n", "  - {name: app, image: \"example/app:1\"}\n  - {name: side, image: \"example/side:1\", port: 443}\n", 1), "services[1].port: is 443"},
		{"a port beyond the last", strings.Replace(minimal, "port: 8000", "port: 70000", 1), "port: is 70000, not a port"},
		{"a port on the app service", strings.Replace(minimal, "{name: app, image: \"example/app:1\"}", "{name: app, image: \"example/app:1\", port: 8001}", 1), "services[0].port"},
		{"an extra database without postgres", strings.Replace(minimal, "database: {engine: none}", "database: {engine: none, extra: [side]}", 1), "database.extra"},
		{"a local only reason without local only", minimal + "storage: {local_only_reason: because}\n", "local_only_reason"},
		{"a secret of no known format", minimal + "secrets:\n  generate:\n    - {name: k, format: base64}\n", "format"},
		{"a keypair with no passphrase", minimal + "secrets:\n  generate:\n    - {name: k, format: rsa-keypair, group: g}\n", "passphrase"},
		{"a keypair outside its passphrase's group", minimal + "secrets:\n  generate:\n    - {name: p}\n    - {name: k, format: rsa-keypair, passphrase: p, group: g}\n", "one group"},
		{"a kept secret with a cost", minimal + "secrets:\n  generate:\n    - {name: k, never_rotate: why, rotation_cost: restart}\n", "never_rotate"},
		{"a rotation in a service the blueprint lacks", minimal + "secrets:\n  generate:\n    - {name: k, rotate: {service: db, change: [x], check: [y]}}\n", "rotate.service"},
		{"a rotation condition without its reason", minimal + "secrets:\n  generate:\n    - {name: k, rotate: {service: app, change: [x], check: [y], when: \"true\"}}\n", "when and kept"},
		{"a check with two keys", minimal + "checks:\n  - {id: x, level: warn, rule: \"true\", message: m, key: a, key_expr: \"'b'\"}\n", "key_expr"},
		{"an enum with no values", minimal + "settings:\n  mode: {type: enum}\n", "values go with type enum"},
		{"an unknown setting type", minimal + "settings:\n  mode: {type: colour}\n", "settings.mode.type"},
		{"leaves open something else", minimal + "gate: {leaves_open: [ssh]}\n", "gate.leaves_open"},
		{"a requirement in another form", minimal + "requires_toolkit: \"^1.0\"\n", "requires_toolkit"},
		{"a requirement this toolkit does not meet", minimal + "requires_toolkit: \">= 999.0\"\n", "needs Paisans 999.0"},
	}
	for _, c := range cases {
		_, err := Parse("example", []byte(c.change), always)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error naming %q", c.name, err, c.want)
		}
	}
	for _, port := range []string{"1024", "65535"} {
		if _, err := Parse("example", []byte(strings.Replace(minimal, "port: 8000", "port: "+port, 1)), always); err != nil {
			t.Errorf("port %s, the edge of what rootless Docker publishes: %v", port, err)
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
func TestTheReservedBlueprintsAreTheSix(t *testing.T) {
	want := map[config.Blueprint]bool{config.BlueprintPocketID: true, config.BlueprintOAuth2Proxy: true, config.BlueprintAccount: true, config.BlueprintUptime: true, config.BlueprintEngagement: true, config.BlueprintBeszel: true}
	for _, blueprint := range known() {
		if defs[blueprint].Reserved != want[blueprint] {
			t.Errorf("%s reserved is %v", blueprint, defs[blueprint].Reserved)
		}
	}
}

// Health may be left out, or be a TCP check alone, and a database the
// backups cannot read as files may say how to dump it.
func TestOptionalHealthAndADumpParse(t *testing.T) {
	noHealth := strings.Replace(minimal, "health: {path: /status, status: [200]}\n", "", 1)
	d, err := Parse("example", []byte(noHealth), always)
	if err != nil || d.Health.Path != "" || d.Health.TCP {
		t.Errorf("no health: %+v, %v", d, err)
	}
	tcp := strings.Replace(minimal, "health: {path: /status, status: [200]}", "health: {tcp: true}", 1)
	if d, err := Parse("example", []byte(tcp), always); err != nil || !d.Health.TCP {
		t.Errorf("tcp: %+v, %v", d, err)
	}
	mysql := strings.Replace(minimal, "engine: none", "engine: mysql", 1) + "backup:\n  dump: {service: db, run: [mysqldump, x]}\n  restore: {service: db, run: [mysql, x]}\n"
	if d, err := Parse("example", []byte(mysql), always); err != nil || d.Backup == nil || d.Engine != "mysql" {
		t.Errorf("mysql: %+v, %v", d, err)
	}
}

// An Ed25519 pair is two secrets in one group, the public half naming the
// private one, and the private key has no passphrase.
func TestParseEd25519Pair(t *testing.T) {
	pair := "secrets:\n  generate:\n    - {name: hub_key, format: ed25519-openssh, group: hub}\n    - {name: hub_key_public, format: ed25519-public, of: hub_key, group: hub}\n"
	if _, err := Parse("example", []byte(minimal+pair), always); err != nil {
		t.Fatalf("a valid pair: %v", err)
	}
	for _, c := range []struct{ name, change, want string }{
		{"a public half naming nothing", minimal + "secrets:\n  generate:\n    - {name: hub_key_public, format: ed25519-public, group: hub}\n", "names the ed25519-openssh secret"},
		{"a public half naming an RSA keypair", minimal + "secrets:\n  generate:\n    - {name: p, group: hub}\n    - {name: k, format: rsa-keypair, passphrase: p, group: hub}\n    - {name: hub_key_public, format: ed25519-public, of: k, group: hub}\n", "names the ed25519-openssh secret"},
		{"a private key with a passphrase", minimal + "secrets:\n  generate:\n    - {name: p, group: hub}\n    - {name: hub_key, format: ed25519-openssh, passphrase: p, group: hub}\n", "passphrase"},
		{"a pair outside one group", minimal + "secrets:\n  generate:\n    - {name: hub_key, format: ed25519-openssh, group: hub}\n    - {name: hub_key_public, format: ed25519-public, of: hub_key}\n", "one group"},
	} {
		if _, err := Parse("example", []byte(c.change), always); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error naming %q", c.name, err, c.want)
		}
	}
}
