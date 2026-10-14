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
	"invites", "require_approval", "code_of_conduct", "member_group", "admin_group",
}

// AccountSettingDefaults are what the service is told when a setting is unset.
var AccountSettingDefaults = map[string]string{
	"invite_quota": "3", "require_approval": "true", "invite_lifetime_days": "14",
	"application_wait_days": "14", "decline_retention_days": "183", "login_link_days": "7",
}

// AccountInvitesSetting switches an account app's invites on or off. It is
// access policy, like gated, so it is declared and never defaulted: it
// decides whether invite.<domain> exists at all.
const AccountInvitesSetting = "invites"

// AccountInviteSettings are the settings that belong to invites, valid only
// with invites on.
var AccountInviteSettings = []string{
	"invite_quota", "require_approval", "invite_lifetime_days", "application_wait_days",
	"decline_retention_days", "login_link_days", "code_of_conduct",
}

// AccountInvites reports whether an account app's invites are on: true only
// when settings.invites is the YAML boolean true. validate refuses it unset
// or of another type, so anything else reads as off.
func AccountInvites(app config.App) bool {
	on, _ := app.Settings[AccountInvitesSetting].(bool)
	return app.Blueprint == config.BlueprintAccount && on
}
