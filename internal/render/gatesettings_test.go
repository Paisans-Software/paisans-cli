package render_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// Every gate setting a kind records is rendered with the value it wants and
// named in the file's lock list, which is what lets doctor read it from the
// running app and call apply its only fix. A record that drifted from its
// template fails here rather than in a doctor run on a live host.
func TestGateSettingsAreRenderedAndLocked(t *testing.T) {
	gated := func(a *config.App) { a.Gated = true }

	env := renderTalk(t, fixtureSMTP, nil, gated)
	talk := config.App{Kind: config.KindMbin, Gated: true}
	locked := strings.Split(envValue(env, "MBIN_LOCKED_SETTINGS"), ",")
	for _, s := range kinds.GateSettingsFor(talk) {
		if got := envValue(env, s.Name); got != s.Want {
			t.Errorf("mbin: %s = %q, want %q", s.Name, got, s.Want)
		}
		if !slices.Contains(locked, s.Name) {
			t.Errorf("mbin: %s is not in MBIN_LOCKED_SETTINGS %v", s.Name, locked)
		}
	}

	ini := renderBlog(t, gated)
	blog := config.App{Kind: config.KindWriteFreely, Gated: true}
	value, _ := iniValue(ini, "", "locked_settings")
	var lockedINI []string
	for _, n := range strings.Split(value, ",") {
		lockedINI = append(lockedINI, strings.TrimSpace(n))
	}
	for _, s := range kinds.GateSettingsFor(blog) {
		section, key, _ := strings.Cut(s.Name, ".")
		if got, _ := iniValue(ini, section, key); got != s.Want {
			t.Errorf("writefreely: %s = %q, want %q", s.Name, got, s.Want)
		}
		if !slices.Contains(lockedINI, s.Name) {
			t.Errorf("writefreely: %s is not in locked_settings %v", s.Name, lockedINI)
		}
	}
}
