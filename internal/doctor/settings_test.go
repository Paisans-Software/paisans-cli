package doctor

import (
	"strings"
	"testing"
)

const talkLocked = "MBIN_AUTHORIZED_FETCH\ttrue\nMBIN_PRIVATE_INSTANCE\ttrue\nMBIN_USE_FEDERATION_ALLOW_LIST\ttrue\nMBIN_LOCKED_SETTINGS\tKBIN_DOMAIN,MBIN_AUTHORIZED_FETCH,MBIN_PRIVATE_INSTANCE,MBIN_USE_FEDERATION_ALLOW_LIST\n"

// A copy enforcing and locking every gate setting passes; one with a setting
// off, or not locked, fails and names apply as the fix; one not running, or
// that could not be read, is skipped rather than passed.
func TestSettingsCheckEachRunningCopy(t *testing.T) {
	cfg := fixture(t)
	findings := Settings(cfg, []SettingsProbe{
		{Site: "home-a", App: "talk", Out: talkLocked},
		{Site: "home-b", App: "talk", Out: strings.Replace(strings.Replace(talkLocked, "MBIN_PRIVATE_INSTANCE\ttrue", "MBIN_PRIVATE_INSTANCE\tfalse", 1), ",MBIN_USE_FEDERATION_ALLOW_LIST\n", "\n", 1)},
		{Site: "vm", App: "talk", NotRunning: true},
		{Site: "watch", App: "talk", Err: "permission denied"},
		// Public with auto-login: nothing is left to the app, so nothing to check.
		{Site: "home-a", App: "blog", Out: ""},
	})
	if len(findings) != 4 {
		t.Fatalf("%d findings, want 4 (blog uses the gate only to sign in): %+v", len(findings), findings)
	}
	if f := findings[0]; f.Level != OK || !strings.Contains(f.Line, "talk on home-a") {
		t.Errorf("a locked, enforcing copy: %+v", f)
	}
	f := findings[1]
	text := strings.Join(f.More, "\n")
	if f.Level != Fail || !strings.Contains(f.Line, "2 gate setting(s)") {
		t.Errorf("a copy with one setting off and one unlocked: %+v", f)
	}
	for _, want := range []string{"MBIN_PRIVATE_INSTANCE is false, and must be true", "MBIN_USE_FEDERATION_ALLOW_LIST is true but not locked", "`paisans apply --site home-b`"} {
		if !strings.Contains(text, want) {
			t.Errorf("the failure lacks %q:\n%s", want, text)
		}
	}
	if findings[2].Level != Skip || findings[3].Level != Skip {
		t.Errorf("not running and unreadable should skip: %+v %+v", findings[2], findings[3])
	}
}

// The read finds the app's container by its compose labels and this
// deployment's, and says so when there is none.
func TestSettingsCommandFindsTheContainerByLabel(t *testing.T) {
	cfg := fixture(t)
	cmd := SettingsCommand(cfg.Deployment(), "talk", "app", []string{"sh", "-c", "printenv X"})
	for _, want := range []string{"label=com.docker.compose.project=" + cfg.Deployment().Project("talk"), "label=com.docker.compose.service=app", cfg.Deployment().LabelFilter(), NotRunningMarker, `docker exec "$c" 'sh' '-c' 'printenv X'`} {
		if !strings.Contains(cmd, want) {
			t.Errorf("the command lacks %q:\n%s", want, cmd)
		}
	}
}
