package blueprints

import "github.com/paisans-software/paisans-stack/internal/config"

// AccountSettingRanges are the integer settings of the account blueprint and
// their inclusive ranges, from docs/specs/2026-10-12-invite-service.md.
var AccountSettingRanges = map[string][2]int{
	"invite_quota":           {1, 100},
	"invite_lifetime_days":   {1, 365},
	"application_wait_days":  {1, 365},
	"decline_retention_days": {1, 3650},
	"login_link_days":        {1, 31},
}

// AccountOtherSettings are the account settings that are not whole numbers.
var AccountOtherSettings = []string{
	"signup", "require_approval", "code_of_conduct", "member_group", "admin_group",
}

// AccountSettingDefaults are what the service is told when a setting is unset.
var AccountSettingDefaults = map[string]string{
	"invite_quota": "3", "require_approval": "true", "invite_lifetime_days": "14",
	"application_wait_days": "14", "decline_retention_days": "183", "login_link_days": "7",
}

// AccountSignupSetting is how people join an account app's community. It is
// access policy, like gated, so it is declared and never defaulted: it
// decides whether a stranger can apply at all.
const AccountSignupSetting = "signup"

// The values of settings.signup, which the service reads as ACCOUNT_SIGNUP.
const (
	// SignupInvite admits someone holding a member's invite link.
	SignupInvite = "invite"
	// SignupOpen admits anyone, through the registration page on join.
	SignupOpen = "open"
)

// AccountInviteSettings are the settings that belong to invite links, valid
// only with signup: invite. Every other setting applies in both modes.
var AccountInviteSettings = []string{"invite_quota", "invite_lifetime_days"}

// AccountSignup is an account app's settings.signup when it is SignupInvite
// or SignupOpen, and empty otherwise. validate refuses anything else, so an
// empty answer is never rendered.
func AccountSignup(app config.App) string {
	if app.Blueprint != config.BlueprintAccount {
		return ""
	}
	switch v, _ := app.Settings[AccountSignupSetting].(string); v {
	case SignupInvite, SignupOpen:
		return v
	}
	return ""
}
