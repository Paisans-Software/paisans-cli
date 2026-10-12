package validate_test

import (
	"testing"

	"github.com/paisans-software/paisans-stack/internal/validate"
)

// withInviteSetting is valid.yaml with one setting of its invite app changed.
func withInviteSetting(t *testing.T, key string, value any) validate.Result {
	t.Helper()
	cfg := load(t, "valid")
	app := cfg.Apps["invite"]
	app.Settings[key] = value
	cfg.Apps["invite"] = app
	return validate.Check(cfg)
}

func TestInviteSignupsUnsetIsFine(t *testing.T) {
	r := validate.Check(load(t, "valid"))
	if f := findings(r, "invite-signups-not-disabled"); len(f) != 0 {
		t.Fatalf("unset allow_user_signups is Pocket ID's default, disabled: %v", f)
	}
}

func TestInviteRecoveryMustBeExplicitlyTrue(t *testing.T) {
	for name, set := range map[string]func(map[string]any){
		"false":   func(s map[string]any) { s["email_one_time_access_as_unauthenticated"] = false },
		"deleted": func(s map[string]any) { delete(s, "email_one_time_access_as_unauthenticated") },
	} {
		cfg := load(t, "valid")
		set(cfg.Apps["auth"].Settings)
		if len(findings(validate.Check(cfg), "invite-recovery-disabled")) != 1 {
			t.Fatalf("%s is not true and must be refused", name)
		}
	}
}

func TestInviteCodeOfConductIsText(t *testing.T) {
	cfg := load(t, "valid")
	delete(cfg.Apps["invite"].Settings, "code_of_conduct")
	if len(findings(validate.Check(cfg), "invite-code-of-conduct-missing")) != 1 {
		t.Fatal("unset not refused")
	}
	for name, bad := range map[string]any{"empty": "", "whitespace": " \n\t ", "non-string": 42} {
		if len(findings(withInviteSetting(t, "code_of_conduct", bad), "invite-code-of-conduct-missing")) != 1 {
			t.Fatalf("%s not refused", name)
		}
	}
	for name, good := range map[string]any{
		"multi-line": "# Be kind\n\nTreat each other well.\n",
		"path-like":  "code-of-conduct.md",
	} {
		if f := findings(withInviteSetting(t, "code_of_conduct", good), "invite-code-of-conduct-missing"); len(f) != 0 {
			t.Fatalf("%s is text and must be accepted: %v", name, f)
		}
	}
}

func TestInviteWithoutSMTPIsTheOnlyRefusal(t *testing.T) {
	r := validate.Check(load(t, "invite-without-smtp"))
	for _, f := range r.Findings {
		if f.Level == validate.Refuse && f.Rule != "invite-without-smtp" {
			t.Fatalf("unexpected refusal %s", f.Rule)
		}
	}
}

func TestInviteSettingRanges(t *testing.T) {
	for key, bad := range map[string]any{
		"invite_quota": 101, "invite_lifetime_days": 0, "application_wait_days": 366,
		"decline_retention_days": 3651, "login_link_days": 32, "require_approval": "yes",
	} {
		if len(findings(withInviteSetting(t, key, bad), "invite-setting-invalid")) != 1 {
			t.Fatalf("%s=%v not refused", key, bad)
		}
	}
	for name, bad := range map[string]any{"quoted": "7", "float": 7.0} {
		if len(findings(withInviteSetting(t, "login_link_days", bad), "invite-setting-invalid")) != 1 {
			t.Fatalf("%s login_link_days not refused", name)
		}
	}
	for key, good := range map[string]any{"invite_quota": 100, "login_link_days": 31, "require_approval": false} {
		if len(findings(withInviteSetting(t, key, good), "invite-setting-invalid")) != 0 {
			t.Fatalf("%s=%v is in range", key, good)
		}
	}
}

// A misspelt invite setting renders nothing, so it is said; the known ones are not.
func TestAnUnknownInviteSettingIsWarnedAbout(t *testing.T) {
	r := withInviteSetting(t, "invite_qouta", 3)
	if got := findings(r, "invite-setting-unknown"); len(got) != 1 || got[0] != "WARN apps.invite.settings.invite_qouta" {
		t.Errorf("got %v", got)
	}
	if r := validate.Check(load(t, "valid")); r.Has("invite-setting-unknown") {
		t.Errorf("the valid fixture was warned: %v", r.Findings)
	}
}

// The invite app's client is created at the deployment's one pocket-id app,
// so a second pocket-id app is refused.
func TestAnInviteAppWithTwoPocketIDAppsIsRefused(t *testing.T) {
	r := validate.Check(load(t, "invite-one-identity-app"))
	if got := findings(r, "invite-one-identity-app"); len(got) != 1 || got[0] != "REFUSE apps.invite.settings.identity_app" {
		t.Errorf("got %v", got)
	}
}
