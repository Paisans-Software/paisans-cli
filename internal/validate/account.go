package validate

import (
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
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

// accountSignup refuses an account app whose signup setting is missing or
// is neither invite nor open, and under open signup refuses the settings
// that belong to invite links. signup is access policy, like gated, so it is
// declared and never defaulted: it decides whether a stranger can apply.
func (c *checker) accountSignup() {
	for _, name := range c.accountApps() {
		app := c.cfg.Apps[name]
		key := fmt.Sprintf("apps.%s.settings.%s", name, blueprints.AccountSignupSetting)
		raw, set := app.Settings[blueprints.AccountSignupSetting]
		if !set {
			c.refuse("account-signup-unset", key,
				"signup must be declared",
				"is missing. Set signup: invite for a community that admits new members by a member's invite link, or signup: open for one where anyone may apply on join.%s. It is never defaulted, because it decides whether a stranger can apply at all.", c.cfg.Community.Domain)
			continue
		}
		mode := blueprints.AccountSignup(app)
		if mode == "" {
			c.refuse("account-setting-invalid", key,
				"account setting out of range", "is %v. It must be invite or open.", raw)
			continue
		}
		if mode != blueprints.SignupOpen {
			continue
		}
		for _, k := range blueprints.AccountInviteSettings {
			if _, ok := app.Settings[k]; ok {
				c.refuse("account-invite-settings-under-open-signup", fmt.Sprintf("apps.%s.settings.%s", name, k),
					"an invite setting under open signup",
					"is set, and signup is open. It belongs to invite links, which open signup does not have, and the service is never told it, so it would read as configured and do nothing. Remove it, or set signup: invite.")
			}
		}
	}
}

func (c *checker) accountMustBeClustered() {
	for _, name := range c.accountApps() {
		if c.cfg.Apps[name].Placement.Mode != config.PlacementCluster {
			c.refuse("account-must-be-clustered", fmt.Sprintf("apps.%s.placement", name),
				"account must have cluster placement",
				"is not cluster. Invites, applications and bans must survive the loss of a site, so the account service keeps its state in the cluster's Postgres and runs on every apps site. Set placement: cluster.")
		}
	}
}

// accountIdentityProvider checks the pocket-id app every account app signs
// in through and creates accounts at. It is the deployment's one pocket-id
// app, found rather than named: with none there is nothing to sign in to, and
// with more than one the account service's client, issuer and API key could
// come from different identity providers. It also checks the one setting of
// that app every signup depends on that the toolkit does not render itself.
func (c *checker) accountIdentityProvider() {
	for _, name := range c.accountApps() {
		n := len(c.pocketIDApps())
		if n == 0 {
			c.refuse("account-without-pocket-id", fmt.Sprintf("apps.%s", name),
				"an account app needs a pocket-id app",
				"is an account app, and no app has blueprint: pocket-id. The account service signs members in through the deployment's pocket-id app and creates each approved member's account there. Declare one.")
			continue
		}
		if n > 1 {
			c.refuse("account-one-identity-app", fmt.Sprintf("apps.%s", name),
				"an account app needs exactly one pocket-id app",
				"is an account app, and this configuration has %d pocket-id apps. The account service's OIDC client, its issuer and its API key all come from the deployment's pocket-id app; with more than one, they could be different identity providers. Declare one pocket-id app.", n)
			continue
		}
		idp, _ := c.cfg.PocketIDApp()
		if v, set := c.cfg.Apps[idp].Settings["email_one_time_access_as_admin"]; set && v == false {
			c.refuse("account-login-link-disabled", fmt.Sprintf("apps.%s.settings.email_one_time_access_as_admin", idp),
				"approved members could not be sent a sign-in link",
				"is false. The account service has Pocket ID email each approved member a one-time sign-in link, which Pocket ID refuses while email_one_time_access_as_admin is false. Set it true or remove it.")
		}
	}
}

// accountWithoutSMTP refuses an account app with no mail server: the service
// reads ACCOUNT_SMTP_HOST and ACCOUNT_SMTP_FROM as
// required and refuses to start without them.
func (c *checker) accountWithoutSMTP() {
	for _, name := range c.accountApps() {
		if !c.cfg.CanMail(name) {
			c.refuse("account-without-smtp", fmt.Sprintf("apps.%s", name),
				"account needs mail",
				"resolves no SMTP host and from_address. The account service refuses to start without one; it emails admins when an application is waiting and tells applicants the outcome. Declare an smtp: block with a host and from_address.")
		}
	}
}

// accountCodeOfConduct refuses a missing, non-string or blank code of
// conduct, in either signup mode. The setting is the Markdown text itself, so
// nothing reads a file.
func (c *checker) accountCodeOfConduct() {
	for _, name := range c.accountApps() {
		text, ok := c.cfg.Apps[name].Settings["code_of_conduct"].(string)
		if !ok || strings.TrimSpace(text) == "" {
			c.refuse("account-code-of-conduct-missing", fmt.Sprintf("apps.%s.settings.code_of_conduct", name),
				"the code of conduct text is missing",
				"is missing, empty or not text. Everyone who signs up, by invite or by registering, agrees to the code of conduct before an account exists. Put the community's code of conduct in code_of_conduct as a block of text (code_of_conduct: |).")
		}
	}
}

func (c *checker) accountSettingInvalid() {
	for _, name := range c.accountApps() {
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
// read, which renders nothing and is most likely a misspelling. It refuses
// identity_app: the account app uses the deployment's one pocket-id app, and
// a setting that names one would read as a choice that is not made there.
func (c *checker) accountSettingUnknown() {
	for _, name := range c.accountApps() {
		for _, key := range sortedKeys(c.cfg.Apps[name].Settings) {
			if _, ranged := blueprints.AccountSettingRanges[key]; ranged || slices.Contains(blueprints.AccountOtherSettings, key) {
				continue
			}
			if key == "identity_app" {
				c.refuse("account-setting-unknown", fmt.Sprintf("apps.%s.settings.%s", name, key),
					"account setting is not read by the toolkit",
					"is set, and the account app takes no identity_app: it signs in through the deployment's one pocket-id app. Remove it.")
				continue
			}
			c.warn("account-setting-unknown", fmt.Sprintf("apps.%s.settings.%s", name, key),
				"account setting is not read by the toolkit",
				"is not a setting the account blueprint reads, so it renders nothing. The settings are listed in README.md, \"Accounts\".")
		}
	}
}
