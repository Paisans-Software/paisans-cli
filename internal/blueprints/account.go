package blueprints

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
	"identity_app", "require_approval", "code_of_conduct", "member_group", "admin_group",
}

// AccountSettingDefaults are what the service is told when a setting is unset.
var AccountSettingDefaults = map[string]string{
	"invite_quota": "3", "require_approval": "true", "invite_lifetime_days": "14",
	"application_wait_days": "14", "decline_retention_days": "183", "login_link_days": "7",
}
