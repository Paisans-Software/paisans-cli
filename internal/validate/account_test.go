package validate_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// withAccountSetting is valid.yaml with one setting of its account app changed.
func withAccountSetting(t *testing.T, key string, value any) validate.Result {
	t.Helper()
	cfg := load(t, "valid")
	app := cfg.Apps["account"]
	app.Settings[key] = value
	cfg.Apps["account"] = app
	return validate.Check(cfg)
}

func TestAccountCodeOfConductIsText(t *testing.T) {
	cfg := load(t, "valid")
	delete(cfg.Apps["account"].Settings, "code_of_conduct")
	if len(findings(validate.Check(cfg), "account-code-of-conduct-missing")) != 1 {
		t.Fatal("unset not refused")
	}
	for name, bad := range map[string]any{"empty": "", "whitespace": " \n\t ", "non-string": 42} {
		if len(findings(withAccountSetting(t, "code_of_conduct", bad), "account-code-of-conduct-missing")) != 1 {
			t.Fatalf("%s not refused", name)
		}
	}
	for name, good := range map[string]any{
		"multi-line": "# Be kind\n\nTreat each other well.\n",
		"path-like":  "code-of-conduct.md",
	} {
		if f := findings(withAccountSetting(t, "code_of_conduct", good), "account-code-of-conduct-missing"); len(f) != 0 {
			t.Fatalf("%s is text and must be accepted: %v", name, f)
		}
	}
}

func TestAccountWithoutSMTPIsTheOnlyRefusal(t *testing.T) {
	r := validate.Check(load(t, "account-without-smtp"))
	for _, f := range r.Findings {
		if f.Level == validate.Refuse && f.Rule != "account-without-smtp" {
			t.Fatalf("unexpected refusal %s", f.Rule)
		}
	}
}

func TestAccountSettingRanges(t *testing.T) {
	for key, bad := range map[string]any{
		"invite_quota": 101, "invite_lifetime_days": 0, "application_wait_days": 366,
		"decline_retention_days": 3651, "login_link_days": 32, "require_approval": "yes",
	} {
		if len(findings(withAccountSetting(t, key, bad), "account-setting-invalid")) != 1 {
			t.Fatalf("%s=%v not refused", key, bad)
		}
	}
	for name, bad := range map[string]any{"quoted": "7", "float": 7.0} {
		if len(findings(withAccountSetting(t, "login_link_days", bad), "account-setting-invalid")) != 1 {
			t.Fatalf("%s login_link_days not refused", name)
		}
	}
	for key, good := range map[string]any{"invite_quota": 100, "login_link_days": 31, "require_approval": false} {
		if len(findings(withAccountSetting(t, key, good), "account-setting-invalid")) != 0 {
			t.Fatalf("%s=%v is in range", key, good)
		}
	}
}

// identity_app is refused: the account app uses the deployment's one
// pocket-id app, so a setting naming one would read as a choice made there.
func TestIdentityAppIsRefused(t *testing.T) {
	r := withAccountSetting(t, "identity_app", "auth")
	if got := findings(r, "account-setting-unknown"); len(got) != 1 || got[0] != "REFUSE apps.account.settings.identity_app" {
		t.Errorf("got %v", got)
	}
}

// A misspelt account setting renders nothing, so it is said; the known ones are not.
func TestAnUnknownAccountSettingIsWarnedAbout(t *testing.T) {
	r := withAccountSetting(t, "invite_qouta", 3)
	if got := findings(r, "account-setting-unknown"); len(got) != 1 || got[0] != "WARN apps.account.settings.invite_qouta" {
		t.Errorf("got %v", got)
	}
	if r := validate.Check(load(t, "valid")); r.Has("account-setting-unknown") {
		t.Errorf("the valid fixture was warned: %v", r.Findings)
	}
}

// The account app's client is created at the deployment's one pocket-id app,
// so a second pocket-id app is refused.
func TestAnAccountAppWithTwoPocketIDAppsIsRefused(t *testing.T) {
	r := validate.Check(load(t, "account-one-identity-app"))
	if got := findings(r, "account-one-identity-app"); len(got) != 1 || got[0] != "REFUSE apps.account" {
		t.Errorf("got %v", got)
	}
}

// withOpenSignup is valid.yaml with the account app's signup open and the
// settings that belong to invite links removed.
func withOpenSignup(t *testing.T) *config.Config {
	t.Helper()
	cfg := load(t, "valid")
	app := cfg.Apps["account"]
	app.Settings["signup"] = "open"
	for _, k := range blueprints.AccountInviteSettings {
		delete(app.Settings, k)
	}
	cfg.Apps["account"] = app
	return cfg
}

// Open signup with the settings that apply in both modes is a valid
// configuration, and the code of conduct is still required.
func TestOpenSignupIsValid(t *testing.T) {
	cfg := withOpenSignup(t)
	for k, v := range map[string]any{"require_approval": false, "application_wait_days": 7, "decline_retention_days": 30, "login_link_days": 3} {
		cfg.Apps["account"].Settings[k] = v
	}
	if r := validate.Check(cfg); r.Refused() {
		t.Errorf("refused: %v", r.Refusals())
	}
	cfg = withOpenSignup(t)
	delete(cfg.Apps["account"].Settings, "code_of_conduct")
	if f := findings(validate.Check(cfg), "account-code-of-conduct-missing"); len(f) != 1 {
		t.Errorf("open signup without a code of conduct: %v", f)
	}
}

// signup is one of two lowercase strings; anything else is a typo, never
// read as either mode.
func TestSignupMustBeInviteOrOpen(t *testing.T) {
	for _, bad := range []any{"Invite", "closed", true, 1} {
		r := withAccountSetting(t, "signup", bad)
		if f := findings(r, "account-setting-invalid"); len(f) != 1 || f[0] != "REFUSE apps.account.settings.signup" {
			t.Errorf("signup %v: %v", bad, f)
		}
	}
}

// Every invite link setting is refused under open signup, each by its own
// key.
func TestEveryInviteSettingIsRefusedUnderOpenSignup(t *testing.T) {
	for _, k := range blueprints.AccountInviteSettings {
		cfg := withOpenSignup(t)
		cfg.Apps["account"].Settings[k] = 3
		if f := findings(validate.Check(cfg), "account-invite-settings-under-open-signup"); len(f) != 1 || f[0] != "REFUSE apps.account.settings."+k {
			t.Errorf("%s: %v", k, f)
		}
	}
}

// The join hostname is claimed like any other in both modes, so one that
// repeats the account app's own hostname, or a derived join.<domain> that
// another app already uses, is refused rather than rendered as two site
// blocks for one name, by the key the operator has to change.
func TestTheJoinHostnameIsClaimed(t *testing.T) {
	cfg := load(t, "valid")
	app := cfg.Apps["account"]
	app.Hostnames = map[string]string{"join": "account.example.org"}
	cfg.Apps["account"] = app
	if f := findings(validate.Check(cfg), "duplicate-hostname"); len(f) != 1 || f[0] != "REFUSE apps.account.hostnames.join" {
		t.Errorf("hostnames.join equal to the primary: %v", f)
	}

	for mode, cfg := range map[string]*config.Config{"invite": load(t, "valid"), "open": withOpenSignup(t)} {
		wiki := cfg.Apps["wiki"]
		wiki.Hostname = "join.example.org"
		cfg.Apps["wiki"] = wiki
		var said string
		for _, f := range validate.Check(cfg).Findings {
			if f.Rule == "duplicate-hostname" {
				said = f.Key + ": " + f.Message
			}
		}
		if !strings.Contains(said, "apps.wiki.hostname") || !strings.Contains(said, "apps.account.hostnames.join, derived from the community's domain") {
			t.Errorf("%s: an app on the derived join hostname: %q", mode, said)
		}
	}
}

// The invite role is gone: a hostnames.invite left in a file is refused,
// never silently dropped.
func TestTheInviteRoleIsRefused(t *testing.T) {
	cfg := load(t, "valid")
	app := cfg.Apps["account"]
	app.Hostnames = map[string]string{"invite": "invite.example.org"}
	cfg.Apps["account"] = app
	if f := findings(validate.Check(cfg), "unknown-hostname-role"); len(f) != 1 {
		t.Errorf("hostnames.invite: %v", f)
	}
}

// The service needs a mail server to start, so it is refused without one in
// either signup mode.
func TestAccountWithoutSMTPIsRefusedUnderOpenSignup(t *testing.T) {
	cfg := withOpenSignup(t)
	cfg.SMTP = config.SMTP{}
	for name, app := range cfg.Apps {
		app.SMTP = nil
		cfg.Apps[name] = app
	}
	if f := findings(validate.Check(cfg), "account-without-smtp"); len(f) != 1 {
		t.Errorf("account-without-smtp under open signup: %v", f)
	}
}
