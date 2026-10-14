package blueprints

// InviteSettingRanges are the integer settings of the invite kind and their
// inclusive ranges, from docs/specs/2026-10-12-invite-service.md.
var InviteSettingRanges = map[string][2]int{
	"invite_quota":           {1, 100},
	"invite_lifetime_days":   {1, 365},
	"application_wait_days":  {1, 365},
	"decline_retention_days": {1, 3650},
	"login_link_days":        {1, 31},
}

// InviteOtherSettings are the invite settings that are not whole numbers.
var InviteOtherSettings = []string{
	"identity_app", "require_approval", "code_of_conduct", "member_group", "admin_group",
}

// InviteSettingDefaults are what the service is told when a setting is unset.
var InviteSettingDefaults = map[string]string{
	"invite_quota": "3", "require_approval": "true", "invite_lifetime_days": "14",
	"application_wait_days": "14", "decline_retention_days": "183", "login_link_days": "7",
}
