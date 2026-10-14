package render_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
)

const (
	accountEnvA = "home-a/srv/paisans/f2a9/account/.env"
	accountCoCA = "home-a/srv/paisans/f2a9/account/code-of-conduct.md"
)

// accountServiceEnv is every variable paisans-account-service's config.Load
// reads, as docs/specs/2026-10-14-account-service.md names them for its
// v0.2.0. It is the service's environment contract: a name the service reads and this
// toolkit does not render is a setting nobody can change, and a required one
// is a container that refuses to start.
var accountServiceEnv = []string{
	"ACCOUNT_LISTEN_ADDR", "ACCOUNT_PUBLIC_URL", "ACCOUNT_INVITES", "ACCOUNT_INVITE_PUBLIC_URL", "ACCOUNT_DATABASE_URL",
	"ACCOUNT_SESSION_KEY", "ACCOUNT_EMAIL_HASH_KEY",
	"ACCOUNT_OIDC_ISSUER", "ACCOUNT_OIDC_CLIENT_ID", "ACCOUNT_OIDC_CLIENT_SECRET",
	"ACCOUNT_MEMBER_GROUP", "ACCOUNT_ADMIN_GROUP",
	"ACCOUNT_IDP_URL", "ACCOUNT_IDP_API_KEY",
	"ACCOUNT_SMTP_HOST", "ACCOUNT_SMTP_PORT", "ACCOUNT_SMTP_USER", "ACCOUNT_SMTP_PASSWORD",
	"ACCOUNT_SMTP_FROM", "ACCOUNT_SMTP_TLS",
	"ACCOUNT_ACCENT_COLOR", "ACCOUNT_INVITE_QUOTA", "ACCOUNT_REQUIRE_APPROVAL",
	"ACCOUNT_INVITE_LIFETIME_DAYS", "ACCOUNT_APPLICATION_WAIT_DAYS", "ACCOUNT_DECLINE_RETENTION_DAYS",
	"ACCOUNT_LOGIN_LINK_DAYS", "ACCOUNT_COC_PATH", "ACCOUNT_TRUST_FORWARDED",
}

// buildAccount renders the fixture with change applied to its configuration
// and secrets first.
func buildAccount(t *testing.T, change func(*config.Config, *config.Secrets)) map[string]string {
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

// withAccountSettings replaces keys in the account app's settings.
func withAccountSettings(set map[string]any) func(*config.Config, *config.Secrets) {
	return func(cfg *config.Config, _ *config.Secrets) {
		app := cfg.Apps["account"]
		settings := map[string]any{}
		for k, v := range app.Settings {
			settings[k] = v
		}
		for k, v := range set {
			settings[k] = v
		}
		app.Settings = settings
		cfg.Apps["account"] = app
	}
}

func TestAccountRendersEveryVariableTheServiceReads(t *testing.T) {
	env := buildAccount(t, nil)[accountEnvA]
	if env == "" {
		t.Fatal("the account app rendered no .env on home-a")
	}
	for _, name := range accountServiceEnv {
		if !assignsEnvVar(env, name) {
			t.Errorf("%s is read by paisans-account-service and not rendered", name)
		}
	}
	for _, line := range strings.Split(env, "\n") {
		name, _, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") || !strings.HasPrefix(name, "ACCOUNT_") {
			continue
		}
		known := false
		for _, want := range accountServiceEnv {
			known = known || want == name
		}
		if !known {
			t.Errorf("%s is rendered but paisans-account-service does not read it", name)
		}
	}
}

func TestAccountEnvFromTheFixture(t *testing.T) {
	env := buildAccount(t, nil)[accountEnvA]
	for key, want := range map[string]string{
		"ACCOUNT_LISTEN_ADDR":            ":8082",
		"ACCOUNT_PUBLIC_URL":             "https://account.example.org",
		"ACCOUNT_DATABASE_URL":           "postgresql://account:fixture-not-a-secret-account-db@10.44.0.1:5000/account",
		"ACCOUNT_SESSION_KEY":            "Zml4dHVyZS1ub3QtYS1zZWNyZXQtaW52aXRlLXNlc3M",
		"ACCOUNT_EMAIL_HASH_KEY":         "Zml4dHVyZS1ub3QtYS1zZWNyZXQtaW52aXRlLWhhc2g",
		"ACCOUNT_OIDC_CLIENT_ID":         "fixture-not-a-secret-account-id",
		"ACCOUNT_OIDC_CLIENT_SECRET":     "fixture-not-a-secret-account-secret",
		"ACCOUNT_MEMBER_GROUP":           "members",
		"ACCOUNT_ADMIN_GROUP":            "admins",
		"ACCOUNT_SMTP_HOST":              "smtp.example.org",
		"ACCOUNT_SMTP_PORT":              "587",
		"ACCOUNT_SMTP_USER":              "robot@example.org",
		"ACCOUNT_SMTP_PASSWORD":          "fixture-not-a-secret-smtp",
		"ACCOUNT_SMTP_FROM":              "hello@example.org",
		"ACCOUNT_SMTP_TLS":               "starttls",
		"ACCOUNT_INVITE_QUOTA":           "5",
		"ACCOUNT_REQUIRE_APPROVAL":       "true",
		"ACCOUNT_INVITE_LIFETIME_DAYS":   "14",
		"ACCOUNT_APPLICATION_WAIT_DAYS":  "14",
		"ACCOUNT_DECLINE_RETENTION_DAYS": "183",
		"ACCOUNT_LOGIN_LINK_DAYS":        "7",
		"ACCOUNT_COC_PATH":               "/etc/paisans-account-service/code-of-conduct.md",
		"ACCOUNT_TRUST_FORWARDED":        "true",
	} {
		if got := envValue(env, key); got != want {
			t.Errorf("%s=%q, want %q", key, got, want)
		}
	}
}

// Every declared setting reaches the service, as the service spells it.
func TestAccountSettingsOverrideTheDefaults(t *testing.T) {
	env := buildAccount(t, withAccountSettings(map[string]any{
		"invite_quota": 100, "require_approval": false, "invite_lifetime_days": 30,
		"application_wait_days": 2, "decline_retention_days": 3650, "login_link_days": 31,
	}))[accountEnvA]
	for key, want := range map[string]string{
		"ACCOUNT_INVITE_QUOTA": "100", "ACCOUNT_REQUIRE_APPROVAL": "false", "ACCOUNT_INVITE_LIFETIME_DAYS": "30",
		"ACCOUNT_APPLICATION_WAIT_DAYS": "2", "ACCOUNT_DECLINE_RETENTION_DAYS": "3650", "ACCOUNT_LOGIN_LINK_DAYS": "31",
	} {
		if got := envValue(env, key); got != want {
			t.Errorf("%s=%q, want %q", key, got, want)
		}
	}
}

// The code of conduct is the setting's text, byte for byte, trailing newline
// or none. Text that looks like a path is still text: nothing reads a file.
func TestAccountCoCRendered(t *testing.T) {
	files := buildAccount(t, nil)
	if got, want := files[accountCoCA], "# Be kind\n\nTo each other.\n"; got != want {
		t.Fatalf("code-of-conduct.md is %q, want the setting's text %q", got, want)
	}
	for _, text := range []string{"no trailing newline", "two trailing\n\n", "code-of-conduct.md"} {
		files := buildAccount(t, withAccountSettings(map[string]any{"code_of_conduct": text}))
		if got := files[accountCoCA]; got != text {
			t.Errorf("code_of_conduct %q rendered as %q", text, got)
		}
	}
}

// The image runs as distroless nonroot (uid 65532), and apply writes every
// file root owned, so the code of conduct has to be readable by others.
func TestAccountCoCIsWorldReadable(t *testing.T) {
	var found int
	for _, f := range build(t).Files {
		if !strings.HasSuffix(f.Path, "/account/code-of-conduct.md") {
			continue
		}
		found++
		if f.Mode != 0o644 {
			t.Errorf("%s is %04o; the service's nonroot user could not read it", f.Path, f.Mode)
		}
	}
	// One per apps site the clustered account app runs on.
	if found != 2 {
		t.Fatalf("%d code-of-conduct.md files rendered, want one on each of home-a and home-b", found)
	}
}

// The issuer, the API and its key all belong to the deployment's one
// pocket-id app. A second is refused by validate (account-one-identity-app),
// so there is exactly one for the values to come from.
func TestAccountReadsTheDeploymentsIdentityApp(t *testing.T) {
	files := buildAccount(t, nil)
	env := files[accountEnvA]
	key := envValue(files[authEnvA], "STATIC_API_KEY")
	if key == "" || envValue(env, "ACCOUNT_IDP_API_KEY") != key {
		t.Errorf("ACCOUNT_IDP_API_KEY is %q, want auth's static key", envValue(env, "ACCOUNT_IDP_API_KEY"))
	}
	for _, name := range []string{"ACCOUNT_IDP_URL", "ACCOUNT_OIDC_ISSUER"} {
		if got := envValue(env, name); got != "https://id.example.org" {
			t.Errorf("%s is %q, want auth's https://id.example.org", name, got)
		}
	}
}

// Pocket ID's accent colour reaches the account pages: empty, so the service
// keeps its own, when the identity app leaves it unset or at `default`.
func TestAccountAccent(t *testing.T) {
	for _, tc := range []struct {
		accent any
		want   string
	}{
		{nil, ""},
		{"default", ""},
		{"#2f6f5e", `"#2f6f5e"`},
		{"rebeccapurple", "rebeccapurple"},
	} {
		files := buildAccount(t, func(cfg *config.Config, _ *config.Secrets) {
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
		env := files[accountEnvA]
		if !assignsEnvVar(env, "ACCOUNT_ACCENT_COLOR") {
			t.Fatalf("accent %v: ACCOUNT_ACCENT_COLOR is not rendered", tc.accent)
		}
		if got := envValue(env, "ACCOUNT_ACCENT_COLOR"); got != tc.want {
			t.Errorf("accent %v: ACCOUNT_ACCENT_COLOR=%s, want %s", tc.accent, got, tc.want)
		}
		if tc.want != "" && envValue(files[authEnvA], "ACCENT_COLOR") != tc.want {
			t.Errorf("accent %v: Pocket ID's ACCENT_COLOR=%s, but account's is %s", tc.accent, envValue(files[authEnvA], "ACCENT_COLOR"), tc.want)
		}
	}
}

// Founder requirement, 2026-10-12: the service learns Pocket ID's address
// only from the identity app's hostname in paisans-stack.yaml, so renaming that
// host changes both values, and they stay equal.
func TestAccountFollowsTheIdentityHostname(t *testing.T) {
	first := buildAccount(t, nil)[accountEnvA]
	second := buildAccount(t, func(cfg *config.Config, _ *config.Secrets) {
		auth := cfg.Apps["auth"]
		auth.Hostname = "login.renamed.example"
		cfg.Apps["auth"] = auth
	})[accountEnvA]
	for _, key := range []string{"ACCOUNT_IDP_URL", "ACCOUNT_OIDC_ISSUER"} {
		if got := envValue(second, key); got != "https://login.renamed.example" {
			t.Errorf("%s = %q after the identity hostname changed", key, got)
		}
		if envValue(first, key) == envValue(second, key) {
			t.Errorf("%s did not change", key)
		}
	}
}

// An account app without either key would be a container that refuses to
// start after the apply has moved, so render refuses first.
func TestAccountWithoutItsKeysIsRefused(t *testing.T) {
	for _, key := range []string{"email_hash_key", "session_key"} {
		cfg, secrets := fixture(t), fixtureSecrets(t)
		delete(secrets.Apps["account"], key)
		_, err := render.Build(cfg, secrets)
		if err == nil || !strings.Contains(err.Error(), "apps.account."+key) {
			t.Errorf("an account app without %s rendered: %v", key, err)
		}
	}
}

// The service is reachable through the gateway on every apps site, with
// nothing in front of it: invitees arrive before they have an account.
func TestAccountIsRoutedToEveryAppsSiteUngated(t *testing.T) {
	var snippet, caddyfile string
	for path, content := range buildAccount(t, nil) {
		if strings.HasPrefix(path, "vm/") && strings.HasSuffix(path, "/account.caddy") {
			snippet = content
		}
		if path == "vm/srv/paisans/f2a9/infra/caddy/Caddyfile" {
			caddyfile = content
		}
	}
	// The gate is placed by the gateway's host block, not the snippet, so the
	// block is where an ungated app shows it.
	block := hostBlock(t, caddyfile, "account.example.org")
	if !strings.Contains(block, "import /etc/caddy/snippets/account.caddy") {
		t.Errorf("the account host block does not import its snippet:\n%s", block)
	}
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "import gate") || strings.HasPrefix(line, "forward_auth") {
			t.Errorf("the account host block is gated: %q\n%s", line, block)
		}
	}
	if snippet == "" {
		t.Fatal("the gateway has no snippet for account")
	}
	for _, want := range []string{"10.44.0.1:", "10.44.0.2:", "import upstream_failover"} {
		if !strings.Contains(snippet, want) {
			t.Errorf("the account snippet lacks %q:\n%s", want, snippet)
		}
	}
	if strings.Contains(snippet, "forward_auth") {
		t.Errorf("the account snippet is gated:\n%s", snippet)
	}
}

// invitesOff turns the account app's invites off and removes every setting
// that belongs to them, as validate requires.
func invitesOff(cfg *config.Config, _ *config.Secrets) {
	app := cfg.Apps["account"]
	settings := map[string]any{}
	for k, v := range app.Settings {
		settings[k] = v
	}
	settings["invites"] = false
	for _, k := range blueprints.AccountInviteSettings {
		delete(settings, k)
	}
	app.Settings = settings
	cfg.Apps["account"] = app
}

// The service is told whether invites are on, and with them off it is told
// nothing that belongs to them.
func TestAccountInvitesReachTheService(t *testing.T) {
	if got := envValue(buildAccount(t, nil)[accountEnvA], "ACCOUNT_INVITES"); got != "true" {
		t.Errorf("ACCOUNT_INVITES=%q with invites on", got)
	}
	files := buildAccount(t, invitesOff)
	env := files[accountEnvA]
	if got := envValue(env, "ACCOUNT_INVITES"); got != "false" {
		t.Errorf("ACCOUNT_INVITES=%q with invites off", got)
	}
	for _, name := range []string{
		"ACCOUNT_INVITE_QUOTA", "ACCOUNT_REQUIRE_APPROVAL", "ACCOUNT_INVITE_LIFETIME_DAYS",
		"ACCOUNT_APPLICATION_WAIT_DAYS", "ACCOUNT_DECLINE_RETENTION_DAYS", "ACCOUNT_LOGIN_LINK_DAYS",
		"ACCOUNT_COC_PATH", "ACCOUNT_INVITE_PUBLIC_URL",
	} {
		if assignsEnvVar(env, name) {
			t.Errorf("%s is rendered with invites off", name)
		}
	}
	if got := files[accountCoCA]; got != "" {
		t.Errorf("code-of-conduct.md is %q with invites off, want empty", got)
	}
}

// With invites on, invitees are served on their own hostname, never gated,
// and its snippet passes only the invite pages and their static files; the
// primary refuses the invite pages.
func TestAccountInviteHostnameWithInvitesOn(t *testing.T) {
	files := buildAccount(t, nil)
	const snippetPath = "vm/srv/paisans/f2a9/infra/caddy/snippets/account-invite.caddy"
	snippet, ok := files[snippetPath]
	if !ok {
		t.Fatalf("%s was not rendered", snippetPath)
	}
	for _, want := range []string{"@invitee path /i/* /static/*", "respond 404", "10.44.0.1:8082", "import upstream_failover"} {
		if !strings.Contains(snippet, want) {
			t.Errorf("the invite snippet lacks %q:\n%s", want, snippet)
		}
	}
	block := hostBlock(t, files["vm/srv/paisans/f2a9/infra/caddy/Caddyfile"], "invite.example.org")
	if !strings.Contains(block, "import /etc/caddy/snippets/account-invite.caddy") {
		t.Errorf("the invite host block does not import its snippet:\n%s", block)
	}
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "import gate") || strings.HasPrefix(line, "forward_auth") {
			t.Errorf("the invite host block is gated: %q", line)
		}
	}
	if primary := files["vm/srv/paisans/f2a9/infra/caddy/snippets/account.caddy"]; !strings.Contains(primary, "@invitepages path /i/*") || !strings.Contains(primary, "respond @invitepages 404") {
		t.Errorf("the primary snippet does not refuse /i/*:\n%s", primary)
	}
	if got := envValue(files[accountEnvA], "ACCOUNT_INVITE_PUBLIC_URL"); got != "https://invite.example.org" {
		t.Errorf("ACCOUNT_INVITE_PUBLIC_URL=%q", got)
	}
	declared := buildAccount(t, func(cfg *config.Config, _ *config.Secrets) {
		app := cfg.Apps["account"]
		app.Hostnames = map[string]string{"invite": "join.example.org"}
		cfg.Apps["account"] = app
	})
	if got := envValue(declared[accountEnvA], "ACCOUNT_INVITE_PUBLIC_URL"); got != "https://join.example.org" {
		t.Errorf("ACCOUNT_INVITE_PUBLIC_URL=%q with hostnames.invite declared", got)
	}
	hostBlock(t, declared["vm/srv/paisans/f2a9/infra/caddy/Caddyfile"], "join.example.org")
}

// With invites off the invite hostname exists nowhere: no host block, no
// snippet, no URL, even with a stale hostnames.invite that validate would
// refuse.
func TestAccountInviteHostnameWithInvitesOff(t *testing.T) {
	for name, change := range map[string]func(*config.Config, *config.Secrets){
		"derived": invitesOff,
		"stale declared": func(cfg *config.Config, s *config.Secrets) {
			invitesOff(cfg, s)
			app := cfg.Apps["account"]
			app.Hostnames = map[string]string{"invite": "invite.example.org"}
			cfg.Apps["account"] = app
		},
	} {
		for path, content := range buildAccount(t, change) {
			if strings.HasSuffix(path, "/account-invite.caddy") {
				t.Errorf("%s: %s rendered with invites off", name, path)
			}
			if strings.Contains(content, "invite.example.org") {
				t.Errorf("%s: %s names invite.example.org with invites off", name, path)
			}
		}
	}
}
