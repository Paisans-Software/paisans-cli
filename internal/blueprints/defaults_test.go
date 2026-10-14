package blueprints

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints/coretest"
	"github.com/paisans-software/paisans-stack/internal/config"
)

const withDefaults = minimal + `settings:
  title: {type: string, default: Notes}
  open: {type: bool, default: false}
  limit: {type: int, default: 5}
  mode: {type: enum, values: [quiet, loud], default: quiet}
  contact: {type: email}
`

// A setting may declare a default of its own type, and one that is not of
// its type, or not one of an enum's values, is refused when the blueprint
// is read.
func TestASettingDefaultIsOfItsType(t *testing.T) {
	d, err := Parse("example", []byte(withDefaults), always)
	if err != nil {
		t.Fatal(err)
	}
	if d.Settings["title"].Default != "Notes" || d.Settings["open"].Default != false || d.Settings["limit"].Default != 5 || d.Settings["contact"].Default != nil {
		t.Errorf("%+v", d.Settings)
	}
	for _, bad := range []string{
		"settings:\n  open: {type: bool, default: \"no\"}\n",
		"settings:\n  limit: {type: int, default: many}\n",
		"settings:\n  contact: {type: email, default: \"Ann <ann@example.org>\"}\n",
		"settings:\n  mode: {type: enum, values: [quiet, loud], default: shouting}\n",
	} {
		if _, err := Parse("example", []byte(minimal+bad), always); err == nil || !strings.Contains(err.Error(), ".default") {
			t.Errorf("%s: got %v, want a refusal naming the default", bad, err)
		}
	}
}

// A catalog blueprint's CEL sees its defaults for the settings an app leaves
// out and the app's own value for one it sets, and the app's configuration
// is not written to.
func TestACatalogBlueprintsExpressionsSeeItsDefaults(t *testing.T) {
	coretest.Cache(t)
	spec := strings.Replace(withDefaults, "name: example", "name: journal", 1) +
		"checks:\n  - {id: quiet-by-default, level: refuse, rule: 'app.settings.mode == \"quiet\" && app.settings.limit == 5', message: m}\n"
	url, commit := testCatalog(t, map[string]string{"blueprints/journal/blueprint.yaml": spec})
	cfg := catalogConfig(url, commit, "", "example/journal")
	if p := resolve(cfg); len(p) > 0 {
		t.Fatal(p)
	}
	check := ChecksFor("example/journal")[0]
	if !check.Holds(cfg, "notes") {
		t.Error("the check did not see the defaults")
	}
	if cfg.Apps["notes"].Settings != nil {
		t.Errorf("the defaults were written into the configuration: %v", cfg.Apps["notes"].Settings)
	}
	app := cfg.Apps["notes"]
	app.Settings = map[string]any{"mode": "loud"}
	cfg.Apps["notes"] = app
	if check.Holds(cfg, "notes") {
		t.Error("the default won over the app's own setting")
	}
	if seen := SettingsSeen(app); seen["mode"] != "loud" || seen["title"] != "Notes" || len(app.Settings) != 1 {
		t.Errorf("seen %v, declared %v", seen, app.Settings)
	}
}

// A built in blueprint's settings are what the app declares and nothing
// more, so the meaning Go and validate give an absent setting is kept.
func TestABuiltInBlueprintGetsNoDefaults(t *testing.T) {
	app := config.App{Blueprint: config.Blueprint("mbin")}
	if seen := SettingsSeen(app); seen != nil {
		t.Errorf("an mbin app with no settings sees %v", seen)
	}
}

// Lint refuses a template whose literal fallback for a setting is not the
// default the blueprint declares for it, and accepts one that agrees.
func TestLintCatchesADisagreeingFallback(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "example")
	write := func(name, content string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("blueprint.yaml", withDefaults)
	write("caddy.snippet.tmpl", "\n")
	write("compose.yaml.tmpl", "services: {}\n")
	write(".env.tmpl", `TITLE={{ .Setting "title" "Notes" }}
OPEN={{ .SettingBool "open" false }}
MODE={{ .Setting "mode" "quiet" }}
CONTACT={{ .Setting "contact" "nobody@example.org" }}
`)
	if _, problems, err := Lint(dir); err != nil || len(problems) > 0 {
		t.Fatalf("agreeing fallbacks: %v, %v", problems, err)
	}
	write(".env.tmpl", `TITLE={{ .Setting "title" "Journal" }}
OPEN={{ if .SettingBool "open" true }}yes{{ end }}
`)
	_, problems, err := Lint(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(problems, "\n")
	if len(problems) != 2 || !strings.Contains(got, `.Setting "title" falls back to "Journal"`) || !strings.Contains(got, `.SettingBool "open" falls back to true`) {
		t.Errorf("problems:\n%s", got)
	}
}
