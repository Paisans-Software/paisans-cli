package render_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
)

const (
	inviteEnvA = "home-a/srv/paisans/f2a9/invite/.env"
	inviteCoCA = "home-a/srv/paisans/f2a9/invite/code-of-conduct.md"
)

// inviteServiceEnv is every variable paisans-invite's config.Load reads,
// copied from internal/config/config.go at Paisans-Software/paisans-invite
// commit 9211c160d74f2e4b1924c84e72c6bd8c504edfb0 (develop, 2026-10-12). It
// is the service's environment contract: a name the service reads and this
// toolkit does not render is a setting nobody can change, and a required one
// is a container that refuses to start.
var inviteServiceEnv = []string{
	"INVITE_LISTEN_ADDR", "INVITE_PUBLIC_URL", "INVITE_DATABASE_URL",
	"INVITE_SESSION_KEY", "INVITE_EMAIL_HASH_KEY",
	"INVITE_OIDC_ISSUER", "INVITE_OIDC_CLIENT_ID", "INVITE_OIDC_CLIENT_SECRET",
	"INVITE_MEMBER_GROUP", "INVITE_ADMIN_GROUP",
	"INVITE_IDP_URL", "INVITE_IDP_API_KEY",
	"INVITE_SMTP_HOST", "INVITE_SMTP_PORT", "INVITE_SMTP_USER", "INVITE_SMTP_PASSWORD",
	"INVITE_SMTP_FROM", "INVITE_SMTP_TLS",
	"INVITE_ACCENT_COLOR", "INVITE_QUOTA", "INVITE_REQUIRE_APPROVAL",
	"INVITE_LIFETIME_DAYS", "INVITE_APPLICATION_WAIT_DAYS", "INVITE_DECLINE_RETENTION_DAYS",
	"INVITE_LOGIN_LINK_DAYS", "INVITE_COC_PATH", "INVITE_TRUST_FORWARDED",
}

// buildInvite renders the fixture with change applied to its configuration
// and secrets first.
func buildInvite(t *testing.T, change func(*config.Config, *config.Secrets)) map[string]string {
	t.Helper()
	cfg, secrets := fixture(t), fixtureSecrets(t)
	if change != nil {
		change(cfg, secrets)
	}
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	return planFiles(plan)
}

// withInviteSettings replaces keys in the invite app's settings.
func withInviteSettings(set map[string]any) func(*config.Config, *config.Secrets) {
	return func(cfg *config.Config, _ *config.Secrets) {
		app := cfg.Apps["invite"]
		settings := map[string]any{}
		for k, v := range app.Settings {
			settings[k] = v
		}
		for k, v := range set {
			settings[k] = v
		}
		app.Settings = settings
		cfg.Apps["invite"] = app
	}
}

func TestInviteRendersEveryVariableTheServiceReads(t *testing.T) {
	env := buildInvite(t, nil)[inviteEnvA]
	if env == "" {
		t.Fatal("the invite app rendered no .env on home-a")
	}
	for _, name := range inviteServiceEnv {
		if !assignsEnvVar(env, name) {
			t.Errorf("%s is read by paisans-invite and not rendered", name)
		}
	}
	for _, line := range strings.Split(env, "\n") {
		name, _, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") || !strings.HasPrefix(name, "INVITE_") {
			continue
		}
		known := false
		for _, want := range inviteServiceEnv {
			known = known || want == name
		}
		if !known {
			t.Errorf("%s is rendered but paisans-invite does not read it", name)
		}
	}
}

func TestInviteEnvFromTheFixture(t *testing.T) {
	env := buildInvite(t, nil)[inviteEnvA]
	for key, want := range map[string]string{
		"INVITE_LISTEN_ADDR":            ":8082",
		"INVITE_PUBLIC_URL":             "https://invite.example.org",
		"INVITE_DATABASE_URL":           "postgresql://invite:fixture-not-a-secret-invite-db@10.44.0.1:5000/invite",
		"INVITE_SESSION_KEY":            "Zml4dHVyZS1ub3QtYS1zZWNyZXQtaW52aXRlLXNlc3M",
		"INVITE_EMAIL_HASH_KEY":         "Zml4dHVyZS1ub3QtYS1zZWNyZXQtaW52aXRlLWhhc2g",
		"INVITE_OIDC_CLIENT_ID":         "fixture-not-a-secret-invite-id",
		"INVITE_OIDC_CLIENT_SECRET":     "fixture-not-a-secret-invite-secret",
		"INVITE_MEMBER_GROUP":           "members",
		"INVITE_ADMIN_GROUP":            "admins",
		"INVITE_SMTP_HOST":              "smtp.example.org",
		"INVITE_SMTP_PORT":              "587",
		"INVITE_SMTP_USER":              "robot@example.org",
		"INVITE_SMTP_PASSWORD":          "fixture-not-a-secret-smtp",
		"INVITE_SMTP_FROM":              "hello@example.org",
		"INVITE_SMTP_TLS":               "starttls",
		"INVITE_QUOTA":                  "5",
		"INVITE_REQUIRE_APPROVAL":       "true",
		"INVITE_LIFETIME_DAYS":          "14",
		"INVITE_APPLICATION_WAIT_DAYS":  "14",
		"INVITE_DECLINE_RETENTION_DAYS": "183",
		"INVITE_LOGIN_LINK_DAYS":        "7",
		"INVITE_COC_PATH":               "/etc/paisans-invite/code-of-conduct.md",
		"INVITE_TRUST_FORWARDED":        "true",
	} {
		if got := envValue(env, key); got != want {
			t.Errorf("%s=%q, want %q", key, got, want)
		}
	}
}

// Every declared setting reaches the service, as the service spells it.
func TestInviteSettingsOverrideTheDefaults(t *testing.T) {
	env := buildInvite(t, withInviteSettings(map[string]any{
		"invite_quota": 100, "require_approval": false, "invite_lifetime_days": 30,
		"application_wait_days": 2, "decline_retention_days": 3650, "login_link_days": 31,
	}))[inviteEnvA]
	for key, want := range map[string]string{
		"INVITE_QUOTA": "100", "INVITE_REQUIRE_APPROVAL": "false", "INVITE_LIFETIME_DAYS": "30",
		"INVITE_APPLICATION_WAIT_DAYS": "2", "INVITE_DECLINE_RETENTION_DAYS": "3650", "INVITE_LOGIN_LINK_DAYS": "31",
	} {
		if got := envValue(env, key); got != want {
			t.Errorf("%s=%q, want %q", key, got, want)
		}
	}
}

// The code of conduct is the setting's text, byte for byte, trailing newline
// or none. Text that looks like a path is still text: nothing reads a file.
func TestInviteCoCRendered(t *testing.T) {
	files := buildInvite(t, nil)
	if got, want := files[inviteCoCA], "# Be kind\n\nTo each other.\n"; got != want {
		t.Fatalf("code-of-conduct.md is %q, want the setting's text %q", got, want)
	}
	for _, text := range []string{"no trailing newline", "two trailing\n\n", "code-of-conduct.md"} {
		files := buildInvite(t, withInviteSettings(map[string]any{"code_of_conduct": text}))
		if got := files[inviteCoCA]; got != text {
			t.Errorf("code_of_conduct %q rendered as %q", text, got)
		}
	}
}

// The image runs as distroless nonroot (uid 65532), and apply writes every
// file root owned, so the code of conduct has to be readable by others.
func TestInviteCoCIsWorldReadable(t *testing.T) {
	for _, f := range build(t).Files {
		if strings.HasSuffix(f.Path, "/invite/code-of-conduct.md") && f.Mode != 0o644 {
			t.Errorf("%s is %04o; the service's nonroot user could not read it", f.Path, f.Mode)
		}
	}
}

// The issuer, the API and its key all belong to the app identity_app names,
// even when another pocket-id app comes first in the configuration.
func TestInviteReadsItsNamedIdentityApp(t *testing.T) {
	files := buildInvite(t, func(cfg *config.Config, secrets *config.Secrets) {
		other := cfg.Apps["auth"]
		other.Hostname = "aardvark.example.org"
		cfg.Apps["aardvark"] = other
		secrets.Apps["aardvark"] = map[string]any{
			"database_password": "fixture-not-a-secret-aardvark-db",
			"encryption_key":    "fixture-not-a-secret-aardvark-encryption",
			"static_api_key":    "fixture-not-a-secret-aardvark-static-api",
			// Every Pocket ID pushes to the fixture's monitor.
			"admins_heartbeat_token": "8888888888888888888888888888abcd",
		}
	})
	env := files[inviteEnvA]
	key := envValue(files[authEnvA], "STATIC_API_KEY")
	if key == "" || envValue(env, "INVITE_IDP_API_KEY") != key {
		t.Errorf("INVITE_IDP_API_KEY is %q, want auth's static key", envValue(env, "INVITE_IDP_API_KEY"))
	}
	for _, name := range []string{"INVITE_IDP_URL", "INVITE_OIDC_ISSUER"} {
		if got := envValue(env, name); got != "https://id.example.org" {
			t.Errorf("%s is %q, want auth's https://id.example.org", name, got)
		}
	}
}

// Pocket ID's accent colour reaches the invite pages: empty, so the service
// keeps its own, when the identity app leaves it unset or at `default`.
func TestInviteAccent(t *testing.T) {
	for _, tc := range []struct {
		accent any
		want   string
	}{
		{nil, ""},
		{"default", ""},
		{"#2f6f5e", `"#2f6f5e"`},
		{"rebeccapurple", "rebeccapurple"},
	} {
		files := buildInvite(t, func(cfg *config.Config, _ *config.Secrets) {
			auth := cfg.Apps["auth"]
			settings := map[string]any{}
			for k, v := range auth.Settings {
				settings[k] = v
			}
			delete(settings, "accent_color")
			if tc.accent != nil {
				settings["accent_color"] = tc.accent
			}
			auth.Settings = settings
			cfg.Apps["auth"] = auth
		})
		env := files[inviteEnvA]
		if !assignsEnvVar(env, "INVITE_ACCENT_COLOR") {
			t.Fatalf("accent %v: INVITE_ACCENT_COLOR is not rendered", tc.accent)
		}
		if got := envValue(env, "INVITE_ACCENT_COLOR"); got != tc.want {
			t.Errorf("accent %v: INVITE_ACCENT_COLOR=%s, want %s", tc.accent, got, tc.want)
		}
		if tc.want != "" && envValue(files[authEnvA], "ACCENT_COLOR") != tc.want {
			t.Errorf("accent %v: Pocket ID's ACCENT_COLOR=%s, but invite's is %s", tc.accent, envValue(files[authEnvA], "ACCENT_COLOR"), tc.want)
		}
	}
}

// Founder requirement, 2026-10-12: the service learns Pocket ID's address
// only from the identity app's hostname in paisans.yaml, so renaming that
// host changes both values, and they stay equal.
func TestInviteFollowsTheIdentityHostname(t *testing.T) {
	first := buildInvite(t, nil)[inviteEnvA]
	second := buildInvite(t, func(cfg *config.Config, _ *config.Secrets) {
		auth := cfg.Apps["auth"]
		auth.Hostname = "login.renamed.example"
		cfg.Apps["auth"] = auth
	})[inviteEnvA]
	for _, key := range []string{"INVITE_IDP_URL", "INVITE_OIDC_ISSUER"} {
		if got := envValue(second, key); got != "https://login.renamed.example" {
			t.Errorf("%s = %q after the identity hostname changed", key, got)
		}
		if envValue(first, key) == envValue(second, key) {
			t.Errorf("%s did not change", key)
		}
	}
}

// An invite app without either key would be a container that refuses to
// start after the apply has moved, so render refuses first.
func TestInviteWithoutItsKeysIsRefused(t *testing.T) {
	for _, key := range []string{"email_hash_key", "session_key"} {
		cfg, secrets := fixture(t), fixtureSecrets(t)
		delete(secrets.Apps["invite"], key)
		_, err := render.Build(cfg, secrets)
		if err == nil || !strings.Contains(err.Error(), "apps.invite."+key) {
			t.Errorf("an invite app without %s rendered: %v", key, err)
		}
	}
}

// The service is reachable through the gateway on every apps site, with
// nothing in front of it: invitees arrive before they have an account.
func TestInviteIsRoutedToEveryAppsSiteUngated(t *testing.T) {
	var snippet string
	for path, content := range buildInvite(t, nil) {
		if strings.HasPrefix(path, "vm/") && strings.HasSuffix(path, "/invite.caddy") {
			snippet = content
		}
	}
	if snippet == "" {
		t.Fatal("the gateway has no snippet for invite")
	}
	for _, want := range []string{"10.44.0.1:", "10.44.0.2:", "import upstream_failover"} {
		if !strings.Contains(snippet, want) {
			t.Errorf("the invite snippet lacks %q:\n%s", want, snippet)
		}
	}
	if strings.Contains(snippet, "forward_auth") {
		t.Errorf("the invite snippet is gated:\n%s", snippet)
	}
}
