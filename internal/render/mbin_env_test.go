package render_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
)

const talkEnvA = "home-a/srv/paisans/f2a9/talk/.env"

// renderTalk renders the fixture with the Mbin app changed by edit and the
// deployment's smtp block replaced, and returns its .env on home-a.
func renderTalk(t *testing.T, smtp config.SMTP, secrets *config.Secrets, edit func(*config.App)) string {
	t.Helper()
	cfg := fixture(t)
	cfg.SMTP = smtp
	app := cfg.Apps["talk"]
	if edit != nil {
		edit(&app)
	}
	cfg.Apps["talk"] = app
	if secrets == nil {
		secrets = fixtureSecrets(t)
	}
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

var fixtureSMTP = config.SMTP{
	Host: "smtp.example.org", Security: config.SMTPStartTLS,
	Username: "robot@example.org", FromAddress: "hello@example.org",
}

// With an SMTP host, the .env carries a DSN built from it with the
// deployment's password, the sender, and the contact address from settings.
func TestMbinRendersTheDeploymentsSMTP(t *testing.T) {
	env := renderTalk(t, fixtureSMTP, nil, func(a *config.App) {
		a.Settings = map[string]any{"contact_email": "admins@example.org"}
	})
	for name, want := range map[string]string{
		"MAILER_DSN":         "smtp://robot%40example.org:fixture-not-a-secret-smtp@smtp.example.org:587?require_tls=true",
		"KBIN_SENDER_EMAIL":  "hello@example.org",
		"KBIN_CONTACT_EMAIL": "admins@example.org",
	} {
		if got := envValue(env, name); got != want {
			t.Errorf("%s=%s, want %s", name, got, want)
		}
	}
}

// Without contact_email, the contact form sends to the sender.
func TestMbinContactEmailFallsBackToTheSender(t *testing.T) {
	env := renderTalk(t, fixtureSMTP, nil, func(a *config.App) { a.Settings = nil })
	if got := envValue(env, "KBIN_CONTACT_EMAIL"); got != "hello@example.org" {
		t.Errorf("KBIN_CONTACT_EMAIL=%s, want the sender", got)
	}
}

// The app's own smtp block wins field by field, and its own password wins
// over external.smtp_password. tls makes the DSN smtps on port 465.
func TestMbinAppSMTPOverrideWins(t *testing.T) {
	secrets := fixtureSecrets(t)
	if secrets.Apps["talk"] == nil {
		secrets.Apps["talk"] = map[string]any{}
	}
	secrets.Apps["talk"]["smtp_password"] = "talk-own-$#"
	env := renderTalk(t, fixtureSMTP, secrets, func(a *config.App) {
		a.SMTP = &config.SMTP{Security: config.SMTPTLS, Username: "talk@example.org", FromAddress: "talk@example.org"}
	})
	for name, want := range map[string]string{
		"MAILER_DSN":         "smtps://talk%40example.org:talk-own-%24%23@smtp.example.org:465",
		"KBIN_SENDER_EMAIL":  "talk@example.org",
		"KBIN_CONTACT_EMAIL": "admins@example.org",
	} {
		if got := envValue(env, name); got != want {
			t.Errorf("%s=%s, want %s", name, got, want)
		}
	}
}

// With no SMTP host anywhere, mail goes to the null transport and both
// addresses still render, at the app's hostname, so Mbin builds every mail it
// would send without an error and discards it.
func TestMbinWithoutSMTPRendersTheNullTransport(t *testing.T) {
	env := renderTalk(t, config.SMTP{}, nil, func(a *config.App) { a.Settings = nil })
	for name, want := range map[string]string{
		"MAILER_DSN":         "null://null",
		"KBIN_SENDER_EMAIL":  "noreply@talk.example.org",
		"KBIN_CONTACT_EMAIL": "noreply@talk.example.org",
	} {
		if got := envValue(env, name); got != want {
			t.Errorf("%s=%s, want %s", name, got, want)
		}
	}
}

// mbinDatabaseSettings are the fields of the paisans fork's SettingsDto
// (src/DTO/SettingsDto.php, read at Paisans-Software/mbin v1.15.0+paisans,
// the first release carrying locked settings, and unchanged on develop
// 64794776): every setting Mbin keeps in its database once an admin saves,
// after which the environment value of an unlocked one is ignored.
var mbinDatabaseSettings = []string{
	"KBIN_DOMAIN", "KBIN_TITLE", "KBIN_META_TITLE", "KBIN_META_KEYWORDS", "KBIN_META_DESCRIPTION",
	"KBIN_DEFAULT_LANG", "KBIN_CONTACT_EMAIL", "KBIN_SENDER_EMAIL", "MBIN_DEFAULT_THEME", "KBIN_JS_ENABLED",
	"KBIN_FEDERATION_ENABLED", "KBIN_REGISTRATIONS_ENABLED", "KBIN_HEADER_LOGO", "KBIN_CAPTCHA_ENABLED",
	"KBIN_MERCURE_ENABLED", "KBIN_FEDERATION_PAGE_ENABLED", "KBIN_ADMIN_ONLY_OAUTH_CLIENTS",
	"MBIN_SSO_ONLY_MODE", "MBIN_PRIVATE_INSTANCE", "KBIN_FEDERATED_SEARCH_ONLY_LOGGEDIN",
	"MBIN_SIDEBAR_SECTIONS_RANDOM_LOCAL_ONLY", "MBIN_SIDEBAR_SECTIONS_USERS_LOCAL_ONLY",
	"MBIN_SSO_REGISTRATIONS_ENABLED", "MBIN_RESTRICT_MAGAZINE_CREATION", "MBIN_SSO_SHOW_FIRST",
	"MBIN_DOWNVOTES_MODE", "MBIN_NEW_USERS_NEED_APPROVAL", "MBIN_USE_FEDERATION_ALLOW_LIST",
	"MBIN_AUTHORIZED_FETCH",
}

// mbinLockableSettings are the settings the fork can lock, the ones that fall
// back to an environment variable (docs/02-admin/03-optional-features/
// 11-locked_settings.md at v1.15.0+paisans). Naming any other setting in
// MBIN_LOCKED_SETTINGS makes Mbin answer every request with an error.
var mbinLockableSettings = []string{
	"KBIN_DOMAIN", "KBIN_TITLE", "KBIN_META_TITLE", "KBIN_META_KEYWORDS", "KBIN_META_DESCRIPTION",
	"KBIN_DEFAULT_LANG", "KBIN_CONTACT_EMAIL", "KBIN_SENDER_EMAIL", "MBIN_DEFAULT_THEME", "KBIN_JS_ENABLED",
	"KBIN_FEDERATION_ENABLED", "KBIN_REGISTRATIONS_ENABLED", "KBIN_HEADER_LOGO", "KBIN_CAPTCHA_ENABLED",
	"KBIN_FEDERATION_PAGE_ENABLED", "KBIN_ADMIN_ONLY_OAUTH_CLIENTS", "MBIN_SSO_ONLY_MODE",
	"MBIN_PRIVATE_INSTANCE", "MBIN_DOWNVOTES_MODE", "MBIN_NEW_USERS_NEED_APPROVAL",
	"MBIN_USE_FEDERATION_ALLOW_LIST", "MBIN_AUTHORIZED_FETCH",
}

// mbinSettingEnv maps a Mbin database setting to the environment variable
// that feeds it, where the two names differ (config/services.yaml:135 at
// Paisans-Software/mbin develop: sso_only_mode reads SSO_ONLY_MODE).
var mbinSettingEnv = map[string]string{
	"MBIN_SSO_ONLY_MODE": "SSO_ONLY_MODE",
}

// MBIN_LOCKED_SETTINGS is exactly the database settings the template renders,
// gated and not, with authorized fetch on and off: every name in it is a key
// the .env sets and one the fork can lock, and every key the .env sets that
// Mbin keeps in its database is in it. Passthrough keys are left out, since
// only the template is the toolkit's.
func TestMbinLockedSettingsAreRendered(t *testing.T) {
	// Keyed by the environment variable that feeds each setting, which is the
	// setting's own name unless mbinSettingEnv says otherwise.
	database := map[string]string{}
	for _, name := range mbinDatabaseSettings {
		key := name
		if env, ok := mbinSettingEnv[name]; ok {
			key = env
		}
		database[key] = name
	}
	lockable := map[string]bool{}
	for _, name := range mbinLockableSettings {
		lockable[name] = true
	}
	for _, c := range []struct {
		gate  bool
		fetch any
	}{{true, nil}, {false, nil}, {false, false}, {false, true}} {
		gate := c.gate
		env := renderTalk(t, fixtureSMTP, nil, func(a *config.App) {
			a.Gated = gate
			a.Config = nil
			if c.fetch != nil {
				settings := map[string]any{}
				for k, v := range a.Settings {
					settings[k] = v
				}
				settings["authorized_fetch"] = c.fetch
				a.Settings = settings
			}
		})
		wantFetch := "true"
		if c.fetch == false {
			wantFetch = "false"
		}
		if got := envValue(env, "MBIN_AUTHORIZED_FETCH"); got != wantFetch {
			t.Errorf("gated %v, authorized_fetch %v: MBIN_AUTHORIZED_FETCH = %q, want %s", gate, c.fetch, got, wantFetch)
		}
		rendered := map[string]bool{}
		var want []string
		for _, line := range strings.Split(env, "\n") {
			key, _, ok := strings.Cut(line, "=")
			if !ok || strings.HasPrefix(key, "#") {
				continue
			}
			if setting, ok := database[key]; ok {
				rendered[setting] = true
				want = append(want, setting)
			}
			rendered[key] = true
		}
		locked := strings.Split(envValue(env, "MBIN_LOCKED_SETTINGS"), ",")
		for _, name := range locked {
			if !rendered[name] {
				t.Errorf("gated %v: %s is locked but the .env does not set it", gate, name)
			}
			if !lockable[name] {
				t.Errorf("gated %v: %s is locked but the fork cannot lock it, so Mbin would refuse every request", gate, name)
			}
		}
		sort.Strings(locked)
		sort.Strings(want)
		if strings.Join(locked, ",") != strings.Join(want, ",") {
			t.Errorf("gated %v: locked %v, want every rendered database setting %v", gate, locked, want)
		}
		if gated := rendered["MBIN_PRIVATE_INSTANCE"]; gated != gate {
			t.Errorf("gated %v: MBIN_PRIVATE_INSTANCE rendered = %v", gate, gated)
		}
		// The fixture's talk has a Pocket ID client, so sign in is the
		// provider's alone and password registration is closed.
		if got := envValue(env, "SSO_ONLY_MODE"); got != "true" {
			t.Errorf("gated %v: SSO_ONLY_MODE = %q, want true", gate, got)
		}
	}
}
