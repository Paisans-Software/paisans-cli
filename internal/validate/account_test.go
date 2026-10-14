package validate_test

import (
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/validate"
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

func TestAccountSignupsUnsetIsFine(t *testing.T) {
	r := validate.Check(load(t, "valid"))
	if f := findings(r, "account-signups-not-disabled"); len(f) != 0 {
		t.Fatalf("unset allow_user_signups is Pocket ID's default, disabled: %v", f)
	}
}

func TestAccountRecoveryMustBeExplicitlyTrue(t *testing.T) {
	for name, set := range map[string]func(map[string]any){
		"false":   func(s map[string]any) { s["email_one_time_access_as_unauthenticated"] = false },
		"deleted": func(s map[string]any) { delete(s, "email_one_time_access_as_unauthenticated") },
	} {
		cfg := load(t, "valid")
		set(cfg.Apps["auth"].Settings)
		if len(findings(validate.Check(cfg), "account-recovery-disabled")) != 1 {
			t.Fatalf("%s is not true and must be refused", name)
		}
	}
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
	if got := findings(r, "account-one-identity-app"); len(got) != 1 || got[0] != "REFUSE apps.account.settings.identity_app" {
		t.Errorf("got %v", got)
	}
}

// withInvitesOff is valid.yaml with the account app's invites false and every
// setting that belongs to invites removed.
func withInvitesOff(t *testing.T) *config.Config {
	t.Helper()
	cfg := load(t, "valid")
	app := cfg.Apps["account"]
	app.Settings["invites"] = false
	for _, k := range blueprints.AccountInviteSettings {
		delete(app.Settings, k)
	}
	cfg.Apps["account"] = app
	return cfg
}

// With invites off, Pocket ID's own settings decide who signs up, so none of
// the rules an invite community needs fires.
func TestInvitesOffLeavesThePocketIDSettingsAlone(t *testing.T) {
	cfg := withInvitesOff(t)
	cfg.Apps["auth"].Settings["allow_user_signups"] = "open"
	delete(cfg.Apps["auth"].Settings, "email_one_time_access_as_unauthenticated")
	r := validate.Check(cfg)
	for _, rule := range []string{"account-signups-not-disabled", "account-recovery-disabled", "account-code-of-conduct-missing", "account-setting-unknown"} {
		if f := findings(r, rule); len(f) != 0 {
			t.Errorf("%s fired with invites off: %v", rule, f)
		}
	}
	if f := findings(r, "account-serves-nothing"); len(f) != 1 || f[0] != "WARN apps.account.settings.invites" {
		t.Errorf("account-serves-nothing: %v", f)
	}
	if r.Refused() {
		t.Errorf("refused: %v", r.Refusals())
	}
}

// invites is a YAML boolean; a quoted one is a typo for something else.
func TestInvitesMustBeABoolean(t *testing.T) {
	r := withAccountSetting(t, "invites", "yes")
	if f := findings(r, "account-setting-invalid"); len(f) != 1 || f[0] != "REFUSE apps.account.settings.invites" {
		t.Errorf("account-setting-invalid: %v", f)
	}
}

// Every setting that belongs to invites is refused with invites off, each by
// its own key.
func TestEveryInviteSettingIsRefusedWithInvitesOff(t *testing.T) {
	for _, k := range blueprints.AccountInviteSettings {
		cfg := withInvitesOff(t)
		cfg.Apps["account"].Settings[k] = 3
		if f := findings(validate.Check(cfg), "account-invite-settings-without-invites"); len(f) != 1 || f[0] != "REFUSE apps.account.settings."+k {
			t.Errorf("%s: %v", k, f)
		}
	}
}
