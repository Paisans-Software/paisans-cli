package render_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
)

const authEnvA = "home-a/srv/paisans/f2a9/auth/.env"
const authEnvB = "home-b/srv/paisans/f2a9/auth/.env"

// renderAuth renders the fixture with the Pocket ID app's settings and smtp
// block replaced, and returns its .env on home-a.
func renderAuth(t *testing.T, settings map[string]any, smtp *config.SMTP, secrets *config.Secrets) string {
	t.Helper()
	cfg := fixture(t)
	app := cfg.Apps["auth"]
	app.Settings = settings
	app.SMTP = smtp
	cfg.Apps["auth"] = app
	if secrets == nil {
		secrets = fixtureSecrets(t)
	}
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	return planFiles(plan)[authEnvA]
}

// The admin page is locked on every Pocket ID, settings or none, and a
// setting left out renders nothing, so Pocket ID's own default holds.
func TestPocketIDAlwaysRendersItsConfigurationLocked(t *testing.T) {
	env := renderAuth(t, map[string]any{"file_backend": "database"}, nil, nil)
	if envValue(env, "UI_CONFIG_DISABLED") != "true" {
		t.Errorf("UI_CONFIG_DISABLED is %q", envValue(env, "UI_CONFIG_DISABLED"))
	}
	for _, name := range []string{"APP_NAME", "SESSION_DURATION", "ALLOW_USER_SIGNUPS", "SIGNUP_DEFAULT_USER_GROUP_IDS", "EMAILS_VERIFIED"} {
		if strings.Contains(env, "\n"+name+"=") {
			t.Errorf("%s is rendered although no setting declares it", name)
		}
	}
}

// Account editing is off on every Pocket ID, whether or not the setting is
// declared: a username feeds other apps' identities, which do not follow a
// rename.
func TestPocketIDAlwaysRendersAccountEditingOff(t *testing.T) {
	for _, settings := range []map[string]any{
		{"file_backend": "database"},
		{"file_backend": "database", "allow_own_account_edit": false},
	} {
		env := renderAuth(t, settings, nil, nil)
		if got := envValue(env, "ALLOW_OWN_ACCOUNT_EDIT"); got != "false" {
			t.Errorf("%v: ALLOW_OWN_ACCOUNT_EDIT is %q", settings, got)
		}
		if n := strings.Count(env, "\nALLOW_OWN_ACCOUNT_EDIT="); n != 1 {
			t.Errorf("%v: ALLOW_OWN_ACCOUNT_EDIT is rendered %d times", settings, n)
		}
	}
}

// Each setting renders its variable, as Pocket ID spells the value, and a
// value compose would read specially is quoted so it arrives as written.
func TestPocketIDSettingsRenderTheirVariables(t *testing.T) {
	env := renderAuth(t, map[string]any{
		"file_backend":                   "database",
		"app_name":                       "Example $ID",
		"session_duration":               10080,
		"home_page_url":                  "/settings/apps",
		"allow_user_signups":             "withToken",
		"require_user_email":             true,
		"email_one_time_access_as_admin": true,
		"email_api_key_expiration":       false,
	}, nil, nil)
	for name, want := range map[string]string{
		"APP_NAME":                               `"Example \$ID"`,
		"SESSION_DURATION":                       "10080",
		"HOME_PAGE_URL":                          "/settings/apps",
		"ALLOW_USER_SIGNUPS":                     "withToken",
		"REQUIRE_USER_EMAIL":                     "true",
		"EMAIL_ONE_TIME_ACCESS_AS_ADMIN_ENABLED": "true",
		"EMAIL_API_KEY_EXPIRATION_ENABLED":       "false",
	} {
		if got := envValue(env, name); got != want {
			t.Errorf("%s=%s, want %s", name, got, want)
		}
	}
}

// Signup groups render as the IDs recorded for them, in the declared order,
// as the JSON array Pocket ID reads. A name with no ID yet is left out and
// named in a comment, and with none resolved the variable is not rendered.
func TestPocketIDSignupGroupsRenderTheirRecordedIDs(t *testing.T) {
	secrets := fixtureSecrets(t)
	secrets.PocketIDGroups = map[string]string{"members": "id-m", "provisional": "id-p"}
	env := renderAuth(t, map[string]any{"signup_default_groups": []any{"provisional", "later", "members"}}, nil, secrets)
	if got := envValue(env, "SIGNUP_DEFAULT_USER_GROUP_IDS"); got != `"[\"id-p\",\"id-m\"]"` {
		t.Errorf("SIGNUP_DEFAULT_USER_GROUP_IDS=%s", got)
	}
	if !strings.Contains(env, "# signup_default_groups names later, with no ID recorded") {
		t.Errorf("the unresolved group is not named:\n%s", env)
	}

	secrets.PocketIDGroups = nil
	env = renderAuth(t, map[string]any{"signup_default_groups": []any{"provisional"}}, nil, secrets)
	if strings.Contains(env, "\nSIGNUP_DEFAULT_USER_GROUP_IDS=") {
		t.Errorf("rendered with no group resolved:\n%s", env)
	}
}

// Pocket ID inherits the deployment's smtp block field by field, takes its
// own password before the shared one, and has no sender name to give.
func TestPocketIDInheritsTheSMTPBlock(t *testing.T) {
	secrets := fixtureSecrets(t)
	secrets.Apps["auth"]["smtp_password"] = "own#pass$word"
	env := renderAuth(t, nil, &config.SMTP{Security: config.SMTPTLS, FromAddress: "id@example.org", FromName: "unused"}, secrets)
	for name, want := range map[string]string{
		"SMTP_HOST":     "smtp.example.org",
		"SMTP_PORT":     "465",
		"SMTP_TLS":      "tls",
		"SMTP_USER":     "robot@example.org",
		"SMTP_PASSWORD": `"own#pass\$word"`,
		"SMTP_FROM":     "id@example.org",
	} {
		if got := envValue(env, name); got != want {
			t.Errorf("%s=%s, want %s", name, got, want)
		}
	}
	if strings.Contains(env, "unused") {
		t.Error("the sender name was rendered somewhere")
	}

	cfg := fixture(t)
	cfg.SMTP = config.SMTP{}
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	if env := planFiles(plan)[authEnvA]; strings.Contains(env, "\nSMTP_") {
		t.Errorf("SMTP rendered with no host declared:\n%s", env)
	}
}

// Every instance of a Pocket ID on more than one apps site renders the same
// configuration: only the site it names and the HAProxy it connects through
// differ.
func TestEveryPocketIDInstanceRendersTheSameConfiguration(t *testing.T) {
	files := planFiles(build(t))
	strip := func(env string) string {
		var out []string
		for _, line := range strings.Split(env, "\n") {
			if strings.HasPrefix(line, "PAISANS_SITE=") || strings.HasPrefix(line, "DB_CONNECTION_STRING=") {
				continue
			}
			out = append(out, line)
		}
		return strings.Join(out, "\n")
	}
	a, b := files[authEnvA], files[authEnvB]
	if a == "" || b == "" {
		t.Fatal("the fixture's Pocket ID does not render on both home sites")
	}
	if strip(a) != strip(b) {
		t.Errorf("the two instances differ beyond the site and the database host:\n%s\n----\n%s", a, b)
	}
}

// file_backend left out renders `database`, the backend validate asks for,
// rather than leaving Pocket ID on its own filesystem default.
func TestPocketIDFileBackendDefaultsToDatabase(t *testing.T) {
	env := renderAuth(t, map[string]any{}, nil, nil)
	if got := envValue(env, "FILE_BACKEND"); got != "database" {
		t.Errorf("FILE_BACKEND is %q", got)
	}
}

// With SMTP, Pocket ID may email an admin a login link unless the settings
// say otherwise: apply's admin step sends one. Without SMTP it renders
// nothing, so Pocket ID's own default, off, holds.
func TestPocketIDAdminEmailDefaultsOnWithSMTP(t *testing.T) {
	smtp := &config.SMTP{Security: config.SMTPTLS, FromAddress: "id@example.org"}
	const name = "EMAIL_ONE_TIME_ACCESS_AS_ADMIN_ENABLED"
	if got := envValue(renderAuth(t, nil, smtp, fixtureSecrets(t)), name); got != "true" {
		t.Errorf("with SMTP and the setting unset, %s=%q, want true", name, got)
	}
	off := map[string]any{"email_one_time_access_as_admin": false}
	if got := envValue(renderAuth(t, off, smtp, fixtureSecrets(t)), name); got != "false" {
		t.Errorf("with the setting false, %s=%q, want false", name, got)
	}

	cfg := fixture(t)
	cfg.SMTP = config.SMTP{}
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	if env := planFiles(plan)[authEnvA]; strings.Contains(env, "\n"+name+"=") {
		t.Errorf("%s rendered with no SMTP:\n%s", name, env)
	}
	// A host with no sender cannot send either.
	cfg = fixture(t)
	cfg.SMTP.FromAddress = ""
	plan, err = render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	if env := planFiles(plan)[authEnvA]; strings.Contains(env, "\n"+name+"=") {
		t.Errorf("%s rendered with no sender:\n%s", name, env)
	}
}

// Under management: paisans the .env closes signup and opens the email
// sign-in code whatever else is declared; under pocket-id each is the
// operator's, and unset renders nothing.
func TestPocketIDEnvFollowsManagement(t *testing.T) {
	env := renderAuth(t, map[string]any{"management": "paisans"}, nil, nil)
	if envValue(env, "ALLOW_USER_SIGNUPS") != "disabled" || envValue(env, "EMAIL_ONE_TIME_ACCESS_AS_UNAUTHENTICATED_ENABLED") != "true" {
		t.Errorf("paisans renders signups %q, email code %q", envValue(env, "ALLOW_USER_SIGNUPS"), envValue(env, "EMAIL_ONE_TIME_ACCESS_AS_UNAUTHENTICATED_ENABLED"))
	}
	env = renderAuth(t, map[string]any{"management": "pocket-id", "allow_user_signups": "open"}, nil, nil)
	if envValue(env, "ALLOW_USER_SIGNUPS") != "open" || strings.Contains(env, "EMAIL_ONE_TIME_ACCESS_AS_UNAUTHENTICATED_ENABLED=") {
		t.Errorf("pocket-id renders:\n%s", env)
	}
}
