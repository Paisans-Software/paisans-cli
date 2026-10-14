package validate

import (
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
)

func (c *checker) accountApps() []string {
	var out []string
	for _, name := range c.cfg.AppNames() {
		if c.cfg.Apps[name].Blueprint == config.BlueprintAccount {
			out = append(out, name)
		}
	}
	return out
}

// inviteApps are the account apps with invites on. Every rule an invite
// community needs reads these; an app whose invites setting is missing or
// not a boolean is refused by accountInvites and held to none of them.
func (c *checker) inviteApps() []string {
	var out []string
	for _, name := range c.accountApps() {
		if blueprints.AccountInvites(c.cfg.Apps[name]) {
			out = append(out, name)
		}
	}
	return out
}

// accountInvites refuses an account app whose invites setting is missing or
// not a boolean, and with invites off refuses what belongs to them: their
// settings and the invite hostname. invites is access policy, like gated, so
// it is declared and never defaulted; it decides whether invite.<domain>
// exists at all.
func (c *checker) accountInvites() {
	for _, name := range c.accountApps() {
		app := c.cfg.Apps[name]
		key := fmt.Sprintf("apps.%s.settings.%s", name, blueprints.AccountInvitesSetting)
		raw, set := app.Settings[blueprints.AccountInvitesSetting]
		if !set {
			c.refuse("account-invites-unset", key,
				"invites must be declared",
				"is missing. Set invites: true for a community that admits new members by invite, which serves the invite pages on invite.%s, or invites: false for one without invites. It is never defaulted, because it decides whether a hostname exists.", c.cfg.Community.Domain)
			continue
		}
		if _, ok := raw.(bool); !ok {
			c.refuse("account-setting-invalid", key,
				"account setting out of range", "is %v. It must be true or false, unquoted.", raw)
			continue
		}
		if blueprints.AccountInvites(app) {
			continue
		}
		for _, k := range blueprints.AccountInviteSettings {
			if _, ok := app.Settings[k]; ok {
				c.refuse("account-invite-settings-without-invites", fmt.Sprintf("apps.%s.settings.%s", name, k),
					"an invite setting with invites off",
					"is set, and invites is false. It belongs to invites and the service is never told it, so it would read as configured and do nothing. Remove it, or set invites: true.")
			}
		}
		if _, ok := app.Hostnames[blueprints.InviteRole]; ok {
			c.refuse("account-invite-hostname-without-invites", fmt.Sprintf("apps.%s.hostnames.%s", name, blueprints.InviteRole),
				"an invite hostname with invites off",
				"is set, and invites is false. The invite hostname exists only with invites on, so nothing would serve it, no DNS record or certificate would be made for it, and the line would read as though one were. Remove it, or set invites: true.")
		}
		c.warn("account-serves-nothing", key,
			"the account app has nothing to serve",
			"is false. Invites are the account service's one feature so far, so with them off it shows a signed in member their name and nothing else. Leave the app out, or set invites: true.")
	}
}

func (c *checker) accountMustBeClustered() {
	for _, name := range c.accountApps() {
		if c.cfg.Apps[name].Placement.Mode != config.PlacementCluster {
			c.refuse("account-must-be-clustered", fmt.Sprintf("apps.%s.placement", name),
				"account must have cluster placement",
				"is not cluster. Invites and applications must survive the loss of a site, so the account service keeps its state in the cluster's Postgres and runs on every apps site. Set placement: cluster.")
		}
	}
}

// accountIdentityApp checks the pocket-id app an account app names, which
// every account app signs in through, and, with invites on, the settings of
// that app the invite flow depends on.
func (c *checker) accountIdentityApp() {
	for _, name := range c.accountApps() {
		idp, _ := c.cfg.Apps[name].Settings["identity_app"].(string)
		app, ok := c.cfg.Apps[idp]
		if !ok || app.Blueprint != config.BlueprintPocketID {
			c.refuse("account-identity-app", fmt.Sprintf("apps.%s.settings.identity_app", name),
				"identity_app must name a pocket-id app",
				"is %q, which is not a pocket-id app in this configuration. The account service creates each approved member's account at the pocket-id app named here, and signs members in through it.", idp)
			continue
		}
		if n := len(c.pocketIDApps()); n > 1 {
			c.refuse("account-one-identity-app", fmt.Sprintf("apps.%s.settings.identity_app", name),
				"an account app needs exactly one pocket-id app",
				"is %q, but this configuration has %d pocket-id apps. The account service's OIDC client is created at the deployment's pocket-id app, while its issuer and API key come from identity_app; with more than one, the two could be different identity providers. Declare one pocket-id app.", idp, n)
			continue
		}
		if !blueprints.AccountInvites(c.cfg.Apps[name]) {
			continue
		}
		s := app.Settings
		if v, set := s["allow_user_signups"]; set && v != "disabled" {
			c.refuse("account-signups-not-disabled", fmt.Sprintf("apps.%s.settings.allow_user_signups", idp),
				"pocket id signups bypass the code of conduct",
				"is %v. With invites on, new accounts come only from invites, after the code of conduct; Pocket ID's own signup links would admit people past it. Set allow_user_signups: disabled or remove it.", v)
		}
		if v := s["email_one_time_access_as_unauthenticated"]; v != true {
			c.refuse("account-recovery-disabled", fmt.Sprintf("apps.%s.settings.email_one_time_access_as_unauthenticated", idp),
				"members could not recover an expired sign-in link",
				"is not true. A new member whose sign-in link expired gets a fresh one from Pocket ID's sign-in page with an email code. Set email_one_time_access_as_unauthenticated: true.")
		}
		if v, set := s["email_one_time_access_as_admin"]; set && v == false {
			c.refuse("account-login-link-disabled", fmt.Sprintf("apps.%s.settings.email_one_time_access_as_admin", idp),
				"approved members could not be sent a sign-in link",
				"is false. The account service has Pocket ID email each approved member a one-time sign-in link, which Pocket ID refuses while email_one_time_access_as_admin is false. Set it true or remove it.")
		}
	}
}

// accountWithoutSMTP refuses an account app with no mail server, invites on
// or off: the service reads ACCOUNT_SMTP_HOST and ACCOUNT_SMTP_FROM as
// required and refuses to start without them.
func (c *checker) accountWithoutSMTP() {
	for _, name := range c.accountApps() {
		if !c.cfg.CanMail(name) {
			c.refuse("account-without-smtp", fmt.Sprintf("apps.%s", name),
				"account needs mail",
				"resolves no SMTP host and from_address. The account service refuses to start without one, and with invites on it emails admins when an application is waiting and tells invitees the outcome. Declare an smtp: block with a host and from_address.")
		}
	}
}

// accountCodeOfConduct refuses a missing, non-string or blank code of
// conduct. The setting is the Markdown text itself, so nothing reads a file.
func (c *checker) accountCodeOfConduct() {
	for _, name := range c.inviteApps() {
		text, ok := c.cfg.Apps[name].Settings["code_of_conduct"].(string)
		if !ok || strings.TrimSpace(text) == "" {
			c.refuse("account-code-of-conduct-missing", fmt.Sprintf("apps.%s.settings.code_of_conduct", name),
				"the code of conduct text is missing",
				"is missing, empty or not text. Every invitee agrees to the code of conduct before an account exists. Put the community's code of conduct in code_of_conduct as a block of text (code_of_conduct: |).")
		}
	}
}

func (c *checker) accountSettingInvalid() {
	for _, name := range c.inviteApps() {
		s := c.cfg.Apps[name].Settings
		for _, key := range sortedKeys(blueprints.AccountSettingRanges) {
			v, set := s[key]
			if !set {
				continue
			}
			rng := blueprints.AccountSettingRanges[key]
			if n, ok := v.(int); !ok || n < rng[0] || n > rng[1] {
				c.refuse("account-setting-invalid", fmt.Sprintf("apps.%s.settings.%s", name, key),
					"account setting out of range",
					"is %v. It must be a whole number from %d to %d.", v, rng[0], rng[1])
			}
		}
		if v, set := s["require_approval"]; set {
			if _, ok := v.(bool); !ok {
				c.refuse("account-setting-invalid", fmt.Sprintf("apps.%s.settings.require_approval", name),
					"account setting out of range", "is %v. It must be true or false, unquoted.", v)
			}
		}
	}
}

// accountSettingUnknown warns about an account setting the blueprint does not
// read, which renders nothing and is most likely a misspelling.
func (c *checker) accountSettingUnknown() {
	for _, name := range c.accountApps() {
		for _, key := range sortedKeys(c.cfg.Apps[name].Settings) {
			if _, ranged := blueprints.AccountSettingRanges[key]; ranged || slices.Contains(blueprints.AccountOtherSettings, key) {
				continue
			}
			c.warn("account-setting-unknown", fmt.Sprintf("apps.%s.settings.%s", name, key),
				"account setting is not read by the toolkit",
				"is not a setting the account blueprint reads, so it renders nothing. The settings are listed in README.md, \"Accounts\".")
		}
	}
}
