package render_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
)

const blogINIA = "home-a/srv/paisans/f2a9/blog/config.ini"

// writefreelyDatabaseSettings is the fork's setting registry, every setting
// it keeps in its database and the only names locked_settings accepts
// (config/settings.go dbSettings, read at writefreely-wisp develop f95d840,
// v0.21.1+wisp, the first release carrying locked settings).
var writefreelyDatabaseSettings = []string{
	"app.site_name", "app.site_description", "app.theme", "app.editor", "app.disable_js", "app.webfonts",
	"app.landing", "app.simple_nav", "app.wf_modesty", "app.chorus", "app.forest", "app.disable_drafts",
	"app.open_registration", "app.open_deletion", "app.min_username_len", "app.max_blogs", "app.federation",
	"app.public_stats", "app.monetization", "app.notes_only", "app.private", "app.federation_allowlist",
	"app.authorized_fetch", "app.instance_announce", "app.local_timeline", "app.user_invites",
	"app.default_visibility", "app.update_checks", "uploads.enabled", "uploads.max_size_mb",
}

// writefreelyBootstrap is what the fork reads from config.ini alone
// (config/settings.go bootstrapSections and bootstrapKeys, same commit):
// a whole section, or one key in a section that otherwise holds settings.
var writefreelyBootstrap = map[string]bool{
	"server": true, "database": true, "email": true, "storage": true,
	"oauth.slack": true, "oauth.writeas": true, "oauth.gitlab": true, "oauth.gitea": true, "oauth.generic": true,
	"app.host": true, "app.single_user": true, "app.disable_password_auth": true, "uploads.dir": true,
	".settings_location": true, ".locked_settings": true,
}

// iniKeys is every key a config.ini sets, as section.key, with the lines
// before any header in section "".
func iniKeys(t *testing.T, ini string) []string {
	t.Helper()
	var out []string
	section := ""
	for _, line := range strings.Split(ini, "\n") {
		l := strings.TrimSpace(line)
		switch {
		case l == "" || strings.HasPrefix(l, ";") || strings.HasPrefix(l, "#"):
		case strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]"):
			section = l[1 : len(l)-1]
		default:
			key, _, ok := strings.Cut(l, "=")
			if !ok {
				t.Fatalf("config.ini line %q is not key = value", line)
			}
			out = append(out, section+"."+strings.TrimSpace(key))
		}
	}
	return out
}

func iniValue(ini, section, key string) (string, bool) {
	cur := ""
	for _, line := range strings.Split(ini, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") {
			cur = l[1 : len(l)-1]
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok && cur == section && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

func renderBlog(t *testing.T, edit func(*config.App)) string {
	t.Helper()
	cfg := fixture(t)
	app := cfg.Apps["blog"]
	if edit != nil {
		edit(&app)
	}
	cfg.Apps["blog"] = app
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	ini, ok := planFiles(plan)[blogINIA]
	if !ok {
		t.Fatalf("no %s in the plan", blogINIA)
	}
	return ini
}

// locked_settings is exactly the database settings the template renders:
// every name in it is in the fork's registry and set in the file, and every
// registry setting the file sets is in it. Every other key the file sets is
// one the fork reads from config.ini alone, so nothing rendered is imported
// into the database and then stripped. A passthrough key is the operator's
// and is not locked.
func TestWriteFreelyLockedSettingsAreRendered(t *testing.T) {
	registry := map[string]bool{}
	for _, name := range writefreelyDatabaseSettings {
		registry[name] = true
	}
	for _, c := range []struct {
		name string
		edit func(*config.App)
	}{
		{"template only", func(a *config.App) { a.Config = nil }},
		{"gated, authorized_fetch off", func(a *config.App) {
			a.Config = nil
			a.Gated = true
			a.Settings = map[string]any{"authorized_fetch": false, "private": false, "federation": false}
		}},
	} {
		ini := renderBlog(t, c.edit)
		var want []string
		for _, key := range iniKeys(t, ini) {
			section := key[:strings.LastIndex(key, ".")]
			switch {
			case registry[key]:
				want = append(want, key)
			case writefreelyBootstrap[key] || writefreelyBootstrap[section]:
			default:
				t.Errorf("%s: config.ini sets %s, which the fork classifies as neither a database setting nor a config.ini one", c.name, key)
			}
		}
		raw, ok := iniValue(ini, "", "locked_settings")
		if !ok {
			t.Fatalf("%s: config.ini has no locked_settings before its first section:\n%s", c.name, ini)
		}
		var locked []string
		for _, name := range strings.Split(raw, ",") {
			name = strings.TrimSpace(name)
			if !registry[name] {
				t.Errorf("%s: %s is locked but is not in the fork's registry, so WriteFreely would refuse to start", c.name, name)
			}
			locked = append(locked, name)
		}
		sort.Strings(locked)
		sort.Strings(want)
		if strings.Join(locked, ",") != strings.Join(want, ",") {
			t.Errorf("%s: locked %v, want every rendered database setting %v", c.name, locked, want)
		}
	}

	// The fixture's own passthrough key is a registry setting, and stays
	// unlocked: it is the operator's.
	ini := renderBlog(t, nil)
	if _, ok := iniValue(ini, "app", "max_blogs"); !ok {
		t.Fatal("the fixture's blog no longer sets config.app.max_blogs, so there is nothing to prove")
	}
	raw, _ := iniValue(ini, "", "locked_settings")
	if strings.Contains(raw, "app.max_blogs") {
		t.Errorf("the passthrough key app.max_blogs is locked: %s", raw)
	}
}

// authorized_fetch is rendered always, on unless the setting turns it off.
func TestWriteFreelyRendersAuthorizedFetch(t *testing.T) {
	for setting, want := range map[any]string{nil: "true", true: "true", false: "false"} {
		ini := renderBlog(t, func(a *config.App) {
			if setting != nil {
				a.Settings = map[string]any{"authorized_fetch": setting}
			}
		})
		if got, _ := iniValue(ini, "app", "authorized_fetch"); got != want {
			t.Errorf("settings.authorized_fetch %v: [app] authorized_fetch = %q, want %s", setting, got, want)
		}
	}
}
