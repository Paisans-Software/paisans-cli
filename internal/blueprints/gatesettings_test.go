package blueprints_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// A gateable kind that federates leaves ActivityPub to the app, so it has
// gate settings and a way to read them; Wiki.js and Element leave nothing to
// a setting.
func TestEveryFederatingGateableKindRecordsItsGateSettings(t *testing.T) {
	for _, kind := range blueprints.Builtin() {
		spec := blueprints.GateFor(kind)
		app := config.App{Kind: kind, Gated: true}
		_, live := blueprints.LiveSettingsFor(kind)
		has := len(blueprints.GateSettingsFor(app)) > 0
		if spec.Gateable && spec.SignedFetch != nil && (!has || !live) {
			t.Errorf("%s federates behind the gate but records no gate settings to check", kind)
		}
		if has != live {
			t.Errorf("%s: gate settings %v, a way to read them %v", kind, has, live)
		}
	}
}

// An app that does not use the gate needs none of them.
func TestAPublicAppNeedsNoGateSettings(t *testing.T) {
	if got := blueprints.GateSettingsFor(config.App{Kind: config.Kind("mbin")}); len(got) != 0 {
		t.Errorf("public mbin needs %v", got)
	}
}

func TestMbinReadsOnlyTheNamedVariables(t *testing.T) {
	live, _ := blueprints.LiveSettingsFor(config.Kind("mbin"))
	argv := live.ReadCommand([]string{"MBIN_PRIVATE_INSTANCE"})
	script := argv[len(argv)-1]
	if !strings.Contains(script, "for n in MBIN_PRIVATE_INSTANCE MBIN_LOCKED_SETTINGS;") {
		t.Errorf("mbin read is %q", script)
	}
	got, err := live.Parse("MBIN_PRIVATE_INSTANCE\ttrue\nMBIN_USE_FEDERATION_ALLOW_LIST\tfalse\nMBIN_LOCKED_SETTINGS\tKBIN_DOMAIN,MBIN_PRIVATE_INSTANCE\n",
		[]string{"MBIN_PRIVATE_INSTANCE", "MBIN_USE_FEDERATION_ALLOW_LIST", "MBIN_AUTHORIZED_FETCH"})
	if err != nil {
		t.Fatal(err)
	}
	if got["MBIN_PRIVATE_INSTANCE"] != (blueprints.Live{Value: "true", Locked: true}) {
		t.Errorf("private instance read as %+v", got["MBIN_PRIVATE_INSTANCE"])
	}
	if got["MBIN_USE_FEDERATION_ALLOW_LIST"] != (blueprints.Live{Value: "false"}) {
		t.Errorf("allow list read as %+v", got["MBIN_USE_FEDERATION_ALLOW_LIST"])
	}
	if _, ok := got["MBIN_AUTHORIZED_FETCH"]; ok {
		t.Error("a variable the read did not print is present")
	}
}

// The settings command's tabwriter output, as cmd/writefreely/settings.go
// prints it at v0.21.1+wisp.
func TestWriteFreelyReadsItsSettingsList(t *testing.T) {
	live, _ := blueprints.LiveSettingsFor(config.Kind("writefreely"))
	out := "app.authorized_fetch  true   (locked in config.ini)\napp.private           false  (default)\napp.site_name         My Blog  (locked in config.ini)\n"
	got, err := live.Parse(out, []string{"app.private", "app.authorized_fetch"})
	if err != nil {
		t.Fatal(err)
	}
	if got["app.authorized_fetch"] != (blueprints.Live{Value: "true", Locked: true}) || got["app.private"] != (blueprints.Live{Value: "false"}) {
		t.Errorf("read %+v", got)
	}
	if _, ok := got["app.site_name"]; ok {
		t.Error("a setting not asked for is present")
	}
}

// A config: key equal to the value gating requires only repeats what the
// template writes; any other value is a contradiction and is not redundant.
func TestRedundantGateConfig(t *testing.T) {
	cfg := &config.Config{Apps: map[string]config.App{
		"talk": {Kind: config.Kind("mbin"), Gated: true, Config: map[string]any{"MBIN_USE_FEDERATION_ALLOW_LIST": true, "MBIN_PRIVATE_INSTANCE": false, "KBIN_META_TITLE": "x"}},
		"open": {Kind: config.Kind("mbin"), Config: map[string]any{"MBIN_USE_FEDERATION_ALLOW_LIST": true}},
		"blog": {Kind: config.Kind("writefreely"), Gated: true, Config: map[string]any{"app.private": "true"}},
	}}
	got := blueprints.RedundantGateConfig(cfg)
	want := []blueprints.RedundantKey{{App: "blog", Key: "app.private"}, {App: "talk", Key: "MBIN_USE_FEDERATION_ALLOW_LIST"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}
}
