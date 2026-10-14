package render_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
)

const (
	accountEnvA = "home-a/srv/paisans/f2a9/account/.env"
	accountCoCA = "home-a/srv/paisans/f2a9/account/code-of-conduct.md"
)

// accountServiceEnv is every variable paisans-account-service's config.Load
// reads at v0.2.0, from its internal/config/config.go and README. It is the
// service's environment contract: a name the service reads and this toolkit
// does not render is a setting nobody can change, and a required one is a
// container that refuses to start.
var accountServiceEnv = []string{
	"ACCOUNT_LISTEN_ADDR", "ACCOUNT_PUBLIC_URL", "ACCOUNT_SIGNUP", "ACCOUNT_JOIN_PUBLIC_URL", "ACCOUNT_OPERATORS",
	"ACCOUNT_DATABASE_URL",
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

// accountInviteEnv are the variables the service reads only with
// ACCOUNT_SIGNUP=invite, so they are rendered only then.
var accountInviteEnv = []string{"ACCOUNT_INVITE_QUOTA", "ACCOUNT_INVITE_LIFETIME_DAYS"}

// Under invite signup every name the service reads is rendered; under open
// every one but the invite link settings. Nothing else with the service's
// prefix is rendered in either.
func TestAccountRendersEveryVariableTheServiceReads(t *testing.T) {
	for mode, change := range map[string]func(*config.Config, *config.Secrets){"invite": nil, "open": openSignup} {
		env := buildAccount(t, change)[accountEnvA]
		if env == "" {
			t.Fatalf("%s: the account app rendered no .env on home-a", mode)
		}
		for _, name := range accountServiceEnv {
			want := mode == "invite" || !slices.Contains(accountInviteEnv, name)
			if got := assignsEnvVar(env, name); got != want {
				t.Errorf("%s: %s rendered is %v, want %v", mode, name, got, want)
			}
		}
		for _, line := range strings.Split(env, "\n") {
			name, _, ok := strings.Cut(line, "=")
			if !ok || strings.HasPrefix(line, "#") || !strings.HasPrefix(name, "ACCOUNT_") {
				continue
			}
			if !slices.Contains(accountServiceEnv, name) {
				t.Errorf("%s: %s is rendered but paisans-account-service does not read it", mode, name)
			}
		}
	}
}

func TestAccountEnvFromTheFixture(t *testing.T) {
	env := buildAccount(t, nil)[accountEnvA]
	for key, want := range map[string]string{
		"ACCOUNT_LISTEN_ADDR":            ":8082",
		"ACCOUNT_PUBLIC_URL":             "https://account.example.org",
		"ACCOUNT_SIGNUP":                 "invite",
		"ACCOUNT_JOIN_PUBLIC_URL":        "https://join.example.org",
		"ACCOUNT_OPERATORS":              "alice,bob",
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
// nothing in front of it: people signing up arrive before they have an account.
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

// openSignup sets the account app's signup to open and removes the settings
// that belong to invite links, as validate requires.
func openSignup(cfg *config.Config, _ *config.Secrets) {
	app := cfg.Apps["account"]
	settings := map[string]any{}
	for k, v := range app.Settings {
		settings[k] = v
	}
	settings["signup"] = "open"
	for _, k := range blueprints.AccountInviteSettings {
		delete(settings, k)
	}
	app.Settings = settings
	cfg.Apps["account"] = app
}

// The service is told the signup mode, and under open signup nothing that
// belongs to invite links; the code of conduct and the settings that apply
// to every signup reach it in both.
func TestAccountSignupReachesTheService(t *testing.T) {
	files := buildAccount(t, openSignup)
	env := files[accountEnvA]
	if got := envValue(env, "ACCOUNT_SIGNUP"); got != "open" {
		t.Errorf("ACCOUNT_SIGNUP=%q under open signup", got)
	}
	for key, want := range map[string]string{
		"ACCOUNT_JOIN_PUBLIC_URL": "https://join.example.org", "ACCOUNT_OPERATORS": "alice,bob",
		"ACCOUNT_REQUIRE_APPROVAL": "true", "ACCOUNT_APPLICATION_WAIT_DAYS": "14", "ACCOUNT_DECLINE_RETENTION_DAYS": "183",
		"ACCOUNT_LOGIN_LINK_DAYS": "7", "ACCOUNT_COC_PATH": "/etc/paisans-account-service/code-of-conduct.md",
	} {
		if got := envValue(env, key); got != want {
			t.Errorf("%s=%q under open signup, want %q", key, got, want)
		}
	}
	if got, want := files[accountCoCA], "# Be kind\n\nTo each other.\n"; got != want {
		t.Errorf("code-of-conduct.md is %q under open signup, want %q", got, want)
	}
}

// ACCOUNT_OPERATORS is the operators section's keys, sorted, whatever order
// the file lists them in.
func TestAccountOperatorsAreSorted(t *testing.T) {
	env := buildAccount(t, func(cfg *config.Config, _ *config.Secrets) {
		cfg.Operators["aaron"] = cfg.Operators["alice"]
	})[accountEnvA]
	if got := envValue(env, "ACCOUNT_OPERATORS"); got != "aaron,alice,bob" {
		t.Errorf("ACCOUNT_OPERATORS=%q", got)
	}
}

// joinSnippet and the rest are the gateway's files for the account app.
const (
	joinSnippet    = "vm/srv/paisans/f2a9/infra/caddy/snippets/account-join.caddy"
	primarySnippet = "vm/srv/paisans/f2a9/infra/caddy/snippets/account.caddy"
	gatewayCaddy   = "vm/srv/paisans/f2a9/infra/caddy/Caddyfile"
)

// In both modes join.<domain> exists, ungated, with its own snippet, and the
// primary refuses the invite pages while still proxying its own / (the
// member's home page).
func TestAccountJoinHostnameInBothModes(t *testing.T) {
	for mode, change := range map[string]func(*config.Config, *config.Secrets){"invite": nil, "open": openSignup} {
		files := buildAccount(t, change)
		if _, ok := files[joinSnippet]; !ok {
			t.Fatalf("%s: %s was not rendered", mode, joinSnippet)
		}
		block := hostBlock(t, files[gatewayCaddy], "join.example.org")
		if !strings.Contains(block, "import /etc/caddy/snippets/account-join.caddy") {
			t.Errorf("%s: the join host block does not import its snippet:\n%s", mode, block)
		}
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "import gate") || strings.HasPrefix(line, "forward_auth") {
				t.Errorf("%s: the join host block is gated: %q", mode, line)
			}
		}
		primary := files[primarySnippet]
		if !strings.Contains(primary, "@invitepages path /i/*") || !strings.Contains(primary, "respond @invitepages 404") {
			t.Errorf("%s: the primary snippet does not refuse /i/*:\n%s", mode, primary)
		}
		if strings.Contains(primary, "path /\n") || strings.Contains(primary, "path / ") {
			t.Errorf("%s: the primary snippet has a rule for bare /, the member's home page:\n%s", mode, primary)
		}
	}
}

// Under invite signup join. passes the invite pages and the static files;
// under open, exactly / for GET and POST and the static files. Everything
// else is a 404 at the gateway.
func TestAccountJoinSnippetPerMode(t *testing.T) {
	invite := buildAccount(t, nil)[joinSnippet]
	for _, want := range []string{"@invitee path /i/* /static/*", "handle @invitee {", "respond 404", "10.44.0.1:8082", "import upstream_failover"} {
		if !strings.Contains(invite, want) {
			t.Errorf("invite: the join snippet lacks %q:\n%s", want, invite)
		}
	}
	if strings.Contains(invite, "@register") {
		t.Errorf("invite: the join snippet passes the registration page:\n%s", invite)
	}
	open := buildAccount(t, openSignup)[joinSnippet]
	for _, want := range []string{"@register {\n\tpath /\n\tmethod GET POST\n}", "handle @register {", "@static path /static/*", "handle @static {", "respond 404", "10.44.0.1:8082", "import upstream_failover"} {
		if !strings.Contains(open, want) {
			t.Errorf("open: the join snippet lacks %q:\n%s", want, open)
		}
	}
	if strings.Contains(open, "/i/") {
		t.Errorf("open: the join snippet passes the invite pages:\n%s", open)
	}
}

// hostnames.join replaces the derived name everywhere it is used.
func TestAccountDeclaredJoinHostname(t *testing.T) {
	declared := buildAccount(t, func(cfg *config.Config, _ *config.Secrets) {
		app := cfg.Apps["account"]
		app.Hostnames = map[string]string{"join": "welcome.example.org"}
		cfg.Apps["account"] = app
	})
	if got := envValue(declared[accountEnvA], "ACCOUNT_JOIN_PUBLIC_URL"); got != "https://welcome.example.org" {
		t.Errorf("ACCOUNT_JOIN_PUBLIC_URL=%q with hostnames.join declared", got)
	}
	hostBlock(t, declared[gatewayCaddy], "welcome.example.org")
	if strings.Contains(declared[gatewayCaddy], "join.example.org") {
		t.Error("the gateway still routes the derived join.example.org")
	}
}
