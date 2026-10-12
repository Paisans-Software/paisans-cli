package validate_test

import (
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
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

func TestInviteRecoveryUnsetCountsAsOff(t *testing.T) {
	cfg := load(t, "valid")
	app := cfg.Apps["auth"]
	app.Settings["email_one_time_access_as_unauthenticated"] = false
	cfg.Apps["auth"] = app
	if len(findings(validate.Check(cfg), "invite-recovery-disabled")) != 1 {
		t.Fatal("false is not true")
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
	for key, good := range map[string]any{"invite_quota": 100, "login_link_days": 31, "require_approval": false} {
		if len(findings(withInviteSetting(t, key, good), "invite-setting-invalid")) != 0 {
			t.Fatalf("%s=%v is in range", key, good)
		}
	}
}

var _ = config.KindInvite
