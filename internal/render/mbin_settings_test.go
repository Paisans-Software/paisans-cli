package render_test

import (
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// The fixture's community is "Fixture Community", and its talk sets no
// display_name, so the app's name stands in for one.
func TestMbinTitlesComeFromTheDisplayAndCommunityNames(t *testing.T) {
	for _, c := range []struct{ display, community, title, meta string }{
		{"Talk", "Paisans", "Talk", "Talk · Paisans"},
		{"Talk", "", "Talk", "Talk"},
		{"", "Fixture Community", "talk", "talk · Fixture Community"},
	} {
		cfg := fixture(t)
		cfg.Community.Name = c.community
		app := cfg.Apps["talk"]
		app.DisplayName = c.display
		cfg.Apps["talk"] = app
		env := talkEnv(t, cfg)
		if got := envValue(env, "KBIN_TITLE"); got != c.title {
			t.Errorf("display %q, community %q: KBIN_TITLE=%s, want %s", c.display, c.community, got, c.title)
		}
		if got := envValue(env, "KBIN_META_TITLE"); got != c.meta {
			t.Errorf("display %q, community %q: KBIN_META_TITLE=%s, want %s", c.display, c.community, got, c.meta)
		}
	}
}

// Each setting the configuration gives wins over its default, and text
// survives the characters an env file treats specially.
func TestMbinSettingsOverrideTheirDefaults(t *testing.T) {
	env := renderTalk(t, fixtureSMTP, nil, func(a *config.App) {
		a.Settings = map[string]any{
			"title": "Forum", "meta_title": "The Forum", "meta_description": `He said "hi" #1 $5`,
			"meta_keywords": "talk, forum", "downvotes_mode": "hidden", "registrations": true,
			"sso_registrations": false, "mercure": true, "avatar_override": false,
		}
	})
	for name, want := range map[string]string{
		"KBIN_TITLE":                     "Forum",
		"KBIN_META_TITLE":                "The Forum",
		"KBIN_META_DESCRIPTION":          `"He said \"hi\" #1 \$5"`,
		"KBIN_META_KEYWORDS":             "talk, forum",
		"MBIN_DOWNVOTES_MODE":            "hidden",
		"KBIN_REGISTRATIONS_ENABLED":     "true",
		"MBIN_SSO_REGISTRATIONS_ENABLED": "false",
		"KBIN_MERCURE_ENABLED":           "true",
		"MBIN_SSO_AVATAR_OVERRIDE":       "false",
	} {
		if got := envValue(env, name); got != want {
			t.Errorf("%s=%s, want %s", name, got, want)
		}
	}
}

// An app that sets nothing gets the spec's defaults: Mbin's own, except
// that its signup form is off, since members join through the account
// service and the identity provider.
func TestMbinDefaults(t *testing.T) {
	env := renderTalk(t, fixtureSMTP, nil, func(a *config.App) { a.Settings = nil; a.Gated = false })
	for name, want := range map[string]string{
		"KBIN_META_DESCRIPTION":                   "",
		"KBIN_META_KEYWORDS":                      "",
		"MBIN_DOWNVOTES_MODE":                     "enabled",
		"KBIN_HEADER_LOGO":                        "false",
		"KBIN_REGISTRATIONS_ENABLED":              "false",
		"MBIN_SSO_REGISTRATIONS_ENABLED":          "true",
		"MBIN_SSO_SHOW_FIRST":                     "false",
		"MBIN_SSO_AVATAR_OVERRIDE":                "true",
		"KBIN_CAPTCHA_ENABLED":                    "false",
		"KBIN_MERCURE_ENABLED":                    "false",
		"KBIN_ADMIN_ONLY_OAUTH_CLIENTS":           "false",
		"KBIN_FEDERATED_SEARCH_ONLY_LOGGEDIN":     "true",
		"MBIN_RESTRICT_MAGAZINE_CREATION":         "false",
		"MBIN_SIDEBAR_SECTIONS_RANDOM_LOCAL_ONLY": "false",
		"MBIN_SIDEBAR_SECTIONS_USERS_LOCAL_ONLY":  "false",
		"MBIN_NEW_USERS_NEED_APPROVAL":            "false",
		"MBIN_PRIVATE_INSTANCE":                   "false",
		"KBIN_FEDERATION_ENABLED":                 "true",
		"KBIN_FEDERATION_PAGE_ENABLED":            "true",
		"MBIN_USE_FEDERATION_ALLOW_LIST":          "false",
	} {
		if got := envValue(env, name); got != want {
			t.Errorf("%s=%q, want %q", name, got, want)
		}
	}
}

// A gated Mbin is a private instance on an allow list whatever its settings
// say; an ungated one is either only when its settings say so.
func TestMbinGatedForcesPrivateAndAllowList(t *testing.T) {
	for _, c := range []struct {
		gated bool
		set   map[string]any
		want  string
	}{
		{true, nil, "true"},
		{false, nil, "false"},
		{false, map[string]any{"private_instance": true, "federation_allow_list": true}, "true"},
	} {
		env := renderTalk(t, fixtureSMTP, nil, func(a *config.App) { a.Gated = c.gated; a.AutoLogin = false; a.Settings = c.set })
		for _, name := range []string{"MBIN_PRIVATE_INSTANCE", "MBIN_USE_FEDERATION_ALLOW_LIST"} {
			if got := envValue(env, name); got != c.want {
				t.Errorf("gated %v, settings %v: %s=%s, want %s", c.gated, c.set, name, got, c.want)
			}
		}
	}
}

// With federation false, Mbin's federation is off and the toolkit treats
// the app as one that does not federate: the monitor seeds no signed fetch
// check for it, gated though it is.
func TestMbinFederationOff(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	app := cfg.Apps["talk"]
	app.Settings = map[string]any{"federation": false}
	cfg.Apps["talk"] = app
	if got := envValue(talkEnvWith(t, cfg, secrets), "KBIN_FEDERATION_ENABLED"); got != "false" {
		t.Errorf("KBIN_FEDERATION_ENABLED=%s, want false", got)
	}
	if byName(renderSeed(t, cfg, secrets))["talk — signed fetch"] != nil {
		t.Error("the monitor checks signed fetch on an app that does not federate")
	}
}

// talkEnv is talk's .env on home-a, rendered from cfg with the fixture's
// secrets, and talkEnvWith the same with secrets of the caller's.
func talkEnv(t *testing.T, cfg *config.Config) string {
	t.Helper()
	return talkEnvWith(t, cfg, fixtureSecrets(t))
}

func talkEnvWith(t *testing.T, cfg *config.Config, secrets *config.Secrets) string {
	t.Helper()
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	env, ok := planFiles(plan)[talkEnvA]
	if !ok {
		t.Fatalf("no %s in the plan", talkEnvA)
	}
	return env
}
